package repository

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/catalog"
	"xirang/backend/internal/backupasset/content"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

const contentTestLocator = "FAKE_PRIVATE_CONTENT_LOCATOR_FOR_TEST_ONLY"

func TestValidateContentCacheRootRejectsTaskAndManagedSourceOverlap(t *testing.T) {
	db := newRepositoryTestDB(t)
	root := t.TempDir()
	source := filepath.Join(root, "backup-source")
	cache := filepath.Join(root, "asset-content-cache")
	taskEntity := seedTask(t, db, "rsync", filepath.Join(root, "backup-target"), "")
	if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Update("rsync_source", source).Error; err != nil {
		t.Fatal(err)
	}
	service := &Service{db: db}
	for name, candidate := range map[string]string{
		"cache inside source": filepath.Join(source, "cache"),
		"source inside cache": root,
		"relative":            "relative/cache",
		"forbidden root":      "/data/content-cache",
	} {
		t.Run(name, func(t *testing.T) {
			err := service.ValidateContentCacheRoot(context.Background(), candidate)
			if !errors.Is(err, content.ErrCacheUnsafeRoot) {
				t.Fatalf("ValidateContentCacheRoot(%q)=%v, want safe rejection", candidate, err)
			}
			if strings.Contains(fmt.Sprint(err), source) {
				t.Fatalf("cache-root error leaked private source path: %v", err)
			}
		})
	}
	if err := service.ValidateContentCacheRoot(context.Background(), cache); err != nil {
		t.Fatalf("unrelated dedicated cache root rejected: %v", err)
	}
}

type contentSourceProviderSpy struct {
	mu             sync.Mutex
	sourceRevision string
	pointLocator   provider.PointLocator
	entryLocator   provider.EntryLocator
	body           []byte
	stat           provider.ContentStat
	pointCalls     int
	statCalls      int
	sequential     int
	ranges         []provider.ByteRange
	handles        []*contentReadHandleSpy
}

func (spy *contentSourceProviderSpy) ListPoints(_ context.Context, snapshot provider.ReadSnapshot, _ provider.PageRequest) (provider.NativePointPage, error) {
	spy.mu.Lock()
	defer spy.mu.Unlock()
	spy.pointCalls++
	revision := spy.sourceRevision
	if revision == "" {
		revision = snapshot.SourceRevision
	}
	return provider.NativePointPage{Items: []provider.NativePoint{{
		OpaqueDigest: strings.Repeat("8", 64), Semantics: backupasset.PointMutableHead,
		SourceRevision: revision, Locator: spy.pointLocator,
	}}}, nil
}

func (spy *contentSourceProviderSpy) StatEntry(_ context.Context, snapshot provider.ReadSnapshot, point provider.PointLocator, locator provider.EntryLocator) (provider.Entry, error) {
	spy.mu.Lock()
	defer spy.mu.Unlock()
	spy.statCalls++
	if point != spy.pointLocator || locator != spy.entryLocator || snapshot.SourceRevision != spy.sourceRevision {
		return provider.Entry{}, fmt.Errorf("unexpected private source identity")
	}
	return provider.Entry{
		OpaqueDigest: strings.Repeat("7", 64), Type: backupasset.CatalogEntryFile,
		Size: spy.stat.Size, ModTime: spy.stat.ModTime, SourceRevision: spy.stat.SourceRevision,
	}, nil
}

func (spy *contentSourceProviderSpy) OpenSequential(ctx context.Context, snapshot provider.ReadSnapshot, point provider.PointLocator, locator provider.EntryLocator, request provider.ReadRequest) (provider.ReadHandle, provider.ContentStat, error) {
	if err := request.Validate(); err != nil {
		return nil, provider.ContentStat{}, err
	}
	spy.mu.Lock()
	defer spy.mu.Unlock()
	if point != spy.pointLocator || locator != spy.entryLocator || snapshot.SourceRevision != spy.sourceRevision {
		return nil, provider.ContentStat{}, fmt.Errorf("unexpected private source identity")
	}
	spy.sequential++
	handle := &contentReadHandleSpy{ctx: ctx, reader: bytes.NewReader(append([]byte(nil), spy.body...))}
	spy.handles = append(spy.handles, handle)
	return handle, spy.stat, nil
}

func (spy *contentSourceProviderSpy) OpenRange(ctx context.Context, snapshot provider.ReadSnapshot, point provider.PointLocator, locator provider.EntryLocator, byteRange provider.ByteRange) (provider.ReadHandle, provider.ContentStat, error) {
	if err := byteRange.Validate(); err != nil {
		return nil, provider.ContentStat{}, err
	}
	spy.mu.Lock()
	defer spy.mu.Unlock()
	if point != spy.pointLocator || locator != spy.entryLocator || snapshot.SourceRevision != spy.sourceRevision {
		return nil, provider.ContentStat{}, fmt.Errorf("unexpected private source identity")
	}
	spy.ranges = append(spy.ranges, byteRange)
	end := byteRange.Offset + byteRange.Length
	if end > int64(len(spy.body)) {
		return nil, provider.ContentStat{}, fmt.Errorf("range beyond fake body")
	}
	handle := &contentReadHandleSpy{ctx: ctx, reader: bytes.NewReader(append([]byte(nil), spy.body[byteRange.Offset:end]...))}
	spy.handles = append(spy.handles, handle)
	return handle, spy.stat, nil
}

func (spy *contentSourceProviderSpy) setSourceRevision(value string) {
	spy.mu.Lock()
	spy.sourceRevision = value
	spy.stat.SourceRevision = value
	spy.mu.Unlock()
}

func (spy *contentSourceProviderSpy) closedHandles() int {
	spy.mu.Lock()
	defer spy.mu.Unlock()
	count := 0
	for _, handle := range spy.handles {
		count += handle.closeCount()
	}
	return count
}

type contentReadHandleSpy struct {
	ctx    context.Context
	reader *bytes.Reader
	mu     sync.Mutex
	closed int
	read   int64
}

type contentCloseErrorReader struct{ err error }

func (*contentCloseErrorReader) Read([]byte) (int, error) { return 0, io.EOF }
func (reader *contentCloseErrorReader) Close() error      { return reader.err }

type decreasingProviderByteReader struct {
	reports []int64
	next    int
}

func (*decreasingProviderByteReader) Read([]byte) (int, error) { return 0, io.EOF }
func (*decreasingProviderByteReader) Close() error             { return nil }
func (reader *decreasingProviderByteReader) ProviderBytes() int64 {
	if reader.next >= len(reader.reports) {
		return reader.reports[len(reader.reports)-1]
	}
	value := reader.reports[reader.next]
	reader.next++
	return value
}

func (handle *contentReadHandleSpy) Read(buffer []byte) (int, error) {
	select {
	case <-handle.ctx.Done():
		return 0, handle.ctx.Err()
	default:
		count, err := handle.reader.Read(buffer)
		handle.mu.Lock()
		handle.read += int64(count)
		handle.mu.Unlock()
		return count, err
	}
}

func (handle *contentReadHandleSpy) Close() error {
	handle.mu.Lock()
	handle.closed++
	handle.mu.Unlock()
	return nil
}

func (handle *contentReadHandleSpy) closeCount() int {
	handle.mu.Lock()
	defer handle.mu.Unlock()
	return handle.closed
}

func (handle *contentReadHandleSpy) ProviderBytes() int64 {
	handle.mu.Lock()
	defer handle.mu.Unlock()
	return handle.read
}

type mutableContentFixture struct {
	db        *gorm.DB
	service   *Service
	provider  *contentSourceProviderSpy
	admission *publicationAdmission
	request   content.SourceRequest
}

type rejectingContentAdmission struct {
	err   error
	calls int
}

func (admission *rejectingContentAdmission) Acquire(_ context.Context, operation publication.ResticOperation) (publication.AdmissionToken, error) {
	admission.calls++
	if operation != publication.OperationContentRead {
		return nil, fmt.Errorf("unexpected admission operation %q", operation)
	}
	return nil, admission.err
}

func newMutableContentFixture(t *testing.T) mutableContentFixture {
	t.Helper()
	db := newRepositoryTestDB(t)
	taskEntity := seedTask(t, db, "rsync", t.TempDir(), "")
	prober := scopedObservationProber(backupasset.ProviderRsync)
	reader := &contentSourceProviderSpy{
		sourceRevision: strings.Repeat("a", 64),
		pointLocator:   provider.PointLocator{Native: "FAKE_PRIVATE_POINT_LOCATOR_FOR_TEST_ONLY"},
		entryLocator:   provider.EntryLocator{Native: contentTestLocator},
		body:           []byte("0123456789abcdef"),
		stat: provider.ContentStat{
			Size: 16, ModTime: time.Date(2026, 7, 13, 8, 59, 0, 0, time.UTC),
			SourceRevision: strings.Repeat("7", 64), MediaType: "text/plain",
		},
	}
	registry := provider.NewRegistry()
	if err := registry.Register(backupasset.ProviderRsync, provider.Registration{
		Prober: prober, PointLister: reader, EntryStatter: reader,
		SequentialReader: reader, RangeReader: reader,
	}); err != nil {
		t.Fatal(err)
	}
	admission := &publicationAdmission{mode: publication.AdmissionManaged, generation: 12}
	service, err := NewService(Dependencies{
		DB: db, Foundation: enabledFoundation(), Registry: registry, Admission: admission,
		Now: func() time.Time { return time.Date(2026, 7, 13, 9, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	connected, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{})
	if err != nil || connected.MutablePoint == nil {
		t.Fatalf("connect mutable content fixture: result=%+v err=%v", connected, err)
	}
	generation := model.CatalogGeneration{
		ID: strings.Repeat("c", 32), RecoveryPointID: connected.MutablePoint.ID,
		Generation: 1, State: string(catalog.GenerationComplete), IsActive: true,
		SourceFingerprint: reader.sourceRevision, ExpectedEntryCount: 1, WrittenEntryCount: 1,
		StartedAt: service.utcNow(), CreatedAt: service.utcNow(), UpdatedAt: service.utcNow(),
	}
	finished := service.utcNow()
	generation.FinishedAt = &finished
	if err := db.Create(&generation).Error; err != nil {
		t.Fatal(err)
	}
	entry := model.CatalogEntry{
		GenerationID: generation.ID, EntryID: strings.Repeat("b", 64), RecoveryPointID: connected.MutablePoint.ID,
		NormalizedPath: "docs/report.txt", Name: "report.txt", EntryType: string(backupasset.CatalogEntryFile),
		Size: reader.stat.Size, ModifiedAt: &reader.stat.ModTime, MimeType: reader.stat.MediaType,
		Fingerprint: strings.Repeat("e", 64), FingerprintStrength: string(catalog.FingerprintStrong),
		EncryptedProviderLocator: `{"version":1,"native":"` + contentTestLocator + `"}`,
		SecurityState:            "sealed", CreatedAt: service.utcNow(),
	}
	if err := db.Create(&entry).Error; err != nil {
		t.Fatal(err)
	}
	return mutableContentFixture{
		db: db, service: service, provider: reader, admission: admission,
		request: content.SourceRequest{
			Ref:                 backupasset.AssetRef{RecoveryPointID: connected.MutablePoint.ID, EntryID: entry.EntryID},
			CatalogGenerationID: generation.ID, ExpectedSource: generation.SourceFingerprint,
			ExpectedEntry: entry.Fingerprint, Mode: content.SourceModeSequential, MaxBytes: 16,
		},
	}
}

func TestContentAdmissionRejectsBeforeCatalogQueryOrProviderAccess(t *testing.T) {
	fixture := newMutableContentFixture(t)
	rejection := errors.New("FAKE_CONTENT_ADMISSION_REJECTED_FOR_TEST_ONLY")
	admission := &rejectingContentAdmission{err: rejection}
	fixture.service.admission = admission
	queryCalls := 0
	callbackName := "child8:reject-content-query:" + strings.ReplaceAll(t.Name(), "/", "_")
	if err := fixture.db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		queryCalls++
		_ = tx.AddError(errors.New("FAKE_UNEXPECTED_CONTENT_QUERY_FOR_TEST_ONLY"))
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fixture.db.Callback().Query().Remove(callbackName) })
	fixture.provider.mu.Lock()
	providerCallsBefore := fixture.provider.pointCalls + fixture.provider.statCalls + fixture.provider.sequential + len(fixture.provider.ranges)
	fixture.provider.mu.Unlock()

	_, err := fixture.service.OpenContentSource(context.Background(), fixture.request)
	if !errors.Is(err, rejection) {
		t.Fatalf("OpenContentSource error=%v, want admission rejection", err)
	}
	fixture.provider.mu.Lock()
	providerCallsAfter := fixture.provider.pointCalls + fixture.provider.statCalls + fixture.provider.sequential + len(fixture.provider.ranges)
	fixture.provider.mu.Unlock()
	if admission.calls != 1 || queryCalls != 0 || providerCallsAfter != providerCallsBefore {
		t.Fatalf("admission calls=%d queries=%d provider before=%d after=%d", admission.calls, queryCalls, providerCallsBefore, providerCallsAfter)
	}
}

func TestContentSourceExactActiveCompositeReadHidesPrivateLocator(t *testing.T) {
	fixture := newMutableContentFixture(t)
	session, err := fixture.service.OpenContentSource(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("OpenContentSource: %v", err)
	}
	if got := session.Stat(); got.Size != 16 || got.MediaType != "text/plain" ||
		got.SourceFingerprint != fixture.request.ExpectedSource || got.EntryFingerprint != fixture.request.ExpectedEntry {
		t.Fatalf("source stat=%+v", got)
	}
	if got := session.Capabilities(); got.Provider != backupasset.ProviderRsync || !got.Sequential || !got.Range || got.Reason != nil {
		t.Fatalf("source capabilities=%+v", got)
	}
	payload, err := io.ReadAll(session.Reader())
	if err != nil || string(payload) != "0123456789abcdef" {
		t.Fatalf("source payload=%q err=%v", payload, err)
	}
	if got := session.Reader().ProviderBytes(); got != int64(len(payload)) {
		t.Fatalf("source Provider bytes=%d, want %d", got, len(payload))
	}
	encoded, err := json.Marshal(session)
	if err != nil || strings.Contains(string(encoded), contentTestLocator) || string(encoded) != "{}" {
		t.Fatalf("private content source escaped: payload=%s err=%v", encoded, err)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("close content source: %v", err)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("second close content source: %v", err)
	}
	if got := fixture.admission.operations(); len(got) != 1 || got[0] != publication.OperationContentRead {
		t.Fatalf("content admission operations=%v", got)
	}
	if fixture.admission.closedCount() != 1 || fixture.provider.closedHandles() != 1 {
		t.Fatalf("close counts token=%d handle=%d", fixture.admission.closedCount(), fixture.provider.closedHandles())
	}
}

func TestContentSourceRejectsOldGenerationCrossPointAndEntryOnlyLookup(t *testing.T) {
	fixture := newMutableContentFixture(t)
	tests := []struct {
		name   string
		mutate func(*content.SourceRequest)
	}{
		{"old generation", func(request *content.SourceRequest) { request.CatalogGenerationID = strings.Repeat("d", 32) }},
		{"cross point", func(request *content.SourceRequest) { request.Ref.RecoveryPointID = strings.Repeat("f", 32) }},
		{"entry only", func(request *content.SourceRequest) { request.CatalogGenerationID = "" }},
		{"wrong source", func(request *content.SourceRequest) { request.ExpectedSource = strings.Repeat("1", 64) }},
		{"wrong entry", func(request *content.SourceRequest) { request.ExpectedEntry = strings.Repeat("2", 64) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := fixture.request
			test.mutate(&request)
			if session, err := fixture.service.OpenContentSource(context.Background(), request); err == nil || session != nil {
				t.Fatalf("rejected lookup returned session=%v err=%v", session, err)
			}
		})
	}
	operations := fixture.admission.operations()
	if fixture.provider.sequential != 0 || len(operations) != 4 || fixture.admission.closedCount() != 4 {
		t.Fatalf("rejected exact lookups source/admission mismatch: sequential=%d operations=%v closes=%d", fixture.provider.sequential, operations, fixture.admission.closedCount())
	}
	for _, operation := range operations {
		if operation != publication.OperationContentRead {
			t.Fatalf("rejected exact lookup used admission operation %q", operation)
		}
	}
	var point model.RecoveryPoint
	if err := fixture.db.First(&point, "id = ?", fixture.request.Ref.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.BackupRepository{}).Where("id = ?", point.RepositoryID).
		Update("provider_kind", backupasset.ProviderCommand).Error; err != nil {
		t.Fatal(err)
	}
	_, err := fixture.service.OpenContentSource(context.Background(), fixture.request)
	var capabilityErr *CapabilityError
	if !errors.As(err, &capabilityErr) || capabilityErr.Reason.Code != backupasset.CapabilityTaskArtifactContractMissing {
		t.Fatalf("Command content source error=%v", err)
	}
	operations = fixture.admission.operations()
	if len(operations) != 5 || operations[len(operations)-1] != publication.OperationContentRead || fixture.admission.closedCount() != 5 {
		t.Fatalf("Command content admission lifecycle operations=%v closes=%d", operations, fixture.admission.closedCount())
	}
}

func TestMutableSourceDriftFailsCloseAfterClosingReaderAndAdmission(t *testing.T) {
	fixture := newMutableContentFixture(t)
	session, err := fixture.service.OpenContentSource(context.Background(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	fixture.provider.setSourceRevision(strings.Repeat("9", 64))
	if err := session.Revalidate(context.Background()); err == nil {
		t.Fatal("mutable source drift passed explicit revalidation")
	}
	err = session.Close()
	var capabilityErr *CapabilityError
	if !errors.As(err, &capabilityErr) || capabilityErr.Reason.Code != backupasset.CapabilityMutableSourceChanged {
		t.Fatalf("mutable close error=%v", err)
	}
	if fixture.provider.closedHandles() != 1 || fixture.admission.closedCount() != 1 {
		t.Fatalf("drift close counts handle=%d token=%d", fixture.provider.closedHandles(), fixture.admission.closedCount())
	}
}

func TestContentSourceCloseUsesDetachedBoundedCleanupDeadline(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	cancel()
	started := time.Now()
	var observedDeadline time.Time
	session := &sealedContentSourceSession{
		cleanupParent:  parent,
		cleanupTimeout: 5 * time.Second,
		revalidate: func(ctx context.Context) error {
			if err := ctx.Err(); err != nil {
				return fmt.Errorf("cleanup inherited request cancellation: %w", err)
			}
			deadline, ok := ctx.Deadline()
			if !ok {
				return errors.New("cleanup context has no deadline")
			}
			observedDeadline = deadline
			return nil
		},
	}
	if err := session.Close(); err != nil {
		t.Fatalf("close source session: %v", err)
	}
	if observedDeadline.IsZero() || observedDeadline.Before(started.Add(4*time.Second)) || observedDeadline.After(time.Now().Add(5*time.Second)) {
		t.Fatalf("cleanup deadline=%s start=%s", observedDeadline, started)
	}
}

func TestContentSourceCloseKeepsEarlierRequestDeadline(t *testing.T) {
	requestDeadline := time.Now().Add(2 * time.Second)
	parent, cancel := context.WithDeadline(context.Background(), requestDeadline)
	defer cancel()
	var observedDeadline time.Time
	session := &sealedContentSourceSession{
		cleanupParent:  parent,
		cleanupTimeout: 5 * time.Second,
		revalidate: func(ctx context.Context) error {
			var ok bool
			observedDeadline, ok = ctx.Deadline()
			if !ok {
				return errors.New("cleanup context has no deadline")
			}
			return nil
		},
	}
	if err := session.Close(); err != nil {
		t.Fatalf("close source session: %v", err)
	}
	if !observedDeadline.Equal(requestDeadline) {
		t.Fatalf("cleanup deadline=%s want request deadline=%s", observedDeadline, requestDeadline)
	}
}

func TestContentSourceClosePrioritizesSourceDriftOverGenericReaderError(t *testing.T) {
	limitErr := errors.New("FAKE_PROVIDER_LIMIT_ERROR_FOR_TEST_ONLY")
	driftErr := fmt.Errorf("%w: source drift", backupasset.ErrConflict)
	session := &sealedContentSourceSession{
		reader:         newOnceReadCloser(&contentCloseErrorReader{err: limitErr}, false),
		cleanupParent:  context.Background(),
		cleanupTimeout: time.Second,
		revalidate:     func(context.Context) error { return driftErr },
	}
	err := session.Close()
	if !errors.Is(err, driftErr) || !errors.Is(err, limitErr) {
		t.Fatalf("close error lost source or reader evidence: %v", err)
	}
	joined, ok := err.(interface{ Unwrap() []error })
	if !ok || len(joined.Unwrap()) < 2 || !errors.Is(joined.Unwrap()[0], backupasset.ErrConflict) {
		t.Fatalf("source drift is not authoritative close error: %v", err)
	}
}

func TestContentSourceProviderBytesRejectsDecreasingEvidence(t *testing.T) {
	reader := newOnceReadCloser(&decreasingProviderByteReader{reports: []int64{5, 4}}, false)
	if got := reader.ProviderBytes(); got != 5 {
		t.Fatalf("first Provider byte report=%d want 5", got)
	}
	if got := reader.ProviderBytes(); got != -1 {
		t.Fatalf("decreasing Provider byte report=%d want unknown -1", got)
	}
}

func TestContentSourceCancellationReachesReaderAndCloseJoinsExactlyOnce(t *testing.T) {
	fixture := newMutableContentFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	session, err := fixture.service.OpenContentSource(ctx, fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	buffer := make([]byte, 1)
	if _, err := session.Reader().Read(buffer); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled reader error=%v", err)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("close canceled content source: %v", err)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("second close canceled content source: %v", err)
	}
	if fixture.provider.closedHandles() != 1 || fixture.admission.closedCount() != 1 {
		t.Fatalf("canceled lifecycle handle=%d token=%d", fixture.provider.closedHandles(), fixture.admission.closedCount())
	}
}

func TestContentSourceReadsExactImmutableResticPublicationWithoutFakeRange(t *testing.T) {
	fixture := newPublicationFixture(t, true, publication.AdmissionManaged)
	fixture.connectExactResticBinding(t)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	commit := fixture.commitEvidence()
	if _, err := execution.RecordProviderCommit(context.Background(), resticProviderCommit(commit)); err != nil {
		t.Fatal(err)
	}
	fixture.manifest.build = func(_ context.Context, attempt provider.ResticAttemptV1, _ provider.ResticCommitV1, _ provider.ManifestLimits) (provider.ResticManifestV1, error) {
		return provider.ResticManifestV1{
			DigestAlgorithm: "sha256", Digest: strings.Repeat("d", 64), Generator: "xirang-restic-ls", GeneratorVersion: "1",
			Completeness: backupasset.ManifestComplete, EntryCount: 1, LogicalBytes: 16, Fidelity: provider.ResticManifestFidelityV1(),
			HeaderCapturedAt: commit.CaptureStartedAt, ObservedTagDigest: publicationTagDigest(attempt.RequiredTags),
		}, nil
	}
	pointID := resticAttemptForExecution(t, execution).RecoveryPointID
	if outcome, err := fixture.service.ProcessPoint(context.Background(), pointID); err != nil || outcome.State != backupasset.RecoveryPointCommitted {
		t.Fatalf("commit Restic point outcome=%+v err=%v", outcome, err)
	}
	if err := fixture.db.Model(&model.RecoveryPoint{}).Where("id = ?", pointID).Update("state", backupasset.RecoveryPointDegraded).Error; err != nil {
		t.Fatal(err)
	}
	var point model.RecoveryPoint
	if err := fixture.db.First(&point, "id = ?", pointID).Error; err != nil {
		t.Fatal(err)
	}
	reader := &contentSourceProviderSpy{
		sourceRevision: point.SourceFingerprint,
		pointLocator:   provider.PointLocator{Native: commit.NativePointID},
		entryLocator:   provider.EntryLocator{Native: contentTestLocator},
		body:           []byte("immutable-restic"),
		stat: provider.ContentStat{
			Size: 16, ModTime: fixture.now.Add(-time.Minute), SourceRevision: strings.Repeat("7", 64), MediaType: "text/plain",
		},
	}
	registry := provider.NewRegistry()
	if err := registry.Register(backupasset.ProviderRestic, provider.Registration{
		Prober: &scriptedProber{}, EntryStatter: reader, SequentialReader: reader,
	}); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(Dependencies{
		DB: fixture.db, Foundation: fixture.service.foundation, Registry: registry, Keyring: fixture.service.keyring,
		Now: func() time.Time { return fixture.now }, Admission: fixture.admission, Publication: fixture.service,
	})
	if err != nil {
		t.Fatal(err)
	}
	var manifest model.RecoveryPointManifest
	if err := fixture.db.Where("recovery_point_id = ? AND is_active = ?", pointID, true).First(&manifest).Error; err != nil {
		t.Fatal(err)
	}
	generation := model.CatalogGeneration{
		ID: strings.Repeat("c", 32), RecoveryPointID: pointID, ManifestID: &manifest.ID, Generation: 1,
		State: string(catalog.GenerationComplete), IsActive: true, SourceFingerprint: point.SourceFingerprint,
		ExpectedEntryCount: 1, WrittenEntryCount: 1, StartedAt: fixture.now, CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}
	finished := fixture.now
	generation.FinishedAt = &finished
	if err := fixture.db.Create(&generation).Error; err != nil {
		t.Fatal(err)
	}
	modified := reader.stat.ModTime
	entry := model.CatalogEntry{
		GenerationID: generation.ID, EntryID: strings.Repeat("b", 64), RecoveryPointID: pointID,
		NormalizedPath: "report.txt", Name: "report.txt", EntryType: string(backupasset.CatalogEntryFile),
		Size: reader.stat.Size, ModifiedAt: &modified, MimeType: reader.stat.MediaType,
		Fingerprint: strings.Repeat("e", 64), FingerprintStrength: string(catalog.FingerprintStrong),
		EncryptedProviderLocator: `{"version":1,"native":"` + contentTestLocator + `"}`,
		SecurityState:            "sealed", CreatedAt: fixture.now,
	}
	if err := fixture.db.Create(&entry).Error; err != nil {
		t.Fatal(err)
	}
	request := content.SourceRequest{
		Ref: backupasset.AssetRef{RecoveryPointID: pointID, EntryID: entry.EntryID}, CatalogGenerationID: generation.ID,
		ExpectedSource: point.SourceFingerprint, ExpectedEntry: entry.Fingerprint,
		Mode: content.SourceModeSequential, MaxBytes: reader.stat.Size,
	}
	session, err := service.OpenContentSource(context.Background(), request)
	if err != nil {
		t.Fatalf("open immutable Restic content: %v", err)
	}
	payload, err := io.ReadAll(session.Reader())
	if err != nil || string(payload) != "immutable-restic" {
		t.Fatalf("immutable payload=%q err=%v", payload, err)
	}
	capabilities := session.Capabilities()
	if !capabilities.Sequential || capabilities.Range || capabilities.Provider != backupasset.ProviderRestic ||
		capabilities.Reason == nil || capabilities.Reason.Code != backupasset.CapabilityRangeUnavailable {
		t.Fatalf("immutable Restic capabilities=%+v", capabilities)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("close immutable Restic content: %v", err)
	}
	operations := fixture.admission.operations()
	if len(operations) == 0 || operations[len(operations)-1] != publication.OperationContentRead {
		t.Fatalf("immutable Restic admission operations=%v", operations)
	}
	if reader.closedHandles() != 1 || reader.pointLocator.Native != commit.NativePointID {
		t.Fatalf("immutable Restic source lifecycle mismatch: closes=%d point=%q", reader.closedHandles(), reader.pointLocator.Native)
	}
}

type contentManagedRsyncAdapter struct {
	stat    provider.Entry
	body    string
	handles []*contentReadHandleSpy
}

func (adapter *contentManagedRsyncAdapter) ListPoints(context.Context, provider.ReadSnapshot, provider.PageRequest) (provider.NativePointPage, error) {
	return provider.NativePointPage{}, nil
}

func (adapter *contentManagedRsyncAdapter) ListEntries(context.Context, provider.ReadSnapshot, provider.PointLocator, provider.EntryLocator, provider.PageRequest) (provider.EntryPage, error) {
	return provider.EntryPage{}, nil
}

func (adapter *contentManagedRsyncAdapter) StatEntry(context.Context, provider.ReadSnapshot, provider.PointLocator, provider.EntryLocator) (provider.Entry, error) {
	return adapter.stat, nil
}

func (adapter *contentManagedRsyncAdapter) OpenSequential(ctx context.Context, _ provider.ReadSnapshot, _ provider.PointLocator, _ provider.EntryLocator, _ provider.ReadRequest) (provider.ReadHandle, provider.ContentStat, error) {
	handle := &contentReadHandleSpy{ctx: ctx, reader: bytes.NewReader([]byte(adapter.body))}
	adapter.handles = append(adapter.handles, handle)
	return handle, provider.ContentStat{
		Size: adapter.stat.Size, ModTime: adapter.stat.ModTime, SourceRevision: adapter.stat.SourceRevision, MediaType: "text/plain",
	}, nil
}

func (adapter *contentManagedRsyncAdapter) OpenRange(ctx context.Context, _ provider.ReadSnapshot, _ provider.PointLocator, _ provider.EntryLocator, byteRange provider.ByteRange) (provider.ReadHandle, provider.ContentStat, error) {
	end := byteRange.Offset + byteRange.Length
	if byteRange.Validate() != nil || end > int64(len(adapter.body)) {
		return nil, provider.ContentStat{}, fmt.Errorf("invalid fake managed Rsync range")
	}
	handle := &contentReadHandleSpy{ctx: ctx, reader: bytes.NewReader([]byte(adapter.body[byteRange.Offset:end]))}
	adapter.handles = append(adapter.handles, handle)
	return handle, provider.ContentStat{
		Size: adapter.stat.Size, ModTime: adapter.stat.ModTime, SourceRevision: adapter.stat.SourceRevision, MediaType: "text/plain",
	}, nil
}

func TestContentSourceUsesExactManagedRsyncSessionAndContentAdmission(t *testing.T) {
	fixture := newRsyncPublicationFixture(t)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	state := execution.(*rsyncPublicationExecution)
	commit := provider.RsyncTreeCommitV1{
		LayoutVersion: 1, RepositoryID: state.attempt.RepositoryID, TaskRepositoryLinkID: state.attempt.TaskRepositoryLinkID,
		RecoveryPointID: state.attempt.RecoveryPointID, AttemptID: state.attempt.AttemptID, PublicationMode: state.attempt.PublicationMode,
		ManifestDigestAlgorithm: "sha256", ManifestDigest: strings.Repeat("1", 64), ManifestEntryCount: 1, LogicalBytes: 13,
		FidelityDigest: strings.Repeat("2", 64), SourceFingerprint: managedRsyncSourceFingerprint(state.markerKey, fixture.binding, state.attempt.RecoveryPointID),
		ProviderCommittedAt: fixture.now, CommitMarkerDigest: strings.Repeat("3", 64), ChildFenceDigest: rsyncChildFenceDigest(state.markerKey, state.childFence),
		PointDeadlineAt: state.attempt.PointDeadlineAt, RenameVerified: true, DirectoryFsyncVerified: true,
	}
	if _, err := execution.RecordProviderCommit(context.Background(), provider.NewRsyncTreeProviderCommit(commit)); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.RecoveryPoint{}).Where("id = ?", state.attempt.RecoveryPointID).Updates(map[string]any{
		"state": string(backupasset.RecoveryPointCommitted), "committed_at": fixture.now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	manifest := model.RecoveryPointManifest{
		ID: strings.Repeat("8", 32), RecoveryPointID: state.attempt.RecoveryPointID, Revision: 1,
		DigestAlgorithm: "sha256", Digest: commit.ManifestDigest, Generator: "xirang-rsync", GeneratorVersion: "1",
		Completeness: string(backupasset.ManifestComplete), EntryCount: int64(commit.ManifestEntryCount), LogicalBytes: int64(commit.LogicalBytes),
		FidelityJSON: "{}", EncryptedCommitEvidence: "{}", IsActive: true, CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}
	if err := fixture.db.Create(&manifest).Error; err != nil {
		t.Fatal(err)
	}
	generation := model.CatalogGeneration{
		ID: strings.Repeat("c", 32), RecoveryPointID: state.attempt.RecoveryPointID, ManifestID: &manifest.ID,
		Generation: 1, State: string(catalog.GenerationComplete), IsActive: true, SourceFingerprint: commit.SourceFingerprint,
		ExpectedEntryCount: 1, WrittenEntryCount: 1, StartedAt: fixture.now, CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}
	finished := fixture.now
	generation.FinishedAt = &finished
	if err := fixture.db.Create(&generation).Error; err != nil {
		t.Fatal(err)
	}
	modified := fixture.now.Add(-time.Minute)
	entry := model.CatalogEntry{
		GenerationID: generation.ID, EntryID: strings.Repeat("b", 64), RecoveryPointID: state.attempt.RecoveryPointID,
		NormalizedPath: "managed.txt", Name: "managed.txt", EntryType: string(backupasset.CatalogEntryFile),
		Size: 13, ModifiedAt: &modified, MimeType: "text/plain", Fingerprint: strings.Repeat("e", 64),
		FingerprintStrength: string(catalog.FingerprintStrong), EncryptedProviderLocator: `{"version":1,"native":"managed.txt"}`,
		SecurityState: "sealed", CreatedAt: fixture.now,
	}
	if err := fixture.db.Create(&entry).Error; err != nil {
		t.Fatal(err)
	}
	service, err := NewService(Dependencies{
		DB: fixture.db, Foundation: fixture.service.foundation, Registry: fixture.service.registry, Keyring: fixture.service.keyring,
		Now: func() time.Time { return fixture.now }, Admission: fixture.admission, Publication: fixture.service,
	})
	if err != nil {
		t.Fatal(err)
	}
	adapter := &contentManagedRsyncAdapter{
		stat: provider.Entry{Type: backupasset.CatalogEntryFile, Size: entry.Size, ModTime: modified, SourceRevision: strings.Repeat("7", 64)},
		body: "managed-rsync",
	}
	token := &publicationAdmissionToken{mode: publication.AdmissionManaged, generation: 13, operation: publication.OperationContentRead}
	managedSession := &ManagedRsyncPointReadSession{
		adapter: adapter,
		snapshot: provider.ReadSnapshot{
			RepositoryID: fixture.repository.ID, CapabilityRevision: 1, SourceRevision: commit.SourceFingerprint,
		},
		point: provider.PointLocator{Native: "FAKE_MANAGED_RSYNC_POINT_FOR_TEST_ONLY"}, token: token,
	}
	request := content.SourceRequest{
		Ref: backupasset.AssetRef{RecoveryPointID: state.attempt.RecoveryPointID, EntryID: entry.EntryID}, CatalogGenerationID: generation.ID,
		ExpectedSource: commit.SourceFingerprint, ExpectedEntry: entry.Fingerprint,
		Mode: content.SourceModeSequential, MaxBytes: entry.Size,
	}
	record, err := service.loadExactContentRecord(context.Background(), request)
	if err != nil {
		t.Fatalf("load exact managed Rsync content record: %v", err)
	}
	session, err := service.openManagedRsyncContentSession(context.Background(), request, record, managedSession)
	if err != nil {
		t.Fatalf("open managed Rsync content: %v", err)
	}
	payload, err := io.ReadAll(session.Reader())
	if err != nil || string(payload) != adapter.body {
		t.Fatalf("managed Rsync payload=%q err=%v", payload, err)
	}
	if capabilities := session.Capabilities(); !capabilities.Sequential || !capabilities.Range || capabilities.Provider != backupasset.ProviderRsync || capabilities.Reason != nil {
		t.Fatalf("managed Rsync capabilities=%+v", capabilities)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("close managed Rsync content: %v", err)
	}
	if token.closed.Load() != 1 || len(adapter.handles) != 1 || adapter.handles[0].closeCount() != 1 {
		t.Fatalf("managed Rsync lifecycle token=%d handles=%d", token.closed.Load(), len(adapter.handles))
	}
}

func TestContentSourceReconstructsManagedRclonePortableRuntime(t *testing.T) {
	fixture := newRclonePublicationFixture(t, backupasset.PublicationVersionedPrefix)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := execution.Attempt().RcloneAttempt()
	if err != nil {
		t.Fatal(err)
	}
	input, err := execution.(interface {
		RclonePublicationInput() (provider.RclonePublicationInput, error)
	}).RclonePublicationInput()
	if err != nil {
		t.Fatal(err)
	}
	commit := validRcloneRepositoryCommit(attempt, input.PortableRequest.CostEvidenceDigest, fixture.now.Add(time.Minute))
	fixture.strategy.reconcile = provider.RcloneReconcileV1{
		State: provider.RcloneReconcileProviderCommitted, Commit: &commit,
		Manifest: &provider.RcloneManifestV1{
			ManifestIndexDigest: commit.ManifestIndexDigest, ManifestChunkDigests: append([]string(nil), commit.ManifestChunkDigests...),
			EntryCount: commit.ManifestEntryCount, LogicalBytes: commit.LogicalBytes, FidelityEvidenceDigest: commit.FidelityEvidenceDigest,
		},
	}
	if err := execution.Abandon(backupasset.ErrPublicationSessionAbandoned); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.RecoveryPointLease{}).
		Where("recovery_point_id = ? AND status = ?", attempt.RecoveryPointID, backupasset.LeaseActive).
		Updates(map[string]any{"status": backupasset.LeaseExpired, "lease_expires_at": fixture.now.Add(-time.Second)}).Error; err != nil {
		t.Fatal(err)
	}
	if outcome, err := fixture.service.ProcessPoint(context.Background(), attempt.RecoveryPointID); err != nil || outcome.State != backupasset.RecoveryPointVerifying {
		t.Fatalf("Rclone preparing outcome=%+v err=%v", outcome, err)
	}
	if outcome, err := fixture.service.ProcessPoint(context.Background(), attempt.RecoveryPointID); err != nil || outcome.State != backupasset.RecoveryPointCommitted {
		t.Fatalf("Rclone verifying outcome=%+v err=%v", outcome, err)
	}
	var point model.RecoveryPoint
	if err := fixture.db.First(&point, "id = ?", attempt.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	var manifest model.RecoveryPointManifest
	if err := fixture.db.Where("recovery_point_id = ? AND is_active = ?", point.ID, true).First(&manifest).Error; err != nil {
		t.Fatal(err)
	}
	generation := model.CatalogGeneration{
		ID: strings.Repeat("c", 32), RecoveryPointID: point.ID, ManifestID: &manifest.ID,
		Generation: 1, State: string(catalog.GenerationComplete), IsActive: true, SourceFingerprint: point.SourceFingerprint,
		ExpectedEntryCount: 1, WrittenEntryCount: 1, StartedAt: fixture.now, CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}
	finished := fixture.now
	generation.FinishedAt = &finished
	if err := fixture.db.Create(&generation).Error; err != nil {
		t.Fatal(err)
	}
	modified := fixture.now.Add(-time.Minute)
	entry := model.CatalogEntry{
		GenerationID: generation.ID, EntryID: strings.Repeat("b", 64), RecoveryPointID: point.ID,
		NormalizedPath: "docs/portable.txt", Name: "portable.txt", EntryType: string(backupasset.CatalogEntryFile),
		Size: 15, ModifiedAt: &modified, MimeType: "text/plain", Fingerprint: strings.Repeat("e", 64),
		FingerprintStrength:      string(catalog.FingerprintStrong),
		EncryptedProviderLocator: `{"version":1,"native":"portable:docs/portable.txt"}`,
		SecurityState:            "sealed", CreatedAt: fixture.now,
	}
	if err := fixture.db.Create(&entry).Error; err != nil {
		t.Fatal(err)
	}
	reader := &contentSourceProviderSpy{
		sourceRevision: commit.DestinationObservationDigest,
		pointLocator: provider.PointLocator{
			Native: "managed:" + point.ID + ":" + attempt.AttemptID + ":" + point.ManifestDigest,
		},
		entryLocator: provider.EntryLocator{Native: "docs/portable.txt"},
		body:         []byte("rclone-portable"),
		stat: provider.ContentStat{
			Size: entry.Size, ModTime: modified, SourceRevision: strings.Repeat("7", 64), MediaType: "text/plain",
		},
	}
	registry := provider.NewRegistry()
	if err := registry.Register(backupasset.ProviderRclone, provider.Registration{
		Prober: &scriptedProber{}, EntryStatter: reader, SequentialReader: reader, RangeReader: reader,
	}); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(Dependencies{
		DB: fixture.db, Foundation: fixture.service.foundation, Registry: registry, Keyring: fixture.service.keyring,
		Now: func() time.Time { return fixture.now }, Admission: fixture.service.admission, Publication: fixture.service,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := content.SourceRequest{
		Ref: backupasset.AssetRef{RecoveryPointID: point.ID, EntryID: entry.EntryID}, CatalogGenerationID: generation.ID,
		ExpectedSource: point.SourceFingerprint, ExpectedEntry: entry.Fingerprint,
		Mode: content.SourceModeSequential, MaxBytes: entry.Size,
	}
	session, err := service.OpenContentSource(context.Background(), request)
	if err != nil {
		t.Fatalf("open portable Rclone content: %v", err)
	}
	payload, err := io.ReadAll(session.Reader())
	if err != nil || string(payload) != "rclone-portable" {
		t.Fatalf("portable Rclone payload=%q err=%v", payload, err)
	}
	if capabilities := session.Capabilities(); !capabilities.Sequential || !capabilities.Range || capabilities.Provider != backupasset.ProviderRclone || capabilities.Reason != nil {
		t.Fatalf("portable Rclone capabilities=%+v", capabilities)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("close portable Rclone content: %v", err)
	}
	operations := fixture.service.admission.(*publicationAdmission).operations()
	if len(operations) == 0 || operations[len(operations)-1] != publication.OperationContentRead || reader.closedHandles() != 1 {
		t.Fatalf("portable Rclone lifecycle operations=%v closes=%d", operations, reader.closedHandles())
	}
}

type contentNativeExactReaderFake struct {
	payload       []byte
	readRequests  []provider.RcloneNativeExactReadRequest
	rangeRequests []provider.RcloneNativeExactRangeRequest
	handles       []*contentReadHandleSpy
}

func (fake *contentNativeExactReaderFake) OpenVersion(ctx context.Context, request provider.RcloneNativeExactReadRequest) (io.ReadCloser, error) {
	fake.readRequests = append(fake.readRequests, request)
	handle := &contentReadHandleSpy{ctx: ctx, reader: bytes.NewReader(append([]byte(nil), fake.payload...))}
	fake.handles = append(fake.handles, handle)
	return handle, nil
}

func (fake *contentNativeExactReaderFake) OpenVersionRange(ctx context.Context, request provider.RcloneNativeExactRangeRequest) (io.ReadCloser, error) {
	fake.rangeRequests = append(fake.rangeRequests, request)
	end := request.Offset + request.Length
	if end > uint64(len(fake.payload)) {
		return nil, errors.New("FAKE_NATIVE_RANGE_OUT_OF_BOUNDS_FOR_TEST_ONLY")
	}
	handle := &contentReadHandleSpy{
		ctx:    ctx,
		reader: bytes.NewReader(append([]byte(nil), fake.payload[request.Offset:end]...)),
	}
	fake.handles = append(fake.handles, handle)
	return handle, nil
}

func TestContentSourceUsesRcloneNativeExactVersionReader(t *testing.T) {
	physicalKey := "FAKE_MANAGED_PREFIX_FOR_TEST_ONLY/data/native.txt"
	versionID := "FAKE_OPAQUE_VERSION_ID_FOR_TEST_ONLY"
	reader := &contentNativeExactReaderFake{payload: []byte("exact")}

	sequential, err := openRcloneNativeContentReader(context.Background(), reader, content.SourceRequest{
		Mode: content.SourceModeSequential, MaxBytes: 5,
	}, physicalKey, versionID)
	if err != nil {
		t.Fatalf("open native Rclone sequential reader: %v", err)
	}
	payload, err := io.ReadAll(sequential)
	if err != nil || string(payload) != "exact" {
		t.Fatalf("native Rclone payload=%q err=%v", payload, err)
	}
	if err := sequential.Close(); err != nil {
		t.Fatalf("close native Rclone sequential reader: %v", err)
	}

	ranged, err := openRcloneNativeContentReader(context.Background(), reader, content.SourceRequest{
		Mode: content.SourceModeRange, MaxBytes: 3,
		Range: &content.ResolvedRange{Offset: 1, Length: 3},
	}, physicalKey, versionID)
	if err != nil {
		t.Fatalf("open native Rclone Range reader: %v", err)
	}
	rangePayload, err := io.ReadAll(ranged)
	if err != nil || string(rangePayload) != "xac" {
		t.Fatalf("native Rclone Range payload=%q err=%v", rangePayload, err)
	}
	if err := ranged.Close(); err != nil {
		t.Fatalf("close native Rclone Range reader: %v", err)
	}

	if len(reader.readRequests) != 1 || reader.readRequests[0].PhysicalKey != physicalKey ||
		reader.readRequests[0].VersionID != versionID || len(reader.rangeRequests) != 1 ||
		reader.rangeRequests[0].PhysicalKey != physicalKey || reader.rangeRequests[0].VersionID != versionID ||
		reader.rangeRequests[0].Offset != 1 || reader.rangeRequests[0].Length != 3 ||
		len(reader.handles) != 2 || reader.handles[0].closeCount() != 1 || reader.handles[1].closeCount() != 1 {
		t.Fatalf("native exact lifecycle reads=%+v ranges=%+v handles=%d", reader.readRequests, reader.rangeRequests, len(reader.handles))
	}
}
