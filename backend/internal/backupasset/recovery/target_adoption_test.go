package recovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/model"
)

type recoveryTargetAdoptionCaseForTest struct {
	fixture  *recoveryLocalSFTPTargetFixture
	base     *recoveryLocalSFTPClient
	client   *recoveryScriptedSFTPClient
	guard    *recoveryAdoptionReadOnlyClientForTest
	target   *recoverySFTPTarget
	handoff  interruptedOperationHandoff
	permit   TargetVerifyPermit
	prior    []byte
	post     []byte
	final    string
	intent   string
	captured string
	postPath string
	marker   string
}

type recoveryAdoptionReadOnlyClientForTest struct {
	recoveryTargetSFTPClient
	writes int
	syncs  int
}

type recoveryAdoptionReadOnlyFileForTest struct {
	recoveryTargetSFTPFile
	owner *recoveryAdoptionReadOnlyClientForTest
}

func (client *recoveryAdoptionReadOnlyClientForTest) Open(name string) (recoveryTargetSFTPFile, error) {
	file, err := client.recoveryTargetSFTPClient.Open(name)
	if file == nil {
		return nil, err
	}
	return &recoveryAdoptionReadOnlyFileForTest{recoveryTargetSFTPFile: file, owner: client}, err
}

func (file *recoveryAdoptionReadOnlyFileForTest) Write(data []byte) (int, error) {
	file.owner.writes++
	return file.recoveryTargetSFTPFile.Write(data)
}

func (file *recoveryAdoptionReadOnlyFileForTest) Sync() error {
	file.owner.syncs++
	return file.recoveryTargetSFTPFile.Sync()
}

func newRecoveryTargetAdoptionCaseForTest(t *testing.T, kind RecoveryOperationKind) *recoveryTargetAdoptionCaseForTest {
	t.Helper()
	fixture := newRecoveryLocalSFTPTargetFixture(t)
	binding := fixture.binding
	jobID := fixture.writePermit.permit.JobID
	object := TargetObjectRef{
		RootID: binding.RootID, RootLocatorDigest: binding.RootLocatorDigest,
		PrivateRelativeLocator: "existing/item.bin",
	}
	object.TargetPathDigest = mustTargetPathDigest(t, object.RootID, object.RootLocatorDigest, object.PrivateRelativeLocator)
	final := filepath.Join(fixture.root, filepath.FromSlash(object.PrivateRelativeLocator))
	if err := os.MkdirAll(filepath.Dir(final), 0o700); err != nil {
		t.Fatal(err)
	}
	prior := []byte("prior payload for adoption")
	post := []byte("new payload from operation")
	if err := os.WriteFile(final, prior, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(final, 0o640); err != nil {
		t.Fatal(err)
	}
	priorSum, postSum := sha256.Sum256(prior), sha256.Sum256(post)
	priorDigest := hex.EncodeToString(priorSum[:])
	if kind == RecoveryOperationDelete {
		info, err := os.Lstat(final)
		if err != nil {
			t.Fatal(err)
		}
		priorDigest = recoveryDeleteEntryIdentityForTest(
			t, binding.RootRevision, object.PrivateRelativeLocator, TargetEntryRegular,
			recoveryOwnedFileInfoForOverwriteTest(info), 501, 502, priorDigest,
		)
	}
	item := model.BackupAssetRecoveryJobItem{
		ID: strings.Repeat("4", 32), PlanID: binding.PlanID, JobID: jobID,
		OperationKind: string(kind), TargetObjectDigest: object.TargetPathDigest,
		ExpectedPriorKind: string(ExpectedTargetPresent), ExpectedPriorDigest: priorDigest,
		ExpectedPriorBytes: int64(len(prior)), ExpectedPostIdentityDigest: hex.EncodeToString(postSum[:]),
		ExpectedPostBytes: int64(len(post)), TargetLocatorKeyVersion: fixture.material.Version,
	}
	if kind == RecoveryOperationDelete {
		item.ExpectedPriorBytes, item.ExpectedPostBytes, item.ExpectedPostIdentityDigest = -1, -1, ""
	}
	operation := RecoveryOperation{
		Kind: kind, ExpectedPrior: ExpectedTargetIdentity{Kind: ExpectedTargetPresent, Digest: priorDigest},
		TargetRelativeLocator: object.PrivateRelativeLocator,
		ExpectedPriorBytes:    item.ExpectedPriorBytes, ExpectedPostIdentityDigest: item.ExpectedPostIdentityDigest,
		ExpectedPostBytes: item.ExpectedPostBytes,
	}
	plan := model.BackupAssetRecoveryPlan{
		ID: binding.PlanID, BindingDigest: binding.PlanBindingDigest, TargetMode: string(TargetModeInPlace),
		TargetNodeID: binding.NodeID, TargetRootID: binding.RootID, TargetBaseRevision: binding.NodeRevision,
		CredentialScopeRevision: binding.CredentialRevision, EncryptedTargetRootLocator: binding.RootLocator,
		RootLocatorDigest: binding.RootLocatorDigest, RootRevision: binding.RootRevision,
	}
	job := model.BackupAssetRecoveryJob{
		ID: jobID, PlanID: plan.ID, PlanBindingDigest: plan.BindingDigest,
		TargetMode: string(TargetModeInPlace), TargetNodeID: binding.NodeID,
		TargetRootID: binding.RootID, RootLocatorDigest: binding.RootLocatorDigest,
	}
	expectation, err := recoveryOperationVerifyExpectation(operation, item)
	if err != nil {
		t.Fatal(err)
	}
	handoff := interruptedOperationHandoff{
		plan: plan, job: job, item: item, operation: operation, object: object, expectation: expectation,
		targetSessionBinding: binding, operationDigest: recoveryJobItemOperationDigest(item),
		durableDigest: strings.Repeat("d", sha256DigestLength),
	}
	value := &recoveryTargetAdoptionCaseForTest{
		fixture: fixture, prior: prior, post: post, final: final,
	}
	if kind == RecoveryOperationOverwrite {
		handoff.overwriteArtifacts, err = newRecoveryOverwriteArtifactBinding(fixture.material, recoveryOverwriteArtifactBindingInput{
			keyVersion: fixture.material.Version, planID: plan.ID, planBindingDigest: plan.BindingDigest,
			jobID: job.ID, jobItemID: item.ID, operationDigest: handoff.operationDigest,
			targetMode: TargetModeInPlace, nodeID: binding.NodeID, rootID: binding.RootID,
			rootLocatorDigest: binding.RootLocatorDigest, rootRevision: binding.RootRevision,
			object: object, expectedPrior: operation.ExpectedPrior, expectedPriorBytes: item.ExpectedPriorBytes,
			expectedPostDigest: item.ExpectedPostIdentityDigest, expectedPostBytes: item.ExpectedPostBytes,
		})
		if err != nil {
			t.Fatal(err)
		}
		value.intent = filepath.Join(filepath.Dir(final), handoff.overwriteArtifacts.intentComponent)
		value.captured = filepath.Join(filepath.Dir(final), handoff.overwriteArtifacts.priorComponent)
		value.postPath = filepath.Join(filepath.Dir(final), handoff.overwriteArtifacts.postComponent)
		value.marker = filepath.Join(filepath.Dir(final), handoff.overwriteArtifacts.publishedComponent)
	} else {
		consumed := model.BackupAssetRecoveryCheckpoint{
			ID: strings.Repeat("6", 32), DeleteGrantID: strings.Repeat("7", 32),
			DeleteGrantBindingDigest: strings.Repeat("8", sha256DigestLength),
		}
		handoff.consumedDeleteArtifacts, err = newRecoveryConsumedDeleteArtifactBinding(
			fixture.material, binding, job.ID, item, handoff.operationDigest, object, consumed,
		)
		if err != nil {
			t.Fatal(err)
		}
		handoff.deleteAuthorityConsumed = true
		value.intent = filepath.Join(filepath.Dir(final), handoff.consumedDeleteArtifacts.artifacts.intentComponent)
		value.captured = filepath.Join(filepath.Dir(final), handoff.consumedDeleteArtifacts.artifacts.capturedComponent)
		value.marker = filepath.Join(filepath.Dir(final), handoff.consumedDeleteArtifacts.artifacts.verifiedComponent)
	}
	value.handoff = handoff
	value.permit, err = newRecoveryTargetAdoptionVerifyPermit(handoff, fixture.now.Add(time.Minute), fixture.now)
	if err != nil {
		t.Fatalf("issue adoption verify permit: %v", err)
	}
	value.base = &recoveryLocalSFTPClient{}
	value.client = &recoveryScriptedSFTPClient{base: value.base}
	value.client.lstat = func(name string, _ int) (os.FileInfo, error) {
		info, err := value.base.Lstat(name)
		if err != nil {
			return info, err
		}
		return recoveryOwnedFileInfoForOverwriteTest(info), nil
	}
	value.guard = &recoveryAdoptionReadOnlyClientForTest{recoveryTargetSFTPClient: value.client}
	value.target = fixture.targetWithClient(value.guard)
	value.target.now = func() time.Time { return fixture.now }
	return value
}

func (value *recoveryTargetAdoptionCaseForTest) write(t *testing.T, name string, content []byte) {
	t.Helper()
	if err := os.WriteFile(name, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(name, 0o600); err != nil {
		t.Fatal(err)
	}
}

func (value *recoveryTargetAdoptionCaseForTest) publishOverwrite(t *testing.T) {
	t.Helper()
	if err := os.Rename(value.final, value.captured); err != nil {
		t.Fatal(err)
	}
	value.write(t, value.final, value.post)
	value.write(t, value.intent, []byte(value.handoff.overwriteArtifacts.intentDocument))
}

func (value *recoveryTargetAdoptionCaseForTest) verify(t *testing.T, wantSuccess bool) {
	t.Helper()
	observed, err := value.target.Verify(context.Background(), value.permit, value.handoff.object, value.handoff.expectation)
	if wantSuccess {
		if err != nil || observed.ValidateAgainst(value.handoff.expectation) != nil {
			t.Fatalf("adoption verification observation=%+v error=%v", observed, err)
		}
	} else if err == nil {
		t.Fatalf("unsafe adoption verification returned observation=%+v", observed)
	}
	if value.base.mkdirCalls != 0 || value.base.chmodCalls != 0 || value.base.openFileCalls != 0 ||
		value.base.renameCalls != 0 || value.base.removeCalls != 0 || value.base.removeDirectoryCalls != 0 ||
		value.guard.writes != 0 || value.guard.syncs != 0 {
		t.Fatalf("adoption verification mutated target: client=%+v writes=%d syncs=%d", value.base, value.guard.writes, value.guard.syncs)
	}
}

func TestRecoverySFTPTargetAdoptionOverwriteTuples(t *testing.T) {
	cases := []struct {
		name        string
		setup       func(*testing.T, *recoveryTargetAdoptionCaseForTest)
		wantSuccess bool
	}{
		{name: "published_unacknowledged", wantSuccess: true},
		{name: "acknowledged_with_prior_and_intent", wantSuccess: true, setup: func(t *testing.T, v *recoveryTargetAdoptionCaseForTest) {
			v.write(t, v.marker, []byte(v.handoff.overwriteArtifacts.publishedDocument))
		}},
		{name: "acknowledged_with_post_residue", wantSuccess: true, setup: func(t *testing.T, v *recoveryTargetAdoptionCaseForTest) {
			v.write(t, v.marker, []byte(v.handoff.overwriteArtifacts.publishedDocument))
			v.write(t, v.postPath, v.post)
		}},
		{name: "acknowledged_intent_only", wantSuccess: true, setup: func(t *testing.T, v *recoveryTargetAdoptionCaseForTest) {
			v.write(t, v.marker, []byte(v.handoff.overwriteArtifacts.publishedDocument))
			if err := os.Remove(v.captured); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "acknowledged_cleanup_complete", wantSuccess: true, setup: func(t *testing.T, v *recoveryTargetAdoptionCaseForTest) {
			v.write(t, v.marker, []byte(v.handoff.overwriteArtifacts.publishedDocument))
			if err := os.Remove(v.captured); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(v.intent); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "matching_final_with_unpublished_post", setup: func(t *testing.T, v *recoveryTargetAdoptionCaseForTest) {
			v.write(t, v.postPath, v.post)
		}},
		{name: "acknowledged_missing_intent_with_captured_prior", setup: func(t *testing.T, v *recoveryTargetAdoptionCaseForTest) {
			v.write(t, v.marker, []byte(v.handoff.overwriteArtifacts.publishedDocument))
			if err := os.Remove(v.intent); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "malformed_published_marker", setup: func(t *testing.T, v *recoveryTargetAdoptionCaseForTest) {
			v.write(t, v.marker, []byte("invalid marker"))
		}},
		{name: "substituted_captured_prior", setup: func(t *testing.T, v *recoveryTargetAdoptionCaseForTest) {
			v.write(t, v.captured, []byte("substituted prior"))
		}},
		{name: "capture_not_published", setup: func(t *testing.T, v *recoveryTargetAdoptionCaseForTest) {
			if err := os.Remove(v.final); err != nil {
				t.Fatal(err)
			}
			v.write(t, v.postPath, v.post)
		}},
		{name: "final_content_mismatch", setup: func(t *testing.T, v *recoveryTargetAdoptionCaseForTest) {
			v.write(t, v.final, []byte(strings.Repeat("x", len(v.post))))
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			value := newRecoveryTargetAdoptionCaseForTest(t, RecoveryOperationOverwrite)
			value.publishOverwrite(t)
			if test.setup != nil {
				test.setup(t, value)
			}
			value.verify(t, test.wantSuccess)
		})
	}
}

func TestRecoverySFTPTargetAdoptionDeleteTuples(t *testing.T) {
	cases := []struct {
		name        string
		setup       func(*testing.T, *recoveryTargetAdoptionCaseForTest)
		keepFinal   bool
		wantState   recoveryDeleteTupleState
		wantSuccess bool
	}{
		{name: "clean", wantSuccess: true},
		{name: "deleted_with_both_markers", wantSuccess: true, setup: func(t *testing.T, v *recoveryTargetAdoptionCaseForTest) {
			v.write(t, v.intent, []byte(v.handoff.consumedDeleteArtifacts.artifacts.intentDocument))
			v.write(t, v.marker, []byte(v.handoff.consumedDeleteArtifacts.artifacts.verifiedDocument))
		}},
		{name: "deleted_with_verified_marker", wantSuccess: true, setup: func(t *testing.T, v *recoveryTargetAdoptionCaseForTest) {
			v.write(t, v.marker, []byte(v.handoff.consumedDeleteArtifacts.artifacts.verifiedDocument))
		}},
		{name: "absent_with_captured_prior", keepFinal: true, wantState: recoveryDeleteTupleCaptured, setup: func(t *testing.T, v *recoveryTargetAdoptionCaseForTest) {
			if err := os.Rename(v.final, v.captured); err != nil {
				t.Fatal(err)
			}
			v.write(t, v.intent, []byte(v.handoff.consumedDeleteArtifacts.artifacts.intentDocument))
		}},
		{name: "absent_with_captured_and_verified", keepFinal: true, wantState: recoveryDeleteTupleVerified, setup: func(t *testing.T, v *recoveryTargetAdoptionCaseForTest) {
			if err := os.Rename(v.final, v.captured); err != nil {
				t.Fatal(err)
			}
			v.write(t, v.intent, []byte(v.handoff.consumedDeleteArtifacts.artifacts.intentDocument))
			v.write(t, v.marker, []byte(v.handoff.consumedDeleteArtifacts.artifacts.verifiedDocument))
		}},
		{name: "malformed_consumed_marker", setup: func(t *testing.T, v *recoveryTargetAdoptionCaseForTest) {
			v.write(t, v.marker, []byte("invalid marker"))
		}},
		{name: "contradictory_intent_only", setup: func(t *testing.T, v *recoveryTargetAdoptionCaseForTest) {
			v.write(t, v.intent, []byte(v.handoff.consumedDeleteArtifacts.artifacts.intentDocument))
		}},
		{name: "substituted_consumed_marker", setup: func(t *testing.T, v *recoveryTargetAdoptionCaseForTest) {
			other := model.BackupAssetRecoveryCheckpoint{
				ID: strings.Repeat("a", 32), DeleteGrantID: strings.Repeat("b", 32),
				DeleteGrantBindingDigest: strings.Repeat("c", sha256DigestLength),
			}
			binding, err := newRecoveryConsumedDeleteArtifactBinding(v.fixture.material, v.fixture.binding,
				v.handoff.job.ID, v.handoff.item, v.handoff.operationDigest, v.handoff.object, other)
			if err != nil {
				t.Fatal(err)
			}
			v.write(t, v.marker, []byte(binding.artifacts.verifiedDocument))
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			value := newRecoveryTargetAdoptionCaseForTest(t, RecoveryOperationDelete)
			if !test.keepFinal {
				if err := os.Remove(value.final); err != nil {
					t.Fatal(err)
				}
			}
			if test.setup != nil {
				test.setup(t, value)
			}
			if test.wantState != 0 {
				parent := value.permit.permit.proof
				authority := parent.adoption.deleteAuthority(parent)
				paths, err := recoveryDeleteArtifactPathsFor(value.final, authority.artifacts)
				if err != nil {
					t.Fatal(err)
				}
				tuple, err := observeRecoveryDeleteTuple(value.client, authority, paths)
				if err != nil {
					t.Fatal(err)
				}
				if got := classifyRecoveryDeleteTuple(value.fixture.material, authority, tuple).state; got != test.wantState {
					t.Fatalf("delete tuple state=%v, want %v", got, test.wantState)
				}
			}
			value.verify(t, test.wantSuccess)
		})
	}
}

func TestRecoverySFTPTargetAdoptionObservationBoundaries(t *testing.T) {
	t.Run("same_state_tuple_drift", func(t *testing.T) {
		value := newRecoveryTargetAdoptionCaseForTest(t, RecoveryOperationOverwrite)
		value.publishOverwrite(t)
		value.write(t, value.marker, []byte(value.handoff.overwriteArtifacts.publishedDocument))
		finalOpens := 0
		value.client.open = func(name string) (recoveryTargetSFTPFile, error) {
			file, err := value.base.Open(name)
			if err != nil || name != value.final {
				return file, err
			}
			finalOpens++
			if finalOpens != 2 {
				return file, nil
			}
			return &recoveryScriptedSFTPFile{base: file, close: func() error {
				if err := file.Close(); err != nil {
					return err
				}
				return os.Remove(value.captured)
			}}, nil
		}
		value.verify(t, false)
		if finalOpens < 3 {
			t.Fatalf("expected both tuple observations and final verification, opened final %d times", finalOpens)
		}
	})
	t.Run("independent_final_verification", func(t *testing.T) {
		value := newRecoveryTargetAdoptionCaseForTest(t, RecoveryOperationOverwrite)
		value.publishOverwrite(t)
		finalOpens := 0
		value.client.open = func(name string) (recoveryTargetSFTPFile, error) {
			if name == value.final {
				finalOpens++
				if finalOpens == 2 {
					value.write(t, value.final, []byte(strings.Repeat("x", len(value.post))))
				}
			}
			return value.base.Open(name)
		}
		value.verify(t, false)
		if finalOpens != 2 {
			t.Fatalf("final verification did not read independently: opens=%d", finalOpens)
		}
	})
	t.Run("file_close_failure", func(t *testing.T) {
		value := newRecoveryTargetAdoptionCaseForTest(t, RecoveryOperationOverwrite)
		value.publishOverwrite(t)
		value.client.open = func(name string) (recoveryTargetSFTPFile, error) {
			file, err := value.base.Open(name)
			if err != nil {
				return file, err
			}
			return &recoveryScriptedSFTPFile{base: file, close: func() error {
				_ = file.Close()
				return errors.New("FAKE_ADOPTION_CLOSE_FAILURE_FOR_TEST_ONLY")
			}}, nil
		}
		value.verify(t, false)
	})
	t.Run("session_close_failure", func(t *testing.T) {
		value := newRecoveryTargetAdoptionCaseForTest(t, RecoveryOperationDelete)
		if err := os.Remove(value.final); err != nil {
			t.Fatal(err)
		}
		value.client.close = func() error {
			_ = value.base.Close()
			return errors.New("FAKE_ADOPTION_CLOSE_FAILURE_FOR_TEST_ONLY")
		}
		value.verify(t, false)
		if value.base.closeCalls != 1 {
			t.Fatalf("SFTP close calls=%d", value.base.closeCalls)
		}
	})
	t.Run("permit_expires_during_observation", func(t *testing.T) {
		value := newRecoveryTargetAdoptionCaseForTest(t, RecoveryOperationDelete)
		if err := os.Remove(value.final); err != nil {
			t.Fatal(err)
		}
		calls := 0
		value.target.now = func() time.Time {
			calls++
			if calls > 1 {
				return value.fixture.now.Add(time.Minute)
			}
			return value.fixture.now
		}
		value.verify(t, false)
	})
}

func TestRecoverySFTPTargetAdoptionDeleteObservationBoundaries(t *testing.T) {
	t.Run("same_deleted_state_tuple_drift", func(t *testing.T) {
		value := newRecoveryTargetAdoptionCaseForTest(t, RecoveryOperationDelete)
		if err := os.Remove(value.final); err != nil {
			t.Fatal(err)
		}
		value.write(t, value.intent, []byte(value.handoff.consumedDeleteArtifacts.artifacts.intentDocument))
		value.write(t, value.marker, []byte(value.handoff.consumedDeleteArtifacts.artifacts.verifiedDocument))
		originalLstat := value.client.lstat
		finalStats := 0
		value.client.lstat = func(name string, call int) (os.FileInfo, error) {
			if name == value.final {
				finalStats++
				if finalStats == 3 {
					if err := os.Remove(value.intent); err != nil {
						return nil, err
					}
				}
			}
			return originalLstat(name, call)
		}
		value.verify(t, false)
		if finalStats < 6 {
			t.Fatalf("expected both tuple observations and final verification, final stats=%d", finalStats)
		}
	})
	t.Run("ordinary_verify_retains_absence_contract", func(t *testing.T) {
		value := newRecoveryTargetAdoptionCaseForTest(t, RecoveryOperationDelete)
		if err := os.Rename(value.final, value.captured); err != nil {
			t.Fatal(err)
		}
		value.write(t, value.intent, []byte(value.handoff.consumedDeleteArtifacts.artifacts.intentDocument))
		permit, err := newRecoveryTargetVerifyPermit(value.handoff, value.fixture.now.Add(time.Minute), value.fixture.now)
		if err != nil {
			t.Fatal(err)
		}
		value.permit = permit
		value.verify(t, true)
	})
}

func cloneRecoveryAdoptionPermitForTest(permit TargetVerifyPermit) TargetVerifyPermit {
	cloned := permit
	parent := *permit.permit.proof
	adoption := *parent.adoption
	parent.adoption = &adoption
	cloned.permit.proof = &parent
	return cloned
}

func TestRecoveryTargetAdoptionPermitBinding(t *testing.T) {
	value := newRecoveryTargetAdoptionCaseForTest(t, RecoveryOperationDelete)
	cases := []struct {
		name   string
		mutate func(*TargetVerifyPermit)
	}{
		{name: "missing_adoption_proof", mutate: func(p *TargetVerifyPermit) { p.permit.proof.adoption = nil }},
		{name: "durable_digest", mutate: func(p *TargetVerifyPermit) { p.permit.proof.adoption.durableDigest = strings.Repeat("a", 64) }},
		{name: "item", mutate: func(p *TargetVerifyPermit) { p.permit.proof.adoption.jobItemID = strings.Repeat("a", 32) }},
		{name: "operation", mutate: func(p *TargetVerifyPermit) { p.permit.proof.adoption.operationDigest = strings.Repeat("a", 64) }},
		{name: "object", mutate: func(p *TargetVerifyPermit) { p.permit.proof.adoption.object.PrivateRelativeLocator += ".other" }},
		{name: "expectation", mutate: func(p *TargetVerifyPermit) { p.permit.proof.adoption.expectationKind = TargetPresencePresent }},
		{name: "prior_size", mutate: func(p *TargetVerifyPermit) { p.permit.proof.adoption.expectedPriorBytes++ }},
		{name: "consumed_checkpoint", mutate: func(p *TargetVerifyPermit) { p.permit.proof.adoption.consumedCheckpointID = strings.Repeat("a", 32) }},
		{name: "consumed_grant", mutate: func(p *TargetVerifyPermit) { p.permit.proof.adoption.consumedGrantID = strings.Repeat("a", 32) }},
		{name: "consumed_grant_digest", mutate: func(p *TargetVerifyPermit) { p.permit.proof.adoption.consumedGrantDigest = strings.Repeat("a", 64) }},
		{name: "artifact", mutate: func(p *TargetVerifyPermit) { p.permit.proof.adoption.deleteArtifacts.verifiedDocument += " " }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			permit := cloneRecoveryAdoptionPermitForTest(value.permit)
			test.mutate(&permit)
			if err := permit.ValidateAt(value.fixture.now); !errors.Is(err, ErrInvalidTargetPermit) {
				t.Fatalf("substituted permit error=%v", err)
			}
		})
	}
	t.Run("overwrite_fields", func(t *testing.T) {
		value := newRecoveryTargetAdoptionCaseForTest(t, RecoveryOperationOverwrite)
		for _, test := range []struct {
			name   string
			mutate func(*targetAdoptionVerifyProof)
		}{
			{name: "post_digest", mutate: func(p *targetAdoptionVerifyProof) { p.expectedDigest = strings.Repeat("a", 64) }},
			{name: "post_bytes", mutate: func(p *targetAdoptionVerifyProof) { p.expectedBytes++ }},
			{name: "artifact_document", mutate: func(p *targetAdoptionVerifyProof) { p.overwriteArtifacts.intentDocument += " " }},
			{name: "artifact_component", mutate: func(p *targetAdoptionVerifyProof) { p.overwriteArtifacts.priorComponent += "x" }},
		} {
			t.Run(test.name, func(t *testing.T) {
				permit := cloneRecoveryAdoptionPermitForTest(value.permit)
				test.mutate(permit.permit.proof.adoption)
				if err := permit.ValidateAt(value.fixture.now); !errors.Is(err, ErrInvalidTargetPermit) {
					t.Fatalf("substituted overwrite proof error=%v", err)
				}
			})
		}
	})
	t.Run("issuer_requires_overwrite_artifacts", func(t *testing.T) {
		value := newRecoveryTargetAdoptionCaseForTest(t, RecoveryOperationOverwrite)
		handoff := value.handoff
		handoff.overwriteArtifacts = recoveryOverwriteArtifactBinding{}
		if _, err := newRecoveryTargetAdoptionVerifyPermit(handoff, value.fixture.now.Add(time.Minute), value.fixture.now); !errors.Is(err, ErrRecoveryWorkerFenceLost) {
			t.Fatalf("missing overwrite proof error=%v", err)
		}
	})
	t.Run("issuer_rejects_isolated_overwrite", func(t *testing.T) {
		value := newRecoveryTargetAdoptionCaseForTest(t, RecoveryOperationOverwrite)
		handoff := value.handoff
		handoff.plan.TargetMode, handoff.job.TargetMode = string(TargetModeIsolated), string(TargetModeIsolated)
		handoff.job.EncryptedWorkspaceRelativeLocator = recoveryWorkspaceLocatorDirectory + "/" + handoff.job.ID
		handoff.object.PrivateRelativeLocator = handoff.job.EncryptedWorkspaceRelativeLocator + "/" + handoff.operation.TargetRelativeLocator
		handoff.object.TargetPathDigest = mustTargetPathDigest(t, handoff.object.RootID,
			handoff.object.RootLocatorDigest, handoff.object.PrivateRelativeLocator)
		handoff.item.TargetObjectDigest = handoff.object.TargetPathDigest
		handoff.operationDigest = recoveryJobItemOperationDigest(handoff.item)
		handoff.overwriteArtifacts = recoveryOverwriteArtifactBinding{}
		if _, err := newRecoveryTargetAdoptionVerifyPermit(handoff, value.fixture.now.Add(time.Minute), value.fixture.now); !errors.Is(err, ErrRecoveryWorkerFenceLost) {
			t.Fatalf("isolated overwrite error=%v", err)
		}
	})
	t.Run("issuer_requires_consumed_artifacts", func(t *testing.T) {
		handoff := value.handoff
		handoff.consumedDeleteArtifacts = recoveryConsumedDeleteArtifactBinding{}
		if _, err := newRecoveryTargetAdoptionVerifyPermit(handoff, value.fixture.now.Add(time.Minute), value.fixture.now); !errors.Is(err, ErrRecoveryWorkerFenceLost) {
			t.Fatalf("missing consumed proof error=%v", err)
		}
	})
	t.Run("issuer_rejects_operation_substitution", func(t *testing.T) {
		handoff := value.handoff
		handoff.operation.ExpectedPriorBytes++
		if _, err := newRecoveryTargetAdoptionVerifyPermit(handoff, value.fixture.now.Add(time.Minute), value.fixture.now); !errors.Is(err, ErrRecoveryWorkerFenceLost) {
			t.Fatalf("substituted operation error=%v", err)
		}
	})
	t.Run("request_substitution", func(t *testing.T) {
		value := newRecoveryTargetAdoptionCaseForTest(t, RecoveryOperationOverwrite)
		value.publishOverwrite(t)
		expectation := TargetVerifyExpectation{Kind: TargetPresencePresent, Present: &PresentExpectation{
			IdentityDigest: strings.Repeat("a", sha256DigestLength), Bytes: int64(len(value.post)),
		}}
		if _, err := value.target.Verify(context.Background(), value.permit, value.handoff.object, expectation); !errors.Is(err, ErrInvalidTargetPermit) {
			t.Fatalf("substituted expectation error=%v", err)
		}
		if value.base.openCalls != 0 {
			t.Fatal("substituted expectation reached target")
		}
	})
	t.Run("resealed_other_consumed_artifact", func(t *testing.T) {
		value := newRecoveryTargetAdoptionCaseForTest(t, RecoveryOperationDelete)
		if err := os.Remove(value.final); err != nil {
			t.Fatal(err)
		}
		other := model.BackupAssetRecoveryCheckpoint{
			ID: strings.Repeat("a", 32), DeleteGrantID: strings.Repeat("b", 32),
			DeleteGrantBindingDigest: strings.Repeat("c", sha256DigestLength),
		}
		binding, err := newRecoveryConsumedDeleteArtifactBinding(value.fixture.material, value.fixture.binding,
			value.handoff.job.ID, value.handoff.item, value.handoff.operationDigest, value.handoff.object, other)
		if err != nil {
			t.Fatal(err)
		}
		value.permit = cloneRecoveryAdoptionPermitForTest(value.permit)
		parent := value.permit.permit.proof
		parent.adoption.deleteArtifacts = binding.artifacts
		parent.bindingDigest = targetVerifyPermitProofDigest(value.permit.permit, parent)
		value.verify(t, false)
	})
	t.Run("resealed_other_overwrite_artifact", func(t *testing.T) {
		value := newRecoveryTargetAdoptionCaseForTest(t, RecoveryOperationOverwrite)
		value.publishOverwrite(t)
		binding := value.fixture.binding
		item := value.handoff.item
		other, err := newRecoveryOverwriteArtifactBinding(value.fixture.material, recoveryOverwriteArtifactBindingInput{
			keyVersion: item.TargetLocatorKeyVersion, planID: binding.PlanID, planBindingDigest: binding.PlanBindingDigest,
			jobID: value.handoff.job.ID, jobItemID: strings.Repeat("a", 32), operationDigest: value.handoff.operationDigest,
			targetMode: TargetModeInPlace, nodeID: binding.NodeID, rootID: binding.RootID,
			rootLocatorDigest: binding.RootLocatorDigest, rootRevision: binding.RootRevision,
			object: value.handoff.object, expectedPrior: value.handoff.operation.ExpectedPrior,
			expectedPriorBytes: item.ExpectedPriorBytes, expectedPostDigest: item.ExpectedPostIdentityDigest,
			expectedPostBytes: item.ExpectedPostBytes,
		})
		if err != nil {
			t.Fatal(err)
		}
		value.permit = cloneRecoveryAdoptionPermitForTest(value.permit)
		parent := value.permit.permit.proof
		parent.adoption.overwriteArtifacts = other
		parent.bindingDigest = targetVerifyPermitProofDigest(value.permit.permit, parent)
		value.verify(t, false)
	})
}
