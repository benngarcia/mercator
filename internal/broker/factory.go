// Package broker turns registered connections into live adapters and routes
// offer collection + run lifecycle across them.
package broker

import (
	"fmt"
	"sort"
	"sync"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/capability"
)

// FactoryFunc builds one connection's implementation. Which contracts the
// result satisfies is discovered from the implementation rather than declared
// at registration, so an adapter cannot advertise a lane it cannot serve.
type FactoryFunc func(config map[string]string, secret string) (capability.Backend, error)

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

// Build constructs one connection's Backend, negotiating its capabilities so
// callers receive a lane rather than an untyped implementation.
func (f *Factory) Build(adapterType string, config map[string]string, secret string) (Backend, error) {
	f.mu.RLock()
	fn, ok := f.fns[adapterType]
	f.mu.RUnlock()
	if !ok {
		return Backend{}, fmt.Errorf("broker: no adapter registered for type %q", adapterType)
	}
	built, err := fn(config, secret)
	if err != nil {
		return Backend{}, err
	}
	return NewBackend(adapterType, built)
}

// Declarations returns every registered adapter's negotiated capability
// Declaration, built with empty configuration so onboarding surfaces and
// compatibility tests can state each backend's lane without a connection.
func (f *Factory) Declarations() ([]capability.Declaration, error) {
	declarations := make([]capability.Declaration, 0, len(f.manifests))
	for _, manifest := range f.Manifests() {
		backend, err := f.Build(manifest.Type, map[string]string{}, "")
		if err != nil {
			return nil, fmt.Errorf("broker: declare %s: %w", manifest.Type, err)
		}
		declarations = append(declarations, backend.Declaration)
	}
	return declarations, nil
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
