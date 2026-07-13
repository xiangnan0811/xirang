package provider

import (
	"errors"
	"testing"

	"xirang/backend/internal/backupasset"
)

func TestRegistryReturnsTypedMissingCapability(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(backupasset.ProviderRestic, Registration{Prober: &fakeProvider{}}); err != nil {
		t.Fatal(err)
	}
	_, err := registry.RangeReader(backupasset.ProviderRestic)
	if !errors.Is(err, backupasset.ErrCapabilityUnavailable) {
		t.Fatalf("missing Range error=%v", err)
	}
	var capabilityErr *CapabilityError
	if !errors.As(err, &capabilityErr) || capabilityErr.Reason.Code != backupasset.CapabilityRangeUnavailable {
		t.Fatalf("missing safe capability reason: %#v", err)
	}
}

func TestRegistryRejectsDuplicateRegistration(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(backupasset.ProviderRestic, Registration{Prober: &fakeProvider{}}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(backupasset.ProviderRestic, Registration{Prober: &fakeProvider{}}); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("duplicate registration=%v", err)
	}
}

func TestRegistryRejectsUnsupportedAndNilProviders(t *testing.T) {
	tests := []struct {
		name         string
		provider     backupasset.ProviderKind
		registration Registration
	}{
		{"command", backupasset.ProviderCommand, Registration{Prober: &fakeProvider{}}},
		{"unknown", backupasset.ProviderKind("future"), Registration{Prober: &fakeProvider{}}},
		{"nil prober", backupasset.ProviderRsync, Registration{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := NewRegistry().Register(tt.provider, tt.registration); !errors.Is(err, backupasset.ErrCapabilityUnavailable) {
				t.Fatalf("Register error=%v", err)
			}
		})
	}
}

func TestRegistryReturnsOnlyRequestedNarrowPort(t *testing.T) {
	provider := &fakeProvider{}
	registry := NewRegistry()
	if err := registry.Register(backupasset.ProviderRsync, Registration{
		Prober: provider, PointLister: provider, EntryLister: provider,
		EntryStatter: provider, SequentialReader: provider,
	}); err != nil {
		t.Fatal(err)
	}
	if got, err := registry.PointLister(backupasset.ProviderRsync); err != nil || got != provider {
		t.Fatalf("PointLister=%T err=%v", got, err)
	}
	if _, err := registry.Prober(backupasset.ProviderCommand); !errors.Is(err, backupasset.ErrCapabilityUnavailable) {
		t.Fatalf("Command prober error=%v", err)
	}
}
