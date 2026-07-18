// Package broker turns registered connections into live adapters and routes
// offer collection + run lifecycle across them.
package broker

import (
	"fmt"
	"sort"
	"sync"

	"github.com/benngarcia/mercator/internal/adapter"
)

type FactoryFunc func(config map[string]string, secret string) (adapter.Adapter, error)

type Factory struct {
	mu        sync.RWMutex
	fns       map[string]FactoryFunc
	manifests map[string]adapter.Manifest
}

func NewFactory() *Factory {
	return &Factory{fns: map[string]FactoryFunc{}, manifests: map[string]adapter.Manifest{}}
}

// Register binds an adapter type to its builder and its manifest. The manifest
// is mandatory so a registered provider always ships its onboarding contract;
// the registration key is manifest.Type.
func (f *Factory) Register(m adapter.Manifest, fn FactoryFunc) {
	if m.Type == "" {
		panic("broker: adapter manifest has empty type")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fns[m.Type] = fn
	f.manifests[m.Type] = m
}

func (f *Factory) Build(adapterType string, config map[string]string, secret string) (adapter.Adapter, error) {
	f.mu.RLock()
	fn, ok := f.fns[adapterType]
	f.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("broker: no adapter registered for type %q", adapterType)
	}
	return fn(config, secret)
}

// Manifests returns every registered adapter's manifest, sorted by type.
func (f *Factory) Manifests() []adapter.Manifest {
	f.mu.RLock()
	defer f.mu.RUnlock()
	out := make([]adapter.Manifest, 0, len(f.manifests))
	for _, m := range f.manifests {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Type < out[j].Type })
	return out
}
