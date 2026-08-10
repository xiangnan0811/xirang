package recovery

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash"
	"io"
	"math"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"
	"xirang/backend/internal/settings"
	"xirang/backend/internal/sshutil"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

const (
	// RecoverySchemaUseLatchID is the public symbolic kind of the permanent
	// recovery-schema use latch. Its fixed row ID remains private to the
	// durable worker boundary.
	RecoverySchemaUseLatchID    = "schema_use_latch"
	recoverySchemaUseLatchRowID = "00000000000000000000000000000069"

	recoveryWorkspaceMarkerSchemaVersion        = 1
	recoveryWorkspaceMarkerDocumentMaxBytes     = 2048
	recoveryWorkspaceMarkerNonceBytes           = 32
	recoveryWorkspaceMarkerInstallationDomain   = "xirang/recovery/workspace-marker-installation/v1"
	recoveryWorkspaceMarkerDocumentDomain       = "xirang/recovery/workspace-marker-document/v1"
	recoveryTargetSessionBindingDomain          = "xirang/recovery/target-session-binding/v1"
	recoveryTargetPreflightSessionBindingDomain = "xirang/recovery/target-preflight-session-binding/v1"
	targetVerifyPermitProofDomain               = "xirang/recovery/target-verify-permit-proof/v2"
	targetResultReadPermitProofDomain           = "xirang/recovery/target-result-read-permit-proof/v1"
	targetPreflightRequestDomain                = "xirang/recovery/target-preflight-request/v1"
	targetPreflightPermitProofDomain            = "xirang/recovery/target-preflight-permit-proof/v1"
	targetItemWritePermitProofDomain            = "xirang/recovery/target-item-write-permit-proof/v2"
	targetFinalizeOverwritePermitProofDomain    = "xirang/recovery/target-finalize-overwrite-permit-proof/v1"
	targetDeletePermitProofDomain               = "xirang/recovery/target-delete-permit-proof/v1"
	recoveryOverwriteArtifactBindingDomain      = "xirang/recovery/overwrite-artifact-binding/v1"
	recoveryOverwriteMarkerDocumentDomain       = "xirang/recovery/overwrite-artifact-marker/v1"
	recoveryDeleteArtifactBindingDomain         = "xirang/recovery/delete-artifact/v1"
	recoveryDeleteIntentDocumentDomain          = "xirang/recovery/delete-intent/v1"
	recoveryDeleteVerifiedDocumentDomain        = "xirang/recovery/delete-verified/v1"
	recoveryWorkspaceObservationDomain          = "xirang/recovery/sftp-owned-workspace-observation/v1"
	recoverySFTPRegularFileObservationDomain    = "xirang/recovery/sftp-regular-file-observation/v1"
	recoverySFTPRootObservationDomain           = "xirang/recovery/sftp-root-observation/v1"
	recoverySFTPFilesystemObservationDomain     = "xirang/recovery/sftp-filesystem-observation/v1"
	recoverySFTPTargetObservationDomain         = "xirang/recovery/sftp-target-observation/v1"
	recoverySFTPDeleteEntryIdentityDomain       = "xirang/recovery/sftp-delete-entry-identity/v1"
	recoverySFTPRootRevisionPrefix              = "sftpr1:"
	recoverySFTPFilesystemRevisionPrefix        = "sftpf1:"
	recoverySFTPTargetRevisionPrefix            = "sftpt1:"
	recoverySFTPTargetAbsentKind                = "absent"
	recoveryWorkspaceMarkerFileName             = ".xirang-recovery-owner-v1"
	recoveryWorkspaceMarkerTempPrefix           = ".xirang-recovery-owner-v1.tmp-"
	recoveryOwnedCleanupArtifactDomain          = "xirang/recovery/owned-cleanup-artifact/v1"
	recoveryOwnedCleanupVerifiedDomain          = "xirang/recovery/owned-cleanup-verified/v1"
	recoveryOwnedCleanupArtifactPrefix          = ".xirang-recovery-owned-cleanup-v1-"
	recoveryOwnedCleanupVerifiedPrefix          = ".xirang-recovery-owned-verified-v1-"
	recoveryReconciliationFindingDomain         = "xirang/recovery/reconcile-finding/v1"
	recoveryReconciliationCursorDomain          = "xirang/recovery/reconcile-cursor/v1"
	recoveryReconciliationCursorSchemaVersion   = 1
	recoveryReconciliationCursorHeaderBytes     = 2 + 4
	recoveryReconciliationCursorOrdinalBytes    = 4
	recoveryReconciliationCursorDigestBytes     = sha256.Size
	recoveryReconciliationCursorWireBytes       = recoveryReconciliationCursorHeaderBytes + recoveryReconciliationCursorOrdinalBytes + 3*recoveryReconciliationCursorDigestBytes
	recoveryPayloadTempPrefix                   = ".xirang-recovery-file-v1.tmp-"
	recoveryPayloadTempEntropyBytes             = 32
	recoveryCleanupRemoveLimit                  = 256
	recoveryCleanupReadBatch                    = 64
	recoveryCleanupMaxDepth                     = 64
	recoveryOverwriteMarkerSchemaVersion        = 1
	recoveryOverwriteMarkerDocumentMaxBytes     = 1024
	recoveryOverwriteArtifactComponentMaxBytes  = 255
	recoveryOverwriteArtifactPrefix             = ".xirang-recovery-overwrite-v1-"
	recoveryDeleteMarkerSchemaVersion           = 1
	recoveryDeleteMarkerDocumentMaxBytes        = 1024
	recoveryDeleteArtifactComponentMaxBytes     = 255
	recoveryDeleteArtifactPrefix                = ".xirang-recovery-delete-"
	recoveryPreflightCommandMaxBytes            = 4 << 10
	recoveryResultReadChunkBytes                = 32 << 10
)

var (
	ErrInvalidTargetPermit                = errors.New("invalid recovery target permit")
	ErrInvalidRecoveryWorkspaceMarker     = errors.New("invalid recovery workspace marker")
	ErrRecoveryWorkspaceMarkerUnavailable = errors.New("recovery workspace marker unavailable")
	ErrRecoveryTargetUnavailable          = errors.New("recovery target unavailable")
)

type recoveryTargetSessionBinding struct {
	PlanID             string
	PlanBindingDigest  string
	NodeID             uint
	NodeRevision       string
	CredentialRevision string
	RootID             string
	RootLocator        string
	RootLocatorDigest  string
	RootRevision       string
	bindingDigest      string
}

type recoveryTargetReconciliationSessionBinding struct {
	nodeID             uint
	nodeRevision       string
	credentialRevision string
	rootID             string
	rootLocator        string
	rootLocatorDigest  string
	rootRevision       string
	bindingDigest      string
}

func (binding recoveryTargetReconciliationSessionBinding) digest() string {
	return framedDigest(
		"xirang/recovery/target-reconciliation-session-binding/v1",
		strconv.FormatUint(uint64(binding.nodeID), 10), binding.nodeRevision,
		binding.credentialRevision, binding.rootID, binding.rootLocator,
		binding.rootLocatorDigest, binding.rootRevision,
	)
}

func (binding recoveryTargetReconciliationSessionBinding) auditCorrelationID() string {
	return framedDigest(
		"xirang/recovery/target-reconciliation-audit-correlation/v1",
		strconv.FormatUint(uint64(binding.nodeID), 10), binding.rootID, binding.rootRevision,
	)
}

func (binding recoveryTargetReconciliationSessionBinding) valid() bool {
	if binding.nodeID == 0 || !validOpaqueRevision(binding.nodeRevision) ||
		!validOpaqueRevision(binding.credentialRevision) ||
		!validBoundedOpaque(binding.rootID, targetRootIDMax) ||
		!validDigest(binding.rootLocatorDigest) || !validOpaqueRevision(binding.rootRevision) ||
		!validDigest(binding.bindingDigest) || strings.HasPrefix(binding.rootLocator, "enc:v2:") {
		return false
	}
	locatorDigest, err := settings.RecoveryTargetRootLocatorDigest(
		binding.nodeID, binding.rootID, binding.rootLocator,
	)
	return err == nil && locatorDigest == binding.rootLocatorDigest &&
		binding.bindingDigest == binding.digest() && validDigest(binding.auditCorrelationID())
}

type recoveryTargetPreflightSessionBinding struct {
	planID                 string
	planBindingDigest      string
	planTransitionRevision uint64
	targetMode             TargetMode
	nodeID                 uint
	nodeRevision           string
	credentialRevision     string
	rootID                 string
	rootLocator            string
	rootLocatorDigest      string
	rootRevision           string
	filesystemRevision     string
	targetPathDigest       string
	privateRelativeLocator string
	targetRevision         string
	preflightRevision      string
	bindingDigest          string
}

func newRecoveryTargetPreflightSessionBinding(
	plan model.BackupAssetRecoveryPlan,
) (recoveryTargetPreflightSessionBinding, error) {
	mode := TargetMode(plan.TargetMode)
	if !validOpaqueID(plan.ID) || PlanState(plan.State) != PlanStateDraft ||
		!validDigest(plan.BindingDigest) || plan.TransitionRevision == 0 || mode.Validate() != nil ||
		plan.TargetNodeID == 0 || !validOpaqueRevision(plan.TargetBaseRevision) ||
		!validOpaqueRevision(plan.CredentialScopeRevision) ||
		!validBoundedOpaque(plan.TargetRootID, targetRootIDMax) ||
		!validDigest(plan.RootLocatorDigest) || !validOpaqueRevision(plan.RootRevision) ||
		!validOpaqueRevision(plan.FilesystemRevision) || !validOpaqueRevision(plan.TargetBaseRevision) ||
		!validDigest(plan.PathDigest) || !validOpaqueRevision(plan.PreflightRevision) ||
		plan.PreflightExpiresAt.IsZero() || strings.HasPrefix(plan.EncryptedTargetRootLocator, "enc:v2:") ||
		strings.HasPrefix(plan.EncryptedTargetRelativePath, "enc:v2:") ||
		!validTargetRelativeLocator(plan.EncryptedTargetRelativePath) {
		return recoveryTargetPreflightSessionBinding{}, ErrInvalidTargetPermit
	}
	locatorDigest, err := settings.RecoveryTargetRootLocatorDigest(
		plan.TargetNodeID, plan.TargetRootID, plan.EncryptedTargetRootLocator,
	)
	if err != nil || locatorDigest != plan.RootLocatorDigest {
		return recoveryTargetPreflightSessionBinding{}, ErrInvalidTargetPermit
	}
	pathDigest, err := TargetPathDigest(
		plan.TargetRootID, plan.RootLocatorDigest, plan.EncryptedTargetRelativePath,
	)
	if err != nil || pathDigest != plan.PathDigest {
		return recoveryTargetPreflightSessionBinding{}, ErrInvalidTargetPermit
	}
	binding := recoveryTargetPreflightSessionBinding{
		planID: plan.ID, planBindingDigest: plan.BindingDigest,
		planTransitionRevision: plan.TransitionRevision, targetMode: mode,
		nodeID: plan.TargetNodeID, nodeRevision: plan.TargetBaseRevision,
		credentialRevision: plan.CredentialScopeRevision,
		rootID:             plan.TargetRootID, rootLocator: plan.EncryptedTargetRootLocator,
		rootLocatorDigest: plan.RootLocatorDigest, rootRevision: plan.RootRevision,
		filesystemRevision: plan.FilesystemRevision, targetPathDigest: plan.PathDigest,
		privateRelativeLocator: plan.EncryptedTargetRelativePath,
		targetRevision:         plan.TargetBaseRevision, preflightRevision: plan.PreflightRevision,
	}
	binding.bindingDigest = binding.digest()
	return binding, nil
}

func (binding recoveryTargetPreflightSessionBinding) digest() string {
	return framedDigest(
		recoveryTargetPreflightSessionBindingDomain,
		binding.planID, binding.planBindingDigest,
		strconv.FormatUint(binding.planTransitionRevision, 10), string(binding.targetMode),
		strconv.FormatUint(uint64(binding.nodeID), 10), binding.nodeRevision,
		binding.credentialRevision, binding.rootID, binding.rootLocator,
		binding.rootLocatorDigest, binding.rootRevision, binding.filesystemRevision,
		binding.targetPathDigest, binding.privateRelativeLocator,
		binding.targetRevision, binding.preflightRevision,
	)
}

func (binding recoveryTargetPreflightSessionBinding) valid() bool {
	if !validOpaqueID(binding.planID) || !validDigest(binding.planBindingDigest) ||
		binding.planTransitionRevision == 0 || binding.targetMode.Validate() != nil ||
		binding.nodeID == 0 || !validOpaqueRevision(binding.nodeRevision) ||
		!validOpaqueRevision(binding.credentialRevision) ||
		!validBoundedOpaque(binding.rootID, targetRootIDMax) ||
		!validDigest(binding.rootLocatorDigest) || !validOpaqueRevision(binding.rootRevision) ||
		!validOpaqueRevision(binding.filesystemRevision) || !validDigest(binding.targetPathDigest) ||
		!validTargetRelativeLocator(binding.privateRelativeLocator) ||
		!validOpaqueRevision(binding.targetRevision) || !validOpaqueRevision(binding.preflightRevision) ||
		!validDigest(binding.bindingDigest) || strings.HasPrefix(binding.rootLocator, "enc:v2:") {
		return false
	}
	locatorDigest, err := settings.RecoveryTargetRootLocatorDigest(
		binding.nodeID, binding.rootID, binding.rootLocator,
	)
	if err != nil || locatorDigest != binding.rootLocatorDigest {
		return false
	}
	pathDigest, err := TargetPathDigest(
		binding.rootID, binding.rootLocatorDigest, binding.privateRelativeLocator,
	)
	return err == nil && pathDigest == binding.targetPathDigest &&
		binding.bindingDigest == binding.digest()
}

func newRecoveryTargetSessionBinding(
	plan model.BackupAssetRecoveryPlan,
) (recoveryTargetSessionBinding, error) {
	if !validOpaqueID(plan.ID) || PlanState(plan.State) != PlanStateExecuted ||
		!validDigest(plan.BindingDigest) || plan.TargetNodeID == 0 ||
		!validOpaqueRevision(plan.TargetBaseRevision) ||
		!validOpaqueRevision(plan.CredentialScopeRevision) ||
		!validBoundedOpaque(plan.TargetRootID, targetRootIDMax) ||
		!validDigest(plan.RootLocatorDigest) || !validOpaqueRevision(plan.RootRevision) ||
		strings.HasPrefix(plan.EncryptedTargetRootLocator, "enc:v2:") {
		return recoveryTargetSessionBinding{}, ErrInvalidTargetPermit
	}
	locatorDigest, err := settings.RecoveryTargetRootLocatorDigest(
		plan.TargetNodeID, plan.TargetRootID, plan.EncryptedTargetRootLocator,
	)
	if err != nil || locatorDigest != plan.RootLocatorDigest {
		return recoveryTargetSessionBinding{}, ErrInvalidTargetPermit
	}
	binding := recoveryTargetSessionBinding{
		PlanID: plan.ID, PlanBindingDigest: plan.BindingDigest,
		NodeID: plan.TargetNodeID, NodeRevision: plan.TargetBaseRevision,
		CredentialRevision: plan.CredentialScopeRevision,
		RootID:             plan.TargetRootID, RootLocator: plan.EncryptedTargetRootLocator,
		RootLocatorDigest: plan.RootLocatorDigest, RootRevision: plan.RootRevision,
	}
	binding.bindingDigest = binding.digest()
	return binding, nil
}

func (binding recoveryTargetSessionBinding) digest() string {
	return framedDigest(
		recoveryTargetSessionBindingDomain,
		binding.PlanID, binding.PlanBindingDigest,
		strconv.FormatUint(uint64(binding.NodeID), 10), binding.NodeRevision,
		binding.CredentialRevision, binding.RootID, binding.RootLocator,
		binding.RootLocatorDigest, binding.RootRevision,
	)
}

func (binding recoveryTargetSessionBinding) valid() bool {
	if !validOpaqueID(binding.PlanID) || !validDigest(binding.PlanBindingDigest) ||
		binding.NodeID == 0 || !validOpaqueRevision(binding.NodeRevision) ||
		!validOpaqueRevision(binding.CredentialRevision) ||
		!validBoundedOpaque(binding.RootID, targetRootIDMax) ||
		!validDigest(binding.RootLocatorDigest) || !validOpaqueRevision(binding.RootRevision) ||
		!validDigest(binding.bindingDigest) || strings.HasPrefix(binding.RootLocator, "enc:v2:") {
		return false
	}
	locatorDigest, err := settings.RecoveryTargetRootLocatorDigest(
		binding.NodeID, binding.RootID, binding.RootLocator,
	)
	return err == nil && locatorDigest == binding.RootLocatorDigest &&
		binding.bindingDigest == binding.digest()
}

type recoveryTargetNodeSession struct {
	Node               model.Node
	NodeRevision       string
	CredentialRevision string
}

type recoveryTargetNodeSessionResolver interface {
	ResolveRecoveryTargetNodeSession(
		context.Context,
		uint,
		TargetPurpose,
	) (recoveryTargetNodeSession, error)
}

type recoveryTargetNodeDialer interface {
	Dial(
		context.Context,
		model.Node,
		string,
		sshutil.DialAuditContext,
	) (*ssh.Client, error)
}

type recoveryTargetSFTPFile interface {
	Read([]byte) (int, error)
	Write([]byte) (int, error)
	ReadDir(int) ([]os.FileInfo, error)
	Stat() (os.FileInfo, error)
	Sync() error
	Close() error
}

type recoveryTargetSFTPClient interface {
	RealPath(string) (string, error)
	Lstat(string) (os.FileInfo, error)
	ReadLink(string) (string, error)
	Stat(string) (os.FileInfo, error)
	StatVFS(string) (*sftp.StatVFS, error)
	Mkdir(string) error
	Chmod(string, os.FileMode) error
	Open(string) (recoveryTargetSFTPFile, error)
	OpenFile(string, int) (recoveryTargetSFTPFile, error)
	Rename(string, string) error
	Remove(string) error
	RemoveDirectory(string) error
	Close() error
}

type recoverySFTPFileHandle interface {
	Read([]byte) (int, error)
	Write([]byte) (int, error)
	Stat() (os.FileInfo, error)
	Sync() error
	Close() error
}

type recoverySFTPDirectoryReader interface {
	ReadDir(int) ([]os.FileInfo, error)
	Close() error
}

type recoverySFTPDirectoryOpener func(string) (recoverySFTPDirectoryReader, error)

type recoverySFTPDirectorySession interface {
	OpenDirectory(string) (recoverySFTPDirectoryReader, error)
	Close() error
}

type recoverySFTPDirectorySessionOpener func() (recoverySFTPDirectorySession, error)

type recoverySSHChannelOpener interface {
	OpenChannel(string, []byte) (ssh.Channel, <-chan *ssh.Request, error)
}

type recoverySFTPClient struct {
	client               *sftp.Client
	directoryMu          sync.Mutex
	directorySession     recoverySFTPDirectorySession
	openDirectorySession recoverySFTPDirectorySessionOpener
}

type recoverySFTPFile struct {
	file          recoverySFTPFileHandle
	path          string
	openDirectory recoverySFTPDirectoryOpener
	directory     recoverySFTPDirectoryReader
	closeOnce     sync.Once
	closeErr      error
}

const (
	recoverySFTPAttributeSize       = uint32(0x00000001)
	recoverySFTPAttributeUIDGID     = uint32(0x00000002)
	recoverySFTPAttributePermission = uint32(0x00000004)
	recoverySFTPAttributeTimes      = uint32(0x00000008)
	recoverySFTPAttributeExtended   = uint32(0x80000000)

	recoverySFTPPacketInit    = byte(1)
	recoverySFTPPacketVersion = byte(2)
	recoverySFTPPacketClose   = byte(4)
	recoverySFTPPacketOpenDir = byte(11)
	recoverySFTPPacketReadDir = byte(12)
	recoverySFTPPacketStatus  = byte(101)
	recoverySFTPPacketHandle  = byte(102)
	recoverySFTPPacketName    = byte(104)

	recoverySFTPStatusOK                = uint32(0)
	recoverySFTPStatusEOF               = uint32(1)
	recoverySFTPProtocolVersion         = uint32(3)
	recoverySFTPDirectoryPacketMaxBytes = uint32(256 << 10)
)

type recoverySFTPWireReader struct {
	value  []byte
	offset int
}

func (reader *recoverySFTPWireReader) readUint32() (uint32, bool) {
	if reader == nil || len(reader.value)-reader.offset < 4 {
		return 0, false
	}
	value := binary.BigEndian.Uint32(reader.value[reader.offset : reader.offset+4])
	reader.offset += 4
	return value, true
}

func (reader *recoverySFTPWireReader) readUint64() (uint64, bool) {
	if reader == nil || len(reader.value)-reader.offset < 8 {
		return 0, false
	}
	value := binary.BigEndian.Uint64(reader.value[reader.offset : reader.offset+8])
	reader.offset += 8
	return value, true
}

func (reader *recoverySFTPWireReader) readString() (string, bool) {
	length, ok := reader.readUint32()
	if !ok || uint64(length) > uint64(len(reader.value)-reader.offset) {
		return "", false
	}
	value := string(reader.value[reader.offset : reader.offset+int(length)])
	reader.offset += int(length)
	return value, true
}

func (reader *recoverySFTPWireReader) done() bool {
	return reader != nil && reader.offset == len(reader.value)
}

type recoverySFTPDirectoryFileInfo struct {
	name string
	stat sftp.FileStat
}

func (info recoverySFTPDirectoryFileInfo) Name() string       { return info.name }
func (info recoverySFTPDirectoryFileInfo) Size() int64        { return int64(info.stat.Size) }
func (info recoverySFTPDirectoryFileInfo) Mode() os.FileMode  { return info.stat.FileMode() }
func (info recoverySFTPDirectoryFileInfo) ModTime() time.Time { return info.stat.ModTime() }
func (info recoverySFTPDirectoryFileInfo) IsDir() bool        { return info.Mode().IsDir() }
func (info recoverySFTPDirectoryFileInfo) Sys() any           { return &info.stat }

func decodeRecoverySFTPAttributes(reader *recoverySFTPWireReader) (sftp.FileStat, bool) {
	flags, ok := reader.readUint32()
	if !ok || flags&^(recoverySFTPAttributeSize|recoverySFTPAttributeUIDGID|
		recoverySFTPAttributePermission|recoverySFTPAttributeTimes|recoverySFTPAttributeExtended) != 0 {
		return sftp.FileStat{}, false
	}
	stat := sftp.FileStat{}
	if flags&recoverySFTPAttributeSize != 0 {
		stat.Size, ok = reader.readUint64()
		if !ok || stat.Size > math.MaxInt64 {
			return sftp.FileStat{}, false
		}
	}
	if flags&recoverySFTPAttributeUIDGID != 0 {
		stat.UID, ok = reader.readUint32()
		if !ok {
			return sftp.FileStat{}, false
		}
		stat.GID, ok = reader.readUint32()
		if !ok {
			return sftp.FileStat{}, false
		}
	}
	if flags&recoverySFTPAttributePermission != 0 {
		stat.Mode, ok = reader.readUint32()
		if !ok {
			return sftp.FileStat{}, false
		}
	}
	if flags&recoverySFTPAttributeTimes != 0 {
		stat.Atime, ok = reader.readUint32()
		if !ok {
			return sftp.FileStat{}, false
		}
		stat.Mtime, ok = reader.readUint32()
		if !ok {
			return sftp.FileStat{}, false
		}
	}
	if flags&recoverySFTPAttributeExtended != 0 {
		count, countOK := reader.readUint32()
		if !countOK {
			return sftp.FileStat{}, false
		}
		for index := uint32(0); index < count; index++ {
			if _, ok = reader.readString(); !ok {
				return sftp.FileStat{}, false
			}
			if _, ok = reader.readString(); !ok {
				return sftp.FileStat{}, false
			}
		}
	}
	return stat, true
}

func decodeRecoverySFTPDirectoryNamePacket(
	payload []byte,
	expectedID uint32,
) ([]os.FileInfo, bool, error) {
	reader := &recoverySFTPWireReader{value: payload}
	responseID, ok := reader.readUint32()
	if !ok || responseID != expectedID {
		return nil, false, ErrRecoveryTargetUnavailable
	}
	count, ok := reader.readUint32()
	if !ok {
		return nil, false, ErrRecoveryTargetUnavailable
	}
	entries := make([]os.FileInfo, 0, min(int(count), recoveryCleanupRemoveLimit+1))
	truncated := false
	for index := uint32(0); index < count; index++ {
		name, nameOK := reader.readString()
		if !nameOK {
			return nil, false, ErrRecoveryTargetUnavailable
		}
		if _, longNameOK := reader.readString(); !longNameOK {
			return nil, false, ErrRecoveryTargetUnavailable
		}
		stat, statOK := decodeRecoverySFTPAttributes(reader)
		if !statOK {
			return nil, false, ErrRecoveryTargetUnavailable
		}
		if name == "." || name == ".." {
			continue
		}
		if len(entries) >= recoveryCleanupRemoveLimit+1 {
			truncated = true
			continue
		}
		entries = append(entries, recoverySFTPDirectoryFileInfo{name: name, stat: stat})
	}
	if !reader.done() {
		return nil, false, ErrRecoveryTargetUnavailable
	}
	return entries, truncated, nil
}

type recoverySFTPDirectoryTransport interface {
	io.Reader
	io.Writer
	io.Closer
}

type recoveryBoundedSFTPDirectorySession struct {
	requestMu  sync.Mutex
	stateMu    sync.Mutex
	transport  recoverySFTPDirectoryTransport
	nextID     uint32
	wireBroken bool
	closed     bool
	closeOnce  sync.Once
	closeErr   error
}

type recoveryBoundedSFTPDirectory struct {
	session     *recoveryBoundedSFTPDirectorySession
	handle      string
	pending     []os.FileInfo
	truncated   bool
	eof         bool
	ownsSession bool
	closeOnce   sync.Once
	closeErr    error
}

func appendRecoverySFTPUint32(value []byte, field uint32) []byte {
	return binary.BigEndian.AppendUint32(value, field)
}

func appendRecoverySFTPString(value []byte, field string) []byte {
	value = appendRecoverySFTPUint32(value, uint32(len(field)))
	return append(value, field...)
}

func writeRecoverySFTPPacket(
	transport recoverySFTPDirectoryTransport,
	packetType byte,
	payload []byte,
) error {
	if transport == nil || uint64(len(payload))+1 > uint64(recoverySFTPDirectoryPacketMaxBytes) {
		return ErrRecoveryTargetUnavailable
	}
	packet := make([]byte, 0, len(payload)+5)
	packet = appendRecoverySFTPUint32(packet, uint32(len(payload)+1))
	packet = append(packet, packetType)
	packet = append(packet, payload...)
	for len(packet) > 0 {
		written, err := transport.Write(packet)
		if written > 0 {
			packet = packet[written:]
		}
		if err != nil || written <= 0 {
			return ErrRecoveryTargetUnavailable
		}
	}
	return nil
}

func readRecoverySFTPPacket(
	transport recoverySFTPDirectoryTransport,
) (byte, []byte, error) {
	if transport == nil {
		return 0, nil, ErrRecoveryTargetUnavailable
	}
	var header [4]byte
	if _, err := io.ReadFull(transport, header[:]); err != nil {
		return 0, nil, ErrRecoveryTargetUnavailable
	}
	length := binary.BigEndian.Uint32(header[:])
	if length < 1 || length > recoverySFTPDirectoryPacketMaxBytes {
		return 0, nil, ErrRecoveryTargetUnavailable
	}
	packet := make([]byte, int(length))
	if _, err := io.ReadFull(transport, packet); err != nil {
		return 0, nil, ErrRecoveryTargetUnavailable
	}
	return packet[0], packet[1:], nil
}

func decodeRecoverySFTPVersion(payload []byte) error {
	reader := &recoverySFTPWireReader{value: payload}
	version, ok := reader.readUint32()
	if !ok || version != recoverySFTPProtocolVersion {
		return ErrRecoveryTargetUnavailable
	}
	for !reader.done() {
		if _, ok := reader.readString(); !ok {
			return ErrRecoveryTargetUnavailable
		}
		if _, ok := reader.readString(); !ok {
			return ErrRecoveryTargetUnavailable
		}
	}
	return nil
}

func decodeRecoverySFTPHandle(payload []byte, expectedID uint32) (string, error) {
	reader := &recoverySFTPWireReader{value: payload}
	responseID, ok := reader.readUint32()
	if !ok || responseID != expectedID {
		return "", ErrRecoveryTargetUnavailable
	}
	handle, ok := reader.readString()
	if !ok || handle == "" || !reader.done() {
		return "", ErrRecoveryTargetUnavailable
	}
	return handle, nil
}

func decodeRecoverySFTPStatus(payload []byte, expectedID uint32) (uint32, error) {
	reader := &recoverySFTPWireReader{value: payload}
	responseID, ok := reader.readUint32()
	if !ok || responseID != expectedID {
		return 0, ErrRecoveryTargetUnavailable
	}
	status, ok := reader.readUint32()
	if !ok {
		return 0, ErrRecoveryTargetUnavailable
	}
	if _, ok := reader.readString(); !ok {
		return 0, ErrRecoveryTargetUnavailable
	}
	if _, ok := reader.readString(); !ok || !reader.done() {
		return 0, ErrRecoveryTargetUnavailable
	}
	return status, nil
}

func newRecoveryBoundedSFTPDirectorySession(
	transport recoverySFTPDirectoryTransport,
) (*recoveryBoundedSFTPDirectorySession, error) {
	if transport == nil {
		return nil, ErrRecoveryTargetUnavailable
	}
	session := &recoveryBoundedSFTPDirectorySession{transport: transport, nextID: 1}
	if err := writeRecoverySFTPPacket(
		transport, recoverySFTPPacketInit, appendRecoverySFTPUint32(nil, recoverySFTPProtocolVersion),
	); err != nil {
		_ = transport.Close()
		return nil, err
	}
	packetType, payload, err := readRecoverySFTPPacket(transport)
	if err != nil || packetType != recoverySFTPPacketVersion || decodeRecoverySFTPVersion(payload) != nil {
		_ = transport.Close()
		return nil, ErrRecoveryTargetUnavailable
	}
	return session, nil
}

func (session *recoveryBoundedSFTPDirectorySession) activeTransport() (
	recoverySFTPDirectoryTransport,
	error,
) {
	if session == nil {
		return nil, ErrRecoveryTargetUnavailable
	}
	session.stateMu.Lock()
	defer session.stateMu.Unlock()
	if session.closed || session.wireBroken || session.transport == nil {
		return nil, ErrRecoveryTargetUnavailable
	}
	return session.transport, nil
}

func (session *recoveryBoundedSFTPDirectorySession) markWireBroken() {
	if session == nil {
		return
	}
	session.stateMu.Lock()
	session.wireBroken = true
	session.stateMu.Unlock()
}

func (session *recoveryBoundedSFTPDirectorySession) nextRequestIDLocked() (uint32, error) {
	if session == nil || session.nextID == 0 || session.nextID == math.MaxUint32 {
		return 0, ErrRecoveryTargetUnavailable
	}
	requestID := session.nextID
	session.nextID++
	return requestID, nil
}

func (session *recoveryBoundedSFTPDirectorySession) OpenDirectory(
	directoryPath string,
) (recoverySFTPDirectoryReader, error) {
	if session == nil || !path.IsAbs(directoryPath) || path.Clean(directoryPath) != directoryPath {
		return nil, ErrRecoveryTargetUnavailable
	}
	session.requestMu.Lock()
	defer session.requestMu.Unlock()
	transport, err := session.activeTransport()
	if err != nil {
		return nil, err
	}
	requestID, err := session.nextRequestIDLocked()
	if err != nil {
		return nil, err
	}
	request := appendRecoverySFTPUint32(nil, requestID)
	request = appendRecoverySFTPString(request, directoryPath)
	if err := writeRecoverySFTPPacket(transport, recoverySFTPPacketOpenDir, request); err != nil {
		session.markWireBroken()
		return nil, ErrRecoveryTargetUnavailable
	}
	packetType, payload, err := readRecoverySFTPPacket(transport)
	if err != nil || packetType != recoverySFTPPacketHandle {
		session.markWireBroken()
		return nil, ErrRecoveryTargetUnavailable
	}
	handle, err := decodeRecoverySFTPHandle(payload, requestID)
	if err != nil {
		session.markWireBroken()
		return nil, ErrRecoveryTargetUnavailable
	}
	return &recoveryBoundedSFTPDirectory{session: session, handle: handle}, nil
}

func (session *recoveryBoundedSFTPDirectorySession) readDirectory(
	handle string,
) (byte, []byte, uint32, error) {
	if session == nil || handle == "" {
		return 0, nil, 0, ErrRecoveryTargetUnavailable
	}
	session.requestMu.Lock()
	defer session.requestMu.Unlock()
	transport, err := session.activeTransport()
	if err != nil {
		return 0, nil, 0, err
	}
	requestID, err := session.nextRequestIDLocked()
	if err != nil {
		return 0, nil, 0, err
	}
	request := appendRecoverySFTPUint32(nil, requestID)
	request = appendRecoverySFTPString(request, handle)
	if err := writeRecoverySFTPPacket(transport, recoverySFTPPacketReadDir, request); err != nil {
		session.markWireBroken()
		return 0, nil, 0, ErrRecoveryTargetUnavailable
	}
	packetType, payload, err := readRecoverySFTPPacket(transport)
	if err != nil {
		session.markWireBroken()
		return 0, nil, 0, ErrRecoveryTargetUnavailable
	}
	return packetType, payload, requestID, nil
}

func (session *recoveryBoundedSFTPDirectorySession) closeDirectory(handle string) error {
	if session == nil || handle == "" {
		return ErrRecoveryTargetUnavailable
	}
	session.requestMu.Lock()
	defer session.requestMu.Unlock()
	transport, err := session.activeTransport()
	if err != nil {
		return err
	}
	requestID, err := session.nextRequestIDLocked()
	if err != nil {
		return err
	}
	request := appendRecoverySFTPUint32(nil, requestID)
	request = appendRecoverySFTPString(request, handle)
	if err := writeRecoverySFTPPacket(transport, recoverySFTPPacketClose, request); err != nil {
		session.markWireBroken()
		return ErrRecoveryTargetUnavailable
	}
	packetType, payload, err := readRecoverySFTPPacket(transport)
	if err != nil || packetType != recoverySFTPPacketStatus {
		session.markWireBroken()
		return ErrRecoveryTargetUnavailable
	}
	status, err := decodeRecoverySFTPStatus(payload, requestID)
	if err != nil || status != recoverySFTPStatusOK {
		session.markWireBroken()
		return ErrRecoveryTargetUnavailable
	}
	return nil
}

func (session *recoveryBoundedSFTPDirectorySession) Close() error {
	if session == nil {
		return ErrRecoveryTargetUnavailable
	}
	session.closeOnce.Do(func() {
		session.stateMu.Lock()
		transport := session.transport
		wireBroken := session.wireBroken
		session.closed = true
		session.transport = nil
		session.stateMu.Unlock()
		if wireBroken {
			session.closeErr = ErrRecoveryTargetUnavailable
		}
		if transport == nil {
			if session.closeErr == nil {
				session.closeErr = ErrRecoveryTargetUnavailable
			}
			return
		}
		if err := transport.Close(); err != nil && session.closeErr == nil {
			session.closeErr = ErrRecoveryTargetUnavailable
		}
	})
	return session.closeErr
}

func newRecoveryBoundedSFTPDirectory(
	transport recoverySFTPDirectoryTransport,
	directoryPath string,
) (*recoveryBoundedSFTPDirectory, error) {
	session, err := newRecoveryBoundedSFTPDirectorySession(transport)
	if err != nil {
		return nil, err
	}
	reader, err := session.OpenDirectory(directoryPath)
	if err != nil {
		_ = session.Close()
		return nil, err
	}
	directory, ok := reader.(*recoveryBoundedSFTPDirectory)
	if !ok {
		_ = reader.Close()
		_ = session.Close()
		return nil, ErrRecoveryTargetUnavailable
	}
	directory.ownsSession = true
	return directory, nil
}

func (directory *recoveryBoundedSFTPDirectory) ReadDir(n int) ([]os.FileInfo, error) {
	if directory == nil || directory.session == nil || directory.handle == "" ||
		n <= 0 || n > recoveryCleanupReadBatch {
		return nil, ErrRecoveryTargetUnavailable
	}
	if len(directory.pending) > 0 {
		count := min(n, len(directory.pending))
		entries := append([]os.FileInfo(nil), directory.pending[:count]...)
		directory.pending = directory.pending[count:]
		return entries, nil
	}
	if directory.truncated {
		return nil, ErrRecoveryTargetUnavailable
	}
	if directory.eof {
		return nil, io.EOF
	}
	packetType, payload, requestID, err := directory.session.readDirectory(directory.handle)
	if err != nil {
		return nil, err
	}
	switch packetType {
	case recoverySFTPPacketName:
		directory.pending, directory.truncated, err = decodeRecoverySFTPDirectoryNamePacket(payload, requestID)
		if err != nil || len(directory.pending) == 0 {
			directory.session.markWireBroken()
			return nil, ErrRecoveryTargetUnavailable
		}
		return directory.ReadDir(n)
	case recoverySFTPPacketStatus:
		status, statusErr := decodeRecoverySFTPStatus(payload, requestID)
		if statusErr != nil {
			directory.session.markWireBroken()
			return nil, statusErr
		}
		if status == recoverySFTPStatusEOF {
			directory.eof = true
			return nil, io.EOF
		}
		directory.session.markWireBroken()
		return nil, ErrRecoveryTargetUnavailable
	default:
		directory.session.markWireBroken()
		return nil, ErrRecoveryTargetUnavailable
	}
}

func (directory *recoveryBoundedSFTPDirectory) Close() error {
	if directory == nil {
		return ErrRecoveryTargetUnavailable
	}
	directory.closeOnce.Do(func() {
		if directory.session == nil || directory.handle == "" {
			directory.closeErr = ErrRecoveryTargetUnavailable
			return
		}
		if err := directory.session.closeDirectory(directory.handle); err != nil {
			directory.closeErr = ErrRecoveryTargetUnavailable
		}
		directory.handle = ""
		if directory.ownsSession {
			if err := directory.session.Close(); err != nil && directory.closeErr == nil {
				directory.closeErr = ErrRecoveryTargetUnavailable
			}
		}
	})
	return directory.closeErr
}

func openRecoveryBoundedSFTPDirectorySession(
	client recoverySSHChannelOpener,
) (recoverySFTPDirectorySession, error) {
	if client == nil {
		return nil, ErrRecoveryTargetUnavailable
	}
	channel, requests, err := client.OpenChannel("session", nil)
	if err != nil {
		return nil, ErrRecoveryTargetUnavailable
	}
	go ssh.DiscardRequests(requests)
	subsystemPayload := ssh.Marshal(struct{ Name string }{Name: "sftp"})
	accepted, err := channel.SendRequest("subsystem", true, subsystemPayload)
	if err != nil || !accepted {
		_ = channel.Close()
		return nil, ErrRecoveryTargetUnavailable
	}
	session, err := newRecoveryBoundedSFTPDirectorySession(channel)
	if err != nil {
		return nil, ErrRecoveryTargetUnavailable
	}
	return session, nil
}

func (file *recoverySFTPFile) Read(value []byte) (int, error) {
	if file == nil || file.file == nil {
		return 0, ErrRecoveryTargetUnavailable
	}
	return file.file.Read(value)
}

func (file *recoverySFTPFile) Write(value []byte) (int, error) {
	if file == nil || file.file == nil {
		return 0, ErrRecoveryTargetUnavailable
	}
	return file.file.Write(value)
}

func (file *recoverySFTPFile) ReadDir(n int) ([]os.FileInfo, error) {
	if file == nil || file.file == nil || file.path == "" || file.openDirectory == nil ||
		n <= 0 || n > recoveryCleanupReadBatch {
		return nil, ErrRecoveryTargetUnavailable
	}
	if file.directory == nil {
		directory, err := file.openDirectory(file.path)
		if err != nil || directory == nil {
			if directory != nil {
				_ = directory.Close()
			}
			return nil, ErrRecoveryTargetUnavailable
		}
		file.directory = directory
	}
	return file.directory.ReadDir(n)
}

func (file *recoverySFTPFile) Stat() (os.FileInfo, error) {
	if file == nil || file.file == nil {
		return nil, ErrRecoveryTargetUnavailable
	}
	return file.file.Stat()
}

func (file *recoverySFTPFile) Sync() error {
	if file == nil || file.file == nil {
		return ErrRecoveryTargetUnavailable
	}
	return file.file.Sync()
}

func (file *recoverySFTPFile) Close() error {
	if file == nil {
		return ErrRecoveryTargetUnavailable
	}
	file.closeOnce.Do(func() {
		if file.directory != nil {
			if err := file.directory.Close(); err != nil {
				file.closeErr = ErrRecoveryTargetUnavailable
			}
			file.directory = nil
		}
		if file.file == nil {
			if file.closeErr == nil {
				file.closeErr = ErrRecoveryTargetUnavailable
			}
			return
		}
		if err := file.file.Close(); err != nil && file.closeErr == nil {
			file.closeErr = ErrRecoveryTargetUnavailable
		}
		file.file = nil
	})
	return file.closeErr
}

func (client *recoverySFTPClient) RealPath(value string) (string, error) {
	return client.client.RealPath(value)
}

func (client *recoverySFTPClient) Lstat(value string) (os.FileInfo, error) {
	return client.client.Lstat(value)
}

func (client *recoverySFTPClient) ReadLink(value string) (string, error) {
	return client.client.ReadLink(value)
}

func (client *recoverySFTPClient) Stat(value string) (os.FileInfo, error) {
	return client.client.Stat(value)
}

func (client *recoverySFTPClient) StatVFS(value string) (*sftp.StatVFS, error) {
	return client.client.StatVFS(value)
}

func (client *recoverySFTPClient) Mkdir(value string) error {
	return client.client.Mkdir(value)
}

func (client *recoverySFTPClient) Chmod(value string, mode os.FileMode) error {
	return client.client.Chmod(value, mode)
}

func (client *recoverySFTPClient) openBoundedDirectory(
	value string,
) (recoverySFTPDirectoryReader, error) {
	if client == nil || client.openDirectorySession == nil {
		return nil, ErrRecoveryTargetUnavailable
	}
	client.directoryMu.Lock()
	if client.directorySession == nil {
		session, err := client.openDirectorySession()
		if err != nil || session == nil {
			client.directoryMu.Unlock()
			if session != nil {
				_ = session.Close()
			}
			return nil, ErrRecoveryTargetUnavailable
		}
		client.directorySession = session
	}
	session := client.directorySession
	client.directoryMu.Unlock()
	return session.OpenDirectory(value)
}

func (client *recoverySFTPClient) Open(value string) (recoveryTargetSFTPFile, error) {
	file, err := client.client.Open(value)
	if err != nil {
		return nil, err
	}
	return &recoverySFTPFile{file: file, path: value, openDirectory: client.openBoundedDirectory}, nil
}

func (client *recoverySFTPClient) OpenFile(value string, flag int) (recoveryTargetSFTPFile, error) {
	file, err := client.client.OpenFile(value, flag)
	if err != nil {
		return nil, err
	}
	return &recoverySFTPFile{file: file}, nil
}

func (client *recoverySFTPClient) Rename(oldName, newName string) error {
	return client.client.Rename(oldName, newName)
}

func (client *recoverySFTPClient) Remove(value string) error {
	return client.client.Remove(value)
}

func (client *recoverySFTPClient) RemoveDirectory(value string) error {
	return client.client.RemoveDirectory(value)
}

func (client *recoverySFTPClient) Close() error {
	if client == nil || client.client == nil {
		return ErrRecoveryTargetUnavailable
	}
	client.directoryMu.Lock()
	directorySession := client.directorySession
	client.directorySession = nil
	client.directoryMu.Unlock()
	var closeErr error
	if directorySession != nil {
		closeErr = directorySession.Close()
	}
	if err := client.client.Close(); err != nil && closeErr == nil {
		closeErr = err
	}
	return closeErr
}

type recoveryTargetSFTPOpener func(*ssh.Client) (recoveryTargetSFTPClient, error)
type recoveryTargetSSHCloser func(*ssh.Client) error

type recoveryTargetSessionFactory struct {
	resolver          recoveryTargetNodeSessionResolver
	dialer            recoveryTargetNodeDialer
	openSFTP          recoveryTargetSFTPOpener
	openCommandRunner func(*ssh.Client) *sshutil.CommandRunner
	closeSSH          recoveryTargetSSHCloser
}

func newRecoveryTargetSessionFactory(
	resolver recoveryTargetNodeSessionResolver,
	dialer *sshutil.NodeDialer,
) *recoveryTargetSessionFactory {
	return newRecoveryTargetSessionFactoryForTest(
		resolver,
		dialer,
		func(client *ssh.Client) (recoveryTargetSFTPClient, error) {
			sftpClient, err := sftp.NewClient(client)
			if err != nil {
				return nil, err
			}
			return &recoverySFTPClient{
				client: sftpClient,
				openDirectorySession: func() (recoverySFTPDirectorySession, error) {
					return openRecoveryBoundedSFTPDirectorySession(client)
				},
			}, nil
		},
		func(client *ssh.Client) error {
			if client == nil {
				return ErrRecoveryTargetUnavailable
			}
			return client.Close()
		},
	)
}

func newRecoveryTargetSessionFactoryForTest(
	resolver recoveryTargetNodeSessionResolver,
	dialer recoveryTargetNodeDialer,
	openSFTP recoveryTargetSFTPOpener,
	closeSSH recoveryTargetSSHCloser,
) *recoveryTargetSessionFactory {
	return &recoveryTargetSessionFactory{
		resolver: resolver, dialer: dialer, openSFTP: openSFTP,
		openCommandRunner: func(client *ssh.Client) *sshutil.CommandRunner {
			return sshutil.NewSSHCommandRunner(client, 1)
		},
		closeSSH: closeSSH,
	}
}

type recoveryTargetSession struct {
	mu            sync.Mutex
	client        recoveryTargetSFTPClient
	sshClient     *ssh.Client
	commandRunner *sshutil.CommandRunner
	closeSSH      recoveryTargetSSHCloser
	trackedFiles  map[*recoveryResultTrackedSFTPFile]struct{}
	closed        bool
	resourcesOnce sync.Once
	stopOnce      sync.Once
	watchStop     chan struct{}
	watchDone     chan struct{}
	closeErr      error
}

func (factory *recoveryTargetSessionFactory) Open(
	ctx context.Context,
	binding recoveryTargetSessionBinding,
	purpose TargetPurpose,
	jobID string,
) (*recoveryTargetSession, error) {
	ctx = recoveryWorkspaceMarkerContext(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if factory == nil || factory.resolver == nil || factory.dialer == nil ||
		factory.openSFTP == nil || factory.closeSSH == nil || !binding.valid() ||
		(purpose != TargetPurposeWrite && purpose != TargetPurposeVerify &&
			purpose != TargetPurposeResultRead && purpose != TargetPurposeCleanup) ||
		!validOpaqueID(jobID) {
		return nil, ErrInvalidTargetPermit
	}
	resolved, err := factory.resolver.ResolveRecoveryTargetNodeSession(ctx, binding.NodeID, purpose)
	if err != nil {
		return nil, recoveryTargetUnavailableForContext(ctx)
	}
	if resolved.Node.ID != binding.NodeID || resolved.Node.Archived ||
		resolved.NodeRevision != binding.NodeRevision ||
		resolved.CredentialRevision != binding.CredentialRevision {
		return nil, ErrInvalidTargetPermit
	}
	return factory.openResolved(ctx, resolved, purpose, jobID)
}

func (factory *recoveryTargetSessionFactory) OpenPreflight(
	ctx context.Context,
	binding recoveryTargetPreflightSessionBinding,
) (*recoveryTargetSession, error) {
	ctx = recoveryWorkspaceMarkerContext(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if factory == nil || factory.resolver == nil || factory.dialer == nil ||
		factory.openSFTP == nil || factory.openCommandRunner == nil ||
		factory.closeSSH == nil || !binding.valid() {
		return nil, ErrInvalidTargetPermit
	}
	resolved, err := factory.resolver.ResolveRecoveryTargetNodeSession(
		ctx, binding.nodeID, TargetPurposePreflight,
	)
	if err != nil {
		return nil, recoveryTargetOperationError(ctx, err)
	}
	if resolved.Node.ID != binding.nodeID || resolved.Node.Archived ||
		resolved.NodeRevision != binding.nodeRevision ||
		resolved.CredentialRevision != binding.credentialRevision {
		return nil, ErrInvalidTargetPermit
	}
	session, err := factory.openResolved(ctx, resolved, TargetPurposePreflight, binding.planID)
	if err != nil {
		return nil, err
	}
	session.commandRunner = factory.openCommandRunner(session.sshClient)
	if session.commandRunner == nil {
		_ = session.Close()
		return nil, recoveryTargetUnavailableForContext(ctx)
	}
	return session, nil
}

func (factory *recoveryTargetSessionFactory) OpenReconciliation(
	ctx context.Context,
	binding recoveryTargetReconciliationSessionBinding,
) (*recoveryTargetSession, error) {
	ctx = recoveryWorkspaceMarkerContext(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if factory == nil || factory.resolver == nil || factory.dialer == nil ||
		factory.openSFTP == nil || factory.closeSSH == nil || !binding.valid() {
		return nil, ErrInvalidTargetPermit
	}
	resolved, err := factory.resolver.ResolveRecoveryTargetNodeSession(
		ctx, binding.nodeID, TargetPurposeReconcile,
	)
	if err != nil {
		return nil, recoveryTargetOperationError(ctx, err)
	}
	if resolved.Node.ID != binding.nodeID || resolved.Node.Archived ||
		resolved.NodeRevision != binding.nodeRevision ||
		resolved.CredentialRevision != binding.credentialRevision {
		return nil, ErrInvalidTargetPermit
	}
	return factory.openResolved(
		ctx, resolved, TargetPurposeReconcile, binding.auditCorrelationID(),
	)
}

func (factory *recoveryTargetSessionFactory) openResolved(
	ctx context.Context,
	resolved recoveryTargetNodeSession,
	purpose TargetPurpose,
	correlationID string,
) (*recoveryTargetSession, error) {
	sshClient, err := factory.dialer.Dial(
		ctx, resolved.Node, string(purpose), sshutil.DialAuditContext{CorrelationID: correlationID},
	)
	if err != nil {
		if sshClient != nil {
			_ = factory.closeSSH(sshClient)
		}
		return nil, recoveryTargetOperationError(ctx, err)
	}
	session := &recoveryTargetSession{
		sshClient: sshClient, closeSSH: factory.closeSSH,
		watchStop: make(chan struct{}), watchDone: make(chan struct{}),
	}
	session.watch(ctx)
	sftpClient, err := factory.openSFTP(sshClient)
	if err != nil {
		if sftpClient != nil {
			_ = sftpClient.Close()
		}
		_ = session.Close()
		return nil, recoveryTargetOperationError(ctx, err)
	}
	if !session.attachSFTP(sftpClient) {
		_ = session.Close()
		return nil, recoveryTargetUnavailableForContext(ctx)
	}
	if err := ctx.Err(); err != nil {
		_ = session.Close()
		return nil, err
	}
	return session, nil
}

func (session *recoveryTargetSession) watch(ctx context.Context) {
	go func() {
		defer close(session.watchDone)
		select {
		case <-ctx.Done():
			session.closeResources()
		case <-session.watchStop:
		}
	}()
}

func (session *recoveryTargetSession) attachSFTP(client recoveryTargetSFTPClient) bool {
	if client == nil {
		return false
	}
	session.mu.Lock()
	if session.closed {
		session.mu.Unlock()
		_ = client.Close()
		return false
	}
	session.client = client
	session.mu.Unlock()
	return true
}

func (session *recoveryTargetSession) closeResources() {
	session.resourcesOnce.Do(func() {
		session.mu.Lock()
		session.closed = true
		trackedFiles := make([]*recoveryResultTrackedSFTPFile, 0, len(session.trackedFiles))
		for file := range session.trackedFiles {
			trackedFiles = append(trackedFiles, file)
		}
		client := session.client
		session.mu.Unlock()
		for _, file := range trackedFiles {
			if err := file.Close(); session.closeErr == nil {
				session.closeErr = err
			}
		}
		if client != nil {
			if err := client.Close(); session.closeErr == nil {
				session.closeErr = err
			}
		}
		if session.closeSSH != nil {
			if err := session.closeSSH(session.sshClient); session.closeErr == nil {
				session.closeErr = err
			}
		}
	})
}

func (session *recoveryTargetSession) trackResultFile(
	file recoveryTargetSFTPFile,
) (*recoveryResultTrackedSFTPFile, error) {
	if session == nil || file == nil {
		return nil, ErrRecoveryTargetUnavailable
	}
	tracked := &recoveryResultTrackedSFTPFile{recoveryTargetSFTPFile: file, session: session}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closed {
		_ = file.Close()
		return nil, ErrRecoveryTargetUnavailable
	}
	if session.trackedFiles == nil {
		session.trackedFiles = make(map[*recoveryResultTrackedSFTPFile]struct{})
	}
	session.trackedFiles[tracked] = struct{}{}
	return tracked, nil
}

func (session *recoveryTargetSession) untrackResultFile(file *recoveryResultTrackedSFTPFile) {
	if session == nil || file == nil {
		return
	}
	session.mu.Lock()
	delete(session.trackedFiles, file)
	session.mu.Unlock()
}

type recoveryResultTrackedSFTPFile struct {
	recoveryTargetSFTPFile
	session   *recoveryTargetSession
	closeOnce sync.Once
	closeErr  error
}

func (file *recoveryResultTrackedSFTPFile) Close() error {
	if file == nil {
		return ErrRecoveryTargetUnavailable
	}
	file.closeOnce.Do(func() {
		file.closeErr = file.recoveryTargetSFTPFile.Close()
		file.session.untrackResultFile(file)
	})
	return file.closeErr
}

type recoveryResultTrackedSFTPClient struct {
	recoveryTargetSFTPClient
	session *recoveryTargetSession
}

func (client *recoveryResultTrackedSFTPClient) Open(value string) (recoveryTargetSFTPFile, error) {
	if client == nil || client.recoveryTargetSFTPClient == nil || client.session == nil {
		return nil, ErrRecoveryTargetUnavailable
	}
	file, err := client.recoveryTargetSFTPClient.Open(value)
	if file == nil {
		return nil, err
	}
	tracked, trackErr := client.session.trackResultFile(file)
	if trackErr != nil {
		return nil, trackErr
	}
	return tracked, err
}

func (session *recoveryTargetSession) Close() error {
	if session == nil {
		return ErrRecoveryTargetUnavailable
	}
	session.stopOnce.Do(func() { close(session.watchStop) })
	session.closeResources()
	<-session.watchDone
	return session.closeErr
}

func recoveryTargetUnavailableForContext(ctx context.Context) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return ErrRecoveryTargetUnavailable
}

type TargetPurpose string

const (
	TargetPurposePreflight  TargetPurpose = sshutil.PurposeRecoveryPreflight
	TargetPurposeWrite      TargetPurpose = sshutil.PurposeRecoveryWrite
	TargetPurposeVerify     TargetPurpose = sshutil.PurposeRecoveryVerify
	TargetPurposeResultRead TargetPurpose = sshutil.PurposeRecoveryResultRead
	TargetPurposeCleanup    TargetPurpose = sshutil.PurposeRecoveryCleanup
	TargetPurposeReconcile  TargetPurpose = sshutil.PurposeRecoveryReconcile
)

func (purpose TargetPurpose) valid() bool {
	switch purpose {
	case TargetPurposePreflight, TargetPurposeWrite, TargetPurposeVerify, TargetPurposeResultRead, TargetPurposeCleanup:
		return true
	default:
		return false
	}
}

type TargetObservationPermit struct {
	SchemaVersion     int
	NodeID            uint
	Purpose           TargetPurpose
	RootID            string
	RootLocatorDigest string `json:"-"`
	TargetPathDigest  string `json:"-"`
	RootRevision      string
	ExpiresAt         time.Time
	proof             *targetVerifyPermitProof     `json:"-"`
	resultReadProof   *targetResultReadPermitProof `json:"-"`
}

type targetVerifyPermitProof struct {
	sessionBinding recoveryTargetSessionBinding
	jobID          string
	targetMode     TargetMode
	operation      RecoveryOperationKind
	expectedPrior  ExpectedTargetIdentity
	bindingDigest  string
}

func (permit TargetObservationPermit) ValidateAt(now time.Time) error {
	if now.IsZero() || permit.SchemaVersion != 1 || permit.NodeID == 0 || !permit.Purpose.valid() ||
		!validBoundedOpaque(permit.RootID, targetRootIDMax) || !validDigest(permit.RootLocatorDigest) ||
		!validDigest(permit.TargetPathDigest) ||
		!validOpaqueRevision(permit.RootRevision) || !permit.ExpiresAt.After(now) {
		return ErrInvalidTargetPermit
	}
	return nil
}

func (permit TargetObservationPermit) ValidateObjectAt(now time.Time, object TargetObjectRef) error {
	if permit.ValidateAt(now) != nil || !permit.matchesObject(object) {
		return ErrInvalidTargetPermit
	}
	return nil
}

func (permit TargetObservationPermit) matchesObject(object TargetObjectRef) bool {
	return object.valid() && permit.RootID == object.RootID &&
		permit.RootLocatorDigest == object.RootLocatorDigest && permit.TargetPathDigest == object.TargetPathDigest
}

func (permit TargetObservationPermit) validatePurposeAt(now time.Time, purpose TargetPurpose) error {
	if permit.Purpose != purpose || permit.ValidateAt(now) != nil {
		return ErrInvalidTargetPermit
	}
	return nil
}

func (permit TargetObservationPermit) validateVerifyPurposeAt(now time.Time) error {
	proof := permit.proof
	if permit.validatePurposeAt(now, TargetPurposeVerify) != nil || proof == nil || permit.resultReadProof != nil ||
		!proof.sessionBinding.valid() || !validOpaqueID(proof.jobID) || proof.targetMode.Validate() != nil ||
		!validRecoveryVerifyOperation(proof.operation, proof.expectedPrior) ||
		proof.sessionBinding.NodeID != permit.NodeID || proof.sessionBinding.RootID != permit.RootID ||
		proof.sessionBinding.RootLocatorDigest != permit.RootLocatorDigest ||
		proof.sessionBinding.RootRevision != permit.RootRevision ||
		proof.bindingDigest != targetVerifyPermitProofDigest(permit, proof) {
		return ErrInvalidTargetPermit
	}
	return nil
}

func targetVerifyPermitProofDigest(
	permit TargetObservationPermit,
	proof *targetVerifyPermitProof,
) string {
	if proof == nil {
		return ""
	}
	return framedDigest(
		targetVerifyPermitProofDomain,
		strconv.Itoa(permit.SchemaVersion), strconv.FormatUint(uint64(permit.NodeID), 10),
		string(permit.Purpose), permit.RootID, permit.RootLocatorDigest,
		permit.TargetPathDigest, permit.RootRevision,
		permit.ExpiresAt.UTC().Format(time.RFC3339Nano), proof.jobID,
		string(proof.targetMode), string(proof.operation), string(proof.expectedPrior.Kind),
		proof.expectedPrior.Digest, proof.sessionBinding.bindingDigest,
	)
}

func validRecoveryVerifyOperation(
	operation RecoveryOperationKind,
	expectedPrior ExpectedTargetIdentity,
) bool {
	if !expectedPrior.valid() {
		return false
	}
	switch operation {
	case RecoveryOperationCreate:
		return expectedPrior.Kind == ExpectedTargetAbsent
	case RecoveryOperationOverwrite, RecoveryOperationSkip, RecoveryOperationDelete:
		return expectedPrior.Kind == ExpectedTargetPresent
	default:
		return false
	}
}

// issueTargetVerifyPermit seals structurally valid authority derived from one
// executed-plan session binding without making wall-clock decisions.
func issueTargetVerifyPermit(
	permit TargetObservationPermit,
	binding recoveryTargetSessionBinding,
	jobID string,
	mode TargetMode,
	operation RecoveryOperationKind,
	expectedPrior ExpectedTargetIdentity,
) TargetObservationPermit {
	permit.proof = nil
	permit.resultReadProof = nil
	if permit.SchemaVersion != 1 || permit.NodeID == 0 || permit.Purpose != TargetPurposeVerify ||
		!validBoundedOpaque(permit.RootID, targetRootIDMax) || !validDigest(permit.RootLocatorDigest) ||
		!validDigest(permit.TargetPathDigest) || !validOpaqueRevision(permit.RootRevision) ||
		permit.ExpiresAt.IsZero() || !binding.valid() || !validOpaqueID(jobID) || mode.Validate() != nil ||
		!validRecoveryVerifyOperation(operation, expectedPrior) ||
		binding.NodeID != permit.NodeID || binding.RootID != permit.RootID ||
		binding.RootLocatorDigest != permit.RootLocatorDigest || binding.RootRevision != permit.RootRevision {
		return permit
	}
	permit.proof = &targetVerifyPermitProof{
		sessionBinding: binding,
		jobID:          jobID,
		targetMode:     mode,
		operation:      operation,
		expectedPrior:  expectedPrior,
	}
	permit.proof.bindingDigest = targetVerifyPermitProofDigest(permit, permit.proof)
	return permit
}

type TargetPreflightPermit struct {
	permit TargetObservationPermit
	proof  *targetPreflightPermitProof
}

type targetPreflightPermitProof struct {
	sessionBinding recoveryTargetPreflightSessionBinding
	requestDigest  string
	bindingDigest  string
}

func (permit TargetPreflightPermit) ValidateAt(now time.Time) error {
	proof := permit.proof
	binding := recoveryTargetPreflightSessionBinding{}
	if proof != nil {
		binding = proof.sessionBinding
	}
	if permit.permit.validatePurposeAt(now, TargetPurposePreflight) != nil || proof == nil ||
		permit.permit.proof != nil || permit.permit.resultReadProof != nil ||
		!binding.valid() || !validDigest(proof.requestDigest) ||
		binding.nodeID != permit.permit.NodeID || binding.rootID != permit.permit.RootID ||
		binding.rootLocatorDigest != permit.permit.RootLocatorDigest ||
		binding.rootRevision != permit.permit.RootRevision ||
		binding.targetPathDigest != permit.permit.TargetPathDigest ||
		proof.bindingDigest != targetPreflightPermitProofDigest(permit.permit, proof) {
		return ErrInvalidTargetPermit
	}
	return nil
}

func (permit TargetPreflightPermit) ValidateObjectAt(now time.Time, object TargetObjectRef) error {
	if permit.ValidateAt(now) != nil || !permit.permit.matchesObject(object) {
		return ErrInvalidTargetPermit
	}
	return nil
}

func (permit TargetPreflightPermit) ValidateRequestAt(
	now time.Time,
	public TargetObservationPermit,
	request TargetProbeRequest,
) error {
	if permit.ValidateAt(now) != nil || public.proof != nil ||
		permit.permit.SchemaVersion != public.SchemaVersion || permit.permit.NodeID != public.NodeID ||
		permit.permit.Purpose != public.Purpose || permit.permit.RootID != public.RootID ||
		permit.permit.RootLocatorDigest != public.RootLocatorDigest ||
		permit.permit.TargetPathDigest != public.TargetPathDigest ||
		permit.permit.RootRevision != public.RootRevision ||
		!permit.permit.ExpiresAt.Equal(public.ExpiresAt) ||
		permit.ValidateObjectAt(now, request.Object) != nil || permit.proof == nil ||
		permit.proof.requestDigest != targetPreflightRequestDigest(request) ||
		permit.proof.sessionBinding.privateRelativeLocator != request.Object.PrivateRelativeLocator {
		return ErrInvalidTargetPermit
	}
	return nil
}

func targetPreflightRequestDigest(request TargetProbeRequest) string {
	if !request.Object.valid() || !validDigest(request.SourceRevisionDigest) ||
		!validOpaqueRevision(request.CapabilityRevision) || !validOpaqueRevision(request.PolicyRevision) ||
		request.RequiredBytes < 0 || request.RequiredInodes < 0 {
		return ""
	}
	return framedDigest(
		targetPreflightRequestDomain,
		request.Object.RootID, request.Object.RootLocatorDigest,
		request.Object.TargetPathDigest, request.Object.PrivateRelativeLocator,
		request.SourceRevisionDigest, request.CapabilityRevision, request.PolicyRevision,
		strconv.FormatInt(request.RequiredBytes, 10), strconv.FormatInt(request.RequiredInodes, 10),
	)
}

func targetPreflightPermitProofDigest(
	permit TargetObservationPermit,
	proof *targetPreflightPermitProof,
) string {
	if proof == nil {
		return ""
	}
	return framedDigest(
		targetPreflightPermitProofDomain,
		strconv.Itoa(permit.SchemaVersion), strconv.FormatUint(uint64(permit.NodeID), 10),
		string(permit.Purpose), permit.RootID, permit.RootLocatorDigest,
		permit.TargetPathDigest, permit.RootRevision,
		permit.ExpiresAt.UTC().Format(time.RFC3339Nano),
		proof.sessionBinding.bindingDigest, proof.requestDigest,
	)
}

func issueTargetPreflightPermit(
	permit TargetObservationPermit,
	binding recoveryTargetPreflightSessionBinding,
	request TargetProbeRequest,
) TargetPreflightPermit {
	permit.proof = nil
	permit.resultReadProof = nil
	result := TargetPreflightPermit{permit: permit}
	requestDigest := targetPreflightRequestDigest(request)
	if permit.SchemaVersion != 1 || permit.NodeID == 0 || permit.Purpose != TargetPurposePreflight ||
		!validBoundedOpaque(permit.RootID, targetRootIDMax) || !validDigest(permit.RootLocatorDigest) ||
		!validDigest(permit.TargetPathDigest) || !validOpaqueRevision(permit.RootRevision) ||
		permit.ExpiresAt.IsZero() || !binding.valid() || !validDigest(requestDigest) ||
		binding.nodeID != permit.NodeID || binding.rootID != permit.RootID ||
		binding.rootLocatorDigest != permit.RootLocatorDigest ||
		binding.rootRevision != permit.RootRevision || binding.targetPathDigest != permit.TargetPathDigest ||
		!permit.matchesObject(request.Object) ||
		binding.privateRelativeLocator != request.Object.PrivateRelativeLocator {
		return result
	}
	result.proof = &targetPreflightPermitProof{
		sessionBinding: binding,
		requestDigest:  requestDigest,
	}
	result.proof.bindingDigest = targetPreflightPermitProofDigest(result.permit, result.proof)
	return result
}

type TargetVerifyPermit struct {
	permit TargetObservationPermit
}

func NewTargetVerifyPermit(permit TargetObservationPermit, now time.Time) (TargetVerifyPermit, error) {
	if permit.validateVerifyPurposeAt(now) != nil {
		return TargetVerifyPermit{}, ErrInvalidTargetPermit
	}
	return TargetVerifyPermit{permit: permit}, nil
}

func (permit TargetVerifyPermit) ValidateAt(now time.Time) error {
	return permit.permit.validateVerifyPurposeAt(now)
}

func (permit TargetVerifyPermit) ValidateObjectAt(now time.Time, object TargetObjectRef) error {
	if permit.ValidateAt(now) != nil || !permit.permit.matchesObject(object) {
		return ErrInvalidTargetPermit
	}
	return nil
}

type TargetResultReadPermit struct {
	permit TargetObservationPermit
}

func NewTargetResultReadPermit(permit TargetObservationPermit, now time.Time) (TargetResultReadPermit, error) {
	if permit.validateResultReadPurposeAt(now) != nil {
		return TargetResultReadPermit{}, ErrInvalidTargetPermit
	}
	return TargetResultReadPermit{permit: permit}, nil
}

func (permit TargetResultReadPermit) ValidateAt(now time.Time) error {
	return permit.permit.validateResultReadPurposeAt(now)
}

func (permit TargetResultReadPermit) ValidateObjectAt(now time.Time, object TargetObjectRef) error {
	if permit.ValidateAt(now) != nil || !permit.permit.matchesObject(object) {
		return ErrInvalidTargetPermit
	}
	return nil
}

func (permit TargetResultReadPermit) ValidateRequestAt(
	now time.Time,
	request OpenOwnedResultRequest,
) error {
	proof := permit.permit.resultReadProof
	if permit.ValidateObjectAt(now, request.Object) != nil || proof == nil ||
		request != proof.request || !proof.authority.matchesRequest(request) {
		return ErrInvalidTargetPermit
	}
	return nil
}

func (permit TargetResultReadPermit) authorityForRequestAt(
	now time.Time,
	request OpenOwnedResultRequest,
) (targetResultReadAuthority, error) {
	if permit.ValidateRequestAt(now, request) != nil || permit.permit.resultReadProof == nil {
		return targetResultReadAuthority{}, ErrInvalidTargetPermit
	}
	return permit.permit.resultReadProof.authority, nil
}

type targetResultReadAuthority struct {
	sessionBinding        recoveryTargetSessionBinding
	jobID                 string
	resultSetID           string
	resultID              string
	publicationRevision   uint64
	cleanupFence          uint64
	resultSetState        ResultSetState
	markerBindingDigest   string
	markerCreatorID       string
	markerCreatorFence    uint64
	locatorDigest         string
	object                TargetObjectRef
	expectedBytes         int64
	expectedContentDigest string
	plaintextDeadline     time.Time
}

func (authority targetResultReadAuthority) valid() bool {
	return authority.sessionBinding.valid() && validOpaqueID(authority.jobID) &&
		validOpaqueID(authority.resultSetID) && validOpaqueID(authority.resultID) &&
		authority.publicationRevision > 0 && authority.cleanupFence == 0 &&
		authority.resultSetState == ResultSetStateReady && validDigest(authority.markerBindingDigest) &&
		validRecoveryWorkerID(authority.markerCreatorID) && authority.markerCreatorFence > 0 &&
		validDigest(authority.locatorDigest) && authority.object.valid() && authority.expectedBytes >= 0 &&
		validDigest(authority.expectedContentDigest) && !authority.plaintextDeadline.IsZero() &&
		authority.sessionBinding.NodeID != 0 &&
		authority.sessionBinding.RootID == authority.object.RootID &&
		authority.sessionBinding.RootLocatorDigest == authority.object.RootLocatorDigest &&
		validateRecoveryVerifyNamespace(
			authority.object.PrivateRelativeLocator, authority.jobID, TargetModeIsolated,
		) == nil
}

func (authority targetResultReadAuthority) matchesRequest(request OpenOwnedResultRequest) bool {
	return authority.valid() && request.Object == authority.object &&
		request.ExpectedBytes == authority.expectedBytes &&
		request.IdentityDigest == authority.expectedContentDigest
}

type targetResultReadPermitProof struct {
	authority     targetResultReadAuthority
	request       OpenOwnedResultRequest
	bindingDigest string
}

func issueTargetResultReadPermit(
	permit TargetObservationPermit,
	authority targetResultReadAuthority,
	request OpenOwnedResultRequest,
) TargetObservationPermit {
	permit.proof = nil
	permit.resultReadProof = nil
	if permit.SchemaVersion != 1 || permit.NodeID == 0 || permit.Purpose != TargetPurposeResultRead ||
		!validBoundedOpaque(permit.RootID, targetRootIDMax) || !validDigest(permit.RootLocatorDigest) ||
		!validDigest(permit.TargetPathDigest) || !validOpaqueRevision(permit.RootRevision) ||
		permit.ExpiresAt.IsZero() || !authority.matchesRequest(request) ||
		authority.sessionBinding.NodeID != permit.NodeID || authority.object.RootID != permit.RootID ||
		authority.object.RootLocatorDigest != permit.RootLocatorDigest ||
		authority.object.TargetPathDigest != permit.TargetPathDigest ||
		authority.sessionBinding.RootRevision != permit.RootRevision ||
		permit.ExpiresAt.After(authority.plaintextDeadline) {
		return permit
	}
	proof := &targetResultReadPermitProof{authority: authority, request: request}
	proof.bindingDigest = targetResultReadPermitProofDigest(permit, proof)
	if !validDigest(proof.bindingDigest) {
		return permit
	}
	permit.resultReadProof = proof
	return permit
}

func (permit TargetObservationPermit) validateResultReadPurposeAt(now time.Time) error {
	proof := permit.resultReadProof
	if permit.validatePurposeAt(now, TargetPurposeResultRead) != nil || permit.proof != nil || proof == nil ||
		!proof.authority.matchesRequest(proof.request) ||
		proof.authority.sessionBinding.NodeID != permit.NodeID ||
		proof.authority.object.RootID != permit.RootID ||
		proof.authority.object.RootLocatorDigest != permit.RootLocatorDigest ||
		proof.authority.object.TargetPathDigest != permit.TargetPathDigest ||
		proof.authority.sessionBinding.RootRevision != permit.RootRevision ||
		permit.ExpiresAt.After(proof.authority.plaintextDeadline) ||
		proof.bindingDigest != targetResultReadPermitProofDigest(permit, proof) {
		return ErrInvalidTargetPermit
	}
	return nil
}

func targetResultReadPermitProofDigest(
	permit TargetObservationPermit,
	proof *targetResultReadPermitProof,
) string {
	if proof == nil {
		return ""
	}
	authority := proof.authority
	request := proof.request
	return framedDigest(
		targetResultReadPermitProofDomain,
		strconv.Itoa(permit.SchemaVersion), strconv.FormatUint(uint64(permit.NodeID), 10),
		string(permit.Purpose), permit.RootID, permit.RootLocatorDigest,
		permit.TargetPathDigest, permit.RootRevision, permit.ExpiresAt.UTC().Format(time.RFC3339Nano),
		authority.sessionBinding.bindingDigest, authority.jobID, authority.resultSetID, authority.resultID,
		strconv.FormatUint(authority.publicationRevision, 10), strconv.FormatUint(authority.cleanupFence, 10),
		string(authority.resultSetState), authority.markerBindingDigest, authority.markerCreatorID,
		strconv.FormatUint(authority.markerCreatorFence, 10), authority.locatorDigest,
		authority.object.RootID, authority.object.RootLocatorDigest, authority.object.TargetPathDigest,
		authority.object.PrivateRelativeLocator, strconv.FormatInt(authority.expectedBytes, 10),
		authority.expectedContentDigest, authority.plaintextDeadline.UTC().Format(time.RFC3339Nano),
		request.Object.RootID, request.Object.RootLocatorDigest, request.Object.TargetPathDigest,
		request.Object.PrivateRelativeLocator, strconv.FormatInt(request.ExpectedBytes, 10), request.IdentityDigest,
	)
}

type TargetMutationPermit struct {
	SchemaVersion          int
	NodeID                 uint
	Purpose                TargetPurpose
	RootID                 string
	RootLocatorDigest      string `json:"-"`
	TargetPathDigest       string `json:"-"`
	RootRevision           string
	ExpiresAt              time.Time
	UseLatchID             string
	JobID                  string
	AttemptID              string
	NodeLeaseID            string
	AttemptFence           uint64
	NodeFence              uint64
	ExpectedTargetRevision string
	proof                  *targetMutationPermitProof
}

type targetMutationPermitProof struct {
	validateAt     func(time.Time) error
	sessionBinding recoveryTargetSessionBinding
	bindingDigest  string
}

func (permit TargetMutationPermit) ValidateAt(now time.Time) error {
	if permit.validateShapeAt(now) != nil || permit.proof == nil || permit.proof.validateAt == nil ||
		permit.proof.validateAt(now) != nil ||
		permit.proof.bindingDigest != targetMutationPermitProofDigest(permit, permit.proof.sessionBinding) ||
		(permit.proof.sessionBinding != (recoveryTargetSessionBinding{}) && !permit.proof.sessionBinding.valid()) {
		return ErrInvalidTargetPermit
	}
	return nil
}

func targetMutationPermitProofDigest(
	permit TargetMutationPermit,
	binding recoveryTargetSessionBinding,
) string {
	return framedDigest(
		"xirang/recovery/target-mutation-permit-proof/v1",
		strconv.Itoa(permit.SchemaVersion), strconv.FormatUint(uint64(permit.NodeID), 10),
		string(permit.Purpose), permit.RootID, permit.RootLocatorDigest,
		permit.TargetPathDigest, permit.RootRevision,
		permit.ExpiresAt.UTC().Format(time.RFC3339Nano), permit.UseLatchID,
		permit.JobID, permit.AttemptID, permit.NodeLeaseID,
		strconv.FormatUint(permit.AttemptFence, 10), strconv.FormatUint(permit.NodeFence, 10),
		permit.ExpectedTargetRevision, binding.bindingDigest,
	)
}

func (permit TargetMutationPermit) validateShapeAt(now time.Time) error {
	if now.IsZero() || permit.SchemaVersion != 1 || permit.NodeID == 0 ||
		permit.Purpose != TargetPurposeWrite ||
		!validBoundedOpaque(permit.RootID, targetRootIDMax) || !validDigest(permit.RootLocatorDigest) ||
		!validDigest(permit.TargetPathDigest) ||
		!validOpaqueRevision(permit.RootRevision) || !permit.ExpiresAt.After(now) ||
		permit.UseLatchID != RecoverySchemaUseLatchID || !validOpaqueID(permit.JobID) ||
		!validOpaqueID(permit.AttemptID) || !validOpaqueID(permit.NodeLeaseID) ||
		permit.AttemptFence == 0 || permit.NodeFence == 0 || !validOpaqueRevision(permit.ExpectedTargetRevision) {
		return ErrInvalidTargetPermit
	}
	return nil
}

// issueTargetMutationPermit marks a structurally valid permit as originating
// from the durable first-write boundary. The proof remains package-private so
// callers outside recovery cannot construct remote write authority directly.
func issueTargetMutationPermit(
	permit TargetMutationPermit,
	validateAt func(time.Time) error,
	bindings ...recoveryTargetSessionBinding,
) TargetMutationPermit {
	if len(bindings) > 1 {
		permit.proof = nil
		return permit
	}
	var binding recoveryTargetSessionBinding
	if len(bindings) == 1 {
		binding = bindings[0]
	}
	permit.proof = &targetMutationPermitProof{
		validateAt: validateAt, sessionBinding: binding,
		bindingDigest: targetMutationPermitProofDigest(permit, binding),
	}
	return permit
}

func (permit TargetMutationPermit) ValidateObjectAt(now time.Time, object TargetObjectRef) error {
	if permit.ValidateAt(now) != nil || !permit.matchesObject(object) {
		return ErrInvalidTargetPermit
	}
	return nil
}

func (permit TargetMutationPermit) ValidateFrozenJobAt(now time.Time, job FrozenJobBinding) error {
	if permit.ValidateAt(now) != nil || job.ValidateAt(now) != nil ||
		permit.NodeID != job.Plan.Target.NodeID || permit.RootID != job.Plan.Target.RootID ||
		permit.RootLocatorDigest != job.Plan.Target.RootLocatorDigest ||
		permit.TargetPathDigest != job.Plan.Target.PathDigest ||
		permit.RootRevision != job.Plan.Target.RootRevision ||
		permit.ExpectedTargetRevision != job.Preflight.TargetRevision {
		return ErrInvalidTargetPermit
	}
	return nil
}

func (permit TargetMutationPermit) matchesObject(object TargetObjectRef) bool {
	return object.valid() && permit.RootID == object.RootID &&
		permit.RootLocatorDigest == object.RootLocatorDigest && permit.TargetPathDigest == object.TargetPathDigest
}

func (permit TargetMutationPermit) validatePurposeAt(now time.Time, purpose TargetPurpose) error {
	if permit.Purpose != purpose || permit.ValidateAt(now) != nil {
		return ErrInvalidTargetPermit
	}
	return nil
}

type TargetWritePermit struct {
	permit    TargetMutationPermit
	itemProof *targetItemWritePermitProof
}

func (TargetWritePermit) String() string {
	return redactedRecoveryTargetProduct("TargetWritePermit")
}

func (TargetWritePermit) GoString() string {
	return redactedRecoveryTargetProduct("TargetWritePermit")
}

type TargetFinalizeOverwriteRequest struct {
	Object         TargetObjectRef
	ExpectedDigest string
	ExpectedBytes  int64
}

func (TargetFinalizeOverwriteRequest) String() string {
	return redactedRecoveryTargetProduct("TargetFinalizeOverwriteRequest")
}

func (TargetFinalizeOverwriteRequest) GoString() string {
	return redactedRecoveryTargetProduct("TargetFinalizeOverwriteRequest")
}

type TargetFinalizeOverwritePermit struct {
	permit TargetMutationPermit
	proof  *targetFinalizeOverwritePermitProof
}

func (TargetFinalizeOverwritePermit) String() string {
	return redactedRecoveryTargetProduct("TargetFinalizeOverwritePermit")
}

func (TargetFinalizeOverwritePermit) GoString() string {
	return redactedRecoveryTargetProduct("TargetFinalizeOverwritePermit")
}

type recoveryDeleteArtifactBinding struct {
	keyVersion        int
	bindingDigest     string
	token             string
	intentComponent   string
	capturedComponent string
	verifiedComponent string
	intentDocument    string
	verifiedDocument  string
}

type targetDeletePermitProof struct {
	sessionBinding       recoveryTargetSessionBinding
	jobID                string
	jobItemID            string
	operationDigest      string
	consumedCheckpointID string
	consumedGrantID      string
	consumedGrantDigest  string
	currentAttemptID     string
	currentAttemptFence  uint64
	currentNodeLeaseID   string
	currentNodeFence     uint64
	currentSourceFence   backupasset.LeaseFence
	targetChainRevision  string
	targetMode           TargetMode
	object               TargetObjectRef
	expectedPrior        ExpectedTargetIdentity
	expectedPriorBytes   int64
	artifacts            recoveryDeleteArtifactBinding
	bindingDigest        string
}

type TargetDeletePermit struct {
	permit TargetMutationPermit
	proof  *targetDeletePermitProof
}

func (TargetDeletePermit) String() string {
	return redactedRecoveryTargetProduct("TargetDeletePermit")
}

func (TargetDeletePermit) GoString() string {
	return redactedRecoveryTargetProduct("TargetDeletePermit")
}

type TargetDeleteRequest struct {
	Object TargetObjectRef
}

func (TargetDeleteRequest) String() string {
	return redactedRecoveryTargetProduct("TargetDeleteRequest")
}

func (TargetDeleteRequest) GoString() string {
	return redactedRecoveryTargetProduct("TargetDeleteRequest")
}

type recoveryDeleteArtifactBindingInput struct {
	keyVersion           int
	planID               string
	planBindingDigest    string
	jobID                string
	jobItemID            string
	operationDigest      string
	consumedCheckpointID string
	consumedGrantID      string
	consumedGrantDigest  string
	targetMode           TargetMode
	nodeID               uint
	rootID               string
	rootLocatorDigest    string
	rootRevision         string
	object               TargetObjectRef
	expectedPrior        ExpectedTargetIdentity
	expectedPriorBytes   int64
}

type recoveryDeleteMarkerDocumentBody struct {
	SchemaVersion int    `json:"schema_version"`
	KeyVersion    int    `json:"key_version"`
	Phase         string `json:"phase"`
	BindingDigest string `json:"binding_digest"`
}

type recoveryDeleteMarkerDocument struct {
	SchemaVersion     int    `json:"schema_version"`
	KeyVersion        int    `json:"key_version"`
	Phase             string `json:"phase"`
	BindingDigest     string `json:"binding_digest"`
	AuthenticationTag string `json:"authentication_tag"`
}

func deriveRecoveryDeleteArtifactBinding(
	material backupasset.DomainKeyMaterial,
	input recoveryDeleteArtifactBindingInput,
) (recoveryDeleteArtifactBinding, error) {
	if !validTargetLocatorKey(material, input.keyVersion) || !validOpaqueID(input.planID) ||
		!validDigest(input.planBindingDigest) || !validOpaqueID(input.jobID) ||
		!validOpaqueID(input.jobItemID) || !validDigest(input.operationDigest) ||
		!validOpaqueID(input.consumedCheckpointID) || !validOpaqueID(input.consumedGrantID) ||
		!validDigest(input.consumedGrantDigest) || input.targetMode != TargetModeInPlace || input.nodeID == 0 ||
		!validBoundedOpaque(input.rootID, targetRootIDMax) || !validDigest(input.rootLocatorDigest) ||
		!validOpaqueRevision(input.rootRevision) || !input.object.valid() ||
		input.object.RootID != input.rootID || input.object.RootLocatorDigest != input.rootLocatorDigest ||
		validateRecoveryVerifyNamespace(input.object.PrivateRelativeLocator, input.jobID, input.targetMode) != nil ||
		input.expectedPrior.Kind != ExpectedTargetPresent || !validDigest(input.expectedPrior.Digest) ||
		input.expectedPriorBytes != -1 {
		return recoveryDeleteArtifactBinding{}, ErrInvalidTargetPermit
	}

	raw := recoveryOverwriteFramedHMAC(
		material.Key,
		recoveryDeleteArtifactBindingDomain,
		strconv.Itoa(input.keyVersion),
		input.planID,
		input.planBindingDigest,
		input.jobID,
		input.jobItemID,
		input.operationDigest,
		input.consumedCheckpointID,
		input.consumedGrantID,
		input.consumedGrantDigest,
		string(input.targetMode),
		strconv.FormatUint(uint64(input.nodeID), 10),
		input.rootID,
		input.rootLocatorDigest,
		input.rootRevision,
		input.object.RootID,
		input.object.RootLocatorDigest,
		input.object.TargetPathDigest,
		input.object.PrivateRelativeLocator,
		string(input.expectedPrior.Kind),
		input.expectedPrior.Digest,
		strconv.FormatInt(input.expectedPriorBytes, 10),
	)
	token := base64.RawURLEncoding.EncodeToString(raw)
	binding := recoveryDeleteArtifactBinding{
		keyVersion: input.keyVersion, bindingDigest: hex.EncodeToString(raw), token: token,
		intentComponent:   recoveryDeleteArtifactPrefix + token + ".intent",
		capturedComponent: recoveryDeleteArtifactPrefix + token + ".captured",
		verifiedComponent: recoveryDeleteArtifactPrefix + token + ".verified",
	}
	var err error
	binding.intentDocument, err = encodeRecoveryDeleteMarkerDocument(
		material.Key, binding.keyVersion, "intent", binding.bindingDigest,
	)
	if err != nil {
		return recoveryDeleteArtifactBinding{}, ErrInvalidTargetPermit
	}
	binding.verifiedDocument, err = encodeRecoveryDeleteMarkerDocument(
		material.Key, binding.keyVersion, "verified", binding.bindingDigest,
	)
	if err != nil || !binding.valid() ||
		!verifyRecoveryDeleteMarkerDocument(
			material.Key, binding.intentDocument, binding.keyVersion, "intent", binding.bindingDigest,
		) || !verifyRecoveryDeleteMarkerDocument(
		material.Key, binding.verifiedDocument, binding.keyVersion, "verified", binding.bindingDigest,
	) {
		return recoveryDeleteArtifactBinding{}, ErrInvalidTargetPermit
	}
	return binding, nil
}

func encodeRecoveryDeleteMarkerDocument(
	key []byte,
	keyVersion int,
	phase string,
	bindingDigest string,
) (string, error) {
	domain, ok := recoveryDeleteMarkerDocumentDomain(phase)
	if !ok || len(key) != sha256.Size || keyVersion <= 0 || !validDigest(bindingDigest) {
		return "", ErrInvalidTargetPermit
	}
	body := recoveryDeleteMarkerDocumentBody{
		SchemaVersion: recoveryDeleteMarkerSchemaVersion,
		KeyVersion:    keyVersion,
		Phase:         phase,
		BindingDigest: bindingDigest,
	}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return "", ErrInvalidTargetPermit
	}
	tag := recoveryOverwriteFramedHMAC(key, domain, string(bodyBytes))
	document, err := json.Marshal(recoveryDeleteMarkerDocument{
		SchemaVersion:     body.SchemaVersion,
		KeyVersion:        body.KeyVersion,
		Phase:             body.Phase,
		BindingDigest:     body.BindingDigest,
		AuthenticationTag: base64.RawURLEncoding.EncodeToString(tag),
	})
	if err != nil || len(document) == 0 || len(document) > recoveryDeleteMarkerDocumentMaxBytes {
		return "", ErrInvalidTargetPermit
	}
	return string(document), nil
}

func recoveryDeleteMarkerDocumentDomain(phase string) (string, bool) {
	switch phase {
	case "intent":
		return recoveryDeleteIntentDocumentDomain, true
	case "verified":
		return recoveryDeleteVerifiedDocumentDomain, true
	default:
		return "", false
	}
}

func verifyRecoveryDeleteMarkerDocument(
	key []byte,
	encoded string,
	keyVersion int,
	phase string,
	bindingDigest string,
) bool {
	if len(key) != sha256.Size ||
		!validRecoveryDeleteMarkerDocumentShape(encoded, keyVersion, phase, bindingDigest) {
		return false
	}
	domain, ok := recoveryDeleteMarkerDocumentDomain(phase)
	if !ok {
		return false
	}
	var document recoveryDeleteMarkerDocument
	if err := json.Unmarshal([]byte(encoded), &document); err != nil {
		return false
	}
	bodyBytes, err := json.Marshal(recoveryDeleteMarkerDocumentBody{
		SchemaVersion: document.SchemaVersion,
		KeyVersion:    document.KeyVersion,
		Phase:         document.Phase,
		BindingDigest: document.BindingDigest,
	})
	if err != nil {
		return false
	}
	tag, err := base64.RawURLEncoding.DecodeString(document.AuthenticationTag)
	if err != nil || len(tag) != sha256.Size ||
		base64.RawURLEncoding.EncodeToString(tag) != document.AuthenticationTag {
		return false
	}
	want := recoveryOverwriteFramedHMAC(key, domain, string(bodyBytes))
	return hmac.Equal(tag, want)
}

func validRecoveryDeleteMarkerDocumentShape(
	encoded string,
	keyVersion int,
	phase string,
	bindingDigest string,
) bool {
	if encoded == "" || len(encoded) > recoveryDeleteMarkerDocumentMaxBytes {
		return false
	}
	if _, ok := recoveryDeleteMarkerDocumentDomain(phase); !ok {
		return false
	}
	var document recoveryDeleteMarkerDocument
	if err := json.Unmarshal([]byte(encoded), &document); err != nil {
		return false
	}
	canonical, err := json.Marshal(document)
	if err != nil || string(canonical) != encoded ||
		document.SchemaVersion != recoveryDeleteMarkerSchemaVersion ||
		document.KeyVersion != keyVersion || document.Phase != phase ||
		document.BindingDigest != bindingDigest || !validDigest(document.BindingDigest) {
		return false
	}
	tag, err := base64.RawURLEncoding.DecodeString(document.AuthenticationTag)
	return err == nil && len(tag) == sha256.Size &&
		base64.RawURLEncoding.EncodeToString(tag) == document.AuthenticationTag
}

func (binding recoveryDeleteArtifactBinding) valid() bool {
	if binding.keyVersion <= 0 || !validDigest(binding.bindingDigest) {
		return false
	}
	rawToken, err := base64.RawURLEncoding.DecodeString(binding.token)
	rawDigest, digestErr := hex.DecodeString(binding.bindingDigest)
	if err != nil || digestErr != nil || len(rawToken) != sha256.Size ||
		base64.RawURLEncoding.EncodeToString(rawToken) != binding.token || !hmac.Equal(rawToken, rawDigest) {
		return false
	}
	components := map[string]string{
		"intent": binding.intentComponent, "captured": binding.capturedComponent,
		"verified": binding.verifiedComponent,
	}
	for phase, component := range components {
		if component != recoveryDeleteArtifactPrefix+binding.token+"."+phase ||
			len(component) > recoveryDeleteArtifactComponentMaxBytes || path.Base(component) != component {
			return false
		}
	}
	return validRecoveryDeleteMarkerDocumentShape(
		binding.intentDocument, binding.keyVersion, "intent", binding.bindingDigest,
	) && validRecoveryDeleteMarkerDocumentShape(
		binding.verifiedDocument, binding.keyVersion, "verified", binding.bindingDigest,
	)
}

type recoveryDeleteTupleState uint8

const (
	recoveryDeleteTupleFresh recoveryDeleteTupleState = iota + 1
	recoveryDeleteTupleIntent
	recoveryDeleteTupleCaptured
	recoveryDeleteTupleVerified
	recoveryDeleteTupleDeleted
	recoveryDeleteTupleClean
	recoveryDeleteTupleConflict
)

type recoveryDeleteTupleTransition uint8

const (
	recoveryDeleteTupleStop recoveryDeleteTupleTransition = iota + 1
	recoveryDeleteTupleCreateIntent
	recoveryDeleteTupleCapture
	recoveryDeleteTupleVerifyCaptured
	recoveryDeleteTupleDeleteCaptured
	recoveryDeleteTupleRemoveIntent
	recoveryDeleteTupleRemoveVerified
	recoveryDeleteTupleComplete
)

type recoveryDeleteMarkerObservation struct {
	entry    recoveryDeleteEntryObservation
	document string
}

type recoveryDeleteTupleObservation struct {
	final    recoveryDeleteEntryObservation
	intent   recoveryDeleteMarkerObservation
	captured recoveryDeleteEntryObservation
	verified recoveryDeleteMarkerObservation
}

type recoveryDeleteTupleClassification struct {
	state      recoveryDeleteTupleState
	transition recoveryDeleteTupleTransition
}

type recoveryDeleteMarkerFacts struct {
	missing bool
	exact   bool
}

func exactRecoveryDeleteMissingObservation(observation recoveryDeleteEntryObservation) bool {
	return observation.result.Kind == TargetEntryMissing &&
		observation.result.IdentityDigest == "" && validOpaqueRevision(observation.result.TargetRevision) &&
		observation.size == 0 && observation.mode == 0 && observation.uid == 0 &&
		observation.gid == 0 && observation.mtime == 0 && observation.payloadFact == ""
}

func validRecoveryDeleteRegularObservation(observation recoveryDeleteEntryObservation) bool {
	return observation.result.Kind == TargetEntryRegular &&
		validDigest(observation.result.IdentityDigest) && validOpaqueRevision(observation.result.TargetRevision) &&
		observation.size >= 0 && observation.mode.IsRegular() && validDigest(observation.payloadFact)
}

func validRecoveryDeletePresentObservation(observation recoveryDeleteEntryObservation) bool {
	if !validDigest(observation.result.IdentityDigest) ||
		!validOpaqueRevision(observation.result.TargetRevision) || observation.size < 0 {
		return false
	}
	switch observation.result.Kind {
	case TargetEntryRegular:
		return observation.mode.IsRegular() && validDigest(observation.payloadFact)
	case TargetEntryDirectory:
		return observation.mode.IsDir() && observation.payloadFact == ""
	case TargetEntrySymlink:
		return observation.mode&os.ModeSymlink != 0
	case TargetEntrySpecial:
		return !observation.mode.IsRegular() && !observation.mode.IsDir() &&
			observation.mode&os.ModeSymlink == 0 && observation.payloadFact == ""
	default:
		return false
	}
}

func exactRecoveryDeletePriorObservation(
	observation recoveryDeleteEntryObservation,
	expected ExpectedTargetIdentity,
	expectedBytes int64,
) bool {
	return expected.Kind == ExpectedTargetPresent && validDigest(expected.Digest) && expectedBytes == -1 &&
		validRecoveryDeletePresentObservation(observation) &&
		observation.result.IdentityDigest == expected.Digest
}

func classifyRecoveryDeleteMarkerObservation(
	material backupasset.DomainKeyMaterial,
	observation recoveryDeleteMarkerObservation,
	keyVersion int,
	phase string,
	bindingDigest string,
	expectedDocument string,
) recoveryDeleteMarkerFacts {
	if exactRecoveryDeleteMissingObservation(observation.entry) && observation.document == "" {
		return recoveryDeleteMarkerFacts{missing: true}
	}
	if !validRecoveryDeleteRegularObservation(observation.entry) ||
		observation.entry.mode.Perm() != 0o600 || observation.document != expectedDocument ||
		observation.entry.size != int64(len(observation.document)) ||
		!verifyRecoveryDeleteMarkerDocument(
			material.Key, observation.document, keyVersion, phase, bindingDigest,
		) {
		return recoveryDeleteMarkerFacts{}
	}
	digest := sha256.Sum256([]byte(observation.document))
	if observation.entry.payloadFact != hex.EncodeToString(digest[:]) {
		return recoveryDeleteMarkerFacts{}
	}
	return recoveryDeleteMarkerFacts{exact: true}
}

func classifyRecoveryDeleteTuple(
	material backupasset.DomainKeyMaterial,
	authority targetDeletePermitProof,
	observation recoveryDeleteTupleObservation,
) recoveryDeleteTupleClassification {
	conflict := recoveryDeleteTupleClassification{
		state: recoveryDeleteTupleConflict, transition: recoveryDeleteTupleStop,
	}
	if !validTargetLocatorKey(material, authority.artifacts.keyVersion) ||
		!authority.sessionBinding.valid() || !validOpaqueID(authority.jobID) ||
		!validOpaqueID(authority.jobItemID) || !validDigest(authority.operationDigest) ||
		!validOpaqueID(authority.consumedCheckpointID) || !validOpaqueID(authority.consumedGrantID) ||
		!validDigest(authority.consumedGrantDigest) || authority.targetMode != TargetModeInPlace ||
		!authority.object.valid() || authority.expectedPrior.Kind != ExpectedTargetPresent ||
		!validDigest(authority.expectedPrior.Digest) || authority.expectedPriorBytes != -1 ||
		!authority.artifacts.valid() || !verifyRecoveryDeleteMarkerDocument(
		material.Key, authority.artifacts.intentDocument, authority.artifacts.keyVersion,
		"intent", authority.artifacts.bindingDigest,
	) || !verifyRecoveryDeleteMarkerDocument(
		material.Key, authority.artifacts.verifiedDocument, authority.artifacts.keyVersion,
		"verified", authority.artifacts.bindingDigest,
	) {
		return conflict
	}

	finalMissing := exactRecoveryDeleteMissingObservation(observation.final)
	finalPrior := exactRecoveryDeletePriorObservation(
		observation.final, authority.expectedPrior, authority.expectedPriorBytes,
	)
	capturedMissing := exactRecoveryDeleteMissingObservation(observation.captured)
	capturedPrior := exactRecoveryDeletePriorObservation(
		observation.captured, authority.expectedPrior, authority.expectedPriorBytes,
	)
	intent := classifyRecoveryDeleteMarkerObservation(
		material, observation.intent, authority.artifacts.keyVersion, "intent",
		authority.artifacts.bindingDigest, authority.artifacts.intentDocument,
	)
	verified := classifyRecoveryDeleteMarkerObservation(
		material, observation.verified, authority.artifacts.keyVersion, "verified",
		authority.artifacts.bindingDigest, authority.artifacts.verifiedDocument,
	)
	if (!finalMissing && !finalPrior) || (!capturedMissing && !capturedPrior) ||
		(!intent.missing && !intent.exact) || (!verified.missing && !verified.exact) {
		return conflict
	}

	switch {
	case finalPrior && capturedMissing && intent.missing && verified.missing:
		return recoveryDeleteTupleClassification{
			state: recoveryDeleteTupleFresh, transition: recoveryDeleteTupleCreateIntent,
		}
	case finalPrior && capturedMissing && intent.exact && verified.missing:
		return recoveryDeleteTupleClassification{
			state: recoveryDeleteTupleIntent, transition: recoveryDeleteTupleCapture,
		}
	case finalMissing && capturedPrior && intent.exact && verified.missing:
		return recoveryDeleteTupleClassification{
			state: recoveryDeleteTupleCaptured, transition: recoveryDeleteTupleVerifyCaptured,
		}
	case finalMissing && capturedPrior && intent.exact && verified.exact:
		return recoveryDeleteTupleClassification{
			state: recoveryDeleteTupleVerified, transition: recoveryDeleteTupleDeleteCaptured,
		}
	case finalMissing && capturedMissing && intent.exact && verified.exact:
		return recoveryDeleteTupleClassification{
			state: recoveryDeleteTupleDeleted, transition: recoveryDeleteTupleRemoveIntent,
		}
	case finalMissing && capturedMissing && intent.missing && verified.exact:
		return recoveryDeleteTupleClassification{
			state: recoveryDeleteTupleDeleted, transition: recoveryDeleteTupleRemoveVerified,
		}
	case finalMissing && capturedMissing && intent.missing && verified.missing:
		return recoveryDeleteTupleClassification{
			state: recoveryDeleteTupleClean, transition: recoveryDeleteTupleComplete,
		}
	default:
		return conflict
	}
}

func issueTargetDeletePermit(
	permit TargetMutationPermit,
	proof targetDeletePermitProof,
) TargetDeletePermit {
	result := TargetDeletePermit{permit: permit}
	proof.bindingDigest = ""
	if !targetDeletePermitProofMatches(permit, &proof) {
		return result
	}
	proof.bindingDigest = targetDeletePermitProofDigest(permit, &proof)
	if !validDigest(proof.bindingDigest) {
		return result
	}
	result.proof = &proof
	return result
}

func targetDeletePermitProofMatches(
	permit TargetMutationPermit,
	proof *targetDeletePermitProof,
) bool {
	if permit.proof == nil || permit.proof.validateAt == nil ||
		permit.Purpose != TargetPurposeWrite || !validDigest(permit.proof.bindingDigest) || proof == nil ||
		!proof.sessionBinding.valid() || proof.sessionBinding != permit.proof.sessionBinding ||
		!validOpaqueID(proof.jobID) || proof.jobID != permit.JobID ||
		!validOpaqueID(proof.jobItemID) || !validDigest(proof.operationDigest) ||
		!validOpaqueID(proof.consumedCheckpointID) || !validOpaqueID(proof.consumedGrantID) ||
		!validDigest(proof.consumedGrantDigest) ||
		proof.currentAttemptID != permit.AttemptID || proof.currentAttemptFence != permit.AttemptFence ||
		proof.currentNodeLeaseID != permit.NodeLeaseID || proof.currentNodeFence != permit.NodeFence ||
		proof.currentSourceFence.LeaseID == "" || proof.currentSourceFence.RecoveryPointID == "" ||
		proof.currentSourceFence.HolderType != backupasset.LeaseHolderRecoveryJob ||
		proof.currentSourceFence.OwnerID == "" ||
		proof.currentSourceFence.AttemptID != proof.currentAttemptID ||
		proof.currentSourceFence.FenceToken == "" ||
		!validOpaqueRevision(proof.targetChainRevision) ||
		proof.targetChainRevision != permit.ExpectedTargetRevision ||
		proof.targetMode != TargetModeInPlace ||
		!proof.object.valid() || !permit.matchesObject(proof.object) ||
		proof.sessionBinding.NodeID != permit.NodeID || proof.sessionBinding.RootID != permit.RootID ||
		proof.sessionBinding.RootLocatorDigest != permit.RootLocatorDigest ||
		proof.sessionBinding.RootRevision != permit.RootRevision ||
		validateRecoveryVerifyNamespace(
			proof.object.PrivateRelativeLocator, proof.jobID, proof.targetMode,
		) != nil || proof.expectedPrior.Kind != ExpectedTargetPresent ||
		!validDigest(proof.expectedPrior.Digest) || proof.expectedPriorBytes != -1 || !proof.artifacts.valid() {
		return false
	}
	return true
}

func targetDeletePermitProofDigest(
	permit TargetMutationPermit,
	proof *targetDeletePermitProof,
) string {
	if proof == nil {
		return ""
	}
	return framedDigest(
		targetDeletePermitProofDomain,
		targetMutationPermitProofDigest(permit, proof.sessionBinding),
		proof.sessionBinding.bindingDigest,
		proof.jobID,
		proof.jobItemID,
		proof.operationDigest,
		proof.consumedCheckpointID,
		proof.consumedGrantID,
		proof.consumedGrantDigest,
		proof.currentAttemptID,
		strconv.FormatUint(proof.currentAttemptFence, 10),
		proof.currentNodeLeaseID,
		strconv.FormatUint(proof.currentNodeFence, 10),
		proof.currentSourceFence.LeaseID,
		proof.currentSourceFence.RecoveryPointID,
		string(proof.currentSourceFence.HolderType),
		proof.currentSourceFence.OwnerID,
		proof.currentSourceFence.AttemptID,
		proof.currentSourceFence.FenceToken,
		proof.targetChainRevision,
		string(proof.targetMode),
		proof.object.RootID,
		proof.object.RootLocatorDigest,
		proof.object.TargetPathDigest,
		proof.object.PrivateRelativeLocator,
		string(proof.expectedPrior.Kind),
		proof.expectedPrior.Digest,
		strconv.FormatInt(proof.expectedPriorBytes, 10),
		strconv.Itoa(proof.artifacts.keyVersion),
		proof.artifacts.bindingDigest,
		proof.artifacts.token,
		proof.artifacts.intentComponent,
		proof.artifacts.capturedComponent,
		proof.artifacts.verifiedComponent,
		proof.artifacts.intentDocument,
		proof.artifacts.verifiedDocument,
	)
}

func (permit TargetDeletePermit) validateRequestAt(
	now time.Time,
	request TargetDeleteRequest,
) error {
	_, err := permit.authorityAt(now, request)
	return err
}

func (permit TargetDeletePermit) authorityAt(
	now time.Time,
	request TargetDeleteRequest,
) (targetDeletePermitProof, error) {
	proof := permit.proof
	if permit.permit.ValidateAt(now) != nil || !targetDeletePermitProofMatches(permit.permit, proof) ||
		proof.bindingDigest != targetDeletePermitProofDigest(permit.permit, proof) || request.Object != proof.object {
		return targetDeletePermitProof{}, ErrInvalidTargetPermit
	}
	return *proof, nil
}

type recoveryOverwriteArtifactBindingInput struct {
	keyVersion         int
	planID             string
	planBindingDigest  string
	jobID              string
	jobItemID          string
	operationDigest    string
	targetMode         TargetMode
	nodeID             uint
	rootID             string
	rootLocatorDigest  string
	rootRevision       string
	object             TargetObjectRef
	expectedPrior      ExpectedTargetIdentity
	expectedPriorBytes int64
	expectedPostDigest string
	expectedPostBytes  int64
}

type recoveryOverwriteArtifactBinding struct {
	keyVersion         int
	bindingDigest      string
	token              string
	intentComponent    string
	priorComponent     string
	postComponent      string
	publishedComponent string
	intentDocument     string
	publishedDocument  string
}

type recoveryOverwriteMarkerDocumentBody struct {
	SchemaVersion int    `json:"schema_version"`
	KeyVersion    int    `json:"key_version"`
	Phase         string `json:"phase"`
	BindingDigest string `json:"binding_digest"`
}

type recoveryOverwriteMarkerDocument struct {
	SchemaVersion     int    `json:"schema_version"`
	KeyVersion        int    `json:"key_version"`
	Phase             string `json:"phase"`
	BindingDigest     string `json:"binding_digest"`
	AuthenticationTag string `json:"authentication_tag"`
}

func newRecoveryOverwriteArtifactBinding(
	material backupasset.DomainKeyMaterial,
	input recoveryOverwriteArtifactBindingInput,
) (recoveryOverwriteArtifactBinding, error) {
	if !validTargetLocatorKey(material, input.keyVersion) || !validOpaqueID(input.planID) ||
		!validDigest(input.planBindingDigest) || !validOpaqueID(input.jobID) ||
		!validOpaqueID(input.jobItemID) || !validDigest(input.operationDigest) ||
		input.targetMode != TargetModeInPlace || input.nodeID == 0 ||
		!validBoundedOpaque(input.rootID, targetRootIDMax) || !validDigest(input.rootLocatorDigest) ||
		!validOpaqueRevision(input.rootRevision) || !input.object.valid() ||
		input.object.RootID != input.rootID || input.object.RootLocatorDigest != input.rootLocatorDigest ||
		input.expectedPrior.Kind != ExpectedTargetPresent || !validDigest(input.expectedPrior.Digest) ||
		input.expectedPriorBytes < 0 || !validDigest(input.expectedPostDigest) || input.expectedPostBytes < 0 {
		return recoveryOverwriteArtifactBinding{}, ErrInvalidTargetPermit
	}

	raw := recoveryOverwriteFramedHMAC(
		material.Key,
		recoveryOverwriteArtifactBindingDomain,
		strconv.Itoa(input.keyVersion),
		input.planID,
		input.planBindingDigest,
		input.jobID,
		input.jobItemID,
		input.operationDigest,
		string(input.targetMode),
		strconv.FormatUint(uint64(input.nodeID), 10),
		input.rootID,
		input.rootLocatorDigest,
		input.rootRevision,
		input.object.RootID,
		input.object.RootLocatorDigest,
		input.object.TargetPathDigest,
		input.object.PrivateRelativeLocator,
		string(input.expectedPrior.Kind),
		input.expectedPrior.Digest,
		strconv.FormatInt(input.expectedPriorBytes, 10),
		input.expectedPostDigest,
		strconv.FormatInt(input.expectedPostBytes, 10),
	)
	token := base64.RawURLEncoding.EncodeToString(raw)
	binding := recoveryOverwriteArtifactBinding{
		keyVersion: input.keyVersion, bindingDigest: hex.EncodeToString(raw), token: token,
		intentComponent:    recoveryOverwriteArtifactPrefix + token + ".intent",
		priorComponent:     recoveryOverwriteArtifactPrefix + token + ".prior",
		postComponent:      recoveryOverwriteArtifactPrefix + token + ".post",
		publishedComponent: recoveryOverwriteArtifactPrefix + token + ".published",
	}
	var err error
	binding.intentDocument, err = encodeRecoveryOverwriteMarkerDocument(
		material.Key, binding.keyVersion, "intent", binding.bindingDigest,
	)
	if err != nil {
		return recoveryOverwriteArtifactBinding{}, ErrInvalidTargetPermit
	}
	binding.publishedDocument, err = encodeRecoveryOverwriteMarkerDocument(
		material.Key, binding.keyVersion, "published", binding.bindingDigest,
	)
	if err != nil || !binding.valid() {
		return recoveryOverwriteArtifactBinding{}, ErrInvalidTargetPermit
	}
	return binding, nil
}

func recoveryOverwriteFramedHMAC(key []byte, domain string, values ...string) []byte {
	buffer := bytes.NewBuffer(nil)
	writeRecoveryDigestString(buffer, domain)
	writeRecoveryDigestUint64(buffer, uint64(len(values)))
	for _, value := range values {
		writeRecoveryDigestString(buffer, value)
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(buffer.Bytes())
	return mac.Sum(nil)
}

func encodeRecoveryOverwriteMarkerDocument(
	key []byte,
	keyVersion int,
	phase string,
	bindingDigest string,
) (string, error) {
	body := recoveryOverwriteMarkerDocumentBody{
		SchemaVersion: recoveryOverwriteMarkerSchemaVersion,
		KeyVersion:    keyVersion, Phase: phase, BindingDigest: bindingDigest,
	}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	tag := recoveryOverwriteFramedHMAC(key, recoveryOverwriteMarkerDocumentDomain, string(bodyBytes))
	document, err := json.Marshal(recoveryOverwriteMarkerDocument{
		SchemaVersion: body.SchemaVersion, KeyVersion: body.KeyVersion,
		Phase: body.Phase, BindingDigest: body.BindingDigest,
		AuthenticationTag: base64.RawURLEncoding.EncodeToString(tag),
	})
	if err != nil || len(document) > recoveryOverwriteMarkerDocumentMaxBytes {
		return "", ErrInvalidTargetPermit
	}
	return string(document), nil
}

func (binding recoveryOverwriteArtifactBinding) valid() bool {
	if binding.keyVersion <= 0 || !validDigest(binding.bindingDigest) {
		return false
	}
	rawToken, err := base64.RawURLEncoding.DecodeString(binding.token)
	rawDigest, digestErr := hex.DecodeString(binding.bindingDigest)
	if err != nil || digestErr != nil || len(rawToken) != sha256.Size ||
		base64.RawURLEncoding.EncodeToString(rawToken) != binding.token || !hmac.Equal(rawToken, rawDigest) {
		return false
	}
	components := map[string]string{
		"intent": binding.intentComponent, "prior": binding.priorComponent,
		"post": binding.postComponent, "published": binding.publishedComponent,
	}
	for phase, component := range components {
		if component != recoveryOverwriteArtifactPrefix+binding.token+"."+phase ||
			len(component) > recoveryOverwriteArtifactComponentMaxBytes || path.Base(component) != component {
			return false
		}
	}
	return validRecoveryOverwriteMarkerDocumentShape(
		binding.intentDocument, binding.keyVersion, "intent", binding.bindingDigest,
	) && validRecoveryOverwriteMarkerDocumentShape(
		binding.publishedDocument, binding.keyVersion, "published", binding.bindingDigest,
	)
}

func validRecoveryOverwriteMarkerDocumentShape(
	encoded string,
	keyVersion int,
	phase string,
	bindingDigest string,
) bool {
	if encoded == "" || len(encoded) > recoveryOverwriteMarkerDocumentMaxBytes {
		return false
	}
	var document recoveryOverwriteMarkerDocument
	if err := json.Unmarshal([]byte(encoded), &document); err != nil {
		return false
	}
	canonical, err := json.Marshal(document)
	if err != nil || string(canonical) != encoded ||
		document.SchemaVersion != recoveryOverwriteMarkerSchemaVersion ||
		document.KeyVersion != keyVersion || document.Phase != phase ||
		document.BindingDigest != bindingDigest || !validDigest(document.BindingDigest) {
		return false
	}
	tag, err := base64.RawURLEncoding.DecodeString(document.AuthenticationTag)
	return err == nil && len(tag) == sha256.Size &&
		base64.RawURLEncoding.EncodeToString(tag) == document.AuthenticationTag
}

type targetItemWritePermitProof struct {
	sessionBinding     recoveryTargetSessionBinding
	jobID              string
	jobItemID          string
	operationDigest    string
	targetMode         TargetMode
	object             TargetObjectRef
	operation          RecoveryOperationKind
	expectedPrior      ExpectedTargetIdentity
	expectedPriorBytes int64
	expectedDigest     string
	expectedBytes      int64
	artifacts          recoveryOverwriteArtifactBinding
	bindingDigest      string
}

type targetItemWriteAuthority struct {
	sessionBinding     recoveryTargetSessionBinding
	jobID              string
	jobItemID          string
	operationDigest    string
	targetMode         TargetMode
	operation          RecoveryOperationKind
	expectedPrior      ExpectedTargetIdentity
	expectedPriorBytes int64
	artifacts          recoveryOverwriteArtifactBinding
}

type targetFinalizeOverwritePermitProof struct {
	sessionBinding         recoveryTargetSessionBinding
	jobID                  string
	jobItemID              string
	checkpointID           string
	operationDigest        string
	checkpointAttemptID    string
	checkpointAttemptFence uint64
	checkpointNodeFence    uint64
	currentAttemptID       string
	currentAttemptFence    uint64
	currentNodeLeaseID     string
	currentNodeFence       uint64
	currentSourceFence     backupasset.LeaseFence
	targetChainRevision    string
	priorTargetRevision    string
	nextTargetRevision     string
	object                 TargetObjectRef
	expectedPrior          ExpectedTargetIdentity
	expectedPriorBytes     int64
	expectedPostDigest     string
	expectedPostBytes      int64
	artifacts              recoveryOverwriteArtifactBinding
	bindingDigest          string
}

type targetFinalizeOverwriteAuthority struct {
	sessionBinding      recoveryTargetSessionBinding
	jobID               string
	jobItemID           string
	checkpointID        string
	operationDigest     string
	priorTargetRevision string
	nextTargetRevision  string
	object              TargetObjectRef
	expectedPrior       ExpectedTargetIdentity
	expectedPriorBytes  int64
	expectedPostDigest  string
	expectedPostBytes   int64
	artifacts           recoveryOverwriteArtifactBinding
}

type recoveryCreateParentSnapshot struct {
	path string
	mode os.FileMode
}

type recoveryPreparedCreateParents struct {
	finalPath string
	parents   []recoveryCreateParentSnapshot
}

func NewTargetWritePermit(permit TargetMutationPermit, now time.Time) (TargetWritePermit, error) {
	if permit.validatePurposeAt(now, TargetPurposeWrite) != nil {
		return TargetWritePermit{}, ErrInvalidTargetPermit
	}
	return TargetWritePermit{permit: permit}, nil
}

func (permit TargetWritePermit) ValidateAt(now time.Time) error {
	return permit.permit.validatePurposeAt(now, TargetPurposeWrite)
}

func (permit TargetWritePermit) ValidateObjectAt(now time.Time, object TargetObjectRef) error {
	if permit.ValidateAt(now) != nil || !permit.permit.matchesObject(object) {
		return ErrInvalidTargetPermit
	}
	return nil
}

func issueTargetItemWritePermit(
	permit TargetWritePermit,
	proof targetItemWritePermitProof,
) TargetWritePermit {
	permit.itemProof = nil
	proof.bindingDigest = ""
	if !targetItemWritePermitProofMatches(permit, &proof) {
		return permit
	}
	proof.bindingDigest = targetItemWritePermitProofDigest(permit, &proof)
	if !validDigest(proof.bindingDigest) {
		return permit
	}
	permit.itemProof = &proof
	return permit
}

func targetItemWritePermitProofDigest(
	permit TargetWritePermit,
	proof *targetItemWritePermitProof,
) string {
	if permit.permit.proof == nil || proof == nil {
		return ""
	}
	return framedDigest(
		targetItemWritePermitProofDomain,
		permit.permit.proof.bindingDigest,
		proof.sessionBinding.bindingDigest,
		proof.jobID,
		proof.jobItemID,
		proof.operationDigest,
		string(proof.targetMode),
		proof.object.RootID,
		proof.object.RootLocatorDigest,
		proof.object.TargetPathDigest,
		proof.object.PrivateRelativeLocator,
		string(proof.operation),
		string(proof.expectedPrior.Kind),
		proof.expectedPrior.Digest,
		strconv.FormatInt(proof.expectedPriorBytes, 10),
		proof.expectedDigest,
		strconv.FormatInt(proof.expectedBytes, 10),
		strconv.Itoa(proof.artifacts.keyVersion),
		proof.artifacts.bindingDigest,
		proof.artifacts.token,
		proof.artifacts.intentComponent,
		proof.artifacts.priorComponent,
		proof.artifacts.postComponent,
		proof.artifacts.publishedComponent,
		proof.artifacts.intentDocument,
		proof.artifacts.publishedDocument,
	)
}

func targetItemWritePermitProofMatches(
	permit TargetWritePermit,
	proof *targetItemWritePermitProof,
) bool {
	if permit.permit.proof == nil || permit.permit.proof.validateAt == nil ||
		!validDigest(permit.permit.proof.bindingDigest) || proof == nil ||
		!proof.sessionBinding.valid() || proof.sessionBinding != permit.permit.proof.sessionBinding ||
		!validOpaqueID(proof.jobID) || proof.jobID != permit.permit.JobID ||
		!validOpaqueID(proof.jobItemID) || !validDigest(proof.operationDigest) ||
		proof.targetMode.Validate() != nil || !proof.object.valid() ||
		!permit.permit.matchesObject(proof.object) ||
		proof.sessionBinding.NodeID != permit.permit.NodeID ||
		proof.sessionBinding.RootID != permit.permit.RootID ||
		proof.sessionBinding.RootLocatorDigest != permit.permit.RootLocatorDigest ||
		proof.sessionBinding.RootRevision != permit.permit.RootRevision ||
		validateRecoveryVerifyNamespace(proof.object.PrivateRelativeLocator, proof.jobID, proof.targetMode) != nil ||
		!proof.expectedPrior.valid() || !validDigest(proof.expectedDigest) || proof.expectedBytes < 0 {
		return false
	}
	switch proof.operation {
	case RecoveryOperationCreate:
		return proof.expectedPrior.Kind == ExpectedTargetAbsent && proof.expectedPriorBytes == -1 &&
			proof.artifacts == (recoveryOverwriteArtifactBinding{})
	case RecoveryOperationOverwrite:
		if proof.expectedPrior.Kind != ExpectedTargetPresent || proof.expectedPriorBytes < 0 {
			return false
		}
		if proof.targetMode == TargetModeInPlace {
			return proof.artifacts.valid()
		}
		return proof.targetMode == TargetModeIsolated &&
			proof.artifacts == (recoveryOverwriteArtifactBinding{})
	default:
		return false
	}
}

func (permit TargetWritePermit) validateItemWriteAt(
	now time.Time,
	request TargetWriteAtomicRequest,
) (targetItemWriteAuthority, error) {
	proof := permit.itemProof
	if permit.ValidateObjectAt(now, request.Object) != nil || request.Content == nil ||
		!targetItemWritePermitProofMatches(permit, proof) ||
		proof.bindingDigest != targetItemWritePermitProofDigest(permit, proof) ||
		request.Object != proof.object || request.ExpectedDigest != proof.expectedDigest ||
		request.ExpectedBytes != proof.expectedBytes {
		return targetItemWriteAuthority{}, ErrInvalidTargetPermit
	}
	return targetItemWriteAuthority{
		sessionBinding:     proof.sessionBinding,
		jobID:              proof.jobID,
		jobItemID:          proof.jobItemID,
		operationDigest:    proof.operationDigest,
		targetMode:         proof.targetMode,
		operation:          proof.operation,
		expectedPrior:      proof.expectedPrior,
		expectedPriorBytes: proof.expectedPriorBytes,
		artifacts:          proof.artifacts,
	}, nil
}

func issueTargetFinalizeOverwritePermit(
	permit TargetMutationPermit,
	proof targetFinalizeOverwritePermitProof,
) TargetFinalizeOverwritePermit {
	result := TargetFinalizeOverwritePermit{permit: permit}
	proof.bindingDigest = ""
	if !targetFinalizeOverwritePermitProofMatches(permit, &proof) {
		return result
	}
	proof.bindingDigest = targetFinalizeOverwritePermitProofDigest(permit, &proof)
	if !validDigest(proof.bindingDigest) {
		return result
	}
	result.proof = &proof
	return result
}

func targetFinalizeOverwritePermitProofMatches(
	permit TargetMutationPermit,
	proof *targetFinalizeOverwritePermitProof,
) bool {
	if permit.proof == nil || permit.proof.validateAt == nil ||
		!validDigest(permit.proof.bindingDigest) || proof == nil ||
		!proof.sessionBinding.valid() || proof.sessionBinding != permit.proof.sessionBinding ||
		!validOpaqueID(proof.jobID) || proof.jobID != permit.JobID ||
		!validOpaqueID(proof.jobItemID) || !validOpaqueID(proof.checkpointID) ||
		!validDigest(proof.operationDigest) || !validOpaqueID(proof.checkpointAttemptID) ||
		proof.checkpointAttemptFence == 0 || proof.checkpointNodeFence == 0 ||
		proof.currentAttemptID != permit.AttemptID || proof.currentAttemptFence != permit.AttemptFence ||
		proof.currentNodeLeaseID != permit.NodeLeaseID || proof.currentNodeFence != permit.NodeFence ||
		proof.currentSourceFence.LeaseID == "" || proof.currentSourceFence.RecoveryPointID == "" ||
		proof.currentSourceFence.HolderType != backupasset.LeaseHolderRecoveryJob ||
		proof.currentSourceFence.OwnerID == "" || proof.currentSourceFence.AttemptID != proof.currentAttemptID ||
		proof.currentSourceFence.FenceToken == "" ||
		!validOpaqueRevision(proof.targetChainRevision) ||
		proof.targetChainRevision != permit.ExpectedTargetRevision ||
		!validOpaqueRevision(proof.priorTargetRevision) ||
		!validOpaqueRevision(proof.nextTargetRevision) ||
		proof.nextTargetRevision == proof.priorTargetRevision ||
		!proof.object.valid() || !permit.matchesObject(proof.object) ||
		proof.sessionBinding.NodeID != permit.NodeID ||
		proof.sessionBinding.RootID != permit.RootID ||
		proof.sessionBinding.RootLocatorDigest != permit.RootLocatorDigest ||
		proof.sessionBinding.RootRevision != permit.RootRevision ||
		validateRecoveryVerifyNamespace(
			proof.object.PrivateRelativeLocator, proof.jobID, TargetModeInPlace,
		) != nil || proof.expectedPrior.Kind != ExpectedTargetPresent ||
		!validDigest(proof.expectedPrior.Digest) || proof.expectedPriorBytes < 0 ||
		!validDigest(proof.expectedPostDigest) || proof.expectedPostBytes < 0 ||
		!proof.artifacts.valid() {
		return false
	}
	return true
}

func targetFinalizeOverwritePermitProofDigest(
	permit TargetMutationPermit,
	proof *targetFinalizeOverwritePermitProof,
) string {
	if proof == nil {
		return ""
	}
	return framedDigest(
		targetFinalizeOverwritePermitProofDomain,
		targetMutationPermitProofDigest(permit, proof.sessionBinding),
		proof.sessionBinding.bindingDigest,
		proof.jobID, proof.jobItemID, proof.checkpointID, proof.operationDigest,
		proof.checkpointAttemptID,
		strconv.FormatUint(proof.checkpointAttemptFence, 10),
		strconv.FormatUint(proof.checkpointNodeFence, 10),
		proof.currentAttemptID,
		strconv.FormatUint(proof.currentAttemptFence, 10),
		proof.currentNodeLeaseID,
		strconv.FormatUint(proof.currentNodeFence, 10),
		proof.currentSourceFence.LeaseID,
		proof.currentSourceFence.RecoveryPointID,
		string(proof.currentSourceFence.HolderType),
		proof.currentSourceFence.OwnerID,
		proof.currentSourceFence.AttemptID,
		proof.currentSourceFence.FenceToken,
		proof.targetChainRevision, proof.priorTargetRevision, proof.nextTargetRevision,
		proof.object.RootID, proof.object.RootLocatorDigest,
		proof.object.TargetPathDigest, proof.object.PrivateRelativeLocator,
		string(proof.expectedPrior.Kind), proof.expectedPrior.Digest,
		strconv.FormatInt(proof.expectedPriorBytes, 10),
		proof.expectedPostDigest, strconv.FormatInt(proof.expectedPostBytes, 10),
		strconv.Itoa(proof.artifacts.keyVersion), proof.artifacts.bindingDigest,
		proof.artifacts.token, proof.artifacts.intentComponent, proof.artifacts.priorComponent,
		proof.artifacts.postComponent, proof.artifacts.publishedComponent,
		proof.artifacts.intentDocument, proof.artifacts.publishedDocument,
	)
}

func (permit TargetFinalizeOverwritePermit) authorityAt(
	now time.Time,
	request TargetFinalizeOverwriteRequest,
) (targetFinalizeOverwriteAuthority, error) {
	proof := permit.proof
	if permit.permit.ValidateAt(now) != nil ||
		!targetFinalizeOverwritePermitProofMatches(permit.permit, proof) ||
		proof.bindingDigest != targetFinalizeOverwritePermitProofDigest(permit.permit, proof) ||
		request.Object != proof.object || request.ExpectedDigest != proof.expectedPostDigest ||
		request.ExpectedBytes != proof.expectedPostBytes {
		return targetFinalizeOverwriteAuthority{}, ErrInvalidTargetPermit
	}
	return targetFinalizeOverwriteAuthority{
		sessionBinding: proof.sessionBinding, jobID: proof.jobID,
		jobItemID: proof.jobItemID, checkpointID: proof.checkpointID,
		operationDigest:     proof.operationDigest,
		priorTargetRevision: proof.priorTargetRevision,
		nextTargetRevision:  proof.nextTargetRevision,
		object:              request.Object, expectedPrior: proof.expectedPrior,
		expectedPriorBytes: proof.expectedPriorBytes,
		expectedPostDigest: request.ExpectedDigest,
		expectedPostBytes:  request.ExpectedBytes,
		artifacts:          proof.artifacts,
	}, nil
}

func (permit TargetWritePermit) ValidateOwnedJobDirRequestAt(
	now time.Time,
	request CreateOwnedJobDirRequest,
) error {
	if permit.ValidateObjectAt(now, request.Object) != nil ||
		!validDigest(request.MarkerBindingDigest) ||
		!validRecoveryWorkerID(request.MarkerCreatorID) || request.MarkerCreatorFence == 0 {
		return ErrInvalidTargetPermit
	}
	return nil
}

func (permit TargetWritePermit) ValidateFrozenJobAt(now time.Time, job FrozenJobBinding) error {
	if permit.ValidateAt(now) != nil {
		return ErrInvalidTargetPermit
	}
	return permit.permit.ValidateFrozenJobAt(now, job)
}

type CleanupResourceKind string

const (
	CleanupResourceResultSet CleanupResourceKind = "result_set"
	CleanupResourceWorkspace CleanupResourceKind = "workspace"
)

func (kind CleanupResourceKind) valid() bool {
	switch kind {
	case CleanupResourceResultSet, CleanupResourceWorkspace:
		return true
	default:
		return false
	}
}

type TargetCleanupOperation string

const (
	TargetCleanupValidateOwnedJobDir   TargetCleanupOperation = "validate_owned_job_dir"
	TargetCleanupRemoveOwnedJobDir     TargetCleanupOperation = "remove_owned_job_dir"
	TargetCleanupValidateRemovedJobDir TargetCleanupOperation = "validate_removed_job_dir"
)

type targetCleanupLiveValidator func(context.Context, TargetCleanupPermit) error

type OwnedJobDirRemoval struct {
	Complete       bool
	RemovedEntries int
	ProgressDigest string
}

type OwnedJobDirRemovalValidation struct {
	Object         TargetObjectRef
	RootRevision   string
	TargetRevision string
}

type RecoveryCleanupProgress struct {
	Phase          CleanupPhase
	Complete       bool
	RemovedEntries int
	ProgressDigest string
}

func (RecoveryCleanupProgress) String() string {
	return redactedRecoveryTargetProduct("RecoveryCleanupProgress")
}

func (RecoveryCleanupProgress) GoString() string {
	return redactedRecoveryTargetProduct("RecoveryCleanupProgress")
}

func (TargetCleanupPermit) String() string {
	return redactedRecoveryTargetProduct("TargetCleanupPermit")
}

func (TargetCleanupPermit) GoString() string {
	return redactedRecoveryTargetProduct("TargetCleanupPermit")
}

func (ValidateOwnedJobDirRequest) String() string {
	return redactedRecoveryTargetProduct("ValidateOwnedJobDirRequest")
}

func (ValidateOwnedJobDirRequest) GoString() string {
	return redactedRecoveryTargetProduct("ValidateOwnedJobDirRequest")
}

func (RemoveOwnedJobDirRequest) String() string {
	return redactedRecoveryTargetProduct("RemoveOwnedJobDirRequest")
}

func (RemoveOwnedJobDirRequest) GoString() string {
	return redactedRecoveryTargetProduct("RemoveOwnedJobDirRequest")
}

func (OwnedJobDirValidation) String() string {
	return redactedRecoveryTargetProduct("OwnedJobDirValidation")
}

func (OwnedJobDirValidation) GoString() string {
	return redactedRecoveryTargetProduct("OwnedJobDirValidation")
}

func (OwnedJobDirRemovalValidation) String() string {
	return redactedRecoveryTargetProduct("OwnedJobDirRemovalValidation")
}

func (OwnedJobDirRemovalValidation) GoString() string {
	return redactedRecoveryTargetProduct("OwnedJobDirRemovalValidation")
}

func (OwnedJobDirRemoval) String() string {
	return redactedRecoveryTargetProduct("OwnedJobDirRemoval")
}

func (OwnedJobDirRemoval) GoString() string {
	return redactedRecoveryTargetProduct("OwnedJobDirRemoval")
}

func (operation TargetCleanupOperation) valid() bool {
	switch operation {
	case TargetCleanupValidateOwnedJobDir, TargetCleanupRemoveOwnedJobDir, TargetCleanupValidateRemovedJobDir:
		return true
	default:
		return false
	}
}

type TargetCleanupPermit struct {
	SchemaVersion       int
	Purpose             TargetPurpose
	Operation           TargetCleanupOperation
	ResourceKind        CleanupResourceKind
	ResourceID          string
	JobID               string
	CleanupOwner        string
	CleanupFence        uint64
	CleanupAttempt      uint64
	NodeID              uint
	NodeLeaseID         string
	NodeFence           uint64
	RootID              string
	RootLocatorDigest   string `json:"-"`
	TargetPathDigest    string `json:"-"`
	RootRevision        string
	MarkerBindingDigest string `json:"-"`
	MarkerCreatorID     string `json:"-"`
	MarkerCreatorFence  uint64 `json:"-"`
	UseLatchID          string
	ExpiresAt           time.Time
	proof               *targetCleanupPermitProof
}

type targetCleanupPermitProof struct {
	bindingDigest  string
	sessionBinding recoveryTargetSessionBinding
	validateLive   targetCleanupLiveValidator
}

func targetCleanupPermitBindingDigest(
	permit TargetCleanupPermit,
	sessionBindingDigest string,
) string {
	return framedDigest(
		"xirang/recovery/target-cleanup-permit/v1",
		strconv.Itoa(permit.SchemaVersion), string(permit.Purpose),
		string(permit.Operation), string(permit.ResourceKind),
		permit.ResourceID, permit.JobID, permit.CleanupOwner,
		strconv.FormatUint(permit.CleanupFence, 10),
		strconv.FormatUint(permit.CleanupAttempt, 10),
		strconv.FormatUint(uint64(permit.NodeID), 10), permit.NodeLeaseID,
		strconv.FormatUint(permit.NodeFence, 10), permit.RootID,
		permit.RootLocatorDigest, permit.TargetPathDigest,
		permit.RootRevision, permit.MarkerBindingDigest, permit.MarkerCreatorID,
		strconv.FormatUint(permit.MarkerCreatorFence, 10), permit.UseLatchID,
		permit.ExpiresAt.UTC().Format(time.RFC3339Nano), sessionBindingDigest,
	)
}

func issueTargetCleanupPermit(
	permit TargetCleanupPermit,
	bindings ...recoveryTargetSessionBinding,
) TargetCleanupPermit {
	return issueTargetCleanupPermitWithLiveValidator(permit, nil, bindings...)
}

func issueTargetCleanupPermitWithLiveValidator(
	permit TargetCleanupPermit,
	validateLive targetCleanupLiveValidator,
	bindings ...recoveryTargetSessionBinding,
) TargetCleanupPermit {
	if len(bindings) > 1 {
		permit.proof = nil
		return permit
	}
	var binding recoveryTargetSessionBinding
	if len(bindings) == 1 {
		binding = bindings[0]
	}
	permit.proof = &targetCleanupPermitProof{
		bindingDigest:  targetCleanupPermitBindingDigest(permit, binding.bindingDigest),
		sessionBinding: binding,
		validateLive:   validateLive,
	}
	return permit
}

func (permit TargetCleanupPermit) ValidateAt(now time.Time) error {
	if permit.validateShapeAt(now) != nil || permit.proof == nil ||
		(permit.Operation == TargetCleanupRemoveOwnedJobDir && permit.proof.validateLive == nil) ||
		permit.proof.bindingDigest != targetCleanupPermitBindingDigest(
			permit, permit.proof.sessionBinding.bindingDigest,
		) || (permit.proof.sessionBinding != (recoveryTargetSessionBinding{}) &&
		!permit.proof.sessionBinding.valid()) {
		return ErrInvalidTargetPermit
	}
	return nil
}

func (permit TargetCleanupPermit) validateShapeAt(now time.Time) error {
	if now.IsZero() || permit.SchemaVersion != 1 || permit.Purpose != TargetPurposeCleanup ||
		!permit.Operation.valid() || !permit.ResourceKind.valid() ||
		!validOpaqueID(permit.ResourceID) || !validOpaqueID(permit.JobID) ||
		(permit.ResourceKind == CleanupResourceWorkspace && permit.ResourceID != permit.JobID) ||
		!validRecoveryWorkerID(permit.CleanupOwner) || permit.CleanupFence == 0 || permit.CleanupAttempt == 0 ||
		permit.NodeID == 0 || !validOpaqueID(permit.NodeLeaseID) || permit.NodeFence == 0 ||
		!validBoundedOpaque(permit.RootID, targetRootIDMax) || !validDigest(permit.RootLocatorDigest) ||
		!validDigest(permit.TargetPathDigest) || !validOpaqueRevision(permit.RootRevision) ||
		!validDigest(permit.MarkerBindingDigest) || !validRecoveryWorkerID(permit.MarkerCreatorID) ||
		permit.MarkerCreatorFence == 0 || permit.UseLatchID != RecoverySchemaUseLatchID ||
		!permit.ExpiresAt.After(now) {
		return ErrInvalidTargetPermit
	}
	return nil
}

func (permit TargetCleanupPermit) ValidateOperationObjectAt(
	now time.Time,
	operation TargetCleanupOperation,
	object TargetObjectRef,
) error {
	if permit.ValidateAt(now) != nil || permit.Operation != operation || !permit.matchesObject(object) {
		return ErrInvalidTargetPermit
	}
	return nil
}

func (permit TargetCleanupPermit) ValidateOwnedJobDirRequestAt(
	now time.Time,
	request ValidateOwnedJobDirRequest,
) error {
	if permit.ValidateOperationObjectAt(now, TargetCleanupValidateOwnedJobDir, request.Object) != nil ||
		request.MarkerBindingDigest != permit.MarkerBindingDigest ||
		request.MarkerCreatorID != permit.MarkerCreatorID || request.MarkerCreatorFence != permit.MarkerCreatorFence {
		return ErrInvalidTargetPermit
	}
	return nil
}

type recoveryWorkspaceMarkerBody struct {
	SchemaVersion       int    `json:"schema_version"`
	KeyVersion          int    `json:"key_version"`
	InstallationID      string `json:"installation_id"`
	JobID               string `json:"job_id"`
	RootID              string `json:"root_id"`
	RootRevision        string `json:"root_revision"`
	OwnershipNonce      string `json:"ownership_nonce"`
	MarkerBindingDigest string `json:"marker_binding_digest"`
}

type recoveryWorkspaceMarkerDocument struct {
	SchemaVersion       int    `json:"schema_version"`
	KeyVersion          int    `json:"key_version"`
	InstallationID      string `json:"installation_id"`
	JobID               string `json:"job_id"`
	RootID              string `json:"root_id"`
	RootRevision        string `json:"root_revision"`
	OwnershipNonce      string `json:"ownership_nonce"`
	MarkerBindingDigest string `json:"marker_binding_digest"`
	AuthenticationTag   string `json:"authentication_tag"`
}

func (document recoveryWorkspaceMarkerDocument) body() recoveryWorkspaceMarkerBody {
	return recoveryWorkspaceMarkerBody{
		SchemaVersion: document.SchemaVersion, KeyVersion: document.KeyVersion,
		InstallationID: document.InstallationID, JobID: document.JobID,
		RootID: document.RootID, RootRevision: document.RootRevision,
		OwnershipNonce: document.OwnershipNonce, MarkerBindingDigest: document.MarkerBindingDigest,
	}
}

type recoveryWorkspaceMarkerCodec struct {
	keys   RecoveryWorkspaceKeySource
	random io.Reader
}

func newRecoveryWorkspaceMarkerCodec(
	keys RecoveryWorkspaceKeySource,
	randomSource io.Reader,
) *recoveryWorkspaceMarkerCodec {
	if randomSource == nil {
		randomSource = rand.Reader
	}
	return &recoveryWorkspaceMarkerCodec{keys: keys, random: randomSource}
}

func (codec *recoveryWorkspaceMarkerCodec) EncodeForCreate(
	ctx context.Context,
	permit TargetWritePermit,
	request CreateOwnedJobDirRequest,
	now time.Time,
) ([]byte, error) {
	ctx = recoveryWorkspaceMarkerContext(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if codec == nil || codec.keys == nil || codec.random == nil {
		return nil, ErrRecoveryWorkspaceMarkerUnavailable
	}
	if permit.ValidateOwnedJobDirRequestAt(now, request) != nil {
		return nil, ErrInvalidTargetPermit
	}

	material, err := codec.keys.Active(ctx, backupasset.KeyDomainRecoveryCleanupOwnership)
	if err != nil {
		return nil, recoveryWorkspaceMarkerDependencyError(ctx, err)
	}
	defer clear(material.Key)
	if !validRecoveryWorkspaceMarkerKey(material, material.Version) {
		return nil, ErrRecoveryWorkspaceMarkerUnavailable
	}
	expectedBinding := recoveryWorkspaceMarkerBindingDigest(
		material, permit.permit.JobID, request.Object.RootID, permit.permit.RootRevision,
		request.Object.PrivateRelativeLocator,
		RecoveryWorkerClaim{WorkerID: request.MarkerCreatorID, AttemptFence: request.MarkerCreatorFence},
	)
	if !recoveryWorkspaceMarkerDigestEqual(expectedBinding, request.MarkerBindingDigest) {
		return nil, ErrInvalidTargetPermit
	}

	nonce := make([]byte, recoveryWorkspaceMarkerNonceBytes)
	if _, err := io.ReadFull(codec.random, nonce); err != nil {
		return nil, recoveryWorkspaceMarkerDependencyError(ctx, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	body := recoveryWorkspaceMarkerBody{
		SchemaVersion: recoveryWorkspaceMarkerSchemaVersion, KeyVersion: material.Version,
		InstallationID: recoveryWorkspaceMarkerInstallationID(material.Key),
		JobID:          permit.permit.JobID, RootID: request.Object.RootID,
		RootRevision:        permit.permit.RootRevision,
		OwnershipNonce:      base64.RawURLEncoding.EncodeToString(nonce),
		MarkerBindingDigest: request.MarkerBindingDigest,
	}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return nil, ErrRecoveryWorkspaceMarkerUnavailable
	}
	document := recoveryWorkspaceMarkerDocument{
		SchemaVersion: body.SchemaVersion, KeyVersion: body.KeyVersion,
		InstallationID: body.InstallationID, JobID: body.JobID, RootID: body.RootID,
		RootRevision: body.RootRevision, OwnershipNonce: body.OwnershipNonce,
		MarkerBindingDigest: body.MarkerBindingDigest,
		AuthenticationTag:   hex.EncodeToString(recoveryWorkspaceMarkerDocumentTag(material.Key, bodyBytes)),
	}
	encoded, err := json.Marshal(document)
	if err != nil || len(encoded) == 0 || len(encoded) > recoveryWorkspaceMarkerDocumentMaxBytes {
		return nil, ErrRecoveryWorkspaceMarkerUnavailable
	}
	return encoded, nil
}

func (codec *recoveryWorkspaceMarkerCodec) ValidateForCleanup(
	ctx context.Context,
	permit TargetCleanupPermit,
	request ValidateOwnedJobDirRequest,
	encoded []byte,
	now time.Time,
) error {
	ctx = recoveryWorkspaceMarkerContext(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	if codec == nil || codec.keys == nil {
		return ErrRecoveryWorkspaceMarkerUnavailable
	}
	if permit.ValidateOwnedJobDirRequestAt(now, request) != nil {
		return ErrInvalidTargetPermit
	}
	return codec.validateEncoded(
		ctx, encoded, permit.JobID, request.Object.RootID, permit.RootRevision,
		request.Object.PrivateRelativeLocator, request.MarkerBindingDigest,
		request.MarkerCreatorID, request.MarkerCreatorFence,
	)
}

func (codec *recoveryWorkspaceMarkerCodec) ValidateForCreate(
	ctx context.Context,
	permit TargetWritePermit,
	request CreateOwnedJobDirRequest,
	encoded []byte,
	now time.Time,
) error {
	ctx = recoveryWorkspaceMarkerContext(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	if codec == nil || codec.keys == nil {
		return ErrRecoveryWorkspaceMarkerUnavailable
	}
	if permit.ValidateOwnedJobDirRequestAt(now, request) != nil {
		return ErrInvalidTargetPermit
	}
	return codec.validateEncoded(
		ctx, encoded, permit.permit.JobID, request.Object.RootID, permit.permit.RootRevision,
		request.Object.PrivateRelativeLocator, request.MarkerBindingDigest,
		request.MarkerCreatorID, request.MarkerCreatorFence,
	)
}

func (codec *recoveryWorkspaceMarkerCodec) validateEncoded(
	ctx context.Context,
	encoded []byte,
	jobID string,
	rootID string,
	rootRevision string,
	privateRelativeLocator string,
	markerBindingDigest string,
	markerCreatorID string,
	markerCreatorFence uint64,
) error {
	document, err := decodeRecoveryWorkspaceMarkerDocument(encoded)
	if err != nil || !validRecoveryWorkspaceMarkerDocument(document) {
		return ErrInvalidRecoveryWorkspaceMarker
	}

	material, err := codec.keys.ByVersion(
		ctx, backupasset.KeyDomainRecoveryCleanupOwnership, document.KeyVersion,
	)
	if err != nil {
		return recoveryWorkspaceMarkerDependencyError(ctx, err)
	}
	defer clear(material.Key)
	if !validRecoveryWorkspaceMarkerKey(material, document.KeyVersion) {
		return ErrRecoveryWorkspaceMarkerUnavailable
	}
	expectedBinding := recoveryWorkspaceMarkerBindingDigest(
		material, jobID, rootID, rootRevision, privateRelativeLocator,
		RecoveryWorkerClaim{WorkerID: markerCreatorID, AttemptFence: markerCreatorFence},
	)
	if !recoveryWorkspaceMarkerDigestEqual(expectedBinding, markerBindingDigest) {
		return ErrInvalidRecoveryWorkspaceMarker
	}
	if document.SchemaVersion != recoveryWorkspaceMarkerSchemaVersion || document.KeyVersion != material.Version ||
		document.JobID != jobID || document.RootID != rootID ||
		document.RootRevision != rootRevision ||
		!recoveryWorkspaceMarkerDigestEqual(document.InstallationID, recoveryWorkspaceMarkerInstallationID(material.Key)) ||
		!recoveryWorkspaceMarkerDigestEqual(document.MarkerBindingDigest, markerBindingDigest) {
		return ErrInvalidRecoveryWorkspaceMarker
	}
	bodyBytes, err := json.Marshal(document.body())
	if err != nil {
		return ErrInvalidRecoveryWorkspaceMarker
	}
	expectedTag := recoveryWorkspaceMarkerDocumentTag(material.Key, bodyBytes)
	providedTag, err := hex.DecodeString(document.AuthenticationTag)
	if err != nil || !hmac.Equal(providedTag, expectedTag) {
		return ErrInvalidRecoveryWorkspaceMarker
	}
	return nil
}

func decodeRecoveryWorkspaceMarkerDocument(encoded []byte) (recoveryWorkspaceMarkerDocument, error) {
	if len(encoded) == 0 || len(encoded) > recoveryWorkspaceMarkerDocumentMaxBytes {
		return recoveryWorkspaceMarkerDocument{}, ErrInvalidRecoveryWorkspaceMarker
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return recoveryWorkspaceMarkerDocument{}, ErrInvalidRecoveryWorkspaceMarker
	}
	var document recoveryWorkspaceMarkerDocument
	seen := make(map[string]struct{}, 9)
	for decoder.More() {
		nameToken, tokenErr := decoder.Token()
		name, ok := nameToken.(string)
		if tokenErr != nil || !ok {
			return recoveryWorkspaceMarkerDocument{}, ErrInvalidRecoveryWorkspaceMarker
		}
		if _, duplicate := seen[name]; duplicate {
			return recoveryWorkspaceMarkerDocument{}, ErrInvalidRecoveryWorkspaceMarker
		}
		seen[name] = struct{}{}
		switch name {
		case "schema_version":
			err = decoder.Decode(&document.SchemaVersion)
		case "key_version":
			err = decoder.Decode(&document.KeyVersion)
		case "installation_id":
			err = decoder.Decode(&document.InstallationID)
		case "job_id":
			err = decoder.Decode(&document.JobID)
		case "root_id":
			err = decoder.Decode(&document.RootID)
		case "root_revision":
			err = decoder.Decode(&document.RootRevision)
		case "ownership_nonce":
			err = decoder.Decode(&document.OwnershipNonce)
		case "marker_binding_digest":
			err = decoder.Decode(&document.MarkerBindingDigest)
		case "authentication_tag":
			err = decoder.Decode(&document.AuthenticationTag)
		default:
			return recoveryWorkspaceMarkerDocument{}, ErrInvalidRecoveryWorkspaceMarker
		}
		if err != nil {
			return recoveryWorkspaceMarkerDocument{}, ErrInvalidRecoveryWorkspaceMarker
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') || len(seen) != 9 {
		return recoveryWorkspaceMarkerDocument{}, ErrInvalidRecoveryWorkspaceMarker
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return recoveryWorkspaceMarkerDocument{}, ErrInvalidRecoveryWorkspaceMarker
	}
	return document, nil
}

func validRecoveryWorkspaceMarkerDocument(document recoveryWorkspaceMarkerDocument) bool {
	if document.SchemaVersion != recoveryWorkspaceMarkerSchemaVersion || document.KeyVersion <= 0 ||
		!validDigest(document.InstallationID) || !validOpaqueID(document.JobID) ||
		!validBoundedOpaque(document.RootID, targetRootIDMax) || !validOpaqueRevision(document.RootRevision) ||
		!validDigest(document.MarkerBindingDigest) || !validDigest(document.AuthenticationTag) {
		return false
	}
	nonce, err := base64.RawURLEncoding.DecodeString(document.OwnershipNonce)
	return err == nil && len(nonce) == recoveryWorkspaceMarkerNonceBytes &&
		base64.RawURLEncoding.EncodeToString(nonce) == document.OwnershipNonce
}

type recoveryOwnedCleanupArtifactBody struct {
	SchemaVersion       int    `json:"schema_version"`
	KeyVersion          int    `json:"key_version"`
	JobID               string `json:"job_id"`
	RootID              string `json:"root_id"`
	RootRevision        string `json:"root_revision"`
	WorkspaceLocator    string `json:"workspace_locator"`
	MarkerBindingDigest string `json:"marker_binding_digest"`
	MarkerCreatorID     string `json:"marker_creator_id"`
	MarkerCreatorFence  uint64 `json:"marker_creator_fence"`
	MarkerDigest        string `json:"marker_digest"`
	CapturedComponent   string `json:"captured_component"`
}

type recoveryOwnedCleanupArtifactDocument struct {
	recoveryOwnedCleanupArtifactBody
	AuthenticationTag string `json:"authentication_tag"`
}

type recoveryOwnedCleanupArtifactBinding struct {
	keyVersion        int
	markerDigest      string
	capturedComponent string
	verifiedComponent string
	capturedDocument  string
	verifiedDocument  string
}

func deriveRecoveryOwnedCleanupArtifactBinding(
	material backupasset.DomainKeyMaterial,
	permit TargetCleanupPermit,
	markerDocument recoveryWorkspaceMarkerDocument,
	markerBytes []byte,
) (recoveryOwnedCleanupArtifactBinding, error) {
	if !validRecoveryWorkspaceMarkerKey(material, markerDocument.KeyVersion) ||
		permit.Operation != TargetCleanupRemoveOwnedJobDir || !validOpaqueID(permit.JobID) ||
		!validBoundedOpaque(permit.RootID, targetRootIDMax) || !validOpaqueRevision(permit.RootRevision) ||
		!validDigest(permit.MarkerBindingDigest) || !validRecoveryWorkerID(permit.MarkerCreatorID) ||
		permit.MarkerCreatorFence == 0 || len(markerBytes) == 0 {
		return recoveryOwnedCleanupArtifactBinding{}, ErrInvalidTargetPermit
	}
	markerSum := sha256.Sum256(markerBytes)
	markerDigest := hex.EncodeToString(markerSum[:])
	capturedComponent, verifiedComponent := recoveryOwnedCleanupComponents(
		permit.JobID, permit.RootID, permit.RootRevision, permit.TargetPathDigest,
		permit.MarkerBindingDigest, permit.MarkerCreatorID, permit.MarkerCreatorFence,
	)
	body := recoveryOwnedCleanupArtifactBody{
		SchemaVersion: 1, KeyVersion: markerDocument.KeyVersion, JobID: permit.JobID,
		RootID: permit.RootID, RootRevision: permit.RootRevision,
		WorkspaceLocator:    recoveryWorkspaceLocatorDirectory + "/" + permit.JobID,
		MarkerBindingDigest: permit.MarkerBindingDigest, MarkerCreatorID: permit.MarkerCreatorID,
		MarkerCreatorFence: permit.MarkerCreatorFence, MarkerDigest: markerDigest,
		CapturedComponent: capturedComponent,
	}
	capturedDocument, err := encodeRecoveryOwnedCleanupArtifactDocument(body, material.Key, recoveryOwnedCleanupArtifactDomain)
	if err != nil {
		return recoveryOwnedCleanupArtifactBinding{}, err
	}
	body.CapturedComponent = capturedComponent
	verifiedDocument, err := encodeRecoveryOwnedCleanupArtifactDocument(body, material.Key, recoveryOwnedCleanupVerifiedDomain)
	if err != nil {
		return recoveryOwnedCleanupArtifactBinding{}, err
	}
	return recoveryOwnedCleanupArtifactBinding{
		keyVersion: markerDocument.KeyVersion, markerDigest: markerDigest,
		capturedComponent: capturedComponent, verifiedComponent: verifiedComponent,
		capturedDocument: string(capturedDocument), verifiedDocument: string(verifiedDocument),
	}, nil
}

// recoveryOwnedCleanupCapturedComponent is intentionally derivable from the
// cleanup permit alone. Once the final workspace has been atomically captured,
// the final path is absent and the owner marker is only reachable inside the
// captured sibling. Re-entry must still be able to locate that one candidate
// without adopting arbitrary jobs-directory siblings; the marker digest and
// historical key remain bound by the authenticated artifact documents below.
func recoveryOwnedCleanupCapturedComponent(permit TargetCleanupPermit) string {
	captured, _ := recoveryOwnedCleanupComponents(
		permit.JobID, permit.RootID, permit.RootRevision, permit.TargetPathDigest,
		permit.MarkerBindingDigest, permit.MarkerCreatorID, permit.MarkerCreatorFence,
	)
	return captured
}

func recoveryOwnedCleanupComponents(
	jobID string,
	rootID string,
	rootRevision string,
	targetPathDigest string,
	markerBindingDigest string,
	markerCreatorID string,
	markerCreatorFence uint64,
) (string, string) {
	common := []string{
		jobID, rootID, rootRevision, targetPathDigest, markerBindingDigest, markerCreatorID,
		strconv.FormatUint(markerCreatorFence, 10),
	}
	captured := recoveryOwnedCleanupArtifactPrefix +
		framedDigest(recoveryOwnedCleanupArtifactDomain, common...)
	verified := recoveryOwnedCleanupVerifiedPrefix + framedDigest(
		recoveryOwnedCleanupVerifiedDomain, append(common, captured)...,
	)
	return captured, verified
}

func encodeRecoveryOwnedCleanupArtifactDocument(
	body recoveryOwnedCleanupArtifactBody,
	key []byte,
	domain string,
) ([]byte, error) {
	if len(key) == 0 || body.SchemaVersion != 1 || body.KeyVersion <= 0 ||
		!validOpaqueID(body.JobID) || !validBoundedOpaque(body.RootID, targetRootIDMax) ||
		!validOpaqueRevision(body.RootRevision) || !validTargetRelativeLocator(body.WorkspaceLocator) ||
		!validDigest(body.MarkerBindingDigest) || !validRecoveryWorkerID(body.MarkerCreatorID) ||
		body.MarkerCreatorFence == 0 || !validDigest(body.MarkerDigest) ||
		!strings.HasPrefix(body.CapturedComponent, recoveryOwnedCleanupArtifactPrefix) {
		return nil, ErrInvalidTargetPermit
	}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return nil, ErrRecoveryTargetUnavailable
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(domain))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write(bodyBytes)
	document := recoveryOwnedCleanupArtifactDocument{recoveryOwnedCleanupArtifactBody: body,
		AuthenticationTag: hex.EncodeToString(mac.Sum(nil))}
	encoded, err := json.Marshal(document)
	if err != nil || len(encoded) == 0 || len(encoded) > recoveryWorkspaceMarkerDocumentMaxBytes {
		return nil, ErrRecoveryTargetUnavailable
	}
	return encoded, nil
}

func validateRecoveryOwnedCleanupArtifactDocument(
	encoded []byte,
	expected recoveryOwnedCleanupArtifactBody,
	key []byte,
	domain string,
) error {
	if len(encoded) == 0 || len(encoded) > recoveryWorkspaceMarkerDocumentMaxBytes || len(key) == 0 {
		return ErrRecoveryTargetChanged
	}
	var document recoveryOwnedCleanupArtifactDocument
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil || len(fields) != 12 {
		return ErrRecoveryTargetChanged
	}
	for _, field := range []string{
		"schema_version", "key_version", "job_id", "root_id", "root_revision",
		"workspace_locator", "marker_binding_digest", "marker_creator_id",
		"marker_creator_fence", "marker_digest", "captured_component", "authentication_tag",
	} {
		if _, ok := fields[field]; !ok {
			return ErrRecoveryTargetChanged
		}
	}
	if err := json.Unmarshal(encoded, &document); err != nil {
		return ErrRecoveryTargetChanged
	}
	if document.recoveryOwnedCleanupArtifactBody != expected || document.AuthenticationTag == "" {
		return ErrRecoveryTargetChanged
	}
	bodyBytes, err := json.Marshal(document.recoveryOwnedCleanupArtifactBody)
	if err != nil {
		return ErrRecoveryTargetChanged
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(domain))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write(bodyBytes)
	provided, err := hex.DecodeString(document.AuthenticationTag)
	if err != nil || !hmac.Equal(provided, mac.Sum(nil)) {
		return ErrRecoveryTargetChanged
	}
	return nil
}

func validRecoveryWorkspaceMarkerKey(material backupasset.DomainKeyMaterial, version int) bool {
	return validRecoveryWorkspaceKey(material) && material.Version == version
}

func recoveryWorkspaceMarkerInstallationID(key []byte) string {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(recoveryWorkspaceMarkerInstallationDomain))
	return hex.EncodeToString(mac.Sum(nil))
}

func recoveryWorkspaceMarkerDocumentTag(key, body []byte) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(recoveryWorkspaceMarkerDocumentDomain))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write(body)
	return mac.Sum(nil)
}

func recoveryWorkspaceMarkerDigestEqual(left, right string) bool {
	leftBytes, leftErr := hex.DecodeString(left)
	rightBytes, rightErr := hex.DecodeString(right)
	return leftErr == nil && rightErr == nil && len(leftBytes) == sha256.Size && len(rightBytes) == sha256.Size &&
		hmac.Equal(leftBytes, rightBytes)
}

func recoveryWorkspaceMarkerContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func recoveryWorkspaceMarkerDependencyError(ctx context.Context, err error) error {
	if ctx != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return contextErr
		}
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return ErrRecoveryWorkspaceMarkerUnavailable
}

func (permit TargetCleanupPermit) matchesObject(object TargetObjectRef) bool {
	return object.valid() && permit.RootID == object.RootID &&
		permit.RootLocatorDigest == object.RootLocatorDigest && permit.TargetPathDigest == object.TargetPathDigest
}

// TargetObjectRef is the only target-object addressing product accepted by
// TargetPort. The private relative locator is confined to the target boundary
// and never participates in JSON, logs, audit, or public authority DTOs.
type TargetObjectRef struct {
	RootID                 string `json:"root_id"`
	RootLocatorDigest      string `json:"-"`
	TargetPathDigest       string `json:"-"`
	PrivateRelativeLocator string `json:"-"`
}

func redactedRecoveryTargetProduct(name string) string {
	return "recovery." + name + "{redacted}"
}

func (TargetObjectRef) String() string {
	return redactedRecoveryTargetProduct("TargetObjectRef")
}

func (TargetObjectRef) GoString() string {
	return redactedRecoveryTargetProduct("TargetObjectRef")
}

func (ref TargetObjectRef) valid() bool {
	pathDigest, err := TargetPathDigest(ref.RootID, ref.RootLocatorDigest, ref.PrivateRelativeLocator)
	return err == nil && ref.TargetPathDigest == pathDigest
}

type TargetProbeRequest struct {
	Object               TargetObjectRef
	SourceRevisionDigest string
	CapabilityRevision   string
	PolicyRevision       string
	RequiredBytes        int64
	RequiredInodes       int64
}

type TargetRootProbeFacts struct {
	ObservedAt             time.Time
	ExpiresAt              time.Time
	RootRevision           string
	FilesystemRevision     string
	TargetRevision         string
	CredentialRevision     string
	RequiredToolsAvailable bool
	RootReal               bool
	RootCanonical          bool
	DeviceValid            bool
	MountValid             bool
	OwnerValid             bool
	ModeValid              bool
	HasSymlinkComponent    bool
	FreeBytes              int64
	FreeInodes             int64
	TargetExists           bool
}

type CreateOwnedJobDirRequest struct {
	Object              TargetObjectRef
	MarkerBindingDigest string `json:"-"`
	MarkerCreatorID     string `json:"-"`
	MarkerCreatorFence  uint64 `json:"-"`
}

type OwnedJobDir struct {
	Object              TargetObjectRef
	MarkerBindingDigest string `json:"-"`
	TargetRevision      string
}

type TargetLstatRequest struct {
	Object TargetObjectRef
}

type TargetEntryKind string

const (
	TargetEntryMissing   TargetEntryKind = "missing"
	TargetEntryRegular   TargetEntryKind = "regular"
	TargetEntryDirectory TargetEntryKind = "directory"
	TargetEntrySymlink   TargetEntryKind = "symlink"
	TargetEntrySpecial   TargetEntryKind = "special"
)

type TargetLstatResult struct {
	Kind           TargetEntryKind
	IdentityDigest string
	TargetRevision string
}

type CreateTargetDirectoryRequest struct {
	Object TargetObjectRef
	Mode   uint32
}

type TargetWriteAtomicRequest struct {
	Object         TargetObjectRef
	ExpectedBytes  int64
	ExpectedDigest string
	Content        io.Reader `json:"-"`
}

func (TargetWriteAtomicRequest) String() string {
	return redactedRecoveryTargetProduct("TargetWriteAtomicRequest")
}

func (TargetWriteAtomicRequest) GoString() string {
	return redactedRecoveryTargetProduct("TargetWriteAtomicRequest")
}

type TargetWriteResult struct {
	BytesWritten   int64
	IdentityDigest string
	TargetRevision string
}

type RemoveOwnedJobDirRequest struct {
	Object              TargetObjectRef
	MarkerBindingDigest string `json:"-"`
}

type ValidateOwnedJobDirRequest struct {
	Object              TargetObjectRef
	MarkerBindingDigest string `json:"-"`
	MarkerCreatorID     string `json:"-"`
	MarkerCreatorFence  uint64 `json:"-"`
}

type OwnedJobDirValidation struct {
	Object              TargetObjectRef
	MarkerBindingDigest string `json:"-"`
	RootRevision        string
	TargetRevision      string
}

type OpenOwnedResultRequest struct {
	Object         TargetObjectRef
	ExpectedBytes  int64
	IdentityDigest string
}

type recoverySFTPTarget struct {
	sessions *recoveryTargetSessionFactory
	marker   *recoveryWorkspaceMarkerCodec
	entropy  io.Reader
	now      func() time.Time
}

func newRecoverySFTPTarget(
	resolver recoveryTargetNodeSessionResolver,
	dialer *sshutil.NodeDialer,
	marker *recoveryWorkspaceMarkerCodec,
) (*recoverySFTPTarget, error) {
	if resolver == nil || dialer == nil || marker == nil {
		return nil, ErrRecoveryTargetUnavailable
	}
	return newRecoverySFTPTargetForTest(
		newRecoveryTargetSessionFactory(resolver, dialer), marker,
	), nil
}

func newRecoverySFTPTargetForTest(
	sessions *recoveryTargetSessionFactory,
	marker *recoveryWorkspaceMarkerCodec,
) *recoverySFTPTarget {
	return &recoverySFTPTarget{
		sessions: sessions, marker: marker, entropy: rand.Reader, now: time.Now,
	}
}

func (target *recoverySFTPTarget) ScanRecoveryRoot(
	ctx context.Context,
	permit TargetReconciliationPermit,
	request TargetReconciliationRequest,
) (TargetReconciliationPage, error) {
	if ctx == nil {
		return TargetReconciliationPage{}, ErrInvalidTargetPermit
	}
	if err := ctx.Err(); err != nil {
		return TargetReconciliationPage{}, err
	}
	if target == nil || target.now == nil {
		return TargetReconciliationPage{}, ErrInvalidTargetPermit
	}
	now := target.now().UTC()
	if permit.ValidateRequestAt(now, request) != nil {
		return TargetReconciliationPage{}, ErrInvalidTargetPermit
	}
	cursor, ok := recoveryReconciliationDecodeCursor(permit)
	if !ok {
		return recoveryReconciliationIncompletePage(
			TargetReconciliationPage{Findings: []RecoveryReconciliationFinding{}}, permit, "cursor",
		), nil
	}
	if target.sessions == nil || target.marker == nil || target.marker.keys == nil {
		return TargetReconciliationPage{}, ErrRecoveryTargetUnavailable
	}
	setupKey, err := target.marker.keys.Active(ctx, backupasset.KeyDomainRecoveryCleanupOwnership)
	if err != nil {
		return TargetReconciliationPage{}, recoveryReconciliationSetupError(ctx)
	}
	setupKeyValid := validRecoveryWorkspaceMarkerKey(setupKey, setupKey.Version)
	clear(setupKey.Key)
	if !setupKeyValid {
		return TargetReconciliationPage{}, ErrRecoveryTargetUnavailable
	}

	binding := permit.proof.sessionBinding
	session, err := target.sessions.OpenReconciliation(ctx, binding)
	if err != nil {
		return TargetReconciliationPage{}, recoveryReconciliationSetupError(ctx)
	}
	client := &recoveryResultTrackedSFTPClient{
		recoveryTargetSFTPClient: session.client,
		session:                  session,
	}
	jobsPath := path.Join(binding.rootLocator, recoveryWorkspaceLocatorDirectory)
	if err := validateRecoveryRootPrefixes(client, binding.rootLocator); err != nil {
		_ = session.Close()
		return TargetReconciliationPage{}, recoveryReconciliationSetupError(ctx)
	}
	jobsBefore, err := client.Lstat(jobsPath)
	if os.IsNotExist(err) {
		if cursor.ordinal != 0 {
			page := recoveryReconciliationIncompletePage(
				TargetReconciliationPage{Findings: []RecoveryReconciliationFinding{}}, permit, "prefix",
			)
			_ = session.Close()
			if ctxErr := ctx.Err(); ctxErr != nil {
				return TargetReconciliationPage{}, ctxErr
			}
			return page, nil
		}
		page := recoveryReconciliationFinalizeExpectedAbsence(
			TargetReconciliationPage{Findings: []RecoveryReconciliationFinding{}},
			permit, make([]bool, len(permit.proof.expected)),
		)
		closeErr := session.Close()
		if ctxErr := ctx.Err(); ctxErr != nil {
			return TargetReconciliationPage{}, ctxErr
		}
		if closeErr != nil {
			return recoveryReconciliationIncompletePage(page, permit, "close"), nil
		}
		return page, nil
	}
	if err == nil {
		jobsBefore, err = validateRecoveryCanonicalDirectoryInfo(
			client, jobsPath, jobsBefore, true,
		)
	}
	if err != nil {
		_ = session.Close()
		return TargetReconciliationPage{}, recoveryReconciliationSetupError(ctx)
	}
	directory, err := client.Open(jobsPath)
	if err != nil || directory == nil {
		if directory != nil {
			_ = directory.Close()
		}
		_ = session.Close()
		return TargetReconciliationPage{}, recoveryReconciliationSetupError(ctx)
	}
	jobsOpened, statErr := directory.Stat()
	jobsAfter, afterErr := observeRecoveryCanonicalDirectory(client, jobsPath, true)
	jobsSnapshot := recoverySFTPFileSnapshotOf(jobsBefore)
	if statErr != nil || afterErr != nil || jobsOpened == nil || jobsAfter == nil || !jobsOpened.IsDir() ||
		recoverySFTPFileSnapshotOf(jobsOpened) != jobsSnapshot ||
		recoverySFTPFileSnapshotOf(jobsAfter) != jobsSnapshot {
		_ = directory.Close()
		_ = session.Close()
		return TargetReconciliationPage{}, recoveryReconciliationSetupError(ctx)
	}

	page := target.scanRecoveryRootDirectChildren(ctx, client, directory, jobsPath, permit, cursor)
	directoryCloseErr := directory.Close()
	sessionCloseErr := session.Close()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return TargetReconciliationPage{}, ctxErr
	}
	if directoryCloseErr != nil || sessionCloseErr != nil {
		return recoveryReconciliationIncompletePage(page, permit, "close"), nil
	}
	return page, nil
}

func (target *recoverySFTPTarget) scanRecoveryRootDirectChildren(
	ctx context.Context,
	client recoveryTargetSFTPClient,
	directory recoveryTargetSFTPFile,
	jobsPath string,
	permit TargetReconciliationPermit,
	cursor recoveryReconciliationCursor,
) TargetReconciliationPage {
	page := TargetReconciliationPage{Findings: []RecoveryReconciliationFinding{}}
	matchedExpected := make([]bool, len(permit.proof.expected))
	prefixDigest := recoveryReconciliationInitialPrefixDigest(permit)
	replayVerified := cursor.ordinal == 0
	pageEnd := cursor.ordinal + permit.PageLimit
	if pageEnd > permit.ChainLimit {
		pageEnd = permit.ChainLimit
	}
	for {
		entries, readErr := directory.ReadDir(recoveryCleanupReadBatch)
		for index, entry := range entries {
			if entry == nil {
				return recoveryReconciliationIncompletePage(page, permit, "entry")
			}
			fact, complete := target.scanRecoveryReconciliationEntry(
				ctx, client, jobsPath, entry, permit, &page, matchedExpected,
			)
			if !complete {
				return recoveryReconciliationIncompletePage(page, permit, "entry")
			}
			prefixDigest = recoveryReconciliationAdvancePrefixDigest(
				permit, prefixDigest, page.Counts.Scanned, fact,
			)
			if page.Counts.Scanned == cursor.ordinal {
				if !hmac.Equal(prefixDigest[:], cursor.prefixDigest[:]) {
					return recoveryReconciliationIncompletePage(page, permit, "prefix")
				}
				replayVerified = true
			}
			if page.Counts.Scanned == pageEnd {
				hasMore := index+1 < len(entries)
				if readErr != nil && !errors.Is(readErr, io.EOF) {
					return recoveryReconciliationIncompletePage(page, permit, "read")
				}
				if !hasMore && readErr == nil {
					peek, peekErr := directory.ReadDir(recoveryCleanupReadBatch)
					hasMore = len(peek) > 0
					if peekErr != nil && !errors.Is(peekErr, io.EOF) {
						return recoveryReconciliationIncompletePage(page, permit, "read")
					}
					if !hasMore && !errors.Is(peekErr, io.EOF) {
						return recoveryReconciliationIncompletePage(page, permit, "read")
					}
					readErr = peekErr
				}
				if !hasMore && errors.Is(readErr, io.EOF) {
					if !replayVerified {
						return recoveryReconciliationIncompletePage(page, permit, "prefix")
					}
					return recoveryReconciliationFinalizeExpectedAbsence(page, permit, matchedExpected)
				}
				if page.Counts.Scanned >= permit.ChainLimit {
					return recoveryReconciliationIncompletePage(page, permit, "chain")
				}
				if !replayVerified {
					return recoveryReconciliationIncompletePage(page, permit, "prefix")
				}
				page.NextCursor = recoveryReconciliationEncodeCursor(
					permit, page.Counts.Scanned, prefixDigest,
				)
				if page.NextCursor == "" {
					return recoveryReconciliationIncompletePage(page, permit, "cursor")
				}
				return page
			}
		}
		if errors.Is(readErr, io.EOF) {
			if !replayVerified {
				return recoveryReconciliationIncompletePage(page, permit, "prefix")
			}
			return recoveryReconciliationFinalizeExpectedAbsence(page, permit, matchedExpected)
		}
		if readErr != nil || len(entries) == 0 {
			return recoveryReconciliationIncompletePage(page, permit, "read")
		}
		if ctx.Err() != nil {
			return recoveryReconciliationIncompletePage(page, permit, "context")
		}
	}
}

type recoveryReconciliationEntryFact struct {
	rawName           string
	kind              TargetEntryKind
	category          RecoveryReconciliationCategory
	jobID             string
	observationDigest string
}

func (target *recoverySFTPTarget) scanRecoveryReconciliationEntry(
	ctx context.Context,
	client recoveryTargetSFTPClient,
	jobsPath string,
	entry os.FileInfo,
	permit TargetReconciliationPermit,
	page *TargetReconciliationPage,
	matchedExpected []bool,
) (recoveryReconciliationEntryFact, bool) {
	rawName := entry.Name()
	kind := recoveryReconciliationEntryKind(entry)
	page.Counts.Scanned++
	if !validRecoveryReconciliationDirectChildName(rawName) {
		if !recoveryReconciliationAddFinding(
			page, permit, RecoveryReconciliationForgedOrUnknown, kind, "", rawName,
		) {
			return recoveryReconciliationEntryFact{}, false
		}
		return recoveryReconciliationEntryFact{
			rawName: rawName, kind: kind, category: RecoveryReconciliationForgedOrUnknown,
		}, true
	}
	entryPath := path.Join(jobsPath, rawName)
	observed, err := client.Lstat(entryPath)
	if err != nil || observed == nil {
		return recoveryReconciliationEntryFact{}, false
	}
	kind = recoveryReconciliationEntryKind(observed)
	matched := recoveryReconciliationExpectedMatches(permit, rawName)
	if len(matched) > 1 {
		return recoveryReconciliationEntryFact{}, false
	}
	fact := recoveryReconciliationEntryFact{
		rawName: rawName, kind: kind, category: RecoveryReconciliationForgedOrUnknown,
	}
	if len(matched) == 1 {
		index := matched[0]
		matchedExpected[index] = true
		expected := permit.proof.expected[index]
		fact.jobID = expected.jobID
		healthy, interrupted, observationDigest := target.recoveryReconciliationExpectedEntryHealthy(
			ctx, client, entryPath, rawName, kind, permit, expected,
		)
		if interrupted {
			return recoveryReconciliationEntryFact{}, false
		}
		fact.observationDigest = observationDigest
		if healthy {
			fact.category = RecoveryReconciliationKnownHealthy
			page.Counts.KnownHealthy++
			return fact, true
		}
		fact.category = RecoveryReconciliationKnownDrift
	} else {
		authenticated, interrupted, observationDigest := target.recoveryReconciliationUnknownEntryAuthenticated(
			ctx, client, entryPath, rawName, kind, permit,
		)
		if interrupted {
			return recoveryReconciliationEntryFact{}, false
		}
		fact.observationDigest = observationDigest
		if authenticated {
			fact.category = RecoveryReconciliationDBUnmatched
		}
	}
	if !recoveryReconciliationAddFinding(page, permit, fact.category, kind, fact.jobID, rawName) {
		return recoveryReconciliationEntryFact{}, false
	}
	return fact, true
}

func recoveryReconciliationFinalizeExpectedAbsence(
	page TargetReconciliationPage,
	permit TargetReconciliationPermit,
	matchedExpected []bool,
) TargetReconciliationPage {
	for index, expected := range permit.proof.expected {
		if index < len(matchedExpected) && matchedExpected[index] || expected.entryKind == TargetEntryMissing {
			continue
		}
		if !recoveryReconciliationAddFinding(
			&page, permit, RecoveryReconciliationKnownDrift, TargetEntryMissing,
			expected.jobID, expected.componentToken,
		) {
			return recoveryReconciliationIncompletePage(page, permit, "finding")
		}
	}
	page.Complete = true
	return page
}

func recoveryReconciliationSetupError(ctx context.Context) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return ErrRecoveryTargetUnavailable
}

func validRecoveryReconciliationDirectChildName(value string) bool {
	return value != "" && value != "." && value != ".." && len(value) <= 255 &&
		!strings.ContainsRune(value, 0) && path.Base(value) == value
}

func recoveryReconciliationEntryKind(info os.FileInfo) TargetEntryKind {
	if info == nil {
		return TargetEntrySpecial
	}
	mode := info.Mode()
	switch {
	case mode&os.ModeSymlink != 0:
		return TargetEntrySymlink
	case mode.IsRegular():
		return TargetEntryRegular
	case mode.IsDir():
		return TargetEntryDirectory
	default:
		return TargetEntrySpecial
	}
}

func recoveryReconciliationExpectedMatches(
	permit TargetReconciliationPermit,
	rawName string,
) []int {
	matches := make([]int, 0, 1)
	for index, expected := range permit.proof.expected {
		if !recoveryReconciliationExpectedMayMatchName(expected, rawName) {
			continue
		}
		candidate := recoveryReconciliationComponentToken(
			permit.proof.auditTokenKey, permit.proof.auditKeyVersion,
			permit.proof.sessionBinding, rawName, expected,
		)
		candidateBytes, candidateErr := base64.RawURLEncoding.DecodeString(candidate)
		expectedBytes, expectedErr := base64.RawURLEncoding.DecodeString(expected.componentToken)
		if candidateErr == nil && expectedErr == nil && hmac.Equal(candidateBytes, expectedBytes) {
			matches = append(matches, index)
		}
	}
	return matches
}

func recoveryReconciliationExpectedMayMatchName(
	expected targetReconciliationExpected,
	rawName string,
) bool {
	switch {
	case validOpaqueID(rawName):
		return expected.jobID == rawName
	case validRecoveryReconciliationArtifactComponent(rawName, recoveryOwnedCleanupArtifactPrefix):
		return expected.remoteState == recoveryReconciliationRemoteDeleteStarted &&
			expected.entryKind == TargetEntryDirectory
	case validRecoveryReconciliationArtifactComponent(rawName, recoveryOwnedCleanupVerifiedPrefix):
		return expected.remoteState == recoveryReconciliationRemoteDeleteStarted &&
			expected.entryKind == TargetEntryRegular
	default:
		return false
	}
}

func (target *recoverySFTPTarget) recoveryReconciliationExpectedEntryHealthy(
	ctx context.Context,
	client recoveryTargetSFTPClient,
	entryPath string,
	rawName string,
	kind TargetEntryKind,
	permit TargetReconciliationPermit,
	expected targetReconciliationExpected,
) (bool, bool, string) {
	if kind != expected.entryKind || expected.entryKind == TargetEntryMissing {
		return false, false, ""
	}
	binding := permit.proof.sessionBinding
	workspaceLocator := recoveryWorkspaceLocatorDirectory + "/" + expected.jobID
	targetPathDigest, err := TargetPathDigest(
		binding.rootID, binding.rootLocatorDigest, workspaceLocator,
	)
	if err != nil {
		return false, true, ""
	}
	captured, verified := recoveryOwnedCleanupComponents(
		expected.jobID, binding.rootID, binding.rootRevision, targetPathDigest,
		expected.markerBindingDigest, expected.markerCreatorID, expected.markerCreatorFence,
	)
	switch expected.remoteState {
	case recoveryReconciliationRemoteFinal:
		if expected.entryKind != TargetEntryDirectory || rawName != expected.jobID {
			return false, false, ""
		}
		return target.recoveryReconciliationExpectedWorkspaceMarkerHealthy(
			ctx, client, path.Join(entryPath, recoveryWorkspaceMarkerFileName), permit, expected,
		)
	case recoveryReconciliationRemoteDeleteStarted:
		switch expected.entryKind {
		case TargetEntryDirectory:
			if rawName != captured {
				return false, false, ""
			}
			return target.recoveryReconciliationExpectedWorkspaceMarkerHealthy(
				ctx, client, path.Join(entryPath, recoveryWorkspaceMarkerFileName), permit, expected,
			)
		case TargetEntryRegular:
			if rawName != verified {
				return false, false, ""
			}
			encoded, _, readErr := readRecoveryMarkerFile(client, entryPath)
			if readErr != nil {
				return false, !recoveryReconciliationClassifiedDrift(readErr), ""
			}
			observationDigest := recoveryReconciliationEncodedObservationDigest(encoded)
			document, authenticated, authErr := target.recoveryReconciliationAuthenticateVerifiedArtifact(
				ctx, encoded, rawName, permit,
			)
			if authErr != nil {
				return false, true, observationDigest
			}
			return authenticated && document.JobID == expected.jobID &&
				document.MarkerBindingDigest == expected.markerBindingDigest &&
				document.MarkerCreatorID == expected.markerCreatorID &&
				document.MarkerCreatorFence == expected.markerCreatorFence, false, observationDigest
		default:
			return false, false, ""
		}
	default:
		return false, false, ""
	}
}

func (target *recoverySFTPTarget) recoveryReconciliationExpectedWorkspaceMarkerHealthy(
	ctx context.Context,
	client recoveryTargetSFTPClient,
	markerPath string,
	permit TargetReconciliationPermit,
	expected targetReconciliationExpected,
) (bool, bool, string) {
	encoded, _, err := readRecoveryMarkerFile(client, markerPath)
	if err != nil {
		return false, !recoveryReconciliationClassifiedDrift(err), ""
	}
	observationDigest := recoveryReconciliationEncodedObservationDigest(encoded)
	binding := permit.proof.sessionBinding
	err = target.marker.validateEncoded(
		ctx, encoded, expected.jobID, binding.rootID, binding.rootRevision,
		recoveryWorkspaceLocatorDirectory+"/"+expected.jobID,
		expected.markerBindingDigest, expected.markerCreatorID, expected.markerCreatorFence,
	)
	if err == nil {
		return true, false, observationDigest
	}
	return false, !recoveryReconciliationClassifiedDrift(err), observationDigest
}

func recoveryReconciliationEncodedObservationDigest(encoded []byte) string {
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func recoveryReconciliationClassifiedDrift(err error) bool {
	return errors.Is(err, ErrRecoveryTargetChanged) ||
		errors.Is(err, ErrInvalidRecoveryWorkspaceMarker)
}

func (target *recoverySFTPTarget) recoveryReconciliationUnknownEntryAuthenticated(
	ctx context.Context,
	client recoveryTargetSFTPClient,
	entryPath string,
	rawName string,
	kind TargetEntryKind,
	permit TargetReconciliationPermit,
) (bool, bool, string) {
	switch kind {
	case TargetEntryDirectory:
		if !validOpaqueID(rawName) {
			return false, false, ""
		}
		encoded, _, err := readRecoveryMarkerFile(
			client, path.Join(entryPath, recoveryWorkspaceMarkerFileName),
		)
		if err != nil {
			return false, !recoveryReconciliationClassifiedDrift(err), ""
		}
		observationDigest := recoveryReconciliationEncodedObservationDigest(encoded)
		authenticated, authErr := target.recoveryReconciliationAuthenticateUnknownWorkspaceMarker(
			ctx, encoded, rawName, permit,
		)
		return authenticated, authErr != nil, observationDigest
	case TargetEntryRegular:
		if !validRecoveryReconciliationArtifactComponent(rawName, recoveryOwnedCleanupVerifiedPrefix) {
			return false, false, ""
		}
		encoded, _, err := readRecoveryMarkerFile(client, entryPath)
		if err != nil {
			return false, !recoveryReconciliationClassifiedDrift(err), ""
		}
		observationDigest := recoveryReconciliationEncodedObservationDigest(encoded)
		_, authenticated, authErr := target.recoveryReconciliationAuthenticateVerifiedArtifact(
			ctx, encoded, rawName, permit,
		)
		return authenticated, authErr != nil, observationDigest
	default:
		return false, false, ""
	}
}

func (target *recoverySFTPTarget) recoveryReconciliationAuthenticateUnknownWorkspaceMarker(
	ctx context.Context,
	encoded []byte,
	rawName string,
	permit TargetReconciliationPermit,
) (bool, error) {
	document, err := decodeRecoveryWorkspaceMarkerDocument(encoded)
	if err != nil || !validRecoveryWorkspaceMarkerDocument(document) {
		return false, nil
	}
	material, err := target.marker.keys.ByVersion(
		ctx, backupasset.KeyDomainRecoveryCleanupOwnership, document.KeyVersion,
	)
	if err != nil {
		return false, err
	}
	defer clear(material.Key)
	if !validRecoveryWorkspaceMarkerKey(material, document.KeyVersion) {
		return false, errRecoveryReconciliationIncomplete
	}
	binding := permit.proof.sessionBinding
	if document.JobID != rawName || document.RootID != binding.rootID ||
		document.RootRevision != binding.rootRevision ||
		!recoveryWorkspaceMarkerDigestEqual(
			document.InstallationID, recoveryWorkspaceMarkerInstallationID(material.Key),
		) {
		return false, nil
	}
	bodyBytes, err := json.Marshal(document.body())
	if err != nil {
		return false, errRecoveryReconciliationIncomplete
	}
	provided, err := hex.DecodeString(document.AuthenticationTag)
	if err != nil || !hmac.Equal(provided, recoveryWorkspaceMarkerDocumentTag(material.Key, bodyBytes)) {
		return false, nil
	}
	return true, nil
}

func validRecoveryReconciliationArtifactComponent(value string, prefix string) bool {
	if !strings.HasPrefix(value, prefix) || len(value) != len(prefix)+sha256DigestLength {
		return false
	}
	return validDigest(strings.TrimPrefix(value, prefix))
}

func (target *recoverySFTPTarget) recoveryReconciliationAuthenticateVerifiedArtifact(
	ctx context.Context,
	encoded []byte,
	rawName string,
	permit TargetReconciliationPermit,
) (recoveryOwnedCleanupArtifactBody, bool, error) {
	document, err := decodeRecoveryOwnedCleanupArtifactDocumentForReconciliation(encoded)
	if err != nil {
		return recoveryOwnedCleanupArtifactBody{}, false, nil
	}
	body := document.recoveryOwnedCleanupArtifactBody
	binding := permit.proof.sessionBinding
	if body.SchemaVersion != 1 || body.KeyVersion <= 0 || !validOpaqueID(body.JobID) ||
		body.RootID != binding.rootID || body.RootRevision != binding.rootRevision ||
		body.WorkspaceLocator != recoveryWorkspaceLocatorDirectory+"/"+body.JobID ||
		!validDigest(body.MarkerBindingDigest) || !validRecoveryWorkerID(body.MarkerCreatorID) ||
		body.MarkerCreatorFence == 0 || !validDigest(body.MarkerDigest) {
		return recoveryOwnedCleanupArtifactBody{}, false, nil
	}
	targetPathDigest, err := TargetPathDigest(
		binding.rootID, binding.rootLocatorDigest, body.WorkspaceLocator,
	)
	if err != nil {
		return recoveryOwnedCleanupArtifactBody{}, false, nil
	}
	captured, verified := recoveryOwnedCleanupComponents(
		body.JobID, binding.rootID, binding.rootRevision, targetPathDigest,
		body.MarkerBindingDigest, body.MarkerCreatorID, body.MarkerCreatorFence,
	)
	if body.CapturedComponent != captured || rawName != verified {
		return recoveryOwnedCleanupArtifactBody{}, false, nil
	}
	material, err := target.marker.keys.ByVersion(
		ctx, backupasset.KeyDomainRecoveryCleanupOwnership, body.KeyVersion,
	)
	if err != nil {
		return recoveryOwnedCleanupArtifactBody{}, false, err
	}
	defer clear(material.Key)
	if !validRecoveryWorkspaceMarkerKey(material, body.KeyVersion) {
		return recoveryOwnedCleanupArtifactBody{}, false, errRecoveryReconciliationIncomplete
	}
	if err := validateRecoveryOwnedCleanupArtifactDocument(
		encoded, body, material.Key, recoveryOwnedCleanupVerifiedDomain,
	); err != nil {
		return recoveryOwnedCleanupArtifactBody{}, false, nil
	}
	return body, true, nil
}

func decodeRecoveryOwnedCleanupArtifactDocumentForReconciliation(
	encoded []byte,
) (recoveryOwnedCleanupArtifactDocument, error) {
	if len(encoded) == 0 || len(encoded) > recoveryWorkspaceMarkerDocumentMaxBytes {
		return recoveryOwnedCleanupArtifactDocument{}, ErrRecoveryTargetChanged
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return recoveryOwnedCleanupArtifactDocument{}, ErrRecoveryTargetChanged
	}
	var document recoveryOwnedCleanupArtifactDocument
	seen := make(map[string]struct{}, 12)
	for decoder.More() {
		nameToken, tokenErr := decoder.Token()
		name, ok := nameToken.(string)
		if tokenErr != nil || !ok {
			return recoveryOwnedCleanupArtifactDocument{}, ErrRecoveryTargetChanged
		}
		if _, duplicate := seen[name]; duplicate {
			return recoveryOwnedCleanupArtifactDocument{}, ErrRecoveryTargetChanged
		}
		seen[name] = struct{}{}
		switch name {
		case "schema_version":
			err = decoder.Decode(&document.SchemaVersion)
		case "key_version":
			err = decoder.Decode(&document.KeyVersion)
		case "job_id":
			err = decoder.Decode(&document.JobID)
		case "root_id":
			err = decoder.Decode(&document.RootID)
		case "root_revision":
			err = decoder.Decode(&document.RootRevision)
		case "workspace_locator":
			err = decoder.Decode(&document.WorkspaceLocator)
		case "marker_binding_digest":
			err = decoder.Decode(&document.MarkerBindingDigest)
		case "marker_creator_id":
			err = decoder.Decode(&document.MarkerCreatorID)
		case "marker_creator_fence":
			err = decoder.Decode(&document.MarkerCreatorFence)
		case "marker_digest":
			err = decoder.Decode(&document.MarkerDigest)
		case "captured_component":
			err = decoder.Decode(&document.CapturedComponent)
		case "authentication_tag":
			err = decoder.Decode(&document.AuthenticationTag)
		default:
			return recoveryOwnedCleanupArtifactDocument{}, ErrRecoveryTargetChanged
		}
		if err != nil {
			return recoveryOwnedCleanupArtifactDocument{}, ErrRecoveryTargetChanged
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') || len(seen) != 12 {
		return recoveryOwnedCleanupArtifactDocument{}, ErrRecoveryTargetChanged
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return recoveryOwnedCleanupArtifactDocument{}, ErrRecoveryTargetChanged
	}
	return document, nil
}

func recoveryReconciliationAddFinding(
	page *TargetReconciliationPage,
	permit TargetReconciliationPermit,
	category RecoveryReconciliationCategory,
	kind TargetEntryKind,
	jobID string,
	subject string,
) bool {
	if page == nil || len(page.Findings) >= permit.FindingLimit {
		return false
	}
	finding := RecoveryReconciliationFinding{
		Category: category, Fingerprint: recoveryReconciliationFindingFingerprint(
			permit, category, kind, subject,
		),
		EntryKind: kind, JobID: jobID,
	}
	page.Findings = append(page.Findings, finding)
	switch category {
	case RecoveryReconciliationKnownDrift:
		page.Counts.KnownDrift++
	case RecoveryReconciliationDBUnmatched:
		page.Counts.DBUnmatched++
	case RecoveryReconciliationForgedOrUnknown:
		page.Counts.ForgedOrUnknown++
	case RecoveryReconciliationScanIncomplete:
		page.Counts.ScanIncomplete++
	default:
		return false
	}
	return true
}

func recoveryReconciliationIncompletePage(
	page TargetReconciliationPage,
	permit TargetReconciliationPermit,
	subject string,
) TargetReconciliationPage {
	page.Complete = false
	page.NextCursor = ""
	if page.Findings == nil {
		page.Findings = []RecoveryReconciliationFinding{}
	}
	if page.Counts.ScanIncomplete == 0 {
		if !recoveryReconciliationAddFinding(
			&page, permit, RecoveryReconciliationScanIncomplete, TargetEntryMissing, "", subject,
		) {
			page.Counts.ScanIncomplete = 1
		}
	}
	return page
}

type recoveryReconciliationCursor struct {
	ordinal      int
	prefixDigest [sha256.Size]byte
}

func recoveryReconciliationCursorAuditKeyVersion(value string) (int, bool) {
	if value == "" || len(value) > recoveryReconciliationCursorMax {
		return 0, false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(decoded) < recoveryReconciliationCursorHeaderBytes ||
		base64.RawURLEncoding.EncodeToString(decoded) != value ||
		binary.BigEndian.Uint16(decoded[:2]) != recoveryReconciliationCursorSchemaVersion {
		return 0, false
	}
	version := binary.BigEndian.Uint32(decoded[2:recoveryReconciliationCursorHeaderBytes])
	if version == 0 {
		return 0, false
	}
	return int(version), true
}

func recoveryReconciliationDecodeCursor(
	permit TargetReconciliationPermit,
) (recoveryReconciliationCursor, bool) {
	if permit.Cursor == "" {
		return recoveryReconciliationCursor{}, true
	}
	version, ok := recoveryReconciliationCursorAuditKeyVersion(permit.Cursor)
	if !ok || permit.proof == nil || version != permit.proof.auditKeyVersion {
		return recoveryReconciliationCursor{}, false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(permit.Cursor)
	if err != nil || len(decoded) != recoveryReconciliationCursorWireBytes {
		return recoveryReconciliationCursor{}, false
	}
	tagOffset := recoveryReconciliationCursorWireBytes - recoveryReconciliationCursorDigestBytes
	wantTag := recoveryReconciliationCursorMAC(
		permit.proof.auditTokenKey, "tag", string(decoded[:tagOffset]),
	)
	if !hmac.Equal(decoded[tagOffset:], wantTag[:]) {
		return recoveryReconciliationCursor{}, false
	}
	ordinalOffset := recoveryReconciliationCursorHeaderBytes
	ordinalEnd := ordinalOffset + recoveryReconciliationCursorOrdinalBytes
	ordinal := int(binary.BigEndian.Uint32(decoded[ordinalOffset:ordinalEnd]))
	if ordinal <= 0 || ordinal > permit.ChainLimit || ordinal%permit.PageLimit != 0 {
		return recoveryReconciliationCursor{}, false
	}
	prefixEnd := ordinalEnd + recoveryReconciliationCursorDigestBytes
	scopeEnd := prefixEnd + recoveryReconciliationCursorDigestBytes
	wantScope := recoveryReconciliationCursorScopeDigest(permit)
	if !hmac.Equal(decoded[prefixEnd:scopeEnd], wantScope[:]) {
		return recoveryReconciliationCursor{}, false
	}
	cursor := recoveryReconciliationCursor{ordinal: ordinal}
	copy(cursor.prefixDigest[:], decoded[ordinalEnd:prefixEnd])
	return cursor, true
}

func recoveryReconciliationEncodeCursor(
	permit TargetReconciliationPermit,
	ordinal int,
	prefixDigest [sha256.Size]byte,
) string {
	if permit.proof == nil || permit.proof.auditKeyVersion <= 0 ||
		ordinal <= 0 || ordinal >= permit.ChainLimit || ordinal%permit.PageLimit != 0 {
		return ""
	}
	encoded := make([]byte, recoveryReconciliationCursorWireBytes)
	binary.BigEndian.PutUint16(encoded[:2], recoveryReconciliationCursorSchemaVersion)
	binary.BigEndian.PutUint32(encoded[2:recoveryReconciliationCursorHeaderBytes], uint32(permit.proof.auditKeyVersion))
	ordinalOffset := recoveryReconciliationCursorHeaderBytes
	ordinalEnd := ordinalOffset + recoveryReconciliationCursorOrdinalBytes
	binary.BigEndian.PutUint32(encoded[ordinalOffset:ordinalEnd], uint32(ordinal))
	prefixEnd := ordinalEnd + recoveryReconciliationCursorDigestBytes
	copy(encoded[ordinalEnd:prefixEnd], prefixDigest[:])
	scopeEnd := prefixEnd + recoveryReconciliationCursorDigestBytes
	scopeDigest := recoveryReconciliationCursorScopeDigest(permit)
	copy(encoded[prefixEnd:scopeEnd], scopeDigest[:])
	tag := recoveryReconciliationCursorMAC(
		permit.proof.auditTokenKey, "tag", string(encoded[:scopeEnd]),
	)
	copy(encoded[scopeEnd:], tag[:])
	value := base64.RawURLEncoding.EncodeToString(encoded)
	if len(value) > recoveryReconciliationCursorMax {
		return ""
	}
	return value
}

func recoveryReconciliationCursorScopeDigest(
	permit TargetReconciliationPermit,
) [sha256.Size]byte {
	return recoveryReconciliationCursorMAC(
		permit.proof.auditTokenKey, "scope",
		strconv.Itoa(permit.proof.auditKeyVersion), permit.proof.sessionBinding.bindingDigest,
		permit.ExpectedSetDigest, permit.AdmissionGeneration, strconv.Itoa(permit.PageLimit),
		strconv.Itoa(permit.ChainLimit), strconv.Itoa(permit.FindingLimit),
	)
}

func recoveryReconciliationInitialPrefixDigest(
	permit TargetReconciliationPermit,
) [sha256.Size]byte {
	scopeDigest := recoveryReconciliationCursorScopeDigest(permit)
	return recoveryReconciliationCursorMAC(
		permit.proof.auditTokenKey, "prefix", string(scopeDigest[:]),
	)
}

func recoveryReconciliationAdvancePrefixDigest(
	permit TargetReconciliationPermit,
	prior [sha256.Size]byte,
	ordinal int,
	fact recoveryReconciliationEntryFact,
) [sha256.Size]byte {
	return recoveryReconciliationCursorMAC(
		permit.proof.auditTokenKey, "prefix-entry", string(prior[:]), strconv.Itoa(ordinal),
		fact.rawName, string(fact.kind), string(fact.category), fact.jobID, fact.observationDigest,
	)
}

func recoveryReconciliationCursorMAC(
	key [sha256.Size]byte,
	values ...string,
) [sha256.Size]byte {
	buffer := bytes.NewBuffer(nil)
	writeRecoveryDigestString(buffer, recoveryReconciliationCursorDomain)
	for _, value := range values {
		writeRecoveryDigestString(buffer, value)
	}
	mac := hmac.New(sha256.New, key[:])
	_, _ = mac.Write(buffer.Bytes())
	var digest [sha256.Size]byte
	copy(digest[:], mac.Sum(nil))
	return digest
}

func recoveryReconciliationFindingFingerprint(
	permit TargetReconciliationPermit,
	category RecoveryReconciliationCategory,
	kind TargetEntryKind,
	subject string,
) string {
	buffer := bytes.NewBuffer(nil)
	for _, value := range []string{
		recoveryReconciliationFindingDomain, strconv.Itoa(permit.proof.auditKeyVersion),
		permit.proof.sessionBinding.bindingDigest, string(category), string(kind), subject,
	} {
		writeRecoveryDigestString(buffer, value)
	}
	mac := hmac.New(sha256.New, permit.proof.auditTokenKey[:])
	_, _ = mac.Write(buffer.Bytes())
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (target *recoverySFTPTarget) ProbeRoot(
	ctx context.Context,
	permit TargetPreflightPermit,
	request TargetProbeRequest,
) (TargetRootProbeFacts, error) {
	if target == nil || target.now == nil || ctx == nil {
		return TargetRootProbeFacts{}, ErrInvalidTargetPermit
	}
	if err := ctx.Err(); err != nil {
		return TargetRootProbeFacts{}, err
	}
	now := target.now().UTC()
	if permit.ValidateRequestAt(now, permit.permit, request) != nil {
		return TargetRootProbeFacts{}, ErrInvalidTargetPermit
	}
	if target.sessions == nil {
		return TargetRootProbeFacts{}, ErrRecoveryTargetUnavailable
	}
	binding := permit.proof.sessionBinding
	session, err := target.sessions.OpenPreflight(ctx, binding)
	if err != nil {
		return TargetRootProbeFacts{}, recoveryTargetOperationError(ctx, err)
	}
	facts, operationErr := recoveryProbeRootFacts(ctx, session, binding, permit, now)
	closeErr := session.Close()
	if operationErr != nil {
		return TargetRootProbeFacts{}, recoveryTargetOperationError(ctx, operationErr)
	}
	if closeErr != nil || ctx.Err() != nil {
		return TargetRootProbeFacts{}, recoveryTargetOperationError(ctx, closeErr)
	}
	return facts, nil
}

type recoveryRootProbeObservation struct {
	rootReal            bool
	rootCanonical       bool
	deviceValid         bool
	mountValid          bool
	ownerValid          bool
	modeValid           bool
	hasSymlinkComponent bool
	freeBytes           int64
	freeInodes          int64
	rootRevision        string
	filesystemRevision  string
	targetRevision      string
	targetExists        bool
}

func (observation recoveryRootProbeObservation) sameStableIdentity(
	other recoveryRootProbeObservation,
) bool {
	return observation.rootReal == other.rootReal &&
		observation.rootCanonical == other.rootCanonical &&
		observation.deviceValid == other.deviceValid &&
		observation.mountValid == other.mountValid &&
		observation.ownerValid == other.ownerValid &&
		observation.modeValid == other.modeValid &&
		observation.hasSymlinkComponent == other.hasSymlinkComponent &&
		observation.rootRevision == other.rootRevision &&
		observation.filesystemRevision == other.filesystemRevision &&
		observation.targetRevision == other.targetRevision &&
		observation.targetExists == other.targetExists
}

func recoveryProbeRootFacts(
	ctx context.Context,
	session *recoveryTargetSession,
	binding recoveryTargetPreflightSessionBinding,
	permit TargetPreflightPermit,
	now time.Time,
) (TargetRootProbeFacts, error) {
	if session == nil || session.client == nil || session.commandRunner == nil ||
		!binding.valid() || permit.ValidateAt(now) != nil {
		return TargetRootProbeFacts{}, ErrRecoveryTargetUnavailable
	}
	uid, groups, err := recoveryProbePrincipal(
		ctx, session.commandRunner, permit.permit.ExpiresAt.Sub(now),
	)
	if err != nil {
		return TargetRootProbeFacts{}, err
	}
	first, err := recoveryObserveRootAndTarget(ctx, session.client, binding, uid, groups)
	if err != nil {
		return TargetRootProbeFacts{}, err
	}
	observed, err := recoveryObserveRootAndTarget(ctx, session.client, binding, uid, groups)
	if err != nil {
		return TargetRootProbeFacts{}, err
	}
	if !first.sameStableIdentity(observed) {
		return TargetRootProbeFacts{}, ErrRecoveryTargetChanged
	}
	return TargetRootProbeFacts{
		ObservedAt: now, ExpiresAt: permit.permit.ExpiresAt,
		RootRevision: observed.rootRevision, FilesystemRevision: observed.filesystemRevision,
		TargetRevision: observed.targetRevision, CredentialRevision: binding.credentialRevision,
		RequiredToolsAvailable: true,
		RootReal:               observed.rootReal,
		RootCanonical:          observed.rootCanonical,
		DeviceValid:            observed.deviceValid,
		MountValid:             observed.mountValid,
		OwnerValid:             observed.ownerValid,
		ModeValid:              observed.modeValid,
		HasSymlinkComponent:    observed.hasSymlinkComponent,
		FreeBytes:              observed.freeBytes,
		FreeInodes:             observed.freeInodes,
		TargetExists:           observed.targetExists,
	}, nil
}

func recoveryProbePrincipal(
	ctx context.Context,
	runner *sshutil.CommandRunner,
	timeout time.Duration,
) (uint32, map[uint32]struct{}, error) {
	if runner == nil || timeout <= 0 {
		return 0, nil, ErrRecoveryTargetUnavailable
	}
	commandContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	run := func(argument string) ([]byte, error) {
		result, err := runner.Run(commandContext, sshutil.CommandSpec{
			Binary: "id", Args: []string{argument}, Timeout: timeout,
			MaxStdoutBytes: recoveryPreflightCommandMaxBytes,
			MaxStderrBytes: recoveryPreflightCommandMaxBytes,
			MaxRecordBytes: recoveryPreflightCommandMaxBytes,
		})
		if err != nil {
			return nil, recoveryTargetOperationError(commandContext, err)
		}
		if len(result.Stderr) != 0 {
			return nil, ErrRecoveryTargetUnavailable
		}
		return result.Stdout, nil
	}
	uidOutput, err := run("-u")
	if err != nil {
		return 0, nil, err
	}
	uid, ok := recoveryParsePrincipalIDLine(uidOutput)
	if !ok {
		return 0, nil, ErrRecoveryTargetUnavailable
	}
	groupOutput, err := run("-G")
	if err != nil {
		return 0, nil, err
	}
	groups, ok := recoveryParsePrincipalGroupsLine(groupOutput)
	if !ok {
		return 0, nil, ErrRecoveryTargetUnavailable
	}
	return uid, groups, nil
}

func recoveryParsePrincipalIDLine(value []byte) (uint32, bool) {
	line, ok := recoveryExactDecimalLine(value)
	if !ok || strings.Contains(line, " ") {
		return 0, false
	}
	parsed, err := strconv.ParseUint(line, 10, 32)
	return uint32(parsed), err == nil
}

func recoveryParsePrincipalGroupsLine(value []byte) (map[uint32]struct{}, bool) {
	line, ok := recoveryExactDecimalLine(value)
	if !ok {
		return nil, false
	}
	parts := strings.Split(line, " ")
	groups := make(map[uint32]struct{}, len(parts))
	for _, part := range parts {
		if part == "" {
			return nil, false
		}
		parsed, err := strconv.ParseUint(part, 10, 32)
		if err != nil {
			return nil, false
		}
		group := uint32(parsed)
		if _, duplicate := groups[group]; duplicate {
			return nil, false
		}
		groups[group] = struct{}{}
	}
	return groups, len(groups) > 0
}

func recoveryExactDecimalLine(value []byte) (string, bool) {
	if len(value) < 2 || len(value) > recoveryPreflightCommandMaxBytes ||
		value[len(value)-1] != '\n' || bytes.Count(value, []byte{'\n'}) != 1 {
		return "", false
	}
	line := string(value[:len(value)-1])
	if line == "" {
		return "", false
	}
	for _, character := range line {
		if character != ' ' && (character < '0' || character > '9') {
			return "", false
		}
	}
	return line, true
}

func recoveryObserveRootAndTarget(
	ctx context.Context,
	client recoveryTargetSFTPClient,
	binding recoveryTargetPreflightSessionBinding,
	uid uint32,
	groups map[uint32]struct{},
) (recoveryRootProbeObservation, error) {
	rootLocator := binding.rootLocator
	prefixes, ok := recoveryAbsolutePathPrefixes(rootLocator)
	if client == nil || !binding.valid() || !ok || len(groups) == 0 {
		return recoveryRootProbeObservation{}, ErrRecoveryTargetUnavailable
	}
	observed := recoveryRootProbeObservation{rootReal: true, rootCanonical: true}
	var rootInfo os.FileInfo
	for _, prefix := range prefixes {
		info, err := client.Lstat(prefix)
		if err != nil {
			return recoveryRootProbeObservation{}, recoveryTargetOperationError(ctx, err)
		}
		if info == nil {
			return recoveryRootProbeObservation{}, ErrRecoveryTargetUnavailable
		}
		if info.Mode()&os.ModeSymlink != 0 {
			observed.hasSymlinkComponent = true
		}
		if !info.IsDir() {
			observed.rootReal = false
		}
		realPath, err := client.RealPath(prefix)
		if err != nil {
			return recoveryRootProbeObservation{}, recoveryTargetOperationError(ctx, err)
		}
		if realPath != prefix {
			observed.rootCanonical = false
		}
		if prefix == rootLocator {
			rootInfo = info
		}
	}
	if rootInfo == nil {
		return recoveryRootProbeObservation{}, ErrRecoveryTargetUnavailable
	}
	rootUID, rootGID, ok := recoverySFTPFileOwner(rootInfo)
	if !ok {
		return recoveryRootProbeObservation{}, ErrRecoveryTargetUnavailable
	}
	rootMode := rootInfo.Mode()
	observed.modeValid = rootInfo.IsDir() && rootMode.Perm()&0o002 == 0
	observed.ownerValid = rootInfo.IsDir() && recoveryPrincipalControlsDirectory(
		uid, groups, rootUID, rootGID, rootMode.Perm(),
	)
	rootVFS, err := client.StatVFS(rootLocator)
	if err != nil {
		return recoveryRootProbeObservation{}, recoveryTargetOperationError(ctx, err)
	}
	if rootVFS == nil {
		return recoveryRootProbeObservation{}, ErrRecoveryTargetUnavailable
	}
	parentVFS, err := client.StatVFS(path.Dir(rootLocator))
	if err != nil {
		return recoveryRootProbeObservation{}, recoveryTargetOperationError(ctx, err)
	}
	if parentVFS == nil {
		return recoveryRootProbeObservation{}, ErrRecoveryTargetUnavailable
	}
	observed.deviceValid = rootVFS.Fsid != 0
	observed.mountValid = observed.deviceValid && parentVFS.Fsid == rootVFS.Fsid
	freeBytes, ok := recoveryAvailableBytes(rootVFS.Bavail, rootVFS.Frsize)
	if !ok || rootVFS.Favail > uint64(math.MaxInt64) {
		return recoveryRootProbeObservation{}, ErrRecoveryTargetUnavailable
	}
	observed.freeBytes = freeBytes
	observed.freeInodes = int64(rootVFS.Favail)
	observed.rootRevision, err = recoverySFTPRootObservationRevision(
		binding, rootInfo.Mode(), rootUID, rootGID, rootVFS.Fsid,
	)
	if err != nil {
		return recoveryRootProbeObservation{}, err
	}
	observed.filesystemRevision, err = recoverySFTPFilesystemObservationRevision(rootVFS)
	if err != nil {
		return recoveryRootProbeObservation{}, err
	}
	target, err := recoveryObservePreflightTarget(
		ctx, client, binding, rootVFS.Fsid, observed.rootRevision,
	)
	if err != nil {
		return recoveryRootProbeObservation{}, err
	}
	observed.mountValid = observed.mountValid && target.mountValid
	observed.targetExists = target.exists
	observed.targetRevision = target.revision
	return observed, nil
}

type recoveryPreflightTargetObservation struct {
	exists     bool
	mountValid bool
	revision   string
}

func recoveryObservePreflightTarget(
	ctx context.Context,
	client recoveryTargetSFTPClient,
	binding recoveryTargetPreflightSessionBinding,
	rootFilesystemID uint64,
	rootRevision string,
) (recoveryPreflightTargetObservation, error) {
	if client == nil || !binding.valid() || rootRevision == "" {
		return recoveryPreflightTargetObservation{}, ErrRecoveryTargetUnavailable
	}
	components := strings.Split(binding.privateRelativeLocator, "/")
	if len(components) == 0 {
		return recoveryPreflightTargetObservation{}, ErrRecoveryTargetUnavailable
	}
	current := binding.rootLocator
	mountValid := true
	for index, component := range components {
		current = path.Join(current, component)
		info, err := client.Lstat(current)
		if err != nil {
			if os.IsNotExist(err) {
				revision, revisionErr := recoverySFTPTargetAbsentRevision(
					rootRevision, binding.privateRelativeLocator,
				)
				return recoveryPreflightTargetObservation{
					mountValid: mountValid, revision: revision,
				}, revisionErr
			}
			return recoveryPreflightTargetObservation{}, recoveryTargetOperationError(ctx, err)
		}
		if info == nil {
			return recoveryPreflightTargetObservation{}, ErrRecoveryTargetUnavailable
		}
		if index == len(components)-1 {
			revision, revisionErr := recoverySFTPTargetPresentRevision(
				rootRevision, binding.privateRelativeLocator, info,
			)
			return recoveryPreflightTargetObservation{
				exists: true, mountValid: mountValid, revision: revision,
			}, revisionErr
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return recoveryPreflightTargetObservation{}, ErrRecoveryTargetChanged
		}
		realPath, err := client.RealPath(current)
		if err != nil {
			return recoveryPreflightTargetObservation{}, recoveryTargetOperationError(ctx, err)
		}
		if realPath != current {
			return recoveryPreflightTargetObservation{}, ErrRecoveryTargetChanged
		}
		filesystem, err := client.StatVFS(current)
		if err != nil {
			return recoveryPreflightTargetObservation{}, recoveryTargetOperationError(ctx, err)
		}
		if filesystem == nil {
			return recoveryPreflightTargetObservation{}, ErrRecoveryTargetUnavailable
		}
		if rootFilesystemID == 0 || filesystem.Fsid != rootFilesystemID {
			mountValid = false
		}
	}
	return recoveryPreflightTargetObservation{}, ErrRecoveryTargetUnavailable
}

func recoverySFTPRootObservationRevision(
	binding recoveryTargetPreflightSessionBinding,
	mode os.FileMode,
	uid uint32,
	gid uint32,
	filesystemID uint64,
) (string, error) {
	return recoverySFTPOpaqueObservationRevision(
		recoverySFTPRootRevisionPrefix, recoverySFTPRootObservationDomain,
		strconv.FormatUint(uint64(binding.nodeID), 10), binding.rootID,
		binding.rootLocatorDigest, binding.rootLocator, strconv.FormatUint(uint64(mode), 10),
		strconv.FormatUint(uint64(uid), 10), strconv.FormatUint(uint64(gid), 10),
		strconv.FormatUint(filesystemID, 10),
	)
}

func recoverySFTPFilesystemObservationRevision(filesystem *sftp.StatVFS) (string, error) {
	if filesystem == nil {
		return "", ErrRecoveryTargetUnavailable
	}
	return recoverySFTPOpaqueObservationRevision(
		recoverySFTPFilesystemRevisionPrefix, recoverySFTPFilesystemObservationDomain,
		strconv.FormatUint(filesystem.Fsid, 10), strconv.FormatUint(filesystem.Bsize, 10),
		strconv.FormatUint(filesystem.Frsize, 10), strconv.FormatUint(filesystem.Blocks, 10),
		strconv.FormatUint(filesystem.Files, 10), strconv.FormatUint(filesystem.Flag, 10),
		strconv.FormatUint(filesystem.Namemax, 10),
	)
}

func recoverySFTPTargetAbsentRevision(rootRevision, privateRelativeLocator string) (string, error) {
	return recoverySFTPOpaqueObservationRevision(
		recoverySFTPTargetRevisionPrefix, recoverySFTPTargetObservationDomain,
		rootRevision, privateRelativeLocator, recoverySFTPTargetAbsentKind,
	)
}

func recoverySFTPTargetPresentRevision(
	rootRevision string,
	privateRelativeLocator string,
	info os.FileInfo,
) (string, error) {
	uid, gid, ok := recoverySFTPFileOwner(info)
	if !ok || info.Size() < 0 {
		return "", ErrRecoveryTargetUnavailable
	}
	kind := TargetEntrySpecial
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		kind = TargetEntrySymlink
	case info.IsDir():
		kind = TargetEntryDirectory
	case info.Mode().IsRegular():
		kind = TargetEntryRegular
	}
	return recoverySFTPOpaqueObservationRevision(
		recoverySFTPTargetRevisionPrefix, recoverySFTPTargetObservationDomain,
		rootRevision, privateRelativeLocator, string(kind), strconv.FormatInt(info.Size(), 10),
		strconv.FormatUint(uint64(info.Mode()), 10), strconv.FormatUint(uint64(uid), 10),
		strconv.FormatUint(uint64(gid), 10), strconv.FormatInt(info.ModTime().Unix(), 10),
	)
}

func recoverySFTPOpaqueObservationRevision(
	prefix string,
	domain string,
	values ...string,
) (string, error) {
	encoded := framedDigest(domain, values...)
	raw, err := hex.DecodeString(encoded)
	if err != nil {
		return "", ErrRecoveryTargetUnavailable
	}
	revision := prefix + base64.RawURLEncoding.EncodeToString(raw)
	if len(revision) != len(prefix)+43 || len(revision) > opaqueRevisionMax ||
		!validOpaqueRevision(revision) || sha256Shaped(revision) {
		return "", ErrRecoveryTargetUnavailable
	}
	return revision, nil
}

func recoveryAbsolutePathPrefixes(value string) ([]string, bool) {
	if value == "" || value[0] != '/' || path.Clean(value) != value {
		return nil, false
	}
	if value == "/" {
		return []string{"/"}, true
	}
	parts := strings.Split(strings.TrimPrefix(value, "/"), "/")
	prefixes := make([]string, 0, len(parts)+1)
	prefixes = append(prefixes, "/")
	current := ""
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return nil, false
		}
		current += "/" + part
		prefixes = append(prefixes, current)
	}
	return prefixes, true
}

func recoverySFTPFileOwner(info os.FileInfo) (uint32, uint32, bool) {
	if info == nil {
		return 0, 0, false
	}
	if stat, ok := info.Sys().(*sftp.FileStat); ok && stat != nil {
		return stat.UID, stat.GID, true
	}
	if owned, ok := info.(sftp.FileInfoUidGid); ok {
		return owned.Uid(), owned.Gid(), true
	}
	return 0, 0, false
}

type recoveryDeleteEntryObservation struct {
	result      TargetLstatResult
	size        int64
	mode        os.FileMode
	uid         uint32
	gid         uint32
	mtime       int64
	payloadFact string
}

func observeRecoveryDeleteEntryTwice(
	client recoveryTargetSFTPClient,
	binding recoveryTargetSessionBinding,
	jobID string,
	mode TargetMode,
	object TargetObjectRef,
) (TargetLstatResult, error) {
	observation, err := observeRecoveryDeleteEntryObservationTwice(
		client, binding, jobID, mode, object,
	)
	if err != nil {
		return TargetLstatResult{}, err
	}
	return observation.result, nil
}

func observeRecoveryDeleteEntryObservationTwice(
	client recoveryTargetSFTPClient,
	binding recoveryTargetSessionBinding,
	jobID string,
	mode TargetMode,
	object TargetObjectRef,
) (recoveryDeleteEntryObservation, error) {
	first, err := observeRecoveryDeleteEntry(client, binding, jobID, mode, object)
	if err != nil {
		return recoveryDeleteEntryObservation{}, err
	}
	second, err := observeRecoveryDeleteEntry(client, binding, jobID, mode, object)
	if err != nil {
		return recoveryDeleteEntryObservation{}, err
	}
	if first != second {
		return recoveryDeleteEntryObservation{}, ErrRecoveryTargetChanged
	}
	return first, nil
}

func observeRecoveryDeleteEntry(
	client recoveryTargetSFTPClient,
	binding recoveryTargetSessionBinding,
	jobID string,
	mode TargetMode,
	object TargetObjectRef,
) (recoveryDeleteEntryObservation, error) {
	finalPath, err := validateRecoveryVerifyCanonicalParents(client, binding, jobID, mode, object)
	if err != nil {
		return recoveryDeleteEntryObservation{}, err
	}
	info, err := client.Lstat(finalPath)
	if err != nil {
		if os.IsNotExist(err) {
			if _, validationErr := validateRecoveryVerifyCanonicalParents(
				client, binding, jobID, mode, object,
			); validationErr != nil {
				return recoveryDeleteEntryObservation{}, validationErr
			}
			revision, revisionErr := recoverySFTPTargetAbsentRevision(
				binding.RootRevision, object.PrivateRelativeLocator,
			)
			if revisionErr != nil {
				return recoveryDeleteEntryObservation{}, revisionErr
			}
			return recoveryDeleteEntryObservation{result: TargetLstatResult{
				Kind: TargetEntryMissing, TargetRevision: revision,
			}}, nil
		}
		return recoveryDeleteEntryObservation{}, ErrRecoveryTargetUnavailable
	}
	observation, err := recoveryDeleteEntryMetadata(info)
	if err != nil {
		return recoveryDeleteEntryObservation{}, err
	}
	beforeSnapshot := recoverySFTPFileSnapshotOf(info)
	switch observation.result.Kind {
	case TargetEntryRegular:
		identity, bytesRead, readSnapshot, readErr := readRecoveryPresentRegularFile(
			client, finalPath, PresentExpectation{Bytes: observation.size},
		)
		if readErr != nil {
			return recoveryDeleteEntryObservation{}, readErr
		}
		if bytesRead != observation.size || readSnapshot != beforeSnapshot {
			return recoveryDeleteEntryObservation{}, ErrRecoveryTargetChanged
		}
		observation.payloadFact = identity
	case TargetEntrySymlink:
		linkTarget, readErr := client.ReadLink(finalPath)
		if readErr != nil {
			return recoveryDeleteEntryObservation{}, ErrRecoveryTargetUnavailable
		}
		observation.payloadFact = linkTarget
	case TargetEntryDirectory, TargetEntrySpecial:
	default:
		return recoveryDeleteEntryObservation{}, ErrRecoveryTargetUnavailable
	}
	after, err := client.Lstat(finalPath)
	if err != nil {
		if os.IsNotExist(err) {
			return recoveryDeleteEntryObservation{}, ErrRecoveryTargetChanged
		}
		return recoveryDeleteEntryObservation{}, ErrRecoveryTargetUnavailable
	}
	afterObservation, err := recoveryDeleteEntryMetadata(after)
	if err != nil {
		return recoveryDeleteEntryObservation{}, err
	}
	if afterObservation.size != observation.size || afterObservation.mode != observation.mode ||
		afterObservation.uid != observation.uid || afterObservation.gid != observation.gid ||
		afterObservation.mtime != observation.mtime ||
		afterObservation.result.Kind != observation.result.Kind {
		return recoveryDeleteEntryObservation{}, ErrRecoveryTargetChanged
	}
	if _, err := validateRecoveryVerifyCanonicalParents(client, binding, jobID, mode, object); err != nil {
		return recoveryDeleteEntryObservation{}, err
	}
	revision, err := recoverySFTPTargetPresentRevision(
		binding.RootRevision, object.PrivateRelativeLocator, info,
	)
	if err != nil {
		return recoveryDeleteEntryObservation{}, err
	}
	observation.result.TargetRevision = revision
	observation.result.IdentityDigest = recoveryDeleteEntryIdentityDigest(
		binding.RootRevision, object.PrivateRelativeLocator, observation,
	)
	if !validDigest(observation.result.IdentityDigest) {
		return recoveryDeleteEntryObservation{}, ErrRecoveryTargetUnavailable
	}
	return observation, nil
}

func recoveryDeleteEntryIdentityDigest(
	rootRevision string,
	privateRelativeLocator string,
	observation recoveryDeleteEntryObservation,
) string {
	return framedDigest(
		recoverySFTPDeleteEntryIdentityDomain,
		rootRevision, privateRelativeLocator, string(observation.result.Kind),
		strconv.FormatInt(observation.size, 10), strconv.FormatUint(uint64(observation.mode), 10),
		strconv.FormatUint(uint64(observation.uid), 10), strconv.FormatUint(uint64(observation.gid), 10),
		strconv.FormatInt(observation.mtime, 10), observation.payloadFact,
	)
}

func recoveryDeleteEntryMetadata(info os.FileInfo) (recoveryDeleteEntryObservation, error) {
	uid, gid, ok := recoverySFTPFileOwner(info)
	if info == nil || !ok || info.Size() < 0 {
		return recoveryDeleteEntryObservation{}, ErrRecoveryTargetUnavailable
	}
	kind := TargetEntrySpecial
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		kind = TargetEntrySymlink
	case info.IsDir():
		kind = TargetEntryDirectory
	case info.Mode().IsRegular():
		kind = TargetEntryRegular
	}
	return recoveryDeleteEntryObservation{
		result: TargetLstatResult{Kind: kind},
		size:   info.Size(), mode: info.Mode(), uid: uid, gid: gid, mtime: info.ModTime().Unix(),
	}, nil
}

type recoveryDeleteArtifactPaths struct {
	final    string
	intent   string
	captured string
	verified string
}

func recoveryDeleteArtifactPathsFor(
	finalPath string,
	artifacts recoveryDeleteArtifactBinding,
) (recoveryDeleteArtifactPaths, error) {
	if finalPath == "" || path.Clean(finalPath) != finalPath || !artifacts.valid() {
		return recoveryDeleteArtifactPaths{}, ErrInvalidTargetPermit
	}
	parent := path.Dir(finalPath)
	paths := recoveryDeleteArtifactPaths{
		final:    finalPath,
		intent:   path.Join(parent, artifacts.intentComponent),
		captured: path.Join(parent, artifacts.capturedComponent),
		verified: path.Join(parent, artifacts.verifiedComponent),
	}
	seen := map[string]struct{}{finalPath: {}}
	for component, value := range map[string]string{
		artifacts.intentComponent:   paths.intent,
		artifacts.capturedComponent: paths.captured,
		artifacts.verifiedComponent: paths.verified,
	} {
		if path.Base(component) != component || path.Dir(value) != parent ||
			path.Base(value) != component || path.Clean(value) != value {
			return recoveryDeleteArtifactPaths{}, ErrInvalidTargetPermit
		}
		if _, exists := seen[value]; exists {
			return recoveryDeleteArtifactPaths{}, ErrInvalidTargetPermit
		}
		seen[value] = struct{}{}
	}
	return paths, nil
}

type recoveryTargetSFTPDirectory interface {
	Readdir(int) ([]os.FileInfo, error)
}

func verifyRecoveryDeleteCapturedDirectoryEmpty(
	client recoveryTargetSFTPClient,
	value string,
	expected recoveryDeleteEntryObservation,
) error {
	if client == nil || value == "" || expected.result.Kind != TargetEntryDirectory {
		return ErrInvalidTargetPermit
	}
	before, err := client.Lstat(value)
	if err != nil {
		if os.IsNotExist(err) {
			return ErrRecoveryTargetChanged
		}
		return ErrRecoveryTargetUnavailable
	}
	beforeObservation, err := recoveryDeleteEntryMetadata(before)
	if err != nil || !sameRecoveryCapturedEntry(expected, beforeObservation) {
		if err != nil {
			return err
		}
		return ErrRecoveryTargetChanged
	}
	file, err := client.Open(value)
	if err != nil {
		if file != nil {
			_ = file.Close()
		}
		if os.IsNotExist(err) {
			return ErrRecoveryTargetChanged
		}
		return ErrRecoveryTargetUnavailable
	}
	opened, statErr := file.Stat()
	directory, ok := file.(recoveryTargetSFTPDirectory)
	if statErr != nil || opened == nil || !ok || !opened.IsDir() ||
		recoverySFTPFileSnapshotOf(opened) != recoverySFTPFileSnapshotOf(before) {
		_ = file.Close()
		if statErr != nil || !ok {
			return ErrRecoveryTargetUnavailable
		}
		return ErrRecoveryTargetChanged
	}
	entries, readErr := directory.Readdir(1)
	closeErr := file.Close()
	if closeErr != nil {
		return ErrRecoveryTargetUnavailable
	}
	if len(entries) != 0 {
		return ErrRecoveryTargetChanged
	}
	if !errors.Is(readErr, io.EOF) {
		return ErrRecoveryTargetUnavailable
	}
	after, err := client.Lstat(value)
	if err != nil {
		if os.IsNotExist(err) {
			return ErrRecoveryTargetChanged
		}
		return ErrRecoveryTargetUnavailable
	}
	afterObservation, err := recoveryDeleteEntryMetadata(after)
	if err != nil || !sameRecoveryCapturedEntry(expected, afterObservation) {
		if err != nil {
			return err
		}
		return ErrRecoveryTargetChanged
	}
	return nil
}

func observeRecoveryDeleteCapturedEntry(
	client recoveryTargetSFTPClient,
	binding recoveryTargetSessionBinding,
	jobID string,
	value string,
	expectedObject TargetObjectRef,
) (recoveryDeleteEntryObservation, error) {
	if !expectedObject.valid() || expectedObject.RootID != binding.RootID ||
		expectedObject.RootLocatorDigest != binding.RootLocatorDigest {
		return recoveryDeleteEntryObservation{}, ErrInvalidTargetPermit
	}
	object, err := recoveryOverwriteArtifactObject(binding, value)
	if err != nil {
		return recoveryDeleteEntryObservation{}, err
	}
	observation, err := observeRecoveryDeleteEntryObservationTwice(
		client, binding, jobID, TargetModeInPlace, object,
	)
	if err != nil {
		return observation, err
	}
	if observation.result.Kind == TargetEntryDirectory {
		for pass := 0; pass < 2; pass++ {
			if err := verifyRecoveryDeleteCapturedDirectoryEmpty(client, value, observation); err != nil {
				return recoveryDeleteEntryObservation{}, err
			}
		}
	}
	if observation.result.Kind != TargetEntryMissing {
		observation.result.IdentityDigest = recoveryDeleteEntryIdentityDigest(
			binding.RootRevision, expectedObject.PrivateRelativeLocator, observation,
		)
		if !validDigest(observation.result.IdentityDigest) {
			return recoveryDeleteEntryObservation{}, ErrRecoveryTargetUnavailable
		}
	}
	return observation, nil
}

func observeRecoveryDeleteMarker(
	client recoveryTargetSFTPClient,
	binding recoveryTargetSessionBinding,
	jobID string,
	value string,
) (recoveryDeleteMarkerObservation, error) {
	object, err := recoveryOverwriteArtifactObject(binding, value)
	if err != nil {
		return recoveryDeleteMarkerObservation{}, err
	}
	entry, err := observeRecoveryDeleteEntryObservationTwice(
		client, binding, jobID, TargetModeInPlace, object,
	)
	if err != nil || entry.result.Kind == TargetEntryMissing {
		return recoveryDeleteMarkerObservation{entry: entry}, err
	}
	if entry.result.Kind != TargetEntryRegular {
		return recoveryDeleteMarkerObservation{entry: entry}, nil
	}
	encoded, _, err := readRecoveryMarkerFile(client, value)
	if err != nil {
		return recoveryDeleteMarkerObservation{}, err
	}
	if len(encoded) > recoveryDeleteMarkerDocumentMaxBytes {
		return recoveryDeleteMarkerObservation{}, ErrRecoveryTargetChanged
	}
	return recoveryDeleteMarkerObservation{entry: entry, document: string(encoded)}, nil
}

func observeRecoveryDeleteTuple(
	client recoveryTargetSFTPClient,
	authority targetDeletePermitProof,
	paths recoveryDeleteArtifactPaths,
) (recoveryDeleteTupleObservation, error) {
	if client == nil || !authority.sessionBinding.valid() || !authority.object.valid() ||
		authority.targetMode != TargetModeInPlace || !authority.artifacts.valid() {
		return recoveryDeleteTupleObservation{}, ErrInvalidTargetPermit
	}
	var tuple recoveryDeleteTupleObservation
	var err error
	tuple.final, err = observeRecoveryDeleteEntryObservationTwice(
		client, authority.sessionBinding, authority.jobID, TargetModeInPlace, authority.object,
	)
	if err != nil {
		return recoveryDeleteTupleObservation{}, err
	}
	tuple.intent, err = observeRecoveryDeleteMarker(
		client, authority.sessionBinding, authority.jobID, paths.intent,
	)
	if err != nil {
		return recoveryDeleteTupleObservation{}, err
	}
	tuple.captured, err = observeRecoveryDeleteCapturedEntry(
		client, authority.sessionBinding, authority.jobID, paths.captured, authority.object,
	)
	if err != nil {
		return recoveryDeleteTupleObservation{}, err
	}
	tuple.verified, err = observeRecoveryDeleteMarker(
		client, authority.sessionBinding, authority.jobID, paths.verified,
	)
	if err != nil {
		return recoveryDeleteTupleObservation{}, err
	}
	return tuple, nil
}

func validateRecoveryDeleteMutationAuthority(
	client recoveryTargetSFTPClient,
	authority targetDeletePermitProof,
	paths recoveryDeleteArtifactPaths,
	validateLive func() error,
) error {
	if validateLive == nil {
		return ErrRecoveryTargetUnavailable
	}
	finalPath, err := validateRecoveryVerifyCanonicalParents(
		client, authority.sessionBinding, authority.jobID, TargetModeInPlace, authority.object,
	)
	if err != nil {
		return err
	}
	if finalPath != paths.final {
		return ErrInvalidTargetPermit
	}
	return validateLive()
}

func requireRecoveryDeleteTupleState(
	client recoveryTargetSFTPClient,
	material backupasset.DomainKeyMaterial,
	authority targetDeletePermitProof,
	paths recoveryDeleteArtifactPaths,
	want recoveryDeleteTupleState,
) (recoveryDeleteTupleObservation, error) {
	tuple, err := observeRecoveryDeleteTuple(client, authority, paths)
	if err != nil {
		return recoveryDeleteTupleObservation{}, err
	}
	classification := classifyRecoveryDeleteTuple(material, authority, tuple)
	if classification.state != want {
		return recoveryDeleteTupleObservation{}, ErrRecoveryTargetChanged
	}
	return tuple, nil
}

func createRecoveryDeleteIntent(
	client recoveryTargetSFTPClient,
	material backupasset.DomainKeyMaterial,
	authority targetDeletePermitProof,
	paths recoveryDeleteArtifactPaths,
	validateLive func() error,
) error {
	if _, err := requireRecoveryDeleteTupleState(
		client, material, authority, paths, recoveryDeleteTupleFresh,
	); err != nil {
		return err
	}
	if err := validateRecoveryDeleteMutationAuthority(
		client, authority, paths, validateLive,
	); err != nil {
		return err
	}
	document := []byte(authority.artifacts.intentDocument)
	digest := sha256.Sum256(document)
	if err := createRecoveryOverwriteExactArtifact(
		client, paths.intent, bytes.NewReader(document), PresentExpectation{
			IdentityDigest: hex.EncodeToString(digest[:]), Bytes: int64(len(document)),
		},
	); err != nil {
		return err
	}
	_, err := requireRecoveryDeleteTupleState(
		client, material, authority, paths, recoveryDeleteTupleIntent,
	)
	return err
}

func restoreRecoveryDeleteCapturedMismatch(
	client recoveryTargetSFTPClient,
	authority targetDeletePermitProof,
	paths recoveryDeleteArtifactPaths,
	captured recoveryDeleteEntryObservation,
	validateLive func() error,
) error {
	if err := validateRecoveryFinalAbsent(client, paths.final); err != nil {
		return err
	}
	current, err := observeRecoveryDeleteCapturedEntry(
		client, authority.sessionBinding, authority.jobID, paths.captured, authority.object,
	)
	if err != nil {
		return err
	}
	if !sameRecoveryCapturedEntry(captured, current) {
		return ErrRecoveryTargetChanged
	}
	if err := validateRecoveryDeleteMutationAuthority(
		client, authority, paths, validateLive,
	); err != nil {
		return err
	}
	if err := validateRecoveryFinalAbsent(client, paths.final); err != nil {
		return err
	}
	if err := client.Rename(paths.captured, paths.final); err != nil {
		return ErrRecoveryTargetUnavailable
	}
	restored, err := observeRecoveryDeleteEntryObservationTwice(
		client, authority.sessionBinding, authority.jobID, TargetModeInPlace, authority.object,
	)
	if err != nil {
		return err
	}
	if !sameRecoveryCapturedEntry(captured, restored) {
		return ErrRecoveryTargetChanged
	}
	missing, err := recoveryOverwritePathMissing(client, paths.captured)
	if err != nil {
		return err
	}
	if !missing {
		return ErrRecoveryTargetChanged
	}
	return ErrRecoveryTargetChanged
}

func captureRecoveryDeleteMutationInstantObject(
	client recoveryTargetSFTPClient,
	material backupasset.DomainKeyMaterial,
	authority targetDeletePermitProof,
	paths recoveryDeleteArtifactPaths,
	validateLive func() error,
) error {
	if _, err := requireRecoveryDeleteTupleState(
		client, material, authority, paths, recoveryDeleteTupleIntent,
	); err != nil {
		return err
	}
	if err := validateRecoveryDeleteMutationAuthority(
		client, authority, paths, validateLive,
	); err != nil {
		return err
	}
	if err := client.Rename(paths.final, paths.captured); err != nil {
		return ErrRecoveryTargetUnavailable
	}
	captured, err := observeRecoveryDeleteCapturedEntry(
		client, authority.sessionBinding, authority.jobID, paths.captured, authority.object,
	)
	if err != nil {
		return err
	}
	if !exactRecoveryDeletePriorObservation(
		captured, authority.expectedPrior, authority.expectedPriorBytes,
	) {
		return restoreRecoveryDeleteCapturedMismatch(
			client, authority, paths, captured, validateLive,
		)
	}
	if err := validateRecoveryFinalAbsent(client, paths.final); err != nil {
		return err
	}
	_, err = requireRecoveryDeleteTupleState(
		client, material, authority, paths, recoveryDeleteTupleCaptured,
	)
	return err
}

func requireRecoveryDeleteTupleClassification(
	client recoveryTargetSFTPClient,
	material backupasset.DomainKeyMaterial,
	authority targetDeletePermitProof,
	paths recoveryDeleteArtifactPaths,
	wantState recoveryDeleteTupleState,
	wantTransition recoveryDeleteTupleTransition,
) (recoveryDeleteTupleObservation, error) {
	tuple, err := observeRecoveryDeleteTuple(client, authority, paths)
	if err != nil {
		return recoveryDeleteTupleObservation{}, err
	}
	classification := classifyRecoveryDeleteTuple(material, authority, tuple)
	if classification.state != wantState || classification.transition != wantTransition {
		return recoveryDeleteTupleObservation{}, ErrRecoveryTargetChanged
	}
	return tuple, nil
}

func createRecoveryDeleteVerified(
	client recoveryTargetSFTPClient,
	material backupasset.DomainKeyMaterial,
	authority targetDeletePermitProof,
	paths recoveryDeleteArtifactPaths,
	validateLive func() error,
) error {
	if _, err := requireRecoveryDeleteTupleClassification(
		client, material, authority, paths,
		recoveryDeleteTupleCaptured, recoveryDeleteTupleVerifyCaptured,
	); err != nil {
		return err
	}
	if err := validateRecoveryDeleteMutationAuthority(
		client, authority, paths, validateLive,
	); err != nil {
		return err
	}
	document := []byte(authority.artifacts.verifiedDocument)
	digest := sha256.Sum256(document)
	if err := createRecoveryOverwriteExactArtifact(
		client, paths.verified, bytes.NewReader(document), PresentExpectation{
			IdentityDigest: hex.EncodeToString(digest[:]), Bytes: int64(len(document)),
		},
	); err != nil {
		return err
	}
	_, err := requireRecoveryDeleteTupleClassification(
		client, material, authority, paths,
		recoveryDeleteTupleVerified, recoveryDeleteTupleDeleteCaptured,
	)
	return err
}

func deleteRecoveryVerifiedCapturedEntry(
	client recoveryTargetSFTPClient,
	material backupasset.DomainKeyMaterial,
	authority targetDeletePermitProof,
	paths recoveryDeleteArtifactPaths,
	validateLive func() error,
) error {
	tuple, err := requireRecoveryDeleteTupleClassification(
		client, material, authority, paths,
		recoveryDeleteTupleVerified, recoveryDeleteTupleDeleteCaptured,
	)
	if err != nil {
		return err
	}
	if err := validateRecoveryDeleteMutationAuthority(
		client, authority, paths, validateLive,
	); err != nil {
		return err
	}
	switch tuple.captured.result.Kind {
	case TargetEntryDirectory:
		err = client.RemoveDirectory(paths.captured)
	case TargetEntryRegular, TargetEntrySymlink, TargetEntrySpecial:
		err = client.Remove(paths.captured)
	default:
		return ErrRecoveryTargetChanged
	}
	after, observeErr := observeRecoveryDeleteTuple(client, authority, paths)
	if observeErr != nil {
		return observeErr
	}
	classification := classifyRecoveryDeleteTuple(material, authority, after)
	if classification.state == recoveryDeleteTupleDeleted &&
		classification.transition == recoveryDeleteTupleRemoveIntent {
		return nil
	}
	if classification.state == recoveryDeleteTupleVerified &&
		classification.transition == recoveryDeleteTupleDeleteCaptured && err != nil {
		return ErrRecoveryTargetUnavailable
	}
	return ErrRecoveryTargetChanged
}

func removeRecoveryDeleteMarker(
	client recoveryTargetSFTPClient,
	material backupasset.DomainKeyMaterial,
	authority targetDeletePermitProof,
	paths recoveryDeleteArtifactPaths,
	value string,
	wantTransition recoveryDeleteTupleTransition,
	nextState recoveryDeleteTupleState,
	nextTransition recoveryDeleteTupleTransition,
	validateLive func() error,
) error {
	if _, err := requireRecoveryDeleteTupleClassification(
		client, material, authority, paths, recoveryDeleteTupleDeleted, wantTransition,
	); err != nil {
		return err
	}
	if err := validateRecoveryDeleteMutationAuthority(
		client, authority, paths, validateLive,
	); err != nil {
		return err
	}
	removeErr := client.Remove(value)
	after, observeErr := observeRecoveryDeleteTuple(client, authority, paths)
	if observeErr != nil {
		return observeErr
	}
	classification := classifyRecoveryDeleteTuple(material, authority, after)
	if classification.state == nextState && classification.transition == nextTransition {
		return nil
	}
	if classification.state == recoveryDeleteTupleDeleted &&
		classification.transition == wantTransition && removeErr != nil {
		return ErrRecoveryTargetUnavailable
	}
	return ErrRecoveryTargetChanged
}

func driveRecoveryDeleteTransitions(
	client recoveryTargetSFTPClient,
	material backupasset.DomainKeyMaterial,
	authority targetDeletePermitProof,
	validateLive func() error,
) (TargetWriteResult, error) {
	if client == nil || validateLive == nil || !authority.sessionBinding.valid() ||
		authority.targetMode != TargetModeInPlace || !authority.object.valid() ||
		!authority.artifacts.valid() {
		return TargetWriteResult{}, ErrInvalidTargetPermit
	}
	paths, err := recoveryDeleteArtifactPathsFor(
		path.Join(authority.sessionBinding.RootLocator, authority.object.PrivateRelativeLocator),
		authority.artifacts,
	)
	if err != nil {
		return TargetWriteResult{}, err
	}
	for transition := 0; transition < 7; transition++ {
		tuple, observeErr := observeRecoveryDeleteTuple(client, authority, paths)
		if observeErr != nil {
			return TargetWriteResult{}, observeErr
		}
		classification := classifyRecoveryDeleteTuple(material, authority, tuple)
		switch classification.state {
		case recoveryDeleteTupleFresh:
			err = createRecoveryDeleteIntent(client, material, authority, paths, validateLive)
		case recoveryDeleteTupleIntent:
			err = captureRecoveryDeleteMutationInstantObject(
				client, material, authority, paths, validateLive,
			)
		case recoveryDeleteTupleCaptured:
			err = createRecoveryDeleteVerified(
				client, material, authority, paths, validateLive,
			)
		case recoveryDeleteTupleVerified:
			err = deleteRecoveryVerifiedCapturedEntry(
				client, material, authority, paths, validateLive,
			)
		case recoveryDeleteTupleDeleted:
			switch classification.transition {
			case recoveryDeleteTupleRemoveIntent:
				err = removeRecoveryDeleteMarker(
					client, material, authority, paths, paths.intent,
					recoveryDeleteTupleRemoveIntent,
					recoveryDeleteTupleDeleted, recoveryDeleteTupleRemoveVerified,
					validateLive,
				)
			case recoveryDeleteTupleRemoveVerified:
				err = removeRecoveryDeleteMarker(
					client, material, authority, paths, paths.verified,
					recoveryDeleteTupleRemoveVerified,
					recoveryDeleteTupleClean, recoveryDeleteTupleComplete,
					validateLive,
				)
			default:
				return TargetWriteResult{}, ErrRecoveryTargetChanged
			}
		case recoveryDeleteTupleClean:
			if !exactRecoveryDeleteMissingObservation(tuple.final) ||
				!exactRecoveryDeleteMissingObservation(tuple.captured) {
				return TargetWriteResult{}, ErrRecoveryTargetChanged
			}
			return TargetWriteResult{TargetRevision: tuple.final.result.TargetRevision}, nil
		default:
			return TargetWriteResult{}, ErrRecoveryTargetChanged
		}
		if err != nil {
			return TargetWriteResult{}, err
		}
	}
	return TargetWriteResult{}, ErrRecoveryTargetUnavailable
}

func recoveryPrincipalControlsDirectory(
	uid uint32,
	groups map[uint32]struct{},
	ownerUID uint32,
	ownerGID uint32,
	mode os.FileMode,
) bool {
	if uid == 0 {
		return true
	}
	if uid == ownerUID {
		return mode&0o300 == 0o300
	}
	if _, member := groups[ownerGID]; member {
		return mode&0o030 == 0o030
	}
	return false
}

func recoveryAvailableBytes(blocks uint64, blockSize uint64) (int64, bool) {
	if blockSize != 0 && blocks > uint64(math.MaxInt64)/blockSize {
		return 0, false
	}
	value := blocks * blockSize
	if value > uint64(math.MaxInt64) {
		return 0, false
	}
	return int64(value), true
}

func (target *recoverySFTPTarget) Lstat(
	ctx context.Context,
	permit TargetVerifyPermit,
	request TargetLstatRequest,
) (TargetLstatResult, error) {
	ctx = recoveryWorkspaceMarkerContext(ctx)
	if err := ctx.Err(); err != nil {
		return TargetLstatResult{}, err
	}
	if target == nil || target.sessions == nil || target.marker == nil || target.now == nil {
		return TargetLstatResult{}, ErrRecoveryTargetUnavailable
	}
	binding, jobID, mode, err := recoveryTargetDeleteVerifyObjectAuthority(
		permit, request.Object, target.now().UTC(),
	)
	if err != nil {
		return TargetLstatResult{}, err
	}
	session, err := target.sessions.Open(ctx, binding, TargetPurposeVerify, jobID)
	if err != nil {
		return TargetLstatResult{}, err
	}
	result, operationErr := observeRecoveryDeleteEntryTwice(
		session.client, binding, jobID, mode, request.Object,
	)
	if operationErr == nil {
		currentBinding, currentJobID, currentMode, currentErr := recoveryTargetVerifyObjectAuthority(
			permit, request.Object, target.now().UTC(),
		)
		if currentErr != nil {
			operationErr = currentErr
		} else if currentBinding != binding || currentJobID != jobID || currentMode != mode {
			operationErr = ErrInvalidTargetPermit
		}
	}
	closeErr := session.Close()
	if err := ctx.Err(); err != nil {
		return TargetLstatResult{}, err
	}
	if operationErr != nil {
		return TargetLstatResult{}, recoveryTargetOperationError(ctx, operationErr)
	}
	if closeErr != nil {
		return TargetLstatResult{}, ErrRecoveryTargetUnavailable
	}
	return result, nil
}

func (*recoverySFTPTarget) CreateDirectory(
	context.Context,
	TargetWritePermit,
	CreateTargetDirectoryRequest,
) error {
	return ErrRecoveryTargetUnavailable
}

func (target *recoverySFTPTarget) WriteAtomic(
	ctx context.Context,
	permit TargetWritePermit,
	request TargetWriteAtomicRequest,
) (TargetWriteResult, error) {
	ctx = recoveryWorkspaceMarkerContext(ctx)
	if err := ctx.Err(); err != nil {
		return TargetWriteResult{}, err
	}
	if target == nil || target.sessions == nil || target.marker == nil ||
		target.entropy == nil || target.now == nil {
		return TargetWriteResult{}, ErrRecoveryTargetUnavailable
	}
	authority, err := recoveryTargetItemWriteAuthority(
		permit, request, target.now().UTC(),
	)
	if err != nil {
		return TargetWriteResult{}, err
	}
	if authority.operation == RecoveryOperationOverwrite {
		session, err := target.sessions.Open(
			ctx, authority.sessionBinding, TargetPurposeWrite, authority.jobID,
		)
		if err != nil {
			return TargetWriteResult{}, err
		}
		validateLive := func() error {
			if err := ctx.Err(); err != nil {
				return err
			}
			current, err := recoveryTargetItemWriteAuthority(
				permit, request, target.now().UTC(),
			)
			if err != nil {
				return err
			}
			if current != authority {
				return ErrInvalidTargetPermit
			}
			return nil
		}
		result, operationErr := driveRecoveryOverwriteTransitions(
			session.client, authority.sessionBinding, authority, request, validateLive,
		)
		closeErr := session.Close()
		if err := ctx.Err(); err != nil {
			return TargetWriteResult{}, err
		}
		if operationErr != nil {
			return TargetWriteResult{}, recoveryTargetOperationError(ctx, operationErr)
		}
		if closeErr != nil {
			return TargetWriteResult{}, ErrRecoveryTargetUnavailable
		}
		return result, nil
	}

	var nonce [recoveryPayloadTempEntropyBytes]byte
	if _, err := io.ReadFull(target.entropy, nonce[:]); err != nil {
		return TargetWriteResult{}, recoveryTargetUnavailableForContext(ctx)
	}
	finalPath := path.Join(
		authority.sessionBinding.RootLocator, request.Object.PrivateRelativeLocator,
	)
	tempPath := path.Join(
		path.Dir(finalPath),
		recoveryPayloadTempPrefix+base64.RawURLEncoding.EncodeToString(nonce[:]),
	)
	if tempPath == finalPath {
		return TargetWriteResult{}, ErrInvalidTargetPermit
	}

	session, err := target.sessions.Open(
		ctx, authority.sessionBinding, TargetPurposeWrite, authority.jobID,
	)
	if err != nil {
		return TargetWriteResult{}, err
	}
	validateLive := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		current, err := recoveryTargetItemWriteAuthority(
			permit, request, target.now().UTC(),
		)
		if err != nil {
			return err
		}
		if current != authority {
			return ErrInvalidTargetPermit
		}
		return nil
	}
	prepared, operationErr := prepareRecoveryCreateParents(
		session.client, authority.sessionBinding, authority, request.Object, validateLive,
	)
	result := TargetWriteResult{}
	if operationErr == nil {
		operationErr = revalidateRecoveryCreateParents(
			session.client, authority.sessionBinding, authority, request.Object, prepared,
		)
	}
	if operationErr == nil {
		result, operationErr = writeRecoveryRegularCreate(
			session.client, authority.sessionBinding, authority, request.Object,
			request, prepared, tempPath, validateLive,
		)
	}
	closeErr := session.Close()
	if err := ctx.Err(); err != nil {
		return TargetWriteResult{}, err
	}
	if operationErr != nil {
		return TargetWriteResult{}, recoveryTargetOperationError(ctx, operationErr)
	}
	if closeErr != nil {
		return TargetWriteResult{}, ErrRecoveryTargetUnavailable
	}
	return result, nil
}

func (target *recoverySFTPTarget) FinalizeOverwrite(
	ctx context.Context,
	permit TargetFinalizeOverwritePermit,
	request TargetFinalizeOverwriteRequest,
) (TargetWriteResult, error) {
	ctx = recoveryWorkspaceMarkerContext(ctx)
	if err := ctx.Err(); err != nil {
		return TargetWriteResult{}, err
	}
	if target == nil || target.sessions == nil || target.marker == nil || target.now == nil {
		return TargetWriteResult{}, ErrRecoveryTargetUnavailable
	}
	authority, err := permit.authorityAt(target.now().UTC(), request)
	if err != nil {
		return TargetWriteResult{}, err
	}
	session, err := target.sessions.Open(
		ctx, authority.sessionBinding, TargetPurposeWrite, authority.jobID,
	)
	if err != nil {
		return TargetWriteResult{}, err
	}
	validateLive := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		current, err := permit.authorityAt(target.now().UTC(), request)
		if err != nil {
			return err
		}
		if current != authority {
			return ErrInvalidTargetPermit
		}
		return nil
	}
	result, operationErr := finalizeRecoveryOverwritePublication(
		session.client, authority, request, validateLive,
	)
	closeErr := session.Close()
	if err := ctx.Err(); err != nil {
		return TargetWriteResult{}, err
	}
	if operationErr != nil {
		return TargetWriteResult{}, recoveryTargetOperationError(ctx, operationErr)
	}
	if closeErr != nil {
		return TargetWriteResult{}, ErrRecoveryTargetUnavailable
	}
	return result, nil
}

func (target *recoverySFTPTarget) Delete(
	ctx context.Context,
	permit TargetDeletePermit,
	request TargetDeleteRequest,
) (TargetWriteResult, error) {
	ctx = recoveryWorkspaceMarkerContext(ctx)
	if err := ctx.Err(); err != nil {
		return TargetWriteResult{}, err
	}
	if target == nil || target.now == nil {
		return TargetWriteResult{}, ErrRecoveryTargetUnavailable
	}
	authority, err := permit.authorityAt(target.now().UTC(), request)
	if err != nil {
		return TargetWriteResult{}, err
	}
	if target.sessions == nil || target.marker == nil || target.marker.keys == nil {
		return TargetWriteResult{}, ErrRecoveryTargetUnavailable
	}
	material, err := target.marker.keys.ByVersion(
		ctx, backupasset.KeyDomainRecoveryCleanupOwnership, authority.artifacts.keyVersion,
	)
	if err != nil {
		return TargetWriteResult{}, recoveryTargetUnavailableForContext(ctx)
	}
	defer clear(material.Key)
	if !validTargetLocatorKey(material, authority.artifacts.keyVersion) {
		return TargetWriteResult{}, ErrRecoveryTargetUnavailable
	}
	session, err := target.sessions.Open(
		ctx, authority.sessionBinding, TargetPurposeWrite, authority.jobID,
	)
	if err != nil {
		return TargetWriteResult{}, err
	}
	validateLive := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		current, err := permit.authorityAt(target.now().UTC(), request)
		if err != nil {
			return err
		}
		if current != authority {
			return ErrInvalidTargetPermit
		}
		return nil
	}
	result, operationErr := driveRecoveryDeleteTransitions(
		session.client, material, authority, validateLive,
	)
	closeErr := session.Close()
	if err := ctx.Err(); err != nil {
		return TargetWriteResult{}, err
	}
	if operationErr != nil {
		return TargetWriteResult{}, recoveryTargetOperationError(ctx, operationErr)
	}
	if closeErr != nil {
		return TargetWriteResult{}, ErrRecoveryTargetUnavailable
	}
	return result, nil
}

func (target *recoverySFTPTarget) Verify(
	ctx context.Context,
	permit TargetVerifyPermit,
	object TargetObjectRef,
	expectation TargetVerifyExpectation,
) (TargetVerifyObservation, error) {
	ctx = recoveryWorkspaceMarkerContext(ctx)
	if err := ctx.Err(); err != nil {
		return TargetVerifyObservation{}, err
	}
	if target == nil || target.sessions == nil || target.marker == nil || target.now == nil {
		return TargetVerifyObservation{}, ErrRecoveryTargetUnavailable
	}
	now := target.now().UTC()
	binding, jobID, mode, err := recoveryTargetVerifySessionAuthority(
		permit, object, expectation, now,
	)
	if err != nil {
		return TargetVerifyObservation{}, err
	}
	session, err := target.sessions.Open(ctx, binding, TargetPurposeVerify, jobID)
	if err != nil {
		return TargetVerifyObservation{}, err
	}
	var observation TargetVerifyObservation
	var operationErr error
	if expectation.Kind == TargetPresencePresent {
		observation, operationErr = verifyRecoveryPresentRegularFile(
			session.client, binding, jobID, mode, object, *expectation.Present,
		)
	} else {
		var result TargetLstatResult
		result, operationErr = observeRecoveryDeleteEntryTwice(
			session.client, binding, jobID, mode, object,
		)
		if operationErr == nil {
			if result.Kind != TargetEntryMissing || result.IdentityDigest != "" ||
				!validOpaqueRevision(result.TargetRevision) {
				operationErr = ErrRecoveryTargetChanged
			} else {
				observation = TargetVerifyObservation{
					Kind:             TargetPresenceAbsent,
					Absent:           &AbsentObservation{Evidence: TargetAbsenceEvidenceExact},
					ObservedRevision: result.TargetRevision,
				}
			}
		}
	}
	if operationErr == nil {
		currentBinding, currentJobID, currentMode, currentErr := recoveryTargetVerifySessionAuthority(
			permit, object, expectation, target.now().UTC(),
		)
		if currentErr != nil {
			operationErr = currentErr
		} else if currentBinding != binding || currentJobID != jobID || currentMode != mode {
			operationErr = ErrInvalidTargetPermit
		}
	}
	closeErr := session.Close()
	if err := ctx.Err(); err != nil {
		return TargetVerifyObservation{}, err
	}
	if operationErr != nil {
		return TargetVerifyObservation{}, recoveryTargetOperationError(ctx, operationErr)
	}
	if closeErr != nil {
		return TargetVerifyObservation{}, ErrRecoveryTargetUnavailable
	}
	return observation, nil
}

func (target *recoverySFTPTarget) OpenOwnedResult(
	ctx context.Context,
	permit TargetResultReadPermit,
	request OpenOwnedResultRequest,
) (io.ReadCloser, error) {
	ctx = recoveryWorkspaceMarkerContext(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if target == nil || target.sessions == nil || target.marker == nil || target.now == nil {
		return nil, ErrRecoveryTargetUnavailable
	}
	now := target.now().UTC()
	authority, err := permit.authorityForRequestAt(now, request)
	if err != nil {
		return nil, err
	}
	session, err := target.sessions.Open(
		ctx, authority.sessionBinding, TargetPurposeResultRead, authority.jobID,
	)
	if err != nil {
		return nil, err
	}
	client := &recoveryResultTrackedSFTPClient{
		recoveryTargetSFTPClient: session.client,
		session:                  session,
	}
	fail := func(file recoveryTargetSFTPFile, operationErr error) (io.ReadCloser, error) {
		var fileErr error
		if file != nil {
			fileErr = file.Close()
		}
		sessionErr := session.Close()
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if operationErr != nil {
			return nil, recoveryTargetOperationError(ctx, operationErr)
		}
		if fileErr != nil || sessionErr != nil {
			return nil, ErrRecoveryTargetUnavailable
		}
		return nil, ErrRecoveryTargetUnavailable
	}
	if err := validateRecoveryOwnedResultMarker(ctx, target, client, authority); err != nil {
		return fail(nil, err)
	}
	observation, err := verifyRecoveryPresentRegularFile(
		client, authority.sessionBinding, authority.jobID, TargetModeIsolated,
		request.Object, PresentExpectation{IdentityDigest: request.IdentityDigest, Bytes: request.ExpectedBytes},
	)
	if err != nil || observation.Kind != TargetPresencePresent || observation.Present == nil ||
		observation.Present.IdentityDigest != request.IdentityDigest ||
		observation.Present.Bytes != request.ExpectedBytes {
		if err == nil {
			err = ErrRecoveryTargetChanged
		}
		return fail(nil, err)
	}
	if err := validateRecoveryOwnedResultMarker(ctx, target, client, authority); err != nil {
		return fail(nil, err)
	}
	finalPath, err := validateRecoveryVerifyCanonicalParents(
		client, authority.sessionBinding, authority.jobID, TargetModeIsolated, request.Object,
	)
	if err != nil {
		return fail(nil, err)
	}
	before, err := observeRecoveryCanonicalRegularFile(client, finalPath)
	if err != nil {
		return fail(nil, err)
	}
	if before.Size() != request.ExpectedBytes || before.Mode().Perm() != 0o600 {
		return fail(nil, ErrRecoveryTargetChanged)
	}
	verifiedSnapshot := recoverySFTPFileSnapshotOf(before)
	file, err := client.Open(finalPath)
	if err != nil {
		if os.IsNotExist(err) {
			err = ErrRecoveryTargetChanged
		}
		return fail(file, err)
	}
	opened, err := file.Stat()
	if err != nil || opened == nil {
		return fail(file, err)
	}
	after, err := client.Lstat(finalPath)
	if err != nil || after == nil {
		if os.IsNotExist(err) {
			err = ErrRecoveryTargetChanged
		}
		return fail(file, err)
	}
	if recoverySFTPFileSnapshotOf(opened) != verifiedSnapshot ||
		recoverySFTPFileSnapshotOf(after) != verifiedSnapshot {
		return fail(file, ErrRecoveryTargetChanged)
	}
	if err := validateRecoveryOwnedResultMarker(ctx, target, client, authority); err != nil {
		return fail(file, err)
	}
	if _, err := permit.authorityForRequestAt(target.now().UTC(), request); err != nil {
		return fail(file, err)
	}
	reader := &recoveryOwnedResultReader{
		ctx: ctx, target: target, permit: permit, request: request, authority: authority,
		session: session, client: client, file: file, finalPath: finalPath, snapshot: verifiedSnapshot,
		hasher: sha256.New(),
	}
	if request.ExpectedBytes == 0 {
		reader.readMu.Lock()
		validationErr := reader.validateCompleteLocked()
		reader.readMu.Unlock()
		if validationErr != nil {
			return fail(file, validationErr)
		}
	}
	return reader, nil
}

type recoveryOwnedResultReader struct {
	readMu      sync.Mutex
	closeOnce   sync.Once
	ctx         context.Context
	target      *recoverySFTPTarget
	permit      TargetResultReadPermit
	request     OpenOwnedResultRequest
	authority   targetResultReadAuthority
	session     *recoveryTargetSession
	client      recoveryTargetSFTPClient
	file        recoveryTargetSFTPFile
	finalPath   string
	snapshot    recoverySFTPFileSnapshot
	hasher      hash.Hash
	readBytes   int64
	complete    bool
	closed      atomic.Bool
	terminalErr error
	closeErr    error
}

func (reader *recoveryOwnedResultReader) Read(buffer []byte) (int, error) {
	if reader == nil {
		return 0, ErrRecoveryTargetUnavailable
	}
	reader.readMu.Lock()
	defer reader.readMu.Unlock()
	if reader.closed.Load() {
		return 0, ErrRecoveryTargetUnavailable
	}
	if reader.terminalErr != nil {
		return 0, reader.terminalErr
	}
	if reader.complete {
		return 0, io.EOF
	}
	if err := reader.ctx.Err(); err != nil {
		reader.terminalErr = err
		return 0, err
	}
	if len(buffer) == 0 {
		return 0, nil
	}
	remaining := reader.request.ExpectedBytes - reader.readBytes
	if remaining <= 0 {
		if err := reader.validateCompleteLocked(); err != nil {
			return 0, err
		}
		return 0, io.EOF
	}
	limit := len(buffer)
	if limit > recoveryResultReadChunkBytes {
		limit = recoveryResultReadChunkBytes
	}
	if int64(limit) > remaining {
		limit = int(remaining)
	}
	count, readErr := reader.file.Read(buffer[:limit])
	if count > 0 {
		_, _ = reader.hasher.Write(buffer[:count])
		reader.readBytes += int64(count)
	}
	if readErr != nil && !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrUnexpectedEOF) {
		return count, reader.failLocked(recoveryTargetOperationError(reader.ctx, readErr))
	}
	if reader.readBytes > reader.request.ExpectedBytes {
		return count, reader.failLocked(ErrRecoveryTargetChanged)
	}
	if reader.readBytes == reader.request.ExpectedBytes {
		if err := reader.validateCompleteLocked(); err != nil {
			return count, err
		}
		return count, io.EOF
	}
	if readErr != nil {
		return count, reader.failLocked(ErrRecoveryTargetChanged)
	}
	if count == 0 {
		return 0, reader.failLocked(ErrRecoveryTargetUnavailable)
	}
	return count, nil
}

func (reader *recoveryOwnedResultReader) validateCompleteLocked() error {
	if reader.terminalErr != nil {
		return reader.terminalErr
	}
	if reader.complete {
		return nil
	}
	if reader.closed.Load() {
		return reader.failLocked(recoveryTargetUnavailableForContext(reader.ctx))
	}
	if err := reader.ctx.Err(); err != nil {
		return reader.failLocked(err)
	}
	if reader.readBytes != reader.request.ExpectedBytes {
		return reader.failLocked(ErrRecoveryTargetChanged)
	}
	var extra [1]byte
	count, err := reader.file.Read(extra[:])
	if count != 0 || err == nil {
		return reader.failLocked(ErrRecoveryTargetChanged)
	}
	if !errors.Is(err, io.EOF) {
		return reader.failLocked(recoveryTargetOperationError(reader.ctx, err))
	}
	if hex.EncodeToString(reader.hasher.Sum(nil)) != reader.request.IdentityDigest {
		return reader.failLocked(ErrRecoveryTargetChanged)
	}
	opened, err := reader.file.Stat()
	if err != nil || opened == nil {
		return reader.failLocked(recoveryTargetOperationError(reader.ctx, err))
	}
	after, err := reader.client.Lstat(reader.finalPath)
	if err != nil || after == nil {
		if os.IsNotExist(err) {
			err = ErrRecoveryTargetChanged
		}
		return reader.failLocked(recoveryTargetOperationError(reader.ctx, err))
	}
	if recoverySFTPFileSnapshotOf(opened) != reader.snapshot ||
		recoverySFTPFileSnapshotOf(after) != reader.snapshot {
		return reader.failLocked(ErrRecoveryTargetChanged)
	}
	finalPath, err := validateRecoveryVerifyCanonicalParents(
		reader.client, reader.authority.sessionBinding, reader.authority.jobID,
		TargetModeIsolated, reader.request.Object,
	)
	if err != nil || finalPath != reader.finalPath {
		if err == nil {
			err = ErrRecoveryTargetChanged
		}
		return reader.failLocked(recoveryTargetOperationError(reader.ctx, err))
	}
	if err := validateRecoveryOwnedResultMarker(
		reader.ctx, reader.target, reader.client, reader.authority,
	); err != nil {
		return reader.failLocked(recoveryTargetOperationError(reader.ctx, err))
	}
	if _, err := reader.permit.authorityForRequestAt(reader.target.now().UTC(), reader.request); err != nil {
		return reader.failLocked(err)
	}
	if err := reader.ctx.Err(); err != nil {
		return reader.failLocked(err)
	}
	reader.complete = true
	return nil
}

func (reader *recoveryOwnedResultReader) revalidateCompleteLocked() error {
	reader.complete = false
	return reader.validateCompleteLocked()
}

func (reader *recoveryOwnedResultReader) failLocked(err error) error {
	if err == nil {
		err = ErrRecoveryTargetUnavailable
	}
	if reader.terminalErr == nil {
		reader.terminalErr = recoveryTargetOperationError(reader.ctx, err)
	}
	return reader.terminalErr
}

func (reader *recoveryOwnedResultReader) Close() error {
	if reader == nil {
		return ErrRecoveryTargetUnavailable
	}
	reader.closeOnce.Do(func() {
		if reader.request.ExpectedBytes == 0 {
			reader.readMu.Lock()
			_ = reader.revalidateCompleteLocked()
			reader.readMu.Unlock()
		}
		reader.closed.Store(true)
		fileErr := reader.file.Close()
		sessionErr := reader.session.Close()
		reader.readMu.Lock()
		terminalErr := reader.terminalErr
		reader.readMu.Unlock()
		if err := reader.ctx.Err(); err != nil {
			reader.closeErr = err
		} else if terminalErr != nil {
			reader.closeErr = terminalErr
		} else if fileErr != nil || sessionErr != nil {
			reader.closeErr = ErrRecoveryTargetUnavailable
		}
	})
	return reader.closeErr
}

func validateRecoveryOwnedResultMarker(
	ctx context.Context,
	target *recoverySFTPTarget,
	client recoveryTargetSFTPClient,
	authority targetResultReadAuthority,
) error {
	if target == nil || target.marker == nil || client == nil || !authority.valid() {
		return ErrInvalidTargetPermit
	}
	workspaceLocator := recoveryWorkspaceLocatorDirectory + "/" + authority.jobID
	if err := validateRecoveryRootPrefixes(client, authority.sessionBinding.RootLocator); err != nil {
		return err
	}
	jobsPath := path.Join(authority.sessionBinding.RootLocator, recoveryWorkspaceLocatorDirectory)
	jobPath := path.Join(authority.sessionBinding.RootLocator, workspaceLocator)
	if err := validateRecoveryCanonicalDirectory(client, jobsPath, true); err != nil {
		return err
	}
	if err := validateRecoveryCanonicalDirectory(client, jobPath, true); err != nil {
		return err
	}
	markerBytes, _, err := readRecoveryMarkerFile(
		client, path.Join(jobPath, recoveryWorkspaceMarkerFileName),
	)
	if err != nil {
		return err
	}
	if err := target.marker.validateEncoded(
		ctx, markerBytes, authority.jobID, authority.object.RootID,
		authority.sessionBinding.RootRevision, workspaceLocator,
		authority.markerBindingDigest, authority.markerCreatorID, authority.markerCreatorFence,
	); err != nil {
		if errors.Is(err, ErrInvalidRecoveryWorkspaceMarker) {
			return ErrRecoveryTargetChanged
		}
		return err
	}
	if err := validateRecoveryCanonicalDirectory(client, jobsPath, true); err != nil {
		return err
	}
	return validateRecoveryCanonicalDirectory(client, jobPath, true)
}

func (target *recoverySFTPTarget) RemoveOwnedJobDir(
	ctx context.Context,
	permit TargetCleanupPermit,
	request RemoveOwnedJobDirRequest,
) (OwnedJobDirRemoval, error) {
	ctx = recoveryWorkspaceMarkerContext(ctx)
	if err := ctx.Err(); err != nil {
		return OwnedJobDirRemoval{}, err
	}
	if target == nil || target.sessions == nil || target.marker == nil || target.marker.keys == nil || target.now == nil {
		return OwnedJobDirRemoval{}, ErrRecoveryTargetUnavailable
	}
	now := target.now().UTC()
	binding, err := recoveryTargetCleanupRemovalSessionBinding(permit, request, now)
	if err != nil {
		return OwnedJobDirRemoval{}, err
	}
	session, err := target.sessions.Open(ctx, binding, TargetPurposeCleanup, permit.JobID)
	if err != nil {
		return OwnedJobDirRemoval{}, err
	}
	removal, operationErr := target.removeOwnedJobDir(ctx, session.client, binding, permit, request, now)
	closeErr := session.Close()
	if err := ctx.Err(); err != nil {
		return OwnedJobDirRemoval{}, err
	}
	if operationErr != nil {
		return OwnedJobDirRemoval{}, recoveryTargetOperationError(ctx, operationErr)
	}
	// A complete removal has already re-proved the final workspace, captured
	// sibling and verified marker absent. Discarding that closed result on a
	// later session-close ambiguity would leave delete_started with no remote
	// evidence to replay. Incomplete removal still treats close failure as
	// unavailable.
	if closeErr != nil && !removal.Complete {
		return OwnedJobDirRemoval{}, ErrRecoveryTargetUnavailable
	}
	return removal, nil
}

func (target *recoverySFTPTarget) CreateOwnedJobDir(
	ctx context.Context,
	permit TargetWritePermit,
	request CreateOwnedJobDirRequest,
) (OwnedJobDir, error) {
	ctx = recoveryWorkspaceMarkerContext(ctx)
	if err := ctx.Err(); err != nil {
		return OwnedJobDir{}, err
	}
	if target == nil || target.sessions == nil || target.marker == nil || target.now == nil {
		return OwnedJobDir{}, ErrRecoveryTargetUnavailable
	}
	now := target.now().UTC()
	binding, err := recoveryTargetWriteSessionBinding(permit, request, now)
	if err != nil {
		return OwnedJobDir{}, err
	}
	session, err := target.sessions.Open(ctx, binding, TargetPurposeWrite, permit.permit.JobID)
	if err != nil {
		return OwnedJobDir{}, err
	}
	owned, operationErr := target.createOwnedJobDir(ctx, session.client, binding, permit, request, now)
	closeErr := session.Close()
	if err := ctx.Err(); err != nil {
		return OwnedJobDir{}, err
	}
	if operationErr != nil {
		return OwnedJobDir{}, recoveryTargetOperationError(ctx, operationErr)
	}
	if closeErr != nil {
		return OwnedJobDir{}, ErrRecoveryTargetUnavailable
	}
	return owned, nil
}

func (target *recoverySFTPTarget) ValidateOwnedJobDir(
	ctx context.Context,
	permit TargetCleanupPermit,
	request ValidateOwnedJobDirRequest,
) (OwnedJobDirValidation, error) {
	ctx = recoveryWorkspaceMarkerContext(ctx)
	if err := ctx.Err(); err != nil {
		return OwnedJobDirValidation{}, err
	}
	if target == nil || target.sessions == nil || target.marker == nil || target.now == nil {
		return OwnedJobDirValidation{}, ErrRecoveryTargetUnavailable
	}
	now := target.now().UTC()
	binding, err := recoveryTargetCleanupSessionBinding(permit, request, now)
	if err != nil {
		return OwnedJobDirValidation{}, err
	}
	session, err := target.sessions.Open(ctx, binding, TargetPurposeCleanup, permit.JobID)
	if err != nil {
		return OwnedJobDirValidation{}, err
	}
	validation, operationErr := target.validateOwnedJobDir(
		ctx, session.client, binding, permit, request, now,
	)
	closeErr := session.Close()
	if err := ctx.Err(); err != nil {
		return OwnedJobDirValidation{}, err
	}
	if operationErr != nil {
		return OwnedJobDirValidation{}, recoveryTargetOperationError(ctx, operationErr)
	}
	if closeErr != nil {
		return OwnedJobDirValidation{}, ErrRecoveryTargetUnavailable
	}
	return validation, nil
}

func (target *recoverySFTPTarget) ValidateOwnedJobDirRemoved(
	ctx context.Context,
	permit TargetCleanupPermit,
	request RemoveOwnedJobDirRequest,
) (OwnedJobDirRemovalValidation, error) {
	ctx = recoveryWorkspaceMarkerContext(ctx)
	if err := ctx.Err(); err != nil {
		return OwnedJobDirRemovalValidation{}, err
	}
	if target == nil || target.sessions == nil || target.marker == nil || target.now == nil {
		return OwnedJobDirRemovalValidation{}, ErrRecoveryTargetUnavailable
	}
	now := target.now().UTC()
	binding, err := recoveryTargetCleanupRemovedSessionBinding(permit, request, now)
	if err != nil {
		return OwnedJobDirRemovalValidation{}, err
	}
	session, err := target.sessions.Open(ctx, binding, TargetPurposeCleanup, permit.JobID)
	if err != nil {
		return OwnedJobDirRemovalValidation{}, err
	}
	validation, operationErr := target.validateOwnedJobDirRemoved(
		ctx, session.client, binding, permit, request,
	)
	closeErr := session.Close()
	if err := ctx.Err(); err != nil {
		return OwnedJobDirRemovalValidation{}, err
	}
	if operationErr != nil {
		return OwnedJobDirRemovalValidation{}, recoveryTargetOperationError(ctx, operationErr)
	}
	if closeErr != nil {
		return OwnedJobDirRemovalValidation{}, ErrRecoveryTargetUnavailable
	}
	return validation, nil
}

var _ TargetPort = (*recoverySFTPTarget)(nil)

func recoveryTargetWriteSessionBinding(
	permit TargetWritePermit,
	request CreateOwnedJobDirRequest,
	now time.Time,
) (recoveryTargetSessionBinding, error) {
	if permit.ValidateOwnedJobDirRequestAt(now, request) != nil || permit.permit.proof == nil {
		return recoveryTargetSessionBinding{}, ErrInvalidTargetPermit
	}
	binding := permit.permit.proof.sessionBinding
	wantLocator := recoveryWorkspaceLocatorDirectory + "/" + permit.permit.JobID
	if !binding.valid() || binding.NodeID != permit.permit.NodeID ||
		binding.RootID != permit.permit.RootID ||
		binding.RootLocatorDigest != permit.permit.RootLocatorDigest ||
		binding.RootRevision != permit.permit.RootRevision ||
		request.Object.PrivateRelativeLocator != wantLocator ||
		request.Object.TargetPathDigest != permit.permit.TargetPathDigest {
		return recoveryTargetSessionBinding{}, ErrInvalidTargetPermit
	}
	return binding, nil
}

func recoveryTargetCleanupSessionBinding(
	permit TargetCleanupPermit,
	request ValidateOwnedJobDirRequest,
	now time.Time,
) (recoveryTargetSessionBinding, error) {
	if permit.ValidateOwnedJobDirRequestAt(now, request) != nil || permit.proof == nil {
		return recoveryTargetSessionBinding{}, ErrInvalidTargetPermit
	}
	binding := permit.proof.sessionBinding
	wantLocator := recoveryWorkspaceLocatorDirectory + "/" + permit.JobID
	if !binding.valid() || binding.NodeID != permit.NodeID || binding.RootID != permit.RootID ||
		binding.RootLocatorDigest != permit.RootLocatorDigest || binding.RootRevision != permit.RootRevision ||
		request.Object.PrivateRelativeLocator != wantLocator ||
		request.Object.TargetPathDigest != permit.TargetPathDigest {
		return recoveryTargetSessionBinding{}, ErrInvalidTargetPermit
	}
	return binding, nil
}

func recoveryTargetCleanupRemovalSessionBinding(
	permit TargetCleanupPermit,
	request RemoveOwnedJobDirRequest,
	now time.Time,
) (recoveryTargetSessionBinding, error) {
	if permit.ValidateAt(now) != nil || permit.Operation != TargetCleanupRemoveOwnedJobDir ||
		request.MarkerBindingDigest != permit.MarkerBindingDigest || permit.proof == nil ||
		permit.proof.validateLive == nil {
		return recoveryTargetSessionBinding{}, ErrInvalidTargetPermit
	}
	binding := permit.proof.sessionBinding
	wantLocator := recoveryWorkspaceLocatorDirectory + "/" + permit.JobID
	if !binding.valid() || binding.NodeID != permit.NodeID || binding.RootID != permit.RootID ||
		binding.RootLocatorDigest != permit.RootLocatorDigest || binding.RootRevision != permit.RootRevision ||
		request.Object.PrivateRelativeLocator != wantLocator || request.Object.TargetPathDigest != permit.TargetPathDigest {
		return recoveryTargetSessionBinding{}, ErrInvalidTargetPermit
	}
	return binding, nil
}

func recoveryTargetCleanupRemovedSessionBinding(
	permit TargetCleanupPermit,
	request RemoveOwnedJobDirRequest,
	now time.Time,
) (recoveryTargetSessionBinding, error) {
	if permit.ValidateAt(now) != nil || permit.Operation != TargetCleanupValidateRemovedJobDir ||
		request.MarkerBindingDigest != permit.MarkerBindingDigest || permit.proof == nil {
		return recoveryTargetSessionBinding{}, ErrInvalidTargetPermit
	}
	binding := permit.proof.sessionBinding
	wantLocator := recoveryWorkspaceLocatorDirectory + "/" + permit.JobID
	if !binding.valid() || binding.NodeID != permit.NodeID || binding.RootID != permit.RootID ||
		binding.RootLocatorDigest != permit.RootLocatorDigest || binding.RootRevision != permit.RootRevision ||
		request.Object.PrivateRelativeLocator != wantLocator || request.Object.TargetPathDigest != permit.TargetPathDigest {
		return recoveryTargetSessionBinding{}, ErrInvalidTargetPermit
	}
	return binding, nil
}

func (target *recoverySFTPTarget) removeOwnedJobDir(
	ctx context.Context,
	client recoveryTargetSFTPClient,
	binding recoveryTargetSessionBinding,
	permit TargetCleanupPermit,
	request RemoveOwnedJobDirRequest,
	now time.Time,
) (OwnedJobDirRemoval, error) {
	if client == nil || target == nil || target.marker == nil || permit.proof == nil ||
		permit.proof.validateLive == nil || !binding.valid() || permit.ValidateAt(now) != nil {
		return OwnedJobDirRemoval{}, ErrInvalidTargetPermit
	}
	if err := validateRecoveryRootPrefixes(client, binding.RootLocator); err != nil {
		return OwnedJobDirRemoval{}, err
	}
	jobsPath := path.Join(binding.RootLocator, recoveryWorkspaceLocatorDirectory)
	jobPath := path.Join(binding.RootLocator, request.Object.PrivateRelativeLocator)
	if err := validateRecoveryCanonicalDirectory(client, jobsPath, true); err != nil {
		return OwnedJobDirRemoval{}, err
	}
	jobMissing, err := recoverySFTPPathMissing(client, jobPath)
	if err != nil {
		return OwnedJobDirRemoval{}, err
	}
	if jobMissing {
		// After a successful capture the final workspace is intentionally absent.
		// Locate only the permit-derived candidate; arbitrary jobs-directory
		// siblings are never adopted as cleanup evidence.
		capturedComponent := recoveryOwnedCleanupCapturedComponent(permit)
		capturedPath := path.Join(jobsPath, capturedComponent)
		capturedMissing, err := recoverySFTPPathMissing(client, capturedPath)
		if err != nil {
			return OwnedJobDirRemoval{}, err
		}
		if capturedMissing {
			// delete_started is already durable cleanup authority. A caller
			// cancellation can win after the previous invocation proved and
			// removed the complete tuple, so replay must be able to adopt that
			// exact absence without recreating or mutating any artifact.
			if err := observeRecoveryOwnedCleanupVerifiedAbsence(ctx, client, jobsPath); err != nil {
				return OwnedJobDirRemoval{}, err
			}
			if err := permit.proof.validateLive(ctx, permit); err != nil {
				return OwnedJobDirRemoval{}, err
			}
			return OwnedJobDirRemoval{
				Complete: true, RemovedEntries: 0,
				ProgressDigest: recoveryCleanupProgressDigest(permit, 0, true),
			}, nil
		}
		if err := validateRecoveryCanonicalDirectory(client, capturedPath, true); err != nil {
			return OwnedJobDirRemoval{}, err
		}
		capturedMarkerPath := path.Join(capturedPath, recoveryWorkspaceMarkerFileName)
		capturedMarker, _, err := readRecoveryMarkerFile(client, capturedMarkerPath)
		if err != nil {
			return OwnedJobDirRemoval{}, err
		}
		if err := target.marker.validateEncoded(
			ctx, capturedMarker, permit.JobID, request.Object.RootID, permit.RootRevision,
			request.Object.PrivateRelativeLocator, permit.MarkerBindingDigest,
			permit.MarkerCreatorID, permit.MarkerCreatorFence,
		); err != nil {
			if errors.Is(err, ErrInvalidRecoveryWorkspaceMarker) {
				return OwnedJobDirRemoval{}, ErrRecoveryTargetChanged
			}
			return OwnedJobDirRemoval{}, err
		}
		markerDocument, err := decodeRecoveryWorkspaceMarkerDocument(capturedMarker)
		if err != nil {
			return OwnedJobDirRemoval{}, ErrRecoveryTargetChanged
		}
		material, err := target.marker.keys.ByVersion(
			ctx, backupasset.KeyDomainRecoveryCleanupOwnership, markerDocument.KeyVersion,
		)
		if err != nil {
			return OwnedJobDirRemoval{}, recoveryWorkspaceMarkerDependencyError(ctx, err)
		}
		defer clear(material.Key)
		artifacts, err := deriveRecoveryOwnedCleanupArtifactBinding(
			material, permit, markerDocument, capturedMarker,
		)
		if err != nil || artifacts.capturedComponent != capturedComponent {
			if err != nil {
				return OwnedJobDirRemoval{}, err
			}
			return OwnedJobDirRemoval{}, ErrRecoveryTargetChanged
		}
		return target.authenticateCapturedOwnedJobDir(
			ctx, client, permit, request, jobsPath, jobPath, capturedPath,
			capturedMarker, material, artifacts,
		)
	}
	if err := validateRecoveryCanonicalDirectory(client, jobPath, true); err != nil {
		return OwnedJobDirRemoval{}, err
	}
	markerPath := path.Join(jobPath, recoveryWorkspaceMarkerFileName)
	markerBytes, _, err := readRecoveryMarkerFile(client, markerPath)
	if err != nil {
		return OwnedJobDirRemoval{}, err
	}
	if err := target.marker.validateEncoded(
		ctx, markerBytes, permit.JobID, request.Object.RootID, permit.RootRevision,
		request.Object.PrivateRelativeLocator, permit.MarkerBindingDigest,
		permit.MarkerCreatorID, permit.MarkerCreatorFence,
	); err != nil {
		if errors.Is(err, ErrInvalidRecoveryWorkspaceMarker) {
			return OwnedJobDirRemoval{}, ErrRecoveryTargetChanged
		}
		return OwnedJobDirRemoval{}, err
	}
	markerDocument, err := decodeRecoveryWorkspaceMarkerDocument(markerBytes)
	if err != nil {
		return OwnedJobDirRemoval{}, ErrRecoveryTargetChanged
	}
	material, err := target.marker.keys.ByVersion(
		ctx, backupasset.KeyDomainRecoveryCleanupOwnership, markerDocument.KeyVersion,
	)
	if err != nil {
		return OwnedJobDirRemoval{}, recoveryWorkspaceMarkerDependencyError(ctx, err)
	}
	defer clear(material.Key)
	artifacts, err := deriveRecoveryOwnedCleanupArtifactBinding(material, permit, markerDocument, markerBytes)
	if err != nil {
		return OwnedJobDirRemoval{}, err
	}
	capturedPath := path.Join(jobsPath, artifacts.capturedComponent)
	verifiedPath := path.Join(jobsPath, artifacts.verifiedComponent)
	capturedMissing, err := recoverySFTPPathMissing(client, capturedPath)
	if err != nil {
		return OwnedJobDirRemoval{}, err
	}
	verifiedMissing, err := recoverySFTPPathMissing(client, verifiedPath)
	if err != nil {
		return OwnedJobDirRemoval{}, err
	}
	if !capturedMissing || !verifiedMissing {
		return OwnedJobDirRemoval{}, ErrRecoveryTargetChanged
	}
	if err := permit.proof.validateLive(ctx, permit); err != nil {
		return OwnedJobDirRemoval{}, err
	}
	if err := client.Rename(jobPath, capturedPath); err != nil {
		if os.IsExist(err) {
			return OwnedJobDirRemoval{}, ErrRecoveryTargetChanged
		}
		return OwnedJobDirRemoval{}, ErrRecoveryTargetUnavailable
	}
	return target.authenticateCapturedOwnedJobDir(
		ctx, client, permit, request, jobsPath, jobPath, capturedPath,
		markerBytes, material, artifacts,
	)
}

func (target *recoverySFTPTarget) authenticateCapturedOwnedJobDir(
	ctx context.Context,
	client recoveryTargetSFTPClient,
	permit TargetCleanupPermit,
	request RemoveOwnedJobDirRequest,
	jobsPath string,
	jobPath string,
	capturedPath string,
	markerBytes []byte,
	material backupasset.DomainKeyMaterial,
	artifacts recoveryOwnedCleanupArtifactBinding,
) (OwnedJobDirRemoval, error) {
	if err := validateRecoveryCanonicalDirectory(client, jobsPath, true); err != nil {
		return OwnedJobDirRemoval{}, err
	}
	if err := validateRecoveryCanonicalDirectory(client, capturedPath, true); err != nil {
		return OwnedJobDirRemoval{}, err
	}
	capturedMarkerPath := path.Join(capturedPath, recoveryWorkspaceMarkerFileName)
	capturedMarker, _, err := readRecoveryMarkerFile(client, capturedMarkerPath)
	if err != nil {
		return OwnedJobDirRemoval{}, err
	}
	if !bytes.Equal(capturedMarker, markerBytes) {
		return OwnedJobDirRemoval{}, ErrRecoveryTargetChanged
	}
	if err := target.marker.validateEncoded(
		ctx, capturedMarker, permit.JobID, request.Object.RootID, permit.RootRevision,
		request.Object.PrivateRelativeLocator, permit.MarkerBindingDigest,
		permit.MarkerCreatorID, permit.MarkerCreatorFence,
	); err != nil {
		if errors.Is(err, ErrInvalidRecoveryWorkspaceMarker) {
			return OwnedJobDirRemoval{}, ErrRecoveryTargetChanged
		}
		return OwnedJobDirRemoval{}, err
	}
	jobMissing, err := recoverySFTPPathMissing(client, jobPath)
	if err != nil {
		return OwnedJobDirRemoval{}, err
	}
	if !jobMissing {
		return OwnedJobDirRemoval{}, ErrRecoveryTargetChanged
	}
	verifiedPath := path.Join(jobsPath, artifacts.verifiedComponent)
	verifiedMissing, err := recoverySFTPPathMissing(client, verifiedPath)
	if err != nil {
		return OwnedJobDirRemoval{}, err
	}
	if verifiedMissing {
		if err := permit.proof.validateLive(ctx, permit); err != nil {
			return OwnedJobDirRemoval{}, err
		}
		if err := writeRecoveryOwnedCleanupMarker(client, verifiedPath, []byte(artifacts.verifiedDocument)); err != nil {
			return OwnedJobDirRemoval{}, err
		}
	}
	verifiedMarker, _, err := readRecoveryMarkerFile(client, verifiedPath)
	if err != nil {
		return OwnedJobDirRemoval{}, err
	}
	expectedBody := recoveryOwnedCleanupArtifactBody{
		SchemaVersion: 1, KeyVersion: artifacts.keyVersion, JobID: permit.JobID,
		RootID: permit.RootID, RootRevision: permit.RootRevision,
		WorkspaceLocator:    recoveryWorkspaceLocatorDirectory + "/" + permit.JobID,
		MarkerBindingDigest: permit.MarkerBindingDigest, MarkerCreatorID: permit.MarkerCreatorID,
		MarkerCreatorFence: permit.MarkerCreatorFence, MarkerDigest: artifacts.markerDigest,
		CapturedComponent: artifacts.capturedComponent,
	}
	if err := validateRecoveryOwnedCleanupArtifactDocument(
		verifiedMarker, expectedBody, material.Key, recoveryOwnedCleanupVerifiedDomain,
	); err != nil {
		return OwnedJobDirRemoval{}, err
	}
	if err := validateRecoveryCanonicalDirectory(client, jobsPath, true); err != nil {
		return OwnedJobDirRemoval{}, err
	}
	if err := validateRecoveryCanonicalDirectory(client, capturedPath, true); err != nil {
		return OwnedJobDirRemoval{}, err
	}
	jobMissing, err = recoverySFTPPathMissing(client, jobPath)
	if err != nil {
		return OwnedJobDirRemoval{}, err
	}
	if !jobMissing {
		return OwnedJobDirRemoval{}, ErrRecoveryTargetChanged
	}
	if !verifiedMissing {
		return target.removeCapturedOwnedCleanup(
			ctx, client, permit, jobsPath, jobPath, capturedPath, verifiedPath,
			material, artifacts,
		)
	}
	return OwnedJobDirRemoval{
		Complete: false, RemovedEntries: 0,
		ProgressDigest: framedDigest(
			"xirang/recovery/owned-cleanup-progress/v1", permit.JobID,
			artifacts.capturedComponent, artifacts.verifiedComponent, artifacts.markerDigest,
		),
	}, nil
}

type recoveryCleanupDirectoryFrame struct {
	path      string
	file      recoveryTargetSFTPFile
	entries   []os.FileInfo
	entryNext int
	readDone  bool
	depth     int
}

func recoveryCleanupProgressDigest(permit TargetCleanupPermit, removed int, complete bool) string {
	phase := "incomplete"
	if complete {
		phase = "complete"
	}
	return framedDigest(
		"xirang/recovery/owned-cleanup-progress/v1", permit.JobID,
		phase, strconv.Itoa(removed),
	)
}

func validateRecoveryCleanupDirectory(
	client recoveryTargetSFTPClient,
	value string,
	filesystemID uint64,
	requirePrivateMode bool,
) error {
	if err := validateRecoveryCanonicalDirectory(client, value, requirePrivateMode); err != nil {
		return err
	}
	filesystem, err := client.StatVFS(value)
	if err != nil || filesystem == nil || filesystem.Fsid == 0 {
		return ErrRecoveryTargetUnavailable
	}
	if filesystem.Fsid != filesystemID {
		return ErrRecoveryTargetChanged
	}
	return nil
}

func recoveryCleanupChildPath(parent, name, capturedRoot string) (string, error) {
	if name == "" || name == "." || name == ".." || path.Base(name) != name {
		return "", ErrRecoveryTargetChanged
	}
	child := path.Join(parent, name)
	prefix := strings.TrimSuffix(capturedRoot, "/") + "/"
	if child == capturedRoot || !strings.HasPrefix(child, prefix) || path.Clean(child) != child {
		return "", ErrRecoveryTargetChanged
	}
	return child, nil
}

func closeRecoveryCleanupFrames(frames []recoveryCleanupDirectoryFrame) error {
	var closeErr error
	for index := len(frames) - 1; index >= 0; index-- {
		if frames[index].file == nil {
			continue
		}
		if err := frames[index].file.Close(); err != nil && closeErr == nil {
			closeErr = err
		}
		frames[index].file = nil
	}
	return closeErr
}

func (target *recoverySFTPTarget) removeCapturedOwnedCleanup(
	ctx context.Context,
	client recoveryTargetSFTPClient,
	permit TargetCleanupPermit,
	jobsPath string,
	jobPath string,
	capturedPath string,
	verifiedPath string,
	material backupasset.DomainKeyMaterial,
	artifacts recoveryOwnedCleanupArtifactBinding,
) (result OwnedJobDirRemoval, operationErr error) {
	if client == nil || permit.proof == nil || permit.proof.validateLive == nil {
		return OwnedJobDirRemoval{}, ErrInvalidTargetPermit
	}
	frames := make([]recoveryCleanupDirectoryFrame, 0, 8)
	defer func() {
		if closeErr := closeRecoveryCleanupFrames(frames); closeErr != nil && operationErr == nil {
			result = OwnedJobDirRemoval{}
			operationErr = ErrRecoveryTargetUnavailable
		}
	}()
	filesystem, err := client.StatVFS(capturedPath)
	if err != nil || filesystem == nil || filesystem.Fsid == 0 {
		return OwnedJobDirRemoval{}, ErrRecoveryTargetUnavailable
	}
	filesystemID := filesystem.Fsid
	if err := validateRecoveryCleanupDirectory(client, capturedPath, filesystemID, true); err != nil {
		return OwnedJobDirRemoval{}, err
	}
	rootFile, err := client.Open(capturedPath)
	if err != nil {
		if rootFile != nil {
			_ = rootFile.Close()
		}
		return OwnedJobDirRemoval{}, ErrRecoveryTargetUnavailable
	}
	frames = append(frames, recoveryCleanupDirectoryFrame{
		path: capturedPath, file: rootFile, depth: 0,
	})
	removed := 0
	for len(frames) > 0 {
		if err := ctx.Err(); err != nil {
			return OwnedJobDirRemoval{}, err
		}
		last := len(frames) - 1
		frame := &frames[last]
		if !frame.readDone && frame.entryNext >= len(frame.entries) {
			batch, readErr := frame.file.ReadDir(recoveryCleanupReadBatch)
			if readErr != nil && !errors.Is(readErr, io.EOF) {
				return OwnedJobDirRemoval{}, ErrRecoveryTargetUnavailable
			}
			frame.entries = batch
			frame.entryNext = 0
			if len(batch) == 0 || errors.Is(readErr, io.EOF) {
				frame.readDone = true
			}
			if len(batch) > 1 {
				sort.SliceStable(batch, func(left, right int) bool {
					return batch[left].Name() < batch[right].Name()
				})
			}
		}
		if frame.entryNext < len(frame.entries) {
			entry := frame.entries[frame.entryNext]
			frame.entryNext++
			childPath, pathErr := recoveryCleanupChildPath(frame.path, entry.Name(), capturedPath)
			if pathErr != nil {
				return OwnedJobDirRemoval{}, pathErr
			}
			childInfo, lstatErr := client.Lstat(childPath)
			if lstatErr != nil {
				if os.IsNotExist(lstatErr) {
					return OwnedJobDirRemoval{}, ErrRecoveryTargetChanged
				}
				return OwnedJobDirRemoval{}, ErrRecoveryTargetUnavailable
			}
			if childInfo == nil {
				return OwnedJobDirRemoval{}, ErrRecoveryTargetUnavailable
			}
			if childInfo.IsDir() && childInfo.Mode()&os.ModeSymlink == 0 {
				if frame.depth+1 > recoveryCleanupMaxDepth {
					return OwnedJobDirRemoval{}, ErrRecoveryTargetChanged
				}
				if err := validateRecoveryCleanupDirectory(client, childPath, filesystemID, false); err != nil {
					return OwnedJobDirRemoval{}, err
				}
				childFile, openErr := client.Open(childPath)
				if openErr != nil {
					if childFile != nil {
						_ = childFile.Close()
					}
					return OwnedJobDirRemoval{}, ErrRecoveryTargetUnavailable
				}
				frames = append(frames, recoveryCleanupDirectoryFrame{
					path: childPath, file: childFile, depth: frame.depth + 1,
				})
				continue
			}
			if removed >= recoveryCleanupRemoveLimit {
				return OwnedJobDirRemoval{
					Complete: false, RemovedEntries: removed,
					ProgressDigest: recoveryCleanupProgressDigest(permit, removed, false),
				}, nil
			}
			if err := permit.proof.validateLive(ctx, permit); err != nil {
				return OwnedJobDirRemoval{}, err
			}
			if removeErr := client.Remove(childPath); removeErr != nil {
				if os.IsNotExist(removeErr) {
					return OwnedJobDirRemoval{}, ErrRecoveryTargetChanged
				}
				return OwnedJobDirRemoval{}, ErrRecoveryTargetUnavailable
			}
			removed++
			continue
		}
		if !frame.readDone {
			continue
		}
		if frame.file != nil {
			if closeErr := frame.file.Close(); closeErr != nil {
				frame.file = nil
				return OwnedJobDirRemoval{}, ErrRecoveryTargetUnavailable
			}
			frame.file = nil
		}
		if err := validateRecoveryCleanupDirectory(client, frame.path, filesystemID, frame.path == capturedPath); err != nil {
			return OwnedJobDirRemoval{}, err
		}
		if frame.path != capturedPath && removed >= recoveryCleanupRemoveLimit {
			return OwnedJobDirRemoval{
				Complete: false, RemovedEntries: removed,
				ProgressDigest: recoveryCleanupProgressDigest(permit, removed, false),
			}, nil
		}
		if err := permit.proof.validateLive(ctx, permit); err != nil {
			return OwnedJobDirRemoval{}, err
		}
		if removeErr := client.RemoveDirectory(frame.path); removeErr != nil {
			if os.IsNotExist(removeErr) {
				return OwnedJobDirRemoval{}, ErrRecoveryTargetChanged
			}
			return OwnedJobDirRemoval{}, ErrRecoveryTargetUnavailable
		}
		removed++
		frames = frames[:last]
		if removed >= recoveryCleanupRemoveLimit {
			return OwnedJobDirRemoval{
				Complete: false, RemovedEntries: removed,
				ProgressDigest: recoveryCleanupProgressDigest(permit, removed, false),
			}, nil
		}
	}
	if err := validateRecoveryCanonicalDirectory(client, jobsPath, true); err != nil {
		return OwnedJobDirRemoval{}, err
	}
	finalMissing, err := recoverySFTPPathMissing(client, jobPath)
	if err != nil || !finalMissing {
		if err != nil {
			return OwnedJobDirRemoval{}, err
		}
		return OwnedJobDirRemoval{}, ErrRecoveryTargetChanged
	}
	capturedMissing, err := recoverySFTPPathMissing(client, capturedPath)
	if err != nil || !capturedMissing {
		if err != nil {
			return OwnedJobDirRemoval{}, err
		}
		return OwnedJobDirRemoval{}, ErrRecoveryTargetChanged
	}
	verifiedMarker, _, err := readRecoveryMarkerFile(client, verifiedPath)
	if err != nil {
		return OwnedJobDirRemoval{}, err
	}
	expectedBody := recoveryOwnedCleanupArtifactBody{
		SchemaVersion: 1, KeyVersion: artifacts.keyVersion, JobID: permit.JobID,
		RootID: permit.RootID, RootRevision: permit.RootRevision,
		WorkspaceLocator:    recoveryWorkspaceLocatorDirectory + "/" + permit.JobID,
		MarkerBindingDigest: permit.MarkerBindingDigest, MarkerCreatorID: permit.MarkerCreatorID,
		MarkerCreatorFence: permit.MarkerCreatorFence, MarkerDigest: artifacts.markerDigest,
		CapturedComponent: artifacts.capturedComponent,
	}
	if err := validateRecoveryOwnedCleanupArtifactDocument(
		verifiedMarker, expectedBody, material.Key, recoveryOwnedCleanupVerifiedDomain,
	); err != nil {
		return OwnedJobDirRemoval{}, err
	}
	if removed >= recoveryCleanupRemoveLimit {
		return OwnedJobDirRemoval{
			Complete: false, RemovedEntries: removed,
			ProgressDigest: recoveryCleanupProgressDigest(permit, removed, false),
		}, nil
	}
	if err := permit.proof.validateLive(ctx, permit); err != nil {
		return OwnedJobDirRemoval{}, err
	}
	if removeErr := client.Remove(verifiedPath); removeErr != nil {
		if os.IsNotExist(removeErr) {
			return OwnedJobDirRemoval{}, ErrRecoveryTargetChanged
		}
		return OwnedJobDirRemoval{}, ErrRecoveryTargetUnavailable
	}
	removed++
	for _, value := range []string{jobPath, capturedPath, verifiedPath} {
		missing, missingErr := recoverySFTPPathMissing(client, value)
		if missingErr != nil {
			return OwnedJobDirRemoval{}, missingErr
		}
		if !missing {
			return OwnedJobDirRemoval{}, ErrRecoveryTargetChanged
		}
	}
	return OwnedJobDirRemoval{
		Complete: true, RemovedEntries: removed,
		ProgressDigest: recoveryCleanupProgressDigest(permit, removed, true),
	}, nil
}

func writeRecoveryOwnedCleanupMarker(
	client recoveryTargetSFTPClient,
	markerPath string,
	document []byte,
) error {
	if client == nil || len(document) == 0 || len(document) > recoveryWorkspaceMarkerDocumentMaxBytes {
		return ErrRecoveryTargetUnavailable
	}
	file, err := client.OpenFile(markerPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		if file != nil {
			_ = file.Close()
		}
		if os.IsExist(err) {
			return ErrRecoveryTargetChanged
		}
		return ErrRecoveryTargetUnavailable
	}
	if err := client.Chmod(markerPath, 0o600); err != nil {
		_ = file.Close()
		return ErrRecoveryTargetUnavailable
	}
	if err := validateRecoveryCanonicalRegularFile(client, markerPath, 0o600); err != nil {
		_ = file.Close()
		return err
	}
	if err := writeRecoverySFTPFile(file, document); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return ErrRecoveryTargetUnavailable
	}
	if err := file.Close(); err != nil {
		return ErrRecoveryTargetUnavailable
	}
	written, _, err := readRecoveryMarkerFile(client, markerPath)
	if err != nil {
		return err
	}
	if !bytes.Equal(written, document) {
		return ErrRecoveryTargetChanged
	}
	return nil
}

func recoveryTargetItemWriteAuthority(
	permit TargetWritePermit,
	request TargetWriteAtomicRequest,
	now time.Time,
) (targetItemWriteAuthority, error) {
	authority, err := permit.validateItemWriteAt(now, request)
	if err != nil {
		return targetItemWriteAuthority{}, ErrInvalidTargetPermit
	}
	if authority.operation == RecoveryOperationCreate &&
		authority.expectedPrior == (ExpectedTargetIdentity{Kind: ExpectedTargetAbsent}) &&
		authority.expectedPriorBytes == -1 &&
		authority.artifacts == (recoveryOverwriteArtifactBinding{}) {
		return authority, nil
	}
	if authority.operation == RecoveryOperationOverwrite {
		if authority.targetMode == TargetModeIsolated && authority.artifacts == (recoveryOverwriteArtifactBinding{}) {
			return targetItemWriteAuthority{}, ErrRecoveryTargetUnavailable
		}
		if authority.targetMode != TargetModeInPlace || authority.expectedPrior.Kind != ExpectedTargetPresent ||
			!validDigest(authority.expectedPrior.Digest) || authority.expectedPriorBytes < 0 ||
			!authority.artifacts.valid() {
			return targetItemWriteAuthority{}, ErrInvalidTargetPermit
		}
		return authority, nil
	}
	return targetItemWriteAuthority{}, ErrInvalidTargetPermit
}

func recoveryTargetVerifySessionAuthority(
	permit TargetVerifyPermit,
	object TargetObjectRef,
	expectation TargetVerifyExpectation,
	now time.Time,
) (recoveryTargetSessionBinding, string, TargetMode, error) {
	if expectation.Validate() != nil {
		return recoveryTargetSessionBinding{}, "", "", ErrInvalidTargetPermit
	}
	var binding recoveryTargetSessionBinding
	var jobID string
	var mode TargetMode
	var err error
	if expectation.Kind == TargetPresenceAbsent {
		binding, jobID, mode, err = recoveryTargetDeleteVerifyObjectAuthority(permit, object, now)
	} else {
		binding, jobID, mode, err = recoveryTargetVerifyObjectAuthority(permit, object, now)
	}
	if err != nil {
		return recoveryTargetSessionBinding{}, "", "", err
	}
	return binding, jobID, mode, nil
}

func recoveryTargetDeleteVerifyObjectAuthority(
	permit TargetVerifyPermit,
	object TargetObjectRef,
	now time.Time,
) (recoveryTargetSessionBinding, string, TargetMode, error) {
	binding, jobID, mode, err := recoveryTargetVerifyObjectAuthority(permit, object, now)
	if err != nil {
		return recoveryTargetSessionBinding{}, "", "", err
	}
	proof := permit.permit.proof
	if proof == nil || proof.operation != RecoveryOperationDelete ||
		proof.expectedPrior.Kind != ExpectedTargetPresent || !validDigest(proof.expectedPrior.Digest) {
		return recoveryTargetSessionBinding{}, "", "", ErrInvalidTargetPermit
	}
	return binding, jobID, mode, nil
}

func recoveryTargetVerifyObjectAuthority(
	permit TargetVerifyPermit,
	object TargetObjectRef,
	now time.Time,
) (recoveryTargetSessionBinding, string, TargetMode, error) {
	if permit.ValidateObjectAt(now, object) != nil || permit.permit.proof == nil {
		return recoveryTargetSessionBinding{}, "", "", ErrInvalidTargetPermit
	}
	proof := permit.permit.proof
	binding := proof.sessionBinding
	if !binding.valid() || !validOpaqueID(proof.jobID) || proof.targetMode.Validate() != nil ||
		binding.NodeID != permit.permit.NodeID || binding.RootID != object.RootID ||
		binding.RootLocatorDigest != object.RootLocatorDigest ||
		binding.RootRevision != permit.permit.RootRevision ||
		validateRecoveryVerifyNamespace(object.PrivateRelativeLocator, proof.jobID, proof.targetMode) != nil {
		return recoveryTargetSessionBinding{}, "", "", ErrInvalidTargetPermit
	}
	return binding, proof.jobID, proof.targetMode, nil
}

func validateRecoveryVerifyNamespace(
	privateRelativeLocator string,
	jobID string,
	mode TargetMode,
) error {
	if !validTargetRelativeLocator(privateRelativeLocator) || !validOpaqueID(jobID) || mode.Validate() != nil {
		return ErrInvalidTargetPermit
	}
	if mode == TargetModeInPlace {
		return nil
	}
	components := strings.Split(privateRelativeLocator, "/")
	if len(components) < 3 || components[0] != recoveryWorkspaceLocatorDirectory ||
		components[1] != jobID || components[2] == recoveryWorkspaceMarkerFileName ||
		strings.HasPrefix(components[2], recoveryWorkspaceMarkerTempPrefix) {
		return ErrInvalidTargetPermit
	}
	return nil
}

func (target *recoverySFTPTarget) createOwnedJobDir(
	ctx context.Context,
	client recoveryTargetSFTPClient,
	binding recoveryTargetSessionBinding,
	permit TargetWritePermit,
	request CreateOwnedJobDirRequest,
	now time.Time,
) (OwnedJobDir, error) {
	if client == nil {
		return OwnedJobDir{}, ErrRecoveryTargetUnavailable
	}
	if err := validateRecoveryRootPrefixes(client, binding.RootLocator); err != nil {
		return OwnedJobDir{}, err
	}
	jobsPath := path.Join(binding.RootLocator, recoveryWorkspaceLocatorDirectory)
	jobPath := path.Join(binding.RootLocator, request.Object.PrivateRelativeLocator)
	markerPath := path.Join(jobPath, recoveryWorkspaceMarkerFileName)
	jobsMissing, err := recoverySFTPPathMissing(client, jobsPath)
	if err != nil {
		return OwnedJobDir{}, err
	}
	if !jobsMissing {
		if err := validateRecoveryCanonicalDirectory(client, jobsPath, true); err != nil {
			return OwnedJobDir{}, err
		}
	}

	jobMissing, err := recoverySFTPPathMissing(client, jobPath)
	if err != nil {
		return OwnedJobDir{}, err
	}
	if !jobMissing {
		return target.validateExistingCreatedWorkspace(
			ctx, client, binding, permit, request, jobsPath, jobPath, markerPath, now,
		)
	}
	encoded, err := target.marker.EncodeForCreate(ctx, permit, request, now)
	if err != nil {
		return OwnedJobDir{}, err
	}

	if jobsMissing {
		if err := client.Mkdir(jobsPath); err != nil {
			if validationErr := validateRecoveryCanonicalDirectory(client, jobsPath, true); validationErr != nil {
				return OwnedJobDir{}, ErrRecoveryTargetUnavailable
			}
		} else {
			if err := client.Chmod(jobsPath, 0o700); err != nil {
				return OwnedJobDir{}, ErrRecoveryTargetUnavailable
			}
		}
	}
	if err := validateRecoveryCanonicalDirectory(client, jobsPath, true); err != nil {
		return OwnedJobDir{}, err
	}

	if err := client.Mkdir(jobPath); err != nil {
		if missing, missingErr := recoverySFTPPathMissing(client, jobPath); missingErr != nil || missing {
			return OwnedJobDir{}, ErrRecoveryTargetUnavailable
		}
		return target.validateExistingCreatedWorkspace(
			ctx, client, binding, permit, request, jobsPath, jobPath, markerPath, now,
		)
	}
	if err := client.Chmod(jobPath, 0o700); err != nil {
		return OwnedJobDir{}, ErrRecoveryTargetUnavailable
	}
	if err := validateRecoveryCanonicalDirectory(client, jobPath, true); err != nil {
		return OwnedJobDir{}, err
	}
	if missing, err := recoverySFTPPathMissing(client, markerPath); err != nil || !missing {
		if err != nil {
			return OwnedJobDir{}, err
		}
		return OwnedJobDir{}, ErrRecoveryTargetChanged
	}

	markerSum := sha256.Sum256(encoded)
	tempPath := path.Join(jobPath, recoveryWorkspaceMarkerTempPrefix+hex.EncodeToString(markerSum[:]))
	file, err := client.OpenFile(tempPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		return OwnedJobDir{}, ErrRecoveryTargetUnavailable
	}
	tempOwned := true
	defer func() {
		if tempOwned {
			_ = client.Remove(tempPath)
		}
	}()
	if err := client.Chmod(tempPath, 0o600); err != nil {
		_ = file.Close()
		return OwnedJobDir{}, ErrRecoveryTargetUnavailable
	}
	if err := validateRecoveryCanonicalRegularFile(client, tempPath, 0o600); err != nil {
		_ = file.Close()
		return OwnedJobDir{}, err
	}
	if err := writeRecoverySFTPFile(file, encoded); err != nil {
		_ = file.Close()
		return OwnedJobDir{}, err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return OwnedJobDir{}, ErrRecoveryTargetUnavailable
	}
	if err := file.Close(); err != nil {
		return OwnedJobDir{}, ErrRecoveryTargetUnavailable
	}
	tempBytes, _, err := readRecoveryMarkerFile(client, tempPath)
	if err != nil {
		return OwnedJobDir{}, err
	}
	if !bytes.Equal(tempBytes, encoded) {
		return OwnedJobDir{}, ErrRecoveryTargetChanged
	}
	if err := validateRecoveryRootPrefixes(client, binding.RootLocator); err != nil {
		return OwnedJobDir{}, err
	}
	if err := validateRecoveryCanonicalDirectory(client, jobsPath, true); err != nil {
		return OwnedJobDir{}, err
	}
	if err := validateRecoveryCanonicalDirectory(client, jobPath, true); err != nil {
		return OwnedJobDir{}, err
	}
	if err := validateRecoveryCanonicalRegularFile(client, tempPath, 0o600); err != nil {
		return OwnedJobDir{}, err
	}
	if missing, err := recoverySFTPPathMissing(client, markerPath); err != nil || !missing {
		if err != nil {
			return OwnedJobDir{}, err
		}
		return OwnedJobDir{}, ErrRecoveryTargetChanged
	}
	if err := client.Rename(tempPath, markerPath); err != nil {
		return OwnedJobDir{}, ErrRecoveryTargetUnavailable
	}
	tempOwned = false

	markerBytes, _, err := readRecoveryMarkerFile(client, markerPath)
	if err != nil {
		return OwnedJobDir{}, err
	}
	if err := target.marker.ValidateForCreate(ctx, permit, request, markerBytes, now); err != nil {
		return OwnedJobDir{}, err
	}
	if err := validateRecoveryRootPrefixes(client, binding.RootLocator); err != nil {
		return OwnedJobDir{}, err
	}
	if err := validateRecoveryCanonicalDirectory(client, jobsPath, true); err != nil {
		return OwnedJobDir{}, err
	}
	if err := validateRecoveryCanonicalDirectory(client, jobPath, true); err != nil {
		return OwnedJobDir{}, err
	}
	return recoveryOwnedJobDirObservation(binding, request, markerBytes), nil
}

func (target *recoverySFTPTarget) validateExistingCreatedWorkspace(
	ctx context.Context,
	client recoveryTargetSFTPClient,
	binding recoveryTargetSessionBinding,
	permit TargetWritePermit,
	request CreateOwnedJobDirRequest,
	jobsPath string,
	jobPath string,
	markerPath string,
	now time.Time,
) (OwnedJobDir, error) {
	if err := validateRecoveryCanonicalDirectory(client, jobsPath, true); err != nil {
		return OwnedJobDir{}, err
	}
	if err := validateRecoveryCanonicalDirectory(client, jobPath, true); err != nil {
		return OwnedJobDir{}, err
	}
	markerBytes, _, err := readRecoveryMarkerFile(client, markerPath)
	if err != nil {
		if errors.Is(err, ErrRecoveryTargetUnavailable) {
			if missing, missingErr := recoverySFTPPathMissing(client, markerPath); missingErr == nil && missing {
				return OwnedJobDir{}, ErrRecoveryTargetChanged
			}
		}
		return OwnedJobDir{}, err
	}
	if err := target.marker.ValidateForCreate(ctx, permit, request, markerBytes, now); err != nil {
		return OwnedJobDir{}, err
	}
	if err := validateRecoveryRootPrefixes(client, binding.RootLocator); err != nil {
		return OwnedJobDir{}, err
	}
	if err := validateRecoveryCanonicalDirectory(client, jobsPath, true); err != nil {
		return OwnedJobDir{}, err
	}
	if err := validateRecoveryCanonicalDirectory(client, jobPath, true); err != nil {
		return OwnedJobDir{}, err
	}
	return recoveryOwnedJobDirObservation(binding, request, markerBytes), nil
}

func (target *recoverySFTPTarget) validateOwnedJobDir(
	ctx context.Context,
	client recoveryTargetSFTPClient,
	binding recoveryTargetSessionBinding,
	permit TargetCleanupPermit,
	request ValidateOwnedJobDirRequest,
	now time.Time,
) (OwnedJobDirValidation, error) {
	if client == nil {
		return OwnedJobDirValidation{}, ErrRecoveryTargetUnavailable
	}
	if err := validateRecoveryRootPrefixes(client, binding.RootLocator); err != nil {
		return OwnedJobDirValidation{}, err
	}
	jobsPath := path.Join(binding.RootLocator, recoveryWorkspaceLocatorDirectory)
	jobPath := path.Join(binding.RootLocator, request.Object.PrivateRelativeLocator)
	markerPath := path.Join(jobPath, recoveryWorkspaceMarkerFileName)
	if err := validateRecoveryCanonicalDirectory(client, jobsPath, true); err != nil {
		return OwnedJobDirValidation{}, err
	}
	if err := validateRecoveryCanonicalDirectory(client, jobPath, true); err != nil {
		return OwnedJobDirValidation{}, err
	}
	markerBytes, _, err := readRecoveryMarkerFile(client, markerPath)
	if err != nil {
		return OwnedJobDirValidation{}, err
	}
	if err := validateRecoveryRootPrefixes(client, binding.RootLocator); err != nil {
		return OwnedJobDirValidation{}, err
	}
	if err := validateRecoveryCanonicalDirectory(client, jobsPath, true); err != nil {
		return OwnedJobDirValidation{}, err
	}
	if err := validateRecoveryCanonicalDirectory(client, jobPath, true); err != nil {
		return OwnedJobDirValidation{}, err
	}
	if err := target.marker.ValidateForCleanup(ctx, permit, request, markerBytes, now); err != nil {
		return OwnedJobDirValidation{}, err
	}
	return OwnedJobDirValidation{
		Object: request.Object, MarkerBindingDigest: request.MarkerBindingDigest,
		RootRevision: binding.RootRevision,
		TargetRevision: recoveryOwnedWorkspaceObservationRevision(
			binding, request.Object.PrivateRelativeLocator, markerBytes,
		),
	}, nil
}

func (target *recoverySFTPTarget) validateOwnedJobDirRemoved(
	ctx context.Context,
	client recoveryTargetSFTPClient,
	binding recoveryTargetSessionBinding,
	permit TargetCleanupPermit,
	request RemoveOwnedJobDirRequest,
) (OwnedJobDirRemovalValidation, error) {
	if client == nil || target == nil || target.now == nil || !binding.valid() ||
		permit.ValidateAt(target.now().UTC()) != nil {
		return OwnedJobDirRemovalValidation{}, ErrInvalidTargetPermit
	}
	if err := validateRecoveryRootPrefixes(client, binding.RootLocator); err != nil {
		return OwnedJobDirRemovalValidation{}, err
	}
	jobsPath := path.Join(binding.RootLocator, recoveryWorkspaceLocatorDirectory)
	jobPath := path.Join(binding.RootLocator, request.Object.PrivateRelativeLocator)
	if err := validateRecoveryCanonicalDirectory(client, jobsPath, true); err != nil {
		return OwnedJobDirRemovalValidation{}, err
	}
	finalMissing, err := recoverySFTPPathMissing(client, jobPath)
	if err != nil {
		return OwnedJobDirRemovalValidation{}, err
	}
	if !finalMissing {
		return OwnedJobDirRemovalValidation{}, ErrRecoveryTargetChanged
	}
	capturedPath := path.Join(jobsPath, recoveryOwnedCleanupCapturedComponent(permit))
	capturedMissing, err := recoverySFTPPathMissing(client, capturedPath)
	if err != nil {
		return OwnedJobDirRemovalValidation{}, err
	}
	if !capturedMissing {
		return OwnedJobDirRemovalValidation{}, ErrRecoveryTargetChanged
	}
	if err := observeRecoveryOwnedCleanupVerifiedAbsence(ctx, client, jobsPath); err != nil {
		return OwnedJobDirRemovalValidation{}, err
	}
	targetRevision, err := recoverySFTPTargetAbsentRevision(
		binding.RootRevision, request.Object.PrivateRelativeLocator,
	)
	if err != nil {
		return OwnedJobDirRemovalValidation{}, err
	}
	return OwnedJobDirRemovalValidation{
		Object: request.Object, RootRevision: binding.RootRevision, TargetRevision: targetRevision,
	}, nil
}

func observeRecoveryOwnedCleanupVerifiedAbsence(
	ctx context.Context,
	client recoveryTargetSFTPClient,
	jobsPath string,
) (err error) {
	if client == nil {
		return ErrRecoveryTargetUnavailable
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	directory, openErr := client.Open(jobsPath)
	if openErr != nil {
		if directory != nil {
			_ = directory.Close()
		}
		return ErrRecoveryTargetUnavailable
	}
	defer func() {
		if closeErr := directory.Close(); closeErr != nil && err == nil {
			err = ErrRecoveryTargetUnavailable
		}
	}()
	observed := 0
	for {
		if contextErr := ctx.Err(); contextErr != nil {
			return contextErr
		}
		entries, readErr := directory.ReadDir(recoveryCleanupReadBatch)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return ErrRecoveryTargetUnavailable
		}
		observed += len(entries)
		if observed > recoveryCleanupRemoveLimit {
			return ErrRecoveryTargetUnavailable
		}
		for _, entry := range entries {
			if entry == nil || entry.Name() == "" || entry.Name() == "." || entry.Name() == ".." {
				return ErrRecoveryTargetChanged
			}
			if strings.HasPrefix(entry.Name(), recoveryOwnedCleanupVerifiedPrefix) {
				return ErrRecoveryTargetChanged
			}
		}
		if len(entries) == 0 || errors.Is(readErr, io.EOF) {
			return nil
		}
	}
}

func verifyRecoveryPresentRegularFile(
	client recoveryTargetSFTPClient,
	binding recoveryTargetSessionBinding,
	jobID string,
	mode TargetMode,
	object TargetObjectRef,
	expectation PresentExpectation,
) (TargetVerifyObservation, error) {
	if client == nil {
		return TargetVerifyObservation{}, ErrRecoveryTargetUnavailable
	}
	finalPath, err := validateRecoveryVerifyCanonicalParents(
		client, binding, jobID, mode, object,
	)
	if err != nil {
		return TargetVerifyObservation{}, err
	}
	identityDigest, bytesRead, beforeSnapshot, err := readRecoveryPresentRegularFile(
		client, finalPath, expectation,
	)
	if err != nil {
		return TargetVerifyObservation{}, err
	}
	after, err := client.Lstat(finalPath)
	if err != nil {
		if os.IsNotExist(err) {
			return TargetVerifyObservation{}, ErrRecoveryTargetChanged
		}
		return TargetVerifyObservation{}, ErrRecoveryTargetUnavailable
	}
	if after == nil {
		return TargetVerifyObservation{}, ErrRecoveryTargetUnavailable
	}
	if recoverySFTPFileSnapshotOf(after) != beforeSnapshot {
		return TargetVerifyObservation{}, ErrRecoveryTargetChanged
	}
	if _, err := validateRecoveryVerifyCanonicalParents(
		client, binding, jobID, mode, object,
	); err != nil {
		return TargetVerifyObservation{}, err
	}
	final, err := observeRecoveryCanonicalRegularFile(client, finalPath)
	if err != nil {
		return TargetVerifyObservation{}, err
	}
	if recoverySFTPFileSnapshotOf(final) != beforeSnapshot ||
		identityDigest != expectation.IdentityDigest || bytesRead != expectation.Bytes {
		return TargetVerifyObservation{}, ErrRecoveryTargetChanged
	}
	revision, err := recoverySFTPRegularFileObservationRevision(
		binding, object, identityDigest, bytesRead,
	)
	if err != nil {
		return TargetVerifyObservation{}, err
	}
	return TargetVerifyObservation{
		Kind: TargetPresencePresent,
		Present: &PresentObservation{
			IdentityDigest: identityDigest,
			Bytes:          bytesRead,
		},
		ObservedRevision: revision,
	}, nil
}

type recoveryOverwriteArtifactPaths struct {
	final     string
	intent    string
	prior     string
	post      string
	published string
}

func recoveryOverwriteArtifactPathsFor(
	finalPath string,
	artifacts recoveryOverwriteArtifactBinding,
) (recoveryOverwriteArtifactPaths, error) {
	if finalPath == "" || path.Clean(finalPath) != finalPath || !artifacts.valid() {
		return recoveryOverwriteArtifactPaths{}, ErrInvalidTargetPermit
	}
	parent := path.Dir(finalPath)
	paths := recoveryOverwriteArtifactPaths{
		final:     finalPath,
		intent:    path.Join(parent, artifacts.intentComponent),
		prior:     path.Join(parent, artifacts.priorComponent),
		post:      path.Join(parent, artifacts.postComponent),
		published: path.Join(parent, artifacts.publishedComponent),
	}
	seen := map[string]struct{}{finalPath: {}}
	for component, value := range map[string]string{
		artifacts.intentComponent:    paths.intent,
		artifacts.priorComponent:     paths.prior,
		artifacts.postComponent:      paths.post,
		artifacts.publishedComponent: paths.published,
	} {
		if path.Base(component) != component || path.Dir(value) != parent ||
			path.Base(value) != component || path.Clean(value) != value {
			return recoveryOverwriteArtifactPaths{}, ErrInvalidTargetPermit
		}
		if _, exists := seen[value]; exists {
			return recoveryOverwriteArtifactPaths{}, ErrInvalidTargetPermit
		}
		seen[value] = struct{}{}
	}
	return paths, nil
}

func recoveryOverwritePathMissing(
	client recoveryTargetSFTPClient,
	value string,
) (bool, error) {
	if client == nil || value == "" {
		return false, ErrRecoveryTargetUnavailable
	}
	info, err := client.Lstat(value)
	if err == nil {
		if info == nil {
			return false, ErrRecoveryTargetUnavailable
		}
		return false, nil
	}
	if os.IsNotExist(err) {
		return true, nil
	}
	return false, ErrRecoveryTargetUnavailable
}

func observeRecoveryOverwriteIntent(
	client recoveryTargetSFTPClient,
	value string,
	expected string,
) (bool, error) {
	missing, err := recoveryOverwritePathMissing(client, value)
	if err != nil || missing {
		return false, err
	}
	encoded, readSnapshot, err := readRecoveryMarkerFile(client, value)
	if err != nil {
		return false, err
	}
	if len(encoded) > recoveryOverwriteMarkerDocumentMaxBytes || string(encoded) != expected {
		return false, ErrRecoveryTargetChanged
	}
	canonical, err := observeRecoveryCanonicalRegularFile(client, value)
	if err != nil {
		return false, err
	}
	if canonical == nil || canonical.Mode().Perm() != 0o600 ||
		recoverySFTPFileSnapshotOf(canonical) != readSnapshot {
		return false, ErrRecoveryTargetChanged
	}
	return true, nil
}

func observeRecoveryOverwritePost(
	client recoveryTargetSFTPClient,
	value string,
	expectation PresentExpectation,
) (bool, error) {
	missing, err := recoveryOverwritePathMissing(client, value)
	if err != nil || missing {
		return false, err
	}
	if err := validateRecoveryCanonicalRegularFile(client, value, 0o600); err != nil {
		return false, err
	}
	digest, bytesRead, _, err := readRecoveryPresentRegularFile(client, value, expectation)
	if err != nil {
		return false, err
	}
	if digest != expectation.IdentityDigest || bytesRead != expectation.Bytes {
		return false, ErrRecoveryTargetChanged
	}
	if err := validateRecoveryCanonicalRegularFile(client, value, 0o600); err != nil {
		return false, err
	}
	return true, nil
}

func validateRecoveryOverwriteFutureArtifactsAbsent(
	client recoveryTargetSFTPClient,
	paths recoveryOverwriteArtifactPaths,
) error {
	for _, value := range []string{paths.prior, paths.published} {
		missing, err := recoveryOverwritePathMissing(client, value)
		if err != nil {
			return err
		}
		if !missing {
			return ErrRecoveryTargetChanged
		}
	}
	return nil
}

func verifyRecoveryOverwriteExpectedPrior(
	client recoveryTargetSFTPClient,
	binding recoveryTargetSessionBinding,
	authority targetItemWriteAuthority,
	object TargetObjectRef,
) error {
	observation, err := verifyRecoveryPresentRegularFile(
		client, binding, authority.jobID, TargetModeInPlace, object,
		PresentExpectation{
			IdentityDigest: authority.expectedPrior.Digest,
			Bytes:          authority.expectedPriorBytes,
		},
	)
	if err != nil {
		return err
	}
	if observation.Kind != TargetPresencePresent || observation.Present == nil ||
		observation.Present.IdentityDigest != authority.expectedPrior.Digest ||
		observation.Present.Bytes != authority.expectedPriorBytes ||
		!validOpaqueRevision(observation.ObservedRevision) {
		return ErrRecoveryTargetChanged
	}
	return nil
}

func createRecoveryOverwriteExactArtifact(
	client recoveryTargetSFTPClient,
	value string,
	content io.Reader,
	expectation PresentExpectation,
) error {
	if client == nil || value == "" || content == nil || !expectation.valid() {
		return ErrRecoveryTargetUnavailable
	}
	file, err := client.OpenFile(value, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		if file != nil {
			_ = file.Close()
		}
		return ErrRecoveryTargetUnavailable
	}
	fileClosed := false
	defer func() {
		if !fileClosed {
			_ = file.Close()
		}
	}()
	if err := client.Chmod(value, 0o600); err != nil {
		return ErrRecoveryTargetUnavailable
	}
	if err := validateRecoveryCanonicalRegularFile(client, value, 0o600); err != nil {
		return err
	}
	if err := writeRecoveryRegularContent(
		file, content, expectation.Bytes, expectation.IdentityDigest,
	); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return ErrRecoveryTargetUnavailable
	}
	fileClosed = true
	if err := file.Close(); err != nil {
		return ErrRecoveryTargetUnavailable
	}
	if err := validateRecoveryCanonicalRegularFile(client, value, 0o600); err != nil {
		return err
	}
	digest, bytesRead, _, err := readRecoveryPresentRegularFile(client, value, expectation)
	if err != nil {
		return err
	}
	if digest != expectation.IdentityDigest || bytesRead != expectation.Bytes {
		return ErrRecoveryTargetChanged
	}
	return validateRecoveryCanonicalRegularFile(client, value, 0o600)
}

func validateRecoveryOverwritePreparedTuple(
	client recoveryTargetSFTPClient,
	binding recoveryTargetSessionBinding,
	authority targetItemWriteAuthority,
	request TargetWriteAtomicRequest,
	paths recoveryOverwriteArtifactPaths,
	validateLive func() error,
) error {
	if err := verifyRecoveryOverwriteExpectedPrior(
		client, binding, authority, request.Object,
	); err != nil {
		return err
	}
	intentPresent, err := observeRecoveryOverwriteIntent(
		client, paths.intent, authority.artifacts.intentDocument,
	)
	if err != nil {
		return err
	}
	postPresent, err := observeRecoveryOverwritePost(
		client, paths.post, PresentExpectation{
			IdentityDigest: request.ExpectedDigest, Bytes: request.ExpectedBytes,
		},
	)
	if err != nil {
		return err
	}
	if !intentPresent || !postPresent {
		return ErrRecoveryTargetChanged
	}
	if err := validateRecoveryOverwriteFutureArtifactsAbsent(client, paths); err != nil {
		return err
	}
	return validateLive()
}

func prepareRecoveryOverwriteArtifacts(
	client recoveryTargetSFTPClient,
	binding recoveryTargetSessionBinding,
	authority targetItemWriteAuthority,
	request TargetWriteAtomicRequest,
	validateLive func() error,
) error {
	if client == nil || validateLive == nil || !binding.valid() ||
		authority.sessionBinding != binding || authority.targetMode != TargetModeInPlace ||
		authority.operation != RecoveryOperationOverwrite ||
		authority.expectedPrior.Kind != ExpectedTargetPresent ||
		!validDigest(authority.expectedPrior.Digest) || authority.expectedPriorBytes < 0 ||
		!authority.artifacts.valid() || request.Content == nil {
		return ErrInvalidTargetPermit
	}
	finalPath := path.Join(binding.RootLocator, request.Object.PrivateRelativeLocator)
	paths, err := recoveryOverwriteArtifactPathsFor(finalPath, authority.artifacts)
	if err != nil {
		return err
	}
	if err := verifyRecoveryOverwriteExpectedPrior(
		client, binding, authority, request.Object,
	); err != nil {
		return err
	}
	if err := validateLive(); err != nil {
		return err
	}
	if err := validateRecoveryOverwriteFutureArtifactsAbsent(client, paths); err != nil {
		return err
	}
	intentPresent, err := observeRecoveryOverwriteIntent(
		client, paths.intent, authority.artifacts.intentDocument,
	)
	if err != nil {
		return err
	}
	postExpectation := PresentExpectation{
		IdentityDigest: request.ExpectedDigest, Bytes: request.ExpectedBytes,
	}
	postPresent, err := observeRecoveryOverwritePost(client, paths.post, postExpectation)
	if err != nil {
		return err
	}
	if !intentPresent && postPresent {
		return ErrRecoveryTargetChanged
	}
	if intentPresent && postPresent {
		return validateRecoveryOverwritePreparedTuple(
			client, binding, authority, request, paths, validateLive,
		)
	}
	if !intentPresent {
		if err := verifyRecoveryOverwriteExpectedPrior(
			client, binding, authority, request.Object,
		); err != nil {
			return err
		}
		if err := validateLive(); err != nil {
			return err
		}
		intent := []byte(authority.artifacts.intentDocument)
		intentDigest := sha256.Sum256(intent)
		if err := createRecoveryOverwriteExactArtifact(
			client, paths.intent, bytes.NewReader(intent), PresentExpectation{
				IdentityDigest: hex.EncodeToString(intentDigest[:]), Bytes: int64(len(intent)),
			},
		); err != nil {
			return err
		}
	}
	if !postPresent {
		intentPresent, err = observeRecoveryOverwriteIntent(
			client, paths.intent, authority.artifacts.intentDocument,
		)
		if err != nil {
			return err
		}
		if !intentPresent {
			return ErrRecoveryTargetChanged
		}
		if err := verifyRecoveryOverwriteExpectedPrior(
			client, binding, authority, request.Object,
		); err != nil {
			return err
		}
		if err := validateLive(); err != nil {
			return err
		}
		if err := createRecoveryOverwriteExactArtifact(
			client, paths.post, request.Content, postExpectation,
		); err != nil {
			return err
		}
	}
	return validateRecoveryOverwritePreparedTuple(
		client, binding, authority, request, paths, validateLive,
	)
}

func recoveryOverwriteArtifactObject(
	binding recoveryTargetSessionBinding,
	value string,
) (TargetObjectRef, error) {
	prefix := binding.RootLocator + "/"
	if !binding.valid() || !strings.HasPrefix(value, prefix) || path.Clean(value) != value {
		return TargetObjectRef{}, ErrInvalidTargetPermit
	}
	relativeLocator := strings.TrimPrefix(value, prefix)
	digest, err := TargetPathDigest(
		binding.RootID, binding.RootLocatorDigest, relativeLocator,
	)
	if err != nil {
		return TargetObjectRef{}, ErrInvalidTargetPermit
	}
	object := TargetObjectRef{
		RootID: binding.RootID, RootLocatorDigest: binding.RootLocatorDigest,
		TargetPathDigest: digest, PrivateRelativeLocator: relativeLocator,
	}
	if !object.valid() {
		return TargetObjectRef{}, ErrInvalidTargetPermit
	}
	return object, nil
}

func sameRecoveryCapturedEntry(
	left recoveryDeleteEntryObservation,
	right recoveryDeleteEntryObservation,
) bool {
	return left.result.Kind == right.result.Kind && left.size == right.size &&
		left.mode == right.mode && left.uid == right.uid && left.gid == right.gid &&
		left.mtime == right.mtime && left.payloadFact == right.payloadFact
}

func recoveryCapturedEntryMatchesExpectedPrior(
	observation recoveryDeleteEntryObservation,
	authority targetItemWriteAuthority,
) bool {
	return observation.result.Kind == TargetEntryRegular &&
		observation.size == authority.expectedPriorBytes &&
		observation.payloadFact == authority.expectedPrior.Digest
}

func observeRecoveryOverwriteCapturedEntry(
	client recoveryTargetSFTPClient,
	binding recoveryTargetSessionBinding,
	authority targetItemWriteAuthority,
	value string,
) (recoveryDeleteEntryObservation, error) {
	object, err := recoveryOverwriteArtifactObject(binding, value)
	if err != nil {
		return recoveryDeleteEntryObservation{}, err
	}
	return observeRecoveryDeleteEntryObservationTwice(
		client, binding, authority.jobID, TargetModeInPlace, object,
	)
}

func validateRecoveryOverwriteCapturedEvidence(
	client recoveryTargetSFTPClient,
	authority targetItemWriteAuthority,
	request TargetWriteAtomicRequest,
	paths recoveryOverwriteArtifactPaths,
) error {
	intentPresent, err := observeRecoveryOverwriteIntent(
		client, paths.intent, authority.artifacts.intentDocument,
	)
	if err != nil {
		return err
	}
	postPresent, err := observeRecoveryOverwritePost(
		client, paths.post, PresentExpectation{
			IdentityDigest: request.ExpectedDigest, Bytes: request.ExpectedBytes,
		},
	)
	if err != nil {
		return err
	}
	publishedMissing, err := recoveryOverwritePathMissing(client, paths.published)
	if err != nil {
		return err
	}
	if !intentPresent || !postPresent || !publishedMissing {
		return ErrRecoveryTargetChanged
	}
	return nil
}

func restoreRecoveryOverwriteCapturedMismatch(
	client recoveryTargetSFTPClient,
	binding recoveryTargetSessionBinding,
	authority targetItemWriteAuthority,
	request TargetWriteAtomicRequest,
	paths recoveryOverwriteArtifactPaths,
	captured recoveryDeleteEntryObservation,
	validateLive func() error,
) error {
	if err := validateRecoveryOverwriteCapturedEvidence(
		client, authority, request, paths,
	); err != nil {
		return err
	}
	current, err := observeRecoveryOverwriteCapturedEntry(
		client, binding, authority, paths.prior,
	)
	if err != nil {
		return err
	}
	if !sameRecoveryCapturedEntry(captured, current) {
		return ErrRecoveryTargetChanged
	}
	finalPath, err := validateRecoveryVerifyCanonicalParents(
		client, binding, authority.jobID, TargetModeInPlace, request.Object,
	)
	if err != nil {
		return err
	}
	if finalPath != paths.final {
		return ErrInvalidTargetPermit
	}
	if err := validateLive(); err != nil {
		return err
	}
	if err := validateRecoveryFinalAbsent(client, paths.final); err != nil {
		return err
	}
	if err := client.Rename(paths.prior, paths.final); err != nil {
		return ErrRecoveryTargetUnavailable
	}
	restored, err := observeRecoveryDeleteEntryObservationTwice(
		client, binding, authority.jobID, TargetModeInPlace, request.Object,
	)
	if err != nil {
		return err
	}
	if !sameRecoveryCapturedEntry(captured, restored) {
		return ErrRecoveryTargetChanged
	}
	priorMissing, err := recoveryOverwritePathMissing(client, paths.prior)
	if err != nil {
		return err
	}
	if !priorMissing {
		return ErrRecoveryTargetChanged
	}
	return ErrRecoveryTargetChanged
}

func captureRecoveryOverwritePrior(
	client recoveryTargetSFTPClient,
	binding recoveryTargetSessionBinding,
	authority targetItemWriteAuthority,
	request TargetWriteAtomicRequest,
	validateLive func() error,
) error {
	if client == nil || validateLive == nil || !binding.valid() ||
		authority.sessionBinding != binding || authority.targetMode != TargetModeInPlace ||
		authority.operation != RecoveryOperationOverwrite ||
		authority.expectedPrior.Kind != ExpectedTargetPresent ||
		!validDigest(authority.expectedPrior.Digest) || authority.expectedPriorBytes < 0 ||
		!authority.artifacts.valid() {
		return ErrInvalidTargetPermit
	}
	paths, err := recoveryOverwriteArtifactPathsFor(
		path.Join(binding.RootLocator, request.Object.PrivateRelativeLocator),
		authority.artifacts,
	)
	if err != nil {
		return err
	}
	if err := validateRecoveryOverwritePreparedTuple(
		client, binding, authority, request, paths, validateLive,
	); err != nil {
		return err
	}
	finalPath, err := validateRecoveryVerifyCanonicalParents(
		client, binding, authority.jobID, TargetModeInPlace, request.Object,
	)
	if err != nil {
		return err
	}
	if finalPath != paths.final {
		return ErrInvalidTargetPermit
	}
	if err := validateRecoveryOverwriteFutureArtifactsAbsent(client, paths); err != nil {
		return err
	}
	if err := validateLive(); err != nil {
		return err
	}
	if err := client.Rename(paths.final, paths.prior); err != nil {
		return ErrRecoveryTargetUnavailable
	}
	if err := validateRecoveryFinalAbsent(client, paths.final); err != nil {
		return err
	}
	captured, err := observeRecoveryOverwriteCapturedEntry(
		client, binding, authority, paths.prior,
	)
	if err != nil {
		return err
	}
	if err := validateRecoveryFinalAbsent(client, paths.final); err != nil {
		return err
	}
	if !recoveryCapturedEntryMatchesExpectedPrior(captured, authority) {
		return restoreRecoveryOverwriteCapturedMismatch(
			client, binding, authority, request, paths, captured, validateLive,
		)
	}
	if err := validateRecoveryOverwriteCapturedEvidence(
		client, authority, request, paths,
	); err != nil {
		return err
	}
	if _, err := validateRecoveryVerifyCanonicalParents(
		client, binding, authority.jobID, TargetModeInPlace, request.Object,
	); err != nil {
		return err
	}
	if err := validateRecoveryFinalAbsent(client, paths.final); err != nil {
		return err
	}
	return validateLive()
}

type recoveryOverwriteTupleState uint8

const (
	recoveryOverwriteTupleStateConflicted recoveryOverwriteTupleState = iota
	recoveryOverwriteTupleStateFresh
	recoveryOverwriteTupleStateIntentOnly
	recoveryOverwriteTupleStatePrepared
	recoveryOverwriteTupleStateCaptured
	recoveryOverwriteTupleStatePublishedUnacknowledged
	recoveryOverwriteTupleStateAcknowledged
)

type recoveryOverwriteRegularFacts struct {
	missing bool
	prior   bool
	post    bool
}

type recoveryOverwriteMarkerFacts struct {
	missing bool
	exact   bool
}

type recoveryOverwriteTuple struct {
	final     recoveryOverwriteRegularFacts
	intent    recoveryOverwriteMarkerFacts
	prior     recoveryOverwriteRegularFacts
	post      recoveryOverwriteRegularFacts
	published recoveryOverwriteMarkerFacts
}

func observeRecoveryOverwriteRegularFacts(
	client recoveryTargetSFTPClient,
	value string,
	priorExpectation PresentExpectation,
	postExpectation PresentExpectation,
	acceptPrior bool,
	acceptPost bool,
) (recoveryOverwriteRegularFacts, error) {
	missing, err := recoveryOverwritePathMissing(client, value)
	if err != nil || missing {
		return recoveryOverwriteRegularFacts{missing: missing}, err
	}
	before, err := observeRecoveryCanonicalRegularFile(client, value)
	if err != nil {
		return recoveryOverwriteRegularFacts{}, err
	}
	var readExpectation PresentExpectation
	switch {
	case acceptPrior && before.Size() == priorExpectation.Bytes:
		readExpectation = priorExpectation
	case acceptPost && before.Size() == postExpectation.Bytes:
		readExpectation = postExpectation
	default:
		return recoveryOverwriteRegularFacts{}, ErrRecoveryTargetChanged
	}
	digest, bytesRead, readSnapshot, err := readRecoveryPresentRegularFile(
		client, value, readExpectation,
	)
	if err != nil {
		return recoveryOverwriteRegularFacts{}, err
	}
	after, err := client.Lstat(value)
	if err != nil {
		if os.IsNotExist(err) {
			return recoveryOverwriteRegularFacts{}, ErrRecoveryTargetChanged
		}
		return recoveryOverwriteRegularFacts{}, ErrRecoveryTargetUnavailable
	}
	if after == nil || recoverySFTPFileSnapshotOf(before) != readSnapshot ||
		recoverySFTPFileSnapshotOf(after) != readSnapshot {
		return recoveryOverwriteRegularFacts{}, ErrRecoveryTargetChanged
	}
	canonical, err := observeRecoveryCanonicalRegularFile(client, value)
	if err != nil {
		return recoveryOverwriteRegularFacts{}, err
	}
	if canonical == nil || recoverySFTPFileSnapshotOf(canonical) != readSnapshot {
		return recoveryOverwriteRegularFacts{}, ErrRecoveryTargetChanged
	}
	facts := recoveryOverwriteRegularFacts{
		prior: acceptPrior && bytesRead == priorExpectation.Bytes &&
			digest == priorExpectation.IdentityDigest,
		post: acceptPost && before.Mode().Perm() == 0o600 &&
			bytesRead == postExpectation.Bytes && digest == postExpectation.IdentityDigest,
	}
	if !facts.prior && !facts.post {
		return recoveryOverwriteRegularFacts{}, ErrRecoveryTargetChanged
	}
	return facts, nil
}

func observeRecoveryOverwriteMarkerFacts(
	client recoveryTargetSFTPClient,
	value string,
	expected string,
) (recoveryOverwriteMarkerFacts, error) {
	present, err := observeRecoveryOverwriteIntent(client, value, expected)
	if err != nil {
		return recoveryOverwriteMarkerFacts{}, err
	}
	return recoveryOverwriteMarkerFacts{missing: !present, exact: present}, nil
}

func classifyRecoveryOverwriteTuple(
	client recoveryTargetSFTPClient,
	binding recoveryTargetSessionBinding,
	authority targetItemWriteAuthority,
	request TargetWriteAtomicRequest,
	paths recoveryOverwriteArtifactPaths,
) (recoveryOverwriteTupleState, recoveryOverwriteTuple, error) {
	if client == nil || !binding.valid() || authority.sessionBinding != binding ||
		authority.targetMode != TargetModeInPlace ||
		authority.operation != RecoveryOperationOverwrite ||
		authority.expectedPrior.Kind != ExpectedTargetPresent ||
		!validDigest(authority.expectedPrior.Digest) || authority.expectedPriorBytes < 0 ||
		!authority.artifacts.valid() || !validDigest(request.ExpectedDigest) ||
		request.ExpectedBytes < 0 {
		return recoveryOverwriteTupleStateConflicted, recoveryOverwriteTuple{},
			ErrInvalidTargetPermit
	}
	finalPath, err := validateRecoveryVerifyCanonicalParents(
		client, binding, authority.jobID, TargetModeInPlace, request.Object,
	)
	if err != nil {
		return recoveryOverwriteTupleStateConflicted, recoveryOverwriteTuple{}, err
	}
	if finalPath != paths.final {
		return recoveryOverwriteTupleStateConflicted, recoveryOverwriteTuple{},
			ErrInvalidTargetPermit
	}
	priorExpectation := PresentExpectation{
		IdentityDigest: authority.expectedPrior.Digest,
		Bytes:          authority.expectedPriorBytes,
	}
	postExpectation := PresentExpectation{
		IdentityDigest: request.ExpectedDigest,
		Bytes:          request.ExpectedBytes,
	}
	var tuple recoveryOverwriteTuple
	tuple.final, err = observeRecoveryOverwriteRegularFacts(
		client, paths.final, priorExpectation, postExpectation, true, true,
	)
	if err != nil {
		return recoveryOverwriteTupleStateConflicted, recoveryOverwriteTuple{}, err
	}
	tuple.intent, err = observeRecoveryOverwriteMarkerFacts(
		client, paths.intent, authority.artifacts.intentDocument,
	)
	if err != nil {
		return recoveryOverwriteTupleStateConflicted, recoveryOverwriteTuple{}, err
	}
	tuple.prior, err = observeRecoveryOverwriteRegularFacts(
		client, paths.prior, priorExpectation, postExpectation, true, false,
	)
	if err != nil {
		return recoveryOverwriteTupleStateConflicted, recoveryOverwriteTuple{}, err
	}
	tuple.post, err = observeRecoveryOverwriteRegularFacts(
		client, paths.post, priorExpectation, postExpectation, false, true,
	)
	if err != nil {
		return recoveryOverwriteTupleStateConflicted, recoveryOverwriteTuple{}, err
	}
	tuple.published, err = observeRecoveryOverwriteMarkerFacts(
		client, paths.published, authority.artifacts.publishedDocument,
	)
	if err != nil {
		return recoveryOverwriteTupleStateConflicted, recoveryOverwriteTuple{}, err
	}

	allArtifactsMissing := tuple.intent.missing && tuple.prior.missing &&
		tuple.post.missing && tuple.published.missing
	switch {
	case tuple.final.prior && allArtifactsMissing:
		return recoveryOverwriteTupleStateFresh, tuple, nil
	case tuple.final.prior && tuple.intent.exact && tuple.prior.missing &&
		tuple.post.missing && tuple.published.missing:
		return recoveryOverwriteTupleStateIntentOnly, tuple, nil
	case tuple.final.prior && tuple.intent.exact && tuple.prior.missing &&
		tuple.post.post && tuple.published.missing:
		return recoveryOverwriteTupleStatePrepared, tuple, nil
	case tuple.final.missing && tuple.intent.exact && tuple.prior.prior &&
		tuple.post.post && tuple.published.missing:
		return recoveryOverwriteTupleStateCaptured, tuple, nil
	case tuple.final.post && tuple.intent.exact && tuple.prior.prior &&
		tuple.post.missing && tuple.published.missing:
		return recoveryOverwriteTupleStatePublishedUnacknowledged, tuple, nil
	case tuple.final.post && tuple.published.exact:
		return recoveryOverwriteTupleStateAcknowledged, tuple, nil
	default:
		return recoveryOverwriteTupleStateConflicted, tuple, nil
	}
}

func requireRecoveryOverwriteTupleState(
	client recoveryTargetSFTPClient,
	binding recoveryTargetSessionBinding,
	authority targetItemWriteAuthority,
	request TargetWriteAtomicRequest,
	paths recoveryOverwriteArtifactPaths,
	want recoveryOverwriteTupleState,
) (recoveryOverwriteTuple, error) {
	state, tuple, err := classifyRecoveryOverwriteTuple(
		client, binding, authority, request, paths,
	)
	if err != nil {
		return recoveryOverwriteTuple{}, err
	}
	if state != want {
		return recoveryOverwriteTuple{}, ErrRecoveryTargetChanged
	}
	return tuple, nil
}

func validateRecoveryOverwriteMutationAuthority(
	client recoveryTargetSFTPClient,
	binding recoveryTargetSessionBinding,
	authority targetItemWriteAuthority,
	request TargetWriteAtomicRequest,
	paths recoveryOverwriteArtifactPaths,
	validateLive func() error,
) error {
	finalPath, err := validateRecoveryVerifyCanonicalParents(
		client, binding, authority.jobID, TargetModeInPlace, request.Object,
	)
	if err != nil {
		return err
	}
	if finalPath != paths.final {
		return ErrInvalidTargetPermit
	}
	return validateLive()
}

func publishRecoveryOverwritePost(
	client recoveryTargetSFTPClient,
	binding recoveryTargetSessionBinding,
	authority targetItemWriteAuthority,
	request TargetWriteAtomicRequest,
	paths recoveryOverwriteArtifactPaths,
	validateLive func() error,
) error {
	if _, err := requireRecoveryOverwriteTupleState(
		client, binding, authority, request, paths,
		recoveryOverwriteTupleStateCaptured,
	); err != nil {
		return err
	}
	if err := validateRecoveryOverwriteMutationAuthority(
		client, binding, authority, request, paths, validateLive,
	); err != nil {
		return err
	}
	if err := validateRecoveryFinalAbsent(client, paths.final); err != nil {
		return err
	}
	if err := client.Rename(paths.post, paths.final); err != nil {
		return ErrRecoveryTargetUnavailable
	}
	_, err := requireRecoveryOverwriteTupleState(
		client, binding, authority, request, paths,
		recoveryOverwriteTupleStatePublishedUnacknowledged,
	)
	return err
}

func acknowledgeRecoveryOverwritePublication(
	client recoveryTargetSFTPClient,
	binding recoveryTargetSessionBinding,
	authority targetItemWriteAuthority,
	request TargetWriteAtomicRequest,
	paths recoveryOverwriteArtifactPaths,
	validateLive func() error,
) error {
	if _, err := requireRecoveryOverwriteTupleState(
		client, binding, authority, request, paths,
		recoveryOverwriteTupleStatePublishedUnacknowledged,
	); err != nil {
		return err
	}
	if err := validateRecoveryOverwriteMutationAuthority(
		client, binding, authority, request, paths, validateLive,
	); err != nil {
		return err
	}
	published := []byte(authority.artifacts.publishedDocument)
	publishedDigest := sha256.Sum256(published)
	if err := createRecoveryOverwriteExactArtifact(
		client, paths.published, bytes.NewReader(published), PresentExpectation{
			IdentityDigest: hex.EncodeToString(publishedDigest[:]),
			Bytes:          int64(len(published)),
		},
	); err != nil {
		return err
	}
	_, err := requireRecoveryOverwriteTupleState(
		client, binding, authority, request, paths,
		recoveryOverwriteTupleStateAcknowledged,
	)
	return err
}

func cleanupRecoveryOverwriteAcknowledgedTupleOne(
	client recoveryTargetSFTPClient,
	binding recoveryTargetSessionBinding,
	authority targetItemWriteAuthority,
	request TargetWriteAtomicRequest,
	paths recoveryOverwriteArtifactPaths,
	validateLive func() error,
) (bool, error) {
	tuple, err := requireRecoveryOverwriteTupleState(
		client, binding, authority, request, paths,
		recoveryOverwriteTupleStateAcknowledged,
	)
	if err != nil {
		return false, err
	}
	type residueKind uint8
	const (
		residuePrior residueKind = iota
		residuePost
		residueIntent
	)
	var kind residueKind
	var value string
	switch {
	case !tuple.prior.missing:
		kind, value = residuePrior, paths.prior
	case !tuple.post.missing:
		kind, value = residuePost, paths.post
	case !tuple.intent.missing:
		kind, value = residueIntent, paths.intent
	default:
		return false, nil
	}
	if err := validateRecoveryOverwriteMutationAuthority(
		client, binding, authority, request, paths, validateLive,
	); err != nil {
		return false, err
	}
	priorExpectation := PresentExpectation{
		IdentityDigest: authority.expectedPrior.Digest,
		Bytes:          authority.expectedPriorBytes,
	}
	postExpectation := PresentExpectation{
		IdentityDigest: request.ExpectedDigest,
		Bytes:          request.ExpectedBytes,
	}
	switch kind {
	case residuePrior:
		facts, observeErr := observeRecoveryOverwriteRegularFacts(
			client, value, priorExpectation, postExpectation, true, false,
		)
		if observeErr != nil || !facts.prior {
			if observeErr != nil {
				return false, observeErr
			}
			return false, ErrRecoveryTargetChanged
		}
	case residuePost:
		facts, observeErr := observeRecoveryOverwriteRegularFacts(
			client, value, priorExpectation, postExpectation, false, true,
		)
		if observeErr != nil || !facts.post {
			if observeErr != nil {
				return false, observeErr
			}
			return false, ErrRecoveryTargetChanged
		}
	case residueIntent:
		facts, observeErr := observeRecoveryOverwriteMarkerFacts(
			client, value, authority.artifacts.intentDocument,
		)
		if observeErr != nil || !facts.exact {
			if observeErr != nil {
				return false, observeErr
			}
			return false, ErrRecoveryTargetChanged
		}
	}
	if err := client.Remove(value); err != nil {
		return false, ErrRecoveryTargetUnavailable
	}
	missing, err := recoveryOverwritePathMissing(client, value)
	if err != nil {
		return false, err
	}
	if !missing {
		return false, ErrRecoveryTargetChanged
	}
	return true, nil
}

func recoveryOverwriteAcknowledgedResult(
	client recoveryTargetSFTPClient,
	binding recoveryTargetSessionBinding,
	authority targetItemWriteAuthority,
	request TargetWriteAtomicRequest,
	paths recoveryOverwriteArtifactPaths,
) (TargetWriteResult, error) {
	tuple, err := requireRecoveryOverwriteTupleState(
		client, binding, authority, request, paths,
		recoveryOverwriteTupleStateAcknowledged,
	)
	if err != nil {
		return TargetWriteResult{}, err
	}
	if !tuple.prior.missing || !tuple.post.missing || !tuple.intent.missing {
		return TargetWriteResult{}, ErrRecoveryTargetChanged
	}
	revision, err := recoverySFTPRegularFileObservationRevision(
		binding, request.Object, request.ExpectedDigest, request.ExpectedBytes,
	)
	if err != nil {
		return TargetWriteResult{}, err
	}
	return TargetWriteResult{
		BytesWritten:   request.ExpectedBytes,
		IdentityDigest: request.ExpectedDigest,
		TargetRevision: revision,
	}, nil
}

func driveRecoveryOverwriteTransitions(
	client recoveryTargetSFTPClient,
	binding recoveryTargetSessionBinding,
	authority targetItemWriteAuthority,
	request TargetWriteAtomicRequest,
	validateLive func() error,
) (TargetWriteResult, error) {
	if validateLive == nil {
		return TargetWriteResult{}, ErrRecoveryTargetUnavailable
	}
	paths, err := recoveryOverwriteArtifactPathsFor(
		path.Join(binding.RootLocator, request.Object.PrivateRelativeLocator),
		authority.artifacts,
	)
	if err != nil {
		return TargetWriteResult{}, err
	}
	const maxTransitions = 8
	for transition := 0; transition < maxTransitions; transition++ {
		state, _, err := classifyRecoveryOverwriteTuple(
			client, binding, authority, request, paths,
		)
		if err != nil {
			return TargetWriteResult{}, err
		}
		switch state {
		case recoveryOverwriteTupleStateFresh, recoveryOverwriteTupleStateIntentOnly:
			err = prepareRecoveryOverwriteArtifacts(
				client, binding, authority, request, validateLive,
			)
		case recoveryOverwriteTupleStatePrepared:
			err = captureRecoveryOverwritePrior(
				client, binding, authority, request, validateLive,
			)
		case recoveryOverwriteTupleStateCaptured:
			err = publishRecoveryOverwritePost(
				client, binding, authority, request, paths, validateLive,
			)
		case recoveryOverwriteTupleStatePublishedUnacknowledged:
			err = acknowledgeRecoveryOverwritePublication(
				client, binding, authority, request, paths, validateLive,
			)
		case recoveryOverwriteTupleStateAcknowledged:
			var removed bool
			removed, err = cleanupRecoveryOverwriteAcknowledgedTupleOne(
				client, binding, authority, request, paths, validateLive,
			)
			if err == nil && !removed {
				return recoveryOverwriteAcknowledgedResult(
					client, binding, authority, request, paths,
				)
			}
		default:
			return TargetWriteResult{}, ErrRecoveryTargetChanged
		}
		if err != nil {
			return TargetWriteResult{}, err
		}
	}
	return TargetWriteResult{}, ErrRecoveryTargetUnavailable
}

func finalizeRecoveryOverwritePublication(
	client recoveryTargetSFTPClient,
	authority targetFinalizeOverwriteAuthority,
	request TargetFinalizeOverwriteRequest,
	validateLive func() error,
) (TargetWriteResult, error) {
	if client == nil || validateLive == nil || !authority.sessionBinding.valid() ||
		authority.jobID == "" || authority.jobItemID == "" || authority.checkpointID == "" ||
		!validDigest(authority.operationDigest) || !authority.object.valid() ||
		request.Object != authority.object || request.ExpectedDigest != authority.expectedPostDigest ||
		request.ExpectedBytes != authority.expectedPostBytes ||
		authority.expectedPrior.Kind != ExpectedTargetPresent ||
		!validDigest(authority.expectedPrior.Digest) || authority.expectedPriorBytes < 0 ||
		!validDigest(authority.expectedPostDigest) || authority.expectedPostBytes < 0 ||
		!authority.artifacts.valid() {
		return TargetWriteResult{}, ErrInvalidTargetPermit
	}
	finalPath, err := validateRecoveryVerifyCanonicalParents(
		client, authority.sessionBinding, authority.jobID, TargetModeInPlace, request.Object,
	)
	if err != nil {
		return TargetWriteResult{}, err
	}
	paths, err := recoveryOverwriteArtifactPathsFor(finalPath, authority.artifacts)
	if err != nil || paths.final != finalPath {
		return TargetWriteResult{}, ErrInvalidTargetPermit
	}
	observeExact := func() (bool, error) {
		final, observeErr := observeRecoveryOverwriteRegularFacts(
			client, paths.final, PresentExpectation{}, PresentExpectation{
				IdentityDigest: request.ExpectedDigest, Bytes: request.ExpectedBytes,
			}, false, true,
		)
		if observeErr != nil || !final.post {
			if observeErr != nil {
				return false, observeErr
			}
			return false, ErrRecoveryTargetChanged
		}
		for _, residue := range []string{paths.prior, paths.post, paths.intent} {
			missing, missingErr := recoveryOverwritePathMissing(client, residue)
			if missingErr != nil || !missing {
				if missingErr != nil {
					return false, missingErr
				}
				return false, ErrRecoveryTargetChanged
			}
		}
		published, publishedErr := observeRecoveryOverwriteMarkerFacts(
			client, paths.published, authority.artifacts.publishedDocument,
		)
		if publishedErr != nil {
			return false, publishedErr
		}
		if !published.missing && !published.exact {
			return false, ErrRecoveryTargetChanged
		}
		return published.exact, nil
	}
	publishedPresent, err := observeExact()
	if err != nil {
		return TargetWriteResult{}, err
	}
	if publishedPresent {
		if err := validateLive(); err != nil {
			return TargetWriteResult{}, err
		}
		if err := client.Remove(paths.published); err != nil {
			return TargetWriteResult{}, ErrRecoveryTargetUnavailable
		}
	}
	publishedPresent, err = observeExact()
	if err != nil {
		return TargetWriteResult{}, err
	}
	if publishedPresent {
		return TargetWriteResult{}, ErrRecoveryTargetChanged
	}
	revision, err := recoverySFTPRegularFileObservationRevision(
		authority.sessionBinding, request.Object, request.ExpectedDigest, request.ExpectedBytes,
	)
	if err != nil {
		return TargetWriteResult{}, err
	}
	return TargetWriteResult{
		BytesWritten: request.ExpectedBytes, IdentityDigest: request.ExpectedDigest,
		TargetRevision: revision,
	}, nil
}

func prepareRecoveryCreateParents(
	client recoveryTargetSFTPClient,
	binding recoveryTargetSessionBinding,
	authority targetItemWriteAuthority,
	object TargetObjectRef,
	validateLive func() error,
) (recoveryPreparedCreateParents, error) {
	if client == nil || validateLive == nil {
		return recoveryPreparedCreateParents{}, ErrRecoveryTargetUnavailable
	}
	if validateRecoveryCreateParentAuthority(binding, authority, object) != nil {
		return recoveryPreparedCreateParents{}, ErrInvalidTargetPermit
	}
	if err := validateRecoveryRootPrefixes(client, binding.RootLocator); err != nil {
		return recoveryPreparedCreateParents{}, err
	}

	components := strings.Split(object.PrivateRelativeLocator, "/")
	prepared := recoveryPreparedCreateParents{
		finalPath: path.Join(binding.RootLocator, object.PrivateRelativeLocator),
		parents:   make([]recoveryCreateParentSnapshot, 0, len(components)-1),
	}
	current := binding.RootLocator
	for index, component := range components[:len(components)-1] {
		current = path.Join(current, component)
		info, err := client.Lstat(current)
		if err == nil {
			observed, validationErr := validateRecoveryCanonicalDirectoryInfo(
				client, current, info, authority.targetMode == TargetModeIsolated,
			)
			if validationErr != nil {
				return recoveryPreparedCreateParents{}, validationErr
			}
			prepared.parents = append(prepared.parents, recoveryCreateParentSnapshot{
				path: current, mode: observed.Mode(),
			})
			continue
		}
		if !os.IsNotExist(err) {
			return recoveryPreparedCreateParents{}, ErrRecoveryTargetUnavailable
		}
		if authority.targetMode == TargetModeInPlace || index < 2 {
			return recoveryPreparedCreateParents{}, ErrRecoveryTargetChanged
		}
		if err := validateLive(); err != nil {
			return recoveryPreparedCreateParents{}, err
		}
		if err := client.Mkdir(current); err != nil {
			winner, winnerErr := client.Lstat(current)
			if winnerErr != nil {
				return recoveryPreparedCreateParents{}, ErrRecoveryTargetUnavailable
			}
			observed, validationErr := validateRecoveryCanonicalDirectoryInfo(
				client, current, winner, true,
			)
			if validationErr != nil {
				return recoveryPreparedCreateParents{}, validationErr
			}
			prepared.parents = append(prepared.parents, recoveryCreateParentSnapshot{
				path: current, mode: observed.Mode(),
			})
			continue
		}
		if err := validateLive(); err != nil {
			return recoveryPreparedCreateParents{}, err
		}
		if err := client.Chmod(current, 0o700); err != nil {
			return recoveryPreparedCreateParents{}, ErrRecoveryTargetUnavailable
		}
		observed, err := observeRecoveryCanonicalDirectory(client, current, true)
		if err != nil {
			return recoveryPreparedCreateParents{}, err
		}
		prepared.parents = append(prepared.parents, recoveryCreateParentSnapshot{
			path: current, mode: observed.Mode(),
		})
	}
	return prepared, nil
}

func revalidateRecoveryCreateParents(
	client recoveryTargetSFTPClient,
	binding recoveryTargetSessionBinding,
	authority targetItemWriteAuthority,
	object TargetObjectRef,
	prepared recoveryPreparedCreateParents,
) error {
	if client == nil {
		return ErrRecoveryTargetUnavailable
	}
	if validateRecoveryCreateParentAuthority(binding, authority, object) != nil {
		return ErrInvalidTargetPermit
	}
	components := strings.Split(object.PrivateRelativeLocator, "/")
	if prepared.finalPath != path.Join(binding.RootLocator, object.PrivateRelativeLocator) ||
		len(prepared.parents) != len(components)-1 {
		return ErrRecoveryTargetChanged
	}
	if err := validateRecoveryRootPrefixes(client, binding.RootLocator); err != nil {
		return err
	}
	current := binding.RootLocator
	for index, component := range components[:len(components)-1] {
		current = path.Join(current, component)
		snapshot := prepared.parents[index]
		if snapshot.path != current {
			return ErrRecoveryTargetChanged
		}
		observed, err := observeRecoveryCanonicalDirectory(
			client, current, authority.targetMode == TargetModeIsolated,
		)
		if err != nil {
			return err
		}
		if observed.Mode() != snapshot.mode {
			return ErrRecoveryTargetChanged
		}
	}
	return nil
}

func writeRecoveryRegularCreate(
	client recoveryTargetSFTPClient,
	binding recoveryTargetSessionBinding,
	authority targetItemWriteAuthority,
	object TargetObjectRef,
	request TargetWriteAtomicRequest,
	prepared recoveryPreparedCreateParents,
	tempPath string,
	validateLive func() error,
) (TargetWriteResult, error) {
	if client == nil || request.Content == nil || prepared.finalPath == "" ||
		tempPath == "" || tempPath == prepared.finalPath || validateLive == nil {
		return TargetWriteResult{}, ErrRecoveryTargetUnavailable
	}
	if err := validateRecoveryFinalAbsent(client, prepared.finalPath); err != nil {
		return TargetWriteResult{}, err
	}
	if err := validateLive(); err != nil {
		return TargetWriteResult{}, err
	}
	file, err := client.OpenFile(tempPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		return TargetWriteResult{}, ErrRecoveryTargetUnavailable
	}
	tempOwned := true
	fileClosed := false
	defer func() {
		if !fileClosed {
			_ = file.Close()
		}
		if tempOwned {
			_ = client.Remove(tempPath)
		}
	}()

	if err := validateLive(); err != nil {
		return TargetWriteResult{}, err
	}
	if err := client.Chmod(tempPath, 0o600); err != nil {
		return TargetWriteResult{}, ErrRecoveryTargetUnavailable
	}
	if err := validateRecoveryCanonicalRegularFile(client, tempPath, 0o600); err != nil {
		return TargetWriteResult{}, err
	}
	if err := writeRecoveryRegularContent(
		file, request.Content, request.ExpectedBytes, request.ExpectedDigest,
	); err != nil {
		return TargetWriteResult{}, err
	}
	if err := file.Sync(); err != nil {
		return TargetWriteResult{}, ErrRecoveryTargetUnavailable
	}
	fileClosed = true
	if err := file.Close(); err != nil {
		return TargetWriteResult{}, ErrRecoveryTargetUnavailable
	}
	if err := validateRecoveryCanonicalRegularFile(client, tempPath, 0o600); err != nil {
		return TargetWriteResult{}, err
	}
	identityDigest, bytesRead, _, err := readRecoveryPresentRegularFile(
		client, tempPath, PresentExpectation{
			IdentityDigest: request.ExpectedDigest,
			Bytes:          request.ExpectedBytes,
		},
	)
	if err != nil {
		return TargetWriteResult{}, err
	}
	if identityDigest != request.ExpectedDigest || bytesRead != request.ExpectedBytes {
		return TargetWriteResult{}, ErrRecoveryTargetChanged
	}
	if err := validateRecoveryCanonicalRegularFile(client, tempPath, 0o600); err != nil {
		return TargetWriteResult{}, err
	}
	if err := revalidateRecoveryCreateParents(
		client, binding, authority, object, prepared,
	); err != nil {
		return TargetWriteResult{}, err
	}
	if err := validateLive(); err != nil {
		return TargetWriteResult{}, err
	}
	if err := validateRecoveryFinalAbsent(client, prepared.finalPath); err != nil {
		return TargetWriteResult{}, err
	}
	if err := client.Rename(tempPath, prepared.finalPath); err != nil {
		return TargetWriteResult{}, ErrRecoveryTargetUnavailable
	}
	tempOwned = false
	if err := validateRecoveryCanonicalRegularFile(client, prepared.finalPath, 0o600); err != nil {
		return TargetWriteResult{}, err
	}
	observation, err := verifyRecoveryPresentRegularFile(
		client, binding, authority.jobID, authority.targetMode, object,
		PresentExpectation{
			IdentityDigest: request.ExpectedDigest,
			Bytes:          request.ExpectedBytes,
		},
	)
	if err != nil {
		return TargetWriteResult{}, err
	}
	if err := validateRecoveryCanonicalRegularFile(client, prepared.finalPath, 0o600); err != nil {
		return TargetWriteResult{}, err
	}
	if err := revalidateRecoveryCreateParents(
		client, binding, authority, object, prepared,
	); err != nil {
		return TargetWriteResult{}, err
	}
	if err := validateLive(); err != nil {
		return TargetWriteResult{}, err
	}
	if observation.Kind != TargetPresencePresent || observation.Present == nil ||
		observation.Present.IdentityDigest != request.ExpectedDigest ||
		observation.Present.Bytes != request.ExpectedBytes ||
		!validOpaqueRevision(observation.ObservedRevision) {
		return TargetWriteResult{}, ErrRecoveryTargetChanged
	}
	return TargetWriteResult{
		BytesWritten: request.ExpectedBytes, IdentityDigest: request.ExpectedDigest,
		TargetRevision: observation.ObservedRevision,
	}, nil
}

func writeRecoveryRegularContent(
	file recoveryTargetSFTPFile,
	content io.Reader,
	expectedBytes int64,
	expectedDigest string,
) error {
	if file == nil || content == nil || expectedBytes < 0 || !validDigest(expectedDigest) {
		return ErrRecoveryTargetUnavailable
	}
	hasher := sha256.New()
	copied, err := io.CopyN(io.MultiWriter(file, hasher), content, expectedBytes)
	if err != nil || copied != expectedBytes {
		return ErrRecoveryTargetUnavailable
	}
	var extra [1]byte
	count, err := content.Read(extra[:])
	if count != 0 || err == nil || !errors.Is(err, io.EOF) {
		return ErrRecoveryTargetUnavailable
	}
	if hex.EncodeToString(hasher.Sum(nil)) != expectedDigest {
		return ErrRecoveryTargetUnavailable
	}
	return nil
}

func validateRecoveryFinalAbsent(
	client recoveryTargetSFTPClient,
	finalPath string,
) error {
	if client == nil || finalPath == "" {
		return ErrRecoveryTargetUnavailable
	}
	_, err := client.Lstat(finalPath)
	if err == nil {
		return ErrRecoveryTargetChanged
	}
	if os.IsNotExist(err) {
		return nil
	}
	return ErrRecoveryTargetUnavailable
}

func validateRecoveryCreateParentAuthority(
	binding recoveryTargetSessionBinding,
	authority targetItemWriteAuthority,
	object TargetObjectRef,
) error {
	if !binding.valid() || authority.sessionBinding != binding ||
		!validOpaqueID(authority.jobID) || authority.targetMode.Validate() != nil ||
		authority.operation != RecoveryOperationCreate ||
		authority.expectedPrior != (ExpectedTargetIdentity{Kind: ExpectedTargetAbsent}) ||
		!object.valid() ||
		validateRecoveryVerifyNamespace(
			object.PrivateRelativeLocator, authority.jobID, authority.targetMode,
		) != nil {
		return ErrInvalidTargetPermit
	}
	return nil
}

func validateRecoveryVerifyCanonicalParents(
	client recoveryTargetSFTPClient,
	binding recoveryTargetSessionBinding,
	jobID string,
	mode TargetMode,
	object TargetObjectRef,
) (string, error) {
	if client == nil || !binding.valid() || !object.valid() ||
		validateRecoveryVerifyNamespace(object.PrivateRelativeLocator, jobID, mode) != nil {
		return "", ErrInvalidTargetPermit
	}
	if err := validateRecoveryRootPrefixes(client, binding.RootLocator); err != nil {
		return "", err
	}
	components := strings.Split(object.PrivateRelativeLocator, "/")
	current := binding.RootLocator
	for index, component := range components[:len(components)-1] {
		current = path.Join(current, component)
		requirePrivateMode := mode == TargetModeIsolated && index < 2
		if err := validateRecoveryCanonicalDirectory(client, current, requirePrivateMode); err != nil {
			return "", err
		}
	}
	return path.Join(binding.RootLocator, object.PrivateRelativeLocator), nil
}

func validateRecoveryRootPrefixes(client recoveryTargetSFTPClient, root string) error {
	if client == nil || !path.IsAbs(root) || path.Clean(root) != root || root == "/" {
		return ErrRecoveryTargetChanged
	}
	if err := validateRecoveryCanonicalDirectory(client, "/", false); err != nil {
		return err
	}
	current := ""
	for _, component := range strings.Split(strings.TrimPrefix(root, "/"), "/") {
		if component == "" {
			return ErrRecoveryTargetChanged
		}
		current = path.Join(current, "/", component)
		if err := validateRecoveryCanonicalDirectory(client, current, false); err != nil {
			return err
		}
	}
	return nil
}

func validateRecoveryCanonicalDirectory(
	client recoveryTargetSFTPClient,
	value string,
	requirePrivateMode bool,
) error {
	_, err := observeRecoveryCanonicalDirectory(client, value, requirePrivateMode)
	return err
}

func observeRecoveryCanonicalDirectory(
	client recoveryTargetSFTPClient,
	value string,
	requirePrivateMode bool,
) (os.FileInfo, error) {
	info, err := client.Lstat(value)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrRecoveryTargetChanged
		}
		return nil, ErrRecoveryTargetUnavailable
	}
	return validateRecoveryCanonicalDirectoryInfo(client, value, info, requirePrivateMode)
}

func validateRecoveryCanonicalDirectoryInfo(
	client recoveryTargetSFTPClient,
	value string,
	info os.FileInfo,
	requirePrivateMode bool,
) (os.FileInfo, error) {
	if info == nil {
		return nil, ErrRecoveryTargetUnavailable
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 ||
		(requirePrivateMode && info.Mode().Perm() != 0o700) {
		return nil, ErrRecoveryTargetChanged
	}
	canonical, err := client.RealPath(value)
	if err != nil {
		return nil, ErrRecoveryTargetUnavailable
	}
	if canonical != value {
		return nil, ErrRecoveryTargetChanged
	}
	return info, nil
}

func validateRecoveryCanonicalRegularFile(
	client recoveryTargetSFTPClient,
	value string,
	mode os.FileMode,
) error {
	info, err := observeRecoveryCanonicalRegularFile(client, value)
	if err != nil {
		return err
	}
	if info.Mode().Perm() != mode {
		return ErrRecoveryTargetChanged
	}
	return nil
}

func observeRecoveryCanonicalRegularFile(
	client recoveryTargetSFTPClient,
	value string,
) (os.FileInfo, error) {
	info, err := client.Lstat(value)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrRecoveryTargetChanged
		}
		return nil, ErrRecoveryTargetUnavailable
	}
	if info == nil {
		return nil, ErrRecoveryTargetUnavailable
	}
	if !info.Mode().IsRegular() {
		return nil, ErrRecoveryTargetChanged
	}
	canonical, err := client.RealPath(value)
	if err != nil {
		return nil, ErrRecoveryTargetUnavailable
	}
	if canonical != value {
		return nil, ErrRecoveryTargetChanged
	}
	return info, nil
}

func recoverySFTPPathMissing(client recoveryTargetSFTPClient, value string) (bool, error) {
	_, err := client.Lstat(value)
	if err == nil {
		return false, nil
	}
	if os.IsNotExist(err) {
		return true, nil
	}
	return false, ErrRecoveryTargetUnavailable
}

func writeRecoverySFTPFile(file recoveryTargetSFTPFile, value []byte) error {
	for len(value) > 0 {
		written, err := file.Write(value)
		if written > 0 {
			value = value[written:]
		}
		if err != nil || written <= 0 {
			return ErrRecoveryTargetUnavailable
		}
	}
	return nil
}

type recoverySFTPFileSnapshot struct {
	size    int64
	mode    os.FileMode
	modTime time.Time
}

func recoverySFTPFileSnapshotOf(info os.FileInfo) recoverySFTPFileSnapshot {
	return recoverySFTPFileSnapshot{size: info.Size(), mode: info.Mode(), modTime: info.ModTime()}
}

func readRecoveryMarkerFile(
	client recoveryTargetSFTPClient,
	value string,
) ([]byte, recoverySFTPFileSnapshot, error) {
	if err := validateRecoveryCanonicalRegularFile(client, value, 0o600); err != nil {
		return nil, recoverySFTPFileSnapshot{}, err
	}
	before, err := client.Lstat(value)
	if err != nil || before.Size() <= 0 || before.Size() > recoveryWorkspaceMarkerDocumentMaxBytes {
		if err != nil {
			return nil, recoverySFTPFileSnapshot{}, ErrRecoveryTargetUnavailable
		}
		return nil, recoverySFTPFileSnapshot{}, ErrRecoveryTargetChanged
	}
	file, err := client.Open(value)
	if err != nil {
		if file != nil {
			if closeErr := file.Close(); closeErr != nil {
				return nil, recoverySFTPFileSnapshot{}, ErrRecoveryTargetUnavailable
			}
		}
		if os.IsNotExist(err) {
			return nil, recoverySFTPFileSnapshot{}, ErrRecoveryTargetChanged
		}
		return nil, recoverySFTPFileSnapshot{}, ErrRecoveryTargetUnavailable
	}
	opened, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, recoverySFTPFileSnapshot{}, ErrRecoveryTargetUnavailable
	}
	encoded, err := io.ReadAll(io.LimitReader(file, recoveryWorkspaceMarkerDocumentMaxBytes+1))
	if err != nil {
		_ = file.Close()
		return nil, recoverySFTPFileSnapshot{}, ErrRecoveryTargetUnavailable
	}
	if err := file.Close(); err != nil {
		return nil, recoverySFTPFileSnapshot{}, ErrRecoveryTargetUnavailable
	}
	after, err := client.Lstat(value)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, recoverySFTPFileSnapshot{}, ErrRecoveryTargetChanged
		}
		return nil, recoverySFTPFileSnapshot{}, ErrRecoveryTargetUnavailable
	}
	beforeSnapshot := recoverySFTPFileSnapshotOf(before)
	if len(encoded) == 0 || len(encoded) > recoveryWorkspaceMarkerDocumentMaxBytes ||
		int64(len(encoded)) != before.Size() || recoverySFTPFileSnapshotOf(opened) != beforeSnapshot ||
		recoverySFTPFileSnapshotOf(after) != beforeSnapshot {
		return nil, recoverySFTPFileSnapshot{}, ErrRecoveryTargetChanged
	}
	if err := validateRecoveryCanonicalRegularFile(client, value, 0o600); err != nil {
		return nil, recoverySFTPFileSnapshot{}, err
	}
	return encoded, beforeSnapshot, nil
}

func readRecoveryPresentRegularFile(
	client recoveryTargetSFTPClient,
	finalPath string,
	expectation PresentExpectation,
) (string, int64, recoverySFTPFileSnapshot, error) {
	before, err := observeRecoveryCanonicalRegularFile(client, finalPath)
	if err != nil {
		return "", 0, recoverySFTPFileSnapshot{}, err
	}
	if before.Size() != expectation.Bytes {
		return "", 0, recoverySFTPFileSnapshot{}, ErrRecoveryTargetChanged
	}
	file, err := client.Open(finalPath)
	if err != nil {
		if file != nil {
			if closeErr := file.Close(); closeErr != nil {
				return "", 0, recoverySFTPFileSnapshot{}, ErrRecoveryTargetUnavailable
			}
		}
		if os.IsNotExist(err) {
			return "", 0, recoverySFTPFileSnapshot{}, ErrRecoveryTargetChanged
		}
		return "", 0, recoverySFTPFileSnapshot{}, ErrRecoveryTargetUnavailable
	}
	opened, err := file.Stat()
	beforeSnapshot := recoverySFTPFileSnapshotOf(before)
	if err != nil || opened == nil {
		_ = file.Close()
		return "", 0, recoverySFTPFileSnapshot{}, ErrRecoveryTargetUnavailable
	}
	if recoverySFTPFileSnapshotOf(opened) != beforeSnapshot {
		_ = file.Close()
		return "", 0, recoverySFTPFileSnapshot{}, ErrRecoveryTargetChanged
	}
	hasher := sha256.New()
	copied, err := io.CopyN(hasher, file, expectation.Bytes)
	if err != nil || copied != expectation.Bytes {
		_ = file.Close()
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || copied != expectation.Bytes && err == nil {
			return "", 0, recoverySFTPFileSnapshot{}, ErrRecoveryTargetChanged
		}
		return "", 0, recoverySFTPFileSnapshot{}, ErrRecoveryTargetUnavailable
	}
	var extra [1]byte
	count, extraErr := file.Read(extra[:])
	if count != 0 || extraErr == nil {
		_ = file.Close()
		return "", 0, recoverySFTPFileSnapshot{}, ErrRecoveryTargetChanged
	}
	if !errors.Is(extraErr, io.EOF) {
		_ = file.Close()
		return "", 0, recoverySFTPFileSnapshot{}, ErrRecoveryTargetUnavailable
	}
	if err := file.Close(); err != nil {
		return "", 0, recoverySFTPFileSnapshot{}, ErrRecoveryTargetUnavailable
	}
	return hex.EncodeToString(hasher.Sum(nil)), copied, beforeSnapshot, nil
}

func recoveryOwnedJobDirObservation(
	binding recoveryTargetSessionBinding,
	request CreateOwnedJobDirRequest,
	marker []byte,
) OwnedJobDir {
	return OwnedJobDir{
		Object: request.Object, MarkerBindingDigest: request.MarkerBindingDigest,
		TargetRevision: recoveryOwnedWorkspaceObservationRevision(
			binding, request.Object.PrivateRelativeLocator, marker,
		),
	}
}

func recoveryOwnedWorkspaceObservationRevision(
	binding recoveryTargetSessionBinding,
	privateRelativeLocator string,
	marker []byte,
) string {
	markerSum := sha256.Sum256(marker)
	return framedDigest(
		recoveryWorkspaceObservationDomain,
		strconv.FormatUint(uint64(binding.NodeID), 10), binding.RootID,
		binding.RootLocatorDigest, binding.RootRevision,
		privateRelativeLocator, "0700", "0600",
		strconv.Itoa(len(marker)), hex.EncodeToString(markerSum[:]),
	)
}

func recoverySFTPRegularFileObservationRevision(
	binding recoveryTargetSessionBinding,
	object TargetObjectRef,
	identityDigest string,
	bytesRead int64,
) (string, error) {
	return recoverySFTPOpaqueObservationRevision(
		"sftp1:", recoverySFTPRegularFileObservationDomain,
		strconv.FormatUint(uint64(binding.NodeID), 10), binding.RootID,
		binding.RootLocatorDigest, binding.RootRevision,
		object.PrivateRelativeLocator, "regular", identityDigest,
		strconv.FormatInt(bytesRead, 10),
	)
}

func recoveryTargetOperationError(ctx context.Context, err error) error {
	if ctx != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return contextErr
		}
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	for _, sentinel := range []error{
		ErrInvalidTargetPermit, ErrRecoveryTargetChanged,
		ErrInvalidRecoveryWorkspaceMarker, ErrRecoveryWorkspaceMarkerUnavailable,
		ErrRecoveryTargetUnavailable,
	} {
		if errors.Is(err, sentinel) {
			return sentinel
		}
	}
	return ErrRecoveryTargetUnavailable
}

type TargetReconciliationOperation string

const TargetReconciliationScanRoot TargetReconciliationOperation = "scan_root"

type TargetReconciliationPermit struct {
	SchemaVersion       int
	Purpose             TargetPurpose
	Operation           TargetReconciliationOperation
	NodeID              uint
	RootID              string
	RootLocatorDigest   string `json:"-"`
	RootRevision        string
	ExpectedSetDigest   string
	PageLimit           int
	ChainLimit          int
	FindingLimit        int
	Cursor              string `json:"-"`
	AdmissionGeneration string `json:"-"`
	ExpiresAt           time.Time
	proof               *targetReconciliationPermitProof
}

func (permit TargetReconciliationPermit) ValidateRequestAt(
	now time.Time,
	request TargetReconciliationRequest,
) error {
	proof := permit.proof
	if now.IsZero() || permit.SchemaVersion != 1 || permit.Purpose != TargetPurposeReconcile ||
		permit.Operation != TargetReconciliationScanRoot || permit.NodeID == 0 ||
		!validBoundedOpaque(permit.RootID, targetRootIDMax) || request.RootID != permit.RootID ||
		!validDigest(permit.RootLocatorDigest) || !validOpaqueRevision(permit.RootRevision) ||
		!validDigest(permit.ExpectedSetDigest) || permit.PageLimit != recoveryReconciliationPageLimit ||
		permit.ChainLimit != recoveryReconciliationChainLimit ||
		permit.FindingLimit != recoveryReconciliationFindingLimit ||
		len(permit.Cursor) > recoveryReconciliationCursorMax ||
		(permit.AdmissionGeneration != "" && !validOpaqueRevision(permit.AdmissionGeneration)) ||
		!permit.ExpiresAt.After(now) || proof == nil || !proof.sessionBinding.valid() ||
		proof.sessionBinding.nodeID != permit.NodeID || proof.sessionBinding.rootID != permit.RootID ||
		proof.sessionBinding.rootLocatorDigest != permit.RootLocatorDigest ||
		proof.sessionBinding.rootRevision != permit.RootRevision || proof.auditKeyVersion <= 0 ||
		proof.auditKeyVersion > math.MaxUint32 ||
		proof.auditTokenKey == ([sha256.Size]byte{}) || len(proof.expected) > recoveryReconciliationExpectedLimit {
		return ErrInvalidTargetPermit
	}
	priorJobID := ""
	priorComponentToken := ""
	seenComponentTokens := make(map[string]struct{}, len(proof.expected))
	for _, expected := range proof.expected {
		_, duplicateToken := seenComponentTokens[expected.componentToken]
		if !validOpaqueID(expected.jobID) || expected.jobID < priorJobID ||
			(expected.jobID == priorJobID && expected.componentToken <= priorComponentToken) ||
			!validDigest(expected.markerBindingDigest) || !validRecoveryWorkerID(expected.markerCreatorID) ||
			expected.markerCreatorFence == 0 || !validRecoveryReconciliationExpectedState(expected) ||
			!validRecoveryReconciliationComponentToken(expected.componentToken) || duplicateToken {
			return ErrInvalidTargetPermit
		}
		seenComponentTokens[expected.componentToken] = struct{}{}
		priorJobID = expected.jobID
		priorComponentToken = expected.componentToken
	}
	if permit.ExpectedSetDigest != recoveryReconciliationExpectedSetDigest(
		proof.auditKeyVersion, proof.sessionBinding, proof.expected,
	) || proof.bindingDigest != targetReconciliationPermitBindingDigest(
		proof.auditTokenKey, proof.auditKeyVersion, permit, proof.sessionBinding.bindingDigest,
	) {
		return ErrInvalidTargetPermit
	}
	return nil
}

func validRecoveryReconciliationComponentToken(value string) bool {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func validRecoveryReconciliationExpectedState(expected targetReconciliationExpected) bool {
	switch expected.remoteState {
	case recoveryReconciliationRemoteFinal:
		return expected.entryKind == TargetEntryDirectory
	case recoveryReconciliationRemoteDeleteStarted:
		return expected.entryKind == TargetEntryDirectory || expected.entryKind == TargetEntryRegular
	case recoveryReconciliationRemoteAbsent:
		return expected.entryKind == TargetEntryMissing
	default:
		return false
	}
}

func (TargetReconciliationPermit) String() string {
	return redactedRecoveryTargetProduct("TargetReconciliationPermit")
}

func (TargetReconciliationPermit) GoString() string {
	return redactedRecoveryTargetProduct("TargetReconciliationPermit")
}

type targetReconciliationExpected struct {
	componentToken      string
	jobID               string
	entryKind           TargetEntryKind
	remoteState         string
	markerBindingDigest string
	markerCreatorID     string
	markerCreatorFence  uint64
}

type targetReconciliationPermitProof struct {
	sessionBinding  recoveryTargetReconciliationSessionBinding
	auditKeyVersion int
	auditTokenKey   [32]byte
	expected        []targetReconciliationExpected
	bindingDigest   string
}

func (targetReconciliationPermitProof) String() string {
	return redactedRecoveryTargetProduct("targetReconciliationPermitProof")
}

func (targetReconciliationPermitProof) GoString() string {
	return redactedRecoveryTargetProduct("targetReconciliationPermitProof")
}

type TargetReconciliationRequest struct {
	RootID string
}

func (TargetReconciliationRequest) String() string {
	return redactedRecoveryTargetProduct("TargetReconciliationRequest")
}

func (TargetReconciliationRequest) GoString() string {
	return redactedRecoveryTargetProduct("TargetReconciliationRequest")
}

type TargetReconciliationPage struct {
	Complete   bool
	NextCursor string
	Counts     RecoveryReconciliationCounts
	Findings   []RecoveryReconciliationFinding
}

func (TargetReconciliationPage) String() string {
	return redactedRecoveryTargetProduct("TargetReconciliationPage")
}

func (TargetReconciliationPage) GoString() string {
	return redactedRecoveryTargetProduct("TargetReconciliationPage")
}

// TargetPort is deliberately closed. It exposes no generic command runner and
// no arbitrary-path read or mutation method.
type TargetPort interface {
	ProbeRoot(context.Context, TargetPreflightPermit, TargetProbeRequest) (TargetRootProbeFacts, error)
	CreateOwnedJobDir(context.Context, TargetWritePermit, CreateOwnedJobDirRequest) (OwnedJobDir, error)
	Lstat(context.Context, TargetVerifyPermit, TargetLstatRequest) (TargetLstatResult, error)
	CreateDirectory(context.Context, TargetWritePermit, CreateTargetDirectoryRequest) error
	WriteAtomic(context.Context, TargetWritePermit, TargetWriteAtomicRequest) (TargetWriteResult, error)
	FinalizeOverwrite(context.Context, TargetFinalizeOverwritePermit, TargetFinalizeOverwriteRequest) (TargetWriteResult, error)
	Delete(context.Context, TargetDeletePermit, TargetDeleteRequest) (TargetWriteResult, error)
	Verify(context.Context, TargetVerifyPermit, TargetObjectRef, TargetVerifyExpectation) (TargetVerifyObservation, error)
	ValidateOwnedJobDir(context.Context, TargetCleanupPermit, ValidateOwnedJobDirRequest) (OwnedJobDirValidation, error)
	ValidateOwnedJobDirRemoved(context.Context, TargetCleanupPermit, RemoveOwnedJobDirRequest) (OwnedJobDirRemovalValidation, error)
	RemoveOwnedJobDir(context.Context, TargetCleanupPermit, RemoveOwnedJobDirRequest) (OwnedJobDirRemoval, error)
	OpenOwnedResult(context.Context, TargetResultReadPermit, OpenOwnedResultRequest) (io.ReadCloser, error)
}

// TargetReconciliationPort is intentionally separate from TargetPort so a
// read-only root scanner cannot inherit any Recovery write or cleanup method.
type TargetReconciliationPort interface {
	ScanRecoveryRoot(
		context.Context,
		TargetReconciliationPermit,
		TargetReconciliationRequest,
	) (TargetReconciliationPage, error)
}

type TargetObservationPort interface {
	ProbeRoot(context.Context, TargetPreflightPermit, TargetProbeRequest) (TargetRootProbeFacts, error)
}
