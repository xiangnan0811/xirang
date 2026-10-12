package executor

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"xirang/backend/internal/model"
	"xirang/backend/internal/sshutil"

	"golang.org/x/crypto/ssh"
)

type commandExecutorSSHTestBehavior func(command string, channel ssh.Channel, closeConnection func())

type commandExecutorSSHTestServer struct {
	listener   net.Listener
	config     *ssh.ServerConfig
	user       string
	userPublic ssh.PublicKey
	node       model.Node
	behavior   commandExecutorSSHTestBehavior
	rejectExec bool

	connectionsMu sync.Mutex
	connections   map[net.Conn]struct{}
	workers       sync.WaitGroup
}

func startCommandExecutorSSHTestServer(t *testing.T, rejectExec bool, behavior commandExecutorSSHTestBehavior) *commandExecutorSSHTestServer {
	t.Helper()
	t.Setenv("SSH_STRICT_HOST_KEY_CHECKING", "false")
	t.Setenv("SSH_AUTO_ACCEPT_NEW_HOSTS", "false")

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听 SSH 测试服务失败: %v", err)
	}

	_, hostPrivateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		_ = listener.Close()
		t.Fatalf("生成 SSH 主机密钥失败: %v", err)
	}
	hostSigner, err := ssh.NewSignerFromKey(hostPrivateKey)
	if err != nil {
		_ = listener.Close()
		t.Fatalf("加载 SSH 主机密钥失败: %v", err)
	}

	_, userPrivateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		_ = listener.Close()
		t.Fatalf("生成 SSH 用户密钥失败: %v", err)
	}
	userSigner, err := ssh.NewSignerFromKey(userPrivateKey)
	if err != nil {
		_ = listener.Close()
		t.Fatalf("加载 SSH 用户密钥失败: %v", err)
	}
	privateKeyBlock, err := ssh.MarshalPrivateKey(userPrivateKey, "command-executor-test")
	if err != nil {
		_ = listener.Close()
		t.Fatalf("编码 SSH 用户私钥失败: %v", err)
	}
	privateKey := string(pem.EncodeToMemory(privateKeyBlock))

	host, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		_ = listener.Close()
		t.Fatalf("解析 SSH 测试服务地址失败: %v", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		_ = listener.Close()
		t.Fatalf("解析 SSH 测试服务端口失败: %v", err)
	}

	server := &commandExecutorSSHTestServer{
		listener:   listener,
		user:       "executor-test",
		userPublic: userSigner.PublicKey(),
		node: model.Node{
			ID:         1001,
			Host:       host,
			Port:       port,
			Username:   "executor-test",
			AuthType:   "key",
			PrivateKey: privateKey,
		},
		behavior:    behavior,
		rejectExec:  rejectExec,
		connections: make(map[net.Conn]struct{}),
	}
	server.config = &ssh.ServerConfig{
		PublicKeyCallback: func(metadata ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if metadata.User() != server.user || !bytes.Equal(key.Marshal(), server.userPublic.Marshal()) {
				return nil, fmt.Errorf("SSH 测试服务拒绝用户认证")
			}
			return nil, nil
		},
	}
	server.config.AddHostKey(hostSigner)

	server.workers.Add(1)
	go server.acceptLoop()
	t.Cleanup(func() { server.close() })
	return server
}

func (s *commandExecutorSSHTestServer) acceptLoop() {
	defer s.workers.Done()
	for {
		rawConn, err := s.listener.Accept()
		if err != nil {
			return
		}
		s.connectionsMu.Lock()
		s.connections[rawConn] = struct{}{}
		s.connectionsMu.Unlock()
		s.workers.Add(1)
		go func() {
			defer s.workers.Done()
			defer func() {
				s.connectionsMu.Lock()
				delete(s.connections, rawConn)
				s.connectionsMu.Unlock()
			}()
			s.serveConnection(rawConn)
		}()
	}
}

func (s *commandExecutorSSHTestServer) serveConnection(rawConn net.Conn) {
	serverConn, channels, requests, err := ssh.NewServerConn(rawConn, s.config)
	if err != nil {
		return
	}
	defer func() { _ = serverConn.Close() }()
	go ssh.DiscardRequests(requests)

	for newChannel := range channels {
		if newChannel.ChannelType() != "session" {
			_ = newChannel.Reject(ssh.UnknownChannelType, "session required")
			continue
		}
		channel, channelRequests, err := newChannel.Accept()
		if err != nil {
			continue
		}
		s.serveSession(channel, channelRequests, rawConn)
	}
}

func (s *commandExecutorSSHTestServer) serveSession(channel ssh.Channel, requests <-chan *ssh.Request, rawConn net.Conn) {
	defer func() { _ = channel.Close() }()
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
		if s.rejectExec {
			_ = request.Reply(false, nil)
			return
		}
		if err := request.Reply(true, nil); err != nil {
			return
		}
		if s.behavior != nil {
			s.behavior(payload.Command, channel, func() { _ = rawConn.Close() })
		}
		return
	}
}

func (s *commandExecutorSSHTestServer) close() {
	_ = s.listener.Close()
	s.connectionsMu.Lock()
	connections := make([]net.Conn, 0, len(s.connections))
	for connection := range s.connections {
		connections = append(connections, connection)
	}
	s.connectionsMu.Unlock()
	for _, connection := range connections {
		_ = connection.Close()
	}
	s.workers.Wait()
}

func (s *commandExecutorSSHTestServer) taskNode() model.Node {
	return s.node
}

func sendCommandExecutorExitStatus(channel ssh.Channel, status uint32) {
	payload := struct {
		Status uint32
	}{Status: status}
	_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(&payload))
	_ = channel.Close()
}

func sendCommandExecutorExitSignal(channel ssh.Channel, signal string) {
	payload := struct {
		Signal   string
		CoreDump bool
		Error    string
		Lang     string
	}{Signal: signal}
	_, _ = channel.SendRequest("exit-signal", false, ssh.Marshal(&payload))
	_ = channel.Close()
}

func noExecutorLogs(string, string) {}

func TestCommandExecutorKnownExitFailureUsesCommandExitError(t *testing.T) {
	server := startCommandExecutorSSHTestServer(t, false, func(command string, channel ssh.Channel, _ func()) {
		if command != "printf known-failure" {
			t.Errorf("服务端收到命令 %q，期望原始命令", command)
		}
		sendCommandExecutorExitStatus(channel, 7)
	})

	exitCode, err := (&CommandExecutor{}).Run(context.Background(), model.Task{
		Command: "printf known-failure",
		Node:    server.taskNode(),
	}, noExecutorLogs, nil)
	if exitCode != 7 {
		t.Fatalf("退出码=%d，期望 7", exitCode)
	}
	var commandErr *CommandExitError
	if !errors.As(err, &commandErr) {
		t.Fatalf("错误=%v，期望 CommandExitError", err)
	}
	if commandErr.ExitCode != 7 {
		t.Fatalf("CommandExitError.ExitCode=%d，期望 7", commandErr.ExitCode)
	}
	var sshExitErr *ssh.ExitError
	if !errors.As(err, &sshExitErr) {
		t.Fatalf("错误链=%v，期望保留 ssh.ExitError", err)
	}
	if sshExitErr.Signal() != "" || sshExitErr.ExitStatus() != 7 {
		t.Fatalf("底层退出结果 signal=%q status=%d", sshExitErr.Signal(), sshExitErr.ExitStatus())
	}
}

func TestCommandExecutorUsesBatchCommandPurpose(t *testing.T) {
	received := make(chan string, 1)
	server := startCommandExecutorSSHTestServer(t, false, func(command string, channel ssh.Channel, _ func()) {
		received <- command
		sendCommandExecutorExitStatus(channel, 0)
	})

	node := server.taskNode()
	keyID := uint(73)
	privateKey := node.PrivateKey
	node.PrivateKey = ""
	node.SSHKeyID = &keyID
	node.SSHKey = &model.SSHKey{
		ID:              keyID,
		Name:            "command-executor-test",
		PrivateKey:      privateKey,
		AllowedPurposes: sshutil.PurposeBatchCommand,
		AllowedNodeIDs:  strconv.FormatUint(uint64(node.ID), 10),
	}

	ordinaryExitCode, ordinaryErr := (&CommandExecutor{}).Run(context.Background(), model.Task{
		Command: "ordinary command",
		Source:  "manual",
		Node:    node,
	}, noExecutorLogs, nil)
	if ordinaryExitCode != -1 {
		t.Fatalf("普通命令退出码=%d，期望 -1", ordinaryExitCode)
	}
	var noStartErr *NoProcessStartError
	if !errors.As(ordinaryErr, &noStartErr) {
		t.Fatalf("普通命令错误=%v，期望用途校验失败被标记为 NoProcessStartError", ordinaryErr)
	}

	batchExitCode, batchErr := (&CommandExecutor{}).Run(context.Background(), model.Task{
		Command: "batch command",
		Source:  "batch",
		Node:    node,
	}, noExecutorLogs, nil)
	if batchErr != nil || batchExitCode != 0 {
		t.Fatalf("批量命令退出码=%d 错误=%v，期望成功", batchExitCode, batchErr)
	}
	select {
	case command := <-received:
		if command != "batch command" {
			t.Fatalf("批量命令服务端收到 %q，期望原始命令", command)
		}
	default:
		t.Fatal("批量命令未到达真实 SSH 服务端")
	}
}

func TestCommandExecutorPreStartFailuresUseNoProcessStartError(t *testing.T) {
	tests := []struct {
		name string
		task model.Task
	}{
		{
			name: "empty command",
			task: model.Task{Command: "   "},
		},
		{
			name: "empty node host",
			task: model.Task{Command: "true", Node: model.Node{AuthType: "key"}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			exitCode, err := (&CommandExecutor{}).Run(context.Background(), test.task, noExecutorLogs, nil)
			if exitCode != -1 {
				t.Fatalf("退出码=%d，期望 -1", exitCode)
			}
			var noStartErr *NoProcessStartError
			if !errors.As(err, &noStartErr) {
				t.Fatalf("错误=%v，期望 NoProcessStartError", err)
			}
		})
	}

	server := startCommandExecutorSSHTestServer(t, false, nil)
	_ = server.listener.Close()
	exitCode, err := (&CommandExecutor{}).Run(context.Background(), model.Task{
		Command: "dial failure",
		Node:    server.taskNode(),
	}, noExecutorLogs, nil)
	if exitCode != -1 {
		t.Fatalf("连接失败退出码=%d，期望 -1", exitCode)
	}
	var noStartErr *NoProcessStartError
	if !errors.As(err, &noStartErr) {
		t.Fatalf("连接失败错误=%v，期望 NoProcessStartError", err)
	}
}

func TestCommandExecutorStartFailureIsNotNoProcessStart(t *testing.T) {
	server := startCommandExecutorSSHTestServer(t, true, nil)
	exitCode, err := (&CommandExecutor{}).Run(context.Background(), model.Task{
		Command: "rejected by server",
		Node:    server.taskNode(),
	}, noExecutorLogs, nil)
	if exitCode != -1 {
		t.Fatalf("启动失败退出码=%d，期望 -1", exitCode)
	}
	if err == nil {
		t.Fatal("启动失败应返回错误")
	}
	var noStartErr *NoProcessStartError
	if errors.As(err, &noStartErr) {
		t.Fatalf("启动失败错误=%v，不应标记 NoProcessStartError", err)
	}
	var commandErr *CommandExitError
	if errors.As(err, &commandErr) {
		t.Fatalf("启动失败错误=%v，不应标记 CommandExitError", err)
	}
}

func TestCommandExecutorUnknownProtocolOutcomesRemainUnclassified(t *testing.T) {
	tests := []struct {
		name     string
		behavior commandExecutorSSHTestBehavior
		check    func(*testing.T, error)
	}{
		{
			name: "no exit status",
			behavior: func(_ string, channel ssh.Channel, _ func()) {
				_ = channel.Close()
			},
		},
		{
			name: "signal",
			behavior: func(_ string, channel ssh.Channel, _ func()) {
				sendCommandExecutorExitSignal(channel, "TERM")
			},
			check: func(t *testing.T, err error) {
				var sshExitErr *ssh.ExitError
				if !errors.As(err, &sshExitErr) {
					t.Fatalf("错误链=%v，期望 ssh.ExitError", err)
				}
				if sshExitErr.Signal() != "TERM" {
					t.Fatalf("信号=%q，期望 TERM", sshExitErr.Signal())
				}
			},
		},
		{
			name: "transport close",
			behavior: func(_ string, _ ssh.Channel, closeConnection func()) {
				closeConnection()
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := startCommandExecutorSSHTestServer(t, false, test.behavior)
			exitCode, err := (&CommandExecutor{}).Run(context.Background(), model.Task{
				Command: "unknown outcome",
				Node:    server.taskNode(),
			}, noExecutorLogs, nil)
			if exitCode != -1 {
				t.Fatalf("退出码=%d，期望 -1", exitCode)
			}
			if err == nil {
				t.Fatal("未知协议结果应返回错误")
			}
			var commandErr *CommandExitError
			if errors.As(err, &commandErr) {
				t.Fatalf("错误=%v，不应标记 CommandExitError", err)
			}
			if test.check != nil {
				test.check(t, err)
			}
		})
	}
}

func TestCommandExecutorCancellationAndTimeoutRemainUnclassified(t *testing.T) {
	tests := []struct {
		name       string
		newContext func() (context.Context, func(error))
		wantErr    error
	}{
		{
			name: "cancellation",
			newContext: func() (context.Context, func(error)) {
				ctx, cancel := context.WithCancel(context.Background())
				return ctx, func(error) { cancel() }
			},
			wantErr: context.Canceled,
		},
		{
			name: "timeout",
			newContext: func() (context.Context, func(error)) {
				ctx := newCommandExecutorControlledContext()
				return ctx, ctx.expire
			},
			wantErr: context.DeadlineExceeded,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			started := make(chan struct{})
			release := make(chan struct{})
			server := startCommandExecutorSSHTestServer(t, false, func(_ string, _ ssh.Channel, closeConnection func()) {
				close(started)
				<-release
				closeConnection()
			})
			ctx, expire := test.newContext()
			result := make(chan struct {
				exitCode int
				err      error
			}, 1)
			go func() {
				exitCode, err := (&CommandExecutor{}).Run(ctx, model.Task{
					Command: "cancellable command",
					Node:    server.taskNode(),
				}, noExecutorLogs, nil)
				result <- struct {
					exitCode int
					err      error
				}{exitCode: exitCode, err: err}
			}()

			select {
			case <-started:
			case <-time.After(2 * time.Second):
				t.Fatal("真实 SSH 服务端未收到命令")
			}
			expire(test.wantErr)
			close(release)

			select {
			case outcome := <-result:
				if outcome.exitCode != -1 {
					t.Fatalf("退出码=%d，期望 -1", outcome.exitCode)
				}
				if !errors.Is(outcome.err, test.wantErr) {
					t.Fatalf("错误=%v，期望 errors.Is(..., %v)", outcome.err, test.wantErr)
				}
				var commandErr *CommandExitError
				if errors.As(outcome.err, &commandErr) {
					t.Fatalf("错误=%v，不应标记 CommandExitError", outcome.err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("取消/超时后命令未在有界时间内结束")
			}
		})
	}
}

type commandExecutorControlledContext struct {
	done chan struct{}

	mu  sync.RWMutex
	err error
}

func newCommandExecutorControlledContext() *commandExecutorControlledContext {
	return &commandExecutorControlledContext{done: make(chan struct{})}
}

func (c *commandExecutorControlledContext) Deadline() (time.Time, bool) {
	return time.Time{}, false
}

func (c *commandExecutorControlledContext) Done() <-chan struct{} {
	return c.done
}

func (c *commandExecutorControlledContext) Err() error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.err
}

func (c *commandExecutorControlledContext) Value(key any) any {
	return context.Background().Value(key)
}

func (c *commandExecutorControlledContext) expire(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return
	}
	c.err = err
	close(c.done)
}
