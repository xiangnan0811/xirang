package search

import (
	"errors"
	"time"
)

const QuerySchemaVersion = 1

var (
	ErrInvalidQuery         = errors.New("invalid backup asset search query")
	ErrInvalidScope         = errors.New("invalid backup asset search scope")
	ErrScopeStale           = errors.New("backup asset search scope stale")
	ErrResourceLimit        = errors.New("backup asset search resource limit")
	ErrInvalidCursor        = errors.New("invalid backup asset search cursor")
	ErrStaleCursor          = errors.New("backup asset search cursor stale")
	ErrInvalidSecurityState = errors.New("invalid backup asset search security state")
	ErrSearchSourceChanged  = errors.New("backup asset search source changed")
	ErrSearchCatalogChanged = errors.New("backup asset search catalog changed")
	ErrSearchKeyUnavailable = errors.New("backup asset search key unavailable")
)

type SearchField string

const (
	SearchFieldAny          SearchField = "any"
	SearchFieldName         SearchField = "name"
	SearchFieldPath         SearchField = "path"
	SearchFieldExtension    SearchField = "extension"
	SearchFieldType         SearchField = "type"
	SearchFieldModifiedTime SearchField = "modified_time"
	SearchFieldTag          SearchField = "tag"
	SearchFieldContent      SearchField = "content"
	SearchFieldOCR          SearchField = "ocr"
)

type TokenKind string

const (
	TokenKindExact   TokenKind = "exact"
	TokenKindSegment TokenKind = "segment"
	TokenKindBigram  TokenKind = "bigram"
	TokenKindDate    TokenKind = "date"
)

var validPostingFields = map[SearchField]bool{
	SearchFieldName: true, SearchFieldPath: true, SearchFieldExtension: true,
	SearchFieldType: true, SearchFieldModifiedTime: true, SearchFieldTag: true,
	SearchFieldContent: true, SearchFieldOCR: true,
}

var validTokenKinds = map[TokenKind]bool{
	TokenKindExact: true, TokenKindSegment: true, TokenKindBigram: true, TokenKindDate: true,
}

type QueryOp string

const (
	QueryOpAnd          QueryOp = "and"
	QueryOpOr           QueryOp = "or"
	QueryOpNot          QueryOp = "not"
	QueryOpTerm         QueryOp = "term"
	QueryOpType         QueryOp = "type"
	QueryOpModifiedTime QueryOp = "modified_time"
)

type SearchScopeMode string

const (
	SearchScopeCurrent     SearchScopeMode = "current"
	SearchScopeAllRetained SearchScopeMode = "all_retained"
	SearchScopeExactPoints SearchScopeMode = "exact_points"
)

type SearchSort string

const (
	SearchSortRelevance    SearchSort = "relevance"
	SearchSortNameAsc      SearchSort = "name_asc"
	SearchSortModifiedDesc SearchSort = "modified_desc"
)

type QueryNode struct {
	Op       QueryOp     `json:"op"`
	Field    SearchField `json:"field,omitempty"`
	Text     string      `json:"text,omitempty"`
	Values   []string    `json:"values,omitempty"`
	From     *time.Time  `json:"from,omitempty"`
	To       *time.Time  `json:"to,omitempty"`
	Children []QueryNode `json:"children,omitempty"`
}

type SearchScope struct {
	Mode             SearchScopeMode `json:"mode"`
	RepositoryIDs    []string        `json:"repository_ids,omitempty"`
	TaskIDs          []uint          `json:"task_ids,omitempty"`
	RecoveryPointIDs []string        `json:"recovery_point_ids,omitempty"`
}

type SearchRequest struct {
	SchemaVersion int         `json:"schema_version"`
	Root          QueryNode   `json:"root"`
	Scope         SearchScope `json:"scope"`
	Sort          SearchSort  `json:"sort"`
	Limit         int         `json:"limit"`
	Cursor        string      `json:"cursor,omitempty"`
}

type QueryLimits struct {
	MaxBodyBytes     int
	MaxDepth         int
	MaxNodes         int
	MaxValuesPerNode int
	MaxValueBytes    int
	MaxValueRunes    int
	MaxPageSize      int
	MaxCandidates    int
	MaxExecutionTime time.Duration
	MaxSuggestions   int
}

func DefaultQueryLimits() QueryLimits {
	return QueryLimits{
		MaxBodyBytes: 64 * 1024, MaxDepth: 12, MaxNodes: 128, MaxValuesPerNode: 32,
		MaxValueBytes: 4096, MaxValueRunes: 2048, MaxPageSize: 200,
		MaxCandidates: 10000, MaxExecutionTime: 2 * time.Second, MaxSuggestions: 20,
	}
}

type SearchGenerationState string

const (
	SearchGenerationBuilding   SearchGenerationState = "building"
	SearchGenerationComplete   SearchGenerationState = "complete"
	SearchGenerationFailed     SearchGenerationState = "failed"
	SearchGenerationSuperseded SearchGenerationState = "superseded"
)

type SearchGenerationError string

const (
	SearchErrorNone                 SearchGenerationError = ""
	SearchErrorBuildAbandoned       SearchGenerationError = "search_build_abandoned"
	SearchErrorBuildFailed          SearchGenerationError = "search_build_failed"
	SearchErrorBuildLimit           SearchGenerationError = "search_build_limit"
	SearchErrorBuildTimeout         SearchGenerationError = "search_build_timeout"
	SearchErrorCatalogChanged       SearchGenerationError = "search_catalog_changed"
	SearchErrorSourceChanged        SearchGenerationError = "search_source_changed"
	SearchErrorFenceLost            SearchGenerationError = "search_fence_lost"
	SearchErrorKeyUnavailable       SearchGenerationError = "search_key_unavailable"
	SearchErrorInvalidSecurityState SearchGenerationError = "search_invalid_security_state"
	SearchErrorProjectionMismatch   SearchGenerationError = "search_projection_mismatch"
)

type Sensitivity string

const (
	SensitivityNonSecret Sensitivity = "non_secret"
	SensitivitySecret    Sensitivity = "secret"
	SensitivityUnknown   Sensitivity = "unknown"
)

type FieldCoverage string

const (
	FieldCoverageComplete    FieldCoverage = "complete"
	FieldCoveragePartial     FieldCoverage = "partial"
	FieldCoverageBuilding    FieldCoverage = "building"
	FieldCoverageFailed      FieldCoverage = "failed"
	FieldCoverageUnavailable FieldCoverage = "unavailable"
)
