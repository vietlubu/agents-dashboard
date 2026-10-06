//go:build !server

package main

import (
	"strings"
	"testing"

	"github.com/vietlubu/agents-dashboard/internal/config"
	"github.com/vietlubu/agents-dashboard/internal/sleep"
)

func TestTrayLidStatusRequiresAcknowledgementAndReadback(t *testing.T) {
	for _, locale := range []string{"en", "vi"} {
		labels := trayLabelsFor(locale)
		tray := &tray{labels: labels}
		st := sleep.Status{Mode: config.SleepModeAlways, Supported: true, Detail: "always",
			LidControl: sleep.LidState{PrivateAPI: true}}
		if got := tray.statusText(st, true); got != labels.lidPending {
			t.Fatalf("%s selected but not requested lid reported %q", locale, got)
		}
		st.LidControl.Requested = true
		st.KeepingAwake = true // Idle system prevention does not prove the lid override.
		if got := tray.statusText(st, true); got != labels.lidPending {
			t.Fatalf("%s unobserved lid request reported full success: %q", locale, got)
		}
		st.LidControl.Known, st.LidControl.Effective = true, true
		if got := tray.statusText(st, true); got != labels.stAlways {
			t.Fatalf("%s effective lid status = %q", locale, got)
		}
		st.Clamshell = true
		if got := tray.statusText(st, true); got != labels.legacySleepDisabled {
			t.Fatalf("%s legacy all-sleep veto was hidden by successful lid state: %q", locale, got)
		}
		st.Error = "native reset failed"
		if got := tray.statusText(st, true); !strings.Contains(got, st.Error) || !strings.Contains(got, labels.stError) {
			t.Fatalf("%s native release error was hidden: %q", locale, got)
		}
	}
}
