package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"xirang/backend/internal/model"
	"xirang/backend/internal/task/executor"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// DiffChange 表示两个快照之间的一个文件变更。
type DiffChange struct {
	Path       string `json:"path"`
	Type       string `json:"type"` // "added", "removed", "changed"
	SizeBefore *int64 `json:"size_before,omitempty"`
	SizeAfter  *int64 `json:"size_after,omitempty"`
}

// DiffStats 表示快照差异的统计信息。
type DiffStats struct {
	Added   int `json:"added"`
	Removed int `json:"removed"`
	Changed int `json:"changed"`
}

// DiffResult 表示两个快照之间的差异结果。
type DiffResult struct {
	Snap1   string       `json:"snap1"`
	Snap2   string       `json:"snap2"`
	Stats   DiffStats    `json:"stats"`
	Changes []DiffChange `json:"changes"`
}

// SnapshotDiffHandler 处理 restic 快照差异比较。
type SnapshotDiffHandler struct {
	db *gorm.DB
}

func NewSnapshotDiffHandler(db *gorm.DB) *SnapshotDiffHandler {
	return &SnapshotDiffHandler{db: db}
}

// snapshotIDPattern 校验快照 ID 格式（十六进制字符串，4-64 位）。
var snapshotIDPattern = regexp.MustCompile(`^[a-fA-F0-9]{4,64}$`)

// Diff 比较两个 restic 快照之间的差异。
// GET /api/v1/tasks/:id/snapshots/diff?snap1=abc123&snap2=def456
func (h *SnapshotDiffHandler) Diff(c *gin.Context) {
	taskID, ok := parseID(c, "id")
	if !ok {
		return
	}

	snap1 := strings.TrimSpace(c.Query("snap1"))
	snap2 := strings.TrimSpace(c.Query("snap2"))
	if snap1 == "" || snap2 == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少 snap1 或 snap2 参数"})
		return
	}
	if !snapshotIDPattern.MatchString(snap1) || !snapshotIDPattern.MatchString(snap2) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "快照 ID 格式无效"})
		return
	}

	var task model.Task
	if err := h.db.Preload("Node").Preload("Node.SSHKey").First(&task, taskID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "任务不存在"})
		return
	}
	if task.ExecutorType != "restic" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "仅 restic 类型任务支持快照比较"})
		return
	}

	repo := strings.TrimSpace(task.RsyncTarget)
	if repo == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "restic 仓库路径为空"})
		return
	}

	cfg, err := parseResticConfigForDiff(task.ExecutorConfig)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "解析 restic 配置失败"})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()

	client, err := executor.DialSSHForNode(ctx, task.Node)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "SSH 连接失败: " + err.Error()})
		return
	}
	defer client.Close()

	envPrefix := buildDiffEnvPrefix(cfg.repositoryPassword)
	resticBin := "restic"
	repoArg := "'" + strings.ReplaceAll(repo, "'", "'\\''") + "'"
	snap1Arg := "'" + strings.ReplaceAll(snap1, "'", "'\\''") + "'"
	snap2Arg := "'" + strings.ReplaceAll(snap2, "'", "'\\''") + "'"

	cmd := fmt.Sprintf("%s %s diff %s %s -r %s 2>&1", envPrefix, resticBin, snap1Arg, snap2Arg, repoArg)
	output, err := executor.RunSSHCommandOutput(ctx, client, cmd)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "执行 restic diff 失败: " + err.Error()})
		return
	}

	result := parseDiffOutput(output, snap1, snap2)
	c.JSON(http.StatusOK, gin.H{"data": result})
}

// parseDiffOutput 解析 restic diff 的文本输出。
// 每行格式：
//
//	+    /path/to/file    (added)
//	-    /path/to/file    (removed)
//	M    /path/to/file    (changed)
//
// 也可能有带大小信息的行：
//
//	+    1.234 KiB /path/to/file
//	M    1.234 KiB 2.345 KiB /path/to/file
func parseDiffOutput(output string, snap1, snap2 string) DiffResult {
	result := DiffResult{
		Snap1:   snap1,
		Snap2:   snap2,
		Changes: []DiffChange{},
	}

	lines := strings.Split(output, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		var change *DiffChange

		switch {
		case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "++"):
			change = parseDiffLine(line, "+", "added")
		case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "--"):
			change = parseDiffLine(line, "-", "removed")
		case strings.HasPrefix(line, "M"):
			change = parseDiffLine(line, "M", "changed")
		}

		if change != nil && change.Path != "" {
			result.Changes = append(result.Changes, *change)
			switch change.Type {
			case "added":
				result.Stats.Added++
			case "removed":
				result.Stats.Removed++
			case "changed":
				result.Stats.Changed++
			}
		}
	}
	return result
}

// parseDiffLine 解析单行 restic diff 输出。
func parseDiffLine(line, prefix, changeType string) *DiffChange {
	// 去掉前缀标记
	rest := strings.TrimSpace(strings.TrimPrefix(line, prefix))
	if rest == "" {
		return nil
	}

	change := &DiffChange{Type: changeType}

	// 尝试解析带大小信息的格式：
	// added:   "1.234 KiB /path"
	// removed: "1.234 KiB /path"
	// changed: "1.234 KiB 2.345 KiB /path"
	parts := strings.Fields(rest)
	if len(parts) == 0 {
		return nil
	}

	// 检查第一个 token 是否像一个数字（大小值）
	if len(parts) >= 3 && looksLikeSize(parts[0]) {
		if changeType == "changed" && len(parts) >= 5 && looksLikeSize(parts[2]) {
			// "1.234 KiB 2.345 KiB /path/to/file"
			sizeBefore := parseHumanSize(parts[0], parts[1])
			sizeAfter := parseHumanSize(parts[2], parts[3])
			change.Path = strings.Join(parts[4:], " ")
			if sizeBefore >= 0 {
				change.SizeBefore = &sizeBefore
			}
			if sizeAfter >= 0 {
				change.SizeAfter = &sizeAfter
			}
		} else {
			// "1.234 KiB /path/to/file"
			size := parseHumanSize(parts[0], parts[1])
			change.Path = strings.Join(parts[2:], " ")
			if size >= 0 {
				if changeType == "added" {
					change.SizeAfter = &size
				} else {
					change.SizeBefore = &size
				}
			}
		}
	} else {
		// 纯路径格式："/path/to/file"
		change.Path = rest
	}

	// 路径必须以 / 开头
	if !strings.HasPrefix(change.Path, "/") {
		return nil
	}

	return change
}

// looksLikeSize 检查一个 token 是否看起来像大小值数字。
func looksLikeSize(s string) bool {
	_, err := strconv.ParseFloat(s, 64)
	return err == nil
}

// parseHumanSize 将人类可读的大小转换为字节数。
func parseHumanSize(numStr, unit string) int64 {
	num, err := strconv.ParseFloat(numStr, 64)
	if err != nil {
		return -1
	}
	switch strings.ToUpper(unit) {
	case "B":
		return int64(num)
	case "KIB":
		return int64(num * 1024)
	case "MIB":
		return int64(num * 1024 * 1024)
	case "GIB":
		return int64(num * 1024 * 1024 * 1024)
	case "TIB":
		return int64(num * 1024 * 1024 * 1024 * 1024)
	default:
		return -1
	}
}

// resticDiffConfig 内部解析 restic 配置。
type resticDiffConfig struct {
	repositoryPassword string
}

func parseResticConfigForDiff(raw string) (resticDiffConfig, error) {
	if strings.TrimSpace(raw) == "" {
		return resticDiffConfig{}, nil
	}
	// 复用 executor 中的结构，但只取密码字段
	type cfg struct {
		RepositoryPassword string `json:"repository_password,omitempty"`
	}
	var c cfg
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return resticDiffConfig{}, err
	}
	return resticDiffConfig{repositoryPassword: c.RepositoryPassword}, nil
}

func buildDiffEnvPrefix(password string) string {
	if password == "" {
		return "RESTIC_PASSWORD=''"
	}
	return "RESTIC_PASSWORD=" + executor.ShellEscape(password)
}
