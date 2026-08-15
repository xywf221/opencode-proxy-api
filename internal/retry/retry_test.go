package retry

import (
	"net/http"
	"testing"
	"time"
)

func TestParseRetryAfter(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name   string
		header string
		want   time.Duration
	}{
		{"empty", "", 0},
		{"seconds_120", "120", 120 * time.Second},
		{"seconds_60", "60", 60 * time.Second},
		{"seconds_0", "0", 0},
		{"seconds_negative", "-10", 0},
		{"http_date_future", now.UTC().Add(90 * time.Second).Format(http.TimeFormat), 90 * time.Second},
		{"http_date_past", now.UTC().Add(-10 * time.Second).Format(http.TimeFormat), 0},
		{"invalid", "invalid", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseRetryAfter(tt.header)
			if tt.name == "http_date_future" {
				// Allow 2 second tolerance for future dates
				if got < 88*time.Second || got > 92*time.Second {
					t.Errorf("ParseRetryAfter(%q) = %v, want ~90s", tt.header, got)
				}
			} else if tt.name == "http_date_past" {
				// Allow small tolerance for past dates
				if got > 1*time.Second {
					t.Errorf("ParseRetryAfter(%q) = %v, want 0s", tt.header, got)
				}
			} else {
				if got != tt.want {
					t.Errorf("ParseRetryAfter(%q) = %v, want %v", tt.header, got, tt.want)
				}
			}
		})
	}
}

func TestExponentialBackoff(t *testing.T) {
	baseDelay := 15 * time.Second
	maxDelay := 2 * time.Minute

	tests := []struct {
		failures uint32
		want     time.Duration
	}{
		{0, 0},
		{1, 15 * time.Second},
		{2, 30 * time.Second},
		{3, 60 * time.Second},
		{4, 120 * time.Second}, // capped
		{5, 120 * time.Second}, // capped
		{10, 120 * time.Second}, // capped
	}

	for _, tt := range tests {
		t.Run("", func(t *testing.T) {
			got := ExponentialBackoff(tt.failures, baseDelay, maxDelay)
			if got != tt.want {
				t.Errorf("ExponentialBackoff(%d, %v, %v) = %v, want %v",
					tt.failures, baseDelay, maxDelay, got, tt.want)
			}
		})
	}
}
