package verifier

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"xirang/backend/internal/model"
	"xirang/backend/internal/task/executor"
)

func verifyRsyncBackupSelection(ctx context.Context, task model.Task, logf func(level, msg string)) Result {
	differences, err := executor.RsyncSelectionDifferences(ctx, task, false)
	if err != nil {
		message := sanitizeVerifierRuntimeEvidence(fmt.Sprintf("备份校验失败: %v", err))
		if logf != nil {
			logf("warn", message)
		}
		return Result{Status: "warning", Message: message}
	}
	if differences > 0 {
		message := fmt.Sprintf("备份校验发现 %d 个文件或目录差异", differences)
		if logf != nil {
			logf("warn", message)
		}
		return Result{Status: "warning", Message: message}
	}
	return Result{Status: "passed", Message: "备份校验通过：Rsync 选择集合与内容一致"}
}

func verifyRsyncRestoreManifest(ctx context.Context, task model.Task, logf func(level, msg string)) Result {
	warn := func(message string) Result {
		message = sanitizeVerifierRuntimeEvidence(message)
		if logf != nil {
			logf("warn", message)
		}
		return Result{Status: "warning", Message: message}
	}
	if strings.TrimSpace(task.RsyncCaptureManifest) == "" {
		return warn("恢复校验失败：缺少捕获证据，历史 Rsync 布局无法证明")
	}
	expected, err := model.DecodeRsyncCaptureManifest(task.RsyncCaptureManifest)
	if err != nil {
		return warn("恢复校验失败：捕获证据无效")
	}

	// First prove that the Core source still contains the exact bytes captured
	// before the backup write. A changed or corrupted Core copy is not silently
	// accepted as the source of restore truth.
	source := strings.TrimSpace(task.RsyncSource)
	if source == "" || !filepath.IsAbs(source) || strings.ContainsRune(source, '\x00') {
		return warn("恢复校验失败：Core 备份源不可用")
	}
	info, err := os.Lstat(source)
	if err != nil {
		return warn("恢复校验失败：Core 备份源不存在或不可读")
	}
	resolvedSource, err := executor.ResolveRsyncRestoreSource(source, info, task.RsyncCaptureLayout, task.RsyncCaptureRoot)
	if err != nil {
		return warn("恢复校验失败：捕获的逻辑根不可用")
	}
	coreTask := task
	coreTask.RsyncSource = resolvedSource
	coreTask.RsyncTarget = ""
	if expected.Layout == model.TaskRunCaptureLayoutSingleFile {
		coreTask.RsyncCaptureLayout = model.TaskRunCaptureLayoutSingleFile
	} else {
		coreTask.RsyncCaptureLayout = model.TaskRunCaptureLayoutDirectoryContents
	}
	coreTask.RsyncCaptureRoot = ""
	coreTask.Node = model.Node{}
	coreRaw, err := executor.CaptureRsyncManifest(ctx, coreTask)
	if err != nil {
		return warn("恢复校验失败：Core 源枚举或哈希读取失败")
	}
	coreManifest, err := model.DecodeRsyncCaptureManifest(coreRaw)
	if err != nil || !rsyncManifestEntriesEqual(expected, coreManifest) {
		return warn("恢复校验失败：Core 捕获字节已变化")
	}

	// Enumerate and hash the node target with Rsync's own selection machinery.
	// This is Core -> node in the real topology; the old remote-to-remote path
	// could accidentally compare an unrelated same-named node directory.
	remoteSource := strings.TrimSpace(task.RsyncTarget)
	if remoteSource == "" {
		return warn("恢复校验失败：节点恢复目标不可用")
	}
	remoteBase := remoteSource
	remoteTask := task
	if expected.Layout == model.TaskRunCaptureLayoutSingleFile {
		// A single-file restore to an existing directory receives the
		// resolved source basename. Root is Core-layout provenance only and
		// may be empty when the Core target was an absent plain file path.
		remoteSource = filepath.Join(remoteSource, filepath.Base(resolvedSource))
		remoteTask.RsyncCaptureLayout = model.TaskRunCaptureLayoutSingleFile
		remoteTask.RsyncCaptureRoot = ""
	} else {
		if !strings.HasSuffix(remoteSource, string(filepath.Separator)) {
			remoteSource += string(filepath.Separator)
		}
		remoteTask.RsyncCaptureLayout = model.TaskRunCaptureLayoutDirectoryContents
		remoteTask.RsyncCaptureRoot = ""
	}
	remoteTask.RsyncSource = remoteSource
	remoteTask.RsyncTarget = ""
	remoteRaw, err := executor.CaptureRsyncManifest(ctx, remoteTask)
	if err != nil && expected.Layout == model.TaskRunCaptureLayoutSingleFile {
		// A direct-file restore target is not a directory containing the
		// basename. Retry the exact target path.
		remoteTask.RsyncSource = remoteBase
		remoteRaw, err = executor.CaptureRsyncManifest(ctx, remoteTask)
	}
	if err != nil {
		return warn("恢复校验失败：节点目标枚举或哈希读取失败")
	}
	remoteManifest, err := model.DecodeRsyncCaptureManifest(remoteRaw)
	if err != nil || !rsyncManifestEntriesEqual(expected, remoteManifest) {
		return warn("恢复校验失败：恢复目标文件集合或内容不一致")
	}

	files := 0
	var bytes int64
	for _, entry := range expected.Entries {
		if entry.Kind == "file" {
			files++
			if entry.Size > 0 {
				bytes += entry.Size
			}
		}
	}
	return Result{
		Status:       "passed",
		Message:      fmt.Sprintf("恢复校验通过：Core 捕获证据与节点目标均为 %d 个文件", files),
		FileCountSrc: files,
		FileCountDst: files,
		TotalSizeSrc: bytes,
	}
}

func rsyncManifestEntriesEqual(left, right model.RsyncCaptureManifest) bool {
	if len(left.Entries) != len(right.Entries) {
		return false
	}
	type key struct {
		path string
		kind string
	}
	entries := make(map[key]model.RsyncCaptureManifestEntry, len(right.Entries))
	for _, entry := range right.Entries {
		entries[key{path: entry.Path, kind: entry.Kind}] = entry
	}
	for _, entry := range left.Entries {
		other, ok := entries[key{path: entry.Path, kind: entry.Kind}]
		if !ok || entry.Kind != other.Kind {
			return false
		}
		switch entry.Kind {
		case "file":
			if entry.SHA256 == "" || entry.SHA256 != other.SHA256 {
				return false
			}
			if entry.Size >= 0 && other.Size >= 0 && entry.Size != other.Size {
				return false
			}
		case "symlink":
			if entry.LinkTarget != other.LinkTarget {
				return false
			}
		}
	}
	return true
}
