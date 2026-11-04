package ga

import (
	"errors"
	"testing"

	"xirang/backend/internal/settings"
)

func TestEnablementRequiresReadiness(t *testing.T) {
	t.Run("blocked_does_not_enable", func(t *testing.T) {
		err := EvaluateEnablement(ReadinessSnapshot{
			Class:             InstallationFresh,
			Status:            ReadinessBlocked,
			InventoryComplete: false,
		})
		if !errors.Is(err, ErrEnablementBlocked) {
			t.Fatalf("error=%v, want ErrEnablementBlocked", err)
		}
	})

	t.Run("fresh_ready_without_ack_succeeds", func(t *testing.T) {
		err := EvaluateEnablement(ReadinessSnapshot{
			Class:             InstallationFresh,
			Status:            ReadinessReady,
			InventoryComplete: true,
			InventoryDigest:   "fresh-digest",
			ExportRootValid:   true,
			KeyDomainsReady:   true,
		})
		if err != nil {
			t.Fatalf("fresh ready enablement: %v", err)
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
		err := EvaluateEnablement(readyExisting)
		if !errors.Is(err, ErrEnablementAckRequired) {
			t.Fatalf("error=%v, want ErrEnablementAckRequired", err)
		}
	})

	t.Run("ack_for_current_digest_succeeds", func(t *testing.T) {
		acked := readyExisting
		acked.Status = ReadinessAcknowledged
		acked.AcknowledgedDigest = readyExisting.InventoryDigest
		if err := EvaluateEnablement(acked); err != nil {
			t.Fatalf("acked existing enablement: %v", err)
		}
	})

	t.Run("stale_ack_is_rejected", func(t *testing.T) {
		stale := readyExisting
		stale.Status = ReadinessAcknowledged
		stale.AcknowledgedDigest = "old-digest"
		err := EvaluateEnablement(stale)
		if !errors.Is(err, ErrEnablementAckRequired) {
			t.Fatalf("stale ack error=%v, want ErrEnablementAckRequired", err)
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
