package export

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"sort"

	"xirang/backend/internal/backupasset"
)

type IdempotencyDomain string

const (
	IdempotencyDomainExportCreate        IdempotencyDomain = "export_create"
	IdempotencyDomainArchiveMemberCreate IdempotencyDomain = "archive_member_create"
	MinIdempotencyKeyBytes                                 = 16
	MaxIdempotencyKeyBytes                                 = 256
)

type CreateIntentV1 struct {
	SchemaVersion   int    `json:"schema_version"`
	OwnerUserID     uint   `json:"owner_user_id"`
	SelectionDigest string `json:"selection_digest"`
	ArchiveFormat   string `json:"archive_format"`
	ArchiveProfile  string `json:"archive_profile"`
	ChunkBytes      int64  `json:"chunk_bytes"`
}

type createRequestIntentV1 struct {
	SchemaVersion      int                    `json:"schema_version"`
	OwnerUserID        uint                   `json:"owner_user_id"`
	SelectionKind      SelectionKind          `json:"selection_kind"`
	ExplicitRefs       []backupasset.AssetRef `json:"explicit_refs,omitempty"`
	SavedSearchID      string                 `json:"saved_search_id,omitempty"`
	SavedSearchVersion int                    `json:"saved_search_version,omitempty"`
	ArchiveFormat      string                 `json:"archive_format"`
	ArchiveProfile     string                 `json:"archive_profile"`
}

func IdempotencyKeyDigest(domain IdempotencyDomain, ownerUserID uint, key string) (string, error) {
	return IdempotencyKeyDigestWithMaxBytes(domain, ownerUserID, key, MaxIdempotencyKeyBytes)
}

// IdempotencyKeyDigestWithMaxBytes validates an opaque client key against the
// caller's active settings snapshot before deriving its domain-separated digest.
func IdempotencyKeyDigestWithMaxBytes(
	domain IdempotencyDomain,
	ownerUserID uint,
	key string,
	maxBytes int,
) (string, error) {
	if (domain != IdempotencyDomainExportCreate && domain != IdempotencyDomainArchiveMemberCreate) ||
		ownerUserID == 0 || !ValidIdempotencyKeyMaxBytes(maxBytes) ||
		len(key) < MinIdempotencyKeyBytes || len(key) > maxBytes {
		return "", ErrInvalidIdempotency
	}
	hash := sha256.New()
	hash.Write([]byte("xirang.backup_asset.idempotency.v1\x00"))
	hash.Write([]byte(domain))
	hash.Write([]byte{0})
	var owner [8]byte
	binary.BigEndian.PutUint64(owner[:], uint64(ownerUserID))
	hash.Write(owner[:])
	hash.Write([]byte{0})
	hash.Write([]byte(key))
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func ValidIdempotencyKeyMaxBytes(value int) bool {
	return value >= MinIdempotencyKeyBytes && value <= MaxIdempotencyKeyBytes
}

func CreateIntentDigest(intent CreateIntentV1) (string, error) {
	if intent.SchemaVersion != 1 || intent.OwnerUserID == 0 || !lowerHex(intent.SelectionDigest, 64) ||
		!ValidArchiveProfilePair(ArchiveFormat(intent.ArchiveFormat), intent.ArchiveProfile) || intent.ChunkBytes <= 0 {
		return "", ErrInvalidIdempotency
	}
	canonical, err := json.Marshal(intent)
	if err != nil {
		return "", ErrInvalidIdempotency
	}
	hash := sha256.New()
	hash.Write([]byte("xirang.backup_asset.export.create_intent.v1\x00"))
	hash.Write(canonical)
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func createRequestIntentDigest(request CreateRequest) (string, error) {
	if request.Actor.UserID == 0 || ValidateCreateSelection(request.Selection) != nil ||
		!ValidArchiveProfilePair(request.ArchiveFormat, request.ArchiveProfile) {
		return "", ErrInvalidIdempotency
	}
	intent := createRequestIntentV1{
		SchemaVersion:  1,
		OwnerUserID:    request.Actor.UserID,
		SelectionKind:  request.Selection.Kind,
		ArchiveFormat:  string(request.ArchiveFormat),
		ArchiveProfile: request.ArchiveProfile,
	}
	switch request.Selection.Kind {
	case SelectionExplicit:
		intent.ExplicitRefs = canonicalIntentRefs(request.Selection.Refs)
	case SelectionSavedSearch:
		intent.SavedSearchID = request.Selection.SavedSearchID
		intent.SavedSearchVersion = request.Selection.SavedSearchVersion
	default:
		return "", ErrInvalidIdempotency
	}
	canonical, err := json.Marshal(intent)
	if err != nil {
		return "", ErrInvalidIdempotency
	}
	hash := sha256.New()
	hash.Write([]byte("xirang.backup_asset.export.create_request_intent.v1\x00"))
	hash.Write(canonical)
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func canonicalIntentRefs(refs []backupasset.AssetRef) []backupasset.AssetRef {
	result := append([]backupasset.AssetRef(nil), refs...)
	sort.Slice(result, func(left, right int) bool {
		if result[left].RecoveryPointID != result[right].RecoveryPointID {
			return result[left].RecoveryPointID < result[right].RecoveryPointID
		}
		return result[left].EntryID < result[right].EntryID
	})
	unique := result[:0]
	for _, ref := range result {
		if len(unique) == 0 || unique[len(unique)-1] != ref {
			unique = append(unique, ref)
		}
	}
	return unique
}
