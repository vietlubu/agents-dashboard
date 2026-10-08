//go:build linux

package sleep

import (
	"context"
	"os/exec"
	"regexp"
	"strings"
)

// linuxMediaWatcher is best-effort. There is no single Linux API for "media is playing",
// so it combines the two signals a desktop session usually exposes: an audio stream in the
// RUNNING state on the PulseAudio/PipeWire mixer, and an idle inhibitor whose stated reason
// names playback (Chromium and Discord register "Playing video").
type linuxMediaWatcher struct{}

func (linuxMediaWatcher) MediaPlaying() (Media, bool) {
	var out Media
	// measured tracks whether any probe could answer at all. A session with neither tool
	// reports unmeasurable, and the controller then keeps its previous behaviour.
	measured := false

	if playing, ok := pulseAudioPlaying(); ok {
		measured = true
		if playing {
			addMediaSource(&out, "audio output")
		}
	}
	if sources, ok := logindMediaInhibitors(); ok {
		measured = true
		for _, source := range sources {
			addMediaSource(&out, source)
		}
	}
	return out, measured
}

var sinkStateRe = regexp.MustCompile(`(?m)^\s*State:\s*(\w+)`)

// pulseAudioPlaying reports whether any audio stream is actively being played. A paused or
// corked stream does not count; the mixer marks it IDLE or CORKED.
func pulseAudioPlaying() (bool, bool) {
	if _, err := exec.LookPath("pactl"); err != nil {
		return false, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), mediaProbeTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "pactl", "list", "sink-inputs").Output()
	if err != nil {
		return false, false
	}
	for _, m := range sinkStateRe.FindAllStringSubmatch(string(out), -1) {
		if strings.EqualFold(m[1], "RUNNING") {
			return true, true
		}
	}
	return false, true
}

// logindMediaInhibitors lists sessions' idle inhibitors whose reason names playback. The
// reason is free text, so this is a keyword match rather than a structured read; only
// inhibitors that cover idle at all are considered.
func logindMediaInhibitors() ([]string, bool) {
	if _, err := exec.LookPath("systemd-inhibit"); err != nil {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), mediaProbeTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "systemd-inhibit", "--list", "--no-pager").Output()
	if err != nil {
		return nil, false
	}
	var sources []string
	for _, line := range strings.Split(string(out), "\n") {
		lower := strings.ToLower(line)
		if !strings.Contains(lower, "idle") || !mentionsPlayback(lower) {
			continue
		}
		source := "media"
		if fields := strings.Fields(line); len(fields) > 0 {
			source = fields[0]
		}
		sources = append(sources, source)
	}
	return sources, true
}

// mentionsPlayback matches the playback keywords media apps put in an inhibitor's reason,
// such as Chromium's "Playing video".
func mentionsPlayback(line string) bool {
	for _, keyword := range []string{"video", "audio", "media", "playback", "playing"} {
		if strings.Contains(line, keyword) {
			return true
		}
	}
	return false
}
