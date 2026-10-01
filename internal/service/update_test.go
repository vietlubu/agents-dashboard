package service

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vietlubu/agents-dashboard/internal/update"
	"github.com/vietlubu/agents-dashboard/internal/version"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/updater"
)

const (
	updateCurrent = "v26.10.01.001"
	updateLatest  = "v26.10.01.002"
	updateLink    = "https://github.com/vietlubu/agents-dashboard/releases/tag/" + updateLatest
)

type updateHost struct {
	mu      sync.Mutex
	events  []string
	quits   atomic.Int32
	windows atomic.Int32
}

func (h *updateHost) Emit(name string, _ ...any) bool {
	h.mu.Lock()
	h.events = append(h.events, name)
	h.mu.Unlock()
	return true
}
func (*updateHost) OnEvent(string, func(any)) func() { return func() {} }
func (h *updateHost) OpenWindow(updater.WindowOptions) updater.WindowHandle {
	h.windows.Add(1)
	return nil
}
func (h *updateHost) Quit() { h.quits.Add(1) }

func (h *updateHost) assertNoDownload(t *testing.T) {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, name := range h.events {
		if name == updater.EventDownloadStarted || name == updater.EventUpdateReady {
			t.Errorf("check emitted a download/staging event: %s", name)
		}
	}
}

type updateFixture struct {
	mu              sync.Mutex
	tag             string
	apiStatus       int
	payloadStatus   int
	corrupt         bool
	payload         []byte
	asset           string
	started         chan struct{}
	cancelled       chan struct{}
	block           <-chan struct{}
	apiRequests     atomic.Int32
	payloadRequests atomic.Int32
	server          *httptest.Server
}

func newUpdateFixture(t *testing.T) *updateFixture {
	t.Helper()
	asset, err := update.DesktopAsset(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Skip(err)
	}
	f := &updateFixture{tag: updateLatest, asset: asset, payload: updatePayload(t)}
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)
	return f
}

func (f *updateFixture) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	tag, status, payloadStatus, corrupt := f.tag, f.apiStatus, f.payloadStatus, f.corrupt
	started, cancelled, block := f.started, f.cancelled, f.block
	f.mu.Unlock()
	switch r.URL.Path {
	case "/repos/vietlubu/agents-dashboard/releases/latest":
		f.apiRequests.Add(1)
		if started != nil {
			select {
			case started <- struct{}{}:
			default:
			}
		}
		if block != nil {
			select {
			case <-r.Context().Done():
				if cancelled != nil {
					close(cancelled)
				}
				return
			case <-block:
			}
		}
		if status != 0 {
			w.WriteHeader(status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tag_name": tag, "name": tag, "draft": false, "prerelease": false,
			"assets": []map[string]any{
				{"id": 1, "name": f.asset, "size": len(f.payload), "browser_download_url": f.server.URL + "/payload"},
				{"id": 2, "name": "SHA256SUMS", "browser_download_url": f.server.URL + "/checksums"},
			},
		})
	case "/checksums":
		fmt.Fprintf(w, "%x  %s\n", sha256.Sum256(f.payload), f.asset)
	case "/payload":
		f.payloadRequests.Add(1)
		if payloadStatus != 0 {
			w.WriteHeader(payloadStatus)
			return
		}
		if corrupt {
			_, _ = w.Write(bytes.Repeat([]byte("x"), len(f.payload)))
			return
		}
		_, _ = w.Write(f.payload)
	default:
		http.NotFound(w, r)
	}
}

func updatePayload(t *testing.T) []byte {
	t.Helper()
	var data bytes.Buffer
	payload := []byte("new executable")
	switch runtime.GOOS {
	case "darwin":
		archive := zip.NewWriter(&data)
		header := &zip.FileHeader{Name: "Agents Dashboard.app/Contents/MacOS/agents-dashboard"}
		header.SetMode(0o755)
		file, err := archive.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(payload); err != nil {
			t.Fatal(err)
		}
		if err := archive.Close(); err != nil {
			t.Fatal(err)
		}
	case "linux":
		compressed := gzip.NewWriter(&data)
		archive := tar.NewWriter(compressed)
		if err := archive.WriteHeader(&tar.Header{Name: "Agents Dashboard", Mode: 0o755, Size: int64(len(payload))}); err != nil {
			t.Fatal(err)
		}
		if _, err := archive.Write(payload); err != nil {
			t.Fatal(err)
		}
		if err := archive.Close(); err != nil {
			t.Fatal(err)
		}
		if err := compressed.Close(); err != nil {
			t.Fatal(err)
		}
	default:
		data.Write(payload)
	}
	return data.Bytes()
}

type updateHarness struct {
	service  *AppService
	fixture  *updateFixture
	host     *updateHost
	target   string
	root     string
	mu       sync.Mutex
	statuses []UpdateStatus
}

func newUpdateHarness(t *testing.T) *updateHarness {
	t.Helper()
	oldVersion := version.Version
	version.Version = updateCurrent
	t.Cleanup(func() { version.Version = oldVersion })
	h := &updateHarness{fixture: newUpdateFixture(t), host: &updateHost{}, root: t.TempDir()}
	h.target = filepath.Join(h.root, "installation", "Agents Dashboard")
	if runtime.GOOS == "darwin" {
		h.target = filepath.Join(h.root, "installation", "Agents Dashboard.app", "Contents", "MacOS", "agents-dashboard")
	} else if runtime.GOOS == "windows" {
		h.target += ".exe"
	}
	if err := os.MkdirAll(filepath.Dir(h.target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.target, []byte("old executable"), 0o755); err != nil {
		t.Fatal(err)
	}
	provider, err := update.NewGitHubProvider(h.fixture.server.Client(), h.fixture.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	engine := updater.New(h.host)
	if err := engine.Init(updater.Config{CurrentVersion: updateCurrent, Providers: []updater.Provider{provider}, Window: updater.WindowNone}); err != nil {
		t.Fatal(err)
	}
	deps := &Deps{Updater: engine}
	h.service = newAppService(deps, func() (string, error) {
		return update.InstallCapability(h.target, os.TempDir())
	})
	deps.Emit = func(name string, payload any) {
		if name != EventAppUpdate {
			t.Errorf("unexpected service event %s", name)
			return
		}
		status, ok := payload.(UpdateStatus)
		if !ok {
			t.Errorf("update event is not a full snapshot: %T", payload)
			return
		}
		// This must remain safe inside callbacks; emission cannot hold updateMu.
		_ = h.service.UpdateStatus()
		h.mu.Lock()
		h.statuses = append(h.statuses, status)
		h.mu.Unlock()
	}
	t.Cleanup(func() {
		_ = h.service.ServiceShutdown()
		if path := engine.DownloadedPath(); path != "" {
			parent := filepath.Dir(path)
			if !strings.HasPrefix(filepath.Base(parent), "wails-update-") {
				t.Errorf("unexpected staging parent %s", parent)
				return
			}
			if err := os.RemoveAll(parent); err != nil {
				t.Errorf("remove staged fixture: %v", err)
			}
		}
	})
	return h
}

func (h *updateHarness) snapshots() []UpdateStatus {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]UpdateStatus(nil), h.statuses...)
}

func (h *updateHarness) assertOldProcess(t *testing.T) {
	t.Helper()
	if h.host.quits.Load() != 0 || h.host.windows.Load() != 0 {
		t.Fatal("update quit the app or opened a Wails update window")
	}
	data, err := os.ReadFile(h.target)
	if err != nil || string(data) != "old executable" {
		t.Fatalf("old target changed: %q, %v", data, err)
	}
}

func (h *updateHarness) available(t *testing.T) UpdateStatus {
	t.Helper()
	status, err := h.service.CheckForUpdates()
	want := UpdateStatus{Enabled: true, CanInstall: true, State: "available", CurrentVersion: updateCurrent, LatestVersion: updateLatest, ReleaseURL: updateLink}
	if err != nil || status != want {
		t.Fatalf("CheckForUpdates = %+v, %v; want %+v", status, err, want)
	}
	return status
}

func TestUpdateStatusBeforeConfiguration(t *testing.T) {
	h := newUpdateHarness(t)
	engine := h.service.deps.Updater
	h.service.deps.Updater = nil
	h.service.deps.UpdaterDisabledReason = "development"
	h.service = newAppService(h.service.deps, h.service.installCapability)
	want := UpdateStatus{State: "disabled", CurrentVersion: updateCurrent, Reason: "development"}
	if got := h.service.UpdateStatus(); got != want {
		t.Fatalf("disabled status = %+v", got)
	}
	if got, err := h.service.CheckForUpdates(); got != want || err == nil || err.Error() != "automatic updates are unavailable" {
		t.Fatalf("disabled check = %+v, %v", got, err)
	}
	if err := h.service.InstallUpdate(updateLatest); err == nil || err.Error() != "automatic updates are unavailable" {
		t.Fatalf("disabled install = %v", err)
	}
	if err := h.service.ServiceStartup(context.Background(), application.ServiceOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := h.service.ServiceShutdown(); err != nil {
		t.Fatal(err)
	}
	if h.fixture.apiRequests.Load() != 0 {
		t.Fatal("disabled startup reached the update feed")
	}
	h.service.deps.UpdaterDisabledReason = "server"
	want.Reason = "server"
	if got := h.service.UpdateStatus(); got != want {
		t.Fatalf("dynamic disabled reason = %+v", got)
	}
	h.service.deps.Updater = engine
	want = UpdateStatus{Enabled: true, State: "idle", CurrentVersion: updateCurrent}
	if got := h.service.UpdateStatus(); got != want {
		t.Fatalf("post-configuration status = %+v; want %+v", got, want)
	}
}

func TestCheckForUpdatesRequiresConsent(t *testing.T) {
	h := newUpdateHarness(t)
	available := h.available(t)
	want := []UpdateStatus{
		{Enabled: true, State: "checking", CurrentVersion: updateCurrent}, available,
	}
	if got := h.snapshots(); !reflect.DeepEqual(got, want) {
		t.Fatalf("transitions = %+v; want %+v", got, want)
	}
	for _, tag := range []string{"", "26.10.01.002", updateCurrent, "v26.10.01.003"} {
		if err := h.service.InstallUpdate(tag); err == nil || err.Error() != "update version changed; check again" {
			t.Errorf("InstallUpdate(%q) = %v", tag, err)
		}
	}
	if h.fixture.payloadRequests.Load() != 0 || h.service.deps.Updater.DownloadedPath() != "" {
		t.Fatal("checking or stale confirmation downloaded/staged a payload")
	}
	h.host.assertNoDownload(t)
	h.assertOldProcess(t)
}

func TestCheckForUpdatesClearsStaleCandidate(t *testing.T) {
	for _, statusCode := range []int{http.StatusNotFound, http.StatusForbidden} {
		t.Run(fmt.Sprint(statusCode), func(t *testing.T) {
			h := newUpdateHarness(t)
			h.available(t)
			h.fixture.mu.Lock()
			h.fixture.apiStatus = statusCode
			h.fixture.mu.Unlock()
			status, err := h.service.CheckForUpdates()
			want := UpdateStatus{Enabled: true, State: "up-to-date", CurrentVersion: updateCurrent}
			if statusCode == http.StatusForbidden {
				if err == nil {
					t.Fatal("HTTP failure was reported as success")
				}
				want.State, want.Error = "error", err.Error()
			} else if err != nil {
				t.Fatal(err)
			}
			if status != want || h.service.UpdateStatus() != want {
				t.Fatalf("stale candidate survived: %+v; want %+v", status, want)
			}
			if err := h.service.InstallUpdate(updateLatest); err == nil || err.Error() != "update version changed; check again" {
				t.Fatalf("stale install = %v", err)
			}
			if h.fixture.payloadRequests.Load() != 0 {
				t.Fatal("stale Wails pending release was downloaded")
			}
			snapshots := h.snapshots()
			if !reflect.DeepEqual(snapshots[2:], []UpdateStatus{{Enabled: true, State: "checking", CurrentVersion: updateCurrent}, want}) {
				t.Fatalf("check reset transitions = %+v", snapshots[2:])
			}
			h.assertOldProcess(t)
		})
	}
}

func TestUpdateOperationsRejectBusy(t *testing.T) {
	h := newUpdateHarness(t)
	started, release := make(chan struct{}, 1), make(chan struct{})
	h.fixture.mu.Lock()
	h.fixture.started, h.fixture.block = started, release
	h.fixture.mu.Unlock()
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	done := make(chan error, 1)
	go func() { _, err := h.service.CheckForUpdates(); done <- err }()
	awaitUpdateSignal(t, started)
	if status, err := h.service.CheckForUpdates(); err == nil || err.Error() != "update operation already running" || status.State != "checking" {
		t.Fatalf("busy check = %+v, %v", status, err)
	}
	if err := h.service.InstallUpdate(updateLatest); err == nil || err.Error() != "update operation already running" {
		t.Fatalf("busy install = %v", err)
	}
	h.service.autoCheckForUpdates(context.Background())
	if h.fixture.apiRequests.Load() != 1 {
		t.Fatal("busy automatic check did not skip")
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("check did not complete")
	}
	h.assertOldProcess(t)
}

func TestInstallUpdateCorruptOrFailedDownload(t *testing.T) {
	for _, failure := range []string{"checksum", "http"} {
		t.Run(failure, func(t *testing.T) {
			h := newUpdateHarness(t)
			available := h.available(t)
			h.fixture.mu.Lock()
			if failure == "checksum" {
				h.fixture.corrupt = true
			} else {
				h.fixture.payloadStatus = http.StatusBadGateway
			}
			h.fixture.mu.Unlock()
			for attempt := 1; attempt <= 2; attempt++ {
				err := h.service.InstallUpdate(updateLatest)
				if err == nil {
					t.Fatal("unsafe download succeeded")
				}
				if failure == "checksum" && !strings.Contains(err.Error(), "digest mismatch") {
					t.Fatalf("wrong checksum error: %v", err)
				}
				available.Error = err.Error()
				if status := h.service.UpdateStatus(); status != available {
					t.Fatalf("retry status = %+v; want %+v", status, available)
				}
				if h.fixture.payloadRequests.Load() != int32(attempt) || h.service.deps.Updater.DownloadedPath() != "" {
					t.Fatal("retry did not redownload safely or left a staged update")
				}
				snapshots := h.snapshots()
				installing := available
				installing.State, installing.Error = "installing", ""
				if !reflect.DeepEqual(snapshots[len(snapshots)-2:], []UpdateStatus{installing, available}) {
					t.Fatalf("failed install transitions = %+v", snapshots)
				}
				h.assertOldProcess(t)
			}
		})
	}
}

func makeUpdateReadOnly(t *testing.T, h *updateHarness) {
	t.Helper()
	path := filepath.Join(h.root, "installation")
	mode := os.FileMode(0o555)
	if runtime.GOOS == "windows" {
		path, mode = h.target, 0o444
	}
	t.Cleanup(func() {
		if err := os.Chmod(path, 0o755); err != nil {
			t.Errorf("restore fixture permissions: %v", err)
		}
	})
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func TestUpdatePreflightBlocksBeforeDownload(t *testing.T) {
	h := newUpdateHarness(t)
	h.available(t)
	makeUpdateReadOnly(t, h)
	if err := h.service.InstallUpdate(updateLatest); err == nil {
		t.Fatal("read-only installation was permitted")
	}
	want := UpdateStatus{Enabled: true, State: "available", CurrentVersion: updateCurrent, LatestVersion: updateLatest, ReleaseURL: updateLink, Reason: "not-writable"}
	if got := h.service.UpdateStatus(); got != want {
		t.Fatalf("blocked preflight = %+v; want %+v", got, want)
	}
	if h.fixture.payloadRequests.Load() != 0 {
		t.Fatal("preflight failure downloaded a payload")
	}
	h.assertOldProcess(t)
}

func TestCheckBlockedCapabilityStillShowsRelease(t *testing.T) {
	h := newUpdateHarness(t)
	makeUpdateReadOnly(t, h)
	status, err := h.service.CheckForUpdates()
	want := UpdateStatus{Enabled: true, State: "available", CurrentVersion: updateCurrent, LatestVersion: updateLatest, ReleaseURL: updateLink, Reason: "not-writable"}
	if err != nil || status != want {
		t.Fatalf("blocked discovery = %+v, %v; want %+v", status, err, want)
	}
	if err := h.service.InstallUpdate(updateLatest); err == nil {
		t.Fatal("known blocked installation was permitted")
	}
	if h.fixture.payloadRequests.Load() != 0 {
		t.Fatal("blocked installation downloaded a payload")
	}
}

func TestUpdatePreflightRechecksBeforeRestart(t *testing.T) {
	h := newUpdateHarness(t)
	checks := 0
	h.service.installCapability = func() (string, error) {
		checks++
		if checks == 3 {
			makeUpdateReadOnly(t, h)
		}
		return update.InstallCapability(h.target, os.TempDir())
	}
	h.available(t)
	if err := h.service.InstallUpdate(updateLatest); err == nil {
		t.Fatal("post-download read-only installation was permitted")
	}
	if checks != 3 || h.fixture.payloadRequests.Load() != 1 || h.service.deps.Updater.DownloadedPath() == "" {
		t.Fatal("both preflights did not surround a real staged download")
	}
	want := UpdateStatus{Enabled: true, State: "available", CurrentVersion: updateCurrent, LatestVersion: updateLatest, ReleaseURL: updateLink, Reason: "not-writable"}
	if status := h.service.UpdateStatus(); status != want {
		t.Fatalf("post-download preflight = %+v; want %+v", status, want)
	}
	h.assertOldProcess(t)
}

func TestUnexpectedUpdatePreflightError(t *testing.T) {
	for _, phase := range []string{"check", "before download", "before restart"} {
		t.Run(phase, func(t *testing.T) {
			h := newUpdateHarness(t)
			checks := 0
			failAt := map[string]int{"check": 1, "before download": 2, "before restart": 3}[phase]
			h.service.installCapability = func() (string, error) {
				checks++
				staging := os.TempDir()
				if checks == failAt {
					staging = filepath.Join(h.root, "missing-staging")
				}
				// Windows intentionally ignores staging filesystem, so a missing
				// executable supplies the same real unexpected preflight failure.
				target := h.target
				if runtime.GOOS == "windows" && checks == failAt {
					target = filepath.Join(h.root, "missing-executable")
				}
				return update.InstallCapability(target, staging)
			}
			var err error
			if phase == "check" {
				_, err = h.service.CheckForUpdates()
			} else {
				h.available(t)
				err = h.service.InstallUpdate(updateLatest)
			}
			if err == nil {
				t.Fatal("unexpected preflight failure was hidden")
			}
			want := UpdateStatus{Enabled: true, State: "error", CurrentVersion: updateCurrent, LatestVersion: updateLatest, ReleaseURL: updateLink, Error: err.Error()}
			if got := h.service.UpdateStatus(); got != want {
				t.Fatalf("unexpected preflight = %+v; want %+v", got, want)
			}
			if err := h.service.InstallUpdate(updateLatest); err == nil || err.Error() != "update version changed; check again" {
				t.Fatalf("error state allowed stale install: %v", err)
			}
			wantDownloads := int32(0)
			if phase == "before restart" {
				wantDownloads = 1
			}
			if h.fixture.payloadRequests.Load() != wantDownloads {
				t.Fatalf("payload requests = %d; want %d", h.fixture.payloadRequests.Load(), wantDownloads)
			}
			if checks != failAt {
				t.Fatalf("capability attempts = %d; want %d", checks, failAt)
			}
			h.assertOldProcess(t)
		})
	}
}

func TestAutomaticUpdateCheckSkipsPendingAndBusy(t *testing.T) {
	h := newUpdateHarness(t)
	available := h.available(t)
	for _, state := range []string{"available", "installing", "restarting"} {
		status := available
		status.State = state
		h.service.setUpdateStatus(status)
		h.service.autoCheckForUpdates(context.Background())
		if h.service.UpdateStatus() != status {
			t.Fatalf("automatic check disturbed %s", state)
		}
	}
	h.service.updateOperation.Lock()
	h.service.autoCheckForUpdates(context.Background())
	h.service.updateOperation.Unlock()
	if h.fixture.apiRequests.Load() != 1 || h.fixture.payloadRequests.Load() != 0 {
		t.Fatal("automatic check did not skip pending/busy operations")
	}
	if _, err := h.service.CheckForUpdates(); err == nil || err.Error() != "update operation already running" {
		t.Fatalf("manual check while restarting = %v", err)
	}
}

func awaitUpdateSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatal("update operation did not reach fixture")
	}
}

func TestUpdateLoopStartsImmediatelyAndShutdownJoins(t *testing.T) {
	h := newUpdateHarness(t)
	started, cancelled, blocked := make(chan struct{}, 1), make(chan struct{}), make(chan struct{})
	h.fixture.mu.Lock()
	h.fixture.started, h.fixture.cancelled, h.fixture.block = started, cancelled, blocked
	h.fixture.mu.Unlock()
	if err := h.service.ServiceStartup(context.Background(), application.ServiceOptions{}); err != nil {
		t.Fatal(err)
	}
	awaitUpdateSignal(t, started)
	h.service.updateMu.Lock()
	loopDone := h.service.updateDone
	h.service.updateMu.Unlock()
	if err := h.service.ServiceStartup(context.Background(), application.ServiceOptions{}); err != nil {
		t.Fatal(err)
	}
	shutdown := make(chan error, 1)
	go func() { shutdown <- h.service.ServiceShutdown() }()
	select {
	case err := <-shutdown:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not cancel/join check")
	}
	awaitUpdateSignal(t, cancelled)
	select {
	case <-loopDone:
	default:
		t.Fatal("shutdown returned before loop finished")
	}
	if h.fixture.apiRequests.Load() != 1 || h.fixture.payloadRequests.Load() != 0 {
		t.Fatal("startup duplicated loop or downloaded payload")
	}
	status := h.service.UpdateStatus()
	if status.State != "error" || status.Error == "" || status.CanInstall || status.LatestVersion != "" {
		t.Fatalf("cancelled check status = %+v", status)
	}
	if err := h.service.ServiceShutdown(); err != nil {
		t.Fatal(err)
	}
	h.assertOldProcess(t)
}

func TestUpdateLoopStartupFindsReleaseWithoutDownload(t *testing.T) {
	h := newUpdateHarness(t)
	available := make(chan struct{}, 1)
	emit := h.service.deps.Emit
	h.service.deps.Emit = func(name string, payload any) {
		emit(name, payload)
		if status, ok := payload.(UpdateStatus); ok && status.State == "available" {
			available <- struct{}{}
		}
	}
	if err := h.service.ServiceStartup(context.Background(), application.ServiceOptions{}); err != nil {
		t.Fatal(err)
	}
	awaitUpdateSignal(t, available)
	if err := h.service.ServiceShutdown(); err != nil {
		t.Fatal(err)
	}
	want := UpdateStatus{Enabled: true, CanInstall: true, State: "available", CurrentVersion: updateCurrent, LatestVersion: updateLatest, ReleaseURL: updateLink}
	if got := h.service.UpdateStatus(); got != want {
		t.Fatalf("startup release = %+v; want %+v", got, want)
	}
	if !reflect.DeepEqual(h.snapshots(), []UpdateStatus{{Enabled: true, State: "idle", CurrentVersion: updateCurrent}, {Enabled: true, State: "checking", CurrentVersion: updateCurrent}, want}) {
		t.Fatalf("startup snapshots = %+v", h.snapshots())
	}
	if h.fixture.payloadRequests.Load() != 0 || h.service.deps.Updater.DownloadedPath() != "" {
		t.Fatal("startup downloaded/staged a payload")
	}
	h.host.assertNoDownload(t)
	h.assertOldProcess(t)
}

func TestInstallUpdateRestartFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("running executables cannot be unlinked on Windows")
	}
	const marker = "AGENTS_DASHBOARD_TEST_RESTART_FAILURE"
	if os.Getenv(marker) == "1" {
		h := newUpdateHarness(t)
		checks := 0
		h.service.installCapability = func() (string, error) {
			checks++
			if checks == 3 {
				self, err := os.Executable()
				if err != nil {
					t.Fatal(err)
				}
				// Only the isolated subprocess copy is removed; Restart must fail
				// to spawn it, never Quit or touch the real installation fixture.
				if err := os.Remove(self); err != nil {
					t.Fatal(err)
				}
			}
			return update.InstallCapability(h.target, os.TempDir())
		}
		h.available(t)
		err := h.service.InstallUpdate(updateLatest)
		if err == nil {
			t.Fatal("missing helper executable did not fail Restart")
		}
		want := UpdateStatus{Enabled: true, CanInstall: true, State: "available", CurrentVersion: updateCurrent, LatestVersion: updateLatest, ReleaseURL: updateLink, Error: err.Error()}
		if status := h.service.UpdateStatus(); status != want {
			t.Fatalf("restart failure = %+v; want %+v", status, want)
		}
		snapshots := h.snapshots()
		var states []string
		for _, snapshot := range snapshots {
			states = append(states, snapshot.State)
		}
		if !reflect.DeepEqual(states, []string{"checking", "available", "installing", "restarting", "available"}) {
			t.Fatalf("restart transitions = %v", states)
		}
		h.assertOldProcess(t)
		return
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(self)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	copyPath := filepath.Join(t.TempDir(), "update-service.test")
	copyFile, err := os.OpenFile(copyPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	_, copyErr := io.Copy(copyFile, source)
	closeErr := copyFile.Close()
	if copyErr != nil || closeErr != nil {
		t.Fatalf("copy test executable: %v, %v", copyErr, closeErr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, copyPath, "-test.run=^TestInstallUpdateRestartFailure$")
	command.Env = append(os.Environ(), marker+"=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("isolated restart failure: %v\n%s", err, output)
	}
}
