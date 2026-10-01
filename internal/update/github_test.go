package update

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/wailsapp/wails/v3/pkg/updater"
)

var fixturePayload = []byte("desktop release payload")

type githubFixture struct {
	tag             string
	assets          []string
	checksum        string
	apiStatus       int
	checksumStatus  int
	malformed       bool
	apiRequests     atomic.Int32
	payloadRequests atomic.Int32
}

func newGitHubFixture(t *testing.T, fixture *githubFixture) updater.Provider {
	t.Helper()
	var baseURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/vietlubu/agents-dashboard/releases/latest":
			fixture.apiRequests.Add(1)
			if fixture.apiStatus != 0 {
				w.WriteHeader(fixture.apiStatus)
				return
			}
			if fixture.malformed {
				fmt.Fprint(w, "{invalid json")
				return
			}
			assets := make([]map[string]any, 0, len(fixture.assets))
			for i, name := range fixture.assets {
				assets = append(assets, map[string]any{
					"id": i + 1, "name": name, "size": len(fixturePayload),
					"content_type":         "application/octet-stream",
					"browser_download_url": baseURL + "/assets/" + name,
				})
			}
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(map[string]any{
				"tag_name": fixture.tag, "name": fixture.tag,
				"html_url": "https://github.com/vietlubu/agents-dashboard/releases/tag/" + fixture.tag,
				"draft":    false, "prerelease": false, "assets": assets,
			}); err != nil {
				t.Errorf("encode release: %v", err)
			}
		case "/assets/SHA256SUMS":
			if fixture.checksumStatus != 0 {
				w.WriteHeader(fixture.checksumStatus)
				return
			}
			fmt.Fprint(w, fixture.checksum)
		default:
			if strings.HasPrefix(r.URL.Path, "/assets/") {
				fixture.payloadRequests.Add(1)
				w.Write(fixturePayload)
				return
			}
			http.NotFound(w, r)
		}
	}))
	baseURL = server.URL
	t.Cleanup(server.Close)
	provider, err := NewGitHubProvider(server.Client(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func checksumLine(name string) string {
	return fmt.Sprintf("%x  %s\n", sha256.Sum256(fixturePayload), name)
}

func TestGitHubProviderDesktopTargets(t *testing.T) {
	for _, tc := range []struct {
		platform, arch, name, server string
	}{
		{"darwin", "arm64", "agents-dashboard-darwin-arm64.zip", "agents-dashboard-server-darwin-arm64.tar.gz"},
		{"windows", "amd64", "agents-dashboard-windows-amd64.exe", "agents-dashboard-server-windows-amd64.zip"},
		{"linux", "amd64", "agents-dashboard-linux-amd64.tar.gz", "agents-dashboard-server-linux-amd64.tar.gz"},
		{"linux", "arm64", "agents-dashboard-linux-arm64.tar.gz", "agents-dashboard-server-linux-arm64.tar.gz"},
	} {
		t.Run(tc.platform+"/"+tc.arch, func(t *testing.T) {
			fixture := &githubFixture{
				tag:      "v26.10.01.002",
				assets:   []string{tc.server, "agents-dashboard-windows-amd64-installer.exe", tc.name, "SHA256SUMS"},
				checksum: checksumLine(tc.server) + checksumLine(tc.name),
			}
			provider := newGitHubFixture(t, fixture)
			release, err := provider.Check(context.Background(), updater.CheckRequest{
				CurrentVersion: "v26.10.01.001", Platform: tc.platform, Arch: tc.arch,
			})
			if err != nil || release == nil {
				t.Fatalf("Check = %v, %v", release, err)
			}
			if release.Version != "26.10.01.002" || release.Artifact.Filename != tc.name {
				t.Fatalf("wrong release: %+v", release)
			}
			digest := sha256.Sum256(fixturePayload)
			if release.Verification == nil || release.Verification.DigestAlgo != "sha256" || !bytes.Equal(release.Verification.Digest, digest[:]) {
				t.Fatalf("wrong verification: %+v", release.Verification)
			}
			if fixture.payloadRequests.Load() != 0 {
				t.Fatal("checking downloaded a payload")
			}
			if provider.Name() != "github" {
				t.Fatalf("Name = %q", provider.Name())
			}
			var payload bytes.Buffer
			var written int64
			if err := provider.Download(context.Background(), release, &payload, func(n, total int64) {
				written = n
				if total != int64(len(fixturePayload)) {
					t.Errorf("download total = %d", total)
				}
			}); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(payload.Bytes(), fixturePayload) || written != int64(len(fixturePayload)) || fixture.payloadRequests.Load() != 1 {
				t.Fatal("explicit download did not return the desktop payload with progress")
			}
		})
	}
}

func TestGitHubProviderRejectsUnsafeReleases(t *testing.T) {
	const name = "agents-dashboard-linux-amd64.tar.gz"
	for _, tc := range []struct {
		name   string
		change func(*githubFixture, *updater.CheckRequest)
		noAPI  bool
	}{
		{"development current", func(f *githubFixture, r *updater.CheckRequest) { r.CurrentVersion = "dev" }, true},
		{"malformed current", func(f *githubFixture, r *updater.CheckRequest) { r.CurrentVersion = "v26.02.29.001" }, true},
		{"empty current", func(f *githubFixture, r *updater.CheckRequest) { r.CurrentVersion = "" }, true},
		{"unsupported arch", func(f *githubFixture, r *updater.CheckRequest) { r.Arch = "386" }, true},
		{"unsupported platform", func(f *githubFixture, r *updater.CheckRequest) { r.Platform = "freebsd" }, true},
		{"unsupported windows arm", func(f *githubFixture, r *updater.CheckRequest) { r.Platform, r.Arch = "windows", "arm64" }, true},
		{"unsupported macOS Intel", func(f *githubFixture, r *updater.CheckRequest) { r.Platform, r.Arch = "darwin", "amd64" }, true},
		{"missing payload", func(f *githubFixture, r *updater.CheckRequest) {
			f.assets = []string{"agents-dashboard-server-linux-amd64.tar.gz", "SHA256SUMS"}
		}, false},
		{"wrong architecture payload", func(f *githubFixture, r *updater.CheckRequest) {
			f.assets = []string{"agents-dashboard-linux-arm64.tar.gz", "SHA256SUMS"}
		}, false},
		{"missing sidecar", func(f *githubFixture, r *updater.CheckRequest) { f.assets = []string{name} }, false},
		{"missing checksum entry", func(f *githubFixture, r *updater.CheckRequest) {
			f.checksum = checksumLine("agents-dashboard-server-linux-amd64.tar.gz")
		}, false},
		{"short digest", func(f *githubFixture, r *updater.CheckRequest) { f.checksum = "abcd  " + name + "\n" }, false},
		{"long digest", func(f *githubFixture, r *updater.CheckRequest) {
			f.checksum = strings.Repeat("ab", 33) + "  " + name + "\n"
		}, false},
		{"nonhex digest", func(f *githubFixture, r *updater.CheckRequest) { f.checksum = "nothex  " + name + "\n" }, false},
		{"invalid tag date", func(f *githubFixture, r *updater.CheckRequest) { f.tag = "v26.02.29.001" }, false},
		{"invalid tag serial", func(f *githubFixture, r *updater.CheckRequest) { f.tag = "v26.10.01.000" }, false},
		{"invalid tag grammar", func(f *githubFixture, r *updater.CheckRequest) { f.tag = "v1.2.3" }, false},
		{"uppercase tag prefix", func(f *githubFixture, r *updater.CheckRequest) { f.tag = "V26.10.01.002" }, false},
		{"empty tag", func(f *githubFixture, r *updater.CheckRequest) { f.tag = "" }, false},
		{"bare tag prefix", func(f *githubFixture, r *updater.CheckRequest) { f.tag = "v" }, false},
		{"rate limit", func(f *githubFixture, r *updater.CheckRequest) { f.apiStatus = http.StatusForbidden }, false},
		{"server failure", func(f *githubFixture, r *updater.CheckRequest) { f.apiStatus = http.StatusInternalServerError }, false},
		{"sidecar HTTP failure", func(f *githubFixture, r *updater.CheckRequest) { f.checksumStatus = http.StatusNotFound }, false},
		{"malformed response", func(f *githubFixture, r *updater.CheckRequest) { f.malformed = true }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := &githubFixture{tag: "v26.10.01.002", assets: []string{name, "SHA256SUMS"}, checksum: checksumLine(name)}
			req := updater.CheckRequest{CurrentVersion: "v26.10.01.001", Platform: "linux", Arch: "amd64"}
			tc.change(fixture, &req)
			provider := newGitHubFixture(t, fixture)
			if release, err := provider.Check(context.Background(), req); err == nil || release != nil {
				t.Fatalf("unsafe Check = %v, %v", release, err)
			}
			if tc.noAPI && fixture.apiRequests.Load() != 0 {
				t.Fatal("invalid request reached the network")
			}
			if fixture.payloadRequests.Load() != 0 {
				t.Fatal("checking downloaded a payload")
			}
		})
	}
}

func TestGitHubProviderNoUpdate(t *testing.T) {
	const name = "agents-dashboard-linux-amd64.tar.gz"
	for _, tc := range []struct {
		name, tag string
		status    int
	}{
		{"no release", "", http.StatusNotFound},
		{"equal", "v26.10.01.002", 0},
		{"older serial", "v26.10.01.001", 0},
		{"older date", "v26.09.30.999", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Old/equal releases do not need a checksum to be ignored.
			fixture := &githubFixture{tag: tc.tag, apiStatus: tc.status, assets: []string{name}}
			provider := newGitHubFixture(t, fixture)
			release, err := provider.Check(context.Background(), updater.CheckRequest{
				CurrentVersion: "v26.10.01.002", Platform: "linux", Arch: "amd64",
			})
			if err != nil || release != nil {
				t.Fatalf("Check = %v, %v; want no update", release, err)
			}
			if fixture.payloadRequests.Load() != 0 {
				t.Fatal("checking downloaded a payload")
			}
		})
	}
}
