package adapters

import (
	"context"
	"net/http"
)

// GWorkspaceAdapter handles Google Workspace status.
// Endpoint: https://www.google.com/appsstatus/dashboard/incidents.json
//
// Workspace publishes the same schema as Google Cloud, so both share one parser.
type GWorkspaceAdapter struct {
	client *http.Client
	ua     string
}

const (
	gworkspaceIncidentsURL = "https://www.google.com/appsstatus/dashboard/incidents.json"
	gworkspaceBaseURL      = "https://www.google.com/appsstatus/dashboard/"
)

func NewGWorkspaceAdapter(client *http.Client, ua string) *GWorkspaceAdapter {
	return &GWorkspaceAdapter{client: client, ua: ua}
}

func (a *GWorkspaceAdapter) Fetch(ctx context.Context, p ProviderInfo) (Result, error) {
	endpoint := p.Endpoint
	if endpoint == "" {
		endpoint = gworkspaceIncidentsURL
	}
	return fetchGoogleStatus(ctx, a.client, a.ua, endpoint, gworkspaceBaseURL)
}
