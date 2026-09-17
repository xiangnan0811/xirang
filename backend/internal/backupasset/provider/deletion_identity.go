package provider

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"
	"xirang/backend/internal/sshutil"
)

const (
	DeletionTargetIdentitySchemaVersion = 1
	deletionTargetIdentityDomain        = "xirang/lifecycle/effect-target.v1"
	deletionTargetEndpointDomain        = "xirang/lifecycle/effect-target-endpoints.v1"
	deletionTargetPrivateDomain         = "xirang/lifecycle/effect-target-private.v1"
	deletionTargetRemoteDomain          = "xirang/lifecycle/effect-target-remote-command.v1"
	deletionTargetMaxFrameBytes         = 1 << 20
)

// DeletionTargetIdentityInput joins the lifecycle-owned identity with the
// provider-owned deletion request. DeletePointRequest intentionally does not
// contain the recovery-point or lifecycle-attempt identity, so callers must
// supply those outer values explicitly rather than hashing the request alone.
type DeletionTargetIdentityInput struct {
	RecoveryPointID    string
	AttemptID          string
	Operation          backupasset.LifecycleOperation
	RepositoryIdentity string
	Request            DeletePointRequest
}

// DeletionTargetProjection is the safe, persistence-ready projection of a
// deletion target. It contains only identifiers and keyed/digest fingerprints;
// locators, credentials, marker keys, command arguments, clients, and audit
// contexts are never returned.
type DeletionTargetProjection struct {
	SchemaVersion                     int                            `json:"schema_version"`
	RepositoryID                      string                         `json:"repository_id"`
	RecoveryPointID                   string                         `json:"recovery_point_id"`
	AttemptID                         string                         `json:"attempt_id"`
	Operation                         backupasset.LifecycleOperation `json:"operation"`
	Provider                          backupasset.ProviderKind       `json:"provider"`
	RepositoryIdentity                string                         `json:"repository_identity"`
	CapabilityRevision                int                            `json:"capability_revision"`
	SourceRevision                    string                         `json:"source_revision"`
	ExpectedSourceRevision            string                         `json:"expected_source_revision"`
	AccessRepositoryID                string                         `json:"access_repository_id"`
	AccessTaskID                      uint                           `json:"access_task_id"`
	AccessNodeID                      uint                           `json:"access_node_id"`
	EndpointFactsFingerprint          string                         `json:"endpoint_facts_fingerprint"`
	ProviderAuthorityFingerprint      string                         `json:"provider_authority_fingerprint"`
	RemoteCommandAuthorityFingerprint string                         `json:"remote_command_authority_fingerprint"`
	PrivateBindingFingerprint         string                         `json:"private_binding_fingerprint"`
	Digest                            string                         `json:"digest"`
}

// CanonicalDeletionTargetProjection returns the safe provider-owned identity
// projection for a lifecycle deletion target.
func CanonicalDeletionTargetProjection(input DeletionTargetIdentityInput) (DeletionTargetProjection, error) {
	projection, err := canonicalDeletionTargetProjection(input)
	if err != nil {
		return DeletionTargetProjection{}, err
	}
	return projection, nil
}

// DeletionTargetIdentityDigest returns the only value that should be persisted
// for a deletion target identity.
func DeletionTargetIdentityDigest(input DeletionTargetIdentityInput) (string, error) {
	projection, err := canonicalDeletionTargetProjection(input)
	if err != nil {
		return "", err
	}
	return projection.Digest, nil
}

// CompareDeletionTargetAuthority compares two lifecycle/provider snapshots. A
// malformed or incomplete snapshot is never considered equal.
func CompareDeletionTargetAuthority(left, right DeletionTargetIdentityInput) error {
	leftProjection, err := canonicalDeletionTargetProjection(left)
	if err != nil {
		return err
	}
	rightProjection, err := canonicalDeletionTargetProjection(right)
	if err != nil {
		return err
	}
	if leftProjection.Digest != rightProjection.Digest {
		return ErrDeletePointIdentityConflict
	}
	return nil
}

// DeletionTargetAuthoritiesEqual is the non-error comparator for callers that
// only need a fail-closed equality check.
func DeletionTargetAuthoritiesEqual(left, right DeletionTargetIdentityInput) bool {
	return CompareDeletionTargetAuthority(left, right) == nil
}

func canonicalDeletionTargetProjection(input DeletionTargetIdentityInput) (DeletionTargetProjection, error) {
	if err := validateDeletionTargetIdentityInput(input); err != nil {
		return DeletionTargetProjection{}, err
	}
	request := input.Request
	access := request.Snapshot.Access
	salt := access.IdentitySalt

	endpointFingerprint, err := deletionTargetEndpointFingerprint(salt, access.EndpointFacts)
	if err != nil {
		return DeletionTargetProjection{}, err
	}
	providerFingerprint, providerMaterial, err := deletionTargetProviderAuthority(input)
	if err != nil {
		return DeletionTargetProjection{}, err
	}
	remoteFingerprint, err := deletionTargetRemoteCommandFingerprint(salt, access)
	if err != nil {
		return DeletionTargetProjection{}, err
	}
	privateFingerprint, err := deletionTargetPrivateFingerprint(salt, input, providerMaterial, remoteFingerprint)
	if err != nil {
		return DeletionTargetProjection{}, err
	}

	projection := DeletionTargetProjection{
		SchemaVersion:                     DeletionTargetIdentitySchemaVersion,
		RepositoryID:                      request.Snapshot.RepositoryID,
		RecoveryPointID:                   input.RecoveryPointID,
		AttemptID:                         input.AttemptID,
		Operation:                         input.Operation,
		Provider:                          access.Provider,
		RepositoryIdentity:                input.RepositoryIdentity,
		CapabilityRevision:                request.Snapshot.CapabilityRevision,
		SourceRevision:                    request.Snapshot.SourceRevision,
		ExpectedSourceRevision:            request.ExpectedSourceRevision,
		AccessRepositoryID:                access.RepositoryID,
		AccessTaskID:                      access.TaskID,
		AccessNodeID:                      access.NodeID,
		EndpointFactsFingerprint:          endpointFingerprint,
		ProviderAuthorityFingerprint:      providerFingerprint,
		RemoteCommandAuthorityFingerprint: remoteFingerprint,
		PrivateBindingFingerprint:         privateFingerprint,
	}
	projection.Digest, err = deletionTargetPublicDigest(projection)
	if err != nil {
		return DeletionTargetProjection{}, err
	}
	return projection, nil
}

func validateDeletionTargetIdentityInput(input DeletionTargetIdentityInput) error {
	if backupasset.ValidateOpaqueID(input.RecoveryPointID) != nil || backupasset.ValidateOpaqueID(input.AttemptID) != nil {
		return deletionTargetIdentityError("lifecycle identity is unavailable")
	}
	if err := backupasset.ValidateLifecycleOperation(input.Operation); err != nil {
		return deletionTargetIdentityError("lifecycle operation is unavailable")
	}
	if !validDeletionIdentityString(input.RepositoryIdentity, 256, false) {
		return deletionTargetIdentityError("repository identity is unavailable")
	}
	if err := input.Request.Validate(); err != nil {
		return deletionTargetIdentityError("deletion request is unavailable")
	}
	if err := input.Request.requireSourceRevision(); err != nil {
		return deletionTargetIdentityError("deletion source revision is unavailable")
	}
	if input.Request.OperationID != input.AttemptID {
		return deletionTargetIdentityError("lifecycle attempt identity changed")
	}
	if snapshotIdentity := strings.TrimSpace(input.Request.Snapshot.RepositoryIdentity); snapshotIdentity != "" && snapshotIdentity != input.RepositoryIdentity {
		return deletionTargetIdentityError("repository identity changed")
	}
	access := input.Request.Snapshot.Access
	if access.RepositoryID != input.Request.Snapshot.RepositoryID || access.TaskID == 0 || access.NodeID == 0 || len(access.IdentitySalt) != IdentitySaltBytes {
		return deletionTargetIdentityError("deletion access identity is unavailable")
	}
	if len(access.EndpointFacts) == 0 {
		return deletionTargetIdentityError("deletion endpoint authority is unavailable")
	}
	return nil
}

func deletionTargetIdentityError(reason string) error {
	return fmt.Errorf("%w: %s", ErrDeletePointIdentityConflict, reason)
}

func validDeletionIdentityString(value string, maxBytes int, allowEmpty bool) bool {
	if value == "" {
		return allowEmpty
	}
	return len(value) <= maxBytes && utf8.ValidString(value) && !strings.ContainsAny(value, "\x00\r\n")
}

type deletionIdentityFrame struct {
	bytes.Buffer
	err error
}

func newDeletionIdentityFrame(domain string) *deletionIdentityFrame {
	frame := &deletionIdentityFrame{}
	frame.addString("domain", domain)
	return frame
}

func (frame *deletionIdentityFrame) writeRaw(value []byte) {
	if frame == nil || frame.err != nil {
		return
	}
	if len(value) > deletionTargetMaxFrameBytes-frame.Len() {
		frame.err = fmt.Errorf("%w: deletion identity material is too large", backupasset.ErrInvalidState)
		return
	}
	_, _ = frame.Write(value)
}

func (frame *deletionIdentityFrame) addBytes(label string, value []byte) {
	frame.addTypeWithoutValue(label, 0x01)
	frame.addLengthAndValue(value)
}

func (frame *deletionIdentityFrame) addString(label, value string) {
	frame.addTypeWithoutValue(label, 0x02)
	frame.addLengthAndValue([]byte(value))
}

func (frame *deletionIdentityFrame) addLengthAndValue(value []byte) {
	if frame == nil || frame.err != nil {
		return
	}
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	frame.writeRaw(length[:])
	frame.writeRaw(value)
}

func (frame *deletionIdentityFrame) addTypeWithoutValue(label string, kind byte) {
	if frame == nil || frame.err != nil {
		return
	}
	frame.addLengthAndValue([]byte(label))
	frame.writeRaw([]byte{kind})
}

func (frame *deletionIdentityFrame) addUint64(label string, value uint64) {
	frame.addTypeWithoutValue(label, 0x03)
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], value)
	frame.writeRaw(encoded[:])
}

func (frame *deletionIdentityFrame) addBool(label string, value bool) {
	frame.addTypeWithoutValue(label, 0x05)
	if value {
		frame.writeRaw([]byte{1})
		return
	}
	frame.writeRaw([]byte{0})
}

func (frame *deletionIdentityFrame) addOptionalBytes(label string, value []byte) {
	frame.addBool(label+".present", value != nil)
	frame.addBytes(label, value)
}

func (frame *deletionIdentityFrame) addJSON(label string, value any) {
	if frame == nil || frame.err != nil {
		return
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		frame.err = fmt.Errorf("%w: canonicalize deletion authority", backupasset.ErrInvalidState)
		return
	}
	frame.addBytes(label, encoded)
}

func (frame *deletionIdentityFrame) bytesValue() ([]byte, error) {
	if frame == nil {
		return nil, fmt.Errorf("%w: deletion identity frame is unavailable", backupasset.ErrInvalidState)
	}
	if frame.err != nil {
		return nil, frame.err
	}
	if frame.Len() == 0 || frame.Len() > deletionTargetMaxFrameBytes {
		return nil, fmt.Errorf("%w: deletion identity frame is unavailable", backupasset.ErrInvalidState)
	}
	return append([]byte(nil), frame.Bytes()...), nil
}

func deletionTargetEndpointFingerprint(salt []byte, facts []string) (string, error) {
	canonicalFacts := append([]string(nil), facts...)
	sort.Strings(canonicalFacts)
	for index, fact := range canonicalFacts {
		if !validDeletionIdentityString(fact, 2048, false) || strings.TrimSpace(fact) != fact || (index > 0 && fact == canonicalFacts[index-1]) {
			return "", deletionTargetIdentityError("deletion endpoint authority is invalid")
		}
	}
	frame := newDeletionIdentityFrame(deletionTargetEndpointDomain)
	frame.addUint64("fact_count", uint64(len(canonicalFacts)))
	for index, fact := range canonicalFacts {
		frame.addString(fmt.Sprintf("fact:%d", index), fact)
	}
	material, err := frame.bytesValue()
	if err != nil {
		return "", err
	}
	return DeriveConfigFingerprint(salt, material)
}

func deletionTargetProviderAuthority(input DeletionTargetIdentityInput) (string, []byte, error) {
	request := input.Request
	access := request.Snapshot.Access
	providerFrame := newDeletionIdentityFrame("xirang/lifecycle/effect-target-provider-authority.v1")
	providerFrame.addString("provider", string(access.Provider))

	switch access.Provider {
	case backupasset.ProviderRestic:
		runtimeAccess, ok := access.AdapterData.(ResticRuntimeAccess)
		if !ok || !lowerHex(runtimeAccess.NativeRepositoryID, 64) {
			return "", nil, deletionTargetIdentityError("Restic deletion authority is unavailable")
		}
		providerFrame.addString("native_repository_id", runtimeAccess.NativeRepositoryID)

	case backupasset.ProviderRsync:
		rsyncAccess, ok := access.AdapterData.(RsyncPointDeletionAccess)
		if !ok || rsyncAccess.Attempt.Validate() != nil || !validRsyncTreeMarkerKey(rsyncAccess.MarkerKey) ||
			!validTaggedDigest(rsyncAccess.CommitMarkerDigest) || !validTaggedDigest(rsyncAccess.SourceFingerprint) {
			return "", nil, deletionTargetIdentityError("Rsync deletion authority is unavailable")
		}
		managedRoot, err := normalizeRsyncManagedRoot(rsyncAccess.ManagedRoot)
		if err != nil || request.Point.Native != rsyncAccess.Attempt.FinalComponent ||
			rsyncAccess.SourceFingerprint != request.ExpectedSourceRevision ||
			rsyncAccess.Attempt.RecoveryPointID != input.RecoveryPointID {
			return "", nil, deletionTargetIdentityError("Rsync deletion authority changed")
		}
		providerFrame.addString("managed_root", managedRoot)
		providerFrame.addBytes("marker_key", rsyncAccess.MarkerKey)
		providerFrame.addString("commit_marker_digest", rsyncAccess.CommitMarkerDigest)
		providerFrame.addString("source_fingerprint", rsyncAccess.SourceFingerprint)
		providerFrame.addJSON("attempt", rsyncAccess.Attempt)

	case backupasset.ProviderRclone:
		switch access.AdapterData.(type) {
		case RclonePrefixDeletionAccess:
			rcloneAccess, err := rclonePrefixDeletionAccess(request)
			if err != nil || rcloneAccess.Attempt.RecoveryPointID != input.RecoveryPointID {
				return "", nil, deletionTargetIdentityError("Rclone prefix deletion authority is unavailable")
			}
			providerFrame.addString("prefix", rcloneAccess.Prefix.value)
			providerFrame.addString("marker_digest", rcloneAccess.MarkerDigest)
			providerFrame.addString("expected_backend", rcloneAccess.ExpectedBackend)
			providerFrame.addString("expected_root_identity", rcloneAccess.ExpectedRootIdentity)
			providerFrame.addString("config_digest", rcloneAccess.ConfigDigest)
			providerFrame.addBytes("marker_key", rcloneAccess.MarkerKey)
			providerFrame.addString("expected_attempt_root", rcloneAccess.ExpectedAttemptRoot)
			providerFrame.addJSON("attempt", rcloneAccess.Attempt)
			providerFrame.addJSON("commit", rcloneAccess.Commit)
		case RcloneNativeDeletionAccess:
			rcloneAccess, err := rcloneNativeDeletionAccess(request)
			if err != nil {
				return "", nil, deletionTargetIdentityError("Rclone native deletion authority is unavailable")
			}
			versions := append([]RcloneNativeExactVersion(nil), rcloneAccess.Versions...)
			sort.Slice(versions, func(i, j int) bool {
				if versions[i].PhysicalKey == versions[j].PhysicalKey {
					return versions[i].VersionID < versions[j].VersionID
				}
				return versions[i].PhysicalKey < versions[j].PhysicalKey
			})
			providerFrame.addString("authority_digest", rcloneAccess.AuthorityDigest)
			addCanonicalRcloneNativeVersionSet(providerFrame, versions)
		default:
			return "", nil, deletionTargetIdentityError("Rclone deletion authority is unavailable")
		}
	default:
		return "", nil, deletionTargetIdentityError("deletion provider authority is unavailable")
	}

	if providerFrame.err != nil {
		return "", nil, providerFrame.err
	}
	providerMaterial, err := providerFrame.bytesValue()
	if err != nil {
		return "", nil, err
	}
	providerFingerprint, err := DeriveConfigFingerprint(access.IdentitySalt, providerMaterial)
	if err != nil {
		return "", nil, err
	}
	return providerFingerprint, providerMaterial, nil
}

func deletionTargetRemoteCommandFingerprint(salt []byte, access AccessBinding) (string, error) {
	var command *RemoteCommandAccess
	switch value := access.AdapterData.(type) {
	case ResticRuntimeAccess:
		command = value.Command
	case RsyncPointDeletionAccess:
		command = value.Command
	case RclonePrefixDeletionAccess:
		command = value.Command
	case RcloneNativeDeletionAccess:
		command = value.Command
	default:
		return "", deletionTargetIdentityError("deletion command authority is unavailable")
	}
	if command == nil {
		if access.Provider != backupasset.ProviderRsync {
			return "", deletionTargetIdentityError("deletion command authority is unavailable")
		}
		frame := newDeletionIdentityFrame(deletionTargetRemoteDomain)
		frame.addString("mode", "not_applicable")
		material, err := frame.bytesValue()
		if err != nil {
			return "", err
		}
		return DeriveConfigFingerprint(salt, material)
	}
	if command.Node.ID != access.NodeID {
		return "", deletionTargetIdentityError("deletion command Node authority changed")
	}
	return canonicalRemoteCommandAuthorityFingerprint(salt, command)
}

func canonicalRemoteCommandAuthorityFingerprint(salt []byte, command *RemoteCommandAccess) (string, error) {
	if command == nil {
		return "", deletionTargetIdentityError("deletion command authority is unavailable")
	}
	node := command.Node
	if node.ID == 0 || !validDeletionIdentityString(node.Host, 2048, false) || node.Port <= 0 || node.Port > 65535 ||
		!validDeletionIdentityString(strings.TrimSpace(node.Username), 512, false) {
		return "", deletionTargetIdentityError("deletion command endpoint authority is unavailable")
	}
	authType := strings.ToLower(strings.TrimSpace(node.AuthType))
	if authType != "password" && authType != "key" {
		return "", deletionTargetIdentityError("deletion command authentication authority is unavailable")
	}
	if !validDeletionIdentityString(node.BasePath, 16<<10, true) || !validDeletionIdentityString(node.BackupDir, 16<<10, true) {
		return "", deletionTargetIdentityError("deletion command path authority is unavailable")
	}
	nodeTags, err := canonicalAuthorityTagList(node.Tags)
	if err != nil {
		return "", deletionTargetIdentityError("deletion command tag authority is unavailable")
	}

	frame := newDeletionIdentityFrame(deletionTargetRemoteDomain)
	frame.addUint64("node_id", uint64(node.ID))
	frame.addString("host", node.Host)
	frame.addUint64("port", uint64(node.Port))
	frame.addString("username", strings.TrimSpace(node.Username))
	frame.addString("auth_type", authType)
	frame.addString("base_path", node.BasePath)
	frame.addString("backup_dir", node.BackupDir)
	frame.addBool("use_sudo", node.UseSudo)
	frame.addString("tags", nodeTags)
	frame.addOptionalBytes("password", []byte(node.Password))
	frame.addOptionalBytes("private_key", []byte(node.PrivateKey))
	if authType != "key" {
		frame.addBool("ssh_key_authority.present", false)
	} else if node.SSHKeyID == nil {
		frame.addBool("ssh_key_authority.present", false)
		if node.SSHKey != nil {
			return "", deletionTargetIdentityError("deletion SSH key lineage is unavailable")
		}
	} else {
		if *node.SSHKeyID == 0 || node.SSHKey == nil || node.SSHKey.ID != *node.SSHKeyID {
			return "", deletionTargetIdentityError("deletion SSH key lineage is unavailable")
		}
		frame.addBool("ssh_key_authority.present", true)
		frame.addUint64("ssh_key_id", uint64(*node.SSHKeyID))
		if err := appendCanonicalSSHKey(frame, *node.SSHKey); err != nil {
			return "", err
		}
	}
	if authType == "password" && node.Password == "" {
		return "", deletionTargetIdentityError("deletion password authority is unavailable")
	}
	if authType == "key" && node.PrivateKey == "" && (node.SSHKey == nil || node.SSHKey.PrivateKey == "") {
		return "", deletionTargetIdentityError("deletion key authority is unavailable")
	}
	material, err := frame.bytesValue()
	if err != nil {
		return "", err
	}
	return DeriveConfigFingerprint(salt, material)
}

func appendCanonicalSSHKey(frame *deletionIdentityFrame, key model.SSHKey) error {
	if key.ID == 0 || !validDeletionIdentityString(key.Username, 512, true) || !validDeletionIdentityString(key.KeyType, 128, true) ||
		!validDeletionIdentityString(key.Fingerprint, 512, true) {
		return deletionTargetIdentityError("deletion SSH key authority is unavailable")
	}
	purposes, err := sshutil.NormalizePurposeList(key.AllowedPurposes)
	if err != nil {
		return deletionTargetIdentityError("deletion SSH key policy is unavailable")
	}
	nodeIDs, err := sshutil.NormalizeNodeIDList(key.AllowedNodeIDs)
	if err != nil {
		return deletionTargetIdentityError("deletion SSH key policy is unavailable")
	}
	tags := sshutil.NormalizeTagList(key.AllowedNodeTags)
	purposes = sortCanonicalCSV(purposes)
	nodeIDs = sortCanonicalCSV(nodeIDs)
	tags = sortCanonicalCSV(tags)
	frame.addUint64("ssh_key.id", uint64(key.ID))
	frame.addString("ssh_key.username", key.Username)
	frame.addString("ssh_key.key_type", key.KeyType)
	frame.addOptionalBytes("ssh_key.private_key", []byte(key.PrivateKey))
	frame.addString("ssh_key.fingerprint", key.Fingerprint)
	frame.addBool("ssh_key.disabled", key.Disabled)
	if key.ExpiresAt == nil {
		frame.addBool("ssh_key.expires_at.present", false)
	} else {
		frame.addBool("ssh_key.expires_at.present", true)
		frame.addString("ssh_key.expires_at", key.ExpiresAt.UTC().Format(time.RFC3339Nano))
	}
	frame.addString("ssh_key.allowed_purposes", purposes)
	frame.addString("ssh_key.allowed_node_ids", nodeIDs)
	frame.addString("ssh_key.allowed_node_tags", tags)
	if key.PrivateKey == "" {
		return deletionTargetIdentityError("deletion SSH key authority is unavailable")
	}
	return nil
}

func canonicalAuthorityTagList(raw string) (string, error) {
	if !validDeletionIdentityString(raw, 4096, true) {
		return "", deletionTargetIdentityError("deletion tag authority is unavailable")
	}
	return sortCanonicalCSV(sshutil.NormalizeTagList(raw)), nil
}

func sortCanonicalCSV(raw string) string {
	if raw == "" {
		return ""
	}
	parts := strings.Split(raw, ",")
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

func addCanonicalRcloneNativeVersionSet(frame *deletionIdentityFrame, versions []RcloneNativeExactVersion) {
	frame.addUint64("version_count", uint64(len(versions)))
	for _, version := range versions {
		frame.addString("version.physical_key", version.PhysicalKey)
		frame.addString("version.version_id", version.VersionID)
	}
}

func deletionTargetPrivateFingerprint(salt []byte, input DeletionTargetIdentityInput, providerMaterial []byte, remoteFingerprint string) (string, error) {
	frame := newDeletionIdentityFrame(deletionTargetPrivateDomain)
	request := input.Request
	access := request.Snapshot.Access
	frame.addString("point_native", request.Point.Native)
	frame.addOptionalBytes("access_locator", []byte(access.Locator))
	frame.addOptionalBytes("access_config", access.Config)
	frame.addOptionalBytes("access_secret", access.Secret)
	frame.addBytes("provider_authority", providerMaterial)
	frame.addString("remote_command_authority_fingerprint", remoteFingerprint)
	material, err := frame.bytesValue()
	if err != nil {
		return "", err
	}
	return DeriveConfigFingerprint(salt, material)
}

func deletionTargetPublicDigest(projection DeletionTargetProjection) (string, error) {
	writer := backupasset.NewCanonicalSHA256()
	writer.String(deletionTargetIdentityDomain)
	writer.Uint32(uint32(projection.SchemaVersion))
	writer.String("repository_id")
	writer.String(projection.RepositoryID)
	writer.String("recovery_point_id")
	writer.String(projection.RecoveryPointID)
	writer.String("attempt_id")
	writer.String(projection.AttemptID)
	writer.String("operation")
	writer.String(string(projection.Operation))
	writer.String("provider")
	writer.String(string(projection.Provider))
	writer.String("repository_identity")
	writer.String(projection.RepositoryIdentity)
	writer.String("capability_revision")
	writer.Int64(int64(projection.CapabilityRevision))
	writer.String("source_revision")
	writer.String(projection.SourceRevision)
	writer.String("expected_source_revision")
	writer.String(projection.ExpectedSourceRevision)
	writer.String("access_repository_id")
	writer.String(projection.AccessRepositoryID)
	writer.String("access_task_id")
	writer.Uint64(uint64(projection.AccessTaskID))
	writer.String("access_node_id")
	writer.Uint64(uint64(projection.AccessNodeID))
	writer.String("endpoint_facts_fingerprint")
	writer.String(projection.EndpointFactsFingerprint)
	writer.String("provider_authority_fingerprint")
	writer.String(projection.ProviderAuthorityFingerprint)
	writer.String("remote_command_authority_fingerprint")
	writer.String(projection.RemoteCommandAuthorityFingerprint)
	writer.String("private_binding_fingerprint")
	writer.String(projection.PrivateBindingFingerprint)
	return writer.HexDigest()
}
