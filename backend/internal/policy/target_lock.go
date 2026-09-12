package policy

import (
	"fmt"
	"strings"

	"gorm.io/gorm"
)

// targetOwnershipLockKey is intentionally shared by every task/policy write
// path. A single transaction-scoped key avoids lock-order deadlocks when an
// import or policy sync introduces several targets, and protects targets that
// do not yet have a task row to lock.
const targetOwnershipLockKey = "xirang:task-target-ownership:v1"

// LockTargetOwnershipSpace acquires the durable ownership lock for the
// duration of the surrounding database transaction. It is intentionally a
// single shared key: imports and policy synchronization can introduce several
// targets in arbitrary order, and a global key avoids lock-order deadlocks
// while protecting targets that do not yet have a task row to lock.
func LockTargetOwnershipSpace(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("target ownership database is unavailable")
	}
	switch db.Name() {
	case "postgres":
		if err := db.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, 0))", targetOwnershipLockKey).Error; err != nil {
			return fmt.Errorf("lock target ownership space: %w", err)
		}
		return nil
	case "sqlite":
		if err := lockSQLiteOwnershipSpace(db); err != nil {
			return fmt.Errorf("lock target ownership space: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("unsupported database for durable target ownership lock: %s", db.Name())
	}
}

func lockSQLiteOwnershipSpace(db *gorm.DB) error {
	// Try fixed, known durable tables directly. A preceding sqlite_master
	// probe would establish a read snapshot and make SQLite's read-to-write
	// upgrade fail immediately with SQLITE_BUSY when another writer holds the
	// lock, instead of waiting for that writer to commit.
	queries := []string{
		"UPDATE policies SET id = id WHERE id = (SELECT id FROM policies ORDER BY id LIMIT 1)",
		"UPDATE nodes SET id = id WHERE id = (SELECT id FROM nodes ORDER BY id LIMIT 1)",
		"UPDATE tasks SET id = id WHERE id = (SELECT id FROM tasks ORDER BY id LIMIT 1)",
	}
	var lastMissing error
	for _, query := range queries {
		err := db.Exec(query).Error
		if err == nil {
			return nil
		}
		if strings.Contains(strings.ToLower(err.Error()), "no such table") {
			lastMissing = err
			continue
		}
		return err
	}
	return fmt.Errorf("no durable ownership lock table exists: %w", lastMissing)
}
