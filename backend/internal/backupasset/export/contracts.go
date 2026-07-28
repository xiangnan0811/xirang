package export

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"xirang/backend/internal/backupasset"

	"gorm.io/gorm"
)

var (
	ErrInvalidSelection         = errors.New("invalid export selection")
	ErrSelectionLimit           = errors.New("export selection limit exceeded")
	ErrInvalidIdempotency       = errors.New("invalid export idempotency")
	ErrConflict                 = errors.New("export conflict")
	ErrNotFound                 = errors.New("export not found")
	ErrUnavailable              = errors.New("export unavailable")
	ErrInvalidTransition        = errors.New("invalid export transition")
	ErrDeadlineUnsafe           = errors.New("export deadline is unsafe")
	ErrArchiveLimit             = errors.New("export archive limit exceeded")
	ErrArchiveSource            = errors.New("export archive source changed")
	ErrCipherTampered           = errors.New("export ciphertext authentication failed")
	ErrInvalidStore             = errors.New("invalid export store")
	ErrQuotaExceeded            = errors.New("export quota exceeded")
	ErrAttemptFenceLost         = errors.New("export attempt fence lost")
	ErrAttemptLeaseExpired      = errors.New("export attempt lease expired")
	ErrAttemptNotClaimable      = errors.New("export attempt is not claimable")
	ErrExecutionDeadlineReached = errors.New("export execution deadline reached")
	ErrReadyExpired             = errors.New("export ready expiry reached")
	ErrSourceDeadlineReached    = errors.New("export source deadline reached")
)

type SelectionKind string

const maxSelectionItemsV1 = 100_000

const (
	SelectionExplicit    SelectionKind = "explicit"
	SelectionSavedSearch SelectionKind = "saved_search"
)

type CreateSelectionV1 struct {
	SchemaVersion      int
	Kind               SelectionKind
	Refs               []backupasset.AssetRef
	SavedSearchID      string
	SavedSearchVersion int
}

type SelectionActor struct {
	UserID uint
	Role   string
}

type SelectionLimits struct {
	MaxItems        int
	MaxSourcePoints int
	MaxLogicalBytes int64
}

type SavedSearchCommitBindingV1 struct {
	SavedSearchID          string
	ExpectedVersion        int
	CanonicalQueryDigest   string
	SearchGenerationDigest string
}

type FrozenItem struct {
	SchemaVersion              int
	Ref                        backupasset.AssetRef
	CatalogGenerationID        string
	SourceFingerprint          string
	EntryFingerprint           string
	FingerprintStrength        string
	ProviderCapabilityRevision int64
	EntryType                  backupasset.CatalogEntryType
	LogicalSize                int64
	MediaType                  string
	RetentionUntil             *time.Time
	SelectionRootOrdinal       int
	ArchiveComponents          []string
}

type FrozenSelection struct {
	Items       []FrozenItem
	SavedSearch *SavedSearchCommitBindingV1
	Digest      string
}

type SelectionResolver interface {
	ResolveExplicit(context.Context, SelectionActor, []backupasset.AssetRef, SelectionLimits) (FrozenSelection, error)
	ResolveSavedSearch(context.Context, SelectionActor, string, int64, SelectionLimits) (FrozenSelection, error)
	RevalidateFrozenTx(context.Context, *gorm.DB, SelectionActor, FrozenSelection) error
	RevalidateMetadataTx(context.Context, *gorm.DB, FrozenItem) error
}

func ValidateCreateSelection(selection CreateSelectionV1) error {
	if selection.SchemaVersion != 1 {
		return ErrInvalidSelection
	}
	switch selection.Kind {
	case SelectionExplicit:
		if len(selection.Refs) == 0 || selection.SavedSearchID != "" || selection.SavedSearchVersion != 0 {
			return ErrInvalidSelection
		}
		for _, ref := range selection.Refs {
			if err := backupasset.ValidateAssetRef(ref); err != nil {
				return ErrInvalidSelection
			}
		}
	case SelectionSavedSearch:
		if len(selection.Refs) != 0 || backupasset.ValidateOpaqueID(selection.SavedSearchID) != nil || selection.SavedSearchVersion <= 0 {
			return ErrInvalidSelection
		}
	default:
		return ErrInvalidSelection
	}
	return nil
}

func ValidateFrozenItem(item FrozenItem) error {
	if item.SchemaVersion != 1 || backupasset.ValidateAssetRef(item.Ref) != nil ||
		backupasset.ValidateOpaqueID(item.CatalogGenerationID) != nil ||
		len(item.SourceFingerprint) == 0 || len(item.SourceFingerprint) > 128 ||
		len(item.EntryFingerprint) == 0 || len(item.EntryFingerprint) > 128 ||
		(item.FingerprintStrength != "strong" && item.FingerprintStrength != "weak" && item.FingerprintStrength != "none") ||
		item.ProviderCapabilityRevision <= 0 || !validEntryType(item.EntryType) || item.LogicalSize < 0 ||
		len(item.MediaType) > 255 || item.SelectionRootOrdinal < 0 || len(item.ArchiveComponents) == 0 {
		return ErrInvalidSelection
	}
	if item.RetentionUntil != nil && (item.RetentionUntil.Location() != time.UTC || item.RetentionUntil.IsZero()) {
		return ErrInvalidSelection
	}
	for _, component := range item.ArchiveComponents {
		if component == "" || component == "." || component == ".." || strings.ContainsAny(component, "/\\\x00") {
			return ErrInvalidSelection
		}
	}
	return nil
}

func validateSavedSearchBinding(binding *SavedSearchCommitBindingV1) error {
	if binding == nil {
		return nil
	}
	if backupasset.ValidateOpaqueID(binding.SavedSearchID) != nil || binding.ExpectedVersion <= 0 ||
		!lowerHex(binding.CanonicalQueryDigest, 64) || !lowerHex(binding.SearchGenerationDigest, 64) {
		return ErrInvalidSelection
	}
	return nil
}

func validSelectionLimits(limits SelectionLimits) bool {
	return limits.MaxItems > 0 && limits.MaxItems <= maxSelectionItemsV1 &&
		limits.MaxSourcePoints > 0 && limits.MaxLogicalBytes > 0
}

func validEntryType(value backupasset.CatalogEntryType) bool {
	switch value {
	case backupasset.CatalogEntryFile, backupasset.CatalogEntryDirectory, backupasset.CatalogEntrySymlink,
		backupasset.CatalogEntryHardlink, backupasset.CatalogEntrySpecial, backupasset.CatalogEntryUnknown:
		return true
	default:
		return false
	}
}

func lowerHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func validateActor(actor SelectionActor) error {
	if actor.UserID == 0 || (actor.Role != "admin" && actor.Role != "operator") {
		return fmt.Errorf("%w: actor", ErrInvalidSelection)
	}
	return nil
}
