package anomaly

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"xirang/backend/internal/logger"
	"xirang/backend/internal/model"
	"xirang/backend/internal/sshutil"
	"xirang/backend/internal/task/executor"

	"gorm.io/gorm"
)

// RansomSuffixes 已知勒索软件常见后缀。匹配时大小写不敏感。
// 后续 PR 可通过系统设置扩展为动态配置后缀列表。
var RansomSuffixes = []string{
	".encrypted", ".locked", ".crypt", ".ransom", ".xxx", ".zzz",
	".enc", ".lock", ".crypto", ".wannacry", ".pay", ".decrypt",
}

// DiffRecord 一次快照差异的统计数据，用于历史记录写入和基线计算。
type DiffRecord struct {
	AddedCount       int
	RemovedCount     int
	ChangedCount     int
	TotalSizeBytes   int64
	RansomSuffixHits int
}

// Baseline 基线统计值（移动平均 μ + 标准差 σ）。
type Baseline struct {
	Mean   float64
	StdDev float64
	N      int // 样本数
}

// TotalChanges 返回总变更文件数（added + removed + changed）。
func (d DiffRecord) TotalChanges() int {
	return d.AddedCount + d.RemovedCount + d.ChangedCount
}

// CountRansomSuffixHits 统计 paths 中有多少文件路径以已知勒索后缀结尾。
// 匹配大小写不敏感；每个文件最多匹配一次。
func CountRansomSuffixHits(paths []string) int {
	count := 0
	for _, p := range paths {
		lower := strings.ToLower(p)
		for _, suffix := range RansomSuffixes {
			if strings.HasSuffix(lower, suffix) {
				count++
				break
			}
		}
	}
	return count
}

// CalculateBaseline 从最近的历史记录计算移动平均 μ 和标准差 σ。
// 若记录数不足 minSamples 条，返回 nil 表示基线不足。
func CalculateBaseline(records []DiffRecord, minSamples int) *Baseline {
	if len(records) < minSamples {
		return nil
	}
	total := 0.0
	for _, r := range records {
		total += float64(r.TotalChanges())
	}
	mean := total / float64(len(records))

	variance := 0.0
	for _, r := range records {
		diff := float64(r.TotalChanges()) - mean
		variance += diff * diff
	}
	variance /= float64(len(records))

	return &Baseline{
		Mean:   mean,
		StdDev: math.Sqrt(variance),
		N:      len(records),
	}
}

// IsAnomalous 判断当前变更量是否超出基线 k 倍标准差。
// baseline 为 nil 时返回 false（基线不足，不做判断）。
func IsAnomalous(current DiffRecord, baseline *Baseline, k float64) bool {
	if baseline == nil {
		return false
	}
	threshold := baseline.Mean + k*baseline.StdDev
	return float64(current.TotalChanges()) > threshold
}

// ---------------------------------------------------------------------------
// restic diff 输出解析（逻辑复制自 snapshot_diff_handler.go 的 parseDiffOutput）
// 放在 anomaly 包内以避免 handlers 包依赖。
// ---------------------------------------------------------------------------

type resticDiffChange struct {
	Path       string
	Type       string // "added", "removed", "changed"
	SizeBefore *int64
	SizeAfter  *int64
}

type resticDiffStats struct {
	Added   int
	Removed int
	Changed int
}

type resticDiffResult struct {
	Snap1   string
	Snap2   string
	Stats   resticDiffStats
	Changes []resticDiffChange
}

// parseResticDiff 解析 restic diff 文本输出（+ 新增 / - 删除 / M 修改）。
func parseResticDiff(output string, snap1, snap2 string) resticDiffResult {
	result := resticDiffResult{
		Snap1:   snap1,
		Snap2:   snap2,
		Changes: []resticDiffChange{},
	}

	lines := strings.Split(output, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		var change *resticDiffChange
		switch {
		case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "++"):
			change = parseResticDiffLine(line, "+", "added")
		case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "--"):
			change = parseResticDiffLine(line, "-", "removed")
		case strings.HasPrefix(line, "M"):
			change = parseResticDiffLine(line, "M", "changed")
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

// parseResticDiffLine 解析单行 restic diff 输出（可能包含大小信息）。
func parseResticDiffLine(line, prefix, changeType string) *resticDiffChange {
	rest := strings.TrimSpace(strings.TrimPrefix(line, prefix))
	if rest == "" {
		return nil
	}

	change := &resticDiffChange{Type: changeType}
	parts := strings.Fields(rest)
	if len(parts) == 0 {
		return nil
	}

	// 检查第一个 token 是否是数值（大小值）
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

func looksLikeSize(s string) bool {
	_, err := strconv.ParseFloat(s, 64)
	return err == nil
}

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

// ---------------------------------------------------------------------------
// restic 辅助函数
// ---------------------------------------------------------------------------

// resticSnapshot 解析 restic snapshots --json 输出中的单个快照。
type resticSnapshot struct {
	ID string `json:"id"`
}

var snapshotIDPat = regexp.MustCompile(`^[a-fA-F0-9]{4,64}$`)

// parseResticSnapshotsJSON 解析 restic snapshots --json 输出，返回快照 ID 列表。
func parseResticSnapshotsJSON(output string) ([]string, error) {
	var snapshots []resticSnapshot
	if err := json.Unmarshal([]byte(output), &snapshots); err != nil {
		return nil, fmt.Errorf("解析 restic snapshots JSON 失败: %w", err)
	}
	ids := make([]string, 0, len(snapshots))
	for _, s := range snapshots {
		if s.ID != "" {
			ids = append(ids, s.ID)
		}
	}
	return ids, nil
}

func logResticSnapshotsJSONParseFailure(taskID uint, parseErr error, output string) {
	logger.Module("anomaly").Debug().
		Uint("task_id", taskID).
		Str("stage", "snapshots_json_parse").
		Bool("output_present", strings.TrimSpace(output) != "").
		Err(parseErr).
		Msg("解析 restic snapshots JSON 失败")
}

// shellEscapeArg 将字符串包裹在单引号中，防止 shell 注入。
func shellEscapeArg(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// ---------------------------------------------------------------------------
// AnalyzeSnapshotDiff 主检测函数
// ---------------------------------------------------------------------------

// AnalyzeSnapshotDiff 在备份成功后异步运行，对比最近两次 restic 快照差异，
// 写入 diff history 记录，并在变更量异常或检测到已知勒索后缀时返回 Finding。
// 调用方（task runner）将 findings 传入 anomaly.AlertSink.Raise() 以持久化。
func AnalyzeSnapshotDiff(ctx context.Context, db *gorm.DB, task model.Task, taskRunID uint) ([]Finding, error) {
	// 非 restic 类型：跳过
	if task.ExecutorType != "restic" {
		return nil, nil
	}
	if strings.TrimSpace(task.RsyncTarget) == "" {
		return nil, fmt.Errorf("restic 仓库路径为空")
	}

	// 重新加载 task 附带 Node + SSHKey（调用方可能只传了部分字段）
	var fullTask model.Task
	if err := db.Preload("Node").Preload("Node.SSHKey").First(&fullTask, task.ID).Error; err != nil {
		return nil, fmt.Errorf("加载任务失败: %w", err)
	}
	if fullTask.Node.ID == 0 {
		return nil, fmt.Errorf("任务未关联节点")
	}

	access := executor.ResolveResticRepositoryAccessOrEmpty(fullTask.ExecutorConfig)
	envPrefix := executor.BuildResticEnvPrefix(access)
	repoArg := shellEscapeArg(fullTask.RsyncTarget)
	policyID := uint(0)
	if fullTask.PolicyID != nil {
		policyID = *fullTask.PolicyID
	}

	// SSH 连接（使用 ctx 已有的超时控制）
	client, err := executor.DialSSHForNodePurpose(ctx, fullTask.Node, sshutil.PurposeSnapshotDiff)
	if err != nil {
		return nil, fmt.Errorf("SSH 连接失败: %w", err)
	}
	defer client.Close() //nolint:errcheck

	// 获取最近 2 个快照 ID
	snapCmd := fmt.Sprintf("%s restic snapshots --json -r %s --latest 2 2>&1", envPrefix, repoArg)
	snapOutput, err := executor.RunSSHCommandOutput(ctx, client, snapCmd)
	if err != nil {
		return nil, fmt.Errorf("获取快照列表失败: %w", err)
	}

	snapIDs, err := parseResticSnapshotsJSON(snapOutput)
	if err != nil {
		// JSON 解析失败说明仓库可能为空或输出非预期 — 静默跳过
		logResticSnapshotsJSONParseFailure(task.ID, err, snapOutput)
		return nil, nil
	}
	if len(snapIDs) < 2 {
		// 首次备份，至少需要 2 个快照才能 diff
		return nil, nil
	}

	// 校验快照 ID 格式
	for _, id := range snapIDs {
		if !snapshotIDPat.MatchString(id) {
			return nil, fmt.Errorf("快照 ID 格式无效: %s", id)
		}
	}

	// 执行 restic diff（snapIDs[0]=较旧, snapIDs[1]=较新）
	snap1Arg := shellEscapeArg(snapIDs[0])
	snap2Arg := shellEscapeArg(snapIDs[1])
	diffCmd := fmt.Sprintf("%s restic diff %s %s -r %s 2>&1", envPrefix, snap1Arg, snap2Arg, repoArg)
	diffOutput, err := executor.RunSSHCommandOutput(ctx, client, diffCmd)
	if err != nil {
		return nil, fmt.Errorf("执行 restic diff 失败: %w", err)
	}

	result := parseResticDiff(diffOutput, snapIDs[0], snapIDs[1])

	// 收集所有变更路径用于勒索后缀匹配，同时计算总大小
	var changedPaths []string
	var totalSize int64
	for _, ch := range result.Changes {
		changedPaths = append(changedPaths, ch.Path)
		if ch.SizeAfter != nil {
			totalSize += *ch.SizeAfter
		}
	}

	ransomHits := CountRansomSuffixHits(changedPaths)

	diffRecord := DiffRecord{
		AddedCount:       result.Stats.Added,
		RemovedCount:     result.Stats.Removed,
		ChangedCount:     result.Stats.Changed,
		TotalSizeBytes:   totalSize,
		RansomSuffixHits: ransomHits,
	}

	// 写入历史记录（无论是否异常 — 作为基线种子）
	history := model.SnapshotDiffHistory{
		PolicyID:         policyID,
		TaskID:           fullTask.ID,
		TaskRunID:        taskRunID,
		AddedCount:       diffRecord.AddedCount,
		RemovedCount:     diffRecord.RemovedCount,
		ChangedCount:     diffRecord.ChangedCount,
		TotalSizeBytes:   diffRecord.TotalSizeBytes,
		RansomSuffixHits: diffRecord.RansomSuffixHits,
	}
	if err := db.Create(&history).Error; err != nil {
		return nil, fmt.Errorf("写入 diff history 失败: %w", err)
	}

	// 查询历史基线（最近 10 条，排除当前记录）
	var histories []model.SnapshotDiffHistory
	if err := db.Where("policy_id = ? AND id != ?", policyID, history.ID).
		Order("created_at desc").Limit(10).
		Find(&histories).Error; err != nil {
		return nil, fmt.Errorf("查询历史基线失败: %w", err)
	}

	// 构建时间升序的 DiffRecord 切片（基线计算需要按时间顺序）
	var records []DiffRecord
	for i := len(histories) - 1; i >= 0; i-- {
		h := histories[i]
		records = append(records, DiffRecord{
			AddedCount:       h.AddedCount,
			RemovedCount:     h.RemovedCount,
			ChangedCount:     h.ChangedCount,
			TotalSizeBytes:   h.TotalSizeBytes,
			RansomSuffixHits: h.RansomSuffixHits,
		})
	}

	baseline := CalculateBaseline(records, 3)

	var findings []Finding

	// 维度 1：变更数量异常（μ + k·σ 阈值）
	if IsAnomalous(diffRecord, baseline, 3.0) {
		var sigma float64
		var meanVal float64
		var n int
		if baseline != nil {
			sigma = baseline.StdDev
			meanVal = baseline.Mean
			n = baseline.N
		}
		s := sigma
		findings = append(findings, Finding{
			NodeID:        fullTask.NodeID,
			Detector:      "snapshot_diff",
			Metric:        "snapshot_churn",
			Severity:      "warning",
			ObservedValue: float64(diffRecord.TotalChanges()),
			BaselineValue: meanVal,
			Sigma:         &s,
			ErrorCode:     fmt.Sprintf("XR-SNAPSHOT-CHURN-%d", policyID),
			Message: fmt.Sprintf("策略 %d 的快照变更量异常（本次变更 %d 个文件，基线 μ=%.1f σ=%.1f，样本数=%d）",
				policyID, diffRecord.TotalChanges(), meanVal, sigma, n),
			Details: map[string]any{
				"added_count":        diffRecord.AddedCount,
				"removed_count":      diffRecord.RemovedCount,
				"changed_count":      diffRecord.ChangedCount,
				"total_changes":      diffRecord.TotalChanges(),
				"total_size_bytes":   diffRecord.TotalSizeBytes,
				"ransom_suffix_hits": diffRecord.RansomSuffixHits,
				"baseline_mean":      meanVal,
				"baseline_stddev":    sigma,
				"baseline_samples":   n,
				"snap1":              snapIDs[0],
				"snap2":              snapIDs[1],
			},
		})
	}

	// 维度 2：勒索后缀检测（任何匹配即告警，不依赖基线）
	if diffRecord.RansomSuffixHits > 0 {
		findings = append(findings, Finding{
			NodeID:        fullTask.NodeID,
			Detector:      "snapshot_diff",
			Metric:        "ransomware_pattern",
			Severity:      "critical",
			ObservedValue: float64(diffRecord.RansomSuffixHits),
			BaselineValue: 0,
			ErrorCode:     fmt.Sprintf("XR-SNAPSHOT-RANSOM-%d", policyID),
			Message: fmt.Sprintf("策略 %d 的快照中发现 %d 个匹配已知勒索后缀的文件",
				policyID, diffRecord.RansomSuffixHits),
			Details: map[string]any{
				"ransom_suffix_hits": diffRecord.RansomSuffixHits,
				"added_count":        diffRecord.AddedCount,
				"removed_count":      diffRecord.RemovedCount,
				"changed_count":      diffRecord.ChangedCount,
				"total_changes":      diffRecord.TotalChanges(),
				"snap1":              snapIDs[0],
				"snap2":              snapIDs[1],
			},
		})
	}

	return findings, nil
}
