package config_test

import (
	"os"
	"testing"
	"time"

	"github.com/badilhq/badil/internal/config"
)

// unsetEnv unsets keys for the test, restoring them when it ends.
func unsetEnv(t *testing.T, keys ...string) {
	t.Helper()
	for _, k := range keys {
		t.Setenv(k, "")
		if err := os.Unsetenv(k); err != nil {
			t.Fatal(err)
		}
	}
}

func TestParseCleanupTTL(t *testing.T) {
	t.Parallel()

	cases := []struct {
		raw     string
		want    time.Duration
		wantErr string
	}{
		{raw: "", want: 0},
		{raw: "168h", want: 168 * time.Hour},
		{raw: "0", wantErr: `CLEANUP_TTL must be a positive duration, got "0"`},
		{raw: "-1h", wantErr: `CLEANUP_TTL must be a positive duration, got "-1h"`},
		{raw: "abc", wantErr: `CLEANUP_TTL must be a positive duration, got "abc"`},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			t.Parallel()

			got, err := config.ParseCleanupTTL(tc.raw)
			if tc.wantErr != "" {
				if err == nil || err.Error() != tc.wantErr {
					t.Fatalf("ParseCleanupTTL(%q) err = %v, want %q", tc.raw, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseCleanupTTL(%q): %v", tc.raw, err)
			}
			if got != tc.want {
				t.Errorf("ParseCleanupTTL(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

func TestParseCleanupInterval(t *testing.T) {
	t.Parallel()

	cases := []struct {
		raw     string
		want    time.Duration
		wantErr string
	}{
		{raw: "", want: 0},
		{raw: "1m", want: time.Minute},
		{raw: "1h", want: time.Hour},
		{raw: "30s", wantErr: `CLEANUP_INTERVAL must be a duration of at least 1m, got "30s"`},
		{raw: "abc", wantErr: `CLEANUP_INTERVAL must be a duration of at least 1m, got "abc"`},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			t.Parallel()

			got, err := config.ParseCleanupInterval(tc.raw)
			if tc.wantErr != "" {
				if err == nil || err.Error() != tc.wantErr {
					t.Fatalf("ParseCleanupInterval(%q) err = %v, want %q", tc.raw, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseCleanupInterval(%q): %v", tc.raw, err)
			}
			if got != tc.want {
				t.Errorf("ParseCleanupInterval(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

// TestLoadSweepSchedule checks CLEANUP_TTL is read only when CLEANUP_INTERVAL
// is set and valid, with the texts of ParseCleanupInterval and ParseCleanupTTL.
func TestLoadSweepSchedule(t *testing.T) {
	cases := []struct {
		name                  string
		env                   map[string]string
		wantInterval, wantTTL time.Duration
		wantErr               string
	}{
		{name: "both unset", env: map[string]string{}},
		{name: "ttl without interval", env: map[string]string{"CLEANUP_TTL": "abc"}},
		{
			name:    "interval too short",
			env:     map[string]string{"CLEANUP_INTERVAL": "30s"},
			wantErr: `CLEANUP_INTERVAL must be a duration of at least 1m, got "30s"`,
		},
		{
			name:    "invalid ttl",
			env:     map[string]string{"CLEANUP_INTERVAL": "1h", "CLEANUP_TTL": "0"},
			wantErr: `CLEANUP_TTL must be a positive duration, got "0"`,
		},
		{
			name:         "interval and ttl",
			env:          map[string]string{"CLEANUP_INTERVAL": "1h", "CLEANUP_TTL": "168h"},
			wantInterval: time.Hour, wantTTL: 168 * time.Hour,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			unsetEnv(t, "CLEANUP_INTERVAL", "CLEANUP_TTL")
			setEnv(t, tc.env)
			interval, ttl, err := config.LoadSweepSchedule()
			gotErr := ""
			if err != nil {
				gotErr = err.Error()
			}
			if interval != tc.wantInterval || ttl != tc.wantTTL || gotErr != tc.wantErr {
				t.Errorf("LoadSweepSchedule() = %v, %v, %q; want %v, %v, %q",
					interval, ttl, gotErr, tc.wantInterval, tc.wantTTL, tc.wantErr)
			}
		})
	}
}

func TestLoadCleanupTTL(t *testing.T) {
	t.Run("unset", func(t *testing.T) {
		unsetEnv(t, "CLEANUP_TTL")
		ttl, err := config.LoadCleanupTTL()
		if err != nil || ttl != 0 {
			t.Errorf("LoadCleanupTTL() = %v, %v, want 0, nil", ttl, err)
		}
	})
	t.Run("negative", func(t *testing.T) {
		t.Setenv("CLEANUP_TTL", "-1h")
		const want = `CLEANUP_TTL must be a positive duration, got "-1h"`
		if _, err := config.LoadCleanupTTL(); err == nil || err.Error() != want {
			t.Errorf("LoadCleanupTTL() err = %v, want %q", err, want)
		}
	})
}
