package cronbackup

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Initialize creates the private state directory, engine directory, lock
// files, and the first empty state record. A valid existing state is preserved
// byte-for-byte in place; initialization never resets source identity,
// revision, or the first-run grace period.
func Initialize(ctx context.Context, cfg Config, now time.Time) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateConfig(cfg); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	now, err := operationTime(now)
	if err != nil {
		return err
	}
	storage, err := ensureStorage(cfg)
	if err != nil {
		return err
	}
	defer storage.close()

	lockFile, err := openLock(storage.engine, stateLockName)
	if err != nil {
		return err
	}
	defer func() { _ = lockFile.Close() }()
	if err := acquireLock(ctx, lockFile, stateLockWorkerTimeout, ErrStateUnavailable); err != nil {
		return err
	}
	_, present, readErr := readStateFile(storage)
	if readErr != nil {
		if errors.Is(readErr, ErrStateInvalid) {
			return ErrStateInvalid
		}
		return readErr
	}
	if !present {
		sourceID, randomErr := randomHexID()
		if randomErr != nil {
			return fmt.Errorf("%w: source identity: %v", ErrStatePublishFailed, randomErr)
		}
		state := stateDocument{Version: 1, SourceID: sourceID, Revision: 1, Engine: cfg.Engine, InitializedAt: now}
		if err := writeStateFile(storage, state); err != nil {
			if errors.Is(err, ErrStatePublishFailed) {
				return ErrStatePublishFailed
			}
			return err
		}
		return nil
	}
	return nil
}
