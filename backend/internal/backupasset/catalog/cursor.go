package catalog

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"xirang/backend/internal/backupasset"
)

const (
	cursorFormatVersion = 1
	maxCursorTokenBytes = 8192
	CursorForward       = "forward"
	CursorBackward      = "backward"
)

type CursorEndpoint string

const (
	CursorEndpointRepositories       CursorEndpoint = "repositories"
	CursorEndpointRecoveryPoints     CursorEndpoint = "recovery_points"
	CursorEndpointEntries            CursorEndpoint = "entries"
	CursorEndpointDiff               CursorEndpoint = "diff"
	CursorEndpointFileSourceNodes    CursorEndpoint = "file_source_nodes"
	CursorEndpointFileSourceSets     CursorEndpoint = "file_source_sets"
	CursorEndpointFileSourceVersions CursorEndpoint = "file_source_versions"
)

type CursorAnchor struct {
	RepositoryID    string         `json:"repository_id,omitempty"`
	RecoveryPointID string         `json:"recovery_point_id,omitempty"`
	EntryID         string         `json:"entry_id,omitempty"`
	BaseEntryID     string         `json:"base_entry_id,omitempty"`
	CompareEntryID  string         `json:"compare_entry_id,omitempty"`
	ChangeKind      DiffChangeKind `json:"change_kind,omitempty"`
	NodeID          uint           `json:"node_id,omitempty"`
	BackupSetID     string         `json:"backup_set_id,omitempty"`
}

type CursorScope struct {
	Endpoint               CursorEndpoint `json:"endpoint"`
	Direction              string         `json:"direction"`
	UserID                 uint           `json:"user_id"`
	Role                   string         `json:"role"`
	Sort                   string         `json:"-"`
	NodeID                 uint           `json:"node_id,omitempty"`
	BackupSetID            string         `json:"backup_set_id,omitempty"`
	ProjectionDigest       string         `json:"projection_digest,omitempty"`
	RepositoryID           string         `json:"repository_id,omitempty"`
	RecoveryPointID        string         `json:"recovery_point_id,omitempty"`
	GenerationID           string         `json:"generation_id,omitempty"`
	ParentEntryID          string         `json:"parent_entry_id,omitempty"`
	BaseRecoveryPointID    string         `json:"base_recovery_point_id,omitempty"`
	CompareRecoveryPointID string         `json:"compare_recovery_point_id,omitempty"`
	BaseGenerationID       string         `json:"base_generation_id,omitempty"`
	CompareGenerationID    string         `json:"compare_generation_id,omitempty"`
	BaseParentEntryID      string         `json:"base_parent_entry_id,omitempty"`
	CompareParentEntryID   string         `json:"compare_parent_entry_id,omitempty"`
	Anchor                 CursorAnchor   `json:"anchor"`
}

// cursorClaims is the serializable closed form. CursorScope keeps Sort typed
// for callers while this form stores only the validated public enum string.
type cursorClaims struct {
	Endpoint               CursorEndpoint `json:"endpoint"`
	Direction              string         `json:"direction"`
	UserID                 uint           `json:"user_id"`
	Role                   string         `json:"role"`
	Sort                   string         `json:"sort"`
	NodeID                 uint           `json:"node_id,omitempty"`
	BackupSetID            string         `json:"backup_set_id,omitempty"`
	ProjectionDigest       string         `json:"projection_digest,omitempty"`
	RepositoryID           string         `json:"repository_id,omitempty"`
	RecoveryPointID        string         `json:"recovery_point_id,omitempty"`
	GenerationID           string         `json:"generation_id,omitempty"`
	ParentEntryID          string         `json:"parent_entry_id,omitempty"`
	BaseRecoveryPointID    string         `json:"base_recovery_point_id,omitempty"`
	CompareRecoveryPointID string         `json:"compare_recovery_point_id,omitempty"`
	BaseGenerationID       string         `json:"base_generation_id,omitempty"`
	CompareGenerationID    string         `json:"compare_generation_id,omitempty"`
	BaseParentEntryID      string         `json:"base_parent_entry_id,omitempty"`
	CompareParentEntryID   string         `json:"compare_parent_entry_id,omitempty"`
	Anchor                 CursorAnchor   `json:"anchor"`
}

type CursorKeySource interface {
	Active(context.Context, backupasset.KeyDomain) (backupasset.DomainKeyMaterial, error)
	ByVersion(context.Context, backupasset.KeyDomain, int) (backupasset.DomainKeyMaterial, error)
}

type CursorCodec struct {
	keys CursorKeySource
	now  func() time.Time
	ttl  time.Duration
}

type cursorEnvelope struct {
	FormatVersion int          `json:"format_version"`
	KeyVersion    int          `json:"key_version"`
	IssuedAt      int64        `json:"issued_at"`
	ExpiresAt     int64        `json:"expires_at"`
	Scope         cursorClaims `json:"scope"`
}

func NewCursorCodec(keys CursorKeySource, now func() time.Time, ttl time.Duration) *CursorCodec {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &CursorCodec{keys: keys, now: now, ttl: ttl}
}

func (codec *CursorCodec) Encode(ctx context.Context, scope CursorScope) (string, error) {
	claims, err := cursorClaimsFromScope(scope, true)
	if err != nil {
		return "", err
	}
	if codec == nil || codec.keys == nil || codec.ttl <= 0 || codec.ttl > 15*time.Minute {
		return "", fmt.Errorf("%w: cursor codec unavailable", ErrInvalidCursor)
	}
	material, err := codec.keys.Active(ctx, backupasset.KeyDomainCursorSigning)
	if err != nil || material.Version <= 0 || material.State != backupasset.DomainKeyActive || len(material.Key) < 32 {
		return "", fmt.Errorf("%w: cursor signing key unavailable", ErrInvalidCursor)
	}
	now := codec.now().UTC()
	envelope := cursorEnvelope{
		FormatVersion: cursorFormatVersion, KeyVersion: material.Version,
		IssuedAt: now.Unix(), ExpiresAt: now.Add(codec.ttl).Unix(), Scope: claims,
	}
	payload, err := json.Marshal(envelope)
	if err != nil || len(payload) > maxCursorTokenBytes {
		return "", fmt.Errorf("%w: cursor payload", ErrInvalidCursor)
	}
	signature := signCursor(material.Key, payload)
	token := base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(signature)
	if len(token) > maxCursorTokenBytes {
		return "", fmt.Errorf("%w: cursor token exceeds limit", ErrInvalidCursor)
	}
	return token, nil
}

func (codec *CursorCodec) Decode(ctx context.Context, token string, expected CursorScope) (CursorScope, error) {
	expectedClaims, err := cursorClaimsFromScope(expected, false)
	if err != nil {
		return CursorScope{}, err
	}
	actualScope, err := codec.decodeAuthenticated(ctx, token)
	if err != nil {
		return CursorScope{}, err
	}
	if !cursorClaimsMatch(cursorClaims(actualScope), expectedClaims) {
		return CursorScope{}, fmt.Errorf("%w: cursor scope changed", ErrStaleCursor)
	}
	return actualScope, nil
}

func (codec *CursorCodec) decodeAuthenticated(ctx context.Context, token string) (CursorScope, error) {
	if codec == nil || codec.keys == nil || token == "" || len(token) > maxCursorTokenBytes {
		return CursorScope{}, fmt.Errorf("%w: cursor token unavailable", ErrInvalidCursor)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return CursorScope{}, fmt.Errorf("%w: cursor token format", ErrInvalidCursor)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || len(payload) == 0 || len(payload) > maxCursorTokenBytes {
		return CursorScope{}, fmt.Errorf("%w: cursor payload encoding", ErrInvalidCursor)
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(signature) != sha256.Size {
		return CursorScope{}, fmt.Errorf("%w: cursor signature encoding", ErrInvalidCursor)
	}
	var envelope cursorEnvelope
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return CursorScope{}, fmt.Errorf("%w: cursor payload schema", ErrInvalidCursor)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return CursorScope{}, fmt.Errorf("%w: cursor payload schema", ErrInvalidCursor)
	}
	if envelope.FormatVersion != cursorFormatVersion || envelope.KeyVersion <= 0 {
		return CursorScope{}, fmt.Errorf("%w: cursor version", ErrInvalidCursor)
	}
	actualScope, err := envelope.Scope.toScope()
	if err != nil {
		return CursorScope{}, err
	}
	now := codec.now().UTC()
	material, err := codec.keys.ByVersion(ctx, backupasset.KeyDomainCursorSigning, envelope.KeyVersion)
	if err != nil || len(material.Key) < 32 || !cursorKeyMayVerify(material, now) ||
		!hmac.Equal(signature, signCursor(material.Key, payload)) {
		return CursorScope{}, fmt.Errorf("%w: cursor authentication", ErrInvalidCursor)
	}
	if envelope.IssuedAt <= 0 || envelope.ExpiresAt <= envelope.IssuedAt || !now.Before(time.Unix(envelope.ExpiresAt, 0).UTC()) ||
		envelope.IssuedAt > now.Add(time.Minute).Unix() {
		return CursorScope{}, fmt.Errorf("%w: cursor expired", ErrStaleCursor)
	}
	return actualScope, nil
}

func cursorClaimsFromScope(scope CursorScope, requireAnchor bool) (cursorClaims, error) {
	claims := cursorClaims(scope)
	if err := claims.validate(requireAnchor); err != nil {
		return cursorClaims{}, err
	}
	return claims, nil
}

func (claims cursorClaims) toScope() (CursorScope, error) {
	if err := claims.validate(true); err != nil {
		return CursorScope{}, err
	}
	scope := CursorScope{
		Endpoint: claims.Endpoint, Direction: claims.Direction, UserID: claims.UserID, Role: claims.Role,
		NodeID: claims.NodeID, BackupSetID: claims.BackupSetID, ProjectionDigest: claims.ProjectionDigest, RepositoryID: claims.RepositoryID,
		RecoveryPointID: claims.RecoveryPointID, GenerationID: claims.GenerationID,
		ParentEntryID: claims.ParentEntryID, BaseRecoveryPointID: claims.BaseRecoveryPointID,
		CompareRecoveryPointID: claims.CompareRecoveryPointID, BaseGenerationID: claims.BaseGenerationID,
		CompareGenerationID: claims.CompareGenerationID, BaseParentEntryID: claims.BaseParentEntryID,
		CompareParentEntryID: claims.CompareParentEntryID, Anchor: claims.Anchor,
	}
	scope.Sort = claims.Sort
	return scope, nil
}

func (claims cursorClaims) validate(requireAnchor bool) error {
	if claims.UserID == 0 || (claims.Role != "admin" && claims.Role != "operator") ||
		(claims.Direction != CursorForward && claims.Direction != CursorBackward) {
		return fmt.Errorf("%w: cursor authorization scope", ErrInvalidCursor)
	}
	entryID := func(value string) bool { return value == "" || lowerHexLength(value, 64) }
	opaqueID := func(value string) bool { return value == "" || backupasset.ValidateOpaqueID(value) == nil }
	if !opaqueID(claims.BackupSetID) || (claims.ProjectionDigest != "" && !lowerHexLength(claims.ProjectionDigest, 64)) ||
		!opaqueID(claims.RepositoryID) || !opaqueID(claims.RecoveryPointID) || !opaqueID(claims.GenerationID) ||
		!opaqueID(claims.BaseRecoveryPointID) || !opaqueID(claims.CompareRecoveryPointID) ||
		!opaqueID(claims.BaseGenerationID) || !opaqueID(claims.CompareGenerationID) ||
		!entryID(claims.ParentEntryID) || !entryID(claims.BaseParentEntryID) || !entryID(claims.CompareParentEntryID) ||
		!opaqueID(claims.Anchor.RepositoryID) || !opaqueID(claims.Anchor.RecoveryPointID) ||
		!entryID(claims.Anchor.EntryID) || !entryID(claims.Anchor.BaseEntryID) || !entryID(claims.Anchor.CompareEntryID) ||
		!opaqueID(claims.Anchor.BackupSetID) {
		return fmt.Errorf("%w: cursor resource scope", ErrInvalidCursor)
	}
	switch claims.Endpoint {
	case CursorEndpointRepositories:
		if claims.Sort != RepositorySortCreatedDesc || (requireAnchor && claims.Anchor.RepositoryID == "") || claims.hasPointScope() || claims.hasFileSourceScope() {
			return fmt.Errorf("%w: repository cursor scope", ErrInvalidCursor)
		}
	case CursorEndpointRecoveryPoints:
		if claims.RepositoryID == "" || (requireAnchor && claims.Anchor.RecoveryPointID == "") ||
			!validRecoveryPointSort(RecoveryPointSort(claims.Sort)) || claims.hasEntryOrDiffScope() || claims.hasFileSourceScope() {
			return fmt.Errorf("%w: recovery point cursor scope", ErrInvalidCursor)
		}
	case CursorEndpointEntries:
		if claims.RepositoryID == "" || claims.RecoveryPointID == "" || claims.GenerationID == "" || !lowerHexLength(claims.ProjectionDigest, 64) ||
			(requireAnchor && claims.Anchor.EntryID == "") || !validEntrySort(EntrySort(claims.Sort)) || claims.hasDiffScope() || claims.hasFileSourceIdentityScope() {
			return fmt.Errorf("%w: entry cursor scope", ErrInvalidCursor)
		}
	case CursorEndpointDiff:
		if claims.RepositoryID == "" || claims.BaseRecoveryPointID == "" || claims.CompareRecoveryPointID == "" ||
			claims.BaseRecoveryPointID == claims.CompareRecoveryPointID || claims.BaseGenerationID == "" || claims.CompareGenerationID == "" ||
			claims.Sort != DiffSortPathAsc || (requireAnchor && claims.Anchor.BaseEntryID == "" && claims.Anchor.CompareEntryID == "") ||
			((claims.Anchor.BaseEntryID != "" || claims.Anchor.CompareEntryID != "") && !validDiffChangeKinds[claims.Anchor.ChangeKind]) ||
			claims.RecoveryPointID != "" || claims.GenerationID != "" || claims.ParentEntryID != "" || claims.hasFileSourceScope() {
			return fmt.Errorf("%w: diff cursor scope", ErrInvalidCursor)
		}
	case CursorEndpointFileSourceSets:
		if claims.NodeID == 0 || claims.BackupSetID != "" || !lowerHexLength(claims.ProjectionDigest, 64) || claims.Sort != fileSourceBackupSetSort ||
			(requireAnchor && claims.Anchor.BackupSetID == "") || claims.Anchor.NodeID != 0 || claims.hasLegacyCatalogScope() {
			return fmt.Errorf("%w: file-source set cursor scope", ErrInvalidCursor)
		}
	case CursorEndpointFileSourceNodes:
		if claims.NodeID != 0 || claims.BackupSetID != "" || !lowerHexLength(claims.ProjectionDigest, 64) || claims.Sort != fileSourceNodeSort ||
			(requireAnchor && claims.Anchor.NodeID == 0) || claims.Anchor.BackupSetID != "" || claims.hasLegacyCatalogScope() {
			return fmt.Errorf("%w: file-source node cursor scope", ErrInvalidCursor)
		}
	case CursorEndpointFileSourceVersions:
		if claims.NodeID != 0 || claims.BackupSetID == "" || !lowerHexLength(claims.ProjectionDigest, 64) || claims.Sort != fileSourceVersionSort ||
			(requireAnchor && claims.Anchor.RecoveryPointID == "") || claims.Anchor.NodeID != 0 ||
			claims.Anchor.BackupSetID != "" || claims.hasLegacyCatalogScopeExceptRecoveryPointAnchor() {
			return fmt.Errorf("%w: file-source version cursor scope", ErrInvalidCursor)
		}
	default:
		return fmt.Errorf("%w: cursor endpoint", ErrInvalidCursor)
	}
	return nil
}

func (claims cursorClaims) hasPointScope() bool {
	return claims.RepositoryID != "" || claims.RecoveryPointID != "" || claims.GenerationID != "" || claims.ParentEntryID != "" || claims.hasDiffScope() ||
		claims.Anchor.RecoveryPointID != "" || claims.Anchor.EntryID != ""
}

func (claims cursorClaims) hasEntryOrDiffScope() bool {
	return claims.RecoveryPointID != "" || claims.GenerationID != "" || claims.ParentEntryID != "" || claims.hasDiffScope() || claims.Anchor.EntryID != ""
}

func (claims cursorClaims) hasDiffScope() bool {
	return claims.BaseRecoveryPointID != "" || claims.CompareRecoveryPointID != "" || claims.BaseGenerationID != "" ||
		claims.CompareGenerationID != "" || claims.BaseParentEntryID != "" || claims.CompareParentEntryID != "" ||
		claims.Anchor.BaseEntryID != "" || claims.Anchor.CompareEntryID != "" || claims.Anchor.ChangeKind != ""
}

func (claims cursorClaims) hasFileSourceScope() bool {
	return claims.NodeID != 0 || claims.BackupSetID != "" || claims.ProjectionDigest != "" ||
		claims.Anchor.NodeID != 0 || claims.Anchor.BackupSetID != ""
}

func (claims cursorClaims) hasFileSourceIdentityScope() bool {
	return claims.NodeID != 0 || claims.BackupSetID != "" || claims.Anchor.NodeID != 0 || claims.Anchor.BackupSetID != ""
}

func (claims cursorClaims) hasLegacyCatalogScope() bool {
	return claims.RepositoryID != "" || claims.RecoveryPointID != "" || claims.GenerationID != "" || claims.ParentEntryID != "" ||
		claims.BaseRecoveryPointID != "" || claims.CompareRecoveryPointID != "" || claims.BaseGenerationID != "" ||
		claims.CompareGenerationID != "" || claims.BaseParentEntryID != "" || claims.CompareParentEntryID != "" ||
		claims.Anchor.RepositoryID != "" || claims.Anchor.RecoveryPointID != "" || claims.Anchor.EntryID != "" ||
		claims.Anchor.BaseEntryID != "" || claims.Anchor.CompareEntryID != "" || claims.Anchor.ChangeKind != ""
}

func (claims cursorClaims) hasLegacyCatalogScopeExceptRecoveryPointAnchor() bool {
	return claims.RepositoryID != "" || claims.RecoveryPointID != "" || claims.GenerationID != "" || claims.ParentEntryID != "" ||
		claims.BaseRecoveryPointID != "" || claims.CompareRecoveryPointID != "" || claims.BaseGenerationID != "" ||
		claims.CompareGenerationID != "" || claims.BaseParentEntryID != "" || claims.CompareParentEntryID != "" ||
		claims.Anchor.RepositoryID != "" || claims.Anchor.EntryID != "" || claims.Anchor.BaseEntryID != "" ||
		claims.Anchor.CompareEntryID != "" || claims.Anchor.ChangeKind != ""
}

func validRecoveryPointSort(value RecoveryPointSort) bool {
	return value == RecoveryPointSortCapturedDesc || value == RecoveryPointSortCapturedAsc || value == RecoveryPointSortCreatedDesc
}

func validEntrySort(value EntrySort) bool {
	return value == EntrySortNameAsc || value == EntrySortNameDesc || value == EntrySortSizeDesc || value == EntrySortModifiedDesc
}

func cursorClaimsMatch(actual, expected cursorClaims) bool {
	if actual.Endpoint != expected.Endpoint || actual.Direction != expected.Direction || actual.UserID != expected.UserID ||
		actual.Role != expected.Role || actual.Sort != expected.Sort || actual.NodeID != expected.NodeID ||
		actual.BackupSetID != expected.BackupSetID || actual.ProjectionDigest != expected.ProjectionDigest || actual.RepositoryID != expected.RepositoryID ||
		actual.RecoveryPointID != expected.RecoveryPointID || actual.GenerationID != expected.GenerationID ||
		actual.ParentEntryID != expected.ParentEntryID || actual.BaseRecoveryPointID != expected.BaseRecoveryPointID ||
		actual.CompareRecoveryPointID != expected.CompareRecoveryPointID || actual.BaseGenerationID != expected.BaseGenerationID ||
		actual.CompareGenerationID != expected.CompareGenerationID || actual.BaseParentEntryID != expected.BaseParentEntryID ||
		actual.CompareParentEntryID != expected.CompareParentEntryID {
		return false
	}
	if expected.Anchor != (CursorAnchor{}) && actual.Anchor != expected.Anchor {
		return false
	}
	return true
}

func cursorKeyMayVerify(material backupasset.DomainKeyMaterial, now time.Time) bool {
	switch material.State {
	case backupasset.DomainKeyActive:
		return true
	case backupasset.DomainKeyVerifyOnly:
		return material.VerifyUntil != nil && now.Before(material.VerifyUntil.UTC())
	default:
		return false
	}
}

func signCursor(key, payload []byte) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("xirang.catalog.cursor.v1"))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write(payload)
	return mac.Sum(nil)
}
