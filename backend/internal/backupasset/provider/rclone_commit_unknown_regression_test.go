package provider

import (
	"context"
	"errors"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
)

// This regression uses only the pre-change public contracts so it can also be
// exercised against the baseline without backporting implementation changes.
func TestRcloneCommitReadbackFailureIsUnknownToConsumer(t *testing.T) {
	request := portablePublicationRequestForTest(t, false)
	remote := &fakeRclonePortableRemote{presence: RcloneAttemptAbsent, tamperReadback: "commit.json",
		observations: []RcloneManifestBundle{request.Manifest, request.Manifest, request.Manifest, request.Manifest}}
	strategy, err := NewRclonePublicationStrategy(NewRclonePortablePublisher(remote, func(time.Duration) {}, portableNowForTest),
		NewRcloneNativePublisher(&rcloneNativeDataPlaneFake{}, portableNowForTest))
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := strategy.Prepare(context.Background(), PublicationPrepareRequest{Attempt: NewRclonePublicationAttempt(request.Attempt),
		RcloneInput: &RclonePublicationInput{ManifestLimits: request.ManifestOptions.Limits, PortableRequest: &request}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := strategy.Execute(context.Background(), prepared, PublicationProgress{})
	if !errors.Is(err, ErrRcloneCommitOutcomeUnknown) || result.Completion != backupasset.CompletionOutcomeUnknown || result.ExitCode != UnknownProviderExitCode {
		t.Fatalf("commit was written but failed readback was not classified unknown: result=%+v error=%v", result, err)
	}
	if len(remote.controls["commit.json"]) == 0 {
		t.Fatal("no commit was written")
	}
}
