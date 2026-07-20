//go:build !linux

package updater

import (
	"context"
	"net"
	"os"
)

type secureInboxDirectory struct{}

func openSecureInboxRoot(string) (*secureInboxDirectory, error) {
	return nil, ErrPolicyRejected
}

func (*secureInboxDirectory) OpenDirectory(string) (*secureInboxDirectory, error) {
	return nil, ErrPolicyRejected
}

func (*secureInboxDirectory) OpenRegular(string) (*os.File, error) {
	return nil, ErrPolicyRejected
}

func (*secureInboxDirectory) Stat() (os.FileInfo, error)      { return nil, ErrPolicyRejected }
func (*secureInboxDirectory) ReadDir() ([]os.DirEntry, error) { return nil, ErrPolicyRejected }
func (*secureInboxDirectory) Close() error                    { return nil }

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
