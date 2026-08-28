package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/catalog"
	"xirang/backend/internal/backupasset/content"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/backupasset/publication"
	backuprepository "xirang/backend/internal/backupasset/repository"
	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"
	settingservice "xirang/backend/internal/settings"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestBackupContentSafePreviewCrossesActualRepositoryRsyncBrokerAndLiveIssueHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	now := time.Now().UTC().Add(time.Minute).Truncate(time.Second)
	payload := []byte(strings.Repeat("synthetic=value\r\n", 80))
	db := openPhase8BackupContentIntegrationDB(t)
	root := t.TempDir()
	entryPath := filepath.Join(root, "synthetic", "service.conf")
	if err := os.MkdirAll(filepath.Dir(entryPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entryPath, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(entryPath)
	if err != nil {
		t.Fatal(err)
	}

	limits := provider.OperationLimits{
		Timeout: time.Minute, MaxMetadataBytes: 1 << 20, MaxStderrBytes: 64 << 10,
		MaxRecordBytes: 64 << 10, MaxItems: 1000,
	}
	keys := phase8CursorKeys{material: backupasset.DomainKeyMaterial{
		Version: 1, Domain: backupasset.KeyDomainCursorSigning,
		Key: []byte("FAKE_PHASE8_CURSOR_SIGNING_KEY_FOR_TEST_ONLY"),
	}}
	cursors := provider.NewCursorCodec(keys, func() time.Time { return now }, time.Hour)
	actualAdapter, err := provider.NewRsyncAdapter(cursors, limits, 100, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	adapter := &phase8CountingRsyncAdapter{inner: actualAdapter}
	registry := provider.NewRegistry()
	if err := registry.Register(backupasset.ProviderRsync, provider.Registration{
		Prober: adapter, PointLister: adapter, EntryStatter: adapter,
		SequentialReader: adapter, RangeReader: adapter,
	}); err != nil {
		t.Fatal(err)
	}
	settings := settingservice.NewService(db)
	if err := settings.Update("backup_assets.enabled", "true"); err != nil {
		t.Fatal(err)
	}
	foundation := backupasset.NewFoundationService(settings)
	repositoryService, err := backuprepository.NewService(backuprepository.Dependencies{
		DB: db, Foundation: foundation, Registry: registry,
		Admission: phase8ContentAdmission{}, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	node := model.Node{
		Name: "phase8-local", Host: "localhost", Port: 22, Username: "phase8",
		AuthType: "password", Password: "FAKE_PHASE8_NODE_PASSWORD_FOR_TEST_ONLY",
		BasePath: "/data", BackupDir: "phase8",
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	taskEntity := model.Task{
		Name: "phase8-rsync", NodeID: node.ID, ExecutorType: string(backupasset.ProviderRsync),
		RsyncSource: "/synthetic/source", RsyncTarget: root, Status: "pending", Enabled: true,
	}
	if err := db.Create(&taskEntity).Error; err != nil {
		t.Fatal(err)
	}
	connected, err := repositoryService.Connect(
		context.Background(), backuprepository.ConnectRequest{TaskID: taskEntity.ID}, backuprepository.RequestContext{},
	)
	if err != nil || connected.MutablePoint == nil {
		t.Fatalf("connect actual Rsync repository: result=%+v err=%v", connected, err)
	}
	var point model.RecoveryPoint
	if err := db.First(&point, "id = ?", connected.MutablePoint.ID).Error; err != nil {
		t.Fatal(err)
	}
	entryFingerprint := strings.Repeat("e", 64)
	generation := model.CatalogGeneration{
		ID: strings.Repeat("c", 32), RecoveryPointID: point.ID, Generation: 1,
		State: string(catalog.GenerationComplete), IsActive: true,
		SourceFingerprint: point.SourceFingerprint, ExpectedEntryCount: 1, WrittenEntryCount: 1,
		StartedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	finishedAt := now
	generation.FinishedAt = &finishedAt
	if err := db.Create(&generation).Error; err != nil {
		t.Fatal(err)
	}
	entry := model.CatalogEntry{
		GenerationID: generation.ID, EntryID: strings.Repeat("b", 64), RecoveryPointID: point.ID,
		NormalizedPath: "synthetic/service.conf", Name: "service.conf", EntryType: string(backupasset.CatalogEntryFile),
		Size: int64(len(payload)), ModifiedAt: phase8TimePointer(info.ModTime()), MimeType: "application/octet-stream",
		Fingerprint: entryFingerprint, FingerprintStrength: string(catalog.FingerprintStrong),
		EncryptedProviderLocator: `{"version":1,"native":"synthetic/service.conf"}`,
		SecurityState:            "sealed", CreatedAt: now,
	}
	if err := db.Create(&entry).Error; err != nil {
		t.Fatal(err)
	}

	asset := content.AuthorizedAsset{
		Ref:                 backupasset.AssetRef{RecoveryPointID: point.ID, EntryID: entry.EntryID},
		CatalogGenerationID: generation.ID, RepositoryID: connected.Repository.ID,
		Provider: backupasset.ProviderRsync, ProviderCapabilityRevision: int64(point.CapabilityRevision),
		SourceFingerprint: point.SourceFingerprint, EntryFingerprint: entry.Fingerprint,
		FingerprintStrength: entry.FingerprintStrength, Size: entry.Size, ModifiedAt: entry.ModifiedAt,
		MediaType: entry.MimeType, Path: "/synthetic/service.conf", Name: entry.Name, RangeProven: true,
	}
	budget, err := content.NewBudgetService(content.BudgetDependencies{
		DB: db, Now: func() time.Time { return now },
		Limits: func(context.Context) (content.BudgetLimits, error) {
			limits := content.BudgetScopeLimits{WindowBytes: 1 << 20, WindowRequests: 100, MaxInFlight: 10}
			return content.BudgetLimits{Window: time.Minute, Global: limits, Provider: limits, User: limits}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	broker, err := content.NewBroker(content.BrokerDependencies{
		DB: db, Now: func() time.Time { return now },
		FeatureEnabled: func(context.Context) (bool, error) { return true, nil },
		Authorize:      phase8AssetAuthorizer{asset: asset}, Session: phase8SessionValidator{},
		Lease: &phase8LeaseController{now: now}, Source: repositoryService,
		Audit: phase8BrokerAudit{}, Budget: budget,
		Config: func(context.Context) (content.BrokerConfig, error) {
			return content.BrokerConfig{
				TicketTimeout: 5 * time.Second, PreviewTTL: 2 * time.Minute, MediaTTL: 15 * time.Minute,
				IdleTTL: time.Minute, WriteIdleTimeout: 30 * time.Second, LeaseHeartbeat: time.Minute,
				MaxBytesPerRequest: 1 << 20, MaxCumulativeBytes: 4 << 20, MaxRequests: 100, MaxInFlight: 2,
				Classification: content.ClassificationConfig{ScanBytes: 4 << 10},
				Renderer: content.RendererConfig{
					TextBytes: 512, HexBytes: 512, RasterMaxPixels: 1 << 20,
					PDFMaxBytes: 1 << 20, MediaMaxBytes: 1 << 20,
				},
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	capturedService := &phase8CapturingContentService{service: broker}
	handler := NewBackupContentHandler(capturedService, nil, nil, func(context.Context) (BackupContentHandlerConfig, error) {
		return BackupContentHandlerConfig{TicketTimeout: 5 * time.Second}, nil
	})
	router := gin.New()
	router.POST("/api/v1/recovery-points/:recoveryPointId/entries/:entryId/delivery-tickets", func(c *gin.Context) {
		c.Set(middleware.CtxUserID, uint(42))
		c.Set(middleware.CtxUsername, "operator")
		c.Set(middleware.CtxRole, "operator")
		c.Set(middleware.CtxSessionBinding, middleware.SessionBinding{
			JTI: strings.Repeat("f", 32), UserID: 42, Role: "operator", TokenVersion: 1,
			ExpiresAt: now.Add(time.Hour),
		})
		c.Next()
	}, handler.Issue)
	request := httptest.NewRequest(
		http.MethodPost,
		"https://xirang.example/api/v1/recovery-points/"+point.ID+"/entries/"+entry.EntryID+"/delivery-tickets",
		strings.NewReader(`{"schema_version":1,"action":"preview","preview_intent":"safe_preview_v1"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	issueResponse := httptest.NewRecorder()
	router.ServeHTTP(issueResponse, request)
	if issueResponse.Code != http.StatusOK {
		t.Fatalf("live Issue status=%d err=%v body=%s", issueResponse.Code, capturedService.issueErr, issueResponse.Body.String())
	}
	var envelope struct {
		Data content.TicketDescriptor `json:"data"`
	}
	if err := json.Unmarshal(issueResponse.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.Renderer != content.RendererPlainText || envelope.Data.Profile != content.ProfileTextV2 ||
		!envelope.Data.Truncated || envelope.Data.ContentLength != 512 {
		t.Fatalf("resolved ticket descriptor=%+v", envelope.Data)
	}
	cookies := issueResponse.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Value == "" {
		t.Fatalf("live Issue cookies=%v", cookies)
	}
	var grant model.BackupAssetDeliveryGrant
	if err := db.First(&grant).Error; err != nil {
		t.Fatalf("persisted grant: %v", err)
	}
	if grant.Renderer != string(content.RendererPlainText) || grant.Profile != string(content.ProfileTextV2) ||
		!grant.RepresentationTruncated || grant.DeliveryID == "" {
		t.Fatalf("persisted resolved grant=%+v", grant)
	}
	serveResponse := &phase8DeadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	if err := broker.Serve(context.Background(), content.GatewayRequest{
		DeliveryID: grant.DeliveryID, Method: http.MethodGet,
		RawCookie: cookies[0].Name + "=" + cookies[0].Value,
	}, serveResponse); err != nil {
		t.Fatalf("Serve through actual Repository Service: %v", err)
	}
	if serveResponse.Code != http.StatusOK || serveResponse.Body.String() != string(payload[:512]) {
		t.Fatalf("Serve status=%d bytes=%d", serveResponse.Code, serveResponse.Body.Len())
	}
	if adapter.sequentialOpens != 2 {
		t.Fatalf("actual Rsync opens=%d, want one bounded read for Issue and one for Serve", adapter.sequentialOpens)
	}
}

func openPhase8BackupContentIntegrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s-phase8?mode=memory&cache=shared&_busy_timeout=5000&_foreign_keys=ON&_loc=UTC", handlerTestDBName(t))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(
		&model.SystemSetting{}, &model.Node{}, &model.Task{}, &model.BackupRepository{}, &model.RepositoryAccessBinding{},
		&model.TaskRepositoryLink{}, &model.RecoveryPoint{}, &model.RecoveryPointLifecycleAttempt{},
		&model.CatalogGeneration{}, &model.CatalogEntry{},
		&model.BackupAssetDeliveryGrant{}, &model.BackupAssetDeliveryRequest{}, &model.BackupAssetDeliveryUsage{},
	); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_phase8_repository_binding_active ON repository_access_bindings(repository_id) WHERE status = 'active'`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_phase8_task_link_active ON task_repository_links(task_id) WHERE task_id IS NOT NULL AND unlinked_at IS NULL`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_phase8_mutable_point ON recovery_points(repository_id) WHERE semantics = 'mutable_head'`,
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	return db
}

type phase8CursorKeys struct{ material backupasset.DomainKeyMaterial }

func (keys phase8CursorKeys) Active(context.Context, backupasset.KeyDomain) (backupasset.DomainKeyMaterial, error) {
	return keys.material, nil
}

func (keys phase8CursorKeys) ByVersion(context.Context, backupasset.KeyDomain, int) (backupasset.DomainKeyMaterial, error) {
	return keys.material, nil
}

type phase8CountingRsyncAdapter struct {
	inner           *provider.RsyncAdapter
	sequentialOpens int
}

func (adapter *phase8CountingRsyncAdapter) Probe(ctx context.Context, access provider.AccessBinding, limits provider.OperationLimits) (provider.RepositoryObservation, error) {
	return adapter.inner.Probe(ctx, access, limits)
}

func (adapter *phase8CountingRsyncAdapter) ListPoints(ctx context.Context, snapshot provider.ReadSnapshot, request provider.PageRequest) (provider.NativePointPage, error) {
	return adapter.inner.ListPoints(ctx, snapshot, request)
}

func (adapter *phase8CountingRsyncAdapter) StatEntry(ctx context.Context, snapshot provider.ReadSnapshot, point provider.PointLocator, entry provider.EntryLocator) (provider.Entry, error) {
	return adapter.inner.StatEntry(ctx, snapshot, point, entry)
}

func (adapter *phase8CountingRsyncAdapter) OpenSequential(ctx context.Context, snapshot provider.ReadSnapshot, point provider.PointLocator, entry provider.EntryLocator, request provider.ReadRequest) (provider.ReadHandle, provider.ContentStat, error) {
	adapter.sequentialOpens++
	return adapter.inner.OpenSequential(ctx, snapshot, point, entry, request)
}

func (adapter *phase8CountingRsyncAdapter) OpenRange(ctx context.Context, snapshot provider.ReadSnapshot, point provider.PointLocator, entry provider.EntryLocator, byteRange provider.ByteRange) (provider.ReadHandle, provider.ContentStat, error) {
	return adapter.inner.OpenRange(ctx, snapshot, point, entry, byteRange)
}

type phase8ContentAdmission struct{}

func (phase8ContentAdmission) Acquire(_ context.Context, operation publication.ResticOperation) (publication.AdmissionToken, error) {
	return phase8ContentAdmissionToken{operation: operation}, nil
}

type phase8ContentAdmissionToken struct{ operation publication.ResticOperation }

func (phase8ContentAdmissionToken) Generation() uint64 { return 1 }
func (phase8ContentAdmissionToken) Mode() publication.AdmissionMode {
	return publication.AdmissionManaged
}
func (token phase8ContentAdmissionToken) Operation() publication.ResticOperation {
	return token.operation
}
func (phase8ContentAdmissionToken) Close() error { return nil }

type phase8AssetAuthorizer struct{ asset content.AuthorizedAsset }

func (authorizer phase8AssetAuthorizer) Authorize(_ context.Context, _ content.DeliveryActor, ref backupasset.AssetRef, _ content.DeliveryAction) (content.AuthorizedAsset, error) {
	if ref != authorizer.asset.Ref {
		return content.AuthorizedAsset{}, backupasset.ErrNotFound
	}
	return authorizer.asset, nil
}

func (phase8AssetAuthorizer) Reauthorize(context.Context, content.DeliveryActor, content.AuthorizedAsset, content.DeliveryAction) error {
	return nil
}

type phase8SessionValidator struct{}

func (phase8SessionValidator) Validate(context.Context, content.DeliverySession) error { return nil }

type phase8BrokerAudit struct{}

func (phase8BrokerAudit) Write(context.Context, backupasset.AuditEventInput) error { return nil }
func (phase8BrokerAudit) BacklogAvailable(context.Context) error                   { return nil }

type phase8LeaseController struct{ now time.Time }

func (controller *phase8LeaseController) Acquire(_ context.Context, request backupasset.AcquireLeaseRequest) (backupasset.Lease, error) {
	fence := backupasset.LeaseFence{
		LeaseID: request.OwnerID, RecoveryPointID: request.RecoveryPointID,
		HolderType: request.HolderType, OwnerID: request.OwnerID,
		AttemptID: strings.Repeat("7", 32), FenceToken: strings.Repeat("8", 64),
	}
	return controller.lease(fence), nil
}

func (controller *phase8LeaseController) Renew(_ context.Context, fence backupasset.LeaseFence) (backupasset.Lease, error) {
	return controller.lease(fence), nil
}

func (*phase8LeaseController) ValidateFence(context.Context, backupasset.LeaseFence) error {
	return nil
}
func (*phase8LeaseController) Release(context.Context, backupasset.LeaseFence) error { return nil }
func (*phase8LeaseController) Takeover(context.Context, backupasset.TakeoverLeaseRequest) (backupasset.Lease, error) {
	return backupasset.Lease{}, nil
}

func (controller *phase8LeaseController) lease(fence backupasset.LeaseFence) backupasset.Lease {
	return backupasset.Lease{
		ID: fence.LeaseID, RecoveryPointID: fence.RecoveryPointID, HolderType: fence.HolderType,
		OwnerID: fence.OwnerID, Status: backupasset.LeaseActive,
		LeaseExpiresAt: controller.now.Add(5 * time.Minute), AbsoluteDeadline: controller.now.Add(time.Hour),
		LastHeartbeatAt: controller.now, Fence: fence,
	}
}

type phase8DeadlineRecorder struct{ *httptest.ResponseRecorder }

func (*phase8DeadlineRecorder) SetWriteDeadline(time.Time) error { return nil }

func phase8TimePointer(value time.Time) *time.Time {
	value = value.UTC()
	return &value
}

type phase8CapturingContentService struct {
	service  *content.Broker
	issueErr error
}

func (service *phase8CapturingContentService) Issue(ctx context.Context, request content.IssueRequest) (content.IssuedTicket, error) {
	ticket, err := service.service.Issue(ctx, request)
	service.issueErr = err
	return ticket, err
}

func (service *phase8CapturingContentService) Serve(ctx context.Context, request content.GatewayRequest, writer http.ResponseWriter) error {
	return service.service.Serve(ctx, request, writer)
}

func (service *phase8CapturingContentService) RevokeSession(ctx context.Context, jti, reason string) error {
	return service.service.RevokeSession(ctx, jti, reason)
}
