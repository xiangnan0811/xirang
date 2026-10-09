package cronbackup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	engineDirectoryMode = 0o700
	stateFileMode       = 0o600
	lockWaitInterval    = 5 * time.Millisecond
)

type rootedStorage struct {
	base   *os.Root
	engine *os.Root
	cfg    Config
}

func (storage *rootedStorage) close() {
	if storage == nil {
		return
	}
	if storage.engine != nil {
		_ = storage.engine.Close()
	}
	if storage.base != nil {
		_ = storage.base.Close()
	}
}

func openExistingStorage(cfg Config) (*rootedStorage, error) {
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}
	base, err := openVerifiedRoot(cfg.StateDirectory, false)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotInitialized
		}
		return nil, fmt.Errorf("%w: open state directory: %v", ErrStateUnavailable, err)
	}
	engineInfo, err := base.Lstat(cfg.Engine)
	if err != nil {
		_ = base.Close()
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotInitialized
		}
		return nil, fmt.Errorf("%w: inspect engine directory: %v", ErrStateUnavailable, err)
	}
	if engineInfo.Mode()&os.ModeSymlink != 0 || !engineInfo.IsDir() || engineInfo.Mode().Perm() != engineDirectoryMode {
		_ = base.Close()
		return nil, fmt.Errorf("%w: unsafe engine directory", ErrStateUnavailable)
	}
	engine, err := base.OpenRoot(cfg.Engine)
	if err != nil {
		_ = base.Close()
		return nil, fmt.Errorf("%w: open engine directory: %v", ErrStateUnavailable, err)
	}
	openedInfo, openedErr := engine.Stat(".")
	currentInfo, currentErr := base.Lstat(cfg.Engine)
	if openedErr != nil || currentErr != nil || !openedInfo.IsDir() || currentInfo.Mode()&os.ModeSymlink != 0 || !currentInfo.IsDir() || currentInfo.Mode().Perm() != engineDirectoryMode || !os.SameFile(engineInfo, openedInfo) || !os.SameFile(openedInfo, currentInfo) {
		_ = engine.Close()
		_ = base.Close()
		return nil, fmt.Errorf("%w: engine directory changed", ErrStateUnavailable)
	}
	return &rootedStorage{base: base, engine: engine, cfg: cfg}, nil
}

func ensureStorage(cfg Config) (*rootedStorage, error) {
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}
	base, err := openVerifiedRoot(cfg.StateDirectory, true)
	if err != nil {
		return nil, fmt.Errorf("%w: create state directory: %v", ErrStateUnavailable, err)
	}
	engineInfo, err := base.Lstat(cfg.Engine)
	if errors.Is(err, os.ErrNotExist) {
		if err := base.Mkdir(cfg.Engine, engineDirectoryMode); err != nil && !errors.Is(err, os.ErrExist) {
			_ = base.Close()
			return nil, fmt.Errorf("%w: create engine directory: %v", ErrStateUnavailable, err)
		}
		engineInfo, err = base.Lstat(cfg.Engine)
	}
	if err != nil || engineInfo.Mode()&os.ModeSymlink != 0 || !engineInfo.IsDir() {
		_ = base.Close()
		return nil, fmt.Errorf("%w: unsafe engine directory", ErrStateUnavailable)
	}
	if engineInfo.Mode().Perm() != engineDirectoryMode {
		if err := chmodRootEntry(base, cfg.Engine, engineDirectoryMode); err != nil {
			_ = base.Close()
			return nil, fmt.Errorf("%w: restrict engine directory: %v", ErrStateUnavailable, err)
		}
		engineInfo, err = base.Lstat(cfg.Engine)
		if err != nil || engineInfo.Mode().Perm() != engineDirectoryMode {
			_ = base.Close()
			return nil, fmt.Errorf("%w: verify engine directory mode", ErrStateUnavailable)
		}
	}
	engine, err := base.OpenRoot(cfg.Engine)
	if err != nil {
		_ = base.Close()
		return nil, fmt.Errorf("%w: open engine directory: %v", ErrStateUnavailable, err)
	}
	openedInfo, openedErr := engine.Stat(".")
	currentInfo, currentErr := base.Lstat(cfg.Engine)
	if openedErr != nil || currentErr != nil || !openedInfo.IsDir() || currentInfo.Mode()&os.ModeSymlink != 0 || !currentInfo.IsDir() || currentInfo.Mode().Perm() != engineDirectoryMode || !os.SameFile(engineInfo, openedInfo) || !os.SameFile(openedInfo, currentInfo) {
		_ = engine.Close()
		_ = base.Close()
		return nil, fmt.Errorf("%w: engine directory changed", ErrStateUnavailable)
	}
	storage := &rootedStorage{base: base, engine: engine, cfg: cfg}
	if err := ensureLock(storage.engine, runLockName); err != nil {
		storage.close()
		return nil, err
	}
	if err := ensureLock(storage.engine, stateLockName); err != nil {
		storage.close()
		return nil, err
	}
	if err := syncDirectoryRoot(storage.engine); err != nil {
		storage.close()
		return nil, fmt.Errorf("%w: sync engine directory: %v", ErrStateUnavailable, err)
	}
	if err := syncDirectoryRoot(storage.base); err != nil {
		storage.close()
		return nil, fmt.Errorf("%w: sync state root: %v", ErrStateUnavailable, err)
	}
	return storage, nil
}

func openVerifiedRoot(path string, create bool) (*os.Root, error) {
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		absolute, absErr := filepath.Abs(clean)
		if absErr != nil {
			return nil, absErr
		}
		clean = filepath.Clean(absolute)
	}

	// Walk from the filesystem root one descriptor at a time. In particular,
	// do not use MkdirAll on the configured path: it would follow a symlink in
	// an ancestor before we had a chance to reject it.
	current, err := os.OpenRoot(string(filepath.Separator))
	if err != nil {
		return nil, err
	}
	components := strings.Split(strings.TrimPrefix(clean, string(filepath.Separator)), string(filepath.Separator))
	if clean == string(filepath.Separator) {
		components = nil
	}
	if len(components) == 0 {
		info, statErr := current.Stat(".")
		if statErr != nil {
			_ = current.Close()
			return nil, statErr
		}
		if !info.IsDir() || info.Mode().Perm() != engineDirectoryMode {
			_ = current.Close()
			return nil, fmt.Errorf("state root mode is not private")
		}
		return current, nil
	}

	for index, component := range components {
		info, statErr := current.Lstat(component)
		if errors.Is(statErr, os.ErrNotExist) {
			if !create {
				_ = current.Close()
				return nil, statErr
			}
			mkdirErr := current.Mkdir(component, engineDirectoryMode)
			if mkdirErr != nil && !errors.Is(mkdirErr, os.ErrExist) {
				_ = current.Close()
				return nil, mkdirErr
			}
			info, statErr = current.Lstat(component)
		}
		if statErr != nil {
			_ = current.Close()
			return nil, statErr
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			_ = current.Close()
			return nil, fmt.Errorf("state root component is not a directory")
		}

		child, openErr := current.OpenRoot(component)
		if openErr != nil {
			_ = current.Close()
			return nil, openErr
		}
		currentInfo, verifyErr := verifyOpenedDirectory(current, component, info, child)
		if verifyErr != nil {
			_ = child.Close()
			_ = current.Close()
			return nil, verifyErr
		}

		final := index == len(components)-1
		if final && currentInfo.Mode().Perm() != engineDirectoryMode {
			if !create {
				_ = child.Close()
				_ = current.Close()
				return nil, fmt.Errorf("state root mode is not private")
			}
			if chmodErr := chmodOpenedRoot(child, engineDirectoryMode); chmodErr != nil {
				_ = child.Close()
				_ = current.Close()
				return nil, chmodErr
			}
			currentInfo, verifyErr = verifyOpenedDirectory(current, component, info, child)
			if verifyErr != nil || currentInfo.Mode().Perm() != engineDirectoryMode {
				_ = child.Close()
				_ = current.Close()
				if verifyErr != nil {
					return nil, verifyErr
				}
				return nil, fmt.Errorf("state root mode is not private")
			}
		}

		if create {
			// A failed initialization can leave a newly visible directory
			// whose parent-entry durability was not established. Re-sync
			// every rooted component on every create-mode walk instead of
			// assuming that an existing component is already durable.
			if syncErr := stateSyncDirectory(child); syncErr != nil {
				_ = child.Close()
				_ = current.Close()
				return nil, syncErr
			}
			if syncErr := stateSyncDirectory(current); syncErr != nil {
				_ = child.Close()
				_ = current.Close()
				return nil, syncErr
			}
		}

		if final {
			_ = current.Close()
			return child, nil
		}
		_ = current.Close()
		current = child
	}
	return nil, fmt.Errorf("state root path was empty")
}

func verifyOpenedDirectory(parent *os.Root, name string, before os.FileInfo, child *os.Root) (os.FileInfo, error) {
	openedInfo, openedErr := child.Stat(".")
	currentInfo, currentErr := parent.Lstat(name)
	if openedErr != nil || currentErr != nil || !openedInfo.IsDir() || currentInfo.Mode()&os.ModeSymlink != 0 || !currentInfo.IsDir() || !os.SameFile(before, openedInfo) || !os.SameFile(openedInfo, currentInfo) {
		return nil, fmt.Errorf("state root component changed")
	}
	return currentInfo, nil
}

func chmodOpenedRoot(root *os.Root, mode os.FileMode) error {
	file, err := root.OpenFile(".", os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	return file.Chmod(mode)
}

func chmodRootEntry(root *os.Root, name string, mode os.FileMode) error {
	file, err := root.OpenFile(name, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	if err := file.Chmod(mode); err != nil {
		return err
	}
	return nil
}

const (
	runLockName   = "run.lock"
	stateLockName = "state.lock"
	stateFileName = "state.json"
)

func ensureLock(root *os.Root, name string) error {
	file, err := root.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, stateFileMode)
	if errors.Is(err, os.ErrExist) {
		file, err = openLock(root, name)
		if err != nil {
			return err
		}
		_ = file.Close()
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: create %s: %v", ErrStateUnavailable, name, err)
	}
	info, statErr := file.Stat()
	closeErr := file.Close()
	if statErr != nil || closeErr != nil || !info.Mode().IsRegular() || info.Mode().Perm() != stateFileMode {
		return fmt.Errorf("%w: unsafe %s", ErrStateUnavailable, name)
	}
	return nil
}

func openLock(root *os.Root, name string) (*os.File, error) {
	before, err := root.Lstat(name)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotInitialized
		}
		return nil, fmt.Errorf("%w: inspect %s: %v", ErrStateUnavailable, name, err)
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() || before.Mode().Perm() != stateFileMode {
		return nil, fmt.Errorf("%w: unsafe %s", ErrStateUnavailable, name)
	}
	file, err := root.OpenFile(name, os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("%w: open %s: %v", ErrStateUnavailable, name, err)
	}
	info, statErr := file.Stat()
	after, afterErr := root.Lstat(name)
	if statErr != nil || afterErr != nil || !info.Mode().IsRegular() || info.Mode().Perm() != stateFileMode || !os.SameFile(before, info) || !os.SameFile(info, after) {
		_ = file.Close()
		return nil, fmt.Errorf("%w: %s changed", ErrStateUnavailable, name)
	}
	return file, nil
}

func acquireLock(ctx context.Context, file *os.File, timeout time.Duration, busy error) error {
	if file == nil {
		return fmt.Errorf("%w: nil lock", ErrStateUnavailable)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	deadline := time.Now().Add(timeout)
	for {
		err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return nil
		}
		if !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EWOULDBLOCK) {
			return fmt.Errorf("%w: acquire lock: %v", ErrStateUnavailable, err)
		}
		if timeout <= 0 || !time.Now().Before(deadline) {
			return busy
		}
		timer := time.NewTimer(lockWaitInterval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func syncDirectoryRoot(root *os.Root) error {
	directory, err := root.OpenFile(".", os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	return stateSyncFile(directory)
}

func randomHexID() (string, error) {
	var bytesValue [16]byte
	if _, err := rand.Read(bytesValue[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytesValue[:]), nil
}

var (
	stateFileLstat     = func(root *os.Root, name string) (os.FileInfo, error) { return root.Lstat(name) }
	stateFileStat      = func(file *os.File) (os.FileInfo, error) { return file.Stat() }
	stateSyncFile      = func(file *os.File) error { return file.Sync() }
	stateRenameFile    = func(root *os.Root, oldName, newName string) error { return root.Rename(oldName, newName) }
	stateSyncDirectory = syncDirectoryRoot
)

func readStateFile(storage *rootedStorage) (stateDocument, bool, error) {
	info, err := stateFileLstat(storage.engine, stateFileName)
	if errors.Is(err, os.ErrNotExist) {
		return stateDocument{}, false, nil
	}
	if err != nil {
		return stateDocument{}, false, fmt.Errorf("%w: inspect state file: %v", ErrStateUnavailable, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != stateFileMode {
		return stateDocument{}, true, ErrStateInvalid
	}
	file, err := storage.engine.OpenFile(stateFileName, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return stateDocument{}, true, fmt.Errorf("%w: open state file: %v", ErrStateUnavailable, err)
	}
	defer func() { _ = file.Close() }()
	before, err := stateFileStat(file)
	if err != nil {
		return stateDocument{}, true, fmt.Errorf("%w: inspect open state file: %v", ErrStateUnavailable, err)
	}
	if !before.Mode().IsRegular() || before.Mode().Perm() != stateFileMode || before.Size() > maxStateBytes {
		return stateDocument{}, true, ErrStateInvalid
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maxStateBytes+1))
	if readErr != nil {
		return stateDocument{}, true, fmt.Errorf("%w: read state file: %v", ErrStateUnavailable, readErr)
	}
	after, err := stateFileStat(file)
	current, currentErr := stateFileLstat(storage.engine, stateFileName)
	if err != nil || currentErr != nil || !sameFileSnapshot(before, after) || !os.SameFile(after, current) {
		return stateDocument{}, true, fmt.Errorf("%w: state file changed", ErrStateUnavailable)
	}
	if len(data) > maxStateBytes || before.Size() != int64(len(data)) {
		return stateDocument{}, true, ErrStateInvalid
	}
	state, parseErr := parseState(data, storage.cfg)
	if parseErr != nil {
		return stateDocument{}, true, ErrStateInvalid
	}
	return state, true, nil
}

func sameFileSnapshot(before, after os.FileInfo) bool {
	return before != nil && after != nil && os.SameFile(before, after) && before.Size() == after.Size() && before.Mode() == after.Mode() && before.ModTime().Equal(after.ModTime())
}

func writeStateFile(storage *rootedStorage, state stateDocument) error {
	data, err := encodeState(state)
	if err != nil {
		return fmt.Errorf("%w: encode state: %v", ErrStatePublishFailed, err)
	}
	if existing, statErr := storage.engine.Lstat(stateFileName); statErr == nil {
		if existing.Mode()&os.ModeSymlink != 0 || !existing.Mode().IsRegular() || existing.Mode().Perm() != stateFileMode {
			return fmt.Errorf("%w: unsafe state destination", ErrStatePublishFailed)
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("%w: inspect state destination: %v", ErrStatePublishFailed, statErr)
	}
	suffix, err := randomHexID()
	if err != nil {
		return fmt.Errorf("%w: temporary name: %v", ErrStatePublishFailed, err)
	}
	temporary := ".state.json.tmp." + suffix
	file, err := storage.engine.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, stateFileMode)
	if err != nil {
		return fmt.Errorf("%w: create temporary state: %v", ErrStatePublishFailed, err)
	}
	removeTemporary := true
	defer func() {
		_ = file.Close()
		if removeTemporary {
			_ = storage.engine.Remove(temporary)
		}
	}()
	if err := writeAll(file, data); err != nil {
		return fmt.Errorf("%w: write temporary state: %v", ErrStatePublishFailed, err)
	}
	if err := stateSyncFile(file); err != nil {
		return fmt.Errorf("%w: sync temporary state: %v", ErrStatePublishFailed, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("%w: close temporary state: %v", ErrStatePublishFailed, err)
	}
	if err := stateRenameFile(storage.engine, temporary, stateFileName); err != nil {
		return fmt.Errorf("%w: publish state: %v", ErrStatePublishFailed, err)
	}
	removeTemporary = false
	if err := stateSyncDirectory(storage.engine); err != nil {
		return fmt.Errorf("%w: sync state directory: %v", ErrStatePublishFailed, err)
	}
	return nil
}

func writeAll(file *os.File, data []byte) error {
	for len(data) > 0 {
		written, err := file.Write(data)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}
