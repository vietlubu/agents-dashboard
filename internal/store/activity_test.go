package store

import (
	"context"
	"testing"
	"time"
)

func TestLatestActivityAndActiveSessions(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	loc := time.UTC
	base := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC).UnixMilli()

	if got, err := db.LatestActivityMs(ctx); err != nil || got != 0 {
		t.Fatalf("empty LatestActivityMs = %d err=%v, want 0", got, err)
	}

	events := []Event{
		sampleEvent("claude|m1|r1", base, 100),
		sampleEvent("claude|m1|r2", base+2000, 100),
	}
	if _, _, err := db.InsertEvents(ctx, events, loc); err != nil {
		t.Fatalf("insert events: %v", err)
	}
	if got, err := db.LatestActivityMs(ctx); err != nil || got != base+2000 {
		t.Errorf("LatestActivityMs from events = %d err=%v, want %d", got, err, base+2000)
	}

	// A newer session update must win over the newest event.
	if err := db.UpsertSessions(ctx, []Session{{
		Harness: "claude", SessionID: "sess-1", UpdatedAt: base + 9000,
	}}); err != nil {
		t.Fatalf("upsert session: %v", err)
	}
	if got, err := db.LatestActivityMs(ctx); err != nil || got != base+9000 {
		t.Errorf("LatestActivityMs with session = %d err=%v, want %d", got, err, base+9000)
	}

	if n, err := db.ActiveSessionCount(ctx, base+9000); err != nil || n != 1 {
		t.Errorf("ActiveSessionCount(since=update) = %d err=%v, want 1", n, err)
	}
	if n, err := db.ActiveSessionCount(ctx, base+9001); err != nil || n != 0 {
		t.Errorf("ActiveSessionCount(since=after) = %d err=%v, want 0", n, err)
	}
}
