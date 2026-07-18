package overlay

import (
	"errors"
	"fmt"
	"time"

	"xirang/backend/internal/backupasset"
	assetsearch "xirang/backend/internal/backupasset/search"
)

var (
	ErrInvalidOverlay      = errors.New("invalid backup asset overlay")
	ErrQuotaExceeded       = errors.New("backup asset overlay quota exceeded")
	ErrRateLimited         = errors.New("backup asset overlay rate limited")
	ErrIdempotencyConflict = errors.New("backup asset overlay idempotency conflict")
	ErrOverlayUnavailable  = errors.New("backup asset overlay unavailable")
	ErrSavedSearchBroken   = errors.New("backup asset saved search broken")
)

type Actor struct {
	UserID uint
	Role   string
}

type SavedSearchState string

const (
	SavedSearchActive  SavedSearchState = "active"
	SavedSearchBroken  SavedSearchState = "broken"
	SavedSearchBlocked SavedSearchState = "blocked"
)

type SavedSearchReason string

const (
	SavedReasonNone                 SavedSearchReason = ""
	SavedReasonPointRetired         SavedSearchReason = "point_retired"
	SavedReasonPointExpiring        SavedSearchReason = "point_expiring"
	SavedReasonPointExpired         SavedSearchReason = "point_expired"
	SavedReasonPointFailed          SavedSearchReason = "point_failed"
	SavedReasonPointPurgeBlocked    SavedSearchReason = "point_purge_blocked"
	SavedReasonPointMissing         SavedSearchReason = "point_missing"
	SavedReasonScopeUnauthorized    SavedSearchReason = "scope_unauthorized"
	SavedReasonASTSchemaUnsupported SavedSearchReason = "ast_schema_unsupported"
)

type OverlayState string

const (
	OverlayActive    OverlayState = "active"
	OverlayTombstone OverlayState = "tombstone"
)

type TombstoneReason string

const (
	TombstoneSourceRetired      TombstoneReason = "source_retired"
	TombstoneSourceExpiring     TombstoneReason = "source_expiring"
	TombstoneSourceExpired      TombstoneReason = "source_expired"
	TombstoneSourceFailed       TombstoneReason = "source_failed"
	TombstoneSourcePurgeBlocked TombstoneReason = "source_purge_blocked"
	TombstoneSourceMissing      TombstoneReason = "source_missing"
)

type SourceReason string

const (
	SourceRetired      SourceReason = "retired"
	SourceExpiring     SourceReason = "expiring"
	SourceExpired      SourceReason = "expired"
	SourceFailed       SourceReason = "failed"
	SourcePurgeBlocked SourceReason = "purge_blocked"
	SourceMissing      SourceReason = "missing"
)

type SavedSearch struct {
	ID          string                    `json:"id"`
	OwnerUserID uint                      `json:"-"`
	Query       assetsearch.SearchRequest `json:"query"`
	Version     int                       `json:"version"`
	State       SavedSearchState          `json:"state"`
	StateReason SavedSearchReason         `json:"state_reason,omitempty"`
	BrokenAt    *time.Time                `json:"broken_at,omitempty"`
	CreatedAt   time.Time                 `json:"created_at"`
	UpdatedAt   time.Time                 `json:"updated_at"`
}

type Favorite struct {
	ID          string               `json:"id"`
	OwnerUserID uint                 `json:"-"`
	Ref         backupasset.AssetRef `json:"ref"`
	Label       string               `json:"label,omitempty"`
	State       OverlayState         `json:"state"`
	Reason      TombstoneReason      `json:"tombstone_reason,omitempty"`
	Version     int                  `json:"version"`
	CreatedAt   time.Time            `json:"created_at"`
	UpdatedAt   time.Time            `json:"updated_at"`
}

type Tag struct {
	ID          string    `json:"id"`
	OwnerUserID uint      `json:"-"`
	Name        string    `json:"name"`
	Version     int       `json:"version"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type TagAssignment struct {
	ID          string               `json:"id"`
	OwnerUserID uint                 `json:"-"`
	TagID       string               `json:"tag_id"`
	Ref         backupasset.AssetRef `json:"ref"`
	State       OverlayState         `json:"state"`
	Reason      TombstoneReason      `json:"tombstone_reason,omitempty"`
	Version     int                  `json:"version"`
}

type RecentAccess struct {
	ID             string               `json:"id"`
	OwnerUserID    uint                 `json:"-"`
	Ref            backupasset.AssetRef `json:"ref"`
	AccessCount    int64                `json:"access_count"`
	LastAccessedAt time.Time            `json:"last_accessed_at"`
	ExpiresAt      time.Time            `json:"expires_at"`
	Version        int                  `json:"version"`
}

type OverlayListRequest struct {
	Limit  int
	Cursor string
}

type SavedSearchPage struct {
	Items      []SavedSearch `json:"items"`
	NextCursor string        `json:"next_cursor,omitempty"`
}

type FavoritePage struct {
	Items      []Favorite `json:"items"`
	NextCursor string     `json:"next_cursor,omitempty"`
}

type TagPage struct {
	Items      []Tag  `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
}

type RecentAccessPage struct {
	Items      []RecentAccess `json:"items"`
	NextCursor string         `json:"next_cursor,omitempty"`
}

func ParseSavedSearchState(value string) (SavedSearchState, error) {
	state := SavedSearchState(value)
	if state != SavedSearchActive && state != SavedSearchBroken && state != SavedSearchBlocked {
		return "", ErrInvalidOverlay
	}
	return state, nil
}

func ValidateOverlayRef(ref backupasset.AssetRef) error {
	if backupasset.ValidateAssetRef(ref) != nil {
		return fmt.Errorf("%w: AssetRef", ErrInvalidOverlay)
	}
	return nil
}

func validActor(actor Actor) bool {
	return actor.UserID > 0 && (actor.Role == "admin" || actor.Role == "operator")
}
