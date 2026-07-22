package updater

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"

	workerCapabilities "xirang/backend/internal/backupasset/processing/capabilities"
)

type Store struct {
	rootPath    string
	root        *secureStoreDirectory
	bundlesRoot *secureStoreDirectory
	rootInfo    os.FileInfo
	bundlesInfo os.FileInfo
}

type StoredBundle struct {
	BundleFingerprint string
	AlreadyPresent    bool
}

const sharedStoreRootMode = 0o750

const storedBundleReceiptPath = workerCapabilities.StoredBundleReceiptPath

func validStoreComponent(value string) bool {
	return value != "" && value != "." && value != ".." && len(value) <= 255 && utf8.ValidString(value) &&
		!strings.ContainsAny(value, "/\\\x00\r\n")
}

func NewStore(root string) (*Store, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || root == string(os.PathSeparator) || strings.ContainsAny(root, "\x00\r\n") {
		return nil, ErrPolicyRejected
	}
	rootDirectory, err := openSecureStoreRoot(root)
	if err != nil {
		return nil, ErrPolicyRejected
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = rootDirectory.Close()
		}
	}()
	rootInfo, err := rootDirectory.Stat()
	if err != nil || !validStoreDirectory(rootInfo, sharedStoreRootMode) {
		return nil, ErrPolicyRejected
	}
	if err := rootDirectory.CreateDirectory("bundles", sharedStoreRootMode); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, ErrActivationFailed
	}
	bundlesRoot, err := rootDirectory.OpenDirectory("bundles")
	if err != nil {
		return nil, ErrPolicyRejected
	}
	defer func() {
		if cleanup {
			_ = bundlesRoot.Close()
		}
	}()
	bundlesInfo, err := bundlesRoot.Stat()
	if err != nil || !validStoreDirectory(bundlesInfo, sharedStoreRootMode) || rootDirectory.Sync() != nil {
		return nil, ErrActivationFailed
	}
	store := &Store{
		rootPath: root, root: rootDirectory, bundlesRoot: bundlesRoot,
		rootInfo: rootInfo, bundlesInfo: bundlesInfo,
	}
	if err := store.verifyRootIdentity(); err != nil {
		return nil, err
	}
	cleanup = false
	return store, nil
}

func (store *Store) StoreBundle(ctx context.Context, bundle VerifiedBundle) (StoredBundle, error) {
	if store == nil || ctx == nil || !lowerHex(bundle.BundleFingerprint, 64) || len(bundle.Files) == 0 || len(bundle.Files) > maximumBundleFiles {
		return StoredBundle{}, ErrPolicyRejected
	}
	if err := validateStoredPayload(bundle.Files); err != nil {
		return StoredBundle{}, err
	}
	canonical, _, err := BuildCanonicalTar(bundle.Files)
	if err != nil {
		return StoredBundle{}, err
	}
	return store.storeVerifiedBundle(ctx, bundle, bytes.NewReader(canonical), nil)
}

func (store *Store) storeInboxCandidate(ctx context.Context, candidate InboxCandidate) (StoredBundle, error) {
	if candidate.source == nil || candidate.source.handle == nil {
		return StoredBundle{}, ErrPolicyRejected
	}
	return store.storeVerifiedBundle(ctx, candidate.Bundle, candidate.source.handle, candidate.source.verifyStable)
}

func (store *Store) storeVerifiedBundle(
	ctx context.Context,
	bundle VerifiedBundle,
	source io.Reader,
	verifyStable func(context.Context) error,
) (StoredBundle, error) {
	if store == nil || ctx == nil || source == nil || !lowerHex(bundle.BundleFingerprint, 64) ||
		len(bundle.Manifest.Files) == 0 || len(bundle.Manifest.Files) > maximumBundleFiles {
		return StoredBundle{}, ErrPolicyRejected
	}
	receiptPayload, err := encodeStoredBundleReceipt(bundle)
	if err != nil {
		return StoredBundle{}, err
	}
	if err := store.verifyRootIdentity(); err != nil {
		return StoredBundle{}, err
	}
	destination := bundle.BundleFingerprint
	existing, openErr := store.bundlesRoot.OpenDirectory(destination)
	alreadyPresent := openErr == nil
	if openErr != nil && !errors.Is(openErr, os.ErrNotExist) {
		return StoredBundle{}, ErrPolicyRejected
	}
	if existing != nil {
		defer func() { _ = existing.Close() }()
	}
	stagingName := ""
	var staging *secureStoreDirectory
	cleanup := false
	if !alreadyPresent {
		stagingName, staging, err = store.createStagingDirectory()
		if err != nil {
			return StoredBundle{}, ErrActivationFailed
		}
		cleanup = true
	}
	defer func() {
		if staging != nil {
			_ = staging.Close()
		}
		if cleanup && stagingName != "" {
			removePrivateTree(store.bundlesRoot, stagingName)
		}
	}()
	consumer := canonicalTarMemberConsumer(nil)
	if !alreadyPresent {
		copyBuffer := make([]byte, 64<<10)
		consumer = func(ctx context.Context, declaration ManifestFile, content io.Reader) error {
			return writeStagedBundleMember(ctx, staging, declaration, content, copyBuffer)
		}
	}
	if err := verifyCanonicalTarStream(ctx, source, bundle.Manifest.Files, bundle.Manifest.BundleSHA256, consumer); err != nil {
		return StoredBundle{}, err
	}
	if verifyStable != nil {
		if err := verifyStable(ctx); err != nil {
			return StoredBundle{}, err
		}
	}
	if err := ctx.Err(); err != nil {
		return StoredBundle{}, err
	}
	if alreadyPresent {
		if err := validateStoredTree(ctx, existing, bundle.Manifest.Files, receiptPayload, bundle.BundleFingerprint); err != nil {
			return StoredBundle{}, err
		}
		if err := store.verifyRootIdentity(); err != nil {
			return StoredBundle{}, err
		}
		return StoredBundle{BundleFingerprint: bundle.BundleFingerprint, AlreadyPresent: true}, nil
	}
	if err := writeStoredBundleReceipt(ctx, staging, receiptPayload); err != nil {
		return StoredBundle{}, err
	}
	if err := freezeAndSyncTree(ctx, staging); err != nil {
		return StoredBundle{}, err
	}
	if err := ctx.Err(); err != nil {
		return StoredBundle{}, err
	}
	if err := staging.Close(); err != nil {
		return StoredBundle{}, ErrActivationFailed
	}
	staging = nil
	if err := store.bundlesRoot.RenameNoReplace(stagingName, destination); err != nil {
		if errors.Is(err, os.ErrExist) {
			existing, openErr := store.bundlesRoot.OpenDirectory(destination)
			if openErr != nil {
				return StoredBundle{}, ErrPolicyRejected
			}
			defer func() { _ = existing.Close() }()
			if validateErr := validateStoredTree(ctx, existing, bundle.Manifest.Files, receiptPayload, bundle.BundleFingerprint); validateErr != nil {
				return StoredBundle{}, validateErr
			}
			if err := store.verifyRootIdentity(); err != nil {
				return StoredBundle{}, err
			}
			return StoredBundle{BundleFingerprint: bundle.BundleFingerprint, AlreadyPresent: true}, nil
		}
		return StoredBundle{}, ErrActivationFailed
	}
	stagingName = destination
	if err := store.bundlesRoot.Sync(); err != nil {
		return StoredBundle{}, ErrActivationFailed
	}
	if err := ctx.Err(); err != nil {
		return StoredBundle{}, err
	}
	if err := store.verifyRootIdentity(); err != nil {
		return StoredBundle{}, err
	}
	cleanup = false
	return StoredBundle{BundleFingerprint: bundle.BundleFingerprint}, nil
}

func (store *Store) createStagingDirectory() (string, *secureStoreDirectory, error) {
	for attempt := 0; attempt < 16; attempt++ {
		path, err := privateTemporaryPath(store.rootPath, ".staging-")
		if err != nil {
			return "", nil, err
		}
		name := filepath.Base(path)
		if err := store.bundlesRoot.CreateDirectory(name, 0o700); err != nil {
			if errors.Is(err, os.ErrExist) {
				continue
			}
			return "", nil, err
		}
		directory, err := store.bundlesRoot.OpenDirectory(name)
		if err != nil {
			_ = store.bundlesRoot.RemoveDirectory(name)
			return "", nil, err
		}
		info, err := directory.Stat()
		if err != nil || !validStoreDirectory(info, 0o700) {
			_ = directory.Close()
			_ = store.bundlesRoot.RemoveDirectory(name)
			return "", nil, ErrPolicyRejected
		}
		return name, directory, nil
	}
	return "", nil, ErrActivationFailed
}

func (store *Store) verifyRootIdentity() error {
	if store == nil || store.root == nil || store.bundlesRoot == nil || store.rootInfo == nil || store.bundlesInfo == nil {
		return ErrPolicyRejected
	}
	currentRootInfo, err := store.root.Stat()
	if err != nil || !sameStoreDirectory(store.rootInfo, currentRootInfo, sharedStoreRootMode) {
		return ErrPolicyRejected
	}
	currentBundlesInfo, err := store.bundlesRoot.Stat()
	if err != nil || !sameStoreDirectory(store.bundlesInfo, currentBundlesInfo, sharedStoreRootMode) {
		return ErrPolicyRejected
	}
	pathRoot, err := openSecureStoreRoot(store.rootPath)
	if err != nil {
		return ErrPolicyRejected
	}
	defer func() { _ = pathRoot.Close() }()
	pathRootInfo, err := pathRoot.Stat()
	if err != nil || !sameStoreDirectory(store.rootInfo, pathRootInfo, sharedStoreRootMode) {
		return ErrPolicyRejected
	}
	pathBundles, err := pathRoot.OpenDirectory("bundles")
	if err != nil {
		return ErrPolicyRejected
	}
	defer func() { _ = pathBundles.Close() }()
	pathBundlesInfo, err := pathBundles.Stat()
	if err != nil || !sameStoreDirectory(store.bundlesInfo, pathBundlesInfo, sharedStoreRootMode) {
		return ErrPolicyRejected
	}
	return nil
}

func validStoreDirectory(info os.FileInfo, mode os.FileMode) bool {
	return info != nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 && info.Mode().Perm() == mode
}

func sameStoreDirectory(before, after os.FileInfo, mode os.FileMode) bool {
	return validStoreDirectory(before, mode) && validStoreDirectory(after, mode) && os.SameFile(before, after)
}

func validateStoredPayload(files []BundleFilePayload) error {
	seen := make(map[string]bool, len(files))
	last := ""
	var total int64
	for _, file := range files {
		folded := strings.ToLower(file.Path)
		if !validBundlePath(file.Path) || file.Path == storedBundleReceiptPath || file.Path <= last || seen[folded] ||
			file.Mode != 0o444 || int64(len(file.Content)) > maximumBundleBytes-total {
			return ErrPolicyRejected
		}
		seen[folded] = true
		last = file.Path
		total += int64(len(file.Content))
	}
	return nil
}

func validateStoredTree(
	ctx context.Context,
	root *secureStoreDirectory,
	files []ManifestFile,
	receiptPayload []byte,
	bundleFingerprint string,
) error {
	if root == nil {
		return ErrPolicyRejected
	}
	expectedFiles := make(map[string]ManifestFile, len(files))
	expectedDirs := map[string]bool{".": true}
	for _, file := range files {
		expectedFiles[file.Path] = file
		for directory := path.Dir(file.Path); directory != "."; directory = path.Dir(directory) {
			expectedDirs[directory] = true
		}
	}
	seenFiles := make(map[string]bool, len(files))
	receiptSeen, err := validateStoredDirectory(ctx, root, ".", expectedFiles, expectedDirs, seenFiles, receiptPayload)
	if err != nil || !receiptSeen || len(seenFiles) != len(expectedFiles) {
		return ErrPolicyRejected
	}
	receipt, err := decodeStoredBundleReceipt(receiptPayload)
	if err != nil || receipt.BundleFingerprint != bundleFingerprint {
		return ErrPolicyRejected
	}
	return nil
}

func validateStoredDirectory(
	ctx context.Context,
	directory *secureStoreDirectory,
	relative string,
	expectedFiles map[string]ManifestFile,
	expectedDirs map[string]bool,
	seenFiles map[string]bool,
	receiptPayload []byte,
) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	info, err := directory.Stat()
	if err != nil || !validStoreDirectory(info, 0o555) || !expectedDirs[relative] {
		return false, ErrPolicyRejected
	}
	entries, err := readStoreDirectory(directory, maximumBundleFiles+1)
	if err != nil {
		return false, err
	}
	receiptSeen := false
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if !validStoreComponent(entry.Name()) {
			return false, ErrPolicyRejected
		}
		childRelative := entry.Name()
		if relative != "." {
			childRelative = path.Join(relative, entry.Name())
		}
		if expectedDirs[childRelative] {
			child, openErr := directory.OpenDirectory(entry.Name())
			if openErr != nil {
				return false, ErrPolicyRejected
			}
			childReceiptSeen, validateErr := validateStoredDirectory(
				ctx, child, childRelative, expectedFiles, expectedDirs, seenFiles, receiptPayload,
			)
			closeErr := child.Close()
			if validateErr != nil || closeErr != nil {
				return false, ErrPolicyRejected
			}
			receiptSeen = receiptSeen || childReceiptSeen
			continue
		}
		if childRelative == storedBundleReceiptPath {
			if relative != "." || receiptSeen || verifySmallStoredFile(ctx, directory, entry.Name(), receiptPayload) != nil {
				return false, ErrPolicyRejected
			}
			receiptSeen = true
			continue
		}
		expected, ok := expectedFiles[childRelative]
		if !ok || seenFiles[childRelative] || verifyStoredFileDigest(ctx, directory, entry.Name(), expected.Size, expected.SHA256) != nil {
			return false, ErrPolicyRejected
		}
		seenFiles[childRelative] = true
	}
	return receiptSeen, nil
}

func writeStagedBundleMember(
	ctx context.Context,
	staging *secureStoreDirectory,
	declaration ManifestFile,
	content io.Reader,
	buffer []byte,
) error {
	if len(buffer) < 32<<10 || len(buffer) > 64<<10 {
		return ErrActivationFailed
	}
	parent, leaf, err := openStagedBundleParent(staging, declaration.Path)
	if err != nil {
		return err
	}
	if parent != staging {
		defer func() { _ = parent.Close() }()
	}
	handle, err := parent.OpenFile(leaf, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return ErrActivationFailed
	}
	closed := false
	defer func() {
		if !closed {
			_ = handle.Close()
		}
	}()
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		count, readErr := content.Read(buffer)
		if count > 0 {
			for offset := 0; offset < count; {
				written, writeErr := handle.Write(buffer[offset:count])
				if writeErr != nil || written <= 0 {
					return ErrActivationFailed
				}
				offset += written
				total += int64(written)
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			return ErrPolicyRejected
		}
		if count == 0 {
			return ErrPolicyRejected
		}
	}
	if total != declaration.Size || handle.Sync() != nil || handle.Chmod(0o444) != nil || handle.Sync() != nil {
		return ErrActivationFailed
	}
	if err := handle.Close(); err != nil {
		return ErrActivationFailed
	}
	closed = true
	if err := parent.Sync(); err != nil {
		return ErrActivationFailed
	}
	return nil
}

func openStagedBundleParent(root *secureStoreDirectory, bundlePath string) (*secureStoreDirectory, string, error) {
	if root == nil || !validBundlePath(bundlePath) {
		return nil, "", ErrPolicyRejected
	}
	components := strings.Split(bundlePath, "/")
	current := root
	owned := false
	for _, component := range components[:len(components)-1] {
		if err := current.CreateDirectory(component, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			if owned {
				_ = current.Close()
			}
			return nil, "", ErrActivationFailed
		}
		next, err := current.OpenDirectory(component)
		if err != nil {
			if owned {
				_ = current.Close()
			}
			return nil, "", ErrPolicyRejected
		}
		info, statErr := next.Stat()
		if statErr != nil || !validStoreDirectory(info, 0o700) {
			_ = next.Close()
			if owned {
				_ = current.Close()
			}
			return nil, "", ErrPolicyRejected
		}
		if owned {
			_ = current.Close()
		}
		current = next
		owned = true
	}
	return current, components[len(components)-1], nil
}

func writeStoredBundleReceipt(ctx context.Context, staging *secureStoreDirectory, payload []byte) error {
	handle, err := staging.OpenFile(storedBundleReceiptPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return ErrActivationFailed
	}
	closed := false
	defer func() {
		if !closed {
			_ = handle.Close()
		}
	}()
	for offset := 0; offset < len(payload); {
		if err := ctx.Err(); err != nil {
			return err
		}
		written, writeErr := handle.Write(payload[offset:])
		if writeErr != nil || written <= 0 {
			return ErrActivationFailed
		}
		offset += written
	}
	if handle.Sync() != nil || handle.Chmod(0o444) != nil || handle.Sync() != nil || handle.Close() != nil {
		return ErrActivationFailed
	}
	closed = true
	if err := staging.Sync(); err != nil {
		return ErrActivationFailed
	}
	return nil
}

func verifySmallStoredFile(ctx context.Context, directory *secureStoreDirectory, name string, expected []byte) error {
	handle, err := directory.OpenFile(name, os.O_RDONLY, 0)
	if err != nil {
		return ErrPolicyRejected
	}
	before, statErr := handle.Stat()
	if statErr != nil || !validStableStoredFile(before, int64(len(expected))) {
		_ = handle.Close()
		return ErrPolicyRejected
	}
	buffer := make([]byte, 64<<10)
	offset := 0
	for {
		if err := ctx.Err(); err != nil {
			_ = handle.Close()
			return err
		}
		count, readErr := handle.Read(buffer)
		if count > 0 {
			if offset+count > len(expected) || !bytes.Equal(buffer[:count], expected[offset:offset+count]) {
				_ = handle.Close()
				return ErrPolicyRejected
			}
			offset += count
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil || count == 0 {
			_ = handle.Close()
			return ErrPolicyRejected
		}
	}
	openedAfter, openErr := handle.Stat()
	closeErr := handle.Close()
	reopened, reopenErr := directory.OpenFile(name, os.O_RDONLY, 0)
	if reopenErr != nil {
		return ErrPolicyRejected
	}
	pathAfter, pathErr := reopened.Stat()
	reopenCloseErr := reopened.Close()
	if openErr != nil || closeErr != nil || pathErr != nil || reopenCloseErr != nil || offset != len(expected) ||
		!sameStableRegularFile(before, openedAfter, int64(len(expected))) ||
		!sameStableRegularFile(before, pathAfter, int64(len(expected))) {
		return ErrPolicyRejected
	}
	return nil
}

func verifyStoredFileDigest(ctx context.Context, directory *secureStoreDirectory, name string, expectedSize int64, expected string) error {
	handle, err := directory.OpenFile(name, os.O_RDONLY, 0)
	if err != nil {
		return ErrPolicyRejected
	}
	before, statErr := handle.Stat()
	if statErr != nil || !validStableStoredFile(before, expectedSize) {
		_ = handle.Close()
		return ErrPolicyRejected
	}
	digest := sha256.New()
	buffer := make([]byte, 64<<10)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			_ = handle.Close()
			return err
		}
		count, readErr := handle.Read(buffer)
		if count > 0 {
			total += int64(count)
			if total > before.Size() {
				_ = handle.Close()
				return ErrPolicyRejected
			}
			_, _ = digest.Write(buffer[:count])
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil || count == 0 {
			_ = handle.Close()
			return ErrPolicyRejected
		}
	}
	openedAfter, openErr := handle.Stat()
	closeErr := handle.Close()
	reopened, reopenErr := directory.OpenFile(name, os.O_RDONLY, 0)
	if reopenErr != nil {
		return ErrPolicyRejected
	}
	pathAfter, pathErr := reopened.Stat()
	reopenCloseErr := reopened.Close()
	if openErr != nil || closeErr != nil || pathErr != nil || reopenCloseErr != nil || total != before.Size() ||
		hex.EncodeToString(digest.Sum(nil)) != expected || !sameStableRegularFile(before, openedAfter, before.Size()) ||
		!sameStableRegularFile(before, pathAfter, before.Size()) {
		return ErrPolicyRejected
	}
	return nil
}

func validStableStoredFile(info os.FileInfo, expectedSize int64) bool {
	return info != nil && info.Mode().IsRegular() && info.Mode().Perm() == 0o444 && info.Size() == expectedSize && linkCount(info) == 1
}

func sameStableRegularFile(before, after os.FileInfo, expectedSize int64) bool {
	return before != nil && after != nil && before.Mode().IsRegular() && after.Mode().IsRegular() &&
		before.Mode().Perm() == 0o444 && after.Mode().Perm() == 0o444 && before.Size() == expectedSize &&
		after.Size() == expectedSize && before.ModTime().Equal(after.ModTime()) && os.SameFile(before, after) &&
		linkCount(before) == 1 && linkCount(after) == 1 && sameStableChangeTime(before, after)
}

func removePrivateTree(parent *secureStoreDirectory, name string) {
	if parent == nil || !validStoreComponent(name) {
		return
	}
	directory, err := parent.OpenDirectory(name)
	if err != nil {
		_ = parent.RemoveFile(name)
		return
	}
	_ = directory.Chmod(0o700)
	entries, readErr := readStoreDirectory(directory, maximumBundleFiles+1)
	if readErr == nil {
		for _, entry := range entries {
			if validStoreComponent(entry.Name()) {
				removePrivateTree(directory, entry.Name())
			}
		}
	}
	_ = directory.Close()
	_ = parent.RemoveDirectory(name)
}

func encodeStoredBundleReceipt(bundle VerifiedBundle) ([]byte, error) {
	if bundle.Manifest.SchemaVersion != 1 || len(bundle.Manifest.Capabilities) == 0 ||
		len(bundle.Manifest.Files) == 0 || bundle.Manifest.BundleSHA256 == "" {
		return nil, ErrPolicyRejected
	}
	capabilities := make([]workerCapabilities.StoredBundleCapability, 0, len(bundle.Manifest.Capabilities))
	for _, capability := range bundle.Manifest.Capabilities {
		capabilities = append(capabilities, workerCapabilities.StoredBundleCapability{
			Capability: capability.Capability, Schema: capability.Schema, Profiles: append([]string(nil), capability.Profiles...),
			ToolRevision: capability.ToolRevision, ModelRevision: capability.ModelRevision, DataRevision: capability.DataRevision,
		})
	}
	files := make([]workerCapabilities.StoredBundleFile, 0, len(bundle.Manifest.Files))
	for _, file := range bundle.Manifest.Files {
		files = append(files, workerCapabilities.StoredBundleFile{
			Path: file.Path, Mode: file.Mode, Size: file.Size, SHA256: file.SHA256,
		})
	}
	payload, err := workerCapabilities.EncodeStoredBundleReceipt(workerCapabilities.StoredBundleReceipt{
		SchemaVersion: 1, BundleFingerprint: bundle.BundleFingerprint, ManifestSchemaVersion: bundle.Manifest.SchemaVersion,
		Capabilities: capabilities, Files: files, BundleSHA256: bundle.Manifest.BundleSHA256,
	})
	if err != nil {
		return nil, ErrPolicyRejected
	}
	return payload, nil
}

func decodeStoredBundleReceipt(payload []byte) (workerCapabilities.StoredBundleReceipt, error) {
	return workerCapabilities.DecodeStoredBundleReceipt(payload)
}

func freezeAndSyncTree(ctx context.Context, root *secureStoreDirectory) error {
	if root == nil {
		return ErrActivationFailed
	}
	if err := root.rewind(); err != nil {
		return ErrActivationFailed
	}
	entries, err := readStoreDirectory(root, maximumBundleFiles+1)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !validStoreComponent(entry.Name()) {
			return ErrPolicyRejected
		}
		child, openErr := root.OpenDirectory(entry.Name())
		if openErr == nil {
			freezeErr := freezeAndSyncTree(ctx, child)
			closeErr := child.Close()
			if freezeErr != nil {
				return freezeErr
			}
			if closeErr != nil {
				return ErrActivationFailed
			}
			continue
		}
		handle, fileErr := root.OpenFile(entry.Name(), os.O_RDONLY, 0)
		if fileErr != nil {
			return ErrPolicyRejected
		}
		info, statErr := handle.Stat()
		if statErr != nil || info == nil {
			_ = handle.Close()
			return ErrPolicyRejected
		}
		syncErr := handle.Sync()
		closeErr := handle.Close()
		if !validStableStoredFile(info, info.Size()) || syncErr != nil || closeErr != nil {
			return ErrPolicyRejected
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if root.Chmod(0o555) != nil || root.Sync() != nil {
		return ErrActivationFailed
	}
	return nil
}

func readStoreDirectory(directory *secureStoreDirectory, maximum int) ([]os.DirEntry, error) {
	if directory == nil || maximum <= 0 {
		return nil, ErrPolicyRejected
	}
	entries := make([]os.DirEntry, 0, min(maximum, inboxReadBatchSize))
	for {
		remaining := maximum + 1 - len(entries)
		if remaining <= 0 {
			return nil, ErrPolicyRejected
		}
		batch, err := directory.ReadDir(min(remaining, inboxReadBatchSize))
		entries = append(entries, batch...)
		if len(entries) > maximum {
			return nil, ErrPolicyRejected
		}
		if errors.Is(err, io.EOF) {
			return entries, nil
		}
		if err != nil || len(batch) == 0 {
			return nil, ErrPolicyRejected
		}
	}
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
