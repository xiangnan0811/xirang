package repository

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/model"
)

func validManagedRclonePortableBindingForTest(t *testing.T) managedRcloneBindingDocumentV3 {
	t.Helper()
	saltHex := strings.Repeat("42", provider.IdentitySaltBytes)
	salt, err := hex.DecodeString(saltHex)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := encodeBindingDocument(bindingDocument{
		Version: bindingDocumentVersion, Provider: backupasset.ProviderRclone,
		IdentityClass: provider.IdentityTaskScopedEndpoint, TaskID: 7, NodeID: 9,
		IdentitySalt: saltHex, Locator: "backup:legacy",
		EndpointFacts: []string{"task:7", "node:9", "remote:backup:legacy"},
		ConfigSource:  provider.RcloneConfigNodeDefault,
	})
	if err != nil {
		t.Fatal(err)
	}
	config := []byte("[backup]\ntype = b2\naccount = FAKE_B2_ACCOUNT_FOR_TEST_ONLY\nkey = FAKE_B2_KEY_FOR_TEST_ONLY\n")
	bound, err := provider.ValidateRcloneBoundConfigV1744(config, "backup", salt, 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	legacyPolicy := `{"version":1,"publication_mode":"legacy_mutable","bandwidth_limit":"10M","transfers":4}`
	return managedRcloneBindingDocumentV3{
		Version: managedRcloneBindingDocumentVersion, Provider: backupasset.ProviderRclone,
		IdentityClass: provider.IdentityXirangManagedRepository, TaskID: 7, NodeID: 9,
		RepositoryID: strings.Repeat("a", 32), TaskRepositoryLinkID: strings.Repeat("b", 32),
		LayoutRevision: managedRcloneLayoutRevisionV1, MinimumRuntimeRevision: managedRcloneMinimumRuntimeRevisionV1,
		PublicationMode: backupasset.PublicationVersionedPrefix,
		BindingRevision: 2, ConfigRevision: 3, CapabilityRevision: 4, CredentialRevision: 5,
		PreflightID: strings.Repeat("c", 32), PreflightRevision: 6,
		PreflightDigest:           strings.Repeat("d", 64),
		PreflightExpiresAt:        time.Date(2026, 7, 16, 10, 30, 0, 0, time.UTC),
		ManagedRootIdentityDigest: strings.Repeat("e", 64),
		RepositoryMarkerDigest:    strings.Repeat("1", 64),
		LegacyLocatorDigest:       managedRcloneBindingDigest(salt, "legacy-locator", "backup:legacy"),
		LegacyBindingV1:           legacy,
		LegacyBindingDigest:       managedRcloneBindingDigest(salt, "legacy-binding", legacy),
		LegacyTaskPolicy:          legacyPolicy,
		LegacyTaskPolicyDigest:    managedRcloneBindingDigest(salt, "legacy-task-policy", legacyPolicy),
		IdentitySalt:              saltHex,
		Portable: &managedRclonePortableBindingV3{
			ManagedRootLocator: "backup:managed/v1", TargetRemote: "backup",
			BoundConfig: string(config), ConfigDigest: bound.KeyedDigest(), Backend: bound.Backend(),
			DependencyRemotes:      bound.DependencyRemotes(),
			ClassificationRevision: bound.ClassificationRevision(),
		},
	}
}

func validManagedRcloneNativeBindingForTest(t *testing.T, encryption provider.RcloneNativeEncryptionProfileCode) managedRcloneBindingDocumentV3 {
	t.Helper()
	document := validManagedRclonePortableBindingForTest(t)
	document.PublicationMode = backupasset.PublicationNativeObjectVersions
	document.Portable = nil
	document.Native = &managedRcloneNativeBindingV3{
		ProfileCode: provider.RcloneNativeAWSS3GeneralPurposeV1,
		Region:      "us-east-1", Bucket: "xirang-managed-test", ManagedPrefix: "managed/v1/",
		RegionIdentityDigest: strings.Repeat("2", 64), BucketIdentityDigest: strings.Repeat("3", 64),
		ManagedPrefixIdentityDigest: strings.Repeat("4", 64),
		RoleARN:                     "arn:aws:iam::123456789012:role/xirang-backup-test",
		ExternalID:                  "FAKE_EXTERNAL_ID_FOR_TEST_ONLY",
		Bootstrap: &managedRcloneNativeBootstrapV3{
			Mode:     managedRcloneBootstrapWorkloadChain,
			Workload: &managedRcloneWorkloadBootstrapV3{},
		},
		VersioningDigest: strings.Repeat("5", 64), LifecycleDigest: strings.Repeat("6", 64),
		CapabilityStableObservedAt: time.Date(2026, 7, 16, 9, 0, 0, 0, time.UTC),
		EncryptionProfile:          encryption, BucketEncryptionDigest: strings.Repeat("7", 64),
		CanaryEncryptionEvidenceDigest: strings.Repeat("8", 64),
	}
	if encryption == provider.RcloneNativeSSEKMSV1 {
		document.Native.ActiveKMSKeyARN = "arn:aws:kms:us-east-1:123456789012:key/FAKE-KMS-KEY-FOR-TEST-ONLY"
		document.Native.ActiveKMSKeyDigest = strings.Repeat("9", 64)
		document.Native.KMSCapabilityRevision = 1
		document.Native.RetainedReadKeys = []managedRcloneKMSReadKeyV3{{
			KeyARN:    "arn:aws:kms:us-east-1:123456789012:key/FAKE-OLD-KMS-KEY-FOR-TEST-ONLY",
			KeyDigest: strings.Repeat("a", 64),
		}}
	}
	return document
}

func TestManagedRcloneBindingV3NativeBootstrapAndEncryptionAreClosed(t *testing.T) {
	for _, profile := range []provider.RcloneNativeEncryptionProfileCode{
		provider.RcloneNativeSSES3V1,
		provider.RcloneNativeSSEKMSV1,
	} {
		t.Run(string(profile), func(t *testing.T) {
			want := validManagedRcloneNativeBindingForTest(t, profile)
			payload, err := encodeManagedRcloneBindingDocumentV3(want)
			if err != nil {
				t.Fatal(err)
			}
			got, err := decodeManagedRcloneBindingDocumentV3(payload)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("native V3 round trip=%+v err=%v want=%+v", got, err, want)
			}
		})
	}

	tests := []struct {
		name   string
		mutate func(*managedRcloneBindingDocumentV3)
	}{
		{"portable and native", func(value *managedRcloneBindingDocumentV3) {
			value.Portable = validManagedRclonePortableBindingForTest(t).Portable
		}},
		{"workload and static bootstrap", func(value *managedRcloneBindingDocumentV3) {
			value.Native.Bootstrap.Static = &managedRcloneStaticSTSBootstrapV3{
				AccessKeyID:     "FAKE_AWS_ACCESS_KEY_ID_FOR_TEST_ONLY",
				SecretAccessKey: "FAKE_AWS_SECRET_ACCESS_KEY_FOR_TEST_ONLY",
			}
		}},
		{"sse s3 with kms", func(value *managedRcloneBindingDocumentV3) {
			value.Native.EncryptionProfile = provider.RcloneNativeSSES3V1
		}},
		{"kms alias", func(value *managedRcloneBindingDocumentV3) {
			value.Native.ActiveKMSKeyARN = "arn:aws:kms:us-east-1:123456789012:alias/not-allowed"
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate := validManagedRcloneNativeBindingForTest(t, provider.RcloneNativeSSEKMSV1)
			tt.mutate(&candidate)
			if _, err := encodeManagedRcloneBindingDocumentV3(candidate); !errors.Is(err, backupasset.ErrInvalidState) {
				t.Fatalf("invalid native V3 error=%v", err)
			}
		})
	}
}

func TestManagedRcloneBindingV3RejectsNestedDuplicateNullAndIdentityDrift(t *testing.T) {
	document := validManagedRclonePortableBindingForTest(t)
	payload, err := encodeManagedRcloneBindingDocumentV3(document)
	if err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{
		strings.Replace(payload, `"target_remote":"backup"`, `"target_remote":"backup","target_remote":"backup"`, 1),
		strings.Replace(payload, `"portable":{`, `"portable":null,"ignored_portable":{`, 1),
		strings.Replace(payload, `"task_id":7`, `"task_id":8`, 1),
	} {
		if _, err := decodeManagedRcloneBindingDocumentV3(invalid); !errors.Is(err, backupasset.ErrInvalidState) {
			t.Fatalf("invalid V3 error=%v payload=%s", err, invalid)
		}
	}

	serialized, err := json.Marshal(model.RepositoryAccessBinding{EncryptedConfig: payload})
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{document.Portable.BoundConfig, document.Portable.ManagedRootLocator, document.LegacyBindingV1} {
		if strings.Contains(string(serialized), secret) {
			t.Fatalf("public binding JSON leaked private V3 data: %s", serialized)
		}
	}
}

func TestManagedRcloneBindingV3RequiresObjectLegacyPolicySnapshot(t *testing.T) {
	document := validManagedRclonePortableBindingForTest(t)
	salt, err := hex.DecodeString(document.IdentitySalt)
	if err != nil {
		t.Fatal(err)
	}
	document.LegacyTaskPolicy = `[]`
	document.LegacyTaskPolicyDigest = managedRcloneBindingDigest(salt, "legacy-task-policy", document.LegacyTaskPolicy)
	if _, err := encodeManagedRcloneBindingDocumentV3(document); !errors.Is(err, backupasset.ErrInvalidState) {
		t.Fatalf("non-object legacy policy error=%v, want invalid state", err)
	}
}

func TestManagedRcloneBindingV3AssociationRejectsTaskLinkRepositoryAndModeDrift(t *testing.T) {
	document := validManagedRclonePortableBindingForTest(t)
	task := model.Task{ID: document.TaskID, NodeID: document.NodeID, ExecutorType: string(backupasset.ProviderRclone)}
	linkTaskID := task.ID
	association := managedRcloneBindingAssociation{
		Task: task,
		Link: model.TaskRepositoryLink{
			ID: document.TaskRepositoryLinkID, TaskID: &linkTaskID, RepositoryID: document.RepositoryID,
			PublicationMode: string(document.PublicationMode),
		},
		Repository: model.BackupRepository{
			ID: document.RepositoryID, ProviderKind: string(backupasset.ProviderRclone),
			VersionMode: string(backupasset.VersionVersionedPrefix),
		},
	}
	if err := validateManagedRcloneBindingAssociation(document, association); err != nil {
		t.Fatalf("valid managed Rclone association: %v", err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*managedRcloneBindingAssociation)
	}{
		{"task", func(value *managedRcloneBindingAssociation) { value.Task.ID++ }},
		{"node", func(value *managedRcloneBindingAssociation) { value.Task.NodeID++ }},
		{"link", func(value *managedRcloneBindingAssociation) { value.Link.ID = strings.Repeat("f", 32) }},
		{"repository", func(value *managedRcloneBindingAssociation) { value.Repository.ID = strings.Repeat("e", 32) }},
		{"mode", func(value *managedRcloneBindingAssociation) {
			value.Link.PublicationMode = string(backupasset.PublicationLegacyMutable)
		}},
		{"legacy target restored", func(value *managedRcloneBindingAssociation) { value.Task.RsyncTarget = "backup:legacy" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := association
			test.mutate(&candidate)
			if err := validateManagedRcloneBindingAssociation(document, candidate); !errors.Is(err, backupasset.ErrConflict) {
				t.Fatalf("association drift error=%v, want conflict", err)
			}
		})
	}
}
