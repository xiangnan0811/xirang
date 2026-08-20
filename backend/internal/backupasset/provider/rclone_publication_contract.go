package provider

import (
	"fmt"
	"reflect"
	"strings"
	"time"

	"xirang/backend/internal/backupasset"
)

type RcloneNativeProfileCode string

const RcloneNativeAWSS3GeneralPurposeV1 RcloneNativeProfileCode = "aws_s3_general_purpose_v1"

type RcloneNativeEncryptionProfileCode string

const (
	RcloneNativeSSES3V1  RcloneNativeEncryptionProfileCode = "sse_s3_v1"
	RcloneNativeSSEKMSV1 RcloneNativeEncryptionProfileCode = "sse_kms_cmk_v1"
)

type RcloneAttemptV1 struct {
	SchemaVersion              int
	LayoutVersion              int
	MinimumRuntimeRevision     int
	Provider                   backupasset.ProviderKind
	RepositoryID               string
	TaskRepositoryLinkID       string
	RecoveryPointID            string
	AttemptID                  string
	TaskID                     uint
	TaskRunID                  uint
	Trigger                    string
	PublicationMode            backupasset.TaskPublicationMode
	ImportedBaseline           bool
	CaptureStartedAt           time.Time
	PreparedAt                 time.Time
	PointDeadlineAt            time.Time
	ExpectedTaskRevision       uint64
	BindingRevision            uint64
	ConfigRevision             uint64
	ConfigDigest               string
	CapabilityRevision         uint64
	CredentialRevision         uint64
	PreflightID                string
	PreflightRevision          uint64
	PreflightDigest            string
	ManifestSchemaRevision     uint64
	ManifestLimitsRevision     uint64
	ManifestLimitsDigest       string
	RepositoryIdentityDigest   string
	ManagedRootIdentityDigest  string
	ChildFenceDigest           string
	LegacyOriginEvidenceDigest string
	Portable                   *RclonePortableAttemptV1
	Native                     *RcloneNativeAttemptV1
}

type RclonePortableAttemptV1 struct {
	AttemptComponent         string
	DataComponent            string
	ControlComponent         string
	AttemptMarkerDigest      string
	ExpectedConsistencyClass string
	ExpectedHashFidelity     string
	CopyDest                 *RclonePortableCopyDestV1
}

type RclonePortableCopyDestV1 struct {
	ParentRecoveryPointID    string
	ParentAttemptID          string
	ParentDataComponent      string
	ParentCommitDigest       string
	ParentManifestDigest     string
	ParentCapabilityRevision uint64
}

type RcloneNativeAttemptV1 struct {
	ProfileCode                 RcloneNativeProfileCode
	RegionIdentityDigest        string
	BucketIdentityDigest        string
	ManagedPrefixIdentityDigest string
	RoleSessionIdentityDigest   string
	SessionExpiresAt            time.Time
	VersioningDigest            string
	LifecycleDigest             string
	CapabilityStableObservedAt  time.Time
	EncryptionProfile           RcloneNativeEncryptionProfileCode
	BucketEncryptionDigest      string
	ActiveKeyDigest             string
	RetainedReadKeySetDigest    string
	KMSCapabilityRevision       uint64
	B0VersionGraphDigest        string
	StartMarkerIdentityDigest   string
	CanaryIdentityDigest        string
}

type RcloneCommitV1 struct {
	SchemaVersion                int
	LayoutVersion                int
	MinimumRuntimeRevision       int
	RepositoryID                 string
	TaskRepositoryLinkID         string
	RecoveryPointID              string
	AttemptID                    string
	PublicationMode              backupasset.TaskPublicationMode
	PointDeadlineAt              time.Time
	ProviderCommittedAt          time.Time
	ManifestIndexDigest          string
	ManifestChunkDigests         []string
	ManifestEntryCount           uint64
	LogicalBytes                 uint64
	SourceObservationDigest      string
	DestinationObservationDigest string
	ContentProofDigest           string
	FidelityEvidenceDigest       string
	CostEvidenceDigest           string
	CapabilityEvidenceDigest     string
	ChildFenceDigest             string
	Portable                     *RclonePortableCommitV1
	Native                       *RcloneNativeCommitV1
}

type RclonePortableCommitV1 struct {
	AttemptIdentityDigest      string
	ControlIdentityDigest      string
	DataIdentityDigest         string
	AttemptMarkerDigest        string
	ParentRecoveryPointID      string
	ParentCommitDigest         string
	ParentManifestDigest       string
	CommitComponent            string
	CommitPayloadDigest        string
	CommitAuthenticationDigest string
	ConsistencyEvidenceDigest  string
	HashEvidenceDigest         string
	DownloadVerifiedBytes      uint64
}

type RcloneNativeCommitV1 struct {
	CommitKey                  string                     `json:"-"`
	CommitVersionID            string                     `json:"-"`
	FrozenNativeVersions       []RcloneNativeExactVersion `json:"-"`
	CommitContentDigest        string
	ManifestControlGraphDigest string
	PointViewDigest            string
	MutationLedgerDigest       string
	B0VersionGraphDigest       string
	B1VersionGraphDigest       string
	ExactReadProofDigest       string
	VersioningDigest           string
	LifecycleDigest            string
	BucketEncryptionDigest     string
	EncryptionEvidenceDigest   string
	ActiveKeyDigest            string
	RetainedReadKeySetDigest   string
	RoleSessionIdentityDigest  string
	CapabilityRevision         uint64
	CredentialRevision         uint64
	KMSCapabilityRevision      uint64
	SessionExpiresAt           time.Time
}

type RcloneManifestV1 struct {
	ManifestIndexDigest    string
	ManifestChunkDigests   []string
	EntryCount             uint64
	LogicalBytes           uint64
	FidelityEvidenceDigest string
	FailureCode            backupasset.PublicationFailureCode
}

type RclonePublicationInput struct {
	ManifestLimits  ManifestLimits
	Access          AccessBinding                     `json:"-"`
	PortableRequest *RclonePortablePublicationRequest `json:"-"`
	NativeRequest   *RcloneNativePublicationRequest   `json:"-"`
}

type RcloneReconcileInput struct {
	ManifestLimits  ManifestLimits
	Access          AccessBinding                     `json:"-"`
	PortableRequest *RclonePortablePublicationRequest `json:"-"`
	NativeRequest   *RcloneNativePublicationRequest   `json:"-"`
}

func (value *RclonePublicationInput) validateVariant(mode backupasset.TaskPublicationMode) bool {
	if value == nil {
		return false
	}
	switch mode {
	case backupasset.PublicationVersionedPrefix:
		return value.PortableRequest != nil && value.NativeRequest == nil
	case backupasset.PublicationNativeObjectVersions:
		return value.NativeRequest != nil && value.PortableRequest == nil
	default:
		return false
	}
}

func (value *RcloneReconcileInput) validateVariant(mode backupasset.TaskPublicationMode) bool {
	if value == nil {
		return false
	}
	switch mode {
	case backupasset.PublicationVersionedPrefix:
		return value.PortableRequest != nil && value.NativeRequest == nil
	case backupasset.PublicationNativeObjectVersions:
		return value.NativeRequest != nil && value.PortableRequest == nil
	default:
		return false
	}
}

type RcloneReconcileState string

const (
	RcloneReconcileAbsent            RcloneReconcileState = "absent"
	RcloneReconcileIncomplete        RcloneReconcileState = "incomplete"
	RcloneReconcileProviderCommitted RcloneReconcileState = "provider_committed"
)

type RcloneReconcileV1 struct {
	State    RcloneReconcileState
	Commit   *RcloneCommitV1
	Manifest *RcloneManifestV1
}

func (value RcloneAttemptV1) Validate() error {
	if value.SchemaVersion != taggedPublicationSchemaV1 || value.LayoutVersion != taggedPublicationSchemaV1 || value.MinimumRuntimeRevision <= 0 ||
		value.Provider != backupasset.ProviderRclone || backupasset.ValidateOpaqueID(value.RepositoryID) != nil ||
		backupasset.ValidateOpaqueID(value.TaskRepositoryLinkID) != nil || backupasset.ValidateOpaqueID(value.RecoveryPointID) != nil ||
		backupasset.ValidateOpaqueID(value.AttemptID) != nil || backupasset.ValidateOpaqueID(value.PreflightID) != nil || value.TaskID == 0 || value.TaskRunID == 0 ||
		!validTaggedPublicationLabel(value.Trigger) || !validOrderedRcloneAttemptTimes(value) || value.ExpectedTaskRevision == 0 || value.BindingRevision == 0 ||
		value.ConfigRevision == 0 || value.CapabilityRevision == 0 || value.CredentialRevision == 0 || value.PreflightRevision == 0 ||
		value.ManifestSchemaRevision == 0 || value.ManifestLimitsRevision == 0 || !validTaggedDigest(value.ConfigDigest) ||
		!validTaggedDigest(value.PreflightDigest) || !validTaggedDigest(value.ManifestLimitsDigest) || !validTaggedDigest(value.RepositoryIdentityDigest) ||
		!validTaggedDigest(value.ManagedRootIdentityDigest) || !validTaggedDigest(value.ChildFenceDigest) || !validTaggedDigest(value.LegacyOriginEvidenceDigest) {
		return fmt.Errorf("%w: invalid Rclone publication attempt", backupasset.ErrInvalidState)
	}
	switch value.PublicationMode {
	case backupasset.PublicationVersionedPrefix:
		if value.Portable == nil || value.Native != nil || value.Portable.Validate(value.RecoveryPointID, value.AttemptID) != nil {
			return fmt.Errorf("%w: invalid portable Rclone publication attempt", backupasset.ErrInvalidState)
		}
	case backupasset.PublicationNativeObjectVersions:
		if value.Native == nil || value.Portable != nil || value.Native.Validate(value.PointDeadlineAt) != nil {
			return fmt.Errorf("%w: invalid native Rclone publication attempt", backupasset.ErrInvalidState)
		}
	default:
		return fmt.Errorf("%w: invalid Rclone publication mode", backupasset.ErrInvalidState)
	}
	return nil
}

func validOrderedRcloneAttemptTimes(value RcloneAttemptV1) bool {
	return validTaggedPublicationTime(value.CaptureStartedAt) && validTaggedPublicationTime(value.PreparedAt) && validTaggedPublicationTime(value.PointDeadlineAt) &&
		!value.PreparedAt.Before(value.CaptureStartedAt) && value.PointDeadlineAt.After(value.PreparedAt)
}

func (value RclonePortableAttemptV1) Validate(pointID, attemptID string) error {
	if value.AttemptComponent != pointID+"."+attemptID || value.DataComponent != "data" || value.ControlComponent != "control" ||
		!validTaggedDigest(value.AttemptMarkerDigest) || value.ExpectedConsistencyClass != string(backupasset.RcloneConsistencyObservationallyStable) ||
		(value.ExpectedHashFidelity != string(backupasset.RcloneHashProviderStrongChecksum) && value.ExpectedHashFidelity != string(backupasset.RcloneHashDownloadVerifiedBytes)) {
		return fmt.Errorf("%w: invalid portable Rclone attempt variant", backupasset.ErrInvalidState)
	}
	if value.CopyDest != nil {
		if err := value.CopyDest.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func (value RclonePortableCopyDestV1) Validate() error {
	if backupasset.ValidateOpaqueID(value.ParentRecoveryPointID) != nil || backupasset.ValidateOpaqueID(value.ParentAttemptID) != nil ||
		value.ParentDataComponent != "data" || !validTaggedDigest(value.ParentCommitDigest) || !validTaggedDigest(value.ParentManifestDigest) ||
		value.ParentCapabilityRevision == 0 {
		return fmt.Errorf("%w: invalid portable Rclone copy-dest", backupasset.ErrInvalidState)
	}
	return nil
}

func (value RcloneNativeAttemptV1) Validate(pointDeadline time.Time) error {
	if value.ProfileCode != RcloneNativeAWSS3GeneralPurposeV1 || !validTaggedDigest(value.RegionIdentityDigest) ||
		!validTaggedDigest(value.BucketIdentityDigest) || !validTaggedDigest(value.ManagedPrefixIdentityDigest) ||
		!validTaggedDigest(value.RoleSessionIdentityDigest) || !validTaggedPublicationTime(value.SessionExpiresAt) ||
		!value.SessionExpiresAt.After(pointDeadline) || !validTaggedDigest(value.VersioningDigest) || !validTaggedDigest(value.LifecycleDigest) ||
		!validTaggedPublicationTime(value.CapabilityStableObservedAt) || !validTaggedDigest(value.BucketEncryptionDigest) ||
		!validTaggedDigest(value.B0VersionGraphDigest) || !validTaggedDigest(value.StartMarkerIdentityDigest) || !validTaggedDigest(value.CanaryIdentityDigest) {
		return fmt.Errorf("%w: invalid native Rclone attempt variant", backupasset.ErrInvalidState)
	}
	switch value.EncryptionProfile {
	case RcloneNativeSSES3V1:
		if value.ActiveKeyDigest != "" || value.RetainedReadKeySetDigest != "" || value.KMSCapabilityRevision != 0 {
			return fmt.Errorf("%w: invalid SSE-S3 Rclone attempt variant", backupasset.ErrInvalidState)
		}
	case RcloneNativeSSEKMSV1:
		if !validTaggedDigest(value.ActiveKeyDigest) || !validTaggedDigest(value.RetainedReadKeySetDigest) || value.KMSCapabilityRevision == 0 {
			return fmt.Errorf("%w: invalid SSE-KMS Rclone attempt variant", backupasset.ErrInvalidState)
		}
	default:
		return fmt.Errorf("%w: unsupported native Rclone encryption profile", backupasset.ErrInvalidState)
	}
	return nil
}

func (value RcloneCommitV1) Validate() error {
	if value.SchemaVersion != taggedPublicationSchemaV1 || value.LayoutVersion != taggedPublicationSchemaV1 || value.MinimumRuntimeRevision <= 0 ||
		backupasset.ValidateOpaqueID(value.RepositoryID) != nil || backupasset.ValidateOpaqueID(value.TaskRepositoryLinkID) != nil ||
		backupasset.ValidateOpaqueID(value.RecoveryPointID) != nil || backupasset.ValidateOpaqueID(value.AttemptID) != nil ||
		!validTaggedPublicationTime(value.PointDeadlineAt) || !validTaggedPublicationTime(value.ProviderCommittedAt) ||
		value.ProviderCommittedAt.After(value.PointDeadlineAt) || !validTaggedDigest(value.ManifestIndexDigest) ||
		!validRcloneManifestDigestSlice(value.ManifestChunkDigests, value.ManifestEntryCount == 0 && value.LogicalBytes == 0) || !validTaggedDigest(value.SourceObservationDigest) ||
		!validTaggedDigest(value.DestinationObservationDigest) || !validTaggedDigest(value.ContentProofDigest) ||
		!validTaggedDigest(value.FidelityEvidenceDigest) || !validTaggedDigest(value.CostEvidenceDigest) ||
		!validTaggedDigest(value.CapabilityEvidenceDigest) || !validTaggedDigest(value.ChildFenceDigest) {
		return fmt.Errorf("%w: invalid Rclone provider commit", backupasset.ErrInvalidState)
	}
	switch value.PublicationMode {
	case backupasset.PublicationVersionedPrefix:
		if value.Portable == nil || value.Native != nil || value.Portable.Validate() != nil {
			return fmt.Errorf("%w: invalid portable Rclone provider commit", backupasset.ErrInvalidState)
		}
	case backupasset.PublicationNativeObjectVersions:
		if value.Native == nil || value.Portable != nil || value.Native.Validate(value.PointDeadlineAt) != nil {
			return fmt.Errorf("%w: invalid native Rclone provider commit", backupasset.ErrInvalidState)
		}
	default:
		return fmt.Errorf("%w: invalid Rclone provider commit mode", backupasset.ErrInvalidState)
	}
	return nil
}

func (value RclonePortableCommitV1) Validate() error {
	if !validTaggedDigest(value.AttemptIdentityDigest) || !validTaggedDigest(value.ControlIdentityDigest) || !validTaggedDigest(value.DataIdentityDigest) ||
		!validTaggedDigest(value.AttemptMarkerDigest) || value.CommitComponent != "commit.json" || !validTaggedDigest(value.CommitPayloadDigest) ||
		!validTaggedDigest(value.CommitAuthenticationDigest) || !validTaggedDigest(value.ConsistencyEvidenceDigest) || !validTaggedDigest(value.HashEvidenceDigest) {
		return fmt.Errorf("%w: invalid portable Rclone commit variant", backupasset.ErrInvalidState)
	}
	parentEmpty := value.ParentRecoveryPointID == "" && value.ParentCommitDigest == "" && value.ParentManifestDigest == ""
	parentFull := backupasset.ValidateOpaqueID(value.ParentRecoveryPointID) == nil && validTaggedDigest(value.ParentCommitDigest) && validTaggedDigest(value.ParentManifestDigest)
	if !parentEmpty && !parentFull {
		return fmt.Errorf("%w: incomplete portable Rclone commit parent", backupasset.ErrInvalidState)
	}
	return nil
}

func (value RcloneNativeCommitV1) Validate(pointDeadline time.Time) error {
	if (value.CommitKey == "") != (value.CommitVersionID == "") || strings.ContainsRune(value.CommitKey, '\x00') || strings.ContainsRune(value.CommitVersionID, '\x00') ||
		!validTaggedDigest(value.CommitContentDigest) || !validTaggedDigest(value.ManifestControlGraphDigest) || !validTaggedDigest(value.PointViewDigest) ||
		!validTaggedDigest(value.MutationLedgerDigest) || !validTaggedDigest(value.B0VersionGraphDigest) || !validTaggedDigest(value.B1VersionGraphDigest) ||
		!validTaggedDigest(value.ExactReadProofDigest) || !validTaggedDigest(value.VersioningDigest) || !validTaggedDigest(value.LifecycleDigest) ||
		!validTaggedDigest(value.BucketEncryptionDigest) || !validTaggedDigest(value.EncryptionEvidenceDigest) ||
		!validTaggedDigest(value.RoleSessionIdentityDigest) || value.CapabilityRevision == 0 ||
		value.CredentialRevision == 0 || !validTaggedPublicationTime(value.SessionExpiresAt) ||
		!value.SessionExpiresAt.After(pointDeadline) {
		return fmt.Errorf("%w: invalid native Rclone commit variant", backupasset.ErrInvalidState)
	}
	kmsEmpty := value.ActiveKeyDigest == "" && value.RetainedReadKeySetDigest == "" && value.KMSCapabilityRevision == 0
	kmsFull := validTaggedDigest(value.ActiveKeyDigest) && validTaggedDigest(value.RetainedReadKeySetDigest) && value.KMSCapabilityRevision != 0
	if !kmsEmpty && !kmsFull {
		return fmt.Errorf("%w: incomplete native Rclone KMS commit evidence", backupasset.ErrInvalidState)
	}
	return nil
}

func (value RcloneManifestV1) Validate() error {
	if value.FailureCode != "" {
		if backupasset.ValidatePublicationFailureCode(value.FailureCode) != nil || value.ManifestIndexDigest != "" || len(value.ManifestChunkDigests) != 0 ||
			value.EntryCount != 0 || value.LogicalBytes != 0 || value.FidelityEvidenceDigest != "" {
			return fmt.Errorf("%w: invalid failed Rclone manifest", backupasset.ErrInvalidState)
		}
		return nil
	}
	if !validTaggedDigest(value.ManifestIndexDigest) || !validRcloneManifestDigestSlice(value.ManifestChunkDigests, value.EntryCount == 0 && value.LogicalBytes == 0) || !validTaggedDigest(value.FidelityEvidenceDigest) {
		return fmt.Errorf("%w: invalid Rclone manifest", backupasset.ErrInvalidState)
	}
	return nil
}

func validRcloneManifestDigestSlice(values []string, allowEmpty bool) bool {
	if len(values) == 0 {
		return allowEmpty
	}
	if len(values) > 4096 {
		return false
	}
	for _, value := range values {
		if !validTaggedDigest(value) {
			return false
		}
	}
	return true
}

func (value RcloneReconcileV1) Validate() error {
	switch value.State {
	case RcloneReconcileAbsent, RcloneReconcileIncomplete:
		if value.Commit != nil || value.Manifest != nil {
			return fmt.Errorf("%w: non-committed Rclone reconciliation returned evidence", backupasset.ErrInvalidState)
		}
		return nil
	case RcloneReconcileProviderCommitted:
		if value.Commit == nil || value.Manifest == nil || value.Commit.Validate() != nil || value.Manifest.Validate() != nil ||
			value.Commit.ManifestIndexDigest != value.Manifest.ManifestIndexDigest ||
			!reflect.DeepEqual(value.Commit.ManifestChunkDigests, value.Manifest.ManifestChunkDigests) ||
			value.Commit.ManifestEntryCount != value.Manifest.EntryCount || value.Commit.LogicalBytes != value.Manifest.LogicalBytes ||
			value.Commit.FidelityEvidenceDigest != value.Manifest.FidelityEvidenceDigest {
			return fmt.Errorf("%w: invalid committed Rclone reconciliation", backupasset.ErrInvalidState)
		}
		return nil
	default:
		return fmt.Errorf("%w: unsupported Rclone reconciliation state", backupasset.ErrInvalidState)
	}
}

type rcloneAttemptWireV1 struct {
	SchemaVersion              int                             `json:"schema_version"`
	LayoutVersion              int                             `json:"layout_version"`
	MinimumRuntimeRevision     int                             `json:"minimum_runtime_revision"`
	Provider                   backupasset.ProviderKind        `json:"provider"`
	RepositoryID               string                          `json:"repository_id"`
	TaskRepositoryLinkID       string                          `json:"task_repository_link_id"`
	RecoveryPointID            string                          `json:"recovery_point_id"`
	AttemptID                  string                          `json:"attempt_id"`
	TaskID                     uint                            `json:"task_id"`
	TaskRunID                  uint                            `json:"task_run_id"`
	Trigger                    string                          `json:"trigger"`
	PublicationMode            backupasset.TaskPublicationMode `json:"publication_mode"`
	ImportedBaseline           bool                            `json:"imported_baseline"`
	CaptureStartedAt           time.Time                       `json:"capture_started_at"`
	PreparedAt                 time.Time                       `json:"prepared_at"`
	PointDeadlineAt            time.Time                       `json:"point_deadline_at"`
	ExpectedTaskRevision       uint64                          `json:"expected_task_revision"`
	BindingRevision            uint64                          `json:"binding_revision"`
	ConfigRevision             uint64                          `json:"config_revision"`
	ConfigDigest               string                          `json:"config_digest"`
	CapabilityRevision         uint64                          `json:"capability_revision"`
	CredentialRevision         uint64                          `json:"credential_revision"`
	PreflightID                string                          `json:"preflight_id"`
	PreflightRevision          uint64                          `json:"preflight_revision"`
	PreflightDigest            string                          `json:"preflight_digest"`
	ManifestSchemaRevision     uint64                          `json:"manifest_schema_revision"`
	ManifestLimitsRevision     uint64                          `json:"manifest_limits_revision"`
	ManifestLimitsDigest       string                          `json:"manifest_limits_digest"`
	RepositoryIdentityDigest   string                          `json:"repository_identity_digest"`
	ManagedRootIdentityDigest  string                          `json:"managed_root_identity_digest"`
	ChildFenceDigest           string                          `json:"child_fence_digest"`
	LegacyOriginEvidenceDigest string                          `json:"legacy_origin_evidence_digest"`
	Portable                   *rclonePortableAttemptWireV1    `json:"portable,omitempty"`
	Native                     *rcloneNativeAttemptWireV1      `json:"native,omitempty"`
}

type rclonePortableAttemptWireV1 struct {
	AttemptComponent         string                        `json:"attempt_component"`
	DataComponent            string                        `json:"data_component"`
	ControlComponent         string                        `json:"control_component"`
	AttemptMarkerDigest      string                        `json:"attempt_marker_digest"`
	ExpectedConsistencyClass string                        `json:"expected_consistency_class"`
	ExpectedHashFidelity     string                        `json:"expected_hash_fidelity"`
	CopyDest                 *rclonePortableCopyDestWireV1 `json:"copy_dest,omitempty"`
}

type rclonePortableCopyDestWireV1 struct {
	ParentRecoveryPointID    string `json:"parent_recovery_point_id"`
	ParentAttemptID          string `json:"parent_attempt_id"`
	ParentDataComponent      string `json:"parent_data_component"`
	ParentCommitDigest       string `json:"parent_commit_digest"`
	ParentManifestDigest     string `json:"parent_manifest_digest"`
	ParentCapabilityRevision uint64 `json:"parent_capability_revision"`
}

type rcloneNativeAttemptWireV1 struct {
	ProfileCode                 RcloneNativeProfileCode           `json:"profile_code"`
	RegionIdentityDigest        string                            `json:"region_identity_digest"`
	BucketIdentityDigest        string                            `json:"bucket_identity_digest"`
	ManagedPrefixIdentityDigest string                            `json:"managed_prefix_identity_digest"`
	RoleSessionIdentityDigest   string                            `json:"role_session_identity_digest"`
	SessionExpiresAt            time.Time                         `json:"session_expires_at"`
	VersioningDigest            string                            `json:"versioning_digest"`
	LifecycleDigest             string                            `json:"lifecycle_digest"`
	CapabilityStableObservedAt  time.Time                         `json:"capability_stable_observed_at"`
	EncryptionProfile           RcloneNativeEncryptionProfileCode `json:"encryption_profile"`
	BucketEncryptionDigest      string                            `json:"bucket_encryption_digest"`
	ActiveKeyDigest             string                            `json:"active_key_digest"`
	RetainedReadKeySetDigest    string                            `json:"retained_read_key_set_digest"`
	KMSCapabilityRevision       uint64                            `json:"kms_capability_revision"`
	B0VersionGraphDigest        string                            `json:"b0_version_graph_digest"`
	StartMarkerIdentityDigest   string                            `json:"start_marker_identity_digest"`
	CanaryIdentityDigest        string                            `json:"canary_identity_digest"`
}

type rcloneCommitWireV1 struct {
	SchemaVersion                int                             `json:"schema_version"`
	LayoutVersion                int                             `json:"layout_version"`
	MinimumRuntimeRevision       int                             `json:"minimum_runtime_revision"`
	RepositoryID                 string                          `json:"repository_id"`
	TaskRepositoryLinkID         string                          `json:"task_repository_link_id"`
	RecoveryPointID              string                          `json:"recovery_point_id"`
	AttemptID                    string                          `json:"attempt_id"`
	PublicationMode              backupasset.TaskPublicationMode `json:"publication_mode"`
	PointDeadlineAt              time.Time                       `json:"point_deadline_at"`
	ProviderCommittedAt          time.Time                       `json:"provider_committed_at"`
	ManifestIndexDigest          string                          `json:"manifest_index_digest"`
	ManifestChunkDigests         []string                        `json:"manifest_chunk_digests"`
	ManifestEntryCount           uint64                          `json:"manifest_entry_count"`
	LogicalBytes                 uint64                          `json:"logical_bytes"`
	SourceObservationDigest      string                          `json:"source_observation_digest"`
	DestinationObservationDigest string                          `json:"destination_observation_digest"`
	ContentProofDigest           string                          `json:"content_proof_digest"`
	FidelityEvidenceDigest       string                          `json:"fidelity_evidence_digest"`
	CostEvidenceDigest           string                          `json:"cost_evidence_digest"`
	CapabilityEvidenceDigest     string                          `json:"capability_evidence_digest"`
	ChildFenceDigest             string                          `json:"child_fence_digest"`
	Portable                     *rclonePortableCommitWireV1     `json:"portable,omitempty"`
	Native                       *rcloneNativeCommitWireV1       `json:"native,omitempty"`
}

type rclonePortableCommitWireV1 struct {
	AttemptIdentityDigest      string `json:"attempt_identity_digest"`
	ControlIdentityDigest      string `json:"control_identity_digest"`
	DataIdentityDigest         string `json:"data_identity_digest"`
	AttemptMarkerDigest        string `json:"attempt_marker_digest"`
	ParentRecoveryPointID      string `json:"parent_recovery_point_id,omitempty"`
	ParentCommitDigest         string `json:"parent_commit_digest,omitempty"`
	ParentManifestDigest       string `json:"parent_manifest_digest,omitempty"`
	CommitComponent            string `json:"commit_component"`
	CommitPayloadDigest        string `json:"commit_payload_digest"`
	CommitAuthenticationDigest string `json:"commit_authentication_digest"`
	ConsistencyEvidenceDigest  string `json:"consistency_evidence_digest"`
	HashEvidenceDigest         string `json:"hash_evidence_digest"`
	DownloadVerifiedBytes      uint64 `json:"download_verified_bytes"`
}

type rcloneNativeCommitWireV1 struct {
	CommitContentDigest        string    `json:"commit_content_digest"`
	ManifestControlGraphDigest string    `json:"manifest_control_graph_digest"`
	PointViewDigest            string    `json:"point_view_digest"`
	MutationLedgerDigest       string    `json:"mutation_ledger_digest"`
	B0VersionGraphDigest       string    `json:"b0_version_graph_digest"`
	B1VersionGraphDigest       string    `json:"b1_version_graph_digest"`
	ExactReadProofDigest       string    `json:"exact_read_proof_digest"`
	VersioningDigest           string    `json:"versioning_digest"`
	LifecycleDigest            string    `json:"lifecycle_digest"`
	BucketEncryptionDigest     string    `json:"bucket_encryption_digest"`
	EncryptionEvidenceDigest   string    `json:"encryption_evidence_digest"`
	ActiveKeyDigest            string    `json:"active_key_digest"`
	RetainedReadKeySetDigest   string    `json:"retained_read_key_set_digest"`
	RoleSessionIdentityDigest  string    `json:"role_session_identity_digest"`
	CapabilityRevision         uint64    `json:"capability_revision"`
	CredentialRevision         uint64    `json:"credential_revision"`
	KMSCapabilityRevision      uint64    `json:"kms_capability_revision"`
	SessionExpiresAt           time.Time `json:"session_expires_at"`
}

func rcloneAttemptToWire(value RcloneAttemptV1) *rcloneAttemptWireV1 {
	wire := &rcloneAttemptWireV1{
		SchemaVersion: value.SchemaVersion, LayoutVersion: value.LayoutVersion, MinimumRuntimeRevision: value.MinimumRuntimeRevision,
		Provider: value.Provider, RepositoryID: value.RepositoryID, TaskRepositoryLinkID: value.TaskRepositoryLinkID,
		RecoveryPointID: value.RecoveryPointID, AttemptID: value.AttemptID, TaskID: value.TaskID, TaskRunID: value.TaskRunID,
		Trigger: value.Trigger, PublicationMode: value.PublicationMode, ImportedBaseline: value.ImportedBaseline,
		CaptureStartedAt: value.CaptureStartedAt.UTC(), PreparedAt: value.PreparedAt.UTC(), PointDeadlineAt: value.PointDeadlineAt.UTC(),
		ExpectedTaskRevision: value.ExpectedTaskRevision, BindingRevision: value.BindingRevision, ConfigRevision: value.ConfigRevision,
		ConfigDigest: value.ConfigDigest, CapabilityRevision: value.CapabilityRevision, CredentialRevision: value.CredentialRevision,
		PreflightID: value.PreflightID, PreflightRevision: value.PreflightRevision, PreflightDigest: value.PreflightDigest,
		ManifestSchemaRevision: value.ManifestSchemaRevision, ManifestLimitsRevision: value.ManifestLimitsRevision,
		ManifestLimitsDigest: value.ManifestLimitsDigest, RepositoryIdentityDigest: value.RepositoryIdentityDigest,
		ManagedRootIdentityDigest: value.ManagedRootIdentityDigest, ChildFenceDigest: value.ChildFenceDigest,
		LegacyOriginEvidenceDigest: value.LegacyOriginEvidenceDigest,
	}
	if value.Portable != nil {
		portable := &rclonePortableAttemptWireV1{
			AttemptComponent: value.Portable.AttemptComponent, DataComponent: value.Portable.DataComponent,
			ControlComponent: value.Portable.ControlComponent, AttemptMarkerDigest: value.Portable.AttemptMarkerDigest,
			ExpectedConsistencyClass: value.Portable.ExpectedConsistencyClass, ExpectedHashFidelity: value.Portable.ExpectedHashFidelity,
		}
		if value.Portable.CopyDest != nil {
			portable.CopyDest = &rclonePortableCopyDestWireV1{
				ParentRecoveryPointID: value.Portable.CopyDest.ParentRecoveryPointID, ParentAttemptID: value.Portable.CopyDest.ParentAttemptID,
				ParentDataComponent: value.Portable.CopyDest.ParentDataComponent, ParentCommitDigest: value.Portable.CopyDest.ParentCommitDigest,
				ParentManifestDigest: value.Portable.CopyDest.ParentManifestDigest, ParentCapabilityRevision: value.Portable.CopyDest.ParentCapabilityRevision,
			}
		}
		wire.Portable = portable
	}
	if value.Native != nil {
		wire.Native = &rcloneNativeAttemptWireV1{
			ProfileCode: value.Native.ProfileCode, RegionIdentityDigest: value.Native.RegionIdentityDigest,
			BucketIdentityDigest: value.Native.BucketIdentityDigest, ManagedPrefixIdentityDigest: value.Native.ManagedPrefixIdentityDigest,
			RoleSessionIdentityDigest: value.Native.RoleSessionIdentityDigest, SessionExpiresAt: value.Native.SessionExpiresAt.UTC(),
			VersioningDigest: value.Native.VersioningDigest, LifecycleDigest: value.Native.LifecycleDigest,
			CapabilityStableObservedAt: value.Native.CapabilityStableObservedAt.UTC(), EncryptionProfile: value.Native.EncryptionProfile,
			BucketEncryptionDigest: value.Native.BucketEncryptionDigest, ActiveKeyDigest: value.Native.ActiveKeyDigest,
			RetainedReadKeySetDigest: value.Native.RetainedReadKeySetDigest, KMSCapabilityRevision: value.Native.KMSCapabilityRevision,
			B0VersionGraphDigest: value.Native.B0VersionGraphDigest, StartMarkerIdentityDigest: value.Native.StartMarkerIdentityDigest,
			CanaryIdentityDigest: value.Native.CanaryIdentityDigest,
		}
	}
	return wire
}

func rcloneAttemptFromWire(wire *rcloneAttemptWireV1) *RcloneAttemptV1 {
	if wire == nil {
		return nil
	}
	value := &RcloneAttemptV1{
		SchemaVersion: wire.SchemaVersion, LayoutVersion: wire.LayoutVersion, MinimumRuntimeRevision: wire.MinimumRuntimeRevision,
		Provider: wire.Provider, RepositoryID: wire.RepositoryID, TaskRepositoryLinkID: wire.TaskRepositoryLinkID,
		RecoveryPointID: wire.RecoveryPointID, AttemptID: wire.AttemptID, TaskID: wire.TaskID, TaskRunID: wire.TaskRunID,
		Trigger: wire.Trigger, PublicationMode: wire.PublicationMode, ImportedBaseline: wire.ImportedBaseline,
		CaptureStartedAt: wire.CaptureStartedAt.UTC(), PreparedAt: wire.PreparedAt.UTC(), PointDeadlineAt: wire.PointDeadlineAt.UTC(),
		ExpectedTaskRevision: wire.ExpectedTaskRevision, BindingRevision: wire.BindingRevision, ConfigRevision: wire.ConfigRevision,
		ConfigDigest: wire.ConfigDigest, CapabilityRevision: wire.CapabilityRevision, CredentialRevision: wire.CredentialRevision,
		PreflightID: wire.PreflightID, PreflightRevision: wire.PreflightRevision, PreflightDigest: wire.PreflightDigest,
		ManifestSchemaRevision: wire.ManifestSchemaRevision, ManifestLimitsRevision: wire.ManifestLimitsRevision,
		ManifestLimitsDigest: wire.ManifestLimitsDigest, RepositoryIdentityDigest: wire.RepositoryIdentityDigest,
		ManagedRootIdentityDigest: wire.ManagedRootIdentityDigest, ChildFenceDigest: wire.ChildFenceDigest,
		LegacyOriginEvidenceDigest: wire.LegacyOriginEvidenceDigest,
	}
	if wire.Portable != nil {
		value.Portable = &RclonePortableAttemptV1{
			AttemptComponent: wire.Portable.AttemptComponent, DataComponent: wire.Portable.DataComponent,
			ControlComponent: wire.Portable.ControlComponent, AttemptMarkerDigest: wire.Portable.AttemptMarkerDigest,
			ExpectedConsistencyClass: wire.Portable.ExpectedConsistencyClass, ExpectedHashFidelity: wire.Portable.ExpectedHashFidelity,
		}
		if wire.Portable.CopyDest != nil {
			value.Portable.CopyDest = &RclonePortableCopyDestV1{
				ParentRecoveryPointID: wire.Portable.CopyDest.ParentRecoveryPointID, ParentAttemptID: wire.Portable.CopyDest.ParentAttemptID,
				ParentDataComponent: wire.Portable.CopyDest.ParentDataComponent, ParentCommitDigest: wire.Portable.CopyDest.ParentCommitDigest,
				ParentManifestDigest: wire.Portable.CopyDest.ParentManifestDigest, ParentCapabilityRevision: wire.Portable.CopyDest.ParentCapabilityRevision,
			}
		}
	}
	if wire.Native != nil {
		value.Native = &RcloneNativeAttemptV1{
			ProfileCode: wire.Native.ProfileCode, RegionIdentityDigest: wire.Native.RegionIdentityDigest,
			BucketIdentityDigest: wire.Native.BucketIdentityDigest, ManagedPrefixIdentityDigest: wire.Native.ManagedPrefixIdentityDigest,
			RoleSessionIdentityDigest: wire.Native.RoleSessionIdentityDigest, SessionExpiresAt: wire.Native.SessionExpiresAt.UTC(),
			VersioningDigest: wire.Native.VersioningDigest, LifecycleDigest: wire.Native.LifecycleDigest,
			CapabilityStableObservedAt: wire.Native.CapabilityStableObservedAt.UTC(), EncryptionProfile: wire.Native.EncryptionProfile,
			BucketEncryptionDigest: wire.Native.BucketEncryptionDigest, ActiveKeyDigest: wire.Native.ActiveKeyDigest,
			RetainedReadKeySetDigest: wire.Native.RetainedReadKeySetDigest, KMSCapabilityRevision: wire.Native.KMSCapabilityRevision,
			B0VersionGraphDigest: wire.Native.B0VersionGraphDigest, StartMarkerIdentityDigest: wire.Native.StartMarkerIdentityDigest,
			CanaryIdentityDigest: wire.Native.CanaryIdentityDigest,
		}
	}
	return value
}

func rcloneCommitToWire(value RcloneCommitV1) *rcloneCommitWireV1 {
	wire := &rcloneCommitWireV1{
		SchemaVersion: value.SchemaVersion, LayoutVersion: value.LayoutVersion, MinimumRuntimeRevision: value.MinimumRuntimeRevision,
		RepositoryID: value.RepositoryID, TaskRepositoryLinkID: value.TaskRepositoryLinkID, RecoveryPointID: value.RecoveryPointID,
		AttemptID: value.AttemptID, PublicationMode: value.PublicationMode, PointDeadlineAt: value.PointDeadlineAt.UTC(),
		ProviderCommittedAt: value.ProviderCommittedAt.UTC(), ManifestIndexDigest: value.ManifestIndexDigest,
		ManifestChunkDigests: append([]string(nil), value.ManifestChunkDigests...), ManifestEntryCount: value.ManifestEntryCount,
		LogicalBytes: value.LogicalBytes, SourceObservationDigest: value.SourceObservationDigest,
		DestinationObservationDigest: value.DestinationObservationDigest, ContentProofDigest: value.ContentProofDigest,
		FidelityEvidenceDigest: value.FidelityEvidenceDigest, CostEvidenceDigest: value.CostEvidenceDigest,
		CapabilityEvidenceDigest: value.CapabilityEvidenceDigest, ChildFenceDigest: value.ChildFenceDigest,
	}
	if value.Portable != nil {
		wire.Portable = &rclonePortableCommitWireV1{
			AttemptIdentityDigest: value.Portable.AttemptIdentityDigest, ControlIdentityDigest: value.Portable.ControlIdentityDigest,
			DataIdentityDigest: value.Portable.DataIdentityDigest, AttemptMarkerDigest: value.Portable.AttemptMarkerDigest,
			ParentRecoveryPointID: value.Portable.ParentRecoveryPointID, ParentCommitDigest: value.Portable.ParentCommitDigest,
			ParentManifestDigest: value.Portable.ParentManifestDigest, CommitComponent: value.Portable.CommitComponent,
			CommitPayloadDigest: value.Portable.CommitPayloadDigest, CommitAuthenticationDigest: value.Portable.CommitAuthenticationDigest,
			ConsistencyEvidenceDigest: value.Portable.ConsistencyEvidenceDigest, HashEvidenceDigest: value.Portable.HashEvidenceDigest,
			DownloadVerifiedBytes: value.Portable.DownloadVerifiedBytes,
		}
	}
	if value.Native != nil {
		wire.Native = &rcloneNativeCommitWireV1{
			CommitContentDigest: value.Native.CommitContentDigest, ManifestControlGraphDigest: value.Native.ManifestControlGraphDigest,
			PointViewDigest: value.Native.PointViewDigest, MutationLedgerDigest: value.Native.MutationLedgerDigest,
			B0VersionGraphDigest: value.Native.B0VersionGraphDigest, B1VersionGraphDigest: value.Native.B1VersionGraphDigest,
			ExactReadProofDigest: value.Native.ExactReadProofDigest, VersioningDigest: value.Native.VersioningDigest,
			LifecycleDigest: value.Native.LifecycleDigest, BucketEncryptionDigest: value.Native.BucketEncryptionDigest,
			EncryptionEvidenceDigest: value.Native.EncryptionEvidenceDigest, ActiveKeyDigest: value.Native.ActiveKeyDigest,
			RetainedReadKeySetDigest: value.Native.RetainedReadKeySetDigest, RoleSessionIdentityDigest: value.Native.RoleSessionIdentityDigest,
			CapabilityRevision: value.Native.CapabilityRevision, CredentialRevision: value.Native.CredentialRevision,
			KMSCapabilityRevision: value.Native.KMSCapabilityRevision, SessionExpiresAt: value.Native.SessionExpiresAt.UTC(),
		}
	}
	return wire
}

func rcloneCommitFromWire(wire *rcloneCommitWireV1) *RcloneCommitV1 {
	if wire == nil {
		return nil
	}
	value := &RcloneCommitV1{
		SchemaVersion: wire.SchemaVersion, LayoutVersion: wire.LayoutVersion, MinimumRuntimeRevision: wire.MinimumRuntimeRevision,
		RepositoryID: wire.RepositoryID, TaskRepositoryLinkID: wire.TaskRepositoryLinkID, RecoveryPointID: wire.RecoveryPointID,
		AttemptID: wire.AttemptID, PublicationMode: wire.PublicationMode, PointDeadlineAt: wire.PointDeadlineAt.UTC(),
		ProviderCommittedAt: wire.ProviderCommittedAt.UTC(), ManifestIndexDigest: wire.ManifestIndexDigest,
		ManifestChunkDigests: append([]string(nil), wire.ManifestChunkDigests...), ManifestEntryCount: wire.ManifestEntryCount,
		LogicalBytes: wire.LogicalBytes, SourceObservationDigest: wire.SourceObservationDigest,
		DestinationObservationDigest: wire.DestinationObservationDigest, ContentProofDigest: wire.ContentProofDigest,
		FidelityEvidenceDigest: wire.FidelityEvidenceDigest, CostEvidenceDigest: wire.CostEvidenceDigest,
		CapabilityEvidenceDigest: wire.CapabilityEvidenceDigest, ChildFenceDigest: wire.ChildFenceDigest,
	}
	if wire.Portable != nil {
		value.Portable = &RclonePortableCommitV1{
			AttemptIdentityDigest: wire.Portable.AttemptIdentityDigest, ControlIdentityDigest: wire.Portable.ControlIdentityDigest,
			DataIdentityDigest: wire.Portable.DataIdentityDigest, AttemptMarkerDigest: wire.Portable.AttemptMarkerDigest,
			ParentRecoveryPointID: wire.Portable.ParentRecoveryPointID, ParentCommitDigest: wire.Portable.ParentCommitDigest,
			ParentManifestDigest: wire.Portable.ParentManifestDigest, CommitComponent: wire.Portable.CommitComponent,
			CommitPayloadDigest: wire.Portable.CommitPayloadDigest, CommitAuthenticationDigest: wire.Portable.CommitAuthenticationDigest,
			ConsistencyEvidenceDigest: wire.Portable.ConsistencyEvidenceDigest, HashEvidenceDigest: wire.Portable.HashEvidenceDigest,
			DownloadVerifiedBytes: wire.Portable.DownloadVerifiedBytes,
		}
	}
	if wire.Native != nil {
		value.Native = &RcloneNativeCommitV1{
			CommitContentDigest: wire.Native.CommitContentDigest, ManifestControlGraphDigest: wire.Native.ManifestControlGraphDigest,
			PointViewDigest: wire.Native.PointViewDigest, MutationLedgerDigest: wire.Native.MutationLedgerDigest,
			B0VersionGraphDigest: wire.Native.B0VersionGraphDigest, B1VersionGraphDigest: wire.Native.B1VersionGraphDigest,
			ExactReadProofDigest: wire.Native.ExactReadProofDigest, VersioningDigest: wire.Native.VersioningDigest,
			LifecycleDigest: wire.Native.LifecycleDigest, BucketEncryptionDigest: wire.Native.BucketEncryptionDigest,
			EncryptionEvidenceDigest: wire.Native.EncryptionEvidenceDigest, ActiveKeyDigest: wire.Native.ActiveKeyDigest,
			RetainedReadKeySetDigest: wire.Native.RetainedReadKeySetDigest, RoleSessionIdentityDigest: wire.Native.RoleSessionIdentityDigest,
			CapabilityRevision: wire.Native.CapabilityRevision, CredentialRevision: wire.Native.CredentialRevision,
			KMSCapabilityRevision: wire.Native.KMSCapabilityRevision, SessionExpiresAt: wire.Native.SessionExpiresAt.UTC(),
		}
	}
	return value
}
