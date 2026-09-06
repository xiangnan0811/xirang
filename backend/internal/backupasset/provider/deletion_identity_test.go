package provider

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"
)

func TestDeletionTargetIdentityFixedVectors(t *testing.T) {
	cases := []struct {
		name  string
		input DeletionTargetIdentityInput
		want  string
	}{
		{
			name:  "restic",
			input: deletionIdentityResticInput(t),
			want:  "dcdfa99f9a42f85b7e43a3f51ac12b2942f4977e03d28cd250874c9042e24cac",
		},
		{
			name:  "rsync",
			input: deletionIdentityRsyncInput(t),
			want:  "ef309fcf3952c886b9a312c653ffee696aebfecf33fb624ab14d605025b10571",
		},
		{
			name:  "rclone-prefix",
			input: deletionIdentityRclonePrefixInput(t),
			want:  "615f616b151fdd004c1848d389b00b83771648163cb1687368a60235c97385e7",
		},
		{
			name:  "rclone-native",
			input: deletionIdentityRcloneNativeInput(t),
			want:  "1de1ea9a5b12c52b6e23809aaba5111982bc628d4fd0f01af6c631e196e26671",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := DeletionTargetIdentityDigest(testCase.input)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("deletion target identity vector: %s", got)
			if testCase.want != "" && got != testCase.want {
				t.Fatalf("digest=%q, want %q", got, testCase.want)
			}
			if len(got) != 64 || strings.ToLower(got) != got {
				t.Fatalf("digest=%q is not a lower-case SHA-256 digest", got)
			}
		})
	}
}

func TestDeletionTargetIdentityProjectionNeverExposesPrivateMaterial(t *testing.T) {
	cases := []struct {
		name  string
		input DeletionTargetIdentityInput
	}{
		{name: "restic", input: deletionIdentityResticInput(t)},
		{name: "restic-key", input: deletionIdentityWithCommandNode(deletionIdentityResticInput(t), deletionIdentityKeyNode())},
		{name: "rsync", input: deletionIdentityRsyncInput(t)},
		{name: "rclone-prefix", input: deletionIdentityRclonePrefixInput(t)},
		{name: "rclone-prefix-key", input: deletionIdentityWithCommandNode(deletionIdentityRclonePrefixInput(t), deletionIdentityKeyNode())},
		{name: "rclone-native", input: deletionIdentityRcloneNativeInput(t)},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			input := deletionIdentityRedactionInput(testCase.input)
			projection, err := CanonicalDeletionTargetProjection(input)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(projection)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(encoded, []byte(`"expected_version_fingerprint"`)) {
				t.Fatalf("projection retained removed expected version fingerprint field: %s", encoded)
			}
			digest, err := DeletionTargetIdentityDigest(input)
			if err != nil {
				t.Fatal(err)
			}
			access := input.Request.Snapshot.Access
			forbidden := []string{
				access.Locator,
				string(access.Secret),
				string(access.Config),
				string(access.IdentitySalt),
				hex.EncodeToString(access.IdentitySalt),
			}
			if access.Provider != backupasset.ProviderRsync {
				forbidden = append(forbidden, input.Request.Point.Native)
			}
			switch runtime := access.AdapterData.(type) {
			case ResticRuntimeAccess:
				if runtime.Command != nil {
					forbidden = append(forbidden, runtime.Command.Node.Password, runtime.Command.Node.PrivateKey)
					if runtime.Command.Node.SSHKey != nil {
						forbidden = append(forbidden, runtime.Command.Node.SSHKey.PrivateKey)
					}
				}
			case RsyncPointDeletionAccess:
				forbidden = append(forbidden, string(runtime.MarkerKey))
				if runtime.Command != nil {
					forbidden = append(forbidden, runtime.Command.Node.Password, runtime.Command.Node.PrivateKey)
					if runtime.Command.Node.SSHKey != nil {
						forbidden = append(forbidden, runtime.Command.Node.SSHKey.PrivateKey)
					}
				}
			case RclonePrefixDeletionAccess:
				forbidden = append(forbidden, string(runtime.MarkerKey))
				if runtime.Command != nil {
					forbidden = append(forbidden, runtime.Command.Node.Password, runtime.Command.Node.PrivateKey)
					if runtime.Command.Node.SSHKey != nil {
						forbidden = append(forbidden, runtime.Command.Node.SSHKey.PrivateKey)
					}
				}
			case RcloneNativeDeletionAccess:
				for _, version := range runtime.Versions {
					forbidden = append(forbidden, version.PhysicalKey, version.VersionID)
				}
				if runtime.Command != nil {
					forbidden = append(forbidden, runtime.Command.Node.Password, runtime.Command.Node.PrivateKey)
					if runtime.Command.Node.SSHKey != nil {
						forbidden = append(forbidden, runtime.Command.Node.SSHKey.PrivateKey)
					}
				}
			}
			publicMaterial := strings.Join([]string{
				projection.RepositoryID, projection.RecoveryPointID, projection.AttemptID,
				projection.RepositoryIdentity, projection.SourceRevision, projection.ExpectedSourceRevision,
				projection.AccessRepositoryID, projection.EndpointFactsFingerprint,
				projection.ProviderAuthorityFingerprint, projection.RemoteCommandAuthorityFingerprint,
				projection.PrivateBindingFingerprint,
			}, "\x00")
			for leftIndex, left := range forbidden {
				if left == "" {
					continue
				}
				for rightIndex := leftIndex + 1; rightIndex < len(forbidden); rightIndex++ {
					right := forbidden[rightIndex]
					if right != "" && (strings.Contains(left, right) || strings.Contains(right, left)) {
						t.Fatalf("redaction canaries overlap: %q and %q", left, right)
					}
				}
			}
			seen := make(map[string]struct{}, len(forbidden))
			for _, value := range forbidden {
				if value == "" {
					continue
				}
				if _, duplicate := seen[value]; duplicate {
					continue
				}
				seen[value] = struct{}{}
				if strings.Contains(digest, value) {
					t.Fatalf("digest exposed private material %q: digest=%s", value, digest)
				}
				if strings.Contains(publicMaterial, value) {
					t.Fatalf("projection field exposed private material %q: projection=%s", value, publicMaterial)
				}
				if bytes.Contains(encoded, []byte(value)) {
					t.Fatalf("projection exposed private material %q: projection=%s", value, encoded)
				}
			}
		})
	}
}

func deletionIdentityRedactionInput(input DeletionTargetIdentityInput) DeletionTargetIdentityInput {
	access := input.Request.Snapshot.Access
	provider := string(access.Provider)
	access.Locator = "REDACTION_LOCATOR_" + provider
	access.Secret = []byte("REDACTION_SECRET_" + provider)
	access.Config = []byte("REDACTION_CONFIG_" + provider)
	salt := bytes.Repeat([]byte{'Z'}, IdentitySaltBytes)
	copy(salt, []byte("REDACTION_SALT_"+provider))
	access.IdentitySalt = salt
	input.Request.Snapshot.Access = access

	switch access.Provider {
	case backupasset.ProviderRestic:
		input.Request.Point.Native = strings.Repeat("deadbeef", 8)
	case backupasset.ProviderRclone:
		if native, ok := access.AdapterData.(RcloneNativeDeletionAccess); ok {
			native.Versions = append([]RcloneNativeExactVersion(nil), native.Versions...)
			if len(native.Versions) > 0 {
				native.Versions[0].PhysicalKey = "REDACTION_NATIVE_PHYSICAL_KEY_ZERO"
				native.Versions[0].VersionID = "REDACTION_NATIVE_VERSION_ID_ZERO"
			}
			if len(native.Versions) > 1 {
				native.Versions[1].PhysicalKey = "REDACTION_NATIVE_PHYSICAL_KEY_ONE"
				native.Versions[1].VersionID = "REDACTION_NATIVE_VERSION_ID_ONE"
			}
			input.Request.Point.Native = "REDACTION_NATIVE_POINT"
			access.AdapterData = native
			input.Request.Snapshot.Access = access
		}
	}
	return deletionIdentityUpdateCommandNode(input, func(node *model.Node) {
		node.Password = "REDACTION_" + provider + "_NODE_PASSWORD"
		node.PrivateKey = "REDACTION_" + provider + "_NODE_PRIVATE_KEY"
		if node.SSHKey != nil {
			node.SSHKey.PrivateKey = "REDACTION_" + provider + "_SSH_PRIVATE_KEY"
		}
	})
}

func deletionIdentityUpdateCommandNode(input DeletionTargetIdentityInput, mutate func(*model.Node)) DeletionTargetIdentityInput {
	access := input.Request.Snapshot.Access
	update := func(command *RemoteCommandAccess) *RemoteCommandAccess {
		if command == nil {
			return nil
		}
		updated := *command
		if command.Node.SSHKey != nil {
			key := *command.Node.SSHKey
			updated.Node.SSHKey = &key
		}
		mutate(&updated.Node)
		return &updated
	}
	switch runtime := access.AdapterData.(type) {
	case ResticRuntimeAccess:
		runtime.Command = update(runtime.Command)
		access.AdapterData = runtime
	case RsyncPointDeletionAccess:
		runtime.Command = update(runtime.Command)
		access.AdapterData = runtime
	case RclonePrefixDeletionAccess:
		runtime.Command = update(runtime.Command)
		access.AdapterData = runtime
	case RcloneNativeDeletionAccess:
		runtime.Command = update(runtime.Command)
		access.AdapterData = runtime
	}
	input.Request.Snapshot.Access = access
	return input
}

func deletionIdentityWithCommandNode(input DeletionTargetIdentityInput, node model.Node) DeletionTargetIdentityInput {
	return deletionIdentityUpdateCommandNode(input, func(target *model.Node) {
		replacement := node
		if node.SSHKey != nil {
			key := *node.SSHKey
			replacement.SSHKey = &key
		}
		*target = replacement
	})
}

func TestDeletionTargetIdentityProviderAuthorityIsSaltKeyed(t *testing.T) {
	base := deletionIdentityRcloneNativeInput(t)
	first, err := CanonicalDeletionTargetProjection(base)
	if err != nil {
		t.Fatal(err)
	}

	alternate := base
	access := alternate.Request.Snapshot.Access
	access.IdentitySalt = bytes.Repeat([]byte{0x24}, IdentitySaltBytes)
	alternate.Request.Snapshot.Access = access
	second, err := CanonicalDeletionTargetProjection(alternate)
	if err != nil {
		t.Fatal(err)
	}
	if first.ProviderAuthorityFingerprint == second.ProviderAuthorityFingerprint {
		t.Fatal("provider authority fingerprint did not depend on identity salt")
	}
	if first.PrivateBindingFingerprint == second.PrivateBindingFingerprint {
		t.Fatal("private binding fingerprint did not depend on identity salt")
	}
	if first.Digest == second.Digest {
		t.Fatal("deletion target digest did not depend on identity salt")
	}
}

func TestDeletionTargetIdentityBindsResticRemoteCommandAuthority(t *testing.T) {
	runDeletionIdentityRemoteAuthorityMatrix(t, deletionIdentityResticInput)
}

func TestDeletionTargetIdentityBindsRclonePrefixRemoteCommandAuthority(t *testing.T) {
	runDeletionIdentityRemoteAuthorityMatrix(t, deletionIdentityRclonePrefixInput)
}

func runDeletionIdentityRemoteAuthorityMatrix(t *testing.T, baseFactory func(*testing.T) DeletionTargetIdentityInput) {
	t.Helper()
	base := baseFactory(t)
	keyBase := deletionIdentityWithCommandNode(base, deletionIdentityKeyNode())
	tests := []struct {
		name   string
		input  DeletionTargetIdentityInput
		mutate func(*model.Node)
	}{
		{name: "host", input: base, mutate: func(node *model.Node) { node.Host = "changed.identity-host.example.invalid" }},
		{name: "port", input: base, mutate: func(node *model.Node) { node.Port = 2201 }},
		{name: "username", input: base, mutate: func(node *model.Node) { node.Username = "changed-identity-user" }},
		{name: "auth_type", input: keyBase, mutate: func(node *model.Node) {
			node.AuthType = "password"
			node.Password = "FAKE_CHANGED_IDENTITY_AUTH_PASSWORD_FOR_TEST_ONLY"
		}},
		{name: "password", input: base, mutate: func(node *model.Node) {
			node.Password = "FAKE_CHANGED_IDENTITY_NODE_PASSWORD_FOR_TEST_ONLY"
		}},
		{name: "private_key", input: base, mutate: func(node *model.Node) {
			node.PrivateKey = "FAKE_CHANGED_IDENTITY_NODE_PRIVATE_KEY_FOR_TEST_ONLY"
		}},
		{name: "ssh_key_lineage", input: keyBase, mutate: func(node *model.Node) {
			alternateID := uint(18)
			node.SSHKeyID = &alternateID
			node.SSHKey.ID = alternateID
		}},
		{name: "ssh_key_username", input: keyBase, mutate: func(node *model.Node) {
			node.SSHKey.Username = "changed-identity-key-user"
		}},
		{name: "ssh_key_type", input: keyBase, mutate: func(node *model.Node) {
			node.SSHKey.KeyType = "rsa"
		}},
		{name: "ssh_key_private_key", input: keyBase, mutate: func(node *model.Node) {
			node.SSHKey.PrivateKey = "FAKE_CHANGED_IDENTITY_SSH_PRIVATE_KEY_FOR_TEST_ONLY"
		}},
		{name: "ssh_key_fingerprint", input: keyBase, mutate: func(node *model.Node) {
			node.SSHKey.Fingerprint = "SHA256:changed-identity-key"
		}},
		{name: "ssh_key_disabled", input: keyBase, mutate: func(node *model.Node) {
			node.SSHKey.Disabled = true
		}},
		{name: "ssh_key_expiry", input: keyBase, mutate: func(node *model.Node) {
			expiresAt := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
			node.SSHKey.ExpiresAt = &expiresAt
		}},
		{name: "ssh_key_allowed_purposes", input: keyBase, mutate: func(node *model.Node) {
			node.SSHKey.AllowedPurposes = "retention,probe"
		}},
		{name: "ssh_key_allowed_node_ids", input: keyBase, mutate: func(node *model.Node) {
			node.SSHKey.AllowedNodeIDs = "999"
		}},
		{name: "ssh_key_allowed_node_tags", input: keyBase, mutate: func(node *model.Node) {
			node.SSHKey.AllowedNodeTags = "changed-identity-tag"
		}},
		{name: "base_path", input: base, mutate: func(node *model.Node) {
			node.BasePath = "/changed/identity/base"
		}},
		{name: "backup_dir", input: base, mutate: func(node *model.Node) {
			node.BackupDir = "changed-identity-backup-dir"
		}},
		{name: "sudo", input: base, mutate: func(node *model.Node) {
			node.UseSudo = !node.UseSudo
		}},
		{name: "tags", input: base, mutate: func(node *model.Node) {
			node.Tags = "prod,changed-identity-tag"
		}},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			mutated := deletionIdentityUpdateCommandNode(testCase.input, testCase.mutate)
			if err := CompareDeletionTargetAuthority(testCase.input, mutated); err == nil {
				t.Fatalf("remote command authority mutation %q was accepted", testCase.name)
			}
		})
	}
}

func deletionIdentityKeyNode() model.Node {
	node := deletionIdentityNode()
	node.AuthType = "key"
	node.PrivateKey = ""
	keyID := uint(17)
	node.SSHKeyID = &keyID
	node.SSHKey = &model.SSHKey{
		ID: keyID, Name: "identity-key", Username: "identity-key-user", KeyType: "ed25519",
		PrivateKey: "FAKE_IDENTITY_SSH_PRIVATE_KEY_FOR_TEST_ONLY", Fingerprint: "SHA256:identity-key",
		AllowedPurposes: "retention", AllowedNodeIDs: "9", AllowedNodeTags: "prod,archive",
	}
	return node
}

func TestDeletionTargetIdentityIgnoresOpaqueRuntimeAndTelemetry(t *testing.T) {
	base := deletionIdentityResticInput(t)
	mutated := base
	access := mutated.Request.Snapshot.Access
	runtime := access.AdapterData.(ResticRuntimeAccess)
	node := runtime.Command.Node
	node.Name = "renamed-node"
	node.Status = "maintenance"
	node.LastSeenAt = timePtr(time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC))
	node.LastBackupAt = timePtr(time.Date(2026, 8, 18, 12, 1, 0, 0, time.UTC))
	node.LastProbeAt = timePtr(time.Date(2026, 8, 18, 12, 2, 0, 0, time.UTC))
	node.ConnectionLatency = 99
	node.DiskUsedGB = 11
	node.DiskTotalGB = 22
	node.ConsecutiveFailures = 3
	node.MaintenanceStart = timePtr(time.Date(2026, 8, 18, 12, 3, 0, 0, time.UTC))
	node.MaintenanceEnd = timePtr(time.Date(2026, 8, 18, 12, 4, 0, 0, time.UTC))
	node.ExpiryDate = timePtr(time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC))
	node.Archived = true
	node.LogPaths = `["/opaque/log"]`
	node.LogJournalctlEnabled = false
	node.LogRetentionDays = 2
	node.UpdatedAt = time.Date(2026, 8, 18, 12, 5, 0, 0, time.UTC)
	runtime.Command = &RemoteCommandAccess{Node: node}
	runtime.Command.Audit.CorrelationID = "audit-only-change"
	runtime.Command.Audit.UserID = 9001
	runtime.Command.Audit.Username = "audit-user"
	runtime.Command.Audit.Role = "audit-role"
	taskID := uint(9002)
	runtime.Command.Audit.TaskID = &taskID
	access.EndpointFacts = []string{access.EndpointFacts[1], access.EndpointFacts[0]}
	access.AdapterData = runtime
	mutated.Request.Snapshot.Access = access
	if err := CompareDeletionTargetAuthority(base, mutated); err != nil {
		t.Fatalf("opaque runtime/telemetry change changed identity: %v", err)
	}

	node.Host = "changed.example.invalid"
	runtime.Command = &RemoteCommandAccess{Node: node}
	access.AdapterData = runtime
	mutated.Request.Snapshot.Access = access
	if err := CompareDeletionTargetAuthority(base, mutated); err == nil {
		t.Fatal("material endpoint change was accepted")
	}
}

func TestDeletionTargetIdentityIgnoresRclonePrefixTelemetry(t *testing.T) {
	base := deletionIdentityRclonePrefixInput(t)
	mutated := deletionIdentityUpdateCommandNode(base, func(node *model.Node) {
		node.Name = "renamed-node"
		node.Status = "maintenance"
		node.LastSeenAt = timePtr(time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC))
		node.LastBackupAt = timePtr(time.Date(2026, 8, 18, 12, 1, 0, 0, time.UTC))
		node.LastProbeAt = timePtr(time.Date(2026, 8, 18, 12, 2, 0, 0, time.UTC))
		node.ConnectionLatency = 99
		node.DiskUsedGB = 11
		node.DiskTotalGB = 22
		node.ConsecutiveFailures = 3
		node.MaintenanceStart = timePtr(time.Date(2026, 8, 18, 12, 3, 0, 0, time.UTC))
		node.MaintenanceEnd = timePtr(time.Date(2026, 8, 18, 12, 4, 0, 0, time.UTC))
		node.ExpiryDate = timePtr(time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC))
		node.Archived = true
		node.LogPaths = `["/opaque/log"]`
		node.LogJournalctlEnabled = false
		node.LogRetentionDays = 2
		node.UpdatedAt = time.Date(2026, 8, 18, 12, 5, 0, 0, time.UTC)
	})
	access := mutated.Request.Snapshot.Access
	runtime := access.AdapterData.(RclonePrefixDeletionAccess)
	if runtime.Command == nil {
		t.Fatal("rclone prefix telemetry fixture has no command authority")
	}
	runtime.Command.Audit.CorrelationID = "audit-only-change"
	runtime.Command.Audit.UserID = 9001
	runtime.Command.Audit.Username = "audit-user"
	runtime.Command.Audit.Role = "audit-role"
	taskID := uint(9002)
	runtime.Command.Audit.TaskID = &taskID
	facts := append([]string(nil), access.EndpointFacts...)
	facts[0], facts[1] = facts[1], facts[0]
	access.EndpointFacts = facts
	access.AdapterData = runtime
	mutated.Request.Snapshot.Access = access
	if err := CompareDeletionTargetAuthority(base, mutated); err != nil {
		t.Fatalf("opaque Rclone prefix runtime/telemetry change changed identity: %v", err)
	}

	changed := deletionIdentityUpdateCommandNode(base, func(node *model.Node) {
		node.Host = "changed.example.invalid"
	})
	if err := CompareDeletionTargetAuthority(base, changed); err == nil {
		t.Fatal("material Rclone prefix endpoint change was accepted")
	}
}

func TestDeletionTargetIdentitySortsNativeVersionSet(t *testing.T) {
	base := deletionIdentityRcloneNativeInput(t)
	baseDigest, err := DeletionTargetIdentityDigest(base)
	if err != nil {
		t.Fatal(err)
	}
	mutated := base
	access := mutated.Request.Snapshot.Access
	rcloneAccess := access.AdapterData.(RcloneNativeDeletionAccess)
	rcloneAccess.Versions = append([]RcloneNativeExactVersion(nil), rcloneAccess.Versions...)
	rcloneAccess.Versions[0], rcloneAccess.Versions[1] = rcloneAccess.Versions[1], rcloneAccess.Versions[0]
	rcloneAccess.Client = &identityNoopNativeDeleter{}
	access.AdapterData = rcloneAccess
	mutated.Request.Snapshot.Access = access
	if err := CompareDeletionTargetAuthority(base, mutated); err != nil {
		t.Fatalf("version order/client pointer changed identity: %v", err)
	}
	mutatedDigest, err := DeletionTargetIdentityDigest(mutated)
	if err != nil {
		t.Fatal(err)
	}
	if mutatedDigest != baseDigest {
		t.Fatalf("permuted versions changed digest: got %q, want %q", mutatedDigest, baseDigest)
	}
}

func TestDeletionTargetIdentityBindsNativeVersionPairs(t *testing.T) {
	base := deletionIdentityRcloneNativeInput(t)
	baseDigest, err := DeletionTargetIdentityDigest(base)
	if err != nil {
		t.Fatal(err)
	}
	for _, testCase := range []struct {
		name   string
		mutate func(*RcloneNativeExactVersion)
	}{
		{name: "physical_key", mutate: func(version *RcloneNativeExactVersion) { version.PhysicalKey = "points/changed" }},
		{name: "version_id", mutate: func(version *RcloneNativeExactVersion) { version.VersionID = "v3" }},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			mutated := base
			access := mutated.Request.Snapshot.Access
			rcloneAccess := access.AdapterData.(RcloneNativeDeletionAccess)
			rcloneAccess.Versions = append([]RcloneNativeExactVersion(nil), rcloneAccess.Versions...)
			testCase.mutate(&rcloneAccess.Versions[0])
			access.AdapterData = rcloneAccess
			mutated.Request.Snapshot.Access = access

			if err := CompareDeletionTargetAuthority(base, mutated); err == nil {
				t.Fatal("native version pair mutation was accepted")
			}
			mutatedDigest, err := DeletionTargetIdentityDigest(mutated)
			if err != nil {
				t.Fatal(err)
			}
			if mutatedDigest == baseDigest {
				t.Fatalf("native version pair mutation did not change digest %q", mutatedDigest)
			}
		})
	}
}

func TestDeletionTargetIdentityNativeVersionSetDigestSmoke(t *testing.T) {
	base := deletionIdentityRcloneNativeInput(t)
	baseAccess := base.Request.Snapshot.Access
	baseNative := baseAccess.AdapterData.(RcloneNativeDeletionAccess)
	if len(baseNative.Versions) < 2 {
		t.Fatal("native identity fixture must contain at least two versions")
	}
	singleVersion := func(version RcloneNativeExactVersion) DeletionTargetIdentityInput {
		input := base
		access := input.Request.Snapshot.Access
		nativeAccess := access.AdapterData.(RcloneNativeDeletionAccess)
		nativeAccess.Versions = []RcloneNativeExactVersion{version}
		access.AdapterData = nativeAccess
		input.Request.Snapshot.Access = access
		return input
	}
	left := singleVersion(baseNative.Versions[0])
	right := singleVersion(RcloneNativeExactVersion{
		PhysicalKey: baseNative.Versions[0].PhysicalKey + "-different",
		VersionID:   baseNative.Versions[0].VersionID,
	})
	leftDigest, err := DeletionTargetIdentityDigest(left)
	if err != nil {
		t.Fatal(err)
	}
	rightDigest, err := DeletionTargetIdentityDigest(right)
	if err != nil {
		t.Fatal(err)
	}
	if leftDigest == rightDigest {
		t.Fatalf("different one-element native version sets share digest %q", leftDigest)
	}

	permuted := base
	access := permuted.Request.Snapshot.Access
	nativeAccess := access.AdapterData.(RcloneNativeDeletionAccess)
	nativeAccess.Versions = append([]RcloneNativeExactVersion(nil), nativeAccess.Versions...)
	nativeAccess.Versions[0], nativeAccess.Versions[1] = nativeAccess.Versions[1], nativeAccess.Versions[0]
	access.AdapterData = nativeAccess
	permuted.Request.Snapshot.Access = access
	permutedDigest, err := DeletionTargetIdentityDigest(permuted)
	if err != nil {
		t.Fatal(err)
	}
	baseDigest, err := DeletionTargetIdentityDigest(base)
	if err != nil {
		t.Fatal(err)
	}
	if permutedDigest != baseDigest {
		t.Fatalf("permuted native version set changed digest: got %q, want %q", permutedDigest, baseDigest)
	}
}

type identityNoopNativeDeleter struct{}

func (identityNoopNativeDeleter) ProbeExactVersion(context.Context, RcloneNativeExactVersion) (RcloneNativeVersionProbe, error) {
	return RcloneNativeVersionProbe{}, nil
}

func (identityNoopNativeDeleter) DeleteExactVersion(context.Context, RcloneNativeExactVersion) error {
	return nil
}

func deletionIdentityNode() model.Node {
	return model.Node{
		ID: 9, Name: "identity-node", Host: "backup.example.invalid", Port: 22, Username: "reader", AuthType: "password",
		Password: "FAKE_NODE_PASSWORD_FOR_TEST_ONLY", BasePath: "/data", BackupDir: "backup", Tags: "prod,archive",
		UseSudo: true,
	}
}

func deletionIdentityResticInput(t *testing.T) DeletionTargetIdentityInput {
	t.Helper()
	request := validResticDeletePointRequest(t)
	node := deletionIdentityNode()
	access := request.Snapshot.Access
	access.TaskID = 7
	access.NodeID = node.ID
	access.IdentitySalt = bytes.Repeat([]byte{0x42}, IdentitySaltBytes)
	access.EndpointFacts = []string{"task:7", "node:9"}
	runtime := access.AdapterData.(ResticRuntimeAccess)
	runtime.Command = &RemoteCommandAccess{Node: node}
	access.AdapterData = runtime
	access.Config = []byte(`{"repository_password":"FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY"}`)
	request.Snapshot.Access = access
	return DeletionTargetIdentityInput{
		RecoveryPointID: strings.Repeat("c", 32), AttemptID: request.OperationID,
		Operation:          backupasset.LifecycleRetentionExpire,
		RepositoryIdentity: NativeResticIdentityPrefix + strings.Repeat("a", 64), Request: request,
	}
}

func deletionIdentityRsyncInput(t *testing.T) DeletionTargetIdentityInput {
	t.Helper()
	attempt := rsyncTreeAttemptForMarkerTest()
	source := strings.Repeat("3", 64)
	request := DeletePointRequest{
		Snapshot: ReadSnapshot{
			RepositoryID: attempt.RepositoryID, CapabilityRevision: 1, SourceRevision: source,
			Access: AccessBinding{
				Provider: backupasset.ProviderRsync, RepositoryID: attempt.RepositoryID, TaskID: attempt.TaskID, NodeID: attempt.TaskID + 2,
				IdentitySalt: bytes.Repeat([]byte{0x42}, IdentitySaltBytes), EndpointFacts: []string{"transport:local", "task:7", "node:9"},
				AdapterData: RsyncPointDeletionAccess{
					ManagedRoot: "/var/lib/xirang", MarkerKey: bytes.Repeat([]byte{0x11}, 32), Attempt: attempt,
					CommitMarkerDigest: strings.Repeat("4", 64), SourceFingerprint: source,
					Command: &RemoteCommandAccess{Node: deletionIdentityNode()},
				},
			},
		},
		Point:                  PointLocator{Native: attempt.FinalComponent},
		ExpectedSourceRevision: source,
		OperationID:            strings.Repeat("e", 32),
	}
	return DeletionTargetIdentityInput{
		RecoveryPointID: attempt.RecoveryPointID, AttemptID: strings.Repeat("e", 32),
		Operation:          backupasset.LifecycleExplicitPurge,
		RepositoryIdentity: "rsync-managed:v1:" + strings.Repeat("a", 64), Request: request,
	}
}

func deletionIdentityRclonePrefixInput(t *testing.T) DeletionTargetIdentityInput {
	t.Helper()
	prefix := mustRclonePrivateLocatorForTest(t, "backup:managed/v1/points/"+strings.Repeat("a", 32)+"."+strings.Repeat("b", 32))
	request := validRclonePrefixDeleteRequest(t, prefix)
	node := deletionIdentityNode()
	access := request.Snapshot.Access
	access.TaskID = 7
	access.NodeID = node.ID
	access.IdentitySalt = bytes.Repeat([]byte{0x42}, IdentitySaltBytes)
	access.EndpointFacts = []string{"remote:backup:managed", "task:7", "node:9"}
	access.Locator = prefix.value
	access.Config = append([]byte(nil), access.Secret...)
	rcloneAccess := access.AdapterData.(RclonePrefixDeletionAccess)
	rcloneAccess.Command = &RemoteCommandAccess{Node: node}
	access.AdapterData = rcloneAccess
	request.Snapshot.Access = access
	return DeletionTargetIdentityInput{
		RecoveryPointID: rcloneAccess.Attempt.RecoveryPointID, AttemptID: strings.Repeat("e", 32),
		Operation:          backupasset.LifecycleRetentionExpire,
		RepositoryIdentity: "rclone-prefix:v1:" + strings.Repeat("a", 64), Request: request,
	}
}

func deletionIdentityRcloneNativeInput(t *testing.T) DeletionTargetIdentityInput {
	t.Helper()
	versions := []RcloneNativeExactVersion{{PhysicalKey: "points/p", VersionID: "v2"}, {PhysicalKey: "points/p", VersionID: "v1"}}
	request := validRcloneNativeDeleteRequest(t, versions)
	node := deletionIdentityNode()
	access := request.Snapshot.Access
	access.TaskID = 7
	access.NodeID = node.ID
	access.IdentitySalt = bytes.Repeat([]byte{0x42}, IdentitySaltBytes)
	access.EndpointFacts = []string{"remote:backup:native", "task:7", "node:9"}
	nativeAccess := access.AdapterData.(RcloneNativeDeletionAccess)
	nativeAccess.Command = &RemoteCommandAccess{Node: node}
	access.AdapterData = nativeAccess
	request.Snapshot.Access = access
	return DeletionTargetIdentityInput{
		RecoveryPointID: strings.Repeat("c", 32), AttemptID: request.OperationID,
		Operation:          backupasset.LifecycleExplicitPurge,
		RepositoryIdentity: "rclone-native:v1:" + strings.Repeat("a", 64), Request: request,
	}
}

func timePtr(value time.Time) *time.Time { return &value }
