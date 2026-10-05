//go:build darwin

package sleep

import "testing"

func TestCaffeinateFlags(t *testing.T) {
	cases := []struct {
		name      string
		spec      InhibitSpec
		clamshell bool
		want      string
	}{
		{"system only", InhibitSpec{System: true}, false, "-i"},
		{"display only", InhibitSpec{Display: true}, false, "-d"},
		{"lid uses -s on AC", InhibitSpec{Lid: true}, false, "-s"},
		{"all three", InhibitSpec{System: true, Display: true, Lid: true}, false, "-i -d -s"},
		{"clamshell handles lid via pmset", InhibitSpec{System: true, Display: true, Lid: true}, true, "-i -d"},
		{"lid alone under clamshell needs no process", InhibitSpec{Lid: true}, true, ""},
		{"empty", InhibitSpec{}, false, ""},
	}
	for _, tc := range cases {
		if got := caffeinateFlags(tc.spec, tc.clamshell); got != tc.want {
			t.Errorf("%s: caffeinateFlags = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestDarwinInhibitorLifecycle(t *testing.T) {
	inh := newDarwinInhibitor()

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
