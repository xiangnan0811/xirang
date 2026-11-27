package handlers

import (
	"context"
	"errors"
	"testing"
	"time"

	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

func TestConfigImportRollbackFencesNodeProbeSQLitePostgres(t *testing.T) {
	retryTransactionForEachEngine(t, func(t *testing.T, engine string) {
		db, node, key := newMigratedNodeProbeFixture(t, engine)
		ctx := context.Background()
		before, err := admitNodeProbe(ctx, db, node.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := applyNodeProbe(ctx, db, before, "online", 7, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}

		data := configImportData{
			Nodes:   []map[string]any{{"name": node.Name}},
			SSHKeys: []map[string]any{{"name": key.Name}},
		}
		journal := newConfigImportRollbackJournal(db, nil)
		var snapshot *configImportRollbackSnapshot
		err = db.Transaction(func(tx *gorm.DB) error {
			if _, err := lockConfigImportCredentialInventory(tx, data); err != nil {
				return err
			}
			var err error
			snapshot, err = captureConfigImportRollbackSnapshot(ctx, tx, data, nil, configImportEnvelope{})
			if err != nil {
				return err
			}
			if err := tx.Model(&model.Node{}).Where("id = ?", node.ID).Update("host", "127.0.0.2").Error; err != nil {
				return err
			}
			if err := tx.Model(&model.SSHKey{}).Where("id = ?", key.ID).Update("disabled", true).Error; err != nil {
				return err
			}
			return snapshot.seal(ctx, tx)
		})
		if err != nil {
			t.Fatal(err)
		}
		journal.install(snapshot)
		imported, err := admitNodeProbe(ctx, db, node.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := journal.Restore(ctx); err != nil {
			t.Fatalf("restore with an in-flight probe: %v", err)
		}
		if _, err := applyNodeProbe(ctx, db, imported, "offline", 0, time.Now().UTC()); !errors.Is(err, errNodeProbeStale) {
			t.Fatalf("restored node accepted the imported probe: %v", err)
		}
		if _, err := trustProbeSnapshot(ctx, db, node.ID, before.token); !errors.Is(err, errNodeProbeStale) {
			t.Fatalf("restored node accepted the pre-import trust token: %v", err)
		}

		stored, token, err := readNodeWithProbeToken(ctx, db, node.ID)
		if err != nil || stored.Host != node.Host || stored.SSHKey == nil || stored.SSHKey.Disabled || stored.Status != "online" || stored.ConnectionLatency != 7 || token != "" {
			t.Fatalf("restore changed credential or health state: %+v token=%q err=%v", stored.Sanitized(), token, err)
		}
		fresh, err := admitNodeProbe(ctx, db, node.ID)
		if err != nil || fresh.seq <= imported.seq {
			t.Fatalf("compensation reset probe order: seq=%d prior=%d err=%v", fresh.seq, imported.seq, err)
		}
		if _, err := applyNodeProbe(ctx, db, fresh, "online", 9, time.Now().UTC()); err != nil {
			t.Fatalf("apply fresh probe after compensation: %v", err)
		}
	})
}
