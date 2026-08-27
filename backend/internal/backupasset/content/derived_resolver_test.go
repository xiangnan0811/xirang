package content

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestDerivedResolverStreamsExactActiveRepresentationThroughContentSource(t *testing.T) {
	db, binding := derivedResolverFixture(t)
	payload := []byte("safe derived text")
	reader := &derivedArtifactReaderFake{artifactID: binding.artifactID, payload: payload}
	resolver, err := NewDerivedRepresentationResolver(
		db, reader.Read, derivedResolverTestActivePipeline, derivedResolverTestMalwareSafe,
	)
	if err != nil {
		t.Fatal(err)
	}
	request := derivedResolverTestRequest(binding)
	resolved, err := resolver.Resolve(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Size != int64(len(payload)) || resolved.MediaType != "text/plain" ||
		resolved.EntryFingerprint != binding.digest || resolved.Provider != backupasset.ProviderRestic {
		t.Fatalf("resolved representation=%+v", resolved)
	}
	encoded, err := json.Marshal(resolved)
	if err != nil || string(encoded) != "{}" {
		t.Fatalf("private binding serialized: %s err=%v", encoded, err)
	}
	session, err := resolver.Open(context.Background(), resolved, SourceRequest{
		Ref: binding.ref, CatalogGenerationID: binding.catalogGenerationID,
		ExpectedSource: binding.sourceFingerprint, ExpectedEntry: binding.digest,
		Mode: SourceModeSequential, MaxBytes: int64(len(payload)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if session.Capabilities().Provider != backupasset.ProviderRestic || !session.Capabilities().Sequential || session.Capabilities().Range {
		t.Fatalf("derived source capabilities=%+v", session.Capabilities())
	}
	readPayload, err := io.ReadAll(session.Reader())
	if err != nil || !bytes.Equal(readPayload, payload) || reader.calls != 1 {
		t.Fatalf("derived read=%q calls=%d err=%v", readPayload, reader.calls, err)
	}
	if providerBytes := session.Reader().ProviderBytes(); providerBytes != 0 {
		t.Fatalf("Derived source charged Provider bytes=%d", providerBytes)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestDerivedResolverFailsClosedAfterLifecycleOrBindingChange(t *testing.T) {
	db, fixture := derivedResolverFixture(t)
	resolver, err := NewDerivedRepresentationResolver(
		db,
		func(context.Context, DerivedArtifactRead, io.Writer) error { return nil },
		derivedResolverTestActivePipeline,
		derivedResolverTestMalwareSafe,
	)
	if err != nil {
		t.Fatal(err)
	}
	base := derivedResolverTestRequest(fixture)
	for name, mutate := range map[string]func(*DerivedRepresentationRequest){
		"wrong source": func(value *DerivedRepresentationRequest) { value.SourceFingerprint = "changed" },
		"wrong policy": func(value *DerivedRepresentationRequest) { value.SecurityPolicyRevision = "policy-v2" },
		"wrong renderer": func(value *DerivedRepresentationRequest) {
			value.Renderer = RendererSafeRaster
			value.Profile = ProfileRasterV1
		},
		"range": func(value *DerivedRepresentationRequest) {},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := base
			mutate(&candidate)
			resolved, resolveErr := resolver.Resolve(context.Background(), candidate)
			if name == "range" {
				if resolveErr != nil {
					t.Fatal(resolveErr)
				}
				_, resolveErr = resolver.Open(context.Background(), resolved, SourceRequest{
					Ref: fixture.ref, CatalogGenerationID: fixture.catalogGenerationID,
					ExpectedSource: fixture.sourceFingerprint, ExpectedEntry: fixture.digest,
					Mode: SourceModeRange, MaxBytes: 1, Range: &ResolvedRange{Length: 1},
				})
			}
			if !errors.Is(resolveErr, ErrDerivedRepresentationUnavailable) {
				t.Fatalf("error=%v", resolveErr)
			}
		})
	}
	resolved, err := resolver.Resolve(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.BackupAssetDerivedArtifactSet{}).Where("id = ?", fixture.setID).Update("state", "stale").Error; err != nil {
		t.Fatal(err)
	}
	if err := resolver.Revalidate(context.Background(), resolved); !errors.Is(err, ErrDerivedRepresentationUnavailable) {
		t.Fatalf("stale Derived revalidation error=%v", err)
	}
}

func TestDerivedResolverRevalidatesLifecycleImmediatelyBeforeReader(t *testing.T) {
	db, fixture := derivedResolverFixture(t)
	reader := &derivedArtifactReaderFake{artifactID: fixture.artifactID, payload: []byte("must not escape")}
	resolver, err := NewDerivedRepresentationResolver(
		db, reader.Read, derivedResolverTestActivePipeline, derivedResolverTestMalwareSafe,
	)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := resolver.Resolve(context.Background(), derivedResolverTestRequest(fixture))
	if err != nil {
		t.Fatal(err)
	}
	session, err := resolver.Open(context.Background(), resolved, SourceRequest{
		Ref: fixture.ref, CatalogGenerationID: fixture.catalogGenerationID,
		ExpectedSource: fixture.sourceFingerprint, ExpectedEntry: fixture.digest,
		Mode: SourceModeSequential, MaxBytes: resolved.Size,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.BackupAssetDerivedArtifactSet{}).Where("id = ?", fixture.setID).Update("state", "stale").Error; err != nil {
		t.Fatal(err)
	}
	payload, readErr := io.ReadAll(session.Reader())
	if !errors.Is(readErr, ErrDerivedRepresentationUnavailable) || len(payload) != 0 || reader.calls != 0 {
		t.Fatalf("stale reader payload=%q calls=%d err=%v", payload, reader.calls, readErr)
	}
}

func TestDerivedResolverRejectsInactivePipelineAtResolveAndRead(t *testing.T) {
	db, fixture := derivedResolverFixture(t)
	reader := &derivedArtifactReaderFake{artifactID: fixture.artifactID, payload: []byte("must not be read")}
	resolver, err := NewDerivedRepresentationResolver(
		db, reader.Read, derivedResolverTestActivePipeline, derivedResolverTestMalwareSafe,
	)
	if err != nil {
		t.Fatal(err)
	}
	request := derivedResolverTestRequest(fixture)
	resolved, err := resolver.Resolve(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.BackupAssetProcessingJob{}).Where("id = ?", fixture.jobID).
		Update("pipeline_fingerprint", "inactive-pipeline-v0").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.Resolve(context.Background(), request); !errors.Is(err, ErrDerivedRepresentationUnavailable) {
		t.Fatalf("inactive pipeline resolve error=%v", err)
	}
	if _, err := resolver.Open(context.Background(), resolved, SourceRequest{
		Ref: fixture.ref, CatalogGenerationID: fixture.catalogGenerationID,
		ExpectedSource: fixture.sourceFingerprint, ExpectedEntry: fixture.digest,
		Mode: SourceModeSequential, MaxBytes: resolved.Size,
	}); !errors.Is(err, ErrDerivedRepresentationUnavailable) {
		t.Fatalf("inactive pipeline open error=%v", err)
	}
	if reader.calls != 0 {
		t.Fatalf("inactive pipeline reached Derived bytes reads=%d", reader.calls)
	}
}

func TestDerivedResolverRechecksMalwareSafetyAtResolveAndRead(t *testing.T) {
	db, fixture := derivedResolverFixture(t)
	reader := &derivedArtifactReaderFake{artifactID: fixture.artifactID, payload: []byte("must not be read")}
	resolver, err := NewDerivedRepresentationResolver(
		db, reader.Read, derivedResolverTestActivePipeline, derivedResolverTestMalwareSafe,
	)
	if err != nil {
		t.Fatal(err)
	}
	safety := &derivedMalwareSafetyFake{safe: true}
	resolver.malwareSafety = safety.Check
	request := DerivedRepresentationRequest{
		Ref: fixture.ref, CatalogGenerationID: fixture.catalogGenerationID,
		SourceFingerprint: fixture.sourceFingerprint, SourceEntryFingerprint: fixture.sourceEntryFingerprint,
		FingerprintStrength: "strong", ProviderCapabilityRevision: 1, SourceSize: 4096,
		SourceMediaType: "text/plain", SecurityPolicyRevision: fixture.policyRevision,
		Provider: backupasset.ProviderRestic, Renderer: RendererEscapedText, Profile: ProfileTextV1,
	}
	resolved, err := resolver.Resolve(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(safety.assets) != 1 || safety.assets[0].Ref != fixture.ref ||
		safety.assets[0].CatalogGenerationID != fixture.catalogGenerationID ||
		safety.assets[0].SourceFingerprint != fixture.sourceFingerprint ||
		safety.assets[0].EntryFingerprint != fixture.sourceEntryFingerprint ||
		safety.assets[0].ProviderCapabilityRevision != 1 || safety.assets[0].Size != 4096 {
		t.Fatalf("malware safety asset=%+v", safety.assets)
	}
	safety.safe = false
	if _, err := resolver.Resolve(context.Background(), request); !errors.Is(err, ErrDerivedRepresentationUnavailable) {
		t.Fatalf("unsafe resolve error=%v", err)
	}
	if _, err := resolver.Open(context.Background(), resolved, SourceRequest{
		Ref: fixture.ref, CatalogGenerationID: fixture.catalogGenerationID,
		ExpectedSource: fixture.sourceFingerprint, ExpectedEntry: fixture.digest,
		Mode: SourceModeSequential, MaxBytes: resolved.Size,
	}); !errors.Is(err, ErrDerivedRepresentationUnavailable) {
		t.Fatalf("unsafe open error=%v", err)
	}
	if reader.calls != 0 {
		t.Fatalf("unsafe Derived bytes reads=%d", reader.calls)
	}
}

func TestDerivedResolverRejectsMultipleActiveTerminalPublications(t *testing.T) {
	db, fixture := derivedResolverFixture(t)
	duplicateDerivedResolverPublication(t, db, fixture)
	resolver, err := NewDerivedRepresentationResolver(
		db,
		func(context.Context, DerivedArtifactRead, io.Writer) error { return nil },
		derivedResolverTestActivePipeline,
		derivedResolverTestMalwareSafe,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.Resolve(context.Background(), derivedResolverTestRequest(fixture)); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("multiple active terminal publications error=%v, want conflict", err)
	}
}

func TestDerivedResolverDocumentPagesSelectsOnlyOrdinalZero(t *testing.T) {
	for _, mediaType := range []string{"image/png", "image/jpeg", "image/webp"} {
		t.Run(mediaType, func(t *testing.T) {
			db, fixture := derivedResolverFixture(t)
			configureDerivedResolverProduct(t, db, fixture, "document.convert", "document.convert.v1", "static_pages_v1", "thumbnail", mediaType)
			addDerivedResolverArtifact(t, db, fixture, 1, "thumbnail", "image/png", "0", "a", "b")
			addDerivedResolverArtifact(t, db, fixture, 2, "thumbnail", "image/png", "c", "d", "e")

			resolver, err := NewDerivedRepresentationResolver(
				db,
				func(context.Context, DerivedArtifactRead, io.Writer) error { return nil },
				derivedResolverTestActivePipeline,
				derivedResolverTestMalwareSafe,
			)
			if err != nil {
				t.Fatal(err)
			}
			request := derivedResolverTestRequest(fixture)
			request.SourceMediaType = "application/pdf"
			request.Renderer = RendererSafeRaster
			request.Profile = ProfileRasterV1

			resolved, err := resolver.Resolve(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			if resolved.artifactID != fixture.artifactID || resolved.Role != "thumbnail" || resolved.MediaType != mediaType {
				t.Fatalf("document page binding=%+v", resolved)
			}
		})
	}
}

func TestDerivedResolverDocumentPagesRejectsDuplicateCurrentOrdinalZero(t *testing.T) {
	db, fixture := derivedResolverFixture(t)
	configureDerivedResolverProduct(t, db, fixture, "document.convert", "document.convert.v1", "static_pages_v1", "thumbnail", "image/png")
	duplicateDerivedResolverPublication(t, db, fixture)
	if err := db.Model(&model.BackupAssetProcessingJob{}).
		Where("id <> ?", fixture.jobID).
		Updates(map[string]any{
			"capability": "document.convert", "capability_schema": "document.convert.v1", "output_profile": "static_pages_v1",
		}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.BackupAssetDerivedArtifact{}).
		Where("id <> ?", fixture.artifactID).
		Updates(map[string]any{"role": "thumbnail", "media_type": "image/png", "ordinal": 0}).Error; err != nil {
		t.Fatal(err)
	}
	resolver, err := NewDerivedRepresentationResolver(
		db,
		func(context.Context, DerivedArtifactRead, io.Writer) error { return nil },
		derivedResolverTestActivePipeline,
		derivedResolverTestMalwareSafe,
	)
	if err != nil {
		t.Fatal(err)
	}
	request := derivedResolverTestRequest(fixture)
	request.SourceMediaType = "application/pdf"
	request.Renderer = RendererSafeRaster
	request.Profile = ProfileRasterV1
	if _, err := resolver.Resolve(context.Background(), request); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("duplicate document ordinal zero error=%v", err)
	}
}

func TestDerivedResolverArchiveIndexResolvesExactCanonicalJSON(t *testing.T) {
	db, fixture := derivedResolverFixture(t)
	payload := []byte(`{"schema_version":1,"entries":[{"id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","display_name":"member.txt","size":3,"media_type":"text/plain"}],"expanded_bytes":3,"complete":true}`)
	configureDerivedResolverPayload(t, db, fixture, payload)
	configureDerivedResolverProduct(t, db, fixture, "archive.inspect", "archive.inspect.v1", "archive_index_v1", "metadata", "application/json")
	reader := &derivedArtifactReaderFake{artifactID: fixture.artifactID, payload: payload}
	resolver, err := NewDerivedRepresentationResolver(
		db, reader.Read, derivedResolverTestActivePipeline, derivedResolverTestMalwareSafe,
	)
	if err != nil {
		t.Fatal(err)
	}
	request := derivedResolverTestRequest(fixture)
	request.SourceMediaType = "application/zip"
	request.SourceSize = 1024
	request.Renderer = RendererEscapedText
	request.Profile = ProfileTextV1
	resolved, err := resolver.Resolve(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	session, err := resolver.Open(context.Background(), resolved, SourceRequest{
		Ref: fixture.ref, CatalogGenerationID: fixture.catalogGenerationID,
		ExpectedSource: fixture.sourceFingerprint, ExpectedEntry: resolved.EntryFingerprint,
		Mode: SourceModeSequential, MaxBytes: resolved.Size,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(session.Reader())
	if err != nil || !bytes.Equal(got, payload) || reader.calls != 2 {
		t.Fatalf("archive index payload=%q calls=%d err=%v", got, reader.calls, err)
	}
}

func TestDerivedResolverArchiveIndexViewKeepsOrdinalAndArtifactBindingPrivate(t *testing.T) {
	db, fixture := derivedResolverFixture(t)
	firstID := strings.Repeat("a", 32)
	secondID := strings.Repeat("b", 32)
	payload := []byte(`{"schema_version":1,"entries":[{"id":"` + firstID + `","display_name":"first.txt","size":3,"media_type":"text/plain"},{"id":"` + secondID + `","parent_id":"` + strings.Repeat("c", 32) + `","display_name":"second.pdf","size":5,"media_type":"application/pdf"}],"expanded_bytes":8,"complete":true}`)
	configureDerivedResolverPayload(t, db, fixture, payload)
	configureDerivedResolverProduct(t, db, fixture, "archive.inspect", "archive.inspect.v1", "archive_index_v1", "metadata", "application/json")
	reader := &derivedArtifactReaderFake{artifactID: fixture.artifactID, payload: payload}
	resolver, err := NewDerivedRepresentationResolver(
		db, reader.Read, derivedResolverTestActivePipeline, derivedResolverTestMalwareSafe,
	)
	if err != nil {
		t.Fatal(err)
	}
	asset := derivedResolverTestAsset(fixture)
	asset.MediaType = "application/zip"
	asset.Size = 1024
	index, err := resolver.ResolveArchiveIndex(context.Background(), ArchiveIndexRequest{
		Asset: asset, SecurityPolicyRevision: fixture.policyRevision,
	})
	if err != nil {
		t.Fatal(err)
	}
	if index.SchemaVersion != 1 || index.IndexRevision == "" || len(index.Entries) != 2 ||
		index.ArtifactID() != fixture.artifactID || index.PipelineFingerprint() != derivedResolverTestPipeline ||
		index.SecurityPolicyRevision() != fixture.policyRevision {
		t.Fatalf("archive index=%+v", index)
	}
	if member, ok := index.ResolveMember(secondID); !ok || member.Ordinal != 1 || member.Digest == "" ||
		member.Size != 5 || member.MediaType != "application/pdf" {
		t.Fatalf("resolved member=%+v ok=%v", member, ok)
	}
	encoded, err := json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"ordinal", fixture.artifactID, fixture.setID, fixture.blobID, derivedResolverTestPipeline, "artifact", "locator"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("archive index JSON leaked %q: %s", forbidden, encoded)
		}
	}
	if !strings.Contains(string(encoded), secondID) || !strings.Contains(string(encoded), "second.pdf") {
		t.Fatalf("archive index JSON lost safe member view: %s", encoded)
	}
}

func TestDerivedResolverArchiveMemberBindsExactRequestAndDerivedArtifact(t *testing.T) {
	db, fixture := derivedResolverFixture(t)
	memberID := strings.Repeat("a", 32)
	requestID, contentPayload := configureDerivedArchiveMemberFixture(t, db, fixture, memberID, 7)
	reader := &derivedArtifactReaderMapFake{payloads: map[string][]byte{}}
	var contentArtifact model.BackupAssetDerivedArtifact
	if err := db.Where("artifact_set_id = ? AND role = ?", fixture.setID, "content").Take(&contentArtifact).Error; err != nil {
		t.Fatal(err)
	}
	var metadataArtifact model.BackupAssetDerivedArtifact
	if err := db.Where("artifact_set_id = ? AND role = ?", fixture.setID, "metadata").Take(&metadataArtifact).Error; err != nil {
		t.Fatal(err)
	}
	reader.payloads[contentArtifact.ID] = contentPayload
	reader.payloads[metadataArtifact.ID] = []byte(`{"schema_version":1,"member_id":"` + memberID + `","display_name":"member.txt","size":14,"media_type":"text/plain"}`)
	resolver, err := NewDerivedRepresentationResolver(
		db, reader.Read, derivedResolverTestActivePipeline, derivedResolverTestMalwareSafe,
	)
	if err != nil {
		t.Fatal(err)
	}
	asset := derivedResolverTestAsset(fixture)
	asset.MediaType = "application/zip"
	asset.Size = 1024
	resolved, err := resolver.ResolveArchiveMember(context.Background(), ArchiveMemberArtifactRequest{
		RequestID: requestID, OwnerUserID: 42, Asset: asset,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.MemberRequestID != requestID || resolved.Ref != fixture.ref ||
		resolved.CatalogGenerationID != fixture.catalogGenerationID || resolved.SourceFingerprint != fixture.sourceFingerprint ||
		resolved.EntryFingerprint != fixture.sourceEntryFingerprint || resolved.ProcessingJobID != fixture.jobID ||
		resolved.ProcessingAttemptID == "" || resolved.DerivedArtifactSetID != fixture.setID ||
		resolved.DerivedArtifactID != contentArtifact.ID || resolved.DerivedBlobID != contentArtifact.BlobID ||
		resolved.DerivedDigest != contentArtifact.PlaintextDigest || resolved.DerivedSize != int64(len(contentPayload)) ||
		resolved.MediaType != "text/plain" || resolved.MemberChainDigest != ArchiveMemberChainDigest(fixture.ref, strings.Repeat("3", 64), memberID) {
		t.Fatalf("resolved archive member=%+v", resolved)
	}
	if encoded, err := json.Marshal(resolved); err != nil || string(encoded) != "{}" {
		t.Fatalf("private member binding serialized: %s err=%v", encoded, err)
	}
	var destination bytes.Buffer
	if err := resolver.ReadArchiveMember(context.Background(), resolved, &destination); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(destination.Bytes(), contentPayload) {
		t.Fatalf("member payload=%q", destination.Bytes())
	}
}

func TestDerivedResolverArchiveMemberValidatesSucceededOutputBeforeReadyWithoutServingIt(t *testing.T) {
	db, fixture := derivedResolverFixture(t)
	memberID := strings.Repeat("a", 32)
	requestID, contentPayload := configureDerivedArchiveMemberFixture(t, db, fixture, memberID, 7)
	if err := db.Model(&model.BackupAssetArchiveMemberRequest{}).Where("id = ?", requestID).Updates(map[string]any{
		"state": "running", "finished_at": nil,
	}).Error; err != nil {
		t.Fatal(err)
	}
	reader := &derivedArtifactReaderMapFake{payloads: map[string][]byte{}}
	var contentArtifact model.BackupAssetDerivedArtifact
	if err := db.Where("artifact_set_id = ? AND role = ?", fixture.setID, "content").Take(&contentArtifact).Error; err != nil {
		t.Fatal(err)
	}
	var metadataArtifact model.BackupAssetDerivedArtifact
	if err := db.Where("artifact_set_id = ? AND role = ?", fixture.setID, "metadata").Take(&metadataArtifact).Error; err != nil {
		t.Fatal(err)
	}
	reader.payloads[contentArtifact.ID] = contentPayload
	reader.payloads[metadataArtifact.ID] = []byte(`{"schema_version":1,"member_id":"` + memberID + `","display_name":"member.txt","size":14,"media_type":"text/plain"}`)
	resolver, err := NewDerivedRepresentationResolver(
		db, reader.Read, derivedResolverTestActivePipeline, derivedResolverTestMalwareSafe,
	)
	if err != nil {
		t.Fatal(err)
	}
	asset := derivedResolverTestAsset(fixture)
	asset.MediaType = "application/zip"
	asset.Size = 1024
	request := ArchiveMemberArtifactRequest{RequestID: requestID, OwnerUserID: 42, Asset: asset}

	validated, err := resolver.ValidateArchiveMemberOutput(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if validated.MemberRequestID != requestID || validated.ProcessingJobID != fixture.jobID ||
		validated.DerivedArtifactID != contentArtifact.ID || validated.DerivedSize != int64(len(contentPayload)) {
		t.Fatalf("validated output=%+v", validated)
	}
	if _, err := resolver.ResolveArchiveMember(context.Background(), request); !errors.Is(err, ErrDerivedRepresentationUnavailable) {
		t.Fatalf("running member became serveable: %v", err)
	}
}

func TestDerivedResolverArchiveMemberRejectsMalformedOrCrossMemberMetadata(t *testing.T) {
	memberID := strings.Repeat("a", 32)
	for _, testCase := range []struct {
		name      string
		metadata  []byte
		forbidden string
	}{
		{name: "malformed", metadata: []byte(`{"schema_version":`)},
		{name: "cross member", metadata: []byte(`{"schema_version":1,"member_id":"` + strings.Repeat("b", 32) + `","display_name":"member.txt","size":14,"media_type":"text/plain"}`)},
		{name: "raw path display", metadata: []byte(`{"schema_version":1,"member_id":"` + memberID + `","display_name":"folder/member.txt","size":14,"media_type":"text/plain"}`), forbidden: "folder/member.txt"},
		{name: "size mismatch", metadata: []byte(`{"schema_version":1,"member_id":"` + memberID + `","display_name":"member.txt","size":15,"media_type":"text/plain"}`)},
		{name: "unknown field", metadata: []byte(`{"schema_version":1,"member_id":"` + memberID + `","display_name":"member.txt","size":14,"media_type":"text/plain","path":"secret/member.txt"}`), forbidden: "secret/member.txt"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			db, fixture := derivedResolverFixture(t)
			requestID, contentPayload := configureDerivedArchiveMemberFixture(t, db, fixture, memberID, 7)
			var contentArtifact model.BackupAssetDerivedArtifact
			if err := db.Where("artifact_set_id = ? AND role = ?", fixture.setID, "content").Take(&contentArtifact).Error; err != nil {
				t.Fatal(err)
			}
			var metadataArtifact model.BackupAssetDerivedArtifact
			if err := db.Where("artifact_set_id = ? AND role = ?", fixture.setID, "metadata").Take(&metadataArtifact).Error; err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256(testCase.metadata)
			digestHex := hex.EncodeToString(digest[:])
			if err := db.Model(&model.BackupAssetDerivedArtifact{}).Where("id = ?", metadataArtifact.ID).Updates(map[string]any{
				"plaintext_size": int64(len(testCase.metadata)), "plaintext_digest": digestHex,
			}).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Model(&model.BackupAssetDerivedBlob{}).Where("id = ?", metadataArtifact.BlobID).Updates(map[string]any{
				"plaintext_size": int64(len(testCase.metadata)), "plaintext_digest": digestHex,
			}).Error; err != nil {
				t.Fatal(err)
			}
			reader := &derivedArtifactReaderMapFake{payloads: map[string][]byte{
				contentArtifact.ID: contentPayload, metadataArtifact.ID: testCase.metadata,
			}}
			resolver, err := NewDerivedRepresentationResolver(
				db, reader.Read, derivedResolverTestActivePipeline, derivedResolverTestMalwareSafe,
			)
			if err != nil {
				t.Fatal(err)
			}
			asset := derivedResolverTestAsset(fixture)
			asset.MediaType, asset.Size = "application/zip", 1024
			_, err = resolver.ResolveArchiveMember(context.Background(), ArchiveMemberArtifactRequest{
				RequestID: requestID, OwnerUserID: 42, Asset: asset,
			})
			if !errors.Is(err, ErrDerivedRepresentationUnavailable) {
				t.Fatalf("malformed member metadata error=%v", err)
			}
			if testCase.forbidden != "" && strings.Contains(err.Error(), testCase.forbidden) {
				t.Fatalf("member metadata error leaked %q: %v", testCase.forbidden, err)
			}
		})
	}
}

func TestDerivedResolverArchiveIndexRejectsUntrustedPayloads(t *testing.T) {
	validEntryID := strings.Repeat("a", 32)
	for _, testCase := range []struct {
		name      string
		payload   []byte
		wantReads int
		forbidden string
	}{
		{name: "malformed", payload: []byte(`{"schema_version":`), wantReads: 1},
		{name: "noncanonical", payload: []byte(`{ "schema_version": 1, "entries": [], "expanded_bytes": 0, "complete": true }`), wantReads: 1},
		{name: "arbitrary metadata", payload: []byte(`{"schema_version":1,"duration_millis":0,"streams":[]}`), wantReads: 1},
		{name: "raw path field", payload: []byte(`{"schema_version":1,"entries":[{"id":"` + validEntryID + `","path":"folder/member.txt","display_name":"member.txt","size":1,"media_type":"text/plain"}],"expanded_bytes":1,"complete":true}`), wantReads: 1},
		{name: "raw path display name", payload: []byte(`{"schema_version":1,"entries":[{"id":"` + validEntryID + `","display_name":"folder/member.txt","size":1,"media_type":"text/plain"}],"expanded_bytes":1,"complete":true}`), wantReads: 1},
		{name: "ASCII control display name", payload: canonicalDerivedArchiveIndexPayload(t, "member\u0001.txt"), wantReads: 1, forbidden: "member\u0001.txt"},
		{name: "tab display name", payload: canonicalDerivedArchiveIndexPayload(t, "member\t.txt"), wantReads: 1, forbidden: "member\t.txt"},
		{name: "bidi override display name", payload: canonicalDerivedArchiveIndexPayload(t, "member\u202etxt"), wantReads: 1, forbidden: "member\u202etxt"},
		{name: "zero width format display name", payload: canonicalDerivedArchiveIndexPayload(t, "member\u200b.txt"), wantReads: 1, forbidden: "member\u200b.txt"},
		{name: "confusable dot display name", payload: canonicalDerivedArchiveIndexPayload(t, "\uff0e"), wantReads: 1, forbidden: "\uff0e"},
		{name: "confusable dot dot display name", payload: canonicalDerivedArchiveIndexPayload(t, "\uff0e\uff0e"), wantReads: 1, forbidden: "\uff0e\uff0e"},
		{name: "confusable slash display name", payload: canonicalDerivedArchiveIndexPayload(t, "member\uff0fescape.txt"), wantReads: 1, forbidden: "member\uff0fescape.txt"},
		{name: "confusable reverse slash display name", payload: canonicalDerivedArchiveIndexPayload(t, "member\uff3cescape.txt"), wantReads: 1, forbidden: "member\uff3cescape.txt"},
		{name: "duplicate member", payload: []byte(`{"schema_version":1,"schema_version":1,"entries":[],"expanded_bytes":0,"complete":true}`), wantReads: 1},
		{name: "oversized", payload: bytes.Repeat([]byte("x"), 16<<20+1)},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			db, fixture := derivedResolverFixture(t)
			configureDerivedResolverPayload(t, db, fixture, testCase.payload)
			configureDerivedResolverProduct(t, db, fixture, "archive.inspect", "archive.inspect.v1", "archive_index_v1", "metadata", "application/json")
			reader := &derivedArtifactReaderFake{artifactID: fixture.artifactID, payload: testCase.payload}
			resolver, err := NewDerivedRepresentationResolver(
				db, reader.Read, derivedResolverTestActivePipeline, derivedResolverTestMalwareSafe,
			)
			if err != nil {
				t.Fatal(err)
			}
			request := derivedResolverTestRequest(fixture)
			request.SourceMediaType = "application/zip"
			request.SourceSize = 1024
			request.Renderer = RendererEscapedText
			request.Profile = ProfileTextV1
			if _, err := resolver.Resolve(context.Background(), request); !errors.Is(err, ErrDerivedRepresentationUnavailable) {
				t.Fatalf("untrusted archive payload error=%v", err)
			} else if testCase.forbidden != "" && strings.Contains(err.Error(), testCase.forbidden) {
				t.Fatalf("untrusted archive error leaked display name %q: %v", testCase.forbidden, err)
			}
			if reader.calls != testCase.wantReads {
				t.Fatalf("untrusted archive payload reads=%d want=%d", reader.calls, testCase.wantReads)
			}
		})
	}
}

func TestDerivedArchiveDisplayNameAllowsSafeNormalizedUnicode(t *testing.T) {
	for _, displayName := range []string{
		"r\u00e9sum\u00e9.txt",
		"cafe\u0301.txt",
		"\uff21.txt",
		"\u6771\u4eac-\u8cc7\u6599.txt",
		"\u0645\u0644\u0641.txt",
		"receipt-\U0001f9fe.txt",
	} {
		if !safeDerivedArchiveDisplayName(displayName) {
			t.Fatalf("safe normalized Unicode display name rejected: %q", displayName)
		}
	}
}

func TestDerivedResolverArchiveIndexRejectsNormalizedDisplayNameCollisions(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		first string
		last  string
	}{
		{name: "canonical normalization", first: "caf\u00e9.txt", last: "cafe\u0301.txt"},
		{name: "compatibility normalization", first: "A.txt", last: "\uff21.txt"},
		{name: "Unicode case fold", first: "Stra\u00dfe.txt", last: "STRASSE.txt"},
		{name: "default ignorable code point", first: "a.txt", last: "a\u034f.txt"},
		{name: "variation selector", first: "a.txt", last: "a\ufe0f.txt"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			db, fixture := derivedResolverFixture(t)
			payload := canonicalDerivedArchiveIndexPayload(t, testCase.first, testCase.last)
			configureDerivedResolverPayload(t, db, fixture, payload)
			configureDerivedResolverProduct(t, db, fixture, "archive.inspect", "archive.inspect.v1", "archive_index_v1", "metadata", "application/json")
			reader := &derivedArtifactReaderFake{artifactID: fixture.artifactID, payload: payload}
			resolver, err := NewDerivedRepresentationResolver(
				db, reader.Read, derivedResolverTestActivePipeline, derivedResolverTestMalwareSafe,
			)
			if err != nil {
				t.Fatal(err)
			}
			request := derivedResolverTestRequest(fixture)
			request.SourceMediaType = "application/zip"
			request.SourceSize = 1024
			request.Renderer = RendererEscapedText
			request.Profile = ProfileTextV1
			if _, err := resolver.Resolve(context.Background(), request); !errors.Is(err, ErrDerivedRepresentationUnavailable) {
				t.Fatalf("normalized display-name collision error=%v", err)
			}
		})
	}
}

func canonicalDerivedArchiveIndexPayload(t *testing.T, displayNames ...string) []byte {
	t.Helper()
	entries := make([]derivedArchiveIndexEntry, 0, len(displayNames))
	for index, displayName := range displayNames {
		entries = append(entries, derivedArchiveIndexEntry{
			ID: fmt.Sprintf("%032x", index+1), DisplayName: displayName,
			MediaType: "application/octet-stream",
		})
	}
	payload, err := json.Marshal(derivedArchiveIndex{
		SchemaVersion: 1, Entries: entries, ExpandedBytes: 0, Complete: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func TestDerivedResolverArchiveIndexRejectsOtherArtifactProducts(t *testing.T) {
	for _, testCase := range []struct {
		name             string
		capability       string
		capabilitySchema string
		outputProfile    string
		ordinal          int
		role             string
		mediaType        string
		renderer         Renderer
		profile          RendererProfile
	}{
		{name: "metadata hex", capability: "archive.inspect", capabilitySchema: "archive.inspect.v1", outputProfile: "archive_index_v1", role: "metadata", mediaType: "application/json", renderer: RendererMetadataHex, profile: ProfileHexV1},
		{name: "arbitrary metadata capability", capability: "media.probe", capabilitySchema: "media.probe.v1", outputProfile: "media_probe_v1", role: "metadata", mediaType: "application/json", renderer: RendererEscapedText, profile: ProfileTextV1},
		{name: "member retrieval", capability: "archive.extract_entry", capabilitySchema: "archive.extract_entry.v1", outputProfile: "archive_member_v1", role: "metadata", mediaType: "application/json", renderer: RendererEscapedText, profile: ProfileTextV1},
		{name: "wrong profile", capability: "archive.inspect", capabilitySchema: "archive.inspect.v1", outputProfile: "archive_member_v1", role: "metadata", mediaType: "application/json", renderer: RendererEscapedText, profile: ProfileTextV1},
		{name: "wrong ordinal", capability: "archive.inspect", capabilitySchema: "archive.inspect.v1", outputProfile: "archive_index_v1", ordinal: 1, role: "metadata", mediaType: "application/json", renderer: RendererEscapedText, profile: ProfileTextV1},
		{name: "wrong role", capability: "archive.inspect", capabilitySchema: "archive.inspect.v1", outputProfile: "archive_index_v1", role: "content", mediaType: "application/json", renderer: RendererEscapedText, profile: ProfileTextV1},
		{name: "wrong media", capability: "archive.inspect", capabilitySchema: "archive.inspect.v1", outputProfile: "archive_index_v1", role: "metadata", mediaType: "text/plain", renderer: RendererEscapedText, profile: ProfileTextV1},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			db, fixture := derivedResolverFixture(t)
			configureDerivedResolverProduct(t, db, fixture, testCase.capability, testCase.capabilitySchema, testCase.outputProfile, testCase.role, testCase.mediaType)
			if testCase.ordinal != 0 {
				if err := db.Model(&model.BackupAssetDerivedArtifact{}).Where("id = ?", fixture.artifactID).
					Update("ordinal", testCase.ordinal).Error; err != nil {
					t.Fatal(err)
				}
			}
			resolver, err := NewDerivedRepresentationResolver(
				db,
				func(context.Context, DerivedArtifactRead, io.Writer) error { return nil },
				derivedResolverTestActivePipeline,
				derivedResolverTestMalwareSafe,
			)
			if err != nil {
				t.Fatal(err)
			}
			request := derivedResolverTestRequest(fixture)
			request.SourceMediaType = "application/zip"
			request.Renderer = testCase.renderer
			request.Profile = testCase.profile
			if _, err := resolver.Resolve(context.Background(), request); !errors.Is(err, ErrDerivedRepresentationUnavailable) {
				t.Fatalf("other archive product error=%v", err)
			}
		})
	}
}

func TestDerivedAttemptResolverStreamsCompleteTextWithoutProviderFallback(t *testing.T) {
	db, fixture := derivedResolverFixture(t)
	payload := []byte("safe derived text")
	reader := &derivedArtifactReaderFake{artifactID: fixture.artifactID, payload: payload}
	derived, err := NewDerivedRepresentationResolver(
		db, reader.Read, derivedResolverTestActivePipeline, derivedResolverTestMalwareSafe,
	)
	if err != nil {
		t.Fatal(err)
	}
	primary := &derivedPrimaryResolverFake{payload: []byte("provider source must not be read")}
	resolver, err := NewDerivedAttemptSourceResolver(
		primary,
		derived,
		fixture.policyRevision,
		func(_ context.Context, ref backupasset.AssetRef, catalogGenerationID, sourceFingerprint string) (AuthorizedAsset, error) {
			if ref != fixture.ref || catalogGenerationID != fixture.catalogGenerationID || sourceFingerprint != fixture.sourceFingerprint {
				t.Fatalf("provider binding ref=%+v catalog=%q source=%q", ref, catalogGenerationID, sourceFingerprint)
			}
			return derivedResolverTestAsset(fixture), nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 20, 8, 30, 0, 0, time.UTC)
	budget := &attemptBudgetFake{}
	broker, err := NewAttemptBroker(resolver, budget, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	binding := AttemptSourceBinding{
		SessionID: strings.Repeat("e", 32), Ref: fixture.ref, CatalogGenerationID: fixture.catalogGenerationID,
		SourceFingerprint: fixture.sourceFingerprint, EntryFingerprint: fixture.digest,
		AllowedModes: []SourceMode{SourceModeStat, SourceModeSequential},
		Limits: AttemptReadLimits{
			MaxBytesPerRequest: 16 << 20, MaxCumulativeBytes: 16 << 20, MaxRequests: 4, MaxInFlight: 1,
		},
		AbsoluteExpiresAt: now.Add(time.Minute),
	}
	session, info, err := broker.OpenSession(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size != int64(len(payload)) || info.MediaType != "text/plain" || !info.Sequential || info.Range {
		t.Fatalf("Derived attempt source info=%+v", info)
	}
	handle, err := session.OpenSequential(context.Background(), int64(len(payload)))
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(handle)
	if err != nil {
		t.Fatal(err)
	}
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) || reader.calls != 1 || primary.opens != 0 {
		t.Fatalf("Derived attempt payload=%q reads=%d Provider opens=%d", got, reader.calls, primary.opens)
	}
	if snapshot := budget.snapshot(); snapshot.charged != 0 || snapshot.unknown != 0 || snapshot.finalizations != 2 {
		t.Fatalf("Derived attempt budget=%+v", snapshot)
	}
}

func TestDerivedAttemptResolverRevalidatesLiveCatalogSourceAfterOpen(t *testing.T) {
	db, fixture := derivedResolverFixture(t)
	derived, err := NewDerivedRepresentationResolver(
		db,
		func(context.Context, DerivedArtifactRead, io.Writer) error { return nil },
		derivedResolverTestActivePipeline,
		derivedResolverTestMalwareSafe,
	)
	if err != nil {
		t.Fatal(err)
	}
	current := derivedResolverTestAsset(fixture)
	resolver, err := NewDerivedAttemptSourceResolver(
		&derivedPrimaryResolverFake{payload: []byte("must not fall back")},
		derived,
		fixture.policyRevision,
		func(context.Context, backupasset.AssetRef, string, string) (AuthorizedAsset, error) {
			return current, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	session, err := resolver.OpenContentSource(context.Background(), SourceRequest{
		Ref: fixture.ref, CatalogGenerationID: fixture.catalogGenerationID,
		ExpectedSource: fixture.sourceFingerprint, ExpectedEntry: fixture.digest,
		Mode: SourceModeStat,
	})
	if err != nil {
		t.Fatal(err)
	}
	current.EntryFingerprint = strings.Repeat("f", 64)
	if err := session.Revalidate(context.Background()); !errors.Is(err, ErrDerivedRepresentationUnavailable) {
		t.Fatalf("live Catalog/source drift error=%v", err)
	}
}

func TestDerivedAttemptResolverFallsBackOnlyWhenNoDerivedIdentityExists(t *testing.T) {
	db, fixture := derivedResolverFixture(t)
	derived, err := NewDerivedRepresentationResolver(
		db,
		func(context.Context, DerivedArtifactRead, io.Writer) error { return nil },
		derivedResolverTestActivePipeline,
		derivedResolverTestMalwareSafe,
	)
	if err != nil {
		t.Fatal(err)
	}
	primary := &derivedPrimaryResolverFake{payload: []byte("original provider source")}
	resolver, err := NewDerivedAttemptSourceResolver(
		primary, derived, fixture.policyRevision,
		func(context.Context, backupasset.AssetRef, string, string) (AuthorizedAsset, error) {
			return derivedResolverTestAsset(fixture), nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	request := SourceRequest{
		Ref: fixture.ref, CatalogGenerationID: fixture.catalogGenerationID,
		ExpectedSource: fixture.sourceFingerprint, ExpectedEntry: "original-entry-v1", Mode: SourceModeStat,
	}
	session, err := resolver.OpenContentSource(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if primary.opens != 1 || session.Stat().EntryFingerprint != request.ExpectedEntry {
		t.Fatalf("Provider fallback opens=%d stat=%+v", primary.opens, session.Stat())
	}
}

func TestDerivedAttemptResolverFailsClosedForKnownInvalidDerivedIdentity(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		mutate func(*testing.T, *gorm.DB, derivedResolverTestBinding)
		policy string
	}{
		{
			name: "partial",
			mutate: func(t *testing.T, db *gorm.DB, fixture derivedResolverTestBinding) {
				t.Helper()
				if err := db.Model(&model.BackupAssetDerivedArtifactSet{}).Where("id = ?", fixture.setID).
					Updates(map[string]any{"completeness": "partial"}).Error; err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "revoked",
			mutate: func(t *testing.T, db *gorm.DB, fixture derivedResolverTestBinding) {
				t.Helper()
				if err := db.Model(&model.BackupAssetDerivedArtifactSet{}).Where("id = ?", fixture.setID).
					Update("state", "stale").Error; err != nil {
					t.Fatal(err)
				}
			},
		},
		{name: "policy drift", mutate: func(*testing.T, *gorm.DB, derivedResolverTestBinding) {}, policy: "security-policy-v2"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			db, fixture := derivedResolverFixture(t)
			testCase.mutate(t, db, fixture)
			derived, err := NewDerivedRepresentationResolver(
				db,
				func(context.Context, DerivedArtifactRead, io.Writer) error { return nil },
				derivedResolverTestActivePipeline,
				derivedResolverTestMalwareSafe,
			)
			if err != nil {
				t.Fatal(err)
			}
			primary := &derivedPrimaryResolverFake{payload: []byte("must not fall back")}
			policy := testCase.policy
			if policy == "" {
				policy = fixture.policyRevision
			}
			resolver, err := NewDerivedAttemptSourceResolver(
				primary, derived, policy,
				func(context.Context, backupasset.AssetRef, string, string) (AuthorizedAsset, error) {
					return derivedResolverTestAsset(fixture), nil
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			_, err = resolver.OpenContentSource(context.Background(), SourceRequest{
				Ref: fixture.ref, CatalogGenerationID: fixture.catalogGenerationID,
				ExpectedSource: fixture.sourceFingerprint, ExpectedEntry: fixture.digest, Mode: SourceModeStat,
			})
			if !errors.Is(err, ErrDerivedRepresentationUnavailable) || primary.opens != 0 {
				t.Fatalf("invalid Derived error=%v Provider opens=%d", err, primary.opens)
			}
		})
	}
}

func TestDerivedAttemptResolverNeverUsesArchiveMemberArtifactAsNestedWorkerInput(t *testing.T) {
	db, fixture := derivedResolverFixture(t)
	memberID := strings.Repeat("a", 32)
	configureDerivedArchiveMemberFixture(t, db, fixture, memberID, 7)
	var member model.BackupAssetDerivedArtifact
	if err := db.Where("artifact_set_id = ? AND role = ?", fixture.setID, "content").Take(&member).Error; err != nil {
		t.Fatal(err)
	}
	derived, err := NewDerivedRepresentationResolver(
		db,
		func(context.Context, DerivedArtifactRead, io.Writer) error { return nil },
		derivedResolverTestActivePipeline,
		derivedResolverTestMalwareSafe,
	)
	if err != nil {
		t.Fatal(err)
	}
	primary := &derivedPrimaryResolverFake{payload: []byte("must not open the outer Provider")}
	asset := derivedResolverTestAsset(fixture)
	asset.MediaType = "application/zip"
	asset.Size = 1024
	resolver, err := NewDerivedAttemptSourceResolver(
		primary, derived, fixture.policyRevision,
		func(context.Context, backupasset.AssetRef, string, string) (AuthorizedAsset, error) {
			return asset, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = resolver.OpenContentSource(context.Background(), SourceRequest{
		Ref: fixture.ref, CatalogGenerationID: fixture.catalogGenerationID,
		ExpectedSource: fixture.sourceFingerprint, ExpectedEntry: member.PlaintextDigest,
		Mode: SourceModeStat,
	})
	if !errors.Is(err, ErrDerivedRepresentationUnavailable) || primary.opens != 0 {
		t.Fatalf("archive-member nested input error=%v Provider opens=%d", err, primary.opens)
	}
}

type derivedResolverTestBinding struct {
	ref                    backupasset.AssetRef
	catalogGenerationID    string
	sourceFingerprint      string
	policyRevision         string
	setID                  string
	artifactID             string
	blobID                 string
	digest                 string
	jobID                  string
	sourceEntryFingerprint string
}

const derivedResolverTestPipeline = "derived-resolver-pipeline-v1"

func derivedResolverTestActivePipeline(context.Context, string, string) (string, error) {
	return derivedResolverTestPipeline, nil
}

func derivedResolverTestMalwareSafe(context.Context, AuthorizedAsset) (bool, error) {
	return true, nil
}

func derivedResolverTestRequest(fixture derivedResolverTestBinding) DerivedRepresentationRequest {
	return DerivedRepresentationRequest{
		Ref: fixture.ref, CatalogGenerationID: fixture.catalogGenerationID,
		SourceFingerprint:      fixture.sourceFingerprint,
		SourceEntryFingerprint: fixture.sourceEntryFingerprint,
		FingerprintStrength:    "strong", ProviderCapabilityRevision: 1,
		SourceSize: int64(len("safe derived text")), SourceMediaType: "text/plain",
		SecurityPolicyRevision: fixture.policyRevision, Provider: backupasset.ProviderRestic,
		Renderer: RendererEscapedText, Profile: ProfileTextV1,
	}
}

func derivedResolverTestAsset(fixture derivedResolverTestBinding) AuthorizedAsset {
	request := derivedResolverTestRequest(fixture)
	return derivedSafetyAsset(request)
}

type derivedMalwareSafetyFake struct {
	safe   bool
	assets []AuthorizedAsset
}

func (fake *derivedMalwareSafetyFake) Check(_ context.Context, asset AuthorizedAsset) (bool, error) {
	fake.assets = append(fake.assets, asset)
	return fake.safe, nil
}

func derivedResolverFixture(t *testing.T) (*gorm.DB, derivedResolverTestBinding) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+contentTestDBName(t)+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&model.BackupAssetProcessingJob{}, &model.BackupAssetProcessingAttempt{},
		&model.BackupAssetDerivedArtifactSet{}, &model.BackupAssetDerivedArtifact{},
		&model.BackupAssetDerivedBlobReference{}, &model.BackupAssetDerivedBlob{},
		&model.BackupAssetArchiveMemberRequest{},
	); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 20, 8, 0, 0, 0, time.UTC)
	fixture := derivedResolverTestBinding{
		ref:                 backupasset.AssetRef{RecoveryPointID: strings.Repeat("1", 32), EntryID: strings.Repeat("2", 64)},
		catalogGenerationID: strings.Repeat("3", 32), sourceFingerprint: strings.Repeat("4", 64),
		policyRevision: "security-policy-v1", setID: strings.Repeat("5", 32),
		artifactID: strings.Repeat("6", 32), digest: strings.Repeat("7", 64), jobID: strings.Repeat("9", 32),
		sourceEntryFingerprint: strings.Repeat("d", 64),
	}
	attemptID := strings.Repeat("a", 32)
	blobID := strings.Repeat("8", 32)
	fixture.blobID = blobID
	finishedAt := now
	if err := db.Create(&model.BackupAssetProcessingJob{
		ID: fixture.jobID, WorkKey: strings.Repeat("e", 64),
		DescriptorSchemaVersion: 1,
		RecoveryPointID:         fixture.ref.RecoveryPointID, CatalogGenerationID: fixture.catalogGenerationID,
		EntryID: fixture.ref.EntryID, SourceFingerprint: fixture.sourceFingerprint,
		EntryFingerprint: fixture.sourceEntryFingerprint, ProviderCapabilityRevision: 1,
		Capability: "text.extract", CapabilitySchema: "text.extract.v1",
		PipelineFingerprint: derivedResolverTestPipeline, OutputProfile: "bounded_text_v1",
		SecurityPolicyRevision: fixture.policyRevision,
		DescriptorCanonical:    []byte(`{}`), State: "succeeded", TransitionRevision: 1,
		PriorityClass: "background", EffectivePriority: 1, CurrentAttemptID: &attemptID,
		IsCurrent: false, QueuedAt: now, FinishedAt: &finishedAt, AbsoluteDeadline: now.Add(time.Hour),
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.BackupAssetProcessingAttempt{
		ID: attemptID, JobID: fixture.jobID, AttemptNumber: 1, WorkerID: strings.Repeat("f", 32),
		SlotClass: "background", State: "succeeded", WorkerLeaseExpiresAt: now.Add(time.Minute),
		LastHeartbeatAt: now, RecoveryPointLeaseID: strings.Repeat("1", 32),
		RecoveryPointAttemptID: strings.Repeat("2", 32), RecoveryPointFenceHash: strings.Repeat("3", 64),
		AbsoluteDeadline: now.Add(time.Hour), IsCurrent: false, StartedAt: now, FinishedAt: &finishedAt,
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.BackupAssetProcessingAttempt{}).Where("id = ?", attemptID).
		Update("is_current", false).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.BackupAssetDerivedArtifactSet{
		ID: fixture.setID, JobID: fixture.jobID, AttemptID: attemptID, WorkKey: strings.Repeat("b", 64),
		RecoveryPointID: fixture.ref.RecoveryPointID, CatalogGenerationID: fixture.catalogGenerationID,
		EntryID: fixture.ref.EntryID, SourceFingerprint: fixture.sourceFingerprint,
		SecurityPolicyRevision: fixture.policyRevision, ManifestDigest: strings.Repeat("c", 64),
		State: "active", Completeness: "complete", ArtifactCount: 1, TotalPlaintextBytes: int64(len("safe derived text")),
		ProjectionRequired: true, ProjectionPublished: true, ProjectionRevision: 1, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.BackupAssetProcessingJob{}).Where("id = ?", fixture.jobID).Updates(map[string]any{
		"current_artifact_set_id": fixture.setID,
		"finished_at":             &finishedAt,
		"is_current":              false,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.BackupAssetDerivedBlob{
		ID: blobID, PlaintextDigest: fixture.digest, PlaintextSize: int64(len("safe derived text")), PhysicalSize: 128,
		CipherFormatVersion: 1, ChunkSize: 64 << 10, ChunkCount: 1, NoncePrefix: []byte("12345678"),
		OpaqueLocator: blobID + ".xrd", WrappedDEK: bytes.Repeat([]byte{1}, 48), EnvelopeNonce: bytes.Repeat([]byte{2}, 12),
		DerivedKEKVersion: 1, State: "active", RefCount: 1, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.BackupAssetDerivedArtifact{
		ID: fixture.artifactID, ArtifactSetID: fixture.setID, Ordinal: 0, Role: "content", MediaType: "text/plain",
		PlaintextSize: int64(len("safe derived text")), PlaintextDigest: fixture.digest, Completeness: "complete",
		CoverageCanonical: []byte(`{"schema_version":1}`), BlobID: blobID, CreatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.BackupAssetDerivedBlobReference{
		ID: strings.Repeat("d", 32), BlobID: blobID, ArtifactID: fixture.artifactID,
		RecoveryPointID: fixture.ref.RecoveryPointID, CatalogGenerationID: fixture.catalogGenerationID,
		EntryID: fixture.ref.EntryID, SourceFingerprint: fixture.sourceFingerprint,
		State: "active", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	return db, fixture
}

func configureDerivedArchiveMemberFixture(
	t *testing.T,
	db *gorm.DB,
	fixture derivedResolverTestBinding,
	memberID string,
	ordinal int,
) (string, []byte) {
	t.Helper()
	now := time.Date(2026, 7, 20, 8, 0, 0, 0, time.UTC)
	contentPayload := []byte("member payload")
	contentDigest := sha256.Sum256(contentPayload)
	metadataPayload := []byte(`{"schema_version":1,"member_id":"` + memberID + `","display_name":"member.txt","size":14,"media_type":"text/plain"}`)
	metadataDigest := sha256.Sum256(metadataPayload)
	metadataArtifactID := strings.Repeat("b", 32)
	metadataBlobID := strings.Repeat("c", 32)
	metadataReferenceID := strings.Repeat("e", 32)
	if err := db.Model(&model.BackupAssetProcessingJob{}).Where("id = ?", fixture.jobID).Updates(map[string]any{
		"capability": "archive.extract_entry", "capability_schema": "archive.extract_entry.v1",
		"output_profile": "archive_member_v1", "descriptor_canonical": []byte(fmt.Sprintf(`{"schema_version":1,"parameters":{"member_start":%d,"member_end":%d}}`, ordinal, ordinal)),
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.BackupAssetDerivedArtifact{}).Where("id = ?", fixture.artifactID).Updates(map[string]any{
		"ordinal": 0, "role": "content", "media_type": "text/plain", "plaintext_size": int64(len(contentPayload)),
		"plaintext_digest": hex.EncodeToString(contentDigest[:]), "completeness": "complete",
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.BackupAssetDerivedBlob{}).Where("id = ?", fixture.blobID).Updates(map[string]any{
		"plaintext_size": int64(len(contentPayload)), "plaintext_digest": hex.EncodeToString(contentDigest[:]),
	}).Error; err != nil {
		t.Fatal(err)
	}
	metadataBlob := model.BackupAssetDerivedBlob{
		ID: metadataBlobID, PlaintextDigest: hex.EncodeToString(metadataDigest[:]), PlaintextSize: int64(len(metadataPayload)),
		PhysicalSize: 256, CipherFormatVersion: 1, ChunkSize: 64 << 10, ChunkCount: 1,
		NoncePrefix: []byte("87654321"), OpaqueLocator: metadataBlobID + ".xrd",
		WrappedDEK: bytes.Repeat([]byte{3}, 48), EnvelopeNonce: bytes.Repeat([]byte{4}, 12),
		DerivedKEKVersion: 1, State: "active", RefCount: 1, CreatedAt: now, UpdatedAt: now,
	}
	metadataArtifact := model.BackupAssetDerivedArtifact{
		ID: metadataArtifactID, ArtifactSetID: fixture.setID, Ordinal: 1, Role: "metadata", MediaType: "application/json",
		PlaintextSize: int64(len(metadataPayload)), PlaintextDigest: metadataBlob.PlaintextDigest, Completeness: "complete",
		CoverageCanonical: []byte(`{"schema_version":1}`), BlobID: metadataBlobID, CreatedAt: now,
	}
	metadataReference := model.BackupAssetDerivedBlobReference{
		ID: metadataReferenceID, BlobID: metadataBlobID, ArtifactID: metadataArtifactID,
		RecoveryPointID: fixture.ref.RecoveryPointID, CatalogGenerationID: fixture.catalogGenerationID,
		EntryID: fixture.ref.EntryID, SourceFingerprint: fixture.sourceFingerprint,
		State: "active", CreatedAt: now, UpdatedAt: now,
	}
	for _, value := range []any{&metadataBlob, &metadataArtifact, &metadataReference} {
		if err := db.Create(value).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Model(&model.BackupAssetDerivedArtifactSet{}).Where("id = ?", fixture.setID).Updates(map[string]any{
		"artifact_count": 2, "total_plaintext_bytes": int64(len(contentPayload) + len(metadataPayload)),
		"completeness": "complete",
	}).Error; err != nil {
		t.Fatal(err)
	}
	requestID := strings.Repeat("0", 32)
	interestID := strings.Repeat("1", 32)
	jobID := fixture.jobID
	finished := now
	request := model.BackupAssetArchiveMemberRequest{
		ID: requestID, OwnerUserID: 42, Endpoint: "archive_member_create",
		KeyDigest: strings.Repeat("1", 64), RequestIntentDigest: strings.Repeat("2", 64),
		RecoveryPointID: fixture.ref.RecoveryPointID, EntryID: fixture.ref.EntryID,
		CatalogGenerationID: fixture.catalogGenerationID, SourceFingerprint: fixture.sourceFingerprint,
		EntryFingerprint: fixture.sourceEntryFingerprint, IndexArtifactID: strings.Repeat("f", 32),
		IndexRevision:     strings.Repeat("3", 64),
		MemberChainDigest: ArchiveMemberChainDigest(fixture.ref, strings.Repeat("3", 64), memberID),
		ResolvedOrdinal:   ordinal, ProcessingInterestID: &interestID, ProcessingJobID: &jobID,
		State: "ready", AbsoluteExpiresAt: now.Add(2 * time.Hour), CreatedAt: now, UpdatedAt: now,
		FinishedAt: &finished, Version: 2,
	}
	if err := db.Create(&request).Error; err != nil {
		t.Fatal(err)
	}
	return requestID, contentPayload
}

func configureDerivedResolverProduct(
	t *testing.T,
	db *gorm.DB,
	fixture derivedResolverTestBinding,
	capability string,
	capabilitySchema string,
	outputProfile string,
	role string,
	mediaType string,
) {
	t.Helper()
	if err := db.Model(&model.BackupAssetProcessingJob{}).Where("id = ?", fixture.jobID).Updates(map[string]any{
		"capability": capability, "capability_schema": capabilitySchema, "output_profile": outputProfile,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.BackupAssetDerivedArtifact{}).Where("id = ?", fixture.artifactID).Updates(map[string]any{
		"ordinal": 0, "role": role, "media_type": mediaType,
	}).Error; err != nil {
		t.Fatal(err)
	}
}

func configureDerivedResolverPayload(t *testing.T, db *gorm.DB, fixture derivedResolverTestBinding, payload []byte) {
	t.Helper()
	digest := sha256.Sum256(payload)
	digestHex := fmt.Sprintf("%x", digest[:])
	if err := db.Model(&model.BackupAssetDerivedArtifact{}).Where("id = ?", fixture.artifactID).Updates(map[string]any{
		"plaintext_size": int64(len(payload)), "plaintext_digest": digestHex,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.BackupAssetDerivedBlob{}).Where("id = ?", fixture.blobID).Updates(map[string]any{
		"plaintext_size": int64(len(payload)), "plaintext_digest": digestHex,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.BackupAssetDerivedArtifactSet{}).Where("id = ?", fixture.setID).
		Update("total_plaintext_bytes", int64(len(payload))).Error; err != nil {
		t.Fatal(err)
	}
}

func addDerivedResolverArtifact(
	t *testing.T,
	db *gorm.DB,
	fixture derivedResolverTestBinding,
	ordinal int,
	role string,
	mediaType string,
	artifactCharacter string,
	blobCharacter string,
	referenceCharacter string,
) {
	t.Helper()
	now := time.Date(2026, 7, 20, 8, ordinal, 0, 0, time.UTC)
	artifactID := strings.Repeat(artifactCharacter, 32)
	blobID := strings.Repeat(blobCharacter, 32)
	digest := strings.Repeat(blobCharacter, 64)
	blob := model.BackupAssetDerivedBlob{
		ID: blobID, PlaintextDigest: digest, PlaintextSize: 16, PhysicalSize: 128,
		CipherFormatVersion: 1, ChunkSize: 64 << 10, ChunkCount: 1, NoncePrefix: []byte("12345678"),
		OpaqueLocator: blobID + ".xrd", WrappedDEK: bytes.Repeat([]byte{1}, 48),
		EnvelopeNonce: bytes.Repeat([]byte{2}, 12), DerivedKEKVersion: 1,
		State: "active", RefCount: 1, CreatedAt: now, UpdatedAt: now,
	}
	artifact := model.BackupAssetDerivedArtifact{
		ID: artifactID, ArtifactSetID: fixture.setID, Ordinal: ordinal, Role: role, MediaType: mediaType,
		PlaintextSize: 16, PlaintextDigest: digest, Completeness: "complete",
		CoverageCanonical: []byte(`{"schema_version":1}`), BlobID: blobID, CreatedAt: now,
	}
	reference := model.BackupAssetDerivedBlobReference{
		ID: strings.Repeat(referenceCharacter, 32), BlobID: blobID, ArtifactID: artifactID,
		RecoveryPointID: fixture.ref.RecoveryPointID, CatalogGenerationID: fixture.catalogGenerationID,
		EntryID: fixture.ref.EntryID, SourceFingerprint: fixture.sourceFingerprint,
		State: "active", CreatedAt: now, UpdatedAt: now,
	}
	for _, value := range []any{&blob, &artifact, &reference} {
		if err := db.Create(value).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Model(&model.BackupAssetDerivedArtifactSet{}).Where("id = ?", fixture.setID).Updates(map[string]any{
		"artifact_count":        gorm.Expr("artifact_count + 1"),
		"total_plaintext_bytes": gorm.Expr("total_plaintext_bytes + ?", artifact.PlaintextSize),
	}).Error; err != nil {
		t.Fatal(err)
	}
}

func duplicateDerivedResolverPublication(t *testing.T, db *gorm.DB, fixture derivedResolverTestBinding) {
	t.Helper()
	now := time.Date(2026, 7, 20, 8, 1, 0, 0, time.UTC)
	jobID := strings.Repeat("0", 32)
	attemptID := strings.Repeat("1", 32)
	setID := strings.Repeat("2", 32)
	artifactID := strings.Repeat("3", 32)
	blobID := strings.Repeat("4", 32)
	finishedAt := now
	job := model.BackupAssetProcessingJob{
		ID: jobID, WorkKey: strings.Repeat("5", 64), DescriptorSchemaVersion: 1,
		DescriptorCanonical: []byte(`{}`), RecoveryPointID: fixture.ref.RecoveryPointID,
		CatalogGenerationID: fixture.catalogGenerationID, EntryID: fixture.ref.EntryID,
		SourceFingerprint: fixture.sourceFingerprint, EntryFingerprint: fixture.sourceEntryFingerprint,
		ProviderCapabilityRevision: 1, Capability: "text.extract", CapabilitySchema: "text.extract.v1",
		PipelineFingerprint: derivedResolverTestPipeline, OutputProfile: "bounded_text_v1",
		SecurityPolicyRevision: fixture.policyRevision, PriorityClass: "background", EffectivePriority: 1,
		State: "succeeded", TransitionRevision: 2, CurrentAttemptID: &attemptID,
		CurrentArtifactSetID: &setID, IsCurrent: false, QueuedAt: now, FinishedAt: &finishedAt,
		AbsoluteDeadline: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now,
	}
	attempt := model.BackupAssetProcessingAttempt{
		ID: attemptID, JobID: jobID, AttemptNumber: 1, WorkerID: strings.Repeat("6", 32),
		SlotClass: "background", State: "succeeded", WorkerLeaseExpiresAt: now.Add(time.Minute),
		LastHeartbeatAt: now, RecoveryPointLeaseID: strings.Repeat("7", 32),
		RecoveryPointAttemptID: strings.Repeat("8", 32), RecoveryPointFenceHash: strings.Repeat("9", 64),
		AbsoluteDeadline: now.Add(time.Hour), IsCurrent: false, StartedAt: now, FinishedAt: &finishedAt,
		CreatedAt: now, UpdatedAt: now,
	}
	set := model.BackupAssetDerivedArtifactSet{
		ID: setID, JobID: jobID, AttemptID: attemptID, WorkKey: job.WorkKey,
		RecoveryPointID: fixture.ref.RecoveryPointID, CatalogGenerationID: fixture.catalogGenerationID,
		EntryID: fixture.ref.EntryID, SourceFingerprint: fixture.sourceFingerprint,
		SecurityPolicyRevision: fixture.policyRevision, ManifestDigest: strings.Repeat("a", 64),
		State: "active", Completeness: "complete", ArtifactCount: 1,
		TotalPlaintextBytes: int64(len("second derived text")), ProjectionRequired: true,
		ProjectionPublished: true, ProjectionRevision: 2, CreatedAt: now, UpdatedAt: now,
	}
	blob := model.BackupAssetDerivedBlob{
		ID: blobID, PlaintextDigest: strings.Repeat("b", 64), PlaintextSize: int64(len("second derived text")),
		PhysicalSize: 128, CipherFormatVersion: 1, ChunkSize: 64 << 10, ChunkCount: 1,
		NoncePrefix: []byte("12345678"), OpaqueLocator: blobID + ".xrd",
		WrappedDEK: bytes.Repeat([]byte{1}, 48), EnvelopeNonce: bytes.Repeat([]byte{2}, 12),
		DerivedKEKVersion: 1, State: "active", RefCount: 1, CreatedAt: now, UpdatedAt: now,
	}
	artifact := model.BackupAssetDerivedArtifact{
		ID: artifactID, ArtifactSetID: setID, Ordinal: 0, Role: "content", MediaType: "text/plain",
		PlaintextSize: blob.PlaintextSize, PlaintextDigest: blob.PlaintextDigest, Completeness: "complete",
		CoverageCanonical: []byte(`{"schema_version":1}`), BlobID: blobID, CreatedAt: now,
	}
	reference := model.BackupAssetDerivedBlobReference{
		ID: strings.Repeat("c", 32), BlobID: blobID, ArtifactID: artifactID,
		RecoveryPointID: fixture.ref.RecoveryPointID, CatalogGenerationID: fixture.catalogGenerationID,
		EntryID: fixture.ref.EntryID, SourceFingerprint: fixture.sourceFingerprint,
		State: "active", CreatedAt: now, UpdatedAt: now,
	}
	for _, value := range []any{&job, &attempt, &set, &blob, &artifact, &reference} {
		if err := db.Create(value).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, target := range []struct {
		model any
		id    string
	}{{&model.BackupAssetProcessingJob{}, jobID}, {&model.BackupAssetProcessingAttempt{}, attemptID}} {
		if err := db.Model(target.model).Where("id = ?", target.id).Update("is_current", false).Error; err != nil {
			t.Fatal(err)
		}
	}
}

type derivedArtifactReaderFake struct {
	artifactID string
	payload    []byte
	calls      int
}

type derivedArtifactReaderMapFake struct {
	payloads map[string][]byte
	calls    int
}

func (fake *derivedArtifactReaderMapFake) Read(_ context.Context, request DerivedArtifactRead, destination io.Writer) error {
	payload, ok := fake.payloads[request.ArtifactID]
	if !ok {
		return errors.New("unexpected artifact")
	}
	fake.calls++
	_, err := destination.Write(payload)
	return err
}

type derivedPrimaryResolverFake struct {
	payload []byte
	opens   int
}

func (fake *derivedPrimaryResolverFake) OpenContentSource(_ context.Context, request SourceRequest) (SourceSession, error) {
	fake.opens++
	return &attemptSourceSessionFake{
		request: request,
		reader:  &attemptSourceReaderFake{Reader: bytes.NewReader(fake.payload), providerBytes: int64(len(fake.payload))},
	}, nil
}

func (*derivedPrimaryResolverFake) ValidateContentCacheRoot(context.Context, string) error {
	return nil
}

func (fake *derivedArtifactReaderFake) Read(_ context.Context, request DerivedArtifactRead, destination io.Writer) error {
	if request.ArtifactID != fake.artifactID {
		return errors.New("wrong artifact")
	}
	fake.calls++
	_, err := destination.Write(fake.payload)
	return err
}
