package provider

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pkg/sftp"

	"xirang/backend/internal/backupasset"
)

// The first Remove completes; the second is sent to a real SFTP server whose
// response is withheld. The directory scanner remains the real bounded SFTP
// scanner from configStoreForTest.
type configFaultStalledSession struct {
	*rcloneConfigSession
	gate       *stallReplyConn
	removes    atomic.Int32
	closed     chan struct{}
	closeOnce  sync.Once
	server     *sftp.Server
	serverDone chan struct{}
	release    sync.Once
}

func (session *configFaultStalledSession) Remove(name string) error {
	if session.removes.Add(1) == 2 {
		session.gate.stall.Store(true)
	}
	return session.rcloneConfigSession.Remove(name)
}

func (session *configFaultStalledSession) Close() error {
	err := session.rcloneConfigSession.Close()
	session.closeOnce.Do(func() { close(session.closed) })
	return err
}

func (session *configFaultStalledSession) releaseServer() {
	session.release.Do(func() { close(session.gate.release) })
	_ = session.server.Close()
}

func configFaultStalledFactory(t *testing.T, home string, ready chan<- *configFaultStalledSession) rcloneCommandConfigSessionFactory {
	t.Helper()
	base := configStoreForTest(t, home, nil).factory
	return func(ctx context.Context, access RemoteCommandAccess, purpose string) (stagedPayloadSession, error) {
		// Reuse the independently owned bounded directory-subsystem fixture.
		template, err := base(ctx, access, purpose)
		if err != nil {
			return nil, err
		}
		directory, ok := template.(*rcloneConfigSession)
		if !ok {
			_ = template.Close()
			return nil, errors.New("test directory subsystem unavailable")
		}
		openDirectory := directory.openDirectory
		if err := template.Close(); err != nil {
			return nil, err
		}

		clientConn, serverConn := net.Pipe()
		gate := &stallReplyConn{Conn: serverConn, started: make(chan struct{}), release: make(chan struct{})}
		server, err := sftp.NewServer(gate, sftp.WithServerWorkingDirectory(home))
		if err != nil {
			_ = clientConn.Close()
			_ = serverConn.Close()
			return nil, err
		}
		serverDone := make(chan struct{})
		go func() { _ = server.Serve(); close(serverDone) }()
		underlying, err := newSFTPStagedPayloadSession(ctx, clientConn, func() (*sftp.Client, error) {
			return sftp.NewClientPipe(clientConn, clientConn)
		})
		if err != nil {
			_ = server.Close()
			<-serverDone
			return nil, err
		}
		wrapped := &configFaultStalledSession{
			rcloneConfigSession: &rcloneConfigSession{stagedPayloadSession: underlying, openDirectory: openDirectory},
			gate:                gate, closed: make(chan struct{}), server: server, serverDone: serverDone,
		}
		ready <- wrapped
		return wrapped, nil
	}
}

func TestRcloneCommandConfigCleanupInterruptedDuringRealSFTPRemove(t *testing.T) {
	for _, interruption := range []string{"cancel", "deadline"} {
		t.Run(interruption, func(t *testing.T) {
			home := t.TempDir()
			store := configStoreForTest(t, home, nil)
			invocation := configInvocationForTest()
			ref, err := store.prepare(context.Background(), invocation)
			if err != nil {
				t.Fatal(err)
			}
			sibling, err := store.prepare(context.Background(), invocation)
			if err != nil {
				t.Fatal(err)
			}
			files := []string{ref.path, ref.path + "123", ref.path + ".old456"}
			for _, name := range files[1:] {
				if err := os.WriteFile(name, []byte("backup"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			unknownDir := filepath.Join(filepath.Dir(filepath.Dir(ref.path)), "foreign-owner")
			if err := os.Mkdir(unknownDir, 0o700); err != nil {
				t.Fatal(err)
			}
			unknownFile := filepath.Join(unknownDir, "keep")
			if err := os.WriteFile(unknownFile, []byte("foreign content"), 0o600); err != nil {
				t.Fatal(err)
			}
			markerPath := filepath.Join(filepath.Dir(ref.path), ".owner-v1")
			marker, err := os.ReadFile(markerPath)
			if err != nil {
				t.Fatal(err)
			}

			ready := make(chan *configFaultStalledSession, 1)
			store.factory = configFaultStalledFactory(t, home, ready)
			var ctx context.Context
			var cancel context.CancelFunc
			if interruption == "deadline" {
				ctx, cancel = context.WithTimeout(context.Background(), 4*time.Second)
			} else {
				ctx, cancel = context.WithCancel(context.Background())
			}
			defer cancel()
			result := make(chan error, 1)
			go func() { result <- store.cleanup(ctx, invocation, ref) }()
			var session *configFaultStalledSession
			select {
			case session = <-ready:
			case err := <-result:
				t.Fatalf("cleanup failed before the SFTP session opened: %v", err)
			case <-time.After(3 * time.Second):
				t.Fatal("cleanup did not open the SFTP session")
			}
			defer session.releaseServer()
			awaitStagingSignal(t, session.gate.started)
			if session.removes.Load() != 2 {
				t.Fatalf("expected a completed remove before the stalled response: calls=%d", session.removes.Load())
			}
			if interruption == "cancel" {
				cancel()
			}
			var cleanupErr error
			select {
			case cleanupErr = <-result:
			case <-time.After(6 * time.Second):
				t.Fatal("interrupted cleanup did not return while the remote response was stalled")
			}
			want := context.Canceled
			if interruption == "deadline" {
				want = context.DeadlineExceeded
			}
			if !errors.Is(cleanupErr, want) || !errors.Is(cleanupErr, backupasset.ErrProviderUnavailable) {
				t.Fatalf("cleanup lost interruption or unavailable classification: %v", cleanupErr)
			}
			if strings.Contains(cleanupErr.Error(), ref.path) || strings.Contains(cleanupErr.Error(), string(invocation.SecretStdin)) || strings.Contains(cleanupErr.Error(), string(marker)) {
				t.Fatal("cleanup error disclosed private data")
			}
			select {
			case <-session.closed:
			default:
				t.Fatal("cleanup returned before the local SFTP owner was joined")
			}
			retained, removed := 0, 0
			for _, name := range files {
				_, err := os.Lstat(name)
				if err == nil {
					retained++
				} else if os.IsNotExist(err) {
					removed++
				} else {
					t.Fatal(err)
				}
			}
			if removed == 0 || retained == 0 {
				t.Fatalf("cleanup did not leave a partial owned directory: removed=%d retained=%d", removed, retained)
			}
			if actual, err := os.ReadFile(markerPath); err != nil || !bytes.Equal(actual, marker) {
				t.Fatal("partial cleanup removed or changed the ownership marker")
			}
			if actual, err := os.ReadFile(sibling.path); err != nil || !bytes.Equal(actual, invocation.SecretStdin) {
				t.Fatal("cleanup changed another command's config")
			}
			if actual, err := os.ReadFile(unknownFile); err != nil || string(actual) != "foreign content" {
				t.Fatal("cleanup changed an unknown sibling directory")
			}
			session.releaseServer()
			awaitStagingSignal(t, session.serverDone)
		})
	}
}

type configMarkerFaultSession struct {
	*rcloneConfigSession
	fault stagedFileFault
}

func (session *configMarkerFaultSession) OpenFile(name string, flags int) (stagedPayloadFile, error) {
	file, err := session.rcloneConfigSession.OpenFile(name, flags)
	if err != nil || filepath.Base(name) != ".owner-v1" {
		return file, err
	}
	return &faultingStagedFile{stagedPayloadFile: file, fault: session.fault}, nil
}

func TestRcloneCommandConfigMarkerFailurePreservesUnverifiedAndCleansVerified(t *testing.T) {
	closeErr := errors.New("FAKE_MARKER_CLOSE_FAILURE_FOR_TEST_ONLY")
	for name, fault := range map[string]stagedFileFault{
		"partial-write": {partialWrite: true},
		"close":         {closeError: closeErr},
	} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			store := configStoreForTest(t, home, nil)
			invocation := configInvocationForTest()
			sibling, err := store.prepare(context.Background(), invocation)
			if err != nil {
				t.Fatal(err)
			}
			root := filepath.Dir(filepath.Dir(sibling.path))
			unknownDir := filepath.Join(root, "foreign-owner")
			if err := os.Mkdir(unknownDir, 0o700); err != nil {
				t.Fatal(err)
			}
			unknownFile := filepath.Join(unknownDir, "keep")
			if err := os.WriteFile(unknownFile, []byte("foreign content"), 0o600); err != nil {
				t.Fatal(err)
			}
			seed := bytes.Repeat([]byte{0x41}, 48)
			store.random = bytes.NewReader(seed)
			operationID := hex.EncodeToString(seed[:16])
			createdDirectory := filepath.Join(root, operationID)
			base := store.factory
			var sessions atomic.Int32
			store.factory = func(ctx context.Context, access RemoteCommandAccess, purpose string) (stagedPayloadSession, error) {
				session, err := base(ctx, access, purpose)
				if err != nil || sessions.Add(1) != 1 {
					return session, err
				}
				return &configMarkerFaultSession{rcloneConfigSession: session.(*rcloneConfigSession), fault: fault}, nil
			}
			ref, err := store.prepare(context.Background(), invocation)
			want := error(io.ErrShortWrite)
			if name == "close" {
				want = closeErr
			}
			if ref != nil || !errors.Is(err, want) || !errors.Is(err, backupasset.ErrProviderUnavailable) {
				t.Fatalf("marker failure did not reach the caller: ref=%v error=%v", ref != nil, err)
			}
			if strings.Contains(err.Error(), home) || strings.Contains(err.Error(), string(invocation.SecretStdin)) {
				t.Fatal("marker failure disclosed private information")
			}
			if name == "close" {
				if _, err := os.Lstat(createdDirectory); !os.IsNotExist(err) {
					t.Fatalf("complete marker was not revalidated and cleaned: %v", err)
				}
			} else {
				marker, readErr := os.ReadFile(filepath.Join(createdDirectory, ".owner-v1"))
				canonicalHome := filepath.Dir(filepath.Dir(root))
				expectedMarker := []byte("xirang-rclone-command-config-v1\n" + hex.EncodeToString(seed[16:]) + "\n" + canonicalHome + "\n" + operationID + "\n")
				if readErr != nil || len(marker) == 0 || bytes.Equal(marker, expectedMarker) || !bytes.HasPrefix(expectedMarker, marker) {
					t.Fatal("unverified partial owner marker was not retained")
				}
				if _, statErr := os.Lstat(filepath.Join(createdDirectory, "rclone.conf")); !os.IsNotExist(statErr) {
					t.Fatalf("secret config was created without a verified owner: %v", statErr)
				}
			}
			if actual, err := os.ReadFile(sibling.path); err != nil || !bytes.Equal(actual, invocation.SecretStdin) {
				t.Fatal("marker failure changed another command's config")
			}
			if actual, err := os.ReadFile(unknownFile); err != nil || string(actual) != "foreign content" {
				t.Fatal("marker failure changed an unknown directory")
			}
		})
	}
}

func TestRcloneCommandConfigRejectsReplacedRootWithForeignOwner(t *testing.T) {
	home := t.TempDir()
	store := configStoreForTest(t, home, nil)
	invocation := configInvocationForTest()
	ref, err := store.prepare(context.Background(), invocation)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Dir(filepath.Dir(ref.path))
	oldRoot := root + "-original"
	if err := os.Rename(root, oldRoot); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	foreignDir := filepath.Join(root, filepath.Base(filepath.Dir(ref.path)))
	if err := os.Mkdir(foreignDir, 0o700); err != nil {
		t.Fatal(err)
	}
	foreignMarker := filepath.Join(foreignDir, ".owner-v1")
	foreignConfig := filepath.Join(foreignDir, "rclone.conf")
	if err := os.WriteFile(foreignMarker, []byte("foreign owner"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(foreignConfig, []byte("foreign config"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.cleanup(context.Background(), invocation, ref); err == nil || !errors.Is(err, backupasset.ErrProviderUnavailable) {
		t.Fatalf("replacement owner was accepted: %v", err)
	}
	originalConfig := filepath.Join(oldRoot, filepath.Base(filepath.Dir(ref.path)), "rclone.conf")
	if actual, err := os.ReadFile(originalConfig); err != nil || !bytes.Equal(actual, invocation.SecretStdin) {
		t.Fatal("replacement cleanup changed the original owner's config")
	}
	if actual, err := os.ReadFile(foreignMarker); err != nil || string(actual) != "foreign owner" {
		t.Fatal("replacement cleanup changed the foreign marker")
	}
	if actual, err := os.ReadFile(foreignConfig); err != nil || string(actual) != "foreign config" {
		t.Fatal("replacement cleanup changed the foreign config")
	}
}
