package harness

import (
	"context"
	"os"
	"path/filepath"
	"sort"

	"github.com/vietlubu/agents-dashboard/internal/store"
)

// All returns the registered harnesses rooted at the current user's home directory.
func All() []Adapter {
	home, _ := os.UserHomeDir()
	return AllWithHome(home)
}

// AllWithHome returns the registered harnesses rooted at an explicit home directory.
//
// It exists so a scan can be pointed somewhere other than the running user's home: the
// tests use it to scan a fixture tree instead of the developer's real sessions, and it is
// the hook a future "read another machine's sessions" feature would use.
func AllWithHome(home string) []Adapter {
	return []Adapter{
		newClaudeAdapter(home),
		newCodexAdapter(home),
		newOpencodeAdapter(home),
		newPiAdapter(home),
		newOmpAdapter(home),
		newFreebuffAdapter(home),
		newFreebuffDesktopAdapter(home),
		newDshAdapter(home),
	}
}

// Find returns the adapter with the given id.
func Find(id string) (Adapter, bool) {
	for _, a := range All() {
		if a.ID() == id {
			return a, true
		}
	}
	return nil, false
}

// ResolvedRoots is the outcome of resolving one harness's scan roots.
type ResolvedRoots struct {
	// All is the de-duplicated root list, existing or not.
	All []string
	// Found is the subset present on disk; this is what a scan walks.
	Found []string
	// Missing is the subset that does not exist, shown in the UI as the expected
	// location of a harness that is not installed.
	Missing []string
}

// ResolveRoots merges an adapter's built-in roots with the user-added roots from the
// database, then cleans, de-duplicates and symlink-resolves them. Two paths that resolve
// to the same directory collapse into one, so a session tree reachable through a symlink
// is never walked twice.
func ResolveRoots(ctx context.Context, a Adapter, db *store.DB) (ResolvedRoots, error) {
	var extra []string
	if db != nil {
		rows, err := db.ScanRoots(ctx, a.ID())
		if err != nil {
			return ResolvedRoots{}, err
		}
		for _, r := range rows {
			extra = append(extra, r.Path)
		}
	}

	out := ResolvedRoots{All: []string{}, Found: []string{}, Missing: []string{}}
	seen := map[string]struct{}{}
	for _, raw := range a.Roots(ctx, extra) {
		if raw == "" {
			continue
		}
		path := raw
		if abs, err := filepath.Abs(path); err == nil {
			path = abs
		}
		path = filepath.Clean(path)
		if resolved, err := filepath.EvalSymlinks(path); err == nil {
			path = resolved
		}
		if _, dup := seen[path]; dup {
			continue
		}
		seen[path] = struct{}{}
		out.All = append(out.All, path)
		if _, err := os.Stat(path); err == nil {
			out.Found = append(out.Found, path)
		} else {
			out.Missing = append(out.Missing, path)
		}
	}
	sort.Strings(out.All)
	sort.Strings(out.Found)
	sort.Strings(out.Missing)
	return out, nil
}

// ScanRoots is the convenience used by the sync engine: the roots a scan should walk.
func ScanRoots(ctx context.Context, a Adapter, db *store.DB) ([]string, error) {
	r, err := ResolveRoots(ctx, a, db)
	if err != nil {
		return nil, err
	}
	return r.Found, nil
}
