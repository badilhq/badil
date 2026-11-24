package config

import (
	"fmt"
	"os"
	"time"
)

// minCleanupInterval is the shortest CLEANUP_INTERVAL accepted.
const minCleanupInterval = time.Minute

// LoadSweepSchedule reads CLEANUP_INTERVAL and, when it is set, CLEANUP_TTL:
// the in-server sweep's interval (0 when off) and TTL.
func LoadSweepSchedule() (time.Duration, time.Duration, error) {
	interval, err := ParseCleanupInterval(os.Getenv("CLEANUP_INTERVAL"))
	if err != nil || interval == 0 {
		return 0, 0, err
	}
	ttl, err := ParseCleanupTTL(os.Getenv("CLEANUP_TTL"))
	if err != nil {
		return 0, 0, err
	}
	return interval, ttl, nil
}

// LoadCleanupTTL reads CLEANUP_TTL (ParseCleanupTTL).
func LoadCleanupTTL() (time.Duration, error) {
	return ParseCleanupTTL(os.Getenv("CLEANUP_TTL"))
}

// ParseCleanupInterval parses CLEANUP_INTERVAL. Empty means no in-server
// sweep (0); otherwise it must be a Go duration of at least
// minCleanupInterval.
func ParseCleanupInterval(raw string) (time.Duration, error) {
	if raw == "" {
		return 0, nil
	}
	interval, err := time.ParseDuration(raw)
	if err != nil || interval < minCleanupInterval {
		return 0, fmt.Errorf("CLEANUP_INTERVAL must be a duration of at least 1m, got %q", raw)
	}
	return interval, nil
}

// ParseCleanupTTL parses CLEANUP_TTL. Empty means no expiry (0); otherwise it
// must be a positive Go duration.
func ParseCleanupTTL(raw string) (time.Duration, error) {
	if raw == "" {
		return 0, nil
	}
	ttl, err := time.ParseDuration(raw)
	if err != nil || ttl <= 0 {
		return 0, fmt.Errorf("CLEANUP_TTL must be a positive duration, got %q", raw)
	}
	return ttl, nil
}
