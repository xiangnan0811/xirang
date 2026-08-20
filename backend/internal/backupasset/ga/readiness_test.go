package ga

import (
	"context"
	"errors"
	"testing"

	"xirang/backend/internal/settings"
)

func TestEnablementRequiresReadiness(t *testing.T) {
	t.Run("blocked_does_not_become_managed", func(t *testing.T) {
		inner := &recordingFeatureTransitioner{}
		gate := NewEnablementGate(EnablementGateDependencies{
			Readiness: staticReadiness{snapshot: ReadinessSnapshot{
				Class:             InstallationFresh,
				Status:            ReadinessBlocked,
				InventoryComplete: false,
			}},
			Inner: inner,
		})
		persisted := false
		err := gate.TransitionFeature(context.Background(), true, func() error {
			persisted = true
			return nil
		})
		if !errors.Is(err, ErrEnablementBlocked) {
			t.Fatalf("error=%v, want ErrEnablementBlocked", err)
		}
		if persisted || len(inner.targets) != 0 {
			t.Fatalf("blocked enablement became managed persist=%t targets=%v", persisted, inner.targets)
		}
	})

	t.Run("fresh_ready_without_ack_succeeds", func(t *testing.T) {
		inner := &recordingFeatureTransitioner{}
		gate := NewEnablementGate(EnablementGateDependencies{
			Readiness: staticReadiness{snapshot: ReadinessSnapshot{
				Class:             InstallationFresh,
				Status:            ReadinessReady,
				InventoryComplete: true,
				InventoryDigest:   "fresh-digest",
				ExportRootValid:   true,
				KeyDomainsReady:   true,
			}},
			Inner: inner,
		})
		persisted := false
		if err := gate.TransitionFeature(context.Background(), true, func() error {
			persisted = true
			return nil
		}); err != nil {
			t.Fatalf("fresh ready enablement: %v", err)
		}
		if !persisted || len(inner.targets) != 1 || !inner.targets[0] {
			t.Fatalf("fresh ready persist=%t targets=%v", persisted, inner.targets)
		}
	})

	t.Run("disablement_skips_readiness", func(t *testing.T) {
		inner := &recordingFeatureTransitioner{}
		gate := NewEnablementGate(EnablementGateDependencies{
			Readiness: staticReadiness{snapshot: ReadinessSnapshot{Status: ReadinessBlocked}},
			Inner:     inner,
		})
		if err := gate.TransitionFeature(context.Background(), false, func() error { return nil }); err != nil {
			t.Fatalf("disablement: %v", err)
		}
		if len(inner.targets) != 1 || inner.targets[0] {
			t.Fatalf("disablement targets=%v", inner.targets)
		}
	})
}

func TestExistingInstallRequiresAck(t *testing.T) {
	readyExisting := ReadinessSnapshot{
		Class:             InstallationExisting,
		Status:            ReadinessReady,
		InventoryComplete: true,
		InventoryDigest:   "current-digest",
		ExportRootValid:   true,
		KeyDomainsReady:   true,
	}

	t.Run("ready_without_ack_stays_disabled", func(t *testing.T) {
		inner := &recordingFeatureTransitioner{}
		gate := NewEnablementGate(EnablementGateDependencies{
			Readiness: staticReadiness{snapshot: readyExisting},
			Inner:     inner,
		})
		persisted := false
		err := gate.TransitionFeature(context.Background(), true, func() error {
			persisted = true
			return nil
		})
		if !errors.Is(err, ErrEnablementAckRequired) {
			t.Fatalf("error=%v, want ErrEnablementAckRequired", err)
		}
		if persisted || len(inner.targets) != 0 {
			t.Fatalf("existing without ack became managed persist=%t targets=%v", persisted, inner.targets)
		}
	})

	t.Run("ack_for_current_digest_succeeds", func(t *testing.T) {
		inner := &recordingFeatureTransitioner{}
		acked := readyExisting
		acked.Status = ReadinessAcknowledged
		acked.AcknowledgedDigest = readyExisting.InventoryDigest
		gate := NewEnablementGate(EnablementGateDependencies{
			Readiness: staticReadiness{snapshot: acked},
			Inner:     inner,
		})
		if err := gate.TransitionFeature(context.Background(), true, func() error { return nil }); err != nil {
			t.Fatalf("acked existing enablement: %v", err)
		}
		if len(inner.targets) != 1 || !inner.targets[0] {
			t.Fatalf("acked existing targets=%v", inner.targets)
		}
	})

	t.Run("stale_ack_is_rejected", func(t *testing.T) {
		inner := &recordingFeatureTransitioner{}
		stale := readyExisting
		stale.Status = ReadinessAcknowledged
		stale.AcknowledgedDigest = "old-digest"
		gate := NewEnablementGate(EnablementGateDependencies{
			Readiness: staticReadiness{snapshot: stale},
			Inner:     inner,
		})
		err := gate.TransitionFeature(context.Background(), true, func() error { return nil })
		if !errors.Is(err, ErrEnablementAckRequired) {
			t.Fatalf("stale ack error=%v, want ErrEnablementAckRequired", err)
		}
		if len(inner.targets) != 0 {
			t.Fatalf("stale ack became managed targets=%v", inner.targets)
		}
	})
}

func TestBackupAssetsEnabledCodeDefaultRemainsFalse(t *testing.T) {
	found := false
	for _, definition := range settings.NewService(nil).Registry() {
		if definition.Key != "backup_assets.enabled" {
			continue
		}
		found = true
		if definition.CodeDefault != "false" || definition.EnvVar != "BACKUP_ASSETS_ENABLED" {
			t.Fatalf("backup_assets.enabled CodeDefault=%q env=%q, want false / BACKUP_ASSETS_ENABLED", definition.CodeDefault, definition.EnvVar)
		}
	}
	if !found {
		t.Fatal("backup_assets.enabled is missing from the settings registry")
	}
}

type staticReadiness struct {
	snapshot ReadinessSnapshot
	err      error
}

func (source staticReadiness) CurrentReadiness(context.Context) (ReadinessSnapshot, error) {
	return source.snapshot, source.err
}

type recordingFeatureTransitioner struct {
	targets []bool
}

func (spy *recordingFeatureTransitioner) TransitionFeature(_ context.Context, enabled bool, persist func() error) error {
	spy.targets = append(spy.targets, enabled)
	if persist != nil {
		return persist()
	}
	return nil
}

func (spy *recordingFeatureTransitioner) PrepareApplicationDowngrade(context.Context, func() error) error {
	return nil
}

func (spy *recordingFeatureTransitioner) PrepareSchemaDown(context.Context, func() error) error {
	return nil
}
