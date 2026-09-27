package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"xirang/backend/internal/model"
	"xirang/backend/internal/rsyncconfinement"
	"xirang/backend/internal/sshutil"
	"xirang/backend/internal/util"
)

// RsyncBackupCapture owns the private source used by one legacy backup. Only
// PrepareRsyncBackupCapture can grant this source the trusted staging capability.
// Close must run after the executor has stopped, on every terminal path.
type RsyncBackupCapture struct {
	directory string
	source    string
	target    string
	binary    string
	manifest  string
}

func (c *RsyncBackupCapture) Manifest() string { return c.manifest }

func (c *RsyncBackupCapture) Close() error {
	if c.directory == "" {
		return nil
	}
	relaxRsyncRestoreStagingPermissions(c.directory)
	err := os.RemoveAll(c.directory)
	if err == nil {
		c.directory = ""
	}
	return err
}

// CapturedRsyncFactory preserves the executor injection boundary while requiring
// an explicit stable-source implementation. There is no live-source fallback
// for a successful capture.
type CapturedRsyncFactory interface {
	ResolveRsyncBackupCapture(*RsyncBackupCapture) Executor
}

func (f *factory) ResolveRsyncBackupCapture(c *RsyncBackupCapture) Executor { return c }

// PrepareRsyncBackupCapture records selection in the same rsync invocation that
// creates a fresh private tree. Operation records are not completion evidence:
// only exit zero plus an exact tree check and hashes may produce a manifest.
func PrepareRsyncBackupCapture(ctx context.Context, task model.Task) (_ *RsyncBackupCapture, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(task.RsyncSource) == "" {
		return nil, fmt.Errorf("rsync capture source is empty")
	}
	if !filepath.IsAbs(task.RsyncTarget) || util.IsRemotePathSpec(task.RsyncTarget) {
		return nil, fmt.Errorf("rsync capture target is not a Core-local absolute path")
	}
	layout, root, err := inferRsyncCaptureLayout(ctx, task, task.RsyncSource, RsyncCaptureSourceRole)
	if err != nil {
		return nil, err
	}
	rules, err := parseRsyncExcludeRules(task.Policy)
	if err != nil {
		return nil, err
	}
	policy, err := rsyncconfinement.LoadPolicyFromEnv()
	if err != nil {
		return nil, err
	}
	operand, localSource, transport, runtimePaths, cleanup, err := prepareRsyncCaptureSourceForPurpose(ctx, task, task.RsyncSource, policy, RsyncCaptureSourceRole, sshutil.PurposeTaskBackup)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	directory, err := os.MkdirTemp("", "xirang-rsync-backup-capture-*")
	if err != nil {
		return nil, fmt.Errorf("create rsync capture evidence tree: %w", err)
	}
	capture := &RsyncBackupCapture{directory: directory, target: task.RsyncTarget, binary: rsyncCommandBinary(task)}
	defer func() {
		if err != nil {
			if cleanupErr := capture.Close(); cleanupErr != nil {
				err = errors.Join(err, newRsyncCaptureFailure("rsync capture cleanup failed", cleanupErr))
			}
		}
	}()
	// Archive mode can replace the receiving root's permissions. Keep it below
	// a separate 0700 owner directory that rsync never receives as an operand.
	tree := filepath.Join(directory, "tree")
	if err := os.Mkdir(tree, 0700); err != nil {
		return nil, err
	}
	args := appendRsyncExcludeArgs([]string{"-a", "--8-bit-output", "--out-format=%i %n"}, rules)
	args = append(args, transport...)
	if bw := resolveBwLimit(task); bw > 0 {
		args = append(args, "--bwlimit", fmt.Sprintf("%dk", bw*1000/8))
	}
	args = append(args, "--", operand, tree+string(filepath.Separator))
	stdout := &rsyncCaptureOutputBuffer{limit: maxRsyncCaptureManifestLen}
	stderr := &rsyncCaptureOutputBuffer{limit: maxRsyncCaptureCommandBytes}
	if err := runRsyncCaptureCommand(ctx, task, args, localSource, tree, false, false, runtimePaths, stdout, stderr); err != nil {
		return nil, newRsyncCaptureFailure("rsync capture evidence copy failed", err, stderr, stdout)
	}
	listed, err := parseRsyncCaptureEntries(stdout.buf.String())
	if err != nil {
		return nil, err
	}
	base := filepath.Base(filepath.Clean(task.RsyncSource))
	capture.source = tree + string(filepath.Separator)
	if layout != model.TaskRunCaptureLayoutDirectoryContents {
		capture.source = filepath.Join(tree, base)
	}
	entries := make([]model.RsyncCaptureManifestEntry, 0, len(listed)+1)
	hasRoot := false
	for _, selected := range listed {
		if layout == model.TaskRunCaptureLayoutSingleFile && selected.path != base {
			return nil, fmt.Errorf("rsync capture single-file selection name does not match source")
		}
		path, pathErr := normalizeRsyncCapturePath(selected.path, selected.kind, layout, base)
		if pathErr != nil {
			return nil, pathErr
		}
		entries = append(entries, model.RsyncCaptureManifestEntry{Path: path, Kind: selected.kind})
		if path == "" {
			hasRoot = true
		}
	}
	// A directory's own attributes may already match the empty receiving root.
	// Add only the known root, never infer missing child records by walking it.
	if !hasRoot && layout != model.TaskRunCaptureLayoutSingleFile {
		entries = append(entries, model.RsyncCaptureManifestEntry{Path: "", Kind: "directory"})
	}
	// rsync treats one empty source directory and an absent unslashed target as
	// a rename. Other directory-root transfers retain the source basename.
	if layout == model.TaskRunCaptureLayoutDirectoryRoot && len(entries) == 1 &&
		!strings.HasSuffix(task.RsyncTarget, string(filepath.Separator)) {
		if _, statErr := os.Lstat(task.RsyncTarget); os.IsNotExist(statErr) {
			root = ""
		}
	}
	if err := validateRsyncCaptureEntries(entries, layout, root, false); err != nil {
		return nil, err
	}
	if err := populateBackupCaptureTree(ctx, tree, task.RsyncSource, layout, root, entries); err != nil {
		return nil, err
	}
	manifest := model.RsyncCaptureManifest{Version: 1, Layout: layout, Root: root, Entries: entries}
	// Directory-root and single-file transfers must not leave siblings outside
	// the logical root, including anything not reported by the sender.
	if layout != model.TaskRunCaptureLayoutDirectoryContents {
		children, readErr := os.ReadDir(tree)
		if readErr != nil {
			return nil, readErr
		}
		if len(children) != 1 || children[0].Name() != base {
			return nil, fmt.Errorf("rsync capture contains entries outside its logical root")
		}
	}
	if err := validateRsyncCaptureEntries(entries, layout, root, true); err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	capture.manifest, err = model.EncodeRsyncCaptureManifest(manifest)
	if err != nil {
		return nil, err
	}
	if len(capture.manifest) > maxRsyncCaptureManifestLen {
		return nil, fmt.Errorf("rsync capture evidence exceeds size limit")
	}
	return capture, nil
}

// Check both directions before reading any payload. WalkDir does not follow
// symlinks, so a reported descendant of a symlink can never become evidence.
// Root additionally confines all subsequent reads to the owned tree.
func populateBackupCaptureTree(ctx context.Context, tree, source, layout, root string, entries []model.RsyncCaptureManifestEntry) error {
	expected := make(map[string]int, len(entries))
	for i := range entries {
		path := rsyncCaptureStagedPath(tree, source, layout, root, entries[i].Path)
		relative, err := filepath.Rel(tree, path)
		if err != nil {
			return err
		}
		expected[relative] = i
	}
	seen := make(map[string]bool, len(entries))
	if err := filepath.WalkDir(tree, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		relative, err := filepath.Rel(tree, path)
		if err != nil {
			return err
		}
		if relative == "." && layout != model.TaskRunCaptureLayoutDirectoryContents {
			return nil // The receiving container is not part of the source.
		}
		i, ok := expected[relative]
		if !ok {
			return fmt.Errorf("rsync captured tree contains an unreported entry: %q", relative)
		}
		kind := "file"
		if entry.IsDir() {
			kind = "directory"
		} else if entry.Type()&os.ModeSymlink != 0 {
			kind = "symlink"
		} else if !entry.Type().IsRegular() {
			return fmt.Errorf("rsync captured tree contains an unsupported entry: %q", relative)
		}
		if entries[i].Kind != kind {
			return fmt.Errorf("rsync captured entry type differs from selection: %q", relative)
		}
		seen[relative] = true
		return nil
	}); err != nil {
		return err
	}
	for path := range expected {
		if !seen[path] {
			return fmt.Errorf("rsync captured selection is absent from tree: %q", path)
		}
	}
	bounded, err := os.OpenRoot(tree)
	if err != nil {
		return err
	}
	defer func() { _ = bounded.Close() }()
	for path, i := range expected {
		switch entries[i].Kind {
		case "file":
			file, err := bounded.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
			if err != nil {
				return newRsyncCaptureFailure("rsync capture source hashing failed", err)
			}
			hasher := sha256.New()
			size, readErr := io.Copy(hasher, &rsyncCaptureContextReader{ctx: ctx, reader: file})
			closeErr := file.Close()
			if err := errors.Join(readErr, closeErr); err != nil {
				return newRsyncCaptureFailure("rsync capture source hashing failed", err)
			}
			entries[i].Size, entries[i].SHA256 = size, hex.EncodeToString(hasher.Sum(nil))
		case "symlink":
			target, err := bounded.Readlink(path)
			if err != nil {
				return newRsyncCaptureFailure("rsync capture symlink evidence failed", err)
			}
			entries[i].LinkTarget = target
		}
	}
	return nil
}

func (c *RsyncBackupCapture) Run(ctx context.Context, task model.Task, logf LogFunc, progressf ProgressFunc) (int, error) {
	if c == nil || c.directory == "" || c.manifest == "" || task.RsyncTarget != c.target {
		return -1, markNoProcessStart(fmt.Errorf("rsync backup capture is unavailable or target changed"))
	}
	if err := ctx.Err(); err != nil {
		return -1, markNoProcessStart(err)
	}
	if err := EnsureLocalTargetReady(task.RsyncTarget); err != nil {
		return -1, markNoProcessStart(err)
	}
	args := []string{"-av", "--checksum", "--info=progress2"}
	if bw := resolveBwLimit(task); bw > 0 {
		args = append(args, "--bwlimit", fmt.Sprintf("%dk", bw*1000/8))
	}
	args = append(args, "--", c.source, task.RsyncTarget)
	if logf != nil {
		logf("info", "从本次 Rsync 捕获副本执行备份传输")
	}
	return (&RsyncExecutor{binary: c.binary}).runRsyncCommandWithOptions(ctx, args, rsyncCommandOptions{
		localSource: c.source, localTarget: task.RsyncTarget, trustedLocalSource: true,
	}, logf, progressf)
}
