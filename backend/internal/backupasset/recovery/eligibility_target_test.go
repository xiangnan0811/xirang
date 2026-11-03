package recovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"
	"xirang/backend/internal/settings"
	"xirang/backend/internal/sshutil"

	"github.com/pkg/sftp"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestRecoveryEligibilityTargetDraftPreflightDoesNotRequirePersistedPreflightRow(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_RECOVERY_DRAFT_PREFLIGHT_PLAN_KEY_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)

	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.BackupAssetRecoveryPlan{}, &model.BackupAssetRecoveryPreflight{}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	planID := strings.Repeat("1", 32)
	rootLocator := "/srv/FAKE_DRAFT_PREFLIGHT_ROOT_FOR_TEST_ONLY"
	relativeLocator := "FAKE_DRAFT_PREFLIGHT_TARGET_FOR_TEST_ONLY"
	rootDigest, err := settings.RecoveryTargetRootLocatorDigest(41, "root-a", rootLocator)
	if err != nil {
		t.Fatal(err)
	}
	pathDigest, err := TargetPathDigest("root-a", rootDigest, relativeLocator)
	if err != nil {
		t.Fatal(err)
	}
	plan := model.BackupAssetRecoveryPlan{
		ID: planID, BindingDigest: strings.Repeat("a", 64), TransitionRevision: 1,
		TargetMode: string(TargetModeInPlace), TargetNodeID: 41, TargetRootID: "root-a", EncryptedTargetRootLocator: rootLocator,
		EncryptedTargetRelativePath: relativeLocator, RootLocatorDigest: rootDigest, PathDigest: pathDigest,
		TargetBaseRevision: "node-v1", CredentialScopeRevision: "credential-v1",
		RootRevision: "sftpr1:root-v1", FilesystemRevision: "sftpf1:filesystem-v1",
		PreflightRevision: "preflight-v1", PreflightExpiresAt: now.Add(time.Hour), State: string(PlanStateDraft),
	}
	if err := db.Create(&plan).Error; err != nil {
		t.Fatal(err)
	}
	request := RecoveryEligibilityTargetObservationRequest{
		Binding: RecoveryAuthorityBinding{
			PlanID: planID, PlanBindingDigest: plan.BindingDigest, PlanTransitionRevision: 1,
			TargetMode: TargetModeInPlace, TargetNodeID: 41, TargetRootID: "root-a", RootLocatorDigest: rootDigest, PathDigest: pathDigest,
			TargetBaseRevision: "node-v1", CredentialScopeRevision: "credential-v1",
			RootRevision: plan.RootRevision, FilesystemRevision: plan.FilesystemRevision,
			PreflightID: planID, PreflightRevision: plan.PreflightRevision,
			PreflightTargetRevision: "node-v1", PreflightNodeRevision: "node-v1",
			draftPreflight: true,
		},
		TargetRoot: RecoveryEligibilityTargetRootSnapshot{
			NodeID: 41, RootID: "root-a", Locator: rootLocator, LocatorDigest: rootDigest,
		},
		Purpose: TargetPurposePreflight,
	}

	snapshot, err := (&recoveryEligibilityTargetGORMPlanSource{db: db}).ResolveRecoveryEligibilityTargetPlan(
		context.Background(), request,
	)
	if err != nil {
		t.Fatalf("resolve draft-plan preflight target without impossible preflight row: %v", err)
	}
	if snapshot.privateRelativeLocator != relativeLocator || !snapshot.expiresAt.Equal(plan.PreflightExpiresAt) {
		t.Fatalf("draft target snapshot=%#v", snapshot)
	}
	request.Binding.TargetMode = TargetModeIsolated
	if _, err := (&recoveryEligibilityTargetGORMPlanSource{db: db}).ResolveRecoveryEligibilityTargetPlan(
		context.Background(), request,
	); !errors.Is(err, ErrRecoveryTargetChanged) {
		t.Fatalf("substituted target mode error=%v, want changed", err)
	}
	request.Binding.TargetMode = TargetModeInPlace
	request.Binding.draftPreflight = false
	if _, err := (&recoveryEligibilityTargetGORMPlanSource{db: db}).ResolveRecoveryEligibilityTargetPlan(
		context.Background(), request,
	); !errors.Is(err, ErrRecoveryTargetUnavailable) {
		t.Fatalf("equal live plan/preflight IDs without private draft marker error=%v, want unavailable", err)
	}
}

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

func TestRecoveryPlanTargetEnumerationIsBoundedReadOnlyAndCompleteForEveryMode(t *testing.T) {
	now := time.Date(2026, 8, 16, 7, 0, 0, 0, time.UTC)
	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared&_txlock=immediate"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	seedRecoveryEligibilityTargetRootLockRow(t, db, 71, "root-a")
	root := "/srv/recovery"
	rootDigest, err := settings.RecoveryTargetRootLocatorDigest(71, "root-a", root)
	if err != nil {
		t.Fatal(err)
	}
	rootInfo := recoveryEligibilityTargetFileInfo{name: "recovery", mode: os.ModeDir | 0o750, modTime: now, uid: 1000, gid: 1000}
	fileInfo := recoveryEligibilityTargetFileInfo{name: "report.txt", size: 3, mode: 0o640, modTime: now, uid: 1000, gid: 1000}
	vfs := &sftp.StatVFS{Bavail: 1024, Frsize: 4096, Favail: 1024, Fsid: 77}
	registry := &recoveryEligibilityTargetRootRegistryFake{current: settings.RecoveryTargetRootResolution{
		NodeID: 71, RootID: "root-a", Locator: root, LocatorDigest: rootDigest,
		AuthorityRevision: "authority-v1", RootObservationRevision: "registration-v1",
		Policy: settings.RecoveryTargetRootPolicy{ReserveBytes: 1, ReserveInodes: 1, OverlapPolicyBinding: "overlap-v1"},
	}}
	revisions := &recoveryEligibilityTargetRevisionSourceFake{current: RecoveryNodeRevisionSnapshot{
		NodeRevision: "node-v1", CredentialRevision: "credential-v1",
	}}
	proof := issueRecoverySourceHostIdentityProof(recoverySourceHostIdentityStrictKnown, "SHA256:target-host", "known-host-entry")
	identity, ok := recoverySourceAuthenticatedNodeIdentity(proof, 71, "target.invalid:22")
	if !ok {
		t.Fatal("build strict host identity")
	}
	item := RecoveryPlanSourceItemEvidence{
		AssetRef:              backupasset.AssetRef{RecoveryPointID: strings.Repeat("1", 32), EntryID: strings.Repeat("2", 64)},
		TargetRelativeLocator: "docs/report.txt", ContentDigest: strings.Repeat("a", 64), Bytes: 3,
		DisplayClass: RecoveryDisplayClassRegular,
	}

	tests := []struct {
		name       string
		mode       TargetMode
		policy     ConflictPolicy
		wantCreate int64
		wantWrite  int64
		wantSkip   int64
		wantDelete int64
		mutate     func(*recoveryEligibilityTargetSFTPFake)
	}{
		{name: "isolated", mode: TargetModeIsolated, policy: ConflictFailOnConflict, wantCreate: 1},
		{name: "isolated unrelated symlink parent", mode: TargetModeIsolated, policy: ConflictFailOnConflict, wantCreate: 1,
			mutate: func(client *recoveryEligibilityTargetSFTPFake) {
				client.infos[root+"/docs"] = recoveryEligibilityTargetFileInfo{name: "docs", mode: os.ModeSymlink | 0o777}
			}},
		{name: "isolated unrelated non-directory parent", mode: TargetModeIsolated, policy: ConflictFailOnConflict, wantCreate: 1,
			mutate: func(client *recoveryEligibilityTargetSFTPFake) {
				client.infos[root+"/docs"] = fileInfo.withName("docs")
			}},
		{name: "isolated unrelated missing parent", mode: TargetModeIsolated, policy: ConflictFailOnConflict, wantCreate: 1,
			mutate: func(client *recoveryEligibilityTargetSFTPFake) { delete(client.infos, root+"/docs") }},
		{name: "isolated unrelated symlink leaf", mode: TargetModeIsolated, policy: ConflictFailOnConflict, wantCreate: 1,
			mutate: func(client *recoveryEligibilityTargetSFTPFake) {
				client.infos[root+"/docs/report.txt"] = recoveryEligibilityTargetFileInfo{name: "report.txt", mode: os.ModeSymlink | 0o777}
			}},
		{name: "isolated unrelated leaf metadata", mode: TargetModeIsolated, policy: ConflictFailOnConflict, wantCreate: 1,
			mutate: func(client *recoveryEligibilityTargetSFTPFake) {
				client.infos[root+"/docs/report.txt"] = recoveryEligibilityTargetFileInfo{name: "report.txt", mode: 0o777, size: 999, modTime: now.Add(time.Second)}
			}},
		{name: "in place overwrite", mode: TargetModeInPlace, policy: ConflictOverwriteSelected, wantWrite: 1},
		{name: "in place skip", mode: TargetModeInPlace, policy: ConflictSkipExisting, wantSkip: 1},
		{name: "exact mirror", mode: TargetModeInPlace, policy: ConflictExactMirror, wantWrite: 1, wantDelete: 1},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			base := &recoveryEligibilityTargetSFTPFake{
				infos: map[string]os.FileInfo{
					"/": rootInfo.withName("/"), "/srv": rootInfo.withName("srv"), root: rootInfo,
					root + "/docs": rootInfo.withName("docs"), root + "/docs/report.txt": fileInfo,
					root + "/obsolete.txt": fileInfo.withName("obsolete.txt"),
				},
				realPaths: map[string]string{}, vfs: vfs,
			}
			if testCase.mutate != nil {
				testCase.mutate(base)
			}
			for value := range base.infos {
				base.realPaths[value] = value
			}
			reader := &recoveryPlanTargetReaderFake{
				directories: map[string][]os.FileInfo{
					root:           {rootInfo.withName("docs"), fileInfo.withName("obsolete.txt")},
					root + "/docs": {fileInfo},
				},
				files: map[string]string{root + "/docs/report.txt": "old", root + "/obsolete.txt": "old"},
			}
			sessions := &recoveryEligibilityTargetSessionOpenerFake{session: &recoveryEligibilityTargetSession{
				nodeID: 71, nodeRevision: "node-v1", credentialRevision: "credential-v1",
				registeredNodeEndpoint: "target.invalid:22", authenticatedNodeIdentity: identity,
				hostIdentityProof: proof, sftp: base, planReader: reader,
			}}
			authority := newRecoveryPlanTargetEnumerationAuthorityForTest(recoveryPlanTargetEnumerationDependencies{
				DB: db, Roots: registry, Revisions: revisions, Sessions: sessions, Now: func() time.Time { return now },
			})
			result, enumerateErr := authority.EnumerateRecoveryPlanTarget(context.Background(), RecoveryPlanTargetEnumerationRequest{
				RequesterID: 41, SelectionDigest: strings.Repeat("b", 64), TargetMode: testCase.mode,
				TargetNodeID: 71, TargetRootID: "root-a", ConflictPolicy: testCase.policy,
				Items: []RecoveryPlanSourceItemEvidence{item}, MaxRows: 4, MaxBytes: 64,
				ExpiresAt: now.Add(time.Hour),
			})
			if enumerateErr != nil {
				t.Fatalf("EnumerateRecoveryPlanTarget() error=%v", enumerateErr)
			}
			impact := result.Operations.Impact
			if impact.CreateCount != testCase.wantCreate || impact.OverwriteCount != testCase.wantWrite ||
				impact.SkipCount != testCase.wantSkip || impact.DeleteCount != testCase.wantDelete ||
				result.Target.Mode != testCase.mode || result.Target.RootLocatorDigest != rootDigest ||
				sessions.calls != 1 || reader.readDirCalls > 4 || reader.openCalls > 4 {
				t.Fatalf("enumeration=%#v calls session/read/open=%d/%d/%d", result, sessions.calls, reader.readDirCalls, reader.openCalls)
			}
			if testCase.mode == TargetModeIsolated {
				if result.TargetRevision != result.Target.RootRevision || reader.openCalls != 0 || reader.readDirCalls != 0 {
					t.Fatalf("isolated enumeration inspected target contents or lost root identity: %#v", result)
				}
				for _, observedPath := range base.lstatPaths {
					if strings.HasPrefix(observedPath, root+"/") {
						t.Fatalf("isolated enumeration inspected unrelated lexical path %q", observedPath)
					}
				}
			}
			if testCase.policy == ConflictExactMirror {
				observation, observationErr := recoveryDeleteEntryMetadata(fileInfo.withName("obsolete.txt"))
				if observationErr != nil {
					t.Fatal(observationErr)
				}
				payloadDigest := sha256.Sum256([]byte("old"))
				observation.payloadFact = hex.EncodeToString(payloadDigest[:])
				wantPrior := recoveryDeleteEntryIdentityDigest(result.Target.RootRevision, "obsolete.txt", observation)
				var gotPrior string
				for _, operation := range result.Operations.Rows {
					if operation.Kind == RecoveryOperationDelete {
						gotPrior = operation.ExpectedPrior.Digest
					}
				}
				if gotPrior != wantPrior {
					t.Fatalf("exact-mirror delete prior=%q, want root-revision-bound identity %q", gotPrior, wantPrior)
				}
			}
			encoded, marshalErr := json.Marshal(result)
			formatted := fmt.Sprintf("%+v %#v", result, result)
			for _, private := range []string{root, item.TargetRelativeLocator, "obsolete.txt"} {
				if marshalErr != nil || strings.Contains(string(encoded), private) || strings.Contains(formatted, private) {
					t.Fatalf("private target evidence escaped: json=%q formatted=%q err=%v", encoded, formatted, marshalErr)
				}
			}
		})
	}
}

func TestRecoveryPlanTargetEnumerationDirectoryReadHonorsObservationDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	budget := int64(1)
	_, err := enumerateRecoveryPlanTargetInventory(
		ctx,
		&recoveryEligibilityTargetSession{planReader: recoveryPlanTargetBlockingReader{}},
		"/srv/recovery", "root-revision", 1, &budget, map[string]struct{}{}, map[string]struct{}{},
	)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("directory observation error=%v, want deadline exceeded", err)
	}
}

func TestRecoveryPlanTargetEnumerationCountsSymlinkPayloadAgainstByteBudget(t *testing.T) {
	now := time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)
	symlink := recoveryEligibilityTargetFileInfo{name: "link", mode: os.ModeSymlink | 0o777, modTime: now}
	reader := &recoveryPlanTargetReaderFake{
		directories: map[string][]os.FileInfo{"/srv/recovery": {symlink}},
		links:       map[string]string{"/srv/recovery/link": "private-target"},
	}
	client := &recoveryEligibilityTargetSFTPFake{infos: map[string]os.FileInfo{"/srv/recovery/link": symlink}}
	budget := int64(2)
	_, err := enumerateRecoveryPlanTargetInventory(
		context.Background(), &recoveryEligibilityTargetSession{sftp: client, planReader: reader},
		"/srv/recovery", "root-revision", 2, &budget, map[string]struct{}{}, map[string]struct{}{},
	)
	if !errors.Is(err, ErrRecoveryOperationLimit) {
		t.Fatalf("symlink observation error=%v, want closed byte limit", err)
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
		TargetMode: TargetModeInPlace, TargetNodeID: 71, TargetRootID: "root-c", RootLocatorDigest: rootDigest, PathDigest: pathDigest,
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

type recoveryTargetContentReadCloserForTest struct {
	reader     io.Reader
	closeErr   error
	onClose    func()
	bytesRead  int64
	closeCalls int
}

func (reader *recoveryTargetContentReadCloserForTest) Read(value []byte) (int, error) {
	n, err := reader.reader.Read(value)
	reader.bytesRead += int64(n)
	return n, err
}

func (reader *recoveryTargetContentReadCloserForTest) Close() error {
	reader.closeCalls++
	if reader.onClose != nil {
		reader.onClose()
	}
	return reader.closeErr
}

type recoveryTargetFailedReaderForTest struct{}

func (recoveryTargetFailedReaderForTest) Read([]byte) (int, error) {
	return 0, errors.New("target read failed")
}

type recoveryTargetBytesWithErrorReaderForTest struct{ value []byte }

func (reader *recoveryTargetBytesWithErrorReaderForTest) Read(value []byte) (int, error) {
	if len(reader.value) == 0 {
		return 0, io.EOF
	}
	n := copy(value, reader.value)
	reader.value = reader.value[n:]
	if len(reader.value) == 0 {
		return n, errors.New("target terminal read failed")
	}
	return n, nil
}

type recoveryTargetEOFWithBytesReaderForTest struct{ value []byte }

func (reader *recoveryTargetEOFWithBytesReaderForTest) Read(value []byte) (int, error) {
	n := copy(value, reader.value)
	reader.value = reader.value[n:]
	if len(reader.value) == 0 {
		return n, io.EOF
	}
	return n, nil
}

type recoveryTargetNoProgressAtEndForTest struct{ value *strings.Reader }

func (reader recoveryTargetNoProgressAtEndForTest) Read(value []byte) (int, error) {
	if reader.value.Len() == 0 {
		return 0, nil
	}
	return reader.value.Read(value)
}

type recoveryTargetNoOwnerInfoForTest struct{ os.FileInfo }

func (recoveryTargetNoOwnerInfoForTest) Sys() any { return nil }

type recoveryTargetContentFixtureForTest struct {
	root     string
	parent   string
	leaf     string
	request  RecoveryEligibilityTargetObservationRequest
	client   *recoveryEligibilityTargetSFTPFake
	reader   *recoveryPlanTargetReaderFake
	handle   *recoveryTargetContentReadCloserForTest
	observer *recoveryEligibilityTargetObserver
}

func newRecoveryTargetContentFixtureForTest(t *testing.T, payload string) *recoveryTargetContentFixtureForTest {
	t.Helper()
	now := time.Date(2026, 8, 15, 11, 0, 0, 0, time.UTC)
	root, relative := "/srv/recovery", "docs/item.bin"
	parent, leaf := path.Join(root, "docs"), path.Join(root, relative)
	rootInfo := recoveryEligibilityTargetFileInfo{name: "recovery", mode: os.ModeDir | 0o750, uid: 1000, gid: 1000, modTime: now}
	fileInfo := recoveryEligibilityTargetFileInfo{name: "item.bin", mode: 0o640, uid: 1001, gid: 1000, size: int64(len(payload)), modTime: now}
	vfs := &sftp.StatVFS{Fsid: 17, Bsize: 4096, Frsize: 4096, Blocks: 2000, Bavail: 1000, Files: 500, Favail: 300, Namemax: 255}
	request := recoveryEligibilityTargetObservationRequestForFailureTest(t, now, 81, root, relative)
	rootRevision, err := recoverySFTPRootObservationRevision(recoveryTargetPreflightSessionBinding{
		nodeID: request.TargetRoot.NodeID, rootID: request.TargetRoot.RootID,
		rootLocator: root, rootLocatorDigest: request.TargetRoot.LocatorDigest,
	}, rootInfo.Mode(), rootInfo.uid, rootInfo.gid, vfs.Fsid)
	if err != nil {
		t.Fatal(err)
	}
	filesystemRevision, err := recoverySFTPFilesystemObservationRevision(vfs)
	if err != nil {
		t.Fatal(err)
	}
	oldTargetRevision, err := recoverySFTPTargetAbsentRevision(rootRevision, relative)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(payload))
	request.Binding.RootRevision = rootRevision
	request.Binding.FilesystemRevision = filesystemRevision
	request.Binding.PreflightTargetRevision = oldTargetRevision
	request.TargetRoot.RootObservationRevision = rootRevision
	request.Binding.targetExpectation = recoveryAuthorityTargetExpectation{
		phase: recoveryAuthorityTargetCheckpointed, jobID: strings.Repeat("1", 32), jobTransitionRevision: 2,
		chainRevision: "chain-v1", historyDigest: strings.Repeat("a", 64), workerID: "worker-1",
		attemptID: strings.Repeat("2", 32), attemptFence: 1, nodeLeaseID: strings.Repeat("3", 32), nodeFence: 1,
		sourceLeaseID: strings.Repeat("4", 32), sourceFenceDigest: strings.Repeat("b", 64),
		anchorItemID: strings.Repeat("5", 32), anchorOperationDigest: strings.Repeat("c", 64),
		anchorCheckpointID: strings.Repeat("6", 32), expectedDigest: hex.EncodeToString(digest[:]), expectedBytes: int64(len(payload)),
	}
	client := &recoveryEligibilityTargetSFTPFake{
		infos: map[string]os.FileInfo{
			"/": rootInfo.withName("/"), "/srv": rootInfo.withName("srv"), root: rootInfo,
			parent: rootInfo.withName("docs"), leaf: fileInfo,
		},
		realPaths: map[string]string{"/": "/", "/srv": "/srv", root: root, parent: parent, leaf: leaf},
		vfs:       vfs, vfsByPath: map[string]*sftp.StatVFS{},
	}
	handle := &recoveryTargetContentReadCloserForTest{reader: strings.NewReader(payload)}
	reader := &recoveryPlanTargetReaderFake{openRead: func(value string) (io.ReadCloser, error) {
		if value != leaf {
			t.Fatalf("opened unexpected target %q", value)
		}
		return handle, nil
	}}
	proof := issueRecoverySourceHostIdentityProof(recoverySourceHostIdentityStrictKnown, "SHA256:strict-target", "known-host-entry")
	identity, ok := recoverySourceAuthenticatedNodeIdentity(proof, 81, "target.invalid:22")
	if !ok {
		t.Fatal("construct strict target host identity")
	}
	session := &recoveryEligibilityTargetSession{
		nodeID: 81, nodeRevision: "node-v1", credentialRevision: "credential-v1",
		registeredNodeEndpoint: "target.invalid:22", authenticatedNodeIdentity: identity,
		hostIdentityProof: proof, sftp: client, planReader: reader,
	}
	observer := newRecoveryEligibilityTargetObserverForTest(recoveryEligibilityTargetObserverDependencies{
		Now: func() time.Time { return now },
		Plans: &recoveryEligibilityTargetPlanSourceFake{snapshot: recoveryEligibilityTargetPlanSnapshot{
			privateRelativeLocator: relative, expiresAt: now.Add(time.Minute),
		}},
		Sessions: &recoveryEligibilityTargetSessionOpenerFake{session: session},
	})
	return &recoveryTargetContentFixtureForTest{
		root: root, parent: parent, leaf: leaf, request: request, client: client, reader: reader,
		handle: handle, observer: observer,
	}
}

func TestRecoveryEligibilityTargetCurrentAnchorContentProof(t *testing.T) {
	tests := []struct {
		name      string
		empty     bool
		wantError bool
		wantOpen  int
		mutate    func(*recoveryTargetContentFixtureForTest)
	}{
		{name: "checkpointed content", wantOpen: 1},
		{name: "empty content", empty: true, wantOpen: 1},
		{name: "bytes with EOF", wantOpen: 1, mutate: func(f *recoveryTargetContentFixtureForTest) {
			f.handle.reader = &recoveryTargetEOFWithBytesReaderForTest{value: []byte("restored")}
		}},
		{name: "current post", wantOpen: 1, mutate: func(f *recoveryTargetContentFixtureForTest) {
			expectation := &f.request.Binding.targetExpectation
			expectation.phase, expectation.anchorCheckpointID = recoveryAuthorityTargetCurrentPost, ""
			expectation.currentItemID, expectation.currentOperationDigest = expectation.anchorItemID, expectation.anchorOperationDigest
		}},
		{name: "same-size different content", wantError: true, wantOpen: 1, mutate: func(f *recoveryTargetContentFixtureForTest) {
			f.handle.reader = strings.NewReader("RESTORED")
		}},
		{name: "short stream", wantError: true, wantOpen: 1, mutate: func(f *recoveryTargetContentFixtureForTest) {
			f.handle.reader = strings.NewReader("short")
		}},
		{name: "long stream", wantError: true, wantOpen: 1, mutate: func(f *recoveryTargetContentFixtureForTest) {
			f.handle.reader = strings.NewReader("restored-extra")
		}},
		{name: "nonterminal extra probe", wantError: true, wantOpen: 1, mutate: func(f *recoveryTargetContentFixtureForTest) {
			f.handle.reader = recoveryTargetNoProgressAtEndForTest{value: strings.NewReader("restored")}
		}},
		{name: "metadata exceeds frozen size", wantError: true, mutate: func(f *recoveryTargetContentFixtureForTest) {
			info := f.client.infos[f.leaf].(recoveryEligibilityTargetFileInfo)
			info.size++
			f.client.infos[f.leaf] = info
		}},
		{name: "maximum size cannot overflow", wantError: true, wantOpen: 1, mutate: func(f *recoveryTargetContentFixtureForTest) {
			info := f.client.infos[f.leaf].(recoveryEligibilityTargetFileInfo)
			info.size = math.MaxInt64
			f.client.infos[f.leaf] = info
			f.request.Binding.targetExpectation.expectedBytes = math.MaxInt64
		}},
		{name: "failed open closes returned handle", wantError: true, wantOpen: 1, mutate: func(f *recoveryTargetContentFixtureForTest) {
			f.reader.openRead = func(string) (io.ReadCloser, error) { return f.handle, errors.New("open failed") }
		}},
		{name: "read failure", wantError: true, wantOpen: 1, mutate: func(f *recoveryTargetContentFixtureForTest) {
			f.handle.reader = recoveryTargetFailedReaderForTest{}
		}},
		{name: "exact bytes with read failure", wantError: true, wantOpen: 1, mutate: func(f *recoveryTargetContentFixtureForTest) {
			f.handle.reader = &recoveryTargetBytesWithErrorReaderForTest{value: []byte("restored")}
		}},
		{name: "close failure", wantError: true, wantOpen: 1, mutate: func(f *recoveryTargetContentFixtureForTest) {
			f.handle.closeErr = errors.New("target close failed")
		}},
		{name: "session close failure", wantError: true, wantOpen: 1, mutate: func(f *recoveryTargetContentFixtureForTest) {
			f.client.closeErr = errors.New("target session close failed")
		}},
		{name: "missing parent", wantError: true, mutate: func(f *recoveryTargetContentFixtureForTest) { delete(f.client.infos, f.parent) }},
		{name: "missing leaf", wantError: true, mutate: func(f *recoveryTargetContentFixtureForTest) { delete(f.client.infos, f.leaf) }},
		{name: "symlink parent", wantError: true, mutate: func(f *recoveryTargetContentFixtureForTest) {
			f.client.infos[f.parent] = recoveryEligibilityTargetFileInfo{name: "docs", mode: os.ModeSymlink | 0o777}
		}},
		{name: "aliased parent", wantError: true, mutate: func(f *recoveryTargetContentFixtureForTest) {
			f.client.realPaths[f.parent] = f.parent + "-alias"
		}},
		{name: "unsafe parent mode", wantError: true, mutate: func(f *recoveryTargetContentFixtureForTest) {
			info := f.client.infos[f.parent].(recoveryEligibilityTargetFileInfo)
			info.mode |= 0o002
			f.client.infos[f.parent] = info
		}},
		{name: "parent owner unavailable", wantError: true, mutate: func(f *recoveryTargetContentFixtureForTest) {
			f.client.infos[f.parent] = recoveryTargetNoOwnerInfoForTest{f.client.infos[f.parent]}
		}},
		{name: "symlink leaf", wantError: true, mutate: func(f *recoveryTargetContentFixtureForTest) {
			f.client.infos[f.leaf] = recoveryEligibilityTargetFileInfo{name: "item.bin", mode: os.ModeSymlink | 0o777}
		}},
		{name: "aliased leaf", wantError: true, mutate: func(f *recoveryTargetContentFixtureForTest) {
			f.client.realPaths[f.leaf] = f.leaf + "-alias"
		}},
		{name: "directory leaf", wantError: true, mutate: func(f *recoveryTargetContentFixtureForTest) {
			info := f.client.infos[f.leaf].(recoveryEligibilityTargetFileInfo)
			info.mode = os.ModeDir | 0o750
			f.client.infos[f.leaf] = info
		}},
		{name: "unsafe leaf mode", wantError: true, mutate: func(f *recoveryTargetContentFixtureForTest) {
			info := f.client.infos[f.leaf].(recoveryEligibilityTargetFileInfo)
			info.mode |= 0o002
			f.client.infos[f.leaf] = info
		}},
		{name: "different filesystem", wantError: true, mutate: func(f *recoveryTargetContentFixtureForTest) {
			other := *f.client.vfs
			other.Fsid++
			f.client.vfsByPath[f.parent] = &other
		}},
		{name: "frozen root changed", wantError: true, mutate: func(f *recoveryTargetContentFixtureForTest) {
			f.request.Binding.RootRevision = "different-root-revision"
		}},
		{name: "leaf owner changes during read", wantError: true, wantOpen: 1, mutate: func(f *recoveryTargetContentFixtureForTest) {
			f.handle.onClose = func() {
				info := f.client.infos[f.leaf].(recoveryEligibilityTargetFileInfo)
				info.uid++
				f.client.infos[f.leaf] = info
			}
		}},
		{name: "leaf nanosecond metadata drift", wantError: true, wantOpen: 1, mutate: func(f *recoveryTargetContentFixtureForTest) {
			f.handle.onClose = func() {
				info := f.client.infos[f.leaf].(recoveryEligibilityTargetFileInfo)
				info.modTime = info.modTime.Add(time.Nanosecond)
				f.client.infos[f.leaf] = info
			}
		}},
		{name: "leaf mode changes during read", wantError: true, wantOpen: 1, mutate: func(f *recoveryTargetContentFixtureForTest) {
			f.handle.onClose = func() {
				info := f.client.infos[f.leaf].(recoveryEligibilityTargetFileInfo)
				info.mode = 0o600
				f.client.infos[f.leaf] = info
			}
		}},
		{name: "leaf becomes symlink during read", wantError: true, wantOpen: 1, mutate: func(f *recoveryTargetContentFixtureForTest) {
			f.handle.onClose = func() {
				f.client.infos[f.leaf] = recoveryEligibilityTargetFileInfo{name: "item.bin", mode: os.ModeSymlink | 0o777}
			}
		}},
		{name: "parent owner changes during read", wantError: true, wantOpen: 1, mutate: func(f *recoveryTargetContentFixtureForTest) {
			f.handle.onClose = func() {
				info := f.client.infos[f.parent].(recoveryEligibilityTargetFileInfo)
				info.uid++
				f.client.infos[f.parent] = info
			}
		}},
		{name: "parent mode changes during read", wantError: true, wantOpen: 1, mutate: func(f *recoveryTargetContentFixtureForTest) {
			f.handle.onClose = func() {
				info := f.client.infos[f.parent].(recoveryEligibilityTargetFileInfo)
				info.mode = os.ModeDir | 0o700
				f.client.infos[f.parent] = info
			}
		}},
		{name: "parent becomes symlink during read", wantError: true, wantOpen: 1, mutate: func(f *recoveryTargetContentFixtureForTest) {
			f.handle.onClose = func() {
				f.client.infos[f.parent] = recoveryEligibilityTargetFileInfo{name: "docs", mode: os.ModeSymlink | 0o777}
			}
		}},
		{name: "parent filesystem changes during read", wantError: true, wantOpen: 1, mutate: func(f *recoveryTargetContentFixtureForTest) {
			f.handle.onClose = func() {
				other := *f.client.vfs
				other.Fsid++
				f.client.vfsByPath[f.parent] = &other
			}
		}},
		{name: "root owner changes during read", wantError: true, wantOpen: 1, mutate: func(f *recoveryTargetContentFixtureForTest) {
			f.handle.onClose = func() {
				info := f.client.infos[f.root].(recoveryEligibilityTargetFileInfo)
				info.uid++
				f.client.infos[f.root] = info
			}
		}},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			payload := "restored"
			if testCase.empty {
				payload = ""
			}
			fixture := newRecoveryTargetContentFixtureForTest(t, payload)
			if testCase.mutate != nil {
				testCase.mutate(fixture)
			}
			observed, err := fixture.observer.ObserveRecoveryEligibilityTarget(context.Background(), fixture.request)
			if testCase.wantError {
				if err == nil || observed != (RecoveryEligibilityTargetObservation{}) {
					t.Fatalf("unsafe observation=%#v error=%v", observed, err)
				}
			} else if err != nil || !observed.targetContentVerified ||
				observed.verifiedTargetDigest != fixture.request.Binding.targetExpectation.expectedDigest ||
				observed.verifiedTargetBytes != int64(len(payload)) ||
				observed.TargetRevision == fixture.request.Binding.PreflightTargetRevision {
				t.Fatalf("verified observation=%#v error=%v", observed, err)
			}
			if !testCase.wantError {
				wantRevision, revisionErr := recoverySFTPTargetPresentRevision(fixture.request.Binding.RootRevision,
					"docs/item.bin", fixture.client.infos[fixture.leaf])
				if revisionErr != nil || observed.TargetRevision != wantRevision || fixture.handle.bytesRead != int64(len(payload)) ||
					!validRecoveryEligibilityTarget(fixture.request.Binding, fixture.request.TargetRoot, observed, observed.ObservedAt) {
					t.Fatalf("verified current revision=%q want=%q bytes=%d error=%v", observed.TargetRevision,
						wantRevision, fixture.handle.bytesRead, revisionErr)
				}
			}
			if fixture.reader.openCalls != testCase.wantOpen || fixture.handle.closeCalls != testCase.wantOpen ||
				fixture.client.closeCalls != 1 || fixture.handle.bytesRead > int64(len(payload))+1 {
				t.Fatalf("open/close/session close/bytes=%d/%d/%d/%d", fixture.reader.openCalls,
					fixture.handle.closeCalls, fixture.client.closeCalls, fixture.handle.bytesRead)
			}
		})
	}
}

func TestRecoveryEligibilityTargetCurrentAnchorCancellation(t *testing.T) {
	fixture := newRecoveryTargetContentFixtureForTest(t, "restored")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fixture.handle.onClose = cancel
	observed, err := fixture.observer.ObserveRecoveryEligibilityTarget(ctx, fixture.request)
	if !errors.Is(err, context.Canceled) || observed != (RecoveryEligibilityTargetObservation{}) {
		t.Fatalf("canceled observation=%#v error=%v", observed, err)
	}
	if fixture.reader.openCalls != 1 || fixture.handle.closeCalls != 1 || fixture.client.closeCalls != 1 {
		t.Fatalf("canceled read resource counts=%d/%d/%d", fixture.reader.openCalls,
			fixture.handle.closeCalls, fixture.client.closeCalls)
	}
}

func TestRecoveryEligibilityTargetUnchangedAnchorUsesFrozenMetadata(t *testing.T) {
	for _, phase := range []recoveryAuthorityTargetPhase{
		recoveryAuthorityTargetFreshFirstWrite, recoveryAuthorityTargetCheckpointed,
	} {
		for _, matches := range []bool{true, false} {
			t.Run(fmt.Sprintf("phase-%d-matches-%t", phase, matches), func(t *testing.T) {
				fixture := newRecoveryTargetContentFixtureForTest(t, "restored")
				expectation := &fixture.request.Binding.targetExpectation
				expectation.phase, expectation.anchorCheckpointID = phase, ""
				expectation.expectedDigest, expectation.expectedBytes = "", 0
				if matches {
					revision, err := recoverySFTPTargetPresentRevision(fixture.request.Binding.RootRevision,
						"docs/item.bin", fixture.client.infos[fixture.leaf])
					if err != nil {
						t.Fatal(err)
					}
					fixture.request.Binding.PreflightTargetRevision = revision
				}
				observed, err := fixture.observer.ObserveRecoveryEligibilityTarget(context.Background(), fixture.request)
				if matches {
					if err != nil || observed.TargetRevision != fixture.request.Binding.PreflightTargetRevision ||
						observed.targetContentVerified || observed.verifiedTargetDigest != "" || observed.verifiedTargetBytes != 0 {
						t.Fatalf("frozen observation=%#v error=%v", observed, err)
					}
				} else if !errors.Is(err, ErrRecoveryTargetChanged) || observed != (RecoveryEligibilityTargetObservation{}) {
					t.Fatalf("changed frozen target observation=%#v error=%v", observed, err)
				}
				if fixture.reader.openCalls != 0 || fixture.handle.closeCalls != 0 || fixture.client.closeCalls != 1 {
					t.Fatalf("frozen observation read content or leaked session: opens=%d closes=%d session=%d",
						fixture.reader.openCalls, fixture.handle.closeCalls, fixture.client.closeCalls)
				}
			})
		}
	}
}

func TestRecoveryEligibilityTargetIsolatedIgnoresLexicalPaths(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		mutate    func(*recoveryTargetContentFixtureForTest)
		wantError bool
	}{
		{name: "unrelated regular file"},
		{name: "unrelated symlink parent", mutate: func(f *recoveryTargetContentFixtureForTest) {
			f.client.infos[f.parent] = recoveryEligibilityTargetFileInfo{name: "docs", mode: os.ModeSymlink | 0o777}
		}},
		{name: "unrelated missing parent", mutate: func(f *recoveryTargetContentFixtureForTest) { delete(f.client.infos, f.parent) }},
		{name: "unrelated non-directory parent", mutate: func(f *recoveryTargetContentFixtureForTest) {
			f.client.infos[f.parent] = recoveryEligibilityTargetFileInfo{name: "docs", mode: 0o600}
		}},
		{name: "unrelated symlink leaf", mutate: func(f *recoveryTargetContentFixtureForTest) {
			f.client.infos[f.leaf] = recoveryEligibilityTargetFileInfo{name: "item.bin", mode: os.ModeSymlink | 0o777}
		}},
		{name: "unsafe root", wantError: true, mutate: func(f *recoveryTargetContentFixtureForTest) {
			info := f.client.infos[f.root].(recoveryEligibilityTargetFileInfo)
			info.mode |= 0o002
			f.client.infos[f.root] = info
		}},
		{name: "root symlink", wantError: true, mutate: func(f *recoveryTargetContentFixtureForTest) {
			f.client.infos[f.root] = recoveryEligibilityTargetFileInfo{name: "recovery", mode: os.ModeSymlink | 0o777}
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newRecoveryTargetContentFixtureForTest(t, "restored")
			fixture.request.Binding.TargetMode = TargetModeIsolated
			fixture.request.Binding.targetExpectation = recoveryAuthorityTargetExpectation{}
			fixture.request.Binding.PreflightTargetRevision = fixture.request.Binding.RootRevision
			if testCase.mutate != nil {
				testCase.mutate(fixture)
			}
			observed, err := fixture.observer.ObserveRecoveryEligibilityTarget(context.Background(), fixture.request)
			if testCase.wantError {
				if err == nil || observed != (RecoveryEligibilityTargetObservation{}) {
					t.Fatalf("unsafe isolated root observation=%#v error=%v", observed, err)
				}
			} else if err != nil || observed.TargetRevision != observed.RootRevision ||
				observed.targetContentVerified || observed.verifiedTargetDigest != "" || observed.verifiedTargetBytes != 0 {
				t.Fatalf("isolated observation=%#v error=%v", observed, err)
			}
			if fixture.reader.openCalls != 0 || fixture.client.closeCalls != 1 {
				t.Fatalf("isolated observation read content or leaked session: opens=%d session=%d",
					fixture.reader.openCalls, fixture.client.closeCalls)
			}
			for _, value := range fixture.client.lstatPaths {
				if strings.HasPrefix(value, fixture.root+"/") {
					t.Fatalf("isolated observation inspected unrelated lexical path %q", value)
				}
			}
		})
	}
}

func TestRecoveryEligibilityTargetRejectsInvalidModeAndExpectationBeforeDial(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		mutate func(*RecoveryAuthorityBinding)
	}{
		{name: "missing mode", mutate: func(binding *RecoveryAuthorityBinding) { binding.TargetMode = "" }},
		{name: "unknown mode", mutate: func(binding *RecoveryAuthorityBinding) { binding.TargetMode = "other" }},
		{name: "isolated content expectation", mutate: func(binding *RecoveryAuthorityBinding) { binding.TargetMode = TargetModeIsolated }},
		{name: "invalid current post", mutate: func(binding *RecoveryAuthorityBinding) {
			binding.targetExpectation.phase = recoveryAuthorityTargetCurrentPost
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newRecoveryTargetContentFixtureForTest(t, "restored")
			testCase.mutate(&fixture.request.Binding)
			observed, err := fixture.observer.ObserveRecoveryEligibilityTarget(context.Background(), fixture.request)
			if !errors.Is(err, ErrRecoveryTargetUnavailable) || observed != (RecoveryEligibilityTargetObservation{}) ||
				fixture.client.lstatCalls != 0 || fixture.client.closeCalls != 0 || fixture.reader.openCalls != 0 {
				t.Fatalf("invalid target observation=%#v error=%v lstat/close/open=%d/%d/%d", observed, err,
					fixture.client.lstatCalls, fixture.client.closeCalls, fixture.reader.openCalls)
			}
		})
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
			TargetMode: TargetModeInPlace, TargetNodeID: 91, TargetRootID: rootID, RootLocatorDigest: rootDigest, PathDigest: pathDigest,
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
			TargetMode: TargetModeInPlace, TargetNodeID: nodeID, TargetRootID: rootID, RootLocatorDigest: rootDigest, PathDigest: pathDigest,
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
	lstatPaths    []string
	vfsByPath     map[string]*sftp.StatVFS
	realPaths     map[string]string
	vfs           *sftp.StatVFS
	lstatCalls    int
	realPathCalls int
	statVFSCalls  int
	closeCalls    int
	closeErr      error
}

type recoveryPlanTargetReaderFake struct {
	directories  map[string][]os.FileInfo
	files        map[string]string
	links        map[string]string
	openRead     func(string) (io.ReadCloser, error)
	readDirCalls int
	openCalls    int
}

type recoveryPlanTargetBlockingReader struct{}

func (recoveryPlanTargetBlockingReader) ReadDir(ctx context.Context, _ string, _ int) ([]os.FileInfo, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (recoveryPlanTargetBlockingReader) OpenRead(string) (io.ReadCloser, error) {
	return nil, errors.New("read unavailable")
}

func (recoveryPlanTargetBlockingReader) ReadLink(string) (string, error) {
	return "", errors.New("readlink unavailable")
}

func (fake *recoveryPlanTargetReaderFake) ReadDir(ctx context.Context, value string, maxRows int) ([]os.FileInfo, error) {
	fake.readDirCalls++
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entries, ok := fake.directories[value]
	if !ok {
		return nil, fs.ErrNotExist
	}
	if len(entries) > maxRows {
		return nil, ErrRecoveryOperationLimit
	}
	return append([]os.FileInfo(nil), entries...), nil
}

func (fake *recoveryPlanTargetReaderFake) OpenRead(value string) (io.ReadCloser, error) {
	fake.openCalls++
	if fake.openRead != nil {
		return fake.openRead(value)
	}
	contents, ok := fake.files[value]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return io.NopCloser(strings.NewReader(contents)), nil
}

func (fake *recoveryPlanTargetReaderFake) ReadLink(value string) (string, error) {
	target, ok := fake.links[value]
	if !ok {
		return "", errors.New("readlink unavailable")
	}
	return target, nil
}

func (fake *recoveryEligibilityTargetSFTPFake) Lstat(value string) (os.FileInfo, error) {
	fake.lstatCalls++
	fake.lstatPaths = append(fake.lstatPaths, value)
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

func (fake *recoveryEligibilityTargetSFTPFake) StatVFS(value string) (*sftp.StatVFS, error) {
	fake.statVFSCalls++
	if vfs, ok := fake.vfsByPath[value]; ok {
		copy := *vfs
		return &copy, nil
	}
	if fake.vfs == nil {
		return nil, errors.New("statvfs unavailable")
	}
	copy := *fake.vfs
	return &copy, nil
}

func (fake *recoveryEligibilityTargetSFTPFake) Close() error {
	fake.closeCalls++
	return fake.closeErr
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
