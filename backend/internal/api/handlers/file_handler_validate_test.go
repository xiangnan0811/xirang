package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/credentialaudit"
	"xirang/backend/internal/logger"
	"xirang/backend/internal/model"
	"xirang/backend/internal/sshutil"

	"github.com/gin-gonic/gin"

	"github.com/pkg/sftp"
	"github.com/rs/zerolog"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// fsRealPathResolver 用真实文件系统 + filepath.EvalSymlinks 模拟一个 SFTP 节点端的 RealPath 行为。
// 这样测试可以使用 os.Symlink 构造符号链接逃逸场景，而不必启动完整的 SSH/SFTP server。
//
// 对应生产实现：*sftp.Client.RealPath 走 SFTP_FXP_REALPATH 协议包，由 OpenSSH server 端用 realpath(3)
// 语义解析符号链接。本地用 filepath.EvalSymlinks 行为等价（去掉 Windows 平台差异）。
type fsRealPathResolver struct {
	// notExist 设为 true 时模拟节点端 ENOENT；用于覆盖 RealPath 失败分支。
	notExist bool
}

func (r *fsRealPathResolver) RealPath(p string) (string, error) {
	if r.notExist {
		return "", &os.PathError{Op: "realpath", Path: p, Err: os.ErrNotExist}
	}
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", err
	}
	return resolved, nil
}

func newValidateTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+handlerTestDBName(t)+"?mode=memory&cache=shared&_loc=UTC"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.Task{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// setupSymlinkTree 构造如下结构（root 末尾不带 /）：
//
//	<root>/
//	  file              普通文件
//	  sub/              子目录
//	    nested.txt
//	  link_safe -> sub  指向 root 内的 symlink，应允许
//	  escape    -> /etc 指向 root 外的 symlink，应拒绝
//	  nest/
//	    escape -> /etc/passwd 嵌套位置的逃逸 symlink
//
// 返回 root 绝对路径（已 EvalSymlinks，因为 macOS 的 /var → /private/var 之类会影响断言）。
func setupSymlinkTree(t *testing.T) (root, externalTarget string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("symlink 构造在 Windows CI 上需要管理员权限，跳过")
	}
	tmp := t.TempDir()
	// macOS 的 t.TempDir 返回 /var/... 实际是 /private/var/...，统一用解析后的真实路径，
	// 否则后续 EvalSymlinks 比较会因 /var → /private/var 失败。
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

	// 选一个一定存在的外部目标作为逃逸 symlink 指向；用 t.TempDir 的兄弟目录避免污染 /etc 这类系统路径。
	externalTarget = filepath.Join(resolvedTmp, "outside")
	if err := os.MkdirAll(externalTarget, 0o755); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}
	if err := os.WriteFile(filepath.Join(externalTarget, "secret"), []byte("leak"), 0o644); err != nil {
		t.Fatalf("write secret: %v", err)
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
	return root, externalTarget
}

func TestValidateNodePath_ResolvesRealPathAndAllowsCleanPath(t *testing.T) {
	root, _ := setupSymlinkTree(t)
	db := newValidateTestDB(t)
	node := model.Node{ID: 1, BasePath: root}
	resolver := &fsRealPathResolver{}

	got, err := validateNodePath(context.Background(), resolver, filepath.Join(root, "file"), node, db)
	if err != nil {
		t.Fatalf("clean path 应通过：%v", err)
	}
	want := filepath.Join(root, "file")
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestValidateNodePath_AllowsSafeSymlink(t *testing.T) {
	root, _ := setupSymlinkTree(t)
	db := newValidateTestDB(t)
	node := model.Node{ID: 1, BasePath: root}
	resolver := &fsRealPathResolver{}

	// link_safe 指向 root 内的 sub，访问 link_safe/nested.txt 应被允许，且返回值是解析后的 sub/nested.txt
	got, err := validateNodePath(context.Background(), resolver,
		filepath.Join(root, "link_safe", "nested.txt"), node, db)
	if err != nil {
		t.Fatalf("root 内 symlink 应允许：%v", err)
	}
	want := filepath.Join(root, "sub", "nested.txt")
	if got != want {
		t.Fatalf("got %q want %q (返回值应为解析后的真实路径)", got, want)
	}
}

func TestValidateNodePath_RejectsEscapeSymlink(t *testing.T) {
	root, externalTarget := setupSymlinkTree(t)
	db := newValidateTestDB(t)
	node := model.Node{ID: 1, BasePath: root}
	resolver := &fsRealPathResolver{}

	_, err := validateNodePath(context.Background(), resolver,
		filepath.Join(root, "escape", "secret"), node, db)
	if err == nil {
		t.Fatalf("逃逸 symlink 必须被拒绝")
	}
	if !strings.Contains(err.Error(), "超出允许范围") {
		t.Fatalf("错误信息应提示越界：%v", err)
	}
	// 安全反例：错误信息不应该泄露 resolved 真实路径（外部目标）
	if strings.Contains(err.Error(), externalTarget) {
		t.Fatalf("错误信息泄露了 resolved 真实路径：%v", err)
	}
}

func TestValidateNodePath_RejectsNestedEscapeSymlink(t *testing.T) {
	root, _ := setupSymlinkTree(t)
	db := newValidateTestDB(t)
	node := model.Node{ID: 1, BasePath: root}
	resolver := &fsRealPathResolver{}

	_, err := validateNodePath(context.Background(), resolver,
		filepath.Join(root, "nest", "escape"), node, db)
	if err == nil {
		t.Fatalf("嵌套位置的逃逸 symlink 必须被拒绝")
	}
	if !strings.Contains(err.Error(), "超出允许范围") {
		t.Fatalf("错误信息应提示越界：%v", err)
	}
}

func TestValidateNodePath_RootItselfIsSymlink_IsResolvedAndStillAllowed(t *testing.T) {
	// 场景：用户在 Node.BasePath 配置 /alias，节点上 /alias → /real/data
	// 修复前会因为 RealPath(input) 已展开但 root 仍是 /alias，导致永远拒绝。
	// 修复后 roots 也被 RealPath，二者都是 /real/data，命中。
	if runtime.GOOS == "windows" {
		t.Skip("symlink 在 Windows 需管理员权限")
	}
	tmp := t.TempDir()
	resolvedTmp, err := filepath.EvalSymlinks(tmp)
	if err != nil {
		t.Fatalf("eval tmp: %v", err)
	}
	realData := filepath.Join(resolvedTmp, "realdata")
	if err := os.MkdirAll(realData, 0o755); err != nil {
		t.Fatalf("mkdir realdata: %v", err)
	}
	if err := os.WriteFile(filepath.Join(realData, "x"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write x: %v", err)
	}
	alias := filepath.Join(resolvedTmp, "alias")
	if err := os.Symlink(realData, alias); err != nil {
		t.Fatalf("symlink alias: %v", err)
	}

	db := newValidateTestDB(t)
	node := model.Node{ID: 1, BasePath: alias} // 用 alias（symlink）作为 BasePath
	resolver := &fsRealPathResolver{}

	got, err := validateNodePath(context.Background(), resolver,
		filepath.Join(alias, "x"), node, db)
	if err != nil {
		t.Fatalf("alias 下合法访问被拒绝：%v", err)
	}
	want := filepath.Join(realData, "x")
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestValidateNodePath_RealPathMissingPath(t *testing.T) {
	// RealPath 返回 ENOENT 时应给出"路径不存在或不可访问"，而不是"超出允许范围"
	root, _ := setupSymlinkTree(t)
	db := newValidateTestDB(t)
	node := model.Node{ID: 1, BasePath: root}
	resolver := &fsRealPathResolver{}

	_, err := validateNodePath(context.Background(), resolver,
		filepath.Join(root, "this-file-does-not-exist"), node, db)
	if err == nil {
		t.Fatalf("不存在路径应报错")
	}
	if !strings.Contains(err.Error(), "路径不存在或不可访问") {
		t.Fatalf("不存在路径应给出明确错误，而不是越界：%v", err)
	}
}

func TestValidateNodePath_NilResolverIsRejected(t *testing.T) {
	db := newValidateTestDB(t)
	node := model.Node{ID: 1, BasePath: "/data"}

	_, err := validateNodePath(context.Background(), nil, "/data/x", node, db)
	if err == nil {
		t.Fatalf("缺少 resolver 应报错")
	}
	if !strings.Contains(err.Error(), "缺少 SFTP 会话") {
		t.Fatalf("nil resolver 错误信息应明确：%v", err)
	}
}

func TestValidateNodePath_FileBrowserAllowAllBypassInDev(t *testing.T) {
	t.Setenv("FILE_BROWSER_ALLOW_ALL", "true")
	t.Setenv("APP_ENV", "development")
	db := newValidateTestDB(t)
	node := model.Node{ID: 1, BasePath: "/restricted"}

	// 即便没有 resolver 也应通过（开发期 escape hatch）
	got, err := validateNodePath(context.Background(), nil, "/anywhere/../else", node, db)
	if err != nil {
		t.Fatalf("dev 旁路应通过：%v", err)
	}
	if got != "/else" {
		t.Fatalf("dev 旁路应仅做 Clean：got %q", got)
	}
}

func TestValidateNodePath_FileBrowserAllowAllRejectedInProd(t *testing.T) {
	t.Setenv("FILE_BROWSER_ALLOW_ALL", "true")
	t.Setenv("APP_ENV", "production")
	t.Setenv("ENVIRONMENT", "")
	t.Setenv("GIN_MODE", "release")
	db := newValidateTestDB(t)
	node := model.Node{ID: 1, BasePath: "/restricted"}

	_, err := validateNodePath(context.Background(), &fsRealPathResolver{}, "/anywhere", node, db)
	if err == nil {
		t.Fatalf("生产环境下 FILE_BROWSER_ALLOW_ALL 必须被拒绝")
	}
}

func TestValidateNodePath_TaskRsyncSourceAlsoActsAsRoot(t *testing.T) {
	// 校验 root 集合包含该节点所有 task 的 RsyncSource
	tmp := t.TempDir()
	resolvedTmp, _ := filepath.EvalSymlinks(tmp)
	src := filepath.Join(resolvedTmp, "task_src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "f"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write f: %v", err)
	}

	db := newValidateTestDB(t)
	if err := db.Create(&model.Task{
		Name:        "t1",
		NodeID:      7,
		RsyncSource: src,
		RsyncTarget: "/tmp/dest",
	}).Error; err != nil {
		t.Fatalf("seed task: %v", err)
	}
	node := model.Node{ID: 7, BasePath: ""} // 仅靠 task RsyncSource 提供白名单
	resolver := &fsRealPathResolver{}

	got, err := validateNodePath(context.Background(), resolver, filepath.Join(src, "f"), node, db)
	if err != nil {
		t.Fatalf("task RsyncSource 应作为白名单根：%v", err)
	}
	if got != filepath.Join(src, "f") {
		t.Fatalf("got %q want %q", got, filepath.Join(src, "f"))
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
	for _, forbidden := range []string{"sftpClient.Open(", "sftpClient.Stat(", "listSFTPDir(", "os.ReadDir("} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("file handler retains check-then-use primitive %q", forbidden)
		}
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
	if err := db.AutoMigrate(&model.Node{}, &model.CredentialAuditEvent{}); err != nil {
		t.Fatalf("migrate audit tables: %v", err)
	}
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

// 注意：pkg/sftp 自带的 NewServer 实现的 RealPath 仅做 filepath.Abs+cleanPath，不解析符号链接。
// 这里的 smoke 测试只是验证 *sftp.Client.RealPath() 方法签名兼容 realPathResolver 接口，
// 真实的符号链接解析行为由前面的 fsRealPathResolver 单元测试覆盖。生产环境对接的是 OpenSSH SFTP server，
// 其 RealPath 走 realpath(3) 语义会解析符号链接。
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

	// 直接断言 *sftp.Client 实现了我们定义的接口
	var _ realPathResolver = client

	// 实际调用一次 RealPath 走通端到端编解码（路径用根目录避免依赖具体环境）
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

	// 让 server 优雅退出
	_ = c2.Close()
	_ = c1.Close()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrClosedPipe) {
			// 正常关闭路径：忽略 EOF / closed pipe
			t.Logf("server.Serve returned: %v", err)
		}
	case <-time.After(3 * time.Second):
		// 不强制要求退出，net.Pipe 关闭后通常会立即返回；超时则放过
	}
}

// serverPipe 把 net.Pipe 的一端包装成 io.ReadWriteCloser（net.Conn 已经是，但显式声明字段以方便阅读）。
type serverPipe struct {
	io.ReadCloser
	io.WriteCloser
}

func (p serverPipe) Close() error {
	_ = p.WriteCloser.Close()
	return p.ReadCloser.Close()
}
