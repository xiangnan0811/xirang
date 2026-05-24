package handlers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"xirang/backend/internal/credentialaudit"
	"xirang/backend/internal/model"
	"xirang/backend/internal/policy"
	"xirang/backend/internal/settings"
	"xirang/backend/internal/sshutil"
	"xirang/backend/internal/task/executor"
	"xirang/backend/internal/util"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
	"gorm.io/gorm"
)

type doctorCheckStatus string

const (
	doctorStatusPass doctorCheckStatus = "pass"
	doctorStatusWarn doctorCheckStatus = "warn"
	doctorStatusFail doctorCheckStatus = "fail"
	doctorStatusSkip doctorCheckStatus = "skip"
)

type doctorCheckResult struct {
	Check      string            `json:"check"`
	Status     doctorCheckStatus `json:"status"`
	Evidence   string            `json:"evidence"`
	Suggestion string            `json:"suggestion"`
}

type doctorResponse struct {
	NodeID      uint                `json:"node_id"`
	NodeName    string              `json:"node_name"`
	GeneratedAt time.Time           `json:"generated_at"`
	Checks      []doctorCheckResult `json:"checks"`
}

// RunDoctor godoc
// @Summary      运行节点 SSH Fleet Doctor
// @Description  执行服务端 allowlist 只读诊断，覆盖 SSH、known_hosts、sudo、工具、备份目录、磁盘和探针状态；不接受请求体或自定义命令。
// @Tags         nodes
// @Security     Bearer
// @Produce      json
// @Param        id   path      int  true  "节点 ID"
// @Success      200  {object}  handlers.Response{data=doctorResponse}
// @Failure      400  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Failure      403  {object}  handlers.Response
// @Failure      404  {object}  handlers.Response
// @Router       /nodes/{id}/doctor [post]
func (h *NodeHandler) RunDoctor(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}

	if !doctorRequestBodyAllowed(c) {
		respondBadRequest(c, "Doctor 仅支持服务端 allowlist 诊断，不接受自定义命令或检查项")
		return
	}

	var node model.Node
	if err := h.db.Preload("SSHKey").First(&node, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			respondNotFound(c, "节点不存在")
			return
		}
		respondInternalError(c, err)
		return
	}

	result, credential, err := h.runNodeDoctor(c.Request.Context(), node)
	if err != nil {
		h.writeNodeDoctorAudit(c, node, credential, credentialaudit.OutcomeFailure, "run", err, nil)
		respondInternalError(c, err)
		return
	}
	h.writeNodeDoctorAudit(c, node, credential, doctorAuditOutcome(result.Checks), "complete", nil, result.Checks)
	respondOK(c, result)
}

func (h *NodeHandler) runNodeDoctor(ctx context.Context, node model.Node) (doctorResponse, sshutil.ResolvedCredential, error) {
	runner := &nodeDoctorRunner{
		db:          h.db,
		settingsSvc: h.settingsSvc,
		node:        node,
		now:         time.Now().UTC(),
	}
	return runner.run(ctx)
}

func doctorRequestBodyAllowed(c *gin.Context) bool {
	if c.Request.Body == nil || c.Request.Body == http.NoBody || c.Request.ContentLength == 0 {
		return true
	}
	if c.Request.ContentLength > 0 {
		return false
	}
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, 1))
	if err != nil {
		return false
	}
	return len(body) == 0
}

type nodeDoctorRunner struct {
	db          *gorm.DB
	settingsSvc *settings.Service
	node        model.Node
	now         time.Time
	checks      []doctorCheckResult
}

func (r *nodeDoctorRunner) run(ctx context.Context) (doctorResponse, sshutil.ResolvedCredential, error) {
	r.checkAuthConfig()
	authMethods, _, credential, authErr := sshutil.BuildSSHAuthWithKeyForPurpose(r.node, r.db, sshutil.PurposeNodeTest)
	if authErr != nil {
		r.add("ssh", doctorStatusFail, "SSH 认证配置无效", "检查节点认证方式、密码或绑定的 SSH Key。")
		r.add("known_hosts", doctorStatusSkip, "未建立 SSH 连接，跳过主机密钥校验", "先修复认证配置后重新运行 Doctor。")
		r.addSSHDependentSkips()
		return r.response(), credential, nil
	}

	callback, knownHostsEvidence, knownHostsStatus, knownHostsSuggestion := resolveDoctorHostKeyCallback()
	if knownHostsStatus != "" && knownHostsStatus == doctorStatusFail {
		r.add("known_hosts", knownHostsStatus, knownHostsEvidence, knownHostsSuggestion)
		r.add("ssh", doctorStatusSkip, "known_hosts 配置不可用，未尝试 SSH 连接", "修复 known_hosts 配置后重新运行 Doctor。")
		r.addSSHDependentSkips()
		return r.response(), credential, nil
	}

	client, latency, sshEvidence, sshStatus, sshSuggestion := r.dial(ctx, authMethods, callback)
	if client == nil {
		if classifyDoctorSSHEvidence(errors.New(sshEvidence)) == "known_hosts 校验失败或主机密钥冲突" || strings.Contains(sshEvidence, "known_hosts") {
			r.add("known_hosts", doctorStatusFail, sshEvidence, sshSuggestion)
			r.add("ssh", doctorStatusSkip, "known_hosts 校验失败，未建立可信 SSH 连接", "先处理主机密钥冲突后重新运行 Doctor。")
		} else {
			r.add("ssh", sshStatus, sshEvidence, sshSuggestion)
			if knownHostsStatus != "" {
				r.add("known_hosts", knownHostsStatus, knownHostsEvidence, knownHostsSuggestion)
			}
		}
		r.addSSHDependentSkips()
		return r.response(), credential, nil
	}
	defer client.Close() //nolint:errcheck // close error is not actionable after diagnostics

	r.add("ssh", doctorStatusPass, fmt.Sprintf("SSH 握手成功，延迟约 %d ms", latency), "连接正常，无需处理。")
	if knownHostsStatus != "" {
		r.add("known_hosts", knownHostsStatus, knownHostsEvidence, knownHostsSuggestion)
	}

	r.checkSudo(ctx, client)
	r.checkTools(ctx, client)
	r.checkBackupDirectories(ctx, client)
	r.checkDisk(ctx, client)
	r.checkProbeStatus()

	return r.response(), credential, nil
}

func (h *NodeHandler) writeNodeDoctorAudit(c *gin.Context, node model.Node, credential sshutil.ResolvedCredential, outcome, stage string, err error, checks []doctorCheckResult) {
	fallbackKind, fallbackSource, fallbackKeyID := nodeCredentialFallback(node)
	kind, source, keyID := eventCredentialFields(credential, fallbackKind, fallbackSource)
	if keyID == nil {
		keyID = fallbackKeyID
	}
	metadata := map[string]any{
		"stage": stage,
	}
	if len(checks) > 0 {
		passCount, warnCount, failCount, skipCount := doctorCheckCounts(checks)
		metadata["check_count"] = len(checks)
		metadata["pass_count"] = passCount
		metadata["warn_count"] = warnCount
		metadata["failure_count"] = failCount
		metadata["skip_count"] = skipCount
	}
	event := credentialaudit.Event{
		Action:           "node.doctor.run",
		Purpose:          sshutil.PurposeNodeTest,
		CredentialKind:   kind,
		CredentialSource: source,
		SSHKeyID:         keyID,
		NodeID:           credentialaudit.PtrUint(node.ID),
		Outcome:          outcome,
		Metadata:         metadata,
	}
	if err != nil {
		event.ErrorMessage = credentialAuditSafeError(stage, err)
	}
	writeCredentialAuditFromGin(c, h.db, event)
}

func doctorAuditOutcome(checks []doctorCheckResult) string {
	_, _, failCount, _ := doctorCheckCounts(checks)
	if failCount == 0 {
		return credentialaudit.OutcomeSuccess
	}
	for _, check := range checks {
		if check.Status != doctorStatusFail {
			continue
		}
		if check.Check == "auth" || (check.Check == "ssh" && strings.Contains(check.Evidence, "认证")) {
			return credentialaudit.OutcomeBlocked
		}
	}
	return credentialaudit.OutcomeFailure
}

func doctorCheckCounts(checks []doctorCheckResult) (passCount, warnCount, failCount, skipCount int) {
	for _, check := range checks {
		switch check.Status {
		case doctorStatusPass:
			passCount++
		case doctorStatusWarn:
			warnCount++
		case doctorStatusFail:
			failCount++
		case doctorStatusSkip:
			skipCount++
		}
	}
	return passCount, warnCount, failCount, skipCount
}

func (r *nodeDoctorRunner) response() doctorResponse {
	return doctorResponse{
		NodeID:      r.node.ID,
		NodeName:    r.node.Name,
		GeneratedAt: r.now,
		Checks:      r.checks,
	}
}

func (r *nodeDoctorRunner) add(check string, status doctorCheckStatus, evidence string, suggestion string) {
	r.checks = append(r.checks, doctorCheckResult{
		Check:      check,
		Status:     status,
		Evidence:   sanitizeDoctorEvidence(evidence),
		Suggestion: sanitizeDoctorEvidence(suggestion),
	})
}

func (r *nodeDoctorRunner) checkAuthConfig() {
	switch r.node.AuthType {
	case "password":
		if strings.TrimSpace(r.node.Password) == "" {
			r.add("auth", doctorStatusFail, "节点使用密码认证，但未保存密码", "编辑节点并填写 SSH 密码，或切换到密钥认证。")
			return
		}
		r.add("auth", doctorStatusPass, "节点配置为密码认证，已保存密码凭据", "认证配置完整。")
	case "key":
		if r.node.SSHKeyID != nil {
			r.add("auth", doctorStatusPass, fmt.Sprintf("节点绑定 SSH Key #%d", *r.node.SSHKeyID), "认证配置完整。")
			return
		}
		if strings.TrimSpace(r.node.PrivateKey) != "" {
			r.add("auth", doctorStatusPass, "节点使用内联私钥认证", "建议优先使用集中管理的 SSH Key，便于轮换。")
			return
		}
		r.add("auth", doctorStatusFail, "节点使用密钥认证，但未绑定 SSH Key 或内联私钥", "编辑节点并选择 SSH Key。")
	default:
		r.add("auth", doctorStatusFail, "节点认证方式不受支持", "编辑节点并选择密码或密钥认证。")
	}
}

func resolveDoctorHostKeyCallback() (ssh.HostKeyCallback, string, doctorCheckStatus, string) {
	strictHostCheck, err := util.ReadBoolEnv("SSH_STRICT_HOST_KEY_CHECKING", true)
	if err != nil {
		return nil, "无法读取 SSH 主机密钥校验配置", doctorStatusFail, "检查 SSH_STRICT_HOST_KEY_CHECKING 配置。"
	}
	if !strictHostCheck {
		return ssh.InsecureIgnoreHostKey(), "SSH 主机密钥校验已关闭", doctorStatusWarn, "生产环境建议启用 SSH_STRICT_HOST_KEY_CHECKING=true。"
	}

	rawPath := strings.TrimSpace(util.GetEnvOrDefault("SSH_KNOWN_HOSTS_PATH", "~/.ssh/known_hosts"))
	knownHostsPath, err := util.ExpandHomePath(rawPath)
	if err != nil || strings.TrimSpace(knownHostsPath) == "" {
		return nil, "known_hosts 路径配置不可用", doctorStatusFail, "检查 SSH_KNOWN_HOSTS_PATH 配置。"
	}
	callback, err := knownhosts.New(knownHostsPath)
	if err != nil {
		return nil, "known_hosts 文件无法加载", doctorStatusFail, "确认 known_hosts 文件存在且格式正确。"
	}
	return callback, "known_hosts 严格校验已启用", doctorStatusPass, "主机密钥校验配置正常。"
}

func (r *nodeDoctorRunner) dial(ctx context.Context, authMethods []ssh.AuthMethod, callback ssh.HostKeyCallback) (*ssh.Client, int, string, doctorCheckStatus, string) {
	address := net.JoinHostPort(r.node.Host, strconv.Itoa(r.node.Port))
	start := time.Now()
	client, err := sshutil.DialSSH(ctx, address, r.node.Username, authMethods, callback)
	latency := int(time.Since(start).Milliseconds())
	if latency <= 0 {
		latency = 1
	}
	if err == nil {
		return client, latency, "", doctorStatusPass, ""
	}
	return nil, latency, classifyDoctorSSHEvidence(err), doctorStatusFail, classifyDoctorSSHSuggestion(err)
}

func classifyDoctorSSHEvidence(err error) string {
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "knownhosts") || strings.Contains(msg, "host key") || strings.Contains(msg, "主机密钥") {
		return "known_hosts 校验失败或主机密钥冲突"
	}
	if strings.Contains(msg, "unable to authenticate") || strings.Contains(msg, "permission denied") || strings.Contains(msg, "no supported methods remain") || strings.Contains(msg, "认证") {
		return "SSH 认证失败"
	}
	if strings.Contains(msg, "connection refused") || strings.Contains(msg, "no route") || strings.Contains(msg, "network is unreachable") || strings.Contains(msg, "i/o timeout") || strings.Contains(msg, "连接失败") {
		return "SSH 网络连接失败或端口不可达"
	}
	if strings.Contains(msg, "handshake") || strings.Contains(msg, "握手") {
		return "SSH 握手失败"
	}
	return "SSH 连接失败"
}

func classifyDoctorSSHSuggestion(err error) string {
	evidence := classifyDoctorSSHEvidence(err)
	switch evidence {
	case "known_hosts 校验失败或主机密钥冲突":
		return "核对服务器指纹；确认变更可信后更新 known_hosts。"
	case "SSH 认证失败":
		return "检查用户名、密码、SSH Key、公钥授权和 sudo 用户登录权限。"
	case "SSH 网络连接失败或端口不可达":
		return "检查节点地址、端口、防火墙、安全组和 SSH 服务状态。"
	case "SSH 握手失败":
		return "检查 SSH 服务端算法兼容性、主机密钥和登录策略。"
	default:
		return "检查节点 SSH 配置后重新运行 Doctor。"
	}
}

func (r *nodeDoctorRunner) addSSHDependentSkips() {
	r.add("sudo", doctorStatusSkip, "未建立 SSH 连接，跳过 sudo 检查", "先修复 SSH 连接。")
	r.add("tools", doctorStatusSkip, "未建立 SSH 连接，跳过工具检查", "先修复 SSH 连接。")
	r.add("backup_dir", doctorStatusSkip, "未建立 SSH 连接，跳过备份目录检查", "先修复 SSH 连接。")
	r.add("disk", doctorStatusSkip, "未建立 SSH 连接，跳过磁盘空间检查", "先修复 SSH 连接。")
	r.checkProbeStatus()
}

func (r *nodeDoctorRunner) checkSudo(ctx context.Context, client *ssh.Client) {
	if !executor.NeedsSudo(r.node) {
		r.add("sudo", doctorStatusSkip, "节点未启用 sudo 执行或当前用户为 root", "无需 sudo 检查。")
		return
	}
	output, err := runDoctorCommand(ctx, client, "sudo -n true 2>&1")
	if err != nil {
		r.add("sudo", doctorStatusFail, classifyDoctorSudoEvidence(output), "为该用户配置 NOPASSWD sudo，或关闭节点 sudo 执行。")
		return
	}
	r.add("sudo", doctorStatusPass, "sudo 非交互检查通过", "sudo 可用。")
}

func classifyDoctorSudoEvidence(output string) string {
	msg := strings.ToLower(strings.TrimSpace(output))
	if msg == "" {
		return "sudo 非交互检查失败"
	}
	if strings.Contains(msg, "password") || strings.Contains(msg, "askpass") || strings.Contains(msg, "tty") {
		return "sudo 需要交互式密码或终端，非交互检查失败"
	}
	if strings.Contains(msg, "not in the sudoers") || strings.Contains(msg, "not allowed") || strings.Contains(msg, "permission denied") {
		return "当前用户未授予 sudo 权限"
	}
	if strings.Contains(msg, "not found") || strings.Contains(msg, "command not found") {
		return "sudo 工具不可用"
	}
	return "sudo 非交互检查失败"
}

func (r *nodeDoctorRunner) checkTools(ctx context.Context, client *ssh.Client) {
	required, err := r.requiredTools()
	if err != nil {
		r.add("tools", doctorStatusWarn, "工具需求查询失败，按基础工具检查", "检查任务配置后重新运行 Doctor。")
		required = []string{"sh", "df", "awk"}
	}
	missing := make([]string, 0)
	present := make([]string, 0, len(required))
	for _, tool := range required {
		cmd := fmt.Sprintf("command -v %s >/dev/null 2>&1", executor.ShellEscape(tool))
		if _, err := runDoctorCommand(ctx, client, cmd); err != nil {
			missing = append(missing, tool)
			continue
		}
		present = append(present, tool)
	}
	if len(missing) > 0 {
		r.add("tools", doctorStatusFail, fmt.Sprintf("缺失工具：%s；已发现：%s", strings.Join(missing, ", "), strings.Join(present, ", ")), "安装缺失工具后重新运行备份任务。")
		return
	}
	r.add("tools", doctorStatusPass, fmt.Sprintf("必需工具可用：%s", strings.Join(present, ", ")), "工具检查通过。")
}

func (r *nodeDoctorRunner) requiredTools() ([]string, error) {
	tools := map[string]struct{}{
		"sh":  {},
		"df":  {},
		"awk": {},
	}
	var tasks []model.Task
	if err := r.db.Select("executor_type").Where("node_id = ? AND enabled = ?", r.node.ID, true).Find(&tasks).Error; err != nil {
		return sortedDoctorKeys(tools), err
	}
	for _, task := range tasks {
		switch task.ExecutorType {
		case "rsync":
			tools["rsync"] = struct{}{}
		case "restic":
			tools["restic"] = struct{}{}
		case "rclone":
			tools["rclone"] = struct{}{}
		}
	}
	return sortedDoctorKeys(tools), nil
}

func sortedDoctorKeys(values map[string]struct{}) []string {
	preferred := []string{"sh", "df", "awk", "rsync", "restic", "rclone"}
	out := make([]string, 0, len(values))
	for _, key := range preferred {
		if _, ok := values[key]; ok {
			out = append(out, key)
			delete(values, key)
		}
	}
	for key := range values {
		out = append(out, key)
	}
	return out
}

func (r *nodeDoctorRunner) checkBackupDirectories(ctx context.Context, client *ssh.Client) {
	paths, err := r.backupPaths()
	if err != nil {
		r.add("backup_dir", doctorStatusWarn, "备份目录查询失败", "检查策略配置后重新运行 Doctor。")
		return
	}
	if len(paths) == 0 {
		r.add("backup_dir", doctorStatusSkip, "该节点暂无本地备份目录配置", "为节点配置本地 rsync 策略后可检查目录。")
		return
	}

	missing := make([]string, 0)
	unwritable := make([]string, 0)
	okCount := 0
	for _, path := range paths {
		cmd := fmt.Sprintf("test -d %s && test -w %s", executor.ShellEscape(path), executor.ShellEscape(path))
		if _, err := runDoctorCommand(ctx, client, cmd); err == nil {
			okCount++
			continue
		}
		if _, statErr := runDoctorCommand(ctx, client, fmt.Sprintf("test -d %s", executor.ShellEscape(path))); statErr != nil {
			missing = append(missing, path)
			continue
		}
		unwritable = append(unwritable, path)
	}

	if len(missing) > 0 || len(unwritable) > 0 {
		parts := []string{fmt.Sprintf("通过 %d 个目录", okCount)}
		if len(missing) > 0 {
			parts = append(parts, fmt.Sprintf("不存在：%d 个目录", len(missing)))
		}
		if len(unwritable) > 0 {
			parts = append(parts, fmt.Sprintf("不可写：%d 个目录", len(unwritable)))
		}
		r.add("backup_dir", doctorStatusFail, strings.Join(parts, "；"), "预先创建备份目录并授予备份用户写权限；Doctor 不会自动创建目录。")
		return
	}
	r.add("backup_dir", doctorStatusPass, fmt.Sprintf("%d 个备份目录存在且当前用户可写", okCount), "目录检查通过。")
}

func (r *nodeDoctorRunner) backupPaths() ([]string, error) {
	var tasks []model.Task
	if err := r.db.Select("rsync_target, executor_type").Where("node_id = ? AND enabled = ?", r.node.ID, true).Find(&tasks).Error; err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	paths := make([]string, 0)
	for _, task := range tasks {
		if task.ExecutorType != "rsync" {
			continue
		}
		path := strings.TrimSpace(task.RsyncTarget)
		if path == "" || strings.Contains(path, ":") || seen[path] {
			continue
		}
		seen[path] = true
		paths = append(paths, path)
	}
	if len(paths) > 0 {
		return paths, nil
	}

	var policies []model.Policy
	if err := r.db.Joins("JOIN policy_nodes ON policy_nodes.policy_id = policies.id").
		Where("policy_nodes.node_id = ? AND policies.enabled = ? AND policies.is_template = ?", r.node.ID, true, false).
		Find(&policies).Error; err != nil {
		return nil, err
	}
	for _, p := range policies {
		basePath := strings.TrimSpace(p.TargetPath)
		if basePath == "" || strings.Contains(basePath, ":") {
			continue
		}
		path := policy.NodeTargetPath(basePath, r.node.BackupDir)
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		paths = append(paths, path)
	}
	return paths, nil
}

func (r *nodeDoctorRunner) checkDisk(ctx context.Context, client *ssh.Client) {
	output, err := runDoctorCommand(ctx, client, "df -BG / | awk 'NR==2 {print $2\" \"$3}'")
	if err != nil {
		r.add("disk", doctorStatusWarn, "无法读取根文件系统磁盘空间", "确认 df/awk 可用，或检查 SSH 用户权限。")
		return
	}
	used, total, ok := sshutil.ParseDiskProbe(output)
	if !ok || total <= 0 {
		r.add("disk", doctorStatusWarn, "磁盘空间输出无法解析", "确认远端 df 输出格式兼容。")
		return
	}
	free := total - used
	usagePct := float64(used) / float64(total) * 100
	minFreeGB := r.doctorMinFreeGB()
	if free < minFreeGB || usagePct >= 90 {
		r.add("disk", doctorStatusFail, fmt.Sprintf("根文件系统剩余 %dGB / 总计 %dGB，使用率 %.0f%%", free, total, usagePct), "清理空间或扩容后再运行备份。")
		return
	}
	if free < minFreeGB*2 || usagePct >= 80 {
		r.add("disk", doctorStatusWarn, fmt.Sprintf("根文件系统剩余 %dGB / 总计 %dGB，使用率 %.0f%%", free, total, usagePct), "磁盘空间接近阈值，建议规划扩容或清理。")
		return
	}
	r.add("disk", doctorStatusPass, fmt.Sprintf("根文件系统剩余 %dGB / 总计 %dGB，使用率 %.0f%%", free, total, usagePct), "磁盘空间充足。")
}

func (r *nodeDoctorRunner) doctorMinFreeGB() int {
	minFreeGB := 10
	if r.settingsSvc != nil {
		if v, err := strconv.Atoi(r.settingsSvc.GetEffective("storage.min_free_gb")); err == nil && v >= 0 {
			return v
		}
	}
	if raw := util.GetEnvOrDefault("BACKUP_STORAGE_MIN_FREE_GB", "10"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v >= 0 {
			minFreeGB = v
		}
	}
	return minFreeGB
}

func (r *nodeDoctorRunner) checkProbeStatus() {
	if r.node.LastProbeAt == nil {
		r.add("probe", doctorStatusWarn, "节点尚无探针采样记录", "确认后台 Prober 正在运行，并稍后重新检查。")
		return
	}
	age := r.now.Sub(r.node.LastProbeAt.UTC())
	if r.node.Status == "offline" || r.node.ConsecutiveFailures > 0 {
		r.add("probe", doctorStatusFail, fmt.Sprintf("最近探针状态为 %s，连续失败 %d 次", r.node.Status, r.node.ConsecutiveFailures), "修复 SSH 或系统指标采集问题后等待下一次探针。")
		return
	}
	if age > 30*time.Minute {
		r.add("probe", doctorStatusWarn, fmt.Sprintf("最近探针采样距今约 %d 分钟", int(age.Minutes())), "确认 Prober 调度正常。")
		return
	}
	r.add("probe", doctorStatusPass, fmt.Sprintf("最近探针采样距今约 %d 分钟", int(age.Minutes())), "探针状态正常。")
}

func runDoctorCommand(ctx context.Context, client *ssh.Client, command string) (string, error) {
	if !doctorCommandAllowed(command) {
		return "", fmt.Errorf("diagnostic command is not allowlisted")
	}
	session, err := client.NewSession()
	if err != nil {
		return "", err
	}
	defer session.Close() //nolint:errcheck // close error is not actionable after command result

	type result struct {
		output []byte
		err    error
	}
	done := make(chan result, 1)
	go func() {
		output, runErr := session.CombinedOutput(command)
		done <- result{output: output, err: runErr}
	}()

	select {
	case <-ctx.Done():
		_ = session.Signal(ssh.SIGKILL)
		return "", ctx.Err()
	case res := <-done:
		return string(res.output), res.err
	case <-time.After(8 * time.Second):
		_ = session.Signal(ssh.SIGKILL)
		return "", errors.New("diagnostic command timed out")
	}
}

func doctorCommandAllowed(command string) bool {
	trimmed := strings.TrimSpace(command)
	if trimmed == "sudo -n true 2>&1" || trimmed == "df -BG / | awk 'NR==2 {print $2\" \"$3}'" {
		return true
	}
	if strings.HasPrefix(trimmed, "command -v ") && strings.HasSuffix(trimmed, " >/dev/null 2>&1") {
		tool := strings.TrimSuffix(strings.TrimPrefix(trimmed, "command -v "), " >/dev/null 2>&1")
		return isShellEscapedSingleArg(tool) && isDoctorAllowedTool(unquoteDoctorShellArg(tool))
	}
	if strings.HasPrefix(trimmed, "test -d ") {
		payload := strings.TrimPrefix(trimmed, "test -d ")
		parts := strings.Split(payload, " && test -w ")
		if len(parts) == 1 {
			path := unquoteDoctorShellArg(parts[0])
			return path != "" && isDoctorSafePath(path)
		}
		if len(parts) == 2 {
			path := unquoteDoctorShellArg(parts[0])
			return path != "" && path == unquoteDoctorShellArg(parts[1]) && isDoctorSafePath(path)
		}
	}
	return false
}

func isShellEscapedSingleArg(value string) bool {
	if len(value) < 2 || value[0] != '\'' {
		return false
	}
	i := 0
	for i < len(value) {
		if value[i] != '\'' {
			return false
		}
		i++
		for i < len(value) && value[i] != '\'' {
			if value[i] == '\n' || value[i] == 0 {
				return false
			}
			i++
		}
		if i >= len(value) {
			return false
		}
		i++
		if i == len(value) {
			return true
		}
		if i+2 >= len(value) || value[i] != '\\' || value[i+1] != '\'' || value[i+2] != '\'' {
			return false
		}
		i += 2
	}
	return false
}

func unquoteDoctorShellArg(value string) string {
	if !isShellEscapedSingleArg(value) {
		return ""
	}
	inner := strings.TrimPrefix(strings.TrimSuffix(value, "'"), "'")
	return strings.ReplaceAll(inner, "'\\''", "'")
}

func isDoctorAllowedTool(tool string) bool {
	switch tool {
	case "sh", "df", "awk", "rsync", "restic", "rclone":
		return true
	default:
		return false
	}
}

func isDoctorSafePath(rawPath string) bool {
	if !strings.HasPrefix(rawPath, "/") || strings.Contains(rawPath, "\x00") || strings.Contains(rawPath, "\n") || strings.Contains(rawPath, "\r") {
		return false
	}
	for _, segment := range strings.Split(rawPath, "/") {
		if segment == ".." {
			return false
		}
	}
	cleaned := path.Clean(rawPath)
	return cleaned != "/" && cleaned != "."
}

func sanitizeDoctorEvidence(input string) string {
	return sanitizeDiagnosticEvidence(input)
}

func sanitizeDiagnosticEvidence(input string) string {
	value := strings.TrimSpace(input)
	if value == "" {
		return ""
	}
	if diagnosticEvidenceHasSensitiveMarker(value) {
		return "诊断输出包含敏感模式，已隐藏。"
	}
	value = redactDiagnosticCommandText(value)
	value = redactDiagnosticOutput(value)
	value = util.SanitizeMessage(value)
	value = diagnosticRemotePathPattern.ReplaceAllString(value, "$1[REMOTE_PATH_REDACTED]")
	value = diagnosticHomePathPattern.ReplaceAllString(value, "$1[PATH_REDACTED]")
	value = diagnosticAbsolutePathPattern.ReplaceAllString(value, "$1[PATH_REDACTED]")
	value = diagnosticEndpointPattern.ReplaceAllString(value, "$1=[ENDPOINT_REDACTED]")
	value = diagnosticIPv6Pattern.ReplaceAllString(value, "[HOST_REDACTED]")
	value = diagnosticIPv4Pattern.ReplaceAllString(value, "[HOST_REDACTED]")
	value = diagnosticHostnamePattern.ReplaceAllString(value, "[HOST_REDACTED]")
	value = diagnosticHostValuePattern.ReplaceAllString(value, "$1=[HOST_REDACTED]")
	value = diagnosticProxyValuePattern.ReplaceAllString(value, "$1=[ENDPOINT_REDACTED]")
	value = strings.TrimSpace(value)
	if len([]rune(value)) > 500 {
		value = string([]rune(value)[:500]) + "…"
	}
	return value
}

func diagnosticEvidenceHasSensitiveMarker(value string) bool {
	secretMarkers := []string{
		"-----BEGIN", "PRIVATE KEY", "password", "passwd=", "passwd:", "token", "secret", "authorization", "bearer", "proxy_url", "proxy=", "DATA_ENCRYPTION_KEY",
		"otp", "totp", "recovery code", "step-up", "step_up", "executor_config", "terminal stream", "docker output", "import payload", "export payload",
	}
	lower := strings.ToLower(value)
	for _, marker := range secretMarkers {
		if strings.Contains(lower, strings.ToLower(marker)) {
			return true
		}
	}
	return false
}

func redactDiagnosticOutput(value string) string {
	return redactDiagnosticAfterMarker(value, []string{"输出:", "output:", "stdout:", "stderr:", "command output:", "diagnostic output:"}, "[REDACTED_OUTPUT]")
}

func redactDiagnosticCommandText(value string) string {
	return redactDiagnosticAfterMarker(value, []string{"命令:", "command:", "cmd:"}, "[REDACTED_COMMAND]")
}

func redactDiagnosticAfterMarker(value string, markers []string, placeholder string) string {
	lower := strings.ToLower(value)
	cut := -1
	for _, marker := range markers {
		idx := strings.Index(lower, strings.ToLower(marker))
		if idx >= 0 && (cut == -1 || idx < cut) {
			cut = idx + len(marker)
		}
	}
	if cut == -1 {
		return value
	}
	return strings.TrimSpace(value[:cut]) + " " + placeholder
}

var (
	diagnosticRemotePathPattern   = regexp.MustCompile(`(^|[\s（(：:，,；;=])(?:[A-Za-z0-9._~-]+@)?[A-Za-z0-9][A-Za-z0-9._-]*(?::\d+)?:(?:/(?:[^\s，,；;)）]+)|~(?:/[^\s，,；;)）]*)?)`)
	diagnosticHomePathPattern     = regexp.MustCompile(`(^|[\s（(：:，,；;=])~(?:/[^\s，,；;)）]*)?`)
	diagnosticAbsolutePathPattern = regexp.MustCompile(`(^|[\s（(：:，,；;=])/(?:[^\s，,；;)）]+)`)
	diagnosticEndpointPattern     = regexp.MustCompile(`(?i)\b(host|hostname|endpoint|proxy|proxy_url|addr|address|server|url)\s*[=:]\s*[^\s，,；;)）]+`)
	diagnosticHostValuePattern    = regexp.MustCompile(`(?i)\b(host|hostname|addr|address|server)\s*=\s*[^\s，,；;)）]+`)
	diagnosticProxyValuePattern   = regexp.MustCompile(`(?i)\b(proxy|proxy_url|endpoint|url)\s*=\s*[^\s，,；;)）]+`)
	diagnosticIPv4Pattern         = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}(?::\d+)?\b`)
	diagnosticIPv6Pattern         = regexp.MustCompile(`\[[0-9A-Fa-f:]+\](?::\d+)?|\b[0-9A-Fa-f]{0,4}:[0-9A-Fa-f:]{2,}(?::\d+)?\b`)
	diagnosticHostnamePattern     = regexp.MustCompile(`(?i)\b[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+(?:\.?)\b(?::\d+)?`)
)
