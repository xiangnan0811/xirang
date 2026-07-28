package export

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/content"
	"xirang/backend/internal/database"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type AttemptCoordinator struct {
	db             *gorm.DB
	now            func() time.Time
	sourceLeases   SourceLeaseCoordinator
	workerCapacity *WorkerCapacityLimits
}

type SourceLeaseCoordinator interface {
	RenewTx(context.Context, *gorm.DB, backupasset.LeaseFence) (backupasset.Lease, error)
	TakeoverTx(context.Context, *gorm.DB, backupasset.TakeoverLeaseRequest) (backupasset.Lease, error)
}

type ExportKeyVersionSource interface {
	ByVersion(context.Context, backupasset.KeyDomain, int) (backupasset.DomainKeyMaterial, error)
}

type PersistentAttemptLoader struct {
	db   *gorm.DB
	keys ExportKeyVersionSource
	now  func() time.Time
}

type PersistentAttemptLoadRequest struct {
	JobID      string
	AttemptID  string
	FenceToken []byte
}

type PersistentAttemptSnapshot struct {
	JobID                   string
	AttemptID               string
	AttemptFenceDigest      string
	AttemptNoncePrefix      []byte
	SelectionDigest         string
	SelectionSchemaVersion  int
	ArchiveFormat           ArchiveFormat
	ArchiveProfile          string
	LimitsSchemaVersion     int
	ChunkBytes              int64
	MaxItems                int
	MaxSourcePoints         int
	MaxItemBytes            int64
	MaxLogicalBytes         int64
	MaxProviderBytes        int64
	MaxCiphertextBytes      int64
	MaxOpenReaders          int
	MaxDurationSeconds      int64
	MaxAttempts             int
	RetryBaseSeconds        int64
	RetryMaxDelaySeconds    int64
	LeaseTTLSeconds         int64
	LeaseRenewMarginSeconds int64
	ReadyTTL                time.Duration
	AbsoluteDeadline        time.Time
	CurrentFenceRevision    int64
	TransitionRevision      int64
	AttemptLeaseExpires     time.Time
	JobKeyID                string
	KEKVersion              int
	DEK                     []byte
	Items                   []PersistentAttemptItem
}

// ClearKeyMaterial zeros the caller-owned plaintext DEK held by the snapshot.
func (snapshot *PersistentAttemptSnapshot) ClearKeyMaterial() {
	if snapshot == nil {
		return
	}
	clear(snapshot.DEK)
	snapshot.DEK = nil
}

type PersistentAttemptItem struct {
	ItemID         string
	ItemAttemptID  string
	Ordinal        int
	Frozen         FrozenItem
	PathNonce      []byte
	PathCiphertext []byte
	State          ItemState
	ItemUpdatedAt  time.Time
	SpoolDigest    string
	SpoolSize      int64
	SpoolLocator   string
	LogicalBytes   int64
	ProviderBytes  int64
	ErrorCategory  string
	StartedAt      time.Time
	ReadAt         *time.Time
	PackedAt       *time.Time
	FinishedAt     *time.Time
}

func persistentAttemptSnapshotPeakStoreBytes(snapshot PersistentAttemptSnapshot) (int64, error) {
	items := make([]FrozenItem, 0, len(snapshot.Items))
	for _, item := range snapshot.Items {
		items = append(items, item.Frozen)
	}
	return createPeakStoreBytes(items, ServiceConfig{
		ChunkBytes: snapshot.ChunkBytes, MaxItemBytes: snapshot.MaxItemBytes, MaxCiphertextBytes: snapshot.MaxCiphertextBytes,
	})
}

// PreHeaderSpoolFailure marks a source or local failure that occurred before
// an archive member header exists. Callers may checkpoint only this error as a
// failed item and continue building a partial archive.
type PreHeaderSpoolFailure struct {
	cause         error
	providerBytes int64
	itemID        string
}

func NewPreHeaderSpoolFailure(cause error) error {
	return newPreHeaderSpoolFailureWithUnknownProviderBytes(cause)
}

func newPreHeaderSpoolFailureWithUnknownProviderBytes(cause error) error {
	if !recoverablePreHeaderSpoolError(cause) {
		return cause
	}
	return &PreHeaderSpoolFailure{cause: cause, providerBytes: -1}
}

func newPreHeaderSpoolFailureAfterAuthentication(cause error, itemID string, providerBytes int64) error {
	if !recoverablePreHeaderAuthenticatedSpoolError(cause) || backupasset.ValidateOpaqueID(itemID) != nil || providerBytes < 0 {
		return cause
	}
	return &PreHeaderSpoolFailure{cause: cause, providerBytes: providerBytes, itemID: itemID}
}

func recoverablePreHeaderAuthenticatedSpoolError(cause error) bool {
	if !recoverablePreHeaderSpoolError(cause) || errors.Is(cause, ErrInvalidStore) ||
		errors.Is(cause, ErrStoreObjectUnsafe) {
		return false
	}
	if errors.Is(cause, ErrCipherTampered) {
		return true
	}
	return errors.Is(cause, ErrStoreObjectAbsent) && errors.Is(cause, os.ErrNotExist)
}

func (failure *PreHeaderSpoolFailure) Error() string {
	if failure == nil || failure.cause == nil {
		return "export pre-header spool failed"
	}
	return failure.cause.Error()
}

func (failure *PreHeaderSpoolFailure) Unwrap() error {
	if failure == nil {
		return nil
	}
	return failure.cause
}

func (failure *PreHeaderSpoolFailure) ProviderBytes() int64 {
	if failure == nil {
		return -1
	}
	return failure.providerBytes
}

// ItemID identifies the authenticated, purged spool that failed before an
// archive member header was emitted. It is empty for source-stage failures.
func (failure *PreHeaderSpoolFailure) ItemID() string {
	if failure == nil {
		return ""
	}
	return failure.itemID
}

func NewPersistentAttemptLoader(
	db *gorm.DB, keys ExportKeyVersionSource, now func() time.Time,
) (*PersistentAttemptLoader, error) {
	if db == nil || keys == nil {
		return nil, ErrUnavailable
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &PersistentAttemptLoader{db: db, keys: keys, now: now}, nil
}

type persistedAttemptLoad struct {
	job                   model.BackupAssetExportJob
	attempt               model.BackupAssetExportAttempt
	key                   model.BackupAssetExportKey
	items                 []model.BackupAssetExportItem
	itemAttempts          []model.BackupAssetExportItemAttempt
	storeReservations     []model.BackupAssetExportReservation
	storeBucketIdentities []persistedStoreBucketIdentity
}

type persistedStoreBucketIdentity struct {
	ID      string
	Scope   string
	Subject string
}

const (
	persistentAttemptStableLoadLimit  = 4
	persistentAttemptStoreCardinality = 2
)

func (loader *PersistentAttemptLoader) Load(
	ctx context.Context, request PersistentAttemptLoadRequest,
) (PersistentAttemptSnapshot, error) {
	if loader == nil || backupasset.ValidateOpaqueID(request.JobID) != nil ||
		backupasset.ValidateOpaqueID(request.AttemptID) != nil || len(request.FenceToken) != 32 {
		return PersistentAttemptSnapshot{}, ErrAttemptFenceLost
	}
	ctx = nonNilServiceContext(ctx)
	now := loader.now().UTC()
	for loadAttempt := 0; loadAttempt < persistentAttemptStableLoadLimit; loadAttempt++ {
		if err := ctx.Err(); err != nil {
			return PersistentAttemptSnapshot{}, err
		}
		snapshot, tupleChange, err := loader.loadSnapshotForAttempt(ctx, request, now)
		if err != nil {
			return PersistentAttemptSnapshot{}, err
		}
		switch tupleChange {
		case persistedAttemptTupleStable:
			return snapshot, nil
		case persistedAttemptTupleProgress:
			continue
		case persistedAttemptTupleAuthorityLost:
			return PersistentAttemptSnapshot{}, ErrAttemptFenceLost
		default:
			return PersistentAttemptSnapshot{}, ErrUnavailable
		}
	}
	return PersistentAttemptSnapshot{}, ErrUnavailable
}

func (loader *PersistentAttemptLoader) loadSnapshotForAttempt(
	ctx context.Context, request PersistentAttemptLoadRequest, now time.Time,
) (PersistentAttemptSnapshot, persistedAttemptTupleChange, error) {
	persisted, err := loader.loadPersistedAttempt(ctx, request, now)
	if err != nil {
		return PersistentAttemptSnapshot{}, persistedAttemptTupleInvalid, err
	}
	material, err := loader.keys.ByVersion(ctx, backupasset.KeyDomainExportStore, persisted.key.KEKVersion)
	defer clear(material.Key)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return PersistentAttemptSnapshot{}, persistedAttemptTupleInvalid, ctxErr
	}
	if err != nil {
		if errors.Is(err, backupasset.ErrKeyUnavailable) || errors.Is(err, backupasset.ErrKeyLost) {
			return PersistentAttemptSnapshot{}, persistedAttemptTupleInvalid, ErrUnavailable
		}
		return PersistentAttemptSnapshot{}, persistedAttemptTupleInvalid, fmt.Errorf("load export KEK: %w", err)
	}
	if material.Domain != backupasset.KeyDomainExportStore || material.Version != persisted.key.KEKVersion ||
		(material.State != backupasset.DomainKeyActive && material.State != backupasset.DomainKeyVerifyOnly) || len(material.Key) != 32 {
		return PersistentAttemptSnapshot{}, persistedAttemptTupleInvalid, ErrUnavailable
	}
	dek, err := UnwrapJobDEK(JobKeyBinding{
		ExportID: persisted.job.ID, SelectionDigest: persisted.job.SelectionDigest,
		KEKVersion: persisted.key.KEKVersion, WrapAlgorithm: persisted.key.WrapAlgorithm,
	}, material.Key, JobKeyEnvelope{
		Nonce: persisted.key.EnvelopeNonce, Ciphertext: persisted.key.WrappedDEK,
	})
	defer clear(dek)
	if err != nil {
		return PersistentAttemptSnapshot{}, persistedAttemptTupleInvalid, ErrUnavailable
	}
	reloaded, err := loader.loadPersistedAttempt(ctx, request, now)
	if err != nil {
		return PersistentAttemptSnapshot{}, persistedAttemptTupleInvalid, err
	}
	tupleChange := classifyPersistedAttemptTupleChange(persisted, reloaded)
	if tupleChange != persistedAttemptTupleStable {
		return PersistentAttemptSnapshot{}, tupleChange, nil
	}
	snapshot, err := persistentAttemptSnapshot(reloaded, dek)
	return snapshot, tupleChange, err
}

func persistentAttemptSnapshot(
	persisted persistedAttemptLoad, dek []byte,
) (PersistentAttemptSnapshot, error) {
	frozenItems, peakStoreBytes, err := validatePersistedAttemptSelection(persisted.job, persisted.items, dek)
	if err != nil {
		return PersistentAttemptSnapshot{}, err
	}
	if validatePersistedAttemptStoreReservations(persisted, peakStoreBytes) != nil {
		return PersistentAttemptSnapshot{}, ErrUnavailable
	}
	itemAttempts := make(map[string]model.BackupAssetExportItemAttempt, len(persisted.itemAttempts))
	for _, row := range persisted.itemAttempts {
		if _, duplicate := itemAttempts[row.ItemID]; duplicate {
			return PersistentAttemptSnapshot{}, ErrUnavailable
		}
		itemAttempts[row.ItemID] = row
	}
	snapshotItems := make([]PersistentAttemptItem, 0, len(persisted.items))
	for ordinal, row := range persisted.items {
		itemAttempt, found := itemAttempts[row.ID]
		if !found {
			return PersistentAttemptSnapshot{}, ErrUnavailable
		}
		snapshotItems = append(snapshotItems, PersistentAttemptItem{
			ItemID: row.ID, ItemAttemptID: itemAttempt.ID, Ordinal: row.Ordinal, Frozen: frozenItems[ordinal],
			PathNonce: append([]byte(nil), row.PathNonce...), PathCiphertext: append([]byte(nil), row.PathCiphertext...),
			State: ItemState(itemAttempt.State), ItemUpdatedAt: row.UpdatedAt.UTC(),
			SpoolDigest: itemAttempt.SpoolDigest, SpoolSize: itemAttempt.SpoolSize, SpoolLocator: itemAttempt.SpoolLocator,
			LogicalBytes: itemAttempt.LogicalBytes, ProviderBytes: itemAttempt.ProviderBytes,
			ErrorCategory: itemAttempt.ErrorCategory, StartedAt: itemAttempt.StartedAt.UTC(),
			ReadAt: cloneOptionalTime(itemAttempt.ReadAt), PackedAt: cloneOptionalTime(itemAttempt.PackedAt),
			FinishedAt: cloneOptionalTime(itemAttempt.FinishedAt),
		})
	}
	snapshot := PersistentAttemptSnapshot{
		JobID: persisted.job.ID, AttemptID: persisted.attempt.ID, AttemptFenceDigest: persisted.attempt.FenceDigest,
		AttemptNoncePrefix: append([]byte(nil), persisted.attempt.NoncePrefix...),
		SelectionDigest:    persisted.job.SelectionDigest, SelectionSchemaVersion: persisted.job.SelectionSchemaVersion,
		ArchiveFormat: ArchiveFormat(persisted.job.ArchiveFormat), ArchiveProfile: persisted.job.ArchiveProfile,
		LimitsSchemaVersion: persisted.job.LimitsSchemaVersion,
		ChunkBytes:          persisted.job.ChunkBytes, MaxItems: persisted.job.MaxItems,
		MaxSourcePoints: persisted.job.MaxSourcePoints,
		MaxItemBytes:    persisted.job.MaxItemBytes, MaxLogicalBytes: persisted.job.MaxLogicalBytes,
		MaxProviderBytes:        persisted.job.MaxProviderBytes,
		MaxCiphertextBytes:      persisted.job.MaxCiphertextBytes,
		MaxOpenReaders:          persisted.job.MaxOpenReaders,
		MaxDurationSeconds:      persisted.job.MaxDurationSeconds,
		MaxAttempts:             persisted.job.MaxAttempts,
		RetryBaseSeconds:        persisted.job.RetryBaseSeconds,
		RetryMaxDelaySeconds:    persisted.job.RetryMaxDelaySeconds,
		LeaseTTLSeconds:         persisted.job.LeaseTTLSeconds,
		LeaseRenewMarginSeconds: persisted.job.LeaseRenewMarginSeconds,
		ReadyTTL:                time.Duration(persisted.job.ReadyTTLSeconds) * time.Second,
		AbsoluteDeadline:        persisted.job.AbsoluteDeadline.UTC(),
		CurrentFenceRevision:    persisted.job.CurrentFenceRevision,
		TransitionRevision:      persisted.job.TransitionRevision,
		AttemptLeaseExpires:     persisted.attempt.LeaseExpiresAt.UTC(), KEKVersion: persisted.key.KEKVersion,
		JobKeyID: persisted.key.ID,
		DEK:      append([]byte(nil), dek...), Items: snapshotItems,
	}
	return snapshot, nil
}

func validatePersistedAttemptSelection(
	job model.BackupAssetExportJob,
	items []model.BackupAssetExportItem,
	dek []byte,
) ([]FrozenItem, int64, error) {
	limits := SelectionLimits{
		MaxItems: job.MaxItems, MaxSourcePoints: job.MaxSourcePoints, MaxLogicalBytes: job.MaxLogicalBytes,
	}
	if !validSelectionLimits(limits) || job.MaxSourcePoints > job.MaxItems || job.MaxItemBytes <= 0 ||
		job.MaxItemBytes > job.MaxLogicalBytes || job.MaxProviderBytes < job.MaxLogicalBytes ||
		job.MaxCiphertextBytes <= 0 || !validCipherChunkBytesV1(job.ChunkBytes) {
		return nil, 0, ErrUnavailable
	}
	minimumCiphertextBytes, err := minimumArchiveCiphertextBytesV1(
		job.MaxLogicalBytes, job.MaxItems, job.ChunkBytes,
	)
	if err != nil || job.MaxCiphertextBytes < minimumCiphertextBytes {
		return nil, 0, ErrUnavailable
	}

	decoded := make([]FrozenItem, 0, len(items))
	for ordinal, row := range items {
		if row.Ordinal != ordinal {
			return nil, 0, ErrUnavailable
		}
		components, err := decryptSelectionPath(
			dek, job.ID, row.ID, job.SelectionDigest, row.PathNonce, row.PathCiphertext,
		)
		if err != nil {
			return nil, 0, ErrUnavailable
		}
		item := frozenItemFromModel(job.SelectionSchemaVersion, row, components)
		if ValidateFrozenItem(item) != nil ||
			(item.EntryType == backupasset.CatalogEntryFile && item.LogicalSize > job.MaxItemBytes) {
			return nil, 0, ErrUnavailable
		}
		decoded = append(decoded, item)
	}

	frozen, err := FreezeSelection(decoded, nil, limits)
	if err != nil || frozen.Digest != job.SelectionDigest || len(frozen.Items) != len(decoded) ||
		int64(len(frozen.Items)) != job.ItemCount {
		return nil, 0, ErrUnavailable
	}
	for ordinal := range decoded {
		if !frozenItemsEqual(decoded[ordinal], frozen.Items[ordinal]) {
			return nil, 0, ErrUnavailable
		}
	}
	peakStoreBytes, err := createPeakStoreBytes(frozen.Items, ServiceConfig{
		ChunkBytes: job.ChunkBytes, MaxItemBytes: job.MaxItemBytes, MaxCiphertextBytes: job.MaxCiphertextBytes,
	})
	if err != nil {
		return nil, 0, ErrUnavailable
	}
	return frozen.Items, peakStoreBytes, nil
}

func validatePersistedAttemptStoreReservations(persisted persistedAttemptLoad, peakStoreBytes int64) error {
	if persisted.job.OwnerUserID == 0 || peakStoreBytes <= 0 ||
		len(persisted.storeReservations) != persistentAttemptStoreCardinality ||
		len(persisted.storeBucketIdentities) != persistentAttemptStoreCardinality {
		return ErrUnavailable
	}
	userSubject := strconv.FormatUint(uint64(persisted.job.OwnerUserID), 10)
	bucketScopes := make(map[string]string, persistentAttemptStoreCardinality)
	for _, bucket := range persisted.storeBucketIdentities {
		if backupasset.ValidateOpaqueID(bucket.ID) != nil {
			return ErrUnavailable
		}
		switch {
		case bucket.Scope == "global" && bucket.Subject == "global":
			bucketScopes[bucket.ID] = "global"
		case bucket.Scope == "user" && bucket.Subject == userSubject:
			bucketScopes[bucket.ID] = "user"
		default:
			return ErrUnavailable
		}
	}
	if len(bucketScopes) != persistentAttemptStoreCardinality {
		return ErrUnavailable
	}
	seenScopes := make(map[string]struct{}, persistentAttemptStoreCardinality)
	for _, reservation := range persisted.storeReservations {
		scope, found := bucketScopes[reservation.BucketID]
		if !found || backupasset.ValidateOpaqueID(reservation.ID) != nil ||
			backupasset.ValidateOpaqueID(reservation.BucketID) != nil || reservation.JobID == nil ||
			*reservation.JobID != persisted.job.ID || backupasset.ValidateOpaqueID(*reservation.JobID) != nil ||
			reservation.AttemptID != nil || reservation.Kind != "store" || reservation.ReservedSlots != 0 ||
			reservation.ReservedLogicalBytes != 0 || reservation.ReservedProviderBytes != 0 ||
			reservation.ReservedCipherBytes != 0 || reservation.ReservedStoreBytes != peakStoreBytes ||
			reservation.LeaseOwner != persisted.job.ID || backupasset.ValidateOpaqueID(reservation.LeaseOwner) != nil ||
			!reservation.LeaseExpiresAt.Equal(persisted.job.AbsoluteDeadline) || reservation.State != "active" ||
			reservation.ReleasedAt != nil {
			return ErrUnavailable
		}
		if _, duplicate := seenScopes[scope]; duplicate {
			return ErrUnavailable
		}
		seenScopes[scope] = struct{}{}
	}
	if len(seenScopes) != persistentAttemptStoreCardinality {
		return ErrUnavailable
	}
	return nil
}

type persistedAttemptTupleChange uint8

const (
	persistedAttemptTupleInvalid persistedAttemptTupleChange = iota
	persistedAttemptTupleStable
	persistedAttemptTupleProgress
	persistedAttemptTupleAuthorityLost
)

func classifyPersistedAttemptTupleChange(left, right persistedAttemptLoad) persistedAttemptTupleChange {
	if persistedAttemptAuthorityDrifted(left, right) {
		return persistedAttemptTupleAuthorityLost
	}
	if !samePersistedAttemptImmutableTuple(left, right) {
		return persistedAttemptTupleInvalid
	}

	keyChanged := !samePersistedAttemptKey(left.key, right.key)
	if keyChanged && !validPersistedAttemptKeyRewrap(left.key, right.key) {
		return persistedAttemptTupleInvalid
	}
	progressed, valid := validPersistedAttemptProgress(left, right)
	if !valid {
		return persistedAttemptTupleInvalid
	}
	if keyChanged || progressed {
		return persistedAttemptTupleProgress
	}
	return persistedAttemptTupleStable
}

func samePersistedAttemptImmutableTuple(left, right persistedAttemptLoad) bool {
	return samePersistedAttemptJobImmutableFields(left.job, right.job) &&
		samePersistedAttemptIdentityImmutableFields(left.attempt, right.attempt) &&
		samePersistedAttemptItemsImmutableFields(left.items, right.items) &&
		samePersistedAttemptItemAttemptsImmutableFields(left.itemAttempts, right.itemAttempts) &&
		samePersistedAttemptReservations(left.storeReservations, right.storeReservations) &&
		samePersistedAttemptBucketIdentities(left.storeBucketIdentities, right.storeBucketIdentities)
}

func samePersistedAttemptJobImmutableFields(left, right model.BackupAssetExportJob) bool {
	return left.ID == right.ID && left.OwnerUserID == right.OwnerUserID &&
		left.SelectionDigest == right.SelectionDigest && left.SelectionSchemaVersion == right.SelectionSchemaVersion &&
		left.ArchiveFormat == right.ArchiveFormat && left.ArchiveProfile == right.ArchiveProfile &&
		left.LimitsSchemaVersion == right.LimitsSchemaVersion && left.ChunkBytes == right.ChunkBytes &&
		left.MaxItems == right.MaxItems && left.MaxSourcePoints == right.MaxSourcePoints &&
		left.MaxItemBytes == right.MaxItemBytes && left.MaxLogicalBytes == right.MaxLogicalBytes &&
		left.MaxProviderBytes == right.MaxProviderBytes && left.MaxCiphertextBytes == right.MaxCiphertextBytes &&
		left.MaxOpenReaders == right.MaxOpenReaders && left.MaxDurationSeconds == right.MaxDurationSeconds &&
		left.MaxAttempts == right.MaxAttempts && left.RetryBaseSeconds == right.RetryBaseSeconds &&
		left.RetryMaxDelaySeconds == right.RetryMaxDelaySeconds && left.LeaseTTLSeconds == right.LeaseTTLSeconds &&
		left.LeaseRenewMarginSeconds == right.LeaseRenewMarginSeconds && left.ReadyTTLSeconds == right.ReadyTTLSeconds &&
		left.CleanupState == right.CleanupState && left.ResultKind == right.ResultKind &&
		left.AbsoluteDeadline.Equal(right.AbsoluteDeadline) && sameOptionalTime(left.ReadyAt, right.ReadyAt) &&
		sameOptionalTime(left.ExpiresAt, right.ExpiresAt) && left.ItemCount == right.ItemCount &&
		left.ArtifactBytes == right.ArtifactBytes && left.ErrorCategory == right.ErrorCategory &&
		left.CreatedAt.Equal(right.CreatedAt)
}

func samePersistedAttemptIdentityImmutableFields(left, right model.BackupAssetExportAttempt) bool {
	return left.ID == right.ID && left.JobID == right.JobID && left.AttemptNumber == right.AttemptNumber &&
		left.StartedAt.Equal(right.StartedAt) && sameOptionalTime(left.FinishedAt, right.FinishedAt) &&
		left.CreatedAt.Equal(right.CreatedAt) && left.StagingLocator == right.StagingLocator &&
		left.FailureCategory == right.FailureCategory
}

func samePersistedAttemptItemsImmutableFields(left, right []model.BackupAssetExportItem) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		leftItem, rightItem := left[index], right[index]
		if leftItem.ID != rightItem.ID || leftItem.JobID != rightItem.JobID || leftItem.Ordinal != rightItem.Ordinal ||
			leftItem.RecoveryPointID != rightItem.RecoveryPointID || leftItem.EntryID != rightItem.EntryID ||
			leftItem.CatalogGenerationID != rightItem.CatalogGenerationID || leftItem.SourceFingerprint != rightItem.SourceFingerprint ||
			leftItem.EntryFingerprint != rightItem.EntryFingerprint || leftItem.FingerprintStrength != rightItem.FingerprintStrength ||
			leftItem.ProviderCapabilityRevision != rightItem.ProviderCapabilityRevision || leftItem.EntryType != rightItem.EntryType ||
			leftItem.LogicalSize != rightItem.LogicalSize || leftItem.MediaType != rightItem.MediaType ||
			!sameOptionalTime(leftItem.RetentionUntil, rightItem.RetentionUntil) || leftItem.SelectionRootOrdinal != rightItem.SelectionRootOrdinal ||
			!sameBytes(leftItem.PathNonce, rightItem.PathNonce) || !sameBytes(leftItem.PathCiphertext, rightItem.PathCiphertext) ||
			!leftItem.CreatedAt.Equal(rightItem.CreatedAt) {
			return false
		}
	}
	return true
}

func samePersistedAttemptItemAttemptsImmutableFields(left, right []model.BackupAssetExportItemAttempt) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		leftAttempt, rightAttempt := left[index], right[index]
		if leftAttempt.ID != rightAttempt.ID || leftAttempt.JobID != rightAttempt.JobID || leftAttempt.ItemID != rightAttempt.ItemID ||
			leftAttempt.AttemptID != rightAttempt.AttemptID || !leftAttempt.StartedAt.Equal(rightAttempt.StartedAt) ||
			!leftAttempt.CreatedAt.Equal(rightAttempt.CreatedAt) {
			return false
		}
	}
	return true
}

func samePersistedAttemptReservations(left, right []model.BackupAssetExportReservation) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		l, r := left[index], right[index]
		if !sameOptionalString(l.JobID, r.JobID) || !sameOptionalString(l.AttemptID, r.AttemptID) ||
			l.ID != r.ID || l.BucketID != r.BucketID || l.Kind != r.Kind || l.ReservedSlots != r.ReservedSlots ||
			l.ReservedLogicalBytes != r.ReservedLogicalBytes || l.ReservedProviderBytes != r.ReservedProviderBytes ||
			l.ReservedCipherBytes != r.ReservedCipherBytes || l.ReservedStoreBytes != r.ReservedStoreBytes ||
			l.LeaseOwner != r.LeaseOwner || !l.LeaseExpiresAt.Equal(r.LeaseExpiresAt) || l.State != r.State ||
			!l.CreatedAt.Equal(r.CreatedAt) || !l.UpdatedAt.Equal(r.UpdatedAt) || !sameOptionalTime(l.ReleasedAt, r.ReleasedAt) {
			return false
		}
	}
	return true
}

func samePersistedAttemptBucketIdentities(left, right []persistedStoreBucketIdentity) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func persistedAttemptAuthorityDrifted(left, right persistedAttemptLoad) bool {
	if left.job.ExecutionState != right.job.ExecutionState ||
		!sameOptionalString(left.job.CurrentAttemptID, right.job.CurrentAttemptID) ||
		left.job.CurrentFenceRevision != right.job.CurrentFenceRevision ||
		left.attempt.WorkerOwner != right.attempt.WorkerOwner || left.attempt.State != right.attempt.State ||
		!sameBytes(left.attempt.FenceToken, right.attempt.FenceToken) || left.attempt.FenceDigest != right.attempt.FenceDigest ||
		!sameBytes(left.attempt.NoncePrefix, right.attempt.NoncePrefix) || left.attempt.IsCurrent != right.attempt.IsCurrent {
		return true
	}
	if len(left.items) != len(right.items) {
		return false
	}
	for index := range left.items {
		if !sameOptionalString(left.items[index].CurrentAttemptID, right.items[index].CurrentAttemptID) {
			return true
		}
	}
	return false
}

func samePersistedAttemptKey(left, right model.BackupAssetExportKey) bool {
	return left.ID == right.ID && left.JobID == right.JobID && left.State == right.State &&
		sameBytes(left.WrappedDEK, right.WrappedDEK) && sameBytes(left.EnvelopeNonce, right.EnvelopeNonce) &&
		left.KEKVersion == right.KEKVersion && left.WrapAlgorithm == right.WrapAlgorithm &&
		left.KeyRevision == right.KeyRevision && left.CreatedAt.Equal(right.CreatedAt) &&
		sameOptionalTime(left.RewrappedAt, right.RewrappedAt) && sameOptionalTime(left.DestroyedAt, right.DestroyedAt)
}

func validPersistedAttemptKeyRewrap(left, right model.BackupAssetExportKey) bool {
	if left.ID != right.ID || left.JobID != right.JobID || left.State != "active" || right.State != "active" ||
		left.WrapAlgorithm != right.WrapAlgorithm || !left.CreatedAt.Equal(right.CreatedAt) ||
		!sameOptionalTime(left.DestroyedAt, right.DestroyedAt) || right.KeyRevision <= left.KeyRevision ||
		right.KEKVersion < left.KEKVersion || right.RewrappedAt == nil ||
		(right.RewrappedAt != nil && right.RewrappedAt.Before(left.CreatedAt)) ||
		(left.RewrappedAt != nil && right.RewrappedAt.Before(*left.RewrappedAt)) ||
		sameBytes(left.EnvelopeNonce, right.EnvelopeNonce) || sameBytes(left.WrappedDEK, right.WrappedDEK) {
		return false
	}
	return true
}

func validPersistedAttemptProgress(left, right persistedAttemptLoad) (bool, bool) {
	jobProgressed, valid := validPersistedJobProgress(left, right)
	if !valid {
		return false, false
	}
	attemptProgressed, valid := validPersistedAttemptProgressFields(left, right)
	if !valid {
		return false, false
	}
	itemProgressed, valid := validPersistedItemProgress(left, right)
	if !valid {
		return false, false
	}
	return jobProgressed || attemptProgressed || itemProgressed, true
}

func validPersistedJobProgress(left, right persistedAttemptLoad) (bool, bool) {
	if !validPersistedJobAggregates(left.job, left.itemAttempts) || !validPersistedJobAggregates(right.job, right.itemAttempts) ||
		right.job.PackedCount < left.job.PackedCount || right.job.SkippedCount < left.job.SkippedCount ||
		right.job.FailedCount < left.job.FailedCount || right.job.LogicalBytes < left.job.LogicalBytes ||
		right.job.ProviderBytes < left.job.ProviderBytes || right.job.TransitionRevision < left.job.TransitionRevision ||
		right.job.UpdatedAt.Before(left.job.UpdatedAt) {
		return false, false
	}
	aggregateChanged := left.job.PackedCount != right.job.PackedCount || left.job.SkippedCount != right.job.SkippedCount ||
		left.job.FailedCount != right.job.FailedCount || left.job.LogicalBytes != right.job.LogicalBytes ||
		left.job.ProviderBytes != right.job.ProviderBytes
	if aggregateChanged && right.job.TransitionRevision <= left.job.TransitionRevision {
		return false, false
	}
	if !aggregateChanged && right.job.TransitionRevision != left.job.TransitionRevision {
		return false, false
	}
	if !aggregateChanged && !right.job.UpdatedAt.Equal(left.job.UpdatedAt) {
		return false, false
	}
	return aggregateChanged, true
}

func validPersistedJobAggregates(job model.BackupAssetExportJob, itemAttempts []model.BackupAssetExportItemAttempt) bool {
	if job.ItemCount <= 0 || job.PackedCount < 0 || job.SkippedCount < 0 || job.FailedCount < 0 ||
		job.PackedCount > job.ItemCount || job.SkippedCount > job.ItemCount || job.FailedCount > job.ItemCount ||
		job.PackedCount+job.SkippedCount+job.FailedCount > job.ItemCount || job.LogicalBytes < 0 ||
		job.LogicalBytes > job.MaxLogicalBytes || job.ProviderBytes < 0 || job.ProviderBytes > job.MaxProviderBytes {
		return false
	}
	var packed, skipped, failed, logical, provider int64
	for _, row := range itemAttempts {
		if row.LogicalBytes < 0 || row.LogicalBytes > job.MaxLogicalBytes || row.ProviderBytes < 0 ||
			row.ProviderBytes > job.MaxProviderBytes {
			return false
		}
		switch ItemState(row.State) {
		case ItemPacked:
			packed++
		case ItemSkipped:
			skipped++
		case ItemFailed:
			failed++
		case ItemPending, ItemRead:
		default:
			return false
		}
		if ItemState(row.State) == ItemPacked || ItemState(row.State) == ItemSkipped || ItemState(row.State) == ItemFailed {
			if logical > job.MaxLogicalBytes-row.LogicalBytes || provider > job.MaxProviderBytes-row.ProviderBytes {
				return false
			}
			logical += row.LogicalBytes
			provider += row.ProviderBytes
		}
	}
	return job.PackedCount == packed && job.SkippedCount == skipped && job.FailedCount == failed &&
		job.LogicalBytes == logical && job.ProviderBytes == provider
}

func validPersistedAttemptProgressFields(left, right persistedAttemptLoad) (bool, bool) {
	if !validPersistedCheckpoint(left.attempt, left.job) || !validPersistedCheckpoint(right.attempt, right.job) ||
		right.attempt.LeaseExpiresAt.Before(left.attempt.LeaseExpiresAt) ||
		right.attempt.LeaseExpiresAt.After(right.job.AbsoluteDeadline) ||
		right.attempt.CheckpointOrdinal < left.attempt.CheckpointOrdinal ||
		right.attempt.CheckpointItemCount < left.attempt.CheckpointItemCount ||
		right.attempt.CheckpointLogicalBytes < left.attempt.CheckpointLogicalBytes ||
		right.attempt.CheckpointProviderBytes < left.attempt.CheckpointProviderBytes ||
		right.attempt.CheckpointItemCount > right.job.ItemCount ||
		right.attempt.CheckpointLogicalBytes > right.job.MaxLogicalBytes ||
		right.attempt.CheckpointProviderBytes > right.job.MaxProviderBytes ||
		right.attempt.UpdatedAt.Before(left.attempt.UpdatedAt) {
		return false, false
	}
	return !right.attempt.LeaseExpiresAt.Equal(left.attempt.LeaseExpiresAt) ||
		right.attempt.CheckpointOrdinal != left.attempt.CheckpointOrdinal ||
		right.attempt.CheckpointItemCount != left.attempt.CheckpointItemCount ||
		right.attempt.CheckpointLogicalBytes != left.attempt.CheckpointLogicalBytes ||
		right.attempt.CheckpointProviderBytes != left.attempt.CheckpointProviderBytes ||
		!right.attempt.UpdatedAt.Equal(left.attempt.UpdatedAt), true
}

func validPersistedCheckpoint(attempt model.BackupAssetExportAttempt, job model.BackupAssetExportJob) bool {
	return attempt.CheckpointOrdinal >= 0 && attempt.CheckpointOrdinal < job.MaxItems &&
		attempt.CheckpointItemCount == job.PackedCount+job.SkippedCount+job.FailedCount &&
		attempt.CheckpointLogicalBytes == job.LogicalBytes && attempt.CheckpointProviderBytes == job.ProviderBytes
}

func validPersistedItemProgress(left, right persistedAttemptLoad) (bool, bool) {
	if len(left.items) != len(right.items) || len(left.itemAttempts) != len(right.itemAttempts) {
		return false, false
	}
	leftAttempts := make(map[string]model.BackupAssetExportItemAttempt, len(left.itemAttempts))
	rightAttempts := make(map[string]model.BackupAssetExportItemAttempt, len(right.itemAttempts))
	for _, row := range left.itemAttempts {
		if _, duplicate := leftAttempts[row.ItemID]; duplicate {
			return false, false
		}
		leftAttempts[row.ItemID] = row
	}
	for _, row := range right.itemAttempts {
		if _, duplicate := rightAttempts[row.ItemID]; duplicate {
			return false, false
		}
		rightAttempts[row.ItemID] = row
	}
	progressed := false
	for index := range left.items {
		leftItem, rightItem := left.items[index], right.items[index]
		leftAttempt, leftFound := leftAttempts[leftItem.ID]
		rightAttempt, rightFound := rightAttempts[rightItem.ID]
		if !leftFound || !rightFound {
			return false, false
		}
		itemChanged, valid := validPersistedItemRowProgress(leftItem, rightItem, leftAttempt, rightAttempt, right.job)
		if !valid {
			return false, false
		}
		itemAttemptChanged, valid := validPersistedItemAttemptRowProgress(leftAttempt, rightAttempt, rightItem, right.job)
		if !valid {
			return false, false
		}
		progressed = progressed || itemChanged || itemAttemptChanged
	}
	return progressed, true
}

func validPersistedItemRowProgress(
	left, right model.BackupAssetExportItem,
	leftAttempt, rightAttempt model.BackupAssetExportItemAttempt,
	job model.BackupAssetExportJob,
) (bool, bool) {
	if !validPersistedItemStateProgress(ItemState(left.State), ItemState(right.State)) ||
		!validPersistedItemProjection(right, job) || leftAttempt.ItemID != left.ID || rightAttempt.ItemID != right.ID ||
		ItemState(left.State) != ItemState(leftAttempt.State) || ItemState(right.State) != ItemState(rightAttempt.State) {
		return false, false
	}
	if ItemState(right.State) != ItemPending &&
		(right.LogicalBytes != rightAttempt.LogicalBytes || right.ProviderBytes != rightAttempt.ProviderBytes || right.ErrorCategory != rightAttempt.ErrorCategory) {
		return false, false
	}
	if right.UpdatedAt.Before(left.UpdatedAt) {
		return false, false
	}
	stateChanged := left.State != right.State
	fieldsChanged := left.LogicalBytes != right.LogicalBytes || left.ProviderBytes != right.ProviderBytes ||
		left.ErrorCategory != right.ErrorCategory || !right.UpdatedAt.Equal(left.UpdatedAt)
	if !stateChanged && fieldsChanged {
		return false, false
	}
	changed := stateChanged
	return changed, true
}

func validPersistedItemAttemptRowProgress(
	left, right model.BackupAssetExportItemAttempt,
	item model.BackupAssetExportItem,
	job model.BackupAssetExportJob,
) (bool, bool) {
	if !validPersistedItemStateProgress(ItemState(left.State), ItemState(right.State)) ||
		!validPersistedItemAttemptState(right, item, job) || right.ProviderBytes < left.ProviderBytes ||
		right.LogicalBytes < 0 || right.ProviderBytes < 0 || right.ProviderBytes > job.MaxProviderBytes ||
		right.LogicalBytes > item.LogicalSize {
		return false, false
	}
	if ItemState(right.State) == ItemPending && right.LogicalBytes != 0 {
		return false, false
	}
	stateChanged := left.State != right.State
	fieldsChanged := left.SpoolDigest != right.SpoolDigest || left.SpoolSize != right.SpoolSize ||
		left.SpoolLocator != right.SpoolLocator || left.LogicalBytes != right.LogicalBytes || left.ProviderBytes != right.ProviderBytes ||
		left.ErrorCategory != right.ErrorCategory || !sameOptionalTime(left.ReadAt, right.ReadAt) ||
		!sameOptionalTime(left.PackedAt, right.PackedAt) || !sameOptionalTime(left.FinishedAt, right.FinishedAt)
	if !stateChanged {
		providerProgress := ItemState(right.State) == ItemPending && right.ProviderBytes > left.ProviderBytes
		if fieldsChanged && !providerProgress {
			return false, false
		}
		if providerProgress && (left.SpoolDigest != right.SpoolDigest || left.SpoolSize != right.SpoolSize ||
			left.SpoolLocator != right.SpoolLocator || left.LogicalBytes != right.LogicalBytes ||
			left.ErrorCategory != right.ErrorCategory || !sameOptionalTime(left.ReadAt, right.ReadAt) ||
			!sameOptionalTime(left.PackedAt, right.PackedAt) || !sameOptionalTime(left.FinishedAt, right.FinishedAt)) {
			return false, false
		}
	}
	changed := stateChanged || fieldsChanged
	return changed, true
}

func validPersistedItemStateProgress(left, right ItemState) bool {
	if left == right {
		return true
	}
	switch left {
	case ItemPending:
		return right == ItemRead || right == ItemPacked || right == ItemSkipped || right == ItemFailed
	case ItemRead:
		return right == ItemPacked || right == ItemFailed
	default:
		return false
	}
}

func validPersistedItemProjection(row model.BackupAssetExportItem, job model.BackupAssetExportJob) bool {
	state := ItemState(row.State)
	if row.LogicalBytes < 0 || row.ProviderBytes < 0 || row.ProviderBytes > job.MaxProviderBytes ||
		row.ErrorCategory == "" && (state == ItemSkipped || state == ItemFailed) {
		return false
	}
	if state == ItemPending && (row.LogicalBytes != 0 || row.ProviderBytes != 0 || row.ErrorCategory != "") {
		return false
	}
	if state == ItemRead && (row.LogicalBytes != row.LogicalSize || row.ErrorCategory != "") {
		return false
	}
	if state == ItemPacked && (row.LogicalBytes != row.LogicalSize || row.ErrorCategory != "") {
		return false
	}
	if state == ItemFailed && (row.EntryType != string(backupasset.CatalogEntryFile) || row.LogicalBytes != 0 ||
		!validPreHeaderFailureCategory(row.ErrorCategory)) {
		return false
	}
	return state == ItemPending || state == ItemRead || state == ItemPacked || state == ItemSkipped || state == ItemFailed
}

func validPersistedItemAttemptState(
	row model.BackupAssetExportItemAttempt,
	item model.BackupAssetExportItem,
	job model.BackupAssetExportJob,
) bool {
	state := ItemState(row.State)
	if state == ItemPending {
		return row.SpoolDigest == "" && row.SpoolSize == 0 && row.SpoolLocator == "" && row.ErrorCategory == "" &&
			row.ReadAt == nil && row.PackedAt == nil && row.FinishedAt == nil
	}
	if state == ItemRead {
		return lowerHex(row.SpoolDigest, 64) && row.SpoolSize > 0 && row.SpoolSize <= job.MaxCiphertextBytes &&
			validPersistedItemSpoolLocator(row.SpoolLocator) &&
			row.LogicalBytes == item.LogicalSize && row.ErrorCategory == "" && row.ReadAt != nil &&
			row.PackedAt == nil && row.FinishedAt == nil
	}
	if state == ItemPacked {
		if !validPersistedTerminalSpool(row, job) || row.LogicalBytes != item.LogicalSize ||
			row.ErrorCategory != "" || row.PackedAt == nil || row.FinishedAt == nil {
			return false
		}
		return item.EntryType != string(backupasset.CatalogEntryFile) ||
			(row.SpoolDigest != "" && row.SpoolSize > 0 && row.ReadAt != nil)
	}
	if state == ItemSkipped {
		return validPersistedTerminalSpool(row, job) && row.ErrorCategory != "" && row.FinishedAt != nil && row.PackedAt == nil
	}
	if state == ItemFailed {
		return item.EntryType == string(backupasset.CatalogEntryFile) &&
			row.SpoolDigest == "" && row.SpoolSize == 0 && row.SpoolLocator == "" && row.LogicalBytes == 0 &&
			validPreHeaderFailureCategory(row.ErrorCategory) && row.ReadAt == nil && row.PackedAt == nil && row.FinishedAt != nil
	}
	return false
}

func validPersistedTerminalSpool(row model.BackupAssetExportItemAttempt, job model.BackupAssetExportJob) bool {
	if row.SpoolDigest == "" || row.SpoolSize <= 0 {
		return row.SpoolDigest == "" && row.SpoolSize == 0 && row.SpoolLocator == ""
	}
	if !lowerHex(row.SpoolDigest, 64) || row.SpoolSize > job.MaxCiphertextBytes {
		return false
	}
	return row.SpoolLocator == "" || validPersistedItemSpoolLocator(row.SpoolLocator)
}

func validPersistedItemSpoolLocator(locator string) bool {
	return strings.HasSuffix(locator, ".xrs") && lowerHex(strings.TrimSuffix(locator, ".xrs"), 32)
}

func sameOptionalString(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func sameOptionalTime(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}

func cloneOptionalTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	cloned := value.UTC()
	return &cloned
}

func sameBytes(left, right []byte) bool {
	return (left == nil) == (right == nil) && bytes.Equal(left, right)
}

func validAttemptFenceDigest(fenceToken []byte, fenceDigest string) bool {
	if len(fenceToken) != 32 || !lowerHex(fenceDigest, 64) {
		return false
	}
	digest := sha256.Sum256(fenceToken)
	return hex.EncodeToString(digest[:]) == fenceDigest
}

func (loader *PersistentAttemptLoader) loadPersistedAttempt(
	ctx context.Context, request PersistentAttemptLoadRequest, now time.Time,
) (persistedAttemptLoad, error) {
	var persisted persistedAttemptLoad
	err := loader.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return loader.loadPersistedAttemptTx(ctx, tx, request, now, &persisted)
	})
	if ctxErr := ctx.Err(); ctxErr != nil {
		return persisted, ctxErr
	}
	return persisted, err
}

func (loader *PersistentAttemptLoader) loadPersistedAttemptTx(
	ctx context.Context,
	tx *gorm.DB,
	request PersistentAttemptLoadRequest,
	now time.Time,
	persisted *persistedAttemptLoad,
) error {
	if loader == nil || tx == nil || persisted == nil {
		return ErrUnavailable
	}
	storeBucketIDs := tx.Model(&model.BackupAssetExportReservation{}).Select("bucket_id").
		Where("job_id = ? AND kind = ?", request.JobID, "store")
	if err := tx.Model(&model.BackupAssetExportQuotaBucket{}).
		Select("id", "scope", "subject").Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id IN (?)", storeBucketIDs).Order("scope ASC, subject ASC, id ASC").
		Limit(persistentAttemptStoreCardinality + 1).
		Find(&persisted.storeBucketIdentities).Error; err != nil {
		return fmt.Errorf("lock export store quota buckets for worker: %w", err)
	}
	if len(persisted.storeBucketIdentities) != persistentAttemptStoreCardinality {
		return ErrUnavailable
	}
	result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND current_attempt_id = ? AND execution_state IN ?", request.JobID, request.AttemptID,
			[]string{string(ExecutionRunning), string(ExecutionSealing)}).Limit(1).Find(&persisted.job)
	if result.Error != nil {
		return fmt.Errorf("load export job for worker: %w", result.Error)
	}
	if result.RowsAffected != 1 || !now.Before(persisted.job.AbsoluteDeadline.UTC()) {
		return ErrAttemptFenceLost
	}
	if persisted.job.SelectionSchemaVersion != 1 || persisted.job.LimitsSchemaVersion != 1 ||
		!lowerHex(persisted.job.SelectionDigest, 64) || !ValidArchiveProfilePair(
		ArchiveFormat(persisted.job.ArchiveFormat), persisted.job.ArchiveProfile,
	) {
		return ErrUnavailable
	}
	if persisted.job.MaxItems <= 0 || persisted.job.MaxItems > maxSelectionItemsV1 ||
		persisted.job.ItemCount <= 0 || persisted.job.ItemCount > int64(persisted.job.MaxItems) {
		return ErrUnavailable
	}
	result = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND job_id = ? AND is_current = ? AND state IN ?", request.AttemptID, request.JobID, true,
			[]string{string(AttemptActive), string(AttemptSealing)}).Limit(1).Find(&persisted.attempt)
	if result.Error != nil {
		return fmt.Errorf("load export attempt for worker: %w", result.Error)
	}
	if result.RowsAffected != 1 || !now.Before(persisted.attempt.LeaseExpiresAt.UTC()) ||
		!equalFenceToken(persisted.attempt.FenceToken, request.FenceToken) ||
		!validAttemptFenceDigest(persisted.attempt.FenceToken, persisted.attempt.FenceDigest) {
		return ErrAttemptFenceLost
	}
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("job_id = ?", request.JobID).
		Order("ordinal ASC").Limit(persisted.job.MaxItems + 1).Find(&persisted.items).Error; err != nil {
		return fmt.Errorf("load export items for worker: %w", err)
	}
	if len(persisted.items) == 0 || int64(len(persisted.items)) != persisted.job.ItemCount {
		return ErrUnavailable
	}
	if err := validatePersistedSourceFencesForItemsTx(tx, persisted.job, persisted.items, now); err != nil {
		return err
	}
	result = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("job_id = ? AND state = ?", request.JobID, "active").Limit(1).Find(&persisted.key)
	if result.Error != nil {
		return fmt.Errorf("load active export job key for worker: %w", result.Error)
	}
	if result.RowsAffected != 1 || backupasset.ValidateOpaqueID(persisted.key.ID) != nil ||
		persisted.key.KeyRevision <= 0 || persisted.key.KEKVersion <= 0 ||
		persisted.key.WrapAlgorithm != JobKeyWrapAlgorithmV1 || len(persisted.key.EnvelopeNonce) != 12 || len(persisted.key.WrappedDEK) == 0 {
		return ErrUnavailable
	}
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("job_id = ? AND attempt_id = ?", request.JobID, request.AttemptID).
		Order("item_id ASC, id ASC").Limit(persisted.job.MaxItems + 1).Find(&persisted.itemAttempts).Error; err != nil {
		return fmt.Errorf("load export item attempts for worker: %w", err)
	}
	if len(persisted.itemAttempts) != len(persisted.items) {
		return ErrUnavailable
	}
	for _, row := range persisted.items {
		if row.CurrentAttemptID == nil || *row.CurrentAttemptID != request.AttemptID || row.JobID != request.JobID {
			return ErrAttemptFenceLost
		}
	}
	for _, row := range persisted.itemAttempts {
		if row.JobID != request.JobID || row.AttemptID != request.AttemptID || backupasset.ValidateOpaqueID(row.ID) != nil {
			return ErrAttemptFenceLost
		}
	}
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("job_id = ? AND kind = ?", request.JobID, "store").
		Order("bucket_id ASC, id ASC").Limit(persistentAttemptStoreCardinality + 1).
		Find(&persisted.storeReservations).Error; err != nil {
		return fmt.Errorf("lock export store reservations for worker: %w", err)
	}
	if len(persisted.storeReservations) != persistentAttemptStoreCardinality {
		return ErrUnavailable
	}
	return nil
}

func frozenItemFromModel(schemaVersion int, row model.BackupAssetExportItem, components []string) FrozenItem {
	return FrozenItem{
		SchemaVersion: schemaVersion, Ref: backupasset.AssetRef{RecoveryPointID: row.RecoveryPointID, EntryID: row.EntryID},
		CatalogGenerationID: row.CatalogGenerationID, SourceFingerprint: row.SourceFingerprint,
		EntryFingerprint: row.EntryFingerprint, FingerprintStrength: row.FingerprintStrength,
		ProviderCapabilityRevision: row.ProviderCapabilityRevision, EntryType: backupasset.CatalogEntryType(row.EntryType),
		LogicalSize: row.LogicalSize, MediaType: row.MediaType, RetentionUntil: row.RetentionUntil,
		SelectionRootOrdinal: row.SelectionRootOrdinal, ArchiveComponents: append([]string(nil), components...),
	}
}

type PersistentWorkerDependencies struct {
	DB             *gorm.DB
	Keys           ExportKeyVersionSource
	Broker         AttemptSourceBroker
	Metadata       MetadataValidator
	Store          *Store
	Lifecycle      *Lifecycle
	SourceLeases   SourceLeaseCoordinator
	WorkerCapacity *WorkerCapacityLimits
	AttemptWork    *AttemptWorkRegistry
	Now            func() time.Time
}

type PersistentWorker struct {
	db             *gorm.DB
	loader         *PersistentAttemptLoader
	keys           ExportKeyVersionSource
	broker         AttemptSourceBroker
	metadata       MetadataValidator
	store          *Store
	lifecycle      *Lifecycle
	sourceLeases   SourceLeaseCoordinator
	workerCapacity *WorkerCapacityLimits
	attemptWork    *AttemptWorkRegistry
	now            func() time.Time
}

// AttemptWorkRegistry tracks source-backed work so terminal lifecycle cleanup
// can cancel and join it before returning worker capacity to the quota buckets.
type AttemptWorkRegistry struct {
	mu   sync.Mutex
	jobs map[string]*attemptWorkJob
}

type attemptWorkJob struct {
	draining bool
	active   map[uint64]context.CancelFunc
	done     chan struct{}
	nextID   uint64
}

type AttemptWorkHandle struct {
	registry *AttemptWorkRegistry
	jobID    string
	id       uint64
	ctx      context.Context
	once     sync.Once
}

func NewAttemptWorkRegistry() *AttemptWorkRegistry {
	return &AttemptWorkRegistry{jobs: make(map[string]*attemptWorkJob)}
}

func (registry *AttemptWorkRegistry) Start(ctx context.Context, jobID string) (*AttemptWorkHandle, error) {
	if registry == nil || backupasset.ValidateOpaqueID(jobID) != nil {
		return nil, ErrAttemptFenceLost
	}
	ctx = nonNilServiceContext(ctx)
	registry.mu.Lock()
	defer registry.mu.Unlock()
	job := registry.jobs[jobID]
	if job == nil {
		job = &attemptWorkJob{active: make(map[uint64]context.CancelFunc), done: make(chan struct{})}
		registry.jobs[jobID] = job
	}
	if job.draining {
		return nil, ErrAttemptFenceLost
	}
	job.nextID++
	workCtx, cancel := context.WithCancel(ctx)
	job.active[job.nextID] = cancel
	return &AttemptWorkHandle{registry: registry, jobID: jobID, id: job.nextID, ctx: workCtx}, nil
}

func (handle *AttemptWorkHandle) Context() context.Context {
	if handle == nil || handle.ctx == nil {
		return context.Background()
	}
	return handle.ctx
}

func (handle *AttemptWorkHandle) Finish() {
	if handle == nil || handle.registry == nil {
		return
	}
	handle.once.Do(func() {
		handle.registry.mu.Lock()
		defer handle.registry.mu.Unlock()
		job := handle.registry.jobs[handle.jobID]
		if job == nil {
			return
		}
		if cancel, found := job.active[handle.id]; found {
			cancel()
			delete(job.active, handle.id)
		}
		if job.draining && len(job.active) == 0 {
			select {
			case <-job.done:
			default:
				close(job.done)
			}
		}
	})
}

func (registry *AttemptWorkRegistry) Drain(ctx context.Context, jobID string) error {
	if registry == nil || backupasset.ValidateOpaqueID(jobID) != nil {
		return ErrAttemptFenceLost
	}
	registry.mu.Lock()
	job := registry.jobs[jobID]
	if job == nil {
		job = &attemptWorkJob{active: make(map[uint64]context.CancelFunc), done: make(chan struct{}), draining: true}
		close(job.done)
		registry.jobs[jobID] = job
		registry.mu.Unlock()
		return nil
	}
	job.draining = true
	for _, cancel := range job.active {
		cancel()
	}
	done := job.done
	if len(job.active) == 0 {
		select {
		case <-done:
		default:
			close(done)
		}
	}
	registry.mu.Unlock()
	select {
	case <-done:
		return nil
	case <-nonNilServiceContext(ctx).Done():
		return nonNilServiceContext(ctx).Err()
	}
}

type PersistentSpoolItemRequest struct {
	JobID      string
	AttemptID  string
	FenceToken []byte
	ItemID     string
}

type PersistentSpoolResult struct {
	ItemID           string
	ItemAttemptID    string
	Locator          string
	NoncePrefix      []byte
	LogicalBytes     int64
	ProviderBytes    int64
	CiphertextBytes  int64
	PlaintextDigest  string
	CiphertextDigest string
}

type PersistentDiscardAttemptRequest struct {
	JobID     string
	AttemptID string
}

type PersistentSealRequest struct {
	JobID      string
	AttemptID  string
	FenceToken []byte
}

type PersistentSealResult struct {
	ArtifactID       string
	Locator          string
	Report           ArchiveReport
	PlaintextBytes   int64
	CiphertextBytes  int64
	PlaintextDigest  string
	CiphertextDigest string
}

type PersistentPublishRequest struct {
	JobID      string
	AttemptID  string
	FenceToken []byte
	ArtifactID string
}

type PersistentPublishResult struct {
	ArtifactID string
	ExpiresAt  time.Time
}

type PersistentReconcileAction string

const (
	PersistentReconcilePublished PersistentReconcileAction = "published"
	PersistentReconcileRevoked   PersistentReconcileAction = "revoked"
)

type PersistentReconcileRequest struct {
	JobID string
}

type PersistentReconcileResult struct {
	Action         PersistentReconcileAction
	ArtifactID     string
	ExpiresAt      time.Time
	ReadyIntegrity *ReadyIntegrityToken
}

// ReadyIntegrityToken is an in-memory proof that a ready artifact, its key,
// attempt and source lease tuple was verified as one stable snapshot. The
// fields are intentionally private: only the export worker can mint or
// inspect the proof, while runtime may pass it back to the lease coordinator.
type ReadyIntegrityToken struct {
	state *readyIntegrityTokenState
}

type readyIntegrityTokenState struct {
	mu             sync.Mutex
	consumed       bool
	jobID          string
	readyExpiry    time.Time
	digest         [32]byte
	verifyArtifact readyArtifactVerifier
}

type readyArtifactVerifier func(context.Context) (func() error, error)

func NewPersistentWorker(dependencies PersistentWorkerDependencies) (*PersistentWorker, error) {
	if dependencies.DB == nil || dependencies.Keys == nil || dependencies.Broker == nil || dependencies.Metadata == nil ||
		dependencies.Store == nil || dependencies.Store.closed() || dependencies.AttemptWork == nil {
		return nil, ErrUnavailable
	}
	if dependencies.WorkerCapacity != nil && !validWorkerCapacityLimits(*dependencies.WorkerCapacity) {
		return nil, ErrUnavailable
	}
	if dependencies.Lifecycle != nil {
		if port, ok := dependencies.Lifecycle.port.(*PersistentLifecyclePort); ok &&
			(port == nil || port.attemptWork != dependencies.AttemptWork) {
			return nil, ErrUnavailable
		}
	}
	if dependencies.Lifecycle != nil && dependencies.Lifecycle.port == nil {
		return nil, ErrUnavailable
	}
	if dependencies.Now == nil {
		dependencies.Now = func() time.Time { return time.Now().UTC() }
	}
	loader, err := NewPersistentAttemptLoader(dependencies.DB, dependencies.Keys, dependencies.Now)
	if err != nil {
		return nil, err
	}
	return &PersistentWorker{
		db: dependencies.DB, loader: loader, keys: dependencies.Keys, broker: dependencies.Broker, metadata: dependencies.Metadata,
		store: dependencies.Store, lifecycle: dependencies.Lifecycle, sourceLeases: dependencies.SourceLeases,
		workerCapacity: dependencies.WorkerCapacity,
		attemptWork:    dependencies.AttemptWork,
		now:            dependencies.Now,
	}, nil
}

func (worker *PersistentWorker) DiscardAttempt(
	ctx context.Context,
	request PersistentDiscardAttemptRequest,
) error {
	if worker == nil || worker.store == nil || worker.store.closed() ||
		backupasset.ValidateOpaqueID(request.JobID) != nil || backupasset.ValidateOpaqueID(request.AttemptID) != nil {
		return ErrUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	closedAttemptStates := []string{string(AttemptFailed), string(AttemptCanceled), string(AttemptSuperseded)}
	var attempt model.BackupAssetExportAttempt
	result := worker.db.WithContext(ctx).
		Where("id = ? AND job_id = ? AND is_current = ? AND state IN ?", request.AttemptID, request.JobID, false,
			closedAttemptStates).
		Limit(1).Find(&attempt)
	if result.Error != nil {
		return fmt.Errorf("load closed Export attempt for discard: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return ErrAttemptFenceLost
	}
	var discoveredJob struct {
		OwnerUserID uint `gorm:"column:owner_user_id"`
	}
	result = worker.db.WithContext(ctx).Model(&model.BackupAssetExportJob{}).
		Select("owner_user_id").Where("id = ?", request.JobID).Limit(1).Find(&discoveredJob)
	if result.Error != nil {
		return fmt.Errorf("load export job owner for discard: %w", result.Error)
	}
	if result.RowsAffected != 1 || discoveredJob.OwnerUserID == 0 {
		return ErrAttemptFenceLost
	}
	var itemAttempts []model.BackupAssetExportItemAttempt
	if err := worker.db.WithContext(ctx).Where("job_id = ? AND attempt_id = ?", request.JobID, request.AttemptID).
		Order("id ASC").Find(&itemAttempts).Error; err != nil {
		return fmt.Errorf("load Export item spools for discard: %w", err)
	}
	var artifacts []model.BackupAssetExportArtifact
	if err := worker.db.WithContext(ctx).
		Where("job_id = ? AND attempt_id = ? AND state IN ? AND expires_at IS NULL",
			request.JobID, request.AttemptID, []string{"staged", "sealed"}).
		Order("id ASC").Find(&artifacts).Error; err != nil {
		return fmt.Errorf("load unpublished Export artifacts for discard: %w", err)
	}
	locators := make([]string, 0, len(itemAttempts)+len(artifacts)+1)
	for _, itemAttempt := range itemAttempts {
		if itemAttempt.SpoolLocator == "" {
			continue
		}
		if !validStoreLocator(itemAttempt.SpoolLocator) {
			return ErrInvalidStore
		}
		locators = append(locators, itemAttempt.SpoolLocator)
	}
	if attempt.StagingLocator != "" {
		if !validStoreLocator(attempt.StagingLocator) {
			return ErrInvalidStore
		}
		locators = append(locators, attempt.StagingLocator)
	}
	for _, artifact := range artifacts {
		if !validStoreLocator(artifact.Locator) {
			return ErrInvalidStore
		}
		locators = append(locators, artifact.Locator)
	}
	if err := worker.store.PurgeBatch(locators); err != nil {
		return err
	}
	now := worker.now().UTC()
	return worker.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		buckets, err := ensureAndLockQuotaBucketPairTx(tx, discoveredJob.OwnerUserID, now)
		if err != nil {
			return err
		}
		var job model.BackupAssetExportJob
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND owner_user_id = ?", request.JobID, discoveredJob.OwnerUserID).Limit(1).Find(&job)
		if result.Error != nil {
			return fmt.Errorf("lock export job for discard: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return ErrAttemptFenceLost
		}
		var lockedAttempt model.BackupAssetExportAttempt
		result = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND job_id = ? AND is_current = ? AND state IN ?", request.AttemptID, request.JobID, false,
				closedAttemptStates).
			Limit(1).Find(&lockedAttempt)
		if result.Error != nil {
			return fmt.Errorf("lock closed Export attempt for discard: %w", result.Error)
		}
		if result.RowsAffected != 1 || lockedAttempt.State != attempt.State ||
			lockedAttempt.StagingLocator != attempt.StagingLocator {
			return ErrAttemptFenceLost
		}
		lockedArtifacts := make([]model.BackupAssetExportArtifact, 0, len(artifacts))
		for _, artifact := range artifacts {
			var lockedArtifact model.BackupAssetExportArtifact
			result = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("id = ? AND job_id = ? AND attempt_id = ? AND state = ? AND locator = ? AND expires_at IS NULL",
					artifact.ID, request.JobID, request.AttemptID, artifact.State, artifact.Locator).
				Limit(1).Find(&lockedArtifact)
			if result.Error != nil {
				return fmt.Errorf("lock unpublished Export artifact for discard: %w", result.Error)
			}
			if result.RowsAffected != 1 || lockedArtifact.JobID != job.ID || lockedArtifact.AttemptID != lockedAttempt.ID ||
				lockedArtifact.State != artifact.State || lockedArtifact.Locator != artifact.Locator || lockedArtifact.ExpiresAt != nil {
				return ErrAttemptFenceLost
			}
			switch lockedArtifact.State {
			case "staged":
			case "sealed":
				if lockedArtifact.SealedAt == nil || lockedArtifact.CiphertextSize <= 0 {
					return ErrAttemptFenceLost
				}
			default:
				return ErrAttemptFenceLost
			}
			lockedArtifacts = append(lockedArtifacts, lockedArtifact)
		}
		for _, artifact := range lockedArtifacts {
			if artifact.State != "sealed" {
				continue
			}
			if err := debitDiscardedSealedStoreBytesTx(tx, buckets, job, artifact.CiphertextSize, now); err != nil {
				return err
			}
		}
		for _, itemAttempt := range itemAttempts {
			if itemAttempt.SpoolLocator == "" {
				continue
			}
			result := tx.Model(&model.BackupAssetExportItemAttempt{}).
				Where("id = ? AND job_id = ? AND attempt_id = ? AND spool_locator = ?", itemAttempt.ID, request.JobID,
					request.AttemptID, itemAttempt.SpoolLocator).
				Update("spool_locator", "")
			if result.Error != nil {
				return fmt.Errorf("clear Export item spool for discard: %w", result.Error)
			}
			if result.RowsAffected != 1 {
				return ErrAttemptFenceLost
			}
		}
		for _, artifact := range lockedArtifacts {
			result := tx.Where("id = ? AND job_id = ? AND attempt_id = ? AND state = ? AND locator = ? AND expires_at IS NULL",
				artifact.ID, request.JobID, request.AttemptID, artifact.State, artifact.Locator).
				Delete(&model.BackupAssetExportArtifact{})
			if result.Error != nil {
				return fmt.Errorf("delete unpublished Export artifact reference: %w", result.Error)
			}
			if result.RowsAffected != 1 {
				return ErrAttemptFenceLost
			}
		}
		if attempt.StagingLocator != "" {
			result := tx.Model(&model.BackupAssetExportAttempt{}).
				Where("id = ? AND job_id = ? AND is_current = ? AND staging_locator = ?", request.AttemptID, request.JobID,
					false, attempt.StagingLocator).
				Updates(map[string]any{"staging_locator": "", "updated_at": now})
			if result.Error != nil {
				return fmt.Errorf("clear Export attempt staging locator for discard: %w", result.Error)
			}
			if result.RowsAffected != 1 {
				return ErrAttemptFenceLost
			}
		}
		return nil
	})
}

func (worker *PersistentWorker) SpoolItem(
	ctx context.Context, request PersistentSpoolItemRequest,
) (result PersistentSpoolResult, resultErr error) {
	if worker == nil || worker.attemptWork == nil || backupasset.ValidateOpaqueID(request.JobID) != nil ||
		backupasset.ValidateOpaqueID(request.AttemptID) != nil || backupasset.ValidateOpaqueID(request.ItemID) != nil ||
		len(request.FenceToken) != 32 {
		return PersistentSpoolResult{}, ErrAttemptFenceLost
	}
	work, err := worker.attemptWork.Start(ctx, request.JobID)
	if err != nil {
		return PersistentSpoolResult{}, err
	}
	defer work.Finish()
	ctx = work.Context()
	loadRequest := PersistentAttemptLoadRequest{
		JobID: request.JobID, AttemptID: request.AttemptID, FenceToken: request.FenceToken,
	}
	snapshot, err := worker.loader.Load(ctx, loadRequest)
	if err != nil {
		return PersistentSpoolResult{}, err
	}
	defer snapshot.ClearKeyMaterial()
	item, found := persistentItemByID(snapshot.Items, request.ItemID)
	if !found || item.Frozen.EntryType != backupasset.CatalogEntryFile || item.Frozen.LogicalSize < 0 ||
		snapshot.ChunkBytes <= 0 || snapshot.ChunkBytes > 8*1024*1024 || snapshot.ChunkBytes > int64(^uint(0)>>1) {
		return PersistentSpoolResult{}, ErrArchiveSource
	}
	deadline := snapshot.AbsoluteDeadline.UTC()
	if snapshot.AttemptLeaseExpires.Before(deadline) {
		deadline = snapshot.AttemptLeaseExpires.UTC()
	}
	if !worker.now().UTC().Before(deadline) {
		return PersistentSpoolResult{}, ErrAttemptFenceLost
	}
	spoolPersisted := false
	defer func() {
		if !spoolPersisted && recoverablePreHeaderSpoolError(resultErr) {
			resultErr = newPreHeaderSpoolFailureWithUnknownProviderBytes(resultErr)
		}
	}()
	staging, err := worker.store.CreateItemSpool(snapshot.JobID, snapshot.AttemptID, item.ItemAttemptID)
	if err != nil {
		return PersistentSpoolResult{}, err
	}
	stagingOwned := true
	defer func() {
		if stagingOwned {
			_ = worker.store.DiscardStaging(staging)
		}
	}()

	zeroByte := item.Frozen.LogicalSize == 0
	allowedModes := []content.SourceMode{content.SourceModeStat, content.SourceModeSequential}
	readLimit := item.Frozen.LogicalSize
	maxRequests := int64(3)
	if zeroByte {
		allowedModes = []content.SourceMode{content.SourceModeStat}
		readLimit = 1
		maxRequests = 2
	}
	binding := content.AttemptSourceBinding{
		SessionID: item.ItemAttemptID, Ref: item.Frozen.Ref, CatalogGenerationID: item.Frozen.CatalogGenerationID,
		SourceFingerprint: item.Frozen.SourceFingerprint, EntryFingerprint: item.Frozen.EntryFingerprint,
		AllowedModes: allowedModes,
		Limits: content.AttemptReadLimits{
			MaxBytesPerRequest: readLimit, MaxCumulativeBytes: readLimit,
			MaxRequests: maxRequests, MaxInFlight: 1,
		},
		AbsoluteExpiresAt: deadline,
	}
	session, info, err := worker.broker.OpenSession(ctx, binding)
	if err != nil {
		return PersistentSpoolResult{}, err
	}
	sessionOwned := true
	defer func() {
		if sessionOwned {
			_ = session.Close()
		}
	}()
	if info.Size != item.Frozen.LogicalSize || info.MediaType != item.Frozen.MediaType || !zeroByte && !info.Sequential ||
		(item.Frozen.FingerprintStrength == "strong" && !info.FingerprintStrong) {
		return PersistentSpoolResult{}, content.ErrAttemptSourceChanged
	}
	cipherBinding := CipherBinding{
		ExportID: snapshot.JobID, SelectionDigest: snapshot.SelectionDigest,
		ArchiveProfile: snapshot.ArchiveProfile, FormatVersion: 1,
		AttemptFenceDigest: snapshot.AttemptFenceDigest, Purpose: CipherPurposeItemSpool, ObjectID: item.ItemAttemptID,
	}
	var cipherResult CipherResult
	if zeroByte {
		revalidateErr := session.Revalidate(ctx)
		sessionCloseErr := session.Close()
		sessionOwned = false
		if revalidateErr != nil || sessionCloseErr != nil {
			return PersistentSpoolResult{}, errors.Join(revalidateErr, sessionCloseErr)
		}
		cipherResult, err = EncryptStream(
			ctx, staging.File, bytes.NewReader(nil), snapshot.DEK, cipherBinding, int(snapshot.ChunkBytes),
		)
		if err != nil {
			return PersistentSpoolResult{}, err
		}
	} else {
		reader, openErr := session.OpenSequential(ctx, item.Frozen.LogicalSize)
		if openErr != nil {
			return PersistentSpoolResult{}, openErr
		}
		var encryptErr error
		cipherResult, encryptErr = EncryptStream(
			ctx, staging.File, reader, snapshot.DEK, cipherBinding, int(snapshot.ChunkBytes),
		)
		readCloseErr := reader.Close()
		revalidateErr := session.Revalidate(ctx)
		sessionCloseErr := session.Close()
		sessionOwned = false
		if encryptErr != nil || readCloseErr != nil || revalidateErr != nil || sessionCloseErr != nil {
			return PersistentSpoolResult{}, errors.Join(encryptErr, readCloseErr, revalidateErr, sessionCloseErr)
		}
	}
	if err := worker.metadata.RevalidateMetadata(ctx, item.Frozen); err != nil {
		return PersistentSpoolResult{}, err
	}
	reloaded, err := worker.loader.Load(ctx, loadRequest)
	if err != nil {
		return PersistentSpoolResult{}, err
	}
	reloaded.ClearKeyMaterial()
	reloadedItem, found := persistentItemByID(reloaded.Items, item.ItemID)
	if !found || reloadedItem.ItemAttemptID != item.ItemAttemptID {
		return PersistentSpoolResult{}, ErrAttemptFenceLost
	}
	locator, releasePublication, err := worker.store.sealWithPublicationPin(staging)
	if err != nil {
		return PersistentSpoolResult{}, err
	}
	stagingOwned = false
	publicationPersisted := false
	defer func() {
		releasePublication()
		if !publicationPersisted {
			_ = worker.store.Purge(locator)
		}
	}()
	if err := worker.persistReadSpool(ctx, request, item, locator, cipherResult); err != nil {
		return PersistentSpoolResult{}, err
	}
	publicationPersisted = true
	spoolPersisted = true
	var persisted model.BackupAssetExportItemAttempt
	if err := worker.db.WithContext(ctx).Where("id = ?", item.ItemAttemptID).Take(&persisted).Error; err != nil {
		return PersistentSpoolResult{}, ErrUnavailable
	}
	return PersistentSpoolResult{
		ItemID: item.ItemID, ItemAttemptID: item.ItemAttemptID, Locator: locator,
		NoncePrefix: append([]byte(nil), cipherResult.NoncePrefix...), LogicalBytes: cipherResult.PlaintextBytes,
		ProviderBytes: persisted.ProviderBytes, CiphertextBytes: cipherResult.CiphertextBytes,
		PlaintextDigest: cipherResult.PlaintextDigest, CiphertextDigest: cipherResult.CiphertextDigest,
	}, nil
}

func recoverablePreHeaderSpoolError(err error) bool {
	return err != nil &&
		!errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) &&
		!errors.Is(err, ErrAttemptFenceLost) && !errors.Is(err, backupasset.ErrLeaseFenceLost) &&
		!errors.Is(err, ErrExecutionDeadlineReached) && !errors.Is(err, backupasset.ErrLeaseDeadlineExceeded) &&
		!errors.Is(err, ErrSourceDeadlineReached) && !errors.Is(err, ErrArchiveLimit) &&
		!errors.Is(err, ErrQuotaExceeded) && !errors.Is(err, content.ErrAttemptBudgetExceeded)
}

func (worker *PersistentWorker) persistReadSpool(
	ctx context.Context, request PersistentSpoolItemRequest, item PersistentAttemptItem,
	locator string, cipherResult CipherResult,
) error {
	now := worker.now().UTC()
	return worker.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var job model.BackupAssetExportJob
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND current_attempt_id = ? AND execution_state = ?", request.JobID, request.AttemptID, ExecutionRunning).
			Limit(1).Find(&job)
		if result.Error != nil {
			return fmt.Errorf("load export job for spool persistence: %w", result.Error)
		}
		if result.RowsAffected != 1 || !now.Before(job.AbsoluteDeadline.UTC()) ||
			cipherResult.PlaintextBytes != item.Frozen.LogicalSize || cipherResult.CiphertextBytes > job.MaxCiphertextBytes {
			return ErrAttemptFenceLost
		}
		var attempt model.BackupAssetExportAttempt
		result = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND job_id = ? AND is_current = ? AND state = ?", request.AttemptID, request.JobID, true, AttemptActive).
			Limit(1).Find(&attempt)
		if result.Error != nil {
			return fmt.Errorf("load export attempt for spool persistence: %w", result.Error)
		}
		if result.RowsAffected != 1 || !now.Before(attempt.LeaseExpiresAt.UTC()) ||
			!equalFenceToken(attempt.FenceToken, request.FenceToken) {
			return ErrAttemptFenceLost
		}
		if err := validatePersistedSourceFencesTx(tx, job, now); err != nil {
			return err
		}
		var itemRow model.BackupAssetExportItem
		result = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND job_id = ? AND current_attempt_id = ? AND state = ?", item.ItemID, job.ID, attempt.ID, ItemPending).
			Limit(1).Find(&itemRow)
		if result.Error != nil {
			return fmt.Errorf("load export item for spool persistence: %w", result.Error)
		}
		if result.RowsAffected != 1 || itemRow.RecoveryPointID != item.Frozen.Ref.RecoveryPointID ||
			itemRow.EntryID != item.Frozen.Ref.EntryID {
			return ErrAttemptFenceLost
		}
		var itemAttempt model.BackupAssetExportItemAttempt
		result = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND job_id = ? AND item_id = ? AND attempt_id = ? AND state = ? AND spool_locator = ?",
				item.ItemAttemptID, job.ID, item.ItemID, attempt.ID, ItemPending, "").Limit(1).Find(&itemAttempt)
		if result.Error != nil {
			return fmt.Errorf("load export item attempt for spool persistence: %w", result.Error)
		}
		if result.RowsAffected != 1 || itemAttempt.ProviderBytes < 0 || itemAttempt.ProviderBytes > job.MaxProviderBytes {
			return ErrAttemptFenceLost
		}
		readAt := now
		result = tx.Model(&model.BackupAssetExportItemAttempt{}).
			Where("id = ? AND state = ? AND spool_locator = ?", itemAttempt.ID, ItemPending, "").
			Updates(map[string]any{
				"state": string(ItemRead), "spool_digest": cipherResult.CiphertextDigest,
				"spool_size": cipherResult.CiphertextBytes, "spool_locator": locator,
				"logical_bytes": cipherResult.PlaintextBytes, "read_at": readAt,
			})
		if result.Error != nil {
			return fmt.Errorf("persist export item spool: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return ErrAttemptFenceLost
		}
		result = tx.Model(&model.BackupAssetExportItem{}).
			Where("id = ? AND current_attempt_id = ? AND state = ?", itemRow.ID, attempt.ID, ItemPending).
			Updates(map[string]any{
				"state": string(ItemRead), "logical_bytes": cipherResult.PlaintextBytes,
				"provider_bytes": itemAttempt.ProviderBytes, "updated_at": now,
			})
		if result.Error != nil {
			return fmt.Errorf("persist export item spool projection: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return ErrAttemptFenceLost
		}
		return nil
	})
}

func (worker *PersistentWorker) SealArchive(
	ctx context.Context, request PersistentSealRequest,
) (PersistentSealResult, error) {
	if worker == nil || worker.attemptWork == nil || backupasset.ValidateOpaqueID(request.JobID) != nil ||
		backupasset.ValidateOpaqueID(request.AttemptID) != nil || len(request.FenceToken) != 32 {
		return PersistentSealResult{}, ErrAttemptFenceLost
	}
	work, err := worker.attemptWork.Start(ctx, request.JobID)
	if err != nil {
		return PersistentSealResult{}, err
	}
	defer work.Finish()
	ctx = work.Context()
	loadRequest := PersistentAttemptLoadRequest(request)
	snapshot, err := worker.loader.Load(ctx, loadRequest)
	if err != nil {
		return PersistentSealResult{}, err
	}
	defer snapshot.ClearKeyMaterial()
	if err := worker.validateFinalArchiveNonceAvailable(ctx, request, snapshot); err != nil {
		return PersistentSealResult{}, err
	}
	entries := make([]ArchiveEntry, 0, len(snapshot.Items))
	for _, item := range snapshot.Items {
		entry := ArchiveEntry{
			ItemID: item.ItemID, Components: append([]string(nil), item.Frozen.ArchiveComponents...),
			RecoveryPointID: item.Frozen.Ref.RecoveryPointID, EntryID: item.Frozen.Ref.EntryID,
			SelectionRootOrdinal: item.Frozen.SelectionRootOrdinal,
			Type:                 item.Frozen.EntryType, Size: item.Frozen.LogicalSize,
		}
		if item.Frozen.EntryType == backupasset.CatalogEntryFile {
			switch item.State {
			case ItemRead:
				if item.SpoolLocator == "" || item.SpoolSize <= 0 || item.LogicalBytes != item.Frozen.LogicalSize || item.ProviderBytes < 0 {
					return PersistentSealResult{}, ErrArchiveSource
				}
				if err := worker.authenticateSpool(ctx, snapshot, item); err != nil {
					if recoverablePreHeaderAuthenticatedSpoolError(err) {
						if purgeErr := worker.store.Purge(item.SpoolLocator); purgeErr != nil {
							return PersistentSealResult{}, errors.Join(err, purgeErr)
						}
						return PersistentSealResult{}, newPreHeaderSpoolFailureAfterAuthentication(
							err, item.ItemID, item.ProviderBytes,
						)
					}
					return PersistentSealResult{}, err
				}
				spooled := item
				entry.ProviderBytes = item.ProviderBytes
				entry.ProviderEvidence = true
				entry.Open = func(openCtx context.Context) (io.ReadCloser, error) {
					reader, openErr := worker.openDecryptedSpool(openCtx, snapshot, spooled)
					if openErr == nil || !recoverablePreHeaderAuthenticatedSpoolError(openErr) {
						return reader, openErr
					}
					if purgeErr := worker.store.Purge(spooled.SpoolLocator); purgeErr != nil {
						return nil, errors.Join(openErr, purgeErr)
					}
					return nil, newPreHeaderSpoolFailureAfterAuthentication(
						openErr, spooled.ItemID, spooled.ProviderBytes,
					)
				}
			case ItemFailed:
				if item.SpoolLocator != "" || item.SpoolSize != 0 || item.LogicalBytes != 0 || item.ProviderBytes < 0 || item.ErrorCategory == "" {
					return PersistentSealResult{}, ErrArchiveSource
				}
				entry.ProviderBytes = item.ProviderBytes
				entry.PreHeaderFailure = item.ErrorCategory
			default:
				return PersistentSealResult{}, ErrArchiveSource
			}
		} else {
			if item.State != ItemPending {
				return PersistentSealResult{}, ErrAttemptFenceLost
			}
			if err := worker.metadata.RevalidateMetadata(ctx, item.Frozen); err != nil {
				return PersistentSealResult{}, err
			}
		}
		entries = append(entries, entry)
	}

	staging, err := worker.store.CreateStaging(snapshot.JobID, snapshot.AttemptID)
	if err != nil {
		return PersistentSealResult{}, err
	}
	stagingOwned := true
	defer func() {
		if stagingOwned {
			_ = worker.store.DiscardStaging(staging)
		}
	}()
	if err := worker.claimFinalArchiveNonce(ctx, request, snapshot, staging.Locator()); err != nil {
		return PersistentSealResult{}, err
	}

	pipeReader, pipeWriter := io.Pipe()
	type archiveResult struct {
		report ArchiveReport
		err    error
	}
	archiveDone := make(chan archiveResult, 1)
	go func() {
		report, writeErr := WriteArchive(ctx, pipeWriter, snapshot.ArchiveFormat, snapshot.ArchiveProfile, snapshot.SelectionDigest, entries, ArchiveLimits{
			MaxItems: snapshot.MaxItems, MaxLogicalBytes: snapshot.MaxLogicalBytes,
			MaxProviderBytes: snapshot.MaxProviderBytes,
		})
		if writeErr != nil {
			_ = pipeWriter.CloseWithError(writeErr)
		} else {
			writeErr = pipeWriter.Close()
		}
		archiveDone <- archiveResult{report: report, err: writeErr}
	}()
	limited := &ciphertextLimitWriter{writer: staging.File, remaining: snapshot.MaxCiphertextBytes}
	cipherResult, cipherErr := EncryptStreamWithNonce(
		ctx, limited, pipeReader, snapshot.DEK, CipherBinding{
			ExportID: snapshot.JobID, SelectionDigest: snapshot.SelectionDigest,
			ArchiveProfile: snapshot.ArchiveProfile, FormatVersion: 1,
			AttemptFenceDigest: snapshot.AttemptFenceDigest, Purpose: CipherPurposeFinalArchive,
		}, int(snapshot.ChunkBytes), snapshot.AttemptNoncePrefix,
	)
	if cipherErr != nil {
		_ = pipeReader.CloseWithError(cipherErr)
	} else {
		_ = pipeReader.Close()
	}
	archive := <-archiveDone
	if cipherErr != nil || archive.err != nil {
		return PersistentSealResult{}, errors.Join(cipherErr, archive.err)
	}
	if cipherResult.CiphertextBytes > snapshot.MaxCiphertextBytes || archive.report.Packed == 0 {
		return PersistentSealResult{}, ErrArchiveLimit
	}
	locator, releasePublication, err := worker.store.sealWithPublicationPin(staging)
	if err != nil {
		return PersistentSealResult{}, err
	}
	stagingOwned = false
	publicationPersisted := false
	defer func() {
		releasePublication()
		if !publicationPersisted {
			_ = worker.store.Purge(locator)
		}
	}()

	artifactID, err := backupasset.NewOpaqueID()
	if err != nil {
		return PersistentSealResult{}, err
	}
	if err := worker.persistSealedArchive(ctx, request, snapshot, artifactID, locator, cipherResult, archive.report); err != nil {
		return PersistentSealResult{}, err
	}
	publicationPersisted = true
	for _, item := range snapshot.Items {
		if item.SpoolLocator == "" {
			continue
		}
		if err := worker.store.Purge(item.SpoolLocator); err != nil {
			return PersistentSealResult{
				ArtifactID: artifactID, Locator: locator, Report: archive.report,
				PlaintextBytes: cipherResult.PlaintextBytes, CiphertextBytes: cipherResult.CiphertextBytes,
				PlaintextDigest: cipherResult.PlaintextDigest, CiphertextDigest: cipherResult.CiphertextDigest,
			}, err
		}
	}
	return PersistentSealResult{
		ArtifactID: artifactID, Locator: locator, Report: archive.report,
		PlaintextBytes: cipherResult.PlaintextBytes, CiphertextBytes: cipherResult.CiphertextBytes,
		PlaintextDigest: cipherResult.PlaintextDigest, CiphertextDigest: cipherResult.CiphertextDigest,
	}, nil
}

func (worker *PersistentWorker) validateFinalArchiveNonceAvailable(
	ctx context.Context,
	request PersistentSealRequest,
	snapshot PersistentAttemptSnapshot,
) error {
	var attempt model.BackupAssetExportAttempt
	result := worker.db.WithContext(ctx).Model(&model.BackupAssetExportAttempt{}).
		Joins("JOIN backup_asset_export_jobs AS job ON job.id = backup_asset_export_attempts.job_id").
		Where("backup_asset_export_attempts.id = ? AND backup_asset_export_attempts.job_id = ?", request.AttemptID, request.JobID).
		Where("backup_asset_export_attempts.is_current = ? AND backup_asset_export_attempts.state = ?", true, AttemptActive).
		Where("backup_asset_export_attempts.staging_locator = ?", "").
		Where("job.current_attempt_id = ? AND job.execution_state = ?", request.AttemptID, ExecutionRunning).
		Limit(1).Find(&attempt)
	if result.Error != nil {
		return fmt.Errorf("load final archive nonce claim: %w", result.Error)
	}
	if result.RowsAffected != 1 || !equalFenceToken(attempt.FenceToken, request.FenceToken) ||
		!validAttemptFenceDigest(attempt.FenceToken, attempt.FenceDigest) ||
		attempt.FenceDigest != snapshot.AttemptFenceDigest ||
		!bytes.Equal(attempt.NoncePrefix, snapshot.AttemptNoncePrefix) {
		return ErrAttemptFenceLost
	}
	return nil
}

func (worker *PersistentWorker) claimFinalArchiveNonce(
	ctx context.Context,
	request PersistentSealRequest,
	snapshot PersistentAttemptSnapshot,
	locator string,
) error {
	if !validStoreLocator(locator) {
		return ErrAttemptFenceLost
	}
	return database.WithSQLiteBusyRetryTx(ctx, worker.db, func(tx *gorm.DB) error {
		now := worker.now().UTC()
		var discovered struct {
			OwnerUserID uint `gorm:"column:owner_user_id"`
		}
		result := tx.Model(&model.BackupAssetExportJob{}).Select("owner_user_id").
			Where("id = ?", request.JobID).Limit(1).Scan(&discovered)
		if result.Error != nil {
			return fmt.Errorf("discover export job owner for final nonce claim: %w", result.Error)
		}
		if result.RowsAffected != 1 || discovered.OwnerUserID == 0 {
			return ErrAttemptFenceLost
		}
		if _, err := ensureAndLockQuotaBucketPairTx(tx, discovered.OwnerUserID, now); err != nil {
			return err
		}

		var job model.BackupAssetExportJob
		result = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND owner_user_id = ? AND current_attempt_id = ? AND execution_state = ?",
				request.JobID, discovered.OwnerUserID, request.AttemptID, ExecutionRunning).
			Limit(1).Find(&job)
		if result.Error != nil {
			return fmt.Errorf("lock export job for final nonce claim: %w", result.Error)
		}
		if result.RowsAffected != 1 || !sameSealedSnapshotAuthority(job, snapshot) ||
			!now.Before(job.AbsoluteDeadline.UTC()) {
			return ErrAttemptFenceLost
		}

		var attempt model.BackupAssetExportAttempt
		result = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND job_id = ? AND is_current = ? AND state = ? AND staging_locator = ?",
				request.AttemptID, request.JobID, true, AttemptActive, "").
			Limit(1).Find(&attempt)
		if result.Error != nil {
			return fmt.Errorf("lock export attempt for final nonce claim: %w", result.Error)
		}
		if result.RowsAffected != 1 || !now.Before(attempt.LeaseExpiresAt.UTC()) ||
			!equalFenceToken(attempt.FenceToken, request.FenceToken) ||
			!validAttemptFenceDigest(attempt.FenceToken, attempt.FenceDigest) ||
			attempt.FenceDigest != snapshot.AttemptFenceDigest ||
			!bytes.Equal(attempt.NoncePrefix, snapshot.AttemptNoncePrefix) {
			return ErrAttemptFenceLost
		}
		if err := validatePersistedSourceFencesTx(tx, job, now); err != nil {
			return err
		}
		result = tx.Model(&model.BackupAssetExportAttempt{}).
			Where("id = ? AND job_id = ? AND is_current = ? AND state = ? AND staging_locator = ?",
				attempt.ID, job.ID, true, AttemptActive, "").
			Updates(map[string]any{"staging_locator": locator, "updated_at": now})
		if result.Error != nil {
			return fmt.Errorf("claim export final archive nonce: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return ErrAttemptFenceLost
		}
		return nil
	})
}

func (worker *PersistentWorker) PublishReady(
	ctx context.Context, request PersistentPublishRequest,
) (PersistentPublishResult, error) {
	if worker == nil || worker.attemptWork == nil || backupasset.ValidateOpaqueID(request.JobID) != nil ||
		backupasset.ValidateOpaqueID(request.AttemptID) != nil || backupasset.ValidateOpaqueID(request.ArtifactID) != nil ||
		len(request.FenceToken) != 32 {
		return PersistentPublishResult{}, ErrAttemptFenceLost
	}
	work, err := worker.attemptWork.Start(ctx, request.JobID)
	if err != nil {
		return PersistentPublishResult{}, err
	}
	defer work.Finish()
	ctx = work.Context()
	snapshot, err := worker.loader.Load(ctx, PersistentAttemptLoadRequest{
		JobID: request.JobID, AttemptID: request.AttemptID, FenceToken: request.FenceToken,
	})
	if err != nil {
		return PersistentPublishResult{}, err
	}
	defer snapshot.ClearKeyMaterial()
	artifact, err := worker.loadAndAuthenticateSealedArtifact(ctx, snapshot, request.ArtifactID)
	if err != nil {
		return PersistentPublishResult{}, err
	}
	var expiresAt time.Time
	err = worker.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var buckets quotaBucketPair
		if worker.workerCapacity != nil {
			var discovered struct {
				OwnerUserID uint `gorm:"column:owner_user_id"`
			}
			result := tx.Model(&model.BackupAssetExportJob{}).Select("owner_user_id").
				Where("id = ?", request.JobID).Limit(1).Scan(&discovered)
			if result.Error != nil {
				return fmt.Errorf("discover export job owner for ready publication: %w", result.Error)
			}
			if result.RowsAffected != 1 || discovered.OwnerUserID == 0 {
				return ErrAttemptFenceLost
			}
			var err error
			buckets, err = ensureAndLockQuotaBucketPairTx(tx, discovered.OwnerUserID, worker.now().UTC())
			if err != nil {
				return err
			}
		}
		var job model.BackupAssetExportJob
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND current_attempt_id = ? AND execution_state = ?", request.JobID, request.AttemptID, ExecutionSealing).
			Limit(1).Find(&job)
		if result.Error != nil {
			return fmt.Errorf("load export job for ready publication: %w", result.Error)
		}
		if result.RowsAffected != 1 || job.ResultKind == "" || job.ArtifactBytes != artifact.CiphertextSize {
			return ErrAttemptFenceLost
		}
		var attempt model.BackupAssetExportAttempt
		result = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND job_id = ? AND is_current = ? AND state = ?", request.AttemptID, request.JobID, true, AttemptSealing).
			Limit(1).Find(&attempt)
		if result.Error != nil {
			return fmt.Errorf("load export attempt for ready publication: %w", result.Error)
		}
		if result.RowsAffected != 1 || !equalFenceToken(attempt.FenceToken, request.FenceToken) ||
			!validAttemptFenceDigest(attempt.FenceToken, attempt.FenceDigest) ||
			attempt.FenceDigest != snapshot.AttemptFenceDigest || !bytes.Equal(attempt.NoncePrefix, artifact.NoncePrefix) {
			return ErrAttemptFenceLost
		}
		var workerReservations []model.BackupAssetExportReservation
		if worker.workerCapacity != nil {
			workerReservations, err = lockAttemptWorkerReservationPairTx(tx, buckets, job, attempt)
			if err != nil {
				return err
			}
		}
		var persistedArtifact model.BackupAssetExportArtifact
		result = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND job_id = ? AND attempt_id = ? AND state = ?", request.ArtifactID, request.JobID, request.AttemptID, "sealed").
			Limit(1).Find(&persistedArtifact)
		if result.Error != nil {
			return fmt.Errorf("load sealed export artifact for ready publication: %w", result.Error)
		}
		if result.RowsAffected != 1 || !sameSealedArtifact(persistedArtifact, artifact) ||
			persistedArtifact.ExpiresAt != nil || persistedArtifact.SealedAt == nil {
			return ErrAttemptFenceLost
		}
		var key model.BackupAssetExportKey
		result = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND job_id = ? AND state = ?", artifact.JobKeyID, job.ID, "active").Limit(1).Find(&key)
		if result.Error != nil {
			return fmt.Errorf("load export job key for ready publication: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return ErrAttemptFenceLost
		}
		if key.KEKVersion != snapshot.KEKVersion || len(key.WrappedDEK) == 0 {
			return ErrUnavailable
		}
		lockedSources, err := lockValidatedPersistedSourceFencesTx(tx, job)
		if err != nil {
			return err
		}
		now := worker.now().UTC()
		if !now.Before(job.AbsoluteDeadline.UTC()) || !now.Before(attempt.LeaseExpiresAt.UTC()) {
			return ErrAttemptFenceLost
		}
		sources, err := validateLockedPersistedSourceFenceExpiries(lockedSources, now)
		if err != nil {
			return err
		}
		for _, item := range snapshot.Items {
			if err := worker.metadata.RevalidateMetadataTx(ctx, tx, item.Frozen); err != nil {
				return err
			}
		}
		deadlines := make([]SourceDeadline, 0, len(sources))
		for _, source := range sources {
			deadlines = append(deadlines, SourceDeadline{
				AbsoluteDeadline: source.AbsoluteDeadline, RetentionUntil: source.RetentionUntil,
			})
		}
		expiresAt, err = ComputeReadyExpiry(now, time.Duration(job.ReadyTTLSeconds)*time.Second, deadlines)
		if err != nil {
			return err
		}
		result = tx.Model(&model.BackupAssetExportArtifact{}).
			Where("id = ? AND state = ? AND expires_at IS NULL", artifact.ID, "sealed").
			Updates(map[string]any{"expires_at": expiresAt, "updated_at": now})
		if result.Error != nil {
			return fmt.Errorf("set sealed export artifact expiry: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return ErrAttemptFenceLost
		}
		finishedAt := now
		result = tx.Model(&model.BackupAssetExportAttempt{}).
			Where("id = ? AND job_id = ? AND is_current = ? AND state = ?", attempt.ID, job.ID, true, AttemptSealing).
			Updates(map[string]any{
				"state": string(AttemptSealed), "is_current": false, "finished_at": finishedAt, "updated_at": now,
			})
		if result.Error != nil {
			return fmt.Errorf("seal export attempt for ready publication: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return ErrAttemptFenceLost
		}
		if worker.workerCapacity != nil {
			if err := releaseAttemptWorkerReservationPairTx(tx, workerReservations, attempt, now); err != nil {
				return err
			}
		}
		result = tx.Model(&model.BackupAssetExportJob{}).
			Where("id = ? AND current_attempt_id = ? AND execution_state = ?", job.ID, attempt.ID, ExecutionSealing).
			Updates(map[string]any{
				"execution_state": string(ExecutionReady), "ready_at": now, "expires_at": expiresAt,
				"transition_revision": gorm.Expr("transition_revision + 1"), "updated_at": now,
			})
		if result.Error != nil {
			return fmt.Errorf("mark export job ready: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return ErrAttemptFenceLost
		}
		return nil
	})
	if err != nil {
		return PersistentPublishResult{}, err
	}
	return PersistentPublishResult{ArtifactID: artifact.ID, ExpiresAt: expiresAt}, nil
}

func (worker *PersistentWorker) ReconcileJob(
	ctx context.Context, request PersistentReconcileRequest,
) (PersistentReconcileResult, error) {
	if worker == nil || backupasset.ValidateOpaqueID(request.JobID) != nil {
		return PersistentReconcileResult{}, ErrUnavailable
	}
	ctx = nonNilServiceContext(ctx)
	var job model.BackupAssetExportJob
	result := worker.db.WithContext(ctx).Where("id = ?", request.JobID).Limit(1).Find(&job)
	if result.Error != nil {
		return PersistentReconcileResult{}, fmt.Errorf("load export job for reconciliation: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return PersistentReconcileResult{}, ErrUnavailable
	}
	if ExecutionState(job.ExecutionState) == ExecutionReady {
		return worker.reconcileReadyJob(ctx, job)
	}
	if ExecutionState(job.ExecutionState) != ExecutionSealing || job.CurrentAttemptID == nil {
		return PersistentReconcileResult{}, ErrUnavailable
	}
	var attempt model.BackupAssetExportAttempt
	result = worker.db.WithContext(ctx).
		Where("id = ? AND job_id = ? AND state = ? AND is_current = ?", *job.CurrentAttemptID, job.ID, AttemptSealing, true).
		Limit(1).Find(&attempt)
	if result.Error != nil {
		return PersistentReconcileResult{}, fmt.Errorf("load sealing export attempt for reconciliation: %w", result.Error)
	}
	if result.RowsAffected != 1 || !validAttemptFenceDigest(attempt.FenceToken, attempt.FenceDigest) {
		return PersistentReconcileResult{}, ErrAttemptFenceLost
	}
	if !worker.now().UTC().Before(attempt.LeaseExpiresAt.UTC()) {
		return worker.retryExpiredSealingAttempt(ctx, job, attempt)
	}
	var artifact model.BackupAssetExportArtifact
	result = worker.db.WithContext(ctx).
		Where("job_id = ? AND attempt_id = ? AND state = ?", job.ID, attempt.ID, "sealed").
		Limit(1).Find(&artifact)
	if result.Error != nil {
		return PersistentReconcileResult{}, fmt.Errorf("load sealed export artifact for reconciliation: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return worker.revokeUnpublishable(ctx, job.ID, "artifact_missing", ErrUnavailable)
	}
	var key model.BackupAssetExportKey
	result = worker.db.WithContext(ctx).
		Where("id = ? AND job_id = ? AND state = ?", artifact.JobKeyID, job.ID, "active").Limit(1).Find(&key)
	if result.Error != nil {
		return PersistentReconcileResult{}, fmt.Errorf("load export job key for reconciliation: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return worker.revokeUnpublishable(ctx, job.ID, "key_unavailable", ErrUnavailable)
	}
	needsTakeover, err := worker.sourceOwnerTakeoverRequired(ctx, job)
	if err != nil {
		return PersistentReconcileResult{}, err
	}
	if needsTakeover {
		if worker.sourceLeases == nil {
			return PersistentReconcileResult{}, ErrAttemptFenceLost
		}
		coordinator, err := NewAttemptCoordinator(worker.db, worker.now, worker.sourceLeases)
		if err != nil {
			return PersistentReconcileResult{}, err
		}
		if _, err := coordinator.takeoverSourceLeasesForSealingAttempt(ctx, job.ID, sourceLeaseTakeoverAttempt{
			attemptID: attempt.ID, fenceToken: append([]byte(nil), attempt.FenceToken...), fenceDigest: attempt.FenceDigest,
		}); err != nil {
			return PersistentReconcileResult{}, err
		}
	}
	published, err := worker.PublishReady(ctx, PersistentPublishRequest{
		JobID: job.ID, AttemptID: attempt.ID, FenceToken: append([]byte(nil), attempt.FenceToken...), ArtifactID: artifact.ID,
	})
	if err != nil {
		if errors.Is(err, ErrCipherTampered) || errors.Is(err, ErrInvalidStore) {
			return worker.revokeUnpublishable(ctx, job.ID, "artifact_tampered", err)
		}
		return PersistentReconcileResult{}, err
	}
	return PersistentReconcileResult{
		Action: PersistentReconcilePublished, ArtifactID: published.ArtifactID, ExpiresAt: published.ExpiresAt,
	}, nil
}

func (worker *PersistentWorker) retryExpiredSealingAttempt(
	ctx context.Context,
	job model.BackupAssetExportJob,
	attempt model.BackupAssetExportAttempt,
) (PersistentReconcileResult, error) {
	coordinator, err := newAttemptCoordinator(worker.db, worker.now, worker.workerCapacity)
	if err != nil {
		return PersistentReconcileResult{}, errors.Join(ErrAttemptLeaseExpired, err)
	}
	failed, err := coordinator.Fail(ctx, AttemptFailureRequest{
		JobID: job.ID, AttemptID: attempt.ID, FenceToken: append([]byte(nil), attempt.FenceToken...),
		Category: "heartbeat_lost", Retryable: true,
	})
	if err != nil {
		return PersistentReconcileResult{}, errors.Join(ErrAttemptLeaseExpired, err)
	}
	if failed.ExecutionState != ExecutionRetryWait && failed.ExecutionState != ExecutionFailed {
		return PersistentReconcileResult{}, errors.Join(ErrAttemptLeaseExpired, ErrInvalidTransition)
	}
	cleanupErr := worker.DiscardAttempt(ctx, PersistentDiscardAttemptRequest{JobID: job.ID, AttemptID: attempt.ID})
	return PersistentReconcileResult{}, errors.Join(ErrAttemptLeaseExpired, cleanupErr)
}

type persistentReadyTuple struct {
	job      model.BackupAssetExportJob
	attempt  model.BackupAssetExportAttempt
	artifact model.BackupAssetExportArtifact
	key      model.BackupAssetExportKey
	sources  []model.BackupAssetExportSourceLease
}

type readyIntegrityJobTuple struct {
	ID                       string
	OwnerUserID              uint
	LifecycleEnqueueSequence int64
	SelectionDigest          string
	SelectionSchemaVersion   int
	ArchiveFormat            string
	ArchiveProfile           string
	LimitsSchemaVersion      int
	ChunkBytes               int64
	MaxItems                 int
	MaxSourcePoints          int
	MaxItemBytes             int64
	MaxLogicalBytes          int64
	MaxProviderBytes         int64
	MaxCiphertextBytes       int64
	MaxOpenReaders           int
	MaxDurationSeconds       int64
	MaxAttempts              int
	RetryBaseSeconds         int64
	RetryMaxDelaySeconds     int64
	LeaseTTLSeconds          int64
	LeaseRenewMarginSeconds  int64
	ReadyTTLSeconds          int64
	ExecutionState           string
	ResultKind               string
	CleanupState             string
	CurrentAttemptID         *string
	CurrentFenceRevision     int64
	AbsoluteDeadline         time.Time
	ReadyAt                  *time.Time
	ExpiresAt                *time.Time
	ItemCount                int64
	PackedCount              int64
	SkippedCount             int64
	FailedCount              int64
	LogicalBytes             int64
	ProviderBytes            int64
	ArtifactBytes            int64
	ErrorCategory            string
	TransitionRevision       int64
	CreatedAt                time.Time
	UpdatedAt                time.Time
}

type readyIntegrityAttemptTuple struct {
	ID                      string
	JobID                   string
	AttemptNumber           int
	WorkerOwner             string
	State                   string
	FenceToken              []byte
	FenceDigest             string
	NoncePrefix             []byte
	LeaseExpiresAt          time.Time
	CheckpointOrdinal       int
	CheckpointItemCount     int64
	CheckpointLogicalBytes  int64
	CheckpointProviderBytes int64
	StagingLocator          string
	FailureCategory         string
	IsCurrent               bool
	StartedAt               time.Time
	FinishedAt              *time.Time
	CreatedAt               time.Time
	UpdatedAt               time.Time
}

type readyIntegrityArtifactTuple struct {
	ID               string
	JobID            string
	AttemptID        string
	State            string
	JobKeyID         string
	CipherVersion    int
	FormatVersion    int
	ChunkBytes       int64
	ChunkCount       int64
	NoncePrefix      []byte
	PlaintextSize    int64
	CiphertextSize   int64
	PlaintextDigest  string
	ArchiveDigest    string
	CiphertextDigest string
	Locator          string
	CreatedAt        time.Time
	SealedAt         *time.Time
	ExpiresAt        *time.Time
	PurgedAt         *time.Time
	PurgeError       string
	UpdatedAt        time.Time
}

type readyIntegrityKeyTuple struct {
	ID            string
	JobID         string
	State         string
	KeyRevision   int64
	KEKVersion    int
	WrapAlgorithm string
	EnvelopeNonce []byte
	WrappedDEK    []byte
	CreatedAt     time.Time
	RewrappedAt   *time.Time
	DestroyedAt   *time.Time
}

type readyIntegritySourceTuple struct {
	ID               string
	JobID            string
	RecoveryPointID  string
	LeaseID          string
	LeaseAttemptID   string
	FenceHash        string
	AbsoluteDeadline time.Time
	RetentionUntil   *time.Time
	State            string
	AcquiredAt       time.Time
	RenewedAt        time.Time
	ReleasedAt       *time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type readyIntegrityTuple struct {
	Job      readyIntegrityJobTuple
	Attempt  readyIntegrityAttemptTuple
	Artifact readyIntegrityArtifactTuple
	Key      readyIntegrityKeyTuple
	Sources  []readyIntegritySourceTuple
}

func readyIntegrityExpiry(expiresAt *time.Time) time.Time {
	if expiresAt == nil {
		return time.Time{}
	}
	return expiresAt.UTC()
}

func newReadyIntegrityToken(tuple persistentReadyTuple, verifyArtifact readyArtifactVerifier) (*ReadyIntegrityToken, error) {
	digest, err := readyIntegrityDigest(tuple.job, tuple.attempt, tuple.artifact, tuple.key, tuple.sources)
	if err != nil {
		return nil, err
	}
	return &ReadyIntegrityToken{
		state: &readyIntegrityTokenState{
			jobID:          tuple.job.ID,
			readyExpiry:    readyIntegrityExpiry(tuple.job.ExpiresAt),
			digest:         digest,
			verifyArtifact: verifyArtifact,
		},
	}, nil
}

func readyIntegrityDigest(
	job model.BackupAssetExportJob,
	attempt model.BackupAssetExportAttempt,
	artifact model.BackupAssetExportArtifact,
	key model.BackupAssetExportKey,
	sources []model.BackupAssetExportSourceLease,
) ([32]byte, error) {
	material := readyIntegrityTuple{
		Job: readyIntegrityJobTuple{
			ID: job.ID, OwnerUserID: job.OwnerUserID,
			LifecycleEnqueueSequence: job.LifecycleEnqueueSequence, SelectionDigest: job.SelectionDigest,
			SelectionSchemaVersion:  job.SelectionSchemaVersion,
			ArchiveFormat:           job.ArchiveFormat,
			ArchiveProfile:          job.ArchiveProfile,
			LimitsSchemaVersion:     job.LimitsSchemaVersion,
			ChunkBytes:              job.ChunkBytes,
			MaxItems:                job.MaxItems,
			MaxSourcePoints:         job.MaxSourcePoints,
			MaxItemBytes:            job.MaxItemBytes,
			MaxLogicalBytes:         job.MaxLogicalBytes,
			MaxProviderBytes:        job.MaxProviderBytes,
			MaxCiphertextBytes:      job.MaxCiphertextBytes,
			MaxOpenReaders:          job.MaxOpenReaders,
			MaxDurationSeconds:      job.MaxDurationSeconds,
			MaxAttempts:             job.MaxAttempts,
			RetryBaseSeconds:        job.RetryBaseSeconds,
			RetryMaxDelaySeconds:    job.RetryMaxDelaySeconds,
			LeaseTTLSeconds:         job.LeaseTTLSeconds,
			LeaseRenewMarginSeconds: job.LeaseRenewMarginSeconds,
			ReadyTTLSeconds:         job.ReadyTTLSeconds,
			CurrentAttemptID:        job.CurrentAttemptID,
			CurrentFenceRevision:    job.CurrentFenceRevision, ExecutionState: job.ExecutionState,
			TransitionRevision: job.TransitionRevision, ResultKind: job.ResultKind, CleanupState: job.CleanupState,
			AbsoluteDeadline: job.AbsoluteDeadline, ReadyAt: job.ReadyAt, ExpiresAt: job.ExpiresAt,
			ItemCount: job.ItemCount, PackedCount: job.PackedCount, SkippedCount: job.SkippedCount,
			FailedCount: job.FailedCount, LogicalBytes: job.LogicalBytes, ProviderBytes: job.ProviderBytes,
			ArtifactBytes: job.ArtifactBytes, ErrorCategory: job.ErrorCategory,
			CreatedAt: job.CreatedAt, UpdatedAt: job.UpdatedAt,
		},
		Attempt: readyIntegrityAttemptTuple{
			ID: attempt.ID, JobID: attempt.JobID, AttemptNumber: attempt.AttemptNumber, WorkerOwner: attempt.WorkerOwner,
			State: attempt.State, FenceToken: append([]byte(nil), attempt.FenceToken...), FenceDigest: attempt.FenceDigest,
			NoncePrefix: append([]byte(nil), attempt.NoncePrefix...), LeaseExpiresAt: attempt.LeaseExpiresAt,
			CheckpointOrdinal: attempt.CheckpointOrdinal, CheckpointItemCount: attempt.CheckpointItemCount,
			CheckpointLogicalBytes: attempt.CheckpointLogicalBytes, CheckpointProviderBytes: attempt.CheckpointProviderBytes,
			StagingLocator: attempt.StagingLocator, FailureCategory: attempt.FailureCategory, IsCurrent: attempt.IsCurrent,
			StartedAt: attempt.StartedAt, FinishedAt: attempt.FinishedAt, CreatedAt: attempt.CreatedAt, UpdatedAt: attempt.UpdatedAt,
		},
		Artifact: readyIntegrityArtifactTuple{
			ID: artifact.ID, JobID: artifact.JobID, AttemptID: artifact.AttemptID, State: artifact.State,
			JobKeyID: artifact.JobKeyID, CipherVersion: artifact.CipherVersion, FormatVersion: artifact.FormatVersion,
			ChunkBytes: artifact.ChunkBytes, ChunkCount: artifact.ChunkCount,
			NoncePrefix: append([]byte(nil), artifact.NoncePrefix...), PlaintextSize: artifact.PlaintextSize,
			CiphertextSize: artifact.CiphertextSize, PlaintextDigest: artifact.PlaintextDigest,
			ArchiveDigest: artifact.ArchiveDigest, CiphertextDigest: artifact.CiphertextDigest,
			Locator: artifact.Locator, CreatedAt: artifact.CreatedAt, SealedAt: artifact.SealedAt, ExpiresAt: artifact.ExpiresAt,
			PurgedAt: artifact.PurgedAt, PurgeError: artifact.PurgeError,
			UpdatedAt: artifact.UpdatedAt,
		},
		Key: readyIntegrityKeyTuple{
			ID: key.ID, JobID: key.JobID, State: key.State, KeyRevision: key.KeyRevision,
			KEKVersion: key.KEKVersion, WrapAlgorithm: key.WrapAlgorithm,
			EnvelopeNonce: append([]byte(nil), key.EnvelopeNonce...), WrappedDEK: append([]byte(nil), key.WrappedDEK...),
			CreatedAt: key.CreatedAt, RewrappedAt: key.RewrappedAt, DestroyedAt: key.DestroyedAt,
		},
		Sources: make([]readyIntegritySourceTuple, 0, len(sources)),
	}
	for _, source := range sources {
		material.Sources = append(material.Sources, readyIntegritySourceTuple{
			ID: source.ID, JobID: source.JobID, RecoveryPointID: source.RecoveryPointID, LeaseID: source.LeaseID,
			LeaseAttemptID: source.LeaseAttemptID,
			FenceHash:      source.FenceHash, AbsoluteDeadline: source.AbsoluteDeadline,
			RetentionUntil: source.RetentionUntil, State: source.State,
			AcquiredAt: source.AcquiredAt, RenewedAt: source.RenewedAt, ReleasedAt: source.ReleasedAt,
			CreatedAt: source.CreatedAt, UpdatedAt: source.UpdatedAt,
		})
	}
	if err := validateReadyIntegrityCanonicalValue(reflect.ValueOf(material)); err != nil {
		return [32]byte{}, err
	}
	encoded, err := json.Marshal(material)
	if err != nil {
		return [32]byte{}, fmt.Errorf("encode ready integrity tuple: %w", err)
	}
	return sha256.Sum256(encoded), nil
}

func validateReadyIntegrityCanonicalValue(value reflect.Value) error {
	if !value.IsValid() {
		return nil
	}
	if value.Type() == reflect.TypeFor[time.Time]() {
		if _, err := value.Interface().(time.Time).MarshalJSON(); err != nil {
			return fmt.Errorf("invalid ready integrity timestamp: %w", err)
		}
		return nil
	}
	switch value.Kind() {
	case reflect.String:
		if !utf8.ValidString(value.String()) {
			return errors.New("invalid UTF-8 in ready integrity tuple")
		}
	case reflect.Pointer:
		if !value.IsNil() {
			return validateReadyIntegrityCanonicalValue(value.Elem())
		}
	case reflect.Struct:
		for index := 0; index < value.NumField(); index++ {
			if err := validateReadyIntegrityCanonicalValue(value.Field(index)); err != nil {
				return err
			}
		}
	case reflect.Slice:
		if value.Type().Elem().Kind() == reflect.Uint8 {
			return nil
		}
		for index := 0; index < value.Len(); index++ {
			if err := validateReadyIntegrityCanonicalValue(value.Index(index)); err != nil {
				return err
			}
		}
	}
	return nil
}

func readyIntegrityTokenMatches(
	token *ReadyIntegrityToken,
	job model.BackupAssetExportJob,
	attempt model.BackupAssetExportAttempt,
	artifact model.BackupAssetExportArtifact,
	key model.BackupAssetExportKey,
	sources []model.BackupAssetExportSourceLease,
) bool {
	if token == nil || token.state == nil {
		return false
	}
	digest, err := readyIntegrityDigest(job, attempt, artifact, key, sources)
	if err != nil {
		return false
	}
	token.state.mu.Lock()
	defer token.state.mu.Unlock()
	return token.state.consumed && token.state.jobID == job.ID && !token.state.readyExpiry.IsZero() &&
		token.state.readyExpiry.Equal(readyIntegrityExpiry(job.ExpiresAt)) &&
		token.state.digest == digest
}

func (token *ReadyIntegrityToken) consumeAndPin(ctx context.Context, jobID string) (func() error, error) {
	if token == nil || token.state == nil {
		return nil, ErrAttemptFenceLost
	}
	state := token.state
	state.mu.Lock()
	if state.consumed {
		state.mu.Unlock()
		return nil, ErrAttemptFenceLost
	}
	state.consumed = true
	verifyArtifact := state.verifyArtifact
	state.verifyArtifact = nil
	valid := state.jobID == jobID && verifyArtifact != nil
	state.mu.Unlock()
	if !valid {
		return nil, ErrAttemptFenceLost
	}
	return verifyArtifact(ctx)
}

func (worker *PersistentWorker) reconcileReadyJob(
	ctx context.Context, job model.BackupAssetExportJob,
) (PersistentReconcileResult, error) {
	jobID := job.ID
	if job.CurrentAttemptID == nil || backupasset.ValidateOpaqueID(*job.CurrentAttemptID) != nil {
		return worker.revokeUnpublishable(ctx, jobID, "artifact_tampered", ErrUnavailable)
	}
	var attempt model.BackupAssetExportAttempt
	result := worker.db.WithContext(ctx).
		Where("id = ? AND job_id = ?", *job.CurrentAttemptID, jobID).
		Limit(1).Find(&attempt)
	if result.Error != nil {
		return PersistentReconcileResult{}, fmt.Errorf("load ready Export attempt for fence preflight: %w", result.Error)
	}
	if result.RowsAffected != 1 || !validAttemptFenceDigest(attempt.FenceToken, attempt.FenceDigest) {
		return worker.revokeUnpublishable(ctx, jobID, "artifact_tampered", ErrUnavailable)
	}

	now := worker.now().UTC()
	persisted, category, err := worker.loadReadyTuple(ctx, jobID, now)
	if err != nil {
		if category != "" {
			if category == "source_expired" {
				return worker.revokeSourceExpired(ctx, jobID, err)
			}
			return worker.revokeUnpublishable(ctx, jobID, category, err)
		}
		return PersistentReconcileResult{}, err
	}
	if err := worker.revalidateReadyAttemptFence(ctx, persisted.job.ID, persisted.attempt); err != nil {
		if errors.Is(err, ErrAttemptFenceLost) {
			return worker.revokeUnpublishable(ctx, jobID, "artifact_tampered", ErrUnavailable)
		}
		return PersistentReconcileResult{}, err
	}

	material, err := worker.keys.ByVersion(ctx, backupasset.KeyDomainExportStore, persisted.key.KEKVersion)
	defer clear(material.Key)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return PersistentReconcileResult{}, ctxErr
	}
	if err != nil {
		if errors.Is(err, backupasset.ErrKeyUnavailable) || errors.Is(err, backupasset.ErrKeyLost) {
			return worker.revokeUnpublishable(ctx, jobID, "key_unavailable", ErrUnavailable)
		}
		return PersistentReconcileResult{}, fmt.Errorf("load ready Export KEK: %w", err)
	}
	if material.Domain != backupasset.KeyDomainExportStore || material.Version != persisted.key.KEKVersion ||
		(material.State != backupasset.DomainKeyActive && material.State != backupasset.DomainKeyVerifyOnly) ||
		len(material.Key) != 32 {
		return worker.revokeUnpublishable(ctx, jobID, "key_unavailable", ErrUnavailable)
	}
	dek, err := UnwrapJobDEK(JobKeyBinding{
		ExportID: persisted.job.ID, SelectionDigest: persisted.job.SelectionDigest,
		KEKVersion: persisted.key.KEKVersion, WrapAlgorithm: persisted.key.WrapAlgorithm,
	}, material.Key, JobKeyEnvelope{
		Nonce: persisted.key.EnvelopeNonce, Ciphertext: persisted.key.WrappedDEK,
	})
	if err != nil {
		return worker.revokeUnpublishable(ctx, jobID, "key_unavailable", ErrUnavailable)
	}
	defer clear(dek)

	reader, category, err := worker.openReadyArtifact(persisted.artifact.Locator)
	if err != nil {
		if category != "" {
			return worker.revokeUnpublishable(ctx, jobID, category, err)
		}
		return PersistentReconcileResult{}, err
	}
	var cipherResult CipherResult
	decryptErr := withReadyArtifactRelease(reader.Close, func() error {
		var decryptErr error
		cipherResult, decryptErr = DecryptStream(ctx, io.Discard, reader, dek, CipherBinding{
			ExportID: persisted.job.ID, SelectionDigest: persisted.job.SelectionDigest,
			ArchiveProfile: persisted.job.ArchiveProfile, FormatVersion: persisted.artifact.FormatVersion,
			AttemptFenceDigest: persisted.attempt.FenceDigest, Purpose: CipherPurposeFinalArchive,
		})
		return decryptErr
	})
	if ctxErr := ctx.Err(); ctxErr != nil {
		return PersistentReconcileResult{}, errors.Join(ctxErr, decryptErr)
	}
	if decryptErr != nil {
		if errors.Is(decryptErr, ErrCipherTampered) {
			return worker.revokeUnpublishable(ctx, jobID, "artifact_tampered", decryptErr)
		}
		return PersistentReconcileResult{}, decryptErr
	}
	if !sameReadyCipherResult(cipherResult, persisted.artifact) {
		return worker.revokeUnpublishable(ctx, jobID, "artifact_tampered", ErrCipherTampered)
	}

	reloaded, _, err := worker.loadReadyTuple(ctx, jobID, now)
	if err != nil || !reflect.DeepEqual(reloaded, persisted) {
		return PersistentReconcileResult{}, ErrUnavailable
	}
	readyIntegrity, err := newReadyIntegrityToken(
		reloaded, newReadyArtifactVerifier(worker.keys, worker.store, reloaded),
	)
	if err != nil {
		return worker.revokeUnpublishable(ctx, jobID, "internal_failure", errors.Join(ErrUnavailable, err))
	}
	return PersistentReconcileResult{ReadyIntegrity: readyIntegrity}, nil
}

func (worker *PersistentWorker) loadReadyTuple(
	ctx context.Context, jobID string, now time.Time,
) (persistentReadyTuple, string, error) {
	var persisted persistentReadyTuple
	result := worker.db.WithContext(ctx).Where("id = ?", jobID).Limit(1).Find(&persisted.job)
	if result.Error != nil {
		return persisted, "", fmt.Errorf("load ready Export job: %w", result.Error)
	}
	if result.RowsAffected != 1 || ExecutionState(persisted.job.ExecutionState) != ExecutionReady {
		return persisted, "", ErrUnavailable
	}
	if err := readyMaintenanceShapeError(persisted.job, now); err != nil {
		return persisted, "internal_failure", err
	}
	readyExpired := !now.Before(persisted.job.ExpiresAt.UTC())
	validationNow := now
	if readyExpired {
		validationNow = persisted.job.ReadyAt.UTC()
	}
	if !validReadyDeliveryJob(persisted.job, validationNow) || persisted.job.ReadyAt.Before(persisted.job.CreatedAt) {
		return persisted, "internal_failure", ErrUnavailable
	}
	if persisted.job.SelectionSchemaVersion != 1 || persisted.job.LimitsSchemaVersion != 1 ||
		persisted.job.ReadyTTLSeconds <= 0 || persisted.job.ReadyTTLSeconds > math.MaxInt64/int64(time.Second) ||
		persisted.job.AbsoluteDeadline.IsZero() || persisted.job.CreatedAt.IsZero() || persisted.job.CurrentFenceRevision <= 0 {
		return persisted, "internal_failure", ErrUnavailable
	}
	cleanupCategory := ""
	err := worker.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		persisted.sources, err = loadPersistedSourceLeasesTx(tx, persisted.job.ID)
		if err != nil {
			if errors.Is(err, ErrAttemptFenceLost) {
				cleanupCategory = "source_expired"
			}
			return err
		}
		deadlines := make([]SourceDeadline, 0, len(persisted.sources))
		for _, source := range persisted.sources {
			if _, err := loadSourceLeaseIdentityTx(tx, persisted.job, source, now); err != nil {
				if errors.Is(err, ErrAttemptFenceLost) || errors.Is(err, ErrSourceDeadlineReached) ||
					errors.Is(err, ErrUnavailable) {
					cleanupCategory = "source_expired"
				}
				if readyExpired && errors.Is(err, ErrSourceDeadlineReached) {
					return ErrReadyExpired
				}
				return err
			}
			deadlines = append(deadlines, SourceDeadline{
				AbsoluteDeadline: source.AbsoluteDeadline, RetentionUntil: source.RetentionUntil,
			})
		}
		expectedExpiry, err := ComputeReadyExpiry(
			persisted.job.ReadyAt.UTC(), time.Duration(persisted.job.ReadyTTLSeconds)*time.Second, deadlines,
		)
		if err != nil {
			cleanupCategory = "source_expired"
			return err
		}
		if !expectedExpiry.Equal(persisted.job.ExpiresAt.UTC()) {
			cleanupCategory = "artifact_tampered"
			return ErrCipherTampered
		}
		if readyExpired {
			cleanupCategory = "deadline"
			return ErrReadyExpired
		}
		return nil
	})
	if err != nil && cleanupCategory != "" {
		return persisted, cleanupCategory, err
	}
	if err != nil {
		return persisted, "", err
	}

	result = worker.db.WithContext(ctx).
		Where("id = ? AND job_id = ?", *persisted.job.CurrentAttemptID, persisted.job.ID).
		Limit(1).Find(&persisted.attempt)
	if result.Error != nil {
		return persisted, "", fmt.Errorf("load ready Export attempt: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return persisted, "artifact_tampered", ErrUnavailable
	}

	var artifacts []model.BackupAssetExportArtifact
	result = worker.db.WithContext(ctx).Where("job_id = ?", persisted.job.ID).
		Order("id ASC").Limit(2).Find(&artifacts)
	if result.Error != nil {
		return persisted, "", fmt.Errorf("load ready Export artifact: %w", result.Error)
	}
	if len(artifacts) == 0 {
		return persisted, "artifact_missing", ErrUnavailable
	}
	if len(artifacts) != 1 {
		return persisted, "artifact_tampered", ErrUnavailable
	}
	persisted.artifact = artifacts[0]
	fenceDigest := sha256.Sum256(persisted.attempt.FenceToken)
	if len(persisted.attempt.FenceToken) != 32 ||
		hex.EncodeToString(fenceDigest[:]) != persisted.attempt.FenceDigest ||
		persisted.attempt.FinishedAt == nil || !persisted.attempt.FinishedAt.Equal(*persisted.job.ReadyAt) ||
		persisted.attempt.StartedAt.After(*persisted.attempt.FinishedAt) ||
		persisted.artifact.SealedAt == nil || persisted.artifact.SealedAt.After(*persisted.job.ReadyAt) ||
		persisted.artifact.CreatedAt.After(*persisted.artifact.SealedAt) ||
		!validReadyDeliveryArtifact(persisted.job, persisted.attempt, persisted.artifact, now) {
		return persisted, "artifact_tampered", ErrUnavailable
	}

	var keys []model.BackupAssetExportKey
	result = worker.db.WithContext(ctx).Where("job_id = ? AND state = ?", persisted.job.ID, "active").
		Order("id ASC").Limit(2).Find(&keys)
	if result.Error != nil {
		return persisted, "", fmt.Errorf("load ready Export key: %w", result.Error)
	}
	if len(keys) != 1 {
		return persisted, "key_unavailable", ErrUnavailable
	}
	persisted.key = keys[0]
	if persisted.key.ID != persisted.artifact.JobKeyID || backupasset.ValidateOpaqueID(persisted.key.ID) != nil ||
		persisted.key.KeyRevision <= 0 || persisted.key.KEKVersion <= 0 ||
		persisted.key.WrapAlgorithm != JobKeyWrapAlgorithmV1 || len(persisted.key.EnvelopeNonce) != 12 ||
		len(persisted.key.WrappedDEK) == 0 || persisted.key.DestroyedAt != nil {
		return persisted, "key_unavailable", ErrUnavailable
	}
	return persisted, "", nil
}

func (worker *PersistentWorker) revalidateReadyAttemptFence(
	ctx context.Context,
	jobID string,
	expected model.BackupAssetExportAttempt,
) error {
	var attempt model.BackupAssetExportAttempt
	result := worker.db.WithContext(ctx).Where("id = ? AND job_id = ?", expected.ID, jobID).Limit(1).Find(&attempt)
	if result.Error != nil {
		return fmt.Errorf("revalidate ready Export attempt fence: %w", result.Error)
	}
	if result.RowsAffected != 1 || !validAttemptFenceDigest(attempt.FenceToken, attempt.FenceDigest) ||
		attempt.FenceDigest != expected.FenceDigest || !sameBytes(attempt.FenceToken, expected.FenceToken) {
		return ErrAttemptFenceLost
	}
	return nil
}

func (worker *PersistentWorker) openReadyArtifact(locator string) (*os.File, string, error) {
	store := worker.store
	if store == nil || !validStoreLocator(locator) || !strings.HasSuffix(locator, ".xre") {
		return nil, "artifact_tampered", ErrInvalidStore
	}
	reader, err := store.OpenSealed(locator)
	if err == nil {
		return reader, "", nil
	}
	if errors.Is(err, ErrStoreObjectAbsent) && !errors.Is(err, ErrInvalidStore) {
		return nil, "artifact_missing", ErrUnavailable
	}
	if errors.Is(err, ErrStoreObjectUnsafe) {
		return nil, "artifact_tampered", err
	}
	return nil, "", err
}

func sameReadyCipherResult(result CipherResult, artifact model.BackupAssetExportArtifact) bool {
	return result.ChunkBytes == artifact.ChunkBytes && result.ChunkCount == artifact.ChunkCount &&
		result.PlaintextBytes == artifact.PlaintextSize && result.CiphertextBytes == artifact.CiphertextSize &&
		result.PlaintextDigest == artifact.PlaintextDigest && result.ArchiveDigest == artifact.ArchiveDigest &&
		result.CiphertextDigest == artifact.CiphertextDigest && bytes.Equal(result.NoncePrefix, artifact.NoncePrefix)
}

func newReadyArtifactVerifier(keys ExportKeyVersionSource, store *Store, tuple persistentReadyTuple) readyArtifactVerifier {
	locator := tuple.artifact.Locator
	binding := CipherBinding{
		ExportID: tuple.job.ID, SelectionDigest: tuple.job.SelectionDigest,
		ArchiveProfile: tuple.job.ArchiveProfile, FormatVersion: tuple.artifact.FormatVersion,
		AttemptFenceDigest: tuple.attempt.FenceDigest, Purpose: CipherPurposeFinalArchive,
	}
	expected := tuple.artifact
	expected.NoncePrefix = append([]byte(nil), tuple.artifact.NoncePrefix...)
	return func(ctx context.Context) (pinRelease func() error, resultErr error) {
		if keys == nil || store == nil {
			return nil, ErrAttemptFenceLost
		}
		reader, release, err := store.pinSealed(locator)
		if err != nil {
			return nil, readyArtifactVerificationError(err)
		}
		releaseOwned := true
		resultErr = withReadyArtifactRelease(func() error {
			if !releaseOwned {
				return nil
			}
			return release()
		}, func() error {
			material, err := keys.ByVersion(ctx, backupasset.KeyDomainExportStore, tuple.key.KEKVersion)
			defer clear(material.Key)
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			if err != nil {
				if errors.Is(err, backupasset.ErrKeyUnavailable) || errors.Is(err, backupasset.ErrKeyLost) {
					return errors.Join(ErrAttemptFenceLost, ErrUnavailable)
				}
				return fmt.Errorf("load ready Export KEK for pinned verification: %w", err)
			}
			if material.Domain != backupasset.KeyDomainExportStore || material.Version != tuple.key.KEKVersion ||
				(material.State != backupasset.DomainKeyActive && material.State != backupasset.DomainKeyVerifyOnly) ||
				len(material.Key) != 32 {
				return errors.Join(ErrAttemptFenceLost, ErrUnavailable)
			}
			dek, err := UnwrapJobDEK(JobKeyBinding{
				ExportID: tuple.job.ID, SelectionDigest: tuple.job.SelectionDigest,
				KEKVersion: tuple.key.KEKVersion, WrapAlgorithm: tuple.key.WrapAlgorithm,
			}, material.Key, JobKeyEnvelope{
				Nonce: tuple.key.EnvelopeNonce, Ciphertext: tuple.key.WrappedDEK,
			})
			if err != nil {
				return errors.Join(ErrAttemptFenceLost, ErrUnavailable)
			}
			defer clear(dek)
			result, decryptErr := DecryptStream(ctx, io.Discard, reader, dek, binding)
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			if decryptErr != nil {
				return readyArtifactVerificationError(decryptErr)
			}
			if !sameReadyCipherResult(result, expected) {
				return errors.Join(ErrAttemptFenceLost, ErrCipherTampered)
			}
			releaseOwned = false
			pinRelease = release
			return nil
		})
		return pinRelease, resultErr
	}
}

func withReadyArtifactRelease(release func() error, operation func() error) (resultErr error) {
	defer func() {
		recovered := recover()
		releaseErr := release()
		if recovered != nil {
			if releaseErr == nil {
				panic(recovered)
			}
			if panicErr, ok := recovered.(error); ok {
				panic(errors.Join(panicErr, releaseErr))
			}
			panic(errors.Join(fmt.Errorf("ready artifact verification panic: %v", recovered), releaseErr))
		}
		resultErr = errors.Join(resultErr, releaseErr)
	}()
	return operation()
}

func readyArtifactVerificationError(err error) error {
	if errors.Is(err, ErrStoreObjectAbsent) || errors.Is(err, ErrStoreObjectUnsafe) ||
		errors.Is(err, ErrInvalidStore) || errors.Is(err, ErrCipherTampered) {
		return errors.Join(ErrAttemptFenceLost, err)
	}
	return err
}

func (worker *PersistentWorker) revokeUnpublishable(
	ctx context.Context, jobID, category string, cause error,
) (PersistentReconcileResult, error) {
	if worker.lifecycle == nil {
		return PersistentReconcileResult{}, cause
	}
	if err := worker.lifecycle.FailUnpublishable(ctx, jobID, category); err != nil {
		return PersistentReconcileResult{}, errors.Join(cause, err)
	}
	return PersistentReconcileResult{Action: PersistentReconcileRevoked}, nil
}

func (worker *PersistentWorker) revokeSourceExpired(
	ctx context.Context, jobID string, cause error,
) (PersistentReconcileResult, error) {
	if worker.lifecycle == nil {
		return PersistentReconcileResult{}, cause
	}
	if err := worker.lifecycle.FailSourceExpired(ctx, jobID); err != nil {
		return PersistentReconcileResult{}, errors.Join(cause, err)
	}
	return PersistentReconcileResult{Action: PersistentReconcileRevoked}, nil
}

func (worker *PersistentWorker) sourceOwnerTakeoverRequired(
	ctx context.Context, job model.BackupAssetExportJob,
) (bool, error) {
	now := worker.now().UTC()
	needsTakeover := false
	err := worker.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		sources, err := loadPersistedSourceLeasesTx(tx, job.ID)
		if err != nil {
			return err
		}
		for _, source := range sources {
			lease, err := loadSourceLeaseIdentityTx(tx, job, source, now)
			if err != nil {
				return err
			}
			if !now.Before(lease.LeaseExpiresAt.UTC()) {
				needsTakeover = true
			}
		}
		return nil
	})
	return needsTakeover, err
}

func (worker *PersistentWorker) ReconcileOrphans(ctx context.Context) (int, error) {
	if worker == nil || worker.store == nil || worker.store.closed() {
		return 0, ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return purgeUnreferencedStore(ctx, worker.store, func(loadCtx context.Context) (map[string]struct{}, error) {
		return referencedStoreLocators(loadCtx, worker.db)
	})
}

func purgeUnreferencedStore(ctx context.Context, store *Store, loadReferences storeReferenceLoader) (int, error) {
	if store == nil || loadReferences == nil {
		return 0, ErrUnavailable
	}
	return store.purgeUnreferencedResolved(ctx, loadReferences)
}

func referencedStoreLocators(ctx context.Context, db *gorm.DB) (map[string]struct{}, error) {
	if db == nil {
		return nil, ErrUnavailable
	}
	referenced := make(map[string]struct{})
	var artifactLocators []string
	if err := db.WithContext(ctx).Model(&model.BackupAssetExportArtifact{}).
		Where("state <> ?", "purged").Pluck("locator", &artifactLocators).Error; err != nil {
		return nil, fmt.Errorf("load referenced export artifacts: %w", err)
	}
	var spoolLocators []string
	if err := db.WithContext(ctx).Model(&model.BackupAssetExportItemAttempt{}).
		Where("spool_locator <> ?", "").Pluck("spool_locator", &spoolLocators).Error; err != nil {
		return nil, fmt.Errorf("load referenced export spools: %w", err)
	}
	var attemptLocators []string
	if err := db.WithContext(ctx).Model(&model.BackupAssetExportAttempt{}).
		Where("staging_locator <> ?", "").Pluck("staging_locator", &attemptLocators).Error; err != nil {
		return nil, fmt.Errorf("load referenced export attempt objects: %w", err)
	}
	for _, locator := range append(append(artifactLocators, spoolLocators...), attemptLocators...) {
		if !validStoreLocator(locator) {
			return nil, ErrInvalidStore
		}
		referenced[locator] = struct{}{}
	}
	return referenced, nil
}

func (worker *PersistentWorker) authenticateSpool(
	ctx context.Context, snapshot PersistentAttemptSnapshot, item PersistentAttemptItem,
) error {
	reader, err := worker.store.OpenSealed(item.SpoolLocator)
	if err != nil {
		return err
	}
	result, decryptErr := DecryptStream(ctx, io.Discard, reader, snapshot.DEK, spoolCipherBinding(snapshot, item))
	closeErr := reader.Close()
	if decryptErr != nil || closeErr != nil {
		return errors.Join(decryptErr, closeErr)
	}
	if result.PlaintextBytes != item.Frozen.LogicalSize || result.PlaintextBytes != item.LogicalBytes ||
		result.CiphertextBytes != item.SpoolSize || result.CiphertextDigest != item.SpoolDigest {
		return ErrCipherTampered
	}
	return nil
}

func (worker *PersistentWorker) openDecryptedSpool(
	ctx context.Context, snapshot PersistentAttemptSnapshot, item PersistentAttemptItem,
) (io.ReadCloser, error) {
	reader, err := worker.store.OpenSealed(item.SpoolLocator)
	if err != nil {
		return nil, err
	}
	pipeReader, pipeWriter := io.Pipe()
	dek := append([]byte(nil), snapshot.DEK...)
	go func() {
		defer clear(dek)
		_, decryptErr := DecryptStream(ctx, pipeWriter, reader, dek, spoolCipherBinding(snapshot, item))
		closeErr := reader.Close()
		_ = pipeWriter.CloseWithError(errors.Join(decryptErr, closeErr))
	}()
	return pipeReader, nil
}

func spoolCipherBinding(snapshot PersistentAttemptSnapshot, item PersistentAttemptItem) CipherBinding {
	return CipherBinding{
		ExportID: snapshot.JobID, SelectionDigest: snapshot.SelectionDigest,
		ArchiveProfile: snapshot.ArchiveProfile, FormatVersion: 1,
		AttemptFenceDigest: snapshot.AttemptFenceDigest, Purpose: CipherPurposeItemSpool, ObjectID: item.ItemAttemptID,
	}
}

func (worker *PersistentWorker) persistSealedArchive(
	ctx context.Context,
	request PersistentSealRequest,
	snapshot PersistentAttemptSnapshot,
	artifactID, locator string,
	cipherResult CipherResult,
	report ArchiveReport,
) error {
	expectedPeakStoreBytes, err := persistentAttemptSnapshotPeakStoreBytes(snapshot)
	if err != nil {
		return ErrAttemptFenceLost
	}
	return worker.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		quotaNow := worker.now().UTC()
		var discovered struct {
			OwnerUserID uint `gorm:"column:owner_user_id"`
		}
		result := tx.Model(&model.BackupAssetExportJob{}).Select("owner_user_id").
			Where("id = ?", request.JobID).Limit(1).Scan(&discovered)
		if result.Error != nil {
			return fmt.Errorf("discover export job owner for sealing: %w", result.Error)
		}
		if result.RowsAffected != 1 || discovered.OwnerUserID == 0 {
			return ErrAttemptFenceLost
		}
		buckets, err := ensureAndLockQuotaBucketPairTx(tx, discovered.OwnerUserID, quotaNow)
		if err != nil {
			return err
		}
		var reloaded persistedAttemptLoad
		err = worker.loader.loadPersistedAttemptTx(ctx, tx, PersistentAttemptLoadRequest(request), quotaNow, &reloaded)
		if err != nil {
			if errors.Is(err, ErrAttemptFenceLost) {
				return ErrAttemptFenceLost
			}
			return err
		}
		reloadedSnapshot, err := persistentAttemptSnapshot(reloaded, snapshot.DEK)
		if err != nil {
			return ErrAttemptFenceLost
		}
		defer reloadedSnapshot.ClearKeyMaterial()
		reloadedPeakStoreBytes, err := persistentAttemptSnapshotPeakStoreBytes(reloadedSnapshot)
		if err != nil {
			return err
		}
		if reloadedPeakStoreBytes != expectedPeakStoreBytes || !reflect.DeepEqual(reloadedSnapshot, snapshot) {
			return ErrAttemptFenceLost
		}
		var job model.BackupAssetExportJob
		result = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND current_attempt_id = ? AND execution_state = ?", request.JobID, request.AttemptID, ExecutionRunning).
			Limit(1).Find(&job)
		if result.Error != nil {
			return fmt.Errorf("load export job for sealing: %w", result.Error)
		}
		if result.RowsAffected == 1 && job.ItemCount != int64(len(snapshot.Items)) {
			if _, err := frozenSourceRetentionCapsTx(tx, job); err != nil {
				return err
			}
		}
		if result.RowsAffected != 1 || job.OwnerUserID != discovered.OwnerUserID || !sameSealedSnapshotAuthority(job, snapshot) || cipherResult.CiphertextBytes < 0 ||
			cipherResult.CiphertextBytes > snapshot.MaxCiphertextBytes {
			return ErrAttemptFenceLost
		}
		var attempt model.BackupAssetExportAttempt
		result = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND job_id = ? AND is_current = ? AND state = ?", request.AttemptID, request.JobID, true, AttemptActive).
			Limit(1).Find(&attempt)
		if result.Error != nil {
			return fmt.Errorf("load export attempt for sealing: %w", result.Error)
		}
		if result.RowsAffected != 1 || !equalFenceToken(attempt.FenceToken, request.FenceToken) ||
			!validAttemptFenceDigest(attempt.FenceToken, attempt.FenceDigest) ||
			attempt.FenceDigest != snapshot.AttemptFenceDigest ||
			!bytes.Equal(attempt.NoncePrefix, snapshot.AttemptNoncePrefix) ||
			!bytes.Equal(attempt.NoncePrefix, cipherResult.NoncePrefix) ||
			(attempt.StagingLocator != "" && attempt.StagingLocator != locator) {
			return ErrAttemptFenceLost
		}
		lockedSources, err := lockValidatedPersistedSourceFencesTx(tx, job)
		if err != nil {
			return err
		}
		now := worker.now().UTC()
		if !now.Before(job.AbsoluteDeadline.UTC()) || !now.Before(attempt.LeaseExpiresAt.UTC()) {
			return ErrAttemptFenceLost
		}
		if _, err := validateLockedPersistedSourceFenceExpiries(lockedSources, now); err != nil {
			return err
		}
		for _, item := range snapshot.Items {
			if err := worker.metadata.RevalidateMetadataTx(ctx, tx, item.Frozen); err != nil {
				return err
			}
		}
		reports, err := validateSealedArchiveReport(snapshot, report)
		if err != nil {
			return err
		}
		finishedAt := now
		for _, item := range snapshot.Items {
			itemReport, found := reports[item.ItemID]
			if !found || (itemReport.State != ItemPacked && itemReport.State != ItemSkipped && itemReport.State != ItemFailed) {
				return ErrArchiveSource
			}
			if item.Frozen.EntryType != backupasset.CatalogEntryFile && itemReport.State == ItemFailed {
				return ErrAttemptFenceLost
			}
			var itemRow model.BackupAssetExportItem
			result = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("id = ? AND job_id = ? AND current_attempt_id = ?", item.ItemID, job.ID, attempt.ID).
				Limit(1).Find(&itemRow)
			if result.Error != nil {
				return fmt.Errorf("load export item for sealing: %w", result.Error)
			}
			if result.RowsAffected != 1 ||
				!sameSealedItemAuthority(job, itemRow, item, attempt.ID) ||
				!sameSealedItemProjectionEvidence(itemRow, item) {
				return ErrAttemptFenceLost
			}
			var itemAttempt model.BackupAssetExportItemAttempt
			result = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("id = ? AND job_id = ? AND item_id = ? AND attempt_id = ?", item.ItemAttemptID, job.ID, item.ItemID, attempt.ID).
				Limit(1).Find(&itemAttempt)
			if result.Error != nil {
				return fmt.Errorf("load export item-attempt for sealing: %w", result.Error)
			}
			if result.RowsAffected != 1 || !sameSealedItemAttemptEvidence(itemAttempt, item) {
				return ErrAttemptFenceLost
			}
			if item.Frozen.EntryType == backupasset.CatalogEntryFile && item.State == ItemRead {
				if !sameSealedReadEvidence(itemRow, itemAttempt, item) {
					return ErrAttemptFenceLost
				}
				switch itemReport.State {
				case ItemPacked:
					if itemReport.LogicalBytes != item.LogicalBytes ||
						itemReport.ProviderBytes != item.ProviderBytes || itemReport.ErrorCategory != "" {
						return ErrAttemptFenceLost
					}
				case ItemFailed:
					if itemReport.MemberPath != "" || itemReport.LogicalBytes != 0 ||
						itemReport.ProviderBytes != item.ProviderBytes ||
						!validPreHeaderFailureCategory(itemReport.ErrorCategory) {
						return ErrAttemptFenceLost
					}
				default:
					return ErrAttemptFenceLost
				}
			}
			if itemReport.State == ItemPacked && item.Frozen.EntryType == backupasset.CatalogEntryFile && item.State != ItemRead {
				return ErrAttemptFenceLost
			}
			if itemReport.State != ItemPacked && itemReport.ErrorCategory == "" {
				return ErrArchiveSource
			}
			itemAttemptUpdate := sealedItemAttemptUpdateQuery(tx, job.ID, attempt.ID, item)
			itemAttemptUpdates := map[string]any{
				"state": string(itemReport.State), "logical_bytes": itemReport.LogicalBytes,
				"provider_bytes": itemReport.ProviderBytes, "error_category": itemReport.ErrorCategory,
				"spool_locator": "", "finished_at": finishedAt, "packed_at": packedAt(itemReport.State, now),
			}
			if item.State == ItemRead && itemReport.State == ItemFailed {
				itemAttemptUpdates["spool_digest"] = ""
				itemAttemptUpdates["spool_size"] = int64(0)
				itemAttemptUpdates["read_at"] = nil
			}
			result = itemAttemptUpdate.Updates(itemAttemptUpdates)
			if result.Error != nil {
				return fmt.Errorf("seal export item-attempt: %w", result.Error)
			}
			if result.RowsAffected != 1 {
				return ErrAttemptFenceLost
			}
			itemUpdate := sealedItemProjectionUpdateQuery(tx, job.ID, attempt.ID, item)
			result = itemUpdate.
				Updates(map[string]any{
					"state": string(itemReport.State), "logical_bytes": itemReport.LogicalBytes,
					"provider_bytes": itemReport.ProviderBytes, "error_category": itemReport.ErrorCategory, "updated_at": now,
				})
			if result.Error != nil {
				return fmt.Errorf("seal export item projection: %w", result.Error)
			}
			if result.RowsAffected != 1 {
				return ErrAttemptFenceLost
			}
		}
		artifact := model.BackupAssetExportArtifact{
			ID: artifactID, JobID: job.ID, AttemptID: attempt.ID, JobKeyID: snapshot.JobKeyID,
			State: "sealed", Locator: locator, CipherVersion: 1, ChunkBytes: snapshot.ChunkBytes, FormatVersion: 1,
			NoncePrefix: append([]byte(nil), cipherResult.NoncePrefix...), ChunkCount: cipherResult.ChunkCount,
			PlaintextDigest: cipherResult.PlaintextDigest, ArchiveDigest: cipherResult.ArchiveDigest,
			CiphertextDigest: cipherResult.CiphertextDigest, PlaintextSize: cipherResult.PlaintextBytes,
			CiphertextSize: cipherResult.CiphertextBytes, SealedAt: &finishedAt, CreatedAt: now, UpdatedAt: now,
		}
		if err := tx.Create(&artifact).Error; err != nil {
			return fmt.Errorf("create sealed export artifact: %w", err)
		}
		if err := chargeSealedStoreBytesTx(tx, buckets, job, artifact.CiphertextSize, expectedPeakStoreBytes, now); err != nil {
			return err
		}
		result = tx.Model(&model.BackupAssetExportAttempt{}).
			Where("id = ? AND job_id = ? AND is_current = ? AND state = ? AND staging_locator = ?",
				attempt.ID, job.ID, true, AttemptActive, attempt.StagingLocator).
			Updates(map[string]any{
				"state": string(AttemptSealing), "staging_locator": locator,
				"checkpoint_item_count": job.ItemCount, "checkpoint_logical_bytes": report.LogicalBytes,
				"checkpoint_provider_bytes": report.ProviderBytes, "updated_at": now,
			})
		if result.Error != nil {
			return fmt.Errorf("mark export attempt sealing: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return ErrAttemptFenceLost
		}
		result = tx.Model(&model.BackupAssetExportJob{}).
			Where("id = ? AND current_attempt_id = ? AND execution_state = ? AND current_fence_revision = ? AND transition_revision = ?",
				job.ID, attempt.ID, ExecutionRunning, snapshot.CurrentFenceRevision, snapshot.TransitionRevision).
			Updates(map[string]any{
				"execution_state": string(ExecutionSealing), "result_kind": string(report.ResultKind),
				"packed_count": report.Packed, "skipped_count": report.Skipped, "failed_count": report.Failed,
				"logical_bytes": report.LogicalBytes, "provider_bytes": report.ProviderBytes,
				"artifact_bytes":      cipherResult.CiphertextBytes,
				"transition_revision": gorm.Expr("transition_revision + 1"), "updated_at": now,
			})
		if result.Error != nil {
			return fmt.Errorf("mark export job sealing: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return ErrAttemptFenceLost
		}
		return nil
	})
}

func sameSealedReadEvidence(
	itemRow model.BackupAssetExportItem,
	itemAttempt model.BackupAssetExportItemAttempt,
	snapshot PersistentAttemptItem,
) bool {
	return snapshot.State == ItemRead && snapshot.ReadAt != nil && snapshot.PackedAt == nil && snapshot.FinishedAt == nil &&
		sameSealedItemProjectionEvidence(itemRow, snapshot) && sameSealedItemAttemptEvidence(itemAttempt, snapshot)
}

func sameSealedItemProjectionEvidence(
	row model.BackupAssetExportItem,
	snapshot PersistentAttemptItem,
) bool {
	return row.ID == snapshot.ItemID && row.State == string(snapshot.State) &&
		row.LogicalBytes == snapshot.LogicalBytes && row.ProviderBytes == snapshot.ProviderBytes &&
		row.ErrorCategory == snapshot.ErrorCategory && row.UpdatedAt.UTC().Equal(snapshot.ItemUpdatedAt.UTC())
}

func sameSealedItemAttemptEvidence(
	row model.BackupAssetExportItemAttempt,
	snapshot PersistentAttemptItem,
) bool {
	return row.ID == snapshot.ItemAttemptID && row.ItemID == snapshot.ItemID && row.State == string(snapshot.State) &&
		row.SpoolDigest == snapshot.SpoolDigest && row.SpoolSize == snapshot.SpoolSize &&
		row.SpoolLocator == snapshot.SpoolLocator && row.LogicalBytes == snapshot.LogicalBytes &&
		row.ProviderBytes == snapshot.ProviderBytes && row.ErrorCategory == snapshot.ErrorCategory &&
		row.StartedAt.UTC().Equal(snapshot.StartedAt.UTC()) && sameOptionalTime(row.ReadAt, snapshot.ReadAt) &&
		sameOptionalTime(row.PackedAt, snapshot.PackedAt) && sameOptionalTime(row.FinishedAt, snapshot.FinishedAt)
}

func sealedItemAttemptUpdateQuery(
	tx *gorm.DB,
	jobID, attemptID string,
	snapshot PersistentAttemptItem,
) *gorm.DB {
	query := tx.Model(&model.BackupAssetExportItemAttempt{}).
		Where("id = ? AND job_id = ? AND item_id = ? AND attempt_id = ?", snapshot.ItemAttemptID, jobID, snapshot.ItemID, attemptID).
		Where(
			"state = ? AND spool_digest = ? AND spool_size = ? AND spool_locator = ? AND logical_bytes = ? AND provider_bytes = ? AND error_category = ? AND started_at = ?",
			snapshot.State, snapshot.SpoolDigest, snapshot.SpoolSize, snapshot.SpoolLocator,
			snapshot.LogicalBytes, snapshot.ProviderBytes, snapshot.ErrorCategory, snapshot.StartedAt.UTC(),
		)
	query = whereSnapshotOptionalTime(query, "read_at", snapshot.ReadAt)
	query = whereSnapshotOptionalTime(query, "packed_at", snapshot.PackedAt)
	return whereSnapshotOptionalTime(query, "finished_at", snapshot.FinishedAt)
}

func sealedItemProjectionUpdateQuery(
	tx *gorm.DB,
	jobID, attemptID string,
	snapshot PersistentAttemptItem,
) *gorm.DB {
	return tx.Model(&model.BackupAssetExportItem{}).
		Where("id = ? AND job_id = ? AND current_attempt_id = ?", snapshot.ItemID, jobID, attemptID).
		Where(
			"state = ? AND logical_bytes = ? AND provider_bytes = ? AND error_category = ? AND updated_at = ?",
			snapshot.State, snapshot.LogicalBytes, snapshot.ProviderBytes, snapshot.ErrorCategory, snapshot.ItemUpdatedAt.UTC(),
		)
}

func whereSnapshotOptionalTime(query *gorm.DB, column string, value *time.Time) *gorm.DB {
	if value == nil {
		return query.Where(column + " IS NULL")
	}
	return query.Where(column+" = ?", value.UTC())
}

func sameSealedSnapshotAuthority(job model.BackupAssetExportJob, snapshot PersistentAttemptSnapshot) bool {
	return job.ID == snapshot.JobID && job.SelectionDigest == snapshot.SelectionDigest &&
		job.SelectionSchemaVersion == snapshot.SelectionSchemaVersion &&
		job.ArchiveFormat == string(snapshot.ArchiveFormat) && job.ArchiveProfile == snapshot.ArchiveProfile &&
		job.LimitsSchemaVersion == snapshot.LimitsSchemaVersion && job.ChunkBytes == snapshot.ChunkBytes &&
		job.MaxItems == snapshot.MaxItems && job.MaxSourcePoints == snapshot.MaxSourcePoints &&
		job.MaxItemBytes == snapshot.MaxItemBytes &&
		job.MaxLogicalBytes == snapshot.MaxLogicalBytes && job.MaxProviderBytes == snapshot.MaxProviderBytes &&
		job.MaxCiphertextBytes == snapshot.MaxCiphertextBytes && job.MaxOpenReaders == snapshot.MaxOpenReaders &&
		job.MaxDurationSeconds == snapshot.MaxDurationSeconds && job.MaxAttempts == snapshot.MaxAttempts &&
		job.RetryBaseSeconds == snapshot.RetryBaseSeconds && job.RetryMaxDelaySeconds == snapshot.RetryMaxDelaySeconds &&
		job.LeaseTTLSeconds == snapshot.LeaseTTLSeconds && job.LeaseRenewMarginSeconds == snapshot.LeaseRenewMarginSeconds &&
		snapshot.ReadyTTL > 0 && snapshot.ReadyTTL%time.Second == 0 &&
		job.ReadyTTLSeconds == int64(snapshot.ReadyTTL/time.Second) &&
		job.AbsoluteDeadline.UTC().Equal(snapshot.AbsoluteDeadline.UTC()) &&
		job.CurrentFenceRevision == snapshot.CurrentFenceRevision &&
		job.TransitionRevision == snapshot.TransitionRevision &&
		job.ItemCount == int64(len(snapshot.Items))
}

func sameSealedItemAuthority(
	job model.BackupAssetExportJob,
	row model.BackupAssetExportItem,
	snapshot PersistentAttemptItem,
	attemptID string,
) bool {
	frozen := snapshot.Frozen
	return row.ID == snapshot.ItemID && row.JobID == job.ID && row.Ordinal == snapshot.Ordinal &&
		row.RecoveryPointID == frozen.Ref.RecoveryPointID && row.EntryID == frozen.Ref.EntryID &&
		row.CatalogGenerationID == frozen.CatalogGenerationID && row.SourceFingerprint == frozen.SourceFingerprint &&
		row.EntryFingerprint == frozen.EntryFingerprint && row.FingerprintStrength == frozen.FingerprintStrength &&
		row.ProviderCapabilityRevision == frozen.ProviderCapabilityRevision && row.EntryType == string(frozen.EntryType) &&
		row.LogicalSize == frozen.LogicalSize && row.MediaType == frozen.MediaType &&
		sameOptionalTime(row.RetentionUntil, frozen.RetentionUntil) &&
		row.SelectionRootOrdinal == frozen.SelectionRootOrdinal && row.CurrentAttemptID != nil &&
		*row.CurrentAttemptID == attemptID && sameBytes(row.PathNonce, snapshot.PathNonce) &&
		sameBytes(row.PathCiphertext, snapshot.PathCiphertext)
}

func validateSealedArchiveReport(
	snapshot PersistentAttemptSnapshot, report ArchiveReport,
) (map[string]ArchiveItemReport, error) {
	if report.SchemaVersion != 1 || !lowerHex(report.SelectionDigest, 64) ||
		report.SelectionDigest != snapshot.SelectionDigest || len(report.Items) != len(snapshot.Items) ||
		report.Packed < 0 || report.Skipped < 0 || report.Failed < 0 ||
		report.LogicalBytes < 0 || report.ProviderBytes < 0 {
		return nil, ErrArchiveSource
	}
	expected := make(map[string]PersistentAttemptItem, len(snapshot.Items))
	for _, item := range snapshot.Items {
		if backupasset.ValidateOpaqueID(item.ItemID) != nil {
			return nil, ErrAttemptFenceLost
		}
		if _, duplicate := expected[item.ItemID]; duplicate {
			return nil, ErrAttemptFenceLost
		}
		expected[item.ItemID] = item
	}

	reports := make(map[string]ArchiveItemReport, len(report.Items))
	var packed, skipped, failed, logicalBytes, providerBytes int64
	for _, itemReport := range report.Items {
		item, found := expected[itemReport.ItemID]
		if !found {
			return nil, ErrArchiveSource
		}
		if _, duplicate := reports[itemReport.ItemID]; duplicate {
			return nil, ErrArchiveSource
		}
		if itemReport.LogicalBytes < 0 || itemReport.ProviderBytes < 0 ||
			logicalBytes > snapshot.MaxLogicalBytes-itemReport.LogicalBytes ||
			providerBytes > snapshot.MaxProviderBytes-itemReport.ProviderBytes {
			return nil, ErrArchiveLimit
		}
		if err := validateSealedArchiveItemReport(item, itemReport); err != nil {
			return nil, err
		}
		switch itemReport.State {
		case ItemPacked:
			packed++
		case ItemSkipped:
			skipped++
		case ItemFailed:
			failed++
		default:
			return nil, ErrArchiveSource
		}
		logicalBytes += itemReport.LogicalBytes
		providerBytes += itemReport.ProviderBytes
		reports[itemReport.ItemID] = itemReport
	}
	if len(reports) != len(expected) || report.Packed != packed || report.Skipped != skipped || report.Failed != failed ||
		report.LogicalBytes != logicalBytes || report.ProviderBytes != providerBytes {
		return nil, ErrArchiveSource
	}
	switch report.ResultKind {
	case ResultComplete:
		if skipped != 0 || failed != 0 {
			return nil, ErrArchiveSource
		}
	case ResultPartial:
		if packed == 0 || skipped+failed == 0 {
			return nil, ErrArchiveSource
		}
	default:
		return nil, ErrArchiveSource
	}
	return reports, nil
}

func validateSealedArchiveItemReport(item PersistentAttemptItem, report ArchiveItemReport) error {
	switch item.Frozen.EntryType {
	case backupasset.CatalogEntryFile:
		switch item.State {
		case ItemRead:
			if report.State == ItemPacked && report.LogicalBytes == item.LogicalBytes &&
				report.ProviderBytes == item.ProviderBytes && report.ErrorCategory == "" &&
				!report.preHeaderSpoolRecovered {
				return nil
			}
			if report.State != ItemFailed || report.MemberPath != "" || report.LogicalBytes != 0 ||
				report.ProviderBytes != item.ProviderBytes || report.ErrorCategory != "internal_failure" {
				return ErrAttemptFenceLost
			}
			if !report.preHeaderSpoolRecovered {
				return ErrAttemptFenceLost
			}
		case ItemFailed:
			if item.LogicalBytes != 0 || !validPreHeaderFailureCategory(item.ErrorCategory) ||
				report.State != ItemFailed || report.LogicalBytes != item.LogicalBytes ||
				report.ProviderBytes != item.ProviderBytes || report.ErrorCategory != item.ErrorCategory ||
				report.preHeaderSpoolRecovered {
				return ErrAttemptFenceLost
			}
		default:
			return ErrAttemptFenceLost
		}
	case backupasset.CatalogEntryDirectory:
		if item.State != ItemPending || report.State != ItemPacked || report.LogicalBytes != 0 ||
			report.ProviderBytes != 0 || report.ErrorCategory != "" {
			return ErrAttemptFenceLost
		}
	case backupasset.CatalogEntrySymlink, backupasset.CatalogEntryHardlink:
		if item.State != ItemPending || report.State != ItemSkipped || report.LogicalBytes != 0 ||
			report.ProviderBytes != 0 || report.ErrorCategory != ItemErrorLinkMetadataUnavailable {
			return ErrAttemptFenceLost
		}
	case backupasset.CatalogEntrySpecial, backupasset.CatalogEntryUnknown:
		if item.State != ItemPending || report.State != ItemSkipped || report.LogicalBytes != 0 ||
			report.ProviderBytes != 0 || report.ErrorCategory != ItemErrorSpecialFileSkipped {
			return ErrAttemptFenceLost
		}
	default:
		return ErrArchiveSource
	}
	return nil
}

func (worker *PersistentWorker) loadAndAuthenticateSealedArtifact(
	ctx context.Context, snapshot PersistentAttemptSnapshot, artifactID string,
) (model.BackupAssetExportArtifact, error) {
	var artifact model.BackupAssetExportArtifact
	result := worker.db.WithContext(ctx).Where(
		"id = ? AND job_id = ? AND attempt_id = ? AND state = ?", artifactID, snapshot.JobID, snapshot.AttemptID, "sealed",
	).Limit(1).Find(&artifact)
	if result.Error != nil {
		return model.BackupAssetExportArtifact{}, fmt.Errorf("load sealed export artifact for authentication: %w", result.Error)
	}
	if result.RowsAffected != 1 || artifact.JobKeyID != snapshot.JobKeyID || artifact.ExpiresAt != nil ||
		artifact.CipherVersion != 1 || artifact.FormatVersion != 1 || artifact.ChunkBytes != snapshot.ChunkBytes ||
		!bytes.Equal(artifact.NoncePrefix, snapshot.AttemptNoncePrefix) {
		return model.BackupAssetExportArtifact{}, ErrAttemptFenceLost
	}
	reader, err := worker.store.OpenSealed(artifact.Locator)
	if err != nil {
		return model.BackupAssetExportArtifact{}, err
	}
	cipherResult, decryptErr := DecryptStream(ctx, io.Discard, reader, snapshot.DEK, CipherBinding{
		ExportID: snapshot.JobID, SelectionDigest: snapshot.SelectionDigest,
		ArchiveProfile: snapshot.ArchiveProfile, FormatVersion: 1,
		AttemptFenceDigest: snapshot.AttemptFenceDigest, Purpose: CipherPurposeFinalArchive,
	})
	closeErr := reader.Close()
	if decryptErr != nil || closeErr != nil {
		return model.BackupAssetExportArtifact{}, errors.Join(decryptErr, closeErr)
	}
	if cipherResult.ChunkBytes != snapshot.ChunkBytes || cipherResult.ChunkBytes != artifact.ChunkBytes ||
		cipherResult.ChunkCount != artifact.ChunkCount || cipherResult.PlaintextBytes != artifact.PlaintextSize ||
		cipherResult.CiphertextBytes != artifact.CiphertextSize || cipherResult.PlaintextDigest != artifact.PlaintextDigest ||
		cipherResult.ArchiveDigest != artifact.ArchiveDigest || cipherResult.CiphertextDigest != artifact.CiphertextDigest ||
		!bytes.Equal(cipherResult.NoncePrefix, artifact.NoncePrefix) {
		return model.BackupAssetExportArtifact{}, ErrCipherTampered
	}
	return artifact, nil
}

func sameSealedArtifact(left, right model.BackupAssetExportArtifact) bool {
	return left.ID == right.ID && left.JobID == right.JobID && left.AttemptID == right.AttemptID &&
		left.JobKeyID == right.JobKeyID && left.State == right.State && left.Locator == right.Locator &&
		left.CipherVersion == right.CipherVersion && left.ChunkBytes == right.ChunkBytes &&
		left.FormatVersion == right.FormatVersion && bytes.Equal(left.NoncePrefix, right.NoncePrefix) &&
		left.ChunkCount == right.ChunkCount && left.PlaintextDigest == right.PlaintextDigest &&
		left.ArchiveDigest == right.ArchiveDigest && left.CiphertextDigest == right.CiphertextDigest &&
		left.PlaintextSize == right.PlaintextSize && left.CiphertextSize == right.CiphertextSize
}

type ciphertextLimitWriter struct {
	writer    io.Writer
	remaining int64
}

func (writer *ciphertextLimitWriter) Write(buffer []byte) (int, error) {
	if writer == nil || writer.writer == nil || writer.remaining < int64(len(buffer)) {
		return 0, ErrArchiveLimit
	}
	written, err := writer.writer.Write(buffer)
	writer.remaining -= int64(written)
	return written, err
}

func persistentItemByID(items []PersistentAttemptItem, itemID string) (PersistentAttemptItem, bool) {
	for _, item := range items {
		if item.ItemID == itemID {
			return item, true
		}
	}
	return PersistentAttemptItem{}, false
}

type AttemptClaimRequest struct {
	JobID       string
	WorkerOwner string
}

type AttemptClaim struct {
	AttemptID           string
	SupersededAttemptID string
	AttemptNumber       int
	FenceToken          []byte
	FenceDigest         string
	NoncePrefix         []byte
	LeaseExpiresAt      time.Time
}

type AttemptCheckpoint struct {
	JobID         string
	AttemptID     string
	FenceToken    []byte
	ItemID        string
	State         ItemState
	LogicalBytes  int64
	ProviderBytes int64
	ErrorCategory string
	// PreHeaderSpoolRecovered is set only after SealArchive authenticated and
	// purged a durable read spool before creating an archive member header.
	PreHeaderSpoolRecovered bool
}

type AttemptHeartbeatRequest struct {
	JobID      string
	AttemptID  string
	FenceToken []byte
}

type AttemptHeartbeatResult struct {
	LeaseExpiresAt         time.Time
	SourceLeaseExpiresAt   []time.Time
	SourceAbsoluteDeadline time.Time
}

type AttemptFailureRequest struct {
	JobID      string
	AttemptID  string
	FenceToken []byte
	Category   string
	Retryable  bool
}

type AttemptFailureResult struct {
	ExecutionState ExecutionState
	RetryAt        *time.Time
}

type SourceLeaseTakeoverRequest struct {
	JobID string
}

type sourceLeaseTakeoverAttempt struct {
	attemptID   string
	fenceToken  []byte
	fenceDigest string
}

type SourceLeaseTakeoverResult struct {
	LeaseExpiresAt   []time.Time
	AbsoluteDeadline time.Time
}

type SourceLeaseMaintenanceRequest struct {
	JobID          string
	ReadyIntegrity *ReadyIntegrityToken
}

type SourceLeaseMaintenanceResult struct {
	LeaseExpiresAt   []time.Time
	AbsoluteDeadline time.Time
	TakenOver        bool
}

func NewAttemptCoordinator(db *gorm.DB, now func() time.Time, sourceLeases ...SourceLeaseCoordinator) (*AttemptCoordinator, error) {
	return newAttemptCoordinator(db, now, nil, sourceLeases...)
}

func NewAttemptCoordinatorWithWorkerCapacity(
	db *gorm.DB,
	now func() time.Time,
	limits WorkerCapacityLimits,
	sourceLeases ...SourceLeaseCoordinator,
) (*AttemptCoordinator, error) {
	if !validWorkerCapacityLimits(limits) {
		return nil, ErrUnavailable
	}
	return newAttemptCoordinator(db, now, &limits, sourceLeases...)
}

func newAttemptCoordinator(
	db *gorm.DB,
	now func() time.Time,
	workerCapacity *WorkerCapacityLimits,
	sourceLeases ...SourceLeaseCoordinator,
) (*AttemptCoordinator, error) {
	if db == nil || len(sourceLeases) > 1 || len(sourceLeases) == 1 && sourceLeases[0] == nil {
		return nil, ErrUnavailable
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	var coordinator SourceLeaseCoordinator
	if len(sourceLeases) == 1 {
		coordinator = sourceLeases[0]
	}
	return &AttemptCoordinator{db: db, now: now, sourceLeases: coordinator, workerCapacity: workerCapacity}, nil
}

func (coordinator *AttemptCoordinator) MaintainSourceLeases(
	ctx context.Context,
	request SourceLeaseMaintenanceRequest,
) (response SourceLeaseMaintenanceResult, resultErr error) {
	if coordinator == nil || coordinator.sourceLeases == nil || backupasset.ValidateOpaqueID(request.JobID) != nil {
		return SourceLeaseMaintenanceResult{}, ErrAttemptFenceLost
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var releaseReadyArtifact func() error
	if request.ReadyIntegrity != nil {
		var err error
		releaseReadyArtifact, err = request.ReadyIntegrity.consumeAndPin(ctx, request.JobID)
		if err != nil {
			return SourceLeaseMaintenanceResult{}, err
		}
		defer func() {
			if releaseReadyArtifact == nil {
				return
			}
			releaseErr := releaseReadyArtifact()
			releaseReadyArtifact = nil
			if releaseErr != nil {
				releaseErr = errors.Join(ErrUnavailable, fmt.Errorf("release pinned ready artifact: %w", releaseErr))
			}
			if recovered := recover(); recovered != nil {
				if releaseErr == nil {
					panic(recovered)
				}
				if panicErr, ok := recovered.(error); ok {
					panic(errors.Join(panicErr, releaseErr))
				}
				panic(errors.Join(fmt.Errorf("source maintenance panic: %v", recovered), releaseErr))
			}
			resultErr = errors.Join(resultErr, releaseErr)
		}()
	}
	resultErr = database.WithSQLiteBusyRetryTx(ctx, coordinator.db, func(tx *gorm.DB) error {
		now := coordinator.now().UTC()
		var job model.BackupAssetExportJob
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", request.JobID).Limit(1).Find(&job)
		if result.Error != nil {
			return fmt.Errorf("load export job for source maintenance: %w", result.Error)
		}
		if result.RowsAffected != 1 || !sourceHeartbeatState(ExecutionState(job.ExecutionState)) {
			return ErrAttemptFenceLost
		}
		ready := ExecutionState(job.ExecutionState) == ExecutionReady
		if request.ReadyIntegrity != nil && !ready {
			return ErrAttemptFenceLost
		}
		if ready {
			if err := readyMaintenanceShapeError(job, now); err != nil {
				return err
			}
		} else if err := sourceMaintenanceDeadlineError(job, now); err != nil {
			return err
		}
		sources, err := loadPersistedSourceLeasesTx(tx, job.ID)
		if err != nil {
			return err
		}
		if ready {
			attempt, artifact, key, err := loadReadyIntegrityRowsTx(tx, job)
			if err != nil {
				return err
			}
			if err := validateReadyIntegrityRows(job, attempt, artifact, key); err != nil {
				return err
			}
			if !readyIntegrityTokenMatches(request.ReadyIntegrity, job, attempt, artifact, key, sources) {
				return ErrAttemptFenceLost
			}
			if err := validateReadySourceMaintenanceExpiryTx(tx, job, sources, now); err != nil {
				return err
			}
			if !now.Before(job.ExpiresAt.UTC()) {
				return ErrReadyExpired
			}
		}
		response.LeaseExpiresAt = make([]time.Time, 0, len(sources))
		for _, source := range sources {
			leaseRow, err := loadSourceLeaseIdentityTx(tx, job, source, now)
			if err != nil {
				return err
			}
			var maintained backupasset.Lease
			if now.Before(leaseRow.LeaseExpiresAt.UTC()) {
				maintained, err = coordinator.sourceLeases.RenewTx(ctx, tx, backupasset.LeaseFence{
					LeaseID: leaseRow.ID, RecoveryPointID: leaseRow.RecoveryPointID,
					HolderType: backupasset.LeaseHolderType(leaseRow.HolderType), OwnerID: leaseRow.OwnerID,
					AttemptID: leaseRow.AttemptID, FenceToken: leaseRow.FenceToken,
				})
			} else {
				maintained, err = coordinator.sourceLeases.TakeoverTx(ctx, tx, backupasset.TakeoverLeaseRequest{
					LeaseID: source.LeaseID, OwnerID: job.ID,
				})
				response.TakenOver = true
			}
			if err != nil {
				if errors.Is(err, backupasset.ErrLeaseDeadlineExceeded) {
					return fmt.Errorf("%w: maintain Foundation source lease: %w", ErrSourceDeadlineReached, err)
				}
				return fmt.Errorf("maintain Foundation source lease: %w", err)
			}
			if maintained.ID != source.LeaseID || maintained.RecoveryPointID != source.RecoveryPointID ||
				maintained.HolderType != backupasset.LeaseHolderExportJob || maintained.OwnerID != job.ID ||
				!maintained.AbsoluteDeadline.UTC().Equal(source.AbsoluteDeadline.UTC()) {
				return ErrAttemptFenceLost
			}
			updates := map[string]any{
				"renewed_at": maintained.LastHeartbeatAt.UTC(), "updated_at": maintained.LastHeartbeatAt.UTC(),
			}
			if maintained.Fence.AttemptID != source.LeaseAttemptID {
				digest := sha256.Sum256([]byte(maintained.Fence.FenceToken))
				updates["lease_attempt_id"] = maintained.Fence.AttemptID
				updates["fence_hash"] = hex.EncodeToString(digest[:])
			}
			result = tx.Model(&model.BackupAssetExportSourceLease{}).
				Where("id = ? AND job_id = ? AND state = ? AND lease_attempt_id = ? AND fence_hash = ? AND absolute_deadline = ?",
					source.ID, job.ID, "active", source.LeaseAttemptID, source.FenceHash, source.AbsoluteDeadline).
				Updates(updates)
			if result.Error != nil || result.RowsAffected != 1 {
				return ErrAttemptFenceLost
			}
			response.LeaseExpiresAt = append(response.LeaseExpiresAt, maintained.LeaseExpiresAt.UTC())
			if response.AbsoluteDeadline.IsZero() || source.AbsoluteDeadline.UTC().Before(response.AbsoluteDeadline) {
				response.AbsoluteDeadline = source.AbsoluteDeadline.UTC()
			}
		}
		return nil
	})
	return response, resultErr
}

func loadReadyIntegrityRowsTx(
	tx *gorm.DB,
	job model.BackupAssetExportJob,
) (model.BackupAssetExportAttempt, model.BackupAssetExportArtifact, model.BackupAssetExportKey, error) {
	if job.CurrentAttemptID == nil || backupasset.ValidateOpaqueID(*job.CurrentAttemptID) != nil {
		return model.BackupAssetExportAttempt{}, model.BackupAssetExportArtifact{}, model.BackupAssetExportKey{}, ErrAttemptFenceLost
	}
	var attempt model.BackupAssetExportAttempt
	result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND job_id = ?", *job.CurrentAttemptID, job.ID).
		Limit(1).Find(&attempt)
	if result.Error != nil {
		return model.BackupAssetExportAttempt{}, model.BackupAssetExportArtifact{}, model.BackupAssetExportKey{}, fmt.Errorf("load ready Export attempt for source maintenance: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return model.BackupAssetExportAttempt{}, model.BackupAssetExportArtifact{}, model.BackupAssetExportKey{}, ErrAttemptFenceLost
	}
	var artifacts []model.BackupAssetExportArtifact
	result = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("job_id = ?", job.ID).
		Order("id ASC").Limit(2).Find(&artifacts)
	if result.Error != nil {
		return model.BackupAssetExportAttempt{}, model.BackupAssetExportArtifact{}, model.BackupAssetExportKey{}, fmt.Errorf("load ready Export artifact for source maintenance: %w", result.Error)
	}
	if len(artifacts) != 1 {
		return model.BackupAssetExportAttempt{}, model.BackupAssetExportArtifact{}, model.BackupAssetExportKey{}, ErrAttemptFenceLost
	}
	var keys []model.BackupAssetExportKey
	result = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("job_id = ?", job.ID).
		Order("id ASC").Limit(2).Find(&keys)
	if result.Error != nil {
		return model.BackupAssetExportAttempt{}, model.BackupAssetExportArtifact{}, model.BackupAssetExportKey{}, fmt.Errorf("load ready Export key for source maintenance: %w", result.Error)
	}
	if len(keys) != 1 {
		return model.BackupAssetExportAttempt{}, model.BackupAssetExportArtifact{}, model.BackupAssetExportKey{}, ErrAttemptFenceLost
	}
	return attempt, artifacts[0], keys[0], nil
}

func validateReadyIntegrityRows(
	job model.BackupAssetExportJob,
	attempt model.BackupAssetExportAttempt,
	artifact model.BackupAssetExportArtifact,
	key model.BackupAssetExportKey,
) error {
	if job.ExecutionState != string(ExecutionReady) || job.CurrentAttemptID == nil ||
		*job.CurrentAttemptID != attempt.ID || attempt.JobID != job.ID ||
		attempt.State != string(AttemptSealed) || attempt.IsCurrent || attempt.FinishedAt == nil ||
		job.ReadyAt == nil || !attempt.FinishedAt.Equal(job.ReadyAt.UTC()) || len(attempt.FenceToken) != 32 ||
		len(attempt.NoncePrefix) != 8 {
		return ErrAttemptFenceLost
	}
	fenceDigest := sha256.Sum256(attempt.FenceToken)
	if hex.EncodeToString(fenceDigest[:]) != attempt.FenceDigest {
		return ErrAttemptFenceLost
	}
	if artifact.JobID != job.ID || artifact.AttemptID != attempt.ID || artifact.State != "sealed" ||
		artifact.JobKeyID != key.ID || artifact.ExpiresAt == nil || job.ExpiresAt == nil ||
		!artifact.ExpiresAt.Equal(job.ExpiresAt.UTC()) || artifact.PurgedAt != nil || artifact.PurgeError != "" ||
		artifact.SealedAt == nil || !validStoreLocator(artifact.Locator) || !strings.HasSuffix(artifact.Locator, ".xre") ||
		artifact.CipherVersion != 1 || artifact.FormatVersion != 1 || artifact.ChunkBytes <= 0 ||
		artifact.ChunkCount <= 0 || artifact.CiphertextSize <= 0 || len(artifact.NoncePrefix) != 8 ||
		!bytes.Equal(artifact.NoncePrefix, attempt.NoncePrefix) {
		return ErrAttemptFenceLost
	}
	if key.JobID != job.ID || key.ID != artifact.JobKeyID || key.State != "active" || key.KeyRevision <= 0 ||
		key.KEKVersion <= 0 || key.WrapAlgorithm != JobKeyWrapAlgorithmV1 || len(key.EnvelopeNonce) != 12 ||
		len(key.WrappedDEK) == 0 || key.DestroyedAt != nil {
		return ErrAttemptFenceLost
	}
	return nil
}

func validateReadySourceMaintenanceExpiryTx(
	tx *gorm.DB,
	job model.BackupAssetExportJob,
	sources []model.BackupAssetExportSourceLease,
	now time.Time,
) error {
	if job.ReadyAt == nil || job.ExpiresAt == nil || job.ReadyTTLSeconds <= 0 ||
		job.ReadyTTLSeconds > math.MaxInt64/int64(time.Second) {
		return ErrUnavailable
	}
	deadlines := make([]SourceDeadline, 0, len(sources))
	for _, source := range sources {
		if _, err := loadSourceLeaseIdentityTx(tx, job, source, now); err != nil {
			return err
		}
		deadlines = append(deadlines, SourceDeadline{
			AbsoluteDeadline: source.AbsoluteDeadline,
			RetentionUntil:   source.RetentionUntil,
		})
	}
	expectedExpiry, err := ComputeReadyExpiry(
		job.ReadyAt.UTC(), time.Duration(job.ReadyTTLSeconds)*time.Second, deadlines,
	)
	if err != nil || !expectedExpiry.Equal(job.ExpiresAt.UTC()) {
		return ErrUnavailable
	}
	var artifacts []model.BackupAssetExportArtifact
	result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("job_id = ?", job.ID).
		Order("id ASC").Limit(2).Find(&artifacts)
	if result.Error != nil {
		return fmt.Errorf("load ready Export artifact for source maintenance: %w", result.Error)
	}
	if len(artifacts) != 1 || artifacts[0].State != "sealed" || artifacts[0].PurgedAt != nil ||
		artifacts[0].ExpiresAt == nil || !expectedExpiry.Equal(artifacts[0].ExpiresAt.UTC()) {
		return ErrUnavailable
	}
	return nil
}

func (coordinator *AttemptCoordinator) TakeoverSourceLeases(
	ctx context.Context,
	request SourceLeaseTakeoverRequest,
) (SourceLeaseTakeoverResult, error) {
	return coordinator.takeoverSourceLeases(ctx, request, nil)
}

func (coordinator *AttemptCoordinator) takeoverSourceLeasesForSealingAttempt(
	ctx context.Context,
	jobID string,
	expected sourceLeaseTakeoverAttempt,
) (SourceLeaseTakeoverResult, error) {
	return coordinator.takeoverSourceLeases(ctx, SourceLeaseTakeoverRequest{JobID: jobID}, &expected)
}

func (coordinator *AttemptCoordinator) takeoverSourceLeases(
	ctx context.Context,
	request SourceLeaseTakeoverRequest,
	expected *sourceLeaseTakeoverAttempt,
) (SourceLeaseTakeoverResult, error) {
	if coordinator == nil || coordinator.sourceLeases == nil || backupasset.ValidateOpaqueID(request.JobID) != nil {
		return SourceLeaseTakeoverResult{}, ErrAttemptFenceLost
	}
	now := coordinator.now().UTC()
	var response SourceLeaseTakeoverResult
	err := coordinator.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var job model.BackupAssetExportJob
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", request.JobID).Limit(1).Find(&job)
		if result.Error != nil {
			return fmt.Errorf("load export job for source takeover: %w", result.Error)
		}
		if result.RowsAffected != 1 || ExecutionState(job.ExecutionState) == ExecutionReady ||
			!sourceHeartbeatState(ExecutionState(job.ExecutionState)) {
			return ErrAttemptFenceLost
		}
		if err := sourceMaintenanceDeadlineError(job, now); err != nil {
			return err
		}
		if err := validateExpectedSealingTakeoverAttemptTx(tx, job, expected); err != nil {
			return err
		}
		sources, err := loadPersistedSourceLeasesTx(tx, job.ID)
		if err != nil {
			return err
		}
		response.LeaseExpiresAt = make([]time.Time, 0, len(sources))
		takenOver := false
		for _, source := range sources {
			lease, err := loadSourceLeaseIdentityTx(tx, job, source, now)
			if err != nil {
				return err
			}
			if now.Before(lease.LeaseExpiresAt.UTC()) {
				response.LeaseExpiresAt = append(response.LeaseExpiresAt, lease.LeaseExpiresAt.UTC())
				if response.AbsoluteDeadline.IsZero() || source.AbsoluteDeadline.UTC().Before(response.AbsoluteDeadline) {
					response.AbsoluteDeadline = source.AbsoluteDeadline.UTC()
				}
				continue
			}
			taken, err := coordinator.sourceLeases.TakeoverTx(ctx, tx, backupasset.TakeoverLeaseRequest{
				LeaseID: source.LeaseID, OwnerID: job.ID,
			})
			if err != nil {
				if errors.Is(err, backupasset.ErrLeaseDeadlineExceeded) {
					return fmt.Errorf("%w: take over Foundation source lease: %w", ErrSourceDeadlineReached, err)
				}
				return fmt.Errorf("take over Foundation source lease: %w", err)
			}
			if !taken.AbsoluteDeadline.UTC().Equal(source.AbsoluteDeadline.UTC()) ||
				taken.RecoveryPointID != source.RecoveryPointID || taken.OwnerID != job.ID ||
				taken.HolderType != backupasset.LeaseHolderExportJob {
				return ErrAttemptFenceLost
			}
			digest := sha256.Sum256([]byte(taken.Fence.FenceToken))
			result = tx.Model(&model.BackupAssetExportSourceLease{}).
				Where("id = ? AND job_id = ? AND state = ? AND lease_attempt_id = ? AND fence_hash = ? AND absolute_deadline = ?",
					source.ID, job.ID, "active", source.LeaseAttemptID, source.FenceHash, source.AbsoluteDeadline).
				Updates(map[string]any{
					"lease_attempt_id": taken.Fence.AttemptID, "fence_hash": hex.EncodeToString(digest[:]),
					"renewed_at": taken.LastHeartbeatAt.UTC(), "updated_at": taken.LastHeartbeatAt.UTC(),
				})
			if result.Error != nil || result.RowsAffected != 1 {
				return ErrAttemptFenceLost
			}
			takenOver = true
			response.LeaseExpiresAt = append(response.LeaseExpiresAt, taken.LeaseExpiresAt.UTC())
			if response.AbsoluteDeadline.IsZero() || source.AbsoluteDeadline.UTC().Before(response.AbsoluteDeadline) {
				response.AbsoluteDeadline = source.AbsoluteDeadline.UTC()
			}
		}
		if !takenOver {
			return ErrAttemptFenceLost
		}
		return nil
	})
	return response, err
}

func validateExpectedSealingTakeoverAttemptTx(
	tx *gorm.DB,
	job model.BackupAssetExportJob,
	expected *sourceLeaseTakeoverAttempt,
) error {
	if expected == nil {
		return nil
	}
	if backupasset.ValidateOpaqueID(expected.attemptID) != nil ||
		!validAttemptFenceDigest(expected.fenceToken, expected.fenceDigest) ||
		job.CurrentAttemptID == nil || *job.CurrentAttemptID != expected.attemptID {
		return ErrAttemptFenceLost
	}
	var attempt model.BackupAssetExportAttempt
	result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND job_id = ? AND is_current = ? AND state = ?", expected.attemptID, job.ID, true, AttemptSealing).
		Limit(1).Find(&attempt)
	if result.Error != nil {
		return fmt.Errorf("load sealing export attempt for source takeover: %w", result.Error)
	}
	if result.RowsAffected != 1 || !equalFenceToken(attempt.FenceToken, expected.fenceToken) ||
		!validAttemptFenceDigest(attempt.FenceToken, attempt.FenceDigest) || attempt.FenceDigest != expected.fenceDigest {
		return ErrAttemptFenceLost
	}
	return nil
}

func (coordinator *AttemptCoordinator) Claim(ctx context.Context, request AttemptClaimRequest) (AttemptClaim, error) {
	if coordinator == nil || backupasset.ValidateOpaqueID(request.JobID) != nil ||
		strings.TrimSpace(request.WorkerOwner) == "" || len(request.WorkerOwner) > 128 {
		return AttemptClaim{}, ErrAttemptNotClaimable
	}
	var claim AttemptClaim
	err := database.WithSQLiteBusyRetryTx(ctx, coordinator.db, func(tx *gorm.DB) error {
		now := coordinator.now().UTC()
		var buckets quotaBucketPair
		if coordinator.workerCapacity != nil {
			var discovered struct {
				OwnerUserID uint `gorm:"column:owner_user_id"`
			}
			result := tx.Model(&model.BackupAssetExportJob{}).Select("owner_user_id").
				Where("id = ?", request.JobID).Limit(1).Scan(&discovered)
			if result.Error != nil {
				return fmt.Errorf("discover export job owner for worker claim: %w", result.Error)
			}
			if result.RowsAffected != 1 || discovered.OwnerUserID == 0 {
				return ErrAttemptNotClaimable
			}
			var err error
			buckets, err = ensureAndLockQuotaBucketPairTx(tx, discovered.OwnerUserID, now)
			if err != nil {
				return err
			}
		}
		var job model.BackupAssetExportJob
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", request.JobID).Limit(1).Find(&job)
		if result.Error != nil {
			return fmt.Errorf("load export job for claim: %w", result.Error)
		}
		if result.RowsAffected != 1 || !attemptClaimState(ExecutionState(job.ExecutionState)) ||
			!now.Before(job.AbsoluteDeadline.UTC()) {
			return ErrAttemptNotClaimable
		}
		safeWindow := time.Duration(job.LeaseTTLSeconds+job.LeaseRenewMarginSeconds) * time.Second
		if safeWindow <= 0 || !job.AbsoluteDeadline.UTC().After(now.Add(safeWindow)) {
			return ErrDeadlineUnsafe
		}
		if err := validatePersistedSourceFencesTx(tx, job, now); err != nil {
			return err
		}

		var previous *model.BackupAssetExportAttempt
		var previousWorkerReservations []model.BackupAssetExportReservation
		if job.CurrentAttemptID != nil {
			var current model.BackupAssetExportAttempt
			result = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("id = ? AND job_id = ? AND is_current = ?", *job.CurrentAttemptID, job.ID, true).
				Limit(1).Find(&current)
			if result.Error != nil || result.RowsAffected != 1 {
				return ErrAttemptFenceLost
			}
			if now.Before(current.LeaseExpiresAt.UTC()) {
				return ErrAttemptNotClaimable
			}
			previous = &current
			if coordinator.workerCapacity != nil {
				var pairErr error
				previousWorkerReservations, pairErr = lockAttemptWorkerReservationPairTx(tx, buckets, job, current)
				if pairErr != nil {
					return pairErr
				}
			}
		}

		var attemptCount int64
		if err := tx.Model(&model.BackupAssetExportAttempt{}).Where("job_id = ?", job.ID).Count(&attemptCount).Error; err != nil {
			return fmt.Errorf("count export attempts: %w", err)
		}
		if ExecutionState(job.ExecutionState) == ExecutionRetryWait {
			retryAt := job.UpdatedAt.UTC().Add(attemptRetryDelay(job, attemptCount))
			if now.Before(retryAt) {
				return ErrAttemptNotClaimable
			}
		}
		if attemptCount >= int64(job.MaxAttempts) {
			return ErrAttemptNotClaimable
		}
		attemptID, err := backupasset.NewOpaqueID()
		if err != nil {
			return err
		}
		fenceToken := make([]byte, 32)
		noncePrefix := make([]byte, 8)
		if _, err := io.ReadFull(rand.Reader, fenceToken); err != nil {
			return err
		}
		if _, err := io.ReadFull(rand.Reader, noncePrefix); err != nil {
			return err
		}
		fenceHash := sha256.Sum256(fenceToken)
		claimTTL := time.Duration(job.LeaseTTLSeconds) * time.Second
		if claimTTL <= 0 {
			return ErrDeadlineUnsafe
		}
		leaseExpiresAt := now.Add(claimTTL)
		if leaseExpiresAt.After(job.AbsoluteDeadline.UTC()) {
			leaseExpiresAt = job.AbsoluteDeadline.UTC()
		}

		if previous != nil {
			finishedAt := now
			result = tx.Model(&model.BackupAssetExportAttempt{}).
				Where("id = ? AND job_id = ? AND is_current = ? AND lease_expires_at <= ?", previous.ID, job.ID, true, now).
				Updates(map[string]any{"state": string(AttemptSuperseded), "is_current": false, "finished_at": finishedAt, "updated_at": now})
			if result.Error != nil || result.RowsAffected != 1 {
				return ErrAttemptFenceLost
			}
			if coordinator.workerCapacity != nil {
				if err := releaseAttemptWorkerReservationPairTx(tx, previousWorkerReservations, *previous, now); err != nil {
					return err
				}
			}
		}

		attempt := model.BackupAssetExportAttempt{
			ID: attemptID, JobID: job.ID, AttemptNumber: int(attemptCount) + 1, WorkerOwner: request.WorkerOwner,
			State: string(AttemptActive), FenceToken: fenceToken, FenceDigest: hex.EncodeToString(fenceHash[:]),
			NoncePrefix: noncePrefix, LeaseExpiresAt: leaseExpiresAt, IsCurrent: true, StartedAt: now, CreatedAt: now, UpdatedAt: now,
		}
		if err := tx.Create(&attempt).Error; err != nil {
			return fmt.Errorf("create export attempt: %w", err)
		}
		if coordinator.workerCapacity != nil {
			if err := reserveAttemptWorkerCapacityTx(tx, buckets, job, attempt, *coordinator.workerCapacity, now); err != nil {
				return err
			}
		}
		if err := tx.Model(&model.BackupAssetExportItem{}).Where("job_id = ?", job.ID).
			Updates(map[string]any{
				"state": string(ItemPending), "current_attempt_id": attemptID, "logical_bytes": 0,
				"provider_bytes": 0, "error_category": "", "updated_at": now,
			}).Error; err != nil {
			return fmt.Errorf("reset export item projections: %w", err)
		}
		var items []model.BackupAssetExportItem
		if err := tx.Where("job_id = ?", job.ID).Order("ordinal ASC").Find(&items).Error; err != nil {
			return fmt.Errorf("load export items for attempt: %w", err)
		}
		for _, item := range items {
			itemAttemptID, err := backupasset.NewOpaqueID()
			if err != nil {
				return err
			}
			row := model.BackupAssetExportItemAttempt{
				ID: itemAttemptID, JobID: job.ID, ItemID: item.ID, AttemptID: attemptID, State: string(ItemPending),
				StartedAt: now, CreatedAt: now,
			}
			if err := tx.Create(&row).Error; err != nil {
				return fmt.Errorf("create export item attempt: %w", err)
			}
		}
		result = tx.Model(&model.BackupAssetExportJob{}).
			Where("id = ? AND transition_revision = ?", job.ID, job.TransitionRevision).
			Updates(map[string]any{
				"execution_state": string(ExecutionRunning), "current_attempt_id": attemptID,
				"current_fence_revision": job.CurrentFenceRevision + 1, "packed_count": 0, "skipped_count": 0,
				"failed_count": 0, "logical_bytes": 0, "provider_bytes": 0, "artifact_bytes": 0,
				"result_kind": "", "error_category": "",
				"transition_revision": gorm.Expr("transition_revision + 1"), "updated_at": now,
			})
		if result.Error != nil || result.RowsAffected != 1 {
			return ErrAttemptFenceLost
		}
		candidate := AttemptClaim{
			AttemptID: attempt.ID, AttemptNumber: attempt.AttemptNumber, FenceToken: append([]byte(nil), fenceToken...),
			FenceDigest: attempt.FenceDigest, NoncePrefix: append([]byte(nil), noncePrefix...), LeaseExpiresAt: leaseExpiresAt,
		}
		if previous != nil {
			candidate.SupersededAttemptID = previous.ID
		}
		claim = candidate
		return nil
	})
	if err != nil {
		return AttemptClaim{}, err
	}
	return claim, nil
}

func (coordinator *AttemptCoordinator) Fail(
	ctx context.Context,
	request AttemptFailureRequest,
) (AttemptFailureResult, error) {
	if coordinator == nil || backupasset.ValidateOpaqueID(request.JobID) != nil ||
		backupasset.ValidateOpaqueID(request.AttemptID) != nil || len(request.FenceToken) != 32 ||
		!validAttemptFailureCategory(request.Category) {
		return AttemptFailureResult{}, ErrAttemptFenceLost
	}
	now := coordinator.now().UTC()
	var response AttemptFailureResult
	err := coordinator.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var buckets quotaBucketPair
		if coordinator.workerCapacity != nil {
			var discovered struct {
				OwnerUserID uint `gorm:"column:owner_user_id"`
			}
			result := tx.Model(&model.BackupAssetExportJob{}).Select("owner_user_id").
				Where("id = ?", request.JobID).Limit(1).Scan(&discovered)
			if result.Error != nil {
				return fmt.Errorf("discover export job owner for worker failure: %w", result.Error)
			}
			if result.RowsAffected != 1 || discovered.OwnerUserID == 0 {
				return ErrAttemptFenceLost
			}
			var err error
			buckets, err = ensureAndLockQuotaBucketPairTx(tx, discovered.OwnerUserID, now)
			if err != nil {
				return err
			}
		}
		var job model.BackupAssetExportJob
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND current_attempt_id = ? AND execution_state IN ?", request.JobID, request.AttemptID,
				[]string{string(ExecutionRunning), string(ExecutionSealing)}).
			Limit(1).Find(&job)
		if result.Error != nil || result.RowsAffected != 1 {
			return ErrAttemptFenceLost
		}
		var attempt model.BackupAssetExportAttempt
		result = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND job_id = ? AND is_current = ? AND state IN ?", request.AttemptID, request.JobID, true,
				[]string{string(AttemptActive), string(AttemptSealing)}).
			Limit(1).Find(&attempt)
		if result.Error != nil || result.RowsAffected != 1 || !equalFenceToken(attempt.FenceToken, request.FenceToken) {
			return ErrAttemptFenceLost
		}
		var workerReservations []model.BackupAssetExportReservation
		if coordinator.workerCapacity != nil {
			var pairErr error
			workerReservations, pairErr = lockAttemptWorkerReservationPairTx(tx, buckets, job, attempt)
			if pairErr != nil {
				return pairErr
			}
		}
		finishedAt := now
		if err := tx.Model(&model.BackupAssetExportItemAttempt{}).
			Where("job_id = ? AND attempt_id = ? AND state IN ?", job.ID, attempt.ID,
				[]string{string(ItemPending), string(ItemRead)}).
			Updates(map[string]any{
				"state": string(ItemFailed), "error_category": request.Category, "finished_at": finishedAt,
			}).Error; err != nil {
			return fmt.Errorf("fail unfinished export item attempts: %w", err)
		}
		result = tx.Model(&model.BackupAssetExportAttempt{}).
			Where("id = ? AND job_id = ? AND is_current = ?", attempt.ID, job.ID, true).
			Updates(map[string]any{
				"state": string(AttemptFailed), "is_current": false, "failure_category": request.Category,
				"finished_at": finishedAt, "updated_at": now,
			})
		if result.Error != nil || result.RowsAffected != 1 {
			return ErrAttemptFenceLost
		}
		if coordinator.workerCapacity != nil {
			if err := releaseAttemptWorkerReservationPairTx(tx, workerReservations, attempt, now); err != nil {
				return err
			}
		}

		nextState := ExecutionFailed
		if request.Retryable &&
			(ExecutionState(job.ExecutionState) == ExecutionRunning || ExecutionState(job.ExecutionState) == ExecutionSealing) &&
			attempt.AttemptNumber < job.MaxAttempts {
			retryAt := now.Add(attemptRetryDelay(job, int64(attempt.AttemptNumber)))
			safeWindow := time.Duration(job.LeaseTTLSeconds+job.LeaseRenewMarginSeconds) * time.Second
			if safeWindow > 0 && job.AbsoluteDeadline.UTC().After(retryAt.Add(safeWindow)) {
				nextState = ExecutionRetryWait
				response.RetryAt = &retryAt
			}
		}
		if ValidateExecutionTransition(ExecutionState(job.ExecutionState), nextState) != nil {
			return ErrInvalidTransition
		}
		result = tx.Model(&model.BackupAssetExportJob{}).
			Where("id = ? AND current_attempt_id = ? AND transition_revision = ?", job.ID, attempt.ID, job.TransitionRevision).
			Updates(map[string]any{
				"execution_state": string(nextState), "current_attempt_id": nil,
				"current_fence_revision": job.CurrentFenceRevision + 1, "error_category": request.Category,
				"transition_revision": gorm.Expr("transition_revision + 1"), "updated_at": now,
			})
		if result.Error != nil || result.RowsAffected != 1 {
			return ErrAttemptFenceLost
		}
		response.ExecutionState = nextState
		return nil
	})
	return response, err
}

func (coordinator *AttemptCoordinator) ReconcileExpiredWorkerReservations(ctx context.Context, limit int) (int, error) {
	if coordinator == nil || coordinator.workerCapacity == nil || limit <= 0 || limit > 1000 {
		return 0, ErrUnavailable
	}
	now := coordinator.now().UTC()
	type candidate struct {
		JobID       string `gorm:"column:job_id"`
		AttemptID   string `gorm:"column:attempt_id"`
		OwnerUserID uint   `gorm:"column:owner_user_id"`
	}
	var candidates []candidate
	if err := coordinator.db.WithContext(ctx).Table("backup_asset_export_reservations AS reservation").
		Select("reservation.job_id, reservation.attempt_id, job.owner_user_id").
		Joins("JOIN backup_asset_export_jobs AS job ON job.id = reservation.job_id").
		Where("reservation.kind = ? AND reservation.state = ? AND reservation.lease_expires_at <= ?", "worker", "active", now).
		Order("reservation.lease_expires_at ASC, reservation.attempt_id ASC, reservation.id ASC").Limit(limit * 2).Scan(&candidates).Error; err != nil {
		return 0, fmt.Errorf("discover expired export worker reservations: %w", err)
	}
	processed := 0
	seen := make(map[string]struct{}, limit)
	var reconcileErr error
	for _, candidate := range candidates {
		if processed >= limit {
			break
		}
		if _, duplicate := seen[candidate.AttemptID]; duplicate {
			continue
		}
		seen[candidate.AttemptID] = struct{}{}
		if err := coordinator.reconcileExpiredWorkerAttempt(ctx, candidate, now); err != nil {
			reconcileErr = errors.Join(reconcileErr, err)
			continue
		}
		processed++
	}
	return processed, reconcileErr
}

func (coordinator *AttemptCoordinator) reconcileExpiredWorkerAttempt(
	ctx context.Context,
	candidate struct {
		JobID       string `gorm:"column:job_id"`
		AttemptID   string `gorm:"column:attempt_id"`
		OwnerUserID uint   `gorm:"column:owner_user_id"`
	},
	now time.Time,
) error {
	if backupasset.ValidateOpaqueID(candidate.JobID) != nil || backupasset.ValidateOpaqueID(candidate.AttemptID) != nil || candidate.OwnerUserID == 0 {
		return ErrAttemptFenceLost
	}
	return coordinator.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		buckets, err := ensureAndLockQuotaBucketPairTx(tx, candidate.OwnerUserID, now)
		if err != nil {
			return err
		}
		var job model.BackupAssetExportJob
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND owner_user_id = ? AND current_attempt_id = ? AND execution_state IN ?", candidate.JobID, candidate.OwnerUserID, candidate.AttemptID,
				[]string{string(ExecutionRunning), string(ExecutionSealing)}).
			Limit(1).Find(&job)
		if result.Error != nil {
			return fmt.Errorf("lock export job for expired worker reconciliation: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return ErrAttemptFenceLost
		}
		var attempt model.BackupAssetExportAttempt
		result = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND job_id = ? AND is_current = ? AND state IN ? AND lease_expires_at <= ?", candidate.AttemptID, job.ID, true,
				[]string{string(AttemptActive), string(AttemptSealing)}, now).
			Limit(1).Find(&attempt)
		if result.Error != nil {
			return fmt.Errorf("lock expired export worker attempt: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return ErrAttemptFenceLost
		}
		if (ExecutionState(job.ExecutionState) == ExecutionRunning && AttemptState(attempt.State) != AttemptActive) ||
			(ExecutionState(job.ExecutionState) == ExecutionSealing && AttemptState(attempt.State) != AttemptSealing) {
			return ErrAttemptFenceLost
		}
		rows, err := lockAttemptWorkerReservationPairTx(tx, buckets, job, attempt)
		if err != nil {
			return err
		}
		finishedAt := now
		result = tx.Model(&model.BackupAssetExportAttempt{}).
			Where("id = ? AND job_id = ? AND is_current = ? AND state = ? AND lease_expires_at = ?", attempt.ID, job.ID, true, attempt.State, attempt.LeaseExpiresAt).
			Updates(map[string]any{
				"state": string(AttemptSuperseded), "is_current": false, "failure_category": "heartbeat_lost",
				"finished_at": finishedAt, "updated_at": now,
			})
		if result.Error != nil || result.RowsAffected != 1 {
			return ErrAttemptFenceLost
		}
		if err := releaseAttemptWorkerReservationPairTx(tx, rows, attempt, now); err != nil {
			return err
		}
		nextState := ExecutionFailed
		if (ExecutionState(job.ExecutionState) == ExecutionRunning || ExecutionState(job.ExecutionState) == ExecutionSealing) &&
			attempt.AttemptNumber < job.MaxAttempts {
			retryAt := now.Add(attemptRetryDelay(job, int64(attempt.AttemptNumber)))
			safeWindow := time.Duration(job.LeaseTTLSeconds+job.LeaseRenewMarginSeconds) * time.Second
			if safeWindow > 0 && job.AbsoluteDeadline.UTC().After(retryAt.Add(safeWindow)) {
				nextState = ExecutionRetryWait
			}
		}
		if ValidateExecutionTransition(ExecutionState(job.ExecutionState), nextState) != nil {
			return ErrInvalidTransition
		}
		result = tx.Model(&model.BackupAssetExportJob{}).
			Where("id = ? AND current_attempt_id = ? AND transition_revision = ? AND execution_state = ?", job.ID, attempt.ID, job.TransitionRevision, job.ExecutionState).
			Updates(map[string]any{
				"execution_state": string(nextState), "current_attempt_id": nil,
				"current_fence_revision": job.CurrentFenceRevision + 1, "error_category": "heartbeat_lost",
				"transition_revision": gorm.Expr("transition_revision + 1"), "updated_at": now,
			})
		if result.Error != nil || result.RowsAffected != 1 {
			return ErrAttemptFenceLost
		}
		return nil
	})
}

func attemptRetryDelay(job model.BackupAssetExportJob, attemptNumber int64) time.Duration {
	base := time.Duration(job.RetryBaseSeconds) * time.Second
	maximum := time.Duration(job.RetryMaxDelaySeconds) * time.Second
	if base <= 0 || maximum < base || attemptNumber <= 1 {
		return base
	}
	delay := base
	for remaining := attemptNumber - 1; remaining > 0 && delay < maximum; remaining-- {
		if delay > maximum/2 {
			return maximum
		}
		delay *= 2
	}
	if delay > maximum {
		return maximum
	}
	return delay
}

func validAttemptFailureCategory(category string) bool {
	switch category {
	case "worker_unavailable", "source_changed", "archive_failed", "heartbeat_lost", "deadline":
		return true
	default:
		return false
	}
}

func (coordinator *AttemptCoordinator) Heartbeat(
	ctx context.Context,
	request AttemptHeartbeatRequest,
) (AttemptHeartbeatResult, error) {
	if coordinator == nil || coordinator.sourceLeases == nil || backupasset.ValidateOpaqueID(request.JobID) != nil ||
		backupasset.ValidateOpaqueID(request.AttemptID) != nil || len(request.FenceToken) != 32 {
		return AttemptHeartbeatResult{}, ErrAttemptFenceLost
	}
	now := coordinator.now().UTC()
	var response AttemptHeartbeatResult
	err := coordinator.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var buckets quotaBucketPair
		if coordinator.workerCapacity != nil {
			var discovered struct {
				OwnerUserID uint `gorm:"column:owner_user_id"`
			}
			result := tx.Model(&model.BackupAssetExportJob{}).Select("owner_user_id").
				Where("id = ?", request.JobID).Limit(1).Scan(&discovered)
			if result.Error != nil {
				return fmt.Errorf("discover export job owner for worker heartbeat: %w", result.Error)
			}
			if result.RowsAffected != 1 || discovered.OwnerUserID == 0 {
				return ErrAttemptFenceLost
			}
			var err error
			buckets, err = ensureAndLockQuotaBucketPairTx(tx, discovered.OwnerUserID, now)
			if err != nil {
				return err
			}
		}
		var job model.BackupAssetExportJob
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND current_attempt_id = ? AND execution_state IN ?",
				request.JobID, request.AttemptID, []string{string(ExecutionRunning), string(ExecutionSealing)}).
			Limit(1).Find(&job)
		if result.Error != nil {
			return fmt.Errorf("load export job for heartbeat: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return ErrAttemptFenceLost
		}
		if !now.Before(job.AbsoluteDeadline.UTC()) {
			return ErrExecutionDeadlineReached
		}
		var attempt model.BackupAssetExportAttempt
		result = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND job_id = ? AND is_current = ? AND state IN ?",
				request.AttemptID, request.JobID, true, []string{string(AttemptActive), string(AttemptSealing)}).
			Limit(1).Find(&attempt)
		if result.Error != nil {
			return fmt.Errorf("load export attempt for heartbeat: %w", result.Error)
		}
		if result.RowsAffected != 1 || !now.Before(attempt.LeaseExpiresAt.UTC()) ||
			!equalFenceToken(attempt.FenceToken, request.FenceToken) {
			return ErrAttemptFenceLost
		}
		var workerReservations []model.BackupAssetExportReservation
		if coordinator.workerCapacity != nil {
			var pairErr error
			workerReservations, pairErr = lockAttemptWorkerReservationPairTx(tx, buckets, job, attempt)
			if pairErr != nil {
				return pairErr
			}
		}

		sources, err := loadPersistedSourceLeasesTx(tx, job.ID)
		if err != nil {
			return err
		}
		response.SourceLeaseExpiresAt = make([]time.Time, 0, len(sources))
		var sourceCap time.Time
		for _, source := range sources {
			leaseRow, err := loadMatchingSourceLeaseTx(tx, job, source, now)
			if err != nil {
				return err
			}
			renewed, err := coordinator.sourceLeases.RenewTx(ctx, tx, backupasset.LeaseFence{
				LeaseID: leaseRow.ID, RecoveryPointID: leaseRow.RecoveryPointID,
				HolderType: backupasset.LeaseHolderType(leaseRow.HolderType), OwnerID: leaseRow.OwnerID,
				AttemptID: leaseRow.AttemptID, FenceToken: leaseRow.FenceToken,
			})
			if err != nil {
				if errors.Is(err, backupasset.ErrLeaseDeadlineExceeded) {
					return fmt.Errorf("%w: renew Foundation source heartbeat: %w", ErrSourceDeadlineReached, err)
				}
				return fmt.Errorf("renew Foundation source heartbeat: %w", err)
			}
			if !renewed.AbsoluteDeadline.UTC().Equal(source.AbsoluteDeadline.UTC()) {
				return ErrAttemptFenceLost
			}
			if err := tx.Model(&model.BackupAssetExportSourceLease{}).
				Where("id = ? AND job_id = ? AND state = ? AND absolute_deadline = ?",
					source.ID, job.ID, "active", source.AbsoluteDeadline).
				Updates(map[string]any{"renewed_at": renewed.LastHeartbeatAt.UTC(), "updated_at": renewed.LastHeartbeatAt.UTC()}).Error; err != nil {
				return fmt.Errorf("persist export source heartbeat: %w", err)
			}
			response.SourceLeaseExpiresAt = append(response.SourceLeaseExpiresAt, renewed.LeaseExpiresAt.UTC())
			if sourceCap.IsZero() || source.AbsoluteDeadline.UTC().Before(sourceCap) {
				sourceCap = source.AbsoluteDeadline.UTC()
			}
		}

		nextExpiry := now.Add(time.Duration(job.LeaseTTLSeconds) * time.Second)
		if nextExpiry.After(job.AbsoluteDeadline.UTC()) {
			nextExpiry = job.AbsoluteDeadline.UTC()
		}
		for _, sourceExpiry := range response.SourceLeaseExpiresAt {
			if sourceExpiry.Before(nextExpiry) {
				nextExpiry = sourceExpiry
			}
		}
		if coordinator.workerCapacity != nil {
			if err := renewAttemptWorkerReservationPairTx(tx, workerReservations, attempt, nextExpiry, now); err != nil {
				return err
			}
		}
		result = tx.Model(&model.BackupAssetExportAttempt{}).
			Where("id = ? AND job_id = ? AND is_current = ? AND lease_expires_at = ?",
				attempt.ID, job.ID, true, attempt.LeaseExpiresAt).
			Updates(map[string]any{"lease_expires_at": nextExpiry, "updated_at": now})
		if result.Error != nil || result.RowsAffected != 1 {
			return ErrAttemptFenceLost
		}
		response.LeaseExpiresAt = nextExpiry
		response.SourceAbsoluteDeadline = sourceCap
		return nil
	})
	return response, err
}

func (coordinator *AttemptCoordinator) Checkpoint(ctx context.Context, checkpoint AttemptCheckpoint) error {
	if coordinator == nil || backupasset.ValidateOpaqueID(checkpoint.JobID) != nil ||
		backupasset.ValidateOpaqueID(checkpoint.AttemptID) != nil || backupasset.ValidateOpaqueID(checkpoint.ItemID) != nil ||
		len(checkpoint.FenceToken) != 32 || !terminalCheckpointState(checkpoint.State) ||
		checkpoint.LogicalBytes < 0 || checkpoint.ProviderBytes < 0 || len(checkpoint.ErrorCategory) > 64 {
		return ErrAttemptFenceLost
	}
	now := coordinator.now().UTC()
	return coordinator.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var job model.BackupAssetExportJob
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND current_attempt_id = ? AND execution_state = ?", checkpoint.JobID, checkpoint.AttemptID, ExecutionRunning).
			Limit(1).Find(&job)
		if result.Error != nil || result.RowsAffected != 1 || !now.Before(job.AbsoluteDeadline.UTC()) {
			return ErrAttemptFenceLost
		}
		var attempt model.BackupAssetExportAttempt
		result = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND job_id = ? AND is_current = ? AND state = ?", checkpoint.AttemptID, checkpoint.JobID, true, AttemptActive).
			Limit(1).Find(&attempt)
		if result.Error != nil || result.RowsAffected != 1 || !now.Before(attempt.LeaseExpiresAt.UTC()) ||
			!equalFenceToken(attempt.FenceToken, checkpoint.FenceToken) {
			return ErrAttemptFenceLost
		}
		if err := validatePersistedSourceFencesTx(tx, job, now); err != nil {
			return err
		}
		var item model.BackupAssetExportItem
		result = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND job_id = ? AND current_attempt_id = ?", checkpoint.ItemID, checkpoint.JobID, checkpoint.AttemptID).
			Limit(1).Find(&item)
		if result.Error != nil || result.RowsAffected != 1 {
			return ErrAttemptFenceLost
		}
		if checkpoint.State == ItemFailed &&
			(checkpoint.LogicalBytes != 0 || !validPreHeaderFailureCategory(checkpoint.ErrorCategory)) {
			return ErrInvalidTransition
		}
		if checkpoint.PreHeaderSpoolRecovered &&
			(checkpoint.State != ItemFailed || item.EntryType != string(backupasset.CatalogEntryFile) || item.State != string(ItemRead)) {
			return ErrInvalidTransition
		}
		if checkpoint.State == ItemFailed &&
			(item.EntryType != string(backupasset.CatalogEntryFile) ||
				(item.State != string(ItemPending) && (!checkpoint.PreHeaderSpoolRecovered || item.State != string(ItemRead)))) {
			return ErrInvalidTransition
		}
		if checkpoint.State == ItemPacked && (checkpoint.LogicalBytes != item.LogicalSize || checkpoint.ErrorCategory != "") {
			return ErrInvalidTransition
		}
		if checkpoint.State != ItemPacked && checkpoint.ErrorCategory == "" {
			return ErrInvalidTransition
		}
		var packedFileAttempt model.BackupAssetExportItemAttempt
		var recoveredReadFileAttempt model.BackupAssetExportItemAttempt
		if checkpoint.State == ItemPacked && item.EntryType == string(backupasset.CatalogEntryFile) {
			if item.State != string(ItemRead) || !validPersistedItemProjection(item, job) {
				return ErrInvalidTransition
			}
			result = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("job_id = ? AND attempt_id = ? AND item_id = ? AND state = ?", checkpoint.JobID, checkpoint.AttemptID,
					checkpoint.ItemID, ItemRead).
				Limit(1).Find(&packedFileAttempt)
			if result.Error != nil || result.RowsAffected != 1 {
				return ErrAttemptFenceLost
			}
			if !validPersistedItemAttemptState(packedFileAttempt, item, job) ||
				checkpoint.ProviderBytes != packedFileAttempt.ProviderBytes {
				return ErrInvalidTransition
			}
		}
		if checkpoint.State == ItemFailed && checkpoint.PreHeaderSpoolRecovered {
			if !validPersistedItemProjection(item, job) {
				return ErrInvalidTransition
			}
			result = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("job_id = ? AND attempt_id = ? AND item_id = ? AND state = ?", checkpoint.JobID, checkpoint.AttemptID,
					checkpoint.ItemID, ItemRead).
				Limit(1).Find(&recoveredReadFileAttempt)
			if result.Error != nil || result.RowsAffected != 1 {
				return ErrAttemptFenceLost
			}
			if !validPersistedItemAttemptState(recoveredReadFileAttempt, item, job) ||
				checkpoint.ProviderBytes != recoveredReadFileAttempt.ProviderBytes {
				return ErrInvalidTransition
			}
		}
		finishedAt := now
		itemAttemptStates := []string{string(ItemPending), string(ItemRead)}
		if checkpoint.State == ItemFailed {
			if checkpoint.PreHeaderSpoolRecovered {
				itemAttemptStates = []string{string(ItemRead)}
			} else {
				itemAttemptStates = []string{string(ItemPending)}
			}
		} else if checkpoint.State == ItemPacked && item.EntryType == string(backupasset.CatalogEntryFile) {
			itemAttemptStates = []string{string(ItemRead)}
		}
		itemAttemptUpdate := tx.Model(&model.BackupAssetExportItemAttempt{}).
			Where("job_id = ? AND attempt_id = ? AND item_id = ? AND state IN ?", checkpoint.JobID, checkpoint.AttemptID, checkpoint.ItemID, itemAttemptStates)
		if checkpoint.State == ItemFailed {
			if checkpoint.PreHeaderSpoolRecovered {
				itemAttemptUpdate = itemAttemptUpdate.Where(
					"spool_digest = ? AND spool_size = ? AND spool_locator = ? AND logical_bytes = ? AND provider_bytes = ? AND error_category = ? AND read_at IS NOT NULL AND packed_at IS NULL AND finished_at IS NULL",
					recoveredReadFileAttempt.SpoolDigest, recoveredReadFileAttempt.SpoolSize, recoveredReadFileAttempt.SpoolLocator,
					recoveredReadFileAttempt.LogicalBytes, recoveredReadFileAttempt.ProviderBytes, "",
				)
			} else {
				itemAttemptUpdate = itemAttemptUpdate.Where(
					"spool_digest = ? AND spool_size = ? AND spool_locator = ? AND logical_bytes = ? AND provider_bytes = ? AND error_category = ? AND read_at IS NULL AND packed_at IS NULL AND finished_at IS NULL",
					"", 0, "", 0, checkpoint.ProviderBytes, "",
				)
			}
		} else if checkpoint.State == ItemPacked && item.EntryType == string(backupasset.CatalogEntryFile) {
			itemAttemptUpdate = itemAttemptUpdate.Where(
				"spool_digest = ? AND spool_size = ? AND spool_locator = ? AND logical_bytes = ? AND provider_bytes = ? AND error_category = ? AND read_at IS NOT NULL AND packed_at IS NULL AND finished_at IS NULL",
				packedFileAttempt.SpoolDigest, packedFileAttempt.SpoolSize, packedFileAttempt.SpoolLocator,
				packedFileAttempt.LogicalBytes, packedFileAttempt.ProviderBytes, "",
			)
		}
		itemAttemptUpdates := map[string]any{
			"state": string(checkpoint.State), "logical_bytes": checkpoint.LogicalBytes,
			"provider_bytes": checkpoint.ProviderBytes, "error_category": checkpoint.ErrorCategory,
			"finished_at": finishedAt, "packed_at": packedAt(checkpoint.State, now),
		}
		if checkpoint.State == ItemFailed && checkpoint.PreHeaderSpoolRecovered {
			itemAttemptUpdates["spool_digest"] = ""
			itemAttemptUpdates["spool_size"] = 0
			itemAttemptUpdates["spool_locator"] = ""
			itemAttemptUpdates["read_at"] = nil
		}
		result = itemAttemptUpdate.Updates(itemAttemptUpdates)
		if result.Error != nil {
			return errors.Join(ErrUnavailable, fmt.Errorf("update export item-attempt checkpoint: %w", result.Error))
		}
		if result.RowsAffected != 1 {
			return ErrAttemptFenceLost
		}
		if err := tx.Model(&model.BackupAssetExportItem{}).Where("id = ? AND current_attempt_id = ?", item.ID, checkpoint.AttemptID).
			Updates(map[string]any{
				"state": string(checkpoint.State), "logical_bytes": checkpoint.LogicalBytes,
				"provider_bytes": checkpoint.ProviderBytes, "error_category": checkpoint.ErrorCategory, "updated_at": now,
			}).Error; err != nil {
			return fmt.Errorf("update export item projection: %w", err)
		}
		counts, err := currentAttemptCountsTx(tx, checkpoint.AttemptID)
		if err != nil {
			return err
		}
		if err := tx.Model(&model.BackupAssetExportAttempt{}).Where("id = ? AND is_current = ?", attempt.ID, true).
			Updates(map[string]any{
				"checkpoint_ordinal": item.Ordinal, "checkpoint_item_count": counts.finalized,
				"checkpoint_logical_bytes": counts.logical, "checkpoint_provider_bytes": counts.provider, "updated_at": now,
			}).Error; err != nil {
			return fmt.Errorf("update export attempt checkpoint: %w", err)
		}
		result = tx.Model(&model.BackupAssetExportJob{}).Where("id = ? AND current_attempt_id = ?", job.ID, attempt.ID).
			Updates(map[string]any{
				"packed_count": counts.packed, "skipped_count": counts.skipped, "failed_count": counts.failed,
				"logical_bytes": counts.logical, "provider_bytes": counts.provider,
				"transition_revision": gorm.Expr("transition_revision + 1"), "updated_at": now,
			})
		if result.Error != nil || result.RowsAffected != 1 {
			return ErrAttemptFenceLost
		}
		return nil
	})
}

type attemptCounts struct {
	packed, skipped, failed, finalized, logical, provider int64
}

func currentAttemptCountsTx(tx *gorm.DB, attemptID string) (attemptCounts, error) {
	var rows []model.BackupAssetExportItemAttempt
	if err := tx.Where("attempt_id = ?", attemptID).Find(&rows).Error; err != nil {
		return attemptCounts{}, fmt.Errorf("load export attempt checkpoints: %w", err)
	}
	var counts attemptCounts
	for _, row := range rows {
		switch ItemState(row.State) {
		case ItemPacked:
			counts.packed++
		case ItemSkipped:
			counts.skipped++
		case ItemFailed:
			counts.failed++
		default:
			continue
		}
		counts.finalized++
		counts.logical += row.LogicalBytes
		counts.provider += row.ProviderBytes
	}
	return counts, nil
}

func validatePersistedSourceFencesTx(tx *gorm.DB, job model.BackupAssetExportJob, now time.Time) error {
	_, err := loadValidatedPersistedSourceFencesTx(tx, job, now)
	return err
}

func loadValidatedPersistedSourceFencesTx(
	tx *gorm.DB,
	job model.BackupAssetExportJob,
	now time.Time,
) ([]model.BackupAssetExportSourceLease, error) {
	locked, err := lockValidatedPersistedSourceFencesTx(tx, job)
	if err != nil {
		return nil, err
	}
	return validateLockedPersistedSourceFenceExpiries(locked, now)
}

func validatePersistedSourceFencesForItemsTx(
	tx *gorm.DB,
	job model.BackupAssetExportJob,
	items []model.BackupAssetExportItem,
	now time.Time,
) error {
	expected, err := frozenSourceRetentionCapsForItems(job, items)
	if err != nil {
		return err
	}
	locked, err := lockValidatedPersistedSourceFencesForCapsTx(tx, job, expected)
	if err != nil {
		return err
	}
	_, err = validateLockedPersistedSourceFenceExpiries(locked, now)
	return err
}

type lockedPersistedSourceFence struct {
	source model.BackupAssetExportSourceLease
	lease  model.RecoveryPointLease
}

func lockValidatedPersistedSourceFencesTx(
	tx *gorm.DB,
	job model.BackupAssetExportJob,
) ([]lockedPersistedSourceFence, error) {
	expected, err := frozenSourceRetentionCapsTx(tx, job)
	if err != nil {
		return nil, err
	}
	return lockValidatedPersistedSourceFencesForCapsTx(tx, job, expected)
}

func lockValidatedPersistedSourceFencesForCapsTx(
	tx *gorm.DB,
	job model.BackupAssetExportJob,
	expected map[string]*time.Time,
) ([]lockedPersistedSourceFence, error) {
	sources, err := loadPersistedSourceLeasesTx(tx, job.ID)
	if err != nil {
		return nil, err
	}
	if len(sources) != len(expected) {
		return nil, ErrAttemptFenceLost
	}
	locked := make([]lockedPersistedSourceFence, 0, len(sources))
	seen := make(map[string]struct{}, len(sources))
	for _, source := range sources {
		retentionUntil, found := expected[source.RecoveryPointID]
		if !found || !sameOptionalTime(source.RetentionUntil, retentionUntil) {
			return nil, ErrAttemptFenceLost
		}
		if _, duplicate := seen[source.RecoveryPointID]; duplicate {
			return nil, ErrAttemptFenceLost
		}
		seen[source.RecoveryPointID] = struct{}{}
		lease, err := lockSourceLeaseIdentityTx(tx, job, source)
		if err != nil {
			return nil, err
		}
		locked = append(locked, lockedPersistedSourceFence{source: source, lease: lease})
	}
	return locked, nil
}

func validateLockedPersistedSourceFenceExpiries(
	locked []lockedPersistedSourceFence,
	now time.Time,
) ([]model.BackupAssetExportSourceLease, error) {
	sources := make([]model.BackupAssetExportSourceLease, 0, len(locked))
	for _, fence := range locked {
		if err := validateSourceFenceDeadlineAt(fence.source, now); err != nil {
			return nil, err
		}
		if !now.Before(fence.lease.LeaseExpiresAt.UTC()) {
			return nil, ErrAttemptFenceLost
		}
		sources = append(sources, fence.source)
	}
	return sources, nil
}

func frozenSourceRetentionCapsTx(
	tx *gorm.DB, job model.BackupAssetExportJob,
) (map[string]*time.Time, error) {
	if job.ItemCount <= 0 || job.MaxItems <= 0 || job.MaxItems > maxSelectionItemsV1 ||
		job.ItemCount > int64(job.MaxItems) {
		return nil, ErrUnavailable
	}
	var items []model.BackupAssetExportItem
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("recovery_point_id", "retention_until").Where("job_id = ?", job.ID).
		Order("recovery_point_id ASC, ordinal ASC, id ASC").Limit(job.MaxItems + 1).Find(&items).Error; err != nil {
		return nil, fmt.Errorf("load frozen export source authority: %w", err)
	}
	return frozenSourceRetentionCapsForItems(job, items)
}

func frozenSourceRetentionCapsForItems(
	job model.BackupAssetExportJob,
	items []model.BackupAssetExportItem,
) (map[string]*time.Time, error) {
	if len(items) == 0 || int64(len(items)) != job.ItemCount {
		return nil, ErrUnavailable
	}
	frozen := make([]FrozenItem, 0, len(items))
	for _, item := range items {
		if backupasset.ValidateOpaqueID(item.RecoveryPointID) != nil ||
			item.RetentionUntil != nil && item.RetentionUntil.UTC().IsZero() {
			return nil, ErrAttemptFenceLost
		}
		frozen = append(frozen, FrozenItem{
			Ref:            backupasset.AssetRef{RecoveryPointID: item.RecoveryPointID},
			RetentionUntil: cloneOptionalTime(item.RetentionUntil),
		})
	}
	expected := make(map[string]*time.Time, len(frozen))
	for _, source := range groupSelectionSources(frozen) {
		expected[source.recoveryPointID] = cloneOptionalTime(source.retentionUntil)
	}
	return expected, nil
}

func loadPersistedSourceLeasesTx(tx *gorm.DB, jobID string) ([]model.BackupAssetExportSourceLease, error) {
	var sources []model.BackupAssetExportSourceLease
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("job_id = ? AND state = ?", jobID, "active").Order("recovery_point_id ASC").Find(&sources).Error; err != nil {
		return nil, fmt.Errorf("load export source leases: %w", err)
	}
	if len(sources) == 0 {
		return nil, ErrAttemptFenceLost
	}
	return sources, nil
}

func loadMatchingSourceLeaseTx(
	tx *gorm.DB,
	job model.BackupAssetExportJob,
	source model.BackupAssetExportSourceLease,
	now time.Time,
) (model.RecoveryPointLease, error) {
	lease, err := loadSourceLeaseIdentityTx(tx, job, source, now)
	if err != nil {
		return model.RecoveryPointLease{}, err
	}
	if !now.Before(lease.LeaseExpiresAt.UTC()) {
		return model.RecoveryPointLease{}, ErrAttemptFenceLost
	}
	return lease, nil
}

func loadSourceLeaseIdentityTx(
	tx *gorm.DB,
	job model.BackupAssetExportJob,
	source model.BackupAssetExportSourceLease,
	now time.Time,
) (model.RecoveryPointLease, error) {
	lease, err := lockSourceLeaseIdentityTx(tx, job, source)
	if err != nil {
		return model.RecoveryPointLease{}, err
	}
	if err := validateSourceFenceDeadlineAt(source, now); err != nil {
		return model.RecoveryPointLease{}, err
	}
	return lease, nil
}

func lockSourceLeaseIdentityTx(
	tx *gorm.DB,
	job model.BackupAssetExportJob,
	source model.BackupAssetExportSourceLease,
) (model.RecoveryPointLease, error) {
	var lease model.RecoveryPointLease
	result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", source.LeaseID).Limit(1).Find(&lease)
	if result.Error != nil {
		return model.RecoveryPointLease{}, fmt.Errorf("load Foundation export source lease: %w", result.Error)
	}
	if source.AbsoluteDeadline.IsZero() || source.RetentionUntil != nil && source.RetentionUntil.UTC().IsZero() {
		return model.RecoveryPointLease{}, ErrUnavailable
	}
	if result.RowsAffected != 1 || lease.RecoveryPointID != source.RecoveryPointID ||
		lease.HolderType != string(backupasset.LeaseHolderExportJob) || lease.OwnerID != job.ID ||
		lease.AttemptID != source.LeaseAttemptID || lease.Status != string(backupasset.LeaseActive) ||
		!lease.AbsoluteDeadline.UTC().Equal(source.AbsoluteDeadline.UTC()) {
		return model.RecoveryPointLease{}, ErrAttemptFenceLost
	}
	digest := sha256.Sum256([]byte(lease.FenceToken))
	if hex.EncodeToString(digest[:]) != source.FenceHash {
		return model.RecoveryPointLease{}, ErrAttemptFenceLost
	}
	return lease, nil
}

func validateSourceFenceDeadlineAt(source model.BackupAssetExportSourceLease, now time.Time) error {
	if source.AbsoluteDeadline.IsZero() || source.RetentionUntil != nil && source.RetentionUntil.UTC().IsZero() {
		return ErrUnavailable
	}
	if !now.Before(source.AbsoluteDeadline.UTC()) ||
		source.RetentionUntil != nil && !now.Before(source.RetentionUntil.UTC()) {
		return ErrSourceDeadlineReached
	}
	return nil
}

func attemptClaimState(state ExecutionState) bool {
	switch state {
	case ExecutionQueued, ExecutionRetryWait, ExecutionRunning, ExecutionSealing:
		return true
	default:
		return false
	}
}

func sourceHeartbeatState(state ExecutionState) bool {
	switch state {
	case ExecutionQueued, ExecutionRunning, ExecutionRetryWait, ExecutionSealing, ExecutionReady:
		return true
	default:
		return false
	}
}

func sourceMaintenanceDeadlineError(job model.BackupAssetExportJob, now time.Time) error {
	if ExecutionState(job.ExecutionState) == ExecutionReady {
		if err := readyMaintenanceShapeError(job, now); err != nil {
			return err
		}
		expiresAt := job.ExpiresAt.UTC()
		if !now.Before(expiresAt) {
			return ErrReadyExpired
		}
		return nil
	}
	if job.AbsoluteDeadline.IsZero() {
		return ErrUnavailable
	}
	if !now.Before(job.AbsoluteDeadline.UTC()) {
		return ErrExecutionDeadlineReached
	}
	return nil
}

func readyMaintenanceShapeError(job model.BackupAssetExportJob, now time.Time) error {
	if job.ReadyAt == nil || job.ExpiresAt == nil {
		return ErrUnavailable
	}
	readyAt := job.ReadyAt.UTC()
	expiresAt := job.ExpiresAt.UTC()
	if readyAt.IsZero() || expiresAt.IsZero() || !expiresAt.After(readyAt) || now.Before(readyAt) {
		return ErrUnavailable
	}
	return nil
}

func terminalCheckpointState(state ItemState) bool {
	return state == ItemPacked || state == ItemSkipped || state == ItemFailed
}

func equalFenceToken(left, right []byte) bool {
	leftDigest := sha256.Sum256(left)
	rightDigest := sha256.Sum256(right)
	return leftDigest == rightDigest
}

func packedAt(state ItemState, now time.Time) *time.Time {
	if state != ItemPacked {
		return nil
	}
	value := now.UTC()
	return &value
}

type AttemptSourceBroker interface {
	OpenSession(context.Context, content.AttemptSourceBinding) (content.AttemptInputSession, content.AttemptSourceInfo, error)
}

type MetadataValidator interface {
	RevalidateMetadata(context.Context, FrozenItem) error
	RevalidateMetadataTx(context.Context, *gorm.DB, FrozenItem) error
}

type Worker struct {
	broker   AttemptSourceBroker
	metadata MetadataValidator
	now      func() time.Time
}

type WorkerItem struct {
	ItemID string
	Frozen FrozenItem
}

type WorkerArchiveRequest struct {
	AttemptID       string
	SelectionDigest string
	Deadline        time.Time
	Format          ArchiveFormat
	ArchiveProfile  string
	Limits          ArchiveLimits
	Items           []WorkerItem
}

func NewWorker(broker AttemptSourceBroker, metadata MetadataValidator, clocks ...func() time.Time) (*Worker, error) {
	if broker == nil || metadata == nil || len(clocks) > 1 || len(clocks) == 1 && clocks[0] == nil {
		return nil, ErrUnavailable
	}
	now := func() time.Time { return time.Now().UTC() }
	if len(clocks) == 1 {
		now = clocks[0]
	}
	return &Worker{broker: broker, metadata: metadata, now: now}, nil
}

func (worker *Worker) WriteArchive(
	ctx context.Context,
	destination io.Writer,
	request WorkerArchiveRequest,
) (ArchiveReport, error) {
	if worker == nil || backupasset.ValidateOpaqueID(request.AttemptID) != nil || !lowerHex(request.SelectionDigest, 64) ||
		!ValidArchiveProfilePair(request.Format, request.ArchiveProfile) || request.Deadline.IsZero() ||
		!request.Deadline.After(worker.now().UTC()) || len(request.Items) == 0 {
		return ArchiveReport{}, ErrUnavailable
	}
	entries := make([]ArchiveEntry, 0, len(request.Items))
	for _, value := range request.Items {
		if backupasset.ValidateOpaqueID(value.ItemID) != nil || ValidateFrozenItem(value.Frozen) != nil {
			return ArchiveReport{}, ErrInvalidSelection
		}
		entry := ArchiveEntry{
			ItemID: value.ItemID, Components: append([]string(nil), value.Frozen.ArchiveComponents...),
			RecoveryPointID: value.Frozen.Ref.RecoveryPointID, EntryID: value.Frozen.Ref.EntryID,
			SelectionRootOrdinal: value.Frozen.SelectionRootOrdinal,
			Type:                 value.Frozen.EntryType, Size: value.Frozen.LogicalSize,
		}
		if value.Frozen.EntryType == backupasset.CatalogEntryFile {
			frozen := cloneFrozenItem(value.Frozen)
			entry.Open = func(openCtx context.Context) (io.ReadCloser, error) {
				zeroByte := frozen.LogicalSize == 0
				allowedModes := []content.SourceMode{content.SourceModeStat, content.SourceModeSequential}
				readLimit := frozen.LogicalSize
				maxRequests := int64(2)
				if zeroByte {
					allowedModes = []content.SourceMode{content.SourceModeStat}
					readLimit = 1
					maxRequests = 2
				}
				binding := content.AttemptSourceBinding{
					SessionID: request.AttemptID, Ref: frozen.Ref, CatalogGenerationID: frozen.CatalogGenerationID,
					SourceFingerprint: frozen.SourceFingerprint, EntryFingerprint: frozen.EntryFingerprint,
					AllowedModes: allowedModes,
					Limits: content.AttemptReadLimits{
						MaxBytesPerRequest: readLimit, MaxCumulativeBytes: readLimit,
						MaxRequests: maxRequests, MaxInFlight: 1,
					},
					AbsoluteExpiresAt: request.Deadline.UTC(),
				}
				session, info, err := worker.broker.OpenSession(openCtx, binding)
				if err != nil {
					return nil, err
				}
				if info.Size != frozen.LogicalSize || info.MediaType != frozen.MediaType || !zeroByte && !info.Sequential ||
					(frozen.FingerprintStrength == "strong" && !info.FingerprintStrong) {
					_ = session.Close()
					return nil, content.ErrAttemptSourceChanged
				}
				if zeroByte {
					revalidateErr := session.Revalidate(openCtx)
					closeErr := session.Close()
					if revalidateErr != nil || closeErr != nil {
						return nil, errors.Join(revalidateErr, closeErr)
					}
					return io.NopCloser(bytes.NewReader(nil)), nil
				}
				reader, err := session.OpenSequential(openCtx, frozen.LogicalSize)
				if err != nil {
					_ = session.Close()
					return nil, err
				}
				return &brokeredReadCloser{ctx: openCtx, reader: reader, session: session}, nil
			}
		} else if err := worker.metadata.RevalidateMetadata(ctx, value.Frozen); err != nil {
			return ArchiveReport{}, err
		}
		entries = append(entries, entry)
	}
	return WriteArchive(ctx, destination, request.Format, request.ArchiveProfile, request.SelectionDigest, entries, request.Limits)
}

type brokeredReadCloser struct {
	ctx     context.Context
	reader  content.AttemptReadHandle
	session content.AttemptInputSession
	closed  bool
}

func (reader *brokeredReadCloser) Read(buffer []byte) (int, error) {
	if reader.closed {
		return 0, io.ErrClosedPipe
	}
	return reader.reader.Read(buffer)
}

func (reader *brokeredReadCloser) Close() error {
	if reader.closed {
		return nil
	}
	reader.closed = true
	readErr := reader.reader.Close()
	revalidateErr := reader.session.Revalidate(reader.ctx)
	closeErr := reader.session.Close()
	return errors.Join(readErr, revalidateErr, closeErr)
}
