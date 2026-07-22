package search

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/catalog"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

type TotalRelation string

const (
	TotalRelationExact       TotalRelation = "exact"
	TotalRelationLowerBound  TotalRelation = "lower_bound"
	TotalRelationUnavailable TotalRelation = "unavailable"
)

type CoverageStatus string

const (
	CoverageComplete    CoverageStatus = "complete"
	CoveragePartial     CoverageStatus = "partial"
	CoverageBuilding    CoverageStatus = "building"
	CoverageFailed      CoverageStatus = "failed"
	CoverageUnavailable CoverageStatus = "unavailable"
)

type StalenessStatus string

const (
	StalenessFresh   StalenessStatus = "fresh"
	StalenessStale   StalenessStatus = "stale"
	StalenessUnknown StalenessStatus = "unknown"
)

type SearchIndexStatus struct {
	RecoveryPointID     string          `json:"recovery_point_id"`
	CatalogGenerationID string          `json:"catalog_generation_id,omitempty"`
	SearchGenerationID  string          `json:"search_generation_id,omitempty"`
	ProjectionRevision  int64           `json:"projection_revision"`
	Coverage            CoverageStatus  `json:"coverage"`
	Staleness           StalenessStatus `json:"staleness"`
}

type VerifiedSnippet struct {
	Field SearchField `json:"field"`
	Text  string      `json:"text"`
}

type SearchHit struct {
	Ref       backupasset.AssetRef `json:"ref"`
	Asset     catalog.EntryDTO     `json:"asset"`
	HitFields []SearchField        `json:"hit_fields"`
	Score     int64                `json:"score"`
	Snippet   *VerifiedSnippet     `json:"snippet,omitempty"`
}

type SearchCoverage struct {
	Status CoverageStatus `json:"status"`
}

type SearchCapabilities struct {
	Metadata bool `json:"metadata"`
	Content  bool `json:"content"`
}

type SearchPermissions struct {
	List         bool `json:"list"`
	SecretReveal bool `json:"secret_reveal"`
}

type SearchSuggestion struct {
	Field SearchField `json:"field"`
	Value string      `json:"value"`
}

type SearchResponse struct {
	QueryGeneration    string              `json:"query_generation"`
	Indexes            []SearchIndexStatus `json:"indexes"`
	Items              []SearchHit         `json:"items"`
	NextCursor         string              `json:"next_cursor,omitempty"`
	Total              *int64              `json:"total"`
	TotalRelation      TotalRelation       `json:"total_relation"`
	AuthoritativeEmpty bool                `json:"authoritative_empty"`
	Coverage           SearchCoverage      `json:"coverage"`
	Suggestions        []SearchSuggestion  `json:"suggestions"`
	Capabilities       SearchCapabilities  `json:"capabilities"`
	Permissions        SearchPermissions   `json:"permissions"`
}

type SecretRevealProof struct {
	ID        string
	ExpiresAt time.Time
}

type SearchActor struct {
	Authorization catalog.AuthorizationScope
	SecretProof   *SecretRevealProof
}

type ExcerptVerifyRequest struct {
	Ref        backupasset.AssetRef
	Field      SearchField
	Terms      []string
	ExcerptRef string
}

type ExcerptResolver interface {
	Verify(context.Context, ExcerptVerifyRequest) (VerifiedSnippet, bool, error)
}

type TagResolver interface {
	Matches(context.Context, uint, backupasset.AssetRef, string) (bool, error)
	CandidateRefs(context.Context, uint, string, []string, int) ([]backupasset.AssetRef, error)
	Revision(context.Context, uint) (string, error)
}

type ServiceLimits struct {
	Query            QueryLimits
	MaxCandidates    int
	ExecutionTimeout time.Duration
}

type ContentPipelineRevisions struct {
	Content int64
	OCR     int64
}

type ContentPipelineRevisionSource func(context.Context) (ContentPipelineRevisions, error)

type MalwareSafetyRequest struct {
	Ref                        backupasset.AssetRef
	CatalogGenerationID        string
	SourceFingerprint          string
	EntryFingerprint           string
	FingerprintStrength        string
	ProviderCapabilityRevision int64
	Size                       int64
	MediaType                  string
}

type MalwareSafetySource func(context.Context, MalwareSafetyRequest) (bool, error)

func DefaultServiceLimits() ServiceLimits {
	return ServiceLimits{Query: DefaultQueryLimits(), MaxCandidates: 10000, ExecutionTimeout: 2 * time.Second}
}

type ServiceDependencies struct {
	DB                *gorm.DB
	Scope             *ScopeResolver
	Keys              SearchKeySource
	Cursor            *CursorCodec
	Tags              TagResolver
	Excerpts          ExcerptResolver
	Now               func() time.Time
	Limits            ServiceLimits
	FeatureEnabled    func() (bool, error)
	PipelineRevisions ContentPipelineRevisionSource
	MalwareSafety     MalwareSafetySource
}

type Service struct {
	db                *gorm.DB
	scope             *ScopeResolver
	keys              SearchKeySource
	cursor            *CursorCodec
	tags              TagResolver
	excerpts          ExcerptResolver
	now               func() time.Time
	limits            ServiceLimits
	featureEnabled    func() (bool, error)
	pipelineRevisions ContentPipelineRevisionSource
	malwareSafety     MalwareSafetySource
}

type activeSearchIndex struct {
	point      SelectedPoint
	pointModel model.RecoveryPoint
	catalog    model.CatalogGeneration
	search     model.BackupAssetSearchGeneration
	status     SearchIndexStatus
}

type searchableDocument struct {
	document model.BackupAssetSearchDocument
	entry    model.CatalogEntry
	point    model.RecoveryPoint
	postings []model.BackupAssetSearchPosting
	fields   map[SearchField]model.BackupAssetSearchDocumentField
	current  bool
	pointAt  time.Time
}

type evaluatedHit struct {
	hit            SearchHit
	document       searchableDocument
	anchorID       string
	lineageToken   string
	pathGroupToken string
}

type queryEvaluationState struct {
	excerptAvailable bool
	tagAvailable     bool
	malwareSafety    map[backupasset.AssetRef]bool
}

func NewService(dependencies ServiceDependencies) (*Service, error) {
	if dependencies.DB == nil || dependencies.Scope == nil || dependencies.Keys == nil || dependencies.Cursor == nil ||
		dependencies.FeatureEnabled == nil ||
		(dependencies.PipelineRevisions != nil && dependencies.MalwareSafety == nil) ||
		dependencies.Limits.MaxCandidates <= 0 || dependencies.Limits.ExecutionTimeout <= 0 ||
		!validQueryLimits(dependencies.Limits.Query) {
		return nil, fmt.Errorf("%w: invalid Search service dependencies", backupasset.ErrInvalidState)
	}
	if dependencies.Now == nil {
		dependencies.Now = func() time.Time { return time.Now().UTC() }
	}
	return &Service{
		db: dependencies.DB, scope: dependencies.Scope, keys: dependencies.Keys, cursor: dependencies.Cursor,
		tags: dependencies.Tags, excerpts: dependencies.Excerpts, now: dependencies.Now, limits: dependencies.Limits,
		featureEnabled: dependencies.FeatureEnabled, pipelineRevisions: dependencies.PipelineRevisions,
		malwareSafety: dependencies.MalwareSafety,
	}, nil
}

func (service *Service) Search(ctx context.Context, actor SearchActor, request SearchRequest) (SearchResponse, error) {
	if service == nil || service.db == nil {
		return SearchResponse{}, fmt.Errorf("%w: Search service unavailable", backupasset.ErrInvalidState)
	}
	enabled, err := service.featureEnabled()
	if err != nil {
		return SearchResponse{}, err
	}
	if !enabled {
		return SearchResponse{}, catalog.ErrFeatureDisabled
	}
	if err := catalog.ValidateAuthorizationScope(actor.Authorization); err != nil {
		return SearchResponse{}, err
	}
	canonical, err := ValidateAndCanonicalize(request, service.limits.Query)
	if err != nil {
		return SearchResponse{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	queryCtx, cancel := context.WithTimeout(ctx, service.limits.ExecutionTimeout)
	defer cancel()
	key, err := service.keys.Active(queryCtx, backupasset.KeyDomainSearchToken)
	if err != nil || key.Domain != backupasset.KeyDomainSearchToken || key.State != backupasset.DomainKeyActive || len(key.Key) != 32 {
		return SearchResponse{}, ErrSearchKeyUnavailable
	}
	selection, err := service.scope.Resolve(queryCtx, actor.Authorization, canonical.Request.Scope)
	if err != nil {
		return SearchResponse{}, err
	}
	indexes, statuses, err := service.resolveIndexes(queryCtx, selection, key.Version)
	if err != nil {
		return SearchResponse{}, err
	}
	pipelineRevisions := service.contentPipelineRevisions(queryCtx)
	if err := service.applyPipelineCoverage(
		queryCtx, indexes, statuses, fieldsUsedByQuery(canonical.Request.Root), pipelineRevisions,
	); err != nil {
		return SearchResponse{}, err
	}
	proofValid, proofDigest := service.proofBinding(actor.SecretProof)
	tagDigest, err := service.tagRevision(queryCtx, actor.Authorization.UserID, canonical.Request.Root)
	if err != nil {
		return SearchResponse{}, err
	}
	documents, err := service.loadDocuments(
		queryCtx, indexes, canonical.Request.Root, key, actor.Authorization.UserID, proofValid, pipelineRevisions,
	)
	if err != nil {
		return SearchResponse{}, err
	}
	if len(documents) > service.limits.MaxCandidates {
		return SearchResponse{}, ErrResourceLimit
	}
	evaluation := queryEvaluationState{
		excerptAvailable: service.excerpts != nil && (pipelineRevisions.Content > 0 || pipelineRevisions.OCR > 0),
		tagAvailable:     service.tags != nil,
		malwareSafety:    make(map[backupasset.AssetRef]bool, len(documents)),
	}
	evaluated := make([]evaluatedHit, 0, len(documents))
	for _, document := range documents {
		truth, facts, err := service.evaluateNode(
			queryCtx, canonical.Request.Root, document, key, actor.Authorization.UserID, proofValid, &evaluation, false,
		)
		if err != nil {
			return SearchResponse{}, err
		}
		if truth != truthTrue {
			continue
		}
		hit, err := buildEvaluatedHit(document, facts)
		if err != nil {
			return SearchResponse{}, err
		}
		evaluated = append(evaluated, hit)
	}
	currentTagDigest, err := service.tagRevision(queryCtx, actor.Authorization.UserID, canonical.Request.Root)
	if err != nil {
		return SearchResponse{}, err
	}
	if currentTagDigest != tagDigest {
		return SearchResponse{}, ErrScopeStale
	}
	sortEvaluatedHits(evaluated, canonical.Request.Sort)
	if canonical.Request.Scope.Mode == SearchScopeAllRetained {
		evaluated = groupAllRetainedHits(evaluated)
	}
	suggestions := buildSearchSuggestions(evaluated, service.limits.Query.MaxSuggestions)
	queryHMAC := searchQueryHMAC(key.Key, key.Version, canonical.JSON)
	projectionDigest := searchIndexDigest(statuses)
	classificationDigest := searchClassificationDigest(documents)
	binding := SearchCursorBinding{
		UserID: actor.Authorization.UserID, Role: actor.Authorization.Role, Sort: canonical.Request.Sort,
		QueryHMAC: queryHMAC, ScopeDigest: scopeRequestDigest(canonical.Request.Scope),
		SelectionDigest: selection.RevisionDigest, ProjectionDigest: projectionDigest,
		ClassificationDigest: classificationDigest, TagDigest: tagDigest,
		SearchKeyVersion: key.Version, ProofDigest: proofDigest,
	}
	start := 0
	if canonical.Request.Cursor != "" {
		decoded, err := service.cursor.Decode(queryCtx, canonical.Request.Cursor, binding)
		if err != nil {
			return SearchResponse{}, err
		}
		found := false
		for index := range evaluated {
			if evaluated[index].anchorID == decoded.AnchorID {
				start = index + 1
				found = true
				break
			}
		}
		if !found {
			return SearchResponse{}, ErrStaleCursor
		}
	}
	end := min(start+canonical.Request.Limit, len(evaluated))
	items := make([]SearchHit, 0, end-start)
	for _, value := range evaluated[start:end] {
		items = append(items, value.hit)
	}
	nextCursor := ""
	if end < len(evaluated) && end > 0 {
		binding.AnchorID = evaluated[end-1].anchorID
		nextCursor, err = service.cursor.Encode(queryCtx, binding)
		if err != nil {
			return SearchResponse{}, err
		}
	}
	coverage := aggregateSearchCoverage(statuses, fieldsUsedByQuery(canonical.Request.Root), documents, evaluation)
	response := SearchResponse{
		QueryGeneration: queryHMAC, Indexes: statuses, Items: items, NextCursor: nextCursor,
		Coverage: SearchCoverage{Status: coverage}, Suggestions: suggestions,
		Capabilities: SearchCapabilities{Metadata: true, Content: evaluation.excerptAvailable},
		Permissions:  SearchPermissions{List: true, SecretReveal: proofValid}, TotalRelation: TotalRelationUnavailable,
	}
	if coverage == CoverageComplete {
		total := int64(len(evaluated))
		response.Total = &total
		response.TotalRelation = TotalRelationExact
		response.AuthoritativeEmpty = total == 0
	} else if len(evaluated) > 0 {
		total := int64(len(evaluated))
		response.Total = &total
		response.TotalRelation = TotalRelationLowerBound
	}
	return response, nil
}

func (service *Service) resolveIndexes(
	ctx context.Context,
	selection ScopeSelection,
	searchKeyVersion int,
) ([]activeSearchIndex, []SearchIndexStatus, error) {
	indexes := make([]activeSearchIndex, 0, len(selection.Points))
	statuses := make([]SearchIndexStatus, 0, len(selection.Points))
	for _, point := range selection.Points {
		status := SearchIndexStatus{RecoveryPointID: point.ID, Coverage: CoverageUnavailable, Staleness: StalenessUnknown}
		var pointModel model.RecoveryPoint
		if err := service.db.WithContext(ctx).Where("id = ?", point.ID).Take(&pointModel).Error; err != nil {
			return nil, nil, ErrScopeStale
		}
		var catalogGeneration model.CatalogGeneration
		catalogErr := service.db.WithContext(ctx).
			Where("recovery_point_id = ? AND is_active = ? AND state = ?", point.ID, true, "complete").
			Take(&catalogGeneration).Error
		if errors.Is(catalogErr, gorm.ErrRecordNotFound) {
			statuses = append(statuses, status)
			continue
		}
		if catalogErr != nil {
			return nil, nil, fmt.Errorf("load active Catalog for Search query: %w", catalogErr)
		}
		status.CatalogGenerationID = catalogGeneration.ID
		var generation model.BackupAssetSearchGeneration
		searchErr := service.db.WithContext(ctx).
			Where(`recovery_point_id = ? AND catalog_generation_id = ? AND source_fingerprint = ?
				AND normalizer_version = ? AND search_key_version = ? AND is_active = ? AND state = ?`,
				point.ID, catalogGeneration.ID, pointModel.SourceFingerprint, NormalizerVersion, searchKeyVersion, true, SearchGenerationComplete).
			Take(&generation).Error
		if errors.Is(searchErr, gorm.ErrRecordNotFound) {
			status.Coverage = service.latestSearchCoverage(ctx, point.ID)
			statuses = append(statuses, status)
			continue
		}
		if searchErr != nil {
			return nil, nil, fmt.Errorf("load active Search generation: %w", searchErr)
		}
		status.SearchGenerationID = generation.ID
		status.ProjectionRevision = generation.ProjectionRevision
		status.Coverage = CoverageComplete
		status.Staleness = StalenessFresh
		statuses = append(statuses, status)
		indexes = append(indexes, activeSearchIndex{point: point, pointModel: pointModel, catalog: catalogGeneration, search: generation, status: status})
	}
	return indexes, statuses, nil
}

func (service *Service) latestSearchCoverage(ctx context.Context, pointID string) CoverageStatus {
	var generation model.BackupAssetSearchGeneration
	if err := service.db.WithContext(ctx).Where("recovery_point_id = ?", pointID).
		Order("generation DESC").Take(&generation).Error; err != nil {
		return CoverageUnavailable
	}
	switch SearchGenerationState(generation.State) {
	case SearchGenerationBuilding:
		return CoverageBuilding
	case SearchGenerationFailed:
		return CoverageFailed
	case SearchGenerationComplete:
		return CoverageUnavailable
	default:
		return CoverageUnavailable
	}
}

type candidatePlan struct {
	query     string
	args      []any
	refs      map[backupasset.AssetRef]struct{}
	selective bool
}

func (plan candidatePlan) empty() bool {
	return plan.selective && plan.query == "" && len(plan.refs) == 0
}

func (service *Service) loadDocuments(
	ctx context.Context,
	indexes []activeSearchIndex,
	root QueryNode,
	key backupasset.DomainKeyMaterial,
	userID uint,
	secretProof bool,
	pipelineRevisions ContentPipelineRevisions,
) ([]searchableDocument, error) {
	pointIDs := make([]string, 0, len(indexes))
	for _, index := range indexes {
		pointIDs = append(pointIDs, index.point.ID)
	}
	plan, err := service.buildCandidatePlan(ctx, root, key, userID, pointIDs, secretProof)
	if err != nil {
		return nil, err
	}
	type candidateDocument struct {
		document model.BackupAssetSearchDocument
		index    activeSearchIndex
	}
	candidates := make(map[string]candidateDocument)
	addCandidate := func(index activeSearchIndex, document model.BackupAssetSearchDocument) error {
		candidateKey := document.SearchGenerationID + ":" + document.DocumentID
		if _, exists := candidates[candidateKey]; exists {
			return nil
		}
		candidates[candidateKey] = candidateDocument{document: document, index: index}
		if len(candidates) > service.limits.MaxCandidates {
			return ErrResourceLimit
		}
		return nil
	}
	for _, index := range indexes {
		if !plan.selective || plan.query != "" {
			var documents []model.BackupAssetSearchDocument
			query := service.db.WithContext(ctx).Table("backup_asset_search_documents AS documents").
				Where("documents.search_generation_id = ?", index.search.ID)
			if plan.selective {
				query = query.Where(plan.query, plan.args...)
			}
			if err := query.Order("documents.document_id ASC").Limit(service.limits.MaxCandidates + 1).Find(&documents).Error; err != nil {
				return nil, fmt.Errorf("load bounded Search documents: %w", err)
			}
			for _, document := range documents {
				if err := addCandidate(index, document); err != nil {
					return nil, err
				}
			}
		}
		entryIDs := make([]string, 0)
		for ref := range plan.refs {
			if ref.RecoveryPointID == index.point.ID {
				entryIDs = append(entryIDs, ref.EntryID)
			}
		}
		sort.Strings(entryIDs)
		for start := 0; start < len(entryIDs); start += 400 {
			end := min(start+400, len(entryIDs))
			var documents []model.BackupAssetSearchDocument
			if err := service.db.WithContext(ctx).
				Where("search_generation_id = ? AND recovery_point_id = ? AND entry_id IN ?", index.search.ID, index.point.ID, entryIDs[start:end]).
				Order("document_id ASC").Find(&documents).Error; err != nil {
				return nil, fmt.Errorf("load owner-tag Search candidates: %w", err)
			}
			for _, document := range documents {
				if err := addCandidate(index, document); err != nil {
					return nil, err
				}
			}
		}
	}
	ordered := make([]candidateDocument, 0, len(candidates))
	for _, candidate := range candidates {
		ordered = append(ordered, candidate)
	}
	sort.Slice(ordered, func(left, right int) bool {
		if ordered[left].document.RecoveryPointID != ordered[right].document.RecoveryPointID {
			return ordered[left].document.RecoveryPointID < ordered[right].document.RecoveryPointID
		}
		return ordered[left].document.DocumentID < ordered[right].document.DocumentID
	})
	result := make([]searchableDocument, 0, len(ordered))
	for _, candidate := range ordered {
		document, index := candidate.document, candidate.index
		var entry model.CatalogEntry
		if err := service.db.WithContext(ctx).
			Where("generation_id = ? AND entry_id = ? AND recovery_point_id = ?", index.catalog.ID, document.EntryID, index.point.ID).
			Take(&entry).Error; err != nil {
			return nil, ErrSearchCatalogChanged
		}
		var postings []model.BackupAssetSearchPosting
		if err := service.db.WithContext(ctx).Where("search_generation_id = ? AND document_id = ?", index.search.ID, document.DocumentID).
			Find(&postings).Error; err != nil {
			return nil, fmt.Errorf("load Search postings: %w", err)
		}
		var fieldRows []model.BackupAssetSearchDocumentField
		if err := service.db.WithContext(ctx).Where("search_generation_id = ? AND document_id = ?", index.search.ID, document.DocumentID).
			Find(&fieldRows).Error; err != nil {
			return nil, fmt.Errorf("load Search field coverage: %w", err)
		}
		fields := make(map[SearchField]model.BackupAssetSearchDocumentField, len(fieldRows))
		staleFields := make(map[SearchField]bool, 2)
		for _, field := range fieldRows {
			fieldName := SearchField(field.Field)
			if (fieldName == SearchFieldContent || fieldName == SearchFieldOCR) &&
				!contentPipelineRevisionMatches(fieldName, field.PipelineRevision, pipelineRevisions) {
				field.State = string(FieldCoverageUnavailable)
				field.ExcerptRef = nil
				staleFields[fieldName] = true
			}
			fields[fieldName] = field
		}
		if len(staleFields) > 0 {
			filtered := postings[:0]
			for _, posting := range postings {
				if !staleFields[SearchField(posting.Field)] {
					filtered = append(filtered, posting)
				}
			}
			postings = filtered
		}
		result = append(result, searchableDocument{
			document: document, entry: entry, point: index.pointModel, postings: postings, fields: fields, current: index.point.Current,
			pointAt: selectedPointTime(index.point),
		})
	}
	return result, nil
}

func (service *Service) contentPipelineRevisions(ctx context.Context) ContentPipelineRevisions {
	if service.pipelineRevisions == nil {
		return ContentPipelineRevisions{}
	}
	revisions, err := service.pipelineRevisions(ctx)
	if err != nil || revisions.Content <= 0 || revisions.OCR <= 0 {
		return ContentPipelineRevisions{}
	}
	return revisions
}

func (service *Service) applyPipelineCoverage(
	ctx context.Context,
	indexes []activeSearchIndex,
	statuses []SearchIndexStatus,
	fields map[SearchField]bool,
	revisions ContentPipelineRevisions,
) error {
	required := make(map[SearchField]int64, 2)
	if fields[SearchFieldContent] {
		required[SearchFieldContent] = revisions.Content
	}
	if fields[SearchFieldOCR] {
		required[SearchFieldOCR] = revisions.OCR
	}
	if fields[SearchFieldAny] {
		required[SearchFieldContent] = revisions.Content
		required[SearchFieldOCR] = revisions.OCR
	}
	if len(required) == 0 {
		return nil
	}
	statusByGeneration := make(map[string]int, len(statuses))
	for index := range statuses {
		statusByGeneration[statuses[index].SearchGenerationID] = index
	}
	for _, index := range indexes {
		statusIndex, ok := statusByGeneration[index.search.ID]
		if !ok {
			return fmt.Errorf("%w: Search coverage status mismatch", backupasset.ErrInvalidState)
		}
		for field, revision := range required {
			if revision <= 0 || revision > int64(^uint(0)>>1) {
				statuses[statusIndex].Coverage = CoverageUnavailable
				continue
			}
			var mismatched int64
			if err := service.db.WithContext(ctx).Model(&model.BackupAssetSearchDocumentField{}).
				Where("search_generation_id = ? AND field = ? AND pipeline_revision <> ?", index.search.ID, field, int(revision)).
				Count(&mismatched).Error; err != nil {
				return fmt.Errorf("load Search pipeline coverage: %w", err)
			}
			if mismatched > 0 {
				statuses[statusIndex].Coverage = CoverageUnavailable
			}
		}
	}
	return nil
}

func contentPipelineRevisionMatches(field SearchField, persisted int, active ContentPipelineRevisions) bool {
	if persisted <= 0 {
		return false
	}
	switch field {
	case SearchFieldContent:
		return int64(persisted) == active.Content
	case SearchFieldOCR:
		return int64(persisted) == active.OCR
	default:
		return true
	}
}

func (service *Service) buildCandidatePlan(
	ctx context.Context,
	node QueryNode,
	key backupasset.DomainKeyMaterial,
	userID uint,
	pointIDs []string,
	secretProof bool,
) (candidatePlan, error) {
	switch node.Op {
	case QueryOpAnd:
		parts := make([]candidatePlan, 0, len(node.Children))
		for _, child := range node.Children {
			part, err := service.buildCandidatePlan(ctx, child, key, userID, pointIDs, secretProof)
			if err != nil {
				return candidatePlan{}, err
			}
			if part.empty() {
				return candidatePlan{selective: true}, nil
			}
			if part.selective {
				parts = append(parts, part)
			}
		}
		if len(parts) == 0 {
			return candidatePlan{}, nil
		}
		var refParts []candidatePlan
		var queryParts []candidatePlan
		for _, part := range parts {
			if part.query == "" && len(part.refs) > 0 {
				refParts = append(refParts, part)
			} else if part.query != "" && len(part.refs) == 0 {
				queryParts = append(queryParts, part)
			}
		}
		if len(refParts) > 0 {
			refs := cloneCandidateRefs(refParts[0].refs)
			for _, part := range refParts[1:] {
				for ref := range refs {
					if _, exists := part.refs[ref]; !exists {
						delete(refs, ref)
					}
				}
			}
			return candidatePlan{refs: refs, selective: true}, nil
		}
		if len(queryParts) > 0 {
			return combineCandidateQueries(queryParts, " AND "), nil
		}
		return parts[0], nil
	case QueryOpOr:
		parts := make([]candidatePlan, 0, len(node.Children))
		for _, child := range node.Children {
			part, err := service.buildCandidatePlan(ctx, child, key, userID, pointIDs, secretProof)
			if err != nil {
				return candidatePlan{}, err
			}
			if !part.selective {
				return candidatePlan{}, nil
			}
			parts = append(parts, part)
		}
		return unionCandidatePlans(parts), nil
	case QueryOpNot:
		return candidatePlan{}, nil
	case QueryOpTerm:
		return service.termCandidatePlan(ctx, node, key, userID, pointIDs, secretProof)
	case QueryOpType:
		return candidatePlan{query: "documents.entry_type IN ?", args: []any{node.Values}, selective: true}, nil
	case QueryOpModifiedTime:
		parts := []string{"documents.modified_at IS NOT NULL"}
		args := make([]any, 0, 2)
		if node.From != nil {
			parts = append(parts, "documents.modified_at >= ?")
			args = append(args, node.From.UTC())
		}
		if node.To != nil {
			parts = append(parts, "documents.modified_at <= ?")
			args = append(args, node.To.UTC())
		}
		return candidatePlan{query: strings.Join(parts, " AND "), args: args, selective: true}, nil
	default:
		return candidatePlan{}, ErrInvalidQuery
	}
}

func (service *Service) termCandidatePlan(
	ctx context.Context,
	node QueryNode,
	key backupasset.DomainKeyMaterial,
	userID uint,
	pointIDs []string,
	secretProof bool,
) (candidatePlan, error) {
	if node.Field == SearchFieldAny {
		parts := make([]candidatePlan, 0, 6)
		for _, field := range []SearchField{
			SearchFieldName, SearchFieldPath, SearchFieldExtension, SearchFieldTag, SearchFieldContent, SearchFieldOCR,
		} {
			fieldNode := node
			fieldNode.Field = field
			part, err := service.termCandidatePlan(ctx, fieldNode, key, userID, pointIDs, secretProof)
			if err != nil {
				return candidatePlan{}, err
			}
			parts = append(parts, part)
		}
		return unionCandidatePlans(parts), nil
	}
	if node.Field == SearchFieldTag {
		if service.tags == nil {
			return candidatePlan{selective: true}, nil
		}
		refs, err := service.tags.CandidateRefs(ctx, userID, node.Text, pointIDs, service.limits.MaxCandidates)
		if err != nil {
			return candidatePlan{}, err
		}
		if len(refs) > service.limits.MaxCandidates {
			return candidatePlan{}, ErrResourceLimit
		}
		allowedPoints := make(map[string]struct{}, len(pointIDs))
		for _, pointID := range pointIDs {
			allowedPoints[pointID] = struct{}{}
		}
		result := candidatePlan{refs: make(map[backupasset.AssetRef]struct{}, len(refs)), selective: true}
		for _, ref := range refs {
			if backupasset.ValidateAssetRef(ref) != nil {
				return candidatePlan{}, ErrScopeStale
			}
			if _, exists := allowedPoints[ref.RecoveryPointID]; !exists {
				return candidatePlan{}, ErrScopeStale
			}
			result.refs[ref] = struct{}{}
		}
		return result, nil
	}
	if (node.Field == SearchFieldContent || node.Field == SearchFieldOCR) && service.excerpts == nil {
		return candidatePlan{selective: true}, nil
	}
	normalizerField := node.Field
	if normalizerField == SearchFieldExtension {
		normalizerField = SearchFieldName
	}
	normalized, err := NormalizeFieldV1(normalizerField, node.Text, DefaultNormalizerLimits())
	if err != nil {
		return candidatePlan{}, ErrInvalidQuery
	}
	digests := make([]string, 0, len(normalized.Tokens))
	for _, token := range normalized.Tokens {
		digest, err := TokenHMAC(key.Key, key.Version, NormalizerVersion, node.Field, token.Kind, token.Value)
		if err != nil {
			return candidatePlan{}, err
		}
		digests = append(digests, digest)
	}
	if len(digests) == 0 {
		return candidatePlan{selective: true}, nil
	}
	query := `EXISTS (
		SELECT 1 FROM backup_asset_search_postings AS candidate_postings
		WHERE candidate_postings.search_generation_id = documents.search_generation_id
		  AND candidate_postings.document_id = documents.document_id
		  AND candidate_postings.field = ? AND candidate_postings.key_version = ?
		  AND candidate_postings.token_hmac IN ?
	)`
	args := []any{node.Field, key.Version, digests}
	if (node.Field == SearchFieldContent || node.Field == SearchFieldOCR) && !secretProof {
		query = "documents.sensitivity = ? AND (" + query + ")"
		args = append([]any{SensitivityNonSecret}, args...)
	}
	return candidatePlan{query: query, args: args, selective: true}, nil
}

func unionCandidatePlans(parts []candidatePlan) candidatePlan {
	result := candidatePlan{refs: make(map[backupasset.AssetRef]struct{}), selective: true}
	queries := make([]string, 0, len(parts))
	for _, part := range parts {
		if !part.selective {
			return candidatePlan{}
		}
		if part.query != "" {
			queries = append(queries, "("+part.query+")")
			result.args = append(result.args, part.args...)
		}
		for ref := range part.refs {
			result.refs[ref] = struct{}{}
		}
	}
	result.query = strings.Join(queries, " OR ")
	return result
}

func combineCandidateQueries(parts []candidatePlan, operator string) candidatePlan {
	result := candidatePlan{selective: true}
	queries := make([]string, 0, len(parts))
	for _, part := range parts {
		queries = append(queries, "("+part.query+")")
		result.args = append(result.args, part.args...)
	}
	result.query = strings.Join(queries, operator)
	return result
}

func cloneCandidateRefs(source map[backupasset.AssetRef]struct{}) map[backupasset.AssetRef]struct{} {
	result := make(map[backupasset.AssetRef]struct{}, len(source))
	for ref := range source {
		result[ref] = struct{}{}
	}
	return result
}

type truthValue uint8

const (
	truthFalse truthValue = iota
	truthTrue
	truthUnknown
)

type matchFact struct {
	leafKey   string
	field     SearchField
	kind      TokenKind
	frequency int
	proximity int
	snippet   *VerifiedSnippet
}

func (service *Service) evaluateNode(
	ctx context.Context,
	node QueryNode,
	document searchableDocument,
	key backupasset.DomainKeyMaterial,
	userID uint,
	secretProof bool,
	evaluation *queryEvaluationState,
	negated bool,
) (truthValue, []matchFact, error) {
	switch node.Op {
	case QueryOpAnd:
		truth := truthTrue
		facts := []matchFact{}
		for _, child := range node.Children {
			childTruth, childFacts, err := service.evaluateNode(ctx, child, document, key, userID, secretProof, evaluation, negated)
			if err != nil {
				return truthFalse, nil, err
			}
			truth = andTruth(truth, childTruth)
			if childTruth == truthTrue {
				facts = append(facts, childFacts...)
			}
		}
		if truth != truthTrue {
			facts = nil
		}
		return truth, facts, nil
	case QueryOpOr:
		truth := truthFalse
		facts := []matchFact{}
		for _, child := range node.Children {
			childTruth, childFacts, err := service.evaluateNode(ctx, child, document, key, userID, secretProof, evaluation, negated)
			if err != nil {
				return truthFalse, nil, err
			}
			truth = orTruth(truth, childTruth)
			if childTruth == truthTrue {
				facts = append(facts, childFacts...)
			}
		}
		if truth != truthTrue {
			facts = nil
		}
		return truth, facts, nil
	case QueryOpNot:
		truth, _, err := service.evaluateNode(ctx, node.Children[0], document, key, userID, secretProof, evaluation, !negated)
		if err != nil {
			return truthFalse, nil, err
		}
		switch truth {
		case truthTrue:
			return truthFalse, nil, nil
		case truthFalse:
			return truthTrue, nil, nil
		default:
			return truthUnknown, nil, nil
		}
	case QueryOpTerm:
		return service.evaluateTerm(ctx, node, document, key, userID, secretProof, evaluation, negated)
	case QueryOpType:
		for _, value := range node.Values {
			if document.document.EntryType == value {
				return truthTrue, []matchFact{{leafKey: canonicalLeafKey(node), field: SearchFieldType, kind: TokenKindExact, frequency: 1}}, nil
			}
		}
		return truthFalse, nil, nil
	case QueryOpModifiedTime:
		if document.document.ModifiedAt == nil || (node.From != nil && document.document.ModifiedAt.Before(*node.From)) ||
			(node.To != nil && document.document.ModifiedAt.After(*node.To)) {
			return truthFalse, nil, nil
		}
		return truthTrue, []matchFact{{leafKey: canonicalLeafKey(node), field: SearchFieldModifiedTime, kind: TokenKindDate, frequency: 1}}, nil
	default:
		return truthFalse, nil, ErrInvalidQuery
	}
}

func (service *Service) evaluateTerm(
	ctx context.Context,
	node QueryNode,
	document searchableDocument,
	key backupasset.DomainKeyMaterial,
	userID uint,
	secretProof bool,
	evaluation *queryEvaluationState,
	negated bool,
) (truthValue, []matchFact, error) {
	fields := []SearchField{node.Field}
	if node.Field == SearchFieldAny {
		fields = []SearchField{SearchFieldName, SearchFieldPath, SearchFieldExtension, SearchFieldTag, SearchFieldContent, SearchFieldOCR}
	}
	truth := truthFalse
	facts := []matchFact{}
	for _, field := range fields {
		fieldTruth, fact, err := service.evaluateTermField(ctx, node, field, document, key, userID, secretProof, evaluation, negated)
		if err != nil {
			return truthFalse, nil, err
		}
		truth = orTruth(truth, fieldTruth)
		if fieldTruth == truthTrue && fact != nil {
			facts = append(facts, *fact)
		}
	}
	return truth, facts, nil
}

func (service *Service) evaluateTermField(
	ctx context.Context,
	node QueryNode,
	field SearchField,
	document searchableDocument,
	key backupasset.DomainKeyMaterial,
	userID uint,
	secretProof bool,
	evaluation *queryEvaluationState,
	negated bool,
) (truthValue, *matchFact, error) {
	if field == SearchFieldTag {
		if service.tags == nil {
			return truthUnknown, nil, nil
		}
		matched, err := service.tags.Matches(ctx, userID, backupasset.AssetRef{RecoveryPointID: document.document.RecoveryPointID, EntryID: document.document.EntryID}, node.Text)
		if err != nil {
			return truthUnknown, nil, err
		}
		if !matched {
			return truthFalse, nil, nil
		}
		return truthTrue, &matchFact{leafKey: canonicalLeafKey(node), field: field, kind: TokenKindExact, frequency: 1}, nil
	}
	if field == SearchFieldContent || field == SearchFieldOCR {
		sensitivity := Sensitivity(document.document.Sensitivity)
		if (sensitivity == SensitivitySecret || sensitivity == SensitivityUnknown) && !secretProof {
			return truthUnknown, nil, nil
		}
		if !service.contentMalwareSafe(ctx, document, evaluation) {
			return truthUnknown, nil, nil
		}
	}
	normalizerField := field
	if normalizerField == SearchFieldExtension {
		normalizerField = SearchFieldName
	}
	normalized, err := NormalizeFieldV1(normalizerField, node.Text, DefaultNormalizerLimits())
	if err != nil {
		return truthFalse, nil, ErrInvalidQuery
	}
	matched, kind, frequency, terms := postingMatch(document.postings, key, field, normalized.Tokens)
	if !matched {
		return truthFalse, nil, nil
	}
	fact := &matchFact{leafKey: canonicalLeafKey(node), field: field, kind: kind, frequency: frequency}
	if field == SearchFieldPath {
		fact.proximity = pathLeafProximity(document.entry.NormalizedPath, normalized.Tokens)
	}
	if field != SearchFieldContent && field != SearchFieldOCR {
		return truthTrue, fact, nil
	}
	coverage, exists := document.fields[field]
	if !exists || coverage.State != string(FieldCoverageComplete) || coverage.ExcerptRef == nil || service.excerpts == nil {
		return truthUnknown, nil, nil
	}
	snippet, verified, err := service.excerpts.Verify(ctx, ExcerptVerifyRequest{
		Ref:   backupasset.AssetRef{RecoveryPointID: document.document.RecoveryPointID, EntryID: document.document.EntryID},
		Field: field, Terms: terms, ExcerptRef: *coverage.ExcerptRef,
	})
	if err != nil {
		if evaluation != nil {
			evaluation.excerptAvailable = false
		}
		return truthUnknown, nil, nil
	}
	if !verified {
		return truthFalse, nil, nil
	}
	if negated {
		return truthTrue, fact, nil
	}
	fact.snippet = &snippet
	return truthTrue, fact, nil
}

func (service *Service) contentMalwareSafe(
	ctx context.Context,
	document searchableDocument,
	evaluation *queryEvaluationState,
) bool {
	ref := backupasset.AssetRef{
		RecoveryPointID: document.document.RecoveryPointID,
		EntryID:         document.document.EntryID,
	}
	if evaluation != nil {
		if safe, exists := evaluation.malwareSafety[ref]; exists {
			return safe
		}
	}
	safe := false
	if service.malwareSafety != nil {
		value, err := service.malwareSafety(ctx, MalwareSafetyRequest{
			Ref:                        ref,
			CatalogGenerationID:        document.document.CatalogGenerationID,
			SourceFingerprint:          document.point.SourceFingerprint,
			EntryFingerprint:           document.entry.Fingerprint,
			FingerprintStrength:        document.entry.FingerprintStrength,
			ProviderCapabilityRevision: int64(document.point.CapabilityRevision),
			Size:                       document.entry.Size,
			MediaType:                  document.entry.MimeType,
		})
		safe = err == nil && value
	}
	if evaluation != nil {
		if evaluation.malwareSafety == nil {
			evaluation.malwareSafety = make(map[backupasset.AssetRef]bool)
		}
		evaluation.malwareSafety[ref] = safe
		if !safe {
			evaluation.excerptAvailable = false
		}
	}
	return safe
}

func postingMatch(
	postings []model.BackupAssetSearchPosting,
	key backupasset.DomainKeyMaterial,
	field SearchField,
	tokens []NormalizedToken,
) (bool, TokenKind, int, []string) {
	terms := make([]string, 0, len(tokens))
	for _, token := range tokens {
		digest, err := TokenHMAC(key.Key, key.Version, NormalizerVersion, field, token.Kind, token.Value)
		if err != nil {
			continue
		}
		terms = append(terms, token.Value)
		for _, posting := range postings {
			if posting.Field == string(field) && posting.TokenKind == string(token.Kind) && posting.TokenHMAC == digest && posting.KeyVersion == key.Version {
				return true, token.Kind, posting.TermFrequency, terms
			}
		}
	}
	return false, "", 0, terms
}

func buildEvaluatedHit(document searchableDocument, facts []matchFact) (evaluatedHit, error) {
	uniqueLeaves := make(map[string]bool)
	fields := make(map[SearchField]bool)
	var score int64
	var snippet *VerifiedSnippet
	for _, fact := range facts {
		if !uniqueLeaves[fact.leafKey] {
			uniqueLeaves[fact.leafKey] = true
			score += 1_000_000
		}
		fields[fact.field] = true
		score += int64(fieldWeight(fact.field)*1000 + tokenKindWeight(fact.kind)*100 + min(fact.frequency, 99) + fact.proximity)
		if snippet == nil && fact.snippet != nil {
			copy := *fact.snippet
			snippet = &copy
		}
	}
	hitFields := make([]SearchField, 0, len(fields))
	for _, field := range []SearchField{SearchFieldName, SearchFieldPath, SearchFieldExtension, SearchFieldTag, SearchFieldContent, SearchFieldOCR, SearchFieldType, SearchFieldModifiedTime} {
		if fields[field] {
			hitFields = append(hitFields, field)
		}
	}
	entryType := backupasset.CatalogEntryType(document.entry.EntryType)
	fingerprintStrength := catalog.FingerprintNone
	if document.entry.FingerprintStrength == string(catalog.FingerprintStrong) {
		fingerprintStrength = catalog.FingerprintStrong
	} else if document.entry.FingerprintStrength == string(catalog.FingerprintWeak) {
		fingerprintStrength = catalog.FingerprintWeak
	}
	asset := catalog.EntryDTO{
		RecoveryPointID: document.entry.RecoveryPointID, EntryID: document.entry.EntryID,
		ParentEntryID: document.entry.ParentEntryID, Name: document.entry.Name, EntryType: entryType,
		Size: document.entry.Size, ModifiedAt: utcPointer(document.entry.ModifiedAt), Mode: document.entry.Mode,
		Owner: document.entry.Owner, MIMEType: document.entry.MimeType, FingerprintStrength: fingerprintStrength,
	}
	if err := asset.Validate(); err != nil {
		return evaluatedHit{}, err
	}
	ref := backupasset.AssetRef{RecoveryPointID: document.entry.RecoveryPointID, EntryID: document.entry.EntryID}
	return evaluatedHit{
		hit:      SearchHit{Ref: ref, Asset: asset, HitFields: hitFields, Score: score, Snippet: snippet},
		document: document, anchorID: hitAnchor(ref), lineageToken: document.document.LineageToken,
		pathGroupToken: document.document.PathGroupToken,
	}, nil
}

func pathLeafProximity(path string, queryTokens []NormalizedToken) int {
	normalizedPath, err := NormalizeFieldV1(SearchFieldPath, path, DefaultNormalizerLimits())
	if err != nil {
		return 0
	}
	type tokenKey struct {
		value string
		kind  TokenKind
	}
	query := make(map[tokenKey]struct{}, len(queryTokens))
	for _, token := range queryTokens {
		query[tokenKey{value: token.Value, kind: token.Kind}] = struct{}{}
	}
	segments := strings.Split(normalizedPath.Canonical, "/")
	for index := len(segments) - 1; index >= 0; index-- {
		segment, err := NormalizeFieldV1(SearchFieldName, segments[index], DefaultNormalizerLimits())
		if err != nil {
			continue
		}
		for _, token := range segment.Tokens {
			if _, exists := query[tokenKey{value: token.Value, kind: token.Kind}]; exists {
				return max(0, 64-(len(segments)-1-index))
			}
		}
	}
	return 0
}

func buildSearchSuggestions(values []evaluatedHit, limit int) []SearchSuggestion {
	result := make([]SearchSuggestion, 0, min(len(values), limit))
	if limit <= 0 {
		return result
	}
	seen := make(map[string]struct{})
	appendSuggestion := func(field SearchField, value string) bool {
		value = strings.TrimSpace(value)
		if value == "" {
			return false
		}
		key := string(field) + "\x00" + value
		if _, exists := seen[key]; exists {
			return false
		}
		seen[key] = struct{}{}
		result = append(result, SearchSuggestion{Field: field, Value: value})
		return len(result) >= limit
	}
	for _, evaluated := range values {
		for _, field := range evaluated.hit.HitFields {
			var value string
			switch field {
			case SearchFieldName:
				value = evaluated.document.entry.Name
			case SearchFieldPath:
				value = evaluated.document.entry.NormalizedPath
			case SearchFieldExtension:
				normalized, err := NormalizeFieldV1(SearchFieldName, evaluated.document.entry.Name, DefaultNormalizerLimits())
				if err == nil {
					value = normalized.Extension
				}
			case SearchFieldType:
				value = evaluated.document.entry.EntryType
			case SearchFieldModifiedTime:
				if evaluated.document.entry.ModifiedAt != nil {
					value = evaluated.document.entry.ModifiedAt.UTC().Format("2006-01-02")
				}
			default:
				continue
			}
			if appendSuggestion(field, value) {
				return result
			}
		}
	}
	return result
}

func sortEvaluatedHits(values []evaluatedHit, requested SearchSort) {
	sort.SliceStable(values, func(leftIndex, rightIndex int) bool {
		left, right := values[leftIndex], values[rightIndex]
		switch requested {
		case SearchSortNameAsc:
			if left.document.document.NameSortKey != right.document.document.NameSortKey {
				return left.document.document.NameSortKey < right.document.document.NameSortKey
			}
		case SearchSortModifiedDesc:
			leftTime, rightTime := left.document.document.ModifiedAt, right.document.document.ModifiedAt
			if leftTime != nil || rightTime != nil {
				if leftTime == nil {
					return false
				}
				if rightTime == nil {
					return true
				}
				if !leftTime.Equal(*rightTime) {
					return leftTime.After(*rightTime)
				}
			}
		default:
			if left.hit.Score != right.hit.Score {
				return left.hit.Score > right.hit.Score
			}
		}
		if left.document.current != right.document.current {
			return left.document.current
		}
		if !left.document.pointAt.Equal(right.document.pointAt) {
			return left.document.pointAt.After(right.document.pointAt)
		}
		if left.document.document.PathSortKey != right.document.document.PathSortKey {
			return left.document.document.PathSortKey < right.document.document.PathSortKey
		}
		if left.document.document.NameSortKey != right.document.document.NameSortKey {
			return left.document.document.NameSortKey < right.document.document.NameSortKey
		}
		if left.hit.Ref.RecoveryPointID != right.hit.Ref.RecoveryPointID {
			return left.hit.Ref.RecoveryPointID < right.hit.Ref.RecoveryPointID
		}
		return left.hit.Ref.EntryID < right.hit.Ref.EntryID
	})
}

func groupAllRetainedHits(values []evaluatedHit) []evaluatedHit {
	seen := make(map[string]bool, len(values))
	result := make([]evaluatedHit, 0, len(values))
	for _, value := range values {
		key := value.lineageToken + ":" + value.pathGroupToken
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, value)
	}
	return result
}

func aggregateSearchCoverage(
	statuses []SearchIndexStatus,
	fields map[SearchField]bool,
	documents []searchableDocument,
	evaluation queryEvaluationState,
) CoverageStatus {
	if len(statuses) == 0 {
		return CoverageComplete
	}
	result := CoverageComplete
	for _, status := range statuses {
		if status.Coverage != CoverageComplete {
			if status.Coverage == CoverageBuilding {
				return CoverageBuilding
			}
			if status.Coverage == CoverageFailed {
				return CoverageFailed
			}
			result = CoverageUnavailable
		}
	}
	for field := range fields {
		if field == SearchFieldAny {
			return CoveragePartial
		}
		if field == SearchFieldTag {
			if !evaluation.tagAvailable {
				return CoverageUnavailable
			}
			continue
		}
		if field == SearchFieldContent || field == SearchFieldOCR {
			if !evaluation.excerptAvailable {
				return CoverageUnavailable
			}
			for _, document := range documents {
				coverage, exists := document.fields[field]
				if !exists || coverage.State != string(FieldCoverageComplete) {
					return CoveragePartial
				}
			}
		}
	}
	return result
}

func fieldsUsedByQuery(node QueryNode) map[SearchField]bool {
	fields := make(map[SearchField]bool)
	var visit func(QueryNode)
	visit = func(current QueryNode) {
		switch current.Op {
		case QueryOpTerm:
			fields[current.Field] = true
		case QueryOpType:
			fields[SearchFieldType] = true
		case QueryOpModifiedTime:
			fields[SearchFieldModifiedTime] = true
		default:
			for _, child := range current.Children {
				visit(child)
			}
		}
	}
	visit(node)
	return fields
}

func (service *Service) tagRevision(ctx context.Context, ownerID uint, node QueryNode) (string, error) {
	fields := fieldsUsedByQuery(node)
	if (!fields[SearchFieldTag] && !fields[SearchFieldAny]) || service.tags == nil {
		return "", nil
	}
	revision, err := service.tags.Revision(ctx, ownerID)
	if err != nil {
		return "", err
	}
	if !lowerHex(revision, 64) {
		return "", ErrScopeStale
	}
	return revision, nil
}

func (service *Service) proofBinding(proof *SecretRevealProof) (bool, string) {
	if proof == nil || strings.TrimSpace(proof.ID) == "" || proof.ID != strings.TrimSpace(proof.ID) ||
		len(proof.ID) > 64 || strings.ContainsAny(proof.ID, "\r\n\x00") || !service.utcNow().Before(proof.ExpiresAt.UTC()) ||
		proof.ExpiresAt.After(service.utcNow().Add(5*time.Minute)) {
		return false, ""
	}
	digest := sha256.Sum256([]byte("xirang/search/proof/v1\x00" + proof.ID + "\x00" + proof.ExpiresAt.UTC().Format(time.RFC3339Nano)))
	return true, hex.EncodeToString(digest[:])
}

func searchQueryHMAC(key []byte, keyVersion int, canonical []byte) string {
	mac := hmac.New(sha256.New, key)
	_, _ = fmt.Fprintf(mac, "xirang/search/query/v1\x00%d\x00", keyVersion)
	_, _ = mac.Write(canonical)
	return hex.EncodeToString(mac.Sum(nil))
}

func scopeRequestDigest(scope SearchScope) string {
	encoded, _ := json.Marshal(scope)
	digest := sha256.Sum256(append([]byte("xirang/search/scope-request/v1\x00"), encoded...))
	return hex.EncodeToString(digest[:])
}

func searchIndexDigest(statuses []SearchIndexStatus) string {
	encoded, _ := json.Marshal(statuses)
	digest := sha256.Sum256(append([]byte("xirang/search/indexes/v1\x00"), encoded...))
	return hex.EncodeToString(digest[:])
}

func searchClassificationDigest(documents []searchableDocument) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte("xirang/search/classification/v1"))
	for _, document := range documents {
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(document.document.RecoveryPointID))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(document.document.EntryID))
		_, _ = hash.Write([]byte{0})
		_, _ = fmt.Fprintf(hash, "%d", document.document.ClassificationRevision)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func hitAnchor(ref backupasset.AssetRef) string {
	digest := sha256.Sum256([]byte("xirang/search/anchor/v1\x00" + ref.RecoveryPointID + "\x00" + ref.EntryID))
	return hex.EncodeToString(digest[:])
}

func canonicalLeafKey(node QueryNode) string {
	encoded, _ := json.Marshal(node)
	return string(encoded)
}

func fieldWeight(field SearchField) int {
	return map[SearchField]int{
		SearchFieldName: 500, SearchFieldPath: 350, SearchFieldExtension: 300, SearchFieldTag: 250,
		SearchFieldContent: 150, SearchFieldOCR: 120, SearchFieldType: 100, SearchFieldModifiedTime: 80,
	}[field]
}

func tokenKindWeight(kind TokenKind) int {
	return map[TokenKind]int{TokenKindExact: 4, TokenKindSegment: 3, TokenKindDate: 2, TokenKindBigram: 1}[kind]
}

func andTruth(left, right truthValue) truthValue {
	if left == truthFalse || right == truthFalse {
		return truthFalse
	}
	if left == truthUnknown || right == truthUnknown {
		return truthUnknown
	}
	return truthTrue
}

func orTruth(left, right truthValue) truthValue {
	if left == truthTrue || right == truthTrue {
		return truthTrue
	}
	if left == truthUnknown || right == truthUnknown {
		return truthUnknown
	}
	return truthFalse
}

func selectedPointTime(point SelectedPoint) time.Time {
	for _, value := range []*time.Time{point.CommittedAt, point.CapturedAt, point.ObservedAt} {
		if value != nil {
			return value.UTC()
		}
	}
	return point.CreatedAt.UTC()
}

func (service *Service) utcNow() time.Time { return service.now().UTC() }
