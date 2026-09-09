package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// GenericJSONAdapter handles custom JSON status APIs that don't fit
// other adapter patterns. It looks for common status indicators in
// JSON responses:
// - "level": "Good"/"Bad" 
// - "status": "operational"/"degraded"/etc.
// - Region-level status objects
type GenericJSONAdapter struct {
	client *http.Client
	ua     string
}

// NewGenericJSONAdapter creates a generic JSON status adapter.
func NewGenericJSONAdapter(client *http.Client, ua string) *GenericJSONAdapter {
	return &GenericJSONAdapter{client: client, ua: ua}
}

// Fetch implements the Adapter interface for custom JSON status pages.
func (a *GenericJSONAdapter) Fetch(ctx context.Context, p ProviderInfo) (Result, error) {
	endpoint := p.Endpoint
	if endpoint == "" {
		return Result{}, fmt.Errorf("no endpoint for provider %s", p.Name)
	}

	httpResp, err := httpGet(ctx, a.client, a.ua, endpoint, "application/json")
	if err != nil {
		return Result{HTTPStatus: httpResp.StatusCode}, err
	}
	data := httpResp.Body

	result := Result{
		HTTPStatus: httpResp.StatusCode,
		Indicator:  IndicatorUnknown,
	}

	// Try to parse as JSON
	var raw interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return result, fmt.Errorf("failed to parse JSON: %w", err)
	}

	// Look for status patterns in the JSON
	result.Indicator = a.extractStatus(raw)

	return result, nil
}

// extractStatus recursively searches JSON for status indicators
func (a *GenericJSONAdapter) extractStatus(data interface{}) Indicator {
	switch v := data.(type) {
	case map[string]interface{}:
		// Check for direct status fields
		for key, val := range v {
			lk := strings.ToLower(key)
			if strings.Contains(lk, "status") || strings.Contains(lk, "level") || strings.Contains(lk, "indicator") {
				if s, ok := val.(string); ok {
					if ind := mapGenericStatus(s); ind != IndicatorUnknown {
						return ind
					}
				}
			}
			
			// Check string values that might be status indicators
			// (handles keys like "us", "uk", "region" with "operational" values)
			if s, ok := val.(string); ok {
				if ind := mapGenericStatus(s); ind != IndicatorUnknown {
					return ind
				}
			}
		}
		
		// Recursively check nested objects
		for _, val := range v {
			if ind := a.extractStatus(val); ind != IndicatorUnknown {
				return ind
			}
		}
		
	case []interface{}:
		for _, item := range v {
			if ind := a.extractStatus(item); ind != IndicatorUnknown {
				return ind
			}
		}
	}
	
	return IndicatorUnknown
}

// mapGenericStatus maps various status strings to our Indicator type
func mapGenericStatus(s string) Indicator {
	switch strings.ToLower(s) {
	case "none", "operational", "healthy", "success", "good", "all_good":
		return IndicatorNone
	case "minor", "degraded", "warning", "performance":
		return IndicatorMinor
	case "major", "partial_outage", "major_outage", "bad":
		return IndicatorMajor
	case "critical", "full_outage":
		return IndicatorCritical
	case "maintenance", "under_maintenance":
		return IndicatorMaintenance
	default:
		return IndicatorUnknown
	}
}
