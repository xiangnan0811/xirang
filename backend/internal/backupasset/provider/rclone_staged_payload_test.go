package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pkg/sftp"
)

func TestRcloneStagedPayloadRealSFTPVerifiesModesBytesDigestAndCleanup(t *testing.T) {
	home := t.TempDir()
	transport := newRcloneStagedPayloadTransportForTest(
		newPipeSFTPFactoryForTest(t, home, nil),
		[]byte("FAKE_STAGING_OWNERSHIP_KEY_32_BYTES_FOR_TEST_ONLY"),
		func() time.Time { return time.Date(2026, 7, 16, 1, 0, 0, 0, time.UTC) },
	)
	request := StagedPayloadRequest{
		AttemptID: strings.Repeat("a", 32), Name: "manifest-000001.ndjson", Payload: []byte("verified payload\n"), MaxBytes: 1024,
	}
	ref, err := transport.Stage(context.Background(), RemoteCommandAccess{}, request)
	if err != nil {
		t.Fatalf("stage payload: %v", err)
	}
	if ref.path == "" || ref.size != int64(len(request.Payload)) || ref.digest == "" || ref.attemptID != request.AttemptID || ref.name != request.Name {
		t.Fatalf("staged ref missing verified facts: %+v", ref)
	}
	info, err := os.Lstat(ref.path)
	if err != nil || info.Mode().Perm() != 0o600 || !info.Mode().IsRegular() {
		t.Fatalf("payload mode=%v err=%v", info, err)
	}
	for _, directory := range []string{filepath.Dir(filepath.Dir(ref.path)), filepath.Dir(ref.path)} {
		info, err := os.Lstat(directory)
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
			t.Fatalf("staging directory %s mode=%v err=%v", directory, info, err)
		}
	}
	got, err := os.ReadFile(ref.path)
	if err != nil || string(got) != string(request.Payload) {
		t.Fatalf("payload=%q err=%v", got, err)
	}
	if err := transport.Cleanup(context.Background(), RemoteCommandAccess{}, ref); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if _, err := os.Lstat(ref.path); !os.IsNotExist(err) {
		t.Fatalf("payload survived cleanup: %v", err)
	}
}

func TestRcloneStagedPayloadRejectsDuplicateTamperedOwnerAndRootReplacement(t *testing.T) {
	home := t.TempDir()
	transport := newRcloneStagedPayloadTransportForTest(
		newPipeSFTPFactoryForTest(t, home, nil),
		[]byte("FAKE_STAGING_OWNERSHIP_KEY_32_BYTES_FOR_TEST_ONLY"), time.Now,
	)
	request := StagedPayloadRequest{AttemptID: strings.Repeat("b", 32), Name: "commit.json", Payload: []byte("commit"), MaxBytes: 1024}
	ref, err := transport.Stage(context.Background(), RemoteCommandAccess{}, request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transport.Stage(context.Background(), RemoteCommandAccess{}, request); err == nil {
		t.Fatal("duplicate staged payload name was accepted")
	}
	if err := os.WriteFile(ref.ownerMarkerPath, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := transport.Cleanup(context.Background(), RemoteCommandAccess{}, ref); err == nil {
		t.Fatal("cleanup accepted a tampered ownership marker")
	}
	if _, err := os.Lstat(ref.path); err != nil {
		t.Fatalf("tampered-owner cleanup removed payload: %v", err)
	}

	other := t.TempDir()
	root := filepath.Join(home, ".xirang", "rclone-publication")
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, root); err != nil {
		t.Fatal(err)
	}
	request.AttemptID = strings.Repeat("c", 32)
	if _, err := transport.Stage(context.Background(), RemoteCommandAccess{}, request); err == nil {
		t.Fatal("symlink-replaced staging root was accepted")
	}
}

func TestRcloneStagedPayloadFailsClosedOnPartialWriteCloseFailureAndCancellation(t *testing.T) {
	for name, fault := range map[string]stagedFileFault{
		"partial write": {partialWrite: true},
		"close failure": {closeError: errors.New("FAKE_CLOSE_FAILURE_FOR_TEST_ONLY")},
	} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			transport := newRcloneStagedPayloadTransportForTest(
				newPipeSFTPFactoryForTest(t, home, &fault),
				[]byte("FAKE_STAGING_OWNERSHIP_KEY_32_BYTES_FOR_TEST_ONLY"), time.Now,
			)
			request := StagedPayloadRequest{AttemptID: strings.Repeat("d", 32), Name: "payload.json", Payload: []byte("complete payload"), MaxBytes: 1024}
			if _, err := transport.Stage(context.Background(), RemoteCommandAccess{}, request); err == nil {
				t.Fatal("faulted staged payload unexpectedly succeeded")
			}
			if matches, _ := filepath.Glob(filepath.Join(home, ".xirang", "rclone-publication", request.AttemptID, request.Name)); len(matches) != 0 {
				t.Fatalf("partial payload survived failure: %v", matches)
			}
		})
	}

	home := t.TempDir()
	transport := newRcloneStagedPayloadTransportForTest(newPipeSFTPFactoryForTest(t, home, nil), []byte("FAKE_STAGING_OWNERSHIP_KEY_32_BYTES_FOR_TEST_ONLY"), time.Now)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := transport.Stage(ctx, RemoteCommandAccess{}, StagedPayloadRequest{
		AttemptID: strings.Repeat("e", 32), Name: "payload.json", Payload: []byte("payload"), MaxBytes: 1024,
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled stage error=%v", err)
	}
}

func TestRcloneStagedPayloadProtectsActiveReferenceAndBoundsAgedCleanup(t *testing.T) {
	home := t.TempDir()
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	transport := newRcloneStagedPayloadTransportForTest(
		newPipeSFTPFactoryForTest(t, home, nil),
		[]byte("FAKE_STAGING_OWNERSHIP_KEY_32_BYTES_FOR_TEST_ONLY"), func() time.Time { return now },
	)
	stage := func(attempt string) StagedPayloadRef {
		t.Helper()
		ref, err := transport.Stage(context.Background(), RemoteCommandAccess{}, StagedPayloadRequest{
			AttemptID: attempt, Name: "payload.json", Payload: []byte(attempt), MaxBytes: 1024,
		})
		if err != nil {
			t.Fatal(err)
		}
		return ref
	}
	first := stage(strings.Repeat("1", 32))
	release, err := first.acquire()
	if err != nil {
		t.Fatal(err)
	}
	if err := transport.Cleanup(context.Background(), RemoteCommandAccess{}, first); err == nil {
		t.Fatal("active staged reference was cleaned")
	}
	release()
	if err := transport.Cleanup(context.Background(), RemoteCommandAccess{}, first); err != nil {
		t.Fatalf("released staged reference cleanup: %v", err)
	}

	second := stage(strings.Repeat("2", 32))
	third := stage(strings.Repeat("3", 32))
	old := now.Add(-48 * time.Hour)
	for _, ref := range []StagedPayloadRef{second, third} {
		if err := os.Chtimes(filepath.Dir(ref.path), old, old); err != nil {
			t.Fatal(err)
		}
	}
	unknown := filepath.Join(home, ".xirang", "rclone-publication", strings.Repeat("4", 32))
	if err := os.Mkdir(unknown, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(unknown, old, old); err != nil {
		t.Fatal(err)
	}
	if err := transport.CleanupAged(context.Background(), RemoteCommandAccess{}, 24*time.Hour, 3); err != nil {
		t.Fatalf("bounded aged cleanup: %v", err)
	}
	removed := 0
	for _, ref := range []StagedPayloadRef{second, third} {
		if _, err := os.Lstat(ref.path); os.IsNotExist(err) {
			removed++
		}
	}
	if removed != 2 {
		t.Fatalf("aged cleanup removed %d owned attempts, want exactly 2", removed)
	}
	if _, err := os.Lstat(unknown); err != nil {
		t.Fatalf("unknown attempt directory was removed: %v", err)
	}
}

func TestRcloneStagedPayloadRejectsAgedCleanupScanOverflow(t *testing.T) {
	home := t.TempDir()
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	transport := newRcloneStagedPayloadTransportForTest(
		newPipeSFTPFactoryForTest(t, home, nil),
		[]byte("FAKE_STAGING_OWNERSHIP_KEY_32_BYTES_FOR_TEST_ONLY"), func() time.Time { return now },
	)
	for index := 0; index < 2; index++ {
		attemptID := fmt.Sprintf("%032d", index+10)
		ref, err := transport.Stage(context.Background(), RemoteCommandAccess{}, StagedPayloadRequest{
			AttemptID: attemptID, Name: "payload.json", Payload: []byte("payload"), MaxBytes: 1024,
		})
		if err != nil {
			t.Fatal(err)
		}
		old := now.Add(-48 * time.Hour)
		if err := os.Chtimes(filepath.Dir(ref.path), old, old); err != nil {
			t.Fatal(err)
		}
	}
	if err := transport.CleanupAged(context.Background(), RemoteCommandAccess{}, 24*time.Hour, 1); !errors.Is(err, ErrRcloneStagedPayloadScanLimitExceeded) {
		t.Fatalf("scan overflow error=%v", err)
	}
	matches, err := filepath.Glob(filepath.Join(home, ".xirang", "rclone-publication", "*", "payload.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 2 {
		t.Fatalf("scan overflow removed staged payloads: %v", matches)
	}
}

type stagedFileFault struct {
	partialWrite bool
	closeError   error
}

type faultingStagedSession struct {
	stagedPayloadSession
	fault *stagedFileFault
}

func (session *faultingStagedSession) OpenFile(path string, flags int) (stagedPayloadFile, error) {
	file, err := session.stagedPayloadSession.OpenFile(path, flags)
	if err != nil || session.fault == nil || filepath.Base(path) == stagedPayloadOwnerMarkerName {
		return file, err
	}
	return &faultingStagedFile{stagedPayloadFile: file, fault: *session.fault}, nil
}

type faultingStagedFile struct {
	stagedPayloadFile
	fault stagedFileFault
	once  sync.Once
}

func (file *faultingStagedFile) Write(payload []byte) (int, error) {
	if file.fault.partialWrite {
		written := 0
		file.once.Do(func() {
			if len(payload) > 1 {
				written, _ = file.stagedPayloadFile.Write(payload[:len(payload)/2])
			}
		})
		return written, io.ErrShortWrite
	}
	return file.stagedPayloadFile.Write(payload)
}

func (file *faultingStagedFile) Close() error {
	err := file.stagedPayloadFile.Close()
	if file.fault.closeError != nil {
		return file.fault.closeError
	}
	return err
}

func newPipeSFTPFactoryForTest(t *testing.T, home string, fault *stagedFileFault) stagedPayloadSessionFactory {
	t.Helper()
	return func(context.Context, RemoteCommandAccess) (stagedPayloadSession, error) {
		clientConnection, serverConnection := net.Pipe()
		server, err := sftp.NewServer(serverConnection, sftp.WithServerWorkingDirectory(home))
		if err != nil {
			return nil, err
		}
		serveDone := make(chan struct{})
		go func() {
			_ = server.Serve()
			close(serveDone)
		}()
		client, err := sftp.NewClientPipe(clientConnection, clientConnection)
		if err != nil {
			_ = server.Close()
			return nil, err
		}
		session := stagedPayloadSession(&pipeSFTPSessionForTest{stagedPayloadSession: &sftpStagedPayloadSession{client: client}, server: server, done: serveDone})
		if fault != nil {
			session = &faultingStagedSession{stagedPayloadSession: session, fault: fault}
		}
		return session, nil
	}
}

type pipeSFTPSessionForTest struct {
	stagedPayloadSession
	server *sftp.Server
	done   <-chan struct{}
}

func (session *pipeSFTPSessionForTest) Close() error {
	clientErr := session.stagedPayloadSession.Close()
	serverErr := session.server.Close()
	select {
	case <-session.done:
	case <-time.After(time.Second):
	}
	if clientErr != nil {
		return clientErr
	}
	return serverErr
}
