package adapters

import (
	"context"
)

// AWSAdapter handles AWS Health status
// Endpoint: https://health.aws.amazon.com/public/currentevents
// Note: AWS returns HTML, so we return unknown indicator and let users check manually
type AWSAdapter struct {
	ua string
}

func NewAWSAdapter(client interface{}, ua string) *AWSAdapter {
	return &AWSAdapter{ua: ua}
}

func (a *AWSAdapter) Fetch(ctx context.Context, p ProviderInfo) (Result, error) {
	// AWS returns HTML at the currentevents endpoint, not structured JSON
	// We return unknown indicator - users should visit the AWS Health Dashboard directly
	return Result{
		Indicator: IndicatorUnknown,
	}, nil
}
