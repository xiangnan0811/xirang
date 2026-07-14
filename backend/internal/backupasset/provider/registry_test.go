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

func TestRegistryReturnsTypedPublicationPortsOnlyWhenRegistered(t *testing.T) {
	missing := NewRegistry()
	if err := missing.Register(backupasset.ProviderRestic, Registration{Prober: &fakeProvider{}}); err != nil {
		t.Fatal(err)
	}
	for _, lookup := range []func(backupasset.ProviderKind) error{
		func(kind backupasset.ProviderKind) error { _, err := missing.ResticPublisher(kind); return err },
		func(kind backupasset.ProviderKind) error { _, err := missing.ManifestBuilder(kind); return err },
	} {
		if err := lookup(backupasset.ProviderRestic); !errors.Is(err, backupasset.ErrCapabilityUnavailable) {
			t.Fatalf("missing optional publication port error=%v", err)
		}
	}
	publisher := &fakeResticPublisher{}
	builder := &fakeManifestBuilder{}
	registered := NewRegistry()
	if err := registered.Register(backupasset.ProviderRestic, Registration{Prober: &fakeProvider{}, ResticPublisher: publisher, ManifestBuilder: builder}); err != nil {
		t.Fatal(err)
	}
	if got, err := registered.ResticPublisher(backupasset.ProviderRestic); err != nil || got != publisher {
		t.Fatalf("ResticPublisher=%T err=%v", got, err)
	}
	if got, err := registered.ManifestBuilder(backupasset.ProviderRestic); err != nil || got != builder {
		t.Fatalf("ManifestBuilder=%T err=%v", got, err)
	}
}

type fakeResticPublisher struct{}

func (*fakeResticPublisher) Backup(context.Context, PublicationAttempt, ResticBackupInput, func(ResticBackupProgress)) (ResticBackupResult, error) {
	return ResticBackupResult{}, nil
}

func (*fakeResticPublisher) LookupAttempt(context.Context, PublicationAttempt) ([]ResticSnapshotObservation, error) {
	return nil, nil
}

type fakeManifestBuilder struct{}

func (*fakeManifestBuilder) BuildManifest(context.Context, PublicationAttempt, ProviderCommitEvidence, ManifestLimits) (ManifestEvidence, error) {
	return ManifestEvidence{}, nil
}
