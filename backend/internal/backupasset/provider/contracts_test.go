package provider

import (
	"context"
	"encoding/json"
	"errors"
	"go/parser"
	"go/token"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
)

type fakeProvider struct{}

func (*fakeProvider) Probe(context.Context, AccessBinding, OperationLimits) (RepositoryObservation, error) {
	return RepositoryObservation{}, nil
}

func (*fakeProvider) ListPoints(context.Context, ReadSnapshot, PageRequest) (NativePointPage, error) {
	return NativePointPage{}, nil
}

func (*fakeProvider) ListEntries(context.Context, ReadSnapshot, PointLocator, EntryLocator, PageRequest) (EntryPage, error) {
	return EntryPage{}, nil
}

func (*fakeProvider) StatEntry(context.Context, ReadSnapshot, PointLocator, EntryLocator) (Entry, error) {
	return Entry{}, nil
}

func (*fakeProvider) OpenSequential(context.Context, ReadSnapshot, PointLocator, EntryLocator, ReadRequest) (ReadHandle, ContentStat, error) {
	return nil, ContentStat{}, nil
}

var (
	_ RepositoryProber = (*fakeProvider)(nil)
	_ PointLister      = (*fakeProvider)(nil)
	_ EntryLister      = (*fakeProvider)(nil)
	_ EntryStatter     = (*fakeProvider)(nil)
	_ SequentialReader = (*fakeProvider)(nil)
	_ io.ReadCloser    = (ReadHandle)(nil)
)

func TestContractsClampPageAndHideProviderLocators(t *testing.T) {
	request, err := (PageRequest{Limit: 1000}).Normalize(200)
	if err != nil || request.Limit != 200 {
		t.Fatalf("Normalize=%+v err=%v", request, err)
	}
	if _, err := (PageRequest{Limit: -1}).Normalize(200); err == nil {
		t.Fatal("negative page limit accepted")
	}

	payload, err := json.Marshal(struct {
		Access AccessBinding `json:"access"`
		Point  PointLocator  `json:"point"`
		Entry  EntryLocator  `json:"entry"`
	}{
		Access: AccessBinding{Locator: "FAKE_RAW_ROOT_FOR_TEST_ONLY", Secret: []byte("FAKE_PASSWORD_FOR_TEST_ONLY")},
		Point:  PointLocator{Native: "FAKE_NATIVE_POINT_FOR_TEST_ONLY"},
		Entry:  EntryLocator{Native: "FAKE_NATIVE_ENTRY_FOR_TEST_ONLY"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"FAKE_RAW_ROOT_FOR_TEST_ONLY", "FAKE_PASSWORD_FOR_TEST_ONLY", "FAKE_NATIVE_POINT_FOR_TEST_ONLY", "FAKE_NATIVE_ENTRY_FOR_TEST_ONLY"} {
		if strings.Contains(string(payload), forbidden) {
			t.Fatalf("private provider value %q leaked in %s", forbidden, payload)
		}
	}
}

func TestRepositoryObservationJSONHidesInternalIdentityFacts(t *testing.T) {
	observation := RepositoryObservation{
		RepositoryIdentity: "FAKE_REPOSITORY_IDENTITY_FOR_TEST_ONLY",
		SourceRevision:     "FAKE_SOURCE_REVISION_FOR_TEST_ONLY",
		ConfigFingerprint:  "FAKE_CONFIG_FINGERPRINT_FOR_TEST_ONLY",
		InternalProviderFacts: map[string]string{
			"backend": "FAKE_PROVIDER_FACT_FOR_TEST_ONLY",
		},
	}
	payload, err := json.Marshal(observation)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"FAKE_REPOSITORY_IDENTITY_FOR_TEST_ONLY",
		"FAKE_SOURCE_REVISION_FOR_TEST_ONLY",
		"FAKE_CONFIG_FINGERPRINT_FOR_TEST_ONLY",
		"FAKE_PROVIDER_FACT_FOR_TEST_ONLY",
	} {
		if strings.Contains(string(payload), forbidden) {
			t.Fatalf("internal observation value %q leaked in %s", forbidden, payload)
		}
	}
}

func TestContractsRejectUnboundedSequentialRead(t *testing.T) {
	for _, maxBytes := range []int64{0, -1} {
		if err := (ReadRequest{MaxBytes: maxBytes}).Validate(); err == nil {
			t.Fatalf("MaxBytes=%d accepted", maxBytes)
		}
	}
	if err := (ReadRequest{MaxBytes: 1}).Validate(); err != nil {
		t.Fatalf("positive MaxBytes rejected: %v", err)
	}
}

func TestContractsRequireBoundedOperationLimits(t *testing.T) {
	valid := OperationLimits{Timeout: time.Minute, MaxMetadataBytes: 1 << 20, MaxStderrBytes: 1 << 10, MaxRecordBytes: 1 << 10, MaxItems: 100}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid limits rejected: %v", err)
	}
	valid.MaxItems = 0
	if err := valid.Validate(); err == nil {
		t.Fatal("unbounded item limit accepted")
	}
}

func TestNewMetadataOperationLimitsClampsRecordBudget(t *testing.T) {
	limits, err := NewMetadataOperationLimits(time.Minute, 64<<10)
	if err != nil || limits.MaxMetadataBytes != 64<<10 || limits.MaxRecordBytes != 64<<10 || limits.MaxItems <= 0 {
		t.Fatalf("metadata limits=%+v err=%v", limits, err)
	}
}

func TestEntryListRevisionIncludesVisibleEntryMetadata(t *testing.T) {
	base := Entry{
		OpaqueDigest: strings.Repeat("a", 64), Name: "item", Type: backupasset.CatalogEntryFile,
		Size: 1, ModTime: time.Date(2026, 7, 13, 1, 0, 0, 0, time.UTC), SourceRevision: strings.Repeat("b", 64),
	}
	changed := base
	changed.Type = backupasset.CatalogEntryDirectory
	if entryListRevision(strings.Repeat("c", 64), []Entry{base}) == entryListRevision(strings.Repeat("c", 64), []Entry{changed}) {
		t.Fatal("entry-list revision ignored visible entry type changes")
	}
}

func TestProviderParsersRejectNamesThatDoNotMatchExactLocators(t *testing.T) {
	snapshotID := strings.Repeat("d", 64)
	resticPayload := []byte(`{"struct_type":"snapshot","id":"` + snapshotID + `"}` + "\n" +
		`{"struct_type":"node","name":"spoofed","path":"/dir/actual","type":"file","size":1,"mtime":"2026-07-13T01:00:00Z"}` + "\n")
	if _, err := parseResticEntries(resticPayload, snapshotID, "/dir", false, testOperationLimits()); !errors.Is(err, backupasset.ErrCapabilityUnavailable) {
		t.Fatalf("Restic mismatched display name error=%v", err)
	}
	rclonePayload := []byte(`[{"Path":"actual","Name":"spoofed","Size":1,"ModTime":"2026-07-13T01:00:00Z","IsDir":false}]`)
	if _, err := parseRcloneList(rclonePayload, testOperationLimits()); !errors.Is(err, backupasset.ErrCapabilityUnavailable) {
		t.Fatalf("Rclone mismatched display name error=%v", err)
	}
}

func TestContractProviderKindsRemainDomainTyped(t *testing.T) {
	observation := RepositoryObservation{Provider: backupasset.ProviderRestic}
	if observation.Provider != backupasset.ProviderRestic {
		t.Fatalf("provider kind drifted: %+v", observation)
	}
}

func TestProviderProductionImportsStayBelowAPIAndExecutorBoundaries(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		syntax, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		for _, imported := range syntax.Imports {
			path, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				t.Fatalf("decode import in %s: %v", file, err)
			}
			if strings.Contains(path, "/internal/api/handlers") || strings.Contains(path, "/internal/task/executor") {
				t.Fatalf("provider production file %s imports forbidden package %s", file, path)
			}
		}
	}
}

func TestTaggedPublicationAttemptsAreStrictAndProviderSeparated(t *testing.T) {
	restic := ResticAttemptV1{
		RepositoryID:         strings.Repeat("a", 32),
		RepositoryIdentity:   NativeResticIdentityPrefix + strings.Repeat("f", 64),
		TaskRepositoryLinkID: strings.Repeat("b", 32),
		RecoveryPointID:      strings.Repeat("c", 32),
		TaskID:               41,
		TaskRunID:            42,
		RequiredTags: [2]string{
			"xirang.link.v1.aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			"xirang.point.v1.bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		},
		PointDeadlineAt:    time.Date(2026, 7, 16, 4, 0, 0, 0, time.UTC),
		CapabilityRevision: 1,
		AdapterRevision:    "restic-v1",
	}
	encoded, err := EncodePublicationAttempt(NewResticPublicationAttempt(restic))
	if err != nil {
		t.Fatalf("encode Restic tagged attempt: %v", err)
	}
	decoded, err := DecodeResticAttemptV1(encoded)
	if err != nil {
		t.Fatalf("decode Restic tagged attempt: %v", err)
	}
	if decoded.RepositoryID != restic.RepositoryID || decoded.RecoveryPointID != restic.RecoveryPointID || decoded.RequiredTags != restic.RequiredTags {
		t.Fatalf("Restic tagged round-trip drifted: got=%+v want=%+v", decoded, restic)
	}
	if _, err := DecodeRsyncTreeAttemptV1(encoded); err == nil {
		t.Fatal("Rsync decoder accepted a Restic attempt")
	}

	for name, raw := range map[string]string{
		"unknown provider": `{"provider":"rclone","version":1,"restic":{"repository_id":"` + strings.Repeat("a", 32) + `"}}`,
		"unknown version":  `{"provider":"restic","version":2,"restic":{"repository_id":"` + strings.Repeat("a", 32) + `"}}`,
		"unknown field":    strings.TrimSuffix(encoded, "}") + `,"unexpected":"value"}`,
		"duplicate field":  strings.Replace(encoded, `"provider":"restic"`, `"provider":"restic","provider":"restic"`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodePublicationAttempt(raw); err == nil {
				t.Fatalf("unsafe tagged attempt accepted: %s", raw)
			}
		})
	}
}

func TestTaggedPublicationCommitRejectsMixedProviderPayloads(t *testing.T) {
	commit := ResticCommitV1{
		RepositoryIdentity: NativeResticIdentityPrefix + strings.Repeat("f", 64),
		NativePointID:      strings.Repeat("a", 64),
		CaptureStartedAt:   time.Date(2026, 7, 16, 3, 0, 0, 0, time.UTC),
		CaptureFinishedAt:  time.Date(2026, 7, 16, 3, 0, 1, 0, time.UTC),
		FilesProcessed:     7,
		LogicalBytes:       16 << 10,
	}
	encoded, err := EncodeProviderCommit(NewResticProviderCommit(commit))
	if err != nil {
		t.Fatalf("encode Restic commit: %v", err)
	}
	if _, err := DecodeRsyncTreeCommitV1(encoded); err == nil {
		t.Fatal("Rsync decoder accepted a Restic provider commit")
	}
	if _, err := DecodeProviderCommit(strings.TrimSuffix(encoded, "}") + `,"rsync_tree":{}}`); err == nil {
		t.Fatal("mixed provider commit payload was accepted")
	}
	if _, err := DecodeProviderCommit(strings.Replace(encoded, `"provider":"restic"`, `"provider":"restic","provider":"restic"`, 1)); err == nil {
		t.Fatal("duplicate provider commit field was accepted")
	}
}

func TestRsyncTreeFailedCommitCarriesIdentityWithoutSuccessEvidence(t *testing.T) {
	failed := RsyncTreeCommitV1{
		LayoutVersion:        taggedPublicationSchemaV1,
		RepositoryID:         strings.Repeat("a", 32),
		TaskRepositoryLinkID: strings.Repeat("b", 32),
		RecoveryPointID:      strings.Repeat("c", 32),
		AttemptID:            strings.Repeat("d", 32),
		PublicationMode:      backupasset.PublicationVersionedFullCopy,
		PointDeadlineAt:      time.Date(2026, 7, 16, 4, 0, 0, 0, time.UTC),
		FailureCode:          backupasset.FailureProviderNonzeroExit,
	}
	if err := failed.Validate(); err != nil {
		t.Fatalf("failed Rsync tree commit rejected: %v", err)
	}
	failed.ManifestDigest = strings.Repeat("e", 64)
	if err := failed.Validate(); err == nil {
		t.Fatal("failed Rsync tree commit accepted success evidence")
	}
}

func TestRsyncTreeAttemptAcceptsFixedOpaqueStagingComponent(t *testing.T) {
	pointID := strings.Repeat("a", 32)
	attemptID := strings.Repeat("b", 32)
	attempt := RsyncTreeAttemptV1{
		RepositoryID:              strings.Repeat("c", 32),
		TaskRepositoryLinkID:      strings.Repeat("d", 32),
		RecoveryPointID:           pointID,
		AttemptID:                 attemptID,
		TaskID:                    7,
		TaskRunID:                 8,
		PublicationMode:           backupasset.PublicationVersionedFullCopy,
		PointDeadlineAt:           time.Date(2026, 7, 16, 4, 0, 0, 0, time.UTC),
		ExpectedTaskRevision:      1,
		RepositoryMarkerDigest:    strings.Repeat("e", 64),
		ManagedRootIdentityDigest: strings.Repeat("f", 64),
		StagingComponent:          pointID + "." + attemptID,
		FinalComponent:            pointID,
		CommandProfileVersion:     1,
		PreflightID:               strings.Repeat("1", 32),
		PreflightDigest:           strings.Repeat("2", 64),
	}
	if err := attempt.Validate(); err != nil {
		t.Fatalf("fixed opaque staging component rejected: %v", err)
	}
}
