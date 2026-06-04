package handlers

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"xirang/backend/internal/apperr"
	"xirang/backend/internal/credentialaudit"
	"xirang/backend/internal/model"
	"xirang/backend/internal/sshutil"

	"xirang/backend/internal/logger"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/ssh"
	"gorm.io/gorm"
)

// DockerVolume 表示一个 Docker 卷。
type DockerVolume struct {
	Name       string `json:"name"`
	Driver     string `json:"driver"`
	Mountpoint string `json:"mountpoint"`
}

// DockerHandler 处理 Docker 相关请求。
type DockerHandler struct {
	db *gorm.DB
}

func NewDockerHandler(db *gorm.DB) *DockerHandler {
	return &DockerHandler{db: db}
}

// ListVolumes godoc
// @Summary      列出 Docker 卷
// @Description  通过 SSH 列举远端节点上的 Docker 卷
// @Tags         docker
// @Security     Bearer
// @Produce      json
// @Param        id  path      int  true  "节点 ID"
// @Success      200  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Failure      404  {object}  handlers.Response
// @Failure      502  {object}  handlers.Response
// @Router       /nodes/{id}/docker-volumes [get]
func (h *DockerHandler) ListVolumes(c *gin.Context) {
	nodeID, ok := parseID(c, "id")
	if !ok {
		return
	}

	var node model.Node
	if err := h.db.Preload("SSHKey").First(&node, nodeID).Error; err != nil {
		respondNotFound(c, "节点不存在")
		return
	}

	sshClient, credential, err := dialSSHForDocker(c.Request.Context(), node, h.db)
	if err != nil {
		h.writeDockerVolumeAudit(c, node, credential, credentialAuditSSHOutcome("dial", err), "dial", err, 0, false)
		respondBadGateway(c, "SSH 连接失败")
		return
	}
	defer sshClient.Close() //nolint:errcheck // close error not actionable on deferred cleanup

	volumes, warning, err := listDockerVolumes(sshClient)
	if err != nil {
		if !errors.Is(err, apperr.ErrNotFound) {
			logger.Log.Error().Err(err).Msg("获取 Docker 卷失败")
			h.writeDockerVolumeAudit(c, node, credential, credentialaudit.OutcomeFailure, "list", err, 0, false)
			respondOK(c, gin.H{"data": []DockerVolume{}, "warning": "获取 Docker 卷失败"})
			return
		}
		// Docker 未安装 — 使用 warning 继续
	}

	volumeOutcome := credentialaudit.OutcomeSuccess
	if warning != "" {
		volumeOutcome = credentialaudit.OutcomeFailure
	}
	h.writeDockerVolumeAudit(c, node, credential, volumeOutcome, "success", nil, len(volumes), warning != "")

	resp := gin.H{"data": volumes}
	if warning != "" {
		resp["warning"] = warning
	}
	respondOK(c, resp)
}

func (h *DockerHandler) writeDockerVolumeAudit(c *gin.Context, node model.Node, credential sshutil.ResolvedCredential, outcome, stage string, err error, count int, warning bool) {
	fallbackKind, fallbackSource, fallbackKeyID := nodeCredentialFallback(node)
	kind, source, keyID := eventCredentialFields(credential, fallbackKind, fallbackSource)
	if keyID == nil {
		keyID = fallbackKeyID
	}
	event := credentialaudit.Event{
		Action:           "docker_volumes.discover",
		Purpose:          sshutil.PurposeDockerVolumes,
		CredentialKind:   kind,
		CredentialSource: source,
		SSHKeyID:         keyID,
		NodeID:           credentialaudit.PtrUint(node.ID),
		Outcome:          outcome,
		Metadata: map[string]any{
			"stage":       stage,
			"count":       count,
			"has_warning": warning,
		},
	}
	if err != nil {
		event.ErrorMessage = credentialAuditSafeError(stage, err)
	}
	writeCredentialAuditFromGin(c, h.db, event)
}

// dialSSHForDocker 建立 SSH 连接，用于执行 Docker 命令。
func dialSSHForDocker(ctx context.Context, node model.Node, db *gorm.DB) (*ssh.Client, sshutil.ResolvedCredential, error) {
	auth, credential, err := sshutil.BuildSSHAuthForPurpose(node, db, sshutil.PurposeDockerVolumes)
	if err != nil {
		return nil, credential, err
	}
	hostKey, err := sshutil.ResolveSSHHostKeyCallback()
	if err != nil {
		return nil, credential, err
	}

	dialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	addr := fmt.Sprintf("%s:%d", node.Host, node.Port)
	client, err := sshutil.DialSSH(dialCtx, addr, node.Username, auth, hostKey)
	return client, credential, err
}

// dockerVolumeLsEntry 用于解析 docker volume ls --format '{{json .}}' 的输出。
type dockerVolumeLsEntry struct {
	Driver     string `json:"Driver"`
	Name       string `json:"Name"`
	Mountpoint string `json:"Mountpoint"`
}

// listDockerVolumes 通过 SSH 执行 docker 命令获取卷列表。
func listDockerVolumes(client *ssh.Client) ([]DockerVolume, string, error) {
	// 先执行 docker volume ls 获取卷列表
	session, err := client.NewSession()
	if err != nil {
		return nil, "", fmt.Errorf("创建 SSH 会话失败: %w", err)
	}
	output, err := session.CombinedOutput("docker volume ls --format '{{json .}}'")
	_ = session.Close()

	if err != nil {
		outStr := strings.TrimSpace(string(output))
		// Docker 未安装或无权限
		if strings.Contains(outStr, "command not found") || strings.Contains(outStr, "not found") {
			return []DockerVolume{}, "Docker 未安装或不在 PATH 中", apperr.ErrNotFound
		}
		if strings.Contains(outStr, "permission denied") || strings.Contains(outStr, "Cannot connect") {
			return []DockerVolume{}, "无权访问 Docker（当前用户可能不在 docker 组中）", nil
		}
		return []DockerVolume{}, "执行 docker volume ls 失败", nil
	}

	// 解析 JSON 行
	var entries []dockerVolumeLsEntry
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var entry dockerVolumeLsEntry
		if jsonErr := json.Unmarshal([]byte(line), &entry); jsonErr == nil {
			entries = append(entries, entry)
		}
	}

	if len(entries) == 0 {
		return []DockerVolume{}, "", nil
	}

	// 对每个卷获取 mountpoint（ls 的 json 格式可能不包含 Mountpoint）
	volumes := make([]DockerVolume, 0, len(entries))
	for _, entry := range entries {
		mountpoint := entry.Mountpoint
		if mountpoint == "" {
			mountpoint = inspectVolumeMountpoint(client, entry.Name)
		}
		volumes = append(volumes, DockerVolume{
			Name:       entry.Name,
			Driver:     entry.Driver,
			Mountpoint: mountpoint,
		})
	}

	return volumes, "", nil
}

var safeDockerName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.\-]*$`)

// inspectVolumeMountpoint 通过 docker volume inspect 获取卷的挂载点。
func inspectVolumeMountpoint(client *ssh.Client, volumeName string) string {
	if !safeDockerName.MatchString(volumeName) {
		return ""
	}
	session, err := client.NewSession()
	if err != nil {
		return ""
	}
	defer session.Close() //nolint:errcheck // close error not actionable on deferred cleanup

	// 使用 Go template 格式直接输出 Mountpoint
	output, err := session.Output(fmt.Sprintf("docker volume inspect '%s' --format '{{.Mountpoint}}'", volumeName))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}
