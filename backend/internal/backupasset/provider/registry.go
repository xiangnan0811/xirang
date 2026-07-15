package provider

import (
	"fmt"
	"reflect"
	"sync"

	"xirang/backend/internal/backupasset"
)

type Registration struct {
	Prober              RepositoryProber
	PointLister         PointLister
	EntryLister         EntryLister
	EntryStatter        EntryStatter
	SequentialReader    SequentialReader
	RangeReader         RangeReader
	PublicationStrategy PublicationStrategy
}

type Registry struct {
	mu            sync.RWMutex
	registrations map[backupasset.ProviderKind]Registration
}

func NewRegistry() *Registry {
	return &Registry{registrations: make(map[backupasset.ProviderKind]Registration)}
}

func (registry *Registry) Register(kind backupasset.ProviderKind, registration Registration) error {
	if !readableProvider(kind) || interfaceNil(registration.Prober) {
		return newCapabilityError(backupasset.CapabilityProviderUnavailable)
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if _, exists := registry.registrations[kind]; exists {
		return fmt.Errorf("%w: provider already registered", backupasset.ErrConflict)
	}
	registry.registrations[kind] = registration
	return nil
}

func (registry *Registry) Prober(kind backupasset.ProviderKind) (RepositoryProber, error) {
	registration, err := registry.registration(kind)
	if err != nil || interfaceNil(registration.Prober) {
		if err != nil {
			return nil, err
		}
		return nil, newCapabilityError(backupasset.CapabilityProviderUnavailable)
	}
	return registration.Prober, nil
}

func (registry *Registry) PointLister(kind backupasset.ProviderKind) (PointLister, error) {
	registration, err := registry.registration(kind)
	if err != nil || interfaceNil(registration.PointLister) {
		if err != nil {
			return nil, err
		}
		return nil, newCapabilityError(backupasset.CapabilityCatalogUnavailable)
	}
	return registration.PointLister, nil
}

func (registry *Registry) EntryLister(kind backupasset.ProviderKind) (EntryLister, error) {
	registration, err := registry.registration(kind)
	if err != nil || interfaceNil(registration.EntryLister) {
		if err != nil {
			return nil, err
		}
		return nil, newCapabilityError(backupasset.CapabilityCatalogUnavailable)
	}
	return registration.EntryLister, nil
}

func (registry *Registry) EntryStatter(kind backupasset.ProviderKind) (EntryStatter, error) {
	registration, err := registry.registration(kind)
	if err != nil || interfaceNil(registration.EntryStatter) {
		if err != nil {
			return nil, err
		}
		return nil, newCapabilityError(backupasset.CapabilityCatalogUnavailable)
	}
	return registration.EntryStatter, nil
}

func (registry *Registry) SequentialReader(kind backupasset.ProviderKind) (SequentialReader, error) {
	registration, err := registry.registration(kind)
	if err != nil || interfaceNil(registration.SequentialReader) {
		if err != nil {
			return nil, err
		}
		return nil, newCapabilityError(backupasset.CapabilitySequentialReadUnavailable)
	}
	return registration.SequentialReader, nil
}

func (registry *Registry) RangeReader(kind backupasset.ProviderKind) (RangeReader, error) {
	registration, err := registry.registration(kind)
	if err != nil || interfaceNil(registration.RangeReader) {
		if err != nil {
			return nil, err
		}
		return nil, newCapabilityError(backupasset.CapabilityRangeUnavailable)
	}
	return registration.RangeReader, nil
}

// PublicationStrategy returns the one registered tagged publication strategy
// for a provider. A missing or mismatched strategy is unavailable rather than
// falling back to another provider's behavior.
func (registry *Registry) PublicationStrategy(kind backupasset.ProviderKind) (PublicationStrategy, error) {
	registration, err := registry.registration(kind)
	if err != nil || interfaceNil(registration.PublicationStrategy) {
		if err != nil {
			return nil, err
		}
		return nil, newCapabilityError(backupasset.CapabilityProviderUnavailable)
	}
	strategy := registration.PublicationStrategy
	if strategy.Kind() != kind {
		return nil, fmt.Errorf("%w: publication strategy provider mismatch", backupasset.ErrInvalidState)
	}
	return strategy, nil
}

func (registry *Registry) registration(kind backupasset.ProviderKind) (Registration, error) {
	if registry == nil || !readableProvider(kind) {
		return Registration{}, newCapabilityError(backupasset.CapabilityProviderUnavailable)
	}
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	registration, ok := registry.registrations[kind]
	if !ok {
		return Registration{}, newCapabilityError(backupasset.CapabilityProviderUnavailable)
	}
	return registration, nil
}

func readableProvider(kind backupasset.ProviderKind) bool {
	return kind == backupasset.ProviderRestic || kind == backupasset.ProviderRsync || kind == backupasset.ProviderRclone
}

func interfaceNil(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
