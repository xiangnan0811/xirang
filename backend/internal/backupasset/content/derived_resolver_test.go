package content

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	resolver, err := NewDerivedRepresentationResolver(db, reader.Read)
	if err != nil {
		t.Fatal(err)
	}
	request := DerivedRepresentationRequest{
		Ref: binding.ref, CatalogGenerationID: binding.catalogGenerationID,
		SourceFingerprint: binding.sourceFingerprint, SecurityPolicyRevision: binding.policyRevision,
		Provider: backupasset.ProviderRestic, Renderer: RendererEscapedText, Profile: ProfileTextV1,
	}
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
	resolver, err := NewDerivedRepresentationResolver(db, func(context.Context, DerivedArtifactRead, io.Writer) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	base := DerivedRepresentationRequest{
		Ref: fixture.ref, CatalogGenerationID: fixture.catalogGenerationID,
		SourceFingerprint: fixture.sourceFingerprint, SecurityPolicyRevision: fixture.policyRevision,
		Provider: backupasset.ProviderRestic, Renderer: RendererEscapedText, Profile: ProfileTextV1,
	}
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
	resolver, err := NewDerivedRepresentationResolver(db, reader.Read)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := resolver.Resolve(context.Background(), DerivedRepresentationRequest{
		Ref: fixture.ref, CatalogGenerationID: fixture.catalogGenerationID,
		SourceFingerprint: fixture.sourceFingerprint, SecurityPolicyRevision: fixture.policyRevision,
		Provider: backupasset.ProviderRestic, Renderer: RendererEscapedText, Profile: ProfileTextV1,
	})
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

func TestDerivedAttemptResolverStreamsCompleteTextWithoutProviderFallback(t *testing.T) {
	db, fixture := derivedResolverFixture(t)
	payload := []byte("safe derived text")
	reader := &derivedArtifactReaderFake{artifactID: fixture.artifactID, payload: payload}
	derived, err := NewDerivedRepresentationResolver(db, reader.Read)
	if err != nil {
		t.Fatal(err)
	}
	primary := &derivedPrimaryResolverFake{payload: []byte("provider source must not be read")}
	resolver, err := NewDerivedAttemptSourceResolver(
		primary,
		derived,
		fixture.policyRevision,
		func(_ context.Context, ref backupasset.AssetRef, catalogGenerationID, sourceFingerprint string) (backupasset.ProviderKind, error) {
			if ref != fixture.ref || catalogGenerationID != fixture.catalogGenerationID || sourceFingerprint != fixture.sourceFingerprint {
				t.Fatalf("provider binding ref=%+v catalog=%q source=%q", ref, catalogGenerationID, sourceFingerprint)
			}
			return backupasset.ProviderRestic, nil
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

func TestDerivedAttemptResolverFallsBackOnlyWhenNoDerivedIdentityExists(t *testing.T) {
	db, fixture := derivedResolverFixture(t)
	derived, err := NewDerivedRepresentationResolver(db, func(context.Context, DerivedArtifactRead, io.Writer) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	primary := &derivedPrimaryResolverFake{payload: []byte("original provider source")}
	resolver, err := NewDerivedAttemptSourceResolver(
		primary, derived, fixture.policyRevision,
		func(context.Context, backupasset.AssetRef, string, string) (backupasset.ProviderKind, error) {
			return backupasset.ProviderRestic, nil
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
			derived, err := NewDerivedRepresentationResolver(db, func(context.Context, DerivedArtifactRead, io.Writer) error { return nil })
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
				func(context.Context, backupasset.AssetRef, string, string) (backupasset.ProviderKind, error) {
					return backupasset.ProviderRestic, nil
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

type derivedResolverTestBinding struct {
	ref                 backupasset.AssetRef
	catalogGenerationID string
	sourceFingerprint   string
	policyRevision      string
	setID               string
	artifactID          string
	digest              string
}

func derivedResolverFixture(t *testing.T) (*gorm.DB, derivedResolverTestBinding) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&model.BackupAssetDerivedArtifactSet{}, &model.BackupAssetDerivedArtifact{},
		&model.BackupAssetDerivedBlobReference{}, &model.BackupAssetDerivedBlob{},
	); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 20, 8, 0, 0, 0, time.UTC)
	fixture := derivedResolverTestBinding{
		ref:                 backupasset.AssetRef{RecoveryPointID: strings.Repeat("1", 32), EntryID: strings.Repeat("2", 64)},
		catalogGenerationID: strings.Repeat("3", 32), sourceFingerprint: strings.Repeat("4", 64),
		policyRevision: "security-policy-v1", setID: strings.Repeat("5", 32),
		artifactID: strings.Repeat("6", 32), digest: strings.Repeat("7", 64),
	}
	blobID := strings.Repeat("8", 32)
	if err := db.Create(&model.BackupAssetDerivedArtifactSet{
		ID: fixture.setID, JobID: strings.Repeat("9", 32), AttemptID: strings.Repeat("a", 32), WorkKey: strings.Repeat("b", 64),
		RecoveryPointID: fixture.ref.RecoveryPointID, CatalogGenerationID: fixture.catalogGenerationID,
		EntryID: fixture.ref.EntryID, SourceFingerprint: fixture.sourceFingerprint,
		SecurityPolicyRevision: fixture.policyRevision, ManifestDigest: strings.Repeat("c", 64),
		State: "active", Completeness: "complete", ArtifactCount: 1, TotalPlaintextBytes: int64(len("safe derived text")),
		ProjectionRequired: true, ProjectionPublished: true, ProjectionRevision: 1, CreatedAt: now, UpdatedAt: now,
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

type derivedArtifactReaderFake struct {
	artifactID string
	payload    []byte
	calls      int
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
