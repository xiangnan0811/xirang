package backupasset

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"xirang/backend/internal/model"
	"xirang/backend/internal/settings"
)

type SettingsReader interface {
	GetEffective(key string) string
}

type BackupAssetSettingsSnapshotReader interface {
	BackupAssetSettingsSnapshot() (map[string]string, error)
}

type FoundationService struct {
	settings SettingsReader
}

type ProviderConfig struct {
	OperationTimeout   time.Duration
	MaxConcurrency     int
	MetadataLimitBytes int64
}

type RetentionConfig struct {
	Enabled           bool
	ReconcileInterval time.Duration
	BatchSize         int
	DrainTimeout      time.Duration
}

type CatalogConfig struct {
	Enabled           bool
	BatchSize         int
	BuildTimeout      time.Duration
	ReconcileInterval time.Duration
	MaxConcurrency    int
	MaxEntries        int64
	Lease             LeaseConfig
}

type SearchConfig struct {
	Enabled           bool
	ReconcileInterval time.Duration
	BuildTimeout      time.Duration
	BatchSize         int
	MaxConcurrency    int
	ASTMaxDepth       int
	ASTMaxNodes       int
	ValuesPerNode     int
	BodyMaxBytes      int
	ValueMaxBytes     int
	CandidateLimit    int
	QueryTimeout      time.Duration
	PageSizeMax       int
	SuggestionLimit   int
	MaxDocuments      int64
	Lease             LeaseConfig
}

type OverlayConfig struct {
	Enabled                bool
	SavedSearchQuota       int64
	FavoriteQuota          int64
	TagDefinitionQuota     int64
	TagAssignmentQuota     int64
	BulkMaxItems           int
	LabelMaxBytes          int
	RecentQuota            int64
	RecentRetention        time.Duration
	RecentWritesPerMinute  int64
	IdempotencyTTL         time.Duration
	IdempotencyKeyMaxBytes int
}

type ContentScopeConfig struct {
	MaxConcurrency int64
	WindowBytes    int64
	WindowRequests int64
}

type ContentMemoryConfig struct {
	ObjectBytes   int64
	UserBytes     int64
	ProviderBytes int64
	GlobalBytes   int64
}

type ContentCacheConfig struct {
	Enabled       bool
	Root          string
	ChunkBytes    int64
	ObjectBytes   int64
	UserBytes     int64
	ProviderBytes int64
	GlobalBytes   int64
	ObjectFiles   int64
	UserFiles     int64
	ProviderFiles int64
	GlobalFiles   int64
	IdleTTL       time.Duration
	AbsoluteTTL   time.Duration
}

type ContentConfig struct {
	Enabled                     bool
	PreviewTTL                  time.Duration
	MediaTTL                    time.Duration
	IdleTTL                     time.Duration
	WriteIdleTimeout            time.Duration
	LeaseHeartbeat              time.Duration
	TicketTimeout               time.Duration
	RequestMaxBytes             int64
	CumulativeMaxBytes          int64
	MaxRequests                 int64
	GrantMaxInFlight            int64
	RateWindow                  time.Duration
	User                        ContentScopeConfig
	Provider                    ContentScopeConfig
	Global                      ContentScopeConfig
	ClassificationScanBytes     int64
	TextPreviewBytes            int64
	HexPreviewBytes             int64
	RasterMaxPixels             int64
	Memory                      ContentMemoryConfig
	Cache                       ContentCacheConfig
	ReconcileInterval           time.Duration
	ReconcileBatchSize          int
	AuditBacklogMax             int64
	AllowInsecureLoopback       bool
	AllowInsecurePrivateNetwork bool
}

type ProcessingInputConfig struct {
	RequestMaxBytes    int64
	CumulativeMaxBytes int64
	MaxRequests        int64
	MaxInFlight        int64
}

type ProcessingSinkConfig struct {
	MaxArtifacts     int
	ArtifactMaxBytes int64
	TotalMaxBytes    int64
}

type ProcessingLocalWorkerConfig struct {
	Enabled bool
	Socket  string
}

type ProcessingRemoteWorkerConfig struct {
	Enabled        bool
	ListenAddress  string
	ServerCertFile string
	ServerKeyFile  string
	ClientCAFile   string
	TrustDomain    string
}

type ProcessingDerivedStoreConfig struct {
	Root               string
	ChunkBytes         int64
	BlobMaxBytes       int64
	GlobalMaxBytes     int64
	ReconcileInterval  time.Duration
	ReconcileBatchSize int
}

type ProcessingUpdaterConfig struct {
	Enabled       bool
	OnlineEnabled bool
	OnlineOrigins []string
}

type ProcessingBackfillConfig struct {
	Paused                bool
	BatchSize             int
	InspectedLimit        int
	JobsPerHour           int
	BytesPerHour          int64
	ProviderConcurrency   int
	CapabilityConcurrency int
	RecentWindow          time.Duration
	HistoryAgingStep      time.Duration
}

type ProcessingConfig struct {
	Enabled              bool
	QueueMax             int
	InteractiveSlots     int
	BackgroundSlots      int
	PullLease            time.Duration
	PullHeartbeat        time.Duration
	AttemptTimeout       time.Duration
	RetryMax             int
	RetryBase            time.Duration
	RetryMaxDelay        time.Duration
	Input                ProcessingInputConfig
	Sink                 ProcessingSinkConfig
	ProtocolJSONMaxBytes int64
	LocalWorker          ProcessingLocalWorkerConfig
	RemoteWorker         ProcessingRemoteWorkerConfig
	Updater              ProcessingUpdaterConfig
	SecretClassify       bool
	Backfill             ProcessingBackfillConfig
	DerivedStore         ProcessingDerivedStoreConfig
}

type ExportTicketConfig struct {
	TTL                time.Duration
	MaxRequests        int
	MaxInFlight        int
	MaxCumulativeBytes int64
}

type ExportQuotaConfig struct {
	UserStoreBytes   int64
	GlobalStoreBytes int64
}

type ExportArchiveConfig struct {
	MemberTTL           time.Duration
	MaxExpandedBytes    int64
	MemberMaxBytes      int64
	MaxEntries          int
	MaxDepth            int
	MaxCompressionRatio int
	MaxDuration         time.Duration
}

type ExportConfig struct {
	Enabled                bool
	Root                   string
	DefaultProfile         string
	ChunkBytes             int64
	MaxItems               int
	MaxSourcePoints        int
	MaxItemBytes           int64
	MaxLogicalBytes        int64
	MaxProviderBytes       int64
	MaxCiphertextBytes     int64
	UserActiveJobs         int
	GlobalActiveJobs       int
	WorkerConcurrency      int
	MaxOpenReaders         int
	MaxDuration            time.Duration
	MaxAttempts            int
	RetryBase              time.Duration
	RetryMaxDelay          time.Duration
	LeaseTTL               time.Duration
	LeaseRenewMargin       time.Duration
	ReadyTTL               time.Duration
	IdempotencyTTL         time.Duration
	IdempotencyKeyMaxBytes int
	SummaryTTL             time.Duration
	Ticket                 ExportTicketConfig
	Quota                  ExportQuotaConfig
	GCCadence              time.Duration
	ReconcileBatchSize     int
	Archive                ExportArchiveConfig
}

type RecoveryAuthorizationConfig struct {
	ReceiptReplayTTL       time.Duration
	WriteGrantTTL          time.Duration
	DeleteGrantTTL         time.Duration
	ReceiptReaperCadence   time.Duration
	ReceiptReaperBatchSize int
}

type RecoveryConfig struct {
	RecoveryAuthorizationConfig
	Enabled                    bool
	PreflightTTL               time.Duration
	MaxSelectionItems          int
	MaxLogicalBytes            int64
	WorkerConcurrency          int
	LeaseTTL                   time.Duration
	LeaseRenewMargin           time.Duration
	TakeoverCadence            time.Duration
	RetryBase                  time.Duration
	RetryMaxDelay              time.Duration
	ScanLimit                  int
	ExecutionTimeout           time.Duration
	ResultDefaultTTL           time.Duration
	ResultRetainHardCap        time.Duration
	ResultReadPermitTTL        time.Duration
	ResultDrainTimeout         time.Duration
	CleanupCadence             time.Duration
	CleanupBatchSize           int
	CleanupLeaseTTL            time.Duration
	CleanupRetryBase           time.Duration
	CleanupRetryMaxDelay       time.Duration
	ReconciliationFindingLimit int
}

// FoundationTransitionConfig is the complete typed configuration needed to
// prepare one backup-assets enable transition without rereading settings.
type FoundationTransitionConfig struct {
	Enabled  bool
	Content  ContentConfig
	Search   SearchConfig
	Overlay  OverlayConfig
	Export   ExportConfig
	Recovery RecoveryConfig
}

func NewFoundationService(reader SettingsReader) *FoundationService {
	return &FoundationService{settings: reader}
}

func (service *FoundationService) Enabled() bool {
	enabled, err := service.FeatureEnabled()
	return err == nil && enabled
}

// FeatureEnabled returns the effective dynamic feature value together with a
// validation error. Callers that enforce admission transitions must not treat a
// settings-resolution failure as a harmless disabled value.
func (service *FoundationService) FeatureEnabled() (bool, error) {
	values, err := service.effectiveFoundationValues()
	if err != nil {
		return false, err
	}
	enabled, err := strconv.ParseBool(strings.TrimSpace(values["backup_assets.enabled"]))
	if err != nil {
		return false, fmt.Errorf("%w: parse backup_assets.enabled: %v", ErrInvalidState, err)
	}
	return enabled, nil
}

func (service *FoundationService) RetentionConfig() (RetentionConfig, error) {
	values, err := service.effectiveFoundationValues()
	if err != nil {
		return RetentionConfig{}, err
	}
	enabled, err := parseFoundationBool(values, "backup_assets.enabled")
	if err != nil {
		return RetentionConfig{}, err
	}
	reconcileInterval, err := parseFoundationDuration(values, "backup_assets.retention_reconcile_interval")
	if err != nil {
		return RetentionConfig{}, err
	}
	batchSize, err := parseFoundationInt(values, "backup_assets.retention_batch_size")
	if err != nil {
		return RetentionConfig{}, err
	}
	drainTimeout, err := parseFoundationDuration(values, "backup_assets.retention_drain_timeout")
	if err != nil {
		return RetentionConfig{}, err
	}
	if reconcileInterval < 30*time.Second || reconcileInterval > 24*time.Hour || batchSize < 1 || batchSize > 1000 ||
		drainTimeout < 5*time.Second || drainTimeout > 30*time.Minute {
		return RetentionConfig{}, fmt.Errorf("%w: invalid Retention settings", ErrInvalidState)
	}
	return RetentionConfig{
		Enabled:           enabled,
		ReconcileInterval: reconcileInterval,
		BatchSize:         batchSize,
		DrainTimeout:      drainTimeout,
	}, nil
}

func (service *FoundationService) LeaseConfig() (LeaseConfig, error) {
	values, err := service.effectiveFoundationValues()
	if err != nil {
		return LeaseConfig{}, err
	}
	duration, err := parseFoundationDuration(values, "backup_assets.lease_duration")
	if err != nil {
		return LeaseConfig{}, err
	}
	heartbeat, err := parseFoundationDuration(values, "backup_assets.lease_heartbeat")
	if err != nil {
		return LeaseConfig{}, err
	}
	absoluteDeadline, err := parseFoundationDuration(values, "backup_assets.lease_absolute_deadline")
	if err != nil {
		return LeaseConfig{}, err
	}
	return LeaseConfig{Duration: duration, Heartbeat: heartbeat, AbsoluteDeadline: absoluteDeadline}, nil
}

func (service *FoundationService) ProviderConfig() (ProviderConfig, error) {
	values, err := service.effectiveFoundationValues()
	if err != nil {
		return ProviderConfig{}, err
	}
	operationTimeout, err := parseFoundationDuration(values, "backup_assets.provider_operation_timeout")
	if err != nil {
		return ProviderConfig{}, err
	}
	maxConcurrency, err := parseFoundationInt(values, "backup_assets.provider_max_concurrency")
	if err != nil {
		return ProviderConfig{}, err
	}
	metadataLimitBytes, err := parseFoundationInt64(values, "backup_assets.provider_metadata_limit_bytes")
	if err != nil {
		return ProviderConfig{}, err
	}
	return ProviderConfig{
		OperationTimeout:   operationTimeout,
		MaxConcurrency:     maxConcurrency,
		MetadataLimitBytes: metadataLimitBytes,
	}, nil
}

func (service *FoundationService) CatalogConfig() (CatalogConfig, error) {
	values, err := service.effectiveFoundationValues()
	if err != nil {
		return CatalogConfig{}, err
	}
	enabled, err := strconv.ParseBool(strings.TrimSpace(values["backup_assets.enabled"]))
	if err != nil {
		return CatalogConfig{}, fmt.Errorf("%w: parse backup_assets.enabled: %v", ErrInvalidState, err)
	}
	batchSize, err := parseFoundationInt(values, "backup_assets.catalog_batch_size")
	if err != nil {
		return CatalogConfig{}, err
	}
	buildTimeout, err := parseFoundationDuration(values, "backup_assets.catalog_build_timeout")
	if err != nil {
		return CatalogConfig{}, err
	}
	reconcileInterval, err := parseFoundationDuration(values, "backup_assets.repository_reconcile_interval")
	if err != nil {
		return CatalogConfig{}, err
	}
	maxConcurrency, err := parseFoundationInt(values, "backup_assets.provider_max_concurrency")
	if err != nil {
		return CatalogConfig{}, err
	}
	maxEntries, err := parseFoundationInt64(values, "backup_assets.manifest_max_entries")
	if err != nil {
		return CatalogConfig{}, err
	}
	leaseDuration, err := parseFoundationDuration(values, "backup_assets.lease_duration")
	if err != nil {
		return CatalogConfig{}, err
	}
	leaseHeartbeat, err := parseFoundationDuration(values, "backup_assets.lease_heartbeat")
	if err != nil {
		return CatalogConfig{}, err
	}
	leaseDeadline, err := parseFoundationDuration(values, "backup_assets.lease_absolute_deadline")
	if err != nil {
		return CatalogConfig{}, err
	}
	if batchSize < 1 || batchSize > 100000 || buildTimeout < time.Minute || buildTimeout > 24*time.Hour ||
		reconcileInterval < time.Minute || reconcileInterval > 24*time.Hour || maxConcurrency < 1 || maxConcurrency > 32 ||
		maxEntries < 1 || leaseDuration < 30*time.Second || leaseDuration > 30*time.Minute ||
		leaseHeartbeat < 10*time.Second || leaseHeartbeat >= leaseDuration || leaseDeadline < leaseDuration || leaseDeadline > 168*time.Hour {
		return CatalogConfig{}, fmt.Errorf("%w: invalid Catalog settings", ErrInvalidState)
	}
	return CatalogConfig{
		Enabled: enabled, BatchSize: batchSize, BuildTimeout: buildTimeout, ReconcileInterval: reconcileInterval,
		MaxConcurrency: maxConcurrency, MaxEntries: maxEntries,
		Lease: LeaseConfig{Duration: leaseDuration, Heartbeat: leaseHeartbeat, AbsoluteDeadline: leaseDeadline},
	}, nil
}

func (service *FoundationService) SearchOverlayConfig() (SearchConfig, OverlayConfig, error) {
	values, err := service.foundationValuesSnapshot()
	if err != nil {
		return SearchConfig{}, OverlayConfig{}, err
	}
	return SearchOverlayConfigFromValues(values)
}

// SearchOverlayConfigFromValues parses Search and Overlay from one complete,
// validated foundation snapshot without reading settings.
func SearchOverlayConfigFromValues(values map[string]string) (SearchConfig, OverlayConfig, error) {
	if err := validateCompleteFoundationValues(values); err != nil {
		return SearchConfig{}, OverlayConfig{}, err
	}
	return searchOverlayConfigFromValidatedValues(values)
}

func searchOverlayConfigFromValidatedValues(values map[string]string) (SearchConfig, OverlayConfig, error) {
	var err error
	enabled, err := parseFoundationBool(values, "backup_assets.enabled")
	if err != nil {
		return SearchConfig{}, OverlayConfig{}, err
	}
	reconcileInterval, err := parseFoundationDuration(values, "backup_assets.search_reconcile_interval")
	if err != nil {
		return SearchConfig{}, OverlayConfig{}, err
	}
	buildTimeout, err := parseFoundationDuration(values, "backup_assets.search_build_timeout")
	if err != nil {
		return SearchConfig{}, OverlayConfig{}, err
	}
	batchSize, err := parseFoundationInt(values, "backup_assets.search_batch_size")
	if err != nil {
		return SearchConfig{}, OverlayConfig{}, err
	}
	maxConcurrency, err := parseFoundationInt(values, "backup_assets.search_max_concurrency")
	if err != nil {
		return SearchConfig{}, OverlayConfig{}, err
	}
	astMaxDepth, err := parseFoundationInt(values, "backup_assets.search_ast_max_depth")
	if err != nil {
		return SearchConfig{}, OverlayConfig{}, err
	}
	astMaxNodes, err := parseFoundationInt(values, "backup_assets.search_ast_max_nodes")
	if err != nil {
		return SearchConfig{}, OverlayConfig{}, err
	}
	valuesPerNode, err := parseFoundationInt(values, "backup_assets.search_values_per_node")
	if err != nil {
		return SearchConfig{}, OverlayConfig{}, err
	}
	bodyMaxBytes, err := parseFoundationInt(values, "backup_assets.search_body_max_bytes")
	if err != nil {
		return SearchConfig{}, OverlayConfig{}, err
	}
	valueMaxBytes, err := parseFoundationInt(values, "backup_assets.search_value_max_bytes")
	if err != nil {
		return SearchConfig{}, OverlayConfig{}, err
	}
	candidateLimit, err := parseFoundationInt(values, "backup_assets.search_candidate_limit")
	if err != nil {
		return SearchConfig{}, OverlayConfig{}, err
	}
	queryTimeout, err := parseFoundationDuration(values, "backup_assets.search_query_timeout")
	if err != nil {
		return SearchConfig{}, OverlayConfig{}, err
	}
	pageSizeMax, err := parseFoundationInt(values, "backup_assets.search_page_size_max")
	if err != nil {
		return SearchConfig{}, OverlayConfig{}, err
	}
	suggestionLimit, err := parseFoundationInt(values, "backup_assets.search_suggestion_limit")
	if err != nil {
		return SearchConfig{}, OverlayConfig{}, err
	}
	maxDocuments, err := parseFoundationInt64(values, "backup_assets.manifest_max_entries")
	if err != nil {
		return SearchConfig{}, OverlayConfig{}, err
	}
	leaseDuration, err := parseFoundationDuration(values, "backup_assets.lease_duration")
	if err != nil {
		return SearchConfig{}, OverlayConfig{}, err
	}
	leaseHeartbeat, err := parseFoundationDuration(values, "backup_assets.lease_heartbeat")
	if err != nil {
		return SearchConfig{}, OverlayConfig{}, err
	}
	leaseDeadline, err := parseFoundationDuration(values, "backup_assets.lease_absolute_deadline")
	if err != nil {
		return SearchConfig{}, OverlayConfig{}, err
	}

	savedSearchQuota, err := parseFoundationInt64(values, "backup_assets.saved_search_quota")
	if err != nil {
		return SearchConfig{}, OverlayConfig{}, err
	}
	favoriteQuota, err := parseFoundationInt64(values, "backup_assets.favorite_quota")
	if err != nil {
		return SearchConfig{}, OverlayConfig{}, err
	}
	tagDefinitionQuota, err := parseFoundationInt64(values, "backup_assets.tag_definition_quota")
	if err != nil {
		return SearchConfig{}, OverlayConfig{}, err
	}
	tagAssignmentQuota, err := parseFoundationInt64(values, "backup_assets.tag_assignment_quota")
	if err != nil {
		return SearchConfig{}, OverlayConfig{}, err
	}
	bulkMaxItems, err := parseFoundationInt(values, "backup_assets.overlay_bulk_max_items")
	if err != nil {
		return SearchConfig{}, OverlayConfig{}, err
	}
	labelMaxBytes, err := parseFoundationInt(values, "backup_assets.overlay_label_max_bytes")
	if err != nil {
		return SearchConfig{}, OverlayConfig{}, err
	}
	recentQuota, err := parseFoundationInt64(values, "backup_assets.recent_quota")
	if err != nil {
		return SearchConfig{}, OverlayConfig{}, err
	}
	recentRetention, err := parseFoundationDuration(values, "backup_assets.recent_retention")
	if err != nil {
		return SearchConfig{}, OverlayConfig{}, err
	}
	recentWritesPerMinute, err := parseFoundationInt64(values, "backup_assets.recent_writes_per_minute")
	if err != nil {
		return SearchConfig{}, OverlayConfig{}, err
	}
	idempotencyTTL, err := parseFoundationDuration(values, "backup_assets.idempotency_ttl")
	if err != nil {
		return SearchConfig{}, OverlayConfig{}, err
	}
	idempotencyKeyMaxBytes, err := parseFoundationInt(values, "backup_assets.idempotency_key_max_bytes")
	if err != nil {
		return SearchConfig{}, OverlayConfig{}, err
	}

	searchConfig := SearchConfig{
		Enabled: enabled, ReconcileInterval: reconcileInterval, BuildTimeout: buildTimeout,
		BatchSize: batchSize, MaxConcurrency: maxConcurrency, ASTMaxDepth: astMaxDepth, ASTMaxNodes: astMaxNodes,
		ValuesPerNode: valuesPerNode, BodyMaxBytes: bodyMaxBytes, ValueMaxBytes: valueMaxBytes,
		CandidateLimit: candidateLimit, QueryTimeout: queryTimeout, PageSizeMax: pageSizeMax, SuggestionLimit: suggestionLimit,
		MaxDocuments: maxDocuments, Lease: LeaseConfig{Duration: leaseDuration, Heartbeat: leaseHeartbeat, AbsoluteDeadline: leaseDeadline},
	}
	overlayConfig := OverlayConfig{
		Enabled: enabled, SavedSearchQuota: savedSearchQuota, FavoriteQuota: favoriteQuota,
		TagDefinitionQuota: tagDefinitionQuota, TagAssignmentQuota: tagAssignmentQuota,
		BulkMaxItems: bulkMaxItems, LabelMaxBytes: labelMaxBytes, RecentQuota: recentQuota,
		RecentRetention: recentRetention, RecentWritesPerMinute: recentWritesPerMinute,
		IdempotencyTTL: idempotencyTTL, IdempotencyKeyMaxBytes: idempotencyKeyMaxBytes,
	}
	return searchConfig, overlayConfig, nil
}

func (service *FoundationService) ContentConfig() (ContentConfig, error) {
	values, err := service.foundationValuesSnapshot()
	if err != nil {
		return ContentConfig{}, err
	}
	return ContentConfigFromValues(values)
}

// ContentConfigFromValues parses Content from one complete, validated
// foundation snapshot without reading settings.
func ContentConfigFromValues(values map[string]string) (ContentConfig, error) {
	if err := validateCompleteFoundationValues(values); err != nil {
		return ContentConfig{}, err
	}
	return contentConfigFromValidatedValues(values)
}

func contentConfigFromValidatedValues(values map[string]string) (ContentConfig, error) {
	var err error
	result := ContentConfig{}
	if result.Enabled, err = parseFoundationBool(values, "backup_assets.enabled"); err != nil {
		return ContentConfig{}, err
	}
	for key, target := range map[string]*time.Duration{
		"backup_assets.content_preview_ttl":        &result.PreviewTTL,
		"backup_assets.content_media_ttl":          &result.MediaTTL,
		"backup_assets.content_idle_ttl":           &result.IdleTTL,
		"backup_assets.content_write_idle_timeout": &result.WriteIdleTimeout,
		"backup_assets.lease_heartbeat":            &result.LeaseHeartbeat,
		"backup_assets.content_ticket_timeout":     &result.TicketTimeout,
		"backup_assets.content_rate_window":        &result.RateWindow,
		"backup_assets.content_cache_idle_ttl":     &result.Cache.IdleTTL,
		"backup_assets.content_cache_absolute_ttl": &result.Cache.AbsoluteTTL,
		"backup_assets.content_reconcile_interval": &result.ReconcileInterval,
	} {
		if *target, err = parseFoundationDuration(values, key); err != nil {
			return ContentConfig{}, err
		}
	}
	for key, target := range map[string]*int64{
		"backup_assets.content_request_max_bytes":         &result.RequestMaxBytes,
		"backup_assets.content_cumulative_max_bytes":      &result.CumulativeMaxBytes,
		"backup_assets.content_max_requests":              &result.MaxRequests,
		"backup_assets.content_grant_max_in_flight":       &result.GrantMaxInFlight,
		"backup_assets.content_user_max_concurrency":      &result.User.MaxConcurrency,
		"backup_assets.content_provider_max_concurrency":  &result.Provider.MaxConcurrency,
		"backup_assets.content_global_max_concurrency":    &result.Global.MaxConcurrency,
		"backup_assets.content_user_window_bytes":         &result.User.WindowBytes,
		"backup_assets.content_provider_window_bytes":     &result.Provider.WindowBytes,
		"backup_assets.content_global_window_bytes":       &result.Global.WindowBytes,
		"backup_assets.content_user_window_requests":      &result.User.WindowRequests,
		"backup_assets.content_provider_window_requests":  &result.Provider.WindowRequests,
		"backup_assets.content_global_window_requests":    &result.Global.WindowRequests,
		"backup_assets.content_classification_scan_bytes": &result.ClassificationScanBytes,
		"backup_assets.content_text_preview_bytes":        &result.TextPreviewBytes,
		"backup_assets.content_hex_preview_bytes":         &result.HexPreviewBytes,
		"backup_assets.content_raster_max_pixels":         &result.RasterMaxPixels,
		"backup_assets.content_memory_object_bytes":       &result.Memory.ObjectBytes,
		"backup_assets.content_memory_user_bytes":         &result.Memory.UserBytes,
		"backup_assets.content_memory_provider_bytes":     &result.Memory.ProviderBytes,
		"backup_assets.content_memory_global_bytes":       &result.Memory.GlobalBytes,
		"backup_assets.content_cache_chunk_bytes":         &result.Cache.ChunkBytes,
		"backup_assets.content_cache_object_bytes":        &result.Cache.ObjectBytes,
		"backup_assets.content_cache_user_bytes":          &result.Cache.UserBytes,
		"backup_assets.content_cache_provider_bytes":      &result.Cache.ProviderBytes,
		"backup_assets.content_cache_global_bytes":        &result.Cache.GlobalBytes,
		"backup_assets.content_cache_object_files":        &result.Cache.ObjectFiles,
		"backup_assets.content_cache_user_files":          &result.Cache.UserFiles,
		"backup_assets.content_cache_provider_files":      &result.Cache.ProviderFiles,
		"backup_assets.content_cache_global_files":        &result.Cache.GlobalFiles,
		"backup_assets.content_audit_backlog_max":         &result.AuditBacklogMax,
	} {
		if *target, err = parseFoundationInt64(values, key); err != nil {
			return ContentConfig{}, err
		}
	}
	if result.ReconcileBatchSize, err = parseFoundationInt(values, "backup_assets.content_reconcile_batch_size"); err != nil {
		return ContentConfig{}, err
	}
	if result.Cache.Enabled, err = parseFoundationBool(values, "backup_assets.content_cache_enabled"); err != nil {
		return ContentConfig{}, err
	}
	result.Cache.Root = strings.TrimSpace(values["backup_assets.content_cache_root"])
	if result.AllowInsecureLoopback, err = parseFoundationBool(values, "backup_assets.content_allow_insecure_loopback"); err != nil {
		return ContentConfig{}, err
	}
	if result.AllowInsecurePrivateNetwork, err = parseFoundationBool(values, "backup_assets.content_allow_insecure_private_network"); err != nil {
		return ContentConfig{}, err
	}
	return result, nil
}

func (service *FoundationService) ProcessingConfig() (ProcessingConfig, error) {
	values, err := service.atomicFoundationValues()
	if err != nil {
		return ProcessingConfig{}, err
	}
	result := ProcessingConfig{}
	for _, field := range []struct {
		key    string
		target *bool
	}{
		{"backup_assets.enabled", &result.Enabled},
		{"backup_assets.worker_local_enabled", &result.LocalWorker.Enabled},
		{"backup_assets.worker_remote_enabled", &result.RemoteWorker.Enabled},
		{"backup_assets.worker_updater_enabled", &result.Updater.Enabled},
		{"backup_assets.worker_updater_online_enabled", &result.Updater.OnlineEnabled},
		{"backup_assets.processing_secret_classify", &result.SecretClassify},
		{"backup_assets.processing_backfill_paused", &result.Backfill.Paused},
	} {
		if *field.target, err = parseFoundationBool(values, field.key); err != nil {
			return ProcessingConfig{}, err
		}
	}
	for _, field := range []struct {
		key    string
		target *time.Duration
	}{
		{"backup_assets.processing_pull_lease", &result.PullLease},
		{"backup_assets.processing_pull_heartbeat", &result.PullHeartbeat},
		{"backup_assets.processing_attempt_timeout", &result.AttemptTimeout},
		{"backup_assets.processing_retry_base", &result.RetryBase},
		{"backup_assets.processing_retry_max_delay", &result.RetryMaxDelay},
		{"backup_assets.derived_store_reconcile_interval", &result.DerivedStore.ReconcileInterval},
		{"backup_assets.processing_backfill_recent_window", &result.Backfill.RecentWindow},
		{"backup_assets.processing_backfill_history_aging_step", &result.Backfill.HistoryAgingStep},
	} {
		if *field.target, err = parseFoundationDuration(values, field.key); err != nil {
			return ProcessingConfig{}, err
		}
	}
	for _, field := range []struct {
		key    string
		target *int
	}{
		{"backup_assets.processing_queue_max", &result.QueueMax},
		{"backup_assets.processing_interactive_slots", &result.InteractiveSlots},
		{"backup_assets.processing_background_slots", &result.BackgroundSlots},
		{"backup_assets.processing_retry_max", &result.RetryMax},
		{"backup_assets.processing_sink_max_artifacts", &result.Sink.MaxArtifacts},
		{"backup_assets.derived_store_reconcile_batch_size", &result.DerivedStore.ReconcileBatchSize},
		{"backup_assets.processing_backfill_batch_size", &result.Backfill.BatchSize},
		{"backup_assets.processing_backfill_jobs_per_hour", &result.Backfill.JobsPerHour},
		{"backup_assets.processing_backfill_provider_concurrency", &result.Backfill.ProviderConcurrency},
		{"backup_assets.processing_backfill_capability_concurrency", &result.Backfill.CapabilityConcurrency},
	} {
		if *field.target, err = parseFoundationInt(values, field.key); err != nil {
			return ProcessingConfig{}, err
		}
	}
	for _, field := range []struct {
		key    string
		target *int64
	}{
		{"backup_assets.processing_input_request_max_bytes", &result.Input.RequestMaxBytes},
		{"backup_assets.processing_input_cumulative_max_bytes", &result.Input.CumulativeMaxBytes},
		{"backup_assets.processing_input_max_requests", &result.Input.MaxRequests},
		{"backup_assets.processing_input_max_in_flight", &result.Input.MaxInFlight},
		{"backup_assets.processing_sink_artifact_max_bytes", &result.Sink.ArtifactMaxBytes},
		{"backup_assets.processing_sink_total_max_bytes", &result.Sink.TotalMaxBytes},
		{"backup_assets.processing_protocol_json_max_bytes", &result.ProtocolJSONMaxBytes},
		{"backup_assets.derived_store_chunk_bytes", &result.DerivedStore.ChunkBytes},
		{"backup_assets.derived_store_blob_max_bytes", &result.DerivedStore.BlobMaxBytes},
		{"backup_assets.derived_store_global_max_bytes", &result.DerivedStore.GlobalMaxBytes},
		{"backup_assets.processing_backfill_bytes_per_hour", &result.Backfill.BytesPerHour},
	} {
		if *field.target, err = parseFoundationInt64(values, field.key); err != nil {
			return ProcessingConfig{}, err
		}
	}
	result.LocalWorker.Socket = strings.TrimSpace(values["backup_assets.worker_local_socket"])
	result.RemoteWorker.ListenAddress = strings.TrimSpace(values["backup_assets.worker_remote_listen_addr"])
	result.RemoteWorker.ServerCertFile = strings.TrimSpace(values["backup_assets.worker_remote_server_cert_file"])
	result.RemoteWorker.ServerKeyFile = strings.TrimSpace(values["backup_assets.worker_remote_server_key_file"])
	result.RemoteWorker.ClientCAFile = strings.TrimSpace(values["backup_assets.worker_remote_client_ca_file"])
	result.RemoteWorker.TrustDomain = strings.TrimSpace(values["backup_assets.worker_remote_trust_domain"])
	if origins := strings.TrimSpace(values["backup_assets.worker_updater_online_origins"]); origins != "" {
		result.Updater.OnlineOrigins = strings.Split(origins, ",")
	}
	result.DerivedStore.Root = strings.TrimSpace(values["backup_assets.derived_store_root"])
	return result, nil
}

func (service *FoundationService) ExportConfig() (ExportConfig, error) {
	values, err := service.atomicFoundationValues()
	if err != nil {
		return ExportConfig{}, err
	}
	return ExportConfigFromValues(values)
}

// RecoveryAuthorizationConfig returns the coupled receipt/grant deadlines and
// bounded maintenance limits from one validated Foundation snapshot.
func (service *FoundationService) RecoveryAuthorizationConfig() (RecoveryAuthorizationConfig, error) {
	values, err := service.atomicFoundationValues()
	if err != nil {
		return RecoveryAuthorizationConfig{}, err
	}
	result := RecoveryAuthorizationConfig{}
	for _, field := range []struct {
		key    string
		target *time.Duration
	}{
		{"backup_assets.recovery.receipt_replay_ttl", &result.ReceiptReplayTTL},
		{"backup_assets.recovery.write_grant_ttl", &result.WriteGrantTTL},
		{"backup_assets.recovery.delete_grant_ttl", &result.DeleteGrantTTL},
		{"backup_assets.recovery.receipt_reaper_cadence", &result.ReceiptReaperCadence},
	} {
		if *field.target, err = parseFoundationDuration(values, field.key); err != nil {
			return RecoveryAuthorizationConfig{}, err
		}
	}
	if result.ReceiptReaperBatchSize, err = parseFoundationInt(values, "backup_assets.recovery.receipt_reaper_batch_size"); err != nil {
		return RecoveryAuthorizationConfig{}, err
	}
	return result, nil
}

func (service *FoundationService) RecoveryConfig() (RecoveryConfig, error) {
	values, err := service.atomicFoundationValues()
	if err != nil {
		return RecoveryConfig{}, err
	}
	return recoveryConfigFromValidatedValues(values)
}

// RecoveryConfigFromValues parses one complete prospective Foundation
// snapshot. Settings transitions use it before persistence so graph validation
// never rereads the old effective values.
func RecoveryConfigFromValues(values map[string]string) (RecoveryConfig, error) {
	if err := validateCompleteFoundationValues(values); err != nil {
		return RecoveryConfig{}, err
	}
	return recoveryConfigFromValidatedValues(values)
}

func recoveryConfigFromValidatedValues(values map[string]string) (RecoveryConfig, error) {
	var err error
	config := RecoveryConfig{}
	if config.Enabled, err = parseFoundationBool(values, "backup_assets.recovery.enabled"); err != nil {
		return RecoveryConfig{}, err
	}
	for _, field := range []struct {
		key    string
		target *time.Duration
	}{
		{"backup_assets.recovery.receipt_replay_ttl", &config.ReceiptReplayTTL},
		{"backup_assets.recovery.write_grant_ttl", &config.WriteGrantTTL},
		{"backup_assets.recovery.delete_grant_ttl", &config.DeleteGrantTTL},
		{"backup_assets.recovery.receipt_reaper_cadence", &config.ReceiptReaperCadence},
		{"backup_assets.recovery.preflight_ttl", &config.PreflightTTL},
		{"backup_assets.recovery.lease_ttl", &config.LeaseTTL},
		{"backup_assets.recovery.lease_renew_margin", &config.LeaseRenewMargin},
		{"backup_assets.recovery.takeover_cadence", &config.TakeoverCadence},
		{"backup_assets.recovery.retry_base", &config.RetryBase},
		{"backup_assets.recovery.retry_max_delay", &config.RetryMaxDelay},
		{"backup_assets.recovery.execution_timeout", &config.ExecutionTimeout},
		{"backup_assets.recovery.result_default_ttl", &config.ResultDefaultTTL},
		{"backup_assets.recovery.result_retain_hard_cap", &config.ResultRetainHardCap},
		{"backup_assets.recovery.result_read_permit_ttl", &config.ResultReadPermitTTL},
		{"backup_assets.recovery.result_drain_timeout", &config.ResultDrainTimeout},
		{"backup_assets.recovery.cleanup_cadence", &config.CleanupCadence},
		{"backup_assets.recovery.cleanup_lease_ttl", &config.CleanupLeaseTTL},
		{"backup_assets.recovery.cleanup_retry_base", &config.CleanupRetryBase},
		{"backup_assets.recovery.cleanup_retry_max_delay", &config.CleanupRetryMaxDelay},
	} {
		if *field.target, err = parseFoundationDuration(values, field.key); err != nil {
			return RecoveryConfig{}, err
		}
	}
	for _, field := range []struct {
		key    string
		target *int
	}{
		{"backup_assets.recovery.receipt_reaper_batch_size", &config.ReceiptReaperBatchSize},
		{"backup_assets.recovery.max_selection_items", &config.MaxSelectionItems},
		{"backup_assets.recovery.worker_concurrency", &config.WorkerConcurrency},
		{"backup_assets.recovery.scan_limit", &config.ScanLimit},
		{"backup_assets.recovery.cleanup_batch_size", &config.CleanupBatchSize},
		{"backup_assets.recovery.reconciliation_finding_limit", &config.ReconciliationFindingLimit},
	} {
		if *field.target, err = parseFoundationInt(values, field.key); err != nil {
			return RecoveryConfig{}, err
		}
	}
	if config.MaxLogicalBytes, err = parseFoundationInt64(values, "backup_assets.recovery.max_logical_bytes"); err != nil {
		return RecoveryConfig{}, err
	}
	return config, nil
}

// ExportConfigFromValues parses one complete, validated foundation snapshot
// without reading settings. Callers that already hold the settings mutation
// gate use it to prepare a prospective Export graph safely.
func ExportConfigFromValues(values map[string]string) (ExportConfig, error) {
	if err := validateCompleteFoundationValues(values); err != nil {
		return ExportConfig{}, err
	}
	return exportConfigFromValidatedValues(values)
}

func exportConfigFromValidatedValues(values map[string]string) (ExportConfig, error) {
	result := ExportConfig{
		Root:           strings.TrimSpace(values["backup_assets.export.root"]),
		DefaultProfile: strings.TrimSpace(values["backup_assets.export.default_profile"]),
	}
	var err error
	if result.Enabled, err = parseFoundationBool(values, "backup_assets.export.enabled"); err != nil {
		return ExportConfig{}, err
	}
	for _, field := range []struct {
		key    string
		target *time.Duration
	}{
		{"backup_assets.export.max_duration", &result.MaxDuration},
		{"backup_assets.export.retry_base", &result.RetryBase},
		{"backup_assets.export.retry_max_delay", &result.RetryMaxDelay},
		{"backup_assets.export.lease_ttl", &result.LeaseTTL},
		{"backup_assets.export.lease_renew_margin", &result.LeaseRenewMargin},
		{"backup_assets.export.ready_ttl", &result.ReadyTTL},
		{"backup_assets.idempotency_ttl", &result.IdempotencyTTL},
		{"backup_assets.export.summary_ttl", &result.SummaryTTL},
		{"backup_assets.export.ticket_ttl", &result.Ticket.TTL},
		{"backup_assets.export.gc_cadence", &result.GCCadence},
		{"backup_assets.archive.member_ttl", &result.Archive.MemberTTL},
		{"backup_assets.archive.max_duration", &result.Archive.MaxDuration},
	} {
		if *field.target, err = parseFoundationDuration(values, field.key); err != nil {
			return ExportConfig{}, err
		}
	}
	for _, field := range []struct {
		key    string
		target *int
	}{
		{"backup_assets.export.max_items", &result.MaxItems},
		{"backup_assets.export.max_source_points", &result.MaxSourcePoints},
		{"backup_assets.export.user_active_jobs", &result.UserActiveJobs},
		{"backup_assets.export.global_active_jobs", &result.GlobalActiveJobs},
		{"backup_assets.export.worker_concurrency", &result.WorkerConcurrency},
		{"backup_assets.export.max_open_readers", &result.MaxOpenReaders},
		{"backup_assets.export.max_attempts", &result.MaxAttempts},
		{"backup_assets.export.ticket_max_requests", &result.Ticket.MaxRequests},
		{"backup_assets.export.ticket_max_in_flight", &result.Ticket.MaxInFlight},
		{"backup_assets.export.reconcile_batch_size", &result.ReconcileBatchSize},
		{"backup_assets.idempotency_key_max_bytes", &result.IdempotencyKeyMaxBytes},
		{"backup_assets.archive.max_entries", &result.Archive.MaxEntries},
		{"backup_assets.archive.max_depth", &result.Archive.MaxDepth},
		{"backup_assets.archive.max_compression_ratio", &result.Archive.MaxCompressionRatio},
	} {
		if *field.target, err = parseFoundationInt(values, field.key); err != nil {
			return ExportConfig{}, err
		}
	}
	for _, field := range []struct {
		key    string
		target *int64
	}{
		{"backup_assets.export.chunk_bytes", &result.ChunkBytes},
		{"backup_assets.export.max_item_bytes", &result.MaxItemBytes},
		{"backup_assets.export.max_logical_bytes", &result.MaxLogicalBytes},
		{"backup_assets.export.max_provider_bytes", &result.MaxProviderBytes},
		{"backup_assets.export.max_ciphertext_bytes", &result.MaxCiphertextBytes},
		{"backup_assets.export.ticket_max_cumulative_bytes", &result.Ticket.MaxCumulativeBytes},
		{"backup_assets.export.user_store_quota", &result.Quota.UserStoreBytes},
		{"backup_assets.export.store_quota", &result.Quota.GlobalStoreBytes},
		{"backup_assets.archive.max_expanded_bytes", &result.Archive.MaxExpandedBytes},
		{"backup_assets.archive.member_max_bytes", &result.Archive.MemberMaxBytes},
	} {
		if *field.target, err = parseFoundationInt64(values, field.key); err != nil {
			return ExportConfig{}, err
		}
	}
	return result, nil
}

// FoundationTransitionConfigFromValues validates one complete snapshot once
// and builds every typed config used by the enable transition.
func FoundationTransitionConfigFromValues(values map[string]string) (FoundationTransitionConfig, error) {
	if err := validateCompleteFoundationValues(values); err != nil {
		return FoundationTransitionConfig{}, err
	}
	enabled, err := parseFoundationBool(values, "backup_assets.enabled")
	if err != nil {
		return FoundationTransitionConfig{}, err
	}
	contentConfig, err := contentConfigFromValidatedValues(values)
	if err != nil {
		return FoundationTransitionConfig{}, err
	}
	searchConfig, overlayConfig, err := searchOverlayConfigFromValidatedValues(values)
	if err != nil {
		return FoundationTransitionConfig{}, err
	}
	exportConfig, err := exportConfigFromValidatedValues(values)
	if err != nil {
		return FoundationTransitionConfig{}, err
	}
	recoveryConfig, err := recoveryConfigFromValidatedValues(values)
	if err != nil {
		return FoundationTransitionConfig{}, err
	}
	return FoundationTransitionConfig{
		Enabled:  enabled,
		Content:  contentConfig,
		Search:   searchConfig,
		Overlay:  overlayConfig,
		Export:   exportConfig,
		Recovery: recoveryConfig,
	}, nil
}

// TransitionConfig reads one complete atomic Foundation snapshot and builds
// the typed bundle used by runtime transitions outside a settings mutation.
func (service *FoundationService) TransitionConfig() (FoundationTransitionConfig, error) {
	values, err := service.foundationValuesSnapshot()
	if err != nil {
		return FoundationTransitionConfig{}, err
	}
	return FoundationTransitionConfigFromValues(values)
}

func validateCompleteFoundationValues(values map[string]string) error {
	for _, key := range settings.BackupAssetFoundationSettingKeys() {
		if _, exists := values[key]; !exists {
			return fmt.Errorf("%w: incomplete backup asset settings snapshot", ErrInvalidState)
		}
	}
	if err := settings.ValidateBackupAssetFoundationConfig(values); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidState, err)
	}
	return nil
}

func (service *FoundationService) foundationValuesSnapshot() (map[string]string, error) {
	if service == nil || service.settings == nil {
		return nil, fmt.Errorf("%w: settings service is unavailable", ErrInvalidState)
	}
	reader, ok := service.settings.(BackupAssetSettingsSnapshotReader)
	if !ok {
		return nil, fmt.Errorf("%w: atomic backup asset settings snapshot is unavailable", ErrInvalidState)
	}
	values, err := reader.BackupAssetSettingsSnapshot()
	if err != nil {
		return nil, fmt.Errorf("%w: read backup asset settings snapshot: %v", ErrInvalidState, err)
	}
	return values, nil
}

func (service *FoundationService) atomicFoundationValues() (map[string]string, error) {
	values, err := service.foundationValuesSnapshot()
	if err != nil {
		return nil, err
	}
	if err := validateCompleteFoundationValues(values); err != nil {
		return nil, err
	}
	return values, nil
}

func (service *FoundationService) SearchConfig() (SearchConfig, error) {
	config, _, err := service.SearchOverlayConfig()
	return config, err
}

func (service *FoundationService) OverlayConfig() (OverlayConfig, error) {
	_, config, err := service.SearchOverlayConfig()
	return config, err
}

func (service *FoundationService) AuditRetentionConfig() (detailDays int, checkpointDays int, err error) {
	values, err := service.effectiveFoundationValues()
	if err != nil {
		return 0, 0, err
	}
	detailDays, err = parseFoundationInt(values, "backup_assets.audit_detail_retention_days")
	if err != nil {
		return 0, 0, err
	}
	checkpointDays, err = parseFoundationInt(values, "backup_assets.audit_checkpoint_retention_days")
	if err != nil {
		return 0, 0, err
	}
	if detailDays < 1 || detailDays > 3650 || checkpointDays < 180 || checkpointDays > 36500 {
		return 0, 0, fmt.Errorf("%w: invalid audit retention settings", ErrInvalidState)
	}
	return detailDays, checkpointDays, nil
}

func (service *FoundationService) AuditConfig() (AuditConfig, error) {
	values, err := service.effectiveFoundationValues()
	if err != nil {
		return AuditConfig{}, err
	}
	segmentMaxEvents, err := parseFoundationInt64(values, "backup_assets.audit_segment_max_events")
	if err != nil {
		return AuditConfig{}, err
	}
	segmentMaxAge, err := parseFoundationDuration(values, "backup_assets.audit_segment_max_age")
	if err != nil {
		return AuditConfig{}, err
	}
	return AuditConfig{SegmentMaxEvents: segmentMaxEvents, SegmentMaxAge: segmentMaxAge}, nil
}

func (service *FoundationService) PublicationConfig() (PublicationConfig, error) {
	values, err := service.effectiveFoundationValues()
	if err != nil {
		return PublicationConfig{}, err
	}
	reconcileInterval, err := parseFoundationDuration(values, "backup_assets.publication_reconcile_interval")
	if err != nil {
		return PublicationConfig{}, err
	}
	reconcileBatchSize, err := parseFoundationInt(values, "backup_assets.publication_reconcile_batch_size")
	if err != nil {
		return PublicationConfig{}, err
	}
	workerConcurrency, err := parseFoundationInt(values, "backup_assets.publication_worker_concurrency")
	if err != nil {
		return PublicationConfig{}, err
	}
	missingGrace, err := parseFoundationDuration(values, "backup_assets.publication_missing_grace")
	if err != nil {
		return PublicationConfig{}, err
	}
	backupStreamMaxBytes, err := parseFoundationInt64(values, "backup_assets.publication_stream_max_bytes")
	if err != nil {
		return PublicationConfig{}, err
	}
	manifestTimeout, err := parseFoundationDuration(values, "backup_assets.manifest_timeout")
	if err != nil {
		return PublicationConfig{}, err
	}
	manifestMaxBytes, err := parseFoundationInt64(values, "backup_assets.manifest_max_bytes")
	if err != nil {
		return PublicationConfig{}, err
	}
	manifestMaxEntries, err := parseFoundationInt64(values, "backup_assets.manifest_max_entries")
	if err != nil {
		return PublicationConfig{}, err
	}
	manifestMaxRecordBytes, err := parseFoundationInt(values, "backup_assets.manifest_max_record_bytes")
	if err != nil {
		return PublicationConfig{}, err
	}
	manifestMaxDepth, err := parseFoundationInt(values, "backup_assets.manifest_max_depth")
	if err != nil {
		return PublicationConfig{}, err
	}
	rcloneConfig, err := parseRclonePublicationConfig(values)
	if err != nil {
		return PublicationConfig{}, err
	}
	return PublicationConfig{
		ReconcileInterval:      reconcileInterval,
		ReconcileBatchSize:     reconcileBatchSize,
		WorkerConcurrency:      workerConcurrency,
		MissingGrace:           missingGrace,
		BackupStreamMaxBytes:   backupStreamMaxBytes,
		ManifestTimeout:        manifestTimeout,
		ManifestMaxBytes:       manifestMaxBytes,
		ManifestMaxEntries:     manifestMaxEntries,
		ManifestMaxRecordBytes: manifestMaxRecordBytes,
		ManifestMaxDepth:       manifestMaxDepth,
		Rclone:                 rcloneConfig,
	}, nil
}

func parseRclonePublicationConfig(values map[string]string) (RclonePublicationConfig, error) {
	preflightTTL, err := parseFoundationDuration(values, "backup_assets.rclone_preflight_ttl")
	if err != nil {
		return RclonePublicationConfig{}, err
	}
	portableDeadline, err := parseFoundationDuration(values, "backup_assets.rclone_portable_deadline")
	if err != nil {
		return RclonePublicationConfig{}, err
	}
	nativeDeadline, err := parseFoundationDuration(values, "backup_assets.rclone_native_deadline")
	if err != nil {
		return RclonePublicationConfig{}, err
	}
	boundConfigMaxBytes, err := parseFoundationInt64(values, "backup_assets.rclone_bound_config_max_bytes")
	if err != nil {
		return RclonePublicationConfig{}, err
	}
	controlPayloadMaxBytes, err := parseFoundationInt64(values, "backup_assets.rclone_control_payload_max_bytes")
	if err != nil {
		return RclonePublicationConfig{}, err
	}
	fullVerifyMaxBytes, err := parseFoundationInt64(values, "backup_assets.rclone_full_verify_max_bytes")
	if err != nil {
		return RclonePublicationConfig{}, err
	}
	manifestChunkMaxBytes, err := parseFoundationInt64(values, "backup_assets.rclone_manifest_chunk_max_bytes")
	if err != nil {
		return RclonePublicationConfig{}, err
	}
	lowLevelRetries, err := parseFoundationInt(values, "backup_assets.rclone_low_level_retries")
	if err != nil {
		return RclonePublicationConfig{}, err
	}
	stagingOrphanAge, err := parseFoundationDuration(values, "backup_assets.rclone_staging_orphan_age")
	if err != nil {
		return RclonePublicationConfig{}, err
	}
	stagingScanLimit, err := parseFoundationInt(values, "backup_assets.rclone_staging_scan_limit")
	if err != nil {
		return RclonePublicationConfig{}, err
	}
	kmsReadKeyMaxCount, err := parseFoundationInt(values, "backup_assets.rclone_kms_read_key_max_count")
	if err != nil {
		return RclonePublicationConfig{}, err
	}
	healthInterval, err := parseFoundationDuration(values, "backup_assets.rclone_health_interval")
	if err != nil {
		return RclonePublicationConfig{}, err
	}
	healthBatchSize, err := parseFoundationInt(values, "backup_assets.rclone_health_batch_size")
	if err != nil {
		return RclonePublicationConfig{}, err
	}
	awsSDKMaxAttempts, err := parseFoundationInt(values, "backup_assets.rclone_aws_sdk_max_attempts")
	if err != nil {
		return RclonePublicationConfig{}, err
	}
	return RclonePublicationConfig{
		PreflightTTL: preflightTTL, PortableDeadline: portableDeadline, NativeDeadline: nativeDeadline,
		BoundConfigMaxBytes: boundConfigMaxBytes, ControlPayloadMaxBytes: controlPayloadMaxBytes,
		FullVerifyMaxBytes: fullVerifyMaxBytes, ManifestChunkMaxBytes: manifestChunkMaxBytes,
		LowLevelRetries: lowLevelRetries, StagingOrphanAge: stagingOrphanAge, StagingScanLimit: stagingScanLimit,
		KMSReadKeyMaxCount: kmsReadKeyMaxCount, HealthInterval: healthInterval, HealthBatchSize: healthBatchSize,
		AWSSDKMaxAttempts: awsSDKMaxAttempts,
	}, nil
}

func (service *FoundationService) effectiveFoundationValues() (map[string]string, error) {
	if service == nil || service.settings == nil {
		return nil, fmt.Errorf("%w: settings service is unavailable", ErrInvalidState)
	}
	values := make(map[string]string, len(settings.BackupAssetCoreSettingKeys()))
	for _, key := range settings.BackupAssetCoreSettingKeys() {
		values[key] = service.settings.GetEffective(key)
	}
	if err := settings.ValidateBackupAssetFoundationConfig(values); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidState, err)
	}
	return values, nil
}

func parseFoundationBool(values map[string]string, key string) (bool, error) {
	value, err := strconv.ParseBool(strings.TrimSpace(values[key]))
	if err != nil {
		return false, fmt.Errorf("%w: parse %s: %v", ErrInvalidState, key, err)
	}
	return value, nil
}

func parseFoundationDuration(values map[string]string, key string) (time.Duration, error) {
	value, err := time.ParseDuration(values[key])
	if err != nil {
		return 0, fmt.Errorf("%w: parse %s: %v", ErrInvalidState, key, err)
	}
	return value, nil
}

func parseFoundationInt64(values map[string]string, key string) (int64, error) {
	value, err := strconv.ParseInt(values[key], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: parse %s: %v", ErrInvalidState, key, err)
	}
	return value, nil
}

func parseFoundationInt(values map[string]string, key string) (int, error) {
	value, err := parseFoundationInt64(values, key)
	if err != nil {
		return 0, err
	}
	if value > int64(^uint(0)>>1) {
		return 0, fmt.Errorf("%w: %s exceeds int range", ErrInvalidState, key)
	}
	return int(value), nil
}

type RepositoryDTO struct {
	ID                 string            `json:"id"`
	Provider           ProviderKind      `json:"provider_kind"`
	DisplayName        string            `json:"display_name"`
	Description        string            `json:"description"`
	VersionMode        VersionMode       `json:"version_mode"`
	Status             RepositoryStatus  `json:"status"`
	CapabilityRevision int               `json:"capability_revision"`
	Capabilities       CapabilitySet     `json:"capabilities"`
	Immutability       ImmutabilityLevel `json:"immutability_level"`
	LastSeenAt         *time.Time        `json:"last_seen_at,omitempty"`
	LastReconciledAt   *time.Time        `json:"last_reconciled_at,omitempty"`
	CreatedAt          time.Time         `json:"created_at"`
	UpdatedAt          time.Time         `json:"updated_at"`
}

type RecoveryPointLineageSummary struct {
	ProducingTaskID    *uint  `json:"producing_task_id,omitempty"`
	ProducingTaskRunID *uint  `json:"producing_task_run_id,omitempty"`
	SourcePointID      string `json:"source_recovery_point_id,omitempty"`
}

type RecoveryPointDTO struct {
	ID                 string                      `json:"id"`
	RepositoryID       string                      `json:"repository_id"`
	Lineage            RecoveryPointLineageSummary `json:"lineage"`
	Semantics          PointVersionSemantics       `json:"semantics"`
	State              RecoveryPointState          `json:"state"`
	Availability       PhysicalAvailability        `json:"physical_availability"`
	Hold               HoldState                   `json:"hold_state"`
	Immutability       ImmutabilityLevel           `json:"immutability_level"`
	ManifestDigest     string                      `json:"manifest_digest,omitempty"`
	EntryCount         int64                       `json:"entry_count"`
	LogicalBytes       int64                       `json:"logical_bytes"`
	CapturedAt         *time.Time                  `json:"captured_at,omitempty"`
	CommittedAt        *time.Time                  `json:"committed_at,omitempty"`
	ObservedAt         *time.Time                  `json:"observed_at,omitempty"`
	CapabilityRevision int                         `json:"capability_revision"`
	Capabilities       CapabilitySet               `json:"capabilities"`
	CreatedAt          time.Time                   `json:"created_at"`
	UpdatedAt          time.Time                   `json:"updated_at"`
}

func ToRepositoryDTO(record model.BackupRepository) (RepositoryDTO, error) {
	provider := ProviderKind(record.ProviderKind)
	versionMode := VersionMode(record.VersionMode)
	status := RepositoryStatus(record.Status)
	immutability := ImmutabilityLevel(record.ImmutabilityLevel)
	if err := ValidateOpaqueID(record.ID); err != nil || !validRepositoryProviderKinds[provider] ||
		!validVersionModes[versionMode] || !validRepositoryStatuses[status] || !validImmutabilityLevels[immutability] ||
		record.CapabilityRevision <= 0 {
		return RepositoryDTO{}, fmt.Errorf("%w: invalid repository model", ErrInvalidState)
	}
	capabilities, err := decodeCapabilities(record.CapabilitiesJSON)
	if err != nil {
		return RepositoryDTO{}, err
	}
	return RepositoryDTO{
		ID:                 record.ID,
		Provider:           provider,
		DisplayName:        record.DisplayName,
		Description:        record.Description,
		VersionMode:        versionMode,
		Status:             status,
		CapabilityRevision: record.CapabilityRevision,
		Capabilities:       capabilities,
		Immutability:       immutability,
		LastSeenAt:         utcTimePointer(record.LastSeenAt),
		LastReconciledAt:   utcTimePointer(record.LastReconciledAt),
		CreatedAt:          record.CreatedAt.UTC(),
		UpdatedAt:          record.UpdatedAt.UTC(),
	}, nil
}

func ToRecoveryPointDTO(record model.RecoveryPoint, repositoryVersion VersionMode) (RecoveryPointDTO, error) {
	if ValidateOpaqueID(record.ID) != nil || ValidateOpaqueID(record.RepositoryID) != nil ||
		record.EntryCount < 0 || record.LogicalBytes < 0 || record.CapabilityRevision <= 0 {
		return RecoveryPointDTO{}, fmt.Errorf("%w: invalid recovery point model", ErrInvalidState)
	}

	semantics := PointVersionSemantics(record.Semantics)
	state := RecoveryPointState(record.State)
	availability := PhysicalAvailability(record.PhysicalAvailability)
	hold := HoldState(record.HoldState)
	immutability := ImmutabilityLevel(record.ImmutabilityLevel)
	retirementReason := RetirementReason("")
	if record.RetirementReason != nil {
		retirementReason = RetirementReason(*record.RetirementReason)
	}
	profile := RecoveryPointProfile{
		VersionMode:                 repositoryVersion,
		Semantics:                   semantics,
		State:                       state,
		Immutability:                immutability,
		Availability:                availability,
		Hold:                        hold,
		ObservedAt:                  record.ObservedAt,
		RetirementReason:            retirementReason,
		RetiredAt:                   record.RetiredAt,
		HasEncryptedRollbackLocator: strings.TrimSpace(record.EncryptedRollbackLocator) != "",
	}
	if err := ValidateRecoveryPointProfile(profile); err != nil {
		return RecoveryPointDTO{}, err
	}

	var lineage RecoveryPointLineageSummary
	if strings.TrimSpace(record.LineageJSON) != "" {
		if err := json.Unmarshal([]byte(record.LineageJSON), &lineage); err != nil {
			return RecoveryPointDTO{}, fmt.Errorf("%w: invalid recovery point lineage", ErrInvalidState)
		}
	}
	if lineage.SourcePointID != "" && ValidateOpaqueID(lineage.SourcePointID) != nil {
		return RecoveryPointDTO{}, fmt.Errorf("%w: invalid lineage source", ErrInvalidState)
	}
	if lineage.ProducingTaskID == nil {
		lineage.ProducingTaskID = record.ProducingTaskID
	}
	if lineage.ProducingTaskRunID == nil {
		lineage.ProducingTaskRunID = record.ProducingTaskRunID
	}

	capabilities, err := decodeCapabilities(record.CapabilitiesJSON)
	if err != nil {
		return RecoveryPointDTO{}, err
	}
	return RecoveryPointDTO{
		ID:                 record.ID,
		RepositoryID:       record.RepositoryID,
		Lineage:            lineage,
		Semantics:          semantics,
		State:              state,
		Availability:       availability,
		Hold:               hold,
		Immutability:       immutability,
		ManifestDigest:     record.ManifestDigest,
		EntryCount:         record.EntryCount,
		LogicalBytes:       record.LogicalBytes,
		CapturedAt:         utcTimePointer(record.CapturedAt),
		CommittedAt:        utcTimePointer(record.CommittedAt),
		ObservedAt:         utcTimePointer(record.ObservedAt),
		CapabilityRevision: record.CapabilityRevision,
		Capabilities:       capabilities,
		CreatedAt:          record.CreatedAt.UTC(),
		UpdatedAt:          record.UpdatedAt.UTC(),
	}, nil
}

func decodeCapabilities(raw string) (CapabilitySet, error) {
	var capabilities CapabilitySet
	if strings.TrimSpace(raw) == "" {
		raw = "{}"
	}
	if err := json.Unmarshal([]byte(raw), &capabilities); err != nil {
		return CapabilitySet{}, fmt.Errorf("%w: invalid capability snapshot", ErrInvalidState)
	}
	if capabilities.Reason != nil {
		if err := ValidateCapabilityReason(*capabilities.Reason); err != nil {
			return CapabilitySet{}, err
		}
	}
	return capabilities, nil
}

func utcTimePointer(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	utc := value.UTC()
	return &utc
}

var validRepositoryProviderKinds = setOf(ProviderRestic, ProviderRsync, ProviderRclone, ProviderCommand)
