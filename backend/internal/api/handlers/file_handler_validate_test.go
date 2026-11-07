package handlers

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/credentialaudit"
	"xirang/backend/internal/fileaccess"
	"xirang/backend/internal/logger"
	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"
	"xirang/backend/internal/sshutil"

	"github.com/gin-gonic/gin"
	"github.com/pkg/sftp"
	"github.com/rs/zerolog"
	"golang.org/x/crypto/ssh"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

const (
	fileBrowserTestSSHUser        = "file-browser-test"
	fileBrowserTestSSHPassword    = "FAKE_FILE_BROWSER_SSH_PASSWORD_FOR_TEST_ONLY"
	fileBrowserOutsideSecretBytes = "FAKE_OUTSIDE_SECRET_BYTES_FOR_TEST_ONLY"
	fileBrowserBufferedSecret     = "FAKE_BUFFERED_PREVIEW_SECRET_FOR_TEST_ONLY"
)

type fileInfoListerAt []os.FileInfo

func (l fileInfoListerAt) ListAt(ls []os.FileInfo, offset int64) (int, error) {
	if offset >= int64(len(l)) {
		return 0, io.EOF
	}
	n := copy(ls, l[offset:])
	if int(offset)+n >= len(l) {
		return n, io.EOF
	}
	return n, nil
}

type hookableReaderAt struct {
	data       []byte
	onComplete func()
	once       sync.Once
}

func (r *hookableReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off >= int64(len(r.data)) {
		if r.onComplete != nil {
			r.once.Do(r.onComplete)
		}
		return 0, io.EOF
	}
	n := copy(p, r.data[off:])
	if off+int64(n) >= int64(len(r.data)) {
		if r.onComplete != nil {
			r.once.Do(r.onComplete)
		}
		return n, io.EOF
	}
	return n, nil
}

// controlledSFTPFSHost 实现 pkg/sftp 的 FileReader、FileLister、LstatFileLister 与 RealPathFileLister：
//   - RealPath 走 filepath.EvalSymlinks，与生产环境 OpenSSH sftp-server 的 realpath(3) 语义一致；
//   - Lstat 走 os.Lstat，保留符号链接元数据供 fileaccess.SFTPTree 校验；
//   - Fileread 支持注入 onReadComplete 钩子，用于在 io.ReadFull 读入缓冲区后、handle.Close()
//     触发最后一次 postCheck 前确定性模拟远端文件被篡改或替换为越界软链接。
type controlledSFTPFSHost struct {
	mu             sync.Mutex
	onBeforeOpen   map[string]func()
	onAfterOpen    map[string]func()
	onReadComplete map[string]func()
}

func newControlledSFTPFSHost() *controlledSFTPFSHost {
	return &controlledSFTPFSHost{
		onBeforeOpen:   make(map[string]func()),
		onAfterOpen:    make(map[string]func()),
		onReadComplete: make(map[string]func()),
	}
}

func (h *controlledSFTPFSHost) setOnBeforeOpen(targetPath string, fn func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if fn == nil {
		delete(h.onBeforeOpen, filepath.Clean(targetPath))
		return
	}
	h.onBeforeOpen[filepath.Clean(targetPath)] = fn
}

func (h *controlledSFTPFSHost) setOnAfterOpen(targetPath string, fn func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if fn == nil {
		delete(h.onAfterOpen, filepath.Clean(targetPath))
		return
	}
	h.onAfterOpen[filepath.Clean(targetPath)] = fn
}

func (h *controlledSFTPFSHost) setOnReadComplete(targetPath string, fn func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if fn == nil {
		delete(h.onReadComplete, filepath.Clean(targetPath))
		return
	}
	h.onReadComplete[filepath.Clean(targetPath)] = fn
}

func (h *controlledSFTPFSHost) RealPath(p string) (string, error) {
	return filepath.EvalSymlinks(p)
}

func (h *controlledSFTPFSHost) Lstat(req *sftp.Request) (sftp.ListerAt, error) {
	info, err := os.Lstat(req.Filepath)
	if err != nil {
		return nil, err
	}
	return fileInfoListerAt{info}, nil
}

func (h *controlledSFTPFSHost) Filelist(req *sftp.Request) (sftp.ListerAt, error) {
	switch req.Method {
	case "List":
		entries, err := os.ReadDir(req.Filepath)
		if err != nil {
			return nil, err
		}
		infos := make([]os.FileInfo, 0, len(entries))
		for _, entry := range entries {
			info, infoErr := entry.Info()
			if infoErr != nil {
				return nil, infoErr
			}
			infos = append(infos, info)
		}
		return fileInfoListerAt(infos), nil
	case "Stat":
		info, err := os.Stat(req.Filepath)
		if err != nil {
			return nil, err
		}
		return fileInfoListerAt{info}, nil
	default:
		return nil, sftp.ErrSSHFxOpUnsupported
	}
}

func (h *controlledSFTPFSHost) Fileread(req *sftp.Request) (io.ReaderAt, error) {
	cleanPath := filepath.Clean(req.Filepath)
	resolvedPath := cleanPath
	if eval, evalErr := filepath.EvalSymlinks(cleanPath); evalErr == nil {
		resolvedPath = filepath.Clean(eval)
	}
	h.mu.Lock()
	beforeHook := h.onBeforeOpen[cleanPath]
	if beforeHook == nil {
		beforeHook = h.onBeforeOpen[resolvedPath]
	}
	afterHook := h.onAfterOpen[cleanPath]
	if afterHook == nil {
		afterHook = h.onAfterOpen[resolvedPath]
	}
	readHook := h.onReadComplete[cleanPath]
	if readHook == nil {
		readHook = h.onReadComplete[resolvedPath]
	}
	h.mu.Unlock()
	if beforeHook != nil {
		beforeHook()
	}
	data, err := os.ReadFile(req.Filepath)
	if err != nil {
		return nil, err
	}
	if afterHook != nil {
		afterHook()
	}
	return &hookableReaderAt{data: data, onComplete: readHook}, nil
}

func (h *controlledSFTPFSHost) Filewrite(*sftp.Request) (io.WriterAt, error) {
	return nil, sftp.ErrSSHFxPermissionDenied
}

func (h *controlledSFTPFSHost) Filecmd(*sftp.Request) error {
	return sftp.ErrSSHFxPermissionDenied
}

type fileBrowserSSHSFTPTestServer struct {
	addr          string
	listener      net.Listener
	config        *ssh.ServerConfig
	fsHost        *controlledSFTPFSHost
	authAttempts  atomic.Int32
	sftpSessions  atomic.Int32
	connectionsMu sync.Mutex
	connections   map[net.Conn]struct{}
	workers       sync.WaitGroup
}

func startFileBrowserSSHSFTPTestServer(t *testing.T) *fileBrowserSSHSFTPTestServer {
	t.Helper()
	t.Setenv("SSH_STRICT_HOST_KEY_CHECKING", "false")
	t.Setenv("SSH_AUTO_ACCEPT_NEW_HOSTS", "false")

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen SFTP SSH test server: %v", err)
	}
	_, hostPrivateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		_ = listener.Close()
		t.Fatalf("generate SFTP SSH host key: %v", err)
	}
	hostSigner, err := ssh.NewSignerFromKey(hostPrivateKey)
	if err != nil {
		_ = listener.Close()
		t.Fatalf("new SFTP SSH signer: %v", err)
	}

	server := &fileBrowserSSHSFTPTestServer{
		addr:        listener.Addr().String(),
		listener:    listener,
		fsHost:      newControlledSFTPFSHost(),
		connections: make(map[net.Conn]struct{}),
	}
	config := &ssh.ServerConfig{
		PasswordCallback: func(metadata ssh.ConnMetadata, supplied []byte) (*ssh.Permissions, error) {
			server.authAttempts.Add(1)
			if metadata.User() != fileBrowserTestSSHUser || string(supplied) != fileBrowserTestSSHPassword {
				return nil, fmt.Errorf("SFTP SSH test credentials rejected")
			}
			return nil, nil
		},
	}
	config.AddHostKey(hostSigner)
	server.config = config
	server.workers.Add(1)
	go server.acceptLoop()
	t.Cleanup(func() {
		server.close(t)
	})
	return server
}

func (s *fileBrowserSSHSFTPTestServer) acceptLoop() {
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

func (s *fileBrowserSSHSFTPTestServer) serveConnection(rawConn net.Conn) {
	serverConn, channels, requests, err := ssh.NewServerConn(rawConn, s.config)
	if err != nil {
		_ = rawConn.Close()
		return
	}
	defer func() { _ = serverConn.Close() }()
	go ssh.DiscardRequests(requests)

	var channelWorkers sync.WaitGroup
	for newChannel := range channels {
		if newChannel.ChannelType() != "session" {
			_ = newChannel.Reject(ssh.UnknownChannelType, "only session channels supported")
			continue
		}
		channel, channelRequests, err := newChannel.Accept()
		if err != nil {
			continue
		}
		channelWorkers.Add(1)
		go func() {
			defer channelWorkers.Done()
			s.serveSession(channel, channelRequests)
		}()
	}
	channelWorkers.Wait()
}

func (s *fileBrowserSSHSFTPTestServer) serveSession(channel ssh.Channel, requests <-chan *ssh.Request) {
	defer func() { _ = channel.Close() }()
	for req := range requests {
		switch req.Type {
		case "subsystem":
			var payload struct {
				Name string
			}
			if err := ssh.Unmarshal(req.Payload, &payload); err != nil || payload.Name != "sftp" {
				if req.WantReply {
					_ = req.Reply(false, nil)
				}
				continue
			}
			if req.WantReply {
				if err := req.Reply(true, nil); err != nil {
					return
				}
			}
			s.sftpSessions.Add(1)
			requestServer := sftp.NewRequestServer(channel, sftp.Handlers{
				FileGet:  s.fsHost,
				FilePut:  s.fsHost,
				FileCmd:  s.fsHost,
				FileList: s.fsHost,
			})
			_ = requestServer.Serve()
			_ = requestServer.Close()
			return
		case "exec":
			var payload struct {
				Command string
			}
			if err := ssh.Unmarshal(req.Payload, &payload); err != nil {
				if req.WantReply {
					_ = req.Reply(false, nil)
				}
				return
			}
			if req.WantReply {
				if err := req.Reply(true, nil); err != nil {
					return
				}
			}
			cmd := exec.Command("/bin/sh", "-c", payload.Command)
			cmd.Stdout = channel
			cmd.Stderr = channel.Stderr()
			err := cmd.Run()
			exitCode := uint32(0)
			if err != nil {
				if exitErr, ok := err.(*exec.ExitError); ok {
					exitCode = uint32(exitErr.ExitCode())
				} else {
					exitCode = 1
				}
			}
			_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{Status: exitCode}))
			return
		default:
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
		}
	}
}

func (s *fileBrowserSSHSFTPTestServer) close(t testing.TB) {
	t.Helper()
	_ = s.listener.Close()
	s.connectionsMu.Lock()
	for conn := range s.connections {
		_ = conn.Close()
	}
	s.connectionsMu.Unlock()

	done := make(chan struct{})
	go func() {
		s.workers.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Log("SFTP SSH test server workers did not stop within 2s")
	}
}

func newValidateTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+handlerTestDBName(t)+"?mode=memory&cache=shared&_loc=UTC"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.Node{}, &model.NodeOwner{}, &model.Task{}, &model.CredentialAuditEvent{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func seedFileBrowserTestNode(t *testing.T, db *gorm.DB, server *fileBrowserSSHSFTPTestServer, name, basePath string) model.Node {
	t.Helper()
	host, portText, err := net.SplitHostPort(server.addr)
	if err != nil {
		t.Fatalf("split SFTP server addr: %v", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("parse SFTP server port: %v", err)
	}
	node := model.Node{
		Name:      name,
		Host:      host,
		Port:      port,
		Username:  fileBrowserTestSSHUser,
		AuthType:  "password",
		Password:  fileBrowserTestSSHPassword,
		BasePath:  basePath,
		BackupDir: name + "-backup",
		Status:    "online",
	}
	if err := db.Session(&gorm.Session{SkipHooks: true}).Create(&node).Error; err != nil {
		t.Fatalf("seed file browser node %s: %v", name, err)
	}
	return node
}

type fileBrowserActor struct {
	userID   uint
	username string
	role     string
}

func newFileBrowserTestRouter(db *gorm.DB) *gin.Engine {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		if rawID := c.GetHeader("X-Test-User-ID"); rawID != "" {
			if parsed, err := strconv.ParseUint(rawID, 10, 64); err == nil {
				c.Set(middleware.CtxUserID, uint(parsed))
			}
		}
		if username := c.GetHeader("X-Test-Username"); username != "" {
			c.Set(middleware.CtxUsername, username)
		}
		if role := c.GetHeader("X-Test-Role"); role != "" {
			c.Set(middleware.CtxRole, role)
		}
		c.Next()
	})
	fileHandler := NewFileHandler(db)
	v1 := engine.Group("/api/v1")
	v1.GET("/nodes/:id/files", middleware.RBAC("nodes:files"), middleware.OwnershipNodeCheck(db), fileHandler.ListNodeFiles)
	v1.GET("/nodes/:id/files/content", middleware.RBAC("nodes:files"), middleware.OwnershipNodeCheck(db), fileHandler.GetNodeFileContent)
	return engine
}

func performNodeFileBrowserRequest(t *testing.T, router *gin.Engine, actor fileBrowserActor, endpoint string, nodeID uint, rawPath string) *httptest.ResponseRecorder {
	t.Helper()
	target := fmt.Sprintf("/api/v1/nodes/%d/%s", nodeID, strings.TrimPrefix(endpoint, "/"))
	if rawPath != "<omit>" {
		target += "?path=" + url.QueryEscape(rawPath)
	}
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.Header.Set("X-Test-User-ID", strconv.FormatUint(uint64(actor.userID), 10))
	req.Header.Set("X-Test-Username", actor.username)
	req.Header.Set("X-Test-Role", actor.role)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	return recorder
}

type fileListEnvelope struct {
	Code    int              `json:"code"`
	Message string           `json:"message"`
	Data    FileListResponse `json:"data"`
}

type fileContentEnvelope struct {
	Code    int                 `json:"code"`
	Message string              `json:"message"`
	Data    FileContentResponse `json:"data"`
}

// setupSymlinkTree 构造如下结构（root 末尾不带 /）：
//
//	<root>/
//	  file                       普通文件（内容 "hi"）
//	  sub/                       子目录
//	    nested.txt               普通文件（内容 "nested"）
//	  link_safe -> sub           指向 root 内的目录 symlink，应允许
//	  escape    -> <outside>     直接指向 root 外目录的 symlink，应拒绝
//	  nest/
//	    escape     -> <outside>/secret 嵌套位置指向 root 外文件的 symlink，应拒绝
//	    escape_dir -> <outside>        嵌套位置指向 root 外目录的 symlink，应拒绝
//
// 返回 root 绝对路径与 externalTarget 绝对路径（均已 EvalSymlinks）。
func setupSymlinkTree(t *testing.T) (root, externalTarget string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("symlink 构造在 Windows CI 上需要管理员权限，跳过")
	}
	tmp := t.TempDir()
	resolvedTmp, err := filepath.EvalSymlinks(tmp)
	if err != nil {
		t.Fatalf("eval tmp: %v", err)
	}
	root = filepath.Join(resolvedTmp, "safe")
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "nest"), 0o755); err != nil {
		t.Fatalf("mkdir nest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "file"), []byte("hi"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "nested.txt"), []byte("nested"), 0o644); err != nil {
		t.Fatalf("write nested: %v", err)
	}

	externalTarget = filepath.Join(resolvedTmp, "outside")
	if err := os.MkdirAll(externalTarget, 0o755); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}
	if err := os.WriteFile(filepath.Join(externalTarget, "secret"), []byte(fileBrowserOutsideSecretBytes), 0o644); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	if err := os.WriteFile(filepath.Join(externalTarget, "outside_only_entry.txt"), []byte("outside-dir-entry"), 0o644); err != nil {
		t.Fatalf("write outside_only_entry.txt: %v", err)
	}

	if err := os.Symlink(filepath.Join(root, "sub"), filepath.Join(root, "link_safe")); err != nil {
		t.Fatalf("symlink safe: %v", err)
	}
	if err := os.Symlink(externalTarget, filepath.Join(root, "escape")); err != nil {
		t.Fatalf("symlink escape: %v", err)
	}
	if err := os.Symlink(filepath.Join(externalTarget, "secret"), filepath.Join(root, "nest", "escape")); err != nil {
		t.Fatalf("symlink nested escape: %v", err)
	}
	if err := os.Symlink(externalTarget, filepath.Join(root, "nest", "escape_dir")); err != nil {
		t.Fatalf("symlink nested escape_dir: %v", err)
	}
	return root, externalTarget
}

// TestNodeFileBrowserHTTP_InRootPathsAndSafeSymlinksAllowed 沿真实 GET /api/v1/nodes/:id/files 和
// GET /api/v1/nodes/:id/files/content 路由（配受控 SSH+SFTP server）验证：
//  1. 节点 BasePath 下的合法目录、普通文件可正常列举与预览；
//  2. 节点 BasePath 内指向根内目标的目录/文件软链接（link_safe、link_safe/nested.txt）可用，且响应 Path 为解析后的真实路径；
//  3. 当节点 BasePath 自身为指向真实目录的软链接（alias -> realdata）时，通过 alias 访问目录与文件同样被允许。
func TestNodeFileBrowserHTTP_InRootPathsAndSafeSymlinksAllowed(t *testing.T) {
	root, _ := setupSymlinkTree(t)
	server := startFileBrowserSSHSFTPTestServer(t)
	db := newValidateTestDB(t)
	router := newFileBrowserTestRouter(db)
	admin := fileBrowserActor{userID: 1, username: "admin", role: "admin"}

	node := seedFileBrowserTestNode(t, db, server, "node-in-root", root)

	// 1) 列举根内子目录 sub
	subResp := performNodeFileBrowserRequest(t, router, admin, "files", node.ID, filepath.Join(root, "sub"))
	if subResp.Code != http.StatusOK {
		t.Fatalf("list in-root sub status=%d body=%s", subResp.Code, subResp.Body.String())
	}
	var listSub fileListEnvelope
	if err := json.Unmarshal(subResp.Body.Bytes(), &listSub); err != nil {
		t.Fatalf("decode sub list response: %v", err)
	}
	if listSub.Data.Path != filepath.Join(root, "sub") || len(listSub.Data.Entries) != 1 || listSub.Data.Entries[0].Name != "nested.txt" {
		t.Fatalf("unexpected sub list payload: %+v", listSub.Data)
	}

	// 2) 通过根内安全目录软链接 link_safe 列举目录
	safeDirResp := performNodeFileBrowserRequest(t, router, admin, "files", node.ID, filepath.Join(root, "link_safe"))
	if safeDirResp.Code != http.StatusOK {
		t.Fatalf("list safe symlink dir status=%d body=%s", safeDirResp.Code, safeDirResp.Body.String())
	}
	var listSafeDir fileListEnvelope
	if err := json.Unmarshal(safeDirResp.Body.Bytes(), &listSafeDir); err != nil {
		t.Fatalf("decode safe symlink list response: %v", err)
	}
	if listSafeDir.Data.Path != filepath.Join(root, "sub") || len(listSafeDir.Data.Entries) != 1 || listSafeDir.Data.Entries[0].Name != "nested.txt" {
		t.Fatalf("unexpected safe symlink dir list payload: %+v", listSafeDir.Data)
	}

	// 3) 预览根内普通文件 file
	fileResp := performNodeFileBrowserRequest(t, router, admin, "files/content", node.ID, filepath.Join(root, "file"))
	if fileResp.Code != http.StatusOK {
		t.Fatalf("preview clean file status=%d body=%s", fileResp.Code, fileResp.Body.String())
	}
	var contentFile fileContentEnvelope
	if err := json.Unmarshal(fileResp.Body.Bytes(), &contentFile); err != nil {
		t.Fatalf("decode clean file content: %v", err)
	}
	if contentFile.Data.Path != filepath.Join(root, "file") || contentFile.Data.Content != "hi" || contentFile.Data.Size != 2 || contentFile.Data.Truncated {
		t.Fatalf("unexpected clean file content payload: %+v", contentFile.Data)
	}

	// 4) 通过根内安全软链接路径 link_safe/nested.txt 预览文件
	safeFileResp := performNodeFileBrowserRequest(t, router, admin, "files/content", node.ID, filepath.Join(root, "link_safe", "nested.txt"))
	if safeFileResp.Code != http.StatusOK {
		t.Fatalf("preview safe symlink file status=%d body=%s", safeFileResp.Code, safeFileResp.Body.String())
	}
	var contentSafeFile fileContentEnvelope
	if err := json.Unmarshal(safeFileResp.Body.Bytes(), &contentSafeFile); err != nil {
		t.Fatalf("decode safe symlink file content: %v", err)
	}
	wantNestedPath := filepath.Join(root, "sub", "nested.txt")
	if contentSafeFile.Data.Path != wantNestedPath || contentSafeFile.Data.Content != "nested" || contentSafeFile.Data.Size != 6 {
		t.Fatalf("unexpected safe symlink file content payload: %+v (want path %q)", contentSafeFile.Data, wantNestedPath)
	}

	// 5) BasePath 自身是软链接（alias -> realdata）
	tmp := t.TempDir()
	resolvedTmp, err := filepath.EvalSymlinks(tmp)
	if err != nil {
		t.Fatalf("eval alias tmp: %v", err)
	}
	realData := filepath.Join(resolvedTmp, "realdata")
	if err := os.MkdirAll(realData, 0o755); err != nil {
		t.Fatalf("mkdir realdata: %v", err)
	}
	if err := os.WriteFile(filepath.Join(realData, "x.txt"), []byte("alias-ok"), 0o644); err != nil {
		t.Fatalf("write x.txt: %v", err)
	}
	alias := filepath.Join(resolvedTmp, "alias")
	if err := os.Symlink(realData, alias); err != nil {
		t.Fatalf("symlink alias: %v", err)
	}
	aliasNode := seedFileBrowserTestNode(t, db, server, "node-alias-root", alias)

	aliasListResp := performNodeFileBrowserRequest(t, router, admin, "files", aliasNode.ID, alias)
	if aliasListResp.Code != http.StatusOK {
		t.Fatalf("list alias root status=%d body=%s", aliasListResp.Code, aliasListResp.Body.String())
	}
	var aliasList fileListEnvelope
	if err := json.Unmarshal(aliasListResp.Body.Bytes(), &aliasList); err != nil {
		t.Fatalf("decode alias list: %v", err)
	}
	if aliasList.Data.Path != realData || len(aliasList.Data.Entries) != 1 || aliasList.Data.Entries[0].Name != "x.txt" {
		t.Fatalf("unexpected alias list payload: %+v", aliasList.Data)
	}

	aliasContentResp := performNodeFileBrowserRequest(t, router, admin, "files/content", aliasNode.ID, filepath.Join(alias, "x.txt"))
	if aliasContentResp.Code != http.StatusOK {
		t.Fatalf("preview alias file status=%d body=%s", aliasContentResp.Code, aliasContentResp.Body.String())
	}
	var aliasContent fileContentEnvelope
	if err := json.Unmarshal(aliasContentResp.Body.Bytes(), &aliasContent); err != nil {
		t.Fatalf("decode alias content: %v", err)
	}
	if aliasContent.Data.Path != filepath.Join(realData, "x.txt") || aliasContent.Data.Content != "alias-ok" {
		t.Fatalf("unexpected alias content payload: %+v", aliasContent.Data)
	}

	// 6) 根内以双点 ".." 开头但并非父目录遍历组件的合法文件与子目录（如 ..config、..data/key.txt）可正常预览
	dotDotFile := filepath.Join(root, "..config")
	if err := os.WriteFile(dotDotFile, []byte("dotdot-file-ok"), 0o644); err != nil {
		t.Fatalf("write ..config: %v", err)
	}
	dotDotDir := filepath.Join(root, "..data")
	if err := os.MkdirAll(dotDotDir, 0o755); err != nil {
		t.Fatalf("mkdir ..data: %v", err)
	}
	dotDotNestedFile := filepath.Join(dotDotDir, "key.txt")
	if err := os.WriteFile(dotDotNestedFile, []byte("dotdot-nested-ok"), 0o644); err != nil {
		t.Fatalf("write ..data/key.txt: %v", err)
	}
	for _, tc := range []struct {
		path    string
		content string
	}{
		{path: dotDotFile, content: "dotdot-file-ok"},
		{path: dotDotNestedFile, content: "dotdot-nested-ok"},
	} {
		resp := performNodeFileBrowserRequest(t, router, admin, "files/content", node.ID, tc.path)
		if resp.Code != http.StatusOK {
			t.Fatalf("preview %s status=%d body=%s", tc.path, resp.Code, resp.Body.String())
		}
		var env fileContentEnvelope
		if err := json.Unmarshal(resp.Body.Bytes(), &env); err != nil {
			t.Fatalf("decode %s content: %v", tc.path, err)
		}
		if env.Data.Path != tc.path || env.Data.Content != tc.content {
			t.Fatalf("unexpected %s content payload: %+v", tc.path, env.Data)
		}
	}
}

// TestNodeFileBrowserHTTP_DirectAndNestedEscapesRejectedWithoutLeakingEntriesOrBytes 沿真实
// GET /api/v1/nodes/:id/files 和 GET /api/v1/nodes/:id/files/content 验证：
//  1. 词法越界、直接指向根外的软链接（escape、escape/secret）与嵌套指向根外的软链接（nest/escape_dir、nest/escape）均返回 403；
//  2. 响应体不带出任何越界目录项名称（outside_only_entry.txt、secret）、越界文件字节（FAKE_OUTSIDE_SECRET_BYTES_FOR_TEST_ONLY）或解析后的外部真实路径；
//  3. 不存在的路径返回 403 "路径不存在或不可访问"，不误报为越界。
func TestNodeFileBrowserHTTP_DirectAndNestedEscapesRejectedWithoutLeakingEntriesOrBytes(t *testing.T) {
	root, externalTarget := setupSymlinkTree(t)
	server := startFileBrowserSSHSFTPTestServer(t)
	db := newValidateTestDB(t)
	router := newFileBrowserTestRouter(db)
	admin := fileBrowserActor{userID: 1, username: "admin", role: "admin"}

	node := seedFileBrowserTestNode(t, db, server, "node-escape-guard", root)

	escapeDirCases := []struct {
		name string
		path string
	}{
		{name: "direct_outside_path", path: externalTarget},
		{name: "lexical_dotdot_escape", path: filepath.Join(root, "..", "outside")},
		{name: "direct_escape_symlink_dir", path: filepath.Join(root, "escape")},
		{name: "nested_escape_symlink_dir", path: filepath.Join(root, "nest", "escape_dir")},
	}
	for _, tc := range escapeDirCases {
		t.Run("list_"+tc.name, func(t *testing.T) {
			resp := performNodeFileBrowserRequest(t, router, admin, "files", node.ID, tc.path)
			if resp.Code != http.StatusForbidden {
				t.Fatalf("expected 403 for %s, got %d body=%s", tc.name, resp.Code, resp.Body.String())
			}
			body := resp.Body.String()
			if !strings.Contains(body, "超出允许范围") {
				t.Fatalf("expected out-of-bounds message for %s, got %s", tc.name, body)
			}
			for _, forbidden := range []string{externalTarget, "outside_only_entry.txt", "secret", fileBrowserOutsideSecretBytes} {
				if strings.Contains(body, forbidden) {
					t.Fatalf("list escape response leaked %q in body: %s", forbidden, body)
				}
			}
		})
	}

	escapeFileCases := []struct {
		name string
		path string
	}{
		{name: "direct_outside_file", path: filepath.Join(externalTarget, "secret")},
		{name: "direct_escape_symlink_file", path: filepath.Join(root, "escape", "secret")},
		{name: "nested_escape_symlink_file", path: filepath.Join(root, "nest", "escape")},
		{name: "nested_escape_dir_child_file", path: filepath.Join(root, "nest", "escape_dir", "secret")},
	}
	for _, tc := range escapeFileCases {
		t.Run("content_"+tc.name, func(t *testing.T) {
			resp := performNodeFileBrowserRequest(t, router, admin, "files/content", node.ID, tc.path)
			if resp.Code != http.StatusForbidden {
				t.Fatalf("expected 403 for %s, got %d body=%s", tc.name, resp.Code, resp.Body.String())
			}
			body := resp.Body.String()
			if !strings.Contains(body, "超出允许范围") {
				t.Fatalf("expected out-of-bounds message for %s, got %s", tc.name, body)
			}
			for _, forbidden := range []string{externalTarget, "outside_only_entry.txt", fileBrowserOutsideSecretBytes} {
				if strings.Contains(body, forbidden) {
					t.Fatalf("content escape response leaked %q in body: %s", forbidden, body)
				}
			}
		})
	}

	// 不存在的路径应返回 "路径不存在或不可访问"
	missingPath := filepath.Join(root, "this-file-does-not-exist")
	for _, endpoint := range []string{"files", "files/content"} {
		resp := performNodeFileBrowserRequest(t, router, admin, endpoint, node.ID, missingPath)
		if resp.Code != http.StatusForbidden || !strings.Contains(resp.Body.String(), "路径不存在或不可访问") {
			t.Fatalf("missing path on %s status=%d body=%s", endpoint, resp.Code, resp.Body.String())
		}
	}
}

// TestNodeFileBrowserHTTP_OnlyOwnNodeRootsAndOwnershipEnforced 沿真实 GET /api/v1/nodes/:id/files 和
// GET /api/v1/nodes/:id/files/content 验证：
//  1. 本节点任务的 RsyncSource 作为本节点允许根目录可用；
//  2. 其他节点的 BasePath 或其他节点任务的 RsyncSource 不被本节点认可，访问返回 403 且不带出目录项或文件字节；
//  3. 保留节点归属检查（OwnershipNodeCheck）与 RBAC("nodes:files")：非归属 operator 返回 403 "无权访问该节点"、viewer 返回 403 "权限不足" 且均不发起 SFTP 连接，归属 operator 可正常访问。
func TestNodeFileBrowserHTTP_OnlyOwnNodeRootsAndOwnershipEnforced(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink/realpath 测试跳过 Windows")
	}
	tmp := t.TempDir()
	resolvedTmp, err := filepath.EvalSymlinks(tmp)
	if err != nil {
		t.Fatalf("eval tmp: %v", err)
	}

	nodeABase := filepath.Join(resolvedTmp, "nodeA_base")
	nodeATaskSrc := filepath.Join(resolvedTmp, "nodeA_task_src")
	nodeBBase := filepath.Join(resolvedTmp, "nodeB_base")
	nodeBTaskSrc := filepath.Join(resolvedTmp, "nodeB_task_src")
	for _, dir := range []string{nodeABase, nodeATaskSrc, nodeBBase, nodeBTaskSrc} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(nodeATaskSrc, "a_task.txt"), []byte("nodeA-task-data"), 0o644); err != nil {
		t.Fatalf("write a_task.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nodeBBase, "b_base_only.txt"), []byte("FAKE_NODE_B_BASE_SECRET_FOR_TEST_ONLY"), 0o644); err != nil {
		t.Fatalf("write b_base_only.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nodeBTaskSrc, "b_task_only.txt"), []byte("FAKE_NODE_B_TASK_SECRET_FOR_TEST_ONLY"), 0o644); err != nil {
		t.Fatalf("write b_task_only.txt: %v", err)
	}

	server := startFileBrowserSSHSFTPTestServer(t)
	db := newValidateTestDB(t)
	router := newFileBrowserTestRouter(db)

	nodeA := seedFileBrowserTestNode(t, db, server, "node-A", nodeABase)
	nodeB := seedFileBrowserTestNode(t, db, server, "node-B", nodeBBase)

	if err := db.Create(&model.Task{
		Name:        "task-A",
		NodeID:      nodeA.ID,
		RsyncSource: nodeATaskSrc,
		RsyncTarget: filepath.Join(resolvedTmp, "destA"),
	}).Error; err != nil {
		t.Fatalf("seed task-A: %v", err)
	}
	if err := db.Create(&model.Task{
		Name:        "task-B",
		NodeID:      nodeB.ID,
		RsyncSource: nodeBTaskSrc,
		RsyncTarget: filepath.Join(resolvedTmp, "destB"),
	}).Error; err != nil {
		t.Fatalf("seed task-B: %v", err)
	}

	ownerOperator := fileBrowserActor{userID: 11, username: "op-owner", role: "operator"}
	otherOperator := fileBrowserActor{userID: 12, username: "op-other", role: "operator"}
	viewerActor := fileBrowserActor{userID: 13, username: "viewer-user", role: "viewer"}

	if err := db.Create(&model.NodeOwner{NodeID: nodeA.ID, UserID: ownerOperator.userID}).Error; err != nil {
		t.Fatalf("seed nodeA owner: %v", err)
	}
	if err := db.Create(&model.NodeOwner{NodeID: nodeB.ID, UserID: otherOperator.userID}).Error; err != nil {
		t.Fatalf("seed nodeB owner: %v", err)
	}

	// 1) 非归属 operator 与 viewer 在中间件层直接被 403 拒绝，不触发任何 SSH/SFTP 握手
	beforeAttempts := server.authAttempts.Load()
	for _, endpoint := range []string{"files", "files/content"} {
		unownedResp := performNodeFileBrowserRequest(t, router, otherOperator, endpoint, nodeA.ID, filepath.Join(nodeATaskSrc, "a_task.txt"))
		if unownedResp.Code != http.StatusForbidden || !strings.Contains(unownedResp.Body.String(), "无权访问该节点") {
			t.Fatalf("unowned operator on %s status=%d body=%s", endpoint, unownedResp.Code, unownedResp.Body.String())
		}
		viewerResp := performNodeFileBrowserRequest(t, router, viewerActor, endpoint, nodeA.ID, filepath.Join(nodeATaskSrc, "a_task.txt"))
		if viewerResp.Code != http.StatusForbidden || !strings.Contains(viewerResp.Body.String(), "权限不足") {
			t.Fatalf("viewer on %s status=%d body=%s", endpoint, viewerResp.Code, viewerResp.Body.String())
		}
	}
	if gotAttempts := server.authAttempts.Load(); gotAttempts != beforeAttempts {
		t.Fatalf("middleware rejection must not dial SSH/SFTP: before=%d after=%d", beforeAttempts, gotAttempts)
	}

	// 2) 归属 operator 访问 nodeA 自身任务的 RsyncSource 目录与文件成功
	listOwnTaskResp := performNodeFileBrowserRequest(t, router, ownerOperator, "files", nodeA.ID, nodeATaskSrc)
	if listOwnTaskResp.Code != http.StatusOK {
		t.Fatalf("owner operator list own task RsyncSource status=%d body=%s", listOwnTaskResp.Code, listOwnTaskResp.Body.String())
	}
	var ownTaskList fileListEnvelope
	if err := json.Unmarshal(listOwnTaskResp.Body.Bytes(), &ownTaskList); err != nil {
		t.Fatalf("decode own task list: %v", err)
	}
	if len(ownTaskList.Data.Entries) != 1 || ownTaskList.Data.Entries[0].Name != "a_task.txt" {
		t.Fatalf("unexpected own task list entries: %+v", ownTaskList.Data)
	}

	previewOwnTaskResp := performNodeFileBrowserRequest(t, router, ownerOperator, "files/content", nodeA.ID, filepath.Join(nodeATaskSrc, "a_task.txt"))
	if previewOwnTaskResp.Code != http.StatusOK {
		t.Fatalf("owner operator preview own task RsyncSource file status=%d body=%s", previewOwnTaskResp.Code, previewOwnTaskResp.Body.String())
	}
	var ownTaskContent fileContentEnvelope
	if err := json.Unmarshal(previewOwnTaskResp.Body.Bytes(), &ownTaskContent); err != nil {
		t.Fatalf("decode own task content: %v", err)
	}
	if ownTaskContent.Data.Content != "nodeA-task-data" {
		t.Fatalf("unexpected own task file content: %+v", ownTaskContent.Data)
	}

	// 3) 通过 nodeA 请求 nodeB 的 BasePath 或 nodeB 的 Task.RsyncSource 均被拒绝（403），不泄露目录项或内容
	for _, crossDir := range []string{nodeBBase, nodeBTaskSrc} {
		resp := performNodeFileBrowserRequest(t, router, ownerOperator, "files", nodeA.ID, crossDir)
		if resp.Code != http.StatusForbidden {
			t.Fatalf("cross-node list %s on nodeA expected 403, got %d body=%s", crossDir, resp.Code, resp.Body.String())
		}
		body := resp.Body.String()
		for _, forbidden := range []string{crossDir, "b_base_only.txt", "b_task_only.txt", "FAKE_NODE_B_BASE_SECRET_FOR_TEST_ONLY", "FAKE_NODE_B_TASK_SECRET_FOR_TEST_ONLY"} {
			if strings.Contains(body, forbidden) {
				t.Fatalf("cross-node list response leaked %q: %s", forbidden, body)
			}
		}
	}
	for _, crossFile := range []string{
		filepath.Join(nodeBBase, "b_base_only.txt"),
		filepath.Join(nodeBTaskSrc, "b_task_only.txt"),
	} {
		resp := performNodeFileBrowserRequest(t, router, ownerOperator, "files/content", nodeA.ID, crossFile)
		if resp.Code != http.StatusForbidden {
			t.Fatalf("cross-node content %s on nodeA expected 403, got %d body=%s", crossFile, resp.Code, resp.Body.String())
		}
		body := resp.Body.String()
		for _, forbidden := range []string{crossFile, "FAKE_NODE_B_BASE_SECRET_FOR_TEST_ONLY", "FAKE_NODE_B_TASK_SECRET_FOR_TEST_ONLY"} {
			if strings.Contains(body, forbidden) {
				t.Fatalf("cross-node content response leaked %q: %s", forbidden, body)
			}
		}
	}
}

// TestNodeFileBrowserHTTP_DevAllowAllSwitchBoundary 沿真实 GET /api/v1/nodes/:id/files 和
// GET /api/v1/nodes/:id/files/content 验证现有开发环境开关边界：
//  1. FILE_BROWSER_ALLOW_ALL=true 且 APP_ENV=development 时，允许访问 BasePath 之外的受控路径；
//  2. FILE_BROWSER_ALLOW_ALL=true 且处于非开发环境（如 APP_ENV=production）时，两个入口均返回 403
//     "FILE_BROWSER_ALLOW_ALL 仅允许在开发环境中使用"，不带出任何目录项或文件字节。
func TestNodeFileBrowserHTTP_DevAllowAllSwitchBoundary(t *testing.T) {
	root, externalTarget := setupSymlinkTree(t)
	server := startFileBrowserSSHSFTPTestServer(t)
	db := newValidateTestDB(t)
	router := newFileBrowserTestRouter(db)
	admin := fileBrowserActor{userID: 1, username: "admin", role: "admin"}

	node := seedFileBrowserTestNode(t, db, server, "node-env-boundary", root)
	outsideFile := filepath.Join(externalTarget, "outside_only_entry.txt")

	t.Run("allowed_in_development", func(t *testing.T) {
		t.Setenv("FILE_BROWSER_ALLOW_ALL", "true")
		t.Setenv("APP_ENV", "development")

		listResp := performNodeFileBrowserRequest(t, router, admin, "files", node.ID, externalTarget)
		if listResp.Code != http.StatusOK {
			t.Fatalf("dev allow-all list status=%d body=%s", listResp.Code, listResp.Body.String())
		}
		var listEnv fileListEnvelope
		if err := json.Unmarshal(listResp.Body.Bytes(), &listEnv); err != nil {
			t.Fatalf("decode dev allow-all list: %v", err)
		}
		if listEnv.Data.Path != externalTarget || len(listEnv.Data.Entries) == 0 {
			t.Fatalf("expected entries in dev allow-all list: %+v", listEnv.Data)
		}

		contentResp := performNodeFileBrowserRequest(t, router, admin, "files/content", node.ID, outsideFile)
		if contentResp.Code != http.StatusOK {
			t.Fatalf("dev allow-all content status=%d body=%s", contentResp.Code, contentResp.Body.String())
		}
		var contentEnv fileContentEnvelope
		if err := json.Unmarshal(contentResp.Body.Bytes(), &contentEnv); err != nil {
			t.Fatalf("decode dev allow-all content: %v", err)
		}
		if contentEnv.Data.Path != outsideFile || contentEnv.Data.Content != "outside-dir-entry" {
			t.Fatalf("unexpected dev allow-all content: %+v", contentEnv.Data)
		}

		// 开发模式 FILE_BROWSER_ALLOW_ALL=true 下通过文件软链接或目录软链接预览稳定目标文件同样应返回 200 OK
		for _, tc := range []struct {
			name        string
			requestPath string
			wantContent string
		}{
			{
				name:        "dev_allow_all_intermediate_dir_symlink",
				requestPath: filepath.Join(root, "escape", "outside_only_entry.txt"),
				wantContent: "outside-dir-entry",
			},
			{
				name:        "dev_allow_all_final_file_symlink",
				requestPath: filepath.Join(root, "nest", "escape"),
				wantContent: fileBrowserOutsideSecretBytes,
			},
		} {
			symResp := performNodeFileBrowserRequest(t, router, admin, "files/content", node.ID, tc.requestPath)
			if symResp.Code != http.StatusOK {
				t.Fatalf("%s status=%d body=%s", tc.name, symResp.Code, symResp.Body.String())
			}
			var symEnv fileContentEnvelope
			if err := json.Unmarshal(symResp.Body.Bytes(), &symEnv); err != nil {
				t.Fatalf("decode %s: %v", tc.name, err)
			}
			if symEnv.Data.Path != filepath.Clean(tc.requestPath) || symEnv.Data.Content != tc.wantContent {
				t.Fatalf("unexpected %s payload: %+v", tc.name, symEnv.Data)
			}
		}
	})

	t.Run("rejected_in_production", func(t *testing.T) {
		t.Setenv("FILE_BROWSER_ALLOW_ALL", "true")
		t.Setenv("APP_ENV", "production")
		t.Setenv("ENVIRONMENT", "")
		t.Setenv("GIN_MODE", "release")

		for _, tc := range []struct {
			endpoint string
			path     string
		}{
			{endpoint: "files", path: root},
			{endpoint: "files", path: externalTarget},
			{endpoint: "files/content", path: filepath.Join(root, "file")},
			{endpoint: "files/content", path: filepath.Join(externalTarget, "secret")},
		} {
			resp := performNodeFileBrowserRequest(t, router, admin, tc.endpoint, node.ID, tc.path)
			if resp.Code != http.StatusForbidden {
				t.Fatalf("prod allow-all on %s (%s) expected 403, got %d body=%s", tc.endpoint, tc.path, resp.Code, resp.Body.String())
			}
			body := resp.Body.String()
			if !strings.Contains(body, "FILE_BROWSER_ALLOW_ALL 仅允许在开发环境中使用") {
				t.Fatalf("expected prod allow-all rejection message, got %s", body)
			}
			for _, forbidden := range []string{"outside_only_entry.txt", fileBrowserOutsideSecretBytes, `"entries":[{`, `"content":"`} {
				if strings.Contains(body, forbidden) {
					t.Fatalf("prod allow-all response leaked %q: %s", forbidden, body)
				}
			}
		}
	})
}

// TestNodeFileBrowserHTTP_PreviewPostReadValidationFailureDoesNotReturnBufferedContent 沿真实
// GET /api/v1/nodes/:id/files/content 验证：
// 当 io.ReadFull(handle, buf) 已经将文件字节读入内存缓冲区 buf 后，若 handle.Close() 执行的最后一次
// 校验（postCheck：重验 RealPath 与 Lstat 快照）检测到文件在读取期间被修改或被替换为指向根外的软链接，
// handler 返回 502 Bad Gateway（"文件在读取期间发生变化"），绝不把已缓冲的字节返回给客户端或写入审计。
func TestNodeFileBrowserHTTP_PreviewPostReadValidationFailureDoesNotReturnBufferedContent(t *testing.T) {
	root, externalTarget := setupSymlinkTree(t)
	server := startFileBrowserSSHSFTPTestServer(t)
	db := newValidateTestDB(t)
	router := newFileBrowserTestRouter(db)
	admin := fileBrowserActor{userID: 1, username: "admin", role: "admin"}

	node := seedFileBrowserTestNode(t, db, server, "node-post-check", root)

	t.Run("mutated_after_read_before_close", func(t *testing.T) {
		targetFile := filepath.Join(root, "buffered_mutable.txt")
		if err := os.WriteFile(targetFile, []byte(fileBrowserBufferedSecret), 0o644); err != nil {
			t.Fatalf("write buffered_mutable.txt: %v", err)
		}
		server.fsHost.setOnReadComplete(targetFile, func() {
			// 在 Fileread 读完整个文件字节并返回 EOF 时修改文件大小/内容，使紧随其后的 handle.Close() postCheck 失败
			_ = os.WriteFile(targetFile, []byte(fileBrowserBufferedSecret+"-mutated-during-read"), 0o644)
		})
		t.Cleanup(func() {
			server.fsHost.setOnReadComplete(targetFile, nil)
		})

		resp := performNodeFileBrowserRequest(t, router, admin, "files/content", node.ID, targetFile)
		if resp.Code != http.StatusBadGateway {
			t.Fatalf("expected 502 on post-read mutation, got %d body=%s", resp.Code, resp.Body.String())
		}
		body := resp.Body.String()
		if !strings.Contains(body, "文件在读取期间发生变化") {
			t.Fatalf("expected post-check failure message, got %s", body)
		}
		if strings.Contains(body, fileBrowserBufferedSecret) {
			t.Fatalf("post-check failure leaked buffered file content in HTTP response: %s", body)
		}

		var event model.CredentialAuditEvent
		if err := db.Order("id desc").First(&event).Error; err != nil {
			t.Fatalf("load latest audit event: %v", err)
		}
		if event.Action != "file_browser.preview" || event.Outcome != credentialaudit.OutcomeFailure {
			t.Fatalf("unexpected audit event on post-check failure: %+v", event)
		}
		if !strings.Contains(event.Metadata, `"stage":"post_check"`) || strings.Contains(event.Metadata, fileBrowserBufferedSecret) {
			t.Fatalf("unexpected audit metadata on post-check failure: %s", event.Metadata)
		}
	})

	t.Run("replaced_with_escape_symlink_after_read_before_close", func(t *testing.T) {
		targetFile := filepath.Join(root, "buffered_swap_escape.txt")
		if err := os.WriteFile(targetFile, []byte(fileBrowserBufferedSecret), 0o644); err != nil {
			t.Fatalf("write buffered_swap_escape.txt: %v", err)
		}
		server.fsHost.setOnReadComplete(targetFile, func() {
			_ = os.Remove(targetFile)
			_ = os.Symlink(filepath.Join(externalTarget, "secret"), targetFile)
		})
		t.Cleanup(func() {
			server.fsHost.setOnReadComplete(targetFile, nil)
		})

		resp := performNodeFileBrowserRequest(t, router, admin, "files/content", node.ID, targetFile)
		if resp.Code != http.StatusBadGateway {
			t.Fatalf("expected 502 on post-read symlink swap, got %d body=%s", resp.Code, resp.Body.String())
		}
		body := resp.Body.String()
		if !strings.Contains(body, "文件在读取期间发生变化") {
			t.Fatalf("expected post-check failure message, got %s", body)
		}
		for _, forbidden := range []string{fileBrowserBufferedSecret, fileBrowserOutsideSecretBytes, externalTarget} {
			if strings.Contains(body, forbidden) {
				t.Fatalf("post-check symlink swap leaked %q in HTTP response: %s", forbidden, body)
			}
		}
	})
}

// TestNodeFileBrowserHTTP_OpenedHandleAfterPathRestoreProbe 沿真实 GET /api/v1/nodes/:id/files/content
// 验证相邻调查项：“节点文件预览：路径恢复后，已打开句柄是否仍可能返回根外文件内容”，保留正常与拒绝对照。
func TestNodeFileBrowserHTTP_OpenedHandleAfterPathRestoreProbe(t *testing.T) {
	root, externalTarget := setupSymlinkTree(t)
	server := startFileBrowserSSHSFTPTestServer(t)
	db := newValidateTestDB(t)
	router := newFileBrowserTestRouter(db)
	admin := fileBrowserActor{userID: 1, username: "admin", role: "admin"}

	node := seedFileBrowserTestNode(t, db, server, "node-opened-handle-restore", root)

	t.Run("normal_in_root_control", func(t *testing.T) {
		targetFile := filepath.Join(root, "normal_control.txt")
		if err := os.WriteFile(targetFile, []byte("IN_ROOT_NORMAL_PAYLOAD"), 0o644); err != nil {
			t.Fatalf("write normal_control.txt: %v", err)
		}
		resp := performNodeFileBrowserRequest(t, router, admin, "files/content", node.ID, targetFile)
		if resp.Code != http.StatusOK {
			t.Fatalf("expected 200 on normal control, got %d body=%s", resp.Code, resp.Body.String())
		}
		var env fileContentEnvelope
		if err := json.Unmarshal(resp.Body.Bytes(), &env); err != nil {
			t.Fatalf("decode normal control: %v", err)
		}
		if env.Data.Content != "IN_ROOT_NORMAL_PAYLOAD" {
			t.Fatalf("unexpected normal control content: %+v", env.Data)
		}
		var event model.CredentialAuditEvent
		if err := db.Order("id desc").First(&event).Error; err != nil {
			t.Fatalf("load audit event: %v", err)
		}
		if event.Action != "file_browser.preview" || event.Outcome != credentialaudit.OutcomeSuccess || !strings.Contains(event.Metadata, `"stage":"success"`) {
			t.Fatalf("unexpected normal control audit event: %+v metadata=%s", event, event.Metadata)
		}
	})

	t.Run("denied_outside_root_control", func(t *testing.T) {
		outsideSecret := filepath.Join(externalTarget, "secret")
		resp := performNodeFileBrowserRequest(t, router, admin, "files/content", node.ID, outsideSecret)
		if resp.Code != http.StatusForbidden {
			t.Fatalf("expected 403 on denied control, got %d body=%s", resp.Code, resp.Body.String())
		}
		if strings.Contains(resp.Body.String(), fileBrowserOutsideSecretBytes) {
			t.Fatalf("denied control leaked outside marker: %s", resp.Body.String())
		}
		var event model.CredentialAuditEvent
		if err := db.Order("id desc").First(&event).Error; err != nil {
			t.Fatalf("load audit event: %v", err)
		}
		if event.Action != "file_browser.preview" || event.Outcome != credentialaudit.OutcomeBlocked || !strings.Contains(event.Metadata, `"stage":"path_validate"`) {
			t.Fatalf("unexpected denied control audit event: %+v metadata=%s", event, event.Metadata)
		}
	})

	t.Run("opened_handle_after_path_restore_rejects_outside_bytes", func(t *testing.T) {
		targetFile := filepath.Join(root, "swap_restore.txt")
		stashFile := filepath.Join(root, ".swap_restore.stash")
		outsideSecret := filepath.Join(externalTarget, "secret")

		// 构造与根外 secret 具有相同字节长度和秒级 mtime 的根内合法文件，模拟 SFTP Wire FileStat 属性匹配
		secretInfo, err := os.Stat(outsideSecret)
		if err != nil {
			t.Fatalf("stat outside secret: %v", err)
		}
		inRootContent := strings.Repeat("A", int(secretInfo.Size()))
		if err := os.WriteFile(targetFile, []byte(inRootContent), secretInfo.Mode().Perm()); err != nil {
			t.Fatalf("write swap_restore.txt: %v", err)
		}
		alignedTime := time.Unix(secretInfo.ModTime().Unix(), 0)
		if err := os.Chtimes(outsideSecret, alignedTime, alignedTime); err != nil {
			t.Fatalf("chtimes outside secret: %v", err)
		}
		if err := os.Chtimes(targetFile, alignedTime, alignedTime); err != nil {
			t.Fatalf("chtimes targetFile: %v", err)
		}

		// 在每次 SFTP Open(candidate) 即将打开底层文件时将路径替换为指向根外 secret 的软链接，
		// 并在底层句柄打开后立即恢复回原根内普通文件（保留原 inode/mtime/size）。
		server.fsHost.setOnBeforeOpen(targetFile, func() {
			_ = os.Rename(targetFile, stashFile)
			_ = os.Symlink(outsideSecret, targetFile)
		})
		server.fsHost.setOnAfterOpen(targetFile, func() {
			_ = os.Remove(targetFile)
			_ = os.Rename(stashFile, targetFile)
		})
		t.Cleanup(func() {
			server.fsHost.setOnBeforeOpen(targetFile, nil)
			server.fsHost.setOnAfterOpen(targetFile, nil)
		})

		resp := performNodeFileBrowserRequest(t, router, admin, "files/content", node.ID, targetFile)
		body := resp.Body.String()
		var event model.CredentialAuditEvent
		if err := db.Order("id desc").First(&event).Error; err != nil {
			t.Fatalf("load audit event: %v", err)
		}
		t.Logf("probe observed HTTP status=%d contains_outside_marker=%v body=%s audit_outcome=%s audit_metadata=%s",
			resp.Code, strings.Contains(body, fileBrowserOutsideSecretBytes), body, event.Outcome, event.Metadata)

		if strings.Contains(body, fileBrowserOutsideSecretBytes) {
			t.Fatalf("opened handle after path restore leaked outside test marker %q in HTTP response (status=%d): %s",
				fileBrowserOutsideSecretBytes, resp.Code, body)
		}
		if resp.Code != http.StatusBadGateway || !strings.Contains(body, "文件在读取期间发生变化") {
			t.Fatalf("expected 502 with post-check failure message when opened handle diverges from restored path, got %d body=%s", resp.Code, body)
		}
		if event.Action != "file_browser.preview" || event.Outcome != credentialaudit.OutcomeFailure || !strings.Contains(event.Metadata, `"stage":"post_check"`) {
			t.Fatalf("unexpected audit event on opened-handle path restore mismatch: %+v metadata=%s", event, event.Metadata)
		}
		if strings.Contains(event.Metadata, fileBrowserOutsideSecretBytes) {
			t.Fatalf("audit metadata leaked outside test marker: %s", event.Metadata)
		}
	})

	t.Run("intermediate_dir_swap_and_restore_rejects_outside_bytes", func(t *testing.T) {
		swapDir := filepath.Join(root, "swap_dir")
		stashDir := filepath.Join(root, ".swap_dir.stash")
		if err := os.MkdirAll(swapDir, 0o755); err != nil {
			t.Fatalf("mkdir swap_dir: %v", err)
		}
		targetFile := filepath.Join(swapDir, "secret")
		outsideSecret := filepath.Join(externalTarget, "secret")
		secretInfo, err := os.Stat(outsideSecret)
		if err != nil {
			t.Fatalf("stat outside secret: %v", err)
		}
		inRootContent := strings.Repeat("B", int(secretInfo.Size()))
		if err := os.WriteFile(targetFile, []byte(inRootContent), secretInfo.Mode().Perm()); err != nil {
			t.Fatalf("write swap_dir/secret: %v", err)
		}
		alignedTime := time.Unix(secretInfo.ModTime().Unix(), 0)
		_ = os.Chtimes(outsideSecret, alignedTime, alignedTime)
		_ = os.Chtimes(targetFile, alignedTime, alignedTime)

		// 在 SFTP Open(candidate) 打开中间目录 swap_dir 下的文件时，将整个中间目录替换为指向 externalTarget 的软链接，
		// 并在底层句柄打开后立即将中间目录恢复回原根内目录。
		server.fsHost.setOnBeforeOpen(targetFile, func() {
			_ = os.Rename(swapDir, stashDir)
			_ = os.Symlink(externalTarget, swapDir)
		})
		server.fsHost.setOnAfterOpen(targetFile, func() {
			_ = os.Remove(swapDir)
			_ = os.Rename(stashDir, swapDir)
		})
		t.Cleanup(func() {
			server.fsHost.setOnBeforeOpen(targetFile, nil)
			server.fsHost.setOnAfterOpen(targetFile, nil)
		})

		resp := performNodeFileBrowserRequest(t, router, admin, "files/content", node.ID, targetFile)
		body := resp.Body.String()
		var event model.CredentialAuditEvent
		if err := db.Order("id desc").First(&event).Error; err != nil {
			t.Fatalf("load audit event: %v", err)
		}
		if strings.Contains(body, fileBrowserOutsideSecretBytes) {
			t.Fatalf("intermediate dir swap-and-restore leaked outside test marker %q in HTTP response (status=%d): %s",
				fileBrowserOutsideSecretBytes, resp.Code, body)
		}
		if resp.Code != http.StatusBadGateway || !strings.Contains(body, "文件在读取期间发生变化") {
			t.Fatalf("expected 502 on intermediate dir swap-and-restore, got %d body=%s", resp.Code, body)
		}
		if event.Action != "file_browser.preview" || event.Outcome != credentialaudit.OutcomeFailure || !strings.Contains(event.Metadata, `"stage":"post_check"`) {
			t.Fatalf("unexpected audit event on intermediate dir swap-and-restore: %+v metadata=%s", event, event.Metadata)
		}
	})
}

// TestResolveNodeFileAccess_NilResolverAndDevBypassUnit 覆盖 resolveNodeFileAccess 的内部防御边界：
//  1. 正常模式下 resolver == nil 返回 "缺少 SFTP 会话"；
//  2. FILE_BROWSER_ALLOW_ALL=true + APP_ENV=development 下无需 SFTP 会话即可完成语法 Clean。
func TestResolveNodeFileAccess_NilResolverAndDevBypassUnit(t *testing.T) {
	roots := []fileaccess.Root{{Path: "/restricted"}}
	if _, _, _, err := resolveNodeFileAccess(nil, "/restricted/x", roots); err == nil || !strings.Contains(err.Error(), "缺少 SFTP 会话") {
		t.Fatalf("nil resolver should fail with missing SFTP session, got %v", err)
	}

	t.Setenv("FILE_BROWSER_ALLOW_ALL", "true")
	t.Setenv("APP_ENV", "development")
	accessRoot, locator, cleanPath, err := resolveNodeFileAccess(nil, "/anywhere/../else", roots)
	if err != nil {
		t.Fatalf("dev allow-all bypass should succeed with nil resolver: %v", err)
	}
	if accessRoot.Path != string(filepath.Separator) || locator.Path != "/else" || cleanPath != "/else" {
		t.Fatalf("unexpected dev allow-all resolution: root=%+v locator=%+v clean=%q", accessRoot, locator, cleanPath)
	}
}

func TestLoadNodeFileRootsFailsClosedOnQueryError(t *testing.T) {
	db := newValidateTestDB(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := loadNodeFileRoots(context.Background(), db, model.Node{ID: 7, BasePath: "/allowed"}); !errors.Is(err, errFileRootQuery) {
		t.Fatalf("root query error=%v", err)
	}
}

func TestFileHandlerUsesSharedOperationCoupledFileAccess(t *testing.T) {
	source, err := os.ReadFile("file_handler.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	if !strings.Contains(text, "internal/fileaccess") {
		t.Fatal("file handler does not import shared fileaccess package")
	}
	for _, forbidden := range []string{"sftpClient.Open(", "sftpClient.Stat(", "listSFTPDir(", "os.ReadDir(", "func validateNodePath("} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("file handler retains forbidden or dead primitive %q", forbidden)
		}
	}
	migrateSource, err := os.ReadFile("node_migrate_handler.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(migrateSource), "func directoryTreesEqual(") {
		t.Fatal("node_migrate_handler.go retains dead wrapper directoryTreesEqual")
	}
}

func TestFileHandlerFailureLogsUseSafeStructuredFields(t *testing.T) {
	var buf bytes.Buffer
	previous := logger.Log
	logger.Log = zerolog.New(&buf)
	t.Cleanup(func() { logger.Log = previous })

	rawPath := "/safe/FAKE_FILE_NAME_FOR_TEST_ONLY"
	logFileHandlerFailure("error", 42, "read", rawPath, "SFTP 读取文件失败")
	logLocalFileHandlerFailure("warn", 77, 88, "read_dir", "/backups/FAKE_BACKUP_NAME_FOR_TEST_ONLY", "读取本地目录失败")

	logOutput := buf.String()
	if strings.Contains(logOutput, rawPath) || strings.Contains(logOutput, "/backups/FAKE_BACKUP_NAME_FOR_TEST_ONLY") {
		t.Fatalf("file handler process logs must not include raw paths: %s", logOutput)
	}
	if strings.Contains(logOutput, "FAKE_SFTP_OUTPUT_FOR_TEST_ONLY") || strings.Contains(logOutput, "no such file") {
		t.Fatalf("file handler process logs must not include raw error/output evidence: %s", logOutput)
	}
	for _, want := range []string{`"module":"file_handler"`, `"node_id":42`, `"node_id":88`, `"task_id":77`, `"stage":"read"`, `"stage":"read_dir"`, `"path_hash":"`} {
		if !strings.Contains(logOutput, want) {
			t.Fatalf("file handler process log missing safe field %s: %s", want, logOutput)
		}
	}
}

func TestFileBrowserAuditDoesNotPersistPathOrPreviewContent(t *testing.T) {
	db := newValidateTestDB(t)
	h := NewFileHandler(db)
	node := model.Node{ID: 42, Name: "file-node", Host: "10.20.30.40", AuthType: "key", SSHKeyID: credentialaudit.PtrUint(7)}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("userID", uint(101))
	c.Set("username", "alice")
	c.Set("role", "operator")
	req := httptest.NewRequest("GET", "/nodes/42/files/content?path=/safe/FAKE_FILE_NAME_FOR_TEST_ONLY", nil)
	c.Request = req

	h.writeFileBrowserAudit(c, node, sshutil.ResolvedCredential{Kind: "ssh_key", Source: "ssh_key_id=7", KeyID: credentialaudit.PtrUint(7)}, "file_browser.preview", credentialaudit.OutcomeSuccess, nil, map[string]any{
		"stage":         "success",
		"kind":          "file",
		"path_hash":     safePathHash("/safe/FAKE_FILE_NAME_FOR_TEST_ONLY"),
		"preview_bytes": 23,
		"content":       "FAKE_FILE_CONTENT_FOR_TEST_ONLY",
		"output":        "FAKE_SFTP_OUTPUT_FOR_TEST_ONLY",
	})

	var event model.CredentialAuditEvent
	if err := db.First(&event).Error; err != nil {
		t.Fatalf("load audit event: %v", err)
	}
	if event.Action != "file_browser.preview" || event.Purpose != sshutil.PurposeFileBrowser || event.Outcome != credentialaudit.OutcomeSuccess {
		t.Fatalf("unexpected file audit event: %+v", event)
	}
	if event.NodeID == nil || *event.NodeID != node.ID || event.SSHKeyID == nil || *event.SSHKeyID != 7 {
		t.Fatalf("expected node/key ids in audit event: %+v", event)
	}
	if strings.Contains(event.Metadata, "/safe/") || strings.Contains(event.Metadata, "FAKE_FILE_CONTENT_FOR_TEST_ONLY") || strings.Contains(event.Metadata, "FAKE_SFTP_OUTPUT_FOR_TEST_ONLY") {
		t.Fatalf("file audit metadata must not include raw path/content/output: %s", event.Metadata)
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(event.Metadata), &metadata); err != nil {
		t.Fatalf("metadata json: %v", err)
	}
	if metadata["path_hash"] == "" || metadata["preview_bytes"] == nil {
		t.Fatalf("safe file audit metadata missing expected fields: %#v", metadata)
	}
	if _, ok := metadata["content"]; ok {
		t.Fatalf("content metadata key should be dropped: %#v", metadata)
	}
	if _, ok := metadata["output"]; ok {
		t.Fatalf("output metadata key should be dropped: %#v", metadata)
	}
}

// --- 集成 smoke：用 net.Pipe + sftp.NewServer 验证 *sftp.Client 满足 realPathResolver 接口 ---

func TestSFTPClient_SatisfiesRealPathResolver(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("net.Pipe + sftp server 在 Windows CI 路径表示有差异，跳过")
	}

	c1, c2 := net.Pipe()
	defer c1.Close() //nolint:errcheck // test cleanup, error not actionable
	defer c2.Close() //nolint:errcheck // test cleanup, error not actionable

	server, err := sftp.NewServer(serverPipe{ReadCloser: c1, WriteCloser: c1})
	if err != nil {
		t.Fatalf("new sftp server: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve() }()

	client, err := sftp.NewClientPipe(c2, c2)
	if err != nil {
		t.Fatalf("new sftp client: %v", err)
	}
	defer client.Close() //nolint:errcheck // test cleanup, error not actionable

	var _ realPathResolver = client

	resultCh := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		p, err := client.RealPath("/")
		if err != nil {
			errCh <- err
			return
		}
		resultCh <- p
	}()
	select {
	case p := <-resultCh:
		if p == "" {
			t.Fatalf("RealPath 返回空字符串")
		}
	case err := <-errCh:
		t.Fatalf("RealPath: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatalf("RealPath 调用超时")
	}

	_ = c2.Close()
	_ = c1.Close()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrClosedPipe) {
			t.Logf("server.Serve returned: %v", err)
		}
	case <-time.After(3 * time.Second):
	}
}

type serverPipe struct {
	io.ReadCloser
	io.WriteCloser
}

func (p serverPipe) Close() error {
	_ = p.WriteCloser.Close()
	return p.ReadCloser.Close()
}
