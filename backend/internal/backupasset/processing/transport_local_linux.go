//go:build linux

package processing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

type LocalWorkerListener struct {
	listener   *net.UnixListener
	socketPath string
	socketInfo os.FileInfo
}

func ListenLocalWorker(config LocalTransportConfig) (*LocalWorkerListener, error) {
	if !validLocalSocketPath(config.SocketPath) {
		return nil, ErrWorkerTransportUnsafe
	}
	parent := filepath.Dir(config.SocketPath)
	parentInfo, err := os.Lstat(parent)
	if err != nil || !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 || parentInfo.Mode().Perm() != 0o700 {
		return nil, errors.Join(ErrWorkerTransportUnsafe, err)
	}
	parentStat, ok := parentInfo.Sys().(*syscall.Stat_t)
	if !ok || parentStat.Uid != uint32(os.Geteuid()) {
		return nil, ErrWorkerTransportUnsafe
	}
	resolved, err := filepath.EvalSymlinks(parent)
	if err != nil || filepath.Clean(resolved) != parent {
		return nil, errors.Join(ErrWorkerTransportUnsafe, err)
	}
	if err := removeOwnedStaleSocket(config.SocketPath); err != nil {
		return nil, err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: config.SocketPath, Net: "unix"})
	if err != nil {
		return nil, errors.Join(ErrWorkerTransportUnsafe, err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = listener.Close()
			_ = os.Remove(config.SocketPath)
		}
	}()
	if err := os.Chmod(config.SocketPath, 0o600); err != nil {
		return nil, errors.Join(ErrWorkerTransportUnsafe, err)
	}
	socketInfo, err := os.Lstat(config.SocketPath)
	if err != nil || socketInfo.Mode()&os.ModeSocket == 0 || socketInfo.Mode().Perm() != 0o600 {
		return nil, errors.Join(ErrWorkerTransportUnsafe, err)
	}
	cleanup = false
	return &LocalWorkerListener{listener: listener, socketPath: config.SocketPath, socketInfo: socketInfo}, nil
}

func (listener *LocalWorkerListener) AcceptIdentity(ctx context.Context) (net.Conn, WorkerTransportIdentity, error) {
	if listener == nil || listener.listener == nil {
		return nil, WorkerTransportIdentity{}, ErrWorkerTransportUnsafe
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = listener.listener.SetDeadline(deadline)
		defer func() { _ = listener.listener.SetDeadline(time.Time{}) }()
	}
	connection, err := listener.listener.AcceptUnix()
	if err != nil {
		return nil, WorkerTransportIdentity{}, err
	}
	identity, err := localPeerIdentity(connection)
	if err != nil {
		_ = connection.Close()
		return nil, WorkerTransportIdentity{}, err
	}
	return connection, identity, nil
}

func (listener *LocalWorkerListener) Accept() (net.Conn, error) {
	connection, identity, err := listener.AcceptIdentity(context.Background())
	if err != nil {
		return nil, err
	}
	return &workerIdentityConn{Conn: connection, identity: identity}, nil
}

func (listener *LocalWorkerListener) Addr() net.Addr {
	if listener == nil || listener.listener == nil {
		return nil
	}
	return listener.listener.Addr()
}

func (listener *LocalWorkerListener) Close() error {
	if listener == nil {
		return nil
	}
	var result error
	if listener.listener != nil {
		result = listener.listener.Close()
	}
	current, err := os.Lstat(listener.socketPath)
	if err == nil && current.Mode()&os.ModeSocket != 0 && listener.socketInfo != nil && os.SameFile(current, listener.socketInfo) {
		result = errors.Join(result, os.Remove(listener.socketPath))
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		result = errors.Join(result, err)
	}
	return result
}

func localPeerIdentity(connection *net.UnixConn) (WorkerTransportIdentity, error) {
	raw, err := connection.SyscallConn()
	if err != nil {
		return WorkerTransportIdentity{}, ErrWorkerUnauthenticated
	}
	var credential *syscall.Ucred
	var socketErr error
	if err := raw.Control(func(fd uintptr) {
		credential, socketErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil || socketErr != nil || credential == nil || credential.Uid != uint32(os.Geteuid()) {
		return WorkerTransportIdentity{}, ErrWorkerUnauthenticated
	}
	payload := "xirang.asset-worker.local.v1\x00" + strconv.FormatUint(uint64(credential.Uid), 10) + "\x00" + strconv.FormatUint(uint64(credential.Gid), 10)
	digest := sha256.Sum256([]byte(payload))
	return WorkerTransportIdentity{
		Kind: WorkerTransportLocal, Fingerprint: hex.EncodeToString(digest[:]),
		PeerPID: credential.Pid, PeerUID: credential.Uid, PeerGID: credential.Gid,
	}, nil
}

func validLocalSocketPath(path string) bool {
	return path != "" && filepath.IsAbs(path) && filepath.Clean(path) == path && filepath.Base(path) != "." && filepath.Base(path) != ".."
}

func removeOwnedStaleSocket(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || info.Mode()&os.ModeSocket == 0 {
		return errors.Join(ErrWorkerTransportUnsafe, err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return ErrWorkerTransportUnsafe
	}
	connection, dialErr := net.DialTimeout("unix", path, 100*time.Millisecond)
	if dialErr == nil {
		_ = connection.Close()
		return fmt.Errorf("%w: socket is already active", ErrWorkerTransportUnsafe)
	}
	if err := os.Remove(path); err != nil {
		return errors.Join(ErrWorkerTransportUnsafe, err)
	}
	return nil
}
