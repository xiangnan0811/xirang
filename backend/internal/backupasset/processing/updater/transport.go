package updater

import (
	"net"
)

type UpdaterTransportIdentity struct {
	Fingerprint string `json:"-"`
	PeerPID     int32  `json:"-"`
	PeerUID     uint32 `json:"-"`
	PeerGID     uint32 `json:"-"`
}

type UpdaterAuthenticatedConn interface {
	net.Conn
	UpdaterIdentity() UpdaterTransportIdentity
}

type updaterIdentityConn struct {
	net.Conn
	identity UpdaterTransportIdentity
}

func (connection *updaterIdentityConn) UpdaterIdentity() UpdaterTransportIdentity {
	if connection == nil {
		return UpdaterTransportIdentity{}
	}
	return connection.identity
}

func UpdaterIdentityFromConn(connection net.Conn) (UpdaterTransportIdentity, bool) {
	authenticated, ok := connection.(UpdaterAuthenticatedConn)
	if !ok || authenticated == nil {
		return UpdaterTransportIdentity{}, false
	}
	identity := authenticated.UpdaterIdentity()
	if !lowerHex(identity.Fingerprint, 64) || identity.PeerPID < 0 {
		return UpdaterTransportIdentity{}, false
	}
	return identity, true
}

type LocalUpdaterTransportConfig struct {
	SocketPath      string
	ExpectedPeerUID uint32
	ExpectedPeerGID uint32
}
