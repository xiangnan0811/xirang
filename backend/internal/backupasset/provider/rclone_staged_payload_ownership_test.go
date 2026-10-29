package provider

import (
	"context"
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

	"xirang/backend/internal/backupasset"

	"github.com/pkg/sftp"
)

// stallReplyConn lets the real SFTP server read a request but prevents its
// response from reaching the client until the test releases it.
type stallReplyConn struct {
	net.Conn
	stall   atomic.Bool
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (conn *stallReplyConn) Write(p []byte) (int, error) {
	if conn.stall.Load() {
		conn.once.Do(func() { close(conn.started) })
		<-conn.release
	}
	return conn.Conn.Write(p)
}

func awaitStagingSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for staging operation")
	}
}

func TestRcloneStagedPayloadSFTPCancellationInterruptsHandshake(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := newSFTPStagedPayloadSession(ctx, client, func() (*sftp.Client, error) {
			return sftp.NewClientPipe(client, client)
		})
		done <- err
	}()
	// Consume the init packet so the client is stalled awaiting the version.
	var header [9]byte
	if _, err := io.ReadFull(server, header[:]); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("handshake error=%v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancel did not interrupt SFTP handshake")
	}
}

type stagingCloseFaultConn struct {
	net.Conn
	err error
}

func (conn *stagingCloseFaultConn) Close() error {
	return errors.Join(conn.Conn.Close(), conn.err)
}

func TestRcloneStagedPayloadSFTPOpenFailurePreservesErrorsSafely(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	privatePath := filepath.Join(t.TempDir(), "private")
	openErr := errors.New("open failed at " + privatePath)
	closeErr := errors.New("close failed at " + privatePath)
	session, err := newSFTPStagedPayloadSession(context.Background(), &stagingCloseFaultConn{Conn: client, err: closeErr}, func() (*sftp.Client, error) {
		return nil, openErr
	})
	if session != nil || !errors.Is(err, openErr) || !errors.Is(err, closeErr) || !errors.Is(err, backupasset.ErrProviderUnavailable) {
		t.Fatalf("open failure lost its typed causes: %v", err)
	}
	if strings.Contains(err.Error(), privatePath) || strings.Contains(err.Error(), openErr.Error()) || strings.Contains(err.Error(), closeErr.Error()) {
		t.Fatalf("open failure exposed private detail: %v", err)
	}
	if _, err := server.Write([]byte("closed")); err == nil {
		t.Fatal("failed open did not close its owned connection")
	}
}

func TestRcloneStagedPayloadSFTPCancellationInterruptsWriteAndRemove(t *testing.T) {
	for _, operation := range []string{"write", "remove"} {
		t.Run(operation, func(t *testing.T) {
			home := t.TempDir()
			filePath := filepath.Join(home, "payload")
			if err := os.WriteFile(filePath, []byte("existing"), 0o600); err != nil {
				t.Fatal(err)
			}
			clientConn, serverConn := net.Pipe()
			gate := &stallReplyConn{Conn: serverConn, started: make(chan struct{}), release: make(chan struct{})}
			server, err := sftp.NewServer(gate, sftp.WithServerWorkingDirectory(home))
			if err != nil {
				t.Fatal(err)
			}
			serverDone := make(chan struct{})
			go func() { _ = server.Serve(); close(serverDone) }()
			ctx, cancel := context.WithCancel(context.Background())
			session, err := newSFTPStagedPayloadSession(ctx, clientConn, func() (*sftp.Client, error) {
				return sftp.NewClientPipe(clientConn, clientConn)
			})
			if err != nil {
				t.Fatal(err)
			}
			var file stagedPayloadFile
			if operation == "write" {
				file, err = session.OpenFile(filePath, os.O_WRONLY)
				if err != nil {
					t.Fatal(err)
				}
			}
			gate.stall.Store(true)
			done := make(chan error, 1)
			go func() {
				if operation == "write" {
					_, err := file.Write([]byte("replacement"))
					done <- err
				} else {
					done <- session.Remove(filePath)
				}
			}()
			awaitStagingSignal(t, gate.started)
			cancel()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("stalled operation succeeded after cancellation")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("cancellation did not interrupt stalled SFTP operation")
			}
			closed := make(chan struct{})
			go func() { _ = session.Close(); close(closed) }()
			awaitStagingSignal(t, closed)
			close(gate.release)
			_ = server.Close()
			awaitStagingSignal(t, serverDone)
		})
	}
}

type blockingStagedCloser struct {
	entered chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (closer *blockingStagedCloser) Close() error {
	closer.calls.Add(1)
	close(closer.entered)
	<-closer.release
	return nil
}

func TestRcloneStagedPayloadCloseJoinsCancellationCallback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	connection := &blockingStagedCloser{entered: make(chan struct{}), release: make(chan struct{})}
	session, err := newSFTPStagedPayloadSession(ctx, connection, func() (*sftp.Client, error) { return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	awaitStagingSignal(t, connection.entered)
	done := make(chan struct{})
	go func() { _ = session.Close(); close(done) }()
	select {
	case <-done:
		t.Fatal("close did not join cancellation callback")
	default:
	}
	close(connection.release)
	awaitStagingSignal(t, done)
	if err := session.Close(); err != nil || connection.calls.Load() != 1 {
		t.Fatalf("idempotent close: calls=%d err=%v", connection.calls.Load(), err)
	}
}

type stageOwnershipFaultSession struct {
	stagedPayloadSession
	payload       string
	verifyFailure bool
	beforeClose   func()
	closeErr      error
	root          string
	rootChecks    int
	onRootDrift   func()
}

func (session *stageOwnershipFaultSession) RealPath(name string) (string, error) {
	if session.onRootDrift != nil && name == session.root {
		session.rootChecks++
		if session.rootChecks == 2 {
			session.onRootDrift()
		}
	}
	return session.stagedPayloadSession.RealPath(name)
}
func (session *stageOwnershipFaultSession) Open(name string) (stagedPayloadFile, error) {
	if session.verifyFailure && filepath.Base(name) == session.payload {
		if err := os.WriteFile(name, []byte("tampered"), 0o600); err != nil {
			return nil, err
		}
	}
	return session.stagedPayloadSession.Open(name)
}
func (session *stageOwnershipFaultSession) Close() error {
	err := session.stagedPayloadSession.Close()
	if session.beforeClose != nil {
		session.beforeClose()
	}
	return errors.Join(err, session.closeErr)
}

func TestRcloneStagedPayloadFailedStageCleansOnlyCreatedPayload(t *testing.T) {
	for _, scenario := range []string{"write", "verify", "session-close", "marker-drift", "root-drift", "root-drift-during", "payload-symlink"} {
		t.Run(scenario, func(t *testing.T) {
			home := t.TempDir()
			base := newPipeSFTPFactoryForTest(t, home, nil)
			attempt := strings.Repeat("a", 32)
			directory := filepath.Join(home, stagedPayloadRootDirectory, attempt)
			request := StagedPayloadRequest{AttemptID: attempt, Name: "new.json", Payload: []byte("original payload"), MaxBytes: 1024}
			closeFault := errors.New("private close detail at " + home)
			var calls int
			var rootSession *stageOwnershipFaultSession
			factory := func(ctx context.Context, access RemoteCommandAccess) (stagedPayloadSession, error) {
				calls++
				session, err := base(ctx, access)
				if err != nil || calls != 1 {
					return session, err
				}
				wrapped := &stageOwnershipFaultSession{stagedPayloadSession: session, payload: request.Name, verifyFailure: scenario == "verify"}
				if scenario == "root-drift-during" {
					rootSession = wrapped
					wrapped.root = filepath.Dir(directory)
					wrapped.onRootDrift = func() {
						if err := os.Rename(wrapped.root, wrapped.root+"-old"); err != nil {
							t.Error(err)
							return
						}
						if err := os.Symlink(wrapped.root+"-old", wrapped.root); err != nil {
							t.Error(err)
						}
					}
				}
				if scenario == "write" {
					wrapped.stagedPayloadSession = &faultingStagedSession{stagedPayloadSession: session, fault: &stagedFileFault{partialWrite: true}}
				}
				if scenario == "session-close" {
					wrapped.closeErr = closeFault
				}
				if scenario == "marker-drift" || scenario == "root-drift" || scenario == "payload-symlink" {
					wrapped.closeErr = closeFault
					wrapped.beforeClose = func() {
						switch scenario {
						case "marker-drift":
							if err := os.WriteFile(filepath.Join(directory, stagedPayloadOwnerMarkerName), []byte("wrong"), 0o600); err != nil {
								t.Error(err)
							}
						case "root-drift":
							root := filepath.Dir(directory)
							if err := os.Rename(root, root+"-old"); err != nil {
								t.Error(err)
								return
							}
							if err := os.Symlink(root+"-old", root); err != nil {
								t.Error(err)
							}
						case "payload-symlink":
							payload := filepath.Join(directory, request.Name)
							if err := os.Remove(payload); err != nil {
								t.Error(err)
								return
							}
							if err := os.Symlink("sibling.json", payload); err != nil {
								t.Error(err)
							}
						}
					}
				}
				return wrapped, nil
			}
			transport := newRcloneStagedPayloadTransportForTest(factory, []byte("FAKE_STAGING_OWNERSHIP_KEY_32_BYTES_FOR_TEST_ONLY"), time.Now)
			// Create a valid sibling using a separate transport before fault injection.
			seed := newRcloneStagedPayloadTransportForTest(base, []byte("FAKE_STAGING_OWNERSHIP_KEY_32_BYTES_FOR_TEST_ONLY"), time.Now)
			sibling, err := seed.Stage(context.Background(), RemoteCommandAccess{}, StagedPayloadRequest{AttemptID: attempt, Name: "sibling.json", Payload: []byte("sibling"), MaxBytes: 1024})
			if err != nil {
				t.Fatal(err)
			}
			_, err = transport.Stage(context.Background(), RemoteCommandAccess{}, request)
			if err == nil || calls != 2 {
				t.Fatalf("stage error=%v sessions=%d", err, calls)
			}
			if strings.Contains(err.Error(), home) || strings.Contains(err.Error(), closeFault.Error()) {
				t.Fatalf("private error detail exposed: %v", err)
			}
			if errors.Is(err, backupasset.ErrProviderUnavailable) {
				t.Fatalf("internal staging failure classified as provider unavailable: %v", err)
			}
			if (scenario == "session-close" || scenario == "marker-drift" || scenario == "root-drift" || scenario == "payload-symlink") && !errors.Is(err, closeFault) {
				t.Fatalf("close error missing: %v", err)
			}
			if (scenario == "verify" || scenario == "marker-drift" || scenario == "root-drift" || scenario == "root-drift-during" || scenario == "payload-symlink") && !errors.Is(err, backupasset.ErrInvalidState) {
				t.Fatalf("identity error missing: %v", err)
			}
			if scenario == "root-drift-during" && (rootSession == nil || rootSession.rootChecks != 2) {
				t.Fatalf("root drift did not occur at the final stability check")
			}
			siblingPath := sibling.path
			payloadPath := filepath.Join(directory, request.Name)
			if scenario == "root-drift" || scenario == "root-drift-during" {
				siblingPath = filepath.Join(filepath.Dir(directory)+"-old", attempt, "sibling.json")
				payloadPath = filepath.Join(filepath.Dir(directory)+"-old", attempt, request.Name)
			}
			if data, readErr := os.ReadFile(siblingPath); readErr != nil || string(data) != "sibling" {
				t.Fatalf("sibling lost: %v", readErr)
			}
			_, statErr := os.Lstat(payloadPath)
			unsafe := scenario == "marker-drift" || scenario == "root-drift" || scenario == "root-drift-during" || scenario == "payload-symlink"
			if unsafe && statErr != nil {
				t.Fatalf("uncertain payload removed: %v", statErr)
			}
			if !unsafe && !os.IsNotExist(statErr) {
				t.Fatalf("owned payload retained: %v", statErr)
			}
		})
	}
}

func TestRcloneStagedPayloadCollisionNeverCleansExistingFile(t *testing.T) {
	home := t.TempDir()
	base := newPipeSFTPFactoryForTest(t, home, nil)
	key := []byte("FAKE_STAGING_OWNERSHIP_KEY_32_BYTES_FOR_TEST_ONLY")
	seed := newRcloneStagedPayloadTransportForTest(base, key, time.Now)
	request := StagedPayloadRequest{AttemptID: strings.Repeat("c", 32), Name: "existing.json", Payload: []byte("existing"), MaxBytes: 1024}
	ref, err := seed.Stage(context.Background(), RemoteCommandAccess{}, request)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	transport := newRcloneStagedPayloadTransportForTest(func(ctx context.Context, access RemoteCommandAccess) (stagedPayloadSession, error) {
		calls++
		return base(ctx, access)
	}, key, time.Now)
	request.Payload = []byte("replacement")
	if _, err := transport.Stage(context.Background(), RemoteCommandAccess{}, request); err == nil {
		t.Fatal("exclusive collision accepted")
	}
	if calls != 1 {
		t.Fatalf("collision started cleanup session: %d", calls)
	}
	if data, err := os.ReadFile(ref.path); err != nil || string(data) != "existing" {
		t.Fatalf("collision damaged existing payload: %v", err)
	}
}

func TestRcloneStagedPayloadFailedStageJoinsCloseAndUsesDetachedCleanup(t *testing.T) {
	home := t.TempDir()
	base := newPipeSFTPFactoryForTest(t, home, nil)
	closeFault := errors.New("private close failure")
	var calls int
	ctx, cancel := context.WithCancel(context.Background())
	factory := func(factoryCtx context.Context, access RemoteCommandAccess) (stagedPayloadSession, error) {
		calls++
		if calls == 2 {
			if factoryCtx.Err() != nil {
				t.Error("cleanup inherited caller cancellation")
			}
			deadline, ok := factoryCtx.Deadline()
			if !ok || time.Until(deadline) > 10*time.Second || time.Until(deadline) <= 0 {
				t.Error("cleanup has no bounded deadline")
			}
		}
		session, err := base(factoryCtx, access)
		if err != nil || calls != 1 {
			return session, err
		}
		return &stageOwnershipFaultSession{
			stagedPayloadSession: &faultingStagedSession{stagedPayloadSession: session, fault: &stagedFileFault{partialWrite: true}},
			closeErr:             closeFault,
			beforeClose:          cancel,
		}, nil
	}
	transport := newRcloneStagedPayloadTransportForTest(factory, []byte("FAKE_STAGING_OWNERSHIP_KEY_32_BYTES_FOR_TEST_ONLY"), time.Now)
	request := StagedPayloadRequest{AttemptID: strings.Repeat("d", 32), Name: "payload.json", Payload: []byte("payload"), MaxBytes: 1024}
	_, err := transport.Stage(ctx, RemoteCommandAccess{}, request)
	if err == nil || !errors.Is(err, io.ErrShortWrite) || !errors.Is(err, closeFault) || !errors.Is(err, context.Canceled) || calls != 2 {
		t.Fatalf("missing joined error or cleanup session: %v (%d sessions)", err, calls)
	}
	if strings.Contains(err.Error(), closeFault.Error()) {
		t.Fatal("close details leaked")
	}
	directory := filepath.Join(home, stagedPayloadRootDirectory, request.AttemptID)
	if _, err := os.Lstat(directory); !os.IsNotExist(err) {
		t.Fatalf("empty attempt directory retained: %v", err)
	}
}

func TestRcloneStagedPayloadCleanupJoinsSessionCloseError(t *testing.T) {
	home := t.TempDir()
	base := newPipeSFTPFactoryForTest(t, home, nil)
	key := []byte("FAKE_STAGING_OWNERSHIP_KEY_32_BYTES_FOR_TEST_ONLY")
	seed := newRcloneStagedPayloadTransportForTest(base, key, time.Now)
	ref, err := seed.Stage(context.Background(), RemoteCommandAccess{}, StagedPayloadRequest{
		AttemptID: strings.Repeat("e", 32), Name: "payload.json", Payload: []byte("payload"), MaxBytes: 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ref.ownerMarkerPath, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	closeFault := errors.New("private close failure")
	transport := newRcloneStagedPayloadTransportForTest(func(ctx context.Context, access RemoteCommandAccess) (stagedPayloadSession, error) {
		session, err := base(ctx, access)
		if err != nil {
			return nil, err
		}
		return &stageOwnershipFaultSession{stagedPayloadSession: session, closeErr: closeFault}, nil
	}, key, time.Now)
	err = transport.Cleanup(context.Background(), RemoteCommandAccess{}, ref)
	if err == nil || !errors.Is(err, backupasset.ErrInvalidState) || !errors.Is(err, closeFault) || strings.Contains(err.Error(), closeFault.Error()) {
		t.Fatalf("cleanup did not retain safe close error: %v", err)
	}
	if _, err := os.Lstat(ref.path); err != nil {
		t.Fatalf("invalid marker removed payload: %v", err)
	}
}

func TestRcloneStagedPayloadAgedCleanupJoinsSessionCloseError(t *testing.T) {
	home := t.TempDir()
	base := newPipeSFTPFactoryForTest(t, home, nil)
	key := []byte("FAKE_STAGING_OWNERSHIP_KEY_32_BYTES_FOR_TEST_ONLY")
	seed := newRcloneStagedPayloadTransportForTest(base, key, time.Now)
	for _, attempt := range []string{strings.Repeat("1", 32), strings.Repeat("2", 32)} {
		if _, err := seed.Stage(context.Background(), RemoteCommandAccess{}, StagedPayloadRequest{
			AttemptID: attempt, Name: "payload.json", Payload: []byte("payload"), MaxBytes: 1024,
		}); err != nil {
			t.Fatal(err)
		}
	}
	closeFault := errors.New("private close failure")
	transport := newRcloneStagedPayloadTransportForTest(func(ctx context.Context, access RemoteCommandAccess) (stagedPayloadSession, error) {
		session, err := base(ctx, access)
		if err != nil {
			return nil, err
		}
		return &stageOwnershipFaultSession{stagedPayloadSession: session, closeErr: closeFault}, nil
	}, key, time.Now)
	err := transport.CleanupAged(context.Background(), RemoteCommandAccess{}, time.Hour, 1)
	if !errors.Is(err, ErrRcloneStagedPayloadScanLimitExceeded) || !errors.Is(err, closeFault) || strings.Contains(err.Error(), closeFault.Error()) {
		t.Fatalf("aged cleanup did not join safe close error: %v", err)
	}
}
