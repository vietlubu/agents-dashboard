//go:build darwin

package sleep

import (
	"context"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// darwinMediaWatcher reads the power assertions the system already tracks. macOS is the
// only platform where "something is playing" is stated outright: coreaudiod holds a
// prevent-idle assertion for as long as audio is being output, and players add their own
// (Chromium's video wake lock, QuickTime, VLC, …).
type darwinMediaWatcher struct{}

func (darwinMediaWatcher) MediaPlaying() (Media, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), mediaProbeTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "pmset", "-g", "assertions").Output()
	if err != nil {
		return Media{}, false
	}
	return parseAssertions(string(out), os.Getpid()), true
}

// powerAssertion is one entry of the "Listed by owning process" section. created is the
// process the assertion was made on behalf of, which is how the dashboard recognises the
// caffeinate child it spawns itself (`caffeinate -w <pid>`).
type powerAssertion struct {
	pid     int
	owner   string
	kind    string
	name    string
	created int
}

var (
	assertionLineRe = regexp.MustCompile(`^\s*pid (\d+)\(([^)]*)\):\s+\[[^\]]*\]\s+\S+\s+(\w+) named: "(.*)"\s*$`)
	createdForRe    = regexp.MustCompile(`^\s*Created for PID:\s*(\d+)`)
	externalMediaRe = regexp.MustCompile(`(?m)^\s*ExternalMedia\s+(\d+)\s*$`)
)

// mediaPlayerNames are processes that only hold prevent-idle assertions while they are
// actually playing something. Compared case-insensitively against the assertion owner.
var mediaPlayerNames = map[string]struct{}{
	"music":             {},
	"tv":                {},
	"podcasts":          {},
	"quicktime player":  {},
	"quicktimeplayer":   {},
	"vlc":               {},
	"iina":              {},
	"infuse":            {},
	"mpv":               {},
	"plex":              {},
	"plexamp":           {},
	"plex media player": {},
	"spotify":           {},
	"tidal":             {},
}

// parseAssertions classifies one `pmset -g assertions` report. It is pure so the
// classification can be tested against captured reports for every case that matters:
// audio playback, a browser video wake lock, our own caffeinate child, powerd's
// system-wide aggregate, and an idle report.
//
// Only media counts. Another app holding the machine awake for a download, a backup or a
// call is deliberately not a reason to stay up; the user asked for playback to be honoured.
func parseAssertions(text string, ownPID int) Media {
	var out Media
	if m := externalMediaRe.FindStringSubmatch(text); m != nil && m[1] != "0" {
		addMediaSource(&out, "external media")
	}
	for _, a := range parseAssertionEntries(text) {
		// powerd restates other clients' assertions system-wide ("Prevent sleep while
		// display is on"), so counting it would mean never sleeping.
		if a.owner == "powerd" || a.pid == ownPID || a.created == ownPID {
			continue
		}
		switch {
		case isAudioAssertion(a):
			addMediaSource(&out, "audio output")
		case strings.Contains(strings.ToLower(a.name), "video"):
			addMediaSource(&out, a.owner+": video")
		default:
			if _, ok := mediaPlayerNames[strings.ToLower(a.owner)]; ok {
				addMediaSource(&out, a.owner)
			}
		}
	}
	return out
}

func parseAssertionEntries(text string) []powerAssertion {
	var entries []powerAssertion
	for _, line := range strings.Split(text, "\n") {
		if m := assertionLineRe.FindStringSubmatch(line); m != nil {
			pid, _ := strconv.Atoi(m[1])
			// -1 marks "no created-for line", so an unnamed assertion can never look like
			// one made on the dashboard's behalf.
			entries = append(entries, powerAssertion{pid: pid, owner: m[2], kind: m[3], name: m[4], created: -1})
			continue
		}
		if m := createdForRe.FindStringSubmatch(line); m != nil && len(entries) > 0 {
			created, _ := strconv.Atoi(m[1])
			entries[len(entries)-1].created = created
		}
	}
	return entries
}

// isAudioAssertion recognises audio being output. coreaudiod is the only process that
// holds the audio-output prevent-idle assertion, and it releases it when playback stops.
func isAudioAssertion(a powerAssertion) bool {
	if a.owner != "coreaudiod" {
		return false
	}
	return strings.Contains(a.kind, "PreventUserIdle") && strings.Contains(a.name, "output")
}
