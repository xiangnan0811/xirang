//go:build !linux

package processing

import (
	"context"
	"net"
)

type LocalWorkerListener struct{}

func ListenLocalWorker(LocalTransportConfig) (*LocalWorkerListener, error) {
	return nil, ErrWorkerTransportUnsupported
}

func (*LocalWorkerListener) AcceptIdentity(context.Context) (net.Conn, WorkerTransportIdentity, error) {
	return nil, WorkerTransportIdentity{}, ErrWorkerTransportUnsupported
}

func (*LocalWorkerListener) Close() error { return nil }
