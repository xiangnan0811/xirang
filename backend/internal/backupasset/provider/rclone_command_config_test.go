package provider

import (
	"bytes"
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

	"github.com/pkg/sftp"

	"xirang/backend/internal/model"
)

func configStoreForTest(t *testing.T, home string, fault *stagedFileFault) *rcloneCommandConfigStore {
	t.Helper()
	factory := newPipeSFTPFactoryForTest(t, home, fault)
	return &rcloneCommandConfigStore{factory: func(ctx context.Context, access RemoteCommandAccess, _ string) (stagedPayloadSession, error) {
		session, err := factory(ctx, access)
		if err != nil {
			return nil, err
		}
		return &rcloneConfigSession{stagedPayloadSession: session, openDirectory: func() (io.ReadWriteCloser, error) {
			client, remote := net.Pipe()
			server, err := sftp.NewServer(remote, sftp.WithServerWorkingDirectory(home))
			if err != nil {
				_ = client.Close()
				_ = remote.Close()
				return nil, err
			}
			done := make(chan struct{})
			go func() { defer close(done); _ = server.Serve() }()
			wire := &configDirectoryWireForTest{Conn: client, server: server, done: done}
			wire.stop = context.AfterFunc(ctx, func() { _ = client.Close(); _ = server.Close() })
			return wire, nil
		}}, nil
	}}
}

type configDirectoryWireForTest struct {
	net.Conn
	server *sftp.Server
	done   <-chan struct{}
	stop   func() bool
	once   sync.Once
}

func (wire *configDirectoryWireForTest) Close() error {
	wire.once.Do(func() { wire.stop(); _ = wire.Conn.Close(); _ = wire.server.Close(); <-wire.done })
	return nil
}

func configInvocationForTest() CommandInvocation {
	return CommandInvocation{Tool: ToolRclone, Operation: OperationRcloneManagedVersion, Purpose: CommandPurposeProbe, Runtime: &RemoteCommandAccess{Node: model.Node{ID: 9}}, SecretStdin: []byte("[test]\ntype = local\n# FAKE_CONFIG_FOR_TEST_ONLY")}
}

func TestRcloneCommandConfigRealSFTPExactBytesRewriteAndIsolation(t *testing.T) {
	for _, ending := range []string{"", "\n"} {
		t.Run(map[bool]string{true: "newline", false: "no newline"}[ending != ""], func(t *testing.T) {
			home := t.TempDir()
			store := configStoreForTest(t, home, nil)
			inv := configInvocationForTest()
			inv.SecretStdin = append(inv.SecretStdin, ending...)
			first, err := store.prepare(context.Background(), inv)
			if err != nil {
				t.Fatal(err)
			}
			second, err := store.prepare(context.Background(), inv)
			if err != nil {
				t.Fatal(err)
			}
			if first.path == second.path {
				t.Fatal("commands share config ownership")
			}
			got, err := os.ReadFile(first.path)
			if err != nil || !bytes.Equal(got, inv.SecretStdin) {
				t.Fatal("config bytes changed during delivery")
			}
			for _, dir := range []string{filepath.Dir(first.path), filepath.Dir(filepath.Dir(first.path)), filepath.Join(home, ".xirang")} {
				info, err := os.Lstat(dir)
				if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
					t.Fatalf("private directory mode: %v", err)
				}
			}
			info, err := os.Lstat(first.path)
			if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
				t.Fatalf("private config mode: %v", err)
			}
			// Model v1.74.4 Save: same-directory temp, old backup, atomic replacement.
			replacement := first.path + "123456789"
			if err := os.WriteFile(replacement, []byte("rewritten config"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(first.path, first.path+".old987654321"); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(replacement, first.path); err != nil {
				t.Fatal(err)
			}
			if err := store.cleanup(context.Background(), inv, first); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Lstat(filepath.Dir(first.path)); !os.IsNotExist(err) {
				t.Fatalf("operation directory survives: %v", err)
			}
			got, err = os.ReadFile(second.path)
			if err != nil || !bytes.Equal(got, inv.SecretStdin) {
				t.Fatal("other command config was affected")
			}
			if err := store.cleanup(context.Background(), inv, second); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRcloneCommandConfigRejectsDriftBeforeAnyDeletion(t *testing.T) {
	mutations := map[string]func(*testing.T, string){
		"unknown": func(t *testing.T, p string) {
			if err := os.WriteFile(filepath.Join(filepath.Dir(p), "unknown"), []byte("keep"), 0600); err != nil {
				t.Fatal(err)
			}
		},
		"subdirectory": func(t *testing.T, p string) {
			if err := os.Mkdir(p+"123", 0700); err != nil {
				t.Fatal(err)
			}
		},
		"symlink": func(t *testing.T, p string) {
			if err := os.Symlink(p, p+"123"); err != nil {
				t.Fatal(err)
			}
		},
		"mode": func(t *testing.T, p string) {
			if err := os.Chmod(p, 0644); err != nil {
				t.Fatal(err)
			}
		},
		"owner": func(t *testing.T, p string) {
			if err := os.WriteFile(filepath.Join(filepath.Dir(p), ".owner-v1"), []byte("tampered"), 0600); err != nil {
				t.Fatal(err)
			}
		},
		"root mode": func(t *testing.T, p string) {
			if err := os.Chmod(filepath.Dir(filepath.Dir(p)), 0755); err != nil {
				t.Fatal(err)
			}
		},
		"scan limit": func(t *testing.T, p string) {
			for n := 1; n <= 127; n++ {
				name := p + strings.Repeat("0", n/10) + string(rune('0'+n%10))
				if err := os.WriteFile(name, []byte("keep"), 0600); err != nil {
					t.Fatal(err)
				}
			}
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			store := configStoreForTest(t, t.TempDir(), nil)
			inv := configInvocationForTest()
			ref, err := store.prepare(context.Background(), inv)
			if err != nil {
				t.Fatal(err)
			}
			mutate(t, ref.path)
			err = store.cleanup(context.Background(), inv, ref)
			if err == nil {
				t.Fatal("unsafe cleanup succeeded")
			}
			if strings.Contains(err.Error(), ref.path) || strings.Contains(err.Error(), string(inv.SecretStdin)) {
				t.Fatal("cleanup error disclosed private data")
			}
			if _, err := os.Lstat(ref.path); err != nil {
				t.Fatal("deleted config before full validation")
			}
			if _, err := os.Lstat(filepath.Join(filepath.Dir(ref.path), ".owner-v1")); err != nil {
				t.Fatal("deleted marker before full validation")
			}
		})
	}
}

func TestRcloneCommandConfigFailedWriteAndCloseCleanup(t *testing.T) {
	for name, fault := range map[string]stagedFileFault{"partial": {partialWrite: true}, "close": {closeError: errors.New("FAKE_CLOSE_FAILURE_FOR_TEST_ONLY")}} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			store := configStoreForTest(t, home, &fault)
			if _, err := store.prepare(context.Background(), configInvocationForTest()); err == nil {
				t.Fatal("failed write succeeded")
			}
			entries, err := os.ReadDir(filepath.Join(home, ".xirang", "rclone-command-config"))
			if err != nil || len(entries) != 0 {
				t.Fatalf("failed stage retained owned config: entries=%d err=%v", len(entries), err)
			}
		})
	}
}

func TestRcloneCommandConfigCollisionPreservesExistingOwner(t *testing.T) {
	home := t.TempDir()
	store := configStoreForTest(t, home, nil)
	seed := bytes.Repeat([]byte{0x41}, 48)
	store.random = bytes.NewReader(seed)
	inv := configInvocationForTest()
	ref, err := store.prepare(context.Background(), inv)
	if err != nil {
		t.Fatal(err)
	}
	store.random = bytes.NewReader(seed)
	if _, err := store.prepare(context.Background(), inv); err == nil {
		t.Fatal("operation collision accepted")
	}
	got, err := os.ReadFile(ref.path)
	if err != nil || !bytes.Equal(got, inv.SecretStdin) {
		t.Fatal("collision changed existing owner")
	}
	wrong := inv
	wrong.Runtime = &RemoteCommandAccess{Node: model.Node{ID: 10}}
	if err := store.cleanup(context.Background(), wrong, ref); err == nil {
		t.Fatal("foreign node acquired config ownership")
	}
	if _, err := os.Lstat(ref.path); err != nil {
		t.Fatal("foreign node removed config")
	}
	if err := store.cleanup(context.Background(), inv, ref); err != nil {
		t.Fatal(err)
	}
}

func TestRcloneCommandConfigScanBoundary(t *testing.T) {
	for _, count := range []int{128, 129} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			store := configStoreForTest(t, t.TempDir(), nil)
			inv := configInvocationForTest()
			ref, err := store.prepare(context.Background(), inv)
			if err != nil {
				t.Fatal(err)
			}
			for n := range count - 2 {
				if err := os.WriteFile(ref.path+fmt.Sprint(n), []byte("backup"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			err = store.cleanup(context.Background(), inv, ref)
			if count == 128 {
				if err != nil {
					t.Fatal(err)
				}
				if _, err := os.Lstat(filepath.Dir(ref.path)); !os.IsNotExist(err) {
					t.Fatal("complete directory retained")
				}
			} else {
				if err == nil {
					t.Fatal("scan overflow accepted")
				}
				entries, readErr := os.ReadDir(filepath.Dir(ref.path))
				if readErr != nil || len(entries) != 129 {
					t.Fatal("overflow deleted files before complete validation")
				}
			}
		})
	}
}
