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

func TestResticProbeUsesNativeIdentityAndSecretStdin(t *testing.T) {
	nativeID := strings.Repeat("a", 64)
	transport := &fakeCommandTransport{outputs: map[CommandOperation]CommandOutput{
		OperationResticVersion: {Stdout: []byte("restic 0.18.0 compiled with go1.26\n")},
		OperationResticConfig:  {Stdout: []byte(`{"version":2,"id":"` + nativeID + `","chunker_polynomial":"abc"}`)},
	}}
	adapter := newResticAdapterForTest(t, transport)
	binding := resticBindingForTest()
	observation, err := adapter.Probe(context.Background(), binding, testOperationLimits())
	if err != nil {
		t.Fatal(err)
	}
	if observation.RepositoryIdentity != NativeResticIdentityPrefix+nativeID || observation.IdentityClass != IdentityNativeRepository || observation.VersionMode != backupasset.VersionNativeSnapshot || observation.Capabilities.OpenRange {
		t.Fatalf("unexpected observation: %+v", observation)
	}
	if len(transport.requests) != 2 || transport.requests[1].Operation != OperationResticConfig {
		t.Fatalf("unexpected requests: %+v", transport.requests)
	}
	for _, request := range transport.requests {
		if request.Purpose != CommandPurposeProbe {
			t.Fatalf("probe request purpose=%q request=%+v", request.Purpose, request)
		}
	}
	configRequest := transport.requests[1]
	if string(configRequest.SecretStdin) != "FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY" || configRequest.PrivateLocator != "FAKE_RESTIC_REPOSITORY_FOR_TEST_ONLY" {
		t.Fatalf("private transport values missing: %+v", configRequest)
	}
	joined := strings.Join(configRequest.Args, " ")
	if strings.Contains(joined, "FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY") || !strings.Contains(joined, "/dev/stdin") {
		t.Fatalf("unsafe Restic config args: %#v", configRequest.Args)
	}
}

func TestResticProbeRejectsMalformedOrTrailingConfig(t *testing.T) {
	for _, config := range []string{
		`{"version":2}`,
		`{"version":2,"id":"short"}`,
		`{"version":2,"id":"` + strings.Repeat("a", 64) + `"} trailing`,
	} {
		transport := &fakeCommandTransport{outputs: map[CommandOperation]CommandOutput{
			OperationResticVersion: {Stdout: []byte("restic 0.18.0")}, OperationResticConfig: {Stdout: []byte(config)},
		}}
		adapter := newResticAdapterForTest(t, transport)
		if _, err := adapter.Probe(context.Background(), resticBindingForTest(), testOperationLimits()); !errors.Is(err, backupasset.ErrCapabilityUnavailable) {
			t.Fatalf("config %q error=%v", config, err)
		}
	}
}

func TestResticMapsRunnerTimeoutAndResourceLimit(t *testing.T) {
	for _, test := range []struct {
		err  error
		code backupasset.CapabilityCode
	}{{sshutil.ErrCommandTimeout, backupasset.CapabilityProviderOperationTimeout}, {sshutil.ErrCommandOutputLimit, backupasset.CapabilityProviderResourceLimit}} {
		transport := &fakeCommandTransport{errors: map[CommandOperation]error{OperationResticVersion: test.err}}
		_, err := newResticAdapterForTest(t, transport).Probe(context.Background(), resticBindingForTest(), testOperationLimits())
		var capabilityErr *CapabilityError
		if !errors.As(err, &capabilityErr) || capabilityErr.Reason.Code != test.code {
			t.Fatalf("runner error=%v mapped=%v", test.err, err)
		}
	}
}

func TestResticListPointsUsesFullIDsAndOpaqueCursor(t *testing.T) {
	firstID := strings.Repeat("a", 64)
	secondID := strings.Repeat("b", 64)
	transport := &fakeCommandTransport{outputs: map[CommandOperation]CommandOutput{
		OperationResticSnapshots: {Stdout: []byte(`[
{"id":"` + secondID + `","time":"2026-07-13T02:00:00Z","hostname":"host-b","paths":["/b"]},
{"id":"` + firstID + `","time":"2026-07-13T01:00:00Z","hostname":"host-a","paths":["/a"]}
]`)},
	}}
	adapter := newResticAdapterForTest(t, transport)
	snapshot := resticReadSnapshotForTest()
	page, err := adapter.ListPoints(context.Background(), snapshot, PageRequest{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].Locator.Native != firstID || page.NextCursor == "" || strings.Contains(page.NextCursor, firstID) {
		t.Fatalf("unexpected first page: %+v", page)
	}
	next, err := adapter.ListPoints(context.Background(), snapshot, PageRequest{Limit: 1, Cursor: page.NextCursor})
	if err != nil || len(next.Items) != 1 || next.Items[0].Locator.Native != secondID || next.NextCursor != "" {
		t.Fatalf("unexpected second page: %+v err=%v", next, err)
	}
	before := len(transport.requests)
	badSnapshot := snapshot
	badSnapshot.Access.AdapterData = ResticRuntimeAccess{NativeRepositoryID: strings.Repeat("c", 64)}
	if _, err := adapter.ListEntries(context.Background(), badSnapshot, PointLocator{Native: "latest"}, EntryLocator{Native: "/"}, PageRequest{Limit: 1}); err == nil {
		t.Fatal("latest snapshot locator accepted")
	}
	if len(transport.requests) != before {
		t.Fatal("invalid snapshot locator reached command transport")
	}
}

func TestResticListPointsRejectsCursorAfterNativeListingChanges(t *testing.T) {
	firstID := strings.Repeat("a", 64)
	secondID := strings.Repeat("b", 64)
	insertedID := strings.Repeat("c", 64)
	transport := &fakeCommandTransport{outputs: map[CommandOperation]CommandOutput{
		OperationResticSnapshots: {Stdout: []byte(`[
{"id":"` + firstID + `","time":"2026-07-13T01:00:00Z"},
{"id":"` + secondID + `","time":"2026-07-13T03:00:00Z"}
]`)},
	}}
	adapter := newResticAdapterForTest(t, transport)
	snapshot := resticReadSnapshotForTest()
	page, err := adapter.ListPoints(context.Background(), snapshot, PageRequest{Limit: 1})
	if err != nil || page.NextCursor == "" {
		t.Fatalf("first page=%+v err=%v", page, err)
	}
	transport.outputs[OperationResticSnapshots] = CommandOutput{Stdout: []byte(`[
{"id":"` + firstID + `","time":"2026-07-13T01:00:00Z"},
{"id":"` + insertedID + `","time":"2026-07-13T02:00:00Z"},
{"id":"` + secondID + `","time":"2026-07-13T03:00:00Z"}
]`)}
	if _, err := adapter.ListPoints(context.Background(), snapshot, PageRequest{Limit: 1, Cursor: page.NextCursor}); !errors.Is(err, ErrStaleCursor) {
		t.Fatalf("changed native listing cursor error=%v", err)
	}
}

func TestResticListEntriesStrictlyParsesKnownRecords(t *testing.T) {
	snapshotID := strings.Repeat("d", 64)
	transport := &fakeCommandTransport{outputs: map[CommandOperation]CommandOutput{
		OperationResticList: {Stdout: []byte(
			`{"struct_type":"snapshot","id":"` + snapshotID + `"}` + "\n" +
				`{"struct_type":"node","name":"文件.txt","path":"/dir/文件.txt","type":"file","size":7,"mtime":"2026-07-13T01:00:00Z"}` + "\n")},
	}}
	adapter := newResticAdapterForTest(t, transport)
	page, err := adapter.ListEntries(context.Background(), resticReadSnapshotForTest(), PointLocator{Native: snapshotID}, EntryLocator{Native: "/dir"}, PageRequest{Limit: 10})
	if err != nil || len(page.Items) != 1 || page.Items[0].Name != "文件.txt" || page.Items[0].Type != backupasset.CatalogEntryFile {
		t.Fatalf("entries=%+v err=%v", page, err)
	}
	request := transport.requests[len(transport.requests)-1]
	if request.Operation != OperationResticList || request.Purpose != CommandPurposeList || request.Args[len(request.Args)-1] != "/dir" {
		t.Fatalf("exact path was not one operand: %#v", request.Args)
	}

	transport.outputs[OperationResticList] = CommandOutput{Stdout: []byte(`{"struct_type":"future","path":"/dir/x"}` + "\n")}
	if _, err := adapter.ListEntries(context.Background(), resticReadSnapshotForTest(), PointLocator{Native: snapshotID}, EntryLocator{Native: "/dir"}, PageRequest{Limit: 10}); !errors.Is(err, backupasset.ErrCapabilityUnavailable) {
		t.Fatalf("unknown record error=%v", err)
	}
}

func TestResticListEntriesRejectsCursorAfterDirectoryListingChanges(t *testing.T) {
	snapshotID := strings.Repeat("d", 64)
	header := `{"struct_type":"snapshot","id":"` + snapshotID + `"}` + "\n"
	node := func(name string) string {
		return `{"struct_type":"node","name":"` + name + `","path":"/dir/` + name + `","type":"file","size":1,"mtime":"2026-07-13T01:00:00Z"}` + "\n"
	}
	transport := &fakeCommandTransport{outputs: map[CommandOperation]CommandOutput{
		OperationResticList: {Stdout: []byte(header + node("a") + node("c"))},
	}}
	adapter := newResticAdapterForTest(t, transport)
	snapshot := resticReadSnapshotForTest()
	point := PointLocator{Native: snapshotID}
	parent := EntryLocator{Native: "/dir"}
	page, err := adapter.ListEntries(context.Background(), snapshot, point, parent, PageRequest{Limit: 1})
	if err != nil || page.NextCursor == "" {
		t.Fatalf("first page=%+v err=%v", page, err)
	}
	transport.outputs[OperationResticList] = CommandOutput{Stdout: []byte(header + node("a") + node("b") + node("c"))}
	if _, err := adapter.ListEntries(context.Background(), snapshot, point, parent, PageRequest{Limit: 1, Cursor: page.NextCursor}); !errors.Is(err, ErrStaleCursor) {
		t.Fatalf("changed directory listing cursor error=%v", err)
	}
}

func TestResticOpenSequentialRequiresLimitAndNeverRegistersRange(t *testing.T) {
	snapshotID := strings.Repeat("e", 64)
	transport := &fakeCommandTransport{outputs: map[CommandOperation]CommandOutput{
		OperationResticList: {Stdout: []byte(`{"struct_type":"snapshot","id":"` + snapshotID + `"}` + "\n" + `{"struct_type":"node","name":"-odd name","path":"/dir/-odd name","type":"file","size":4,"mtime":"2026-07-13T01:00:00Z"}` + "\n")},
	}, open: io.NopCloser(strings.NewReader("data"))}
	adapter := newResticAdapterForTest(t, transport)
	point := PointLocator{Native: snapshotID}
	entry := EntryLocator{Native: "/dir/-odd name"}
	if _, _, err := adapter.OpenSequential(context.Background(), resticReadSnapshotForTest(), point, entry, ReadRequest{}); err == nil {
		t.Fatal("unbounded sequential read accepted")
	}
	beforeOpen := len(transport.requests)
	handle, stat, err := adapter.OpenSequential(context.Background(), resticReadSnapshotForTest(), point, entry, ReadRequest{MaxBytes: 4})
	if err != nil || stat.Size != 4 {
		t.Fatalf("OpenSequential stat=%+v err=%v", stat, err)
	}
	value, _ := io.ReadAll(handle)
	if err := handle.Close(); err != nil || string(value) != "data" {
		t.Fatalf("dump value=%q close=%v", value, err)
	}
	last := transport.requests[len(transport.requests)-1]
	if last.Operation != OperationResticDump || last.Purpose != CommandPurposeRead || last.Args[len(last.Args)-1] != entry.Native {
		t.Fatalf("unexpected dump invocation: %+v", last)
	}
	for _, request := range transport.requests[beforeOpen:] {
		if request.Purpose != CommandPurposeRead {
			t.Fatalf("content-open request used purpose %q: %+v", request.Purpose, request)
		}
	}
	registry := NewRegistry()
	if err := registry.Register(backupasset.ProviderRestic, Registration{Prober: adapter, PointLister: adapter, EntryLister: adapter, EntryStatter: adapter, SequentialReader: adapter}); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.RangeReader(backupasset.ProviderRestic); !errors.Is(err, backupasset.ErrCapabilityUnavailable) {
		t.Fatalf("Restic range unexpectedly registered: %v", err)
	}
	for _, request := range transport.requests {
		joined := strings.Join(request.Args, " ")
		for _, forbidden := range []string{"latest", " init ", " backup ", " restore ", " forget ", " prune ", " unlock "} {
			if strings.Contains(" "+joined+" ", forbidden) {
				t.Fatalf("forbidden Restic command reached transport: %q", joined)
			}
		}
	}
}

func newResticAdapterForTest(t *testing.T, transport CommandTransport) *ResticAdapter {
	t.Helper()
	now := time.Date(2026, 7, 13, 6, 0, 0, 0, time.UTC)
	material := backupasset.DomainKeyMaterial{Version: 1, Domain: backupasset.KeyDomainCursorSigning, Key: []byte("FAKE_CURSOR_SIGNING_KEY_FOR_TEST_ONLY")}
	keys := staticCursorKeys{active: material, versions: map[int]backupasset.DomainKeyMaterial{1: material}}
	adapter, err := NewResticAdapter(transport, NewCursorCodec(keys, func() time.Time { return now }, time.Hour), testOperationLimits(), 100, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

func resticBindingForTest() AccessBinding {
	return AccessBinding{Provider: backupasset.ProviderRestic, RepositoryID: strings.Repeat("1", 32), Locator: "FAKE_RESTIC_REPOSITORY_FOR_TEST_ONLY", Secret: []byte("FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY")}
}

func resticReadSnapshotForTest() ReadSnapshot {
	binding := resticBindingForTest()
	binding.AdapterData = ResticRuntimeAccess{NativeRepositoryID: strings.Repeat("a", 64)}
	return ReadSnapshot{RepositoryID: binding.RepositoryID, CapabilityRevision: 1, SourceRevision: strings.Repeat("f", 64), Access: binding}
}

func testOperationLimits() OperationLimits {
	return OperationLimits{Timeout: time.Minute, MaxMetadataBytes: 1 << 20, MaxStderrBytes: 64 << 10, MaxRecordBytes: 64 << 10, MaxItems: 1000}
}
