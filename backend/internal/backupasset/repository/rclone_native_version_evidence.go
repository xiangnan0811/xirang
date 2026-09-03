package repository

import (
	"context"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	managedRcloneNativeVersionIdentityDomain   = "xirang/rclone/native-version-identity.v1"
	managedRcloneNativeVersionAggregateDomain  = "xirang/rclone/native-version-aggregate.v1"
	managedRcloneNativeMaximumPhysicalKeyBytes = 1024
	managedRcloneNativeMaximumVersionIDBytes   = 4096
)

type managedRcloneNativeVersionEvidenceRows struct {
	owned      []model.RecoveryPointRcloneNativeVersion
	references []model.RecoveryPointRcloneNativeVersion
}

// rejectManagedRcloneNativeDeletionReservationTx enforces the repository-wide
// native deletion reservation while the caller holds the repository row lock.
// An in-flight provider delete or a blocked, unproven provider effect on any
// point in this repository reserves the native namespace until deletion settles.
func rejectManagedRcloneNativeDeletionReservationTx(ctx context.Context, tx *gorm.DB, repositoryID string) error {
	if tx == nil || backupasset.ValidateOpaqueID(repositoryID) != nil {
		return fmt.Errorf("%w: managed Rclone native deletion reservation is unavailable", backupasset.ErrInvalidState)
	}
	var reservations int64
	unsafeBlockedReasons := []string{
		string(backupasset.LifecycleBlockedActiveHold),
		string(backupasset.LifecycleBlockedProviderWORM),
		string(backupasset.LifecycleBlockedProviderUnavailable),
		string(backupasset.LifecycleBlockedProviderIdentityConflict),
		string(backupasset.LifecycleBlockedProviderDeleteUnproven),
		string(backupasset.LifecycleBlockedDeletionUnavailable),
		string(backupasset.LifecycleBlockedFenceLost),
	}
	// provider_native_version_referenced is intentionally absent: it is a
	// dependency wait with no provider effect to reserve.
	if err := tx.WithContext(ctx).Model(&model.RecoveryPointLifecycleAttempt{}).
		Joins("JOIN recovery_points ON recovery_points.id = recovery_point_lifecycle_attempts.recovery_point_id").
		Where(
			`recovery_points.repository_id = ? AND (
				recovery_point_lifecycle_attempts.phase = ? OR
				(recovery_point_lifecycle_attempts.phase = ? AND recovery_point_lifecycle_attempts.blocked_reason IN ?)
			)`,
			repositoryID,
			backupasset.LifecyclePhaseProviderDelete,
			backupasset.LifecyclePhaseBlocked,
			unsafeBlockedReasons,
		).
		Count(&reservations).Error; err != nil {
		return fmt.Errorf("check managed Rclone native deletion reservation: %w", err)
	}
	if reservations != 0 {
		return fmt.Errorf("%w: managed Rclone native provider commit is blocked by provider deletion", backupasset.ErrConflict)
	}
	return nil
}

func managedRcloneNativeVersionIdentityDigest(markerKey []byte, physicalKey, versionID string) string {
	if len(markerKey) < 32 || strings.TrimSpace(physicalKey) == "" || strings.TrimSpace(versionID) == "" {
		return ""
	}
	return hex.EncodeToString(rcloneOwnershipDigest(markerKey, managedRcloneNativeVersionIdentityDomain, physicalKey, versionID))
}

func managedRcloneNativeVersionAggregateDigest(markerKey []byte, role string, identities []string) string {
	if len(markerKey) < 32 || (role != model.RecoveryPointRcloneNativeEvidenceRoleOwned && role != model.RecoveryPointRcloneNativeEvidenceRoleReference) {
		return ""
	}
	values := make([]string, 0, len(identities)*2+2)
	values = append(values, role, fmt.Sprintf("%d", len(identities)))
	for ordinal, identity := range identities {
		values = append(values, fmt.Sprintf("%d", ordinal), identity)
	}
	return hex.EncodeToString(rcloneOwnershipDigest(markerKey, managedRcloneNativeVersionAggregateDomain, values...))
}

func validManagedRcloneNativeVersionIdentity(physicalKey, versionID string) bool {
	return physicalKey != "" && versionID != "" &&
		strings.TrimSpace(physicalKey) == physicalKey && strings.TrimSpace(versionID) == versionID &&
		len(physicalKey) <= managedRcloneNativeMaximumPhysicalKeyBytes &&
		len(versionID) <= managedRcloneNativeMaximumVersionIDBytes &&
		!strings.ContainsRune(physicalKey, '\x00') && !strings.ContainsRune(versionID, '\x00')
}

// managedRcloneLegacyNativeVersions reconstructs the single, conflated
// version set carried by a version-1 locator. Older locators may have only
// the control commit pair, or may carry the full frozen set introduced before
// the durable evidence table. Both representations are treated identically
// by current readers: every entry is an owned entry and a reference entry.
func managedRcloneLegacyNativeVersions(locator managedRclonePointLocatorV1) ([]provider.RcloneNativeExactVersion, error) {
	if locator.Version != managedRclonePointLocatorLegacyVersion ||
		!validManagedRcloneNativeVersionIdentity(locator.NativeCommitKey, locator.NativeCommitVersionID) {
		return nil, fmt.Errorf("%w: invalid managed Rclone legacy native version identity", backupasset.ErrInvalidState)
	}
	if len(locator.FrozenNativeVersions) == 0 {
		return []provider.RcloneNativeExactVersion{{
			PhysicalKey: locator.NativeCommitKey, VersionID: locator.NativeCommitVersionID,
		}}, nil
	}
	versions := make([]provider.RcloneNativeExactVersion, len(locator.FrozenNativeVersions))
	seen := make(map[string]struct{}, len(versions))
	foundCommit := false
	for ordinal, value := range locator.FrozenNativeVersions {
		if !validManagedRcloneNativeVersionIdentity(value.PhysicalKey, value.VersionID) {
			return nil, fmt.Errorf("%w: invalid managed Rclone legacy native version identity at ordinal %d", backupasset.ErrInvalidState, ordinal)
		}
		identity := value.PhysicalKey + "\x00" + value.VersionID
		if _, exists := seen[identity]; exists {
			return nil, fmt.Errorf("%w: duplicate managed Rclone legacy native version identity", backupasset.ErrInvalidState)
		}
		seen[identity] = struct{}{}
		versions[ordinal] = provider.RcloneNativeExactVersion{
			PhysicalKey: value.PhysicalKey, VersionID: value.VersionID,
		}
		if value.PhysicalKey == locator.NativeCommitKey && value.VersionID == locator.NativeCommitVersionID {
			foundCommit = true
		}
	}
	if !foundCommit {
		return nil, fmt.Errorf("%w: managed Rclone legacy native version set omits the commit", backupasset.ErrInvalidState)
	}
	return versions, nil
}

func orderedManagedRcloneNativeVersions(values []provider.RcloneNativeExactVersion, role string) ([]provider.RcloneNativeExactVersion, error) {
	if role != model.RecoveryPointRcloneNativeEvidenceRoleOwned && role != model.RecoveryPointRcloneNativeEvidenceRoleReference {
		return nil, fmt.Errorf("%w: invalid managed Rclone native evidence role", backupasset.ErrInvalidState)
	}
	if role == model.RecoveryPointRcloneNativeEvidenceRoleOwned && len(values) == 0 {
		return nil, fmt.Errorf("%w: managed Rclone native owned evidence is empty", backupasset.ErrInvalidState)
	}
	ordered := append([]provider.RcloneNativeExactVersion(nil), values...)
	seen := make(map[string]struct{}, len(ordered))
	for _, value := range ordered {
		if !validManagedRcloneNativeVersionIdentity(value.PhysicalKey, value.VersionID) {
			return nil, fmt.Errorf("%w: invalid managed Rclone native version identity", backupasset.ErrInvalidState)
		}
		identity := value.PhysicalKey + "\x00" + value.VersionID
		if _, exists := seen[identity]; exists {
			return nil, fmt.Errorf("%w: duplicate managed Rclone native version identity", backupasset.ErrInvalidState)
		}
		seen[identity] = struct{}{}
	}
	sort.Slice(ordered, func(left, right int) bool {
		if ordered[left].PhysicalKey != ordered[right].PhysicalKey {
			return ordered[left].PhysicalKey < ordered[right].PhysicalKey
		}
		return ordered[left].VersionID < ordered[right].VersionID
	})
	return ordered, nil
}

func managedRcloneNativeVersionIdentityDigests(markerKey []byte, values []provider.RcloneNativeExactVersion) ([]string, error) {
	identities := make([]string, len(values))
	for ordinal, value := range values {
		identity := managedRcloneNativeVersionIdentityDigest(markerKey, value.PhysicalKey, value.VersionID)
		if !isLowerHex64(identity) {
			return nil, fmt.Errorf("%w: managed Rclone native version identity HMAC unavailable", backupasset.ErrInvalidState)
		}
		identities[ordinal] = identity
	}
	return identities, nil
}

func managedRcloneNativeVersionRowsForSet(
	repositoryID, pointID, role string,
	markerKey []byte,
	values []provider.RcloneNativeExactVersion,
	now time.Time,
) ([]model.RecoveryPointRcloneNativeVersion, string, error) {
	ordered, err := orderedManagedRcloneNativeVersions(values, role)
	if err != nil {
		return nil, "", err
	}
	identities, err := managedRcloneNativeVersionIdentityDigests(markerKey, ordered)
	if err != nil {
		return nil, "", err
	}
	rows := make([]model.RecoveryPointRcloneNativeVersion, len(ordered))
	for ordinal, value := range ordered {
		rows[ordinal] = model.RecoveryPointRcloneNativeVersion{
			RecoveryPointID: pointID, EvidenceRole: role, Ordinal: int64(ordinal), RepositoryID: repositoryID,
			IdentityDigest: identities[ordinal], EncryptedPhysicalKey: value.PhysicalKey, EncryptedVersionID: value.VersionID,
			CreatedAt: now.UTC(), UpdatedAt: now.UTC(),
		}
	}
	return rows, managedRcloneNativeVersionAggregateDigest(markerKey, role, identities), nil
}

func buildManagedRcloneNativeVersionEvidenceRows(
	repositoryID, pointID string,
	markerKey []byte,
	native *provider.RcloneNativeCommitV1,
	now time.Time,
) (managedRcloneNativeVersionEvidenceRows, string, string, error) {
	if native == nil || !managedRcloneNativeCommitIdentityPresent(native) ||
		backupasset.ValidateOpaqueID(repositoryID) != nil || backupasset.ValidateOpaqueID(pointID) != nil || len(markerKey) < 32 {
		return managedRcloneNativeVersionEvidenceRows{}, "", "", fmt.Errorf("%w: managed Rclone native version evidence is unavailable", backupasset.ErrInvalidState)
	}
	owned, ownedDigest, err := managedRcloneNativeVersionRowsForSet(
		repositoryID, pointID, model.RecoveryPointRcloneNativeEvidenceRoleOwned, markerKey, native.FrozenNativeVersions, now,
	)
	if err != nil {
		return managedRcloneNativeVersionEvidenceRows{}, "", "", err
	}
	references, referenceDigest, err := managedRcloneNativeVersionRowsForSet(
		repositoryID, pointID, model.RecoveryPointRcloneNativeEvidenceRoleReference, markerKey, native.FrozenNativeReferences, now,
	)
	if err != nil {
		return managedRcloneNativeVersionEvidenceRows{}, "", "", err
	}
	return managedRcloneNativeVersionEvidenceRows{owned: owned, references: references}, ownedDigest, referenceDigest, nil
}

func managedRcloneNativeCommitIdentityPresent(native *provider.RcloneNativeCommitV1) bool {
	if native == nil || !validManagedRcloneNativeVersionIdentity(native.CommitKey, native.CommitVersionID) {
		return false
	}
	for _, version := range native.FrozenNativeVersions {
		if version.PhysicalKey == native.CommitKey && version.VersionID == native.CommitVersionID {
			return true
		}
	}
	return false
}
func validateManagedRcloneNativeControlIdentity(
	attempt provider.RcloneAttemptV1,
	binding managedRcloneBindingDocumentV3,
	native *provider.RcloneNativeCommitV1,
) error {
	if attempt.PublicationMode != backupasset.PublicationNativeObjectVersions {
		return nil
	}
	if native == nil || len(native.FrozenNativeVersions) == 0 {
		return fmt.Errorf("%w: managed Rclone native control commit evidence is missing", backupasset.ErrInvalidState)
	}
	expectedCommitKey := managedRcloneNativeControlCommitKey(binding, attempt)
	if !validManagedRcloneNativeVersionIdentity(expectedCommitKey, "placeholder") ||
		native.CommitKey != expectedCommitKey || !validManagedRcloneNativeVersionIdentity(native.CommitKey, native.CommitVersionID) {
		return fmt.Errorf("%w: managed Rclone native control commit identity mismatch", backupasset.ErrConflict)
	}
	commitVersions := 0
	for _, version := range native.FrozenNativeVersions {
		if version.PhysicalKey != expectedCommitKey {
			continue
		}
		commitVersions++
		if version.VersionID != native.CommitVersionID {
			return fmt.Errorf("%w: managed Rclone native control commit version mismatch", backupasset.ErrConflict)
		}
	}
	if commitVersions != 1 {
		return fmt.Errorf("%w: managed Rclone native control commit version is not unique", backupasset.ErrConflict)
	}
	return nil
}

func managedRcloneNativeControlCommitKey(binding managedRcloneBindingDocumentV3, attempt provider.RcloneAttemptV1) string {
	if binding.Native == nil || backupasset.ValidateOpaqueID(attempt.RecoveryPointID) != nil || backupasset.ValidateOpaqueID(attempt.AttemptID) != nil {
		return ""
	}
	return strings.TrimSuffix(binding.Native.ManagedPrefix, "/") + "/control/points/" +
		attempt.RecoveryPointID + "/attempts/" + attempt.AttemptID + "/commit.json"
}

func managedRcloneNativeCommitVersion(
	owned []provider.RcloneNativeExactVersion,
	commitKey string,
) (string, error) {
	if !validManagedRcloneNativeVersionIdentity(commitKey, "placeholder") {
		return "", lifecycleDeleteIdentityConflict("managed Rclone native control commit key is unavailable")
	}
	for _, version := range owned {
		if version.PhysicalKey == commitKey && validManagedRcloneNativeVersionIdentity(version.PhysicalKey, version.VersionID) {
			return version.VersionID, nil
		}
	}
	return "", lifecycleDeleteIdentityConflict("managed Rclone native control commit version is missing")
}

func managedRcloneNativePointIdentityDigest(
	markerKey []byte,
	repositoryID, commitContentDigest string,
	ownedCount uint64, ownedDigest string,
	referenceCount uint64, referenceDigest string,
) string {
	if len(markerKey) < 32 || !isLowerHex64(commitContentDigest) || !isLowerHex64(ownedDigest) || !isLowerHex64(referenceDigest) {
		return ""
	}
	return hex.EncodeToString(rcloneOwnershipDigest(markerKey,
		"xirang.rclone.native-point-identity.v2", repositoryID, commitContentDigest,
		fmt.Sprintf("%d", ownedCount), ownedDigest, fmt.Sprintf("%d", referenceCount), referenceDigest,
	))
}

func persistManagedRcloneNativeVersionEvidenceTx(
	ctx context.Context,
	tx *gorm.DB,
	repositoryID, pointID string,
	markerKey []byte,
	native *provider.RcloneNativeCommitV1,
	now time.Time,
) (string, string, error) {
	if tx == nil {
		return "", "", fmt.Errorf("%w: managed Rclone native evidence transaction is unavailable", backupasset.ErrInvalidState)
	}
	rows, ownedDigest, referenceDigest, err := buildManagedRcloneNativeVersionEvidenceRows(repositoryID, pointID, markerKey, native, now)
	if err != nil {
		return "", "", err
	}
	all := make([]model.RecoveryPointRcloneNativeVersion, 0, len(rows.owned)+len(rows.references))
	all = append(all, rows.owned...)
	all = append(all, rows.references...)
	if len(all) == 0 {
		return "", "", fmt.Errorf("%w: managed Rclone native evidence rows are empty", backupasset.ErrInvalidState)
	}
	if err := tx.WithContext(ctx).CreateInBatches(&all, 200).Error; err != nil {
		return "", "", fmt.Errorf("persist managed Rclone native version evidence: %w", err)
	}
	return ownedDigest, referenceDigest, nil
}

func loadManagedRcloneNativeVersionEvidenceTx(
	ctx context.Context,
	tx *gorm.DB,
	repositoryID, pointID string,
	markerKey []byte,
	locator managedRclonePointLocatorV1,
	expectedCommitKey string,
) ([]provider.RcloneNativeExactVersion, []provider.RcloneNativeExactVersion, error) {
	if tx == nil || len(markerKey) < 32 || backupasset.ValidateOpaqueID(repositoryID) != nil ||
		backupasset.ValidateOpaqueID(pointID) != nil || locator.RepositoryID != repositoryID ||
		locator.RecoveryPointID != pointID {
		return nil, nil, lifecycleDeleteIdentityConflict("managed Rclone native evidence authority is unavailable")
	}
	if locator.Version == managedRclonePointLocatorLegacyVersion {
		versions, err := managedRcloneLegacyNativeVersions(locator)
		if err != nil {
			return nil, nil, lifecycleDeleteIdentityConflict("managed Rclone legacy native evidence is unavailable")
		}
		if !validManagedRcloneNativeVersionIdentity(expectedCommitKey, "placeholder") ||
			locator.NativeCommitKey != expectedCommitKey {
			return nil, nil, lifecycleDeleteIdentityConflict("managed Rclone legacy native control commit key changed")
		}
		expectedSource := hex.EncodeToString(rcloneOwnershipDigest(
			markerKey, "xirang.rclone.native-point-identity.v1", repositoryID,
			locator.NativeCommitKey, locator.NativeCommitVersionID, locator.CommitPayloadDigest,
		))
		if locator.PhysicalIdentityDigest != expectedSource {
			return nil, nil, lifecycleDeleteIdentityConflict("managed Rclone legacy native point identity changed")
		}
		// Version 1 did not distinguish ownership from point-view references.
		// Return independent slices so callers cannot accidentally mutate one
		// authority view through the other.
		owned := append([]provider.RcloneNativeExactVersion(nil), versions...)
		references := append([]provider.RcloneNativeExactVersion(nil), versions...)
		return owned, references, nil
	}
	if locator.Version != managedRclonePointLocatorVersion {
		return nil, nil, lifecycleDeleteIdentityConflict("managed Rclone native locator version is unsupported")
	}
	ownedRows, err := loadManagedRcloneNativeVersionRoleTx(ctx, tx, repositoryID, pointID, model.RecoveryPointRcloneNativeEvidenceRoleOwned)
	if err != nil {
		return nil, nil, err
	}
	referenceRows, err := loadManagedRcloneNativeVersionRoleTx(ctx, tx, repositoryID, pointID, model.RecoveryPointRcloneNativeEvidenceRoleReference)
	if err != nil {
		return nil, nil, err
	}
	var unknown int64
	if err := tx.WithContext(ctx).Model(&model.RecoveryPointRcloneNativeVersion{}).
		Where("repository_id = ? AND recovery_point_id = ? AND evidence_role NOT IN ?", repositoryID, pointID,
			[]string{model.RecoveryPointRcloneNativeEvidenceRoleOwned, model.RecoveryPointRcloneNativeEvidenceRoleReference}).Count(&unknown).Error; err != nil {
		return nil, nil, lifecycleDeleteIdentityConflict("managed Rclone native evidence role query failed")
	}
	if unknown != 0 {
		return nil, nil, lifecycleDeleteIdentityConflict("managed Rclone native evidence contains an unknown role")
	}
	owned, ownedDigest, err := validateManagedRcloneNativeVersionRows(markerKey, model.RecoveryPointRcloneNativeEvidenceRoleOwned, ownedRows)
	if err != nil {
		return nil, nil, err
	}
	references, referenceDigest, err := validateManagedRcloneNativeVersionRows(markerKey, model.RecoveryPointRcloneNativeEvidenceRoleReference, referenceRows)
	if err != nil {
		return nil, nil, err
	}
	if uint64(len(owned)) != locator.FrozenNativeVersionCount || uint64(len(references)) != locator.FrozenNativeReferenceCount ||
		ownedDigest != locator.FrozenNativeVersionsDigest || referenceDigest != locator.FrozenNativeReferencesDigest {
		return nil, nil, lifecycleDeleteIdentityConflict("managed Rclone native evidence aggregate changed")
	}
	if !validManagedRcloneNativeVersionIdentity(expectedCommitKey, "placeholder") {
		return nil, nil, lifecycleDeleteIdentityConflict("managed Rclone native control commit key is unavailable")
	}
	commitVersions := 0
	for _, version := range owned {
		if version.PhysicalKey == expectedCommitKey {
			commitVersions++
		}
	}
	if commitVersions != 1 {
		return nil, nil, lifecycleDeleteIdentityConflict("managed Rclone native owned evidence must contain exactly one commit version")
	}
	return owned, references, nil
}

func loadManagedRcloneNativeVersionRoleTx(
	ctx context.Context,
	tx *gorm.DB,
	repositoryID, pointID, role string,
) ([]model.RecoveryPointRcloneNativeVersion, error) {
	var rows []model.RecoveryPointRcloneNativeVersion
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("repository_id = ? AND recovery_point_id = ? AND evidence_role = ?", repositoryID, pointID, role).
		Order("ordinal ASC").Find(&rows).Error; err != nil {
		return nil, lifecycleDeleteIdentityConflict("managed Rclone native evidence rows unavailable")
	}
	return rows, nil
}

func validateManagedRcloneNativeVersionRows(
	markerKey []byte,
	role string,
	rows []model.RecoveryPointRcloneNativeVersion,
) ([]provider.RcloneNativeExactVersion, string, error) {
	if role == model.RecoveryPointRcloneNativeEvidenceRoleOwned && len(rows) == 0 {
		return nil, "", lifecycleDeleteIdentityConflict("managed Rclone native owned evidence rows are missing")
	}
	identities := make([]string, len(rows))
	versions := make([]provider.RcloneNativeExactVersion, len(rows))
	seen := make(map[string]struct{}, len(rows))
	for ordinal, row := range rows {
		if row.Ordinal != int64(ordinal) || row.EvidenceRole != role || !validManagedRcloneNativeVersionIdentity(row.EncryptedPhysicalKey, row.EncryptedVersionID) ||
			!isLowerHex64(row.IdentityDigest) {
			return nil, "", lifecycleDeleteIdentityConflict("managed Rclone native evidence row ordering or identity changed")
		}
		identity := row.EncryptedPhysicalKey + "\x00" + row.EncryptedVersionID
		if _, exists := seen[identity]; exists {
			return nil, "", lifecycleDeleteIdentityConflict("managed Rclone native evidence contains a duplicate")
		}
		seen[identity] = struct{}{}
		expected := managedRcloneNativeVersionIdentityDigest(markerKey, row.EncryptedPhysicalKey, row.EncryptedVersionID)
		if expected == "" || expected != row.IdentityDigest {
			return nil, "", lifecycleDeleteIdentityConflict("managed Rclone native evidence identity HMAC changed")
		}
		identities[ordinal] = row.IdentityDigest
		versions[ordinal] = provider.RcloneNativeExactVersion{PhysicalKey: row.EncryptedPhysicalKey, VersionID: row.EncryptedVersionID}
	}
	digest := managedRcloneNativeVersionAggregateDigest(markerKey, role, identities)
	if !isLowerHex64(digest) {
		return nil, "", lifecycleDeleteIdentityConflict("managed Rclone native evidence aggregate unavailable")
	}
	return versions, digest, nil
}

func rejectManagedRcloneNativeReferenceIntersectionTx(
	ctx context.Context,
	tx *gorm.DB,
	repositoryID, pointID string,
	owned []provider.RcloneNativeExactVersion,
	markerKey []byte,
	binding managedRcloneBindingDocumentV3,
) error {
	if tx == nil || len(owned) == 0 || len(markerKey) < 32 || binding.Native == nil {
		return lifecycleDeleteIdentityConflict("managed Rclone native owned evidence is unavailable")
	}
	ownedIdentities := make(map[string]struct{}, len(owned))
	for _, version := range owned {
		identity := managedRcloneNativeVersionIdentityDigest(markerKey, version.PhysicalKey, version.VersionID)
		if !isLowerHex64(identity) {
			return lifecycleDeleteIdentityConflict("managed Rclone native owned identity HMAC is unavailable")
		}
		ownedIdentities[identity] = struct{}{}
	}

	var otherPoints []model.RecoveryPoint
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("repository_id = ? AND id <> ? AND state NOT IN ?",
			repositoryID, pointID, []string{
				string(backupasset.RecoveryPointExpired),
				string(backupasset.RecoveryPointFailed),
			}).
		Order("id ASC").Find(&otherPoints).Error; err != nil {
		return lifecycleDeleteIdentityConflict("managed Rclone native live point query failed")
	}
	for _, otherPoint := range otherPoints {
		if otherPoint.State == string(backupasset.RecoveryPointPreparing) {
			return fmt.Errorf("%w: managed Rclone native publication is preparing", provider.ErrDeletePointNativeVersionReferenced)
		}
		otherLocator, err := decodeManagedRclonePointLocator(lifecycleDeleteLocator(otherPoint))
		if err != nil || otherLocator.PublicationMode != backupasset.PublicationNativeObjectVersions ||
			otherLocator.RepositoryID != repositoryID || otherLocator.RecoveryPointID != otherPoint.ID {
			return lifecycleDeleteIdentityConflict("managed Rclone native live point evidence is unavailable")
		}
		otherAttempt, err := provider.DecodeRcloneAttemptV1(otherLocator.TaggedAttempt)
		if err != nil {
			return lifecycleDeleteIdentityConflict("managed Rclone native live point attempt is unavailable")
		}
		_, references, err := loadManagedRcloneNativeVersionEvidenceTx(
			ctx, tx, repositoryID, otherPoint.ID, markerKey, otherLocator,
			managedRcloneNativeControlCommitKey(binding, otherAttempt),
		)
		if err != nil {
			return err
		}
		for _, reference := range references {
			identity := managedRcloneNativeVersionIdentityDigest(markerKey, reference.PhysicalKey, reference.VersionID)
			if _, shared := ownedIdentities[identity]; shared {
				return fmt.Errorf("%w: managed Rclone native version is referenced by another live point", provider.ErrDeletePointNativeVersionReferenced)
			}
		}
	}
	return nil
}
