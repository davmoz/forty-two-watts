package evcloud

import "fmt"

// Charger is the common representation returned by all providers.
type Charger struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// Provider can authenticate with a cloud EV charger service and list
// the chargers on the account.
type Provider interface {
	ListChargers(email, password string) ([]Charger, error)
}

var providers = map[string]Provider{}

// Register adds a named provider. Call from init().
func Register(name string, p Provider) { providers[name] = p }

// Get returns the provider for the given name or an error.
func Get(name string) (Provider, error) {
	p, ok := providers[name]
	if !ok {
		return nil, fmt.Errorf("unknown ev cloud provider: %q", name)
	}
	return p, nil
}

// Names returns all registered provider names.
func Names() []string {
	out := make([]string, 0, len(providers))
	for k := range providers {
		out = append(out, k)
	}
	return out
}
