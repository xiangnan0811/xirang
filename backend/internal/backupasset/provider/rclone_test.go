package provider

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/sshutil"
)

func TestRcloneProbeUsesTaskScopedIdentityAndExecutableRangeProof(t *testing.T) {
	transport := rcloneProbeTransport(true)
	adapter := newRcloneAdapterForTest(t, transport)
	binding := rcloneBindingForTest()
	observation, err := adapter.Probe(context.Background(), binding, testOperationLimits())
	if err != nil {
		t.Fatal(err)
	}
	if observation.IdentityClass != IdentityTaskScopedEndpoint || observation.VersionMode != backupasset.VersionMutableHead || !observation.Capabilities.OpenRange || observation.SourceRevision == "" {
		t.Fatalf("unexpected observation: %+v", observation)
	}
	if strings.Contains(observation.RepositoryIdentity, binding.Locator) || strings.Contains(observation.RepositoryIdentity, "remote-name") {
		t.Fatalf("raw remote leaked in identity: %q", observation.RepositoryIdentity)
	}
	other := binding
	other.TaskID++
	otherObservation, err := adapter.Probe(context.Background(), other, testOperationLimits())
	if err != nil || otherObservation.RepositoryIdentity == observation.RepositoryIdentity {
		t.Fatalf("task-scoped identity merged: %+v err=%v", otherObservation, err)
	}
	for _, request := range transport.requests {
		if request.Purpose != CommandPurposeProbe {
			t.Fatalf("probe request purpose=%q request=%+v", request.Purpose, request)
		}
		if strings.Contains(strings.Join(request.Args, " "), binding.Locator) {
			t.Fatalf("private locator leaked into public argv: %#v", request.Args)
		}
		if request.Operation != OperationRcloneVersion && string(request.SecretStdin) != "FAKE_RCLONE_CONFIG_FOR_TEST_ONLY" {
			t.Fatalf("Rclone config not delivered through secret stdin: %+v", request)
		}
	}
}

func TestRcloneNodeDefaultConfigOmitsSecretTransport(t *testing.T) {
	transport := rcloneProbeTransport(true)
	adapter := newRcloneAdapterForTest(t, transport)
	binding := rcloneBindingForTest()
	binding.Secret = nil
	binding.AdapterData = RcloneRuntimeAccess{ConfigSource: RcloneConfigNodeDefault}
	observation, err := adapter.Probe(context.Background(), binding, testOperationLimits())
	if err != nil {
		t.Fatal(err)
	}
	if observation.ConfigFingerprint == "" {
		t.Fatal("node-default config fingerprint missing")
	}
	for _, request := range transport.requests {
		if len(request.SecretStdin) != 0 {
			t.Fatalf("node-default config unexpectedly used secret stdin: %+v", request)
		}
		for _, argument := range request.Args {
			if argument == "--config" || argument == "/dev/stdin" {
				t.Fatalf("node-default config unexpectedly forced a config file: %+v", request.Args)
			}
		}
	}
}

func TestRcloneManagedReaderRequiresExactCommittedPointAndNeverFallsBackToMutable(t *testing.T) {
	listJSON := `[{"Path":"file.txt","Name":"file.txt","Size":4,"ModTime":"2026-07-16T01:00:00Z","IsDir":false,"Hashes":{"sha256":"abcd"}}]`
	transport := &fakeCommandTransport{outputs: map[CommandOperation]CommandOutput{OperationRcloneList: {Stdout: []byte(listJSON)}}}
	adapter := newRcloneAdapterForTest(t, transport)
	binding := rcloneBindingForTest()
	binding.Locator = "remote-name:managed/v1/points/point/attempts/attempt/data"
	sourceRevision := rcloneListFingerprintForTest(t, listJSON)
	managed := RcloneManagedPointAccess{
		RecoveryPointID: strings.Repeat("a", 32), AttemptID: strings.Repeat("b", 32), DataLocator: binding.Locator,
		ManifestDigest: strings.Repeat("c", 64), SourceRevision: sourceRevision, Committed: true,
	}
	binding.AdapterData = RcloneRuntimeAccess{Backend: "s3", ConfigSource: RcloneConfigBound, ManagedPoint: &managed}
	snapshot := ReadSnapshot{RepositoryID: binding.RepositoryID, CapabilityRevision: 1, SourceRevision: sourceRevision, Access: binding}
	points, err := adapter.ListPoints(context.Background(), snapshot, PageRequest{Limit: 10})
	if err != nil || len(points.Items) != 1 || points.Items[0].Semantics != backupasset.PointXirangManifest || points.Items[0].Locator.Native == "" || strings.Contains(points.Items[0].Locator.Native, binding.Locator) {
		t.Fatalf("managed points=%+v err=%v", points, err)
	}
	page, err := adapter.ListEntries(context.Background(), snapshot, points.Items[0].Locator, EntryLocator{}, PageRequest{Limit: 10})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("managed page=%+v err=%v", page, err)
	}
	if _, err := adapter.ListEntries(context.Background(), snapshot, rclonePointLocator(sourceRevision), EntryLocator{}, PageRequest{Limit: 10}); err == nil {
		t.Fatal("managed reader accepted mutable-head point fallback")
	}

	unsafeBinding := binding
	unsafeRuntime := *unsafeBinding.AdapterData.(RcloneRuntimeAccess).ManagedPoint
	unsafeRuntime.Committed = false
	unsafeBinding.AdapterData = RcloneRuntimeAccess{Backend: "s3", ConfigSource: RcloneConfigBound, ManagedPoint: &unsafeRuntime}
	unsafeSnapshot := snapshot
	unsafeSnapshot.Access = unsafeBinding
	if _, err := adapter.ListPoints(context.Background(), unsafeSnapshot, PageRequest{Limit: 10}); err == nil {
		t.Fatal("preparing managed point was readable")
	}
	unsafeBinding = binding
	unsafeBinding.Secret = nil
	unsafeBinding.AdapterData = RcloneRuntimeAccess{Backend: "s3", ConfigSource: RcloneConfigNodeDefault, ManagedPoint: &managed}
	unsafeSnapshot.Access = unsafeBinding
	if _, err := adapter.ListPoints(context.Background(), unsafeSnapshot, PageRequest{Limit: 10}); err == nil {
		t.Fatal("managed point accepted node-default config")
	}
}

func TestRcloneRangeRemainsFalseWhenProofIsWrongOrUnavailable(t *testing.T) {
	for _, transport := range []*fakeCommandTransport{rcloneProbeTransport(false), rcloneEmptyProbeTransport()} {
		adapter := newRcloneAdapterForTest(t, transport)
		observation, err := adapter.Probe(context.Background(), rcloneBindingForTest(), testOperationLimits())
		if err != nil {
			t.Fatal(err)
		}
		if observation.Capabilities.OpenRange {
			t.Fatalf("unproven Range enabled: %+v", observation)
		}
	}
}

func TestRcloneMapsRunnerTimeoutAndResourceLimit(t *testing.T) {
	for _, test := range []struct {
		err  error
		code backupasset.CapabilityCode
	}{{sshutil.ErrCommandTimeout, backupasset.CapabilityProviderOperationTimeout}, {sshutil.ErrCommandOutputLimit, backupasset.CapabilityProviderResourceLimit}} {
		transport := &fakeCommandTransport{errors: map[CommandOperation]error{OperationRcloneVersion: test.err}}
		_, err := newRcloneAdapterForTest(t, transport).Probe(context.Background(), rcloneBindingForTest(), testOperationLimits())
		var capabilityErr *CapabilityError
		if !errors.As(err, &capabilityErr) || capabilityErr.Reason.Code != test.code {
			t.Fatalf("runner error=%v mapped=%v", test.err, err)
		}
	}
}

func TestRcloneAdapterRefreshesDynamicOperationLimits(t *testing.T) {
	listJSON := `[
{"Path":"a","Name":"a","Size":1,"ModTime":"2026-07-13T01:00:00Z","IsDir":false},
{"Path":"b","Name":"b","Size":1,"ModTime":"2026-07-13T01:00:00Z","IsDir":false}
]`
	transport := &fakeCommandTransport{outputs: map[CommandOperation]CommandOutput{OperationRcloneList: {Stdout: []byte(listJSON)}}}
	current := testOperationLimits()
	current.MaxItems = 1
	now := time.Date(2026, 7, 13, 6, 0, 0, 0, time.UTC)
	material := backupasset.DomainKeyMaterial{Version: 1, Domain: backupasset.KeyDomainCursorSigning, Key: []byte("FAKE_CURSOR_SIGNING_KEY_FOR_TEST_ONLY")}
	keys := staticCursorKeys{active: material, versions: map[int]backupasset.DomainKeyMaterial{1: material}}
	adapter, err := NewRcloneAdapterWithLimitsSource(transport, NewCursorCodec(keys, func() time.Time { return now }, time.Hour), func() (OperationLimits, error) {
		return current, nil
	}, 100, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	binding := rcloneBindingForTest()
	binding.AdapterData = RcloneRuntimeAccess{Backend: "s3"}
	rootRevision := rcloneListFingerprintForTest(t, listJSON)
	snapshot := ReadSnapshot{RepositoryID: binding.RepositoryID, CapabilityRevision: 1, SourceRevision: rootRevision, Access: binding}
	point := rclonePointLocator(rootRevision)
	if _, err := adapter.ListEntries(context.Background(), snapshot, point, EntryLocator{}, PageRequest{Limit: 10}); !errors.Is(err, backupasset.ErrCapabilityUnavailable) {
		t.Fatalf("tight dynamic item limit error=%v", err)
	}
	current.MaxItems = 10
	page, err := adapter.ListEntries(context.Background(), snapshot, point, EntryLocator{}, PageRequest{Limit: 10})
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("refreshed dynamic limits page=%+v err=%v", page, err)
	}
}

func TestRcloneListStatAndSequentialRead(t *testing.T) {
	listJSON := `[{
"Path":"-奇怪 名称.txt","Name":"-奇怪 名称.txt","Size":4,"MimeType":"text/plain","ModTime":"2026-07-13T01:00:00Z","IsDir":false,"Hashes":{"sha256":"abcd"}
}]`
	statJSON := `{"Path":"-奇怪 名称.txt","Name":"-奇怪 名称.txt","Size":4,"MimeType":"text/plain","ModTime":"2026-07-13T01:00:00Z","IsDir":false,"Hashes":{"sha256":"abcd"}}`
	transport := &fakeCommandTransport{outputs: map[CommandOperation]CommandOutput{
		OperationRcloneList: {Stdout: []byte(listJSON)}, OperationRcloneStat: {Stdout: []byte(statJSON)},
	}, open: io.NopCloser(strings.NewReader("data"))}
	adapter := newRcloneAdapterForTest(t, transport)
	binding := rcloneBindingForTest()
	binding.AdapterData = RcloneRuntimeAccess{Backend: "s3", RangeProven: false}
	snapshot := ReadSnapshot{RepositoryID: binding.RepositoryID, CapabilityRevision: 1, SourceRevision: rcloneListFingerprintForTest(t, listJSON), Access: binding}
	point := rclonePointLocator(snapshot.SourceRevision)
	page, err := adapter.ListEntries(context.Background(), snapshot, point, EntryLocator{}, PageRequest{Limit: 10})
	if err != nil || len(page.Items) != 1 || page.Items[0].Name != "-奇怪 名称.txt" {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	entry := page.Items[0].Locator
	stat, err := adapter.StatEntry(context.Background(), snapshot, point, entry)
	if err != nil || stat.Size != 4 {
		t.Fatalf("stat=%+v err=%v", stat, err)
	}
	if _, _, err := adapter.OpenSequential(context.Background(), snapshot, point, entry, ReadRequest{}); err == nil {
		t.Fatal("unbounded sequential read accepted")
	}
	beforeOpen := len(transport.requests)
	handle, contentStat, err := adapter.OpenSequential(context.Background(), snapshot, point, entry, ReadRequest{MaxBytes: 4})
	if err != nil || contentStat.Size != 4 {
		t.Fatalf("OpenSequential stat=%+v err=%v", contentStat, err)
	}
	value, readErr := io.ReadAll(handle)
	if closeErr := handle.Close(); readErr != nil || closeErr != nil || string(value) != "data" {
		t.Fatalf("value=%q read=%v close=%v", value, readErr, closeErr)
	}
	last := lastRcloneInvocation(t, transport.requests, OperationRcloneCat)
	if last.Operation != OperationRcloneCat || last.PrivateLocator != "remote-name:root/-奇怪 名称.txt" {
		t.Fatalf("exact private object locator missing: %+v", last)
	}
	for _, request := range transport.requests[beforeOpen:] {
		if request.Purpose != CommandPurposeRead {
			t.Fatalf("content-open request used purpose %q: %+v", request.Purpose, request)
		}
	}
}

func TestRcloneRangeRequiresPersistedProofAndExactLength(t *testing.T) {
	listJSON := `[{"Path":"file","Name":"file","Size":10,"ModTime":"2026-07-13T01:00:00Z","IsDir":false}]`
	statJSON := `{"Path":"file","Name":"file","Size":10,"ModTime":"2026-07-13T01:00:00Z","IsDir":false}`
	transport := &fakeCommandTransport{outputs: map[CommandOperation]CommandOutput{OperationRcloneList: {Stdout: []byte(listJSON)}, OperationRcloneStat: {Stdout: []byte(statJSON)}}, open: io.NopCloser(strings.NewReader("2345"))}
	adapter := newRcloneAdapterForTest(t, transport)
	binding := rcloneBindingForTest()
	binding.AdapterData = RcloneRuntimeAccess{Backend: "local", RangeProven: false}
	snapshot := ReadSnapshot{RepositoryID: binding.RepositoryID, CapabilityRevision: 1, SourceRevision: rcloneListFingerprintForTest(t, listJSON), Access: binding}
	point := rclonePointLocator(snapshot.SourceRevision)
	entry := EntryLocator{Native: "file"}
	if _, _, err := adapter.OpenRange(context.Background(), snapshot, point, entry, ByteRange{Offset: 2, Length: 4}); !errors.Is(err, backupasset.ErrCapabilityUnavailable) {
		t.Fatalf("unproven range error=%v", err)
	}
	runtimeAccess := binding.AdapterData.(RcloneRuntimeAccess)
	runtimeAccess.RangeProven = true
	snapshot.Access.AdapterData = runtimeAccess
	handle, _, err := adapter.OpenRange(context.Background(), snapshot, point, entry, ByteRange{Offset: 2, Length: 4})
	if err != nil {
		t.Fatal(err)
	}
	value, readErr := io.ReadAll(handle)
	if closeErr := handle.Close(); readErr != nil || closeErr != nil || string(value) != "2345" {
		t.Fatalf("range value=%q read=%v close=%v", value, readErr, closeErr)
	}
	request := lastRcloneInvocation(t, transport.requests, OperationRcloneCat)
	joined := strings.Join(request.Args, " ")
	if request.Operation != OperationRcloneCat || !strings.Contains(joined, "--offset 2") || !strings.Contains(joined, "--count 4") {
		t.Fatalf("range invocation=%+v", request)
	}
}

func TestRcloneChangedRootFailsWithoutPartialSuccess(t *testing.T) {
	first := `[{"Path":"a","Name":"a","Size":1,"ModTime":"2026-07-13T01:00:00Z","IsDir":false}]`
	second := `[{"Path":"b","Name":"b","Size":1,"ModTime":"2026-07-13T01:00:00Z","IsDir":false}]`
	listCalls := 0
	transport := &fakeCommandTransport{runFunc: func(invocation CommandInvocation) (CommandOutput, error) {
		if invocation.Operation != OperationRcloneList {
			return CommandOutput{}, nil
		}
		listCalls++
		if listCalls == 1 {
			return CommandOutput{Stdout: []byte(first)}, nil
		}
		return CommandOutput{Stdout: []byte(second)}, nil
	}}
	adapter := newRcloneAdapterForTest(t, transport)
	binding := rcloneBindingForTest()
	binding.AdapterData = RcloneRuntimeAccess{Backend: "local"}
	snapshot := ReadSnapshot{RepositoryID: binding.RepositoryID, CapabilityRevision: 1, SourceRevision: rcloneListFingerprintForTest(t, first), Access: binding}
	if page, err := adapter.ListEntries(context.Background(), snapshot, rclonePointLocator(snapshot.SourceRevision), EntryLocator{}, PageRequest{Limit: 10}); !errors.Is(err, backupasset.ErrCapabilityUnavailable) || len(page.Items) != 0 {
		t.Fatalf("changed page=%+v err=%v", page, err)
	}
}

func TestRcloneListEntriesRejectsCursorAfterNestedListingChanges(t *testing.T) {
	rootJSON := `[{"Path":"dir","Name":"dir","Size":0,"ModTime":"2026-07-13T01:00:00Z","IsDir":true}]`
	nestedJSON := `[
{"Path":"a","Name":"a","Size":1,"ModTime":"2026-07-13T01:00:00Z","IsDir":false},
{"Path":"c","Name":"c","Size":1,"ModTime":"2026-07-13T01:00:00Z","IsDir":false}
]`
	transport := &fakeCommandTransport{runFunc: func(invocation CommandInvocation) (CommandOutput, error) {
		if invocation.Operation != OperationRcloneList {
			return CommandOutput{}, nil
		}
		if strings.HasSuffix(invocation.PrivateLocator, "/dir") {
			return CommandOutput{Stdout: []byte(nestedJSON)}, nil
		}
		return CommandOutput{Stdout: []byte(rootJSON)}, nil
	}}
	adapter := newRcloneAdapterForTest(t, transport)
	binding := rcloneBindingForTest()
	binding.AdapterData = RcloneRuntimeAccess{Backend: "s3"}
	snapshot := ReadSnapshot{RepositoryID: binding.RepositoryID, CapabilityRevision: 1, SourceRevision: rcloneListFingerprintForTest(t, rootJSON), Access: binding}
	point := rclonePointLocator(snapshot.SourceRevision)
	parent := EntryLocator{Native: "dir"}
	page, err := adapter.ListEntries(context.Background(), snapshot, point, parent, PageRequest{Limit: 1})
	if err != nil || page.NextCursor == "" {
		t.Fatalf("first page=%+v err=%v", page, err)
	}
	nestedJSON = `[
{"Path":"a","Name":"a","Size":1,"ModTime":"2026-07-13T01:00:00Z","IsDir":false},
{"Path":"b","Name":"b","Size":1,"ModTime":"2026-07-13T01:00:00Z","IsDir":false},
{"Path":"c","Name":"c","Size":1,"ModTime":"2026-07-13T01:00:00Z","IsDir":false}
]`
	if _, err := adapter.ListEntries(context.Background(), snapshot, point, parent, PageRequest{Limit: 1, Cursor: page.NextCursor}); !errors.Is(err, ErrStaleCursor) {
		t.Fatalf("changed nested listing cursor error=%v", err)
	}
}

func TestRcloneCommandHistoryContainsNoMutationOperations(t *testing.T) {
	transport := rcloneProbeTransport(true)
	adapter := newRcloneAdapterForTest(t, transport)
	if _, err := adapter.Probe(context.Background(), rcloneBindingForTest(), testOperationLimits()); err != nil {
		t.Fatal(err)
	}
	for _, request := range transport.requests {
		joined := " " + strings.Join(request.Args, " ") + " "
		for _, forbidden := range []string{" sync ", " copy ", " move ", " delete ", " purge ", " mkdir ", " rmdir ", " cleanup "} {
			if strings.Contains(joined, forbidden) {
				t.Fatalf("mutation operation reached transport: %q", joined)
			}
		}
	}
}

func rcloneProbeTransport(rangeCorrect bool) *fakeCommandTransport {
	listJSON := `[{"Path":"probe.bin","Name":"probe.bin","Size":4,"ModTime":"2026-07-13T01:00:00Z","IsDir":false,"Hashes":{"sha256":"abcd"}}]`
	statJSON := `{"Path":"probe.bin","Name":"probe.bin","Size":4,"ModTime":"2026-07-13T01:00:00Z","IsDir":false,"Hashes":{"sha256":"abcd"}}`
	return &fakeCommandTransport{runFunc: func(invocation CommandInvocation) (CommandOutput, error) {
		switch invocation.Operation {
		case OperationRcloneVersion:
			return CommandOutput{Stdout: []byte("rclone v1.70.0\n")}, nil
		case OperationRcloneFeatures:
			return CommandOutput{Stdout: []byte(`{"Name":"s3","Features":{"Hashes":true}}`)}, nil
		case OperationRcloneList:
			return CommandOutput{Stdout: []byte(listJSON)}, nil
		case OperationRcloneStat:
			return CommandOutput{Stdout: []byte(statJSON)}, nil
		case OperationRcloneCat:
			if strings.Contains(strings.Join(invocation.Args, " "), "--offset") && !rangeCorrect {
				return CommandOutput{Stdout: []byte("WRONG")}, nil
			}
			return CommandOutput{Stdout: []byte("data")}, nil
		default:
			return CommandOutput{}, errors.New("unexpected operation")
		}
	}}
}

func rcloneEmptyProbeTransport() *fakeCommandTransport {
	return &fakeCommandTransport{outputs: map[CommandOperation]CommandOutput{
		OperationRcloneVersion:  {Stdout: []byte("rclone v1.70.0")},
		OperationRcloneFeatures: {Stdout: []byte(`{"Name":"local","Features":{}}`)},
		OperationRcloneList:     {Stdout: []byte(`[]`)},
	}}
}

func newRcloneAdapterForTest(t *testing.T, transport CommandTransport) *RcloneAdapter {
	t.Helper()
	now := time.Date(2026, 7, 13, 8, 0, 0, 0, time.UTC)
	material := backupasset.DomainKeyMaterial{Version: 1, Domain: backupasset.KeyDomainCursorSigning, Key: []byte("FAKE_CURSOR_SIGNING_KEY_FOR_TEST_ONLY")}
	keys := staticCursorKeys{active: material, versions: map[int]backupasset.DomainKeyMaterial{1: material}}
	adapter, err := NewRcloneAdapter(transport, NewCursorCodec(keys, func() time.Time { return now }, time.Hour), testOperationLimits(), 100, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

func rcloneBindingForTest() AccessBinding {
	return AccessBinding{
		Provider: backupasset.ProviderRclone, RepositoryID: strings.Repeat("3", 32), TaskID: 7, NodeID: 9,
		IdentitySalt: []byte(strings.Repeat("x", IdentitySaltBytes)), EndpointFacts: []string{"remote-fact", "root-fact"},
		Locator: "remote-name:root", Secret: []byte("FAKE_RCLONE_CONFIG_FOR_TEST_ONLY"),
	}
}

func rcloneListFingerprintForTest(t *testing.T, payload string) string {
	t.Helper()
	entries, err := parseRcloneList([]byte(payload), testOperationLimits())
	if err != nil {
		t.Fatal(err)
	}
	return rcloneListFingerprint(entries)
}

func lastRcloneInvocation(t *testing.T, requests []CommandInvocation, operation CommandOperation) CommandInvocation {
	t.Helper()
	for index := len(requests) - 1; index >= 0; index-- {
		if requests[index].Operation == operation {
			return requests[index]
		}
	}
	t.Fatalf("operation %s not found in history: %+v", operation, requests)
	return CommandInvocation{}
}
