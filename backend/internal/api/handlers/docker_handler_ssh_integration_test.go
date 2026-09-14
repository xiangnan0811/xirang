package handlers

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"xirang/backend/internal/model"
	"xirang/backend/internal/sshutil"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/ssh"
	"gorm.io/gorm"
)

const dockerSSHTestPassword = "FAKE_DOCKER_SSH_PASSWORD_FOR_TEST_ONLY"

type dockerSSHTestServer struct {
	addr     string
	password string
	listener net.Listener
	config   *ssh.ServerConfig
	behavior func(command string, channel ssh.Channel, connectionDone <-chan struct{})

	commands         chan string
	connectionClosed chan struct{}
	connectionClose  sync.Once
	connectionsMu    sync.Mutex
	connections      map[net.Conn]struct{}
	workers          sync.WaitGroup
}

func startDockerSSHTestServer(t *testing.T, behavior func(string, ssh.Channel, <-chan struct{})) *dockerSSHTestServer {
	t.Helper()
	t.Setenv("SSH_STRICT_HOST_KEY_CHECKING", "false")
	t.Setenv("SSH_AUTO_ACCEPT_NEW_HOSTS", "false")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen Docker SSH test server: %v", err)
	}
	_, hostPrivateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		_ = listener.Close()
		t.Fatalf("generate Docker SSH host key: %v", err)
	}
	hostSigner, err := ssh.NewSignerFromKey(hostPrivateKey)
	if err != nil {
		_ = listener.Close()
		t.Fatalf("load Docker SSH host key: %v", err)
	}
	config := &ssh.ServerConfig{
		PasswordCallback: func(metadata ssh.ConnMetadata, supplied []byte) (*ssh.Permissions, error) {
			if metadata.User() != "docker-test" || string(supplied) != dockerSSHTestPassword {
				return nil, fmt.Errorf("Docker SSH test credentials rejected")
			}
			return nil, nil
		},
	}
	config.AddHostKey(hostSigner)
	server := &dockerSSHTestServer{
		addr:             listener.Addr().String(),
		password:         dockerSSHTestPassword,
		listener:         listener,
		config:           config,
		behavior:         behavior,
		commands:         make(chan string, 8),
		connectionClosed: make(chan struct{}),
		connections:      make(map[net.Conn]struct{}),
	}
	server.workers.Add(1)
	go server.acceptLoop()
	t.Cleanup(func() {
		server.close(t)
	})
	return server
}

func (server *dockerSSHTestServer) acceptLoop() {
	defer server.workers.Done()
	for {
		rawConn, err := server.listener.Accept()
		if err != nil {
			return
		}
		server.connectionsMu.Lock()
		server.connections[rawConn] = struct{}{}
		server.connectionsMu.Unlock()
		server.workers.Add(1)
		go func() {
			defer server.workers.Done()
			defer func() {
				server.connectionsMu.Lock()
				delete(server.connections, rawConn)
				server.connectionsMu.Unlock()
			}()
			server.serveConnection(rawConn)
		}()
	}
}

func (server *dockerSSHTestServer) serveConnection(rawConn net.Conn) {
	serverConn, channels, requests, err := ssh.NewServerConn(rawConn, server.config)
	if err != nil {
		return
	}
	defer func() { _ = serverConn.Close() }()
	go ssh.DiscardRequests(requests)

	connectionDone := make(chan struct{})
	go func() {
		_ = serverConn.Wait()
		close(connectionDone)
		server.connectionClose.Do(func() { close(server.connectionClosed) })
	}()

	var channelWorkers sync.WaitGroup
	for newChannel := range channels {
		if newChannel.ChannelType() != "session" {
			_ = newChannel.Reject(ssh.UnknownChannelType, "Docker test server only accepts sessions")
			continue
		}
		channel, channelRequests, err := newChannel.Accept()
		if err != nil {
			continue
		}
		channelWorkers.Add(1)
		go func() {
			defer channelWorkers.Done()
			server.serveSession(channel, channelRequests, connectionDone)
		}()
	}
	channelWorkers.Wait()
	<-connectionDone
}

func (server *dockerSSHTestServer) serveSession(channel ssh.Channel, requests <-chan *ssh.Request, connectionDone <-chan struct{}) {
	for request := range requests {
		if request.Type != "exec" {
			_ = request.Reply(false, nil)
			continue
		}
		var payload struct {
			Command string
		}
		if err := ssh.Unmarshal(request.Payload, &payload); err != nil {
			_ = request.Reply(false, nil)
			return
		}
		if err := request.Reply(true, nil); err != nil {
			return
		}
		select {
		case server.commands <- payload.Command:
		case <-connectionDone:
			return
		}

		behaviorDone := make(chan struct{})
		go func() {
			defer close(behaviorDone)
			server.behavior(payload.Command, channel, connectionDone)
		}()
		for {
			select {
			case <-behaviorDone:
				return
			case request, ok := <-requests:
				if !ok {
					<-behaviorDone
					return
				}
				// The production runner intentionally tolerates a remote command
				// that ignores SIGTERM. Keep draining requests so transport close
				// remains the only lifecycle boundary under test.
				_ = request.Reply(false, nil)
			}
		}
	}
}

func (server *dockerSSHTestServer) close(t testing.TB) {
	t.Helper()
	_ = server.listener.Close()
	server.connectionsMu.Lock()
	for connection := range server.connections {
		_ = connection.Close()
	}
	server.connectionsMu.Unlock()

	done := make(chan struct{})
	go func() {
		server.workers.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Log("Docker SSH test server workers did not stop within 2s")
	}
}

func dialDockerSSHTestClient(t *testing.T, server *dockerSSHTestServer) *ssh.Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	client, err := sshutil.DialSSH(ctx, server.addr, "docker-test", []ssh.AuthMethod{ssh.Password(server.password)}, ssh.InsecureIgnoreHostKey())
	if err != nil {
		t.Fatalf("dial Docker SSH test server: %v", err)
	}
	return client
}

func waitDockerSSHCommand(t *testing.T, server *dockerSSHTestServer) string {
	t.Helper()
	select {
	case command := <-server.commands:
		return command
	case <-time.After(2 * time.Second):
		t.Fatal("Docker SSH test server did not receive an exec request")
		return ""
	}
}

func waitDockerSSHConnectionClosed(t *testing.T, server *dockerSSHTestServer) {
	t.Helper()
	select {
	case <-server.connectionClosed:
	case <-time.After(2 * time.Second):
		t.Fatal("owned Docker SSH transport did not close")
	}
}

func isDockerVolumeListCommand(command string) bool {
	return strings.Contains(command, "'volume' 'ls'")
}

func isDockerVolumeInspectCommand(command string) bool {
	return strings.Contains(command, "'volume' 'inspect'")
}

func writeDockerSSHResult(channel ssh.Channel, output []byte, exitCode uint32) {
	if len(output) > 0 {
		if _, err := channel.Write(output); err != nil {
			_ = channel.Close()
			return
		}
	}
	_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ ExitStatus uint32 }{ExitStatus: exitCode}))
	_ = channel.CloseWrite()
	_ = channel.Close()
}

func seedDockerSSHHandlerNode(t *testing.T, db *gorm.DB, server *dockerSSHTestServer) model.Node {
	t.Helper()
	host, portText, err := net.SplitHostPort(server.addr)
	if err != nil {
		t.Fatalf("split Docker SSH test address: %v", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("parse Docker SSH test port: %v", err)
	}
	node := model.Node{
		Name:      "docker-ssh-test-node",
		Host:      host,
		Port:      port,
		Username:  "docker-test",
		AuthType:  "password",
		Password:  server.password,
		BackupDir: "docker-ssh-test-backup",
		Status:    "online",
	}
	if err := db.Session(&gorm.Session{SkipHooks: true}).Create(&node).Error; err != nil {
		t.Fatalf("seed Docker SSH test node: %v", err)
	}
	return node
}

func runDockerHandlerRequest(t *testing.T, handler *DockerHandler, node model.Node, requestContext context.Context) (*httptest.ResponseRecorder, <-chan struct{}) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	response := httptest.NewRecorder()
	contextForRequest, _ := gin.CreateTestContext(response)
	contextForRequest.Params = gin.Params{{Key: "id", Value: strconv.FormatUint(uint64(node.ID), 10)}}
	contextForRequest.Set("userID", uint(1))
	contextForRequest.Set("username", "docker-test")
	contextForRequest.Set("role", "operator")
	request := httptest.NewRequest(http.MethodGet, "/nodes/"+strconv.FormatUint(uint64(node.ID), 10)+"/docker-volumes", nil).WithContext(requestContext)
	contextForRequest.Request = request
	done := make(chan struct{})
	go func() {
		handler.ListVolumes(contextForRequest)
		close(done)
	}()
	return response, done
}

func TestListDockerVolumesHandlerCancellationClosesOwnedSSHTransport(t *testing.T) {
	server := startDockerSSHTestServer(t, func(command string, channel ssh.Channel, connectionDone <-chan struct{}) {
		if isDockerVolumeListCommand(command) {
			<-connectionDone
			_ = channel.Close()
			return
		}
		writeDockerSSHResult(channel, nil, 1)
	})

	db := openDockerHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Node{}); err != nil {
		t.Fatalf("migrate Docker SSH test node: %v", err)
	}
	node := seedDockerSSHHandlerNode(t, db, server)
	handler := NewDockerHandler(db)
	requestContext, cancel := context.WithCancel(context.Background())
	defer cancel()
	response, done := runDockerHandlerRequest(t, handler, node, requestContext)
	command := waitDockerSSHCommand(t, server)
	if !isDockerVolumeListCommand(command) {
		t.Fatalf("first Docker command=%q, want volume list", command)
	}

	started := time.Now()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Docker handler did not return promptly after request cancellation")
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("canceled Docker handler elapsed=%s, want <2s", elapsed)
	}
	if response.Code != http.StatusBadGateway {
		t.Fatalf("canceled Docker handler status=%d body=%s, want 502", response.Code, response.Body.String())
	}
	waitDockerSSHConnectionClosed(t, server)
}

func TestListDockerVolumesHandlerDeadlineClosesOwnedSSHTransportDuringInspect(t *testing.T) {
	server := startDockerSSHTestServer(t, func(command string, channel ssh.Channel, connectionDone <-chan struct{}) {
		switch {
		case isDockerVolumeListCommand(command):
			writeDockerSSHResult(channel, []byte(`{"Name":"stalled-inspect","Driver":"local","Mountpoint":""}`+"\n"), 0)
		case isDockerVolumeInspectCommand(command):
			<-connectionDone
			_ = channel.Close()
		default:
			writeDockerSSHResult(channel, nil, 1)
		}
	})

	db := openDockerHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Node{}); err != nil {
		t.Fatalf("migrate Docker SSH test node: %v", err)
	}
	node := seedDockerSSHHandlerNode(t, db, server)
	handler := NewDockerHandler(db)
	requestContext, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	response, done := runDockerHandlerRequest(t, handler, node, requestContext)
	command := waitDockerSSHCommand(t, server)
	if !isDockerVolumeListCommand(command) {
		t.Fatalf("first Docker command=%q, want volume list", command)
	}
	command = waitDockerSSHCommand(t, server)
	if !isDockerVolumeInspectCommand(command) {
		t.Fatalf("second Docker command=%q, want volume inspect", command)
	}

	started := time.Now()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Docker handler did not return promptly after total deadline")
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("deadline Docker handler elapsed=%s, want <2s", elapsed)
	}
	if response.Code != http.StatusBadGateway {
		t.Fatalf("deadline Docker handler status=%d body=%s, want 502", response.Code, response.Body.String())
	}
	waitDockerSSHConnectionClosed(t, server)
}

func TestListDockerVolumesHandlerBoundsOversizedOutputAndClosesOwnedSSHTransport(t *testing.T) {
	server := startDockerSSHTestServer(t, func(command string, channel ssh.Channel, connectionDone <-chan struct{}) {
		if !isDockerVolumeListCommand(command) {
			writeDockerSSHResult(channel, nil, 1)
			return
		}
		line := []byte(`{"Name":"oversized","Driver":"local","Mountpoint":""}` + "\n")
		output := bytes.Repeat(line, int(dockerVolumeMaxStdoutBytes/int64(len(line)))+2)
		writeDockerSSHResult(channel, output, 0)
	})

	db := openDockerHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Node{}); err != nil {
		t.Fatalf("migrate Docker SSH test node: %v", err)
	}
	node := seedDockerSSHHandlerNode(t, db, server)
	handler := NewDockerHandler(db)
	response, done := runDockerHandlerRequest(t, handler, node, context.Background())
	command := waitDockerSSHCommand(t, server)
	if !isDockerVolumeListCommand(command) {
		t.Fatalf("Docker command=%q, want volume list", command)
	}

	started := time.Now()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Docker handler did not return promptly after oversized output")
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("oversized Docker handler elapsed=%s, want <2s", elapsed)
	}
	if response.Code != http.StatusBadGateway {
		t.Fatalf("oversized Docker handler status=%d body=%s, want 502", response.Code, response.Body.String())
	}
	waitDockerSSHConnectionClosed(t, server)
}

func TestListDockerVolumesSSHClientReturnsPartialForKnownInspectResult(t *testing.T) {
	for _, test := range []struct {
		name            string
		inspectOutput   []byte
		inspectExit     uint32
		wantFirstMount  string
		wantSecondMount string
	}{
		{
			name:            "known-nonzero",
			inspectExit:     1,
			wantFirstMount:  "",
			wantSecondMount: "",
		},
		{
			name:            "known-success-with-gap",
			inspectOutput:   []byte(`{"Name":"first","Driver":"local","Mountpoint":"/var/lib/docker/volumes/first/_data"}` + "\n"),
			inspectExit:     0,
			wantFirstMount:  "/var/lib/docker/volumes/first/_data",
			wantSecondMount: "",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := startDockerSSHTestServer(t, func(command string, channel ssh.Channel, connectionDone <-chan struct{}) {
				switch {
				case isDockerVolumeListCommand(command):
					output := []byte(`{"Name":"first","Driver":"local","Mountpoint":""}` + "\n" + `{"Name":"second","Driver":"local","Mountpoint":""}` + "\n")
					writeDockerSSHResult(channel, output, 0)
				case isDockerVolumeInspectCommand(command):
					writeDockerSSHResult(channel, test.inspectOutput, test.inspectExit)
				default:
					writeDockerSSHResult(channel, nil, 1)
				}
			})
			client := dialDockerSSHTestClient(t, server)
			defer func() { _ = client.Close() }()
			runner := sshutil.NewSSHCommandRunnerWithTransportClose(client, 1)

			volumes, warning, partial, err := listDockerVolumes(context.Background(), runner)
			if err != nil {
				t.Fatalf("known inspect result: %v", err)
			}
			if !partial || warning == "" {
				t.Fatalf("known inspect result warning=%q partial=%v, want partial warning", warning, partial)
			}
			if len(volumes) != 2 || volumes[0].Mountpoint != test.wantFirstMount || volumes[1].Mountpoint != test.wantSecondMount {
				t.Fatalf("known inspect volumes=%+v, want first mount=%q second mount=%q", volumes, test.wantFirstMount, test.wantSecondMount)
			}
		})
	}
}
