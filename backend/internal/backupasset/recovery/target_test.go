package recovery

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"math"
	"net"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/logger"
	"xirang/backend/internal/model"
	"xirang/backend/internal/settings"
	"xirang/backend/internal/sshutil"

	"github.com/pkg/sftp"
	"github.com/rs/zerolog"
	"golang.org/x/crypto/ssh"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type closedTargetPortFake struct{}

func (closedTargetPortFake) ProbeRoot(context.Context, TargetPreflightPermit, TargetProbeRequest) (TargetRootProbeFacts, error) {
	return TargetRootProbeFacts{}, nil
}

func (closedTargetPortFake) CreateOwnedJobDir(
	_ context.Context,
	_ TargetWritePermit,
	request CreateOwnedJobDirRequest,
) (OwnedJobDir, error) {
	return OwnedJobDir{
		Object: request.Object, MarkerBindingDigest: request.MarkerBindingDigest,
		TargetRevision: "target-revision-workspace-created",
	}, nil
}

func (closedTargetPortFake) Lstat(context.Context, TargetVerifyPermit, TargetLstatRequest) (TargetLstatResult, error) {
	return TargetLstatResult{}, nil
}

func (closedTargetPortFake) CreateDirectory(context.Context, TargetWritePermit, CreateTargetDirectoryRequest) error {
	return nil
}

func (closedTargetPortFake) WriteAtomic(context.Context, TargetWritePermit, TargetWriteAtomicRequest) (TargetWriteResult, error) {
	return TargetWriteResult{}, nil
}

func (closedTargetPortFake) FinalizeOverwrite(
	context.Context,
	TargetFinalizeOverwritePermit,
	TargetFinalizeOverwriteRequest,
) (TargetWriteResult, error) {
	return TargetWriteResult{}, nil
}

func (closedTargetPortFake) Delete(context.Context, TargetDeletePermit, TargetDeleteRequest) (TargetWriteResult, error) {
	return TargetWriteResult{}, nil
}

func (closedTargetPortFake) Verify(
	context.Context,
	TargetVerifyPermit,
	TargetObjectRef,
	TargetVerifyExpectation,
) (TargetVerifyObservation, error) {
	return TargetVerifyObservation{}, nil
}

func (closedTargetPortFake) ValidateOwnedJobDir(
	_ context.Context,
	permit TargetCleanupPermit,
	request ValidateOwnedJobDirRequest,
) (OwnedJobDirValidation, error) {
	return OwnedJobDirValidation{
		Object:              request.Object,
		MarkerBindingDigest: request.MarkerBindingDigest,
		RootRevision:        permit.RootRevision,
		TargetRevision:      "target-revision-cleanup-validated",
	}, nil
}

func (closedTargetPortFake) RemoveOwnedJobDir(
	context.Context,
	TargetCleanupPermit,
	RemoveOwnedJobDirRequest,
) (OwnedJobDirRemoval, error) {
	return OwnedJobDirRemoval{}, nil
}

func (closedTargetPortFake) ValidateOwnedJobDirRemoved(
	_ context.Context,
	permit TargetCleanupPermit,
	request RemoveOwnedJobDirRequest,
) (OwnedJobDirRemovalValidation, error) {
	return OwnedJobDirRemovalValidation{
		Object: request.Object, RootRevision: permit.RootRevision,
		TargetRevision: "target-revision-cleanup-removed-validated",
	}, nil
}

func (closedTargetPortFake) OpenOwnedResult(context.Context, TargetResultReadPermit, OpenOwnedResultRequest) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}

var _ TargetPort = closedTargetPortFake{}

func TestRecoverySFTPTargetPreflightRequiresExactObservedDraftPlan(t *testing.T) {
	fixture := newPreflightPersistenceFixture(t)
	var plan model.BackupAssetRecoveryPlan
	if err := fixture.db.Where("id = ?", fixture.planID).Take(&plan).Error; err != nil {
		t.Fatalf("load observed draft plan: %v", err)
	}
	binding, err := newRecoveryTargetPreflightSessionBinding(plan)
	if err != nil {
		t.Fatalf("bind observed draft plan: %v", err)
	}
	permit := issueTargetPreflightPermit(fixture.request.Input.Permit, binding, fixture.request.Input.ProbeRequest)
	if err := permit.ValidateRequestAt(fixture.now, fixture.request.Input.Permit, fixture.request.Input.ProbeRequest); err != nil {
		t.Fatalf("exact observed draft authority rejected: %v", err)
	}

	target := newRecoverySFTPTargetForTest(nil, nil)
	target.now = func() time.Time { return fixture.now }
	unsealed := TargetPreflightPermit{permit: fixture.request.Input.Permit}
	if _, err := target.ProbeRoot(
		context.Background(), unsealed, fixture.request.Input.ProbeRequest,
	); !errors.Is(err, ErrInvalidTargetPermit) {
		t.Fatalf("unsealed preflight error = %v, want ErrInvalidTargetPermit", err)
	}
	if _, err := target.ProbeRoot(
		context.Background(), permit, fixture.request.Input.ProbeRequest,
	); !errors.Is(err, ErrRecoveryTargetUnavailable) {
		t.Fatalf("sealed R26 preflight error = %v, want deferred ErrRecoveryTargetUnavailable", err)
	}

	tests := []struct {
		name   string
		mutate func(*TargetPreflightPermit, *TargetObservationPermit, *TargetProbeRequest)
	}{
		{name: "plan id", mutate: func(value *TargetPreflightPermit, _ *TargetObservationPermit, _ *TargetProbeRequest) {
			value.proof.sessionBinding.planID = strings.Repeat("9", 32)
		}},
		{name: "plan binding", mutate: func(value *TargetPreflightPermit, _ *TargetObservationPermit, _ *TargetProbeRequest) {
			value.proof.sessionBinding.planBindingDigest = strings.Repeat("9", sha256DigestLength)
		}},
		{name: "transition revision", mutate: func(value *TargetPreflightPermit, _ *TargetObservationPermit, _ *TargetProbeRequest) {
			value.proof.sessionBinding.planTransitionRevision++
		}},
		{name: "target mode", mutate: func(value *TargetPreflightPermit, _ *TargetObservationPermit, _ *TargetProbeRequest) {
			value.proof.sessionBinding.targetMode = TargetModeInPlace
		}},
		{name: "node revision", mutate: func(value *TargetPreflightPermit, _ *TargetObservationPermit, _ *TargetProbeRequest) {
			value.proof.sessionBinding.nodeRevision = "node-revision-substituted"
		}},
		{name: "credential revision", mutate: func(value *TargetPreflightPermit, _ *TargetObservationPermit, _ *TargetProbeRequest) {
			value.proof.sessionBinding.credentialRevision = "credential-revision-substituted"
		}},
		{name: "root locator", mutate: func(value *TargetPreflightPermit, _ *TargetObservationPermit, _ *TargetProbeRequest) {
			value.proof.sessionBinding.rootLocator += "-substituted"
		}},
		{name: "filesystem revision", mutate: func(value *TargetPreflightPermit, _ *TargetObservationPermit, _ *TargetProbeRequest) {
			value.proof.sessionBinding.filesystemRevision = "filesystem-revision-substituted"
		}},
		{name: "target revision", mutate: func(value *TargetPreflightPermit, _ *TargetObservationPermit, _ *TargetProbeRequest) {
			value.proof.sessionBinding.targetRevision = "target-revision-substituted"
		}},
		{name: "preflight revision", mutate: func(value *TargetPreflightPermit, _ *TargetObservationPermit, _ *TargetProbeRequest) {
			value.proof.sessionBinding.preflightRevision = "preflight-revision-substituted"
		}},
		{name: "public root revision", mutate: func(_ *TargetPreflightPermit, value *TargetObservationPermit, _ *TargetProbeRequest) {
			value.RootRevision = "root-revision-substituted"
		}},
		{name: "public expiry", mutate: func(_ *TargetPreflightPermit, value *TargetObservationPermit, _ *TargetProbeRequest) {
			value.ExpiresAt = value.ExpiresAt.Add(time.Second)
		}},
		{name: "request private locator", mutate: func(_ *TargetPreflightPermit, _ *TargetObservationPermit, value *TargetProbeRequest) {
			value.Object.PrivateRelativeLocator += "-substituted"
		}},
		{name: "request source", mutate: func(_ *TargetPreflightPermit, _ *TargetObservationPermit, value *TargetProbeRequest) {
			value.SourceRevisionDigest = strings.Repeat("9", sha256DigestLength)
		}},
		{name: "request capability", mutate: func(_ *TargetPreflightPermit, _ *TargetObservationPermit, value *TargetProbeRequest) {
			value.CapabilityRevision = "capability-revision-substituted"
		}},
		{name: "request policy", mutate: func(_ *TargetPreflightPermit, _ *TargetObservationPermit, value *TargetProbeRequest) {
			value.PolicyRevision = "policy-revision-substituted"
		}},
		{name: "request bytes", mutate: func(_ *TargetPreflightPermit, _ *TargetObservationPermit, value *TargetProbeRequest) {
			value.RequiredBytes++
		}},
		{name: "request inodes", mutate: func(_ *TargetPreflightPermit, _ *TargetObservationPermit, value *TargetProbeRequest) {
			value.RequiredInodes++
		}},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			mutated := permit
			proof := *permit.proof
			mutated.proof = &proof
			public := fixture.request.Input.Permit
			request := fixture.request.Input.ProbeRequest
			testCase.mutate(&mutated, &public, &request)
			if err := mutated.ValidateRequestAt(fixture.now, public, request); !errors.Is(err, ErrInvalidTargetPermit) {
				t.Fatalf("substituted authority error = %v, want ErrInvalidTargetPermit", err)
			}
		})
	}
}

func TestRecoveryTargetSessionBindingRequiresExactHookDecryptedPlanSnapshot(t *testing.T) {
	fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptWriteAuthorize)
	if err := fixture.db.Model(&model.BackupAssetRecoveryPlan{}).
		Where("state = ?", PlanStatePreflightReady).
		Update("state", PlanStateExecuted).Error; err != nil {
		t.Fatalf("mark fixture plan executed: %v", err)
	}

	var plan model.BackupAssetRecoveryPlan
	if err := fixture.db.Transaction(func(tx *gorm.DB) error {
		return tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("state = ?", PlanStateExecuted).Take(&plan).Error
	}); err != nil {
		t.Fatalf("load locked executed plan: %v", err)
	}
	if strings.HasPrefix(plan.EncryptedTargetRootLocator, "enc:v2:") {
		t.Fatal("normal GORM plan load left target root ciphertext in memory")
	}

	binding, err := newRecoveryTargetSessionBinding(plan)
	if err != nil {
		t.Fatalf("construct exact target session binding: %v", err)
	}
	wantLocatorDigest, err := settings.RecoveryTargetRootLocatorDigest(
		plan.TargetNodeID, plan.TargetRootID, plan.EncryptedTargetRootLocator,
	)
	if err != nil {
		t.Fatalf("recompute target root locator digest: %v", err)
	}
	wantBindingDigest := framedDigest(
		recoveryTargetSessionBindingDomain,
		plan.ID, plan.BindingDigest, strconv.FormatUint(uint64(plan.TargetNodeID), 10),
		plan.TargetBaseRevision, plan.CredentialScopeRevision, plan.TargetRootID,
		plan.EncryptedTargetRootLocator, plan.RootLocatorDigest, plan.RootRevision,
	)
	if binding.PlanID != plan.ID || binding.PlanBindingDigest != plan.BindingDigest ||
		binding.NodeID != plan.TargetNodeID || binding.NodeRevision != plan.TargetBaseRevision ||
		binding.CredentialRevision != plan.CredentialScopeRevision || binding.RootID != plan.TargetRootID ||
		binding.RootLocator != plan.EncryptedTargetRootLocator || binding.RootLocatorDigest != wantLocatorDigest ||
		binding.RootRevision != plan.RootRevision || binding.bindingDigest != wantBindingDigest ||
		!validDigest(binding.bindingDigest) {
		t.Fatalf("target session binding = %#v, want exact locked plan snapshot", binding)
	}

	invalidPlans := []model.BackupAssetRecoveryPlan{plan, plan, plan}
	invalidPlans[0].EncryptedTargetRootLocator = "enc:v2:FAKE_CIPHERTEXT_FOR_TEST_ONLY"
	invalidPlans[1].EncryptedTargetRootLocator += "/../FAKE_NONCANONICAL_FOR_TEST_ONLY"
	invalidPlans[2].RootLocatorDigest = strings.Repeat("f", sha256DigestLength)
	for index, invalid := range invalidPlans {
		if _, err := newRecoveryTargetSessionBinding(invalid); !errors.Is(err, ErrInvalidTargetPermit) {
			t.Fatalf("invalid plan %d error = %v, want ErrInvalidTargetPermit", index, err)
		}
	}
}

func TestRecoveryTargetSessionBindingProofsRejectSubstitutionAndStayPrivate(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	binding := recoveryTargetSessionBindingForTest(t)
	writePermit, createRequest, cleanupPermit, cleanupRequest := recoveryWorkspaceMarkerAuthorityForTest(
		t, now, recoveryWorkspaceMarkerMaterialForTest(1, strings.Repeat("k", 32)),
		"session-binding-creator", 17, "jobs/"+strings.Repeat("1", 32),
	)
	mutation := writePermit.permit
	mutation.proof = nil
	mutation = issueTargetMutationPermit(mutation, func(time.Time) error { return nil }, binding)
	writePermit, err := NewTargetWritePermit(mutation, now)
	if err != nil {
		t.Fatalf("seal session-bound write permit: %v", err)
	}
	cleanupPermit.proof = nil
	cleanupPermit = issueTargetCleanupPermit(cleanupPermit, binding)
	if writePermit.ValidateOwnedJobDirRequestAt(now, createRequest) != nil ||
		cleanupPermit.ValidateOwnedJobDirRequestAt(now, cleanupRequest) != nil {
		t.Fatal("exact session-bound authority was rejected")
	}

	mutatedWrite := mutation
	mutatedWriteProof := *mutation.proof
	mutatedWrite.proof = &mutatedWriteProof
	mutatedWrite.proof.sessionBinding.RootLocator += "-substituted"
	if !errors.Is(mutatedWrite.ValidateAt(now), ErrInvalidTargetPermit) {
		t.Fatal("write proof accepted substituted private root locator")
	}
	mutatedCleanup := cleanupPermit
	mutatedCleanupProof := *cleanupPermit.proof
	mutatedCleanup.proof = &mutatedCleanupProof
	mutatedCleanup.proof.sessionBinding.CredentialRevision = "credential-revision-substituted"
	if !errors.Is(mutatedCleanup.ValidateAt(now), ErrInvalidTargetPermit) {
		t.Fatal("cleanup proof accepted substituted credential revision")
	}
	mutatedCleanup = cleanupPermit
	mutatedCleanupProof = *cleanupPermit.proof
	mutatedCleanup.proof = &mutatedCleanupProof
	mutatedCleanup.proof.sessionBinding.bindingDigest = strings.Repeat("f", sha256DigestLength)
	if !errors.Is(mutatedCleanup.ValidateAt(now), ErrInvalidTargetPermit) {
		t.Fatal("cleanup proof accepted substituted private binding digest")
	}

	for name, value := range map[string]any{
		"mutation": mutation, "write": writePermit, "cleanup": cleanupPermit,
		"create": createRequest, "validate": cleanupRequest,
	} {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("marshal %s authority: %v", name, err)
		}
		for _, forbidden := range []string{
			binding.PlanID, binding.PlanBindingDigest, binding.NodeRevision,
			binding.CredentialRevision, binding.RootLocator, binding.bindingDigest,
		} {
			if strings.Contains(string(encoded), forbidden) {
				t.Fatalf("%s JSON leaked private session value %q: %s", name, forbidden, encoded)
			}
		}
	}
}

type recoveryTargetNodeSessionResolverFake struct {
	result  recoveryTargetNodeSession
	err     error
	calls   int
	nodeID  uint
	purpose TargetPurpose
	resolve func(context.Context, uint, TargetPurpose) (recoveryTargetNodeSession, error)
}

func (fake *recoveryTargetNodeSessionResolverFake) ResolveRecoveryTargetNodeSession(
	ctx context.Context,
	nodeID uint,
	purpose TargetPurpose,
) (recoveryTargetNodeSession, error) {
	fake.calls++
	fake.nodeID = nodeID
	fake.purpose = purpose
	if fake.resolve != nil {
		return fake.resolve(ctx, nodeID, purpose)
	}
	return fake.result, fake.err
}

type recoveryTargetNodeDialerFake struct {
	err     error
	calls   int
	node    model.Node
	purpose string
	audit   sshutil.DialAuditContext
	dial    func(context.Context, model.Node, string, sshutil.DialAuditContext) (*ssh.Client, error)
}

func (fake *recoveryTargetNodeDialerFake) Dial(
	ctx context.Context,
	node model.Node,
	purpose string,
	audit sshutil.DialAuditContext,
) (*ssh.Client, error) {
	fake.calls++
	fake.node = node
	fake.purpose = purpose
	fake.audit = audit
	if fake.dial != nil {
		return fake.dial(ctx, node, purpose, audit)
	}
	return nil, fake.err
}

type recoveryTargetSFTPClientFake struct {
	closeOrder           *[]string
	closeCalls           int
	realPathCalls        map[string]int
	lstatCalls           map[string]int
	readLinkCalls        map[string]int
	statVFSCalls         map[string]int
	mkdirCalls           int
	chmodCalls           int
	openCalls            int
	openFileCalls        int
	renameCalls          int
	removeCalls          int
	removeDirectoryCalls int
	realPath             func(string, int) (string, error)
	lstat                func(string, int) (os.FileInfo, error)
	readLink             func(string, int) (string, error)
	statVFS              func(string, int) (*sftp.StatVFS, error)
	openFile             func(string, int) (recoveryTargetSFTPFile, error)
	close                func() error
}

type recoveryProbeCommandSession struct {
	stdout   []byte
	commands *[]string
}

type recoveryProbeCommandStdin struct{ bytes.Buffer }

func (*recoveryProbeCommandStdin) Close() error { return nil }

func (*recoveryProbeCommandSession) StdinPipe() (io.WriteCloser, error) {
	return &recoveryProbeCommandStdin{}, nil
}

func (session *recoveryProbeCommandSession) StdoutPipe() (io.Reader, error) {
	return bytes.NewReader(session.stdout), nil
}

func (*recoveryProbeCommandSession) StderrPipe() (io.Reader, error) {
	return strings.NewReader(""), nil
}

func (session *recoveryProbeCommandSession) Start(command string) error {
	*session.commands = append(*session.commands, command)
	return nil
}

func (*recoveryProbeCommandSession) Wait() error             { return nil }
func (*recoveryProbeCommandSession) Signal(ssh.Signal) error { return nil }
func (*recoveryProbeCommandSession) Close() error            { return nil }

type recoveryProbeContextForR30 struct {
	context.Context
	done chan struct{}
	err  error
	once sync.Once
}

func newRecoveryProbeContextForR30(err error) *recoveryProbeContextForR30 {
	return &recoveryProbeContextForR30{
		Context: context.Background(), done: make(chan struct{}), err: err,
	}
}

func (ctx *recoveryProbeContextForR30) Done() <-chan struct{} { return ctx.done }

func (ctx *recoveryProbeContextForR30) Err() error {
	select {
	case <-ctx.done:
		return ctx.err
	default:
		return nil
	}
}

func (ctx *recoveryProbeContextForR30) trigger() {
	ctx.once.Do(func() { close(ctx.done) })
}

type recoveryProbeCommandSessionForR30 struct {
	ctx              context.Context
	stdout           []byte
	waitErr          error
	blockUntilCancel bool
	waitStarted      chan struct{}
	waitDone         chan struct{}
	waitStartOnce    sync.Once
	waitDoneOnce     sync.Once
	mu               sync.Mutex
	closeCalls       int
}

func (session *recoveryProbeCommandSessionForR30) StdinPipe() (io.WriteCloser, error) {
	return &recoveryProbeCommandStdin{}, nil
}

func (session *recoveryProbeCommandSessionForR30) StdoutPipe() (io.Reader, error) {
	return bytes.NewReader(session.stdout), nil
}

func (*recoveryProbeCommandSessionForR30) StderrPipe() (io.Reader, error) {
	return strings.NewReader(""), nil
}

func (*recoveryProbeCommandSessionForR30) Start(string) error { return nil }

func (session *recoveryProbeCommandSessionForR30) Wait() error {
	if session.blockUntilCancel {
		session.waitStartOnce.Do(func() { close(session.waitStarted) })
		<-session.ctx.Done()
	}
	session.waitDoneOnce.Do(func() { close(session.waitDone) })
	if session.waitErr != nil {
		return session.waitErr
	}
	return session.ctx.Err()
}

func (*recoveryProbeCommandSessionForR30) Signal(ssh.Signal) error { return nil }

func (session *recoveryProbeCommandSessionForR30) Close() error {
	session.mu.Lock()
	session.closeCalls++
	session.mu.Unlock()
	return nil
}

func (session *recoveryProbeCommandSessionForR30) closeCount() int {
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.closeCalls
}

type recoveryPreflightExternalEvidenceFuncForR30 func(
	context.Context,
	PreflightExternalEvidenceRequest,
) (PreflightExternalEvidence, error)

func (observe recoveryPreflightExternalEvidenceFuncForR30) ObservePreflightEvidence(
	ctx context.Context,
	request PreflightExternalEvidenceRequest,
) (PreflightExternalEvidence, error) {
	return observe(ctx, request)
}

func recoveryPrincipalCommandRunnerForTest(
	outputs [][]byte,
	commands *[]string,
	deadlines *[]time.Time,
) *sshutil.CommandRunner {
	next := 0
	return sshutil.NewCommandRunner(func(ctx context.Context) (sshutil.CommandSession, error) {
		if next >= len(outputs) {
			return nil, errors.New("unexpected recovery principal command")
		}
		deadline, _ := ctx.Deadline()
		*deadlines = append(*deadlines, deadline)
		session := &recoveryProbeCommandSession{stdout: outputs[next], commands: commands}
		next++
		return session, nil
	}, 1)
}

type recoveryProbeFileInfo struct {
	name    string
	size    int64
	mode    os.FileMode
	modTime time.Time
	uid     uint32
	gid     uint32
}

func (info recoveryProbeFileInfo) Name() string       { return info.name }
func (info recoveryProbeFileInfo) Size() int64        { return info.size }
func (info recoveryProbeFileInfo) Mode() os.FileMode  { return info.mode }
func (info recoveryProbeFileInfo) ModTime() time.Time { return info.modTime }
func (info recoveryProbeFileInfo) IsDir() bool        { return info.mode.IsDir() }
func (info recoveryProbeFileInfo) Sys() any {
	return &sftp.FileStat{Size: uint64(info.size), UID: info.uid, GID: info.gid}
}

type recoveryPipeSFTPClient struct {
	*recoverySFTPClient
	server *sftp.Server
	done   <-chan struct{}
}

type recoveryLocalSFTPClient struct {
	realPathCalls        int
	lstatCalls           int
	readLinkCalls        int
	statCalls            int
	mkdirCalls           int
	chmodCalls           int
	openCalls            int
	openFileCalls        int
	renameCalls          int
	removeCalls          int
	removeDirectoryCalls int
	closeCalls           int
	syncCalls            int
	readBytes            int
	maxReadRequest       int
	readDirRequests      []int
	statVFSCalls         map[string]int
	statVFS              func(string, int) (*sftp.StatVFS, error)
	mkdirPaths           []string
	chmodPaths           []string
	chmodModes           []os.FileMode
	openPaths            []string
	openFilePaths        []string
	openFileFlags        []int
	renamePaths          [][2]string
	removePaths          []string
	removeDirectoryPaths []string
	removeOrder          []string
}

func (client *recoveryLocalSFTPClient) RealPath(value string) (string, error) {
	client.realPathCalls++
	return filepath.EvalSymlinks(value)
}
func (client *recoveryLocalSFTPClient) Lstat(value string) (os.FileInfo, error) {
	client.lstatCalls++
	return os.Lstat(value)
}
func (client *recoveryLocalSFTPClient) ReadLink(value string) (string, error) {
	client.readLinkCalls++
	return os.Readlink(value)
}
func (client *recoveryLocalSFTPClient) Stat(value string) (os.FileInfo, error) {
	client.statCalls++
	return os.Stat(value)
}
func (client *recoveryLocalSFTPClient) StatVFS(value string) (*sftp.StatVFS, error) {
	if client.statVFS != nil {
		if client.statVFSCalls == nil {
			client.statVFSCalls = make(map[string]int)
		}
		client.statVFSCalls[value]++
		return client.statVFS(value, client.statVFSCalls[value])
	}
	return &sftp.StatVFS{Fsid: 7, Files: 100, Favail: 20, Namemax: 255}, nil
}
func (client *recoveryLocalSFTPClient) Mkdir(value string) error {
	client.mkdirCalls++
	client.mkdirPaths = append(client.mkdirPaths, value)
	return os.Mkdir(value, 0o777)
}
func (client *recoveryLocalSFTPClient) Chmod(value string, mode os.FileMode) error {
	client.chmodCalls++
	client.chmodPaths = append(client.chmodPaths, value)
	client.chmodModes = append(client.chmodModes, mode)
	return os.Chmod(value, mode)
}
func (client *recoveryLocalSFTPClient) Open(value string) (recoveryTargetSFTPFile, error) {
	client.openCalls++
	client.openPaths = append(client.openPaths, value)
	file, err := os.Open(value)
	if err != nil {
		return nil, err
	}
	return &recoveryLocalSFTPFile{File: file, owner: client}, nil
}
func (client *recoveryLocalSFTPClient) OpenFile(value string, flag int) (recoveryTargetSFTPFile, error) {
	client.openFileCalls++
	client.openFilePaths = append(client.openFilePaths, value)
	client.openFileFlags = append(client.openFileFlags, flag)
	file, err := os.OpenFile(value, flag, 0o666)
	if err != nil {
		return nil, err
	}
	return &recoveryLocalSFTPFile{File: file, owner: client}, nil
}

func (client *recoveryLocalSFTPFile) ReadDir(n int) ([]os.FileInfo, error) {
	if client.owner != nil {
		client.owner.readDirRequests = append(client.owner.readDirRequests, n)
	}
	entries, err := client.File.ReadDir(n)
	if err != nil && len(entries) == 0 {
		return nil, err
	}
	infos := make([]os.FileInfo, 0, len(entries))
	for _, entry := range entries {
		info, infoErr := entry.Info()
		if infoErr != nil {
			return nil, infoErr
		}
		infos = append(infos, info)
	}
	return infos, err
}
func (client *recoveryLocalSFTPClient) Rename(oldName, newName string) error {
	client.renameCalls++
	client.renamePaths = append(client.renamePaths, [2]string{oldName, newName})
	if _, err := os.Lstat(newName); err == nil {
		return os.ErrExist
	} else if !os.IsNotExist(err) {
		return err
	}
	return os.Rename(oldName, newName)
}
func (client *recoveryLocalSFTPClient) Remove(value string) error {
	client.removeCalls++
	client.removePaths = append(client.removePaths, value)
	client.removeOrder = append(client.removeOrder, "leaf:"+value)
	return os.Remove(value)
}
func (client *recoveryLocalSFTPClient) RemoveDirectory(value string) error {
	client.removeDirectoryCalls++
	client.removeDirectoryPaths = append(client.removeDirectoryPaths, value)
	client.removeOrder = append(client.removeOrder, "directory:"+value)
	return os.Remove(value)
}
func (client *recoveryLocalSFTPClient) Close() error {
	client.closeCalls++
	return nil
}

type recoveryLocalSFTPFile struct {
	*os.File
	owner *recoveryLocalSFTPClient
}

type recoveryScriptedSFTPClient struct {
	base            *recoveryLocalSFTPClient
	lstatCalls      map[string]int
	readLinkCalls   map[string]int
	realPathCalls   map[string]int
	statVFSCalls    map[string]int
	lstat           func(string, int) (os.FileInfo, error)
	readLink        func(string, int) (string, error)
	realPath        func(string, int) (string, error)
	statVFS         func(string, int) (*sftp.StatVFS, error)
	open            func(string) (recoveryTargetSFTPFile, error)
	mkdir           func(string) error
	chmod           func(string, os.FileMode) error
	openFile        func(string, int) (recoveryTargetSFTPFile, error)
	rename          func(string, string) error
	remove          func(string) error
	removeDirectory func(string) error
	close           func() error
}

func (client *recoveryScriptedSFTPClient) RealPath(value string) (string, error) {
	if client.realPathCalls == nil {
		client.realPathCalls = make(map[string]int)
	}
	client.realPathCalls[value]++
	if client.realPath != nil {
		return client.realPath(value, client.realPathCalls[value])
	}
	return client.base.RealPath(value)
}

func (client *recoveryScriptedSFTPClient) Lstat(value string) (os.FileInfo, error) {
	if client.lstatCalls == nil {
		client.lstatCalls = make(map[string]int)
	}
	client.lstatCalls[value]++
	if client.lstat != nil {
		return client.lstat(value, client.lstatCalls[value])
	}
	return client.base.Lstat(value)
}

func (client *recoveryScriptedSFTPClient) ReadLink(value string) (string, error) {
	if client.readLinkCalls == nil {
		client.readLinkCalls = make(map[string]int)
	}
	client.readLinkCalls[value]++
	if client.readLink != nil {
		return client.readLink(value, client.readLinkCalls[value])
	}
	return client.base.ReadLink(value)
}

func (client *recoveryScriptedSFTPClient) Stat(value string) (os.FileInfo, error) {
	return client.base.Stat(value)
}

func (client *recoveryScriptedSFTPClient) StatVFS(value string) (*sftp.StatVFS, error) {
	if client.statVFSCalls == nil {
		client.statVFSCalls = make(map[string]int)
	}
	client.statVFSCalls[value]++
	if client.statVFS != nil {
		return client.statVFS(value, client.statVFSCalls[value])
	}
	return client.base.StatVFS(value)
}

func (client *recoveryScriptedSFTPClient) Mkdir(value string) error {
	if client.mkdir != nil {
		return client.mkdir(value)
	}
	return client.base.Mkdir(value)
}

func (client *recoveryScriptedSFTPClient) Chmod(value string, mode os.FileMode) error {
	if client.chmod != nil {
		return client.chmod(value, mode)
	}
	return client.base.Chmod(value, mode)
}

func (client *recoveryScriptedSFTPClient) Open(value string) (recoveryTargetSFTPFile, error) {
	if client.open != nil {
		return client.open(value)
	}
	return client.base.Open(value)
}

func (client *recoveryScriptedSFTPClient) OpenFile(value string, flag int) (recoveryTargetSFTPFile, error) {
	if client.openFile != nil {
		return client.openFile(value, flag)
	}
	return client.base.OpenFile(value, flag)
}

func (client *recoveryScriptedSFTPClient) Rename(oldName, newName string) error {
	if client.rename != nil {
		return client.rename(oldName, newName)
	}
	return client.base.Rename(oldName, newName)
}

func (client *recoveryScriptedSFTPClient) Remove(value string) error {
	if client.remove != nil {
		return client.remove(value)
	}
	return client.base.Remove(value)
}

func (client *recoveryScriptedSFTPClient) RemoveDirectory(value string) error {
	if client.removeDirectory != nil {
		return client.removeDirectory(value)
	}
	return client.base.RemoveDirectory(value)
}

func (client *recoveryScriptedSFTPClient) Close() error {
	if client.close != nil {
		return client.close()
	}
	return client.base.Close()
}

type recoveryScriptedSFTPFile struct {
	base    recoveryTargetSFTPFile
	read    func([]byte) (int, error)
	write   func([]byte) (int, error)
	readDir func(int) ([]os.FileInfo, error)
	stat    func() (os.FileInfo, error)
	sync    func() error
	close   func() error
}

type recoveryReadTrackingReader struct {
	reader         io.Reader
	read           func([]byte) (int, error)
	requests       []int
	maxReadRequest int
}

func (reader *recoveryReadTrackingReader) Read(value []byte) (int, error) {
	reader.requests = append(reader.requests, len(value))
	if len(value) > reader.maxReadRequest {
		reader.maxReadRequest = len(value)
	}
	if reader.read != nil {
		return reader.read(value)
	}
	return reader.reader.Read(value)
}

type recoveryCloseCountingSFTPFile struct {
	recoveryTargetSFTPFile
	closeCalls int
}

func TestRecoveryHeartbeatCancellationClosesActiveTargetWrite(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writeStarted := make(chan struct{})
	closed := make(chan struct{})
	var closeOnce sync.Once
	file := &recoveryScriptedSFTPFile{
		write: func([]byte) (int, error) {
			close(writeStarted)
			<-closed
			return 0, os.ErrClosed
		},
		close: func() error {
			closeOnce.Do(func() { close(closed) })
			return nil
		},
	}
	raw := &recoveryTargetSFTPClientFake{
		openFile: func(string, int) (recoveryTargetSFTPFile, error) {
			return file, nil
		},
	}
	session := &recoveryTargetSession{
		client: raw, watchStop: make(chan struct{}), watchDone: make(chan struct{}),
	}
	session.watch(ctx)
	client := &recoveryResultTrackedSFTPClient{recoveryTargetSFTPClient: raw, session: session}
	opened, err := client.OpenFile("/srv/recovery-active-write", os.O_WRONLY|os.O_CREATE)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("active recovery write")
	digest := sha256.Sum256(payload)
	done := make(chan error, 1)
	go func() {
		done <- writeRecoveryRegularContent(opened, bytes.NewReader(payload), int64(len(payload)), hex.EncodeToString(digest[:]))
	}()
	select {
	case <-writeStarted:
	case <-time.After(time.Second):
		t.Fatal("target write did not block")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, ErrRecoveryTargetUnavailable) {
			t.Fatalf("canceled active write error=%v, want target unavailable", err)
		}
	case <-time.After(100 * time.Millisecond):
		_ = file.Close()
		<-done
		t.Fatal("context cancellation did not close and unblock the active SFTP write")
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRecoveryHeartbeatCancellationClosesTransportBeforeBlockedFileCleanup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	transportClosed := make(chan struct{})
	var transportOnce sync.Once
	var fileCloseCalls atomic.Int32
	closeOrder := make([]string, 0, 3)
	file := &recoveryScriptedSFTPFile{
		close: func() error {
			fileCloseCalls.Add(1)
			<-transportClosed
			closeOrder = append(closeOrder, "file")
			return os.ErrClosed
		},
	}
	raw := &recoveryTargetSFTPClientFake{
		openFile: func(string, int) (recoveryTargetSFTPFile, error) { return file, nil },
		close: func() error {
			closeOrder = append(closeOrder, "sftp")
			transportOnce.Do(func() { close(transportClosed) })
			return nil
		},
	}
	session := &recoveryTargetSession{
		client: raw,
		closeSSH: func(*ssh.Client) error {
			closeOrder = append(closeOrder, "ssh")
			transportOnce.Do(func() { close(transportClosed) })
			return nil
		},
		watchStop: make(chan struct{}), watchDone: make(chan struct{}),
	}
	session.watch(ctx)
	client := &recoveryResultTrackedSFTPClient{recoveryTargetSFTPClient: raw, session: session}
	if _, err := client.OpenFile("/srv/recovery-blocked-close", os.O_WRONLY|os.O_CREATE); err != nil {
		t.Fatal(err)
	}

	cancel()
	select {
	case <-session.watchDone:
	case <-time.After(100 * time.Millisecond):
		transportOnce.Do(func() { close(transportClosed) })
		<-session.watchDone
		t.Fatal("cancellation did not promptly close the target transport")
	}

	if err := session.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
		t.Fatalf("session close error=%v, want safe best-effort file cleanup error", err)
	}
	if got := fileCloseCalls.Load(); got != 1 {
		t.Fatalf("file Close calls=%d, want exactly one", got)
	}
	if raw.closeCalls != 1 || !reflect.DeepEqual(closeOrder, []string{"sftp", "ssh", "file"}) {
		t.Fatalf("close calls/order=%d/%v, want transport first then exactly-once file cleanup", raw.closeCalls, closeOrder)
	}
}

func (file *recoveryCloseCountingSFTPFile) Close() error {
	file.closeCalls++
	return file.recoveryTargetSFTPFile.Close()
}

type recoveryFileInfoOverride struct {
	os.FileInfo
	size    *int64
	mode    *os.FileMode
	modTime *time.Time
}

func (info recoveryFileInfoOverride) Size() int64 {
	if info.size != nil {
		return *info.size
	}
	return info.FileInfo.Size()
}

func (info recoveryFileInfoOverride) Mode() os.FileMode {
	if info.mode != nil {
		return *info.mode
	}
	return info.FileInfo.Mode()
}

func (info recoveryFileInfoOverride) ModTime() time.Time {
	if info.modTime != nil {
		return *info.modTime
	}
	return info.FileInfo.ModTime()
}

func (file *recoveryScriptedSFTPFile) Read(value []byte) (int, error) {
	if file.read != nil {
		return file.read(value)
	}
	return file.base.Read(value)
}

func (file *recoveryScriptedSFTPFile) Write(value []byte) (int, error) {
	if file.write != nil {
		return file.write(value)
	}
	return file.base.Write(value)
}

func (file *recoveryScriptedSFTPFile) ReadDir(n int) ([]os.FileInfo, error) {
	if file.readDir != nil {
		return file.readDir(n)
	}
	return file.base.ReadDir(n)
}

func (file *recoveryScriptedSFTPFile) Stat() (os.FileInfo, error) {
	if file.stat != nil {
		return file.stat()
	}
	return file.base.Stat()
}

func (file *recoveryScriptedSFTPFile) Sync() error {
	if file.sync != nil {
		return file.sync()
	}
	return file.base.Sync()
}

func (file *recoveryScriptedSFTPFile) Close() error {
	if file.close != nil {
		return file.close()
	}
	return file.base.Close()
}

func (file *recoveryLocalSFTPFile) Read(value []byte) (int, error) {
	if file.owner != nil && len(value) > file.owner.maxReadRequest {
		file.owner.maxReadRequest = len(value)
	}
	read, err := file.File.Read(value)
	if file.owner != nil {
		file.owner.readBytes += read
	}
	return read, err
}

func (file *recoveryLocalSFTPFile) Sync() error {
	if file.owner != nil {
		file.owner.syncCalls++
	}
	return file.File.Sync()
}

type recoveryLocalSFTPTargetFixture struct {
	root           string
	binding        recoveryTargetSessionBinding
	now            time.Time
	material       backupasset.DomainKeyMaterial
	codec          *recoveryWorkspaceMarkerCodec
	writePermit    TargetWritePermit
	createRequest  CreateOwnedJobDirRequest
	cleanupPermit  TargetCleanupPermit
	cleanupRequest ValidateOwnedJobDirRequest
	resolver       *recoveryTargetNodeSessionResolverFake
	dialer         *recoveryTargetNodeDialerFake
	target         *recoverySFTPTarget
	clients        []*recoveryLocalSFTPClient
}

func newRecoveryLocalSFTPTargetFixture(t *testing.T) *recoveryLocalSFTPTargetFixture {
	t.Helper()
	fixture := &recoveryLocalSFTPTargetFixture{
		root:     t.TempDir(),
		now:      time.Now().UTC().Truncate(time.Second),
		material: recoveryWorkspaceMarkerMaterialForTest(1, strings.Repeat("k", 32)),
	}
	if err := os.Chmod(fixture.root, 0o700); err != nil {
		t.Fatalf("chmod recovery root: %v", err)
	}
	fixture.binding = recoveryTargetSessionBindingForLocatorTest(t, fixture.root)
	fixture.codec = newRecoveryWorkspaceMarkerCodec(
		&recoveryWorkspaceMarkerKeySourceForTest{
			active:   fixture.material,
			versions: map[int]backupasset.DomainKeyMaterial{1: fixture.material},
		},
		bytes.NewReader(bytes.Repeat([]byte{0x5a}, 32)),
	)
	fixture.writePermit, fixture.createRequest, fixture.cleanupPermit, fixture.cleanupRequest =
		recoveryWorkspaceMarkerAuthorityWithSessionForTest(
			t, fixture.now, fixture.material, fixture.binding,
		)
	fixture.resolver = &recoveryTargetNodeSessionResolverFake{result: recoveryTargetNodeSession{
		Node: model.Node{ID: fixture.binding.NodeID}, NodeRevision: fixture.binding.NodeRevision,
		CredentialRevision: fixture.binding.CredentialRevision,
	}}
	fixture.dialer = &recoveryTargetNodeDialerFake{}
	fixture.target = newRecoverySFTPTargetForTest(
		newRecoveryTargetSessionFactoryForTest(
			fixture.resolver, fixture.dialer,
			func(*ssh.Client) (recoveryTargetSFTPClient, error) {
				client := &recoveryLocalSFTPClient{}
				fixture.clients = append(fixture.clients, client)
				return client, nil
			},
			func(*ssh.Client) error { return nil },
		),
		fixture.codec,
	)
	return fixture
}

func (fixture *recoveryLocalSFTPTargetFixture) create(t *testing.T) OwnedJobDir {
	t.Helper()
	created, err := fixture.target.CreateOwnedJobDir(
		context.Background(), fixture.writePermit, fixture.createRequest,
	)
	if err != nil {
		t.Fatalf("create local owned workspace: %v", err)
	}
	return created
}

func (fixture *recoveryLocalSFTPTargetFixture) paths() (string, string, string) {
	jobsPath := filepath.Join(fixture.root, recoveryWorkspaceLocatorDirectory)
	jobPath := filepath.Join(jobsPath, fixture.writePermit.permit.JobID)
	return jobsPath, jobPath, filepath.Join(jobPath, recoveryWorkspaceMarkerFileName)
}

func recoveryTargetResultReadPermitForTest(
	t *testing.T,
	fixture *recoveryLocalSFTPTargetFixture,
	object TargetObjectRef,
	payload []byte,
) (TargetResultReadPermit, OpenOwnedResultRequest) {
	t.Helper()
	identity := sha256.Sum256(payload)
	request := OpenOwnedResultRequest{
		Object: object, ExpectedBytes: int64(len(payload)), IdentityDigest: hex.EncodeToString(identity[:]),
	}
	authority := targetResultReadAuthority{
		sessionBinding: fixture.binding, jobID: fixture.writePermit.permit.JobID,
		resultSetID: strings.Repeat("6", 32), resultID: strings.Repeat("7", 32),
		publicationRevision: 1, resultSetState: ResultSetStateReady,
		markerBindingDigest: fixture.createRequest.MarkerBindingDigest,
		markerCreatorID:     fixture.createRequest.MarkerCreatorID,
		markerCreatorFence:  fixture.createRequest.MarkerCreatorFence,
		locatorDigest:       framedDigest("xirang/recovery/result-read-test-locator/v1", object.PrivateRelativeLocator),
		object:              object, expectedBytes: request.ExpectedBytes,
		expectedContentDigest: request.IdentityDigest, plaintextDeadline: fixture.now.Add(time.Minute),
	}
	observation := issueTargetResultReadPermit(TargetObservationPermit{
		SchemaVersion: 1, NodeID: fixture.binding.NodeID, Purpose: TargetPurposeResultRead,
		RootID: object.RootID, RootLocatorDigest: object.RootLocatorDigest,
		TargetPathDigest: object.TargetPathDigest, RootRevision: fixture.binding.RootRevision,
		ExpiresAt: fixture.now.Add(time.Minute),
	}, authority, request)
	permit, err := NewTargetResultReadPermit(observation, fixture.now)
	if err != nil {
		t.Fatalf("construct target result-read permit: %v", err)
	}
	return permit, request
}

type recoveryOwnedResultReadCaseForTest struct {
	fixture    *recoveryLocalSFTPTargetFixture
	payload    []byte
	resultPath string
	markerPath string
	permit     TargetResultReadPermit
	request    OpenOwnedResultRequest
}

func newRecoveryOwnedResultReadCaseForTest(
	t *testing.T,
	payload []byte,
) *recoveryOwnedResultReadCaseForTest {
	t.Helper()
	fixture := newRecoveryLocalSFTPTargetFixture(t)
	fixture.create(t)
	_, jobPath, markerPath := fixture.paths()
	resultPath := filepath.Join(jobPath, "result.bin")
	if err := os.WriteFile(resultPath, payload, 0o600); err != nil {
		t.Fatalf("write owned result-read fixture: %v", err)
	}
	object := TargetObjectRef{
		RootID: fixture.binding.RootID, RootLocatorDigest: fixture.binding.RootLocatorDigest,
		PrivateRelativeLocator: recoveryWorkspaceLocatorDirectory + "/" +
			fixture.writePermit.permit.JobID + "/result.bin",
	}
	object.TargetPathDigest = mustTargetPathDigest(
		t, object.RootID, object.RootLocatorDigest, object.PrivateRelativeLocator,
	)
	permit, request := recoveryTargetResultReadPermitForTest(t, fixture, object, payload)
	return &recoveryOwnedResultReadCaseForTest{
		fixture: fixture, payload: append([]byte(nil), payload...), resultPath: resultPath,
		markerPath: markerPath, permit: permit, request: request,
	}
}

func assertRecoveryOwnedResultReadOnlyForTest(
	t *testing.T,
	client *recoveryLocalSFTPClient,
) {
	t.Helper()
	if client.mkdirCalls != 0 || client.chmodCalls != 0 || client.openFileCalls != 0 ||
		client.renameCalls != 0 || client.removeCalls != 0 {
		t.Fatalf("result read performed mutation: %+v", client)
	}
}

func (fixture *recoveryLocalSFTPTargetFixture) validationTarget(
	client recoveryTargetSFTPClient,
) *recoverySFTPTarget {
	return fixture.targetWithClient(client)
}

func (fixture *recoveryLocalSFTPTargetFixture) targetWithClient(
	client recoveryTargetSFTPClient,
) *recoverySFTPTarget {
	return newRecoverySFTPTargetForTest(
		newRecoveryTargetSessionFactoryForTest(
			fixture.resolver, fixture.dialer,
			func(*ssh.Client) (recoveryTargetSFTPClient, error) { return client, nil },
			func(*ssh.Client) error { return nil },
		),
		fixture.codec,
	)
}

func (client *recoveryPipeSFTPClient) Close() error {
	clientErr := client.recoverySFTPClient.Close()
	serverErr := client.server.Close()
	select {
	case <-client.done:
	case <-time.After(time.Second):
	}
	if clientErr != nil {
		return clientErr
	}
	return serverErr
}

func (fake *recoveryTargetSFTPClientFake) RealPath(value string) (string, error) {
	if fake.realPathCalls == nil {
		fake.realPathCalls = make(map[string]int)
	}
	fake.realPathCalls[value]++
	if fake.realPath != nil {
		return fake.realPath(value, fake.realPathCalls[value])
	}
	return "", nil
}
func (fake *recoveryTargetSFTPClientFake) Lstat(value string) (os.FileInfo, error) {
	if fake.lstatCalls == nil {
		fake.lstatCalls = make(map[string]int)
	}
	fake.lstatCalls[value]++
	if fake.lstat != nil {
		return fake.lstat(value, fake.lstatCalls[value])
	}
	return nil, os.ErrNotExist
}
func (fake *recoveryTargetSFTPClientFake) ReadLink(value string) (string, error) {
	if fake.readLinkCalls == nil {
		fake.readLinkCalls = make(map[string]int)
	}
	fake.readLinkCalls[value]++
	if fake.readLink != nil {
		return fake.readLink(value, fake.readLinkCalls[value])
	}
	return "", os.ErrNotExist
}
func (*recoveryTargetSFTPClientFake) Stat(string) (os.FileInfo, error) { return nil, os.ErrNotExist }
func (fake *recoveryTargetSFTPClientFake) StatVFS(value string) (*sftp.StatVFS, error) {
	if fake.statVFSCalls == nil {
		fake.statVFSCalls = make(map[string]int)
	}
	fake.statVFSCalls[value]++
	if fake.statVFS != nil {
		return fake.statVFS(value, fake.statVFSCalls[value])
	}
	return nil, os.ErrInvalid
}
func (fake *recoveryTargetSFTPClientFake) Mkdir(string) error {
	fake.mkdirCalls++
	return nil
}
func (fake *recoveryTargetSFTPClientFake) Chmod(string, os.FileMode) error {
	fake.chmodCalls++
	return nil
}
func (fake *recoveryTargetSFTPClientFake) Open(string) (recoveryTargetSFTPFile, error) {
	fake.openCalls++
	return nil, os.ErrNotExist
}
func (fake *recoveryTargetSFTPClientFake) OpenFile(value string, flag int) (recoveryTargetSFTPFile, error) {
	fake.openFileCalls++
	if fake.openFile != nil {
		return fake.openFile(value, flag)
	}
	return nil, os.ErrNotExist
}
func (fake *recoveryTargetSFTPClientFake) Rename(string, string) error {
	fake.renameCalls++
	return nil
}
func (fake *recoveryTargetSFTPClientFake) Remove(string) error {
	fake.removeCalls++
	return nil
}
func (fake *recoveryTargetSFTPClientFake) RemoveDirectory(string) error {
	fake.removeDirectoryCalls++
	return nil
}
func (fake *recoveryTargetSFTPClientFake) Close() error {
	fake.closeCalls++
	if fake.closeOrder != nil {
		*fake.closeOrder = append(*fake.closeOrder, "sftp")
	}
	if fake.close != nil {
		return fake.close()
	}
	return nil
}

func TestRecoverySFTPTargetFactoryUsesExactPurposeAndRevisions(t *testing.T) {
	jobID := strings.Repeat("1", 32)
	for _, testCase := range []struct {
		name        string
		purpose     TargetPurpose
		wantPurpose string
	}{
		{name: "create", purpose: TargetPurposeWrite, wantPurpose: sshutil.PurposeRecoveryWrite},
		{name: "cleanup", purpose: TargetPurposeCleanup, wantPurpose: sshutil.PurposeRecoveryCleanup},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			binding := recoveryTargetSessionBindingForTest(t)
			resolver := &recoveryTargetNodeSessionResolverFake{result: recoveryTargetNodeSession{
				Node: model.Node{ID: binding.NodeID}, NodeRevision: binding.NodeRevision,
				CredentialRevision: binding.CredentialRevision,
			}}
			dialer := &recoveryTargetNodeDialerFake{}
			closeOrder := make([]string, 0, 2)
			sftpClient := &recoveryTargetSFTPClientFake{closeOrder: &closeOrder}
			factory := newRecoveryTargetSessionFactoryForTest(
				resolver, dialer,
				func(*ssh.Client) (recoveryTargetSFTPClient, error) { return sftpClient, nil },
				func(*ssh.Client) error {
					closeOrder = append(closeOrder, "ssh")
					return nil
				},
			)
			session, err := factory.Open(context.Background(), binding, testCase.purpose, jobID)
			if err != nil {
				t.Fatalf("open exact recovery target session: %v", err)
			}
			if resolver.calls != 1 || resolver.nodeID != binding.NodeID || resolver.purpose != testCase.purpose ||
				dialer.calls != 1 || dialer.node.ID != binding.NodeID || dialer.purpose != testCase.wantPurpose ||
				dialer.audit.CorrelationID != jobID || dialer.audit.Action != "" {
				t.Fatalf("resolver=%+v dialer=%+v, want exact purpose and safe job correlation", resolver, dialer)
			}
			if err := session.Close(); err != nil {
				t.Fatalf("close recovery target session: %v", err)
			}
			if err := session.Close(); err != nil {
				t.Fatalf("repeat close recovery target session: %v", err)
			}
			if !reflect.DeepEqual(closeOrder, []string{"sftp", "ssh"}) || sftpClient.closeCalls != 1 {
				t.Fatalf("close order=%v sftp calls=%d, want exactly sftp then ssh once", closeOrder, sftpClient.closeCalls)
			}
		})
	}

	binding := recoveryTargetSessionBindingForTest(t)
	resolver := &recoveryTargetNodeSessionResolverFake{result: recoveryTargetNodeSession{
		Node: model.Node{ID: binding.NodeID}, NodeRevision: binding.NodeRevision,
		CredentialRevision: "credential-revision-substituted",
	}}
	dialer := &recoveryTargetNodeDialerFake{}
	factory := newRecoveryTargetSessionFactoryForTest(
		resolver, dialer,
		func(*ssh.Client) (recoveryTargetSFTPClient, error) {
			return &recoveryTargetSFTPClientFake{}, nil
		},
		func(*ssh.Client) error { return nil },
	)
	if _, err := factory.Open(context.Background(), binding, TargetPurposeWrite, jobID); !errors.Is(err, ErrInvalidTargetPermit) {
		t.Fatalf("revision substitution error = %v, want ErrInvalidTargetPermit", err)
	}
	if dialer.calls != 0 {
		t.Fatalf("revision substitution dial calls = %d, want zero", dialer.calls)
	}
}

func TestRecoveryTargetSessionFactoryOpensRootScopedReconciliation(t *testing.T) {
	executed := recoveryTargetSessionBindingForTest(t)
	binding := recoveryTargetReconciliationSessionBinding{
		nodeID: executed.NodeID, nodeRevision: executed.NodeRevision,
		credentialRevision: executed.CredentialRevision,
		rootID:             executed.RootID, rootLocator: executed.RootLocator,
		rootLocatorDigest: executed.RootLocatorDigest, rootRevision: executed.RootRevision,
	}
	binding.bindingDigest = binding.digest()
	resolver := &recoveryTargetNodeSessionResolverFake{result: recoveryTargetNodeSession{
		Node: model.Node{ID: binding.nodeID}, NodeRevision: binding.nodeRevision,
		CredentialRevision: binding.credentialRevision,
	}}
	dialer := &recoveryTargetNodeDialerFake{}
	closeOrder := make([]string, 0, 2)
	sftpClient := &recoveryTargetSFTPClientFake{closeOrder: &closeOrder}
	factory := newRecoveryTargetSessionFactoryForTest(
		resolver, dialer,
		func(*ssh.Client) (recoveryTargetSFTPClient, error) { return sftpClient, nil },
		func(*ssh.Client) error {
			closeOrder = append(closeOrder, "ssh")
			return nil
		},
	)

	session, err := factory.OpenReconciliation(context.Background(), binding)
	if err != nil {
		t.Fatalf("open root-scoped reconciliation session: %v", err)
	}
	if resolver.calls != 1 || resolver.nodeID != binding.nodeID || resolver.purpose != TargetPurposeReconcile ||
		dialer.calls != 1 || dialer.node.ID != binding.nodeID ||
		dialer.purpose != sshutil.PurposeRecoveryReconcile ||
		dialer.audit.CorrelationID != binding.auditCorrelationID() || dialer.audit.Action != "" {
		t.Fatalf("reconciliation resolver=%+v dialer=%+v, want exact root-scoped purpose", resolver, dialer)
	}
	if dialer.audit.CorrelationID == "" || strings.Contains(dialer.audit.CorrelationID, binding.rootLocator) {
		t.Fatalf("reconciliation audit correlation is not a safe root identity: %q", dialer.audit.CorrelationID)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("close reconciliation session: %v", err)
	}
	if !reflect.DeepEqual(closeOrder, []string{"sftp", "ssh"}) || sftpClient.closeCalls != 1 {
		t.Fatalf("reconciliation close order=%v sftp calls=%d", closeOrder, sftpClient.closeCalls)
	}

	t.Run("job scoped opener remains closed", func(t *testing.T) {
		if _, err := factory.Open(
			context.Background(), executed, TargetPurposeReconcile, strings.Repeat("1", 32),
		); !errors.Is(err, ErrInvalidTargetPermit) {
			t.Fatalf("job-scoped reconciliation error=%v, want ErrInvalidTargetPermit", err)
		}
	})
}

func TestRecoveryTargetSessionFactoryRejectsReconciliationRevisionSubstitutionBeforeDial(t *testing.T) {
	executed := recoveryTargetSessionBindingForTest(t)
	binding := recoveryTargetReconciliationSessionBinding{
		nodeID: executed.NodeID, nodeRevision: executed.NodeRevision,
		credentialRevision: executed.CredentialRevision,
		rootID:             executed.RootID, rootLocator: executed.RootLocator,
		rootLocatorDigest: executed.RootLocatorDigest, rootRevision: executed.RootRevision,
	}
	binding.bindingDigest = binding.digest()

	for _, testCase := range []struct {
		name   string
		mutate func(*recoveryTargetReconciliationSessionBinding)
	}{
		{name: "node revision", mutate: func(value *recoveryTargetReconciliationSessionBinding) {
			value.nodeRevision = "node-revision-substituted"
		}},
		{name: "credential revision", mutate: func(value *recoveryTargetReconciliationSessionBinding) {
			value.credentialRevision = "credential-revision-substituted"
		}},
		{name: "root revision", mutate: func(value *recoveryTargetReconciliationSessionBinding) {
			value.rootRevision = "root-revision-substituted"
		}},
	} {
		t.Run("binding "+testCase.name, func(t *testing.T) {
			candidate := binding
			testCase.mutate(&candidate)
			resolver := &recoveryTargetNodeSessionResolverFake{}
			dialer := &recoveryTargetNodeDialerFake{}
			factory := newRecoveryTargetSessionFactoryForTest(
				resolver, dialer,
				func(*ssh.Client) (recoveryTargetSFTPClient, error) {
					t.Fatal("substituted reconciliation binding opened SFTP")
					return nil, nil
				},
				func(*ssh.Client) error { return nil },
			)
			if _, err := factory.OpenReconciliation(context.Background(), candidate); !errors.Is(err, ErrInvalidTargetPermit) {
				t.Fatalf("substituted reconciliation binding error=%v", err)
			}
			if resolver.calls != 0 || dialer.calls != 0 {
				t.Fatalf("substituted reconciliation binding resolved=%d dialed=%d", resolver.calls, dialer.calls)
			}
		})
	}

	for _, testCase := range []struct {
		name   string
		result recoveryTargetNodeSession
	}{
		{name: "node revision", result: recoveryTargetNodeSession{
			Node: model.Node{ID: binding.nodeID}, NodeRevision: "node-revision-substituted",
			CredentialRevision: binding.credentialRevision,
		}},
		{name: "credential revision", result: recoveryTargetNodeSession{
			Node: model.Node{ID: binding.nodeID}, NodeRevision: binding.nodeRevision,
			CredentialRevision: "credential-revision-substituted",
		}},
	} {
		t.Run("resolver "+testCase.name, func(t *testing.T) {
			resolver := &recoveryTargetNodeSessionResolverFake{result: testCase.result}
			dialer := &recoveryTargetNodeDialerFake{}
			factory := newRecoveryTargetSessionFactoryForTest(
				resolver, dialer,
				func(*ssh.Client) (recoveryTargetSFTPClient, error) {
					t.Fatal("substituted reconciliation resolution opened SFTP")
					return nil, nil
				},
				func(*ssh.Client) error { return nil },
			)
			if _, err := factory.OpenReconciliation(context.Background(), binding); !errors.Is(err, ErrInvalidTargetPermit) {
				t.Fatalf("substituted reconciliation resolution error=%v", err)
			}
			if resolver.calls != 1 || dialer.calls != 0 {
				t.Fatalf("substituted reconciliation resolution resolved=%d dialed=%d", resolver.calls, dialer.calls)
			}
		})
	}
}

func TestRecoveryTargetSessionFactoryOpensPurposeExactResultRead(t *testing.T) {
	binding := recoveryTargetSessionBindingForTest(t)
	jobID := strings.Repeat("1", 32)
	resolver := &recoveryTargetNodeSessionResolverFake{result: recoveryTargetNodeSession{
		Node: model.Node{ID: binding.NodeID}, NodeRevision: binding.NodeRevision,
		CredentialRevision: binding.CredentialRevision,
	}}
	dialer := &recoveryTargetNodeDialerFake{}
	closeOrder := make([]string, 0, 2)
	client := &recoveryTargetSFTPClientFake{closeOrder: &closeOrder}
	factory := newRecoveryTargetSessionFactoryForTest(
		resolver,
		dialer,
		func(*ssh.Client) (recoveryTargetSFTPClient, error) { return client, nil },
		func(*ssh.Client) error {
			closeOrder = append(closeOrder, "ssh")
			return nil
		},
	)

	session, err := factory.Open(context.Background(), binding, TargetPurposeResultRead, jobID)
	if err != nil {
		t.Fatalf("open result-read target session: %v", err)
	}
	if resolver.calls != 1 || resolver.nodeID != binding.NodeID || resolver.purpose != TargetPurposeResultRead ||
		dialer.calls != 1 || dialer.purpose != sshutil.PurposeRecoveryResultRead ||
		dialer.audit.CorrelationID != jobID {
		t.Fatalf("result-read resolver=%+v dialer=%+v", resolver, dialer)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("close result-read target session: %v", err)
	}
	if !reflect.DeepEqual(closeOrder, []string{"sftp", "ssh"}) {
		t.Fatalf("result-read close order=%v, want [sftp ssh]", closeOrder)
	}

	for _, testCase := range []struct {
		name   string
		result recoveryTargetNodeSession
		err    error
	}{
		{name: "node revision drift", result: recoveryTargetNodeSession{
			Node: model.Node{ID: binding.NodeID}, NodeRevision: "drifted-node-revision",
			CredentialRevision: binding.CredentialRevision,
		}},
		{name: "credential revision drift", result: recoveryTargetNodeSession{
			Node: model.Node{ID: binding.NodeID}, NodeRevision: binding.NodeRevision,
			CredentialRevision: "drifted-credential-revision",
		}},
		{name: "resolver unavailable", err: errors.New("RAW_RESULT_READ_RESOLVER_FAILURE")},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			resolver := &recoveryTargetNodeSessionResolverFake{result: testCase.result, err: testCase.err}
			dialer := &recoveryTargetNodeDialerFake{}
			factory := newRecoveryTargetSessionFactoryForTest(
				resolver, dialer,
				func(*ssh.Client) (recoveryTargetSFTPClient, error) {
					t.Fatal("invalid result-read authority opened SFTP")
					return nil, nil
				},
				func(*ssh.Client) error { return nil },
			)
			opened, err := factory.Open(context.Background(), binding, TargetPurposeResultRead, jobID)
			if opened != nil || dialer.calls != 0 {
				t.Fatalf("invalid result-read session=%v dial_calls=%d", opened, dialer.calls)
			}
			want := ErrInvalidTargetPermit
			if testCase.err != nil {
				want = ErrRecoveryTargetUnavailable
			}
			if err != want || strings.Contains(err.Error(), "RAW_RESULT_READ_RESOLVER_FAILURE") {
				t.Fatalf("invalid result-read error=%v, want exact %v", err, want)
			}
		})
	}
}

func TestRecoveryTargetSessionFactoryOpensPurposeExactVerify(t *testing.T) {
	binding := recoveryTargetSessionBindingForTest(t)
	jobID := strings.Repeat("1", 32)
	resolver := &recoveryTargetNodeSessionResolverFake{result: recoveryTargetNodeSession{
		Node: model.Node{ID: binding.NodeID}, NodeRevision: binding.NodeRevision,
		CredentialRevision: binding.CredentialRevision,
	}}
	dialer := &recoveryTargetNodeDialerFake{}
	closeOrder := make([]string, 0, 2)
	sftpClient := &recoveryTargetSFTPClientFake{closeOrder: &closeOrder}
	factory := newRecoveryTargetSessionFactoryForTest(
		resolver, dialer,
		func(*ssh.Client) (recoveryTargetSFTPClient, error) { return sftpClient, nil },
		func(*ssh.Client) error {
			closeOrder = append(closeOrder, "ssh")
			return nil
		},
	)

	session, err := factory.Open(context.Background(), binding, TargetPurposeVerify, jobID)
	if err != nil {
		t.Fatalf("open exact verify session: %v", err)
	}
	if resolver.calls != 1 || resolver.nodeID != binding.NodeID || resolver.purpose != TargetPurposeVerify ||
		dialer.calls != 1 || dialer.node.ID != binding.NodeID ||
		dialer.purpose != sshutil.PurposeRecoveryVerify || dialer.audit.CorrelationID != jobID ||
		dialer.audit.Action != "" {
		t.Fatalf("verify resolver=%+v dialer=%+v, want exact purpose and safe job correlation", resolver, dialer)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("close verify session: %v", err)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("repeat close verify session: %v", err)
	}
	if !reflect.DeepEqual(closeOrder, []string{"sftp", "ssh"}) || sftpClient.closeCalls != 1 {
		t.Fatalf("verify close order=%v sftp calls=%d, want exactly sftp then ssh once",
			closeOrder, sftpClient.closeCalls)
	}

	wrongRevisionResolver := &recoveryTargetNodeSessionResolverFake{result: recoveryTargetNodeSession{
		Node: model.Node{ID: binding.NodeID}, NodeRevision: "node-revision-substituted",
		CredentialRevision: binding.CredentialRevision,
	}}
	wrongRevisionDialer := &recoveryTargetNodeDialerFake{}
	wrongRevisionFactory := newRecoveryTargetSessionFactoryForTest(
		wrongRevisionResolver, wrongRevisionDialer,
		func(*ssh.Client) (recoveryTargetSFTPClient, error) { return &recoveryTargetSFTPClientFake{}, nil },
		func(*ssh.Client) error { return nil },
	)
	if _, err := wrongRevisionFactory.Open(
		context.Background(), binding, TargetPurposeVerify, jobID,
	); !errors.Is(err, ErrInvalidTargetPermit) {
		t.Fatalf("verify node-revision substitution error=%v, want ErrInvalidTargetPermit", err)
	}
	if wrongRevisionDialer.calls != 0 {
		t.Fatalf("verify node-revision substitution dial calls=%d, want zero", wrongRevisionDialer.calls)
	}
}

func TestRecoveryTargetSessionFactoryOpensPurposeExactPreflight(t *testing.T) {
	fixture := newPreflightPersistenceFixture(t)
	var plan model.BackupAssetRecoveryPlan
	if err := fixture.db.Where("id = ?", fixture.planID).Take(&plan).Error; err != nil {
		t.Fatalf("load exact draft plan: %v", err)
	}
	binding, err := newRecoveryTargetPreflightSessionBinding(plan)
	if err != nil {
		t.Fatalf("construct exact draft binding: %v", err)
	}

	t.Run("exact draft purpose and lifecycle", func(t *testing.T) {
		resolver := &recoveryTargetNodeSessionResolverFake{result: recoveryTargetNodeSession{
			Node: model.Node{ID: binding.nodeID}, NodeRevision: binding.nodeRevision,
			CredentialRevision: binding.credentialRevision,
		}}
		dialer := &recoveryTargetNodeDialerFake{}
		closeOrder := make([]string, 0, 2)
		sftpClient := &recoveryTargetSFTPClientFake{closeOrder: &closeOrder}
		commandRunner := sshutil.NewSSHCommandRunner(nil, 1)
		commandRunnerCalls := 0
		factory := newRecoveryTargetSessionFactoryForTest(
			resolver, dialer,
			func(*ssh.Client) (recoveryTargetSFTPClient, error) { return sftpClient, nil },
			func(*ssh.Client) error {
				closeOrder = append(closeOrder, "ssh")
				return nil
			},
		)
		factory.openCommandRunner = func(client *ssh.Client) *sshutil.CommandRunner {
			commandRunnerCalls++
			if client != nil {
				t.Fatal("preflight command runner did not receive the dialed SSH client")
			}
			return commandRunner
		}

		session, err := factory.OpenPreflight(context.Background(), binding)
		if err != nil {
			t.Fatalf("open exact preflight session: %v", err)
		}
		if resolver.calls != 1 || resolver.nodeID != binding.nodeID ||
			resolver.purpose != TargetPurposePreflight || dialer.calls != 1 ||
			dialer.node.ID != binding.nodeID || dialer.purpose != sshutil.PurposeRecoveryPreflight ||
			dialer.audit.CorrelationID != binding.planID || dialer.audit.Action != "" {
			t.Fatalf("preflight resolver=%+v dialer=%+v, want exact purpose and safe plan correlation", resolver, dialer)
		}
		if commandRunnerCalls != 1 || session.commandRunner != commandRunner {
			t.Fatalf("preflight command runner calls=%d attached=%t, want one/true",
				commandRunnerCalls, session.commandRunner == commandRunner)
		}
		if err := session.Close(); err != nil {
			t.Fatalf("close preflight session: %v", err)
		}
		if err := session.Close(); err != nil {
			t.Fatalf("repeat close preflight session: %v", err)
		}
		if !reflect.DeepEqual(closeOrder, []string{"sftp", "ssh"}) || sftpClient.closeCalls != 1 {
			t.Fatalf("preflight close order=%v sftp calls=%d, want exactly sftp then ssh once",
				closeOrder, sftpClient.closeCalls)
		}
	})

	t.Run("executed opener remains closed", func(t *testing.T) {
		executedBinding := recoveryTargetSessionBindingForTest(t)
		resolver := &recoveryTargetNodeSessionResolverFake{}
		dialer := &recoveryTargetNodeDialerFake{}
		factory := newRecoveryTargetSessionFactoryForTest(
			resolver, dialer,
			func(*ssh.Client) (recoveryTargetSFTPClient, error) {
				return &recoveryTargetSFTPClientFake{}, nil
			},
			func(*ssh.Client) error { return nil },
		)
		if _, err := factory.Open(
			context.Background(), executedBinding, TargetPurposePreflight, binding.planID,
		); !errors.Is(err, ErrInvalidTargetPermit) {
			t.Fatalf("executed opener preflight error=%v, want ErrInvalidTargetPermit", err)
		}
		if resolver.calls != 0 || dialer.calls != 0 {
			t.Fatalf("executed opener resolved=%d dialed=%d, want zero", resolver.calls, dialer.calls)
		}
	})

	t.Run("invalid draft state and binding stop before dependencies", func(t *testing.T) {
		nonDraft := plan
		nonDraft.State = string(PlanStatePreflightReady)
		if _, err := newRecoveryTargetPreflightSessionBinding(nonDraft); !errors.Is(err, ErrInvalidTargetPermit) {
			t.Fatalf("non-draft binding error=%v, want ErrInvalidTargetPermit", err)
		}

		resolver := &recoveryTargetNodeSessionResolverFake{}
		dialer := &recoveryTargetNodeDialerFake{}
		factory := newRecoveryTargetSessionFactoryForTest(
			resolver, dialer,
			func(*ssh.Client) (recoveryTargetSFTPClient, error) {
				return &recoveryTargetSFTPClientFake{}, nil
			},
			func(*ssh.Client) error { return nil },
		)
		invalid := binding
		invalid.bindingDigest = strings.Repeat("9", sha256DigestLength)
		if _, err := factory.OpenPreflight(context.Background(), invalid); !errors.Is(err, ErrInvalidTargetPermit) {
			t.Fatalf("invalid draft binding error=%v, want ErrInvalidTargetPermit", err)
		}
		if resolver.calls != 0 || dialer.calls != 0 {
			t.Fatalf("invalid draft binding resolved=%d dialed=%d, want zero", resolver.calls, dialer.calls)
		}
	})

	for _, testCase := range []struct {
		name   string
		mutate func(*recoveryTargetNodeSession)
	}{
		{name: "node", mutate: func(value *recoveryTargetNodeSession) { value.Node.ID++ }},
		{name: "archived", mutate: func(value *recoveryTargetNodeSession) { value.Node.Archived = true }},
		{name: "node revision", mutate: func(value *recoveryTargetNodeSession) {
			value.NodeRevision = "node-revision-substituted"
		}},
		{name: "credential revision", mutate: func(value *recoveryTargetNodeSession) {
			value.CredentialRevision = "credential-revision-substituted"
		}},
	} {
		t.Run(testCase.name+" drift stops before dial", func(t *testing.T) {
			resolved := recoveryTargetNodeSession{
				Node: model.Node{ID: binding.nodeID}, NodeRevision: binding.nodeRevision,
				CredentialRevision: binding.credentialRevision,
			}
			testCase.mutate(&resolved)
			resolver := &recoveryTargetNodeSessionResolverFake{result: resolved}
			dialer := &recoveryTargetNodeDialerFake{}
			factory := newRecoveryTargetSessionFactoryForTest(
				resolver, dialer,
				func(*ssh.Client) (recoveryTargetSFTPClient, error) {
					return &recoveryTargetSFTPClientFake{}, nil
				},
				func(*ssh.Client) error { return nil },
			)
			if _, err := factory.OpenPreflight(context.Background(), binding); !errors.Is(err, ErrInvalidTargetPermit) {
				t.Fatalf("%s drift error=%v, want ErrInvalidTargetPermit", testCase.name, err)
			}
			if resolver.calls != 1 || dialer.calls != 0 {
				t.Fatalf("%s drift resolved=%d dialed=%d, want one/zero", testCase.name, resolver.calls, dialer.calls)
			}
		})
	}

	t.Run("cancellation closes and joins", func(t *testing.T) {
		resolver := &recoveryTargetNodeSessionResolverFake{result: recoveryTargetNodeSession{
			Node: model.Node{ID: binding.nodeID}, NodeRevision: binding.nodeRevision,
			CredentialRevision: binding.credentialRevision,
		}}
		dialer := &recoveryTargetNodeDialerFake{}
		closeOrder := make([]string, 0, 2)
		allClosed := make(chan struct{})
		sftpClient := &recoveryTargetSFTPClientFake{closeOrder: &closeOrder}
		factory := newRecoveryTargetSessionFactoryForTest(
			resolver, dialer,
			func(*ssh.Client) (recoveryTargetSFTPClient, error) { return sftpClient, nil },
			func(*ssh.Client) error {
				closeOrder = append(closeOrder, "ssh")
				close(allClosed)
				return nil
			},
		)
		ctx, cancel := context.WithCancel(context.Background())
		session, err := factory.OpenPreflight(ctx, binding)
		if err != nil {
			t.Fatalf("open cancelable preflight session: %v", err)
		}
		cancel()
		select {
		case <-allClosed:
		case <-time.After(time.Second):
			_ = session.Close()
			t.Fatal("preflight session was not closed by cancellation")
		}
		if err := session.Close(); err != nil {
			t.Fatalf("join canceled preflight session: %v", err)
		}
		if !reflect.DeepEqual(closeOrder, []string{"sftp", "ssh"}) || sftpClient.closeCalls != 1 {
			t.Fatalf("canceled preflight close order=%v sftp calls=%d, want one each",
				closeOrder, sftpClient.closeCalls)
		}
	})
}

func TestRecoverySFTPTargetProbeRootCanonicalIdentityAndCapacity(t *testing.T) {
	now := time.Date(2026, time.August, 5, 1, 0, 0, 0, time.UTC)
	rootLocator := "/srv/xirang-recovery"
	rootParent := "/srv"
	rootLocatorDigest, err := settings.RecoveryTargetRootLocatorDigest(7, "root-a", rootLocator)
	if err != nil {
		t.Fatalf("root locator digest: %v", err)
	}
	object := TargetObjectRef{
		RootID: "root-a", RootLocatorDigest: rootLocatorDigest,
		PrivateRelativeLocator: "jobs/restore-item",
	}
	object.TargetPathDigest = mustTargetPathDigest(
		t, object.RootID, object.RootLocatorDigest, object.PrivateRelativeLocator,
	)
	publicPermit := TargetObservationPermit{
		SchemaVersion: 1, NodeID: 7, Purpose: TargetPurposePreflight,
		RootID: object.RootID, RootLocatorDigest: object.RootLocatorDigest,
		TargetPathDigest: object.TargetPathDigest, RootRevision: "root-revision-1",
		ExpiresAt: now.Add(time.Minute),
	}
	request := TargetProbeRequest{
		Object: object, SourceRevisionDigest: strings.Repeat("4", sha256DigestLength),
		CapabilityRevision: "capability-revision-1", PolicyRevision: "policy-revision-1",
		RequiredBytes: 1, RequiredInodes: 1,
	}
	binding := recoveryTargetPreflightSessionBindingForPermitTest(
		t, publicPermit, request, rootLocator,
	)
	permit := issueTargetPreflightPermit(publicPermit, binding, request)
	if err := permit.ValidateRequestAt(now, publicPermit, request); err != nil {
		t.Fatalf("construct exact root probe authority: %v", err)
	}

	type probeConfig struct {
		rootMode     os.FileMode
		rootUID      uint32
		rootGID      uint32
		uidOutput    string
		gidOutput    string
		rootRealPath string
		parentReal   string
		rootSymlink  bool
		parentLink   bool
		rootVFS      sftp.StatVFS
		parentVFS    sftp.StatVFS
		statErrPath  string
	}
	type probeWant struct {
		rootReal, rootCanonical, deviceValid, mountValid bool
		ownerValid, modeValid, hasSymlink                bool
		freeBytes, freeInodes                            int64
		unavailable                                      bool
		commands                                         []string
	}
	tests := []struct {
		name   string
		mutate func(*probeConfig, *probeWant)
	}{
		{name: "canonical owner and ordinary capacity"},
		{name: "effective group", mutate: func(config *probeConfig, _ *probeWant) {
			config.rootUID, config.rootGID = 2000, 3000
			config.rootMode = os.ModeDir | 0o030
			config.gidOutput = "1000 3000\n"
		}},
		{name: "root principal", mutate: func(config *probeConfig, _ *probeWant) {
			config.rootUID, config.rootGID = 2000, 3000
			config.rootMode = os.ModeDir
			config.uidOutput, config.gidOutput = "0\n", "0\n"
		}},
		{name: "root alias", mutate: func(config *probeConfig, want *probeWant) {
			config.rootRealPath = rootLocator + "-alias"
			want.rootCanonical = false
		}},
		{name: "parent alias", mutate: func(config *probeConfig, want *probeWant) {
			config.parentReal = rootParent + "-alias"
			want.rootCanonical = false
		}},
		{name: "root symlink", mutate: func(config *probeConfig, want *probeWant) {
			config.rootSymlink = true
			config.rootRealPath = rootLocator + "-resolved"
			want.rootReal, want.rootCanonical = false, false
			want.ownerValid, want.modeValid, want.hasSymlink = false, false, true
		}},
		{name: "parent symlink", mutate: func(config *probeConfig, want *probeWant) {
			config.parentLink = true
			config.parentReal = rootParent + "-resolved"
			want.rootReal, want.rootCanonical, want.hasSymlink = false, false, true
		}},
		{name: "non-directory root", mutate: func(config *probeConfig, want *probeWant) {
			config.rootMode = 0o600
			want.rootReal, want.ownerValid, want.modeValid = false, false, false
		}},
		{name: "world writable root", mutate: func(config *probeConfig, want *probeWant) {
			config.rootMode = os.ModeDir | 0o702
			want.modeValid = false
		}},
		{name: "owner lacks write", mutate: func(config *probeConfig, want *probeWant) {
			config.rootMode = os.ModeDir | 0o500
			want.ownerValid = false
		}},
		{name: "owner lacks execute", mutate: func(config *probeConfig, want *probeWant) {
			config.rootMode = os.ModeDir | 0o600
			want.ownerValid = false
		}},
		{name: "zero filesystem id", mutate: func(config *probeConfig, want *probeWant) {
			config.rootVFS.Fsid, config.parentVFS.Fsid = 0, 0
			want.deviceValid, want.mountValid = false, false
		}},
		{name: "different parent filesystem", mutate: func(config *probeConfig, want *probeWant) {
			config.parentVFS.Fsid = config.rootVFS.Fsid + 1
			want.mountValid = false
		}},
		{name: "unavailable statvfs", mutate: func(config *probeConfig, want *probeWant) {
			config.statErrPath = rootLocator
			want.unavailable = true
		}},
		{name: "byte capacity overflow", mutate: func(config *probeConfig, want *probeWant) {
			config.rootVFS.Bavail, config.rootVFS.Frsize = math.MaxInt64, 2
			want.unavailable = true
		}},
		{name: "inode capacity overflow", mutate: func(config *probeConfig, want *probeWant) {
			config.rootVFS.Favail = math.MaxInt64 + 1
			want.unavailable = true
		}},
		{name: "zero capacity", mutate: func(config *probeConfig, want *probeWant) {
			config.rootVFS.Bavail, config.rootVFS.Favail = 0, 0
			want.freeBytes, want.freeInodes = 0, 0
		}},
		{name: "multiple uid lines", mutate: func(config *probeConfig, want *probeWant) {
			config.uidOutput = "1000\n1001\n"
			want.unavailable, want.commands = true, []string{"'id' '-u'"}
		}},
		{name: "uid overflow", mutate: func(config *probeConfig, want *probeWant) {
			config.uidOutput = "4294967296\n"
			want.unavailable, want.commands = true, []string{"'id' '-u'"}
		}},
		{name: "duplicate effective group", mutate: func(config *probeConfig, want *probeWant) {
			config.gidOutput = "1000 1000\n"
			want.unavailable = true
		}},
		{name: "empty effective group", mutate: func(config *probeConfig, want *probeWant) {
			config.gidOutput = "\n"
			want.unavailable = true
		}},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			config := probeConfig{
				rootMode: os.ModeDir | 0o700, rootUID: 1000, rootGID: 1000,
				uidOutput: "1000\n", gidOutput: "1000 2000\n",
				rootRealPath: rootLocator, parentReal: rootParent,
				rootVFS: sftp.StatVFS{
					Bsize: 4096, Frsize: 4096, Blocks: 1000, Bavail: 10,
					Files: 100, Favail: 20, Fsid: 7, Namemax: 255,
				},
				parentVFS: sftp.StatVFS{
					Bsize: 4096, Frsize: 4096, Blocks: 1000, Bavail: 10,
					Files: 100, Favail: 20, Fsid: 7, Namemax: 255,
				},
			}
			want := probeWant{
				rootReal: true, rootCanonical: true, deviceValid: true, mountValid: true,
				ownerValid: true, modeValid: true, freeBytes: 10 * 4096, freeInodes: 20,
				commands: []string{"'id' '-u'", "'id' '-G'"},
			}
			if testCase.mutate != nil {
				testCase.mutate(&config, &want)
			}

			commands := make([]string, 0, 2)
			commandDeadlines := make([]time.Time, 0, 2)
			runner := recoveryPrincipalCommandRunnerForTest(
				[][]byte{[]byte(config.uidOutput), []byte(config.gidOutput)}, &commands, &commandDeadlines,
			)
			client := &recoveryTargetSFTPClientFake{}
			client.lstat = func(value string, _ int) (os.FileInfo, error) {
				if strings.HasPrefix(value, rootLocator+"/") {
					return nil, os.ErrNotExist
				}
				mode, uid, gid := os.ModeDir|0o755, uint32(0), uint32(0)
				switch value {
				case rootLocator:
					mode, uid, gid = config.rootMode, config.rootUID, config.rootGID
					if config.rootSymlink {
						mode = os.ModeSymlink | 0o777
					}
				case rootParent:
					if config.parentLink {
						mode = os.ModeSymlink | 0o777
					}
				}
				return recoveryProbeFileInfo{
					name: value, mode: mode, modTime: now.Add(-time.Minute), uid: uid, gid: gid,
				}, nil
			}
			client.realPath = func(value string, _ int) (string, error) {
				switch value {
				case rootLocator:
					return config.rootRealPath, nil
				case rootParent:
					return config.parentReal, nil
				default:
					return value, nil
				}
			}
			client.statVFS = func(value string, _ int) (*sftp.StatVFS, error) {
				if value == config.statErrPath {
					return nil, errors.New("private statvfs failure")
				}
				var result sftp.StatVFS
				switch value {
				case rootLocator:
					result = config.rootVFS
				case rootParent:
					result = config.parentVFS
				default:
					return nil, errors.New("unexpected statvfs path")
				}
				return &result, nil
			}

			resolver := &recoveryTargetNodeSessionResolverFake{result: recoveryTargetNodeSession{
				Node: model.Node{ID: binding.nodeID}, NodeRevision: binding.nodeRevision,
				CredentialRevision: binding.credentialRevision,
			}}
			factory := newRecoveryTargetSessionFactoryForTest(
				resolver, &recoveryTargetNodeDialerFake{},
				func(*ssh.Client) (recoveryTargetSFTPClient, error) { return client, nil },
				func(*ssh.Client) error { return nil },
			)
			factory.openCommandRunner = func(*ssh.Client) *sshutil.CommandRunner { return runner }
			target := newRecoverySFTPTargetForTest(factory, nil)
			target.now = func() time.Time { return now }

			facts, probeErr := target.ProbeRoot(context.Background(), permit, request)
			if !reflect.DeepEqual(commands, want.commands) {
				t.Fatalf("commands=%v, want exact fixed commands %v", commands, want.commands)
			}
			if len(commandDeadlines) != len(commands) ||
				(len(commandDeadlines) > 1 && !commandDeadlines[0].Equal(commandDeadlines[1])) {
				t.Fatalf("command deadlines=%v, want one shared permit deadline for %d commands",
					commandDeadlines, len(commands))
			}
			if want.unavailable {
				if !errors.Is(probeErr, ErrRecoveryTargetUnavailable) || facts != (TargetRootProbeFacts{}) {
					t.Fatalf("unavailable probe facts=%+v error=%v", facts, probeErr)
				}
			} else {
				if probeErr != nil {
					t.Fatalf("probe root: %v", probeErr)
				}
				if facts.ObservedAt != now || facts.ExpiresAt != publicPermit.ExpiresAt ||
					facts.CredentialRevision != binding.credentialRevision || !facts.RequiredToolsAvailable ||
					facts.RootReal != want.rootReal || facts.RootCanonical != want.rootCanonical ||
					facts.DeviceValid != want.deviceValid || facts.MountValid != want.mountValid ||
					facts.OwnerValid != want.ownerValid || facts.ModeValid != want.modeValid ||
					facts.HasSymlinkComponent != want.hasSymlink || facts.FreeBytes != want.freeBytes ||
					facts.FreeInodes != want.freeInodes {
					t.Fatalf("probe facts=%+v, want=%+v", facts, want)
				}
				if client.lstatCalls[rootLocator] < 2 || client.realPathCalls[rootLocator] < 2 ||
					client.statVFSCalls[rootLocator] < 2 {
					t.Fatalf("root was not fully re-observed: lstat=%v realpath=%v statvfs=%v",
						client.lstatCalls, client.realPathCalls, client.statVFSCalls)
				}
			}
			if client.mkdirCalls != 0 || client.chmodCalls != 0 || client.openCalls != 0 ||
				client.openFileCalls != 0 || client.renameCalls != 0 || client.removeCalls != 0 {
				t.Fatalf("read-only probe mutated target: %+v", client)
			}
		})
	}
}

type recoverySFTPTargetProbeConfigForTest struct {
	now             time.Time
	nodeID          uint
	rootID          string
	rootLocator     string
	relativeLocator string
	rootMode        os.FileMode
	rootUID         uint32
	rootGID         uint32
	rootMtime       time.Time
	targetMode      os.FileMode
	targetUID       uint32
	targetGID       uint32
	targetSize      int64
	targetMtime     time.Time
	rootVFS         sftp.StatVFS
	parentVFS       sftp.StatVFS
	missingPath     string
	ambiguousPath   string
	aliasPath       string
	symlinkPath     string
	nonDirPath      string
	rootReplacement bool
	targetReplace   bool
	filesystemDrift bool
	freeDrift       bool
}

func newRecoverySFTPTargetProbeConfigForTest() recoverySFTPTargetProbeConfigForTest {
	now := time.Date(2026, time.August, 5, 2, 0, 0, 0, time.UTC)
	rootVFS := sftp.StatVFS{
		Bsize: 4096, Frsize: 4096, Blocks: 1000, Bfree: 12, Bavail: 10,
		Files: 100, Ffree: 22, Favail: 20, Fsid: 7, Flag: 1, Namemax: 255,
	}
	return recoverySFTPTargetProbeConfigForTest{
		now: now, nodeID: 7, rootID: "root-a", rootLocator: "/srv/xirang-recovery",
		relativeLocator: "jobs/restore/item.bin",
		rootMode:        os.ModeDir | 0o700, rootUID: 1000, rootGID: 1000,
		rootMtime:  now.Add(-3 * time.Minute),
		targetMode: 0o600, targetUID: 1000, targetGID: 1000, targetSize: 17,
		targetMtime: now.Add(-2 * time.Minute), rootVFS: rootVFS, parentVFS: rootVFS,
	}
}

func runRecoverySFTPTargetProbeForTest(
	t *testing.T,
	config recoverySFTPTargetProbeConfigForTest,
) (TargetRootProbeFacts, error) {
	t.Helper()
	rootLocatorDigest, err := settings.RecoveryTargetRootLocatorDigest(
		config.nodeID, config.rootID, config.rootLocator,
	)
	if err != nil {
		t.Fatalf("root locator digest: %v", err)
	}
	object := TargetObjectRef{
		RootID: config.rootID, RootLocatorDigest: rootLocatorDigest,
		PrivateRelativeLocator: config.relativeLocator,
	}
	object.TargetPathDigest = mustTargetPathDigest(
		t, object.RootID, object.RootLocatorDigest, object.PrivateRelativeLocator,
	)
	publicPermit := TargetObservationPermit{
		SchemaVersion: 1, NodeID: config.nodeID, Purpose: TargetPurposePreflight,
		RootID: object.RootID, RootLocatorDigest: object.RootLocatorDigest,
		TargetPathDigest: object.TargetPathDigest, RootRevision: "root-revision-before-probe",
		ExpiresAt: config.now.Add(time.Minute),
	}
	request := TargetProbeRequest{
		Object: object, SourceRevisionDigest: strings.Repeat("4", sha256DigestLength),
		CapabilityRevision: "capability-revision-1", PolicyRevision: "policy-revision-1",
		RequiredBytes: 1, RequiredInodes: 1,
	}
	binding := recoveryTargetPreflightSessionBindingForPermitTest(
		t, publicPermit, request, config.rootLocator,
	)
	permit := issueTargetPreflightPermit(publicPermit, binding, request)
	if err := permit.ValidateRequestAt(config.now, publicPermit, request); err != nil {
		t.Fatalf("construct exact target probe authority: %v", err)
	}

	finalPath := path.Join(config.rootLocator, config.relativeLocator)
	client := &recoveryTargetSFTPClientFake{}
	client.lstat = func(value string, call int) (os.FileInfo, error) {
		if value == config.ambiguousPath {
			return nil, errors.New("private target lstat ambiguity")
		}
		if value == config.missingPath {
			return nil, os.ErrNotExist
		}
		mode, uid, gid, size, mtime := os.ModeDir|0o755, uint32(0), uint32(0), int64(0), config.rootMtime
		switch value {
		case config.rootLocator:
			mode, uid, gid, mtime = config.rootMode, config.rootUID, config.rootGID, config.rootMtime
			if config.rootReplacement && call > 1 {
				mode ^= 0o010
			}
		case finalPath:
			mode, uid, gid, size, mtime = config.targetMode, config.targetUID,
				config.targetGID, config.targetSize, config.targetMtime
			if config.targetReplace && call > 1 {
				size++
			}
		}
		if value == config.symlinkPath {
			mode = os.ModeSymlink | 0o777
		}
		if value == config.nonDirPath {
			mode = 0o600
		}
		return recoveryProbeFileInfo{
			name: value, size: size, mode: mode, modTime: mtime, uid: uid, gid: gid,
		}, nil
	}
	client.realPath = func(value string, _ int) (string, error) {
		if value == config.aliasPath {
			return value + "-alias", nil
		}
		return value, nil
	}
	client.statVFS = func(value string, call int) (*sftp.StatVFS, error) {
		var result sftp.StatVFS
		switch value {
		case config.rootLocator:
			result = config.rootVFS
			if config.filesystemDrift && call > 1 {
				result.Fsid++
			}
			if config.freeDrift && call > 1 {
				result.Bfree, result.Bavail, result.Ffree, result.Favail = 13, 11, 23, 21
			}
		case path.Dir(config.rootLocator):
			result = config.parentVFS
		default:
			if strings.HasPrefix(value, config.rootLocator+"/") {
				result = config.rootVFS
			} else {
				return nil, errors.New("unexpected target statvfs path")
			}
		}
		return &result, nil
	}

	commands := make([]string, 0, 2)
	deadlines := make([]time.Time, 0, 2)
	runner := recoveryPrincipalCommandRunnerForTest(
		[][]byte{[]byte("1000\n"), []byte("1000 2000\n")}, &commands, &deadlines,
	)
	resolver := &recoveryTargetNodeSessionResolverFake{result: recoveryTargetNodeSession{
		Node: model.Node{ID: binding.nodeID}, NodeRevision: binding.nodeRevision,
		CredentialRevision: binding.credentialRevision,
	}}
	factory := newRecoveryTargetSessionFactoryForTest(
		resolver, &recoveryTargetNodeDialerFake{},
		func(*ssh.Client) (recoveryTargetSFTPClient, error) { return client, nil },
		func(*ssh.Client) error { return nil },
	)
	factory.openCommandRunner = func(*ssh.Client) *sshutil.CommandRunner { return runner }
	target := newRecoverySFTPTargetForTest(factory, nil)
	target.now = func() time.Time { return config.now }
	facts, probeErr := target.ProbeRoot(context.Background(), permit, request)
	if !reflect.DeepEqual(commands, []string{"'id' '-u'", "'id' '-G'"}) {
		t.Fatalf("commands=%v, want exact fixed principal commands", commands)
	}
	if client.mkdirCalls != 0 || client.chmodCalls != 0 || client.openCalls != 0 ||
		client.openFileCalls != 0 || client.renameCalls != 0 || client.removeCalls != 0 {
		t.Fatalf("target probe mutated remote state: %+v", client)
	}
	return facts, probeErr
}

func recoverySFTPProbeRevisionForTest(
	t *testing.T,
	prefix string,
	domain string,
	values ...string,
) string {
	t.Helper()
	digest := framedDigest(domain, values...)
	raw, err := hex.DecodeString(digest)
	if err != nil {
		t.Fatalf("decode probe revision digest: %v", err)
	}
	return prefix + base64.RawURLEncoding.EncodeToString(raw)
}

func TestRecoverySFTPTargetProbeRootTargetMatrixAndRevisions(t *testing.T) {
	base := newRecoverySFTPTargetProbeConfigForTest()

	matrix := []struct {
		name       string
		mutate     func(*recoverySFTPTargetProbeConfigForTest)
		wantExists bool
		wantErr    error
	}{
		{name: "absent final", mutate: func(config *recoverySFTPTargetProbeConfigForTest) {
			config.missingPath = path.Join(config.rootLocator, config.relativeLocator)
		}},
		{name: "absent intermediate", mutate: func(config *recoverySFTPTargetProbeConfigForTest) {
			config.missingPath = path.Join(config.rootLocator, "jobs")
		}},
		{name: "regular", wantExists: true},
		{name: "directory", wantExists: true, mutate: func(config *recoverySFTPTargetProbeConfigForTest) {
			config.targetMode, config.targetSize = os.ModeDir|0o700, 0
		}},
		{name: "symlink", wantExists: true, mutate: func(config *recoverySFTPTargetProbeConfigForTest) {
			config.targetMode, config.targetSize = os.ModeSymlink|0o777, 11
		}},
		{name: "special", wantExists: true, mutate: func(config *recoverySFTPTargetProbeConfigForTest) {
			config.targetMode, config.targetSize = os.ModeNamedPipe|0o600, 0
		}},
		{name: "prefix alias", wantErr: ErrRecoveryTargetChanged, mutate: func(config *recoverySFTPTargetProbeConfigForTest) {
			config.aliasPath = path.Join(config.rootLocator, "jobs")
		}},
		{name: "prefix symlink", wantErr: ErrRecoveryTargetChanged, mutate: func(config *recoverySFTPTargetProbeConfigForTest) {
			config.symlinkPath = path.Join(config.rootLocator, "jobs")
		}},
		{name: "prefix non-directory", wantErr: ErrRecoveryTargetChanged, mutate: func(config *recoverySFTPTargetProbeConfigForTest) {
			config.nonDirPath = path.Join(config.rootLocator, "jobs")
		}},
		{name: "ambiguous target error", wantErr: ErrRecoveryTargetUnavailable, mutate: func(config *recoverySFTPTargetProbeConfigForTest) {
			config.ambiguousPath = path.Join(config.rootLocator, "jobs")
		}},
		{name: "root replacement", wantErr: ErrRecoveryTargetChanged, mutate: func(config *recoverySFTPTargetProbeConfigForTest) {
			config.rootReplacement = true
		}},
		{name: "target replacement", wantErr: ErrRecoveryTargetChanged, mutate: func(config *recoverySFTPTargetProbeConfigForTest) {
			config.targetReplace = true
		}},
		{name: "filesystem drift", wantErr: ErrRecoveryTargetChanged, mutate: func(config *recoverySFTPTargetProbeConfigForTest) {
			config.filesystemDrift = true
		}},
		{name: "free capacity drift", wantExists: true, mutate: func(config *recoverySFTPTargetProbeConfigForTest) {
			config.freeDrift = true
		}},
	}
	for _, testCase := range matrix {
		t.Run(testCase.name, func(t *testing.T) {
			config := base
			if testCase.mutate != nil {
				testCase.mutate(&config)
			}
			facts, err := runRecoverySFTPTargetProbeForTest(t, config)
			if testCase.wantErr != nil {
				if !errors.Is(err, testCase.wantErr) || facts != (TargetRootProbeFacts{}) {
					t.Fatalf("probe facts=%+v error=%v, want zero facts and %v", facts, err, testCase.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("probe target matrix: %v", err)
			}
			if facts.TargetExists != testCase.wantExists || facts.RootRevision == "" ||
				facts.FilesystemRevision == "" || facts.TargetRevision == "" {
				t.Fatalf("probe facts=%+v, want exists=%t and all revisions", facts, testCase.wantExists)
			}
			if testCase.name == "free capacity drift" &&
				(facts.FreeBytes != 11*4096 || facts.FreeInodes != 21) {
				t.Fatalf("free drift facts=%+v, want latest capacity 45056/21", facts)
			}
		})
	}

	baseline, err := runRecoverySFTPTargetProbeForTest(t, base)
	if err != nil {
		t.Fatalf("baseline target probe: %v", err)
	}
	rootLocatorDigest, err := settings.RecoveryTargetRootLocatorDigest(
		base.nodeID, base.rootID, base.rootLocator,
	)
	if err != nil {
		t.Fatalf("baseline root locator digest: %v", err)
	}
	wantRoot := recoverySFTPProbeRevisionForTest(
		t, "sftpr1:", "xirang/recovery/sftp-root-observation/v1",
		strconv.FormatUint(uint64(base.nodeID), 10), base.rootID, rootLocatorDigest,
		base.rootLocator, strconv.FormatUint(uint64(base.rootMode), 10),
		strconv.FormatUint(uint64(base.rootUID), 10), strconv.FormatUint(uint64(base.rootGID), 10),
		strconv.FormatUint(base.rootVFS.Fsid, 10),
	)
	wantFilesystem := recoverySFTPProbeRevisionForTest(
		t, "sftpf1:", "xirang/recovery/sftp-filesystem-observation/v1",
		strconv.FormatUint(base.rootVFS.Fsid, 10), strconv.FormatUint(base.rootVFS.Bsize, 10),
		strconv.FormatUint(base.rootVFS.Frsize, 10), strconv.FormatUint(base.rootVFS.Blocks, 10),
		strconv.FormatUint(base.rootVFS.Files, 10), strconv.FormatUint(base.rootVFS.Flag, 10),
		strconv.FormatUint(base.rootVFS.Namemax, 10),
	)
	wantTarget := recoverySFTPProbeRevisionForTest(
		t, "sftpt1:", "xirang/recovery/sftp-target-observation/v1",
		wantRoot, base.relativeLocator, string(TargetEntryRegular), strconv.FormatInt(base.targetSize, 10),
		strconv.FormatUint(uint64(base.targetMode), 10), strconv.FormatUint(uint64(base.targetUID), 10),
		strconv.FormatUint(uint64(base.targetGID), 10), strconv.FormatInt(base.targetMtime.Unix(), 10),
	)
	if baseline.RootRevision != wantRoot || baseline.FilesystemRevision != wantFilesystem ||
		baseline.TargetRevision != wantTarget {
		t.Fatalf("revisions root=%q fs=%q target=%q, want %q %q %q",
			baseline.RootRevision, baseline.FilesystemRevision, baseline.TargetRevision,
			wantRoot, wantFilesystem, wantTarget)
	}
	absentConfig := base
	absentConfig.missingPath = path.Join(absentConfig.rootLocator, absentConfig.relativeLocator)
	absent, err := runRecoverySFTPTargetProbeForTest(t, absentConfig)
	if err != nil {
		t.Fatalf("exact absent target probe: %v", err)
	}
	wantAbsentTarget := recoverySFTPProbeRevisionForTest(
		t, "sftpt1:", "xirang/recovery/sftp-target-observation/v1",
		wantRoot, base.relativeLocator, "absent",
	)
	if absent.TargetExists || absent.TargetRevision != wantAbsentTarget {
		t.Fatalf("absent target facts=%+v, want exact revision %q", absent, wantAbsentTarget)
	}
	for name, revision := range map[string]string{
		"root": baseline.RootRevision, "filesystem": baseline.FilesystemRevision, "target": baseline.TargetRevision,
	} {
		if len(revision) != 50 || !validOpaqueRevision(revision) || sha256Shaped(revision) {
			t.Fatalf("%s revision=%q, want exact 50-byte non-SHA opaque token", name, revision)
		}
	}
	stable, err := runRecoverySFTPTargetProbeForTest(t, base)
	if err != nil || stable.RootRevision != baseline.RootRevision ||
		stable.FilesystemRevision != baseline.FilesystemRevision || stable.TargetRevision != baseline.TargetRevision {
		t.Fatalf("stable probe facts=%+v error=%v, want identical revisions", stable, err)
	}

	variants := []struct {
		name                                 string
		mutate                               func(*recoverySFTPTargetProbeConfigForTest)
		rootDiff, filesystemDiff, targetDiff bool
	}{
		{name: "node", rootDiff: true, targetDiff: true, mutate: func(config *recoverySFTPTargetProbeConfigForTest) { config.nodeID++ }},
		{name: "root id", rootDiff: true, targetDiff: true, mutate: func(config *recoverySFTPTargetProbeConfigForTest) { config.rootID = "root-b" }},
		{name: "root locator", rootDiff: true, targetDiff: true, mutate: func(config *recoverySFTPTargetProbeConfigForTest) { config.rootLocator += "-b" }},
		{name: "target path", targetDiff: true, mutate: func(config *recoverySFTPTargetProbeConfigForTest) { config.relativeLocator = "jobs/restore/other.bin" }},
		{name: "target kind", targetDiff: true, mutate: func(config *recoverySFTPTargetProbeConfigForTest) { config.targetMode = os.ModeDir | 0o600 }},
		{name: "target size", targetDiff: true, mutate: func(config *recoverySFTPTargetProbeConfigForTest) { config.targetSize++ }},
		{name: "target mode", targetDiff: true, mutate: func(config *recoverySFTPTargetProbeConfigForTest) { config.targetMode = 0o640 }},
		{name: "target uid", targetDiff: true, mutate: func(config *recoverySFTPTargetProbeConfigForTest) { config.targetUID++ }},
		{name: "target gid", targetDiff: true, mutate: func(config *recoverySFTPTargetProbeConfigForTest) { config.targetGID++ }},
		{name: "target mtime", targetDiff: true, mutate: func(config *recoverySFTPTargetProbeConfigForTest) {
			config.targetMtime = config.targetMtime.Add(time.Second)
		}},
		{name: "filesystem id", rootDiff: true, filesystemDiff: true, targetDiff: true, mutate: func(config *recoverySFTPTargetProbeConfigForTest) { config.rootVFS.Fsid++; config.parentVFS.Fsid++ }},
		{name: "filesystem bsize", filesystemDiff: true, mutate: func(config *recoverySFTPTargetProbeConfigForTest) { config.rootVFS.Bsize++ }},
		{name: "filesystem frsize", filesystemDiff: true, mutate: func(config *recoverySFTPTargetProbeConfigForTest) { config.rootVFS.Frsize++ }},
		{name: "filesystem blocks", filesystemDiff: true, mutate: func(config *recoverySFTPTargetProbeConfigForTest) { config.rootVFS.Blocks++ }},
		{name: "filesystem files", filesystemDiff: true, mutate: func(config *recoverySFTPTargetProbeConfigForTest) { config.rootVFS.Files++ }},
		{name: "filesystem flag", filesystemDiff: true, mutate: func(config *recoverySFTPTargetProbeConfigForTest) { config.rootVFS.Flag++ }},
		{name: "filesystem namemax", filesystemDiff: true, mutate: func(config *recoverySFTPTargetProbeConfigForTest) { config.rootVFS.Namemax++ }},
		{name: "volatile free counters", mutate: func(config *recoverySFTPTargetProbeConfigForTest) {
			config.rootVFS.Bfree++
			config.rootVFS.Bavail++
			config.rootVFS.Ffree++
			config.rootVFS.Favail++
		}},
	}
	for _, variant := range variants {
		t.Run("revision difference "+variant.name, func(t *testing.T) {
			config := base
			variant.mutate(&config)
			facts, err := runRecoverySFTPTargetProbeForTest(t, config)
			if err != nil {
				t.Fatalf("variant probe: %v", err)
			}
			if (facts.RootRevision != baseline.RootRevision) != variant.rootDiff ||
				(facts.FilesystemRevision != baseline.FilesystemRevision) != variant.filesystemDiff ||
				(facts.TargetRevision != baseline.TargetRevision) != variant.targetDiff {
				t.Fatalf("variant revisions root=%q fs=%q target=%q, want differences %t/%t/%t",
					facts.RootRevision, facts.FilesystemRevision, facts.TargetRevision,
					variant.rootDiff, variant.filesystemDiff, variant.targetDiff)
			}
		})
	}
}

func TestRecoverySFTPTargetProbeRootCancellationPrivacyAndNoMutation(t *testing.T) {
	var capturedLogs bytes.Buffer
	previousLogger := logger.Log
	logger.Log = zerolog.New(&capturedLogs)
	t.Cleanup(func() { logger.Log = previousLogger })

	now := time.Date(2026, time.August, 5, 3, 0, 0, 0, time.UTC)
	privateRoot := "/srv/FAKE_PRIVATE_ROOT_R30"
	privatePath := "FAKE_PRIVATE_PATH_R30/item.bin"
	privateHost := "FAKE_PRIVATE_HOST_R30"
	privateUser := "FAKE_PRIVATE_USER_R30"
	privateCredential := "FAKE_PRIVATE_CREDENTIAL_R30"
	privateUID := "4000000001"
	privateGID := "4000000002"
	privateCommand := "FAKE_PRIVATE_COMMAND_R30"
	privateStat := "FAKE_PRIVATE_STAT_R30"

	rootLocatorDigest, err := settings.RecoveryTargetRootLocatorDigest(7, "root-r30", privateRoot)
	if err != nil {
		t.Fatalf("root locator digest: %v", err)
	}
	object := TargetObjectRef{
		RootID: "root-r30", RootLocatorDigest: rootLocatorDigest,
		PrivateRelativeLocator: privatePath,
	}
	object.TargetPathDigest = mustTargetPathDigest(
		t, object.RootID, object.RootLocatorDigest, object.PrivateRelativeLocator,
	)
	publicPermit := TargetObservationPermit{
		SchemaVersion: 1, NodeID: 7, Purpose: TargetPurposePreflight,
		RootID: object.RootID, RootLocatorDigest: object.RootLocatorDigest,
		TargetPathDigest: object.TargetPathDigest, RootRevision: "root-revision-r30",
		ExpiresAt: now.Add(time.Minute),
	}
	request := TargetProbeRequest{
		Object: object, SourceRevisionDigest: strings.Repeat("4", sha256DigestLength),
		CapabilityRevision: "capability-revision-r30", PolicyRevision: "policy-revision-r30",
		RequiredBytes: 1, RequiredInodes: 1,
	}
	binding := recoveryTargetPreflightSessionBindingForPermitTest(
		t, publicPermit, request, privateRoot,
	)
	permit := issueTargetPreflightPermit(publicPermit, binding, request)
	if err := permit.ValidateRequestAt(now, publicPermit, request); err != nil {
		t.Fatalf("construct R30 target authority: %v", err)
	}

	type probeConfig struct {
		resolverErr    error
		dialErr        error
		openErr        error
		statErr        error
		commandWaitErr error
		closeSFTPErr   error
		closeSSHErr    error
		uidOutput      []byte
		gidOutput      []byte
		blockStage     string
		started        chan struct{}
	}
	type probeState struct {
		client          *recoveryTargetSFTPClientFake
		commandSessions []*recoveryProbeCommandSessionForR30
		sshCloseCalls   int
		sshOpened       bool
		sftpOpened      bool
	}
	type probeResult struct {
		facts TargetRootProbeFacts
		err   error
		state *probeState
	}

	buildProbe := func(ctx context.Context, config probeConfig) (*recoverySFTPTarget, *probeState) {
		state := &probeState{client: &recoveryTargetSFTPClientFake{}}
		resolved := recoveryTargetNodeSession{
			Node: model.Node{
				ID: binding.nodeID, Host: privateHost, Username: privateUser,
				Password: privateCredential,
			},
			NodeRevision: binding.nodeRevision, CredentialRevision: binding.credentialRevision,
		}
		var blockOnce sync.Once
		resolver := &recoveryTargetNodeSessionResolverFake{result: resolved, err: config.resolverErr}
		if config.blockStage == "resolver" {
			resolver.resolve = func(resolveCtx context.Context, _ uint, _ TargetPurpose) (recoveryTargetNodeSession, error) {
				blockOnce.Do(func() { close(config.started) })
				<-resolveCtx.Done()
				return recoveryTargetNodeSession{}, fmt.Errorf("resolver %s: %w", privateHost, resolveCtx.Err())
			}
		}
		dialer := &recoveryTargetNodeDialerFake{err: config.dialErr}

		closedSFTP := make(chan struct{})
		var closedSFTPOnce sync.Once
		state.client.close = func() error {
			if config.blockStage == "close" {
				blockOnce.Do(func() { close(config.started) })
				<-ctx.Done()
			}
			closedSFTPOnce.Do(func() { close(closedSFTP) })
			return config.closeSFTPErr
		}
		finalPath := path.Join(privateRoot, privatePath)
		state.client.lstat = func(value string, _ int) (os.FileInfo, error) {
			mode := os.ModeDir | 0o755
			size := int64(0)
			if value == privateRoot {
				mode = os.ModeDir | 0o700
			}
			if value == finalPath {
				mode, size = 0o600, 17
			}
			return recoveryProbeFileInfo{
				name: value, size: size, mode: mode, modTime: now.Add(-time.Minute),
				uid: 4000000001, gid: 4000000002,
			}, nil
		}
		state.client.realPath = func(value string, _ int) (string, error) { return value, nil }
		state.client.statVFS = func(_ string, _ int) (*sftp.StatVFS, error) {
			if config.blockStage == "sftp" {
				blockOnce.Do(func() { close(config.started) })
				<-closedSFTP
				return nil, fmt.Errorf("statvfs %s", privateStat)
			}
			if config.statErr != nil {
				return nil, config.statErr
			}
			return &sftp.StatVFS{
				Bsize: 4096, Frsize: 4096, Blocks: 1000, Bavail: 10,
				Files: 100, Favail: 20, Fsid: 7, Namemax: 255,
			}, nil
		}

		uidOutput := config.uidOutput
		if uidOutput == nil {
			uidOutput = []byte(privateUID + "\n")
		}
		gidOutput := config.gidOutput
		if gidOutput == nil {
			gidOutput = []byte(privateUID + " " + privateGID + "\n")
		}
		outputs := [][]byte{uidOutput, gidOutput}
		commandIndex := 0
		runner := sshutil.NewCommandRunner(func(commandCtx context.Context) (sshutil.CommandSession, error) {
			if commandIndex >= len(outputs) {
				return nil, errors.New("unexpected R30 principal command")
			}
			session := &recoveryProbeCommandSessionForR30{
				ctx: commandCtx, stdout: outputs[commandIndex], waitDone: make(chan struct{}),
			}
			if config.blockStage == "command" && commandIndex == 0 {
				session.blockUntilCancel = true
				session.waitStarted = config.started
			}
			if commandIndex == 0 {
				session.waitErr = config.commandWaitErr
			}
			commandIndex++
			state.commandSessions = append(state.commandSessions, session)
			return session, nil
		}, 1)

		factory := newRecoveryTargetSessionFactoryForTest(
			resolver, dialer,
			func(*ssh.Client) (recoveryTargetSFTPClient, error) {
				state.sshOpened = true
				if config.openErr != nil {
					return nil, config.openErr
				}
				state.sftpOpened = true
				return state.client, nil
			},
			func(*ssh.Client) error {
				state.sshCloseCalls++
				return config.closeSSHErr
			},
		)
		factory.openCommandRunner = func(*ssh.Client) *sshutil.CommandRunner { return runner }
		target := newRecoverySFTPTargetForTest(factory, nil)
		target.now = func() time.Time { return now }
		return target, state
	}

	runProbe := func(ctx context.Context, config probeConfig) probeResult {
		target, state := buildProbe(ctx, config)
		facts, probeErr := target.ProbeRoot(ctx, permit, request)
		return probeResult{facts: facts, err: probeErr, state: state}
	}
	assertReadOnlyAndClosedAtMostOnce := func(t *testing.T, state *probeState) {
		t.Helper()
		client := state.client
		if client.mkdirCalls != 0 || client.chmodCalls != 0 || client.openCalls != 0 ||
			client.openFileCalls != 0 || client.renameCalls != 0 || client.removeCalls != 0 {
			t.Fatalf("R30 probe mutated target: %+v", client)
		}
		wantSFTPClose := 0
		if state.sftpOpened {
			wantSFTPClose = 1
		}
		wantSSHClose := 0
		if state.sshOpened {
			wantSSHClose = 1
		}
		if client.closeCalls != wantSFTPClose || state.sshCloseCalls != wantSSHClose {
			t.Fatalf("R30 resource close calls sftp=%d ssh=%d, want exactly %d/%d",
				client.closeCalls, state.sshCloseCalls, wantSFTPClose, wantSSHClose)
		}
		for index, session := range state.commandSessions {
			if calls := session.closeCount(); calls != 1 {
				t.Fatalf("R30 command session %d close calls=%d, want exactly once", index, calls)
			}
			select {
			case <-session.waitDone:
			default:
				t.Fatalf("R30 command session %d goroutine was not joined", index)
			}
		}
	}

	privateErrors := []error{
		errors.New("FAKE_PRIVATE_RESOLVER_R30"),
		errors.New("FAKE_PRIVATE_DIAL_R30"),
		errors.New("FAKE_PRIVATE_SFTP_OPEN_R30"),
		errors.New(privateStat),
		errors.New(privateCommand),
		errors.New("FAKE_PRIVATE_SFTP_CLOSE_R30"),
		errors.New("FAKE_PRIVATE_SSH_CLOSE_R30"),
	}
	failures := []struct {
		name    string
		config  probeConfig
		wantErr error
	}{
		{name: "resolver", config: probeConfig{resolverErr: privateErrors[0]}, wantErr: ErrRecoveryTargetUnavailable},
		{name: "dial", config: probeConfig{dialErr: privateErrors[1]}, wantErr: ErrRecoveryTargetUnavailable},
		{name: "sftp open", config: probeConfig{openErr: privateErrors[2]}, wantErr: ErrRecoveryTargetUnavailable},
		{name: "statvfs", config: probeConfig{statErr: privateErrors[3]}, wantErr: ErrRecoveryTargetUnavailable},
		{name: "command", config: probeConfig{commandWaitErr: privateErrors[4]}, wantErr: ErrRecoveryTargetUnavailable},
		{name: "parse", config: probeConfig{uidOutput: []byte("42949672960\n")}, wantErr: ErrRecoveryTargetUnavailable},
		{name: "sftp close", config: probeConfig{closeSFTPErr: privateErrors[5]}, wantErr: ErrRecoveryTargetUnavailable},
		{name: "ssh close", config: probeConfig{closeSSHErr: privateErrors[6]}, wantErr: ErrRecoveryTargetUnavailable},
		{
			name:    "resolver cancellation identity",
			config:  probeConfig{resolverErr: fmt.Errorf("FAKE_PRIVATE_RESOLVER_CANCEL_R30: %w", context.Canceled)},
			wantErr: context.Canceled,
		},
		{
			name:    "statvfs cancellation identity",
			config:  probeConfig{statErr: fmt.Errorf("FAKE_PRIVATE_STAT_CANCEL_R30: %w", context.Canceled)},
			wantErr: context.Canceled,
		},
		{
			name:    "close deadline identity",
			config:  probeConfig{closeSFTPErr: fmt.Errorf("FAKE_PRIVATE_CLOSE_DEADLINE_R30: %w", context.DeadlineExceeded)},
			wantErr: context.DeadlineExceeded,
		},
	}
	observedErrors := make([]error, 0, len(failures)+4)
	for _, testCase := range failures {
		t.Run("failure "+testCase.name, func(t *testing.T) {
			result := runProbe(context.Background(), testCase.config)
			observedErrors = append(observedErrors, result.err)
			if result.err != testCase.wantErr || result.facts != (TargetRootProbeFacts{}) {
				t.Fatalf("R30 failure facts=%+v error=%v, want zero facts and exact %v",
					result.facts, result.err, testCase.wantErr)
			}
			assertReadOnlyAndClosedAtMostOnce(t, result.state)
		})
	}

	for _, testCase := range []struct {
		name, stage string
		contextErr  error
	}{
		{name: "resolver canceled", stage: "resolver", contextErr: context.Canceled},
		{name: "command deadline", stage: "command", contextErr: context.DeadlineExceeded},
		{name: "sftp canceled", stage: "sftp", contextErr: context.Canceled},
		{name: "close deadline", stage: "close", contextErr: context.DeadlineExceeded},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := newRecoveryProbeContextForR30(testCase.contextErr)
			started := make(chan struct{})
			target, state := buildProbe(ctx, probeConfig{blockStage: testCase.stage, started: started})
			done := make(chan probeResult, 1)
			go func() {
				facts, probeErr := target.ProbeRoot(ctx, permit, request)
				done <- probeResult{facts: facts, err: probeErr, state: state}
			}()
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatalf("R30 %s cancellation point was not reached", testCase.stage)
			}
			ctx.trigger()
			select {
			case result := <-done:
				observedErrors = append(observedErrors, result.err)
				if result.err != testCase.contextErr || result.facts != (TargetRootProbeFacts{}) {
					t.Fatalf("R30 canceled facts=%+v error=%v, want zero facts and exact %v",
						result.facts, result.err, testCase.contextErr)
				}
				assertReadOnlyAndClosedAtMostOnce(t, result.state)
			case <-time.After(time.Second):
				t.Fatalf("R30 %s cancellation did not join probe goroutines", testCase.stage)
			}
		})
	}

	success := runProbe(context.Background(), probeConfig{})
	if success.err != nil || success.facts.RootRevision == "" || success.facts.TargetRevision == "" {
		t.Fatalf("R30 success facts=%+v error=%v", success.facts, success.err)
	}
	assertReadOnlyAndClosedAtMostOnce(t, success.state)
	for name, value := range map[string]any{
		"public permit": publicPermit, "sealed permit": permit,
		"request": request, "facts": success.facts,
	} {
		encoded, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			t.Fatalf("marshal R30 %s: %v", name, marshalErr)
		}
		for _, forbidden := range []string{
			privateRoot, privatePath, privateHost, privateUser, privateCredential,
			privateUID, privateGID, privateCommand, privateStat,
		} {
			if strings.Contains(string(encoded), forbidden) {
				t.Fatalf("R30 %s JSON leaked %q: %s", name, forbidden, encoded)
			}
		}
	}

	preflightNow := time.Now().UTC().Truncate(time.Second)
	input, targetFacts, externalFacts := validTargetPreflightInput(t, preflightNow)
	for _, testCase := range []struct {
		name    string
		observe recoveryPreflightExternalEvidenceFuncForR30
	}{
		{
			name: "missing private proof",
			observe: func(_ context.Context, _ PreflightExternalEvidenceRequest) (PreflightExternalEvidence, error) {
				return externalFacts, nil
			},
		},
		{
			name: "request substitution",
			observe: func(_ context.Context, externalRequest PreflightExternalEvidenceRequest) (PreflightExternalEvidence, error) {
				externalRequest.RequiredBytes++
				return issuePreflightExternalEvidenceForTest(externalRequest, externalFacts), nil
			},
		},
		{
			name: "result substitution",
			observe: func(_ context.Context, externalRequest PreflightExternalEvidenceRequest) (PreflightExternalEvidence, error) {
				issued := issuePreflightExternalEvidenceForTest(externalRequest, externalFacts)
				issued.ReservedInodes++
				return issued, nil
			},
		},
	} {
		t.Run("external evidence "+testCase.name, func(t *testing.T) {
			target := &readOnlyPreflightTargetFake{facts: targetFacts}
			evaluator, evaluatorErr := NewTargetPreflightEvaluator(target, testCase.observe)
			if evaluatorErr != nil {
				t.Fatalf("construct R30 evaluator: %v", evaluatorErr)
			}
			result, evaluateErr := evaluator.Evaluate(context.Background(), preflightNow, input)
			observedErrors = append(observedErrors, evaluateErr)
			if !errors.Is(evaluateErr, ErrInvalidTargetPreflight) ||
				!reflect.DeepEqual(result, TargetPreflightResult{}) {
				t.Fatalf("R30 external evidence result=%#v error=%v, want no reason/snapshot output",
					result, evaluateErr)
			}
			if target.probeCalls != 1 {
				t.Fatalf("R30 external evidence target probe calls=%d, want one", target.probeCalls)
			}
		})
	}

	privacyCorpus := capturedLogs.String()
	for _, observedErr := range observedErrors {
		privacyCorpus += "\n" + fmt.Sprint(observedErr)
	}
	for _, forbidden := range []string{
		privateRoot, privatePath, privateHost, privateUser, privateCredential,
		privateUID, privateGID, privateCommand, privateStat,
		"FAKE_PRIVATE_RESOLVER_R30", "FAKE_PRIVATE_DIAL_R30",
		"FAKE_PRIVATE_SFTP_OPEN_R30", "FAKE_PRIVATE_SFTP_CLOSE_R30",
		"FAKE_PRIVATE_SSH_CLOSE_R30", "FAKE_PRIVATE_RESOLVER_CANCEL_R30",
		"FAKE_PRIVATE_STAT_CANCEL_R30",
		"FAKE_PRIVATE_CLOSE_DEADLINE_R30",
	} {
		if strings.Contains(privacyCorpus, forbidden) {
			t.Fatalf("R30 errors/logs leaked private token %q: %s", forbidden, privacyCorpus)
		}
	}
}

func recoveryVerifyAuthorityForTest(
	t *testing.T,
	now time.Time,
	binding recoveryTargetSessionBinding,
	jobID string,
	mode TargetMode,
	privateRelativeLocator string,
	identityDigest string,
	byteCount int64,
) (TargetVerifyPermit, TargetObjectRef, TargetVerifyExpectation) {
	return recoveryVerifyAuthorityForOperationForTest(
		t, now, binding, jobID, mode, privateRelativeLocator,
		identityDigest, byteCount, RecoveryOperationOverwrite,
		ExpectedTargetIdentity{Kind: ExpectedTargetPresent, Digest: identityDigest},
	)
}

func recoveryDeleteVerifyAuthorityForTest(
	t *testing.T,
	now time.Time,
	binding recoveryTargetSessionBinding,
	jobID string,
	mode TargetMode,
	privateRelativeLocator string,
	identityDigest string,
	byteCount int64,
) (TargetVerifyPermit, TargetObjectRef, TargetVerifyExpectation) {
	return recoveryVerifyAuthorityForOperationForTest(
		t, now, binding, jobID, mode, privateRelativeLocator,
		identityDigest, byteCount, RecoveryOperationDelete,
		ExpectedTargetIdentity{Kind: ExpectedTargetPresent, Digest: identityDigest},
	)
}

func recoveryVerifyAuthorityForOperationForTest(
	t *testing.T,
	now time.Time,
	binding recoveryTargetSessionBinding,
	jobID string,
	mode TargetMode,
	privateRelativeLocator string,
	identityDigest string,
	byteCount int64,
	operation RecoveryOperationKind,
	expectedPrior ExpectedTargetIdentity,
) (TargetVerifyPermit, TargetObjectRef, TargetVerifyExpectation) {
	t.Helper()
	object := TargetObjectRef{
		RootID: binding.RootID, RootLocatorDigest: binding.RootLocatorDigest,
		PrivateRelativeLocator: privateRelativeLocator,
	}
	object.TargetPathDigest = mustTargetPathDigest(
		t, object.RootID, object.RootLocatorDigest, object.PrivateRelativeLocator,
	)
	raw := TargetObservationPermit{
		SchemaVersion: 1, NodeID: binding.NodeID, Purpose: TargetPurposeVerify,
		RootID: object.RootID, RootLocatorDigest: object.RootLocatorDigest,
		TargetPathDigest: object.TargetPathDigest, RootRevision: binding.RootRevision,
		ExpiresAt: now.Add(time.Minute),
	}
	permit, err := NewTargetVerifyPermit(
		issueTargetVerifyPermit(raw, binding, jobID, mode, operation, expectedPrior), now,
	)
	if err != nil {
		t.Fatalf("construct regular-file verify authority: %v", err)
	}
	return permit, object, TargetVerifyExpectation{
		Kind: TargetPresencePresent,
		Present: &PresentExpectation{
			IdentityDigest: identityDigest,
			Bytes:          byteCount,
		},
	}
}

func recoveryItemWriteAuthorityForTest(
	t *testing.T,
	now time.Time,
	binding recoveryTargetSessionBinding,
	jobID string,
	mode TargetMode,
	privateRelativeLocator string,
	operation RecoveryOperationKind,
	expectedPrior ExpectedTargetIdentity,
	payload []byte,
) (TargetWritePermit, TargetWriteAtomicRequest) {
	t.Helper()
	object := TargetObjectRef{
		RootID: binding.RootID, RootLocatorDigest: binding.RootLocatorDigest,
		PrivateRelativeLocator: privateRelativeLocator,
	}
	object.TargetPathDigest = mustTargetPathDigest(
		t, object.RootID, object.RootLocatorDigest, object.PrivateRelativeLocator,
	)
	mutation := issueTargetMutationPermit(TargetMutationPermit{
		SchemaVersion: 1, NodeID: binding.NodeID, Purpose: TargetPurposeWrite,
		RootID: object.RootID, RootLocatorDigest: object.RootLocatorDigest,
		TargetPathDigest: object.TargetPathDigest, RootRevision: binding.RootRevision,
		ExpiresAt: now.Add(time.Minute), UseLatchID: RecoverySchemaUseLatchID,
		JobID: jobID, AttemptID: strings.Repeat("2", 32), NodeLeaseID: strings.Repeat("3", 32),
		AttemptFence: 19, NodeFence: 23, ExpectedTargetRevision: "target-revision-item-write",
	}, func(time.Time) error { return nil }, binding)
	base, err := NewTargetWritePermit(mutation, now)
	if err != nil {
		t.Fatalf("construct base item write authority: %v", err)
	}
	digest := sha256.Sum256(payload)
	expectedPriorBytes := int64(-1)
	if expectedPrior.Kind == ExpectedTargetPresent {
		expectedPriorBytes = 0
	}
	permit := issueTargetItemWritePermit(base, targetItemWritePermitProof{
		sessionBinding: binding,
		jobID:          jobID,
		jobItemID:      strings.Repeat("6", 32),
		operationDigest: framedDigest(
			"xirang/recovery/test-item-write-operation/v1", jobID, privateRelativeLocator, string(operation),
		),
		targetMode:         mode,
		object:             object,
		operation:          operation,
		expectedPrior:      expectedPrior,
		expectedPriorBytes: expectedPriorBytes,
		expectedDigest:     hex.EncodeToString(digest[:]),
		expectedBytes:      int64(len(payload)),
	})
	return permit, TargetWriteAtomicRequest{
		Object: object, ExpectedBytes: int64(len(payload)), ExpectedDigest: hex.EncodeToString(digest[:]),
		Content: bytes.NewReader(payload),
	}
}

func recoveryOverwriteArtifactBindingInputForTest(
	t *testing.T,
	binding recoveryTargetSessionBinding,
	jobID string,
	privateRelativeLocator string,
	payload []byte,
) (backupasset.DomainKeyMaterial, recoveryOverwriteArtifactBindingInput) {
	t.Helper()
	object := TargetObjectRef{
		RootID: binding.RootID, RootLocatorDigest: binding.RootLocatorDigest,
		PrivateRelativeLocator: privateRelativeLocator,
	}
	object.TargetPathDigest = mustTargetPathDigest(
		t, object.RootID, object.RootLocatorDigest, object.PrivateRelativeLocator,
	)
	postDigest := sha256.Sum256(payload)
	material := backupasset.DomainKeyMaterial{
		ID: strings.Repeat("f", 32), Domain: backupasset.KeyDomainRecoveryCleanupOwnership,
		Version: 7, State: backupasset.DomainKeyActive, Key: []byte(strings.Repeat("h", sha256.Size)),
	}
	return material, recoveryOverwriteArtifactBindingInput{
		keyVersion: material.Version,
		planID:     binding.PlanID, planBindingDigest: binding.PlanBindingDigest,
		jobID: jobID, jobItemID: strings.Repeat("6", 32),
		operationDigest: strings.Repeat("7", sha256DigestLength),
		targetMode:      TargetModeInPlace, nodeID: binding.NodeID,
		rootID: binding.RootID, rootLocatorDigest: binding.RootLocatorDigest,
		rootRevision: binding.RootRevision, object: object,
		expectedPrior: ExpectedTargetIdentity{
			Kind: ExpectedTargetPresent, Digest: strings.Repeat("b", sha256DigestLength),
		},
		expectedPriorBytes: 11, expectedPostDigest: hex.EncodeToString(postDigest[:]),
		expectedPostBytes: int64(len(payload)),
	}
}

func cloneTargetWritePermitForTest(permit TargetWritePermit) TargetWritePermit {
	cloned := permit
	if permit.permit.proof != nil {
		proof := *permit.permit.proof
		cloned.permit.proof = &proof
	}
	if permit.itemProof != nil {
		proof := *permit.itemProof
		cloned.itemProof = &proof
	}
	return cloned
}

func recoveryLocalSFTPCallCountForTest(client *recoveryLocalSFTPClient) int {
	if client == nil {
		return 0
	}
	return client.realPathCalls + client.lstatCalls + client.statCalls + client.mkdirCalls +
		client.chmodCalls + client.openCalls + client.openFileCalls + client.renameCalls +
		client.removeCalls + client.closeCalls
}

func recoverySFTPRegularFileObservationRevisionForTest(
	t *testing.T,
	binding recoveryTargetSessionBinding,
	object TargetObjectRef,
	identityDigest string,
	byteCount int64,
) string {
	t.Helper()
	encoded := framedDigest(
		"xirang/recovery/sftp-regular-file-observation/v1",
		strconv.FormatUint(uint64(binding.NodeID), 10), binding.RootID,
		binding.RootLocatorDigest, binding.RootRevision,
		object.PrivateRelativeLocator, "regular", identityDigest,
		strconv.FormatInt(byteCount, 10),
	)
	raw, err := hex.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode expected regular-file observation digest: %v", err)
	}
	return "sftp1:" + base64.RawURLEncoding.EncodeToString(raw)
}

func writeRecoveryVerifyFileForTest(
	t *testing.T,
	root string,
	privateRelativeLocator string,
	payload []byte,
) string {
	t.Helper()
	finalPath := filepath.Join(root, filepath.FromSlash(privateRelativeLocator))
	if err := os.MkdirAll(filepath.Dir(finalPath), 0o700); err != nil {
		t.Fatalf("create regular-file verify parent: %v", err)
	}
	if err := os.WriteFile(finalPath, payload, 0o640); err != nil {
		t.Fatalf("write regular-file verify fixture: %v", err)
	}
	return finalPath
}

type recoverySFTPVerifyCaseForTest struct {
	fixture      *recoveryLocalSFTPTargetFixture
	base         *recoveryLocalSFTPClient
	client       *recoveryScriptedSFTPClient
	decorateFile func(*recoveryScriptedSFTPFile)
	openedFiles  []*recoveryCloseCountingSFTPFile
	jobID        string
	locator      string
	finalPath    string
	payload      []byte
	permit       TargetVerifyPermit
	object       TargetObjectRef
	expectation  TargetVerifyExpectation
}

func newRecoverySFTPVerifyCaseForTest(
	t *testing.T,
	itemLocator string,
	payload []byte,
) *recoverySFTPVerifyCaseForTest {
	t.Helper()
	fixture := newRecoveryLocalSFTPTargetFixture(t)
	fixture.create(t)
	jobID := fixture.writePermit.permit.JobID
	locator := recoveryWorkspaceLocatorDirectory + "/" + jobID + "/" + itemLocator
	finalPath := writeRecoveryVerifyFileForTest(t, fixture.root, locator, payload)
	sum := sha256.Sum256(payload)
	permit, object, expectation := recoveryVerifyAuthorityForTest(
		t, fixture.now, fixture.binding, jobID, TargetModeIsolated,
		locator, hex.EncodeToString(sum[:]), int64(len(payload)),
	)
	result := &recoverySFTPVerifyCaseForTest{
		fixture: fixture, base: &recoveryLocalSFTPClient{}, jobID: jobID,
		locator: locator, finalPath: finalPath, payload: payload,
		permit: permit, object: object, expectation: expectation,
	}
	result.client = &recoveryScriptedSFTPClient{base: result.base}
	result.client.open = func(value string) (recoveryTargetSFTPFile, error) {
		baseFile, err := result.base.Open(value)
		if err != nil {
			return nil, err
		}
		scripted := &recoveryScriptedSFTPFile{base: baseFile}
		if result.decorateFile != nil {
			result.decorateFile(scripted)
		}
		counted := &recoveryCloseCountingSFTPFile{recoveryTargetSFTPFile: scripted}
		result.openedFiles = append(result.openedFiles, counted)
		return counted, nil
	}
	return result
}

func (testCase *recoverySFTPVerifyCaseForTest) target() *recoverySFTPTarget {
	return testCase.fixture.targetWithClient(testCase.client)
}

func (testCase *recoverySFTPVerifyCaseForTest) assertResourcesClosedOnce(t *testing.T) {
	t.Helper()
	if testCase.base.closeCalls != 1 {
		t.Fatalf("SFTP client close calls=%d, want exactly one", testCase.base.closeCalls)
	}
	for index, file := range testCase.openedFiles {
		if file.closeCalls != 1 {
			t.Fatalf("opened file %d close calls=%d, want exactly one", index, file.closeCalls)
		}
	}
}

func recoveryFileInfoDriftForTest(info os.FileInfo, field string) os.FileInfo {
	override := recoveryFileInfoOverride{FileInfo: info}
	switch field {
	case "size":
		value := info.Size() + 1
		override.size = &value
	case "mode":
		value := info.Mode() ^ 0o001
		override.mode = &value
	case "modtime":
		value := info.ModTime().Add(time.Second)
		override.modTime = &value
	}
	return override
}

func TestRecoverySFTPTargetVerifyPresentRegularFile(t *testing.T) {
	t.Run("isolated bounded regular file", func(t *testing.T) {
		fixture := newRecoveryLocalSFTPTargetFixture(t)
		fixture.create(t)
		jobID := fixture.writePermit.permit.JobID
		locator := recoveryWorkspaceLocatorDirectory + "/" + jobID + "/item.bin"
		payload := bytes.Repeat([]byte("bounded-regular-file-payload:"), 4097)
		writeRecoveryVerifyFileForTest(t, fixture.root, locator, payload)
		sum := sha256.Sum256(payload)
		digest := hex.EncodeToString(sum[:])
		permit, object, expectation := recoveryVerifyAuthorityForTest(
			t, fixture.now, fixture.binding, jobID, TargetModeIsolated,
			locator, digest, int64(len(payload)),
		)

		observation, err := fixture.target.Verify(
			context.Background(), permit, object, expectation,
		)
		if err != nil || observation.ValidateAgainst(expectation) != nil {
			t.Fatalf("verify exact isolated regular file: observation=%+v error=%v", observation, err)
		}
		wantRevision := recoverySFTPRegularFileObservationRevisionForTest(
			t, fixture.binding, object, digest, int64(len(payload)),
		)
		if observation.ObservedRevision != wantRevision ||
			!strings.HasPrefix(observation.ObservedRevision, "sftp1:") ||
			len(observation.ObservedRevision) != 49 || sha256Shaped(observation.ObservedRevision) {
			t.Fatalf("observation revision=%q, want exact bounded opaque token %q",
				observation.ObservedRevision, wantRevision)
		}
		client := fixture.clients[len(fixture.clients)-1]
		if client.openCalls != 1 || client.readBytes != len(payload) ||
			client.maxReadRequest <= 0 || client.maxReadRequest > 32*1024 {
			t.Fatalf("bounded verify reads: opens=%d bytes=%d/%d max-request=%d",
				client.openCalls, client.readBytes, len(payload), client.maxReadRequest)
		}
	})

	t.Run("zero byte isolated file", func(t *testing.T) {
		fixture := newRecoveryLocalSFTPTargetFixture(t)
		fixture.create(t)
		jobID := fixture.writePermit.permit.JobID
		locator := recoveryWorkspaceLocatorDirectory + "/" + jobID + "/zero.bin"
		writeRecoveryVerifyFileForTest(t, fixture.root, locator, nil)
		sum := sha256.Sum256(nil)
		permit, object, expectation := recoveryVerifyAuthorityForTest(
			t, fixture.now, fixture.binding, jobID, TargetModeIsolated,
			locator, hex.EncodeToString(sum[:]), 0,
		)
		observation, err := fixture.target.Verify(context.Background(), permit, object, expectation)
		if err != nil || observation.ValidateAgainst(expectation) != nil {
			t.Fatalf("verify zero-byte regular file: observation=%+v error=%v", observation, err)
		}
		client := fixture.clients[len(fixture.clients)-1]
		if client.openCalls != 1 || client.readBytes != 0 || client.maxReadRequest != 1 {
			t.Fatalf("zero-byte verify reads: opens=%d bytes=%d max-request=%d, want 1/0/1",
				client.openCalls, client.readBytes, client.maxReadRequest)
		}
	})

	t.Run("in-place exact object", func(t *testing.T) {
		fixture := newRecoveryLocalSFTPTargetFixture(t)
		jobID := fixture.writePermit.permit.JobID
		locator := "in-place.bin"
		payload := []byte("exact in-place regular-file observation")
		writeRecoveryVerifyFileForTest(t, fixture.root, locator, payload)
		sum := sha256.Sum256(payload)
		permit, object, expectation := recoveryVerifyAuthorityForTest(
			t, fixture.now, fixture.binding, jobID, TargetModeInPlace,
			locator, hex.EncodeToString(sum[:]), int64(len(payload)),
		)
		observation, err := fixture.target.Verify(context.Background(), permit, object, expectation)
		if err != nil || observation.ValidateAgainst(expectation) != nil {
			t.Fatalf("verify exact in-place regular file: observation=%+v error=%v", observation, err)
		}
	})
}

func TestRecoverySFTPTargetVerifyNamespaceAndObservationRevision(t *testing.T) {
	t.Run("observation revision is stable and field-separated", func(t *testing.T) {
		fixture := newRecoveryLocalSFTPTargetFixture(t)
		fixture.create(t)
		jobID := fixture.writePermit.permit.JobID
		locator := recoveryWorkspaceLocatorDirectory + "/" + jobID + "/stable.bin"
		payload := []byte("stable regular-file observation")
		writeRecoveryVerifyFileForTest(t, fixture.root, locator, payload)
		sum := sha256.Sum256(payload)
		digest := hex.EncodeToString(sum[:])
		permit, object, expectation := recoveryVerifyAuthorityForTest(
			t, fixture.now, fixture.binding, jobID, TargetModeIsolated,
			locator, digest, int64(len(payload)),
		)
		first, err := fixture.target.Verify(context.Background(), permit, object, expectation)
		if err != nil {
			t.Fatalf("first stable verify: %v", err)
		}
		second, err := fixture.target.Verify(context.Background(), permit, object, expectation)
		if err != nil {
			t.Fatalf("second stable verify: %v", err)
		}
		want := recoverySFTPRegularFileObservationRevisionForTest(
			t, fixture.binding, object, digest, int64(len(payload)),
		)
		if first.ObservedRevision != want || second.ObservedRevision != want {
			t.Fatalf("stable revisions first=%q second=%q want=%q",
				first.ObservedRevision, second.ObservedRevision, want)
		}

		rootVariant := fixture.binding
		rootVariant.RootID = "root-b"
		pathVariant := object
		pathVariant.PrivateRelativeLocator += "-other"
		variants := map[string]string{
			"root": recoverySFTPRegularFileObservationRevisionForTest(
				t, rootVariant, object, digest, int64(len(payload)),
			),
			"path": recoverySFTPRegularFileObservationRevisionForTest(
				t, fixture.binding, pathVariant, digest, int64(len(payload)),
			),
			"content": recoverySFTPRegularFileObservationRevisionForTest(
				t, fixture.binding, object, strings.Repeat("f", sha256DigestLength), int64(len(payload)),
			),
			"bytes": recoverySFTPRegularFileObservationRevisionForTest(
				t, fixture.binding, object, digest, int64(len(payload))+1,
			),
		}
		for name, revision := range variants {
			if revision == want {
				t.Fatalf("%s substitution did not separate observation revision %q", name, want)
			}
		}
	})

	t.Run("isolated first item rejects marker namespace", func(t *testing.T) {
		fixture := newRecoveryLocalSFTPTargetFixture(t)
		fixture.create(t)
		jobID := fixture.writePermit.permit.JobID
		_, _, markerPath := fixture.paths()
		marker, err := os.ReadFile(markerPath)
		if err != nil {
			t.Fatalf("read workspace marker fixture: %v", err)
		}
		markerSum := sha256.Sum256(marker)
		cases := []struct {
			name    string
			locator string
			digest  string
			bytes   int64
		}{
			{
				name: "final marker", locator: recoveryWorkspaceLocatorDirectory + "/" + jobID + "/" + recoveryWorkspaceMarkerFileName,
				digest: hex.EncodeToString(markerSum[:]), bytes: int64(len(marker)),
			},
			{
				name: "marker temp", locator: recoveryWorkspaceLocatorDirectory + "/" + jobID + "/" + recoveryWorkspaceMarkerTempPrefix + "candidate",
				digest: strings.Repeat("a", sha256DigestLength), bytes: 0,
			},
		}
		for _, testCase := range cases {
			t.Run(testCase.name, func(t *testing.T) {
				permit, object, expectation := recoveryVerifyAuthorityForTest(
					t, fixture.now, fixture.binding, jobID, TargetModeIsolated,
					testCase.locator, testCase.digest, testCase.bytes,
				)
				resolverCalls := fixture.resolver.calls
				_, err := fixture.target.Verify(context.Background(), permit, object, expectation)
				if !errors.Is(err, ErrInvalidTargetPermit) {
					t.Fatalf("isolated marker namespace error=%v, want ErrInvalidTargetPermit", err)
				}
				if fixture.resolver.calls != resolverCalls {
					t.Fatalf("isolated marker namespace opened resolver: calls=%d want=%d",
						fixture.resolver.calls, resolverCalls)
				}
			})
		}
	})

	for _, testCase := range []struct {
		name      string
		chmodJobs bool
		chmodJob  bool
	}{
		{name: "jobs mode", chmodJobs: true},
		{name: "job mode", chmodJob: true},
	} {
		t.Run("isolated rejects wrong "+testCase.name, func(t *testing.T) {
			fixture := newRecoveryLocalSFTPTargetFixture(t)
			fixture.create(t)
			jobsPath, jobPath, _ := fixture.paths()
			jobID := fixture.writePermit.permit.JobID
			locator := recoveryWorkspaceLocatorDirectory + "/" + jobID + "/item.bin"
			payload := []byte("private workspace mode")
			writeRecoveryVerifyFileForTest(t, fixture.root, locator, payload)
			if testCase.chmodJobs {
				if err := os.Chmod(jobsPath, 0o755); err != nil {
					t.Fatalf("chmod jobs fixture: %v", err)
				}
			}
			if testCase.chmodJob {
				if err := os.Chmod(jobPath, 0o755); err != nil {
					t.Fatalf("chmod job fixture: %v", err)
				}
			}
			sum := sha256.Sum256(payload)
			permit, object, expectation := recoveryVerifyAuthorityForTest(
				t, fixture.now, fixture.binding, jobID, TargetModeIsolated,
				locator, hex.EncodeToString(sum[:]), int64(len(payload)),
			)
			_, err := fixture.target.Verify(context.Background(), permit, object, expectation)
			if !errors.Is(err, ErrRecoveryTargetChanged) {
				t.Fatalf("wrong private workspace mode error=%v, want ErrRecoveryTargetChanged", err)
			}
		})
	}

	t.Run("deeper marker-named file remains ordinary", func(t *testing.T) {
		fixture := newRecoveryLocalSFTPTargetFixture(t)
		fixture.create(t)
		jobID := fixture.writePermit.permit.JobID
		locator := recoveryWorkspaceLocatorDirectory + "/" + jobID + "/nested/" + recoveryWorkspaceMarkerFileName
		payload := []byte("ordinary deeper marker-named payload")
		writeRecoveryVerifyFileForTest(t, fixture.root, locator, payload)
		sum := sha256.Sum256(payload)
		permit, object, expectation := recoveryVerifyAuthorityForTest(
			t, fixture.now, fixture.binding, jobID, TargetModeIsolated,
			locator, hex.EncodeToString(sum[:]), int64(len(payload)),
		)
		observation, err := fixture.target.Verify(context.Background(), permit, object, expectation)
		if err != nil || observation.ValidateAgainst(expectation) != nil {
			t.Fatalf("verify deeper marker-named ordinary file: observation=%+v error=%v", observation, err)
		}
	})
}

func TestRecoverySFTPTargetVerifyRejectsPathContentAndStatDrift(t *testing.T) {
	payload := []byte("adversarial regular-file payload")
	assertChanged := func(t *testing.T, testCase *recoverySFTPVerifyCaseForTest) {
		t.Helper()
		_, err := testCase.target().Verify(
			context.Background(), testCase.permit, testCase.object, testCase.expectation,
		)
		if err != ErrRecoveryTargetChanged {
			t.Fatalf("verify drift error=%v, want exact ErrRecoveryTargetChanged", err)
		}
		for _, forbidden := range []string{
			testCase.fixture.root, testCase.locator,
			testCase.fixture.binding.CredentialRevision, "FAKE_RAW_VERIFY_DRIFT_FOR_TEST_ONLY",
		} {
			if strings.Contains(err.Error(), forbidden) {
				t.Fatalf("verify drift error leaked %q: %v", forbidden, err)
			}
		}
		testCase.assertResourcesClosedOnce(t)
	}

	for _, testCase := range []struct {
		name   string
		item   string
		mutate func(*testing.T, *recoverySFTPVerifyCaseForTest)
	}{
		{
			name: "missing final", item: "item.bin",
			mutate: func(t *testing.T, value *recoverySFTPVerifyCaseForTest) {
				if err := os.Remove(value.finalPath); err != nil {
					t.Fatalf("remove final fixture: %v", err)
				}
			},
		},
		{
			name: "parent symlink", item: "nested/item.bin",
			mutate: func(t *testing.T, value *recoverySFTPVerifyCaseForTest) {
				parent := filepath.Dir(value.finalPath)
				if err := os.RemoveAll(parent); err != nil {
					t.Fatalf("remove parent fixture: %v", err)
				}
				realParent := filepath.Join(value.fixture.root, "real-parent")
				if err := os.Mkdir(realParent, 0o700); err != nil {
					t.Fatalf("create real parent fixture: %v", err)
				}
				if err := os.WriteFile(filepath.Join(realParent, "item.bin"), value.payload, 0o640); err != nil {
					t.Fatalf("write real parent fixture: %v", err)
				}
				if err := os.Symlink(realParent, parent); err != nil {
					t.Fatalf("create parent symlink fixture: %v", err)
				}
			},
		},
		{
			name: "final symlink", item: "item.bin",
			mutate: func(t *testing.T, value *recoverySFTPVerifyCaseForTest) {
				other := filepath.Join(value.fixture.root, "other-final.bin")
				if err := os.WriteFile(other, value.payload, 0o640); err != nil {
					t.Fatalf("write symlink target fixture: %v", err)
				}
				if err := os.Remove(value.finalPath); err != nil {
					t.Fatalf("remove final before symlink: %v", err)
				}
				if err := os.Symlink(other, value.finalPath); err != nil {
					t.Fatalf("create final symlink fixture: %v", err)
				}
			},
		},
		{
			name: "final directory", item: "item.bin",
			mutate: func(t *testing.T, value *recoverySFTPVerifyCaseForTest) {
				if err := os.Remove(value.finalPath); err != nil {
					t.Fatalf("remove final before directory: %v", err)
				}
				if err := os.Mkdir(value.finalPath, 0o700); err != nil {
					t.Fatalf("create final directory fixture: %v", err)
				}
			},
		},
		{
			name: "final special file", item: "item.bin",
			mutate: func(t *testing.T, value *recoverySFTPVerifyCaseForTest) {
				if err := os.Remove(value.finalPath); err != nil {
					t.Fatalf("remove final before fifo: %v", err)
				}
				if err := syscall.Mkfifo(value.finalPath, 0o600); err != nil {
					t.Fatalf("create final fifo fixture: %v", err)
				}
			},
		},
		{
			name: "wrong final realpath", item: "item.bin",
			mutate: func(_ *testing.T, value *recoverySFTPVerifyCaseForTest) {
				value.client.realPath = func(pathValue string, _ int) (string, error) {
					if pathValue == value.finalPath {
						return pathValue + "-alias", nil
					}
					return value.base.RealPath(pathValue)
				}
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			value := newRecoverySFTPVerifyCaseForTest(t, testCase.item, payload)
			testCase.mutate(t, value)
			assertChanged(t, value)
		})
	}

	t.Run("declared size mismatch", func(t *testing.T) {
		value := newRecoverySFTPVerifyCaseForTest(t, "item.bin", payload)
		value.expectation.Present.Bytes++
		assertChanged(t, value)
	})

	t.Run("content digest mismatch", func(t *testing.T) {
		value := newRecoverySFTPVerifyCaseForTest(t, "item.bin", payload)
		value.expectation.Present.IdentityDigest = strings.Repeat("f", sha256DigestLength)
		assertChanged(t, value)
	})

	for _, phase := range []string{"opened", "post"} {
		for _, field := range []string{"size", "mode", "modtime"} {
			t.Run(phase+" "+field+" drift", func(t *testing.T) {
				value := newRecoverySFTPVerifyCaseForTest(t, "item.bin", payload)
				if phase == "opened" {
					value.decorateFile = func(file *recoveryScriptedSFTPFile) {
						info, err := file.base.Stat()
						if err != nil {
							t.Fatalf("stat opened drift fixture: %v", err)
						}
						file.stat = func() (os.FileInfo, error) {
							return recoveryFileInfoDriftForTest(info, field), nil
						}
					}
				} else {
					value.client.lstat = func(pathValue string, call int) (os.FileInfo, error) {
						info, err := value.base.Lstat(pathValue)
						if err != nil || pathValue != value.finalPath || call != 2 {
							return info, err
						}
						return recoveryFileInfoDriftForTest(info, field), nil
					}
				}
				assertChanged(t, value)
			})
		}
	}

	t.Run("short read", func(t *testing.T) {
		value := newRecoverySFTPVerifyCaseForTest(t, "item.bin", payload)
		value.decorateFile = func(file *recoveryScriptedSFTPFile) {
			file.read = func(buffer []byte) (int, error) {
				limit := len(buffer) / 2
				if limit == 0 {
					limit = 1
				}
				read, _ := file.base.Read(buffer[:limit])
				return read, io.EOF
			}
		}
		assertChanged(t, value)
	})

	t.Run("extra byte", func(t *testing.T) {
		value := newRecoverySFTPVerifyCaseForTest(t, "item.bin", payload)
		if err := os.WriteFile(value.finalPath, append(append([]byte(nil), payload...), 'x'), 0o640); err != nil {
			t.Fatalf("write extra-byte fixture: %v", err)
		}
		expectedSize := int64(len(payload))
		value.client.lstat = func(pathValue string, _ int) (os.FileInfo, error) {
			info, err := value.base.Lstat(pathValue)
			if err != nil || pathValue != value.finalPath {
				return info, err
			}
			return recoveryFileInfoOverride{FileInfo: info, size: &expectedSize}, nil
		}
		value.decorateFile = func(file *recoveryScriptedSFTPFile) {
			info, err := file.base.Stat()
			if err != nil {
				t.Fatalf("stat extra-byte fixture: %v", err)
			}
			file.stat = func() (os.FileInfo, error) {
				return recoveryFileInfoOverride{FileInfo: info, size: &expectedSize}, nil
			}
		}
		assertChanged(t, value)
	})

	t.Run("zero nil after expected bytes", func(t *testing.T) {
		value := newRecoverySFTPVerifyCaseForTest(t, "item.bin", payload)
		value.decorateFile = func(file *recoveryScriptedSFTPFile) {
			remaining := len(payload)
			file.read = func(buffer []byte) (int, error) {
				if remaining == 0 {
					return 0, nil
				}
				if len(buffer) > remaining {
					buffer = buffer[:remaining]
				}
				read, err := file.base.Read(buffer)
				remaining -= read
				return read, err
			}
		}
		assertChanged(t, value)
	})
}

func TestRecoverySFTPTargetVerifyCancellationAndErrors(t *testing.T) {
	payload := []byte("dependency-error regular-file payload")
	rawText := "FAKE_RAW_VERIFY_DEPENDENCY_FOR_TEST_ONLY"
	rawErr := errors.New(rawText)
	for _, testCase := range []struct {
		name      string
		configure func(*testing.T, *recoverySFTPVerifyCaseForTest)
	}{
		{
			name: "lstat error",
			configure: func(_ *testing.T, value *recoverySFTPVerifyCaseForTest) {
				value.client.lstat = func(pathValue string, _ int) (os.FileInfo, error) {
					if pathValue == value.finalPath {
						return nil, rawErr
					}
					return value.base.Lstat(pathValue)
				}
			},
		},
		{
			name: "open error",
			configure: func(_ *testing.T, value *recoverySFTPVerifyCaseForTest) {
				value.client.open = func(string) (recoveryTargetSFTPFile, error) { return nil, rawErr }
			},
		},
		{
			name: "file stat error",
			configure: func(_ *testing.T, value *recoverySFTPVerifyCaseForTest) {
				value.decorateFile = func(file *recoveryScriptedSFTPFile) {
					file.stat = func() (os.FileInfo, error) { return nil, rawErr }
				}
			},
		},
		{
			name: "read error",
			configure: func(_ *testing.T, value *recoverySFTPVerifyCaseForTest) {
				value.decorateFile = func(file *recoveryScriptedSFTPFile) {
					file.read = func([]byte) (int, error) { return 0, rawErr }
				}
			},
		},
		{
			name: "file close error",
			configure: func(_ *testing.T, value *recoverySFTPVerifyCaseForTest) {
				value.decorateFile = func(file *recoveryScriptedSFTPFile) {
					file.close = func() error {
						_ = file.base.Close()
						return rawErr
					}
				}
			},
		},
		{
			name: "client close error",
			configure: func(_ *testing.T, value *recoverySFTPVerifyCaseForTest) {
				value.client.close = func() error {
					_ = value.base.Close()
					return rawErr
				}
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			value := newRecoverySFTPVerifyCaseForTest(t, "item.bin", payload)
			testCase.configure(t, value)
			_, err := value.target().Verify(
				context.Background(), value.permit, value.object, value.expectation,
			)
			if err != ErrRecoveryTargetUnavailable {
				t.Fatalf("dependency error=%v, want exact ErrRecoveryTargetUnavailable", err)
			}
			for _, forbidden := range []string{
				rawText, value.fixture.root, value.locator,
				value.fixture.binding.CredentialRevision,
			} {
				if strings.Contains(err.Error(), forbidden) {
					t.Fatalf("dependency error leaked %q: %v", forbidden, err)
				}
			}
			value.assertResourcesClosedOnce(t)
		})
	}

	t.Run("caller cancellation during read", func(t *testing.T) {
		value := newRecoverySFTPVerifyCaseForTest(t, "item.bin", payload)
		ctx, cancel := context.WithCancel(context.Background())
		value.decorateFile = func(file *recoveryScriptedSFTPFile) {
			file.read = func([]byte) (int, error) {
				cancel()
				return 0, rawErr
			}
		}
		_, err := value.target().Verify(ctx, value.permit, value.object, value.expectation)
		if err != context.Canceled {
			t.Fatalf("cancellation error=%v, want exact context.Canceled", err)
		}
		if strings.Contains(err.Error(), rawText) || strings.Contains(err.Error(), value.fixture.root) {
			t.Fatalf("cancellation error leaked dependency/root: %v", err)
		}
		value.assertResourcesClosedOnce(t)
	})
}

func TestRecoverySFTPTargetProductionConstructorRequiresDependencies(t *testing.T) {
	if target, err := newRecoverySFTPTarget(nil, nil, nil); target != nil ||
		!errors.Is(err, ErrRecoveryTargetUnavailable) {
		t.Fatalf("nil production constructor target=%v error=%v, want closed unavailable", target, err)
	}
}

type recoveryNodeRevisionSourceFake struct {
	snapshot RecoveryNodeRevisionSnapshot
	err      error
	calls    int
	nodeID   uint
	purpose  TargetPurpose
}

func (source *recoveryNodeRevisionSourceFake) ResolveRecoveryNodeRevisionsTx(
	_ context.Context,
	_ *gorm.DB,
	nodeID uint,
	purpose TargetPurpose,
) (RecoveryNodeRevisionSnapshot, error) {
	source.calls++
	source.nodeID = nodeID
	source.purpose = purpose
	return source.snapshot, source.err
}

func TestRecoveryProductionTargetOwnsPrivateNodeSessionResolution(t *testing.T) {
	db := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptWriteAuthorize).db
	node := model.Node{ID: 42, Name: "target-node", Host: "127.0.0.1", Port: 22, Username: "root", Archived: false}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	revisions := &recoveryNodeRevisionSourceFake{snapshot: RecoveryNodeRevisionSnapshot{
		NodeRevision: "node-revision-1", CredentialRevision: "credential-revision-1",
	}}
	workspaceKeys := recoveryWorkerWorkspaceKeySource{}
	target, err := NewProductionTarget(ProductionTargetDependencies{
		DB: db, Revisions: revisions, Dialer: sshutil.NewNodeDialer(db), WorkspaceKeys: workspaceKeys,
	})
	if err != nil {
		t.Fatal(err)
	}
	concrete, ok := target.(*recoverySFTPTarget)
	if !ok {
		t.Fatalf("production target=%T, want Recovery-owned private target", target)
	}
	resolver, ok := concrete.sessions.resolver.(*productionRecoveryTargetNodeSessionResolver)
	if !ok {
		t.Fatalf("production resolver=%T, want Recovery-owned private resolver", concrete.sessions.resolver)
	}
	resolved, err := resolver.ResolveRecoveryTargetNodeSession(context.Background(), node.ID, TargetPurposeWrite)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Node.ID != node.ID || resolved.NodeRevision != revisions.snapshot.NodeRevision ||
		resolved.CredentialRevision != revisions.snapshot.CredentialRevision ||
		revisions.calls != 1 || revisions.nodeID != node.ID || revisions.purpose != TargetPurposeWrite {
		t.Fatalf("resolved private session=%+v revisions=%+v", resolved, revisions)
	}

	if err := db.Model(&model.Node{}).Where("id = ?", node.ID).Update("archived", true).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.ResolveRecoveryTargetNodeSession(context.Background(), node.ID, TargetPurposeWrite); !errors.Is(err, ErrRecoveryTargetUnavailable) {
		t.Fatalf("archived node resolution error=%v, want unavailable", err)
	}

	for name, dependencies := range map[string]ProductionTargetDependencies{
		"database":       {Revisions: revisions, Dialer: sshutil.NewNodeDialer(db), WorkspaceKeys: workspaceKeys},
		"revisions":      {DB: db, Dialer: sshutil.NewNodeDialer(db), WorkspaceKeys: workspaceKeys},
		"dialer":         {DB: db, Revisions: revisions, WorkspaceKeys: workspaceKeys},
		"workspace keys": {DB: db, Revisions: revisions, Dialer: sshutil.NewNodeDialer(db)},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewProductionTarget(dependencies); !errors.Is(err, ErrRecoveryTargetUnavailable) {
				t.Fatalf("missing %s error=%v, want unavailable", name, err)
			}
		})
	}
}

func TestRecoverySFTPTargetA2aDeferredMethodsOpenNoSession(t *testing.T) {
	binding := recoveryTargetSessionBindingForTest(t)
	now := time.Now().UTC().Truncate(time.Second)
	jobID := strings.Repeat("1", 32)
	object := TargetObjectRef{
		RootID: binding.RootID, RootLocatorDigest: binding.RootLocatorDigest,
		PrivateRelativeLocator: recoveryWorkspaceLocatorDirectory + "/" + jobID + "/item.bin",
	}
	object.TargetPathDigest = mustTargetPathDigest(
		t, object.RootID, object.RootLocatorDigest, object.PrivateRelativeLocator,
	)
	raw := TargetObservationPermit{
		SchemaVersion: 1, NodeID: binding.NodeID, Purpose: TargetPurposeVerify,
		RootID: object.RootID, RootLocatorDigest: object.RootLocatorDigest,
		TargetPathDigest: object.TargetPathDigest, RootRevision: binding.RootRevision,
		ExpiresAt: now.Add(time.Minute),
	}
	absent := TargetVerifyExpectation{Kind: TargetPresenceAbsent, Absent: &AbsentExpectation{}}
	resolver := &recoveryTargetNodeSessionResolverFake{result: recoveryTargetNodeSession{
		Node: model.Node{ID: binding.NodeID}, NodeRevision: binding.NodeRevision,
		CredentialRevision: binding.CredentialRevision,
	}}
	dialer := &recoveryTargetNodeDialerFake{}
	factory := newRecoveryTargetSessionFactoryForTest(
		resolver, dialer,
		func(*ssh.Client) (recoveryTargetSFTPClient, error) {
			return &recoveryTargetSFTPClientFake{}, nil
		},
		func(*ssh.Client) error { return nil },
	)
	target := newRecoverySFTPTargetForTest(factory, newRecoveryWorkspaceMarkerCodec(nil, nil))
	target.now = func() time.Time { return now }
	ctx := context.Background()
	assertUnavailable := func(name string, err error) {
		t.Helper()
		if !errors.Is(err, ErrRecoveryTargetUnavailable) {
			t.Fatalf("%s error = %v, want ErrRecoveryTargetUnavailable", name, err)
		}
	}

	reservedObject := object
	reservedObject.PrivateRelativeLocator = recoveryWorkspaceLocatorDirectory + "/" + jobID + "/" + recoveryWorkspaceMarkerFileName
	reservedObject.TargetPathDigest = mustTargetPathDigest(
		t, reservedObject.RootID, reservedObject.RootLocatorDigest, reservedObject.PrivateRelativeLocator,
	)
	reservedRaw := raw
	reservedRaw.TargetPathDigest = reservedObject.TargetPathDigest
	reservedPermit, err := NewTargetVerifyPermit(
		issueTargetVerifyPermit(
			reservedRaw, binding, jobID, TargetModeIsolated, RecoveryOperationDelete,
			ExpectedTargetIdentity{Kind: ExpectedTargetPresent, Digest: strings.Repeat("a", sha256DigestLength)},
		), now,
	)
	if err != nil {
		t.Fatalf("construct reserved absent verify authority: %v", err)
	}
	_, err = target.Verify(ctx, reservedPermit, reservedObject, absent)
	if err != ErrInvalidTargetPermit {
		t.Fatalf("reserved absent Verify error=%v, want exact ErrInvalidTargetPermit", err)
	}
	_, err = target.Lstat(ctx, TargetVerifyPermit{}, TargetLstatRequest{})
	if err != ErrInvalidTargetPermit {
		t.Fatalf("unsealed Lstat error=%v, want exact ErrInvalidTargetPermit", err)
	}
	assertUnavailable("CreateDirectory", target.CreateDirectory(ctx, TargetWritePermit{}, CreateTargetDirectoryRequest{}))
	_, err = target.WriteAtomic(ctx, TargetWritePermit{}, TargetWriteAtomicRequest{})
	if err != ErrInvalidTargetPermit {
		t.Fatalf("unsealed WriteAtomic error=%v, want exact ErrInvalidTargetPermit", err)
	}
	_, err = target.Delete(ctx, TargetDeletePermit{}, TargetDeleteRequest{})
	if err != ErrInvalidTargetPermit {
		t.Fatalf("unsealed Delete error=%v, want exact ErrInvalidTargetPermit", err)
	}
	reader, err := target.OpenOwnedResult(ctx, TargetResultReadPermit{}, OpenOwnedResultRequest{})
	if err != ErrInvalidTargetPermit {
		t.Fatalf("unsealed OpenOwnedResult error=%v, want exact ErrInvalidTargetPermit", err)
	}
	if reader != nil {
		t.Fatal("unsealed OpenOwnedResult returned a reader")
	}
	_, removeErr := target.RemoveOwnedJobDir(
		ctx, TargetCleanupPermit{}, RemoveOwnedJobDirRequest{},
	)
	assertUnavailable("RemoveOwnedJobDir", removeErr)
	if resolver.calls != 0 || dialer.calls != 0 {
		t.Fatalf("absent/deferred/unsealed methods resolved=%d dialed=%d, want zero sessions", resolver.calls, dialer.calls)
	}
}

func TestRecoverySFTPTargetLstatRequiresExactSealedDeleteAuthority(t *testing.T) {
	fixture := newRecoveryLocalSFTPTargetFixture(t)
	fixture.create(t)
	jobID := fixture.writePermit.permit.JobID
	locator := recoveryWorkspaceLocatorDirectory + "/" + jobID + "/delete-item.bin"
	permit, object, _ := recoveryDeleteVerifyAuthorityForTest(
		t, fixture.now, fixture.binding, jobID, TargetModeIsolated,
		locator, strings.Repeat("a", sha256DigestLength), 1,
	)
	request := TargetLstatRequest{Object: object}

	resolverCalls := fixture.resolver.calls
	dialerCalls := fixture.dialer.calls
	clientCount := len(fixture.clients)
	result, err := fixture.target.Lstat(context.Background(), permit, request)
	wantRevision := recoverySFTPProbeRevisionForTest(
		t, "sftpt1:", "xirang/recovery/sftp-target-observation/v1",
		fixture.binding.RootRevision, locator, "absent",
	)
	if result != (TargetLstatResult{Kind: TargetEntryMissing, TargetRevision: wantRevision}) || err != nil {
		t.Fatalf("sealed Lstat result=%+v error=%v, want exact missing revision=%q", result, err, wantRevision)
	}
	if fixture.resolver.calls != resolverCalls+1 || fixture.dialer.calls != dialerCalls+1 ||
		len(fixture.clients) != clientCount+1 || fixture.clients[len(fixture.clients)-1].closeCalls != 1 {
		t.Fatalf("sealed Lstat session resolver=%d/%d dialer=%d/%d clients=%d/%d close=%d, want one exact closed session",
			fixture.resolver.calls, resolverCalls+1, fixture.dialer.calls, dialerCalls+1,
			len(fixture.clients), clientCount+1, fixture.clients[len(fixture.clients)-1].closeCalls)
	}
	if fixture.resolver.purpose != TargetPurposeVerify || fixture.dialer.purpose != "recovery_verify" ||
		fixture.dialer.audit.CorrelationID != jobID {
		t.Fatalf("sealed Lstat purpose=%q dial-purpose=%q correlation=%q, want exact verify session",
			fixture.resolver.purpose, fixture.dialer.purpose, fixture.dialer.audit.CorrelationID)
	}

	clonePermit := func(source TargetVerifyPermit) TargetVerifyPermit {
		cloned := source
		if source.permit.proof != nil {
			proof := *source.permit.proof
			cloned.permit.proof = &proof
		}
		return cloned
	}
	for _, testCase := range []struct {
		name    string
		permit  TargetVerifyPermit
		request TargetLstatRequest
	}{
		{name: "unsealed", permit: TargetVerifyPermit{}, request: request},
		{name: "object substitution", permit: permit, request: func() TargetLstatRequest {
			replaced := object
			replaced.PrivateRelativeLocator += "-other"
			replaced.TargetPathDigest = mustTargetPathDigest(
				t, replaced.RootID, replaced.RootLocatorDigest, replaced.PrivateRelativeLocator,
			)
			return TargetLstatRequest{Object: replaced}
		}()},
		{name: "public permit substitution", permit: func() TargetVerifyPermit {
			replaced := clonePermit(permit)
			replaced.permit.NodeID++
			return replaced
		}(), request: request},
		{name: "session proof substitution", permit: func() TargetVerifyPermit {
			replaced := clonePermit(permit)
			replaced.permit.proof.sessionBinding.NodeRevision = "node-revision-substituted"
			return replaced
		}(), request: request},
		{name: "expiry substitution", permit: func() TargetVerifyPermit {
			replaced := clonePermit(permit)
			replaced.permit.ExpiresAt = fixture.now.Add(-time.Second)
			return replaced
		}(), request: request},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			beforeResolver := fixture.resolver.calls
			beforeDialer := fixture.dialer.calls
			beforeClients := len(fixture.clients)
			observed, observedErr := fixture.target.Lstat(
				context.Background(), testCase.permit, testCase.request,
			)
			if observed != (TargetLstatResult{}) || observedErr != ErrInvalidTargetPermit {
				t.Fatalf("substituted Lstat result=%+v error=%v, want exact invalid permit", observed, observedErr)
			}
			if fixture.resolver.calls != beforeResolver || fixture.dialer.calls != beforeDialer ||
				len(fixture.clients) != beforeClients {
				t.Fatalf("substituted Lstat opened dependency resolver=%d/%d dialer=%d/%d clients=%d/%d",
					fixture.resolver.calls, beforeResolver, fixture.dialer.calls, beforeDialer,
					len(fixture.clients), beforeClients)
			}
		})
	}

	overwritePermit, _, _ := recoveryVerifyAuthorityForOperationForTest(
		t, fixture.now, fixture.binding, jobID, TargetModeIsolated,
		locator, strings.Repeat("a", sha256DigestLength), 1,
		RecoveryOperationOverwrite,
		ExpectedTargetIdentity{Kind: ExpectedTargetPresent, Digest: strings.Repeat("a", sha256DigestLength)},
	)
	beforeResolver := fixture.resolver.calls
	beforeDialer := fixture.dialer.calls
	beforeClients := len(fixture.clients)
	result, err = fixture.target.Lstat(context.Background(), overwritePermit, request)
	if result != (TargetLstatResult{}) || err != ErrInvalidTargetPermit {
		t.Fatalf("cross-operation Lstat result=%+v error=%v, want exact invalid permit", result, err)
	}
	if fixture.resolver.calls != beforeResolver || fixture.dialer.calls != beforeDialer ||
		len(fixture.clients) != beforeClients {
		t.Fatalf("cross-operation Lstat opened dependency resolver=%d/%d dialer=%d/%d clients=%d/%d",
			fixture.resolver.calls, beforeResolver, fixture.dialer.calls, beforeDialer,
			len(fixture.clients), beforeClients)
	}
}

func recoveryDeleteEntryIdentityForTest(
	t *testing.T,
	rootRevision string,
	privateRelativeLocator string,
	kind TargetEntryKind,
	info os.FileInfo,
	uid uint32,
	gid uint32,
	payloadFact string,
) string {
	t.Helper()
	if info == nil {
		t.Fatal("delete-entry identity fixture requires file info")
	}
	return framedDigest(
		"xirang/recovery/sftp-delete-entry-identity/v1",
		rootRevision, privateRelativeLocator, string(kind), strconv.FormatInt(info.Size(), 10),
		strconv.FormatUint(uint64(info.Mode()), 10), strconv.FormatUint(uint64(uid), 10),
		strconv.FormatUint(uint64(gid), 10), strconv.FormatInt(info.ModTime().Unix(), 10),
		payloadFact,
	)
}

func TestRecoverySFTPTargetLstatPresentIdentityMatrix(t *testing.T) {
	const (
		entryUID = uint32(501)
		entryGID = uint32(502)
	)
	type entryCase struct {
		name        string
		kind        TargetEntryKind
		payloadFact string
		create      func(*testing.T, string) []byte
	}
	regular := func(payload []byte) func(*testing.T, string) []byte {
		return func(t *testing.T, finalPath string) []byte {
			t.Helper()
			if err := os.WriteFile(finalPath, payload, 0o640); err != nil {
				t.Fatalf("write delete-entry regular fixture: %v", err)
			}
			return append([]byte(nil), payload...)
		}
	}
	ordinaryPayload := bytes.Repeat([]byte("delete-entry-bounded-payload:"), 4097)
	ordinarySum := sha256.Sum256(ordinaryPayload)
	zeroSum := sha256.Sum256(nil)
	linkTarget := "private-relative-link-target.bin"
	cases := []entryCase{
		{
			name: "ordinary regular", kind: TargetEntryRegular,
			payloadFact: hex.EncodeToString(ordinarySum[:]), create: regular(ordinaryPayload),
		},
		{
			name: "zero-byte regular", kind: TargetEntryRegular,
			payloadFact: hex.EncodeToString(zeroSum[:]), create: regular(nil),
		},
		{
			name: "directory", kind: TargetEntryDirectory,
			create: func(t *testing.T, finalPath string) []byte {
				t.Helper()
				if err := os.Mkdir(finalPath, 0o750); err != nil {
					t.Fatalf("create delete-entry directory fixture: %v", err)
				}
				return nil
			},
		},
		{
			name: "symlink", kind: TargetEntrySymlink, payloadFact: linkTarget,
			create: func(t *testing.T, finalPath string) []byte {
				t.Helper()
				if err := os.Symlink(linkTarget, finalPath); err != nil {
					t.Fatalf("create delete-entry symlink fixture: %v", err)
				}
				return nil
			},
		},
		{
			name: "special", kind: TargetEntrySpecial,
			create: func(t *testing.T, finalPath string) []byte {
				t.Helper()
				if err := syscall.Mkfifo(finalPath, 0o600); err != nil {
					t.Fatalf("create delete-entry fifo fixture: %v", err)
				}
				return nil
			},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newRecoveryLocalSFTPTargetFixture(t)
			fixture.create(t)
			jobID := fixture.writePermit.permit.JobID
			locator := recoveryWorkspaceLocatorDirectory + "/" + jobID + "/entry"
			finalPath := filepath.Join(fixture.root, filepath.FromSlash(locator))
			payload := testCase.create(t, finalPath)
			info, err := os.Lstat(finalPath)
			if err != nil {
				t.Fatalf("lstat delete-entry fixture: %v", err)
			}
			ownedInfo := recoveryProbeFileInfo{
				name: info.Name(), size: info.Size(), mode: info.Mode(), modTime: info.ModTime(),
				uid: entryUID, gid: entryGID,
			}
			base := &recoveryLocalSFTPClient{}
			client := &recoveryScriptedSFTPClient{base: base}
			client.lstat = func(pathValue string, _ int) (os.FileInfo, error) {
				if pathValue == finalPath {
					return ownedInfo, nil
				}
				return base.Lstat(pathValue)
			}
			permit, object, _ := recoveryDeleteVerifyAuthorityForTest(
				t, fixture.now, fixture.binding, jobID, TargetModeIsolated,
				locator, strings.Repeat("a", sha256DigestLength), int64(len(payload)),
			)

			result, observedErr := fixture.targetWithClient(client).Lstat(
				context.Background(), permit, TargetLstatRequest{Object: object},
			)
			wantIdentity := recoveryDeleteEntryIdentityForTest(
				t, fixture.binding.RootRevision, locator, testCase.kind,
				ownedInfo, entryUID, entryGID, testCase.payloadFact,
			)
			wantRevision := recoverySFTPProbeRevisionForTest(
				t, "sftpt1:", "xirang/recovery/sftp-target-observation/v1",
				fixture.binding.RootRevision, locator, string(testCase.kind),
				strconv.FormatInt(ownedInfo.Size(), 10), strconv.FormatUint(uint64(ownedInfo.Mode()), 10),
				strconv.FormatUint(uint64(entryUID), 10), strconv.FormatUint(uint64(entryGID), 10),
				strconv.FormatInt(ownedInfo.ModTime().Unix(), 10),
			)
			if observedErr != nil || result.Kind != testCase.kind ||
				result.IdentityDigest != wantIdentity || result.TargetRevision != wantRevision {
				t.Fatalf("present Lstat result=%+v error=%v, want kind=%q identity=%q revision=%q",
					result, observedErr, testCase.kind, wantIdentity, wantRevision)
			}
			if !validDigest(result.IdentityDigest) || !strings.HasPrefix(result.TargetRevision, "sftpt1:") ||
				len(result.TargetRevision) != 50 || sha256Shaped(result.TargetRevision) {
				t.Fatalf("present Lstat product has invalid identity/revision: %+v", result)
			}
			if base.mkdirCalls != 0 || base.chmodCalls != 0 || base.openFileCalls != 0 ||
				base.renameCalls != 0 || base.removeCalls != 0 {
				t.Fatalf("present Lstat mutated target: %+v", base)
			}
			if testCase.kind == TargetEntryRegular {
				if base.openCalls != 2 || base.readBytes != 2*len(payload) ||
					base.maxReadRequest <= 0 || base.maxReadRequest > recoveryResultReadChunkBytes {
					t.Fatalf("regular Lstat bounded reads opens=%d bytes=%d/%d max=%d",
						base.openCalls, base.readBytes, 2*len(payload), base.maxReadRequest)
				}
			} else if base.openCalls != 0 {
				t.Fatalf("non-regular Lstat opened payload files: %d", base.openCalls)
			}
		})
	}
}

func TestRecoverySFTPTargetExactAbsenceObservationParity(t *testing.T) {
	fixture := newRecoveryLocalSFTPTargetFixture(t)
	fixture.create(t)
	jobID := fixture.writePermit.permit.JobID
	locator := recoveryWorkspaceLocatorDirectory + "/" + jobID + "/missing-entry"
	permit, object, _ := recoveryDeleteVerifyAuthorityForTest(
		t, fixture.now, fixture.binding, jobID, TargetModeIsolated,
		locator, strings.Repeat("a", sha256DigestLength), 0,
	)
	request := TargetLstatRequest{Object: object}
	wantRevision := recoverySFTPProbeRevisionForTest(
		t, "sftpt1:", "xirang/recovery/sftp-target-observation/v1",
		fixture.binding.RootRevision, locator, "absent",
	)

	newMissingClient := func() (*recoveryLocalSFTPClient, *recoveryScriptedSFTPClient) {
		base := &recoveryLocalSFTPClient{}
		return base, &recoveryScriptedSFTPClient{base: base}
	}
	lstatBase, lstatClient := newMissingClient()
	lstatResult, err := fixture.targetWithClient(lstatClient).Lstat(
		context.Background(), permit, request,
	)
	if err != nil || lstatResult != (TargetLstatResult{
		Kind: TargetEntryMissing, TargetRevision: wantRevision,
	}) {
		t.Fatalf("exact missing Lstat result=%+v error=%v, want empty identity revision=%q",
			lstatResult, err, wantRevision)
	}
	if lstatClient.lstatCalls[filepath.Join(fixture.root, filepath.FromSlash(locator))] != 2 {
		t.Fatalf("exact missing Lstat final observations=%d, want two complete observations",
			lstatClient.lstatCalls[filepath.Join(fixture.root, filepath.FromSlash(locator))])
	}
	if lstatBase.mkdirCalls != 0 || lstatBase.chmodCalls != 0 || lstatBase.openFileCalls != 0 ||
		lstatBase.renameCalls != 0 || lstatBase.removeCalls != 0 {
		t.Fatalf("exact missing Lstat mutated target: %+v", lstatBase)
	}

	verifyBase, verifyClient := newMissingClient()
	absent := TargetVerifyExpectation{Kind: TargetPresenceAbsent, Absent: &AbsentExpectation{}}
	verifyResult, err := fixture.targetWithClient(verifyClient).Verify(
		context.Background(), permit, object, absent,
	)
	wantVerify := TargetVerifyObservation{
		Kind: TargetPresenceAbsent, Absent: &AbsentObservation{Evidence: TargetAbsenceEvidenceExact},
		ObservedRevision: wantRevision,
	}
	if err != nil || !reflect.DeepEqual(verifyResult, wantVerify) || verifyResult.ValidateAgainst(absent) != nil {
		t.Fatalf("exact absent Verify result=%+v error=%v, want %+v", verifyResult, err, wantVerify)
	}
	if verifyClient.lstatCalls[filepath.Join(fixture.root, filepath.FromSlash(locator))] != 2 ||
		verifyBase.mkdirCalls != 0 || verifyBase.chmodCalls != 0 || verifyBase.openFileCalls != 0 ||
		verifyBase.renameCalls != 0 || verifyBase.removeCalls != 0 {
		t.Fatalf("exact absent Verify observations/mutations client=%+v base=%+v", verifyClient, verifyBase)
	}

	for _, testCase := range []struct {
		name       string
		transition string
	}{
		{name: "first missing second present", transition: "missing-present"},
		{name: "first present second missing", transition: "present-missing"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			value := newRecoveryLocalSFTPTargetFixture(t)
			value.create(t)
			jobID := value.writePermit.permit.JobID
			locator := recoveryWorkspaceLocatorDirectory + "/" + jobID + "/drifting-entry"
			finalPath := filepath.Join(value.root, filepath.FromSlash(locator))
			if err := os.Mkdir(finalPath, 0o750); err != nil {
				t.Fatalf("create absence drift directory: %v", err)
			}
			info, err := os.Lstat(finalPath)
			if err != nil {
				t.Fatalf("lstat absence drift directory: %v", err)
			}
			ownedInfo := recoveryProbeFileInfo{
				name: info.Name(), size: info.Size(), mode: info.Mode(), modTime: info.ModTime(), uid: 501, gid: 502,
			}
			base := &recoveryLocalSFTPClient{}
			client := &recoveryScriptedSFTPClient{base: base}
			client.lstat = func(pathValue string, call int) (os.FileInfo, error) {
				if pathValue != finalPath {
					return base.Lstat(pathValue)
				}
				if testCase.transition == "missing-present" && call == 1 {
					return nil, os.ErrNotExist
				}
				if testCase.transition == "present-missing" && call >= 3 {
					return nil, os.ErrNotExist
				}
				return ownedInfo, nil
			}
			permit, object, _ := recoveryDeleteVerifyAuthorityForTest(
				t, value.now, value.binding, jobID, TargetModeIsolated,
				locator, strings.Repeat("a", sha256DigestLength), 0,
			)
			result, observedErr := value.targetWithClient(client).Lstat(
				context.Background(), permit, TargetLstatRequest{Object: object},
			)
			if result != (TargetLstatResult{}) || observedErr != ErrRecoveryTargetChanged {
				t.Fatalf("absence drift result=%+v error=%v, want exact target changed", result, observedErr)
			}
		})
	}

	for _, testCase := range []struct {
		name string
		err  error
	}{
		{name: "permission", err: os.ErrPermission},
		{name: "unsupported", err: errors.New("RAW_UNSUPPORTED_ABSENCE_OBSERVATION")},
		{name: "transport", err: errors.New("RAW_TRANSPORT_ABSENCE_OBSERVATION")},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			base := &recoveryLocalSFTPClient{}
			client := &recoveryScriptedSFTPClient{base: base}
			finalPath := filepath.Join(fixture.root, filepath.FromSlash(locator))
			client.lstat = func(pathValue string, _ int) (os.FileInfo, error) {
				if pathValue == finalPath {
					return nil, testCase.err
				}
				return base.Lstat(pathValue)
			}
			result, observedErr := fixture.targetWithClient(client).Lstat(
				context.Background(), permit, request,
			)
			if result != (TargetLstatResult{}) || observedErr != ErrRecoveryTargetUnavailable ||
				strings.Contains(observedErr.Error(), testCase.err.Error()) {
				t.Fatalf("ambiguous absence result=%+v error=%v, want sanitized unavailable", result, observedErr)
			}
		})
	}
}

func newRecoverySFTPLstatRegularCaseForTest(
	t *testing.T,
	itemLocator string,
	payload []byte,
) *recoverySFTPVerifyCaseForTest {
	t.Helper()
	value := newRecoverySFTPVerifyCaseForTest(t, itemLocator, payload)
	sum := sha256.Sum256(payload)
	permit, _, _ := recoveryDeleteVerifyAuthorityForTest(
		t, value.fixture.now, value.fixture.binding, value.jobID, TargetModeIsolated,
		value.locator, hex.EncodeToString(sum[:]), int64(len(payload)),
	)
	value.permit = permit
	info, err := os.Lstat(value.finalPath)
	if err != nil {
		t.Fatalf("lstat delete-entry regular case: %v", err)
	}
	ownedInfo := recoveryProbeFileInfo{
		name: info.Name(), size: info.Size(), mode: info.Mode(), modTime: info.ModTime(), uid: 501, gid: 502,
	}
	value.client.lstat = func(pathValue string, _ int) (os.FileInfo, error) {
		if pathValue == value.finalPath {
			return ownedInfo, nil
		}
		return value.base.Lstat(pathValue)
	}
	return value
}

func assertRecoveryLstatReadOnlyAndClosedForTest(
	t *testing.T,
	value *recoverySFTPVerifyCaseForTest,
) {
	t.Helper()
	value.assertResourcesClosedOnce(t)
	if value.base.mkdirCalls != 0 || value.base.chmodCalls != 0 ||
		value.base.openFileCalls != 0 || value.base.renameCalls != 0 || value.base.removeCalls != 0 {
		t.Fatalf("delete-entry observation mutated target: %+v", value.base)
	}
}

func TestRecoverySFTPTargetLstatAndAbsenceDriftResourcePrivacyMatrix(t *testing.T) {
	payload := bytes.Repeat([]byte("delete-entry-resource-payload:"), 2049)
	rawText := "RAW_PRIVATE_DELETE_ENTRY_DEPENDENCY_R41"
	rawErr := errors.New(rawText)

	t.Run("permit expires during observation", func(t *testing.T) {
		value := newRecoverySFTPLstatRegularCaseForTest(t, "entry.bin", payload)
		target := value.target()
		nowCalls := 0
		target.now = func() time.Time {
			nowCalls++
			if nowCalls == 1 {
				return value.fixture.now
			}
			return value.permit.permit.ExpiresAt
		}
		result, err := target.Lstat(
			context.Background(), value.permit, TargetLstatRequest{Object: value.object},
		)
		if result != (TargetLstatResult{}) || err != ErrInvalidTargetPermit || nowCalls < 2 {
			t.Fatalf("expired-during-observation result=%+v error=%v now_calls=%d, want invalid after live revalidation",
				result, err, nowCalls)
		}
		assertRecoveryLstatReadOnlyAndClosedForTest(t, value)
	})

	t.Run("metadata drift", func(t *testing.T) {
		value := newRecoverySFTPLstatRegularCaseForTest(t, "entry.bin", payload)
		baseLstat := value.client.lstat
		value.client.lstat = func(pathValue string, call int) (os.FileInfo, error) {
			info, err := baseLstat(pathValue, call)
			if err != nil || pathValue != value.finalPath || call != 3 {
				return info, err
			}
			return recoveryFileInfoDriftForTest(info, "mode"), nil
		}
		result, err := value.target().Lstat(
			context.Background(), value.permit, TargetLstatRequest{Object: value.object},
		)
		if result != (TargetLstatResult{}) || err != ErrRecoveryTargetChanged {
			t.Fatalf("metadata drift result=%+v error=%v, want exact target changed", result, err)
		}
		assertRecoveryLstatReadOnlyAndClosedForTest(t, value)
	})

	t.Run("regular content drift between observations", func(t *testing.T) {
		value := newRecoverySFTPLstatRegularCaseForTest(t, "entry.bin", payload)
		originalInfo, err := os.Stat(value.finalPath)
		if err != nil {
			t.Fatalf("stat regular content drift fixture: %v", err)
		}
		openIndex := 0
		value.decorateFile = func(file *recoveryScriptedSFTPFile) {
			openIndex++
			if openIndex != 1 {
				return
			}
			file.close = func() error {
				if closeErr := file.base.Close(); closeErr != nil {
					return closeErr
				}
				replacement := bytes.Repeat([]byte("x"), len(payload))
				if writeErr := os.WriteFile(value.finalPath, replacement, originalInfo.Mode().Perm()); writeErr != nil {
					return writeErr
				}
				return os.Chtimes(value.finalPath, originalInfo.ModTime(), originalInfo.ModTime())
			}
		}
		result, err := value.target().Lstat(
			context.Background(), value.permit, TargetLstatRequest{Object: value.object},
		)
		if result != (TargetLstatResult{}) || err != ErrRecoveryTargetChanged {
			t.Fatalf("content drift result=%+v error=%v, want exact target changed", result, err)
		}
		assertRecoveryLstatReadOnlyAndClosedForTest(t, value)
	})

	t.Run("symlink target drift", func(t *testing.T) {
		fixture := newRecoveryLocalSFTPTargetFixture(t)
		fixture.create(t)
		jobID := fixture.writePermit.permit.JobID
		locator := recoveryWorkspaceLocatorDirectory + "/" + jobID + "/entry-link"
		finalPath := filepath.Join(fixture.root, filepath.FromSlash(locator))
		if err := os.Symlink("first-link-target", finalPath); err != nil {
			t.Fatalf("create link drift fixture: %v", err)
		}
		info, err := os.Lstat(finalPath)
		if err != nil {
			t.Fatalf("lstat link drift fixture: %v", err)
		}
		ownedInfo := recoveryProbeFileInfo{
			name: info.Name(), size: info.Size(), mode: info.Mode(), modTime: info.ModTime(), uid: 501, gid: 502,
		}
		base := &recoveryLocalSFTPClient{}
		client := &recoveryScriptedSFTPClient{base: base}
		client.lstat = func(pathValue string, _ int) (os.FileInfo, error) {
			if pathValue == finalPath {
				return ownedInfo, nil
			}
			return base.Lstat(pathValue)
		}
		client.readLink = func(_ string, call int) (string, error) {
			if call == 1 {
				return "first-link-target", nil
			}
			return "other-link-target", nil
		}
		permit, object, _ := recoveryDeleteVerifyAuthorityForTest(
			t, fixture.now, fixture.binding, jobID, TargetModeIsolated,
			locator, strings.Repeat("a", sha256DigestLength), 0,
		)
		result, observedErr := fixture.targetWithClient(client).Lstat(
			context.Background(), permit, TargetLstatRequest{Object: object},
		)
		if result != (TargetLstatResult{}) || observedErr != ErrRecoveryTargetChanged ||
			client.readLinkCalls[finalPath] != 2 {
			t.Fatalf("link drift result=%+v error=%v readlinks=%d, want changed after two exact reads",
				result, observedErr, client.readLinkCalls[finalPath])
		}
		if base.mkdirCalls != 0 || base.chmodCalls != 0 || base.openCalls != 0 ||
			base.openFileCalls != 0 || base.renameCalls != 0 || base.removeCalls != 0 || base.closeCalls != 1 {
			t.Fatalf("link drift resource/mutation state: %+v", base)
		}
	})

	t.Run("parent canonical drift", func(t *testing.T) {
		value := newRecoverySFTPLstatRegularCaseForTest(t, "nested/entry.bin", payload)
		parentPath := filepath.Dir(value.finalPath)
		value.client.realPath = func(pathValue string, call int) (string, error) {
			canonical, err := value.base.RealPath(pathValue)
			if err == nil && pathValue == parentPath && call == 2 {
				return canonical + "-alias", nil
			}
			return canonical, err
		}
		result, err := value.target().Lstat(
			context.Background(), value.permit, TargetLstatRequest{Object: value.object},
		)
		if result != (TargetLstatResult{}) || err != ErrRecoveryTargetChanged {
			t.Fatalf("parent drift result=%+v error=%v, want exact target changed", result, err)
		}
		assertRecoveryLstatReadOnlyAndClosedForTest(t, value)
	})

	for _, testCase := range []struct {
		name      string
		configure func(*recoverySFTPVerifyCaseForTest)
	}{
		{name: "final lstat", configure: func(value *recoverySFTPVerifyCaseForTest) {
			baseLstat := value.client.lstat
			value.client.lstat = func(pathValue string, call int) (os.FileInfo, error) {
				if pathValue == value.finalPath {
					return nil, rawErr
				}
				return baseLstat(pathValue, call)
			}
		}},
		{name: "open", configure: func(value *recoverySFTPVerifyCaseForTest) {
			value.client.open = func(string) (recoveryTargetSFTPFile, error) { return nil, rawErr }
		}},
		{name: "file stat", configure: func(value *recoverySFTPVerifyCaseForTest) {
			value.decorateFile = func(file *recoveryScriptedSFTPFile) {
				file.stat = func() (os.FileInfo, error) { return nil, rawErr }
			}
		}},
		{name: "read", configure: func(value *recoverySFTPVerifyCaseForTest) {
			value.decorateFile = func(file *recoveryScriptedSFTPFile) {
				file.read = func([]byte) (int, error) { return 0, rawErr }
			}
		}},
		{name: "file close", configure: func(value *recoverySFTPVerifyCaseForTest) {
			value.decorateFile = func(file *recoveryScriptedSFTPFile) {
				file.close = func() error {
					_ = file.base.Close()
					return rawErr
				}
			}
		}},
		{name: "sftp close", configure: func(value *recoverySFTPVerifyCaseForTest) {
			value.client.close = func() error {
				_ = value.base.Close()
				return rawErr
			}
		}},
	} {
		t.Run(testCase.name+" failure", func(t *testing.T) {
			value := newRecoverySFTPLstatRegularCaseForTest(t, "entry.bin", payload)
			testCase.configure(value)
			result, err := value.target().Lstat(
				context.Background(), value.permit, TargetLstatRequest{Object: value.object},
			)
			if result != (TargetLstatResult{}) || err != ErrRecoveryTargetUnavailable ||
				strings.Contains(err.Error(), rawText) || strings.Contains(err.Error(), value.fixture.root) {
				t.Fatalf("dependency failure result=%+v error=%v, want sanitized unavailable", result, err)
			}
			assertRecoveryLstatReadOnlyAndClosedForTest(t, value)
		})
	}

	t.Run("readlink failure", func(t *testing.T) {
		fixture := newRecoveryLocalSFTPTargetFixture(t)
		fixture.create(t)
		jobID := fixture.writePermit.permit.JobID
		locator := recoveryWorkspaceLocatorDirectory + "/" + jobID + "/entry-link"
		finalPath := filepath.Join(fixture.root, filepath.FromSlash(locator))
		if err := os.Symlink("private-link-target", finalPath); err != nil {
			t.Fatalf("create readlink failure fixture: %v", err)
		}
		info, err := os.Lstat(finalPath)
		if err != nil {
			t.Fatalf("lstat readlink failure fixture: %v", err)
		}
		ownedInfo := recoveryProbeFileInfo{
			name: info.Name(), size: info.Size(), mode: info.Mode(), modTime: info.ModTime(), uid: 501, gid: 502,
		}
		base := &recoveryLocalSFTPClient{}
		client := &recoveryScriptedSFTPClient{base: base}
		client.lstat = func(pathValue string, _ int) (os.FileInfo, error) {
			if pathValue == finalPath {
				return ownedInfo, nil
			}
			return base.Lstat(pathValue)
		}
		client.readLink = func(string, int) (string, error) { return "", rawErr }
		permit, object, _ := recoveryDeleteVerifyAuthorityForTest(
			t, fixture.now, fixture.binding, jobID, TargetModeIsolated,
			locator, strings.Repeat("a", sha256DigestLength), 0,
		)
		result, observedErr := fixture.targetWithClient(client).Lstat(
			context.Background(), permit, TargetLstatRequest{Object: object},
		)
		if result != (TargetLstatResult{}) || observedErr != ErrRecoveryTargetUnavailable ||
			strings.Contains(observedErr.Error(), rawText) {
			t.Fatalf("readlink failure result=%+v error=%v, want sanitized unavailable", result, observedErr)
		}
	})

	t.Run("caller cancellation during read", func(t *testing.T) {
		value := newRecoverySFTPLstatRegularCaseForTest(t, "entry.bin", payload)
		ctx, cancel := context.WithCancel(context.Background())
		value.decorateFile = func(file *recoveryScriptedSFTPFile) {
			file.read = func([]byte) (int, error) {
				cancel()
				return 0, rawErr
			}
		}
		result, err := value.target().Lstat(
			ctx, value.permit, TargetLstatRequest{Object: value.object},
		)
		if result != (TargetLstatResult{}) || err != context.Canceled || strings.Contains(err.Error(), rawText) {
			t.Fatalf("canceled Lstat result=%+v error=%v, want exact context canceled", result, err)
		}
		assertRecoveryLstatReadOnlyAndClosedForTest(t, value)
	})

	t.Run("successful products keep private inputs out of JSON", func(t *testing.T) {
		value := newRecoverySFTPLstatRegularCaseForTest(t, "entry.bin", payload)
		result, err := value.target().Lstat(
			context.Background(), value.permit, TargetLstatRequest{Object: value.object},
		)
		if err != nil {
			t.Fatalf("successful privacy Lstat: %v", err)
		}
		corpus := ""
		for name, product := range map[string]any{
			"permit": value.permit, "request": TargetLstatRequest{Object: value.object}, "result": result,
		} {
			encoded, marshalErr := json.Marshal(product)
			if marshalErr != nil {
				t.Fatalf("marshal privacy %s: %v", name, marshalErr)
			}
			corpus += string(encoded)
		}
		for _, forbidden := range []string{
			value.fixture.root, value.locator, string(payload), value.fixture.binding.CredentialRevision,
		} {
			if strings.Contains(corpus, forbidden) {
				t.Fatalf("delete-entry JSON leaked %q: %s", forbidden, corpus)
			}
		}
		assertRecoveryLstatReadOnlyAndClosedForTest(t, value)
	})
}

func TestRecoverySFTPTargetSessionCancellationClosesAndJoins(t *testing.T) {
	t.Run("cancel during sftp construction", func(t *testing.T) {
		binding := recoveryTargetSessionBindingForTest(t)
		resolver := &recoveryTargetNodeSessionResolverFake{result: recoveryTargetNodeSession{
			Node: model.Node{ID: binding.NodeID}, NodeRevision: binding.NodeRevision,
			CredentialRevision: binding.CredentialRevision,
		}}
		dialer := &recoveryTargetNodeDialerFake{}
		started := make(chan struct{})
		sshClosed := make(chan struct{})
		var closeOnce sync.Once
		factory := newRecoveryTargetSessionFactoryForTest(
			resolver, dialer,
			func(*ssh.Client) (recoveryTargetSFTPClient, error) {
				close(started)
				select {
				case <-sshClosed:
					return nil, errors.New("FAKE_SFTP_OPEN_UNBLOCKED_FOR_TEST_ONLY")
				case <-time.After(time.Second):
					return nil, errors.New("FAKE_SFTP_OPEN_TIMEOUT_FOR_TEST_ONLY")
				}
			},
			func(*ssh.Client) error {
				closeOnce.Do(func() { close(sshClosed) })
				return nil
			},
		)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			_, err := factory.Open(ctx, binding, TargetPurposeWrite, strings.Repeat("1", 32))
			done <- err
		}()
		<-started
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("sftp-construction cancellation error = %v, want context.Canceled", err)
			}
		case <-time.After(250 * time.Millisecond):
			select {
			case err := <-done:
				t.Fatalf("sftp construction did not unblock promptly; eventual error = %v", err)
			case <-time.After(2 * time.Second):
				t.Fatal("sftp construction remained blocked after cancellation")
			}
		}
	})

	t.Run("cancel established session", func(t *testing.T) {
		binding := recoveryTargetSessionBindingForTest(t)
		resolver := &recoveryTargetNodeSessionResolverFake{result: recoveryTargetNodeSession{
			Node: model.Node{ID: binding.NodeID}, NodeRevision: binding.NodeRevision,
			CredentialRevision: binding.CredentialRevision,
		}}
		dialer := &recoveryTargetNodeDialerFake{}
		closeOrder := make([]string, 0, 2)
		allClosed := make(chan struct{})
		sftpClient := &recoveryTargetSFTPClientFake{closeOrder: &closeOrder}
		factory := newRecoveryTargetSessionFactoryForTest(
			resolver, dialer,
			func(*ssh.Client) (recoveryTargetSFTPClient, error) { return sftpClient, nil },
			func(*ssh.Client) error {
				closeOrder = append(closeOrder, "ssh")
				close(allClosed)
				return nil
			},
		)
		ctx, cancel := context.WithCancel(context.Background())
		session, err := factory.Open(ctx, binding, TargetPurposeCleanup, strings.Repeat("1", 32))
		if err != nil {
			t.Fatalf("open established session: %v", err)
		}
		cancel()
		select {
		case <-allClosed:
		case <-time.After(time.Second):
			_ = session.Close()
			t.Fatal("established session was not closed by cancellation")
		}
		if err := session.Close(); err != nil {
			t.Fatalf("join canceled session: %v", err)
		}
		if !reflect.DeepEqual(closeOrder, []string{"sftp", "ssh"}) || sftpClient.closeCalls != 1 {
			t.Fatalf("canceled close order=%v sftp calls=%d, want sftp then ssh once", closeOrder, sftpClient.closeCalls)
		}
	})
}

func TestRecoverySFTPTargetFactorySanitizesFailures(t *testing.T) {
	binding := recoveryTargetSessionBindingForTest(t)
	jobID := strings.Repeat("1", 32)
	for _, testCase := range []struct {
		name     string
		resolver error
		dial     error
		open     error
	}{
		{name: "resolver", resolver: errors.New("FAKE_PRIVATE_RESOLVER_ERROR_FOR_TEST_ONLY")},
		{name: "dial", dial: errors.New("FAKE_PRIVATE_DIAL_ERROR_FOR_TEST_ONLY")},
		{name: "sftp open", open: errors.New("FAKE_PRIVATE_SFTP_ERROR_FOR_TEST_ONLY")},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			resolver := &recoveryTargetNodeSessionResolverFake{result: recoveryTargetNodeSession{
				Node: model.Node{ID: binding.NodeID}, NodeRevision: binding.NodeRevision,
				CredentialRevision: binding.CredentialRevision,
			}, err: testCase.resolver}
			dialer := &recoveryTargetNodeDialerFake{err: testCase.dial}
			sshCloseCalls := 0
			factory := newRecoveryTargetSessionFactoryForTest(
				resolver, dialer,
				func(*ssh.Client) (recoveryTargetSFTPClient, error) {
					if testCase.open != nil {
						return nil, testCase.open
					}
					return &recoveryTargetSFTPClientFake{}, nil
				},
				func(*ssh.Client) error { sshCloseCalls++; return nil },
			)
			_, err := factory.Open(context.Background(), binding, TargetPurposeWrite, jobID)
			if !errors.Is(err, ErrRecoveryTargetUnavailable) ||
				strings.Contains(fmt.Sprint(err), "FAKE_PRIVATE_") {
				t.Fatalf("factory failure error = %v, want sanitized unavailable", err)
			}
			wantClose := 0
			if testCase.open != nil {
				wantClose = 1
			}
			if sshCloseCalls != wantClose {
				t.Fatalf("factory failure ssh close calls = %d, want %d", sshCloseCalls, wantClose)
			}
		})
	}
}

func TestRecoverySFTPTargetCreateOwnedJobDirWritesExactProtocol(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatalf("chmod recovery root: %v", err)
	}
	binding := recoveryTargetSessionBindingForLocatorTest(t, root)
	now := time.Now().UTC().Truncate(time.Second)
	material := recoveryWorkspaceMarkerMaterialForTest(1, strings.Repeat("k", 32))
	randomSource := bytes.NewReader(bytes.Repeat([]byte{0x5a}, 64))
	codec := newRecoveryWorkspaceMarkerCodec(
		&recoveryWorkspaceMarkerKeySourceForTest{
			active: material, versions: map[int]backupasset.DomainKeyMaterial{1: material},
		},
		randomSource,
	)
	writePermit, request, _, _ := recoveryWorkspaceMarkerAuthorityWithSessionForTest(
		t, now, material, binding,
	)
	resolver := &recoveryTargetNodeSessionResolverFake{result: recoveryTargetNodeSession{
		Node: model.Node{ID: binding.NodeID}, NodeRevision: binding.NodeRevision,
		CredentialRevision: binding.CredentialRevision,
	}}
	dialer := &recoveryTargetNodeDialerFake{}
	clients := make([]*recoveryLocalSFTPClient, 0, 2)
	factory := newRecoveryTargetSessionFactoryForTest(
		resolver, dialer,
		func(*ssh.Client) (recoveryTargetSFTPClient, error) {
			client := &recoveryLocalSFTPClient{}
			clients = append(clients, client)
			return client, nil
		},
		func(*ssh.Client) error { return nil },
	)
	target := newRecoverySFTPTargetForTest(factory, codec)

	created, err := target.CreateOwnedJobDir(context.Background(), writePermit, request)
	if err != nil {
		entries := make([]string, 0, 4)
		_ = filepath.WalkDir(root, func(value string, entry os.DirEntry, walkErr error) error {
			if walkErr == nil {
				entries = append(entries, value+":"+entry.Type().String())
			}
			return nil
		})
		t.Fatalf("create exact owned workspace: %v entries=%v", err, entries)
	}
	if created.Object != request.Object || created.MarkerBindingDigest != request.MarkerBindingDigest ||
		!validDigest(created.TargetRevision) {
		t.Fatalf("created owned workspace = %+v, want exact request and observation", created)
	}
	jobPath := filepath.Join(root, recoveryWorkspaceLocatorDirectory, writePermit.permit.JobID)
	markerPath := filepath.Join(jobPath, recoveryWorkspaceMarkerFileName)
	jobInfo, err := os.Stat(jobPath)
	if err != nil || !jobInfo.IsDir() || jobInfo.Mode().Perm() != 0o700 {
		t.Fatalf("owned job directory info=%v error=%v, want directory 0700", jobInfo, err)
	}
	markerInfo, err := os.Lstat(markerPath)
	if err != nil || !markerInfo.Mode().IsRegular() || markerInfo.Mode().Perm() != 0o600 {
		t.Fatalf("workspace marker info=%v error=%v, want regular 0600", markerInfo, err)
	}
	marker, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatalf("read workspace marker: %v", err)
	}
	if err := codec.ValidateForCreate(context.Background(), writePermit, request, marker, now); err != nil {
		t.Fatalf("authenticate created workspace marker: %v", err)
	}
	if randomSource.Len() != 32 {
		t.Fatalf("initial create remaining entropy = %d, want 32", randomSource.Len())
	}
	if len(clients) != 1 {
		t.Fatalf("initial create clients=%d, want one", len(clients))
	}
	if clients[0].syncCalls != 1 || clients[0].renameCalls != 1 {
		t.Fatalf("initial create sync=%d rename=%d, want one mandatory sync and standard rename",
			clients[0].syncCalls, clients[0].renameCalls)
	}
	markerSum := sha256.Sum256(marker)
	wantTempPath := filepath.Join(
		jobPath, recoveryWorkspaceMarkerTempPrefix+hex.EncodeToString(markerSum[:]),
	)
	if len(clients[0].openFilePaths) != 1 || clients[0].openFilePaths[0] != wantTempPath ||
		len(clients[0].openFileFlags) != 1 ||
		clients[0].openFileFlags[0] != (os.O_WRONLY|os.O_CREATE|os.O_EXCL) ||
		len(clients[0].renamePaths) != 1 ||
		clients[0].renamePaths[0] != [2]string{wantTempPath, markerPath} || clients[0].removeCalls != 0 {
		t.Fatalf("marker temp protocol: open=%v flags=%v rename=%v remove=%v",
			clients[0].openFilePaths, clients[0].openFileFlags,
			clients[0].renamePaths, clients[0].removePaths)
	}

	replayed, err := target.CreateOwnedJobDir(context.Background(), writePermit, request)
	if err != nil {
		t.Fatalf("replay exact owned workspace: %v", err)
	}
	if replayed != created || randomSource.Len() != 32 {
		t.Fatalf("replayed workspace=%+v entropy=%d, want stable result and no entropy", replayed, randomSource.Len())
	}
	if len(clients) != 2 {
		t.Fatalf("replay clients=%d, want two", len(clients))
	}
	if clients[1].syncCalls != 0 || clients[1].renameCalls != 0 {
		t.Fatalf("replay sync=%d rename=%d, want observation-only replay",
			clients[1].syncCalls, clients[1].renameCalls)
	}

	t.Run("complete short writes", func(t *testing.T) {
		fixture := newRecoveryLocalSFTPTargetFixture(t)
		base := &recoveryLocalSFTPClient{}
		client := &recoveryScriptedSFTPClient{base: base}
		writeCalls := 0
		client.openFile = func(value string, flag int) (recoveryTargetSFTPFile, error) {
			file, err := base.OpenFile(value, flag)
			if err != nil {
				return nil, err
			}
			return &recoveryScriptedSFTPFile{base: file, write: func(value []byte) (int, error) {
				writeCalls++
				if len(value) > 7 {
					value = value[:7]
				}
				return file.Write(value)
			}}, nil
		}
		owned, err := fixture.targetWithClient(client).CreateOwnedJobDir(
			context.Background(), fixture.writePermit, fixture.createRequest,
		)
		if err != nil {
			t.Fatalf("create through short writes: %v", err)
		}
		if writeCalls < 2 || !validDigest(owned.TargetRevision) || base.syncCalls != 1 {
			t.Fatalf("short write completion calls=%d owned=%+v sync=%d", writeCalls, owned, base.syncCalls)
		}
	})
}

func TestRecoverySFTPTargetCreateOwnedJobDirIsAuthenticatedIdempotent(t *testing.T) {
	t.Run("markerless existing job is never adopted", func(t *testing.T) {
		fixture := newRecoveryLocalSFTPTargetFixture(t)
		jobsPath, jobPath, _ := fixture.paths()
		if err := os.Mkdir(jobsPath, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(jobsPath, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(jobPath, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(jobPath, 0o700); err != nil {
			t.Fatal(err)
		}

		_, err := fixture.target.CreateOwnedJobDir(
			context.Background(), fixture.writePermit, fixture.createRequest,
		)
		if !errors.Is(err, ErrRecoveryTargetChanged) {
			t.Fatalf("markerless adoption error=%v, want ErrRecoveryTargetChanged", err)
		}
		if len(fixture.clients) != 1 {
			t.Fatalf("markerless replay clients=%d, want one", len(fixture.clients))
		}
		client := fixture.clients[0]
		if client.mkdirCalls != 0 || client.chmodCalls != 0 || client.openFileCalls != 0 ||
			client.renameCalls != 0 || client.removeCalls != 0 || client.syncCalls != 0 {
			t.Fatalf("markerless replay mutated target: %+v", client)
		}
	})

	t.Run("mismatched marker is never adopted", func(t *testing.T) {
		fixture := newRecoveryLocalSFTPTargetFixture(t)
		fixture.create(t)
		_, _, markerPath := fixture.paths()
		marker, err := os.ReadFile(markerPath)
		if err != nil {
			t.Fatal(err)
		}
		var document map[string]any
		if err := json.Unmarshal(marker, &document); err != nil {
			t.Fatal(err)
		}
		document["marker_creator_id"] = "FAKE_DIFFERENT_MARKER_CREATOR_FOR_TEST_ONLY"
		mismatched, err := json.Marshal(document)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(markerPath, mismatched, 0o600); err != nil {
			t.Fatal(err)
		}

		_, err = fixture.target.CreateOwnedJobDir(
			context.Background(), fixture.writePermit, fixture.createRequest,
		)
		if !errors.Is(err, ErrInvalidRecoveryWorkspaceMarker) {
			t.Fatalf("mismatched marker replay error=%v, want ErrInvalidRecoveryWorkspaceMarker", err)
		}
		if len(fixture.clients) != 2 {
			t.Fatalf("mismatched replay clients=%d, want create plus replay", len(fixture.clients))
		}
		client := fixture.clients[1]
		if client.mkdirCalls != 0 || client.chmodCalls != 0 || client.openFileCalls != 0 ||
			client.renameCalls != 0 || client.removeCalls != 0 || client.syncCalls != 0 {
			t.Fatalf("mismatched replay mutated target: %+v", client)
		}
	})

	t.Run("lost job create race accepts only exact authenticated winner", func(t *testing.T) {
		fixture := newRecoveryLocalSFTPTargetFixture(t)
		winnerCodec := newRecoveryWorkspaceMarkerCodec(
			&recoveryWorkspaceMarkerKeySourceForTest{
				active:   fixture.material,
				versions: map[int]backupasset.DomainKeyMaterial{1: fixture.material},
			},
			bytes.NewReader(bytes.Repeat([]byte{0x5a}, 32)),
		)
		winnerMarker, err := winnerCodec.EncodeForCreate(
			context.Background(), fixture.writePermit, fixture.createRequest, fixture.now,
		)
		if err != nil {
			t.Fatalf("encode race winner marker: %v", err)
		}
		_, jobPath, markerPath := fixture.paths()
		base := &recoveryLocalSFTPClient{}
		client := &recoveryScriptedSFTPClient{base: base}
		client.mkdir = func(value string) error {
			if value != jobPath {
				return base.Mkdir(value)
			}
			if err := os.Mkdir(value, 0o700); err != nil {
				t.Fatalf("create race-winning job: %v", err)
			}
			if err := os.Chmod(value, 0o700); err != nil {
				t.Fatalf("chmod race-winning job: %v", err)
			}
			if err := os.WriteFile(markerPath, winnerMarker, 0o600); err != nil {
				t.Fatalf("write race-winning marker: %v", err)
			}
			if err := os.Chmod(markerPath, 0o600); err != nil {
				t.Fatalf("chmod race-winning marker: %v", err)
			}
			return os.ErrExist
		}

		owned, err := fixture.targetWithClient(client).CreateOwnedJobDir(
			context.Background(), fixture.writePermit, fixture.createRequest,
		)
		if err != nil {
			t.Fatalf("adopt exact authenticated race winner: %v", err)
		}
		if owned.Object != fixture.createRequest.Object ||
			owned.MarkerBindingDigest != fixture.createRequest.MarkerBindingDigest ||
			owned.TargetRevision != recoveryOwnedWorkspaceObservationRevision(
				fixture.binding, fixture.createRequest.Object.PrivateRelativeLocator, winnerMarker,
			) {
			t.Fatalf("race winner observation=%+v", owned)
		}
		if base.openFileCalls != 0 || base.renameCalls != 0 || base.removeCalls != 0 || base.syncCalls != 0 {
			t.Fatalf("race winner adoption used temp mutation: %+v", base)
		}
	})
}

func TestRecoverySFTPTargetCreateOwnedJobDirRejectsPathAndModeDrift(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatalf("chmod recovery root: %v", err)
	}
	jobsPath := filepath.Join(root, recoveryWorkspaceLocatorDirectory)
	if err := os.Mkdir(jobsPath, 0o755); err != nil {
		t.Fatalf("create wrong-mode jobs directory: %v", err)
	}
	if err := os.Chmod(jobsPath, 0o755); err != nil {
		t.Fatalf("preserve wrong-mode jobs directory: %v", err)
	}
	binding := recoveryTargetSessionBindingForLocatorTest(t, root)
	now := time.Now().UTC().Truncate(time.Second)
	material := recoveryWorkspaceMarkerMaterialForTest(1, strings.Repeat("k", 32))
	randomSource := bytes.NewReader(bytes.Repeat([]byte{0x5a}, 32))
	codec := newRecoveryWorkspaceMarkerCodec(
		&recoveryWorkspaceMarkerKeySourceForTest{
			active: material, versions: map[int]backupasset.DomainKeyMaterial{1: material},
		},
		randomSource,
	)
	writePermit, request, _, _ := recoveryWorkspaceMarkerAuthorityWithSessionForTest(t, now, material, binding)
	resolver := &recoveryTargetNodeSessionResolverFake{result: recoveryTargetNodeSession{
		Node: model.Node{ID: binding.NodeID}, NodeRevision: binding.NodeRevision,
		CredentialRevision: binding.CredentialRevision,
	}}
	target := newRecoverySFTPTargetForTest(
		newRecoveryTargetSessionFactoryForTest(
			resolver, &recoveryTargetNodeDialerFake{},
			func(*ssh.Client) (recoveryTargetSFTPClient, error) { return &recoveryLocalSFTPClient{}, nil },
			func(*ssh.Client) error { return nil },
		),
		codec,
	)

	if _, err := target.CreateOwnedJobDir(context.Background(), writePermit, request); !errors.Is(err, ErrRecoveryTargetChanged) {
		t.Fatalf("wrong-mode jobs error = %v, want ErrRecoveryTargetChanged", err)
	}
	if randomSource.Len() != 32 {
		t.Fatalf("wrong-mode jobs consumed entropy: remaining=%d want=32", randomSource.Len())
	}
	if info, err := os.Stat(jobsPath); err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("wrong-mode jobs was repaired: info=%v error=%v", info, err)
	}

	for _, test := range []struct {
		name        string
		createFirst bool
		mutate      func(*testing.T, *recoveryLocalSFTPTargetFixture)
	}{
		{
			name: "root canonical alias",
			mutate: func(t *testing.T, fixture *recoveryLocalSFTPTargetFixture) {
				realRoot := fixture.root + "-real"
				if err := os.Rename(fixture.root, realRoot); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Base(realRoot), fixture.root); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "root replaced by file",
			mutate: func(t *testing.T, fixture *recoveryLocalSFTPTargetFixture) {
				if err := os.Remove(fixture.root); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(fixture.root, []byte("replacement"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "jobs canonical alias",
			mutate: func(t *testing.T, fixture *recoveryLocalSFTPTargetFixture) {
				jobsPath, _, _ := fixture.paths()
				realJobs := jobsPath + "-real"
				if err := os.Mkdir(realJobs, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(realJobs, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Base(realJobs), jobsPath); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "jobs replaced by file",
			mutate: func(t *testing.T, fixture *recoveryLocalSFTPTargetFixture) {
				jobsPath, _, _ := fixture.paths()
				if err := os.WriteFile(jobsPath, []byte("replacement"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "job wrong mode",
			mutate: func(t *testing.T, fixture *recoveryLocalSFTPTargetFixture) {
				jobsPath, jobPath, _ := fixture.paths()
				if err := os.Mkdir(jobsPath, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(jobsPath, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(jobPath, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(jobPath, 0o755); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "job canonical alias",
			mutate: func(t *testing.T, fixture *recoveryLocalSFTPTargetFixture) {
				jobsPath, jobPath, _ := fixture.paths()
				if err := os.Mkdir(jobsPath, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(jobsPath, 0o700); err != nil {
					t.Fatal(err)
				}
				realJob := jobPath + "-real"
				if err := os.Mkdir(realJob, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(realJob, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Base(realJob), jobPath); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "final marker wrong mode", createFirst: true,
			mutate: func(t *testing.T, fixture *recoveryLocalSFTPTargetFixture) {
				_, _, markerPath := fixture.paths()
				if err := os.Chmod(markerPath, 0o644); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "final marker canonical alias", createFirst: true,
			mutate: func(t *testing.T, fixture *recoveryLocalSFTPTargetFixture) {
				_, _, markerPath := fixture.paths()
				realMarker := markerPath + "-real"
				if err := os.Rename(markerPath, realMarker); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Base(realMarker), markerPath); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRecoveryLocalSFTPTargetFixture(t)
			if test.createFirst {
				fixture.create(t)
			}
			test.mutate(t, fixture)
			_, err := fixture.target.CreateOwnedJobDir(
				context.Background(), fixture.writePermit, fixture.createRequest,
			)
			if !errors.Is(err, ErrRecoveryTargetChanged) {
				t.Fatalf("create path drift error=%v, want ErrRecoveryTargetChanged", err)
			}
			client := fixture.clients[len(fixture.clients)-1]
			if client.mkdirCalls != 0 || client.chmodCalls != 0 || client.openFileCalls != 0 ||
				client.renameCalls != 0 || client.removeCalls != 0 || client.syncCalls != 0 {
				t.Fatalf("create drift repaired or mutated target: %+v", client)
			}
		})
	}

	t.Run("temp wrong mode is rejected before marker write", func(t *testing.T) {
		fixture := newRecoveryLocalSFTPTargetFixture(t)
		base := &recoveryLocalSFTPClient{}
		client := &recoveryScriptedSFTPClient{base: base}
		markerWriteCalled := false
		client.openFile = func(value string, flag int) (recoveryTargetSFTPFile, error) {
			file, err := base.OpenFile(value, flag)
			if err != nil {
				return nil, err
			}
			return &recoveryScriptedSFTPFile{base: file, write: func(value []byte) (int, error) {
				markerWriteCalled = true
				return file.Write(value)
			}}, nil
		}
		client.chmod = func(value string, mode os.FileMode) error {
			if strings.HasPrefix(filepath.Base(value), recoveryWorkspaceMarkerTempPrefix) {
				return os.Chmod(value, 0o644)
			}
			return base.Chmod(value, mode)
		}

		_, err := fixture.targetWithClient(client).CreateOwnedJobDir(
			context.Background(), fixture.writePermit, fixture.createRequest,
		)
		if !errors.Is(err, ErrRecoveryTargetChanged) {
			t.Fatalf("wrong-mode temp error=%v, want ErrRecoveryTargetChanged", err)
		}
		if markerWriteCalled {
			t.Fatal("wrong-mode temp received marker bytes before mode verification")
		}
		if base.removeCalls != 1 || len(base.removePaths) != 1 ||
			!strings.HasPrefix(filepath.Base(base.removePaths[0]), recoveryWorkspaceMarkerTempPrefix) {
			t.Fatalf("wrong-mode temp cleanup=%v, want only exact owned temp", base.removePaths)
		}
	})
}

func TestRecoverySFTPTargetCreateOwnedJobDirFailureMatrix(t *testing.T) {
	rawFailure := errors.New("RAW_SFTP_STAGE_FAILURE_FOR_TEST_ONLY")
	conflictMarker := []byte("FAKE_RENAME_CONFLICT_MARKER_FOR_TEST_ONLY")
	tests := []struct {
		name       string
		want       error
		finalState string
		configure  func(*testing.T, *recoveryLocalSFTPTargetFixture, *recoveryScriptedSFTPClient, *recoveryLocalSFTPClient)
	}{
		{
			name: "mkdir jobs", want: ErrRecoveryTargetUnavailable, finalState: "absent",
			configure: func(_ *testing.T, fixture *recoveryLocalSFTPTargetFixture, client *recoveryScriptedSFTPClient, base *recoveryLocalSFTPClient) {
				jobsPath, _, _ := fixture.paths()
				client.mkdir = func(value string) error {
					if value == jobsPath {
						return rawFailure
					}
					return base.Mkdir(value)
				}
			},
		},
		{
			name: "chmod jobs", want: ErrRecoveryTargetUnavailable, finalState: "absent",
			configure: func(_ *testing.T, fixture *recoveryLocalSFTPTargetFixture, client *recoveryScriptedSFTPClient, base *recoveryLocalSFTPClient) {
				jobsPath, _, _ := fixture.paths()
				client.chmod = func(value string, mode os.FileMode) error {
					if value == jobsPath {
						return rawFailure
					}
					return base.Chmod(value, mode)
				}
			},
		},
		{
			name: "mkdir job", want: ErrRecoveryTargetUnavailable, finalState: "absent",
			configure: func(_ *testing.T, fixture *recoveryLocalSFTPTargetFixture, client *recoveryScriptedSFTPClient, base *recoveryLocalSFTPClient) {
				_, jobPath, _ := fixture.paths()
				client.mkdir = func(value string) error {
					if value == jobPath {
						return rawFailure
					}
					return base.Mkdir(value)
				}
			},
		},
		{
			name: "chmod job", want: ErrRecoveryTargetUnavailable, finalState: "absent",
			configure: func(_ *testing.T, fixture *recoveryLocalSFTPTargetFixture, client *recoveryScriptedSFTPClient, base *recoveryLocalSFTPClient) {
				_, jobPath, _ := fixture.paths()
				client.chmod = func(value string, mode os.FileMode) error {
					if value == jobPath {
						return rawFailure
					}
					return base.Chmod(value, mode)
				}
			},
		},
		{
			name: "exclusive temp open", want: ErrRecoveryTargetUnavailable, finalState: "absent",
			configure: func(_ *testing.T, _ *recoveryLocalSFTPTargetFixture, client *recoveryScriptedSFTPClient, _ *recoveryLocalSFTPClient) {
				client.openFile = func(string, int) (recoveryTargetSFTPFile, error) { return nil, rawFailure }
			},
		},
		{
			name: "zero write", want: ErrRecoveryTargetUnavailable, finalState: "absent",
			configure: func(t *testing.T, _ *recoveryLocalSFTPTargetFixture, client *recoveryScriptedSFTPClient, base *recoveryLocalSFTPClient) {
				client.openFile = func(value string, flag int) (recoveryTargetSFTPFile, error) {
					file, err := base.OpenFile(value, flag)
					if err != nil {
						t.Fatalf("open temp before zero write: %v", err)
					}
					return &recoveryScriptedSFTPFile{base: file, write: func([]byte) (int, error) { return 0, nil }}, nil
				}
			},
		},
		{
			name: "partial write error", want: ErrRecoveryTargetUnavailable, finalState: "absent",
			configure: func(t *testing.T, _ *recoveryLocalSFTPTargetFixture, client *recoveryScriptedSFTPClient, base *recoveryLocalSFTPClient) {
				client.openFile = func(value string, flag int) (recoveryTargetSFTPFile, error) {
					file, err := base.OpenFile(value, flag)
					if err != nil {
						t.Fatalf("open temp before partial write error: %v", err)
					}
					return &recoveryScriptedSFTPFile{base: file, write: func(value []byte) (int, error) {
						if len(value) > 7 {
							value = value[:7]
						}
						written, _ := file.Write(value)
						return written, rawFailure
					}}, nil
				}
			},
		},
		{
			name: "sync", want: ErrRecoveryTargetUnavailable, finalState: "absent",
			configure: func(t *testing.T, _ *recoveryLocalSFTPTargetFixture, client *recoveryScriptedSFTPClient, base *recoveryLocalSFTPClient) {
				client.openFile = func(value string, flag int) (recoveryTargetSFTPFile, error) {
					file, err := base.OpenFile(value, flag)
					if err != nil {
						t.Fatalf("open temp before sync failure: %v", err)
					}
					return &recoveryScriptedSFTPFile{base: file, sync: func() error { return rawFailure }}, nil
				}
			},
		},
		{
			name: "temp close", want: ErrRecoveryTargetUnavailable, finalState: "absent",
			configure: func(t *testing.T, _ *recoveryLocalSFTPTargetFixture, client *recoveryScriptedSFTPClient, base *recoveryLocalSFTPClient) {
				client.openFile = func(value string, flag int) (recoveryTargetSFTPFile, error) {
					file, err := base.OpenFile(value, flag)
					if err != nil {
						t.Fatalf("open temp before close failure: %v", err)
					}
					return &recoveryScriptedSFTPFile{base: file, close: func() error {
						_ = file.Close()
						return rawFailure
					}}, nil
				}
			},
		},
		{
			name: "temp reopen", want: ErrRecoveryTargetUnavailable, finalState: "absent",
			configure: func(_ *testing.T, _ *recoveryLocalSFTPTargetFixture, client *recoveryScriptedSFTPClient, base *recoveryLocalSFTPClient) {
				client.open = func(value string) (recoveryTargetSFTPFile, error) {
					if strings.HasPrefix(filepath.Base(value), recoveryWorkspaceMarkerTempPrefix) {
						return nil, rawFailure
					}
					return base.Open(value)
				}
			},
		},
		{
			name: "temp byte mismatch", want: ErrRecoveryTargetChanged, finalState: "absent",
			configure: func(t *testing.T, _ *recoveryLocalSFTPTargetFixture, client *recoveryScriptedSFTPClient, base *recoveryLocalSFTPClient) {
				client.open = func(value string) (recoveryTargetSFTPFile, error) {
					file, err := base.Open(value)
					if err != nil {
						return nil, err
					}
					if !strings.HasPrefix(filepath.Base(value), recoveryWorkspaceMarkerTempPrefix) {
						return file, nil
					}
					changed := false
					return &recoveryScriptedSFTPFile{base: file, read: func(value []byte) (int, error) {
						read, readErr := file.Read(value)
						if read > 0 && !changed {
							value[0] ^= 0xff
							changed = true
						}
						return read, readErr
					}}, nil
				}
				_ = t
			},
		},
		{
			name: "rename conflict", want: ErrRecoveryTargetUnavailable, finalState: "conflict",
			configure: func(t *testing.T, _ *recoveryLocalSFTPTargetFixture, client *recoveryScriptedSFTPClient, _ *recoveryLocalSFTPClient) {
				client.rename = func(_, newName string) error {
					if err := os.WriteFile(newName, conflictMarker, 0o600); err != nil {
						t.Fatalf("create conflicting final marker: %v", err)
					}
					if err := os.Chmod(newName, 0o600); err != nil {
						t.Fatalf("chmod conflicting final marker: %v", err)
					}
					return os.ErrExist
				}
			},
		},
		{
			name: "ambiguous rename", want: ErrRecoveryTargetUnavailable, finalState: "present",
			configure: func(t *testing.T, _ *recoveryLocalSFTPTargetFixture, client *recoveryScriptedSFTPClient, _ *recoveryLocalSFTPClient) {
				client.rename = func(oldName, newName string) error {
					if err := os.Rename(oldName, newName); err != nil {
						t.Fatalf("perform ambiguous rename: %v", err)
					}
					return rawFailure
				}
			},
		},
		{
			name: "final read", want: ErrRecoveryTargetUnavailable, finalState: "present",
			configure: func(_ *testing.T, fixture *recoveryLocalSFTPTargetFixture, client *recoveryScriptedSFTPClient, base *recoveryLocalSFTPClient) {
				_, _, markerPath := fixture.paths()
				client.open = func(value string) (recoveryTargetSFTPFile, error) {
					if value == markerPath {
						return nil, rawFailure
					}
					return base.Open(value)
				}
			},
		},
		{
			name: "final authentication", want: ErrInvalidRecoveryWorkspaceMarker, finalState: "present",
			configure: func(_ *testing.T, fixture *recoveryLocalSFTPTargetFixture, client *recoveryScriptedSFTPClient, base *recoveryLocalSFTPClient) {
				_, _, markerPath := fixture.paths()
				client.open = func(value string) (recoveryTargetSFTPFile, error) {
					file, err := base.Open(value)
					if err != nil || value != markerPath {
						return file, err
					}
					changed := false
					return &recoveryScriptedSFTPFile{base: file, read: func(value []byte) (int, error) {
						read, readErr := file.Read(value)
						if read > 0 && !changed {
							value[0] = 'x'
							changed = true
						}
						return read, readErr
					}}, nil
				}
			},
		},
		{
			name: "final revalidation", want: ErrRecoveryTargetChanged, finalState: "present",
			configure: func(_ *testing.T, fixture *recoveryLocalSFTPTargetFixture, client *recoveryScriptedSFTPClient, base *recoveryLocalSFTPClient) {
				jobsPath, _, markerPath := fixture.paths()
				finalOpened := false
				client.open = func(value string) (recoveryTargetSFTPFile, error) {
					if value == markerPath {
						finalOpened = true
					}
					return base.Open(value)
				}
				client.lstat = func(value string, _ int) (os.FileInfo, error) {
					if finalOpened && value == jobsPath {
						return nil, os.ErrNotExist
					}
					return base.Lstat(value)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRecoveryLocalSFTPTargetFixture(t)
			base := &recoveryLocalSFTPClient{}
			client := &recoveryScriptedSFTPClient{base: base}
			test.configure(t, fixture, client, base)
			_, err := fixture.targetWithClient(client).CreateOwnedJobDir(
				context.Background(), fixture.writePermit, fixture.createRequest,
			)
			if !errors.Is(err, test.want) {
				t.Fatalf("failure error=%v, want %v", err, test.want)
			}
			if strings.Contains(err.Error(), rawFailure.Error()) {
				t.Fatalf("failure leaked dependency text: %v", err)
			}
			_, jobPath, markerPath := fixture.paths()
			marker, markerErr := os.ReadFile(markerPath)
			switch test.finalState {
			case "absent":
				if !os.IsNotExist(markerErr) {
					t.Fatalf("failed stage left final marker: bytes=%q error=%v", marker, markerErr)
				}
			case "conflict":
				if markerErr != nil || !bytes.Equal(marker, conflictMarker) {
					t.Fatalf("rename conflict overwrote final marker: bytes=%q error=%v", marker, markerErr)
				}
			case "present":
				if markerErr != nil || len(marker) == 0 {
					t.Fatalf("post-rename stage lost final marker: bytes=%q error=%v", marker, markerErr)
				}
			default:
				t.Fatalf("unknown final state %q", test.finalState)
			}
			if base.removeCalls > 1 {
				t.Fatalf("failure removed more than one path: %v", base.removePaths)
			}
			for _, removed := range base.removePaths {
				if filepath.Dir(removed) != jobPath ||
					!strings.HasPrefix(filepath.Base(removed), recoveryWorkspaceMarkerTempPrefix) {
					t.Fatalf("failure removed non-owned temp path %q", removed)
				}
			}
		})
	}

	for _, dependency := range []struct {
		name  string
		codec func(*recoveryLocalSFTPTargetFixture) *recoveryWorkspaceMarkerCodec
	}{
		{
			name: "marker key",
			codec: func(*recoveryLocalSFTPTargetFixture) *recoveryWorkspaceMarkerCodec {
				return newRecoveryWorkspaceMarkerCodec(
					&recoveryWorkspaceMarkerKeySourceForTest{activeErr: rawFailure},
					bytes.NewReader(bytes.Repeat([]byte{0x5a}, 32)),
				)
			},
		},
		{
			name: "marker entropy",
			codec: func(fixture *recoveryLocalSFTPTargetFixture) *recoveryWorkspaceMarkerCodec {
				return newRecoveryWorkspaceMarkerCodec(
					&recoveryWorkspaceMarkerKeySourceForTest{
						active:   fixture.material,
						versions: map[int]backupasset.DomainKeyMaterial{1: fixture.material},
					},
					recoveryWorkspaceMarkerFailingEntropyForTest{err: rawFailure},
				)
			},
		},
	} {
		t.Run(dependency.name, func(t *testing.T) {
			fixture := newRecoveryLocalSFTPTargetFixture(t)
			base := &recoveryLocalSFTPClient{}
			fixture.codec = dependency.codec(fixture)
			_, err := fixture.targetWithClient(base).CreateOwnedJobDir(
				context.Background(), fixture.writePermit, fixture.createRequest,
			)
			if err != ErrRecoveryWorkspaceMarkerUnavailable || strings.Contains(err.Error(), rawFailure.Error()) {
				t.Fatalf("marker dependency error=%v, want sanitized unavailable", err)
			}
			if base.mkdirCalls != 0 || base.chmodCalls != 0 || base.openFileCalls != 0 ||
				base.renameCalls != 0 || base.removeCalls != 0 {
				t.Fatalf("marker dependency failure mutated target: %+v", base)
			}
			_, _, markerPath := fixture.paths()
			if _, statErr := os.Lstat(markerPath); !os.IsNotExist(statErr) {
				t.Fatalf("marker dependency failure left final marker: %v", statErr)
			}
		})
	}
}

func TestRecoverySFTPTargetCreateOwnedJobDirRequiresServerFsync(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatalf("chmod recovery root: %v", err)
	}
	binding := recoveryTargetSessionBindingForLocatorTest(t, root)
	now := time.Now().UTC().Truncate(time.Second)
	material := recoveryWorkspaceMarkerMaterialForTest(1, strings.Repeat("k", 32))
	codec := newRecoveryWorkspaceMarkerCodec(
		&recoveryWorkspaceMarkerKeySourceForTest{
			active: material, versions: map[int]backupasset.DomainKeyMaterial{1: material},
		},
		bytes.NewReader(bytes.Repeat([]byte{0x5a}, 32)),
	)
	writePermit, request, _, _ := recoveryWorkspaceMarkerAuthorityWithSessionForTest(t, now, material, binding)
	resolver := &recoveryTargetNodeSessionResolverFake{result: recoveryTargetNodeSession{
		Node: model.Node{ID: binding.NodeID}, NodeRevision: binding.NodeRevision,
		CredentialRevision: binding.CredentialRevision,
	}}
	target := newRecoverySFTPTargetForTest(
		newRecoveryTargetSessionFactoryForTest(
			resolver, &recoveryTargetNodeDialerFake{}, newRecoveryPipeSFTPOpenerForTest(t),
			func(*ssh.Client) error { return nil },
		),
		codec,
	)

	if _, err := target.CreateOwnedJobDir(context.Background(), writePermit, request); !errors.Is(err, ErrRecoveryTargetUnavailable) {
		t.Fatalf("server without fsync error = %v, want ErrRecoveryTargetUnavailable", err)
	}
	jobPath := filepath.Join(root, recoveryWorkspaceLocatorDirectory, writePermit.permit.JobID)
	if _, err := os.Lstat(filepath.Join(jobPath, recoveryWorkspaceMarkerFileName)); !os.IsNotExist(err) {
		t.Fatalf("server without fsync left final marker: %v", err)
	}
	entries, err := os.ReadDir(jobPath)
	if err != nil {
		t.Fatalf("read markerless job directory: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("server without fsync left temp entries: %v", entries)
	}
}

func TestRecoverySFTPTargetCreateAndValidateReturnSameObservation(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatalf("chmod recovery root: %v", err)
	}
	binding := recoveryTargetSessionBindingForLocatorTest(t, root)
	now := time.Now().UTC().Truncate(time.Second)
	material := recoveryWorkspaceMarkerMaterialForTest(1, strings.Repeat("k", 32))
	codec := newRecoveryWorkspaceMarkerCodec(
		&recoveryWorkspaceMarkerKeySourceForTest{
			active: material, versions: map[int]backupasset.DomainKeyMaterial{1: material},
		},
		bytes.NewReader(bytes.Repeat([]byte{0x5a}, 32)),
	)
	writePermit, createRequest, cleanupPermit, cleanupRequest :=
		recoveryWorkspaceMarkerAuthorityWithSessionForTest(t, now, material, binding)
	resolver := &recoveryTargetNodeSessionResolverFake{result: recoveryTargetNodeSession{
		Node: model.Node{ID: binding.NodeID}, NodeRevision: binding.NodeRevision,
		CredentialRevision: binding.CredentialRevision,
	}}
	clients := make([]*recoveryLocalSFTPClient, 0, 2)
	target := newRecoverySFTPTargetForTest(
		newRecoveryTargetSessionFactoryForTest(
			resolver, &recoveryTargetNodeDialerFake{},
			func(*ssh.Client) (recoveryTargetSFTPClient, error) {
				client := &recoveryLocalSFTPClient{}
				clients = append(clients, client)
				return client, nil
			},
			func(*ssh.Client) error { return nil },
		),
		codec,
	)
	created, err := target.CreateOwnedJobDir(context.Background(), writePermit, createRequest)
	if err != nil {
		t.Fatalf("create workspace before validation: %v", err)
	}
	validated, err := target.ValidateOwnedJobDir(context.Background(), cleanupPermit, cleanupRequest)
	if err != nil {
		t.Fatalf("validate unchanged owned workspace: %v", err)
	}
	if validated.Object != cleanupRequest.Object ||
		validated.MarkerBindingDigest != cleanupRequest.MarkerBindingDigest ||
		validated.RootRevision != binding.RootRevision ||
		validated.TargetRevision != created.TargetRevision {
		t.Fatalf("validated workspace=%+v created=%+v, want identical observation", validated, created)
	}
	if len(clients) != 2 {
		t.Fatalf("validation clients=%d, want two", len(clients))
	}
	if clients[1].syncCalls != 0 || clients[1].renameCalls != 0 {
		t.Fatalf("validation sync=%d rename=%d, want observation only",
			clients[1].syncCalls, clients[1].renameCalls)
	}
}

func TestRecoverySFTPTargetValidateOwnedJobDirReadsWithoutMutation(t *testing.T) {
	fixture := newRecoveryLocalSFTPTargetFixture(t)
	created := fixture.create(t)
	_, _, markerPath := fixture.paths()
	marker, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatalf("read created marker: %v", err)
	}

	validated, err := fixture.target.ValidateOwnedJobDir(
		context.Background(), fixture.cleanupPermit, fixture.cleanupRequest,
	)
	if err != nil {
		t.Fatalf("validate unchanged owned workspace: %v", err)
	}
	if validated.Object != fixture.cleanupRequest.Object ||
		validated.MarkerBindingDigest != fixture.cleanupRequest.MarkerBindingDigest ||
		validated.RootRevision != fixture.binding.RootRevision ||
		validated.TargetRevision != created.TargetRevision {
		t.Fatalf("validation=%+v created=%+v, want exact immutable observation", validated, created)
	}
	if fixture.resolver.purpose != TargetPurposeCleanup {
		t.Fatalf("validation purpose=%q, want %q", fixture.resolver.purpose, TargetPurposeCleanup)
	}
	if len(fixture.clients) != 2 {
		t.Fatalf("opened clients=%d, want create plus validation", len(fixture.clients))
	}
	client := fixture.clients[1]
	if client.mkdirCalls != 0 || client.chmodCalls != 0 || client.openFileCalls != 0 ||
		client.renameCalls != 0 || client.removeCalls != 0 || client.syncCalls != 0 {
		t.Fatalf("validation mutated target: mkdir=%d chmod=%d open_file=%d rename=%d remove=%d sync=%d",
			client.mkdirCalls, client.chmodCalls, client.openFileCalls,
			client.renameCalls, client.removeCalls, client.syncCalls)
	}
	if client.openCalls != 1 || client.readBytes != len(marker) ||
		client.maxReadRequest <= 0 || client.maxReadRequest > recoveryWorkspaceMarkerDocumentMaxBytes+1 {
		t.Fatalf("bounded validation read: open=%d bytes=%d want=%d max_request=%d",
			client.openCalls, client.readBytes, len(marker), client.maxReadRequest)
	}
	if client.lstatCalls == 0 || client.realPathCalls == 0 || client.closeCalls != 1 {
		t.Fatalf("validation observations: lstat=%d realpath=%d close=%d",
			client.lstatCalls, client.realPathCalls, client.closeCalls)
	}
}

func TestRecoverySFTPTargetValidateOwnedJobDirRemovedIsReadOnly(t *testing.T) {
	newRemovedAuthority := func(t *testing.T, fixture *recoveryLocalSFTPTargetFixture) (TargetCleanupPermit, RemoveOwnedJobDirRequest) {
		t.Helper()
		permit := fixture.cleanupPermit
		permit.Operation = TargetCleanupValidateRemovedJobDir
		permit.proof = nil
		permit = issueTargetCleanupPermit(permit, fixture.binding)
		return permit, RemoveOwnedJobDirRequest{
			Object: fixture.cleanupRequest.Object, MarkerBindingDigest: fixture.cleanupRequest.MarkerBindingDigest,
		}
	}

	assertReadOnly := func(t *testing.T, client *recoveryLocalSFTPClient) {
		t.Helper()
		if client.mkdirCalls != 0 || client.chmodCalls != 0 || client.openFileCalls != 0 ||
			client.renameCalls != 0 || client.removeCalls != 0 || client.removeDirectoryCalls != 0 ||
			client.syncCalls != 0 {
			t.Fatalf("removed validation mutated target: mkdir=%d chmod=%d open_file=%d rename=%d remove=%d remove_directory=%d sync=%d",
				client.mkdirCalls, client.chmodCalls, client.openFileCalls, client.renameCalls,
				client.removeCalls, client.removeDirectoryCalls, client.syncCalls)
		}
	}

	removeOwned := func(t *testing.T, fixture *recoveryLocalSFTPTargetFixture) {
		t.Helper()
		permit := fixture.cleanupPermit
		permit.Operation = TargetCleanupRemoveOwnedJobDir
		permit.proof = nil
		permit = issueTargetCleanupPermitWithLiveValidator(
			permit, func(context.Context, TargetCleanupPermit) error { return nil }, fixture.binding,
		)
		request := RemoveOwnedJobDirRequest{
			Object: fixture.cleanupRequest.Object, MarkerBindingDigest: fixture.cleanupRequest.MarkerBindingDigest,
		}
		removal, err := fixture.target.RemoveOwnedJobDir(context.Background(), permit, request)
		if err == nil && !removal.Complete {
			removal, err = fixture.target.RemoveOwnedJobDir(context.Background(), permit, request)
		}
		if err != nil || !removal.Complete {
			t.Fatalf("prepare removed tuple removal=%+v error=%v", removal, err)
		}
	}

	validateRemoved := func(t *testing.T, fixture *recoveryLocalSFTPTargetFixture) (OwnedJobDirRemovalValidation, *recoveryLocalSFTPClient, error) {
		t.Helper()
		permit, request := newRemovedAuthority(t, fixture)
		client := &recoveryLocalSFTPClient{}
		validation, err := fixture.targetWithClient(client).ValidateOwnedJobDirRemoved(
			context.Background(), permit, request,
		)
		return validation, client, err
	}

	t.Run("exact absence returns an observation", func(t *testing.T) {
		fixture := newRecoveryLocalSFTPTargetFixture(t)
		fixture.create(t)
		removeOwned(t, fixture)

		validation, client, err := validateRemoved(t, fixture)
		if err != nil {
			t.Fatalf("validate clean tuple: %v", err)
		}
		if validation.Object != fixture.cleanupRequest.Object ||
			validation.RootRevision != fixture.binding.RootRevision ||
			!validOpaqueRevision(validation.TargetRevision) {
			t.Fatalf("removed validation=%+v, want exact clean tuple observation", validation)
		}
		assertReadOnly(t, client)
	})

	t.Run("final workspace present blocks", func(t *testing.T) {
		fixture := newRecoveryLocalSFTPTargetFixture(t)
		fixture.create(t)

		_, client, err := validateRemoved(t, fixture)
		if !errors.Is(err, ErrRecoveryTargetChanged) {
			t.Fatalf("final-present validation error=%v, want changed", err)
		}
		assertReadOnly(t, client)
	})

	t.Run("captured sibling present blocks", func(t *testing.T) {
		fixture := newRecoveryLocalSFTPTargetFixture(t)
		fixture.create(t)
		jobsPath, jobPath, _ := fixture.paths()
		permit := fixture.cleanupPermit
		permit.Operation = TargetCleanupRemoveOwnedJobDir
		permit.proof = nil
		permit = issueTargetCleanupPermitWithLiveValidator(
			permit, func(context.Context, TargetCleanupPermit) error { return nil }, fixture.binding,
		)
		request := RemoveOwnedJobDirRequest{
			Object: fixture.cleanupRequest.Object, MarkerBindingDigest: fixture.cleanupRequest.MarkerBindingDigest,
		}
		base := &recoveryLocalSFTPClient{}
		client := &recoveryScriptedSFTPClient{base: base}
		client.openFile = func(value string, flag int) (recoveryTargetSFTPFile, error) {
			if strings.HasPrefix(filepath.Base(value), recoveryOwnedCleanupVerifiedPrefix) {
				return nil, os.ErrPermission
			}
			return base.OpenFile(value, flag)
		}
		if _, err := fixture.targetWithClient(client).RemoveOwnedJobDir(context.Background(), permit, request); !errors.Is(err, ErrRecoveryTargetUnavailable) {
			t.Fatalf("prepare captured tuple error=%v, want unavailable", err)
		}
		if _, err := os.Lstat(jobPath); !os.IsNotExist(err) {
			t.Fatalf("final workspace stat=%v, want absent", err)
		}
		capturedPath := filepath.Join(jobsPath, recoveryOwnedCleanupCapturedComponent(permit))
		if _, err := os.Lstat(capturedPath); err != nil {
			t.Fatalf("captured workspace stat=%v, want present", err)
		}

		_, validationClient, err := validateRemoved(t, fixture)
		if !errors.Is(err, ErrRecoveryTargetChanged) {
			t.Fatalf("captured-present validation error=%v, want changed", err)
		}
		assertReadOnly(t, validationClient)
	})

	t.Run("verified marker present blocks", func(t *testing.T) {
		fixture := newRecoveryLocalSFTPTargetFixture(t)
		fixture.create(t)
		jobsPath, jobPath, markerPath := fixture.paths()
		markerBytes, err := os.ReadFile(markerPath)
		if err != nil {
			t.Fatalf("read owner marker: %v", err)
		}
		markerDocument, err := decodeRecoveryWorkspaceMarkerDocument(markerBytes)
		if err != nil {
			t.Fatalf("decode owner marker: %v", err)
		}
		permit := fixture.cleanupPermit
		permit.Operation = TargetCleanupRemoveOwnedJobDir
		permit.proof = nil
		permit = issueTargetCleanupPermitWithLiveValidator(
			permit, func(context.Context, TargetCleanupPermit) error { return nil }, fixture.binding,
		)
		artifacts, err := deriveRecoveryOwnedCleanupArtifactBinding(fixture.material, permit, markerDocument, markerBytes)
		if err != nil {
			t.Fatalf("derive cleanup artifacts: %v", err)
		}
		capturedPath := filepath.Join(jobsPath, artifacts.capturedComponent)
		verifiedPath := filepath.Join(jobsPath, artifacts.verifiedComponent)
		if err := os.Rename(jobPath, capturedPath); err != nil {
			t.Fatalf("capture workspace: %v", err)
		}
		if err := os.RemoveAll(capturedPath); err != nil {
			t.Fatalf("remove captured workspace: %v", err)
		}
		if err := os.WriteFile(verifiedPath, []byte(artifacts.verifiedDocument), 0o600); err != nil {
			t.Fatalf("write verified marker: %v", err)
		}

		_, client, err := validateRemoved(t, fixture)
		if !errors.Is(err, ErrRecoveryTargetChanged) {
			t.Fatalf("verified-present validation error=%v, want changed", err)
		}
		assertReadOnly(t, client)
	})
}

func TestRecoverySFTPTargetRemoveOwnedJobDirCapturesExactWorkspace(t *testing.T) {
	fixture := newRecoveryLocalSFTPTargetFixture(t)
	fixture.create(t)
	jobsPath, jobPath, markerPath := fixture.paths()
	if err := os.WriteFile(filepath.Join(jobPath, "payload.bin"), []byte("owned residue"), 0o600); err != nil {
		t.Fatalf("write owned residue: %v", err)
	}
	markerBefore, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatalf("read owner marker before capture: %v", err)
	}

	liveCalls := 0
	permit := fixture.cleanupPermit
	permit.Operation = TargetCleanupRemoveOwnedJobDir
	permit.proof = nil
	permit = issueTargetCleanupPermitWithLiveValidator(
		permit,
		func(context.Context, TargetCleanupPermit) error {
			liveCalls++
			return nil
		},
		fixture.binding,
	)
	request := RemoveOwnedJobDirRequest{
		Object: fixture.cleanupRequest.Object, MarkerBindingDigest: fixture.cleanupRequest.MarkerBindingDigest,
	}
	base := &recoveryLocalSFTPClient{}
	var renameLiveCalls int
	client := &recoveryScriptedSFTPClient{base: base}
	client.rename = func(oldName, newName string) error {
		renameLiveCalls = liveCalls
		return base.Rename(oldName, newName)
	}
	target := fixture.targetWithClient(client)
	removal, err := target.RemoveOwnedJobDir(context.Background(), permit, request)
	if err != nil {
		t.Fatalf("capture owned workspace: %v", err)
	}
	if removal.Complete || removal.RemovedEntries != 0 || !validDigest(removal.ProgressDigest) {
		t.Fatalf("capture removal=%+v, want incomplete zero-remove bounded progress", removal)
	}
	if liveCalls == 0 || renameLiveCalls == 0 {
		t.Fatalf("capture live validation calls=%d rename_observed=%d, want validation before rename",
			liveCalls, renameLiveCalls)
	}
	if base.renameCalls != 1 || len(base.renamePaths) != 1 || base.renamePaths[0][0] != jobPath {
		t.Fatalf("capture rename calls=%d paths=%v, want one exact job-directory rename", base.renameCalls, base.renamePaths)
	}
	capturedPath := base.renamePaths[0][1]
	if filepath.Dir(capturedPath) != jobsPath || capturedPath == jobPath || filepath.Base(capturedPath) == "" {
		t.Fatalf("capture destination=%q, want deterministic same-parent cleanup sibling", capturedPath)
	}
	if _, err := os.Lstat(jobPath); !os.IsNotExist(err) {
		t.Fatalf("final workspace after capture stat error=%v, want absent", err)
	}
	markerAfter, err := os.ReadFile(filepath.Join(capturedPath, recoveryWorkspaceMarkerFileName))
	if err != nil || !bytes.Equal(markerAfter, markerBefore) {
		t.Fatalf("captured owner marker error=%v equal=%t, want exact reauthenticated marker retained",
			err, bytes.Equal(markerAfter, markerBefore))
	}
	if base.removeCalls != 0 || base.removeDirectoryCalls != 0 {
		t.Fatalf("capture removed descendants: remove=%d remove_directory=%d", base.removeCalls, base.removeDirectoryCalls)
	}
	entries, err := os.ReadDir(jobsPath)
	if err != nil {
		t.Fatalf("read jobs parent after capture: %v", err)
	}
	verifiedSiblings := 0
	for _, entry := range entries {
		if entry.Name() == filepath.Base(capturedPath) || entry.Name() == filepath.Base(jobPath) {
			continue
		}
		verifiedSiblings++
		if entry.IsDir() {
			t.Fatalf("unexpected cleanup sibling after capture: %s", entry.Name())
		}
	}
	if verifiedSiblings != 1 {
		t.Fatalf("verified cleanup siblings=%d, want one external authenticated marker", verifiedSiblings)
	}
}

func TestRecoveryOwnedCleanupArtifactBindingUsesHistoricalKey(t *testing.T) {
	fixture := newRecoveryLocalSFTPTargetFixture(t)
	fixture.create(t)
	_, _, markerPath := fixture.paths()
	markerBytes, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatalf("read owner marker for cleanup artifact: %v", err)
	}
	markerDocument, err := decodeRecoveryWorkspaceMarkerDocument(markerBytes)
	if err != nil {
		t.Fatalf("decode owner marker for cleanup artifact: %v", err)
	}
	permit := fixture.cleanupPermit
	permit.Operation = TargetCleanupRemoveOwnedJobDir
	permit.proof = nil
	permit = issueTargetCleanupPermitWithLiveValidator(
		permit, func(context.Context, TargetCleanupPermit) error { return nil }, fixture.binding,
	)
	first, err := deriveRecoveryOwnedCleanupArtifactBinding(fixture.material, permit, markerDocument, markerBytes)
	if err != nil {
		t.Fatalf("derive owned cleanup artifact binding: %v", err)
	}
	replayed, err := deriveRecoveryOwnedCleanupArtifactBinding(
		cloneDomainKeyMaterial(fixture.material), permit, markerDocument, append([]byte(nil), markerBytes...),
	)
	if err != nil || first.capturedComponent != replayed.capturedComponent ||
		first.verifiedComponent != replayed.verifiedComponent || first.capturedDocument != replayed.capturedDocument ||
		first.verifiedDocument != replayed.verifiedDocument {
		t.Fatalf("replayed cleanup artifacts=%+v err=%v, want exact historical-key replay", replayed, err)
	}
	if first.capturedComponent == first.verifiedComponent ||
		!strings.HasPrefix(first.capturedComponent, recoveryOwnedCleanupArtifactPrefix) ||
		!strings.HasPrefix(first.verifiedComponent, recoveryOwnedCleanupVerifiedPrefix) ||
		!validDigest(first.markerDigest) {
		t.Fatalf("cleanup artifact components=%+v, want domain-separated bounded components", first)
	}
	expectedBody := recoveryOwnedCleanupArtifactBody{
		SchemaVersion: 1, KeyVersion: first.keyVersion, JobID: permit.JobID, RootID: permit.RootID,
		RootRevision: permit.RootRevision, WorkspaceLocator: recoveryWorkspaceLocatorDirectory + "/" + permit.JobID,
		MarkerBindingDigest: permit.MarkerBindingDigest, MarkerCreatorID: permit.MarkerCreatorID,
		MarkerCreatorFence: permit.MarkerCreatorFence, MarkerDigest: first.markerDigest,
		CapturedComponent: first.capturedComponent,
	}
	if err := validateRecoveryOwnedCleanupArtifactDocument(
		[]byte(first.capturedDocument), expectedBody, fixture.material.Key, recoveryOwnedCleanupArtifactDomain,
	); err != nil {
		t.Fatalf("captured cleanup artifact document: %v", err)
	}
	if err := validateRecoveryOwnedCleanupArtifactDocument(
		[]byte(first.verifiedDocument), expectedBody, fixture.material.Key, recoveryOwnedCleanupVerifiedDomain,
	); err != nil {
		t.Fatalf("verified cleanup artifact document: %v", err)
	}
	wrongDomain := recoveryOwnedCleanupArtifactDomain
	if err := validateRecoveryOwnedCleanupArtifactDocument(
		[]byte(first.verifiedDocument), expectedBody, fixture.material.Key, wrongDomain,
	); err == nil {
		t.Fatal("verified cleanup artifact accepted under captured domain")
	}
	mutations := []struct {
		name   string
		change func(*backupasset.DomainKeyMaterial, *TargetCleanupPermit, []byte, *recoveryWorkspaceMarkerDocument)
	}{
		{name: "historical key", change: func(material *backupasset.DomainKeyMaterial, _ *TargetCleanupPermit, _ []byte, _ *recoveryWorkspaceMarkerDocument) {
			material.Key[0] ^= 0x01
		}},
		{name: "key version", change: func(_ *backupasset.DomainKeyMaterial, _ *TargetCleanupPermit, _ []byte, document *recoveryWorkspaceMarkerDocument) {
			document.KeyVersion++
		}},
		{name: "job", change: func(_ *backupasset.DomainKeyMaterial, candidate *TargetCleanupPermit, _ []byte, _ *recoveryWorkspaceMarkerDocument) {
			candidate.JobID = strings.Repeat("2", 32)
		}},
		{name: "marker", change: func(_ *backupasset.DomainKeyMaterial, _ *TargetCleanupPermit, marker []byte, _ *recoveryWorkspaceMarkerDocument) {
			marker[len(marker)-1] ^= 0x01
		}},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			material := cloneDomainKeyMaterial(fixture.material)
			candidate := permit
			candidate.proof = permit.proof
			markerCopy := append([]byte(nil), markerBytes...)
			documentCopy := markerDocument
			mutation.change(&material, &candidate, markerCopy, &documentCopy)
			changed, deriveErr := deriveRecoveryOwnedCleanupArtifactBinding(material, candidate, documentCopy, markerCopy)
			if deriveErr == nil && changed.capturedComponent == first.capturedComponent &&
				changed.verifiedComponent == first.verifiedComponent && changed.capturedDocument == first.capturedDocument {
				t.Fatalf("mutation %s retained cleanup artifact binding=%+v", mutation.name, changed)
			}
		})
	}
}

func TestRecoverySFTPTargetRemoveOwnedJobDirReentersAuthenticatedCapture(t *testing.T) {
	fixture := newRecoveryLocalSFTPTargetFixture(t)
	fixture.create(t)
	jobsPath, jobPath, _ := fixture.paths()
	unknownSibling := filepath.Join(jobsPath, ".unknown-cleanup-sibling")
	if err := os.WriteFile(unknownSibling, []byte("must remain"), 0o600); err != nil {
		t.Fatalf("create unknown cleanup sibling: %v", err)
	}
	permit := fixture.cleanupPermit
	permit.Operation = TargetCleanupRemoveOwnedJobDir
	permit.proof = nil
	permit = issueTargetCleanupPermitWithLiveValidator(
		permit, func(context.Context, TargetCleanupPermit) error { return nil }, fixture.binding,
	)
	request := RemoveOwnedJobDirRequest{
		Object: fixture.cleanupRequest.Object, MarkerBindingDigest: fixture.cleanupRequest.MarkerBindingDigest,
	}
	firstBase := &recoveryLocalSFTPClient{}
	firstClient := &recoveryScriptedSFTPClient{base: firstBase}
	firstClient.openFile = func(value string, flag int) (recoveryTargetSFTPFile, error) {
		if strings.HasPrefix(filepath.Base(value), recoveryOwnedCleanupVerifiedPrefix) {
			return nil, os.ErrPermission
		}
		return firstBase.OpenFile(value, flag)
	}
	firstTarget := fixture.targetWithClient(firstClient)
	if _, err := firstTarget.RemoveOwnedJobDir(context.Background(), permit, request); !errors.Is(err, ErrRecoveryTargetUnavailable) {
		t.Fatalf("pre-verified-marker crash error=%v, want unavailable", err)
	}
	if firstBase.renameCalls != 1 || len(firstBase.renamePaths) != 1 || firstBase.renamePaths[0][0] != jobPath {
		t.Fatalf("pre-verified-marker capture rename calls=%d paths=%v", firstBase.renameCalls, firstBase.renamePaths)
	}
	capturedPath := firstBase.renamePaths[0][1]
	if _, err := os.Lstat(capturedPath); err != nil {
		t.Fatalf("captured workspace after interrupted marker creation: %v", err)
	}
	verifiedPath := ""
	entries, err := os.ReadDir(jobsPath)
	if err != nil {
		t.Fatalf("read jobs parent after interrupted marker creation: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), recoveryOwnedCleanupVerifiedPrefix) {
			verifiedPath = filepath.Join(jobsPath, entry.Name())
		}
	}
	if verifiedPath != "" {
		t.Fatalf("interrupted marker creation left verified marker %q", verifiedPath)
	}

	secondBase := &recoveryLocalSFTPClient{}
	secondTarget := fixture.targetWithClient(&recoveryScriptedSFTPClient{base: secondBase})
	resumed, err := secondTarget.RemoveOwnedJobDir(context.Background(), permit, request)
	if err != nil {
		t.Fatalf("reenter authenticated captured workspace: %v", err)
	}
	if resumed.Complete || resumed.RemovedEntries != 0 || !validDigest(resumed.ProgressDigest) ||
		secondBase.renameCalls != 0 || secondBase.removeCalls != 0 || secondBase.removeDirectoryCalls != 0 {
		t.Fatalf("reentry result=%+v mutations rename=%d remove=%d remove_directory=%d",
			resumed, secondBase.renameCalls, secondBase.removeCalls, secondBase.removeDirectoryCalls)
	}
	if _, err := os.Lstat(filepath.Join(capturedPath, recoveryWorkspaceMarkerFileName)); err != nil {
		t.Fatalf("reentry captured owner marker: %v", err)
	}
	entries, err = os.ReadDir(jobsPath)
	if err != nil {
		t.Fatalf("read jobs parent after authenticated reentry: %v", err)
	}
	verifiedCount := 0
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), recoveryOwnedCleanupVerifiedPrefix) {
			verifiedCount++
		}
	}
	if verifiedCount != 1 {
		t.Fatalf("reentry verified marker count=%d, want one", verifiedCount)
	}
	if content, err := os.ReadFile(unknownSibling); err != nil || string(content) != "must remain" {
		t.Fatalf("reentry changed unknown sibling content=%q err=%v", content, err)
	}
}

func TestRecoverySFTPTargetRemoveOwnedJobDirRejectsUnownedReentryAndWinners(t *testing.T) {
	newAuthority := func(t *testing.T, fixture *recoveryLocalSFTPTargetFixture) (TargetCleanupPermit, RemoveOwnedJobDirRequest) {
		t.Helper()
		permit := fixture.cleanupPermit
		permit.Operation = TargetCleanupRemoveOwnedJobDir
		permit.proof = nil
		permit = issueTargetCleanupPermitWithLiveValidator(
			permit, func(context.Context, TargetCleanupPermit) error { return nil }, fixture.binding,
		)
		return permit, RemoveOwnedJobDirRequest{
			Object: fixture.cleanupRequest.Object, MarkerBindingDigest: fixture.cleanupRequest.MarkerBindingDigest,
		}
	}

	t.Run("captured collision is preserved", func(t *testing.T) {
		fixture := newRecoveryLocalSFTPTargetFixture(t)
		fixture.create(t)
		jobsPath, jobPath, _ := fixture.paths()
		permit, request := newAuthority(t, fixture)
		capturedPath := filepath.Join(jobsPath, recoveryOwnedCleanupCapturedComponent(permit))
		if err := os.WriteFile(capturedPath, []byte("external captured winner"), 0o600); err != nil {
			t.Fatalf("install captured collision: %v", err)
		}
		base := &recoveryLocalSFTPClient{}
		removal, err := fixture.targetWithClient(&recoveryScriptedSFTPClient{base: base}).RemoveOwnedJobDir(
			context.Background(), permit, request,
		)
		if removal != (OwnedJobDirRemoval{}) || !errors.Is(err, ErrRecoveryTargetChanged) {
			t.Fatalf("captured collision removal=%+v error=%v, want unchanged target", removal, err)
		}
		if base.renameCalls != 0 || base.removeCalls != 0 || base.removeDirectoryCalls != 0 {
			t.Fatalf("captured collision mutated target: rename=%d remove=%d remove_directory=%d",
				base.renameCalls, base.removeCalls, base.removeDirectoryCalls)
		}
		if _, statErr := os.Lstat(jobPath); statErr != nil {
			t.Fatalf("captured collision lost final workspace: %v", statErr)
		}
		if got, readErr := os.ReadFile(capturedPath); readErr != nil || string(got) != "external captured winner" {
			t.Fatalf("captured collision evidence=%q error=%v, want preserved", got, readErr)
		}
	})

	t.Run("forged captured marker is never adopted", func(t *testing.T) {
		fixture := newRecoveryLocalSFTPTargetFixture(t)
		fixture.create(t)
		_, jobPath, _ := fixture.paths()
		permit, request := newAuthority(t, fixture)
		firstBase := &recoveryLocalSFTPClient{}
		firstClient := &recoveryScriptedSFTPClient{base: firstBase}
		firstClient.openFile = func(value string, flag int) (recoveryTargetSFTPFile, error) {
			if strings.HasPrefix(filepath.Base(value), recoveryOwnedCleanupVerifiedPrefix) {
				return nil, os.ErrPermission
			}
			return firstBase.OpenFile(value, flag)
		}
		if _, err := fixture.targetWithClient(firstClient).RemoveOwnedJobDir(context.Background(), permit, request); !errors.Is(err, ErrRecoveryTargetUnavailable) {
			t.Fatalf("interrupted capture error=%v, want unavailable", err)
		}
		if len(firstBase.renamePaths) != 1 || firstBase.renamePaths[0][0] != jobPath {
			t.Fatalf("interrupted capture renames=%v, want one final capture", firstBase.renamePaths)
		}
		capturedPath := firstBase.renamePaths[0][1]
		if err := os.WriteFile(filepath.Join(capturedPath, recoveryWorkspaceMarkerFileName), []byte("{}"), 0o600); err != nil {
			t.Fatalf("forge captured marker: %v", err)
		}
		secondBase := &recoveryLocalSFTPClient{}
		removal, err := fixture.targetWithClient(&recoveryScriptedSFTPClient{base: secondBase}).RemoveOwnedJobDir(
			context.Background(), permit, request,
		)
		if removal != (OwnedJobDirRemoval{}) || !errors.Is(err, ErrRecoveryTargetChanged) {
			t.Fatalf("forged marker removal=%+v error=%v, want changed", removal, err)
		}
		if secondBase.renameCalls != 0 || secondBase.removeCalls != 0 || secondBase.removeDirectoryCalls != 0 {
			t.Fatalf("forged marker mutated target: rename=%d remove=%d remove_directory=%d",
				secondBase.renameCalls, secondBase.removeCalls, secondBase.removeDirectoryCalls)
		}
	})

	t.Run("canonical drift captured sibling is never adopted", func(t *testing.T) {
		fixture := newRecoveryLocalSFTPTargetFixture(t)
		fixture.create(t)
		permit, request := newAuthority(t, fixture)
		firstBase := &recoveryLocalSFTPClient{}
		firstClient := &recoveryScriptedSFTPClient{base: firstBase}
		firstClient.openFile = func(value string, flag int) (recoveryTargetSFTPFile, error) {
			if strings.HasPrefix(filepath.Base(value), recoveryOwnedCleanupVerifiedPrefix) {
				return nil, os.ErrPermission
			}
			return firstBase.OpenFile(value, flag)
		}
		if _, err := fixture.targetWithClient(firstClient).RemoveOwnedJobDir(context.Background(), permit, request); !errors.Is(err, ErrRecoveryTargetUnavailable) {
			t.Fatalf("interrupted capture error=%v, want unavailable", err)
		}
		capturedPath := firstBase.renamePaths[0][1]
		if err := os.RemoveAll(capturedPath); err != nil {
			t.Fatalf("remove captured directory for drift: %v", err)
		}
		external := filepath.Join(t.TempDir(), "external-owned-looking")
		if err := os.Mkdir(external, 0o700); err != nil {
			t.Fatalf("create external drift directory: %v", err)
		}
		if err := os.WriteFile(filepath.Join(external, recoveryWorkspaceMarkerFileName), []byte("must remain"), 0o600); err != nil {
			t.Fatalf("write external drift marker: %v", err)
		}
		if err := os.Symlink(external, capturedPath); err != nil {
			t.Fatalf("install captured canonical drift: %v", err)
		}
		secondBase := &recoveryLocalSFTPClient{}
		removal, err := fixture.targetWithClient(&recoveryScriptedSFTPClient{base: secondBase}).RemoveOwnedJobDir(
			context.Background(), permit, request,
		)
		if removal != (OwnedJobDirRemoval{}) || !errors.Is(err, ErrRecoveryTargetChanged) {
			t.Fatalf("canonical drift removal=%+v error=%v, want changed", removal, err)
		}
		if secondBase.renameCalls != 0 || secondBase.removeCalls != 0 || secondBase.removeDirectoryCalls != 0 {
			t.Fatalf("canonical drift mutated target: rename=%d remove=%d remove_directory=%d",
				secondBase.renameCalls, secondBase.removeCalls, secondBase.removeDirectoryCalls)
		}
		if content, readErr := os.ReadFile(filepath.Join(external, recoveryWorkspaceMarkerFileName)); readErr != nil || string(content) != "must remain" {
			t.Fatalf("canonical drift external evidence=%q error=%v, want preserved", content, readErr)
		}
	})

	t.Run("external final winner after capture is preserved", func(t *testing.T) {
		fixture := newRecoveryLocalSFTPTargetFixture(t)
		fixture.create(t)
		_, jobPath, _ := fixture.paths()
		permit, request := newAuthority(t, fixture)
		base := &recoveryLocalSFTPClient{}
		client := &recoveryScriptedSFTPClient{base: base}
		client.rename = func(oldName, newName string) error {
			if err := base.Rename(oldName, newName); err != nil {
				return err
			}
			if oldName == jobPath {
				if err := os.Mkdir(jobPath, 0o700); err != nil {
					return err
				}
				if err := os.WriteFile(filepath.Join(jobPath, recoveryWorkspaceMarkerFileName), []byte("external final winner"), 0o600); err != nil {
					return err
				}
			}
			return nil
		}
		removal, err := fixture.targetWithClient(client).RemoveOwnedJobDir(context.Background(), permit, request)
		if removal != (OwnedJobDirRemoval{}) || !errors.Is(err, ErrRecoveryTargetChanged) {
			t.Fatalf("external final winner removal=%+v error=%v, want changed", removal, err)
		}
		if base.renameCalls != 1 || base.removeCalls != 0 || base.removeDirectoryCalls != 0 {
			t.Fatalf("external final winner mutations rename=%d remove=%d remove_directory=%d",
				base.renameCalls, base.removeCalls, base.removeDirectoryCalls)
		}
		if _, statErr := os.Lstat(jobPath); statErr != nil {
			t.Fatalf("external final winner disappeared: %v", statErr)
		}
	})

	t.Run("external final winner after verified creation is preserved", func(t *testing.T) {
		fixture := newRecoveryLocalSFTPTargetFixture(t)
		fixture.create(t)
		_, jobPath, _ := fixture.paths()
		permit, request := newAuthority(t, fixture)
		base := &recoveryLocalSFTPClient{}
		client := &recoveryScriptedSFTPClient{base: base}
		client.openFile = func(value string, flag int) (recoveryTargetSFTPFile, error) {
			file, err := base.OpenFile(value, flag)
			if err != nil {
				return file, err
			}
			if strings.HasPrefix(filepath.Base(value), recoveryOwnedCleanupVerifiedPrefix) {
				if err := os.Mkdir(jobPath, 0o700); err != nil {
					_ = file.Close()
					return nil, err
				}
				if err := os.WriteFile(filepath.Join(jobPath, recoveryWorkspaceMarkerFileName), []byte("external final winner"), 0o600); err != nil {
					_ = file.Close()
					return nil, err
				}
			}
			return file, nil
		}
		removal, err := fixture.targetWithClient(client).RemoveOwnedJobDir(context.Background(), permit, request)
		if removal != (OwnedJobDirRemoval{}) || !errors.Is(err, ErrRecoveryTargetChanged) {
			t.Fatalf("late external final winner removal=%+v error=%v, want changed", removal, err)
		}
		if base.renameCalls != 1 || base.removeCalls != 0 || base.removeDirectoryCalls != 0 {
			t.Fatalf("late external final winner mutations rename=%d remove=%d remove_directory=%d",
				base.renameCalls, base.removeCalls, base.removeDirectoryCalls)
		}
		if _, statErr := os.Lstat(jobPath); statErr != nil {
			t.Fatalf("late external final winner disappeared: %v", statErr)
		}
	})

	t.Run("verified collision is preserved", func(t *testing.T) {
		fixture := newRecoveryLocalSFTPTargetFixture(t)
		fixture.create(t)
		jobsPath, jobPath, markerPath := fixture.paths()
		permit, request := newAuthority(t, fixture)
		markerBytes, err := os.ReadFile(markerPath)
		if err != nil {
			t.Fatalf("read owner marker for verified collision: %v", err)
		}
		markerDocument, err := decodeRecoveryWorkspaceMarkerDocument(markerBytes)
		if err != nil {
			t.Fatalf("decode owner marker for verified collision: %v", err)
		}
		artifacts, err := deriveRecoveryOwnedCleanupArtifactBinding(fixture.material, permit, markerDocument, markerBytes)
		if err != nil {
			t.Fatalf("derive verified collision artifact: %v", err)
		}
		verifiedPath := filepath.Join(jobsPath, artifacts.verifiedComponent)
		if err := os.WriteFile(verifiedPath, []byte("external verified winner"), 0o600); err != nil {
			t.Fatalf("install verified collision: %v", err)
		}
		base := &recoveryLocalSFTPClient{}
		removal, err := fixture.targetWithClient(&recoveryScriptedSFTPClient{base: base}).RemoveOwnedJobDir(
			context.Background(), permit, request,
		)
		if removal != (OwnedJobDirRemoval{}) || !errors.Is(err, ErrRecoveryTargetChanged) {
			t.Fatalf("verified collision removal=%+v error=%v, want changed", removal, err)
		}
		if base.renameCalls != 0 || base.removeCalls != 0 || base.removeDirectoryCalls != 0 {
			t.Fatalf("verified collision mutated target: rename=%d remove=%d remove_directory=%d",
				base.renameCalls, base.removeCalls, base.removeDirectoryCalls)
		}
		if _, statErr := os.Lstat(jobPath); statErr != nil {
			t.Fatalf("verified collision lost final workspace: %v", statErr)
		}
		if got, readErr := os.ReadFile(verifiedPath); readErr != nil || string(got) != "external verified winner" {
			t.Fatalf("verified collision evidence=%q error=%v, want preserved", got, readErr)
		}
	})
}

func TestRecoverySFTPTargetRemoveOwnedJobDirCrashReentryMatrix(t *testing.T) {
	rawErr := errors.New("RAW_R56_CRASH_FOR_TEST_ONLY")
	type crashCase struct {
		name           string
		reenter        bool
		expectComplete bool
		configure      func(*recoveryLocalSFTPTargetFixture, *recoveryScriptedSFTPClient, *recoveryLocalSFTPClient, string, string, string)
	}
	cases := []crashCase{
		{
			name: "before capture", reenter: false,
			configure: func(_ *recoveryLocalSFTPTargetFixture, client *recoveryScriptedSFTPClient, _ *recoveryLocalSFTPClient, _, _, _ string) {
				client.rename = func(string, string) error { return rawErr }
			},
		},
		{
			name: "after capture", reenter: true,
			configure: func(_ *recoveryLocalSFTPTargetFixture, client *recoveryScriptedSFTPClient, base *recoveryLocalSFTPClient, jobPath, _, _ string) {
				client.rename = func(oldName, newName string) error {
					if err := base.Rename(oldName, newName); err != nil {
						return err
					}
					return rawErr
				}
				_ = jobPath
			},
		},
		{
			name: "captured owner-marker read", reenter: true,
			configure: func(_ *recoveryLocalSFTPTargetFixture, client *recoveryScriptedSFTPClient, base *recoveryLocalSFTPClient, jobPath, capturedPath, _ string) {
				captured := false
				client.rename = func(oldName, newName string) error {
					if err := base.Rename(oldName, newName); err != nil {
						return err
					}
					captured = true
					return nil
				}
				client.open = func(value string) (recoveryTargetSFTPFile, error) {
					if captured && value == filepath.Join(capturedPath, recoveryWorkspaceMarkerFileName) {
						return nil, rawErr
					}
					return base.Open(value)
				}
				_ = jobPath
			},
		},
		{
			name: "after verified marker create", reenter: true, expectComplete: true,
			configure: func(_ *recoveryLocalSFTPTargetFixture, client *recoveryScriptedSFTPClient, base *recoveryLocalSFTPClient, _, _, verifiedPath string) {
				client.openFile = func(value string, flag int) (recoveryTargetSFTPFile, error) {
					file, err := base.OpenFile(value, flag)
					if err != nil || value != verifiedPath {
						return file, err
					}
					return &recoveryScriptedSFTPFile{base: file, close: func() error {
						_ = file.Close()
						return rawErr
					}}, nil
				}
			},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newRecoveryLocalSFTPTargetFixture(t)
			fixture.create(t)
			jobsPath, jobPath, markerPath := fixture.paths()
			permit := fixture.cleanupPermit
			permit.Operation = TargetCleanupRemoveOwnedJobDir
			permit.proof = nil
			permit = issueTargetCleanupPermitWithLiveValidator(
				permit, func(context.Context, TargetCleanupPermit) error { return nil }, fixture.binding,
			)
			request := RemoveOwnedJobDirRequest{
				Object: fixture.cleanupRequest.Object, MarkerBindingDigest: fixture.cleanupRequest.MarkerBindingDigest,
			}
			capturedPath := filepath.Join(jobsPath, recoveryOwnedCleanupCapturedComponent(permit))
			markerBytes, err := os.ReadFile(markerPath)
			if err != nil {
				t.Fatalf("read owner marker for crash matrix: %v", err)
			}
			markerDocument, err := decodeRecoveryWorkspaceMarkerDocument(markerBytes)
			if err != nil {
				t.Fatalf("decode owner marker for crash matrix: %v", err)
			}
			artifacts, err := deriveRecoveryOwnedCleanupArtifactBinding(fixture.material, permit, markerDocument, markerBytes)
			if err != nil {
				t.Fatalf("derive artifacts for crash matrix: %v", err)
			}
			verifiedPath := filepath.Join(jobsPath, artifacts.verifiedComponent)
			base := &recoveryLocalSFTPClient{}
			client := &recoveryScriptedSFTPClient{base: base}
			testCase.configure(fixture, client, base, jobPath, capturedPath, verifiedPath)
			_, firstErr := fixture.targetWithClient(client).RemoveOwnedJobDir(context.Background(), permit, request)
			if !errors.Is(firstErr, ErrRecoveryTargetUnavailable) {
				t.Fatalf("first crash error=%v, want unavailable", firstErr)
			}
			if strings.Contains(firstErr.Error(), rawErr.Error()) {
				t.Fatalf("first crash leaked raw dependency error: %v", firstErr)
			}
			if testCase.reenter {
				if _, statErr := os.Lstat(jobPath); !os.IsNotExist(statErr) {
					t.Fatalf("crash re-entry final workspace error=%v, want absent", statErr)
				}
				if _, statErr := os.Lstat(capturedPath); statErr != nil {
					t.Fatalf("crash re-entry captured workspace error=%v", statErr)
				}
				secondBase := &recoveryLocalSFTPClient{}
				resumed, resumeErr := fixture.targetWithClient(&recoveryScriptedSFTPClient{base: secondBase}).RemoveOwnedJobDir(
					context.Background(), permit, request,
				)
				wantComplete := testCase.expectComplete
				wantRemoved := 0
				if wantComplete {
					wantRemoved = 3
				}
				if resumeErr != nil || resumed.Complete != wantComplete || resumed.RemovedEntries != wantRemoved ||
					(!wantComplete && (secondBase.renameCalls != 0 || secondBase.removeCalls != 0 || secondBase.removeDirectoryCalls != 0)) {
					t.Fatalf("crash re-entry result=%+v error=%v mutations rename=%d remove=%d remove_directory=%d",
						resumed, resumeErr, secondBase.renameCalls, secondBase.removeCalls, secondBase.removeDirectoryCalls)
				}
			} else if _, statErr := os.Lstat(jobPath); statErr != nil {
				t.Fatalf("before-capture crash final workspace error=%v, want present", statErr)
			}
		})
	}
}

func TestRecoverySFTPTargetRemoveOwnedJobDirVerifiedCompletionSurvivesTargetCloseError(t *testing.T) {
	fixture := newRecoveryLocalSFTPTargetFixture(t)
	fixture.create(t)
	_, jobPath, _ := fixture.paths()
	permit := fixture.cleanupPermit
	permit.Operation = TargetCleanupRemoveOwnedJobDir
	permit.proof = nil
	permit = issueTargetCleanupPermitWithLiveValidator(
		permit, func(context.Context, TargetCleanupPermit) error { return nil }, fixture.binding,
	)
	request := RemoveOwnedJobDirRequest{
		Object: fixture.cleanupRequest.Object, MarkerBindingDigest: fixture.cleanupRequest.MarkerBindingDigest,
	}

	initial, err := fixture.target.RemoveOwnedJobDir(context.Background(), permit, request)
	if err != nil || initial.Complete || initial.RemovedEntries != 0 {
		t.Fatalf("initial cleanup capture=%+v error=%v, want incomplete capture", initial, err)
	}
	if len(fixture.clients) < 2 || len(fixture.clients[1].renamePaths) != 1 {
		t.Fatalf("initial cleanup clients=%d rename_paths=%v, want captured workspace", len(fixture.clients), fixture.clients[1].renamePaths)
	}
	capturedPath := fixture.clients[1].renamePaths[0][1]

	rawErr := errors.New("RAW_R60_TARGET_CLOSE_FOR_TEST_ONLY")
	base := &recoveryLocalSFTPClient{}
	client := &recoveryScriptedSFTPClient{base: base}
	client.close = func() error {
		_ = base.Close()
		return rawErr
	}
	sshCloseCalls := 0
	target := fixture.targetWithClient(client)
	target.sessions = newRecoveryTargetSessionFactoryForTest(
		fixture.resolver, fixture.dialer,
		func(*ssh.Client) (recoveryTargetSFTPClient, error) { return client, nil },
		func(*ssh.Client) error { sshCloseCalls++; return nil },
	)
	removal, err := target.RemoveOwnedJobDir(
		context.Background(), permit, request,
	)
	if err != nil || !removal.Complete || removal.RemovedEntries != 3 || !validDigest(removal.ProgressDigest) {
		t.Fatalf("verified cleanup with target-close ambiguity=%+v error=%v, want retained complete result", removal, err)
	}
	if base.closeCalls != 1 || sshCloseCalls != 1 {
		t.Fatalf("target SFTP/SSH close calls=%d/%d, want exactly one each", base.closeCalls, sshCloseCalls)
	}
	if _, statErr := os.Lstat(jobPath); !os.IsNotExist(statErr) {
		t.Fatalf("final workspace after verified cleanup stat=%v, want absent", statErr)
	}
	if _, statErr := os.Lstat(capturedPath); !os.IsNotExist(statErr) {
		t.Fatalf("captured workspace after verified cleanup stat=%v, want absent", statErr)
	}
}

func TestRecoverySFTPTargetRemoveOwnedJobDirCanceledAfterCompletionReentersCleanTuple(t *testing.T) {
	fixture := newRecoveryLocalSFTPTargetFixture(t)
	fixture.create(t)
	permit := fixture.cleanupPermit
	permit.Operation = TargetCleanupRemoveOwnedJobDir
	permit.proof = nil
	permit = issueTargetCleanupPermitWithLiveValidator(
		permit, func(context.Context, TargetCleanupPermit) error { return nil }, fixture.binding,
	)
	request := RemoveOwnedJobDirRequest{
		Object: fixture.cleanupRequest.Object, MarkerBindingDigest: fixture.cleanupRequest.MarkerBindingDigest,
	}
	initial, err := fixture.target.RemoveOwnedJobDir(context.Background(), permit, request)
	if err != nil || initial.Complete || initial.RemovedEntries != 0 {
		t.Fatalf("initial cleanup capture=%+v error=%v, want incomplete capture", initial, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	base := &recoveryLocalSFTPClient{}
	client := &recoveryScriptedSFTPClient{base: base}
	client.close = func() error {
		_ = base.Close()
		cancel()
		return nil
	}
	removal, err := fixture.targetWithClient(client).RemoveOwnedJobDir(ctx, permit, request)
	if removal != (OwnedJobDirRemoval{}) || !errors.Is(err, context.Canceled) {
		t.Fatalf("post-completion cancellation removal=%+v error=%v, want original cancellation", removal, err)
	}
	if base.closeCalls != 1 {
		t.Fatalf("post-completion cancellation target close calls=%d, want exactly one", base.closeCalls)
	}

	retryBase := &recoveryLocalSFTPClient{}
	retried, err := fixture.targetWithClient(&recoveryScriptedSFTPClient{base: retryBase}).RemoveOwnedJobDir(
		context.Background(), permit, request,
	)
	if err != nil || !retried.Complete || retried.RemovedEntries != 0 || !validDigest(retried.ProgressDigest) {
		t.Fatalf("clean-tuple reentry=%+v error=%v, want read-only complete adoption", retried, err)
	}
	if retryBase.renameCalls != 0 || retryBase.removeCalls != 0 || retryBase.removeDirectoryCalls != 0 {
		t.Fatalf("clean-tuple reentry mutated target: rename=%d remove=%d remove_directory=%d",
			retryBase.renameCalls, retryBase.removeCalls, retryBase.removeDirectoryCalls)
	}
}

func TestRecoverySFTPTargetRemoveOwnedJobDirR60DependencyFailureMatrix(t *testing.T) {
	var capturedLogs bytes.Buffer
	previousLogger := logger.Log
	logger.Log = zerolog.New(&capturedLogs)
	t.Cleanup(func() { logger.Log = previousLogger })

	type failureCase struct {
		name      string
		build     func(*testing.T, string)
		configure func(*testing.T, *recoveryScriptedSFTPClient, *recoveryLocalSFTPClient, string, error)
		assert    func(*testing.T, *recoveryLocalSFTPClient)
	}
	cases := []failureCase{
		{
			name: "captured directory open",
			configure: func(_ *testing.T, client *recoveryScriptedSFTPClient, _ *recoveryLocalSFTPClient, capturedPath string, rawErr error) {
				client.open = func(value string) (recoveryTargetSFTPFile, error) {
					if value == capturedPath {
						return nil, rawErr
					}
					return client.base.Open(value)
				}
			},
		},
		{
			name: "readdir",
			configure: func(_ *testing.T, client *recoveryScriptedSFTPClient, _ *recoveryLocalSFTPClient, capturedPath string, rawErr error) {
				client.open = func(value string) (recoveryTargetSFTPFile, error) {
					file, err := client.base.Open(value)
					if err != nil || value != capturedPath {
						return file, err
					}
					return &recoveryScriptedSFTPFile{
						base:    file,
						readDir: func(int) ([]os.FileInfo, error) { return nil, rawErr },
					}, nil
				}
			},
		},
		{
			name: "lstat",
			build: func(t *testing.T, jobPath string) {
				if err := os.WriteFile(filepath.Join(jobPath, "!r60-leaf"), []byte("leaf"), 0o600); err != nil {
					t.Fatalf("write lstat leaf: %v", err)
				}
			},
			configure: func(_ *testing.T, client *recoveryScriptedSFTPClient, base *recoveryLocalSFTPClient, capturedPath string, rawErr error) {
				leaf := filepath.Join(capturedPath, "!r60-leaf")
				client.lstat = func(value string, call int) (os.FileInfo, error) {
					if value == leaf {
						return nil, rawErr
					}
					return base.Lstat(value)
				}
			},
		},
		{
			name: "statvfs",
			configure: func(_ *testing.T, client *recoveryScriptedSFTPClient, _ *recoveryLocalSFTPClient, capturedPath string, rawErr error) {
				client.statVFS = func(value string, _ int) (*sftp.StatVFS, error) {
					if value == capturedPath {
						return nil, rawErr
					}
					return client.base.StatVFS(value)
				}
			},
		},
		{
			name: "captured marker read",
			configure: func(_ *testing.T, client *recoveryScriptedSFTPClient, base *recoveryLocalSFTPClient, capturedPath string, rawErr error) {
				markerPath := filepath.Join(capturedPath, recoveryWorkspaceMarkerFileName)
				client.open = func(value string) (recoveryTargetSFTPFile, error) {
					if value == markerPath {
						return nil, rawErr
					}
					return base.Open(value)
				}
			},
		},
		{
			name: "leaf remove",
			build: func(t *testing.T, jobPath string) {
				if err := os.WriteFile(filepath.Join(jobPath, "!r60-remove"), []byte("leaf"), 0o600); err != nil {
					t.Fatalf("write remove leaf: %v", err)
				}
			},
			configure: func(_ *testing.T, client *recoveryScriptedSFTPClient, base *recoveryLocalSFTPClient, capturedPath string, rawErr error) {
				leaf := filepath.Join(capturedPath, "!r60-remove")
				client.remove = func(value string) error {
					if value == leaf {
						return rawErr
					}
					return base.Remove(value)
				}
			},
		},
		{
			name: "directory remove",
			build: func(t *testing.T, jobPath string) {
				if err := os.Mkdir(filepath.Join(jobPath, "!r60-directory"), 0o700); err != nil {
					t.Fatalf("write remove directory: %v", err)
				}
			},
			configure: func(_ *testing.T, client *recoveryScriptedSFTPClient, base *recoveryLocalSFTPClient, capturedPath string, rawErr error) {
				directory := filepath.Join(capturedPath, "!r60-directory")
				client.removeDirectory = func(value string) error {
					if value == directory {
						return rawErr
					}
					return base.RemoveDirectory(value)
				}
			},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newRecoveryLocalSFTPTargetFixture(t)
			fixture.create(t)
			jobsPath, jobPath, markerPath := fixture.paths()
			if testCase.build != nil {
				testCase.build(t, jobPath)
			}
			permit := fixture.cleanupPermit
			permit.Operation = TargetCleanupRemoveOwnedJobDir
			permit.proof = nil
			permit = issueTargetCleanupPermitWithLiveValidator(
				permit, func(context.Context, TargetCleanupPermit) error { return nil }, fixture.binding,
			)
			request := RemoveOwnedJobDirRequest{
				Object: fixture.cleanupRequest.Object, MarkerBindingDigest: fixture.cleanupRequest.MarkerBindingDigest,
			}
			markerBytes, err := os.ReadFile(markerPath)
			if err != nil {
				t.Fatalf("read owner marker: %v", err)
			}
			markerDocument, err := decodeRecoveryWorkspaceMarkerDocument(markerBytes)
			if err != nil {
				t.Fatalf("decode owner marker: %v", err)
			}
			artifacts, err := deriveRecoveryOwnedCleanupArtifactBinding(fixture.material, permit, markerDocument, markerBytes)
			if err != nil {
				t.Fatalf("derive cleanup artifacts: %v", err)
			}
			initial, err := fixture.target.RemoveOwnedJobDir(context.Background(), permit, request)
			if err != nil || initial.Complete || initial.RemovedEntries != 0 {
				t.Fatalf("initial cleanup capture=%+v error=%v, want incomplete capture", initial, err)
			}
			capturedPath := filepath.Join(jobsPath, artifacts.capturedComponent)
			rawErr := errors.New(
				"RAW_R60_DEPENDENCY_FOR_TEST_ONLY private-host private-user private-credential " +
					"private-fsid private-sftp-status " + testCase.name,
			)
			base := &recoveryLocalSFTPClient{}
			client := &recoveryScriptedSFTPClient{base: base}
			testCase.configure(t, client, base, capturedPath, rawErr)
			openedFiles := make([]*recoveryCloseCountingSFTPFile, 0, 16)
			configuredOpen := client.open
			client.open = func(value string) (recoveryTargetSFTPFile, error) {
				var file recoveryTargetSFTPFile
				var openErr error
				if configuredOpen != nil {
					file, openErr = configuredOpen(value)
				} else {
					file, openErr = base.Open(value)
				}
				if file == nil {
					return nil, openErr
				}
				counted := &recoveryCloseCountingSFTPFile{recoveryTargetSFTPFile: file}
				openedFiles = append(openedFiles, counted)
				return counted, openErr
			}
			sshCloseCalls := 0
			target := fixture.targetWithClient(client)
			target.sessions = newRecoveryTargetSessionFactoryForTest(
				fixture.resolver, fixture.dialer,
				func(*ssh.Client) (recoveryTargetSFTPClient, error) { return client, nil },
				func(*ssh.Client) error { sshCloseCalls++; return nil },
			)
			removal, err := target.RemoveOwnedJobDir(context.Background(), permit, request)
			if removal != (OwnedJobDirRemoval{}) || !errors.Is(err, ErrRecoveryTargetUnavailable) {
				t.Fatalf("failure removal=%+v error=%v, want sanitized unavailable", removal, err)
			}
			for index, file := range openedFiles {
				if file.closeCalls != 1 {
					t.Fatalf("failure file %d close calls=%d, want exactly one", index, file.closeCalls)
				}
			}
			if base.closeCalls != 1 || sshCloseCalls != 1 {
				t.Fatalf("failure SFTP/SSH close calls=%d/%d, want exactly one each", base.closeCalls, sshCloseCalls)
			}
			encoded, marshalErr := json.Marshal([]any{permit, request, removal})
			if marshalErr != nil {
				t.Fatalf("marshal failure products: %v", marshalErr)
			}
			corpus := strings.Join([]string{
				err.Error(), string(encoded),
				fmt.Sprintf("%v\n%+v\n%#v", removal, removal, removal),
				fmt.Sprintf("%+v", fixture.dialer.audit), capturedLogs.String(),
			}, "\n")
			for _, forbidden := range []string{
				rawErr.Error(), fixture.binding.RootLocator, fixture.binding.CredentialRevision,
				jobPath, capturedPath, request.Object.PrivateRelativeLocator,
				string(markerBytes), string(fixture.material.Key),
				artifacts.capturedComponent, artifacts.verifiedComponent,
				artifacts.capturedDocument, artifacts.verifiedDocument, artifacts.markerDigest,
				"!r60-leaf", "!r60-remove", "!r60-directory", "private-fsid", "private-sftp-status",
			} {
				if forbidden != "" && strings.Contains(corpus, forbidden) {
					t.Fatalf("failure leaked private canary %q: %s", forbidden, corpus)
				}
			}
			if testCase.assert != nil {
				testCase.assert(t, base)
			}
		})
	}
	if capturedLogs.Len() != 0 {
		t.Fatalf("cleanup target emitted direct logs: %s", capturedLogs.String())
	}
}

func TestRecoveryCleanupProductsR60RedactPrivateFields(t *testing.T) {
	fixture := newRecoveryLocalSFTPTargetFixture(t)
	fixture.create(t)
	permit := fixture.cleanupPermit
	permit.Operation = TargetCleanupRemoveOwnedJobDir
	permit.proof = nil
	permit = issueTargetCleanupPermitWithLiveValidator(
		permit, func(context.Context, TargetCleanupPermit) error { return nil }, fixture.binding,
	)
	removeRequest := RemoveOwnedJobDirRequest{
		Object: fixture.cleanupRequest.Object, MarkerBindingDigest: fixture.cleanupRequest.MarkerBindingDigest,
	}
	products := []any{
		permit,
		removeRequest,
		fixture.cleanupRequest,
		OwnedJobDirValidation{
			Object: fixture.cleanupRequest.Object, MarkerBindingDigest: fixture.cleanupRequest.MarkerBindingDigest,
			RootRevision: permit.RootRevision, TargetRevision: "r60-validation-revision",
		},
		OwnedJobDirRemovalValidation{
			Object: fixture.cleanupRequest.Object, RootRevision: permit.RootRevision,
			TargetRevision: "r60-removed-validation-revision",
		},
		OwnedJobDirRemoval{Complete: true, RemovedEntries: 3,
			ProgressDigest: framedDigest("xirang/recovery/r60-product-progress/v1", permit.JobID)},
		RecoveryCleanupProgress{Phase: CleanupPhaseDeleted, Complete: true, RemovedEntries: 3,
			ProgressDigest: framedDigest("xirang/recovery/r60-lifecycle-progress/v1", permit.JobID)},
	}
	encoded, err := json.Marshal(products)
	if err != nil {
		t.Fatalf("marshal cleanup products: %v", err)
	}
	corpus := string(encoded)
	for _, product := range products {
		corpus += "\n" + fmt.Sprintf("%v\n%+v\n%#v", product, product, product)
	}
	for _, forbidden := range []string{
		fixture.binding.RootLocator,
		fixture.binding.CredentialRevision,
		fixture.binding.NodeRevision,
		fixture.binding.PlanBindingDigest,
		fixture.binding.bindingDigest,
		removeRequest.Object.PrivateRelativeLocator,
		permit.RootLocatorDigest,
		permit.TargetPathDigest,
		permit.MarkerBindingDigest,
		permit.MarkerCreatorID,
	} {
		if forbidden != "" && strings.Contains(corpus, forbidden) {
			t.Fatalf("cleanup products leaked private value %q: %s", forbidden, corpus)
		}
	}
}

func TestRecoverySFTPTargetRemoveOwnedJobDirR60MarkerResourceMatrix(t *testing.T) {
	var capturedLogs bytes.Buffer
	previousLogger := logger.Log
	logger.Log = zerolog.New(&capturedLogs)
	t.Cleanup(func() { logger.Log = previousLogger })

	stages := []string{
		"OpenFile with resource and error", "write", "Sync", "file close", "SFTP close", "SSH close",
	}
	for _, stage := range stages {
		t.Run(stage, func(t *testing.T) {
			fixture := newRecoveryLocalSFTPTargetFixture(t)
			fixture.create(t)
			_, _, markerPath := fixture.paths()
			markerBytes, err := os.ReadFile(markerPath)
			if err != nil {
				t.Fatalf("read owner marker: %v", err)
			}
			permit := fixture.cleanupPermit
			permit.Operation = TargetCleanupRemoveOwnedJobDir
			permit.proof = nil
			permit = issueTargetCleanupPermitWithLiveValidator(
				permit, func(context.Context, TargetCleanupPermit) error { return nil }, fixture.binding,
			)
			request := RemoveOwnedJobDirRequest{
				Object: fixture.cleanupRequest.Object, MarkerBindingDigest: fixture.cleanupRequest.MarkerBindingDigest,
			}
			rawErr := errors.New(
				"RAW_R60_MARKER_RESOURCE_FOR_TEST_ONLY private-host private-user private-credential " +
					"private-marker private-content private-key private-token private-fsid private-sftp-status",
			)
			base := &recoveryLocalSFTPClient{}
			client := &recoveryScriptedSFTPClient{base: base}
			openedFiles := make([]*recoveryCloseCountingSFTPFile, 0, 16)
			wrap := func(file recoveryTargetSFTPFile) *recoveryCloseCountingSFTPFile {
				counted := &recoveryCloseCountingSFTPFile{recoveryTargetSFTPFile: file}
				openedFiles = append(openedFiles, counted)
				return counted
			}
			client.open = func(value string) (recoveryTargetSFTPFile, error) {
				file, openErr := base.Open(value)
				if file == nil {
					return nil, openErr
				}
				return wrap(file), openErr
			}
			faultInjected := false
			client.openFile = func(value string, flags int) (recoveryTargetSFTPFile, error) {
				file, openErr := base.OpenFile(value, flags)
				if file == nil {
					return nil, openErr
				}
				scripted := &recoveryScriptedSFTPFile{base: file}
				if !faultInjected {
					faultInjected = true
					switch stage {
					case "OpenFile with resource and error":
						return wrap(scripted), rawErr
					case "write":
						scripted.write = func([]byte) (int, error) { return 0, rawErr }
					case "Sync":
						scripted.sync = func() error { return rawErr }
					case "file close":
						scripted.close = func() error {
							if closeErr := file.Close(); closeErr != nil {
								return closeErr
							}
							return rawErr
						}
					}
				}
				return wrap(scripted), openErr
			}
			if stage == "SFTP close" {
				client.close = func() error {
					base.closeCalls++
					return rawErr
				}
			}
			sshCloseCalls := 0
			target := fixture.targetWithClient(client)
			target.sessions = newRecoveryTargetSessionFactoryForTest(
				fixture.resolver, fixture.dialer,
				func(*ssh.Client) (recoveryTargetSFTPClient, error) { return client, nil },
				func(*ssh.Client) error {
					sshCloseCalls++
					if stage == "SSH close" {
						return rawErr
					}
					return nil
				},
			)
			removal, err := target.RemoveOwnedJobDir(context.Background(), permit, request)
			if removal != (OwnedJobDirRemoval{}) || !errors.Is(err, ErrRecoveryTargetUnavailable) {
				t.Fatalf("stage=%s removal=%+v error=%v, want zero/sanitized unavailable", stage, removal, err)
			}
			for index, file := range openedFiles {
				if file.closeCalls != 1 {
					t.Fatalf("stage=%s file=%d close calls=%d, want exactly one", stage, index, file.closeCalls)
				}
			}
			if base.closeCalls != 1 || sshCloseCalls != 1 {
				t.Fatalf("stage=%s SFTP/SSH close=%d/%d, want exactly one each", stage, base.closeCalls, sshCloseCalls)
			}
			encoded, marshalErr := json.Marshal([]any{permit, request, removal})
			if marshalErr != nil {
				t.Fatalf("stage=%s marshal products: %v", stage, marshalErr)
			}
			corpus := strings.Join([]string{
				err.Error(), string(encoded), fmt.Sprintf("%v\n%+v\n%#v", removal, removal, removal),
				fmt.Sprintf("%+v", fixture.dialer.audit), capturedLogs.String(),
			}, "\n")
			for _, forbidden := range []string{
				rawErr.Error(), fixture.binding.RootLocator, fixture.binding.CredentialRevision,
				request.Object.PrivateRelativeLocator, string(markerBytes), string(fixture.material.Key),
				"private-marker", "private-content", "private-key", "private-token",
				"private-fsid", "private-sftp-status",
			} {
				if forbidden != "" && strings.Contains(corpus, forbidden) {
					t.Fatalf("stage=%s leaked private canary %q: %s", stage, forbidden, corpus)
				}
			}
		})
	}
	if capturedLogs.Len() != 0 {
		t.Fatalf("cleanup marker target emitted direct logs: %s", capturedLogs.String())
	}
}

func TestRecoveryOwnedCleanupDepthFirstNoFollowMatrix(t *testing.T) {
	fixture := newRecoveryLocalSFTPTargetFixture(t)
	fixture.create(t)
	jobsPath, jobPath, markerPath := fixture.paths()
	if err := os.WriteFile(filepath.Join(jobPath, "regular.bin"), []byte("regular"), 0o600); err != nil {
		t.Fatalf("write regular cleanup leaf: %v", err)
	}
	if err := os.Mkdir(filepath.Join(jobPath, "empty-dir"), 0o700); err != nil {
		t.Fatalf("create empty cleanup directory: %v", err)
	}
	nestedPath := filepath.Join(jobPath, "nested-dir")
	if err := os.Mkdir(nestedPath, 0o700); err != nil {
		t.Fatalf("create nested cleanup directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nestedPath, "nested.bin"), []byte("nested"), 0o600); err != nil {
		t.Fatalf("write nested cleanup leaf: %v", err)
	}
	externalRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(externalRoot, "must-remain"), []byte("external"), 0o600); err != nil {
		t.Fatalf("write external symlink target: %v", err)
	}
	if err := os.Symlink(externalRoot, filepath.Join(jobPath, "directory-link")); err != nil {
		t.Fatalf("create symlink-to-directory cleanup leaf: %v", err)
	}
	if err := syscall.Mkfifo(filepath.Join(jobPath, "special.pipe"), 0o600); err != nil {
		t.Fatalf("create special cleanup leaf: %v", err)
	}
	unknownSibling := filepath.Join(jobsPath, ".unknown-cleanup-sibling")
	if err := os.WriteFile(unknownSibling, []byte("unknown"), 0o600); err != nil {
		t.Fatalf("write unknown cleanup sibling: %v", err)
	}

	permit := fixture.cleanupPermit
	permit.Operation = TargetCleanupRemoveOwnedJobDir
	permit.proof = nil
	permit = issueTargetCleanupPermitWithLiveValidator(
		permit, func(context.Context, TargetCleanupPermit) error { return nil }, fixture.binding,
	)
	request := RemoveOwnedJobDirRequest{
		Object: fixture.cleanupRequest.Object, MarkerBindingDigest: fixture.cleanupRequest.MarkerBindingDigest,
	}
	markerBytes, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatalf("read owner marker before cleanup: %v", err)
	}
	markerDocument, err := decodeRecoveryWorkspaceMarkerDocument(markerBytes)
	if err != nil {
		t.Fatalf("decode owner marker before cleanup: %v", err)
	}
	artifacts, err := deriveRecoveryOwnedCleanupArtifactBinding(
		fixture.material, permit, markerDocument, markerBytes,
	)
	if err != nil {
		t.Fatalf("derive cleanup artifacts: %v", err)
	}
	verifiedPath := filepath.Join(jobsPath, artifacts.verifiedComponent)
	first, err := fixture.target.RemoveOwnedJobDir(context.Background(), permit, request)
	if err != nil || first.Complete || first.RemovedEntries != 0 {
		t.Fatalf("initial cleanup capture=%+v error=%v, want incomplete zero-remove capture", first, err)
	}
	if len(fixture.clients) < 2 || len(fixture.clients[1].renamePaths) != 1 {
		t.Fatalf("initial cleanup clients=%d rename_paths=%v, want exact capture", len(fixture.clients), fixture.clients[1].renamePaths)
	}
	capturedPath := fixture.clients[1].renamePaths[0][1]
	second, err := fixture.target.RemoveOwnedJobDir(context.Background(), permit, request)
	if err != nil || !second.Complete || second.RemovedEntries != 9 || !validDigest(second.ProgressDigest) {
		t.Fatalf("depth-first cleanup=%+v error=%v, want complete nine-remove pass", second, err)
	}
	if len(fixture.clients) < 3 {
		t.Fatalf("depth-first cleanup clients=%d, want fresh re-entry session", len(fixture.clients))
	}
	cleanupClient := fixture.clients[2]
	if cleanupClient.renameCalls != 0 {
		t.Fatalf("depth-first re-entry repeated capture rename=%v", cleanupClient.renamePaths)
	}
	if _, statErr := os.Lstat(filepath.Join(externalRoot, "must-remain")); statErr != nil {
		t.Fatalf("symlink-to-directory target was followed or removed: %v", statErr)
	}
	if _, statErr := os.Lstat(capturedPath); !os.IsNotExist(statErr) {
		t.Fatalf("captured workspace after bounded cleanup stat=%v, want absent", statErr)
	}
	if _, statErr := os.Lstat(jobPath); !os.IsNotExist(statErr) {
		t.Fatalf("final workspace after bounded cleanup stat=%v, want absent", statErr)
	}
	if content, readErr := os.ReadFile(unknownSibling); readErr != nil || string(content) != "unknown" {
		t.Fatalf("unknown sibling content=%q error=%v, want preserved", content, readErr)
	}
	if cleanupClient.removeCalls+cleanupClient.removeDirectoryCalls != 9 {
		t.Fatalf("depth-first remove counts regular=%d directory=%d, want nine total",
			cleanupClient.removeCalls, cleanupClient.removeDirectoryCalls)
	}
	wantOrder := []string{
		"leaf:" + filepath.Join(capturedPath, recoveryWorkspaceMarkerFileName),
		"leaf:" + filepath.Join(capturedPath, "directory-link"),
		"directory:" + filepath.Join(capturedPath, "empty-dir"),
		"leaf:" + filepath.Join(capturedPath, "nested-dir", "nested.bin"),
		"directory:" + filepath.Join(capturedPath, "nested-dir"),
		"leaf:" + filepath.Join(capturedPath, "regular.bin"),
		"leaf:" + filepath.Join(capturedPath, "special.pipe"),
		"directory:" + capturedPath,
		"leaf:" + verifiedPath,
	}
	if !reflect.DeepEqual(cleanupClient.removeOrder, wantOrder) {
		t.Fatalf("depth-first removal order=%v, want exact post-order %v", cleanupClient.removeOrder, wantOrder)
	}
}

func TestRecoveryOwnedCleanupFilesystemBoundaryMatrix(t *testing.T) {
	type boundaryCase struct {
		name       string
		build      func(*testing.T, string)
		configure  func(*testing.T, *recoveryScriptedSFTPClient, *recoveryLocalSFTPClient, string)
		assertKeep func(*testing.T, string)
	}
	cases := []boundaryCase{
		{
			name: "different filesystem on entry",
			build: func(t *testing.T, jobPath string) {
				boundary := filepath.Join(jobPath, "!boundary")
				if err := os.Mkdir(boundary, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(boundary, "leaf"), []byte("keep"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			configure: func(_ *testing.T, client *recoveryScriptedSFTPClient, _ *recoveryLocalSFTPClient, capturedPath string) {
				boundary := filepath.Join(capturedPath, "!boundary")
				client.statVFS = func(value string, _ int) (*sftp.StatVFS, error) {
					if value == boundary {
						return &sftp.StatVFS{Fsid: 8, Files: 100, Favail: 20, Namemax: 255}, nil
					}
					return &sftp.StatVFS{Fsid: 7, Files: 100, Favail: 20, Namemax: 255}, nil
				}
			},
			assertKeep: func(t *testing.T, jobPath string) {
				if _, err := os.Stat(filepath.Join(jobPath, "!boundary", "leaf")); err != nil {
					t.Fatalf("filesystem-boundary leaf error=%v, want preserved", err)
				}
			},
		},
		{
			name: "filesystem drift after enumeration",
			build: func(t *testing.T, jobPath string) {
				if err := os.Mkdir(filepath.Join(jobPath, "!boundary"), 0o700); err != nil {
					t.Fatal(err)
				}
			},
			configure: func(_ *testing.T, client *recoveryScriptedSFTPClient, _ *recoveryLocalSFTPClient, capturedPath string) {
				boundary := filepath.Join(capturedPath, "!boundary")
				client.statVFS = func(value string, call int) (*sftp.StatVFS, error) {
					fsid := uint64(7)
					if value == boundary && call >= 2 {
						fsid = 8
					}
					return &sftp.StatVFS{Fsid: fsid, Files: 100, Favail: 20, Namemax: 255}, nil
				}
			},
			assertKeep: func(t *testing.T, jobPath string) {
				if _, err := os.Stat(filepath.Join(jobPath, "!boundary")); err != nil {
					t.Fatalf("filesystem-drift directory error=%v, want preserved", err)
				}
			},
		},
		{
			name: "canonical escape",
			build: func(t *testing.T, jobPath string) {
				boundary := filepath.Join(jobPath, "!boundary")
				if err := os.Mkdir(boundary, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(boundary, "leaf"), []byte("keep"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			configure: func(t *testing.T, client *recoveryScriptedSFTPClient, _ *recoveryLocalSFTPClient, capturedPath string) {
				boundary := filepath.Join(capturedPath, "!boundary")
				external := t.TempDir()
				client.realPath = func(value string, _ int) (string, error) {
					if value == boundary {
						return external, nil
					}
					return filepath.EvalSymlinks(value)
				}
			},
			assertKeep: func(t *testing.T, jobPath string) {
				if _, err := os.Stat(filepath.Join(jobPath, "!boundary", "leaf")); err != nil {
					t.Fatalf("canonical-escape leaf error=%v, want preserved", err)
				}
			},
		},
		{
			name: "maximum depth",
			build: func(t *testing.T, jobPath string) {
				current := jobPath
				for depth := 0; depth <= recoveryCleanupMaxDepth; depth++ {
					current = filepath.Join(current, fmt.Sprintf("!depth-%02d", depth))
					if err := os.Mkdir(current, 0o700); err != nil {
						t.Fatalf("create depth %d: %v", depth, err)
					}
				}
				if err := os.WriteFile(filepath.Join(current, "boundary-leaf"), []byte("keep"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			configure: func(_ *testing.T, _ *recoveryScriptedSFTPClient, _ *recoveryLocalSFTPClient, _ string) {},
			assertKeep: func(t *testing.T, jobPath string) {
				current := jobPath
				for depth := 0; depth <= recoveryCleanupMaxDepth; depth++ {
					current = filepath.Join(current, fmt.Sprintf("!depth-%02d", depth))
				}
				if _, err := os.Stat(filepath.Join(current, "boundary-leaf")); err != nil {
					t.Fatalf("depth-boundary leaf error=%v, want preserved", err)
				}
			},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newRecoveryLocalSFTPTargetFixture(t)
			fixture.create(t)
			jobsPath, jobPath, markerPath := fixture.paths()
			testCase.build(t, jobPath)
			permit := fixture.cleanupPermit
			permit.Operation = TargetCleanupRemoveOwnedJobDir
			permit.proof = nil
			liveCalls := 0
			permit = issueTargetCleanupPermitWithLiveValidator(
				permit, func(context.Context, TargetCleanupPermit) error {
					liveCalls++
					return nil
				}, fixture.binding,
			)
			request := RemoveOwnedJobDirRequest{
				Object: fixture.cleanupRequest.Object, MarkerBindingDigest: fixture.cleanupRequest.MarkerBindingDigest,
			}
			markerBytes, err := os.ReadFile(markerPath)
			if err != nil {
				t.Fatalf("read owner marker: %v", err)
			}
			markerDocument, err := decodeRecoveryWorkspaceMarkerDocument(markerBytes)
			if err != nil {
				t.Fatalf("decode owner marker: %v", err)
			}
			material := fixture.material
			artifacts, err := deriveRecoveryOwnedCleanupArtifactBinding(material, permit, markerDocument, markerBytes)
			if err != nil {
				t.Fatalf("derive cleanup artifacts: %v", err)
			}
			capturedPath := filepath.Join(jobsPath, artifacts.capturedComponent)
			first, err := fixture.target.RemoveOwnedJobDir(context.Background(), permit, request)
			if err != nil || first.Complete || first.RemovedEntries != 0 {
				t.Fatalf("capture=%+v error=%v, want incomplete zero-remove capture", first, err)
			}
			base := &recoveryLocalSFTPClient{}
			client := &recoveryScriptedSFTPClient{base: base}
			testCase.configure(t, client, base, capturedPath)
			second, err := fixture.targetWithClient(client).RemoveOwnedJobDir(context.Background(), permit, request)
			if !errors.Is(err, ErrRecoveryTargetChanged) || second != (OwnedJobDirRemoval{}) {
				t.Fatalf("boundary cleanup=%+v error=%v, want changed with no result", second, err)
			}
			if base.removeCalls != 0 || base.removeDirectoryCalls != 0 {
				t.Fatalf("boundary cleanup mutated target: remove=%d remove_directory=%d", base.removeCalls, base.removeDirectoryCalls)
			}
			if len(base.readDirRequests) == 0 {
				t.Fatalf("boundary cleanup did not enumerate through directory handle")
			}
			for _, n := range base.readDirRequests {
				if n != recoveryCleanupReadBatch {
					t.Fatalf("ReadDir request=%d, want bounded %d", n, recoveryCleanupReadBatch)
				}
			}
			if liveCalls < 2 {
				t.Fatalf("live validator calls=%d, want capture plus boundary validation", liveCalls)
			}
			testCase.assertKeep(t, filepath.Join(capturedPath))
		})
	}
}

func TestRecoveryOwnedCleanupRemovesAtMost256Entries(t *testing.T) {
	fixture := newRecoveryLocalSFTPTargetFixture(t)
	fixture.create(t)
	jobsPath, jobPath, markerPath := fixture.paths()
	for index := 0; index < recoveryCleanupRemoveLimit+32; index++ {
		name := fmt.Sprintf("!leaf-%03d", index)
		if err := os.WriteFile(filepath.Join(jobPath, name), []byte("leaf"), 0o600); err != nil {
			t.Fatalf("write cleanup leaf %d: %v", index, err)
		}
	}
	permit := fixture.cleanupPermit
	permit.Operation = TargetCleanupRemoveOwnedJobDir
	permit.proof = nil
	liveCalls := 0
	permit = issueTargetCleanupPermitWithLiveValidator(
		permit, func(context.Context, TargetCleanupPermit) error {
			liveCalls++
			return nil
		}, fixture.binding,
	)
	request := RemoveOwnedJobDirRequest{
		Object: fixture.cleanupRequest.Object, MarkerBindingDigest: fixture.cleanupRequest.MarkerBindingDigest,
	}
	markerBytes, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatalf("read owner marker: %v", err)
	}
	markerDocument, err := decodeRecoveryWorkspaceMarkerDocument(markerBytes)
	if err != nil {
		t.Fatalf("decode owner marker: %v", err)
	}
	artifacts, err := deriveRecoveryOwnedCleanupArtifactBinding(fixture.material, permit, markerDocument, markerBytes)
	if err != nil {
		t.Fatalf("derive cleanup artifacts: %v", err)
	}
	verifiedPath := filepath.Join(jobsPath, artifacts.verifiedComponent)
	first, err := fixture.target.RemoveOwnedJobDir(context.Background(), permit, request)
	if err != nil || first.Complete || first.RemovedEntries != 0 {
		t.Fatalf("capture=%+v error=%v, want incomplete zero-remove capture", first, err)
	}
	base := &recoveryLocalSFTPClient{}
	second, err := fixture.targetWithClient(&recoveryScriptedSFTPClient{base: base}).RemoveOwnedJobDir(
		context.Background(), permit, request,
	)
	if err != nil || second.Complete || second.RemovedEntries != recoveryCleanupRemoveLimit || !validDigest(second.ProgressDigest) {
		t.Fatalf("bounded cleanup=%+v error=%v, want incomplete %d-remove pass", second, err, recoveryCleanupRemoveLimit)
	}
	if liveCalls != recoveryCleanupRemoveLimit+2 {
		t.Fatalf("live validator calls=%d, want capture/marker plus %d removes", liveCalls, recoveryCleanupRemoveLimit)
	}
	if base.removeCalls != recoveryCleanupRemoveLimit || base.removeDirectoryCalls != 0 {
		t.Fatalf("bounded cleanup mutations remove=%d remove_directory=%d, want %d/0",
			base.removeCalls, base.removeDirectoryCalls, recoveryCleanupRemoveLimit)
	}
	for _, value := range base.removePaths {
		if value == verifiedPath {
			t.Fatalf("bounded cleanup removed external verified marker before budget was exhausted")
		}
	}
	if _, statErr := os.Lstat(verifiedPath); statErr != nil {
		t.Fatalf("verified marker stat=%v, want preserved", statErr)
	}
	if len(base.readDirRequests) < 4 {
		t.Fatalf("ReadDir pages=%d, want pagination across >256 entries", len(base.readDirRequests))
	}
	for _, n := range base.readDirRequests {
		if n != recoveryCleanupReadBatch {
			t.Fatalf("ReadDir request=%d, want bounded %d", n, recoveryCleanupReadBatch)
		}
	}
	if digest := recoveryCleanupProgressDigest(permit, recoveryCleanupRemoveLimit, false); second.ProgressDigest != digest {
		t.Fatalf("progress digest=%q, want closed deterministic digest %q", second.ProgressDigest, digest)
	}
}

func appendRecoverySFTPTestUint32(value []byte, field uint32) []byte {
	return append(value, byte(field>>24), byte(field>>16), byte(field>>8), byte(field))
}

func appendRecoverySFTPTestUint64(value []byte, field uint64) []byte {
	value = appendRecoverySFTPTestUint32(value, uint32(field>>32))
	return appendRecoverySFTPTestUint32(value, uint32(field))
}

func appendRecoverySFTPTestString(value []byte, field string) []byte {
	value = appendRecoverySFTPTestUint32(value, uint32(len(field)))
	return append(value, field...)
}

func appendRecoverySFTPTestName(
	value []byte,
	name string,
	mode uint32,
	size uint64,
) []byte {
	value = appendRecoverySFTPTestString(value, name)
	value = appendRecoverySFTPTestString(value, "test-long-name")
	value = appendRecoverySFTPTestUint32(value, 0x0f)
	value = appendRecoverySFTPTestUint64(value, size)
	value = appendRecoverySFTPTestUint32(value, 1000)
	value = appendRecoverySFTPTestUint32(value, 1001)
	value = appendRecoverySFTPTestUint32(value, mode)
	value = appendRecoverySFTPTestUint32(value, 1_700_000_000)
	return appendRecoverySFTPTestUint32(value, 1_700_000_001)
}

func TestRecoveryBoundedSFTPDirectoryNamePacketMatrix(t *testing.T) {
	t.Run("valid closed kinds and dot filtering", func(t *testing.T) {
		payload := appendRecoverySFTPTestUint32(nil, 17)
		payload = appendRecoverySFTPTestUint32(payload, 6)
		payload = appendRecoverySFTPTestName(payload, ".", syscall.S_IFDIR|0o700, 0)
		payload = appendRecoverySFTPTestName(payload, "..", syscall.S_IFDIR|0o700, 0)
		payload = appendRecoverySFTPTestName(payload, "regular", syscall.S_IFREG|0o600, 23)
		payload = appendRecoverySFTPTestName(payload, "directory", syscall.S_IFDIR|0o700, 0)
		payload = appendRecoverySFTPTestName(payload, "symlink", syscall.S_IFLNK|0o777, 9)
		payload = appendRecoverySFTPTestName(payload, "fifo", syscall.S_IFIFO|0o600, 0)

		entries, truncated, err := decodeRecoverySFTPDirectoryNamePacket(payload, 17)
		if err != nil || truncated || len(entries) != 4 {
			t.Fatalf("decode entries=%v truncated=%v error=%v, want four exact entries", entries, truncated, err)
		}
		wantNames := []string{"regular", "directory", "symlink", "fifo"}
		for index, entry := range entries {
			if entry.Name() != wantNames[index] {
				t.Fatalf("entry %d name=%q, want %q", index, entry.Name(), wantNames[index])
			}
		}
		if entries[0].Size() != 23 || !entries[0].Mode().IsRegular() || !entries[1].IsDir() ||
			entries[2].Mode()&os.ModeSymlink == 0 || entries[3].Mode()&os.ModeNamedPipe == 0 {
			t.Fatalf("decoded entry kinds=%v, want regular/directory/symlink/fifo", entries)
		}
		if entries[0].ModTime().Unix() != 1_700_000_001 {
			t.Fatalf("decoded modtime=%v, want exact wire mtime", entries[0].ModTime())
		}
	})

	t.Run("retained entry bound", func(t *testing.T) {
		payload := appendRecoverySFTPTestUint32(nil, 19)
		payload = appendRecoverySFTPTestUint32(payload, recoveryCleanupRemoveLimit+2)
		for index := 0; index < recoveryCleanupRemoveLimit+2; index++ {
			payload = appendRecoverySFTPTestName(
				payload, fmt.Sprintf("entry-%03d", index), syscall.S_IFREG|0o600, uint64(index),
			)
		}
		entries, truncated, err := decodeRecoverySFTPDirectoryNamePacket(payload, 19)
		if err != nil || !truncated || len(entries) != recoveryCleanupRemoveLimit+1 {
			t.Fatalf("bounded decode len=%d truncated=%v error=%v, want %d/true",
				len(entries), truncated, err, recoveryCleanupRemoveLimit+1)
		}
		if entries[0].Name() != "entry-000" || entries[len(entries)-1].Name() != "entry-256" {
			t.Fatalf("bounded retained names first=%q last=%q", entries[0].Name(), entries[len(entries)-1].Name())
		}
	})

	t.Run("wrong response id", func(t *testing.T) {
		payload := appendRecoverySFTPTestUint32(nil, 23)
		payload = appendRecoverySFTPTestUint32(payload, 0)
		entries, truncated, err := decodeRecoverySFTPDirectoryNamePacket(payload, 24)
		if entries != nil || truncated || !errors.Is(err, ErrRecoveryTargetUnavailable) {
			t.Fatalf("wrong-id decode entries=%v truncated=%v error=%v, want sanitized unavailable", entries, truncated, err)
		}
	})

	t.Run("malformed attributes", func(t *testing.T) {
		payload := appendRecoverySFTPTestUint32(nil, 29)
		payload = appendRecoverySFTPTestUint32(payload, 1)
		payload = appendRecoverySFTPTestString(payload, "broken")
		payload = appendRecoverySFTPTestString(payload, "broken-long")
		payload = appendRecoverySFTPTestUint32(payload, 0x0f)
		payload = appendRecoverySFTPTestUint64(payload, 1)
		entries, truncated, err := decodeRecoverySFTPDirectoryNamePacket(payload, 29)
		if entries != nil || truncated || !errors.Is(err, ErrRecoveryTargetUnavailable) {
			t.Fatalf("malformed decode entries=%v truncated=%v error=%v, want sanitized unavailable", entries, truncated, err)
		}
	})
}

type recoverySFTPDirectoryTransportForTest struct {
	reader     *bytes.Reader
	writes     bytes.Buffer
	closeCalls int
}

func (transport *recoverySFTPDirectoryTransportForTest) Read(value []byte) (int, error) {
	return transport.reader.Read(value)
}

func (transport *recoverySFTPDirectoryTransportForTest) Write(value []byte) (int, error) {
	return transport.writes.Write(value)
}

func (transport *recoverySFTPDirectoryTransportForTest) Close() error {
	transport.closeCalls++
	return nil
}

func recoverySFTPTestPacket(packetType byte, payload []byte) []byte {
	packet := appendRecoverySFTPTestUint32(nil, uint32(len(payload)+1))
	packet = append(packet, packetType)
	return append(packet, payload...)
}

func recoverySFTPTestStatusPacket(id, code uint32, message string) []byte {
	payload := appendRecoverySFTPTestUint32(nil, id)
	payload = appendRecoverySFTPTestUint32(payload, code)
	payload = appendRecoverySFTPTestString(payload, message)
	payload = appendRecoverySFTPTestString(payload, "")
	return recoverySFTPTestPacket(101, payload)
}

func recoverySFTPTestPacketTypes(t *testing.T, value []byte) []byte {
	t.Helper()
	types := make([]byte, 0, 8)
	for len(value) > 0 {
		if len(value) < 5 {
			t.Fatalf("short written packet header: %x", value)
		}
		length := int(binary.BigEndian.Uint32(value[:4]))
		if length < 1 || length > len(value)-4 {
			t.Fatalf("invalid written packet length=%d remaining=%d", length, len(value)-4)
		}
		types = append(types, value[4])
		value = value[4+length:]
	}
	return types
}

func TestRecoveryBoundedSFTPDirectoryProtocolPagesAndCloses(t *testing.T) {
	version := recoverySFTPTestPacket(2, appendRecoverySFTPTestUint32(nil, 3))
	handlePayload := appendRecoverySFTPTestUint32(nil, 1)
	handlePayload = appendRecoverySFTPTestString(handlePayload, "directory-handle")
	handle := recoverySFTPTestPacket(102, handlePayload)
	namePayload := appendRecoverySFTPTestUint32(nil, 2)
	namePayload = appendRecoverySFTPTestUint32(namePayload, 70)
	for index := 0; index < 70; index++ {
		namePayload = appendRecoverySFTPTestName(
			namePayload, fmt.Sprintf("entry-%02d", index), syscall.S_IFREG|0o600, uint64(index),
		)
	}
	names := recoverySFTPTestPacket(104, namePayload)
	responses := append(append(append(append([]byte{}, version...), handle...), names...),
		recoverySFTPTestStatusPacket(3, 1, "eof")...)
	responses = append(responses, recoverySFTPTestStatusPacket(4, 0, "ok")...)
	transport := &recoverySFTPDirectoryTransportForTest{reader: bytes.NewReader(responses)}
	directory, err := newRecoveryBoundedSFTPDirectory(transport, "/owned/captured")
	if err != nil {
		t.Fatalf("open bounded directory: %v", err)
	}
	first, err := directory.ReadDir(recoveryCleanupReadBatch)
	if err != nil || len(first) != recoveryCleanupReadBatch || first[0].Name() != "entry-00" || first[63].Name() != "entry-63" {
		t.Fatalf("first page len=%d error=%v first=%v last=%v", len(first), err, first[0], first[len(first)-1])
	}
	second, err := directory.ReadDir(recoveryCleanupReadBatch)
	if err != nil || len(second) != 6 || second[0].Name() != "entry-64" || second[5].Name() != "entry-69" {
		t.Fatalf("second page len=%d error=%v entries=%v", len(second), err, second)
	}
	third, err := directory.ReadDir(recoveryCleanupReadBatch)
	if third != nil || !errors.Is(err, io.EOF) {
		t.Fatalf("EOF page=%v error=%v, want io.EOF", third, err)
	}
	if err := directory.Close(); err != nil {
		t.Fatalf("close bounded directory: %v", err)
	}
	if err := directory.Close(); err != nil {
		t.Fatalf("idempotent bounded directory close: %v", err)
	}
	if transport.closeCalls != 1 {
		t.Fatalf("transport close calls=%d, want one", transport.closeCalls)
	}
	if got, want := recoverySFTPTestPacketTypes(t, transport.writes.Bytes()), []byte{1, 11, 12, 12, 4}; !reflect.DeepEqual(got, want) {
		t.Fatalf("wire packet types=%v, want %v", got, want)
	}
}

func TestRecoveryBoundedSFTPDirectorySessionSharesOneTransport(t *testing.T) {
	responses := recoverySFTPTestPacket(2, appendRecoverySFTPTestUint32(nil, 3))
	firstHandle := appendRecoverySFTPTestUint32(nil, 1)
	firstHandle = appendRecoverySFTPTestString(firstHandle, "first-handle")
	responses = append(responses, recoverySFTPTestPacket(102, firstHandle)...)
	secondHandle := appendRecoverySFTPTestUint32(nil, 2)
	secondHandle = appendRecoverySFTPTestString(secondHandle, "second-handle")
	responses = append(responses, recoverySFTPTestPacket(102, secondHandle)...)
	responses = append(responses, recoverySFTPTestStatusPacket(3, 0, "ok")...)
	responses = append(responses, recoverySFTPTestStatusPacket(4, 0, "ok")...)
	transport := &recoverySFTPDirectoryTransportForTest{reader: bytes.NewReader(responses)}
	session, err := newRecoveryBoundedSFTPDirectorySession(transport)
	if err != nil {
		t.Fatalf("open bounded directory session: %v", err)
	}
	first, err := session.OpenDirectory("/owned/captured")
	if err != nil {
		t.Fatalf("open first directory handle: %v", err)
	}
	second, err := session.OpenDirectory("/owned/captured/nested")
	if err != nil {
		t.Fatalf("open second directory handle: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("close second directory handle: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first directory handle: %v", err)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("close shared bounded directory session: %v", err)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("idempotent shared bounded directory session close: %v", err)
	}
	if transport.closeCalls != 1 {
		t.Fatalf("shared transport close calls=%d, want one", transport.closeCalls)
	}
	if got, want := recoverySFTPTestPacketTypes(t, transport.writes.Bytes()), []byte{1, 11, 11, 4, 4}; !reflect.DeepEqual(got, want) {
		t.Fatalf("shared wire packet types=%v, want %v", got, want)
	}
}

func TestRecoveryBoundedSFTPDirectoryInteroperatesWithPkgSFTPServer(t *testing.T) {
	root := t.TempDir()
	const entryCount = 130
	for index := 0; index < entryCount; index++ {
		if err := os.WriteFile(
			filepath.Join(root, fmt.Sprintf("entry-%03d", index)), []byte("value"), 0o600,
		); err != nil {
			t.Fatalf("write interoperability entry %d: %v", index, err)
		}
	}
	clientConnection, serverConnection := net.Pipe()
	server, err := sftp.NewServer(serverConnection)
	if err != nil {
		_ = clientConnection.Close()
		_ = serverConnection.Close()
		t.Fatalf("create interoperability SFTP server: %v", err)
	}
	done := make(chan struct{})
	go func() {
		_ = server.Serve()
		close(done)
	}()
	session, err := newRecoveryBoundedSFTPDirectorySession(clientConnection)
	if err != nil {
		_ = server.Close()
		t.Fatalf("open interoperability directory session: %v", err)
	}
	directory, err := session.OpenDirectory(root)
	if err != nil {
		_ = session.Close()
		_ = server.Close()
		t.Fatalf("open interoperability directory: %v", err)
	}
	seen := make(map[string]struct{}, entryCount)
	for {
		entries, readErr := directory.ReadDir(recoveryCleanupReadBatch)
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			t.Fatalf("read interoperability directory: %v", readErr)
		}
		if len(entries) == 0 || len(entries) > recoveryCleanupReadBatch {
			t.Fatalf("interoperability page length=%d, want 1..%d", len(entries), recoveryCleanupReadBatch)
		}
		for _, entry := range entries {
			seen[entry.Name()] = struct{}{}
		}
	}
	if len(seen) != entryCount {
		t.Fatalf("interoperability entries=%d, want %d", len(seen), entryCount)
	}
	if err := directory.Close(); err != nil {
		t.Fatalf("close interoperability directory: %v", err)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("close interoperability session: %v", err)
	}
	_ = server.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("interoperability SFTP server did not stop")
	}
}

func TestRecoveryBoundedSFTPDirectoryRejectsOversizeAndRawStatus(t *testing.T) {
	openResponses := func() []byte {
		version := recoverySFTPTestPacket(2, appendRecoverySFTPTestUint32(nil, 3))
		handlePayload := appendRecoverySFTPTestUint32(nil, 1)
		handlePayload = appendRecoverySFTPTestString(handlePayload, "directory-handle")
		return append(version, recoverySFTPTestPacket(102, handlePayload)...)
	}

	t.Run("oversize packet", func(t *testing.T) {
		responses := openResponses()
		responses = appendRecoverySFTPTestUint32(responses, recoverySFTPDirectoryPacketMaxBytes+1)
		transport := &recoverySFTPDirectoryTransportForTest{reader: bytes.NewReader(responses)}
		directory, err := newRecoveryBoundedSFTPDirectory(transport, "/owned/captured")
		if err != nil {
			t.Fatalf("open bounded directory: %v", err)
		}
		entries, readErr := directory.ReadDir(recoveryCleanupReadBatch)
		if entries != nil || !errors.Is(readErr, ErrRecoveryTargetUnavailable) {
			t.Fatalf("oversize read entries=%v error=%v, want sanitized unavailable", entries, readErr)
		}
		if closeErr := directory.Close(); !errors.Is(closeErr, ErrRecoveryTargetUnavailable) {
			t.Fatalf("oversize close error=%v, want unavailable", closeErr)
		}
		if transport.closeCalls != 1 {
			t.Fatalf("oversize transport close calls=%d, want one", transport.closeCalls)
		}
	})

	t.Run("raw status", func(t *testing.T) {
		const raw = "RAW_SFTP_DIRECTORY_STATUS_FOR_TEST_ONLY"
		responses := append(openResponses(), recoverySFTPTestStatusPacket(2, 4, raw)...)
		transport := &recoverySFTPDirectoryTransportForTest{reader: bytes.NewReader(responses)}
		directory, err := newRecoveryBoundedSFTPDirectory(transport, "/owned/captured")
		if err != nil {
			t.Fatalf("open bounded directory: %v", err)
		}
		entries, readErr := directory.ReadDir(recoveryCleanupReadBatch)
		if entries != nil || !errors.Is(readErr, ErrRecoveryTargetUnavailable) || strings.Contains(readErr.Error(), raw) {
			t.Fatalf("status read entries=%v error=%v, want redacted unavailable", entries, readErr)
		}
		if closeErr := directory.Close(); !errors.Is(closeErr, ErrRecoveryTargetUnavailable) {
			t.Fatalf("status close error=%v, want unavailable", closeErr)
		}
		if transport.closeCalls != 1 {
			t.Fatalf("status transport close calls=%d, want one", transport.closeCalls)
		}
	})
}

type recoverySFTPFileHandleForTest struct {
	closeCalls int
}

func (*recoverySFTPFileHandleForTest) Read([]byte) (int, error)        { return 0, io.EOF }
func (*recoverySFTPFileHandleForTest) Write(value []byte) (int, error) { return len(value), nil }
func (*recoverySFTPFileHandleForTest) Stat() (os.FileInfo, error) {
	return recoveryProbeFileInfo{name: "directory", mode: os.ModeDir | 0o700}, nil
}
func (*recoverySFTPFileHandleForTest) Sync() error { return nil }
func (handle *recoverySFTPFileHandleForTest) Close() error {
	handle.closeCalls++
	return nil
}

type recoverySFTPDirectoryReaderForTest struct {
	pages      [][]os.FileInfo
	readCalls  []int
	closeCalls int
}

func (reader *recoverySFTPDirectoryReaderForTest) ReadDir(n int) ([]os.FileInfo, error) {
	reader.readCalls = append(reader.readCalls, n)
	if len(reader.pages) == 0 {
		return nil, io.EOF
	}
	page := reader.pages[0]
	reader.pages = reader.pages[1:]
	return append([]os.FileInfo(nil), page...), nil
}

func (reader *recoverySFTPDirectoryReaderForTest) Close() error {
	reader.closeCalls++
	return nil
}

func TestRecoverySFTPFileReadDirUsesBoundedPager(t *testing.T) {
	handle := &recoverySFTPFileHandleForTest{}
	reader := &recoverySFTPDirectoryReaderForTest{pages: [][]os.FileInfo{
		{recoveryProbeFileInfo{name: "first", mode: 0o600}},
		{recoveryProbeFileInfo{name: "second", mode: 0o600}},
	}}
	openCalls := 0
	file := &recoverySFTPFile{
		file: handle,
		path: "/owned/captured",
		openDirectory: func(value string) (recoverySFTPDirectoryReader, error) {
			openCalls++
			if value != "/owned/captured" {
				t.Fatalf("bounded pager path=%q, want exact owned directory", value)
			}
			return reader, nil
		},
	}
	first, err := file.ReadDir(recoveryCleanupReadBatch)
	if err != nil || len(first) != 1 || first[0].Name() != "first" {
		t.Fatalf("first bounded wrapper page=%v error=%v", first, err)
	}
	second, err := file.ReadDir(recoveryCleanupReadBatch)
	if err != nil || len(second) != 1 || second[0].Name() != "second" {
		t.Fatalf("second bounded wrapper page=%v error=%v", second, err)
	}
	if openCalls != 1 || !reflect.DeepEqual(reader.readCalls, []int{recoveryCleanupReadBatch, recoveryCleanupReadBatch}) {
		t.Fatalf("bounded pager opens=%d read calls=%v, want one exact pager", openCalls, reader.readCalls)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close bounded wrapper: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("idempotent bounded wrapper close: %v", err)
	}
	if reader.closeCalls != 1 || handle.closeCalls != 1 {
		t.Fatalf("bounded wrapper close pager/handle=%d/%d, want one/one", reader.closeCalls, handle.closeCalls)
	}
}

func TestRecoverySFTPTargetHasNoUnboundedDirectoryRead(t *testing.T) {
	source, err := os.ReadFile("target.go")
	if err != nil {
		t.Fatalf("read target source: %v", err)
	}
	for _, forbidden := range []string{
		"file.client.ReadDir(", "file.client.ReadDirContext(",
		"client.client.ReadDir(", "client.client.ReadDirContext(",
	} {
		if bytes.Contains(source, []byte(forbidden)) {
			t.Fatalf("target source retains unbounded pkg/sftp call %q", forbidden)
		}
	}
}

type recoverySSHChannelForDirectoryTest struct {
	*recoverySFTPDirectoryTransportForTest
	requestNames    []string
	requestPayloads [][]byte
	accept          bool
	sendErr         error
	stderr          bytes.Buffer
}

func (*recoverySSHChannelForDirectoryTest) CloseWrite() error { return nil }

func (channel *recoverySSHChannelForDirectoryTest) SendRequest(
	name string,
	_ bool,
	payload []byte,
) (bool, error) {
	channel.requestNames = append(channel.requestNames, name)
	channel.requestPayloads = append(channel.requestPayloads, append([]byte(nil), payload...))
	return channel.accept, channel.sendErr
}

func (channel *recoverySSHChannelForDirectoryTest) Stderr() io.ReadWriter {
	return &channel.stderr
}

type recoverySSHChannelOpenerForDirectoryTest struct {
	channel      ssh.Channel
	requests     <-chan *ssh.Request
	channelTypes []string
	extraData    [][]byte
	err          error
}

func (opener *recoverySSHChannelOpenerForDirectoryTest) OpenChannel(
	channelType string,
	extra []byte,
) (ssh.Channel, <-chan *ssh.Request, error) {
	opener.channelTypes = append(opener.channelTypes, channelType)
	opener.extraData = append(opener.extraData, append([]byte(nil), extra...))
	return opener.channel, opener.requests, opener.err
}

func TestRecoveryBoundedSFTPDirectoryOpensDedicatedSubsystem(t *testing.T) {
	version := recoverySFTPTestPacket(2, appendRecoverySFTPTestUint32(nil, 3))
	handlePayload := appendRecoverySFTPTestUint32(nil, 1)
	handlePayload = appendRecoverySFTPTestString(handlePayload, "directory-handle")
	responses := append(version, recoverySFTPTestPacket(102, handlePayload)...)
	responses = append(responses, recoverySFTPTestStatusPacket(2, 0, "ok")...)
	transport := &recoverySFTPDirectoryTransportForTest{reader: bytes.NewReader(responses)}
	channel := &recoverySSHChannelForDirectoryTest{
		recoverySFTPDirectoryTransportForTest: transport,
		accept:                                true,
	}
	requests := make(chan *ssh.Request)
	close(requests)
	opener := &recoverySSHChannelOpenerForDirectoryTest{channel: channel, requests: requests}
	session, err := openRecoveryBoundedSFTPDirectorySession(opener)
	if err != nil {
		t.Fatalf("open dedicated bounded subsystem: %v", err)
	}
	directory, err := session.OpenDirectory("/owned/captured")
	if err != nil {
		t.Fatalf("open directory on dedicated bounded subsystem: %v", err)
	}
	if !reflect.DeepEqual(opener.channelTypes, []string{"session"}) || len(opener.extraData) != 1 || len(opener.extraData[0]) != 0 {
		t.Fatalf("opened channels=%v extra=%v, want one empty-data session", opener.channelTypes, opener.extraData)
	}
	if !reflect.DeepEqual(channel.requestNames, []string{"subsystem"}) || len(channel.requestPayloads) != 1 {
		t.Fatalf("channel requests=%v payloads=%d, want one subsystem", channel.requestNames, len(channel.requestPayloads))
	}
	var subsystem struct{ Name string }
	if err := ssh.Unmarshal(channel.requestPayloads[0], &subsystem); err != nil || subsystem.Name != "sftp" {
		t.Fatalf("subsystem payload=%q error=%v, want sftp", subsystem.Name, err)
	}
	if err := directory.Close(); err != nil {
		t.Fatalf("close dedicated bounded subsystem: %v", err)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("close dedicated bounded session: %v", err)
	}
	if transport.closeCalls != 1 {
		t.Fatalf("dedicated channel close calls=%d, want one", transport.closeCalls)
	}

	t.Run("denied subsystem", func(t *testing.T) {
		deniedTransport := &recoverySFTPDirectoryTransportForTest{reader: bytes.NewReader(nil)}
		deniedChannel := &recoverySSHChannelForDirectoryTest{
			recoverySFTPDirectoryTransportForTest: deniedTransport,
			accept:                                false,
		}
		deniedRequests := make(chan *ssh.Request)
		close(deniedRequests)
		deniedOpener := &recoverySSHChannelOpenerForDirectoryTest{channel: deniedChannel, requests: deniedRequests}
		opened, openErr := openRecoveryBoundedSFTPDirectorySession(deniedOpener)
		if opened != nil || !errors.Is(openErr, ErrRecoveryTargetUnavailable) {
			t.Fatalf("denied subsystem opened=%v error=%v, want sanitized unavailable", opened, openErr)
		}
		if deniedTransport.closeCalls != 1 {
			t.Fatalf("denied subsystem close calls=%d, want one", deniedTransport.closeCalls)
		}
	})
}

func TestRecoverySFTPTargetObservationRevisionIsExact(t *testing.T) {
	binding := recoveryTargetSessionBindingForTest(t)
	privateLocator := "jobs/" + strings.Repeat("1", 32)
	marker := []byte("authenticated-marker-bytes-for-observation")
	markerSum := sha256.Sum256(marker)
	want := framedDigest(
		recoveryWorkspaceObservationDomain,
		strconv.FormatUint(uint64(binding.NodeID), 10), binding.RootID,
		binding.RootLocatorDigest, binding.RootRevision, privateLocator,
		"0700", "0600", strconv.Itoa(len(marker)), hex.EncodeToString(markerSum[:]),
	)
	got := recoveryOwnedWorkspaceObservationRevision(binding, privateLocator, marker)
	if got != want || !validDigest(got) {
		t.Fatalf("observation revision=%q, want exact %q", got, want)
	}

	mutations := []struct {
		name    string
		binding recoveryTargetSessionBinding
		locator string
		marker  []byte
	}{
		{name: "node", binding: binding, locator: privateLocator, marker: marker},
		{name: "root id", binding: binding, locator: privateLocator, marker: marker},
		{name: "root locator digest", binding: binding, locator: privateLocator, marker: marker},
		{name: "root revision", binding: binding, locator: privateLocator, marker: marker},
		{name: "private locator", binding: binding, locator: privateLocator + "-changed", marker: marker},
		{name: "marker", binding: binding, locator: privateLocator, marker: append(append([]byte(nil), marker...), 'x')},
	}
	mutations[0].binding.NodeID++
	mutations[1].binding.RootID += "-changed"
	mutations[2].binding.RootLocatorDigest = strings.Repeat("b", sha256DigestLength)
	mutations[3].binding.RootRevision += "-changed"
	for _, test := range mutations {
		t.Run(test.name, func(t *testing.T) {
			if changed := recoveryOwnedWorkspaceObservationRevision(
				test.binding, test.locator, test.marker,
			); changed == got {
				t.Fatalf("substitution retained observation revision %q", changed)
			}
		})
	}
}

func TestRecoverySFTPTargetValidateOwnedJobDirRejectsObservationDrift(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, string, string, string, []byte)
		want   error
	}{
		{name: "root canonical alias", want: ErrRecoveryTargetChanged, mutate: func(t *testing.T, jobs, _, _ string, _ []byte) {
			root := filepath.Dir(jobs)
			realRoot := root + "-real"
			if err := os.Rename(root, realRoot); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Base(realRoot), root); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "missing jobs", want: ErrRecoveryTargetChanged, mutate: func(t *testing.T, jobs, job, marker string, _ []byte) {
			if err := os.Remove(marker); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(job); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(jobs); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "wrong jobs mode", want: ErrRecoveryTargetChanged, mutate: func(t *testing.T, jobs, _, _ string, _ []byte) {
			if err := os.Chmod(jobs, 0o755); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "job symlink", want: ErrRecoveryTargetChanged, mutate: func(t *testing.T, _, job, _ string, _ []byte) {
			realJob := job + "-real"
			if err := os.Rename(job, realJob); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Base(realJob), job); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "job replaced by file", want: ErrRecoveryTargetChanged, mutate: func(t *testing.T, _, job, marker string, _ []byte) {
			if err := os.Remove(marker); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(job); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(job, []byte("replacement"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "missing marker", want: ErrRecoveryTargetChanged, mutate: func(t *testing.T, _, _, marker string, _ []byte) {
			if err := os.Remove(marker); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "marker symlink", want: ErrRecoveryTargetChanged, mutate: func(t *testing.T, _, _, marker string, _ []byte) {
			realMarker := marker + "-real"
			if err := os.Rename(marker, realMarker); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Base(realMarker), marker); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "marker directory", want: ErrRecoveryTargetChanged, mutate: func(t *testing.T, _, _, marker string, _ []byte) {
			if err := os.Remove(marker); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(marker, 0o700); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "marker special file", want: ErrRecoveryTargetChanged, mutate: func(t *testing.T, _, _, marker string, _ []byte) {
			if err := os.Remove(marker); err != nil {
				t.Fatal(err)
			}
			if err := syscall.Mkfifo(marker, 0o600); err != nil {
				t.Fatalf("create marker FIFO: %v", err)
			}
		}},
		{name: "wrong marker mode", want: ErrRecoveryTargetChanged, mutate: func(t *testing.T, _, _, marker string, _ []byte) {
			if err := os.Chmod(marker, 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "empty marker", want: ErrRecoveryTargetChanged, mutate: func(t *testing.T, _, _, marker string, _ []byte) {
			if err := os.WriteFile(marker, nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "oversized marker", want: ErrRecoveryTargetChanged, mutate: func(t *testing.T, _, _, marker string, _ []byte) {
			if err := os.WriteFile(marker, bytes.Repeat([]byte{'x'}, recoveryWorkspaceMarkerDocumentMaxBytes+1), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "unauthenticated marker", want: ErrInvalidRecoveryWorkspaceMarker, mutate: func(t *testing.T, _, _, marker string, encoded []byte) {
			var document map[string]any
			if err := json.Unmarshal(encoded, &document); err != nil {
				t.Fatal(err)
			}
			tag, ok := document["authentication_tag"].(string)
			if !ok || tag == "" {
				t.Fatalf("marker authentication tag=%v", document["authentication_tag"])
			}
			if tag[0] == 'A' {
				tag = "B" + tag[1:]
			} else {
				tag = "A" + tag[1:]
			}
			document["authentication_tag"] = tag
			changed, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(marker, changed, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRecoveryLocalSFTPTargetFixture(t)
			fixture.create(t)
			jobsPath, jobPath, markerPath := fixture.paths()
			marker, err := os.ReadFile(markerPath)
			if err != nil {
				t.Fatalf("read marker before drift: %v", err)
			}
			test.mutate(t, jobsPath, jobPath, markerPath, marker)

			_, err = fixture.target.ValidateOwnedJobDir(
				context.Background(), fixture.cleanupPermit, fixture.cleanupRequest,
			)
			if !errors.Is(err, test.want) {
				t.Fatalf("validation error=%v, want %v", err, test.want)
			}
			if len(fixture.clients) != 2 {
				t.Fatalf("opened clients=%d, want create plus validation", len(fixture.clients))
			}
			client := fixture.clients[1]
			if client.mkdirCalls != 0 || client.chmodCalls != 0 || client.openFileCalls != 0 ||
				client.renameCalls != 0 || client.removeCalls != 0 || client.syncCalls != 0 {
				t.Fatalf("failed validation mutated target: %+v", client)
			}
		})
	}

	t.Run("marker disappears before open", func(t *testing.T) {
		fixture := newRecoveryLocalSFTPTargetFixture(t)
		fixture.create(t)
		_, _, markerPath := fixture.paths()
		base := &recoveryLocalSFTPClient{}
		client := &recoveryScriptedSFTPClient{base: base}
		client.open = func(value string) (recoveryTargetSFTPFile, error) {
			if value == markerPath {
				if err := os.Remove(markerPath); err != nil {
					t.Fatalf("remove marker in open race: %v", err)
				}
				return nil, os.ErrNotExist
			}
			return base.Open(value)
		}

		_, err := fixture.validationTarget(client).ValidateOwnedJobDir(
			context.Background(), fixture.cleanupPermit, fixture.cleanupRequest,
		)
		if !errors.Is(err, ErrRecoveryTargetChanged) {
			t.Fatalf("open replacement error=%v, want ErrRecoveryTargetChanged", err)
		}
		if base.mkdirCalls != 0 || base.chmodCalls != 0 || base.openFileCalls != 0 ||
			base.renameCalls != 0 || base.removeCalls != 0 || base.syncCalls != 0 {
			t.Fatalf("open replacement validation mutated target: %+v", base)
		}
	})

	t.Run("marker disappears after read", func(t *testing.T) {
		fixture := newRecoveryLocalSFTPTargetFixture(t)
		fixture.create(t)
		_, _, markerPath := fixture.paths()
		base := &recoveryLocalSFTPClient{}
		client := &recoveryScriptedSFTPClient{base: base}
		client.lstat = func(value string, call int) (os.FileInfo, error) {
			if value == markerPath && call == 3 {
				if err := os.Remove(markerPath); err != nil {
					t.Fatalf("remove marker after read: %v", err)
				}
				return nil, os.ErrNotExist
			}
			return base.Lstat(value)
		}

		_, err := fixture.validationTarget(client).ValidateOwnedJobDir(
			context.Background(), fixture.cleanupPermit, fixture.cleanupRequest,
		)
		if !errors.Is(err, ErrRecoveryTargetChanged) {
			t.Fatalf("post-read replacement error=%v, want ErrRecoveryTargetChanged", err)
		}
		if base.mkdirCalls != 0 || base.chmodCalls != 0 || base.openFileCalls != 0 ||
			base.renameCalls != 0 || base.removeCalls != 0 || base.syncCalls != 0 {
			t.Fatalf("post-read replacement validation mutated target: %+v", base)
		}
	})

	assertScriptedDrift := func(
		t *testing.T,
		fixture *recoveryLocalSFTPTargetFixture,
		client *recoveryScriptedSFTPClient,
		base *recoveryLocalSFTPClient,
	) {
		t.Helper()
		_, err := fixture.validationTarget(client).ValidateOwnedJobDir(
			context.Background(), fixture.cleanupPermit, fixture.cleanupRequest,
		)
		if !errors.Is(err, ErrRecoveryTargetChanged) {
			t.Fatalf("dynamic observation drift error=%v, want ErrRecoveryTargetChanged", err)
		}
		if base.mkdirCalls != 0 || base.chmodCalls != 0 || base.openFileCalls != 0 ||
			base.renameCalls != 0 || base.removeCalls != 0 || base.syncCalls != 0 {
			t.Fatalf("dynamic drift validation mutated target: %+v", base)
		}
	}

	t.Run("opened file stat drift", func(t *testing.T) {
		fixture := newRecoveryLocalSFTPTargetFixture(t)
		fixture.create(t)
		_, _, markerPath := fixture.paths()
		base := &recoveryLocalSFTPClient{}
		client := &recoveryScriptedSFTPClient{base: base}
		client.open = func(value string) (recoveryTargetSFTPFile, error) {
			file, err := base.Open(value)
			if err != nil || value != markerPath {
				return file, err
			}
			return &recoveryScriptedSFTPFile{base: file, stat: func() (os.FileInfo, error) {
				info, statErr := file.Stat()
				if statErr != nil {
					return nil, statErr
				}
				changed := info.ModTime().Add(time.Second)
				return recoveryFileInfoOverride{FileInfo: info, modTime: &changed}, nil
			}}, nil
		}
		assertScriptedDrift(t, fixture, client, base)
	})

	t.Run("post read stat drift", func(t *testing.T) {
		fixture := newRecoveryLocalSFTPTargetFixture(t)
		fixture.create(t)
		_, _, markerPath := fixture.paths()
		base := &recoveryLocalSFTPClient{}
		client := &recoveryScriptedSFTPClient{base: base}
		client.lstat = func(value string, call int) (os.FileInfo, error) {
			info, err := base.Lstat(value)
			if err != nil || value != markerPath || call != 3 {
				return info, err
			}
			changed := info.ModTime().Add(time.Second)
			return recoveryFileInfoOverride{FileInfo: info, modTime: &changed}, nil
		}
		assertScriptedDrift(t, fixture, client, base)
	})

	t.Run("short read", func(t *testing.T) {
		fixture := newRecoveryLocalSFTPTargetFixture(t)
		fixture.create(t)
		_, _, markerPath := fixture.paths()
		base := &recoveryLocalSFTPClient{}
		client := &recoveryScriptedSFTPClient{base: base}
		client.open = func(value string) (recoveryTargetSFTPFile, error) {
			file, err := base.Open(value)
			if err != nil || value != markerPath {
				return file, err
			}
			return &recoveryScriptedSFTPFile{base: file, read: func(value []byte) (int, error) {
				if len(value) == 0 {
					return 0, io.EOF
				}
				read, _ := file.Read(value[:1])
				return read, io.EOF
			}}, nil
		}
		assertScriptedDrift(t, fixture, client, base)
	})

	t.Run("2049 byte bounded read", func(t *testing.T) {
		fixture := newRecoveryLocalSFTPTargetFixture(t)
		fixture.create(t)
		_, _, markerPath := fixture.paths()
		base := &recoveryLocalSFTPClient{}
		client := &recoveryScriptedSFTPClient{base: base}
		reader := bytes.NewReader(bytes.Repeat([]byte{'x'}, recoveryWorkspaceMarkerDocumentMaxBytes+1))
		maxRequest := 0
		client.open = func(value string) (recoveryTargetSFTPFile, error) {
			file, err := base.Open(value)
			if err != nil || value != markerPath {
				return file, err
			}
			return &recoveryScriptedSFTPFile{base: file, read: func(value []byte) (int, error) {
				if len(value) > maxRequest {
					maxRequest = len(value)
				}
				return reader.Read(value)
			}}, nil
		}
		assertScriptedDrift(t, fixture, client, base)
		if maxRequest <= 0 || maxRequest > recoveryWorkspaceMarkerDocumentMaxBytes+1 || reader.Len() != 0 {
			t.Fatalf("bounded reader max_request=%d remaining=%d", maxRequest, reader.Len())
		}
	})

	t.Run("post read canonical drift", func(t *testing.T) {
		fixture := newRecoveryLocalSFTPTargetFixture(t)
		fixture.create(t)
		_, _, markerPath := fixture.paths()
		base := &recoveryLocalSFTPClient{}
		client := &recoveryScriptedSFTPClient{base: base}
		client.realPath = func(value string, call int) (string, error) {
			if value == markerPath && call == 2 {
				return value + "-alias", nil
			}
			return base.RealPath(value)
		}
		assertScriptedDrift(t, fixture, client, base)
	})
}

func TestRecoverySFTPTargetCancellationAndErrorsAreClosed(t *testing.T) {
	rawFailure := errors.New("RAW_CANCELED_SFTP_STAGE_FOR_TEST_ONLY")
	tests := []struct {
		name      string
		validate  bool
		configure func(*testing.T, context.CancelFunc, *recoveryLocalSFTPTargetFixture, *recoveryScriptedSFTPClient, *recoveryLocalSFTPClient)
	}{
		{
			name: "write",
			configure: func(t *testing.T, cancel context.CancelFunc, _ *recoveryLocalSFTPTargetFixture, client *recoveryScriptedSFTPClient, base *recoveryLocalSFTPClient) {
				client.openFile = func(value string, flag int) (recoveryTargetSFTPFile, error) {
					file, err := base.OpenFile(value, flag)
					if err != nil {
						t.Fatalf("open cancellation temp: %v", err)
					}
					return &recoveryScriptedSFTPFile{base: file, write: func([]byte) (int, error) {
						cancel()
						return 0, rawFailure
					}}, nil
				}
			},
		},
		{
			name: "sync",
			configure: func(t *testing.T, cancel context.CancelFunc, _ *recoveryLocalSFTPTargetFixture, client *recoveryScriptedSFTPClient, base *recoveryLocalSFTPClient) {
				client.openFile = func(value string, flag int) (recoveryTargetSFTPFile, error) {
					file, err := base.OpenFile(value, flag)
					if err != nil {
						t.Fatalf("open cancellation sync temp: %v", err)
					}
					return &recoveryScriptedSFTPFile{base: file, sync: func() error {
						cancel()
						return rawFailure
					}}, nil
				}
			},
		},
		{
			name: "rename",
			configure: func(_ *testing.T, cancel context.CancelFunc, _ *recoveryLocalSFTPTargetFixture, client *recoveryScriptedSFTPClient, _ *recoveryLocalSFTPClient) {
				client.rename = func(string, string) error {
					cancel()
					return rawFailure
				}
			},
		},
		{
			name: "create session close",
			configure: func(_ *testing.T, cancel context.CancelFunc, _ *recoveryLocalSFTPTargetFixture, client *recoveryScriptedSFTPClient, base *recoveryLocalSFTPClient) {
				client.close = func() error {
					_ = base.Close()
					cancel()
					return rawFailure
				}
			},
		},
		{
			name: "bounded read", validate: true,
			configure: func(t *testing.T, cancel context.CancelFunc, fixture *recoveryLocalSFTPTargetFixture, client *recoveryScriptedSFTPClient, base *recoveryLocalSFTPClient) {
				_, _, markerPath := fixture.paths()
				client.open = func(value string) (recoveryTargetSFTPFile, error) {
					file, err := base.Open(value)
					if err != nil || value != markerPath {
						return file, err
					}
					return &recoveryScriptedSFTPFile{base: file, read: func([]byte) (int, error) {
						cancel()
						return 0, rawFailure
					}}, nil
				}
				_ = t
			},
		},
		{
			name: "validation session close", validate: true,
			configure: func(_ *testing.T, cancel context.CancelFunc, _ *recoveryLocalSFTPTargetFixture, client *recoveryScriptedSFTPClient, base *recoveryLocalSFTPClient) {
				client.close = func() error {
					_ = base.Close()
					cancel()
					return rawFailure
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRecoveryLocalSFTPTargetFixture(t)
			if test.validate {
				fixture.create(t)
			}
			fixture.resolver.result.Node.Host = "FAKE_PRIVATE_HOST_FOR_TEST_ONLY"
			fixture.resolver.result.Node.Username = "FAKE_PRIVATE_USERNAME_FOR_TEST_ONLY"
			base := &recoveryLocalSFTPClient{}
			client := &recoveryScriptedSFTPClient{base: base}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			test.configure(t, cancel, fixture, client, base)
			sshCloseCalls := 0
			target := newRecoverySFTPTargetForTest(
				newRecoveryTargetSessionFactoryForTest(
					fixture.resolver, &recoveryTargetNodeDialerFake{},
					func(*ssh.Client) (recoveryTargetSFTPClient, error) { return client, nil },
					func(*ssh.Client) error {
						sshCloseCalls++
						return nil
					},
				),
				fixture.codec,
			)
			var err error
			if test.validate {
				_, err = target.ValidateOwnedJobDir(ctx, fixture.cleanupPermit, fixture.cleanupRequest)
			} else {
				_, err = target.CreateOwnedJobDir(ctx, fixture.writePermit, fixture.createRequest)
			}
			if err != context.Canceled {
				t.Fatalf("cancellation error=%v, want exact context.Canceled", err)
			}
			if base.closeCalls != 1 || sshCloseCalls != 1 {
				t.Fatalf("cancellation closes: sftp=%d ssh=%d, want one each", base.closeCalls, sshCloseCalls)
			}
			for _, forbidden := range []string{
				rawFailure.Error(), fixture.binding.RootLocator,
				recoveryWorkspaceMarkerFileName, recoveryWorkspaceMarkerTempPrefix,
				fixture.resolver.result.Node.Host, fixture.resolver.result.Node.Username,
			} {
				if strings.Contains(err.Error(), forbidden) {
					t.Fatalf("cancellation error leaked %q: %v", forbidden, err)
				}
			}
		})
	}
}

func TestTargetPurposesMatchExactRecoverySSHPurposes(t *testing.T) {
	want := map[TargetPurpose]string{
		TargetPurposePreflight:  "recovery_preflight",
		TargetPurposeWrite:      "recovery_write",
		TargetPurposeVerify:     "recovery_verify",
		TargetPurposeResultRead: "recovery_result_read",
		TargetPurposeCleanup:    "recovery_cleanup",
		TargetPurposeReconcile:  "recovery_reconcile",
	}
	for purpose, exact := range want {
		if string(purpose) != exact {
			t.Fatalf("target purpose = %q, want exact SSH purpose %q", purpose, exact)
		}
	}
}

func TestTargetPortExposesOnlyClosedMethods(t *testing.T) {
	targetPort := reflect.TypeOf((*TargetPort)(nil)).Elem()
	methods := make([]string, 0, targetPort.NumMethod())
	for index := 0; index < targetPort.NumMethod(); index++ {
		methods = append(methods, targetPort.Method(index).Name)
	}
	sort.Strings(methods)
	want := []string{
		"CreateDirectory", "CreateOwnedJobDir", "Delete", "FinalizeOverwrite", "Lstat", "OpenOwnedResult",
		"ProbeRoot", "RemoveOwnedJobDir", "ValidateOwnedJobDir", "ValidateOwnedJobDirRemoved", "Verify", "WriteAtomic",
	}
	if !reflect.DeepEqual(methods, want) {
		t.Fatalf("TargetPort methods = %v, want exact closed set %v", methods, want)
	}
}

func TestTargetPortOperationPermitsArePurposeExact(t *testing.T) {
	targetPort := reflect.TypeOf((*TargetPort)(nil)).Elem()
	want := map[string]string{
		"ProbeRoot":                  "TargetPreflightPermit",
		"CreateOwnedJobDir":          "TargetWritePermit",
		"Lstat":                      "TargetVerifyPermit",
		"CreateDirectory":            "TargetWritePermit",
		"WriteAtomic":                "TargetWritePermit",
		"FinalizeOverwrite":          "TargetFinalizeOverwritePermit",
		"Delete":                     "TargetDeletePermit",
		"Verify":                     "TargetVerifyPermit",
		"ValidateOwnedJobDir":        "TargetCleanupPermit",
		"ValidateOwnedJobDirRemoved": "TargetCleanupPermit",
		"RemoveOwnedJobDir":          "TargetCleanupPermit",
		"OpenOwnedResult":            "TargetResultReadPermit",
	}
	for methodName, permitType := range want {
		method, ok := targetPort.MethodByName(methodName)
		if !ok {
			t.Fatalf("TargetPort missing method %q", methodName)
		}
		if method.Type.NumIn() < 2 {
			t.Fatalf("TargetPort.%s signature has no permit: %s", methodName, method.Type)
		}
		if got := method.Type.In(1).Name(); got != permitType {
			t.Fatalf("TargetPort.%s permit = %s, want purpose-exact %s", methodName, got, permitType)
		}
	}
}

func TestTargetPortValidateOwnedJobDirUsesClosedCleanupObservationBoundary(t *testing.T) {
	targetPort := reflect.TypeOf((*TargetPort)(nil)).Elem()
	method, ok := targetPort.MethodByName("ValidateOwnedJobDir")
	if !ok {
		t.Fatal("TargetPort missing ValidateOwnedJobDir")
	}

	wantInputs := []reflect.Type{
		reflect.TypeOf((*context.Context)(nil)).Elem(),
		reflect.TypeOf(TargetCleanupPermit{}),
		reflect.TypeOf(ValidateOwnedJobDirRequest{}),
	}
	if method.Type.NumIn() != len(wantInputs) {
		t.Fatalf("TargetPort.ValidateOwnedJobDir inputs = %s, want exact context + cleanup permit + request", method.Type)
	}
	for index, want := range wantInputs {
		if got := method.Type.In(index); got != want {
			t.Fatalf("TargetPort.ValidateOwnedJobDir input %d = %s, want %s", index, got, want)
		}
	}

	wantOutputs := []reflect.Type{
		reflect.TypeOf(OwnedJobDirValidation{}),
		reflect.TypeOf((*error)(nil)).Elem(),
	}
	if method.Type.NumOut() != len(wantOutputs) {
		t.Fatalf("TargetPort.ValidateOwnedJobDir outputs = %s, want exact observation + error", method.Type)
	}
	for index, want := range wantOutputs {
		if got := method.Type.Out(index); got != want {
			t.Fatalf("TargetPort.ValidateOwnedJobDir output %d = %s, want %s", index, got, want)
		}
	}
}

func TestTargetCleanupPermitBindsExactValidationAuthority(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	rootLocatorDigest := strings.Repeat("a", sha256DigestLength)
	object := TargetObjectRef{
		RootID:                 "root-a",
		RootLocatorDigest:      rootLocatorDigest,
		PrivateRelativeLocator: "jobs/cleanup-validation",
	}
	object.TargetPathDigest = mustTargetPathDigest(
		t, object.RootID, object.RootLocatorDigest, object.PrivateRelativeLocator,
	)
	markerBindingDigest := strings.Repeat("b", sha256DigestLength)
	permit := TargetCleanupPermit{
		SchemaVersion:       1,
		Purpose:             TargetPurposeCleanup,
		Operation:           TargetCleanupValidateOwnedJobDir,
		ResourceKind:        CleanupResourceResultSet,
		ResourceID:          strings.Repeat("1", 32),
		JobID:               strings.Repeat("2", 32),
		CleanupOwner:        "cleanup-validation-owner",
		CleanupFence:        3,
		CleanupAttempt:      4,
		NodeID:              7,
		NodeLeaseID:         strings.Repeat("5", 32),
		NodeFence:           6,
		RootID:              object.RootID,
		RootLocatorDigest:   object.RootLocatorDigest,
		TargetPathDigest:    object.TargetPathDigest,
		RootRevision:        "root-revision-1",
		MarkerBindingDigest: markerBindingDigest,
		MarkerCreatorID:     "workspace-marker-creator",
		MarkerCreatorFence:  11,
		UseLatchID:          RecoverySchemaUseLatchID,
		ExpiresAt:           now.Add(time.Minute),
	}
	request := ValidateOwnedJobDirRequest{
		Object: object, MarkerBindingDigest: markerBindingDigest,
		MarkerCreatorID: permit.MarkerCreatorID, MarkerCreatorFence: permit.MarkerCreatorFence,
	}

	if err := permit.ValidateAt(now); !errorsIsTargetPermit(err) {
		t.Fatalf("raw cleanup permit error = %v, want ErrInvalidTargetPermit", err)
	}
	permit = issueTargetCleanupPermit(permit)
	if err := permit.ValidateAt(now); err != nil {
		t.Fatalf("issued cleanup permit error = %v", err)
	}
	if err := permit.ValidateOperationObjectAt(now, TargetCleanupValidateOwnedJobDir, object); err != nil {
		t.Fatalf("issued cleanup operation/object error = %v", err)
	}
	if err := permit.ValidateOwnedJobDirRequestAt(now, request); err != nil {
		t.Fatalf("issued cleanup validation request error = %v", err)
	}

	mutations := map[string]func(*TargetCleanupPermit){
		"schema":          func(candidate *TargetCleanupPermit) { candidate.SchemaVersion++ },
		"purpose":         func(candidate *TargetCleanupPermit) { candidate.Purpose = TargetPurposeWrite },
		"operation":       func(candidate *TargetCleanupPermit) { candidate.Operation = TargetCleanupRemoveOwnedJobDir },
		"resource kind":   func(candidate *TargetCleanupPermit) { candidate.ResourceKind = "invalid" },
		"resource id":     func(candidate *TargetCleanupPermit) { candidate.ResourceID = strings.Repeat("3", 32) },
		"job id":          func(candidate *TargetCleanupPermit) { candidate.JobID = strings.Repeat("4", 32) },
		"cleanup owner":   func(candidate *TargetCleanupPermit) { candidate.CleanupOwner = "other-owner" },
		"cleanup fence":   func(candidate *TargetCleanupPermit) { candidate.CleanupFence++ },
		"cleanup attempt": func(candidate *TargetCleanupPermit) { candidate.CleanupAttempt++ },
		"node id":         func(candidate *TargetCleanupPermit) { candidate.NodeID++ },
		"node lease id":   func(candidate *TargetCleanupPermit) { candidate.NodeLeaseID = strings.Repeat("6", 32) },
		"node fence":      func(candidate *TargetCleanupPermit) { candidate.NodeFence++ },
		"root id":         func(candidate *TargetCleanupPermit) { candidate.RootID = "root-b" },
		"root locator digest": func(candidate *TargetCleanupPermit) {
			candidate.RootLocatorDigest = strings.Repeat("c", sha256DigestLength)
		},
		"target path digest": func(candidate *TargetCleanupPermit) {
			candidate.TargetPathDigest = strings.Repeat("d", sha256DigestLength)
		},
		"root revision": func(candidate *TargetCleanupPermit) { candidate.RootRevision = "root-revision-2" },
		"marker binding": func(candidate *TargetCleanupPermit) {
			candidate.MarkerBindingDigest = strings.Repeat("e", sha256DigestLength)
		},
		"marker creator id": func(candidate *TargetCleanupPermit) {
			candidate.MarkerCreatorID = "other-marker-creator"
		},
		"marker creator fence": func(candidate *TargetCleanupPermit) { candidate.MarkerCreatorFence++ },
		"use latch":            func(candidate *TargetCleanupPermit) { candidate.UseLatchID = "other-latch" },
		"expiry":               func(candidate *TargetCleanupPermit) { candidate.ExpiresAt = candidate.ExpiresAt.Add(time.Second) },
	}
	for name, mutate := range mutations {
		t.Run("proof rejects "+name+" mutation", func(t *testing.T) {
			candidate := permit
			mutate(&candidate)
			if err := candidate.ValidateAt(now); !errorsIsTargetPermit(err) {
				t.Fatalf("mutated cleanup permit error = %v, want ErrInvalidTargetPermit", err)
			}
		})
	}

	workspacePermit := permit
	workspacePermit.ResourceKind = CleanupResourceWorkspace
	workspacePermit.ResourceID = workspacePermit.JobID
	workspacePermit = issueTargetCleanupPermit(workspacePermit)
	if err := workspacePermit.ValidateOwnedJobDirRequestAt(now, request); err != nil {
		t.Fatalf("workspace cleanup permit error = %v", err)
	}
	wrongWorkspaceResource := workspacePermit
	wrongWorkspaceResource.ResourceID = strings.Repeat("7", 32)
	wrongWorkspaceResource = issueTargetCleanupPermit(wrongWorkspaceResource)
	if err := wrongWorkspaceResource.ValidateAt(now); !errorsIsTargetPermit(err) {
		t.Fatalf("cross-resource workspace permit error = %v, want ErrInvalidTargetPermit", err)
	}

	removePermit := permit
	removePermit.Operation = TargetCleanupRemoveOwnedJobDir
	removePermit = issueTargetCleanupPermit(removePermit)
	if err := removePermit.ValidateOwnedJobDirRequestAt(now, request); !errorsIsTargetPermit(err) {
		t.Fatalf("remove permit used for validation error = %v, want ErrInvalidTargetPermit", err)
	}
	otherObject := object
	otherObject.PrivateRelativeLocator = "jobs/other"
	otherObject.TargetPathDigest = mustTargetPathDigest(
		t, otherObject.RootID, otherObject.RootLocatorDigest, otherObject.PrivateRelativeLocator,
	)
	if err := permit.ValidateOwnedJobDirRequestAt(now, ValidateOwnedJobDirRequest{
		Object: otherObject, MarkerBindingDigest: markerBindingDigest,
		MarkerCreatorID: permit.MarkerCreatorID, MarkerCreatorFence: permit.MarkerCreatorFence,
	}); !errorsIsTargetPermit(err) {
		t.Fatalf("cross-object validation error = %v, want ErrInvalidTargetPermit", err)
	}
	if err := permit.ValidateOwnedJobDirRequestAt(now, ValidateOwnedJobDirRequest{
		Object: object, MarkerBindingDigest: strings.Repeat("f", sha256DigestLength),
		MarkerCreatorID: permit.MarkerCreatorID, MarkerCreatorFence: permit.MarkerCreatorFence,
	}); !errorsIsTargetPermit(err) {
		t.Fatalf("cross-marker validation error = %v, want ErrInvalidTargetPermit", err)
	}
	wrongCreator := request
	wrongCreator.MarkerCreatorID = "other-marker-creator"
	if err := permit.ValidateOwnedJobDirRequestAt(now, wrongCreator); !errorsIsTargetPermit(err) {
		t.Fatalf("cross-creator validation error = %v, want ErrInvalidTargetPermit", err)
	}
	wrongCreator = request
	wrongCreator.MarkerCreatorFence++
	if err := permit.ValidateOwnedJobDirRequestAt(now, wrongCreator); !errorsIsTargetPermit(err) {
		t.Fatalf("cross-creator-fence validation error = %v, want ErrInvalidTargetPermit", err)
	}
	if err := permit.ValidateAt(permit.ExpiresAt); !errorsIsTargetPermit(err) {
		t.Fatalf("expired cleanup permit error = %v, want ErrInvalidTargetPermit", err)
	}

	permitType := reflect.TypeOf(TargetCleanupPermit{})
	for _, forbidden := range []string{"AttemptID", "AttemptFence", "SourceFence", "ExpectedTargetRevision"} {
		if _, ok := permitType.FieldByName(forbidden); ok {
			t.Fatalf("TargetCleanupPermit unexpectedly exposes execution field %q", forbidden)
		}
	}
	privateProducts := []any{
		permit,
		request,
		CreateOwnedJobDirRequest{
			Object: object, MarkerBindingDigest: markerBindingDigest,
			MarkerCreatorID: permit.MarkerCreatorID, MarkerCreatorFence: permit.MarkerCreatorFence,
		},
		OwnedJobDir{Object: object, MarkerBindingDigest: markerBindingDigest, TargetRevision: "target-revision-1"},
		OwnedJobDirValidation{
			Object: object, MarkerBindingDigest: markerBindingDigest,
			RootRevision: permit.RootRevision, TargetRevision: "target-revision-1",
		},
	}
	for _, product := range privateProducts {
		encoded, err := json.Marshal(product)
		if err != nil {
			t.Fatalf("marshal private target product %T: %v", product, err)
		}
		for _, privateValue := range []string{
			markerBindingDigest, permit.MarkerCreatorID, object.RootLocatorDigest,
			object.TargetPathDigest, object.PrivateRelativeLocator,
		} {
			if strings.Contains(string(encoded), privateValue) {
				t.Fatalf("private target value %q leaked from %T: %s", privateValue, product, encoded)
			}
		}
	}
}

type recoveryWorkspaceMarkerDocumentForTest struct {
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

type recoveryWorkspaceMarkerBodyForTest struct {
	SchemaVersion       int    `json:"schema_version"`
	KeyVersion          int    `json:"key_version"`
	InstallationID      string `json:"installation_id"`
	JobID               string `json:"job_id"`
	RootID              string `json:"root_id"`
	RootRevision        string `json:"root_revision"`
	OwnershipNonce      string `json:"ownership_nonce"`
	MarkerBindingDigest string `json:"marker_binding_digest"`
}

var recoveryWorkspaceMarkerFieldNamesForTest = []string{
	"schema_version",
	"key_version",
	"installation_id",
	"job_id",
	"root_id",
	"root_revision",
	"ownership_nonce",
	"marker_binding_digest",
	"authentication_tag",
}

type recoveryWorkspaceMarkerKeySourceForTest struct {
	active       backupasset.DomainKeyMaterial
	versions     map[int]backupasset.DomainKeyMaterial
	activeErr    error
	byVersionErr error
	activeCalls  int
	versionCalls int
}

func (source *recoveryWorkspaceMarkerKeySourceForTest) Active(
	ctx context.Context,
	domain backupasset.KeyDomain,
) (backupasset.DomainKeyMaterial, error) {
	source.activeCalls++
	if err := ctx.Err(); err != nil {
		return backupasset.DomainKeyMaterial{}, err
	}
	if domain != backupasset.KeyDomainRecoveryCleanupOwnership {
		return backupasset.DomainKeyMaterial{}, errors.New("unexpected marker key domain")
	}
	if source.activeErr != nil {
		return backupasset.DomainKeyMaterial{}, source.activeErr
	}
	return cloneDomainKeyMaterial(source.active), nil
}

func (source *recoveryWorkspaceMarkerKeySourceForTest) ByVersion(
	ctx context.Context,
	domain backupasset.KeyDomain,
	version int,
) (backupasset.DomainKeyMaterial, error) {
	source.versionCalls++
	if err := ctx.Err(); err != nil {
		return backupasset.DomainKeyMaterial{}, err
	}
	if domain != backupasset.KeyDomainRecoveryCleanupOwnership {
		return backupasset.DomainKeyMaterial{}, errors.New("unexpected marker key domain")
	}
	if source.byVersionErr != nil {
		return backupasset.DomainKeyMaterial{}, source.byVersionErr
	}
	material, ok := source.versions[version]
	if !ok {
		return backupasset.DomainKeyMaterial{}, backupasset.ErrKeyUnavailable
	}
	return cloneDomainKeyMaterial(material), nil
}

type recoveryWorkspaceMarkerEntropyReaderForTest struct {
	reader io.Reader
	read   int
}

func (reader *recoveryWorkspaceMarkerEntropyReaderForTest) Read(buffer []byte) (int, error) {
	count, err := reader.reader.Read(buffer)
	reader.read += count
	return count, err
}

type recoveryWorkspaceMarkerFailingEntropyForTest struct {
	err error
}

func (reader recoveryWorkspaceMarkerFailingEntropyForTest) Read([]byte) (int, error) {
	return 0, reader.err
}

func TestRecoveryWorkspaceMarkerCodecCreatesAndValidatesClosedDocument(t *testing.T) {
	now := time.Date(2026, time.August, 4, 8, 30, 0, 0, time.UTC)
	material := recoveryWorkspaceMarkerMaterialForTest(1, "RECOGNIZABLE_MARKER_KEY_12345678")
	alternate := recoveryWorkspaceMarkerMaterialForTest(2, strings.Repeat("z", 32))
	keys := &recoveryWorkspaceMarkerKeySourceForTest{
		active: material,
		versions: map[int]backupasset.DomainKeyMaterial{
			material.Version:  material,
			alternate.Version: alternate,
		},
	}
	nonceOne := bytes.Repeat([]byte{0x11}, 32)
	nonceTwo := bytes.Repeat([]byte{0x22}, 32)
	entropy := &recoveryWorkspaceMarkerEntropyReaderForTest{
		reader: bytes.NewReader(append(append([]byte(nil), nonceOne...), nonceTwo...)),
	}
	writePermit, createRequest, cleanupPermit, cleanupRequest :=
		recoveryWorkspaceMarkerAuthorityForTest(t, now, material, "marker-creator-a", 17, "jobs/marker-codec")
	codec := newRecoveryWorkspaceMarkerCodec(keys, entropy)

	markerOne, err := codec.EncodeForCreate(context.Background(), writePermit, createRequest, now)
	if err != nil {
		t.Fatalf("encode first recovery workspace marker: %v", err)
	}
	if len(markerOne) == 0 || len(markerOne) > recoveryWorkspaceMarkerDocumentMaxBytes {
		t.Fatalf("first marker bytes=%d, want 1..%d", len(markerOne), recoveryWorkspaceMarkerDocumentMaxBytes)
	}
	documentOne := decodeRecoveryWorkspaceMarkerDocumentForTest(t, markerOne)
	if documentOne.SchemaVersion != 1 || documentOne.KeyVersion != material.Version ||
		documentOne.InstallationID != recoveryWorkspaceMarkerInstallationIDForTest(material.Key) ||
		documentOne.JobID != createRequestJobIDForTest(writePermit) ||
		documentOne.RootID != createRequest.Object.RootID ||
		documentOne.RootRevision != writePermit.permit.RootRevision ||
		documentOne.OwnershipNonce != base64.RawURLEncoding.EncodeToString(nonceOne) ||
		documentOne.MarkerBindingDigest != createRequest.MarkerBindingDigest ||
		!validDigest(documentOne.AuthenticationTag) {
		t.Fatalf("unexpected first marker document: %+v", documentOne)
	}
	if documentOne.AuthenticationTag != recoveryWorkspaceMarkerAuthenticationTagForTest(t, material.Key, documentOne) {
		t.Fatalf("first marker authentication tag=%q, want exact document-domain HMAC", documentOne.AuthenticationTag)
	}
	for _, privateValue := range []string{
		createRequest.Object.PrivateRelativeLocator,
		createRequest.MarkerCreatorID,
		string(material.Key),
	} {
		if strings.Contains(string(markerOne), privateValue) {
			t.Fatalf("private marker authority %q leaked in marker document: %s", privateValue, markerOne)
		}
	}

	markerTwo, err := codec.EncodeForCreate(context.Background(), writePermit, createRequest, now)
	if err != nil {
		t.Fatalf("encode second recovery workspace marker: %v", err)
	}
	documentTwo := decodeRecoveryWorkspaceMarkerDocumentForTest(t, markerTwo)
	if documentTwo.OwnershipNonce != base64.RawURLEncoding.EncodeToString(nonceTwo) ||
		documentTwo.OwnershipNonce == documentOne.OwnershipNonce || bytes.Equal(markerOne, markerTwo) || entropy.read != 64 {
		t.Fatalf("second marker nonce=%q first=%q entropy=%d", documentTwo.OwnershipNonce, documentOne.OwnershipNonce, entropy.read)
	}

	rewrapped := material
	rewrapped.ID = strings.Repeat("e", 32)
	rewrapped.ActivatedAt = now.Add(time.Hour)
	keys.versions[material.Version] = rewrapped
	if err := codec.ValidateForCleanup(context.Background(), cleanupPermit, cleanupRequest, markerOne, now); err != nil {
		t.Fatalf("validate marker through independent cleanup authority: %v", err)
	}
	if entropy.read != 64 || keys.activeCalls != 2 || keys.versionCalls != 1 {
		t.Fatalf("codec dependency calls after validation: entropy=%d active=%d version=%d", entropy.read, keys.activeCalls, keys.versionCalls)
	}
}

func recoveryWorkspaceMarkerInstallationIDForTest(key []byte) string {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("xirang/recovery/workspace-marker-installation/v1"))
	return hex.EncodeToString(mac.Sum(nil))
}

func recoveryWorkspaceMarkerAuthenticationTagForTest(
	t *testing.T,
	key []byte,
	document recoveryWorkspaceMarkerDocumentForTest,
) string {
	t.Helper()
	body, err := json.Marshal(recoveryWorkspaceMarkerBodyForTest{
		SchemaVersion: document.SchemaVersion, KeyVersion: document.KeyVersion,
		InstallationID: document.InstallationID, JobID: document.JobID,
		RootID: document.RootID, RootRevision: document.RootRevision,
		OwnershipNonce: document.OwnershipNonce, MarkerBindingDigest: document.MarkerBindingDigest,
	})
	if err != nil {
		t.Fatalf("encode independent marker authentication body: %v", err)
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("xirang/recovery/workspace-marker-document/v1"))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func TestRecoveryWorkspaceMarkerCodecRejectsTamperAndAmbiguity(t *testing.T) {
	now := time.Date(2026, time.August, 4, 9, 0, 0, 0, time.UTC)
	material := recoveryWorkspaceMarkerMaterialForTest(1, "RECOGNIZABLE_MARKER_KEY_12345678")
	alternate := recoveryWorkspaceMarkerMaterialForTest(2, strings.Repeat("z", 32))
	keys := &recoveryWorkspaceMarkerKeySourceForTest{
		active: material,
		versions: map[int]backupasset.DomainKeyMaterial{
			material.Version:  material,
			alternate.Version: alternate,
		},
	}
	writePermit, createRequest, cleanupPermit, cleanupRequest :=
		recoveryWorkspaceMarkerAuthorityForTest(t, now, material, "marker-creator-a", 17, "jobs/marker-codec")
	codec := newRecoveryWorkspaceMarkerCodec(keys, bytes.NewReader(bytes.Repeat([]byte{0x33}, 32)))
	marker, err := codec.EncodeForCreate(context.Background(), writePermit, createRequest, now)
	if err != nil {
		t.Fatalf("encode baseline recovery workspace marker: %v", err)
	}
	document := decodeRecoveryWorkspaceMarkerDocumentForTest(t, marker)

	malformed := map[string][]byte{
		"empty":           nil,
		"oversized":       bytes.Repeat([]byte{' '}, recoveryWorkspaceMarkerDocumentMaxBytes+1),
		"unknown field":   setRecoveryWorkspaceMarkerFieldForTest(t, marker, "unexpected_private_field", "value"),
		"duplicate field": append([]byte(`{"schema_version":1,`), marker[1:]...),
		"trailing object": append(append([]byte(nil), marker...), []byte(`{}`)...),
	}
	for _, field := range recoveryWorkspaceMarkerFieldNamesForTest {
		malformed["missing "+field] = removeRecoveryWorkspaceMarkerFieldForTest(t, marker, field)
	}
	mutations := map[string]any{
		"schema_version":        2,
		"key_version":           alternate.Version,
		"installation_id":       strings.Repeat("c", sha256DigestLength),
		"job_id":                strings.Repeat("9", 32),
		"root_id":               "root-b",
		"root_revision":         "root-revision-substituted",
		"ownership_nonce":       base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x44}, 32)),
		"marker_binding_digest": strings.Repeat("d", sha256DigestLength),
		"authentication_tag":    strings.Repeat("e", sha256DigestLength),
	}
	for field, value := range mutations {
		malformed["mutated "+field] = setRecoveryWorkspaceMarkerFieldForTest(t, marker, field, value)
	}
	malformed["noncanonical nonce padding"] = setRecoveryWorkspaceMarkerFieldForTest(
		t, marker, "ownership_nonce", document.OwnershipNonce+"=",
	)
	malformed["noncanonical uppercase installation"] = setRecoveryWorkspaceMarkerFieldForTest(
		t, marker, "installation_id", strings.ToUpper(document.InstallationID),
	)
	for name, candidate := range malformed {
		t.Run(name, func(t *testing.T) {
			err := codec.ValidateForCleanup(context.Background(), cleanupPermit, cleanupRequest, candidate, now)
			if err != ErrInvalidRecoveryWorkspaceMarker {
				t.Fatalf("marker error=%v, want exact ErrInvalidRecoveryWorkspaceMarker", err)
			}
		})
	}

	otherObject := cleanupRequest.Object
	otherObject.PrivateRelativeLocator = "jobs/cross-object"
	otherObject.TargetPathDigest = mustTargetPathDigest(
		t, otherObject.RootID, otherObject.RootLocatorDigest, otherObject.PrivateRelativeLocator,
	)
	otherBinding := recoveryWorkspaceMarkerBindingDigest(
		material, cleanupPermit.JobID, otherObject.RootID, cleanupPermit.RootRevision,
		otherObject.PrivateRelativeLocator,
		RecoveryWorkerClaim{WorkerID: cleanupPermit.MarkerCreatorID, AttemptFence: cleanupPermit.MarkerCreatorFence},
	)
	otherRequest := cleanupRequest
	otherRequest.Object = otherObject
	otherRequest.MarkerBindingDigest = otherBinding
	otherPermit := cleanupPermit
	otherPermit.TargetPathDigest = otherObject.TargetPathDigest
	otherPermit.MarkerBindingDigest = otherBinding
	otherPermit = issueTargetCleanupPermit(otherPermit)
	if err := codec.ValidateForCleanup(context.Background(), otherPermit, otherRequest, marker, now); err != ErrInvalidRecoveryWorkspaceMarker {
		t.Fatalf("cross-object marker error=%v, want exact invalid marker", err)
	}

	otherCreatorRequest := cleanupRequest
	otherCreatorRequest.MarkerCreatorID = "marker-creator-b"
	otherCreatorRequest.MarkerCreatorFence++
	otherCreatorRequest.MarkerBindingDigest = recoveryWorkspaceMarkerBindingDigest(
		material, cleanupPermit.JobID, cleanupRequest.Object.RootID, cleanupPermit.RootRevision,
		cleanupRequest.Object.PrivateRelativeLocator,
		RecoveryWorkerClaim{
			WorkerID: otherCreatorRequest.MarkerCreatorID, AttemptFence: otherCreatorRequest.MarkerCreatorFence,
		},
	)
	otherCreatorPermit := cleanupPermit
	otherCreatorPermit.MarkerCreatorID = otherCreatorRequest.MarkerCreatorID
	otherCreatorPermit.MarkerCreatorFence = otherCreatorRequest.MarkerCreatorFence
	otherCreatorPermit.MarkerBindingDigest = otherCreatorRequest.MarkerBindingDigest
	otherCreatorPermit = issueTargetCleanupPermit(otherCreatorPermit)
	if err := codec.ValidateForCleanup(
		context.Background(), otherCreatorPermit, otherCreatorRequest, marker, now,
	); err != ErrInvalidRecoveryWorkspaceMarker {
		t.Fatalf("cross-creator marker error=%v, want exact invalid marker", err)
	}

	mismatchedRequest := cleanupRequest
	mismatchedRequest.MarkerCreatorFence++
	if err := codec.ValidateForCleanup(
		context.Background(), cleanupPermit, mismatchedRequest, marker, now,
	); err != ErrInvalidTargetPermit {
		t.Fatalf("permit/request mismatch error=%v, want exact ErrInvalidTargetPermit", err)
	}

	wrongMaterial := recoveryWorkspaceMarkerMaterialForTest(material.Version, strings.Repeat("w", 32))
	wrongKeys := &recoveryWorkspaceMarkerKeySourceForTest{
		active:   wrongMaterial,
		versions: map[int]backupasset.DomainKeyMaterial{wrongMaterial.Version: wrongMaterial},
	}
	wrongCodec := newRecoveryWorkspaceMarkerCodec(wrongKeys, bytes.NewReader(bytes.Repeat([]byte{0x55}, 32)))
	if err := wrongCodec.ValidateForCleanup(
		context.Background(), cleanupPermit, cleanupRequest, marker, now,
	); err != ErrInvalidRecoveryWorkspaceMarker {
		t.Fatalf("substituted key marker error=%v, want exact invalid marker", err)
	}

	noEntropy := &recoveryWorkspaceMarkerEntropyReaderForTest{reader: bytes.NewReader(nil)}
	noEntropyCodec := newRecoveryWorkspaceMarkerCodec(keys, noEntropy)
	wrongCreateRequest := createRequest
	wrongCreateRequest.MarkerCreatorFence++
	if _, err := noEntropyCodec.EncodeForCreate(
		context.Background(), writePermit, wrongCreateRequest, now,
	); err != ErrInvalidTargetPermit || noEntropy.read != 0 {
		t.Fatalf("invalid create authority error=%v entropy=%d, want target permit before entropy", err, noEntropy.read)
	}
}

func TestRecoveryWorkspaceMarkerCodecErrorsAreSanitized(t *testing.T) {
	now := time.Date(2026, time.August, 4, 9, 30, 0, 0, time.UTC)
	material := recoveryWorkspaceMarkerMaterialForTest(1, "RECOGNIZABLE_MARKER_KEY_12345678")
	writePermit, createRequest, cleanupPermit, cleanupRequest :=
		recoveryWorkspaceMarkerAuthorityForTest(t, now, material, "marker-creator-a", 17, "jobs/marker-codec")
	workingKeys := &recoveryWorkspaceMarkerKeySourceForTest{
		active:   material,
		versions: map[int]backupasset.DomainKeyMaterial{material.Version: material},
	}
	workingCodec := newRecoveryWorkspaceMarkerCodec(workingKeys, bytes.NewReader(bytes.Repeat([]byte{0x66}, 32)))
	marker, err := workingCodec.EncodeForCreate(context.Background(), writePermit, createRequest, now)
	if err != nil {
		t.Fatalf("encode marker for error matrix: %v", err)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := workingCodec.EncodeForCreate(canceled, writePermit, createRequest, now); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled encode error=%v, want context.Canceled", err)
	}
	if err := workingCodec.ValidateForCleanup(canceled, cleanupPermit, cleanupRequest, marker, now); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled validation error=%v, want context.Canceled", err)
	}
	deadline, deadlineCancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer deadlineCancel()
	if _, err := workingCodec.EncodeForCreate(deadline, writePermit, createRequest, now); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline encode error=%v, want context.DeadlineExceeded", err)
	}

	rawKeyError := errors.New("RECOGNIZABLE_PRIVATE_MARKER_KEY_LOOKUP_ERROR")
	unavailableActive := newRecoveryWorkspaceMarkerCodec(
		&recoveryWorkspaceMarkerKeySourceForTest{activeErr: rawKeyError},
		bytes.NewReader(bytes.Repeat([]byte{0x77}, 32)),
	)
	assertRecoveryWorkspaceMarkerUnavailableForTest(t,
		func() error {
			_, err := unavailableActive.EncodeForCreate(context.Background(), writePermit, createRequest, now)
			return err
		}(), rawKeyError.Error(), string(material.Key), createRequest.Object.PrivateRelativeLocator,
	)

	unavailableVersion := newRecoveryWorkspaceMarkerCodec(
		&recoveryWorkspaceMarkerKeySourceForTest{active: material, byVersionErr: rawKeyError},
		bytes.NewReader(bytes.Repeat([]byte{0x77}, 32)),
	)
	assertRecoveryWorkspaceMarkerUnavailableForTest(t,
		unavailableVersion.ValidateForCleanup(context.Background(), cleanupPermit, cleanupRequest, marker, now),
		rawKeyError.Error(), string(marker), createRequest.MarkerCreatorID,
	)

	invalidMaterial := material
	invalidMaterial.Key = bytes.Repeat([]byte{'k'}, 31)
	invalidKeyCodec := newRecoveryWorkspaceMarkerCodec(
		&recoveryWorkspaceMarkerKeySourceForTest{active: invalidMaterial},
		bytes.NewReader(bytes.Repeat([]byte{0x77}, 32)),
	)
	invalidKeyErr := func() error {
		_, err := invalidKeyCodec.EncodeForCreate(context.Background(), writePermit, createRequest, now)
		return err
	}()
	assertRecoveryWorkspaceMarkerUnavailableForTest(t, invalidKeyErr, string(invalidMaterial.Key))

	rawEntropyError := errors.New("RECOGNIZABLE_PRIVATE_MARKER_ENTROPY_ERROR")
	failingEntropyCodec := newRecoveryWorkspaceMarkerCodec(
		workingKeys, recoveryWorkspaceMarkerFailingEntropyForTest{err: rawEntropyError},
	)
	entropyErr := func() error {
		_, err := failingEntropyCodec.EncodeForCreate(context.Background(), writePermit, createRequest, now)
		return err
	}()
	assertRecoveryWorkspaceMarkerUnavailableForTest(t,
		entropyErr, rawEntropyError.Error(), createRequest.MarkerCreatorID, createRequest.Object.PrivateRelativeLocator,
	)

	shortEntropyCodec := newRecoveryWorkspaceMarkerCodec(workingKeys, bytes.NewReader(bytes.Repeat([]byte{0x77}, 31)))
	shortErr := func() error {
		_, err := shortEntropyCodec.EncodeForCreate(context.Background(), writePermit, createRequest, now)
		return err
	}()
	assertRecoveryWorkspaceMarkerUnavailableForTest(t, shortErr, createRequest.MarkerBindingDigest)
}

func recoveryWorkspaceMarkerMaterialForTest(version int, key string) backupasset.DomainKeyMaterial {
	return backupasset.DomainKeyMaterial{
		ID: strings.Repeat("f", 32), Domain: backupasset.KeyDomainRecoveryCleanupOwnership,
		Version: version, State: backupasset.DomainKeyActive, Key: []byte(key),
	}
}

func recoveryWorkspaceMarkerAuthorityForTest(
	t *testing.T,
	now time.Time,
	material backupasset.DomainKeyMaterial,
	creatorID string,
	creatorFence uint64,
	privateRelativeLocator string,
) (TargetWritePermit, CreateOwnedJobDirRequest, TargetCleanupPermit, ValidateOwnedJobDirRequest) {
	t.Helper()
	jobID := strings.Repeat("1", 32)
	rootLocatorDigest := strings.Repeat("a", sha256DigestLength)
	object := TargetObjectRef{
		RootID: "root-a", RootLocatorDigest: rootLocatorDigest,
		PrivateRelativeLocator: privateRelativeLocator,
	}
	object.TargetPathDigest = mustTargetPathDigest(
		t, object.RootID, object.RootLocatorDigest, object.PrivateRelativeLocator,
	)
	rootRevision := "root-revision-1"
	markerBinding := recoveryWorkspaceMarkerBindingDigest(
		material, jobID, object.RootID, rootRevision, object.PrivateRelativeLocator,
		RecoveryWorkerClaim{WorkerID: creatorID, AttemptFence: creatorFence},
	)
	mutation := issueTargetMutationPermit(TargetMutationPermit{
		SchemaVersion: 1, NodeID: 7, Purpose: TargetPurposeWrite,
		RootID: object.RootID, RootLocatorDigest: object.RootLocatorDigest,
		TargetPathDigest: object.TargetPathDigest, RootRevision: rootRevision,
		ExpiresAt: now.Add(time.Minute), UseLatchID: RecoverySchemaUseLatchID,
		JobID: jobID, AttemptID: strings.Repeat("2", 32), NodeLeaseID: strings.Repeat("3", 32),
		AttemptFence: 19, NodeFence: 23, ExpectedTargetRevision: "target-revision-1",
	}, func(time.Time) error { return nil })
	writePermit, err := NewTargetWritePermit(mutation, now)
	if err != nil {
		t.Fatalf("construct marker write permit: %v", err)
	}
	createRequest := CreateOwnedJobDirRequest{
		Object: object, MarkerBindingDigest: markerBinding,
		MarkerCreatorID: creatorID, MarkerCreatorFence: creatorFence,
	}
	cleanupPermit := issueTargetCleanupPermit(TargetCleanupPermit{
		SchemaVersion: 1, Purpose: TargetPurposeCleanup,
		Operation: TargetCleanupValidateOwnedJobDir, ResourceKind: CleanupResourceWorkspace,
		ResourceID: jobID, JobID: jobID, CleanupOwner: "cleanup-marker-owner",
		CleanupFence: 29, CleanupAttempt: 31, NodeID: 7,
		NodeLeaseID: strings.Repeat("4", 32), NodeFence: 37,
		RootID: object.RootID, RootLocatorDigest: object.RootLocatorDigest,
		TargetPathDigest: object.TargetPathDigest, RootRevision: rootRevision,
		MarkerBindingDigest: markerBinding, MarkerCreatorID: creatorID, MarkerCreatorFence: creatorFence,
		UseLatchID: RecoverySchemaUseLatchID, ExpiresAt: now.Add(time.Minute),
	})
	cleanupRequest := ValidateOwnedJobDirRequest{
		Object: object, MarkerBindingDigest: markerBinding,
		MarkerCreatorID: creatorID, MarkerCreatorFence: creatorFence,
	}
	return writePermit, createRequest, cleanupPermit, cleanupRequest
}

func createRequestJobIDForTest(permit TargetWritePermit) string {
	return permit.permit.JobID
}

func decodeRecoveryWorkspaceMarkerDocumentForTest(
	t *testing.T,
	marker []byte,
) recoveryWorkspaceMarkerDocumentForTest {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(marker, &fields); err != nil {
		t.Fatalf("decode marker fields: %v", err)
	}
	if len(fields) != len(recoveryWorkspaceMarkerFieldNamesForTest) {
		t.Fatalf("marker fields=%v, want exact %v", fields, recoveryWorkspaceMarkerFieldNamesForTest)
	}
	for _, field := range recoveryWorkspaceMarkerFieldNamesForTest {
		if _, ok := fields[field]; !ok {
			t.Fatalf("marker missing exact field %q: %v", field, fields)
		}
	}
	var document recoveryWorkspaceMarkerDocumentForTest
	if err := json.Unmarshal(marker, &document); err != nil {
		t.Fatalf("decode marker document: %v", err)
	}
	return document
}

func setRecoveryWorkspaceMarkerFieldForTest(t *testing.T, marker []byte, field string, value any) []byte {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal(marker, &document); err != nil {
		t.Fatalf("decode marker before setting %q: %v", field, err)
	}
	document[field] = value
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("encode marker after setting %q: %v", field, err)
	}
	return encoded
}

func removeRecoveryWorkspaceMarkerFieldForTest(t *testing.T, marker []byte, field string) []byte {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal(marker, &document); err != nil {
		t.Fatalf("decode marker before removing %q: %v", field, err)
	}
	delete(document, field)
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("encode marker after removing %q: %v", field, err)
	}
	return encoded
}

func assertRecoveryWorkspaceMarkerUnavailableForTest(t *testing.T, err error, forbidden ...string) {
	t.Helper()
	if err != ErrRecoveryWorkspaceMarkerUnavailable {
		t.Fatalf("marker dependency error=%v, want exact ErrRecoveryWorkspaceMarkerUnavailable", err)
	}
	for _, value := range forbidden {
		if value != "" && strings.Contains(err.Error(), value) {
			t.Fatalf("marker dependency error leaked %q: %v", value, err)
		}
	}
}

func TestTargetMutationPermitIsWriteOnly(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	rootLocatorDigest := strings.Repeat("a", sha256DigestLength)
	object := TargetObjectRef{
		RootID:                 "root-a",
		RootLocatorDigest:      rootLocatorDigest,
		PrivateRelativeLocator: "jobs/write-only",
	}
	object.TargetPathDigest = mustTargetPathDigest(
		t, object.RootID, object.RootLocatorDigest, object.PrivateRelativeLocator,
	)
	permit := TargetMutationPermit{
		SchemaVersion: 1, NodeID: 7, Purpose: TargetPurposeWrite,
		RootID: object.RootID, RootLocatorDigest: object.RootLocatorDigest,
		TargetPathDigest: object.TargetPathDigest, RootRevision: "root-revision-1",
		ExpiresAt: now.Add(time.Minute), UseLatchID: RecoverySchemaUseLatchID,
		JobID: strings.Repeat("1", 32), AttemptID: strings.Repeat("2", 32),
		NodeLeaseID: strings.Repeat("3", 32), AttemptFence: 1, NodeFence: 2,
		ExpectedTargetRevision: "target-revision-1",
	}
	permit = issuedTargetMutationPermitForTest(permit)
	if err := permit.ValidateAt(now); err != nil {
		t.Fatalf("write mutation permit error = %v", err)
	}

	cleanupMutation := permit
	cleanupMutation.Purpose = TargetPurposeCleanup
	cleanupMutation = issuedTargetMutationPermitForTest(cleanupMutation)
	if err := cleanupMutation.ValidateAt(now); !errorsIsTargetPermit(err) {
		t.Fatalf("cleanup mutation permit error = %v, want ErrInvalidTargetPermit", err)
	}
}

func TestTargetPortVerifyUsesClosedExpectationObservationBoundary(t *testing.T) {
	targetPort := reflect.TypeOf((*TargetPort)(nil)).Elem()
	method, ok := targetPort.MethodByName("Verify")
	if !ok {
		t.Fatal("TargetPort missing Verify")
	}

	wantInputs := []reflect.Type{
		reflect.TypeOf((*context.Context)(nil)).Elem(),
		reflect.TypeOf(TargetVerifyPermit{}),
		reflect.TypeOf(TargetObjectRef{}),
		reflect.TypeOf(TargetVerifyExpectation{}),
	}
	if method.Type.NumIn() != len(wantInputs) {
		t.Fatalf("TargetPort.Verify inputs = %s, want exact context + permit + object + expectation", method.Type)
	}
	for index, want := range wantInputs {
		if got := method.Type.In(index); got != want {
			t.Fatalf("TargetPort.Verify input %d = %s, want %s", index, got, want)
		}
	}

	wantOutputs := []reflect.Type{
		reflect.TypeOf(TargetVerifyObservation{}),
		reflect.TypeOf((*error)(nil)).Elem(),
	}
	if method.Type.NumOut() != len(wantOutputs) {
		t.Fatalf("TargetPort.Verify outputs = %s, want exact observation + error", method.Type)
	}
	for index, want := range wantOutputs {
		if got := method.Type.Out(index); got != want {
			t.Fatalf("TargetPort.Verify output %d = %s, want %s", index, got, want)
		}
	}
}

func TestTargetVerifyPermitRequiresExactPrivateSessionProof(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	binding := recoveryTargetSessionBindingForTest(t)
	jobID := strings.Repeat("1", 32)
	object := TargetObjectRef{
		RootID: binding.RootID, RootLocatorDigest: binding.RootLocatorDigest,
		PrivateRelativeLocator: recoveryWorkspaceLocatorDirectory + "/" + jobID + "/item.bin",
	}
	object.TargetPathDigest = mustTargetPathDigest(
		t, object.RootID, object.RootLocatorDigest, object.PrivateRelativeLocator,
	)
	raw := TargetObservationPermit{
		SchemaVersion: 1, NodeID: binding.NodeID, Purpose: TargetPurposeVerify,
		RootID: object.RootID, RootLocatorDigest: object.RootLocatorDigest,
		TargetPathDigest: object.TargetPathDigest, RootRevision: binding.RootRevision,
		ExpiresAt: now.Add(time.Minute),
	}
	if _, err := NewTargetVerifyPermit(raw, now); !errors.Is(err, ErrInvalidTargetPermit) {
		t.Fatalf("structural verify permit error = %v, want ErrInvalidTargetPermit", err)
	}

	sealed := issueTargetVerifyPermit(
		raw, binding, jobID, TargetModeIsolated, RecoveryOperationOverwrite,
		ExpectedTargetIdentity{Kind: ExpectedTargetPresent, Digest: strings.Repeat("a", sha256DigestLength)},
	)
	if sealed.proof == nil {
		t.Fatal("issued verify permit missing private proof")
	}
	permit, err := NewTargetVerifyPermit(sealed, now)
	if err != nil {
		t.Fatalf("construct exact sealed verify authority: %v", err)
	}
	if err := permit.ValidateObjectAt(now, object); err != nil {
		t.Fatalf("exact sealed verify authority rejected: %v", err)
	}

	mutations := []struct {
		name   string
		mutate func(*TargetObservationPermit)
	}{
		{name: "schema", mutate: func(value *TargetObservationPermit) { value.SchemaVersion++ }},
		{name: "node", mutate: func(value *TargetObservationPermit) { value.NodeID++ }},
		{name: "purpose", mutate: func(value *TargetObservationPermit) { value.Purpose = TargetPurposePreflight }},
		{name: "root id", mutate: func(value *TargetObservationPermit) { value.RootID = "root-b" }},
		{name: "root locator digest", mutate: func(value *TargetObservationPermit) {
			value.RootLocatorDigest = strings.Repeat("b", sha256DigestLength)
		}},
		{name: "target path digest", mutate: func(value *TargetObservationPermit) {
			value.TargetPathDigest = strings.Repeat("c", sha256DigestLength)
		}},
		{name: "root revision", mutate: func(value *TargetObservationPermit) { value.RootRevision += "-changed" }},
		{name: "expiry", mutate: func(value *TargetObservationPermit) { value.ExpiresAt = value.ExpiresAt.Add(time.Second) }},
		{name: "job id", mutate: func(value *TargetObservationPermit) {
			value.proof.jobID = strings.Repeat("2", 32)
		}},
		{name: "target mode", mutate: func(value *TargetObservationPermit) {
			value.proof.targetMode = TargetModeInPlace
		}},
		{name: "operation", mutate: func(value *TargetObservationPermit) {
			value.proof.operation = RecoveryOperationDelete
		}},
		{name: "expected prior", mutate: func(value *TargetObservationPermit) {
			value.proof.expectedPrior.Digest = strings.Repeat("b", sha256DigestLength)
		}},
		{name: "plan id", mutate: func(value *TargetObservationPermit) {
			value.proof.sessionBinding.PlanID = strings.Repeat("7", 32)
			value.proof.sessionBinding.bindingDigest = value.proof.sessionBinding.digest()
		}},
		{name: "plan binding", mutate: func(value *TargetObservationPermit) {
			value.proof.sessionBinding.PlanBindingDigest = strings.Repeat("7", sha256DigestLength)
			value.proof.sessionBinding.bindingDigest = value.proof.sessionBinding.digest()
		}},
		{name: "node revision", mutate: func(value *TargetObservationPermit) {
			value.proof.sessionBinding.NodeRevision = "node-revision-2"
			value.proof.sessionBinding.bindingDigest = value.proof.sessionBinding.digest()
		}},
		{name: "credential revision", mutate: func(value *TargetObservationPermit) {
			value.proof.sessionBinding.CredentialRevision = "credential-revision-2"
			value.proof.sessionBinding.bindingDigest = value.proof.sessionBinding.digest()
		}},
		{name: "root locator", mutate: func(value *TargetObservationPermit) {
			value.proof.sessionBinding.RootLocator = "/srv/FAKE_SUBSTITUTED_RECOVERY_ROOT_FOR_TEST_ONLY"
			value.proof.sessionBinding.bindingDigest = value.proof.sessionBinding.digest()
		}},
		{name: "proof digest", mutate: func(value *TargetObservationPermit) {
			value.proof.bindingDigest = strings.Repeat("7", sha256DigestLength)
		}},
	}
	for _, testCase := range mutations {
		t.Run(testCase.name, func(t *testing.T) {
			mutated := sealed
			proof := *sealed.proof
			mutated.proof = &proof
			testCase.mutate(&mutated)
			if _, err := NewTargetVerifyPermit(mutated, now); !errors.Is(err, ErrInvalidTargetPermit) {
				t.Fatalf("mutated verify authority error = %v, want ErrInvalidTargetPermit", err)
			}
		})
	}

	encoded, err := json.Marshal([]any{raw, sealed, permit})
	if err != nil {
		t.Fatalf("marshal verify authorities: %v", err)
	}
	for _, forbidden := range []string{
		binding.PlanID, binding.PlanBindingDigest, binding.NodeRevision,
		binding.CredentialRevision, binding.RootLocator, binding.bindingDigest,
		sealed.proof.bindingDigest,
	} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("private verify proof value %q leaked through JSON: %s", forbidden, encoded)
		}
	}
}

func TestTargetItemWritePermitRequiresExactLockedHandoffProof(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	binding := recoveryTargetSessionBindingForTest(t)
	jobID := strings.Repeat("1", 32)
	object := TargetObjectRef{
		RootID: binding.RootID, RootLocatorDigest: binding.RootLocatorDigest,
		PrivateRelativeLocator: recoveryWorkspaceLocatorDirectory + "/" + jobID + "/nested/item.txt",
	}
	object.TargetPathDigest = mustTargetPathDigest(
		t, object.RootID, object.RootLocatorDigest, object.PrivateRelativeLocator,
	)
	mutation := issueTargetMutationPermit(TargetMutationPermit{
		SchemaVersion: 1, NodeID: binding.NodeID, Purpose: TargetPurposeWrite,
		RootID: object.RootID, RootLocatorDigest: object.RootLocatorDigest,
		TargetPathDigest: object.TargetPathDigest, RootRevision: binding.RootRevision,
		ExpiresAt: now.Add(time.Minute), UseLatchID: RecoverySchemaUseLatchID,
		JobID: jobID, AttemptID: strings.Repeat("2", 32), NodeLeaseID: strings.Repeat("3", 32),
		AttemptFence: 19, NodeFence: 23, ExpectedTargetRevision: "target-revision-1",
	}, func(time.Time) error { return nil }, binding)
	base, err := NewTargetWritePermit(mutation, now)
	if err != nil {
		t.Fatalf("construct base item write permit: %v", err)
	}
	expectedDigest := strings.Repeat("a", sha256DigestLength)
	proof := targetItemWritePermitProof{
		sessionBinding:     binding,
		jobID:              jobID,
		jobItemID:          strings.Repeat("6", 32),
		operationDigest:    strings.Repeat("7", sha256DigestLength),
		targetMode:         TargetModeIsolated,
		object:             object,
		operation:          RecoveryOperationCreate,
		expectedPrior:      ExpectedTargetIdentity{Kind: ExpectedTargetAbsent},
		expectedPriorBytes: -1,
		expectedDigest:     expectedDigest,
		expectedBytes:      7,
	}
	sealed := issueTargetItemWritePermit(base, proof)
	request := TargetWriteAtomicRequest{
		Object: object, ExpectedBytes: proof.expectedBytes, ExpectedDigest: proof.expectedDigest,
		Content: strings.NewReader("payload"),
	}
	authority, err := sealed.validateItemWriteAt(now, request)
	if err != nil {
		t.Fatalf("validate exact item write permit: %v", err)
	}
	if authority.sessionBinding != binding || authority.jobID != jobID ||
		authority.jobItemID != proof.jobItemID || authority.operationDigest != proof.operationDigest ||
		authority.targetMode != TargetModeIsolated ||
		authority.operation != RecoveryOperationCreate ||
		authority.expectedPrior != (ExpectedTargetIdentity{Kind: ExpectedTargetAbsent}) ||
		authority.expectedPriorBytes != -1 ||
		authority.artifacts != (recoveryOverwriteArtifactBinding{}) {
		t.Fatalf("item authority=%+v, want exact locked create authority", authority)
	}
	if sealed.itemProof == nil || sealed.itemProof.bindingDigest == "" ||
		sealed.itemProof.bindingDigest != targetItemWritePermitProofDigest(sealed, sealed.itemProof) {
		t.Fatalf("item proof=%+v, want exact sealed binding", sealed.itemProof)
	}

	mutations := []struct {
		name   string
		mutate func(*TargetWritePermit)
	}{
		{name: "missing proof", mutate: func(value *TargetWritePermit) { value.itemProof = nil }},
		{name: "node", mutate: func(value *TargetWritePermit) { value.permit.NodeID++ }},
		{name: "root id", mutate: func(value *TargetWritePermit) { value.permit.RootID = "root-b" }},
		{name: "root locator digest", mutate: func(value *TargetWritePermit) {
			value.permit.RootLocatorDigest = strings.Repeat("b", sha256DigestLength)
		}},
		{name: "target path digest", mutate: func(value *TargetWritePermit) {
			value.permit.TargetPathDigest = strings.Repeat("c", sha256DigestLength)
		}},
		{name: "root revision", mutate: func(value *TargetWritePermit) { value.permit.RootRevision += "-changed" }},
		{name: "expiry", mutate: func(value *TargetWritePermit) { value.permit.ExpiresAt = value.permit.ExpiresAt.Add(time.Second) }},
		{name: "public job", mutate: func(value *TargetWritePermit) { value.permit.JobID = strings.Repeat("4", 32) }},
		{name: "expected target revision", mutate: func(value *TargetWritePermit) {
			value.permit.ExpectedTargetRevision = "target-revision-2"
		}},
		{name: "base session binding", mutate: func(value *TargetWritePermit) {
			value.permit.proof.sessionBinding.NodeRevision = "node-revision-substituted"
			value.permit.proof.sessionBinding.bindingDigest = value.permit.proof.sessionBinding.digest()
			value.permit.proof.bindingDigest = targetMutationPermitProofDigest(
				value.permit, value.permit.proof.sessionBinding,
			)
		}},
		{name: "proof session binding", mutate: func(value *TargetWritePermit) {
			value.itemProof.sessionBinding.CredentialRevision = "credential-revision-substituted"
			value.itemProof.sessionBinding.bindingDigest = value.itemProof.sessionBinding.digest()
		}},
		{name: "proof job", mutate: func(value *TargetWritePermit) { value.itemProof.jobID = strings.Repeat("5", 32) }},
		{name: "proof item", mutate: func(value *TargetWritePermit) { value.itemProof.jobItemID = strings.Repeat("5", 32) }},
		{name: "proof operation digest", mutate: func(value *TargetWritePermit) {
			value.itemProof.operationDigest = strings.Repeat("5", sha256DigestLength)
		}},
		{name: "proof mode", mutate: func(value *TargetWritePermit) { value.itemProof.targetMode = TargetModeInPlace }},
		{name: "proof object", mutate: func(value *TargetWritePermit) {
			value.itemProof.object.PrivateRelativeLocator += ".substituted"
		}},
		{name: "proof operation", mutate: func(value *TargetWritePermit) {
			value.itemProof.operation = RecoveryOperationOverwrite
		}},
		{name: "proof prior", mutate: func(value *TargetWritePermit) {
			value.itemProof.expectedPrior = ExpectedTargetIdentity{
				Kind: ExpectedTargetPresent, Digest: strings.Repeat("d", sha256DigestLength),
			}
		}},
		{name: "proof digest", mutate: func(value *TargetWritePermit) {
			value.itemProof.expectedDigest = strings.Repeat("e", sha256DigestLength)
		}},
		{name: "proof bytes", mutate: func(value *TargetWritePermit) { value.itemProof.expectedBytes++ }},
		{name: "proof prior bytes", mutate: func(value *TargetWritePermit) { value.itemProof.expectedPriorBytes++ }},
		{name: "proof artifacts", mutate: func(value *TargetWritePermit) { value.itemProof.artifacts.keyVersion = 1 }},
		{name: "proof binding digest", mutate: func(value *TargetWritePermit) {
			value.itemProof.bindingDigest = strings.Repeat("f", sha256DigestLength)
		}},
	}
	for _, testCase := range mutations {
		t.Run(testCase.name, func(t *testing.T) {
			mutated := sealed
			mutationProof := *sealed.permit.proof
			mutated.permit.proof = &mutationProof
			itemProof := *sealed.itemProof
			mutated.itemProof = &itemProof
			testCase.mutate(&mutated)
			if _, err := mutated.validateItemWriteAt(now, request); !errors.Is(err, ErrInvalidTargetPermit) {
				t.Fatalf("mutated item write permit error=%v, want ErrInvalidTargetPermit", err)
			}
		})
	}

	requestMutations := []struct {
		name   string
		mutate func(*TargetWriteAtomicRequest)
	}{
		{name: "request object", mutate: func(value *TargetWriteAtomicRequest) {
			value.Object.PrivateRelativeLocator += ".substituted"
		}},
		{name: "request digest", mutate: func(value *TargetWriteAtomicRequest) {
			value.ExpectedDigest = strings.Repeat("9", sha256DigestLength)
		}},
		{name: "request bytes", mutate: func(value *TargetWriteAtomicRequest) { value.ExpectedBytes++ }},
	}
	for _, testCase := range requestMutations {
		t.Run(testCase.name, func(t *testing.T) {
			mutated := request
			testCase.mutate(&mutated)
			if _, err := sealed.validateItemWriteAt(now, mutated); !errors.Is(err, ErrInvalidTargetPermit) {
				t.Fatalf("mutated item write request error=%v, want ErrInvalidTargetPermit", err)
			}
		})
	}

	encoded, err := json.Marshal([]any{sealed, request})
	if err != nil {
		t.Fatalf("marshal item write authority: %v", err)
	}
	for _, forbidden := range []string{
		binding.PlanID, binding.PlanBindingDigest, binding.NodeRevision,
		binding.CredentialRevision, binding.RootLocator, binding.bindingDigest,
		object.PrivateRelativeLocator, object.RootLocatorDigest, object.TargetPathDigest,
		sealed.itemProof.bindingDigest,
	} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("private item write value %q leaked through JSON: %s", forbidden, encoded)
		}
	}
}

func TestRecoverySFTPTargetWriteAtomicOverwriteRequiresExactArtifactAuthority(t *testing.T) {
	fixture := newRecoveryLocalSFTPTargetFixture(t)
	jobID := fixture.writePermit.permit.JobID
	payload := []byte("post-payload")
	material, artifactInput := recoveryOverwriteArtifactBindingInputForTest(
		t, fixture.binding, jobID, "items/private-overwrite-target", payload,
	)
	artifacts, err := newRecoveryOverwriteArtifactBinding(material, artifactInput)
	if err != nil {
		t.Fatalf("derive overwrite artifact authority: %v", err)
	}
	mutation := issueTargetMutationPermit(TargetMutationPermit{
		SchemaVersion: 1, NodeID: fixture.binding.NodeID, Purpose: TargetPurposeWrite,
		RootID: artifactInput.object.RootID, RootLocatorDigest: artifactInput.object.RootLocatorDigest,
		TargetPathDigest: artifactInput.object.TargetPathDigest, RootRevision: fixture.binding.RootRevision,
		ExpiresAt: fixture.now.Add(time.Minute), UseLatchID: RecoverySchemaUseLatchID,
		JobID: jobID, AttemptID: strings.Repeat("2", 32), NodeLeaseID: strings.Repeat("3", 32),
		AttemptFence: 19, NodeFence: 23, ExpectedTargetRevision: "target-revision-overwrite-authority",
	}, func(time.Time) error { return nil }, fixture.binding)
	base, err := NewTargetWritePermit(mutation, fixture.now)
	if err != nil {
		t.Fatalf("construct overwrite base permit: %v", err)
	}
	permit := issueTargetItemWritePermit(base, targetItemWritePermitProof{
		sessionBinding:     fixture.binding,
		jobID:              jobID,
		jobItemID:          artifactInput.jobItemID,
		operationDigest:    artifactInput.operationDigest,
		targetMode:         TargetModeInPlace,
		object:             artifactInput.object,
		operation:          RecoveryOperationOverwrite,
		expectedPrior:      artifactInput.expectedPrior,
		expectedPriorBytes: artifactInput.expectedPriorBytes,
		expectedDigest:     artifactInput.expectedPostDigest,
		expectedBytes:      artifactInput.expectedPostBytes,
		artifacts:          artifacts,
	})
	if permit.itemProof == nil {
		t.Fatal("exact overwrite artifact authority was not sealed")
	}
	request := TargetWriteAtomicRequest{
		Object: artifactInput.object, ExpectedBytes: artifactInput.expectedPostBytes,
		ExpectedDigest: artifactInput.expectedPostDigest, Content: bytes.NewReader(payload),
	}
	authority, err := permit.validateItemWriteAt(fixture.now, request)
	if err != nil || authority.jobItemID != artifactInput.jobItemID ||
		authority.operationDigest != artifactInput.operationDigest ||
		authority.expectedPriorBytes != artifactInput.expectedPriorBytes || authority.artifacts != artifacts {
		t.Fatalf("exact overwrite private authority=%+v err=%v, want item/operation/prior/artifact binding", authority, err)
	}
	encodedAuthority, err := json.Marshal([]any{permit, authority})
	if err != nil {
		t.Fatalf("marshal overwrite private authority: %v", err)
	}
	for _, forbidden := range []string{
		artifactInput.jobItemID, artifactInput.operationDigest, artifactInput.object.PrivateRelativeLocator,
		artifacts.bindingDigest, artifacts.token, artifacts.intentComponent, artifacts.priorComponent,
		artifacts.postComponent, artifacts.publishedComponent, artifacts.intentDocument, artifacts.publishedDocument,
	} {
		if strings.Contains(string(encodedAuthority), forbidden) {
			t.Fatalf("overwrite private authority leaked %q through JSON: %s", forbidden, encodedAuthority)
		}
	}

	assertClosedBeforeDependency := func(
		t *testing.T,
		candidate TargetWritePermit,
		candidateRequest TargetWriteAtomicRequest,
		want error,
	) {
		t.Helper()
		fixture.resolver.calls = 0
		fixture.dialer.calls = 0
		client := &recoveryLocalSFTPClient{}
		target := fixture.targetWithClient(client)
		entropy := bytes.NewReader(bytes.Repeat([]byte{0x5a}, recoveryPayloadTempEntropyBytes))
		target.entropy = entropy
		target.now = func() time.Time { return fixture.now }
		_, err := target.WriteAtomic(context.Background(), candidate, candidateRequest)
		if err != want {
			t.Fatalf("overwrite authority error=%v, want exact %v", err, want)
		}
		if entropy.Len() != recoveryPayloadTempEntropyBytes || fixture.resolver.calls != 0 ||
			fixture.dialer.calls != 0 || recoveryLocalSFTPCallCountForTest(client) != 0 {
			t.Fatalf("closed overwrite authority consumed entropy/session: remaining=%d resolver=%d dialer=%d sftp=%d",
				entropy.Len(), fixture.resolver.calls, fixture.dialer.calls,
				recoveryLocalSFTPCallCountForTest(client))
		}
	}

	mutations := []struct {
		name   string
		mutate func(*TargetWritePermit, *TargetWriteAtomicRequest)
	}{
		{name: "missing item proof", mutate: func(permit *TargetWritePermit, _ *TargetWriteAtomicRequest) { permit.itemProof = nil }},
		{name: "mode", mutate: func(permit *TargetWritePermit, _ *TargetWriteAtomicRequest) {
			permit.itemProof.targetMode = TargetModeIsolated
		}},
		{name: "item", mutate: func(permit *TargetWritePermit, _ *TargetWriteAtomicRequest) {
			permit.itemProof.jobItemID = strings.Repeat("8", 32)
		}},
		{name: "operation digest", mutate: func(permit *TargetWritePermit, _ *TargetWriteAtomicRequest) {
			permit.itemProof.operationDigest = strings.Repeat("8", sha256DigestLength)
		}},
		{name: "key version", mutate: func(permit *TargetWritePermit, _ *TargetWriteAtomicRequest) { permit.itemProof.artifacts.keyVersion++ }},
		{name: "prior digest", mutate: func(permit *TargetWritePermit, _ *TargetWriteAtomicRequest) {
			permit.itemProof.expectedPrior.Digest = strings.Repeat("8", sha256DigestLength)
		}},
		{name: "prior bytes", mutate: func(permit *TargetWritePermit, _ *TargetWriteAtomicRequest) { permit.itemProof.expectedPriorBytes++ }},
		{name: "post digest", mutate: func(permit *TargetWritePermit, request *TargetWriteAtomicRequest) {
			permit.itemProof.expectedDigest = strings.Repeat("8", sha256DigestLength)
			request.ExpectedDigest = permit.itemProof.expectedDigest
		}},
		{name: "post bytes", mutate: func(permit *TargetWritePermit, request *TargetWriteAtomicRequest) {
			permit.itemProof.expectedBytes++
			request.ExpectedBytes++
		}},
		{name: "object", mutate: func(permit *TargetWritePermit, _ *TargetWriteAtomicRequest) {
			permit.itemProof.object.PrivateRelativeLocator += "-changed"
		}},
		{name: "token", mutate: func(permit *TargetWritePermit, _ *TargetWriteAtomicRequest) {
			permit.itemProof.artifacts.token = strings.Repeat("A", len(permit.itemProof.artifacts.token))
		}},
		{name: "intent marker binding", mutate: func(permit *TargetWritePermit, _ *TargetWriteAtomicRequest) {
			permit.itemProof.artifacts.intentDocument += " "
		}},
		{name: "published marker binding", mutate: func(permit *TargetWritePermit, _ *TargetWriteAtomicRequest) {
			permit.itemProof.artifacts.publishedDocument += " "
		}},
		{name: "proof binding", mutate: func(permit *TargetWritePermit, _ *TargetWriteAtomicRequest) {
			permit.itemProof.bindingDigest = strings.Repeat("f", sha256DigestLength)
		}},
	}
	for _, testCase := range mutations {
		t.Run(testCase.name, func(t *testing.T) {
			candidate := cloneTargetWritePermitForTest(permit)
			candidateRequest := request
			candidateRequest.Content = bytes.NewReader(payload)
			testCase.mutate(&candidate, &candidateRequest)
			assertClosedBeforeDependency(t, candidate, candidateRequest, ErrInvalidTargetPermit)
		})
	}

	isolatedPermit, isolatedRequest := recoveryItemWriteAuthorityForTest(
		t, fixture.now, fixture.binding, jobID, TargetModeIsolated,
		recoveryWorkspaceLocatorDirectory+"/"+jobID+"/item.txt",
		RecoveryOperationOverwrite, artifactInput.expectedPrior, payload,
	)
	assertClosedBeforeDependency(t, isolatedPermit, isolatedRequest, ErrRecoveryTargetUnavailable)
}

func TestRecoveryDeleteArtifactBindingUsesHistoricalCleanupKey(t *testing.T) {
	binding := recoveryTargetSessionBindingForTest(t)
	material := backupasset.DomainKeyMaterial{
		ID: strings.Repeat("f", 32), Domain: backupasset.KeyDomainRecoveryCleanupOwnership,
		Version: 7, State: backupasset.DomainKeyActive,
		Key: []byte("delete-historical-cleanup-key-v1"),
	}
	object := TargetObjectRef{
		RootID: binding.RootID, RootLocatorDigest: binding.RootLocatorDigest,
		PrivateRelativeLocator: "delete-parent/delete-object-canary",
	}
	object.TargetPathDigest = mustTargetPathDigest(
		t, object.RootID, object.RootLocatorDigest, object.PrivateRelativeLocator,
	)
	input := recoveryDeleteArtifactBindingInput{
		keyVersion: material.Version,
		planID:     binding.PlanID, planBindingDigest: binding.PlanBindingDigest,
		jobID: strings.Repeat("1", 32), jobItemID: strings.Repeat("4", 32),
		operationDigest:      strings.Repeat("5", sha256DigestLength),
		consumedCheckpointID: strings.Repeat("6", 32),
		consumedGrantID:      strings.Repeat("7", 32), consumedGrantDigest: strings.Repeat("8", sha256DigestLength),
		targetMode: TargetModeInPlace, nodeID: binding.NodeID,
		rootID: binding.RootID, rootLocatorDigest: binding.RootLocatorDigest,
		rootRevision: binding.RootRevision, object: object,
		expectedPrior: ExpectedTargetIdentity{
			Kind: ExpectedTargetPresent, Digest: strings.Repeat("9", sha256DigestLength),
		},
		expectedPriorBytes: -1,
	}

	first, err := deriveRecoveryDeleteArtifactBinding(material, input)
	if err != nil {
		t.Fatalf("derive delete artifact binding: %v", err)
	}
	replayed, err := deriveRecoveryDeleteArtifactBinding(cloneDomainKeyMaterial(material), input)
	if err != nil || replayed != first {
		t.Fatalf("same historical key/delete item did not replay exactly: first=%+v replay=%+v err=%v",
			first, replayed, err)
	}

	fields := []string{
		strconv.Itoa(input.keyVersion), input.planID, input.planBindingDigest,
		input.jobID, input.jobItemID, input.operationDigest,
		input.consumedCheckpointID, input.consumedGrantID, input.consumedGrantDigest,
		string(input.targetMode), strconv.FormatUint(uint64(input.nodeID), 10),
		input.rootID, input.rootLocatorDigest, input.rootRevision,
		input.object.RootID, input.object.RootLocatorDigest, input.object.TargetPathDigest,
		input.object.PrivateRelativeLocator, string(input.expectedPrior.Kind), input.expectedPrior.Digest,
		strconv.FormatInt(input.expectedPriorBytes, 10),
	}
	wantRaw := recoveryOverwriteFramedHMACForTest(
		material.Key, "xirang/recovery/delete-artifact/v1", fields...,
	)
	wantToken := base64.RawURLEncoding.EncodeToString(wantRaw)
	wantDigest := hex.EncodeToString(wantRaw)
	if first.keyVersion != material.Version || first.token != wantToken || first.bindingDigest != wantDigest {
		t.Fatalf("delete binding version/token/digest=%d/%q/%q, want exact %d/%q/%q",
			first.keyVersion, first.token, first.bindingDigest, material.Version, wantToken, wantDigest)
	}
	decodedToken, err := base64.RawURLEncoding.DecodeString(first.token)
	if err != nil || len(decodedToken) != sha256.Size ||
		base64.RawURLEncoding.EncodeToString(decodedToken) != first.token {
		t.Fatalf("delete token=%q bytes=%d error=%v, want canonical 32-byte base64url",
			first.token, len(decodedToken), err)
	}

	wantComponents := map[string]string{
		"intent":   ".xirang-recovery-delete-" + first.token + ".intent",
		"captured": ".xirang-recovery-delete-" + first.token + ".captured",
		"verified": ".xirang-recovery-delete-" + first.token + ".verified",
	}
	gotComponents := map[string]string{
		"intent": first.intentComponent, "captured": first.capturedComponent,
		"verified": first.verifiedComponent,
	}
	for phase, component := range gotComponents {
		if component != wantComponents[phase] || path.Base(component) != component || len(component) > 255 {
			t.Fatalf("delete %s component=%q bytes=%d, want exact same-parent %q within 255 bytes",
				phase, component, len(component), wantComponents[phase])
		}
	}

	type markerBody struct {
		SchemaVersion int    `json:"schema_version"`
		KeyVersion    int    `json:"key_version"`
		Phase         string `json:"phase"`
		BindingDigest string `json:"binding_digest"`
	}
	type markerDocument struct {
		SchemaVersion     int    `json:"schema_version"`
		KeyVersion        int    `json:"key_version"`
		Phase             string `json:"phase"`
		BindingDigest     string `json:"binding_digest"`
		AuthenticationTag string `json:"authentication_tag"`
	}
	for phase, encoded := range map[string]string{
		"intent": first.intentDocument, "verified": first.verifiedDocument,
	} {
		body := markerBody{
			SchemaVersion: 1, KeyVersion: material.Version,
			Phase: phase, BindingDigest: first.bindingDigest,
		}
		bodyBytes, bodyErr := json.Marshal(body)
		domain := "xirang/recovery/delete-" + phase + "/v1"
		wantTag := base64.RawURLEncoding.EncodeToString(
			recoveryOverwriteFramedHMACForTest(material.Key, domain, string(bodyBytes)),
		)
		wantDocument, documentErr := json.Marshal(markerDocument{
			SchemaVersion: body.SchemaVersion, KeyVersion: body.KeyVersion,
			Phase: body.Phase, BindingDigest: body.BindingDigest,
			AuthenticationTag: wantTag,
		})
		if bodyErr != nil || documentErr != nil || encoded != string(wantDocument) || len(encoded) > 1024 {
			t.Fatalf("delete %s document=%s, want exact authenticated document %s within 1024 bytes",
				phase, encoded, wantDocument)
		}
		var decoded markerDocument
		if err := json.Unmarshal([]byte(encoded), &decoded); err != nil || decoded != (markerDocument{
			SchemaVersion: 1, KeyVersion: material.Version, Phase: phase,
			BindingDigest: first.bindingDigest, AuthenticationTag: wantTag,
		}) {
			t.Fatalf("decode delete %s document=%+v error=%v", phase, decoded, err)
		}
	}

	privateInputs := []string{
		string(material.Key), input.planID, input.planBindingDigest, input.jobID,
		input.jobItemID, input.operationDigest, input.consumedCheckpointID,
		input.consumedGrantID, input.consumedGrantDigest, input.rootID,
		input.rootLocatorDigest, input.rootRevision, input.object.TargetPathDigest,
		input.object.PrivateRelativeLocator, input.expectedPrior.Digest,
	}
	for _, product := range []string{
		first.intentComponent, first.capturedComponent, first.verifiedComponent,
		first.intentDocument, first.verifiedDocument,
	} {
		for _, forbidden := range privateInputs {
			if strings.Contains(product, forbidden) {
				t.Fatalf("delete artifact product leaked raw private input %q: %s", forbidden, product)
			}
		}
	}

	mutations := []struct {
		name   string
		mutate func(*backupasset.DomainKeyMaterial, *recoveryDeleteArtifactBindingInput)
	}{
		{name: "historical key", mutate: func(material *backupasset.DomainKeyMaterial, _ *recoveryDeleteArtifactBindingInput) {
			material.Key[0] ^= 0xff
		}},
		{name: "key version", mutate: func(material *backupasset.DomainKeyMaterial, input *recoveryDeleteArtifactBindingInput) {
			material.Version++
			input.keyVersion++
		}},
		{name: "plan", mutate: func(_ *backupasset.DomainKeyMaterial, input *recoveryDeleteArtifactBindingInput) {
			input.planID = strings.Repeat("2", 32)
		}},
		{name: "plan binding", mutate: func(_ *backupasset.DomainKeyMaterial, input *recoveryDeleteArtifactBindingInput) {
			input.planBindingDigest = strings.Repeat("2", sha256DigestLength)
		}},
		{name: "job", mutate: func(_ *backupasset.DomainKeyMaterial, input *recoveryDeleteArtifactBindingInput) {
			input.jobID = strings.Repeat("3", 32)
		}},
		{name: "item", mutate: func(_ *backupasset.DomainKeyMaterial, input *recoveryDeleteArtifactBindingInput) {
			input.jobItemID = strings.Repeat("a", 32)
		}},
		{name: "operation", mutate: func(_ *backupasset.DomainKeyMaterial, input *recoveryDeleteArtifactBindingInput) {
			input.operationDigest = strings.Repeat("a", sha256DigestLength)
		}},
		{name: "checkpoint", mutate: func(_ *backupasset.DomainKeyMaterial, input *recoveryDeleteArtifactBindingInput) {
			input.consumedCheckpointID = strings.Repeat("b", 32)
		}},
		{name: "grant id", mutate: func(_ *backupasset.DomainKeyMaterial, input *recoveryDeleteArtifactBindingInput) {
			input.consumedGrantID = strings.Repeat("c", 32)
		}},
		{name: "grant digest", mutate: func(_ *backupasset.DomainKeyMaterial, input *recoveryDeleteArtifactBindingInput) {
			input.consumedGrantDigest = strings.Repeat("c", sha256DigestLength)
		}},
		{name: "node", mutate: func(_ *backupasset.DomainKeyMaterial, input *recoveryDeleteArtifactBindingInput) {
			input.nodeID++
		}},
		{name: "root", mutate: func(_ *backupasset.DomainKeyMaterial, input *recoveryDeleteArtifactBindingInput) {
			input.rootID += "-changed"
			input.object.RootID = input.rootID
			input.object.TargetPathDigest = mustTargetPathDigest(
				t, input.object.RootID, input.object.RootLocatorDigest, input.object.PrivateRelativeLocator,
			)
		}},
		{name: "root locator digest", mutate: func(_ *backupasset.DomainKeyMaterial, input *recoveryDeleteArtifactBindingInput) {
			input.rootLocatorDigest = strings.Repeat("d", sha256DigestLength)
			input.object.RootLocatorDigest = input.rootLocatorDigest
			input.object.TargetPathDigest = mustTargetPathDigest(
				t, input.object.RootID, input.object.RootLocatorDigest, input.object.PrivateRelativeLocator,
			)
		}},
		{name: "root revision", mutate: func(_ *backupasset.DomainKeyMaterial, input *recoveryDeleteArtifactBindingInput) {
			input.rootRevision += "-changed"
		}},
		{name: "private locator", mutate: func(_ *backupasset.DomainKeyMaterial, input *recoveryDeleteArtifactBindingInput) {
			input.object.PrivateRelativeLocator += "-changed"
			input.object.TargetPathDigest = mustTargetPathDigest(
				t, input.object.RootID, input.object.RootLocatorDigest, input.object.PrivateRelativeLocator,
			)
		}},
		{name: "prior digest", mutate: func(_ *backupasset.DomainKeyMaterial, input *recoveryDeleteArtifactBindingInput) {
			input.expectedPrior.Digest = strings.Repeat("e", sha256DigestLength)
		}},
	}
	for _, testCase := range mutations {
		t.Run(testCase.name, func(t *testing.T) {
			changedMaterial := cloneDomainKeyMaterial(material)
			changedInput := input
			testCase.mutate(&changedMaterial, &changedInput)
			changed, err := deriveRecoveryDeleteArtifactBinding(changedMaterial, changedInput)
			if err != nil {
				t.Fatalf("derive field-sensitive delete binding: %v", err)
			}
			if changed == first || changed.token == first.token || changed.bindingDigest == first.bindingDigest {
				t.Fatalf("delete binding did not change for %s: before=%+v after=%+v",
					testCase.name, first, changed)
			}
		})
	}

	for _, testCase := range []struct {
		name   string
		mutate func(*backupasset.DomainKeyMaterial, *recoveryDeleteArtifactBindingInput)
	}{
		{name: "isolated mode", mutate: func(_ *backupasset.DomainKeyMaterial, input *recoveryDeleteArtifactBindingInput) {
			input.targetMode = TargetModeIsolated
		}},
		{name: "object root", mutate: func(_ *backupasset.DomainKeyMaterial, input *recoveryDeleteArtifactBindingInput) {
			input.object.RootID += "-changed"
		}},
		{name: "object digest", mutate: func(_ *backupasset.DomainKeyMaterial, input *recoveryDeleteArtifactBindingInput) {
			input.object.TargetPathDigest = strings.Repeat("f", sha256DigestLength)
		}},
		{name: "absent prior", mutate: func(_ *backupasset.DomainKeyMaterial, input *recoveryDeleteArtifactBindingInput) {
			input.expectedPrior = ExpectedTargetIdentity{Kind: ExpectedTargetAbsent}
		}},
		{name: "prior byte sentinel", mutate: func(_ *backupasset.DomainKeyMaterial, input *recoveryDeleteArtifactBindingInput) {
			input.expectedPriorBytes = 0
		}},
		{name: "material version", mutate: func(material *backupasset.DomainKeyMaterial, _ *recoveryDeleteArtifactBindingInput) {
			material.Version++
		}},
		{name: "key domain", mutate: func(material *backupasset.DomainKeyMaterial, _ *recoveryDeleteArtifactBindingInput) {
			material.Domain = backupasset.KeyDomainEntryIdentity
		}},
	} {
		t.Run("reject "+testCase.name, func(t *testing.T) {
			changedMaterial := cloneDomainKeyMaterial(material)
			changedInput := input
			testCase.mutate(&changedMaterial, &changedInput)
			if _, err := deriveRecoveryDeleteArtifactBinding(changedMaterial, changedInput); err != ErrInvalidTargetPermit {
				t.Fatalf("invalid delete artifact input %s error=%v, want exact ErrInvalidTargetPermit",
					testCase.name, err)
			}
		})
	}
}

func TestRecoverySFTPTargetDeleteRequiresConsumedExactAuthority(t *testing.T) {
	fixture := newRecoveryLocalSFTPTargetFixture(t)
	jobID := fixture.writePermit.permit.JobID
	object := TargetObjectRef{
		RootID: fixture.binding.RootID, RootLocatorDigest: fixture.binding.RootLocatorDigest,
		PrivateRelativeLocator: "items/private-delete-target",
	}
	object.TargetPathDigest = mustTargetPathDigest(
		t, object.RootID, object.RootLocatorDigest, object.PrivateRelativeLocator,
	)
	proof := targetDeletePermitProof{
		sessionBinding:       fixture.binding,
		jobID:                jobID,
		jobItemID:            strings.Repeat("4", 32),
		operationDigest:      strings.Repeat("5", sha256DigestLength),
		consumedCheckpointID: strings.Repeat("6", 32),
		consumedGrantID:      strings.Repeat("7", 32),
		consumedGrantDigest:  strings.Repeat("8", sha256DigestLength),
		currentAttemptID:     strings.Repeat("2", 32),
		currentAttemptFence:  19,
		currentNodeLeaseID:   strings.Repeat("3", 32),
		currentNodeFence:     23,
		currentSourceFence: backupasset.LeaseFence{
			LeaseID: strings.Repeat("1", 32), RecoveryPointID: strings.Repeat("4", 32),
			HolderType: backupasset.LeaseHolderRecoveryJob, OwnerID: jobID,
			AttemptID: strings.Repeat("2", 32), FenceToken: strings.Repeat("5", 32),
		},
		targetChainRevision: "target-revision-delete-authority",
		targetMode:          TargetModeInPlace,
		object:              object,
		expectedPrior: ExpectedTargetIdentity{
			Kind: ExpectedTargetPresent, Digest: strings.Repeat("9", sha256DigestLength),
		},
		expectedPriorBytes: -1,
	}
	var err error
	proof.artifacts, err = deriveRecoveryDeleteArtifactBinding(
		fixture.material,
		recoveryDeleteArtifactBindingInput{
			keyVersion: fixture.material.Version,
			planID:     fixture.binding.PlanID, planBindingDigest: fixture.binding.PlanBindingDigest,
			jobID: proof.jobID, jobItemID: proof.jobItemID,
			operationDigest:      proof.operationDigest,
			consumedCheckpointID: proof.consumedCheckpointID,
			consumedGrantID:      proof.consumedGrantID, consumedGrantDigest: proof.consumedGrantDigest,
			targetMode: proof.targetMode, nodeID: fixture.binding.NodeID,
			rootID: fixture.binding.RootID, rootLocatorDigest: fixture.binding.RootLocatorDigest,
			rootRevision: fixture.binding.RootRevision, object: proof.object,
			expectedPrior: proof.expectedPrior, expectedPriorBytes: proof.expectedPriorBytes,
		},
	)
	if err != nil {
		t.Fatalf("derive delete artifact authority: %v", err)
	}
	mutation := issueTargetMutationPermit(TargetMutationPermit{
		SchemaVersion: 1, NodeID: fixture.binding.NodeID, Purpose: TargetPurposeWrite,
		RootID: object.RootID, RootLocatorDigest: object.RootLocatorDigest,
		TargetPathDigest: object.TargetPathDigest, RootRevision: fixture.binding.RootRevision,
		ExpiresAt: fixture.now.Add(time.Minute), UseLatchID: RecoverySchemaUseLatchID,
		JobID: jobID, AttemptID: strings.Repeat("2", 32), NodeLeaseID: strings.Repeat("3", 32),
		AttemptFence: 19, NodeFence: 23, ExpectedTargetRevision: "target-revision-delete-authority",
	}, func(time.Time) error { return nil }, fixture.binding)
	permit := issueTargetDeletePermit(mutation, proof)
	if permit.proof == nil {
		t.Fatal("exact consumed delete authority was not sealed")
	}
	request := TargetDeleteRequest{Object: object}

	assertClosedBeforeDependency := func(
		t *testing.T,
		candidate TargetDeletePermit,
		want error,
	) {
		t.Helper()
		fixture.resolver.calls = 0
		fixture.dialer.calls = 0
		client := &recoveryLocalSFTPClient{}
		target := fixture.targetWithClient(client)
		target.now = func() time.Time { return fixture.now }
		result, observedErr := target.Delete(context.Background(), candidate, request)
		if observedErr != want || result != (TargetWriteResult{}) {
			t.Fatalf("delete authority result=%+v error=%v, want zero result and exact %v", result, observedErr, want)
		}
		if fixture.resolver.calls != 0 || fixture.dialer.calls != 0 ||
			recoveryLocalSFTPCallCountForTest(client) != 0 {
			t.Fatalf("closed delete authority opened dependency: resolver=%d dialer=%d sftp=%d",
				fixture.resolver.calls, fixture.dialer.calls, recoveryLocalSFTPCallCountForTest(client))
		}
	}

	if err := permit.validateRequestAt(fixture.now, request); err != nil {
		t.Fatalf("exact consumed delete authority no longer validates at R50 boundary: %v", err)
	}
	mutations := []struct {
		name   string
		mutate func(*TargetDeletePermit)
	}{
		{name: "job", mutate: func(value *TargetDeletePermit) { value.proof.jobID = strings.Repeat("a", 32) }},
		{name: "item", mutate: func(value *TargetDeletePermit) { value.proof.jobItemID = strings.Repeat("a", 32) }},
		{name: "operation", mutate: func(value *TargetDeletePermit) {
			value.proof.operationDigest = strings.Repeat("a", sha256DigestLength)
		}},
		{name: "checkpoint", mutate: func(value *TargetDeletePermit) {
			value.proof.consumedCheckpointID = strings.Repeat("a", 32)
		}},
		{name: "grant id", mutate: func(value *TargetDeletePermit) {
			value.proof.consumedGrantID = strings.Repeat("a", 32)
		}},
		{name: "grant digest", mutate: func(value *TargetDeletePermit) {
			value.proof.consumedGrantDigest = strings.Repeat("a", sha256DigestLength)
		}},
		{name: "mode", mutate: func(value *TargetDeletePermit) { value.proof.targetMode = TargetModeIsolated }},
		{name: "object", mutate: func(value *TargetDeletePermit) {
			value.proof.object.PrivateRelativeLocator += "-substituted"
		}},
		{name: "prior", mutate: func(value *TargetDeletePermit) {
			value.proof.expectedPrior.Digest = strings.Repeat("a", sha256DigestLength)
		}},
		{name: "prior bytes", mutate: func(value *TargetDeletePermit) { value.proof.expectedPriorBytes++ }},
		{name: "key version", mutate: func(value *TargetDeletePermit) { value.proof.artifacts.keyVersion++ }},
		{name: "binding", mutate: func(value *TargetDeletePermit) {
			value.proof.bindingDigest = strings.Repeat("a", sha256DigestLength)
		}},
	}
	for _, testCase := range mutations {
		t.Run(testCase.name, func(t *testing.T) {
			candidate := permit
			clonedProof := *permit.proof
			candidate.proof = &clonedProof
			testCase.mutate(&candidate)
			assertClosedBeforeDependency(t, candidate, ErrInvalidTargetPermit)
		})
	}

	for name, candidate := range map[string]TargetDeletePermit{
		"create":    {permit: fixture.writePermit.permit},
		"overwrite": {permit: mutation},
		"finalize":  {permit: TargetFinalizeOverwritePermit{permit: mutation}.permit},
		"cleanup":   {},
	} {
		t.Run("rejects "+name+" permit", func(t *testing.T) {
			assertClosedBeforeDependency(t, candidate, ErrInvalidTargetPermit)
		})
	}
}

func TestRecoveryDeleteTupleClassifier(t *testing.T) {
	binding := recoveryTargetSessionBindingForTest(t)
	material := backupasset.DomainKeyMaterial{
		ID: strings.Repeat("f", 32), Domain: backupasset.KeyDomainRecoveryCleanupOwnership,
		Version: 7, State: backupasset.DomainKeyActive,
		Key: bytes.Repeat([]byte{0x5d}, sha256.Size),
	}
	priorPayload := []byte("exact delete tuple prior")
	priorSum := sha256.Sum256(priorPayload)
	object := TargetObjectRef{
		RootID: binding.RootID, RootLocatorDigest: binding.RootLocatorDigest,
		PrivateRelativeLocator: "delete-parent/delete-tuple-target",
	}
	object.TargetPathDigest = mustTargetPathDigest(
		t, object.RootID, object.RootLocatorDigest, object.PrivateRelativeLocator,
	)
	proof := targetDeletePermitProof{
		sessionBinding:       binding,
		jobID:                strings.Repeat("1", 32),
		jobItemID:            strings.Repeat("4", 32),
		operationDigest:      strings.Repeat("5", sha256DigestLength),
		consumedCheckpointID: strings.Repeat("6", 32),
		consumedGrantID:      strings.Repeat("7", 32),
		consumedGrantDigest:  strings.Repeat("8", sha256DigestLength),
		currentAttemptID:     strings.Repeat("2", 32),
		currentAttemptFence:  19,
		currentNodeLeaseID:   strings.Repeat("3", 32),
		currentNodeFence:     23,
		currentSourceFence: backupasset.LeaseFence{
			LeaseID: strings.Repeat("1", 32), RecoveryPointID: strings.Repeat("4", 32),
			HolderType: backupasset.LeaseHolderRecoveryJob, OwnerID: strings.Repeat("1", 32),
			AttemptID: strings.Repeat("2", 32), FenceToken: strings.Repeat("5", 32),
		},
		targetChainRevision: "target-revision-delete-authority",
		targetMode:          TargetModeInPlace,
		object:              object,
		expectedPrior: ExpectedTargetIdentity{
			Kind: ExpectedTargetPresent, Digest: strings.Repeat("a", sha256DigestLength),
		},
		expectedPriorBytes: -1,
	}
	var err error
	proof.artifacts, err = deriveRecoveryDeleteArtifactBinding(
		material,
		recoveryDeleteArtifactBindingInput{
			keyVersion: material.Version,
			planID:     binding.PlanID, planBindingDigest: binding.PlanBindingDigest,
			jobID: proof.jobID, jobItemID: proof.jobItemID,
			operationDigest:      proof.operationDigest,
			consumedCheckpointID: proof.consumedCheckpointID,
			consumedGrantID:      proof.consumedGrantID,
			consumedGrantDigest:  proof.consumedGrantDigest,
			targetMode:           proof.targetMode,
			nodeID:               binding.NodeID,
			rootID:               binding.RootID,
			rootLocatorDigest:    binding.RootLocatorDigest,
			rootRevision:         binding.RootRevision,
			object:               object,
			expectedPrior:        proof.expectedPrior,
			expectedPriorBytes:   proof.expectedPriorBytes,
		},
	)
	if err != nil {
		t.Fatalf("derive delete tuple artifacts: %v", err)
	}

	missingEntry := recoveryDeleteEntryObservation{
		result: TargetLstatResult{Kind: TargetEntryMissing, TargetRevision: "delete-tuple-absent-revision"},
	}
	priorEntry := recoveryDeleteEntryObservation{
		result: TargetLstatResult{
			Kind: TargetEntryRegular, IdentityDigest: strings.Repeat("a", sha256DigestLength),
			TargetRevision: "delete-tuple-prior-revision",
		},
		size: int64(len(priorPayload)), mode: 0o640, uid: 501, gid: 502, mtime: 1_700_000_000,
		payloadFact: hex.EncodeToString(priorSum[:]),
	}
	missingMarker := recoveryDeleteMarkerObservation{entry: missingEntry}
	marker := func(document string) recoveryDeleteMarkerObservation {
		t.Helper()
		sum := sha256.Sum256([]byte(document))
		return recoveryDeleteMarkerObservation{
			entry: recoveryDeleteEntryObservation{
				result: TargetLstatResult{
					Kind: TargetEntryRegular, IdentityDigest: strings.Repeat("b", sha256DigestLength),
					TargetRevision: "delete-tuple-marker-revision",
				},
				size: int64(len(document)), mode: 0o600, uid: 501, gid: 502, mtime: 1_700_000_001,
				payloadFact: hex.EncodeToString(sum[:]),
			},
			document: document,
		}
	}
	intentMarker := marker(proof.artifacts.intentDocument)
	verifiedMarker := marker(proof.artifacts.verifiedDocument)
	fresh := recoveryDeleteTupleObservation{
		final: priorEntry, intent: missingMarker, captured: missingEntry, verified: missingMarker,
	}
	intent := fresh
	intent.intent = intentMarker
	captured := intent
	captured.final = missingEntry
	captured.captured = priorEntry
	verified := captured
	verified.verified = verifiedMarker
	deleted := verified
	deleted.captured = missingEntry
	verifiedOnly := deleted
	verifiedOnly.intent = missingMarker
	clean := verifiedOnly
	clean.verified = missingMarker

	assertClassification := func(
		t *testing.T,
		name string,
		observation recoveryDeleteTupleObservation,
		wantState recoveryDeleteTupleState,
		wantTransition recoveryDeleteTupleTransition,
	) {
		t.Helper()
		beforeObservation := observation
		beforeProof := proof
		beforeKey := append([]byte(nil), material.Key...)
		got := classifyRecoveryDeleteTuple(material, proof, observation)
		if got.state != wantState || got.transition != wantTransition {
			t.Fatalf("%s classification=%+v, want state=%d transition=%d",
				name, got, wantState, wantTransition)
		}
		if observation != beforeObservation || proof != beforeProof || !bytes.Equal(material.Key, beforeKey) {
			t.Fatalf("%s classifier mutated its pure inputs", name)
		}
	}

	for _, testCase := range []struct {
		name        string
		observation recoveryDeleteTupleObservation
		state       recoveryDeleteTupleState
		transition  recoveryDeleteTupleTransition
	}{
		{name: "fresh", observation: fresh, state: recoveryDeleteTupleFresh, transition: recoveryDeleteTupleCreateIntent},
		{name: "intent", observation: intent, state: recoveryDeleteTupleIntent, transition: recoveryDeleteTupleCapture},
		{name: "captured", observation: captured, state: recoveryDeleteTupleCaptured, transition: recoveryDeleteTupleVerifyCaptured},
		{name: "verified", observation: verified, state: recoveryDeleteTupleVerified, transition: recoveryDeleteTupleDeleteCaptured},
		{name: "deleted with markers", observation: deleted, state: recoveryDeleteTupleDeleted, transition: recoveryDeleteTupleRemoveIntent},
		{name: "deleted with verified marker", observation: verifiedOnly, state: recoveryDeleteTupleDeleted, transition: recoveryDeleteTupleRemoveVerified},
		{name: "exact clean", observation: clean, state: recoveryDeleteTupleClean, transition: recoveryDeleteTupleComplete},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			assertClassification(t, testCase.name, testCase.observation, testCase.state, testCase.transition)
		})
	}

	t.Run("exact authenticated markers are reusable", func(t *testing.T) {
		replayed := verified
		replayed.intent = marker(string(append([]byte(nil), proof.artifacts.intentDocument...)))
		replayed.verified = marker(string(append([]byte(nil), proof.artifacts.verifiedDocument...)))
		assertClassification(
			t, "exact marker replay", replayed,
			recoveryDeleteTupleVerified, recoveryDeleteTupleDeleteCaptured,
		)
	})

	for _, testCase := range []struct {
		name   string
		mutate func(*recoveryDeleteTupleObservation)
	}{
		{name: "malformed intent document", mutate: func(value *recoveryDeleteTupleObservation) {
			value.intent = marker("{")
		}},
		{name: "forged intent document", mutate: func(value *recoveryDeleteTupleObservation) {
			value.intent = marker(string(setRecoveryWorkspaceMarkerFieldForTest(
				t, []byte(proof.artifacts.intentDocument), "authentication_tag", strings.Repeat("A", 43),
			)))
		}},
		{name: "wrong marker phase", mutate: func(value *recoveryDeleteTupleObservation) {
			value.intent = verifiedMarker
		}},
		{name: "wrong marker key version", mutate: func(value *recoveryDeleteTupleObservation) {
			document, encodeErr := encodeRecoveryDeleteMarkerDocument(
				material.Key, material.Version+1, "intent", proof.artifacts.bindingDigest,
			)
			if encodeErr != nil {
				t.Fatalf("encode wrong-version marker: %v", encodeErr)
			}
			value.intent = marker(document)
		}},
		{name: "wrong marker binding", mutate: func(value *recoveryDeleteTupleObservation) {
			document, encodeErr := encodeRecoveryDeleteMarkerDocument(
				material.Key, material.Version, "intent", strings.Repeat("c", sha256DigestLength),
			)
			if encodeErr != nil {
				t.Fatalf("encode wrong-binding marker: %v", encodeErr)
			}
			value.intent = marker(document)
		}},
		{name: "wrong marker bytes", mutate: func(value *recoveryDeleteTupleObservation) {
			value.intent = marker(proof.artifacts.intentDocument + " ")
		}},
		{name: "wrong marker observation bytes", mutate: func(value *recoveryDeleteTupleObservation) {
			value.intent.document += " "
		}},
		{name: "wrong marker type", mutate: func(value *recoveryDeleteTupleObservation) {
			value.intent.entry.result.Kind = TargetEntrySymlink
		}},
		{name: "wrong marker mode", mutate: func(value *recoveryDeleteTupleObservation) {
			value.intent.entry.mode = 0o640
		}},
		{name: "external final winner", mutate: func(value *recoveryDeleteTupleObservation) {
			value.final = priorEntry
			value.final.payloadFact = strings.Repeat("d", sha256DigestLength)
		}},
		{name: "captured artifact collision", mutate: func(value *recoveryDeleteTupleObservation) {
			value.captured = priorEntry
			value.captured.result.Kind = TargetEntryDirectory
		}},
		{name: "final and captured both present", mutate: func(value *recoveryDeleteTupleObservation) {
			value.final = priorEntry
		}},
		{name: "captured disappeared before verification", mutate: func(value *recoveryDeleteTupleObservation) {
			value.captured = missingEntry
			value.verified = missingMarker
		}},
		{name: "verified without intent", mutate: func(value *recoveryDeleteTupleObservation) {
			value.intent = missingMarker
		}},
		{name: "verified marker before capture", mutate: func(value *recoveryDeleteTupleObservation) {
			value.final = priorEntry
			value.captured = missingEntry
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			candidate := verified
			testCase.mutate(&candidate)
			assertClassification(
				t, testCase.name, candidate,
				recoveryDeleteTupleConflict, recoveryDeleteTupleStop,
			)
		})
	}
}

type recoverySFTPDeleteCaptureCaseForTest struct {
	fixture      *recoveryLocalSFTPTargetFixture
	base         *recoveryLocalSFTPClient
	client       *recoveryScriptedSFTPClient
	target       *recoverySFTPTarget
	permit       TargetDeletePermit
	request      TargetDeleteRequest
	priorPayload []byte
	finalPath    string
	intentPath   string
	capturedPath string
	verifiedPath string
}

func newRecoverySFTPDeleteCaptureCaseForTest(
	t *testing.T,
	priorPayload []byte,
) *recoverySFTPDeleteCaptureCaseForTest {
	t.Helper()
	fixture := newRecoveryLocalSFTPTargetFixture(t)
	jobID := fixture.writePermit.permit.JobID
	privateRelativeLocator := "delete-parent/delete-target.bin"
	finalPath := filepath.Join(fixture.root, filepath.FromSlash(privateRelativeLocator))
	if err := os.MkdirAll(filepath.Dir(finalPath), 0o711); err != nil {
		t.Fatalf("create delete target parent: %v", err)
	}
	if err := os.Chmod(filepath.Dir(finalPath), 0o711); err != nil {
		t.Fatalf("chmod delete target parent: %v", err)
	}
	if err := os.WriteFile(finalPath, priorPayload, 0o640); err != nil {
		t.Fatalf("write exact delete prior: %v", err)
	}
	if err := os.Chmod(finalPath, 0o640); err != nil {
		t.Fatalf("chmod exact delete prior: %v", err)
	}
	priorSum := sha256.Sum256(priorPayload)
	priorInfo, err := os.Lstat(finalPath)
	if err != nil {
		t.Fatalf("lstat exact delete prior: %v", err)
	}
	priorInfo = recoveryOwnedFileInfoForOverwriteTest(priorInfo)
	object := TargetObjectRef{
		RootID: fixture.binding.RootID, RootLocatorDigest: fixture.binding.RootLocatorDigest,
		PrivateRelativeLocator: privateRelativeLocator,
	}
	object.TargetPathDigest = mustTargetPathDigest(
		t, object.RootID, object.RootLocatorDigest, object.PrivateRelativeLocator,
	)
	proof := targetDeletePermitProof{
		sessionBinding:       fixture.binding,
		jobID:                jobID,
		jobItemID:            strings.Repeat("4", 32),
		operationDigest:      strings.Repeat("5", sha256DigestLength),
		consumedCheckpointID: strings.Repeat("6", 32),
		consumedGrantID:      strings.Repeat("7", 32),
		consumedGrantDigest:  strings.Repeat("8", sha256DigestLength),
		currentAttemptID:     strings.Repeat("2", 32),
		currentAttemptFence:  19,
		currentNodeLeaseID:   strings.Repeat("3", 32),
		currentNodeFence:     23,
		currentSourceFence: backupasset.LeaseFence{
			LeaseID: strings.Repeat("1", 32), RecoveryPointID: strings.Repeat("4", 32),
			HolderType: backupasset.LeaseHolderRecoveryJob, OwnerID: jobID,
			AttemptID: strings.Repeat("2", 32), FenceToken: strings.Repeat("5", 32),
		},
		targetChainRevision: "target-revision-delete-capture",
		targetMode:          TargetModeInPlace,
		object:              object,
		expectedPrior: ExpectedTargetIdentity{
			Kind: ExpectedTargetPresent,
			Digest: recoveryDeleteEntryIdentityForTest(
				t, fixture.binding.RootRevision, privateRelativeLocator,
				TargetEntryRegular, priorInfo, 501, 502, hex.EncodeToString(priorSum[:]),
			),
		},
		expectedPriorBytes: -1,
	}
	proof.artifacts, err = deriveRecoveryDeleteArtifactBinding(
		fixture.material,
		recoveryDeleteArtifactBindingInput{
			keyVersion:        fixture.material.Version,
			planID:            fixture.binding.PlanID,
			planBindingDigest: fixture.binding.PlanBindingDigest,
			jobID:             proof.jobID, jobItemID: proof.jobItemID,
			operationDigest:      proof.operationDigest,
			consumedCheckpointID: proof.consumedCheckpointID,
			consumedGrantID:      proof.consumedGrantID,
			consumedGrantDigest:  proof.consumedGrantDigest,
			targetMode:           proof.targetMode, nodeID: fixture.binding.NodeID,
			rootID: fixture.binding.RootID, rootLocatorDigest: fixture.binding.RootLocatorDigest,
			rootRevision: fixture.binding.RootRevision, object: object,
			expectedPrior: proof.expectedPrior, expectedPriorBytes: proof.expectedPriorBytes,
		},
	)
	if err != nil {
		t.Fatalf("derive delete capture artifacts: %v", err)
	}
	mutation := issueTargetMutationPermit(TargetMutationPermit{
		SchemaVersion: 1, NodeID: fixture.binding.NodeID, Purpose: TargetPurposeWrite,
		RootID: object.RootID, RootLocatorDigest: object.RootLocatorDigest,
		TargetPathDigest: object.TargetPathDigest, RootRevision: fixture.binding.RootRevision,
		ExpiresAt: fixture.now.Add(time.Minute), UseLatchID: RecoverySchemaUseLatchID,
		JobID: jobID, AttemptID: strings.Repeat("2", 32), NodeLeaseID: strings.Repeat("3", 32),
		AttemptFence: 19, NodeFence: 23,
		ExpectedTargetRevision: "target-revision-delete-capture",
	}, func(time.Time) error { return nil }, fixture.binding)
	permit := issueTargetDeletePermit(mutation, proof)
	if permit.proof == nil {
		t.Fatal("exact delete capture authority was not sealed")
	}
	base := &recoveryLocalSFTPClient{}
	client := &recoveryScriptedSFTPClient{base: base}
	parent := filepath.Dir(finalPath)
	intentPath := filepath.Join(parent, proof.artifacts.intentComponent)
	capturedPath := filepath.Join(parent, proof.artifacts.capturedComponent)
	verifiedPath := filepath.Join(parent, proof.artifacts.verifiedComponent)
	client.lstat = func(value string, _ int) (os.FileInfo, error) {
		info, lstatErr := base.Lstat(value)
		if lstatErr == nil && (value == finalPath || value == intentPath ||
			value == capturedPath || value == verifiedPath) {
			return recoveryOwnedFileInfoForOverwriteTest(info), nil
		}
		return info, lstatErr
	}
	target := fixture.targetWithClient(client)
	target.now = func() time.Time { return fixture.now }
	return &recoverySFTPDeleteCaptureCaseForTest{
		fixture: fixture, base: base, client: client, target: target,
		permit: permit, request: TargetDeleteRequest{Object: object},
		priorPayload: append([]byte(nil), priorPayload...), finalPath: finalPath,
		intentPath: intentPath, capturedPath: capturedPath, verifiedPath: verifiedPath,
	}
}

func (testCase *recoverySFTPDeleteCaptureCaseForTest) delete() (TargetWriteResult, error) {
	return testCase.target.Delete(context.Background(), testCase.permit, testCase.request)
}

func (testCase *recoverySFTPDeleteCaptureCaseForTest) captureOnly() error {
	authority, err := testCase.permit.authorityAt(testCase.fixture.now, testCase.request)
	if err != nil {
		return err
	}
	paths, err := recoveryDeleteArtifactPathsFor(testCase.finalPath, authority.artifacts)
	if err != nil {
		return err
	}
	validateLive := func() error {
		current, currentErr := testCase.permit.authorityAt(testCase.fixture.now, testCase.request)
		if currentErr != nil {
			return currentErr
		}
		if current != authority {
			return ErrInvalidTargetPermit
		}
		return nil
	}
	if err := createRecoveryDeleteIntent(
		testCase.client, testCase.fixture.material, authority, paths, validateLive,
	); err != nil {
		return err
	}
	return captureRecoveryDeleteMutationInstantObject(
		testCase.client, testCase.fixture.material, authority, paths, validateLive,
	)
}

func assertRecoveryDeleteIntentForTest(
	t *testing.T,
	testCase *recoverySFTPDeleteCaptureCaseForTest,
) {
	t.Helper()
	intent, err := os.ReadFile(testCase.intentPath)
	if err != nil || string(intent) != testCase.permit.proof.artifacts.intentDocument {
		t.Fatalf("delete intent=%q error=%v, want exact authenticated document", intent, err)
	}
	info, err := os.Lstat(testCase.intentPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("delete intent info=%v error=%v, want exact regular 0600", info, err)
	}
	if _, err := os.Lstat(testCase.verifiedPath); !os.IsNotExist(err) {
		t.Fatalf("R50 created verified marker early: %v", err)
	}
}

func TestRecoverySFTPTargetDeleteCapturesMutationInstantObject(t *testing.T) {
	t.Run("exact prior is captured after live revalidation", func(t *testing.T) {
		testCase := newRecoverySFTPDeleteCaptureCaseForTest(t, []byte("exact delete prior payload"))
		validationCalls := 0
		testCase.permit.permit.proof.validateAt = func(time.Time) error {
			validationCalls++
			return nil
		}
		testCase.client.rename = func(oldName, newName string) error {
			if oldName != testCase.finalPath || newName != testCase.capturedPath || validationCalls < 3 {
				t.Fatalf("delete capture rename=%q -> %q validations=%d, want final -> captured after live revalidation",
					oldName, newName, validationCalls)
			}
			return testCase.base.Rename(oldName, newName)
		}

		if err := testCase.captureOnly(); err != nil {
			t.Fatalf("R50 exact capture error=%v, want captured tuple", err)
		}
		if !reflect.DeepEqual(testCase.base.renamePaths, [][2]string{{testCase.finalPath, testCase.capturedPath}}) {
			t.Fatalf("exact delete capture renames=%v, want one standard no-overwrite capture",
				testCase.base.renamePaths)
		}
		captured, readErr := os.ReadFile(testCase.capturedPath)
		if readErr != nil || !bytes.Equal(captured, testCase.priorPayload) {
			t.Fatalf("captured exact prior=%q error=%v", captured, readErr)
		}
		if _, statErr := os.Lstat(testCase.finalPath); !os.IsNotExist(statErr) {
			t.Fatalf("captured exact prior left final: %v", statErr)
		}
		assertRecoveryDeleteIntentForTest(t, testCase)
		if testCase.base.removeCalls != 0 {
			t.Fatalf("R50 removed captured evidence: %v", testCase.base.removePaths)
		}
	})

	type raceCase struct {
		name    string
		install func(*testing.T, string)
		assert  func(*testing.T, string)
	}
	regularWinner := []byte("different mutation-instant regular winner")
	linkWinner := "relative-mutation-instant-winner"
	cases := []raceCase{
		{
			name: "regular",
			install: func(t *testing.T, value string) {
				t.Helper()
				if err := os.WriteFile(value, regularWinner, 0o640); err != nil {
					t.Fatalf("install raced regular delete winner: %v", err)
				}
			},
			assert: func(t *testing.T, value string) {
				t.Helper()
				got, err := os.ReadFile(value)
				if err != nil || !bytes.Equal(got, regularWinner) {
					t.Fatalf("restored raced regular=%q error=%v", got, err)
				}
			},
		},
		{
			name: "directory",
			install: func(t *testing.T, value string) {
				t.Helper()
				if err := os.Mkdir(value, 0o750); err != nil {
					t.Fatalf("install raced delete directory: %v", err)
				}
			},
			assert: func(t *testing.T, value string) {
				t.Helper()
				info, err := os.Lstat(value)
				if err != nil || !info.IsDir() {
					t.Fatalf("restored raced directory info=%v error=%v", info, err)
				}
			},
		},
		{
			name: "symlink",
			install: func(t *testing.T, value string) {
				t.Helper()
				if err := os.Symlink(linkWinner, value); err != nil {
					t.Fatalf("install raced delete symlink: %v", err)
				}
			},
			assert: func(t *testing.T, value string) {
				t.Helper()
				got, err := os.Readlink(value)
				if err != nil || got != linkWinner {
					t.Fatalf("restored raced symlink=%q error=%v", got, err)
				}
			},
		},
		{
			name: "special",
			install: func(t *testing.T, value string) {
				t.Helper()
				if err := syscall.Mkfifo(value, 0o600); err != nil {
					t.Fatalf("install raced delete fifo: %v", err)
				}
			},
			assert: func(t *testing.T, value string) {
				t.Helper()
				info, err := os.Lstat(value)
				if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
					t.Fatalf("restored raced fifo info=%v error=%v", info, err)
				}
			},
		},
	}
	for _, entry := range cases {
		t.Run("mutation-instant "+entry.name+" controls result", func(t *testing.T) {
			testCase := newRecoverySFTPDeleteCaptureCaseForTest(t, []byte("prevalidated delete prior"))
			testCase.client.rename = func(oldName, newName string) error {
				if oldName == testCase.finalPath && newName == testCase.capturedPath {
					if err := os.Remove(testCase.finalPath); err != nil {
						t.Fatalf("remove prevalidated delete prior before race: %v", err)
					}
					entry.install(t, testCase.finalPath)
				}
				return testCase.base.Rename(oldName, newName)
			}

			result, err := testCase.delete()
			if result != (TargetWriteResult{}) || err != ErrRecoveryTargetChanged {
				t.Fatalf("raced %s delete result=%+v error=%v, want zero/exact target changed",
					entry.name, result, err)
			}
			wantRenames := [][2]string{
				{testCase.finalPath, testCase.capturedPath},
				{testCase.capturedPath, testCase.finalPath},
			}
			if !reflect.DeepEqual(testCase.base.renamePaths, wantRenames) {
				t.Fatalf("raced %s renames=%v, want capture then same-invocation restore",
					entry.name, testCase.base.renamePaths)
			}
			entry.assert(t, testCase.finalPath)
			if _, statErr := os.Lstat(testCase.capturedPath); !os.IsNotExist(statErr) {
				t.Fatalf("restored raced %s left captured artifact: %v", entry.name, statErr)
			}
			assertRecoveryDeleteIntentForTest(t, testCase)
			if testCase.base.removeCalls != 0 {
				t.Fatalf("raced %s delete removed evidence: %v", entry.name, testCase.base.removePaths)
			}
		})
	}

	t.Run("re-entry mismatch has no restore authority", func(t *testing.T) {
		testCase := newRecoverySFTPDeleteCaptureCaseForTest(t, []byte("re-entry expected prior"))
		if err := os.Remove(testCase.finalPath); err != nil {
			t.Fatalf("remove re-entry final: %v", err)
		}
		if err := os.WriteFile(
			testCase.intentPath,
			[]byte(testCase.permit.proof.artifacts.intentDocument),
			0o600,
		); err != nil {
			t.Fatalf("install re-entry intent: %v", err)
		}
		mismatch := []byte("re-entry mismatched captured evidence")
		if err := os.WriteFile(testCase.capturedPath, mismatch, 0o640); err != nil {
			t.Fatalf("install re-entry captured mismatch: %v", err)
		}

		result, err := testCase.delete()
		if result != (TargetWriteResult{}) || err != ErrRecoveryTargetChanged {
			t.Fatalf("re-entry mismatch result=%+v error=%v, want zero/exact changed", result, err)
		}
		got, readErr := os.ReadFile(testCase.capturedPath)
		if readErr != nil || !bytes.Equal(got, mismatch) || testCase.base.renameCalls != 0 ||
			testCase.base.removeCalls != 0 {
			t.Fatalf("re-entry mismatch changed evidence: bytes=%q error=%v rename=%d remove=%d",
				got, readErr, testCase.base.renameCalls, testCase.base.removeCalls)
		}
		if _, statErr := os.Lstat(testCase.finalPath); !os.IsNotExist(statErr) {
			t.Fatalf("re-entry mismatch restored final without ephemeral authority: %v", statErr)
		}
	})

	t.Run("captured collision is preserved", func(t *testing.T) {
		testCase := newRecoverySFTPDeleteCaptureCaseForTest(t, []byte("delete collision prior"))
		collision := []byte("external captured collision")
		if err := os.WriteFile(testCase.capturedPath, collision, 0o640); err != nil {
			t.Fatalf("install captured collision: %v", err)
		}
		result, err := testCase.delete()
		if result != (TargetWriteResult{}) || err != ErrRecoveryTargetChanged {
			t.Fatalf("captured collision result=%+v error=%v, want zero/exact target changed", result, err)
		}
		got, readErr := os.ReadFile(testCase.capturedPath)
		if readErr != nil || !bytes.Equal(got, collision) || testCase.base.renameCalls != 0 ||
			testCase.base.removeCalls != 0 {
			t.Fatalf("captured collision changed: bytes=%q error=%v rename=%d remove=%d",
				got, readErr, testCase.base.renameCalls, testCase.base.removeCalls)
		}
	})
}

func prepareRecoveryDeleteCapturedEntryForTest(
	t *testing.T,
	testCase *recoverySFTPDeleteCaptureCaseForTest,
) {
	t.Helper()
	if err := os.Remove(testCase.finalPath); err != nil {
		t.Fatalf("remove delete final before captured identity fixture: %v", err)
	}
}

func observeRecoveryDeleteCapturedEntryForTest(
	t *testing.T,
	testCase *recoverySFTPDeleteCaptureCaseForTest,
) (recoveryDeleteEntryObservation, error) {
	t.Helper()
	return observeRecoveryDeleteCapturedEntry(
		testCase.client, testCase.fixture.binding,
		testCase.permit.proof.jobID, testCase.capturedPath, testCase.request.Object,
	)
}

func TestRecoverySFTPTargetDeleteCapturedIdentityMatrix(t *testing.T) {
	t.Run("captured identity remains bound to final locator", func(t *testing.T) {
		value := newRecoverySFTPDeleteCaptureCaseForTest(t, []byte("captured identity binding"))
		if err := value.base.Rename(value.finalPath, value.capturedPath); err != nil {
			t.Fatalf("capture exact identity fixture: %v", err)
		}
		observation, err := observeRecoveryDeleteCapturedEntryForTest(t, value)
		if err != nil || observation.result.IdentityDigest != value.permit.proof.expectedPrior.Digest {
			t.Fatalf("captured identity=%q error=%v, want final-bound %q with observation=%+v",
				observation.result.IdentityDigest, err,
				value.permit.proof.expectedPrior.Digest, observation)
		}
	})

	t.Run("captured remote tuple retains durable prior identity", func(t *testing.T) {
		value := newRecoverySFTPDeleteCaptureCaseForTest(t, []byte("captured tuple binding"))
		if err := os.WriteFile(
			value.intentPath, []byte(value.permit.proof.artifacts.intentDocument), 0o600,
		); err != nil {
			t.Fatalf("install exact delete intent: %v", err)
		}
		if err := value.base.Rename(value.finalPath, value.capturedPath); err != nil {
			t.Fatalf("capture exact tuple fixture: %v", err)
		}
		paths, err := recoveryDeleteArtifactPathsFor(value.finalPath, value.permit.proof.artifacts)
		if err != nil {
			t.Fatalf("derive exact delete tuple paths: %v", err)
		}
		tuple, err := observeRecoveryDeleteTuple(value.client, *value.permit.proof, paths)
		classification := classifyRecoveryDeleteTuple(value.fixture.material, *value.permit.proof, tuple)
		if err != nil || classification.state != recoveryDeleteTupleCaptured {
			t.Fatalf("captured tuple classification=%+v error=%v tuple=%+v, want captured",
				classification, err, tuple)
		}
	})

	t.Run("capture transition reaches exact captured tuple", func(t *testing.T) {
		value := newRecoverySFTPDeleteCaptureCaseForTest(t, []byte("capture transition binding"))
		paths, err := recoveryDeleteArtifactPathsFor(value.finalPath, value.permit.proof.artifacts)
		if err != nil {
			t.Fatalf("derive capture transition paths: %v", err)
		}
		freshTuple, freshErr := observeRecoveryDeleteTuple(value.client, *value.permit.proof, paths)
		freshClassification := classifyRecoveryDeleteTuple(
			value.fixture.material, *value.permit.proof, freshTuple,
		)
		if freshErr != nil || freshClassification.state != recoveryDeleteTupleFresh {
			t.Fatalf("fresh tuple error=%v classification=%+v tuple=%+v expected=%+v",
				freshErr, freshClassification, freshTuple, value.permit.proof.expectedPrior)
		}
		if err := createRecoveryDeleteIntent(
			value.client, value.fixture.material, *value.permit.proof, paths, func() error { return nil },
		); err != nil {
			t.Fatalf("create exact delete intent: %v", err)
		}
		if err := captureRecoveryDeleteMutationInstantObject(
			value.client, value.fixture.material, *value.permit.proof, paths, func() error { return nil },
		); err != nil {
			tuple, tupleErr := observeRecoveryDeleteTuple(value.client, *value.permit.proof, paths)
			classification := classifyRecoveryDeleteTuple(value.fixture.material, *value.permit.proof, tuple)
			t.Fatalf("capture transition error=%v tuple_error=%v classification=%+v tuple=%+v",
				err, tupleErr, classification, tuple)
		}
	})

	for _, testCase := range []struct {
		name    string
		payload []byte
	}{
		{name: "zero", payload: nil},
		{name: "ordinary", payload: []byte("ordinary captured delete payload")},
		{name: "bounded large", payload: bytes.Repeat([]byte("bounded-delete-payload-"), 16<<10)},
	} {
		t.Run("regular "+testCase.name, func(t *testing.T) {
			value := newRecoverySFTPDeleteCaptureCaseForTest(t, []byte("unused regular prior"))
			prepareRecoveryDeleteCapturedEntryForTest(t, value)
			if err := os.WriteFile(value.capturedPath, testCase.payload, 0o640); err != nil {
				t.Fatalf("write %s captured regular: %v", testCase.name, err)
			}
			observation, err := observeRecoveryDeleteCapturedEntryForTest(t, value)
			wantDigest := sha256.Sum256(testCase.payload)
			if err != nil || observation.result.Kind != TargetEntryRegular ||
				observation.size != int64(len(testCase.payload)) || observation.mode.Perm() != 0o640 ||
				observation.payloadFact != hex.EncodeToString(wantDigest[:]) ||
				!validDigest(observation.result.IdentityDigest) {
				t.Fatalf("%s captured regular observation=%+v error=%v", testCase.name, observation, err)
			}
			if value.base.readBytes != len(testCase.payload)*2 || value.base.maxReadRequest > 32<<10 {
				t.Fatalf("%s captured reads=%d max_request=%d, want two exact reads with bounded buffer",
					testCase.name, value.base.readBytes, value.base.maxReadRequest)
			}
		})
	}

	t.Run("exact symlink", func(t *testing.T) {
		value := newRecoverySFTPDeleteCaptureCaseForTest(t, []byte("unused symlink prior"))
		prepareRecoveryDeleteCapturedEntryForTest(t, value)
		linkTarget := "relative-captured-delete-link"
		if err := os.Symlink(linkTarget, value.capturedPath); err != nil {
			t.Fatalf("install captured symlink: %v", err)
		}
		observation, err := observeRecoveryDeleteCapturedEntryForTest(t, value)
		if err != nil || observation.result.Kind != TargetEntrySymlink ||
			observation.payloadFact != linkTarget || value.client.readLinkCalls[value.capturedPath] != 2 ||
			value.base.openCalls != 0 {
			t.Fatalf("captured symlink observation=%+v error=%v readlink=%d open=%d",
				observation, err, value.client.readLinkCalls[value.capturedPath], value.base.openCalls)
		}
	})

	t.Run("exact empty directory", func(t *testing.T) {
		value := newRecoverySFTPDeleteCaptureCaseForTest(t, []byte("unused directory prior"))
		prepareRecoveryDeleteCapturedEntryForTest(t, value)
		if err := os.Mkdir(value.capturedPath, 0o750); err != nil {
			t.Fatalf("install empty captured directory: %v", err)
		}
		observation, err := observeRecoveryDeleteCapturedEntryForTest(t, value)
		if err != nil || observation.result.Kind != TargetEntryDirectory ||
			observation.payloadFact != "" || value.base.readLinkCalls != 0 || value.base.readBytes != 0 {
			t.Fatalf("captured empty directory observation=%+v error=%v readlink=%d bytes=%d",
				observation, err, value.base.readLinkCalls, value.base.readBytes)
		}
	})

	t.Run("exact special metadata", func(t *testing.T) {
		value := newRecoverySFTPDeleteCaptureCaseForTest(t, []byte("unused special prior"))
		prepareRecoveryDeleteCapturedEntryForTest(t, value)
		if err := syscall.Mkfifo(value.capturedPath, 0o600); err != nil {
			t.Fatalf("install captured fifo: %v", err)
		}
		observation, err := observeRecoveryDeleteCapturedEntryForTest(t, value)
		if err != nil || observation.result.Kind != TargetEntrySpecial ||
			observation.mode&os.ModeNamedPipe == 0 || observation.payloadFact != "" ||
			value.base.openCalls != 0 || value.base.readLinkCalls != 0 {
			t.Fatalf("captured special observation=%+v error=%v open=%d readlink=%d",
				observation, err, value.base.openCalls, value.base.readLinkCalls)
		}
	})

	t.Run("non-empty directory", func(t *testing.T) {
		value := newRecoverySFTPDeleteCaptureCaseForTest(t, []byte("unused non-empty directory prior"))
		prepareRecoveryDeleteCapturedEntryForTest(t, value)
		if err := os.Mkdir(value.capturedPath, 0o750); err != nil {
			t.Fatalf("install non-empty captured directory: %v", err)
		}
		child := filepath.Join(value.capturedPath, "external-child")
		if err := os.WriteFile(child, []byte("must remain"), 0o600); err != nil {
			t.Fatalf("install captured directory child: %v", err)
		}
		if observation, err := observeRecoveryDeleteCapturedEntryForTest(t, value); observation != (recoveryDeleteEntryObservation{}) || err != ErrRecoveryTargetChanged {
			t.Fatalf("non-empty captured directory observation=%+v error=%v, want zero/exact changed",
				observation, err)
		}
		if got, err := os.ReadFile(child); err != nil || string(got) != "must remain" {
			t.Fatalf("non-empty captured child=%q error=%v, want preserved", got, err)
		}
	})

	type regularDriftCase struct {
		name      string
		configure func(*testing.T, *recoverySFTPDeleteCaptureCaseForTest, []byte)
	}
	drifts := []regularDriftCase{
		{
			name: "short regular",
			configure: func(t *testing.T, value *recoverySFTPDeleteCaptureCaseForTest, _ []byte) {
				t.Helper()
				value.client.open = func(path string) (recoveryTargetSFTPFile, error) {
					file, err := value.base.Open(path)
					if err != nil {
						return file, err
					}
					return &recoveryScriptedSFTPFile{base: file, read: func([]byte) (int, error) {
						return 0, io.EOF
					}}, nil
				}
			},
		},
		{
			name: "extra regular",
			configure: func(t *testing.T, value *recoverySFTPDeleteCaptureCaseForTest, payload []byte) {
				t.Helper()
				value.client.open = func(path string) (recoveryTargetSFTPFile, error) {
					file, err := value.base.Open(path)
					if err != nil {
						return file, err
					}
					remaining := len(payload)
					return &recoveryScriptedSFTPFile{base: file, read: func(buffer []byte) (int, error) {
						if remaining > 0 {
							read, readErr := file.Read(buffer)
							remaining -= read
							return read, readErr
						}
						buffer[0] = 0x7f
						return 1, nil
					}}, nil
				}
			},
		},
		{
			name: "content drift",
			configure: func(t *testing.T, value *recoverySFTPDeleteCaptureCaseForTest, payload []byte) {
				t.Helper()
				opens := 0
				value.client.open = func(path string) (recoveryTargetSFTPFile, error) {
					file, err := value.base.Open(path)
					if err != nil {
						return file, err
					}
					opens++
					if opens == 1 {
						return file, nil
					}
					changed := append([]byte(nil), payload...)
					changed[0] ^= 0xff
					reader := bytes.NewReader(changed)
					return &recoveryScriptedSFTPFile{base: file, read: reader.Read}, nil
				}
			},
		},
		{
			name: "metadata drift",
			configure: func(t *testing.T, value *recoverySFTPDeleteCaptureCaseForTest, _ []byte) {
				t.Helper()
				value.client.lstat = func(path string, call int) (os.FileInfo, error) {
					info, err := value.base.Lstat(path)
					if err != nil || path != value.capturedPath {
						return info, err
					}
					owned := recoveryOwnedFileInfoForOverwriteTest(info)
					if call >= 4 {
						return recoveryProbeFileInfo{
							name: owned.Name(), size: owned.Size(), mode: owned.Mode(),
							modTime: owned.ModTime().Add(time.Second), uid: 501, gid: 502,
						}, nil
					}
					return owned, nil
				}
			},
		},
		{
			name: "kind drift",
			configure: func(t *testing.T, value *recoverySFTPDeleteCaptureCaseForTest, _ []byte) {
				t.Helper()
				value.client.lstat = func(path string, call int) (os.FileInfo, error) {
					info, err := value.base.Lstat(path)
					if err != nil || path != value.capturedPath {
						return info, err
					}
					if call >= 4 {
						return recoveryProbeFileInfo{
							name: info.Name(), size: int64(len("kind-drift-link")),
							mode: os.ModeSymlink | 0o777, modTime: info.ModTime(), uid: 501, gid: 502,
						}, nil
					}
					return recoveryOwnedFileInfoForOverwriteTest(info), nil
				}
				value.client.readLink = func(string, int) (string, error) {
					return "kind-drift-link", nil
				}
			},
		},
	}
	for _, drift := range drifts {
		t.Run(drift.name, func(t *testing.T) {
			payload := []byte("captured regular drift payload")
			value := newRecoverySFTPDeleteCaptureCaseForTest(t, []byte("unused drift prior"))
			prepareRecoveryDeleteCapturedEntryForTest(t, value)
			if err := os.WriteFile(value.capturedPath, payload, 0o640); err != nil {
				t.Fatalf("write captured drift fixture: %v", err)
			}
			drift.configure(t, value, payload)
			if observation, err := observeRecoveryDeleteCapturedEntryForTest(t, value); observation != (recoveryDeleteEntryObservation{}) || err != ErrRecoveryTargetChanged {
				t.Fatalf("%s observation=%+v error=%v, want zero/exact changed",
					drift.name, observation, err)
			}
		})
	}

	t.Run("symlink target drift", func(t *testing.T) {
		value := newRecoverySFTPDeleteCaptureCaseForTest(t, []byte("unused link drift prior"))
		prepareRecoveryDeleteCapturedEntryForTest(t, value)
		if err := os.Symlink("first-link-target", value.capturedPath); err != nil {
			t.Fatalf("install drifting captured link: %v", err)
		}
		value.client.readLink = func(string, int) (string, error) {
			if value.client.readLinkCalls[value.capturedPath] == 1 {
				return "first-link-target", nil
			}
			return "second-link-target", nil
		}
		if observation, err := observeRecoveryDeleteCapturedEntryForTest(t, value); observation != (recoveryDeleteEntryObservation{}) || err != ErrRecoveryTargetChanged {
			t.Fatalf("link drift observation=%+v error=%v, want zero/exact changed", observation, err)
		}
	})
}

func configureRecoverySFTPDeletePriorForTest(
	t *testing.T,
	testCase *recoverySFTPDeleteCaptureCaseForTest,
	install func(*testing.T, string),
) recoveryDeleteEntryObservation {
	t.Helper()
	if err := os.Remove(testCase.finalPath); err != nil {
		t.Fatalf("remove default delete prior: %v", err)
	}
	install(t, testCase.finalPath)
	observation, err := observeRecoveryDeleteEntryObservationTwice(
		testCase.client, testCase.fixture.binding, testCase.permit.proof.jobID,
		TargetModeInPlace, testCase.request.Object,
	)
	if err != nil || !validRecoveryDeletePresentObservation(observation) {
		t.Fatalf("observe configured delete prior=%+v error=%v", observation, err)
	}
	proof := *testCase.permit.proof
	proof.expectedPrior = ExpectedTargetIdentity{
		Kind: ExpectedTargetPresent, Digest: observation.result.IdentityDigest,
	}
	proof.expectedPriorBytes = -1
	proof.artifacts, err = deriveRecoveryDeleteArtifactBinding(
		testCase.fixture.material,
		recoveryDeleteArtifactBindingInput{
			keyVersion:        proof.artifacts.keyVersion,
			planID:            testCase.fixture.binding.PlanID,
			planBindingDigest: testCase.fixture.binding.PlanBindingDigest,
			jobID:             proof.jobID, jobItemID: proof.jobItemID,
			operationDigest:      proof.operationDigest,
			consumedCheckpointID: proof.consumedCheckpointID,
			consumedGrantID:      proof.consumedGrantID,
			consumedGrantDigest:  proof.consumedGrantDigest,
			targetMode:           proof.targetMode,
			nodeID:               testCase.fixture.binding.NodeID,
			rootID:               testCase.fixture.binding.RootID,
			rootLocatorDigest:    testCase.fixture.binding.RootLocatorDigest,
			rootRevision:         testCase.fixture.binding.RootRevision,
			object:               proof.object,
			expectedPrior:        proof.expectedPrior,
			expectedPriorBytes:   proof.expectedPriorBytes,
		},
	)
	if err != nil {
		t.Fatalf("derive configured delete artifacts: %v", err)
	}
	testCase.permit = issueTargetDeletePermit(testCase.permit.permit, proof)
	if testCase.permit.proof == nil {
		t.Fatal("configured exact delete authority was not sealed")
	}
	parent := filepath.Dir(testCase.finalPath)
	testCase.intentPath = filepath.Join(parent, proof.artifacts.intentComponent)
	testCase.capturedPath = filepath.Join(parent, proof.artifacts.capturedComponent)
	testCase.verifiedPath = filepath.Join(parent, proof.artifacts.verifiedComponent)
	useOwnedRecoverySFTPDeleteEntriesForTest(testCase)
	return observation
}

func useOwnedRecoverySFTPDeleteEntriesForTest(testCase *recoverySFTPDeleteCaptureCaseForTest) {
	testCase.client.lstat = func(value string, _ int) (os.FileInfo, error) {
		info, lstatErr := testCase.base.Lstat(value)
		if lstatErr == nil && (value == testCase.finalPath || value == testCase.intentPath ||
			value == testCase.capturedPath || value == testCase.verifiedPath) {
			return recoveryOwnedFileInfoForOverwriteTest(info), nil
		}
		return info, lstatErr
	}
}

func assertRecoveryDeleteMarkerDocumentForTest(
	t *testing.T,
	value string,
	document string,
) {
	t.Helper()
	encoded, err := os.ReadFile(value)
	if err != nil || string(encoded) != document {
		t.Fatalf("delete marker %q bytes=%q error=%v, want exact authenticated document",
			filepath.Base(value), encoded, err)
	}
	info, err := os.Lstat(value)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("delete marker %q info=%v error=%v, want regular 0600",
			filepath.Base(value), info, err)
	}
}

func TestRecoverySFTPTargetDeleteRemovesOnlyVerifiedCaptured(t *testing.T) {
	type entryCase struct {
		name    string
		kind    TargetEntryKind
		install func(*testing.T, string)
	}
	cases := []entryCase{
		{
			name: "regular", kind: TargetEntryRegular,
			install: func(t *testing.T, value string) {
				t.Helper()
				if err := os.WriteFile(value, []byte("R51 exact regular prior"), 0o640); err != nil {
					t.Fatalf("install exact regular delete prior: %v", err)
				}
			},
		},
		{
			name: "symlink", kind: TargetEntrySymlink,
			install: func(t *testing.T, value string) {
				t.Helper()
				if err := os.Symlink("relative-r51-target", value); err != nil {
					t.Fatalf("install exact symlink delete prior: %v", err)
				}
			},
		},
		{
			name: "empty directory", kind: TargetEntryDirectory,
			install: func(t *testing.T, value string) {
				t.Helper()
				if err := os.Mkdir(value, 0o750); err != nil {
					t.Fatalf("install exact empty directory delete prior: %v", err)
				}
			},
		},
		{
			name: "special", kind: TargetEntrySpecial,
			install: func(t *testing.T, value string) {
				t.Helper()
				if err := syscall.Mkfifo(value, 0o600); err != nil {
					t.Fatalf("install exact special delete prior: %v", err)
				}
			},
		},
	}
	for _, entry := range cases {
		t.Run(entry.name, func(t *testing.T) {
			testCase := newRecoverySFTPDeleteCaptureCaseForTest(t, []byte("replace R51 prior"))
			observation := configureRecoverySFTPDeletePriorForTest(t, testCase, entry.install)
			if observation.result.Kind != entry.kind {
				t.Fatalf("configured delete kind=%q, want %q", observation.result.Kind, entry.kind)
			}
			externalPath := filepath.Join(filepath.Dir(testCase.finalPath), "external-r51-sibling")
			external := []byte("must remain outside exact delete tuple")
			if err := os.WriteFile(externalPath, external, 0o600); err != nil {
				t.Fatalf("install external delete sibling: %v", err)
			}

			validationCalls := 0
			testCase.permit.permit.proof.validateAt = func(time.Time) error {
				validationCalls++
				return nil
			}
			assertBeforeLeafRemoval := func() {
				t.Helper()
				if validationCalls < 5 {
					t.Fatalf("captured leaf remove validations=%d, want live validation immediately before mutation",
						validationCalls)
				}
				assertRecoveryDeleteMarkerDocumentForTest(
					t, testCase.verifiedPath, testCase.permit.proof.artifacts.verifiedDocument,
				)
			}
			testCase.client.remove = func(value string) error {
				switch value {
				case testCase.capturedPath:
					if entry.kind == TargetEntryDirectory {
						t.Fatal("directory captured leaf used Remove instead of RemoveDirectory")
					}
					assertBeforeLeafRemoval()
				case testCase.intentPath:
					if validationCalls < 6 {
						t.Fatalf("intent remove validations=%d, want fresh live validation", validationCalls)
					}
				case testCase.verifiedPath:
					if validationCalls < 7 {
						t.Fatalf("verified remove validations=%d, want fresh live validation", validationCalls)
					}
				}
				return testCase.base.Remove(value)
			}
			testCase.client.removeDirectory = func(value string) error {
				if value != testCase.capturedPath || entry.kind != TargetEntryDirectory {
					t.Fatalf("RemoveDirectory path=%q kind=%q, want exact captured directory",
						value, entry.kind)
				}
				assertBeforeLeafRemoval()
				return testCase.base.RemoveDirectory(value)
			}

			result, err := testCase.delete()
			wantRevision, revisionErr := recoverySFTPTargetAbsentRevision(
				testCase.fixture.binding.RootRevision,
				testCase.request.Object.PrivateRelativeLocator,
			)
			want := TargetWriteResult{TargetRevision: wantRevision}
			if revisionErr != nil || err != nil || result != want {
				paths, pathsErr := recoveryDeleteArtifactPathsFor(
					testCase.finalPath, testCase.permit.proof.artifacts,
				)
				tuple, tupleErr := observeRecoveryDeleteTuple(testCase.client, *testCase.permit.proof, paths)
				classification := classifyRecoveryDeleteTuple(
					testCase.fixture.material, *testCase.permit.proof, tuple,
				)
				t.Fatalf("%s exact delete result=%+v error=%v revision_error=%v, want %+v; paths_error=%v tuple_error=%v classification=%+v open_file=%v chmod=%v rename=%v remove=%v remove_directory=%v validations=%d",
					entry.name, result, err, revisionErr, want, pathsErr, tupleErr, classification,
					testCase.base.openFilePaths, testCase.base.chmodPaths,
					testCase.base.renamePaths, testCase.base.removePaths,
					testCase.base.removeDirectoryPaths, validationCalls)
			}
			for _, value := range []string{
				testCase.finalPath, testCase.intentPath, testCase.capturedPath, testCase.verifiedPath,
			} {
				if _, statErr := os.Lstat(value); !os.IsNotExist(statErr) {
					t.Fatalf("%s exact delete left %q: %v", entry.name, filepath.Base(value), statErr)
				}
			}
			preserved, readErr := os.ReadFile(externalPath)
			if readErr != nil || !bytes.Equal(preserved, external) {
				t.Fatalf("%s external sibling=%q error=%v, want preserved", entry.name, preserved, readErr)
			}
			if entry.kind == TargetEntryDirectory {
				if testCase.base.removeDirectoryCalls != 1 || testCase.base.removeCalls != 2 {
					t.Fatalf("directory removes leaf=%d regular=%d, want 1 directory + 2 markers",
						testCase.base.removeDirectoryCalls, testCase.base.removeCalls)
				}
			} else if testCase.base.removeDirectoryCalls != 0 || testCase.base.removeCalls != 3 {
				t.Fatalf("%s removes leaf=%d regular=%d, want 0 directory + 3 regular",
					entry.name, testCase.base.removeDirectoryCalls, testCase.base.removeCalls)
			}
		})
	}

	t.Run("non-empty directory and child remain untouched", func(t *testing.T) {
		testCase := newRecoverySFTPDeleteCaptureCaseForTest(t, []byte("replace non-empty prior"))
		configureRecoverySFTPDeletePriorForTest(t, testCase, func(t *testing.T, value string) {
			t.Helper()
			if err := os.Mkdir(value, 0o750); err != nil {
				t.Fatalf("install non-empty directory prior: %v", err)
			}
			if err := os.WriteFile(filepath.Join(value, "external-child"), []byte("must remain"), 0o600); err != nil {
				t.Fatalf("install non-empty directory child: %v", err)
			}
		})
		result, err := testCase.delete()
		if result != (TargetWriteResult{}) || err != ErrRecoveryTargetChanged {
			t.Fatalf("non-empty directory delete result=%+v error=%v, want zero/exact changed", result, err)
		}
		if testCase.base.removeCalls != 0 || testCase.base.removeDirectoryCalls != 0 {
			t.Fatalf("non-empty directory mutated remove=%d remove_directory=%d",
				testCase.base.removeCalls, testCase.base.removeDirectoryCalls)
		}
		child := filepath.Join(testCase.capturedPath, "external-child")
		if encoded, readErr := os.ReadFile(child); readErr != nil || string(encoded) != "must remain" {
			t.Fatalf("non-empty captured child=%q error=%v, want preserved", encoded, readErr)
		}
	})
}

func TestRecoverySFTPTargetDeleteCrashStateMatrix(t *testing.T) {
	type interruption struct {
		name   string
		inject func(*recoverySFTPDeleteCaptureCaseForTest)
	}
	interruptionCases := []interruption{
		{
			name: "before intent create",
			inject: func(value *recoverySFTPDeleteCaptureCaseForTest) {
				value.client.openFile = func(path string, flags int) (recoveryTargetSFTPFile, error) {
					if path == value.intentPath {
						return nil, errors.New("scripted before delete intent create")
					}
					return value.base.OpenFile(path, flags)
				}
			},
		},
		{
			name: "after intent create",
			inject: func(value *recoverySFTPDeleteCaptureCaseForTest) {
				value.client.openFile = func(path string, flags int) (recoveryTargetSFTPFile, error) {
					file, err := value.base.OpenFile(path, flags)
					if err != nil || path != value.intentPath {
						return file, err
					}
					return &recoveryScriptedSFTPFile{base: file, close: func() error {
						if closeErr := file.Close(); closeErr != nil {
							return closeErr
						}
						return errors.New("scripted after delete intent create")
					}}, nil
				}
			},
		},
		{
			name: "before capture",
			inject: func(value *recoverySFTPDeleteCaptureCaseForTest) {
				value.client.rename = func(oldName, newName string) error {
					if oldName == value.finalPath && newName == value.capturedPath {
						return errors.New("scripted before delete capture")
					}
					return value.base.Rename(oldName, newName)
				}
			},
		},
		{
			name: "after capture",
			inject: func(value *recoverySFTPDeleteCaptureCaseForTest) {
				value.client.rename = func(oldName, newName string) error {
					if oldName == value.finalPath && newName == value.capturedPath {
						if err := value.base.Rename(oldName, newName); err != nil {
							return err
						}
						return errors.New("scripted after delete capture")
					}
					return value.base.Rename(oldName, newName)
				}
			},
		},
		{
			name: "during captured read",
			inject: func(value *recoverySFTPDeleteCaptureCaseForTest) {
				value.client.open = func(path string) (recoveryTargetSFTPFile, error) {
					if path == value.capturedPath {
						return nil, errors.New("scripted captured delete read")
					}
					return value.base.Open(path)
				}
			},
		},
		{
			name: "during captured stat",
			inject: func(value *recoverySFTPDeleteCaptureCaseForTest) {
				value.client.open = func(path string) (recoveryTargetSFTPFile, error) {
					file, err := value.base.Open(path)
					if err != nil || path != value.capturedPath {
						return file, err
					}
					return &recoveryScriptedSFTPFile{base: file, stat: func() (os.FileInfo, error) {
						return nil, errors.New("scripted captured delete stat")
					}}, nil
				}
			},
		},
		{
			name: "before verified create",
			inject: func(value *recoverySFTPDeleteCaptureCaseForTest) {
				value.client.openFile = func(path string, flags int) (recoveryTargetSFTPFile, error) {
					if path == value.verifiedPath {
						return nil, errors.New("scripted before delete verified create")
					}
					return value.base.OpenFile(path, flags)
				}
			},
		},
		{
			name: "after verified create",
			inject: func(value *recoverySFTPDeleteCaptureCaseForTest) {
				value.client.openFile = func(path string, flags int) (recoveryTargetSFTPFile, error) {
					file, err := value.base.OpenFile(path, flags)
					if err != nil || path != value.verifiedPath {
						return file, err
					}
					return &recoveryScriptedSFTPFile{base: file, close: func() error {
						if closeErr := file.Close(); closeErr != nil {
							return closeErr
						}
						return errors.New("scripted after delete verified create")
					}}, nil
				}
			},
		},
		{
			name: "before captured leaf remove",
			inject: func(value *recoverySFTPDeleteCaptureCaseForTest) {
				value.client.remove = func(path string) error {
					if path == value.capturedPath {
						return errors.New("scripted before captured leaf remove")
					}
					return value.base.Remove(path)
				}
			},
		},
		{
			name: "after captured leaf remove",
			inject: func(value *recoverySFTPDeleteCaptureCaseForTest) {
				value.client.remove = func(path string) error {
					if path == value.capturedPath {
						if err := value.base.Remove(path); err != nil {
							return err
						}
						return errors.New("scripted after captured leaf remove")
					}
					return value.base.Remove(path)
				}
			},
		},
		{
			name: "final absence observation after leaf remove",
			inject: func(value *recoverySFTPDeleteCaptureCaseForTest) {
				removed := false
				previous := value.client.lstat
				value.client.remove = func(path string) error {
					if path == value.capturedPath {
						if err := value.base.Remove(path); err != nil {
							return err
						}
						removed = true
						return errors.New("scripted before final delete absence observation")
					}
					return value.base.Remove(path)
				}
				value.client.lstat = func(path string, call int) (os.FileInfo, error) {
					if removed && path == value.finalPath {
						return nil, errors.New("scripted delete absence observation")
					}
					return previous(path, call)
				}
			},
		},
		{
			name: "captured absence observation after leaf remove",
			inject: func(value *recoverySFTPDeleteCaptureCaseForTest) {
				removed := false
				previous := value.client.lstat
				value.client.remove = func(path string) error {
					if path == value.capturedPath {
						if err := value.base.Remove(path); err != nil {
							return err
						}
						removed = true
						return errors.New("scripted before captured delete absence observation")
					}
					return value.base.Remove(path)
				}
				value.client.lstat = func(path string, call int) (os.FileInfo, error) {
					if removed && path == value.capturedPath {
						return nil, errors.New("scripted captured delete absence observation")
					}
					return previous(path, call)
				}
			},
		},
		{
			name: "before intent remove",
			inject: func(value *recoverySFTPDeleteCaptureCaseForTest) {
				value.client.remove = func(path string) error {
					if path == value.intentPath {
						return errors.New("scripted before intent remove")
					}
					return value.base.Remove(path)
				}
			},
		},
		{
			name: "after intent remove",
			inject: func(value *recoverySFTPDeleteCaptureCaseForTest) {
				value.client.remove = func(path string) error {
					if path == value.intentPath {
						if err := value.base.Remove(path); err != nil {
							return err
						}
						return errors.New("scripted after intent remove")
					}
					return value.base.Remove(path)
				}
			},
		},
		{
			name: "before verified remove",
			inject: func(value *recoverySFTPDeleteCaptureCaseForTest) {
				value.client.remove = func(path string) error {
					if path == value.verifiedPath {
						return errors.New("scripted before verified remove")
					}
					return value.base.Remove(path)
				}
			},
		},
		{
			name: "after verified remove",
			inject: func(value *recoverySFTPDeleteCaptureCaseForTest) {
				value.client.remove = func(path string) error {
					if path == value.verifiedPath {
						if err := value.base.Remove(path); err != nil {
							return err
						}
						return errors.New("scripted after verified remove")
					}
					return value.base.Remove(path)
				}
			},
		},
		{
			name: "after remote success session close",
			inject: func(value *recoverySFTPDeleteCaptureCaseForTest) {
				value.client.close = func() error {
					return errors.New("scripted delete session close")
				}
			},
		},
	}

	for _, interruption := range interruptionCases {
		t.Run(interruption.name, func(t *testing.T) {
			value := newRecoverySFTPDeleteCaptureCaseForTest(t, []byte("R51 crash matrix regular prior"))
			configureRecoverySFTPDeletePriorForTest(t, value, func(t *testing.T, path string) {
				t.Helper()
				if err := os.WriteFile(path, []byte("R51 crash matrix regular prior"), 0o640); err != nil {
					t.Fatalf("install crash matrix prior: %v", err)
				}
			})
			interruption.inject(value)
			firstResult, firstErr := value.delete()
			if firstErr != nil && firstErr != ErrRecoveryTargetUnavailable {
				t.Fatalf("first interrupted delete result=%+v error=%v, want unavailable or clean success",
					firstResult, firstErr)
			}
			if firstErr == ErrRecoveryTargetUnavailable && firstResult != (TargetWriteResult{}) {
				t.Fatalf("unavailable interrupted delete result=%+v, want zero", firstResult)
			}
			if firstErr == nil && (!validOpaqueRevision(firstResult.TargetRevision) ||
				firstResult.BytesWritten != 0 || firstResult.IdentityDigest != "") {
				t.Fatalf("adopted interrupted delete result=%+v, want exact zero-byte absence", firstResult)
			}

			retryBase := &recoveryLocalSFTPClient{}
			retryClient := &recoveryScriptedSFTPClient{base: retryBase}
			retryValue := *value
			retryValue.base = retryBase
			retryValue.client = retryClient
			retryValue.target = value.fixture.targetWithClient(retryClient)
			useOwnedRecoverySFTPDeleteEntriesForTest(&retryValue)
			retryResult, retryErr := retryValue.delete()
			wantRevision, revisionErr := recoverySFTPTargetAbsentRevision(
				retryValue.fixture.binding.RootRevision,
				retryValue.request.Object.PrivateRelativeLocator,
			)
			want := TargetWriteResult{TargetRevision: wantRevision}
			if revisionErr != nil || retryErr != nil || retryResult != want {
				t.Fatalf("retry interrupted delete result=%+v error=%v revision_error=%v, want %+v",
					retryResult, retryErr, revisionErr, want)
			}
		})
	}

	t.Run("symlink readlink interruption resumes", func(t *testing.T) {
		value := newRecoverySFTPDeleteCaptureCaseForTest(t, []byte("R51 crash matrix symlink prior"))
		configureRecoverySFTPDeletePriorForTest(t, value, func(t *testing.T, path string) {
			t.Helper()
			if err := os.Symlink("R51-crash-link-target", path); err != nil {
				t.Fatalf("install crash matrix symlink: %v", err)
			}
		})
		value.client.readLink = func(path string, call int) (string, error) {
			if path == value.capturedPath {
				return "", errors.New("scripted captured readlink")
			}
			return value.base.ReadLink(path)
		}
		firstResult, firstErr := value.delete()
		if firstResult != (TargetWriteResult{}) || firstErr != ErrRecoveryTargetUnavailable {
			t.Fatalf("symlink readlink interruption result=%+v error=%v, want zero/unavailable",
				firstResult, firstErr)
		}
		retryBase := &recoveryLocalSFTPClient{}
		retryClient := &recoveryScriptedSFTPClient{base: retryBase}
		retryValue := *value
		retryValue.base = retryBase
		retryValue.client = retryClient
		retryValue.target = value.fixture.targetWithClient(retryClient)
		useOwnedRecoverySFTPDeleteEntriesForTest(&retryValue)
		retryResult, retryErr := retryValue.delete()
		if retryErr != nil || retryResult.TargetRevision == "" {
			t.Fatalf("symlink readlink retry result=%+v error=%v, want clean success", retryResult, retryErr)
		}
	})
}

type recoverySFTPOverwritePrepareCaseForTest struct {
	fixture       *recoveryLocalSFTPTargetFixture
	base          *recoveryLocalSFTPClient
	client        *recoveryScriptedSFTPClient
	target        *recoverySFTPTarget
	permit        TargetWritePermit
	request       TargetWriteAtomicRequest
	artifacts     recoveryOverwriteArtifactBinding
	priorPayload  []byte
	postPayload   []byte
	finalPath     string
	intentPath    string
	priorPath     string
	postPath      string
	publishedPath string
}

func recoveryOverwriteFinalizeAuthorityForTest(
	t *testing.T,
	testCase *recoverySFTPOverwritePrepareCaseForTest,
) (TargetFinalizeOverwritePermit, TargetFinalizeOverwriteRequest) {
	t.Helper()
	mutation := testCase.permit.permit
	proof := targetFinalizeOverwritePermitProof{
		sessionBinding: testCase.fixture.binding,
		jobID:          mutation.JobID, jobItemID: testCase.permit.itemProof.jobItemID,
		checkpointID:           strings.Repeat("4", 32),
		operationDigest:        testCase.permit.itemProof.operationDigest,
		checkpointAttemptID:    strings.Repeat("5", 32),
		checkpointAttemptFence: 17, checkpointNodeFence: 18,
		currentAttemptID: mutation.AttemptID, currentAttemptFence: mutation.AttemptFence,
		currentNodeLeaseID: mutation.NodeLeaseID, currentNodeFence: mutation.NodeFence,
		currentSourceFence: backupasset.LeaseFence{
			LeaseID: strings.Repeat("6", 32), RecoveryPointID: strings.Repeat("7", 32),
			HolderType: backupasset.LeaseHolderRecoveryJob, OwnerID: "overwrite-finalize-worker",
			AttemptID: mutation.AttemptID, FenceToken: strings.Repeat("8", 32),
		},
		targetChainRevision: mutation.ExpectedTargetRevision,
		priorTargetRevision: "overwrite-prior-chain-revision",
		nextTargetRevision:  "overwrite-next-chain-revision",
		object:              testCase.request.Object,
		expectedPrior:       testCase.permit.itemProof.expectedPrior,
		expectedPriorBytes:  testCase.permit.itemProof.expectedPriorBytes,
		expectedPostDigest:  testCase.request.ExpectedDigest,
		expectedPostBytes:   testCase.request.ExpectedBytes,
		artifacts:           testCase.artifacts,
	}
	permit := issueTargetFinalizeOverwritePermit(mutation, proof)
	request := TargetFinalizeOverwriteRequest{
		Object: testCase.request.Object, ExpectedDigest: testCase.request.ExpectedDigest,
		ExpectedBytes: testCase.request.ExpectedBytes,
	}
	if permit.proof == nil {
		t.Fatal("exact overwrite finalize authority was not sealed")
	}
	if _, err := permit.authorityAt(testCase.fixture.now, request); err != nil {
		t.Fatalf("validate exact overwrite finalize authority: %v", err)
	}
	return permit, request
}

func newRecoverySFTPOverwritePrepareCaseForTest(
	t *testing.T,
	postPayload []byte,
) *recoverySFTPOverwritePrepareCaseForTest {
	t.Helper()
	fixture := newRecoveryLocalSFTPTargetFixture(t)
	jobID := fixture.writePermit.permit.JobID
	privateRelativeLocator := "existing/nested/target.bin"
	priorPayload := []byte("exact-prior-payload")
	finalPath := filepath.Join(fixture.root, filepath.FromSlash(privateRelativeLocator))
	if err := os.MkdirAll(filepath.Dir(finalPath), 0o711); err != nil {
		t.Fatalf("create overwrite parent: %v", err)
	}
	if err := os.Chmod(filepath.Dir(filepath.Dir(finalPath)), 0o750); err != nil {
		t.Fatalf("chmod first overwrite parent: %v", err)
	}
	if err := os.Chmod(filepath.Dir(finalPath), 0o711); err != nil {
		t.Fatalf("chmod final overwrite parent: %v", err)
	}
	if err := os.WriteFile(finalPath, priorPayload, 0o640); err != nil {
		t.Fatalf("write exact overwrite prior: %v", err)
	}
	if err := os.Chmod(finalPath, 0o640); err != nil {
		t.Fatalf("chmod exact overwrite prior: %v", err)
	}

	material, input := recoveryOverwriteArtifactBindingInputForTest(
		t, fixture.binding, jobID, privateRelativeLocator, postPayload,
	)
	priorDigest := sha256.Sum256(priorPayload)
	input.expectedPrior = ExpectedTargetIdentity{
		Kind: ExpectedTargetPresent, Digest: hex.EncodeToString(priorDigest[:]),
	}
	input.expectedPriorBytes = int64(len(priorPayload))
	artifacts, err := newRecoveryOverwriteArtifactBinding(material, input)
	if err != nil {
		t.Fatalf("derive overwrite preparation artifacts: %v", err)
	}
	mutation := issueTargetMutationPermit(TargetMutationPermit{
		SchemaVersion: 1, NodeID: fixture.binding.NodeID, Purpose: TargetPurposeWrite,
		RootID: input.object.RootID, RootLocatorDigest: input.object.RootLocatorDigest,
		TargetPathDigest: input.object.TargetPathDigest, RootRevision: fixture.binding.RootRevision,
		ExpiresAt: fixture.now.Add(time.Minute), UseLatchID: RecoverySchemaUseLatchID,
		JobID: jobID, AttemptID: strings.Repeat("2", 32), NodeLeaseID: strings.Repeat("3", 32),
		AttemptFence: 19, NodeFence: 23, ExpectedTargetRevision: "target-revision-overwrite-prepare",
	}, func(time.Time) error { return nil }, fixture.binding)
	basePermit, err := NewTargetWritePermit(mutation, fixture.now)
	if err != nil {
		t.Fatalf("construct overwrite preparation permit: %v", err)
	}
	permit := issueTargetItemWritePermit(basePermit, targetItemWritePermitProof{
		sessionBinding: fixture.binding, jobID: jobID,
		jobItemID: input.jobItemID, operationDigest: input.operationDigest,
		targetMode: TargetModeInPlace, object: input.object,
		operation: RecoveryOperationOverwrite, expectedPrior: input.expectedPrior,
		expectedPriorBytes: input.expectedPriorBytes,
		expectedDigest:     input.expectedPostDigest, expectedBytes: input.expectedPostBytes,
		artifacts: artifacts,
	})
	request := TargetWriteAtomicRequest{
		Object: input.object, ExpectedBytes: input.expectedPostBytes,
		ExpectedDigest: input.expectedPostDigest, Content: bytes.NewReader(postPayload),
	}
	base := &recoveryLocalSFTPClient{}
	client := &recoveryScriptedSFTPClient{base: base}
	client.rename = func(string, string) error {
		return errors.New("scripted R43 capture boundary")
	}
	target := fixture.targetWithClient(client)
	target.entropy = bytes.NewReader(bytes.Repeat([]byte{0x5a}, recoveryPayloadTempEntropyBytes))
	target.now = func() time.Time { return fixture.now }
	parent := filepath.Dir(finalPath)
	return &recoverySFTPOverwritePrepareCaseForTest{
		fixture: fixture, base: base, client: client, target: target,
		permit: permit, request: request, artifacts: artifacts,
		priorPayload: append([]byte(nil), priorPayload...),
		postPayload:  append([]byte(nil), postPayload...), finalPath: finalPath,
		intentPath:    filepath.Join(parent, artifacts.intentComponent),
		priorPath:     filepath.Join(parent, artifacts.priorComponent),
		postPath:      filepath.Join(parent, artifacts.postComponent),
		publishedPath: filepath.Join(parent, artifacts.publishedComponent),
	}
}

func (testCase *recoverySFTPOverwritePrepareCaseForTest) write() error {
	_, err := testCase.target.WriteAtomic(
		context.Background(), testCase.permit, testCase.request,
	)
	return err
}

func assertRecoveryOverwritePreparationPreservesFinalForTest(
	t *testing.T,
	testCase *recoverySFTPOverwritePrepareCaseForTest,
) {
	t.Helper()
	prior, err := os.ReadFile(testCase.finalPath)
	if err != nil || !bytes.Equal(prior, testCase.priorPayload) {
		t.Fatalf("overwrite final=%q error=%v, want exact prior preserved", prior, err)
	}
	for _, value := range []string{testCase.priorPath, testCase.publishedPath} {
		if _, err := os.Lstat(value); !os.IsNotExist(err) {
			t.Fatalf("future overwrite artifact %q error=%v, want absent", value, err)
		}
	}
}

func assertRecoveryOverwritePreparationHasNoRenameOrRemoveForTest(
	t *testing.T,
	client *recoveryLocalSFTPClient,
) {
	t.Helper()
	if client.renameCalls != 0 || client.removeCalls != 0 {
		t.Fatalf("R43 crossed capture/cleanup boundary: rename=%v remove=%v",
			client.renamePaths, client.removePaths)
	}
}

func TestRecoverySFTPTargetDeleteErrorResourceAndPrivacyMatrix(t *testing.T) {
	rawFailure := errors.New(
		"RAW_DELETE_DEPENDENCY_FAILURE private-host private-user private-credential " +
			"private-root private-path private-token private-marker private-content " +
			"private-link private-digest-input private-sftp-status",
	)
	var capturedLogs bytes.Buffer
	previousLogger := logger.Log
	logger.Log = zerolog.New(&capturedLogs)
	t.Cleanup(func() { logger.Log = previousLogger })

	installIntent := func(t *testing.T, value *recoverySFTPDeleteCaptureCaseForTest) {
		t.Helper()
		if err := os.WriteFile(value.intentPath, []byte(value.permit.proof.artifacts.intentDocument), 0o600); err != nil {
			t.Fatalf("install delete intent: %v", err)
		}
	}
	installCaptured := func(t *testing.T, value *recoverySFTPDeleteCaptureCaseForTest) {
		t.Helper()
		installIntent(t, value)
		if err := value.base.Rename(value.finalPath, value.capturedPath); err != nil {
			t.Fatalf("install delete captured state: %v", err)
		}
	}
	installVerified := func(t *testing.T, value *recoverySFTPDeleteCaptureCaseForTest) {
		t.Helper()
		installCaptured(t, value)
		if err := os.WriteFile(value.verifiedPath, []byte(value.permit.proof.artifacts.verifiedDocument), 0o600); err != nil {
			t.Fatalf("install delete verified marker: %v", err)
		}
	}

	t.Run("dependency, operation and close errors own every resource once", func(t *testing.T) {
		stages := []struct {
			name  string
			state func(*testing.T, *recoverySFTPDeleteCaptureCaseForTest)
		}{
			{name: "resolver"},
			{name: "dial"},
			{name: "SFTP opener"},
			{name: "open with resource and error"},
			{name: "read"},
			{name: "stat"},
			{name: "file close"},
			{name: "OpenFile with resource and error"},
			{name: "write"},
			{name: "Sync"},
			{name: "rename", state: installIntent},
			{name: "remove", state: installVerified},
			{name: "SFTP close"},
			{name: "SSH close"},
		}
		for _, test := range stages {
			t.Run(test.name, func(t *testing.T) {
				value := newRecoverySFTPDeleteCaptureCaseForTest(t, []byte("delete error matrix prior"))
				if test.state != nil {
					test.state(t, value)
				}
				value.client.rename = nil
				openedFiles := make([]*recoveryCloseCountingSFTPFile, 0, 16)
				wrap := func(file recoveryTargetSFTPFile) *recoveryCloseCountingSFTPFile {
					counted := &recoveryCloseCountingSFTPFile{recoveryTargetSFTPFile: file}
					openedFiles = append(openedFiles, counted)
					return counted
				}
				faultInjected := false
				value.client.open = func(path string) (recoveryTargetSFTPFile, error) {
					file, err := value.base.Open(path)
					if err != nil {
						return file, err
					}
					if !faultInjected && (test.name == "open with resource and error" ||
						test.name == "read" || test.name == "stat" || test.name == "file close") {
						faultInjected = true
						scripted := &recoveryScriptedSFTPFile{base: file}
						switch test.name {
						case "open with resource and error":
							return wrap(scripted), rawFailure
						case "read":
							scripted.read = func([]byte) (int, error) { return 0, rawFailure }
						case "stat":
							scripted.stat = func() (os.FileInfo, error) { return nil, rawFailure }
						case "file close":
							scripted.close = func() error {
								if err := file.Close(); err != nil {
									return err
								}
								return rawFailure
							}
						}
						return wrap(scripted), nil
					}
					return wrap(file), nil
				}
				value.client.openFile = func(path string, flags int) (recoveryTargetSFTPFile, error) {
					file, err := value.base.OpenFile(path, flags)
					if err != nil {
						return file, err
					}
					if !faultInjected && (test.name == "OpenFile with resource and error" ||
						test.name == "write" || test.name == "Sync") {
						faultInjected = true
						scripted := &recoveryScriptedSFTPFile{base: file}
						switch test.name {
						case "OpenFile with resource and error":
							return wrap(scripted), rawFailure
						case "write":
							scripted.write = func([]byte) (int, error) { return 0, rawFailure }
						case "Sync":
							scripted.sync = func() error { return rawFailure }
						}
						return wrap(scripted), nil
					}
					return wrap(file), nil
				}
				if test.name == "rename" {
					value.client.rename = func(string, string) error { return rawFailure }
				}
				if test.name == "remove" {
					value.client.remove = func(string) error { return rawFailure }
				}
				if test.name == "SFTP close" {
					value.client.close = func() error {
						value.base.closeCalls++
						return rawFailure
					}
				}
				sshCloseCalls := 0
				value.target.sessions = newRecoveryTargetSessionFactoryForTest(
					value.fixture.resolver, value.fixture.dialer,
					func(*ssh.Client) (recoveryTargetSFTPClient, error) {
						if test.name == "SFTP opener" {
							return value.client, rawFailure
						}
						return value.client, nil
					},
					func(*ssh.Client) error {
						sshCloseCalls++
						if test.name == "SSH close" {
							return rawFailure
						}
						return nil
					},
				)
				if test.name == "resolver" {
					value.fixture.resolver.err = rawFailure
				}
				if test.name == "dial" {
					value.fixture.dialer.dial = func(
						context.Context, model.Node, string, sshutil.DialAuditContext,
					) (*ssh.Client, error) {
						return new(ssh.Client), rawFailure
					}
				}

				result, err := value.delete()
				if result != (TargetWriteResult{}) || err != ErrRecoveryTargetUnavailable {
					t.Fatalf("stage=%s result=%+v error=%v, want zero/exact unavailable", test.name, result, err)
				}
				for index, file := range openedFiles {
					if file.closeCalls != 1 {
						t.Fatalf("stage=%s file=%d close calls=%d, want exactly one", test.name, index, file.closeCalls)
					}
				}
				wantSFTP, wantSSH := 1, 1
				if test.name == "resolver" {
					wantSFTP, wantSSH = 0, 0
				}
				if test.name == "dial" {
					wantSFTP = 0
				}
				if test.name == "SFTP opener" {
					wantSFTP = 1
				}
				if value.base.closeCalls != wantSFTP || sshCloseCalls != wantSSH {
					t.Fatalf("stage=%s SFTP/SSH close=%d/%d, want %d/%d", test.name, value.base.closeCalls, sshCloseCalls, wantSFTP, wantSSH)
				}
				encoded, marshalErr := json.Marshal([]any{value.permit, value.request, result})
				if marshalErr != nil {
					t.Fatalf("stage=%s marshal products: %v", test.name, marshalErr)
				}
				corpus := strings.Join([]string{
					err.Error(), string(encoded), fmt.Sprintf("%v\n%+v\n%#v", result, result, result),
					fmt.Sprintf("%+v", value.fixture.dialer.audit), capturedLogs.String(),
				}, "\n")
				for _, forbidden := range []string{
					rawFailure.Error(), value.fixture.binding.RootLocator,
					value.fixture.binding.CredentialRevision, value.finalPath,
					value.request.Object.PrivateRelativeLocator, value.permit.proof.artifacts.token,
					value.permit.proof.artifacts.intentComponent, value.permit.proof.artifacts.capturedComponent,
					value.permit.proof.artifacts.verifiedComponent, value.permit.proof.artifacts.intentDocument,
					value.permit.proof.artifacts.verifiedDocument, string(value.priorPayload),
				} {
					if forbidden != "" && strings.Contains(corpus, forbidden) {
						t.Fatalf("stage=%s leaked %q: %s", test.name, forbidden, corpus)
					}
				}
			})
		}
	})

	t.Run("live permit revocation blocks every next mutation", func(t *testing.T) {
		cases := []struct {
			name         string
			install      func(*testing.T, *recoverySFTPDeleteCaptureCaseForTest)
			wantRename   int
			wantRemove   int
			wantOpenFile int
		}{
			{name: "intent create", wantOpenFile: 0},
			{name: "capture rename", install: installIntent, wantRename: 0},
			{name: "verified create", install: installCaptured, wantOpenFile: 0},
			{name: "captured leaf remove", install: installVerified, wantRemove: 0},
			{name: "intent remove", install: func(t *testing.T, value *recoverySFTPDeleteCaptureCaseForTest) {
				installVerified(t, value)
				if err := value.base.Remove(value.capturedPath); err != nil {
					t.Fatalf("install deleted captured state: %v", err)
				}
			}, wantRemove: 0},
			{name: "verified remove", install: func(t *testing.T, value *recoverySFTPDeleteCaptureCaseForTest) {
				installVerified(t, value)
				for _, path := range []string{value.capturedPath, value.intentPath} {
					if err := value.base.Remove(path); err != nil {
						t.Fatalf("install verified-only state remove %q: %v", path, err)
					}
				}
			}, wantRemove: 0},
		}
		for _, test := range cases {
			t.Run(test.name, func(t *testing.T) {
				value := newRecoverySFTPDeleteCaptureCaseForTest(t, []byte("delete revocation prior"))
				if test.install != nil {
					test.install(t, value)
				}
				beforeRename := value.base.renameCalls
				beforeRemove := value.base.removeCalls
				beforeOpenFile := value.base.openFileCalls
				validationCalls := 0
				value.permit.permit.proof.validateAt = func(time.Time) error {
					validationCalls++
					if validationCalls >= 2 {
						return ErrInvalidTargetPermit
					}
					return nil
				}
				result, err := value.delete()
				if result != (TargetWriteResult{}) || err != ErrInvalidTargetPermit {
					t.Fatalf("revoked %s result=%+v error=%v calls=%d, want zero/exact invalid", test.name, result, err, validationCalls)
				}
				if value.base.renameCalls-beforeRename != test.wantRename ||
					value.base.removeCalls-beforeRemove != test.wantRemove ||
					value.base.openFileCalls-beforeOpenFile != test.wantOpenFile {
					t.Fatalf("revoked %s crossed mutation rename=%d remove=%d open_file=%d", test.name,
						value.base.renameCalls-beforeRename, value.base.removeCalls-beforeRemove,
						value.base.openFileCalls-beforeOpenFile)
				}
			})
		}

		t.Run("mismatch restore has no re-entry authority after revocation", func(t *testing.T) {
			value := newRecoverySFTPDeleteCaptureCaseForTest(t, []byte("delete mismatch prior"))
			installIntent(t, value)
			validationCalls := 0
			value.permit.permit.proof.validateAt = func(time.Time) error {
				validationCalls++
				if validationCalls >= 3 {
					return ErrInvalidTargetPermit
				}
				return nil
			}
			mismatch := []byte("delete mismatch captured")
			value.client.rename = func(oldName, newName string) error {
				if oldName == value.finalPath && newName == value.capturedPath {
					if err := value.base.Rename(oldName, newName); err != nil {
						return err
					}
					return os.WriteFile(value.capturedPath, mismatch, 0o640)
				}
				return value.base.Rename(oldName, newName)
			}
			result, err := value.delete()
			if result != (TargetWriteResult{}) || err != ErrInvalidTargetPermit {
				t.Fatalf("mismatch restore result=%+v error=%v calls=%d, want zero/exact invalid", result, err, validationCalls)
			}
			if value.base.renameCalls != 1 || value.base.removeCalls != 0 {
				t.Fatalf("mismatch restore mutations rename=%d remove=%d, want capture only", value.base.renameCalls, value.base.removeCalls)
			}
			if got, readErr := os.ReadFile(value.capturedPath); readErr != nil || !bytes.Equal(got, mismatch) {
				t.Fatalf("revoked mismatch captured=%q error=%v, want preserved", got, readErr)
			}
		})
	})

	if capturedLogs.Len() != 0 {
		t.Fatalf("delete target emitted direct logs: %s", capturedLogs.String())
	}
	source, err := os.ReadFile("target.go")
	if err != nil {
		t.Fatalf("read target source for direct-log gate: %v", err)
	}
	for _, forbidden := range []string{"logger.", "log."} {
		if bytes.Contains(source, []byte(forbidden)) {
			t.Fatalf("target source contains direct logging marker %q", forbidden)
		}
	}
}

func TestRecoverySFTPTargetOverwritePreparesAuthenticatedPost(t *testing.T) {
	for _, test := range []struct {
		name    string
		payload []byte
	}{
		{name: "ordinary payload", payload: bytes.Repeat([]byte{0x3c}, 64*1024+17)},
		{name: "zero-byte payload", payload: []byte{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			testCase := newRecoverySFTPOverwritePrepareCaseForTest(t, test.payload)
			writeCloses := make(map[string]int)
			testCase.client.openFile = func(value string, flag int) (recoveryTargetSFTPFile, error) {
				file, err := testCase.base.OpenFile(value, flag)
				if err != nil {
					return nil, err
				}
				return &recoveryScriptedSFTPFile{base: file, close: func() error {
					writeCloses[value]++
					return file.Close()
				}}, nil
			}
			tracker := &recoveryReadTrackingReader{reader: bytes.NewReader(test.payload)}
			testCase.request.Content = tracker
			if err := testCase.write(); err != ErrRecoveryTargetUnavailable {
				t.Fatalf("overwrite preparation error=%v, want exact ErrRecoveryTargetUnavailable before capture", err)
			}
			if testCase.target.entropy.(*bytes.Reader).Len() != recoveryPayloadTempEntropyBytes {
				t.Fatalf("overwrite preparation consumed create entropy")
			}
			assertRecoveryOverwritePreparationPreservesFinalForTest(t, testCase)
			intent, intentErr := os.ReadFile(testCase.intentPath)
			post, postErr := os.ReadFile(testCase.postPath)
			if intentErr != nil || string(intent) != testCase.artifacts.intentDocument {
				t.Fatalf("intent=%q error=%v, want exact authenticated document", intent, intentErr)
			}
			if postErr != nil || !bytes.Equal(post, test.payload) {
				t.Fatalf("post bytes=%d error=%v, want exact %d bytes", len(post), postErr, len(test.payload))
			}
			for _, value := range []string{testCase.intentPath, testCase.postPath} {
				info, err := os.Lstat(value)
				if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
					t.Fatalf("prepared artifact %q info=%v error=%v, want regular 0600", value, info, err)
				}
				if writeCloses[value] != 1 {
					t.Fatalf("prepared artifact %q write closes=%d, want one before verification", value, writeCloses[value])
				}
				if !slices.Contains(testCase.base.openPaths, value) {
					t.Fatalf("prepared artifact %q was not reopened: opens=%v", value, testCase.base.openPaths)
				}
			}
			wantFlags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
			if !reflect.DeepEqual(testCase.base.openFilePaths, []string{testCase.intentPath, testCase.postPath}) ||
				!reflect.DeepEqual(testCase.base.openFileFlags, []int{wantFlags, wantFlags}) ||
				testCase.base.syncCalls != 2 {
				t.Fatalf("exclusive preparation opens=%v flags=%v sync=%d, want intent/post O_EXCL and two Sync",
					testCase.base.openFilePaths, testCase.base.openFileFlags, testCase.base.syncCalls)
			}
			if len(tracker.requests) == 0 || tracker.requests[len(tracker.requests)-1] != 1 ||
				tracker.maxReadRequest > 32*1024 {
				t.Fatalf("post source reads=%v max=%d, want bounded exact EOF proof", tracker.requests, tracker.maxReadRequest)
			}
			assertRecoveryOverwritePreparationHasNoRenameOrRemoveForTest(t, testCase.base)

			replayBase := &recoveryLocalSFTPClient{}
			replayClient := &recoveryScriptedSFTPClient{base: replayBase}
			replayClient.rename = func(string, string) error {
				return errors.New("scripted R43 replay capture boundary")
			}
			replayTarget := testCase.fixture.targetWithClient(replayClient)
			replayTarget.entropy = bytes.NewReader(bytes.Repeat([]byte{0x5a}, recoveryPayloadTempEntropyBytes))
			replayTarget.now = func() time.Time { return testCase.fixture.now }
			sourceReads := 0
			replayRequest := testCase.request
			replayRequest.Content = &recoveryReadTrackingReader{read: func([]byte) (int, error) {
				sourceReads++
				return 0, errors.New("replay source must remain unread")
			}}
			if _, err := replayTarget.WriteAtomic(context.Background(), testCase.permit, replayRequest); err != ErrRecoveryTargetUnavailable {
				t.Fatalf("exact prepared replay error=%v, want exact ErrRecoveryTargetUnavailable before capture", err)
			}
			if sourceReads != 0 || replayBase.openFileCalls != 0 || replayBase.chmodCalls != 0 ||
				replayBase.syncCalls != 0 || replayBase.renameCalls != 0 || replayBase.removeCalls != 0 {
				t.Fatalf("exact replay source=%d open-file=%d chmod=%d sync=%d rename=%d remove=%d, want observation only",
					sourceReads, replayBase.openFileCalls, replayBase.chmodCalls, replayBase.syncCalls,
					replayBase.renameCalls, replayBase.removeCalls)
			}
			assertRecoveryOverwritePreparationPreservesFinalForTest(t, testCase)
		})
	}

	t.Run("intent-only retry creates only exact post", func(t *testing.T) {
		payload := []byte("post-after-exact-intent")
		testCase := newRecoverySFTPOverwritePrepareCaseForTest(t, payload)
		if err := os.WriteFile(
			testCase.intentPath, []byte(testCase.artifacts.intentDocument), 0o600,
		); err != nil {
			t.Fatalf("install exact retry intent: %v", err)
		}
		if err := os.Chmod(testCase.intentPath, 0o600); err != nil {
			t.Fatalf("chmod exact retry intent: %v", err)
		}
		tracker := &recoveryReadTrackingReader{reader: bytes.NewReader(payload)}
		testCase.request.Content = tracker
		if err := testCase.write(); err != ErrRecoveryTargetUnavailable {
			t.Fatalf("intent-only retry error=%v, want exact ErrRecoveryTargetUnavailable before capture", err)
		}
		post, err := os.ReadFile(testCase.postPath)
		if err != nil || !bytes.Equal(post, payload) {
			t.Fatalf("intent-only retry post=%q error=%v, want exact payload", post, err)
		}
		if !reflect.DeepEqual(testCase.base.openFilePaths, []string{testCase.postPath}) ||
			testCase.base.syncCalls != 1 || testCase.base.chmodCalls != 1 ||
			len(tracker.requests) == 0 || tracker.requests[len(tracker.requests)-1] != 1 {
			t.Fatalf("intent-only retry open=%v sync=%d chmod=%d reads=%v, want only exact post create",
				testCase.base.openFilePaths, testCase.base.syncCalls,
				testCase.base.chmodCalls, tracker.requests)
		}
		assertRecoveryOverwritePreparationHasNoRenameOrRemoveForTest(t, testCase.base)
		assertRecoveryOverwritePreparationPreservesFinalForTest(t, testCase)
	})

	t.Run("exact post without intent is never adopted", func(t *testing.T) {
		payload := []byte("orphan-exact-post")
		testCase := newRecoverySFTPOverwritePrepareCaseForTest(t, payload)
		if err := os.WriteFile(testCase.postPath, payload, 0o600); err != nil {
			t.Fatalf("install orphan exact post: %v", err)
		}
		if err := os.Chmod(testCase.postPath, 0o600); err != nil {
			t.Fatalf("chmod orphan exact post: %v", err)
		}
		if err := testCase.write(); err != ErrRecoveryTargetChanged {
			t.Fatalf("orphan exact post error=%v, want exact ErrRecoveryTargetChanged", err)
		}
		post, err := os.ReadFile(testCase.postPath)
		if err != nil || !bytes.Equal(post, payload) {
			t.Fatalf("orphan exact post=%q error=%v, want preserved", post, err)
		}
		if testCase.base.openFileCalls != 0 || testCase.base.chmodCalls != 0 {
			t.Fatalf("orphan exact post triggered mutation: open=%d chmod=%d",
				testCase.base.openFileCalls, testCase.base.chmodCalls)
		}
		assertRecoveryOverwritePreparationHasNoRenameOrRemoveForTest(t, testCase.base)
		assertRecoveryOverwritePreparationPreservesFinalForTest(t, testCase)
	})

	t.Run("intent collision matrix", func(t *testing.T) {
		mutations := []struct {
			name   string
			mutate func(*testing.T, *recoverySFTPOverwritePrepareCaseForTest) []byte
		}{
			{name: "malformed", mutate: func(_ *testing.T, _ *recoverySFTPOverwritePrepareCaseForTest) []byte { return []byte("{") }},
			{name: "wrong phase", mutate: func(_ *testing.T, value *recoverySFTPOverwritePrepareCaseForTest) []byte {
				return []byte(value.artifacts.publishedDocument)
			}},
			{name: "wrong key", mutate: func(t *testing.T, value *recoverySFTPOverwritePrepareCaseForTest) []byte {
				var document recoveryOverwriteMarkerDocument
				if err := json.Unmarshal([]byte(value.artifacts.intentDocument), &document); err != nil {
					t.Fatal(err)
				}
				document.AuthenticationTag = base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x7f}, sha256.Size))
				encoded, err := json.Marshal(document)
				if err != nil {
					t.Fatal(err)
				}
				return encoded
			}},
			{name: "tampered binding", mutate: func(t *testing.T, value *recoverySFTPOverwritePrepareCaseForTest) []byte {
				var document recoveryOverwriteMarkerDocument
				if err := json.Unmarshal([]byte(value.artifacts.intentDocument), &document); err != nil {
					t.Fatal(err)
				}
				document.BindingDigest = strings.Repeat("0", sha256DigestLength)
				encoded, err := json.Marshal(document)
				if err != nil {
					t.Fatal(err)
				}
				return encoded
			}},
		}
		for _, mutation := range mutations {
			t.Run(mutation.name, func(t *testing.T) {
				testCase := newRecoverySFTPOverwritePrepareCaseForTest(t, []byte("post-payload"))
				collision := mutation.mutate(t, testCase)
				if err := os.WriteFile(testCase.intentPath, collision, 0o600); err != nil {
					t.Fatalf("install intent collision: %v", err)
				}
				if err := os.Chmod(testCase.intentPath, 0o600); err != nil {
					t.Fatalf("chmod intent collision: %v", err)
				}
				if err := testCase.write(); err != ErrRecoveryTargetChanged {
					t.Fatalf("intent collision error=%v, want exact ErrRecoveryTargetChanged", err)
				}
				preserved, err := os.ReadFile(testCase.intentPath)
				if err != nil || !bytes.Equal(preserved, collision) {
					t.Fatalf("intent collision preserved=%q error=%v", preserved, err)
				}
				if testCase.base.openFileCalls != 0 || testCase.base.chmodCalls != 0 {
					t.Fatalf("intent collision mutated artifact: open=%d chmod=%d",
						testCase.base.openFileCalls, testCase.base.chmodCalls)
				}
				assertRecoveryOverwritePreparationHasNoRenameOrRemoveForTest(t, testCase.base)
				assertRecoveryOverwritePreparationPreservesFinalForTest(t, testCase)
			})
		}
	})

	t.Run("post collision matrix", func(t *testing.T) {
		collisions := []struct {
			name    string
			install func(*testing.T, string)
			assert  func(*testing.T, string)
		}{
			{
				name: "regular", install: func(t *testing.T, value string) {
					if err := os.WriteFile(value, []byte("foreign-regular-post"), 0o600); err != nil {
						t.Fatal(err)
					}
				}, assert: func(t *testing.T, value string) {
					got, err := os.ReadFile(value)
					if err != nil || string(got) != "foreign-regular-post" {
						t.Fatalf("regular post=%q error=%v, want preserved", got, err)
					}
				},
			},
			{
				name: "directory", install: func(t *testing.T, value string) {
					if err := os.Mkdir(value, 0o700); err != nil {
						t.Fatal(err)
					}
				}, assert: func(t *testing.T, value string) {
					info, err := os.Lstat(value)
					if err != nil || !info.IsDir() {
						t.Fatalf("directory post info=%v error=%v", info, err)
					}
				},
			},
			{
				name: "symlink", install: func(t *testing.T, value string) {
					destination := filepath.Join(t.TempDir(), "post")
					if err := os.WriteFile(destination, []byte("foreign-symlink-target"), 0o600); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(destination, value); err != nil {
						t.Fatal(err)
					}
				}, assert: func(t *testing.T, value string) {
					info, err := os.Lstat(value)
					if err != nil || info.Mode()&os.ModeSymlink == 0 {
						t.Fatalf("symlink post info=%v error=%v", info, err)
					}
				},
			},
			{
				name: "special", install: func(t *testing.T, value string) {
					if err := syscall.Mkfifo(value, 0o600); err != nil {
						t.Fatal(err)
					}
				}, assert: func(t *testing.T, value string) {
					info, err := os.Lstat(value)
					if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
						t.Fatalf("special post info=%v error=%v", info, err)
					}
				},
			},
		}
		for _, collision := range collisions {
			t.Run(collision.name, func(t *testing.T) {
				testCase := newRecoverySFTPOverwritePrepareCaseForTest(t, []byte("post-payload"))
				collision.install(t, testCase.postPath)
				if err := testCase.write(); err != ErrRecoveryTargetChanged {
					t.Fatalf("post collision error=%v, want exact ErrRecoveryTargetChanged", err)
				}
				collision.assert(t, testCase.postPath)
				if testCase.base.openFileCalls != 0 || testCase.base.chmodCalls != 0 {
					t.Fatalf("post collision mutated artifact: open=%d chmod=%d",
						testCase.base.openFileCalls, testCase.base.chmodCalls)
				}
				assertRecoveryOverwritePreparationHasNoRenameOrRemoveForTest(t, testCase.base)
				assertRecoveryOverwritePreparationPreservesFinalForTest(t, testCase)
			})
		}
	})

	t.Run("parent alias symlink and type drift", func(t *testing.T) {
		for _, drift := range []struct {
			name      string
			configure func(*recoverySFTPOverwritePrepareCaseForTest, string)
		}{
			{name: "alias", configure: func(testCase *recoverySFTPOverwritePrepareCaseForTest, parent string) {
				testCase.client.realPath = func(value string, _ int) (string, error) {
					canonical, err := testCase.base.RealPath(value)
					if err == nil && value == parent {
						return canonical + "-alias", nil
					}
					return canonical, err
				}
			}},
			{name: "symlink", configure: func(testCase *recoverySFTPOverwritePrepareCaseForTest, parent string) {
				testCase.client.lstat = func(value string, _ int) (os.FileInfo, error) {
					info, err := testCase.base.Lstat(value)
					if err == nil && value == parent {
						mode := info.Mode() | os.ModeSymlink
						return recoveryFileInfoOverride{FileInfo: info, mode: &mode}, nil
					}
					return info, err
				}
			}},
			{name: "type", configure: func(testCase *recoverySFTPOverwritePrepareCaseForTest, parent string) {
				testCase.client.lstat = func(value string, _ int) (os.FileInfo, error) {
					info, err := testCase.base.Lstat(value)
					if err == nil && value == parent {
						return recoveryProbeFileInfo{
							name: info.Name(), size: info.Size(), mode: 0o600,
							modTime: info.ModTime(),
						}, nil
					}
					return info, err
				}
			}},
		} {
			t.Run(drift.name, func(t *testing.T) {
				testCase := newRecoverySFTPOverwritePrepareCaseForTest(t, []byte("post-payload"))
				drift.configure(testCase, filepath.Dir(testCase.finalPath))
				if err := testCase.write(); err != ErrRecoveryTargetChanged {
					t.Fatalf("parent drift error=%v, want exact ErrRecoveryTargetChanged", err)
				}
				if testCase.base.openFileCalls != 0 || testCase.base.chmodCalls != 0 {
					t.Fatalf("parent drift mutated artifact: open=%d chmod=%d",
						testCase.base.openFileCalls, testCase.base.chmodCalls)
				}
				assertRecoveryOverwritePreparationHasNoRenameOrRemoveForTest(t, testCase.base)
				assertRecoveryOverwritePreparationPreservesFinalForTest(t, testCase)
			})
		}
	})

	t.Run("bounded source and dependency failures", func(t *testing.T) {
		rawFailure := errors.New("RAW_OVERWRITE_PREPARATION_FAILURE_FOR_TEST_ONLY")
		for _, failure := range []struct {
			name      string
			content   func([]byte) io.Reader
			configure func(*recoverySFTPOverwritePrepareCaseForTest)
			wantErr   error
		}{
			{name: "short source", content: func(value []byte) io.Reader { return bytes.NewReader(value[:len(value)-1]) }},
			{name: "extra source", content: func(value []byte) io.Reader {
				return bytes.NewReader(append(append([]byte(nil), value...), 0x7f))
			}},
			{name: "digest mismatch", content: func(value []byte) io.Reader {
				changed := append([]byte(nil), value...)
				changed[0] ^= 0xff
				return bytes.NewReader(changed)
			}},
			{name: "post sync", configure: func(testCase *recoverySFTPOverwritePrepareCaseForTest) {
				testCase.client.openFile = func(value string, flag int) (recoveryTargetSFTPFile, error) {
					file, err := testCase.base.OpenFile(value, flag)
					if err != nil || value != testCase.postPath {
						return file, err
					}
					return &recoveryScriptedSFTPFile{base: file, sync: func() error { return rawFailure }}, nil
				}
			}},
			{name: "post close", configure: func(testCase *recoverySFTPOverwritePrepareCaseForTest) {
				testCase.client.openFile = func(value string, flag int) (recoveryTargetSFTPFile, error) {
					file, err := testCase.base.OpenFile(value, flag)
					if err != nil || value != testCase.postPath {
						return file, err
					}
					return &recoveryScriptedSFTPFile{base: file, close: func() error {
						_ = file.Close()
						return rawFailure
					}}, nil
				}
			}},
			{name: "post reopen", configure: func(testCase *recoverySFTPOverwritePrepareCaseForTest) {
				testCase.client.open = func(value string) (recoveryTargetSFTPFile, error) {
					if value == testCase.postPath {
						return nil, rawFailure
					}
					return testCase.base.Open(value)
				}
			}},
			{name: "post reread short", wantErr: ErrRecoveryTargetChanged, configure: func(testCase *recoverySFTPOverwritePrepareCaseForTest) {
				testCase.client.open = func(value string) (recoveryTargetSFTPFile, error) {
					file, err := testCase.base.Open(value)
					if err != nil || value != testCase.postPath {
						return file, err
					}
					return &recoveryScriptedSFTPFile{base: file, read: func([]byte) (int, error) {
						return 0, io.EOF
					}}, nil
				}
			}},
			{name: "post reread extra", wantErr: ErrRecoveryTargetChanged, configure: func(testCase *recoverySFTPOverwritePrepareCaseForTest) {
				testCase.client.open = func(value string) (recoveryTargetSFTPFile, error) {
					file, err := testCase.base.Open(value)
					if err != nil || value != testCase.postPath {
						return file, err
					}
					remaining := len(testCase.postPayload)
					return &recoveryScriptedSFTPFile{base: file, read: func(buffer []byte) (int, error) {
						if remaining > 0 {
							read, readErr := file.Read(buffer)
							remaining -= read
							return read, readErr
						}
						buffer[0] = 0x7f
						return 1, nil
					}}, nil
				}
			}},
			{name: "post reread digest mismatch", wantErr: ErrRecoveryTargetChanged, configure: func(testCase *recoverySFTPOverwritePrepareCaseForTest) {
				testCase.client.open = func(value string) (recoveryTargetSFTPFile, error) {
					file, err := testCase.base.Open(value)
					if err != nil || value != testCase.postPath {
						return file, err
					}
					changed := append([]byte(nil), testCase.postPayload...)
					changed[0] ^= 0xff
					reader := bytes.NewReader(changed)
					return &recoveryScriptedSFTPFile{base: file, read: reader.Read}, nil
				}
			}},
			{name: "intent permission", configure: func(testCase *recoverySFTPOverwritePrepareCaseForTest) {
				testCase.client.openFile = func(value string, flag int) (recoveryTargetSFTPFile, error) {
					if value == testCase.intentPath {
						return nil, os.ErrPermission
					}
					return testCase.base.OpenFile(value, flag)
				}
			}},
			{name: "intent unsupported", configure: func(testCase *recoverySFTPOverwritePrepareCaseForTest) {
				testCase.client.openFile = func(value string, flag int) (recoveryTargetSFTPFile, error) {
					if value == testCase.intentPath {
						return nil, rawFailure
					}
					return testCase.base.OpenFile(value, flag)
				}
			}},
		} {
			t.Run(failure.name, func(t *testing.T) {
				payload := []byte("expected-post-payload")
				testCase := newRecoverySFTPOverwritePrepareCaseForTest(t, payload)
				if failure.content != nil {
					testCase.request.Content = failure.content(payload)
				}
				if failure.configure != nil {
					failure.configure(testCase)
				}
				wantErr := failure.wantErr
				if wantErr == nil {
					wantErr = ErrRecoveryTargetUnavailable
				}
				if err := testCase.write(); err != wantErr {
					t.Fatalf("preparation failure error=%v, want exact sanitized %v", err, wantErr)
				}
				assertRecoveryOverwritePreparationHasNoRenameOrRemoveForTest(t, testCase.base)
				assertRecoveryOverwritePreparationPreservesFinalForTest(t, testCase)
			})
		}
	})
}

func installRecoveryOverwritePreparedTupleForTest(
	t *testing.T,
	testCase *recoverySFTPOverwritePrepareCaseForTest,
) {
	t.Helper()
	for value, payload := range map[string][]byte{
		testCase.intentPath: []byte(testCase.artifacts.intentDocument),
		testCase.postPath:   testCase.postPayload,
	} {
		if err := os.WriteFile(value, payload, 0o600); err != nil {
			t.Fatalf("install prepared overwrite artifact %q: %v", value, err)
		}
		if err := os.Chmod(value, 0o600); err != nil {
			t.Fatalf("chmod prepared overwrite artifact %q: %v", value, err)
		}
	}
}

func recoveryOwnedFileInfoForOverwriteTest(info os.FileInfo) os.FileInfo {
	if info == nil {
		return nil
	}
	return recoveryProbeFileInfo{
		name: info.Name(), size: info.Size(), mode: info.Mode(), modTime: info.ModTime(),
		uid: 501, gid: 502,
	}
}

func useOwnedRecoveryOverwriteEntriesForTest(
	testCase *recoverySFTPOverwritePrepareCaseForTest,
) {
	testCase.client.lstat = func(value string, _ int) (os.FileInfo, error) {
		info, err := testCase.base.Lstat(value)
		if err == nil && (value == testCase.finalPath || value == testCase.priorPath) {
			return recoveryOwnedFileInfoForOverwriteTest(info), nil
		}
		return info, err
	}
}

func assertRecoveryOverwritePreparedEvidenceForTest(
	t *testing.T,
	testCase *recoverySFTPOverwritePrepareCaseForTest,
) {
	t.Helper()
	intent, intentErr := os.ReadFile(testCase.intentPath)
	post, postErr := os.ReadFile(testCase.postPath)
	if intentErr != nil || string(intent) != testCase.artifacts.intentDocument {
		t.Fatalf("overwrite intent=%q error=%v, want exact prepared evidence", intent, intentErr)
	}
	if postErr != nil || !bytes.Equal(post, testCase.postPayload) {
		t.Fatalf("overwrite post=%q error=%v, want exact prepared evidence", post, postErr)
	}
	if _, err := os.Lstat(testCase.publishedPath); !os.IsNotExist(err) {
		t.Fatalf("overwrite published marker error=%v, want absent before publication", err)
	}
	if testCase.base.removeCalls != 0 {
		t.Fatalf("R44 removed overwrite evidence: %v", testCase.base.removePaths)
	}
}

func (testCase *recoverySFTPOverwritePrepareCaseForTest) writeResult() (TargetWriteResult, error) {
	return testCase.target.WriteAtomic(
		context.Background(), testCase.permit, testCase.request,
	)
}

func installRecoveryOverwriteCapturedTupleForTest(
	t *testing.T,
	testCase *recoverySFTPOverwritePrepareCaseForTest,
) {
	t.Helper()
	installRecoveryOverwritePreparedTupleForTest(t, testCase)
	if err := os.Rename(testCase.finalPath, testCase.priorPath); err != nil {
		t.Fatalf("install captured overwrite tuple: %v", err)
	}
}

func installRecoveryOverwritePublishedUnacknowledgedTupleForTest(
	t *testing.T,
	testCase *recoverySFTPOverwritePrepareCaseForTest,
) {
	t.Helper()
	installRecoveryOverwriteCapturedTupleForTest(t, testCase)
	if err := os.Rename(testCase.postPath, testCase.finalPath); err != nil {
		t.Fatalf("install published-unacknowledged overwrite tuple: %v", err)
	}
}

func installRecoveryOverwriteAcknowledgedTupleForTest(
	t *testing.T,
	testCase *recoverySFTPOverwritePrepareCaseForTest,
) {
	t.Helper()
	installRecoveryOverwritePublishedUnacknowledgedTupleForTest(t, testCase)
	if err := os.WriteFile(
		testCase.publishedPath, []byte(testCase.artifacts.publishedDocument), 0o600,
	); err != nil {
		t.Fatalf("install acknowledged overwrite marker: %v", err)
	}
	if err := os.Chmod(testCase.publishedPath, 0o600); err != nil {
		t.Fatalf("chmod acknowledged overwrite marker: %v", err)
	}
}

func recoveryOverwriteWriteResultForTest(
	t *testing.T,
	testCase *recoverySFTPOverwritePrepareCaseForTest,
) TargetWriteResult {
	t.Helper()
	return TargetWriteResult{
		BytesWritten:   testCase.request.ExpectedBytes,
		IdentityDigest: testCase.request.ExpectedDigest,
		TargetRevision: recoverySFTPRegularFileObservationRevisionForTest(
			t, testCase.fixture.binding, testCase.request.Object,
			testCase.request.ExpectedDigest, testCase.request.ExpectedBytes,
		),
	}
}

func assertRecoveryOverwriteAcknowledgedRemoteStateForTest(
	t *testing.T,
	testCase *recoverySFTPOverwritePrepareCaseForTest,
) {
	t.Helper()
	final, finalErr := os.ReadFile(testCase.finalPath)
	published, publishedErr := os.ReadFile(testCase.publishedPath)
	if finalErr != nil || !bytes.Equal(final, testCase.postPayload) {
		t.Fatalf("acknowledged overwrite final=%q error=%v, want exact post", final, finalErr)
	}
	if publishedErr != nil || string(published) != testCase.artifacts.publishedDocument {
		t.Fatalf("acknowledged overwrite marker=%q error=%v, want exact document", published, publishedErr)
	}
	info, err := os.Lstat(testCase.publishedPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("acknowledged marker info=%v error=%v, want regular 0600", info, err)
	}
	for _, value := range []string{testCase.intentPath, testCase.priorPath, testCase.postPath} {
		if _, err := os.Lstat(value); !os.IsNotExist(err) {
			t.Fatalf("acknowledged residue %q error=%v, want absent", value, err)
		}
	}
}

func retryRecoveryOverwriteWithFreshTargetForTest(
	testCase *recoverySFTPOverwritePrepareCaseForTest,
) (TargetWriteResult, *recoveryLocalSFTPClient, error) {
	base := &recoveryLocalSFTPClient{}
	client := &recoveryScriptedSFTPClient{base: base}
	client.lstat = func(value string, _ int) (os.FileInfo, error) {
		info, err := base.Lstat(value)
		if err == nil && (value == testCase.finalPath || value == testCase.priorPath) {
			return recoveryOwnedFileInfoForOverwriteTest(info), nil
		}
		return info, err
	}
	target := testCase.fixture.targetWithClient(client)
	target.entropy = bytes.NewReader(bytes.Repeat([]byte{0x5a}, recoveryPayloadTempEntropyBytes))
	target.now = func() time.Time { return testCase.fixture.now }
	request := testCase.request
	request.Content = bytes.NewReader(testCase.postPayload)
	result, err := target.WriteAtomic(context.Background(), testCase.permit, request)
	return result, base, err
}

func TestRecoverySFTPTargetOverwritePublishesVerifiedPost(t *testing.T) {
	testCase := newRecoverySFTPOverwritePrepareCaseForTest(
		t, bytes.Repeat([]byte("verified-overwrite-post:"), 4097),
	)
	installRecoveryOverwriteCapturedTupleForTest(t, testCase)
	useOwnedRecoveryOverwriteEntriesForTest(testCase)
	testCase.client.rename = nil
	sourceReads := 0
	testCase.request.Content = &recoveryReadTrackingReader{read: func([]byte) (int, error) {
		sourceReads++
		return 0, errors.New("captured overwrite publish must not read caller source")
	}}

	result, err := testCase.writeResult()
	if err != nil || result != recoveryOverwriteWriteResultForTest(t, testCase) {
		t.Fatalf("overwrite publish result=%+v error=%v, want exact stable result", result, err)
	}
	if sourceReads != 0 {
		t.Fatalf("overwrite publish read caller source %d time(s)", sourceReads)
	}
	if !reflect.DeepEqual(testCase.base.renamePaths, [][2]string{{testCase.postPath, testCase.finalPath}}) {
		t.Fatalf("overwrite publish renames=%v, want one standard post-to-final rename", testCase.base.renamePaths)
	}
	if !reflect.DeepEqual(testCase.base.removePaths, []string{testCase.priorPath, testCase.intentPath}) {
		t.Fatalf("overwrite publish removals=%v, want exact owned prior then intent", testCase.base.removePaths)
	}
	wantFlags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
	if !reflect.DeepEqual(testCase.base.openFilePaths, []string{testCase.publishedPath}) ||
		!reflect.DeepEqual(testCase.base.openFileFlags, []int{wantFlags}) ||
		testCase.base.syncCalls != 1 {
		t.Fatalf("published marker open=%v flags=%v sync=%d, want one exclusive durable create",
			testCase.base.openFilePaths, testCase.base.openFileFlags, testCase.base.syncCalls)
	}
	assertRecoveryOverwriteAcknowledgedRemoteStateForTest(t, testCase)

	replayBase := &recoveryLocalSFTPClient{}
	replayClient := &recoveryScriptedSFTPClient{base: replayBase}
	replayClient.lstat = func(value string, _ int) (os.FileInfo, error) {
		info, statErr := replayBase.Lstat(value)
		if statErr == nil && (value == testCase.finalPath || value == testCase.priorPath) {
			return recoveryOwnedFileInfoForOverwriteTest(info), nil
		}
		return info, statErr
	}
	replayTarget := testCase.fixture.targetWithClient(replayClient)
	replayTarget.entropy = bytes.NewReader(bytes.Repeat([]byte{0x5a}, recoveryPayloadTempEntropyBytes))
	replayTarget.now = func() time.Time { return testCase.fixture.now }
	replayRequest := testCase.request
	replaySourceReads := 0
	replayRequest.Content = &recoveryReadTrackingReader{read: func([]byte) (int, error) {
		replaySourceReads++
		return 0, errors.New("acknowledged replay must not read caller source")
	}}
	replayed, replayErr := replayTarget.WriteAtomic(
		context.Background(), testCase.permit, replayRequest,
	)
	if replayErr != nil || replayed != result {
		t.Fatalf("acknowledged replay result=%+v error=%v, want %+v", replayed, replayErr, result)
	}
	if replaySourceReads != 0 || replayBase.openFileCalls != 0 || replayBase.chmodCalls != 0 ||
		replayBase.syncCalls != 0 || replayBase.renameCalls != 0 || replayBase.removeCalls != 0 {
		t.Fatalf("acknowledged replay source=%d open-file=%d chmod=%d sync=%d rename=%d remove=%d, want observation only",
			replaySourceReads, replayBase.openFileCalls, replayBase.chmodCalls, replayBase.syncCalls,
			replayBase.renameCalls, replayBase.removeCalls)
	}
}

func TestRecoverySFTPTargetFinalizeOverwriteRemovesOnlyPublishedMarker(t *testing.T) {
	testCase := newRecoverySFTPOverwritePrepareCaseForTest(
		t, bytes.Repeat([]byte("finalize-overwrite-post:"), 257),
	)
	useOwnedRecoveryOverwriteEntriesForTest(testCase)
	testCase.client.rename = nil
	published, err := testCase.writeResult()
	if err != nil || published != recoveryOverwriteWriteResultForTest(t, testCase) {
		t.Fatalf("publish overwrite before finalize result=%+v err=%v", published, err)
	}
	assertRecoveryOverwriteAcknowledgedRemoteStateForTest(t, testCase)
	permit, request := recoveryOverwriteFinalizeAuthorityForTest(t, testCase)
	removesBefore := append([]string(nil), testCase.base.removePaths...)

	finalized, err := testCase.target.FinalizeOverwrite(
		context.Background(), permit, request,
	)
	if err != nil || finalized != published {
		t.Fatalf("finalize overwrite result=%+v err=%v, want %+v", finalized, err, published)
	}
	wantRemoves := append(removesBefore, testCase.publishedPath)
	if !reflect.DeepEqual(testCase.base.removePaths, wantRemoves) {
		t.Fatalf("finalize overwrite removals=%v, want only published appended to %v",
			testCase.base.removePaths, removesBefore)
	}
	if _, err := os.Lstat(testCase.publishedPath); !os.IsNotExist(err) {
		t.Fatalf("finalized published marker err=%v, want absent", err)
	}
	final, err := os.ReadFile(testCase.finalPath)
	if err != nil || !bytes.Equal(final, testCase.postPayload) {
		t.Fatalf("finalized overwrite final=%q err=%v, want exact post", final, err)
	}
	for _, residue := range []string{testCase.intentPath, testCase.priorPath, testCase.postPath} {
		if _, err := os.Lstat(residue); !os.IsNotExist(err) {
			t.Fatalf("finalized overwrite residue %q err=%v, want absent", residue, err)
		}
	}

	replayed, err := testCase.target.FinalizeOverwrite(
		context.Background(), permit, request,
	)
	if err != nil || replayed != finalized {
		t.Fatalf("replay finalized overwrite result=%+v err=%v, want %+v", replayed, err, finalized)
	}
	if !reflect.DeepEqual(testCase.base.removePaths, wantRemoves) {
		t.Fatalf("idempotent finalize removals=%v, want unchanged %v", testCase.base.removePaths, wantRemoves)
	}
}

func TestRecoverySFTPTargetFinalizeOverwriteRequiresExactCheckpointAuthority(t *testing.T) {
	testCase := newRecoverySFTPOverwritePrepareCaseForTest(t, []byte("finalize-authority-post"))
	permit, request := recoveryOverwriteFinalizeAuthorityForTest(t, testCase)
	assertClosed := func(
		t *testing.T,
		candidate TargetFinalizeOverwritePermit,
		candidateRequest TargetFinalizeOverwriteRequest,
	) {
		t.Helper()
		testCase.fixture.resolver.calls = 0
		testCase.fixture.dialer.calls = 0
		client := &recoveryLocalSFTPClient{}
		target := testCase.fixture.targetWithClient(client)
		target.now = func() time.Time { return testCase.fixture.now }
		if _, err := target.FinalizeOverwrite(
			context.Background(), candidate, candidateRequest,
		); err != ErrInvalidTargetPermit {
			t.Fatalf("mutated finalize authority err=%v, want ErrInvalidTargetPermit", err)
		}
		if testCase.fixture.resolver.calls != 0 || testCase.fixture.dialer.calls != 0 ||
			recoveryLocalSFTPCallCountForTest(client) != 0 {
			t.Fatalf("mutated finalize reached dependency resolver=%d dialer=%d sftp=%d",
				testCase.fixture.resolver.calls, testCase.fixture.dialer.calls,
				recoveryLocalSFTPCallCountForTest(client))
		}
	}
	mutations := []struct {
		name   string
		mutate func(*targetFinalizeOverwritePermitProof)
	}{
		{name: "checkpoint", mutate: func(proof *targetFinalizeOverwritePermitProof) {
			proof.checkpointID = strings.Repeat("9", 32)
		}},
		{name: "checkpoint attempt", mutate: func(proof *targetFinalizeOverwritePermitProof) {
			proof.checkpointAttemptID = strings.Repeat("9", 32)
		}},
		{name: "checkpoint fence", mutate: func(proof *targetFinalizeOverwritePermitProof) {
			proof.checkpointAttemptFence++
		}},
		{name: "current attempt", mutate: func(proof *targetFinalizeOverwritePermitProof) {
			proof.currentAttemptID = strings.Repeat("9", 32)
		}},
		{name: "source fence", mutate: func(proof *targetFinalizeOverwritePermitProof) {
			proof.currentSourceFence.FenceToken = strings.Repeat("9", 32)
		}},
		{name: "target chain", mutate: func(proof *targetFinalizeOverwritePermitProof) {
			proof.targetChainRevision = "changed-target-chain-revision"
		}},
		{name: "post digest", mutate: func(proof *targetFinalizeOverwritePermitProof) {
			proof.expectedPostDigest = strings.Repeat("9", sha256DigestLength)
		}},
		{name: "artifacts", mutate: func(proof *targetFinalizeOverwritePermitProof) {
			proof.artifacts.publishedDocument += " "
		}},
		{name: "proof binding", mutate: func(proof *targetFinalizeOverwritePermitProof) {
			proof.bindingDigest = strings.Repeat("9", sha256DigestLength)
		}},
	}
	for _, test := range mutations {
		t.Run(test.name, func(t *testing.T) {
			candidate := permit
			proof := *permit.proof
			candidate.proof = &proof
			test.mutate(candidate.proof)
			assertClosed(t, candidate, request)
		})
	}
	t.Run("request", func(t *testing.T) {
		candidateRequest := request
		candidateRequest.ExpectedBytes++
		assertClosed(t, permit, candidateRequest)
	})
}

func TestRecoverySFTPTargetFinalizeOverwriteRemoveAmbiguityIsIdempotent(t *testing.T) {
	testCase := newRecoverySFTPOverwritePrepareCaseForTest(t, []byte("finalize-remove-ambiguity-post"))
	useOwnedRecoveryOverwriteEntriesForTest(testCase)
	testCase.client.rename = nil
	published, err := testCase.writeResult()
	if err != nil {
		t.Fatalf("publish overwrite before ambiguous finalize: %v", err)
	}
	permit, request := recoveryOverwriteFinalizeAuthorityForTest(t, testCase)
	testCase.client.remove = func(value string) error {
		if value != testCase.publishedPath {
			return fmt.Errorf("finalize attempted unexpected remove %q", value)
		}
		if err := testCase.base.Remove(value); err != nil {
			return err
		}
		return errors.New("simulated ambiguous published-marker remove")
	}
	if _, err := testCase.target.FinalizeOverwrite(
		context.Background(), permit, request,
	); err != ErrRecoveryTargetUnavailable {
		t.Fatalf("ambiguous finalize err=%v, want ErrRecoveryTargetUnavailable", err)
	}
	if _, err := os.Lstat(testCase.publishedPath); !os.IsNotExist(err) {
		t.Fatalf("ambiguous finalize published marker err=%v, want removed", err)
	}
	if len(testCase.base.removePaths) == 0 ||
		testCase.base.removePaths[len(testCase.base.removePaths)-1] != testCase.publishedPath {
		t.Fatalf("ambiguous finalize removals=%v, want published marker last", testCase.base.removePaths)
	}

	retryBase := &recoveryLocalSFTPClient{}
	retryClient := &recoveryScriptedSFTPClient{base: retryBase}
	retryClient.lstat = func(value string, _ int) (os.FileInfo, error) {
		info, statErr := retryBase.Lstat(value)
		if statErr == nil && (value == testCase.finalPath || value == testCase.priorPath) {
			return recoveryOwnedFileInfoForOverwriteTest(info), nil
		}
		return info, statErr
	}
	retryTarget := testCase.fixture.targetWithClient(retryClient)
	retryTarget.now = func() time.Time { return testCase.fixture.now }
	replayed, err := retryTarget.FinalizeOverwrite(context.Background(), permit, request)
	if err != nil || replayed != published {
		t.Fatalf("retry ambiguous finalize result=%+v err=%v, want %+v", replayed, err, published)
	}
	if retryBase.removeCalls != 0 {
		t.Fatalf("idempotent ambiguous retry removals=%v, want none", retryBase.removePaths)
	}
}

func TestRecoverySFTPTargetOverwriteErrorResourceAndPrivacyMatrix(t *testing.T) {
	var capturedLogs bytes.Buffer
	previousLogger := logger.Log
	logger.Log = zerolog.New(&capturedLogs)
	t.Cleanup(func() { logger.Log = previousLogger })

	t.Run("formatted authorities hide private overwrite products", func(t *testing.T) {
		testCase := newRecoverySFTPOverwritePrepareCaseForTest(
			t, []byte("FAKE_PRIVATE_OVERWRITE_CONTENT_FOR_TEST_ONLY"),
		)
		finalizePermit, finalizeRequest := recoveryOverwriteFinalizeAuthorityForTest(t, testCase)
		products := []any{
			testCase.request.Object, testCase.permit, testCase.request,
			finalizePermit, finalizeRequest,
		}
		encoded, err := json.Marshal(products)
		if err != nil {
			t.Fatalf("marshal overwrite privacy products: %v", err)
		}
		corpus := string(encoded)
		for _, product := range products {
			corpus += "\n" + fmt.Sprintf("%v\n%+v\n%#v", product, product, product)
		}
		for _, forbidden := range []string{
			testCase.fixture.binding.RootLocator,
			testCase.fixture.binding.CredentialRevision,
			testCase.request.Object.PrivateRelativeLocator,
			testCase.artifacts.token,
			testCase.artifacts.intentComponent,
			testCase.artifacts.priorComponent,
			testCase.artifacts.postComponent,
			testCase.artifacts.publishedComponent,
			testCase.artifacts.intentDocument,
			testCase.artifacts.publishedDocument,
			"FAKE_PRIVATE_OVERWRITE_CONTENT_FOR_TEST_ONLY",
		} {
			if forbidden != "" && strings.Contains(corpus, forbidden) {
				t.Fatalf("formatted overwrite products leaked %q: %s", forbidden, corpus)
			}
		}
	})

	t.Run("file mutation and close errors are sanitized and owned once", func(t *testing.T) {
		rawFailure := errors.New(
			"RAW_OVERWRITE_DEPENDENCY_FAILURE private-host private-user private-sftp-status",
		)
		stages := []string{
			"open with resource and error", "read", "stat", "file close",
			"OpenFile with resource and error", "write", "Sync", "rename", "remove",
			"SFTP close", "SSH close",
		}
		for _, stage := range stages {
			t.Run(stage, func(t *testing.T) {
				testCase := newRecoverySFTPOverwritePrepareCaseForTest(
					t, []byte("FAKE_PRIVATE_OVERWRITE_DEPENDENCY_CONTENT"),
				)
				useOwnedRecoveryOverwriteEntriesForTest(testCase)
				testCase.client.rename = nil
				switch stage {
				case "rename":
					installRecoveryOverwritePreparedTupleForTest(t, testCase)
				case "remove":
					installRecoveryOverwriteAcknowledgedTupleForTest(t, testCase)
				}

				openedFiles := make([]*recoveryCloseCountingSFTPFile, 0, 16)
				faultInjected := false
				wrap := func(file recoveryTargetSFTPFile) *recoveryCloseCountingSFTPFile {
					counted := &recoveryCloseCountingSFTPFile{recoveryTargetSFTPFile: file}
					openedFiles = append(openedFiles, counted)
					return counted
				}
				testCase.client.open = func(value string) (recoveryTargetSFTPFile, error) {
					file, err := testCase.base.Open(value)
					if err != nil {
						return nil, err
					}
					if !faultInjected && (stage == "open with resource and error" ||
						stage == "read" || stage == "stat" || stage == "file close") {
						faultInjected = true
						scripted := &recoveryScriptedSFTPFile{base: file}
						switch stage {
						case "open with resource and error":
							counted := wrap(scripted)
							return counted, rawFailure
						case "read":
							scripted.read = func([]byte) (int, error) { return 0, rawFailure }
						case "stat":
							scripted.stat = func() (os.FileInfo, error) { return nil, rawFailure }
						case "file close":
							scripted.close = func() error {
								if closeErr := file.Close(); closeErr != nil {
									return closeErr
								}
								return rawFailure
							}
						}
						return wrap(scripted), nil
					}
					return wrap(file), nil
				}
				testCase.client.openFile = func(value string, flags int) (recoveryTargetSFTPFile, error) {
					file, err := testCase.base.OpenFile(value, flags)
					if err != nil {
						return nil, err
					}
					if !faultInjected && (stage == "OpenFile with resource and error" ||
						stage == "write" || stage == "Sync") {
						faultInjected = true
						scripted := &recoveryScriptedSFTPFile{base: file}
						switch stage {
						case "OpenFile with resource and error":
							counted := wrap(scripted)
							return counted, rawFailure
						case "write":
							scripted.write = func([]byte) (int, error) { return 0, rawFailure }
						case "Sync":
							scripted.sync = func() error { return rawFailure }
						}
						return wrap(scripted), nil
					}
					return wrap(file), nil
				}
				if stage == "rename" {
					testCase.client.rename = func(oldName, newName string) error {
						testCase.base.renameCalls++
						testCase.base.renamePaths = append(
							testCase.base.renamePaths, [2]string{oldName, newName},
						)
						return rawFailure
					}
				}
				if stage == "remove" {
					testCase.client.remove = func(value string) error {
						testCase.base.removeCalls++
						testCase.base.removePaths = append(testCase.base.removePaths, value)
						return rawFailure
					}
				}
				if stage == "SFTP close" {
					testCase.client.close = func() error {
						testCase.base.closeCalls++
						return rawFailure
					}
				}
				sshCloseCalls := 0
				testCase.target.sessions = newRecoveryTargetSessionFactoryForTest(
					testCase.fixture.resolver, testCase.fixture.dialer,
					func(*ssh.Client) (recoveryTargetSFTPClient, error) {
						return testCase.client, nil
					},
					func(*ssh.Client) error {
						sshCloseCalls++
						if stage == "SSH close" {
							return rawFailure
						}
						return nil
					},
				)

				result, err := testCase.target.WriteAtomic(
					context.Background(), testCase.permit, testCase.request,
				)
				if result != (TargetWriteResult{}) || err != ErrRecoveryTargetUnavailable {
					t.Fatalf("%s result=%+v error=%v, want zero/exact unavailable", stage, result, err)
				}
				for index, file := range openedFiles {
					if file.closeCalls != 1 {
						t.Fatalf("%s file %d close calls=%d, want exactly one", stage, index, file.closeCalls)
					}
				}
				if testCase.base.closeCalls != 1 || sshCloseCalls != 1 {
					t.Fatalf(
						"%s SFTP/SSH close=%d/%d, want exactly one each",
						stage, testCase.base.closeCalls, sshCloseCalls,
					)
				}
				if stage == "rename" && testCase.base.renameCalls != 1 {
					t.Fatalf("rename calls=%d, want one failed mutation", testCase.base.renameCalls)
				}
				if stage == "remove" && testCase.base.removeCalls != 1 {
					t.Fatalf("remove calls=%d, want one failed mutation", testCase.base.removeCalls)
				}

				encoded, jsonErr := json.Marshal([]any{
					testCase.permit, testCase.request, result, testCase.fixture.dialer.audit,
				})
				if jsonErr != nil {
					t.Fatalf("%s marshal products: %v", stage, jsonErr)
				}
				corpus := strings.Join([]string{
					err.Error(), string(encoded), fmt.Sprintf("%+v", result),
					fmt.Sprintf("%+v", testCase.fixture.dialer.audit), capturedLogs.String(),
				}, "\n")
				for _, forbidden := range []string{
					rawFailure.Error(), testCase.fixture.binding.RootLocator,
					testCase.fixture.binding.CredentialRevision,
					testCase.finalPath, testCase.request.Object.PrivateRelativeLocator,
					testCase.artifacts.token, testCase.artifacts.intentComponent,
					testCase.artifacts.priorComponent, testCase.artifacts.postComponent,
					testCase.artifacts.publishedComponent,
					testCase.artifacts.intentDocument, testCase.artifacts.publishedDocument,
					string(testCase.priorPayload), string(testCase.postPayload),
				} {
					if forbidden != "" && strings.Contains(corpus, forbidden) {
						t.Fatalf("%s captured product leaked %q: %s", stage, forbidden, corpus)
					}
				}
			})
		}
	})

	t.Run("context identity wins dependency and close noise", func(t *testing.T) {
		rawFailure := errors.New(
			"RAW_OVERWRITE_CONTEXT_FAILURE private-host private-user private-sftp-status",
		)
		stages := []string{
			"resolver", "dial", "SFTP opener", "open", "read", "stat",
			"write", "Sync", "rename", "remove", "file close", "SFTP close", "SSH close",
		}
		for _, identity := range []struct {
			name string
			want error
		}{
			{name: "cancellation", want: context.Canceled},
			{name: "deadline", want: context.DeadlineExceeded},
		} {
			t.Run(identity.name, func(t *testing.T) {
				for _, stage := range stages {
					t.Run(stage, func(t *testing.T) {
						testCase := newRecoverySFTPOverwritePrepareCaseForTest(
							t, []byte("FAKE_PRIVATE_OVERWRITE_CONTEXT_CONTENT"),
						)
						useOwnedRecoveryOverwriteEntriesForTest(testCase)
						testCase.client.rename = nil
						switch stage {
						case "rename":
							installRecoveryOverwritePreparedTupleForTest(t, testCase)
						case "remove":
							installRecoveryOverwriteAcknowledgedTupleForTest(t, testCase)
						}

						var ctx context.Context
						var cancel context.CancelFunc
						var trigger func()
						if identity.want == context.Canceled {
							ctx, cancel = context.WithCancel(context.Background())
							trigger = cancel
						} else {
							ctx, cancel = context.WithDeadline(
								context.Background(), time.Now().Add(100*time.Millisecond),
							)
							trigger = func() { <-ctx.Done() }
						}
						t.Cleanup(cancel)

						openedFiles := make([]*recoveryCloseCountingSFTPFile, 0, 16)
						faultInjected := false
						wrap := func(file recoveryTargetSFTPFile) *recoveryCloseCountingSFTPFile {
							counted := &recoveryCloseCountingSFTPFile{recoveryTargetSFTPFile: file}
							openedFiles = append(openedFiles, counted)
							return counted
						}
						testCase.client.open = func(value string) (recoveryTargetSFTPFile, error) {
							file, err := testCase.base.Open(value)
							if err != nil {
								return nil, err
							}
							if !faultInjected && (stage == "open" || stage == "read" ||
								stage == "stat" || stage == "file close") {
								faultInjected = true
								scripted := &recoveryScriptedSFTPFile{base: file}
								switch stage {
								case "open":
									counted := wrap(scripted)
									trigger()
									return counted, rawFailure
								case "read":
									scripted.read = func([]byte) (int, error) {
										trigger()
										return 0, rawFailure
									}
								case "stat":
									scripted.stat = func() (os.FileInfo, error) {
										trigger()
										return nil, rawFailure
									}
								case "file close":
									scripted.close = func() error {
										if closeErr := file.Close(); closeErr != nil {
											return closeErr
										}
										trigger()
										return rawFailure
									}
								}
								return wrap(scripted), nil
							}
							return wrap(file), nil
						}
						testCase.client.openFile = func(
							value string, flags int,
						) (recoveryTargetSFTPFile, error) {
							file, err := testCase.base.OpenFile(value, flags)
							if err != nil {
								return nil, err
							}
							if !faultInjected && (stage == "write" || stage == "Sync") {
								faultInjected = true
								scripted := &recoveryScriptedSFTPFile{base: file}
								if stage == "write" {
									scripted.write = func([]byte) (int, error) {
										trigger()
										return 0, rawFailure
									}
								} else {
									scripted.sync = func() error {
										trigger()
										return rawFailure
									}
								}
								return wrap(scripted), nil
							}
							return wrap(file), nil
						}
						if stage == "rename" {
							testCase.client.rename = func(oldName, newName string) error {
								testCase.base.renameCalls++
								testCase.base.renamePaths = append(
									testCase.base.renamePaths, [2]string{oldName, newName},
								)
								trigger()
								return rawFailure
							}
						}
						if stage == "remove" {
							testCase.client.remove = func(value string) error {
								testCase.base.removeCalls++
								testCase.base.removePaths = append(testCase.base.removePaths, value)
								trigger()
								return rawFailure
							}
						}
						if stage == "SFTP close" {
							testCase.client.close = func() error {
								testCase.base.closeCalls++
								trigger()
								return rawFailure
							}
						}

						resolver := &recoveryTargetNodeSessionResolverFake{
							result: testCase.fixture.resolver.result,
						}
						if stage == "resolver" {
							resolver.resolve = func(
								context.Context, uint, TargetPurpose,
							) (recoveryTargetNodeSession, error) {
								trigger()
								return recoveryTargetNodeSession{}, rawFailure
							}
						}
						dialer := &recoveryTargetNodeDialerFake{}
						dialer.dial = func(
							context.Context, model.Node, string, sshutil.DialAuditContext,
						) (*ssh.Client, error) {
							if stage == "dial" {
								trigger()
								return new(ssh.Client), rawFailure
							}
							return new(ssh.Client), nil
						}
						sshCloseCalls := 0
						testCase.target.sessions = newRecoveryTargetSessionFactoryForTest(
							resolver, dialer,
							func(*ssh.Client) (recoveryTargetSFTPClient, error) {
								if stage == "SFTP opener" {
									trigger()
									return testCase.client, rawFailure
								}
								return testCase.client, nil
							},
							func(*ssh.Client) error {
								sshCloseCalls++
								if stage == "SSH close" {
									trigger()
									return rawFailure
								}
								return nil
							},
						)

						result, err := testCase.target.WriteAtomic(
							ctx, testCase.permit, testCase.request,
						)
						if result != (TargetWriteResult{}) || err != identity.want {
							t.Fatalf(
								"%s %s result=%+v error=%v, want zero/exact %v",
								identity.name, stage, result, err, identity.want,
							)
						}
						for index, file := range openedFiles {
							if file.closeCalls != 1 {
								t.Fatalf(
									"%s %s file %d close calls=%d, want exactly one",
									identity.name, stage, index, file.closeCalls,
								)
							}
						}
						wantSFTPClose, wantSSHClose := 1, 1
						switch stage {
						case "resolver":
							wantSFTPClose, wantSSHClose = 0, 0
						case "dial":
							wantSFTPClose = 0
						}
						if testCase.base.closeCalls != wantSFTPClose || sshCloseCalls != wantSSHClose {
							t.Fatalf(
								"%s %s SFTP/SSH close=%d/%d, want %d/%d",
								identity.name, stage, testCase.base.closeCalls, sshCloseCalls,
								wantSFTPClose, wantSSHClose,
							)
						}
						for _, forbidden := range []string{
							rawFailure.Error(), testCase.fixture.binding.RootLocator,
							testCase.request.Object.PrivateRelativeLocator,
							testCase.artifacts.token, string(testCase.postPayload),
						} {
							if forbidden != "" && strings.Contains(err.Error(), forbidden) {
								t.Fatalf("%s %s error leaked %q: %v", identity.name, stage, forbidden, err)
							}
						}
					})
				}
			})
		}
	})

	t.Run("live permit revocation stops every next mutation", func(t *testing.T) {
		type revocationCase struct {
			name       string
			revokeAt   int
			configure  func(*testing.T, *recoverySFTPOverwritePrepareCaseForTest)
			wantRename int
			finalize   bool
		}
		cases := []revocationCase{
			{name: "intent create", revokeAt: 3},
			{
				name: "post create", revokeAt: 3,
				configure: func(t *testing.T, value *recoverySFTPOverwritePrepareCaseForTest) {
					t.Helper()
					if err := os.WriteFile(
						value.intentPath, []byte(value.artifacts.intentDocument), 0o600,
					); err != nil {
						t.Fatalf("install intent-only revocation state: %v", err)
					}
				},
			},
			{
				name: "capture rename", revokeAt: 3,
				configure: installRecoveryOverwritePreparedTupleForTest,
			},
			{
				name: "captured mismatch restore", revokeAt: 4, wantRename: 1,
				configure: func(t *testing.T, value *recoverySFTPOverwritePrepareCaseForTest) {
					t.Helper()
					installRecoveryOverwritePreparedTupleForTest(t, value)
					value.client.rename = func(oldName, newName string) error {
						if oldName == value.finalPath {
							if err := os.Remove(value.finalPath); err != nil {
								t.Fatalf("remove prior before revocation restore: %v", err)
							}
							if err := os.WriteFile(
								value.finalPath, []byte("captured-mismatch-before-revocation"), 0o640,
							); err != nil {
								t.Fatalf("install captured mismatch before revocation: %v", err)
							}
						}
						return value.base.Rename(oldName, newName)
					}
				},
			},
			{
				name: "publish rename", revokeAt: 2,
				configure: installRecoveryOverwriteCapturedTupleForTest,
			},
			{
				name: "published marker create", revokeAt: 2,
				configure: installRecoveryOverwritePublishedUnacknowledgedTupleForTest,
			},
			{
				name: "prior remove", revokeAt: 2,
				configure: installRecoveryOverwriteAcknowledgedTupleForTest,
			},
			{
				name: "post remove", revokeAt: 2,
				configure: func(t *testing.T, value *recoverySFTPOverwritePrepareCaseForTest) {
					t.Helper()
					installRecoveryOverwriteAcknowledgedTupleForTest(t, value)
					if err := os.Remove(value.priorPath); err != nil {
						t.Fatalf("remove prior before post revocation: %v", err)
					}
					if err := os.WriteFile(value.postPath, value.postPayload, 0o600); err != nil {
						t.Fatalf("install post residue before revocation: %v", err)
					}
				},
			},
			{
				name: "intent remove", revokeAt: 2,
				configure: func(t *testing.T, value *recoverySFTPOverwritePrepareCaseForTest) {
					t.Helper()
					installRecoveryOverwriteAcknowledgedTupleForTest(t, value)
					if err := os.Remove(value.priorPath); err != nil {
						t.Fatalf("remove prior before intent revocation: %v", err)
					}
				},
			},
			{
				name: "finalize published remove", revokeAt: 2, finalize: true,
				configure: func(t *testing.T, value *recoverySFTPOverwritePrepareCaseForTest) {
					t.Helper()
					installRecoveryOverwriteAcknowledgedTupleForTest(t, value)
					for _, residue := range []string{value.priorPath, value.intentPath} {
						if err := os.Remove(residue); err != nil {
							t.Fatalf("remove finalize revocation residue %q: %v", residue, err)
						}
					}
				},
			},
		}

		for _, test := range cases {
			t.Run(test.name, func(t *testing.T) {
				testCase := newRecoverySFTPOverwritePrepareCaseForTest(
					t, []byte("permit-revocation-post-content"),
				)
				useOwnedRecoveryOverwriteEntriesForTest(testCase)
				testCase.client.rename = nil
				if test.configure != nil {
					test.configure(t, testCase)
				}
				validationCalls := 0
				revoke := func(time.Time) error {
					validationCalls++
					if validationCalls >= test.revokeAt {
						return ErrInvalidTargetPermit
					}
					return nil
				}

				var result TargetWriteResult
				var err error
				if test.finalize {
					permit, request := recoveryOverwriteFinalizeAuthorityForTest(t, testCase)
					validationCalls = 0
					permit.permit.proof.validateAt = revoke
					result, err = testCase.target.FinalizeOverwrite(
						context.Background(), permit, request,
					)
				} else {
					testCase.permit.permit.proof.validateAt = revoke
					result, err = testCase.writeResult()
				}
				if result != (TargetWriteResult{}) || err != ErrInvalidTargetPermit {
					t.Fatalf(
						"revocation result=%+v error=%v calls=%d, want zero/exact invalid",
						result, err, validationCalls,
					)
				}
				if validationCalls != test.revokeAt {
					t.Fatalf("validation calls=%d, want revocation at %d", validationCalls, test.revokeAt)
				}
				if testCase.base.renameCalls != test.wantRename {
					t.Fatalf(
						"rename calls=%d paths=%v, want %d before revocation",
						testCase.base.renameCalls, testCase.base.renamePaths, test.wantRename,
					)
				}
				if testCase.base.openFileCalls != 0 || testCase.base.removeCalls != 0 {
					t.Fatalf(
						"revocation crossed next mutation: openFile=%d remove=%d",
						testCase.base.openFileCalls, testCase.base.removeCalls,
					)
				}
				if testCase.base.closeCalls != 1 {
					t.Fatalf("revocation SFTP close calls=%d, want one", testCase.base.closeCalls)
				}
			})
		}
	})

	t.Run("session dependency errors close returned resources", func(t *testing.T) {
		rawFailure := errors.New(
			"RAW_OVERWRITE_SESSION_FAILURE private-host private-user private-sftp-status",
		)
		privateHost := "FAKE_PRIVATE_OVERWRITE_HOST"
		privateUser := "FAKE_PRIVATE_OVERWRITE_USER"
		privateCredential := "FAKE_PRIVATE_OVERWRITE_CREDENTIAL"
		for _, stage := range []string{"resolver", "dial", "SFTP opener"} {
			t.Run(stage, func(t *testing.T) {
				testCase := newRecoverySFTPOverwritePrepareCaseForTest(
					t, []byte("FAKE_PRIVATE_OVERWRITE_SESSION_CONTENT"),
				)
				resolved := testCase.fixture.resolver.result
				resolved.Node.Host = privateHost
				resolved.Node.Username = privateUser
				resolved.Node.Password = privateCredential
				resolver := &recoveryTargetNodeSessionResolverFake{result: resolved}
				dialer := &recoveryTargetNodeDialerFake{}
				sftpClient := &recoveryScriptedSFTPClient{
					base: &recoveryLocalSFTPClient{},
				}
				sshCloseCalls := 0
				if stage == "resolver" {
					resolver.err = rawFailure
				}
				dialer.dial = func(
					context.Context, model.Node, string, sshutil.DialAuditContext,
				) (*ssh.Client, error) {
					if stage == "dial" {
						return new(ssh.Client), rawFailure
					}
					return new(ssh.Client), nil
				}
				factory := newRecoveryTargetSessionFactoryForTest(
					resolver, dialer,
					func(*ssh.Client) (recoveryTargetSFTPClient, error) {
						if stage == "SFTP opener" {
							return sftpClient, rawFailure
						}
						return sftpClient, nil
					},
					func(*ssh.Client) error {
						sshCloseCalls++
						return nil
					},
				)
				testCase.target.sessions = factory

				result, err := testCase.target.WriteAtomic(
					context.Background(), testCase.permit, testCase.request,
				)
				if result != (TargetWriteResult{}) || err != ErrRecoveryTargetUnavailable {
					t.Fatalf("%s result=%+v error=%v, want zero/exact unavailable", stage, result, err)
				}
				wantSSHClose := 0
				wantSFTPClose := 0
				if stage == "dial" || stage == "SFTP opener" {
					wantSSHClose = 1
				}
				if stage == "SFTP opener" {
					wantSFTPClose = 1
				}
				if sshCloseCalls != wantSSHClose || sftpClient.base.closeCalls != wantSFTPClose {
					t.Fatalf(
						"%s SFTP/SSH close=%d/%d, want %d/%d",
						stage, sftpClient.base.closeCalls, sshCloseCalls,
						wantSFTPClose, wantSSHClose,
					)
				}
				if resolver.calls != 1 ||
					(stage == "resolver" && (dialer.calls != 0 || testCase.base.openFileCalls != 0)) {
					t.Fatalf(
						"%s resolver/dial/mutation=%d/%d/%d",
						stage, resolver.calls, dialer.calls, testCase.base.openFileCalls,
					)
				}
				encoded, jsonErr := json.Marshal([]any{
					result, testCase.permit, testCase.request, dialer.audit,
				})
				if jsonErr != nil {
					t.Fatalf("%s marshal session products: %v", stage, jsonErr)
				}
				corpus := strings.Join([]string{
					err.Error(), string(encoded), fmt.Sprintf("%v\n%+v\n%#v", result, result, result),
					fmt.Sprintf("%v\n%+v\n%#v", testCase.permit, testCase.permit, testCase.permit),
					fmt.Sprintf("%v\n%+v\n%#v", testCase.request, testCase.request, testCase.request),
					fmt.Sprintf("%+v", dialer.audit), capturedLogs.String(),
				}, "\n")
				for _, forbidden := range []string{
					rawFailure.Error(), testCase.fixture.binding.RootLocator,
					testCase.fixture.binding.CredentialRevision,
					privateHost, privateUser, privateCredential,
					testCase.finalPath, testCase.request.Object.PrivateRelativeLocator,
					testCase.artifacts.token, testCase.artifacts.intentComponent,
					testCase.artifacts.priorComponent, testCase.artifacts.postComponent,
					testCase.artifacts.publishedComponent,
					testCase.artifacts.intentDocument, testCase.artifacts.publishedDocument,
					"FAKE_PRIVATE_OVERWRITE_SESSION_CONTENT",
				} {
					if forbidden != "" && strings.Contains(corpus, forbidden) {
						t.Fatalf("%s captured product leaked %q: %s", stage, forbidden, corpus)
					}
				}
			})
		}
	})

	t.Run("target boundary emits zero direct logs", func(t *testing.T) {
		if capturedLogs.Len() != 0 {
			t.Fatalf("overwrite target emitted logs: %s", capturedLogs.String())
		}
		source, err := os.ReadFile("target.go")
		if err != nil {
			t.Fatalf("read target source for direct-log gate: %v", err)
		}
		for _, forbidden := range []string{"logger.", "log."} {
			if bytes.Contains(source, []byte(forbidden)) {
				t.Fatalf("target source contains direct logging marker %q", forbidden)
			}
		}
	})
}

func TestRecoverySFTPTargetOverwriteCrashStateMatrix(t *testing.T) {
	type stateCase struct {
		name       string
		install    func(*testing.T, *recoverySFTPOverwritePrepareCaseForTest)
		wantErr    error
		wantResult bool
	}
	cases := []stateCase{
		{name: "fresh", wantResult: true},
		{
			name: "intent-only",
			install: func(t *testing.T, value *recoverySFTPOverwritePrepareCaseForTest) {
				t.Helper()
				if err := os.WriteFile(value.intentPath, []byte(value.artifacts.intentDocument), 0o600); err != nil {
					t.Fatalf("install intent-only state: %v", err)
				}
				if err := os.Chmod(value.intentPath, 0o600); err != nil {
					t.Fatalf("chmod intent-only state: %v", err)
				}
			}, wantResult: true,
		},
		{name: "prepared", install: installRecoveryOverwritePreparedTupleForTest, wantResult: true},
		{name: "captured", install: installRecoveryOverwriteCapturedTupleForTest, wantResult: true},
		{
			name:       "published-unacknowledged",
			install:    installRecoveryOverwritePublishedUnacknowledgedTupleForTest,
			wantResult: true,
		},
		{
			name:       "acknowledged with residue",
			install:    installRecoveryOverwriteAcknowledgedTupleForTest,
			wantResult: true,
		},
		{
			name: "acknowledged cleaned replay",
			install: func(t *testing.T, value *recoverySFTPOverwritePrepareCaseForTest) {
				installRecoveryOverwriteAcknowledgedTupleForTest(t, value)
				for _, artifact := range []string{value.priorPath, value.intentPath} {
					if err := os.Remove(artifact); err != nil {
						t.Fatalf("install cleaned acknowledged state remove %q: %v", artifact, err)
					}
				}
			}, wantResult: true,
		},
		{
			name: "matching final alone is insufficient",
			install: func(t *testing.T, value *recoverySFTPOverwritePrepareCaseForTest) {
				t.Helper()
				if err := os.Remove(value.finalPath); err != nil {
					t.Fatalf("remove prior before lone matching final: %v", err)
				}
				if err := os.WriteFile(value.finalPath, value.postPayload, 0o600); err != nil {
					t.Fatalf("install lone matching final: %v", err)
				}
			}, wantErr: ErrRecoveryTargetChanged,
		},
		{
			name: "published collision is closed",
			install: func(t *testing.T, value *recoverySFTPOverwritePrepareCaseForTest) {
				installRecoveryOverwritePublishedUnacknowledgedTupleForTest(t, value)
				if err := os.WriteFile(value.publishedPath, []byte("malformed-published"), 0o600); err != nil {
					t.Fatalf("install malformed published collision: %v", err)
				}
			}, wantErr: ErrRecoveryTargetChanged,
		},
	}

	for _, state := range cases {
		t.Run(state.name, func(t *testing.T) {
			testCase := newRecoverySFTPOverwritePrepareCaseForTest(t, []byte("state-matrix-post"))
			if state.install != nil {
				state.install(t, testCase)
			}
			useOwnedRecoveryOverwriteEntriesForTest(testCase)
			testCase.client.rename = nil
			result, err := testCase.writeResult()
			if err != state.wantErr {
				t.Fatalf("state %s error=%v, want exact %v", state.name, err, state.wantErr)
			}
			if state.wantResult {
				want := recoveryOverwriteWriteResultForTest(t, testCase)
				if result != want {
					t.Fatalf("state %s result=%+v, want %+v", state.name, result, want)
				}
				assertRecoveryOverwriteAcknowledgedRemoteStateForTest(t, testCase)
			} else if result != (TargetWriteResult{}) {
				t.Fatalf("state %s result=%+v, want zero on conflict", state.name, result)
			}
		})
	}

	t.Run("interruption resume or closed conflict", func(t *testing.T) {
		type interruptionCase struct {
			name         string
			inject       func(*recoverySFTPOverwritePrepareCaseForTest)
			retryChanged bool
		}
		interruptions := []interruptionCase{
			{
				name: "before intent create",
				inject: func(value *recoverySFTPOverwritePrepareCaseForTest) {
					value.client.openFile = func(path string, flags int) (recoveryTargetSFTPFile, error) {
						if path == value.intentPath {
							return nil, errors.New("scripted before intent create")
						}
						return value.base.OpenFile(path, flags)
					}
				},
			},
			{
				name: "after intent create",
				inject: func(value *recoverySFTPOverwritePrepareCaseForTest) {
					value.client.openFile = func(path string, flags int) (recoveryTargetSFTPFile, error) {
						file, err := value.base.OpenFile(path, flags)
						if err != nil || path != value.intentPath {
							return file, err
						}
						return &recoveryScriptedSFTPFile{base: file, close: func() error {
							if closeErr := file.Close(); closeErr != nil {
								return closeErr
							}
							return errors.New("scripted after intent create")
						}}, nil
					}
				},
			},
			{
				name: "before post open",
				inject: func(value *recoverySFTPOverwritePrepareCaseForTest) {
					value.client.openFile = func(path string, flags int) (recoveryTargetSFTPFile, error) {
						if path == value.postPath {
							return nil, errors.New("scripted before post open")
						}
						return value.base.OpenFile(path, flags)
					}
				},
			},
			{
				name: "during post write closes conflict",
				inject: func(value *recoverySFTPOverwritePrepareCaseForTest) {
					value.client.openFile = func(path string, flags int) (recoveryTargetSFTPFile, error) {
						file, err := value.base.OpenFile(path, flags)
						if err != nil || path != value.postPath {
							return file, err
						}
						return &recoveryScriptedSFTPFile{base: file, write: func([]byte) (int, error) {
							return 0, errors.New("scripted partial post write")
						}}, nil
					}
				},
				retryChanged: true,
			},
			{
				name: "after post sync",
				inject: func(value *recoverySFTPOverwritePrepareCaseForTest) {
					value.client.openFile = func(path string, flags int) (recoveryTargetSFTPFile, error) {
						file, err := value.base.OpenFile(path, flags)
						if err != nil || path != value.postPath {
							return file, err
						}
						return &recoveryScriptedSFTPFile{base: file, sync: func() error {
							if syncErr := file.Sync(); syncErr != nil {
								return syncErr
							}
							return errors.New("scripted after post sync")
						}}, nil
					}
				},
			},
			{
				name: "after post close",
				inject: func(value *recoverySFTPOverwritePrepareCaseForTest) {
					value.client.openFile = func(path string, flags int) (recoveryTargetSFTPFile, error) {
						file, err := value.base.OpenFile(path, flags)
						if err != nil || path != value.postPath {
							return file, err
						}
						return &recoveryScriptedSFTPFile{base: file, close: func() error {
							if closeErr := file.Close(); closeErr != nil {
								return closeErr
							}
							return errors.New("scripted after post close")
						}}, nil
					}
				},
			},
			{
				name: "during post verify",
				inject: func(value *recoverySFTPOverwritePrepareCaseForTest) {
					value.client.open = func(path string) (recoveryTargetSFTPFile, error) {
						if path == value.postPath {
							return nil, errors.New("scripted post verify")
						}
						return value.base.Open(path)
					}
				},
			},
			{
				name: "before capture",
				inject: func(value *recoverySFTPOverwritePrepareCaseForTest) {
					value.client.rename = func(oldName string, newName string) error {
						if oldName == value.finalPath && newName == value.priorPath {
							return errors.New("scripted before capture")
						}
						return value.base.Rename(oldName, newName)
					}
				},
			},
			{
				name: "after ambiguous capture",
				inject: func(value *recoverySFTPOverwritePrepareCaseForTest) {
					value.client.rename = func(oldName string, newName string) error {
						if oldName == value.finalPath && newName == value.priorPath {
							if err := value.base.Rename(oldName, newName); err != nil {
								return err
							}
							return errors.New("scripted after ambiguous capture")
						}
						return value.base.Rename(oldName, newName)
					}
				},
			},
			{
				name: "during captured read",
				inject: func(value *recoverySFTPOverwritePrepareCaseForTest) {
					value.client.open = func(path string) (recoveryTargetSFTPFile, error) {
						if path == value.priorPath {
							return nil, errors.New("scripted captured read")
						}
						return value.base.Open(path)
					}
				},
			},
			{
				name: "before publish",
				inject: func(value *recoverySFTPOverwritePrepareCaseForTest) {
					value.client.rename = func(oldName string, newName string) error {
						if oldName == value.postPath && newName == value.finalPath {
							return errors.New("scripted before publish")
						}
						return value.base.Rename(oldName, newName)
					}
				},
			},
			{
				name: "after ambiguous publish",
				inject: func(value *recoverySFTPOverwritePrepareCaseForTest) {
					value.client.rename = func(oldName string, newName string) error {
						if oldName == value.postPath && newName == value.finalPath {
							if err := value.base.Rename(oldName, newName); err != nil {
								return err
							}
							return errors.New("scripted after ambiguous publish")
						}
						return value.base.Rename(oldName, newName)
					}
				},
			},
			{
				name: "during final post verify",
				inject: func(value *recoverySFTPOverwritePrepareCaseForTest) {
					value.client.open = func(path string) (recoveryTargetSFTPFile, error) {
						if path == value.finalPath {
							payload, readErr := os.ReadFile(path)
							if readErr == nil && bytes.Equal(payload, value.postPayload) {
								return nil, errors.New("scripted final post verify")
							}
						}
						return value.base.Open(path)
					}
				},
			},
			{
				name: "before published create",
				inject: func(value *recoverySFTPOverwritePrepareCaseForTest) {
					value.client.openFile = func(path string, flags int) (recoveryTargetSFTPFile, error) {
						if path == value.publishedPath {
							return nil, errors.New("scripted before published create")
						}
						return value.base.OpenFile(path, flags)
					}
				},
			},
			{
				name: "after published sync",
				inject: func(value *recoverySFTPOverwritePrepareCaseForTest) {
					value.client.openFile = func(path string, flags int) (recoveryTargetSFTPFile, error) {
						file, err := value.base.OpenFile(path, flags)
						if err != nil || path != value.publishedPath {
							return file, err
						}
						return &recoveryScriptedSFTPFile{base: file, sync: func() error {
							if syncErr := file.Sync(); syncErr != nil {
								return syncErr
							}
							return errors.New("scripted after published sync")
						}}, nil
					}
				},
			},
			{
				name: "before prior removal",
				inject: func(value *recoverySFTPOverwritePrepareCaseForTest) {
					value.client.remove = func(path string) error {
						if path == value.priorPath {
							return errors.New("scripted before prior removal")
						}
						return value.base.Remove(path)
					}
				},
			},
			{
				name: "after ambiguous prior removal",
				inject: func(value *recoverySFTPOverwritePrepareCaseForTest) {
					value.client.remove = func(path string) error {
						if path == value.priorPath {
							if err := value.base.Remove(path); err != nil {
								return err
							}
							return errors.New("scripted after prior removal")
						}
						return value.base.Remove(path)
					}
				},
			},
			{
				name: "after ambiguous intent removal",
				inject: func(value *recoverySFTPOverwritePrepareCaseForTest) {
					value.client.remove = func(path string) error {
						if err := value.base.Remove(path); err != nil {
							return err
						}
						if path == value.intentPath {
							return errors.New("scripted after intent removal")
						}
						return nil
					}
				},
			},
			{
				name: "after remote success session close",
				inject: func(value *recoverySFTPOverwritePrepareCaseForTest) {
					value.client.close = func() error {
						return errors.New("scripted session close")
					}
				},
			},
		}

		for _, interruption := range interruptions {
			t.Run(interruption.name, func(t *testing.T) {
				testCase := newRecoverySFTPOverwritePrepareCaseForTest(
					t, bytes.Repeat([]byte("interrupted-post:"), 257),
				)
				useOwnedRecoveryOverwriteEntriesForTest(testCase)
				testCase.client.rename = nil
				interruption.inject(testCase)
				result, err := testCase.writeResult()
				if result != (TargetWriteResult{}) || err != ErrRecoveryTargetUnavailable {
					t.Fatalf("first interrupted result=%+v error=%v, want zero/unavailable", result, err)
				}

				retried, retryBase, retryErr := retryRecoveryOverwriteWithFreshTargetForTest(testCase)
				if interruption.retryChanged {
					if retried != (TargetWriteResult{}) || retryErr != ErrRecoveryTargetChanged {
						t.Fatalf("closed retry result=%+v error=%v, want zero/changed", retried, retryErr)
					}
					if retryBase.openFileCalls != 0 || retryBase.renameCalls != 0 ||
						retryBase.removeCalls != 0 || retryBase.chmodCalls != 0 {
						t.Fatalf("closed retry mutated open=%d rename=%d remove=%d chmod=%d",
							retryBase.openFileCalls, retryBase.renameCalls,
							retryBase.removeCalls, retryBase.chmodCalls)
					}
					return
				}
				want := recoveryOverwriteWriteResultForTest(t, testCase)
				if retryErr != nil || retried != want {
					t.Fatalf("resumed result=%+v error=%v, want %+v", retried, retryErr, want)
				}
				assertRecoveryOverwriteAcknowledgedRemoteStateForTest(t, testCase)
			})
		}
	})

	t.Run("concurrent final before publish is preserved", func(t *testing.T) {
		testCase := newRecoverySFTPOverwritePrepareCaseForTest(t, []byte("concurrent-post"))
		installRecoveryOverwriteCapturedTupleForTest(t, testCase)
		useOwnedRecoveryOverwriteEntriesForTest(testCase)
		concurrent := []byte("concurrent-final-winner")
		testCase.client.rename = func(oldName string, newName string) error {
			if oldName == testCase.postPath && newName == testCase.finalPath {
				if err := os.WriteFile(testCase.finalPath, concurrent, 0o640); err != nil {
					t.Fatalf("install concurrent final: %v", err)
				}
				return os.ErrExist
			}
			return testCase.base.Rename(oldName, newName)
		}
		if result, err := testCase.writeResult(); result != (TargetWriteResult{}) ||
			err != ErrRecoveryTargetUnavailable {
			t.Fatalf("concurrent publish result=%+v error=%v, want zero/unavailable", result, err)
		}
		retried, retryBase, retryErr := retryRecoveryOverwriteWithFreshTargetForTest(testCase)
		if retried != (TargetWriteResult{}) || retryErr != ErrRecoveryTargetChanged {
			t.Fatalf("concurrent retry result=%+v error=%v, want zero/changed", retried, retryErr)
		}
		preserved, err := os.ReadFile(testCase.finalPath)
		if err != nil || !bytes.Equal(preserved, concurrent) {
			t.Fatalf("concurrent final=%q error=%v, want preserved", preserved, err)
		}
		if retryBase.openFileCalls != 0 || retryBase.renameCalls != 0 || retryBase.removeCalls != 0 {
			t.Fatalf("concurrent retry mutations open=%d rename=%d remove=%d, want zero",
				retryBase.openFileCalls, retryBase.renameCalls, retryBase.removeCalls)
		}
	})

	t.Run("regular fact final canonical drift is rejected", func(t *testing.T) {
		testCase := newRecoverySFTPOverwritePrepareCaseForTest(t, []byte("stable-post"))
		testCase.client.lstat = func(value string, call int) (os.FileInfo, error) {
			if value == testCase.finalPath && call == 5 {
				if err := os.WriteFile(value, []byte("late-regular-drift"), 0o640); err != nil {
					t.Fatalf("install late regular drift: %v", err)
				}
			}
			info, err := testCase.base.Lstat(value)
			if err == nil && (value == testCase.finalPath || value == testCase.priorPath) {
				return recoveryOwnedFileInfoForOverwriteTest(info), nil
			}
			return info, err
		}
		priorDigest := sha256.Sum256(testCase.priorPayload)
		_, err := observeRecoveryOverwriteRegularFacts(
			testCase.client,
			testCase.finalPath,
			PresentExpectation{
				IdentityDigest: hex.EncodeToString(priorDigest[:]),
				Bytes:          int64(len(testCase.priorPayload)),
			},
			PresentExpectation{
				IdentityDigest: testCase.request.ExpectedDigest,
				Bytes:          testCase.request.ExpectedBytes,
			},
			true,
			true,
		)
		if err != ErrRecoveryTargetChanged {
			t.Fatalf("late regular drift error=%v, want exact changed", err)
		}
	})

	t.Run("marker fact final canonical drift is rejected", func(t *testing.T) {
		testCase := newRecoverySFTPOverwritePrepareCaseForTest(t, []byte("marker-post"))
		if err := os.WriteFile(
			testCase.intentPath, []byte(testCase.artifacts.intentDocument), 0o600,
		); err != nil {
			t.Fatalf("install exact marker: %v", err)
		}
		testCase.client.lstat = func(value string, call int) (os.FileInfo, error) {
			if value == testCase.intentPath && call == 5 {
				if err := os.WriteFile(value, []byte("late-marker-drift"), 0o600); err != nil {
					t.Fatalf("install late marker drift: %v", err)
				}
			}
			return testCase.base.Lstat(value)
		}
		_, err := observeRecoveryOverwriteMarkerFacts(
			testCase.client, testCase.intentPath, testCase.artifacts.intentDocument,
		)
		if err != ErrRecoveryTargetChanged {
			t.Fatalf("late marker drift error=%v, want exact changed", err)
		}
	})
}

func TestRecoverySFTPTargetOverwriteCapturesExactPriorWithoutReplacement(t *testing.T) {
	postPayload := bytes.Repeat([]byte("prepared-post:"), 4097)
	testCase := newRecoverySFTPOverwritePrepareCaseForTest(t, postPayload)
	installRecoveryOverwritePreparedTupleForTest(t, testCase)
	useOwnedRecoveryOverwriteEntriesForTest(testCase)
	testCase.client.rename = func(oldName string, newName string) error {
		if oldName == testCase.postPath && newName == testCase.finalPath {
			return errors.New("scripted R44 publish boundary")
		}
		return testCase.base.Rename(oldName, newName)
	}
	sourceReads := 0
	testCase.request.Content = &recoveryReadTrackingReader{read: func([]byte) (int, error) {
		sourceReads++
		return 0, errors.New("prepared capture must not read caller source")
	}}
	testCase.client.open = func(value string) (recoveryTargetSFTPFile, error) {
		if value == testCase.priorPath {
			if _, err := os.Lstat(testCase.finalPath); !os.IsNotExist(err) {
				t.Fatalf("captured prior opened while final error=%v, want exact absence", err)
			}
		}
		return testCase.base.Open(value)
	}

	if err := testCase.write(); err != ErrRecoveryTargetUnavailable {
		t.Fatalf("exact captured prior error=%v, want scripted publish-boundary unavailable", err)
	}
	if sourceReads != 0 {
		t.Fatalf("exact captured prior read caller source %d time(s)", sourceReads)
	}
	if !reflect.DeepEqual(testCase.base.renamePaths, [][2]string{{testCase.finalPath, testCase.priorPath}}) {
		t.Fatalf("capture renames=%v, want one standard final-to-prior rename", testCase.base.renamePaths)
	}
	if _, err := os.Lstat(testCase.finalPath); !os.IsNotExist(err) {
		t.Fatalf("captured final error=%v, want absent at scripted publish boundary", err)
	}
	prior, err := os.ReadFile(testCase.priorPath)
	if err != nil || !bytes.Equal(prior, testCase.priorPayload) {
		t.Fatalf("captured prior=%q error=%v, want exact expected prior", prior, err)
	}
	if testCase.base.maxReadRequest <= 0 || testCase.base.maxReadRequest > recoveryResultReadChunkBytes {
		t.Fatalf("captured prior max read=%d, want bounded full verification", testCase.base.maxReadRequest)
	}
	assertRecoveryOverwritePreparedEvidenceForTest(t, testCase)

	t.Run("live permit is revalidated after captured verification", func(t *testing.T) {
		testCase := newRecoverySFTPOverwritePrepareCaseForTest(t, []byte("prepared-post"))
		installRecoveryOverwritePreparedTupleForTest(t, testCase)
		useOwnedRecoveryOverwriteEntriesForTest(testCase)
		revoked := false
		testCase.permit.permit.proof.validateAt = func(time.Time) error {
			if revoked {
				return errors.New("scripted revoked overwrite authority")
			}
			return nil
		}
		testCase.client.rename = func(oldName, newName string) error {
			err := testCase.base.Rename(oldName, newName)
			if err == nil {
				revoked = true
			}
			return err
		}

		if err := testCase.write(); err != ErrInvalidTargetPermit {
			t.Fatalf("post-capture revocation error=%v, want exact ErrInvalidTargetPermit", err)
		}
		if !reflect.DeepEqual(testCase.base.renamePaths, [][2]string{{testCase.finalPath, testCase.priorPath}}) {
			t.Fatalf("post-capture revocation renames=%v, want capture only", testCase.base.renamePaths)
		}
		if _, err := os.Lstat(testCase.finalPath); !os.IsNotExist(err) {
			t.Fatalf("post-capture revoked final error=%v, want absent", err)
		}
		prior, err := os.ReadFile(testCase.priorPath)
		if err != nil || !bytes.Equal(prior, testCase.priorPayload) {
			t.Fatalf("post-capture revoked prior=%q error=%v, want exact preserved prior", prior, err)
		}
		assertRecoveryOverwritePreparedEvidenceForTest(t, testCase)
	})
}

func TestRecoverySFTPTargetOverwriteRestoresCapturedMismatch(t *testing.T) {
	type mismatchCase struct {
		name    string
		install func(*testing.T, string)
		assert  func(*testing.T, string)
	}
	regularPayload := []byte("raced-regular-winner")
	linkTarget := "raced-relative-target"
	cases := []mismatchCase{
		{
			name: "regular",
			install: func(t *testing.T, value string) {
				t.Helper()
				if err := os.WriteFile(value, regularPayload, 0o640); err != nil {
					t.Fatalf("install raced regular winner: %v", err)
				}
			},
			assert: func(t *testing.T, value string) {
				t.Helper()
				payload, err := os.ReadFile(value)
				if err != nil || !bytes.Equal(payload, regularPayload) {
					t.Fatalf("restored regular=%q error=%v, want raced winner", payload, err)
				}
			},
		},
		{
			name: "directory",
			install: func(t *testing.T, value string) {
				t.Helper()
				if err := os.Mkdir(value, 0o750); err != nil {
					t.Fatalf("install raced directory winner: %v", err)
				}
			},
			assert: func(t *testing.T, value string) {
				t.Helper()
				info, err := os.Lstat(value)
				if err != nil || !info.IsDir() {
					t.Fatalf("restored directory info=%v error=%v", info, err)
				}
			},
		},
		{
			name: "symlink",
			install: func(t *testing.T, value string) {
				t.Helper()
				if err := os.Symlink(linkTarget, value); err != nil {
					t.Fatalf("install raced symlink winner: %v", err)
				}
			},
			assert: func(t *testing.T, value string) {
				t.Helper()
				got, err := os.Readlink(value)
				if err != nil || got != linkTarget {
					t.Fatalf("restored symlink=%q error=%v, want %q", got, err, linkTarget)
				}
			},
		},
		{
			name: "special",
			install: func(t *testing.T, value string) {
				t.Helper()
				if err := syscall.Mkfifo(value, 0o600); err != nil {
					t.Fatalf("install raced fifo winner: %v", err)
				}
			},
			assert: func(t *testing.T, value string) {
				t.Helper()
				info, err := os.Lstat(value)
				if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
					t.Fatalf("restored fifo info=%v error=%v", info, err)
				}
			},
		},
	}

	for _, entry := range cases {
		t.Run(entry.name, func(t *testing.T) {
			testCase := newRecoverySFTPOverwritePrepareCaseForTest(t, []byte("prepared-post"))
			installRecoveryOverwritePreparedTupleForTest(t, testCase)
			useOwnedRecoveryOverwriteEntriesForTest(testCase)
			testCase.client.rename = func(oldName, newName string) error {
				if oldName == testCase.finalPath && newName == testCase.priorPath {
					if err := os.Remove(testCase.finalPath); err != nil {
						t.Fatalf("remove prevalidated prior before race: %v", err)
					}
					entry.install(t, testCase.finalPath)
				}
				return testCase.base.Rename(oldName, newName)
			}

			if err := testCase.write(); err != ErrRecoveryTargetChanged {
				t.Fatalf("captured %s mismatch error=%v, want exact ErrRecoveryTargetChanged", entry.name, err)
			}
			wantRenames := [][2]string{
				{testCase.finalPath, testCase.priorPath},
				{testCase.priorPath, testCase.finalPath},
			}
			if !reflect.DeepEqual(testCase.base.renamePaths, wantRenames) {
				t.Fatalf("captured %s mismatch renames=%v, want capture then no-overwrite restore",
					entry.name, testCase.base.renamePaths)
			}
			entry.assert(t, testCase.finalPath)
			if _, err := os.Lstat(testCase.priorPath); !os.IsNotExist(err) {
				t.Fatalf("restored %s prior error=%v, want absent", entry.name, err)
			}
			assertRecoveryOverwritePreparedEvidenceForTest(t, testCase)
		})
	}

	t.Run("ambiguous capture is never inferred or restored", func(t *testing.T) {
		testCase := newRecoverySFTPOverwritePrepareCaseForTest(t, []byte("prepared-post"))
		installRecoveryOverwritePreparedTupleForTest(t, testCase)
		useOwnedRecoveryOverwriteEntriesForTest(testCase)
		rawFailure := errors.New("RAW_AMBIGUOUS_CAPTURE_FOR_TEST_ONLY")
		testCase.client.rename = func(oldName, newName string) error {
			if err := testCase.base.Rename(oldName, newName); err != nil {
				return err
			}
			return rawFailure
		}

		if err := testCase.write(); err != ErrRecoveryTargetUnavailable {
			t.Fatalf("ambiguous capture error=%v, want exact sanitized unavailable", err)
		}
		if !reflect.DeepEqual(testCase.base.renamePaths, [][2]string{{testCase.finalPath, testCase.priorPath}}) {
			t.Fatalf("ambiguous capture renames=%v, want no inferred restore", testCase.base.renamePaths)
		}
		if _, err := os.Lstat(testCase.finalPath); !os.IsNotExist(err) {
			t.Fatalf("ambiguous capture final error=%v, want observed state left untouched", err)
		}
		prior, err := os.ReadFile(testCase.priorPath)
		if err != nil || !bytes.Equal(prior, testCase.priorPayload) {
			t.Fatalf("ambiguous capture prior=%q error=%v, want evidence preserved", prior, err)
		}
		assertRecoveryOverwritePreparedEvidenceForTest(t, testCase)
	})

	for _, failure := range []struct {
		name string
		err  error
	}{
		{name: "capture permission failure", err: os.ErrPermission},
		{name: "capture unsupported failure", err: errors.New("RAW_CAPTURE_UNSUPPORTED_FOR_TEST_ONLY")},
	} {
		t.Run(failure.name, func(t *testing.T) {
			testCase := newRecoverySFTPOverwritePrepareCaseForTest(t, []byte("prepared-post"))
			installRecoveryOverwritePreparedTupleForTest(t, testCase)
			useOwnedRecoveryOverwriteEntriesForTest(testCase)
			testCase.client.rename = func(oldName, newName string) error {
				testCase.base.renameCalls++
				testCase.base.renamePaths = append(testCase.base.renamePaths, [2]string{oldName, newName})
				return failure.err
			}

			if err := testCase.write(); err != ErrRecoveryTargetUnavailable {
				t.Fatalf("capture dependency error=%v, want exact sanitized unavailable", err)
			}
			if !reflect.DeepEqual(testCase.base.renamePaths, [][2]string{{testCase.finalPath, testCase.priorPath}}) {
				t.Fatalf("capture dependency renames=%v, want one failed attempt", testCase.base.renamePaths)
			}
			final, err := os.ReadFile(testCase.finalPath)
			if err != nil || !bytes.Equal(final, testCase.priorPayload) {
				t.Fatalf("capture dependency final=%q error=%v, want exact prior preserved", final, err)
			}
			if _, err := os.Lstat(testCase.priorPath); !os.IsNotExist(err) {
				t.Fatalf("capture dependency prior error=%v, want absent", err)
			}
			assertRecoveryOverwritePreparedEvidenceForTest(t, testCase)
		})
	}

	t.Run("re-entry with mismatched prior never gains restore authority", func(t *testing.T) {
		testCase := newRecoverySFTPOverwritePrepareCaseForTest(t, []byte("prepared-post"))
		installRecoveryOverwritePreparedTupleForTest(t, testCase)
		if err := os.Rename(testCase.finalPath, testCase.priorPath); err != nil {
			t.Fatalf("install re-entry captured state: %v", err)
		}
		mismatch := []byte("mismatched-prior-from-earlier-invocation")
		if err := os.WriteFile(testCase.priorPath, mismatch, 0o640); err != nil {
			t.Fatalf("install re-entry mismatched prior: %v", err)
		}
		useOwnedRecoveryOverwriteEntriesForTest(testCase)
		testCase.client.rename = func(string, string) error {
			t.Fatal("re-entry mismatched prior must not rename")
			return nil
		}

		if err := testCase.write(); err != ErrRecoveryTargetChanged {
			t.Fatalf("re-entry mismatch error=%v, want exact ErrRecoveryTargetChanged", err)
		}
		if testCase.base.renameCalls != 0 {
			t.Fatalf("re-entry mismatch renames=%v, want none", testCase.base.renamePaths)
		}
		prior, err := os.ReadFile(testCase.priorPath)
		if err != nil || !bytes.Equal(prior, mismatch) {
			t.Fatalf("re-entry mismatch prior=%q error=%v, want preserved", prior, err)
		}
		if _, err := os.Lstat(testCase.finalPath); !os.IsNotExist(err) {
			t.Fatalf("re-entry mismatch final error=%v, want unchanged absence", err)
		}
		assertRecoveryOverwritePreparedEvidenceForTest(t, testCase)
	})

	t.Run("external final occupation prevents restore", func(t *testing.T) {
		testCase := newRecoverySFTPOverwritePrepareCaseForTest(t, []byte("prepared-post"))
		installRecoveryOverwritePreparedTupleForTest(t, testCase)
		mismatch := []byte("captured-mismatch-before-external-winner")
		external := []byte("external-final-winner")
		captured := false
		finalAbsenceChecks := 0
		testCase.client.rename = func(oldName, newName string) error {
			if oldName == testCase.finalPath {
				if err := os.Remove(testCase.finalPath); err != nil {
					t.Fatalf("remove prevalidated prior before external race: %v", err)
				}
				if err := os.WriteFile(testCase.finalPath, mismatch, 0o640); err != nil {
					t.Fatalf("install captured mismatch before external race: %v", err)
				}
			}
			err := testCase.base.Rename(oldName, newName)
			if err == nil && oldName == testCase.finalPath {
				captured = true
			}
			return err
		}
		testCase.client.lstat = func(value string, _ int) (os.FileInfo, error) {
			if captured && value == testCase.finalPath {
				finalAbsenceChecks++
				if finalAbsenceChecks == 3 {
					if err := os.WriteFile(value, external, 0o600); err != nil {
						t.Fatalf("install external final winner: %v", err)
					}
				}
			}
			info, err := testCase.base.Lstat(value)
			if err == nil && (value == testCase.finalPath || value == testCase.priorPath) {
				return recoveryOwnedFileInfoForOverwriteTest(info), nil
			}
			return info, err
		}

		if err := testCase.write(); err != ErrRecoveryTargetChanged {
			t.Fatalf("external final occupation error=%v, want exact ErrRecoveryTargetChanged", err)
		}
		if !reflect.DeepEqual(testCase.base.renamePaths, [][2]string{{testCase.finalPath, testCase.priorPath}}) {
			t.Fatalf("external final occupation renames=%v, want capture only", testCase.base.renamePaths)
		}
		final, finalErr := os.ReadFile(testCase.finalPath)
		prior, priorErr := os.ReadFile(testCase.priorPath)
		if finalErr != nil || !bytes.Equal(final, external) || priorErr != nil || !bytes.Equal(prior, mismatch) {
			t.Fatalf("external conflict final=%q/%v prior=%q/%v, want both winners preserved",
				final, finalErr, prior, priorErr)
		}
		assertRecoveryOverwritePreparedEvidenceForTest(t, testCase)
	})

	t.Run("authenticated evidence drift prevents restore", func(t *testing.T) {
		testCase := newRecoverySFTPOverwritePrepareCaseForTest(t, []byte("prepared-post"))
		installRecoveryOverwritePreparedTupleForTest(t, testCase)
		useOwnedRecoveryOverwriteEntriesForTest(testCase)
		mismatch := []byte("captured-winner-before-evidence-drift")
		tamperedIntent := []byte("tampered-intent-after-capture")
		drifted := false
		testCase.client.rename = func(oldName, newName string) error {
			if oldName == testCase.finalPath {
				if err := os.Remove(testCase.finalPath); err != nil {
					t.Fatalf("remove prevalidated prior before evidence drift: %v", err)
				}
				if err := os.WriteFile(testCase.finalPath, mismatch, 0o640); err != nil {
					t.Fatalf("install mismatch before evidence drift: %v", err)
				}
			}
			return testCase.base.Rename(oldName, newName)
		}
		testCase.client.open = func(value string) (recoveryTargetSFTPFile, error) {
			if value == testCase.priorPath && !drifted {
				drifted = true
				if err := os.WriteFile(testCase.intentPath, tamperedIntent, 0o600); err != nil {
					t.Fatalf("tamper authenticated intent after capture: %v", err)
				}
			}
			return testCase.base.Open(value)
		}

		if err := testCase.write(); err != ErrRecoveryTargetChanged {
			t.Fatalf("authenticated evidence drift error=%v, want exact ErrRecoveryTargetChanged", err)
		}
		if !reflect.DeepEqual(testCase.base.renamePaths, [][2]string{{testCase.finalPath, testCase.priorPath}}) {
			t.Fatalf("authenticated evidence drift renames=%v, want capture only", testCase.base.renamePaths)
		}
		if _, err := os.Lstat(testCase.finalPath); !os.IsNotExist(err) {
			t.Fatalf("authenticated evidence drift final error=%v, want absent", err)
		}
		prior, priorErr := os.ReadFile(testCase.priorPath)
		intent, intentErr := os.ReadFile(testCase.intentPath)
		if priorErr != nil || !bytes.Equal(prior, mismatch) ||
			intentErr != nil || !bytes.Equal(intent, tamperedIntent) {
			t.Fatalf("evidence drift prior=%q/%v intent=%q/%v, want all observed evidence preserved",
				prior, priorErr, intent, intentErr)
		}
		if testCase.base.removeCalls != 0 {
			t.Fatalf("authenticated evidence drift removals=%v, want none", testCase.base.removePaths)
		}
	})

	t.Run("captured entry late drift prevents restore", func(t *testing.T) {
		testCase := newRecoverySFTPOverwritePrepareCaseForTest(t, []byte("prepared-post"))
		installRecoveryOverwritePreparedTupleForTest(t, testCase)
		useOwnedRecoveryOverwriteEntriesForTest(testCase)
		mismatch := []byte("captured-winner-before-late-drift")
		captured := false
		drifted := false
		testCase.client.rename = func(oldName, newName string) error {
			if oldName == testCase.finalPath {
				if err := os.Remove(testCase.finalPath); err != nil {
					t.Fatalf("remove prevalidated prior before late drift: %v", err)
				}
				if err := os.WriteFile(testCase.finalPath, mismatch, 0o640); err != nil {
					t.Fatalf("install mismatch before late drift: %v", err)
				}
			}
			err := testCase.base.Rename(oldName, newName)
			if err == nil && oldName == testCase.finalPath {
				captured = true
			}
			return err
		}
		testCase.client.open = func(value string) (recoveryTargetSFTPFile, error) {
			file, err := testCase.base.Open(value)
			if err == nil && captured && value == testCase.intentPath && !drifted {
				drifted = true
				if writeErr := os.WriteFile(
					testCase.priorPath, []byte("captured-winner-after-late-drift"), 0o640,
				); writeErr != nil {
					t.Fatalf("drift captured prior after initial observation: %v", writeErr)
				}
			}
			return file, err
		}

		if err := testCase.write(); err != ErrRecoveryTargetChanged {
			t.Fatalf("captured late drift error=%v, want exact ErrRecoveryTargetChanged", err)
		}
		if !reflect.DeepEqual(testCase.base.renamePaths, [][2]string{{testCase.finalPath, testCase.priorPath}}) {
			t.Fatalf("captured late drift renames=%v, want capture only", testCase.base.renamePaths)
		}
		if _, err := os.Lstat(testCase.finalPath); !os.IsNotExist(err) {
			t.Fatalf("captured late drift final error=%v, want absent", err)
		}
		assertRecoveryOverwritePreparedEvidenceForTest(t, testCase)
	})

	for _, failure := range []struct {
		name       string
		wantErr    error
		faultCall  int
		fault      func(*testing.T, string) error
		priorAlive bool
	}{
		{
			name: "captured drift", wantErr: ErrRecoveryTargetChanged, faultCall: 2, priorAlive: true,
			fault: func(t *testing.T, value string) error {
				t.Helper()
				return os.WriteFile(value, []byte("drifted-captured-winner"), 0o640)
			},
		},
		{
			name: "captured disappearance", wantErr: ErrRecoveryTargetChanged, faultCall: 2,
			fault: func(t *testing.T, value string) error {
				t.Helper()
				return os.Remove(value)
			},
		},
		{
			name: "captured permission failure", wantErr: ErrRecoveryTargetUnavailable, faultCall: 1,
			fault: func(*testing.T, string) error { return os.ErrPermission }, priorAlive: true,
		},
		{
			name: "captured unsupported failure", wantErr: ErrRecoveryTargetUnavailable, faultCall: 1,
			fault: func(*testing.T, string) error {
				return errors.New("RAW_CAPTURE_OBSERVE_UNSUPPORTED_FOR_TEST_ONLY")
			}, priorAlive: true,
		},
	} {
		t.Run(failure.name, func(t *testing.T) {
			testCase := newRecoverySFTPOverwritePrepareCaseForTest(t, []byte("prepared-post"))
			installRecoveryOverwritePreparedTupleForTest(t, testCase)
			mismatch := []byte("captured-winner-before-observation-fault")
			captured := false
			priorObservations := 0
			testCase.client.rename = func(oldName, newName string) error {
				if oldName == testCase.finalPath {
					if err := os.Remove(testCase.finalPath); err != nil {
						t.Fatalf("remove prevalidated prior before observation fault: %v", err)
					}
					if err := os.WriteFile(testCase.finalPath, mismatch, 0o640); err != nil {
						t.Fatalf("install mismatch before observation fault: %v", err)
					}
				}
				err := testCase.base.Rename(oldName, newName)
				if err == nil && oldName == testCase.finalPath {
					captured = true
				}
				return err
			}
			testCase.client.lstat = func(value string, _ int) (os.FileInfo, error) {
				if captured && value == testCase.priorPath {
					priorObservations++
					if priorObservations == failure.faultCall {
						if faultErr := failure.fault(t, value); faultErr != nil {
							return nil, faultErr
						}
					}
				}
				info, err := testCase.base.Lstat(value)
				if err == nil && (value == testCase.finalPath || value == testCase.priorPath) {
					return recoveryOwnedFileInfoForOverwriteTest(info), nil
				}
				return info, err
			}

			if err := testCase.write(); err != failure.wantErr {
				t.Fatalf("captured observation fault error=%v, want exact %v", err, failure.wantErr)
			}
			if !reflect.DeepEqual(testCase.base.renamePaths, [][2]string{{testCase.finalPath, testCase.priorPath}}) {
				t.Fatalf("captured observation fault renames=%v, want no restore", testCase.base.renamePaths)
			}
			if _, err := os.Lstat(testCase.finalPath); !os.IsNotExist(err) {
				t.Fatalf("captured observation fault final error=%v, want absent", err)
			}
			_, priorErr := os.Lstat(testCase.priorPath)
			if failure.priorAlive && priorErr != nil {
				t.Fatalf("captured observation fault prior error=%v, want preserved", priorErr)
			}
			if !failure.priorAlive && !os.IsNotExist(priorErr) {
				t.Fatalf("captured disappearance prior error=%v, want absent", priorErr)
			}
			assertRecoveryOverwritePreparedEvidenceForTest(t, testCase)
		})
	}

	for _, failure := range []struct {
		name      string
		ambiguous bool
	}{
		{name: "restore permission failure"},
		{name: "ambiguous restore", ambiguous: true},
	} {
		t.Run(failure.name, func(t *testing.T) {
			testCase := newRecoverySFTPOverwritePrepareCaseForTest(t, []byte("prepared-post"))
			installRecoveryOverwritePreparedTupleForTest(t, testCase)
			useOwnedRecoveryOverwriteEntriesForTest(testCase)
			mismatch := []byte("captured-winner-before-restore-fault")
			testCase.client.rename = func(oldName, newName string) error {
				if oldName == testCase.finalPath {
					if err := os.Remove(testCase.finalPath); err != nil {
						t.Fatalf("remove prevalidated prior before restore fault: %v", err)
					}
					if err := os.WriteFile(testCase.finalPath, mismatch, 0o640); err != nil {
						t.Fatalf("install mismatch before restore fault: %v", err)
					}
					return testCase.base.Rename(oldName, newName)
				}
				if failure.ambiguous {
					if err := testCase.base.Rename(oldName, newName); err != nil {
						return err
					}
					return errors.New("RAW_AMBIGUOUS_RESTORE_FOR_TEST_ONLY")
				}
				testCase.base.renameCalls++
				testCase.base.renamePaths = append(testCase.base.renamePaths, [2]string{oldName, newName})
				return os.ErrPermission
			}

			if err := testCase.write(); err != ErrRecoveryTargetUnavailable {
				t.Fatalf("restore fault error=%v, want exact sanitized unavailable", err)
			}
			wantRenames := [][2]string{
				{testCase.finalPath, testCase.priorPath},
				{testCase.priorPath, testCase.finalPath},
			}
			if !reflect.DeepEqual(testCase.base.renamePaths, wantRenames) {
				t.Fatalf("restore fault renames=%v, want one capture and one restore attempt", testCase.base.renamePaths)
			}
			if failure.ambiguous {
				final, err := os.ReadFile(testCase.finalPath)
				if err != nil || !bytes.Equal(final, mismatch) {
					t.Fatalf("ambiguous restore final=%q error=%v, want untouched observed result", final, err)
				}
				if _, err := os.Lstat(testCase.priorPath); !os.IsNotExist(err) {
					t.Fatalf("ambiguous restore prior error=%v, want observed state left untouched", err)
				}
			} else {
				if _, err := os.Lstat(testCase.finalPath); !os.IsNotExist(err) {
					t.Fatalf("failed restore final error=%v, want absent", err)
				}
				prior, err := os.ReadFile(testCase.priorPath)
				if err != nil || !bytes.Equal(prior, mismatch) {
					t.Fatalf("failed restore prior=%q error=%v, want captured winner preserved", prior, err)
				}
			}
			assertRecoveryOverwritePreparedEvidenceForTest(t, testCase)
		})
	}

	t.Run("restored entry verification dependency failure is closed", func(t *testing.T) {
		testCase := newRecoverySFTPOverwritePrepareCaseForTest(t, []byte("prepared-post"))
		installRecoveryOverwritePreparedTupleForTest(t, testCase)
		mismatch := []byte("captured-winner-before-restore-verification")
		restored := false
		testCase.client.rename = func(oldName, newName string) error {
			if oldName == testCase.finalPath {
				if err := os.Remove(testCase.finalPath); err != nil {
					t.Fatalf("remove prevalidated prior before restore verification: %v", err)
				}
				if err := os.WriteFile(testCase.finalPath, mismatch, 0o640); err != nil {
					t.Fatalf("install mismatch before restore verification: %v", err)
				}
			}
			err := testCase.base.Rename(oldName, newName)
			if err == nil && oldName == testCase.priorPath {
				restored = true
			}
			return err
		}
		testCase.client.lstat = func(value string, _ int) (os.FileInfo, error) {
			if restored && value == testCase.finalPath {
				return nil, os.ErrPermission
			}
			info, err := testCase.base.Lstat(value)
			if err == nil && (value == testCase.finalPath || value == testCase.priorPath) {
				return recoveryOwnedFileInfoForOverwriteTest(info), nil
			}
			return info, err
		}

		if err := testCase.write(); err != ErrRecoveryTargetUnavailable {
			t.Fatalf("restore verification failure error=%v, want exact unavailable", err)
		}
		wantRenames := [][2]string{
			{testCase.finalPath, testCase.priorPath},
			{testCase.priorPath, testCase.finalPath},
		}
		if !reflect.DeepEqual(testCase.base.renamePaths, wantRenames) {
			t.Fatalf("restore verification failure renames=%v, want capture then restore", testCase.base.renamePaths)
		}
		final, err := os.ReadFile(testCase.finalPath)
		if err != nil || !bytes.Equal(final, mismatch) {
			t.Fatalf("restore verification failure final=%q error=%v, want restored winner preserved", final, err)
		}
		if testCase.base.removeCalls != 0 {
			t.Fatalf("restore verification failure removals=%v, want none", testCase.base.removePaths)
		}
	})
}

func TestRecoverySFTPTargetWriteAtomicAdmitsOnlyExactCreate(t *testing.T) {
	fixture := newRecoveryLocalSFTPTargetFixture(t)
	fixture.create(t)
	fixture.resolver.calls = 0
	fixture.dialer.calls = 0
	fixture.clients = nil

	jobID := fixture.writePermit.permit.JobID
	payload := []byte("payload")
	locator := recoveryWorkspaceLocatorDirectory + "/" + jobID + "/item.txt"
	createPermit, createRequest := recoveryItemWriteAuthorityForTest(
		t, fixture.now, fixture.binding, jobID, TargetModeIsolated, locator,
		RecoveryOperationCreate, ExpectedTargetIdentity{Kind: ExpectedTargetAbsent}, payload,
	)
	baseClient := &recoveryLocalSFTPClient{}
	client := &recoveryScriptedSFTPClient{
		base: baseClient,
		openFile: func(value string, flag int) (recoveryTargetSFTPFile, error) {
			baseClient.openFileCalls++
			baseClient.openFilePaths = append(baseClient.openFilePaths, value)
			baseClient.openFileFlags = append(baseClient.openFileFlags, flag)
			return nil, errors.New("scripted item temp open failure")
		},
	}
	target := fixture.targetWithClient(client)
	createEntropy := bytes.NewReader(bytes.Repeat([]byte{0x5a}, 32))
	target.entropy = createEntropy
	target.now = func() time.Time { return fixture.now }

	if _, err := target.WriteAtomic(
		context.Background(), createPermit, createRequest,
	); err != ErrRecoveryTargetUnavailable {
		t.Fatalf("exact create partial-boundary error=%v, want exact ErrRecoveryTargetUnavailable", err)
	}
	if createEntropy.Len() != 0 {
		t.Fatalf("exact create entropy remaining=%d, want exact 32-byte read", createEntropy.Len())
	}
	if fixture.resolver.calls != 1 || fixture.resolver.nodeID != fixture.binding.NodeID ||
		fixture.resolver.purpose != TargetPurposeWrite || fixture.dialer.calls != 1 ||
		fixture.dialer.node.ID != fixture.binding.NodeID ||
		fixture.dialer.purpose != sshutil.PurposeRecoveryWrite ||
		fixture.dialer.audit.CorrelationID != jobID || fixture.dialer.audit.Action != "" {
		t.Fatalf("create resolver=%+v dialer=%+v, want exact write purpose and safe job correlation",
			fixture.resolver, fixture.dialer)
	}
	if fixture.resolver.result.NodeRevision != fixture.binding.NodeRevision ||
		fixture.resolver.result.CredentialRevision != fixture.binding.CredentialRevision {
		t.Fatalf("create resolver result=%+v, want exact locked revisions", fixture.resolver.result)
	}

	fixture.resolver.calls = 0
	fixture.dialer.calls = 0
	overwritePermit, overwriteRequest := recoveryItemWriteAuthorityForTest(
		t, fixture.now, fixture.binding, jobID, TargetModeIsolated, locator,
		RecoveryOperationOverwrite,
		ExpectedTargetIdentity{Kind: ExpectedTargetPresent, Digest: strings.Repeat("b", sha256DigestLength)},
		payload,
	)
	overwriteClient := &recoveryLocalSFTPClient{}
	overwriteTarget := fixture.targetWithClient(overwriteClient)
	overwriteEntropy := bytes.NewReader(bytes.Repeat([]byte{0x5a}, 32))
	overwriteTarget.entropy = overwriteEntropy
	overwriteTarget.now = func() time.Time { return fixture.now }
	if _, err := overwriteTarget.WriteAtomic(
		context.Background(), overwritePermit, overwriteRequest,
	); err != ErrRecoveryTargetUnavailable {
		t.Fatalf("valid overwrite error=%v, want exact ErrRecoveryTargetUnavailable", err)
	}
	if overwriteEntropy.Len() != 32 || fixture.resolver.calls != 0 || fixture.dialer.calls != 0 ||
		recoveryLocalSFTPCallCountForTest(overwriteClient) != 0 {
		t.Fatalf("valid overwrite consumed entropy/session: remaining=%d resolver=%d dialer=%d sftp=%d",
			overwriteEntropy.Len(), fixture.resolver.calls, fixture.dialer.calls,
			recoveryLocalSFTPCallCountForTest(overwriteClient))
	}

	invalidCases := []struct {
		name          string
		mutatePermit  func(*TargetWritePermit)
		mutateRequest func(*TargetWriteAtomicRequest)
		now           time.Time
	}{
		{
			name: "nil content",
			mutateRequest: func(value *TargetWriteAtomicRequest) {
				value.Content = nil
			},
		},
		{
			name: "request object substitution",
			mutateRequest: func(value *TargetWriteAtomicRequest) {
				value.Object.PrivateRelativeLocator += ".substituted"
				value.Object.TargetPathDigest = mustTargetPathDigest(
					t, value.Object.RootID, value.Object.RootLocatorDigest,
					value.Object.PrivateRelativeLocator,
				)
			},
		},
		{
			name: "request digest substitution",
			mutateRequest: func(value *TargetWriteAtomicRequest) {
				value.ExpectedDigest = strings.Repeat("9", sha256DigestLength)
			},
		},
		{
			name:          "request bytes substitution",
			mutateRequest: func(value *TargetWriteAtomicRequest) { value.ExpectedBytes++ },
		},
		{
			name: "missing proof",
			mutatePermit: func(value *TargetWritePermit) {
				value.itemProof = nil
			},
		},
		{
			name: "forged proof",
			mutatePermit: func(value *TargetWritePermit) {
				value.itemProof.bindingDigest = strings.Repeat("f", sha256DigestLength)
			},
		},
		{name: "expired authority", now: fixture.now.Add(2 * time.Minute)},
	}
	for _, testCase := range invalidCases {
		t.Run(testCase.name, func(t *testing.T) {
			fixture.resolver.calls = 0
			fixture.dialer.calls = 0
			permit := cloneTargetWritePermitForTest(createPermit)
			request := createRequest
			request.Content = bytes.NewReader(payload)
			if testCase.mutatePermit != nil {
				testCase.mutatePermit(&permit)
			}
			if testCase.mutateRequest != nil {
				testCase.mutateRequest(&request)
			}
			client := &recoveryLocalSFTPClient{}
			target := fixture.targetWithClient(client)
			entropy := bytes.NewReader(bytes.Repeat([]byte{0x5a}, 32))
			target.entropy = entropy
			now := fixture.now
			if !testCase.now.IsZero() {
				now = testCase.now
			}
			target.now = func() time.Time { return now }
			if _, err := target.WriteAtomic(
				context.Background(), permit, request,
			); err != ErrInvalidTargetPermit {
				t.Fatalf("invalid write error=%v, want exact ErrInvalidTargetPermit", err)
			}
			if entropy.Len() != 32 || fixture.resolver.calls != 0 || fixture.dialer.calls != 0 ||
				recoveryLocalSFTPCallCountForTest(client) != 0 {
				t.Fatalf("invalid write consumed entropy/session: remaining=%d resolver=%d dialer=%d sftp=%d",
					entropy.Len(), fixture.resolver.calls, fixture.dialer.calls,
					recoveryLocalSFTPCallCountForTest(client))
			}
		})
	}
}

type recoverySFTPParentWriteCaseForTest struct {
	fixture   *recoveryLocalSFTPTargetFixture
	base      *recoveryLocalSFTPClient
	client    *recoveryScriptedSFTPClient
	target    *recoverySFTPTarget
	permit    TargetWritePermit
	request   TargetWriteAtomicRequest
	finalPath string
}

func newRecoverySFTPParentWriteCaseForTest(
	t *testing.T,
	mode TargetMode,
	itemLocator string,
) *recoverySFTPParentWriteCaseForTest {
	t.Helper()
	fixture := newRecoveryLocalSFTPTargetFixture(t)
	jobID := fixture.writePermit.permit.JobID
	privateRelativeLocator := itemLocator
	if mode == TargetModeIsolated {
		fixture.create(t)
		privateRelativeLocator = recoveryWorkspaceLocatorDirectory + "/" + jobID + "/" + itemLocator
	}
	fixture.resolver.calls = 0
	fixture.dialer.calls = 0
	fixture.clients = nil
	base := &recoveryLocalSFTPClient{}
	client := &recoveryScriptedSFTPClient{base: base}
	client.openFile = func(value string, flag int) (recoveryTargetSFTPFile, error) {
		base.openFileCalls++
		base.openFilePaths = append(base.openFilePaths, value)
		base.openFileFlags = append(base.openFileFlags, flag)
		return nil, errors.New("scripted parent-boundary temp open failure")
	}
	target := fixture.targetWithClient(client)
	target.entropy = bytes.NewReader(bytes.Repeat([]byte{0x5a}, 32))
	target.now = func() time.Time { return fixture.now }
	permit, request := recoveryItemWriteAuthorityForTest(
		t, fixture.now, fixture.binding, jobID, mode, privateRelativeLocator,
		RecoveryOperationCreate, ExpectedTargetIdentity{Kind: ExpectedTargetAbsent}, []byte("payload"),
	)
	return &recoverySFTPParentWriteCaseForTest{
		fixture: fixture, base: base, client: client, target: target,
		permit: permit, request: request,
		finalPath: filepath.Join(fixture.root, filepath.FromSlash(privateRelativeLocator)),
	}
}

func (testCase *recoverySFTPParentWriteCaseForTest) write() error {
	_, err := testCase.target.WriteAtomic(
		context.Background(), testCase.permit, testCase.request,
	)
	return err
}

func assertRecoveryWriteFinalAbsentForTest(t *testing.T, value string) {
	t.Helper()
	if _, err := os.Lstat(value); !os.IsNotExist(err) && !errors.Is(err, syscall.ENOTDIR) {
		t.Fatalf("final path %q stat error=%v, want absent", value, err)
	}
}

func assertRecoveryParentNoFileMutationForTest(
	t *testing.T,
	client *recoveryLocalSFTPClient,
) {
	t.Helper()
	if client.mkdirCalls != 0 || client.chmodCalls != 0 || client.openFileCalls != 0 ||
		client.renameCalls != 0 || client.removeCalls != 0 {
		t.Fatalf("unexpected parent/file mutation: mkdir=%d chmod=%d open=%d rename=%d remove=%d",
			client.mkdirCalls, client.chmodCalls, client.openFileCalls,
			client.renameCalls, client.removeCalls)
	}
}

func TestRecoverySFTPTargetWriteAtomicPreparesModeExactParents(t *testing.T) {
	t.Run("isolated top-level item creates no extra parent", func(t *testing.T) {
		testCase := newRecoverySFTPParentWriteCaseForTest(t, TargetModeIsolated, "item.txt")
		if err := testCase.write(); err != ErrRecoveryTargetUnavailable {
			t.Fatalf("top-level write error=%v, want exact ErrRecoveryTargetUnavailable", err)
		}
		if testCase.base.mkdirCalls != 0 || testCase.base.chmodCalls != 0 ||
			testCase.base.openFileCalls > 1 || testCase.base.renameCalls != 0 || testCase.base.removeCalls != 0 {
			t.Fatalf("top-level parent/file calls=%+v, want no parent creation", testCase.base)
		}
		assertRecoveryWriteFinalAbsentForTest(t, testCase.finalPath)
	})

	t.Run("isolated nested parents are created in order at 0700", func(t *testing.T) {
		testCase := newRecoverySFTPParentWriteCaseForTest(t, TargetModeIsolated, "nested/deeper/item.txt")
		if err := testCase.write(); err != ErrRecoveryTargetUnavailable {
			t.Fatalf("nested write error=%v, want exact ErrRecoveryTargetUnavailable", err)
		}
		first := filepath.Dir(filepath.Dir(testCase.finalPath))
		second := filepath.Dir(testCase.finalPath)
		if !reflect.DeepEqual(testCase.base.mkdirPaths, []string{first, second}) ||
			!reflect.DeepEqual(testCase.base.chmodPaths, []string{first, second}) ||
			!reflect.DeepEqual(testCase.base.chmodModes, []os.FileMode{0o700, 0o700}) {
			t.Fatalf("nested mkdir=%v chmod=%v modes=%v, want ordered 0700 parents",
				testCase.base.mkdirPaths, testCase.base.chmodPaths, testCase.base.chmodModes)
		}
		for _, parent := range []string{first, second} {
			info, err := os.Lstat(parent)
			if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
				t.Fatalf("created parent %q info=%v error=%v, want real 0700 directory", parent, info, err)
			}
		}
		assertRecoveryWriteFinalAbsentForTest(t, testCase.finalPath)
	})

	t.Run("isolated canonical existing chain is mutation-free", func(t *testing.T) {
		testCase := newRecoverySFTPParentWriteCaseForTest(t, TargetModeIsolated, "nested/deeper/item.txt")
		deepest := filepath.Dir(testCase.finalPath)
		if err := os.MkdirAll(deepest, 0o700); err != nil {
			t.Fatalf("create canonical existing chain: %v", err)
		}
		first := filepath.Dir(deepest)
		if err := os.Chmod(first, 0o700); err != nil {
			t.Fatalf("chmod first existing parent: %v", err)
		}
		if err := os.Chmod(deepest, 0o700); err != nil {
			t.Fatalf("chmod deepest existing parent: %v", err)
		}
		if err := testCase.write(); err != ErrRecoveryTargetUnavailable {
			t.Fatalf("existing-chain write error=%v, want exact ErrRecoveryTargetUnavailable", err)
		}
		if testCase.base.mkdirCalls != 0 || testCase.base.chmodCalls != 0 ||
			testCase.base.openFileCalls > 1 || testCase.base.renameCalls != 0 || testCase.base.removeCalls != 0 {
			t.Fatalf("existing canonical chain was mutated: %+v", testCase.base)
		}
		assertRecoveryWriteFinalAbsentForTest(t, testCase.finalPath)
	})

	t.Run("isolated lost mkdir race accepts only canonical 0700 winner", func(t *testing.T) {
		t.Run("canonical winner", func(t *testing.T) {
			testCase := newRecoverySFTPParentWriteCaseForTest(t, TargetModeIsolated, "nested/item.txt")
			parent := filepath.Dir(testCase.finalPath)
			testCase.client.mkdir = func(value string) error {
				testCase.base.mkdirCalls++
				testCase.base.mkdirPaths = append(testCase.base.mkdirPaths, value)
				if err := os.Mkdir(value, 0o700); err != nil {
					t.Fatalf("install canonical race winner: %v", err)
				}
				if err := os.Chmod(value, 0o700); err != nil {
					t.Fatalf("chmod canonical race winner: %v", err)
				}
				return os.ErrExist
			}
			if err := testCase.write(); err != ErrRecoveryTargetUnavailable {
				t.Fatalf("canonical race winner error=%v, want exact ErrRecoveryTargetUnavailable", err)
			}
			if !reflect.DeepEqual(testCase.base.mkdirPaths, []string{parent}) || testCase.base.chmodCalls != 0 {
				t.Fatalf("canonical race calls mkdir=%v chmod=%d, want no loser chmod",
					testCase.base.mkdirPaths, testCase.base.chmodCalls)
			}
			assertRecoveryWriteFinalAbsentForTest(t, testCase.finalPath)
		})

		t.Run("wrong-mode winner", func(t *testing.T) {
			testCase := newRecoverySFTPParentWriteCaseForTest(t, TargetModeIsolated, "nested/item.txt")
			testCase.client.mkdir = func(value string) error {
				testCase.base.mkdirCalls++
				testCase.base.mkdirPaths = append(testCase.base.mkdirPaths, value)
				if err := os.Mkdir(value, 0o755); err != nil {
					t.Fatalf("install wrong-mode race winner: %v", err)
				}
				if err := os.Chmod(value, 0o755); err != nil {
					t.Fatalf("chmod wrong-mode race winner: %v", err)
				}
				return os.ErrExist
			}
			if err := testCase.write(); err != ErrRecoveryTargetChanged {
				t.Fatalf("wrong-mode race winner error=%v, want exact ErrRecoveryTargetChanged", err)
			}
			if testCase.base.mkdirCalls != 1 || testCase.base.chmodCalls != 0 || testCase.base.openFileCalls != 0 {
				t.Fatalf("wrong-mode race winner was repaired or opened: %+v", testCase.base)
			}
			assertRecoveryWriteFinalAbsentForTest(t, testCase.finalPath)
		})
	})

	isolatedConflictCases := []struct {
		name      string
		configure func(*testing.T, *recoverySFTPParentWriteCaseForTest, string)
	}{
		{
			name: "wrong mode",
			configure: func(t *testing.T, _ *recoverySFTPParentWriteCaseForTest, parent string) {
				if err := os.Mkdir(parent, 0o755); err != nil {
					t.Fatalf("create wrong-mode parent: %v", err)
				}
				if err := os.Chmod(parent, 0o755); err != nil {
					t.Fatalf("chmod wrong-mode parent: %v", err)
				}
			},
		},
		{
			name: "symlink",
			configure: func(t *testing.T, _ *recoverySFTPParentWriteCaseForTest, parent string) {
				destination := t.TempDir()
				if err := os.Symlink(destination, parent); err != nil {
					t.Fatalf("create parent symlink: %v", err)
				}
			},
		},
		{
			name: "file",
			configure: func(t *testing.T, _ *recoverySFTPParentWriteCaseForTest, parent string) {
				if err := os.WriteFile(parent, []byte("not-a-directory"), 0o600); err != nil {
					t.Fatalf("create file parent: %v", err)
				}
			},
		},
		{
			name: "special",
			configure: func(t *testing.T, _ *recoverySFTPParentWriteCaseForTest, parent string) {
				if err := syscall.Mkfifo(parent, 0o600); err != nil {
					t.Fatalf("create fifo parent: %v", err)
				}
			},
		},
		{
			name: "wrong realpath",
			configure: func(t *testing.T, testCase *recoverySFTPParentWriteCaseForTest, parent string) {
				if err := os.Mkdir(parent, 0o700); err != nil {
					t.Fatalf("create wrong-realpath parent: %v", err)
				}
				testCase.client.realPath = func(value string, _ int) (string, error) {
					canonical, err := testCase.base.RealPath(value)
					if err == nil && value == parent {
						return canonical + "-alias", nil
					}
					return canonical, err
				}
			},
		},
	}
	for _, conflict := range isolatedConflictCases {
		t.Run("isolated existing "+conflict.name+" parent is changed", func(t *testing.T) {
			testCase := newRecoverySFTPParentWriteCaseForTest(t, TargetModeIsolated, "nested/item.txt")
			parent := filepath.Dir(testCase.finalPath)
			conflict.configure(t, testCase, parent)
			if err := testCase.write(); err != ErrRecoveryTargetChanged {
				t.Fatalf("isolated %s parent error=%v, want exact ErrRecoveryTargetChanged", conflict.name, err)
			}
			assertRecoveryParentNoFileMutationForTest(t, testCase.base)
			assertRecoveryWriteFinalAbsentForTest(t, testCase.finalPath)
		})
	}

	t.Run("isolated chmod failure stops later mutation", func(t *testing.T) {
		testCase := newRecoverySFTPParentWriteCaseForTest(t, TargetModeIsolated, "nested/deeper/item.txt")
		first := filepath.Dir(filepath.Dir(testCase.finalPath))
		second := filepath.Dir(testCase.finalPath)
		testCase.client.chmod = func(value string, mode os.FileMode) error {
			testCase.base.chmodCalls++
			testCase.base.chmodPaths = append(testCase.base.chmodPaths, value)
			testCase.base.chmodModes = append(testCase.base.chmodModes, mode)
			return errors.New("scripted parent chmod failure")
		}
		if err := testCase.write(); err != ErrRecoveryTargetUnavailable {
			t.Fatalf("parent chmod error=%v, want exact ErrRecoveryTargetUnavailable", err)
		}
		if !reflect.DeepEqual(testCase.base.mkdirPaths, []string{first}) ||
			!reflect.DeepEqual(testCase.base.chmodPaths, []string{first}) ||
			testCase.base.openFileCalls != 0 || testCase.base.renameCalls != 0 ||
			testCase.base.removeCalls != 0 {
			t.Fatalf("chmod failure continued mutations: mkdir=%v chmod=%v open=%d rename=%d remove=%d",
				testCase.base.mkdirPaths, testCase.base.chmodPaths, testCase.base.openFileCalls,
				testCase.base.renameCalls, testCase.base.removeCalls)
		}
		if _, err := os.Lstat(second); !os.IsNotExist(err) {
			t.Fatalf("later parent %q stat error=%v, want absent", second, err)
		}
		assertRecoveryWriteFinalAbsentForTest(t, testCase.finalPath)
	})

	t.Run("in-place existing arbitrary modes are accepted without repair", func(t *testing.T) {
		testCase := newRecoverySFTPParentWriteCaseForTest(t, TargetModeInPlace, "existing/deeper/item.txt")
		first := filepath.Dir(filepath.Dir(testCase.finalPath))
		second := filepath.Dir(testCase.finalPath)
		if err := os.MkdirAll(second, 0o700); err != nil {
			t.Fatalf("create in-place parents: %v", err)
		}
		if err := os.Chmod(first, 0o750); err != nil {
			t.Fatalf("chmod first in-place parent: %v", err)
		}
		if err := os.Chmod(second, 0o711); err != nil {
			t.Fatalf("chmod second in-place parent: %v", err)
		}
		if err := testCase.write(); err != ErrRecoveryTargetUnavailable {
			t.Fatalf("in-place existing-parent error=%v, want exact ErrRecoveryTargetUnavailable", err)
		}
		if testCase.base.mkdirCalls != 0 || testCase.base.chmodCalls != 0 ||
			testCase.base.openFileCalls > 1 || testCase.base.renameCalls != 0 || testCase.base.removeCalls != 0 {
			t.Fatalf("in-place existing parents were mutated: %+v", testCase.base)
		}
		assertRecoveryWriteFinalAbsentForTest(t, testCase.finalPath)
	})

	t.Run("in-place later mode drift is changed", func(t *testing.T) {
		testCase := newRecoverySFTPParentWriteCaseForTest(t, TargetModeInPlace, "existing/deeper/item.txt")
		first := filepath.Dir(filepath.Dir(testCase.finalPath))
		second := filepath.Dir(testCase.finalPath)
		if err := os.MkdirAll(second, 0o700); err != nil {
			t.Fatalf("create drifting in-place parents: %v", err)
		}
		if err := os.Chmod(first, 0o750); err != nil {
			t.Fatalf("chmod first drifting parent: %v", err)
		}
		if err := os.Chmod(second, 0o711); err != nil {
			t.Fatalf("chmod second drifting parent: %v", err)
		}
		testCase.client.lstat = func(value string, call int) (os.FileInfo, error) {
			info, err := testCase.base.Lstat(value)
			if err != nil || value != second || call < 2 {
				return info, err
			}
			changedMode := (info.Mode() &^ os.ModePerm) | 0o700
			return recoveryFileInfoOverride{FileInfo: info, mode: &changedMode}, nil
		}
		if err := testCase.write(); err != ErrRecoveryTargetChanged {
			t.Fatalf("in-place mode drift error=%v, want exact ErrRecoveryTargetChanged", err)
		}
		assertRecoveryParentNoFileMutationForTest(t, testCase.base)
		assertRecoveryWriteFinalAbsentForTest(t, testCase.finalPath)
	})

	inPlaceConflictCases := []struct {
		name      string
		configure func(*testing.T, *recoverySFTPParentWriteCaseForTest, string)
	}{
		{name: "missing", configure: func(*testing.T, *recoverySFTPParentWriteCaseForTest, string) {}},
		{
			name: "symlink",
			configure: func(t *testing.T, _ *recoverySFTPParentWriteCaseForTest, parent string) {
				if err := os.Symlink(t.TempDir(), parent); err != nil {
					t.Fatalf("create in-place symlink parent: %v", err)
				}
			},
		},
		{
			name: "file",
			configure: func(t *testing.T, _ *recoverySFTPParentWriteCaseForTest, parent string) {
				if err := os.WriteFile(parent, []byte("not-a-directory"), 0o600); err != nil {
					t.Fatalf("create in-place file parent: %v", err)
				}
			},
		},
		{
			name: "special",
			configure: func(t *testing.T, _ *recoverySFTPParentWriteCaseForTest, parent string) {
				if err := syscall.Mkfifo(parent, 0o600); err != nil {
					t.Fatalf("create in-place fifo parent: %v", err)
				}
			},
		},
		{
			name: "wrong realpath",
			configure: func(t *testing.T, testCase *recoverySFTPParentWriteCaseForTest, parent string) {
				if err := os.Mkdir(parent, 0o700); err != nil {
					t.Fatalf("create in-place wrong-realpath parent: %v", err)
				}
				testCase.client.realPath = func(value string, _ int) (string, error) {
					canonical, err := testCase.base.RealPath(value)
					if err == nil && value == parent {
						return canonical + "-alias", nil
					}
					return canonical, err
				}
			},
		},
	}
	for _, conflict := range inPlaceConflictCases {
		t.Run("in-place "+conflict.name+" parent is changed without mutation", func(t *testing.T) {
			testCase := newRecoverySFTPParentWriteCaseForTest(t, TargetModeInPlace, "parent/item.txt")
			parent := filepath.Dir(testCase.finalPath)
			conflict.configure(t, testCase, parent)
			if err := testCase.write(); err != ErrRecoveryTargetChanged {
				t.Fatalf("in-place %s parent error=%v, want exact ErrRecoveryTargetChanged", conflict.name, err)
			}
			assertRecoveryParentNoFileMutationForTest(t, testCase.base)
			assertRecoveryWriteFinalAbsentForTest(t, testCase.finalPath)
		})
	}
}

func TestRecoverySFTPTargetWriteAtomicUsesExclusivePrivateTemp(t *testing.T) {
	wantBasename := recoveryPayloadTempPrefix +
		"WlpaWlpaWlpaWlpaWlpaWlpaWlpaWlpaWlpaWlpaWlo"

	t.Run("exclusive same-directory temp is private and owned", func(t *testing.T) {
		testCase := newRecoverySFTPParentWriteCaseForTest(t, TargetModeIsolated, "item.txt")
		testCase.client.openFile = nil
		testCase.client.rename = func(oldName, newName string) error {
			testCase.base.renameCalls++
			testCase.base.renamePaths = append(testCase.base.renamePaths, [2]string{oldName, newName})
			return errors.New("scripted pre-publication rename failure")
		}
		tempPath := filepath.Join(filepath.Dir(testCase.finalPath), wantBasename)
		if err := testCase.write(); err != ErrRecoveryTargetUnavailable {
			t.Fatalf("exclusive temp write error=%v, want exact ErrRecoveryTargetUnavailable", err)
		}
		if !reflect.DeepEqual(testCase.base.openFilePaths, []string{tempPath}) ||
			!reflect.DeepEqual(testCase.base.openFileFlags, []int{os.O_WRONLY | os.O_CREATE | os.O_EXCL}) {
			t.Fatalf("temp opens=%v flags=%v, want one exact exclusive open",
				testCase.base.openFilePaths, testCase.base.openFileFlags)
		}
		if !reflect.DeepEqual(testCase.base.chmodPaths, []string{tempPath}) ||
			!reflect.DeepEqual(testCase.base.chmodModes, []os.FileMode{0o600}) {
			t.Fatalf("temp chmod paths=%v modes=%v, want exact 0600", testCase.base.chmodPaths, testCase.base.chmodModes)
		}
		if !reflect.DeepEqual(testCase.base.openPaths, []string{tempPath}) ||
			testCase.client.lstatCalls[tempPath] < 2 || testCase.client.realPathCalls[tempPath] < 2 {
			t.Fatalf("temp verification open=%v lstat=%d realpath=%d, want canonical reread",
				testCase.base.openPaths, testCase.client.lstatCalls[tempPath],
				testCase.client.realPathCalls[tempPath])
		}
		for index, opened := range testCase.base.openFilePaths {
			if opened == testCase.finalPath {
				t.Fatalf("final path opened for mutation at index %d", index)
			}
		}
		if testCase.base.syncCalls != 1 ||
			!reflect.DeepEqual(testCase.base.removePaths, []string{tempPath}) {
			t.Fatalf("temp sync=%d removals=%v, want one exact owned-temp cleanup",
				testCase.base.syncCalls, testCase.base.removePaths)
		}
		if testCase.base.renameCalls > 1 {
			t.Fatalf("pre-publication rename calls=%d, want at most one", testCase.base.renameCalls)
		}
		if _, err := os.Lstat(tempPath); !os.IsNotExist(err) {
			t.Fatalf("owned temp stat error=%v, want removed", err)
		}
		assertRecoveryWriteFinalAbsentForTest(t, testCase.finalPath)
	})

	t.Run("entropy failure precedes parent and session mutation", func(t *testing.T) {
		testCase := newRecoverySFTPParentWriteCaseForTest(t, TargetModeIsolated, "nested/item.txt")
		testCase.target.entropy = bytes.NewReader(bytes.Repeat([]byte{0x5a}, recoveryPayloadTempEntropyBytes-1))
		if err := testCase.write(); err != ErrRecoveryTargetUnavailable {
			t.Fatalf("entropy failure error=%v, want exact ErrRecoveryTargetUnavailable", err)
		}
		if testCase.fixture.resolver.calls != 0 || testCase.fixture.dialer.calls != 0 ||
			recoveryLocalSFTPCallCountForTest(testCase.base) != 0 {
			t.Fatalf("entropy failure reached session/mutation: resolver=%d dialer=%d sftp=%d",
				testCase.fixture.resolver.calls, testCase.fixture.dialer.calls,
				recoveryLocalSFTPCallCountForTest(testCase.base))
		}
		assertRecoveryWriteFinalAbsentForTest(t, testCase.finalPath)
	})

	t.Run("pre-existing temp collision is never removed", func(t *testing.T) {
		testCase := newRecoverySFTPParentWriteCaseForTest(t, TargetModeIsolated, "item.txt")
		testCase.client.openFile = nil
		tempPath := filepath.Join(filepath.Dir(testCase.finalPath), wantBasename)
		collision := []byte("pre-existing-temp-owned-by-another-call")
		if err := os.WriteFile(tempPath, collision, 0o600); err != nil {
			t.Fatalf("create colliding temp: %v", err)
		}
		if err := os.Chmod(tempPath, 0o600); err != nil {
			t.Fatalf("chmod colliding temp: %v", err)
		}
		if err := testCase.write(); err != ErrRecoveryTargetUnavailable {
			t.Fatalf("temp collision error=%v, want exact ErrRecoveryTargetUnavailable", err)
		}
		if testCase.base.openFileCalls != 1 || testCase.base.chmodCalls != 0 ||
			testCase.base.removeCalls != 0 || testCase.base.renameCalls != 0 {
			t.Fatalf("temp collision calls open=%d chmod=%d remove=%d rename=%d, want no ownership mutation",
				testCase.base.openFileCalls, testCase.base.chmodCalls,
				testCase.base.removeCalls, testCase.base.renameCalls)
		}
		preserved, err := os.ReadFile(tempPath)
		if err != nil || !bytes.Equal(preserved, collision) {
			t.Fatalf("colliding temp preserved=%q error=%v, want byte-for-byte preservation", preserved, err)
		}
		assertRecoveryWriteFinalAbsentForTest(t, testCase.finalPath)
	})
}

func decorateRecoveryTempWriteFileForTest(
	testCase *recoverySFTPParentWriteCaseForTest,
	decorate func(*recoveryScriptedSFTPFile, recoveryTargetSFTPFile),
) {
	testCase.client.openFile = func(value string, flag int) (recoveryTargetSFTPFile, error) {
		file, err := testCase.base.OpenFile(value, flag)
		if err != nil {
			return nil, err
		}
		scripted := &recoveryScriptedSFTPFile{base: file}
		decorate(scripted, file)
		return scripted, nil
	}
}

func decorateRecoveryTempReadFileForTest(
	testCase *recoverySFTPParentWriteCaseForTest,
	decorate func(*recoveryScriptedSFTPFile, recoveryTargetSFTPFile),
) {
	testCase.client.open = func(value string) (recoveryTargetSFTPFile, error) {
		file, err := testCase.base.Open(value)
		if err != nil {
			return nil, err
		}
		scripted := &recoveryScriptedSFTPFile{base: file}
		decorate(scripted, file)
		return scripted, nil
	}
}

func TestRecoverySFTPTargetWriteAtomicStreamsExactContent(t *testing.T) {
	rawErr := errors.New("raw scripted stream dependency failure")
	ordinaryPayload := bytes.Repeat([]byte{0x3c}, 64*1024+17)
	tests := []struct {
		name            string
		payload         []byte
		content         func([]byte) *recoveryReadTrackingReader
		configure       func(*testing.T, *recoverySFTPParentWriteCaseForTest, string, []byte)
		wantErr         error
		exactStream     bool
		wantReread      bool
		wantOwnedRemove bool
	}{
		{
			name:        "zero-byte exact stream",
			payload:     []byte{},
			exactStream: true, wantReread: true, wantOwnedRemove: true,
		},
		{
			name:        "ordinary exact stream",
			payload:     ordinaryPayload,
			exactStream: true, wantReread: true, wantOwnedRemove: true,
		},
		{
			name:    "short EOF",
			payload: []byte("expected-payload"),
			content: func(payload []byte) *recoveryReadTrackingReader {
				return &recoveryReadTrackingReader{reader: bytes.NewReader(payload[:len(payload)-1])}
			},
			wantOwnedRemove: true,
		},
		{
			name:    "extra byte",
			payload: []byte("expected-payload"),
			content: func(payload []byte) *recoveryReadTrackingReader {
				return &recoveryReadTrackingReader{reader: bytes.NewReader(append(append([]byte{}, payload...), 0x7f))}
			},
			wantOwnedRemove: true,
		},
		{
			name:    "zero nil after expected bytes",
			payload: []byte("expected-payload"),
			content: func(payload []byte) *recoveryReadTrackingReader {
				base := bytes.NewReader(payload)
				return &recoveryReadTrackingReader{read: func(value []byte) (int, error) {
					if base.Len() == 0 {
						return 0, nil
					}
					return base.Read(value)
				}}
			},
			wantOwnedRemove: true,
		},
		{
			name:    "source read error",
			payload: []byte("expected-payload"),
			content: func([]byte) *recoveryReadTrackingReader {
				return &recoveryReadTrackingReader{read: func([]byte) (int, error) { return 0, rawErr }}
			},
			wantOwnedRemove: true,
		},
		{
			name:    "short write",
			payload: []byte("expected-payload"),
			configure: func(_ *testing.T, testCase *recoverySFTPParentWriteCaseForTest, _ string, _ []byte) {
				decorateRecoveryTempWriteFileForTest(testCase, func(scripted *recoveryScriptedSFTPFile, file recoveryTargetSFTPFile) {
					scripted.write = func(value []byte) (int, error) {
						if len(value) <= 1 {
							return 0, nil
						}
						return file.Write(value[:len(value)-1])
					}
				})
			},
			wantOwnedRemove: true,
		},
		{
			name:    "zero write",
			payload: []byte("expected-payload"),
			configure: func(_ *testing.T, testCase *recoverySFTPParentWriteCaseForTest, _ string, _ []byte) {
				decorateRecoveryTempWriteFileForTest(testCase, func(scripted *recoveryScriptedSFTPFile, _ recoveryTargetSFTPFile) {
					scripted.write = func([]byte) (int, error) { return 0, nil }
				})
			},
			wantOwnedRemove: true,
		},
		{
			name:    "source digest drift",
			payload: []byte("expected-payload"),
			content: func(payload []byte) *recoveryReadTrackingReader {
				drifted := append([]byte{}, payload...)
				drifted[0] ^= 0xff
				return &recoveryReadTrackingReader{reader: bytes.NewReader(drifted)}
			},
			wantOwnedRemove: true,
		},
		{
			name:    "sync failure",
			payload: []byte("expected-payload"),
			configure: func(_ *testing.T, testCase *recoverySFTPParentWriteCaseForTest, _ string, _ []byte) {
				decorateRecoveryTempWriteFileForTest(testCase, func(scripted *recoveryScriptedSFTPFile, _ recoveryTargetSFTPFile) {
					scripted.sync = func() error { return rawErr }
				})
			},
			wantOwnedRemove: true,
		},
		{
			name:    "write handle close failure",
			payload: []byte("expected-payload"),
			configure: func(_ *testing.T, testCase *recoverySFTPParentWriteCaseForTest, _ string, _ []byte) {
				decorateRecoveryTempWriteFileForTest(testCase, func(scripted *recoveryScriptedSFTPFile, file recoveryTargetSFTPFile) {
					scripted.close = func() error {
						_ = file.Close()
						return rawErr
					}
				})
			},
			wantOwnedRemove: true,
		},
		{
			name:    "reopen failure",
			payload: []byte("expected-payload"),
			configure: func(_ *testing.T, testCase *recoverySFTPParentWriteCaseForTest, _ string, _ []byte) {
				testCase.client.open = func(value string) (recoveryTargetSFTPFile, error) {
					testCase.base.openCalls++
					testCase.base.openPaths = append(testCase.base.openPaths, value)
					return nil, rawErr
				}
			},
			wantReread: true, wantOwnedRemove: true,
		},
		{
			name:    "reopened stat drift",
			payload: []byte("expected-payload"),
			configure: func(_ *testing.T, testCase *recoverySFTPParentWriteCaseForTest, _ string, _ []byte) {
				decorateRecoveryTempReadFileForTest(testCase, func(scripted *recoveryScriptedSFTPFile, file recoveryTargetSFTPFile) {
					scripted.stat = func() (os.FileInfo, error) {
						info, err := file.Stat()
						if err != nil {
							return nil, err
						}
						size := info.Size() + 1
						return recoveryFileInfoOverride{FileInfo: info, size: &size}, nil
					}
				})
			},
			wantErr: ErrRecoveryTargetChanged, wantReread: true, wantOwnedRemove: true,
		},
		{
			name:    "reread short",
			payload: []byte("expected-payload"),
			configure: func(_ *testing.T, testCase *recoverySFTPParentWriteCaseForTest, _ string, _ []byte) {
				decorateRecoveryTempReadFileForTest(testCase, func(scripted *recoveryScriptedSFTPFile, _ recoveryTargetSFTPFile) {
					scripted.read = func([]byte) (int, error) { return 0, io.EOF }
				})
			},
			wantErr: ErrRecoveryTargetChanged, wantReread: true, wantOwnedRemove: true,
		},
		{
			name:    "reread extra",
			payload: []byte("expected-payload"),
			configure: func(_ *testing.T, testCase *recoverySFTPParentWriteCaseForTest, _ string, payload []byte) {
				decorateRecoveryTempReadFileForTest(testCase, func(scripted *recoveryScriptedSFTPFile, file recoveryTargetSFTPFile) {
					remaining := len(payload)
					scripted.read = func(value []byte) (int, error) {
						if remaining > 0 {
							read, err := file.Read(value)
							remaining -= read
							return read, err
						}
						value[0] = 0x7f
						return 1, nil
					}
				})
			},
			wantErr: ErrRecoveryTargetChanged, wantReread: true, wantOwnedRemove: true,
		},
		{
			name:    "reread digest drift",
			payload: []byte("expected-payload"),
			configure: func(_ *testing.T, testCase *recoverySFTPParentWriteCaseForTest, _ string, payload []byte) {
				decorateRecoveryTempReadFileForTest(testCase, func(scripted *recoveryScriptedSFTPFile, _ recoveryTargetSFTPFile) {
					drifted := append([]byte{}, payload...)
					drifted[0] ^= 0xff
					reader := bytes.NewReader(drifted)
					scripted.read = reader.Read
				})
			},
			wantErr: ErrRecoveryTargetChanged, wantReread: true, wantOwnedRemove: true,
		},
		{
			name:    "temp mode replacement",
			payload: []byte("expected-payload"),
			configure: func(t *testing.T, testCase *recoverySFTPParentWriteCaseForTest, tempPath string, _ []byte) {
				testCase.client.open = func(value string) (recoveryTargetSFTPFile, error) {
					file, err := testCase.base.Open(value)
					if err != nil {
						return nil, err
					}
					if err := os.Chmod(tempPath, 0o640); err != nil {
						t.Fatalf("replace temp mode: %v", err)
					}
					return file, nil
				}
			},
			wantErr: ErrRecoveryTargetChanged, wantReread: true, wantOwnedRemove: true,
		},
		{
			name:    "temp canonical replacement",
			payload: []byte("expected-payload"),
			configure: func(_ *testing.T, testCase *recoverySFTPParentWriteCaseForTest, tempPath string, _ []byte) {
				testCase.client.realPath = func(value string, call int) (string, error) {
					canonical, err := testCase.base.RealPath(value)
					if err == nil && value == tempPath && call >= 4 {
						return canonical + "-alias", nil
					}
					return canonical, err
				}
			},
			wantErr: ErrRecoveryTargetChanged, wantReread: true, wantOwnedRemove: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			testCase := newRecoverySFTPParentWriteCaseForTest(t, TargetModeIsolated, "item.txt")
			testCase.client.openFile = nil
			tempPath := filepath.Join(
				filepath.Dir(testCase.finalPath),
				recoveryPayloadTempPrefix+"WlpaWlpaWlpaWlpaWlpaWlpaWlpaWlpaWlpaWlpaWlo",
			)
			testCase.client.rename = func(oldName, newName string) error {
				testCase.base.renameCalls++
				testCase.base.renamePaths = append(testCase.base.renamePaths, [2]string{oldName, newName})
				return rawErr
			}
			testCase.permit, testCase.request = recoveryItemWriteAuthorityForTest(
				t, testCase.fixture.now, testCase.fixture.binding,
				testCase.fixture.writePermit.permit.JobID, TargetModeIsolated,
				testCase.request.Object.PrivateRelativeLocator,
				RecoveryOperationCreate, ExpectedTargetIdentity{Kind: ExpectedTargetAbsent}, test.payload,
			)
			tracker := &recoveryReadTrackingReader{reader: bytes.NewReader(test.payload)}
			if test.content != nil {
				tracker = test.content(test.payload)
			}
			testCase.request.Content = tracker
			if test.configure != nil {
				test.configure(t, testCase, tempPath, test.payload)
			}
			wantErr := test.wantErr
			if wantErr == nil {
				wantErr = ErrRecoveryTargetUnavailable
			}
			if err := testCase.write(); err != wantErr {
				t.Fatalf("stream error=%v, want exact %v", err, wantErr)
			}
			if testCase.base.openFileCalls != 1 ||
				!reflect.DeepEqual(testCase.base.openFilePaths, []string{tempPath}) ||
				!reflect.DeepEqual(testCase.base.openFileFlags, []int{os.O_WRONLY | os.O_CREATE | os.O_EXCL}) {
				t.Fatalf("stream temp opens=%v flags=%v, want one exact exclusive open",
					testCase.base.openFilePaths, testCase.base.openFileFlags)
			}
			if test.wantReread && testCase.base.openCalls != 1 {
				t.Fatalf("reread calls=%d, want one (open-file=%d chmod=%d sync=%d temp-lstat=%d temp-realpath=%d remove=%d)",
					testCase.base.openCalls, testCase.base.openFileCalls, testCase.base.chmodCalls,
					testCase.base.syncCalls, testCase.client.lstatCalls[tempPath],
					testCase.client.realPathCalls[tempPath], testCase.base.removeCalls)
			}
			if !test.wantReread && !test.exactStream && testCase.base.openCalls != 0 {
				t.Fatalf("invalid stream reread calls=%d, want zero", testCase.base.openCalls)
			}
			if test.exactStream {
				if len(tracker.requests) == 0 || tracker.requests[len(tracker.requests)-1] != 1 {
					t.Fatalf("exact stream read requests=%v, want final one-byte EOF proof", tracker.requests)
				}
				if tracker.maxReadRequest > 32*1024 {
					t.Fatalf("exact stream max read request=%d, want bounded copy buffer", tracker.maxReadRequest)
				}
				if testCase.base.renameCalls > 1 {
					t.Fatalf("exact stream rename calls=%d, want at most one scripted boundary", testCase.base.renameCalls)
				}
			} else if testCase.base.renameCalls != 0 {
				t.Fatalf("invalid stream reached rename %d time(s)", testCase.base.renameCalls)
			}
			if test.wantOwnedRemove && !reflect.DeepEqual(testCase.base.removePaths, []string{tempPath}) {
				t.Fatalf("owned temp removals=%v, want exact one-path cleanup", testCase.base.removePaths)
			}
			if _, err := os.Lstat(tempPath); !os.IsNotExist(err) {
				t.Fatalf("stream temp stat error=%v, want removed", err)
			}
			assertRecoveryWriteFinalAbsentForTest(t, testCase.finalPath)
		})
	}
}

func TestRecoverySFTPTargetWriteAtomicNeverOverwritesFinal(t *testing.T) {
	preExisting := []struct {
		name    string
		install func(*testing.T, string)
		assert  func(*testing.T, string)
	}{
		{
			name: "same-content regular",
			install: func(t *testing.T, value string) {
				if err := os.WriteFile(value, []byte("payload"), 0o600); err != nil {
					t.Fatalf("install same-content final: %v", err)
				}
			},
			assert: func(t *testing.T, value string) {
				got, err := os.ReadFile(value)
				if err != nil || string(got) != "payload" {
					t.Fatalf("same-content final=%q error=%v, want preserved", got, err)
				}
			},
		},
		{
			name: "different regular",
			install: func(t *testing.T, value string) {
				if err := os.WriteFile(value, []byte("existing-final"), 0o640); err != nil {
					t.Fatalf("install different final: %v", err)
				}
			},
			assert: func(t *testing.T, value string) {
				got, err := os.ReadFile(value)
				if err != nil || string(got) != "existing-final" {
					t.Fatalf("different final=%q error=%v, want preserved", got, err)
				}
			},
		},
		{
			name: "directory",
			install: func(t *testing.T, value string) {
				if err := os.Mkdir(value, 0o700); err != nil {
					t.Fatalf("install final directory: %v", err)
				}
				if err := os.WriteFile(filepath.Join(value, "sentinel"), []byte("preserve"), 0o600); err != nil {
					t.Fatalf("install final directory sentinel: %v", err)
				}
			},
			assert: func(t *testing.T, value string) {
				info, err := os.Lstat(value)
				sentinel, sentinelErr := os.ReadFile(filepath.Join(value, "sentinel"))
				if err != nil || !info.IsDir() || sentinelErr != nil || string(sentinel) != "preserve" {
					t.Fatalf("final directory info=%v error=%v sentinel=%q sentinel-error=%v",
						info, err, sentinel, sentinelErr)
				}
			},
		},
		{
			name: "symlink",
			install: func(t *testing.T, value string) {
				destination := filepath.Join(t.TempDir(), "destination")
				if err := os.WriteFile(destination, []byte("destination"), 0o600); err != nil {
					t.Fatalf("install symlink destination: %v", err)
				}
				if err := os.Symlink(destination, value); err != nil {
					t.Fatalf("install final symlink: %v", err)
				}
			},
			assert: func(t *testing.T, value string) {
				info, err := os.Lstat(value)
				if err != nil || info.Mode()&os.ModeSymlink == 0 {
					t.Fatalf("final symlink info=%v error=%v, want preserved symlink", info, err)
				}
			},
		},
		{
			name: "special",
			install: func(t *testing.T, value string) {
				if err := syscall.Mkfifo(value, 0o600); err != nil {
					t.Fatalf("install final fifo: %v", err)
				}
			},
			assert: func(t *testing.T, value string) {
				info, err := os.Lstat(value)
				if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
					t.Fatalf("final fifo info=%v error=%v, want preserved fifo", info, err)
				}
			},
		},
	}
	for _, entry := range preExisting {
		t.Run("pre-temp "+entry.name, func(t *testing.T) {
			testCase := newRecoverySFTPParentWriteCaseForTest(t, TargetModeIsolated, "item.txt")
			testCase.client.openFile = nil
			entry.install(t, testCase.finalPath)
			if err := testCase.write(); err != ErrRecoveryTargetChanged {
				t.Fatalf("pre-existing %s error=%v, want exact ErrRecoveryTargetChanged", entry.name, err)
			}
			if testCase.base.openFileCalls != 0 || testCase.base.renameCalls != 0 || testCase.base.removeCalls != 0 {
				t.Fatalf("pre-existing %s mutation open=%d rename=%d remove=%d",
					entry.name, testCase.base.openFileCalls, testCase.base.renameCalls, testCase.base.removeCalls)
			}
			entry.assert(t, testCase.finalPath)
		})
	}

	t.Run("concurrent final wins standard rename without replacement", func(t *testing.T) {
		testCase := newRecoverySFTPParentWriteCaseForTest(t, TargetModeIsolated, "item.txt")
		testCase.client.openFile = nil
		concurrent := []byte("concurrent-final-winner")
		testCase.client.rename = func(oldName, newName string) error {
			if err := os.WriteFile(newName, concurrent, 0o600); err != nil {
				t.Fatalf("install concurrent final: %v", err)
			}
			if err := os.Chmod(newName, 0o600); err != nil {
				t.Fatalf("chmod concurrent final: %v", err)
			}
			return testCase.base.Rename(oldName, newName)
		}
		if err := testCase.write(); err != ErrRecoveryTargetUnavailable {
			t.Fatalf("concurrent final error=%v, want exact ErrRecoveryTargetUnavailable", err)
		}
		if testCase.base.renameCalls != 1 || len(testCase.base.renamePaths) != 1 ||
			testCase.base.renamePaths[0][1] != testCase.finalPath {
			t.Fatalf("concurrent rename calls=%d paths=%v, want one standard Rename",
				testCase.base.renameCalls, testCase.base.renamePaths)
		}
		preserved, err := os.ReadFile(testCase.finalPath)
		if err != nil || !bytes.Equal(preserved, concurrent) {
			t.Fatalf("concurrent final=%q error=%v, want winner preserved", preserved, err)
		}
		if len(testCase.base.removePaths) != 1 || testCase.base.removePaths[0] == testCase.finalPath {
			t.Fatalf("concurrent cleanup paths=%v, want temp only", testCase.base.removePaths)
		}
	})

	t.Run("ambiguous rename never infers visible success", func(t *testing.T) {
		testCase := newRecoverySFTPParentWriteCaseForTest(t, TargetModeIsolated, "item.txt")
		testCase.client.openFile = nil
		testCase.client.rename = func(oldName, newName string) error {
			testCase.base.renameCalls++
			testCase.base.renamePaths = append(testCase.base.renamePaths, [2]string{oldName, newName})
			return errors.New("raw ambiguous standard rename status")
		}
		if err := testCase.write(); err != ErrRecoveryTargetUnavailable {
			t.Fatalf("ambiguous rename error=%v, want exact ErrRecoveryTargetUnavailable", err)
		}
		if testCase.base.renameCalls != 1 || len(testCase.base.renamePaths) != 1 ||
			testCase.base.renamePaths[0][1] != testCase.finalPath {
			t.Fatalf("ambiguous rename calls=%d paths=%v, want one standard Rename",
				testCase.base.renameCalls, testCase.base.renamePaths)
		}
		if len(testCase.base.removePaths) != 1 || testCase.base.removePaths[0] == testCase.finalPath {
			t.Fatalf("ambiguous cleanup paths=%v, want temp only", testCase.base.removePaths)
		}
		assertRecoveryWriteFinalAbsentForTest(t, testCase.finalPath)
	})
}

func TestRecoverySFTPTargetWriteAtomicReturnsExactVerifyRevision(t *testing.T) {
	for _, test := range []struct {
		name    string
		payload []byte
	}{
		{name: "zero-byte", payload: []byte{}},
		{name: "ordinary", payload: []byte("published recovery payload")},
	} {
		t.Run(test.name, func(t *testing.T) {
			testCase := newRecoverySFTPParentWriteCaseForTest(t, TargetModeIsolated, "nested/item.txt")
			testCase.client.openFile = nil
			jobID := testCase.fixture.writePermit.permit.JobID
			testCase.permit, testCase.request = recoveryItemWriteAuthorityForTest(
				t, testCase.fixture.now, testCase.fixture.binding, jobID,
				TargetModeIsolated, testCase.request.Object.PrivateRelativeLocator,
				RecoveryOperationCreate, ExpectedTargetIdentity{Kind: ExpectedTargetAbsent}, test.payload,
			)
			write, err := testCase.target.WriteAtomic(
				context.Background(), testCase.permit, testCase.request,
			)
			if err != nil {
				t.Fatalf("publish exact regular file: %v", err)
			}
			info, err := os.Lstat(testCase.finalPath)
			if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
				t.Fatalf("published final info=%v error=%v, want regular 0600", info, err)
			}
			digest := sha256.Sum256(test.payload)
			identityDigest := hex.EncodeToString(digest[:])
			verifyPermit, object, expectation := recoveryVerifyAuthorityForTest(
				t, testCase.fixture.now, testCase.fixture.binding, jobID, TargetModeIsolated,
				testCase.request.Object.PrivateRelativeLocator, identityDigest, int64(len(test.payload)),
			)
			verifyTarget := testCase.fixture.targetWithClient(&recoveryLocalSFTPClient{})
			verifyTarget.now = func() time.Time { return testCase.fixture.now }
			observed, err := verifyTarget.Verify(
				context.Background(), verifyPermit, object, expectation,
			)
			if err != nil {
				t.Fatalf("verify published regular file: %v", err)
			}
			if write.BytesWritten != expectation.Present.Bytes ||
				write.IdentityDigest != expectation.Present.IdentityDigest ||
				write.TargetRevision != observed.ObservedRevision {
				t.Fatalf("write=%+v verify=%+v, want identical content and revision", write, observed)
			}
			wantRevision := recoverySFTPRegularFileObservationRevisionForTest(
				t, testCase.fixture.binding, object, identityDigest, int64(len(test.payload)),
			)
			if write.TargetRevision != wantRevision {
				t.Fatalf("write revision=%q, want stable %q", write.TargetRevision, wantRevision)
			}
			tempPath := filepath.Join(
				filepath.Dir(testCase.finalPath),
				recoveryPayloadTempPrefix+"WlpaWlpaWlpaWlpaWlpaWlpaWlpaWlpaWlpaWlpaWlo",
			)
			if _, err := os.Lstat(tempPath); !os.IsNotExist(err) {
				t.Fatalf("successful temp stat error=%v, want absent", err)
			}
		})
	}
}

func TestRecoverySFTPTargetWriteAtomicRejectsFinalAndCloseDrift(t *testing.T) {
	finalDrifts := []struct {
		name      string
		configure func(*testing.T, *recoverySFTPParentWriteCaseForTest)
	}{
		{
			name: "final mode",
			configure: func(t *testing.T, testCase *recoverySFTPParentWriteCaseForTest) {
				testCase.client.rename = func(oldName, newName string) error {
					if err := testCase.base.Rename(oldName, newName); err != nil {
						return err
					}
					if err := os.Chmod(newName, 0o640); err != nil {
						t.Fatalf("drift final mode: %v", err)
					}
					return nil
				}
			},
		},
		{
			name: "final content",
			configure: func(t *testing.T, testCase *recoverySFTPParentWriteCaseForTest) {
				testCase.client.rename = func(oldName, newName string) error {
					if err := testCase.base.Rename(oldName, newName); err != nil {
						return err
					}
					if err := os.WriteFile(newName, []byte("drifted"), 0o600); err != nil {
						t.Fatalf("drift final content: %v", err)
					}
					return nil
				}
			},
		},
		{
			name: "final stat",
			configure: func(_ *testing.T, testCase *recoverySFTPParentWriteCaseForTest) {
				testCase.client.lstat = func(value string, call int) (os.FileInfo, error) {
					info, err := testCase.base.Lstat(value)
					if err != nil || value != testCase.finalPath || call < 3 {
						return info, err
					}
					size := info.Size() + 1
					return recoveryFileInfoOverride{FileInfo: info, size: &size}, nil
				}
			},
		},
		{
			name: "final canonical path",
			configure: func(_ *testing.T, testCase *recoverySFTPParentWriteCaseForTest) {
				testCase.client.realPath = func(value string, _ int) (string, error) {
					canonical, err := testCase.base.RealPath(value)
					if err == nil && value == testCase.finalPath {
						return canonical + "-alias", nil
					}
					return canonical, err
				}
			},
		},
	}
	for _, drift := range finalDrifts {
		t.Run(drift.name+" drift", func(t *testing.T) {
			testCase := newRecoverySFTPParentWriteCaseForTest(t, TargetModeIsolated, "item.txt")
			testCase.client.openFile = nil
			drift.configure(t, testCase)
			result, err := testCase.target.WriteAtomic(
				context.Background(), testCase.permit, testCase.request,
			)
			if err != ErrRecoveryTargetChanged || result != (TargetWriteResult{}) {
				t.Fatalf("%s drift result=%+v error=%v, want exact changed and no result",
					drift.name, result, err)
			}
		})
	}

	t.Run("live permit revoked before rename", func(t *testing.T) {
		testCase := newRecoverySFTPParentWriteCaseForTest(t, TargetModeIsolated, "item.txt")
		testCase.client.openFile = nil
		revoked := false
		testCase.permit.permit.proof.validateAt = func(time.Time) error {
			if revoked {
				return ErrInvalidTargetPermit
			}
			return nil
		}
		tempPath := filepath.Join(
			filepath.Dir(testCase.finalPath),
			recoveryPayloadTempPrefix+"WlpaWlpaWlpaWlpaWlpaWlpaWlpaWlpaWlpaWlpaWlo",
		)
		testCase.client.realPath = func(value string, call int) (string, error) {
			canonical, err := testCase.base.RealPath(value)
			if err == nil && value == tempPath && call >= 4 {
				revoked = true
			}
			return canonical, err
		}
		result, err := testCase.target.WriteAtomic(
			context.Background(), testCase.permit, testCase.request,
		)
		if err != ErrInvalidTargetPermit || result != (TargetWriteResult{}) {
			t.Fatalf("pre-rename revocation result=%+v error=%v, want exact invalid permit", result, err)
		}
		if testCase.base.renameCalls != 0 {
			t.Fatalf("pre-rename revocation rename calls=%d, want zero", testCase.base.renameCalls)
		}
		assertRecoveryWriteFinalAbsentForTest(t, testCase.finalPath)
	})

	t.Run("live permit revoked after final verify", func(t *testing.T) {
		testCase := newRecoverySFTPParentWriteCaseForTest(t, TargetModeIsolated, "item.txt")
		testCase.client.openFile = nil
		revoked := false
		testCase.permit.permit.proof.validateAt = func(time.Time) error {
			if revoked {
				return ErrInvalidTargetPermit
			}
			return nil
		}
		testCase.client.open = func(value string) (recoveryTargetSFTPFile, error) {
			file, err := testCase.base.Open(value)
			if err == nil && value == testCase.finalPath {
				revoked = true
			}
			return file, err
		}
		result, err := testCase.target.WriteAtomic(
			context.Background(), testCase.permit, testCase.request,
		)
		if err != ErrInvalidTargetPermit || result != (TargetWriteResult{}) {
			t.Fatalf("post-verify revocation result=%+v error=%v, want exact invalid permit", result, err)
		}
		if testCase.base.renameCalls != 1 {
			t.Fatalf("post-verify revocation rename calls=%d, want one", testCase.base.renameCalls)
		}
	})

	t.Run("SFTP close ambiguity blocks success", func(t *testing.T) {
		testCase := newRecoverySFTPParentWriteCaseForTest(t, TargetModeIsolated, "item.txt")
		testCase.client.openFile = nil
		testCase.client.close = func() error {
			testCase.base.closeCalls++
			return errors.New("raw SFTP close failure")
		}
		result, err := testCase.target.WriteAtomic(
			context.Background(), testCase.permit, testCase.request,
		)
		if err != ErrRecoveryTargetUnavailable || result != (TargetWriteResult{}) {
			t.Fatalf("SFTP close result=%+v error=%v, want exact unavailable", result, err)
		}
		if testCase.base.renameCalls != 1 {
			t.Fatalf("SFTP close rename calls=%d, want published final before close failure", testCase.base.renameCalls)
		}
		if _, err := os.Lstat(testCase.finalPath); err != nil {
			t.Fatalf("SFTP close final stat: %v", err)
		}
	})

	t.Run("SSH close ambiguity blocks success", func(t *testing.T) {
		testCase := newRecoverySFTPParentWriteCaseForTest(t, TargetModeIsolated, "item.txt")
		testCase.client.openFile = nil
		sshCloseCalls := 0
		testCase.target = newRecoverySFTPTargetForTest(
			newRecoveryTargetSessionFactoryForTest(
				testCase.fixture.resolver, testCase.fixture.dialer,
				func(*ssh.Client) (recoveryTargetSFTPClient, error) { return testCase.client, nil },
				func(*ssh.Client) error {
					sshCloseCalls++
					return errors.New("raw SSH close failure")
				},
			),
			testCase.fixture.codec,
		)
		testCase.target.entropy = bytes.NewReader(bytes.Repeat([]byte{0x5a}, 32))
		testCase.target.now = func() time.Time { return testCase.fixture.now }
		result, err := testCase.target.WriteAtomic(
			context.Background(), testCase.permit, testCase.request,
		)
		if err != ErrRecoveryTargetUnavailable || result != (TargetWriteResult{}) || sshCloseCalls != 1 {
			t.Fatalf("SSH close result=%+v error=%v calls=%d, want unavailable once",
				result, err, sshCloseCalls)
		}
		if testCase.base.renameCalls != 1 {
			t.Fatalf("SSH close rename calls=%d, want published final before close failure", testCase.base.renameCalls)
		}
		if _, err := os.Lstat(testCase.finalPath); err != nil {
			t.Fatalf("SSH close final stat: %v", err)
		}
	})

	t.Run("context identity wins close noise", func(t *testing.T) {
		testCase := newRecoverySFTPParentWriteCaseForTest(t, TargetModeIsolated, "item.txt")
		testCase.client.openFile = nil
		ctx, cancel := context.WithCancel(context.Background())
		testCase.client.close = func() error {
			testCase.base.closeCalls++
			cancel()
			return errors.New("raw close after cancellation")
		}
		result, err := testCase.target.WriteAtomic(ctx, testCase.permit, testCase.request)
		if err != context.Canceled || result != (TargetWriteResult{}) {
			t.Fatalf("canceled close result=%+v error=%v, want exact context.Canceled", result, err)
		}
		if testCase.base.renameCalls != 1 {
			t.Fatalf("canceled close rename calls=%d, want published final before close cancellation", testCase.base.renameCalls)
		}
		if _, err := os.Lstat(testCase.finalPath); err != nil {
			t.Fatalf("canceled close final stat: %v", err)
		}
	})
}

func TestRecoverySFTPTargetWriteAtomicCancellationAndPrivacy(t *testing.T) {
	rawFailure := "raw-cancel-dependency: private-host private-user private-sftp-status"
	rawErr := errors.New(rawFailure)
	stages := []string{
		"entropy", "parent mkdir", "parent chmod", "source read", "temp write",
		"sync", "reopen", "pre-rename", "rename", "final read", "session close",
	}
	for _, stage := range stages {
		t.Run(stage, func(t *testing.T) {
			itemLocator := "item.txt"
			if stage == "parent mkdir" || stage == "parent chmod" {
				itemLocator = "nested/item.txt"
			}
			testCase := newRecoverySFTPParentWriteCaseForTest(t, TargetModeIsolated, itemLocator)
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			tempPath := filepath.Join(
				filepath.Dir(testCase.finalPath),
				recoveryPayloadTempPrefix+"WlpaWlpaWlpaWlpaWlpaWlpaWlpaWlpaWlpaWlpaWlo",
			)
			openedFiles := make([]*recoveryCloseCountingSFTPFile, 0, 3)
			openWriteFile := func(value string, flag int) (recoveryTargetSFTPFile, error) {
				file, err := testCase.base.OpenFile(value, flag)
				if err != nil {
					return nil, err
				}
				counted := &recoveryCloseCountingSFTPFile{recoveryTargetSFTPFile: file}
				openedFiles = append(openedFiles, counted)
				return counted, nil
			}
			openReadFile := func(value string) (recoveryTargetSFTPFile, error) {
				file, err := testCase.base.Open(value)
				if err != nil {
					return nil, err
				}
				counted := &recoveryCloseCountingSFTPFile{recoveryTargetSFTPFile: file}
				openedFiles = append(openedFiles, counted)
				return counted, nil
			}
			testCase.client.openFile = openWriteFile
			testCase.client.open = openReadFile
			testCase.client.close = func() error {
				testCase.base.closeCalls++
				return nil
			}
			sshCloseCalls := 0
			testCase.target = newRecoverySFTPTargetForTest(
				newRecoveryTargetSessionFactoryForTest(
					testCase.fixture.resolver, testCase.fixture.dialer,
					func(*ssh.Client) (recoveryTargetSFTPClient, error) { return testCase.client, nil },
					func(*ssh.Client) error {
						sshCloseCalls++
						return nil
					},
				),
				testCase.fixture.codec,
			)
			testCase.target.entropy = bytes.NewReader(bytes.Repeat([]byte{0x5a}, 32))
			testCase.target.now = func() time.Time { return testCase.fixture.now }

			switch stage {
			case "entropy":
				testCase.target.entropy = &recoveryReadTrackingReader{read: func([]byte) (int, error) {
					cancel()
					return 0, rawErr
				}}
			case "parent mkdir":
				testCase.client.mkdir = func(value string) error {
					testCase.base.mkdirCalls++
					testCase.base.mkdirPaths = append(testCase.base.mkdirPaths, value)
					cancel()
					return rawErr
				}
			case "parent chmod":
				testCase.client.chmod = func(value string, mode os.FileMode) error {
					testCase.base.chmodCalls++
					testCase.base.chmodPaths = append(testCase.base.chmodPaths, value)
					testCase.base.chmodModes = append(testCase.base.chmodModes, mode)
					cancel()
					return rawErr
				}
			case "source read":
				testCase.request.Content = &recoveryReadTrackingReader{read: func([]byte) (int, error) {
					cancel()
					return 0, rawErr
				}}
			case "temp write":
				testCase.client.openFile = func(value string, flag int) (recoveryTargetSFTPFile, error) {
					file, err := testCase.base.OpenFile(value, flag)
					if err != nil {
						return nil, err
					}
					scripted := &recoveryScriptedSFTPFile{base: file, write: func([]byte) (int, error) {
						cancel()
						return 0, rawErr
					}}
					counted := &recoveryCloseCountingSFTPFile{recoveryTargetSFTPFile: scripted}
					openedFiles = append(openedFiles, counted)
					return counted, nil
				}
			case "sync":
				testCase.client.openFile = func(value string, flag int) (recoveryTargetSFTPFile, error) {
					file, err := testCase.base.OpenFile(value, flag)
					if err != nil {
						return nil, err
					}
					scripted := &recoveryScriptedSFTPFile{base: file, sync: func() error {
						cancel()
						return rawErr
					}}
					counted := &recoveryCloseCountingSFTPFile{recoveryTargetSFTPFile: scripted}
					openedFiles = append(openedFiles, counted)
					return counted, nil
				}
			case "reopen":
				testCase.client.open = func(value string) (recoveryTargetSFTPFile, error) {
					testCase.base.openCalls++
					testCase.base.openPaths = append(testCase.base.openPaths, value)
					cancel()
					return nil, rawErr
				}
			case "pre-rename":
				testCase.client.realPath = func(value string, call int) (string, error) {
					canonical, err := testCase.base.RealPath(value)
					if err == nil && value == tempPath && call >= 4 {
						cancel()
					}
					return canonical, err
				}
			case "rename":
				testCase.client.rename = func(oldName, newName string) error {
					testCase.base.renameCalls++
					testCase.base.renamePaths = append(testCase.base.renamePaths, [2]string{oldName, newName})
					cancel()
					return rawErr
				}
			case "final read":
				testCase.client.open = func(value string) (recoveryTargetSFTPFile, error) {
					if value == testCase.finalPath {
						testCase.base.openCalls++
						testCase.base.openPaths = append(testCase.base.openPaths, value)
						cancel()
						return nil, rawErr
					}
					return openReadFile(value)
				}
			case "session close":
				testCase.client.close = func() error {
					testCase.base.closeCalls++
					cancel()
					return rawErr
				}
			}

			result, err := testCase.target.WriteAtomic(ctx, testCase.permit, testCase.request)
			if err != context.Canceled || result != (TargetWriteResult{}) {
				t.Fatalf("%s cancellation result=%+v error=%v, want exact context.Canceled",
					stage, result, err)
			}
			for index, file := range openedFiles {
				if file.closeCalls != 1 {
					t.Fatalf("%s file %d close calls=%d, want exactly one", stage, index, file.closeCalls)
				}
			}
			wantSessionClose := 1
			if stage == "entropy" {
				wantSessionClose = 0
			}
			if testCase.base.closeCalls != wantSessionClose || sshCloseCalls != wantSessionClose {
				t.Fatalf("%s SFTP/SSH close=%d/%d, want %d/%d",
					stage, testCase.base.closeCalls, sshCloseCalls, wantSessionClose, wantSessionClose)
			}
			if len(testCase.base.removePaths) > 1 ||
				(len(testCase.base.removePaths) == 1 && testCase.base.removePaths[0] != tempPath) {
				t.Fatalf("%s cleanup paths=%v, want at most exact owned temp", stage, testCase.base.removePaths)
			}
			wantRenameCalls := 0
			if stage == "rename" || stage == "final read" || stage == "session close" {
				wantRenameCalls = 1
			}
			if testCase.base.renameCalls != wantRenameCalls {
				t.Fatalf("%s rename calls=%d, want %d", stage, testCase.base.renameCalls, wantRenameCalls)
			}

			encoded, jsonErr := json.Marshal([]any{
				testCase.permit, testCase.request, result, testCase.fixture.dialer.audit,
			})
			if jsonErr != nil {
				t.Fatalf("marshal cancellation products: %v", jsonErr)
			}
			products := []string{err.Error(), string(encoded), fmt.Sprintf("%+v", testCase.fixture.dialer.audit)}
			for _, product := range products {
				for _, forbidden := range []string{
					rawFailure, testCase.fixture.binding.RootLocator,
					testCase.fixture.binding.CredentialRevision,
					testCase.request.Object.PrivateRelativeLocator,
					"payload", filepath.Base(tempPath),
				} {
					if forbidden != "" && strings.Contains(product, forbidden) {
						t.Fatalf("%s product leaked %q: %s", stage, forbidden, product)
					}
				}
			}
		})
	}
}

func TestRecoverySFTPTargetOpenOwnedResultValidatesMarkerAndExactContent(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		payload []byte
	}{
		{name: "ordinary", payload: bytes.Repeat([]byte("r"), 128*1024+7)},
		{name: "zero bytes", payload: []byte{}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newRecoveryLocalSFTPTargetFixture(t)
			fixture.create(t)
			_, jobPath, _ := fixture.paths()
			resultPath := filepath.Join(jobPath, "result.bin")
			if err := os.WriteFile(resultPath, testCase.payload, 0o600); err != nil {
				t.Fatalf("write published result fixture: %v", err)
			}
			object := TargetObjectRef{
				RootID: fixture.binding.RootID, RootLocatorDigest: fixture.binding.RootLocatorDigest,
				PrivateRelativeLocator: recoveryWorkspaceLocatorDirectory + "/" +
					fixture.writePermit.permit.JobID + "/result.bin",
			}
			object.TargetPathDigest = mustTargetPathDigest(
				t, object.RootID, object.RootLocatorDigest, object.PrivateRelativeLocator,
			)
			permit, request := recoveryTargetResultReadPermitForTest(t, fixture, object, testCase.payload)

			reader, err := fixture.target.OpenOwnedResult(context.Background(), permit, request)
			if err != nil {
				t.Fatalf("open published result: %v", err)
			}
			actual, err := io.ReadAll(reader)
			if err != nil || !bytes.Equal(actual, testCase.payload) {
				t.Fatalf("read published result bytes=%d err=%v", len(actual), err)
			}
			if err := reader.Close(); err != nil {
				t.Fatalf("close published result: %v", err)
			}
			client := fixture.clients[len(fixture.clients)-1]
			finalOpens := 0
			for _, opened := range client.openPaths {
				if opened == resultPath {
					finalOpens++
				}
			}
			if finalOpens != 2 || client.maxReadRequest <= 0 || client.maxReadRequest > 32*1024 ||
				client.mkdirCalls != 0 || client.chmodCalls != 0 || client.openFileCalls != 0 ||
				client.renameCalls != 0 || client.removeCalls != 0 || client.closeCalls != 1 {
				t.Fatalf("result-read client=%+v final_opens=%d", client, finalOpens)
			}
		})
	}
}

func TestRecoverySFTPTargetOpenOwnedResultDriftAndResourceMatrix(t *testing.T) {
	t.Run("authenticated marker drift", func(t *testing.T) {
		fixture := newRecoveryLocalSFTPTargetFixture(t)
		fixture.create(t)
		_, jobPath, markerPath := fixture.paths()
		payload := []byte("published recovery result")
		if err := os.WriteFile(filepath.Join(jobPath, "result.bin"), payload, 0o600); err != nil {
			t.Fatalf("write marker-drift result: %v", err)
		}
		if err := os.WriteFile(markerPath, []byte(`{"tampered":true}`), 0o600); err != nil {
			t.Fatalf("tamper result-read marker: %v", err)
		}
		object := TargetObjectRef{
			RootID: fixture.binding.RootID, RootLocatorDigest: fixture.binding.RootLocatorDigest,
			PrivateRelativeLocator: recoveryWorkspaceLocatorDirectory + "/" +
				fixture.writePermit.permit.JobID + "/result.bin",
		}
		object.TargetPathDigest = mustTargetPathDigest(
			t, object.RootID, object.RootLocatorDigest, object.PrivateRelativeLocator,
		)
		permit, request := recoveryTargetResultReadPermitForTest(t, fixture, object, payload)

		reader, err := fixture.target.OpenOwnedResult(context.Background(), permit, request)
		if err != ErrRecoveryTargetChanged || reader != nil {
			t.Fatalf("marker drift reader=%v error=%v, want exact target changed", reader, err)
		}
		client := fixture.clients[len(fixture.clients)-1]
		if client.closeCalls != 1 || client.mkdirCalls != 0 || client.chmodCalls != 0 ||
			client.openFileCalls != 0 || client.renameCalls != 0 || client.removeCalls != 0 {
			t.Fatalf("marker drift client=%+v", client)
		}
	})

	preOpenDrifts := []struct {
		name   string
		mutate func(*testing.T, *recoveryOwnedResultReadCaseForTest)
	}{
		{name: "digest mismatch", mutate: func(t *testing.T, testCase *recoveryOwnedResultReadCaseForTest) {
			t.Helper()
			if err := os.WriteFile(testCase.resultPath, bytes.Repeat([]byte("x"), len(testCase.payload)), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "short content", mutate: func(t *testing.T, testCase *recoveryOwnedResultReadCaseForTest) {
			t.Helper()
			if err := os.WriteFile(testCase.resultPath, testCase.payload[:len(testCase.payload)-1], 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "extra content", mutate: func(t *testing.T, testCase *recoveryOwnedResultReadCaseForTest) {
			t.Helper()
			if err := os.WriteFile(testCase.resultPath, append(testCase.payload, 'x'), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "non-private mode", mutate: func(t *testing.T, testCase *recoveryOwnedResultReadCaseForTest) {
			t.Helper()
			if err := os.Chmod(testCase.resultPath, 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "symlink final", mutate: func(t *testing.T, testCase *recoveryOwnedResultReadCaseForTest) {
			t.Helper()
			if err := os.Remove(testCase.resultPath); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(testCase.markerPath, testCase.resultPath); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "directory final", mutate: func(t *testing.T, testCase *recoveryOwnedResultReadCaseForTest) {
			t.Helper()
			if err := os.Remove(testCase.resultPath); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(testCase.resultPath, 0o700); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, drift := range preOpenDrifts {
		t.Run(drift.name, func(t *testing.T) {
			testCase := newRecoveryOwnedResultReadCaseForTest(t, []byte("published-result-content"))
			drift.mutate(t, testCase)
			reader, err := testCase.fixture.target.OpenOwnedResult(
				context.Background(), testCase.permit, testCase.request,
			)
			if err != ErrRecoveryTargetChanged || reader != nil {
				t.Fatalf("pre-open drift reader=%v error=%v, want exact changed", reader, err)
			}
			client := testCase.fixture.clients[len(testCase.fixture.clients)-1]
			assertRecoveryOwnedResultReadOnlyForTest(t, client)
			if client.closeCalls != 1 {
				t.Fatalf("pre-open drift close calls=%d, want one", client.closeCalls)
			}
		})
	}

	for _, alias := range []struct {
		name string
		path func(*recoveryOwnedResultReadCaseForTest) string
	}{
		{name: "root alias", path: func(testCase *recoveryOwnedResultReadCaseForTest) string {
			return testCase.fixture.binding.RootLocator
		}},
		{name: "parent alias", path: func(testCase *recoveryOwnedResultReadCaseForTest) string {
			return filepath.Dir(testCase.resultPath)
		}},
		{name: "final alias", path: func(testCase *recoveryOwnedResultReadCaseForTest) string {
			return testCase.resultPath
		}},
	} {
		t.Run(alias.name, func(t *testing.T) {
			testCase := newRecoveryOwnedResultReadCaseForTest(t, []byte("published-result-content"))
			base := &recoveryLocalSFTPClient{}
			aliasedPath := alias.path(testCase)
			scripted := &recoveryScriptedSFTPClient{base: base}
			scripted.realPath = func(value string, _ int) (string, error) {
				if value == aliasedPath {
					return value + "-alias", nil
				}
				return base.RealPath(value)
			}
			target := testCase.fixture.targetWithClient(scripted)
			target.now = func() time.Time { return testCase.fixture.now }
			reader, err := target.OpenOwnedResult(
				context.Background(), testCase.permit, testCase.request,
			)
			if err != ErrRecoveryTargetChanged || reader != nil {
				t.Fatalf("canonical alias reader=%v error=%v, want exact target changed", reader, err)
			}
			assertRecoveryOwnedResultReadOnlyForTest(t, base)
			if base.closeCalls != 1 {
				t.Fatalf("canonical alias close calls=%d, want one", base.closeCalls)
			}
		})
	}

	t.Run("partial consumer close", func(t *testing.T) {
		testCase := newRecoveryOwnedResultReadCaseForTest(t, []byte("published-result-content"))
		reader, err := testCase.fixture.target.OpenOwnedResult(
			context.Background(), testCase.permit, testCase.request,
		)
		if err != nil {
			t.Fatalf("open partial result read: %v", err)
		}
		buffer := make([]byte, 1)
		if count, err := reader.Read(buffer); count != 1 || err != nil {
			t.Fatalf("partial result read count=%d error=%v", count, err)
		}
		if err := reader.Close(); err != nil {
			t.Fatalf("partial result first close: %v", err)
		}
		if err := reader.Close(); err != nil {
			t.Fatalf("partial result second close: %v", err)
		}
		client := testCase.fixture.clients[len(testCase.fixture.clients)-1]
		assertRecoveryOwnedResultReadOnlyForTest(t, client)
		if client.closeCalls != 1 {
			t.Fatalf("partial result session close calls=%d, want one", client.closeCalls)
		}
	})

	t.Run("zero byte close revalidates marker", func(t *testing.T) {
		testCase := newRecoveryOwnedResultReadCaseForTest(t, nil)
		reader, err := testCase.fixture.target.OpenOwnedResult(
			context.Background(), testCase.permit, testCase.request,
		)
		if err != nil {
			t.Fatalf("open zero-byte result: %v", err)
		}
		if err := os.WriteFile(testCase.markerPath, []byte(`{"tampered":true}`), 0o600); err != nil {
			t.Fatalf("tamper zero-byte result marker: %v", err)
		}
		if closeErr := reader.Close(); closeErr != ErrRecoveryTargetChanged {
			t.Fatalf("zero-byte result close error=%v, want exact target changed", closeErr)
		}
		client := testCase.fixture.clients[len(testCase.fixture.clients)-1]
		assertRecoveryOwnedResultReadOnlyForTest(t, client)
		if client.closeCalls != 1 {
			t.Fatalf("zero-byte result close calls=%d, want one", client.closeCalls)
		}
	})

	t.Run("context cancellation", func(t *testing.T) {
		testCase := newRecoveryOwnedResultReadCaseForTest(t, []byte("published-result-content"))
		ctx, cancel := context.WithCancel(context.Background())
		reader, err := testCase.fixture.target.OpenOwnedResult(ctx, testCase.permit, testCase.request)
		if err != nil {
			t.Fatalf("open canceled result read: %v", err)
		}
		cancel()
		if _, err := reader.Read(make([]byte, 1)); err != context.Canceled {
			t.Fatalf("canceled result read error=%v, want context.Canceled", err)
		}
		if err := reader.Close(); err != context.Canceled {
			t.Fatalf("canceled result close error=%v, want context.Canceled", err)
		}
		client := testCase.fixture.clients[len(testCase.fixture.clients)-1]
		assertRecoveryOwnedResultReadOnlyForTest(t, client)
		if client.closeCalls != 1 {
			t.Fatalf("canceled result session close calls=%d, want one", client.closeCalls)
		}
	})

	t.Run("context cancellation closes transport before file", func(t *testing.T) {
		testCase := newRecoveryOwnedResultReadCaseForTest(t, []byte("published-result-content"))
		base := &recoveryLocalSFTPClient{}
		finalOpens := 0
		closeOrder := make([]string, 0, 3)
		scripted := &recoveryScriptedSFTPClient{base: base}
		scripted.open = func(value string) (recoveryTargetSFTPFile, error) {
			file, err := base.Open(value)
			if err != nil || value != testCase.resultPath {
				return file, err
			}
			finalOpens++
			if finalOpens != 2 {
				return file, nil
			}
			return &recoveryScriptedSFTPFile{base: file, close: func() error {
				closeOrder = append(closeOrder, "file")
				return file.Close()
			}}, nil
		}
		scripted.close = func() error {
			closeOrder = append(closeOrder, "sftp")
			return base.Close()
		}
		sshClosed := make(chan struct{})
		target := newRecoverySFTPTargetForTest(
			newRecoveryTargetSessionFactoryForTest(
				testCase.fixture.resolver, testCase.fixture.dialer,
				func(*ssh.Client) (recoveryTargetSFTPClient, error) { return scripted, nil },
				func(*ssh.Client) error {
					closeOrder = append(closeOrder, "ssh")
					close(sshClosed)
					return nil
				},
			),
			testCase.fixture.codec,
		)
		target.now = func() time.Time { return testCase.fixture.now }
		ctx, cancel := context.WithCancel(context.Background())
		reader, err := target.OpenOwnedResult(ctx, testCase.permit, testCase.request)
		if err != nil {
			t.Fatalf("open cancel-order result: %v", err)
		}
		cancel()
		select {
		case <-sshClosed:
		case <-time.After(time.Second):
			t.Fatal("canceled result session did not close")
		}
		if closeErr := reader.Close(); closeErr != context.Canceled {
			t.Fatalf("cancel-order result close error=%v, want context.Canceled", closeErr)
		}
		if !reflect.DeepEqual(closeOrder, []string{"sftp", "ssh", "file"}) {
			t.Fatalf("canceled result close order=%v, want [sftp ssh file]", closeOrder)
		}
	})

	for _, drift := range []struct {
		name   string
		mutate func(*testing.T, *recoveryOwnedResultReadCaseForTest, *time.Time)
		want   error
	}{
		{name: "marker after open", want: ErrRecoveryTargetChanged, mutate: func(t *testing.T, testCase *recoveryOwnedResultReadCaseForTest, _ *time.Time) {
			t.Helper()
			if err := os.WriteFile(testCase.markerPath, []byte(`{"tampered":true}`), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "content after open", want: ErrRecoveryTargetChanged, mutate: func(t *testing.T, testCase *recoveryOwnedResultReadCaseForTest, _ *time.Time) {
			t.Helper()
			if err := os.WriteFile(testCase.resultPath, bytes.Repeat([]byte("z"), len(testCase.payload)), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "snapshot after open", want: ErrRecoveryTargetChanged, mutate: func(t *testing.T, testCase *recoveryOwnedResultReadCaseForTest, _ *time.Time) {
			t.Helper()
			changed := time.Now().Add(time.Hour)
			if err := os.Chtimes(testCase.resultPath, changed, changed); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "permit expiry", want: ErrInvalidTargetPermit, mutate: func(_ *testing.T, _ *recoveryOwnedResultReadCaseForTest, now *time.Time) {
			*now = (*now).Add(2 * time.Minute)
		}},
	} {
		t.Run(drift.name, func(t *testing.T) {
			testCase := newRecoveryOwnedResultReadCaseForTest(t, []byte("published-result-content"))
			now := testCase.fixture.now
			testCase.fixture.target.now = func() time.Time { return now }
			reader, err := testCase.fixture.target.OpenOwnedResult(
				context.Background(), testCase.permit, testCase.request,
			)
			if err != nil {
				t.Fatalf("open post-open drift result: %v", err)
			}
			drift.mutate(t, testCase, &now)
			_, readErr := io.ReadAll(reader)
			if readErr != drift.want {
				t.Fatalf("post-open drift read error=%v, want exact %v", readErr, drift.want)
			}
			if closeErr := reader.Close(); closeErr != drift.want {
				t.Fatalf("post-open drift close error=%v, want exact %v", closeErr, drift.want)
			}
			client := testCase.fixture.clients[len(testCase.fixture.clients)-1]
			assertRecoveryOwnedResultReadOnlyForTest(t, client)
			if client.closeCalls != 1 {
				t.Fatalf("post-open drift close calls=%d, want one", client.closeCalls)
			}
		})
	}

	t.Run("read returns bytes with dependency error", func(t *testing.T) {
		testCase := newRecoveryOwnedResultReadCaseForTest(t, []byte("published-result-content"))
		base := &recoveryLocalSFTPClient{}
		finalOpens := 0
		rawFailure := "RAW_RESULT_READ_FAILURE_PRIVATE_HOST_CREDENTIAL"
		rawErr := errors.New(rawFailure)
		scripted := &recoveryScriptedSFTPClient{base: base}
		scripted.open = func(value string) (recoveryTargetSFTPFile, error) {
			file, err := base.Open(value)
			if err != nil || value != testCase.resultPath {
				return file, err
			}
			finalOpens++
			if finalOpens != 2 {
				return file, nil
			}
			firstRead := true
			return &recoveryScriptedSFTPFile{base: file, read: func(buffer []byte) (int, error) {
				count, readErr := file.Read(buffer)
				if firstRead {
					firstRead = false
					return count, rawErr
				}
				return count, readErr
			}}, nil
		}
		closeOrder := make([]string, 0, 2)
		scripted.close = func() error {
			closeOrder = append(closeOrder, "sftp")
			return base.Close()
		}
		sshCloseCalls := 0
		target := newRecoverySFTPTargetForTest(
			newRecoveryTargetSessionFactoryForTest(
				testCase.fixture.resolver, testCase.fixture.dialer,
				func(*ssh.Client) (recoveryTargetSFTPClient, error) { return scripted, nil },
				func(*ssh.Client) error {
					sshCloseCalls++
					closeOrder = append(closeOrder, "ssh")
					return nil
				},
			),
			testCase.fixture.codec,
		)
		target.now = func() time.Time { return testCase.fixture.now }
		reader, err := target.OpenOwnedResult(context.Background(), testCase.permit, testCase.request)
		if err != nil {
			t.Fatalf("open read-error result: %v", err)
		}
		_, readErr := io.ReadAll(reader)
		if readErr != ErrRecoveryTargetUnavailable || strings.Contains(readErr.Error(), rawFailure) {
			t.Fatalf("read dependency error=%v, want sanitized unavailable", readErr)
		}
		if closeErr := reader.Close(); closeErr != ErrRecoveryTargetUnavailable ||
			strings.Contains(closeErr.Error(), rawFailure) {
			t.Fatalf("read dependency close error=%v, want sanitized unavailable", closeErr)
		}
		assertRecoveryOwnedResultReadOnlyForTest(t, base)
		if finalOpens != 2 || base.closeCalls != 1 || sshCloseCalls != 1 ||
			!reflect.DeepEqual(closeOrder, []string{"sftp", "ssh"}) {
			t.Fatalf("read dependency final_opens=%d close=%d/%d order=%v",
				finalOpens, base.closeCalls, sshCloseCalls, closeOrder)
		}
	})

	for _, stage := range []string{"first-pass read", "second open", "second stat"} {
		t.Run(stage+" failure", func(t *testing.T) {
			testCase := newRecoveryOwnedResultReadCaseForTest(t, []byte("published-result-content"))
			base := &recoveryLocalSFTPClient{}
			finalOpens := 0
			rawFailure := "RAW_RESULT_OPEN_FAILURE_PRIVATE_HOST_CREDENTIAL_" + stage
			rawErr := errors.New(rawFailure)
			scripted := &recoveryScriptedSFTPClient{base: base}
			scripted.open = func(value string) (recoveryTargetSFTPFile, error) {
				file, err := base.Open(value)
				if err != nil || value != testCase.resultPath {
					return file, err
				}
				finalOpens++
				switch {
				case stage == "first-pass read" && finalOpens == 1:
					return &recoveryScriptedSFTPFile{base: file, read: func([]byte) (int, error) {
						return 0, rawErr
					}}, nil
				case stage == "second open" && finalOpens == 2:
					_ = file.Close()
					return nil, rawErr
				case stage == "second stat" && finalOpens == 2:
					return &recoveryScriptedSFTPFile{base: file, stat: func() (os.FileInfo, error) {
						return nil, rawErr
					}}, nil
				default:
					return file, nil
				}
			}
			sshCloseCalls := 0
			target := newRecoverySFTPTargetForTest(
				newRecoveryTargetSessionFactoryForTest(
					testCase.fixture.resolver, testCase.fixture.dialer,
					func(*ssh.Client) (recoveryTargetSFTPClient, error) { return scripted, nil },
					func(*ssh.Client) error { sshCloseCalls++; return nil },
				),
				testCase.fixture.codec,
			)
			target.now = func() time.Time { return testCase.fixture.now }
			reader, err := target.OpenOwnedResult(context.Background(), testCase.permit, testCase.request)
			if reader != nil || err != ErrRecoveryTargetUnavailable || strings.Contains(err.Error(), rawFailure) {
				t.Fatalf("%s reader=%v error=%v, want sanitized unavailable", stage, reader, err)
			}
			assertRecoveryOwnedResultReadOnlyForTest(t, base)
			if base.closeCalls != 1 || sshCloseCalls != 1 {
				t.Fatalf("%s close calls=%d/%d, want one/one", stage, base.closeCalls, sshCloseCalls)
			}
		})
	}

	for _, failedOpen := range []int{1, 2} {
		name := "first-pass open returns file with error"
		if failedOpen == 2 {
			name = "second open returns file with error"
		}
		t.Run(name, func(t *testing.T) {
			testCase := newRecoveryOwnedResultReadCaseForTest(t, []byte("published-result-content"))
			base := &recoveryLocalSFTPClient{}
			finalOpens := 0
			rawFailure := "RAW_RESULT_OPEN_RETURNED_FILE_PRIVATE_FAILURE"
			rawErr := errors.New(rawFailure)
			var returned *recoveryCloseCountingSFTPFile
			scripted := &recoveryScriptedSFTPClient{base: base}
			scripted.open = func(value string) (recoveryTargetSFTPFile, error) {
				file, err := base.Open(value)
				if err != nil || value != testCase.resultPath {
					return file, err
				}
				finalOpens++
				if finalOpens != failedOpen {
					return file, nil
				}
				returned = &recoveryCloseCountingSFTPFile{recoveryTargetSFTPFile: file}
				return returned, rawErr
			}
			sshCloseCalls := 0
			target := newRecoverySFTPTargetForTest(
				newRecoveryTargetSessionFactoryForTest(
					testCase.fixture.resolver, testCase.fixture.dialer,
					func(*ssh.Client) (recoveryTargetSFTPClient, error) { return scripted, nil },
					func(*ssh.Client) error { sshCloseCalls++; return nil },
				),
				testCase.fixture.codec,
			)
			target.now = func() time.Time { return testCase.fixture.now }
			reader, err := target.OpenOwnedResult(context.Background(), testCase.permit, testCase.request)
			if reader != nil || err != ErrRecoveryTargetUnavailable || strings.Contains(err.Error(), rawFailure) {
				t.Fatalf("returned-file open reader=%v error=%v, want sanitized unavailable", reader, err)
			}
			if returned == nil || returned.closeCalls != 1 {
				t.Fatalf("returned-file close=%v, want exactly one", returned)
			}
			assertRecoveryOwnedResultReadOnlyForTest(t, base)
			if base.closeCalls != 1 || sshCloseCalls != 1 {
				t.Fatalf("returned-file session close calls=%d/%d", base.closeCalls, sshCloseCalls)
			}
		})
	}

	for _, stage := range []string{"file", "sftp", "ssh"} {
		t.Run(stage+" close ambiguity", func(t *testing.T) {
			testCase := newRecoveryOwnedResultReadCaseForTest(t, []byte("published-result-content"))
			base := &recoveryLocalSFTPClient{}
			finalOpens := 0
			closeOrder := make([]string, 0, 3)
			rawFailure := "RAW_RESULT_CLOSE_FAILURE_PRIVATE_HOST_CREDENTIAL_" + stage
			rawErr := errors.New(rawFailure)
			scripted := &recoveryScriptedSFTPClient{base: base}
			scripted.open = func(value string) (recoveryTargetSFTPFile, error) {
				file, err := base.Open(value)
				if err != nil || value != testCase.resultPath {
					return file, err
				}
				finalOpens++
				if finalOpens != 2 {
					return file, nil
				}
				return &recoveryScriptedSFTPFile{base: file, close: func() error {
					closeOrder = append(closeOrder, "file")
					closeErr := file.Close()
					if stage == "file" {
						return rawErr
					}
					return closeErr
				}}, nil
			}
			scripted.close = func() error {
				closeOrder = append(closeOrder, "sftp")
				closeErr := base.Close()
				if stage == "sftp" {
					return rawErr
				}
				return closeErr
			}
			sshCloseCalls := 0
			target := newRecoverySFTPTargetForTest(
				newRecoveryTargetSessionFactoryForTest(
					testCase.fixture.resolver, testCase.fixture.dialer,
					func(*ssh.Client) (recoveryTargetSFTPClient, error) { return scripted, nil },
					func(*ssh.Client) error {
						sshCloseCalls++
						closeOrder = append(closeOrder, "ssh")
						if stage == "ssh" {
							return rawErr
						}
						return nil
					},
				),
				testCase.fixture.codec,
			)
			target.now = func() time.Time { return testCase.fixture.now }
			reader, err := target.OpenOwnedResult(context.Background(), testCase.permit, testCase.request)
			if err != nil {
				t.Fatalf("open %s-close result: %v", stage, err)
			}
			if _, err := io.ReadAll(reader); err != nil {
				t.Fatalf("read %s-close result: %v", stage, err)
			}
			closeErr := reader.Close()
			if closeErr != ErrRecoveryTargetUnavailable || strings.Contains(closeErr.Error(), rawFailure) {
				t.Fatalf("%s close error=%v, want sanitized unavailable", stage, closeErr)
			}
			if secondCloseErr := reader.Close(); secondCloseErr != ErrRecoveryTargetUnavailable {
				t.Fatalf("%s second close error=%v, want stable unavailable", stage, secondCloseErr)
			}
			assertRecoveryOwnedResultReadOnlyForTest(t, base)
			if finalOpens != 2 || base.closeCalls != 1 || sshCloseCalls != 1 ||
				!reflect.DeepEqual(closeOrder, []string{"file", "sftp", "ssh"}) {
				t.Fatalf("%s close final_opens=%d calls=%d/%d order=%v",
					stage, finalOpens, base.closeCalls, sshCloseCalls, closeOrder)
			}
		})
	}

	t.Run("concurrent close unblocks read", func(t *testing.T) {
		testCase := newRecoveryOwnedResultReadCaseForTest(t, []byte("published-result-content"))
		base := &recoveryLocalSFTPClient{}
		finalOpens := 0
		readStarted := make(chan struct{})
		fileClosed := make(chan struct{})
		var startOnce sync.Once
		var closeOnce sync.Once
		scripted := &recoveryScriptedSFTPClient{base: base}
		scripted.open = func(value string) (recoveryTargetSFTPFile, error) {
			file, err := base.Open(value)
			if err != nil || value != testCase.resultPath {
				return file, err
			}
			finalOpens++
			if finalOpens != 2 {
				return file, nil
			}
			return &recoveryScriptedSFTPFile{
				base: file,
				read: func([]byte) (int, error) {
					startOnce.Do(func() { close(readStarted) })
					<-fileClosed
					return 0, errors.New("RAW_BLOCKED_RESULT_READ_CLOSED")
				},
				close: func() error {
					closeOnce.Do(func() { close(fileClosed) })
					return file.Close()
				},
			}, nil
		}
		target := newRecoverySFTPTargetForTest(
			newRecoveryTargetSessionFactoryForTest(
				testCase.fixture.resolver, testCase.fixture.dialer,
				func(*ssh.Client) (recoveryTargetSFTPClient, error) { return scripted, nil },
				func(*ssh.Client) error { return nil },
			),
			testCase.fixture.codec,
		)
		target.now = func() time.Time { return testCase.fixture.now }
		reader, err := target.OpenOwnedResult(context.Background(), testCase.permit, testCase.request)
		if err != nil {
			t.Fatalf("open blocking result read: %v", err)
		}
		readDone := make(chan error, 1)
		go func() {
			_, readErr := reader.Read(make([]byte, 1))
			readDone <- readErr
		}()
		select {
		case <-readStarted:
		case <-time.After(time.Second):
			t.Fatal("blocking result read did not start")
		}
		closeDone := make(chan error, 1)
		go func() { closeDone <- reader.Close() }()
		select {
		case closeErr := <-closeDone:
			if closeErr != ErrRecoveryTargetUnavailable {
				t.Fatalf("concurrent result close error=%v, want unavailable", closeErr)
			}
		case <-time.After(time.Second):
			t.Fatal("result Close did not unblock blocked Read")
		}
		select {
		case readErr := <-readDone:
			if readErr != ErrRecoveryTargetUnavailable {
				t.Fatalf("unblocked result read error=%v, want unavailable", readErr)
			}
		case <-time.After(time.Second):
			t.Fatal("blocked result Read did not return after Close")
		}
		assertRecoveryOwnedResultReadOnlyForTest(t, base)
		if base.closeCalls != 1 {
			t.Fatalf("concurrent result session close calls=%d, want one", base.closeCalls)
		}
	})
}

func TestTargetPurposeSpecificPermitConstructionRejectsCrossPurpose(t *testing.T) {
	now := time.Now().UTC()
	binding := recoveryTargetSessionBindingForTest(t)
	jobID := strings.Repeat("1", 32)
	rootLocatorDigest := binding.RootLocatorDigest
	relativeLocator := recoveryWorkspaceLocatorDirectory + "/" + jobID + "/item.bin"
	pathDigest := mustTargetPathDigest(t, binding.RootID, rootLocatorDigest, relativeLocator)
	observation := TargetObservationPermit{
		SchemaVersion:     1,
		NodeID:            binding.NodeID,
		RootID:            binding.RootID,
		RootLocatorDigest: rootLocatorDigest,
		TargetPathDigest:  pathDigest,
		RootRevision:      binding.RootRevision,
		ExpiresAt:         now.Add(time.Minute),
	}
	observationConstructors := map[TargetPurpose]func(TargetObservationPermit) error{
		TargetPurposePreflight: func(permit TargetObservationPermit) error {
			request := TargetProbeRequest{
				Object: TargetObjectRef{
					RootID: permit.RootID, RootLocatorDigest: permit.RootLocatorDigest,
					TargetPathDigest: permit.TargetPathDigest, PrivateRelativeLocator: relativeLocator,
				},
				SourceRevisionDigest: strings.Repeat("4", sha256DigestLength),
				CapabilityRevision:   "capability-revision-1", PolicyRevision: "policy-revision-1",
			}
			preflightBinding := recoveryTargetPreflightSessionBindingForPermitTest(
				t, observation, request, binding.RootLocator,
			)
			sealed := issueTargetPreflightPermit(permit, preflightBinding, request)
			return sealed.ValidateRequestAt(now, permit, request)
		},
		TargetPurposeVerify: func(permit TargetObservationPermit) error {
			permit = issueTargetVerifyPermit(
				permit, binding, jobID, TargetModeIsolated, RecoveryOperationOverwrite,
				ExpectedTargetIdentity{Kind: ExpectedTargetPresent, Digest: strings.Repeat("5", sha256DigestLength)},
			)
			_, err := NewTargetVerifyPermit(permit, now)
			return err
		},
		TargetPurposeResultRead: func(permit TargetObservationPermit) error {
			request := OpenOwnedResultRequest{
				Object: TargetObjectRef{
					RootID: permit.RootID, RootLocatorDigest: permit.RootLocatorDigest,
					TargetPathDigest: permit.TargetPathDigest, PrivateRelativeLocator: relativeLocator,
				},
				ExpectedBytes: 0, IdentityDigest: strings.Repeat("4", sha256DigestLength),
			}
			authority := targetResultReadAuthority{
				sessionBinding: binding, jobID: jobID, resultSetID: strings.Repeat("2", 32),
				resultID: strings.Repeat("3", 32), publicationRevision: 1,
				resultSetState: ResultSetStateReady, markerBindingDigest: strings.Repeat("5", sha256DigestLength),
				markerCreatorID: "result-reader", markerCreatorFence: 1,
				locatorDigest: strings.Repeat("6", sha256DigestLength), object: request.Object,
				expectedContentDigest: request.IdentityDigest, plaintextDeadline: permit.ExpiresAt,
			}
			permit = issueTargetResultReadPermit(permit, authority, request)
			result, err := NewTargetResultReadPermit(permit, now)
			if err == nil {
				err = result.ValidateRequestAt(now, request)
			}
			return err
		},
	}
	for requiredPurpose, constructor := range observationConstructors {
		for suppliedPurpose := range observationConstructors {
			permit := observation
			permit.Purpose = suppliedPurpose
			err := constructor(permit)
			if suppliedPurpose == requiredPurpose && err != nil {
				t.Fatalf("construct %q observation permit error = %v", requiredPurpose, err)
			}
			if suppliedPurpose != requiredPurpose && !errorsIsTargetPermit(err) {
				t.Fatalf("construct %q from %q error = %v, want ErrInvalidTargetPermit", requiredPurpose, suppliedPurpose, err)
			}
		}
	}

	mutation := TargetMutationPermit{
		SchemaVersion:          1,
		NodeID:                 7,
		RootID:                 "root-a",
		RootLocatorDigest:      rootLocatorDigest,
		TargetPathDigest:       pathDigest,
		RootRevision:           "root-revision-1",
		ExpiresAt:              now.Add(time.Minute),
		UseLatchID:             RecoverySchemaUseLatchID,
		JobID:                  strings.Repeat("1", 32),
		AttemptID:              strings.Repeat("2", 32),
		NodeLeaseID:            strings.Repeat("3", 32),
		AttemptFence:           1,
		NodeFence:              2,
		ExpectedTargetRevision: "target-revision-1",
	}
	mutationConstructors := map[TargetPurpose]func(TargetMutationPermit) error{
		TargetPurposeWrite: func(permit TargetMutationPermit) error {
			_, err := NewTargetWritePermit(permit, now)
			return err
		},
	}
	for requiredPurpose, constructor := range mutationConstructors {
		for suppliedPurpose := range mutationConstructors {
			permit := mutation
			permit.Purpose = suppliedPurpose
			permit = issuedTargetMutationPermitForTest(permit)
			err := constructor(permit)
			if suppliedPurpose == requiredPurpose && err != nil {
				t.Fatalf("construct %q mutation permit error = %v", requiredPurpose, err)
			}
			if suppliedPurpose != requiredPurpose && !errorsIsTargetPermit(err) {
				t.Fatalf("construct %q from %q error = %v, want ErrInvalidTargetPermit", requiredPurpose, suppliedPurpose, err)
			}
		}
	}
}

func TestTargetResultReadPermitRequiresResolverBoundPublishedProof(t *testing.T) {
	now := time.Now().UTC()
	binding := recoveryTargetSessionBindingForTest(t)
	jobID := strings.Repeat("1", 32)
	object := TargetObjectRef{
		RootID: binding.RootID, RootLocatorDigest: binding.RootLocatorDigest,
		PrivateRelativeLocator: recoveryWorkspaceLocatorDirectory + "/" + jobID + "/result.bin",
	}
	object.TargetPathDigest = mustTargetPathDigest(
		t, object.RootID, object.RootLocatorDigest, object.PrivateRelativeLocator,
	)
	structural := TargetObservationPermit{
		SchemaVersion: 1, NodeID: binding.NodeID, Purpose: TargetPurposeResultRead,
		RootID: object.RootID, RootLocatorDigest: object.RootLocatorDigest,
		TargetPathDigest: object.TargetPathDigest, RootRevision: binding.RootRevision,
		ExpiresAt: now.Add(time.Minute),
	}

	if _, err := NewTargetResultReadPermit(structural, now); err != ErrInvalidTargetPermit {
		t.Fatalf("structural result-read permit error=%v, want exact ErrInvalidTargetPermit", err)
	}

	request := OpenOwnedResultRequest{
		Object: object, ExpectedBytes: 17, IdentityDigest: strings.Repeat("4", sha256DigestLength),
	}
	authority := targetResultReadAuthority{
		sessionBinding: binding, jobID: jobID, resultSetID: strings.Repeat("2", 32),
		resultID: strings.Repeat("3", 32), publicationRevision: 5,
		resultSetState: ResultSetStateReady, markerBindingDigest: strings.Repeat("5", sha256DigestLength),
		markerCreatorID: "result-reader", markerCreatorFence: 7,
		locatorDigest: strings.Repeat("6", sha256DigestLength), object: object,
		expectedBytes: request.ExpectedBytes, expectedContentDigest: request.IdentityDigest,
		plaintextDeadline: now.Add(time.Minute),
	}
	sealed := issueTargetResultReadPermit(structural, authority, request)
	permit, err := NewTargetResultReadPermit(sealed, now)
	if err != nil || permit.ValidateRequestAt(now, request) != nil {
		t.Fatalf("sealed result-read permit=%+v error=%v", permit, err)
	}

	mutations := []struct {
		name   string
		mutate func(*TargetObservationPermit)
	}{
		{name: "publication revision", mutate: func(value *TargetObservationPermit) {
			value.resultReadProof.authority.publicationRevision++
		}},
		{name: "cleanup fence", mutate: func(value *TargetObservationPermit) {
			value.resultReadProof.authority.cleanupFence = 1
		}},
		{name: "marker binding", mutate: func(value *TargetObservationPermit) {
			value.resultReadProof.authority.markerBindingDigest = strings.Repeat("a", sha256DigestLength)
		}},
		{name: "marker creator fence", mutate: func(value *TargetObservationPermit) {
			value.resultReadProof.authority.markerCreatorFence++
		}},
		{name: "result locator", mutate: func(value *TargetObservationPermit) {
			value.resultReadProof.authority.locatorDigest = strings.Repeat("b", sha256DigestLength)
		}},
		{name: "expected size", mutate: func(value *TargetObservationPermit) {
			value.resultReadProof.request.ExpectedBytes++
		}},
		{name: "content digest", mutate: func(value *TargetObservationPermit) {
			value.resultReadProof.authority.expectedContentDigest = strings.Repeat("c", sha256DigestLength)
		}},
		{name: "plaintext deadline", mutate: func(value *TargetObservationPermit) {
			value.resultReadProof.authority.plaintextDeadline = value.resultReadProof.authority.plaintextDeadline.Add(time.Second)
		}},
		{name: "session credential", mutate: func(value *TargetObservationPermit) {
			value.resultReadProof.authority.sessionBinding.CredentialRevision = "substituted-credential"
		}},
		{name: "object", mutate: func(value *TargetObservationPermit) {
			value.resultReadProof.request.Object.PrivateRelativeLocator += ".substituted"
		}},
	}
	for _, testCase := range mutations {
		t.Run(testCase.name, func(t *testing.T) {
			mutated := sealed
			proof := *sealed.resultReadProof
			mutated.resultReadProof = &proof
			testCase.mutate(&mutated)
			if _, err := NewTargetResultReadPermit(mutated, now); err != ErrInvalidTargetPermit {
				t.Fatalf("substituted result-read permit error=%v, want exact invalid", err)
			}
		})
	}

	encoded, err := json.Marshal(sealed)
	if err != nil {
		t.Fatalf("marshal result-read permit: %v", err)
	}
	for _, forbidden := range []string{
		binding.RootLocator, binding.CredentialRevision, object.PrivateRelativeLocator,
		authority.markerBindingDigest, authority.locatorDigest, authority.expectedContentDigest,
	} {
		if forbidden != "" && bytes.Contains(encoded, []byte(forbidden)) {
			t.Fatalf("result-read permit JSON leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestTargetObservationIssuersEraseCrossPurposePrivateProofs(t *testing.T) {
	now := time.Now().UTC()
	binding := recoveryTargetSessionBindingForTest(t)
	jobID := strings.Repeat("1", 32)
	object := TargetObjectRef{
		RootID: binding.RootID, RootLocatorDigest: binding.RootLocatorDigest,
		PrivateRelativeLocator: recoveryWorkspaceLocatorDirectory + "/" + jobID + "/result.bin",
	}
	object.TargetPathDigest = mustTargetPathDigest(
		t, object.RootID, object.RootLocatorDigest, object.PrivateRelativeLocator,
	)
	request := OpenOwnedResultRequest{
		Object: object, ExpectedBytes: 1, IdentityDigest: strings.Repeat("4", sha256DigestLength),
	}
	authority := targetResultReadAuthority{
		sessionBinding: binding, jobID: jobID, resultSetID: strings.Repeat("2", 32),
		resultID: strings.Repeat("3", 32), publicationRevision: 1,
		resultSetState: ResultSetStateReady, markerBindingDigest: strings.Repeat("5", sha256DigestLength),
		markerCreatorID: "result-reader", markerCreatorFence: 1,
		locatorDigest: strings.Repeat("6", sha256DigestLength), object: object,
		expectedBytes: request.ExpectedBytes, expectedContentDigest: request.IdentityDigest,
		plaintextDeadline: now.Add(time.Minute),
	}
	resultObservation := issueTargetResultReadPermit(TargetObservationPermit{
		SchemaVersion: 1, NodeID: binding.NodeID, Purpose: TargetPurposeResultRead,
		RootID: object.RootID, RootLocatorDigest: object.RootLocatorDigest,
		TargetPathDigest: object.TargetPathDigest, RootRevision: binding.RootRevision,
		ExpiresAt: now.Add(time.Minute),
	}, authority, request)
	if resultObservation.resultReadProof == nil {
		t.Fatal("result-read issuer did not create its private proof")
	}

	verifyObservation := resultObservation
	verifyObservation.Purpose = TargetPurposeVerify
	verifyObservation = issueTargetVerifyPermit(
		verifyObservation, binding, jobID, TargetModeIsolated, RecoveryOperationOverwrite,
		ExpectedTargetIdentity{Kind: ExpectedTargetPresent, Digest: strings.Repeat("7", sha256DigestLength)},
	)
	if verifyObservation.proof == nil || verifyObservation.resultReadProof != nil {
		t.Fatalf("verify issuer retained cross-purpose proof: %+v", verifyObservation)
	}
}

func TestTargetWritePermitRejectsUncommittedRawMutationAuthority(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	rootLocatorDigest := strings.Repeat("a", sha256DigestLength)
	privateRelativeLocator := "jobs/uncommitted-authority"
	pathDigest := mustTargetPathDigest(t, "root-a", rootLocatorDigest, privateRelativeLocator)

	// A structurally valid literal has not passed the durable first-write
	// transaction, so it cannot prove the committed latch or live fences.
	raw := TargetMutationPermit{
		SchemaVersion:          1,
		NodeID:                 7,
		Purpose:                TargetPurposeWrite,
		RootID:                 "root-a",
		RootLocatorDigest:      rootLocatorDigest,
		TargetPathDigest:       pathDigest,
		RootRevision:           "root-revision-1",
		ExpiresAt:              now.Add(time.Minute),
		UseLatchID:             RecoverySchemaUseLatchID,
		JobID:                  strings.Repeat("1", 32),
		AttemptID:              strings.Repeat("2", 32),
		NodeLeaseID:            strings.Repeat("3", 32),
		AttemptFence:           1,
		NodeFence:              2,
		ExpectedTargetRevision: "target-revision-1",
	}

	if _, err := NewTargetWritePermit(raw, now); !errorsIsTargetPermit(err) {
		t.Fatalf("uncommitted raw mutation permit error = %v, want ErrInvalidTargetPermit", err)
	}
}

func TestTargetContractsBindPermitsAndHidePrivateLocator(t *testing.T) {
	now := time.Now().UTC()
	rootLocatorDigest := strings.Repeat("a", sha256DigestLength)
	relativeLocator := "recognizable-private-relative-locator"
	pathDigest := mustTargetPathDigest(t, "root-a", rootLocatorDigest, relativeLocator)
	object := TargetObjectRef{
		RootID: "root-a", RootLocatorDigest: rootLocatorDigest, TargetPathDigest: pathDigest,
		PrivateRelativeLocator: relativeLocator,
	}
	encoded, err := json.Marshal(object)
	if err != nil {
		t.Fatalf("marshal target object: %v", err)
	}
	for _, forbidden := range []string{object.PrivateRelativeLocator, object.RootLocatorDigest, object.TargetPathDigest} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("private target binding %q leaked through JSON: %s", forbidden, encoded)
		}
	}

	observation := TargetObservationPermit{
		SchemaVersion: 1, NodeID: 7, Purpose: TargetPurposePreflight,
		RootID: object.RootID, RootLocatorDigest: object.RootLocatorDigest, TargetPathDigest: object.TargetPathDigest,
		RootRevision: "root-revision-1", ExpiresAt: now.Add(time.Minute),
	}
	if err := observation.ValidateAt(now); err != nil {
		t.Fatalf("valid observation permit error = %v", err)
	}
	if err := observation.ValidateAt(now.Add(time.Minute)); !errorsIsTargetPermit(err) {
		t.Fatalf("expiry equality error = %v, want closed permit rejection", err)
	}

	mutation := TargetMutationPermit{
		SchemaVersion: 1, NodeID: 7, Purpose: TargetPurposeWrite,
		RootID: object.RootID, RootLocatorDigest: object.RootLocatorDigest, TargetPathDigest: object.TargetPathDigest,
		RootRevision: "root-revision-1", ExpiresAt: now.Add(time.Minute),
		UseLatchID: RecoverySchemaUseLatchID,
		JobID:      strings.Repeat("1", 32), AttemptID: strings.Repeat("2", 32),
		NodeLeaseID: strings.Repeat("3", 32), AttemptFence: 1, NodeFence: 2,
		ExpectedTargetRevision: "target-revision-1",
	}
	mutation = issuedTargetMutationPermitForTest(mutation)
	if err := mutation.ValidateAt(now); err != nil {
		t.Fatalf("valid mutation permit error = %v", err)
	}
	mutation.UseLatchID = ""
	if err := mutation.ValidateAt(now); !errorsIsTargetPermit(err) {
		t.Fatalf("missing latch error = %v, want closed permit rejection", err)
	}
}

func TestTargetPermitsRequireExactObjectAndFrozenJobBinding(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	rootLocatorA := "/srv/FAKE_TARGET_A_ROOT_FOR_TEST_ONLY"
	rootDigestA, err := settings.RecoveryTargetRootLocatorDigest(7, "root-a", rootLocatorA)
	if err != nil {
		t.Fatalf("target A root locator digest: %v", err)
	}
	objectA := TargetObjectRef{
		RootID: "root-a", RootLocatorDigest: rootDigestA,
		PrivateRelativeLocator: "jobs/job-a",
	}
	objectA.TargetPathDigest = mustTargetPathDigest(
		t, objectA.RootID, objectA.RootLocatorDigest, objectA.PrivateRelativeLocator,
	)
	rootLocatorB := "/srv/FAKE_TARGET_B_ROOT_FOR_TEST_ONLY"
	rootDigestB, err := settings.RecoveryTargetRootLocatorDigest(7, "root-b", rootLocatorB)
	if err != nil {
		t.Fatalf("target B root locator digest: %v", err)
	}
	objectB := TargetObjectRef{
		RootID: "root-b", RootLocatorDigest: rootDigestB,
		PrivateRelativeLocator: "jobs/job-b",
	}
	objectB.TargetPathDigest = mustTargetPathDigest(
		t, objectB.RootID, objectB.RootLocatorDigest, objectB.PrivateRelativeLocator,
	)

	observation := TargetObservationPermit{
		SchemaVersion: 1, NodeID: 7, Purpose: TargetPurposePreflight,
		RootID: objectA.RootID, RootLocatorDigest: objectA.RootLocatorDigest,
		TargetPathDigest: objectA.TargetPathDigest,
		RootRevision:     "root-revision-1", ExpiresAt: now.Add(time.Minute),
	}
	unboundObservation := observation
	unboundObservation.RootID = ""
	unboundObservation.RootLocatorDigest = ""
	unboundObservation.TargetPathDigest = ""
	if err := unboundObservation.ValidateAt(now); !errorsIsTargetPermit(err) {
		t.Fatalf("unbound observation permit error = %v, want ErrInvalidTargetPermit", err)
	}
	probeRequest := TargetProbeRequest{
		Object: objectA, SourceRevisionDigest: strings.Repeat("4", sha256DigestLength),
		CapabilityRevision: "capability-revision-1", PolicyRevision: "policy-revision-1",
	}
	preflightBinding := recoveryTargetPreflightSessionBindingForPermitTest(
		t, observation, probeRequest, rootLocatorA,
	)
	preflight := issueTargetPreflightPermit(observation, preflightBinding, probeRequest)
	if err := preflight.ValidateRequestAt(now, observation, probeRequest); err != nil {
		t.Fatalf("sealed target preflight permit error = %v", err)
	}
	if err := preflight.ValidateObjectAt(now, objectA); err != nil {
		t.Fatalf("target-A preflight permit rejected target A: %v", err)
	}
	if err := preflight.ValidateObjectAt(now, objectB); !errorsIsTargetPermit(err) {
		t.Fatalf("target-A preflight permit accepted target B: %v", err)
	}

	jobA := validFrozenJobBindingForTarget(t, now, objectA.RootID, objectA.RootLocatorDigest, objectA.PrivateRelativeLocator)
	jobB := validFrozenJobBindingForTarget(t, now, objectB.RootID, objectB.RootLocatorDigest, objectB.PrivateRelativeLocator)
	mutation := TargetMutationPermit{
		SchemaVersion: 1, NodeID: 7, Purpose: TargetPurposeWrite,
		RootID: objectA.RootID, RootLocatorDigest: objectA.RootLocatorDigest,
		TargetPathDigest: objectA.TargetPathDigest,
		RootRevision:     jobA.Plan.Target.RootRevision, ExpiresAt: now.Add(time.Minute),
		UseLatchID: RecoverySchemaUseLatchID,
		JobID:      strings.Repeat("1", 32), AttemptID: strings.Repeat("2", 32),
		NodeLeaseID: strings.Repeat("3", 32), AttemptFence: 1, NodeFence: 2,
		ExpectedTargetRevision: jobA.Preflight.TargetRevision,
	}
	mutation = issuedTargetMutationPermitForTest(mutation)
	unboundMutation := mutation
	unboundMutation.RootID = ""
	unboundMutation.RootLocatorDigest = ""
	unboundMutation.TargetPathDigest = ""
	if err := unboundMutation.ValidateAt(now); !errorsIsTargetPermit(err) {
		t.Fatalf("unbound mutation permit error = %v, want ErrInvalidTargetPermit", err)
	}
	write, err := NewTargetWritePermit(mutation, now)
	if err != nil {
		t.Fatalf("NewTargetWritePermit() error = %v", err)
	}
	if err := write.ValidateObjectAt(now, objectA); err != nil {
		t.Fatalf("target-A write permit rejected target A: %v", err)
	}
	if err := write.ValidateObjectAt(now, objectB); !errorsIsTargetPermit(err) {
		t.Fatalf("target-A write permit accepted target B: %v", err)
	}
	if err := write.ValidateFrozenJobAt(now, jobA); err != nil {
		t.Fatalf("target-A write permit rejected frozen job A: %v", err)
	}
	if err := write.ValidateFrozenJobAt(now, jobB); !errorsIsTargetPermit(err) {
		t.Fatalf("target-A write permit accepted frozen job B: %v", err)
	}
}

func errorsIsTargetPermit(err error) bool {
	return err == ErrInvalidTargetPermit
}

func issuedTargetMutationPermitForTest(permit TargetMutationPermit) TargetMutationPermit {
	return issueTargetMutationPermit(permit, func(time.Time) error { return nil })
}

func recoveryTargetPreflightSessionBindingForPermitTest(
	t *testing.T,
	permit TargetObservationPermit,
	request TargetProbeRequest,
	rootLocator string,
) recoveryTargetPreflightSessionBinding {
	t.Helper()
	binding := recoveryTargetPreflightSessionBinding{
		planID: strings.Repeat("7", 32), planBindingDigest: strings.Repeat("8", sha256DigestLength),
		planTransitionRevision: 1, targetMode: TargetModeIsolated,
		nodeID: permit.NodeID, nodeRevision: "node-revision-1", credentialRevision: "credential-revision-1",
		rootID: permit.RootID, rootLocator: rootLocator, rootLocatorDigest: permit.RootLocatorDigest,
		rootRevision: permit.RootRevision, filesystemRevision: "filesystem-revision-1",
		targetPathDigest:       permit.TargetPathDigest,
		privateRelativeLocator: request.Object.PrivateRelativeLocator,
		targetRevision:         "target-revision-1", preflightRevision: "preflight-revision-1",
	}
	binding.bindingDigest = binding.digest()
	if !binding.valid() {
		t.Fatal("invalid target preflight session binding fixture")
	}
	return binding
}

func recoveryTargetSessionBindingForTest(t *testing.T) recoveryTargetSessionBinding {
	t.Helper()
	return recoveryTargetSessionBindingForLocatorTest(
		t, "/srv/FAKE_RECOVERY_SESSION_ROOT_FOR_TEST_ONLY",
	)
}

func recoveryTargetSessionBindingForLocatorTest(
	t *testing.T,
	locator string,
) recoveryTargetSessionBinding {
	t.Helper()
	locatorDigest, err := settings.RecoveryTargetRootLocatorDigest(7, "root-a", locator)
	if err != nil {
		t.Fatalf("construct session root digest: %v", err)
	}
	binding, err := newRecoveryTargetSessionBinding(model.BackupAssetRecoveryPlan{
		ID: strings.Repeat("9", 32), State: string(PlanStateExecuted),
		BindingDigest: strings.Repeat("8", sha256DigestLength), TargetNodeID: 7,
		TargetBaseRevision: "node-revision-1", CredentialScopeRevision: "credential-revision-1",
		TargetRootID: "root-a", EncryptedTargetRootLocator: locator,
		RootLocatorDigest: locatorDigest, RootRevision: "root-revision-1",
	})
	if err != nil {
		t.Fatalf("construct target session binding: %v", err)
	}
	return binding
}

func recoveryWorkspaceMarkerAuthorityWithSessionForTest(
	t *testing.T,
	now time.Time,
	material backupasset.DomainKeyMaterial,
	binding recoveryTargetSessionBinding,
) (TargetWritePermit, CreateOwnedJobDirRequest, TargetCleanupPermit, ValidateOwnedJobDirRequest) {
	t.Helper()
	jobID := strings.Repeat("1", 32)
	object := TargetObjectRef{
		RootID: binding.RootID, RootLocatorDigest: binding.RootLocatorDigest,
		PrivateRelativeLocator: recoveryWorkspaceLocatorDirectory + "/" + jobID,
	}
	object.TargetPathDigest = mustTargetPathDigest(
		t, object.RootID, object.RootLocatorDigest, object.PrivateRelativeLocator,
	)
	markerBinding := recoveryWorkspaceMarkerBindingDigest(
		material, jobID, object.RootID, binding.RootRevision, object.PrivateRelativeLocator,
		RecoveryWorkerClaim{WorkerID: "session-marker-creator", AttemptFence: 17},
	)
	mutation := issueTargetMutationPermit(TargetMutationPermit{
		SchemaVersion: 1, NodeID: binding.NodeID, Purpose: TargetPurposeWrite,
		RootID: object.RootID, RootLocatorDigest: object.RootLocatorDigest,
		TargetPathDigest: object.TargetPathDigest, RootRevision: binding.RootRevision,
		ExpiresAt: now.Add(time.Minute), UseLatchID: RecoverySchemaUseLatchID,
		JobID: jobID, AttemptID: strings.Repeat("2", 32), NodeLeaseID: strings.Repeat("3", 32),
		AttemptFence: 19, NodeFence: 23, ExpectedTargetRevision: "target-revision-1",
	}, func(time.Time) error { return nil }, binding)
	writePermit, err := NewTargetWritePermit(mutation, now)
	if err != nil {
		t.Fatalf("construct session marker write permit: %v", err)
	}
	createRequest := CreateOwnedJobDirRequest{
		Object: object, MarkerBindingDigest: markerBinding,
		MarkerCreatorID: "session-marker-creator", MarkerCreatorFence: 17,
	}
	cleanupPermit := issueTargetCleanupPermit(TargetCleanupPermit{
		SchemaVersion: 1, Purpose: TargetPurposeCleanup,
		Operation: TargetCleanupValidateOwnedJobDir, ResourceKind: CleanupResourceWorkspace,
		ResourceID: jobID, JobID: jobID, CleanupOwner: "session-cleanup-owner",
		CleanupFence: 29, CleanupAttempt: 31, NodeID: binding.NodeID,
		NodeLeaseID: strings.Repeat("4", 32), NodeFence: 37,
		RootID: object.RootID, RootLocatorDigest: object.RootLocatorDigest,
		TargetPathDigest: object.TargetPathDigest, RootRevision: binding.RootRevision,
		MarkerBindingDigest: markerBinding, MarkerCreatorID: createRequest.MarkerCreatorID,
		MarkerCreatorFence: createRequest.MarkerCreatorFence,
		UseLatchID:         RecoverySchemaUseLatchID, ExpiresAt: now.Add(time.Minute),
	}, binding)
	cleanupRequest := ValidateOwnedJobDirRequest{
		Object: object, MarkerBindingDigest: markerBinding,
		MarkerCreatorID: createRequest.MarkerCreatorID, MarkerCreatorFence: createRequest.MarkerCreatorFence,
	}
	return writePermit, createRequest, cleanupPermit, cleanupRequest
}

func newRecoveryPipeSFTPOpenerForTest(t *testing.T) recoveryTargetSFTPOpener {
	t.Helper()
	return func(*ssh.Client) (recoveryTargetSFTPClient, error) {
		clientConnection, serverConnection := net.Pipe()
		server, err := sftp.NewServer(serverConnection)
		if err != nil {
			_ = clientConnection.Close()
			_ = serverConnection.Close()
			return nil, err
		}
		done := make(chan struct{})
		go func() {
			_ = server.Serve()
			close(done)
		}()
		client, err := sftp.NewClientPipe(clientConnection, clientConnection)
		if err != nil {
			_ = server.Close()
			_ = clientConnection.Close()
			return nil, err
		}
		return &recoveryPipeSFTPClient{
			recoverySFTPClient: &recoverySFTPClient{client: client}, server: server, done: done,
		}, nil
	}
}

func TestRecoverySFTPTargetReconciliationClassificationMatrix(t *testing.T) {
	t.Run("empty registered root is a real complete scan", func(t *testing.T) {
		for _, testCase := range []struct {
			name                 string
			expectedByComponent  func(*recoveryLocalSFTPTargetFixture) map[string]targetReconciliationExpected
			wantCounts           RecoveryReconciliationCounts
			wantFindingCategory  RecoveryReconciliationCategory
			wantFindingEntryKind TargetEntryKind
		}{
			{name: "zero expected", wantCounts: RecoveryReconciliationCounts{}},
			{
				name: "expected workspace is missing",
				expectedByComponent: func(fixture *recoveryLocalSFTPTargetFixture) map[string]targetReconciliationExpected {
					return map[string]targetReconciliationExpected{
						fixture.writePermit.permit.JobID: recoveryReconciliationExpectedWorkspaceForTest(fixture),
					}
				},
				wantCounts:           RecoveryReconciliationCounts{KnownDrift: 1},
				wantFindingCategory:  RecoveryReconciliationKnownDrift,
				wantFindingEntryKind: TargetEntryMissing,
			},
		} {
			t.Run(testCase.name, func(t *testing.T) {
				fixture := newRecoveryLocalSFTPTargetFixture(t)
				var expected map[string]targetReconciliationExpected
				if testCase.expectedByComponent != nil {
					expected = testCase.expectedByComponent(fixture)
				}
				permit, request := recoveryReconciliationPermitForTargetTest(t, fixture, expected)
				fixture.target.now = func() time.Time { return fixture.now }

				page, err := fixture.target.ScanRecoveryRoot(context.Background(), permit, request)
				if err != nil || !page.Complete || page.NextCursor != "" || page.Counts != testCase.wantCounts {
					t.Fatalf("empty-root reconciliation page=%+v error=%v", page, err)
				}
				if testCase.wantFindingCategory == "" {
					if len(page.Findings) != 0 {
						t.Fatalf("empty-root reconciliation findings=%+v, want none", page.Findings)
					}
				} else if len(page.Findings) != 1 || page.Findings[0].Category != testCase.wantFindingCategory ||
					page.Findings[0].EntryKind != testCase.wantFindingEntryKind ||
					page.Findings[0].JobID != fixture.writePermit.permit.JobID {
					t.Fatalf("missing expected workspace finding=%+v", page.Findings)
				}
				if len(fixture.clients) != 1 || fixture.clients[0].openCalls != 0 || fixture.clients[0].closeCalls != 1 {
					t.Fatalf("empty-root scan resource state was not exact")
				}
				assertRecoveryReconciliationReadOnlyForTest(t, fixture.clients[0])
			})
		}
	})

	t.Run("R64 cursor remains fail closed", func(t *testing.T) {
		fixture := newRecoveryLocalSFTPTargetFixture(t)
		permit, request := recoveryReconciliationPermitForTargetTest(t, fixture, nil)
		permit.Cursor = "opaque-r64-cursor-not-yet-supported"
		permit.proof.bindingDigest = targetReconciliationPermitBindingDigest(
			permit.proof.auditTokenKey, permit.proof.auditKeyVersion, permit,
			permit.proof.sessionBinding.bindingDigest,
		)
		if err := permit.ValidateRequestAt(fixture.now, request); err != nil {
			t.Fatalf("valid sealed future-cursor fixture: %v", err)
		}
		fixture.target.now = func() time.Time { return fixture.now }
		page, err := fixture.target.ScanRecoveryRoot(context.Background(), permit, request)
		if err != nil || page.Complete || page.NextCursor != "" ||
			page.Counts != (RecoveryReconciliationCounts{ScanIncomplete: 1}) ||
			len(page.Findings) != 1 || page.Findings[0].Category != RecoveryReconciliationScanIncomplete ||
			fixture.resolver.calls != 0 || len(fixture.clients) != 0 {
			t.Fatalf("unimplemented cursor did not fail closed before target dependencies")
		}
		assertRecoveryReconciliationFingerprintForTest(t, page.Findings[0].Fingerprint)
	})

	t.Run("known healthy final workspace", func(t *testing.T) {
		fixture := newRecoveryLocalSFTPTargetFixture(t)
		fixture.create(t)
		expected := recoveryReconciliationExpectedWorkspaceForTest(fixture)
		permit, request := recoveryReconciliationPermitForTargetTest(t, fixture, map[string]targetReconciliationExpected{
			fixture.writePermit.permit.JobID: expected,
		})
		fixture.target.now = func() time.Time { return fixture.now }

		page, err := fixture.target.ScanRecoveryRoot(context.Background(), permit, request)
		if err != nil || !page.Complete || page.NextCursor != "" ||
			page.Counts != (RecoveryReconciliationCounts{Scanned: 1, KnownHealthy: 1}) ||
			len(page.Findings) != 0 {
			t.Fatalf("healthy reconciliation page=%+v error=%v", page, err)
		}
		assertRecoveryReconciliationReadOnlyForTest(t, fixture.clients[len(fixture.clients)-1])
	})

	t.Run("known healthy delete-started cleanup artifacts", func(t *testing.T) {
		fixture := newRecoveryLocalSFTPTargetFixture(t)
		fixture.create(t)
		jobsPath, jobPath, markerPath := fixture.paths()
		marker, err := os.ReadFile(markerPath)
		if err != nil {
			t.Fatalf("read cleanup reconciliation marker fixture: %v", err)
		}
		baseExpected := recoveryReconciliationExpectedWorkspaceForTest(fixture)
		targetPathDigest := mustTargetPathDigest(
			t, fixture.binding.RootID, fixture.binding.RootLocatorDigest,
			recoveryWorkspaceLocatorDirectory+"/"+baseExpected.jobID,
		)
		captured, verified := recoveryOwnedCleanupComponents(
			baseExpected.jobID, fixture.binding.RootID, fixture.binding.RootRevision, targetPathDigest,
			baseExpected.markerBindingDigest, baseExpected.markerCreatorID, baseExpected.markerCreatorFence,
		)
		if err := os.Rename(jobPath, filepath.Join(jobsPath, captured)); err != nil {
			t.Fatalf("prepare captured reconciliation workspace: %v", err)
		}
		markerDigest := sha256.Sum256(marker)
		verifiedDocument, err := encodeRecoveryOwnedCleanupArtifactDocument(
			recoveryOwnedCleanupArtifactBody{
				SchemaVersion: 1, KeyVersion: fixture.material.Version, JobID: baseExpected.jobID,
				RootID: fixture.binding.RootID, RootRevision: fixture.binding.RootRevision,
				WorkspaceLocator:    recoveryWorkspaceLocatorDirectory + "/" + baseExpected.jobID,
				MarkerBindingDigest: baseExpected.markerBindingDigest,
				MarkerCreatorID:     baseExpected.markerCreatorID,
				MarkerCreatorFence:  baseExpected.markerCreatorFence,
				MarkerDigest:        hex.EncodeToString(markerDigest[:]), CapturedComponent: captured,
			},
			fixture.material.Key, recoveryOwnedCleanupVerifiedDomain,
		)
		if err != nil {
			t.Fatalf("encode cleanup reconciliation artifact fixture: %v", err)
		}
		if err := os.WriteFile(filepath.Join(jobsPath, verified), verifiedDocument, 0o600); err != nil {
			t.Fatalf("prepare cleanup reconciliation artifact: %v", err)
		}
		finalExpected := baseExpected
		finalExpected.entryKind = TargetEntryMissing
		finalExpected.remoteState = recoveryReconciliationRemoteAbsent
		capturedExpected := baseExpected
		capturedExpected.remoteState = recoveryReconciliationRemoteDeleteStarted
		verifiedExpected := capturedExpected
		verifiedExpected.entryKind = TargetEntryRegular
		permit, request := recoveryReconciliationPermitForTargetTest(t, fixture, map[string]targetReconciliationExpected{
			baseExpected.jobID: finalExpected,
			captured:           capturedExpected,
			verified:           verifiedExpected,
		})
		fixture.target.now = func() time.Time { return fixture.now }

		page, scanErr := fixture.target.ScanRecoveryRoot(context.Background(), permit, request)
		if scanErr != nil || !page.Complete || page.NextCursor != "" ||
			page.Counts != (RecoveryReconciliationCounts{Scanned: 2, KnownHealthy: 2}) ||
			len(page.Findings) != 0 {
			t.Fatalf("healthy cleanup reconciliation page=%+v error=%v", page, scanErr)
		}
		assertRecoveryReconciliationReadOnlyForTest(t, fixture.clients[len(fixture.clients)-1])
	})

	for _, testCase := range []struct {
		name    string
		prepare func(*testing.T, *recoveryLocalSFTPTargetFixture) targetReconciliationExpected
	}{
		{
			name: "known token with kind drift",
			prepare: func(t *testing.T, fixture *recoveryLocalSFTPTargetFixture) targetReconciliationExpected {
				jobsPath, jobPath, _ := fixture.paths()
				if err := os.Mkdir(jobsPath, 0o700); err != nil {
					t.Fatalf("prepare reconciliation jobs directory: %v", err)
				}
				if err := os.WriteFile(jobPath, []byte("FAKE_R63_KIND_DRIFT_FOR_TEST_ONLY"), 0o600); err != nil {
					t.Fatalf("prepare reconciliation kind drift: %v", err)
				}
				return recoveryReconciliationExpectedWorkspaceForTest(fixture)
			},
		},
		{
			name: "known token with marker drift",
			prepare: func(t *testing.T, fixture *recoveryLocalSFTPTargetFixture) targetReconciliationExpected {
				fixture.create(t)
				_, _, markerPath := fixture.paths()
				if err := os.WriteFile(markerPath, []byte(`{"schema_version":1}`), 0o600); err != nil {
					t.Fatalf("prepare reconciliation marker drift: %v", err)
				}
				return recoveryReconciliationExpectedWorkspaceForTest(fixture)
			},
		},
		{
			name: "known token with phase drift",
			prepare: func(t *testing.T, fixture *recoveryLocalSFTPTargetFixture) targetReconciliationExpected {
				fixture.create(t)
				expected := recoveryReconciliationExpectedWorkspaceForTest(fixture)
				expected.remoteState = recoveryReconciliationRemoteDeleteStarted
				return expected
			},
		},
		{
			name: "known token symlink is not followed",
			prepare: func(t *testing.T, fixture *recoveryLocalSFTPTargetFixture) targetReconciliationExpected {
				jobsPath, jobPath, _ := fixture.paths()
				if err := os.Mkdir(jobsPath, 0o700); err != nil {
					t.Fatalf("prepare reconciliation jobs directory: %v", err)
				}
				if err := os.Symlink(fixture.root, jobPath); err != nil {
					t.Fatalf("prepare reconciliation symlink: %v", err)
				}
				return recoveryReconciliationExpectedWorkspaceForTest(fixture)
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newRecoveryLocalSFTPTargetFixture(t)
			expected := testCase.prepare(t, fixture)
			permit, request := recoveryReconciliationPermitForTargetTest(t, fixture, map[string]targetReconciliationExpected{
				fixture.writePermit.permit.JobID: expected,
			})
			fixture.target.now = func() time.Time { return fixture.now }

			page, err := fixture.target.ScanRecoveryRoot(context.Background(), permit, request)
			if err != nil || !page.Complete || page.Counts.Scanned != 1 || page.Counts.KnownDrift != 1 ||
				len(page.Findings) != 1 || page.Findings[0].Category != RecoveryReconciliationKnownDrift ||
				page.Findings[0].JobID != expected.jobID {
				t.Fatalf("known-drift reconciliation page=%+v error=%v", page, err)
			}
			assertRecoveryReconciliationFingerprintForTest(t, page.Findings[0].Fingerprint)
			client := fixture.clients[len(fixture.clients)-1]
			assertRecoveryReconciliationReadOnlyForTest(t, client)
			if strings.Contains(testCase.name, "symlink") && client.readLinkCalls != 0 {
				t.Fatalf("reconciliation followed a direct-child symlink: read_link=%d", client.readLinkCalls)
			}
		})
	}

	t.Run("historical key authenticates DB unmatched workspace", func(t *testing.T) {
		fixture := newRecoveryLocalSFTPTargetFixture(t)
		historical := recoveryWorkspaceMarkerMaterialForTest(1, strings.Repeat("h", sha256.Size))
		active := recoveryWorkspaceMarkerMaterialForTest(2, strings.Repeat("a", sha256.Size))
		fixture.codec = newRecoveryWorkspaceMarkerCodec(
			&recoveryWorkspaceMarkerKeySourceForTest{
				active:   active,
				versions: map[int]backupasset.DomainKeyMaterial{historical.Version: historical, active.Version: active},
			},
			bytes.NewReader(bytes.Repeat([]byte{0x6a}, recoveryWorkspaceMarkerNonceBytes)),
		)
		fixture.target.marker = fixture.codec
		jobsPath := filepath.Join(fixture.root, recoveryWorkspaceLocatorDirectory)
		rawRemoteName := strings.Repeat("d", 32)
		jobPath := filepath.Join(jobsPath, rawRemoteName)
		if err := os.MkdirAll(jobPath, 0o700); err != nil {
			t.Fatalf("prepare DB-unmatched workspace: %v", err)
		}
		marker := recoveryReconciliationWorkspaceMarkerForTest(t, fixture, historical, rawRemoteName)
		if err := os.WriteFile(filepath.Join(jobPath, recoveryWorkspaceMarkerFileName), marker, 0o600); err != nil {
			t.Fatalf("prepare DB-unmatched marker: %v", err)
		}
		permit, request := recoveryReconciliationPermitForTargetTest(t, fixture, nil)
		fixture.target.now = func() time.Time { return fixture.now }

		page, err := fixture.target.ScanRecoveryRoot(context.Background(), permit, request)
		if err != nil || !page.Complete || page.Counts.Scanned != 1 || page.Counts.DBUnmatched != 1 ||
			len(page.Findings) != 1 || page.Findings[0].Category != RecoveryReconciliationDBUnmatched ||
			page.Findings[0].EntryKind != TargetEntryDirectory || page.Findings[0].JobID != "" {
			t.Fatalf("DB-unmatched reconciliation page=%+v error=%v", page, err)
		}
		assertRecoveryReconciliationFingerprintForTest(t, page.Findings[0].Fingerprint)
		encoded, marshalErr := json.Marshal(page)
		if marshalErr != nil || strings.Contains(string(encoded), rawRemoteName) ||
			strings.Contains(fmt.Sprintf("%v|%+v|%#v", page, page, page), rawRemoteName) {
			t.Fatalf("DB-unmatched reconciliation product leaked a raw remote name")
		}
		assertRecoveryReconciliationReadOnlyForTest(t, fixture.clients[len(fixture.clients)-1])
	})

	t.Run("historical key authenticates DB unmatched cleanup artifact", func(t *testing.T) {
		fixture := newRecoveryLocalSFTPTargetFixture(t)
		historical := recoveryWorkspaceMarkerMaterialForTest(1, strings.Repeat("h", sha256.Size))
		active := recoveryWorkspaceMarkerMaterialForTest(2, strings.Repeat("a", sha256.Size))
		fixture.codec = newRecoveryWorkspaceMarkerCodec(
			&recoveryWorkspaceMarkerKeySourceForTest{
				active:   active,
				versions: map[int]backupasset.DomainKeyMaterial{historical.Version: historical, active.Version: active},
			},
			nil,
		)
		fixture.target.marker = fixture.codec
		jobsPath := filepath.Join(fixture.root, recoveryWorkspaceLocatorDirectory)
		if err := os.Mkdir(jobsPath, 0o700); err != nil {
			t.Fatalf("prepare DB-unmatched cleanup directory: %v", err)
		}
		jobID := strings.Repeat("e", 32)
		workspaceLocator := recoveryWorkspaceLocatorDirectory + "/" + jobID
		targetPathDigest := mustTargetPathDigest(
			t, fixture.binding.RootID, fixture.binding.RootLocatorDigest, workspaceLocator,
		)
		markerBinding := recoveryWorkspaceMarkerBindingDigest(
			historical, jobID, fixture.binding.RootID, fixture.binding.RootRevision, workspaceLocator,
			RecoveryWorkerClaim{WorkerID: "r63-historical-owner", AttemptFence: 41},
		)
		captured, verified := recoveryOwnedCleanupComponents(
			jobID, fixture.binding.RootID, fixture.binding.RootRevision, targetPathDigest,
			markerBinding, "r63-historical-owner", 41,
		)
		verifiedDocument, err := encodeRecoveryOwnedCleanupArtifactDocument(
			recoveryOwnedCleanupArtifactBody{
				SchemaVersion: 1, KeyVersion: historical.Version, JobID: jobID,
				RootID: fixture.binding.RootID, RootRevision: fixture.binding.RootRevision,
				WorkspaceLocator: workspaceLocator, MarkerBindingDigest: markerBinding,
				MarkerCreatorID: "r63-historical-owner", MarkerCreatorFence: 41,
				MarkerDigest: strings.Repeat("f", sha256DigestLength), CapturedComponent: captured,
			},
			historical.Key, recoveryOwnedCleanupVerifiedDomain,
		)
		if err != nil {
			t.Fatalf("encode DB-unmatched cleanup artifact: %v", err)
		}
		if err := os.WriteFile(filepath.Join(jobsPath, verified), verifiedDocument, 0o600); err != nil {
			t.Fatalf("prepare DB-unmatched cleanup artifact: %v", err)
		}
		permit, request := recoveryReconciliationPermitForTargetTest(t, fixture, nil)
		fixture.target.now = func() time.Time { return fixture.now }

		page, scanErr := fixture.target.ScanRecoveryRoot(context.Background(), permit, request)
		if scanErr != nil || !page.Complete || page.Counts.Scanned != 1 || page.Counts.DBUnmatched != 1 ||
			len(page.Findings) != 1 || page.Findings[0].Category != RecoveryReconciliationDBUnmatched ||
			page.Findings[0].EntryKind != TargetEntryRegular || page.Findings[0].JobID != "" {
			t.Fatalf("DB-unmatched cleanup reconciliation page=%+v error=%v", page, scanErr)
		}
		assertRecoveryReconciliationFingerprintForTest(t, page.Findings[0].Fingerprint)
		assertRecoveryReconciliationReadOnlyForTest(t, fixture.clients[len(fixture.clients)-1])
	})

	t.Run("invalid grammar and unknown symlink remain opaque", func(t *testing.T) {
		fixture := newRecoveryLocalSFTPTargetFixture(t)
		jobsPath := filepath.Join(fixture.root, recoveryWorkspaceLocatorDirectory)
		if err := os.Mkdir(jobsPath, 0o700); err != nil {
			t.Fatalf("prepare unknown reconciliation directory: %v", err)
		}
		rawUnknownName := "RAW_R63_UNKNOWN_NAME_FOR_TEST_ONLY"
		rawSymlinkName := "RAW_R63_UNKNOWN_SYMLINK_FOR_TEST_ONLY"
		if err := os.WriteFile(filepath.Join(jobsPath, rawUnknownName), []byte("opaque"), 0o600); err != nil {
			t.Fatalf("prepare unknown reconciliation entry: %v", err)
		}
		if err := os.Symlink(fixture.root, filepath.Join(jobsPath, rawSymlinkName)); err != nil {
			t.Fatalf("prepare unknown reconciliation symlink: %v", err)
		}
		permit, request := recoveryReconciliationPermitForTargetTest(t, fixture, nil)
		fixture.target.now = func() time.Time { return fixture.now }

		page, err := fixture.target.ScanRecoveryRoot(context.Background(), permit, request)
		if err != nil || !page.Complete || page.Counts.Scanned != 2 || page.Counts.ForgedOrUnknown != 2 ||
			len(page.Findings) != 2 {
			t.Fatalf("unknown reconciliation page=%+v error=%v", page, err)
		}
		for _, finding := range page.Findings {
			if finding.Category != RecoveryReconciliationForgedOrUnknown || finding.JobID != "" {
				t.Fatalf("unknown reconciliation finding=%+v", finding)
			}
			assertRecoveryReconciliationFingerprintForTest(t, finding.Fingerprint)
		}
		encoded, marshalErr := json.Marshal(page)
		formatted := fmt.Sprintf("%v|%+v|%#v", page, page, page)
		if marshalErr != nil || strings.Contains(string(encoded), rawUnknownName) ||
			strings.Contains(string(encoded), rawSymlinkName) || strings.Contains(formatted, rawUnknownName) ||
			strings.Contains(formatted, rawSymlinkName) {
			t.Fatalf("unknown reconciliation product leaked a raw remote name")
		}
		client := fixture.clients[len(fixture.clients)-1]
		assertRecoveryReconciliationReadOnlyForTest(t, client)
		if client.readLinkCalls != 0 || client.openCalls != 1 {
			t.Fatalf("unknown entries were followed or read: read_link=%d open=%d", client.readLinkCalls, client.openCalls)
		}
	})

	t.Run("established scan interruption is a normal blocker", func(t *testing.T) {
		rawFailure := errors.New("RAW_R63_ESTABLISHED_SCAN_FAILURE_FOR_TEST_ONLY")
		for _, stage := range []string{"read_dir", "lstat", "marker"} {
			t.Run(stage, func(t *testing.T) {
				fixture := newRecoveryLocalSFTPTargetFixture(t)
				expectedByComponent := map[string]targetReconciliationExpected(nil)
				jobsPath, jobPath, markerPath := fixture.paths()
				if stage == "marker" {
					fixture.create(t)
					expectedByComponent = map[string]targetReconciliationExpected{
						fixture.writePermit.permit.JobID: recoveryReconciliationExpectedWorkspaceForTest(fixture),
					}
				} else {
					if err := os.Mkdir(jobsPath, 0o700); err != nil {
						t.Fatalf("prepare interrupted reconciliation directory: %v", err)
					}
					if stage == "lstat" {
						if err := os.WriteFile(filepath.Join(jobsPath, "opaque"), []byte("opaque"), 0o600); err != nil {
							t.Fatalf("prepare interrupted reconciliation entry: %v", err)
						}
					}
				}
				base := &recoveryLocalSFTPClient{}
				readDirRequests := make([]int, 0, 1)
				client := &recoveryScriptedSFTPClient{base: base}
				if stage == "read_dir" {
					client.open = func(value string) (recoveryTargetSFTPFile, error) {
						file, err := base.Open(value)
						if err != nil || value != jobsPath {
							return file, err
						}
						return &recoveryScriptedSFTPFile{base: file, readDir: func(n int) ([]os.FileInfo, error) {
							readDirRequests = append(readDirRequests, n)
							return nil, rawFailure
						}}, nil
					}
				}
				if stage == "lstat" {
					client.lstat = func(value string, _ int) (os.FileInfo, error) {
						if value == filepath.Join(jobsPath, "opaque") {
							return nil, rawFailure
						}
						return os.Lstat(value)
					}
				}
				if stage == "marker" {
					client.lstat = func(value string, _ int) (os.FileInfo, error) {
						if value == markerPath {
							return nil, rawFailure
						}
						return os.Lstat(value)
					}
				}
				target := fixture.targetWithClient(client)
				target.now = func() time.Time { return fixture.now }
				permit, request := recoveryReconciliationPermitForTargetTest(t, fixture, expectedByComponent)
				page, err := target.ScanRecoveryRoot(context.Background(), permit, request)
				if err != nil || page.Complete || page.NextCursor != "" || page.Counts.ScanIncomplete != 1 ||
					len(page.Findings) != 1 || page.Findings[0].Category != RecoveryReconciliationScanIncomplete ||
					strings.Contains(fmt.Sprintf("%v|%+v|%#v", page, page, page), rawFailure.Error()) {
					t.Fatalf("interrupted reconciliation did not return a sanitized blocker")
				}
				assertRecoveryReconciliationFingerprintForTest(t, page.Findings[0].Fingerprint)
				assertRecoveryReconciliationReadOnlyForTest(t, base)
				if stage == "read_dir" && !reflect.DeepEqual(readDirRequests, []int{recoveryCleanupReadBatch}) {
					t.Fatalf("reconciliation ReadDir requests=%v, want exact bounded batch", readDirRequests)
				}
				if stage == "marker" && jobPath == "" {
					t.Fatal("invalid marker interruption fixture")
				}
			})
		}
	})

	t.Run("setup dependencies fail sanitized before an authenticated scan", func(t *testing.T) {
		rawFailure := errors.New("RAW_R63_SETUP_FAILURE_FOR_TEST_ONLY")
		for _, stage := range []string{"key", "resolver", "dial", "sftp", "open", "open_identity"} {
			t.Run(stage, func(t *testing.T) {
				fixture := newRecoveryLocalSFTPTargetFixture(t)
				jobsPath := filepath.Join(fixture.root, recoveryWorkspaceLocatorDirectory)
				if err := os.Mkdir(jobsPath, 0o700); err != nil {
					t.Fatalf("prepare setup reconciliation directory: %v", err)
				}
				base := &recoveryLocalSFTPClient{}
				client := &recoveryScriptedSFTPClient{base: base}
				resolver := fixture.resolver
				dialer := fixture.dialer
				opener := func(*ssh.Client) (recoveryTargetSFTPClient, error) { return client, nil }
				codec := fixture.codec
				switch stage {
				case "key":
					codec = newRecoveryWorkspaceMarkerCodec(
						&recoveryWorkspaceMarkerKeySourceForTest{activeErr: rawFailure}, nil,
					)
				case "resolver":
					resolver = &recoveryTargetNodeSessionResolverFake{err: rawFailure}
				case "dial":
					dialer = &recoveryTargetNodeDialerFake{err: rawFailure}
				case "sftp":
					opener = func(*ssh.Client) (recoveryTargetSFTPClient, error) { return nil, rawFailure }
				case "open":
					client.open = func(string) (recoveryTargetSFTPFile, error) { return nil, rawFailure }
				case "open_identity":
					client.open = func(value string) (recoveryTargetSFTPFile, error) {
						file, err := base.Open(value)
						if err != nil {
							return nil, err
						}
						return &recoveryScriptedSFTPFile{
							base: file,
							stat: func() (os.FileInfo, error) {
								return recoveryProbeFileInfo{name: "jobs", mode: 0o600}, nil
							},
						}, nil
					}
				}
				target := newRecoverySFTPTargetForTest(
					newRecoveryTargetSessionFactoryForTest(resolver, dialer, opener, func(*ssh.Client) error { return nil }),
					codec,
				)
				target.now = func() time.Time { return fixture.now }
				permit, request := recoveryReconciliationPermitForTargetTest(t, fixture, nil)
				page, err := target.ScanRecoveryRoot(context.Background(), permit, request)
				if !reflect.DeepEqual(page, TargetReconciliationPage{}) || err != ErrRecoveryTargetUnavailable ||
					strings.Contains(err.Error(), rawFailure.Error()) {
					t.Fatalf("setup reconciliation dependency was not sanitized")
				}
			})
		}
	})
}

func TestRecoveryReconciliationCursorPrefixReplay(t *testing.T) {
	t.Run("authenticated page replay retains the complete prefix product", func(t *testing.T) {
		fixture := newRecoveryLocalSFTPTargetFixture(t)
		jobsPath := filepath.Join(fixture.root, recoveryWorkspaceLocatorDirectory)
		if err := os.Mkdir(jobsPath, 0o700); err != nil {
			t.Fatalf("create R64 jobs directory: %v", err)
		}
		rawUnknownName := strings.Repeat("0", 32)
		if err := os.WriteFile(filepath.Join(jobsPath, rawUnknownName), []byte("FAKE_R64_UNKNOWN_FOR_TEST_ONLY"), 0o600); err != nil {
			t.Fatalf("create R64 unknown prefix entry: %v", err)
		}
		expected, healthyNames, markers := recoveryReconciliationHealthyEntriesForTest(t, fixture, 256, 1)
		target, base, order := recoveryReconciliationSortedTargetForTest(t, fixture)
		permit, request := recoveryReconciliationPermitWithAuditForTargetTest(
			t, fixture, expected, recoveryReconciliationAuditKeyForTargetTest(), 11,
			"r64-generation-1", "", true,
		)

		first, err := target.ScanRecoveryRoot(context.Background(), permit, request)
		if err != nil || first.Complete || first.NextCursor == "" ||
			first.Counts != (RecoveryReconciliationCounts{Scanned: 256, KnownHealthy: 255, ForgedOrUnknown: 1}) ||
			len(first.Findings) != 1 || first.Findings[0].Category != RecoveryReconciliationForgedOrUnknown {
			t.Fatalf("R64 first page=%+v error=%v", first, err)
		}
		if len(first.NextCursor) > recoveryReconciliationCursorMax {
			t.Fatalf("R64 cursor bytes=%d, want <= %d", len(first.NextCursor), recoveryReconciliationCursorMax)
		}
		decoded, decodeErr := base64.RawURLEncoding.DecodeString(first.NextCursor)
		if decodeErr != nil || len(decoded) < 6 || binary.BigEndian.Uint16(decoded[:2]) != 1 ||
			binary.BigEndian.Uint32(decoded[2:6]) != 11 {
			t.Fatalf("R64 cursor does not expose the bounded schema/key header")
		}

		resumed := recoveryReconciliationResumePermitForTargetTest(t, permit, first.NextCursor)
		second, err := target.ScanRecoveryRoot(context.Background(), resumed, request)
		wantCounts := RecoveryReconciliationCounts{Scanned: 257, KnownHealthy: 256, ForgedOrUnknown: 1}
		if err != nil || !second.Complete || second.NextCursor != "" || second.Counts != wantCounts ||
			len(second.Findings) != 1 || second.Findings[0] != first.Findings[0] {
			t.Fatalf("R64 resumed page=%+v error=%v, want cumulative %+v", second, err, wantCounts)
		}
		if order.jobsOpens < 2 {
			t.Fatalf("R64 resume opened jobs directory %d times, want replay from the beginning", order.jobsOpens)
		}
		for _, n := range base.readDirRequests {
			if n != recoveryCleanupReadBatch {
				t.Fatalf("R64 ReadDir request=%d, want exact %d", n, recoveryCleanupReadBatch)
			}
		}
		result := recoveryReconciliationResultFromPage(second)
		encoded, marshalErr := json.Marshal(struct {
			First  TargetReconciliationPage     `json:"first"`
			Second TargetReconciliationPage     `json:"second"`
			Result RecoveryReconciliationResult `json:"result"`
		}{First: first, Second: second, Result: result})
		formatted := fmt.Sprintf("%v|%+v|%#v|%v|%+v|%#v", first, first, first, result, result, result)
		privacyKey := recoveryReconciliationAuditKeyForTargetTest()
		for _, private := range []string{
			rawUnknownName, healthyNames[0], healthyNames[len(healthyNames)-1], fixture.root,
			string(privacyKey[:]), string(markers[healthyNames[0]]),
		} {
			if marshalErr != nil || strings.Contains(string(encoded), private) || strings.Contains(formatted, private) ||
				strings.Contains(first.NextCursor, private) || bytes.Contains(decoded, []byte(private)) {
				t.Fatalf("R64 cursor/page/result leaked a private scan value")
			}
		}
		assertRecoveryReconciliationReadOnlyForTest(t, base)
	})

	t.Run("prefix and binding drift scan no unverified suffix", func(t *testing.T) {
		fixture := newRecoveryLocalSFTPTargetFixture(t)
		jobsPath := filepath.Join(fixture.root, recoveryWorkspaceLocatorDirectory)
		if err := os.Mkdir(jobsPath, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(jobsPath, strings.Repeat("0", 32)), []byte("opaque"), 0o600); err != nil {
			t.Fatal(err)
		}
		expected, healthyNames, markers := recoveryReconciliationHealthyEntriesForTest(t, fixture, 256, 1)
		target, base, order := recoveryReconciliationSortedTargetForTest(t, fixture)
		key := recoveryReconciliationAuditKeyForTargetTest()
		permit, request := recoveryReconciliationPermitWithAuditForTargetTest(
			t, fixture, expected, key, 11, "r64-generation-1", "", true,
		)
		first, err := target.ScanRecoveryRoot(context.Background(), permit, request)
		if err != nil || first.NextCursor == "" {
			t.Fatalf("create R64 drift cursor: page=%+v error=%v", first, err)
		}
		assertBlocked := func(label string, candidate TargetReconciliationPermit) {
			t.Helper()
			page, scanErr := target.ScanRecoveryRoot(context.Background(), candidate, request)
			if scanErr != nil || page.Complete || page.NextCursor != "" || page.Counts.ScanIncomplete != 1 ||
				len(page.Findings) == 0 || page.Findings[len(page.Findings)-1].Category != RecoveryReconciliationScanIncomplete ||
				page.Counts.Scanned > 256 {
				t.Fatalf("R64 %s drift page=%+v error=%v", label, page, scanErr)
			}
		}

		order.swapFirst = true
		orderCandidate := recoveryReconciliationResumePermitForTargetTest(t, permit, first.NextCursor)
		orderPage, orderErr := target.ScanRecoveryRoot(context.Background(), orderCandidate, request)
		order.swapFirst = false
		if orderErr != nil || orderPage.Complete || orderPage.NextCursor != "" || orderPage.Counts.ScanIncomplete != 1 ||
			orderPage.Counts.Scanned != 256 {
			t.Fatalf("R64 order drift page=%+v error=%v", orderPage, orderErr)
		}

		firstHealthyPath := filepath.Join(jobsPath, healthyNames[0])
		renamedPath := filepath.Join(jobsPath, strings.Repeat("f", 32))
		if err := os.Rename(firstHealthyPath, renamedPath); err != nil {
			t.Fatal(err)
		}
		namePage, nameErr := target.ScanRecoveryRoot(
			context.Background(), recoveryReconciliationResumePermitForTargetTest(t, permit, first.NextCursor), request,
		)
		if err := os.Rename(renamedPath, firstHealthyPath); err != nil {
			t.Fatal(err)
		}
		if nameErr != nil || namePage.Complete || namePage.NextCursor != "" || namePage.Counts.ScanIncomplete != 1 ||
			namePage.Counts.Scanned > 256 {
			t.Fatalf("R64 name drift page=%+v error=%v", namePage, nameErr)
		}

		markerPath := filepath.Join(firstHealthyPath, recoveryWorkspaceMarkerFileName)
		if err := os.WriteFile(markerPath, []byte(`{"schema_version":1}`), 0o600); err != nil {
			t.Fatal(err)
		}
		markerPage, markerErr := target.ScanRecoveryRoot(
			context.Background(), recoveryReconciliationResumePermitForTargetTest(t, permit, first.NextCursor), request,
		)
		if err := os.WriteFile(markerPath, markers[healthyNames[0]], 0o600); err != nil {
			t.Fatal(err)
		}
		if markerErr != nil || markerPage.Complete || markerPage.NextCursor != "" || markerPage.Counts.ScanIncomplete != 1 ||
			markerPage.Counts.Scanned > 256 {
			t.Fatalf("R64 marker drift page=%+v error=%v", markerPage, markerErr)
		}

		if err := os.Remove(markerPath); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(firstHealthyPath); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(firstHealthyPath, []byte("FAKE_R64_KIND_DRIFT_FOR_TEST_ONLY"), 0o600); err != nil {
			t.Fatal(err)
		}
		kindPage, kindErr := target.ScanRecoveryRoot(
			context.Background(), recoveryReconciliationResumePermitForTargetTest(t, permit, first.NextCursor), request,
		)
		if err := os.Remove(firstHealthyPath); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(firstHealthyPath, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(markerPath, markers[healthyNames[0]], 0o600); err != nil {
			t.Fatal(err)
		}
		if kindErr != nil || kindPage.Complete || kindPage.NextCursor != "" || kindPage.Counts.ScanIncomplete != 1 ||
			kindPage.Counts.Scanned > 256 {
			t.Fatalf("R64 kind drift page=%+v error=%v", kindPage, kindErr)
		}

		tamperedBytes, err := base64.RawURLEncoding.DecodeString(first.NextCursor)
		if err != nil {
			t.Fatal(err)
		}
		tamperedBytes[len(tamperedBytes)-1] ^= 0x01
		tampered := recoveryReconciliationResumePermitForTargetTest(
			t, permit, base64.RawURLEncoding.EncodeToString(tamperedBytes),
		)
		assertBlocked("tag", tampered)

		generation := recoveryReconciliationResumePermitForTargetTest(t, permit, first.NextCursor)
		generation.AdmissionGeneration = "r64-generation-2"
		generation.proof.bindingDigest = targetReconciliationPermitBindingDigest(
			generation.proof.auditTokenKey, generation.proof.auditKeyVersion, generation,
			generation.proof.sessionBinding.bindingDigest,
		)
		assertBlocked("generation", generation)

		expectedDrift := make(map[string]targetReconciliationExpected, len(expected)-1)
		for component, row := range expected {
			if component != healthyNames[len(healthyNames)-1] {
				expectedDrift[component] = row
			}
		}
		expectedPermit, _ := recoveryReconciliationPermitWithAuditForTargetTest(
			t, fixture, expectedDrift, key, 11, "r64-generation-1", first.NextCursor, true,
		)
		assertBlocked("expected set", expectedPermit)

		rootFixture := *fixture
		rootFixture.binding.RootRevision = "root-revision-r64-drift"
		rootPermit, _ := recoveryReconciliationPermitWithAuditForTargetTest(
			t, &rootFixture, expected, key, 11, "r64-generation-1", first.NextCursor, true,
		)
		assertBlocked("root revision", rootPermit)

		rotatedKey := key
		rotatedKey[0] ^= 0xff
		keyPermit, _ := recoveryReconciliationPermitWithAuditForTargetTest(
			t, fixture, expected, rotatedKey, 12, "r64-generation-1", first.NextCursor, true,
		)
		assertBlocked("key version", keyPermit)

		oversize := cloneRecoveryReconciliationPermitForTest(permit)
		oversize.Cursor = strings.Repeat("A", recoveryReconciliationCursorMax+1)
		oversize.proof.bindingDigest = targetReconciliationPermitBindingDigest(
			oversize.proof.auditTokenKey, oversize.proof.auditKeyVersion, oversize,
			oversize.proof.sessionBinding.bindingDigest,
		)
		if page, scanErr := target.ScanRecoveryRoot(context.Background(), oversize, request); scanErr != ErrInvalidTargetPermit || !reflect.DeepEqual(page, TargetReconciliationPage{}) {
			t.Fatalf("R64 oversized cursor page=%+v error=%v", page, scanErr)
		}
		bounds := recoveryReconciliationResumePermitForTargetTest(t, permit, first.NextCursor)
		bounds.PageLimit--
		bounds.proof.bindingDigest = targetReconciliationPermitBindingDigest(
			bounds.proof.auditTokenKey, bounds.proof.auditKeyVersion, bounds,
			bounds.proof.sessionBinding.bindingDigest,
		)
		if _, scanErr := target.ScanRecoveryRoot(context.Background(), bounds, request); scanErr != ErrInvalidTargetPermit {
			t.Fatalf("R64 drifted bound error=%v, want invalid permit", scanErr)
		}
		assertRecoveryReconciliationReadOnlyForTest(t, base)
	})

	t.Run("resume obtains only the historical audit key version", func(t *testing.T) {
		fixture := newRecoveryReconciliationServiceTestFixture(t)
		keys := fixture.service.keys.(*recoveryReconciliationKeySourceFake)
		historical := cloneDomainKeyMaterial(keys.material)
		historical.State = backupasset.DomainKeyVerifyOnly
		active := cloneDomainKeyMaterial(keys.material)
		active.ID = strings.Repeat("4", 32)
		active.Version++
		active.Key = []byte("456789abcdef0123456789abcdef0123")
		keys.material = active
		keys.versions = map[int]backupasset.DomainKeyMaterial{
			historical.Version: historical,
			active.Version:     active,
		}
		cursor := recoveryReconciliationCursorHeaderForTest(1, historical.Version)
		result, err := fixture.service.ReconcileRoot(context.Background(), ReconcileRecoveryRootRequest{
			NodeID: fixture.job.TargetNodeID, RootID: fixture.job.TargetRootID, Cursor: cursor,
		})
		if err != nil || !result.Complete || keys.activeCalls != 0 ||
			!reflect.DeepEqual(keys.byVersionCalls, []int{historical.Version}) ||
			len(fixture.target.permits) != 1 || fixture.target.permits[0].proof == nil ||
			fixture.target.permits[0].proof.auditKeyVersion != historical.Version ||
			fixture.target.permits[0].proof.auditTokenKey != [32]byte(historical.Key) {
			t.Fatalf("R64 historical-key resume result=%+v error=%v active=%d by_version=%v permits=%+v",
				result, err, keys.activeCalls, keys.byVersionCalls, fixture.target.permits)
		}
		for _, issued := range keys.issuedKeys {
			if !allZeroBytes(issued) {
				t.Fatal("R64 issued historical audit key was not cleared")
			}
		}
	})

	t.Run("finding expected root and chain hard bounds accept exact and block plus one", func(t *testing.T) {
		t.Run("findings", func(t *testing.T) {
			for _, count := range []int{recoveryReconciliationFindingLimit, recoveryReconciliationFindingLimit + 1} {
				fixture := newRecoveryLocalSFTPTargetFixture(t)
				jobsPath := filepath.Join(fixture.root, recoveryWorkspaceLocatorDirectory)
				if err := os.Mkdir(jobsPath, 0o700); err != nil {
					t.Fatal(err)
				}
				for index := 0; index < count; index++ {
					name := fmt.Sprintf("unknown-r64-%04d", index)
					if err := os.WriteFile(filepath.Join(jobsPath, name), []byte("opaque"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				target, base, _ := recoveryReconciliationSortedTargetForTest(t, fixture)
				permit, request := recoveryReconciliationPermitForTargetTest(t, fixture, nil)
				page, err := target.ScanRecoveryRoot(context.Background(), permit, request)
				if count == recoveryReconciliationFindingLimit {
					if err != nil || !page.Complete || page.Counts.Scanned != count ||
						page.Counts.ForgedOrUnknown != count || len(page.Findings) != count {
						t.Fatalf("R64 exact finding bound page=%+v error=%v", page, err)
					}
				} else {
					if err != nil || page.Complete || page.NextCursor == "" || len(page.Findings) != recoveryReconciliationFindingLimit {
						t.Fatalf("R64 finding limit+1 first page=%+v error=%v", page, err)
					}
					page, err = target.ScanRecoveryRoot(
						context.Background(), recoveryReconciliationResumePermitForTargetTest(t, permit, page.NextCursor), request,
					)
					if err != nil || page.Complete || page.NextCursor != "" || page.Counts.ScanIncomplete != 1 ||
						page.Counts.Scanned != count || page.Counts.ForgedOrUnknown != recoveryReconciliationFindingLimit ||
						len(page.Findings) != recoveryReconciliationFindingLimit {
						t.Fatalf("R64 finding limit+1 blocker page=%+v error=%v", page, err)
					}
				}
				assertRecoveryReconciliationReadOnlyForTest(t, base)
			}
		})

		t.Run("expected", func(t *testing.T) {
			fixture := newRecoveryLocalSFTPTargetFixture(t)
			expected := make(map[string]targetReconciliationExpected, recoveryReconciliationExpectedLimit)
			for index := 0; index < recoveryReconciliationExpectedLimit; index++ {
				jobID := fmt.Sprintf("%032x", index+1)
				expected["component-r64-"+jobID] = targetReconciliationExpected{
					jobID: jobID, entryKind: TargetEntryMissing, remoteState: recoveryReconciliationRemoteAbsent,
					markerBindingDigest: framedDigest("xirang/recovery/r64-expected-marker/v1", jobID),
					markerCreatorID:     "r64-expected-worker", markerCreatorFence: 1,
				}
			}
			permit, request := recoveryReconciliationPermitWithAuditForTargetTest(
				t, fixture, expected, recoveryReconciliationAuditKeyForTargetTest(), 11, "", "", true,
			)
			if len(permit.proof.expected) != recoveryReconciliationExpectedLimit ||
				permit.ValidateRequestAt(fixture.now, request) != nil {
				t.Fatal("R64 exact expected-set bound was rejected")
			}
			tooMany := cloneRecoveryReconciliationPermitForTest(permit)
			row := targetReconciliationExpected{
				jobID: strings.Repeat("f", 32), entryKind: TargetEntryMissing,
				remoteState:         recoveryReconciliationRemoteAbsent,
				markerBindingDigest: strings.Repeat("e", 64), markerCreatorID: "r64-expected-worker",
				markerCreatorFence: 1,
			}
			row.componentToken = recoveryReconciliationComponentToken(
				tooMany.proof.auditTokenKey, tooMany.proof.auditKeyVersion, tooMany.proof.sessionBinding,
				"component-r64-limit-plus-one", row,
			)
			tooMany.proof.expected = append(tooMany.proof.expected, row)
			sort.Slice(tooMany.proof.expected, func(left, right int) bool {
				if tooMany.proof.expected[left].jobID != tooMany.proof.expected[right].jobID {
					return tooMany.proof.expected[left].jobID < tooMany.proof.expected[right].jobID
				}
				return tooMany.proof.expected[left].componentToken < tooMany.proof.expected[right].componentToken
			})
			tooMany.ExpectedSetDigest = recoveryReconciliationExpectedSetDigest(
				tooMany.proof.auditKeyVersion, tooMany.proof.sessionBinding, tooMany.proof.expected,
			)
			tooMany.proof.bindingDigest = targetReconciliationPermitBindingDigest(
				tooMany.proof.auditTokenKey, tooMany.proof.auditKeyVersion, tooMany,
				tooMany.proof.sessionBinding.bindingDigest,
			)
			if err := tooMany.ValidateRequestAt(fixture.now, request); err != ErrInvalidTargetPermit {
				t.Fatalf("R64 expected limit+1 error=%v", err)
			}
		})

		t.Run("roots", func(t *testing.T) {
			roots := make([]settings.RecoveryTargetRootReference, recoveryReconciliationRootLimit+1)
			for index := range roots {
				roots[index] = settings.RecoveryTargetRootReference{
					NodeID: uint(index + 1), RootID: fmt.Sprintf("root-r64-%04d", index),
				}
			}
			registry := &recoveryReconciliationRootBoundRegistryForTest{roots: roots[:recoveryReconciliationRootLimit]}
			service := &RecoveryReconciliationService{roots: registry}
			bounded, err := service.listRecoveryReconciliationRoots(context.Background())
			if err != nil || len(bounded) != recoveryReconciliationRootLimit || registry.listCalls != 1 {
				t.Fatalf("R64 exact root bound count=%d calls=%d error=%v", len(bounded), registry.listCalls, err)
			}
			registry.roots = roots
			if bounded, err = service.listRecoveryReconciliationRoots(context.Background()); err != ErrRecoveryReconciliationUnavailable || bounded != nil {
				t.Fatalf("R64 root limit+1 count=%d error=%v", len(bounded), err)
			}
		})

		t.Run("chain", func(t *testing.T) {
			fixture := newRecoveryLocalSFTPTargetFixture(t)
			jobsPath := filepath.Join(fixture.root, recoveryWorkspaceLocatorDirectory)
			if err := os.Mkdir(jobsPath, 0o700); err != nil {
				t.Fatal(err)
			}
			expected, _, _ := recoveryReconciliationHealthyEntriesForTest(
				t, fixture, recoveryReconciliationChainLimit, 1,
			)
			target, base, _ := recoveryReconciliationSortedTargetForTest(t, fixture)
			permit, request := recoveryReconciliationPermitForTargetTest(t, fixture, expected)
			exact, pages := recoveryReconciliationScanChainForTest(t, target, permit, request)
			if !exact.Complete || exact.NextCursor != "" || pages != recoveryReconciliationChainLimit/recoveryReconciliationPageLimit ||
				exact.Counts != (RecoveryReconciliationCounts{Scanned: recoveryReconciliationChainLimit, KnownHealthy: recoveryReconciliationChainLimit}) ||
				len(exact.Findings) != 0 {
				t.Fatalf("R64 exact chain pages=%d page=%+v", pages, exact)
			}
			if err := os.WriteFile(filepath.Join(jobsPath, "zzzz-r64-chain-limit-plus-one"), []byte("opaque"), 0o600); err != nil {
				t.Fatal(err)
			}
			blocked, pages := recoveryReconciliationScanChainForTest(t, target, permit, request)
			if blocked.Complete || blocked.NextCursor != "" || pages != recoveryReconciliationChainLimit/recoveryReconciliationPageLimit ||
				blocked.Counts.Scanned != recoveryReconciliationChainLimit ||
				blocked.Counts.KnownHealthy != recoveryReconciliationChainLimit || blocked.Counts.ScanIncomplete != 1 {
				t.Fatalf("R64 chain limit+1 pages=%d page=%+v", pages, blocked)
			}
			assertRecoveryReconciliationReadOnlyForTest(t, base)
		})
	})
}

type recoveryReconciliationCloseRecorderForR66 struct {
	mu     sync.Mutex
	order  []string
	counts map[string]int
}

func (recorder *recoveryReconciliationCloseRecorderForR66) record(name string) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if recorder.counts == nil {
		recorder.counts = make(map[string]int)
	}
	recorder.counts[name]++
	recorder.order = append(recorder.order, name)
}

func (recorder *recoveryReconciliationCloseRecorderForR66) snapshot() ([]string, map[string]int) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	order := append([]string(nil), recorder.order...)
	counts := make(map[string]int, len(recorder.counts))
	for name, count := range recorder.counts {
		counts[name] = count
	}
	return order, counts
}

func TestRecoverySFTPTargetReconciliationResourceErrorAndPrivacyMatrix(t *testing.T) {
	rawFailure := errors.New("RAW_R66_RECONCILIATION_TARGET_FAILURE_FOR_TEST_ONLY")
	rawClose := errors.New("RAW_R66_RECONCILIATION_CLOSE_FAILURE_FOR_TEST_ONLY")

	assertSanitizedUnavailable := func(t *testing.T, page TargetReconciliationPage, err error) {
		t.Helper()
		if err != ErrRecoveryTargetUnavailable || !reflect.DeepEqual(page, TargetReconciliationPage{}) {
			t.Fatalf("target dependency page=%+v error=%v, want sanitized unavailable", page, err)
		}
		corpus := fmt.Sprintf("%v|%+v|%#v|%v|%+v|%#v", page, page, page, err, err, err)
		if strings.Contains(corpus, rawFailure.Error()) || strings.Contains(corpus, rawClose.Error()) {
			t.Fatalf("target dependency leaked a raw error: %s", corpus)
		}
	}
	assertCloseState := func(
		t *testing.T,
		recorder *recoveryReconciliationCloseRecorderForR66,
		wantOrder []string,
	) {
		t.Helper()
		order, counts := recorder.snapshot()
		if !reflect.DeepEqual(order, wantOrder) {
			t.Fatalf("resource close order=%v, want %v", order, wantOrder)
		}
		for _, name := range wantOrder {
			if counts[name] != 1 {
				t.Fatalf("resource %s close calls=%d, want at most/exactly once", name, counts[name])
			}
		}
	}

	t.Run("caller context identity precedes invalid authority", func(t *testing.T) {
		fixture := newRecoveryLocalSFTPTargetFixture(t)
		permit, request := recoveryReconciliationPermitForTargetTest(t, fixture, nil)
		permit.Operation = "invalid-operation"
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		page, err := fixture.target.ScanRecoveryRoot(ctx, permit, request)
		if err != context.Canceled || !reflect.DeepEqual(page, TargetReconciliationPage{}) ||
			fixture.resolver.calls != 0 || fixture.dialer.calls != 0 {
			t.Fatalf("canceled invalid target page=%+v error=%v resolve=%d dial=%d",
				page, err, fixture.resolver.calls, fixture.dialer.calls)
		}
	})

	t.Run("caller context identity precedes invalid target dependencies", func(t *testing.T) {
		fixture := newRecoveryLocalSFTPTargetFixture(t)
		permit, request := recoveryReconciliationPermitForTargetTest(t, fixture, nil)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		for _, testCase := range []struct {
			name   string
			target *recoverySFTPTarget
		}{
			{name: "nil receiver"},
			{name: "nil clock", target: &recoverySFTPTarget{}},
		} {
			t.Run(testCase.name, func(t *testing.T) {
				page, err := testCase.target.ScanRecoveryRoot(ctx, permit, request)
				if err != context.Canceled || !reflect.DeepEqual(page, TargetReconciliationPage{}) {
					t.Fatalf("canceled invalid target page=%+v error=%v, want exact context cancellation", page, err)
				}
			})
		}
	})

	t.Run("invalid authority precedes target dependencies", func(t *testing.T) {
		fixture := newRecoveryLocalSFTPTargetFixture(t)
		permit, request := recoveryReconciliationPermitForTargetTest(t, fixture, nil)
		permit.RootID = "substituted-root"
		page, err := fixture.target.ScanRecoveryRoot(context.Background(), permit, request)
		if err != ErrInvalidTargetPermit || !reflect.DeepEqual(page, TargetReconciliationPage{}) ||
			fixture.resolver.calls != 0 || fixture.dialer.calls != 0 {
			t.Fatalf("invalid target authority page=%+v error=%v resolve=%d dial=%d",
				page, err, fixture.resolver.calls, fixture.dialer.calls)
		}
	})

	t.Run("setup key resolver and dial failures are sanitized", func(t *testing.T) {
		for _, stage := range []string{"key", "resolver", "dial"} {
			t.Run(stage, func(t *testing.T) {
				fixture := newRecoveryLocalSFTPTargetFixture(t)
				resolver := fixture.resolver
				dialer := fixture.dialer
				codec := fixture.codec
				switch stage {
				case "key":
					codec = newRecoveryWorkspaceMarkerCodec(
						&recoveryWorkspaceMarkerKeySourceForTest{activeErr: rawFailure}, nil,
					)
				case "resolver":
					resolver = &recoveryTargetNodeSessionResolverFake{err: rawFailure}
				case "dial":
					dialer = &recoveryTargetNodeDialerFake{err: rawFailure}
				}
				target := newRecoverySFTPTargetForTest(
					newRecoveryTargetSessionFactoryForTest(
						resolver, dialer,
						func(*ssh.Client) (recoveryTargetSFTPClient, error) {
							t.Fatal("setup failure opened SFTP")
							return nil, nil
						},
						func(*ssh.Client) error { t.Fatal("setup failure closed an unowned SSH client"); return nil },
					), codec,
				)
				target.now = func() time.Time { return fixture.now }
				permit, request := recoveryReconciliationPermitForTargetTest(t, fixture, nil)
				page, err := target.ScanRecoveryRoot(context.Background(), permit, request)
				assertSanitizedUnavailable(t, page, err)
			})
		}
	})

	t.Run("dial resource with error closes SSH once", func(t *testing.T) {
		fixture := newRecoveryLocalSFTPTargetFixture(t)
		recorder := &recoveryReconciliationCloseRecorderForR66{}
		dialer := &recoveryTargetNodeDialerFake{dial: func(
			context.Context, model.Node, string, sshutil.DialAuditContext,
		) (*ssh.Client, error) {
			return &ssh.Client{}, rawFailure
		}}
		target := newRecoverySFTPTargetForTest(
			newRecoveryTargetSessionFactoryForTest(
				fixture.resolver, dialer,
				func(*ssh.Client) (recoveryTargetSFTPClient, error) {
					t.Fatal("failed dial opened SFTP")
					return nil, nil
				},
				func(*ssh.Client) error { recorder.record("ssh"); return rawClose },
			), fixture.codec,
		)
		target.now = func() time.Time { return fixture.now }
		permit, request := recoveryReconciliationPermitForTargetTest(t, fixture, nil)
		page, err := target.ScanRecoveryRoot(context.Background(), permit, request)
		assertSanitizedUnavailable(t, page, err)
		assertCloseState(t, recorder, []string{"ssh"})
	})

	t.Run("SFTP resource with error closes SFTP then SSH once", func(t *testing.T) {
		fixture := newRecoveryLocalSFTPTargetFixture(t)
		recorder := &recoveryReconciliationCloseRecorderForR66{}
		client := &recoveryScriptedSFTPClient{
			base:  &recoveryLocalSFTPClient{},
			close: func() error { recorder.record("sftp"); return rawClose },
		}
		target := newRecoverySFTPTargetForTest(
			newRecoveryTargetSessionFactoryForTest(
				fixture.resolver, fixture.dialer,
				func(*ssh.Client) (recoveryTargetSFTPClient, error) { return client, rawFailure },
				func(*ssh.Client) error { recorder.record("ssh"); return rawClose },
			), fixture.codec,
		)
		target.now = func() time.Time { return fixture.now }
		permit, request := recoveryReconciliationPermitForTargetTest(t, fixture, nil)
		page, err := target.ScanRecoveryRoot(context.Background(), permit, request)
		assertSanitizedUnavailable(t, page, err)
		assertCloseState(t, recorder, []string{"sftp", "ssh"})
	})

	t.Run("jobs handle with error closes handle SFTP and SSH once", func(t *testing.T) {
		fixture := newRecoveryLocalSFTPTargetFixture(t)
		jobsPath := filepath.Join(fixture.root, recoveryWorkspaceLocatorDirectory)
		if err := os.Mkdir(jobsPath, 0o700); err != nil {
			t.Fatal(err)
		}
		recorder := &recoveryReconciliationCloseRecorderForR66{}
		base := &recoveryLocalSFTPClient{}
		client := &recoveryScriptedSFTPClient{base: base}
		client.open = func(value string) (recoveryTargetSFTPFile, error) {
			file, err := base.Open(value)
			if err != nil || value != jobsPath {
				return file, err
			}
			return &recoveryScriptedSFTPFile{base: file, close: func() error {
				recorder.record("jobs")
				return file.Close()
			}}, rawFailure
		}
		client.close = func() error { recorder.record("sftp"); return rawClose }
		target := newRecoverySFTPTargetForTest(
			newRecoveryTargetSessionFactoryForTest(
				fixture.resolver, fixture.dialer,
				func(*ssh.Client) (recoveryTargetSFTPClient, error) { return client, nil },
				func(*ssh.Client) error { recorder.record("ssh"); return rawClose },
			), fixture.codec,
		)
		target.now = func() time.Time { return fixture.now }
		permit, request := recoveryReconciliationPermitForTargetTest(t, fixture, nil)
		page, err := target.ScanRecoveryRoot(context.Background(), permit, request)
		assertSanitizedUnavailable(t, page, err)
		assertCloseState(t, recorder, []string{"jobs", "sftp", "ssh"})
	})

	t.Run("established Lstat and marker-read failures block and close once", func(t *testing.T) {
		for _, stage := range []string{"lstat", "marker"} {
			t.Run(stage, func(t *testing.T) {
				fixture := newRecoveryLocalSFTPTargetFixture(t)
				jobsPath, _, markerPath := fixture.paths()
				var expected map[string]targetReconciliationExpected
				if stage == "marker" {
					fixture.create(t)
					expected = map[string]targetReconciliationExpected{
						fixture.writePermit.permit.JobID: recoveryReconciliationExpectedWorkspaceForTest(fixture),
					}
				} else {
					if err := os.Mkdir(jobsPath, 0o700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(jobsPath, "r66-private-entry"), []byte("private"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				recorder := &recoveryReconciliationCloseRecorderForR66{}
				base := &recoveryLocalSFTPClient{}
				client := &recoveryScriptedSFTPClient{base: base}
				client.open = func(value string) (recoveryTargetSFTPFile, error) {
					file, err := base.Open(value)
					if err != nil {
						return file, err
					}
					name := ""
					switch value {
					case jobsPath:
						name = "jobs"
					case markerPath:
						name = "marker"
					}
					if name == "" {
						return file, nil
					}
					wrapped := &recoveryScriptedSFTPFile{base: file, close: func() error {
						recorder.record(name)
						return file.Close()
					}}
					if stage == "marker" && value == markerPath {
						return wrapped, rawFailure
					}
					return wrapped, nil
				}
				if stage == "lstat" {
					entryPath := filepath.Join(jobsPath, "r66-private-entry")
					client.lstat = func(value string, _ int) (os.FileInfo, error) {
						if value == entryPath {
							return nil, rawFailure
						}
						return os.Lstat(value)
					}
				}
				client.close = func() error { recorder.record("sftp"); return rawClose }
				target := newRecoverySFTPTargetForTest(
					newRecoveryTargetSessionFactoryForTest(
						fixture.resolver, fixture.dialer,
						func(*ssh.Client) (recoveryTargetSFTPClient, error) { return client, nil },
						func(*ssh.Client) error { recorder.record("ssh"); return rawClose },
					), fixture.codec,
				)
				target.now = func() time.Time { return fixture.now }
				permit, request := recoveryReconciliationPermitForTargetTest(t, fixture, expected)
				page, err := target.ScanRecoveryRoot(context.Background(), permit, request)
				if err != nil || page.Complete || page.Counts.ScanIncomplete != 1 ||
					len(page.Findings) != 1 || page.Findings[0].Category != RecoveryReconciliationScanIncomplete {
					t.Fatalf("%s interruption page=%+v error=%v", stage, page, err)
				}
				wantOrder := []string{"jobs", "sftp", "ssh"}
				if stage == "marker" {
					wantOrder = []string{"marker", "jobs", "sftp", "ssh"}
				}
				assertCloseState(t, recorder, wantOrder)
				corpus := fmt.Sprintf("%v|%+v|%#v", page, page, page)
				if strings.Contains(corpus, rawFailure.Error()) || strings.Contains(corpus, rawClose.Error()) {
					t.Fatalf("%s interruption leaked raw failure", stage)
				}
			})
		}
	})

	t.Run("close ambiguity blocks clear and preserves ownership order", func(t *testing.T) {
		fixture := newRecoveryLocalSFTPTargetFixture(t)
		jobsPath := filepath.Join(fixture.root, recoveryWorkspaceLocatorDirectory)
		if err := os.Mkdir(jobsPath, 0o700); err != nil {
			t.Fatal(err)
		}
		recorder := &recoveryReconciliationCloseRecorderForR66{}
		base := &recoveryLocalSFTPClient{}
		client := &recoveryScriptedSFTPClient{base: base}
		client.open = func(value string) (recoveryTargetSFTPFile, error) {
			file, err := base.Open(value)
			if err != nil || value != jobsPath {
				return file, err
			}
			return &recoveryScriptedSFTPFile{base: file, close: func() error {
				closeErr := file.Close()
				recorder.record("jobs")
				if closeErr != nil {
					return closeErr
				}
				return rawClose
			}}, nil
		}
		client.close = func() error { recorder.record("sftp"); return rawClose }
		target := newRecoverySFTPTargetForTest(
			newRecoveryTargetSessionFactoryForTest(
				fixture.resolver, fixture.dialer,
				func(*ssh.Client) (recoveryTargetSFTPClient, error) { return client, nil },
				func(*ssh.Client) error { recorder.record("ssh"); return rawClose },
			), fixture.codec,
		)
		target.now = func() time.Time { return fixture.now }
		permit, request := recoveryReconciliationPermitForTargetTest(t, fixture, nil)
		page, err := target.ScanRecoveryRoot(context.Background(), permit, request)
		if err != nil || page.Complete || page.Counts.ScanIncomplete != 1 ||
			len(page.Findings) != 1 || page.Findings[0].Category != RecoveryReconciliationScanIncomplete {
			t.Fatalf("close ambiguity page=%+v error=%v, want blocked incomplete", page, err)
		}
		assertCloseState(t, recorder, []string{"jobs", "sftp", "ssh"})
	})

	t.Run("concurrent cancellation wins close noise and closes transport before tracked handle", func(t *testing.T) {
		fixture := newRecoveryLocalSFTPTargetFixture(t)
		jobsPath := filepath.Join(fixture.root, recoveryWorkspaceLocatorDirectory)
		if err := os.Mkdir(jobsPath, 0o700); err != nil {
			t.Fatal(err)
		}
		recorder := &recoveryReconciliationCloseRecorderForR66{}
		readStarted := make(chan struct{})
		releaseRead := make(chan struct{})
		sftpClosed := make(chan struct{})
		var readOnce sync.Once
		var sftpOnce sync.Once
		base := &recoveryLocalSFTPClient{}
		client := &recoveryScriptedSFTPClient{base: base}
		client.open = func(value string) (recoveryTargetSFTPFile, error) {
			file, err := base.Open(value)
			if err != nil || value != jobsPath {
				return file, err
			}
			return &recoveryScriptedSFTPFile{
				base: file,
				readDir: func(int) ([]os.FileInfo, error) {
					readOnce.Do(func() { close(readStarted) })
					<-releaseRead
					return nil, rawFailure
				},
				close: func() error {
					recorder.record("jobs")
					_ = file.Close()
					return rawClose
				},
			}, nil
		}
		client.close = func() error {
			recorder.record("sftp")
			sftpOnce.Do(func() { close(sftpClosed) })
			return rawClose
		}
		target := newRecoverySFTPTargetForTest(
			newRecoveryTargetSessionFactoryForTest(
				fixture.resolver, fixture.dialer,
				func(*ssh.Client) (recoveryTargetSFTPClient, error) { return client, nil },
				func(*ssh.Client) error { recorder.record("ssh"); return rawClose },
			), fixture.codec,
		)
		target.now = func() time.Time { return fixture.now }
		permit, request := recoveryReconciliationPermitForTargetTest(t, fixture, nil)
		ctx, cancel := context.WithCancel(context.Background())
		type outcome struct {
			page TargetReconciliationPage
			err  error
		}
		outcomes := make(chan outcome, 1)
		go func() {
			page, err := target.ScanRecoveryRoot(ctx, permit, request)
			outcomes <- outcome{page: page, err: err}
		}()
		select {
		case <-readStarted:
		case <-time.After(5 * time.Second):
			t.Fatal("reconciliation did not reach cancellable ReadDir")
		}
		cancel()
		select {
		case <-sftpClosed:
		case <-time.After(5 * time.Second):
			t.Fatal("cancellation did not close the SFTP session")
		}
		close(releaseRead)
		var got outcome
		select {
		case got = <-outcomes:
		case <-time.After(5 * time.Second):
			t.Fatal("canceled reconciliation did not return")
		}
		if got.err != context.Canceled || !reflect.DeepEqual(got.page, TargetReconciliationPage{}) {
			t.Fatalf("canceled reconciliation page=%+v error=%v, want exact context cancellation", got.page, got.err)
		}
		assertCloseState(t, recorder, []string{"sftp", "ssh", "jobs"})
	})

	t.Run("complete boundary privacy canaries and zero direct logs", func(t *testing.T) {
		var capturedLogs bytes.Buffer
		previousLogger := logger.Log
		logger.Log = zerolog.New(&capturedLogs)
		t.Cleanup(func() { logger.Log = previousLogger })

		fixture := newRecoveryLocalSFTPTargetFixture(t)
		hostCanary := "R66_PRIVATE_HOST_FOR_TEST_ONLY"
		userCanary := "R66_PRIVATE_USER_FOR_TEST_ONLY"
		credentialCanary := "R66_PRIVATE_CREDENTIAL_FOR_TEST_ONLY"
		markerCanary := "R66_PRIVATE_MARKER_FOR_TEST_ONLY"
		contentCanary := "R66_PRIVATE_CONTENT_FOR_TEST_ONLY"
		tokenInputCanary := "R66_PRIVATE_HMAC_INPUT_FOR_TEST_ONLY"
		statusCanary := "R66_PRIVATE_SFTP_STATUS_FOR_TEST_ONLY"
		rawErrorCanary := "R66_PRIVATE_RAW_ERROR_FOR_TEST_ONLY"
		fixture.resolver.result.Node.Host = hostCanary
		fixture.resolver.result.Node.Username = userCanary
		fixture.resolver.result.Node.Password = credentialCanary
		jobsPath := filepath.Join(fixture.root, recoveryWorkspaceLocatorDirectory)
		if err := os.Mkdir(jobsPath, 0o700); err != nil {
			t.Fatal(err)
		}
		rawRemoteName := strings.Repeat("d", 32)
		unknownPath := filepath.Join(jobsPath, rawRemoteName)
		if err := os.Mkdir(unknownPath, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(unknownPath, recoveryWorkspaceMarkerFileName),
			[]byte(markerCanary), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(unknownPath, "user-content.bin"), []byte(contentCanary), 0o600); err != nil {
			t.Fatal(err)
		}
		expected := targetReconciliationExpected{
			jobID: strings.Repeat("e", 32), entryKind: TargetEntryMissing,
			remoteState:         recoveryReconciliationRemoteAbsent,
			markerBindingDigest: framedDigest("r66-private-marker-digest", markerCanary),
			markerCreatorID:     "r66-private-marker-owner", markerCreatorFence: 9,
		}
		permit, request := recoveryReconciliationPermitForTargetTest(
			t, fixture, map[string]targetReconciliationExpected{tokenInputCanary: expected},
		)
		page, err := fixture.target.ScanRecoveryRoot(context.Background(), permit, request)
		if err != nil || !page.Complete || page.Counts.ForgedOrUnknown != 1 ||
			len(page.Findings) != 1 || page.Findings[0].JobID != "" {
			t.Fatalf("R66 privacy page=%+v error=%v", page, err)
		}
		cursor := recoveryReconciliationEncodeCursor(
			permit, recoveryReconciliationPageLimit, recoveryReconciliationInitialPrefixDigest(permit),
		)
		result := recoveryReconciliationResultFromPage(page)
		metricsLabels := map[string]string{
			"state": string(result.State), "category": string(page.Findings[0].Category),
			"entry_kind": string(page.Findings[0].EntryKind),
		}
		var corpus strings.Builder
		for _, value := range []any{permit, permit.proof, request, page, result, cursor, metricsLabels} {
			_, _ = fmt.Fprintf(&corpus, "|%v|%+v|%#v", value, value, value)
			encoded, marshalErr := json.Marshal(value)
			if marshalErr != nil {
				t.Fatalf("marshal R66 target privacy product %T: %v", value, marshalErr)
			}
			corpus.Write(encoded)
		}

		rawDependency := errors.New(statusCanary + ":" + rawErrorCanary)
		base := &recoveryLocalSFTPClient{}
		client := &recoveryScriptedSFTPClient{base: base}
		client.open = func(value string) (recoveryTargetSFTPFile, error) {
			file, openErr := base.Open(value)
			if openErr != nil || value != jobsPath {
				return file, openErr
			}
			return &recoveryScriptedSFTPFile{base: file, readDir: func(int) ([]os.FileInfo, error) {
				return nil, rawDependency
			}}, nil
		}
		failureTarget := fixture.targetWithClient(client)
		failureTarget.now = func() time.Time { return fixture.now }
		failurePage, failureErr := failureTarget.ScanRecoveryRoot(context.Background(), permit, request)
		if failureErr != nil || failurePage.Complete || failurePage.Counts.ScanIncomplete != 1 {
			t.Fatalf("R66 private dependency page=%+v error=%v", failurePage, failureErr)
		}
		_, _ = fmt.Fprintf(&corpus, "|%v|%+v|%#v|%v", failurePage, failurePage, failurePage, failureErr)
		encodedFailure, marshalErr := json.Marshal(failurePage)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		corpus.Write(encodedFailure)
		corpus.WriteString(capturedLogs.String())

		canaries := []string{
			hostCanary, userCanary, credentialCanary, fixture.root, rawRemoteName,
			tokenInputCanary, permit.proof.expected[0].componentToken,
			markerCanary, contentCanary, expected.markerBindingDigest,
			statusCanary, rawErrorCanary,
		}
		for _, canary := range canaries {
			if strings.Contains(corpus.String(), canary) {
				t.Fatalf("R66 target boundary leaked private canary %q", canary)
			}
		}
		if capturedLogs.Len() != 0 {
			t.Fatalf("reconciliation target emitted direct logs: %s", capturedLogs.String())
		}
		assertRecoveryReconciliationReadOnlyForTest(t, fixture.clients[len(fixture.clients)-1])
		assertRecoveryReconciliationReadOnlyForTest(t, base)
	})
}

func TestRecoveryTargetReconciliationPortStaticZeroMutationGate(t *testing.T) {
	port := reflect.TypeOf((*TargetReconciliationPort)(nil)).Elem()
	if port.NumMethod() != 1 || port.Method(0).Name != "ScanRecoveryRoot" {
		t.Fatalf("reconciliation port mutation surface drifted: %v", port)
	}

	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, "target.go", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse target imports: %v", err)
	}
	for _, imported := range file.Imports {
		pathValue, unquoteErr := strconv.Unquote(imported.Path.Value)
		if unquoteErr != nil {
			t.Fatalf("decode target import: %v", unquoteErr)
		}
		switch pathValue {
		case "log", "log/slog", "xirang/backend/internal/logger", "github.com/rs/zerolog":
			t.Fatalf("reconciliation target gained a direct logging dependency %q", pathValue)
		}
	}

	file, err = parser.ParseFile(fileSet, "target.go", nil, 0)
	if err != nil {
		t.Fatalf("parse target implementation: %v", err)
	}
	functions := make(map[string][]*ast.FuncDecl)
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Body == nil {
			continue
		}
		functions[function.Name.Name] = append(functions[function.Name.Name], function)
	}
	queue := append([]*ast.FuncDecl(nil), functions["ScanRecoveryRoot"]...)
	if len(queue) == 0 {
		t.Fatal("ScanRecoveryRoot implementation is missing")
	}
	forbidden := map[string]struct{}{
		"Rename": {}, "PosixRename": {}, "Remove": {}, "RemoveDirectory": {},
		"Mkdir": {}, "Chmod": {}, "OpenFile": {},
	}
	seen := make(map[*ast.FuncDecl]struct{})
	for len(queue) > 0 {
		function := queue[0]
		queue = queue[1:]
		if _, visited := seen[function]; visited {
			continue
		}
		seen[function] = struct{}{}
		ast.Inspect(function.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			callee := ""
			switch target := call.Fun.(type) {
			case *ast.Ident:
				callee = target.Name
			case *ast.SelectorExpr:
				callee = target.Sel.Name
				if _, denied := forbidden[callee]; denied {
					position := fileSet.Position(target.Sel.Pos())
					t.Fatalf("read-only reconciliation reaches %s at %s", callee, position)
				}
			}
			queue = append(queue, functions[callee]...)
			return true
		})
	}
}

type recoveryReconciliationDirectoryOrderForTest struct {
	jobsOpens int
	swapFirst bool
}

func recoveryReconciliationSortedTargetForTest(
	t *testing.T,
	fixture *recoveryLocalSFTPTargetFixture,
) (*recoverySFTPTarget, *recoveryLocalSFTPClient, *recoveryReconciliationDirectoryOrderForTest) {
	t.Helper()
	base := &recoveryLocalSFTPClient{}
	order := &recoveryReconciliationDirectoryOrderForTest{}
	jobsPath := filepath.Join(fixture.root, recoveryWorkspaceLocatorDirectory)
	client := &recoveryScriptedSFTPClient{base: base}
	client.open = func(value string) (recoveryTargetSFTPFile, error) {
		file, err := base.Open(value)
		if err != nil || value != jobsPath {
			return file, err
		}
		entries, readErr := os.ReadDir(value)
		if readErr != nil {
			_ = file.Close()
			return nil, readErr
		}
		infos := make([]os.FileInfo, len(entries))
		for index, entry := range entries {
			info, infoErr := entry.Info()
			if infoErr != nil {
				_ = file.Close()
				return nil, infoErr
			}
			infos[index] = info
		}
		order.jobsOpens++
		if order.swapFirst && len(infos) > 1 {
			infos[0], infos[1] = infos[1], infos[0]
		}
		next := 0
		return &recoveryScriptedSFTPFile{base: file, readDir: func(n int) ([]os.FileInfo, error) {
			base.readDirRequests = append(base.readDirRequests, n)
			if next >= len(infos) {
				return nil, io.EOF
			}
			end := next + n
			if end > len(infos) {
				end = len(infos)
			}
			batch := append([]os.FileInfo(nil), infos[next:end]...)
			next = end
			if len(batch) < n {
				return batch, io.EOF
			}
			return batch, nil
		}}, nil
	}
	target := fixture.targetWithClient(client)
	target.now = func() time.Time { return fixture.now }
	return target, base, order
}

func recoveryReconciliationHealthyEntriesForTest(
	t *testing.T,
	fixture *recoveryLocalSFTPTargetFixture,
	count int,
	start int,
) (map[string]targetReconciliationExpected, []string, map[string][]byte) {
	t.Helper()
	jobsPath := filepath.Join(fixture.root, recoveryWorkspaceLocatorDirectory)
	expected := make(map[string]targetReconciliationExpected, count)
	names := make([]string, 0, count)
	markers := make(map[string][]byte, count)
	for index := 0; index < count; index++ {
		jobID := fmt.Sprintf("%032x", start+index)
		jobPath := filepath.Join(jobsPath, jobID)
		if err := os.Mkdir(jobPath, 0o700); err != nil {
			t.Fatalf("create R64 healthy workspace %d: %v", index, err)
		}
		marker := recoveryReconciliationWorkspaceMarkerForTest(t, fixture, fixture.material, jobID)
		if err := os.WriteFile(filepath.Join(jobPath, recoveryWorkspaceMarkerFileName), marker, 0o600); err != nil {
			t.Fatalf("write R64 healthy marker %d: %v", index, err)
		}
		privateLocator := recoveryWorkspaceLocatorDirectory + "/" + jobID
		expected[jobID] = targetReconciliationExpected{
			jobID: jobID, entryKind: TargetEntryDirectory, remoteState: recoveryReconciliationRemoteFinal,
			markerBindingDigest: recoveryWorkspaceMarkerBindingDigest(
				fixture.material, jobID, fixture.binding.RootID, fixture.binding.RootRevision, privateLocator,
				RecoveryWorkerClaim{WorkerID: "r63-historical-owner", AttemptFence: 41},
			),
			markerCreatorID: "r63-historical-owner", markerCreatorFence: 41,
		}
		names = append(names, jobID)
		markers[jobID] = append([]byte(nil), marker...)
	}
	return expected, names, markers
}

func recoveryReconciliationAuditKeyForTargetTest() [sha256.Size]byte {
	var key [sha256.Size]byte
	copy(key[:], []byte("FAKE_R64_AUDIT_TOKEN_KEY_FOR_TEST"))
	return key
}

func recoveryReconciliationPermitWithAuditForTargetTest(
	t *testing.T,
	fixture *recoveryLocalSFTPTargetFixture,
	expectedByComponent map[string]targetReconciliationExpected,
	auditKey [sha256.Size]byte,
	auditKeyVersion int,
	admissionGeneration string,
	cursor string,
	validate bool,
) (TargetReconciliationPermit, TargetReconciliationRequest) {
	t.Helper()
	session := recoveryTargetReconciliationSessionBinding{
		nodeID: fixture.binding.NodeID, nodeRevision: fixture.binding.NodeRevision,
		credentialRevision: fixture.binding.CredentialRevision,
		rootID:             fixture.binding.RootID, rootLocator: fixture.binding.RootLocator,
		rootLocatorDigest: fixture.binding.RootLocatorDigest, rootRevision: fixture.binding.RootRevision,
	}
	session.bindingDigest = session.digest()
	if !session.valid() {
		t.Fatal("invalid R64 reconciliation session fixture")
	}
	expected := make([]targetReconciliationExpected, 0, len(expectedByComponent))
	for component, row := range expectedByComponent {
		row.componentToken = recoveryReconciliationComponentToken(
			auditKey, auditKeyVersion, session, component, row,
		)
		expected = append(expected, row)
	}
	sort.Slice(expected, func(left, right int) bool {
		if expected[left].jobID != expected[right].jobID {
			return expected[left].jobID < expected[right].jobID
		}
		return expected[left].componentToken < expected[right].componentToken
	})
	permit := TargetReconciliationPermit{
		SchemaVersion: 1, Purpose: TargetPurposeReconcile, Operation: TargetReconciliationScanRoot,
		NodeID: session.nodeID, RootID: session.rootID, RootLocatorDigest: session.rootLocatorDigest,
		RootRevision: session.rootRevision,
		PageLimit:    recoveryReconciliationPageLimit, ChainLimit: recoveryReconciliationChainLimit,
		FindingLimit: recoveryReconciliationFindingLimit, Cursor: cursor,
		AdmissionGeneration: admissionGeneration, ExpiresAt: fixture.now.Add(time.Minute),
	}
	permit.ExpectedSetDigest = recoveryReconciliationExpectedSetDigest(auditKeyVersion, session, expected)
	permit.proof = &targetReconciliationPermitProof{
		sessionBinding: session, auditKeyVersion: auditKeyVersion, auditTokenKey: auditKey,
		expected: append([]targetReconciliationExpected(nil), expected...),
	}
	permit.proof.bindingDigest = targetReconciliationPermitBindingDigest(
		auditKey, auditKeyVersion, permit, session.bindingDigest,
	)
	request := TargetReconciliationRequest{RootID: session.rootID}
	if validate {
		if err := permit.ValidateRequestAt(fixture.now, request); err != nil {
			t.Fatalf("invalid R64 reconciliation permit fixture: %v", err)
		}
	}
	return permit, request
}

func TestRecoveryReconciliationFindingLimitAcceptsConfiguredBoundedValues(t *testing.T) {
	fixture := newRecoveryLocalSFTPTargetFixture(t)
	key := sha256.Sum256([]byte("recovery-reconciliation-finding-limit-test"))
	permit, request := recoveryReconciliationPermitWithAuditForTargetTest(
		t, fixture, nil, key, 1, "reconciliation-generation", "", false,
	)

	for _, testCase := range []struct {
		name    string
		limit   int
		wantErr error
	}{
		{name: "configured default", limit: 100},
		{name: "configured hard cap", limit: recoveryReconciliationFindingLimit},
		{name: "zero fails closed", limit: 0, wantErr: ErrInvalidTargetPermit},
		{name: "above hard cap fails closed", limit: recoveryReconciliationFindingLimit + 1, wantErr: ErrInvalidTargetPermit},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			candidate := cloneRecoveryReconciliationPermitForTest(permit)
			candidate.FindingLimit = testCase.limit
			candidate.proof.bindingDigest = targetReconciliationPermitBindingDigest(
				candidate.proof.auditTokenKey, candidate.proof.auditKeyVersion, candidate,
				candidate.proof.sessionBinding.bindingDigest,
			)
			if err := candidate.ValidateRequestAt(fixture.now, request); !errors.Is(err, testCase.wantErr) {
				t.Fatalf("finding limit %d error=%v, want %v", testCase.limit, err, testCase.wantErr)
			}
		})
	}
}

func recoveryReconciliationResumePermitForTargetTest(
	t *testing.T,
	permit TargetReconciliationPermit,
	cursor string,
) TargetReconciliationPermit {
	t.Helper()
	resumed := cloneRecoveryReconciliationPermitForTest(permit)
	resumed.Cursor = cursor
	resumed.proof.bindingDigest = targetReconciliationPermitBindingDigest(
		resumed.proof.auditTokenKey, resumed.proof.auditKeyVersion, resumed,
		resumed.proof.sessionBinding.bindingDigest,
	)
	if err := resumed.ValidateRequestAt(resumed.ExpiresAt.Add(-time.Second), TargetReconciliationRequest{RootID: resumed.RootID}); err != nil {
		t.Fatalf("invalid R64 resumed permit: %v", err)
	}
	return resumed
}

func recoveryReconciliationCursorHeaderForTest(schema int, keyVersion int) string {
	encoded := make([]byte, 6+sha256.Size)
	binary.BigEndian.PutUint16(encoded[:2], uint16(schema))
	binary.BigEndian.PutUint32(encoded[2:6], uint32(keyVersion))
	return base64.RawURLEncoding.EncodeToString(encoded)
}

func recoveryReconciliationScanChainForTest(
	t *testing.T,
	target *recoverySFTPTarget,
	permit TargetReconciliationPermit,
	request TargetReconciliationRequest,
) (TargetReconciliationPage, int) {
	t.Helper()
	for pageNumber := 1; pageNumber <= recoveryReconciliationChainLimit/recoveryReconciliationPageLimit+1; pageNumber++ {
		page, err := target.ScanRecoveryRoot(context.Background(), permit, request)
		if err != nil {
			t.Fatalf("R64 chain page %d error: %v", pageNumber, err)
		}
		if page.Complete || page.NextCursor == "" {
			return page, pageNumber
		}
		permit = recoveryReconciliationResumePermitForTargetTest(t, permit, page.NextCursor)
	}
	t.Fatal("R64 chain exceeded its bounded page count")
	return TargetReconciliationPage{}, 0
}

type recoveryReconciliationRootBoundRegistryForTest struct {
	roots     []settings.RecoveryTargetRootReference
	listCalls int
}

func (registry *recoveryReconciliationRootBoundRegistryForTest) ListAllRecoveryTargetRoots(
	context.Context,
) ([]settings.RecoveryTargetRootReference, error) {
	registry.listCalls++
	return append([]settings.RecoveryTargetRootReference(nil), registry.roots...), nil
}

func (*recoveryReconciliationRootBoundRegistryForTest) ResolveRecoveryTargetRootTx(
	context.Context,
	*gorm.DB,
	uint,
	string,
) (settings.RecoveryTargetRootResolution, error) {
	return settings.RecoveryTargetRootResolution{}, settings.ErrRecoveryTargetRootNotFound
}

func recoveryReconciliationExpectedWorkspaceForTest(
	fixture *recoveryLocalSFTPTargetFixture,
) targetReconciliationExpected {
	return targetReconciliationExpected{
		jobID: fixture.writePermit.permit.JobID, entryKind: TargetEntryDirectory,
		remoteState:         recoveryReconciliationRemoteFinal,
		markerBindingDigest: fixture.createRequest.MarkerBindingDigest,
		markerCreatorID:     fixture.createRequest.MarkerCreatorID,
		markerCreatorFence:  fixture.createRequest.MarkerCreatorFence,
	}
}

func recoveryReconciliationPermitForTargetTest(
	t *testing.T,
	fixture *recoveryLocalSFTPTargetFixture,
	expectedByComponent map[string]targetReconciliationExpected,
) (TargetReconciliationPermit, TargetReconciliationRequest) {
	t.Helper()
	session := recoveryTargetReconciliationSessionBinding{
		nodeID: fixture.binding.NodeID, nodeRevision: fixture.binding.NodeRevision,
		credentialRevision: fixture.binding.CredentialRevision,
		rootID:             fixture.binding.RootID, rootLocator: fixture.binding.RootLocator,
		rootLocatorDigest: fixture.binding.RootLocatorDigest, rootRevision: fixture.binding.RootRevision,
	}
	session.bindingDigest = session.digest()
	if !session.valid() {
		t.Fatal("invalid reconciliation target session fixture")
	}
	var auditKey [sha256.Size]byte
	copy(auditKey[:], []byte("FAKE_R63_AUDIT_TOKEN_KEY_FOR_TEST"))
	const auditKeyVersion = 11
	expected := make([]targetReconciliationExpected, 0, len(expectedByComponent))
	for component, row := range expectedByComponent {
		row.componentToken = recoveryReconciliationComponentToken(
			auditKey, auditKeyVersion, session, component, row,
		)
		expected = append(expected, row)
	}
	sort.Slice(expected, func(left, right int) bool {
		if expected[left].jobID != expected[right].jobID {
			return expected[left].jobID < expected[right].jobID
		}
		return expected[left].componentToken < expected[right].componentToken
	})
	permit := TargetReconciliationPermit{
		SchemaVersion: 1, Purpose: TargetPurposeReconcile, Operation: TargetReconciliationScanRoot,
		NodeID: session.nodeID, RootID: session.rootID, RootLocatorDigest: session.rootLocatorDigest,
		RootRevision: session.rootRevision,
		PageLimit:    recoveryReconciliationPageLimit, ChainLimit: recoveryReconciliationChainLimit,
		FindingLimit: recoveryReconciliationFindingLimit, ExpiresAt: fixture.now.Add(time.Minute),
	}
	permit.ExpectedSetDigest = recoveryReconciliationExpectedSetDigest(auditKeyVersion, session, expected)
	permit.proof = &targetReconciliationPermitProof{
		sessionBinding: session, auditKeyVersion: auditKeyVersion, auditTokenKey: auditKey,
		expected: append([]targetReconciliationExpected(nil), expected...),
	}
	permit.proof.bindingDigest = targetReconciliationPermitBindingDigest(
		auditKey, auditKeyVersion, permit, session.bindingDigest,
	)
	request := TargetReconciliationRequest{RootID: session.rootID}
	if err := permit.ValidateRequestAt(fixture.now, request); err != nil {
		t.Fatalf("invalid reconciliation permit fixture: %v", err)
	}
	return permit, request
}

func recoveryReconciliationWorkspaceMarkerForTest(
	t *testing.T,
	fixture *recoveryLocalSFTPTargetFixture,
	material backupasset.DomainKeyMaterial,
	jobID string,
) []byte {
	t.Helper()
	privateLocator := recoveryWorkspaceLocatorDirectory + "/" + jobID
	markerBinding := recoveryWorkspaceMarkerBindingDigest(
		material, jobID, fixture.binding.RootID, fixture.binding.RootRevision, privateLocator,
		RecoveryWorkerClaim{WorkerID: "r63-historical-owner", AttemptFence: 41},
	)
	body := recoveryWorkspaceMarkerBody{
		SchemaVersion: recoveryWorkspaceMarkerSchemaVersion, KeyVersion: material.Version,
		InstallationID: recoveryWorkspaceMarkerInstallationID(material.Key), JobID: jobID,
		RootID: fixture.binding.RootID, RootRevision: fixture.binding.RootRevision,
		OwnershipNonce:      base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x4d}, recoveryWorkspaceMarkerNonceBytes)),
		MarkerBindingDigest: markerBinding,
	}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("encode reconciliation historical marker body: %v", err)
	}
	document := recoveryWorkspaceMarkerDocument{
		SchemaVersion: body.SchemaVersion, KeyVersion: body.KeyVersion, InstallationID: body.InstallationID,
		JobID: body.JobID, RootID: body.RootID, RootRevision: body.RootRevision,
		OwnershipNonce: body.OwnershipNonce, MarkerBindingDigest: body.MarkerBindingDigest,
		AuthenticationTag: hex.EncodeToString(recoveryWorkspaceMarkerDocumentTag(material.Key, bodyBytes)),
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("encode reconciliation historical marker: %v", err)
	}
	return encoded
}

func assertRecoveryReconciliationReadOnlyForTest(
	t *testing.T,
	client *recoveryLocalSFTPClient,
) {
	t.Helper()
	if client == nil || client.mkdirCalls != 0 || client.chmodCalls != 0 || client.openFileCalls != 0 ||
		client.renameCalls != 0 || client.removeCalls != 0 || client.removeDirectoryCalls != 0 ||
		client.syncCalls != 0 {
		t.Fatalf("reconciliation crossed its read-only target boundary")
	}
	for _, request := range client.readDirRequests {
		if request != recoveryCleanupReadBatch {
			t.Fatalf("reconciliation ReadDir request=%d, want exact %d", request, recoveryCleanupReadBatch)
		}
	}
}

func assertRecoveryReconciliationFingerprintForTest(t *testing.T, value string) {
	t.Helper()
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(decoded) != sha256.Size {
		t.Fatalf("invalid reconciliation finding fingerprint")
	}
}
