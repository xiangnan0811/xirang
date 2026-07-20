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

const maximumManifestBytes = int64(1 << 20)

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

func (inbox *Inbox) Scan(ctx context.Context, trust TrustStore, now time.Time) ([]InboxCandidate, error) {
	if inbox == nil || ctx == nil || inbox.rootInfo == nil || now.Location() != time.UTC {
		return nil, ErrPolicyRejected
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	preRoot, err := os.Lstat(inbox.root)
	if err != nil || !validReadOnlyDirectory(preRoot) || !os.SameFile(inbox.rootInfo, preRoot) {
		return nil, ErrPolicyRejected
	}
	root, err := openSecureInboxRoot(inbox.root)
	if err != nil {
		return nil, ErrPolicyRejected
	}
	defer func() { _ = root.Close() }()
	openedRoot, err := root.Stat()
	if err != nil || !validReadOnlyDirectory(openedRoot) || !os.SameFile(preRoot, openedRoot) {
		return nil, ErrPolicyRejected
	}
	entries, err := readDirectory(root)
	if err != nil {
		return nil, err
	}
	candidates := make([]InboxCandidate, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !validInboxEntryName(entry.Name()) {
			return nil, ErrPolicyRejected
		}
		candidate, err := inbox.scanCandidate(ctx, root, entry.Name(), trust, now)
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, candidate)
	}
	postRoot, err := os.Lstat(inbox.root)
	if err != nil || !validReadOnlyDirectory(postRoot) || !os.SameFile(openedRoot, postRoot) {
		return nil, ErrPolicyRejected
	}
	return candidates, nil
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
	defer func() { _ = candidateRoot.Close() }()
	openedDirectory, err := candidateRoot.Stat()
	if err != nil || !validReadOnlyDirectory(openedDirectory) {
		return InboxCandidate{}, ErrPolicyRejected
	}
	entries, err := readDirectory(candidateRoot)
	if err != nil || len(entries) != 2 || entries[0].Name() != "bundle.tar" || entries[1].Name() != "manifest.json" {
		return InboxCandidate{}, ErrPolicyRejected
	}
	manifest, err := readStableRegularFile(ctx, candidateRoot, "manifest.json", minInt64(inbox.maxPackage, maximumManifestBytes))
	if err != nil {
		return InboxCandidate{}, err
	}
	remaining := inbox.maxPackage - int64(len(manifest))
	if remaining <= 0 {
		return InboxCandidate{}, ErrPolicyRejected
	}
	bundle, err := readStableRegularFile(ctx, candidateRoot, "bundle.tar", remaining)
	if err != nil {
		return InboxCandidate{}, err
	}
	postDirectory, err := root.OpenDirectory(name)
	if err != nil {
		return InboxCandidate{}, ErrPolicyRejected
	}
	postInfo, statErr := postDirectory.Stat()
	closeErr := postDirectory.Close()
	if statErr != nil || closeErr != nil || !validReadOnlyDirectory(postInfo) || !os.SameFile(openedDirectory, postInfo) {
		return InboxCandidate{}, ErrPolicyRejected
	}
	verified, err := VerifyPackage(manifest, bundle, trust, now)
	if err != nil {
		return InboxCandidate{}, err
	}
	capabilities := append([]ManifestCapability(nil), verified.Manifest.Capabilities...)
	return InboxCandidate{
		Receipt: InboxReceipt{
			SchemaVersion: 1, SourceKind: verified.Manifest.SourceKind, SourceID: verified.Manifest.SourceID,
			Version: verified.Manifest.Version, ManifestDigest: verified.ManifestDigest,
			SigningKeyFingerprint: verified.SigningKeyFingerprint, BundleFingerprint: verified.BundleFingerprint,
			BundleSHA256: verified.Manifest.BundleSHA256, Capabilities: capabilities, VerifiedAt: now,
		},
		Bundle: verified,
	}, nil
}

func readDirectory(root *secureInboxDirectory) ([]os.DirEntry, error) {
	entries, err := root.ReadDir()
	if err != nil {
		return nil, ErrPolicyRejected
	}
	sort.Slice(entries, func(left, right int) bool { return entries[left].Name() < entries[right].Name() })
	return entries, nil
}

func readStableRegularFile(ctx context.Context, root *secureInboxDirectory, name string, maximum int64) ([]byte, error) {
	if maximum <= 0 {
		return nil, ErrPolicyRejected
	}
	handle, err := root.OpenRegular(name)
	if err != nil {
		return nil, ErrPolicyRejected
	}
	defer func() { _ = handle.Close() }()
	opened, err := handle.Stat()
	if err != nil || !validReadOnlyRegular(opened, maximum) {
		return nil, ErrPolicyRejected
	}
	payload, err := readBounded(ctx, handle, maximum)
	if err != nil {
		return nil, err
	}
	openedAfter, openErr := handle.Stat()
	postHandle, postOpenErr := root.OpenRegular(name)
	if postOpenErr != nil {
		return nil, ErrPolicyRejected
	}
	post, postErr := postHandle.Stat()
	postCloseErr := postHandle.Close()
	if openErr != nil || postErr != nil || postCloseErr != nil || !validReadOnlyRegular(openedAfter, maximum) ||
		!validReadOnlyRegular(post, maximum) || !os.SameFile(opened, openedAfter) ||
		!os.SameFile(openedAfter, post) || int64(len(payload)) != openedAfter.Size() {
		return nil, ErrPolicyRejected
	}
	return payload, nil
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
