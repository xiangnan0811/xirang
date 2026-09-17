package nodelogs

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"xirang/backend/internal/model"
	"xirang/backend/internal/sshutil"
)

func TestSSHRunnerRejectsInvalidBoundsBeforeCredentials(t *testing.T) {
	for _, maximum := range []int{0, -1} {
		_, err := NewSSHRunner(nil).Run(context.Background(), model.Node{}, "FAKE_COMMAND_FOR_TEST_ONLY", time.Second, maximum)
		if err == nil || !strings.Contains(err.Error(), "invalid collection bounds") {
			t.Fatalf("max=%d: expected bounds failure before credentials, got %v", maximum, err)
		}
	}
}

type collectionSession struct {
	stdout  io.Reader
	waitErr error
}

func (*collectionSession) StdinPipe() (io.WriteCloser, error) { return nil, errors.New("unused") }
func (s *collectionSession) StdoutPipe() (io.Reader, error)   { return s.stdout, nil }
func (*collectionSession) StderrPipe() (io.Reader, error)     { return strings.NewReader(""), nil }
func (*collectionSession) Start(string) error                 { return nil }
func (s *collectionSession) Wait() error                      { return s.waitErr }
func (*collectionSession) Signal(ssh.Signal) error            { return nil }
func (*collectionSession) Close() error                       { return nil }

type failedCollectionReader struct{}

func (failedCollectionReader) Read([]byte) (int, error) {
	return 0, errors.New("FAKE_SECRET_OUTPUT_FOR_TEST_ONLY")
}

func TestSSHCollectionCompleteOutputOnly(t *testing.T) {
	for _, tc := range []struct {
		name        string
		stdout      io.Reader
		waitErr     error
		want        string
		wantLimit   bool
		wantFailure bool
	}{
		{name: "exact limit", stdout: strings.NewReader("1234"), want: "1234"},
		{name: "limit plus one", stdout: strings.NewReader("12345"), wantLimit: true, wantFailure: true},
		{name: "explicit exit", stdout: strings.NewReader("1234"), waitErr: &ssh.ExitError{}, want: "1234"},
		{name: "missing exit", stdout: strings.NewReader("1234"), waitErr: &ssh.ExitMissingError{}, wantFailure: true},
		{name: "read failure", stdout: io.MultiReader(strings.NewReader("12"), failedCollectionReader{}), wantFailure: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session := &collectionSession{stdout: tc.stdout, waitErr: tc.waitErr}
			runner := sshutil.NewCommandRunner(func(context.Context) (sshutil.CommandSession, error) { return session, nil }, 1)
			output, err := collectSSHOutput(context.Background(), runner, "FAKE_COMMAND_FOR_TEST_ONLY", 4)
			if output != tc.want || (err != nil) != tc.wantFailure || errors.Is(err, ErrOutputLimit) != tc.wantLimit {
				t.Fatalf("output=%q err=%v", output, err)
			}
			if err != nil && strings.Contains(err.Error(), "FAKE_") {
				t.Fatalf("raw evidence leaked: %v", err)
			}
		})
	}
}

func TestSSHRunnerRejectsCanceledContextBeforeCredentials(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewSSHRunner(nil).Run(ctx, model.Node{}, "FAKE_COMMAND_FOR_TEST_ONLY", time.Second, 10)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation before credentials, got %v", err)
	}
}
