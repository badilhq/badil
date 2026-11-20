package status

import "time"

// FlushInterval is the shortest time between two writes of a preview's
// ConfigMap.
const FlushInterval = flushInterval

// SetInterval makes s's writer flush every d. It must be called before
// Start.
func SetInterval(s *Store, d time.Duration) {
	s.interval = d
}

// Persisted reports whether s keeps the status in ConfigMaps.
func Persisted(s *Store) bool {
	return s.kube != nil
}
