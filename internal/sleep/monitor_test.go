package sleep

import (
	"reflect"
	"sort"
	"testing"
)

func TestMatchedAgents(t *testing.T) {
	got := matchedAgents([]string{
		// A Node-hosted CLI is identified by a token, not the executable name.
		"node /Users/x/.npm-global/lib/node_modules/@anthropic-ai/claude-code/cli.js --resume",
		"/usr/local/bin/codex",
		// A file name that merely contains an agent name must not match.
		"vim --claude-notes.md",
		// A Windows image name.
		"opencode.exe",
		// An unrelated runtime.
		"node server.js",
	})
	sort.Strings(got)
	want := []string{"claude", "codex", "opencode"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("matchedAgents = %v, want %v", got, want)
	}
}

func TestMatchedAgentsEmpty(t *testing.T) {
	if got := matchedAgents(nil); len(got) != 0 {
		t.Errorf("matchedAgents(nil) = %v, want empty", got)
	}
}
