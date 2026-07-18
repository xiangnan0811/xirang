package provider

import (
	"context"
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

func TestRegistryReturnsTypedPublicationStrategyOnlyWhenRegistered(t *testing.T) {
	missing := NewRegistry()
	if err := missing.Register(backupasset.ProviderRestic, Registration{Prober: &fakeProvider{}}); err != nil {
		t.Fatal(err)
	}
	if _, err := missing.PublicationStrategy(backupasset.ProviderRestic); !errors.Is(err, backupasset.ErrCapabilityUnavailable) {
		t.Fatalf("missing publication strategy error=%v", err)
	}
	strategy := &fakePublicationStrategy{}
	registered := NewRegistry()
	if err := registered.Register(backupasset.ProviderRestic, Registration{Prober: &fakeProvider{}, PublicationStrategy: strategy}); err != nil {
		t.Fatal(err)
	}
	if got, err := registered.PublicationStrategy(backupasset.ProviderRestic); err != nil || got != strategy {
		t.Fatalf("PublicationStrategy=%T err=%v", got, err)
	}
}

func TestRegistryReturnsCatalogReaderOnlyWhenExplicitlyRegistered(t *testing.T) {
	reader := &catalogReaderFake{}
	registry := NewRegistry()
	if err := registry.Register(backupasset.ProviderRestic, Registration{Prober: &fakeProvider{}, CatalogReader: reader}); err != nil {
		t.Fatal(err)
	}
	if got, err := registry.CatalogReader(backupasset.ProviderRestic); err != nil || got != reader {
		t.Fatalf("CatalogReader=%T err=%v", got, err)
	}
	if _, err := registry.CatalogReader(backupasset.ProviderCommand); !errors.Is(err, backupasset.ErrCapabilityUnavailable) {
		t.Fatalf("Command Catalog reader error=%v", err)
	}

	missing := NewRegistry()
	if err := missing.Register(backupasset.ProviderRsync, Registration{Prober: &fakeProvider{}}); err != nil {
		t.Fatal(err)
	}
	if _, err := missing.CatalogReader(backupasset.ProviderRsync); !errors.Is(err, backupasset.ErrCapabilityUnavailable) {
		t.Fatalf("missing Catalog reader error=%v", err)
	}
}

type catalogReaderFake struct{}

func (*catalogReaderFake) OpenCatalogRead(context.Context, CatalogReadRequest) (CatalogReadSession, error) {
	return nil, nil
}

type fakePublicationStrategy struct{}

func (*fakePublicationStrategy) Kind() backupasset.ProviderKind { return backupasset.ProviderRestic }

func (*fakePublicationStrategy) Prepare(context.Context, PublicationPrepareRequest) (PreparedPublication, error) {
	return PreparedPublication{}, nil
}

func (*fakePublicationStrategy) Execute(context.Context, PreparedPublication, PublicationProgress) (ProviderExecutionResult, error) {
	return ProviderExecutionResult{}, nil
}

func (*fakePublicationStrategy) RecordCommit(context.Context, PreparedPublication, ProviderExecutionResult) (ProviderCommit, error) {
	return ProviderCommit{}, nil
}

func (*fakePublicationStrategy) VerifyOrBuildManifest(context.Context, PreparedPublication, ProviderCommit, ManifestLimits) (ManifestResult, error) {
	return ManifestResult{}, nil
}

func (*fakePublicationStrategy) Reconcile(context.Context, PublicationReconcileRequest) (PublicationReconcileResult, error) {
	return PublicationReconcileResult{}, nil
}
