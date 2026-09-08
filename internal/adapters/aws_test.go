package adapters

import (
	"bytes"
	"context"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"testing"
	"unicode/utf16"
)

// encodeUTF16LE mirrors how the AWS Health Dashboard actually serves its JSON.
func encodeUTF16LE(s string) []byte {
	var b bytes.Buffer
	b.Write([]byte{0xFF, 0xFE})
	for _, unit := range utf16.Encode([]rune(s)) {
		binary.Write(&b, binary.LittleEndian, unit)
	}
	return b.Bytes()
}

func TestDecodeUTF16(t *testing.T) {
	if got := string(decodeUTF16(encodeUTF16LE(`{"a":1}`))); got != `{"a":1}` {
		t.Errorf("decodeUTF16 = %q", got)
	}
	// Plain UTF-8 must pass through untouched.
	if got := string(decodeUTF16([]byte(`{"a":1}`))); got != `{"a":1}` {
		t.Errorf("decodeUTF16 mangled UTF-8: %q", got)
	}
}

const awsEventsJSON = `[
  {"date": "1772369485", "arn": "arn:aws:health:me-central-1::event/x", "region_name": "UAE",
   "service_name": "Multiple services", "status": "3", "summary": "Increased Error Rates",
   "event_log": [{"summary": "Increased Error Rates", "message": "We are investigating.", "status": 1, "timestamp": 1772369485}],
   "impacted_services": {"rds-me-central-1": {"service_name": "RDS", "current": "3", "max": "3"}}}
]`

// AWS serves JSON, not HTML. The previous adapter substring-matched the body
// for "maintenance" and reported IndicatorNone whenever the page merely loaded.
func TestAWSParsesEventsAndReportsOutage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json;charset=utf-16")
		w.Write(encodeUTF16LE(awsEventsJSON))
	}))
	defer srv.Close()

	a := NewAWSAdapter(srv.Client(), "belltower-test")
	res, err := a.Fetch(context.Background(), ProviderInfo{ID: "aws", Endpoint: srv.URL})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	if res.Indicator != IndicatorMajor {
		t.Errorf("Indicator = %q, want %q for a status-3 service disruption", res.Indicator, IndicatorMajor)
	}
	if len(res.Incidents) != 1 {
		t.Fatalf("got %d incidents, want 1", len(res.Incidents))
	}
	inc := res.Incidents[0]
	if inc.Title == "" || inc.ExtID == "" {
		t.Errorf("incident missing title/id: %+v", inc)
	}
	if inc.StartedAt.IsZero() {
		t.Error("incident should have a start time parsed from the unix date")
	}
	if inc.Body != "We are investigating." {
		t.Errorf("body = %q, want the latest event_log message", inc.Body)
	}
	if len(res.Components) != 1 || res.Components[0].Status != "major_outage" {
		t.Errorf("components = %+v, want one major_outage", res.Components)
	}
}

func TestAWSNoEventsIsOperational(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(encodeUTF16LE(`[]`))
	}))
	defer srv.Close()

	res, err := NewAWSAdapter(srv.Client(), "t").Fetch(context.Background(), ProviderInfo{Endpoint: srv.URL})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if res.Indicator != IndicatorNone {
		t.Errorf("Indicator = %q, want %q", res.Indicator, IndicatorNone)
	}
}
