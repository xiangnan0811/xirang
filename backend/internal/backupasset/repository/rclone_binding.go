package repository

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/model"
)

const (
	managedRcloneBindingDocumentVersion      = 3
	managedRcloneLayoutRevisionV1            = "rclone-publication:v1"
	managedRcloneMinimumRuntimeRevisionV1    = 1
	managedRcloneBoundConfigMaxBytes         = 64 << 10
	managedRcloneTaskPolicyMaxBytes          = 64 << 10
	managedRcloneRetainedReadKeyMaximum      = 32
	managedRcloneRetainedReadKeyBytesMaximum = 16 << 10
)

type managedRcloneBindingDocumentV3 struct {
	Version                   int                             `json:"version"`
	Provider                  backupasset.ProviderKind        `json:"provider"`
	IdentityClass             provider.IdentityClass          `json:"identity_class"`
	TaskID                    uint                            `json:"task_id"`
	NodeID                    uint                            `json:"node_id"`
	RepositoryID              string                          `json:"repository_id"`
	TaskRepositoryLinkID      string                          `json:"task_repository_link_id"`
	LayoutRevision            string                          `json:"layout_revision"`
	MinimumRuntimeRevision    int                             `json:"minimum_runtime_revision"`
	PublicationMode           backupasset.TaskPublicationMode `json:"publication_mode"`
	BindingRevision           uint64                          `json:"binding_revision"`
	ConfigRevision            uint64                          `json:"config_revision"`
	CapabilityRevision        uint64                          `json:"capability_revision"`
	CredentialRevision        uint64                          `json:"credential_revision"`
	PreflightID               string                          `json:"preflight_id"`
	PreflightRevision         uint64                          `json:"preflight_revision"`
	PreflightDigest           string                          `json:"preflight_digest"`
	PreflightExpiresAt        time.Time                       `json:"preflight_expires_at"`
	ManagedRootIdentityDigest string                          `json:"managed_root_identity_digest"`
	RepositoryMarkerDigest    string                          `json:"repository_marker_digest"`
	LegacyLocatorDigest       string                          `json:"legacy_locator_digest"`
	LegacyBindingV1           string                          `json:"legacy_binding_v1"`
	LegacyBindingDigest       string                          `json:"legacy_binding_digest"`
	LegacyTaskPolicy          string                          `json:"legacy_task_policy"`
	LegacyTaskPolicyDigest    string                          `json:"legacy_task_policy_digest"`
	RollbackPrepared          bool                            `json:"rollback_prepared"`
	IdentitySalt              string                          `json:"identity_salt"`
	Portable                  *managedRclonePortableBindingV3 `json:"portable,omitempty"`
	Native                    *managedRcloneNativeBindingV3   `json:"native,omitempty"`
}

type managedRclonePortableBindingV3 struct {
	ManagedRootLocator     string   `json:"managed_root_locator"`
	TargetRemote           string   `json:"target_remote"`
	BoundConfig            string   `json:"bound_config"`
	ConfigDigest           string   `json:"config_digest"`
	Backend                string   `json:"backend"`
	DependencyRemotes      []string `json:"dependency_remotes"`
	ClassificationRevision int      `json:"classification_revision"`
}

type managedRcloneNativeBootstrapMode string

const (
	managedRcloneBootstrapWorkloadChain managedRcloneNativeBootstrapMode = "workload_chain"
	managedRcloneBootstrapStaticSTS     managedRcloneNativeBootstrapMode = "static_sts_bootstrap"
)

type managedRcloneNativeBootstrapV3 struct {
	Mode     managedRcloneNativeBootstrapMode   `json:"mode"`
	Workload *managedRcloneWorkloadBootstrapV3  `json:"workload,omitempty"`
	Static   *managedRcloneStaticSTSBootstrapV3 `json:"static_sts_bootstrap,omitempty"`
}

type managedRcloneWorkloadBootstrapV3 struct{}

type managedRcloneStaticSTSBootstrapV3 struct {
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
}

type managedRcloneKMSReadKeyV3 struct {
	KeyARN    string `json:"key_arn"`
	KeyDigest string `json:"key_digest"`
}

type managedRcloneNativeBindingV3 struct {
	ProfileCode                    provider.RcloneNativeProfileCode           `json:"profile_code"`
	Region                         string                                     `json:"region"`
	Bucket                         string                                     `json:"bucket"`
	ManagedPrefix                  string                                     `json:"managed_prefix"`
	RegionIdentityDigest           string                                     `json:"region_identity_digest"`
	BucketIdentityDigest           string                                     `json:"bucket_identity_digest"`
	ManagedPrefixIdentityDigest    string                                     `json:"managed_prefix_identity_digest"`
	RoleARN                        string                                     `json:"role_arn"`
	ExternalID                     string                                     `json:"external_id"`
	Bootstrap                      *managedRcloneNativeBootstrapV3            `json:"bootstrap"`
	VersioningDigest               string                                     `json:"versioning_digest"`
	LifecycleDigest                string                                     `json:"lifecycle_digest"`
	CapabilityStableObservedAt     time.Time                                  `json:"capability_stable_observed_at"`
	EncryptionProfile              provider.RcloneNativeEncryptionProfileCode `json:"encryption_profile"`
	BucketEncryptionDigest         string                                     `json:"bucket_encryption_digest"`
	BucketKeyEnabled               bool                                       `json:"bucket_key_enabled"`
	CanaryEncryptionEvidenceDigest string                                     `json:"canary_encryption_evidence_digest"`
	ActiveKMSKeyARN                string                                     `json:"active_kms_key_arn,omitempty"`
	ActiveKMSKeyDigest             string                                     `json:"active_kms_key_digest,omitempty"`
	KMSCapabilityRevision          uint64                                     `json:"kms_capability_revision,omitempty"`
	RetainedReadKeys               []managedRcloneKMSReadKeyV3                `json:"retained_read_keys,omitempty"`
}

type managedRcloneBindingAssociation struct {
	Task       model.Task
	Link       model.TaskRepositoryLink
	Repository model.BackupRepository
}

func encodeManagedRcloneBindingDocumentV3(document managedRcloneBindingDocumentV3) (string, error) {
	if err := validateManagedRcloneBindingDocumentV3(document); err != nil {
		return "", err
	}
	payload, err := json.Marshal(document)
	if err != nil {
		return "", fmt.Errorf("%w: encode managed Rclone binding document", backupasset.ErrInvalidState)
	}
	return string(payload), nil
}

func decodeManagedRcloneBindingDocumentV3(payload string) (managedRcloneBindingDocumentV3, error) {
	if err := rejectDuplicateOrNullJSONMembers(payload); err != nil {
		return managedRcloneBindingDocumentV3{}, fmt.Errorf("%w: invalid managed Rclone binding document", backupasset.ErrInvalidState)
	}
	var document managedRcloneBindingDocumentV3
	decoder := json.NewDecoder(strings.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return managedRcloneBindingDocumentV3{}, fmt.Errorf("%w: invalid managed Rclone binding document", backupasset.ErrInvalidState)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return managedRcloneBindingDocumentV3{}, fmt.Errorf("%w: trailing managed Rclone binding data", backupasset.ErrInvalidState)
	}
	if err := validateManagedRcloneBindingDocumentV3(document); err != nil {
		return managedRcloneBindingDocumentV3{}, err
	}
	return document, nil
}

func validateManagedRcloneBindingDocumentV3(document managedRcloneBindingDocumentV3) error {
	if document.Version != managedRcloneBindingDocumentVersion || document.Provider != backupasset.ProviderRclone ||
		document.IdentityClass != provider.IdentityXirangManagedRepository || document.TaskID == 0 || document.NodeID == 0 ||
		backupasset.ValidateOpaqueID(document.RepositoryID) != nil || backupasset.ValidateOpaqueID(document.TaskRepositoryLinkID) != nil ||
		document.LayoutRevision != managedRcloneLayoutRevisionV1 || document.MinimumRuntimeRevision != managedRcloneMinimumRuntimeRevisionV1 ||
		document.BindingRevision == 0 || document.ConfigRevision == 0 || document.CapabilityRevision == 0 || document.CredentialRevision == 0 ||
		backupasset.ValidateOpaqueID(document.PreflightID) != nil || document.PreflightRevision == 0 ||
		!isLowerHex64(document.PreflightDigest) || !validManagedRcloneUTCTime(document.PreflightExpiresAt) ||
		!isLowerHex64(document.ManagedRootIdentityDigest) || !isLowerHex64(document.RepositoryMarkerDigest) ||
		!isLowerHex64(document.LegacyLocatorDigest) || !isLowerHex64(document.LegacyBindingDigest) ||
		!isLowerHex64(document.LegacyTaskPolicyDigest) {
		return invalidManagedRcloneBinding()
	}
	salt, err := hexDecodeSalt(document.IdentitySalt)
	if err != nil {
		return invalidManagedRcloneBinding()
	}
	legacy, err := decodeBindingDocument(document.LegacyBindingV1)
	if err != nil || legacy.Version != bindingDocumentVersion || legacy.Provider != backupasset.ProviderRclone ||
		legacy.IdentityClass != provider.IdentityTaskScopedEndpoint || legacy.TaskID != document.TaskID || legacy.NodeID != document.NodeID ||
		legacy.IdentitySalt != document.IdentitySalt || strings.TrimSpace(legacy.Locator) == "" ||
		legacy.ConfigSource != provider.RcloneConfigNodeDefault {
		return invalidManagedRcloneBinding()
	}
	if document.LegacyLocatorDigest != managedRcloneBindingDigest(salt, "legacy-locator", legacy.Locator) ||
		document.LegacyBindingDigest != managedRcloneBindingDigest(salt, "legacy-binding", document.LegacyBindingV1) ||
		document.LegacyTaskPolicyDigest != managedRcloneBindingDigest(salt, "legacy-task-policy", document.LegacyTaskPolicy) ||
		!validManagedRcloneTaskPolicySnapshot(document.LegacyTaskPolicy) {
		return invalidManagedRcloneBinding()
	}

	switch document.PublicationMode {
	case backupasset.PublicationVersionedPrefix:
		if document.Portable == nil || document.Native != nil || validateManagedRclonePortableBinding(*document.Portable, legacy.Locator, salt) != nil {
			return invalidManagedRcloneBinding()
		}
	case backupasset.PublicationNativeObjectVersions:
		if document.Native == nil || document.Portable != nil || validateManagedRcloneNativeBinding(*document.Native, document.PreflightExpiresAt) != nil {
			return invalidManagedRcloneBinding()
		}
	default:
		return invalidManagedRcloneBinding()
	}
	return nil
}

func validateManagedRclonePortableBinding(value managedRclonePortableBindingV3, legacyLocator string, salt []byte) error {
	if value.ManagedRootLocator == "" || value.ManagedRootLocator == legacyLocator ||
		!strings.HasPrefix(value.ManagedRootLocator, value.TargetRemote+":") ||
		!isLowerHex64(value.ConfigDigest) || value.ClassificationRevision <= 0 ||
		len(value.BoundConfig) == 0 || len(value.BoundConfig) > managedRcloneBoundConfigMaxBytes {
		return invalidManagedRcloneBinding()
	}
	if _, err := provider.NewRclonePrivateLocator(value.ManagedRootLocator); err != nil {
		return invalidManagedRcloneBinding()
	}
	bound, err := provider.ValidateRcloneBoundConfigV1744([]byte(value.BoundConfig), value.TargetRemote, salt, managedRcloneBoundConfigMaxBytes)
	if err != nil || bound.KeyedDigest() != value.ConfigDigest || bound.Backend() != value.Backend ||
		bound.ClassificationRevision() != value.ClassificationRevision || !slices.Equal(bound.DependencyRemotes(), value.DependencyRemotes) {
		return invalidManagedRcloneBinding()
	}
	return nil
}

func validateManagedRcloneNativeBinding(value managedRcloneNativeBindingV3, preflightExpiresAt time.Time) error {
	roleAccount, ok := managedRcloneAWSRoleAccount(value.RoleARN)
	if value.ProfileCode != provider.RcloneNativeAWSS3GeneralPurposeV1 || !validManagedRcloneAWSRegion(value.Region) ||
		!validManagedRcloneS3Bucket(value.Bucket) || !validManagedRcloneS3Prefix(value.ManagedPrefix) || !ok ||
		!validManagedRcloneExternalID(value.ExternalID) || value.Bootstrap == nil ||
		!isLowerHex64(value.RegionIdentityDigest) || !isLowerHex64(value.BucketIdentityDigest) ||
		!isLowerHex64(value.ManagedPrefixIdentityDigest) || !isLowerHex64(value.VersioningDigest) ||
		!isLowerHex64(value.LifecycleDigest) || !validManagedRcloneUTCTime(value.CapabilityStableObservedAt) ||
		!value.CapabilityStableObservedAt.Before(preflightExpiresAt) || !isLowerHex64(value.BucketEncryptionDigest) ||
		!isLowerHex64(value.CanaryEncryptionEvidenceDigest) || validateManagedRcloneBootstrap(*value.Bootstrap) != nil {
		return invalidManagedRcloneBinding()
	}
	switch value.EncryptionProfile {
	case provider.RcloneNativeSSES3V1:
		if value.BucketKeyEnabled || value.ActiveKMSKeyARN != "" || value.ActiveKMSKeyDigest != "" ||
			value.KMSCapabilityRevision != 0 || len(value.RetainedReadKeys) != 0 {
			return invalidManagedRcloneBinding()
		}
	case provider.RcloneNativeSSEKMSV1:
		if value.KMSCapabilityRevision == 0 || !isLowerHex64(value.ActiveKMSKeyDigest) ||
			!validManagedRcloneKMSKeyARN(value.ActiveKMSKeyARN, value.Region, roleAccount) ||
			validateManagedRcloneReadKeyRing(value.RetainedReadKeys, value.Region, roleAccount, value.ActiveKMSKeyARN) != nil {
			return invalidManagedRcloneBinding()
		}
	default:
		return invalidManagedRcloneBinding()
	}
	return nil
}

func validateManagedRcloneBootstrap(value managedRcloneNativeBootstrapV3) error {
	switch value.Mode {
	case managedRcloneBootstrapWorkloadChain:
		if value.Workload == nil || value.Static != nil {
			return invalidManagedRcloneBinding()
		}
	case managedRcloneBootstrapStaticSTS:
		if value.Workload != nil || value.Static == nil ||
			!validManagedRcloneSecret(value.Static.AccessKeyID, 16, 256) ||
			!validManagedRcloneSecret(value.Static.SecretAccessKey, 16, 4096) {
			return invalidManagedRcloneBinding()
		}
	default:
		return invalidManagedRcloneBinding()
	}
	return nil
}

func validateManagedRcloneReadKeyRing(values []managedRcloneKMSReadKeyV3, region, account, activeARN string) error {
	if len(values) > managedRcloneRetainedReadKeyMaximum {
		return invalidManagedRcloneBinding()
	}
	seen := make(map[string]struct{}, len(values))
	bytes := 0
	previous := ""
	for _, value := range values {
		bytes += len(value.KeyARN) + len(value.KeyDigest)
		if !validManagedRcloneKMSKeyARN(value.KeyARN, region, account) || !isLowerHex64(value.KeyDigest) ||
			value.KeyARN == activeARN || value.KeyARN <= previous {
			return invalidManagedRcloneBinding()
		}
		if _, exists := seen[value.KeyARN]; exists {
			return invalidManagedRcloneBinding()
		}
		seen[value.KeyARN] = struct{}{}
		previous = value.KeyARN
	}
	if bytes > managedRcloneRetainedReadKeyBytesMaximum {
		return invalidManagedRcloneBinding()
	}
	return nil
}

func validateManagedRcloneBindingAssociation(document managedRcloneBindingDocumentV3, association managedRcloneBindingAssociation) error {
	expectedTarget := ""
	if document.RollbackPrepared {
		legacy, err := decodeBindingDocument(document.LegacyBindingV1)
		if err != nil || association.Task.Enabled {
			return fmt.Errorf("%w: managed Rclone rollback association drift", backupasset.ErrConflict)
		}
		expectedTarget = legacy.Locator
	}
	if validateManagedRcloneBindingDocumentV3(document) != nil || association.Task.ID != document.TaskID ||
		association.Task.NodeID != document.NodeID || association.Link.TaskID == nil || *association.Link.TaskID != document.TaskID ||
		association.Link.ID != document.TaskRepositoryLinkID || association.Link.RepositoryID != document.RepositoryID ||
		association.Link.PublicationMode != string(document.PublicationMode) || association.Link.UnlinkedAt != nil ||
		association.Repository.ID != document.RepositoryID || association.Repository.ProviderKind != string(backupasset.ProviderRclone) ||
		association.Repository.VersionMode != string(versionModeForRclonePublication(document.PublicationMode)) ||
		strings.TrimSpace(association.Task.RsyncTarget) != expectedTarget {
		return fmt.Errorf("%w: managed Rclone binding identity drift", backupasset.ErrConflict)
	}
	return nil
}

func versionModeForRclonePublication(mode backupasset.TaskPublicationMode) backupasset.VersionMode {
	switch mode {
	case backupasset.PublicationVersionedPrefix:
		return backupasset.VersionVersionedPrefix
	case backupasset.PublicationNativeObjectVersions:
		return backupasset.VersionNativeObjectVersions
	default:
		return ""
	}
}

func managedRcloneRepositoryIdentity(document managedRcloneBindingDocumentV3) (string, error) {
	if err := validateManagedRcloneBindingDocumentV3(document); err != nil {
		return "", err
	}
	salt, err := hexDecodeSalt(document.IdentitySalt)
	if err != nil {
		return "", err
	}
	return provider.DeriveScopedIdentity(salt, provider.ScopedIdentityDocument{
		Provider: backupasset.ProviderRclone, TaskID: document.TaskID, NodeID: document.NodeID,
		EndpointFacts: []string{
			"identity_class:xirang_managed_repository",
			"layout:" + document.LayoutRevision,
			"managed_root_identity:" + document.ManagedRootIdentityDigest,
			"repository:" + document.RepositoryID,
			"publication_mode:" + string(document.PublicationMode),
		},
	})
}

func managedRcloneBindingDigest(key []byte, label, value string) string {
	mac := hmac.New(sha256.New, key)
	_, _ = io.WriteString(mac, "xirang-managed-rclone-binding-v3\n")
	_, _ = io.WriteString(mac, label+"\n")
	_, _ = io.WriteString(mac, value)
	return hex.EncodeToString(mac.Sum(nil))
}

func validManagedRcloneTaskPolicySnapshot(value string) bool {
	if value == "" || len(value) > managedRcloneTaskPolicyMaxBytes || !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') {
		return false
	}
	if trimmed := strings.TrimSpace(value); !strings.HasPrefix(trimmed, "{") {
		return false
	}
	return rejectDuplicateOrNullJSONMembers(value) == nil
}

func rejectDuplicateOrNullJSONMembers(payload string) error {
	decoder := json.NewDecoder(strings.NewReader(payload))
	first, err := decoder.Token()
	if err != nil {
		return err
	}
	if err := walkStrictJSONValue(decoder, first); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("trailing JSON data")
		}
		return err
	}
	return nil
}

func walkStrictJSONValue(decoder *json.Decoder, token json.Token) error {
	if token == nil {
		return fmt.Errorf("null JSON member")
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			nameToken, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := nameToken.(string)
			if !ok {
				return fmt.Errorf("JSON member name is not a string")
			}
			if _, exists := seen[name]; exists {
				return fmt.Errorf("duplicate JSON member")
			}
			seen[name] = struct{}{}
			value, err := decoder.Token()
			if err != nil {
				return err
			}
			if err := walkStrictJSONValue(decoder, value); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return fmt.Errorf("invalid JSON object terminator")
		}
	case '[':
		for decoder.More() {
			value, err := decoder.Token()
			if err != nil {
				return err
			}
			if err := walkStrictJSONValue(decoder, value); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return fmt.Errorf("invalid JSON array terminator")
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter")
	}
	return nil
}

func validManagedRcloneUTCTime(value time.Time) bool {
	return !value.IsZero() && value.Location() == time.UTC && value.Equal(value.UTC())
}

func validManagedRcloneAWSRegion(value string) bool {
	if value == "" || len(value) > 64 || value[0] < 'a' || value[0] > 'z' {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '-' {
			continue
		}
		return false
	}
	return strings.Count(value, "-") >= 2
}

func validManagedRcloneS3Bucket(value string) bool {
	if len(value) < 3 || len(value) > 63 || value[0] == '-' || value[len(value)-1] == '-' || strings.Contains(value, "..") {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '-' || character == '.' {
			continue
		}
		return false
	}
	return true
}

func validManagedRcloneS3Prefix(value string) bool {
	return value != "" && len(value) <= 1024 && value[0] != '/' && strings.HasSuffix(value, "/") &&
		utf8.ValidString(value) && strings.TrimSpace(value) == value && !strings.ContainsRune(value, '\x00') &&
		!strings.Contains(value, "//") && !strings.Contains(value, "../")
}

func managedRcloneAWSRoleAccount(value string) (string, bool) {
	const prefix = "arn:aws:iam::"
	if !strings.HasPrefix(value, prefix) || strings.ContainsAny(value, "\r\n\x00") {
		return "", false
	}
	remainder := strings.TrimPrefix(value, prefix)
	separator := strings.Index(remainder, ":role/")
	if separator != 12 || len(remainder) <= separator+len(":role/") {
		return "", false
	}
	account := remainder[:separator]
	for _, character := range account {
		if character < '0' || character > '9' {
			return "", false
		}
	}
	return account, true
}

func validManagedRcloneKMSKeyARN(value, region, account string) bool {
	prefix := "arn:aws:kms:" + region + ":" + account + ":key/"
	return strings.HasPrefix(value, prefix) && len(value) > len(prefix) && len(value) <= 2048 &&
		!strings.ContainsAny(value, "\r\n\x00") && !strings.Contains(value, ":alias/")
}

func validManagedRcloneExternalID(value string) bool {
	if len(value) < 2 || len(value) > 1224 || strings.TrimSpace(value) != value || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if character < 0x21 || character == 0x7f {
			return false
		}
	}
	return true
}

func validManagedRcloneSecret(value string, minimum, maximum int) bool {
	return len(value) >= minimum && len(value) <= maximum && utf8.ValidString(value) &&
		strings.TrimSpace(value) == value && !strings.ContainsRune(value, '\x00') && !strings.ContainsAny(value, "\r\n")
}

func invalidManagedRcloneBinding() error {
	return fmt.Errorf("%w: invalid managed Rclone binding document", backupasset.ErrInvalidState)
}
