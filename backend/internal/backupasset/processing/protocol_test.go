package processing

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset/content"
	"xirang/backend/internal/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestProtocolHandshakeStrictlyDecodesClosedCapabilityAdvertisement(t *testing.T) {
	request := validHandshakeRequest()
	payload, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeHandshakeRequest(payload)
	if err != nil {
		t.Fatalf("DecodeHandshakeRequest: %v", err)
	}
	registry, err := NewCapabilityRegistry([]CapabilityDefinition{{
		Capability: "noop", CapabilitySchema: "noop.v1", OutputProfile: "noop.v1", PipelineFingerprint: "noop-pipeline-v1",
	}})
	if err != nil {
		t.Fatal(err)
	}
	identity := WorkerTransportIdentity{Kind: WorkerTransportLocal, Fingerprint: strings.Repeat("a", 64)}
	validated, err := ValidateHandshake(identity, decoded, registry)
	if err != nil {
		t.Fatalf("ValidateHandshake: %v", err)
	}
	if validated.TransportFingerprint != identity.Fingerprint || validated.InstanceID != request.InstanceID || len(validated.Capabilities) != 1 ||
		validated.Capabilities[0].AdvertisementDigest == "" {
		t.Fatalf("validated handshake invalid: %+v", validated)
	}

	invalidPayloads := [][]byte{
		append(append([]byte(nil), payload...), []byte(` {}`)...),
		bytes.Replace(payload, []byte(`"schema_version":1`), []byte(`"schema_version":1,"schema_version":1`), 1),
		bytes.Replace(payload, []byte(`"protocol_version":1`), []byte(`"protocol_version":1,"unknown":true`), 1),
	}
	for index, invalid := range invalidPayloads {
		if _, err := DecodeHandshakeRequest(invalid); !errors.Is(err, ErrProtocolInvalid) {
			t.Fatalf("invalid handshake %d got %v", index, err)
		}
	}
}

func TestProductionCapabilityRegistryAcceptsOnlyClosedWorkerProfiles(t *testing.T) {
	registry := NewProductionCapabilityRegistry()
	advertisements := NewProductionWorkerCapabilitySet().Advertisements()
	want := []string{
		"image.thumbnail/raster_thumbnail_v1", "text.extract/bounded_text_v1", "image.ocr/tesseract_text_v1",
		"document.convert/static_pages_v1", "malware.scan/signature_scan_v1", "media.probe/media_probe_v1",
		"media.transcode/browser_preview_v1", "archive.inspect/archive_index_v1", "archive.extract_entry/archive_member_v1",
		"secret.classify/bounded_secret_v1",
	}
	if len(advertisements) != len(want) {
		t.Fatalf("production advertisements=%d, want %d: %+v", len(advertisements), len(want), advertisements)
	}
	for index, advertisement := range advertisements {
		if got := advertisement.Capability + "/" + advertisement.OutputProfile; got != want[index] {
			t.Fatalf("advertisement %d=%q, want %q", index, got, want[index])
		}
	}
	request := HandshakeRequest{
		SchemaVersion: 1, ProtocolVersion: WorkerProtocolVersion, InstanceID: strings.Repeat("1", 32), IdentityRevision: 1,
		InteractiveSlots: 2, BackgroundSlots: 2, Capabilities: advertisements,
	}
	identity := WorkerTransportIdentity{Kind: WorkerTransportLocal, Fingerprint: strings.Repeat("b", 64)}
	validated, err := ValidateHandshake(identity, request, registry)
	if err != nil || len(validated.Capabilities) != len(want) {
		t.Fatalf("closed production handshake=%+v err=%v", validated, err)
	}
	request.Capabilities[0].Capability = "caller.selected"
	if _, err := ValidateHandshake(identity, request, registry); !errors.Is(err, ErrProtocolCapabilityUnsupported) {
		t.Fatalf("production registry accepted caller-selected capability: %v", err)
	}
}

func TestProductionPipelineFingerprintsUseOnlyAffectedActiveBundles(t *testing.T) {
	base := NewProductionWorkerCapabilitySet().Advertisements()
	bundles := CapabilityBundleFingerprints{
		"image.ocr":    {strings.Repeat("a", 64)},
		"malware.scan": {strings.Repeat("b", 64)},
	}
	registry, err := NewProductionCapabilityRegistryWithBundles(bundles)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := NewProductionWorkerCapabilitySetWithBundles(bundles)
	if err != nil {
		t.Fatal(err)
	}
	advertisements := worker.Advertisements()
	if len(advertisements) != len(base) {
		t.Fatalf("bundle-aware advertisements=%d base=%d", len(advertisements), len(base))
	}
	changed := map[string]bool{}
	for index := range advertisements {
		if advertisements[index].PipelineFingerprint != base[index].PipelineFingerprint {
			changed[advertisements[index].Capability] = true
		}
	}
	if !reflect.DeepEqual(changed, map[string]bool{"image.ocr": true, "malware.scan": true}) {
		t.Fatalf("affected pipeline set=%v", changed)
	}
	request := HandshakeRequest{
		SchemaVersion: 1, ProtocolVersion: WorkerProtocolVersion, InstanceID: strings.Repeat("1", 32), IdentityRevision: 1,
		InteractiveSlots: 1, BackgroundSlots: 1, Capabilities: advertisements,
	}
	identity := WorkerTransportIdentity{Kind: WorkerTransportLocal, Fingerprint: strings.Repeat("c", 64)}
	if _, err := ValidateHandshake(identity, request, registry); err != nil {
		t.Fatalf("matching bundle handshake: %v", err)
	}
	request.Capabilities = base
	if _, err := ValidateHandshake(identity, request, registry); !errors.Is(err, ErrProtocolCapabilityUnsupported) {
		t.Fatalf("stale bundle advertisement error=%v", err)
	}
	if _, err := NewProductionCapabilityRegistryWithBundles(CapabilityBundleFingerprints{
		"caller.selected": {strings.Repeat("d", 64)},
	}); !errors.Is(err, ErrProtocolInvalid) {
		t.Fatalf("unknown bundle capability error=%v", err)
	}
}

func TestProtocolContractsContainNoProviderCredentialPathOrUpdaterFields(t *testing.T) {
	request := validHandshakeRequest()
	payload, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	lower := strings.ToLower(string(payload))
	for _, forbidden := range []string{
		"dsn", "provider_kind", "provider_locator", "repository", "ssh", "restic", "rclone",
		"credential", "host_path", "native_path", "filename", "query", "updater", "url", "stdout", "stderr",
	} {
		if strings.Contains(lower, forbidden) {
			t.Fatalf("protocol payload contains forbidden boundary %q: %s", forbidden, payload)
		}
	}
}

func TestWorkerProtocolDTOAndSourceBoundaryExcludesPrivilegedFields(t *testing.T) {
	forbiddenFields := []string{
		"databasedsn", "providerkind", "providerlocator", "repository", "taskbinding",
		"sshprivatekey", "resticpassword", "rcloneconfig", "credential", "hostpath",
		"nativepath", "originalfilename", "userquery", "updater", "arbitraryurl",
		"rawoutput", "stdout", "stderr", "bundlebytes",
	}
	allowedSecrets := map[string]bool{
		"WorkerActivationMaterial.Secret":   false,
		"WorkerInputActivateRequest.Secret": false,
		"WorkerSinkActivateRequest.Secret":  false,
	}
	for _, path := range []string{"protocol.go", "contracts.go", "derived_manifest.go", "worker_client.go"} {
		syntax, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatalf("parse Worker protocol DTO source %s: %v", path, err)
		}
		for _, declaration := range syntax.Decls {
			general, ok := declaration.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, specification := range general.Specs {
				typed, ok := specification.(*ast.TypeSpec)
				if !ok {
					continue
				}
				structure, ok := typed.Type.(*ast.StructType)
				if !ok {
					continue
				}
				for _, field := range structure.Fields.List {
					if field.Tag == nil || len(field.Names) == 0 {
						continue
					}
					rawTag, err := strconv.Unquote(field.Tag.Value)
					if err != nil {
						t.Fatalf("decode %s.%s tag: %v", typed.Name.Name, field.Names[0].Name, err)
					}
					jsonName := strings.Split(reflect.StructTag(rawTag).Get("json"), ",")[0]
					if jsonName == "" || jsonName == "-" {
						continue
					}
					for _, fieldName := range field.Names {
						qualified := typed.Name.Name + "." + fieldName.Name
						identifier := workerBoundaryIdentifier(fieldName.Name + jsonName)
						for _, forbidden := range forbiddenFields {
							if strings.Contains(identifier, forbidden) {
								t.Fatalf("Worker protocol DTO %s exposes forbidden boundary %q", qualified, forbidden)
							}
						}
						if strings.Contains(identifier, "secret") {
							if _, ok := allowedSecrets[qualified]; !ok || jsonName != "secret" {
								t.Fatalf("Worker protocol DTO %s exposes non-activation secret field %q", qualified, jsonName)
							}
							allowedSecrets[qualified] = true
						}
					}
				}
			}
		}
	}
	for qualified, seen := range allowedSecrets {
		if !seen {
			t.Fatalf("expected one-use activation field %s was not inspected", qualified)
		}
	}

	modelPath := "../../model/backup_asset_processing.go"
	modelSyntax, err := parser.ParseFile(token.NewFileSet(), modelPath, nil, 0)
	if err != nil {
		t.Fatalf("parse processing persistence model: %v", err)
	}
	ast.Inspect(modelSyntax, func(node ast.Node) bool {
		field, ok := node.(*ast.Field)
		if !ok || len(field.Names) == 0 {
			return true
		}
		for _, name := range field.Names {
			if !strings.Contains(workerBoundaryIdentifier(name.Name), "secret") {
				continue
			}
			if !strings.HasSuffix(name.Name, "Hash") || field.Tag == nil {
				t.Errorf("processing persistence model contains plaintext-capable secret field %s", name.Name)
				continue
			}
			rawTag, tagErr := strconv.Unquote(field.Tag.Value)
			if tagErr != nil || reflect.StructTag(rawTag).Get("json") != "-" {
				t.Errorf("processing secret hash %s is not private JSON", name.Name)
			}
		}
		return true
	})

	for _, path := range []string{"worker_client.go", "../../api/worker_router.go", "../../../cmd/asset-worker/main.go"} {
		syntax, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse Worker execution source %s: %v", path, err)
		}
		for _, imported := range syntax.Imports {
			importPath, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				t.Fatalf("decode Worker import in %s: %v", path, err)
			}
			for _, forbidden := range []string{
				"/internal/backupasset/provider", "/internal/backupasset/repository",
				"/internal/database", "/internal/task", "gorm.io/",
			} {
				if strings.Contains(importPath, forbidden) {
					t.Fatalf("Worker execution source %s imports privileged boundary %s", path, importPath)
				}
			}
		}
	}
}

func workerBoundaryIdentifier(value string) string {
	var normalized strings.Builder
	for _, character := range strings.ToLower(value) {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' {
			normalized.WriteRune(character)
		}
	}
	return normalized.String()
}

func TestCapabilityAdvertisementRejectsAmbiguousModesLimitsAndIdentity(t *testing.T) {
	registry, err := NewCapabilityRegistry([]CapabilityDefinition{{
		Capability: "noop", CapabilitySchema: "noop.v1", OutputProfile: "noop.v1", PipelineFingerprint: "noop-pipeline-v1",
	}})
	if err != nil {
		t.Fatal(err)
	}
	identity := WorkerTransportIdentity{Kind: WorkerTransportLocal, Fingerprint: strings.Repeat("c", 64)}
	mutations := []func(*HandshakeRequest){
		func(value *HandshakeRequest) { value.InstanceID = "claimed-worker" },
		func(value *HandshakeRequest) { value.IdentityRevision = 0 },
		func(value *HandshakeRequest) {
			value.Capabilities[0].InputModes = []ProtocolInputMode{ProtocolInputRange, ProtocolInputRange}
		},
		func(value *HandshakeRequest) { value.Capabilities[0].Limits.MaxOutputCount = 0 },
		func(value *HandshakeRequest) {
			value.Capabilities[0].Limits.MaxOutputBytes = value.Capabilities[0].Limits.MaxInputBytes + 1
		},
	}
	for index, mutate := range mutations {
		request := validHandshakeRequest()
		mutate(&request)
		if _, err := ValidateHandshake(identity, request, registry); !errors.Is(err, ErrProtocolInvalid) {
			t.Fatalf("invalid advertisement %d got %v", index, err)
		}
	}
}

func TestProtocolServicePersistsOnlyTransportDerivedWorkerIdentity(t *testing.T) {
	db, service := newProtocolServiceHarness(t)
	request := validHandshakeRequest()
	identity := WorkerTransportIdentity{Kind: WorkerTransportLocal, Fingerprint: strings.Repeat("d", 64), PeerUID: 1000, PeerPID: 123}
	registered, err := service.Handshake(context.Background(), identity, request)
	if err != nil {
		t.Fatalf("Handshake: %v", err)
	}
	if registered.WorkerID == "" || registered.TrustState != "active" || registered.CapabilityCount != 1 {
		t.Fatalf("registered Worker invalid: %+v", registered)
	}
	var row model.BackupAssetWorkerIdentity
	if err := db.First(&row, "id = ?", registered.WorkerID).Error; err != nil {
		t.Fatal(err)
	}
	if row.TransportKind != string(WorkerTransportLocal) || row.TransportFingerprint != identity.Fingerprint || row.InstanceID != request.InstanceID {
		t.Fatalf("persisted identity is self-reported or contains peer process facts: %+v", row)
	}
	again, err := service.Handshake(context.Background(), identity, request)
	if err != nil || again.WorkerID != registered.WorkerID {
		t.Fatalf("same authenticated identity was not idempotent: first=%+v next=%+v err=%v", registered, again, err)
	}
	changed := identity
	changed.Fingerprint = strings.Repeat("e", 64)
	if _, err := service.Handshake(context.Background(), changed, request); !errors.Is(err, ErrWorkerUnauthenticated) {
		t.Fatalf("identity inherited trust across fingerprint revision: %v", err)
	}
}

func TestProtocolServiceRemoteRestartAndRevisionCannotInheritActiveAuthority(t *testing.T) {
	db, service := newProtocolServiceHarness(t)
	workerID := strings.Repeat("b", 32)
	firstIdentity := WorkerTransportIdentity{
		Kind: WorkerTransportMTLS, Fingerprint: strings.Repeat("c", 64), WorkerID: workerID,
	}
	firstRequest := validHandshakeRequest()
	registered, err := service.Handshake(context.Background(), firstIdentity, firstRequest)
	if err != nil || registered.WorkerID != workerID {
		t.Fatalf("initial remote Handshake=%+v err=%v", registered, err)
	}
	now := time.Date(2026, 7, 19, 8, 9, 10, 0, time.UTC)
	attempt := model.BackupAssetProcessingAttempt{
		ID: strings.Repeat("d", 32), JobID: strings.Repeat("e", 32), AttemptNumber: 1,
		WorkerID: workerID, SlotClass: string(SlotInteractive), State: "active",
		WorkerLeaseExpiresAt: now.Add(time.Minute), LastHeartbeatAt: now,
		RecoveryPointLeaseID: strings.Repeat("f", 32), RecoveryPointAttemptID: strings.Repeat("1", 32),
		RecoveryPointFenceHash: strings.Repeat("2", 64), AbsoluteDeadline: now.Add(time.Hour),
		IsCurrent: true, StartedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&attempt).Error; err != nil {
		t.Fatal(err)
	}
	restartRequest := firstRequest
	restartRequest.InstanceID = strings.Repeat("3", 32)
	if _, err := service.Handshake(context.Background(), firstIdentity, restartRequest); !errors.Is(err, ErrWorkerUnauthenticated) {
		t.Fatalf("remote restart inherited active authority: %v", err)
	}
	if err := db.Model(&model.BackupAssetProcessingAttempt{}).Where("id = ?", attempt.ID).
		Updates(map[string]any{"state": "failed", "is_current": false, "finished_at": now}).Error; err != nil {
		t.Fatal(err)
	}
	restarted, err := service.Handshake(context.Background(), firstIdentity, restartRequest)
	if err != nil || restarted.WorkerID != workerID {
		t.Fatalf("idle remote restart=%+v err=%v", restarted, err)
	}
	replacementIdentity := firstIdentity
	replacementIdentity.Fingerprint = strings.Repeat("4", 64)
	replacementRequest := restartRequest
	replacementRequest.InstanceID = strings.Repeat("5", 32)
	replacementRequest.IdentityRevision = 2
	replaced, err := service.Handshake(context.Background(), replacementIdentity, replacementRequest)
	if err != nil || replaced.WorkerID != workerID {
		t.Fatalf("explicit remote identity revision=%+v err=%v", replaced, err)
	}
	if err := service.AuthenticateWorker(context.Background(), firstIdentity, workerID, restartRequest.InstanceID); !errors.Is(err, ErrWorkerUnauthenticated) {
		t.Fatalf("old remote certificate retained authority: %v", err)
	}
	var row model.BackupAssetWorkerIdentity
	if err := db.First(&row, "id = ?", workerID).Error; err != nil {
		t.Fatal(err)
	}
	if row.TransportFingerprint != replacementIdentity.Fingerprint || row.InstanceID != replacementRequest.InstanceID || row.IdentityRevision != 2 {
		t.Fatalf("remote revision was not persisted atomically: %+v", row)
	}
}

func TestProtocolServiceAuthenticatesEveryRequestAgainstTransportAndInstance(t *testing.T) {
	_, service := newProtocolServiceHarness(t)
	identity := WorkerTransportIdentity{Kind: WorkerTransportLocal, Fingerprint: strings.Repeat("7", 64), PeerUID: 1000}
	request := validHandshakeRequest()
	registered, err := service.Handshake(context.Background(), identity, request)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.AuthenticateWorker(context.Background(), identity, registered.WorkerID, request.InstanceID); err != nil {
		t.Fatalf("AuthenticateWorker: %v", err)
	}
	changedFingerprint := identity
	changedFingerprint.Fingerprint = strings.Repeat("8", 64)
	for name, candidate := range map[string]struct {
		identity   WorkerTransportIdentity
		workerID   string
		instanceID string
	}{
		"fingerprint": {changedFingerprint, registered.WorkerID, request.InstanceID},
		"worker":      {identity, strings.Repeat("9", 32), request.InstanceID},
		"instance":    {identity, registered.WorkerID, strings.Repeat("a", 32)},
	} {
		t.Run(name, func(t *testing.T) {
			if err := service.AuthenticateWorker(context.Background(), candidate.identity, candidate.workerID, candidate.instanceID); !errors.Is(err, ErrWorkerUnauthenticated) {
				t.Fatalf("mismatched request identity got %v", err)
			}
		})
	}
	if err := service.SetDraining(context.Background(), registered.WorkerID); err != nil {
		t.Fatal(err)
	}
	if err := service.AuthenticateWorker(context.Background(), identity, registered.WorkerID, request.InstanceID); err != nil {
		t.Fatalf("draining Worker lost authenticated access to current work: %v", err)
	}
}

func TestWorkerProtocolServicePullAuthenticatesAndReturnsAtomicSafeEnvelope(t *testing.T) {
	harness := newCoordinatorHarness(t)
	workerID := harness.registerNoopWorker(t, "6")
	work, err := harness.coordinator.RequestWork(context.Background(), WorkRequest{
		Descriptor: validWorkDescriptor(),
		Interest: InterestRequest{
			OwnerKind: InterestSystem, OwnerKey: "protocol-pull",
			PriorityClass: PriorityInteractive, Priority: 100,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	protocol, err := NewProtocolService(harness.db, NewProductionCapabilityRegistry(), harness.clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	grants, err := NewGrantService(harness.db, harness.coordinator.leaseService, harness.clock.Now, GrantConfig{
		TTL: 30 * time.Second,
		InputLimits: GrantLimits{
			MaxRequests: 8, MaxBytesPerRequest: 64, MaxCumulativeBytes: 256, MaxInFlight: 2,
		},
		SinkLimits: GrantLimits{
			MaxRequests: 4, MaxBytesPerRequest: 128, MaxCumulativeBytes: 512, MaxInFlight: 1,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewWorkerProtocolService(protocol, harness.coordinator, grants, workerInputBrokerStub{}, workerArtifactSinkStub{})
	if err != nil {
		t.Fatal(err)
	}
	identity := WorkerTransportIdentity{
		Kind: WorkerTransportLocal, Fingerprint: strings.Repeat("6", 64), PeerUID: 1000,
	}
	envelope, err := service.Pull(context.Background(), identity, WorkerPullRequest{
		SchemaVersion: 1, WorkerID: workerID, InstanceID: strings.Repeat("6", 32),
	})
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if envelope.SchemaVersion != 1 || envelope.ProtocolVersion != WorkerProtocolVersion ||
		envelope.JobID != work.JobID || envelope.AttemptID == "" || envelope.TransitionRevision != 2 ||
		envelope.Descriptor.Source != validWorkDescriptor().Source ||
		envelope.RecoveryPointFence.LeaseID == "" || envelope.RecoveryPointFence.FenceToken == "" ||
		!envelope.EffectiveLeaseExpiresAt.Equal(minimumTime(envelope.WorkerLeaseExpiresAt, envelope.RecoveryPointLeaseExpiresAt)) ||
		envelope.InputActivation.GrantID == "" || envelope.InputActivation.Secret == "" ||
		envelope.SinkActivation.GrantID == "" || envelope.SinkActivation.Secret == "" ||
		envelope.InputActivation.Secret == envelope.SinkActivation.Secret {
		t.Fatalf("unsafe or incomplete pull envelope: %+v", envelope)
	}

	var rows []model.BackupAssetProcessingGrant
	if err := harness.db.Where("attempt_id = ?", envelope.AttemptID).Order("kind ASC").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("atomic pull persisted %d grants, want 2", len(rows))
	}
	secrets := map[string]string{
		string(GrantInput): envelope.InputActivation.Secret,
		string(GrantSink):  envelope.SinkActivation.Secret,
	}
	for _, row := range rows {
		secret := secrets[row.Kind]
		digest := sha256.Sum256([]byte(secret))
		if secret == "" || row.ActivationSecretHash != fmt.Sprintf("%x", digest[:]) || row.ActivationSecretHash == secret {
			t.Fatalf("grant did not persist hash-only activation material: %+v", row)
		}
	}

	wrongIdentity := identity
	wrongIdentity.Fingerprint = strings.Repeat("7", 64)
	if _, err := service.Pull(context.Background(), wrongIdentity, WorkerPullRequest{
		SchemaVersion: 1, WorkerID: workerID, InstanceID: strings.Repeat("6", 32),
	}); !errors.Is(err, ErrWorkerUnauthenticated) {
		t.Fatalf("follow-up pull trusted a changed transport: %v", err)
	}
}

func TestWorkerProtocolStopAcceptingRejectsNewAdmissionButAllowsHeartbeatGrace(t *testing.T) {
	harness := newCoordinatorHarness(t)
	workerID := harness.registerNoopWorker(t, "5")
	if _, err := harness.coordinator.RequestWork(context.Background(), WorkRequest{
		Descriptor: validWorkDescriptor(),
		Interest: InterestRequest{
			OwnerKind: InterestSystem, OwnerKey: "protocol-drain",
			PriorityClass: PriorityInteractive, Priority: 100,
		},
	}); err != nil {
		t.Fatal(err)
	}
	registry, err := NewCapabilityRegistry([]CapabilityDefinition{{
		Capability: "noop", CapabilitySchema: "noop.v1",
		PipelineFingerprint: "pipeline-fingerprint-v1", OutputProfile: "noop.v1",
	}})
	if err != nil {
		t.Fatal(err)
	}
	protocol, err := NewProtocolService(harness.db, registry, harness.clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	grants, err := NewGrantService(harness.db, harness.coordinator.leaseService, harness.clock.Now, GrantConfig{
		TTL:         30 * time.Second,
		InputLimits: GrantLimits{MaxRequests: 1, MaxBytesPerRequest: 1, MaxCumulativeBytes: 1, MaxInFlight: 1},
		SinkLimits:  GrantLimits{MaxRequests: 1, MaxBytesPerRequest: 1, MaxCumulativeBytes: 1, MaxInFlight: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewWorkerProtocolService(protocol, harness.coordinator, grants, workerInputBrokerStub{}, workerArtifactSinkStub{})
	if err != nil {
		t.Fatal(err)
	}
	identity := WorkerTransportIdentity{Kind: WorkerTransportLocal, Fingerprint: strings.Repeat("5", 64), PeerUID: 1000}
	pull := WorkerPullRequest{SchemaVersion: 1, WorkerID: workerID, InstanceID: strings.Repeat("5", 32)}
	envelope, err := service.Pull(context.Background(), identity, pull)
	if err != nil {
		t.Fatal(err)
	}

	service.StopAccepting()
	if _, err := service.Handshake(context.Background(), identity, validHandshakeRequest()); !errors.Is(err, ErrProtocolUnavailable) {
		t.Fatalf("handshake after StopAccepting got %v", err)
	}
	if _, err := service.Pull(context.Background(), identity, pull); !errors.Is(err, ErrProtocolUnavailable) {
		t.Fatalf("pull after StopAccepting got %v", err)
	}
	heartbeat, err := service.Heartbeat(context.Background(), identity, WorkerHeartbeatRequest{
		SchemaVersion: 1, WorkerID: workerID, InstanceID: strings.Repeat("5", 32), AttemptID: envelope.AttemptID,
	})
	if err != nil {
		t.Fatalf("heartbeat grace after StopAccepting: %v", err)
	}
	if !heartbeat.WorkerDraining {
		t.Fatal("heartbeat grace did not tell the existing Worker to drain")
	}
}

func TestWorkerProtocolShutdownClosesSessionsAndRejectsFurtherCalls(t *testing.T) {
	harness := newCoordinatorHarness(t)
	protocol, err := NewProtocolService(harness.db, NewProductionCapabilityRegistry(), harness.clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	grants, err := NewGrantService(harness.db, harness.coordinator.leaseService, harness.clock.Now, GrantConfig{
		TTL:         30 * time.Second,
		InputLimits: GrantLimits{MaxRequests: 1, MaxBytesPerRequest: 1, MaxCumulativeBytes: 1, MaxInFlight: 1},
		SinkLimits:  GrantLimits{MaxRequests: 1, MaxBytesPerRequest: 1, MaxCumulativeBytes: 1, MaxInFlight: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewWorkerProtocolService(protocol, harness.coordinator, grants, workerInputBrokerStub{}, workerArtifactSinkStub{})
	if err != nil {
		t.Fatal(err)
	}
	input := &workerInputReadSession{payload: []byte("bounded")}
	sessionID := strings.Repeat("a", 32)
	service.inputSessions[sessionID] = workerInputSession{session: input}
	service.sinkSessions[strings.Repeat("b", 32)] = workerSinkSession{}

	if err := service.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if !input.closed || len(service.inputSessions) != 0 || len(service.sinkSessions) != 0 {
		t.Fatalf("shutdown left sessions open: input_closed=%t input=%d sink=%d", input.closed, len(service.inputSessions), len(service.sinkSessions))
	}
	if _, err := service.OpenInput(context.Background(), WorkerTransportIdentity{}, WorkerInputReadRequest{
		SchemaVersion: 1, WorkerID: strings.Repeat("c", 32), InstanceID: strings.Repeat("d", 32),
		SessionID: sessionID, Mode: content.SourceModeSequential, Length: 1,
	}); !errors.Is(err, ErrProtocolUnavailable) {
		t.Fatalf("input call after shutdown got %v", err)
	}
}

func TestWorkerProtocolShutdownWaitsForInFlightCalls(t *testing.T) {
	harness := newCoordinatorHarness(t)
	workerID := harness.registerNoopWorker(t, "4")
	protocol, err := NewProtocolService(harness.db, NewProductionCapabilityRegistry(), harness.clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	grants, err := NewGrantService(harness.db, harness.coordinator.leaseService, harness.clock.Now, GrantConfig{
		TTL:         30 * time.Second,
		InputLimits: GrantLimits{MaxRequests: 1, MaxBytesPerRequest: 1, MaxCumulativeBytes: 1, MaxInFlight: 1},
		SinkLimits:  GrantLimits{MaxRequests: 1, MaxBytesPerRequest: 1, MaxCumulativeBytes: 1, MaxInFlight: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewWorkerProtocolService(protocol, harness.coordinator, grants, workerInputBrokerStub{}, workerArtifactSinkStub{})
	if err != nil {
		t.Fatal(err)
	}
	input := &blockingWorkerInputSession{started: make(chan struct{}), release: make(chan struct{})}
	sessionID := strings.Repeat("3", 32)
	service.inputSessions[sessionID] = workerInputSession{
		session: input, workerID: workerID, instanceID: strings.Repeat("4", 32),
		expiresAt: harness.clock.Now().Add(time.Minute),
	}
	identity := WorkerTransportIdentity{Kind: WorkerTransportLocal, Fingerprint: strings.Repeat("4", 64), PeerUID: 1000}
	readDone := make(chan error, 1)
	go func() {
		_, readErr := service.OpenInput(context.Background(), identity, WorkerInputReadRequest{
			SchemaVersion: 1, WorkerID: workerID, InstanceID: strings.Repeat("4", 32),
			SessionID: sessionID, Mode: content.SourceModeSequential, Length: 1,
		})
		readDone <- readErr
	}()
	<-input.started

	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- service.Shutdown(context.Background()) }()
	select {
	case err := <-shutdownDone:
		t.Fatalf("Shutdown returned before the in-flight call completed: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(input.release)
	if err := <-readDone; err != nil {
		t.Fatalf("in-flight input call failed during drain: %v", err)
	}
	if err := <-shutdownDone; err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if !input.closed {
		t.Fatal("Shutdown did not close the drained input session")
	}
}

func TestWorkerProtocolServiceActivatesOneUseInputAndOwnsBoundedSession(t *testing.T) {
	harness := newCoordinatorHarness(t)
	workerID := harness.registerNoopWorker(t, "8")
	work, err := harness.coordinator.RequestWork(context.Background(), WorkRequest{
		Descriptor: validWorkDescriptor(),
		Interest: InterestRequest{
			OwnerKind: InterestSystem, OwnerKey: "protocol-input",
			PriorityClass: PriorityInteractive, Priority: 100,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	protocol, err := NewProtocolService(harness.db, NewProductionCapabilityRegistry(), harness.clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	grants, err := NewGrantService(harness.db, harness.coordinator.leaseService, harness.clock.Now, GrantConfig{
		TTL: 30 * time.Second,
		InputLimits: GrantLimits{
			MaxRequests: 8, MaxBytesPerRequest: 64, MaxCumulativeBytes: 256, MaxInFlight: 2,
		},
		SinkLimits: GrantLimits{
			MaxRequests: 4, MaxBytesPerRequest: 128, MaxCumulativeBytes: 512, MaxInFlight: 1,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	input := &workerInputBrokerCapture{
		info: content.AttemptSourceInfo{
			Size: 36, MediaType: "application/octet-stream", FingerprintStrong: true,
			Sequential: true, Range: true,
		},
		session: workerInputSessionStub{},
	}
	service, err := NewWorkerProtocolService(protocol, harness.coordinator, grants, input, workerArtifactSinkStub{})
	if err != nil {
		t.Fatal(err)
	}
	identity := WorkerTransportIdentity{
		Kind: WorkerTransportLocal, Fingerprint: strings.Repeat("8", 64), PeerUID: 1000,
	}
	envelope, err := service.Pull(context.Background(), identity, WorkerPullRequest{
		SchemaVersion: 1, WorkerID: workerID, InstanceID: strings.Repeat("8", 32),
	})
	if err != nil {
		t.Fatal(err)
	}
	request := WorkerInputActivateRequest{
		SchemaVersion: 1, WorkerID: workerID, InstanceID: strings.Repeat("8", 32),
		JobID: work.JobID, AttemptID: envelope.AttemptID, ExpectedRevision: envelope.TransitionRevision,
		GrantID: envelope.InputActivation.GrantID, Secret: envelope.InputActivation.Secret,
	}
	wrongIdentity := identity
	wrongIdentity.Fingerprint = strings.Repeat("9", 64)
	if _, err := service.ActivateInput(context.Background(), wrongIdentity, request); !errors.Is(err, ErrWorkerUnauthenticated) {
		t.Fatalf("input activation trusted changed transport: %v", err)
	}
	var before model.BackupAssetProcessingGrant
	if err := harness.db.First(&before, "id = ?", request.GrantID).Error; err != nil {
		t.Fatal(err)
	}
	if before.State != string(GrantIssued) || input.calls != 0 {
		t.Fatalf("unauthenticated activation consumed state: grant=%+v calls=%d", before, input.calls)
	}

	activated, err := service.ActivateInput(context.Background(), identity, request)
	if err != nil {
		t.Fatalf("ActivateInput: %v", err)
	}
	if activated.SchemaVersion != 1 || activated.SessionID != request.GrantID ||
		activated.TransitionRevision != envelope.TransitionRevision+1 || activated.Source.Size != 36 ||
		activated.Source.MediaType != "application/octet-stream" || !activated.Source.FingerprintStrong ||
		!activated.Source.Sequential || !activated.Source.Range || input.calls != 1 {
		t.Fatalf("input activation response invalid: response=%+v calls=%d", activated, input.calls)
	}
	var job model.BackupAssetProcessingJob
	if err := harness.db.First(&job, "id = ?", work.JobID).Error; err != nil {
		t.Fatal(err)
	}
	binding := input.binding
	if binding.SessionID != request.GrantID || binding.Ref != validWorkDescriptor().Source ||
		binding.CatalogGenerationID != validWorkDescriptor().CatalogGenerationID ||
		binding.SourceFingerprint != validWorkDescriptor().SourceFingerprint ||
		binding.EntryFingerprint != validWorkDescriptor().EntryFingerprint ||
		binding.Limits.MaxRequests != 8 || binding.Limits.MaxBytesPerRequest != 64 ||
		binding.Limits.MaxCumulativeBytes != 256 || binding.Limits.MaxInFlight != 2 ||
		!binding.AbsoluteExpiresAt.Equal(job.AbsoluteDeadline.UTC()) || !activated.ExpiresAt.Before(binding.AbsoluteExpiresAt) {
		t.Fatalf("input broker binding lost an exact bounded contract: %+v", binding)
	}
	var grant model.BackupAssetProcessingGrant
	if err := harness.db.First(&grant, "id = ?", request.GrantID).Error; err != nil {
		t.Fatal(err)
	}
	if job.State != string(ProcessingFetching) || job.TransitionRevision != envelope.TransitionRevision+1 ||
		grant.State != string(GrantActive) || grant.ActivationSecretHash != "" {
		t.Fatalf("input activation did not close state and grant together: job=%+v grant=%+v", job, grant)
	}
	encoded, err := json.Marshal(activated)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"source_fingerprint", "entry_fingerprint", "provider", "locator", "path"} {
		if strings.Contains(strings.ToLower(string(encoded)), forbidden) {
			t.Fatalf("input activation response leaked %q: %s", forbidden, encoded)
		}
	}
	if _, err := service.ActivateInput(context.Background(), identity, request); !errors.Is(err, ErrGrantDenied) {
		t.Fatalf("input activation replay got %v", err)
	}
	if input.calls != 1 {
		t.Fatalf("replayed activation reopened source: calls=%d", input.calls)
	}
}

func TestWorkerProtocolServiceInputReadsReauthenticateAndStaySessionBound(t *testing.T) {
	harness := newCoordinatorHarness(t)
	workerID := harness.registerNoopWorker(t, "a")
	work, err := harness.coordinator.RequestWork(context.Background(), WorkRequest{
		Descriptor: validWorkDescriptor(),
		Interest: InterestRequest{
			OwnerKind: InterestSystem, OwnerKey: "protocol-read",
			PriorityClass: PriorityInteractive, Priority: 100,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	protocol, err := NewProtocolService(harness.db, NewProductionCapabilityRegistry(), harness.clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	grants, err := NewGrantService(harness.db, harness.coordinator.leaseService, harness.clock.Now, GrantConfig{
		TTL: 30 * time.Second,
		InputLimits: GrantLimits{
			MaxRequests: 8, MaxBytesPerRequest: 64, MaxCumulativeBytes: 256, MaxInFlight: 2,
		},
		SinkLimits: GrantLimits{
			MaxRequests: 4, MaxBytesPerRequest: 128, MaxCumulativeBytes: 512, MaxInFlight: 1,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	readSession := &workerInputReadSession{payload: []byte("0123456789")}
	input := &workerInputBrokerCapture{
		info: content.AttemptSourceInfo{
			Size: 10, MediaType: "application/octet-stream", FingerprintStrong: true,
			Sequential: true, Range: true,
		},
		session: readSession,
	}
	service, err := NewWorkerProtocolService(protocol, harness.coordinator, grants, input, workerArtifactSinkStub{})
	if err != nil {
		t.Fatal(err)
	}
	identity := WorkerTransportIdentity{
		Kind: WorkerTransportLocal, Fingerprint: strings.Repeat("a", 64), PeerUID: 1000,
	}
	envelope, err := service.Pull(context.Background(), identity, WorkerPullRequest{
		SchemaVersion: 1, WorkerID: workerID, InstanceID: strings.Repeat("a", 32),
	})
	if err != nil {
		t.Fatal(err)
	}
	activation, err := service.ActivateInput(context.Background(), identity, WorkerInputActivateRequest{
		SchemaVersion: 1, WorkerID: workerID, InstanceID: strings.Repeat("a", 32),
		JobID: work.JobID, AttemptID: envelope.AttemptID, ExpectedRevision: envelope.TransitionRevision,
		GrantID: envelope.InputActivation.GrantID, Secret: envelope.InputActivation.Secret,
	})
	if err != nil {
		t.Fatal(err)
	}
	rangeRequest := WorkerInputReadRequest{
		SchemaVersion: 1, WorkerID: workerID, InstanceID: strings.Repeat("a", 32),
		SessionID: activation.SessionID, Mode: content.SourceModeRange, Offset: 2, Length: 4,
	}
	wrongIdentity := identity
	wrongIdentity.Fingerprint = strings.Repeat("b", 64)
	if _, err := service.OpenInput(context.Background(), wrongIdentity, rangeRequest); !errors.Is(err, ErrWorkerUnauthenticated) {
		t.Fatalf("input read trusted changed transport: %v", err)
	}
	if readSession.rangeCalls != 0 {
		t.Fatalf("unauthenticated read reached session: calls=%d", readSession.rangeCalls)
	}
	reader, err := service.OpenInput(context.Background(), identity, rangeRequest)
	if err != nil {
		t.Fatalf("OpenInput(range): %v", err)
	}
	payload, err := io.ReadAll(reader)
	if closeErr := reader.Close(); err == nil {
		err = closeErr
	}
	if err != nil || string(payload) != "2345" || readSession.rangeCalls != 1 {
		t.Fatalf("range read payload=%q calls=%d err=%v", payload, readSession.rangeCalls, err)
	}
	sequentialRequest := rangeRequest
	sequentialRequest.Mode = content.SourceModeSequential
	sequentialRequest.Offset = 0
	sequentialRequest.Length = 3
	reader, err = service.OpenInput(context.Background(), identity, sequentialRequest)
	if err != nil {
		t.Fatalf("OpenInput(sequential): %v", err)
	}
	payload, err = io.ReadAll(reader)
	if closeErr := reader.Close(); err == nil {
		err = closeErr
	}
	if err != nil || string(payload) != "012" || readSession.sequentialCalls != 1 {
		t.Fatalf("sequential read payload=%q calls=%d err=%v", payload, readSession.sequentialCalls, err)
	}
	invalid := rangeRequest
	invalid.Mode = content.SourceMode("materialize")
	if _, err := service.OpenInput(context.Background(), identity, invalid); !errors.Is(err, ErrProtocolInvalid) {
		t.Fatalf("invalid input mode got %v", err)
	}
	otherSession := rangeRequest
	otherSession.SessionID = strings.Repeat("f", 32)
	if _, err := service.OpenInput(context.Background(), identity, otherSession); !errors.Is(err, ErrGrantDenied) {
		t.Fatalf("unowned input session got %v", err)
	}
}

func TestWorkerProtocolServiceControlsHeartbeatTransitionsCancelAndDrain(t *testing.T) {
	harness := newCoordinatorHarness(t)
	workerID := harness.registerNoopWorker(t, "b")
	work, err := harness.coordinator.RequestWork(context.Background(), WorkRequest{
		Descriptor: validWorkDescriptor(),
		Interest: InterestRequest{
			OwnerKind: InterestSystem, OwnerKey: "protocol-control",
			PriorityClass: PriorityInteractive, Priority: 100,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	protocol, err := NewProtocolService(harness.db, NewProductionCapabilityRegistry(), harness.clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	grants, err := NewGrantService(harness.db, harness.coordinator.leaseService, harness.clock.Now, GrantConfig{
		TTL: 30 * time.Second,
		InputLimits: GrantLimits{
			MaxRequests: 8, MaxBytesPerRequest: 64, MaxCumulativeBytes: 256, MaxInFlight: 2,
		},
		SinkLimits: GrantLimits{
			MaxRequests: 4, MaxBytesPerRequest: 128, MaxCumulativeBytes: 512, MaxInFlight: 1,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	inputSession := &workerInputReadSession{payload: []byte("control")}
	input := &workerInputBrokerCapture{
		info:    content.AttemptSourceInfo{Size: 7, Sequential: true, Range: true},
		session: inputSession,
	}
	service, err := NewWorkerProtocolService(protocol, harness.coordinator, grants, input, workerArtifactSinkStub{})
	if err != nil {
		t.Fatal(err)
	}
	identity := WorkerTransportIdentity{
		Kind: WorkerTransportLocal, Fingerprint: strings.Repeat("b", 64), PeerUID: 1000,
	}
	envelope, err := service.Pull(context.Background(), identity, WorkerPullRequest{
		SchemaVersion: 1, WorkerID: workerID, InstanceID: strings.Repeat("b", 32),
	})
	if err != nil {
		t.Fatal(err)
	}
	activation, err := service.ActivateInput(context.Background(), identity, WorkerInputActivateRequest{
		SchemaVersion: 1, WorkerID: workerID, InstanceID: strings.Repeat("b", 32),
		JobID: work.JobID, AttemptID: envelope.AttemptID, ExpectedRevision: envelope.TransitionRevision,
		GrantID: envelope.InputActivation.GrantID, Secret: envelope.InputActivation.Secret,
	})
	if err != nil || activation.TransitionRevision != 3 {
		t.Fatalf("ActivateInput: response=%+v err=%v", activation, err)
	}
	heartbeat, err := service.Heartbeat(context.Background(), identity, WorkerHeartbeatRequest{
		SchemaVersion: 1, WorkerID: workerID, InstanceID: strings.Repeat("b", 32), AttemptID: envelope.AttemptID,
	})
	if err != nil || heartbeat.CancelRequested || heartbeat.WorkerDraining || heartbeat.EffectiveLeaseExpiresAt.IsZero() || heartbeat.TransitionRevision != 3 {
		t.Fatalf("Heartbeat: response=%+v err=%v", heartbeat, err)
	}
	wrongIdentity := identity
	wrongIdentity.Fingerprint = strings.Repeat("c", 64)
	if _, err := service.Transition(context.Background(), wrongIdentity, WorkerTransitionRequest{
		SchemaVersion: 1, WorkerID: workerID, InstanceID: strings.Repeat("b", 32),
		JobID: work.JobID, AttemptID: envelope.AttemptID, ExpectedRevision: 3, To: ProcessingProcessing,
	}); !errors.Is(err, ErrWorkerUnauthenticated) {
		t.Fatalf("transition trusted changed transport: %v", err)
	}
	processing, err := service.Transition(context.Background(), identity, WorkerTransitionRequest{
		SchemaVersion: 1, WorkerID: workerID, InstanceID: strings.Repeat("b", 32),
		JobID: work.JobID, AttemptID: envelope.AttemptID, ExpectedRevision: 3, To: ProcessingProcessing,
	})
	if err != nil || processing.State != ProcessingProcessing || processing.Revision != 4 {
		t.Fatalf("processing transition: response=%+v err=%v", processing, err)
	}
	uploading, err := service.Transition(context.Background(), identity, WorkerTransitionRequest{
		SchemaVersion: 1, WorkerID: workerID, InstanceID: strings.Repeat("b", 32),
		JobID: work.JobID, AttemptID: envelope.AttemptID, ExpectedRevision: 4, To: ProcessingUploading,
	})
	if err != nil || uploading.State != ProcessingUploading || uploading.Revision != 5 {
		t.Fatalf("uploading transition: response=%+v err=%v", uploading, err)
	}
	if err := service.Drain(context.Background(), identity, WorkerDrainRequest{
		SchemaVersion: 1, WorkerID: workerID, InstanceID: strings.Repeat("b", 32),
	}); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if err := harness.coordinator.RemoveInterest(context.Background(), work.JobID, InterestSystem, "protocol-control", InterestRemovedCanceled); err != nil {
		t.Fatal(err)
	}
	heartbeat, err = service.Heartbeat(context.Background(), identity, WorkerHeartbeatRequest{
		SchemaVersion: 1, WorkerID: workerID, InstanceID: strings.Repeat("b", 32), AttemptID: envelope.AttemptID,
	})
	if err != nil || !heartbeat.CancelRequested || heartbeat.CancelReason != CancelReasonInterestWithdrawn || !heartbeat.WorkerDraining || heartbeat.TransitionRevision != 6 {
		t.Fatalf("cancel/drain heartbeat: response=%+v err=%v", heartbeat, err)
	}
	canceled, err := service.Transition(context.Background(), identity, WorkerTransitionRequest{
		SchemaVersion: 1, WorkerID: workerID, InstanceID: strings.Repeat("b", 32),
		JobID: work.JobID, AttemptID: envelope.AttemptID, ExpectedRevision: 6,
		To: ProcessingCanceled, CancelReason: CancelReasonInterestWithdrawn,
	})
	if err != nil || canceled.State != ProcessingCanceled || canceled.Revision != 7 || !inputSession.closed {
		t.Fatalf("cancel acknowledgment: response=%+v session_closed=%v err=%v", canceled, inputSession.closed, err)
	}
}

func TestWorkerProtocolHeartbeatRenewsIssuedAndActiveGrantAuthority(t *testing.T) {
	harness := newCoordinatorHarness(t)
	workerID := harness.registerNoopWorker(t, "c")
	work, err := harness.coordinator.RequestWork(context.Background(), WorkRequest{
		Descriptor: validWorkDescriptor(),
		Interest: InterestRequest{
			OwnerKind: InterestSystem, OwnerKey: "protocol-grant-renewal",
			PriorityClass: PriorityInteractive, Priority: 100,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	protocol, err := NewProtocolService(harness.db, NewProductionCapabilityRegistry(), harness.clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	grants, err := NewGrantService(harness.db, harness.coordinator.leaseService, harness.clock.Now, GrantConfig{
		TTL: 30 * time.Second,
		InputLimits: GrantLimits{
			MaxRequests: 8, MaxBytesPerRequest: 64, MaxCumulativeBytes: 256, MaxInFlight: 2,
		},
		SinkLimits: GrantLimits{
			MaxRequests: 4, MaxBytesPerRequest: 128, MaxCumulativeBytes: 512, MaxInFlight: 1,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	inputSession := &workerInputReadSession{payload: []byte("renewed")}
	input := &workerInputBrokerCapture{
		info:    content.AttemptSourceInfo{Size: 7, Sequential: true, Range: true},
		session: inputSession,
	}
	service, err := NewWorkerProtocolService(protocol, harness.coordinator, grants, input, workerArtifactSinkStub{})
	if err != nil {
		t.Fatal(err)
	}
	identity := WorkerTransportIdentity{
		Kind: WorkerTransportLocal, Fingerprint: strings.Repeat("c", 64), PeerUID: 1000,
	}
	envelope, err := service.Pull(context.Background(), identity, WorkerPullRequest{
		SchemaVersion: 1, WorkerID: workerID, InstanceID: strings.Repeat("c", 32),
	})
	if err != nil {
		t.Fatal(err)
	}
	var inputGrant model.BackupAssetProcessingGrant
	if err := harness.db.First(&inputGrant, "id = ?", envelope.InputActivation.GrantID).Error; err != nil {
		t.Fatal(err)
	}
	initialExpiry := inputGrant.ExpiresAt.UTC()
	activation, err := service.ActivateInput(context.Background(), identity, WorkerInputActivateRequest{
		SchemaVersion: 1, WorkerID: workerID, InstanceID: strings.Repeat("c", 32),
		JobID: work.JobID, AttemptID: envelope.AttemptID, ExpectedRevision: envelope.TransitionRevision,
		GrantID: envelope.InputActivation.GrantID, Secret: envelope.InputActivation.Secret,
	})
	if err != nil {
		t.Fatalf("ActivateInput: %v", err)
	}
	if !input.binding.AbsoluteExpiresAt.After(initialExpiry) {
		t.Fatalf("attempt input session expiry=%s, want after rolling grant expiry %s", input.binding.AbsoluteExpiresAt, initialExpiry)
	}

	harness.clock.Advance(20 * time.Second)
	heartbeat, err := service.Heartbeat(context.Background(), identity, WorkerHeartbeatRequest{
		SchemaVersion: 1, WorkerID: workerID, InstanceID: strings.Repeat("c", 32), AttemptID: envelope.AttemptID,
	})
	if err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	var sinkGrant model.BackupAssetProcessingGrant
	if err := harness.db.First(&inputGrant, "id = ?", envelope.InputActivation.GrantID).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.First(&sinkGrant, "id = ?", envelope.SinkActivation.GrantID).Error; err != nil {
		t.Fatal(err)
	}
	for kind, renewed := range map[GrantKind]model.BackupAssetProcessingGrant{
		GrantInput: inputGrant,
		GrantSink:  sinkGrant,
	} {
		if !renewed.ExpiresAt.UTC().After(initialExpiry) || !renewed.ExpiresAt.UTC().Equal(heartbeat.EffectiveLeaseExpiresAt.UTC()) {
			t.Fatalf("%s grant expiry=%s, initial=%s effective=%s", kind, renewed.ExpiresAt, initialExpiry, heartbeat.EffectiveLeaseExpiresAt)
		}
	}

	harness.clock.Advance(15 * time.Second)
	handle, err := service.OpenInput(context.Background(), identity, WorkerInputReadRequest{
		SchemaVersion: 1, WorkerID: workerID, InstanceID: strings.Repeat("c", 32),
		SessionID: activation.SessionID, Mode: content.SourceModeRange, Offset: 0, Length: 7,
	})
	if err != nil {
		t.Fatalf("OpenInput after original grant expiry: %v", err)
	}
	if err := handle.Close(); err != nil {
		t.Fatalf("close renewed input handle: %v", err)
	}
	processing, err := service.Transition(context.Background(), identity, WorkerTransitionRequest{
		SchemaVersion: 1, WorkerID: workerID, InstanceID: strings.Repeat("c", 32),
		JobID: work.JobID, AttemptID: envelope.AttemptID, ExpectedRevision: activation.TransitionRevision, To: ProcessingProcessing,
	})
	if err != nil {
		t.Fatalf("transition to processing: %v", err)
	}
	uploading, err := service.Transition(context.Background(), identity, WorkerTransitionRequest{
		SchemaVersion: 1, WorkerID: workerID, InstanceID: strings.Repeat("c", 32),
		JobID: work.JobID, AttemptID: envelope.AttemptID, ExpectedRevision: processing.Revision, To: ProcessingUploading,
	})
	if err != nil {
		t.Fatalf("transition to uploading: %v", err)
	}
	if _, err := service.ActivateSink(context.Background(), identity, WorkerSinkActivateRequest{
		SchemaVersion: 1, WorkerID: workerID, InstanceID: strings.Repeat("c", 32),
		JobID: work.JobID, AttemptID: envelope.AttemptID, ExpectedRevision: uploading.Revision,
		GrantID: envelope.SinkActivation.GrantID, Secret: envelope.SinkActivation.Secret,
	}); err != nil {
		t.Fatalf("ActivateSink after original grant expiry: %v", err)
	}
}

func TestWorkerProtocolServiceActivatesSinkUploadsAndCommitsOneManifest(t *testing.T) {
	harness := newCoordinatorHarness(t)
	workerID := harness.registerNoopWorker(t, "c")
	work, err := harness.coordinator.RequestWork(context.Background(), WorkRequest{
		Descriptor: validWorkDescriptor(),
		Interest: InterestRequest{
			OwnerKind: InterestSystem, OwnerKey: "protocol-sink",
			PriorityClass: PriorityInteractive, Priority: 100,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	protocol, err := NewProtocolService(harness.db, NewProductionCapabilityRegistry(), harness.clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	grants, err := NewGrantService(harness.db, harness.coordinator.leaseService, harness.clock.Now, GrantConfig{
		TTL: 30 * time.Second,
		InputLimits: GrantLimits{
			MaxRequests: 8, MaxBytesPerRequest: 64, MaxCumulativeBytes: 256, MaxInFlight: 2,
		},
		SinkLimits: GrantLimits{
			MaxRequests: 4, MaxBytesPerRequest: 128, MaxCumulativeBytes: 512, MaxInFlight: 1,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	inputSession := &workerInputReadSession{payload: []byte("sink-input")}
	input := &workerInputBrokerCapture{
		info:    content.AttemptSourceInfo{Size: 10, Sequential: true, Range: true},
		session: inputSession,
	}
	sink := &workerArtifactSinkCapture{
		uploaded: UploadedArtifact{UploadID: strings.Repeat("1", 32), BlobID: strings.Repeat("2", 32), Ordinal: 0},
		committed: CommitManifestResult{
			ArtifactSetID: strings.Repeat("3", 32), ManifestDigest: strings.Repeat("4", 64), ProjectionRequired: false,
		},
	}
	service, err := NewWorkerProtocolService(protocol, harness.coordinator, grants, input, sink)
	if err != nil {
		t.Fatal(err)
	}
	identity := WorkerTransportIdentity{
		Kind: WorkerTransportLocal, Fingerprint: strings.Repeat("c", 64), PeerUID: 1000,
	}
	envelope, err := service.Pull(context.Background(), identity, WorkerPullRequest{
		SchemaVersion: 1, WorkerID: workerID, InstanceID: strings.Repeat("c", 32),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ActivateInput(context.Background(), identity, WorkerInputActivateRequest{
		SchemaVersion: 1, WorkerID: workerID, InstanceID: strings.Repeat("c", 32),
		JobID: work.JobID, AttemptID: envelope.AttemptID, ExpectedRevision: 2,
		GrantID: envelope.InputActivation.GrantID, Secret: envelope.InputActivation.Secret,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Transition(context.Background(), identity, WorkerTransitionRequest{
		SchemaVersion: 1, WorkerID: workerID, InstanceID: strings.Repeat("c", 32),
		JobID: work.JobID, AttemptID: envelope.AttemptID, ExpectedRevision: 3, To: ProcessingProcessing,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Transition(context.Background(), identity, WorkerTransitionRequest{
		SchemaVersion: 1, WorkerID: workerID, InstanceID: strings.Repeat("c", 32),
		JobID: work.JobID, AttemptID: envelope.AttemptID, ExpectedRevision: 4, To: ProcessingUploading,
	}); err != nil {
		t.Fatal(err)
	}
	activateRequest := WorkerSinkActivateRequest{
		SchemaVersion: 1, WorkerID: workerID, InstanceID: strings.Repeat("c", 32),
		JobID: work.JobID, AttemptID: envelope.AttemptID, ExpectedRevision: 5,
		GrantID: envelope.SinkActivation.GrantID, Secret: envelope.SinkActivation.Secret,
	}
	wrongIdentity := identity
	wrongIdentity.Fingerprint = strings.Repeat("d", 64)
	if _, err := service.ActivateSink(context.Background(), wrongIdentity, activateRequest); !errors.Is(err, ErrWorkerUnauthenticated) {
		t.Fatalf("sink activation trusted changed transport: %v", err)
	}
	activated, err := service.ActivateSink(context.Background(), identity, activateRequest)
	if err != nil {
		t.Fatalf("ActivateSink: %v", err)
	}
	if activated.SchemaVersion != 1 || activated.SessionID != envelope.SinkActivation.GrantID ||
		activated.MaxArtifacts != 4 || activated.MaxArtifactBytes != 128 || activated.MaxTotalBytes != 512 ||
		activated.MaxInFlight != 1 {
		t.Fatalf("sink activation response invalid: %+v", activated)
	}
	payload := []byte("passive-noop")
	digest := sha256.Sum256(payload)
	declaration := ArtifactDeclaration{
		Ordinal: 0, Role: ArtifactRoleNoop, MediaType: "application/octet-stream",
		PlaintextSize: int64(len(payload)), PlaintextDigest: fmt.Sprintf("%x", digest[:]),
		Completeness: ArtifactComplete, CoverageCanonical: []byte(`{"schema_version":1,"kind":"all"}`),
	}
	uploadRequest := WorkerUploadArtifactRequest{
		SchemaVersion: 1, WorkerID: workerID, InstanceID: strings.Repeat("c", 32),
		SessionID: activated.SessionID, JobID: work.JobID, AttemptID: envelope.AttemptID,
		Artifact: declaration,
	}
	wrongSession := uploadRequest
	wrongSession.SessionID = strings.Repeat("f", 32)
	if _, err := service.UploadArtifact(context.Background(), identity, wrongSession, bytes.NewReader(payload)); !errors.Is(err, ErrGrantDenied) {
		t.Fatalf("unowned Sink session got %v", err)
	}
	uploaded, err := service.UploadArtifact(context.Background(), identity, uploadRequest, bytes.NewReader(payload))
	if err != nil || uploaded != sink.uploaded || string(sink.uploadPayload) != string(payload) ||
		sink.uploadRequest.GrantID != activated.SessionID || sink.uploadRequest.WorkerID != workerID {
		t.Fatalf("UploadArtifact: response=%+v captured=%+v payload=%q err=%v", uploaded, sink.uploadRequest, sink.uploadPayload, err)
	}
	manifestRequest := WorkerCommitManifestRequest{
		SchemaVersion: 1, WorkerID: workerID, InstanceID: strings.Repeat("c", 32),
		SessionID: activated.SessionID, JobID: work.JobID, AttemptID: envelope.AttemptID,
		SecurityPolicyRevision: validWorkDescriptor().SecurityPolicyRevision,
		Artifacts:              []ArtifactDeclaration{declaration},
	}
	committed, err := service.CommitManifest(context.Background(), identity, manifestRequest)
	if err != nil {
		t.Fatalf("CommitManifest: %v", err)
	}
	if committed.SchemaVersion != 1 || committed.ArtifactSetID != sink.committed.ArtifactSetID ||
		committed.ManifestDigest != sink.committed.ManifestDigest || committed.ProjectionRequired ||
		sink.commitRequest.GrantID != activated.SessionID ||
		sink.commitRequest.RecoveryPointFence.LeaseID != envelope.RecoveryPointFence.LeaseID ||
		sink.commitRequest.RecoveryPointFence.FenceToken != envelope.RecoveryPointFence.FenceToken ||
		sink.commitRequest.SecurityPolicyRevision != validWorkDescriptor().SecurityPolicyRevision || !inputSession.closed {
		t.Fatalf("manifest orchestration invalid: response=%+v request=%+v input_closed=%v", committed, sink.commitRequest, inputSession.closed)
	}
	if _, err := service.CommitManifest(context.Background(), identity, manifestRequest); !errors.Is(err, ErrGrantDenied) {
		t.Fatalf("manifest replay got %v", err)
	}
}

func TestProtocolSecurityFailureQuarantinesWorkerAndRevokesGrants(t *testing.T) {
	db, service := newProtocolServiceHarness(t)
	registered, err := service.Handshake(context.Background(), WorkerTransportIdentity{
		Kind: WorkerTransportLocal, Fingerprint: strings.Repeat("f", 64),
	}, validHandshakeRequest())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 19, 8, 9, 10, 0, time.UTC)
	grant := model.BackupAssetProcessingGrant{
		ID: strings.Repeat("1", 32), JobID: strings.Repeat("2", 32), AttemptID: strings.Repeat("3", 32), WorkerID: registered.WorkerID,
		Kind: string(GrantInput), ActivationSecretHash: strings.Repeat("4", 64), FenceHash: strings.Repeat("5", 64),
		State: string(GrantIssued), MaxRequests: 1, MaxBytesPerRequest: 1, MaxCumulativeBytes: 1, MaxInFlight: 1,
		ExpiresAt: now.Add(time.Minute), CreatedAt: now, UpdatedAt: now, Version: 1,
	}
	if err := db.Create(&grant).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.Quarantine(context.Background(), registered.WorkerID, ProcessingErrorInvalidOutput); err != nil {
		t.Fatalf("Quarantine: %v", err)
	}
	var worker model.BackupAssetWorkerIdentity
	if err := db.First(&worker, "id = ?", registered.WorkerID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&grant, "id = ?", grant.ID).Error; err != nil {
		t.Fatal(err)
	}
	if worker.TrustState != "quarantined" || worker.QuarantineCode != string(ProcessingErrorInvalidOutput) ||
		grant.State != string(GrantRevoked) || grant.RevocationReason != "quarantine" || grant.ActivationSecretHash != "" {
		t.Fatalf("quarantine product invalid: worker=%+v grant=%+v", worker, grant)
	}
	if _, err := service.Handshake(context.Background(), WorkerTransportIdentity{
		Kind: WorkerTransportLocal, Fingerprint: strings.Repeat("f", 64),
	}, validHandshakeRequest()); !errors.Is(err, ErrWorkerQuarantined) {
		t.Fatalf("quarantined Worker reconnected as active: %v", err)
	}
}

func newProtocolServiceHarness(t *testing.T) (*gorm.DB, *ProtocolService) {
	t.Helper()
	dsn := processingTestSQLiteDSN(t)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.BackupAssetWorkerIdentity{}, &model.BackupAssetWorkerCapability{},
		&model.BackupAssetProcessingAttempt{}, &model.BackupAssetProcessingGrant{}); err != nil {
		t.Fatal(err)
	}
	registry, err := NewCapabilityRegistry([]CapabilityDefinition{{
		Capability: "noop", CapabilitySchema: "noop.v1", OutputProfile: "noop.v1", PipelineFingerprint: "noop-pipeline-v1",
	}})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewProtocolService(db, registry, func() time.Time { return time.Date(2026, 7, 19, 8, 9, 10, 0, time.UTC) })
	if err != nil {
		t.Fatal(err)
	}
	return db, service
}

func validHandshakeRequest() HandshakeRequest {
	return HandshakeRequest{
		SchemaVersion: 1, ProtocolVersion: 1, InstanceID: strings.Repeat("1", 32), IdentityRevision: 1,
		InteractiveSlots: 2, BackgroundSlots: 2,
		Capabilities: []CapabilityAdvertisement{{
			SchemaVersion: 1, Capability: "noop", CapabilitySchema: "noop.v1",
			PipelineFingerprint: "noop-pipeline-v1", OutputProfile: "noop.v1",
			InputModes: []ProtocolInputMode{ProtocolInputStat, ProtocolInputSequential, ProtocolInputRange},
			Limits: ProtocolCapabilityLimits{
				MaxInputBytes: 1 << 20, MaxOutputBytes: 1 << 20, MaxOutputCount: 4,
				MaxPages: 1, MaxPixels: 1, MaxDurationMillis: 1000, MaxExpandedBytes: 1 << 20,
			},
		}},
	}
}

type workerInputBrokerStub struct{}

func (workerInputBrokerStub) OpenSession(context.Context, content.AttemptSourceBinding) (content.AttemptInputSession, content.AttemptSourceInfo, error) {
	return nil, content.AttemptSourceInfo{}, errors.New("unused input broker stub")
}

type workerArtifactSinkStub struct{}

func (workerArtifactSinkStub) UploadArtifact(context.Context, UploadArtifactRequest, io.Reader) (UploadedArtifact, error) {
	return UploadedArtifact{}, errors.New("unused artifact sink stub")
}

func (workerArtifactSinkStub) CommitManifest(context.Context, CommitManifestRequest) (CommitManifestResult, error) {
	return CommitManifestResult{}, errors.New("unused artifact sink stub")
}

type workerArtifactSinkCapture struct {
	uploadRequest UploadArtifactRequest
	uploadPayload []byte
	uploaded      UploadedArtifact
	uploadErr     error
	commitRequest CommitManifestRequest
	committed     CommitManifestResult
	commitErr     error
}

func (capture *workerArtifactSinkCapture) UploadArtifact(_ context.Context, request UploadArtifactRequest, body io.Reader) (UploadedArtifact, error) {
	capture.uploadRequest = request
	payload, err := io.ReadAll(body)
	if err != nil {
		return UploadedArtifact{}, err
	}
	capture.uploadPayload = payload
	return capture.uploaded, capture.uploadErr
}

func (capture *workerArtifactSinkCapture) CommitManifest(_ context.Context, request CommitManifestRequest) (CommitManifestResult, error) {
	capture.commitRequest = request
	return capture.committed, capture.commitErr
}

type workerInputBrokerCapture struct {
	binding content.AttemptSourceBinding
	info    content.AttemptSourceInfo
	session content.AttemptInputSession
	calls   int
}

func (capture *workerInputBrokerCapture) OpenSession(_ context.Context, binding content.AttemptSourceBinding) (content.AttemptInputSession, content.AttemptSourceInfo, error) {
	capture.calls++
	capture.binding = binding
	return capture.session, capture.info, nil
}

type workerInputSessionStub struct{}

func (workerInputSessionStub) Info() content.AttemptSourceInfo { return content.AttemptSourceInfo{} }

func (workerInputSessionStub) OpenSequential(context.Context, int64) (content.AttemptReadHandle, error) {
	return nil, errors.New("unused input session stub")
}

func (workerInputSessionStub) OpenRange(context.Context, int64, int64) (content.AttemptReadHandle, error) {
	return nil, errors.New("unused input session stub")
}

func (workerInputSessionStub) Revalidate(context.Context) error { return nil }

func (workerInputSessionStub) Close() error { return nil }

type workerInputReadSession struct {
	payload         []byte
	sequentialCalls int
	rangeCalls      int
	closed          bool
}

type blockingWorkerInputSession struct {
	started chan struct{}
	release chan struct{}
	closed  bool
}

func (*blockingWorkerInputSession) Info() content.AttemptSourceInfo {
	return content.AttemptSourceInfo{Size: 1, Sequential: true}
}

func (session *blockingWorkerInputSession) OpenSequential(context.Context, int64) (content.AttemptReadHandle, error) {
	close(session.started)
	<-session.release
	return &workerInputReadHandle{Reader: bytes.NewReader([]byte("x"))}, nil
}

func (*blockingWorkerInputSession) OpenRange(context.Context, int64, int64) (content.AttemptReadHandle, error) {
	return nil, errors.New("unused blocking Range")
}

func (*blockingWorkerInputSession) Revalidate(context.Context) error { return nil }

func (session *blockingWorkerInputSession) Close() error {
	session.closed = true
	return nil
}

func (session *workerInputReadSession) Info() content.AttemptSourceInfo {
	return content.AttemptSourceInfo{Size: int64(len(session.payload)), Sequential: true, Range: true}
}

func (session *workerInputReadSession) OpenSequential(_ context.Context, maximum int64) (content.AttemptReadHandle, error) {
	session.sequentialCalls++
	end := maximum
	if end > int64(len(session.payload)) {
		end = int64(len(session.payload))
	}
	return &workerInputReadHandle{Reader: bytes.NewReader(session.payload[:end])}, nil
}

func (session *workerInputReadSession) OpenRange(_ context.Context, offset, length int64) (content.AttemptReadHandle, error) {
	session.rangeCalls++
	end := offset + length
	if offset < 0 || length <= 0 || offset > int64(len(session.payload)) || end > int64(len(session.payload)) {
		return nil, content.ErrAttemptSessionDenied
	}
	return &workerInputReadHandle{Reader: bytes.NewReader(session.payload[offset:end])}, nil
}

func (*workerInputReadSession) Revalidate(context.Context) error { return nil }

func (session *workerInputReadSession) Close() error {
	session.closed = true
	return nil
}

type workerInputReadHandle struct {
	*bytes.Reader
	closed bool
}

func (handle *workerInputReadHandle) Close() error {
	handle.closed = true
	return nil
}
