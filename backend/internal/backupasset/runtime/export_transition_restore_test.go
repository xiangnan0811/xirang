package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
)

func TestExportTransitionFailureAfterPersistRestoresExactPriorSetting(t *testing.T) {
	installErr := errors.New("FAKE_PHASE3_EXPORT_INSTALL_FAILURE_FOR_TEST_ONLY")
	runtime := &managedExportRuntime{
		validateRoot:      func(context.Context, string) error { return installErr },
		publication:       newManagedExportPublication(),
		change:            make(chan struct{}),
		transitionTimeout: time.Second,
	}
	runtime.accepting.Store(true)
	setting := "prior-export-setting"
	restoreCalls := 0
	err := runtime.TransitionSettingsWithRestore(
		context.Background(), true, backupasset.ExportConfig{Enabled: true, Root: t.TempDir()},
		func() error {
			setting = "prospective-export-setting"
			return nil
		},
		func() error {
			restoreCalls++
			setting = "prior-export-setting"
			return nil
		},
	)
	if !errors.Is(err, installErr) {
		t.Fatalf("transition error=%v, want Export install failure", err)
	}
	if setting != "prior-export-setting" || restoreCalls != 1 {
		t.Fatalf("failed Export transition retained setting=%q restore calls=%d", setting, restoreCalls)
	}
	if runtime.Ready() {
		t.Fatal("failed Export transition remained ready")
	}
}

func TestExportTransitionCompensationFailureJoinsErrorsAndFencesReady(t *testing.T) {
	primaryErr := errors.New("FAKE_PHASE3_EXPORT_PRIMARY_FAILURE_FOR_TEST_ONLY")
	restoreErr := errors.New("FAKE_PHASE3_EXPORT_RESTORE_FAILURE_FOR_TEST_ONLY")
	runtime := &managedExportRuntime{
		validateRoot:      func(context.Context, string) error { return primaryErr },
		publication:       newManagedExportPublication(),
		change:            make(chan struct{}),
		transitionTimeout: time.Second,
	}
	runtime.accepting.Store(true)
	runtime.ready.Store(true)
	err := runtime.TransitionSettingsWithRestore(
		context.Background(), true, backupasset.ExportConfig{Enabled: true, Root: t.TempDir()},
		func() error { return nil }, func() error { return restoreErr },
	)
	if !errors.Is(err, primaryErr) || !errors.Is(err, restoreErr) {
		t.Fatalf("transition error=%v, want primary + compensation errors", err)
	}
	if runtime.Ready() || runtime.accepting.Load() {
		t.Fatalf("compensation-failed Export remained ready/accepting=%t/%t", runtime.Ready(), runtime.accepting.Load())
	}
}
