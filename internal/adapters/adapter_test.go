package adapters

import (
	"testing"
	"time"
)

func TestParseTimeNeverInventsNow(t *testing.T) {
	// The old implementation returned time.Now() for anything it could not
	// parse, silently recording stale incidents as starting this minute.
	for _, in := range []string{"", "not a timestamp", "13/45/2020", "  "} {
		if got := parseTime(in); !got.IsZero() {
			t.Errorf("parseTime(%q) = %v, want zero time", in, got)
		}
	}
}

func TestParseTimeLayouts(t *testing.T) {
	tests := []struct {
		in   string
		want time.Time
	}{
		{"2026-09-01T14:44:00Z", time.Date(2026, 9, 1, 14, 44, 0, 0, time.UTC)},
		{"2026-09-01T14:44:00+00:00", time.Date(2026, 9, 1, 14, 44, 0, 0, time.UTC)},
		{"2026-09-01T16:44:00+02:00", time.Date(2026, 9, 1, 14, 44, 0, 0, time.UTC)},
		{"2026-09-01 14:44:00", time.Date(2026, 9, 1, 14, 44, 0, 0, time.UTC)},
		{"Tue, 01 Sep 2026 14:44:00 +0000", time.Date(2026, 9, 1, 14, 44, 0, 0, time.UTC)},
	}
	for _, tt := range tests {
		if got := parseTime(tt.in); !got.Equal(tt.want) {
			t.Errorf("parseTime(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestNormalizeIncidentStatus(t *testing.T) {
	resolved := []string{"resolved", "Resolved", "RESOLVED", "closed", "postmortem", " completed "}
	for _, in := range resolved {
		if got := NormalizeIncidentStatus(in); got != StatusResolved {
			t.Errorf("NormalizeIncidentStatus(%q) = %q, want %q", in, got, StatusResolved)
		}
		if !IsResolvedStatus(in) {
			t.Errorf("IsResolvedStatus(%q) = false, want true", in)
		}
	}

	// An unknown or empty status must stay visible as open, never silently
	// treated as resolved.
	for _, in := range []string{"", "investigating", "something new"} {
		if IsResolvedStatus(in) {
			t.Errorf("IsResolvedStatus(%q) = true, want false", in)
		}
	}

	if got := NormalizeIncidentStatus("upcoming"); got != StatusScheduled {
		t.Errorf("NormalizeIncidentStatus(upcoming) = %q, want %q", got, StatusScheduled)
	}
}

func TestIsRetryableError(t *testing.T) {
	// Bot protection and missing endpoints will fail identically on a retry.
	terminal := []error{
		statusError(403, nil),
		statusError(404, nil),
		statusError(401, nil),
	}
	for _, err := range terminal {
		if isRetryableError(err) {
			t.Errorf("isRetryableError(%v) = true, want false", err)
		}
	}

	if !isRetryableError(statusError(429, nil)) {
		t.Error("429 should be retryable")
	}
}

func TestLooksLikeHTML(t *testing.T) {
	if !looksLikeHTML("text/html; charset=utf-8", nil) {
		t.Error("text/html content type should be detected")
	}
	if !looksLikeHTML("", []byte("<!DOCTYPE html><html>")) {
		t.Error("doctype body should be detected")
	}
	if looksLikeHTML("application/json", []byte(`{"page":{}}`)) {
		t.Error("JSON body should not be detected as HTML")
	}
}
