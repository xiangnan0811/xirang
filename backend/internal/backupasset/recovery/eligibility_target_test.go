package recovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/model"
	"xirang/backend/internal/settings"
	"xirang/backend/internal/sshutil"

	"github.com/pkg/sftp"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestRecoveryEligibilityTargetRootPortCapturesAndRevalidatesExactV2Authority(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:eligibility-target-root?mode=memory&cache=shared&_txlock=immediate"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open target-root test database: %v", err)
	}
	locatorDigest, err := settings.RecoveryTargetRootLocatorDigest(41, "root-a", "/srv/recovery")
	if err != nil {
		t.Fatalf("target-root locator digest: %v", err)
	}
	seedRecoveryEligibilityTargetRootLockRow(t, db, 41, "root-a")
	registry := &recoveryEligibilityTargetRootRegistryFake{current: settings.RecoveryTargetRootResolution{
		NodeID: 41, RootID: "root-a", Locator: "/srv/recovery", LocatorDigest: locatorDigest,
		AuthorityRevision: "authority-v1", RootObservationRevision: "sftpr1:root-v1",
		Policy: settings.RecoveryTargetRootPolicy{
			ReserveBytes: 4096, ReserveInodes: 32, OverlapPolicyBinding: "overlap-policy-v1",
		},
	}}
	revisions := &recoveryEligibilityTargetRevisionSourceFake{current: RecoveryNodeRevisionSnapshot{
		NodeRevision: "node-v1", CredentialRevision: "credential-v1",
	}}
	port, err := NewRecoveryEligibilityTargetRootAuthority(
		RecoveryEligibilityTargetRootAuthorityDependencies{Registry: registry, Revisions: revisions},
	)
	if err != nil {
		t.Fatalf("construct target-root authority: %v", err)
	}
	binding := RecoveryAuthorityBinding{
		TargetNodeID: 41, TargetRootID: "root-a", RootLocatorDigest: locatorDigest,
		PreflightNodeRevision: "node-v1", CredentialScopeRevision: "credential-v1",
	}
	var captured RecoveryEligibilityTargetRootSnapshot
	if err := db.Transaction(func(tx *gorm.DB) error {
		var captureErr error
		captured, captureErr = port.CaptureRecoveryEligibilityTargetRootTx(context.Background(), tx, binding)
		return captureErr
	}); err != nil {
		t.Fatalf("capture exact target root: %v", err)
	}
	if captured.NodeID != registry.current.NodeID || captured.RootID != registry.current.RootID ||
		captured.Locator != registry.current.Locator || captured.LocatorDigest != registry.current.LocatorDigest ||
		captured.AuthorityRevision != registry.current.AuthorityRevision ||
		captured.RootObservationRevision != registry.current.RootObservationRevision ||
		captured.Policy != registry.current.Policy || captured.NodeRevision != revisions.current.NodeRevision ||
		captured.CredentialRevision != revisions.current.CredentialRevision {
		t.Fatalf("captured target-root snapshot drifted: %#v", captured)
	}
	if len(revisions.purposes) != 1 || revisions.purposes[0] != TargetPurposePreflight {
		t.Fatalf("capture purposes=%v, want purpose-exact preflight", revisions.purposes)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		return port.RevalidateRecoveryEligibilityTargetRootTx(context.Background(), tx, binding, captured)
	}); err != nil {
		t.Fatalf("revalidate exact target root: %v", err)
	}

	registry.current.AuthorityRevision = "authority-v2"
	if err := db.Transaction(func(tx *gorm.DB) error {
		return port.RevalidateRecoveryEligibilityTargetRootTx(context.Background(), tx, binding, captured)
	}); !errors.Is(err, ErrRecoveryTargetChanged) {
		t.Fatalf("authority drift error=%v, want ErrRecoveryTargetChanged", err)
	}
}

func TestRecoveryEligibilityTargetRootReconciliationProjectsAuthorityRevisionOnly(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:eligibility-target-reconcile?mode=memory&cache=shared&_txlock=immediate"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open reconciliation test database: %v", err)
	}
	locatorDigest, err := settings.RecoveryTargetRootLocatorDigest(51, "root-b", "/private/root")
	if err != nil {
		t.Fatalf("reconciliation target-root locator digest: %v", err)
	}
	seedRecoveryEligibilityTargetRootLockRow(t, db, 51, "root-b")
	registry := &recoveryEligibilityTargetRootRegistryFake{current: settings.RecoveryTargetRootResolution{
		NodeID: 51, RootID: "root-b", Locator: "/private/root", LocatorDigest: locatorDigest,
		AuthorityRevision: "authority-current", RootObservationRevision: "remote-observation-current",
		Policy: settings.RecoveryTargetRootPolicy{ReserveBytes: 1, ReserveInodes: 1, OverlapPolicyBinding: "policy-current"},
	}}
	revisions := &recoveryEligibilityTargetRevisionSourceFake{current: RecoveryNodeRevisionSnapshot{
		NodeRevision: "node-current", CredentialRevision: "credential-current",
	}}
	port, err := NewRecoveryEligibilityTargetRootAuthority(
		RecoveryEligibilityTargetRootAuthorityDependencies{Registry: registry, Revisions: revisions},
	)
	if err != nil {
		t.Fatalf("construct target-root authority: %v", err)
	}
	var snapshot RecoveryReconciliationRevisionSnapshot
	if err := db.Transaction(func(tx *gorm.DB) error {
		var resolveErr error
		snapshot, resolveErr = port.ResolveRecoveryReconciliationRevisionsTx(context.Background(), tx, 51, "root-b")
		return resolveErr
	}); err != nil {
		t.Fatalf("resolve reconciliation revisions: %v", err)
	}
	if snapshot.RootRevision != registry.current.AuthorityRevision ||
		snapshot.RootRevision == registry.current.RootObservationRevision ||
		snapshot.RootRevision == registry.current.LocatorDigest {
		t.Fatalf("reconciliation root revision=%q, want independent authority revision", snapshot.RootRevision)
	}
	if len(revisions.purposes) != 1 || revisions.purposes[0] != TargetPurposeReconcile {
		t.Fatalf("reconciliation purposes=%v, want purpose-exact reconcile", revisions.purposes)
	}
}

func TestRecoveryEligibilityTargetRootProductsRedactPrivateMaterial(t *testing.T) {
	canary := "FAKE_PRIVATE_TARGET_LOCATOR_FOR_B6_TEST_ONLY"
	product := RecoveryEligibilityTargetRootSnapshot{
		NodeID: 61, RootID: "root-private", Locator: "/" + canary, LocatorDigest: canary,
		AuthorityRevision: canary, RootObservationRevision: canary,
		Policy:       settings.RecoveryTargetRootPolicy{ReserveBytes: 7, ReserveInodes: 9, OverlapPolicyBinding: canary},
		NodeRevision: canary, CredentialRevision: canary,
	}
	encoded, err := json.Marshal(product)
	if err != nil {
		t.Fatalf("marshal private target root: %v", err)
	}
	for _, value := range []string{string(encoded), fmt.Sprint(product), fmt.Sprintf("%+v", product), fmt.Sprintf("%#v", product)} {
		if strings.Contains(value, canary) {
			t.Fatalf("private target-root material leaked: %q", value)
		}
	}
}

type recoveryEligibilityTargetRootRegistryFake struct {
	current settings.RecoveryTargetRootResolution
	err     error
}

func (fake *recoveryEligibilityTargetRootRegistryFake) ResolveRecoveryTargetRootTx(
	_ context.Context,
	_ *gorm.DB,
	nodeID uint,
	rootID string,
) (settings.RecoveryTargetRootResolution, error) {
	if fake == nil || fake.err != nil {
		if fake != nil && fake.err != nil {
			return settings.RecoveryTargetRootResolution{}, fake.err
		}
		return settings.RecoveryTargetRootResolution{}, errors.New("registry unavailable")
	}
	if fake.current.NodeID != nodeID || fake.current.RootID != rootID {
		return settings.RecoveryTargetRootResolution{}, settings.ErrRecoveryTargetRootNotFound
	}
	return fake.current, nil
}

type recoveryEligibilityTargetRevisionSourceFake struct {
	current  RecoveryNodeRevisionSnapshot
	err      error
	purposes []TargetPurpose
}

func (fake *recoveryEligibilityTargetRevisionSourceFake) ResolveRecoveryNodeRevisionsTx(
	_ context.Context,
	_ *gorm.DB,
	_ uint,
	purpose TargetPurpose,
) (RecoveryNodeRevisionSnapshot, error) {
	fake.purposes = append(fake.purposes, purpose)
	return fake.current, fake.err
}

var _ RecoveryTargetRootResolver = (*recoveryEligibilityTargetRootRegistryFake)(nil)
var _ RecoveryNodeRevisionSource = (*recoveryEligibilityTargetRevisionSourceFake)(nil)

func seedRecoveryEligibilityTargetRootLockRow(t *testing.T, db *gorm.DB, nodeID uint, rootID string) {
	t.Helper()
	if err := db.AutoMigrate(&model.SystemSetting{}); err != nil {
		t.Fatalf("migrate target-root lock row: %v", err)
	}
	row := model.SystemSetting{
		Key:   settings.RecoveryTargetRootKeyPrefix + fmt.Sprintf("%d.%s", nodeID, rootID),
		Value: "enc:v2:FAKE_PRIVATE_TARGET_ROOT_CIPHERTEXT_FOR_TEST_ONLY",
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("seed target-root lock row: %v", err)
	}
}

func TestRecoveryEligibilityTargetObservationRequiresStrictStableReadOnlyEvidence(t *testing.T) {
	now := time.Date(2026, 8, 15, 8, 0, 0, 0, time.UTC)
	root := "/srv/recovery"
	relative := "restore/item.bin"
	rootDigest, err := settings.RecoveryTargetRootLocatorDigest(71, "root-c", root)
	if err != nil {
		t.Fatalf("target root digest: %v", err)
	}
	pathDigest, err := TargetPathDigest("root-c", rootDigest, relative)
	if err != nil {
		t.Fatalf("target path digest: %v", err)
	}
	rootInfo := recoveryEligibilityTargetFileInfo{name: "recovery", mode: os.ModeDir | 0o750, uid: 1000, gid: 1000, modTime: now.Add(-time.Hour)}
	rootVFS := &sftp.StatVFS{Fsid: 17, Bsize: 4096, Frsize: 4096, Blocks: 2000, Bavail: 1000, Files: 500, Favail: 300, Namemax: 255}
	revisionBinding := recoveryTargetPreflightSessionBinding{
		nodeID: 71, rootID: "root-c", rootLocator: root, rootLocatorDigest: rootDigest,
	}
	rootRevision, err := recoverySFTPRootObservationRevision(revisionBinding, rootInfo.Mode(), rootInfo.uid, rootInfo.gid, rootVFS.Fsid)
	if err != nil {
		t.Fatalf("root observation revision: %v", err)
	}
	filesystemRevision, err := recoverySFTPFilesystemObservationRevision(rootVFS)
	if err != nil {
		t.Fatalf("filesystem observation revision: %v", err)
	}
	targetRevision, err := recoverySFTPTargetAbsentRevision(rootRevision, relative)
	if err != nil {
		t.Fatalf("target observation revision: %v", err)
	}
	proof := issueRecoverySourceHostIdentityProof(
		recoverySourceHostIdentityStrictKnown, "SHA256:strict-target-host", "known-host-entry-target",
	)
	identity, ok := recoverySourceAuthenticatedNodeIdentity(proof, 71, "target.example.invalid:22")
	if !ok {
		t.Fatal("construct strict target host identity")
	}
	client := &recoveryEligibilityTargetSFTPFake{
		infos: map[string]os.FileInfo{
			"/": rootInfo.withName("/"), "/srv": rootInfo.withName("srv"), root: rootInfo,
			"/var": rootInfo.withName("var"), "/var/lib": rootInfo.withName("lib"),
			"/var/lib/xirang": rootInfo.withName("xirang"),
		},
		realPaths: map[string]string{}, vfs: rootVFS,
	}
	for value := range client.infos {
		client.realPaths[value] = value
	}
	opener := &recoveryEligibilityTargetSessionOpenerFake{session: &recoveryEligibilityTargetSession{
		nodeID: 71, nodeRevision: "node-v1", credentialRevision: "credential-v1",
		registeredNodeEndpoint: "target.example.invalid:22", authenticatedNodeIdentity: identity,
		hostIdentityProof: proof, protectedRoots: []string{"/var/lib/xirang"}, sftp: client,
	}}
	observer := newRecoveryEligibilityTargetObserverForTest(recoveryEligibilityTargetObserverDependencies{
		Now: func() time.Time { return now },
		Plans: &recoveryEligibilityTargetPlanSourceFake{snapshot: recoveryEligibilityTargetPlanSnapshot{
			privateRelativeLocator: relative, expiresAt: now.Add(time.Minute),
		}},
		Sessions: opener,
	})
	if observer == nil {
		t.Fatal("construct strict target observer")
	}
	binding := RecoveryAuthorityBinding{
		PlanID: "plan-target-observation", PlanBindingDigest: strings.Repeat("d", 64), PlanTransitionRevision: 3,
		TargetNodeID: 71, TargetRootID: "root-c", RootLocatorDigest: rootDigest, PathDigest: pathDigest,
		PreflightNodeRevision: "node-v1", CredentialScopeRevision: "credential-v1",
		RootRevision: rootRevision, FilesystemRevision: filesystemRevision,
		PreflightTargetRevision: targetRevision, RequiredBytes: 100, RequiredInodes: 1,
	}
	request := RecoveryEligibilityTargetObservationRequest{
		Binding: binding,
		TargetRoot: RecoveryEligibilityTargetRootSnapshot{
			NodeID: 71, RootID: "root-c", Locator: root, LocatorDigest: rootDigest,
			AuthorityRevision: "authority-v1", RootObservationRevision: rootRevision,
			Policy:       settings.RecoveryTargetRootPolicy{ReserveBytes: 10, ReserveInodes: 2, OverlapPolicyBinding: "policy-v1"},
			NodeRevision: "node-v1", CredentialRevision: "credential-v1",
		},
		Purpose: TargetPurposePreflight, RequiredBytes: 100, RequiredInodes: 1,
	}
	observed, err := observer.ObserveRecoveryEligibilityTarget(context.Background(), request)
	if err != nil {
		t.Fatalf("observe strict target: %v", err)
	}
	if !observed.Complete || !observed.ReadOnly || observed.AuthenticatedNodeIdentity != identity ||
		observed.CanonicalRoot != root || observed.NodeRevision != "node-v1" ||
		observed.CredentialRevision != "credential-v1" || observed.RootRevision != rootRevision ||
		observed.RootObservationRevision != rootRevision || observed.FilesystemRevision != filesystemRevision ||
		observed.TargetRevision != targetRevision || observed.FreeBytes != 4096000 || observed.FreeInodes != 300 ||
		observed.OverlapsXirangRoot || !observed.ObservedAt.Equal(now) || !observed.ExpiresAt.Equal(now.Add(targetEligibilityObservationFreshness)) {
		t.Fatalf("strict target observation drifted: %#v", observed)
	}
	if client.closeCalls != 1 || opener.calls != 1 || client.lstatCalls == 0 || client.realPathCalls == 0 || client.statVFSCalls == 0 {
		t.Fatalf("target observation resource/access counts: opener=%d close=%d lstat=%d realpath=%d statvfs=%d",
			opener.calls, client.closeCalls, client.lstatCalls, client.realPathCalls, client.statVFSCalls)
	}
}

func TestRecoveryEligibilityTargetObservationRejectsUnprovedHostBeforeSFTP(t *testing.T) {
	now := time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC)
	for _, posture := range []recoverySourceHostIdentityPosture{
		recoverySourceHostIdentityAcceptNew, recoverySourceHostIdentityInsecure, recoverySourceHostIdentityUnknown,
	} {
		t.Run(string(posture), func(t *testing.T) {
			client := &recoveryEligibilityTargetSFTPFake{}
			proof := issueRecoverySourceHostIdentityProof(posture, "SHA256:unproved", "unproved-entry")
			opener := &recoveryEligibilityTargetSessionOpenerFake{session: &recoveryEligibilityTargetSession{
				nodeID: 81, nodeRevision: "node-v1", credentialRevision: "credential-v1",
				registeredNodeEndpoint: "unproved.invalid:22", authenticatedNodeIdentity: "caller-echo",
				hostIdentityProof: proof, sftp: client,
			}}
			observer := newRecoveryEligibilityTargetObserverForTest(recoveryEligibilityTargetObserverDependencies{
				Now: func() time.Time { return now },
				Plans: &recoveryEligibilityTargetPlanSourceFake{snapshot: recoveryEligibilityTargetPlanSnapshot{
					privateRelativeLocator: "restore/item", expiresAt: now.Add(time.Minute),
				}},
				Sessions: opener,
			})
			request := recoveryEligibilityTargetObservationRequestForFailureTest(t, now, 81, "/srv/unproved", "restore/item")
			observed, err := observer.ObserveRecoveryEligibilityTarget(context.Background(), request)
			if !errors.Is(err, ErrRecoveryTargetUnavailable) || observed != (RecoveryEligibilityTargetObservation{}) {
				t.Fatalf("unproved host result=%#v error=%v", observed, err)
			}
			if client.lstatCalls != 0 || client.realPathCalls != 0 || client.statVFSCalls != 0 || client.closeCalls != 1 {
				t.Fatalf("unproved host reached SFTP: lstat=%d realpath=%d statvfs=%d close=%d",
					client.lstatCalls, client.realPathCalls, client.statVFSCalls, client.closeCalls)
			}
		})
	}
}

func TestRecoveryEligibilityTargetObservationClosesOverlapAndSymlinkFacts(t *testing.T) {
	now := time.Date(2026, 8, 15, 10, 0, 0, 0, time.UTC)
	root := "/srv/recovery"
	relative := "restore/item"
	rootID := "root-overlap"
	rootDigest, err := settings.RecoveryTargetRootLocatorDigest(91, rootID, root)
	if err != nil {
		t.Fatalf("overlap root digest: %v", err)
	}
	pathDigest, err := TargetPathDigest(rootID, rootDigest, relative)
	if err != nil {
		t.Fatalf("overlap path digest: %v", err)
	}
	info := recoveryEligibilityTargetFileInfo{
		name: "directory", mode: os.ModeDir | 0o750, uid: 1000, gid: 1000, modTime: now,
	}
	filesystem := &sftp.StatVFS{
		Fsid: 23, Bsize: 4096, Frsize: 4096, Blocks: 100, Bavail: 50, Files: 100, Favail: 50, Namemax: 255,
	}
	client := &recoveryEligibilityTargetSFTPFake{
		infos: map[string]os.FileInfo{
			"/": info.withName("/"), "/srv": info.withName("srv"), root: info.withName("recovery"),
			root + "/.xirang": info.withName(".xirang"),
		},
		realPaths: map[string]string{
			"/": "/", "/srv": "/srv", root: root, root + "/.xirang": root + "/.xirang",
		},
		vfs: filesystem,
	}
	request := RecoveryEligibilityTargetObservationRequest{
		Binding: RecoveryAuthorityBinding{
			TargetNodeID: 91, TargetRootID: rootID, RootLocatorDigest: rootDigest, PathDigest: pathDigest,
		},
		TargetRoot: RecoveryEligibilityTargetRootSnapshot{
			NodeID: 91, RootID: rootID, Locator: root, LocatorDigest: rootDigest,
		},
		Purpose: TargetPurposePreflight,
	}
	state, err := observeRecoveryEligibilityTargetState(
		context.Background(), client, request, relative, []string{root + "/.xirang"},
	)
	if err != nil || !state.overlapsXirangRoot {
		t.Fatalf("closed protected-root overlap state=%#v error=%v", state, err)
	}

	client.infos["/srv"] = recoveryEligibilityTargetFileInfo{
		name: "srv", mode: os.ModeSymlink | 0o777, uid: 1000, gid: 1000, modTime: now,
	}
	state, err = observeRecoveryEligibilityTargetState(
		context.Background(), client, request, relative, []string{root + "/.xirang"},
	)
	if !errors.Is(err, ErrRecoveryTargetChanged) || state != (recoveryEligibilityTargetState{}) {
		t.Fatalf("symlink component state=%#v error=%v, want changed/closed", state, err)
	}
}

func TestRecoveryProductionTargetRootRegistrationProbeRequiresStrictStableReadOnlyEvidence(t *testing.T) {
	now := time.Date(2026, 8, 15, 11, 0, 0, 0, time.UTC)
	root := "/srv/recovery"
	rootID := "root-registration"
	rootDigest, err := settings.RecoveryTargetRootLocatorDigest(101, rootID, root)
	if err != nil {
		t.Fatal(err)
	}
	info := recoveryEligibilityTargetFileInfo{
		name: "directory", mode: os.ModeDir | 0o750, uid: 1000, gid: 1000, modTime: now,
	}
	client := &recoveryEligibilityTargetSFTPFake{
		infos: map[string]os.FileInfo{
			"/": info.withName("/"), "/srv": info.withName("srv"), root: info.withName("recovery"),
		},
		realPaths: map[string]string{"/": "/", "/srv": "/srv", root: root},
		vfs: &sftp.StatVFS{
			Fsid: 31, Bsize: 4096, Frsize: 4096, Blocks: 100, Bavail: 50,
			Files: 100, Favail: 50, Namemax: 255,
		},
	}
	proof := issueRecoverySourceHostIdentityProof(
		recoverySourceHostIdentityStrictKnown, "SHA256:strict-registration-host", "known-registration-entry",
	)
	identity, ok := recoverySourceAuthenticatedNodeIdentity(proof, 101, "registration.invalid:22")
	if !ok {
		t.Fatal("strict target-root registration host proof is invalid")
	}
	sessions := &recoveryTargetRootRegistrationSessionOpenerFake{session: &recoveryEligibilityTargetSession{
		nodeID: 101, nodeRevision: "runtime-node-v1", credentialRevision: "runtime-credential-v1",
		registeredNodeEndpoint: "registration.invalid:22", authenticatedNodeIdentity: identity,
		hostIdentityProof: proof, sftp: client,
	}}
	request := TargetRootRegistrationRequest{
		NodeID: 101, RootID: rootID, SafeLabel: "isolated recovery", Locator: root,
		Policy: settings.RecoveryTargetRootPolicy{
			ReserveBytes: 1, ReserveInodes: 1, OverlapPolicyBinding: "policy-v1",
		},
		NodeRevision: "registration-node-v1", CredentialRevision: "registration-credential-v1",
		Purpose: TargetRootRegistrationPurposeReadOnly, ReadOnly: true,
	}
	probe := newRecoveryTargetRootRegistrationProbeForTest(recoveryTargetRootRegistrationProbeDependencies{
		Now: func() time.Time { return now }, Sessions: sessions,
		Capture: func(_ context.Context, got TargetRootRegistrationRequest) (recoveryEligibilityTargetSessionRequest, error) {
			if got != request {
				return recoveryEligibilityTargetSessionRequest{}, errors.New("registration request changed")
			}
			return recoveryEligibilityTargetSessionRequest{
				nodeID: 101, nodeRevision: "runtime-node-v1", credentialRevision: "runtime-credential-v1",
				purpose: TargetPurpose(sshutil.PurposeRecoveryTargetRootRegistration),
			}, nil
		},
	})

	observed, err := probe.ObserveRecoveryTargetRoot(context.Background(), request)
	if err != nil {
		t.Fatalf("observe target-root registration: %v", err)
	}
	if observed.NodeID != request.NodeID || observed.RootID != request.RootID ||
		observed.LocatorDigest != rootDigest || observed.NodeRevision != request.NodeRevision ||
		observed.CredentialRevision != request.CredentialRevision ||
		observed.RootObservationRevision == "" || observed.Purpose != TargetRootRegistrationPurposeReadOnly ||
		!observed.ReadOnly || !observed.ObservedAt.Equal(now) {
		t.Fatalf("registration observation=%#v", observed)
	}
	if sessions.calls != 1 || client.lstatCalls != 6 || client.realPathCalls != 6 ||
		client.statVFSCalls != 4 || client.closeCalls != 1 {
		t.Fatalf("registration calls sessions=%d lstat=%d realpath=%d statvfs=%d close=%d",
			sessions.calls, client.lstatCalls, client.realPathCalls, client.statVFSCalls, client.closeCalls)
	}
}

func recoveryEligibilityTargetObservationRequestForFailureTest(
	t *testing.T,
	now time.Time,
	nodeID uint,
	root string,
	relative string,
) RecoveryEligibilityTargetObservationRequest {
	t.Helper()
	rootID := "root-failure"
	rootDigest, err := settings.RecoveryTargetRootLocatorDigest(nodeID, rootID, root)
	if err != nil {
		t.Fatalf("failure root digest: %v", err)
	}
	pathDigest, err := TargetPathDigest(rootID, rootDigest, relative)
	if err != nil {
		t.Fatalf("failure path digest: %v", err)
	}
	return RecoveryEligibilityTargetObservationRequest{
		Binding: RecoveryAuthorityBinding{
			PlanID: "plan-target-failure", PlanBindingDigest: strings.Repeat("e", 64), PlanTransitionRevision: 1,
			TargetNodeID: nodeID, TargetRootID: rootID, RootLocatorDigest: rootDigest, PathDigest: pathDigest,
			PreflightNodeRevision: "node-v1", CredentialScopeRevision: "credential-v1",
			RootRevision: "root-v1", FilesystemRevision: "filesystem-v1", PreflightTargetRevision: "target-v1",
		},
		TargetRoot: RecoveryEligibilityTargetRootSnapshot{
			NodeID: nodeID, RootID: rootID, Locator: root, LocatorDigest: rootDigest,
			AuthorityRevision: "authority-v1", RootObservationRevision: "root-v1",
			Policy:       settings.RecoveryTargetRootPolicy{ReserveBytes: 1, ReserveInodes: 1, OverlapPolicyBinding: "policy-v1"},
			NodeRevision: "node-v1", CredentialRevision: "credential-v1",
		},
		Purpose: TargetPurposePreflight,
	}
}

type recoveryEligibilityTargetFileInfo struct {
	name    string
	size    int64
	mode    os.FileMode
	modTime time.Time
	uid     uint32
	gid     uint32
}

func (info recoveryEligibilityTargetFileInfo) Name() string       { return info.name }
func (info recoveryEligibilityTargetFileInfo) Size() int64        { return info.size }
func (info recoveryEligibilityTargetFileInfo) Mode() os.FileMode  { return info.mode }
func (info recoveryEligibilityTargetFileInfo) ModTime() time.Time { return info.modTime }
func (info recoveryEligibilityTargetFileInfo) IsDir() bool        { return info.mode.IsDir() }
func (info recoveryEligibilityTargetFileInfo) Sys() any {
	return &sftp.FileStat{UID: info.uid, GID: info.gid}
}
func (info recoveryEligibilityTargetFileInfo) withName(name string) recoveryEligibilityTargetFileInfo {
	info.name = name
	return info
}

type recoveryEligibilityTargetSFTPFake struct {
	infos         map[string]os.FileInfo
	realPaths     map[string]string
	vfs           *sftp.StatVFS
	lstatCalls    int
	realPathCalls int
	statVFSCalls  int
	closeCalls    int
}

func (fake *recoveryEligibilityTargetSFTPFake) Lstat(value string) (os.FileInfo, error) {
	fake.lstatCalls++
	info, ok := fake.infos[value]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return info, nil
}

func (fake *recoveryEligibilityTargetSFTPFake) RealPath(value string) (string, error) {
	fake.realPathCalls++
	resolved, ok := fake.realPaths[value]
	if !ok {
		return "", errors.New("real path unavailable")
	}
	return resolved, nil
}

func (fake *recoveryEligibilityTargetSFTPFake) StatVFS(string) (*sftp.StatVFS, error) {
	fake.statVFSCalls++
	if fake.vfs == nil {
		return nil, errors.New("statvfs unavailable")
	}
	copy := *fake.vfs
	return &copy, nil
}

func (fake *recoveryEligibilityTargetSFTPFake) Close() error {
	fake.closeCalls++
	return nil
}

type recoveryEligibilityTargetSessionOpenerFake struct {
	session *recoveryEligibilityTargetSession
	err     error
	calls   int
}

type recoveryTargetRootRegistrationSessionOpenerFake struct {
	session *recoveryEligibilityTargetSession
	err     error
	calls   int
}

func (fake *recoveryTargetRootRegistrationSessionOpenerFake) OpenRecoveryTargetRootRegistration(
	_ context.Context,
	_ recoveryEligibilityTargetSessionRequest,
) (*recoveryEligibilityTargetSession, error) {
	fake.calls++
	return fake.session, fake.err
}

func (fake *recoveryEligibilityTargetSessionOpenerFake) OpenRecoveryEligibilityTarget(
	_ context.Context,
	_ recoveryEligibilityTargetSessionRequest,
) (*recoveryEligibilityTargetSession, error) {
	fake.calls++
	return fake.session, fake.err
}

type recoveryEligibilityTargetPlanSourceFake struct {
	snapshot recoveryEligibilityTargetPlanSnapshot
	err      error
}

func (fake *recoveryEligibilityTargetPlanSourceFake) ResolveRecoveryEligibilityTargetPlan(
	_ context.Context,
	_ RecoveryEligibilityTargetObservationRequest,
) (recoveryEligibilityTargetPlanSnapshot, error) {
	return fake.snapshot, fake.err
}

var _ recoveryEligibilityTargetSFTP = (*recoveryEligibilityTargetSFTPFake)(nil)
var _ recoveryEligibilityTargetSessionOpener = (*recoveryEligibilityTargetSessionOpenerFake)(nil)
var _ recoveryTargetRootRegistrationSessionOpener = (*recoveryTargetRootRegistrationSessionOpenerFake)(nil)
var _ recoveryEligibilityTargetPlanSource = (*recoveryEligibilityTargetPlanSourceFake)(nil)
