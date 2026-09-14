//go:build linux

package rsyncconfinement

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

func newConfinedCommand(ctx context.Context, helper, binary string, request CommandRequest) (*exec.Cmd, func(), error) {
	policy, err := LoadPolicyFromEnv()
	if err != nil {
		return nil, func() {}, err
	}
	sourceRoots := policy.SourceRoots
	sourceLabel := "rsync_source"
	if request.LocalSourceTargetRole {
		sourceRoots = policy.TargetRoots
		sourceLabel = "rsync_target"
	}
	if request.LocalSource != "" && !request.TrustedLocalSource {
		var sourceErr error
		if request.LocalSourceTargetRole {
			sourceErr = policy.ValidateTarget(request.LocalSource, "rsync_target")
		} else {
			sourceErr = policy.ValidateSource(request.LocalSource, "rsync_source")
		}
		if sourceErr != nil {
			return nil, func() {}, sourceErr
		}
	}
	if request.LocalTarget != "" && !request.TrustedLocalTarget {
		if err := policy.ValidateTarget(request.LocalTarget, "rsync_target"); err != nil {
			return nil, func() {}, err
		}
	}

	if request.LocalSource == "" && request.LocalTarget == "" {
		return nil, func() {}, fmt.Errorf("%w: configured Rsync policy has no local operand", ErrCapabilityUnavailable)
	}

	fds := make([]*os.File, 0, len(request.RuntimeReadPaths)+3)
	closeFiles := func() {
		for _, file := range fds {
			_ = file.Close()
		}
	}
	openPinned := func(path string, allowMissing bool) (string, *os.File, int, error) {
		clean := filepath.Clean(strings.TrimSpace(path))
		if clean == "." || !filepath.IsAbs(clean) {
			return "", nil, -1, fmt.Errorf("%w: local operand must be absolute", ErrCapabilityUnavailable)
		}
		file, err := openPinnedPath(clean)
		if err == nil {
			index := len(fds)
			fds = append(fds, file)
			return clean, file, 3 + index, nil
		}
		if !allowMissing || !errorsIsNotExist(err) {
			return "", nil, -1, fmt.Errorf("%w: pin %s: %v", ErrCapabilityUnavailable, clean, err)
		}
		if len(policy.TargetRoots) > 0 {
			if prepErr := ensurePinnedTargetParent(clean, policy.TargetRoots); prepErr != nil {
				return "", nil, -1, prepErr
			}
			file, retryErr := openPinnedPath(clean)
			if retryErr == nil {
				index := len(fds)
				fds = append(fds, file)
				return clean, file, 3 + index, nil
			}
			if !errorsIsNotExist(retryErr) {
				return "", nil, -1, fmt.Errorf("%w: pin %s after parent creation: %v", ErrCapabilityUnavailable, clean, retryErr)
			}
		}
		parent := filepath.Dir(clean)
		for parent != filepath.Dir(parent) {
			file, parentErr := openPinnedPath(parent)
			if parentErr == nil {
				index := len(fds)
				fds = append(fds, file)
				return parent, file, 3 + index, nil
			}
			if !errorsIsNotExist(parentErr) {
				return "", nil, -1, fmt.Errorf("%w: pin parent %s: %v", ErrCapabilityUnavailable, parent, parentErr)
			}
			parent = filepath.Dir(parent)
		}
		return "", nil, -1, fmt.Errorf("%w: no existing parent for %s", ErrCapabilityUnavailable, clean)
	}

	args := []string{"--protocol=" + fmt.Sprint(HelperProtocolVersion), "--binary=" + binary}
	if request.LocalSource != "" {
		mountSourcePath, sourceFile, sourceFD, sourceErr := openPinned(request.LocalSource, false)
		if sourceErr != nil {
			closeFiles()
			return nil, func() {}, sourceErr
		}
		if !request.TrustedLocalSource && len(sourceRoots) > 0 {
			if pinErr := validatePinnedPathFD(int(sourceFile.Fd()), sourceRoots, sourceLabel); pinErr != nil {
				closeFiles()
				return nil, func() {}, pinErr
			}
		}
		args = append(args, "--mount-read="+mountSourcePath, "--mount-read-fd="+helperFD(sourceFD))
		args = append(args, "--read-fd="+helperFD(sourceFD))
		if shouldPreserveLocalSourceOperand(request.Args, request.LocalSource, sourceFile) {
			args = append(args, "--preserve-directory-root=1")
		}
	}
	mountTargetPath := ""
	if request.LocalTarget != "" {
		var targetFD *os.File
		var targetFDIndex int
		var targetErr error
		mountTargetPath, targetFD, targetFDIndex, targetErr = openPinned(request.LocalTarget, true)
		if targetErr != nil {
			closeFiles()
			return nil, func() {}, targetErr
		}
		if !request.TrustedLocalTarget && len(policy.TargetRoots) > 0 {
			if pinErr := validatePinnedPathFD(int(targetFD.Fd()), policy.TargetRoots, "rsync_target"); pinErr != nil {
				closeFiles()
				return nil, func() {}, pinErr
			}
		}
		args = append(args, "--mount-write="+mountTargetPath, "--mount-write-fd="+helperFD(targetFDIndex))
		args = append(args, "--write-fd="+helperFD(targetFDIndex))
	}
	for _, runtimePath := range request.RuntimeReadPaths {
		clean := filepath.Clean(strings.TrimSpace(runtimePath))
		if clean == "." || !filepath.IsAbs(clean) {
			closeFiles()
			return nil, func() {}, fmt.Errorf("%w: runtime read path must be absolute", ErrCapabilityUnavailable)
		}
		runtimeFile, runtimeErr := openPinnedPath(clean)
		if runtimeErr != nil {
			closeFiles()
			return nil, func() {}, fmt.Errorf("%w: pin runtime read path %s: %v", ErrCapabilityUnavailable, clean, runtimeErr)
		}
		info, statErr := runtimeFile.Stat()
		if statErr != nil || !info.Mode().IsRegular() {
			_ = runtimeFile.Close()
			closeFiles()
			if statErr == nil {
				statErr = fmt.Errorf("not a regular file")
			}
			return nil, func() {}, fmt.Errorf("%w: runtime read path %s: %v", ErrCapabilityUnavailable, clean, statErr)
		}
		index := len(fds)
		fds = append(fds, runtimeFile)
		args = append(args, "--runtime-read-fd="+helperFD(3+index))
	}

	binaryFD, err := openPinnedPath(binary)
	if err != nil {
		closeFiles()
		return nil, func() {}, fmt.Errorf("%w: pin Rsync binary: %v", ErrCapabilityUnavailable, err)
	}
	binaryFDIndex := len(fds)
	fds = append(fds, binaryFD)
	args = append(args, "--mount-exec="+binary, "--mount-exec-fd="+helperFD(3+binaryFDIndex), "--exec-fd="+helperFD(3+binaryFDIndex))
	args = append(args, "--")
	args = append(args, request.Args...)
	cmd := exec.CommandContext(ctx, helper, args...)
	cmd.ExtraFiles = fds
	cleanup := func() { closeFiles() }
	return cmd, cleanup, nil
}

func ensurePinnedTargetParent(path string, roots []string) error {
	parent := filepath.Dir(path)
	existingPath := parent
	existing, err := openPinnedPath(existingPath)
	for err != nil && errorsIsNotExist(err) && existingPath != filepath.Dir(existingPath) {
		existingPath = filepath.Dir(existingPath)
		existing, err = openPinnedPath(existingPath)
	}
	if err != nil {
		return fmt.Errorf("%w: pin destination parent %s: %v", ErrCapabilityUnavailable, existingPath, err)
	}
	defer func() { _ = existing.Close() }()
	var stat unix.Stat_t
	if err := unix.Fstat(int(existing.Fd()), &stat); err != nil {
		return fmt.Errorf("%w: inspect destination parent %s: %v", ErrCapabilityUnavailable, existingPath, err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return fmt.Errorf("%w: destination parent is not a directory", ErrCapabilityUnavailable)
	}
	if len(roots) > 0 {
		if err := validatePinnedPathFD(int(existing.Fd()), roots, "rsync_target"); err != nil {
			return err
		}
	}
	relative, err := filepath.Rel(existingPath, parent)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return fmt.Errorf("%w: destination parent escapes pinned ancestor", ErrCapabilityUnavailable)
	}
	if relative == "." {
		return nil
	}
	current := existing
	components := strings.Split(relative, string(filepath.Separator))
	for _, component := range components {
		if component == "" || component == "." || component == ".." {
			return fmt.Errorf("%w: invalid destination parent component", ErrCapabilityUnavailable)
		}
		if mkdirErr := unix.Mkdirat(int(current.Fd()), component, 0o755); mkdirErr != nil && mkdirErr != unix.EEXIST {
			return fmt.Errorf("%w: create destination parent: %v", ErrCapabilityUnavailable, mkdirErr)
		}
		nextFD, openErr := unix.Openat(int(current.Fd()), component, unix.O_PATH|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if openErr != nil {
			return fmt.Errorf("%w: pin destination parent component: %v", ErrCapabilityUnavailable, openErr)
		}
		var nextStat unix.Stat_t
		if statErr := unix.Fstat(nextFD, &nextStat); statErr != nil {
			_ = unix.Close(nextFD)
			return fmt.Errorf("%w: inspect destination parent component: %v", ErrCapabilityUnavailable, statErr)
		}
		if nextStat.Mode&unix.S_IFMT != unix.S_IFDIR {
			_ = unix.Close(nextFD)
			return fmt.Errorf("%w: destination parent component is not a directory", ErrCapabilityUnavailable)
		}
		next := os.NewFile(uintptr(nextFD), filepath.Join(existingPath, component))
		if next == nil {
			_ = unix.Close(nextFD)
			return fmt.Errorf("%w: create destination parent descriptor", ErrCapabilityUnavailable)
		}
		if current != existing {
			_ = current.Close()
		}
		current = next
	}
	if current != existing {
		_ = current.Close()
	}
	return nil
}

func errorsIsNotExist(err error) bool {
	return err == unix.ENOENT || err == unix.ENOTDIR || os.IsNotExist(err)
}

func openPinnedPath(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}
func shouldPreserveLocalSourceOperand(args []string, path string, pinned *os.File) bool {
	if pinned == nil || path == "" || strings.HasSuffix(path, string(filepath.Separator)) {
		return false
	}
	info, err := os.Lstat(path)
	if err != nil {
		return false
	}
	mode := info.Mode()
	if !mode.IsDir() && !mode.IsRegular() && mode&os.ModeSymlink == 0 {
		return false
	}
	pinnedInfo, err := pinned.Stat()
	if err != nil {
		return false
	}
	pinnedMode := pinnedInfo.Mode()
	if !pinnedMode.IsDir() && !pinnedMode.IsRegular() {
		return false
	}
	start := helperOperandStart(args)
	cleanPath := filepath.Clean(path)
	for index, argument := range args {
		if index < start || strings.HasSuffix(argument, string(filepath.Separator)) {
			continue
		}
		if filepath.Clean(strings.TrimSuffix(argument, string(filepath.Separator))) == cleanPath {
			return true
		}
	}
	return false
}
