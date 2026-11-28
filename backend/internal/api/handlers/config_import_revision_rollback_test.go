package handlers

import (
	"context"
	"testing"

	"xirang/backend/internal/database"
)

func TestConfigImportNodeRevisionRollbackSQLite(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		name := "current_snapshot"
		if legacy {
			name = "legacy_snapshot"
		}
		t.Run(name, func(t *testing.T) {
			db := openConfigHandlerTestDB(t)
			if err := database.RunMigrations(db, "sqlite"); err != nil {
				t.Fatal(err)
			}
			if err := db.Exec(`INSERT INTO nodes (id, name, host, username, backup_dir) VALUES (1, 'node', 'prior', 'root', 'node')`).Error; err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			snapshot := newConfigImportTableSnapshot("nodes", "id", "id = ?", 1)
			if err := snapshot.capture(ctx, db); err != nil {
				t.Fatal(err)
			}
			prior := snapshot.prior["1"]
			originalRevision := rawConfigImportString(prior["config_revision"])
			if originalRevision == "" {
				t.Fatal("missing original revision")
			}
			if legacy {
				delete(prior, "config_revision")
			}
			if err := db.Exec(`UPDATE nodes SET host = 'imported' WHERE id = 1`).Error; err != nil {
				t.Fatal(err)
			}
			if err := snapshot.sealIDs(ctx, db, []any{1}); err != nil {
				t.Fatal(err)
			}
			importedRevision := rawConfigImportString(snapshot.current["1"]["config_revision"])
			if importedRevision == "" || importedRevision == originalRevision {
				t.Fatal("import did not change revision")
			}
			if err := snapshot.verifyCurrent(ctx, db); err != nil {
				t.Fatal(err)
			}
			if err := snapshot.restorePrior(ctx, db); err != nil {
				t.Fatal(err)
			}
			var row struct {
				Host           string
				ConfigRevision string
			}
			if err := db.Raw(`SELECT host, config_revision FROM nodes WHERE id = 1`).Scan(&row).Error; err != nil {
				t.Fatal(err)
			}
			if row.Host != "prior" || row.ConfigRevision == "" || row.ConfigRevision == originalRevision || row.ConfigRevision == importedRevision {
				t.Fatalf("restore retained stale revision or failed to restore host: %+v", row)
			}
		})
	}
	t.Run("concurrent_change_then_revert", func(t *testing.T) {
		db := openConfigHandlerTestDB(t)
		if err := database.RunMigrations(db, "sqlite"); err != nil {
			t.Fatal(err)
		}
		if err := db.Exec(`INSERT INTO nodes (id, name, host, username, backup_dir) VALUES (1, 'node', 'prior', 'root', 'node')`).Error; err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		snapshot := newConfigImportTableSnapshot("nodes", "id", "id = ?", 1)
		if err := snapshot.capture(ctx, db); err != nil {
			t.Fatal(err)
		}
		if err := db.Exec(`UPDATE nodes SET host = 'imported' WHERE id = 1`).Error; err != nil {
			t.Fatal(err)
		}
		if err := snapshot.sealIDs(ctx, db, []any{1}); err != nil {
			t.Fatal(err)
		}
		for _, host := range []string{"concurrent", "imported"} {
			if err := db.Exec(`UPDATE nodes SET host = ? WHERE id = 1`, host).Error; err != nil {
				t.Fatal(err)
			}
		}
		if err := snapshot.verifyCurrent(ctx, db); err == nil {
			t.Fatal("reverted concurrent configuration was not detected")
		}
		if err := snapshot.restorePrior(ctx, db); err == nil {
			t.Fatal("CAS accepted stale configuration revision")
		}
		var host string
		if err := db.Raw(`SELECT host FROM nodes WHERE id = 1`).Scan(&host).Error; err != nil || host != "imported" {
			t.Fatalf("concurrent row changed: host=%q err=%v", host, err)
		}
	})
}
