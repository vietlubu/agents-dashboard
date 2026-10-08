//go:build darwin

package sleep

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestDarwinMediaWatcherLive reads this machine's real power assertions. It is opt-in
// because it depends on a live session, and it asserts only what holds regardless of what
// the user happens to be playing: the probe must be able to read the report at all, and our
// own caffeinate child must never be mistaken for playback.
func TestDarwinMediaWatcherLive(t *testing.T) {
	if os.Getenv("AGENTS_DASHBOARD_LIVE_SLEEP") == "" {
		t.Skip("set AGENTS_DASHBOARD_LIVE_SLEEP to probe real power assertions")
	}

	// Hold exactly the assertion the controller holds while an agent is active.
	caffeinate := exec.Command("caffeinate", "-i", "-d", "-w", strconv.Itoa(os.Getpid()))
	if err := caffeinate.Start(); err != nil {
		t.Fatalf("start caffeinate: %v", err)
	}
	defer func() {
		_ = caffeinate.Process.Kill()
		_ = caffeinate.Wait()
	}()
	time.Sleep(500 * time.Millisecond)

	media, ok := darwinMediaWatcher{}.MediaPlaying()
	if !ok {
		t.Fatal("pmset -g assertions could not be read on a supported platform")
	}
	if media.Playing && len(media.Sources) == 0 {
		t.Errorf("playing media reported without a source: %+v", media)
	}
	for _, source := range media.Sources {
		if strings.Contains(strings.ToLower(source), "caffeinate") {
			t.Errorf("the dashboard's own assertion was read as playback: %+v", media.Sources)
		}
	}
	t.Logf("measured=%v playing=%v sources=%v", ok, media.Playing, media.Sources)
}
