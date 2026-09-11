package executor

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"xirang/backend/internal/model"

	"golang.org/x/sys/unix"
)

type stagedRsyncMetadata struct {
	path    string
	mode    os.FileMode
	modTime time.Time
	uid     int
	gid     int
	symlink bool
}

// stageRsyncRestoreSource copies the captured selection into a private tree and
// verifies that tree before the caller performs any remote operation. The
// source tree is intentionally not reused after this function returns: the
// transfer receives only the isolated staging path.
func stageRsyncRestoreSource(ctx context.Context, source string, manifest model.RsyncCaptureManifest) (string, func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateRsyncCaptureEntries(manifest.Entries, manifest.Layout, manifest.Root, true); err != nil {
		return "", func() {}, err
	}
	if manifest.Layout == model.TaskRunCaptureLayoutSingleFile &&
		(len(manifest.Entries) != 1 || manifest.Entries[0].Path != "" ||
			(manifest.Entries[0].Kind != "file" && manifest.Entries[0].Kind != "symlink")) {
		return "", func() {}, fmt.Errorf("rsync capture single-file evidence is invalid")
	}
	if err := ctx.Err(); err != nil {
		return "", func() {}, err
	}

	source = filepath.Clean(source)
	sourceInfo, err := os.Lstat(source)
	if err != nil {
		return "", func() {}, fmt.Errorf("core 备份源在隔离前不可用")
	}
	if manifest.Layout == model.TaskRunCaptureLayoutSingleFile {
		if sourceInfo.Mode().IsRegular() && manifest.Entries[0].Kind == "file" {
			// Expected type is checked again by copyRsyncRestoreEntry.
		} else if sourceInfo.Mode()&os.ModeSymlink != 0 && manifest.Entries[0].Kind == "symlink" {
			// Expected type is checked again by copyRsyncRestoreEntry.
		} else {
			return "", func() {}, fmt.Errorf("core 备份源类型与捕获证据不一致")
		}
	} else if !sourceInfo.IsDir() || sourceInfo.Mode()&os.ModeSymlink != 0 {
		return "", func() {}, fmt.Errorf("core 备份源目录不可用")
	}

	stageDir, err := os.MkdirTemp("", "xirang-rsync-restore-")
	if err != nil {
		return "", func() {}, fmt.Errorf("create rsync restore staging directory: %w", err)
	}
	if err := os.Chmod(stageDir, 0o700); err != nil {
		_ = os.RemoveAll(stageDir)
		return "", func() {}, fmt.Errorf("protect rsync restore staging directory: %w", err)
	}
	cleanup := func() {
		relaxRsyncRestoreStagingPermissions(stageDir)
		_ = os.RemoveAll(stageDir)
	}
	keepStage := false
	defer func() {
		if !keepStage {
			cleanup()
		}
	}()

	if manifest.Layout == model.TaskRunCaptureLayoutSingleFile {
		stageName := filepath.Base(source)
		if manifest.Root != "" {
			stageName = manifest.Root
		}
		if err := validateRsyncCaptureRoot(stageName); err != nil {
			return "", func() {}, fmt.Errorf("rsync restore staging basename is invalid")
		}
		stagedPath := filepath.Join(stageDir, stageName)
		metadata := make([]stagedRsyncMetadata, 0, 1)
		if err := copyRsyncRestoreEntry(ctx, source, stagedPath, manifest.Entries[0], &metadata); err != nil {
			return "", func() {}, err
		}
		if err := applyStagedRsyncMetadata(metadata); err != nil {
			return "", func() {}, err
		}
		verifyBase := stagedPath
		if manifest.Root != "" {
			verifyBase = stageDir
		}
		if err := verifyStagedRsyncManifest(ctx, verifyBase, stagedPath, manifest); err != nil {
			return "", func() {}, err
		}
		keepStage = true
		return stagedPath, cleanup, nil
	}

	stageName := filepath.Base(source)
	if manifest.Layout == model.TaskRunCaptureLayoutDirectoryRoot && manifest.Root != "" {
		stageName = manifest.Root
	}
	if err := validateRsyncCaptureRoot(stageName); err != nil {
		return "", func() {}, fmt.Errorf("rsync restore staging basename is invalid")
	}
	stagedRoot := filepath.Join(stageDir, stageName)
	if err := os.Mkdir(stagedRoot, 0o700); err != nil {
		return "", func() {}, fmt.Errorf("create rsync restore staging root: %w", err)
	}
	rootMetadata, err := newStagedRsyncMetadata(stagedRoot, sourceInfo)
	if err != nil {
		return "", func() {}, err
	}
	metadata := []stagedRsyncMetadata{rootMetadata}
	for _, entry := range manifest.Entries {
		if err := ctx.Err(); err != nil {
			return "", func() {}, err
		}
		sourcePath, err := safeRsyncRestoreSourcePath(source, entry.Path)
		if err != nil {
			return "", func() {}, err
		}
		destinationPath, err := safeRsyncRestoreStagePath(stagedRoot, entry.Path)
		if err != nil {
			return "", func() {}, err
		}
		if err := copyRsyncRestoreEntry(ctx, sourcePath, destinationPath, entry, &metadata); err != nil {
			return "", func() {}, err
		}
	}
	if err := applyStagedRsyncMetadata(metadata); err != nil {
		return "", func() {}, err
	}
	verifyBase := stagedRoot
	if manifest.Layout == model.TaskRunCaptureLayoutDirectoryRoot && manifest.Root != "" {
		verifyBase = stageDir
	}
	if err := verifyStagedRsyncManifest(ctx, verifyBase, stagedRoot, manifest); err != nil {
		return "", func() {}, err
	}

	keepStage = true
	return stagedRoot + string(filepath.Separator), cleanup, nil
}

const rsyncRestoreMetadataMode = os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky

func newStagedRsyncMetadata(path string, info os.FileInfo) (stagedRsyncMetadata, error) {

	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return stagedRsyncMetadata{}, fmt.Errorf("inspect rsync restore source metadata: unsupported stat")
	}
	return stagedRsyncMetadata{
		path:    path,
		mode:    info.Mode() & rsyncRestoreMetadataMode,
		modTime: info.ModTime(),
		uid:     int(stat.Uid),
		gid:     int(stat.Gid),
		symlink: info.Mode()&os.ModeSymlink != 0,
	}, nil
}
func relaxRsyncRestoreStagingPermissions(root string) {
	_ = os.Chmod(root, 0o700)
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry == nil || path == root || entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if entry.IsDir() {
			_ = os.Chmod(path, 0o700)
		} else {
			_ = os.Chmod(path, 0o600)
		}
		return nil
	})
}

func copyRsyncRestoreEntry(ctx context.Context, sourcePath, destinationPath string, expected model.RsyncCaptureManifestEntry, metadata *[]stagedRsyncMetadata) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := os.Lstat(sourcePath)
	if err != nil {
		return fmt.Errorf("core 备份源在隔离期间发生变化")
	}
	switch expected.Kind {
	case "directory":
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("core 备份源类型在隔离期间发生变化")
		}
		if err := ensureRsyncRestoreStageDirectory(destinationPath); err != nil {
			return err
		}
		entryMetadata, err := newStagedRsyncMetadata(destinationPath, info)
		if err != nil {
			return err
		}
		*metadata = append(*metadata, entryMetadata)
	case "file":
		if !info.Mode().IsRegular() {
			return fmt.Errorf("core 备份源类型在隔离期间发生变化")
		}
		if err := os.MkdirAll(filepath.Dir(destinationPath), 0o700); err != nil {
			return fmt.Errorf("create rsync restore staging parent: %w", err)
		}
		input, err := os.OpenFile(sourcePath, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
		if err != nil {
			return fmt.Errorf("open core backup source for staging: %w", err)
		}
		output, err := os.OpenFile(destinationPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			_ = input.Close()
			return fmt.Errorf("create rsync restore staged file: %w", err)
		}
		_, copyErr := io.Copy(output, &rsyncCaptureContextReader{ctx: ctx, reader: input})
		closeOutputErr := output.Close()
		closeInputErr := input.Close()
		if copyErr != nil {
			return fmt.Errorf("copy core backup source for staging: %w", copyErr)
		}
		if closeOutputErr != nil {
			return fmt.Errorf("close rsync restore staged file: %w", closeOutputErr)
		}
		if closeInputErr != nil {
			return fmt.Errorf("close core backup source after staging: %w", closeInputErr)
		}
		entryMetadata, err := newStagedRsyncMetadata(destinationPath, info)
		if err != nil {
			return err
		}
		*metadata = append(*metadata, entryMetadata)
	case "symlink":
		if info.Mode()&os.ModeSymlink == 0 {
			return fmt.Errorf("core 备份源类型在隔离期间发生变化")
		}
		target, err := os.Readlink(sourcePath)
		if err != nil {
			return fmt.Errorf("read core backup symlink for staging: %w", err)
		}
		if err := os.MkdirAll(filepath.Dir(destinationPath), 0o700); err != nil {
			return fmt.Errorf("create rsync restore staging parent: %w", err)
		}
		if err := os.Symlink(target, destinationPath); err != nil {
			return fmt.Errorf("create rsync restore staged symlink: %w", err)
		}
		entryMetadata, err := newStagedRsyncMetadata(destinationPath, info)
		if err != nil {
			return err
		}
		*metadata = append(*metadata, entryMetadata)
	default:
		return fmt.Errorf("rsync capture evidence has unsupported entry type")
	}
	return nil
}

func ensureRsyncRestoreStageDirectory(path string) error {
	if info, err := os.Lstat(path); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("rsync restore staging path type mismatch")
		}
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect rsync restore staging path: %w", err)
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("create rsync restore staged directory: %w", err)
	}
	return nil
}

func applyStagedRsyncMetadata(metadata []stagedRsyncMetadata) error {
	sort.SliceStable(metadata, func(left, right int) bool {
		return pathDepth(metadata[left].path) > pathDepth(metadata[right].path)
	})
	for _, item := range metadata {
		if err := applyStagedRsyncOwnership(item); err != nil {
			return err
		}
		if !item.symlink {
			if err := os.Chmod(item.path, item.mode); err != nil {
				return fmt.Errorf("preserve rsync restore staged mode: %w", err)
			}
		}
		if err := applyStagedRsyncTimestamp(item); err != nil {
			return err
		}
	}
	return nil
}

func applyStagedRsyncOwnership(item stagedRsyncMetadata) error {
	info, err := os.Lstat(item.path)
	if err != nil {
		return fmt.Errorf("inspect rsync restore staged ownership: %w", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("inspect rsync restore staged ownership: unsupported stat")
	}
	if uint32(item.uid) == stat.Uid && uint32(item.gid) == stat.Gid {
		return nil
	}
	if item.symlink {
		err = os.Lchown(item.path, item.uid, item.gid)
	} else {
		err = os.Chown(item.path, item.uid, item.gid)
	}
	if err != nil {
		return fmt.Errorf("preserve rsync restore staged ownership: %w", err)
	}
	return nil
}

func applyStagedRsyncTimestamp(item stagedRsyncMetadata) error {
	if item.symlink {
		timestamp := unix.NsecToTimespec(item.modTime.UnixNano())
		if err := unix.UtimesNanoAt(unix.AT_FDCWD, item.path, []unix.Timespec{timestamp, timestamp}, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return fmt.Errorf("preserve rsync restore staged symlink time: %w", err)
		}
		return nil
	}
	if err := os.Chtimes(item.path, item.modTime, item.modTime); err != nil {
		return fmt.Errorf("preserve rsync restore staged time: %w", err)
	}
	return nil
}

func pathDepth(path string) int {
	return strings.Count(filepath.Clean(path), string(filepath.Separator))
}

func safeRsyncRestoreSourcePath(root, relative string) (string, error) {
	if relative == "" {
		return filepath.Clean(root), nil
	}
	if _, err := normalizeRsyncCapturePath(relative, "file", model.TaskRunCaptureLayoutDirectoryContents, ""); err != nil {
		return "", err
	}
	root = filepath.Clean(root)
	candidate := filepath.Join(root, filepath.FromSlash(relative))
	rel, err := filepath.Rel(root, candidate)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("rsync capture selected path escapes source root")
	}
	components := strings.Split(filepath.ToSlash(relative), "/")
	current := root
	for _, component := range components[:len(components)-1] {
		if component == "" || component == "." {
			return "", fmt.Errorf("rsync capture selected path is invalid")
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("rsync capture selected path has unsafe parent")
		}
	}
	return candidate, nil
}

func safeRsyncRestoreStagePath(root, relative string) (string, error) {
	if relative == "" {
		return filepath.Clean(root), nil
	}
	if _, err := normalizeRsyncCapturePath(relative, "file", model.TaskRunCaptureLayoutDirectoryContents, ""); err != nil {
		return "", err
	}
	candidate := filepath.Join(filepath.Clean(root), filepath.FromSlash(relative))
	rel, err := filepath.Rel(filepath.Clean(root), candidate)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("rsync capture selected path escapes staging root")
	}
	return candidate, nil
}

func verifyStagedRsyncManifest(ctx context.Context, verifyBase, stagedRoot string, manifest model.RsyncCaptureManifest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, entry := range manifest.Entries {
		path := manifestTargetPath(verifyBase, manifest, entry.Path)
		if err := verifyLocalRsyncEntry(ctx, path, entry); err != nil {
			return fmt.Errorf("rsync restore staged evidence mismatch: %w", err)
		}
	}
	if manifest.Layout != model.TaskRunCaptureLayoutSingleFile {
		if err := verifyStagedRsyncTree(stagedRoot, manifest); err != nil {
			return err
		}
	}
	return nil
}

func verifyStagedRsyncTree(root string, manifest model.RsyncCaptureManifest) error {
	selected := make(map[string]model.RsyncCaptureManifestEntry, len(manifest.Entries))
	for _, entry := range manifest.Entries {
		selected[entry.Path] = entry
	}
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry == nil {
			return fmt.Errorf("rsync restore staging tree entry is missing")
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if relative == "." {
			relative = ""
		}
		if expected, ok := selected[relative]; ok {
			wantDir := expected.Kind == "directory"
			if entry.IsDir() != wantDir {
				return fmt.Errorf("rsync restore staging tree type mismatch")
			}
			return nil
		}
		if relative == "" {
			return nil
		}
		prefix := relative + "/"
		for selectedPath := range selected {
			if strings.HasPrefix(selectedPath, prefix) {
				return nil
			}
		}
		return fmt.Errorf("rsync restore staging tree contains an unselected entry")
	})
}
