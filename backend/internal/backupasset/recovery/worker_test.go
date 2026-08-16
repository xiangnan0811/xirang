package recovery

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"
	"xirang/backend/internal/settings"

	"gorm.io/gorm"
)

type recoveryFirstWritePreparer interface {
	PrepareFirstWrite(context.Context, RecoveryWorkerClaim) (TargetWritePermit, error)
}

type recoveryOverwriteHistoricalKeySourceForTest struct {
	material       backupasset.DomainKeyMaterial
	activeCalls    int
	byVersionCalls []int
	lastVersionKey []byte
}

func (source *recoveryOverwriteHistoricalKeySourceForTest) Active(
	ctx context.Context,
	domain backupasset.KeyDomain,
) (backupasset.DomainKeyMaterial, error) {
	source.activeCalls++
	if err := ctx.Err(); err != nil {
		return backupasset.DomainKeyMaterial{}, err
	}
	if source == nil || domain != backupasset.KeyDomainRecoveryCleanupOwnership {
		return backupasset.DomainKeyMaterial{}, backupasset.ErrKeyUnavailable
	}
	return cloneDomainKeyMaterial(source.material), nil
}

func (source *recoveryOverwriteHistoricalKeySourceForTest) ByVersion(
	ctx context.Context,
	domain backupasset.KeyDomain,
	version int,
) (backupasset.DomainKeyMaterial, error) {
	source.byVersionCalls = append(source.byVersionCalls, version)
	if err := ctx.Err(); err != nil {
		return backupasset.DomainKeyMaterial{}, err
	}
	if source == nil || domain != backupasset.KeyDomainRecoveryCleanupOwnership || version != source.material.Version {
		return backupasset.DomainKeyMaterial{}, backupasset.ErrKeyUnavailable
	}
	material := cloneDomainKeyMaterial(source.material)
	source.lastVersionKey = material.Key
	return material, nil
}

func TestRecoveryOverwriteArtifactBindingUsesHistoricalCleanupKey(t *testing.T) {
	binding := recoveryTargetSessionBindingForTest(t)
	material, input := recoveryOverwriteArtifactBindingInputForTest(
		t, binding, strings.Repeat("1", 32), "items/private-overwrite-target", []byte("post-payload"),
	)

	first, err := newRecoveryOverwriteArtifactBinding(material, input)
	if err != nil {
		t.Fatalf("derive overwrite artifact binding: %v", err)
	}
	replayed, err := newRecoveryOverwriteArtifactBinding(cloneDomainKeyMaterial(material), input)
	if err != nil || replayed != first {
		t.Fatalf("same historical key/item did not replay exactly: first=%+v replay=%+v err=%v", first, replayed, err)
	}

	wantToken := recoveryOverwriteArtifactTokenForTest(material.Key, input)
	if first.token != wantToken {
		t.Fatalf("overwrite artifact token=%q, want exact framed HMAC token %q", first.token, wantToken)
	}
	rawToken, err := base64.RawURLEncoding.DecodeString(first.token)
	if err != nil || len(rawToken) != sha256.Size || base64.RawURLEncoding.EncodeToString(rawToken) != first.token {
		t.Fatalf("overwrite artifact token is not canonical 32-byte base64url: token=%q err=%v bytes=%d", first.token, err, len(rawToken))
	}
	if first.bindingDigest != hex.EncodeToString(rawToken) || first.keyVersion != material.Version {
		t.Fatalf("overwrite artifact private binding/key=%q/%d, want token digest/version %q/%d",
			first.bindingDigest, first.keyVersion, hex.EncodeToString(rawToken), material.Version)
	}

	wantComponents := map[string]string{
		"intent":    recoveryOverwriteArtifactPrefix + first.token + ".intent",
		"prior":     recoveryOverwriteArtifactPrefix + first.token + ".prior",
		"post":      recoveryOverwriteArtifactPrefix + first.token + ".post",
		"published": recoveryOverwriteArtifactPrefix + first.token + ".published",
	}
	gotComponents := map[string]string{
		"intent": first.intentComponent, "prior": first.priorComponent,
		"post": first.postComponent, "published": first.publishedComponent,
	}
	for phase, component := range gotComponents {
		if component != wantComponents[phase] || len(component) > recoveryOverwriteArtifactComponentMaxBytes {
			t.Fatalf("overwrite %s component=%q bytes=%d, want fixed same-parent component %q within %d bytes",
				phase, component, len(component), wantComponents[phase], recoveryOverwriteArtifactComponentMaxBytes)
		}
	}

	for phase, encoded := range map[string]string{
		"intent": first.intentDocument, "published": first.publishedDocument,
	} {
		var document map[string]json.RawMessage
		if err := json.Unmarshal([]byte(encoded), &document); err != nil {
			t.Fatalf("decode overwrite %s document: %v", phase, err)
		}
		if len(document) != 5 {
			t.Fatalf("overwrite %s document fields=%v, want exact five-field closed document", phase, document)
		}
		var schemaVersion, keyVersion int
		var gotPhase, bindingDigest, authenticationTag string
		if err := json.Unmarshal(document["schema_version"], &schemaVersion); err != nil ||
			json.Unmarshal(document["key_version"], &keyVersion) != nil ||
			json.Unmarshal(document["phase"], &gotPhase) != nil ||
			json.Unmarshal(document["binding_digest"], &bindingDigest) != nil ||
			json.Unmarshal(document["authentication_tag"], &authenticationTag) != nil {
			t.Fatalf("overwrite %s document has invalid field types: %s", phase, encoded)
		}
		tag, tagErr := base64.RawURLEncoding.DecodeString(authenticationTag)
		body, bodyErr := json.Marshal(recoveryOverwriteMarkerDocumentBody{
			SchemaVersion: schemaVersion, KeyVersion: keyVersion,
			Phase: gotPhase, BindingDigest: bindingDigest,
		})
		wantTag := recoveryOverwriteFramedHMACForTest(
			material.Key, "xirang/recovery/overwrite-artifact-marker/v1", string(body),
		)
		if schemaVersion != recoveryOverwriteMarkerSchemaVersion || keyVersion != material.Version ||
			gotPhase != phase || bindingDigest != first.bindingDigest || tagErr != nil || len(tag) != sha256.Size ||
			bodyErr != nil || !hmac.Equal(tag, wantTag) ||
			base64.RawURLEncoding.EncodeToString(tag) != authenticationTag {
			t.Fatalf("overwrite %s document=%s, want exact closed authenticated binding", phase, encoded)
		}
	}

	privateInputs := []string{
		input.planID, input.planBindingDigest, input.jobID, input.jobItemID, input.operationDigest,
		input.rootID, input.rootLocatorDigest, input.rootRevision,
		input.object.TargetPathDigest, input.object.PrivateRelativeLocator,
		input.expectedPrior.Digest, input.expectedPostDigest,
	}
	for _, product := range []string{
		first.intentComponent, first.priorComponent, first.postComponent, first.publishedComponent,
		first.intentDocument, first.publishedDocument,
	} {
		for _, forbidden := range privateInputs {
			if strings.Contains(product, forbidden) {
				t.Fatalf("overwrite artifact product leaked raw private input %q: %s", forbidden, product)
			}
		}
	}

	mutations := []struct {
		name   string
		mutate func(*backupasset.DomainKeyMaterial, *recoveryOverwriteArtifactBindingInput)
	}{
		{name: "historical key", mutate: func(material *backupasset.DomainKeyMaterial, _ *recoveryOverwriteArtifactBindingInput) {
			material.Key[0] ^= 0xff
		}},
		{name: "key version", mutate: func(material *backupasset.DomainKeyMaterial, input *recoveryOverwriteArtifactBindingInput) {
			material.Version++
			input.keyVersion++
		}},
		{name: "plan", mutate: func(_ *backupasset.DomainKeyMaterial, input *recoveryOverwriteArtifactBindingInput) {
			input.planID = strings.Repeat("2", 32)
		}},
		{name: "plan binding", mutate: func(_ *backupasset.DomainKeyMaterial, input *recoveryOverwriteArtifactBindingInput) {
			input.planBindingDigest = strings.Repeat("2", sha256DigestLength)
		}},
		{name: "job", mutate: func(_ *backupasset.DomainKeyMaterial, input *recoveryOverwriteArtifactBindingInput) {
			input.jobID = strings.Repeat("3", 32)
		}},
		{name: "item", mutate: func(_ *backupasset.DomainKeyMaterial, input *recoveryOverwriteArtifactBindingInput) {
			input.jobItemID = strings.Repeat("4", 32)
		}},
		{name: "operation", mutate: func(_ *backupasset.DomainKeyMaterial, input *recoveryOverwriteArtifactBindingInput) {
			input.operationDigest = strings.Repeat("5", sha256DigestLength)
		}},
		{name: "node", mutate: func(_ *backupasset.DomainKeyMaterial, input *recoveryOverwriteArtifactBindingInput) { input.nodeID++ }},
		{name: "root", mutate: func(_ *backupasset.DomainKeyMaterial, input *recoveryOverwriteArtifactBindingInput) {
			input.rootID += "-changed"
			input.object.RootID = input.rootID
			input.object.TargetPathDigest = mustTargetPathDigest(t, input.object.RootID, input.object.RootLocatorDigest, input.object.PrivateRelativeLocator)
		}},
		{name: "root locator digest", mutate: func(_ *backupasset.DomainKeyMaterial, input *recoveryOverwriteArtifactBindingInput) {
			input.rootLocatorDigest = strings.Repeat("9", sha256DigestLength)
			input.object.RootLocatorDigest = input.rootLocatorDigest
			input.object.TargetPathDigest = mustTargetPathDigest(t, input.object.RootID, input.object.RootLocatorDigest, input.object.PrivateRelativeLocator)
		}},
		{name: "root revision", mutate: func(_ *backupasset.DomainKeyMaterial, input *recoveryOverwriteArtifactBindingInput) {
			input.rootRevision += "-changed"
		}},
		{name: "private locator", mutate: func(_ *backupasset.DomainKeyMaterial, input *recoveryOverwriteArtifactBindingInput) {
			input.object.PrivateRelativeLocator += "-changed"
			input.object.TargetPathDigest = mustTargetPathDigest(t, input.object.RootID, input.object.RootLocatorDigest, input.object.PrivateRelativeLocator)
		}},
		{name: "prior digest", mutate: func(_ *backupasset.DomainKeyMaterial, input *recoveryOverwriteArtifactBindingInput) {
			input.expectedPrior.Digest = strings.Repeat("6", sha256DigestLength)
		}},
		{name: "prior bytes", mutate: func(_ *backupasset.DomainKeyMaterial, input *recoveryOverwriteArtifactBindingInput) {
			input.expectedPriorBytes++
		}},
		{name: "post digest", mutate: func(_ *backupasset.DomainKeyMaterial, input *recoveryOverwriteArtifactBindingInput) {
			input.expectedPostDigest = strings.Repeat("7", sha256DigestLength)
		}},
		{name: "post bytes", mutate: func(_ *backupasset.DomainKeyMaterial, input *recoveryOverwriteArtifactBindingInput) {
			input.expectedPostBytes++
		}},
	}
	for _, testCase := range mutations {
		t.Run(testCase.name, func(t *testing.T) {
			changedMaterial := cloneDomainKeyMaterial(material)
			changedInput := input
			testCase.mutate(&changedMaterial, &changedInput)
			changed, err := newRecoveryOverwriteArtifactBinding(changedMaterial, changedInput)
			if err != nil {
				t.Fatalf("derive field-sensitive overwrite binding: %v", err)
			}
			if changed == first || changed.token == first.token || changed.bindingDigest == first.bindingDigest {
				t.Fatalf("overwrite artifact binding did not change for %s: before=%+v after=%+v", testCase.name, first, changed)
			}
		})
	}

	for _, testCase := range []struct {
		name   string
		mutate func(*recoveryOverwriteArtifactBindingInput)
	}{
		{name: "isolated mode", mutate: func(input *recoveryOverwriteArtifactBindingInput) { input.targetMode = TargetModeIsolated }},
		{name: "object root", mutate: func(input *recoveryOverwriteArtifactBindingInput) { input.object.RootID += "-changed" }},
		{name: "object digest", mutate: func(input *recoveryOverwriteArtifactBindingInput) {
			input.object.TargetPathDigest = strings.Repeat("8", sha256DigestLength)
		}},
		{name: "absent prior", mutate: func(input *recoveryOverwriteArtifactBindingInput) {
			input.expectedPrior = ExpectedTargetIdentity{Kind: ExpectedTargetAbsent}
		}},
	} {
		t.Run("reject "+testCase.name, func(t *testing.T) {
			changed := input
			testCase.mutate(&changed)
			if _, err := newRecoveryOverwriteArtifactBinding(material, changed); err == nil {
				t.Fatalf("invalid overwrite artifact input %s was accepted", testCase.name)
			}
		})
	}

	t.Run("locked handoff uses item key version", func(t *testing.T) {
		execution := newExactMirrorOrdinaryExecutionFixture(t)
		material, err := (recoveryWorkerWorkspaceKeySource{}).Active(
			context.Background(), backupasset.KeyDomainRecoveryCleanupOwnership,
		)
		if err != nil {
			t.Fatalf("load fixture cleanup key: %v", err)
		}
		keys := &recoveryOverwriteHistoricalKeySourceForTest{material: material}
		execution.coordinator.workspaceKeys = keys
		base, err := execution.coordinator.PrepareFirstWrite(context.Background(), execution.claim)
		if err != nil {
			t.Fatalf("prepare exact-mirror first write: %v", err)
		}
		var item model.BackupAssetRecoveryJobItem
		if err := execution.serviceFixture.db.Where(
			"job_id = ? AND operation_kind = ?", execution.jobID, RecoveryOperationOverwrite,
		).Take(&item).Error; err != nil {
			t.Fatal(err)
		}
		handoff, err := execution.coordinator.loadOrdinaryOperationHandoff(
			context.Background(), execution.claim, item.ID,
		)
		if err != nil {
			t.Fatalf("load locked overwrite handoff: %v", err)
		}
		permit, err := execution.coordinator.ordinaryItemWritePermit(
			execution.claim, base, handoff, handoff.job.TargetChainRevision,
		)
		if err != nil {
			t.Fatalf("issue locked overwrite permit: %v", err)
		}
		if keys.activeCalls != 1 || !reflect.DeepEqual(keys.byVersionCalls, []int{item.TargetLocatorKeyVersion}) {
			t.Fatalf("overwrite cleanup-key calls active/by-version=%d/%v, want 1/%v",
				keys.activeCalls, keys.byVersionCalls, []int{item.TargetLocatorKeyVersion})
		}
		if len(keys.lastVersionKey) != sha256.Size ||
			!bytes.Equal(keys.lastVersionKey, make([]byte, sha256.Size)) {
			t.Fatalf("overwrite historical cleanup key bytes were not cleared after locked handoff")
		}
		if handoff.overwriteArtifacts.keyVersion != item.TargetLocatorKeyVersion ||
			!handoff.overwriteArtifacts.valid() || permit.itemProof == nil ||
			permit.itemProof.jobItemID != item.ID ||
			permit.itemProof.operationDigest != handoff.operationDigest ||
			permit.itemProof.expectedPriorBytes != item.ExpectedPriorBytes ||
			permit.itemProof.artifacts != handoff.overwriteArtifacts {
			t.Fatalf("locked overwrite handoff/proof artifacts=%+v/%+v, want item-bound historical-key authority",
				handoff.overwriteArtifacts, permit.itemProof)
		}
	})
}

func prepareConsumedDeletePermitHandoffForTest(
	t *testing.T,
	suffix string,
) (pausedAuthorizedExactMirrorDelete, interruptedOperationHandoff, model.BackupAssetRecoveryCheckpoint) {
	t.Helper()
	fixture := newPausedAuthorizedExactMirrorDelete(t, suffix)
	db := fixture.execution.serviceFixture.db
	const callbackPrefix = "test:ordinary-delete-permit-consume-"
	callbackName := callbackPrefix + strings.ReplaceAll(t.Name(), "/", "_")
	projectionErr := errors.New("simulated crash before ordinary delete permit projection")
	if err := db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement != nil &&
			tx.Statement.Table == (model.BackupAssetRecoveryCheckpoint{}).TableName() &&
			len(fixture.execution.target.deletes) == 1 {
			_ = tx.AddError(projectionErr)
		}
	}); err != nil {
		t.Fatalf("register consumed delete permit crash: %v", err)
	}
	crashSource := newRecoveryRepositoryContractSource(t, db, fixture.execution.jobID)
	executeErr := fixture.execution.coordinator.ExecuteClaim(
		context.Background(), fixture.execution.claim, crashSource, fixture.request.GrantSecret,
	)
	if err := db.Callback().Create().Remove(callbackName); err != nil {
		t.Fatalf("remove consumed delete permit crash: %v", err)
	}
	if !errors.Is(executeErr, ErrRecoveryWorkerUnavailable) {
		t.Fatalf("consumed delete permit setup error=%v, want worker unavailable", executeErr)
	}

	var item model.BackupAssetRecoveryJobItem
	if err := db.Where("job_id = ? AND operation_kind = ?", fixture.execution.jobID, RecoveryOperationDelete).
		Take(&item).Error; err != nil {
		t.Fatalf("load pending delete item: %v", err)
	}
	handoff, err := fixture.execution.coordinator.loadOrdinaryOperationHandoff(
		context.Background(), fixture.execution.claim, item.ID,
	)
	if err != nil {
		t.Fatalf("load consumed delete handoff: %v", err)
	}
	var checkpoints []model.BackupAssetRecoveryCheckpoint
	if err := db.Where("job_id = ?", fixture.execution.jobID).
		Order("sequence ASC").Find(&checkpoints).Error; err != nil {
		t.Fatalf("load consumed delete checkpoints: %v", err)
	}
	if len(checkpoints) == 0 ||
		checkpoints[len(checkpoints)-1].Phase != string(CheckpointPhaseDeleteAuthorityConsumed) {
		t.Fatalf("consumed delete checkpoints=%+v", checkpoints)
	}
	return fixture, handoff, checkpoints[len(checkpoints)-1]
}

func TestRecoveryOrdinaryDeletePermitLockedIssuance(t *testing.T) {
	t.Run("current consumed checkpoint grant exact pending item historical key and live fences", func(t *testing.T) {
		fixture, handoff, consumed := prepareConsumedDeletePermitHandoffForTest(t, "ISSUE_CURRENT")
		material, err := (recoveryWorkerWorkspaceKeySource{}).Active(
			context.Background(), backupasset.KeyDomainRecoveryCleanupOwnership,
		)
		if err != nil {
			t.Fatalf("load cleanup key: %v", err)
		}
		keys := &recoveryOverwriteHistoricalKeySourceForTest{material: material}
		fixture.execution.coordinator.workspaceKeys = keys

		permit, err := fixture.execution.coordinator.ordinaryDeletePermit(
			context.Background(), fixture.execution.claim, handoff,
			handoff.job.TargetChainRevision,
		)
		if err != nil {
			t.Fatalf("issue ordinary delete permit: %v", err)
		}
		if permit.proof == nil || permit.proof.consumedCheckpointID != consumed.ID ||
			permit.proof.consumedGrantID != consumed.DeleteGrantID ||
			permit.proof.consumedGrantDigest != consumed.DeleteGrantBindingDigest ||
			permit.proof.jobItemID != handoff.item.ID ||
			permit.proof.operationDigest != handoff.operationDigest ||
			permit.proof.expectedPriorBytes != -1 ||
			permit.proof.currentAttemptID != fixture.execution.claim.AttemptID ||
			permit.proof.currentAttemptFence != fixture.execution.claim.AttemptFence ||
			permit.proof.currentNodeLeaseID != fixture.execution.claim.NodeLeaseID ||
			permit.proof.currentNodeFence != fixture.execution.claim.NodeFence ||
			permit.proof.currentSourceFence != fixture.execution.claim.SourceFence ||
			permit.permit.ExpectedTargetRevision != handoff.job.TargetChainRevision ||
			permit.proof.artifacts.keyVersion != handoff.item.TargetLocatorKeyVersion ||
			!permit.proof.artifacts.valid() ||
			len(keys.byVersionCalls) != 1 || keys.byVersionCalls[0] != handoff.item.TargetLocatorKeyVersion {
			t.Fatalf("ordinary delete permit proof=%+v permit=%+v key versions=%v", permit.proof, permit.permit, keys.byVersionCalls)
		}
		if _, err := permit.authorityAt(fixture.execution.serviceFixture.now, TargetDeleteRequest{Object: handoff.object}); err != nil {
			t.Fatalf("issued ordinary delete permit authority=%v", err)
		}
	})

	t.Run("substituted item or consumed grant does not issue", func(t *testing.T) {
		fixture, handoff, consumed := prepareConsumedDeletePermitHandoffForTest(t, "ISSUE_SUBSTITUTION")
		mutatedHandoff := handoff
		mutatedHandoff.item.ID = strings.Repeat("a", 32)
		if _, err := fixture.execution.coordinator.ordinaryDeletePermit(
			context.Background(), fixture.execution.claim, mutatedHandoff,
			handoff.job.TargetChainRevision,
		); !errors.Is(err, ErrRecoveryWorkerFenceLost) {
			t.Fatalf("substituted pending item issuance error=%v, want fence loss", err)
		}
		if updated := fixture.execution.serviceFixture.db.Model(&model.BackupAssetRecoveryCheckpoint{}).
			Where("id = ?", consumed.ID).Update("delete_grant_id", strings.Repeat("b", 32)); updated.Error != nil || updated.RowsAffected != 1 {
			t.Fatalf("substitute consumed grant checkpoint: %v rows=%d", updated.Error, updated.RowsAffected)
		}
		if _, err := fixture.execution.coordinator.ordinaryDeletePermit(
			context.Background(), fixture.execution.claim, handoff,
			handoff.job.TargetChainRevision,
		); !errors.Is(err, ErrRecoveryWorkerFenceLost) {
			t.Fatalf("substituted consumed grant issuance error=%v, want fence loss", err)
		}
	})

	t.Run("expired owner and required-but-unconsumed checkpoint do not issue", func(t *testing.T) {
		fixture, handoff, _ := prepareConsumedDeletePermitHandoffForTest(t, "ISSUE_EXPIRED")
		fixture.execution.serviceFixture.now = fixture.execution.claim.LeaseExpiresAt.Add(time.Second)
		if _, err := fixture.execution.coordinator.ordinaryDeletePermit(
			context.Background(), fixture.execution.claim, handoff,
			handoff.job.TargetChainRevision,
		); !errors.Is(err, ErrRecoveryWorkerFenceLost) {
			t.Fatalf("expired owner issuance error=%v, want fence loss", err)
		}

		paused := newPausedAuthorizedExactMirrorDelete(t, "ISSUE_REQUIRED_ONLY")
		db := paused.execution.serviceFixture.db
		var item model.BackupAssetRecoveryJobItem
		if err := db.Where("job_id = ? AND operation_kind = ?", paused.execution.jobID, RecoveryOperationDelete).
			Take(&item).Error; err != nil {
			t.Fatalf("load required-only delete item: %v", err)
		}
		requiredHandoff, err := paused.execution.coordinator.loadOrdinaryOperationHandoff(
			context.Background(), paused.execution.claim, item.ID,
		)
		if err != nil {
			t.Fatalf("load required-only delete handoff: %v", err)
		}
		if _, err := paused.execution.coordinator.ordinaryDeletePermit(
			context.Background(), paused.execution.claim, requiredHandoff,
			requiredHandoff.job.TargetChainRevision,
		); !errors.Is(err, ErrRecoveryWorkerFenceLost) {
			t.Fatalf("required-but-unconsumed issuance error=%v, want fence loss", err)
		}
	})
}

func recoveryOverwriteArtifactTokenForTest(key []byte, input recoveryOverwriteArtifactBindingInput) string {
	fields := []string{
		fmt.Sprintf("%d", input.keyVersion), input.planID, input.planBindingDigest,
		input.jobID, input.jobItemID, input.operationDigest, string(input.targetMode),
		fmt.Sprintf("%d", input.nodeID), input.rootID, input.rootLocatorDigest, input.rootRevision,
		input.object.RootID, input.object.RootLocatorDigest, input.object.TargetPathDigest,
		input.object.PrivateRelativeLocator, string(input.expectedPrior.Kind), input.expectedPrior.Digest,
		fmt.Sprintf("%d", input.expectedPriorBytes), input.expectedPostDigest,
		fmt.Sprintf("%d", input.expectedPostBytes),
	}
	return base64.RawURLEncoding.EncodeToString(
		recoveryOverwriteFramedHMACForTest(key, "xirang/recovery/overwrite-artifact-binding/v1", fields...),
	)
}

func recoveryOverwriteFramedHMACForTest(key []byte, domain string, fields ...string) []byte {
	buffer := bytes.NewBuffer(nil)
	write := func(value string) {
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(value)))
		buffer.Write(size[:])
		buffer.WriteString(value)
	}
	write(domain)
	var count [8]byte
	binary.BigEndian.PutUint64(count[:], uint64(len(fields)))
	buffer.Write(count[:])
	for _, field := range fields {
		write(field)
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(buffer.Bytes())
	return mac.Sum(nil)
}

type recoveryMarkerValidationProduct struct {
	AttemptID    string `gorm:"column:workspace_marker_validation_attempt_id"`
	AttemptFence uint64 `gorm:"column:workspace_marker_validation_attempt_fence"`
	NodeFence    uint64 `gorm:"column:workspace_marker_validation_node_fence"`
}

func loadRecoveryMarkerValidationProduct(
	t *testing.T,
	db *gorm.DB,
	jobID string,
) recoveryMarkerValidationProduct {
	t.Helper()
	var product recoveryMarkerValidationProduct
	result := db.Table((model.BackupAssetRecoveryJob{}).TableName()).
		Select(`workspace_marker_validation_attempt_id,
			workspace_marker_validation_attempt_fence,
			workspace_marker_validation_node_fence`).
		Where("id = ?", jobID).Limit(1).Scan(&product)
	if result.Error != nil || result.RowsAffected != 1 {
		t.Fatalf("load recovery marker-validation product: rows=%d err=%v", result.RowsAffected, result.Error)
	}
	return product
}

type recoveryOwnedWorkspaceTargetFake struct {
	closedTargetPortFake
	db               *gorm.DB
	now              func() time.Time
	calls            int
	request          CreateOwnedJobDirRequest
	permit           TargetWritePermit
	latchCommitted   bool
	currentAuthority bool
}

type recoveryMutationAdmissionTargetFake struct {
	closedTargetPortFake
	now   time.Time
	calls []string
}

func (fake *recoveryMutationAdmissionTargetFake) admit(
	permit TargetWritePermit,
	object TargetObjectRef,
	mutation string,
) error {
	if permit.ValidateObjectAt(fake.now, object) != nil {
		return ErrInvalidTargetPermit
	}
	fake.calls = append(fake.calls, mutation)
	return nil
}

func (fake *recoveryMutationAdmissionTargetFake) CreateOwnedJobDir(
	_ context.Context,
	permit TargetWritePermit,
	request CreateOwnedJobDirRequest,
) (OwnedJobDir, error) {
	if err := fake.admit(permit, request.Object, "CreateOwnedJobDir"); err != nil {
		return OwnedJobDir{}, err
	}
	return OwnedJobDir{
		Object: request.Object, MarkerBindingDigest: request.MarkerBindingDigest,
		TargetRevision: "target-revision-workspace-created",
	}, nil
}

func (fake *recoveryMutationAdmissionTargetFake) CreateDirectory(
	_ context.Context,
	permit TargetWritePermit,
	request CreateTargetDirectoryRequest,
) error {
	return fake.admit(permit, request.Object, "CreateDirectory")
}

func (fake *recoveryMutationAdmissionTargetFake) WriteAtomic(
	_ context.Context,
	permit TargetWritePermit,
	request TargetWriteAtomicRequest,
) (TargetWriteResult, error) {
	if err := fake.admit(permit, request.Object, "WriteAtomic"); err != nil {
		return TargetWriteResult{}, err
	}
	return TargetWriteResult{TargetRevision: "target-revision-write"}, nil
}

func (fake *recoveryMutationAdmissionTargetFake) Delete(
	_ context.Context,
	permit TargetDeletePermit,
	request TargetDeleteRequest,
) (TargetWriteResult, error) {
	if err := fake.admit(TargetWritePermit{permit: permit.permit}, request.Object, "Delete"); err != nil {
		return TargetWriteResult{}, err
	}
	return TargetWriteResult{TargetRevision: "target-revision-delete"}, nil
}

func (fake *recoveryOwnedWorkspaceTargetFake) CreateOwnedJobDir(
	ctx context.Context,
	permit TargetWritePermit,
	request CreateOwnedJobDirRequest,
) (OwnedJobDir, error) {
	fake.calls++
	fake.request = request
	fake.permit = permit
	var latchCount int64
	if fake.db != nil {
		result := fake.db.WithContext(ctx).Model(&model.BackupAssetRecoveryEvidence{}).
			Where("id = ? AND kind = ?", recoverySchemaUseLatchRowID, RecoverySchemaUseLatchID).
			Count(&latchCount)
		fake.latchCommitted = result.Error == nil && latchCount == 1
	}
	if fake.now != nil {
		fake.currentAuthority = permit.ValidateObjectAt(fake.now().UTC(), request.Object) == nil
	}
	return OwnedJobDir{
		Object: request.Object, MarkerBindingDigest: request.MarkerBindingDigest,
		TargetRevision: "target-revision-workspace-created",
	}, nil
}

func TestRecoveryPrepareFirstWriteCarriesExactTargetSessionBinding(t *testing.T) {
	fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptExecute)
	executed, err := fixture.service.Authorize(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("execute recovery: %v", err)
	}
	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	target := &recoveryOwnedWorkspaceTargetFake{db: fixture.db, now: func() time.Time { return fixture.now }}
	coordinator.target = target
	claim, found, err := coordinator.ClaimNext(context.Background(), "target-session-worker")
	if err != nil || !found || claim.JobID != executed.JobID {
		t.Fatalf("claim recovery: claim=%+v found=%t error=%v", claim, found, err)
	}
	if _, err := coordinator.PrepareFirstWrite(context.Background(), claim); err != nil {
		t.Fatalf("prepare first write: %v", err)
	}

	var plan model.BackupAssetRecoveryPlan
	if err := fixture.db.Where("id = ?", executed.PlanID).Take(&plan).Error; err != nil {
		t.Fatalf("load executed plan: %v", err)
	}
	want, err := newRecoveryTargetSessionBinding(plan)
	if err != nil {
		t.Fatalf("derive expected target session binding: %v", err)
	}
	if target.calls != 1 || target.permit.permit.proof == nil ||
		target.permit.permit.proof.sessionBinding != want ||
		target.permit.permit.proof.bindingDigest != targetMutationPermitProofDigest(
			target.permit.permit, want,
		) {
		t.Fatalf("first-write target session proof = %+v, want exact locked plan binding %+v",
			target.permit.permit.proof, want)
	}
}

func TestRecoveryPrepareFirstWriteRejectsInvalidTargetSessionSnapshotBeforeRemote(t *testing.T) {
	tests := []struct {
		name      string
		column    string
		value     any
		wantError error
	}{
		{
			name: "noncanonical locator", column: "encrypted_target_root_locator",
			value:     "/srv/recovery/../FAKE_NONCANONICAL_TARGET_ROOT_FOR_TEST_ONLY",
			wantError: ErrRecoveryWorkerFenceLost,
		},
		{
			name: "locator substitution", column: "encrypted_target_root_locator",
			value:     "/srv/FAKE_SUBSTITUTED_TARGET_ROOT_FOR_TEST_ONLY",
			wantError: ErrRecoveryWorkerFenceLost,
		},
		{
			name: "locator digest mismatch", column: "root_locator_digest",
			value: strings.Repeat("f", sha256DigestLength), wantError: ErrRecoveryWorkerFenceLost,
		},
		{
			name: "node revision missing", column: "target_base_revision",
			value: "", wantError: ErrRecoveryWorkerFenceLost,
		},
		{
			name: "credential revision missing", column: "credential_scope_revision",
			value: "", wantError: ErrRecoveryWorkerFenceLost,
		},
		{
			name: "ciphertext cannot be hook-decrypted", column: "encrypted_target_root_locator",
			value:     "enc:v2:FAKE_TARGET_ROOT_CIPHERTEXT_FOR_TEST_ONLY",
			wantError: ErrRecoveryWorkerUnavailable,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptExecute)
			executed, err := fixture.service.Authorize(context.Background(), fixture.request)
			if err != nil {
				t.Fatalf("execute recovery fixture: %v", err)
			}
			coordinator := newRecoveryWorkerCoordinator(t, fixture)
			target := &recoveryOwnedWorkspaceTargetFake{db: fixture.db, now: func() time.Time { return fixture.now }}
			coordinator.target = target
			claim, found, err := coordinator.ClaimNext(context.Background(), "invalid-session-snapshot-worker")
			if err != nil || !found || claim.JobID != executed.JobID {
				t.Fatalf("claim recovery: claim=%+v found=%t error=%v", claim, found, err)
			}

			var beforeJob model.BackupAssetRecoveryJob
			if err := fixture.db.Where("id = ?", claim.JobID).Take(&beforeJob).Error; err != nil {
				t.Fatalf("load job before snapshot corruption: %v", err)
			}
			var beforeAttempt model.BackupAssetRecoveryAttempt
			if err := fixture.db.Where("id = ?", claim.AttemptID).Take(&beforeAttempt).Error; err != nil {
				t.Fatalf("load attempt before snapshot corruption: %v", err)
			}
			if err := fixture.db.Table((model.BackupAssetRecoveryPlan{}).TableName()).
				Where("id = ?", executed.PlanID).UpdateColumn(test.column, test.value).Error; err != nil {
				t.Fatalf("corrupt plan session snapshot: %v", err)
			}

			_, err = coordinator.PrepareFirstWrite(context.Background(), claim)
			if !errors.Is(err, test.wantError) {
				t.Fatalf("invalid target session snapshot error=%v, want %v", err, test.wantError)
			}
			if strings.Contains(err.Error(), "FAKE_") || target.calls != 0 {
				t.Fatalf("invalid snapshot leaked or reached target: error=%v calls=%d", err, target.calls)
			}
			var afterJob model.BackupAssetRecoveryJob
			if err := fixture.db.Where("id = ?", claim.JobID).Take(&afterJob).Error; err != nil {
				t.Fatalf("load job after snapshot rejection: %v", err)
			}
			var afterAttempt model.BackupAssetRecoveryAttempt
			if err := fixture.db.Where("id = ?", claim.AttemptID).Take(&afterAttempt).Error; err != nil {
				t.Fatalf("load attempt after snapshot rejection: %v", err)
			}
			if afterJob.WorkspacePhase != beforeJob.WorkspacePhase ||
				afterJob.WorkspaceMarkerBindingDigest != beforeJob.WorkspaceMarkerBindingDigest ||
				afterJob.WorkspaceOwner != beforeJob.WorkspaceOwner ||
				afterJob.WorkspaceFence != beforeJob.WorkspaceFence ||
				afterJob.PlaintextDeadline != beforeJob.PlaintextDeadline ||
				afterAttempt.MutationArmed != beforeAttempt.MutationArmed {
				t.Fatalf("invalid snapshot advanced durable state: before_job=%+v after_job=%+v before_attempt=%+v after_attempt=%+v",
					beforeJob, afterJob, beforeAttempt, afterAttempt)
			}
			var latchCount, checkpointCount int64
			if err := fixture.db.Model(&model.BackupAssetRecoveryEvidence{}).
				Where("id = ?", recoverySchemaUseLatchRowID).Count(&latchCount).Error; err != nil {
				t.Fatal(err)
			}
			if err := fixture.db.Model(&model.BackupAssetRecoveryCheckpoint{}).
				Where("job_id = ?", claim.JobID).Count(&checkpointCount).Error; err != nil {
				t.Fatal(err)
			}
			if latchCount != 0 || checkpointCount != 0 {
				t.Fatalf("invalid snapshot left latch/checkpoints: latch=%d checkpoints=%d", latchCount, checkpointCount)
			}
		})
	}
}

// recoveryInterruptedOperationAdopter describes the restart-only boundary: a
// new fence may adopt an observed remote mutation, but only into the durable
// checkpoint chain for its exact job item.
type recoveryInterruptedOperationAdopter interface {
	AdoptInterruptedOperation(
		context.Context,
		RecoveryWorkerClaim,
		string,
	) (model.BackupAssetRecoveryCheckpoint, error)
}

type recoveryRestartTargetFake struct {
	closedTargetPortFake
	mu           sync.Mutex
	observation  TargetVerifyObservation
	err          error
	beforeReturn func() error
	calls        int
	permits      []TargetVerifyPermit
	object       TargetObjectRef
	expectation  TargetVerifyExpectation
}

type recoveryAdoptionSourceResolverFake struct {
	mu     sync.Mutex
	source provider.RsyncRestoreSource
	err    error
	refs   []provider.RsyncRestoreSourceRef
}

func (fake *recoveryAdoptionSourceResolverFake) ResolveRsyncRestoreSource(
	_ context.Context,
	ref provider.RsyncRestoreSourceRef,
) (provider.RsyncRestoreSource, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.refs = append(fake.refs, ref)
	return fake.source, fake.err
}

func (fake *recoveryRestartTargetFake) Verify(
	_ context.Context,
	permit TargetVerifyPermit,
	object TargetObjectRef,
	expectation TargetVerifyExpectation,
) (TargetVerifyObservation, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	validateAt := permit.permit.ExpiresAt.Add(-time.Nanosecond)
	if permit.ValidateObjectAt(validateAt, object) != nil {
		return TargetVerifyObservation{}, ErrInvalidTargetPermit
	}
	fake.calls++
	fake.permits = append(fake.permits, permit)
	fake.object = object
	fake.expectation = expectation
	if fake.beforeReturn != nil {
		if err := fake.beforeReturn(); err != nil {
			return TargetVerifyObservation{}, err
		}
	}
	if fake.observation.Kind == "" && fake.err == nil {
		fake.observation = TargetVerifyObservation{Kind: expectation.Kind, ObservedRevision: "target-revision-e"}
		if expectation.Present != nil {
			fake.observation.Present = &PresentObservation{
				IdentityDigest: expectation.Present.IdentityDigest,
				Bytes:          expectation.Present.Bytes,
			}
		}
		if expectation.Absent != nil {
			fake.observation.Absent = &AbsentObservation{Evidence: TargetAbsenceEvidenceExact}
		}
	}
	return fake.observation, fake.err
}

type recoveryMutatingExpectationTargetFake struct {
	closedTargetPortFake
	calls       int
	expectation TargetVerifyExpectation
	observation TargetVerifyObservation
}

func (fake *recoveryMutatingExpectationTargetFake) Verify(
	_ context.Context,
	permit TargetVerifyPermit,
	object TargetObjectRef,
	expectation TargetVerifyExpectation,
) (TargetVerifyObservation, error) {
	if permit.ValidateObjectAt(permit.permit.ExpiresAt.Add(-time.Nanosecond), object) != nil {
		return TargetVerifyObservation{}, ErrInvalidTargetPermit
	}
	fake.calls++
	expectation.Present.IdentityDigest = strings.Repeat("d", sha256DigestLength)
	expectation.Present.Bytes++
	fake.expectation = expectation
	fake.observation = TargetVerifyObservation{
		Kind: TargetPresencePresent,
		Present: &PresentObservation{
			IdentityDigest: expectation.Present.IdentityDigest,
			Bytes:          expectation.Present.Bytes,
		},
		ObservedRevision: "target-revision-e",
	}
	return fake.observation, nil
}

func assertRecoveryTargetVerifyPermitProof(
	t *testing.T,
	permit TargetVerifyPermit,
	object TargetObjectRef,
	wantBinding recoveryTargetSessionBinding,
	wantJobID string,
	wantMode TargetMode,
	now time.Time,
) {
	t.Helper()
	proof := permit.permit.proof
	if proof == nil {
		t.Fatal("verify permit missing private proof")
	}
	if permit.ValidateObjectAt(now, object) != nil || proof.sessionBinding != wantBinding ||
		proof.jobID != wantJobID || proof.targetMode != wantMode ||
		!validRecoveryVerifyOperation(proof.operation, proof.expectedPrior) ||
		proof.bindingDigest != targetVerifyPermitProofDigest(permit.permit, proof) {
		t.Fatalf("verify permit proof=%+v binding=%+v job=%q mode=%q, want binding=%+v job=%q mode=%q",
			proof, proof.sessionBinding, proof.jobID, proof.targetMode, wantBinding, wantJobID, wantMode)
	}
}

func TestRecoveryInterruptedOperationHandoffCarriesLockedTargetSessionBinding(t *testing.T) {
	fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptExecute)
	if _, err := fixture.service.Authorize(context.Background(), fixture.request); err != nil {
		t.Fatalf("execute recovery fixture: %v", err)
	}
	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	claim, found, err := coordinator.ClaimNext(context.Background(), "locked-handoff-session-binding")
	if err != nil || !found {
		t.Fatalf("claim recovery job: found=%t err=%v", found, err)
	}
	if _, err := coordinator.PrepareFirstWrite(context.Background(), claim); err != nil {
		t.Fatalf("prepare durable first-write barrier: %v", err)
	}
	var item model.BackupAssetRecoveryJobItem
	if err := fixture.db.Where("job_id = ?", claim.JobID).Order("ordinal ASC").Take(&item).Error; err != nil {
		t.Fatal(err)
	}
	handoff, err := coordinator.loadOrdinaryOperationHandoff(context.Background(), claim, item.ID)
	if err != nil {
		t.Fatalf("load locked interrupted-operation handoff: %v", err)
	}
	want, err := newRecoveryTargetSessionBinding(handoff.plan)
	if err != nil {
		t.Fatalf("derive exact locked plan session binding: %v", err)
	}
	if handoff.targetSessionBinding != want {
		t.Fatalf("handoff target session binding=%+v, want exact locked plan binding=%+v",
			handoff.targetSessionBinding, want)
	}

	mutations := []struct {
		name   string
		mutate func(*recoveryTargetSessionBinding)
	}{
		{name: "plan", mutate: func(binding *recoveryTargetSessionBinding) {
			binding.PlanID = strings.Repeat("7", 32)
			binding.bindingDigest = binding.digest()
		}},
		{name: "node revision", mutate: func(binding *recoveryTargetSessionBinding) {
			binding.NodeRevision = "node-revision-substituted"
			binding.bindingDigest = binding.digest()
		}},
		{name: "credential revision", mutate: func(binding *recoveryTargetSessionBinding) {
			binding.CredentialRevision = "credential-revision-substituted"
			binding.bindingDigest = binding.digest()
		}},
		{name: "root locator", mutate: func(binding *recoveryTargetSessionBinding) {
			binding.RootLocator = "/srv/FAKE_SUBSTITUTED_HANDOFF_ROOT_FOR_TEST_ONLY"
			locatorDigest, digestErr := settings.RecoveryTargetRootLocatorDigest(
				binding.NodeID, binding.RootID, binding.RootLocator,
			)
			if digestErr != nil {
				t.Fatalf("derive substituted locator digest: %v", digestErr)
			}
			binding.RootLocatorDigest = locatorDigest
			binding.bindingDigest = binding.digest()
		}},
	}
	for _, testCase := range mutations {
		t.Run(testCase.name, func(t *testing.T) {
			mutated := handoff
			testCase.mutate(&mutated.targetSessionBinding)
			if _, err := newRecoveryTargetVerifyPermit(
				mutated, claim.LeaseExpiresAt, fixture.now,
			); !errors.Is(err, ErrRecoveryWorkerFenceLost) {
				t.Fatalf("substituted handoff verify permit error=%v, want ErrRecoveryWorkerFenceLost", err)
			}
		})
	}

	mutatedOperation := handoff
	mutatedOperation.operation.Kind = RecoveryOperationDelete
	mutatedOperation.operation.ExpectedPrior = ExpectedTargetIdentity{
		Kind: ExpectedTargetPresent, Digest: strings.Repeat("8", sha256DigestLength),
	}
	if handoff.operation.Kind == RecoveryOperationDelete {
		mutatedOperation.operation.Kind = RecoveryOperationCreate
		mutatedOperation.operation.ExpectedPrior = ExpectedTargetIdentity{Kind: ExpectedTargetAbsent}
	}
	if _, err := newRecoveryTargetVerifyPermit(
		mutatedOperation, claim.LeaseExpiresAt, fixture.now,
	); !errors.Is(err, ErrRecoveryWorkerFenceLost) {
		t.Fatalf("substituted handoff operation/prior verify permit error=%v, want ErrRecoveryWorkerFenceLost", err)
	}
}

func TestRecoveryAdoptionVerifyIssuanceUsesExactLockedTargetSessionBinding(t *testing.T) {
	fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptExecute)
	if _, err := fixture.service.Authorize(context.Background(), fixture.request); err != nil {
		t.Fatalf("execute recovery fixture: %v", err)
	}
	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	target := &recoveryRestartTargetFake{}
	coordinator.target = target
	first, found, err := coordinator.ClaimNext(context.Background(), "verify-issuance-adoption-before")
	if err != nil || !found {
		t.Fatalf("claim recovery job: found=%t err=%v", found, err)
	}
	if _, err := coordinator.PrepareFirstWrite(context.Background(), first); err != nil {
		t.Fatalf("prepare durable first-write barrier: %v", err)
	}
	var item model.BackupAssetRecoveryJobItem
	if err := fixture.db.Where("job_id = ?", first.JobID).Order("ordinal ASC").Take(&item).Error; err != nil {
		t.Fatal(err)
	}
	fixture.now = first.LeaseExpiresAt.Add(time.Second)
	takeover, found, err := coordinator.TakeoverExpired(context.Background(), "verify-issuance-adoption-after")
	if err != nil || !found {
		t.Fatalf("take over interrupted recovery job: found=%t err=%v", found, err)
	}
	if _, err := coordinator.AdoptInterruptedOperation(context.Background(), takeover, item.ID); err != nil {
		t.Fatalf("adopt interrupted recovery operation: %v", err)
	}
	if target.calls != 1 || len(target.permits) != 1 {
		t.Fatalf("adoption verify calls/permits=%d/%d, want 1/1", target.calls, len(target.permits))
	}
	var plan model.BackupAssetRecoveryPlan
	var job model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", takeover.JobID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Where("id = ?", job.PlanID).Take(&plan).Error; err != nil {
		t.Fatal(err)
	}
	want, err := newRecoveryTargetSessionBinding(plan)
	if err != nil {
		t.Fatalf("derive adoption session binding: %v", err)
	}
	assertRecoveryTargetVerifyPermitProof(
		t, target.permits[0], target.object, want, takeover.JobID, TargetMode(job.TargetMode), fixture.now,
	)
}

type recoveryOperationVerificationProjector interface {
	ProjectOperationVerification(
		context.Context,
		RecoveryWorkerClaim,
		string,
		string,
		int64,
		int64,
		string,
		time.Time,
	) (model.BackupAssetRecoveryEvidence, error)
}

type recoveryOperationMismatchProjector interface {
	ProjectOperationMismatch(
		context.Context,
		RecoveryWorkerClaim,
		string,
		string,
		int64,
		int64,
		string,
		time.Time,
		int64,
	) (model.BackupAssetRecoveryEvidence, error)
}

type recoveryPendingOperationMismatchProjector interface {
	projectPendingOperationMismatch(
		context.Context,
		RecoveryWorkerClaim,
		string,
		TargetWriteResult,
		TargetVerifyObservation,
		time.Time,
	) (model.BackupAssetRecoveryEvidence, error)
}

type recoveryWorkerCanceler interface {
	CancelJob(context.Context, string) error
}

type recoveryPermanentCleanupKeyReconciler interface {
	ReconcilePermanentCleanupKeyLoss(context.Context) (int, error)
}

func TestRecoveryPrepareFirstWriteUsesPreallocatedWorkspaceMatrix(t *testing.T) {
	fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptExecute)
	executed, err := fixture.service.Authorize(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("execute recovery: %v", err)
	}
	var before model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", executed.JobID).Take(&before).Error; err != nil {
		t.Fatalf("load unreserved job: %v", err)
	}
	workspaceBinding := reflect.ValueOf(before).FieldByName("WorkspaceBindingDigest")
	if TargetMode(before.TargetMode) != TargetModeIsolated || before.WorkspacePhase != string(WorkspacePhaseNone) ||
		before.EncryptedWorkspaceRelativeLocator == "" || !workspaceBinding.IsValid() || !validDigest(workspaceBinding.String()) ||
		before.WorkspaceMarkerBindingDigest != "" || before.WorkspaceOwner != "" || before.WorkspaceFence != 0 || before.PlaintextDeadline != nil {
		t.Fatalf("execute did not preallocate the isolated none-state workspace product: %+v", before)
	}
	wantWorkspaceLocator := "jobs/" + executed.JobID
	if before.EncryptedWorkspaceRelativeLocator != wantWorkspaceLocator {
		t.Fatalf("preallocated workspace locator = %q, want %q", before.EncryptedWorkspaceRelativeLocator, wantWorkspaceLocator)
	}
	var rawWorkspaceLocator string
	if err := fixture.db.Raw(
		"SELECT encrypted_workspace_relative_locator FROM backup_asset_recovery_jobs WHERE id = ?",
		executed.JobID,
	).Scan(&rawWorkspaceLocator).Error; err != nil {
		t.Fatalf("load raw workspace locator ciphertext: %v", err)
	}
	if !secure.IsEncrypted(rawWorkspaceLocator) || rawWorkspaceLocator == wantWorkspaceLocator ||
		strings.Contains(rawWorkspaceLocator, wantWorkspaceLocator) {
		t.Fatalf("workspace locator was not generic-encrypted at rest: %q", rawWorkspaceLocator)
	}

	var plan model.BackupAssetRecoveryPlan
	if err := fixture.db.Where("id = ?", before.PlanID).Take(&plan).Error; err != nil {
		t.Fatalf("load prepared plan: %v", err)
	}
	var preflight model.BackupAssetRecoveryPreflight
	if err := fixture.db.Where("id = ?", before.PreflightID).Take(&preflight).Error; err != nil {
		t.Fatalf("load prepared preflight: %v", err)
	}
	operations, err := decodeRecoveryOperationRows(preflight.EncryptedOperationRows)
	if err != nil {
		t.Fatalf("decode prepared operation snapshot: %v", err)
	}
	var items []model.BackupAssetRecoveryJobItem
	if err := fixture.db.Where("job_id = ?", before.ID).Order("ordinal ASC").Find(&items).Error; err != nil {
		t.Fatalf("load prepared job items: %v", err)
	}
	if len(items) == 0 || len(items) != len(operations) {
		t.Fatalf("prepared item count = %d, operation count = %d", len(items), len(operations))
	}
	locatorKeys, ok := fixture.dependencies.LocatorKeys.(*authorizationReceiptLocatorKeys)
	if !ok || locatorKeys == nil {
		t.Fatalf("unexpected recovery locator key source %T", fixture.dependencies.LocatorKeys)
	}
	material := cloneDomainKeyMaterial(locatorKeys.material)
	for index, operation := range operations {
		item := items[index]
		semanticDigest, digestErr := SemanticTargetDigest(
			TargetModeIsolated, plan.TargetRootID, plan.RootLocatorDigest, operation.TargetRelativeLocator,
		)
		if digestErr != nil {
			t.Fatalf("derive item %d semantic digest: %v", index, digestErr)
		}
		finalLocator := wantWorkspaceLocator + "/" + operation.TargetRelativeLocator
		objectDigest, digestErr := TargetObjectDigest(plan.TargetRootID, plan.RootLocatorDigest, finalLocator)
		if digestErr != nil {
			t.Fatalf("derive item %d final-object digest: %v", index, digestErr)
		}
		if semanticDigest == objectDigest || item.SemanticTargetDigest != semanticDigest ||
			item.TargetObjectDigest != objectDigest || item.TargetPathDigest != operation.TargetPathDigest ||
			item.TargetLocatorKeyVersion != material.Version ||
			item.TargetLocatorCipherVersion != targetLocatorCipherVersion ||
			!strings.HasPrefix(item.EncryptedTargetRelativeLocator, targetLocatorCiphertextPrefix) ||
			strings.HasPrefix(item.EncryptedTargetRelativeLocator, "enc:v2:") ||
			strings.Contains(item.EncryptedTargetRelativeLocator, operation.TargetRelativeLocator) {
			t.Fatalf("prepared item %d did not preserve distinct semantic/final locator products: %+v", index, item)
		}

		planItemID := ""
		if item.PlanItemID != nil {
			planItemID = *item.PlanItemID
		}
		sourceRecoveryPointID := ""
		sourceEntryID := ""
		if operation.Source.AssetRef != nil {
			sourceRecoveryPointID = operation.Source.AssetRef.RecoveryPointID
			sourceEntryID = operation.Source.AssetRef.EntryID
		}
		binding := TargetLocatorEnvelopeBinding{
			CodecVersion: targetLocatorEnvelopeSchemaVersion,
			JobID:        before.ID, JobItemID: item.ID, PlanDigest: plan.BindingDigest, PlanItemID: planItemID,
			SourceRecoveryPointID: sourceRecoveryPointID, SourceEntryID: sourceEntryID,
			TargetMode: TargetModeIsolated, NodeID: plan.TargetNodeID, RootID: plan.TargetRootID,
			RootLocatorDigest: plan.RootLocatorDigest, SemanticTargetDigest: semanticDigest,
			TargetObjectDigest: objectDigest, Operation: operation.Kind,
			WorkspaceBindingDigest:   before.WorkspaceBindingDigest,
			WorkspaceRelativeLocator: wantWorkspaceLocator,
			ExpectedPriorKind:        operation.ExpectedPrior.Kind, ExpectedPriorDigest: operation.ExpectedPrior.Digest,
			ExpectedPostIdentityDigest: operation.ExpectedPostIdentityDigest,
			ExpectedPostBytes:          operation.ExpectedPostBytes, ExpectedPriorBytes: operation.ExpectedPriorBytes,
			TargetLocatorKeyVersion:    item.TargetLocatorKeyVersion,
			TargetLocatorCipherVersion: item.TargetLocatorCipherVersion,
		}
		locator, openErr := OpenTargetLocatorEnvelope(material, binding, item.EncryptedTargetRelativeLocator)
		if openErr != nil || locator != operation.TargetRelativeLocator {
			t.Fatalf("open prepared item %d locator = %q, error = %v, want %q", index, locator, openErr, operation.TargetRelativeLocator)
		}
	}

	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	target := &recoveryOwnedWorkspaceTargetFake{db: fixture.db, now: func() time.Time { return fixture.now }}
	coordinator.target = target
	claim, found, err := coordinator.ClaimNext(context.Background(), "task6-worker")
	if err != nil || !found || claim.JobID != executed.JobID {
		t.Fatalf("claim prepared job: claim=%+v found=%t error=%v", claim, found, err)
	}
	if _, err := coordinator.PrepareFirstWrite(context.Background(), claim); err != nil {
		t.Fatalf("reserve preallocated workspace: %v", err)
	}
	var after model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", executed.JobID).Take(&after).Error; err != nil {
		t.Fatalf("load reserved job: %v", err)
	}
	afterBinding := reflect.ValueOf(after).FieldByName("WorkspaceBindingDigest")
	if after.EncryptedWorkspaceRelativeLocator != before.EncryptedWorkspaceRelativeLocator ||
		!afterBinding.IsValid() || afterBinding.String() != workspaceBinding.String() {
		t.Fatalf("PrepareFirstWrite replaced preallocated workspace identity: before=%+v after=%+v", before, after)
	}
	if target.calls != 1 {
		t.Fatalf("PrepareFirstWrite mutating target calls=%d, want one owned-workspace creation", target.calls)
	}
	if !target.latchCommitted || !target.currentAuthority {
		t.Fatalf("owned-workspace creation observed latch=%t current-authority=%t, want both committed before target mutation",
			target.latchCommitted, target.currentAuthority)
	}
	if target.request.Object.PrivateRelativeLocator != before.EncryptedWorkspaceRelativeLocator ||
		target.request.MarkerBindingDigest != after.WorkspaceMarkerBindingDigest ||
		target.request.MarkerCreatorID != after.WorkspaceOwner ||
		target.request.MarkerCreatorFence != after.WorkspaceFence {
		t.Fatalf("owned-workspace creation request=%+v, want exact persisted workspace locator and marker binding", target.request)
	}
	markerProductBefore := loadRecoveryMarkerValidationProduct(t, fixture.db, after.ID)
	substituted := target.request
	substituted.MarkerCreatorID = "substituted-marker-creator"
	substituted.MarkerCreatorFence++
	if err := coordinator.markWorkspaceMarkerCreated(context.Background(), claim, substituted); !errors.Is(err, ErrRecoveryWorkerFenceLost) {
		t.Fatalf("substituted marker creator error=%v, want ErrRecoveryWorkerFenceLost", err)
	}
	var afterSubstitution model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", after.ID).Take(&afterSubstitution).Error; err != nil {
		t.Fatalf("reload job after marker creator substitution: %v", err)
	}
	if afterSubstitution.WorkspacePhase != after.WorkspacePhase ||
		loadRecoveryMarkerValidationProduct(t, fixture.db, after.ID) != markerProductBefore {
		t.Fatalf("marker creator substitution changed durable marker state: before=%+v after=%+v",
			after, afterSubstitution)
	}
}

func TestRecoveryPrepareFirstWritePersistsMarkerCreatedBeforeContent(t *testing.T) {
	t.Run("marker phase precedes item content and writing follows the first checkpoint", func(t *testing.T) {
		fixture := newRecoveryExecutionFixture(t)
		executed, err := fixture.service.Authorize(context.Background(), fixture.request)
		if err != nil {
			t.Fatalf("execute recovery fixture: %v", err)
		}
		coordinator := newRecoveryWorkerCoordinator(t, fixture)
		type phaseObservation struct {
			phase                string
			operationCheckpoints int64
		}
		workspacePhases := make([]string, 0, 1)
		contentPhases := make([]phaseObservation, 0, 2)
		target := &recoveryExecutionTargetFake{
			db: fixture.db, now: func() time.Time { return fixture.now },
			beforeWorkspaceCreate: func(ctx context.Context, _ CreateOwnedJobDirRequest) error {
				var job model.BackupAssetRecoveryJob
				if err := fixture.db.WithContext(ctx).Where("id = ?", executed.JobID).Take(&job).Error; err != nil {
					return err
				}
				workspacePhases = append(workspacePhases, job.WorkspacePhase)
				return nil
			},
			beforeWrite: func(ctx context.Context, _ int, _ TargetWriteAtomicRequest) error {
				var job model.BackupAssetRecoveryJob
				if err := fixture.db.WithContext(ctx).Where("id = ?", executed.JobID).Take(&job).Error; err != nil {
					return err
				}
				var checkpoints int64
				if err := fixture.db.WithContext(ctx).Model(&model.BackupAssetRecoveryCheckpoint{}).
					Where("job_id = ? AND phase = ?", executed.JobID, CheckpointPhaseOperation).
					Count(&checkpoints).Error; err != nil {
					return err
				}
				contentPhases = append(contentPhases, phaseObservation{
					phase: job.WorkspacePhase, operationCheckpoints: checkpoints,
				})
				return nil
			},
		}
		coordinator.target = target
		claim, found, err := coordinator.ClaimNext(context.Background(), "marker-phase-order-worker")
		if err != nil || !found || claim.JobID != executed.JobID {
			t.Fatalf("claim marker-phase recovery: claim=%+v found=%t err=%v", claim, found, err)
		}
		source := newRecoveryRepositoryContractSource(t, fixture.db, claim.JobID)
		if err := coordinator.ExecuteClaim(context.Background(), claim, source, ""); err != nil {
			t.Fatalf("execute marker-phase recovery: %v", err)
		}
		if !reflect.DeepEqual(workspacePhases, []string{string(WorkspacePhaseReserved)}) {
			t.Fatalf("owned workspace durable phases=%v, want reserved", workspacePhases)
		}
		wantContent := []phaseObservation{
			{phase: string(WorkspacePhaseMarkerCreated), operationCheckpoints: 0},
			{phase: string(WorkspacePhaseWriting), operationCheckpoints: 1},
		}
		if !reflect.DeepEqual(contentPhases, wantContent) {
			t.Fatalf("item content durable phases=%+v, want %+v", contentPhases, wantContent)
		}
	})

	t.Run("retry reuses the same durable marker identity", func(t *testing.T) {
		fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptExecute)
		executed, err := fixture.service.Authorize(context.Background(), fixture.request)
		if err != nil {
			t.Fatalf("execute recovery fixture: %v", err)
		}
		coordinator := newRecoveryWorkerCoordinator(t, fixture)
		phases := make([]string, 0, 2)
		requests := make([]CreateOwnedJobDirRequest, 0, 2)
		target := &recoveryExecutionTargetFake{
			db: fixture.db, now: func() time.Time { return fixture.now },
			beforeWorkspaceCreate: func(ctx context.Context, request CreateOwnedJobDirRequest) error {
				var job model.BackupAssetRecoveryJob
				if err := fixture.db.WithContext(ctx).Where("id = ?", executed.JobID).Take(&job).Error; err != nil {
					return err
				}
				phases = append(phases, job.WorkspacePhase)
				requests = append(requests, request)
				return nil
			},
		}
		coordinator.target = target
		claim, found, err := coordinator.ClaimNext(context.Background(), "marker-phase-retry-worker")
		if err != nil || !found || claim.JobID != executed.JobID {
			t.Fatalf("claim marker retry recovery: claim=%+v found=%t err=%v", claim, found, err)
		}
		if _, err := coordinator.PrepareFirstWrite(context.Background(), claim); err != nil {
			t.Fatalf("prepare first marker write: %v", err)
		}
		var first model.BackupAssetRecoveryJob
		if err := fixture.db.Where("id = ?", claim.JobID).Take(&first).Error; err != nil {
			t.Fatal(err)
		}
		if _, err := coordinator.PrepareFirstWrite(context.Background(), claim); err != nil {
			t.Fatalf("retry first marker write: %v", err)
		}
		var second model.BackupAssetRecoveryJob
		if err := fixture.db.Where("id = ?", claim.JobID).Take(&second).Error; err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(phases, []string{
			string(WorkspacePhaseReserved), string(WorkspacePhaseMarkerCreated),
		}) {
			t.Fatalf("marker retry durable phases=%v", phases)
		}
		if len(requests) != 2 || requests[0] != requests[1] ||
			first.WorkspacePhase != string(WorkspacePhaseMarkerCreated) ||
			second.WorkspacePhase != string(WorkspacePhaseMarkerCreated) ||
			first.EncryptedWorkspaceRelativeLocator != second.EncryptedWorkspaceRelativeLocator ||
			first.WorkspaceBindingDigest != second.WorkspaceBindingDigest ||
			first.WorkspaceMarkerBindingDigest != second.WorkspaceMarkerBindingDigest ||
			first.WorkspaceOwner != second.WorkspaceOwner || first.WorkspaceFence != second.WorkspaceFence ||
			first.PlaintextDeadline == nil || second.PlaintextDeadline == nil ||
			!first.PlaintextDeadline.Equal(*second.PlaintextDeadline) {
			t.Fatalf("marker retry changed durable identity: requests=%+v first=%+v second=%+v",
				requests, first, second)
		}
	})

	t.Run("marker phase CAS failure blocks every item call", func(t *testing.T) {
		fixture := newRecoveryExecutionFixture(t)
		executed, err := fixture.service.Authorize(context.Background(), fixture.request)
		if err != nil {
			t.Fatalf("execute recovery fixture: %v", err)
		}
		coordinator := newRecoveryWorkerCoordinator(t, fixture)
		callbackName := "recovery:marker-created-cas-failure:" + t.Name()
		injected := false
		target := &recoveryExecutionTargetFake{
			db: fixture.db, now: func() time.Time { return fixture.now },
			beforeWorkspaceCreate: func(context.Context, CreateOwnedJobDirRequest) error {
				return fixture.db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
					if injected || tx.Statement == nil ||
						tx.Statement.Table != (model.BackupAssetRecoveryJob{}).TableName() {
						return
					}
					injected = true
					_ = tx.AddError(errors.New("inject marker-created CAS failure"))
				})
			},
		}
		t.Cleanup(func() { _ = fixture.db.Callback().Update().Remove(callbackName) })
		coordinator.target = target
		claim, found, err := coordinator.ClaimNext(context.Background(), "marker-phase-cas-failure-worker")
		if err != nil || !found || claim.JobID != executed.JobID {
			t.Fatalf("claim marker CAS recovery: claim=%+v found=%t err=%v", claim, found, err)
		}
		source := newRecoveryRepositoryContractSource(t, fixture.db, claim.JobID)
		executeErr := coordinator.ExecuteClaim(context.Background(), claim, source, "")
		if !errors.Is(executeErr, ErrRecoveryWorkerUnavailable) || !injected {
			t.Fatalf("marker CAS failure error=%v injected=%t, want unavailable after injected CAS", executeErr, injected)
		}
		if len(target.workspaceCalls) != 1 || len(target.writes) != 0 || len(target.deletes) != 0 ||
			len(target.verifies) != 0 {
			t.Fatalf("marker CAS failure target calls workspace/writes/deletes/verifies=%d/%d/%d/%d, want 1/0/0/0",
				len(target.workspaceCalls), len(target.writes), len(target.deletes), len(target.verifies))
		}
		var job model.BackupAssetRecoveryJob
		if err := fixture.db.Where("id = ?", claim.JobID).Take(&job).Error; err != nil {
			t.Fatal(err)
		}
		var operationCheckpoints int64
		if err := fixture.db.Model(&model.BackupAssetRecoveryCheckpoint{}).
			Where("job_id = ? AND phase = ?", claim.JobID, CheckpointPhaseOperation).
			Count(&operationCheckpoints).Error; err != nil {
			t.Fatal(err)
		}
		if job.WorkspacePhase != string(WorkspacePhaseReserved) || operationCheckpoints != 0 {
			t.Fatalf("marker CAS failure durable state job=%+v operation_checkpoints=%d", job, operationCheckpoints)
		}
	})
}

func TestRecoveryReservedMarkerTakeoverPersistsValidationBeforeFirstItem(t *testing.T) {
	fixture := newRecoveryExecutionFixture(t)
	executed, err := fixture.service.Authorize(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("execute recovery fixture: %v", err)
	}
	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	callbackName := "recovery:reserved-marker-takeover-cas-failure:" + t.Name()
	injected := false
	target := &recoveryExecutionTargetFake{
		db: fixture.db, now: func() time.Time { return fixture.now },
		beforeWorkspaceCreate: func(context.Context, CreateOwnedJobDirRequest) error {
			return fixture.db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
				if injected || tx.Statement == nil ||
					tx.Statement.Table != (model.BackupAssetRecoveryJob{}).TableName() {
					return
				}
				injected = true
				_ = tx.AddError(errors.New("inject reserved marker takeover CAS failure"))
			})
		},
	}
	t.Cleanup(func() { _ = fixture.db.Callback().Update().Remove(callbackName) })
	coordinator.target = target
	claim, found, err := coordinator.ClaimNext(context.Background(), "reserved-marker-before")
	if err != nil || !found || claim.JobID != executed.JobID {
		t.Fatalf("claim reserved marker recovery: claim=%+v found=%t err=%v", claim, found, err)
	}
	source := newRecoveryRepositoryContractSource(t, fixture.db, claim.JobID)
	if executeErr := coordinator.ExecuteClaim(context.Background(), claim, source, ""); !errors.Is(executeErr, ErrRecoveryWorkerUnavailable) || !injected {
		t.Fatalf("reserved marker crash error=%v injected=%t", executeErr, injected)
	}
	if err := fixture.db.Callback().Update().Remove(callbackName); err != nil {
		t.Fatalf("remove reserved marker takeover callback: %v", err)
	}

	var reserved model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", claim.JobID).Take(&reserved).Error; err != nil {
		t.Fatal(err)
	}
	if product := loadRecoveryMarkerValidationProduct(t, fixture.db, claim.JobID); reserved.WorkspacePhase != string(WorkspacePhaseReserved) || product != (recoveryMarkerValidationProduct{}) ||
		len(target.workspaceCalls) != 1 || len(target.writes) != 0 ||
		target.workspaceCalls[0].MarkerCreatorID != reserved.WorkspaceOwner ||
		target.workspaceCalls[0].MarkerCreatorFence != reserved.WorkspaceFence {
		t.Fatalf("marker CAS crash state job=%+v product=%+v workspace_calls=%d writes=%d",
			reserved, product, len(target.workspaceCalls), len(target.writes))
	}

	fixture.now = claim.LeaseExpiresAt.Add(time.Second)
	restarted := newRecoveryWorkerCoordinator(t, fixture)
	restarted.target = target
	takeover, found, err := restarted.TakeoverExpired(context.Background(), "reserved-marker-after")
	if err != nil || !found || takeover.JobID != claim.JobID || takeover.AttemptID == claim.AttemptID {
		t.Fatalf("take over reserved marker recovery: claim=%+v found=%t err=%v", takeover, found, err)
	}
	workspaceValidated := false
	firstItemValidated := false
	target.beforeWorkspaceCreate = func(ctx context.Context, request CreateOwnedJobDirRequest) error {
		var current model.BackupAssetRecoveryJob
		if err := fixture.db.WithContext(ctx).Where("id = ?", takeover.JobID).Take(&current).Error; err != nil {
			return err
		}
		product := loadRecoveryMarkerValidationProduct(t, fixture.db.WithContext(ctx), takeover.JobID)
		if current.WorkspacePhase != string(WorkspacePhaseReserved) ||
			current.WorkspaceOwner != reserved.WorkspaceOwner || current.WorkspaceFence != reserved.WorkspaceFence ||
			current.WorkspaceMarkerBindingDigest != reserved.WorkspaceMarkerBindingDigest ||
			product != (recoveryMarkerValidationProduct{}) || request != target.workspaceCalls[0] {
			return fmt.Errorf("takeover workspace validation preceded wrong durable state: job=%+v product=%+v request=%+v",
				current, product, request)
		}
		workspaceValidated = true
		return nil
	}
	target.beforeWrite = func(ctx context.Context, call int, _ TargetWriteAtomicRequest) error {
		if call != 1 {
			return nil
		}
		var current model.BackupAssetRecoveryJob
		if err := fixture.db.WithContext(ctx).Where("id = ?", takeover.JobID).Take(&current).Error; err != nil {
			return err
		}
		product := loadRecoveryMarkerValidationProduct(t, fixture.db.WithContext(ctx), takeover.JobID)
		want := recoveryMarkerValidationProduct{
			AttemptID: takeover.AttemptID, AttemptFence: takeover.AttemptFence, NodeFence: takeover.NodeFence,
		}
		if current.WorkspacePhase != string(WorkspacePhaseMarkerCreated) || product != want {
			return fmt.Errorf("item write preceded durable marker validation: phase=%s product=%+v want=%+v",
				current.WorkspacePhase, product, want)
		}
		firstItemValidated = true
		return nil
	}
	continuationSource := newRecoveryRepositoryContractSource(t, fixture.db, takeover.JobID)
	if err := restarted.ExecuteClaim(context.Background(), takeover, continuationSource, ""); err != nil {
		t.Fatalf("execute reserved marker takeover: %v", err)
	}
	var terminal model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", takeover.JobID).Take(&terminal).Error; err != nil {
		t.Fatal(err)
	}
	wantProduct := recoveryMarkerValidationProduct{
		AttemptID: takeover.AttemptID, AttemptFence: takeover.AttemptFence, NodeFence: takeover.NodeFence,
	}
	if product := loadRecoveryMarkerValidationProduct(t, fixture.db, takeover.JobID); !workspaceValidated || !firstItemValidated || product != wantProduct ||
		terminal.State != string(JobStateSucceeded) || terminal.WorkspacePhase != string(WorkspacePhaseSealed) ||
		terminal.WorkspaceOwner != reserved.WorkspaceOwner || terminal.WorkspaceFence != reserved.WorkspaceFence ||
		terminal.WorkspaceMarkerBindingDigest != reserved.WorkspaceMarkerBindingDigest ||
		len(target.workspaceCalls) != 2 || target.workspaceCalls[0] != target.workspaceCalls[1] ||
		target.workspaceCalls[1].MarkerCreatorID != reserved.WorkspaceOwner ||
		target.workspaceCalls[1].MarkerCreatorFence != reserved.WorkspaceFence {
		t.Fatalf("reserved marker takeover terminal job=%+v product=%+v workspace_calls=%+v observed=%t/%t",
			terminal, product, target.workspaceCalls, workspaceValidated, firstItemValidated)
	}
}

func TestRecoveryMarkerCreatedTakeoverRequiresAdoptionBeforeReplay(t *testing.T) {
	fixture := newRecoveryExecutionFixture(t)
	executed, err := fixture.service.Authorize(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("execute recovery fixture: %v", err)
	}
	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	target := &recoveryExecutionTargetFake{db: fixture.db, now: func() time.Time { return fixture.now }}
	coordinator.target = target
	claim, found, err := coordinator.ClaimNext(context.Background(), "marker-created-before")
	if err != nil || !found || claim.JobID != executed.JobID {
		t.Fatalf("claim marker-created recovery: claim=%+v found=%t err=%v", claim, found, err)
	}
	if _, err := coordinator.PrepareFirstWrite(context.Background(), claim); err != nil {
		t.Fatalf("persist marker-created recovery phase: %v", err)
	}
	before := loadRecoveryMarkerValidationProduct(t, fixture.db, claim.JobID)
	wantBefore := recoveryMarkerValidationProduct{
		AttemptID: claim.AttemptID, AttemptFence: claim.AttemptFence, NodeFence: claim.NodeFence,
	}
	if before != wantBefore || len(target.workspaceCalls) != 1 || len(target.writes) != 0 {
		t.Fatalf("initial marker validation product=%+v want=%+v workspace_calls=%d writes=%d",
			before, wantBefore, len(target.workspaceCalls), len(target.writes))
	}

	fixture.now = claim.LeaseExpiresAt.Add(time.Second)
	restarted := newRecoveryWorkerCoordinator(t, fixture)
	restarted.target = target
	takeover, found, err := restarted.TakeoverExpired(context.Background(), "marker-created-after")
	if err != nil || !found || takeover.JobID != claim.JobID || takeover.AttemptID == claim.AttemptID {
		t.Fatalf("take over marker-created recovery: claim=%+v found=%t err=%v", takeover, found, err)
	}
	source := newRecoveryRepositoryContractSource(t, fixture.db, takeover.JobID)
	if err := restarted.ExecuteClaim(context.Background(), takeover, source, ""); !errors.Is(err, ErrRecoveryWorkerFenceLost) {
		t.Fatalf("marker-created takeover ordinary replay error=%v, want ErrRecoveryWorkerFenceLost", err)
	}
	after := loadRecoveryMarkerValidationProduct(t, fixture.db, takeover.JobID)
	if after != before || len(target.workspaceCalls) != 1 || len(target.writes) != 0 || len(target.deletes) != 0 {
		t.Fatalf("marker-created takeover mutated before adoption: before=%+v after=%+v workspace/writes/deletes=%d/%d/%d",
			before, after, len(target.workspaceCalls), len(target.writes), len(target.deletes))
	}
}

func TestRecoveryAdoptInterruptedOperationDurableDerivationMatrix(t *testing.T) {
	t.Run("internal-only signature", func(t *testing.T) {
		method, found := reflect.TypeOf(&WorkerCoordinator{}).MethodByName("AdoptInterruptedOperation")
		if !found {
			t.Fatal("WorkerCoordinator.AdoptInterruptedOperation is missing")
		}
		// Receiver plus ctx, claim, and jobItemID: no caller locator, revision,
		// identity, operation, or chain-advance product is accepted.
		if method.Type.NumIn() != 4 || method.Type.In(1) != reflect.TypeOf((*context.Context)(nil)).Elem() ||
			method.Type.In(2) != reflect.TypeOf(RecoveryWorkerClaim{}) || method.Type.In(3).Kind() != reflect.String {
			t.Fatalf("AdoptInterruptedOperation signature=%s, want (ctx, claim, jobItemID)", method.Type)
		}
	})
	t.Run("create derives and atomically adopts durable product", TestWorkerRestartAdoptsInterruptedMutationFromSequenceZero)
	t.Run("skip derives separate unchanged-target product", TestWorkerRestartAdoptsSkipWithAtomicTerminalFactsAndAttemptClosure)
	t.Run("attempt close failure rolls back projection", TestWorkerAdoptionRollsBackProjectionWhenAttemptClosureFails)
	t.Run("caller cannot substitute target observation", TestWorkerRestartAdoptionRejectsCallerSuppliedVerifiedIdentityWithoutTargetObservation)
	t.Run("mutated expectation is rejected", TestWorkerRestartAdoptionRejectsMutatedVerifyExpectation)
	t.Run("invalid durable expectation is rejected before target I/O", TestWorkerRestartAdoptionRejectsInvalidExpectationBeforeTargetObservation)
	t.Run("closed invalid and uncertain observations fail closed", TestWorkerRestartAdoptionFailsClosedOnInvalidOrUncertainTargetObservation)
	t.Run("delete without consumed authority is rejected before source or target I/O", testRecoveryAdoptionRejectsDeleteWithoutConsumedAuthorityBeforeIO)
	t.Run("unknown item identity is rejected before target I/O", testRecoveryAdoptionRejectsUnknownItemIdentity)
	t.Run("durable drift between I/O and final lock is rejected", testRecoveryAdoptionRejectsDurableDriftBetweenBoundaries)
}

func TestRecoveryAdoptInterruptedOperationUsesProviderSourceAndTerminalDisposition(t *testing.T) {
	const privateFailure = "private-adoption-target-failure: source/private-locator"
	privateErr := errors.New(privateFailure)
	tests := []struct {
		name             string
		sourceErr        error
		configureTarget  func(*recoveryRestartTargetFake)
		category         UnresolvedOperationCategory
		sourceOutcome    SourceRevalidationOutcome
		wantSourceFailed bool
		wantObservation  bool
	}{
		{
			name: "verify error becomes no-product unresolved",
			configureTarget: func(target *recoveryRestartTargetFake) {
				target.err = privateErr
			},
			category: UnresolvedOperationObservationInvalid, sourceOutcome: SourceRevalidationMatched,
		},
		{
			name: "invalid observation becomes unresolved",
			configureTarget: func(target *recoveryRestartTargetFake) {
				target.observation = TargetVerifyObservation{Kind: TargetPresencePresent}
			},
			category: UnresolvedOperationObservationInvalid, sourceOutcome: SourceRevalidationMatched,
			wantObservation: true,
		},
		{
			name: "verification mismatch becomes unresolved",
			configureTarget: func(target *recoveryRestartTargetFake) {
				target.observation = recoveryRestartObservationForTest()
				target.observation.Present.IdentityDigest = strings.Repeat("d", sha256DigestLength)
			},
			category: UnresolvedOperationVerificationMismatch, sourceOutcome: SourceRevalidationMatched,
			wantObservation: true,
		},
		{
			name:      "matching target with untrusted source retains operation then terminates",
			sourceErr: provider.ErrRsyncRestoreSourceDrift, wantSourceFailed: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptExecute)
			if _, err := fixture.service.Authorize(context.Background(), fixture.request); err != nil {
				t.Fatalf("execute recovery fixture: %v", err)
			}
			source := &recoveryExecutionSourceFake{revalidateErr: test.sourceErr}
			resolver := &recoveryAdoptionSourceResolverFake{source: source}
			coordinator := newRecoveryWorkerCoordinatorWithSourceResolver(t, fixture, resolver)
			target := &recoveryRestartTargetFake{}
			if test.configureTarget != nil {
				test.configureTarget(target)
			}
			coordinator.target = target
			first, found, err := coordinator.ClaimNext(context.Background(), "provider-source-adoption-before")
			if err != nil || !found {
				t.Fatalf("claim recovery job: found=%t err=%v", found, err)
			}
			if _, err := coordinator.PrepareFirstWrite(context.Background(), first); err != nil {
				t.Fatalf("prepare durable first-write barrier: %v", err)
			}
			var durableJob model.BackupAssetRecoveryJob
			if err := fixture.db.Where("id = ?", first.JobID).Take(&durableJob).Error; err != nil {
				t.Fatal(err)
			}
			var plan model.BackupAssetRecoveryPlan
			if err := fixture.db.Where("id = ?", durableJob.PlanID).Take(&plan).Error; err != nil {
				t.Fatal(err)
			}
			wantRef, err := NewRsyncRestoreSourceRef(plan)
			if err != nil {
				t.Fatalf("derive durable Rsync source ref: %v", err)
			}
			var item model.BackupAssetRecoveryJobItem
			if err := fixture.db.Where("job_id = ?", first.JobID).Order("ordinal ASC").Take(&item).Error; err != nil {
				t.Fatal(err)
			}
			fixture.now = first.LeaseExpiresAt.Add(time.Second)
			takeover, found, err := coordinator.TakeoverExpired(context.Background(), "provider-source-adoption-after")
			if err != nil || !found {
				t.Fatalf("take over interrupted recovery job: found=%t err=%v", found, err)
			}
			var before model.BackupAssetRecoveryJob
			if err := fixture.db.Where("id = ?", takeover.JobID).Take(&before).Error; err != nil {
				t.Fatal(err)
			}

			_, adoptErr := coordinator.AdoptInterruptedOperation(context.Background(), takeover, item.ID)
			if test.wantSourceFailed {
				if !errors.Is(adoptErr, ErrRecoverySourceChanged) {
					t.Fatalf("untrusted adoption source error=%v, want ErrRecoverySourceChanged", adoptErr)
				}
				assertRecoverySourceRevalidationTerminalProjection(
					t, fixture.db, takeover, before, adoptErr,
				)
			} else {
				if !errors.Is(adoptErr, ErrInvalidTargetVerification) ||
					errors.Is(adoptErr, ErrRecoveryWorkerFenceLost) {
					t.Fatalf("adoption target outcome error=%v, want target verification without fence loss", adoptErr)
				}
				assertRecoveryUnresolvedOutcomeProjection(
					t, fixture.db, takeover, before, test.category, test.sourceOutcome, 0,
				)
				var checkpoint model.BackupAssetRecoveryCheckpoint
				if err := fixture.db.Where("job_id = ?", takeover.JobID).
					Order("sequence DESC").Take(&checkpoint).Error; err != nil {
					t.Fatal(err)
				}
				if checkpoint.WriteResultDigest != "" || checkpoint.WriteTargetRevision != "" ||
					(checkpoint.ObservationDigest != "") != test.wantObservation ||
					(!test.wantObservation && (checkpoint.ObservedTargetRevision != "" || checkpoint.ObservedPresence != "")) {
					t.Fatalf("adoption no-write unresolved checkpoint=%+v want_observation=%t",
						checkpoint, test.wantObservation)
				}
			}
			if target.calls != 1 || len(resolver.refs) != 1 || resolver.refs[0] != wantRef ||
				source.revalidates != 1 || source.closes != 1 || len(source.opened) != 0 || len(source.materialized) != 0 {
				t.Fatalf("adoption source/target calls target=%d refs=%+v want_ref=%+v revalidate/close/open/materialize=%d/%d/%d/%d",
					target.calls, resolver.refs, wantRef, source.revalidates, source.closes,
					len(source.opened), len(source.materialized))
			}
			if adoptErr != nil && (strings.Contains(adoptErr.Error(), privateFailure) ||
				strings.Contains(adoptErr.Error(), "source/private-locator")) {
				t.Fatalf("adoption leaked private target/source failure: %v", adoptErr)
			}
			refType := reflect.TypeOf(resolver.refs[0])
			for index := 0; index < refType.NumField(); index++ {
				name := strings.ToLower(refType.Field(index).Name)
				if strings.Contains(name, "locator") || strings.Contains(name, "root") || strings.Contains(name, "task") {
					t.Fatalf("adoption resolver input exposes provider-private field %q", refType.Field(index).Name)
				}
			}
		})
	}
}

func TestRecoveryAdoptInterruptedOperationObservesBeforeProjectionTransaction(t *testing.T) {
	fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptExecute)
	if _, err := fixture.service.Authorize(context.Background(), fixture.request); err != nil {
		t.Fatalf("execute recovery fixture: %v", err)
	}
	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	first, found, err := coordinator.ClaimNext(context.Background(), "authority-order-adoption-before")
	if err != nil || !found {
		t.Fatalf("claim recovery job: found=%t err=%v", found, err)
	}
	if _, err := coordinator.PrepareFirstWrite(context.Background(), first); err != nil {
		t.Fatalf("prepare durable first-write barrier: %v", err)
	}
	var item model.BackupAssetRecoveryJobItem
	if err := fixture.db.Where("job_id = ?", first.JobID).Order("ordinal ASC").Take(&item).Error; err != nil {
		t.Fatal(err)
	}
	fixture.now = first.LeaseExpiresAt.Add(time.Second)
	takeover, found, err := coordinator.TakeoverExpired(context.Background(), "authority-order-adoption-after")
	if err != nil || !found {
		t.Fatalf("take over interrupted recovery job: found=%t err=%v", found, err)
	}
	spy := &recoveryWorkerAuthorityEffectSpy{observation: RecoveryAuthorityObservation{
		observedAt: fixture.now,
		expiresAt:  fixture.now.Add(time.Minute),
	}}
	coordinator.liveRevalidator = spy

	if _, err := coordinator.AdoptInterruptedOperation(context.Background(), takeover, item.ID); err != nil {
		t.Fatalf("adopt interrupted operation: %v", err)
	}
	if got, want := strings.Join(spy.events, ","), "observe,revalidate"; got != want {
		t.Fatalf("adoption authority effect order=%q, want %q", got, want)
	}
}

func TestRecoveryPermanentCleanupKeyLossMatrix(t *testing.T) {
	TestWorkerPermanentCleanupKeyReconciliationMarksOnlyCurrentPostArmWork(t)
	TestWorkerPermanentCleanupKeyReconciliationLeavesIneligibleWorkUnchanged(t)
	TestWorkerReconcilePermanentCleanupKeyLossClosesOnlyCurrentPostArmWorkWithoutSideEffects(t)
	TestWorkerReconcilePermanentCleanupKeyLossPreservesTakenOverAttempt(t)
	TestWorkerReconcilePermanentCleanupKeyLossHonorsScanLimit(t)
}

func TestRecoveryLocatorRaceTakeoverOneWinner(t *testing.T) {
	TestWorkerConcurrentAdoptersProduceOneCheckpoint(t)
}

func testRecoveryAdoptionRejectsDeleteWithoutConsumedAuthorityBeforeIO(t *testing.T) {
	fixture := newExactMirrorOrdinaryExecutionFixture(t)
	db := fixture.serviceFixture.db
	const callbackName = "test:restart-adoption-delete-before-authority"
	injected := false
	if err := db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Table != (model.BackupAssetRecoveryCheckpoint{}).TableName() {
			return
		}
		checkpoint, ok := tx.Statement.Dest.(*model.BackupAssetRecoveryCheckpoint)
		if !ok || checkpoint.Phase != string(CheckpointPhaseDeleteAuthorityRequired) {
			return
		}
		injected = true
		_ = tx.AddError(errors.New("simulated crash before delete authority checkpoint"))
	}); err != nil {
		t.Fatalf("register pre-authority crash: %v", err)
	}
	source := newRecoveryRepositoryContractSource(t, db, fixture.jobID)
	executeErr := fixture.coordinator.ExecuteClaim(context.Background(), fixture.claim, source, "")
	if err := db.Callback().Create().Remove(callbackName); err != nil {
		t.Fatalf("remove pre-authority crash: %v", err)
	}
	if !injected || !errors.Is(executeErr, ErrRecoveryWorkerUnavailable) {
		t.Fatalf("pre-authority interruption injected=%t error=%v, want worker unavailable", injected, executeErr)
	}

	var checkpoints []model.BackupAssetRecoveryCheckpoint
	if err := db.Where("job_id = ?", fixture.jobID).Order("sequence ASC").Find(&checkpoints).Error; err != nil {
		t.Fatal(err)
	}
	if len(checkpoints) == 0 {
		t.Fatal("pre-authority fixture has no completed operation history")
	}
	for _, checkpoint := range checkpoints {
		if checkpoint.Phase != string(CheckpointPhaseOperation) {
			t.Fatalf("pre-authority checkpoint history contains phase %q: %+v", checkpoint.Phase, checkpoints)
		}
	}
	var deleteItem model.BackupAssetRecoveryJobItem
	if err := db.Where(
		"job_id = ? AND operation_kind = ? AND outcome = ''", fixture.jobID, RecoveryOperationDelete,
	).Order("ordinal ASC").Take(&deleteItem).Error; err != nil {
		t.Fatalf("load pending pre-authority delete item: %v", err)
	}

	providerSource := &recoveryExecutionSourceFake{}
	resolver := &recoveryAdoptionSourceResolverFake{source: providerSource}
	coordinator := newRecoveryWorkerCoordinatorWithSourceResolver(t, fixture.serviceFixture, resolver)
	target := &recoveryRestartTargetFake{}
	coordinator.target = target

	if _, err := coordinator.AdoptInterruptedOperation(
		context.Background(), fixture.claim, deleteItem.ID,
	); !errors.Is(err, ErrRecoveryWorkerFenceLost) {
		t.Fatalf("pre-authority delete adoption error=%v, want ErrRecoveryWorkerFenceLost", err)
	}
	if len(resolver.refs) != 0 || providerSource.revalidates != 0 || providerSource.closes != 0 || target.calls != 0 {
		t.Fatalf("pre-authority delete reached source/target I/O refs/revalidate/close/verify=%d/%d/%d/%d",
			len(resolver.refs), providerSource.revalidates, providerSource.closes, target.calls)
	}
}

func testRecoveryAdoptionRejectsUnknownItemIdentity(t *testing.T) {
	fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptExecute)
	if _, err := fixture.service.Authorize(context.Background(), fixture.request); err != nil {
		t.Fatalf("execute recovery fixture: %v", err)
	}
	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	target := coordinator.target.(*recoveryRestartTargetFake)
	claim, found, err := coordinator.ClaimNext(context.Background(), "recovery-worker-before-unknown-item")
	if err != nil || !found {
		t.Fatalf("claim recovery job: found=%t err=%v", found, err)
	}
	if _, err := coordinator.PrepareFirstWrite(context.Background(), claim); err != nil {
		t.Fatalf("prepare durable sequence-zero write barrier: %v", err)
	}
	fixture.now = claim.LeaseExpiresAt.Add(time.Second)
	takeover, found, err := coordinator.TakeoverExpired(context.Background(), "recovery-worker-after-unknown-item")
	if err != nil || !found {
		t.Fatalf("take over interrupted recovery job: found=%t err=%v", found, err)
	}

	unknownItemID := strings.Repeat("f", 32)
	var collision int64
	if err := fixture.db.Model(&model.BackupAssetRecoveryJobItem{}).Where("id = ?", unknownItemID).Count(&collision).Error; err != nil {
		t.Fatal(err)
	}
	if collision != 0 {
		t.Fatalf("unknown item fixture collided with %q", unknownItemID)
	}
	if _, err := coordinator.AdoptInterruptedOperation(context.Background(), takeover, unknownItemID); !errors.Is(err, ErrRecoveryWorkerFenceLost) {
		t.Fatalf("unknown durable item adoption error=%v, want ErrRecoveryWorkerFenceLost", err)
	}
	if target.calls != 0 {
		t.Fatalf("unknown durable item reached target Verify calls=%d", target.calls)
	}
	var sequenceOne int64
	if err := fixture.db.Model(&model.BackupAssetRecoveryCheckpoint{}).
		Where("job_id = ? AND sequence = ?", takeover.JobID, 1).Count(&sequenceOne).Error; err != nil {
		t.Fatal(err)
	}
	if sequenceOne != 0 {
		t.Fatalf("unknown durable item appended sequence-1 checkpoints=%d", sequenceOne)
	}
}

func testRecoveryAdoptionRejectsDurableDriftBetweenBoundaries(t *testing.T) {
	fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptExecute)
	if _, err := fixture.service.Authorize(context.Background(), fixture.request); err != nil {
		t.Fatalf("execute recovery fixture: %v", err)
	}
	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	claim, found, err := coordinator.ClaimNext(context.Background(), "recovery-worker-before-durable-drift")
	if err != nil || !found {
		t.Fatalf("claim recovery job: found=%t err=%v", found, err)
	}
	if _, err := coordinator.PrepareFirstWrite(context.Background(), claim); err != nil {
		t.Fatalf("prepare durable sequence-zero write barrier: %v", err)
	}
	var plan model.BackupAssetRecoveryPlan
	if err := fixture.db.Where("id = ?", fixture.request.PlanID).Take(&plan).Error; err != nil {
		t.Fatal(err)
	}
	var job model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", claim.JobID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	var item model.BackupAssetRecoveryJobItem
	if err := fixture.db.Where("job_id = ?", claim.JobID).Order("ordinal ASC").Take(&item).Error; err != nil {
		t.Fatal(err)
	}

	fixture.now = claim.LeaseExpiresAt.Add(time.Second)
	takeover, found, err := coordinator.TakeoverExpired(context.Background(), "recovery-worker-after-durable-drift")
	if err != nil || !found {
		t.Fatalf("take over interrupted recovery job: found=%t err=%v", found, err)
	}
	target := coordinator.target.(*recoveryRestartTargetFake)
	material, err := coordinator.workspaceKeys.ByVersion(
		context.Background(), backupasset.KeyDomainRecoveryCleanupOwnership, item.TargetLocatorKeyVersion,
	)
	if err != nil {
		t.Fatalf("load workspace marker key: %v", err)
	}
	defer clear(material.Key)
	mutatedOwner := "recovery-workspace-owner-after-verify"
	mutatedFence := job.WorkspaceFence + 1
	mutatedMarker := recoveryWorkspaceMarkerBindingDigest(
		material, job.ID, job.TargetRootID, plan.RootRevision, job.EncryptedWorkspaceRelativeLocator,
		RecoveryWorkerClaim{WorkerID: mutatedOwner, AttemptFence: mutatedFence},
	)
	target.beforeReturn = func() error {
		return fixture.db.Model(&model.BackupAssetRecoveryJob{}).Where("id = ?", job.ID).
			Updates(map[string]any{
				"workspace_owner": mutatedOwner, "workspace_fence": mutatedFence,
				"workspace_marker_binding_digest": mutatedMarker,
				"updated_at":                      fixture.now.Add(time.Second),
			}).Error
	}

	if _, err := coordinator.AdoptInterruptedOperation(context.Background(), takeover, item.ID); !errors.Is(err, ErrRecoveryWorkerFenceLost) {
		t.Fatalf("between-boundary durable drift error=%v, want ErrRecoveryWorkerFenceLost", err)
	}
	if target.calls != 1 {
		t.Fatalf("between-boundary durable drift target calls=%d, want one", target.calls)
	}
	var afterItem model.BackupAssetRecoveryJobItem
	if err := fixture.db.Where("id = ?", item.ID).Take(&afterItem).Error; err != nil {
		t.Fatal(err)
	}
	if afterItem.Outcome != "" || afterItem.FailureCategory != "" {
		t.Fatalf("between-boundary durable drift projected item=%+v", afterItem)
	}
	var afterJob model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", job.ID).Take(&afterJob).Error; err != nil {
		t.Fatal(err)
	}
	if afterJob.TargetChainRevision != job.TargetChainRevision {
		t.Fatalf("between-boundary durable drift advanced target chain: before=%q after=%q",
			job.TargetChainRevision, afterJob.TargetChainRevision)
	}
	var sequenceOne int64
	if err := fixture.db.Model(&model.BackupAssetRecoveryCheckpoint{}).
		Where("job_id = ? AND sequence = ?", job.ID, 1).Count(&sequenceOne).Error; err != nil {
		t.Fatal(err)
	}
	if sequenceOne != 0 {
		t.Fatalf("between-boundary durable drift appended sequence-1 checkpoints=%d", sequenceOne)
	}
}

type recoveryWorkerWorkspaceKeySource struct{}

func (recoveryWorkerWorkspaceKeySource) Active(
	_ context.Context,
	domain backupasset.KeyDomain,
) (backupasset.DomainKeyMaterial, error) {
	if domain != backupasset.KeyDomainRecoveryCleanupOwnership {
		return backupasset.DomainKeyMaterial{}, fmt.Errorf("unexpected worker key domain %q", domain)
	}
	return backupasset.DomainKeyMaterial{
		Domain: domain, State: backupasset.DomainKeyActive, Version: 1,
		Key: []byte(strings.Repeat("k", 32)),
	}, nil
}

func (recoveryWorkerWorkspaceKeySource) ByVersion(
	ctx context.Context,
	domain backupasset.KeyDomain,
	version int,
) (backupasset.DomainKeyMaterial, error) {
	material, err := (recoveryWorkerWorkspaceKeySource{}).Active(ctx, domain)
	if err != nil || version != material.Version {
		return backupasset.DomainKeyMaterial{}, backupasset.ErrKeyUnavailable
	}
	return material, nil
}

func TestWorkerClaimBindsQueuedJobToOneCurrentOwner(t *testing.T) {
	fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptExecute)
	executed, err := fixture.service.Authorize(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("execute recovery fixture: %v", err)
	}
	coordinator := newRecoveryWorkerCoordinator(t, fixture)

	claim, found, err := coordinator.ClaimNext(context.Background(), "recovery-worker-a")
	if err != nil || !found {
		t.Fatalf("claim queued recovery job: found=%t err=%v", found, err)
	}
	if claim.JobID != executed.JobID || claim.AttemptID != executed.AttemptID ||
		claim.AttemptFence != 1 || claim.NodeFence != executed.NodeLeaseFence ||
		claim.WorkerID != "recovery-worker-a" || claim.TransitionRevision != 2 {
		t.Fatalf("unexpected recovery claim: %+v", claim)
	}

	var job model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", claim.JobID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	if job.State != string(JobStateRunning) || job.TransitionRevision != claim.TransitionRevision {
		t.Fatalf("claimed job state=%q revision=%d", job.State, job.TransitionRevision)
	}
	var attempt model.BackupAssetRecoveryAttempt
	if err := fixture.db.Where("id = ?", claim.AttemptID).Take(&attempt).Error; err != nil {
		t.Fatal(err)
	}
	if attempt.State != string(AttemptStateRunning) || attempt.OwnerID != claim.WorkerID ||
		attempt.Fence != claim.AttemptFence || attempt.LeaseExpiresAt == nil {
		t.Fatalf("claimed attempt mismatch: %+v", attempt)
	}
	var nodeLease model.BackupAssetRecoveryNodeLease
	if err := fixture.db.Where("id = ?", executed.NodeLeaseID).Take(&nodeLease).Error; err != nil {
		t.Fatal(err)
	}
	if nodeLease.State != "active" || nodeLease.OwnerID != claim.WorkerID ||
		nodeLease.AttemptID == nil || *nodeLease.AttemptID != claim.AttemptID ||
		nodeLease.Fence != claim.NodeFence {
		t.Fatalf("claimed node lease mismatch: %+v", nodeLease)
	}
	var sourceLease model.RecoveryPointLease
	if err := fixture.db.Where("id = ?", claim.SourceFence.LeaseID).Take(&sourceLease).Error; err != nil {
		t.Fatal(err)
	}
	if sourceLease.Status != "active" || sourceLease.OwnerID != claim.JobID ||
		sourceLease.AttemptID != claim.AttemptID || sourceLease.FenceToken != claim.SourceFence.FenceToken {
		t.Fatalf("claimed source lease mismatch: %+v", sourceLease)
	}

	if second, secondFound, secondErr := coordinator.ClaimNext(context.Background(), "recovery-worker-b"); secondErr != nil || secondFound || second.JobID != "" {
		t.Fatalf("second worker acquired claimed job: claim=%+v found=%t err=%v", second, secondFound, secondErr)
	}
}

func TestRecoveryHeartbeatLosesAllAuthorityWhenSourceFenceIsGone(t *testing.T) {
	fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptExecute)
	if _, err := fixture.service.Authorize(context.Background(), fixture.request); err != nil {
		t.Fatalf("execute recovery fixture: %v", err)
	}
	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	claim, found, err := coordinator.ClaimNext(context.Background(), "recovery-worker-a")
	if err != nil || !found {
		t.Fatalf("claim queued recovery job: found=%t err=%v", found, err)
	}

	releasedAt := fixture.now
	if err := fixture.db.Model(&model.RecoveryPointLease{}).Where("id = ?", claim.SourceFence.LeaseID).
		Updates(map[string]any{"status": "released", "released_at": releasedAt, "updated_at": releasedAt}).Error; err != nil {
		t.Fatal(err)
	}
	fixture.now = fixture.now.Add(time.Minute)
	if _, err := coordinator.Heartbeat(context.Background(), claim); !errors.Is(err, ErrRecoveryWorkerFenceLost) {
		t.Fatalf("heartbeat after source-fence loss error=%v", err)
	}
}

func TestRecoveryHeartbeatAtomicallyPropagatesSourceNodeAndAttemptDeadline(t *testing.T) {
	fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptExecute)
	if _, err := fixture.service.Authorize(context.Background(), fixture.request); err != nil {
		t.Fatalf("execute recovery fixture: %v", err)
	}
	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	claim, found, err := coordinator.ClaimNext(context.Background(), "recovery-heartbeat-worker")
	if err != nil || !found {
		t.Fatalf("claim queued recovery job: found=%t err=%v", found, err)
	}
	fixture.now = fixture.now.Add(time.Minute)
	renewed, err := coordinator.Heartbeat(context.Background(), claim)
	if err != nil {
		t.Fatal(err)
	}
	wantExpiry := fixture.now.Add(10 * time.Minute)
	var source model.RecoveryPointLease
	if err := fixture.db.Where("id = ?", claim.SourceFence.LeaseID).Take(&source).Error; err != nil {
		t.Fatal(err)
	}
	claimValue := reflect.ValueOf(claim)
	absoluteDeadline := claimValue.FieldByName("AbsoluteDeadline")
	if !absoluteDeadline.IsValid() || !absoluteDeadline.Interface().(time.Time).Equal(source.AbsoluteDeadline) {
		t.Fatalf("claim omits frozen source absolute deadline %s: %+v", source.AbsoluteDeadline, claim)
	}
	renewedDeadline := reflect.ValueOf(renewed).FieldByName("AbsoluteDeadline")
	if !renewedDeadline.IsValid() || !renewedDeadline.Interface().(time.Time).Equal(source.AbsoluteDeadline) {
		t.Fatalf("heartbeat changed frozen source absolute deadline %s: %+v", source.AbsoluteDeadline, renewed)
	}
	var node model.BackupAssetRecoveryNodeLease
	if err := fixture.db.Where("id = ?", claim.NodeLeaseID).Take(&node).Error; err != nil {
		t.Fatal(err)
	}
	var attempt model.BackupAssetRecoveryAttempt
	if err := fixture.db.Where("id = ?", claim.AttemptID).Take(&attempt).Error; err != nil {
		t.Fatal(err)
	}
	if !renewed.LeaseExpiresAt.Equal(wantExpiry) || !source.LeaseExpiresAt.Equal(wantExpiry) ||
		!source.LastHeartbeatAt.Equal(fixture.now) || !node.LeaseExpiresAt.Equal(wantExpiry) ||
		attempt.LeaseExpiresAt == nil || !attempt.LeaseExpiresAt.Equal(wantExpiry) ||
		attempt.HeartbeatAt == nil || !attempt.HeartbeatAt.Equal(fixture.now) {
		t.Fatalf("renewed=%s source=%s/%s node=%s attempt=%v/%v, want %s/%s",
			renewed.LeaseExpiresAt, source.LeaseExpiresAt, source.LastHeartbeatAt, node.LeaseExpiresAt,
			attempt.LeaseExpiresAt, attempt.HeartbeatAt, wantExpiry, fixture.now)
	}
}

func TestRecoveryHeartbeatTransactionRollsBackSourceRenewalWhenNodeUpdateFails(t *testing.T) {
	fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptExecute)
	if _, err := fixture.service.Authorize(context.Background(), fixture.request); err != nil {
		t.Fatalf("execute recovery fixture: %v", err)
	}
	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	claim, found, err := coordinator.ClaimNext(context.Background(), "recovery-heartbeat-rollback-worker")
	if err != nil || !found {
		t.Fatalf("claim queued recovery job: found=%t err=%v", found, err)
	}

	var sourceBefore model.RecoveryPointLease
	if err := fixture.db.Where("id = ?", claim.SourceFence.LeaseID).Take(&sourceBefore).Error; err != nil {
		t.Fatal(err)
	}
	var nodeBefore model.BackupAssetRecoveryNodeLease
	if err := fixture.db.Where("id = ?", claim.NodeLeaseID).Take(&nodeBefore).Error; err != nil {
		t.Fatal(err)
	}
	var attemptBefore model.BackupAssetRecoveryAttempt
	if err := fixture.db.Where("id = ?", claim.AttemptID).Take(&attemptBefore).Error; err != nil {
		t.Fatal(err)
	}

	callbackName := "recovery:heartbeat-node-update-failure:" + t.Name()
	injected := errors.New("injected recovery heartbeat node update failure")
	if err := fixture.db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == (model.BackupAssetRecoveryNodeLease{}).TableName() {
			_ = tx.AddError(injected)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fixture.db.Callback().Update().Remove(callbackName) })

	fixture.now = fixture.now.Add(time.Minute)
	if _, err := coordinator.Heartbeat(context.Background(), claim); !errors.Is(err, ErrRecoveryWorkerUnavailable) {
		t.Fatalf("heartbeat injected node failure error=%v, want unavailable", err)
	}

	var sourceAfter model.RecoveryPointLease
	if err := fixture.db.Where("id = ?", claim.SourceFence.LeaseID).Take(&sourceAfter).Error; err != nil {
		t.Fatal(err)
	}
	var nodeAfter model.BackupAssetRecoveryNodeLease
	if err := fixture.db.Where("id = ?", claim.NodeLeaseID).Take(&nodeAfter).Error; err != nil {
		t.Fatal(err)
	}
	var attemptAfter model.BackupAssetRecoveryAttempt
	if err := fixture.db.Where("id = ?", claim.AttemptID).Take(&attemptAfter).Error; err != nil {
		t.Fatal(err)
	}
	if !sourceAfter.LeaseExpiresAt.Equal(sourceBefore.LeaseExpiresAt) ||
		!sourceAfter.LastHeartbeatAt.Equal(sourceBefore.LastHeartbeatAt) ||
		!sourceAfter.AbsoluteDeadline.Equal(sourceBefore.AbsoluteDeadline) ||
		!nodeAfter.LeaseExpiresAt.Equal(nodeBefore.LeaseExpiresAt) ||
		attemptAfter.LeaseExpiresAt == nil || attemptBefore.LeaseExpiresAt == nil ||
		!attemptAfter.LeaseExpiresAt.Equal(*attemptBefore.LeaseExpiresAt) ||
		attemptAfter.HeartbeatAt == nil || attemptBefore.HeartbeatAt == nil ||
		!attemptAfter.HeartbeatAt.Equal(*attemptBefore.HeartbeatAt) {
		t.Fatalf("heartbeat transaction partially committed: source=%+v/%+v node=%+v/%+v attempt=%+v/%+v",
			sourceBefore, sourceAfter, nodeBefore, nodeAfter, attemptBefore, attemptAfter)
	}
}

func TestWorkerPermanentCleanupKeyReconciliationMarksOnlyCurrentPostArmWork(t *testing.T) {
	fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptExecute)
	executed, err := fixture.service.Authorize(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("execute recovery fixture: %v", err)
	}
	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	claim, found, err := coordinator.ClaimNext(context.Background(), "recovery-worker-cleanup-key")
	if err != nil || !found {
		t.Fatalf("claim recovery job: found=%t err=%v", found, err)
	}
	if _, err := coordinator.PrepareFirstWrite(context.Background(), claim); err != nil {
		t.Fatalf("arm current recovery attempt: %v", err)
	}

	reconciler, ok := any(coordinator).(recoveryPermanentCleanupKeyReconciler)
	if !ok {
		t.Fatal("WorkerCoordinator does not expose permanent cleanup-key reconciliation")
	}
	beforeJob := loadRecoveryCleanupKeyJobSnapshot(t, fixture.db, executed.JobID)
	beforeAttempt := loadRecoveryCleanupKeyAttemptSnapshot(t, fixture.db, claim.AttemptID)
	beforeItem := loadRecoveryCleanupKeyItemSnapshot(t, fixture.db, executed.JobID)
	beforeCheckpoints := countRecoveryCleanupKeyCheckpoints(t, fixture.db, executed.JobID)
	target := &recoveryRestartTargetFake{observation: recoveryRestartObservationForTest()}
	keys := &recoveryCleanupKeyAccessSpy{}
	coordinator.target = target
	coordinator.workspaceKeys = keys

	selectedEncryptedLocator := false
	const callbackName = "recovery:cleanup_key_db_only"
	if err := fixture.db.Callback().Query().After("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if strings.Contains(strings.ToLower(tx.Statement.SQL.String()), "encrypted_workspace_relative_locator") {
			selectedEncryptedLocator = true
		}
	}); err != nil {
		t.Fatalf("register cleanup-key DB-only observer: %v", err)
	}
	reconciled, err := reconciler.ReconcilePermanentCleanupKeyLoss(context.Background())
	if err := fixture.db.Callback().Query().Remove(callbackName); err != nil {
		t.Fatalf("remove cleanup-key DB-only observer: %v", err)
	}
	if err != nil {
		t.Fatalf("reconcile permanent cleanup-key failure: %v", err)
	}
	if reconciled != 1 {
		t.Fatalf("reconciled=%d, want one current post-arm attempt", reconciled)
	}

	afterJob := loadRecoveryCleanupKeyJobSnapshot(t, fixture.db, executed.JobID)
	afterAttempt := loadRecoveryCleanupKeyAttemptSnapshot(t, fixture.db, claim.AttemptID)
	afterItem := loadRecoveryCleanupKeyItemSnapshot(t, fixture.db, executed.JobID)
	if afterJob.State != string(JobStateNeedsAttention) ||
		afterJob.FailureCategory != "cleanup_key_unavailable" ||
		afterJob.WorkspacePhase != string(WorkspacePhaseCleanupDue) ||
		afterJob.TransitionRevision != beforeJob.TransitionRevision+1 {
		t.Fatalf("cleanup-key reconciliation job projection=%+v, before=%+v", afterJob, beforeJob)
	}
	if afterJob.TargetChainRevision != beforeJob.TargetChainRevision {
		t.Fatalf("cleanup-key reconciliation advanced target chain: before=%q after=%q", beforeJob.TargetChainRevision, afterJob.TargetChainRevision)
	}
	if afterAttempt.State != string(AttemptStateFailed) || afterAttempt.ClosedAt == nil ||
		!afterAttempt.ClosedAt.Equal(fixture.now) || !afterAttempt.MutationArmed ||
		afterAttempt.OwnerID != beforeAttempt.OwnerID || afterAttempt.Fence != beforeAttempt.Fence {
		t.Fatalf("cleanup-key reconciliation attempt projection=%+v, before=%+v", afterAttempt, beforeAttempt)
	}
	if !reflect.DeepEqual(afterItem, beforeItem) {
		t.Fatalf("cleanup-key reconciliation projected item success/skip: before=%+v after=%+v", beforeItem, afterItem)
	}
	if after := countRecoveryCleanupKeyCheckpoints(t, fixture.db, executed.JobID); after != beforeCheckpoints {
		t.Fatalf("cleanup-key reconciliation appended checkpoint: before=%d after=%d", beforeCheckpoints, after)
	}
	if target.calls != 0 || keys.calls != 0 || selectedEncryptedLocator {
		t.Fatalf("cleanup-key reconciliation crossed DB-only boundary: target_calls=%d key_calls=%d selected_encrypted_locator=%t", target.calls, keys.calls, selectedEncryptedLocator)
	}

	retryJob := afterJob
	retryAttempt := afterAttempt
	if retried, retryErr := reconciler.ReconcilePermanentCleanupKeyLoss(context.Background()); retryErr != nil || retried != 0 {
		t.Fatalf("idempotent cleanup-key retry: reconciled=%d err=%v", retried, retryErr)
	}
	if got := loadRecoveryCleanupKeyJobSnapshot(t, fixture.db, executed.JobID); !reflect.DeepEqual(got, retryJob) {
		t.Fatalf("cleanup-key retry rewrote terminal job: before=%+v after=%+v", retryJob, got)
	}
	if got := loadRecoveryCleanupKeyAttemptSnapshot(t, fixture.db, claim.AttemptID); !reflect.DeepEqual(got, retryAttempt) {
		t.Fatalf("cleanup-key retry rewrote closed attempt: before=%+v after=%+v", retryAttempt, got)
	}
}

func TestWorkerPermanentCleanupKeyReconciliationLeavesIneligibleWorkUnchanged(t *testing.T) {
	testCases := []struct {
		name  string
		setup func(*testing.T, *authorizationReceiptServiceFixture, *WorkerCoordinator, RecoveryWorkerClaim)
	}{
		{name: "pre_arm"},
		{
			name: "expired",
			setup: func(t *testing.T, fixture *authorizationReceiptServiceFixture, coordinator *WorkerCoordinator, claim RecoveryWorkerClaim) {
				t.Helper()
				if _, err := coordinator.PrepareFirstWrite(context.Background(), claim); err != nil {
					t.Fatalf("arm expiring recovery attempt: %v", err)
				}
				fixture.now = claim.LeaseExpiresAt.Add(time.Nanosecond)
			},
		},
		{
			name: "source_fence_noncurrent",
			setup: func(t *testing.T, fixture *authorizationReceiptServiceFixture, coordinator *WorkerCoordinator, claim RecoveryWorkerClaim) {
				t.Helper()
				if _, err := coordinator.PrepareFirstWrite(context.Background(), claim); err != nil {
					t.Fatalf("arm source-stale recovery attempt: %v", err)
				}
				if err := fixture.db.Table("recovery_point_leases").Where("id = ?", claim.SourceFence.LeaseID).
					Updates(map[string]any{"status": backupasset.LeaseReleased, "released_at": fixture.now, "updated_at": fixture.now}).Error; err != nil {
					t.Fatalf("make source fence noncurrent: %v", err)
				}
			},
		},
		{
			name: "terminal",
			setup: func(t *testing.T, _ *authorizationReceiptServiceFixture, coordinator *WorkerCoordinator, claim RecoveryWorkerClaim) {
				t.Helper()
				if _, err := coordinator.PrepareFirstWrite(context.Background(), claim); err != nil {
					t.Fatalf("arm terminal recovery attempt: %v", err)
				}
				if err := coordinator.CancelJob(context.Background(), claim.JobID); err != nil {
					t.Fatalf("terminalize recovery attempt: %v", err)
				}
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptExecute)
			executed, err := fixture.service.Authorize(context.Background(), fixture.request)
			if err != nil {
				t.Fatalf("execute recovery fixture: %v", err)
			}
			coordinator := newRecoveryWorkerCoordinator(t, fixture)
			claim, found, err := coordinator.ClaimNext(context.Background(), "recovery-worker-cleanup-key-negative")
			if err != nil || !found {
				t.Fatalf("claim recovery job: found=%t err=%v", found, err)
			}
			if testCase.setup != nil {
				testCase.setup(t, fixture, coordinator, claim)
			}
			reconciler, ok := any(coordinator).(recoveryPermanentCleanupKeyReconciler)
			if !ok {
				t.Fatal("WorkerCoordinator does not expose permanent cleanup-key reconciliation")
			}
			beforeJob := loadRecoveryCleanupKeyJobSnapshot(t, fixture.db, executed.JobID)
			beforeAttempt := loadRecoveryCleanupKeyAttemptSnapshot(t, fixture.db, claim.AttemptID)
			beforeCheckpoints := countRecoveryCleanupKeyCheckpoints(t, fixture.db, executed.JobID)
			if reconciled, err := reconciler.ReconcilePermanentCleanupKeyLoss(context.Background()); err != nil || reconciled != 0 {
				t.Fatalf("ineligible cleanup-key reconciliation: reconciled=%d err=%v", reconciled, err)
			}
			if got := loadRecoveryCleanupKeyJobSnapshot(t, fixture.db, executed.JobID); !reflect.DeepEqual(got, beforeJob) {
				t.Fatalf("ineligible cleanup-key reconciliation changed job: before=%+v after=%+v", beforeJob, got)
			}
			if got := loadRecoveryCleanupKeyAttemptSnapshot(t, fixture.db, claim.AttemptID); !reflect.DeepEqual(got, beforeAttempt) {
				t.Fatalf("ineligible cleanup-key reconciliation changed attempt: before=%+v after=%+v", beforeAttempt, got)
			}
			if got := countRecoveryCleanupKeyCheckpoints(t, fixture.db, executed.JobID); got != beforeCheckpoints {
				t.Fatalf("ineligible cleanup-key reconciliation appended checkpoint: before=%d after=%d", beforeCheckpoints, got)
			}
		})
	}
}

func TestWorkerPrepareFirstWriteCommitsLatchReservationAndFenceBoundPermit(t *testing.T) {
	fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptExecute)
	if _, err := fixture.service.Authorize(context.Background(), fixture.request); err != nil {
		t.Fatalf("execute recovery fixture: %v", err)
	}
	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	claim, found, err := coordinator.ClaimNext(context.Background(), "recovery-worker-a")
	if err != nil || !found {
		t.Fatalf("claim queued recovery job: found=%t err=%v", found, err)
	}

	preparer, ok := any(coordinator).(recoveryFirstWritePreparer)
	if !ok {
		t.Fatal("worker coordinator does not expose the fenced first-write boundary")
	}
	permit, err := preparer.PrepareFirstWrite(context.Background(), claim)
	if err != nil {
		t.Fatalf("prepare fenced first write: %v", err)
	}
	if err := permit.ValidateAt(fixture.now); err != nil {
		t.Fatalf("issued first-write permit is not current: %v", err)
	}
	if permit.permit.UseLatchID != RecoverySchemaUseLatchID || permit.permit.JobID != claim.JobID ||
		permit.permit.AttemptID != claim.AttemptID || permit.permit.NodeLeaseID != claim.NodeLeaseID ||
		permit.permit.AttemptFence != claim.AttemptFence || permit.permit.NodeFence != claim.NodeFence {
		t.Fatalf("first-write permit does not bind the claimed fences: %+v", permit.permit)
	}

	var latch model.BackupAssetRecoveryEvidence
	if err := fixture.db.Where("id = ? AND kind = ?", strings.Repeat("0", 30)+"69", RecoverySchemaUseLatchID).
		Take(&latch).Error; err != nil {
		t.Fatalf("load committed schema-use latch: %v", err)
	}
	if latch.JobID != nil || latch.AttemptID != nil || latch.NodeLeaseID != nil {
		t.Fatalf("schema-use latch retained job-specific fields: %+v", latch)
	}

	var job model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", claim.JobID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	if job.WorkspacePhase != string(WorkspacePhaseMarkerCreated) || job.EncryptedWorkspaceRelativeLocator == "" ||
		job.WorkspaceMarkerBindingDigest == "" || job.WorkspaceOwner != claim.WorkerID ||
		job.WorkspaceFence != claim.AttemptFence || job.PlaintextDeadline == nil ||
		!job.PlaintextDeadline.After(fixture.now) {
		t.Fatalf("first-write marker state is incomplete: %+v", job)
	}
	var storedWorkspaceLocator string
	if err := fixture.db.Table((model.BackupAssetRecoveryJob{}).TableName()).
		Select("encrypted_workspace_relative_locator").Where("id = ?", claim.JobID).
		Scan(&storedWorkspaceLocator).Error; err != nil {
		t.Fatal(err)
	}
	if !secure.IsEncrypted(storedWorkspaceLocator) || storedWorkspaceLocator == job.EncryptedWorkspaceRelativeLocator {
		t.Fatalf("workspace locator was not encrypted at rest: %q", storedWorkspaceLocator)
	}

	var attempt model.BackupAssetRecoveryAttempt
	if err := fixture.db.Where("id = ?", claim.AttemptID).Take(&attempt).Error; err != nil {
		t.Fatal(err)
	}
	if !attempt.MutationArmed || attempt.State != string(AttemptStateRunning) {
		t.Fatalf("first-write boundary did not durably arm the current attempt: %+v", attempt)
	}

	var checkpoint model.BackupAssetRecoveryCheckpoint
	if err := fixture.db.Where("job_id = ? AND sequence = ?", claim.JobID, 0).Take(&checkpoint).Error; err != nil {
		t.Fatalf("load durable workspace checkpoint: %v", err)
	}
	if checkpoint.AttemptID != claim.AttemptID || checkpoint.Phase != string(CheckpointPhaseWorkspaceReserved) {
		t.Fatalf("workspace checkpoint is not bound to the current attempt: %+v", checkpoint)
	}

	if err := fixture.db.Model(&model.BackupAssetRecoveryNodeLease{}).
		Where("id = ?", claim.NodeLeaseID).
		Updates(map[string]any{"state": "released", "released_at": fixture.now, "updated_at": fixture.now}).Error; err != nil {
		t.Fatal(err)
	}
	if err := permit.ValidateAt(fixture.now); !errorsIsTargetPermit(err) {
		t.Fatalf("permit survived current node-fence loss: %v", err)
	}
}

func TestRecoveryReviewF6LatchBeforeTargetMutation(t *testing.T) {
	testCases := []struct {
		name   string
		revoke func(*testing.T, *authorizationReceiptServiceFixture, RecoveryWorkerClaim)
	}{
		{name: "current authority"},
		{
			name: "permanent use latch loss",
			revoke: func(t *testing.T, fixture *authorizationReceiptServiceFixture, _ RecoveryWorkerClaim) {
				t.Helper()
				result := fixture.db.Where("id = ?", recoverySchemaUseLatchRowID).
					Delete(&model.BackupAssetRecoveryEvidence{})
				if result.Error != nil || result.RowsAffected != 1 {
					t.Fatalf("remove schema-use latch: rows=%d err=%v", result.RowsAffected, result.Error)
				}
			},
		},
		{
			name: "current job loss",
			revoke: func(t *testing.T, fixture *authorizationReceiptServiceFixture, claim RecoveryWorkerClaim) {
				t.Helper()
				result := fixture.db.Where("id = ?", claim.JobID).Delete(&model.BackupAssetRecoveryJob{})
				if result.Error != nil || result.RowsAffected != 1 {
					t.Fatalf("remove current recovery job: rows=%d err=%v", result.RowsAffected, result.Error)
				}
			},
		},
		{
			name: "attempt fence loss",
			revoke: func(t *testing.T, fixture *authorizationReceiptServiceFixture, claim RecoveryWorkerClaim) {
				t.Helper()
				result := fixture.db.Model(&model.BackupAssetRecoveryAttempt{}).Where("id = ?", claim.AttemptID).
					Update("fence", claim.AttemptFence+1)
				if result.Error != nil || result.RowsAffected != 1 {
					t.Fatalf("replace current attempt fence: rows=%d err=%v", result.RowsAffected, result.Error)
				}
			},
		},
		{
			name: "node lease fence loss",
			revoke: func(t *testing.T, fixture *authorizationReceiptServiceFixture, claim RecoveryWorkerClaim) {
				t.Helper()
				result := fixture.db.Model(&model.BackupAssetRecoveryNodeLease{}).Where("id = ?", claim.NodeLeaseID).
					Update("fence", claim.NodeFence+1)
				if result.Error != nil || result.RowsAffected != 1 {
					t.Fatalf("replace current node-lease fence: rows=%d err=%v", result.RowsAffected, result.Error)
				}
			},
		},
		{
			name: "source fence loss",
			revoke: func(t *testing.T, fixture *authorizationReceiptServiceFixture, claim RecoveryWorkerClaim) {
				t.Helper()
				replacement := strings.Repeat("0", len(claim.SourceFence.FenceToken))
				if replacement == claim.SourceFence.FenceToken {
					replacement = strings.Repeat("1", len(claim.SourceFence.FenceToken))
				}
				result := fixture.db.Model(&model.RecoveryPointLease{}).Where("id = ?", claim.SourceFence.LeaseID).
					Update("fence_token", replacement)
				if result.Error != nil || result.RowsAffected != 1 {
					t.Fatalf("replace current source fence: rows=%d err=%v", result.RowsAffected, result.Error)
				}
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptExecute)
			if _, err := fixture.service.Authorize(context.Background(), fixture.request); err != nil {
				t.Fatalf("execute recovery fixture: %v", err)
			}
			coordinator := newRecoveryWorkerCoordinator(t, fixture)
			coordinator.target = nil
			claim, found, err := coordinator.ClaimNext(context.Background(), "f6-live-permit-worker")
			if err != nil || !found {
				t.Fatalf("claim queued recovery job: found=%t err=%v", found, err)
			}
			permit, err := coordinator.PrepareFirstWrite(context.Background(), claim)
			if err != nil {
				t.Fatalf("issue structurally valid target mutation permit: %v", err)
			}

			var job model.BackupAssetRecoveryJob
			if err := fixture.db.Where("id = ?", claim.JobID).Take(&job).Error; err != nil {
				t.Fatalf("load prepared recovery job: %v", err)
			}
			object := TargetObjectRef{
				RootID: job.TargetRootID, RootLocatorDigest: job.RootLocatorDigest,
				TargetPathDigest: permit.permit.TargetPathDigest, PrivateRelativeLocator: job.EncryptedWorkspaceRelativeLocator,
			}
			if err := permit.ValidateObjectAt(fixture.now, object); err != nil {
				t.Fatalf("prepared permit is not structurally valid and current: %v", err)
			}
			if testCase.revoke != nil {
				testCase.revoke(t, fixture, claim)
			}

			fake := &recoveryMutationAdmissionTargetFake{now: fixture.now}
			mutations := []struct {
				name string
				call func() error
			}{
				{
					name: "CreateOwnedJobDir",
					call: func() error {
						_, err := fake.CreateOwnedJobDir(context.Background(), permit, CreateOwnedJobDirRequest{
							Object: object, MarkerBindingDigest: job.WorkspaceMarkerBindingDigest,
						})
						return err
					},
				},
				{
					name: "CreateDirectory",
					call: func() error {
						return fake.CreateDirectory(context.Background(), permit, CreateTargetDirectoryRequest{Object: object, Mode: 0o700})
					},
				},
				{
					name: "WriteAtomic",
					call: func() error {
						_, err := fake.WriteAtomic(context.Background(), permit, TargetWriteAtomicRequest{
							Object: object, ExpectedBytes: 7, ExpectedDigest: strings.Repeat("a", sha256DigestLength),
							Content: strings.NewReader("payload"),
						})
						return err
					},
				},
			}
			for _, mutation := range mutations {
				err := mutation.call()
				if testCase.revoke == nil && err != nil {
					t.Fatalf("current authority rejected %s: %v", mutation.name, err)
				}
				if testCase.revoke != nil && !errors.Is(err, ErrInvalidTargetPermit) {
					t.Fatalf("revoked authority %s error=%v, want ErrInvalidTargetPermit", mutation.name, err)
				}
			}

			if testCase.revoke == nil {
				want := []string{"CreateOwnedJobDir", "CreateDirectory", "WriteAtomic"}
				if !reflect.DeepEqual(fake.calls, want) {
					t.Fatalf("current authority target mutation calls=%v, want %v", fake.calls, want)
				}
			} else if len(fake.calls) != 0 {
				t.Fatalf("revoked authority reached target mutations: calls=%v, want zero", fake.calls)
			}
		})
	}
}

func TestWorkerPrepareFirstWriteRetriesCurrentClaimIdempotently(t *testing.T) {
	fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptExecute)
	if _, err := fixture.service.Authorize(context.Background(), fixture.request); err != nil {
		t.Fatalf("execute recovery fixture: %v", err)
	}
	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	claim, found, err := coordinator.ClaimNext(context.Background(), "recovery-worker-a")
	if err != nil || !found {
		t.Fatalf("claim queued recovery job: found=%t err=%v", found, err)
	}

	first, err := coordinator.PrepareFirstWrite(context.Background(), claim)
	if err != nil {
		t.Fatalf("prepare initial first write: %v", err)
	}
	second, err := coordinator.PrepareFirstWrite(context.Background(), claim)
	if err != nil {
		t.Fatalf("retry current first-write claim: %v", err)
	}
	for name, permit := range map[string]TargetWritePermit{"first": first, "retry": second} {
		if err := permit.ValidateAt(fixture.now); err != nil {
			t.Fatalf("%s permit is not current after idempotent retry: %v", name, err)
		}
	}
	if first.permit.TargetPathDigest != second.permit.TargetPathDigest ||
		first.permit.ExpectedTargetRevision != second.permit.ExpectedTargetRevision ||
		first.permit.AttemptID != second.permit.AttemptID || first.permit.NodeLeaseID != second.permit.NodeLeaseID {
		t.Fatalf("idempotent retry changed durable mutation authority: first=%+v retry=%+v", first.permit, second.permit)
	}

	var latchCount, checkpointCount int64
	if err := fixture.db.Model(&model.BackupAssetRecoveryEvidence{}).
		Where("id = ?", recoverySchemaUseLatchRowID).Count(&latchCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.BackupAssetRecoveryCheckpoint{}).
		Where("job_id = ?", claim.JobID).Count(&checkpointCount).Error; err != nil {
		t.Fatal(err)
	}
	if latchCount != 1 || checkpointCount != 1 {
		t.Fatalf("retry duplicated first-write state: latches=%d checkpoints=%d", latchCount, checkpointCount)
	}
}

func TestWorkerPrepareFirstWriteRollsBackLateMutationArmFailure(t *testing.T) {
	fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptExecute)
	if _, err := fixture.service.Authorize(context.Background(), fixture.request); err != nil {
		t.Fatalf("execute recovery fixture: %v", err)
	}
	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	claim, found, err := coordinator.ClaimNext(context.Background(), "recovery-worker-a")
	if err != nil || !found {
		t.Fatalf("claim queued recovery job: found=%t err=%v", found, err)
	}

	const callback = "recovery:first-write-late-arm-failure"
	injected := false
	if err := fixture.db.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table != (model.BackupAssetRecoveryAttempt{}).TableName() {
			return
		}
		updates, ok := tx.Statement.Dest.(map[string]any)
		if !ok || updates["mutation_armed"] != true {
			return
		}
		injected = true
		_ = tx.AddError(errors.New("inject late first-write mutation-arm failure"))
	}); err != nil {
		t.Fatalf("register late first-write fault: %v", err)
	}
	t.Cleanup(func() { _ = fixture.db.Callback().Update().Remove(callback) })

	if _, err := coordinator.PrepareFirstWrite(context.Background(), claim); !errors.Is(err, ErrRecoveryWorkerUnavailable) {
		t.Fatalf("prepare first write with injected late arm failure error=%v", err)
	}
	if !injected {
		t.Fatal("late mutation-arm fault was not reached")
	}
	assertNoPreparedFirstWriteState(t, fixture, claim)
}

func TestWorkerPrepareFirstWriteRejectsStaleFencesWithoutCommit(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		invalidate func(*testing.T, *authorizationReceiptServiceFixture, RecoveryWorkerClaim)
	}{
		{
			name: "source",
			invalidate: func(t *testing.T, fixture *authorizationReceiptServiceFixture, claim RecoveryWorkerClaim) {
				t.Helper()
				if err := fixture.db.Model(&model.RecoveryPointLease{}).Where("id = ?", claim.SourceFence.LeaseID).
					Updates(map[string]any{"status": string(backupasset.LeaseReleased), "released_at": fixture.now, "updated_at": fixture.now}).Error; err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "attempt",
			invalidate: func(t *testing.T, fixture *authorizationReceiptServiceFixture, claim RecoveryWorkerClaim) {
				t.Helper()
				if err := fixture.db.Model(&model.BackupAssetRecoveryAttempt{}).Where("id = ?", claim.AttemptID).
					Updates(map[string]any{"lease_expires_at": fixture.now, "updated_at": fixture.now}).Error; err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "node",
			invalidate: func(t *testing.T, fixture *authorizationReceiptServiceFixture, claim RecoveryWorkerClaim) {
				t.Helper()
				if err := fixture.db.Model(&model.BackupAssetRecoveryNodeLease{}).Where("id = ?", claim.NodeLeaseID).
					Updates(map[string]any{"state": "released", "released_at": fixture.now, "updated_at": fixture.now}).Error; err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptExecute)
			if _, err := fixture.service.Authorize(context.Background(), fixture.request); err != nil {
				t.Fatalf("execute recovery fixture: %v", err)
			}
			coordinator := newRecoveryWorkerCoordinator(t, fixture)
			claim, found, err := coordinator.ClaimNext(context.Background(), "recovery-worker-a")
			if err != nil || !found {
				t.Fatalf("claim queued recovery job: found=%t err=%v", found, err)
			}

			testCase.invalidate(t, fixture, claim)
			if _, err := coordinator.PrepareFirstWrite(context.Background(), claim); !errors.Is(err, ErrRecoveryWorkerFenceLost) {
				t.Fatalf("prepare first write after stale %s fence error=%v, want ErrRecoveryWorkerFenceLost", testCase.name, err)
			}
			assertNoPreparedFirstWriteState(t, fixture, claim)
		})
	}
}

func TestWorkerPrepareFirstWriteConcurrentCurrentClaimConverges(t *testing.T) {
	fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptExecute)
	if _, err := fixture.service.Authorize(context.Background(), fixture.request); err != nil {
		t.Fatalf("execute recovery fixture: %v", err)
	}
	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	claim, found, err := coordinator.ClaimNext(context.Background(), "recovery-worker-a")
	if err != nil || !found {
		t.Fatalf("claim queued recovery job: found=%t err=%v", found, err)
	}
	sqlDB, err := fixture.db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)

	type result struct {
		permit TargetWritePermit
		err    error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	var callers sync.WaitGroup
	callers.Add(2)
	for range 2 {
		go func() {
			defer callers.Done()
			<-start
			permit, err := coordinator.PrepareFirstWrite(context.Background(), claim)
			results <- result{permit: permit, err: err}
		}()
	}
	close(start)
	callers.Wait()
	close(results)

	var permits []TargetWritePermit
	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent first-write caller error=%v", result.err)
		}
		if err := result.permit.ValidateAt(fixture.now); err != nil {
			t.Fatalf("concurrent first-write permit is not current: %v", err)
		}
		permits = append(permits, result.permit)
	}
	if len(permits) != 2 || permits[0].permit.TargetPathDigest != permits[1].permit.TargetPathDigest ||
		permits[0].permit.AttemptID != permits[1].permit.AttemptID || permits[0].permit.NodeLeaseID != permits[1].permit.NodeLeaseID {
		t.Fatalf("concurrent callers did not converge on one durable permit: %+v", permits)
	}

	var latchCount, checkpointCount int64
	if err := fixture.db.Model(&model.BackupAssetRecoveryEvidence{}).
		Where("id = ?", recoverySchemaUseLatchRowID).Count(&latchCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.BackupAssetRecoveryCheckpoint{}).
		Where("job_id = ?", claim.JobID).Count(&checkpointCount).Error; err != nil {
		t.Fatal(err)
	}
	if latchCount != 1 || checkpointCount != 1 {
		t.Fatalf("concurrent callers left partial or duplicate first-write state: latches=%d checkpoints=%d", latchCount, checkpointCount)
	}
}

func TestWorkerPrepareFirstWritePermitRejectsLostSourceAndAttemptFences(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		invalidate func(*testing.T, *authorizationReceiptServiceFixture, RecoveryWorkerClaim)
	}{
		{
			name: "source",
			invalidate: func(t *testing.T, fixture *authorizationReceiptServiceFixture, claim RecoveryWorkerClaim) {
				t.Helper()
				if err := fixture.db.Model(&model.RecoveryPointLease{}).Where("id = ?", claim.SourceFence.LeaseID).
					Updates(map[string]any{"status": string(backupasset.LeaseReleased), "released_at": fixture.now, "updated_at": fixture.now}).Error; err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "attempt",
			invalidate: func(t *testing.T, fixture *authorizationReceiptServiceFixture, claim RecoveryWorkerClaim) {
				t.Helper()
				if err := fixture.db.Model(&model.BackupAssetRecoveryAttempt{}).Where("id = ?", claim.AttemptID).
					Updates(map[string]any{"lease_expires_at": fixture.now, "updated_at": fixture.now}).Error; err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptExecute)
			if _, err := fixture.service.Authorize(context.Background(), fixture.request); err != nil {
				t.Fatalf("execute recovery fixture: %v", err)
			}
			coordinator := newRecoveryWorkerCoordinator(t, fixture)
			claim, found, err := coordinator.ClaimNext(context.Background(), "recovery-worker-a")
			if err != nil || !found {
				t.Fatalf("claim queued recovery job: found=%t err=%v", found, err)
			}
			permit, err := coordinator.PrepareFirstWrite(context.Background(), claim)
			if err != nil {
				t.Fatalf("prepare first write: %v", err)
			}

			testCase.invalidate(t, fixture, claim)
			if err := permit.ValidateAt(fixture.now); !errorsIsTargetPermit(err) {
				t.Fatalf("permit survived lost %s fence: %v", testCase.name, err)
			}
		})
	}
}

func TestWorkerPrepareFirstWriteSupersedesSourceDriftBeforeArm(t *testing.T) {
	fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptExecute)
	if _, err := fixture.service.Authorize(context.Background(), fixture.request); err != nil {
		t.Fatalf("execute recovery fixture: %v", err)
	}
	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	claim, found, err := coordinator.ClaimNext(context.Background(), "recovery-worker-a")
	if err != nil || !found {
		t.Fatalf("claim queued recovery job: found=%t err=%v", found, err)
	}

	var plan model.BackupAssetRecoveryPlan
	if err := fixture.db.Where("id = ?", fixture.request.PlanID).Take(&plan).Error; err != nil {
		t.Fatal(err)
	}
	unusedGrant := model.BackupAssetRecoveryGrant{
		ID: strings.Repeat("f", 32), PlanID: plan.ID, AuthorityCategory: string(AuthorityExactMirrorDelete),
		GrantHash: strings.Repeat("e", 64), ActorUserID: plan.RequesterID, ActorSessionID: strings.Repeat("d", 64),
		BindingDigest: strings.Repeat("c", 64), ExpiresAt: fixture.now.Add(time.Minute),
		CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}
	if err := fixture.db.Create(&unusedGrant).Error; err != nil {
		t.Fatalf("create unused authority for revocation: %v", err)
	}
	var planItem model.BackupAssetRecoveryPlanItem
	if err := fixture.db.Where("plan_id = ?", plan.ID).Take(&planItem).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.CatalogEntry{}).
		Where("generation_id = ? AND recovery_point_id = ? AND entry_id = ?",
			planItem.CatalogGenerationID, planItem.RecoveryPointID, planItem.EntryID).
		Update("normalized_path", "/substituted-after-claim").Error; err != nil {
		t.Fatalf("introduce source drift before first write: %v", err)
	}

	if _, err := coordinator.PrepareFirstWrite(context.Background(), claim); !errors.Is(err, ErrRecoverySourceChanged) {
		t.Fatalf("prepare first write after source drift error=%v, want ErrRecoverySourceChanged", err)
	}

	if err := fixture.db.Where("id = ?", plan.ID).Take(&plan).Error; err != nil {
		t.Fatal(err)
	}
	if plan.State != string(PlanStateSuperseded) {
		t.Fatalf("drifted plan state=%q, want superseded", plan.State)
	}
	var job model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", claim.JobID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	if job.State != string(JobStateFailed) || job.FailureCategory != recoveryPreWriteDriftFailureCategory ||
		job.WorkspacePhase != string(WorkspacePhaseNone) {
		t.Fatalf("drifted job did not retain zero-write terminal state: %+v", job)
	}
	var attempt model.BackupAssetRecoveryAttempt
	if err := fixture.db.Where("id = ?", claim.AttemptID).Take(&attempt).Error; err != nil {
		t.Fatal(err)
	}
	if attempt.State != string(AttemptStateSuperseded) || attempt.MutationArmed || attempt.ClosedAt == nil {
		t.Fatalf("drifted attempt state=%+v, want closed unarmed superseded", attempt)
	}
	var source model.RecoveryPointLease
	if err := fixture.db.Where("id = ?", claim.SourceFence.LeaseID).Take(&source).Error; err != nil {
		t.Fatal(err)
	}
	if source.Status != string(backupasset.LeaseReleased) || source.ReleasedAt == nil {
		t.Fatalf("source lease was not released: %+v", source)
	}
	var node model.BackupAssetRecoveryNodeLease
	if err := fixture.db.Where("id = ?", claim.NodeLeaseID).Take(&node).Error; err != nil {
		t.Fatal(err)
	}
	if node.State != "released" || node.ReleasedAt == nil {
		t.Fatalf("node lease was not released: %+v", node)
	}
	if err := fixture.db.Where("id = ?", unusedGrant.ID).Take(&unusedGrant).Error; err != nil {
		t.Fatal(err)
	}
	if unusedGrant.RevokedAt == nil {
		t.Fatal("pre-write drift did not revoke unused authority")
	}
	var latchCount int64
	if err := fixture.db.Model(&model.BackupAssetRecoveryEvidence{}).Where("id = ?", recoverySchemaUseLatchRowID).
		Count(&latchCount).Error; err != nil {
		t.Fatal(err)
	}
	if latchCount != 0 {
		t.Fatal("pre-write drift committed a schema-use latch without a mutation arm")
	}
}

func TestRecoveryReviewF3PreWriteDriftTransactionSQLite(t *testing.T) {
	fixture := newAuthorizationReceiptSQLiteMigrationServiceFixture(
		t, AuthorizationReceiptExecute, false, true,
	)
	result, err := fixture.service.Authorize(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("execute recovery fixture: %v", err)
	}
	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	claim, found, err := coordinator.ClaimNext(context.Background(), "recovery-f3-authority-drift")
	if err != nil || !found {
		t.Fatalf("claim authority-drift job: found=%t err=%v", found, err)
	}
	coordinator.liveRevalidator = &authorizationReceiptLiveRevalidatorSpy{err: ErrAuthorizationDenied}

	if _, err := coordinator.PrepareFirstWrite(context.Background(), claim); !errors.Is(err, ErrRecoverySourceChanged) {
		t.Fatalf("prepare first write after authority drift error=%v, want ErrRecoverySourceChanged", err)
	}

	var plan model.BackupAssetRecoveryPlan
	if err := fixture.db.Where("id = ?", fixture.request.PlanID).Take(&plan).Error; err != nil {
		t.Fatal(err)
	}
	var job model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", result.JobID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	var attempt model.BackupAssetRecoveryAttempt
	if err := fixture.db.Where("id = ?", claim.AttemptID).Take(&attempt).Error; err != nil {
		t.Fatal(err)
	}
	var source model.RecoveryPointLease
	if err := fixture.db.Where("id = ?", claim.SourceFence.LeaseID).Take(&source).Error; err != nil {
		t.Fatal(err)
	}
	var node model.BackupAssetRecoveryNodeLease
	if err := fixture.db.Where("id = ?", claim.NodeLeaseID).Take(&node).Error; err != nil {
		t.Fatal(err)
	}
	if plan.State != string(PlanStateSuperseded) || plan.TransitionRevision != result.PlanTransitionRevision+1 {
		t.Fatalf("authority drift plan=%+v", plan)
	}
	if job.State != string(JobStateFailed) || job.FailureCategory != recoveryPreWriteDriftFailureCategory ||
		job.TransitionRevision != claim.TransitionRevision+1 || job.WorkspacePhase != string(WorkspacePhaseNone) {
		t.Fatalf("authority drift job=%+v", job)
	}
	if attempt.State != string(AttemptStateSuperseded) || attempt.MutationArmed || attempt.ClosedAt == nil {
		t.Fatalf("authority drift attempt=%+v", attempt)
	}
	if source.Status != string(backupasset.LeaseReleased) || source.ReleasedAt == nil ||
		node.State != "released" || node.ReleasedAt == nil {
		t.Fatalf("authority drift leases source=%+v node=%+v", source, node)
	}
	var checkpointCount, latchCount int64
	if err := fixture.db.Model(&model.BackupAssetRecoveryCheckpoint{}).
		Where("job_id = ?", result.JobID).Count(&checkpointCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.BackupAssetRecoveryEvidence{}).
		Where("id = ?", recoverySchemaUseLatchRowID).Count(&latchCount).Error; err != nil {
		t.Fatal(err)
	}
	if checkpointCount != 0 || latchCount != 0 {
		t.Fatalf("authority drift crossed first-write barrier: checkpoints=%d latch=%d", checkpointCount, latchCount)
	}

	terminal := struct {
		PlanRevision uint64
		JobRevision  uint64
		AttemptState string
	}{plan.TransitionRevision, job.TransitionRevision, attempt.State}
	if _, err := coordinator.PrepareFirstWrite(context.Background(), claim); !errors.Is(err, ErrRecoveryWorkerFenceLost) {
		t.Fatalf("stale authority-drift worker error=%v, want ErrRecoveryWorkerFenceLost", err)
	}
	if err := fixture.db.Where("id = ?", plan.ID).Take(&plan).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Where("id = ?", job.ID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Where("id = ?", attempt.ID).Take(&attempt).Error; err != nil {
		t.Fatal(err)
	}
	if got := (struct {
		PlanRevision uint64
		JobRevision  uint64
		AttemptState string
	}{plan.TransitionRevision, job.TransitionRevision, attempt.State}); got != terminal {
		t.Fatalf("stale worker changed terminal aggregate: got=%+v want=%+v", got, terminal)
	}
}

type recoveryWorkerAuthorityEffectSpy struct {
	events             []string
	observeBindings    []RecoveryAuthorityBinding
	revalidateBindings []RecoveryAuthorityBinding
	inTransactions     []bool
	observation        RecoveryAuthorityObservation
	revalidateErr      error
}

func (spy *recoveryWorkerAuthorityEffectSpy) ObserveRecoveryAuthority(
	_ context.Context,
	binding RecoveryAuthorityBinding,
) (RecoveryAuthorityObservation, error) {
	spy.events = append(spy.events, "observe")
	spy.observeBindings = append(spy.observeBindings, binding)
	return spy.observation, nil
}

func (spy *recoveryWorkerAuthorityEffectSpy) RevalidateRecoveryAuthorityTx(
	_ context.Context,
	tx *gorm.DB,
	binding RecoveryAuthorityBinding,
	observation RecoveryAuthorityObservation,
) error {
	spy.events = append(spy.events, "revalidate")
	spy.revalidateBindings = append(spy.revalidateBindings, binding)
	if tx == nil || tx.Statement == nil {
		return errors.New("live authority revalidation did not receive caller transaction")
	}
	_, inTransaction := tx.Statement.ConnPool.(*sql.Tx)
	spy.inTransactions = append(spy.inTransactions, inTransaction)
	if !inTransaction {
		return errors.New("live authority revalidation escaped caller transaction")
	}
	if observation.observedAt != spy.observation.observedAt {
		return errors.New("live authority revalidation did not receive observed sealed product")
	}
	return spy.revalidateErr
}

func TestRecoveryPrepareFirstWriteObservesBeforeLockedRevalidation(t *testing.T) {
	fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptExecute)
	if _, err := fixture.service.Authorize(context.Background(), fixture.request); err != nil {
		t.Fatalf("execute recovery fixture: %v", err)
	}
	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	claim, found, err := coordinator.ClaimNext(context.Background(), "recovery-authority-effect-order")
	if err != nil || !found {
		t.Fatalf("claim recovery job: found=%t err=%v", found, err)
	}
	spy := &recoveryWorkerAuthorityEffectSpy{observation: RecoveryAuthorityObservation{
		observedAt: fixture.now,
		expiresAt:  fixture.now.Add(time.Minute),
	}}
	coordinator.liveRevalidator = spy

	if _, err := coordinator.PrepareFirstWrite(context.Background(), claim); err != nil {
		t.Fatalf("prepare first write with live authority observation: %v", err)
	}
	if got, want := strings.Join(spy.events, ","), "observe,revalidate"; got != want {
		t.Fatalf("live authority effect order=%q, want %q", got, want)
	}
	if len(spy.observeBindings) != 1 || len(spy.revalidateBindings) != 1 ||
		spy.observeBindings[0] != spy.revalidateBindings[0] || len(spy.inTransactions) != 1 ||
		!spy.inTransactions[0] {
		t.Fatalf("live authority binding changed across observation/revalidation: observe=%+v revalidate=%+v",
			spy.observeBindings, spy.revalidateBindings)
	}
}

func TestRecoveryTerminalMetricsObserveCommittedPreWriteFailureOnlyOnce(t *testing.T) {
	fixture := newAuthorizationReceiptSQLiteMigrationServiceFixture(
		t, AuthorizationReceiptExecute, false, true,
	)
	result, err := fixture.service.Authorize(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("execute recovery fixture: %v", err)
	}
	metrics := &recoveryMetricsSpy{}
	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	coordinator.metrics = metrics
	claim, found, err := coordinator.ClaimNext(context.Background(), "recovery-terminal-prewrite-metrics")
	if err != nil || !found {
		t.Fatalf("claim authority-drift job: found=%t err=%v", found, err)
	}
	coordinator.liveRevalidator = &authorizationReceiptLiveRevalidatorSpy{err: ErrAuthorizationDenied}
	if _, err := coordinator.PrepareFirstWrite(context.Background(), claim); !errors.Is(err, ErrRecoverySourceChanged) {
		t.Fatalf("prepare first write after authority drift error=%v, want ErrRecoverySourceChanged", err)
	}
	metrics.assertSingleOutcome(t, backupasset.ProviderRestic, JobStateFailed, MetricOutcomeFailure)
	if _, err := coordinator.PrepareFirstWrite(context.Background(), claim); !errors.Is(err, ErrRecoveryWorkerFenceLost) {
		t.Fatalf("stale authority-drift worker error=%v, want ErrRecoveryWorkerFenceLost", err)
	}
	if len(metrics.outcomes) != 1 {
		t.Fatalf("stale pre-write drift emitted terminal metrics=%+v, want one observation", metrics.outcomes)
	}
	var job model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", result.JobID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	if job.State != string(JobStateFailed) {
		t.Fatalf("pre-write drift job state=%q, want failed", job.State)
	}
}

func TestRecoveryTerminalMetricsObserveCommittedCancellationOnlyOnce(t *testing.T) {
	fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptExecute)
	result, err := fixture.service.Authorize(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("execute recovery fixture: %v", err)
	}
	metrics := &recoveryMetricsSpy{}
	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	coordinator.metrics = metrics
	if err := coordinator.CancelJob(context.Background(), result.JobID); err != nil {
		t.Fatalf("cancel queued recovery job: %v", err)
	}
	metrics.assertSingleOutcome(t, backupasset.ProviderRestic, JobStateCanceled, MetricOutcomeBlocked)
	if err := coordinator.CancelJob(context.Background(), result.JobID); err != nil {
		t.Fatalf("replay queued recovery cancellation: %v", err)
	}
	if len(metrics.outcomes) != 1 {
		t.Fatalf("cancellation replay emitted terminal metrics=%+v, want one observation", metrics.outcomes)
	}
}

func TestWorkerTakeoverReplacesOnlyExpiredCurrentFences(t *testing.T) {
	fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptExecute)
	if _, err := fixture.service.Authorize(context.Background(), fixture.request); err != nil {
		t.Fatalf("execute recovery fixture: %v", err)
	}
	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	first, found, err := coordinator.ClaimNext(context.Background(), "recovery-worker-a")
	if err != nil || !found {
		t.Fatalf("claim queued recovery job: found=%t err=%v", found, err)
	}
	if premature, prematureFound, prematureErr := coordinator.TakeoverExpired(context.Background(), "recovery-worker-b"); prematureErr != nil || prematureFound || premature.JobID != "" {
		t.Fatalf("active owner was taken over: claim=%+v found=%t err=%v", premature, prematureFound, prematureErr)
	}
	if err := fixture.db.Model(&model.BackupAssetRecoveryAttempt{}).
		Where("id = ? AND owner_id = ? AND fence = ?", first.AttemptID, first.WorkerID, first.AttemptFence).
		Update("mutation_armed", true).Error; err != nil {
		t.Fatalf("arm recovery attempt before crash: %v", err)
	}

	fixture.now = first.LeaseExpiresAt.Add(time.Second)
	takeover, takeoverFound, takeoverErr := coordinator.TakeoverExpired(context.Background(), "recovery-worker-b")
	if takeoverErr != nil || !takeoverFound {
		t.Fatalf("take over expired recovery job: found=%t err=%v", takeoverFound, takeoverErr)
	}
	if takeover.JobID != first.JobID || takeover.AttemptID == first.AttemptID ||
		takeover.AttemptFence != first.AttemptFence+1 || takeover.NodeFence != first.NodeFence+1 ||
		takeover.WorkerID != "recovery-worker-b" || takeover.TransitionRevision != first.TransitionRevision+1 ||
		takeover.SourceFence.LeaseID != first.SourceFence.LeaseID ||
		takeover.SourceFence.FenceToken == first.SourceFence.FenceToken {
		t.Fatalf("unexpected recovery takeover: first=%+v takeover=%+v", first, takeover)
	}

	var oldAttempt model.BackupAssetRecoveryAttempt
	if err := fixture.db.Where("id = ?", first.AttemptID).Take(&oldAttempt).Error; err != nil {
		t.Fatal(err)
	}
	if oldAttempt.State != string(AttemptStateLost) || oldAttempt.ClosedAt == nil || oldAttempt.OwnerID != first.WorkerID {
		t.Fatalf("old recovery attempt not durably lost: %+v", oldAttempt)
	}
	var currentAttempt model.BackupAssetRecoveryAttempt
	if err := fixture.db.Where("id = ?", takeover.AttemptID).Take(&currentAttempt).Error; err != nil {
		t.Fatal(err)
	}
	if currentAttempt.State != string(AttemptStateRunning) || currentAttempt.OwnerID != takeover.WorkerID ||
		currentAttempt.Fence != takeover.AttemptFence || currentAttempt.MutationArmed != oldAttempt.MutationArmed {
		t.Fatalf("replacement recovery attempt mismatch: %+v", currentAttempt)
	}
	var nodeLease model.BackupAssetRecoveryNodeLease
	if err := fixture.db.Where("id = ?", takeover.NodeLeaseID).Take(&nodeLease).Error; err != nil {
		t.Fatal(err)
	}
	if nodeLease.AttemptID == nil || *nodeLease.AttemptID != takeover.AttemptID ||
		nodeLease.OwnerID != takeover.WorkerID || nodeLease.Fence != takeover.NodeFence || nodeLease.State != "active" {
		t.Fatalf("takeover node lease mismatch: %+v", nodeLease)
	}
	var sourceLease model.RecoveryPointLease
	if err := fixture.db.Where("id = ?", takeover.SourceFence.LeaseID).Take(&sourceLease).Error; err != nil {
		t.Fatal(err)
	}
	if sourceLease.AttemptID != takeover.AttemptID || sourceLease.FenceToken != takeover.SourceFence.FenceToken ||
		sourceLease.Status != "active" {
		t.Fatalf("takeover source lease mismatch: %+v", sourceLease)
	}

	if _, err := coordinator.Heartbeat(context.Background(), first); !errors.Is(err, ErrRecoveryWorkerFenceLost) {
		t.Fatalf("old worker heartbeat after takeover error=%v", err)
	}
}

func TestRecoveryInterruptedOperationHandoffLocksPlanBeforeJob(t *testing.T) {
	fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptExecute)
	if _, err := fixture.service.Authorize(context.Background(), fixture.request); err != nil {
		t.Fatalf("execute recovery fixture: %v", err)
	}
	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	first, found, err := coordinator.ClaimNext(context.Background(), "interrupted-lock-order-worker-a")
	if err != nil || !found {
		t.Fatalf("claim recovery job: found=%t err=%v", found, err)
	}
	if _, err := coordinator.PrepareFirstWrite(context.Background(), first); err != nil {
		t.Fatalf("prepare durable sequence-zero write barrier: %v", err)
	}
	var item model.BackupAssetRecoveryJobItem
	if err := fixture.db.Where("job_id = ?", first.JobID).Order("ordinal ASC").Take(&item).Error; err != nil {
		t.Fatal(err)
	}
	fixture.now = first.LeaseExpiresAt.Add(time.Second)
	takeover, takeoverFound, takeoverErr := coordinator.TakeoverExpired(
		context.Background(), "interrupted-lock-order-worker-b",
	)
	if takeoverErr != nil || !takeoverFound {
		t.Fatalf("take over interrupted recovery job: found=%t err=%v", takeoverFound, takeoverErr)
	}

	var mu sync.Mutex
	lockedTables := make([]string, 0, 2)
	callbackName := "recovery:interrupted-plan-before-job:" + t.Name()
	if err := fixture.db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if _, locked := tx.Statement.Clauses["FOR"]; !locked || tx.Statement.Schema == nil {
			return
		}
		table := tx.Statement.Schema.Table
		if table != (model.BackupAssetRecoveryPlan{}).TableName() &&
			table != (model.BackupAssetRecoveryJob{}).TableName() {
			return
		}
		mu.Lock()
		lockedTables = append(lockedTables, table)
		mu.Unlock()
	}); err != nil {
		t.Fatalf("register lock-order observer: %v", err)
	}
	t.Cleanup(func() {
		if err := fixture.db.Callback().Query().Remove(callbackName); err != nil {
			t.Errorf("remove lock-order observer: %v", err)
		}
	})

	err = fixture.db.Transaction(func(tx *gorm.DB) error {
		_, loadErr := coordinator.loadInterruptedOperationHandoffTx(
			context.Background(), tx, takeover, item.ID, fixture.now, false, false,
		)
		return loadErr
	})
	if err != nil {
		t.Fatalf("load interrupted operation handoff: %v", err)
	}
	mu.Lock()
	got := append([]string(nil), lockedTables...)
	mu.Unlock()
	want := []string{
		(model.BackupAssetRecoveryPlan{}).TableName(),
		(model.BackupAssetRecoveryJob{}).TableName(),
	}
	if len(got) < len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("interrupted operation handoff plan/job lock order=%v, want prefix %v", got, want)
	}
}

func TestRecoveryOperationVerificationLocksPlanBeforeJob(t *testing.T) {
	fixture, coordinator, claim, item, checkpoint := newRecoveryPostTerminalProjectionLockOrderFixture(t)
	fixture.now = fixture.now.Add(time.Second)
	observeRecoveryProjectionPlanJobLockOrder(t, fixture.db, "verification", func() error {
		_, err := coordinator.ProjectOperationVerification(
			context.Background(), claim, item.ID, checkpoint.ID, 7, 7, strings.Repeat("d", 64), fixture.now,
		)
		return err
	})
}

func TestRecoveryOperationMismatchLocksPlanBeforeJob(t *testing.T) {
	fixture, coordinator, claim, item, checkpoint := newRecoveryPostTerminalProjectionLockOrderFixture(t)
	fixture.now = fixture.now.Add(time.Second)
	observeRecoveryProjectionPlanJobLockOrder(t, fixture.db, "mismatch", func() error {
		_, err := coordinator.ProjectOperationMismatch(
			context.Background(), claim, item.ID, checkpoint.ID, 7, 6, strings.Repeat("e", 64), fixture.now, 1,
		)
		return err
	})
}

func TestWorkerProjectPendingOperationMismatchAtomicallyNeedsAttention(t *testing.T) {
	fixture, coordinator, claim, writeResult, observation, before :=
		newRecoveryPendingOperationMismatchFixture(t)
	projector, ok := any(coordinator).(recoveryPendingOperationMismatchProjector)
	if !ok {
		t.Fatal("recovery worker does not atomically project pending-operation mismatches")
	}

	verifiedAt := fixture.now
	evidence, err := projector.projectPendingOperationMismatch(
		context.Background(), claim, before.item.ID, writeResult, observation, verifiedAt,
	)
	if err != nil {
		t.Fatalf("project pending-operation mismatch: %v", err)
	}
	assertRecoveryUnresolvedOutcomeProjection(
		t, fixture.db, claim, before.job, UnresolvedOperationRevisionDisagreement,
		SourceRevalidationMatched, 0,
	)
	if evidence.Kind != "failure" || evidence.Outcome != "needs_attention" ||
		evidence.DifferenceCount != 0 || evidence.VerifiedAt == nil || !evidence.VerifiedAt.Equal(verifiedAt) {
		t.Fatalf("pending mismatch returned evidence=%+v", evidence)
	}
	if _, err := coordinator.PrepareFirstWrite(context.Background(), claim); !errors.Is(err, ErrRecoveryWorkerFenceLost) {
		t.Fatalf("terminal mismatch claim minted another write permit: %v", err)
	}
}

func TestWorkerPendingProjectionAuthorityDriftRollsBackWithoutEffects(t *testing.T) {
	fixture, coordinator, claim, writeResult, observation, before :=
		newRecoveryPendingOperationMismatchFixture(t)
	spy := &recoveryWorkerAuthorityEffectSpy{
		observation: RecoveryAuthorityObservation{
			observedAt: fixture.now,
			expiresAt:  fixture.now.Add(time.Minute),
		},
		revalidateErr: ErrRecoveryTargetChanged,
	}
	coordinator.liveRevalidator = spy

	_, err := coordinator.projectPendingOperationMismatch(
		context.Background(), claim, before.item.ID, writeResult, observation, fixture.now,
	)
	if !errors.Is(err, ErrRecoveryWorkerUnavailable) {
		t.Fatalf("authority drift projection error=%v, want fail-closed worker unavailable", err)
	}
	if got, want := strings.Join(spy.events, ","), "observe,revalidate"; got != want {
		t.Fatalf("authority drift effect order=%q, want %q", got, want)
	}
	after := loadRecoveryPendingOperationMismatchState(t, fixture.db, claim, before.item.ID)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("authority drift left projection effects: before=%+v after=%+v", before, after)
	}
}

func TestWorkerProjectPendingOperationMismatchRejectsStaleFence(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*RecoveryWorkerClaim)
	}{
		{name: "attempt fence", mutate: func(claim *RecoveryWorkerClaim) { claim.AttemptFence++ }},
		{name: "node fence", mutate: func(claim *RecoveryWorkerClaim) { claim.NodeFence++ }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture, coordinator, claim, writeResult, observation, before :=
				newRecoveryPendingOperationMismatchFixture(t)
			projector, ok := any(coordinator).(recoveryPendingOperationMismatchProjector)
			if !ok {
				t.Fatal("recovery worker does not atomically project pending-operation mismatches")
			}
			stale := claim
			test.mutate(&stale)
			if _, err := projector.projectPendingOperationMismatch(
				context.Background(), stale, before.item.ID, writeResult, observation, fixture.now,
			); !errors.Is(err, ErrRecoveryWorkerFenceLost) {
				t.Fatalf("stale pending mismatch projection error=%v, want ErrRecoveryWorkerFenceLost", err)
			}
			after := loadRecoveryPendingOperationMismatchState(t, fixture.db, claim, before.item.ID)
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("stale %s changed pending mismatch state: before=%+v after=%+v", test.name, before, after)
			}
		})
	}
}

type recoveryPendingOperationMismatchState struct {
	plan        model.BackupAssetRecoveryPlan
	job         model.BackupAssetRecoveryJob
	attempt     model.BackupAssetRecoveryAttempt
	item        model.BackupAssetRecoveryJobItem
	source      model.RecoveryPointLease
	node        model.BackupAssetRecoveryNodeLease
	checkpoints []model.BackupAssetRecoveryCheckpoint
	evidence    []model.BackupAssetRecoveryEvidence
}

func newRecoveryPendingOperationMismatchFixture(
	t *testing.T,
) (
	*authorizationReceiptServiceFixture,
	*WorkerCoordinator,
	RecoveryWorkerClaim,
	TargetWriteResult,
	TargetVerifyObservation,
	recoveryPendingOperationMismatchState,
) {
	t.Helper()
	fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptExecute)
	if _, err := fixture.service.Authorize(context.Background(), fixture.request); err != nil {
		t.Fatalf("execute recovery fixture: %v", err)
	}
	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	claim, found, err := coordinator.ClaimNext(context.Background(), "pending-mismatch-worker")
	if err != nil || !found {
		t.Fatalf("claim pending mismatch fixture: found=%t err=%v", found, err)
	}
	if _, err := coordinator.PrepareFirstWrite(context.Background(), claim); err != nil {
		t.Fatalf("arm pending mismatch fixture: %v", err)
	}
	var item model.BackupAssetRecoveryJobItem
	if err := fixture.db.Where("job_id = ?", claim.JobID).Order("ordinal ASC").Take(&item).Error; err != nil {
		t.Fatal(err)
	}
	if item.PlanItemID == nil ||
		(RecoveryOperationKind(item.OperationKind) != RecoveryOperationCreate &&
			RecoveryOperationKind(item.OperationKind) != RecoveryOperationOverwrite) {
		t.Fatalf("pending mismatch fixture item cannot represent a completed write: %+v", item)
	}
	before := loadRecoveryPendingOperationMismatchState(t, fixture.db, claim, item.ID)
	if before.job.WorkspacePhase != string(WorkspacePhaseMarkerCreated) || !before.attempt.MutationArmed ||
		len(before.checkpoints) != 1 || before.checkpoints[0].Phase != string(CheckpointPhaseWorkspaceReserved) ||
		len(before.evidence) != 0 {
		t.Fatalf("pending mismatch fixture crossed projection boundary: %+v", before)
	}
	mismatchDigest := strings.Repeat("e", 64)
	if mismatchDigest == item.ExpectedPostIdentityDigest {
		mismatchDigest = strings.Repeat("d", 64)
	}
	writeResult := TargetWriteResult{
		BytesWritten: item.ExpectedPostBytes, IdentityDigest: item.ExpectedPostIdentityDigest,
		TargetRevision: "target-revision-pending-mismatch-write",
	}
	observation := TargetVerifyObservation{
		Kind: TargetPresencePresent, Present: &PresentObservation{
			IdentityDigest: mismatchDigest, Bytes: item.ExpectedPostBytes,
		},
		ObservedRevision: "target-revision-pending-mismatch-observed",
	}
	if observation.Validate() != nil {
		t.Fatalf("pending mismatch fixture observation is invalid: %+v", observation)
	}
	return fixture, coordinator, claim, writeResult, observation, before
}

func loadRecoveryPendingOperationMismatchState(
	t *testing.T,
	db *gorm.DB,
	claim RecoveryWorkerClaim,
	jobItemID string,
) recoveryPendingOperationMismatchState {
	t.Helper()
	var state recoveryPendingOperationMismatchState
	if err := db.Where("id = ?", claim.JobID).Take(&state.job).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("id = ?", state.job.PlanID).Take(&state.plan).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("id = ?", claim.AttemptID).Take(&state.attempt).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("id = ?", jobItemID).Take(&state.item).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("id = ?", claim.SourceFence.LeaseID).Take(&state.source).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("id = ?", claim.NodeLeaseID).Take(&state.node).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("job_id = ?", claim.JobID).Order("sequence ASC").Find(&state.checkpoints).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("job_id = ? AND kind = ?", claim.JobID, "failure").
		Order("created_at ASC, id ASC").Find(&state.evidence).Error; err != nil {
		t.Fatal(err)
	}
	return state
}

func newRecoveryPostTerminalProjectionLockOrderFixture(
	t *testing.T,
) (
	*authorizationReceiptServiceFixture,
	*WorkerCoordinator,
	RecoveryWorkerClaim,
	model.BackupAssetRecoveryJobItem,
	model.BackupAssetRecoveryCheckpoint,
) {
	t.Helper()
	fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptExecute)
	if _, err := fixture.service.Authorize(context.Background(), fixture.request); err != nil {
		t.Fatalf("execute recovery fixture: %v", err)
	}
	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	first, found, err := coordinator.ClaimNext(context.Background(), "projection-lock-order-worker-a")
	if err != nil || !found {
		t.Fatalf("claim recovery job: found=%t err=%v", found, err)
	}
	if _, err := coordinator.PrepareFirstWrite(context.Background(), first); err != nil {
		t.Fatalf("prepare durable sequence-zero write barrier: %v", err)
	}
	var item model.BackupAssetRecoveryJobItem
	if err := fixture.db.Where("job_id = ?", first.JobID).Order("ordinal ASC").Take(&item).Error; err != nil {
		t.Fatal(err)
	}
	fixture.now = first.LeaseExpiresAt.Add(time.Second)
	takeover, found, err := coordinator.TakeoverExpired(context.Background(), "projection-lock-order-worker-b")
	if err != nil || !found {
		t.Fatalf("take over interrupted recovery job: found=%t err=%v", found, err)
	}
	checkpoint, err := coordinator.AdoptInterruptedOperation(context.Background(), takeover, item.ID)
	if err != nil {
		t.Fatalf("adopt interrupted operation: %v", err)
	}
	return fixture, coordinator, takeover, item, checkpoint
}

func observeRecoveryProjectionPlanJobLockOrder(
	t *testing.T,
	db *gorm.DB,
	projection string,
	project func() error,
) {
	t.Helper()
	var mu sync.Mutex
	lockedTables := make([]string, 0, 2)
	callbackName := "recovery:" + projection + "-plan-before-job:" + t.Name()
	if err := db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if _, locked := tx.Statement.Clauses["FOR"]; !locked || tx.Statement.Schema == nil {
			return
		}
		table := tx.Statement.Schema.Table
		if table != (model.BackupAssetRecoveryPlan{}).TableName() &&
			table != (model.BackupAssetRecoveryJob{}).TableName() {
			return
		}
		mu.Lock()
		lockedTables = append(lockedTables, table)
		mu.Unlock()
	}); err != nil {
		t.Fatalf("register %s lock-order observer: %v", projection, err)
	}
	t.Cleanup(func() {
		if err := db.Callback().Query().Remove(callbackName); err != nil {
			t.Errorf("remove %s lock-order observer: %v", projection, err)
		}
	})

	if err := project(); !errors.Is(err, ErrRecoveryWorkerFenceLost) {
		t.Fatalf("post-terminal %s projection error=%v, want fence loss", projection, err)
	}
	mu.Lock()
	got := append([]string(nil), lockedTables...)
	mu.Unlock()
	want := []string{
		(model.BackupAssetRecoveryPlan{}).TableName(),
		(model.BackupAssetRecoveryJob{}).TableName(),
	}
	if len(got) < len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("operation %s plan/job lock order=%v, want prefix %v", projection, got, want)
	}
}

func TestWorkerRestartAdoptsInterruptedMutationFromSequenceZero(t *testing.T) {
	fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptExecute)
	if _, err := fixture.service.Authorize(context.Background(), fixture.request); err != nil {
		t.Fatalf("execute recovery fixture: %v", err)
	}
	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	first, found, err := coordinator.ClaimNext(context.Background(), "recovery-worker-a")
	if err != nil || !found {
		t.Fatalf("claim recovery job: found=%t err=%v", found, err)
	}
	if _, err := coordinator.PrepareFirstWrite(context.Background(), first); err != nil {
		t.Fatalf("prepare durable sequence-zero write barrier: %v", err)
	}

	var job model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", first.JobID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	var item model.BackupAssetRecoveryJobItem
	if err := fixture.db.Where("job_id = ?", first.JobID).Order("ordinal ASC").Take(&item).Error; err != nil {
		t.Fatal(err)
	}
	var sequenceZero model.BackupAssetRecoveryCheckpoint
	if err := fixture.db.Where("job_id = ? AND sequence = ?", first.JobID, 0).Take(&sequenceZero).Error; err != nil {
		t.Fatalf("load durable sequence-zero reservation: %v", err)
	}
	if sequenceZero.Phase != string(CheckpointPhaseWorkspaceReserved) {
		t.Fatalf("sequence-zero phase=%q, want workspace reservation", sequenceZero.Phase)
	}

	// The old process has already caused exactly this target mutation but
	// crashed before its operation projection/checkpoint commit.
	fixture.now = first.LeaseExpiresAt.Add(time.Second)
	takeover, takeoverFound, takeoverErr := coordinator.TakeoverExpired(context.Background(), "recovery-worker-b")
	if takeoverErr != nil || !takeoverFound {
		t.Fatalf("take over interrupted recovery job: found=%t err=%v", takeoverFound, takeoverErr)
	}

	adopter, ok := any(coordinator).(recoveryInterruptedOperationAdopter)
	if !ok {
		t.Fatal("recovery worker does not provide interrupted-mutation verify/adopt behavior")
	}
	advance := recoveryTargetChainAdvanceForTest(t, job, item, takeover)
	wantRevision, err := advance.NextRevision()
	if err != nil {
		t.Fatalf("build adopted target-chain revision: %v", err)
	}
	checkpoint, err := adopter.AdoptInterruptedOperation(context.Background(), takeover, item.ID)
	if err != nil {
		t.Fatalf("adopt exact interrupted mutation after restart: %v", err)
	}
	if checkpoint.Sequence != 1 || checkpoint.Phase != string(CheckpointPhaseOperation) ||
		checkpoint.JobID != takeover.JobID || checkpoint.AttemptID != takeover.AttemptID ||
		checkpoint.PriorTargetRevision != job.TargetChainRevision || checkpoint.NextTargetRevision != wantRevision ||
		checkpoint.OperationDigest != advance.OperationDigest || checkpoint.AttemptFence != takeover.AttemptFence ||
		checkpoint.NodeFence != takeover.NodeFence {
		t.Fatalf("adopted checkpoint=%+v, want restart-bound operation checkpoint", checkpoint)
	}
	if err := fixture.db.Where("id = ?", item.ID).Take(&item).Error; err != nil {
		t.Fatal(err)
	}
	if item.Outcome != "succeeded" || item.FailureCategory != "" {
		t.Fatalf("adopted item projection=%+v, want succeeded without failure", item)
	}
	if item.BytesWritten != item.ExpectedPostBytes || item.VerifiedSize != item.ExpectedPostBytes ||
		item.VerifiedDigest != item.ExpectedPostIdentityDigest {
		t.Fatalf("adopted item verification projection=%+v, want outcome and exact verification facts in one final projection", item)
	}
	var attempt model.BackupAssetRecoveryAttempt
	if err := fixture.db.Where("id = ?", takeover.AttemptID).Take(&attempt).Error; err != nil {
		t.Fatal(err)
	}
	if attempt.State != string(AttemptStateRunning) || attempt.ClosedAt != nil {
		t.Fatalf("adoption attempt=%+v, want active continuation attempt", attempt)
	}
	if err := fixture.db.Where("id = ?", takeover.JobID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	if job.TargetChainRevision != wantRevision {
		t.Fatalf("job target-chain revision=%q, want adopted revision %q", job.TargetChainRevision, wantRevision)
	}
}

func TestWorkerRestartAdoptsSkipWithAtomicTerminalFactsAndAttemptClosure(t *testing.T) {
	fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptExecute)
	if _, err := fixture.service.Authorize(context.Background(), fixture.request); err != nil {
		t.Fatalf("execute recovery fixture: %v", err)
	}
	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	first, found, err := coordinator.ClaimNext(context.Background(), "recovery-worker-a")
	if err != nil || !found {
		t.Fatalf("claim recovery job: found=%t err=%v", found, err)
	}
	if _, err := coordinator.PrepareFirstWrite(context.Background(), first); err != nil {
		t.Fatalf("prepare durable sequence-zero write barrier: %v", err)
	}
	var beforeJob model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", first.JobID).Take(&beforeJob).Error; err != nil {
		t.Fatal(err)
	}
	var item model.BackupAssetRecoveryJobItem
	if err := fixture.db.Where("job_id = ? AND operation_kind = ?", first.JobID, RecoveryOperationSkip).
		Take(&item).Error; err != nil {
		t.Fatal(err)
	}
	fixture.now = first.LeaseExpiresAt.Add(time.Second)
	takeover, found, err := coordinator.TakeoverExpired(context.Background(), "recovery-worker-b")
	if err != nil || !found {
		t.Fatalf("take over interrupted recovery job: found=%t err=%v", found, err)
	}

	checkpoint, err := coordinator.AdoptInterruptedOperation(context.Background(), takeover, item.ID)
	if err != nil {
		t.Fatalf("adopt interrupted skip: %v", err)
	}
	if checkpoint.ID == "" || checkpoint.Sequence != 1 ||
		checkpoint.Phase != string(CheckpointPhaseOperation) || checkpoint.JobItemID != item.ID ||
		checkpoint.PriorTargetRevision != beforeJob.TargetChainRevision ||
		checkpoint.NextTargetRevision != checkpoint.PriorTargetRevision ||
		checkpoint.UnresolvedCategory != "" || checkpoint.WriteResultDigest != "" ||
		checkpoint.ObservationDigest != "" || checkpoint.SourceRevalidationOutcome != "" {
		t.Fatalf("skip adoption checkpoint=%+v, want item-bound unchanged-target operation", checkpoint)
	}
	if err := fixture.db.Where("id = ?", item.ID).Take(&item).Error; err != nil {
		t.Fatal(err)
	}
	if item.Outcome != "skipped" || item.BytesWritten != 0 || item.VerifiedSize != item.ExpectedPriorBytes ||
		item.VerifiedDigest != item.ExpectedPriorDigest || item.FailureCategory != "" {
		t.Fatalf("adopted skip projection=%+v, want exact unchanged-target terminal facts", item)
	}
	var attempt model.BackupAssetRecoveryAttempt
	if err := fixture.db.Where("id = ?", takeover.AttemptID).Take(&attempt).Error; err != nil {
		t.Fatal(err)
	}
	if attempt.State != string(AttemptStateRunning) || attempt.ClosedAt != nil {
		t.Fatalf("adopted skip attempt=%+v, want active continuation attempt", attempt)
	}
	var afterJob model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", takeover.JobID).Take(&afterJob).Error; err != nil {
		t.Fatal(err)
	}
	if afterJob.TargetChainRevision != beforeJob.TargetChainRevision ||
		afterJob.WorkspacePhase != string(WorkspacePhaseWriting) {
		t.Fatalf("skip adoption changed target chain/workspace: before=%+v after=%+v", beforeJob, afterJob)
	}
	var operationCheckpoints int64
	if err := fixture.db.Model(&model.BackupAssetRecoveryCheckpoint{}).
		Where("job_id = ? AND sequence = ?", takeover.JobID, 1).Count(&operationCheckpoints).Error; err != nil {
		t.Fatal(err)
	}
	if operationCheckpoints != 1 {
		t.Fatalf("skip adoption persisted sequence-1 checkpoints=%d, want one", operationCheckpoints)
	}
}

func TestWorkerAdoptionRollsBackProjectionWhenAttemptClosureFails(t *testing.T) {
	fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptExecute)
	if _, err := fixture.service.Authorize(context.Background(), fixture.request); err != nil {
		t.Fatalf("execute recovery fixture: %v", err)
	}
	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	first, found, err := coordinator.ClaimNext(context.Background(), "recovery-worker-a")
	if err != nil || !found {
		t.Fatalf("claim recovery job: found=%t err=%v", found, err)
	}
	if _, err := coordinator.PrepareFirstWrite(context.Background(), first); err != nil {
		t.Fatalf("prepare durable sequence-zero write barrier: %v", err)
	}
	var items []model.BackupAssetRecoveryJobItem
	if err := fixture.db.Where("job_id = ?", first.JobID).Order("ordinal ASC").Find(&items).Error; err != nil {
		t.Fatal(err)
	}
	if len(items) < 2 {
		t.Fatalf("rollback fixture items=%d, want multiple items", len(items))
	}
	fixture.now = first.LeaseExpiresAt.Add(time.Second)
	takeover, found, err := coordinator.TakeoverExpired(context.Background(), "recovery-worker-b")
	if err != nil || !found {
		t.Fatalf("take over interrupted recovery job: found=%t err=%v", found, err)
	}
	target, ok := coordinator.target.(*recoveryRestartTargetFake)
	if !ok {
		t.Fatalf("rollback fixture target=%T, want recoveryRestartTargetFake", coordinator.target)
	}
	for index := 0; index < len(items)-1; index++ {
		target.mu.Lock()
		target.observation = TargetVerifyObservation{}
		target.mu.Unlock()
		if _, err := coordinator.AdoptInterruptedOperation(context.Background(), takeover, items[index].ID); err != nil {
			t.Fatalf("adopt rollback setup item %d: %v", index, err)
		}
	}
	item := items[len(items)-1]
	var beforeJob model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", takeover.JobID).Take(&beforeJob).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Where("id = ?", item.ID).Take(&item).Error; err != nil {
		t.Fatal(err)
	}
	var beforeCheckpointCount int64
	if err := fixture.db.Model(&model.BackupAssetRecoveryCheckpoint{}).
		Where("job_id = ?", takeover.JobID).Count(&beforeCheckpointCount).Error; err != nil {
		t.Fatal(err)
	}
	target.mu.Lock()
	target.observation = TargetVerifyObservation{}
	target.mu.Unlock()

	const callbackName = "test:fail-atomic-adoption-attempt-close"
	injected := errors.New("injected attempt closure failure")
	if err := fixture.db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement != nil && tx.Statement.Table == (model.BackupAssetRecoveryAttempt{}).TableName() {
			_ = tx.AddError(injected)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fixture.db.Callback().Update().Remove(callbackName) })

	if _, err := coordinator.AdoptInterruptedOperation(context.Background(), takeover, item.ID); !errors.Is(err, ErrRecoveryWorkerUnavailable) {
		t.Fatalf("adoption with failed attempt closure error=%v, want ErrRecoveryWorkerUnavailable", err)
	}
	assertRecoveryRestartAdoptionUnchanged(t, fixture.db, beforeJob, item, beforeCheckpointCount)
	var attempt model.BackupAssetRecoveryAttempt
	if err := fixture.db.Where("id = ?", takeover.AttemptID).Take(&attempt).Error; err != nil {
		t.Fatal(err)
	}
	if attempt.State != string(AttemptStateRunning) || attempt.ClosedAt != nil {
		t.Fatalf("failed atomic adoption changed attempt=%+v", attempt)
	}
}

func TestWorkerRestartAdoptionRejectsCallerSuppliedVerifiedIdentityWithoutTargetObservation(t *testing.T) {
	fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptExecute)
	if _, err := fixture.service.Authorize(context.Background(), fixture.request); err != nil {
		t.Fatalf("execute recovery fixture: %v", err)
	}
	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	coordinator.target = nil
	first, found, err := coordinator.ClaimNext(context.Background(), "recovery-worker-a")
	if err != nil || !found {
		t.Fatalf("claim recovery job: found=%t err=%v", found, err)
	}
	if _, err := coordinator.PrepareFirstWrite(context.Background(), first); err != nil {
		t.Fatalf("prepare durable sequence-zero write barrier: %v", err)
	}

	var job model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", first.JobID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	var item model.BackupAssetRecoveryJobItem
	if err := fixture.db.Where("job_id = ?", first.JobID).Order("ordinal ASC").Take(&item).Error; err != nil {
		t.Fatal(err)
	}
	fixture.now = first.LeaseExpiresAt.Add(time.Second)
	takeover, found, err := coordinator.TakeoverExpired(context.Background(), "recovery-worker-b")
	if err != nil || !found {
		t.Fatalf("take over interrupted recovery job: found=%t err=%v", found, err)
	}

	_, err = coordinator.AdoptInterruptedOperation(context.Background(), takeover, item.ID)
	if err == nil {
		t.Fatalf("adoption accepted a caller-supplied verified identity without a target observation: %v", err)
	}

	var checkpointCount int64
	if err := fixture.db.Model(&model.BackupAssetRecoveryCheckpoint{}).
		Where("job_id = ? AND sequence = ?", takeover.JobID, 1).Count(&checkpointCount).Error; err != nil {
		t.Fatal(err)
	}
	if checkpointCount != 0 {
		t.Fatalf("unverified restart adoption persisted sequence-1 checkpoints=%d", checkpointCount)
	}
}

func TestWorkerRestartAdoptionRejectsTargetVerificationMismatch(t *testing.T) {
	fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptExecute)
	if _, err := fixture.service.Authorize(context.Background(), fixture.request); err != nil {
		t.Fatalf("execute recovery fixture: %v", err)
	}
	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	target := &recoveryRestartTargetFake{observation: recoveryRestartObservationForTest()}
	coordinator.target = target
	first, found, err := coordinator.ClaimNext(context.Background(), "recovery-worker-a")
	if err != nil || !found {
		t.Fatalf("claim recovery job: found=%t err=%v", found, err)
	}
	if _, err := coordinator.PrepareFirstWrite(context.Background(), first); err != nil {
		t.Fatalf("prepare durable sequence-zero write barrier: %v", err)
	}

	var job model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", first.JobID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	var item model.BackupAssetRecoveryJobItem
	if err := fixture.db.Where("job_id = ?", first.JobID).Order("ordinal ASC").Take(&item).Error; err != nil {
		t.Fatal(err)
	}
	fixture.now = first.LeaseExpiresAt.Add(time.Second)
	takeover, found, err := coordinator.TakeoverExpired(context.Background(), "recovery-worker-b")
	if err != nil || !found {
		t.Fatalf("take over interrupted recovery job: found=%t err=%v", found, err)
	}

	_, err = coordinator.AdoptInterruptedOperation(context.Background(), takeover, item.ID)
	if err == nil {
		t.Fatal("restart adoption accepted a target verification mismatch")
	}
	if target.calls != 1 {
		t.Fatalf("target Verify calls=%d, want exactly one exact restart observation", target.calls)
	}
}

func TestWorkerRestartAdoptionRejectsMutatedVerifyExpectation(t *testing.T) {
	fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptExecute)
	if _, err := fixture.service.Authorize(context.Background(), fixture.request); err != nil {
		t.Fatalf("execute recovery fixture: %v", err)
	}
	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	target := &recoveryMutatingExpectationTargetFake{}
	coordinator.target = target
	first, found, err := coordinator.ClaimNext(context.Background(), "recovery-worker-a")
	if err != nil || !found {
		t.Fatalf("claim recovery job: found=%t err=%v", found, err)
	}
	if _, err := coordinator.PrepareFirstWrite(context.Background(), first); err != nil {
		t.Fatalf("prepare durable sequence-zero write barrier: %v", err)
	}

	var job model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", first.JobID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	var item model.BackupAssetRecoveryJobItem
	if err := fixture.db.Where("job_id = ?", first.JobID).Order("ordinal ASC").Take(&item).Error; err != nil {
		t.Fatal(err)
	}
	fixture.now = first.LeaseExpiresAt.Add(time.Second)
	takeover, found, err := coordinator.TakeoverExpired(context.Background(), "recovery-worker-b")
	if err != nil || !found {
		t.Fatalf("take over interrupted recovery job: found=%t err=%v", found, err)
	}
	if err := fixture.db.Where("id = ?", takeover.JobID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Where("id = ?", item.ID).Take(&item).Error; err != nil {
		t.Fatal(err)
	}

	advance := recoveryTargetChainAdvanceForTest(t, job, item, takeover)
	_, err = coordinator.AdoptInterruptedOperation(context.Background(), takeover, item.ID)
	if !errors.Is(err, ErrInvalidTargetVerification) || errors.Is(err, ErrRecoveryWorkerFenceLost) {
		t.Errorf("mutated verification expectation error=%v, want target verification without fence loss", err)
	}
	if target.calls != 1 {
		t.Errorf("target Verify calls=%d, want one malicious observation", target.calls)
	}
	if target.expectation.Present == nil ||
		target.expectation.Present.IdentityDigest == advance.VerifiedIdentity ||
		target.expectation.Present.Bytes == item.EstimatedBytes {
		t.Errorf("target fake did not mutate both present expectation facts: %+v", target.expectation)
	}
	if target.observation.Present == nil || target.expectation.Present == nil ||
		target.observation.Present.IdentityDigest != target.expectation.Present.IdentityDigest ||
		target.observation.Present.Bytes != target.expectation.Present.Bytes {
		t.Errorf("target observation=%+v, want match for mutated expectation=%+v", target.observation, target.expectation)
	}
	assertRecoveryUnresolvedOutcomeProjection(
		t, fixture.db, takeover, job, UnresolvedOperationVerificationMismatch,
		SourceRevalidationMatched, 0,
	)
}

func TestWorkerRestartAdoptionRejectsInvalidExpectationBeforeTargetObservation(t *testing.T) {
	fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptExecute)
	if _, err := fixture.service.Authorize(context.Background(), fixture.request); err != nil {
		t.Fatalf("execute recovery fixture: %v", err)
	}
	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	target := coordinator.target.(*recoveryRestartTargetFake)
	first, found, err := coordinator.ClaimNext(context.Background(), "recovery-worker-a")
	if err != nil || !found {
		t.Fatalf("claim recovery job: found=%t err=%v", found, err)
	}
	if _, err := coordinator.PrepareFirstWrite(context.Background(), first); err != nil {
		t.Fatalf("prepare durable sequence-zero write barrier: %v", err)
	}

	var job model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", first.JobID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	var item model.BackupAssetRecoveryJobItem
	if err := fixture.db.Where("job_id = ?", first.JobID).Order("ordinal ASC").Take(&item).Error; err != nil {
		t.Fatal(err)
	}
	fixture.now = first.LeaseExpiresAt.Add(time.Second)
	takeover, found, err := coordinator.TakeoverExpired(context.Background(), "recovery-worker-b")
	if err != nil || !found {
		t.Fatalf("take over interrupted recovery job: found=%t err=%v", found, err)
	}

	if err := fixture.db.Model(&model.BackupAssetRecoveryJobItem{}).Where("id = ?", item.ID).
		UpdateColumn("expected_post_identity_digest", strings.ToUpper(item.ExpectedPostIdentityDigest)).Error; err != nil {
		t.Fatal(err)
	}
	_, err = coordinator.AdoptInterruptedOperation(context.Background(), takeover, item.ID)
	if !errors.Is(err, ErrRecoveryWorkerFenceLost) {
		t.Fatalf("invalid verification expectation error=%v, want ErrRecoveryWorkerFenceLost", err)
	}
	if target.calls != 0 {
		t.Fatalf("target Verify calls=%d, want zero for an invalid expectation", target.calls)
	}
}

func TestWorkerRestartAdoptionFailsClosedOnInvalidOrUncertainTargetObservation(t *testing.T) {
	t.Run("ambiguous missing with absent expectation", func(t *testing.T) {
		expectation := TargetVerifyExpectation{
			Kind:   TargetPresenceAbsent,
			Absent: &AbsentExpectation{},
		}
		observation := TargetVerifyObservation{
			Kind: TargetPresenceAbsent,
			Absent: &AbsentObservation{
				Evidence: TargetAbsenceEvidenceExact,
			},
			ObservedRevision: "target-revision-absence-1",
		}
		if err := observation.ValidateAgainst(expectation); err != nil {
			t.Fatalf("valid exact absence baseline rejected: %v", err)
		}

		observation.Absent = &AbsentObservation{
			Evidence: TargetAbsenceEvidenceKind("ambiguous_missing"),
		}
		if err := observation.ValidateAgainst(expectation); !errors.Is(err, ErrInvalidTargetVerification) {
			t.Fatalf("ambiguous absence evidence error=%v, want ErrInvalidTargetVerification", err)
		}
	})

	tests := []struct {
		name     string
		mutate   func(*TargetVerifyObservation)
		err      error
		category UnresolvedOperationCategory
	}{
		{
			name: "content identity mismatch", category: UnresolvedOperationVerificationMismatch,
			mutate: func(observation *TargetVerifyObservation) {
				observation.Present.IdentityDigest = strings.Repeat("d", sha256DigestLength)
			},
		},
		{
			name: "content bytes mismatch", category: UnresolvedOperationVerificationMismatch,
			mutate: func(observation *TargetVerifyObservation) {
				observation.Present.Bytes++
			},
		},
		{
			name: "wrong absence arm", category: UnresolvedOperationVerificationMismatch,
			mutate: func(observation *TargetVerifyObservation) {
				observedRevision := observation.ObservedRevision
				*observation = TargetVerifyObservation{
					Kind: TargetPresenceAbsent,
					Absent: &AbsentObservation{
						Evidence: TargetAbsenceEvidenceExact,
					},
					ObservedRevision: observedRevision,
				}
			},
		},
		{
			name: "both presence arms populated", category: UnresolvedOperationObservationInvalid,
			mutate: func(observation *TargetVerifyObservation) {
				observation.Absent = &AbsentObservation{
					Evidence: TargetAbsenceEvidenceExact,
				}
			},
		},
		{
			name: "no independently derived observation", category: UnresolvedOperationObservationInvalid,
			mutate: func(observation *TargetVerifyObservation) {
				observation.Present = nil
			},
		},
		{name: "permission denied missing", err: errors.New("permission denied while checking target"), category: UnresolvedOperationObservationInvalid},
		{name: "timeout missing", err: context.DeadlineExceeded, category: UnresolvedOperationObservationInvalid},
		{name: "unsupported stat missing", err: errors.New("target stat unsupported"), category: UnresolvedOperationObservationInvalid},
		{name: "transport failure missing", err: errors.New("target transport failed"), category: UnresolvedOperationObservationInvalid},
		{name: "unknown missing", err: errors.New("target missing state unknown"), category: UnresolvedOperationObservationInvalid},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptExecute)
			if _, err := fixture.service.Authorize(context.Background(), fixture.request); err != nil {
				t.Fatalf("execute recovery fixture: %v", err)
			}
			coordinator := newRecoveryWorkerCoordinator(t, fixture)
			target := &recoveryRestartTargetFake{err: testCase.err}
			coordinator.target = target

			first, found, err := coordinator.ClaimNext(context.Background(), "recovery-worker-a")
			if err != nil || !found {
				t.Fatalf("claim recovery job: found=%t err=%v", found, err)
			}
			if _, err := coordinator.PrepareFirstWrite(context.Background(), first); err != nil {
				t.Fatalf("prepare durable sequence-zero write barrier: %v", err)
			}
			var job model.BackupAssetRecoveryJob
			if err := fixture.db.Where("id = ?", first.JobID).Take(&job).Error; err != nil {
				t.Fatal(err)
			}
			var item model.BackupAssetRecoveryJobItem
			if err := fixture.db.Where("job_id = ?", first.JobID).Order("ordinal ASC").Take(&item).Error; err != nil {
				t.Fatal(err)
			}

			fixture.now = first.LeaseExpiresAt.Add(time.Second)
			takeover, found, err := coordinator.TakeoverExpired(context.Background(), "recovery-worker-b")
			if err != nil || !found {
				t.Fatalf("take over interrupted recovery job: found=%t err=%v", found, err)
			}
			if err := fixture.db.Where("id = ?", takeover.JobID).Take(&job).Error; err != nil {
				t.Fatal(err)
			}
			if err := fixture.db.Where("id = ?", item.ID).Take(&item).Error; err != nil {
				t.Fatal(err)
			}
			observation := TargetVerifyObservation{
				Kind: TargetPresencePresent,
				Present: &PresentObservation{
					IdentityDigest: item.ExpectedPostIdentityDigest,
					Bytes:          item.ExpectedPostBytes,
				},
				ObservedRevision: "target-revision-e",
			}
			if testCase.mutate != nil {
				testCase.mutate(&observation)
			}
			target.observation = observation

			_, err = coordinator.AdoptInterruptedOperation(context.Background(), takeover, item.ID)
			if !errors.Is(err, ErrInvalidTargetVerification) || errors.Is(err, ErrRecoveryWorkerFenceLost) {
				t.Fatalf("closed target verification error=%v, want target verification without fence loss", err)
			}
			if target.calls != 1 {
				t.Fatalf("target Verify calls=%d, want one independently returned observation", target.calls)
			}
			wantObject := TargetObjectRef{
				RootID:                 job.TargetRootID,
				RootLocatorDigest:      job.RootLocatorDigest,
				TargetPathDigest:       item.TargetObjectDigest,
				PrivateRelativeLocator: job.EncryptedWorkspaceRelativeLocator + "/items/item-0000",
			}
			if target.object != wantObject {
				t.Fatalf("target Verify object=%+v, want exact workspace object=%+v", target.object, wantObject)
			}
			wantExpectation := TargetVerifyExpectation{
				Kind: TargetPresencePresent,
				Present: &PresentExpectation{
					IdentityDigest: item.ExpectedPostIdentityDigest,
					Bytes:          item.ExpectedPostBytes,
				},
			}
			if !reflect.DeepEqual(target.expectation, wantExpectation) {
				t.Fatalf("target Verify expectation=%+v, want exact frozen expectation=%+v", target.expectation, wantExpectation)
			}
			assertRecoveryUnresolvedOutcomeProjection(
				t, fixture.db, takeover, job, testCase.category, SourceRevalidationMatched, 0,
			)
		})
	}
}

func TestWorkerConcurrentAdoptersProduceOneCheckpoint(t *testing.T) {
	fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptExecute)
	if _, err := fixture.service.Authorize(context.Background(), fixture.request); err != nil {
		t.Fatalf("execute recovery fixture: %v", err)
	}
	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	first, found, err := coordinator.ClaimNext(context.Background(), "recovery-worker-a")
	if err != nil || !found {
		t.Fatalf("claim recovery job: found=%t err=%v", found, err)
	}
	if _, err := coordinator.PrepareFirstWrite(context.Background(), first); err != nil {
		t.Fatalf("prepare durable sequence-zero write barrier: %v", err)
	}
	var initialJob model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", first.JobID).Take(&initialJob).Error; err != nil {
		t.Fatal(err)
	}
	var item model.BackupAssetRecoveryJobItem
	if err := fixture.db.Where("job_id = ?", first.JobID).Order("ordinal ASC").Take(&item).Error; err != nil {
		t.Fatal(err)
	}
	fixture.now = first.LeaseExpiresAt.Add(time.Second)
	takeover, takeoverFound, takeoverErr := coordinator.TakeoverExpired(context.Background(), "recovery-worker-b")
	if takeoverErr != nil || !takeoverFound {
		t.Fatalf("take over interrupted recovery job: found=%t err=%v", takeoverFound, takeoverErr)
	}
	adopter, ok := any(coordinator).(recoveryInterruptedOperationAdopter)
	if !ok {
		t.Fatal("recovery worker does not provide interrupted-mutation verify/adopt behavior")
	}
	sqlDB, err := fixture.db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)

	start := make(chan struct{})
	type result struct {
		checkpoint model.BackupAssetRecoveryCheckpoint
		err        error
	}
	results := make(chan result, 2)
	var callers sync.WaitGroup
	callers.Add(2)
	for range 2 {
		go func() {
			defer callers.Done()
			<-start
			checkpoint, err := adopter.AdoptInterruptedOperation(context.Background(), takeover, item.ID)
			results <- result{checkpoint: checkpoint, err: err}
		}()
	}
	close(start)
	callers.Wait()
	close(results)

	successes, fenceLosses := 0, 0
	var winner model.BackupAssetRecoveryCheckpoint
	for result := range results {
		switch {
		case result.err == nil:
			successes++
			winner = result.checkpoint
		case errors.Is(result.err, ErrRecoveryWorkerFenceLost):
			fenceLosses++
		default:
			t.Fatalf("concurrent adoption error=%v", result.err)
		}
	}
	if successes != 1 || fenceLosses != 1 {
		t.Fatalf("concurrent adoption outcomes successes=%d fence_losses=%d, want 1/1", successes, fenceLosses)
	}
	var checkpointCount int64
	if err := fixture.db.Model(&model.BackupAssetRecoveryCheckpoint{}).
		Where("job_id = ?", takeover.JobID).Count(&checkpointCount).Error; err != nil {
		t.Fatal(err)
	}
	if checkpointCount != 2 || winner.Sequence != 1 {
		t.Fatalf("concurrent adopters persisted checkpoints=%d winner=%+v", checkpointCount, winner)
	}
}

func TestWorkerAtomicAdoptionProjectionPostgres(t *testing.T) {
	fixture := newAuthorizationReceiptPostgresServiceFixture(t, AuthorizationReceiptExecute)
	executed, err := fixture.service.Authorize(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("execute recovery fixture: %v", err)
	}
	var itemCount int64
	if err := fixture.db.Model(&model.BackupAssetRecoveryJobItem{}).
		Where("job_id = ?", executed.JobID).Count(&itemCount).Error; err != nil {
		t.Fatal(err)
	}
	if itemCount != 1 {
		t.Fatalf("PostgreSQL adoption fixture job items=%d, want one terminal item", itemCount)
	}
	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	first, found, err := coordinator.ClaimNext(context.Background(), "recovery-worker-a")
	if err != nil || !found {
		t.Fatalf("claim recovery job: found=%t err=%v", found, err)
	}
	if _, err := coordinator.PrepareFirstWrite(context.Background(), first); err != nil {
		t.Fatalf("prepare durable sequence-zero write barrier: %v", err)
	}
	var initialJob model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", first.JobID).Take(&initialJob).Error; err != nil {
		t.Fatal(err)
	}
	var item model.BackupAssetRecoveryJobItem
	if err := fixture.db.Where("job_id = ?", first.JobID).Order("ordinal ASC").Take(&item).Error; err != nil {
		t.Fatal(err)
	}
	fixture.now = first.LeaseExpiresAt.Add(time.Second)
	takeover, takeoverFound, takeoverErr := coordinator.TakeoverExpired(context.Background(), "recovery-worker-b")
	if takeoverErr != nil || !takeoverFound {
		t.Fatalf("take over interrupted recovery job: found=%t err=%v", takeoverFound, takeoverErr)
	}
	adopter, ok := any(coordinator).(recoveryInterruptedOperationAdopter)
	if !ok {
		t.Fatal("recovery worker does not provide interrupted-mutation verify/adopt behavior")
	}
	checkpoint, err := adopter.AdoptInterruptedOperation(context.Background(), takeover, item.ID)
	if err != nil {
		t.Fatalf("adopt interrupted operation: %v", err)
	}
	if err := fixture.db.Where("id = ?", item.ID).Take(&item).Error; err != nil {
		t.Fatal(err)
	}
	if item.Outcome != "succeeded" || item.BytesWritten != item.ExpectedPostBytes ||
		item.VerifiedSize != item.ExpectedPostBytes || item.VerifiedDigest != item.ExpectedPostIdentityDigest ||
		item.FailureCategory != "" {
		t.Fatalf("PostgreSQL adoption item=%+v, want one complete terminal projection", item)
	}
	var attempt model.BackupAssetRecoveryAttempt
	if err := fixture.db.Where("id = ?", takeover.AttemptID).Take(&attempt).Error; err != nil {
		t.Fatal(err)
	}
	var pending int64
	if err := fixture.db.Model(&model.BackupAssetRecoveryJobItem{}).
		Where("job_id = ? AND outcome = '' AND failure_category = ''", takeover.JobID).Count(&pending).Error; err != nil {
		t.Fatal(err)
	}
	if pending != 0 || attempt.State != string(AttemptStateCompleted) || attempt.ClosedAt == nil {
		t.Fatalf("PostgreSQL terminal adoption pending/attempt=%d/%+v, want no pending item and completed attempt", pending, attempt)
	}
	projector, ok := any(coordinator).(recoveryOperationVerificationProjector)
	if !ok {
		t.Fatal("recovery worker does not expose the legacy verification projector")
	}
	if _, err := projector.ProjectOperationVerification(
		context.Background(), takeover, item.ID, checkpoint.ID, 7, 7, strings.Repeat("d", 64), fixture.now,
	); !errors.Is(err, ErrRecoveryWorkerFenceLost) {
		t.Fatalf("PostgreSQL adoption accepted a post-terminal verification rewrite: %v", err)
	}
	var job model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", takeover.JobID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	if job.State != string(JobStateSucceeded) || job.WorkspacePhase != string(WorkspacePhaseSealed) ||
		job.TargetChainRevision != checkpoint.NextTargetRevision ||
		job.TargetChainRevision == initialJob.TargetChainRevision {
		t.Fatalf("PostgreSQL atomic adoption job=%+v, want terminal verified chain advance", job)
	}
}

func TestWorkerConcurrentCancelersPostgresConvergeOnOneCleanupHandoff(t *testing.T) {
	fixture := newAuthorizationReceiptPostgresServiceFixture(t, AuthorizationReceiptExecute)
	if _, err := fixture.service.Authorize(context.Background(), fixture.request); err != nil {
		t.Fatalf("execute recovery fixture: %v", err)
	}
	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	claim, found, err := coordinator.ClaimNext(context.Background(), "recovery-worker-a")
	if err != nil || !found {
		t.Fatalf("claim recovery job: found=%t err=%v", found, err)
	}
	permit, err := coordinator.PrepareFirstWrite(context.Background(), claim)
	if err != nil {
		t.Fatalf("arm recovery mutation before cancellation: %v", err)
	}
	canceler, ok := any(coordinator).(recoveryWorkerCanceler)
	if !ok {
		t.Fatal("recovery worker does not provide an atomic cancellation boundary")
	}

	start := make(chan struct{})
	errorsByCaller := make(chan error, 2)
	var callers sync.WaitGroup
	callers.Add(2)
	for range 2 {
		go func() {
			defer callers.Done()
			<-start
			errorsByCaller <- canceler.CancelJob(context.Background(), claim.JobID)
		}()
	}
	close(start)
	callers.Wait()
	close(errorsByCaller)
	for err := range errorsByCaller {
		if err != nil {
			t.Fatalf("concurrent PostgreSQL cancellation error=%v", err)
		}
	}

	var job model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", claim.JobID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	if job.State != string(JobStateNeedsAttention) ||
		job.WorkspacePhase != string(WorkspacePhaseCleanupDue) ||
		job.TransitionRevision != claim.TransitionRevision+2 {
		t.Fatalf("concurrent PostgreSQL cancellation did not converge on one cleanup handoff: %+v", job)
	}
	if err := permit.ValidateAt(fixture.now); !errorsIsTargetPermit(err) {
		t.Fatalf("concurrent PostgreSQL cancellation retained a usable target permit: %v", err)
	}
}

func TestWorkerCancelExpiredQueuedJobPostgresReleasesStaleOwnership(t *testing.T) {
	fixture := newAuthorizationReceiptPostgresServiceFixture(t, AuthorizationReceiptExecute)
	executed, err := fixture.service.Authorize(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("execute recovery fixture: %v", err)
	}
	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	var attempt model.BackupAssetRecoveryAttempt
	if err := fixture.db.Where("job_id = ?", executed.JobID).Take(&attempt).Error; err != nil {
		t.Fatal(err)
	}
	if attempt.LeaseExpiresAt == nil {
		t.Fatal("queued PostgreSQL recovery attempt has no lease expiry")
	}
	fixture.now = attempt.LeaseExpiresAt.Add(time.Second)
	canceler, ok := any(coordinator).(recoveryWorkerCanceler)
	if !ok {
		t.Fatal("recovery worker does not provide an atomic cancellation boundary")
	}
	if err := canceler.CancelJob(context.Background(), executed.JobID); err != nil {
		t.Fatalf("cancel expired queued PostgreSQL recovery job: %v", err)
	}
	var job model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", executed.JobID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	if job.State != string(JobStateCanceled) || job.WorkspacePhase != string(WorkspacePhaseNone) {
		t.Fatalf("expired queued PostgreSQL cancellation changed target state: %+v", job)
	}
	var activeSource, activeNode int64
	if err := fixture.db.Model(&model.RecoveryPointLease{}).
		Where("holder_type = ? AND owner_id = ? AND status = ?", backupasset.LeaseHolderRecoveryJob, executed.JobID, backupasset.LeaseActive).
		Count(&activeSource).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.BackupAssetRecoveryNodeLease{}).
		Where("job_id = ? AND state = ?", executed.JobID, "active").Count(&activeNode).Error; err != nil {
		t.Fatal(err)
	}
	if activeSource != 0 || activeNode != 0 {
		t.Fatalf("expired queued PostgreSQL cancellation retained source/node leases=%d/%d", activeSource, activeNode)
	}
}

func TestWorkerAdoptionAtomicallyProjectsVerificationAndRejectsPostTerminalRewrite(t *testing.T) {
	fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptExecute)
	if _, err := fixture.service.Authorize(context.Background(), fixture.request); err != nil {
		t.Fatalf("execute recovery fixture: %v", err)
	}
	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	first, found, err := coordinator.ClaimNext(context.Background(), "recovery-worker-a")
	if err != nil || !found {
		t.Fatalf("claim recovery job: found=%t err=%v", found, err)
	}
	if _, err := coordinator.PrepareFirstWrite(context.Background(), first); err != nil {
		t.Fatalf("prepare durable sequence-zero write barrier: %v", err)
	}
	var initialJob model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", first.JobID).Take(&initialJob).Error; err != nil {
		t.Fatal(err)
	}
	var item model.BackupAssetRecoveryJobItem
	if err := fixture.db.Where("job_id = ?", first.JobID).Order("ordinal ASC").Take(&item).Error; err != nil {
		t.Fatal(err)
	}
	fixture.now = first.LeaseExpiresAt.Add(time.Second)
	takeover, takeoverFound, takeoverErr := coordinator.TakeoverExpired(context.Background(), "recovery-worker-b")
	if takeoverErr != nil || !takeoverFound {
		t.Fatalf("take over interrupted recovery job: found=%t err=%v", takeoverFound, takeoverErr)
	}
	adopter, ok := any(coordinator).(recoveryInterruptedOperationAdopter)
	if !ok {
		t.Fatal("recovery worker does not provide interrupted-mutation verify/adopt behavior")
	}
	checkpoint, err := adopter.AdoptInterruptedOperation(context.Background(), takeover, item.ID)
	if err != nil {
		t.Fatalf("adopt interrupted operation: %v", err)
	}
	if err := fixture.db.Where("id = ?", item.ID).Take(&item).Error; err != nil {
		t.Fatal(err)
	}
	if item.Outcome != "succeeded" || item.BytesWritten != item.ExpectedPostBytes ||
		item.VerifiedSize != item.ExpectedPostBytes || item.VerifiedDigest != item.ExpectedPostIdentityDigest ||
		item.FailureCategory != "" {
		t.Fatalf("adopted job-item projection=%+v, want one complete terminal projection", item)
	}
	var attempt model.BackupAssetRecoveryAttempt
	if err := fixture.db.Where("id = ?", takeover.AttemptID).Take(&attempt).Error; err != nil {
		t.Fatal(err)
	}
	if attempt.State != string(AttemptStateRunning) || attempt.ClosedAt != nil {
		t.Fatalf("adopted attempt=%+v, want active continuation attempt", attempt)
	}

	projector, ok := any(coordinator).(recoveryOperationVerificationProjector)
	if !ok {
		t.Fatal("recovery worker does not expose the ordinary verification projector")
	}
	fixture.now = fixture.now.Add(time.Second)
	verifiedAt := fixture.now
	verifiedDigest := strings.Repeat("d", 64)
	if _, err := projector.ProjectOperationVerification(
		context.Background(), takeover, item.ID, checkpoint.ID, 7, 7, verifiedDigest, verifiedAt,
	); !errors.Is(err, ErrRecoveryWorkerFenceLost) {
		t.Fatalf("adoption accepted a post-terminal verification rewrite: %v", err)
	}

	stale := takeover
	stale.AttemptFence++
	if _, err := projector.ProjectOperationVerification(
		context.Background(), stale, item.ID, checkpoint.ID, 7, 7, verifiedDigest, verifiedAt,
	); !errors.Is(err, ErrRecoveryWorkerFenceLost) {
		t.Fatalf("stale projection error=%v, want fence loss", err)
	}
	var evidenceCount int64
	if err := fixture.db.Model(&model.BackupAssetRecoveryEvidence{}).
		Where("job_id = ? AND kind = ?", takeover.JobID, "verification").Count(&evidenceCount).Error; err != nil {
		t.Fatal(err)
	}
	if evidenceCount != 0 {
		t.Fatalf("post-terminal projection added evidence rows=%d, want zero", evidenceCount)
	}
	var job model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", takeover.JobID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	if job.TargetChainRevision != checkpoint.NextTargetRevision ||
		job.TargetChainRevision == initialJob.TargetChainRevision {
		t.Fatalf("atomic adoption job=%+v, want only the verified checkpoint chain advance", job)
	}
}

func TestWorkerAdoptionRejectsPostTerminalMismatchRewrite(t *testing.T) {
	fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptExecute)
	if _, err := fixture.service.Authorize(context.Background(), fixture.request); err != nil {
		t.Fatalf("execute recovery fixture: %v", err)
	}
	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	first, found, err := coordinator.ClaimNext(context.Background(), "recovery-worker-a")
	if err != nil || !found {
		t.Fatalf("claim recovery job: found=%t err=%v", found, err)
	}
	if _, err := coordinator.PrepareFirstWrite(context.Background(), first); err != nil {
		t.Fatalf("prepare durable sequence-zero write barrier: %v", err)
	}
	var item model.BackupAssetRecoveryJobItem
	if err := fixture.db.Where("job_id = ?", first.JobID).Order("ordinal ASC").Take(&item).Error; err != nil {
		t.Fatal(err)
	}
	fixture.now = first.LeaseExpiresAt.Add(time.Second)
	takeover, takeoverFound, takeoverErr := coordinator.TakeoverExpired(context.Background(), "recovery-worker-b")
	if takeoverErr != nil || !takeoverFound {
		t.Fatalf("take over interrupted recovery job: found=%t err=%v", takeoverFound, takeoverErr)
	}
	adopter, ok := any(coordinator).(recoveryInterruptedOperationAdopter)
	if !ok {
		t.Fatal("recovery worker does not provide interrupted-mutation verify/adopt behavior")
	}
	checkpoint, err := adopter.AdoptInterruptedOperation(context.Background(), takeover, item.ID)
	if err != nil {
		t.Fatalf("adopt interrupted operation: %v", err)
	}

	projector, ok := any(coordinator).(recoveryOperationMismatchProjector)
	if !ok {
		t.Fatal("recovery worker does not project verification mismatches")
	}
	fixture.now = fixture.now.Add(time.Second)
	verifiedAt := fixture.now
	verifiedDigest := strings.Repeat("e", 64)
	_, err = projector.ProjectOperationMismatch(
		context.Background(), takeover, item.ID, checkpoint.ID, 7, 6, verifiedDigest, verifiedAt, 1,
	)
	if !errors.Is(err, ErrRecoveryWorkerFenceLost) {
		t.Fatalf("adoption accepted a post-terminal mismatch rewrite: %v", err)
	}
	if err := fixture.db.Where("id = ?", item.ID).Take(&item).Error; err != nil {
		t.Fatal(err)
	}
	if item.Outcome != "succeeded" || item.BytesWritten != item.ExpectedPostBytes ||
		item.VerifiedSize != item.ExpectedPostBytes || item.VerifiedDigest != item.ExpectedPostIdentityDigest ||
		item.FailureCategory != "" {
		t.Fatalf("post-terminal mismatch changed adopted item=%+v", item)
	}
	var job model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", takeover.JobID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	if job.State != string(JobStateRunning) || job.FailureCategory != "" ||
		job.WorkspacePhase != string(WorkspacePhaseWriting) || job.TargetChainRevision != checkpoint.NextTargetRevision {
		t.Fatalf("post-terminal mismatch changed adopted job=%+v", job)
	}
	var attempt model.BackupAssetRecoveryAttempt
	if err := fixture.db.Where("id = ?", takeover.AttemptID).Take(&attempt).Error; err != nil {
		t.Fatal(err)
	}
	if attempt.State != string(AttemptStateRunning) || attempt.ClosedAt != nil {
		t.Fatalf("post-adoption mismatch changed continuation attempt=%+v", attempt)
	}
	if _, err := coordinator.PrepareFirstWrite(context.Background(), takeover); err != nil {
		t.Fatalf("post-adoption mismatch blocked continuation permit: %v", err)
	}
	var evidenceCount int64
	if err := fixture.db.Model(&model.BackupAssetRecoveryEvidence{}).
		Where("job_id = ? AND kind = ?", takeover.JobID, "difference").Count(&evidenceCount).Error; err != nil {
		t.Fatal(err)
	}
	if evidenceCount != 0 {
		t.Fatalf("post-terminal mismatch added difference evidence rows=%d, want zero", evidenceCount)
	}
}

func TestWorkerCancelAfterMutationArmRevokesPermitAndHandsWorkspaceToCleanup(t *testing.T) {
	fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptExecute)
	if _, err := fixture.service.Authorize(context.Background(), fixture.request); err != nil {
		t.Fatalf("execute recovery fixture: %v", err)
	}
	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	claim, found, err := coordinator.ClaimNext(context.Background(), "recovery-worker-a")
	if err != nil || !found {
		t.Fatalf("claim recovery job: found=%t err=%v", found, err)
	}
	permit, err := coordinator.PrepareFirstWrite(context.Background(), claim)
	if err != nil {
		t.Fatalf("arm recovery mutation before cancellation: %v", err)
	}

	var before model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", claim.JobID).Take(&before).Error; err != nil {
		t.Fatal(err)
	}
	if before.WorkspacePhase != string(WorkspacePhaseMarkerCreated) {
		t.Fatalf("armed workspace phase=%q, want durable marker", before.WorkspacePhase)
	}

	canceler, ok := any(coordinator).(recoveryWorkerCanceler)
	if !ok {
		t.Fatal("recovery worker does not provide an atomic cancellation boundary")
	}
	if err := canceler.CancelJob(context.Background(), claim.JobID); err != nil {
		t.Fatalf("cancel armed recovery job: %v", err)
	}
	if err := permit.ValidateAt(fixture.now); !errorsIsTargetPermit(err) {
		t.Fatalf("canceled job retained a usable target write permit: %v", err)
	}
	if _, err := coordinator.PrepareFirstWrite(context.Background(), claim); !errors.Is(err, ErrRecoveryWorkerFenceLost) {
		t.Fatalf("canceled worker minted a later target permit: %v", err)
	}

	var job model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", claim.JobID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	if job.State != string(JobStateNeedsAttention) || job.WorkspacePhase != string(WorkspacePhaseCleanupDue) ||
		job.EncryptedWorkspaceRelativeLocator != before.EncryptedWorkspaceRelativeLocator ||
		job.WorkspaceMarkerBindingDigest != before.WorkspaceMarkerBindingDigest || job.PlaintextDeadline == nil {
		t.Fatalf("canceled armed job=%+v, want durable cleanup handoff without workspace loss", job)
	}
	var checkpointCount int64
	if err := fixture.db.Model(&model.BackupAssetRecoveryCheckpoint{}).
		Where("job_id = ?", claim.JobID).Count(&checkpointCount).Error; err != nil {
		t.Fatal(err)
	}
	if checkpointCount != 1 {
		t.Fatalf("cancellation changed durable checkpoint count=%d, want sequence-zero reservation only", checkpointCount)
	}
	var attempt model.BackupAssetRecoveryAttempt
	if err := fixture.db.Where("id = ?", claim.AttemptID).Take(&attempt).Error; err != nil {
		t.Fatal(err)
	}
	if attempt.State != string(AttemptStateCanceled) || attempt.ClosedAt == nil {
		t.Fatalf("canceled attempt=%+v, want closed cancellation", attempt)
	}
	var activeSource, activeNode int64
	if err := fixture.db.Model(&model.RecoveryPointLease{}).
		Where("id = ? AND status = ?", claim.SourceFence.LeaseID, backupasset.LeaseActive).Count(&activeSource).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.BackupAssetRecoveryNodeLease{}).
		Where("id = ? AND state = ?", claim.NodeLeaseID, "active").Count(&activeNode).Error; err != nil {
		t.Fatal(err)
	}
	if activeSource != 0 || activeNode != 0 {
		t.Fatalf("canceled worker retained active source/node leases=%d/%d", activeSource, activeNode)
	}
}

func TestWorkerCancelRetryPreservesDurableCleanupHandoff(t *testing.T) {
	fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptExecute)
	if _, err := fixture.service.Authorize(context.Background(), fixture.request); err != nil {
		t.Fatalf("execute recovery fixture: %v", err)
	}
	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	claim, found, err := coordinator.ClaimNext(context.Background(), "recovery-worker-a")
	if err != nil || !found {
		t.Fatalf("claim recovery job: found=%t err=%v", found, err)
	}
	permit, err := coordinator.PrepareFirstWrite(context.Background(), claim)
	if err != nil {
		t.Fatalf("arm recovery mutation before cancellation: %v", err)
	}
	canceler, ok := any(coordinator).(recoveryWorkerCanceler)
	if !ok {
		t.Fatal("recovery worker does not provide an atomic cancellation boundary")
	}
	if err := canceler.CancelJob(context.Background(), claim.JobID); err != nil {
		t.Fatalf("first cancellation: %v", err)
	}
	var first model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", claim.JobID).Take(&first).Error; err != nil {
		t.Fatal(err)
	}
	if err := canceler.CancelJob(context.Background(), claim.JobID); err != nil {
		t.Fatalf("retry cancellation after durable handoff: %v", err)
	}
	var retried model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", claim.JobID).Take(&retried).Error; err != nil {
		t.Fatal(err)
	}
	if retried.State != first.State || retried.TransitionRevision != first.TransitionRevision ||
		retried.WorkspacePhase != first.WorkspacePhase || retried.FailureCategory != first.FailureCategory {
		t.Fatalf("cancellation retry rewrote durable handoff: first=%+v retried=%+v", first, retried)
	}
	if err := permit.ValidateAt(fixture.now); !errorsIsTargetPermit(err) {
		t.Fatalf("cancellation retry made old target permit usable: %v", err)
	}
}

func TestWorkerConcurrentCancelersConvergeOnOneCleanupHandoff(t *testing.T) {
	fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptExecute)
	if _, err := fixture.service.Authorize(context.Background(), fixture.request); err != nil {
		t.Fatalf("execute recovery fixture: %v", err)
	}
	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	claim, found, err := coordinator.ClaimNext(context.Background(), "recovery-worker-a")
	if err != nil || !found {
		t.Fatalf("claim recovery job: found=%t err=%v", found, err)
	}
	permit, err := coordinator.PrepareFirstWrite(context.Background(), claim)
	if err != nil {
		t.Fatalf("arm recovery mutation before cancellation: %v", err)
	}
	canceler, ok := any(coordinator).(recoveryWorkerCanceler)
	if !ok {
		t.Fatal("recovery worker does not provide an atomic cancellation boundary")
	}
	sqlDB, err := fixture.db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)

	start := make(chan struct{})
	errorsByCaller := make(chan error, 2)
	var callers sync.WaitGroup
	callers.Add(2)
	for range 2 {
		go func() {
			defer callers.Done()
			<-start
			errorsByCaller <- canceler.CancelJob(context.Background(), claim.JobID)
		}()
	}
	close(start)
	callers.Wait()
	close(errorsByCaller)
	for err := range errorsByCaller {
		if err != nil {
			t.Fatalf("concurrent cancellation error=%v", err)
		}
	}

	var job model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", claim.JobID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	if job.State != string(JobStateNeedsAttention) ||
		job.WorkspacePhase != string(WorkspacePhaseCleanupDue) ||
		job.TransitionRevision != claim.TransitionRevision+2 {
		t.Fatalf("concurrent cancellation did not converge on one cleanup handoff: %+v", job)
	}
	if err := permit.ValidateAt(fixture.now); !errorsIsTargetPermit(err) {
		t.Fatalf("concurrent cancellation retained a usable target permit: %v", err)
	}
}

func TestWorkerCancelRollsBackLateTerminalWriteFailure(t *testing.T) {
	fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptExecute)
	if _, err := fixture.service.Authorize(context.Background(), fixture.request); err != nil {
		t.Fatalf("execute recovery fixture: %v", err)
	}
	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	metrics := &recoveryMetricsSpy{}
	coordinator.metrics = metrics
	claim, found, err := coordinator.ClaimNext(context.Background(), "recovery-worker-a")
	if err != nil || !found {
		t.Fatalf("claim recovery job: found=%t err=%v", found, err)
	}
	permit, err := coordinator.PrepareFirstWrite(context.Background(), claim)
	if err != nil {
		t.Fatalf("arm recovery mutation before cancellation: %v", err)
	}
	canceler, ok := any(coordinator).(recoveryWorkerCanceler)
	if !ok {
		t.Fatal("recovery worker does not provide an atomic cancellation boundary")
	}

	const callback = "recovery:cancel-late-terminal-write-failure"
	injected := false
	if err := fixture.db.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table != (model.BackupAssetRecoveryJob{}).TableName() || injected {
			return
		}
		injected = true
		_ = tx.AddError(errors.New("inject late cancellation terminal-write failure"))
	}); err != nil {
		t.Fatalf("register late cancellation failure: %v", err)
	}
	t.Cleanup(func() { _ = fixture.db.Callback().Update().Remove(callback) })
	if err := canceler.CancelJob(context.Background(), claim.JobID); !errors.Is(err, ErrRecoveryWorkerUnavailable) {
		t.Fatalf("cancel with injected late terminal-write failure error=%v", err)
	}
	if !injected {
		t.Fatal("late cancellation terminal-write fault was not injected")
	}
	if err := permit.ValidateAt(fixture.now); err != nil {
		t.Fatalf("rolled-back cancellation invalidated current permit: %v", err)
	}
	var job model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", claim.JobID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	if job.State != string(JobStateRunning) || job.WorkspacePhase != string(WorkspacePhaseMarkerCreated) {
		t.Fatalf("rolled-back cancellation changed job=%+v", job)
	}
	var activeSource, activeNode int64
	if err := fixture.db.Model(&model.RecoveryPointLease{}).
		Where("id = ? AND status = ?", claim.SourceFence.LeaseID, backupasset.LeaseActive).Count(&activeSource).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.BackupAssetRecoveryNodeLease{}).
		Where("id = ? AND state = ?", claim.NodeLeaseID, "active").Count(&activeNode).Error; err != nil {
		t.Fatal(err)
	}
	if activeSource != 1 || activeNode != 1 {
		t.Fatalf("rolled-back cancellation released source/node leases=%d/%d", activeSource, activeNode)
	}
	if len(metrics.outcomes) != 0 {
		t.Fatalf("rolled-back cancellation emitted terminal metrics=%+v", metrics.outcomes)
	}
}

func TestWorkerCancelPersistsHandoffAfterCallerContextCancellation(t *testing.T) {
	fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptExecute)
	if _, err := fixture.service.Authorize(context.Background(), fixture.request); err != nil {
		t.Fatalf("execute recovery fixture: %v", err)
	}
	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	claim, found, err := coordinator.ClaimNext(context.Background(), "recovery-worker-a")
	if err != nil || !found {
		t.Fatalf("claim recovery job: found=%t err=%v", found, err)
	}
	permit, err := coordinator.PrepareFirstWrite(context.Background(), claim)
	if err != nil {
		t.Fatalf("arm recovery mutation before cancellation: %v", err)
	}
	canceler, ok := any(coordinator).(recoveryWorkerCanceler)
	if !ok {
		t.Fatal("recovery worker does not provide an atomic cancellation boundary")
	}
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := canceler.CancelJob(canceledCtx, claim.JobID); err != nil {
		t.Fatalf("cancel armed job after caller context cancellation: %v", err)
	}
	if err := permit.ValidateAt(fixture.now); !errorsIsTargetPermit(err) {
		t.Fatalf("canceled caller context retained a usable target permit: %v", err)
	}
	var job model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", claim.JobID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	if job.State != string(JobStateNeedsAttention) || job.WorkspacePhase != string(WorkspacePhaseCleanupDue) {
		t.Fatalf("canceled caller context did not persist cleanup handoff: %+v", job)
	}
}

func TestWorkerCancelQueuedJobDoesNotCreateTargetMutationState(t *testing.T) {
	fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptExecute)
	executed, err := fixture.service.Authorize(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("execute recovery fixture: %v", err)
	}
	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	canceler, ok := any(coordinator).(recoveryWorkerCanceler)
	if !ok {
		t.Fatal("recovery worker does not provide an atomic cancellation boundary")
	}
	if err := canceler.CancelJob(context.Background(), executed.JobID); err != nil {
		t.Fatalf("cancel queued recovery job: %v", err)
	}

	var job model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", executed.JobID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	var plan model.BackupAssetRecoveryPlan
	if err := fixture.db.Where("id = ?", job.PlanID).Take(&plan).Error; err != nil {
		t.Fatal(err)
	}
	wantWorkspace := "jobs/" + job.ID
	if job.State != string(JobStateCanceled) || job.WorkspacePhase != string(WorkspacePhaseNone) ||
		job.EncryptedWorkspaceRelativeLocator != wantWorkspace ||
		job.WorkspaceBindingDigest != recoveryWorkspaceBindingDigest(plan, job.ID, wantWorkspace) ||
		job.WorkspaceMarkerBindingDigest != "" ||
		job.PlaintextDeadline != nil {
		t.Fatalf("queued cancellation created target state: %+v", job)
	}
	var checkpointCount, latchCount int64
	if err := fixture.db.Model(&model.BackupAssetRecoveryCheckpoint{}).
		Where("job_id = ?", executed.JobID).Count(&checkpointCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.BackupAssetRecoveryEvidence{}).
		Where("id = ?", recoverySchemaUseLatchRowID).Count(&latchCount).Error; err != nil {
		t.Fatal(err)
	}
	if checkpointCount != 0 || latchCount != 0 {
		t.Fatalf("queued cancellation wrote target mutation state: checkpoints=%d latch=%d", checkpointCount, latchCount)
	}
	if _, found, err := coordinator.ClaimNext(context.Background(), "recovery-worker-a"); err != nil || found {
		t.Fatalf("canceled queued job remained claimable: found=%t err=%v", found, err)
	}
}

func TestWorkerCancelOwnedJobRejectsRevisionDriftInsideLockedBoundary(t *testing.T) {
	fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptExecute)
	executed, err := fixture.service.Authorize(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("execute recovery fixture: %v", err)
	}
	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	audit := &recoveryAPIAuditSpy{err: errors.New("FAKE_CANCEL_AUDIT_FAILURE_FOR_TEST_ONLY")}
	coordinator.audit = audit
	var job model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", executed.JobID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	staleRevision := job.TransitionRevision
	if err := fixture.db.Model(&model.BackupAssetRecoveryJob{}).Where("id = ?", job.ID).
		UpdateColumn("transition_revision", staleRevision+1).Error; err != nil {
		t.Fatal(err)
	}
	if err := coordinator.CancelOwnedJob(context.Background(), CancelRecoveryJobRequest{
		RequesterID: fixture.request.RequesterID, JobID: job.ID, ExpectedRevision: staleRevision,
	}); !errors.Is(err, ErrRecoveryWorkerFenceLost) {
		t.Fatalf("drifted cancellation error=%v, want ErrRecoveryWorkerFenceLost", err)
	}
	var after model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", job.ID).Take(&after).Error; err != nil {
		t.Fatal(err)
	}
	if after.State != string(JobStateQueued) || after.TransitionRevision != staleRevision+1 {
		t.Fatalf("drifted cancellation mutated job=%+v", after)
	}
	if len(audit.events) != 0 {
		t.Fatalf("drifted cancellation emitted success audit: %+v", audit.events)
	}
	if err := coordinator.CancelOwnedJob(context.Background(), CancelRecoveryJobRequest{
		RequesterID: fixture.request.RequesterID, JobID: job.ID, ExpectedRevision: after.TransitionRevision,
	}); err != nil {
		t.Fatalf("committed cancellation changed response on audit failure: %v", err)
	}
	if len(audit.events) != 1 || audit.events[0].Action != backupasset.AuditActionRecoveryCancel ||
		audit.events[0].Actor.UserID != fixture.request.RequesterID || audit.events[0].RecoveryJobID != job.ID ||
		audit.events[0].GrantID != "" || audit.events[0].StepUpProofID != "" {
		t.Fatalf("cancel audit=%+v", audit.events)
	}
}

func TestWorkerCancelOwnedJobHidesMissingAndForeignJobs(t *testing.T) {
	fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptExecute)
	executed, err := fixture.service.Authorize(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("execute recovery fixture: %v", err)
	}
	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	var job model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", executed.JobID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	for _, request := range []CancelRecoveryJobRequest{
		{RequesterID: fixture.request.RequesterID, JobID: strings.Repeat("f", 32), ExpectedRevision: 1},
		{RequesterID: fixture.request.RequesterID + 1, JobID: job.ID, ExpectedRevision: job.TransitionRevision},
	} {
		if err := coordinator.CancelOwnedJob(context.Background(), request); !errors.Is(err, ErrRecoveryWorkerObjectNotFound) {
			t.Fatalf("hidden cancellation error=%v, want ErrRecoveryWorkerObjectNotFound", err)
		}
	}
	var after model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", job.ID).Take(&after).Error; err != nil {
		t.Fatal(err)
	}
	if after.State != job.State || after.TransitionRevision != job.TransitionRevision {
		t.Fatalf("hidden cancellation mutated job before=%+v after=%+v", job, after)
	}
}

func TestWorkerCancelExpiredQueuedJobReleasesStaleOwnershipWithoutTargetState(t *testing.T) {
	fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptExecute)
	executed, err := fixture.service.Authorize(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("execute recovery fixture: %v", err)
	}
	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	var attempt model.BackupAssetRecoveryAttempt
	if err := fixture.db.Where("job_id = ?", executed.JobID).Take(&attempt).Error; err != nil {
		t.Fatal(err)
	}
	if attempt.LeaseExpiresAt == nil {
		t.Fatal("queued recovery attempt has no lease expiry")
	}
	fixture.now = attempt.LeaseExpiresAt.Add(time.Second)
	canceler, ok := any(coordinator).(recoveryWorkerCanceler)
	if !ok {
		t.Fatal("recovery worker does not provide an atomic cancellation boundary")
	}
	if err := canceler.CancelJob(context.Background(), executed.JobID); err != nil {
		t.Fatalf("cancel expired queued recovery job: %v", err)
	}

	var job model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", executed.JobID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	if job.State != string(JobStateCanceled) || job.WorkspacePhase != string(WorkspacePhaseNone) {
		t.Fatalf("expired queued cancellation changed target state: %+v", job)
	}
	if err := fixture.db.Where("id = ?", attempt.ID).Take(&attempt).Error; err != nil {
		t.Fatal(err)
	}
	if attempt.State != string(AttemptStateCanceled) || attempt.ClosedAt == nil {
		t.Fatalf("expired queued attempt=%+v, want closed cancellation", attempt)
	}
	var activeSource, activeNode, checkpointCount, latchCount int64
	if err := fixture.db.Model(&model.RecoveryPointLease{}).
		Where("holder_type = ? AND owner_id = ? AND status = ?", backupasset.LeaseHolderRecoveryJob, executed.JobID, backupasset.LeaseActive).
		Count(&activeSource).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.BackupAssetRecoveryNodeLease{}).
		Where("job_id = ? AND state = ?", executed.JobID, "active").Count(&activeNode).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.BackupAssetRecoveryCheckpoint{}).
		Where("job_id = ?", executed.JobID).Count(&checkpointCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.BackupAssetRecoveryEvidence{}).
		Where("id = ?", recoverySchemaUseLatchRowID).Count(&latchCount).Error; err != nil {
		t.Fatal(err)
	}
	if activeSource != 0 || activeNode != 0 || checkpointCount != 0 || latchCount != 0 {
		t.Fatalf("expired queued cancellation retained ownership or target state: source=%d node=%d checkpoints=%d latch=%d",
			activeSource, activeNode, checkpointCount, latchCount)
	}
}

func TestWorkerClosedAdoptionAttemptRejectsStaleCancellationAndPreservesCheckpoint(t *testing.T) {
	fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptExecute)
	if _, err := fixture.service.Authorize(context.Background(), fixture.request); err != nil {
		t.Fatalf("execute recovery fixture: %v", err)
	}
	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	first, found, err := coordinator.ClaimNext(context.Background(), "recovery-worker-a")
	if err != nil || !found {
		t.Fatalf("claim recovery job: found=%t err=%v", found, err)
	}
	if _, err := coordinator.PrepareFirstWrite(context.Background(), first); err != nil {
		t.Fatalf("prepare durable sequence-zero write barrier: %v", err)
	}
	var initialJob model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", first.JobID).Take(&initialJob).Error; err != nil {
		t.Fatal(err)
	}
	var items []model.BackupAssetRecoveryJobItem
	if err := fixture.db.Where("job_id = ?", first.JobID).Order("ordinal ASC").Find(&items).Error; err != nil {
		t.Fatal(err)
	}
	if len(items) == 0 {
		t.Fatal("closed adoption fixture has no job items")
	}
	fixture.now = first.LeaseExpiresAt.Add(time.Second)
	takeover, takeoverFound, takeoverErr := coordinator.TakeoverExpired(context.Background(), "recovery-worker-b")
	if takeoverErr != nil || !takeoverFound {
		t.Fatalf("take over interrupted recovery job: found=%t err=%v", takeoverFound, takeoverErr)
	}
	adopter, ok := any(coordinator).(recoveryInterruptedOperationAdopter)
	if !ok {
		t.Fatal("recovery worker does not provide interrupted-mutation verify/adopt behavior")
	}
	target, ok := coordinator.target.(*recoveryRestartTargetFake)
	if !ok {
		t.Fatalf("closed adoption fixture target=%T, want recoveryRestartTargetFake", coordinator.target)
	}
	var checkpoint model.BackupAssetRecoveryCheckpoint
	for index := range items {
		target.mu.Lock()
		target.observation = TargetVerifyObservation{}
		target.mu.Unlock()
		checkpoint, err = adopter.AdoptInterruptedOperation(context.Background(), takeover, items[index].ID)
		if err != nil {
			t.Fatalf("adopt interrupted operation %d: %v", index, err)
		}
	}
	canceler, ok := any(coordinator).(recoveryWorkerCanceler)
	if !ok {
		t.Fatal("recovery worker does not provide an atomic cancellation boundary")
	}
	if err := canceler.CancelJob(context.Background(), takeover.JobID); !errors.Is(err, ErrRecoveryWorkerFenceLost) {
		t.Fatalf("closed adopted attempt accepted stale cancellation: %v", err)
	}

	var job model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", takeover.JobID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	if job.State != string(JobStateSucceeded) || job.FailureCategory != "" ||
		job.WorkspacePhase != string(WorkspacePhaseSealed) ||
		job.WorkspaceOwner != initialJob.WorkspaceOwner || job.WorkspaceFence != initialJob.WorkspaceFence ||
		job.WorkspaceMarkerBindingDigest != initialJob.WorkspaceMarkerBindingDigest ||
		job.TargetChainRevision != checkpoint.NextTargetRevision {
		t.Fatalf("stale post-adoption cancellation changed job=%+v", job)
	}
	if err := fixture.db.Where("job_id = ?", takeover.JobID).Order("ordinal ASC").Find(&items).Error; err != nil {
		t.Fatal(err)
	}
	for index := range items {
		wantOutcome := "succeeded"
		if RecoveryOperationKind(items[index].OperationKind) == RecoveryOperationSkip {
			wantOutcome = "skipped"
		}
		if items[index].Outcome != wantOutcome || items[index].FailureCategory != "" {
			t.Fatalf("post-write cancellation rewrote item %d projection=%+v", index, items[index])
		}
	}
	var persisted model.BackupAssetRecoveryCheckpoint
	if err := fixture.db.Where("id = ?", checkpoint.ID).Take(&persisted).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.Sequence != len(items) || persisted.NextTargetRevision != checkpoint.NextTargetRevision {
		t.Fatalf("stale post-adoption cancellation changed operation checkpoint=%+v", persisted)
	}
	var attempt model.BackupAssetRecoveryAttempt
	if err := fixture.db.Where("id = ?", takeover.AttemptID).Take(&attempt).Error; err != nil {
		t.Fatal(err)
	}
	if attempt.State != string(AttemptStateCompleted) || attempt.ClosedAt == nil {
		t.Fatalf("stale cancellation changed adopted attempt=%+v", attempt)
	}
	var source model.RecoveryPointLease
	if err := fixture.db.Where("id = ?", takeover.SourceFence.LeaseID).Take(&source).Error; err != nil {
		t.Fatal(err)
	}
	var node model.BackupAssetRecoveryNodeLease
	if err := fixture.db.Where("id = ?", takeover.NodeLeaseID).Take(&node).Error; err != nil {
		t.Fatal(err)
	}
	if source.Status != string(backupasset.LeaseReleased) || source.ReleasedAt == nil ||
		node.State != "released" || node.ReleasedAt == nil {
		t.Fatalf("last-pending adoption leases source=%+v node=%+v, want released", source, node)
	}
	if _, err := coordinator.PrepareFirstWrite(context.Background(), takeover); !errors.Is(err, ErrRecoveryWorkerFenceLost) {
		t.Fatalf("post-write cancellation minted a later target permit: %v", err)
	}
}

func recoveryJobItemOperationDigestForTest(item model.BackupAssetRecoveryJobItem) string {
	planItemID := ""
	if item.PlanItemID != nil {
		planItemID = *item.PlanItemID
	}
	return framedDigest(
		"xirang/recovery/operation-row/v1",
		item.ID,
		item.PlanID,
		item.JobID,
		planItemID,
		fmt.Sprintf("%d", item.Ordinal),
		item.OperationKind,
		item.TargetPathDigest,
		item.SemanticTargetDigest,
		item.TargetObjectDigest,
		item.ExpectedPriorKind,
		item.ExpectedPriorDigest,
		item.ExpectedPostIdentityDigest,
		fmt.Sprintf("%d", item.ExpectedPostBytes),
		fmt.Sprintf("%d", item.ExpectedPriorBytes),
		item.EncryptedTargetRelativeLocator,
		fmt.Sprintf("%d", item.TargetLocatorKeyVersion),
		fmt.Sprintf("%d", item.TargetLocatorCipherVersion),
		item.DisplayClass,
		fmt.Sprintf("%d", item.EstimatedBytes),
	)
}

func TestWorkerClaimStatelessEligibilitySkipsPersistentFailuresBeforeLimitAfterRestart(t *testing.T) {
	fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptWriteAuthorize)
	jobs := executeRecoveryWorkerJobs(t, fixture, 3)
	for index, job := range jobs {
		updatedAt := fixture.now.Add(time.Duration(index-3) * time.Minute)
		if err := fixture.db.Model(&model.BackupAssetRecoveryJob{}).Where("id = ?", job.JobID).
			Update("updated_at", updatedAt).Error; err != nil {
			t.Fatalf("order queued recovery job %d: %v", index, err)
		}
	}
	for _, job := range jobs[:2] {
		releasedAt := fixture.now
		if err := fixture.db.Model(&model.RecoveryPointLease{}).
			Where("holder_type = ? AND owner_id = ?", backupasset.LeaseHolderRecoveryJob, job.JobID).
			Updates(map[string]any{"status": string(backupasset.LeaseReleased), "released_at": releasedAt, "updated_at": releasedAt}).Error; err != nil {
			t.Fatalf("persist invalid early recovery source lease: %v", err)
		}
	}

	coordinator := newRecoveryWorkerCoordinatorWithLimit(t, fixture, 2)
	claim, found, err := coordinator.ClaimNext(context.Background(), "recovery-worker-after-restart")
	if err != nil || !found {
		t.Fatalf("claim later eligible recovery job after restart: found=%t err=%v", found, err)
	}
	if claim.JobID != jobs[2].JobID {
		t.Fatalf("claimed job=%q, want later eligible job %q", claim.JobID, jobs[2].JobID)
	}
}

func TestWorkerTakeoverStatelessEligibilitySkipsPersistentFailuresBeforeLimitAfterRestart(t *testing.T) {
	fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptWriteAuthorize)
	jobs := executeRecoveryWorkerJobs(t, fixture, 3)
	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	claims := make([]RecoveryWorkerClaim, 0, len(jobs))
	for index := range jobs {
		claim, found, err := coordinator.ClaimNext(context.Background(), fmt.Sprintf("recovery-worker-%d", index))
		if err != nil || !found {
			t.Fatalf("claim recovery worker fixture %d: found=%t err=%v", index, found, err)
		}
		claims = append(claims, claim)
	}

	expiredBase := claims[0].LeaseExpiresAt.Add(-3 * time.Minute)
	for index, claim := range claims {
		expiresAt := expiredBase.Add(time.Duration(index) * time.Minute)
		if err := fixture.db.Model(&model.BackupAssetRecoveryAttempt{}).Where("id = ?", claim.AttemptID).
			Update("lease_expires_at", expiresAt).Error; err != nil {
			t.Fatalf("expire recovery attempt %d: %v", index, err)
		}
		if err := fixture.db.Model(&model.BackupAssetRecoveryNodeLease{}).Where("id = ?", claim.NodeLeaseID).
			Update("lease_expires_at", expiresAt).Error; err != nil {
			t.Fatalf("expire recovery node lease %d: %v", index, err)
		}
		if err := fixture.db.Model(&model.RecoveryPointLease{}).Where("id = ?", claim.SourceFence.LeaseID).
			Update("lease_expires_at", expiresAt).Error; err != nil {
			t.Fatalf("expire recovery source lease %d: %v", index, err)
		}
	}
	for _, claim := range claims[:2] {
		releasedAt := fixture.now
		if err := fixture.db.Model(&model.RecoveryPointLease{}).Where("id = ?", claim.SourceFence.LeaseID).
			Updates(map[string]any{
				"status": string(backupasset.LeaseReleased), "released_at": releasedAt, "updated_at": releasedAt,
			}).Error; err != nil {
			t.Fatalf("persist invalid early takeover source lease: %v", err)
		}
	}
	fixture.now = expiredBase.Add(3 * time.Minute)

	restarted := newRecoveryWorkerCoordinatorWithLimit(t, fixture, 2)
	takeover, found, err := restarted.TakeoverExpired(context.Background(), "recovery-worker-after-restart")
	if err != nil || !found {
		t.Fatalf("take over later eligible recovery job after restart: found=%t err=%v", found, err)
	}
	if takeover.JobID != claims[2].JobID || takeover.AttemptID == claims[2].AttemptID {
		t.Fatalf("took over job/attempt=%q/%q, want later eligible job %q with a new attempt",
			takeover.JobID, takeover.AttemptID, claims[2].JobID)
	}
}

func TestRecoveryReviewF3TwoWorkerAndCrashBarriers(t *testing.T) {
	t.Run("claim cursor survives persistent fence losers and a reserved-position crash", func(t *testing.T) {
		fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptWriteAuthorize)
		jobs := executeRecoveryWorkerJobs(t, fixture, 5)
		for index, job := range jobs {
			if err := fixture.db.Model(&model.BackupAssetRecoveryJob{}).Where("id = ?", job.JobID).
				Update("updated_at", fixture.now.Add(time.Duration(index-5)*time.Minute)).Error; err != nil {
				t.Fatalf("order F3 claim candidate %d: %v", index, err)
			}
		}
		failures := make(map[string]struct{}, 2)
		for _, job := range jobs[:2] {
			var lease model.RecoveryPointLease
			if err := fixture.db.Where("holder_type = ? AND owner_id = ? AND status = ?",
				backupasset.LeaseHolderRecoveryJob, job.JobID, backupasset.LeaseActive).Take(&lease).Error; err != nil {
				t.Fatal(err)
			}
			failures[lease.ID] = struct{}{}
		}

		coordinator := newRecoveryWorkerCoordinatorWithLimit(t, fixture, 2)
		coordinator.sourceLeases = &recoveryF3FenceLeaseCoordinator{
			delegate: coordinator.sourceLeases, renewFailures: failures,
		}
		if claim, found, err := coordinator.ClaimNext(context.Background(), "recovery-f3-claim-first-pass"); err != nil || found || claim.JobID != "" {
			t.Fatalf("persistent-prefix first pass: claim=%+v found=%t err=%v", claim, found, err)
		}
		for _, job := range jobs[:2] {
			var state string
			if err := fixture.db.Model(&model.BackupAssetRecoveryJob{}).Select("state").Where("id = ?", job.JobID).Scan(&state).Error; err != nil {
				t.Fatal(err)
			}
			if state != string(JobStateQueued) {
				t.Fatalf("fence loser %q mutated domain state to %q", job.JobID, state)
			}
		}

		restarted := newRecoveryWorkerCoordinatorWithLimit(t, fixture, 2)
		restarted.sourceLeases = &recoveryF3FenceLeaseCoordinator{
			delegate: restarted.sourceLeases, renewFailures: failures,
		}
		claim, found, err := restarted.ClaimNext(context.Background(), "recovery-f3-claim-after-restart")
		if err != nil || !found || claim.JobID != jobs[2].JobID {
			t.Fatalf("restart did not pass persistent prefix: claim=%+v want_job=%q found=%t err=%v",
				claim, jobs[2].JobID, found, err)
		}

		var skipped model.BackupAssetRecoveryJob
		if err := fixture.db.Where("id = ?", jobs[3].JobID).Take(&skipped).Error; err != nil {
			t.Fatal(err)
		}
		crashAdvance := fixture.db.Exec(`UPDATE backup_asset_recovery_evidence
			SET scheduler_cursor_at = ?, scheduler_cursor_id = ?, scheduler_revision = scheduler_revision + 1, updated_at = ?
			WHERE id = ? AND kind = 'scheduler_state' AND scheduler_scope = 'claim' AND scheduler_cursor_id = ?`,
			skipped.UpdatedAt, skipped.ID, fixture.now.Add(time.Minute),
			"0000000000000000000000000000006a", jobs[2].JobID)
		if crashAdvance.Error != nil || crashAdvance.RowsAffected != 1 {
			t.Fatalf("simulate committed scheduler pre-advance before crash: rows=%d err=%v",
				crashAdvance.RowsAffected, crashAdvance.Error)
		}
		afterCrash := newRecoveryWorkerCoordinatorWithLimit(t, fixture, 2)
		afterCrash.sourceLeases = &recoveryF3FenceLeaseCoordinator{
			delegate: afterCrash.sourceLeases, renewFailures: failures,
		}
		claim, found, err = afterCrash.ClaimNext(context.Background(), "recovery-f3-claim-after-crash")
		if err != nil || !found || claim.JobID != jobs[4].JobID {
			t.Fatalf("crash-delayed position starved later work: claim=%+v want_job=%q found=%t err=%v",
				claim, jobs[4].JobID, found, err)
		}
		var skippedState string
		if err := fixture.db.Model(&model.BackupAssetRecoveryJob{}).Select("state").
			Where("id = ?", skipped.ID).Scan(&skippedState).Error; err != nil {
			t.Fatal(err)
		}
		if skippedState != string(JobStateQueued) {
			t.Fatalf("crash-delayed job state=%q, want queued until sweep wrap", skippedState)
		}
	})

	t.Run("takeover cursor survives persistent fence losers", func(t *testing.T) {
		fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptWriteAuthorize)
		jobs := executeRecoveryWorkerJobs(t, fixture, 4)
		claimCoordinator := newRecoveryWorkerCoordinator(t, fixture)
		claims := make([]RecoveryWorkerClaim, 0, len(jobs))
		for index := range jobs {
			claim, found, err := claimCoordinator.ClaimNext(context.Background(), fmt.Sprintf("recovery-f3-owner-%d", index))
			if err != nil || !found {
				t.Fatalf("claim takeover fixture %d: found=%t err=%v", index, found, err)
			}
			claims = append(claims, claim)
		}
		expiredBase := fixture.now.Add(-5 * time.Minute)
		for index, claim := range claims {
			expiresAt := expiredBase.Add(time.Duration(index) * time.Minute)
			for _, update := range []struct {
				model  any
				id     string
				column string
			}{
				{model: &model.BackupAssetRecoveryAttempt{}, id: claim.AttemptID, column: "id"},
				{model: &model.BackupAssetRecoveryNodeLease{}, id: claim.NodeLeaseID, column: "id"},
				{model: &model.RecoveryPointLease{}, id: claim.SourceFence.LeaseID, column: "id"},
			} {
				if err := fixture.db.Model(update.model).Where(update.column+" = ?", update.id).
					Update("lease_expires_at", expiresAt).Error; err != nil {
					t.Fatalf("expire F3 takeover fixture %d: %v", index, err)
				}
			}
		}
		failures := map[string]struct{}{
			claims[0].SourceFence.LeaseID: {},
			claims[1].SourceFence.LeaseID: {},
		}
		coordinator := newRecoveryWorkerCoordinatorWithLimit(t, fixture, 2)
		coordinator.sourceLeases = &recoveryF3FenceLeaseCoordinator{
			delegate: coordinator.sourceLeases, takeoverFailures: failures,
		}
		if claim, found, err := coordinator.TakeoverExpired(context.Background(), "recovery-f3-takeover-first-pass"); err != nil || found || claim.JobID != "" {
			t.Fatalf("persistent takeover prefix first pass: claim=%+v found=%t err=%v", claim, found, err)
		}

		restarted := newRecoveryWorkerCoordinatorWithLimit(t, fixture, 2)
		restarted.sourceLeases = &recoveryF3FenceLeaseCoordinator{
			delegate: restarted.sourceLeases, takeoverFailures: failures,
		}
		claim, found, err := restarted.TakeoverExpired(context.Background(), "recovery-f3-takeover-after-restart")
		if err != nil || !found || claim.JobID != claims[2].JobID || claim.AttemptID == claims[2].AttemptID {
			t.Fatalf("restart did not pass persistent takeover prefix: claim=%+v want_job=%q found=%t err=%v",
				claim, claims[2].JobID, found, err)
		}
	})

	t.Run("two workers have one domain winner", func(t *testing.T) {
		fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptExecute)
		result, err := fixture.service.Authorize(context.Background(), fixture.request)
		if err != nil {
			t.Fatal(err)
		}
		first := newRecoveryWorkerCoordinatorWithLimit(t, fixture, 1)
		second := newRecoveryWorkerCoordinatorWithLimit(t, fixture, 1)
		winner, found, err := first.ClaimNext(context.Background(), "recovery-f3-worker-a")
		if err != nil || !found || winner.JobID != result.JobID {
			t.Fatalf("first worker claim=%+v found=%t err=%v", winner, found, err)
		}
		loser, found, err := second.ClaimNext(context.Background(), "recovery-f3-worker-b")
		if err != nil || found || loser.JobID != "" {
			t.Fatalf("second worker crossed one-winner barrier: claim=%+v found=%t err=%v", loser, found, err)
		}
	})
}

type recoveryF3FenceLeaseCoordinator struct {
	delegate         RecoverySourceLeaseCoordinator
	renewFailures    map[string]struct{}
	takeoverFailures map[string]struct{}
}

func (coordinator *recoveryF3FenceLeaseCoordinator) RenewTx(
	ctx context.Context,
	tx *gorm.DB,
	fence backupasset.LeaseFence,
) (backupasset.Lease, error) {
	if _, fail := coordinator.renewFailures[fence.LeaseID]; fail {
		return backupasset.Lease{}, backupasset.ErrLeaseFenceLost
	}
	return coordinator.delegate.RenewTx(ctx, tx, fence)
}

func (coordinator *recoveryF3FenceLeaseCoordinator) TakeoverTx(
	ctx context.Context,
	tx *gorm.DB,
	request backupasset.TakeoverLeaseRequest,
) (backupasset.Lease, error) {
	if _, fail := coordinator.takeoverFailures[request.LeaseID]; fail {
		return backupasset.Lease{}, backupasset.ErrLeaseFenceLost
	}
	return coordinator.delegate.TakeoverTx(ctx, tx, request)
}

func (coordinator *recoveryF3FenceLeaseCoordinator) ReleaseTx(
	ctx context.Context,
	tx *gorm.DB,
	fence backupasset.LeaseFence,
) error {
	return coordinator.delegate.ReleaseTx(ctx, tx, fence)
}

func TestWorkerReconcilePermanentCleanupKeyLossClosesOnlyCurrentPostArmWorkWithoutSideEffects(t *testing.T) {
	fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptWriteAuthorize)
	jobs := executeRecoveryWorkerJobs(t, fixture, 4)
	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	claims := claimRecoveryWorkerJobs(t, coordinator, len(jobs))

	isolatedClaim := claims[jobs[0].JobID]
	preArmClaim := claims[jobs[1].JobID]
	terminalClaim := claims[jobs[2].JobID]
	inPlaceClaim := claims[jobs[3].JobID]
	for name, claim := range map[string]RecoveryWorkerClaim{
		"isolated": isolatedClaim,
		"pre-arm":  preArmClaim,
		"terminal": terminalClaim,
		"in-place": inPlaceClaim,
	} {
		if claim.JobID == "" {
			t.Fatalf("missing %s recovery worker claim", name)
		}
	}
	if _, err := coordinator.PrepareFirstWrite(context.Background(), isolatedClaim); err != nil {
		t.Fatalf("arm isolated recovery job: %v", err)
	}
	if _, err := coordinator.PrepareFirstWrite(context.Background(), inPlaceClaim); err != nil {
		t.Fatalf("arm in-place recovery job fixture: %v", err)
	}
	if err := coordinator.CancelJob(context.Background(), terminalClaim.JobID); err != nil {
		t.Fatalf("terminalize recovery job fixture: %v", err)
	}

	const corruptWorkspaceCiphertext = "enc:v2:corrupt-workspace-ciphertext"
	if err := fixture.db.Table((model.BackupAssetRecoveryJob{}).TableName()).
		Where("id = ?", isolatedClaim.JobID).
		Update("encrypted_workspace_relative_locator", corruptWorkspaceCiphertext).Error; err != nil {
		t.Fatalf("corrupt encrypted workspace locator: %v", err)
	}
	if err := fixture.db.Table((model.BackupAssetRecoveryJob{}).TableName()).
		Where("id = ?", inPlaceClaim.JobID).
		Updates(map[string]any{
			"target_mode": TargetModeInPlace, "workspace_phase": WorkspacePhaseNone,
			"encrypted_workspace_relative_locator": "", "workspace_marker_binding_digest": "",
			"workspace_owner": "", "workspace_fence": 0, "plaintext_deadline": nil,
		}).Error; err != nil {
		t.Fatalf("convert armed recovery fixture to in-place mode: %v", err)
	}

	isolatedBefore := loadRecoveryCleanupKeyJobState(t, fixture.db, isolatedClaim.JobID)
	inPlaceBefore := loadRecoveryCleanupKeyJobState(t, fixture.db, inPlaceClaim.JobID)
	preArmBefore := loadRecoveryCleanupKeyAggregateState(t, fixture.db, preArmClaim.JobID)
	terminalBefore := loadRecoveryCleanupKeyAggregateState(t, fixture.db, terminalClaim.JobID)
	isolatedSideEffectsBefore := loadRecoveryCleanupKeySideEffects(t, fixture.db, isolatedClaim.JobID)
	inPlaceSideEffectsBefore := loadRecoveryCleanupKeySideEffects(t, fixture.db, inPlaceClaim.JobID)

	reconciler, ok := any(coordinator).(recoveryPermanentCleanupKeyReconciler)
	if !ok {
		t.Fatal("recovery worker does not provide permanent cleanup-key reconciliation")
	}
	changed, err := reconciler.ReconcilePermanentCleanupKeyLoss(context.Background())
	if err != nil || changed != 2 {
		t.Fatalf("reconcile permanent cleanup-key loss: changed=%d err=%v", changed, err)
	}

	isolatedAfter := loadRecoveryCleanupKeyAggregateState(t, fixture.db, isolatedClaim.JobID)
	if isolatedAfter.Job.State != string(JobStateNeedsAttention) ||
		isolatedAfter.Job.FailureCategory != "cleanup_key_unavailable" ||
		isolatedAfter.Job.WorkspacePhase != string(WorkspacePhaseCleanupDue) ||
		isolatedAfter.Job.TransitionRevision != isolatedBefore.TransitionRevision+1 ||
		isolatedAfter.Job.EncryptedWorkspaceRelativeLocator != corruptWorkspaceCiphertext ||
		isolatedAfter.Job.TargetChainRevision != isolatedBefore.TargetChainRevision {
		t.Fatalf("isolated cleanup-key handoff mismatch: before=%+v after=%+v", isolatedBefore, isolatedAfter.Job)
	}
	assertRecoveryCleanupKeyAttemptClosed(t, isolatedAfter.Attempts, isolatedClaim.AttemptID)

	inPlaceAfter := loadRecoveryCleanupKeyAggregateState(t, fixture.db, inPlaceClaim.JobID)
	if inPlaceAfter.Job.State != string(JobStateNeedsAttention) ||
		inPlaceAfter.Job.FailureCategory != "cleanup_key_unavailable" ||
		inPlaceAfter.Job.WorkspacePhase != string(WorkspacePhaseNone) ||
		inPlaceAfter.Job.TransitionRevision != inPlaceBefore.TransitionRevision+1 ||
		inPlaceAfter.Job.TargetChainRevision != inPlaceBefore.TargetChainRevision {
		t.Fatalf("in-place cleanup-key handoff mismatch: before=%+v after=%+v", inPlaceBefore, inPlaceAfter.Job)
	}
	assertRecoveryCleanupKeyAttemptClosed(t, inPlaceAfter.Attempts, inPlaceClaim.AttemptID)

	if after := loadRecoveryCleanupKeyAggregateState(t, fixture.db, preArmClaim.JobID); !reflect.DeepEqual(after, preArmBefore) {
		t.Fatalf("cleanup-key reconciliation changed pre-arm work: before=%+v after=%+v", preArmBefore, after)
	}
	if after := loadRecoveryCleanupKeyAggregateState(t, fixture.db, terminalClaim.JobID); !reflect.DeepEqual(after, terminalBefore) {
		t.Fatalf("cleanup-key reconciliation changed terminal work: before=%+v after=%+v", terminalBefore, after)
	}
	if after := loadRecoveryCleanupKeySideEffects(t, fixture.db, isolatedClaim.JobID); !reflect.DeepEqual(after, isolatedSideEffectsBefore) {
		t.Fatalf("cleanup-key reconciliation changed isolated item/checkpoint/lease state: before=%+v after=%+v", isolatedSideEffectsBefore, after)
	}
	if after := loadRecoveryCleanupKeySideEffects(t, fixture.db, inPlaceClaim.JobID); !reflect.DeepEqual(after, inPlaceSideEffectsBefore) {
		t.Fatalf("cleanup-key reconciliation changed in-place item/checkpoint/lease state: before=%+v after=%+v", inPlaceSideEffectsBefore, after)
	}
	for _, jobID := range []string{isolatedClaim.JobID, inPlaceClaim.JobID} {
		var sequenceOne int64
		if err := fixture.db.Model(&model.BackupAssetRecoveryCheckpoint{}).
			Where("job_id = ? AND sequence = ?", jobID, 1).Count(&sequenceOne).Error; err != nil {
			t.Fatal(err)
		}
		if sequenceOne != 0 {
			t.Fatalf("cleanup-key reconciliation appended sequence-1 checkpoints for job %q: %d", jobID, sequenceOne)
		}
	}
	target, ok := coordinator.target.(*recoveryRestartTargetFake)
	if !ok || target.calls != 0 {
		t.Fatalf("cleanup-key reconciliation contacted target: target=%T calls=%d", coordinator.target, target.calls)
	}

	isolatedTerminal := loadRecoveryCleanupKeyAggregateState(t, fixture.db, isolatedClaim.JobID)
	inPlaceTerminal := loadRecoveryCleanupKeyAggregateState(t, fixture.db, inPlaceClaim.JobID)
	changed, err = reconciler.ReconcilePermanentCleanupKeyLoss(context.Background())
	if err != nil || changed != 0 {
		t.Fatalf("retry permanent cleanup-key reconciliation: changed=%d err=%v", changed, err)
	}
	if after := loadRecoveryCleanupKeyAggregateState(t, fixture.db, isolatedClaim.JobID); !reflect.DeepEqual(after, isolatedTerminal) {
		t.Fatalf("cleanup-key retry changed isolated terminal rows: before=%+v after=%+v", isolatedTerminal, after)
	}
	if after := loadRecoveryCleanupKeyAggregateState(t, fixture.db, inPlaceClaim.JobID); !reflect.DeepEqual(after, inPlaceTerminal) {
		t.Fatalf("cleanup-key retry changed in-place terminal rows: before=%+v after=%+v", inPlaceTerminal, after)
	}
}

func TestWorkerReconcilePermanentCleanupKeyLossPreservesTakenOverAttempt(t *testing.T) {
	fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptExecute)
	if _, err := fixture.service.Authorize(context.Background(), fixture.request); err != nil {
		t.Fatalf("execute recovery fixture: %v", err)
	}
	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	first, found, err := coordinator.ClaimNext(context.Background(), "recovery-worker-before-restart")
	if err != nil || !found {
		t.Fatalf("claim recovery job: found=%t err=%v", found, err)
	}
	if _, err := coordinator.PrepareFirstWrite(context.Background(), first); err != nil {
		t.Fatalf("arm recovery job: %v", err)
	}
	fixture.now = first.LeaseExpiresAt.Add(time.Second)
	current, found, err := coordinator.TakeoverExpired(context.Background(), "recovery-worker-after-restart")
	if err != nil || !found {
		t.Fatalf("take over armed recovery job: found=%t err=%v", found, err)
	}
	if current.AttemptID == first.AttemptID {
		t.Fatal("recovery takeover retained stale attempt identity")
	}

	before := loadRecoveryCleanupKeyAggregateState(t, fixture.db, current.JobID)
	staleBefore := recoveryCleanupKeyAttemptByID(t, before.Attempts, first.AttemptID)
	sideEffectsBefore := loadRecoveryCleanupKeySideEffects(t, fixture.db, current.JobID)
	reconciler, ok := any(coordinator).(recoveryPermanentCleanupKeyReconciler)
	if !ok {
		t.Fatal("recovery worker does not provide permanent cleanup-key reconciliation")
	}
	changed, err := reconciler.ReconcilePermanentCleanupKeyLoss(context.Background())
	if err != nil || changed != 1 {
		t.Fatalf("reconcile taken-over cleanup-key loss: changed=%d err=%v", changed, err)
	}

	after := loadRecoveryCleanupKeyAggregateState(t, fixture.db, current.JobID)
	if after.Job.State != string(JobStateNeedsAttention) || after.Job.FailureCategory != "cleanup_key_unavailable" ||
		after.Job.WorkspacePhase != string(WorkspacePhaseCleanupDue) ||
		after.Job.TransitionRevision != before.Job.TransitionRevision+1 ||
		after.Job.TargetChainRevision != before.Job.TargetChainRevision {
		t.Fatalf("taken-over cleanup-key handoff mismatch: before=%+v after=%+v", before.Job, after.Job)
	}
	assertRecoveryCleanupKeyAttemptClosed(t, after.Attempts, current.AttemptID)
	if staleAfter := recoveryCleanupKeyAttemptByID(t, after.Attempts, first.AttemptID); !reflect.DeepEqual(staleAfter, staleBefore) {
		t.Fatalf("cleanup-key reconciliation changed stale taken-over attempt: before=%+v after=%+v", staleBefore, staleAfter)
	}
	if sideEffectsAfter := loadRecoveryCleanupKeySideEffects(t, fixture.db, current.JobID); !reflect.DeepEqual(sideEffectsAfter, sideEffectsBefore) {
		t.Fatalf("taken-over cleanup-key reconciliation changed item/checkpoint/lease state: before=%+v after=%+v", sideEffectsBefore, sideEffectsAfter)
	}
}

func TestWorkerReconcilePermanentCleanupKeyLossHonorsScanLimit(t *testing.T) {
	fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptWriteAuthorize)
	jobs := executeRecoveryWorkerJobs(t, fixture, 3)
	coordinator := newRecoveryWorkerCoordinatorWithLimit(t, fixture, 2)
	claims := claimRecoveryWorkerJobs(t, coordinator, len(jobs))
	for _, job := range jobs {
		if _, err := coordinator.PrepareFirstWrite(context.Background(), claims[job.JobID]); err != nil {
			t.Fatalf("arm bounded recovery job %q: %v", job.JobID, err)
		}
	}
	reconciler, ok := any(coordinator).(recoveryPermanentCleanupKeyReconciler)
	if !ok {
		t.Fatal("recovery worker does not provide permanent cleanup-key reconciliation")
	}

	for pass, wantChanged := range []int{2, 1, 0} {
		changed, err := reconciler.ReconcilePermanentCleanupKeyLoss(context.Background())
		if err != nil || changed != wantChanged {
			t.Fatalf("bounded cleanup-key pass %d: changed=%d want=%d err=%v", pass+1, changed, wantChanged, err)
		}
		var terminal int64
		jobIDs := []string{jobs[0].JobID, jobs[1].JobID, jobs[2].JobID}
		if err := fixture.db.Table((model.BackupAssetRecoveryJob{}).TableName()).
			Where("id IN ? AND state = ?", jobIDs, JobStateNeedsAttention).Count(&terminal).Error; err != nil {
			t.Fatal(err)
		}
		wantTerminal := int64(2)
		if pass > 0 {
			wantTerminal = 3
		}
		if terminal != wantTerminal {
			t.Fatalf("bounded cleanup-key pass %d terminal jobs=%d want=%d", pass+1, terminal, wantTerminal)
		}
	}
	target, ok := coordinator.target.(*recoveryRestartTargetFake)
	if !ok || target.calls != 0 {
		t.Fatalf("bounded cleanup-key reconciliation contacted target: target=%T calls=%d", coordinator.target, target.calls)
	}
}

type recoveryCleanupKeyJobState struct {
	ID                                string     `gorm:"column:id"`
	State                             string     `gorm:"column:state"`
	FailureCategory                   string     `gorm:"column:failure_category"`
	TransitionRevision                uint64     `gorm:"column:transition_revision"`
	WorkspacePhase                    string     `gorm:"column:workspace_phase"`
	EncryptedWorkspaceRelativeLocator string     `gorm:"column:encrypted_workspace_relative_locator"`
	TargetMode                        string     `gorm:"column:target_mode"`
	TargetChainRevision               string     `gorm:"column:target_chain_revision"`
	UpdatedAt                         time.Time  `gorm:"column:updated_at"`
	PlaintextDeadline                 *time.Time `gorm:"column:plaintext_deadline"`
}

type recoveryCleanupKeyAggregateState struct {
	Job      recoveryCleanupKeyJobState
	Attempts []model.BackupAssetRecoveryAttempt
}

type recoveryCleanupKeySideEffects struct {
	Items       []model.BackupAssetRecoveryJobItem
	Checkpoints []model.BackupAssetRecoveryCheckpoint
	Source      []model.RecoveryPointLease
	Node        []model.BackupAssetRecoveryNodeLease
}

func claimRecoveryWorkerJobs(
	t *testing.T,
	coordinator *WorkerCoordinator,
	count int,
) map[string]RecoveryWorkerClaim {
	t.Helper()
	claims := make(map[string]RecoveryWorkerClaim, count)
	for index := 0; index < count; index++ {
		claim, found, err := coordinator.ClaimNext(context.Background(), fmt.Sprintf("cleanup-key-worker-%d", index))
		if err != nil || !found {
			t.Fatalf("claim cleanup-key recovery job %d: found=%t err=%v", index, found, err)
		}
		claims[claim.JobID] = claim
	}
	return claims
}

func loadRecoveryCleanupKeyJobState(t *testing.T, db *gorm.DB, jobID string) recoveryCleanupKeyJobState {
	t.Helper()
	var state recoveryCleanupKeyJobState
	result := db.Table((model.BackupAssetRecoveryJob{}).TableName()).
		Select("id, state, failure_category, transition_revision, workspace_phase, encrypted_workspace_relative_locator, target_mode, target_chain_revision, updated_at, plaintext_deadline").
		Where("id = ?", jobID).Limit(1).Scan(&state)
	if result.Error != nil || result.RowsAffected != 1 {
		t.Fatalf("load raw cleanup-key recovery job %q: rows=%d err=%v", jobID, result.RowsAffected, result.Error)
	}
	return state
}

func loadRecoveryCleanupKeyAggregateState(t *testing.T, db *gorm.DB, jobID string) recoveryCleanupKeyAggregateState {
	t.Helper()
	state := recoveryCleanupKeyAggregateState{Job: loadRecoveryCleanupKeyJobState(t, db, jobID)}
	if err := db.Where("job_id = ?", jobID).Order("created_at ASC, id ASC").Find(&state.Attempts).Error; err != nil {
		t.Fatalf("load cleanup-key recovery attempts for job %q: %v", jobID, err)
	}
	return state
}

func loadRecoveryCleanupKeySideEffects(t *testing.T, db *gorm.DB, jobID string) recoveryCleanupKeySideEffects {
	t.Helper()
	var state recoveryCleanupKeySideEffects
	if err := db.Where("job_id = ?", jobID).Order("ordinal ASC, id ASC").Find(&state.Items).Error; err != nil {
		t.Fatalf("load cleanup-key recovery items for job %q: %v", jobID, err)
	}
	if err := db.Where("job_id = ?", jobID).Order("sequence ASC, id ASC").Find(&state.Checkpoints).Error; err != nil {
		t.Fatalf("load cleanup-key recovery checkpoints for job %q: %v", jobID, err)
	}
	if err := db.Where("holder_type = ? AND owner_id = ?", backupasset.LeaseHolderRecoveryJob, jobID).
		Order("id ASC").Find(&state.Source).Error; err != nil {
		t.Fatalf("load cleanup-key source leases for job %q: %v", jobID, err)
	}
	if err := db.Where("job_id = ?", jobID).Order("id ASC").Find(&state.Node).Error; err != nil {
		t.Fatalf("load cleanup-key node leases for job %q: %v", jobID, err)
	}
	return state
}

func assertRecoveryCleanupKeyAttemptClosed(
	t *testing.T,
	attempts []model.BackupAssetRecoveryAttempt,
	attemptID string,
) {
	t.Helper()
	attempt := recoveryCleanupKeyAttemptByID(t, attempts, attemptID)
	if attempt.State != string(AttemptStateFailed) || !attempt.MutationArmed || attempt.ClosedAt == nil {
		t.Fatalf("cleanup-key reconciliation did not close current armed attempt: %+v", attempt)
	}
}

func recoveryCleanupKeyAttemptByID(
	t *testing.T,
	attempts []model.BackupAssetRecoveryAttempt,
	attemptID string,
) model.BackupAssetRecoveryAttempt {
	t.Helper()
	for _, attempt := range attempts {
		if attempt.ID == attemptID {
			return attempt
		}
	}
	t.Fatalf("cleanup-key recovery attempt %q not found", attemptID)
	return model.BackupAssetRecoveryAttempt{}
}

func executeRecoveryWorkerJobs(
	t *testing.T,
	fixture *authorizationReceiptServiceFixture,
	count int,
) []RecoveryAuthorizationResult {
	t.Helper()
	type target struct {
		planID      string
		preflightID string
	}
	targets := []target{{planID: fixture.request.PlanID, preflightID: fixture.request.PreflightID}}
	for len(targets) < count {
		planID, preflightID := fixture.cloneAuthorizationPlan(t)
		targets = append(targets, target{planID: planID, preflightID: preflightID})
	}
	results := make([]RecoveryAuthorizationResult, 0, len(targets))
	for index, target := range targets {
		if index > 0 {
			node := model.Node{
				Name: fmt.Sprintf("recovery-worker-fairness-node-%d", index), Host: "127.0.0.1", Port: 22,
				Username: "root", AuthType: "key", BackupDir: fmt.Sprintf("/tmp/recovery-worker-fairness-%d", index),
			}
			if err := fixture.db.Create(&node).Error; err != nil {
				t.Fatalf("create recovery worker node %d: %v", index, err)
			}
			if err := fixture.db.Model(&model.BackupAssetRecoveryPlan{}).Where("id = ?", target.planID).
				Update("target_node_id", node.ID).Error; err != nil {
				t.Fatalf("bind recovery worker plan %d to node: %v", index, err)
			}
			if err := fixture.db.Model(&model.BackupAssetRecoveryPreflight{}).Where("id = ?", target.preflightID).
				Update("target_node_id", node.ID).Error; err != nil {
				t.Fatalf("bind recovery worker preflight %d to node: %v", index, err)
			}
		}
		write := fixture.request
		write.PlanID = target.planID
		write.PreflightID = target.preflightID
		write.IdempotencyKey = fmt.Sprintf("worker-fairness-write-%d", index)
		write.Proof.JTI = fmt.Sprintf("FAKE_RECOVERY_WORKER_FAIRNESS_WRITE_PROOF_%d", index)
		write.GrantSecret = mustAuthorizationReceiptSecretForFixture()
		writeResult, err := fixture.service.Authorize(context.Background(), write)
		if err != nil {
			t.Fatalf("authorize recovery worker job %d: %v", index, err)
		}

		execute := write
		execute.Operation = AuthorizationReceiptExecute
		execute.Category = AuthorizationReceiptCategoryExecute
		execute.Endpoint = recoveryExecuteEndpoint
		execute.IdempotencyKey = fmt.Sprintf("worker-fairness-execute-%d", index)
		execute.Proof.JTI = fmt.Sprintf("FAKE_RECOVERY_WORKER_FAIRNESS_EXECUTE_PROOF_%d", index)
		execute.ExpectedPlanRevision = writeResult.PlanTransitionRevision
		execute.GrantID = writeResult.GrantID
		execute.Reason = ""
		executeResult, err := fixture.service.Authorize(context.Background(), execute)
		if err != nil {
			t.Fatalf("execute recovery worker job %d: %v", index, err)
		}
		if index > 0 {
			var plan model.BackupAssetRecoveryPlan
			if err := fixture.db.Where("id = ?", target.planID).Take(&plan).Error; err != nil {
				t.Fatalf("load cloned recovery worker plan %d: %v", index, err)
			}
			rootDigest, err := settings.RecoveryTargetRootLocatorDigest(
				plan.TargetNodeID, plan.TargetRootID, plan.EncryptedTargetRootLocator,
			)
			if err != nil {
				t.Fatalf("rebind cloned recovery worker target root %d: %v", index, err)
			}
			plan.RootLocatorDigest = rootDigest
			if err := fixture.db.Model(&model.BackupAssetRecoveryPlan{}).Where("id = ?", plan.ID).
				UpdateColumn("root_locator_digest", rootDigest).Error; err != nil {
				t.Fatalf("update cloned recovery worker plan root %d: %v", index, err)
			}
			if err := fixture.db.Model(&model.BackupAssetRecoveryPreflight{}).Where("id = ?", target.preflightID).
				UpdateColumn("root_locator_digest", rootDigest).Error; err != nil {
				t.Fatalf("update cloned recovery worker preflight root %d: %v", index, err)
			}
			var job model.BackupAssetRecoveryJob
			if err := fixture.db.Where("id = ?", executeResult.JobID).Take(&job).Error; err != nil {
				t.Fatalf("load cloned recovery worker job %d: %v", index, err)
			}
			workspaceDigest := recoveryWorkspaceBindingDigest(
				plan, job.ID, job.EncryptedWorkspaceRelativeLocator,
			)
			if err := fixture.db.Model(&model.BackupAssetRecoveryJob{}).Where("id = ?", job.ID).
				UpdateColumns(map[string]any{
					"root_locator_digest":      rootDigest,
					"workspace_binding_digest": workspaceDigest,
				}).Error; err != nil {
				t.Fatalf("update cloned recovery worker job root %d: %v", index, err)
			}
		}
		results = append(results, executeResult)
	}
	return results
}

func assertNoPreparedFirstWriteState(
	t *testing.T,
	fixture *authorizationReceiptServiceFixture,
	claim RecoveryWorkerClaim,
) {
	t.Helper()
	var latchCount, checkpointCount int64
	if err := fixture.db.Model(&model.BackupAssetRecoveryEvidence{}).
		Where("id = ?", recoverySchemaUseLatchRowID).Count(&latchCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.BackupAssetRecoveryCheckpoint{}).
		Where("job_id = ?", claim.JobID).Count(&checkpointCount).Error; err != nil {
		t.Fatal(err)
	}
	if latchCount != 0 || checkpointCount != 0 {
		t.Fatalf("failed first-write barrier left latch/checkpoint state: latches=%d checkpoints=%d", latchCount, checkpointCount)
	}

	var job model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", claim.JobID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	var plan model.BackupAssetRecoveryPlan
	if err := fixture.db.Where("id = ?", job.PlanID).Take(&plan).Error; err != nil {
		t.Fatal(err)
	}
	wantWorkspace := "jobs/" + job.ID
	if job.WorkspacePhase != string(WorkspacePhaseNone) || job.EncryptedWorkspaceRelativeLocator != wantWorkspace ||
		job.WorkspaceBindingDigest != recoveryWorkspaceBindingDigest(plan, job.ID, wantWorkspace) ||
		job.WorkspaceMarkerBindingDigest != "" || job.WorkspaceOwner != "" || job.WorkspaceFence != 0 ||
		job.PlaintextDeadline != nil {
		t.Fatalf("failed first-write barrier left workspace reservation: %+v", job)
	}
	var attempt model.BackupAssetRecoveryAttempt
	if err := fixture.db.Where("id = ?", claim.AttemptID).Take(&attempt).Error; err != nil {
		t.Fatal(err)
	}
	if attempt.MutationArmed {
		t.Fatalf("failed first-write barrier armed attempt: %+v", attempt)
	}
}

type recoveryCleanupKeyAccessSpy struct {
	calls int
}

func (spy *recoveryCleanupKeyAccessSpy) Active(
	context.Context,
	backupasset.KeyDomain,
) (backupasset.DomainKeyMaterial, error) {
	spy.calls++
	return backupasset.DomainKeyMaterial{}, backupasset.ErrKeyUnavailable
}

func (spy *recoveryCleanupKeyAccessSpy) ByVersion(
	context.Context,
	backupasset.KeyDomain,
	int,
) (backupasset.DomainKeyMaterial, error) {
	spy.calls++
	return backupasset.DomainKeyMaterial{}, backupasset.ErrKeyUnavailable
}

type recoveryCleanupKeyJobSnapshot struct {
	State                             string
	FailureCategory                   string
	TransitionRevision                uint64
	WorkspacePhase                    string
	EncryptedWorkspaceRelativeLocator string
	WorkspaceMarkerBindingDigest      string
	WorkspaceOwner                    string
	WorkspaceFence                    uint64
	PlaintextDeadline                 *time.Time
	TargetChainRevision               string
	UpdatedAt                         time.Time
}

type recoveryCleanupKeyAttemptSnapshot struct {
	OwnerID        string
	Fence          uint64
	State          string
	MutationArmed  bool
	LeaseExpiresAt *time.Time
	HeartbeatAt    *time.Time
	ClosedAt       *time.Time
	UpdatedAt      time.Time
}

type recoveryCleanupKeyItemSnapshot struct {
	ID              string
	Outcome         string
	BytesWritten    int64
	VerifiedSize    int64
	VerifiedDigest  string
	FailureCategory string
	UpdatedAt       time.Time
}

func loadRecoveryCleanupKeyJobSnapshot(
	t *testing.T,
	db *gorm.DB,
	jobID string,
) recoveryCleanupKeyJobSnapshot {
	t.Helper()
	var snapshot recoveryCleanupKeyJobSnapshot
	result := db.Table("backup_asset_recovery_jobs").
		Select(`state, failure_category, transition_revision, workspace_phase,
			encrypted_workspace_relative_locator, workspace_marker_binding_digest,
			workspace_owner, workspace_fence, plaintext_deadline, target_chain_revision, updated_at`).
		Where("id = ?", jobID).Limit(1).Scan(&snapshot)
	if result.Error != nil || result.RowsAffected != 1 {
		t.Fatalf("load cleanup-key job snapshot: rows=%d err=%v", result.RowsAffected, result.Error)
	}
	return snapshot
}

func loadRecoveryCleanupKeyAttemptSnapshot(
	t *testing.T,
	db *gorm.DB,
	attemptID string,
) recoveryCleanupKeyAttemptSnapshot {
	t.Helper()
	var snapshot recoveryCleanupKeyAttemptSnapshot
	result := db.Table("backup_asset_recovery_attempts").
		Select(`owner_id, fence, state, mutation_armed, lease_expires_at,
			heartbeat_at, closed_at, updated_at`).
		Where("id = ?", attemptID).Limit(1).Scan(&snapshot)
	if result.Error != nil || result.RowsAffected != 1 {
		t.Fatalf("load cleanup-key attempt snapshot: rows=%d err=%v", result.RowsAffected, result.Error)
	}
	return snapshot
}

func loadRecoveryCleanupKeyItemSnapshot(
	t *testing.T,
	db *gorm.DB,
	jobID string,
) []recoveryCleanupKeyItemSnapshot {
	t.Helper()
	var snapshots []recoveryCleanupKeyItemSnapshot
	if err := db.Table("backup_asset_recovery_job_items").
		Select(`id, outcome, bytes_written, verified_size, verified_digest,
			failure_category, updated_at`).
		Where("job_id = ?", jobID).Order("ordinal ASC, id ASC").Scan(&snapshots).Error; err != nil {
		t.Fatalf("load cleanup-key item snapshot: %v", err)
	}
	return snapshots
}

func countRecoveryCleanupKeyCheckpoints(t *testing.T, db *gorm.DB, jobID string) int64 {
	t.Helper()
	var count int64
	if err := db.Table("backup_asset_recovery_checkpoints").Where("job_id = ?", jobID).Count(&count).Error; err != nil {
		t.Fatalf("count cleanup-key checkpoints: %v", err)
	}
	return count
}

func newRecoveryWorkerCoordinator(
	t *testing.T,
	fixture *authorizationReceiptServiceFixture,
) *WorkerCoordinator {
	return newRecoveryWorkerCoordinatorWithLimit(t, fixture, 8)
}

func TestRecoveryClaimAndTakeoverObserveRunningOnlyAfterCommit(t *testing.T) {
	fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptExecute)
	if _, err := fixture.service.Authorize(context.Background(), fixture.request); err != nil {
		t.Fatalf("execute recovery fixture: %v", err)
	}
	metrics := &recoveryMetricsSpy{}
	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	coordinator.metrics = metrics

	claim, found, err := coordinator.ClaimNext(context.Background(), "metrics-claim-worker")
	if err != nil || !found {
		t.Fatalf("claim recovery job: claim=%+v found=%t err=%v", claim, found, err)
	}
	var source model.RecoveryPointLease
	if err := fixture.db.Where("id = ?", claim.SourceFence.LeaseID).Take(&source).Error; err != nil {
		t.Fatal(err)
	}
	claimDeadline := reflect.ValueOf(claim).FieldByName("AbsoluteDeadline")
	if !claimDeadline.IsValid() || !claimDeadline.Interface().(time.Time).Equal(source.AbsoluteDeadline) {
		t.Fatalf("ClaimNext absolute deadline missing or changed: claim=%+v source=%s", claim, source.AbsoluteDeadline)
	}
	metrics.assertSingleState(t, backupasset.ProviderRestic, JobStateRunning)

	if duplicate, duplicateFound, duplicateErr := coordinator.ClaimNext(
		context.Background(), "metrics-lost-claim-worker",
	); duplicateErr != nil || duplicateFound || duplicate.JobID != "" {
		t.Fatalf("lost claim attempt=%+v found=%t err=%v", duplicate, duplicateFound, duplicateErr)
	}
	if premature, prematureFound, prematureErr := coordinator.TakeoverExpired(
		context.Background(), "metrics-premature-takeover-worker",
	); prematureErr != nil || prematureFound || premature.JobID != "" {
		t.Fatalf("premature takeover=%+v found=%t err=%v", premature, prematureFound, prematureErr)
	}
	if len(metrics.states) != 1 {
		t.Fatalf("lost claim or premature takeover emitted running metrics: %+v", metrics.states)
	}

	fixture.now = claim.LeaseExpiresAt.Add(time.Second)
	takeover, takeoverFound, takeoverErr := coordinator.TakeoverExpired(
		context.Background(), "metrics-committed-takeover-worker",
	)
	if takeoverErr != nil || !takeoverFound || takeover.JobID != claim.JobID || takeover.AttemptID == claim.AttemptID {
		t.Fatalf("committed takeover=%+v found=%t err=%v", takeover, takeoverFound, takeoverErr)
	}
	takeoverDeadline := reflect.ValueOf(takeover).FieldByName("AbsoluteDeadline")
	if !takeoverDeadline.IsValid() || !takeoverDeadline.Interface().(time.Time).Equal(source.AbsoluteDeadline) {
		t.Fatalf("takeover absolute deadline missing or changed: claim=%+v source=%s", takeover, source.AbsoluteDeadline)
	}
	if len(metrics.states) != 2 {
		t.Fatalf("running metrics=%+v, want one claim and one takeover", metrics.states)
	}
	for index, observation := range metrics.states {
		if observation.provider != backupasset.ProviderRestic || observation.state != JobStateRunning {
			t.Fatalf("running metric %d=%+v", index, observation)
		}
	}
}

func TestDeclaredWriteKeyBindsAttemptAndNodeFences(t *testing.T) {
	entryID := strings.Repeat("e", 32)
	base := RecoveryWorkerClaim{
		JobID: strings.Repeat("j", 32), AttemptID: strings.Repeat("a", 32),
		NodeLeaseID: strings.Repeat("n", 32), AttemptFence: 1, NodeFence: 1,
	}
	for name, changed := range map[string]RecoveryWorkerClaim{
		"attempt id":    {JobID: base.JobID, AttemptID: strings.Repeat("b", 32), NodeLeaseID: base.NodeLeaseID, AttemptFence: base.AttemptFence, NodeFence: base.NodeFence},
		"node lease":    {JobID: base.JobID, AttemptID: base.AttemptID, NodeLeaseID: strings.Repeat("m", 32), AttemptFence: base.AttemptFence, NodeFence: base.NodeFence},
		"attempt fence": {JobID: base.JobID, AttemptID: base.AttemptID, NodeLeaseID: base.NodeLeaseID, AttemptFence: 2, NodeFence: base.NodeFence},
		"node fence":    {JobID: base.JobID, AttemptID: base.AttemptID, NodeLeaseID: base.NodeLeaseID, AttemptFence: base.AttemptFence, NodeFence: 2},
	} {
		t.Run(name, func(t *testing.T) {
			if declaredWriteKey(base, entryID) == declaredWriteKey(changed, entryID) {
				t.Fatalf("declared write key ignored %s: base=%q changed=%q", name, declaredWriteKey(base, entryID), declaredWriteKey(changed, entryID))
			}
		})
	}
}

func recoveryRestartObservationForTest() TargetVerifyObservation {
	return TargetVerifyObservation{
		Kind: TargetPresencePresent,
		Present: &PresentObservation{
			IdentityDigest: strings.Repeat("c", sha256DigestLength),
			Bytes:          3,
		},
		ObservedRevision: "target-revision-e",
	}
}

func assertRecoveryRestartAdoptionUnchanged(
	t *testing.T,
	db *gorm.DB,
	wantJob model.BackupAssetRecoveryJob,
	wantItem model.BackupAssetRecoveryJobItem,
	wantCheckpointCount int64,
) {
	t.Helper()
	var checkpointCount int64
	if err := db.Model(&model.BackupAssetRecoveryCheckpoint{}).
		Where("job_id = ? AND sequence > ?", wantJob.ID, 0).Count(&checkpointCount).Error; err != nil {
		t.Fatal(err)
	}
	if checkpointCount != wantCheckpointCount-1 {
		t.Fatalf("failed target verification operation checkpoints=%d, want %d",
			checkpointCount, wantCheckpointCount-1)
	}

	var job model.BackupAssetRecoveryJob
	if err := db.Where("id = ?", wantJob.ID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	if job.State != wantJob.State || job.FailureCategory != wantJob.FailureCategory ||
		job.TransitionRevision != wantJob.TransitionRevision || job.WorkspacePhase != wantJob.WorkspacePhase ||
		job.WorkspaceOwner != wantJob.WorkspaceOwner || job.WorkspaceFence != wantJob.WorkspaceFence ||
		job.TargetChainRevision != wantJob.TargetChainRevision {
		t.Fatalf("failed target verification mutated job success/chain state: before=%+v after=%+v", wantJob, job)
	}

	var item model.BackupAssetRecoveryJobItem
	if err := db.Where("id = ?", wantItem.ID).Take(&item).Error; err != nil {
		t.Fatal(err)
	}
	if item.Outcome != wantItem.Outcome || item.FailureCategory != wantItem.FailureCategory ||
		item.BytesWritten != wantItem.BytesWritten || item.VerifiedSize != wantItem.VerifiedSize ||
		item.VerifiedDigest != wantItem.VerifiedDigest {
		t.Fatalf("failed target verification mutated item success projection: before=%+v after=%+v", wantItem, item)
	}
}

func newRecoveryWorkerCoordinatorWithLimit(
	t *testing.T,
	fixture *authorizationReceiptServiceFixture,
	scanLimit int,
) *WorkerCoordinator {
	t.Helper()
	sourceLeases, err := backupasset.NewLeaseService(
		fixture.db,
		func() time.Time { return fixture.now },
		backupasset.LeaseConfig{Duration: 10 * time.Minute, Heartbeat: time.Minute, AbsoluteDeadline: time.Hour},
	)
	if err != nil {
		t.Fatalf("new recovery worker source-lease service: %v", err)
	}
	coordinator, err := NewWorkerCoordinator(WorkerCoordinatorDependencies{
		DB:              fixture.db,
		SourceLeases:    sourceLeases,
		LiveRevalidator: fixture.service.liveRevalidator,
		WorkspaceKeys:   recoveryWorkerWorkspaceKeySource{},
		Target:          &recoveryRestartTargetFake{},
		SourceResolver: &recoveryAdoptionSourceResolverFake{
			source: &recoveryExecutionSourceFake{},
		},
		Now:       func() time.Time { return fixture.now },
		LeaseTTL:  10 * time.Minute,
		ScanLimit: scanLimit,
	})
	if err != nil {
		t.Fatalf("new recovery worker coordinator: %v", err)
	}
	return coordinator
}

func newRecoveryWorkerCoordinatorWithSourceResolver(
	t *testing.T,
	fixture *authorizationReceiptServiceFixture,
	resolver provider.RsyncRestoreSourceResolver,
) *WorkerCoordinator {
	t.Helper()
	sourceLeases, err := backupasset.NewLeaseService(
		fixture.db,
		func() time.Time { return fixture.now },
		backupasset.LeaseConfig{Duration: 10 * time.Minute, Heartbeat: time.Minute, AbsoluteDeadline: time.Hour},
	)
	if err != nil {
		t.Fatalf("new recovery worker source-lease service: %v", err)
	}
	dependencies := WorkerCoordinatorDependencies{
		DB:              fixture.db,
		SourceLeases:    sourceLeases,
		LiveRevalidator: fixture.service.liveRevalidator,
		WorkspaceKeys:   recoveryWorkerWorkspaceKeySource{},
		Target:          &recoveryRestartTargetFake{},
		Now:             func() time.Time { return fixture.now },
		LeaseTTL:        10 * time.Minute,
		ScanLimit:       8,
	}
	resolverField := reflect.ValueOf(&dependencies).Elem().FieldByName("SourceResolver")
	if !resolverField.IsValid() {
		t.Fatal("WorkerCoordinatorDependencies omits the Provider-owned SourceResolver")
	}
	if !resolverField.CanSet() || !reflect.TypeOf(resolver).AssignableTo(resolverField.Type()) {
		t.Fatalf("WorkerCoordinatorDependencies.SourceResolver has incompatible type %s", resolverField.Type())
	}
	resolverField.Set(reflect.ValueOf(resolver))
	coordinator, err := NewWorkerCoordinator(dependencies)
	if err != nil {
		t.Fatalf("new recovery worker coordinator with source resolver: %v", err)
	}
	return coordinator
}

func recoveryTargetChainAdvanceForTest(
	t *testing.T,
	job model.BackupAssetRecoveryJob,
	item model.BackupAssetRecoveryJobItem,
	claim RecoveryWorkerClaim,
) TargetChainAdvance {
	t.Helper()
	if item.PlanItemID == nil {
		t.Fatal("recovery job item has no frozen plan-item binding")
	}
	return TargetChainAdvance{
		PriorRevision:        job.TargetChainRevision,
		OperationDigest:      recoveryJobItemOperationDigestForTest(item),
		PlanItemID:           *item.PlanItemID,
		SourceRevisionDigest: job.SourceRevisionDigest,
		AttemptID:            claim.AttemptID,
		AttemptFence:         claim.AttemptFence,
		NodeFence:            claim.NodeFence,
		VerifiedIdentity:     item.ExpectedPostIdentityDigest,
		TargetRevision:       "target-revision-e",
	}
}
