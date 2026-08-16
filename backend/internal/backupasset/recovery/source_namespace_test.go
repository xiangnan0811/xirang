package recovery

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/model"
	"xirang/backend/internal/sshutil"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestRecoverySourceNamespaceAuthorityCapturesObservesAndRevalidates(t *testing.T) {
	fixture := newRecoverySourceNamespaceAuthorityFixture(t)

	observation, err := fixture.authority.observe(
		context.Background(), fixture.request, fixture.pinned,
	)
	if err != nil {
		t.Fatalf("observe source namespace: %v", err)
	}
	if observation == nil || observation.proof == nil {
		t.Fatal("observe source namespace returned no sealed proof")
	}
	if observation.proof.authenticatedNodeIdentity != fixture.session.authenticatedNodeIdentity ||
		observation.proof.nodeID != fixture.snapshot.nodeID ||
		observation.proof.nodeRevision != fixture.snapshot.nodeRevision ||
		observation.proof.credentialRevision != fixture.snapshot.credentialRevision ||
		observation.proof.taskRevision != fixture.snapshot.taskRevision ||
		observation.proof.repositoryBindingRevision != fixture.snapshot.repositoryBindingRevision ||
		observation.proof.provenanceRevision != fixture.snapshot.provenanceRevision ||
		observation.proof.canonicalPath != fixture.snapshot.sourcePath ||
		observation.proof.observationRevision != fixture.observationRevision ||
		!observation.proof.observedAt.Equal(fixture.now) {
		t.Fatalf("sealed proof does not bind the exact observed source: %#v", observation)
	}
	if got, want := fixture.orderSnapshot(), []string{
		"capture", "open:recovery_preflight",
		"lstat:/", "identity:/", "realpath:/", "lstat:/srv", "identity:/srv", "realpath:/srv",
		"lstat:/srv/backups", "identity:/srv/backups", "realpath:/srv/backups",
		"lstat:/srv/backups/current", "identity:/srv/backups/current", "realpath:/srv/backups/current",
		"lstat:/", "identity:/", "realpath:/", "lstat:/srv", "identity:/srv", "realpath:/srv",
		"lstat:/srv/backups", "identity:/srv/backups", "realpath:/srv/backups",
		"lstat:/srv/backups/current", "identity:/srv/backups/current", "realpath:/srv/backups/current",
		"sftp_close", "ssh_close", "revalidate", "new_revision",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("observer order = %v, want %v", got, want)
	}
	if fixture.durable.captureTx == nil || fixture.durable.revalidateTx == nil ||
		fixture.durable.captureTx == fixture.durable.revalidateTx {
		t.Fatal("capture and revalidation must use two distinct caller-owned transactions")
	}
	if fixture.sftp.closeCount() != 1 || fixture.sshCloseCount() != 1 {
		t.Fatalf("session closes = sftp:%d ssh:%d, want exactly once",
			fixture.sftp.closeCount(), fixture.sshCloseCount())
	}
	if fixture.pinned.closeCount() != 0 {
		t.Fatal("successful observation must transfer pinned source ownership")
	}
	if err := observation.close(); err != nil {
		t.Fatalf("close observation: %v", err)
	}
	if err := observation.close(); err != nil {
		t.Fatalf("close observation again: %v", err)
	}
	if fixture.pinned.closeCount() != 1 {
		t.Fatalf("pinned source closes = %d, want exactly once", fixture.pinned.closeCount())
	}
}

func TestRecoverySourceNamespaceGORMDurableCapturesCurrentTaskNodeAndCredential(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "source-namespace-durable.sqlite")
	dsn := databasePath + "?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=ON&_synchronous=NORMAL&_txlock=immediate&_loc=UTC"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open source namespace durable database: %v", err)
	}
	if err := db.AutoMigrate(&model.Task{}, &model.Node{}, &model.SSHKey{}); err != nil {
		t.Fatalf("migrate source namespace durable database: %v", err)
	}
	now := time.Date(2026, time.August, 15, 10, 0, 0, 0, time.UTC)
	keyID := uint(91)
	rows := []any{
		&model.SSHKey{
			ID: keyID, Name: "source-key", Username: "backup", KeyType: "auto",
			PrivateKey: "PRIVATE_SOURCE_KEY", Fingerprint: "source-key-fingerprint",
			AllowedPurposes: "recovery_preflight", AllowedNodeIDs: "73",
			CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour),
		},
		&model.Node{
			ID: 73, Name: "source-node", Host: "source.internal", Port: 22, Username: "backup",
			AuthType: "key", SSHKeyID: &keyID, BackupDir: "source-node-backup",
			CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour),
		},
		&model.Task{
			ID: 41, Name: "source-task", NodeID: 73, RsyncSource: "/srv/backups/current",
			RsyncTarget: "/srv/repository", ExecutorType: "rsync", Status: "idle", Source: "manual",
			CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour),
		},
	}
	for _, row := range rows {
		if err := db.Session(&gorm.Session{SkipHooks: true}).Create(row).Error; err != nil {
			t.Fatalf("seed source namespace durable row: %v", err)
		}
	}

	request := recoverySourceNamespaceRequest{
		sourceRef: provider.RsyncRestoreSourceRef{
			PlanID: strings.Repeat("1", 32), PlanBindingDigest: strings.Repeat("2", 64),
			RepositoryID: strings.Repeat("3", 32), RecoveryPointID: strings.Repeat("4", 32),
			CatalogGenerationID: strings.Repeat("5", 32), SelectionDigest: strings.Repeat("6", 64),
			SourceRevisionDigest: strings.Repeat("7", 64), ManifestDigest: strings.Repeat("8", 64),
		},
		producingTaskID: 41, repositoryBindingRevision: "repository-binding-revision-1",
		provenanceRevision: "provenance-revision-1",
	}
	durable := newRecoverySourceNamespaceGORMDurable(func() time.Time { return now })
	var captured recoverySourceNamespaceSnapshot
	if err := db.Transaction(func(tx *gorm.DB) error {
		var captureErr error
		captured, captureErr = durable.CaptureRecoverySourceNamespaceTx(context.Background(), tx, request)
		return captureErr
	}); err != nil {
		t.Fatalf("capture production source namespace snapshot: %v", err)
	}
	if captured.sourceRef != request.sourceRef || captured.producingTaskID != request.producingTaskID ||
		captured.sourcePath != "/srv/backups/current" || captured.nodeID != 73 ||
		captured.taskRevision == "" || captured.nodeRevision == "" || captured.credentialRevision == "" ||
		captured.repositoryBindingRevision != request.repositoryBindingRevision ||
		captured.provenanceRevision != request.provenanceRevision {
		t.Fatalf("captured production snapshot is incomplete: %#v", captured)
	}

	writerDB, err := gorm.Open(sqlite.Open(databasePath+"?_journal_mode=WAL&_busy_timeout=25&_foreign_keys=ON&_synchronous=NORMAL&_loc=UTC"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open concurrent source namespace writer: %v", err)
	}
	lockReady := make(chan error, 1)
	releaseLock := make(chan struct{})
	lockDone := make(chan error, 1)
	go func() {
		lockDone <- db.Transaction(func(tx *gorm.DB) error {
			_, revalidateErr := durable.RevalidateRecoverySourceNamespaceTx(
				context.Background(), tx, request, captured,
			)
			lockReady <- revalidateErr
			if revalidateErr != nil {
				return revalidateErr
			}
			<-releaseLock
			return nil
		})
	}()
	if lockErr := <-lockReady; lockErr != nil {
		close(releaseLock)
		<-lockDone
		t.Fatalf("hold source namespace revalidation transaction: %v", lockErr)
	}
	concurrentWriteErr := writerDB.Model(&model.Task{}).
		Where("id = ?", request.producingTaskID).
		Update("status", "running").Error
	close(releaseLock)
	if lockErr := <-lockDone; lockErr != nil {
		t.Fatalf("finish source namespace revalidation transaction: %v", lockErr)
	}
	if concurrentWriteErr == nil {
		t.Fatal("concurrent Task writer committed while the read-only revalidation transaction was active")
	}
	if err := writerDB.Model(&model.Task{}).
		Where("id = ?", request.producingTaskID).
		Update("status", "idle").Error; err != nil {
		t.Fatalf("Task writer remained blocked after source namespace revalidation committed: %v", err)
	}

	if err := db.Model(&model.Task{}).Where("id = ?", request.producingTaskID).Updates(map[string]any{
		"rsync_source": "/srv/backups/replaced",
		"updated_at":   now,
	}).Error; err != nil {
		t.Fatalf("mutate producing Task source: %v", err)
	}
	var current recoverySourceNamespaceSnapshot
	if err := db.Transaction(func(tx *gorm.DB) error {
		var revalidateErr error
		current, revalidateErr = durable.RevalidateRecoverySourceNamespaceTx(context.Background(), tx, request, captured)
		return revalidateErr
	}); err != nil {
		t.Fatalf("load current production source namespace snapshot: %v", err)
	}
	if sameRecoverySourceNamespaceSnapshot(captured, current) ||
		current.sourcePath != "/srv/backups/replaced" || current.taskRevision == captured.taskRevision {
		t.Fatalf("production revalidation did not expose Task/source drift: captured=%#v current=%#v", captured, current)
	}
}

func TestRecoverySourceStrictKnownHostVerifierIsReadOnlyAndBindsVerifiedKey(t *testing.T) {
	publicKey := recoverySourceNamespaceTestPublicKey(t)
	hostname := "source.internal:22"
	knownHostsPath := filepath.Join(t.TempDir(), "known_hosts")
	knownHostsLine := knownhosts.Line([]string{knownhosts.Normalize(hostname)}, publicKey) + "\n"
	if err := os.WriteFile(knownHostsPath, []byte(knownHostsLine), 0o600); err != nil {
		t.Fatalf("write strict source known_hosts: %v", err)
	}

	verifier, err := newRecoverySourceStrictKnownHostVerifier(knownHostsPath)
	if err != nil {
		t.Fatalf("construct strict source known-host verifier: %v", err)
	}
	remote := &net.TCPAddr{IP: net.ParseIP("192.0.2.10"), Port: 22}
	if err := verifier.Verify(hostname, remote, publicKey); err != nil {
		t.Fatalf("verify registered source host key: %v", err)
	}
	authenticatedIdentity := ssh.FingerprintSHA256(publicKey)
	proof := verifier.Proof()
	if !proof.valid(authenticatedIdentity) || proof.authenticatedIdentity != authenticatedIdentity {
		t.Fatalf("strict source host proof does not bind verified key: %#v", proof)
	}

	unknownVerifier, err := newRecoverySourceStrictKnownHostVerifier(knownHostsPath)
	if err != nil {
		t.Fatalf("construct unknown-key verifier: %v", err)
	}
	if err := unknownVerifier.Verify(hostname, remote, recoverySourceNamespaceTestPublicKey(t)); err == nil {
		t.Fatal("strict source verifier accepted an unknown or mismatched key")
	}
	if unknownVerifier.Proof().valid(authenticatedIdentity) {
		t.Fatal("failed strict source verification issued a host proof")
	}
	after, err := os.ReadFile(knownHostsPath)
	if err != nil {
		t.Fatalf("read strict source known_hosts: %v", err)
	}
	if string(after) != knownHostsLine {
		t.Fatal("strict source verification modified known_hosts")
	}
}

func TestRecoverySourceAuthenticatedNodeIdentityBindsRegisteredNodeEndpoint(t *testing.T) {
	proof := issueRecoverySourceHostIdentityProof(
		recoverySourceHostIdentityStrictKnown,
		"shared-host-key-fingerprint",
		"persistent-known-host-proof",
	)
	first, ok := recoverySourceAuthenticatedNodeIdentity(proof, 73, "source-a.internal:22")
	if !ok || first == "" {
		t.Fatal("strict known-host proof did not issue a registered node identity")
	}
	same, ok := recoverySourceAuthenticatedNodeIdentity(proof, 73, "source-a.internal:22")
	if !ok || same != first {
		t.Fatal("same registered node endpoint did not reproduce its authenticated identity")
	}
	otherNode, ok := recoverySourceAuthenticatedNodeIdentity(proof, 74, "source-a.internal:22")
	if !ok || otherNode == first {
		t.Fatal("shared host key collapsed two registered node identities")
	}
	otherEndpoint, ok := recoverySourceAuthenticatedNodeIdentity(proof, 73, "source-b.internal:22")
	if !ok || otherEndpoint == first {
		t.Fatal("shared host key collapsed two registered node endpoints")
	}
	unverified := issueRecoverySourceHostIdentityProof(
		recoverySourceHostIdentityAcceptNew,
		"shared-host-key-fingerprint",
		"persistent-known-host-proof",
	)
	if identity, valid := recoverySourceAuthenticatedNodeIdentity(unverified, 73, "source-a.internal:22"); valid || identity != "" {
		t.Fatal("unverified host posture issued a registered node identity")
	}
}

func TestRecoverySourceNamespaceProductionSFTPUsesBoundedRemoteObjectIdentity(t *testing.T) {
	backend := &recoverySourceNamespaceSFTPSpy{closed: make(chan struct{}), appendOrder: func(string) {}}
	runner := &recoverySourceNamespaceCommandRunnerSpy{
		result: sshutil.CommandResult{Stdout: []byte("2049:87123:41ed")},
	}
	client := &recoverySourceNamespaceProductionSFTP{backend: backend, runner: runner}
	ctx := context.Background()
	info := recoverySourceNamespaceDirectoryInfo("/srv/backups/current")
	identity, err := client.StableIdentity(ctx, "/srv/backups/current", info)
	if err != nil {
		t.Fatalf("observe stable source object identity: %v", err)
	}
	if identity == "" || identity == fmt.Sprint(info.Mode(), info.ModTime()) {
		t.Fatal("stable source identity fell back to SFTP metadata")
	}
	if runner.calls != 1 || runner.ctx != ctx || runner.spec.Binary != "stat" ||
		!reflect.DeepEqual(runner.spec.Args, []string{"--printf=%d:%i:%f", "--", "/srv/backups/current"}) ||
		runner.spec.Timeout <= 0 || runner.spec.MaxStdoutBytes <= 0 || runner.spec.MaxStdoutBytes > 256 ||
		runner.spec.MaxStderrBytes <= 0 || runner.spec.MaxStderrBytes > 256 {
		t.Fatalf("stable identity command is not exact and bounded: %#v", runner.spec)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	runner.err = errors.New("PRIVATE_REMOTE_STAT_ERROR")
	if _, err := client.StableIdentity(canceled, "/srv/backups/current", info); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled stable identity error = %v, want context.Canceled", err)
	}
}

func TestRecoverySourceNamespaceProductionSessionsUseExactPurposeAndCloseOwnership(t *testing.T) {
	publicKey := recoverySourceNamespaceTestPublicKey(t)
	verifier := &recoverySourceStrictKnownHostVerifier{callback: func(string, net.Addr, ssh.PublicKey) error { return nil }}
	connection := &recoverySourceNamespaceSSHConnectionSpy{}
	sftpClient := &recoverySourceNamespaceSFTPSpy{closed: make(chan struct{}), appendOrder: func(string) {}}
	var builtPurpose string
	var dialAddress string
	var dialUser string
	opener := newRecoverySourceNamespaceProductionSessionsForTest(recoverySourceNamespaceProductionSessionDependencies{
		Resolve: func(context.Context, recoverySourceNamespaceSessionRequest) (recoverySourceNamespaceResolvedSession, error) {
			return recoverySourceNamespaceResolvedSession{
				node:         model.Node{ID: 73, Host: "source.internal", Port: 22, Username: "backup", AuthType: "key"},
				nodeRevision: "node-revision-1", credentialRevision: "credential-revision-1",
			}, nil
		},
		BuildAuth: func(_ model.Node, purpose string) ([]ssh.AuthMethod, error) {
			builtPurpose = purpose
			return []ssh.AuthMethod{ssh.Password("test-only")}, nil
		},
		Verifier: func() (*recoverySourceStrictKnownHostVerifier, error) { return verifier, nil },
		Dial: func(
			_ context.Context, address, user string, _ []ssh.AuthMethod, callback ssh.HostKeyCallback,
		) (recoverySourceNamespaceSSHConnection, error) {
			dialAddress = address
			dialUser = user
			if err := callback(address, &net.TCPAddr{IP: net.ParseIP("192.0.2.10"), Port: 22}, publicKey); err != nil {
				return nil, err
			}
			return connection, nil
		},
		OpenSFTP: func(recoverySourceNamespaceSSHConnection) (recoverySourceNamespaceSFTP, error) {
			return sftpClient, nil
		},
	})
	request := recoverySourceNamespaceSessionRequest{
		producingTaskID: 41, nodeID: 73, nodeRevision: "node-revision-1",
		credentialRevision: "credential-revision-1", purpose: recoverySourceNamespacePurposePreflight,
	}
	session, err := opener.OpenRecoverySourceNamespace(context.Background(), request)
	if err != nil {
		t.Fatalf("open production source namespace session: %v", err)
	}
	if session == nil {
		t.Fatal("open production source namespace session returned nil")
	}
	wantNodeIdentity, identityValid := recoverySourceAuthenticatedNodeIdentity(
		session.hostIdentityProof, request.nodeID, dialAddress,
	)
	if session.sftp != sftpClient || session.nodeID != request.nodeID ||
		session.nodeRevision != request.nodeRevision || session.credentialRevision != request.credentialRevision ||
		!identityValid || session.registeredNodeEndpoint != dialAddress ||
		session.authenticatedNodeIdentity != wantNodeIdentity ||
		builtPurpose != sshutil.PurposeRecoveryPreflight || dialAddress != "source.internal:22" || dialUser != "backup" {
		t.Fatalf("production source session is not purpose/identity exact: %#v", session)
	}
	if err := session.close(); err != nil {
		t.Fatalf("close production source session: %v", err)
	}
	if err := session.close(); err != nil {
		t.Fatalf("close production source session again: %v", err)
	}
	if connection.closeCalls != 1 || sftpClient.closeCount() != 1 {
		t.Fatalf("production source close ownership = ssh:%d sftp:%d, want once", connection.closeCalls, sftpClient.closeCount())
	}
}

func TestRecoverySourceNamespaceProductionSessionsClosePartialOpen(t *testing.T) {
	publicKey := recoverySourceNamespaceTestPublicKey(t)
	verifier := &recoverySourceStrictKnownHostVerifier{callback: func(string, net.Addr, ssh.PublicKey) error { return nil }}
	connection := &recoverySourceNamespaceSSHConnectionSpy{}
	partialSFTP := &recoverySourceNamespaceSFTPSpy{closed: make(chan struct{}), appendOrder: func(string) {}}
	opener := newRecoverySourceNamespaceProductionSessionsForTest(recoverySourceNamespaceProductionSessionDependencies{
		Resolve: func(context.Context, recoverySourceNamespaceSessionRequest) (recoverySourceNamespaceResolvedSession, error) {
			return recoverySourceNamespaceResolvedSession{
				node:         model.Node{ID: 73, Host: "source.internal", Port: 22, Username: "backup", AuthType: "key"},
				nodeRevision: "node-revision-1", credentialRevision: "credential-revision-1",
			}, nil
		},
		BuildAuth: func(model.Node, string) ([]ssh.AuthMethod, error) {
			return []ssh.AuthMethod{ssh.Password("test-only")}, nil
		},
		Verifier: func() (*recoverySourceStrictKnownHostVerifier, error) { return verifier, nil },
		Dial: func(
			_ context.Context, address, _ string, _ []ssh.AuthMethod, callback ssh.HostKeyCallback,
		) (recoverySourceNamespaceSSHConnection, error) {
			if err := callback(address, &net.TCPAddr{IP: net.ParseIP("192.0.2.10"), Port: 22}, publicKey); err != nil {
				return nil, err
			}
			return connection, nil
		},
		OpenSFTP: func(recoverySourceNamespaceSSHConnection) (recoverySourceNamespaceSFTP, error) {
			return partialSFTP, errors.New("PRIVATE_SFTP_OPEN_ERROR")
		},
	})
	request := recoverySourceNamespaceSessionRequest{
		producingTaskID: 41, nodeID: 73, nodeRevision: "node-revision-1",
		credentialRevision: "credential-revision-1", purpose: recoverySourceNamespacePurposePreflight,
	}
	if session, err := opener.OpenRecoverySourceNamespace(context.Background(), request); session != nil || err == nil {
		t.Fatalf("partial source session open got session=%v err=%v", session, err)
	}
	if connection.closeCalls != 1 || partialSFTP.closeCount() != 1 {
		t.Fatalf("partial source session close ownership = ssh:%d sftp:%d, want once", connection.closeCalls, partialSFTP.closeCount())
	}
}

func TestRecoverySourceNamespaceAuthorityRequiresStrictKnownHostIdentity(t *testing.T) {
	for _, posture := range []recoverySourceHostIdentityPosture{
		recoverySourceHostIdentityAcceptNew,
		recoverySourceHostIdentityInsecure,
		recoverySourceHostIdentityUnknown,
		"future",
	} {
		t.Run(string(posture), func(t *testing.T) {
			fixture := newRecoverySourceNamespaceAuthorityFixture(t)
			fixture.session.hostIdentityProof = issueRecoverySourceHostIdentityProof(
				posture,
				fixture.session.authenticatedNodeIdentity,
				"known-host-entry",
			)

			observation, err := fixture.authority.observe(
				context.Background(), fixture.request, fixture.pinned,
			)
			if observation != nil || !errors.Is(err, backupasset.ErrCapabilityUnavailable) {
				t.Fatalf("unproved host identity got observation=%v err=%v", observation, err)
			}
			if fixture.sftp.callCount() != 0 || fixture.durable.revalidateCalls != 0 {
				t.Fatal("unproved host identity reached namespace observation or durable revalidation")
			}
			assertRecoverySourceNamespaceResourcesClosed(t, fixture)
		})
	}
}

func TestRecoverySourceNamespaceAuthorityRejectsUnverifiedStrictHostClaim(t *testing.T) {
	fixture := newRecoverySourceNamespaceAuthorityFixture(t)
	fixture.session.hostIdentityProof = recoverySourceHostIdentityProof{
		posture:               recoverySourceHostIdentityStrictKnown,
		authenticatedIdentity: fixture.session.authenticatedNodeIdentity,
		persistentIdentity:    "known-host-entry",
	}

	observation, err := fixture.authority.observe(
		context.Background(), fixture.request, fixture.pinned,
	)
	if observation != nil || !errors.Is(err, backupasset.ErrCapabilityUnavailable) {
		t.Fatalf("unverified strict claim got observation=%v err=%v", observation, err)
	}
	if fixture.sftp.callCount() != 0 || fixture.durable.revalidateCalls != 0 {
		t.Fatal("unverified strict claim reached namespace observation or durable revalidation")
	}
	assertRecoverySourceNamespaceResourcesClosed(t, fixture)
}

func TestRecoverySourceNamespaceAuthorityRejectsUnsafePathComponents(t *testing.T) {
	testCases := []struct {
		name   string
		mutate func(*recoverySourceNamespaceAuthorityFixture)
	}{
		{name: "non canonical source", mutate: func(f *recoverySourceNamespaceAuthorityFixture) {
			f.snapshot.sourcePath = "/srv/backups/../private"
			f.durable.captured = f.snapshot
			f.durable.revalidated = f.snapshot
		}},
		{name: "relative source", mutate: func(f *recoverySourceNamespaceAuthorityFixture) {
			f.snapshot.sourcePath = "srv/backups/current"
			f.durable.captured = f.snapshot
			f.durable.revalidated = f.snapshot
		}},
		{name: "symlink component", mutate: func(f *recoverySourceNamespaceAuthorityFixture) {
			f.sftp.lstat = func(value string) (os.FileInfo, error) {
				if value == "/srv" {
					return recoverySourceNamespaceFileInfo{name: "srv", mode: os.ModeSymlink | 0o777}, nil
				}
				return recoverySourceNamespaceDirectoryInfo(value), nil
			}
		}},
		{name: "non directory component", mutate: func(f *recoverySourceNamespaceAuthorityFixture) {
			f.sftp.lstat = func(value string) (os.FileInfo, error) {
				if value == "/srv/backups" {
					return recoverySourceNamespaceFileInfo{name: "backups", mode: 0o600}, nil
				}
				return recoverySourceNamespaceDirectoryInfo(value), nil
			}
		}},
		{name: "ambiguous canonicalization", mutate: func(f *recoverySourceNamespaceAuthorityFixture) {
			f.sftp.realPath = func(value string) (string, error) {
				if value == "/srv/backups" {
					return "/private/backups", nil
				}
				return value, nil
			}
		}},
		{name: "canonical path drift", mutate: func(f *recoverySourceNamespaceAuthorityFixture) {
			calls := 0
			f.sftp.realPath = func(value string) (string, error) {
				if value == "/srv/backups/current" {
					calls++
					if calls == 2 {
						return "/srv/backups/replaced", nil
					}
				}
				return value, nil
			}
		}},
		{name: "component identity drift", mutate: func(f *recoverySourceNamespaceAuthorityFixture) {
			calls := 0
			f.sftp.lstat = func(value string) (os.FileInfo, error) {
				info := recoverySourceNamespaceDirectoryInfo(value)
				if value == "/srv" {
					calls++
					if calls == 2 {
						info.modTime = info.modTime.Add(time.Second)
					}
				}
				return info, nil
			}
		}},
		{name: "same metadata component replacement", mutate: func(f *recoverySourceNamespaceAuthorityFixture) {
			calls := 0
			f.sftp.stableIdentity = func(value string, _ os.FileInfo) (string, error) {
				if value == "/srv" {
					calls++
					if calls == 2 {
						return "server-object-replacement", nil
					}
				}
				return "server-object:" + value, nil
			}
		}},
		{name: "stable component identity unavailable", mutate: func(f *recoverySourceNamespaceAuthorityFixture) {
			f.sftp.stableIdentity = func(string, os.FileInfo) (string, error) {
				return "", errors.New("PRIVATE_STABLE_IDENTITY_UNAVAILABLE")
			}
		}},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newRecoverySourceNamespaceAuthorityFixture(t)
			testCase.mutate(fixture)

			observation, err := fixture.authority.observe(
				context.Background(), fixture.request, fixture.pinned,
			)
			if observation != nil || !errors.Is(err, backupasset.ErrCapabilityUnavailable) {
				t.Fatalf("unsafe path got observation=%v err=%v", observation, err)
			}
			if fixture.durable.revalidateCalls != 0 {
				t.Fatal("unsafe path reached durable revalidation")
			}
			assertRecoverySourceNamespaceResourcesClosed(t, fixture)
		})
	}
}

func TestRecoverySourceNamespaceAuthorityRejectsDurableDrift(t *testing.T) {
	testCases := []struct {
		name   string
		mutate func(*recoverySourceNamespaceSnapshot)
	}{
		{name: "node", mutate: func(snapshot *recoverySourceNamespaceSnapshot) { snapshot.nodeID++ }},
		{name: "node revision", mutate: func(snapshot *recoverySourceNamespaceSnapshot) { snapshot.nodeRevision = "node-revision-2" }},
		{name: "credential", mutate: func(snapshot *recoverySourceNamespaceSnapshot) { snapshot.credentialRevision = "credential-revision-2" }},
		{name: "producing task", mutate: func(snapshot *recoverySourceNamespaceSnapshot) { snapshot.producingTaskID++ }},
		{name: "task revision", mutate: func(snapshot *recoverySourceNamespaceSnapshot) { snapshot.taskRevision = "task-revision-2" }},
		{name: "source", mutate: func(snapshot *recoverySourceNamespaceSnapshot) { snapshot.sourcePath = "/srv/other" }},
		{name: "source ref", mutate: func(snapshot *recoverySourceNamespaceSnapshot) {
			snapshot.sourceRef.SelectionDigest = strings.Repeat("9", 64)
		}},
		{name: "binding", mutate: func(snapshot *recoverySourceNamespaceSnapshot) {
			snapshot.repositoryBindingRevision = "binding-revision-2"
		}},
		{name: "provenance", mutate: func(snapshot *recoverySourceNamespaceSnapshot) { snapshot.provenanceRevision = "provenance-revision-2" }},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newRecoverySourceNamespaceAuthorityFixture(t)
			current := fixture.snapshot
			testCase.mutate(&current)
			fixture.durable.revalidated = current

			observation, err := fixture.authority.observe(
				context.Background(), fixture.request, fixture.pinned,
			)
			if observation != nil || !errors.Is(err, backupasset.ErrConflict) {
				t.Fatalf("durable drift got observation=%v err=%v", observation, err)
			}
			if fixture.durable.revalidateCalls != 1 {
				t.Fatalf("revalidation calls = %d, want 1", fixture.durable.revalidateCalls)
			}
			assertRecoverySourceNamespaceResourcesClosed(t, fixture)
		})
	}
}

func TestRecoverySourceNamespaceAuthorityCancellationClosesEveryOwnerOnce(t *testing.T) {
	fixture := newRecoverySourceNamespaceAuthorityFixture(t)
	entered := make(chan struct{})
	var enteredOnce sync.Once
	fixture.sftp.lstat = func(string) (os.FileInfo, error) {
		enteredOnce.Do(func() { close(entered) })
		<-fixture.sftp.closed
		return nil, errors.New("PRIVATE_CANCELLATION_DEPENDENCY_ERROR")
	}

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := fixture.authority.observe(ctx, fixture.request, fixture.pinned)
		result <- err
	}()
	<-entered
	cancel()

	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled observation error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("canceled namespace observation did not join")
	}
	if fixture.durable.revalidateCalls != 0 {
		t.Fatal("canceled namespace observation reached durable revalidation")
	}
	assertRecoverySourceNamespaceResourcesClosed(t, fixture)
}

func TestRecoverySourceNamespaceAuthorityClosesPartialAndErrorPaths(t *testing.T) {
	testCases := []struct {
		name   string
		mutate func(*recoverySourceNamespaceAuthorityFixture)
	}{
		{name: "capture", mutate: func(f *recoverySourceNamespaceAuthorityFixture) {
			f.durable.captureErr = errors.New("PRIVATE_CAPTURE_ERROR")
		}},
		{name: "partial open", mutate: func(f *recoverySourceNamespaceAuthorityFixture) {
			f.opener.err = errors.New("PRIVATE_OPEN_ERROR")
		}},
		{name: "lstat", mutate: func(f *recoverySourceNamespaceAuthorityFixture) {
			f.sftp.lstat = func(string) (os.FileInfo, error) { return nil, errors.New("PRIVATE_LSTAT_ERROR") }
		}},
		{name: "realpath", mutate: func(f *recoverySourceNamespaceAuthorityFixture) {
			f.sftp.realPath = func(string) (string, error) { return "", errors.New("PRIVATE_REALPATH_ERROR") }
		}},
		{name: "sftp close", mutate: func(f *recoverySourceNamespaceAuthorityFixture) {
			f.sftp.closeErr = errors.New("PRIVATE_SFTP_CLOSE_ERROR")
		}},
		{name: "ssh close", mutate: func(f *recoverySourceNamespaceAuthorityFixture) {
			f.sshCloseErr = errors.New("PRIVATE_SSH_CLOSE_ERROR")
		}},
		{name: "revalidate", mutate: func(f *recoverySourceNamespaceAuthorityFixture) {
			f.durable.revalidateErr = errors.New("PRIVATE_REVALIDATE_ERROR")
		}},
		{name: "revision", mutate: func(f *recoverySourceNamespaceAuthorityFixture) {
			f.authority.newRevision = func() (string, error) { return "", errors.New("PRIVATE_REVISION_ERROR") }
		}},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newRecoverySourceNamespaceAuthorityFixture(t)
			testCase.mutate(fixture)

			observation, err := fixture.authority.observe(
				context.Background(), fixture.request, fixture.pinned,
			)
			if observation != nil || !errors.Is(err, backupasset.ErrCapabilityUnavailable) {
				t.Fatalf("error path got observation=%v err=%v", observation, err)
			}
			for _, canary := range []string{"PRIVATE_", fixture.snapshot.sourcePath, "source-host-canary"} {
				if strings.Contains(fmt.Sprint(err), canary) {
					t.Fatalf("error leaked private canary %q: %v", canary, err)
				}
			}
			assertRecoverySourceNamespaceResourcesClosed(t, fixture)
		})
	}
}

func TestRecoverySourceNamespaceAuthoritySanitizesConflictCanaries(t *testing.T) {
	fixture := newRecoverySourceNamespaceAuthorityFixture(t)
	fixture.durable.revalidateErr = fmt.Errorf(
		"PRIVATE_DB_CONFLICT_CANARY %s %s: %w",
		fixture.snapshot.sourcePath,
		fixture.snapshot.credentialRevision,
		backupasset.ErrConflict,
	)

	observation, err := fixture.authority.observe(
		context.Background(), fixture.request, fixture.pinned,
	)
	if observation != nil || !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("conflict got observation=%v err=%v, want redacted conflict", observation, err)
	}
	for _, canary := range []string{
		"PRIVATE_DB_CONFLICT_CANARY",
		fixture.snapshot.sourcePath,
		fixture.snapshot.credentialRevision,
	} {
		if strings.Contains(fmt.Sprint(err), canary) {
			t.Fatalf("conflict leaked private canary %q: %v", canary, err)
		}
	}
	assertRecoverySourceNamespaceResourcesClosed(t, fixture)
}

func TestRecoverySourceNamespaceAuthorityProductsArePrivateAndRedacted(t *testing.T) {
	fixture := newRecoverySourceNamespaceAuthorityFixture(t)
	observation, err := fixture.authority.observe(
		context.Background(), fixture.request, fixture.pinned,
	)
	if err != nil {
		t.Fatalf("observe source namespace: %v", err)
	}
	defer func() { _ = observation.close() }()

	observationType := reflect.TypeOf(observation).Elem()
	for index := 0; index < observationType.NumField(); index++ {
		field := observationType.Field(index)
		if field.PkgPath == "" {
			t.Fatalf("source namespace product exposes field %q", field.Name)
		}
	}
	encoded, err := json.Marshal(map[string]any{"observation": observation})
	if err != nil {
		t.Fatalf("marshal source namespace observation: %v", err)
	}
	formats := []string{
		fmt.Sprint(observation), fmt.Sprintf("%+v", observation), fmt.Sprintf("%#v", observation), string(encoded),
	}
	for _, formatted := range formats {
		for _, canary := range []string{
			fixture.snapshot.sourcePath,
			fixture.snapshot.nodeRevision,
			fixture.snapshot.credentialRevision,
			fixture.snapshot.repositoryBindingRevision,
			fixture.snapshot.provenanceRevision,
			fixture.session.authenticatedNodeIdentity,
		} {
			if strings.Contains(formatted, canary) {
				t.Fatalf("private product leaked %q through %q", canary, formatted)
			}
		}
	}
	if observation.proof.observationRevision != fixture.observationRevision {
		t.Fatal("observer must use an opaque revision rather than hash Task.RsyncSource")
	}
}

func TestRecoverySourceNamespaceInternalProductsAreRedacted(t *testing.T) {
	const (
		pathCanary       = "/private/source-path-canary"
		hostCanary       = "source-host-identity-canary"
		credentialCanary = "credential-revision-canary"
		privateKeyCanary = "PRIVATE_KEY_CANARY"
	)
	hostProof := issueRecoverySourceHostIdentityProof(
		recoverySourceHostIdentityStrictKnown,
		hostCanary,
		"persistent-host-identity-canary",
	)
	request := recoverySourceNamespaceRequest{
		repositoryBindingRevision: "repository-binding-canary",
		provenanceRevision:        "provenance-canary",
	}
	snapshot := recoverySourceNamespaceSnapshot{
		sourcePath: pathCanary, nodeRevision: "node-revision-canary",
		credentialRevision: credentialCanary,
	}
	sessionRequest := recoverySourceNamespaceSessionRequest{
		nodeRevision: "node-revision-canary", credentialRevision: credentialCanary,
	}
	session := &recoverySourceNamespaceSession{
		nodeRevision: "node-revision-canary", credentialRevision: credentialCanary,
		authenticatedNodeIdentity: hostCanary, hostIdentityProof: hostProof,
	}
	resolved := recoverySourceNamespaceResolvedSession{
		node: model.Node{
			Host: hostCanary, Password: "password-canary", PrivateKey: privateKeyCanary,
		},
		nodeRevision: "node-revision-canary", credentialRevision: credentialCanary,
	}
	proof := recoverySourceNamespaceProof{
		authenticatedNodeIdentity: hostCanary, credentialRevision: credentialCanary,
		canonicalPath: pathCanary, repositoryBindingRevision: "repository-binding-canary",
		provenanceRevision: "provenance-canary",
	}
	component := recoverySourceNamespaceComponent{name: pathCanary, stableIdentity: "stable-identity-canary"}
	pathObservation := recoverySourceNamespacePathObservation{
		canonicalPath: pathCanary, components: []recoverySourceNamespaceComponent{component},
	}
	verifier := &recoverySourceStrictKnownHostVerifier{proof: hostProof}

	products := []any{
		hostProof, request, snapshot, sessionRequest, session, resolved,
		proof, component, pathObservation, verifier,
	}
	canaries := []string{
		pathCanary, hostCanary, credentialCanary, privateKeyCanary,
		"password-canary", "repository-binding-canary", "provenance-canary",
		"stable-identity-canary", "persistent-host-identity-canary",
	}
	for _, product := range products {
		for _, formatted := range []string{
			fmt.Sprint(product), fmt.Sprintf("%+v", product), fmt.Sprintf("%#v", product),
		} {
			for _, canary := range canaries {
				if strings.Contains(formatted, canary) {
					t.Fatalf("internal source namespace product leaked %q through %q", canary, formatted)
				}
			}
		}
	}
}

type recoverySourceNamespaceAuthorityFixture struct {
	authority           *recoverySourceNamespaceAuthority
	request             recoverySourceNamespaceRequest
	snapshot            recoverySourceNamespaceSnapshot
	durable             *recoverySourceNamespaceDurableSpy
	opener              *recoverySourceNamespaceSessionOpenerSpy
	session             *recoverySourceNamespaceSession
	sftp                *recoverySourceNamespaceSFTPSpy
	pinned              *recoverySourceNamespacePinnedSourceSpy
	now                 time.Time
	observationRevision string
	orderMu             sync.Mutex
	order               []string
	sshCloseMu          sync.Mutex
	sshCloseCalls       int
	sshCloseErr         error
}

func newRecoverySourceNamespaceAuthorityFixture(t *testing.T) *recoverySourceNamespaceAuthorityFixture {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "source-namespace.sqlite")), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open source namespace test database: %v", err)
	}
	fixture := &recoverySourceNamespaceAuthorityFixture{
		now:                 time.Date(2026, time.August, 14, 9, 30, 0, 0, time.UTC),
		observationRevision: strings.Repeat("e", 32),
	}
	appendOrder := func(value string) {
		fixture.orderMu.Lock()
		defer fixture.orderMu.Unlock()
		fixture.order = append(fixture.order, value)
	}
	fixture.request = recoverySourceNamespaceRequest{
		sourceRef: provider.RsyncRestoreSourceRef{
			PlanID: strings.Repeat("1", 32), PlanBindingDigest: strings.Repeat("2", 64),
			RepositoryID: strings.Repeat("3", 32), RecoveryPointID: strings.Repeat("4", 32),
			CatalogGenerationID: strings.Repeat("5", 32), SelectionDigest: strings.Repeat("6", 64),
			SourceRevisionDigest: strings.Repeat("7", 64), ManifestDigest: strings.Repeat("8", 64),
		},
		producingTaskID: 41, repositoryBindingRevision: "binding-revision-1",
		provenanceRevision: "provenance-revision-1",
	}
	fixture.snapshot = recoverySourceNamespaceSnapshot{
		sourceRef: fixture.request.sourceRef, producingTaskID: fixture.request.producingTaskID,
		taskRevision: "task-revision-1", sourcePath: "/srv/backups/current", nodeID: 73,
		nodeRevision: "node-revision-1", credentialRevision: "credential-revision-1",
		repositoryBindingRevision: fixture.request.repositoryBindingRevision,
		provenanceRevision:        fixture.request.provenanceRevision,
	}
	fixture.durable = &recoverySourceNamespaceDurableSpy{
		captured: fixture.snapshot, revalidated: fixture.snapshot, appendOrder: appendOrder,
	}
	sftp := &recoverySourceNamespaceSFTPSpy{
		closed: make(chan struct{}), appendOrder: appendOrder,
	}
	hostProof := issueRecoverySourceHostIdentityProof(
		recoverySourceHostIdentityStrictKnown,
		"source-host-key-canary",
		"known-host-entry",
	)
	authenticatedNodeIdentity, identityValid := recoverySourceAuthenticatedNodeIdentity(
		hostProof, fixture.snapshot.nodeID, "source.internal:22",
	)
	if !identityValid {
		t.Fatal("construct source namespace fixture node identity")
	}
	fixture.session = &recoverySourceNamespaceSession{
		nodeID: fixture.snapshot.nodeID, nodeRevision: fixture.snapshot.nodeRevision,
		credentialRevision:        fixture.snapshot.credentialRevision,
		registeredNodeEndpoint:    "source.internal:22",
		authenticatedNodeIdentity: authenticatedNodeIdentity,
		hostIdentityProof:         hostProof,
		sftp:                      sftp,
		closeSSH: func() error {
			fixture.sshCloseMu.Lock()
			fixture.sshCloseCalls++
			fixture.sshCloseMu.Unlock()
			appendOrder("ssh_close")
			return fixture.sshCloseErr
		},
	}
	fixture.sftp = sftp
	fixture.opener = &recoverySourceNamespaceSessionOpenerSpy{
		session: fixture.session, appendOrder: appendOrder,
	}
	fixture.pinned = &recoverySourceNamespacePinnedSourceSpy{}
	fixture.authority = newRecoverySourceNamespaceAuthority(recoverySourceNamespaceAuthorityDependencies{
		DB: db, Durable: fixture.durable, Sessions: fixture.opener,
		Now: func() time.Time { return fixture.now },
		NewRevision: func() (string, error) {
			appendOrder("new_revision")
			return fixture.observationRevision, nil
		},
	})
	return fixture
}

func (fixture *recoverySourceNamespaceAuthorityFixture) orderSnapshot() []string {
	fixture.orderMu.Lock()
	defer fixture.orderMu.Unlock()
	return append([]string(nil), fixture.order...)
}

type recoverySourceNamespaceDurableSpy struct {
	captured        recoverySourceNamespaceSnapshot
	revalidated     recoverySourceNamespaceSnapshot
	captureErr      error
	revalidateErr   error
	captureTx       *gorm.DB
	revalidateTx    *gorm.DB
	captureCalls    int
	revalidateCalls int
	appendOrder     func(string)
}

func (spy *recoverySourceNamespaceDurableSpy) CaptureRecoverySourceNamespaceTx(
	_ context.Context,
	tx *gorm.DB,
	_ recoverySourceNamespaceRequest,
) (recoverySourceNamespaceSnapshot, error) {
	spy.captureCalls++
	spy.captureTx = tx
	spy.appendOrder("capture")
	return spy.captured, spy.captureErr
}

func (spy *recoverySourceNamespaceDurableSpy) RevalidateRecoverySourceNamespaceTx(
	_ context.Context,
	tx *gorm.DB,
	_ recoverySourceNamespaceRequest,
	_ recoverySourceNamespaceSnapshot,
) (recoverySourceNamespaceSnapshot, error) {
	spy.revalidateCalls++
	spy.revalidateTx = tx
	spy.appendOrder("revalidate")
	return spy.revalidated, spy.revalidateErr
}

type recoverySourceNamespaceSessionOpenerSpy struct {
	session     *recoverySourceNamespaceSession
	err         error
	calls       int
	request     recoverySourceNamespaceSessionRequest
	appendOrder func(string)
}

func (spy *recoverySourceNamespaceSessionOpenerSpy) OpenRecoverySourceNamespace(
	_ context.Context,
	request recoverySourceNamespaceSessionRequest,
) (*recoverySourceNamespaceSession, error) {
	spy.calls++
	spy.request = request
	spy.appendOrder("open:" + string(request.purpose))
	return spy.session, spy.err
}

type recoverySourceNamespaceSFTPSpy struct {
	mu             sync.Mutex
	lstat          func(string) (os.FileInfo, error)
	realPath       func(string) (string, error)
	stableIdentity func(string, os.FileInfo) (string, error)
	closeErr       error
	closeCalls     int
	calls          int
	closed         chan struct{}
	closedOnce     sync.Once
	appendOrder    func(string)
}

type recoverySourceNamespaceCommandRunnerSpy struct {
	ctx    context.Context
	spec   sshutil.CommandSpec
	result sshutil.CommandResult
	err    error
	calls  int
}

type recoverySourceNamespaceSSHConnectionSpy struct {
	closeCalls int
	closeErr   error
}

func (spy *recoverySourceNamespaceSSHConnectionSpy) Close() error {
	spy.closeCalls++
	return spy.closeErr
}

func (spy *recoverySourceNamespaceCommandRunnerSpy) Run(
	ctx context.Context,
	spec sshutil.CommandSpec,
) (sshutil.CommandResult, error) {
	spy.ctx = ctx
	spy.spec = spec
	spy.calls++
	return spy.result, spy.err
}

func recoverySourceNamespaceTestPublicKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate source namespace test key: %v", err)
	}
	sshKey, err := ssh.NewPublicKey(publicKey)
	if err != nil {
		t.Fatalf("convert source namespace test key: %v", err)
	}
	return sshKey
}

func (spy *recoverySourceNamespaceSFTPSpy) Lstat(value string) (os.FileInfo, error) {
	spy.mu.Lock()
	spy.calls++
	spy.mu.Unlock()
	spy.appendOrder("lstat:" + value)
	if spy.lstat != nil {
		return spy.lstat(value)
	}
	return recoverySourceNamespaceDirectoryInfo(value), nil
}

func (spy *recoverySourceNamespaceSFTPSpy) RealPath(value string) (string, error) {
	spy.mu.Lock()
	spy.calls++
	spy.mu.Unlock()
	spy.appendOrder("realpath:" + value)
	if spy.realPath != nil {
		return spy.realPath(value)
	}
	return value, nil
}

func (spy *recoverySourceNamespaceSFTPSpy) StableIdentity(_ context.Context, value string, info os.FileInfo) (string, error) {
	spy.mu.Lock()
	spy.calls++
	spy.mu.Unlock()
	spy.appendOrder("identity:" + value)
	if spy.stableIdentity != nil {
		return spy.stableIdentity(value, info)
	}
	return "server-object:" + value, nil
}

func (spy *recoverySourceNamespaceSFTPSpy) Close() error {
	spy.mu.Lock()
	spy.closeCalls++
	spy.mu.Unlock()
	spy.appendOrder("sftp_close")
	spy.closedOnce.Do(func() { close(spy.closed) })
	return spy.closeErr
}

func (spy *recoverySourceNamespaceSFTPSpy) closeCount() int {
	spy.mu.Lock()
	defer spy.mu.Unlock()
	return spy.closeCalls
}

func (spy *recoverySourceNamespaceSFTPSpy) callCount() int {
	spy.mu.Lock()
	defer spy.mu.Unlock()
	return spy.calls
}

func (fixture *recoverySourceNamespaceAuthorityFixture) sshCloseCount() int {
	fixture.sshCloseMu.Lock()
	defer fixture.sshCloseMu.Unlock()
	return fixture.sshCloseCalls
}

type recoverySourceNamespacePinnedSourceSpy struct {
	mu         sync.Mutex
	closeCalls int
	closeErr   error
}

func (*recoverySourceNamespacePinnedSourceSpy) OpenDeclaredRegular(context.Context, provider.RestoreEntry) (provider.RsyncRestoreSourceStream, error) {
	return nil, errors.New("not used")
}

func (*recoverySourceNamespacePinnedSourceSpy) MaterializeDeclaredEntries(context.Context, []provider.RestoreEntry) ([]provider.RestoreEntry, error) {
	return nil, errors.New("not used")
}

func (*recoverySourceNamespacePinnedSourceSpy) Revalidate(context.Context) error { return nil }

func (spy *recoverySourceNamespacePinnedSourceSpy) Close() error {
	spy.mu.Lock()
	defer spy.mu.Unlock()
	spy.closeCalls++
	return spy.closeErr
}

func (spy *recoverySourceNamespacePinnedSourceSpy) closeCount() int {
	spy.mu.Lock()
	defer spy.mu.Unlock()
	return spy.closeCalls
}

type recoverySourceNamespaceFileInfo struct {
	name    string
	mode    os.FileMode
	modTime time.Time
}

func (info recoverySourceNamespaceFileInfo) Name() string       { return info.name }
func (recoverySourceNamespaceFileInfo) Size() int64             { return 0 }
func (info recoverySourceNamespaceFileInfo) Mode() os.FileMode  { return info.mode }
func (info recoverySourceNamespaceFileInfo) ModTime() time.Time { return info.modTime }
func (info recoverySourceNamespaceFileInfo) IsDir() bool        { return info.mode.IsDir() }
func (recoverySourceNamespaceFileInfo) Sys() any                { return nil }

func recoverySourceNamespaceDirectoryInfo(value string) recoverySourceNamespaceFileInfo {
	return recoverySourceNamespaceFileInfo{
		name: filepath.Base(value), mode: os.ModeDir | 0o700,
		modTime: time.Date(2026, time.August, 14, 9, 0, 0, 0, time.UTC),
	}
}

func assertRecoverySourceNamespaceResourcesClosed(
	t *testing.T,
	fixture *recoverySourceNamespaceAuthorityFixture,
) {
	t.Helper()
	if fixture.pinned.closeCount() != 1 {
		t.Fatalf("pinned source closes = %d, want exactly once", fixture.pinned.closeCount())
	}
	if fixture.opener.calls > 0 && (fixture.sftp.closeCount() != 1 || fixture.sshCloseCount() != 1) {
		t.Fatalf("session closes = sftp:%d ssh:%d, want exactly once",
			fixture.sftp.closeCount(), fixture.sshCloseCount())
	}
}
