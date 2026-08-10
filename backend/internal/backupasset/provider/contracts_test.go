package provider

import (
	"context"
	"encoding/json"
	"errors"
	"go/parser"
	"go/token"
	"io"
	"path/filepath"
	"reflect"
	"sort"
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

func TestRestorePortAndRequestRemainClosed(t *testing.T) {
	portType := reflect.TypeOf((*RestorePort)(nil)).Elem()
	methods := make([]string, 0, portType.NumMethod())
	for index := 0; index < portType.NumMethod(); index++ {
		methods = append(methods, portType.Method(index).Name)
	}
	sort.Strings(methods)
	if want := []string{"Execute", "Preflight", "ProviderKind", "Reconcile", "Verify"}; !reflect.DeepEqual(methods, want) {
		t.Fatalf("RestorePort methods = %v, want %v", methods, want)
	}

	requestType := reflect.TypeOf(RestoreRequest{})
	for _, forbidden := range []string{"Executor", "Command", "Credentials", "SourcePath", "TargetPath"} {
		if _, exists := requestType.FieldByName(forbidden); exists {
			t.Fatalf("RestoreRequest exposes forbidden generic field %q", forbidden)
		}
	}
	for _, field := range []struct {
		typeOf reflect.Type
		name   string
	}{
		{reflect.TypeOf(RestoreSource{}), "Locator"},
		{reflect.TypeOf(RestoreTarget{}), "RootLocatorDigest"},
		{reflect.TypeOf(ResticRestoreRequest{}), "SnapshotID"},
		{reflect.TypeOf(ResticRestoreRequest{}), "Includes"},
		{reflect.TypeOf(TargetSession{}), "ID"},
	} {
		value, exists := field.typeOf.FieldByName(field.name)
		if !exists || value.Tag.Get("json") != "-" {
			t.Fatalf("%s.%s must remain private, field=%#v", field.typeOf.Name(), field.name, value)
		}
	}
}

func TestRsyncRestoreSourceResolverHasOnlyPortableInputAndOpaqueOutput(t *testing.T) {
	sourceType := reflect.TypeOf((*RsyncRestoreSource)(nil)).Elem()
	streamType := reflect.TypeOf((*RsyncRestoreSourceStream)(nil)).Elem()
	resolverType := reflect.TypeOf((*RsyncRestoreSourceResolver)(nil)).Elem()
	if resolverType.NumMethod() != 1 {
		t.Fatalf("RsyncRestoreSourceResolver method count = %d, want 1", resolverType.NumMethod())
	}
	method := resolverType.Method(0)
	if method.Name != "ResolveRsyncRestoreSource" || method.Type.NumIn() != 2 ||
		method.Type.In(0) != reflect.TypeOf((*context.Context)(nil)).Elem() ||
		method.Type.In(1) != reflect.TypeOf(RsyncRestoreSourceRef{}) {
		t.Fatalf("RsyncRestoreSourceResolver method = %s %s, want context plus scalar ref", method.Name, method.Type)
	}
	if method.Type.NumOut() != 2 || method.Type.Out(0) != sourceType ||
		method.Type.Out(1) != reflect.TypeOf((*error)(nil)).Elem() {
		t.Fatalf("RsyncRestoreSourceResolver output = %s, want opaque declared-entry source plus error", method.Type)
	}
	if sourceType.NumMethod() != 4 {
		t.Fatalf("RsyncRestoreSource methods = %d, want 4", sourceType.NumMethod())
	}
	open, ok := sourceType.MethodByName("OpenDeclaredRegular")
	if !ok || open.Type.NumIn() != 2 || open.Type.In(0) != reflect.TypeOf((*context.Context)(nil)).Elem() ||
		open.Type.In(1) != reflect.TypeOf(RestoreEntry{}) || open.Type.NumOut() != 2 || open.Type.Out(0) != streamType ||
		open.Type.Out(1) != reflect.TypeOf((*error)(nil)).Elem() {
		t.Fatalf("RsyncRestoreSource.OpenDeclaredRegular = %v, want context plus frozen entry to bounded stream", open.Type)
	}
	materialize, ok := sourceType.MethodByName("MaterializeDeclaredEntries")
	if !ok || materialize.Type.NumIn() != 2 || materialize.Type.In(0) != reflect.TypeOf((*context.Context)(nil)).Elem() ||
		materialize.Type.In(1) != reflect.TypeOf([]RestoreEntry{}) || materialize.Type.NumOut() != 2 ||
		materialize.Type.Out(0) != reflect.TypeOf([]RestoreEntry{}) ||
		materialize.Type.Out(1) != reflect.TypeOf((*error)(nil)).Elem() {
		t.Fatalf("RsyncRestoreSource.MaterializeDeclaredEntries = %v, want durable facts to strict entries plus error", materialize.Type)
	}
	for _, methodName := range []string{"Close", "Revalidate"} {
		method, ok := sourceType.MethodByName(methodName)
		if !ok || method.Type.NumOut() != 1 || method.Type.Out(0) != reflect.TypeOf((*error)(nil)).Elem() {
			t.Fatalf("RsyncRestoreSource.%s = %v, want error-only capability method", methodName, method.Type)
		}
	}
	methods := make([]string, 0, streamType.NumMethod())
	for index := 0; index < streamType.NumMethod(); index++ {
		methods = append(methods, streamType.Method(index).Name)
	}
	if want := []string{"Close", "Read"}; !reflect.DeepEqual(methods, want) {
		t.Fatalf("RsyncRestoreSourceStream methods = %v, want %v", methods, want)
	}
}

func TestRsyncTargetWriterHasOnlyBoundDeclaredStreamAuthority(t *testing.T) {
	writerType := reflect.TypeOf((*RsyncTargetWriter)(nil)).Elem()
	if writerType.NumMethod() != 1 {
		t.Fatalf("RsyncTargetWriter methods = %d, want 1", writerType.NumMethod())
	}
	method := writerType.Method(0)
	if method.Name != "WriteDeclaredRegular" || method.Type.NumIn() != 2 ||
		method.Type.In(0) != reflect.TypeOf((*context.Context)(nil)).Elem() ||
		method.Type.In(1) != reflect.TypeOf(RsyncTargetWriteCall{}) || method.Type.NumOut() != 1 ||
		method.Type.Out(0) != reflect.TypeOf((*error)(nil)).Elem() {
		t.Fatalf("RsyncTargetWriter method = %s %s, want one bound declared-stream write", method.Name, method.Type)
	}
	callType := reflect.TypeOf(RsyncTargetWriteCall{})
	want := map[string]reflect.Type{
		"Target": reflect.TypeOf(RsyncBoundRemoteTarget{}),
		"Entry":  reflect.TypeOf(RestoreEntry{}),
		"Source": reflect.TypeOf((*RsyncRestoreSourceStream)(nil)).Elem(),
		"Permit": reflect.TypeOf(TargetMutationPermit{}),
	}
	if callType.NumField() != len(want) {
		t.Fatalf("RsyncTargetWriteCall fields = %d, want %d", callType.NumField(), len(want))
	}
	for index := 0; index < callType.NumField(); index++ {
		field := callType.Field(index)
		if want[field.Name] != field.Type {
			t.Fatalf("RsyncTargetWriteCall field = %#v, want closed authority field", field)
		}
		delete(want, field.Name)
	}
	if len(want) != 0 {
		t.Fatalf("RsyncTargetWriteCall missing fields: %v", want)
	}
}
