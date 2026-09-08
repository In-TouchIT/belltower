package adapters

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// providerYAML mirrors one entry in providers.yaml.
type providerYAML struct {
	ID       string `yaml:"id"`
	Name     string `yaml:"name"`
	Category string `yaml:"category"`
	PageURL  string `yaml:"page_url"`
	Adapter  string `yaml:"adapter"`
	Endpoint string `yaml:"endpoint"`
	Tier     int    `yaml:"tier"`
	Enabled  bool   `yaml:"enabled"`
	Notes    string `yaml:"notes"`
}

// ProviderConfig is a provider as configured on disk, including whether it is
// enabled. Callers need the disabled entries too: skipping them here would let
// a provider that was turned off in YAML keep its stale enabled=1 row in the
// database and carry on being polled forever.
type ProviderConfig struct {
	ProviderInfo
	Enabled bool
	Notes   string
}

// LoadProvidersFromYAML loads every provider from a YAML file, enabled or not.
func LoadProvidersFromYAML(path string) ([]ProviderConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read providers file: %w", err)
	}

	var result struct {
		Providers []providerYAML `yaml:"providers"`
	}
	if err := yaml.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("failed to parse YAML: %w", err)
	}

	seen := make(map[string]bool, len(result.Providers))
	providers := make([]ProviderConfig, 0, len(result.Providers))
	for _, p := range result.Providers {
		if p.ID == "" {
			return nil, fmt.Errorf("provider %q has no id", p.Name)
		}
		if seen[p.ID] {
			return nil, fmt.Errorf("duplicate provider id %q", p.ID)
		}
		seen[p.ID] = true

		providers = append(providers, ProviderConfig{
			ProviderInfo: ProviderInfo{
				ID:       p.ID,
				Name:     p.Name,
				Category: p.Category,
				PageURL:  p.PageURL,
				Adapter:  p.Adapter,
				Endpoint: p.Endpoint,
				Tier:     p.Tier,
			},
			Enabled: p.Enabled,
			Notes:   p.Notes,
		})
	}

	return providers, nil
}
