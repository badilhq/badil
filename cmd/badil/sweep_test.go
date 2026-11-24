package main

import (
	"testing"
	"time"
)

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

			got, err := parseCleanupInterval(tc.raw)
			if tc.wantErr != "" {
				if err == nil || err.Error() != tc.wantErr {
					t.Fatalf("parseCleanupInterval(%q) err = %v, want %q", tc.raw, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseCleanupInterval(%q): %v", tc.raw, err)
			}
			if got != tc.want {
				t.Errorf("parseCleanupInterval(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}
