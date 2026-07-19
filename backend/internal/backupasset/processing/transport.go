package processing

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var (
	ErrWorkerTransportUnsupported = errors.New("worker transport unsupported")
	ErrWorkerTransportUnsafe      = errors.New("worker transport boundary unsafe")
	ErrWorkerUnauthenticated      = errors.New("worker transport unauthenticated")
)

type WorkerTransportKind string

const (
	WorkerTransportLocal WorkerTransportKind = "local"
	WorkerTransportMTLS  WorkerTransportKind = "mtls"
)

type WorkerTransportIdentity struct {
	Kind        WorkerTransportKind `json:"-"`
	Fingerprint string              `json:"-"`
	WorkerID    string              `json:"-"`
	PeerPID     int32               `json:"-"`
	PeerUID     uint32              `json:"-"`
	PeerGID     uint32              `json:"-"`
}

type WorkerAuthenticatedConn interface {
	net.Conn
	WorkerIdentity() WorkerTransportIdentity
}

type workerIdentityConn struct {
	net.Conn
	identity WorkerTransportIdentity
}

func (connection *workerIdentityConn) WorkerIdentity() WorkerTransportIdentity {
	if connection == nil {
		return WorkerTransportIdentity{}
	}
	return connection.identity
}

func WorkerIdentityFromConn(connection net.Conn) (WorkerTransportIdentity, bool) {
	authenticated, ok := connection.(WorkerAuthenticatedConn)
	if !ok || authenticated == nil {
		return WorkerTransportIdentity{}, false
	}
	identity := authenticated.WorkerIdentity()
	if !validTransportIdentity(identity) {
		return WorkerTransportIdentity{}, false
	}
	return identity, true
}

type LocalTransportConfig struct {
	SocketPath string
}

type RemoteTransportConfig struct {
	Enabled        bool
	ListenAddress  string
	ServerCertFile string
	ServerKeyFile  string
	ClientCAFile   string
	TrustDomain    string
}

type RemoteWorkerListener struct {
	listener    net.Listener
	tlsConfig   *tls.Config
	trustDomain string
}

func ListenRemoteWorker(config RemoteTransportConfig) (*RemoteWorkerListener, error) {
	tlsConfig, err := NewRemoteServerTLS(config)
	if err != nil || tlsConfig == nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", config.ListenAddress)
	if err != nil {
		return nil, errors.Join(ErrWorkerTransportUnsafe, err)
	}
	return &RemoteWorkerListener{listener: listener, tlsConfig: tlsConfig, trustDomain: config.TrustDomain}, nil
}

func (listener *RemoteWorkerListener) Accept() (net.Conn, error) {
	if listener == nil || listener.listener == nil || listener.tlsConfig == nil {
		return nil, ErrWorkerTransportUnsafe
	}
	for {
		raw, err := listener.listener.Accept()
		if err != nil {
			return nil, err
		}
		connection := tls.Server(raw, listener.tlsConfig.Clone())
		handshakeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err = connection.HandshakeContext(handshakeCtx)
		cancel()
		if err != nil {
			_ = connection.Close()
			continue
		}
		identity, err := RemoteIdentityFromConnection(connection.ConnectionState(), listener.trustDomain)
		if err != nil {
			_ = connection.Close()
			continue
		}
		return &workerIdentityConn{Conn: connection, identity: identity}, nil
	}
}

func (listener *RemoteWorkerListener) Close() error {
	if listener == nil || listener.listener == nil {
		return nil
	}
	return listener.listener.Close()
}

func (listener *RemoteWorkerListener) Addr() net.Addr {
	if listener == nil || listener.listener == nil {
		return nil
	}
	return listener.listener.Addr()
}

func NewRemoteServerTLS(config RemoteTransportConfig) (*tls.Config, error) {
	if !config.Enabled {
		return nil, nil
	}
	if !validRemoteTransportConfig(config) {
		return nil, ErrWorkerTransportUnsafe
	}
	serverCertificate, err := loadPrivateX509KeyPair(config.ServerCertFile, config.ServerKeyFile)
	if err != nil {
		return nil, err
	}
	caPEM, err := readPrivateRegularFile(config.ClientCAFile)
	if err != nil {
		return nil, err
	}
	clientCAs := x509.NewCertPool()
	if !clientCAs.AppendCertsFromPEM(caPEM) {
		return nil, ErrWorkerTransportUnsafe
	}
	result := &tls.Config{
		MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13,
		Certificates: []tls.Certificate{serverCertificate}, ClientCAs: clientCAs,
		ClientAuth: tls.RequireAndVerifyClientCert,
	}
	result.VerifyConnection = func(state tls.ConnectionState) error {
		if state.Version != tls.VersionTLS13 || len(state.VerifiedChains) != 1 || len(state.PeerCertificates) == 0 {
			return ErrWorkerUnauthenticated
		}
		_, err := ValidateRemoteWorkerCertificate(state.PeerCertificates[0], config.TrustDomain)
		return err
	}
	return result, nil
}

func ValidateRemoteWorkerCertificate(certificate *x509.Certificate, trustDomain string) (string, error) {
	if certificate == nil || !validTrustDomain(trustDomain) || time.Now().Before(certificate.NotBefore) || !time.Now().Before(certificate.NotAfter) ||
		len(certificate.ExtKeyUsage) != 1 || certificate.ExtKeyUsage[0] != x509.ExtKeyUsageClientAuth || len(certificate.UnknownExtKeyUsage) != 0 ||
		len(certificate.URIs) != 1 || len(certificate.DNSNames) != 0 || len(certificate.IPAddresses) != 0 || len(certificate.EmailAddresses) != 0 {
		return "", ErrWorkerUnauthenticated
	}
	identity := certificate.URIs[0]
	if identity == nil || identity.Scheme != "spiffe" || identity.Host != trustDomain || identity.User != nil ||
		identity.RawQuery != "" || identity.Fragment != "" || identity.RawPath != "" {
		return "", ErrWorkerUnauthenticated
	}
	prefix := "/asset-worker/"
	if !strings.HasPrefix(identity.Path, prefix) || strings.Count(strings.TrimPrefix(identity.Path, prefix), "/") != 0 {
		return "", ErrWorkerUnauthenticated
	}
	workerID := strings.TrimPrefix(identity.Path, prefix)
	if !lowerHex(workerID, 32) {
		return "", ErrWorkerUnauthenticated
	}
	return workerID, nil
}

func RemoteIdentityFromConnection(state tls.ConnectionState, trustDomain string) (WorkerTransportIdentity, error) {
	if state.Version != tls.VersionTLS13 || len(state.VerifiedChains) != 1 || len(state.PeerCertificates) == 0 {
		return WorkerTransportIdentity{}, ErrWorkerUnauthenticated
	}
	leaf := state.PeerCertificates[0]
	workerID, err := ValidateRemoteWorkerCertificate(leaf, trustDomain)
	if err != nil || len(leaf.Raw) == 0 {
		return WorkerTransportIdentity{}, ErrWorkerUnauthenticated
	}
	digest := sha256.Sum256(leaf.Raw)
	return WorkerTransportIdentity{
		Kind: WorkerTransportMTLS, Fingerprint: hex.EncodeToString(digest[:]), WorkerID: workerID,
	}, nil
}

func validRemoteTransportConfig(config RemoteTransportConfig) bool {
	host, _, err := net.SplitHostPort(config.ListenAddress)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.IsUnspecified() || ip.IsMulticast() || !validTrustDomain(config.TrustDomain) {
		return false
	}
	for _, path := range []string{config.ServerCertFile, config.ServerKeyFile, config.ClientCAFile} {
		if strings.TrimSpace(path) != path || path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return false
		}
	}
	return true
}

func validTrustDomain(value string) bool {
	if value == "" || strings.ToLower(value) != value || len(value) > 253 || strings.ContainsAny(value, "*/:@/?#\x00") {
		return false
	}
	parsed, err := url.Parse("spiffe://" + value + "/")
	return err == nil && parsed.Host == value && parsed.Scheme == "spiffe"
}

func readPrivateRegularFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.Join(ErrWorkerTransportUnsafe, err)
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.Join(ErrWorkerTransportUnsafe, err)
	}
	if block, _ := pem.Decode(payload); block == nil {
		return nil, fmt.Errorf("%w: invalid PEM material", ErrWorkerTransportUnsafe)
	}
	return payload, nil
}

func loadPrivateX509KeyPair(certificatePath, keyPath string) (tls.Certificate, error) {
	certificatePEM, err := readPrivateRegularFile(certificatePath)
	if err != nil {
		return tls.Certificate{}, err
	}
	keyPEM, err := readPrivateRegularFile(keyPath)
	if err != nil {
		return tls.Certificate{}, err
	}
	pair, err := tls.X509KeyPair(certificatePEM, keyPEM)
	if err != nil {
		return tls.Certificate{}, errors.Join(ErrWorkerTransportUnsafe, err)
	}
	return pair, nil
}
