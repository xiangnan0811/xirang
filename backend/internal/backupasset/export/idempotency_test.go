package export

import (
	"errors"
	"strings"
	"testing"
)

func TestIdempotencyDigestsAreDomainSeparatedAndIntentBound(t *testing.T) {
	key := "0123456789abcdef"
	exportKey, err := IdempotencyKeyDigest(IdempotencyDomainExportCreate, 17, key)
	if err != nil {
		t.Fatal(err)
	}
	memberKey, err := IdempotencyKeyDigest(IdempotencyDomainArchiveMemberCreate, 17, key)
	if err != nil {
		t.Fatal(err)
	}
	if exportKey == memberKey || len(exportKey) != 64 || len(memberKey) != 64 {
		t.Fatalf("domain-separated key digests export=%q member=%q", exportKey, memberKey)
	}

	intent := CreateIntentV1{SchemaVersion: 1, OwnerUserID: 17,
		SelectionDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ArchiveFormat:   "zip", ArchiveProfile: "zip_deflate_v1", ChunkBytes: 65536}
	first, err := CreateIntentDigest(intent)
	if err != nil {
		t.Fatal(err)
	}
	intent.ArchiveFormat = "tar"
	intent.ArchiveProfile = "tar_none_v1"
	second, err := CreateIntentDigest(intent)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("archive format did not bind request intent")
	}
}

func TestCreateIntentDigestAcceptsOnlyClosedArchivePairs(t *testing.T) {
	valid := []CreateIntentV1{
		{ArchiveFormat: "zip", ArchiveProfile: "zip_deflate_v1"},
		{ArchiveFormat: "tar", ArchiveProfile: "tar_none_v1"},
		{ArchiveFormat: "tar", ArchiveProfile: "tar_gzip_v1"},
	}
	digests := make(map[string]struct{}, len(valid))
	for _, intent := range valid {
		intent.SchemaVersion = 1
		intent.OwnerUserID = 17
		intent.SelectionDigest = strings.Repeat("a", 64)
		intent.ChunkBytes = 65536
		digest, err := CreateIntentDigest(intent)
		if err != nil {
			t.Fatalf("valid pair %s/%s: %v", intent.ArchiveFormat, intent.ArchiveProfile, err)
		}
		digests[digest] = struct{}{}
	}
	if len(digests) != len(valid) {
		t.Fatalf("closed archive pairs did not produce distinct intents: %v", digests)
	}

	for _, testCase := range []struct {
		name    string
		format  string
		profile string
	}{
		{name: "missing format", profile: "zip_deflate_v1"},
		{name: "missing profile", format: "zip"},
		{name: "unknown format", format: "rar", profile: "zip_deflate_v1"},
		{name: "unknown profile", format: "zip", profile: "future_v2"},
		{name: "zip crossed with tar none", format: "zip", profile: "tar_none_v1"},
		{name: "zip crossed with tar gzip", format: "zip", profile: "tar_gzip_v1"},
		{name: "tar crossed with zip", format: "tar", profile: "zip_deflate_v1"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := CreateIntentDigest(CreateIntentV1{
				SchemaVersion: 1, OwnerUserID: 17, SelectionDigest: strings.Repeat("a", 64),
				ArchiveFormat: testCase.format, ArchiveProfile: testCase.profile, ChunkBytes: 65536,
			})
			if !errors.Is(err, ErrInvalidIdempotency) {
				t.Fatalf("CreateIntentDigest(%q, %q) error=%v, want ErrInvalidIdempotency", testCase.format, testCase.profile, err)
			}
		})
	}
}

func TestIdempotencyKeyRejectsWeakOrOversizedInput(t *testing.T) {
	if _, err := IdempotencyKeyDigest(IdempotencyDomainExportCreate, 1, "short"); !errors.Is(err, ErrInvalidIdempotency) {
		t.Fatalf("short key error=%v", err)
	}
	oversized := make([]byte, 257)
	for index := range oversized {
		oversized[index] = 'x'
	}
	if _, err := IdempotencyKeyDigest(IdempotencyDomainExportCreate, 1, string(oversized)); !errors.Is(err, ErrInvalidIdempotency) {
		t.Fatalf("oversized key error=%v", err)
	}
}

func TestIdempotencyKeyDigestHonorsConfiguredCeiling(t *testing.T) {
	key := strings.Repeat("a", 33)
	if _, err := IdempotencyKeyDigestWithMaxBytes(IdempotencyDomainExportCreate, 1, key, 32); !errors.Is(err, ErrInvalidIdempotency) {
		t.Fatalf("configured ceiling error=%v, want ErrInvalidIdempotency", err)
	}
	if _, err := IdempotencyKeyDigestWithMaxBytes(IdempotencyDomainExportCreate, 1, key, 33); err != nil {
		t.Fatalf("configured ceiling accepted key error=%v", err)
	}
}
