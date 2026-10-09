package cronbackup

import (
	"context"
	"errors"
	"os"
	"time"
)

// Read takes a bounded, read-only snapshot. It never creates a directory or
// file and never mutates state. Expected storage/protocol outcomes are
// represented by JobObservation.Status; only caller cancellation is returned
// as an error. The clock is sampled only after the state lock has been
// acquired and the state file has been read.
func Read(ctx context.Context, cfg Config, clock func() time.Time) (Observation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if cfg.StateDirectory == "" {
		return emptyObservation(cfg, sampleObservationTime(clock), JobStatusNotConfigured), nil
	}
	if err := validateConfig(cfg); err != nil {
		if errors.Is(err, ErrNotConfigured) {
			return emptyObservation(cfg, sampleObservationTime(clock), JobStatusNotConfigured), nil
		}
		return emptyObservation(cfg, sampleObservationTime(clock), JobStatusInvalidConfiguration), nil
	}
	if err := ctx.Err(); err != nil {
		return emptyObservation(cfg, sampleObservationTime(clock), JobStatusStateUnavailable), err
	}
	storage, err := openExistingStorage(cfg)
	if errors.Is(err, ErrNotInitialized) {
		return emptyObservation(cfg, sampleObservationTime(clock), JobStatusNotInitialized), nil
	}
	if err != nil {
		return emptyObservation(cfg, sampleObservationTime(clock), JobStatusStateUnavailable), nil
	}
	defer storage.close()

	stateExists, stateErr := entryPresence(storage.engine, stateFileName)
	if stateErr != nil {
		return emptyObservation(cfg, sampleObservationTime(clock), JobStatusStateUnavailable), nil
	}
	stateLockFile, lockErr := openLock(storage.engine, stateLockName)
	if lockErr != nil {
		if !stateExists && errors.Is(lockErr, ErrNotInitialized) {
			return emptyObservation(cfg, sampleObservationTime(clock), JobStatusNotInitialized), nil
		}
		return emptyObservation(cfg, sampleObservationTime(clock), JobStatusStateUnavailable), nil
	}
	defer func() { _ = stateLockFile.Close() }()
	if err := acquireLock(ctx, stateLockFile, stateLockReadTimeout, ErrStateUnavailable); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return emptyObservation(cfg, sampleObservationTime(clock), JobStatusStateUnavailable), err
		}
		return emptyObservation(cfg, sampleObservationTime(clock), JobStatusStateUnavailable), nil
	}
	return observeLocked(ctx, storage, clock)
}

func withLockedObservation(ctx context.Context, cfg Config, clock func() time.Time, callback func(Observation) error, timeout time.Duration) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateConfig(cfg); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	storage, err := openExistingStorage(cfg)
	if errors.Is(err, ErrNotInitialized) {
		return ErrStateUnavailable
	}
	if err != nil {
		return ErrStateUnavailable
	}
	defer storage.close()

	stateLockFile, err := openLock(storage.engine, stateLockName)
	if err != nil {
		return ErrStateUnavailable
	}
	defer func() { _ = stateLockFile.Close() }()
	if err := acquireLock(ctx, stateLockFile, timeout, ErrStateUnavailable); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return ErrStateUnavailable
	}
	observation, observationErr := observeLocked(ctx, storage, clock)
	if observationErr != nil {
		return observationErr
	}
	if callback == nil {
		return nil
	}
	return callback(observation)
}

func observeLocked(ctx context.Context, storage *rootedStorage, clock func() time.Time) (Observation, error) {
	if err := ctx.Err(); err != nil {
		return emptyObservation(storage.cfg, sampleObservationTime(clock), JobStatusStateUnavailable), err
	}
	state, present, err := readStateFile(storage)
	now := sampleObservationTime(clock)
	if err != nil {
		observation := emptyObservation(storage.cfg, now, JobStatusStateUnavailable)
		observation.statePresent = present
		if errors.Is(err, ErrStateInvalid) {
			observation.Job.Status = JobStatusStateInvalid
		}
		return observation, nil
	}
	if !present {
		return emptyObservation(storage.cfg, now, JobStatusNotInitialized), nil
	}

	runHeld, runErr := probeRunLock(storage.engine)
	if runErr != nil {
		observation := emptyObservation(storage.cfg, now, JobStatusStateUnavailable)
		observation.statePresent = true
		observation.stateValid = true
		observation.sourceID = state.SourceID
		observation.revision = state.Revision
		observation.initializedAt = state.InitializedAt
		observation.Job = stateToJob(state, JobStatusStateUnavailable, now, storage.cfg.MaxAge)
		return observation, nil
	}
	status := deriveStatus(state, now, storage.cfg.MaxAge, runHeld)
	observation := Observation{
		Job:           stateToJob(state, status, now, storage.cfg.MaxAge),
		sourceID:      state.SourceID,
		revision:      state.Revision,
		initializedAt: state.InitializedAt,
		statePresent:  true,
		stateValid:    true,
		runLockHeld:   runHeld,
	}
	return observation, nil
}

func probeRunLock(root *os.Root) (bool, error) {
	file, err := openLock(root, runLockName)
	if err != nil {
		return false, err
	}
	defer func() { _ = file.Close() }()
	if err := acquireLock(context.Background(), file, 0, ErrAlreadyRunning); err != nil {
		if errors.Is(err, ErrAlreadyRunning) {
			return true, nil
		}
		return false, err
	}
	return false, nil
}

func deriveStatus(state stateDocument, now time.Time, maxAge time.Duration, runHeld bool) JobStatus {
	if hasClockAnomaly(state, now) {
		return JobStatusClockAnomaly
	}
	if state.LatestAttempt == nil {
		return JobStatusNeverRun
	}
	attempt := state.LatestAttempt
	switch attempt.Result {
	case AttemptRunning:
		if !runHeld {
			return JobStatusInterrupted
		}
		if now.Sub(attempt.StartedAt) > maxAge {
			return JobStatusOverdueRunning
		}
		return JobStatusRunning
	case AttemptSuccess:
		if now.Sub(*attempt.FinishedAt) <= maxAge {
			return JobStatusSuccess
		}
		return JobStatusStale
	case AttemptFailed:
		return JobStatusFailed
	case AttemptInterrupted:
		return JobStatusInterrupted
	default:
		return JobStatusStateInvalid
	}
}

func hasClockAnomaly(state stateDocument, now time.Time) bool {
	if state.InitializedAt.After(now) {
		return true
	}
	if state.LatestAttempt != nil {
		attempt := state.LatestAttempt
		if attempt.StartedAt.Before(state.InitializedAt) ||
			(attempt.FinishedAt != nil && attempt.FinishedAt.Before(attempt.StartedAt)) ||
			(attempt.DetectedAt != nil && attempt.DetectedAt.Before(attempt.StartedAt)) ||
			attempt.StartedAt.After(now) ||
			(attempt.FinishedAt != nil && attempt.FinishedAt.After(now)) ||
			(attempt.DetectedAt != nil && attempt.DetectedAt.After(now)) {
			return true
		}
	}
	if state.LastSuccess != nil && (state.LastSuccess.StartedAt.Before(state.InitializedAt) || state.LastSuccess.FinishedAt.Before(state.LastSuccess.StartedAt) || state.LastSuccess.StartedAt.After(now) || state.LastSuccess.FinishedAt.After(now)) {
		return true
	}
	return false
}

func sampleObservationTime(clock func() time.Time) time.Time {
	if clock == nil {
		return time.Now().UTC()
	}
	return normalizedTime(clock())
}

func emptyObservation(cfg Config, now time.Time, status JobStatus) Observation {
	maxAge := cfg.MaxAge
	if maxAge < 0 || maxAge > maxMaxAgeHours*time.Hour {
		maxAge = 0
	}
	return Observation{Job: JobObservation{
		Evidence:      "job_record",
		Status:        status,
		CheckedAt:     now.UTC(),
		MaxAgeSeconds: int64(maxAge / time.Second),
	}}
}

func entryPresence(root *os.Root, name string) (bool, error) {
	_, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
