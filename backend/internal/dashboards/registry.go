package dashboards

import "sync"

var (
	registryMu sync.RWMutex
	registry   []Provider
)

// Register adds a provider. Safe to call from init().
func Register(p Provider) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry = append(registry, p)
}

// findProvider returns the first provider that supports the metric.
func findProvider(metric string) (Provider, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	for _, p := range registry {
		if p.Supports(metric) {
			return p, true
		}
	}
	return nil, false
}

// providersSnapshot returns a copy of registered providers for iteration.
func providersSnapshot() []Provider {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]Provider, len(registry))
	copy(out, registry)
	return out
}

// resetForTest clears the registry. Tests only.
func resetForTest() {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry = nil
}

// ResetRegistryForTest clears the provider registry between tests so each
// test can Register a provider bound to its own in-memory DB.
func ResetRegistryForTest() {
	resetForTest()
}
