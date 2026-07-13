package provider

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/fileaccess"
)

func TestRsyncProbeIdentityIsStableAndTaskScoped(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("strict local provider access is Linux-only")
	}
	root := rsyncTreeForTest(t)
	adapter := newRsyncAdapterForTest(t)
	binding := rsyncBindingForTest(root)
	first, err := adapter.Probe(context.Background(), binding, testOperationLimits())
	if err != nil {
		t.Fatal(err)
	}
	second, err := adapter.Probe(context.Background(), binding, testOperationLimits())
	if err != nil || first.RepositoryIdentity != second.RepositoryIdentity {
		t.Fatalf("identity unstable first=%q second=%q err=%v", first.RepositoryIdentity, second.RepositoryIdentity, err)
	}
	other := binding
	other.TaskID++
	otherObservation, err := adapter.Probe(context.Background(), other, testOperationLimits())
	if err != nil || otherObservation.RepositoryIdentity == first.RepositoryIdentity {
		t.Fatalf("task-scoped identity merged: %+v err=%v", otherObservation, err)
	}
	if first.IdentityClass != IdentityTaskScopedEndpoint || first.VersionMode != backupasset.VersionMutableHead || !first.Capabilities.OpenRange || first.SourceRevision == "" {
		t.Fatalf("unexpected Rsync observation: %+v", first)
	}
	if strings.Contains(first.RepositoryIdentity, root) {
		t.Fatalf("raw root leaked in identity: %q", first.RepositoryIdentity)
	}
}

func TestRsyncExposesOneMutablePointAndBoundedEntries(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("strict local provider access is Linux-only")
	}
	root := rsyncTreeForTest(t)
	adapter := newRsyncAdapterForTest(t)
	binding := rsyncBindingForTest(root)
	observation, err := adapter.Probe(context.Background(), binding, testOperationLimits())
	if err != nil {
		t.Fatal(err)
	}
	snapshot := rsyncSnapshotForTest(binding, observation)
	points, err := adapter.ListPoints(context.Background(), snapshot, PageRequest{Limit: 10})
	if err != nil || len(points.Items) != 1 || points.Items[0].Semantics != backupasset.PointMutableHead || points.NextCursor != "" {
		t.Fatalf("points=%+v err=%v", points, err)
	}
	entries, err := adapter.ListEntries(context.Background(), snapshot, points.Items[0].Locator, EntryLocator{}, PageRequest{Limit: 10})
	if err != nil || len(entries.Items) != 2 || entries.Items[0].Name != "dir" || entries.Items[1].Name != "file.txt" {
		t.Fatalf("entries=%+v err=%v", entries, err)
	}
	stat, err := adapter.StatEntry(context.Background(), snapshot, points.Items[0].Locator, entries.Items[1].Locator)
	if err != nil || stat.Size != int64(len("0123456789")) {
		t.Fatalf("stat=%+v err=%v", stat, err)
	}
}

func TestRsyncListEntriesRejectsCursorAfterNestedDirectoryChanges(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("strict local provider access is Linux-only")
	}
	root := rsyncTreeForTest(t)
	adapter := newRsyncAdapterForTest(t)
	binding := rsyncBindingForTest(root)
	observation, err := adapter.Probe(context.Background(), binding, testOperationLimits())
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, "dir")
	for _, name := range []string{"a", "c"} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	snapshot := rsyncSnapshotForTest(binding, observation)
	point := rsyncPointLocator(observation.SourceRevision)
	page, err := adapter.ListEntries(context.Background(), snapshot, point, EntryLocator{Native: "dir"}, PageRequest{Limit: 1})
	if err != nil || page.NextCursor == "" {
		t.Fatalf("first page=%+v err=%v", page, err)
	}
	if err := os.WriteFile(filepath.Join(directory, "b"), []byte("b"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.ListEntries(context.Background(), snapshot, point, EntryLocator{Native: "dir"}, PageRequest{Limit: 1, Cursor: page.NextCursor}); !errors.Is(err, ErrStaleCursor) {
		t.Fatalf("changed nested listing cursor error=%v", err)
	}
}

func TestRsyncSequentialAndRangeRespectLimitsAndProof(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("strict local provider access is Linux-only")
	}
	root := rsyncTreeForTest(t)
	adapter := newRsyncAdapterForTest(t)
	binding := rsyncBindingForTest(root)
	observation, err := adapter.Probe(context.Background(), binding, testOperationLimits())
	if err != nil {
		t.Fatal(err)
	}
	snapshot := rsyncSnapshotForTest(binding, observation)
	point := rsyncPointLocator(observation.SourceRevision)
	entry := EntryLocator{Native: "file.txt"}
	if _, _, err := adapter.OpenSequential(context.Background(), snapshot, point, entry, ReadRequest{}); err == nil {
		t.Fatal("unbounded sequential read accepted")
	}
	handle, _, err := adapter.OpenSequential(context.Background(), snapshot, point, entry, ReadRequest{MaxBytes: 10})
	if err != nil {
		t.Fatal(err)
	}
	value, err := io.ReadAll(handle)
	if err != nil || string(value) != "0123456789" || handle.Close() != nil {
		t.Fatalf("sequential value=%q read=%v", value, err)
	}
	rangeHandle, _, err := adapter.OpenRange(context.Background(), snapshot, point, entry, ByteRange{Offset: 2, Length: 4})
	if err != nil {
		t.Fatal(err)
	}
	value, err = io.ReadAll(rangeHandle)
	if err != nil || string(value) != "2345" || rangeHandle.Close() != nil {
		t.Fatalf("range value=%q read=%v", value, err)
	}

	unproven := snapshot
	runtimeAccess := unproven.Access.AdapterData.(RsyncRuntimeAccess)
	runtimeAccess.RangeProven = false
	unproven.Access.AdapterData = runtimeAccess
	if _, _, err := adapter.OpenRange(context.Background(), unproven, point, entry, ByteRange{Offset: 0, Length: 1}); !errors.Is(err, backupasset.ErrCapabilityUnavailable) {
		t.Fatalf("unproven Range error=%v", err)
	}

	limited, _, err := adapter.OpenSequential(context.Background(), snapshot, point, entry, ReadRequest{MaxBytes: 3})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(limited); !errors.Is(err, backupasset.ErrCapabilityUnavailable) {
		t.Fatalf("limit read error=%v", err)
	}
	if err := limited.Close(); !errors.Is(err, backupasset.ErrCapabilityUnavailable) {
		t.Fatalf("limit close error=%v", err)
	}
}

func TestRsyncRejectsChangedSourceAndSymlinkOpen(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("strict local provider access is Linux-only")
	}
	root := rsyncTreeForTest(t)
	if err := os.Symlink("file.txt", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	adapter := newRsyncAdapterForTest(t)
	binding := rsyncBindingForTest(root)
	observation, err := adapter.Probe(context.Background(), binding, testOperationLimits())
	if err != nil {
		t.Fatal(err)
	}
	snapshot := rsyncSnapshotForTest(binding, observation)
	point := rsyncPointLocator(observation.SourceRevision)
	if _, _, err := adapter.OpenSequential(context.Background(), snapshot, point, EntryLocator{Native: "link"}, ReadRequest{MaxBytes: 10}); err == nil {
		t.Fatal("symlink opened")
	}
	if err := os.WriteFile(filepath.Join(root, "new-file"), []byte("change"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.ListEntries(context.Background(), snapshot, point, EntryLocator{}, PageRequest{Limit: 10}); !errors.Is(err, backupasset.ErrCapabilityUnavailable) {
		t.Fatalf("changed source error=%v", err)
	}
}

func TestRsyncPackageCannotReachTaskExecutor(t *testing.T) {
	source, err := os.ReadFile("rsync.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"task/executor", "RsyncExecutor", "EnsureLocalTargetReady", "mkdir", "write-check", "restore"} {
		if bytes.Contains(source, []byte(forbidden)) {
			t.Fatalf("Rsync reader source contains forbidden dependency %q", forbidden)
		}
	}
}

func rsyncTreeForTest(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "dir"), 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}

func newRsyncAdapterForTest(t *testing.T) *RsyncAdapter {
	t.Helper()
	now := time.Date(2026, 7, 13, 7, 0, 0, 0, time.UTC)
	material := backupasset.DomainKeyMaterial{Version: 1, Domain: backupasset.KeyDomainCursorSigning, Key: []byte("FAKE_CURSOR_SIGNING_KEY_FOR_TEST_ONLY")}
	keys := staticCursorKeys{active: material, versions: map[int]backupasset.DomainKeyMaterial{1: material}}
	adapter, err := NewRsyncAdapter(NewCursorCodec(keys, func() time.Time { return now }, time.Hour), testOperationLimits(), 100, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

func rsyncBindingForTest(root string) AccessBinding {
	return AccessBinding{
		Provider: backupasset.ProviderRsync, RepositoryID: strings.Repeat("2", 32), TaskID: 7, NodeID: 9,
		IdentitySalt: bytes.Repeat([]byte{0x42}, IdentitySaltBytes), EndpointFacts: []string{"local", "filesystem"},
		AdapterData: RsyncRuntimeAccess{Tree: fileaccess.NewLocalTree(), Root: fileaccess.Root{Path: root}},
	}
}

func rsyncSnapshotForTest(binding AccessBinding, observation RepositoryObservation) ReadSnapshot {
	runtimeAccess := binding.AdapterData.(RsyncRuntimeAccess)
	runtimeAccess.RangeProven = observation.Capabilities.OpenRange
	binding.AdapterData = runtimeAccess
	return ReadSnapshot{RepositoryID: binding.RepositoryID, CapabilityRevision: 1, SourceRevision: observation.SourceRevision, Access: binding}
}
