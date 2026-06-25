// Package broker turns registered connections into live adapters and routes
// offer collection + run lifecycle across them.
package broker

import (
	"fmt"
	"sync"

	"github.com/benngarcia/mercator/internal/adapter"
)

type FactoryFunc func(config map[string]string, secret string) (adapter.Adapter, error)

type Factory struct {
	mu sync.RWMutex
	fns map[string]FactoryFunc
}

func NewFactory() *Factory { return &Factory{fns: map[string]FactoryFunc{}} }

func (f *Factory) Register(adapterType string, fn FactoryFunc) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fns[adapterType] = fn
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
