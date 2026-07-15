package provider

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/sshutil"
)

func TestResticBackupParsesFinalSummaryAndSafeProgress(t *testing.T) {
	payload := readResticPublicationFixture(t, "backup-success.ndjson")
	for _, test := range []struct {
		name    string
		payload []byte
	}{
		{name: "offset timestamps", payload: payload},
		{name: "UTC timestamps", payload: bytes.ReplaceAll(bytes.ReplaceAll(payload, []byte("2026-07-14T11:00:00+08:00"), []byte("2026-07-14T03:00:00Z")), []byte("2026-07-14T11:00:02.123456789+08:00"), []byte("2026-07-14T03:00:02.123456789Z"))},
	} {
		t.Run(test.name, func(t *testing.T) {
			stream := &fakePublicationExecution{Reader: bytes.NewReader(test.payload), completion: CommandCompletion{ExitCode: 0, ExitCodeKnown: true}}
			transport := &fakePublicationTransport{execution: stream}
			adapter := newPublicationResticAdapterForTest(t, transport, testOperationLimits())
			var progress []ResticBackupProgress
			result, err := adapter.Backup(context.Background(), publicationAttemptForTest(), ResticBackupInput{
				Source: "/private/source", Excludes: []string{"/var/cache"},
			}, func(value ResticBackupProgress) { progress = append(progress, value) })
			if err != nil {
				t.Fatalf("backup: %v", err)
			}
			if result.Completion != backupasset.CompletionKnownExitZero || result.ExitCode != 0 || result.EvidenceCode != "" || result.ProviderCommit == nil {
				t.Fatalf("backup result=%+v", result)
			}
			commit := result.ProviderCommit
			if commit.NativePointID != strings.Repeat("a", 64) || !commit.CaptureStartedAt.Equal(time.Date(2026, 7, 14, 3, 0, 0, 0, time.UTC)) || !commit.CaptureFinishedAt.Equal(time.Date(2026, 7, 14, 3, 0, 2, 123456789, time.UTC)) || commit.FilesProcessed != 7 || commit.LogicalBytes != 16384 {
				t.Fatalf("commit=%+v", commit)
			}
			if !stream.joined || len(progress) != 1 || progress[0].Percent != 50 || progress[0].FilesDone != 4 || !progress[0].ObservedAt.Equal(time.Date(2026, 7, 14, 3, 0, 0, 0, time.UTC)) {
				t.Fatalf("joined=%v progress=%+v", stream.joined, progress)
			}
			if len(transport.invocations) != 1 || transport.invocations[0].Operation != OperationResticBackup || transport.maxBytes != 1<<20 {
				t.Fatalf("invocations=%+v maxBytes=%d", transport.invocations, transport.maxBytes)
			}
		})
	}
}

func TestResticBackupEvidenceDefectsDrainBeforeJoinedExitZero(t *testing.T) {
	success := readResticPublicationFixture(t, "backup-success.ndjson")
	tooLarge := append([]byte(`{"message_type":"status","current_files":["`+strings.Repeat("x", 256)+`"]}`+"\n"), success...)
	tests := []struct {
		name   string
		body   []byte
		code   backupasset.PublicationFailureCode
		limits OperationLimits
	}{
		{"missing summary", readResticPublicationFixture(t, "backup-missing-summary.ndjson"), backupasset.FailureEvidenceMissingSummary, testOperationLimits()},
		{"malformed JSON", readResticPublicationFixture(t, "backup-malformed.ndjson"), backupasset.FailureEvidenceMalformedStream, testOperationLimits()},
		{"truncated summary", readResticPublicationFixture(t, "backup-truncated.ndjson"), backupasset.FailureEvidenceMalformedStream, testOperationLimits()},
		{"duplicate summary", append(append([]byte(nil), success...), append(bytes.TrimSpace(success[strings.LastIndex(string(success), "{\"message_type\":\"summary\""):]), '\n')...), backupasset.FailureEvidenceDuplicateSummary, testOperationLimits()},
		{"row after summary", append(append([]byte(nil), success...), []byte(`{"message_type":"future_after_summary"}`+"\n")...), backupasset.FailureEvidenceNonFinalSummary, testOperationLimits()},
		{"missing message type", append([]byte("{}\n"), success...), backupasset.FailureEvidenceMalformedStream, testOperationLimits()},
		{"blank message type", append([]byte(`{"message_type":""}`+"\n"), success...), backupasset.FailureEvidenceMalformedStream, testOperationLimits()},
		{"non-string message type", append([]byte(`{"message_type":7}`+"\n"), success...), backupasset.FailureEvidenceMalformedStream, testOperationLimits()},
		{"invalid status counter", append([]byte(`{"message_type":"status","percent_done":-1}`+"\n"), success...), backupasset.FailureEvidenceMalformedStream, testOperationLimits()},
		{"invalid verbose action", append([]byte(`{"message_type":"verbose_status","action":"unknown"}`+"\n"), success...), backupasset.FailureEvidenceMalformedStream, testOperationLimits()},
		{"oversized bounded record", tooLarge, backupasset.FailureEvidenceMalformedStream, OperationLimits{Timeout: time.Minute, MaxMetadataBytes: 1 << 20, MaxStderrBytes: 64 << 10, MaxRecordBytes: 64, MaxItems: 1000}},
		{"dry run", bytes.ReplaceAll(success, []byte(`"dry_run":false`), []byte(`"dry_run":true`)), backupasset.FailureEvidenceInvalidNativeID, testOperationLimits()},
		{"uppercase ID", bytes.ReplaceAll(success, []byte(strings.Repeat("a", 64)), []byte(strings.Repeat("A", 64))), backupasset.FailureEvidenceInvalidNativeID, testOperationLimits()},
		{"short ID", bytes.ReplaceAll(success, []byte(strings.Repeat("a", 64)), []byte("abc")), backupasset.FailureEvidenceInvalidNativeID, testOperationLimits()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stream := &fakePublicationExecution{Reader: bytes.NewReader(test.body), completion: CommandCompletion{ExitCode: 0, ExitCodeKnown: true}}
			adapter := newPublicationResticAdapterForTest(t, &fakePublicationTransport{execution: stream}, test.limits)
			result, err := adapter.Backup(context.Background(), publicationAttemptForTest(), ResticBackupInput{Source: "/private/source"}, nil)
			if err != nil || result.Completion != backupasset.CompletionKnownExitZero || result.ExitCode != 0 || result.ProviderCommit != nil || result.EvidenceCode != test.code || !stream.joined || stream.canceled {
				t.Fatalf("result=%+v err=%v joined=%v canceled=%v", result, err, stream.joined, stream.canceled)
			}
		})
	}
}

func TestResticBackupKnownAndUnknownCompletionClasses(t *testing.T) {
	payload := readResticPublicationFixture(t, "backup-success.ndjson")
	tests := []struct {
		name       string
		completion CommandCompletion
		joinErr    error
		wantClass  backupasset.ProviderCompletionClass
		wantExit   int
		wantErr    bool
	}{
		{"exit 3", CommandCompletion{ExitCode: 3, ExitCodeKnown: true}, nil, backupasset.CompletionKnownNonzero, 3, true},
		{"exit 17", CommandCompletion{ExitCode: 17, ExitCodeKnown: true}, nil, backupasset.CompletionKnownNonzero, 17, true},
		{"timeout", CommandCompletion{}, sshutil.ErrCommandTimeout, backupasset.CompletionOutcomeUnknown, UnknownProviderExitCode, true},
		{"canceled", CommandCompletion{}, context.Canceled, backupasset.CompletionOutcomeUnknown, UnknownProviderExitCode, true},
		{"output limit", CommandCompletion{}, sshutil.ErrCommandOutputLimit, backupasset.CompletionOutcomeUnknown, UnknownProviderExitCode, true},
		{"read lifecycle", CommandCompletion{}, sshutil.ErrCommandFailed, backupasset.CompletionOutcomeUnknown, UnknownProviderExitCode, true},
		{"unknown exit", CommandCompletion{ExitCodeKnown: false}, nil, backupasset.CompletionOutcomeUnknown, UnknownProviderExitCode, true},
		{"known reserved exit", CommandCompletion{ExitCode: UnknownProviderExitCode, ExitCodeKnown: true}, nil, backupasset.CompletionOutcomeUnknown, UnknownProviderExitCode, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stream := &fakePublicationExecution{Reader: bytes.NewReader(payload), completion: test.completion, joinErr: test.joinErr}
			adapter := newPublicationResticAdapterForTest(t, &fakePublicationTransport{execution: stream}, testOperationLimits())
			result, err := adapter.Backup(context.Background(), publicationAttemptForTest(), ResticBackupInput{Source: "/private/source"}, nil)
			if result.Completion != test.wantClass || result.ExitCode != test.wantExit || result.ProviderCommit != nil || (err != nil) != test.wantErr || !stream.joined {
				t.Fatalf("result=%+v err=%v joined=%v", result, err, stream.joined)
			}
		})
	}
	if UnknownProviderExitCode != -1 {
		t.Fatalf("unknown provider exit code=%d", UnknownProviderExitCode)
	}
}

func TestResticBackupHardStreamLimitCancelsIntoUnknownCompletion(t *testing.T) {
	stream := &fakePublicationExecution{Reader: bytes.NewReader(readResticPublicationFixture(t, "backup-success.ndjson")), completion: CommandCompletion{ExitCode: 0, ExitCodeKnown: true}}
	transport := &fakePublicationTransport{execution: stream}
	adapter := newPublicationResticAdapterWithConfigForTest(t, transport, testOperationLimits(), backupasset.PublicationConfig{BackupStreamMaxBytes: 32})
	result, err := adapter.Backup(context.Background(), publicationAttemptForTest(), ResticBackupInput{Source: "/private/source"}, nil)
	if !errors.Is(err, sshutil.ErrCommandOutputLimit) || result.Completion != backupasset.CompletionOutcomeUnknown || result.ExitCode != UnknownProviderExitCode || result.ProviderCommit != nil || !stream.canceled || transport.maxBytes != 32 {
		t.Fatalf("result=%+v err=%v canceled=%v maxBytes=%d", result, err, stream.canceled, transport.maxBytes)
	}
}

func TestResticBackupRejectsMismatchedPrepopulatedAuditBeforeTransport(t *testing.T) {
	stream := &fakePublicationExecution{Reader: bytes.NewReader(readResticPublicationFixture(t, "backup-success.ndjson")), completion: CommandCompletion{ExitCode: 0, ExitCodeKnown: true}}
	transport := &fakePublicationTransport{execution: stream}
	attempt := publicationAttemptForTest()
	runtime := attempt.Access.AdapterData.(ResticRuntimeAccess)
	runtime.Command.Audit.Username = "different-user"
	attempt.Access.AdapterData = runtime
	result, err := newPublicationResticAdapterForTest(t, transport, testOperationLimits()).Backup(context.Background(), attempt, ResticBackupInput{Source: "/private/source"}, nil)
	if !errors.Is(err, backupasset.ErrInvalidState) || result.Completion != backupasset.CompletionOutcomeUnknown || len(transport.invocations) != 0 {
		t.Fatalf("result=%+v err=%v invocation_count=%d", result, err, len(transport.invocations))
	}
}

func TestResticBackupRejectsUnexpectedAdapterRevisionBeforeTransport(t *testing.T) {
	stream := &fakePublicationExecution{Reader: bytes.NewReader(readResticPublicationFixture(t, "backup-success.ndjson")), completion: CommandCompletion{ExitCode: 0, ExitCodeKnown: true}}
	transport := &fakePublicationTransport{execution: stream}
	attempt := publicationAttemptForTest()
	attempt.AdapterRevision = "unexpected-revision"
	result, err := newPublicationResticAdapterForTest(t, transport, testOperationLimits()).Backup(context.Background(), attempt, ResticBackupInput{Source: "/private/source"}, nil)
	if !errors.Is(err, backupasset.ErrInvalidState) || result.Completion != backupasset.CompletionOutcomeUnknown || len(transport.invocations) != 0 {
		t.Fatalf("result=%+v err=%v invocation_count=%d", result, err, len(transport.invocations))
	}
}

func TestResticBackupStderrTruncationDoesNotAlterJoinedResult(t *testing.T) {
	stream := &fakePublicationExecution{
		Reader:     bytes.NewReader(readResticPublicationFixture(t, "backup-success.ndjson")),
		completion: CommandCompletion{ExitCode: 0, ExitCodeKnown: true, Stderr: []byte("stderr payload that must remain private"), StderrTruncated: true},
	}
	adapter := newPublicationResticAdapterForTest(t, &fakePublicationTransport{execution: stream}, testOperationLimits())
	result, err := adapter.Backup(context.Background(), publicationAttemptForTest(), ResticBackupInput{Source: "/private/source"}, nil)
	if err != nil || result.Completion != backupasset.CompletionKnownExitZero || result.ProviderCommit == nil {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestResticLookupAttemptParsesStoredSummaryAndOriginalPresence(t *testing.T) {
	payload := readResticPublicationFixture(t, "snapshots-exact.json")
	payload = bytes.Replace(payload, []byte(`"summary": {`), []byte(`"future_field":{"raw_path":"/private/source"},"summary": {`), 1)
	stream := &fakePublicationExecution{Reader: bytes.NewReader(payload), completion: CommandCompletion{ExitCode: 0, ExitCodeKnown: true}}
	transport := &fakePublicationTransport{execution: stream}
	adapter := newPublicationResticAdapterForTest(t, transport, testOperationLimits())
	points, err := adapter.LookupAttempt(context.Background(), publicationAttemptForTest())
	if err != nil || len(points) != 1 || points[0].NativePointID != strings.Repeat("a", 64) || points[0].OriginalPresent || points[0].Original != nil || points[0].Summary == nil {
		t.Fatalf("points=%+v err=%v", points, err)
	}
	if !points[0].Summary.BackupStartedAt.Equal(time.Date(2026, 7, 14, 3, 0, 0, 0, time.UTC)) || points[0].Summary.FilesProcessed != 7 || points[0].Summary.LogicalBytes != 16384 || !stream.joined {
		t.Fatalf("summary=%+v joined=%v", points[0].Summary, stream.joined)
	}
	if len(transport.invocations) != 1 || transport.invocations[0].Operation != OperationResticSnapshotsByTags || !strings.Contains(strings.Join(transport.invocations[0].Args, "\x00"), "xirang.link.v1.aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa,xirang.point.v1.bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb") {
		t.Fatalf("lookup invocations=%+v", transport.invocations)
	}
}

func TestResticLookupAttemptKeepsOriginalNullEmptyAndRewriteDistinct(t *testing.T) {
	base := `{"id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","time":"2026-07-14T03:00:00Z","tags":["xirang.link.v1.aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","xirang.point.v1.bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"]}`
	tests := []struct {
		name           string
		payload        string
		wantPresent    bool
		wantOriginal   *string
		wantSummaryNil bool
	}{
		{"absent", `[` + base + `]`, false, nil, true},
		{"explicit null", `[` + strings.Replace(base, `}`, `,"original":null}`, 1) + `]`, true, nil, true},
		{"empty", `[` + strings.Replace(base, `}`, `,"original":""}`, 1) + `]`, true, stringPointer(""), true},
		{"rewritten", `[` + strings.Replace(base, `}`, `,"original":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}`, 1) + `]`, true, stringPointer(strings.Repeat("b", 64)), true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stream := &fakePublicationExecution{Reader: strings.NewReader(test.payload), completion: CommandCompletion{ExitCode: 0, ExitCodeKnown: true}}
			adapter := newPublicationResticAdapterForTest(t, &fakePublicationTransport{execution: stream}, testOperationLimits())
			points, err := adapter.LookupAttempt(context.Background(), publicationAttemptForTest())
			if err != nil || len(points) != 1 || points[0].OriginalPresent != test.wantPresent || !sameStringPointer(points[0].Original, test.wantOriginal) || (points[0].Summary == nil) != test.wantSummaryNil {
				t.Fatalf("points=%+v err=%v", points, err)
			}
		})
	}
}

func TestResticPublicationStrategyRetainsKnownBackupAndReconciliationFixtures(t *testing.T) {
	for _, test := range []struct {
		name       string
		fixture    string
		wantCommit bool
		wantCode   backupasset.PublicationFailureCode
	}{
		{name: "known success", fixture: "backup-success.ndjson", wantCommit: true},
		{name: "missing summary", fixture: "backup-missing-summary.ndjson", wantCode: backupasset.FailureEvidenceMissingSummary},
	} {
		t.Run(test.name, func(t *testing.T) {
			stream := &fakePublicationExecution{Reader: bytes.NewReader(readResticPublicationFixture(t, test.fixture)), completion: CommandCompletion{ExitCode: 0, ExitCodeKnown: true}}
			adapter := newPublicationResticAdapterForTest(t, &fakePublicationTransport{execution: stream}, testOperationLimits())
			strategy, err := NewResticPublicationStrategy(adapter, adapter)
			if err != nil {
				t.Fatal(err)
			}
			prepared, err := strategy.Prepare(context.Background(), PublicationPrepareRequest{
				Attempt:     NewResticPublicationAttempt(publicationAttemptForTest()),
				ResticInput: &ResticBackupInput{Source: "/private/source"},
			})
			if err != nil {
				t.Fatalf("prepare strategy: %v", err)
			}
			result, err := strategy.Execute(context.Background(), prepared, PublicationProgress{})
			if err != nil || result.Completion != backupasset.CompletionKnownExitZero || result.EvidenceCode != test.wantCode || (result.ProviderCommit != nil) != test.wantCommit {
				t.Fatalf("strategy result=%+v err=%v", result, err)
			}
			if test.wantCommit {
				commit, err := strategy.RecordCommit(context.Background(), prepared, result)
				if err != nil {
					t.Fatalf("record typed Restic commit: %v", err)
				}
				typed, err := commit.ResticCommit()
				if err != nil || typed.NativePointID != strings.Repeat("a", 64) {
					t.Fatalf("typed Restic commit=%+v err=%v", typed, err)
				}
			} else if _, err := strategy.RecordCommit(context.Background(), prepared, result); err == nil {
				t.Fatal("strategy recorded a commit without a Restic summary")
			}
		})
	}

	rewritten := &fakePublicationExecution{
		Reader:     bytes.NewReader(readResticPublicationFixture(t, "snapshots-rewritten.json")),
		completion: CommandCompletion{ExitCode: 0, ExitCodeKnown: true},
	}
	adapter := newPublicationResticAdapterForTest(t, &fakePublicationTransport{execution: rewritten}, testOperationLimits())
	strategy, err := NewResticPublicationStrategy(adapter, adapter)
	if err != nil {
		t.Fatal(err)
	}
	result, err := strategy.Reconcile(context.Background(), PublicationReconcileRequest{Attempt: NewResticPublicationAttempt(publicationAttemptForTest())})
	if err != nil || len(result.ResticObservations) != 3 || result.ResticObservations[0].Original == nil || *result.ResticObservations[0].Original != strings.Repeat("a", 64) {
		t.Fatalf("rewritten fixture lost Restic lineage facts: result=%+v err=%v", result, err)
	}
}

func TestResticLookupAttemptLeavesMissingOrInvalidStoredSummaryUnusable(t *testing.T) {
	for _, payload := range []string{
		`[{"id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","time":"2026-07-14T03:00:00Z","tags":["xirang.link.v1.aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","xirang.point.v1.bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"]}]`,
		`[{"id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","time":"2026-07-14T03:00:00Z","tags":["xirang.link.v1.aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","xirang.point.v1.bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"],"summary":{"backup_start":"invalid"}}]`,
	} {
		stream := &fakePublicationExecution{Reader: strings.NewReader(payload), completion: CommandCompletion{ExitCode: 0, ExitCodeKnown: true}}
		adapter := newPublicationResticAdapterForTest(t, &fakePublicationTransport{execution: stream}, testOperationLimits())
		points, err := adapter.LookupAttempt(context.Background(), publicationAttemptForTest())
		if err != nil || len(points) != 1 || points[0].Summary != nil {
			t.Fatalf("points=%+v err=%v", points, err)
		}
	}
}

func TestResticLookupAttemptEnforcesMetadataItemLimit(t *testing.T) {
	payload := `[
{"id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","time":"2026-07-14T03:00:00Z","tags":["xirang.link.v1.aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","xirang.point.v1.bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"]},
{"id":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","time":"2026-07-14T03:00:01Z","tags":["xirang.link.v1.aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","xirang.point.v1.bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"]}
]`
	stream := &fakePublicationExecution{Reader: strings.NewReader(payload), completion: CommandCompletion{ExitCode: 0, ExitCodeKnown: true}}
	limits := testOperationLimits()
	limits.MaxItems = 1
	_, err := newPublicationResticAdapterForTest(t, &fakePublicationTransport{execution: stream}, limits).LookupAttempt(context.Background(), publicationAttemptForTest())
	var capabilityErr *CapabilityError
	if !errors.As(err, &capabilityErr) || capabilityErr.Reason.Code != backupasset.CapabilityProviderResourceLimit {
		t.Fatalf("item limit error=%v", err)
	}
}

func newPublicationResticAdapterForTest(t *testing.T, transport *fakePublicationTransport, limits OperationLimits) *ResticAdapter {
	return newPublicationResticAdapterWithConfigForTest(t, transport, limits, backupasset.PublicationConfig{BackupStreamMaxBytes: 1 << 20})
}

func newPublicationResticAdapterWithConfigForTest(t *testing.T, transport *fakePublicationTransport, limits OperationLimits, config backupasset.PublicationConfig) *ResticAdapter {
	t.Helper()
	now := time.Date(2026, 7, 14, 3, 0, 0, 0, time.UTC)
	material := backupasset.DomainKeyMaterial{Version: 1, Domain: backupasset.KeyDomainCursorSigning, Key: []byte("FAKE_CURSOR_SIGNING_KEY_FOR_TEST_ONLY")}
	keys := staticCursorKeys{active: material, versions: map[int]backupasset.DomainKeyMaterial{1: material}}
	adapter, err := NewResticAdapterWithPublication(transport, transport, NewCursorCodec(keys, func() time.Time { return now }, time.Hour), func() (OperationLimits, error) {
		return limits, nil
	}, func() (backupasset.PublicationConfig, error) {
		return config, nil
	}, 100, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

func publicationAttemptForTest() ResticAttemptV1 {
	taskID := uint(41)
	taskRunID := uint(42)
	repositoryID := strings.Repeat("a", 32)
	pointID := strings.Repeat("c", 32)
	return ResticAttemptV1{
		Provider: backupasset.ProviderRestic, RepositoryID: repositoryID, RepositoryIdentity: NativeResticIdentityPrefix + strings.Repeat("f", 64),
		TaskRepositoryLinkID: strings.Repeat("b", 32), RecoveryPointID: pointID, TaskID: taskID, TaskRunID: taskRunID,
		RequiredTags:    [2]string{"xirang.link.v1.aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "xirang.point.v1.bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		PointDeadlineAt: time.Date(2026, 7, 14, 4, 0, 0, 0, time.UTC), CapabilityRevision: 1, AdapterRevision: resticAdapterRevision,
		Audit:  backupasset.PublicationAuditContext{Actor: backupasset.AuditActor{UserID: 7, Username: "operator", Role: "operator"}, CorrelationID: "corr.publication-42"},
		Access: AccessBinding{Provider: backupasset.ProviderRestic, RepositoryID: repositoryID, TaskID: taskID, Locator: "FAKE_REPOSITORY_LOCATOR_FOR_TEST_ONLY", Secret: []byte("FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY"), AdapterData: ResticRuntimeAccess{NativeRepositoryID: strings.Repeat("f", 64), Command: &RemoteCommandAccess{Audit: sshutil.DialAuditContext{UserID: 7, Username: "operator", Role: "operator", CorrelationID: "corr.publication-42", TaskID: &taskID, TaskRunID: &taskRunID}}}},
		Fence:  backupasset.LeaseFence{LeaseID: strings.Repeat("d", 32), RecoveryPointID: pointID, HolderType: backupasset.LeaseHolderPointPublication, OwnerID: "publication-worker", AttemptID: strings.Repeat("e", 32), FenceToken: strings.Repeat("f", 64)},
	}
}

func readResticPublicationFixture(t *testing.T, name string) []byte {
	t.Helper()
	payload, err := os.ReadFile("testdata/restic/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

type fakePublicationTransport struct {
	execution    CommandExecution
	executionErr error
	invocations  []CommandInvocation
	maxBytes     int64
}

func (*fakePublicationTransport) Run(context.Context, CommandInvocation, OperationLimits) (CommandOutput, error) {
	return CommandOutput{}, errors.New("unexpected read-only transport call")
}

func (*fakePublicationTransport) Open(context.Context, CommandInvocation, OperationLimits, int64) (ReadHandle, error) {
	return nil, errors.New("unexpected read-only stream call")
}

func (transport *fakePublicationTransport) OpenExecution(_ context.Context, invocation CommandInvocation, _ OperationLimits, maxBytes int64) (CommandExecution, error) {
	transport.invocations = append(transport.invocations, invocation)
	transport.maxBytes = maxBytes
	return transport.execution, transport.executionErr
}

type fakePublicationExecution struct {
	io.Reader
	completion CommandCompletion
	joinErr    error
	cancelErr  error
	joined     bool
	canceled   bool
}

func (execution *fakePublicationExecution) Join() (CommandCompletion, error) {
	execution.joined = true
	return execution.completion, execution.joinErr
}

func (execution *fakePublicationExecution) Cancel() error {
	execution.canceled = true
	return execution.cancelErr
}

func stringPointer(value string) *string { return &value }

func sameStringPointer(left, right *string) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}
