package cronbackup

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestInitializeRejectsSymlinkedAncestorWithoutOutsideMutation(t *testing.T) {
	sandbox := t.TempDir()
	external := t.TempDir()
	if err := os.Chmod(external, 0o750); err != nil {
		t.Fatal(err)
	}
	sentinelPath := filepath.Join(external, "sentinel")
	sentinel := []byte("do not mutate")
	if err := os.WriteFile(sentinelPath, sentinel, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sentinelPath, 0o640); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(external, "existing")
	if err := os.Mkdir(existing, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(existing, 0o750); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(sandbox, "state-link")
	if err := os.Symlink(external, link); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 10, 9, 1, 0, 0, 0, time.UTC)
	cfg := Config{
		StateDirectory: filepath.Join(link, "created", "state"),
		Engine:         "sqlite",
		MaxAge:         26 * time.Hour,
	}
	if err := Initialize(context.Background(), cfg, now); !errors.Is(err, ErrStateUnavailable) {
		t.Fatalf("Initialize through symlinked ancestor = %v, want state unavailable", err)
	}
	existingCfg := cfg
	existingCfg.StateDirectory = filepath.Join(link, "existing")
	if err := Initialize(context.Background(), existingCfg, now); !errors.Is(err, ErrStateUnavailable) {
		t.Fatalf("Initialize through symlinked existing state = %v, want state unavailable", err)
	}

	info, err := os.Stat(external)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o750 {
		t.Fatalf("external target mode = %#o, want %#o", got, 0o750)
	}
	gotSentinel, err := os.ReadFile(sentinelPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotSentinel, sentinel) {
		t.Fatalf("external sentinel changed from %q to %q", sentinel, gotSentinel)
	}
	sentinelInfo, err := os.Stat(sentinelPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := sentinelInfo.Mode().Perm(); got != 0o640 {
		t.Fatalf("external sentinel mode = %#o, want %#o", got, 0o640)
	}
	existingInfo, err := os.Stat(existing)
	if err != nil {
		t.Fatal(err)
	}
	if got := existingInfo.Mode().Perm(); got != 0o750 {
		t.Fatalf("existing external state mode = %#o, want %#o", got, 0o750)
	}
	if _, err := os.Lstat(filepath.Join(external, "created")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("symlink target received outside child = %v", err)
	}
}

func TestInitializeCreatesNestedStateRoot(t *testing.T) {
	base := t.TempDir()
	if err := os.Chmod(base, 0o750); err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		StateDirectory: filepath.Join(base, "one", "two", "state"),
		Engine:         "sqlite",
		MaxAge:         26 * time.Hour,
	}
	if err := Initialize(context.Background(), cfg, time.Date(2026, 10, 9, 1, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{
		filepath.Join(base, "one"),
		filepath.Join(base, "one", "two"),
		cfg.StateDirectory,
		filepath.Join(cfg.StateDirectory, cfg.Engine),
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if !info.IsDir() || info.Mode().Perm() != engineDirectoryMode {
			t.Fatalf("directory %s = mode %#o dir=%v, want directory mode %#o", path, info.Mode().Perm(), info.IsDir(), engineDirectoryMode)
		}
	}
	baseInfo, err := os.Stat(base)
	if err != nil {
		t.Fatal(err)
	}
	if got := baseInfo.Mode().Perm(); got != 0o750 {
		t.Fatalf("existing ancestor mode = %#o, want %#o", got, 0o750)
	}
	for _, name := range []string{runLockName, stateLockName, stateFileName} {
		if _, err := os.Stat(filepath.Join(cfg.StateDirectory, cfg.Engine, name)); err != nil {
			t.Fatalf("initialized %s: %v", name, err)
		}
	}
}

func TestInitializeFailsClosedWhenNewStateParentSyncFails(t *testing.T) {
	base := t.TempDir()
	parent := filepath.Join(base, "parent")
	if err := os.Mkdir(parent, engineDirectoryMode); err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		StateDirectory: filepath.Join(parent, "state"),
		Engine:         "sqlite",
		MaxAge:         26 * time.Hour,
	}

	oldSyncDirectory := stateSyncDirectory
	t.Cleanup(func() { stateSyncDirectory = oldSyncDirectory })
	remainingParentFailures := 2
	parentSyncs := 0
	stateChildSynced := false
	stateDirectory := filepath.Clean(cfg.StateDirectory)
	parentDirectory := filepath.Clean(parent)
	stateSyncDirectory = func(root *os.Root) error {
		name := filepath.Clean(root.Name())
		if name == stateDirectory {
			stateChildSynced = true
		}
		if name == parentDirectory && stateChildSynced {
			stateChildSynced = false
			parentSyncs++
			if remainingParentFailures > 0 {
				remainingParentFailures--
				return errors.New("injected parent directory sync failure")
			}
		}
		return oldSyncDirectory(root)
	}
	now := time.Date(2026, 10, 9, 1, 0, 0, 0, time.UTC)
	for attempt := 1; attempt <= 2; attempt++ {
		if err := Initialize(context.Background(), cfg, now); !errors.Is(err, ErrStateUnavailable) {
			t.Fatalf("Initialize attempt %d with parent sync failure = %v, want state unavailable", attempt, err)
		}
		if attempt == 1 {
			info, err := os.Stat(cfg.StateDirectory)
			if err != nil || !info.IsDir() {
				t.Fatalf("state directory after first failed parent sync: info=%v err=%v", info, err)
			}
		}
		if _, err := os.Stat(filepath.Join(cfg.StateDirectory, cfg.Engine)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("engine directory exists after failed parent sync attempt %d: %v", attempt, err)
		}
	}
	beforeRecoverySyncs := parentSyncs
	remainingParentFailures = 0
	if err := Initialize(context.Background(), cfg, now); err != nil {
		t.Fatalf("Initialize after parent sync recovery: %v", err)
	}
	if parentSyncs <= beforeRecoverySyncs {
		t.Fatalf("recovery did not resync parent directory: before=%d after=%d", beforeRecoverySyncs, parentSyncs)
	}
}
