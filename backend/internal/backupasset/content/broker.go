package content

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"xirang/backend/internal/auth"
	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

var (
	ErrInvalidBrokerRequest     = errors.New("invalid content broker request")
	ErrBrokerClosed             = errors.New("content broker closed")
	ErrContentNotFound          = errors.New("content delivery not found")
	ErrContentUnavailable       = errors.New("content delivery unavailable")
	ErrContentBudgetExceeded    = errors.New("content delivery budget exceeded")
	ErrContentFeatureDisabled   = errors.New("backup asset content feature disabled")
	ErrContentSourceUnavailable = errors.New("backup asset content source unavailable")
	ErrContentCacheUnavailable  = errors.New("backup asset content cache unavailable")
)

const (
	ticketFailureAuditTimeout = 5 * time.Second
	gatewayCleanupTimeout     = 5 * time.Second
)

type DeliveryActor struct {
	UserID   uint
	Username string
	Role     string
}

type DeliverySession struct {
	JTI          string
	UserID       uint
	Role         string
	TokenVersion uint
	ExpiresAt    time.Time
}

type AuthorizedAsset struct {
	Ref                          backupasset.AssetRef
	CatalogGenerationID          string
	RepositoryID                 string
	Provider                     backupasset.ProviderKind
	ProviderCapabilityRevision   int64
	SourceFingerprint            string
	EntryFingerprint             string
	FingerprintStrength          string
	Size                         int64
	ModifiedAt                   *time.Time
	MediaType                    string
	Path                         string
	Name                         string
	RangeProven                  bool
	SearchClassification         Classification
	SearchClassificationRevision int64
}

type AssetAuthorizer interface {
	Authorize(context.Context, DeliveryActor, backupasset.AssetRef, DeliveryAction) (AuthorizedAsset, error)
	Reauthorize(context.Context, DeliveryActor, AuthorizedAsset, DeliveryAction) error
}

type DerivedRepresentationSource interface {
	Resolve(context.Context, DerivedRepresentationRequest) (DerivedRepresentation, error)
	Open(context.Context, DerivedRepresentation, SourceRequest) (SourceSession, error)
}

type DeliverySessionValidator interface {
	Validate(context.Context, DeliverySession) error
}

type BrokerAudit interface {
	Write(context.Context, backupasset.AuditEventInput) error
	BacklogAvailable(context.Context) error
}

type BrokerBudget interface {
	Reserve(context.Context, ReservationIntent) (Reservation, error)
	RecordBlocked(context.Context, BlockedRequest) error
	Finalize(context.Context, FinalizeIntent) (Finalization, error)
}

type BrokerConfig struct {
	TicketTimeout      time.Duration
	WriteIdleTimeout   time.Duration
	LeaseHeartbeat     time.Duration
	PreviewTTL         time.Duration
	MediaTTL           time.Duration
	IdleTTL            time.Duration
	MaxBytesPerRequest int64
	MaxCumulativeBytes int64
	MaxRequests        int64
	MaxInFlight        int64
	Classification     ClassificationConfig
	Renderer           RendererConfig
}

type BrokerDependencies struct {
	DB                     *gorm.DB
	Now                    func() time.Time
	FeatureEnabled         func(context.Context) (bool, error)
	Authorize              AssetAuthorizer
	Session                DeliverySessionValidator
	Lease                  ContentLeaseController
	Source                 SourceResolver
	Derived                DerivedRepresentationSource
	SecurityPolicyRevision func(context.Context) (string, error)
	Audit                  BrokerAudit
	Budget                 BrokerBudget
	Metrics                Metrics
	TicketMaterial         func() (TicketMaterial, error)
	RequestID              func() (string, error)
	Config                 func(context.Context) (BrokerConfig, error)
}

type IssueRequest struct {
	Actor        DeliveryActor
	Session      DeliverySession
	Ref          backupasset.AssetRef
	Action       DeliveryAction
	Renderer     Renderer
	Profile      RendererProfile
	Proof        *StepUpProof
	SecureCookie bool
}

type TicketDescriptor struct {
	SchemaVersion    int                           `json:"schema_version" minimum:"1" maximum:"1" example:"1"`
	ContentURL       string                        `json:"content_url"`
	Action           DeliveryAction                `json:"action" enums:"preview,download"`
	Renderer         Renderer                      `json:"renderer" enums:"escaped_text,safe_raster,same_origin_pdf,native_audio,native_video,metadata_hex,attachment"`
	Profile          RendererProfile               `json:"profile" enums:"text_v1,raster_v1,pdf_v1,audio_v1,video_v1,hex_v1,original_v1"`
	ContentType      string                        `json:"content_type"`
	ContentLength    int64                         `json:"content_length"`
	ETag             string                        `json:"etag"`
	LastModified     *time.Time                    `json:"last_modified"`
	Range            RangePolicy                   `json:"range" enums:"none,single"`
	Classification   Classification                `json:"classification" enums:"non_secret,secret,unknown"`
	ExpiresAt        time.Time                     `json:"expires_at"`
	IdleExpiresAt    time.Time                     `json:"idle_expires_at"`
	CapabilityReason *backupasset.CapabilityReason `json:"capability_reason"`
	FallbackActions  []DeliveryAction              `json:"fallback_actions" enums:"preview,download"`
}

type IssuedTicket struct {
	Descriptor TicketDescriptor `json:"-"`
	Cookie     *http.Cookie     `json:"-"`
}

type GatewayRequest struct {
	DeliveryID     string
	Method         string
	RawCookie      string
	RangeHeaders   []string
	IfRangeHeaders []string
}

type activeContentRead struct {
	sessionJTI string
	provider   backupasset.ProviderKind
	cancel     context.CancelFunc
	done       chan struct{}
}

type Broker struct {
	db                     *gorm.DB
	now                    func() time.Time
	featureEnabled         func(context.Context) (bool, error)
	authorize              AssetAuthorizer
	session                DeliverySessionValidator
	lease                  ContentLeaseController
	source                 SourceResolver
	derived                DerivedRepresentationSource
	securityPolicyRevision func(context.Context) (string, error)
	audit                  BrokerAudit
	budget                 BrokerBudget
	metrics                Metrics
	ticketMaterial         func() (TicketMaterial, error)
	requestID              func() (string, error)
	config                 func(context.Context) (BrokerConfig, error)

	mu              sync.Mutex
	closed          bool
	accepting       bool
	issues          sync.WaitGroup
	leases          map[string]*ContentLeaseSession
	assets          map[string]AuthorizedAsset
	derivedBindings map[string]DerivedRepresentation
	reads           map[string]map[string]activeContentRead
	inFlight        map[backupasset.ProviderKind]int
	cache           *AuthenticatedCache
}

func NewBroker(dependencies BrokerDependencies) (*Broker, error) {
	if dependencies.DB == nil || dependencies.Now == nil || dependencies.FeatureEnabled == nil ||
		dependencies.Authorize == nil || dependencies.Session == nil || dependencies.Lease == nil ||
		dependencies.Source == nil || dependencies.Audit == nil ||
		dependencies.Budget == nil || dependencies.Config == nil {
		return nil, ErrInvalidBrokerRequest
	}
	if (dependencies.Derived == nil) != (dependencies.SecurityPolicyRevision == nil) {
		return nil, ErrInvalidBrokerRequest
	}
	if dependencies.TicketMaterial == nil {
		dependencies.TicketMaterial = NewTicketMaterial
	}
	if dependencies.RequestID == nil {
		dependencies.RequestID = func() (string, error) { return readOpaqueID(rand.Reader) }
	}
	if dependencies.Metrics == nil {
		dependencies.Metrics = NoopMetrics{}
	}
	config, err := dependencies.Config(context.Background())
	if err != nil || !validBrokerConfig(config) {
		return nil, ErrInvalidBrokerRequest
	}
	if _, _, err := newBrokerPolicies(config); err != nil {
		return nil, ErrInvalidBrokerRequest
	}
	return &Broker{
		db: dependencies.DB, now: dependencies.Now, featureEnabled: dependencies.FeatureEnabled,
		authorize: dependencies.Authorize, session: dependencies.Session, lease: dependencies.Lease,
		source: dependencies.Source, derived: dependencies.Derived,
		securityPolicyRevision: dependencies.SecurityPolicyRevision, audit: dependencies.Audit,
		budget: dependencies.Budget, metrics: dependencies.Metrics,
		ticketMaterial: dependencies.TicketMaterial, requestID: dependencies.RequestID, config: dependencies.Config,
		leases: make(map[string]*ContentLeaseSession), assets: make(map[string]AuthorizedAsset),
		derivedBindings: make(map[string]DerivedRepresentation),
		reads:           make(map[string]map[string]activeContentRead), inFlight: make(map[backupasset.ProviderKind]int), accepting: true,
	}, nil
}

func (broker *Broker) Issue(ctx context.Context, request IssueRequest) (ticket IssuedTicket, resultErr error) {
	if broker == nil {
		return IssuedTicket{}, ErrInvalidBrokerRequest
	}
	metricOutcome := MetricOutcomeFailure
	defer func() { broker.metrics.ObserveTicket(request.Action, metricOutcome) }()
	if err := broker.beginIssue(); err != nil {
		return IssuedTicket{}, err
	}
	defer broker.issues.Done()
	now := broker.now().UTC()
	if request.Actor.Role != "admin" && request.Actor.Role != "operator" {
		return IssuedTicket{}, backupasset.ErrForbidden
	}
	if request.Action == DeliveryDownload && request.Actor.Role != "admin" {
		return IssuedTicket{}, backupasset.ErrForbidden
	}
	if !validIssueRequest(request, now) {
		return IssuedTicket{}, ErrInvalidDeliveryProduct
	}
	ctx = nonNilContext(ctx)
	enabled, err := broker.featureEnabled(ctx)
	if err != nil || !enabled {
		return IssuedTicket{}, ErrContentFeatureDisabled
	}
	if err := broker.audit.BacklogAvailable(ctx); err != nil {
		return IssuedTicket{}, err
	}
	if err := broker.session.Validate(ctx, request.Session); err != nil {
		return IssuedTicket{}, backupasset.ErrForbidden
	}
	asset, err := broker.authorize.Authorize(ctx, request.Actor, request.Ref, request.Action)
	if err != nil {
		return IssuedTicket{}, err
	}
	if !validAuthorizedAsset(asset, request.Ref) {
		return IssuedTicket{}, ErrContentSourceUnavailable
	}
	material, err := broker.ticketMaterial()
	if err != nil || !validTicketMaterial(material) {
		return IssuedTicket{}, ErrInvalidBrokerRequest
	}
	auditGrant := model.BackupAssetDeliveryGrant{
		ID: material.GrantID, Renderer: string(request.Renderer), Profile: string(request.Profile),
		Classification: string(ClassificationUnknown),
	}
	auditFailure := true
	defer func() {
		if !auditFailure || resultErr == nil {
			return
		}
		outcome, failureCode := ticketFailureAuditOutcome(resultErr)
		if outcome == backupasset.AuditOutcomeBlocked {
			metricOutcome = MetricOutcomeBlocked
		}
		auditCtx, cancelAudit := context.WithTimeout(context.WithoutCancel(ctx), ticketFailureAuditTimeout)
		defer cancelAudit()
		if auditErr := broker.audit.Write(
			auditCtx, ticketAuditInput(request, asset, auditGrant, outcome, failureCode),
		); auditErr != nil {
			resultErr = errors.Join(resultErr, ErrContentAuditUnavailable)
		}
		ticket = IssuedTicket{}
	}()
	lease, err := AcquireContentLease(ctx, broker.lease, ContentLeaseRequest{Ref: request.Ref, GrantID: material.GrantID})
	if err != nil {
		return IssuedTicket{}, err
	}
	releaseLease := true
	defer func() {
		if releaseLease {
			releaseCtx, cancelRelease := context.WithTimeout(context.WithoutCancel(ctx), ticketFailureAuditTimeout)
			defer cancelRelease()
			if releaseErr := lease.Release(releaseCtx); releaseErr != nil {
				resultErr = errors.Join(resultErr, ErrContentUnavailable)
				ticket = IssuedTicket{}
			}
		}
	}()
	config, err := broker.config(ctx)
	if err != nil || !validBrokerConfig(config) {
		return IssuedTicket{}, ErrInvalidBrokerRequest
	}
	classifier, renderer, err := newBrokerPolicies(config)
	if err != nil {
		return IssuedTicket{}, ErrInvalidBrokerRequest
	}
	representationAsset := asset
	derivedBinding, err := broker.resolveDerivedRepresentation(ctx, request, asset)
	if err != nil {
		return IssuedTicket{}, err
	}
	if derivedBinding != nil {
		representationAsset = authorizedAssetForDerived(asset, *derivedBinding)
	}
	profileTTL := config.PreviewTTL
	if request.Action == DeliveryDownload || request.Renderer == RendererNativeAudio || request.Renderer == RendererNativeVideo {
		profileTTL = config.MediaTTL
	}
	profileExpiry := now.Add(profileTTL)
	leaseBinding := lease.Binding()
	proofExpiry := (*time.Time)(nil)
	if request.Proof != nil {
		value := request.Proof.ExpiresAt.UTC()
		proofExpiry = &value
	}
	deadlines, err := ResolveGrantDeadlines(GrantDeadlineInput{
		Now: now, SessionExpiresAt: request.Session.ExpiresAt, ProfileExpiresAt: profileExpiry,
		LeaseDeadline: leaseBinding.AbsoluteDeadline, ProofExpiresAt: proofExpiry, IdleTTL: config.IdleTTL,
	})
	if err != nil {
		return IssuedTicket{}, err
	}
	ticketDeadline := minTime(now.Add(config.TicketTimeout), deadlines.AbsoluteExpiresAt)
	ticketCtx, cancel := context.WithDeadline(ctx, ticketDeadline)
	defer cancel()
	prefix, stat, capabilities, err := broker.readTicketPrefix(ticketCtx, representationAsset, derivedBinding, classifier, renderer)
	if err != nil {
		return IssuedTicket{}, err
	}
	classification, err := classifier.Classify(ticketCtx, ClassificationRequest{
		Path: asset.Path, Name: asset.Name, SourceSize: stat.Size, ProviderMediaType: representationAsset.MediaType,
		CatalogGenerationID: asset.CatalogGenerationID, SourceFingerprint: asset.SourceFingerprint,
		Search: searchClassificationEvidence(asset),
	}, bytes.NewReader(prefix))
	if err != nil {
		return IssuedTicket{}, err
	}
	auditGrant.Classification = string(classification.Classification)
	representationAsset.RangeProven = representationAsset.RangeProven && capabilities.Range
	rangePolicy := RangeNone
	if cacheEligibleRenderer(request.Renderer) &&
		(representationAsset.RangeProven || broker.cacheRangeAvailable(cacheObjectForAsset(request.Actor.UserID, representationAsset, request.Renderer, request.Profile))) {
		rangePolicy = RangeSingle
	}
	renderPlan, err := renderer.Prepare(RenderRequest{
		Action: request.Action, Renderer: request.Renderer, Profile: request.Profile, Range: rangePolicy,
		SourceSize: stat.Size, Prefix: prefix, ProviderMediaType: representationAsset.MediaType, Filename: asset.Name,
	})
	if err != nil {
		return IssuedTicket{}, err
	}
	product := DeliveryProduct{
		Action: request.Action, Method: MethodGetHead, Range: rangePolicy,
		Renderer: request.Renderer, Profile: request.Profile, Classification: classification.Classification,
		Proof: request.Proof, AbsoluteExpiresAt: deadlines.AbsoluteExpiresAt,
	}
	if err := ValidateDeliveryProduct(product, now); err != nil {
		return IssuedTicket{}, err
	}
	etag := representationETag(representationAsset, product, renderPlan, classification)
	grant := buildIssuedGrant(request, representationAsset, material, leaseBinding, deadlines, renderPlan, classification, etag, config, now)
	auditGrant = grant
	if err := broker.db.WithContext(ticketCtx).Create(&grant).Error; err != nil {
		return IssuedTicket{}, err
	}
	auditFailure = false
	auditInput := ticketAuditInput(request, asset, grant, backupasset.AuditOutcomeSuccess, "")
	if err := broker.audit.Write(ticketCtx, auditInput); err != nil {
		broker.revokeIssuedGrant(ticketCtx, grant.ID, "audit_failed", now)
		return IssuedTicket{}, ErrContentAuditUnavailable
	}
	result := broker.db.WithContext(ticketCtx).Model(&model.BackupAssetDeliveryGrant{}).
		Where("id = ? AND state = ? AND version = ?", grant.ID, DeliveryIssued, grant.Version).
		Updates(map[string]any{"state": DeliveryActive, "version": grant.Version + 1, "updated_at": now})
	if result.Error != nil || result.RowsAffected != 1 {
		broker.revokeIssuedGrant(ticketCtx, grant.ID, "request_failed", now)
		return IssuedTicket{}, ErrInvalidBrokerRequest
	}
	cookie, err := NewDeliveryCookie(material.DeliveryID, material.CookieSecret, deadlines.AbsoluteExpiresAt, request.SecureCookie)
	if err != nil {
		broker.revokeIssuedGrant(ticketCtx, grant.ID, "request_failed", now)
		return IssuedTicket{}, err
	}
	broker.mu.Lock()
	broker.leases[grant.ID] = lease
	broker.assets[grant.ID] = asset
	if derivedBinding != nil {
		broker.derivedBindings[grant.ID] = *derivedBinding
	}
	broker.mu.Unlock()
	releaseLease = false
	metricOutcome = MetricOutcomeSuccess
	return IssuedTicket{
		Descriptor: TicketDescriptor{
			SchemaVersion: 1, ContentURL: cookie.Path, Action: request.Action,
			Renderer: request.Renderer, Profile: request.Profile, ContentType: renderPlan.MediaType,
			ContentLength: renderPlan.Size, ETag: etag, LastModified: representationAsset.ModifiedAt,
			Range: rangePolicy, Classification: classification.Classification,
			ExpiresAt: deadlines.AbsoluteExpiresAt, IdleExpiresAt: deadlines.IdleExpiresAt,
			FallbackActions: []DeliveryAction{},
		},
		Cookie: cookie,
	}, nil
}

// Serve performs the cookie-only content request flow. It reserves all byte
// and concurrency budgets before opening a source and owns reader close,
// conservative finalization, aggregate audit, and response streaming.
func (broker *Broker) Serve(ctx context.Context, request GatewayRequest, writer http.ResponseWriter) error {
	if broker == nil || writer == nil || !validGatewayRequest(request) {
		return ErrContentNotFound
	}
	ctx = nonNilContext(ctx)
	if broker.isClosed() {
		return ErrContentNotFound
	}
	enabled, err := broker.featureEnabled(ctx)
	if err != nil || !enabled {
		return ErrContentNotFound
	}
	secret, err := ParseDeliveryCookie(request.RawCookie)
	if err != nil {
		return ErrContentNotFound
	}
	grant, actor, session, asset, lease, err := broker.authorizeGatewayRequest(ctx, request.DeliveryID, secret)
	if err != nil {
		return err
	}
	config, err := broker.config(ctx)
	if err != nil || !validBrokerConfig(config) {
		return ErrContentUnavailable
	}

	plan, err := PlanRepresentation(RepresentationRequest{
		Method: request.Method, RangeHeaders: request.RangeHeaders, IfRangeHeaders: request.IfRangeHeaders,
		Size: grant.RepresentationSize, ETag: grant.RepresentationETag, LastModified: grant.SourceModifiedAt,
		RangePolicy: RangePolicy(grant.RangePolicy), Seekable: grant.RangePolicy == string(RangeSingle),
		FullAllowed: grant.RepresentationSize <= grant.MaxBytesPerRequest, MaxResponseBytes: grant.MaxBytesPerRequest,
	})
	if err != nil {
		return ErrContentNotFound
	}
	requestID, err := broker.requestID()
	if err != nil || backupasset.ValidateOpaqueID(requestID) != nil {
		return ErrContentUnavailable
	}
	if plan.FailureCode != "" {
		if err := broker.budget.RecordBlocked(ctx, BlockedRequest{
			RequestID: requestID, GrantID: grant.ID, Method: request.Method,
			Status: plan.Status, FailureCode: plan.FailureCode, RangeRequested: len(request.RangeHeaders) > 0,
		}); err != nil {
			if errors.Is(err, ErrBudgetExhausted) {
				return ErrContentBudgetExceeded
			}
			return ErrContentUnavailable
		}
		writeGatewayHeaders(writer.Header(), grant, plan)
		writer.WriteHeader(plan.Status)
		broker.metrics.ObserveRead(DeliveryAction(grant.Action), MetricOutcomeBlocked)
		return nil
	}

	reservedBytes, err := gatewayReservationBytes(grant, plan, request.Method)
	if err != nil {
		return ErrContentUnavailable
	}
	reservation, err := broker.budget.Reserve(ctx, ReservationIntent{
		RequestID: requestID, GrantID: grant.ID, Method: request.Method,
		Range: plan.Range, ReservedBytes: reservedBytes,
	})
	if err != nil {
		if errors.Is(err, ErrBudgetExhausted) {
			return ErrContentBudgetExceeded
		}
		return ErrContentNotFound
	}
	broker.metrics.AddBytes(MetricBytesReserved, reservedBytes)

	readCtx, cancel := context.WithCancel(ctx)
	done, registered := broker.registerRead(grant.ID, requestID, grant.SessionJTI, asset.Provider, cancel)
	if !registered {
		cancel()
		_ = broker.finalizeGatewayRequest(ctx, reservation, RequestFailed, http.StatusServiceUnavailable, RequestFailureInternal, -1, 0, false)
		return ErrContentNotFound
	}
	defer func() {
		cancel()
		broker.unregisterRead(grant.ID, requestID, done)
	}()

	responseBytes, providerBytes, status, failure, evidenceKnown, serveErr := broker.streamGatewayResponse(
		readCtx, writer, request, grant, actor, session, asset, lease, plan, config,
	)
	cleanupCtx, cancelCleanup := context.WithTimeout(context.WithoutCancel(ctx), gatewayCleanupTimeout)
	defer cancelCleanup()
	revokeReason := ""
	if errors.Is(serveErr, ErrCacheIntegrity) {
		revokeReason = "cache_invalid"
		broker.metrics.ObserveCache(MetricCacheTamper)
		broker.metrics.ObserveReason(MetricReasonSourceChanged)
	} else if errors.Is(serveErr, ErrContentCacheUnavailable) {
		revokeReason = "cache_invalid"
		broker.metrics.ObserveReason(MetricReasonSourceChanged)
	}
	if revokeReason != "" {
		if revokeErr := broker.revokeGrantAfterRead(
			cleanupCtx, grant.ID, requestID, revokeReason,
		); revokeErr != nil {
			serveErr = errors.Join(serveErr, revokeErr)
		}
	}
	state := RequestSucceeded
	metricOutcome := MetricOutcomeSuccess
	if failure != "" {
		state = RequestFailed
		metricOutcome = MetricOutcomeFailure
		if errors.Is(serveErr, context.Canceled) || errors.Is(serveErr, context.DeadlineExceeded) {
			state = RequestCanceled
			failure = RequestFailureClientCanceled
			status = 499
		}
	}
	finalizationErr := broker.finalizeGatewayRequest(
		cleanupCtx, reservation, state, status, failure,
		providerBytes, responseBytes, evidenceKnown,
	)
	broker.metrics.ObserveRead(DeliveryAction(grant.Action), metricOutcome)
	if finalizationErr != nil {
		return ErrContentUnavailable
	}
	return serveErr
}

func (broker *Broker) isClosed() bool {
	broker.mu.Lock()
	defer broker.mu.Unlock()
	return broker.closed
}

func (broker *Broker) SetCache(cache *AuthenticatedCache) error {
	if broker == nil || cache == nil {
		return ErrInvalidBrokerRequest
	}
	broker.mu.Lock()
	defer broker.mu.Unlock()
	if broker.closed {
		return ErrBrokerClosed
	}
	if broker.cache != nil && broker.cache != cache {
		return ErrInvalidBrokerRequest
	}
	broker.cache = cache
	return nil
}

func (broker *Broker) ClearCache(expected *AuthenticatedCache) error {
	if broker == nil || expected == nil {
		return ErrInvalidBrokerRequest
	}
	broker.mu.Lock()
	defer broker.mu.Unlock()
	if broker.cache != expected {
		return ErrInvalidBrokerRequest
	}
	broker.cache = nil
	return nil
}

func (broker *Broker) currentCache() *AuthenticatedCache {
	if broker == nil {
		return nil
	}
	broker.mu.Lock()
	defer broker.mu.Unlock()
	return broker.cache
}

func (broker *Broker) authorizeGatewayRequest(
	ctx context.Context,
	deliveryID string,
	secret string,
) (model.BackupAssetDeliveryGrant, DeliveryActor, DeliverySession, AuthorizedAsset, *ContentLeaseSession, error) {
	var grant model.BackupAssetDeliveryGrant
	result := broker.db.WithContext(ctx).Where("delivery_id = ?", deliveryID).Limit(1).Find(&grant)
	if result.Error != nil || result.RowsAffected != 1 || !VerifyCookieSecret(grant.CookieSecretHash, secret) ||
		!validGatewayGrant(grant, deliveryID, broker.now().UTC()) {
		return grant, DeliveryActor{}, DeliverySession{}, AuthorizedAsset{}, nil, ErrContentNotFound
	}
	actor := DeliveryActor{UserID: grant.OwnerUserID, Role: grant.SessionRole}
	if grant.Action == string(DeliveryDownload) && actor.Role != "admin" ||
		grant.Action == string(DeliveryPreview) && actor.Role != "admin" && actor.Role != "operator" {
		broker.revokeIssuedGrant(ctx, grant.ID, "permission_changed", broker.now().UTC())
		return grant, actor, DeliverySession{}, AuthorizedAsset{}, nil, ErrContentNotFound
	}
	session := DeliverySession{
		JTI: grant.SessionJTI, UserID: grant.OwnerUserID, Role: grant.SessionRole,
		TokenVersion: grant.SessionTokenVersion, ExpiresAt: grant.SessionExpiresAt,
	}
	if err := broker.session.Validate(ctx, session); err != nil {
		broker.revokeIssuedGrant(ctx, grant.ID, "session_revoked", broker.now().UTC())
		return grant, actor, session, AuthorizedAsset{}, nil, ErrContentNotFound
	}
	broker.mu.Lock()
	asset, assetFound := broker.assets[grant.ID]
	lease := broker.leases[grant.ID]
	derivedBinding, derivedFound := broker.derivedBindings[grant.ID]
	broker.mu.Unlock()
	if !assetFound || !authorizedAssetMatchesGrant(asset, grant, derivedBinding, derivedFound) {
		broker.revokeIssuedGrant(ctx, grant.ID, "source_changed", broker.now().UTC())
		return grant, actor, session, AuthorizedAsset{}, nil, ErrContentNotFound
	}
	if derivedFound {
		policyRevision, revisionErr := broker.securityPolicyRevision(ctx)
		if revisionErr != nil || strings.TrimSpace(policyRevision) != policyRevision || policyRevision == "" ||
			len(policyRevision) > 128 || policyRevision != derivedBinding.SecurityPolicyRevision {
			broker.revokeIssuedGrant(ctx, grant.ID, "policy_changed", broker.now().UTC())
			return grant, actor, session, asset, nil, ErrContentNotFound
		}
	}
	if err := broker.authorize.Reauthorize(ctx, actor, asset, DeliveryAction(grant.Action)); err != nil {
		broker.revokeIssuedGrant(ctx, grant.ID, "permission_changed", broker.now().UTC())
		return grant, actor, session, asset, nil, ErrContentNotFound
	}
	if lease == nil || lease.Binding().LeaseID != grant.LeaseID || lease.Binding().AttemptID != grant.LeaseAttemptID ||
		lease.Binding().FenceTokenHash != grant.LeaseFenceTokenHash || lease.Validate(ctx) != nil {
		broker.revokeIssuedGrant(ctx, grant.ID, "lease_lost", broker.now().UTC())
		return grant, actor, session, asset, nil, ErrContentNotFound
	}
	return grant, actor, session, asset, lease, nil
}

func (broker *Broker) streamGatewayResponse(
	ctx context.Context,
	writer http.ResponseWriter,
	request GatewayRequest,
	grant model.BackupAssetDeliveryGrant,
	actor DeliveryActor,
	session DeliverySession,
	asset AuthorizedAsset,
	lease *ContentLeaseSession,
	plan RepresentationPlan,
	config BrokerConfig,
) (responseBytes, providerBytes int64, status int, failure RequestFailureCode, evidenceKnown bool, resultErr error) {
	status = plan.Status
	providerBytes = 0
	if err := broker.gatewayHeartbeat(ctx, grant, actor, session, asset, lease, config.LeaseHeartbeat); err != nil {
		return 0, -1, http.StatusNotFound, RequestFailureCode("session_revoked"), false, err
	}
	streamCtx, stopHeartbeat := broker.monitorGatewayHeartbeat(
		ctx, grant, actor, session, asset, lease, config.LeaseHeartbeat,
	)
	ctx = streamCtx
	defer func() {
		if heartbeatErr := stopHeartbeat(); heartbeatErr != nil &&
			(resultErr == nil || errors.Is(resultErr, context.Canceled) || errors.Is(resultErr, context.DeadlineExceeded)) {
			status = http.StatusNotFound
			failure = RequestFailureCode("session_revoked")
			evidenceKnown = false
			resultErr = heartbeatErr
		}
	}()
	_, renderer, policyErr := newBrokerPolicies(config)
	if policyErr != nil {
		return 0, -1, http.StatusServiceUnavailable, RequestFailureInternal, false, ErrContentUnavailable
	}
	read, err := broker.prepareGatewayRead(ctx, request, grant, asset, plan, renderer)
	if err != nil {
		providerBytes, closeErr := read.close()
		return 0, providerBytes, http.StatusBadGateway, RequestFailureSourceFailed, closeErr == nil && providerBytes >= 0, err
	}
	defer func() { _, _ = read.close() }()

	writeGatewayHeaders(writer.Header(), grant, plan)
	if request.Method == http.MethodGet && plan.ContentLength > 0 {
		if err := setGatewayWriteDeadline(writer, broker.now().UTC(), grant.AbsoluteExpiresAt, config.WriteIdleTimeout); err != nil {
			providerBytes, _ = read.close()
			return 0, providerBytes, http.StatusServiceUnavailable, RequestFailureWriteFailed, false, ErrContentUnavailable
		}
	}
	writer.WriteHeader(plan.Status)
	if request.Method == http.MethodGet && plan.ContentLength > 0 {
		deadlineWriter := &gatewayDeadlineWriter{
			writer: writer, broker: broker, grant: grant, actor: actor, session: session, asset: asset, lease: lease,
			idleTimeout: config.WriteIdleTimeout, heartbeatInterval: config.LeaseHeartbeat, ctx: ctx,
		}
		responseBytes, err = io.CopyN(deadlineWriter, read.body, plan.ContentLength)
		if err != nil {
			providerBytes, closeErr := read.close()
			return responseBytes, providerBytes, http.StatusInternalServerError, RequestFailureWriteFailed,
				closeErr == nil && providerBytes >= 0, err
		}
	}
	providerBytes, closeErr := read.close()
	if closeErr != nil || providerBytes < 0 {
		return responseBytes, providerBytes, http.StatusBadGateway, RequestFailureSourceFailed, false, ErrContentUnavailable
	}
	return responseBytes, providerBytes, plan.Status, "", true, nil
}

type gatewayReadState struct {
	source        SourceSession
	sourceReader  SourceReader
	cacheLease    *CacheLease
	body          io.Reader
	providerBytes int64
	closed        bool
}

func (state *gatewayReadState) close() (int64, error) {
	if state == nil {
		return -1, ErrContentUnavailable
	}
	if state.closed {
		return state.providerBytes, nil
	}
	state.closed = true
	var cacheErr error
	if state.cacheLease != nil {
		cacheErr = state.cacheLease.Close()
	}
	var sourceErr error
	if state.source != nil {
		sourceErr = state.source.Close()
	}
	if state.sourceReader != nil {
		observed := state.sourceReader.ProviderBytes()
		if observed < 0 || state.providerBytes < 0 || observed > math.MaxInt64-state.providerBytes {
			state.providerBytes = -1
		} else {
			state.providerBytes += observed
		}
	}
	return state.providerBytes, errors.Join(cacheErr, sourceErr)
}

func (broker *Broker) prepareGatewayRead(
	ctx context.Context,
	request GatewayRequest,
	grant model.BackupAssetDeliveryGrant,
	asset AuthorizedAsset,
	plan RepresentationPlan,
	renderer *RendererPolicy,
) (*gatewayReadState, error) {
	state := &gatewayReadState{}
	sourceRequest, transformed, err := gatewaySourceRequest(grant, plan, request.Method)
	if err != nil {
		return state, ErrContentUnavailable
	}
	if request.Method == http.MethodHead || plan.ContentLength == 0 || transformed {
		if err := broker.openGatewaySource(ctx, state, grant, sourceRequest); err != nil {
			return state, err
		}
		if request.Method == http.MethodGet && plan.ContentLength > 0 {
			payload := make([]byte, grant.RepresentationSourceBytes)
			if _, err := io.ReadFull(state.sourceReader, payload); err != nil {
				zeroBytes(payload)
				return state, ErrContentUnavailable
			}
			renderPlan, renderErr := renderer.Prepare(RenderRequest{
				Action: DeliveryAction(grant.Action), Renderer: Renderer(grant.Renderer), Profile: RendererProfile(grant.Profile),
				Range: RangePolicy(grant.RangePolicy), SourceSize: grant.SourceSize, Prefix: payload,
				ProviderMediaType: state.source.Stat().MediaType, Filename: gatewayFilename(grant),
			})
			zeroBytes(payload)
			if renderErr != nil || !renderPlanMatchesGrant(renderPlan, grant) {
				return state, ErrContentNotFound
			}
			state.body = bytes.NewReader(renderPlan.Bytes)
		}
		return state, nil
	}

	cache := broker.currentCache()
	object := cacheObjectForGrant(grant)
	cacheEligible := cache != nil && cacheEligibleRenderer(Renderer(grant.Renderer)) && validCacheObject(object)
	if cacheEligible {
		cacheLease, cacheErr := cache.OpenRange(object, plan.Range.Offset, plan.ContentLength)
		if cacheErr == nil {
			broker.metrics.ObserveCache(MetricCacheHit)
			return broker.openGatewayCacheRead(ctx, state, grant, cacheLease)
		}
		broker.metrics.ObserveCache(cacheMetricOutcome(cache, cacheErr))
	}
	if plan.Range.Kind != HTTPRangeFull && !asset.RangeProven {
		return state, errors.Join(ErrContentNotFound, ErrContentCacheUnavailable)
	}

	if cacheEligible && !asset.RangeProven && plan.Range.Kind == HTTPRangeFull {
		materialization, hit, beginErr := cache.BeginMaterialization(object)
		if beginErr == nil && materialization == nil && hit.RangeCapable {
			broker.metrics.ObserveCache(MetricCacheHit)
			cacheLease, openErr := cache.OpenRange(object, 0, plan.ContentLength)
			if openErr == nil {
				if err := broker.upgradeGrantRange(ctx, grant); err != nil {
					_ = cacheLease.Close()
					return state, err
				}
				return broker.openGatewayCacheRead(ctx, state, grant, cacheLease)
			}
		}
		if beginErr != nil {
			broker.metrics.ObserveCache(cacheMetricOutcome(cache, beginErr))
		}
		if materialization != nil {
			defer func() { _ = materialization.Abort() }()
			if err := broker.openGatewaySource(ctx, state, grant, sourceRequest); err != nil {
				return state, err
			}
			source := state.source
			state.source, state.sourceReader = nil, nil
			info, commitErr := materialization.Commit(ctx, source)
			if commitErr != nil || !info.RangeCapable || info.ProviderBytes < 0 {
				broker.metrics.ObserveCache(cacheMetricOutcome(cache, commitErr))
				state.providerBytes = -1
				return state, ErrContentUnavailable
			}
			state.providerBytes = info.ProviderBytes
			cacheLease, openErr := cache.OpenRange(object, 0, plan.ContentLength)
			if openErr != nil {
				return state, ErrContentUnavailable
			}
			if err := broker.upgradeGrantRange(ctx, grant); err != nil {
				_ = cacheLease.Close()
				return state, err
			}
			return broker.openGatewayCacheRead(ctx, state, grant, cacheLease)
		}
	}

	if err := broker.openGatewaySource(ctx, state, grant, sourceRequest); err != nil {
		return state, err
	}
	if state.sourceReader == nil {
		return state, ErrContentUnavailable
	}
	state.body = state.sourceReader
	return state, nil
}

func (broker *Broker) openGatewaySource(
	ctx context.Context,
	state *gatewayReadState,
	grant model.BackupAssetDeliveryGrant,
	request SourceRequest,
) error {
	broker.mu.Lock()
	binding, derived := broker.derivedBindings[grant.ID]
	broker.mu.Unlock()
	var bindingPointer *DerivedRepresentation
	if derived {
		bindingPointer = &binding
	}
	source, err := broker.openRepresentationSource(ctx, bindingPointer, request)
	if err != nil {
		return ErrContentUnavailable
	}
	state.source = source
	if !gatewaySourceMatchesGrant(source, grant, request.Mode) || source.Revalidate(ctx) != nil {
		return ErrContentNotFound
	}
	if request.Mode != SourceModeStat {
		state.sourceReader = source.Reader()
		if state.sourceReader == nil {
			return ErrContentUnavailable
		}
	}
	return nil
}

func (broker *Broker) openGatewayCacheRead(
	ctx context.Context,
	state *gatewayReadState,
	grant model.BackupAssetDeliveryGrant,
	cacheLease *CacheLease,
) (*gatewayReadState, error) {
	state.cacheLease = cacheLease
	if err := broker.openGatewaySource(ctx, state, grant, gatewayStatSourceRequest(grant)); err != nil {
		return state, err
	}
	state.body = cacheLease
	return state, nil
}

func (broker *Broker) upgradeGrantRange(ctx context.Context, grant model.BackupAssetDeliveryGrant) error {
	result := broker.db.WithContext(ctx).Model(&model.BackupAssetDeliveryGrant{}).
		Where("id = ? AND state = ? AND range_policy = ?", grant.ID, DeliveryActive, RangeNone).
		Updates(map[string]any{"range_policy": RangeSingle, "version": gorm.Expr("version + 1"), "updated_at": broker.now().UTC()})
	if result.Error != nil {
		return ErrContentUnavailable
	}
	if result.RowsAffected == 1 {
		return nil
	}
	var current string
	query := broker.db.WithContext(ctx).Model(&model.BackupAssetDeliveryGrant{}).
		Select("range_policy").Where("id = ? AND state = ?", grant.ID, DeliveryActive).Limit(1).Scan(&current)
	if query.Error != nil || query.RowsAffected != 1 || current != string(RangeSingle) {
		return ErrContentUnavailable
	}
	return nil
}

func (broker *Broker) finalizeGatewayRequest(
	ctx context.Context,
	reservation Reservation,
	state RequestState,
	status int,
	failure RequestFailureCode,
	providerBytes int64,
	responseBytes int64,
	evidenceKnown bool,
) error {
	finalization, err := broker.budget.Finalize(nonNilContext(ctx), FinalizeIntent{
		RequestID: reservation.RequestID, ExpectedRequestVersion: reservation.RequestVersion,
		State: state, HTTPStatus: status, FailureCode: failure,
		ProviderBytes: providerBytes, ResponseBytes: responseBytes, EvidenceKnown: evidenceKnown,
	})
	if err == nil {
		broker.metrics.AddBytes(MetricBytesCharged, finalization.ChargedBytes)
	}
	return err
}

func (broker *Broker) beginIssue() error {
	broker.mu.Lock()
	defer broker.mu.Unlock()
	if broker.closed || !broker.accepting {
		return ErrBrokerClosed
	}
	broker.issues.Add(1)
	return nil
}

// Shutdown closes ticket admission before waiting for in-progress issuance,
// then revokes every retained grant and releases every exact content lease.
// Cleanup errors are joined so one failed stage cannot strand later leases.
func (broker *Broker) Shutdown(ctx context.Context) error {
	if broker == nil {
		return ErrInvalidBrokerRequest
	}
	return broker.drain(ctx, "shutdown", true)
}

func (broker *Broker) Drain(ctx context.Context, reason string) error {
	if broker == nil || reason != "feature_disabled" {
		return ErrInvalidBrokerRequest
	}
	return broker.drain(ctx, reason, false)
}

func (broker *Broker) Resume() error {
	if broker == nil {
		return ErrInvalidBrokerRequest
	}
	broker.mu.Lock()
	defer broker.mu.Unlock()
	if broker.closed {
		return ErrBrokerClosed
	}
	broker.accepting = true
	return nil
}

func (broker *Broker) drain(ctx context.Context, reason string, permanent bool) error {
	ctx = nonNilContext(ctx)
	broker.mu.Lock()
	if broker.closed && !permanent {
		broker.mu.Unlock()
		return ErrBrokerClosed
	}
	broker.accepting = false
	if permanent {
		broker.closed = true
	}
	broker.mu.Unlock()

	broker.issues.Wait()
	readWaits := broker.cancelReads("")
	var cleanupErrors []error
	if err := waitForActiveReads(ctx, readWaits); err != nil {
		cleanupErrors = append(cleanupErrors, err)
	}

	broker.mu.Lock()
	leases := make(map[string]*ContentLeaseSession)
	for grantID, lease := range broker.leases {
		if len(broker.reads[grantID]) == 0 {
			leases[grantID] = lease
			delete(broker.leases, grantID)
			delete(broker.assets, grantID)
			delete(broker.derivedBindings, grantID)
		}
	}
	broker.mu.Unlock()

	now := broker.now().UTC()
	for grantID, lease := range leases {
		if err := broker.revokeGrant(ctx, grantID, reason, now); err != nil {
			cleanupErrors = append(cleanupErrors, err)
		}
		if err := lease.Release(ctx); err != nil {
			cleanupErrors = append(cleanupErrors, err)
		}
	}
	switch reason {
	case "feature_disabled":
		broker.metrics.ObserveReason(MetricReasonFeatureDisabled)
	case "shutdown":
		broker.metrics.ObserveReason(MetricReasonShutdown)
	}
	return errors.Join(cleanupErrors...)
}

// RevokeSession revokes all grants bound to one non-bearer login-session JTI,
// cancels matching in-memory reads, waits for reader cleanup, and only then
// releases the exact retained leases.
func (broker *Broker) RevokeSession(ctx context.Context, sessionJTI, reason string) error {
	if broker == nil || backupasset.ValidateOpaqueID(sessionJTI) != nil ||
		(reason != "logout" && reason != "session_revoked") {
		return ErrInvalidBrokerRequest
	}
	ctx = nonNilContext(ctx)
	var rows []struct{ ID string }
	var revokeErrors []error
	query := broker.db.WithContext(ctx).Model(&model.BackupAssetDeliveryGrant{}).
		Select("id").Where("session_jti = ? AND state IN ?", sessionJTI,
		[]string{string(DeliveryIssued), string(DeliveryActive), string(DeliveryDraining)}).
		Scan(&rows)
	if query.Error != nil {
		revokeErrors = append(revokeErrors, query.Error)
	}
	now := broker.now().UTC()
	update := broker.db.WithContext(ctx).Model(&model.BackupAssetDeliveryGrant{}).
		Where("session_jti = ? AND state IN ?", sessionJTI,
			[]string{string(DeliveryIssued), string(DeliveryActive), string(DeliveryDraining)}).
		Updates(map[string]any{
			"state": DeliveryRevoked, "revocation_reason": reason, "revoked_at": now,
			"updated_at": now, "version": gorm.Expr("version + 1"),
		})
	if update.Error != nil {
		revokeErrors = append(revokeErrors, update.Error)
	}
	waits := broker.cancelReads(sessionJTI)
	if err := waitForActiveReads(ctx, waits); err != nil {
		revokeErrors = append(revokeErrors, err)
	}

	grantIDs := make(map[string]bool, len(rows)+len(waits))
	for _, row := range rows {
		grantIDs[row.ID] = true
	}
	for grantID := range waits {
		grantIDs[grantID] = true
	}
	for grantID := range grantIDs {
		broker.mu.Lock()
		lease := broker.leases[grantID]
		if len(broker.reads[grantID]) == 0 {
			delete(broker.leases, grantID)
			delete(broker.assets, grantID)
			delete(broker.derivedBindings, grantID)
		} else {
			lease = nil
		}
		broker.mu.Unlock()
		if lease != nil {
			if err := lease.Release(ctx); err != nil {
				revokeErrors = append(revokeErrors, err)
			}
		}
	}
	if reason == "logout" {
		broker.metrics.ObserveReason(MetricReasonSessionRevoked)
	}
	return errors.Join(revokeErrors...)
}

func (broker *Broker) cancelReads(sessionJTI string) map[string][]<-chan struct{} {
	waits := make(map[string][]<-chan struct{})
	broker.mu.Lock()
	defer broker.mu.Unlock()
	for grantID, reads := range broker.reads {
		for _, read := range reads {
			if sessionJTI != "" && read.sessionJTI != sessionJTI {
				continue
			}
			read.cancel()
			waits[grantID] = append(waits[grantID], read.done)
		}
	}
	return waits
}

func waitForActiveReads(ctx context.Context, waits map[string][]<-chan struct{}) error {
	for _, grantWaits := range waits {
		for _, done := range grantWaits {
			select {
			case <-done:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	return nil
}

func (broker *Broker) resolveDerivedRepresentation(
	ctx context.Context,
	request IssueRequest,
	asset AuthorizedAsset,
) (*DerivedRepresentation, error) {
	if broker.derived == nil || request.Action != DeliveryPreview {
		return nil, nil
	}
	policyRevision, err := broker.securityPolicyRevision(ctx)
	if err != nil || strings.TrimSpace(policyRevision) != policyRevision || policyRevision == "" || len(policyRevision) > 128 {
		return nil, ErrContentSourceUnavailable
	}
	binding, err := broker.derived.Resolve(ctx, DerivedRepresentationRequest{
		Ref: asset.Ref, CatalogGenerationID: asset.CatalogGenerationID,
		SourceFingerprint: asset.SourceFingerprint, SecurityPolicyRevision: policyRevision,
		Provider: asset.Provider, Renderer: request.Renderer, Profile: request.Profile,
	})
	if errors.Is(err, ErrDerivedRepresentationUnavailable) {
		return nil, nil
	}
	if err != nil || !derivedRepresentationMatchesAsset(binding, asset, request) {
		return nil, ErrContentSourceUnavailable
	}
	return &binding, nil
}

func authorizedAssetForDerived(asset AuthorizedAsset, binding DerivedRepresentation) AuthorizedAsset {
	asset.EntryFingerprint = binding.EntryFingerprint
	asset.FingerprintStrength = "strong"
	asset.Size = binding.Size
	asset.ModifiedAt = cloneDerivedTime(binding.ModifiedAt)
	asset.MediaType = binding.MediaType
	asset.RangeProven = false
	return asset
}

func derivedRepresentationMatchesAsset(
	binding DerivedRepresentation,
	asset AuthorizedAsset,
	request IssueRequest,
) bool {
	return validDerivedBinding(binding) && binding.Ref == asset.Ref && binding.CatalogGenerationID == asset.CatalogGenerationID &&
		binding.SourceFingerprint == asset.SourceFingerprint && binding.Provider == asset.Provider &&
		binding.Renderer == request.Renderer && binding.Profile == request.Profile
}

func (broker *Broker) readTicketPrefix(
	ctx context.Context,
	asset AuthorizedAsset,
	derived *DerivedRepresentation,
	classifier *Classifier,
	renderer *RendererPolicy,
) ([]byte, SourceStat, SourceCapabilities, error) {
	if classifier == nil || renderer == nil {
		return nil, SourceStat{}, SourceCapabilities{}, ErrInvalidBrokerRequest
	}
	readLimit := max(classifier.config.ScanBytes+1, max(renderer.config.TextBytes, renderer.config.HexBytes))
	readLimit = max(readLimit, 512)
	readLimit = min(readLimit, asset.Size)
	providerLimit := readLimit
	if readLimit < asset.Size {
		providerLimit++
	}
	mode := SourceModeSequential
	if asset.Size == 0 {
		mode = SourceModeStat
	}
	session, err := broker.openRepresentationSource(ctx, derived, SourceRequest{
		Ref: asset.Ref, CatalogGenerationID: asset.CatalogGenerationID,
		ExpectedSource: asset.SourceFingerprint, ExpectedEntry: asset.EntryFingerprint,
		Mode: mode, MaxBytes: providerLimit,
	})
	if err != nil {
		return nil, SourceStat{}, SourceCapabilities{}, err
	}
	stat, capabilities := session.Stat(), session.Capabilities()
	if stat.Size != asset.Size || stat.SourceFingerprint != asset.SourceFingerprint ||
		stat.EntryFingerprint != asset.EntryFingerprint || capabilities.Provider != asset.Provider {
		_ = session.Close()
		return nil, stat, capabilities, ErrContentSourceUnavailable
	}
	prefix := make([]byte, int(readLimit))
	if readLimit > 0 {
		reader := session.Reader()
		if reader == nil {
			_ = session.Close()
			return nil, stat, capabilities, ErrContentSourceUnavailable
		}
		if _, err := io.ReadFull(reader, prefix); err != nil {
			_ = session.Close()
			zeroBytes(prefix)
			return nil, stat, capabilities, ErrContentSourceUnavailable
		}
	}
	if err := session.Close(); err != nil {
		zeroBytes(prefix)
		return nil, stat, capabilities, err
	}
	return prefix, stat, capabilities, nil
}

func (broker *Broker) openRepresentationSource(
	ctx context.Context,
	binding *DerivedRepresentation,
	request SourceRequest,
) (SourceSession, error) {
	if binding == nil {
		return broker.source.OpenContentSource(ctx, request)
	}
	if broker.derived == nil {
		return nil, ErrDerivedRepresentationUnavailable
	}
	return broker.derived.Open(ctx, *binding, request)
}

func validIssueRequest(request IssueRequest, now time.Time) bool {
	if request.Actor.UserID == 0 || request.Session.UserID != request.Actor.UserID || request.Session.Role != request.Actor.Role ||
		backupasset.ValidateOpaqueID(request.Session.JTI) != nil || !request.Session.ExpiresAt.After(now) ||
		backupasset.ValidateAssetRef(request.Ref) != nil {
		return false
	}
	if request.Action == DeliveryDownload {
		if request.Proof == nil {
			return false
		}
		return request.Renderer == RendererAttachment && request.Profile == ProfileOriginalV1 &&
			validProof(request.Proof, auth.StepUpActionAssetDownload, minTime(request.Proof.ExpiresAt, request.Session.ExpiresAt), now)
	}
	if request.Action != DeliveryPreview || request.Renderer == RendererAttachment ||
		!validCacheRendererProfile(request.Renderer, request.Profile) {
		return false
	}
	return request.Proof == nil || request.Proof.Action == auth.StepUpActionAssetSecretReveal &&
		backupasset.ValidateOpaqueID(request.Proof.ID) == nil && request.Proof.ExpiresAt.After(now)
}

func validAuthorizedAsset(asset AuthorizedAsset, expected backupasset.AssetRef) bool {
	return asset.Ref == expected && backupasset.ValidateAssetRef(asset.Ref) == nil &&
		backupasset.ValidateOpaqueID(asset.CatalogGenerationID) == nil && backupasset.ValidateOpaqueID(asset.RepositoryID) == nil &&
		validCacheProvider(asset.Provider) && len(asset.SourceFingerprint) > 0 && len(asset.SourceFingerprint) <= 128 &&
		len(asset.EntryFingerprint) <= 128 && (asset.FingerprintStrength == "strong" || asset.FingerprintStrength == "weak" || asset.FingerprintStrength == "none") &&
		asset.Size >= 0 && strings.TrimSpace(asset.Path) != "" && strings.TrimSpace(asset.Name) != "" &&
		validAuthorizedAssetSearchClassification(asset)
}

func validAuthorizedAssetSearchClassification(asset AuthorizedAsset) bool {
	if asset.SearchClassificationRevision == 0 {
		return asset.SearchClassification == ""
	}
	if asset.SearchClassificationRevision < 0 {
		return false
	}
	switch asset.SearchClassification {
	case ClassificationNonSecret, ClassificationSecret, ClassificationUnknown:
		return true
	default:
		return false
	}
}

func searchClassificationEvidence(asset AuthorizedAsset) *SearchClassificationEvidence {
	if !validAuthorizedAssetSearchClassification(asset) || asset.SearchClassificationRevision == 0 {
		return nil
	}
	return &SearchClassificationEvidence{
		Classification: asset.SearchClassification, CatalogGenerationID: asset.CatalogGenerationID,
		SourceFingerprint: asset.SourceFingerprint, Revision: asset.SearchClassificationRevision,
	}
}

func validTicketMaterial(material TicketMaterial) bool {
	return backupasset.ValidateOpaqueID(material.GrantID) == nil && backupasset.ValidateOpaqueID(material.DeliveryID) == nil &&
		material.GrantID != material.DeliveryID && VerifyCookieSecret(material.CookieSecretHash, material.CookieSecret)
}

func validBrokerConfig(config BrokerConfig) bool {
	return config.TicketTimeout > 0 && config.TicketTimeout <= 25*time.Second &&
		config.WriteIdleTimeout >= 5*time.Second && config.WriteIdleTimeout <= 2*time.Minute &&
		config.LeaseHeartbeat > 0 &&
		config.PreviewTTL > 0 && config.MediaTTL > 0 && config.IdleTTL > 0 &&
		config.MaxBytesPerRequest > 0 && config.MaxCumulativeBytes >= config.MaxBytesPerRequest &&
		config.MaxRequests > 0 && config.MaxInFlight > 0 &&
		config.Classification.ScanBytes > 0 && config.Classification.ScanBytes <= 4<<20 &&
		config.Renderer.TextBytes > 0 && config.Renderer.HexBytes > 0 && config.Renderer.RasterMaxPixels > 0 &&
		config.Renderer.PDFMaxBytes > 0 && config.Renderer.MediaMaxBytes > 0
}

func newBrokerPolicies(config BrokerConfig) (*Classifier, *RendererPolicy, error) {
	classifier, err := NewClassifier(config.Classification)
	if err != nil {
		return nil, nil, err
	}
	renderer, err := NewRendererPolicy(config.Renderer)
	if err != nil {
		return nil, nil, err
	}
	return classifier, renderer, nil
}

func buildIssuedGrant(
	request IssueRequest,
	asset AuthorizedAsset,
	material TicketMaterial,
	lease ContentLeaseBinding,
	deadlines GrantDeadlines,
	renderPlan RenderPlan,
	classification ClassificationResult,
	etag string,
	config BrokerConfig,
	now time.Time,
) model.BackupAssetDeliveryGrant {
	pointID, catalogID, entryID := asset.Ref.RecoveryPointID, asset.CatalogGenerationID, asset.Ref.EntryID
	grant := model.BackupAssetDeliveryGrant{
		ID: material.GrantID, DeliveryID: material.DeliveryID, ResourceKind: string(DeliveryResourceBackupAsset),
		RecoveryPointID: &pointID, CatalogGenerationID: &catalogID, EntryID: &entryID,
		OwnerUserID: request.Actor.UserID, SessionJTI: request.Session.JTI,
		SessionTokenVersion: request.Session.TokenVersion, SessionRole: request.Session.Role, SessionExpiresAt: request.Session.ExpiresAt.UTC(),
		Action: string(request.Action), MethodPolicy: string(MethodGetHead), RangePolicy: string(renderPlan.Range),
		Renderer: string(request.Renderer), Profile: string(request.Profile), Classification: string(classification.Classification),
		ClassificationRevision: int(classification.PolicyRevision), ClassificationSourceRevision: classification.SourceRevision,
		ProviderKind: string(asset.Provider), SourceFingerprint: asset.SourceFingerprint,
		EntryFingerprint: asset.EntryFingerprint, FingerprintStrength: asset.FingerprintStrength,
		RepresentationETag: etag, SourceSize: asset.Size, SourceModifiedAt: asset.ModifiedAt,
		DetectedMediaType: renderPlan.MediaType, RepresentationSourceBytes: renderPlan.SourceBytes,
		RepresentationSize: renderPlan.Size, RepresentationTruncated: renderPlan.Truncated,
		CookieSecretHash: material.CookieSecretHash, State: string(DeliveryIssued),
		LeaseID: lease.LeaseID, LeaseAttemptID: lease.AttemptID, LeaseFenceTokenHash: lease.FenceTokenHash,
		AbsoluteExpiresAt: deadlines.AbsoluteExpiresAt, IdleExpiresAt: deadlines.IdleExpiresAt,
		IdleTTLSeconds: int64(config.IdleTTL / time.Second), LastActivityAt: now,
		MaxBytesPerRequest: config.MaxBytesPerRequest, MaxCumulativeBytes: config.MaxCumulativeBytes,
		MaxRequests: config.MaxRequests, MaxInFlight: config.MaxInFlight,
		Version: 1, AuditState: "none", CreatedAt: now, UpdatedAt: now,
	}
	if request.Proof != nil {
		action, proofID, expiry := string(request.Proof.Action), request.Proof.ID, request.Proof.ExpiresAt.UTC()
		grant.StepUpAction, grant.StepUpProofID, grant.StepUpExpiresAt = &action, &proofID, &expiry
	}
	return grant
}

func ticketAuditInput(
	request IssueRequest,
	asset AuthorizedAsset,
	grant model.BackupAssetDeliveryGrant,
	outcome backupasset.AuditOutcome,
	failureCode string,
) backupasset.AuditEventInput {
	action := backupasset.AuditActionPreviewTicket
	if request.Action == DeliveryDownload {
		action = backupasset.AuditActionAssetDownloadTicket
	}
	stepUpAction, stepUpProofID := "", ""
	if request.Proof != nil {
		stepUpAction, stepUpProofID = string(request.Proof.Action), request.Proof.ID
	}
	return backupasset.AuditEventInput{
		Actor:  backupasset.AuditActor{UserID: request.Actor.UserID, Username: request.Actor.Username, Role: request.Actor.Role},
		Action: action, Outcome: outcome,
		RepositoryID: asset.RepositoryID, RecoveryPointID: asset.Ref.RecoveryPointID, EntryID: asset.Ref.EntryID,
		ItemCount: 1, ByteCount: grant.RepresentationSize, StepUpAction: stepUpAction, StepUpProofID: stepUpProofID,
		GrantID: grant.ID, FailureCode: failureCode,
		Fields: map[backupasset.AuditField]any{
			backupasset.AuditFieldRenderer: grant.Renderer, backupasset.AuditFieldProfile: grant.Profile,
			backupasset.AuditFieldSource: grant.Classification,
		},
	}
}

func ticketFailureAuditOutcome(err error) (backupasset.AuditOutcome, string) {
	switch {
	case errors.Is(err, ErrInvalidDeliveryProduct), errors.Is(err, ErrInvalidRendererRequest),
		errors.Is(err, ErrRendererUnsupported), errors.Is(err, ErrMIMEConfusion), errors.Is(err, ErrRasterLimit):
		return backupasset.AuditOutcomeBlocked, "request_blocked"
	default:
		return backupasset.AuditOutcomeFailure, "request_failed"
	}
}

func representationETag(
	asset AuthorizedAsset,
	product DeliveryProduct,
	plan RenderPlan,
	classification ClassificationResult,
) string {
	var buffer bytes.Buffer
	modifiedAt := ""
	if asset.ModifiedAt != nil {
		modifiedAt = asset.ModifiedAt.UTC().Format(time.RFC3339Nano)
	}
	for _, value := range []string{
		asset.Ref.RecoveryPointID, asset.Ref.EntryID, asset.CatalogGenerationID,
		asset.SourceFingerprint, asset.EntryFingerprint, asset.FingerprintStrength,
		modifiedAt, string(product.Renderer), string(product.Profile), string(product.Classification), plan.MediaType,
	} {
		_ = binary.Write(&buffer, binary.BigEndian, uint32(len(value)))
		_, _ = buffer.WriteString(value)
	}
	for _, value := range []int64{
		asset.Size, plan.SourceBytes, plan.Size, classification.PolicyRevision, classification.SourceRevision,
	} {
		_ = binary.Write(&buffer, binary.BigEndian, value)
	}
	sum := sha256.Sum256(buffer.Bytes())
	prefix := ""
	if asset.FingerprintStrength != "strong" {
		prefix = "W/"
	}
	return prefix + `"` + hex.EncodeToString(sum[:]) + `"`
}

func (broker *Broker) revokeIssuedGrant(ctx context.Context, grantID, reason string, now time.Time) {
	_ = broker.revokeGrant(ctx, grantID, reason, now)
}

func (broker *Broker) revokeGrant(ctx context.Context, grantID, reason string, now time.Time) error {
	return broker.db.WithContext(nonNilContext(ctx)).Model(&model.BackupAssetDeliveryGrant{}).
		Where("id = ? AND state IN ?", grantID, []string{string(DeliveryIssued), string(DeliveryActive), string(DeliveryDraining)}).
		Updates(map[string]any{
			"state": DeliveryRevoked, "revocation_reason": reason, "revoked_at": now,
			"updated_at": now, "version": gorm.Expr("version + 1"),
		}).Error
}

func (broker *Broker) revokeGrantAfterRead(ctx context.Context, grantID, currentRequestID, reason string) error {
	ctx = nonNilContext(ctx)
	var cleanupErrors []error
	if err := broker.revokeGrant(ctx, grantID, reason, broker.now().UTC()); err != nil {
		cleanupErrors = append(cleanupErrors, err)
	}
	waits := make(map[string][]<-chan struct{})
	broker.mu.Lock()
	for requestID, active := range broker.reads[grantID] {
		if requestID == currentRequestID {
			continue
		}
		active.cancel()
		waits[grantID] = append(waits[grantID], active.done)
	}
	broker.mu.Unlock()
	if err := waitForActiveReads(ctx, waits); err != nil {
		cleanupErrors = append(cleanupErrors, err)
	}
	broker.mu.Lock()
	lease := broker.leases[grantID]
	remaining := broker.reads[grantID]
	if len(remaining) == 0 || len(remaining) == 1 && remaining[currentRequestID].done != nil {
		delete(broker.leases, grantID)
		delete(broker.assets, grantID)
		delete(broker.derivedBindings, grantID)
	} else {
		lease = nil
	}
	broker.mu.Unlock()
	if lease != nil {
		if err := lease.Release(ctx); err != nil {
			cleanupErrors = append(cleanupErrors, err)
		}
	}
	return errors.Join(cleanupErrors...)
}

type gatewayDeadlineWriter struct {
	writer            http.ResponseWriter
	broker            *Broker
	grant             model.BackupAssetDeliveryGrant
	actor             DeliveryActor
	session           DeliverySession
	asset             AuthorizedAsset
	lease             *ContentLeaseSession
	idleTimeout       time.Duration
	heartbeatInterval time.Duration
	ctx               context.Context
}

func (writer *gatewayDeadlineWriter) Write(payload []byte) (int, error) {
	if writer == nil || writer.writer == nil || writer.broker == nil {
		return 0, ErrContentUnavailable
	}
	if err := writer.broker.gatewayHeartbeat(
		writer.ctx, writer.grant, writer.actor, writer.session, writer.asset, writer.lease, writer.heartbeatInterval,
	); err != nil {
		return 0, err
	}
	if err := setGatewayWriteDeadline(
		writer.writer, writer.broker.now().UTC(), writer.grant.AbsoluteExpiresAt, writer.idleTimeout,
	); err != nil {
		return 0, err
	}
	return writer.writer.Write(payload)
}

func validGatewayRequest(request GatewayRequest) bool {
	if backupasset.ValidateOpaqueID(request.DeliveryID) != nil ||
		(request.Method != http.MethodGet && request.Method != http.MethodHead) ||
		request.RawCookie == "" || len(request.RawCookie) > maxCookieHeaderLen ||
		len(request.RangeHeaders) > 2 || len(request.IfRangeHeaders) > 2 {
		return false
	}
	for _, values := range [][]string{request.RangeHeaders, request.IfRangeHeaders} {
		for _, value := range values {
			if len(value) == 0 || len(value) > 512 || strings.ContainsAny(value, "\r\n\x00") {
				return false
			}
		}
	}
	return true
}

func validGatewayGrant(grant model.BackupAssetDeliveryGrant, deliveryID string, now time.Time) bool {
	if backupasset.ValidateOpaqueID(grant.ID) != nil || grant.DeliveryID != deliveryID ||
		grant.ResourceKind != string(DeliveryResourceBackupAsset) || grant.RecoveryPointID == nil ||
		grant.CatalogGenerationID == nil || grant.EntryID == nil || grant.RecoveryJobID != nil || grant.RecoveryResultID != nil ||
		backupasset.ValidateAssetRef(backupasset.AssetRef{RecoveryPointID: *grant.RecoveryPointID, EntryID: *grant.EntryID}) != nil ||
		backupasset.ValidateOpaqueID(*grant.CatalogGenerationID) != nil || grant.OwnerUserID == 0 ||
		backupasset.ValidateOpaqueID(grant.SessionJTI) != nil ||
		(grant.SessionRole != "admin" && grant.SessionRole != "operator") ||
		grant.MethodPolicy != string(MethodGetHead) || grant.State != string(DeliveryActive) ||
		!now.Before(grant.IdleExpiresAt.UTC()) || !now.Before(grant.AbsoluteExpiresAt.UTC()) ||
		!now.Before(grant.SessionExpiresAt.UTC()) || grant.IdleExpiresAt.After(grant.AbsoluteExpiresAt) ||
		grant.AbsoluteExpiresAt.After(grant.SessionExpiresAt) || grant.ClassificationRevision <= 0 ||
		grant.ClassificationSourceRevision <= 0 || !validCacheProvider(backupasset.ProviderKind(grant.ProviderKind)) ||
		len(grant.SourceFingerprint) == 0 || len(grant.SourceFingerprint) > 128 || len(grant.EntryFingerprint) > 128 ||
		(grant.FingerprintStrength != "strong" && grant.FingerprintStrength != "weak" && grant.FingerprintStrength != "none") ||
		grant.SourceSize < 0 || grant.RepresentationSourceBytes < 0 || grant.RepresentationSourceBytes > grant.SourceSize ||
		grant.RepresentationSize < 0 || !validEntityTag(grant.RepresentationETag) || !validGatewayMediaType(grant) ||
		backupasset.ValidateOpaqueID(grant.LeaseID) != nil || backupasset.ValidateOpaqueID(grant.LeaseAttemptID) != nil ||
		!lowerHexOfLength(grant.LeaseFenceTokenHash, 64) || grant.IdleTTLSeconds <= 0 ||
		grant.MaxBytesPerRequest <= 0 || grant.MaxCumulativeBytes < grant.MaxBytesPerRequest ||
		grant.MaxRequests <= 0 || grant.MaxInFlight <= 0 || grant.Version <= 0 {
		return false
	}
	proof, ok := gatewayGrantProof(grant)
	if !ok {
		return false
	}
	product := DeliveryProduct{
		Action: DeliveryAction(grant.Action), Method: MethodPolicy(grant.MethodPolicy), Range: RangePolicy(grant.RangePolicy),
		Renderer: Renderer(grant.Renderer), Profile: RendererProfile(grant.Profile),
		Classification: Classification(grant.Classification), Proof: proof,
		AbsoluteExpiresAt: grant.AbsoluteExpiresAt,
	}
	if ValidateDeliveryProduct(product, now) != nil {
		return false
	}
	transformed := grant.Renderer == string(RendererEscapedText) || grant.Renderer == string(RendererMetadataHex)
	if transformed {
		return grant.RangePolicy == string(RangeNone) && grant.RepresentationTruncated == (grant.RepresentationSourceBytes < grant.SourceSize)
	}
	return !grant.RepresentationTruncated && grant.RepresentationSourceBytes == grant.SourceSize &&
		grant.RepresentationSize == grant.SourceSize
}

func gatewayGrantProof(grant model.BackupAssetDeliveryGrant) (*StepUpProof, bool) {
	if grant.StepUpAction == nil && grant.StepUpProofID == nil && grant.StepUpExpiresAt == nil {
		return nil, true
	}
	if grant.StepUpAction == nil || grant.StepUpProofID == nil || grant.StepUpExpiresAt == nil ||
		backupasset.ValidateOpaqueID(*grant.StepUpProofID) != nil || grant.StepUpExpiresAt.Before(grant.AbsoluteExpiresAt) {
		return nil, false
	}
	return &StepUpProof{
		Action: auth.StepUpAction(*grant.StepUpAction), ID: *grant.StepUpProofID, ExpiresAt: grant.StepUpExpiresAt.UTC(),
	}, true
}

func validGatewayMediaType(grant model.BackupAssetDeliveryGrant) bool {
	if grant.DetectedMediaType == "" || len(grant.DetectedMediaType) > 128 || strings.ContainsAny(grant.DetectedMediaType, "\r\n\x00") {
		return false
	}
	switch Renderer(grant.Renderer) {
	case RendererEscapedText, RendererMetadataHex:
		return grant.DetectedMediaType == "text/plain; charset=utf-8"
	case RendererSafeRaster:
		return grant.DetectedMediaType == "image/png" || grant.DetectedMediaType == "image/jpeg" ||
			grant.DetectedMediaType == "image/gif" || grant.DetectedMediaType == "image/webp"
	case RendererSameOriginPDF:
		return grant.DetectedMediaType == "application/pdf"
	case RendererNativeAudio:
		return strings.HasPrefix(grant.DetectedMediaType, "audio/")
	case RendererNativeVideo:
		return strings.HasPrefix(grant.DetectedMediaType, "video/")
	case RendererAttachment:
		return grant.DetectedMediaType == "application/octet-stream"
	default:
		return false
	}
}

func authorizedAssetMatchesGrant(
	asset AuthorizedAsset,
	grant model.BackupAssetDeliveryGrant,
	derived DerivedRepresentation,
	derivedFound bool,
) bool {
	if grant.RecoveryPointID == nil || grant.CatalogGenerationID == nil || grant.EntryID == nil {
		return false
	}
	identityMatches := asset.Ref == (backupasset.AssetRef{RecoveryPointID: *grant.RecoveryPointID, EntryID: *grant.EntryID}) &&
		asset.CatalogGenerationID == *grant.CatalogGenerationID && asset.Provider == backupasset.ProviderKind(grant.ProviderKind) &&
		asset.SourceFingerprint == grant.SourceFingerprint &&
		classificationSourceRevisionForAsset(asset) == grant.ClassificationSourceRevision
	if !identityMatches {
		return false
	}
	if !derivedFound {
		return asset.EntryFingerprint == grant.EntryFingerprint && asset.FingerprintStrength == grant.FingerprintStrength &&
			asset.Size == grant.SourceSize && sameContentTime(asset.ModifiedAt, grant.SourceModifiedAt)
	}
	return validDerivedBinding(derived) && derived.Ref == asset.Ref &&
		derived.CatalogGenerationID == asset.CatalogGenerationID && derived.SourceFingerprint == asset.SourceFingerprint &&
		derived.Provider == asset.Provider && derived.Renderer == Renderer(grant.Renderer) &&
		derived.Profile == RendererProfile(grant.Profile) && derived.EntryFingerprint == grant.EntryFingerprint &&
		grant.FingerprintStrength == "strong" && derived.Size == grant.SourceSize &&
		sameContentTime(derived.ModifiedAt, grant.SourceModifiedAt)
}

func classificationSourceRevisionForAsset(asset AuthorizedAsset) int64 {
	if asset.SearchClassificationRevision > 0 {
		return asset.SearchClassificationRevision
	}
	return 1
}

func cacheEligibleRenderer(renderer Renderer) bool {
	return renderer == RendererSafeRaster || renderer == RendererSameOriginPDF ||
		renderer == RendererNativeAudio || renderer == RendererNativeVideo || renderer == RendererAttachment
}

func cacheObjectForAsset(ownerUserID uint, asset AuthorizedAsset, renderer Renderer, profile RendererProfile) CacheObject {
	return CacheObject{
		OwnerUserID: ownerUserID, Provider: asset.Provider, Ref: asset.Ref,
		CatalogGenerationID: asset.CatalogGenerationID, SourceFingerprint: asset.SourceFingerprint,
		ContentFingerprint: asset.EntryFingerprint, Renderer: renderer, Profile: profile, Size: asset.Size,
	}
}

func cacheObjectForGrant(grant model.BackupAssetDeliveryGrant) CacheObject {
	if grant.RecoveryPointID == nil || grant.CatalogGenerationID == nil || grant.EntryID == nil {
		return CacheObject{}
	}
	return CacheObject{
		OwnerUserID: grant.OwnerUserID, Provider: backupasset.ProviderKind(grant.ProviderKind),
		Ref:                 backupasset.AssetRef{RecoveryPointID: *grant.RecoveryPointID, EntryID: *grant.EntryID},
		CatalogGenerationID: *grant.CatalogGenerationID, SourceFingerprint: grant.SourceFingerprint,
		ContentFingerprint: grant.EntryFingerprint, Renderer: Renderer(grant.Renderer),
		Profile: RendererProfile(grant.Profile), Size: grant.SourceSize,
	}
}

func (broker *Broker) cacheRangeAvailable(object CacheObject) bool {
	cache := broker.currentCache()
	if cache == nil || object.Size <= 0 || !validCacheObject(object) {
		return false
	}
	lease, err := cache.OpenRange(object, 0, 1)
	if err != nil {
		broker.metrics.ObserveCache(cacheMetricOutcome(cache, err))
		return false
	}
	broker.metrics.ObserveCache(MetricCacheHit)
	return lease.Close() == nil
}

func cacheMetricOutcome(cache *AuthenticatedCache, err error) MetricCacheOutcome {
	if errors.Is(err, ErrCacheIntegrity) {
		return MetricCacheTamper
	}
	if errors.Is(err, ErrCacheMiss) || errors.Is(err, ErrCacheBusy) {
		return MetricCacheMiss
	}
	if errors.Is(err, ErrCacheQuota) {
		if cache != nil && cache.Status().Reason == CacheReasonFull {
			return MetricCacheFull
		}
		return MetricCacheDisabled
	}
	if errors.Is(err, ErrCacheClosed) {
		return MetricCacheDisabled
	}
	return MetricCacheFailure
}

func sameContentTime(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.UTC().Equal(right.UTC())
}

func gatewayReservationBytes(grant model.BackupAssetDeliveryGrant, plan RepresentationPlan, method string) (int64, error) {
	if method == http.MethodHead {
		return 0, nil
	}
	providerLimit := plan.ContentLength
	probePossible := plan.ContentLength > 0 && plan.Range.Kind == HTTPRangeFull
	if grant.Renderer == string(RendererEscapedText) || grant.Renderer == string(RendererMetadataHex) {
		providerLimit = grant.RepresentationSourceBytes
		probePossible = providerLimit > 0 && !grant.RepresentationTruncated
	} else if plan.Range.Kind != HTTPRangeFull {
		probePossible = grant.ProviderKind == string(backupasset.ProviderRclone)
	}
	return ComputeReservationBytes(plan.ContentLength, providerLimit, probePossible)
}

func gatewaySourceRequest(grant model.BackupAssetDeliveryGrant, plan RepresentationPlan, method string) (SourceRequest, bool, error) {
	request := SourceRequest{
		Ref:                 backupasset.AssetRef{RecoveryPointID: *grant.RecoveryPointID, EntryID: *grant.EntryID},
		CatalogGenerationID: *grant.CatalogGenerationID,
		ExpectedSource:      grant.SourceFingerprint, ExpectedEntry: grant.EntryFingerprint,
	}
	transformed := grant.Renderer == string(RendererEscapedText) || grant.Renderer == string(RendererMetadataHex)
	switch {
	case method == http.MethodHead || plan.ContentLength == 0:
		request.Mode = SourceModeStat
	case plan.Range.Kind != HTTPRangeFull:
		request.Mode = SourceModeRange
		request.MaxBytes = plan.ContentLength
		request.Range = &ResolvedRange{Offset: plan.Range.Offset, Length: plan.Range.Length}
	case transformed:
		request.Mode = SourceModeSequential
		request.MaxBytes = grant.RepresentationSourceBytes
		if grant.RepresentationTruncated && request.MaxBytes < grant.SourceSize {
			request.MaxBytes++
		}
	default:
		request.Mode = SourceModeSequential
		request.MaxBytes = grant.SourceSize
	}
	if ValidateSourceRequest(request) != nil {
		return SourceRequest{}, false, ErrInvalidSourceRequest
	}
	return request, transformed, nil
}

func gatewayStatSourceRequest(grant model.BackupAssetDeliveryGrant) SourceRequest {
	return SourceRequest{
		Ref:                 backupasset.AssetRef{RecoveryPointID: *grant.RecoveryPointID, EntryID: *grant.EntryID},
		CatalogGenerationID: *grant.CatalogGenerationID,
		ExpectedSource:      grant.SourceFingerprint, ExpectedEntry: grant.EntryFingerprint,
		Mode: SourceModeStat,
	}
}

func gatewaySourceMatchesGrant(session SourceSession, grant model.BackupAssetDeliveryGrant, mode SourceMode) bool {
	if session == nil {
		return false
	}
	stat, capabilities := session.Stat(), session.Capabilities()
	if stat.Size != grant.SourceSize || stat.SourceFingerprint != grant.SourceFingerprint ||
		stat.EntryFingerprint != grant.EntryFingerprint || capabilities.Provider != backupasset.ProviderKind(grant.ProviderKind) {
		return false
	}
	if grant.SourceModifiedAt != nil && (stat.ModifiedAt == nil || !stat.ModifiedAt.Equal(*grant.SourceModifiedAt)) {
		return false
	}
	return mode == SourceModeStat || mode == SourceModeSequential && capabilities.Sequential || mode == SourceModeRange && capabilities.Range
}

func renderPlanMatchesGrant(plan RenderPlan, grant model.BackupAssetDeliveryGrant) bool {
	return plan.MediaType == grant.DetectedMediaType && plan.Range == RangePolicy(grant.RangePolicy) &&
		plan.SourceBytes == grant.RepresentationSourceBytes && plan.Size == grant.RepresentationSize &&
		plan.Truncated == grant.RepresentationTruncated && int64(len(plan.Bytes)) == grant.RepresentationSize
}

func gatewayFilename(grant model.BackupAssetDeliveryGrant) string {
	if grant.Action == string(DeliveryDownload) {
		return "asset.bin"
	}
	return "asset"
}

func writeGatewayHeaders(header http.Header, grant model.BackupAssetDeliveryGrant, plan RepresentationPlan) {
	for _, name := range []string{
		"Access-Control-Allow-Origin", "Access-Control-Allow-Credentials", "Access-Control-Allow-Headers", "Access-Control-Allow-Methods",
	} {
		header.Del(name)
	}
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Cross-Origin-Resource-Policy", "same-origin")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("Content-Security-Policy", "sandbox; default-src 'none'; frame-ancestors 'self'; object-src 'none'")
	header.Set("X-Frame-Options", "SAMEORIGIN")
	header.Set("Cache-Control", "private, no-store")
	header.Set("Content-Encoding", "identity")
	header.Set("ETag", plan.ETag)
	header.Set("Accept-Ranges", plan.AcceptRanges)
	header.Set("Content-Length", strconv.FormatInt(plan.ContentLength, 10))
	if plan.LastModified != "" {
		header.Set("Last-Modified", plan.LastModified)
	}
	if plan.ContentRange != "" {
		header.Set("Content-Range", plan.ContentRange)
	}
	if plan.Status >= 200 && plan.Status < 300 {
		header.Set("Content-Type", grant.DetectedMediaType)
		disposition := "inline"
		if grant.Action == string(DeliveryDownload) {
			disposition = "attachment"
		}
		header.Set("Content-Disposition", safeContentDisposition(disposition, gatewayFilename(grant)))
	}
}

func setGatewayWriteDeadline(writer http.ResponseWriter, now, absolute time.Time, idle time.Duration) error {
	if writer == nil || !absolute.After(now) || idle <= 0 {
		return ErrContentUnavailable
	}
	deadline := now.Add(idle)
	if deadline.After(absolute) {
		deadline = absolute
	}
	if err := http.NewResponseController(writer).SetWriteDeadline(deadline); err != nil {
		return ErrContentUnavailable
	}
	return nil
}

func (broker *Broker) gatewayHeartbeat(
	ctx context.Context,
	grant model.BackupAssetDeliveryGrant,
	actor DeliveryActor,
	session DeliverySession,
	asset AuthorizedAsset,
	lease *ContentLeaseSession,
	interval time.Duration,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if enabled, err := broker.featureEnabled(ctx); err != nil || !enabled {
		if contextErr := ctx.Err(); contextErr != nil {
			return contextErr
		}
		return ErrContentNotFound
	}
	if err := broker.session.Validate(ctx, session); err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return contextErr
		}
		return ErrContentNotFound
	}
	if err := broker.authorize.Reauthorize(ctx, actor, asset, DeliveryAction(grant.Action)); err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return contextErr
		}
		return ErrContentNotFound
	}
	if lease == nil {
		return ErrContentNotFound
	}
	if _, err := lease.Heartbeat(ctx, broker.now().UTC(), interval); err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return contextErr
		}
		return ErrContentNotFound
	}
	var current struct {
		State             string
		IdleExpiresAt     time.Time
		AbsoluteExpiresAt time.Time
		SessionExpiresAt  time.Time
	}
	result := broker.db.WithContext(ctx).Model(&model.BackupAssetDeliveryGrant{}).
		Select("state", "idle_expires_at", "absolute_expires_at", "session_expires_at").
		Where("id = ? AND session_jti = ?", grant.ID, grant.SessionJTI).Limit(1).Scan(&current)
	if result.Error != nil || result.RowsAffected != 1 {
		if contextErr := ctx.Err(); contextErr != nil {
			return contextErr
		}
		return ErrContentNotFound
	}
	now := broker.now().UTC()
	idleExpiresAt := current.IdleExpiresAt.UTC()
	absoluteExpiresAt := current.AbsoluteExpiresAt.UTC()
	sessionExpiresAt := current.SessionExpiresAt.UTC()
	if current.State != string(DeliveryActive) || !now.Before(idleExpiresAt) ||
		!now.Before(absoluteExpiresAt) || !now.Before(sessionExpiresAt) ||
		idleExpiresAt.After(absoluteExpiresAt) || absoluteExpiresAt.After(sessionExpiresAt) ||
		!absoluteExpiresAt.Equal(grant.AbsoluteExpiresAt.UTC()) ||
		!sessionExpiresAt.Equal(grant.SessionExpiresAt.UTC()) {
		return ErrContentNotFound
	}
	return nil
}

func (broker *Broker) monitorGatewayHeartbeat(
	ctx context.Context,
	grant model.BackupAssetDeliveryGrant,
	actor DeliveryActor,
	session DeliverySession,
	asset AuthorizedAsset,
	lease *ContentLeaseSession,
	interval time.Duration,
) (context.Context, func() error) {
	parent := nonNilContext(ctx)
	streamCtx, cancelStream := context.WithCancel(parent)
	heartbeatCtx, cancelHeartbeat := context.WithCancel(parent)
	done := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatCtx.Done():
				done <- heartbeatCtx.Err()
				return
			case <-ticker.C:
				if err := broker.gatewayHeartbeat(heartbeatCtx, grant, actor, session, asset, lease, interval); err != nil {
					done <- err
					cancelStream()
					return
				}
			}
		}
	}()
	var stopOnce sync.Once
	stop := func() error {
		stopOnce.Do(cancelHeartbeat)
		err := <-done
		cancelStream()
		if errors.Is(err, context.Canceled) && parent.Err() == nil {
			return nil
		}
		return err
	}
	return streamCtx, stop
}

func (broker *Broker) registerRead(
	grantID string,
	requestID string,
	sessionJTI string,
	provider backupasset.ProviderKind,
	cancel context.CancelFunc,
) (chan struct{}, bool) {
	done := make(chan struct{})
	broker.mu.Lock()
	if broker.closed || !broker.accepting || broker.leases[grantID] == nil || len(broker.reads[grantID]) >= 64 {
		broker.mu.Unlock()
		return done, false
	}
	if broker.reads[grantID] == nil {
		broker.reads[grantID] = make(map[string]activeContentRead)
	}
	if _, exists := broker.reads[grantID][requestID]; exists {
		broker.mu.Unlock()
		return done, false
	}
	broker.reads[grantID][requestID] = activeContentRead{sessionJTI: sessionJTI, provider: provider, cancel: cancel, done: done}
	broker.inFlight[provider]++
	count := broker.inFlight[provider]
	broker.mu.Unlock()
	broker.metrics.SetInFlight(provider, count)
	return done, true
}

func (broker *Broker) unregisterRead(grantID, requestID string, done chan struct{}) {
	broker.mu.Lock()
	reads := broker.reads[grantID]
	active, exists := reads[requestID]
	if exists && active.done == done {
		delete(reads, requestID)
		if broker.inFlight[active.provider] > 0 {
			broker.inFlight[active.provider]--
		}
		if len(reads) == 0 {
			delete(broker.reads, grantID)
		}
	}
	count := broker.inFlight[active.provider]
	broker.mu.Unlock()
	if exists && active.done == done {
		broker.metrics.SetInFlight(active.provider, count)
		close(done)
	}
}

func minTime(first, second time.Time) time.Time {
	if first.Before(second) {
		return first
	}
	return second
}
