package provider

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/sshutil"

	"github.com/pkg/sftp"
)

const (
	stagedPayloadRootDirectory   = ".xirang/rclone-publication"
	stagedPayloadOwnerMarkerName = ".owner-v1"
	stagedPayloadOwnerPrefix     = "xirang-rclone-staging-owner-v1:"
	maximumStagedPayloadName     = 128
)

var ErrRcloneStagedPayloadScanLimitExceeded = errors.New("rclone staged payload scan limit exceeded")

type StagedPayloadRequest struct {
	AttemptID string
	Name      string
	Payload   []byte `json:"-"`
	MaxBytes  int64
}

type StagedPayloadRef struct {
	attemptID       string
	name            string
	path            string
	size            int64
	digest          string
	ownerMarkerPath string
	ownerDigest     string
	lease           *stagedPayloadLease
}

type stagedPayloadLease struct {
	mu      sync.Mutex
	active  int
	cleaned bool
}

func (ref StagedPayloadRef) acquire() (func(), error) {
	if err := ref.validate(); err != nil || ref.lease == nil {
		return nil, fmt.Errorf("%w: invalid staged payload reference", backupasset.ErrInvalidState)
	}
	ref.lease.mu.Lock()
	defer ref.lease.mu.Unlock()
	if ref.lease.cleaned {
		return nil, fmt.Errorf("%w: staged payload already cleaned", backupasset.ErrInvalidState)
	}
	ref.lease.active++
	var once sync.Once
	return func() {
		once.Do(func() {
			ref.lease.mu.Lock()
			if ref.lease.active > 0 {
				ref.lease.active--
			}
			ref.lease.mu.Unlock()
		})
	}, nil
}

func (ref StagedPayloadRef) validate() error {
	if backupasset.ValidateOpaqueID(ref.attemptID) != nil || !validStagedPayloadName(ref.name) || ref.size <= 0 || !lowerHex(ref.digest, 64) ||
		!lowerHex(ref.ownerDigest, 64) || !path.IsAbs(ref.path) || path.Clean(ref.path) != ref.path ||
		!path.IsAbs(ref.ownerMarkerPath) || path.Clean(ref.ownerMarkerPath) != ref.ownerMarkerPath ||
		path.Base(ref.path) != ref.name || path.Base(ref.ownerMarkerPath) != stagedPayloadOwnerMarkerName ||
		path.Dir(ref.path) != path.Dir(ref.ownerMarkerPath) || path.Base(path.Dir(ref.path)) != ref.attemptID || ref.lease == nil {
		return fmt.Errorf("%w: invalid staged payload reference", backupasset.ErrInvalidState)
	}
	return nil
}

type StagedPayloadTransport interface {
	Stage(context.Context, RemoteCommandAccess, StagedPayloadRequest) (StagedPayloadRef, error)
	Cleanup(context.Context, RemoteCommandAccess, StagedPayloadRef) error
	CleanupAged(context.Context, RemoteCommandAccess, time.Duration, int) error
}

type stagedPayloadFile interface {
	io.Reader
	io.Writer
	io.Closer
	Stat() (os.FileInfo, error)
}

type stagedPayloadSession interface {
	Getwd() (string, error)
	RealPath(string) (string, error)
	Lstat(string) (os.FileInfo, error)
	Stat(string) (os.FileInfo, error)
	Mkdir(string) error
	Chmod(string, os.FileMode) error
	Open(string) (stagedPayloadFile, error)
	OpenFile(string, int) (stagedPayloadFile, error)
	ReadDir(string) ([]os.FileInfo, error)
	Remove(string) error
	RemoveDirectory(string) error
	Close() error
}

type stagedPayloadSessionFactory func(context.Context, RemoteCommandAccess) (stagedPayloadSession, error)

type RcloneStagedPayloadTransport struct {
	factory      stagedPayloadSessionFactory
	ownershipKey []byte
	now          func() time.Time
	mu           sync.Mutex
	leases       map[string]*stagedPayloadLease
}

func NewRcloneStagedPayloadTransport(dialer *sshutil.NodeDialer, ownershipKey []byte, now func() time.Time) (*RcloneStagedPayloadTransport, error) {
	if dialer == nil {
		return nil, fmt.Errorf("%w: staged payload Node dialer unavailable", backupasset.ErrInvalidState)
	}
	factory := func(ctx context.Context, access RemoteCommandAccess) (stagedPayloadSession, error) {
		if access.Node.ID == 0 {
			return nil, fmt.Errorf("%w: staged payload node unavailable", backupasset.ErrInvalidState)
		}
		client, err := dialer.Dial(ctx, access.Node, sshutil.PurposeTaskBackup, access.Audit)
		if err != nil {
			return nil, err
		}
		sftpClient, err := sftp.NewClient(client)
		if err != nil {
			_ = client.Close()
			return nil, fmt.Errorf("%w: open staged payload SFTP session", backupasset.ErrProviderUnavailable)
		}
		return &sftpStagedPayloadSession{client: sftpClient, connection: client}, nil
	}
	return newRcloneStagedPayloadTransport(factory, ownershipKey, now)
}

func newRcloneStagedPayloadTransportForTest(factory stagedPayloadSessionFactory, ownershipKey []byte, now func() time.Time) *RcloneStagedPayloadTransport {
	transport, err := newRcloneStagedPayloadTransport(factory, ownershipKey, now)
	if err != nil {
		panic(err)
	}
	return transport
}

func newRcloneStagedPayloadTransport(factory stagedPayloadSessionFactory, ownershipKey []byte, now func() time.Time) (*RcloneStagedPayloadTransport, error) {
	if factory == nil || len(ownershipKey) < 32 {
		return nil, fmt.Errorf("%w: invalid staged payload transport dependencies", backupasset.ErrInvalidState)
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &RcloneStagedPayloadTransport{
		factory: factory, ownershipKey: append([]byte(nil), ownershipKey...), now: now, leases: make(map[string]*stagedPayloadLease),
	}, nil
}

func (transport *RcloneStagedPayloadTransport) Stage(ctx context.Context, access RemoteCommandAccess, request StagedPayloadRequest) (StagedPayloadRef, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return StagedPayloadRef{}, err
	}
	if err := validateStagedPayloadRequest(request); err != nil {
		return StagedPayloadRef{}, err
	}
	session, err := transport.factory(ctx, access)
	if err != nil {
		return StagedPayloadRef{}, err
	}
	root, err := ensureStagedPayloadRoot(ctx, session)
	if err != nil {
		_ = session.Close()
		return StagedPayloadRef{}, err
	}
	ownerDigest := transport.ownerDigest(root.home, request.AttemptID)
	attemptDirectory := path.Join(root.root, request.AttemptID)
	ownerMarkerPath := path.Join(attemptDirectory, stagedPayloadOwnerMarkerName)
	if err := ensureStagedAttemptDirectory(ctx, session, attemptDirectory, ownerMarkerPath, ownerDigest); err != nil {
		_ = session.Close()
		return StagedPayloadRef{}, err
	}
	payloadPath := path.Join(attemptDirectory, request.Name)
	if err := writeAndVerifyStagedPayload(ctx, session, payloadPath, request.Payload, request.MaxBytes); err != nil {
		_ = session.Close()
		return StagedPayloadRef{}, err
	}
	if err := verifyStableStagedRoot(session, root); err != nil {
		_ = session.Remove(payloadPath)
		_ = session.Close()
		return StagedPayloadRef{}, err
	}
	if err := session.Close(); err != nil {
		return StagedPayloadRef{}, fmt.Errorf("%w: close staged payload session", backupasset.ErrProviderUnavailable)
	}
	lease := transport.leaseFor(request.AttemptID)
	return StagedPayloadRef{
		attemptID: request.AttemptID, name: request.Name, path: payloadPath, size: int64(len(request.Payload)),
		digest: sha256Hex(request.Payload), ownerMarkerPath: ownerMarkerPath, ownerDigest: ownerDigest, lease: lease,
	}, nil
}

func (transport *RcloneStagedPayloadTransport) Cleanup(ctx context.Context, access RemoteCommandAccess, ref StagedPayloadRef) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := ref.validate(); err != nil {
		return err
	}
	if err := lockStagedPayloadCleanup(ref.lease); err != nil {
		return err
	}
	cleaned := false
	defer func() { finishStagedPayloadCleanup(ref.lease, cleaned) }()

	session, err := transport.factory(ctx, access)
	if err != nil {
		return err
	}
	defer session.Close() //nolint:errcheck // primary cleanup error wins
	root, err := ensureStagedPayloadRoot(ctx, session)
	if err != nil {
		return err
	}
	expectedDirectory := path.Join(root.root, ref.attemptID)
	if ref.path != path.Join(expectedDirectory, ref.name) || ref.ownerMarkerPath != path.Join(expectedDirectory, stagedPayloadOwnerMarkerName) ||
		ref.ownerDigest != transport.ownerDigest(root.home, ref.attemptID) {
		return fmt.Errorf("%w: staged payload reference changed identity", backupasset.ErrInvalidState)
	}
	if err := verifyStagedOwnerMarker(ctx, session, ref.ownerMarkerPath, ref.ownerDigest); err != nil {
		return err
	}
	if err := verifyStagedPayload(ctx, session, ref.path, ref.size, ref.digest, ref.size); err != nil {
		return err
	}
	if err := session.Remove(ref.path); err != nil {
		return fmt.Errorf("%w: remove staged payload", backupasset.ErrProviderUnavailable)
	}
	if err := cleanupEmptyStagedAttempt(session, expectedDirectory, ref.ownerMarkerPath); err != nil {
		return err
	}
	if err := verifyStableStagedRoot(session, root); err != nil {
		return err
	}
	cleaned = true
	return nil
}

func (transport *RcloneStagedPayloadTransport) CleanupAged(ctx context.Context, access RemoteCommandAccess, age time.Duration, limit int) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if age <= 0 || limit <= 0 || limit > 4096 {
		return fmt.Errorf("%w: invalid staged payload cleanup bounds", backupasset.ErrInvalidState)
	}
	session, err := transport.factory(ctx, access)
	if err != nil {
		return err
	}
	defer session.Close() //nolint:errcheck // primary scan error wins
	root, err := ensureStagedPayloadRoot(ctx, session)
	if err != nil {
		return err
	}
	entries, readErr := session.ReadDir(root.root)
	if readErr != nil {
		return fmt.Errorf("%w: read staged payload root", backupasset.ErrProviderUnavailable)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	if len(entries) > limit {
		return fmt.Errorf("%w: staged payload scan limit exceeded", ErrRcloneStagedPayloadScanLimitExceeded)
	}
	cutoff := transport.now().UTC().Add(-age)
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		attemptID := entry.Name()
		if !entry.IsDir() || entry.Mode()&os.ModeSymlink != 0 || backupasset.ValidateOpaqueID(attemptID) != nil || !entry.ModTime().Before(cutoff) {
			continue
		}
		lease := transport.leaseFor(attemptID)
		if err := lockStagedPayloadCleanup(lease); err != nil {
			continue
		}
		cleaned := false
		attemptDirectory := path.Join(root.root, attemptID)
		ownerMarkerPath := path.Join(attemptDirectory, stagedPayloadOwnerMarkerName)
		ownerDigest := transport.ownerDigest(root.home, attemptID)
		if markerErr := verifyStagedOwnerMarker(ctx, session, ownerMarkerPath, ownerDigest); markerErr == nil {
			if removeErr := removeOwnedStagedAttempt(session, attemptDirectory, ownerMarkerPath); removeErr != nil {
				finishStagedPayloadCleanup(lease, false)
				return fmt.Errorf("%w: remove aged staged attempt", backupasset.ErrProviderUnavailable)
			}
			cleaned = true
		}
		finishStagedPayloadCleanup(lease, cleaned)
	}
	return verifyStableStagedRoot(session, root)
}

type stagedPayloadRoot struct {
	home string
	root string
}

func ensureStagedPayloadRoot(ctx context.Context, session stagedPayloadSession) (stagedPayloadRoot, error) {
	if err := ctx.Err(); err != nil {
		return stagedPayloadRoot{}, err
	}
	workingDirectory, err := session.Getwd()
	if err != nil {
		return stagedPayloadRoot{}, fmt.Errorf("%w: resolve SFTP working directory", backupasset.ErrProviderUnavailable)
	}
	home, err := session.RealPath(".")
	if err != nil || !path.IsAbs(home) || path.Clean(home) != home || strings.ContainsRune(home, '\x00') {
		return stagedPayloadRoot{}, fmt.Errorf("%w: invalid SFTP home directory", backupasset.ErrInvalidState)
	}
	workingRealPath, err := session.RealPath(workingDirectory)
	if err != nil || workingRealPath != home {
		return stagedPayloadRoot{}, fmt.Errorf("%w: SFTP home identity mismatch", backupasset.ErrInvalidState)
	}
	xirangDirectory := path.Join(home, ".xirang")
	root := path.Join(home, stagedPayloadRootDirectory)
	for _, directory := range []string{xirangDirectory, root} {
		if err := ensureStrictStagedDirectory(session, directory); err != nil {
			return stagedPayloadRoot{}, err
		}
	}
	return stagedPayloadRoot{home: home, root: root}, nil
}

func ensureStrictStagedDirectory(session stagedPayloadSession, directory string) error {
	info, err := session.Lstat(directory)
	if err != nil {
		if !isSFTPNotExist(err) {
			return fmt.Errorf("%w: inspect staged payload directory", backupasset.ErrProviderUnavailable)
		}
		if err := session.Mkdir(directory); err != nil {
			return fmt.Errorf("%w: create staged payload directory", backupasset.ErrProviderUnavailable)
		}
		info, err = session.Lstat(directory)
	}
	if err != nil || info == nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: staged payload directory is not a real directory", backupasset.ErrInvalidState)
	}
	if err := session.Chmod(directory, 0o700); err != nil {
		return fmt.Errorf("%w: secure staged payload directory", backupasset.ErrProviderUnavailable)
	}
	info, err = session.Stat(directory)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return fmt.Errorf("%w: staged payload directory mode mismatch", backupasset.ErrInvalidState)
	}
	realPath, err := session.RealPath(directory)
	if err != nil || realPath != directory {
		return fmt.Errorf("%w: staged payload directory identity mismatch", backupasset.ErrInvalidState)
	}
	return nil
}

func ensureStagedAttemptDirectory(ctx context.Context, session stagedPayloadSession, directory, markerPath, ownerDigest string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := session.Lstat(directory)
	created := false
	if err != nil {
		if !isSFTPNotExist(err) {
			return fmt.Errorf("%w: inspect staged attempt directory", backupasset.ErrProviderUnavailable)
		}
		if err := session.Mkdir(directory); err != nil {
			return fmt.Errorf("%w: create staged attempt directory", backupasset.ErrProviderUnavailable)
		}
		created = true
		info, err = session.Lstat(directory)
	}
	if err != nil || info == nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: invalid staged attempt directory", backupasset.ErrInvalidState)
	}
	if err := session.Chmod(directory, 0o700); err != nil {
		return fmt.Errorf("%w: secure staged attempt directory", backupasset.ErrProviderUnavailable)
	}
	if created {
		if err := writeExclusiveStagedFile(session, markerPath, []byte(stagedPayloadOwnerPrefix+ownerDigest+"\n")); err != nil {
			_ = session.RemoveDirectory(directory)
			return err
		}
	}
	return verifyStagedOwnerMarker(ctx, session, markerPath, ownerDigest)
}

func writeAndVerifyStagedPayload(ctx context.Context, session stagedPayloadSession, filePath string, payload []byte, maxBytes int64) error {
	if err := writeExclusiveStagedFile(session, filePath, payload); err != nil {
		return err
	}
	return verifyStagedPayload(ctx, session, filePath, int64(len(payload)), sha256Hex(payload), maxBytes)
}

func writeExclusiveStagedFile(session stagedPayloadSession, filePath string, payload []byte) error {
	file, err := session.OpenFile(filePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		return fmt.Errorf("%w: create exclusive staged payload", backupasset.ErrInvalidState)
	}
	if err := session.Chmod(filePath, 0o600); err != nil {
		_ = file.Close()
		_ = session.Remove(filePath)
		return fmt.Errorf("%w: secure staged payload", backupasset.ErrProviderUnavailable)
	}
	written, writeErr := writeAllStagedPayload(file, payload)
	closeErr := file.Close()
	if writeErr != nil || written != len(payload) || closeErr != nil {
		_ = session.Remove(filePath)
		return fmt.Errorf("%w: incomplete staged payload write", backupasset.ErrProviderUnavailable)
	}
	return nil
}

func writeAllStagedPayload(writer io.Writer, payload []byte) (int, error) {
	total := 0
	for total < len(payload) {
		written, err := writer.Write(payload[total:])
		total += written
		if err != nil {
			return total, err
		}
		if written == 0 {
			return total, io.ErrShortWrite
		}
	}
	return total, nil
}

func verifyStagedOwnerMarker(ctx context.Context, session stagedPayloadSession, markerPath, ownerDigest string) error {
	expected := []byte(stagedPayloadOwnerPrefix + ownerDigest + "\n")
	return verifyStagedPayload(ctx, session, markerPath, int64(len(expected)), sha256Hex(expected), int64(len(expected)))
}

func verifyStagedPayload(ctx context.Context, session stagedPayloadSession, filePath string, expectedSize int64, expectedDigest string, maxBytes int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := session.Lstat(filePath)
	if err != nil || info == nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 || info.Size() != expectedSize || expectedSize <= 0 || expectedSize > maxBytes {
		return fmt.Errorf("%w: staged payload stat mismatch", backupasset.ErrInvalidState)
	}
	file, err := session.Open(filePath)
	if err != nil {
		return fmt.Errorf("%w: open staged payload for verification", backupasset.ErrProviderUnavailable)
	}
	hash := sha256.New()
	read, readErr := io.Copy(hash, io.LimitReader(file, maxBytes+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || read != expectedSize || hex.EncodeToString(hash.Sum(nil)) != expectedDigest {
		return fmt.Errorf("%w: staged payload byte verification failed", backupasset.ErrInvalidState)
	}
	after, err := session.Lstat(filePath)
	if err != nil || after.Size() != info.Size() || after.Mode() != info.Mode() || !after.ModTime().Equal(info.ModTime()) {
		return fmt.Errorf("%w: staged payload changed during verification", backupasset.ErrInvalidState)
	}
	return nil
}

func cleanupEmptyStagedAttempt(session stagedPayloadSession, directory, markerPath string) error {
	entries, readErr := session.ReadDir(directory)
	if readErr != nil {
		return fmt.Errorf("%w: inspect staged attempt entries", backupasset.ErrProviderUnavailable)
	}
	if len(entries) != 1 || entries[0].Name() != stagedPayloadOwnerMarkerName || !entries[0].Mode().IsRegular() {
		return nil
	}
	if err := session.Remove(markerPath); err != nil {
		return fmt.Errorf("%w: remove staged ownership marker", backupasset.ErrProviderUnavailable)
	}
	if err := session.RemoveDirectory(directory); err != nil {
		return fmt.Errorf("%w: remove staged attempt directory", backupasset.ErrProviderUnavailable)
	}
	return nil
}

func removeOwnedStagedAttempt(session stagedPayloadSession, directory, markerPath string) error {
	entries, readErr := session.ReadDir(directory)
	if readErr != nil {
		return readErr
	}
	if len(entries) > 4096 {
		return fmt.Errorf("staged attempt cleanup bound exceeded")
	}
	for _, entry := range entries {
		if entry.Name() == stagedPayloadOwnerMarkerName {
			continue
		}
		if !validStagedPayloadName(entry.Name()) || !entry.Mode().IsRegular() || entry.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("unknown staged attempt entry")
		}
		if err := session.Remove(path.Join(directory, entry.Name())); err != nil {
			return err
		}
	}
	if err := session.Remove(markerPath); err != nil {
		return err
	}
	return session.RemoveDirectory(directory)
}

func verifyStableStagedRoot(session stagedPayloadSession, root stagedPayloadRoot) error {
	realHome, err := session.RealPath(".")
	if err != nil || realHome != root.home {
		return fmt.Errorf("%w: SFTP home changed during staged operation", backupasset.ErrInvalidState)
	}
	realRoot, err := session.RealPath(root.root)
	if err != nil || realRoot != root.root {
		return fmt.Errorf("%w: staged payload root changed during operation", backupasset.ErrInvalidState)
	}
	return nil
}

func lockStagedPayloadCleanup(lease *stagedPayloadLease) error {
	if lease == nil {
		return fmt.Errorf("%w: staged payload lease missing", backupasset.ErrInvalidState)
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.active > 0 || lease.cleaned {
		return fmt.Errorf("%w: staged payload is active or already cleaned", backupasset.ErrInvalidState)
	}
	lease.active = -1
	return nil
}

func finishStagedPayloadCleanup(lease *stagedPayloadLease, cleaned bool) {
	lease.mu.Lock()
	lease.active = 0
	lease.cleaned = cleaned
	lease.mu.Unlock()
}

func (transport *RcloneStagedPayloadTransport) leaseFor(attemptID string) *stagedPayloadLease {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	lease := transport.leases[attemptID]
	if lease == nil || lease.cleaned {
		lease = &stagedPayloadLease{}
		transport.leases[attemptID] = lease
	}
	return lease
}

func (transport *RcloneStagedPayloadTransport) ownerDigest(home, attemptID string) string {
	mac := hmac.New(sha256.New, transport.ownershipKey)
	_, _ = io.WriteString(mac, "xirang-rclone-staging-owner-v1\n")
	_, _ = io.WriteString(mac, home)
	_, _ = io.WriteString(mac, "\n")
	_, _ = io.WriteString(mac, attemptID)
	_, _ = io.WriteString(mac, "\n")
	return hex.EncodeToString(mac.Sum(nil))
}

func validateStagedPayloadRequest(request StagedPayloadRequest) error {
	if backupasset.ValidateOpaqueID(request.AttemptID) != nil || !validStagedPayloadName(request.Name) || request.MaxBytes <= 0 ||
		len(request.Payload) == 0 || int64(len(request.Payload)) > request.MaxBytes {
		return fmt.Errorf("%w: invalid staged payload request", backupasset.ErrInvalidState)
	}
	return nil
}

func validStagedPayloadName(value string) bool {
	if value == "" || len(value) > maximumStagedPayloadName || value == stagedPayloadOwnerMarkerName || path.Base(value) != value ||
		strings.HasPrefix(value, ".") || strings.ContainsRune(value, '\x00') {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || strings.ContainsRune("._-", character) {
			continue
		}
		return false
	}
	return true
}

func sha256Hex(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func isSFTPNotExist(err error) bool {
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, sftp.ErrSSHFxNoSuchFile)
}

type sftpStagedPayloadSession struct {
	client     *sftp.Client
	connection io.Closer
}

func (session *sftpStagedPayloadSession) Getwd() (string, error) { return session.client.Getwd() }
func (session *sftpStagedPayloadSession) RealPath(value string) (string, error) {
	return session.client.RealPath(value)
}
func (session *sftpStagedPayloadSession) Lstat(value string) (os.FileInfo, error) {
	return session.client.Lstat(value)
}
func (session *sftpStagedPayloadSession) Stat(value string) (os.FileInfo, error) {
	return session.client.Stat(value)
}
func (session *sftpStagedPayloadSession) Mkdir(value string) error {
	return session.client.Mkdir(value)
}
func (session *sftpStagedPayloadSession) Chmod(value string, mode os.FileMode) error {
	return session.client.Chmod(value, mode)
}
func (session *sftpStagedPayloadSession) Open(value string) (stagedPayloadFile, error) {
	return session.client.Open(value)
}
func (session *sftpStagedPayloadSession) OpenFile(value string, flags int) (stagedPayloadFile, error) {
	return session.client.OpenFile(value, flags)
}
func (session *sftpStagedPayloadSession) ReadDir(value string) ([]os.FileInfo, error) {
	return session.client.ReadDir(value)
}
func (session *sftpStagedPayloadSession) Remove(value string) error {
	return session.client.Remove(value)
}
func (session *sftpStagedPayloadSession) RemoveDirectory(value string) error {
	return session.client.RemoveDirectory(value)
}
func (session *sftpStagedPayloadSession) Close() error {
	clientErr := session.client.Close()
	if session.connection != nil {
		connectionErr := session.connection.Close()
		if clientErr == nil {
			clientErr = connectionErr
		}
	}
	return clientErr
}
