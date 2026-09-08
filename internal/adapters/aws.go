package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
	"unicode/utf16"
	"unicode/utf8"
)

// AWSAdapter handles the AWS Health Dashboard.
//
// https://health.aws.amazon.com/public/currentevents returns a JSON array of
// the currently open events - but encoded as UTF-16 with a BOM, which is why
// it superficially looks like binary garbage and was previously treated as an
// HTML page to substring-match.
type AWSAdapter struct {
	client *http.Client
	ua     string
}

const awsCurrentEventsURL = "https://health.aws.amazon.com/public/currentevents"

// awsEvent is one entry in the currentevents array.
type awsEvent struct {
	ARN         string `json:"arn"`
	Date        string `json:"date"` // unix seconds, as a string
	RegionName  string `json:"region_name"`
	Service     string `json:"service"`
	ServiceName string `json:"service_name"`
	Status      string `json:"status"`
	Summary     string `json:"summary"`
	EventLog    []struct {
		Summary   string `json:"summary"`
		Message   string `json:"message"`
		Status    int    `json:"status"`
		Timestamp int64  `json:"timestamp"`
	} `json:"event_log"`
	ImpactedServices map[string]struct {
		ServiceName string `json:"service_name"`
		Current     string `json:"current"`
		Max         string `json:"max"`
	} `json:"impacted_services"`
}

// AWS Health status codes, per the dashboard's own legend.
const (
	awsStatusOperational   = 0
	awsStatusInformational = 1
	awsStatusDegradation   = 2
	awsStatusDisruption    = 3
)

func NewAWSAdapter(client *http.Client, ua string) *AWSAdapter {
	return &AWSAdapter{client: client, ua: ua}
}

func (a *AWSAdapter) Fetch(ctx context.Context, p ProviderInfo) (Result, error) {
	endpoint := p.Endpoint
	if endpoint == "" {
		endpoint = awsCurrentEventsURL
	}

	resp, err := httpGet(ctx, a.client, a.ua, endpoint, "application/json")
	if err != nil {
		return Result{HTTPStatus: resp.StatusCode}, err
	}

	var events []awsEvent
	if err := json.Unmarshal(decodeUTF16(resp.Body), &events); err != nil {
		return Result{HTTPStatus: resp.StatusCode}, fmt.Errorf("failed to parse AWS response: %w", err)
	}

	result := Result{Indicator: IndicatorNone, HTTPStatus: resp.StatusCode}

	worst := awsStatusOperational
	for _, ev := range events {
		code, convErr := strconv.Atoi(ev.Status)
		if convErr != nil {
			continue
		}
		if code > worst {
			worst = code
		}

		title := ev.Summary
		if ev.ServiceName != "" {
			title = fmt.Sprintf("%s (%s): %s", ev.ServiceName, ev.RegionName, ev.Summary)
		}

		body := ""
		if n := len(ev.EventLog); n > 0 {
			body = ev.EventLog[n-1].Message
		}

		var startedAt time.Time
		if secs, convErr := strconv.ParseInt(ev.Date, 10, 64); convErr == nil {
			startedAt = time.Unix(secs, 0).UTC()
		}

		result.Incidents = append(result.Incidents, Incident{
			ExtID:     ev.ARN,
			Title:     title,
			Body:      body,
			Status:    awsIncidentStatus(code),
			Impact:    awsImpact(code),
			StartedAt: startedAt,
			URL:       "https://health.aws.amazon.com/health/status",
		})

		for _, svc := range ev.ImpactedServices {
			if svc.ServiceName == "" {
				continue
			}
			result.Components = append(result.Components, Component{
				Name:   fmt.Sprintf("%s (%s)", svc.ServiceName, ev.RegionName),
				Status: awsComponentStatus(svc.Current),
			})
		}
	}

	switch worst {
	case awsStatusDisruption:
		result.Indicator = IndicatorMajor
	case awsStatusDegradation:
		result.Indicator = IndicatorMinor
	}

	return result, nil
}

func awsIncidentStatus(code int) string {
	if code == awsStatusOperational {
		return StatusResolved
	}
	return StatusOpen
}

func awsImpact(code int) string {
	switch code {
	case awsStatusDisruption:
		return "major"
	case awsStatusDegradation:
		return "minor"
	case awsStatusInformational:
		return "none"
	default:
		return "none"
	}
}

func awsComponentStatus(current string) string {
	switch current {
	case "3":
		return "major_outage"
	case "2":
		return "degraded_performance"
	case "1":
		return "operational"
	default:
		return "operational"
	}
}

// decodeUTF16 converts a UTF-16 body (with either BOM) to UTF-8. Input that is
// already UTF-8 is returned untouched, so the function is safe to apply
// unconditionally.
func decodeUTF16(b []byte) []byte {
	if len(b) < 2 {
		return b
	}

	var littleEndian bool
	switch {
	case b[0] == 0xFF && b[1] == 0xFE:
		littleEndian = true
	case b[0] == 0xFE && b[1] == 0xFF:
		littleEndian = false
	default:
		return b
	}

	body := b[2:]
	units := make([]uint16, 0, len(body)/2)
	for i := 0; i+1 < len(body); i += 2 {
		if littleEndian {
			units = append(units, uint16(body[i])|uint16(body[i+1])<<8)
		} else {
			units = append(units, uint16(body[i])<<8|uint16(body[i+1]))
		}
	}

	var out bytes.Buffer
	out.Grow(len(units))
	for _, r := range utf16.Decode(units) {
		out.WriteRune(r)
	}
	if !utf8.Valid(out.Bytes()) {
		return b
	}
	return out.Bytes()
}
