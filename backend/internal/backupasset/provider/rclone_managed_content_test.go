package provider

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"xirang/backend/internal/backupasset"
)

func TestRcloneManagedRecursiveProofControlsStatOpenAndClose(t *testing.T) {
	list := `[{"Path":"file.txt","Name":"file.txt","Size":4,"ModTime":"2026-07-16T01:00:00Z","IsDir":false,"Hashes":{"sha256":"abcd"}}]`
	stat := `{"Path":"file.txt","Name":"file.txt","Size":4,"ModTime":"2026-07-16T01:00:00Z","IsDir":false,"Hashes":{"sha256":"abcd"}}`
	calls := 0
	var proofErr error
	transport := &fakeCommandTransport{
		runFunc: func(invocation CommandInvocation) (CommandOutput, error) {
			switch invocation.Operation {
			case OperationRcloneList:
				return CommandOutput{Stdout: []byte(list)}, nil
			case OperationRcloneStat:
				return CommandOutput{Stdout: []byte(stat)}, nil
			default:
				return CommandOutput{}, errors.New("unexpected managed command")
			}
		},
		openFunc: func(CommandInvocation, int64) (ReadHandle, error) {
			return io.NopCloser(strings.NewReader("data")), nil
		},
	}
	adapter := newRcloneAdapterForTest(t, transport)
	binding := rcloneBindingForTest()
	binding.Locator = "remote-name:managed/v1/points/point/attempts/attempt/data"
	managed := &RcloneManagedPointAccess{
		RecoveryPointID: strings.Repeat("a", 32), AttemptID: strings.Repeat("b", 32),
		DataLocator: binding.Locator, ManifestDigest: strings.Repeat("c", 64), SourceRevision: strings.Repeat("d", 64), Committed: true,
		ValidateSnapshot: func(context.Context) error { calls++; return proofErr },
	}
	binding.AdapterData = RcloneRuntimeAccess{Backend: "s3", ConfigSource: RcloneConfigBound, ManagedPoint: managed}
	snapshot := ReadSnapshot{RepositoryID: binding.RepositoryID, CapabilityRevision: 1, SourceRevision: managed.SourceRevision, Access: binding}
	point := rcloneManagedPointLocator(*managed)
	entry := EntryLocator{Native: "file.txt"}
	ctx := context.Background()
	if _, err := adapter.StatEntry(ctx, snapshot, point, entry); err != nil || calls != 2 {
		t.Fatalf("managed stat error=%v proof calls=%d", err, calls)
	}
	handle, _, err := adapter.OpenSequential(ctx, snapshot, point, entry, ReadRequest{MaxBytes: 4})
	if err != nil {
		t.Fatal(err)
	}
	if value, err := io.ReadAll(handle); err != nil || string(value) != "data" {
		t.Fatalf("read=%q err=%v", value, err)
	}
	if err := handle.Close(); err != nil || calls != 4 {
		t.Fatalf("close error=%v proof calls=%d", err, calls)
	}
	for _, invocation := range transport.requests {
		if invocation.Operation == OperationRcloneList {
			t.Fatal("managed stat/open loaded a single-level root fingerprint")
		}
	}
	proofErr = errors.New("FAKE_MARKER_OR_RECURSIVE_OBSERVATION_DRIFT")
	if _, err := adapter.StatEntry(ctx, snapshot, point, entry); !errors.Is(err, proofErr) {
		t.Fatalf("changed proof stat error=%v", err)
	}
	proofErr = nil
	handle, _, err = adapter.OpenSequential(ctx, snapshot, point, entry, ReadRequest{MaxBytes: 4})
	if err != nil {
		t.Fatal(err)
	}
	proofErr = errors.New("FAKE_CLOSE_PROOF_DRIFT")
	if err := handle.Close(); !errors.Is(err, proofErr) {
		t.Fatalf("changed proof close error=%v", err)
	}
	proofErr = nil
	handle, _, err = adapter.OpenSequential(ctx, snapshot, point, entry, ReadRequest{MaxBytes: 4})
	if err != nil {
		t.Fatal(err)
	}
	stat = strings.Replace(stat, "01:00:00Z", "01:00:01Z", 1)
	if err := handle.Close(); !errors.Is(err, backupasset.ErrCapabilityUnavailable) {
		t.Fatalf("same-size object mtime drift close error=%v", err)
	}
}

func TestRcloneManagedRecursiveProofRunsAfterListAndStat(t *testing.T) {
	list := `[{"Path":"file.txt","Name":"file.txt","Size":4,"ModTime":"2026-07-16T01:00:00Z","IsDir":false}]`
	stat := `{"Path":"file.txt","Name":"file.txt","Size":4,"ModTime":"2026-07-16T01:00:00Z","IsDir":false}`
	for _, operation := range []string{"list", "stat"} {
		t.Run(operation, func(t *testing.T) {
			calls := 0
			drift := errors.New("FAKE_POST_OPERATION_RECURSIVE_DRIFT")
			transport := &fakeCommandTransport{outputs: map[CommandOperation]CommandOutput{
				OperationRcloneList: {Stdout: []byte(list)}, OperationRcloneStat: {Stdout: []byte(stat)},
			}}
			adapter := newRcloneAdapterForTest(t, transport)
			binding := rcloneBindingForTest()
			managed := &RcloneManagedPointAccess{
				RecoveryPointID: strings.Repeat("a", 32), AttemptID: strings.Repeat("b", 32), DataLocator: binding.Locator,
				ManifestDigest: strings.Repeat("c", 64), SourceRevision: strings.Repeat("d", 64), Committed: true,
				ValidateSnapshot: func(context.Context) error {
					calls++
					if calls == 2 {
						return drift
					}
					return nil
				},
			}
			binding.AdapterData = RcloneRuntimeAccess{Backend: "local", ManagedPoint: managed}
			snapshot := ReadSnapshot{RepositoryID: binding.RepositoryID, CapabilityRevision: 1, SourceRevision: managed.SourceRevision, Access: binding}
			point := rcloneManagedPointLocator(*managed)
			var err error
			if operation == "list" {
				_, err = adapter.ListEntries(context.Background(), snapshot, point, EntryLocator{}, PageRequest{Limit: 10})
			} else {
				_, err = adapter.StatEntry(context.Background(), snapshot, point, EntryLocator{Native: "file.txt"})
			}
			if !errors.Is(err, drift) || calls != 2 {
				t.Fatalf("post-operation proof calls=%d err=%v", calls, err)
			}
		})
	}
}
