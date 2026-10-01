package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/vietlubu/agents-dashboard/internal/update"
	"github.com/vietlubu/agents-dashboard/internal/version"
)

// UpdateStatus is the complete desktop update snapshot shared with the frontend.
type UpdateStatus struct {
	Enabled        bool   `json:"enabled"`
	CanInstall     bool   `json:"canInstall"`
	State          string `json:"state"`
	CurrentVersion string `json:"currentVersion"`
	LatestVersion  string `json:"latestVersion"`
	ReleaseURL     string `json:"releaseUrl"`
	Reason         string `json:"reason"`
	Error          string `json:"error"`
}

func newAppService(deps *Deps, capability func() (string, error)) *AppService {
	return &AppService{deps: deps, installCapability: capability}
}

func currentInstallCapability() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve update executable: %w", err)
	}
	return update.InstallCapability(executable, os.TempDir())
}

// UpdateStatus derives enabled state from the current dependencies: composition
// configures the updater after this service has already been constructed.
func (s *AppService) UpdateStatus() UpdateStatus {
	s.updateMu.Lock()
	status := s.updateStatus
	s.updateMu.Unlock()
	if s.deps.Updater == nil {
		return UpdateStatus{
			State: "disabled", CurrentVersion: version.Version,
			Reason: s.deps.UpdaterDisabledReason,
		}
	}
	status.Enabled = true
	status.CurrentVersion = version.Version
	if status.State == "" || status.State == "disabled" {
		status.State = "idle"
		status.Reason = ""
	}
	return status
}

func (s *AppService) setUpdateStatus(status UpdateStatus) {
	s.updateMu.Lock()
	s.updateStatus = status
	s.updateMu.Unlock()
	s.deps.emit(EventAppUpdate, status)
}

// CheckForUpdates only discovers an update; no payload is downloaded without
// a subsequent InstallUpdate call confirming the exact available version.
func (s *AppService) CheckForUpdates() (UpdateStatus, error) {
	return s.checkForUpdates(context.Background(), false)
}

func (s *AppService) checkForUpdates(ctx context.Context, automatic bool) (UpdateStatus, error) {
	if !s.updateOperation.TryLock() {
		if automatic {
			return s.UpdateStatus(), nil
		}
		return s.UpdateStatus(), errors.New("update operation already running")
	}
	defer s.updateOperation.Unlock()
	status := s.UpdateStatus()
	if !status.Enabled {
		return status, errors.New("automatic updates are unavailable")
	}
	if automatic && (status.State == "available" || status.State == "installing" || status.State == "restarting") {
		return status, nil
	}
	if status.State == "restarting" {
		return status, errors.New("update operation already running")
	}

	status = UpdateStatus{Enabled: true, State: "checking", CurrentVersion: version.Version}
	s.setUpdateStatus(status)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	release, err := s.deps.Updater.Check(ctx)
	if err != nil {
		status.State, status.Error = "error", err.Error()
		s.setUpdateStatus(status)
		return status, err
	}
	if release == nil {
		status.State = "up-to-date"
		s.setUpdateStatus(status)
		return status, nil
	}

	status.State = "available"
	status.LatestVersion = "v" + strings.TrimPrefix(release.Version, "v")
	status.ReleaseURL = "https://github.com/vietlubu/agents-dashboard/releases/tag/" + status.LatestVersion
	status, err = s.assessInstallCapability(status)
	s.setUpdateStatus(status)
	return status, err
}

func (s *AppService) assessInstallCapability(status UpdateStatus) (UpdateStatus, error) {
	reason, err := s.installCapability()
	status.Reason = reason
	status.CanInstall = reason == "" && err == nil
	status.Error = ""
	if err != nil {
		status.State, status.Error = "error", err.Error()
	}
	return status, err
}

// InstallUpdate requires the exact tag the user confirmed. Wails owns download,
// checksum verification, staging and restart through the normal shutdown path.
func (s *AppService) InstallUpdate(expectedVersion string) error {
	if !s.updateOperation.TryLock() {
		return errors.New("update operation already running")
	}
	defer s.updateOperation.Unlock()
	status := s.UpdateStatus()
	if !status.Enabled {
		return errors.New("automatic updates are unavailable")
	}
	if status.State == "restarting" {
		return errors.New("update operation already running")
	}
	if status.State != "available" || status.LatestVersion == "" || expectedVersion != status.LatestVersion {
		return errors.New("update version changed; check again")
	}
	if !status.CanInstall {
		return fmt.Errorf("update installation is unavailable: %s", status.Reason)
	}

	status, err := s.assessInstallCapability(status)
	if err != nil || !status.CanInstall {
		s.setUpdateStatus(status)
		if err != nil {
			return err
		}
		return fmt.Errorf("update installation is unavailable: %s", status.Reason)
	}
	status.State = "installing"
	s.setUpdateStatus(status)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if err := s.deps.Updater.DownloadAndInstall(ctx); err != nil {
		status.State, status.Error = "available", err.Error()
		s.setUpdateStatus(status)
		return err
	}

	status.State = "available"
	status, err = s.assessInstallCapability(status)
	if err != nil || !status.CanInstall {
		s.setUpdateStatus(status)
		if err != nil {
			return err
		}
		return fmt.Errorf("update installation is unavailable: %s", status.Reason)
	}
	if err := ctx.Err(); err != nil {
		status.Error = err.Error()
		s.setUpdateStatus(status)
		return err
	}
	status.State = "restarting"
	s.setUpdateStatus(status)
	if err := s.deps.Updater.Restart(ctx); err != nil {
		status.State, status.Error = "available", err.Error()
		s.setUpdateStatus(status)
		return err
	}
	return nil
}

func (s *AppService) startUpdateLoop(ctx context.Context) {
	s.updateMu.Lock()
	if s.deps.Updater == nil || s.updateDone != nil {
		s.updateMu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	s.updateCancel, s.updateDone = cancel, done
	s.updateMu.Unlock()
	go func() {
		defer close(done)
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		s.autoCheckForUpdates(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.autoCheckForUpdates(ctx)
			}
		}
	}()
}

func (s *AppService) autoCheckForUpdates(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	if _, err := s.checkForUpdates(ctx, true); err != nil {
		s.deps.log().Warn("automatic update check failed", "error", err)
	}
}

func (s *AppService) stopUpdateLoop() {
	s.updateMu.Lock()
	cancel, done := s.updateCancel, s.updateDone
	s.updateMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
	s.updateMu.Lock()
	if s.updateDone == done {
		s.updateCancel, s.updateDone = nil, nil
	}
	s.updateMu.Unlock()
}
