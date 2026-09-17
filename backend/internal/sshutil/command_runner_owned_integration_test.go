package sshutil

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// Exercise the production adapter and actual channel/transport close ordering.
func TestJoinedOwnedTransportRealSSH(t *testing.T) {
	for _, tc := range []struct {
		name    string
		output  string
		status  uint32
		limited bool
	}{
		{name: "exact", output: "1234"},
		{name: "nonzero", output: "1234", status: 7},
		{name: "limit", output: "12345", limited: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			listener, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = listener.Close() }()
			signer, err := ssh.ParsePrivateKey([]byte(nodeDialerTestPrivateKey(t)))
			if err != nil {
				t.Fatal(err)
			}
			config := &ssh.ServerConfig{NoClientAuth: true}
			config.AddHostKey(signer)
			serverDone := make(chan error, 1)
			go func() {
				serverDone <- func() error {
					conn, err := listener.Accept()
					if err != nil {
						return err
					}
					defer func() { _ = conn.Close() }()
					_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
					server, channels, requests, err := ssh.NewServerConn(conn, config)
					if err != nil {
						return err
					}
					defer func() { _ = server.Close() }()
					go ssh.DiscardRequests(requests)
					incoming, ok := <-channels
					if !ok {
						return fmt.Errorf("session missing")
					}
					channel, execRequests, err := incoming.Accept()
					if err != nil {
						return err
					}
					defer func() { _ = channel.Close() }()
					request, ok := <-execRequests
					if !ok || request.Type != "exec" {
						return fmt.Errorf("exec missing")
					}
					if err := request.Reply(true, nil); err != nil {
						return err
					}
					if _, err := io.WriteString(channel, tc.output); err != nil {
						return err
					}
					_, err = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{tc.status}))
					return err
				}()
			}()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			client, err := DialSSH(ctx, listener.Addr().String(), "FAKE_USER_FOR_TEST_ONLY", nil, ssh.FixedHostKey(signer.PublicKey()))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = client.Close() }()
			runner := NewSSHCommandRunnerWithJoinedTransportClose(client, 1)
			stream, err := runner.OpenRawExecution(ctx, RawCommandSpec{Command: "FAKE_COMMAND_FOR_TEST_ONLY", MaxStdoutBytes: 4})
			if err != nil {
				t.Fatal(err)
			}
			output, readErr := io.ReadAll(stream)
			completion, joinErr := stream.Join()
			if tc.limited {
				if !errors.Is(readErr, ErrCommandOutputLimit) || !errors.Is(joinErr, ErrCommandOutputLimit) {
					t.Fatalf("read=%v join=%v", readErr, joinErr)
				}
			} else if readErr != nil || joinErr != nil || string(output) != tc.output || !completion.ExitCodeKnown || completion.ExitCode != int(tc.status) {
				t.Fatalf("output=%q completion=%+v read=%v join=%v", output, completion, readErr, joinErr)
			}
			if err := <-serverDone; err != nil && !tc.limited {
				t.Fatal(err)
			}
		})
	}
}
