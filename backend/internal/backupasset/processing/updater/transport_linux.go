//go:build linux

package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const secureInboxResolve = unix.RESOLVE_BENEATH | unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_SYMLINKS

type secureStoreDirectory struct {
	file *os.File
}

func openSecureStoreRoot(path string) (*secureStoreDirectory, error) {
	slashFD, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = unix.Close(slashFD) }()
	relative := strings.TrimPrefix(path, "/")
	if relative == "" {
		return nil, ErrPolicyRejected
	}
	return openSecureStoreDirectoryAt(slashFD, relative)
}

func openSecureStoreDirectoryAt(parentFD int, name string) (*secureStoreDirectory, error) {
	fd, err := unix.Openat2(parentFD, name, &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW,
		Resolve: secureInboxResolve,
	})
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		_ = unix.Close(fd)
		return nil, ErrPolicyRejected
	}
	return &secureStoreDirectory{file: file}, nil
}

func (directory *secureStoreDirectory) OpenDirectory(name string) (*secureStoreDirectory, error) {
	if directory == nil || directory.file == nil || !validStoreComponent(name) {
		return nil, ErrPolicyRejected
	}
	return openSecureStoreDirectoryAt(int(directory.file.Fd()), name)
}

func (directory *secureStoreDirectory) CreateDirectory(name string, mode os.FileMode) error {
	if directory == nil || directory.file == nil || !validStoreComponent(name) || mode.Perm() != mode {
		return ErrPolicyRejected
	}
	return unix.Mkdirat(int(directory.file.Fd()), name, uint32(mode.Perm()))
}

func (directory *secureStoreDirectory) OpenFile(name string, flags int, mode os.FileMode) (*os.File, error) {
	if directory == nil || directory.file == nil || !validStoreComponent(name) || mode.Perm() != mode {
		return nil, ErrPolicyRejected
	}
	fd, err := unix.Openat2(int(directory.file.Fd()), name, &unix.OpenHow{
		Flags:   uint64(flags | unix.O_CLOEXEC | unix.O_NOFOLLOW),
		Mode:    uint64(mode.Perm()),
		Resolve: secureInboxResolve,
	})
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		_ = unix.Close(fd)
		return nil, ErrPolicyRejected
	}
	return file, nil
}

func (directory *secureStoreDirectory) RenameNoReplace(oldName, newName string) error {
	if directory == nil || directory.file == nil || !validStoreComponent(oldName) || !validStoreComponent(newName) {
		return ErrPolicyRejected
	}
	fd := int(directory.file.Fd())
	return unix.Renameat2(fd, oldName, fd, newName, unix.RENAME_NOREPLACE)
}

func (directory *secureStoreDirectory) RemoveFile(name string) error {
	if directory == nil || directory.file == nil || !validStoreComponent(name) {
		return ErrPolicyRejected
	}
	return unix.Unlinkat(int(directory.file.Fd()), name, 0)
}

func (directory *secureStoreDirectory) RemoveDirectory(name string) error {
	if directory == nil || directory.file == nil || !validStoreComponent(name) {
		return ErrPolicyRejected
	}
	return unix.Unlinkat(int(directory.file.Fd()), name, unix.AT_REMOVEDIR)
}

func (directory *secureStoreDirectory) Stat() (os.FileInfo, error) {
	if directory == nil || directory.file == nil {
		return nil, ErrPolicyRejected
	}
	return directory.file.Stat()
}

func (directory *secureStoreDirectory) ReadDir(maximum int) ([]os.DirEntry, error) {
	if directory == nil || directory.file == nil || maximum <= 0 {
		return nil, ErrPolicyRejected
	}
	return directory.file.ReadDir(maximum)
}

func (directory *secureStoreDirectory) rewind() error {
	if directory == nil || directory.file == nil {
		return ErrPolicyRejected
	}
	_, err := directory.file.Seek(0, io.SeekStart)
	return err
}

func (directory *secureStoreDirectory) Chmod(mode os.FileMode) error {
	if directory == nil || directory.file == nil || mode.Perm() != mode {
		return ErrPolicyRejected
	}
	return directory.file.Chmod(mode)
}

func (directory *secureStoreDirectory) Sync() error {
	if directory == nil || directory.file == nil {
		return ErrPolicyRejected
	}
	return directory.file.Sync()
}

func (directory *secureStoreDirectory) Close() error {
	if directory == nil || directory.file == nil {
		return nil
	}
	err := directory.file.Close()
	directory.file = nil
	return err
}

type secureInboxDirectory struct {
	file *os.File
}

func openSecureInboxRoot(path string) (*secureInboxDirectory, error) {
	slashFD, err := unix.Open("/", unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = unix.Close(slashFD) }()
	relative := strings.TrimPrefix(path, "/")
	if relative == "" {
		return nil, ErrPolicyRejected
	}
	return openSecureInboxDirectoryAt(slashFD, relative)
}

func openSecureInboxDirectoryAt(parentFD int, name string) (*secureInboxDirectory, error) {
	fd, err := unix.Openat2(parentFD, name, &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW,
		Resolve: secureInboxResolve,
	})
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		_ = unix.Close(fd)
		return nil, ErrPolicyRejected
	}
	return &secureInboxDirectory{file: file}, nil
}

func (directory *secureInboxDirectory) OpenDirectory(name string) (*secureInboxDirectory, error) {
	if directory == nil || directory.file == nil || !validInboxEntryName(name) {
		return nil, ErrPolicyRejected
	}
	return openSecureInboxDirectoryAt(int(directory.file.Fd()), name)
}

func (directory *secureInboxDirectory) OpenRegular(name string) (*os.File, error) {
	if directory == nil || directory.file == nil || !validInboxEntryName(name) {
		return nil, ErrPolicyRejected
	}
	fd, err := unix.Openat2(int(directory.file.Fd()), name, &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW,
		Resolve: secureInboxResolve,
	})
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		_ = unix.Close(fd)
		return nil, ErrPolicyRejected
	}
	return file, nil
}

func (directory *secureInboxDirectory) Stat() (os.FileInfo, error) {
	if directory == nil || directory.file == nil {
		return nil, ErrPolicyRejected
	}
	return directory.file.Stat()
}

func (directory *secureInboxDirectory) ReadDir(maximum int) ([]os.DirEntry, error) {
	if directory == nil || directory.file == nil || maximum <= 0 {
		return nil, ErrPolicyRejected
	}
	return directory.file.ReadDir(maximum)
}

func (directory *secureInboxDirectory) Close() error {
	if directory == nil || directory.file == nil {
		return nil
	}
	return directory.file.Close()
}

type LocalUpdaterListener struct {
	listener    *net.UnixListener
	socketPath  string
	socketInfo  os.FileInfo
	expectedUID uint32
	expectedGID uint32
}

func ListenLocalUpdater(config LocalUpdaterTransportConfig) (*LocalUpdaterListener, error) {
	if !validUpdaterSocketPath(config.SocketPath) {
		return nil, fmt.Errorf("%w: socket_path", ErrTransportUnsafe)
	}
	parent := filepath.Dir(config.SocketPath)
	parentInfo, err := os.Lstat(parent)
	if err != nil || !validUpdaterSocketParent(parentInfo, config.ExpectedPeerGID) {
		return nil, fmt.Errorf("%w: socket_parent", ErrTransportUnsafe)
	}
	resolved, err := filepath.EvalSymlinks(parent)
	if err != nil || filepath.Clean(resolved) != parent {
		return nil, fmt.Errorf("%w: socket_parent_resolution", ErrTransportUnsafe)
	}
	if err := removeUpdaterStaleSocket(config.SocketPath); err != nil {
		return nil, err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: config.SocketPath, Net: "unix"})
	if err != nil {
		return nil, errors.Join(ErrTransportUnsafe, err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = listener.Close()
			_ = os.Remove(config.SocketPath)
		}
	}()
	if err := os.Chown(config.SocketPath, -1, int(config.ExpectedPeerGID)); err != nil || os.Chmod(config.SocketPath, 0o660) != nil {
		return nil, fmt.Errorf("%w: socket_permissions", ErrTransportUnsafe)
	}
	socketInfo, err := os.Lstat(config.SocketPath)
	if err != nil || !validUpdaterSocket(socketInfo, config.ExpectedPeerGID) {
		return nil, fmt.Errorf("%w: socket_identity", ErrTransportUnsafe)
	}
	cleanup = false
	return &LocalUpdaterListener{
		listener: listener, socketPath: config.SocketPath, socketInfo: socketInfo,
		expectedUID: config.ExpectedPeerUID, expectedGID: config.ExpectedPeerGID,
	}, nil
}

func (listener *LocalUpdaterListener) AcceptIdentity(ctx context.Context) (net.Conn, UpdaterTransportIdentity, error) {
	if listener == nil || listener.listener == nil || listener.validateSocket() != nil {
		return nil, UpdaterTransportIdentity{}, ErrTransportUnsafe
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
		return nil, UpdaterTransportIdentity{}, err
	}
	identity, err := updaterPeerIdentity(connection, listener.expectedUID, listener.expectedGID)
	if err != nil {
		_ = connection.Close()
		return nil, UpdaterTransportIdentity{}, err
	}
	return connection, identity, nil
}

func (listener *LocalUpdaterListener) Accept() (net.Conn, error) {
	connection, identity, err := listener.AcceptIdentity(context.Background())
	if err != nil {
		return nil, err
	}
	return &updaterIdentityConn{Conn: connection, identity: identity}, nil
}

func (listener *LocalUpdaterListener) Addr() net.Addr {
	if listener == nil || listener.listener == nil {
		return nil
	}
	return listener.listener.Addr()
}

func (listener *LocalUpdaterListener) Close() error {
	if listener == nil {
		return nil
	}
	var result error
	if listener.listener != nil {
		result = listener.listener.Close()
	}
	current, err := os.Lstat(listener.socketPath)
	if err == nil && listener.socketInfo != nil && current.Mode()&os.ModeSocket != 0 && os.SameFile(current, listener.socketInfo) {
		result = errors.Join(result, os.Remove(listener.socketPath))
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		result = errors.Join(result, err)
	}
	return result
}

func (listener *LocalUpdaterListener) validateSocket() error {
	current, err := os.Lstat(listener.socketPath)
	if err != nil || listener.socketInfo == nil || !os.SameFile(listener.socketInfo, current) ||
		!validUpdaterSocket(current, listener.expectedGID) {
		return ErrTransportUnsafe
	}
	return nil
}

func updaterPeerIdentity(connection *net.UnixConn, expectedUID, expectedGID uint32) (UpdaterTransportIdentity, error) {
	raw, err := connection.SyscallConn()
	if err != nil {
		return UpdaterTransportIdentity{}, ErrUpdaterUnauthenticated
	}
	var credential *syscall.Ucred
	var socketErr error
	if err := raw.Control(func(fd uintptr) {
		credential, socketErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil || socketErr != nil || credential == nil || credential.Uid != expectedUID || credential.Gid != expectedGID {
		return UpdaterTransportIdentity{}, ErrUpdaterUnauthenticated
	}
	payload := "xirang.asset-worker-updater.local.v1\x00" + strconv.FormatUint(uint64(credential.Uid), 10) +
		"\x00" + strconv.FormatUint(uint64(credential.Gid), 10)
	digest := sha256.Sum256([]byte(payload))
	return UpdaterTransportIdentity{
		Fingerprint: hex.EncodeToString(digest[:]), PeerPID: credential.Pid, PeerUID: credential.Uid, PeerGID: credential.Gid,
	}, nil
}

func validUpdaterSocketPath(value string) bool {
	return value != "" && filepath.IsAbs(value) && filepath.Clean(value) == value && filepath.Base(value) != "." &&
		filepath.Base(value) != ".." && !strings.ContainsAny(value, "\x00\r\n")
}

func validUpdaterSocketParent(info os.FileInfo, expectedGID uint32) bool {
	if info == nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return false
	}
	permissions := info.Mode().Perm()
	if permissions == 0o700 {
		return true
	}
	return stat.Gid == expectedGID && (permissions == 0o710 || permissions == 0o750 || permissions == 0o770)
}

func validUpdaterSocket(info os.FileInfo, expectedGID uint32) bool {
	if info == nil || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0o660 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid()) && stat.Gid == expectedGID
}

func removeUpdaterStaleSocket(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || info.Mode()&os.ModeSocket == 0 {
		return errors.Join(ErrTransportUnsafe, err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return ErrTransportUnsafe
	}
	connection, dialErr := net.DialTimeout("unix", path, 100*time.Millisecond)
	if dialErr == nil {
		_ = connection.Close()
		return fmt.Errorf("%w: socket is active", ErrTransportUnsafe)
	}
	if err := os.Remove(path); err != nil {
		return errors.Join(ErrTransportUnsafe, err)
	}
	return nil
}
