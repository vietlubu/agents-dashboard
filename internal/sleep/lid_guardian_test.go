package sleep

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type guardianBrokenWriter struct{}

func (guardianBrokenWriter) Write([]byte) (int, error) { return 0, errors.New("lost acknowledgement") }

func TestLidGuardianProtocol(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		want        []bool
		failed      bool
	}{
		{"startup-eof", "", nil, false},
		{"startup-off", "off\n", nil, false},
		{"parent-eof", "on\n", []bool{true, false}, false},
		{"normal-release", "on\noff\n", []bool{true, false}, false},
		{"reconciliation", "on\non\n", []bool{true, true, false}, false},
		{"invalid-command", "on\nwrong\n", []bool{true, false}, true},
		{"oversized-command", "on\n" + strings.Repeat("x", 300) + "\n", []bool{true, false}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var operations []bool
			var output bytes.Buffer
			err := runLidGuardian(strings.NewReader(tc.input), &output, func(on bool) (bool, error) { operations = append(operations, on); return true, nil })
			if (err != nil) != tc.failed || !reflect.DeepEqual(operations, tc.want) {
				t.Fatalf("operations=%v error=%v", operations, err)
			}
			if !tc.failed && strings.Count(output.String(), "ok\n") != strings.Count(tc.input, "\n") {
				t.Fatalf("reply=%q", output.String())
			}
		})
	}
}

func TestLidGuardianFailures(t *testing.T) {
	t.Run("failed-enable-does-not-reset", func(t *testing.T) {
		var operations []bool
		var output bytes.Buffer
		err := runLidGuardian(strings.NewReader("on\n"), &output, func(on bool) (bool, error) { operations = append(operations, on); return false, errors.New("denied") })
		if err != nil || !reflect.DeepEqual(operations, []bool{true}) || output.String() != "error denied\n" {
			t.Fatalf("operations=%v reply=%q error=%v", operations, output.String(), err)
		}
	})
	t.Run("lost-ack-cleans-successful-enable", func(t *testing.T) {
		var operations []bool
		err := runLidGuardian(strings.NewReader("on\n"), guardianBrokenWriter{}, func(on bool) (bool, error) { operations = append(operations, on); return true, nil })
		if err == nil || !reflect.DeepEqual(operations, []bool{true, false}) {
			t.Fatalf("operations=%v error=%v", operations, err)
		}
	})
	t.Run("close-error-still-cleans-applied-enable", func(t *testing.T) {
		var operations []bool
		err := runLidGuardian(strings.NewReader("on\n"), io.Discard, func(on bool) (bool, error) {
			operations = append(operations, on)
			if on {
				return true, errors.New("close failed")
			}
			return true, nil
		})
		if err != nil || !reflect.DeepEqual(operations, []bool{true, false}) {
			t.Fatalf("operations=%v error=%v", operations, err)
		}
	})
	t.Run("failed-release-retries-at-eof", func(t *testing.T) {
		var operations []bool
		err := runLidGuardian(strings.NewReader("on\noff\n"), io.Discard, func(on bool) (bool, error) {
			operations = append(operations, on)
			if len(operations) == 2 {
				return false, errors.New("reset failed")
			}
			return true, nil
		})
		if err != nil || !reflect.DeepEqual(operations, []bool{true, false, false}) {
			t.Fatalf("operations=%v error=%v", operations, err)
		}
	})
	t.Run("cleanup-error-surfaces", func(t *testing.T) {
		err := runLidGuardian(strings.NewReader("on\n"), io.Discard, func(on bool) (bool, error) {
			if !on {
				return false, errors.New("reset failed")
			}
			return true, nil
		})
		if err == nil || !strings.Contains(err.Error(), "reset failed") {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("applied-reset-close-error-does-not-repeat-reset", func(t *testing.T) {
		var operations []bool
		var output bytes.Buffer
		err := runLidGuardian(strings.NewReader("on\noff\n"), &output, func(on bool) (bool, error) {
			operations = append(operations, on)
			if !on {
				return true, errors.New("close failed")
			}
			return true, nil
		})
		if err != nil || !reflect.DeepEqual(operations, []bool{true, false}) || output.String() != "ok\napplied-error close failed\n" {
			t.Fatalf("operations=%v reply=%q error=%v", operations, output.String(), err)
		}
	})
	t.Run("unapplied-request-cannot-ack-success", func(t *testing.T) {
		var output bytes.Buffer
		err := runLidGuardian(strings.NewReader("on\n"), &output, func(bool) (bool, error) { return false, nil })
		if err != nil || output.String() != "error lid request was not applied\n" {
			t.Fatalf("reply=%q error=%v", output.String(), err)
		}
	})
}

// Only this explicitly selected test subprocess runs the fake protocol. It
// never dispatches RunLidGuardian and cannot reach the native setter.
func TestLidGuardianProcessFixture(t *testing.T) {
	mode := os.Getenv("AD_LID_FIXTURE")
	if mode == "" {
		return
	}
	resets := 0
	set := func(on bool) (bool, error) {
		file, err := os.OpenFile(os.Getenv("AD_LID_LOG"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return false, err
		}
		_, err = fmt.Fprintln(file, on)
		_ = file.Close()
		if err != nil {
			return false, err
		}
		if on && mode == "deny-on" {
			return false, errors.New("enable denied")
		}
		if !on {
			resets++
		}
		if !on && mode == "deny-off-once" && resets == 1 {
			return false, errors.New("reset denied")
		}
		if !on && mode == "close-off-once" && resets == 1 {
			return true, errors.New("close failed")
		}
		if on && mode == "hang" {
			time.Sleep(10 * time.Second)
		}
		return true, nil
	}
	var output io.Writer = os.Stdout
	if mode == "lost-ack" {
		output = guardianBrokenWriter{}
	}
	if err := runLidGuardian(os.Stdin, output, set); err != nil {
		os.Exit(7)
	}
	os.Exit(0)
}

func fixtureLidGuardian(t *testing.T, mode string) (*lidGuardian, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "requests")
	cmd := exec.Command(os.Args[0], "-test.run=^TestLidGuardianProcessFixture$")
	cmd.Env = append(os.Environ(), "AD_LID_FIXTURE="+mode, "AD_LID_LOG="+path)
	// Race-instrumented subprocesses otherwise sleep one second on exit,
	// hiding completed cleanup behind the controller test's deadline.
	cmd.Env = append(cmd.Env, "GORACE="+os.Getenv("GORACE")+" atexit_sleep_ms=0")
	g, err := startLidGuardianCommand(cmd)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = g.stop() })
	return g, path
}

func readLidRequests(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func TestLidGuardianProcessEOF(t *testing.T) {
	g, path := fixtureLidGuardian(t, "ok")
	if _, err := g.exchange(true); err != nil {
		t.Fatal(err)
	}
	if err := g.stop(); err != nil {
		t.Fatal(err)
	}
	if got := readLidRequests(t, path); got != "true\nfalse\n" {
		t.Fatalf("requests=%q", got)
	}
}

func TestLidGuardianProcessLostAck(t *testing.T) {
	g, path := fixtureLidGuardian(t, "lost-ack")
	if _, err := g.exchange(true); err == nil {
		t.Fatal("lost acknowledgement reported success")
	}
	_ = g.stop()
	if got := readLidRequests(t, path); got != "true\nfalse\n" {
		t.Fatalf("requests=%q", got)
	}
}

func TestLidGuardianProcessTimeout(t *testing.T) {
	g, _ := fixtureLidGuardian(t, "hang")
	g.timeout = 50 * time.Millisecond
	if _, err := g.exchange(true); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("error=%v", err)
	}
	if err := g.stop(); err == nil {
		t.Fatal("hung helper cleanup reported success")
	}
	if g.alive() {
		t.Fatal("hung helper was left running")
	}
}

func TestLidGuardianDispatch(t *testing.T) {
	if handled, err := RunLidGuardian(nil); handled || err != nil {
		t.Fatalf("handled=%v error=%v", handled, err)
	}
	if handled, err := RunLidGuardian([]string{"--other"}); handled || err != nil {
		t.Fatalf("handled=%v error=%v", handled, err)
	}
	if handled, err := RunLidGuardian([]string{lidGuardianArgument, "extra"}); !handled || err == nil {
		t.Fatalf("handled=%v error=%v", handled, err)
	}
}

func TestLidGuardianAppliedResetErrorProgress(t *testing.T) {
	g, path := fixtureLidGuardian(t, "close-off-once")
	if applied, err := g.exchange(true); !applied || err != nil {
		t.Fatalf("enable applied=%v error=%v", applied, err)
	}
	if applied, err := g.exchange(false); !applied || err == nil {
		t.Fatalf("reset lost application progress: applied=%v error=%v", applied, err)
	}
	if err := g.stop(); err != nil {
		t.Fatal(err)
	}
	if got := readLidRequests(t, path); got != "true\nfalse\n" {
		t.Fatalf("confirmed reset repeated at EOF: %q", got)
	}
}
