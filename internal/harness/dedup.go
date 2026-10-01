package harness

// EventDedup collapses records that describe the same API response within one file.
//
// Claude repeats the identical usage object on every content-block record of one
// message, and a streamed response can be written as a partial record first. Keeping the
// largest total preserves the completed record, and keeping the earliest timestamp keeps
// the usage attributed to the turn that produced it — the same rule PokeTokenBar applies
// as dedupKeepMax.
//
// This is deliberately per file. Duplicates spread across files (a branch replaying a
// parent session, the same transcript under two roots) are collapsed by the event_key
// upsert in the store, which is global.
type EventDedup struct {
	byKey map[string]Event
	order []string
}

// NewEventDedup returns an empty accumulator.
func NewEventDedup() *EventDedup {
	return &EventDedup{byKey: map[string]Event{}}
}

// Add merges one event.
func (d *EventDedup) Add(e Event) {
	existing, ok := d.byKey[e.EventKey]
	if !ok {
		if e.EventKey == "" {
			// A record without a usable identity cannot be deduped; keep it under a
			// synthetic key so it is still counted once.
			e.EventKey = "anon|" + Sha1Hex([]any{e.SourceFile, e.TS, e.Model, e.Total})
		}
		d.byKey[e.EventKey] = e
		d.order = append(d.order, e.EventKey)
		return
	}
	merged := e
	if existing.Total >= e.Total {
		merged = existing
	}
	if existing.TS < e.TS {
		merged.TS = existing.TS
	}
	if existing.Project == "" && e.Project != "" {
		merged.Project = e.Project
	}
	if merged.LatencyMs == nil {
		merged.LatencyMs = existing.LatencyMs
	}
	if merged.TTFTMs == nil {
		merged.TTFTMs = existing.TTFTMs
	}
	if merged.CostUSD == nil {
		merged.CostUSD = existing.CostUSD
	}
	d.byKey[e.EventKey] = merged
}

// Events returns the deduped events in insertion order.
func (d *EventDedup) Events() []Event {
	out := make([]Event, 0, len(d.order))
	for _, k := range d.order {
		out = append(out, d.byKey[k])
	}
	return out
}

// Len counts distinct events.
func (d *EventDedup) Len() int { return len(d.order) }

// Reset clears the accumulator for the next file.
func (d *EventDedup) Reset() {
	d.byKey = map[string]Event{}
	d.order = d.order[:0]
}
