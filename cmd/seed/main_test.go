package main

import (
	"strings"
	"testing"
)

// TestDetermineAdapterManualOverrides verifies that known false-positive
// statuspage providers are classified as manual (not statuspage) via the
// adapterOverride map. These were previously swallowed by the status.* URL
// catch-all and silently polled against a non-existent API.
func TestDetermineAdapterManualOverrides(t *testing.T) {
	cases := []string{
		"8x8", "Admiral", "Adobe", "Auth0", "Automox", "Bitwarden",
		"Blumira", "CDN77", "ColoBlxs", "Crexendo", "CyberArk", "Fastly",
		"Freshworks", "Hetzner", "Hugging Face", "LastPass", "Level",
		"ManageEngine", "Mistral", "MSPbots", "N-able", "Okta",
		"PagerDuty", "PayPal", "Redis", "Rewst", "RingCentral",
		"Servosity", "SonicWall", "Sophos", "Syncro", "UptimeRobot",
		"VIPRE", "ZeroTier", "Zoho",
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			adapter, endpoint, notes := determineAdapter(name, "https://status.example.com/")
			if adapter != "manual" {
				t.Errorf("%s: adapter = %q, want manual (false positive caught by status.* catch-all)", name, adapter)
			}
			if endpoint != "" {
				t.Errorf("%s: endpoint = %q, want empty", name, endpoint)
			}
			if notes == "" {
				t.Errorf("%s: expected non-empty notes", name)
			}
		})
	}
}

func TestDetermineAdapterCDN77NotBetterstack(t *testing.T) {
	// CDN77's URL contains "cdn77" which the old BetterStack URL check matched,
	// but the API endpoint returns 404.
	adapter, _, notes := determineAdapter("CDN77", "https://client.cdn77.com/support/status")
	if adapter != "manual" {
		t.Errorf("CDN77: adapter = %q, want manual", adapter)
	}
	if notes == "" {
		t.Error("CDN77: expected notes explaining why it's manual")
	}
}

func TestDetermineAdapterLevelNotStatuspage(t *testing.T) {
	// Level's URL is https://status.level.io/en which the catch-all would
	// classify as statuspage, but it's actually a custom platform.
	adapter, _, _ := determineAdapter("Level", "https://status.level.io/en")
	if adapter != "manual" {
		t.Errorf("Level: adapter = %q, want manual", adapter)
	}
}

func TestDetermineAdapterAzureDevOpsNotStatuspage(t *testing.T) {
	adapter, _, notes := determineAdapter("Microsoft Azure DevOps", "https://status.dev.azure.com/")
	if adapter != "manual" {
		t.Errorf("Azure DevOps: adapter = %q, want manual", adapter)
	}
	if notes == "" {
		t.Error("Azure DevOps: expected notes")
	}
}

func TestDetermineAdapterXAIIsRSS(t *testing.T) {
	adapter, endpoint, notes := determineAdapter("xAI", "https://status.x.ai/")
	if adapter != "rss" {
		t.Errorf("xAI: adapter = %q, want rss", adapter)
	}
	if endpoint != "https://status.x.ai/feed.xml" {
		t.Errorf("xAI: endpoint = %q, want RSS feed URL", endpoint)
	}
	if notes == "" {
		t.Error("xAI: expected notes about RSS feed")
	}
}

func TestDetermineAdapterVultrCustomEndpoint(t *testing.T) {
	// Vultr serves a custom JSON endpoint, not the StatusPage summary path.
	adapter, endpoint, notes := determineAdapter("Vultr", "https://status.vultr.com/")
	if adapter != "statuspage" {
		t.Errorf("Vultr: adapter = %q, want statuspage", adapter)
	}
	if endpoint != "https://status.vultr.com/status.json" {
		t.Errorf("Vultr: endpoint = %q, want https://status.vultr.com/status.json", endpoint)
	}
	if notes == "" {
		t.Error("Vultr: expected notes about custom JSON API")
	}
}

func TestDetermineAdapterStatusPageByDefault(t *testing.T) {
	// A typical status.<name>.com URL should still classify as statuspage.
	adapter, endpoint, _ := determineAdapter("Example", "https://status.example.com/")
	if adapter != "statuspage" {
		t.Errorf("Example: adapter = %q, want statuspage", adapter)
	}
	if endpoint != "https://status.example.com/api/v2/summary.json" {
		t.Errorf("Example: endpoint = %q, want standard StatusPage endpoint", endpoint)
	}
}

func TestDetermineAdapterStatusIOWithHardcodedIDs(t *testing.T) {
	cases := []struct {
		name   string
		csvURL string
		pageID string
	}{
		{"GitLab", "https://status.gitlab.com/", "5b36dc6502d06804c08349f7"},
		{"Mimecast", "https://status.mimecast.com/", "5d849b1c02e65b3ec45369d4"},
		{"ConnectWise", "https://status.connectwise.com", "619cf82551fec9053d612f09"},
		{"Let's Encrypt", "https://letsencrypt.status.io/", "55957a99e800baa4470002da"},
		{"HaloPSA", "https://status.haloservicesolutions.com", "63ef45da7ee94905308a1a4a"},
		{"Hornetsecurity", "https://live.hornet-status.com/", "591aaa7fe69f388425000fda"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			adapter, endpoint, _ := determineAdapter(tc.name, tc.csvURL)
			if adapter != "statusio" {
				t.Errorf("%s: adapter = %q, want statusio", tc.name, adapter)
			}
			want := "https://api.status.io/1.0/status/" + tc.pageID
			if endpoint != want {
				t.Errorf("%s: endpoint = %q, want %q (hardcoded page ID from PLAN.md)", tc.name, endpoint, want)
			}
		})
	}
}

func TestDetermineAdapterBetterStackByName(t *testing.T) {
	cases := []struct {
		name   string
		csvURL string
	}{
		{"CloudRadial", "https://status.cloudradial.com/"},
		{"Quad9", "https://uptime.quad9.net/"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			adapter, endpoint, _ := determineAdapter(tc.name, tc.csvURL)
			if adapter != "betterstack" {
				t.Errorf("%s: adapter = %q, want betterstack", tc.name, adapter)
			}
			// URL is normalized (trailing slash stripped) before endpoint is appended.
			want := strings.TrimSuffix(tc.csvURL, "/") + "/index.json"
			if endpoint != want {
				t.Errorf("%s: endpoint = %q, want %q", tc.name, endpoint, want)
			}
		})
	}
}

func TestDetermineAdapterSorryAppByName(t *testing.T) {
	cases := []struct {
		name   string
		csvURL string
	}{
		{"Broadcom (VMware)", "https://status.broadcom.com/"},
		{"Pingdom", "https://status.pingdom.com/"},
		{"Postmark", "https://status.postmarkapp.com/"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			adapter, endpoint, _ := determineAdapter(tc.name, tc.csvURL)
			if adapter != "sorryapp" {
				t.Errorf("%s: adapter = %q, want sorryapp", tc.name, adapter)
			}
			want := strings.TrimSuffix(tc.csvURL, "/") + "/api/v1/status"
			if endpoint != want {
				t.Errorf("%s: endpoint = %q, want %q", tc.name, endpoint, want)
			}
		})
	}
}

func TestDetermineAdapterHandRolledMajors(t *testing.T) {
	cases := []struct {
		name, url, wantAdapter, wantEndpoint string
	}{
		{"Google Cloud", "https://status.cloud.google.com", "gcp", "https://status.cloud.google.com/incidents.json"},
		{"Salesforce", "https://status.salesforce.com", "salesforce", "https://api.status.salesforce.com/v1/incidents"},
		{"Slack", "https://slack-status.com", "slack", "https://slack-status.com/api/v2.0.0/current"},
		{"Heroku", "https://status.heroku.com", "heroku", "https://status.heroku.com/api/v4/current-status"},
		{"Azure", "https://azure.status.microsoft/en-us/status", "rss", "https://azurestatuscdn.azureedge.net/en-us/status/feed/"},
		{"Google Workspace", "https://www.google.com/appsstatus/dashboard", "gworkspace", "https://www.google.com/appsstatus/dashboard/incidents.json"},
		{"Amazon AWS", "https://health.aws.amazon.com/health/status", "aws", "https://health.aws.amazon.com/public/currentevents"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			adapter, endpoint, _ := determineAdapter(tc.name, tc.url)
			if adapter != tc.wantAdapter {
				t.Errorf("%s: adapter = %q, want %q", tc.name, adapter, tc.wantAdapter)
			}
			if endpoint != tc.wantEndpoint {
				t.Errorf("%s: endpoint = %q, want %q", tc.name, endpoint, tc.wantEndpoint)
			}
		})
	}
}

func TestDetermineAdapterNoURLDisables(t *testing.T) {
	// Providers with no status page URL are marked manual and disabled.
	for _, name := range []string{"Ramp", "Hudu", "Blackpoint Cyber"} {
		t.Run(name, func(t *testing.T) {
			adapter, endpoint, notes := determineAdapter(name, "No official public status page found")
			if adapter != "manual" {
				t.Errorf("%s: adapter = %q, want manual", name, adapter)
			}
			if endpoint != "" {
				t.Errorf("%s: endpoint = %q, want empty", name, endpoint)
			}
			if notes != "No dedicated status page" {
				t.Errorf("%s: notes = %q, want \"No dedicated status page\"", name, notes)
			}
		})
	}
}

func TestDetermineAdapterMicrosoft365OutOfScope(t *testing.T) {
	adapter, _, notes := determineAdapter("Microsoft 365", "https://status.cloud.microsoft/")
	if adapter != "manual" {
		t.Errorf("Microsoft 365: adapter = %q, want manual", adapter)
	}
	if !strings.Contains(notes, "out of scope") {
		t.Errorf("Microsoft 365: notes = %q, want it to mention 'out of scope'", notes)
	}
}

func TestDetermineAdapterOracleCloudRSS(t *testing.T) {
	adapter, endpoint, _ := determineAdapter("Oracle Cloud", "https://ocistatus.oraclecloud.com")
	if adapter != "rss" {
		t.Errorf("Oracle Cloud: adapter = %q, want rss", adapter)
	}
	if endpoint != "https://ocistatus.oraclecloud.com/api/v2/incident-summary.rss" {
		t.Errorf("Oracle Cloud: endpoint = %q, want RSS feed URL", endpoint)
	}
}

func TestDetermineAdapterIBMCloudRSS(t *testing.T) {
	adapter, endpoint, _ := determineAdapter("IBM Cloud", "https://cloud.ibm.com/status")
	if adapter != "rss" {
		t.Errorf("IBM Cloud: adapter = %q, want rss", adapter)
	}
	if endpoint != "https://cloud.ibm.com/status/api/notifications/feed.rss" {
		t.Errorf("IBM Cloud: endpoint = %q, want RSS feed URL", endpoint)
	}
}

func TestDetermineAdapterAppleManual(t *testing.T) {
	adapter, _, _ := determineAdapter("Apple", "https://www.apple.com/support/status/data/system_status_en_US.js")
	if adapter != "manual" {
		t.Errorf("Apple: adapter = %q, want manual", adapter)
	}
}

func TestDetermineAdapterZscalerManual(t *testing.T) {
	adapter, _, _ := determineAdapter("Zscaler", "https://trust.zscaler.com/")
	if adapter != "manual" {
		t.Errorf("Zscaler: adapter = %q, want manual", adapter)
	}
}

func TestDetermineAdapterStatusIOURLFallback(t *testing.T) {
	// A URL containing "status.io" should be classified as statusio with the URL as endpoint.
	adapter, endpoint, _ := determineAdapter("SomeService", "https://someservice.status.io")
	if adapter != "statusio" {
		t.Errorf("status.io URL: adapter = %q, want statusio", adapter)
	}
	if endpoint != "https://someservice.status.io" {
		t.Errorf("status.io URL: endpoint = %q, want the URL itself", endpoint)
	}
}

func TestDetermineAdapterInstatusURLFallback(t *testing.T) {
	adapter, endpoint, _ := determineAdapter("SomeService", "https://someservice.instatus.com")
	if adapter != "instatus" {
		t.Errorf("instatus URL: adapter = %q, want instatus", adapter)
	}
	want := "https://someservice.instatus.com/summary.json"
	if endpoint != want {
		t.Errorf("instatus URL: endpoint = %q, want %q", endpoint, want)
	}
}

func TestDetermineAdapterDefaultIsManual(t *testing.T) {
	// An unrecognized URL pattern falls through to manual.
	adapter, _, notes := determineAdapter("UnknownCo", "https://somerandomsite.com/status")
	if adapter != "manual" {
		t.Errorf("UnknownCo: adapter = %q, want manual", adapter)
	}
	if !strings.Contains(notes, "review manually") {
		t.Errorf("UnknownCo: notes = %q, want it to mention 'review manually'", notes)
	}
}

func TestSlugify(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Hello World", "hello-world"},
		{"N-able", "nable"}, // hyphens are not preserved by slugify
		{"8x8", "8x8"},
	}
	for _, tc := range cases {
		if got := slugify(tc.in); got != tc.want {
			t.Errorf("slugify(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
