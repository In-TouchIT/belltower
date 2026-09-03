package main

import (
	"encoding/csv"
	"fmt"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Provider represents a row in providers.yaml
type Provider struct {
	ID       string `yaml:"id"`
	Name     string `yaml:"name"`
	Category string `yaml:"category"`
	PageURL  string `yaml:"page_url,omitempty"`
	Adapter  string `yaml:"adapter"`
	Endpoint string `yaml:"endpoint,omitempty"`
	Tier     int    `yaml:"tier"`
	Enabled  bool   `yaml:"enabled"`
	Notes    string `yaml:"notes,omitempty"`
}

// determineAdapter maps a provider to the appropriate adapter based on URL patterns.
// The logic follows the research documented in PLAN.md for the 150 covered providers.
func determineAdapter(name, url string) (adapter, endpoint, notes string) {
	// Handle no URL cases
	if url == "" || strings.HasPrefix(url, "No") || strings.Contains(url, "No public") {
		return "manual", "", "No dedicated status page"
	}

	// Normalize URL
	url = strings.TrimSuffix(strings.TrimSpace(url), "/")
	lowerName := strings.ToLower(name)

	// Direct URL matches for hand-rolled adapters (from PLAN.md "Hand-rolled majors")
	switch url {
	case "https://status.cloud.google.com":
		return "gcp", "https://status.cloud.google.com/incidents.json", ""
	case "https://status.salesforce.com":
		return "salesforce", "https://api.status.salesforce.com/v1/incidents", ""
	case "https://slack-status.com":
		return "slack", "https://slack-status.com/api/v2.0.0/current", ""
	case "https://status.heroku.com":
		return "heroku", "https://status.heroku.com/api/v4/current-status", ""
	case "https://azure.status.microsoft/en-us/status":
		return "rss", "https://azurestatuscdn.azureedge.net/en-us/status/feed/", ""
	case "https://www.google.com/appsstatus/dashboard":
		return "gworkspace", "https://www.google.com/appsstatus/dashboard/incidents.json", ""
	case "https://health.aws.amazon.com/health/status":
		return "aws", "https://health.aws.amazon.com/public/currentevents", ""
	}

	// Platform-specific detection (exact matches from PLAN.md)
	switch {
	case strings.Contains(url, "lets encrypt") || strings.Contains(url, "letsencrypt.status"):
		return "statusio", "https://api.status.io/1.0/status/55957a99e800baa4470002da", ""
	case strings.Contains(url, "status.io") || strings.Contains(url, "statusio"):
		return "statusio", url, ""
	case strings.Contains(url, "instatus.com"):
		return "instatus", url+"summary.json", ""
	case strings.Contains(url, "betterstack.com") || strings.Contains(url, "cdn77"):
		return "betterstack", url+"/index.json", ""
	case strings.Contains(url, "sorryapp.com"):
		return "sorryapp", url+"/api/v1/status", ""
	case strings.Contains(url, "statuspage.io") || strings.Contains(url, ".statuspage.dev"):
		return "statuspage", url+"/api/v2/summary.json", ""
	}

	// Special case: Oracle Cloud uses a custom React app with RSS feed, not StatusPage
	if lowerName == "oracle cloud" || strings.Contains(strings.ToLower(url), "ocistatus") {
		return "rss", "https://ocistatus.oraclecloud.com/api/v2/incident-summary.rss", ""
	}

	// Special case handling based on provider name
	// Microsoft 365 - requires Graph tenant auth, out of scope for v1
	if lowerName == "microsoft 365" || lowerName == "m365" {
		return "manual", "", "Microsoft 365 requires Graph tenant auth - out of scope for v1"
	}

	// Azure DevOps uses a different endpoint
	if lowerName == "microsoft azure devops" {
		return "statuspage", "https://status.dev.azure.com/api/v2/summary.json", ""
	}

	// SendGrid shares Twilio's status page
	if strings.Contains(lowerName, "sendgrid") {
		return "manual", "", "Uses Twilio status page (SendGrid is under Twilio)"
	}

	// Broadcom duplicates - VMware and Symantec both use same URL
	if lowerName == "broadcom symantec" {
		return "manual", "", "Shares endpoint with Broadcom VMware - check status.broadcom.com manually"
	}

	// Status.io providers from PLAN.md research
	statusioProviders := map[string]string{
		"gitlab":         "5b36dc6502d06804c08349f7",
		"mimecast":       "5d849b1c02e65b3ec45369d4",
		"connectwise":    "619cf82551fec9053d612f09",
		"let's encrypt":  "55957a99e800baa4470002da",
		"halopsa":        "63ef45da7ee94905308a1a4a",
		"hornet":         "591aaa7fe69f388425000fda",
	}

	// Check if this provider uses status.io (based on name match from research)
	for providerName, pageID := range statusioProviders {
		if strings.Contains(lowerName, providerName) {
			return "statusio", "https://api.status.io/1.0/status/" + pageID, ""
		}
	}

	// Betterstack/Instatus providers from PLAN.md
	betterstackProviders := []string{"cloudradial", "level.io", "quad9"}
	for _, p := range betterstackProviders {
		if strings.Contains(lowerName, p) {
			return "betterstack", url+"/index.json", ""
		}
	}

	instatusProviders := []string{}
	for _, p := range instatusProviders {
		if strings.Contains(lowerName, p) {
			return "instatus", url+"summary.json", ""
		}
	}

	// Sorryapp providers from PLAN.md
	sorryappProviders := []string{"broadcom", "pingdom", "postmark"}
	for _, p := range sorryappProviders {
		if strings.Contains(lowerName, p) && !strings.Contains(lowerName, "symantec") {
			return "sorryapp", url+"/api/v1/status", ""
		}
	}

	// Try StatusPage.io pattern for remaining URLs
	// Many status pages follow the pattern status.<provider>.com which are StatusPage instances
	// We'll try the /api/v2/summary.json endpoint for these
	if strings.HasPrefix(url, "https://status.") || strings.Contains(url, "status.") {
		// These are likely StatusPage instances that we can verify by trying the API endpoint
		return "statuspage", url+"/api/v2/summary.json", ""
	}

	// Default to manual for unknown platforms
	return "manual", "", "Unknown status page platform - review manually"
}

// ProviderSort sorts providers by name for consistent output
type ByName []Provider

func (a ByName) Len() int           { return len(a) }
func (a ByName) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }
func (a ByName) Less(i, j int) bool { return a[i].Name < a[j].Name }

func main() {
	csvPath := "Status Pages Master.csv"
	if _, err := os.Stat(csvPath); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Error: %s not found in current directory\n", csvPath)
		os.Exit(1)
	}

	file, err := os.Open(csvPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening CSV: %v\n", err)
		os.Exit(1)
	}
	defer file.Close()

	reader := csv.NewReader(file)
	reader.FieldsPerRecord = -1
	records, err := reader.ReadAll()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading CSV: %v\n", err)
		os.Exit(1)
	}

	if len(records) < 2 {
		fmt.Fprintln(os.Stderr, "CSV file must have a header and at least one row")
		os.Exit(1)
	}

	header := records[0]
	fmt.Printf("CSV Header: %v\n", header)
	fmt.Printf("Total rows (excluding header): %d\n", len(records)-1)

	var providers []Provider
	seenEndpoints := make(map[string]bool) // For deduplication based on endpoint

	for i, record := range records[1:] {
		if len(record) < 4 {
			fmt.Printf("Skipping incomplete row %d: %v\n", i+2, record)
			continue
		}

		name := record[0]
		url := record[1]
		category := record[3]

		adapter, endpoint, notes := determineAdapter(name, url)

		// Skip if we've already processed this endpoint (deduplication)
		if endpoint != "" && seenEndpoints[endpoint] {
			fmt.Printf("Skipping duplicate endpoint for %s: %s\n", name, endpoint)
			continue
		}
		if endpoint != "" {
			seenEndpoints[endpoint] = true
		}

		// Assign tier based on category importance
		tier := 4 // Default (lower priority)
		switch category {
		case "Cloud", "Domain/DNS", "CDN":
			tier = 1 // Most critical
		case "AI/ML", "Financial SaaS", "Security", "Identity", "SASE":
			tier = 2
		case "SaaS", "Telecom", "Cable/ISP", "Colocation":
			tier = 3
		}

		enabled := true
		if adapter == "manual" && (notes == "No dedicated status page" || strings.Contains(notes, "out of scope")) {
			enabled = false
		}

		id := slugify(name)

		provider := Provider{
			ID:       id,
			Name:     name,
			Category: category,
			PageURL:  url,
			Adapter:  adapter,
			Endpoint: endpoint,
			Tier:     tier,
			Enabled:  enabled,
			Notes:    notes,
		}

		providers = append(providers, provider)
	}

	// Sort providers by name
	sort.Sort(ByName(providers))

	// Prepare output structure
	output := struct {
		Providers []Provider `yaml:"providers"`
	}{
		Providers: providers,
	}

	// Marshal to YAML
	out, err := yaml.Marshal(&output)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error marshaling YAML: %v\n", err)
		os.Exit(1)
	}

	outputPath := "providers.yaml"
	if err := os.WriteFile(outputPath, out, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing YAML: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("\nGenerated %s with %d providers\n", outputPath, len(providers))

	// Print summary
	adapterCount := make(map[string]int)
	disabledCount := 0
	enabledCount := 0
	for _, p := range providers {
		adapterCount[p.Adapter]++
		if !p.Enabled {
			disabledCount++
		} else {
			enabledCount++
		}
	}

	fmt.Println("\nAdapter distribution:")
	// Sort adapters alphabetically for consistent output
	adapterNames := make([]string, 0, len(adapterCount))
	for k := range adapterCount {
		adapterNames = append(adapterNames, k)
	}
	sort.Strings(adapterNames)
	for _, adapter := range adapterNames {
		fmt.Printf("  %s: %d\n", adapter, adapterCount[adapter])
	}
	fmt.Printf("\nEnabled: %d, Disabled: %d, Total: %d\n", enabledCount, disabledCount, len(providers))
}

// slugify converts a provider name to a URL-safe ID
func slugify(name string) string {
	result := strings.ToLower(name)
	var b strings.Builder
	for _, r := range result {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == ' ' {
			if r == ' ' {
				b.WriteRune('-')
			} else {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}
