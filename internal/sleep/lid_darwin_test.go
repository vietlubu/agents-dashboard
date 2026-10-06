//go:build darwin

package sleep

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vietlubu/agents-dashboard/internal/config"
)

type fakeDarwinLid struct {
	mu                            sync.Mutex
	mode                          string
	effective                     bool
	readErr                       error
	fallbackErr                   error
	fallbackApplied               bool
	fallbacks                     []bool
	reads, starts, watches, stops int
	events                        chan struct{}
	guardian                      *lidGuardian
	log                           string
}

func isolatedDarwinLid(t *testing.T, mode string) (*darwinInhibitor, *fakeDarwinLid) {
	t.Helper()
	f := &fakeDarwinLid{mode: mode, effective: true}
	l := newDarwinLidControl(darwinLidHooks{
		available: true,
		set: func(on bool) (bool, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.fallbacks = append(f.fallbacks, on)
			return f.fallbackErr == nil || f.fallbackApplied, f.fallbackErr
		},
		read: func() (bool, error) { f.mu.Lock(); defer f.mu.Unlock(); f.reads++; return f.effective, f.readErr },
		watch: func() (<-chan struct{}, func(), error) {
			f.mu.Lock()
			f.watches++
			f.events = make(chan struct{}, 16)
			events := f.events
			f.mu.Unlock()
			return events, func() { f.mu.Lock(); f.stops++; f.mu.Unlock() }, nil
		},
		start: func() (*lidGuardian, error) {
			g, path := fixtureLidGuardian(t, f.mode)
			f.mu.Lock()
			f.starts++
			f.guardian, f.log = g, path
			f.mu.Unlock()
			return g, nil
		},
	})
	inh := &darwinInhibitor{lid: l}
	t.Cleanup(func() { _ = inh.Release(); l.stopWatch() })
	return inh, f
}

func isolatedCaffeinate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "caffeinate"), []byte("#!/bin/sh\nexec /bin/sleep 3600\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	return dir
}

func awaitLidNotification(t *testing.T, notifications <-chan struct{}) {
	t.Helper()
	select {
	case <-notifications:
	case <-time.After(time.Second):
		t.Fatal("no lid reconciliation notification")
	}
}

func TestDarwinLidObservationAndDrift(t *testing.T) {
	inh, f := isolatedDarwinLid(t, "ok")
	notifications := make(chan struct{}, 16)
	inh.SetLidNotifier(func() {
		// Callbacks must run outside both cached-state and inhibitor mutexes.
		_ = inh.LidState()
		_ = inh.ClamshellActive()
		notifications <- struct{}{}
	})
	if state := inh.LidState(); state != (LidState{PrivateAPI: true}) {
		t.Fatalf("startup=%+v", state)
	}
	if f.reads != 0 || f.starts != 0 || f.watches != 0 {
		t.Fatal("notifier or getter started native work")
	}
	if err := inh.Apply(InhibitSpec{Lid: true}); err != nil {
		t.Fatal(err)
	}
	if state := inh.LidState(); !state.Requested || !state.Known || !state.Effective {
		t.Fatalf("enabled=%+v", state)
	}
	if err := inh.Apply(InhibitSpec{Lid: true}); err != nil {
		t.Fatal(err)
	}
	if got := readLidRequests(t, f.log); got != "true\n" {
		t.Fatalf("unchanged requests=%q", got)
	}

	f.mu.Lock()
	f.effective = false
	f.mu.Unlock()
	if err := inh.Apply(InhibitSpec{Lid: true}); err != nil {
		t.Fatal(err)
	}
	if state := inh.LidState(); !state.Requested || !state.Known || state.Effective {
		t.Fatalf("overwritten policy=%+v", state)
	}
	if got := readLidRequests(t, f.log); got != "true\ntrue\n" {
		t.Fatalf("drift requests=%q", got)
	}

	f.mu.Lock()
	f.effective = true
	events := f.events
	f.mu.Unlock()
	inh.lid.mu.Lock()
	epoch := inh.lid.epoch
	inh.lid.mu.Unlock()
	events <- struct{}{}
	awaitLidNotification(t, notifications)
	if state := inh.LidState(); state.Known || state.Effective || !state.Requested {
		t.Fatalf("event cache=%+v", state)
	}
	if err := inh.Apply(InhibitSpec{Lid: true}); err != nil {
		t.Fatal(err)
	}
	if got := readLidRequests(t, f.log); got != "true\ntrue\ntrue\n" {
		t.Fatalf("event requests=%q", got)
	}
	if err := inh.Release(); err != nil {
		t.Fatal(err)
	}
	// Another system writer may keep the aggregate effective status enabled.
	if state := inh.LidState(); state.Requested || !state.Known || !state.Effective {
		t.Fatalf("released aggregate=%+v", state)
	}
	if f.stops != 1 || f.guardian.alive() {
		t.Fatal("release left observer or guardian active")
	}
	inh.lid.invalidate(epoch)
	select {
	case <-notifications:
		t.Fatal("stale observation notified after release")
	default:
	}
	if err := inh.Apply(InhibitSpec{Lid: true}); err != nil {
		t.Fatal(err)
	}
	inh.lid.invalidate(epoch)
	select {
	case <-notifications:
		t.Fatal("old observer notified the new lifecycle")
	default:
	}
	if state := inh.LidState(); !state.Known {
		t.Fatalf("old observer invalidated new state=%+v", state)
	}
}

func TestDarwinLidReleaseFailureIsRetryable(t *testing.T) {
	isolatedCaffeinate(t)
	inh, f := isolatedDarwinLid(t, "deny-off-once")
	if err := inh.Apply(InhibitSpec{System: true, Lid: true}); err != nil {
		t.Fatal(err)
	}
	pid := inh.cmd.Process.Pid
	if err := inh.Apply(InhibitSpec{Display: true}); err == nil {
		t.Fatal("failed lid reset reported success")
	}
	if state := inh.LidState(); !state.Requested {
		t.Fatalf("failed release forgot request=%+v", state)
	}
	if f.stops != 0 || !f.guardian.alive() || inh.cmd.Process.Pid != pid || inh.flags != "-i" {
		t.Fatal("failed release discarded the previous successful state")
	}
	if err := inh.Release(); err != nil {
		t.Fatal(err)
	}
	if inh.cmd != nil || inh.LidState().Requested || f.stops != 1 || f.guardian.alive() {
		t.Fatal("retry did not clean up")
	}
	if got := readLidRequests(t, f.log); got != "true\nfalse\nfalse\n" {
		t.Fatalf("requests=%q", got)
	}
}

func TestDarwinLidFailedApplyPreservesIdleAssertions(t *testing.T) {
	for _, failure := range []string{"unsupported", "watch", "process", "enable", "readback"} {
		t.Run(failure, func(t *testing.T) {
			isolatedCaffeinate(t)
			mode := "ok"
			if failure == "enable" {
				mode = "deny-on"
			}
			inh, f := isolatedDarwinLid(t, mode)
			if err := inh.Apply(InhibitSpec{System: true}); err != nil {
				t.Fatal(err)
			}
			pid := inh.cmd.Process.Pid
			switch failure {
			case "unsupported":
				inh.lid.hooks.available = false
			case "watch":
				inh.lid.hooks.watch = func() (<-chan struct{}, func(), error) { return nil, nil, errors.New("observer unavailable") }
			case "process":
				inh.lid.hooks.start = func() (*lidGuardian, error) { return nil, errors.New("helper unavailable") }
			case "readback":
				f.mu.Lock()
				f.readErr = errors.New("property unavailable")
				f.mu.Unlock()
			}
			if err := inh.Apply(InhibitSpec{Display: true, Lid: true}); err == nil {
				t.Fatal("failed lid change reported success")
			}
			if inh.cmd.Process.Pid != pid || inh.flags != "-i" {
				t.Fatal("failed lid change replaced the previous idle assertion")
			}
			if state := inh.LidState(); state.Requested || state.Known || state.Effective {
				t.Fatalf("failed enable state=%+v", state)
			}
			inh.lid.mu.Lock()
			guardian, watch := inh.lid.guardian, inh.lid.watchStop
			inh.lid.mu.Unlock()
			if guardian != nil || watch != nil {
				t.Fatal("failed enable left native lifecycle running")
			}
			if failure == "enable" && readLidRequests(t, f.log) != "true\n" {
				t.Fatal("failed selector caused an ownership-unsafe reset")
			}
			if failure == "readback" && readLidRequests(t, f.log) != "true\nfalse\n" {
				t.Fatal("readback failure leaked an acknowledged request")
			}
		})
	}
}

func TestDarwinIdleReplacementFailurePreservesOldChild(t *testing.T) {
	dir := isolatedCaffeinate(t)
	inh := &darwinInhibitor{}
	t.Cleanup(func() { _ = inh.Release() })
	if err := inh.Apply(InhibitSpec{System: true}); err != nil {
		t.Fatal(err)
	}
	pid := inh.cmd.Process.Pid
	t.Setenv("PATH", t.TempDir())
	if err := inh.Apply(InhibitSpec{Display: true}); err == nil {
		t.Fatal("missing replacement reported success")
	}
	if inh.cmd.Process.Pid != pid || inh.flags != "-i" {
		t.Fatal("replacement failure killed the old child")
	}
	t.Setenv("PATH", dir)
	if err := inh.Apply(InhibitSpec{Display: true}); err != nil {
		t.Fatal(err)
	}
	if inh.cmd.Process.Pid == pid || inh.flags != "-d" {
		t.Fatal("successful replacement did not switch scopes")
	}
}

func TestDarwinLidUnexpectedGuardianDeath(t *testing.T) {
	isolatedCaffeinate(t)
	inh, f := isolatedDarwinLid(t, "ok")
	notifications := make(chan struct{}, 16)
	inh.SetLidNotifier(func() { notifications <- struct{}{} })
	if err := inh.Apply(InhibitSpec{System: true, Lid: true}); err != nil {
		t.Fatal(err)
	}
	if err := f.guardian.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	awaitLidNotification(t, notifications)
	if state := inh.LidState(); !state.Requested || state.Known || state.Effective {
		t.Fatalf("lost guardian=%+v", state)
	}
	if len(f.fallbacks) != 0 {
		t.Fatal("observation wrote native policy")
	}
	f.mu.Lock()
	f.fallbackErr = errors.New("fallback denied")
	f.mu.Unlock()
	if err := inh.Release(); err == nil {
		t.Fatal("failed fallback reported success")
	}
	if !inh.LidState().Requested || inh.cmd == nil || f.stops != 0 {
		t.Fatal("failed fallback forgot outstanding cleanup")
	}
	f.mu.Lock()
	f.fallbackErr = nil
	f.mu.Unlock()
	if err := inh.Release(); err != nil {
		t.Fatal(err)
	}
	if inh.LidState().Requested || inh.cmd != nil || f.stops != 1 {
		t.Fatal("fallback retry did not release")
	}
	if !reflect.DeepEqual(f.fallbacks, []bool{false, false}) {
		t.Fatalf("fallbacks=%v", f.fallbacks)
	}
}

func TestDarwinLidLostAckRemainsUncertain(t *testing.T) {
	isolatedCaffeinate(t)
	inh, f := isolatedDarwinLid(t, "lost-ack")
	if err := inh.Apply(InhibitSpec{System: true}); err != nil {
		t.Fatal(err)
	}
	pid := inh.cmd.Process.Pid
	if err := inh.Apply(InhibitSpec{Display: true, Lid: true}); err == nil {
		t.Fatal("lost ACK reported success")
	}
	if state := inh.LidState(); state.Requested || state.Known || state.Effective {
		t.Fatalf("lost ACK=%+v", state)
	}
	if inh.cmd.Process.Pid != pid || inh.flags != "-i" {
		t.Fatal("lost ACK changed successful idle state")
	}
	if f.guardian.alive() || readLidRequests(t, f.log) != "true\nfalse\n" {
		t.Fatal("lost ACK did not allow guardian EOF cleanup")
	}
	if err := inh.Release(); err == nil || !strings.Contains(err.Error(), "outcome unknown") {
		t.Fatalf("uncertain release=%v", err)
	}
	if len(f.fallbacks) != 0 {
		t.Fatal("unacknowledged request caused an ownership-unsafe native reset")
	}
	// A new, acknowledged request permits a subsequent safe release.
	f.mode = "ok"
	if err := inh.Apply(InhibitSpec{System: true, Lid: true}); err != nil {
		t.Fatal(err)
	}
	if err := inh.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestDarwinLidReadFailurePreservesPreviousRequest(t *testing.T) {
	inh, f := isolatedDarwinLid(t, "ok")
	if err := inh.Apply(InhibitSpec{Lid: true}); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.readErr = errors.New("read unavailable")
	f.mu.Unlock()
	if err := inh.Apply(InhibitSpec{Lid: true}); err == nil {
		t.Fatal("read failure reported known success")
	}
	if state := inh.LidState(); !state.Requested || state.Known || state.Effective {
		t.Fatalf("read failure=%+v", state)
	}
	if err := inh.Release(); err != nil {
		t.Fatal(err)
	}
	if state := inh.LidState(); state.Requested || state.Known {
		t.Fatalf("released unreadable policy=%+v", state)
	}
}

func TestDarwinControllerReconcilesNativeLidEvents(t *testing.T) {
	inh, f := isolatedDarwinLid(t, "ok")
	cfg := newTestConfig(time.Hour, time.Minute)
	cfg.Apply(config.Mutable{SleepMode: config.SleepModeAlways, PreventLidClosedSleep: true})
	statuses := make(chan Status, 16)
	c := New(Deps{Cfg: cfg, Inhibitor: inh, Supported: true, LidSupported: true, Tick: time.Hour, OnStatus: func(st Status) { statuses <- st }})
	c.Start(context.Background())
	t.Cleanup(c.Stop)
	awaitStatus := func(predicate func(Status) bool) Status {
		t.Helper()
		deadline := time.NewTimer(time.Second)
		defer deadline.Stop()
		for {
			select {
			case st := <-statuses:
				if predicate(st) {
					return st
				}
			case <-deadline.C:
				t.Fatal("controller did not reconcile native status")
			}
		}
	}
	awaitStatus(func(st Status) bool { return st.Held.Lid && st.LidControl.Requested })
	f.mu.Lock()
	f.effective = false
	events := f.events
	f.mu.Unlock()
	events <- struct{}{}
	st := awaitStatus(func(st Status) bool {
		return st.LidControl.Requested && st.LidControl.Known && !st.LidControl.Effective
	})
	if st.Held.Lid || st.KeepingAwake {
		t.Fatalf("saved choice or successful request treated as effective: %+v", st)
	}
	if got := readLidRequests(t, f.log); got != "true\ntrue\n" {
		t.Fatalf("loop reconciliation requests=%q", got)
	}
	cfg.Apply(config.Mutable{SleepMode: config.SleepModeOff, PreventLidClosedSleep: true})
	c.Kick()
	awaitStatus(func(st Status) bool { return st.Mode == config.SleepModeOff && !st.LidControl.Requested })
	if f.guardian.alive() || f.stops != 1 {
		t.Fatal("off transition left native lifecycle alive")
	}
}

func TestDarwinLidAppliedFallbackResetPreservesCloseError(t *testing.T) {
	inh, f := isolatedDarwinLid(t, "ok")
	if err := inh.Apply(InhibitSpec{Lid: true}); err != nil {
		t.Fatal(err)
	}
	if err := f.guardian.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	<-f.guardian.done
	f.mu.Lock()
	f.fallbackApplied = true
	f.fallbackErr = errors.New("close failed")
	f.mu.Unlock()
	if err := inh.Release(); err == nil || !strings.Contains(err.Error(), "close failed") {
		t.Fatalf("reset close error=%v", err)
	}
	if inh.LidState().Requested || f.stops != 1 {
		t.Fatal("successful scalar reset was forgotten because close failed")
	}
	if err := inh.Release(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.fallbacks, []bool{false}) {
		t.Fatalf("successful scalar reset repeated=%v", f.fallbacks)
	}
}

func TestDarwinLidAppliedResetErrorRetiresRequest(t *testing.T) {
	inh, f := isolatedDarwinLid(t, "close-off-once")
	if err := inh.Apply(InhibitSpec{Lid: true}); err != nil {
		t.Fatal(err)
	}
	if err := inh.Release(); err == nil || !strings.Contains(err.Error(), "close failed") {
		t.Fatalf("applied reset error=%v", err)
	}
	if inh.LidState().Requested || f.guardian.alive() || f.stops != 1 {
		t.Fatal("applied reset left request or native lifecycle active")
	}
	if err := inh.Release(); err != nil {
		t.Fatal(err)
	}
	if got := readLidRequests(t, f.log); got != "true\nfalse\n" || len(f.fallbacks) != 0 {
		t.Fatalf("confirmed reset repeated: requests=%q fallback=%v", got, f.fallbacks)
	}
}

func TestDarwinLidCleanEOFDoesNotRepeatSharedReset(t *testing.T) {
	inh, f := isolatedDarwinLid(t, "ok")
	if err := inh.Apply(InhibitSpec{Lid: true}); err != nil {
		t.Fatal(err)
	}
	f.guardian.broken = true // Parent cannot use the pipe, but EOF cleanup still succeeds.
	if err := inh.Release(); err != nil {
		t.Fatal(err)
	}
	if inh.LidState().Requested || f.guardian.alive() || f.stops != 1 {
		t.Fatal("clean EOF failed to retire request")
	}
	if got := readLidRequests(t, f.log); got != "true\nfalse\n" || len(f.fallbacks) != 0 {
		t.Fatalf("clean EOF reset repeated: requests=%q fallback=%v", got, f.fallbacks)
	}
}
