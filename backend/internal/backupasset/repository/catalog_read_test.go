package repository

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"

	"gorm.io/gorm"
)

type catalogReaderSpy struct {
	request provider.CatalogReadRequest
	opened  int
	session provider.CatalogReadSession
	openErr error
	onOpen  func()
}

func (reader *catalogReaderSpy) OpenCatalogRead(_ context.Context, request provider.CatalogReadRequest) (provider.CatalogReadSession, error) {
	if reader.onOpen != nil {
		reader.onOpen()
	}
	reader.opened++
	reader.request = request
	if reader.openErr != nil {
		return nil, reader.openErr
	}
	return reader.session, nil
}

func (*catalogReaderSpy) ListPoints(_ context.Context, snapshot provider.ReadSnapshot, _ provider.PageRequest) (provider.NativePointPage, error) {
	return provider.NativePointPage{Items: []provider.NativePoint{{
		OpaqueDigest: strings.Repeat("f", 64), CapturedAt: time.Date(2026, 7, 13, 9, 0, 0, 0, time.UTC),
		Semantics: backupasset.PointMutableHead, SourceRevision: snapshot.SourceRevision,
		Locator: provider.PointLocator{Native: "FAKE_SERVER_RESOLVED_POINT_FOR_TEST_ONLY"},
	}}}, nil
}

type catalogSessionSpy struct {
	closed             int
	listCanonicalCalls int
	finalizeCalls      int
	onClose            func()
}

func (*catalogSessionSpy) SourceRevision() string { return strings.Repeat("a", 64) }

func (session *catalogSessionSpy) ListCanonical(context.Context, provider.PageRequest) (provider.CatalogRecordPage, error) {
	session.listCanonicalCalls++
	return provider.CatalogRecordPage{Items: []provider.CatalogRecord{{
		NormalizedPath: "docs/report.txt", ParentNormalizedPath: "docs", Name: "report.txt",
		Type: backupasset.CatalogEntryFile, Size: 7,
		ProviderLocator: provider.EntryLocator{Native: "FAKE_PRIVATE_PROVIDER_LOCATOR_FOR_TEST_ONLY"},
	}}}, nil
}

func (session *catalogSessionSpy) Finalize(context.Context) (provider.CatalogReadProof, error) {
	session.finalizeCalls++
	return provider.CatalogReadProof{Provider: backupasset.ProviderRsync, Mode: provider.CatalogProofMutableObservation}, nil
}

func (session *catalogSessionSpy) Close() error {
	session.closed++
	if session.onClose != nil {
		session.onClose()
	}
	return nil
}

type catalogAdmissionSpy struct {
	mu         sync.Mutex
	mode       publication.AdmissionMode
	operation  publication.ResticOperation
	nilToken   bool
	operations []publication.ResticOperation
	tokens     []*catalogAdmissionTokenSpy
	onAcquire  func(publication.ResticOperation)
}

func (spy *catalogAdmissionSpy) Acquire(_ context.Context, operation publication.ResticOperation) (publication.AdmissionToken, error) {
	spy.mu.Lock()
	spy.operations = append(spy.operations, operation)
	onAcquire := spy.onAcquire
	if spy.nilToken {
		spy.mu.Unlock()
		if onAcquire != nil {
			onAcquire(operation)
		}
		return nil, nil
	}
	tokenOperation := spy.operation
	if tokenOperation == "" {
		tokenOperation = operation
	}
	token := &catalogAdmissionTokenSpy{
		mode: spy.mode, operation: tokenOperation, requestedOperation: operation,
	}
	spy.tokens = append(spy.tokens, token)
	spy.mu.Unlock()
	if onAcquire != nil {
		onAcquire(operation)
	}
	return token, nil
}

func (spy *catalogAdmissionSpy) latestToken() *catalogAdmissionTokenSpy {
	spy.mu.Lock()
	defer spy.mu.Unlock()
	if len(spy.tokens) == 0 {
		return nil
	}
	return spy.tokens[len(spy.tokens)-1]
}

func (spy *catalogAdmissionSpy) closeCount() int32 {
	spy.mu.Lock()
	defer spy.mu.Unlock()
	var count int32
	for _, token := range spy.tokens {
		count += token.closed.Load()
	}
	return count
}

func (spy *catalogAdmissionSpy) requestedOperations() []publication.ResticOperation {
	spy.mu.Lock()
	defer spy.mu.Unlock()
	return append([]publication.ResticOperation(nil), spy.operations...)
}

type catalogAdmissionTokenSpy struct {
	mode               publication.AdmissionMode
	operation          publication.ResticOperation
	requestedOperation publication.ResticOperation
	operationAccesses  atomic.Int32
	modeAccesses       atomic.Int32
	closed             atomic.Int32
	onClose            func()
}

func (token *catalogAdmissionTokenSpy) Generation() uint64 { return 1 }
func (token *catalogAdmissionTokenSpy) Mode() publication.AdmissionMode {
	token.modeAccesses.Add(1)
	return token.mode
}
func (token *catalogAdmissionTokenSpy) Operation() publication.ResticOperation {
	token.operationAccesses.Add(1)
	return token.operation
}
func (token *catalogAdmissionTokenSpy) validationComplete() bool {
	if token == nil || token.operation != token.requestedOperation || token.operationAccesses.Load() < 1 {
		return false
	}
	switch token.mode {
	case publication.AdmissionManaged, publication.AdmissionRollbackSafe:
		return token.modeAccesses.Load() >= 1
	default:
		return false
	}
}
func (token *catalogAdmissionTokenSpy) Close() error {
	token.closed.Add(1)
	if token.onClose != nil {
		token.onClose()
	}
	return nil
}

type catalogProbeSpy struct {
	observation provider.RepositoryObservation
	err         error
	calls       atomic.Int32
	started     chan struct{}
	release     chan struct{}
	startOnce   sync.Once
	onStart     func()
}

func (probe *catalogProbeSpy) Probe(ctx context.Context, _ provider.AccessBinding, _ provider.OperationLimits) (provider.RepositoryObservation, error) {
	probe.calls.Add(1)
	if probe.onStart != nil {
		probe.onStart()
	}
	if probe.started != nil {
		probe.startOnce.Do(func() { close(probe.started) })
	}
	if probe.release != nil {
		select {
		case <-probe.release:
		case <-ctx.Done():
			return provider.RepositoryObservation{}, ctx.Err()
		}
	}
	return probe.observation, probe.err
}

func TestSealedCatalogReadSessionClosesInnerBeforeTokenExactlyOnce(t *testing.T) {
	var (
		mu     sync.Mutex
		events []string
	)
	record := func(event string) func() {
		return func() {
			mu.Lock()
			events = append(events, event)
			mu.Unlock()
		}
	}
	inner := &catalogSessionSpy{onClose: record("inner")}
	token := &catalogAdmissionTokenSpy{onClose: record("token")}
	session := &sealedCatalogReadSession{inner: inner, token: token}

	for range 2 {
		if err := session.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}
	if inner.closed != 1 || token.closed.Load() != 1 {
		t.Fatalf("close counts inner=%d token=%d, want one each", inner.closed, token.closed.Load())
	}
	mu.Lock()
	got := append([]string(nil), events...)
	mu.Unlock()
	if len(got) != 2 || got[0] != "inner" || got[1] != "token" {
		t.Fatalf("close order=%v, want [inner token]", got)
	}
}

func TestCatalogPointReadFactoryResolvesMutableSourceFromOpaqueIDsOnly(t *testing.T) {
	db := newRepositoryTestDB(t)
	taskEntity := seedTask(t, db, "rsync", t.TempDir(), "")
	prober := scopedObservationProber(backupasset.ProviderRsync)
	reader := &catalogReaderSpy{session: &catalogSessionSpy{}}
	registry := provider.NewRegistry()
	if err := registry.Register(backupasset.ProviderRsync, provider.Registration{
		Prober: prober, PointLister: reader, CatalogReader: reader,
	}); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(Dependencies{
		DB: db, Foundation: enabledFoundation(), Registry: registry,
		Now: func() time.Time { return time.Date(2026, 7, 13, 9, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	connected, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{})
	if err != nil || connected.MutablePoint == nil {
		t.Fatalf("connect=%+v err=%v", connected, err)
	}
	session, err := service.OpenCatalogRead(context.Background(), CatalogPointReadRequest{
		RepositoryID: connected.Repository.ID, RecoveryPointID: connected.MutablePoint.ID,
	})
	if err != nil {
		t.Fatalf("open Catalog point: %v", err)
	}
	if reader.opened != 1 || reader.request.Provider != backupasset.ProviderRsync ||
		reader.request.RecoveryPointID != connected.MutablePoint.ID || reader.request.Snapshot.RepositoryID != connected.Repository.ID ||
		reader.request.Mode != provider.CatalogProofMutableObservation || reader.request.Point.Native == "" ||
		reader.request.Manifest != (provider.CatalogManifestProof{}) {
		t.Fatalf("resolved request=%+v opened=%d", reader.request, reader.opened)
	}
	page, err := session.ListCanonical(context.Background(), provider.PageRequest{Limit: 10})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("Catalog page=%+v err=%v", page, err)
	}
	record := page.Items[0]
	if record.ProviderLocator.Native != "" || !secure.IsEncrypted(record.SealedProviderLocator) ||
		strings.Contains(record.SealedProviderLocator, "FAKE_PRIVATE_PROVIDER_LOCATOR_FOR_TEST_ONLY") {
		t.Fatalf("unsealed Catalog record=%+v", record)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCatalogPointReadFactoryRejectsCrossRepositoryAndCommandProvider(t *testing.T) {
	db := newRepositoryTestDB(t)
	now := time.Date(2026, 7, 13, 9, 0, 0, 0, time.UTC)
	reader := &catalogReaderSpy{session: &catalogSessionSpy{}}
	registry := provider.NewRegistry()
	if err := registry.Register(backupasset.ProviderRsync, provider.Registration{Prober: &scriptedProber{}, CatalogReader: reader}); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(Dependencies{DB: db, Foundation: enabledFoundation(), Registry: registry, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	repositoryID := strings.Repeat("1", 32)
	otherRepositoryID := strings.Repeat("2", 32)
	pointID := strings.Repeat("3", 32)
	for _, repository := range []model.BackupRepository{
		{ID: repositoryID, ProviderKind: string(backupasset.ProviderCommand), DisplayName: "command", VersionMode: string(backupasset.VersionMutableHead), Status: string(backupasset.RepositoryOnline), CapabilityRevision: 1, CapabilitiesJSON: "{}", ImmutabilityLevel: string(backupasset.ImmutabilityMutable), CreatedAt: now, UpdatedAt: now},
		{ID: otherRepositoryID, ProviderKind: string(backupasset.ProviderRsync), DisplayName: "other", VersionMode: string(backupasset.VersionMutableHead), Status: string(backupasset.RepositoryOnline), CapabilityRevision: 1, CapabilitiesJSON: "{}", ImmutabilityLevel: string(backupasset.ImmutabilityMutable), CreatedAt: now, UpdatedAt: now},
	} {
		if err := db.Create(&repository).Error; err != nil {
			t.Fatal(err)
		}
	}
	point := model.RecoveryPoint{
		ID: pointID, RepositoryID: repositoryID, Semantics: string(backupasset.PointMutableHead), State: string(backupasset.RecoveryPointCommitted),
		LineageJSON: "{}", ManifestDigestAlgorithm: "sha256", ConsistencyJSON: "{}", FidelityJSON: "{}",
		CapabilityRevision: 1, CapabilitiesJSON: "{}", ImmutabilityLevel: string(backupasset.ImmutabilityMutable),
		PhysicalAvailability: string(backupasset.PhysicalOnline), HoldState: string(backupasset.HoldNone), CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&point).Error; err != nil {
		t.Fatal(err)
	}
	_, err = service.OpenCatalogRead(context.Background(), CatalogPointReadRequest{RepositoryID: repositoryID, RecoveryPointID: pointID})
	var capabilityErr *CapabilityError
	if !errors.As(err, &capabilityErr) || capabilityErr.Reason.Code != backupasset.CapabilityTaskArtifactContractMissing {
		t.Fatalf("Command Catalog error=%v", err)
	}
	if _, err := service.OpenCatalogRead(context.Background(), CatalogPointReadRequest{RepositoryID: otherRepositoryID, RecoveryPointID: pointID}); !errors.Is(err, backupasset.ErrNotFound) {
		t.Fatalf("cross-repository Catalog error=%v", err)
	}
	if reader.opened != 0 {
		t.Fatalf("rejected request reached Catalog reader %d time(s)", reader.opened)
	}
}

func TestCatalogPointReadFactoryReconstructsExactResticPublicationProof(t *testing.T) {
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
			Completeness: backupasset.ManifestComplete, EntryCount: 0, LogicalBytes: 0, Fidelity: provider.ResticManifestFidelityV1(),
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

	reader := &catalogReaderSpy{session: &catalogSessionSpy{}}
	prober := &scriptedProber{observation: testObservation(backupasset.ProviderRestic, *fixture.repository.RepositoryIdentity)}
	registry := provider.NewRegistry()
	if err := registry.Register(backupasset.ProviderRestic, provider.Registration{Prober: prober, CatalogReader: reader}); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(Dependencies{
		DB: fixture.db, Foundation: fixture.service.foundation, Registry: registry, Keyring: fixture.service.keyring,
		Now: func() time.Time { return fixture.now }, Admission: fixture.admission, Publication: fixture.service,
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := service.OpenCatalogRead(context.Background(), CatalogPointReadRequest{
		RepositoryID: fixture.repository.ID, RecoveryPointID: pointID,
	})
	if err != nil {
		t.Fatalf("open immutable Restic Catalog: %v", err)
	}
	defer func() { _ = session.Close() }()

	request := reader.request
	if reader.opened != 1 || request.Provider != backupasset.ProviderRestic || request.Mode != provider.CatalogProofPublicationManifest ||
		request.RecoveryPointID != pointID || request.Point.Native != commit.NativePointID || request.ResticProof == nil ||
		request.Manifest.Digest != strings.Repeat("d", 64) || request.Manifest.EntryCount != 0 ||
		request.Manifest.SourceRevision != request.Snapshot.SourceRevision {
		t.Fatalf("immutable Restic Catalog request=%+v opened=%d", request, reader.opened)
	}
	proof := request.ResticProof
	if proof.Attempt.RepositoryID != fixture.repository.ID || proof.Attempt.RecoveryPointID != pointID ||
		proof.Attempt.TaskID != fixture.task.ID || proof.Attempt.TaskRunID != fixture.taskRun.ID ||
		proof.Attempt.Access.RepositoryID != fixture.repository.ID || proof.Commit != commit ||
		proof.Attempt.RequiredTags[0] != "xirang.link.v1."+fixture.link.ID ||
		proof.Attempt.RequiredTags[1] != "xirang.point.v1."+pointID {
		t.Fatalf("Restic Catalog proof=%+v", proof)
	}
	changedLocator := "sftp:changed@example.invalid:/repository"
	changedPassword := "FAKE_CHANGED_RESTIC_PASSWORD_FOR_CATALOG_TEST_ONLY"
	if err := fixture.db.Model(&model.Task{}).Where("id = ?", fixture.task.ID).Updates(map[string]any{
		"rsync_target":    changedLocator,
		"executor_config": `{"repository_password":"` + changedPassword + `"}`,
	}).Error; err != nil {
		t.Fatal(err)
	}
	wrongIdentity := provider.NativeResticIdentityPrefix + strings.Repeat("b", 64)
	prober.probe = func(binding provider.AccessBinding) (provider.RepositoryObservation, error) {
		if binding.TaskID != fixture.task.ID || binding.NodeID != fixture.node.ID ||
			binding.Locator != changedLocator || string(binding.Secret) != changedPassword {
			t.Fatalf("Restic Catalog Probe access=%+v, want current producing Task access", binding)
		}
		return testObservation(backupasset.ProviderRestic, wrongIdentity), nil
	}
	beforeOpened := reader.opened
	if _, err := service.OpenCatalogRead(context.Background(), CatalogPointReadRequest{
		RepositoryID: fixture.repository.ID, RecoveryPointID: pointID,
	}); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("wrong-identity Restic Catalog error=%v, want conflict", err)
	}
	if reader.opened != beforeOpened {
		t.Fatalf("wrong-identity Restic Catalog reached Catalog reader: before=%d after=%d", beforeOpened, reader.opened)
	}
	if prober.calls != 2 {
		t.Fatalf("Restic Catalog Probe calls=%d, want initial proof plus rejected read", prober.calls)
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
	beforeRaceOpened := reader.opened
	beforeRaceProbes := prober.calls
	if _, err := service.OpenCatalogRead(context.Background(), CatalogPointReadRequest{
		RepositoryID: fixture.repository.ID, RecoveryPointID: pointID,
	}); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("capability-revision-race Restic Catalog error=%v, want conflict", err)
	}
	if !raceCallbackFired {
		t.Fatal("capability-revision-race callback did not fire")
	}
	if capturedOuterCapabilityRevision != fixture.repository.CapabilityRevision {
		t.Fatalf("capability-revision-race Restic Catalog outer CapabilityRevision=%d, want unchanged %d", capturedOuterCapabilityRevision, fixture.repository.CapabilityRevision)
	}
	if prober.calls != beforeRaceProbes {
		t.Fatalf("capability-revision-race Restic Catalog Probe calls=%d, want unchanged at %d", prober.calls, beforeRaceProbes)
	}
	if reader.opened != beforeRaceOpened {
		t.Fatalf("capability-revision-race Restic Catalog reached Catalog reader: before=%d after=%d", beforeRaceOpened, reader.opened)
	}
}

func TestResticCatalogReadAcquiresManifestBeforeProbeAndTransfersToken(t *testing.T) {
	admission := &catalogAdmissionSpy{mode: publication.AdmissionManaged}
	probe := &catalogProbeSpy{started: make(chan struct{}), release: make(chan struct{})}
	inner := &catalogSessionSpy{}
	reader := &catalogReaderSpy{session: inner}
	fixture, pointID := newResticCatalogReadFixture(t)
	probe.observation = testObservation(backupasset.ProviderRestic, *fixture.repository.RepositoryIdentity)
	service := newResticCatalogReadService(t, fixture, admission, probe, reader)

	runtimeObserved := atomic.Bool{}
	runtimeBeforeValidation := atomic.Bool{}
	providerBeforeValidation := atomic.Bool{}
	probe.onStart = func() {
		runtimeObserved.Store(true)
		token := admission.latestToken()
		if token == nil || !token.validationComplete() {
			providerBeforeValidation.Store(true)
		}
	}
	callbackName := "repository:restic-catalog-admission-order:" + strings.ReplaceAll(t.Name(), "/", "_")
	if err := fixture.db.Callback().Query().After("gorm:after_query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Table != "tasks" {
			return
		}
		runtimeObserved.Store(true)
		token := admission.latestToken()
		if token == nil || !token.validationComplete() {
			runtimeBeforeValidation.Store(true)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := fixture.db.Callback().Query().Remove(callbackName); err != nil {
			t.Errorf("remove Restic Catalog admission callback: %v", err)
		}
	})

	type openResult struct {
		session provider.CatalogReadSession
		err     error
	}
	resultCh := make(chan openResult, 1)
	go func() {
		session, err := service.OpenCatalogRead(context.Background(), CatalogPointReadRequest{
			RepositoryID: fixture.repository.ID, RecoveryPointID: pointID,
		})
		resultCh <- openResult{session: session, err: err}
	}()

	released := false
	defer func() {
		if !released {
			close(probe.release)
		}
	}()
	select {
	case <-probe.started:
	case <-time.After(time.Second):
		t.Fatal("Restic Catalog Probe did not start")
	}
	if !runtimeObserved.Load() || runtimeBeforeValidation.Load() || providerBeforeValidation.Load() {
		t.Fatalf("Restic Catalog admission validation ordering runtimeObserved=%v runtimeBeforeValidation=%v providerBeforeValidation=%v", runtimeObserved.Load(), runtimeBeforeValidation.Load(), providerBeforeValidation.Load())
	}
	token := admission.latestToken()
	if token == nil || token.Operation() != publication.OperationManifest ||
		token.Mode() != publication.AdmissionManaged || token.closed.Load() != 0 {
		t.Fatalf("manifest admission token=%+v, want active manifest token", token)
	}
	operations := admission.requestedOperations()
	if len(operations) != 1 || operations[0] != publication.OperationManifest {
		t.Fatalf("admission operations=%v, want one manifest operation", operations)
	}
	if probe.calls.Load() != 1 {
		t.Fatalf("Probe calls=%d, want one blocked Probe", probe.calls.Load())
	}
	if reader.opened != 0 {
		t.Fatalf("Catalog reader opened=%d before Probe release, want zero", reader.opened)
	}

	close(probe.release)
	released = true
	var result openResult
	select {
	case result = <-resultCh:
	case <-time.After(time.Second):
		t.Fatal("Restic Catalog open did not finish after Probe release")
	}
	if result.err != nil || result.session == nil {
		t.Fatalf("Restic Catalog open session=%v err=%v", result.session, result.err)
	}
	if token.closed.Load() != 0 {
		t.Fatalf("manifest token closed before returned session use: %d", token.closed.Load())
	}
	if reader.opened != 1 {
		t.Fatalf("Catalog reader opened=%d, want one", reader.opened)
	}
	if _, err := result.session.ListCanonical(context.Background(), provider.PageRequest{Limit: 10}); err != nil {
		t.Fatalf("ListCanonical: %v", err)
	}
	if _, err := result.session.Finalize(context.Background()); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if token.closed.Load() != 0 {
		t.Fatalf("manifest token closed during returned session use: %d", token.closed.Load())
	}
	if err := result.session.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if token.closed.Load() != 1 || admission.closeCount() != 1 || inner.closed != 1 {
		t.Fatalf("close counts token=%d admission=%d inner=%d, want one each", token.closed.Load(), admission.closeCount(), inner.closed)
	}
	if err := result.session.Close(); err != nil {
		t.Fatalf("repeated Close: %v", err)
	}
	if token.closed.Load() != 1 || admission.closeCount() != 1 || inner.closed != 1 {
		t.Fatalf("repeated Close changed counts token=%d admission=%d inner=%d", token.closed.Load(), admission.closeCount(), inner.closed)
	}
}

func TestResticCatalogReadClosesManifestTokenOnProbeReaderAndNilSessionErrors(t *testing.T) {
	probeErr := errors.New("FAKE_RESTIC_CATALOG_PROBE_ERROR_FOR_TEST_ONLY")
	readerErr := errors.New("FAKE_RESTIC_CATALOG_READER_ERROR_FOR_TEST_ONLY")
	tests := []struct {
		name             string
		probeErr         error
		identityMismatch bool
		readerErr        error
		nilReader        bool
		wantErr          error
	}{
		{name: "Probe error", probeErr: probeErr, wantErr: probeErr},
		{name: "identity proof error", identityMismatch: true, wantErr: backupasset.ErrConflict},
		{name: "reader open error", readerErr: readerErr, wantErr: readerErr},
		{name: "nil reader session", nilReader: true, wantErr: backupasset.ErrInvalidState},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			admission := &catalogAdmissionSpy{mode: publication.AdmissionManaged}
			probe := &catalogProbeSpy{err: test.probeErr}
			inner := &catalogSessionSpy{}
			reader := &catalogReaderSpy{session: inner, openErr: test.readerErr}
			fixture, pointID := newResticCatalogReadFixture(t)
			identity := *fixture.repository.RepositoryIdentity
			if test.identityMismatch {
				identity = provider.NativeResticIdentityPrefix + strings.Repeat("b", 64)
			}
			probe.observation = testObservation(backupasset.ProviderRestic, identity)
			if test.nilReader {
				reader.session = nil
			}
			service := newResticCatalogReadService(t, fixture, admission, probe, reader)

			_, err := service.OpenCatalogRead(context.Background(), CatalogPointReadRequest{
				RepositoryID: fixture.repository.ID, RecoveryPointID: pointID,
			})
			if err == nil || !errors.Is(err, test.wantErr) {
				t.Fatalf("Restic Catalog error=%v, want %v", err, test.wantErr)
			}
			if admission.closeCount() != 1 {
				t.Fatalf("manifest token closes=%d, want one", admission.closeCount())
			}
			if probe.calls.Load() != 1 {
				t.Fatalf("Probe calls=%d, want one", probe.calls.Load())
			}
			if test.probeErr != nil || test.identityMismatch {
				if reader.opened != 0 {
					t.Fatalf("Catalog reader opened=%d after proof failure, want zero", reader.opened)
				}
			} else if reader.opened != 1 {
				t.Fatalf("Catalog reader opened=%d, want one", reader.opened)
			}
		})
	}
}

func TestResticCatalogReadRejectsInvalidManifestAdmissionBeforeProvider(t *testing.T) {
	tests := []struct {
		name      string
		mode      publication.AdmissionMode
		operation publication.ResticOperation
		nilToken  bool
		wantClose int32
	}{
		{name: "wrong operation", mode: publication.AdmissionManaged, operation: publication.OperationContentRead, wantClose: 1},
		{name: "PristineLegacy", mode: publication.AdmissionPristineLegacy, operation: publication.OperationManifest, wantClose: 1},
		{name: "unknown mode", mode: publication.AdmissionMode("future_mode"), operation: publication.OperationManifest, wantClose: 1},
		{name: "nil token", mode: publication.AdmissionManaged, nilToken: true, wantClose: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			admission := &catalogAdmissionSpy{mode: test.mode, operation: test.operation, nilToken: test.nilToken}
			probe := &catalogProbeSpy{}
			reader := &catalogReaderSpy{session: &catalogSessionSpy{}}
			fixture, pointID := newResticCatalogReadFixture(t)
			probe.observation = testObservation(backupasset.ProviderRestic, *fixture.repository.RepositoryIdentity)
			service := newResticCatalogReadService(t, fixture, admission, probe, reader)
			var exactRuntimeQueries atomic.Int32
			callbackName := "repository:restic-catalog-invalid-admission:" + strings.ReplaceAll(t.Name(), "/", "_")
			if err := fixture.db.Callback().Query().After("gorm:after_query").Register(callbackName, func(tx *gorm.DB) {
				if tx.Statement != nil && tx.Statement.Table == "tasks" {
					exactRuntimeQueries.Add(1)
				}
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := fixture.db.Callback().Query().Remove(callbackName); err != nil {
					t.Errorf("remove Restic invalid-admission callback: %v", err)
				}
			})

			_, err := service.OpenCatalogRead(context.Background(), CatalogPointReadRequest{
				RepositoryID: fixture.repository.ID, RecoveryPointID: pointID,
			})
			if !errors.Is(err, backupasset.ErrForbidden) {
				t.Fatalf("Restic Catalog error=%v, want ErrForbidden", err)
			}
			if admission.closeCount() != test.wantClose {
				t.Fatalf("manifest token closes=%d, want %d", admission.closeCount(), test.wantClose)
			}
			if probe.calls.Load() != 0 || reader.opened != 0 || exactRuntimeQueries.Load() != 0 {
				t.Fatalf("invalid admission reached exact provider runtime: Probe calls=%d reader opens=%d runtime queries=%d", probe.calls.Load(), reader.opened, exactRuntimeQueries.Load())
			}
		})
	}
}
func TestResticCatalogReadAdmitsManagedAndRollbackSafeManifestTokens(t *testing.T) {
	for _, mode := range []publication.AdmissionMode{publication.AdmissionManaged, publication.AdmissionRollbackSafe} {
		t.Run(string(mode), func(t *testing.T) {
			admission := &catalogAdmissionSpy{mode: mode}
			probe := &catalogProbeSpy{}
			inner := &catalogSessionSpy{}
			reader := &catalogReaderSpy{session: inner}
			fixture, pointID := newResticCatalogReadFixture(t)
			probe.observation = testObservation(backupasset.ProviderRestic, *fixture.repository.RepositoryIdentity)
			service := newResticCatalogReadService(t, fixture, admission, probe, reader)

			session, err := service.OpenCatalogRead(context.Background(), CatalogPointReadRequest{
				RepositoryID: fixture.repository.ID, RecoveryPointID: pointID,
			})
			if err != nil || session == nil {
				t.Fatalf("Restic Catalog session=%v err=%v", session, err)
			}
			token := admission.latestToken()
			if token == nil || token.closed.Load() != 0 {
				t.Fatalf("admitted manifest token=%+v, want active", token)
			}
			if reader.opened != 1 || probe.calls.Load() != 1 {
				t.Fatalf("provider calls Probe=%d reader=%d, want one each", probe.calls.Load(), reader.opened)
			}
			if err := session.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			if admission.closeCount() != 1 || inner.closed != 1 {
				t.Fatalf("close counts admission=%d inner=%d, want one each", admission.closeCount(), inner.closed)
			}
		})
	}
}

func TestCatalogPointReadFactoryAdmitsCommittedRsyncManagedAndRollbackSafe(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("strict managed Rsync Catalog reads are Linux-only")
	}
	for _, mode := range []publication.AdmissionMode{publication.AdmissionManaged, publication.AdmissionRollbackSafe} {
		t.Run(string(mode), func(t *testing.T) {
			fixture := newManagedRsyncCatalogFixture(t)
			baseService, ok := fixture.factory.(*Service)
			if !ok || baseService == nil {
				t.Fatalf("managed Rsync Catalog fixture factory=%T, want repository Service", fixture.factory)
			}
			admission := &catalogAdmissionSpy{mode: mode}
			service, err := NewService(Dependencies{
				DB: fixture.db, Foundation: baseService.foundation, Registry: baseService.registry, Keyring: fixture.keyring,
				Now: func() time.Time { return fixture.now }, Admission: admission, Publication: baseService.publication,
			})
			if err != nil {
				t.Fatal(err)
			}

			runtimeObserved := atomic.Bool{}
			runtimeBeforeValidation := atomic.Bool{}
			callbackName := "repository:rsync-valid-catalog-admission-order:" + strings.ReplaceAll(t.Name(), "/", "_")
			if err := fixture.db.Callback().Query().After("gorm:after_query").Register(callbackName, func(tx *gorm.DB) {
				if tx.Statement == nil || tx.Statement.Table != "tasks" {
					return
				}
				runtimeObserved.Store(true)
				token := admission.latestToken()
				if token == nil || !token.validationComplete() {
					runtimeBeforeValidation.Store(true)
				}
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := fixture.db.Callback().Query().Remove(callbackName); err != nil {
					t.Errorf("remove Rsync Catalog admission callback: %v", err)
				}
			})

			session, err := service.OpenCatalogRead(context.Background(), CatalogPointReadRequest{
				RepositoryID: fixture.repository.ID, RecoveryPointID: fixture.point.ID,
			})
			if err != nil || session == nil {
				t.Fatalf("open committed Rsync Catalog session=%v err=%v", session, err)
			}
			defer func() { _ = session.Close() }()
			token := admission.latestToken()
			if token == nil || token.Operation() != publication.OperationManagedRsyncPointRead ||
				token.Mode() != mode || token.closed.Load() != 0 {
				t.Fatalf("Rsync admission token=%+v, want active %s managed point-read token", token, mode)
			}
			if !runtimeObserved.Load() || runtimeBeforeValidation.Load() {
				t.Fatalf("Rsync Catalog admission ordering runtimeObserved=%v runtimeBeforeValidation=%v", runtimeObserved.Load(), runtimeBeforeValidation.Load())
			}

			page, err := session.ListCanonical(context.Background(), provider.PageRequest{Limit: 10})
			if err != nil || len(page.Items) != 1 || page.NextCursor != "" {
				t.Fatalf("committed Rsync Catalog page=%+v err=%v", page, err)
			}
			if token.closed.Load() != 0 {
				t.Fatalf("Rsync admission token closed during ListCanonical: %d", token.closed.Load())
			}
			proof, err := session.Finalize(context.Background())
			if err != nil || proof.Provider != backupasset.ProviderRsync ||
				proof.Mode != provider.CatalogProofPublicationManifest || !proof.Catalog.Complete {
				t.Fatalf("committed Rsync Catalog proof=%+v err=%v", proof, err)
			}
			if token.closed.Load() != 0 {
				t.Fatalf("Rsync admission token closed during Finalize: %d", token.closed.Load())
			}
			if err := session.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			if err := session.Close(); err != nil {
				t.Fatalf("repeated Close: %v", err)
			}
			if token.closed.Load() != 1 || admission.closeCount() != 1 {
				t.Fatalf("Rsync close counts token=%d admission=%d, want one each", token.closed.Load(), admission.closeCount())
			}
		})
	}
}

func TestCatalogPointReadFactoryRejectsManagedRsyncCapabilityRevisionRace(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("strict managed Rsync Catalog reads are Linux-only")
	}
	fixture := newManagedRsyncCatalogFixture(t)
	baseService, ok := fixture.factory.(*Service)
	if !ok || baseService == nil {
		t.Fatalf("managed Rsync Catalog fixture factory=%T, want repository Service", fixture.factory)
	}
	admission := &catalogAdmissionSpy{mode: publication.AdmissionManaged}
	service, err := NewService(Dependencies{
		DB: fixture.db, Foundation: baseService.foundation, Registry: baseService.registry, Keyring: fixture.keyring,
		Now: func() time.Time { return fixture.now }, Admission: admission, Publication: baseService.publication,
	})
	if err != nil {
		t.Fatal(err)
	}

	raceCallbackName := "repository:managed-rsync-catalog-capability-revision-race:" + strings.ReplaceAll(t.Name(), "/", "_")
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
			t.Errorf("remove managed Rsync Catalog capability revision race callback: %v", err)
		}
	})

	session, err := service.OpenCatalogRead(context.Background(), CatalogPointReadRequest{
		RepositoryID: fixture.repository.ID, RecoveryPointID: fixture.point.ID,
	})
	if session != nil {
		_ = session.Close()
		t.Fatalf("capability-revision-race managed Rsync Catalog session=%v, want nil", session)
	}
	if !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("capability-revision-race managed Rsync Catalog error=%v, want ErrConflict", err)
	}
	if !raceCallbackFired {
		t.Fatal("capability-revision-race managed Rsync Catalog callback did not fire")
	}
	if capturedOuterCapabilityRevision != fixture.repository.CapabilityRevision {
		t.Fatalf("capability-revision-race managed Rsync Catalog outer CapabilityRevision=%d, want unchanged %d", capturedOuterCapabilityRevision, fixture.repository.CapabilityRevision)
	}
	var persisted model.BackupRepository
	if err := fixture.db.First(&persisted, "id = ?", fixture.repository.ID).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.CapabilityRevision != capturedOuterCapabilityRevision+1 {
		t.Fatalf("capability-revision-race managed Rsync Catalog persisted CapabilityRevision=%d, want %d", persisted.CapabilityRevision, capturedOuterCapabilityRevision+1)
	}
	token := admission.latestToken()
	operations := admission.requestedOperations()
	if token == nil || token.Operation() != publication.OperationManagedRsyncPointRead ||
		token.closed.Load() != 1 || admission.closeCount() != 1 ||
		len(operations) != 1 || operations[0] != publication.OperationManagedRsyncPointRead {
		t.Fatalf("capability-revision-race managed Rsync Catalog token=%+v operations=%v closes=%d, want one closed point-read token", token, operations, admission.closeCount())
	}
}

func TestCatalogPointReadFactoryRejectsInvalidRsyncAdmissionBeforeExactRuntime(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("strict managed Rsync Catalog reads are Linux-only")
	}
	tests := []struct {
		name      string
		mode      publication.AdmissionMode
		operation publication.ResticOperation
		nilToken  bool
		wantClose int32
	}{
		{name: "wrong operation", mode: publication.AdmissionManaged, operation: publication.OperationContentRead, wantClose: 1},
		{name: "PristineLegacy", mode: publication.AdmissionPristineLegacy, operation: publication.OperationManagedRsyncPointRead, wantClose: 1},
		{name: "unknown mode", mode: publication.AdmissionMode("future_mode"), operation: publication.OperationManagedRsyncPointRead, wantClose: 1},
		{name: "nil token", mode: publication.AdmissionManaged, nilToken: true, wantClose: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newManagedRsyncCatalogFixture(t)
			baseService, ok := fixture.factory.(*Service)
			if !ok || baseService == nil {
				t.Fatalf("managed Rsync Catalog fixture factory=%T, want repository Service", fixture.factory)
			}
			admission := &catalogAdmissionSpy{mode: test.mode, operation: test.operation, nilToken: test.nilToken}
			service, err := NewService(Dependencies{
				DB: fixture.db, Foundation: baseService.foundation, Registry: baseService.registry, Keyring: fixture.keyring,
				Now: func() time.Time { return fixture.now }, Admission: admission, Publication: baseService.publication,
			})
			if err != nil {
				t.Fatal(err)
			}
			var exactRuntimeQueries atomic.Int32
			callbackName := "repository:rsync-invalid-catalog-admission:" + strings.ReplaceAll(t.Name(), "/", "_")
			if err := fixture.db.Callback().Query().After("gorm:after_query").Register(callbackName, func(tx *gorm.DB) {
				if tx.Statement != nil && tx.Statement.Table == "tasks" {
					exactRuntimeQueries.Add(1)
				}
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := fixture.db.Callback().Query().Remove(callbackName); err != nil {
					t.Errorf("remove Rsync invalid-admission callback: %v", err)
				}
			})

			if _, err := service.OpenCatalogRead(context.Background(), CatalogPointReadRequest{
				RepositoryID: fixture.repository.ID, RecoveryPointID: fixture.point.ID,
			}); !errors.Is(err, backupasset.ErrForbidden) {
				t.Fatalf("invalid committed Rsync Catalog admission error=%v, want ErrForbidden", err)
			}
			token := admission.latestToken()
			if admission.closeCount() != test.wantClose || exactRuntimeQueries.Load() != 0 {
				t.Fatalf("invalid Rsync admission token=%+v closes=%d runtime queries=%d, want close=%d and no exact runtime",
					token, admission.closeCount(), exactRuntimeQueries.Load(), test.wantClose)
			}
			if test.wantClose == 1 && (token == nil || token.closed.Load() != 1) {
				t.Fatalf("invalid Rsync token=%+v, want one close", token)
			}
		})
	}
}

func TestCatalogPointReadFactoryUsesCommittedRsyncEvidenceInsteadOfMutableRoot(t *testing.T) {
	fixture := newRsyncPublicationFixture(t)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = execution.Abandon(backupasset.ErrPublicationSessionAbandoned) }()
	state := execution.(*rsyncPublicationExecution)
	commit := provider.RsyncTreeCommitV1{
		LayoutVersion: 1, RepositoryID: state.attempt.RepositoryID, TaskRepositoryLinkID: state.attempt.TaskRepositoryLinkID,
		RecoveryPointID: state.attempt.RecoveryPointID, AttemptID: state.attempt.AttemptID, PublicationMode: state.attempt.PublicationMode,
		ManifestDigestAlgorithm: "sha256", ManifestDigest: strings.Repeat("1", 64), ManifestEntryCount: 1, LogicalBytes: 42,
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
	admission := &catalogAdmissionSpy{mode: publication.AdmissionManaged}
	service, err := NewService(Dependencies{
		DB: fixture.db, Foundation: fixture.service.foundation, Registry: fixture.service.registry, Keyring: fixture.service.keyring,
		Now: func() time.Time { return fixture.now }, Admission: admission, Publication: fixture.service,
	})
	if err != nil {
		t.Fatal(err)
	}
	invalidAdmission := &catalogAdmissionSpy{mode: publication.AdmissionManaged, operation: publication.OperationContentRead}
	invalidService, err := NewService(Dependencies{
		DB: fixture.db, Foundation: fixture.service.foundation, Registry: fixture.service.registry, Keyring: fixture.service.keyring,
		Now: func() time.Time { return fixture.now }, Admission: invalidAdmission, Publication: fixture.service,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, invalidErr := invalidService.OpenCatalogRead(context.Background(), CatalogPointReadRequest{
		RepositoryID: fixture.repository.ID, RecoveryPointID: state.attempt.RecoveryPointID,
	}); !errors.Is(invalidErr, backupasset.ErrForbidden) {
		t.Fatalf("invalid committed Rsync Catalog admission error=%v, want ErrForbidden", invalidErr)
	}
	invalidToken := invalidAdmission.latestToken()
	if invalidToken == nil || invalidToken.closed.Load() != 1 ||
		len(invalidAdmission.requestedOperations()) != 1 ||
		invalidAdmission.requestedOperations()[0] != publication.OperationManagedRsyncPointRead {
		t.Fatalf("invalid Rsync admission token=%+v operations=%v, want closed requested managed point read", invalidToken, invalidAdmission.requestedOperations())
	}
	admissionAcquired := atomic.Bool{}
	runtimeObserved := atomic.Bool{}
	runtimeBeforeAcquire := atomic.Bool{}
	runtimeBeforeValidation := atomic.Bool{}
	admission.onAcquire = func(publication.ResticOperation) {
		admissionAcquired.Store(true)
	}
	callbackName := "repository:rsync-catalog-admission-order:" + strings.ReplaceAll(t.Name(), "/", "_")
	if err := fixture.db.Callback().Query().After("gorm:after_query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Table != "tasks" {
			return
		}
		runtimeObserved.Store(true)
		token := admission.latestToken()
		if token == nil || !token.validationComplete() {
			runtimeBeforeValidation.Store(true)
		}
		if !admissionAcquired.Load() {
			runtimeBeforeAcquire.Store(true)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := fixture.db.Callback().Query().Remove(callbackName); err != nil {
			t.Errorf("remove Rsync Catalog admission callback: %v", err)
		}
	})
	_, err = service.OpenCatalogRead(context.Background(), CatalogPointReadRequest{
		RepositoryID: fixture.repository.ID, RecoveryPointID: state.attempt.RecoveryPointID,
	})
	reason, _, ok := CapabilityFromError(err)
	if !ok || reason.Code != backupasset.CapabilityMutableSourceChanged {
		t.Fatalf("committed Rsync Catalog exact-source error=%v reason=%+v", err, reason)
	}
	if !runtimeObserved.Load() || runtimeBeforeAcquire.Load() || runtimeBeforeValidation.Load() {
		t.Fatalf("Rsync Catalog admission ordering runtimeObserved=%v runtimeBeforeAcquire=%v runtimeBeforeValidation=%v",
			runtimeObserved.Load(), runtimeBeforeAcquire.Load(), runtimeBeforeValidation.Load())
	}
	token := admission.latestToken()
	operations := admission.requestedOperations()
	if token == nil || token.Operation() != publication.OperationManagedRsyncPointRead ||
		token.Mode() != publication.AdmissionManaged || token.closed.Load() != 1 ||
		len(operations) != 1 || operations[0] != publication.OperationManagedRsyncPointRead {
		t.Fatalf("Rsync admission token=%+v operations=%v, want closed managed point-read token", token, operations)
	}
	rollbackAdmission := &catalogAdmissionSpy{mode: publication.AdmissionRollbackSafe}
	rollbackService, err := NewService(Dependencies{
		DB: fixture.db, Foundation: fixture.service.foundation, Registry: fixture.service.registry, Keyring: fixture.service.keyring,
		Now: func() time.Time { return fixture.now }, Admission: rollbackAdmission, Publication: fixture.service,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, rollbackErr := rollbackService.OpenCatalogRead(context.Background(), CatalogPointReadRequest{
		RepositoryID: fixture.repository.ID, RecoveryPointID: state.attempt.RecoveryPointID,
	})
	rollbackReason, _, rollbackOK := CapabilityFromError(rollbackErr)
	if !rollbackOK || rollbackReason.Code != backupasset.CapabilityMutableSourceChanged {
		t.Fatalf("rollback-safe committed Rsync Catalog exact-source error=%v reason=%+v", rollbackErr, rollbackReason)
	}
	rollbackToken := rollbackAdmission.latestToken()
	rollbackOperations := rollbackAdmission.requestedOperations()
	if rollbackToken == nil || rollbackToken.Operation() != publication.OperationManagedRsyncPointRead ||
		rollbackToken.Mode() != publication.AdmissionRollbackSafe || rollbackToken.closed.Load() != 1 ||
		len(rollbackOperations) != 1 || rollbackOperations[0] != publication.OperationManagedRsyncPointRead {
		t.Fatalf("rollback-safe Rsync admission token=%+v operations=%v, want closed managed point-read token", rollbackToken, rollbackOperations)
	}
}

func TestCatalogPointReadFactoryReconstructsExactRcloneControlProof(t *testing.T) {
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

	inner := &catalogSessionSpy{}
	reader := &catalogReaderSpy{session: inner}
	registry := provider.NewRegistry()
	if err := registry.Register(backupasset.ProviderRclone, provider.Registration{Prober: &scriptedProber{}, CatalogReader: reader}); err != nil {
		t.Fatal(err)
	}
	admission := &catalogAdmissionSpy{mode: publication.AdmissionManaged}
	service, err := NewService(Dependencies{
		DB: fixture.db, Foundation: fixture.service.foundation, Registry: registry, Keyring: fixture.service.keyring,
		Now: func() time.Time { return fixture.now }, Admission: admission, Publication: fixture.service,
	})
	if err != nil {
		t.Fatal(err)
	}
	admissionAcquired := atomic.Bool{}
	runtimeObserved := atomic.Bool{}
	runtimeBeforeAcquire := atomic.Bool{}
	runtimeBeforeValidation := atomic.Bool{}
	providerBeforeAcquire := atomic.Bool{}
	providerBeforeValidation := atomic.Bool{}
	admission.onAcquire = func(publication.ResticOperation) {
		admissionAcquired.Store(true)
	}
	reader.onOpen = func() {
		token := admission.latestToken()
		if token == nil || !token.validationComplete() {
			providerBeforeValidation.Store(true)
		}
		if !admissionAcquired.Load() {
			providerBeforeAcquire.Store(true)
		}
	}
	callbackName := "repository:rclone-catalog-admission-order:" + strings.ReplaceAll(t.Name(), "/", "_")
	if err := fixture.db.Callback().Query().After("gorm:after_query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Table != "tasks" {
			return
		}
		runtimeObserved.Store(true)
		token := admission.latestToken()
		if token == nil || !token.validationComplete() {
			runtimeBeforeValidation.Store(true)
		}
		if !admissionAcquired.Load() {
			runtimeBeforeAcquire.Store(true)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := fixture.db.Callback().Query().Remove(callbackName); err != nil {
			t.Errorf("remove Rclone Catalog admission callback: %v", err)
		}
	})
	session, err := service.OpenCatalogRead(context.Background(), CatalogPointReadRequest{
		RepositoryID: fixture.repository.ID, RecoveryPointID: attempt.RecoveryPointID,
	})
	if err != nil {
		t.Fatalf("open immutable Rclone Catalog: %v", err)
	}
	defer func() { _ = session.Close() }()
	token := admission.latestToken()
	operations := admission.requestedOperations()
	if token == nil || token.Operation() != publication.OperationManifest ||
		token.Mode() != publication.AdmissionManaged || token.closed.Load() != 0 ||
		len(operations) != 1 || operations[0] != publication.OperationManifest {
		t.Fatalf("Rclone admission token=%+v operations=%v, want active manifest token", token, operations)
	}
	request := reader.request
	if reader.opened != 1 || request.Provider != backupasset.ProviderRclone || request.Mode != provider.CatalogProofPublicationManifest ||
		request.RcloneProof == nil || request.RcloneProof.Reconcile.PortableRequest == nil || request.RcloneProof.Reconcile.NativeRequest != nil ||
		request.RcloneProof.Commit.ManifestIndexDigest != request.Manifest.Digest || request.Manifest.EntryCount != int64(commit.ManifestEntryCount) ||
		request.Point.Native == "" || request.Snapshot.Access.Locator != "" {
		t.Fatalf("immutable Rclone Catalog request=%+v opened=%d", request, reader.opened)
	}
	if _, err := session.ListCanonical(context.Background(), provider.PageRequest{Limit: 10}); err != nil {
		t.Fatalf("Rclone ListCanonical: %v", err)
	}
	if _, err := session.Finalize(context.Background()); err != nil {
		t.Fatalf("Rclone Finalize: %v", err)
	}
	if token.closed.Load() != 0 || inner.listCanonicalCalls != 1 || inner.finalizeCalls != 1 {
		t.Fatalf("Rclone returned session use token=%d list=%d finalize=%d, want active token and one each", token.closed.Load(), inner.listCanonicalCalls, inner.finalizeCalls)
	}
	if !runtimeObserved.Load() || runtimeBeforeAcquire.Load() || providerBeforeAcquire.Load() ||
		runtimeBeforeValidation.Load() || providerBeforeValidation.Load() {
		t.Fatalf("Rclone Catalog admission ordering runtimeObserved=%v runtimeBeforeAcquire=%v providerBeforeAcquire=%v runtimeBeforeValidation=%v providerBeforeValidation=%v",
			runtimeObserved.Load(), runtimeBeforeAcquire.Load(), providerBeforeAcquire.Load(), runtimeBeforeValidation.Load(), providerBeforeValidation.Load())
	}
	if err := session.Close(); err != nil {
		t.Fatalf("close immutable Rclone Catalog: %v", err)
	}
	if token.closed.Load() != 1 || admission.closeCount() != 1 || inner.closed != 1 {
		t.Fatalf("Rclone close counts token=%d admission=%d inner=%d, want one each", token.closed.Load(), admission.closeCount(), inner.closed)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("repeat close immutable Rclone Catalog: %v", err)
	}
	if token.closed.Load() != 1 || admission.closeCount() != 1 || inner.closed != 1 {
		t.Fatalf("repeat Rclone close changed counts token=%d admission=%d inner=%d", token.closed.Load(), admission.closeCount(), inner.closed)
	}
	invalidTests := []struct {
		name      string
		mode      publication.AdmissionMode
		operation publication.ResticOperation
		nilToken  bool
		wantClose int32
	}{
		{name: "wrong operation", mode: publication.AdmissionManaged, operation: publication.OperationContentRead, wantClose: 1},
		{name: "PristineLegacy", mode: publication.AdmissionPristineLegacy, operation: publication.OperationManifest, wantClose: 1},
		{name: "unknown mode", mode: publication.AdmissionMode("future_mode"), operation: publication.OperationManifest, wantClose: 1},
		{name: "nil token", mode: publication.AdmissionManaged, nilToken: true, wantClose: 0},
	}
	for _, test := range invalidTests {
		t.Run("invalid "+test.name, func(t *testing.T) {
			invalidAdmission := &catalogAdmissionSpy{mode: test.mode, operation: test.operation, nilToken: test.nilToken}
			invalidReader := &catalogReaderSpy{session: &catalogSessionSpy{}}
			invalidRegistry := provider.NewRegistry()
			if err := invalidRegistry.Register(backupasset.ProviderRclone, provider.Registration{
				Prober: &scriptedProber{}, CatalogReader: invalidReader,
			}); err != nil {
				t.Fatal(err)
			}
			invalidService, err := NewService(Dependencies{
				DB: fixture.db, Foundation: fixture.service.foundation, Registry: invalidRegistry, Keyring: fixture.service.keyring,
				Now: func() time.Time { return fixture.now }, Admission: invalidAdmission, Publication: fixture.service,
			})
			if err != nil {
				t.Fatal(err)
			}
			var exactRuntimeQueries atomic.Int32
			callbackName := "repository:rclone-invalid-admission:" + strings.ReplaceAll(t.Name(), "/", "_")
			if err := fixture.db.Callback().Query().After("gorm:after_query").Register(callbackName, func(tx *gorm.DB) {
				if tx.Statement != nil && tx.Statement.Table == "tasks" {
					exactRuntimeQueries.Add(1)
				}
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := fixture.db.Callback().Query().Remove(callbackName); err != nil {
					t.Errorf("remove Rclone invalid-admission callback: %v", err)
				}
			})

			if _, invalidErr := invalidService.OpenCatalogRead(context.Background(), CatalogPointReadRequest{
				RepositoryID: fixture.repository.ID, RecoveryPointID: attempt.RecoveryPointID,
			}); !errors.Is(invalidErr, backupasset.ErrForbidden) {
				t.Fatalf("invalid immutable Rclone Catalog admission error=%v, want ErrForbidden", invalidErr)
			}
			invalidToken := invalidAdmission.latestToken()
			invalidOperations := invalidAdmission.requestedOperations()
			if invalidAdmission.closeCount() != test.wantClose || len(invalidOperations) != 1 ||
				invalidOperations[0] != publication.OperationManifest || invalidReader.opened != 0 ||
				exactRuntimeQueries.Load() != 0 {
				t.Fatalf("invalid Rclone admission token=%+v operations=%v closes=%d reader opens=%d runtime queries=%d, want close=%d and no exact runtime",
					invalidToken, invalidOperations, invalidAdmission.closeCount(), invalidReader.opened, exactRuntimeQueries.Load(), test.wantClose)
			}
			if test.wantClose == 1 && (invalidToken == nil || invalidToken.closed.Load() != 1) {
				t.Fatalf("invalid Rclone token=%+v, want one close", invalidToken)
			}
		})
	}
	rollbackInner := &catalogSessionSpy{}
	rollbackReader := &catalogReaderSpy{session: rollbackInner}
	rollbackRegistry := provider.NewRegistry()
	if err := rollbackRegistry.Register(backupasset.ProviderRclone, provider.Registration{
		Prober: &scriptedProber{}, CatalogReader: rollbackReader,
	}); err != nil {
		t.Fatal(err)
	}
	rollbackAdmission := &catalogAdmissionSpy{mode: publication.AdmissionRollbackSafe}
	rollbackService, err := NewService(Dependencies{
		DB: fixture.db, Foundation: fixture.service.foundation, Registry: rollbackRegistry, Keyring: fixture.service.keyring,
		Now: func() time.Time { return fixture.now }, Admission: rollbackAdmission, Publication: fixture.service,
	})
	if err != nil {
		t.Fatal(err)
	}
	rollbackSession, err := rollbackService.OpenCatalogRead(context.Background(), CatalogPointReadRequest{
		RepositoryID: fixture.repository.ID, RecoveryPointID: attempt.RecoveryPointID,
	})
	if err != nil || rollbackSession == nil {
		t.Fatalf("open rollback-safe immutable Rclone Catalog session=%v err=%v", rollbackSession, err)
	}
	rollbackToken := rollbackAdmission.latestToken()
	rollbackOperations := rollbackAdmission.requestedOperations()
	if rollbackToken == nil || rollbackToken.Operation() != publication.OperationManifest ||
		rollbackToken.Mode() != publication.AdmissionRollbackSafe || rollbackToken.closed.Load() != 0 ||
		len(rollbackOperations) != 1 || rollbackOperations[0] != publication.OperationManifest {
		t.Fatalf("rollback-safe Rclone admission token=%+v operations=%v, want active manifest token", rollbackToken, rollbackOperations)
	}
	if _, err := rollbackSession.ListCanonical(context.Background(), provider.PageRequest{Limit: 10}); err != nil {
		t.Fatalf("rollback-safe Rclone ListCanonical: %v", err)
	}
	if _, err := rollbackSession.Finalize(context.Background()); err != nil {
		t.Fatalf("rollback-safe Rclone Finalize: %v", err)
	}
	if rollbackToken.closed.Load() != 0 || rollbackInner.listCanonicalCalls != 1 || rollbackInner.finalizeCalls != 1 {
		t.Fatalf("rollback-safe Rclone returned session use token=%d list=%d finalize=%d, want active token and one each",
			rollbackToken.closed.Load(), rollbackInner.listCanonicalCalls, rollbackInner.finalizeCalls)
	}
	for _, test := range []struct {
		name      string
		openErr   error
		nilReader bool
		wantErr   error
	}{
		{name: "reader error", openErr: errors.New("FAKE_RCLONE_CATALOG_READER_ERROR_FOR_TEST_ONLY")},
		{name: "nil session", nilReader: true, wantErr: backupasset.ErrInvalidState},
	} {
		t.Run("Rclone "+test.name, func(t *testing.T) {
			errorAdmission := &catalogAdmissionSpy{mode: publication.AdmissionManaged}
			errorInner := &catalogSessionSpy{}
			errorReader := &catalogReaderSpy{session: errorInner, openErr: test.openErr}
			if test.nilReader {
				errorReader.session = nil
			}
			errorRegistry := provider.NewRegistry()
			if err := errorRegistry.Register(backupasset.ProviderRclone, provider.Registration{
				Prober: &scriptedProber{}, CatalogReader: errorReader,
			}); err != nil {
				t.Fatal(err)
			}
			errorService, err := NewService(Dependencies{
				DB: fixture.db, Foundation: fixture.service.foundation, Registry: errorRegistry, Keyring: fixture.service.keyring,
				Now: func() time.Time { return fixture.now }, Admission: errorAdmission, Publication: fixture.service,
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = errorService.OpenCatalogRead(context.Background(), CatalogPointReadRequest{
				RepositoryID: fixture.repository.ID, RecoveryPointID: attempt.RecoveryPointID,
			})
			wantErr := test.wantErr
			if wantErr == nil {
				wantErr = test.openErr
			}
			if err == nil || !errors.Is(err, wantErr) {
				t.Fatalf("Rclone Catalog error=%v, want %v", err, wantErr)
			}
			errorToken := errorAdmission.latestToken()
			if errorToken == nil || errorToken.closed.Load() != 1 || errorAdmission.closeCount() != 1 ||
				errorReader.opened != 1 || errorInner.closed != 0 {
				t.Fatalf("Rclone post-acquire failure token=%+v closes=%d reader opens=%d inner closes=%d, want token one and no inner close",
					errorToken, errorAdmission.closeCount(), errorReader.opened, errorInner.closed)
			}
		})
	}
	if err := rollbackSession.Close(); err != nil {
		t.Fatalf("close rollback-safe immutable Rclone Catalog: %v", err)
	}
	if rollbackToken.closed.Load() != 1 || rollbackInner.closed != 1 {
		t.Fatalf("rollback-safe Rclone close counts token=%d inner=%d, want one each", rollbackToken.closed.Load(), rollbackInner.closed)
	}
	if err := rollbackSession.Close(); err != nil {
		t.Fatalf("repeat close rollback-safe immutable Rclone Catalog: %v", err)
	}
	if rollbackAdmission.closeCount() != 1 || rollbackInner.closed != 1 {
		t.Fatalf("repeat rollback-safe Rclone close changed counts token=%d inner=%d", rollbackAdmission.closeCount(), rollbackInner.closed)
	}
}

func newResticCatalogReadFixture(t *testing.T) (*publicationFixture, string) {
	t.Helper()
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
			Completeness: backupasset.ManifestComplete, EntryCount: 0, LogicalBytes: 0, Fidelity: provider.ResticManifestFidelityV1(),
			HeaderCapturedAt: commit.CaptureStartedAt, ObservedTagDigest: publicationTagDigest(attempt.RequiredTags),
		}, nil
	}
	pointID := resticAttemptForExecution(t, execution).RecoveryPointID
	if outcome, err := fixture.service.ProcessPoint(context.Background(), pointID); err != nil || outcome.State != backupasset.RecoveryPointCommitted {
		t.Fatalf("commit Restic point outcome=%+v err=%v", outcome, err)
	}
	return fixture, pointID
}

func newResticCatalogReadService(
	t *testing.T,
	fixture *publicationFixture,
	admission publication.Admission,
	prober provider.RepositoryProber,
	reader provider.CatalogReader,
) *Service {
	t.Helper()
	registry := provider.NewRegistry()
	if err := registry.Register(backupasset.ProviderRestic, provider.Registration{Prober: prober, CatalogReader: reader}); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(Dependencies{
		DB: fixture.db, Foundation: fixture.service.foundation, Registry: registry, Keyring: fixture.service.keyring,
		Now: func() time.Time { return fixture.now }, Admission: admission, Publication: fixture.service,
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}
func TestCatalogPointReadRejectsRcloneCapabilityEvidenceBeforeProvider(t *testing.T) {
	fixture := newRclonePublicationFixture(t, backupasset.PublicationVersionedPrefix)
	_, point, _ := completeRcloneTestPoint(t, fixture)
	consistency, err := backupasset.DecodePublicationConsistency(point.ConsistencyJSON)
	if err != nil {
		t.Fatal(err)
	}
	consistency.CapabilityRevision++
	encoded, err := backupasset.EncodePublicationConsistency(consistency)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.RecoveryPoint{}).Where("id = ?", point.ID).
		Update("consistency_json", encoded).Error; err != nil {
		t.Fatal(err)
	}
	reader := &catalogReaderSpy{session: &catalogSessionSpy{}}
	registry := provider.NewRegistry()
	if err := registry.Register(backupasset.ProviderRclone, provider.Registration{
		Prober: &scriptedProber{}, CatalogReader: reader,
	}); err != nil {
		t.Fatal(err)
	}
	admission := &catalogAdmissionSpy{mode: publication.AdmissionManaged}
	service, err := NewService(Dependencies{
		DB: fixture.db, Foundation: fixture.service.foundation, Registry: registry, Keyring: fixture.service.keyring,
		Now: func() time.Time { return fixture.now }, Admission: admission, Publication: fixture.service,
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := service.OpenCatalogRead(context.Background(), CatalogPointReadRequest{
		RepositoryID: point.RepositoryID, RecoveryPointID: point.ID,
	})
	if session != nil || !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("Rclone Catalog capability mismatch session=%v err=%v, want nil and ErrConflict", session, err)
	}
	if reader.opened != 0 || admission.closeCount() != 0 {
		t.Fatalf("Rclone Catalog capability mismatch opened=%d admission closes=%d, want no provider/admission work", reader.opened, admission.closeCount())
	}
}
func TestCatalogPointReadRejectsCapabilityRevisionMismatchBeforeAdmissionProvider(t *testing.T) {
	for _, mode := range []backupasset.TaskPublicationMode{
		backupasset.PublicationVersionedPrefix,
		backupasset.PublicationNativeObjectVersions,
	} {
		for _, testCase := range []struct {
			name  string
			point bool
			value int
		}{
			{name: "point_zero", point: true, value: 0},
			{name: "point_drift", point: true, value: 2},
			{name: "repository_zero", value: 0},
			{name: "repository_drift", value: 2},
		} {
			t.Run(string(mode)+"/"+testCase.name, func(t *testing.T) {
				fixture := newRclonePublicationFixture(t, mode)
				_, point, _ := completeRcloneTestPoint(t, fixture)
				reader := &catalogReaderSpy{session: &catalogSessionSpy{}}
				registry := provider.NewRegistry()
				if err := registry.Register(backupasset.ProviderRclone, provider.Registration{
					Prober: &scriptedProber{}, CatalogReader: reader,
				}); err != nil {
					t.Fatal(err)
				}
				admission := &catalogAdmissionSpy{mode: publication.AdmissionManaged}
				service, err := NewService(Dependencies{
					DB: fixture.db, Foundation: fixture.service.foundation, Registry: registry, Keyring: fixture.service.keyring,
					Now: func() time.Time { return fixture.now }, Admission: admission, Publication: fixture.service,
				})
				if err != nil {
					t.Fatal(err)
				}
				value := testCase.value
				if value == 2 {
					value = fixture.repository.CapabilityRevision + 1
				}
				if testCase.point {
					if err := fixture.db.Model(&model.RecoveryPoint{}).Where("id = ?", point.ID).
						Update("capability_revision", value).Error; err != nil {
						t.Fatal(err)
					}
				} else {
					if err := fixture.db.Model(&model.BackupRepository{}).Where("id = ?", fixture.repository.ID).
						Update("capability_revision", value).Error; err != nil {
						t.Fatal(err)
					}
				}
				session, err := service.OpenCatalogRead(context.Background(), CatalogPointReadRequest{
					RepositoryID: point.RepositoryID, RecoveryPointID: point.ID,
				})
				if session != nil || !errors.Is(err, backupasset.ErrConflict) {
					t.Fatalf("capability revision case=%s session=%v err=%v, want nil and ErrConflict", testCase.name, session, err)
				}
				if reader.opened != 0 || admission.closeCount() != 0 {
					t.Fatalf("capability revision case=%s reached provider/admission: opened=%d admission closes=%d",
						testCase.name, reader.opened, admission.closeCount())
				}
			})
		}
	}
}
