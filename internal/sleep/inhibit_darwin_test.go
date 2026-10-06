//go:build darwin

package sleep

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCaffeinateFlags(t *testing.T) {
	// Exhaust every scope combination: lid must never add a system-wide -s assertion.
	for scopes := range 8 {
		spec := InhibitSpec{System: scopes&1 != 0, Display: scopes&2 != 0, Lid: scopes&4 != 0}
		want := ""
		if spec.System {
			want = "-i"
		}
		if spec.Display {
			if want != "" {
				want += " "
			}
			want += "-d"
		}
		if got := caffeinateFlags(spec); got != want {
			t.Errorf("spec=%+v: flags=%q, want %q", spec, got, want)
		}
	}
}

func TestDarwinInhibitorLifecycle(t *testing.T) {
	// Substitute a harmless child, never invoke the host's caffeinate or pmset.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "caffeinate"), []byte("#!/bin/sh\nexec /bin/sleep 3600\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	inh := &darwinInhibitor{}
	t.Cleanup(func() { _ = inh.Release() })

	if err := inh.Apply(InhibitSpec{System: true, Display: true}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if inh.cmd == nil || inh.cmd.Process == nil {
		t.Fatalf("apply did not start caffeinate")
	}
	pid := inh.cmd.Process.Pid

	// Re-applying the same spec must not restart the child.
	if err := inh.Apply(InhibitSpec{System: true, Display: true}); err != nil {
		t.Fatalf("re-apply: %v", err)
	}
	if inh.cmd.Process.Pid != pid {
		t.Errorf("re-apply restarted caffeinate (%d -> %d)", pid, inh.cmd.Process.Pid)
	}

	if err := inh.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	if inh.cmd != nil {
		t.Errorf("release left a caffeinate child")
	}
}
