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
)

type recordingPortableReadRemote struct {
	*fakeRclonePortableRemote
	requests []RclonePortablePublicationRequest
	locators []RclonePrivateLocator
}

func (remote *recordingPortableReadRemote) ReadControl(ctx context.Context, request RclonePortablePublicationRequest, name string, maxBytes int64) ([]byte, error) {
	remote.requests = append(remote.requests, request)
	return remote.fakeRclonePortableRemote.ReadControl(ctx, request, name, maxBytes)
}

func (remote *recordingPortableReadRemote) Observe(ctx context.Context, request RclonePortablePublicationRequest, locator RclonePrivateLocator) (RcloneManifestBundle, error) {
	remote.requests = append(remote.requests, request)
	remote.locators = append(remote.locators, locator)
	return remote.fakeRclonePortableRemote.Observe(ctx, request, locator)
}

func TestRclonePortableContentResolverV2BindsSourceAndDestination(t *testing.T) {
	request, destination := portableV2PublicationFixture(t)
	past := time.Now().UTC().Add(-2 * time.Hour)
	request.Attempt.CaptureStartedAt = past
	request.Attempt.PreparedAt = past.Add(time.Minute)
	request.Attempt.PointDeadlineAt = past.Add(20 * time.Minute)
	originalDeadline := request.Attempt.PointDeadlineAt
	fake := &fakeRclonePortableRemote{
		presence:      RcloneAttemptAbsent,
		observations:  []RcloneManifestBundle{request.Manifest, request.Manifest, destination, destination, request.Manifest, destination},
		fullByteProof: RclonePortableByteVerification{Complete: true, VerifiedBytes: 5},
	}
	publisher := NewRclonePortablePublisher(fake, func(time.Duration) {}, func() time.Time { return past.Add(2 * time.Minute) })
	commit, err := publisher.Publish(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	beforeExpiredPublish := len(fake.mutations)
	expiredPublisher := NewRclonePortablePublisher(fake, nil, func() time.Time { return time.Now().UTC() })
	if _, err := expiredPublisher.Publish(context.Background(), request); err == nil || len(fake.mutations) != beforeExpiredPublish {
		t.Fatalf("expired publication acquired write authority: err=%v", err)
	}
	fake.observations = append(fake.observations, destination, destination)
	remote := &recordingPortableReadRemote{fakeRclonePortableRemote: fake}
	strategy, err := NewRclonePublicationStrategy(NewRclonePortablePublisher(remote, nil, portableNowForTest), NewRcloneNativePublisher(&rcloneNativeDataPlaneFake{}, portableNowForTest))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	binding, err := strategy.ResolvePortableContent(ctx, request, commit, "file.txt")
	if err != nil {
		t.Fatalf("resolve committed V2 after original deadline: %v", err)
	}
	if binding.ManifestVersion != 2 || binding.Record.NormalizedPath != "file.txt" || binding.Record.ProviderLocator.Native != "portable:file.txt" ||
		binding.Record.ModifiedAt == nil || binding.Record.ModifiedAt.Format(time.RFC3339Nano) != "2026-07-16T01:00:01.123456789Z" ||
		binding.PhysicalModTime.Format(time.RFC3339Nano) != "2026-07-16T01:00:01Z" || binding.ValidateSnapshot == nil {
		t.Fatalf("unexpected V2 content binding: %+v", binding)
	}
	if err := binding.ValidateSnapshot(ctx); err != nil {
		t.Fatalf("stable recursive destination rejected: %v", err)
	}
	if len(remote.locators) != 1 || remote.locators[0] != request.DataRoot {
		t.Fatalf("recursive proof observed wrong locator: %+v", remote.locators)
	}
	callerDeadline, _ := ctx.Deadline()
	for _, read := range remote.requests {
		if read.ReadDeadline.IsZero() || read.ReadDeadline.After(callerDeadline) || !read.ReadDeadline.After(time.Now().UTC()) ||
			read.Source.valid() || read.CopyDest != nil || read.ParentLeaseEligible || !emptyRcloneManifestBundle(read.Manifest) ||
			!read.Attempt.PointDeadlineAt.Equal(originalDeadline) {
			t.Fatalf("invalid read-only request: %+v", read)
		}
	}
	if !request.Attempt.PointDeadlineAt.Equal(originalDeadline) || !request.ReadDeadline.IsZero() {
		t.Fatal("historical read modified the durable publication attempt")
	}
	fake.tamperReadback = "commit.json"
	if err := binding.ValidateSnapshot(ctx); err == nil {
		t.Fatal("changed authenticated marker was accepted")
	}
	fake.tamperReadback = ""
	changed := destination
	changed.ObservationDigest = strings.Repeat("d", 64)
	fake.observations[fake.observationAt] = changed
	if err := binding.ValidateSnapshot(ctx); !errors.Is(err, ErrRcloneDestinationUnstable) {
		t.Fatalf("recursive destination drift error=%v", err)
	}
	canceled, stop := context.WithCancel(context.Background())
	stop()
	if err := binding.ValidateSnapshot(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled historical proof error=%v", err)
	}
	if _, err := strategy.ResolvePortableContent(ctx, request, commit, "missing.txt"); err == nil {
		t.Fatal("missing content path was accepted")
	}
	changedCommit := commit
	changedCommit.Portable = cloneRclonePortableCommit(commit.Portable)
	changedCommit.Portable.DataIdentityDigest = strings.Repeat("a", 64)
	before := len(remote.requests)
	if _, err := strategy.ResolvePortableContent(ctx, request, changedCommit, "file.txt"); err == nil || len(remote.requests) != before {
		t.Fatalf("unauthenticated expected commit reached remote: err=%v", err)
	}
}

func TestRclonePortableContentResolverHistoricalV1UsesExactTime(t *testing.T) {
	fixture := portableV1FixedFixtureForTest(t)
	listing, err := os.ReadFile("testdata/rclone/portable-v1/source-lsjson.json")
	if err != nil {
		t.Fatal(err)
	}
	observation, err := BuildRcloneManifestV1(context.Background(), bytes.NewReader(listing), fixture.request.ManifestOptions)
	if err != nil {
		t.Fatal(err)
	}
	fixture.remote.observations = []RcloneManifestBundle{observation}
	strategy, err := NewRclonePublicationStrategy(NewRclonePortablePublisher(fixture.remote, nil, portableNowForTest), NewRcloneNativePublisher(&rcloneNativeDataPlaneFake{}, portableNowForTest))
	if err != nil {
		t.Fatal(err)
	}
	binding, err := strategy.ResolvePortableContent(context.Background(), fixture.request, fixture.commit, "dir/file.txt")
	if err != nil || binding.ManifestVersion != 1 || binding.Record.ModifiedAt == nil || !binding.PhysicalModTime.Equal(*binding.Record.ModifiedAt) {
		t.Fatalf("fixed V1 content binding=%+v err=%v", binding, err)
	}
	if err := binding.ValidateSnapshot(context.Background()); err != nil {
		t.Fatalf("fixed V1 destination proof: %v", err)
	}
	if _, err := strategy.ResolvePortableContent(context.Background(), fixture.request, fixture.commit, "dir"); err == nil {
		t.Fatal("directory accepted as content file")
	}
}

func TestRclonePortableReadDeadlineRejectsWritesAndScopesCommands(t *testing.T) {
	request := portablePublicationRequestForTest(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	read, err := portableReadRequest(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	commands := &fakeCommandTransport{openFunc: func(invocation CommandInvocation, _ int64) (ReadHandle, error) {
		if invocation.Operation == OperationRcloneManagedRecursiveList {
			return io.NopCloser(strings.NewReader(`[{"Path":"link.rclonelink","Name":"link.rclonelink","Size":12,"ModTime":"2026-07-16T01:00:01Z","IsDir":false}]`)), nil
		}
		if invocation.RcloneSource != nil && strings.HasSuffix(invocation.RcloneSource.value, "/link.rclonelink") {
			return io.NopCloser(strings.NewReader("dir/file.txt")), nil
		}
		return io.NopCloser(strings.NewReader("control")), nil
	}}
	remote, err := NewCommandRclonePortableRemote(commands, &fakeRcloneStagedPayloadTransport{}, func() (OperationLimits, error) { return testOperationLimits(), nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := remote.ReadControl(ctx, read, "commit.json", 32); err != nil {
		t.Fatal(err)
	}
	if _, err := remote.Observe(ctx, read, read.DataRoot); err != nil {
		t.Fatal(err)
	}
	if len(commands.requests) != 3 || commands.requests[2].Operation != OperationRcloneManagedCat ||
		commands.requests[2].RcloneSource == nil || commands.requests[2].RcloneSource.value != read.DataRoot.value+"/link.rclonelink" {
		t.Fatalf("read proof did not fetch the exact link target: %+v", commands.requests)
	}
	for _, invocation := range commands.requests {
		if invocation.Purpose != CommandPurposeRead || !invocation.AbsoluteDeadline.Equal(read.ReadDeadline) || invocation.Validate() != nil {
			t.Fatalf("historical command not bounded as a read: %+v", invocation)
		}
	}
	publicationInvocation := remote.invocation(request, OperationRcloneManagedCat, &request.ControlRoot, nil, nil, nil)
	if publicationInvocation.Purpose != CommandPurposePublish || !publicationInvocation.AbsoluteDeadline.Equal(request.Attempt.PointDeadlineAt) {
		t.Fatal("ordinary publication reconciliation inherited a historical read deadline")
	}
	before := len(commands.requests)
	if _, err := remote.ProbeAttempt(ctx, read); err == nil {
		t.Fatal("read request probed attempt")
	}
	if err := remote.PutControl(ctx, read, "commit.json", []byte("x")); err == nil {
		t.Fatal("read request staged a control")
	}
	if err := remote.TransferData(ctx, read, nil); err == nil {
		t.Fatal("read request transferred data")
	}
	if _, err := remote.VerifyFullBytes(ctx, read, 1); err == nil {
		t.Fatal("read request ran transfer verification")
	}
	if _, err := remote.Observe(ctx, read, request.Source); err == nil {
		t.Fatal("read request observed source")
	}
	outside := mustRclonePrivateLocatorForTest(t, "backup:elsewhere/file")
	if _, err := remote.readObject(ctx, read, outside, 32); err == nil {
		t.Fatal("read request accessed an unrelated object")
	}
	expired := read
	expired.ReadDeadline = time.Now().UTC().Add(-time.Second)
	if _, err := remote.ReadControl(ctx, expired, "commit.json", 32); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired read error=%v", err)
	}
	canceled, stop := context.WithCancel(context.Background())
	stop()
	if _, err := remote.Observe(canceled, read, read.DataRoot); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled read error=%v", err)
	}
	if len(commands.requests) != before {
		t.Fatal("forbidden or expired command reached transport")
	}

	fake := &fakeRclonePortableRemote{presence: RcloneAttemptAbsent}
	publisher := NewRclonePortablePublisher(fake, nil, portableNowForTest)
	strategy, err := NewRclonePublicationStrategy(publisher, NewRcloneNativePublisher(&rcloneNativeDataPlaneFake{}, portableNowForTest))
	if err != nil {
		t.Fatal(err)
	}
	input := &RclonePublicationInput{PortableRequest: &read}
	prepared := PreparedPublication{Attempt: NewRclonePublicationAttempt(read.Attempt), RcloneInput: input}
	if _, err := publisher.Publish(ctx, read); err == nil {
		t.Fatal("read request published")
	}
	if _, err := publisher.Reconcile(ctx, read); err == nil {
		t.Fatal("read request reconciled publication")
	}
	if _, err := strategy.Prepare(ctx, PublicationPrepareRequest{Attempt: prepared.Attempt, RcloneInput: input}); err == nil {
		t.Fatal("read request prepared publication")
	}
	if _, err := strategy.Execute(ctx, prepared, PublicationProgress{}); err == nil {
		t.Fatal("read request executed publication")
	}
	if _, err := strategy.RecordCommit(ctx, prepared, ProviderExecutionResult{}); err == nil {
		t.Fatal("read request recorded publication")
	}
	if _, err := strategy.VerifyOrBuildManifest(ctx, prepared, ProviderCommit{}, ManifestLimits{}); err == nil {
		t.Fatal("read request verified publication")
	}
	if _, err := strategy.Reconcile(ctx, PublicationReconcileRequest{Attempt: prepared.Attempt, RcloneInput: &RcloneReconcileInput{PortableRequest: &read}}); err == nil {
		t.Fatal("read request reconciled publication strategy")
	}
	if len(fake.mutations) != 0 || fake.observationAt != 0 || len(fake.reads) != 0 {
		t.Fatal("forbidden publication reached remote")
	}
}
