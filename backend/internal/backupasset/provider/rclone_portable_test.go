package provider

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"
)

type fakeRclonePortableRemote struct {
	presence       RcloneAttemptPresence
	observations   []RcloneManifestBundle
	observationAt  int
	controls       map[string][]byte
	mutations      []string
	failMutation   string
	tamperReadback string
	fullByteProof  RcloneFullByteProof
	copyDestSeen   *RclonePrivateLocator
	copyDestError  error
}

type fakeRcloneCommandTransport struct {
	completion CommandCompletion
	joinErr    error
	openErr    error
}

func (transport *fakeRcloneCommandTransport) Run(context.Context, CommandInvocation, OperationLimits) (CommandOutput, error) {
	return CommandOutput{}, errors.New("unexpected command Run call")
}

func (transport *fakeRcloneCommandTransport) Open(context.Context, CommandInvocation, OperationLimits, int64) (ReadHandle, error) {
	return nil, errors.New("unexpected command Open call")
}

func (transport *fakeRcloneCommandTransport) OpenExecution(context.Context, CommandInvocation, OperationLimits, int64) (CommandExecution, error) {
	if transport.openErr != nil {
		return nil, transport.openErr
	}
	return &fakeRcloneCommandExecution{completion: transport.completion, joinErr: transport.joinErr}, nil
}

type fakeRcloneCommandExecution struct {
	completion CommandCompletion
	joinErr    error
}

func (*fakeRcloneCommandExecution) Read([]byte) (int, error) { return 0, io.EOF }
func (execution *fakeRcloneCommandExecution) Join() (CommandCompletion, error) {
	return execution.completion, execution.joinErr
}
func (*fakeRcloneCommandExecution) Cancel() error { return nil }

type fakeRcloneStagedPayloadTransport struct{}

func (*fakeRcloneStagedPayloadTransport) Stage(context.Context, RemoteCommandAccess, StagedPayloadRequest) (StagedPayloadRef, error) {
	return StagedPayloadRef{}, errors.New("unexpected staged payload call")
}
func (*fakeRcloneStagedPayloadTransport) Cleanup(context.Context, RemoteCommandAccess, StagedPayloadRef) error {
	return errors.New("unexpected staged payload call")
}
func (*fakeRcloneStagedPayloadTransport) CleanupAged(context.Context, RemoteCommandAccess, time.Duration, int) error {
	return errors.New("unexpected staged payload call")
}

func portableNowForTest() time.Time { return time.Date(2026, 7, 16, 1, 30, 0, 0, time.UTC) }

func (remote *fakeRclonePortableRemote) ProbeAttempt(context.Context, RclonePortablePublicationRequest) (RcloneAttemptPresence, error) {
	return remote.presence, nil
}

func (remote *fakeRclonePortableRemote) Observe(_ context.Context, _ RclonePortablePublicationRequest, _ RclonePrivateLocator) (RcloneManifestBundle, error) {
	if remote.observationAt >= len(remote.observations) {
		return RcloneManifestBundle{}, errors.New("unexpected observation")
	}
	value := remote.observations[remote.observationAt]
	remote.observationAt++
	return value, nil
}

func (remote *fakeRclonePortableRemote) PutControl(_ context.Context, _ RclonePortablePublicationRequest, name string, payload []byte) error {
	remote.mutations = append(remote.mutations, name)
	if remote.failMutation == name {
		return errors.New("FAKE_REMOTE_MUTATION_FAILURE_FOR_TEST_ONLY")
	}
	if remote.controls == nil {
		remote.controls = make(map[string][]byte)
	}
	remote.controls[name] = append([]byte(nil), payload...)
	return nil
}

func (remote *fakeRclonePortableRemote) ReadControl(_ context.Context, _ RclonePortablePublicationRequest, name string, _ int64) ([]byte, error) {
	payload := append([]byte(nil), remote.controls[name]...)
	if remote.tamperReadback == name && len(payload) > 0 {
		payload[len(payload)-1] ^= 1
	}
	return payload, nil
}

func (remote *fakeRclonePortableRemote) TransferData(_ context.Context, _ RclonePortablePublicationRequest, copyDest *RclonePrivateLocator) error {
	remote.mutations = append(remote.mutations, "data")
	remote.copyDestSeen = copyDest
	if remote.failMutation == "data" {
		return errors.New("FAKE_DATA_FAILURE_FOR_TEST_ONLY")
	}
	return remote.copyDestError
}

func (remote *fakeRclonePortableRemote) VerifyFullBytes(context.Context, RclonePortablePublicationRequest, uint64) (RcloneFullByteProof, error) {
	return remote.fullByteProof, nil
}

func portablePublicationRequestForTest(t *testing.T, weak bool) RclonePortablePublicationRequest {
	t.Helper()
	configBytes, err := os.ReadFile("testdata/rclone/bound-config/b2.conf")
	if err != nil {
		t.Fatal(err)
	}
	bound, err := ValidateRcloneBoundConfigV1744(configBytes, "backup", []byte("FAKE_BOUND_CONFIG_IDENTITY_KEY_32_BYTES_FOR_TEST_ONLY"), 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	fixture := "testdata/rclone/lsjson-tree.json"
	if weak {
		fixture = "testdata/rclone/lsjson-weak-hash.json"
	}
	payload, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := BuildRcloneManifestV1(context.Background(), bytes.NewReader(payload), rcloneManifestOptionsForTest())
	if err != nil {
		t.Fatal(err)
	}
	attempt := validRcloneAttemptForTest(backupasset.PublicationVersionedPrefix)
	source := mustRclonePrivateLocatorForTest(t, "/srv/source")
	attemptRoot := mustRclonePrivateLocatorForTest(t, "backup:managed/v1/points/"+attempt.RecoveryPointID+"/attempts/"+attempt.AttemptID)
	dataRoot := mustRclonePrivateLocatorForTest(t, attemptRoot.value+"/data")
	controlRoot := mustRclonePrivateLocatorForTest(t, attemptRoot.value+"/control")
	return RclonePortablePublicationRequest{
		Attempt: attempt, BoundConfig: bound, Source: source, AttemptRoot: attemptRoot, DataRoot: dataRoot, ControlRoot: controlRoot,
		MarkerKey: []byte("FAKE_PORTABLE_MARKER_AUTH_KEY_32_BYTES_FOR_TEST_ONLY"), Manifest: manifest,
		CapabilityEvidenceDigest: strings.Repeat("a", 64), CostEvidenceDigest: strings.Repeat("b", 64),
		SettleInterval: time.Millisecond, FullVerifyMaxBytes: 2 << 30, ControlPayloadMaxBytes: 8 << 20,
		LowLevelRetries: 3, Runtime: RemoteCommandAccess{Node: model.Node{ID: 9}}, ManifestOptions: rcloneManifestOptionsForTest(),
	}
}

func TestRclonePortablePublishesAttemptDataManifestIndexAndCommitLast(t *testing.T) {
	request := portablePublicationRequestForTest(t, false)
	observation := rcloneObservationFromManifest(request.Manifest)
	remote := &fakeRclonePortableRemote{
		presence:     RcloneAttemptAbsent,
		observations: []RcloneManifestBundle{request.Manifest, request.Manifest, request.Manifest, request.Manifest},
	}
	publisher := NewRclonePortablePublisher(remote, func(time.Duration) {}, func() time.Time {
		return time.Date(2026, 7, 16, 1, 30, 0, 0, time.UTC)
	})
	commit, err := publisher.Publish(context.Background(), request)
	if err != nil {
		t.Fatalf("publish portable point: %v", err)
	}
	wantMutations := []string{"attempt.json", "data", "manifest-000000.jsonl", "manifest-000001.jsonl", "manifest-index.json", "commit.json"}
	if !reflect.DeepEqual(remote.mutations, wantMutations) {
		t.Fatalf("mutations=%v want=%v", remote.mutations, wantMutations)
	}
	if err := commit.Validate(); err != nil || commit.Portable == nil || commit.ManifestIndexDigest != request.Manifest.IndexDigest ||
		commit.SourceObservationDigest != observation.Digest || commit.DestinationObservationDigest != observation.Digest ||
		commit.Portable.CommitComponent != "commit.json" || commit.Portable.CommitAuthenticationDigest == "" {
		t.Fatalf("provider commit=%+v err=%v", commit, err)
	}
	if got := remote.controls["commit.json"]; len(got) == 0 {
		t.Fatal("commit marker was not written")
	}
}

func TestCommandRclonePortableRemoteClassifiesJoinedRcloneDirectoryProbeExitCodes(t *testing.T) {
	request := portablePublicationRequestForTest(t, false)
	limits := func() (OperationLimits, error) { return NewMetadataOperationLimits(time.Minute, 1<<20) }
	for name, test := range map[string]struct {
		completion CommandCompletion
		want       RcloneAttemptPresence
		wantErr    error
	}{
		"present":       {completion: CommandCompletion{ExitCodeKnown: true, ExitCode: 0}, want: RcloneAttemptPresent},
		"absent":        {completion: CommandCompletion{ExitCodeKnown: true, ExitCode: 3}, want: RcloneAttemptAbsent},
		"other nonzero": {completion: CommandCompletion{ExitCodeKnown: true, ExitCode: 17}, want: RcloneAttemptPresenceUnknown, wantErr: ErrRcloneAttemptPresenceUnknown},
		"unknown exit":  {completion: CommandCompletion{}, want: RcloneAttemptPresenceUnknown, wantErr: ErrRcloneAttemptPresenceUnknown},
	} {
		t.Run(name, func(t *testing.T) {
			commands := &fakeRcloneCommandTransport{completion: test.completion}
			remote, err := NewCommandRclonePortableRemote(commands, &fakeRcloneStagedPayloadTransport{}, limits)
			if err != nil {
				t.Fatal(err)
			}
			got, gotErr := remote.ProbeAttempt(context.Background(), request)
			if got != test.want || !errors.Is(gotErr, test.wantErr) {
				t.Fatalf("probe presence=%q err=%v want presence=%q err=%v", got, gotErr, test.want, test.wantErr)
			}
		})
	}
}

func TestRclonePortableWeakHashCannotCommitWithoutExactFullByteProof(t *testing.T) {
	request := portablePublicationRequestForTest(t, true)
	remote := &fakeRclonePortableRemote{
		presence:     RcloneAttemptAbsent,
		observations: []RcloneManifestBundle{request.Manifest, request.Manifest, request.Manifest, request.Manifest},
	}
	publisher := NewRclonePortablePublisher(remote, func(time.Duration) {}, portableNowForTest)
	if _, err := publisher.Publish(context.Background(), request); !errors.Is(err, ErrRcloneManifestFullByteProofRequired) {
		t.Fatalf("weak hash publish error=%v", err)
	}
	if containsString(remote.mutations, "commit.json") {
		t.Fatalf("weak manifest wrote commit without bytes: %v", remote.mutations)
	}

	remote = &fakeRclonePortableRemote{
		presence:     RcloneAttemptAbsent,
		observations: []RcloneManifestBundle{request.Manifest, request.Manifest, request.Manifest, request.Manifest},
		fullByteProof: RcloneFullByteProof{
			SourceDigest: strings.Repeat("1", 64), DestinationDigest: strings.Repeat("1", 64),
			VerifiedBytes: request.Manifest.LogicalBytes, Complete: true,
		},
	}
	publisher = NewRclonePortablePublisher(remote, func(time.Duration) {}, portableNowForTest)
	commit, err := publisher.Publish(context.Background(), request)
	if err != nil || commit.Portable.DownloadVerifiedBytes != request.Manifest.LogicalBytes {
		t.Fatalf("full-byte publish commit=%+v err=%v", commit, err)
	}
}

func TestRclonePortableCommitsProvenEmptySourceWithoutInventingData(t *testing.T) {
	request := portablePublicationRequestForTest(t, false)
	payload, err := os.ReadFile("testdata/rclone/lsjson-empty.json")
	if err != nil {
		t.Fatal(err)
	}
	request.Manifest, err = BuildRcloneManifestV1(context.Background(), bytes.NewReader(payload), rcloneManifestOptionsForTest())
	if err != nil {
		t.Fatal(err)
	}
	remote := &fakeRclonePortableRemote{
		presence:     RcloneAttemptAbsent,
		observations: []RcloneManifestBundle{request.Manifest, request.Manifest, request.Manifest, request.Manifest},
	}
	publisher := NewRclonePortablePublisher(remote, func(time.Duration) {}, portableNowForTest)
	commit, err := publisher.Publish(context.Background(), request)
	if err != nil {
		t.Fatalf("publish empty source: %v", err)
	}
	if commit.ManifestEntryCount != 0 || commit.LogicalBytes != 0 || len(commit.ManifestChunkDigests) != 0 ||
		!reflect.DeepEqual(remote.mutations, []string{"attempt.json", "data", "manifest-index.json", "commit.json"}) {
		t.Fatalf("empty commit=%+v mutations=%v", commit, remote.mutations)
	}
}

func TestRclonePortableFailsClosedForCollisionAndUnstableObservations(t *testing.T) {
	request := portablePublicationRequestForTest(t, false)
	changed := request.Manifest
	changed.ObservationDigest = strings.Repeat("f", 64)
	tests := map[string]struct {
		presence     RcloneAttemptPresence
		observations []RcloneManifestBundle
		want         error
	}{
		"visible collision":    {presence: RcloneAttemptPresent, want: ErrRcloneAttemptCollision},
		"presence unknown":     {presence: RcloneAttemptPresenceUnknown, want: ErrRcloneAttemptPresenceUnknown},
		"source drift":         {presence: RcloneAttemptAbsent, observations: []RcloneManifestBundle{request.Manifest, changed, request.Manifest, request.Manifest}, want: ErrRcloneSourceDrift},
		"destination unstable": {presence: RcloneAttemptAbsent, observations: []RcloneManifestBundle{request.Manifest, request.Manifest, request.Manifest, changed}, want: ErrRcloneDestinationUnstable},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			remote := &fakeRclonePortableRemote{presence: test.presence, observations: test.observations}
			publisher := NewRclonePortablePublisher(remote, func(time.Duration) {}, portableNowForTest)
			if _, err := publisher.Publish(context.Background(), request); !errors.Is(err, test.want) {
				t.Fatalf("publish error=%v want=%v", err, test.want)
			}
			if containsString(remote.mutations, "commit.json") {
				t.Fatalf("unsafe publication wrote commit: %v", remote.mutations)
			}
		})
	}
}

func TestRclonePortableRejectsStableObservationsThatDoNotMatchCommittedManifest(t *testing.T) {
	request := portablePublicationRequestForTest(t, false)
	actual := request.Manifest
	actual.ObservationDigest = strings.Repeat("e", 64)
	remote := &fakeRclonePortableRemote{
		presence:     RcloneAttemptAbsent,
		observations: []RcloneManifestBundle{actual, actual, actual, actual},
	}
	publisher := NewRclonePortablePublisher(remote, func(time.Duration) {}, portableNowForTest)
	if _, err := publisher.Publish(context.Background(), request); !errors.Is(err, ErrRcloneManifestObservationMismatch) {
		t.Fatalf("stale manifest publication error=%v", err)
	}
	if containsString(remote.mutations, "commit.json") {
		t.Fatalf("stale manifest wrote commit: %v", remote.mutations)
	}
}

func TestRclonePortableCrashBoundariesNeverMutateAfterFailure(t *testing.T) {
	request := portablePublicationRequestForTest(t, false)
	all := []string{"attempt.json", "data", "manifest-000000.jsonl", "manifest-index.json", "commit.json"}
	for _, failure := range all {
		t.Run(failure, func(t *testing.T) {
			remote := &fakeRclonePortableRemote{
				presence: RcloneAttemptAbsent, failMutation: failure,
				observations: []RcloneManifestBundle{request.Manifest, request.Manifest, request.Manifest, request.Manifest},
			}
			publisher := NewRclonePortablePublisher(remote, func(time.Duration) {}, portableNowForTest)
			if _, err := publisher.Publish(context.Background(), request); err == nil {
				t.Fatal("injected mutation failure unexpectedly succeeded")
			}
			failureIndex := indexString(remote.mutations, failure)
			if failureIndex < 0 || failureIndex != len(remote.mutations)-1 {
				t.Fatalf("publisher mutated after %s failure: %v", failure, remote.mutations)
			}
		})
	}

	remote := &fakeRclonePortableRemote{
		presence: RcloneAttemptAbsent, tamperReadback: "commit.json",
		observations: []RcloneManifestBundle{request.Manifest, request.Manifest, request.Manifest, request.Manifest},
	}
	publisher := NewRclonePortablePublisher(remote, func(time.Duration) {}, portableNowForTest)
	if _, err := publisher.Publish(context.Background(), request); !errors.Is(err, ErrRcloneCommitOutcomeUnknown) {
		t.Fatalf("tampered commit readback error=%v", err)
	}
	if remote.mutations[len(remote.mutations)-1] != "commit.json" {
		t.Fatalf("commit was not final mutation: %v", remote.mutations)
	}
}

func TestRclonePortableCopyDestRequiresExactParentLeaseAndRetryGetsFreshAttempt(t *testing.T) {
	request := portablePublicationRequestForTest(t, false)
	parent := mustRclonePrivateLocatorForTest(t, "backup:managed/v1/points/parent/attempts/exact/data")
	request.CopyDest = &parent
	request.ParentLeaseEligible = false
	remote := &fakeRclonePortableRemote{presence: RcloneAttemptAbsent, observations: []RcloneManifestBundle{request.Manifest, request.Manifest, request.Manifest, request.Manifest}}
	publisher := NewRclonePortablePublisher(remote, func(time.Duration) {}, portableNowForTest)
	if _, err := publisher.Publish(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if remote.copyDestSeen != nil {
		t.Fatal("copy-dest used without exact parent lease")
	}

	request.ParentLeaseEligible = true
	remote = &fakeRclonePortableRemote{presence: RcloneAttemptAbsent, observations: []RcloneManifestBundle{request.Manifest, request.Manifest, request.Manifest, request.Manifest}, copyDestError: ErrRcloneCopyDestRetryRequired}
	publisher = NewRclonePortablePublisher(remote, func(time.Duration) {}, portableNowForTest)
	if _, err := publisher.Publish(context.Background(), request); !errors.Is(err, ErrRcloneCopyDestRetryRequired) || remote.copyDestSeen == nil {
		t.Fatalf("copy-dest failure err=%v seen=%v", err, remote.copyDestSeen)
	}
	pointID := request.Attempt.RecoveryPointID
	next, err := NewRclonePortableRetryAttempt(request.Attempt, rand.Reader)
	if err != nil || next.RecoveryPointID != pointID || next.AttemptID == request.Attempt.AttemptID || len(next.AttemptID) != 32 {
		t.Fatalf("fresh retry attempt=%+v err=%v", next, err)
	}
}

func TestRclonePortableAttemptIDUsesAtLeast128BitsAndRejectsShortEntropy(t *testing.T) {
	id, err := NewRclonePortableAttemptID(rand.Reader)
	if err != nil || len(id) != 32 || id == strings.Repeat("0", 32) {
		t.Fatalf("attempt ID=%q err=%v", id, err)
	}
	if _, err := NewRclonePortableAttemptID(io.LimitReader(bytes.NewReader(make([]byte, 8)), 8)); err == nil {
		t.Fatal("short attempt entropy was accepted")
	}
}

func TestRclonePortableReconcileAcceptsOnlyExactAuthenticatedCommit(t *testing.T) {
	request := portablePublicationRequestForTest(t, false)
	remote := &fakeRclonePortableRemote{presence: RcloneAttemptAbsent, observations: []RcloneManifestBundle{request.Manifest, request.Manifest, request.Manifest, request.Manifest}}
	publisher := NewRclonePortablePublisher(remote, func(time.Duration) {}, portableNowForTest)
	want, err := publisher.Publish(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	got, err := publisher.Reconcile(context.Background(), request)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("reconcile got=%+v err=%v want=%+v", got, err, want)
	}
	restarted := request
	restarted.Manifest = RcloneManifestBundle{}
	got, err = publisher.Reconcile(context.Background(), restarted)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("restart reconcile got=%+v err=%v want=%+v", got, err, want)
	}
	remote.controls["commit.json"][0] ^= 1
	if _, err := publisher.Reconcile(context.Background(), request); !errors.Is(err, ErrRcloneMarkerMismatch) {
		t.Fatalf("tampered reconcile error=%v", err)
	}
}

func TestRclonePublicationStrategyRegistersOneClosedPortableLane(t *testing.T) {
	request := portablePublicationRequestForTest(t, false)
	remote := &fakeRclonePortableRemote{presence: RcloneAttemptAbsent, observations: []RcloneManifestBundle{request.Manifest, request.Manifest, request.Manifest, request.Manifest}}
	strategy, err := NewRclonePublicationStrategy(
		NewRclonePortablePublisher(remote, func(time.Duration) {}, portableNowForTest),
		NewRcloneNativePublisher(&rcloneNativeDataPlaneFake{}, portableNowForTest),
	)
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	if err := registry.Register(backupasset.ProviderRclone, Registration{Prober: &fakeProvider{}, PublicationStrategy: strategy}); err != nil {
		t.Fatal(err)
	}
	gotStrategy, err := registry.PublicationStrategy(backupasset.ProviderRclone)
	if err != nil || gotStrategy.Kind() != backupasset.ProviderRclone {
		t.Fatalf("Rclone strategy=%T err=%v", gotStrategy, err)
	}
	prepared, err := gotStrategy.Prepare(context.Background(), PublicationPrepareRequest{
		Attempt:     NewRclonePublicationAttempt(request.Attempt),
		RcloneInput: &RclonePublicationInput{ManifestLimits: request.ManifestOptions.Limits, PortableRequest: &request},
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	result, err := gotStrategy.Execute(context.Background(), prepared, PublicationProgress{})
	if err != nil || result.ProviderCommit == nil || result.Completion != backupasset.CompletionKnownExitZero || result.ExitCode != 0 {
		t.Fatalf("execute result=%+v err=%v", result, err)
	}
	commit, err := gotStrategy.RecordCommit(context.Background(), prepared, result)
	if err != nil || commit.Provider != backupasset.ProviderRclone {
		t.Fatalf("record commit=%+v err=%v", commit, err)
	}
	manifest, err := gotStrategy.VerifyOrBuildManifest(context.Background(), prepared, commit, request.ManifestOptions.Limits)
	if err != nil || manifest.Rclone == nil || manifest.Rclone.ManifestIndexDigest != request.Manifest.IndexDigest {
		t.Fatalf("manifest=%+v err=%v", manifest, err)
	}
	if _, err := gotStrategy.Prepare(context.Background(), PublicationPrepareRequest{Attempt: NewResticPublicationAttempt(ResticAttemptV1{})}); err == nil {
		t.Fatal("Rclone strategy accepted a Restic attempt")
	}
}

func TestRclonePublicationStrategyBuildsInitialManifestFromExecutorDerivedSource(t *testing.T) {
	request := portablePublicationRequestForTest(t, false)
	wantManifest := request.Manifest
	request.Manifest = RcloneManifestBundle{}
	remote := &fakeRclonePortableRemote{observations: []RcloneManifestBundle{wantManifest}}
	strategy, err := NewRclonePublicationStrategy(
		NewRclonePortablePublisher(remote, func(time.Duration) {}, portableNowForTest),
		NewRcloneNativePublisher(&rcloneNativeDataPlaneFake{}, portableNowForTest),
	)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := strategy.Prepare(context.Background(), PublicationPrepareRequest{
		Attempt: NewRclonePublicationAttempt(request.Attempt),
		RcloneInput: &RclonePublicationInput{
			ManifestLimits: request.ManifestOptions.Limits, PortableRequest: &request,
		},
	})
	if err != nil || prepared.RcloneInput == nil || prepared.RcloneInput.PortableRequest == nil ||
		!equalRcloneManifestBundleIdentity(prepared.RcloneInput.PortableRequest.Manifest, wantManifest) {
		t.Fatalf("prepared initial manifest=%+v err=%v", prepared.RcloneInput, err)
	}
	if remote.observationAt != 1 {
		t.Fatalf("initial source observation count=%d", remote.observationAt)
	}
}

func TestRclonePublicationStrategyPreparesOneClosedNativeLane(t *testing.T) {
	portableRequest := portablePublicationRequestForTest(t, false)
	portable := NewRclonePortablePublisher(&fakeRclonePortableRemote{}, func(time.Duration) {}, portableNowForTest)
	native := NewRcloneNativePublisher(&rcloneNativeDataPlaneFake{}, portableNowForTest)
	strategy, err := NewRclonePublicationStrategy(portable, native)
	if err != nil {
		t.Fatal(err)
	}
	attempt := validRcloneAttemptForTest(backupasset.PublicationNativeObjectVersions)
	nativeRequest := &RcloneNativePublicationRequest{Attempt: attempt}
	prepared, err := strategy.Prepare(context.Background(), PublicationPrepareRequest{
		Attempt: NewRclonePublicationAttempt(attempt),
		RcloneInput: &RclonePublicationInput{
			ManifestLimits: portableRequest.ManifestOptions.Limits,
			NativeRequest:  nativeRequest,
		},
	})
	if err != nil || prepared.RcloneInput == nil || prepared.RcloneInput.NativeRequest == nil || prepared.RcloneInput.PortableRequest != nil {
		t.Fatalf("native prepare=%+v err=%v", prepared, err)
	}
	if prepared.RcloneInput.NativeRequest == nativeRequest {
		t.Fatal("native publication request was not defensively copied")
	}
	mixed := *prepared.RcloneInput
	mixed.PortableRequest = &portableRequest
	if _, err := strategy.Prepare(context.Background(), PublicationPrepareRequest{
		Attempt: NewRclonePublicationAttempt(attempt), RcloneInput: &mixed,
	}); err == nil {
		t.Fatal("strategy accepted mixed portable/native input")
	}
}

func containsString(values []string, want string) bool { return indexString(values, want) >= 0 }

func indexString(values []string, want string) int {
	for index, value := range values {
		if value == want {
			return index
		}
	}
	return -1
}
