package sleep

import "time"

// mediaProbeTimeout bounds one media probe, so a wedged helper binary cannot stall the
// controller's reconcile loop behind its own lock.
const mediaProbeTimeout = 3 * time.Second

// addMediaSource records one playing signal and marks the observation as playing. Sources
// are de-duplicated and kept in the order they were found, because they are only ever
// shown to the user.
func addMediaSource(m *Media, source string) {
	if source == "" {
		return
	}
	for _, existing := range m.Sources {
		if existing == source {
			return
		}
	}
	m.Playing = true
	m.Sources = append(m.Sources, source)
}
