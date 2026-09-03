package repository

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"runtime"
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

type scopedContentSourceProvider struct {
	byRepository map[string]*contentSourceProviderSpy
	ready        chan string
	release      chan struct{}
}

func (sources *scopedContentSourceProvider) sourceFor(snapshot provider.ReadSnapshot) (*contentSourceProviderSpy, error) {
	source := sources.byRepository[snapshot.RepositoryID]
	if source == nil {
		return nil, fmt.Errorf("unexpected content repository %q", snapshot.RepositoryID)
	}
	return source, nil
}

func (sources *scopedContentSourceProvider) ListPoints(ctx context.Context, snapshot provider.ReadSnapshot, request provider.PageRequest) (provider.NativePointPage, error) {
	source, err := sources.sourceFor(snapshot)
	if err != nil {
		return provider.NativePointPage{}, err
	}
	if sources.ready != nil && sources.release != nil {
		sources.ready <- snapshot.RepositoryID
		<-sources.release
	}
	return source.ListPoints(ctx, snapshot, request)
}

func (sources *scopedContentSourceProvider) StatEntry(ctx context.Context, snapshot provider.ReadSnapshot, point provider.PointLocator, locator provider.EntryLocator) (provider.Entry, error) {
	source, err := sources.sourceFor(snapshot)
	if err != nil {
		return provider.Entry{}, err
	}
	return source.StatEntry(ctx, snapshot, point, locator)
}

func (sources *scopedContentSourceProvider) OpenSequential(ctx context.Context, snapshot provider.ReadSnapshot, point provider.PointLocator, locator provider.EntryLocator, request provider.ReadRequest) (provider.ReadHandle, provider.ContentStat, error) {
	source, err := sources.sourceFor(snapshot)
	if err != nil {
		return nil, provider.ContentStat{}, err
	}
	return source.OpenSequential(ctx, snapshot, point, locator, request)
}

func (sources *scopedContentSourceProvider) OpenRange(ctx context.Context, snapshot provider.ReadSnapshot, point provider.PointLocator, locator provider.EntryLocator, byteRange provider.ByteRange) (provider.ReadHandle, provider.ContentStat, error) {
	source, err := sources.sourceFor(snapshot)
	if err != nil {
		return nil, provider.ContentStat{}, err
	}
	return source.OpenRange(ctx, snapshot, point, locator, byteRange)
}

type taskScopedContentProber struct {
	sources map[uint]string
}

func (prober *taskScopedContentProber) Probe(_ context.Context, binding provider.AccessBinding, _ provider.OperationLimits) (provider.RepositoryObservation, error) {
	sourceRevision, ok := prober.sources[binding.TaskID]
	if !ok {
		return provider.RepositoryObservation{}, fmt.Errorf("unexpected content Task %d", binding.TaskID)
	}
	facts := append([]string(nil), binding.EndpointFacts...)
	identity, err := provider.DeriveScopedIdentity(binding.IdentitySalt, provider.ScopedIdentityDocument{
		Provider: binding.Provider, TaskID: binding.TaskID, NodeID: binding.NodeID, EndpointFacts: facts,
	})
	if err != nil {
		return provider.RepositoryObservation{}, err
	}
	observation := testObservation(binding.Provider, identity)
	observation.SourceRevision = sourceRevision
	return observation, nil
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
	ctx          context.Context
	reader       *bytes.Reader
	mu           sync.Mutex
	closed       int
	prefixClosed int
	read         int64
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

func (handle *contentReadHandleSpy) ClosePrefix() error {
	handle.mu.Lock()
	handle.prefixClosed++
	handle.mu.Unlock()
	return nil
}

func (handle *contentReadHandleSpy) closeCount() int {
	handle.mu.Lock()
	defer handle.mu.Unlock()
	return handle.closed
}

func (handle *contentReadHandleSpy) prefixCloseCount() int {
	handle.mu.Lock()
	defer handle.mu.Unlock()
	return handle.prefixClosed
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
	prober    *scriptedProber
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
		db: db, service: service, provider: reader, prober: prober, admission: admission,
		request: content.SourceRequest{
			Ref:                 backupasset.AssetRef{RecoveryPointID: connected.MutablePoint.ID, EntryID: entry.EntryID},
			CatalogGenerationID: generation.ID, ExpectedSource: generation.SourceFingerprint,
			ExpectedEntry: entry.Fingerprint, Mode: content.SourceModeSequential, MaxBytes: 16,
		},
	}
}

func seedMutableContentCatalog(
	t *testing.T,
	db *gorm.DB,
	service *Service,
	connected ConnectResult,
	generationID, entryID, entryFingerprint string,
	source *contentSourceProviderSpy,
) content.SourceRequest {
	t.Helper()
	if connected.MutablePoint == nil {
		t.Fatal("mutable content catalog requires a mutable point")
	}
	now := service.utcNow()
	generation := model.CatalogGeneration{
		ID: strings.Repeat(generationID, 32), RecoveryPointID: connected.MutablePoint.ID,
		Generation: 1, State: string(catalog.GenerationComplete), IsActive: true,
		SourceFingerprint: source.sourceRevision, ExpectedEntryCount: 1, WrittenEntryCount: 1,
		StartedAt: now, FinishedAt: &now, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&generation).Error; err != nil {
		t.Fatal(err)
	}
	locatorPayload, err := json.Marshal(contentEntryLocatorV1{Version: 1, Native: source.entryLocator.Native})
	if err != nil {
		t.Fatal(err)
	}
	modifiedAt := source.stat.ModTime
	entry := model.CatalogEntry{
		GenerationID: generation.ID, EntryID: strings.Repeat(entryID, 64), RecoveryPointID: connected.MutablePoint.ID,
		NormalizedPath: "docs/" + entryID + ".txt", Name: entryID + ".txt", EntryType: string(backupasset.CatalogEntryFile),
		Size: source.stat.Size, ModifiedAt: &modifiedAt, MimeType: source.stat.MediaType,
		Fingerprint: strings.Repeat(entryFingerprint, 64), FingerprintStrength: string(catalog.FingerprintStrong),
		EncryptedProviderLocator: string(locatorPayload), SecurityState: "sealed", CreatedAt: now,
	}
	if err := db.Create(&entry).Error; err != nil {
		t.Fatal(err)
	}
	return content.SourceRequest{
		Ref:                 backupasset.AssetRef{RecoveryPointID: connected.MutablePoint.ID, EntryID: entry.EntryID},
		CatalogGenerationID: generation.ID, ExpectedSource: source.sourceRevision,
		ExpectedEntry: entry.Fingerprint, Mode: content.SourceModeSequential, MaxBytes: int64(len(source.body)),
	}
}

func TestMutableContentSourcesRemainTaskAndNodeScopedConcurrently(t *testing.T) {
	db := newRepositoryTestDB(t)
	target := t.TempDir()
	firstTask := seedTask(t, db, "rsync", target, "")
	secondTask := seedTask(t, db, "rsync", target, "")
	firstSource := strings.Repeat("a", 64)
	secondSource := strings.Repeat("b", 64)
	firstReader := &contentSourceProviderSpy{
		sourceRevision: firstSource,
		pointLocator:   provider.PointLocator{Native: "FAKE_FIRST_PRIVATE_POINT_LOCATOR_FOR_TEST_ONLY"},
		entryLocator:   provider.EntryLocator{Native: "FAKE_FIRST_PRIVATE_ENTRY_LOCATOR_FOR_TEST_ONLY"},
		body:           []byte("first-task-content"),
		stat: provider.ContentStat{
			Size: int64(len("first-task-content")), ModTime: time.Date(2026, 7, 13, 8, 58, 0, 0, time.UTC),
			SourceRevision: firstSource, MediaType: "text/plain",
		},
	}
	secondReader := &contentSourceProviderSpy{
		sourceRevision: secondSource,
		pointLocator:   provider.PointLocator{Native: "FAKE_SECOND_PRIVATE_POINT_LOCATOR_FOR_TEST_ONLY"},
		entryLocator:   provider.EntryLocator{Native: "FAKE_SECOND_PRIVATE_ENTRY_LOCATOR_FOR_TEST_ONLY"},
		body:           []byte("second-task-content"),
		stat: provider.ContentStat{
			Size: int64(len("second-task-content")), ModTime: time.Date(2026, 7, 13, 8, 57, 0, 0, time.UTC),
			SourceRevision: secondSource, MediaType: "text/plain",
		},
	}
	prober := &taskScopedContentProber{sources: map[uint]string{
		firstTask.ID: firstSource, secondTask.ID: secondSource,
	}}
	sourceProvider := &scopedContentSourceProvider{
		byRepository: map[string]*contentSourceProviderSpy{},
		ready:        make(chan string, 16),
		release:      make(chan struct{}),
	}
	registry := provider.NewRegistry()
	if err := registry.Register(backupasset.ProviderRsync, provider.Registration{
		Prober: prober, PointLister: sourceProvider, EntryStatter: sourceProvider,
		SequentialReader: sourceProvider, RangeReader: sourceProvider,
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
	firstConnected, err := service.Connect(context.Background(), ConnectRequest{TaskID: firstTask.ID}, RequestContext{})
	if err != nil || firstConnected.MutablePoint == nil {
		t.Fatalf("first connect=%+v err=%v", firstConnected, err)
	}
	secondConnected, err := service.Connect(context.Background(), ConnectRequest{TaskID: secondTask.ID}, RequestContext{})
	if err != nil || secondConnected.MutablePoint == nil {
		t.Fatalf("second connect=%+v err=%v", secondConnected, err)
	}
	if firstConnected.Repository.ID == secondConnected.Repository.ID ||
		firstConnected.MutablePoint.ID == secondConnected.MutablePoint.ID {
		t.Fatalf("mutable task source identities merged: first=%+v second=%+v", firstConnected, secondConnected)
	}
	sourceProvider.byRepository[firstConnected.Repository.ID] = firstReader
	sourceProvider.byRepository[secondConnected.Repository.ID] = secondReader
	firstRequest := seedMutableContentCatalog(t, db, service, firstConnected, "c", "d", "1", firstReader)
	secondRequest := seedMutableContentCatalog(t, db, service, secondConnected, "e", "f", "2", secondReader)

	type openOutcome struct {
		taskID  uint
		payload string
		err     error
	}
	outcomes := make(chan openOutcome, 2)
	open := func(taskID uint, request content.SourceRequest) {
		session, openErr := service.OpenContentSource(context.Background(), request)
		if openErr == nil && session == nil {
			openErr = errors.New("mutable content source returned nil session")
		}
		var payload []byte
		if openErr == nil {
			payload, openErr = io.ReadAll(session.Reader())
			openErr = errors.Join(openErr, session.Close())
		}
		outcomes <- openOutcome{taskID: taskID, payload: string(payload), err: openErr}
	}
	go open(firstTask.ID, firstRequest)
	go open(secondTask.ID, secondRequest)
	seenRepositories := map[string]bool{}
	for range 2 {
		select {
		case repositoryID := <-sourceProvider.ready:
			seenRepositories[repositoryID] = true
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for concurrent mutable source opens")
		}
	}
	if len(seenRepositories) != 2 || !seenRepositories[firstConnected.Repository.ID] || !seenRepositories[secondConnected.Repository.ID] {
		t.Fatalf("mutable source opens were not repository-scoped: repositories=%v", seenRepositories)
	}
	close(sourceProvider.release)
	opened := map[uint]string{}
	for range 2 {
		select {
		case outcome := <-outcomes:
			if outcome.err != nil {
				t.Fatalf("mutable source open task=%d err=%v", outcome.taskID, outcome.err)
			}
			opened[outcome.taskID] = outcome.payload
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for mutable source payload")
		}
	}
	if opened[firstTask.ID] != string(firstReader.body) || opened[secondTask.ID] != string(secondReader.body) {
		t.Fatalf("mutable source payloads=%v want first=%q second=%q", opened, firstReader.body, secondReader.body)
	}
	sourceProvider.ready = nil

	repairedSource := strings.Repeat("9", 64)
	firstReader.setSourceRevision(repairedSource)
	prober.sources[firstTask.ID] = repairedSource
	wake := &catalogWakeRequesterSpy{accept: true}
	if err := service.SetCatalogWake(wake); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Reconcile(context.Background(), firstConnected.Repository.ID, RequestContext{}); err != nil {
		t.Fatal(err)
	}
	if calls := wake.calls.Load(); calls != 1 {
		t.Fatalf("manual mutable source repair wakes=%d want=1", calls)
	}
	if _, err := service.OpenContentSource(context.Background(), firstRequest); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("stale first mutable source request err=%v, want conflict", err)
	}
	secondSession, err := service.OpenContentSource(context.Background(), secondRequest)
	if err != nil {
		t.Fatalf("second mutable source after first repair: %v", err)
	}
	payload, readErr := io.ReadAll(secondSession.Reader())
	closeErr := secondSession.Close()
	if readErr != nil || closeErr != nil || string(payload) != string(secondReader.body) {
		t.Fatalf("second source after first repair payload=%q read=%v close=%v", payload, readErr, closeErr)
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

func TestContentSourceExplicitPrefixClosePropagatesWithoutOrdinaryClose(t *testing.T) {
	fixture := newMutableContentFixture(t)
	session, err := fixture.service.OpenContentSource(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("OpenContentSource: %v", err)
	}
	prefix := make([]byte, 4)
	if _, err := io.ReadFull(session.Reader(), prefix); err != nil {
		t.Fatalf("read prefix: %v", err)
	}
	prefixCloser, ok := session.(interface{ ClosePrefix() error })
	if !ok {
		t.Fatal("Repository content source session does not expose intentional prefix close")
	}
	if err := prefixCloser.ClosePrefix(); err != nil {
		t.Fatalf("close content source prefix: %v", err)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("second ordinary close content source: %v", err)
	}
	if len(fixture.provider.handles) != 1 || fixture.provider.handles[0].prefixCloseCount() != 1 ||
		fixture.provider.handles[0].closeCount() != 0 || fixture.admission.closedCount() != 1 {
		t.Fatalf("prefix closes=%d ordinary closes=%d admission closes=%d",
			fixture.provider.handles[0].prefixCloseCount(), fixture.provider.handles[0].closeCount(), fixture.admission.closedCount())
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

func TestMutablePreviewSourceRepairRequiresManualReconcile(t *testing.T) {
	fixture := newMutableContentFixture(t)
	session, err := fixture.service.OpenContentSource(context.Background(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	repairedSource := strings.Repeat("9", 64)
	fixture.provider.setSourceRevision(repairedSource)
	if err := session.Revalidate(context.Background()); err == nil {
		t.Fatal("mutable preview source drift passed revalidation")
	}
	var capabilityErr *CapabilityError
	if err := session.Close(); !errors.As(err, &capabilityErr) || capabilityErr.Reason.Code != backupasset.CapabilityMutableSourceChanged {
		t.Fatalf("mutable preview close error=%v", err)
	}

	wake := &catalogWakeRequesterSpy{accept: true}
	if err := fixture.service.SetCatalogWake(wake); err != nil {
		t.Fatal(err)
	}
	baseProbe := fixture.prober.probe
	fixture.prober.probe = func(binding provider.AccessBinding) (provider.RepositoryObservation, error) {
		observation, err := baseProbe(binding)
		observation.SourceRevision = repairedSource
		return observation, err
	}
	var beforePoint model.RecoveryPoint
	if err := fixture.db.First(&beforePoint, "id = ?", fixture.request.Ref.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.Reconcile(context.Background(), beforePoint.RepositoryID, RequestContext{}); err != nil {
		t.Fatal(err)
	}
	if calls := wake.calls.Load(); calls != 1 {
		t.Fatalf("manual Reconcile Catalog wake calls=%d want=1", calls)
	}

	var point model.RecoveryPoint
	if err := fixture.db.First(&point, "id = ?", fixture.request.Ref.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	if point.SourceFingerprint != repairedSource || point.State != string(backupasset.RecoveryPointObserved) {
		t.Fatalf("repaired mutable point=%+v", point)
	}
	if _, err := fixture.service.OpenContentSource(context.Background(), fixture.request); err == nil {
		t.Fatal("stale preview request served after source repair")
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
	prober := &scriptedProber{observation: testObservation(backupasset.ProviderRestic, *fixture.repository.RepositoryIdentity)}
	if err := registry.Register(backupasset.ProviderRestic, provider.Registration{
		Prober: prober, EntryStatter: reader, SequentialReader: reader,
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
	changedLocator := "sftp:changed@example.invalid:/repository"
	changedPassword := "FAKE_CHANGED_RESTIC_PASSWORD_FOR_CONTENT_TEST_ONLY"
	if err := fixture.db.Model(&model.Task{}).Where("id = ?", fixture.task.ID).Updates(map[string]any{
		"rsync_target":    changedLocator,
		"executor_config": fmt.Sprintf(`{"repository_password":%q}`, changedPassword),
	}).Error; err != nil {
		t.Fatal(err)
	}
	wrongIdentity := provider.NativeResticIdentityPrefix + strings.Repeat("b", 64)
	prober.probe = func(binding provider.AccessBinding) (provider.RepositoryObservation, error) {
		if binding.TaskID != fixture.task.ID || binding.NodeID != fixture.node.ID ||
			binding.Locator != changedLocator || string(binding.Secret) != changedPassword {
			t.Fatalf("Restic content Probe access=%+v, want current producing Task access", binding)
		}
		return testObservation(backupasset.ProviderRestic, wrongIdentity), nil
	}
	reader.mu.Lock()
	beforeStatCalls, beforeSequential := reader.statCalls, reader.sequential
	reader.mu.Unlock()
	if _, err := service.OpenContentSource(context.Background(), request); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("wrong-identity Restic content error=%v, want conflict", err)
	}
	reader.mu.Lock()
	afterStatCalls, afterSequential := reader.statCalls, reader.sequential
	reader.mu.Unlock()
	if afterStatCalls != beforeStatCalls || afterSequential != beforeSequential {
		t.Fatalf("wrong-identity Restic content reached provider: stat %d->%d sequential %d->%d", beforeStatCalls, afterStatCalls, beforeSequential, afterSequential)
	}
	if prober.calls != 3 {
		t.Fatalf("Restic content Probe calls=%d, want open, close revalidation, and rejected read", prober.calls)
	}
	raceCallbackName := "repository:restic-capability-revision-race:" + strings.ReplaceAll(t.Name(), "/", "_")
	raceCallbackFired := false
	capturedOuterCapabilityRevision := 0
	if err := fixture.db.Callback().Query().After("gorm:after_query").Register(raceCallbackName, func(tx *gorm.DB) {
		if raceCallbackFired || tx.Statement == nil || tx.Statement.Table != (model.BackupRepository{}).TableName() {
			return
		}
		repository, ok := tx.Statement.Dest.(*model.BackupRepository)
		if !ok || repository == nil || repository.ID != fixture.repository.ID {
			return
		}
		capturedOuterCapabilityRevision = repository.CapabilityRevision
		raceCallbackFired = true
		if err := tx.Session(&gorm.Session{NewDB: true}).Model(&model.BackupRepository{}).
			Where("id = ?", fixture.repository.ID).
			Update("capability_revision", fixture.repository.CapabilityRevision+1).Error; err != nil {
			if addedErr := tx.AddError(err); !errors.Is(addedErr, err) {
				t.Fatalf("capability-revision-race AddError=%v, want %v", addedErr, err)
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := fixture.db.Callback().Query().Remove(raceCallbackName); err != nil {
			t.Errorf("remove Restic capability revision race callback: %v", err)
		}
	})
	reader.mu.Lock()
	beforeRacePointCalls, beforeRaceStatCalls, beforeRaceSequential := reader.pointCalls, reader.statCalls, reader.sequential
	beforeRaceRanges := len(reader.ranges)
	reader.mu.Unlock()
	beforeRaceProbes := prober.calls
	if _, err := service.OpenContentSource(context.Background(), request); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("capability-revision-race Restic content error=%v, want conflict", err)
	}
	if !raceCallbackFired {
		t.Fatal("capability-revision-race callback did not fire")
	}
	if capturedOuterCapabilityRevision != fixture.repository.CapabilityRevision {
		t.Fatalf("capability-revision-race Restic content outer CapabilityRevision=%d, want unchanged %d", capturedOuterCapabilityRevision, fixture.repository.CapabilityRevision)
	}
	if prober.calls != beforeRaceProbes {
		t.Fatalf("capability-revision-race Restic content Probe calls=%d, want unchanged at %d", prober.calls, beforeRaceProbes)
	}
	reader.mu.Lock()
	afterRacePointCalls, afterRaceStatCalls, afterRaceSequential := reader.pointCalls, reader.statCalls, reader.sequential
	afterRaceRanges := len(reader.ranges)
	reader.mu.Unlock()
	if afterRacePointCalls != beforeRacePointCalls || afterRaceStatCalls != beforeRaceStatCalls ||
		afterRaceSequential != beforeRaceSequential || afterRaceRanges != beforeRaceRanges {
		t.Fatalf("capability-revision-race Restic content reached provider: points %d->%d stats %d->%d sequential %d->%d ranges %d->%d",
			beforeRacePointCalls, afterRacePointCalls, beforeRaceStatCalls, afterRaceStatCalls,
			beforeRaceSequential, afterRaceSequential, beforeRaceRanges, afterRaceRanges)
	}
}

type resticLineageContentRequest struct {
	sourceRevision string
	taskID         uint
	nodeID         uint
	secret         string
}

type resticLineageContentProvider struct {
	mu       sync.Mutex
	sources  map[string]*contentSourceProviderSpy
	requests []resticLineageContentRequest
}

func (reader *resticLineageContentProvider) sourceFor(snapshot provider.ReadSnapshot) (*contentSourceProviderSpy, error) {
	reader.mu.Lock()
	reader.requests = append(reader.requests, resticLineageContentRequest{
		sourceRevision: snapshot.SourceRevision,
		taskID:         snapshot.Access.TaskID,
		nodeID:         snapshot.Access.NodeID,
		secret:         string(snapshot.Access.Secret),
	})
	reader.mu.Unlock()
	source := reader.sources[snapshot.SourceRevision]
	if source == nil {
		return nil, fmt.Errorf("unexpected Restic content source %q", snapshot.SourceRevision)
	}
	return source, nil
}

func (reader *resticLineageContentProvider) StatEntry(
	ctx context.Context,
	snapshot provider.ReadSnapshot,
	point provider.PointLocator,
	locator provider.EntryLocator,
) (provider.Entry, error) {
	source, err := reader.sourceFor(snapshot)
	if err != nil {
		return provider.Entry{}, err
	}
	return source.StatEntry(ctx, snapshot, point, locator)
}

func (reader *resticLineageContentProvider) OpenSequential(
	ctx context.Context,
	snapshot provider.ReadSnapshot,
	point provider.PointLocator,
	locator provider.EntryLocator,
	request provider.ReadRequest,
) (provider.ReadHandle, provider.ContentStat, error) {
	source, err := reader.sourceFor(snapshot)
	if err != nil {
		return nil, provider.ContentStat{}, err
	}
	return source.OpenSequential(ctx, snapshot, point, locator, request)
}

func (reader *resticLineageContentProvider) requestSnapshot() []resticLineageContentRequest {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	return append([]resticLineageContentRequest(nil), reader.requests...)
}

func TestContentSourceSharedResticRepositoryUsesImmutablePublicationLineage(t *testing.T) {
	fixture := newPublicationFixture(t, true, publication.AdmissionManaged)
	fixture.connectExactResticBinding(t)
	connectService, err := NewService(Dependencies{
		DB: fixture.db, Foundation: fixture.service.foundation, Registry: fixture.service.registry,
		Keyring: fixture.service.keyring, Now: func() time.Time { return fixture.now },
		Admission: fixture.admission, History: fixture.service.history, Metrics: publication.NoopMetrics{},
		Publication: fixture.service,
	})
	if err != nil {
		t.Fatal(err)
	}
	firstPassword := "FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY"
	secondPassword := "FAKE_SECOND_RESTIC_PASSWORD_FOR_CONTENT_TEST_ONLY"
	if firstPassword == secondPassword {
		t.Fatal("shared Restic lineage fixture needs separate Task credentials")
	}

	// Refresh the first Task through the real connect path so the persisted
	// capability revision matches the one later used by the second Task.
	if _, err := connectService.Connect(context.Background(), ConnectRequest{
		TaskID: fixture.task.ID, RepositoryID: fixture.repository.ID, ReplaceAccess: true,
	}, RequestContext{}); err != nil {
		t.Fatalf("refresh first Restic Task: %v", err)
	}

	firstBody := "first-restic-content"
	secondBody := "second-restic-content"
	firstNativePointID := strings.Repeat("c", 64)
	secondNativePointID := strings.Repeat("d", 64)
	fixture.manifest.build = func(
		_ context.Context,
		attempt provider.ResticAttemptV1,
		commit provider.ResticCommitV1,
		_ provider.ManifestLimits,
	) (provider.ResticManifestV1, error) {
		digest := strings.Repeat("e", 64)
		if commit.NativePointID == secondNativePointID {
			digest = strings.Repeat("f", 64)
		}
		return provider.ResticManifestV1{
			DigestAlgorithm: "sha256", Digest: digest, Generator: "xirang-restic-ls", GeneratorVersion: "1",
			Completeness: backupasset.ManifestComplete, EntryCount: 1, LogicalBytes: int64(commit.LogicalBytes),
			Fidelity: provider.ResticManifestFidelityV1(), HeaderCapturedAt: commit.CaptureStartedAt,
			ObservedTagDigest: publicationTagDigest(attempt.RequiredTags),
		}, nil
	}

	firstExecution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatalf("prepare first Restic Task: %v", err)
	}
	firstAttempt := resticAttemptForExecution(t, firstExecution)
	firstCommit := fixture.commitEvidence()
	firstCommit.NativePointID = firstNativePointID
	firstCommit.FilesProcessed = 1
	firstCommit.LogicalBytes = uint64(len(firstBody))
	if _, err := firstExecution.RecordProviderCommit(context.Background(), resticProviderCommit(firstCommit)); err != nil {
		t.Fatalf("commit first Restic point: %v", err)
	}
	if outcome, err := fixture.service.ProcessPoint(context.Background(), firstAttempt.RecoveryPointID); err != nil || outcome.State != backupasset.RecoveryPointCommitted {
		t.Fatalf("first Restic point outcome=%+v err=%v", outcome, err)
	}

	secondTask := seedTask(t, fixture.db, "restic", "sftp:second@example.invalid:/repository", fmt.Sprintf(`{"repository_password":%q}`, secondPassword))
	var secondNode model.Node
	if err := fixture.db.First(&secondNode, secondTask.NodeID).Error; err != nil {
		t.Fatal(err)
	}
	secondTask.Node = secondNode
	secondRun := model.TaskRun{
		TaskID: secondTask.ID, TriggerType: "manual", Status: "running",
		StartedAt: timePointer(fixture.now.Add(-2 * time.Minute)), CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}
	if err := fixture.db.Create(&secondRun).Error; err != nil {
		t.Fatal(err)
	}
	if fixture.node.ID == secondNode.ID {
		t.Fatal("shared Restic lineage fixture needs different Nodes")
	}

	// Replacing access for the second Task intentionally makes its binding the
	// repository's active binding. The first immutable point must still rebuild
	// access from its own producing Task and Node below.
	if _, err := connectService.Connect(context.Background(), ConnectRequest{
		TaskID: secondTask.ID, RepositoryID: fixture.repository.ID, ReplaceAccess: true,
	}, RequestContext{}); err != nil {
		t.Fatalf("connect second Restic Task: %v", err)
	}

	secondExecution, err := fixture.service.Prepare(context.Background(), publication.Run{
		Task: secondTask, TaskRunID: secondRun.ID, Trigger: secondRun.TriggerType, StartedAt: *secondRun.StartedAt,
		Audit: backupasset.PublicationAuditContext{
			Actor:         backupasset.AuditActor{UserID: 9, Username: "operator", Role: "operator"},
			CorrelationID: "publication-shared-content-2",
		},
	})
	if err != nil {
		t.Fatalf("prepare second Restic Task: %v", err)
	}
	secondAttempt := resticAttemptForExecution(t, secondExecution)
	secondCommit := fixture.commitEvidence()
	secondCommit.NativePointID = secondNativePointID
	secondCommit.FilesProcessed = 1
	secondCommit.LogicalBytes = uint64(len(secondBody))
	if _, err := secondExecution.RecordProviderCommit(context.Background(), resticProviderCommit(secondCommit)); err != nil {
		t.Fatalf("commit second Restic point: %v", err)
	}
	if outcome, err := fixture.service.ProcessPoint(context.Background(), secondAttempt.RecoveryPointID); err != nil || outcome.State != backupasset.RecoveryPointCommitted {
		t.Fatalf("second Restic point outcome=%+v err=%v", outcome, err)
	}

	var firstPoint, secondPoint model.RecoveryPoint
	if err := fixture.db.First(&firstPoint, "id = ?", firstAttempt.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.First(&secondPoint, "id = ?", secondAttempt.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	if firstPoint.SourceFingerprint == "" || secondPoint.SourceFingerprint == "" || firstPoint.SourceFingerprint == secondPoint.SourceFingerprint {
		t.Fatalf("shared Restic points did not retain distinct immutable source fingerprints: first=%q second=%q", firstPoint.SourceFingerprint, secondPoint.SourceFingerprint)
	}

	firstSource := &contentSourceProviderSpy{
		sourceRevision: firstPoint.SourceFingerprint,
		pointLocator:   provider.PointLocator{Native: firstNativePointID},
		entryLocator:   provider.EntryLocator{Native: contentTestLocator},
		body:           []byte(firstBody),
		stat: provider.ContentStat{
			Size: int64(len(firstBody)), ModTime: fixture.now.Add(-time.Minute),
			SourceRevision: firstPoint.SourceFingerprint, MediaType: "text/plain",
		},
	}
	secondSource := &contentSourceProviderSpy{
		sourceRevision: secondPoint.SourceFingerprint,
		pointLocator:   provider.PointLocator{Native: secondNativePointID},
		entryLocator:   provider.EntryLocator{Native: contentTestLocator},
		body:           []byte(secondBody),
		stat: provider.ContentStat{
			Size: int64(len(secondBody)), ModTime: fixture.now.Add(-2 * time.Minute),
			SourceRevision: secondPoint.SourceFingerprint, MediaType: "text/plain",
		},
	}
	firstRequest := seedResticContentCatalog(t, fixture.db, fixture.now, firstPoint.ID, strings.Repeat("4", 32), strings.Repeat("6", 64), strings.Repeat("8", 64), "reports/first.txt", "first.txt", firstSource)
	secondRequest := seedResticContentCatalog(t, fixture.db, fixture.now, secondPoint.ID, strings.Repeat("5", 32), strings.Repeat("7", 64), strings.Repeat("9", 64), "reports/second.txt", "second.txt", secondSource)

	contentReader := &resticLineageContentProvider{sources: map[string]*contentSourceProviderSpy{
		firstPoint.SourceFingerprint: firstSource, secondPoint.SourceFingerprint: secondSource,
	}}
	registry := provider.NewRegistry()
	prober := &scriptedProber{observation: testObservation(backupasset.ProviderRestic, *fixture.repository.RepositoryIdentity)}
	if err := registry.Register(backupasset.ProviderRestic, provider.Registration{
		Prober: prober, EntryStatter: contentReader, SequentialReader: contentReader,
	}); err != nil {
		t.Fatal(err)
	}
	contentService, err := NewService(Dependencies{
		DB: fixture.db, Foundation: fixture.service.foundation, Registry: registry, Keyring: fixture.service.keyring,
		Now: func() time.Time { return fixture.now }, Admission: fixture.admission, Publication: fixture.service,
	})
	if err != nil {
		t.Fatal(err)
	}

	readContent := func(request content.SourceRequest) string {
		session, err := contentService.OpenContentSource(context.Background(), request)
		if err != nil {
			t.Fatalf("open Restic content: %v", err)
		}
		payload, err := io.ReadAll(session.Reader())
		if err != nil {
			_ = session.Close()
			t.Fatalf("read Restic content: %v", err)
		}
		if err := session.Close(); err != nil {
			t.Fatalf("close Restic content: %v", err)
		}
		return string(payload)
	}
	if got := readContent(firstRequest); got != firstBody {
		t.Fatalf("first Restic content=%q want=%q", got, firstBody)
	}
	if got := readContent(secondRequest); got != secondBody {
		t.Fatalf("second Restic content=%q want=%q", got, secondBody)
	}

	requests := contentReader.requestSnapshot()
	if len(requests) != 6 {
		t.Fatalf("Restic provider request count=%d want=6 (%+v)", len(requests), requests)
	}
	type expectedLineage struct {
		taskID uint
		nodeID uint
		secret string
	}
	expected := map[string]expectedLineage{
		firstPoint.SourceFingerprint:  {taskID: fixture.task.ID, nodeID: fixture.node.ID, secret: firstPassword},
		secondPoint.SourceFingerprint: {taskID: secondTask.ID, nodeID: secondNode.ID, secret: secondPassword},
	}
	seen := make(map[string]int, len(expected))
	for _, request := range requests {
		want, ok := expected[request.sourceRevision]
		if !ok {
			t.Fatalf("provider request used unknown Restic source revision %q", request.sourceRevision)
		}
		if request.taskID != want.taskID || request.nodeID != want.nodeID || request.secret != want.secret {
			t.Fatalf("provider request for source %q used task=%d node=%d secret=%q, want task=%d node=%d secret=%q", request.sourceRevision, request.taskID, request.nodeID, request.secret, want.taskID, want.nodeID, want.secret)
		}
		seen[request.sourceRevision]++
	}
	for sourceRevision := range expected {
		if seen[sourceRevision] != 3 {
			t.Fatalf("provider requests for source %q=%d want=3", sourceRevision, seen[sourceRevision])
		}
	}
}

func seedResticContentCatalog(
	t *testing.T,
	db *gorm.DB,
	now time.Time,
	pointID, generationID, entryID, entryFingerprint, normalizedPath, name string,
	source *contentSourceProviderSpy,
) content.SourceRequest {
	t.Helper()
	var point model.RecoveryPoint
	if err := db.First(&point, "id = ?", pointID).Error; err != nil {
		t.Fatal(err)
	}
	var manifest model.RecoveryPointManifest
	if err := db.Where("recovery_point_id = ? AND is_active = ?", pointID, true).First(&manifest).Error; err != nil {
		t.Fatal(err)
	}
	finished := now
	generation := model.CatalogGeneration{
		ID: generationID, RecoveryPointID: pointID, ManifestID: &manifest.ID, Generation: 1,
		State: string(catalog.GenerationComplete), IsActive: true, SourceFingerprint: point.SourceFingerprint,
		ExpectedEntryCount: 1, WrittenEntryCount: 1, ExpectedDigest: manifest.Digest, WrittenDigest: manifest.Digest,
		StartedAt: now, FinishedAt: &finished, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&generation).Error; err != nil {
		t.Fatal(err)
	}
	locatorPayload, err := json.Marshal(contentEntryLocatorV1{Version: 1, Native: source.entryLocator.Native})
	if err != nil {
		t.Fatal(err)
	}
	modifiedAt := source.stat.ModTime
	entry := model.CatalogEntry{
		GenerationID: generation.ID, EntryID: entryID, RecoveryPointID: pointID,
		NormalizedPath: normalizedPath, Name: name, EntryType: string(backupasset.CatalogEntryFile),
		Size: source.stat.Size, ModifiedAt: &modifiedAt, MimeType: source.stat.MediaType,
		Fingerprint: entryFingerprint, FingerprintStrength: string(catalog.FingerprintStrong),
		EncryptedProviderLocator: string(locatorPayload), SecurityState: "sealed", CreatedAt: now,
	}
	if err := db.Create(&entry).Error; err != nil {
		t.Fatal(err)
	}
	return content.SourceRequest{
		Ref: backupasset.AssetRef{RecoveryPointID: pointID, EntryID: entryID}, CatalogGenerationID: generation.ID,
		ExpectedSource: point.SourceFingerprint, ExpectedEntry: entry.Fingerprint,
		Mode: content.SourceModeSequential, MaxBytes: source.stat.Size,
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

func TestContentSourceRejectsManagedRsyncCapabilityRevisionRace(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("strict managed Rsync content reads are Linux-only")
	}
	fixture := newManagedRsyncCatalogFixture(t)
	if _, err := fixture.keyring.Ensure(context.Background(), backupasset.KeyDomainEntryIdentity); err != nil {
		t.Fatal(err)
	}
	indexer, err := catalog.NewIndexer(catalog.IndexerDependencies{
		DB: fixture.db, Factory: fixture.factory, Lease: fixture.lease, IdentityKeys: fixture.keyring,
		Now: func() time.Time { return fixture.now },
		Config: catalog.IndexerConfig{
			BatchSize: 100, BuildTimeout: time.Minute, MaxEntries: 100, HeartbeatInterval: time.Second,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	generation, err := indexer.Build(context.Background(), catalog.BuildRequest{
		RepositoryID: fixture.repository.ID, RecoveryPointID: fixture.point.ID,
	})
	if err != nil {
		t.Fatalf("build real managed-Rsync Catalog: %v", err)
	}
	if generation.State != string(catalog.GenerationComplete) || !generation.IsActive {
		t.Fatalf("Catalog generation=%+v, want active complete", generation)
	}
	var entry model.CatalogEntry
	if err := fixture.db.Where("generation_id = ?", generation.ID).First(&entry).Error; err != nil {
		t.Fatal(err)
	}
	request := content.SourceRequest{
		Ref:                 backupasset.AssetRef{RecoveryPointID: fixture.point.ID, EntryID: entry.EntryID},
		CatalogGenerationID: generation.ID, ExpectedSource: fixture.point.SourceFingerprint,
		ExpectedEntry: entry.Fingerprint, Mode: content.SourceModeSequential, MaxBytes: entry.Size,
	}

	baseService, ok := fixture.factory.(*Service)
	if !ok || baseService == nil {
		t.Fatalf("managed Rsync content fixture factory=%T, want repository Service", fixture.factory)
	}
	admission := &catalogAdmissionSpy{mode: publication.AdmissionManaged}
	service, err := NewService(Dependencies{
		DB: fixture.db, Foundation: baseService.foundation, Registry: baseService.registry, Keyring: fixture.keyring,
		Now: func() time.Time { return fixture.now }, Admission: admission, Publication: baseService.publication,
	})
	if err != nil {
		t.Fatal(err)
	}

	raceCallbackName := "repository:managed-rsync-content-capability-revision-race:" + strings.ReplaceAll(t.Name(), "/", "_")
	raceCallbackFired := false
	capturedOuterCapabilityRevision := 0
	if err := fixture.db.Callback().Query().After("gorm:after_query").Register(raceCallbackName, func(tx *gorm.DB) {
		if raceCallbackFired || tx.Statement == nil || tx.Statement.Table != (model.BackupRepository{}).TableName() {
			return
		}
		repository, ok := tx.Statement.Dest.(*model.BackupRepository)
		if !ok || repository == nil || repository.ID != fixture.repository.ID {
			return
		}
		oldRevision := repository.CapabilityRevision
		capturedOuterCapabilityRevision = oldRevision
		raceCallbackFired = true
		if err := tx.Session(&gorm.Session{NewDB: true}).Model(&model.BackupRepository{}).
			Where("id = ?", repository.ID).
			Update("capability_revision", oldRevision+1).Error; err != nil {
			if addedErr := tx.AddError(err); !errors.Is(addedErr, err) {
				t.Fatalf("capability-revision-race AddError=%v, want %v", addedErr, err)
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := fixture.db.Callback().Query().Remove(raceCallbackName); err != nil {
			t.Errorf("remove managed Rsync content capability revision race callback: %v", err)
		}
	})

	session, err := service.OpenContentSource(context.Background(), request)
	if session != nil {
		_ = session.Close()
		t.Fatalf("capability-revision-race managed Rsync content session=%v, want nil", session)
	}
	if !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("capability-revision-race managed Rsync content error=%v, want ErrConflict", err)
	}
	if !raceCallbackFired {
		t.Fatal("capability-revision-race managed Rsync content callback did not fire")
	}
	if capturedOuterCapabilityRevision != fixture.repository.CapabilityRevision {
		t.Fatalf("capability-revision-race managed Rsync content outer CapabilityRevision=%d, want unchanged %d", capturedOuterCapabilityRevision, fixture.repository.CapabilityRevision)
	}
	var persisted model.BackupRepository
	if err := fixture.db.First(&persisted, "id = ?", fixture.repository.ID).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.CapabilityRevision != capturedOuterCapabilityRevision+1 {
		t.Fatalf("capability-revision-race managed Rsync content persisted CapabilityRevision=%d, want %d", persisted.CapabilityRevision, capturedOuterCapabilityRevision+1)
	}
	token := admission.latestToken()
	operations := admission.requestedOperations()
	if token == nil || token.Operation() != publication.OperationContentRead ||
		token.closed.Load() != 1 || admission.closeCount() != 1 ||
		len(operations) != 1 || operations[0] != publication.OperationContentRead {
		t.Fatalf("capability-revision-race managed Rsync content token=%+v operations=%v closes=%d, want one closed content-read token", token, operations, admission.closeCount())
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
	var changedPoint model.RecoveryPoint
	if err := fixture.db.First(&changedPoint, "id = ?", point.ID).Error; err != nil {
		t.Fatal(err)
	}
	consistency, err := backupasset.DecodePublicationConsistency(changedPoint.ConsistencyJSON)
	if err != nil {
		t.Fatal(err)
	}
	consistency.CapabilityRevision++
	encodedConsistency, err := backupasset.EncodePublicationConsistency(consistency)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.RecoveryPoint{}).Where("id = ?", point.ID).
		Update("consistency_json", encodedConsistency).Error; err != nil {
		t.Fatal(err)
	}
	reader.mu.Lock()
	beforeStatCalls, beforeSequential, beforeRanges := reader.statCalls, reader.sequential, len(reader.ranges)
	reader.mu.Unlock()
	rejected, err := service.OpenContentSource(context.Background(), request)
	if rejected != nil || !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("portable Rclone capability mismatch session=%v err=%v, want nil and ErrConflict", rejected, err)
	}
	reader.mu.Lock()
	afterStatCalls, afterSequential, afterRanges := reader.statCalls, reader.sequential, len(reader.ranges)
	reader.mu.Unlock()
	if afterStatCalls != beforeStatCalls || afterSequential != beforeSequential || afterRanges != beforeRanges {
		t.Fatalf("portable Rclone capability mismatch reached provider: stat %d->%d sequential %d->%d ranges %d->%d",
			beforeStatCalls, afterStatCalls, beforeSequential, afterSequential, beforeRanges, afterRanges)
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

type contentNativeS3Fake struct {
	*rcloneNativeRepositoryFactoryFake
	head                provider.RcloneNativeExactObjectHead
	expectedPhysicalKey string
	expectedVersionID   string
	headErr             error
	reader              *contentNativeExactReaderFake
	headCalls           int
}

func (fake *contentNativeS3Fake) HeadVersion(_ context.Context, request provider.RcloneNativeExactReadRequest) (provider.RcloneNativeExactObjectHead, error) {
	fake.headCalls++
	if fake.headErr != nil {
		return provider.RcloneNativeExactObjectHead{}, fake.headErr
	}
	expectedPhysicalKey, expectedVersionID := fake.head.PhysicalKey, fake.head.VersionID
	if fake.expectedPhysicalKey != "" {
		expectedPhysicalKey = fake.expectedPhysicalKey
	}
	if fake.expectedVersionID != "" {
		expectedVersionID = fake.expectedVersionID
	}
	if request.PhysicalKey != expectedPhysicalKey || request.VersionID != expectedVersionID {
		return provider.RcloneNativeExactObjectHead{}, errors.New("FAKE_NATIVE_HEAD_REQUEST_CHANGED_FOR_TEST_ONLY")
	}
	return fake.head, nil
}

func (fake *contentNativeS3Fake) OpenVersion(ctx context.Context, request provider.RcloneNativeExactReadRequest) (io.ReadCloser, error) {
	return fake.reader.OpenVersion(ctx, request)
}

func (fake *contentNativeS3Fake) OpenVersionRange(ctx context.Context, request provider.RcloneNativeExactRangeRequest) (io.ReadCloser, error) {
	return fake.reader.OpenVersionRange(ctx, request)
}

type contentNativeFactoryFake struct {
	*rcloneNativeRepositoryFactoryFake
	s3 provider.S3Native
}

func (fake *contentNativeFactoryFake) S3(provider.RcloneNativeSession, provider.RcloneNativeProfile, []provider.RcloneNativeKMSKeyDigestBinding) (provider.S3Native, error) {
	return fake.s3, nil
}

func TestContentSourceNativeRcloneCloseRevalidatesCurrentRuntime(t *testing.T) {
	fixture := newRclonePublicationFixture(t, backupasset.PublicationNativeObjectVersions)
	_, point, manifest := completeRcloneTestPoint(t, fixture)
	physicalKey := strings.TrimSuffix(fixture.binding.Native.ManagedPrefix, "/") + "/data/native.txt"
	versionID := "FAKE_NATIVE_CONTENT_VERSION_FOR_TEST_ONLY"
	exactReader := &contentNativeExactReaderFake{payload: []byte("exact")}
	s3 := &contentNativeS3Fake{
		rcloneNativeRepositoryFactoryFake: fixture.nativeFactory,
		head: provider.RcloneNativeExactObjectHead{
			PhysicalKey: physicalKey, VersionID: versionID, Size: 5,
			EncryptionProfile: provider.RcloneNativeSSES3V1,
		},
		reader: exactReader,
	}
	factory := &contentNativeFactoryFake{
		rcloneNativeRepositoryFactoryFake: fixture.nativeFactory,
		s3:                                s3,
	}
	fixture.service.rcloneNativeFactoryBuilder = func(context.Context, provider.RcloneNativeBootstrap, string, int) (RcloneNativeFactory, error) {
		return factory, nil
	}
	generation := model.CatalogGeneration{
		ID: strings.Repeat("c", 32), RecoveryPointID: point.ID, ManifestID: &manifest.ID,
		Generation: 1, State: string(catalog.GenerationComplete), IsActive: true,
		SourceFingerprint: point.SourceFingerprint, ExpectedEntryCount: 1, WrittenEntryCount: 1,
		StartedAt: fixture.now, CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}
	finished := fixture.now
	generation.FinishedAt = &finished
	if err := fixture.db.Create(&generation).Error; err != nil {
		t.Fatal(err)
	}
	entryLocator, err := json.Marshal(contentEntryLocatorV1{
		Version: 1, Native: "native:" + physicalKey + "\x00" + versionID,
	})
	if err != nil {
		t.Fatal(err)
	}
	modified := fixture.now.Add(-time.Minute)
	entry := model.CatalogEntry{
		GenerationID: generation.ID, EntryID: strings.Repeat("b", 64), RecoveryPointID: point.ID,
		NormalizedPath: "docs/native.txt", Name: "native.txt", EntryType: string(backupasset.CatalogEntryFile),
		Size: 5, ModifiedAt: &modified, MimeType: "text/plain", Fingerprint: strings.Repeat("e", 64),
		FingerprintStrength: string(catalog.FingerprintStrong), EncryptedProviderLocator: string(entryLocator),
		SecurityState: "sealed", CreatedAt: fixture.now,
	}
	if err := fixture.db.Create(&entry).Error; err != nil {
		t.Fatal(err)
	}
	service, err := NewService(Dependencies{
		DB: fixture.db, Foundation: fixture.service.foundation, Registry: fixture.service.registry,
		Keyring: fixture.service.keyring, Now: func() time.Time { return fixture.now },
		Admission: fixture.service.admission, Publication: fixture.service,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := content.SourceRequest{
		Ref:                 backupasset.AssetRef{RecoveryPointID: point.ID, EntryID: entry.EntryID},
		CatalogGenerationID: generation.ID, ExpectedSource: point.SourceFingerprint,
		ExpectedEntry: entry.Fingerprint, Mode: content.SourceModeSequential, MaxBytes: entry.Size,
	}
	session, err := service.OpenContentSource(context.Background(), request)
	if err != nil {
		t.Fatalf("open native Rclone content: %v", err)
	}
	payload, err := io.ReadAll(session.Reader())
	if err != nil || string(payload) != "exact" {
		t.Fatalf("native Rclone content payload=%q err=%v", payload, err)
	}
	var access model.RepositoryAccessBinding
	if err := fixture.db.Where("repository_id = ? AND status = ?", point.RepositoryID, bindingStatusActive).First(&access).Error; err != nil {
		t.Fatal(err)
	}
	currentBinding := fixture.binding
	currentBinding.CredentialRevision++
	encodedBinding, err := encodeManagedRcloneBindingDocumentV3(currentBinding)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.RepositoryAccessBinding{}).Where("id = ?", access.ID).
		Update("encrypted_config", encodedBinding).Error; err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("native Rclone close after credential revision drift error=%v, want ErrConflict", err)
	}
	if s3.headCalls != 1 {
		t.Fatalf("native Rclone close after runtime drift head calls=%d, want initial proof only", s3.headCalls)
	}
}
func newNativeRcloneContentCloseTest(t *testing.T) (*Service, *contentNativeS3Fake, content.SourceRequest, *contentNativeExactReaderFake, *publicationAdmission) {
	t.Helper()
	fixture := newRclonePublicationFixture(t, backupasset.PublicationNativeObjectVersions)
	_, point, manifest := completeRcloneTestPoint(t, fixture)
	physicalKey := strings.TrimSuffix(fixture.binding.Native.ManagedPrefix, "/") + "/data/native.txt"
	versionID := "FAKE_NATIVE_CONTENT_VERSION_FOR_TEST_ONLY"
	exactReader := &contentNativeExactReaderFake{payload: []byte("exact")}
	s3 := &contentNativeS3Fake{
		rcloneNativeRepositoryFactoryFake: fixture.nativeFactory,
		head: provider.RcloneNativeExactObjectHead{
			PhysicalKey: physicalKey, VersionID: versionID, Size: 5,
			EncryptionProfile: provider.RcloneNativeSSES3V1,
		},
		expectedPhysicalKey: physicalKey, expectedVersionID: versionID,
		reader: exactReader,
	}
	factory := &contentNativeFactoryFake{
		rcloneNativeRepositoryFactoryFake: fixture.nativeFactory,
		s3:                                s3,
	}
	fixture.service.rcloneNativeFactoryBuilder = func(context.Context, provider.RcloneNativeBootstrap, string, int) (RcloneNativeFactory, error) {
		return factory, nil
	}
	generation := model.CatalogGeneration{
		ID: strings.Repeat("c", 32), RecoveryPointID: point.ID, ManifestID: &manifest.ID,
		Generation: 1, State: string(catalog.GenerationComplete), IsActive: true,
		SourceFingerprint: point.SourceFingerprint, ExpectedEntryCount: 1, WrittenEntryCount: 1,
		StartedAt: fixture.now, CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}
	finished := fixture.now
	generation.FinishedAt = &finished
	if err := fixture.db.Create(&generation).Error; err != nil {
		t.Fatal(err)
	}
	entryLocator, err := json.Marshal(contentEntryLocatorV1{
		Version: 1, Native: "native:" + physicalKey + "\x00" + versionID,
	})
	if err != nil {
		t.Fatal(err)
	}
	modified := fixture.now.Add(-time.Minute)
	entry := model.CatalogEntry{
		GenerationID: generation.ID, EntryID: strings.Repeat("b", 64), RecoveryPointID: point.ID,
		NormalizedPath: "docs/native.txt", Name: "native.txt", EntryType: string(backupasset.CatalogEntryFile),
		Size: 5, ModifiedAt: &modified, MimeType: "text/plain", Fingerprint: strings.Repeat("e", 64),
		FingerprintStrength: string(catalog.FingerprintStrong), EncryptedProviderLocator: string(entryLocator),
		SecurityState: "sealed", CreatedAt: fixture.now,
	}
	if err := fixture.db.Create(&entry).Error; err != nil {
		t.Fatal(err)
	}
	admission := &publicationAdmission{mode: publication.AdmissionManaged, generation: 1}
	service, err := NewService(Dependencies{
		DB: fixture.db, Foundation: fixture.service.foundation, Registry: fixture.service.registry,
		Keyring: fixture.service.keyring, Now: func() time.Time { return fixture.now },
		Admission: admission, Publication: fixture.service,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := content.SourceRequest{
		Ref:                 backupasset.AssetRef{RecoveryPointID: point.ID, EntryID: entry.EntryID},
		CatalogGenerationID: generation.ID, ExpectedSource: point.SourceFingerprint,
		ExpectedEntry: entry.Fingerprint, Mode: content.SourceModeSequential, MaxBytes: entry.Size,
	}
	return service, s3, request, exactReader, admission
}

func TestContentSourceNativeRcloneClosePreservesFreshProviderAndContextErrors(t *testing.T) {
	for _, testCase := range []struct {
		name string
		err  error
	}{
		{name: "provider", err: errors.New("FAKE_NATIVE_HEAD_PROVIDER_ERROR_FOR_TEST_ONLY")},
		{name: "context", err: context.DeadlineExceeded},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			service, s3, request, reader, admission := newNativeRcloneContentCloseTest(t)
			session, err := service.OpenContentSource(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			s3.headErr = testCase.err
			closeErr := session.Close()
			if !errors.Is(closeErr, testCase.err) || errors.Is(closeErr, backupasset.ErrConflict) {
				t.Fatalf("fresh %s error=%v, want original cause without ErrConflict", testCase.name, closeErr)
			}
			if s3.headCalls != 2 || len(reader.handles) != 1 || reader.handles[0].closeCount() != 1 || admission.closedCount() != 1 {
				t.Fatalf("fresh %s close lifecycle headCalls=%d handles=%d closes=%d admission=%d",
					testCase.name, s3.headCalls, len(reader.handles), reader.handles[0].closeCount(), admission.closedCount())
			}
			if secondErr := session.Close(); !errors.Is(secondErr, testCase.err) || s3.headCalls != 2 ||
				reader.handles[0].closeCount() != 1 || admission.closedCount() != 1 {
				t.Fatalf("fresh %s repeated close err=%v headCalls=%d reader=%d admission=%d",
					testCase.name, secondErr, s3.headCalls, reader.handles[0].closeCount(), admission.closedCount())
			}
		})
	}
}

func TestContentSourceNativeRcloneCloseReportsSuccessfulHeadDriftAsConflict(t *testing.T) {
	service, s3, request, reader, admission := newNativeRcloneContentCloseTest(t)
	session, err := service.OpenContentSource(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	s3.head.VersionID = "FAKE_NATIVE_CONTENT_VERSION_DRIFT_FOR_TEST_ONLY"
	if err := session.Close(); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("successful fresh head drift error=%v, want ErrConflict", err)
	}
	if s3.headCalls != 2 || len(reader.handles) != 1 || reader.handles[0].closeCount() != 1 || admission.closedCount() != 1 {
		t.Fatalf("head drift close lifecycle headCalls=%d handles=%d closes=%d admission=%d",
			s3.headCalls, len(reader.handles), reader.handles[0].closeCount(), admission.closedCount())
	}
	if err := session.Close(); !errors.Is(err, backupasset.ErrConflict) || s3.headCalls != 2 ||
		reader.handles[0].closeCount() != 1 || admission.closedCount() != 1 {
		t.Fatalf("head drift repeated close err=%v headCalls=%d reader=%d admission=%d",
			err, s3.headCalls, reader.handles[0].closeCount(), admission.closedCount())
	}
}
