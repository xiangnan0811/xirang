package processing

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRemoteTransportIsDisabledByDefault(t *testing.T) {
	config, err := NewRemoteServerTLS(RemoteTransportConfig{})
	if err != nil || config != nil {
		t.Fatalf("disabled remote transport config=%v err=%v", config, err)
	}
}

func TestRemoteTransportRequiresTLS13PrivateCAAndExactWorkerURISAN(t *testing.T) {
	files, roots, clientCertificate := createTransportCertificates(t, "workers.example", strings.Repeat("a", 32))
	serverTLS, err := NewRemoteServerTLS(RemoteTransportConfig{
		Enabled: true, ListenAddress: "127.0.0.1:0", ServerCertFile: files.serverCert,
		ServerKeyFile: files.serverKey, ClientCAFile: files.caCert, TrustDomain: "workers.example",
	})
	if err != nil {
		t.Fatalf("NewRemoteServerTLS: %v", err)
	}
	if serverTLS.MinVersion != tls.VersionTLS13 || serverTLS.MaxVersion != tls.VersionTLS13 || serverTLS.ClientAuth != tls.RequireAndVerifyClientCert {
		t.Fatalf("remote TLS policy is not closed: %+v", serverTLS)
	}
	listener, err := tls.Listen("tcp", "127.0.0.1:0", serverTLS)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	type accepted struct {
		identity WorkerTransportIdentity
		err      error
	}
	result := make(chan accepted, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			result <- accepted{err: acceptErr}
			return
		}
		defer func() { _ = connection.Close() }()
		tlsConnection := connection.(*tls.Conn)
		if acceptErr = tlsConnection.HandshakeContext(context.Background()); acceptErr != nil {
			result <- accepted{err: acceptErr}
			return
		}
		identity, identityErr := RemoteIdentityFromConnection(tlsConnection.ConnectionState(), "workers.example")
		result <- accepted{identity: identity, err: identityErr}
	}()
	client, err := tls.Dial("tcp", listener.Addr().String(), &tls.Config{
		MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13, RootCAs: roots,
		Certificates: []tls.Certificate{clientCertificate}, ServerName: "localhost",
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = client.Close()
	acceptedResult := <-result
	if acceptedResult.err != nil || acceptedResult.identity.Kind != WorkerTransportMTLS ||
		acceptedResult.identity.WorkerID != strings.Repeat("a", 32) || acceptedResult.identity.Fingerprint == "" {
		t.Fatalf("mTLS identity=%+v err=%v", acceptedResult.identity, acceptedResult.err)
	}
}

func TestRemoteWorkerCertificateRejectsCNFallbackWildcardAndMultipleURI(t *testing.T) {
	workerID := strings.Repeat("b", 32)
	validURI, _ := url.Parse("spiffe://workers.example/asset-worker/" + workerID)
	wrongURI, _ := url.Parse("spiffe://other.example/asset-worker/" + workerID)
	now := time.Now().UTC()
	for name, certificate := range map[string]*x509.Certificate{
		"cn only":      {Subject: pkix.Name{CommonName: workerID}},
		"wrong domain": {URIs: []*url.URL{wrongURI}},
		"multiple URI": {URIs: []*url.URL{validURI, wrongURI}},
		"wildcard id":  {URIs: []*url.URL{{Scheme: "spiffe", Host: "workers.example", Path: "/asset-worker/*"}}},
		"wrong eku":    {NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), URIs: []*url.URL{validURI}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ValidateRemoteWorkerCertificate(certificate, "workers.example"); err == nil {
				t.Fatal("invalid remote certificate identity accepted")
			}
		})
	}
}

func TestRemoteTLSMaterialRejectsSymlinksAndExposedPrivateKeys(t *testing.T) {
	files, _, clientCertificate := createTransportCertificates(t, "workers.example", strings.Repeat("a", 32))
	clientDirectory := t.TempDir()
	clientCert := filepath.Join(clientDirectory, "client.pem")
	clientKey := filepath.Join(clientDirectory, "client-key.pem")
	writePEMFile(t, clientCert, "CERTIFICATE", clientCertificate.Certificate[0], 0o600)
	clientKeyDER, err := x509.MarshalPKCS8PrivateKey(clientCertificate.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	writePEMFile(t, clientKey, "PRIVATE KEY", clientKeyDER, 0o600)
	serverConfig := RemoteTransportConfig{
		Enabled: true, ListenAddress: "127.0.0.1:9443", ServerCertFile: files.serverCert,
		ServerKeyFile: files.serverKey, ClientCAFile: files.caCert, TrustDomain: "workers.example",
	}
	clientConfig := WorkerClientConfig{
		RemoteEndpoint: "https://127.0.0.1:9443", RemoteClientCertFile: clientCert,
		RemoteClientKeyFile: clientKey, RemoteServerCAFile: files.caCert,
	}
	if _, err := NewRemoteServerTLS(serverConfig); err != nil {
		t.Fatalf("safe server material: %v", err)
	}
	if _, _, err := workerRemoteClientTLS(clientConfig); err != nil {
		t.Fatalf("safe client material: %v", err)
	}

	if err := os.Chmod(files.serverKey, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRemoteServerTLS(serverConfig); !errors.Is(err, ErrWorkerTransportUnsafe) {
		t.Fatalf("world-readable server key accepted: %v", err)
	}
	if err := os.Chmod(files.serverKey, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(clientKey, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, _, err := workerRemoteClientTLS(clientConfig); !errors.Is(err, ErrWorkerTransportUnsafe) {
		t.Fatalf("group-readable client key accepted: %v", err)
	}
	if err := os.Chmod(clientKey, 0o600); err != nil {
		t.Fatal(err)
	}

	serverKeyLink := filepath.Join(t.TempDir(), "server-key-link.pem")
	if err := os.Symlink(files.serverKey, serverKeyLink); err != nil {
		t.Fatal(err)
	}
	serverConfig.ServerKeyFile = serverKeyLink
	if _, err := NewRemoteServerTLS(serverConfig); !errors.Is(err, ErrWorkerTransportUnsafe) {
		t.Fatalf("symlink server key accepted: %v", err)
	}
	clientCertLink := filepath.Join(t.TempDir(), "client-cert-link.pem")
	if err := os.Symlink(clientCert, clientCertLink); err != nil {
		t.Fatal(err)
	}
	clientConfig.RemoteClientCertFile = clientCertLink
	if _, _, err := workerRemoteClientTLS(clientConfig); !errors.Is(err, ErrWorkerTransportUnsafe) {
		t.Fatalf("symlink client certificate accepted: %v", err)
	}
}

func TestRemoteWorkerListenerAuthenticatesBeforeAccept(t *testing.T) {
	if listener, err := ListenRemoteWorker(RemoteTransportConfig{}); err != nil || listener != nil {
		t.Fatalf("disabled remote listener=%v err=%v", listener, err)
	}
	workerID := strings.Repeat("c", 32)
	files, roots, clientCertificate := createTransportCertificates(t, "workers.example", workerID)
	listener, err := ListenRemoteWorker(RemoteTransportConfig{
		Enabled: true, ListenAddress: "127.0.0.1:0", ServerCertFile: files.serverCert,
		ServerKeyFile: files.serverKey, ClientCAFile: files.caCert, TrustDomain: "workers.example",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	result := make(chan net.Conn, 1)
	errs := make(chan error, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			errs <- acceptErr
			return
		}
		result <- connection
	}()
	client, err := tls.Dial("tcp", listener.Addr().String(), &tls.Config{
		MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13, RootCAs: roots,
		Certificates: []tls.Certificate{clientCertificate}, ServerName: "localhost",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	select {
	case err := <-errs:
		t.Fatal(err)
	case connection := <-result:
		defer func() { _ = connection.Close() }()
		identity, ok := WorkerIdentityFromConn(connection)
		if !ok || identity.Kind != WorkerTransportMTLS || identity.WorkerID != workerID || identity.Fingerprint == "" {
			t.Fatalf("accepted mTLS connection lost identity: identity=%+v ok=%v", identity, ok)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("mTLS listener accept timed out")
	}
}

type transportCertificateFiles struct {
	caCert     string
	serverCert string
	serverKey  string
}

func createTransportCertificates(t *testing.T, trustDomain, workerID string) (transportCertificateFiles, *x509.CertPool, tls.Certificate) {
	t.Helper()
	now := time.Now().UTC()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "private-test-ca"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true,
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	serverKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	serverTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "ignored-server-cn"}, DNSNames: []string{"localhost"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	serverDER, err := x509.CreateCertificate(rand.Reader, serverTemplate, caTemplate, &serverKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	clientKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	workerURI, _ := url.Parse("spiffe://" + trustDomain + "/asset-worker/" + workerID)
	clientTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(3), Subject: pkix.Name{CommonName: "ignored-client-cn"}, URIs: []*url.URL{workerURI},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	clientDER, err := x509.CreateCertificate(rand.Reader, clientTemplate, caTemplate, &clientKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	files := transportCertificateFiles{
		caCert: filepath.Join(directory, "ca.pem"), serverCert: filepath.Join(directory, "server.pem"), serverKey: filepath.Join(directory, "server-key.pem"),
	}
	writePEMFile(t, files.caCert, "CERTIFICATE", caDER, 0o600)
	writePEMFile(t, files.serverCert, "CERTIFICATE", serverDER, 0o600)
	serverKeyDER, _ := x509.MarshalPKCS8PrivateKey(serverKey)
	writePEMFile(t, files.serverKey, "PRIVATE KEY", serverKeyDER, 0o600)
	clientKeyDER, _ := x509.MarshalPKCS8PrivateKey(clientKey)
	clientCertificate, err := tls.X509KeyPair(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: clientDER}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: clientKeyDER}),
	)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	parsedCA, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	roots.AddCert(parsedCA)
	return files, roots, clientCertificate
}

func writePEMFile(t *testing.T, path, blockType string, payload []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: payload}), mode); err != nil {
		t.Fatal(err)
	}
}
