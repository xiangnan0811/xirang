package recovery

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/model"
	"xirang/backend/internal/sshutil"
	"xirang/backend/internal/util"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// recoverySourceHostIdentityPosture distinguishes strict verification from
// connection postures that can never establish source authority.
type recoverySourceHostIdentityPosture string

const (
	recoverySourceHostIdentityStrictKnown recoverySourceHostIdentityPosture = "strict_known"
	recoverySourceHostIdentityAcceptNew   recoverySourceHostIdentityPosture = "accept_new"
	recoverySourceHostIdentityInsecure    recoverySourceHostIdentityPosture = "insecure"
	recoverySourceHostIdentityUnknown     recoverySourceHostIdentityPosture = "unknown"
)

type recoverySourceHostIdentityProof struct {
	posture               recoverySourceHostIdentityPosture
	authenticatedIdentity string
	persistentIdentity    string
	bindingDigest         string
}

func (recoverySourceHostIdentityProof) String() string {
	return "recoverySourceHostIdentityProof{redacted}"
}

func (recoverySourceHostIdentityProof) GoString() string {
	return "recoverySourceHostIdentityProof{redacted}"
}

func issueRecoverySourceHostIdentityProof(
	posture recoverySourceHostIdentityPosture,
	authenticatedIdentity string,
	persistentIdentity string,
) recoverySourceHostIdentityProof {
	proof := recoverySourceHostIdentityProof{
		posture: posture, authenticatedIdentity: authenticatedIdentity,
		persistentIdentity: persistentIdentity,
	}
	if posture == recoverySourceHostIdentityStrictKnown &&
		strings.TrimSpace(authenticatedIdentity) != "" && strings.TrimSpace(persistentIdentity) != "" {
		proof.bindingDigest = framedDigest(
			"xirang/recovery/source-known-host-proof/v1",
			string(posture), authenticatedIdentity, persistentIdentity,
		)
	}
	return proof
}

func (proof recoverySourceHostIdentityProof) valid(authenticatedIdentity string) bool {
	return proof.posture == recoverySourceHostIdentityStrictKnown &&
		strings.TrimSpace(authenticatedIdentity) != "" &&
		proof.authenticatedIdentity == authenticatedIdentity &&
		strings.TrimSpace(proof.persistentIdentity) != "" &&
		proof.bindingDigest == framedDigest(
			"xirang/recovery/source-known-host-proof/v1",
			string(proof.posture), proof.authenticatedIdentity, proof.persistentIdentity,
		)
}

func recoverySourceAuthenticatedNodeIdentity(
	proof recoverySourceHostIdentityProof,
	nodeID uint,
	endpoint string,
) (string, bool) {
	endpoint = strings.TrimSpace(endpoint)
	host, port, err := net.SplitHostPort(endpoint)
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	portNumber, portErr := strconv.ParseUint(port, 10, 16)
	if nodeID == 0 || err != nil || portErr != nil || host == "" || portNumber == 0 ||
		!proof.valid(proof.authenticatedIdentity) {
		return "", false
	}
	canonicalEndpoint := net.JoinHostPort(host, strconv.FormatUint(portNumber, 10))
	return framedDigest(
		"xirang/recovery/source-authenticated-registered-node/v1",
		strconv.FormatUint(uint64(nodeID), 10), canonicalEndpoint, proof.bindingDigest,
	), true
}

type recoverySourceNamespacePurpose string

const recoverySourceNamespacePurposePreflight recoverySourceNamespacePurpose = "recovery_preflight"

type recoverySourceNamespaceRequest struct {
	sourceRef                 provider.RsyncRestoreSourceRef
	producingTaskID           uint
	repositoryBindingRevision string
	provenanceRevision        string
}

func (recoverySourceNamespaceRequest) String() string {
	return "recoverySourceNamespaceRequest{redacted}"
}

func (recoverySourceNamespaceRequest) GoString() string {
	return "recoverySourceNamespaceRequest{redacted}"
}

type recoverySourceNamespaceSnapshot struct {
	sourceRef                 provider.RsyncRestoreSourceRef
	producingTaskID           uint
	taskRevision              string
	sourcePath                string
	nodeID                    uint
	nodeRevision              string
	credentialRevision        string
	repositoryBindingRevision string
	provenanceRevision        string
}

func (recoverySourceNamespaceSnapshot) String() string {
	return "recoverySourceNamespaceSnapshot{redacted}"
}

func (recoverySourceNamespaceSnapshot) GoString() string {
	return "recoverySourceNamespaceSnapshot{redacted}"
}

type recoverySourceNamespaceDurable interface {
	CaptureRecoverySourceNamespaceTx(context.Context, *gorm.DB, recoverySourceNamespaceRequest) (recoverySourceNamespaceSnapshot, error)
	RevalidateRecoverySourceNamespaceTx(context.Context, *gorm.DB, recoverySourceNamespaceRequest, recoverySourceNamespaceSnapshot) (recoverySourceNamespaceSnapshot, error)
}

type recoverySourceNamespaceGORMDurable struct {
	now func() time.Time
}

func newRecoverySourceNamespaceGORMDurable(now func() time.Time) *recoverySourceNamespaceGORMDurable {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &recoverySourceNamespaceGORMDurable{now: now}
}

func (durable *recoverySourceNamespaceGORMDurable) CaptureRecoverySourceNamespaceTx(
	ctx context.Context,
	tx *gorm.DB,
	request recoverySourceNamespaceRequest,
) (recoverySourceNamespaceSnapshot, error) {
	return durable.loadRecoverySourceNamespaceTx(ctx, tx, request)
}

func (durable *recoverySourceNamespaceGORMDurable) RevalidateRecoverySourceNamespaceTx(
	ctx context.Context,
	tx *gorm.DB,
	request recoverySourceNamespaceRequest,
	captured recoverySourceNamespaceSnapshot,
) (recoverySourceNamespaceSnapshot, error) {
	if !validRecoverySourceNamespaceSnapshot(request, captured) {
		return recoverySourceNamespaceSnapshot{}, fmt.Errorf("%w: source namespace binding changed", backupasset.ErrConflict)
	}
	return durable.loadRecoverySourceNamespaceTx(ctx, tx, request)
}

func (durable *recoverySourceNamespaceGORMDurable) loadRecoverySourceNamespaceTx(
	ctx context.Context,
	tx *gorm.DB,
	request recoverySourceNamespaceRequest,
) (recoverySourceNamespaceSnapshot, error) {
	if durable == nil || durable.now == nil || ctx == nil || tx == nil || request.producingTaskID == 0 ||
		strings.TrimSpace(request.repositoryBindingRevision) == "" || strings.TrimSpace(request.provenanceRevision) == "" {
		return recoverySourceNamespaceSnapshot{}, fmt.Errorf("%w: source namespace authority unavailable", backupasset.ErrCapabilityUnavailable)
	}
	if err := ctx.Err(); err != nil {
		return recoverySourceNamespaceSnapshot{}, err
	}
	authorityNow := durable.now().UTC()
	if authorityNow.IsZero() {
		return recoverySourceNamespaceSnapshot{}, fmt.Errorf("%w: source namespace authority unavailable", backupasset.ErrCapabilityUnavailable)
	}
	type taskRow struct {
		ID           uint
		NodeID       uint
		RsyncSource  string
		ExecutorType string
		ArchivedAt   *time.Time
		UpdatedAt    time.Time
	}
	var task taskRow
	loaded := tx.WithContext(ctx).Table("tasks").
		Select("id", "node_id", "rsync_source", "executor_type", "archived_at", "updated_at").
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("id = ?", request.producingTaskID).Limit(1).Find(&task)
	sourcePath := strings.TrimSpace(task.RsyncSource)
	if loaded.Error != nil || loaded.RowsAffected != 1 || task.ID != request.producingTaskID || task.NodeID == 0 ||
		task.ArchivedAt != nil || strings.ToLower(strings.TrimSpace(task.ExecutorType)) != "rsync" ||
		task.RsyncSource != sourcePath || sourcePath == "" || strings.ContainsRune(sourcePath, '\x00') ||
		!filepath.IsAbs(sourcePath) || filepath.Clean(sourcePath) != sourcePath || task.UpdatedAt.IsZero() {
		return recoverySourceNamespaceSnapshot{}, fmt.Errorf("%w: source namespace authority unavailable", backupasset.ErrCapabilityUnavailable)
	}
	revisions, err := loadRecoveryTargetRootAuthorityNodeCredential(ctx, tx, task.NodeID, authorityNow, true)
	if err != nil || strings.TrimSpace(revisions.nodeRevision) == "" || strings.TrimSpace(revisions.credentialRevision) == "" {
		return recoverySourceNamespaceSnapshot{}, fmt.Errorf("%w: source namespace authority unavailable", backupasset.ErrCapabilityUnavailable)
	}
	taskRevision := framedDigest(
		"xirang/recovery/source-task-binding-revision/v1",
		strconv.FormatUint(uint64(task.ID), 10),
		task.UpdatedAt.UTC().Format(time.RFC3339Nano),
	)
	return recoverySourceNamespaceSnapshot{
		sourceRef: request.sourceRef, producingTaskID: task.ID,
		taskRevision: taskRevision, sourcePath: sourcePath, nodeID: task.NodeID,
		nodeRevision: revisions.nodeRevision, credentialRevision: revisions.credentialRevision,
		repositoryBindingRevision: request.repositoryBindingRevision,
		provenanceRevision:        request.provenanceRevision,
	}, nil
}

type recoverySourceNamespaceSessionRequest struct {
	producingTaskID    uint
	nodeID             uint
	nodeRevision       string
	credentialRevision string
	purpose            recoverySourceNamespacePurpose
}

func (recoverySourceNamespaceSessionRequest) String() string {
	return "recoverySourceNamespaceSessionRequest{redacted}"
}

func (recoverySourceNamespaceSessionRequest) GoString() string {
	return "recoverySourceNamespaceSessionRequest{redacted}"
}

type recoverySourceNamespaceSFTP interface {
	Lstat(string) (os.FileInfo, error)
	RealPath(string) (string, error)
	StableIdentity(context.Context, string, os.FileInfo) (string, error)
	Close() error
}

type recoverySourceStrictKnownHostVerifier struct {
	callback ssh.HostKeyCallback
	mu       sync.Mutex
	proof    recoverySourceHostIdentityProof
}

func (*recoverySourceStrictKnownHostVerifier) String() string {
	return "recoverySourceStrictKnownHostVerifier{redacted}"
}

func (*recoverySourceStrictKnownHostVerifier) GoString() string {
	return "recoverySourceStrictKnownHostVerifier{redacted}"
}

func newRecoverySourceStrictKnownHostVerifier(path string) (*recoverySourceStrictKnownHostVerifier, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("strict source known-host verifier unavailable")
	}
	info, err := os.Stat(path)
	if err != nil || info == nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("strict source known-host verifier unavailable")
	}
	callback, err := knownhosts.New(path)
	if err != nil {
		return nil, fmt.Errorf("strict source known-host verifier unavailable")
	}
	return &recoverySourceStrictKnownHostVerifier{callback: callback}, nil
}

func (verifier *recoverySourceStrictKnownHostVerifier) Verify(
	hostname string,
	remote net.Addr,
	key ssh.PublicKey,
) error {
	if verifier == nil || verifier.callback == nil || strings.TrimSpace(hostname) == "" || remote == nil || key == nil {
		return fmt.Errorf("strict source host verification unavailable")
	}
	verifier.mu.Lock()
	verifier.proof = recoverySourceHostIdentityProof{}
	verifier.mu.Unlock()
	if err := verifier.callback(hostname, remote, key); err != nil {
		return fmt.Errorf("strict source host verification unavailable")
	}
	authenticatedIdentity := ssh.FingerprintSHA256(key)
	persistentIdentity := framedDigest(
		"xirang/recovery/source-known-host-entry/v1",
		knownhosts.Normalize(hostname), authenticatedIdentity,
	)
	proof := issueRecoverySourceHostIdentityProof(
		recoverySourceHostIdentityStrictKnown,
		authenticatedIdentity,
		persistentIdentity,
	)
	if !proof.valid(authenticatedIdentity) {
		return fmt.Errorf("strict source host verification unavailable")
	}
	verifier.mu.Lock()
	verifier.proof = proof
	verifier.mu.Unlock()
	return nil
}

func (verifier *recoverySourceStrictKnownHostVerifier) Proof() recoverySourceHostIdentityProof {
	if verifier == nil {
		return recoverySourceHostIdentityProof{}
	}
	verifier.mu.Lock()
	defer verifier.mu.Unlock()
	return verifier.proof
}

type recoverySourceNamespaceSFTPBackend interface {
	Lstat(string) (os.FileInfo, error)
	RealPath(string) (string, error)
	Close() error
}

type recoverySourceNamespaceCommandRunner interface {
	Run(context.Context, sshutil.CommandSpec) (sshutil.CommandResult, error)
}

type recoverySourceNamespaceProductionSFTP struct {
	backend recoverySourceNamespaceSFTPBackend
	runner  recoverySourceNamespaceCommandRunner
}

func (client *recoverySourceNamespaceProductionSFTP) Lstat(value string) (os.FileInfo, error) {
	if client == nil || client.backend == nil {
		return nil, fmt.Errorf("source SFTP unavailable")
	}
	return client.backend.Lstat(value)
}

func (client *recoverySourceNamespaceProductionSFTP) RealPath(value string) (string, error) {
	if client == nil || client.backend == nil {
		return "", fmt.Errorf("source SFTP unavailable")
	}
	return client.backend.RealPath(value)
}

func (client *recoverySourceNamespaceProductionSFTP) StableIdentity(
	ctx context.Context,
	value string,
	info os.FileInfo,
) (string, error) {
	if ctx == nil || client == nil || client.runner == nil || info == nil || !info.IsDir() ||
		strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("source object identity unavailable")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	result, err := client.runner.Run(ctx, sshutil.CommandSpec{
		Binary: "stat", Args: []string{"--printf=%d:%i:%f", "--", value},
		Timeout: 5 * time.Second, MaxStdoutBytes: 128, MaxStderrBytes: 128, MaxRecordBytes: 128,
	})
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return "", contextErr
		}
		return "", fmt.Errorf("source object identity unavailable")
	}
	encoded := strings.TrimSpace(string(result.Stdout))
	parts := strings.Split(encoded, ":")
	if len(parts) != 3 || encoded == "" || strings.ContainsAny(encoded, " \t\r\n") {
		return "", fmt.Errorf("source object identity unavailable")
	}
	device, deviceErr := strconv.ParseUint(parts[0], 10, 64)
	inode, inodeErr := strconv.ParseUint(parts[1], 10, 64)
	mode, modeErr := strconv.ParseUint(parts[2], 16, 32)
	const (
		unixFileTypeMask  = 0o170000
		unixDirectoryType = 0o040000
	)
	if deviceErr != nil || inodeErr != nil || modeErr != nil || inode == 0 || mode&unixFileTypeMask != unixDirectoryType {
		return "", fmt.Errorf("source object identity unavailable")
	}
	return framedDigest(
		"xirang/recovery/source-server-object-identity/v1",
		strconv.FormatUint(device, 10), strconv.FormatUint(inode, 10), strconv.FormatUint(mode, 8),
	), nil
}

func (client *recoverySourceNamespaceProductionSFTP) Close() error {
	if client == nil || client.backend == nil {
		return fmt.Errorf("source SFTP unavailable")
	}
	return client.backend.Close()
}

type recoverySourceNamespaceSession struct {
	nodeID                    uint
	nodeRevision              string
	credentialRevision        string
	registeredNodeEndpoint    string
	authenticatedNodeIdentity string
	hostIdentityProof         recoverySourceHostIdentityProof
	sftp                      recoverySourceNamespaceSFTP
	closeSSH                  func() error
	closeOnce                 sync.Once
	closeErr                  error
}

func (*recoverySourceNamespaceSession) String() string {
	return "recoverySourceNamespaceSession{redacted}"
}

func (*recoverySourceNamespaceSession) GoString() string {
	return "recoverySourceNamespaceSession{redacted}"
}

func (session *recoverySourceNamespaceSession) close() error {
	if session == nil {
		return nil
	}
	session.closeOnce.Do(func() {
		if session.sftp != nil {
			session.closeErr = session.sftp.Close()
		}
		if session.closeSSH != nil {
			if err := session.closeSSH(); session.closeErr == nil {
				session.closeErr = err
			}
		}
	})
	return session.closeErr
}

type recoverySourceNamespaceSessionOpener interface {
	OpenRecoverySourceNamespace(context.Context, recoverySourceNamespaceSessionRequest) (*recoverySourceNamespaceSession, error)
}

type recoverySourceNamespaceResolvedSession struct {
	node               model.Node
	nodeRevision       string
	credentialRevision string
}

func (recoverySourceNamespaceResolvedSession) String() string {
	return "recoverySourceNamespaceResolvedSession{redacted}"
}

func (recoverySourceNamespaceResolvedSession) GoString() string {
	return "recoverySourceNamespaceResolvedSession{redacted}"
}

type recoverySourceNamespaceSSHConnection interface {
	Close() error
}

type recoverySourceNamespaceProductionSessionDependencies struct {
	Resolve   func(context.Context, recoverySourceNamespaceSessionRequest) (recoverySourceNamespaceResolvedSession, error)
	BuildAuth func(model.Node, string) ([]ssh.AuthMethod, error)
	Verifier  func() (*recoverySourceStrictKnownHostVerifier, error)
	Dial      func(context.Context, string, string, []ssh.AuthMethod, ssh.HostKeyCallback) (recoverySourceNamespaceSSHConnection, error)
	OpenSFTP  func(recoverySourceNamespaceSSHConnection) (recoverySourceNamespaceSFTP, error)
}

type recoverySourceNamespaceProductionSessions struct {
	resolve   func(context.Context, recoverySourceNamespaceSessionRequest) (recoverySourceNamespaceResolvedSession, error)
	buildAuth func(model.Node, string) ([]ssh.AuthMethod, error)
	verifier  func() (*recoverySourceStrictKnownHostVerifier, error)
	dial      func(context.Context, string, string, []ssh.AuthMethod, ssh.HostKeyCallback) (recoverySourceNamespaceSSHConnection, error)
	openSFTP  func(recoverySourceNamespaceSSHConnection) (recoverySourceNamespaceSFTP, error)
}

func newRecoverySourceNamespaceProductionSessions(
	db *gorm.DB,
	now func() time.Time,
) *recoverySourceNamespaceProductionSessions {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return newRecoverySourceNamespaceProductionSessionsForTest(recoverySourceNamespaceProductionSessionDependencies{
		Resolve: func(ctx context.Context, request recoverySourceNamespaceSessionRequest) (recoverySourceNamespaceResolvedSession, error) {
			return resolveRecoverySourceNamespaceSession(ctx, db, now, request)
		},
		BuildAuth: func(node model.Node, purpose string) ([]ssh.AuthMethod, error) {
			auth, _, err := sshutil.BuildSSHAuthForPurpose(node, db, purpose)
			return auth, err
		},
		Verifier: func() (*recoverySourceStrictKnownHostVerifier, error) {
			rawPath := strings.TrimSpace(util.GetEnvOrDefault("SSH_KNOWN_HOSTS_PATH", "~/.ssh/known_hosts"))
			path, err := util.ExpandHomePath(rawPath)
			if err != nil || strings.TrimSpace(path) == "" {
				return nil, fmt.Errorf("strict source known-host verifier unavailable")
			}
			return newRecoverySourceStrictKnownHostVerifier(path)
		},
		Dial: func(
			ctx context.Context,
			address string,
			user string,
			auth []ssh.AuthMethod,
			callback ssh.HostKeyCallback,
		) (recoverySourceNamespaceSSHConnection, error) {
			return sshutil.DialSSH(ctx, address, user, auth, callback)
		},
		OpenSFTP: func(connection recoverySourceNamespaceSSHConnection) (recoverySourceNamespaceSFTP, error) {
			sshClient, ok := connection.(*ssh.Client)
			if !ok || sshClient == nil {
				return nil, fmt.Errorf("source SSH connection unavailable")
			}
			sftpClient, err := sftp.NewClient(sshClient)
			if err != nil {
				return nil, fmt.Errorf("source SFTP unavailable")
			}
			return &recoverySourceNamespaceProductionSFTP{
				backend: sftpClient,
				runner:  sshutil.NewSSHCommandRunner(sshClient, 1),
			}, nil
		},
	})
}

func newRecoverySourceNamespaceProductionSessionsForTest(
	deps recoverySourceNamespaceProductionSessionDependencies,
) *recoverySourceNamespaceProductionSessions {
	return &recoverySourceNamespaceProductionSessions{
		resolve: deps.Resolve, buildAuth: deps.BuildAuth, verifier: deps.Verifier,
		dial: deps.Dial, openSFTP: deps.OpenSFTP,
	}
}

func (sessions *recoverySourceNamespaceProductionSessions) OpenRecoverySourceNamespace(
	ctx context.Context,
	request recoverySourceNamespaceSessionRequest,
) (*recoverySourceNamespaceSession, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if sessions == nil || sessions.resolve == nil || sessions.buildAuth == nil || sessions.verifier == nil ||
		sessions.dial == nil || sessions.openSFTP == nil || request.producingTaskID == 0 || request.nodeID == 0 ||
		strings.TrimSpace(request.nodeRevision) == "" || strings.TrimSpace(request.credentialRevision) == "" ||
		request.purpose != recoverySourceNamespacePurposePreflight {
		return nil, fmt.Errorf("source namespace session unavailable")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	resolved, err := sessions.resolve(ctx, request)
	if err != nil || resolved.node.ID != request.nodeID || resolved.nodeRevision != request.nodeRevision ||
		resolved.credentialRevision != request.credentialRevision || strings.TrimSpace(resolved.node.Host) == "" ||
		strings.TrimSpace(resolved.node.Username) == "" || strings.ToLower(strings.TrimSpace(resolved.node.AuthType)) != "key" {
		return nil, fmt.Errorf("source namespace session unavailable")
	}
	auth, err := sessions.buildAuth(resolved.node, sshutil.PurposeRecoveryPreflight)
	if err != nil || len(auth) == 0 {
		return nil, fmt.Errorf("source namespace session unavailable")
	}
	verifier, err := sessions.verifier()
	if err != nil || verifier == nil {
		return nil, fmt.Errorf("source namespace session unavailable")
	}
	port := resolved.node.Port
	if port == 0 {
		port = 22
	}
	if port < 1 || port > 65535 {
		return nil, fmt.Errorf("source namespace session unavailable")
	}
	address := net.JoinHostPort(strings.TrimSpace(resolved.node.Host), strconv.Itoa(port))
	connection, dialErr := sessions.dial(
		ctx, address, strings.TrimSpace(resolved.node.Username), auth, verifier.Verify,
	)
	if dialErr != nil || connection == nil {
		if connection != nil {
			_ = connection.Close()
		}
		if contextErr := ctx.Err(); contextErr != nil {
			return nil, contextErr
		}
		return nil, fmt.Errorf("source namespace session unavailable")
	}
	proof := verifier.Proof()
	authenticatedNodeIdentity, identityValid := recoverySourceAuthenticatedNodeIdentity(
		proof, request.nodeID, address,
	)
	if !identityValid {
		_ = connection.Close()
		return nil, fmt.Errorf("source namespace session unavailable")
	}
	sftpClient, openErr := sessions.openSFTP(connection)
	if openErr != nil || sftpClient == nil {
		if sftpClient != nil {
			_ = sftpClient.Close()
		}
		_ = connection.Close()
		if contextErr := ctx.Err(); contextErr != nil {
			return nil, contextErr
		}
		return nil, fmt.Errorf("source namespace session unavailable")
	}
	return &recoverySourceNamespaceSession{
		nodeID: request.nodeID, nodeRevision: request.nodeRevision,
		credentialRevision:        request.credentialRevision,
		registeredNodeEndpoint:    address,
		authenticatedNodeIdentity: authenticatedNodeIdentity,
		hostIdentityProof:         proof,
		sftp:                      sftpClient,
		closeSSH:                  connection.Close,
	}, nil
}

func resolveRecoverySourceNamespaceSession(
	ctx context.Context,
	db *gorm.DB,
	now func() time.Time,
	request recoverySourceNamespaceSessionRequest,
) (recoverySourceNamespaceResolvedSession, error) {
	if ctx == nil || db == nil || now == nil || request.producingTaskID == 0 || request.nodeID == 0 ||
		request.purpose != recoverySourceNamespacePurposePreflight {
		return recoverySourceNamespaceResolvedSession{}, fmt.Errorf("source namespace session unavailable")
	}
	authorityNow := now().UTC()
	if authorityNow.IsZero() {
		return recoverySourceNamespaceResolvedSession{}, fmt.Errorf("source namespace session unavailable")
	}
	var resolved recoverySourceNamespaceResolvedSession
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var task struct {
			ID     uint
			NodeID uint
		}
		loaded := tx.WithContext(ctx).Table("tasks").Select("id", "node_id").
			Where("id = ? AND archived_at IS NULL", request.producingTaskID).Limit(1).Find(&task)
		if loaded.Error != nil || loaded.RowsAffected != 1 || task.ID != request.producingTaskID || task.NodeID != request.nodeID {
			return fmt.Errorf("source namespace session unavailable")
		}
		revisions, revisionErr := loadRecoveryTargetRootAuthorityNodeCredential(ctx, tx, request.nodeID, authorityNow, false)
		if revisionErr != nil || revisions.nodeRevision != request.nodeRevision ||
			revisions.credentialRevision != request.credentialRevision {
			return fmt.Errorf("source namespace session unavailable")
		}
		var node model.Node
		loaded = tx.WithContext(ctx).Where("id = ?", request.nodeID).Limit(1).Find(&node)
		if loaded.Error != nil || loaded.RowsAffected != 1 || node.ID != request.nodeID {
			return fmt.Errorf("source namespace session unavailable")
		}
		resolved = recoverySourceNamespaceResolvedSession{
			node: node, nodeRevision: revisions.nodeRevision,
			credentialRevision: revisions.credentialRevision,
		}
		return nil
	})
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return recoverySourceNamespaceResolvedSession{}, contextErr
		}
		return recoverySourceNamespaceResolvedSession{}, fmt.Errorf("source namespace session unavailable")
	}
	return resolved, nil
}

type recoverySourceNamespaceAuthorityDependencies struct {
	DB          *gorm.DB
	Durable     recoverySourceNamespaceDurable
	Sessions    recoverySourceNamespaceSessionOpener
	Now         func() time.Time
	NewRevision func() (string, error)
}

// RecoverySourceNamespaceRequest carries only Repository-owned scalar
// bindings into the Recovery-owned source observer. Every field is private at
// serialization and formatting boundaries.
type RecoverySourceNamespaceRequest struct {
	SourceRef                 provider.RsyncRestoreSourceRef `json:"-"`
	ProducingTaskID           uint                           `json:"-"`
	RepositoryBindingRevision string                         `json:"-"`
	ProvenanceRevision        string                         `json:"-"`
}

func (RecoverySourceNamespaceRequest) String() string {
	return "RecoverySourceNamespaceRequest{redacted}"
}

func (RecoverySourceNamespaceRequest) GoString() string {
	return "RecoverySourceNamespaceRequest{redacted}"
}

// RecoverySourceNamespaceAuthority is the narrow production seam through
// which Repository transfers one already-pinned managed Rsync source to the
// Recovery-owned authenticated namespace observer.
type RecoverySourceNamespaceAuthority interface {
	ObserveRecoverySourceNamespace(
		context.Context,
		RecoverySourceNamespaceRequest,
		provider.RsyncRestoreSource,
	) (*RecoverySourceNamespaceObservation, error)
}

// RecoverySourceNamespaceObservation is an opaque Recovery-owned source
// capability. Its proof and canonical namespace stay private to this package;
// Repository may only transfer and close the capability.
type RecoverySourceNamespaceObservation struct {
	observation *recoverySourceNamespaceObservation
}

func (*RecoverySourceNamespaceObservation) String() string {
	return "RecoverySourceNamespaceObservation{redacted}"
}

func (*RecoverySourceNamespaceObservation) GoString() string {
	return "RecoverySourceNamespaceObservation{redacted}"
}

func (observation *RecoverySourceNamespaceObservation) OpenDeclaredRegular(
	ctx context.Context,
	entry provider.RestoreEntry,
) (provider.RsyncRestoreSourceStream, error) {
	if observation == nil || observation.observation == nil {
		return nil, provider.ErrRsyncRestoreUnavailable
	}
	return observation.observation.OpenDeclaredRegular(ctx, entry)
}

func (observation *RecoverySourceNamespaceObservation) MaterializeDeclaredEntries(
	ctx context.Context,
	entries []provider.RestoreEntry,
) ([]provider.RestoreEntry, error) {
	if observation == nil || observation.observation == nil {
		return nil, provider.ErrRsyncRestoreUnavailable
	}
	return observation.observation.MaterializeDeclaredEntries(ctx, entries)
}

func (observation *RecoverySourceNamespaceObservation) Revalidate(ctx context.Context) error {
	if observation == nil || observation.observation == nil {
		return provider.ErrRsyncRestoreUnavailable
	}
	return observation.observation.Revalidate(ctx)
}

func (observation *RecoverySourceNamespaceObservation) Close() error {
	if observation == nil || observation.observation == nil {
		return nil
	}
	return observation.observation.Close()
}

// NewRecoverySourceNamespaceAuthority constructs the complete production
// observer. It performs no external I/O until ObserveRecoverySourceNamespace.
func NewRecoverySourceNamespaceAuthority(
	db *gorm.DB,
	now func() time.Time,
) (RecoverySourceNamespaceAuthority, error) {
	if db == nil {
		return nil, fmt.Errorf("%w: source namespace authority unavailable", backupasset.ErrCapabilityUnavailable)
	}
	durable := newRecoverySourceNamespaceGORMDurable(now)
	sessions := newRecoverySourceNamespaceProductionSessions(db, now)
	return newRecoverySourceNamespaceAuthority(recoverySourceNamespaceAuthorityDependencies{
		DB: db, Durable: durable, Sessions: sessions, Now: now,
	}), nil
}

func (authority *recoverySourceNamespaceAuthority) ObserveRecoverySourceNamespace(
	ctx context.Context,
	request RecoverySourceNamespaceRequest,
	pinned provider.RsyncRestoreSource,
) (*RecoverySourceNamespaceObservation, error) {
	observation, err := authority.observe(ctx, recoverySourceNamespaceRequest{
		sourceRef: request.SourceRef, producingTaskID: request.ProducingTaskID,
		repositoryBindingRevision: request.RepositoryBindingRevision,
		provenanceRevision:        request.ProvenanceRevision,
	}, pinned)
	if err != nil {
		return nil, err
	}
	if observation == nil {
		return nil, fmt.Errorf("%w: source namespace authority unavailable", backupasset.ErrCapabilityUnavailable)
	}
	return &RecoverySourceNamespaceObservation{observation: observation}, nil
}

var _ provider.RsyncRestoreSource = (*RecoverySourceNamespaceObservation)(nil)
var _ RecoverySourceNamespaceAuthority = (*recoverySourceNamespaceAuthority)(nil)

type recoverySourceNamespaceProof struct {
	authenticatedNodeIdentity string
	nodeID                    uint
	nodeRevision              string
	credentialRevision        string
	taskRevision              string
	producingTaskID           uint
	repositoryBindingRevision string
	provenanceRevision        string
	sourceRef                 provider.RsyncRestoreSourceRef
	canonicalPath             string
	observationRevision       string
	observedAt                time.Time
}

func (recoverySourceNamespaceProof) String() string {
	return "recoverySourceNamespaceProof{redacted}"
}

func (recoverySourceNamespaceProof) GoString() string {
	return "recoverySourceNamespaceProof{redacted}"
}

type recoverySourceNamespaceObservation struct {
	proof    *recoverySourceNamespaceProof
	pinned   provider.RsyncRestoreSource
	durable  recoverySourceNamespaceDurable
	request  recoverySourceNamespaceRequest
	captured recoverySourceNamespaceSnapshot

	closeOnce sync.Once
	closeErr  error
}

func (observation *recoverySourceNamespaceObservation) String() string {
	return "recoverySourceNamespaceObservation{redacted}"
}

func (observation *recoverySourceNamespaceObservation) GoString() string {
	return "recoverySourceNamespaceObservation{redacted}"
}

func (observation *recoverySourceNamespaceObservation) close() error {
	if observation == nil {
		return nil
	}
	observation.closeOnce.Do(func() {
		if observation.pinned != nil {
			observation.closeErr = observation.pinned.Close()
		}
	})
	return observation.closeErr
}

func (observation *recoverySourceNamespaceObservation) OpenDeclaredRegular(
	ctx context.Context,
	entry provider.RestoreEntry,
) (provider.RsyncRestoreSourceStream, error) {
	if observation == nil || observation.pinned == nil || observation.proof == nil {
		return nil, provider.ErrRsyncRestoreUnavailable
	}
	return observation.pinned.OpenDeclaredRegular(ctx, entry)
}

func (observation *recoverySourceNamespaceObservation) MaterializeDeclaredEntries(
	ctx context.Context,
	entries []provider.RestoreEntry,
) ([]provider.RestoreEntry, error) {
	if observation == nil || observation.pinned == nil || observation.proof == nil {
		return nil, provider.ErrRsyncRestoreUnavailable
	}
	return observation.pinned.MaterializeDeclaredEntries(ctx, entries)
}

func (observation *recoverySourceNamespaceObservation) Revalidate(ctx context.Context) error {
	if observation == nil || observation.pinned == nil || observation.proof == nil {
		return provider.ErrRsyncRestoreUnavailable
	}
	return observation.pinned.Revalidate(ctx)
}

func (observation *recoverySourceNamespaceObservation) Close() error {
	return observation.close()
}

// revalidateTx consumes only the durable source namespace snapshot retained
// by the opaque observation. The pinned source is already closed before this
// is called, so no SSH, SFTP, or provider I/O can occur in the caller-owned
// transaction.
func (observation *recoverySourceNamespaceObservation) revalidateTx(
	ctx context.Context,
	tx *gorm.DB,
) error {
	if observation == nil || observation.proof == nil || observation.durable == nil ||
		ctx == nil || tx == nil || !validRecoverySourceNamespaceSnapshot(observation.request, observation.captured) {
		return fmt.Errorf("%w: source namespace binding changed", backupasset.ErrConflict)
	}
	current, err := observation.durable.RevalidateRecoverySourceNamespaceTx(
		ctx, tx, observation.request, observation.captured,
	)
	if err != nil {
		return sourceNamespaceObservationError(ctx, err)
	}
	if !sameRecoverySourceNamespaceSnapshot(observation.captured, current) {
		return fmt.Errorf("%w: source namespace binding changed", backupasset.ErrConflict)
	}
	return nil
}

func (observation *RecoverySourceNamespaceObservation) revalidateTx(
	ctx context.Context,
	tx *gorm.DB,
) error {
	if observation == nil || observation.observation == nil {
		return fmt.Errorf("%w: source namespace binding changed", backupasset.ErrConflict)
	}
	return observation.observation.revalidateTx(ctx, tx)
}

type recoverySourceNamespaceAuthority struct {
	db          *gorm.DB
	durable     recoverySourceNamespaceDurable
	sessions    recoverySourceNamespaceSessionOpener
	now         func() time.Time
	newRevision func() (string, error)
}

func newRecoverySourceNamespaceAuthority(deps recoverySourceNamespaceAuthorityDependencies) *recoverySourceNamespaceAuthority {
	now := deps.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	newRevision := deps.NewRevision
	if newRevision == nil {
		newRevision = func() (string, error) {
			return framedDigest("xirang/recovery/source-namespace-observation/v1", now().UTC().Format(time.RFC3339Nano)), nil
		}
	}
	return &recoverySourceNamespaceAuthority{
		db: deps.DB, durable: deps.Durable, sessions: deps.Sessions,
		now: now, newRevision: newRevision,
	}
}

func (authority *recoverySourceNamespaceAuthority) observe(
	ctx context.Context,
	request recoverySourceNamespaceRequest,
	pinned provider.RsyncRestoreSource,
) (observation *recoverySourceNamespaceObservation, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ownedPinned := pinned != nil
	defer func() {
		if ownedPinned && pinned != nil {
			_ = pinned.Close()
		}
	}()
	if authority == nil || authority.db == nil || authority.durable == nil || authority.sessions == nil ||
		pinned == nil || request.producingTaskID == 0 || request.repositoryBindingRevision == "" || request.provenanceRevision == "" {
		return nil, fmt.Errorf("%w: source namespace authority unavailable", backupasset.ErrCapabilityUnavailable)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	var captured recoverySourceNamespaceSnapshot
	err = authority.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var captureErr error
		captured, captureErr = authority.durable.CaptureRecoverySourceNamespaceTx(ctx, tx, request)
		return captureErr
	})
	if err != nil {
		return nil, sourceNamespaceObservationError(ctx, err)
	}
	if !validRecoverySourceNamespaceSnapshot(request, captured) {
		return nil, fmt.Errorf("%w: source namespace binding unavailable", backupasset.ErrCapabilityUnavailable)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	session, openErr := authority.sessions.OpenRecoverySourceNamespace(ctx, recoverySourceNamespaceSessionRequest{
		producingTaskID: request.producingTaskID, nodeID: captured.nodeID,
		nodeRevision: captured.nodeRevision, credentialRevision: captured.credentialRevision,
		purpose: recoverySourceNamespacePurposePreflight,
	})
	if session != nil {
		stopWatch, watchDone := watchSourceNamespaceContext(ctx, session)
		defer func() {
			close(stopWatch)
			<-watchDone
		}()
	}
	if openErr != nil || session == nil {
		if session != nil {
			_ = session.close()
		}
		return nil, sourceNamespaceObservationError(ctx, openErr)
	}
	expectedNodeIdentity, identityValid := recoverySourceAuthenticatedNodeIdentity(
		session.hostIdentityProof, session.nodeID, session.registeredNodeEndpoint,
	)
	if !identityValid || session.authenticatedNodeIdentity != expectedNodeIdentity ||
		strings.TrimSpace(session.authenticatedNodeIdentity) == "" || session.nodeID != captured.nodeID ||
		session.nodeRevision != captured.nodeRevision || session.credentialRevision != captured.credentialRevision || session.sftp == nil {
		_ = session.close()
		return nil, fmt.Errorf("%w: source host identity unavailable", backupasset.ErrCapabilityUnavailable)
	}

	first, observeErr := observeRecoverySourceNamespacePath(ctx, session.sftp, captured.sourcePath)
	if observeErr == nil {
		second, secondErr := observeRecoverySourceNamespacePath(ctx, session.sftp, captured.sourcePath)
		if secondErr != nil {
			observeErr = secondErr
		} else if !sameRecoverySourceNamespacePathObservation(first, second) {
			observeErr = errors.New("source namespace changed during observation")
		}
	}
	closeErr := session.close()
	if observeErr != nil || closeErr != nil {
		return nil, sourceNamespaceObservationError(ctx, errors.Join(observeErr, closeErr))
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	var revalidated recoverySourceNamespaceSnapshot
	err = authority.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var revalidateErr error
		revalidated, revalidateErr = authority.durable.RevalidateRecoverySourceNamespaceTx(ctx, tx, request, captured)
		return revalidateErr
	})
	if err != nil {
		return nil, sourceNamespaceObservationError(ctx, err)
	}
	if !sameRecoverySourceNamespaceSnapshot(captured, revalidated) {
		return nil, fmt.Errorf("%w: source namespace binding changed", backupasset.ErrConflict)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	revision, err := authority.newRevision()
	if err != nil || strings.TrimSpace(revision) == "" {
		return nil, fmt.Errorf("%w: source namespace observation revision unavailable", backupasset.ErrCapabilityUnavailable)
	}
	ownedPinned = false
	return &recoverySourceNamespaceObservation{
		proof: &recoverySourceNamespaceProof{
			authenticatedNodeIdentity: session.authenticatedNodeIdentity,
			nodeID:                    captured.nodeID, nodeRevision: captured.nodeRevision,
			credentialRevision: captured.credentialRevision, taskRevision: captured.taskRevision,
			producingTaskID:           captured.producingTaskID,
			repositoryBindingRevision: captured.repositoryBindingRevision,
			provenanceRevision:        captured.provenanceRevision, sourceRef: captured.sourceRef,
			canonicalPath: first.canonicalPath, observationRevision: revision,
			observedAt: authority.now().UTC(),
		},
		pinned: pinned, durable: authority.durable, request: request, captured: captured,
	}, nil
}

type recoverySourceNamespacePathObservation struct {
	canonicalPath string
	components    []recoverySourceNamespaceComponent
}

func (recoverySourceNamespacePathObservation) String() string {
	return "recoverySourceNamespacePathObservation{redacted}"
}

func (recoverySourceNamespacePathObservation) GoString() string {
	return "recoverySourceNamespacePathObservation{redacted}"
}

type recoverySourceNamespaceComponent struct {
	name           string
	mode           os.FileMode
	modTime        time.Time
	stableIdentity string
}

func (recoverySourceNamespaceComponent) String() string {
	return "recoverySourceNamespaceComponent{redacted}"
}

func (recoverySourceNamespaceComponent) GoString() string {
	return "recoverySourceNamespaceComponent{redacted}"
}

func observeRecoverySourceNamespacePath(ctx context.Context, sftp recoverySourceNamespaceSFTP, sourcePath string) (recoverySourceNamespacePathObservation, error) {
	prefixes, ok := recoveryAbsolutePathPrefixes(sourcePath)
	if !ok || sftp == nil {
		return recoverySourceNamespacePathObservation{}, fmt.Errorf("%w: source namespace path unavailable", backupasset.ErrCapabilityUnavailable)
	}
	components := make([]recoverySourceNamespaceComponent, 0, len(prefixes))
	for _, prefix := range prefixes {
		if err := ctx.Err(); err != nil {
			return recoverySourceNamespacePathObservation{}, err
		}
		info, err := sftp.Lstat(prefix)
		if err != nil {
			return recoverySourceNamespacePathObservation{}, err
		}
		if info == nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return recoverySourceNamespacePathObservation{}, errors.New("source namespace component unavailable")
		}
		stableIdentity, err := sftp.StableIdentity(ctx, prefix, info)
		if err != nil || strings.TrimSpace(stableIdentity) == "" {
			return recoverySourceNamespacePathObservation{}, errors.New("source namespace component identity unavailable")
		}
		realPath, err := sftp.RealPath(prefix)
		if err != nil {
			return recoverySourceNamespacePathObservation{}, err
		}
		if realPath != prefix {
			return recoverySourceNamespacePathObservation{}, errors.New("source namespace canonicalization ambiguous")
		}
		components = append(components, recoverySourceNamespaceComponent{
			name: info.Name(), mode: info.Mode(), modTime: info.ModTime(), stableIdentity: stableIdentity,
		})
	}
	return recoverySourceNamespacePathObservation{canonicalPath: sourcePath, components: components}, nil
}

func sameRecoverySourceNamespacePathObservation(left, right recoverySourceNamespacePathObservation) bool {
	if left.canonicalPath != right.canonicalPath || len(left.components) != len(right.components) {
		return false
	}
	for index := range left.components {
		if left.components[index] != right.components[index] {
			return false
		}
	}
	return true
}

func validRecoverySourceNamespaceSnapshot(request recoverySourceNamespaceRequest, snapshot recoverySourceNamespaceSnapshot) bool {
	return snapshot.sourceRef == request.sourceRef && snapshot.producingTaskID == request.producingTaskID &&
		snapshot.repositoryBindingRevision == request.repositoryBindingRevision && snapshot.provenanceRevision == request.provenanceRevision &&
		snapshot.taskRevision != "" && snapshot.sourcePath != "" && snapshot.nodeID != 0 && snapshot.nodeRevision != "" && snapshot.credentialRevision != ""
}

func sameRecoverySourceNamespaceSnapshot(left, right recoverySourceNamespaceSnapshot) bool {
	return left.sourceRef == right.sourceRef && left.producingTaskID == right.producingTaskID && left.taskRevision == right.taskRevision &&
		left.sourcePath == right.sourcePath && left.nodeID == right.nodeID && left.nodeRevision == right.nodeRevision &&
		left.credentialRevision == right.credentialRevision && left.repositoryBindingRevision == right.repositoryBindingRevision &&
		left.provenanceRevision == right.provenanceRevision
}

func sourceNamespaceObservationError(ctx context.Context, err error) error {
	if err == nil {
		return fmt.Errorf("%w: source namespace authority unavailable", backupasset.ErrCapabilityUnavailable)
	}
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, backupasset.ErrConflict) {
		return fmt.Errorf("%w: source namespace binding changed", backupasset.ErrConflict)
	}
	return fmt.Errorf("%w: source namespace authority unavailable", backupasset.ErrCapabilityUnavailable)
}

func watchSourceNamespaceContext(ctx context.Context, session *recoverySourceNamespaceSession) (chan struct{}, <-chan struct{}) {
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case <-ctx.Done():
			_ = session.close()
		case <-stop:
		}
	}()
	return stop, done
}
