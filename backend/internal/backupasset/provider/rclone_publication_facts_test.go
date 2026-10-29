package provider

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/sshutil"
)

type rcloneFailureRemote struct {
	*fakeRclonePortableRemote
	transferErr error
}

func (remote *rcloneFailureRemote) TransferData(ctx context.Context, request RclonePortablePublicationRequest, copyDest *RclonePrivateLocator) error {
	remote.mutations = append(remote.mutations, "data")
	return remote.transferErr
}

func TestRclonePublisherCarriesWholeAttemptFacts(t *testing.T) {
	request := portablePublicationRequestForTest(t, false)
	cause := errors.New("private source path must not be printed")
	cases := []struct {
		name                           string
		remote                         *rcloneFailureRemote
		unattempted, nonzero, unproven bool
		quiesced                       *bool
	}{
		{"read only preflight", &rcloneFailureRemote{fakeRclonePortableRemote: &fakeRclonePortableRemote{presence: RcloneAttemptPresent}}, true, false, false, new(true)},
		{"real positive exit", &rcloneFailureRemote{fakeRclonePortableRemote: &fakeRclonePortableRemote{presence: RcloneAttemptAbsent, observations: []RcloneManifestBundle{request.Manifest}}, transferErr: &rcloneMutationCommandError{attempted: true, quiesced: true, positiveExit: 9, cause: cause}}, false, true, false, new(true)},
		{"stopped without status", &rcloneFailureRemote{fakeRclonePortableRemote: &fakeRclonePortableRemote{presence: RcloneAttemptAbsent, observations: []RcloneManifestBundle{request.Manifest}}, transferErr: &rcloneMutationCommandError{attempted: true, quiesced: true, cause: context.Canceled}}, false, false, true, new(true)},
		{"unmarked transport failure", &rcloneFailureRemote{fakeRclonePortableRemote: &fakeRclonePortableRemote{presence: RcloneAttemptAbsent, observations: []RcloneManifestBundle{request.Manifest}}, transferErr: cause}, false, false, false, new(false)},
		{"read only failure after completed writes", &rcloneFailureRemote{fakeRclonePortableRemote: &fakeRclonePortableRemote{presence: RcloneAttemptAbsent, observations: []RcloneManifestBundle{request.Manifest}}}, false, false, true, new(true)},
		{"commit readback", &rcloneFailureRemote{fakeRclonePortableRemote: &fakeRclonePortableRemote{presence: RcloneAttemptAbsent, tamperReadback: "commit.json", observations: []RcloneManifestBundle{request.Manifest, request.Manifest, request.Manifest, request.Manifest}}}, false, false, false, new(true)},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			publisher := NewRclonePortablePublisher(test.remote, func(_ time.Duration) {}, portableNowForTest)
			_, err := publisher.Publish(context.Background(), request)
			if err == nil || strings.Contains(err.Error(), "private source path") {
				t.Fatalf("unsafe or missing publication error: %v", err)
			}
			fact := ClassifyRclonePublicationFailure(err)
			if fact.Unattempted != test.unattempted || fact.KnownNonzero != test.nonzero || fact.CompletionUnproven != test.unproven ||
				fact.WriterQuiesced == nil || *fact.WriterQuiesced != *test.quiesced {
				t.Fatalf("publication facts=%+v", fact)
			}
			if test.nonzero && fact.ExitCode != 9 {
				t.Fatalf("real exit status=%d", fact.ExitCode)
			}
			if test.name == "commit readback" && !errors.Is(err, ErrRcloneCommitOutcomeUnknown) {
				t.Fatal("commit readback cause was lost")
			}
		})
	}
}

func TestRcloneCommandFailureKeepsExitAcrossCleanupError(t *testing.T) {
	for _, exited := range []bool{false, true} {
		t.Run(map[bool]string{false: "unknown", true: "exited"}[exited], func(t *testing.T) {
			home := configTransportHome(t)
			cleanupErr := errors.New("private cleanup failure")
			base := configStoreForTest(t, home, nil).factory
			calls := 0
			factory := func(ctx context.Context, access RemoteCommandAccess, purpose string) (stagedPayloadSession, error) {
				calls++
				if calls == 2 {
					return nil, cleanupErr
				}
				return base(ctx, access, purpose)
			}
			runner := &configTransportRunner{openExecution: func(context.Context, sshutil.CommandSpec) (sshutil.CommandExecutionStream, error) {
				stream := newConfigTransportExecution()
				stream.reader = strings.NewReader("")
				stream.completion = sshutil.CommandCompletion{RemoteExited: exited, ExitCodeKnown: exited, ExitCode: 23}
				stream.joinErr = io.ErrUnexpectedEOF
				return stream, nil
			}}
			transport := configTransportForTest(t, home, runner, factory)
			_, err := transport.Run(context.Background(), configTransportInvocation(false, []byte("[remote]\ntype = local\n")), testOperationLimits())
			var fact *rcloneMutationCommandError
			if !errors.As(err, &fact) || !fact.attempted || fact.quiesced != exited || !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("command exit facts unavailable: %v", err)
			}
			if exited && (fact.positiveExit != 23 || !errors.Is(err, cleanupErr)) {
				t.Fatal("cleanup failure discarded confirmed exit")
			}
			if !exited && fact.positiveExit != 0 {
				t.Fatal("unknown remote exit acquired a status")
			}
		})
	}
}
