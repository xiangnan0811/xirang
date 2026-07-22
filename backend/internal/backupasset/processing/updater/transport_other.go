//go:build !linux

package updater

import (
	"context"
	"net"
	"os"
)

type secureInboxDirectory struct{}

type secureStoreDirectory struct{}

func openSecureStoreRoot(string) (*secureStoreDirectory, error) { return nil, ErrPolicyRejected }
func (*secureStoreDirectory) OpenDirectory(string) (*secureStoreDirectory, error) {
	return nil, ErrPolicyRejected
}
func (*secureStoreDirectory) CreateDirectory(string, os.FileMode) error { return ErrPolicyRejected }
func (*secureStoreDirectory) OpenFile(string, int, os.FileMode) (*os.File, error) {
	return nil, ErrPolicyRejected
}
func (*secureStoreDirectory) RenameNoReplace(string, string) error { return ErrPolicyRejected }
func (*secureStoreDirectory) RemoveFile(string) error              { return ErrPolicyRejected }
func (*secureStoreDirectory) RemoveDirectory(string) error         { return ErrPolicyRejected }
func (*secureStoreDirectory) Stat() (os.FileInfo, error)           { return nil, ErrPolicyRejected }
func (*secureStoreDirectory) ReadDir(int) ([]os.DirEntry, error)   { return nil, ErrPolicyRejected }
func (*secureStoreDirectory) rewind() error                        { return ErrPolicyRejected }
func (*secureStoreDirectory) Chmod(os.FileMode) error              { return ErrPolicyRejected }
func (*secureStoreDirectory) Sync() error                          { return ErrPolicyRejected }
func (*secureStoreDirectory) Close() error                         { return nil }

func openSecureInboxRoot(string) (*secureInboxDirectory, error) {
	return nil, ErrPolicyRejected
}

func (*secureInboxDirectory) OpenDirectory(string) (*secureInboxDirectory, error) {
	return nil, ErrPolicyRejected
}

func (*secureInboxDirectory) OpenRegular(string) (*os.File, error) {
	return nil, ErrPolicyRejected
}

func (*secureInboxDirectory) Stat() (os.FileInfo, error)         { return nil, ErrPolicyRejected }
func (*secureInboxDirectory) ReadDir(int) ([]os.DirEntry, error) { return nil, ErrPolicyRejected }
func (*secureInboxDirectory) Close() error                       { return nil }

type LocalUpdaterListener struct{}

func ListenLocalUpdater(LocalUpdaterTransportConfig) (*LocalUpdaterListener, error) {
	return nil, ErrTransportUnsafe
}

func (*LocalUpdaterListener) AcceptIdentity(context.Context) (net.Conn, UpdaterTransportIdentity, error) {
	return nil, UpdaterTransportIdentity{}, ErrTransportUnsafe
}

func (*LocalUpdaterListener) Accept() (net.Conn, error) { return nil, ErrTransportUnsafe }
func (*LocalUpdaterListener) Addr() net.Addr            { return nil }
func (*LocalUpdaterListener) Close() error              { return nil }
