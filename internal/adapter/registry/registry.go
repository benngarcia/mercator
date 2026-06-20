package registry

import "github.com/bengarcia/mercator/internal/adapter"

type Registry struct {
	adapters map[string]adapter.Adapter
}

func New() *Registry {
	return &Registry{adapters: map[string]adapter.Adapter{}}
}

func (r *Registry) Register(adapterType string, ad adapter.Adapter) {
	r.adapters[adapterType] = ad
}

func (r *Registry) Get(adapterType string) (adapter.Adapter, bool) {
	ad, ok := r.adapters[adapterType]
	return ad, ok
}
