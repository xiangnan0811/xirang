package updater

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Store struct {
	root        string
	bundlesRoot string
}

type StoredBundle struct {
	BundleFingerprint string
	AlreadyPresent    bool
}

const sharedStoreRootMode = 0o750

func NewStore(root string) (*Store, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || root == string(os.PathSeparator) || strings.ContainsAny(root, "\x00\r\n") {
		return nil, ErrPolicyRejected
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != sharedStoreRootMode {
		return nil, ErrPolicyRejected
	}
	bundlesRoot := filepath.Join(root, "bundles")
	if err := os.Mkdir(bundlesRoot, sharedStoreRootMode); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, ErrActivationFailed
	}
	info, err = os.Lstat(bundlesRoot)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != sharedStoreRootMode {
		return nil, ErrPolicyRejected
	}
	if err := syncDirectory(root); err != nil {
		return nil, ErrActivationFailed
	}
	return &Store{root: root, bundlesRoot: bundlesRoot}, nil
}

func (store *Store) StoreBundle(ctx context.Context, bundle VerifiedBundle) (StoredBundle, error) {
	if store == nil || ctx == nil || !lowerHex(bundle.BundleFingerprint, 64) || len(bundle.Files) == 0 || len(bundle.Files) > maximumBundleFiles {
		return StoredBundle{}, ErrPolicyRejected
	}
	if err := validateStoredPayload(bundle.Files); err != nil {
		return StoredBundle{}, err
	}
	destination := filepath.Join(store.bundlesRoot, bundle.BundleFingerprint)
	if _, err := os.Lstat(destination); err == nil {
		if err := validateStoredTree(destination, bundle.Files); err != nil {
			return StoredBundle{}, err
		}
		return StoredBundle{BundleFingerprint: bundle.BundleFingerprint, AlreadyPresent: true}, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return StoredBundle{}, ErrActivationFailed
	}
	staging, err := os.MkdirTemp(store.bundlesRoot, ".staging-")
	if err != nil {
		return StoredBundle{}, ErrActivationFailed
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(staging)
		}
	}()
	if err := os.Chmod(staging, 0o700); err != nil {
		return StoredBundle{}, ErrActivationFailed
	}
	for _, file := range bundle.Files {
		if err := ctx.Err(); err != nil {
			return StoredBundle{}, err
		}
		destinationPath := filepath.Join(staging, filepath.FromSlash(file.Path))
		if err := os.MkdirAll(filepath.Dir(destinationPath), 0o700); err != nil {
			return StoredBundle{}, ErrActivationFailed
		}
		handle, err := os.OpenFile(destinationPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return StoredBundle{}, ErrActivationFailed
		}
		written, writeErr := handle.Write(file.Content)
		syncErr := handle.Sync()
		closeErr := handle.Close()
		if writeErr != nil || syncErr != nil || closeErr != nil || written != len(file.Content) {
			return StoredBundle{}, ErrActivationFailed
		}
		if err := os.Chmod(destinationPath, 0o444); err != nil {
			return StoredBundle{}, ErrActivationFailed
		}
	}
	if err := freezeAndSyncTree(staging); err != nil {
		return StoredBundle{}, err
	}
	if err := os.Rename(staging, destination); err != nil {
		if _, statErr := os.Lstat(destination); statErr == nil {
			if validateErr := validateStoredTree(destination, bundle.Files); validateErr != nil {
				return StoredBundle{}, validateErr
			}
			return StoredBundle{BundleFingerprint: bundle.BundleFingerprint, AlreadyPresent: true}, nil
		}
		return StoredBundle{}, ErrActivationFailed
	}
	cleanup = false
	if err := syncDirectory(store.bundlesRoot); err != nil {
		return StoredBundle{}, ErrActivationFailed
	}
	if err := validateStoredTree(destination, bundle.Files); err != nil {
		return StoredBundle{}, err
	}
	return StoredBundle{BundleFingerprint: bundle.BundleFingerprint}, nil
}

func validateStoredPayload(files []BundleFilePayload) error {
	seen := make(map[string]bool, len(files))
	last := ""
	var total int64
	for _, file := range files {
		folded := strings.ToLower(file.Path)
		if !validBundlePath(file.Path) || file.Path <= last || seen[folded] || file.Mode != 0o444 || int64(len(file.Content)) > maximumBundleBytes-total {
			return ErrPolicyRejected
		}
		seen[folded] = true
		last = file.Path
		total += int64(len(file.Content))
	}
	return nil
}

func validateStoredTree(root string, files []BundleFilePayload) error {
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o555 {
		return ErrPolicyRejected
	}
	expectedFiles := make(map[string]BundleFilePayload, len(files))
	expectedDirs := map[string]bool{".": true}
	for _, file := range files {
		expectedFiles[filepath.FromSlash(file.Path)] = file
		for directory := filepath.Dir(filepath.FromSlash(file.Path)); directory != "."; directory = filepath.Dir(directory) {
			expectedDirs[directory] = true
		}
	}
	seenFiles := make(map[string]bool, len(files))
	err = filepath.WalkDir(root, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return ErrPolicyRejected
		}
		relative, err := filepath.Rel(root, current)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
			return ErrPolicyRejected
		}
		info, err := entry.Info()
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return ErrPolicyRejected
		}
		if entry.IsDir() {
			if !expectedDirs[relative] || info.Mode().Perm() != 0o555 {
				return ErrPolicyRejected
			}
			return nil
		}
		expected, ok := expectedFiles[relative]
		if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != 0o444 || info.Size() != int64(len(expected.Content)) {
			return ErrPolicyRejected
		}
		content, err := os.ReadFile(current)
		if err != nil || !bytesEqual(content, expected.Content) {
			return ErrPolicyRejected
		}
		seenFiles[relative] = true
		return nil
	})
	if err != nil || len(seenFiles) != len(expectedFiles) {
		return ErrPolicyRejected
	}
	return nil
}

func freezeAndSyncTree(root string) error {
	directories := make([]string, 0)
	err := filepath.WalkDir(root, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return ErrActivationFailed
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return ErrPolicyRejected
		}
		if entry.IsDir() {
			directories = append(directories, current)
		}
		return nil
	})
	if err != nil {
		return err
	}
	sort.Slice(directories, func(left, right int) bool { return len(directories[left]) > len(directories[right]) })
	for _, directory := range directories {
		if err := os.Chmod(directory, 0o555); err != nil || syncDirectory(directory) != nil {
			return ErrActivationFailed
		}
	}
	return nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

func bytesEqual(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
