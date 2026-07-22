package updater

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maximumManifestBytes   = int64(1 << 20)
	maximumInboxCandidates = 1024
	inboxReadBatchSize     = 64
	offlinePackageDeadline = 15 * time.Minute
)

type Inbox struct {
	root       string
	rootInfo   os.FileInfo
	maxPackage int64
}

type InboxReceipt struct {
	SchemaVersion         int                  `json:"schema_version"`
	SourceKind            string               `json:"source_kind"`
	SourceID              string               `json:"source_id"`
	Version               string               `json:"version"`
	ManifestDigest        string               `json:"manifest_digest"`
	SigningKeyFingerprint string               `json:"signing_key_fingerprint"`
	BundleFingerprint     string               `json:"bundle_fingerprint"`
	BundleSHA256          string               `json:"bundle_sha256"`
	Capabilities          []ManifestCapability `json:"capabilities"`
	VerifiedAt            time.Time            `json:"verified_at"`
}

type InboxCandidate struct {
	Receipt InboxReceipt   `json:"receipt"`
	Bundle  VerifiedBundle `json:"-"`
	source  *inboxBundleSource
}

type inboxBundleSource struct {
	root            *secureInboxDirectory
	candidateRoot   *secureInboxDirectory
	candidateName   string
	candidateInfo   os.FileInfo
	manifestInfo    os.FileInfo
	handle          *os.File
	bundleInfo      os.FileInfo
	bundleMaximum   int64
	stabilityPassed bool
}

func NewInbox(root string, maxPackageBytes int64) (*Inbox, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || root == string(os.PathSeparator) ||
		strings.ContainsAny(root, "\x00\r\n") || maxPackageBytes <= 0 || maxPackageBytes > maximumBundleBytes+maximumManifestBytes {
		return nil, ErrPolicyRejected
	}
	info, err := os.Lstat(root)
	if err != nil || !validReadOnlyDirectory(info) {
		return nil, ErrPolicyRejected
	}
	return &Inbox{root: root, rootInfo: info, maxPackage: maxPackageBytes}, nil
}

func (inbox *Inbox) Scan(
	ctx context.Context,
	trust TrustStore,
	now time.Time,
	visit func(context.Context, InboxCandidate) error,
) error {
	if inbox == nil || ctx == nil || inbox.rootInfo == nil || now.Location() != time.UTC || visit == nil {
		return ErrPolicyRejected
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	preRoot, err := os.Lstat(inbox.root)
	if err != nil || !validReadOnlyDirectory(preRoot) || !os.SameFile(inbox.rootInfo, preRoot) {
		return ErrPolicyRejected
	}
	root, err := openSecureInboxRoot(inbox.root)
	if err != nil {
		return ErrPolicyRejected
	}
	defer func() { _ = root.Close() }()
	openedRoot, err := root.Stat()
	if err != nil || !sameStableDirectory(preRoot, openedRoot) {
		return ErrPolicyRejected
	}
	entries, err := readBoundedDirectory(root, maximumInboxCandidates)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !validInboxEntryName(entry.Name()) {
			return ErrPolicyRejected
		}
		if err := inbox.scanAndVisitCandidate(ctx, root, entry.Name(), trust, now, visit); err != nil {
			return err
		}
	}
	postRoot, err := os.Lstat(inbox.root)
	if err != nil || !sameStableDirectory(openedRoot, postRoot) {
		return ErrPolicyRejected
	}
	return nil
}

func (inbox *Inbox) scanAndVisitCandidate(
	ctx context.Context,
	root *secureInboxDirectory,
	name string,
	trust TrustStore,
	now time.Time,
	visit func(context.Context, InboxCandidate) error,
) error {
	packageCtx, cancel := context.WithTimeout(ctx, offlinePackageDeadline)
	defer cancel()
	candidate, err := inbox.scanCandidate(packageCtx, root, name, trust, now)
	if err != nil {
		return err
	}
	visitErr := visit(packageCtx, candidate)
	closeErr := candidate.source.close()
	if visitErr != nil {
		return visitErr
	}
	if closeErr != nil || !candidate.source.stabilityPassed {
		return ErrPolicyRejected
	}
	return nil
}

func (inbox *Inbox) scanCandidate(
	ctx context.Context,
	root *secureInboxDirectory,
	name string,
	trust TrustStore,
	now time.Time,
) (InboxCandidate, error) {
	candidateRoot, err := root.OpenDirectory(name)
	if err != nil {
		return InboxCandidate{}, ErrPolicyRejected
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = candidateRoot.Close()
		}
	}()
	openedDirectory, err := candidateRoot.Stat()
	if err != nil || !validReadOnlyDirectory(openedDirectory) {
		return InboxCandidate{}, ErrPolicyRejected
	}
	entries, err := readBoundedDirectory(candidateRoot, 2)
	if err != nil || len(entries) != 2 || entries[0].Name() != "bundle.tar" || entries[1].Name() != "manifest.json" {
		return InboxCandidate{}, ErrPolicyRejected
	}
	manifest, manifestInfo, err := readStableRegularFile(ctx, candidateRoot, "manifest.json", minInt64(inbox.maxPackage, maximumManifestBytes))
	if err != nil {
		return InboxCandidate{}, err
	}
	remaining := inbox.maxPackage - int64(len(manifest))
	if remaining <= 0 {
		return InboxCandidate{}, ErrPolicyRejected
	}
	verified, err := VerifyManifest(manifest, trust, now)
	if err != nil {
		return InboxCandidate{}, err
	}
	bundleMaximum := minInt64(remaining, maximumBundleBytes)
	handle, err := candidateRoot.OpenRegular("bundle.tar")
	if err != nil {
		return InboxCandidate{}, ErrPolicyRejected
	}
	bundleInfo, err := handle.Stat()
	if err != nil || !validReadOnlyRegular(bundleInfo, bundleMaximum) || bundleInfo.Size()+int64(len(manifest)) > inbox.maxPackage {
		_ = handle.Close()
		return InboxCandidate{}, ErrPolicyRejected
	}
	source := &inboxBundleSource{
		root: root, candidateRoot: candidateRoot, candidateName: name, candidateInfo: openedDirectory,
		manifestInfo: manifestInfo, handle: handle, bundleInfo: bundleInfo, bundleMaximum: bundleMaximum,
	}
	capabilities := append([]ManifestCapability(nil), verified.Manifest.Capabilities...)
	cleanup = false
	return InboxCandidate{
		Receipt: InboxReceipt{
			SchemaVersion: 1, SourceKind: verified.Manifest.SourceKind, SourceID: verified.Manifest.SourceID,
			Version: verified.Manifest.Version, ManifestDigest: verified.ManifestDigest,
			SigningKeyFingerprint: verified.SigningKeyFingerprint, BundleFingerprint: verified.BundleFingerprint,
			BundleSHA256: verified.Manifest.BundleSHA256, Capabilities: capabilities, VerifiedAt: now,
		},
		Bundle: verified,
		source: source,
	}, nil
}

type boundedDirectoryReader interface {
	ReadDir(int) ([]os.DirEntry, error)
}

func readBoundedDirectory(root boundedDirectoryReader, maximum int) ([]os.DirEntry, error) {
	if root == nil || maximum <= 0 {
		return nil, ErrPolicyRejected
	}
	entries := make([]os.DirEntry, 0, min(maximum, inboxReadBatchSize))
	for {
		remaining := maximum + 1 - len(entries)
		if remaining <= 0 {
			return nil, ErrPolicyRejected
		}
		batchSize := min(remaining, inboxReadBatchSize)
		batch, err := root.ReadDir(batchSize)
		entries = append(entries, batch...)
		if len(entries) > maximum {
			return nil, ErrPolicyRejected
		}
		if errors.Is(err, io.EOF) {
			sort.Slice(entries, func(left, right int) bool { return entries[left].Name() < entries[right].Name() })
			return entries, nil
		}
		if err != nil || len(batch) == 0 {
			return nil, ErrPolicyRejected
		}
	}
}

func readStableRegularFile(
	ctx context.Context,
	root *secureInboxDirectory,
	name string,
	maximum int64,
) ([]byte, os.FileInfo, error) {
	if maximum <= 0 {
		return nil, nil, ErrPolicyRejected
	}
	handle, err := root.OpenRegular(name)
	if err != nil {
		return nil, nil, ErrPolicyRejected
	}
	defer func() { _ = handle.Close() }()
	opened, err := handle.Stat()
	if err != nil || !validReadOnlyRegular(opened, maximum) {
		return nil, nil, ErrPolicyRejected
	}
	payload, err := readBounded(ctx, handle, maximum)
	if err != nil {
		return nil, nil, err
	}
	openedAfter, openErr := handle.Stat()
	postHandle, postOpenErr := root.OpenRegular(name)
	if postOpenErr != nil {
		return nil, nil, ErrPolicyRejected
	}
	post, postErr := postHandle.Stat()
	postCloseErr := postHandle.Close()
	if openErr != nil || postErr != nil || postCloseErr != nil || int64(len(payload)) != openedAfter.Size() ||
		!sameStableRegularFile(opened, openedAfter, int64(len(payload))) ||
		!sameStableRegularFile(opened, post, int64(len(payload))) {
		return nil, nil, ErrPolicyRejected
	}
	return payload, openedAfter, nil
}

func (source *inboxBundleSource) verifyStable(ctx context.Context) error {
	if source == nil || source.handle == nil || source.stabilityPassed {
		return ErrPolicyRejected
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	openedAfter, statErr := source.handle.Stat()
	offset, seekErr := source.handle.Seek(0, io.SeekCurrent)
	closeErr := source.handle.Close()
	source.handle = nil
	if statErr != nil || seekErr != nil || closeErr != nil || offset != source.bundleInfo.Size() ||
		!sameStableRegularFile(source.bundleInfo, openedAfter, source.bundleInfo.Size()) {
		return ErrPolicyRejected
	}
	candidateAfter, err := source.candidateRoot.Stat()
	if err != nil || !sameStableDirectory(source.candidateInfo, candidateAfter) {
		return ErrPolicyRejected
	}
	postDirectory, err := source.root.OpenDirectory(source.candidateName)
	if err != nil {
		return ErrPolicyRejected
	}
	defer func() { _ = postDirectory.Close() }()
	postInfo, err := postDirectory.Stat()
	if err != nil || !sameStableDirectory(source.candidateInfo, postInfo) {
		return ErrPolicyRejected
	}
	entries, err := readBoundedDirectory(postDirectory, 2)
	if err != nil || len(entries) != 2 || entries[0].Name() != "bundle.tar" || entries[1].Name() != "manifest.json" {
		return ErrPolicyRejected
	}
	manifestHandle, err := postDirectory.OpenRegular("manifest.json")
	if err != nil {
		return ErrPolicyRejected
	}
	manifestInfo, manifestStatErr := manifestHandle.Stat()
	manifestCloseErr := manifestHandle.Close()
	if manifestStatErr != nil || manifestCloseErr != nil ||
		!sameStableRegularFile(source.manifestInfo, manifestInfo, source.manifestInfo.Size()) {
		return ErrPolicyRejected
	}
	postBundle, err := postDirectory.OpenRegular("bundle.tar")
	if err != nil {
		return ErrPolicyRejected
	}
	postBundleInfo, postStatErr := postBundle.Stat()
	postCloseErr := postBundle.Close()
	if postStatErr != nil || postCloseErr != nil ||
		!sameStableRegularFile(source.bundleInfo, postBundleInfo, source.bundleInfo.Size()) {
		return ErrPolicyRejected
	}
	source.stabilityPassed = true
	return nil
}

func (source *inboxBundleSource) close() error {
	if source == nil {
		return nil
	}
	var handleErr error
	if source.handle != nil {
		handleErr = source.handle.Close()
		source.handle = nil
	}
	var directoryErr error
	if source.candidateRoot != nil {
		directoryErr = source.candidateRoot.Close()
		source.candidateRoot = nil
	}
	if handleErr != nil {
		return handleErr
	}
	return directoryErr
}

func sameStableDirectory(before, after os.FileInfo) bool {
	return validReadOnlyDirectory(before) && validReadOnlyDirectory(after) && os.SameFile(before, after) &&
		before.ModTime().Equal(after.ModTime()) && sameStableChangeTime(before, after)
}

func readBounded(ctx context.Context, reader io.Reader, maximum int64) ([]byte, error) {
	buffer := make([]byte, 32<<10)
	payload := make([]byte, 0, minInt64(maximum, int64(len(buffer))))
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		count, err := reader.Read(buffer)
		if count > 0 {
			if int64(len(payload))+int64(count) > maximum {
				return nil, ErrPolicyRejected
			}
			payload = append(payload, buffer[:count]...)
		}
		if errors.Is(err, io.EOF) {
			return payload, nil
		}
		if err != nil {
			return nil, ErrPolicyRejected
		}
	}
}

func validReadOnlyDirectory(info os.FileInfo) bool {
	return info != nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 && info.Mode().Perm() == 0o555
}

func validReadOnlyRegular(info os.FileInfo, maximum int64) bool {
	return info != nil && info.Mode().IsRegular() && info.Mode().Perm() == 0o444 && info.Size() > 0 && info.Size() <= maximum &&
		linkCount(info) == 1
}

func linkCount(info os.FileInfo) uint64 {
	if info == nil || info.Sys() == nil {
		return 0
	}
	value := reflect.Indirect(reflect.ValueOf(info.Sys()))
	if !value.IsValid() || value.Kind() != reflect.Struct {
		return 0
	}
	field := value.FieldByName("Nlink")
	if !field.IsValid() {
		return 0
	}
	switch field.Kind() {
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return field.Uint()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if field.Int() > 0 {
			return uint64(field.Int())
		}
	}
	return 0
}

func sameStableChangeTime(before, after os.FileInfo) bool {
	beforeSeconds, beforeNanos, beforeOK := stableChangeTime(before)
	afterSeconds, afterNanos, afterOK := stableChangeTime(after)
	if beforeOK != afterOK {
		return false
	}
	return !beforeOK || beforeSeconds == afterSeconds && beforeNanos == afterNanos
}

func stableChangeTime(info os.FileInfo) (int64, int64, bool) {
	if info == nil || info.Sys() == nil {
		return 0, 0, false
	}
	value := reflect.Indirect(reflect.ValueOf(info.Sys()))
	if !value.IsValid() || value.Kind() != reflect.Struct {
		return 0, 0, false
	}
	for _, fieldName := range []string{"Ctim", "Ctimespec"} {
		field := reflect.Indirect(value.FieldByName(fieldName))
		if !field.IsValid() || field.Kind() != reflect.Struct {
			continue
		}
		seconds := field.FieldByName("Sec")
		nanos := field.FieldByName("Nsec")
		if seconds.IsValid() && nanos.IsValid() && seconds.CanInt() && nanos.CanInt() {
			return seconds.Int(), nanos.Int(), true
		}
	}
	return 0, 0, false
}

func validInboxEntryName(value string) bool {
	return value != "" && value != "." && value != ".." && len(value) <= 255 && utf8.ValidString(value) &&
		!strings.ContainsAny(value, "/\\\x00\r\n")
}

func minInt64(left, right int64) int64 {
	if left < right {
		return left
	}
	return right
}
