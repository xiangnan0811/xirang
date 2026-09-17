//go:build linux

package rsyncconfinement

import (
	"debug/elf"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"
	"syscall"
)

const (
	landlockCreateRulesetFlagVersion = 1
	minimumLandlockABI               = 3
)

const landlockHandledAccess = unix.LANDLOCK_ACCESS_FS_EXECUTE |
	unix.LANDLOCK_ACCESS_FS_WRITE_FILE |
	unix.LANDLOCK_ACCESS_FS_READ_FILE |
	unix.LANDLOCK_ACCESS_FS_READ_DIR |
	unix.LANDLOCK_ACCESS_FS_REMOVE_DIR |
	unix.LANDLOCK_ACCESS_FS_REMOVE_FILE |
	unix.LANDLOCK_ACCESS_FS_MAKE_CHAR |
	unix.LANDLOCK_ACCESS_FS_MAKE_DIR |
	unix.LANDLOCK_ACCESS_FS_MAKE_REG |
	unix.LANDLOCK_ACCESS_FS_MAKE_SOCK |
	unix.LANDLOCK_ACCESS_FS_MAKE_FIFO |
	unix.LANDLOCK_ACCESS_FS_MAKE_BLOCK |
	unix.LANDLOCK_ACCESS_FS_MAKE_SYM |
	unix.LANDLOCK_ACCESS_FS_TRUNCATE |
	unix.LANDLOCK_ACCESS_FS_REFER

const (
	// Directory traversal is supplied only by the read-only root rule. Data
	// roots therefore do not need EXECUTE, which prevents an Rsync data file
	// from becoming an executable exception.
	landlockDataReadAccess = unix.LANDLOCK_ACCESS_FS_READ_FILE | unix.LANDLOCK_ACCESS_FS_READ_DIR
	landlockRuntimeAccess  = unix.LANDLOCK_ACCESS_FS_EXECUTE | unix.LANDLOCK_ACCESS_FS_READ_FILE
	landlockWriteAccess    = landlockDataReadAccess |
		unix.LANDLOCK_ACCESS_FS_WRITE_FILE |
		unix.LANDLOCK_ACCESS_FS_REMOVE_DIR |
		unix.LANDLOCK_ACCESS_FS_REMOVE_FILE |
		unix.LANDLOCK_ACCESS_FS_MAKE_DIR |
		unix.LANDLOCK_ACCESS_FS_MAKE_REG |
		unix.LANDLOCK_ACCESS_FS_MAKE_SYM |
		unix.LANDLOCK_ACCESS_FS_MAKE_FIFO |
		unix.LANDLOCK_ACCESS_FS_MAKE_CHAR |
		unix.LANDLOCK_ACCESS_FS_MAKE_BLOCK |
		unix.LANDLOCK_ACCESS_FS_MAKE_SOCK |
		unix.LANDLOCK_ACCESS_FS_TRUNCATE |
		unix.LANDLOCK_ACCESS_FS_REFER
)

// These files are consulted by the dynamic loader and OpenSSH on common Linux
// installations.  They are added individually rather than granting execute
// or read access to a broad system directory.
var runtimeReadSupportFiles = []string{
	"/dev/null",
	"/etc/ld.so.cache",
	"/etc/passwd",
	"/etc/group",
	"/etc/nsswitch.conf",
	"/etc/hosts",
	"/etc/resolv.conf",
	"/etc/ssh/ssh_config",
}

type landlockRule struct {
	allowed uint64
	fd      int
}

func executeHelper(request helperRequest) error {
	if err := validateHelperCommand(request); err != nil {
		return err
	}
	if err := validateHelperRsyncArgs(request.Args); err != nil {
		return err
	}
	binary, err := resolveHelperBinary(request.Binary)
	if err != nil {
		return fmt.Errorf("%w: resolve Rsync binary: %v", ErrCapabilityUnavailable, err)
	}
	request.Binary = binary
	opened := make([]int, 0, len(request.ReadRoots)+len(request.WriteRoots)+len(request.ExecRoots)+len(request.ReadFDs)+len(request.RuntimeReadFDs)+len(request.WriteFDs)+len(request.ExecFDs)+8)
	defer func() {
		for _, fd := range opened {
			_ = unix.Close(fd)
		}
		closeHelperFDs(request)
	}()

	if request.MountReadPath != "" && request.MountReadFD < 0 {
		mountPath, fd, openErr := openHelperMount(request.MountReadPath, false, request.ReadRoots)
		if openErr != nil {
			return fmt.Errorf("%w: open remote source: %v", ErrCapabilityUnavailable, openErr)
		}
		request.MountReadPath, request.MountReadFD = mountPath, fd
		opened = append(opened, fd)
	}
	if request.MountWritePath != "" && request.MountWriteFD < 0 {
		mountPath, fd, openErr := openHelperMount(request.MountWritePath, true, request.WriteRoots)
		if openErr != nil {
			return fmt.Errorf("%w: open remote target: %v", ErrCapabilityUnavailable, openErr)
		}
		request.MountWritePath, request.MountWriteFD = mountPath, fd
		opened = append(opened, fd)
	}
	if request.MountExecPath == "" {
		request.MountExecPath = request.Binary
	}
	if request.MountExecFD < 0 {
		fd, openErr := openHelperRoot(request.MountExecPath)
		if openErr != nil {
			return fmt.Errorf("%w: open Rsync executable: %v", ErrCapabilityUnavailable, openErr)
		}
		request.MountExecFD = fd
		opened = append(opened, fd)
	}
	if request.MountReadPath != "" && len(request.ReadRoots) > 0 {
		if err := validatePinnedPathFD(request.MountReadFD, request.ReadRoots, "remote rsync source"); err != nil {
			return err
		}
	}
	if request.MountWritePath != "" && len(request.WriteRoots) > 0 {
		if err := validatePinnedPathFD(request.MountWriteFD, request.WriteRoots, "remote rsync target"); err != nil {
			return err
		}
	}

	var rules []landlockRule
	if request.MountReadPath != "" {
		allowed, accessErr := landlockReadAccessForFD(request.MountReadFD)
		if accessErr != nil {
			return accessErr
		}
		rules = append(rules, landlockRule{allowed: allowed, fd: request.MountReadFD})
	} else {
		for _, root := range request.ReadRoots {
			fd, openErr := openHelperRoot(root)
			if openErr != nil {
				return fmt.Errorf("%w: open remote read root %s: %v", ErrCapabilityUnavailable, root, openErr)
			}
			opened = append(opened, fd)
			rules = append(rules, landlockRule{allowed: landlockDataReadAccess, fd: fd})
		}
	}
	if request.MountWritePath != "" {
		allowed, accessErr := landlockWriteAccessForFD(request.MountWriteFD)
		if accessErr != nil {
			return accessErr
		}
		rules = append(rules, landlockRule{allowed: allowed, fd: request.MountWriteFD})
	} else {
		for _, root := range request.WriteRoots {
			fd, openErr := openHelperRoot(root)
			if openErr != nil {
				return fmt.Errorf("%w: open remote write root %s: %v", ErrCapabilityUnavailable, root, openErr)
			}
			opened = append(opened, fd)
			rules = append(rules, landlockRule{allowed: landlockWriteAccess, fd: fd})
		}
	}
	for _, fd := range request.RuntimeReadFDs {
		var stat unix.Stat_t
		if statErr := unix.Fstat(fd, &stat); statErr != nil {
			return fmt.Errorf("%w: inspect runtime read descriptor: %v", ErrCapabilityUnavailable, statErr)
		}
		if stat.Mode&unix.S_IFMT != unix.S_IFREG {
			return fmt.Errorf("%w: runtime read descriptor must be a regular file", ErrCapabilityUnavailable)
		}
		// A regular runtime file gets content access only on this
		// descriptor's inode. Never grant READ_FILE on a containing
		// directory: that would expose unrelated siblings to Rsync.
		rules = append(rules, landlockRule{allowed: unix.LANDLOCK_ACCESS_FS_READ_FILE, fd: fd})
	}
	for _, fd := range request.ReadFDs {
		allowed, accessErr := landlockReadAccessForFD(fd)
		if accessErr != nil {
			return accessErr
		}
		rules = append(rules, landlockRule{allowed: allowed, fd: fd})
	}
	for _, fd := range request.WriteFDs {
		allowed, accessErr := landlockWriteAccessForFD(fd)
		if accessErr != nil {
			return accessErr
		}
		rules = append(rules, landlockRule{allowed: allowed, fd: fd})
	}
	for _, root := range request.ExecRoots {
		fd, openErr := openHelperRoot(root)
		if openErr != nil {
			return fmt.Errorf("%w: open remote execute root %s: %v", ErrCapabilityUnavailable, root, openErr)
		}
		opened = append(opened, fd)
		rules = append(rules, landlockRule{allowed: unix.LANDLOCK_ACCESS_FS_EXECUTE, fd: fd})
	}
	for _, fd := range request.ExecFDs {
		rules = append(rules, landlockRule{allowed: unix.LANDLOCK_ACCESS_FS_EXECUTE, fd: fd})
	}
	if request.MountExecFD >= 0 {
		rules = append(rules, landlockRule{allowed: unix.LANDLOCK_ACCESS_FS_EXECUTE, fd: request.MountExecFD})
	}

	rootFD, rootErr := openHelperRoot("/")
	if rootErr != nil {
		return fmt.Errorf("%w: open filesystem root: %v", ErrCapabilityUnavailable, rootErr)
	}
	opened = append(opened, rootFD)
	// Landlock requires traversal rights for absolute-path lookup, but the
	// root rule deliberately grants no READ_FILE access. Content access comes
	// only from the pinned operands, configured roots, and exact runtime files
	// below.
	rules = append(rules, landlockRule{
		allowed: unix.LANDLOCK_ACCESS_FS_EXECUTE | unix.LANDLOCK_ACCESS_FS_READ_DIR,
		fd:      rootFD,
	})
	usesSSH := helperUsesSSH(request.Args)
	runtimeFiles, runtimeErr := runtimeFilePaths(request.Binary, usesSSH)
	if runtimeErr != nil {
		return runtimeErr
	}
	for _, path := range runtimeFiles {
		fd, openErr := openHelperRoot(path)
		if openErr != nil {
			return fmt.Errorf("%w: open runtime file %s: %v", ErrCapabilityUnavailable, path, openErr)
		}
		opened = append(opened, fd)
		rules = append(rules, landlockRule{allowed: landlockRuntimeAccess, fd: fd})
	}
	for index, path := range runtimeReadSupportFiles {
		if index >= 2 && !usesSSH {
			continue
		}
		fd, openErr := openHelperRoot(path)
		if openErr != nil {
			if errors.Is(openErr, unix.ENOENT) || errors.Is(openErr, unix.ENOTDIR) {
				continue
			}
			return fmt.Errorf("%w: open runtime support file %s: %v", ErrCapabilityUnavailable, path, openErr)
		}
		var stat unix.Stat_t
		if statErr := unix.Fstat(fd, &stat); statErr != nil {
			_ = unix.Close(fd)
			return fmt.Errorf("%w: inspect runtime support file %s: %v", ErrCapabilityUnavailable, path, statErr)
		}
		modeType := stat.Mode & unix.S_IFMT
		if modeType != unix.S_IFREG && modeType != unix.S_IFCHR {
			_ = unix.Close(fd)
			continue
		}
		allowed := uint64(unix.LANDLOCK_ACCESS_FS_READ_FILE)
		if path == "/dev/null" {
			allowed |= unix.LANDLOCK_ACCESS_FS_WRITE_FILE
		}
		rules = append(rules, landlockRule{allowed: allowed, fd: fd})
	}
	aliasCleanup, keepFDs, aliases, aliasErr := setupOperandAliases(&request, opened)
	if aliasErr != nil {
		return aliasErr
	}
	defer aliasCleanup()
	if err := dropHelperCapabilities(); err != nil {
		return err
	}
	if err := installLandlock(rules); err != nil {
		return err
	}
	closeHelperFDsExcept(request, keepFDs)
	for _, fd := range opened {
		if _, keep := keepFDs[fd]; keep {
			continue
		}
		_ = unix.Close(fd)
	}
	if len(aliases) == 0 {
		return unix.Exec(request.Binary, append([]string{request.Binary}, request.Args...), sanitizedEnvironment(os.Environ()))
	}
	return runConfinedChild(request, aliases)
}

type operandAlias struct {
	original      string
	alias         string
	fd            int
	childFD       int
	directoryRoot bool
}

type helperCleanupMount struct {
	path      string
	directory bool
	mounted   bool
}

func setupOperandAliases(request *helperRequest, opened []int) (func(), map[int]struct{}, []operandAlias, error) {
	keepFDs := make(map[int]struct{})
	if request == nil {
		return func() {}, keepFDs, nil, nil
	}
	aliases := make([]operandAlias, 0, 3)
	matchesPath := func(argument, path string) bool {
		if path == "" {
			return false
		}
		cleanArgument := filepath.Clean(strings.TrimSuffix(argument, string(filepath.Separator)))
		return cleanArgument == filepath.Clean(path)
	}
	findOperand := func(path string) (bool, bool) {
		if path == "" {
			return false, false
		}
		start := helperOperandStart(request.Args)
		for index, argument := range request.Args {
			if index < start {
				continue
			}
			if matchesPath(argument, path) {
				return true, strings.HasSuffix(argument, string(filepath.Separator))
			}
		}
		return false, false
	}
	needsAlias := func(path string) bool {
		if path == "" {
			return false
		}
		start := helperOperandStart(request.Args)
		for index, argument := range request.Args {
			if index >= start && matchesPath(argument, path) {
				return true
			}
		}
		return false
	}
	preserveSourceRoot := false
	preserveSourceFile := false
	if request.PreserveDirectoryRoot {
		found, trailing := findOperand(request.MountReadPath)
		if !found {
			return func() {}, keepFDs, nil, fmt.Errorf("%w: directory-root preservation source operand is missing", ErrInvalidRequest)
		}
		var stat unix.Stat_t
		if err := unix.Fstat(request.MountReadFD, &stat); err != nil {
			return func() {}, keepFDs, nil, fmt.Errorf("%w: inspect pinned directory-root source: %v", ErrCapabilityUnavailable, err)
		}
		if !trailing {
			switch stat.Mode & unix.S_IFMT {
			case unix.S_IFDIR:
				preserveSourceRoot = true
			case unix.S_IFREG:
				preserveSourceFile = true
			default:
				return func() {}, keepFDs, nil, fmt.Errorf("%w: no-trailing source must be a regular file or directory", ErrCapabilityUnavailable)
			}
		}
	}
	if !needsAlias(request.MountReadPath) && !needsAlias(request.MountWritePath) {
		return func() {}, keepFDs, nil, nil
	}
	namespaceCleanup := func() {}
	if preserveSourceRoot || preserveSourceFile {
		var namespaceErr error
		namespaceCleanup, namespaceErr = enterPinnedOperandNamespace(*request, opened)
		if namespaceErr != nil {
			return func() {}, keepFDs, nil, namespaceErr
		}
	}
	aliasRoot, err := os.MkdirTemp("", ".xirang-rsync-confined-*")
	if err != nil {
		return func() {}, keepFDs, nil, fmt.Errorf("%w: create operand alias directory: %v", ErrCapabilityUnavailable, err)
	}
	namespaceActive := preserveSourceRoot || preserveSourceFile
	mountedPaths := make([]helperCleanupMount, 0, 3)
	var cleanupCommand *exec.Cmd
	var cleanupSignal io.WriteCloser
	cleanup := func() {
		if cleanupCommand != nil {
			_ = cleanupSignal.Close()
			_ = cleanupCommand.Wait()
		} else {
			for index := len(mountedPaths) - 1; index >= 0; index-- {
				if mountedPaths[index].mounted {
					_ = unix.Unmount(mountedPaths[index].path, unix.MNT_DETACH)
				}
			}
		}
		namespaceCleanup()
		_ = os.RemoveAll(aliasRoot)
	}
	addAlias := func(path string, fd int, name string) error {
		if !needsAlias(path) {
			return nil
		}
		if fd < 0 {
			return fmt.Errorf("%w: pinned %s descriptor is unavailable", ErrCapabilityUnavailable, name)
		}
		base := filepath.Base(filepath.Clean(path))
		if base == "" || base == "." || base == string(filepath.Separator) {
			base = name
		}
		directoryRoot := preserveSourceRoot && name == "source" && filepath.Clean(path) == filepath.Clean(request.MountReadPath)
		directory := filepath.Join(aliasRoot, name)
		childFD := 3 + len(aliases)
		var aliasPath string
		if namespaceActive {
			if err := os.MkdirAll(directory, 0o700); err != nil {
				return fmt.Errorf("%w: create %s alias directory: %v", ErrCapabilityUnavailable, name, err)
			}
			aliasPath = filepath.Join(directory, base)
			var stat unix.Stat_t
			if err := unix.Fstat(fd, &stat); err != nil {
				return fmt.Errorf("%w: inspect pinned %s descriptor: %v", ErrCapabilityUnavailable, name, err)
			}
			isDirectory := stat.Mode&unix.S_IFMT == unix.S_IFDIR
			if isDirectory {
				if err := os.Mkdir(aliasPath, 0o700); err != nil {
					return fmt.Errorf("%w: create %s directory mountpoint: %v", ErrCapabilityUnavailable, name, err)
				}
			} else if stat.Mode&unix.S_IFMT == unix.S_IFREG {
				file, createErr := os.OpenFile(aliasPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
				if createErr != nil {
					return fmt.Errorf("%w: create %s file mountpoint: %v", ErrCapabilityUnavailable, name, createErr)
				}
				_ = file.Close()
			} else {
				return fmt.Errorf("%w: pinned %s descriptor must be a regular file or directory", ErrCapabilityUnavailable, name)
			}
			if err := bindPinnedOperand(fd, aliasPath, isDirectory, name != "target"); err != nil {
				return err
			}
			mountedPaths = append(mountedPaths, helperCleanupMount{path: aliasPath, directory: isDirectory, mounted: true})
			childFD = -1
			if directoryRoot {
				aliasPath = directory
			}
		} else {
			if err := os.MkdirAll(directory, 0o700); err != nil {
				return fmt.Errorf("%w: create %s alias directory: %v", ErrCapabilityUnavailable, name, err)
			}
			aliasPath = filepath.Join(directory, base)
			if err := os.Symlink(fmt.Sprintf("/proc/self/fd/%d", childFD), aliasPath); err != nil {
				return fmt.Errorf("%w: create %s operand alias: %v", ErrCapabilityUnavailable, name, err)
			}
		}
		aliases = append(aliases, operandAlias{
			original:      filepath.Clean(path),
			alias:         aliasPath,
			fd:            fd,
			childFD:       childFD,
			directoryRoot: directoryRoot,
		})
		keepFDs[fd] = struct{}{}
		return nil
	}
	if err := addAlias(request.MountReadPath, request.MountReadFD, "source"); err != nil {
		cleanup()
		return func() {}, keepFDs, nil, err
	}
	if err := addAlias(request.MountWritePath, request.MountWriteFD, "target"); err != nil {
		cleanup()
		return func() {}, keepFDs, nil, err
	}
	request.Args = rewriteOperandAliases(request.Args, aliases)
	cleanupPaths := append([]helperCleanupMount(nil), mountedPaths...)
	for _, alias := range aliases {
		if alias.childFD >= 0 {
			cleanupPaths = append(cleanupPaths, helperCleanupMount{path: alias.alias, mounted: false})
		}
	}
	if len(cleanupPaths) > 0 {
		cleanupCommand, cleanupSignal, err = startHelperCleanupProcess(aliasRoot, cleanupPaths)
		if err != nil {
			cleanup()
			return func() {}, keepFDs, nil, err
		}
	}
	return cleanup, keepFDs, aliases, nil
}

const (
	helperNamespaceReadyEnv = "XIRANG_RSYNC_NAMESPACE_READY"
	helperNamespaceUserFD   = "XIRANG_RSYNC_NAMESPACE_USER_FD"
	helperNamespaceMountFD  = "XIRANG_RSYNC_NAMESPACE_MOUNT_FD"
)

func enterPinnedOperandNamespace(request helperRequest, opened []int) (func(), error) {
	if os.Getenv(helperNamespaceReadyEnv) == "1" {
		userFD, userErr := strconv.Atoi(os.Getenv(helperNamespaceUserFD))
		mountFD, mountErr := strconv.Atoi(os.Getenv(helperNamespaceMountFD))
		if userErr == nil && mountErr == nil &&
			namespaceDiffers(userFD, "/proc/self/ns/user") &&
			namespaceDiffers(mountFD, "/proc/self/ns/mnt") {
			_ = unix.Close(userFD)
			_ = unix.Close(mountFD)
			_ = os.Unsetenv(helperNamespaceReadyEnv)
			_ = os.Unsetenv(helperNamespaceUserFD)
			_ = os.Unsetenv(helperNamespaceMountFD)
			return func() {}, nil
		}
		if userErr == nil {
			_ = unix.Close(userFD)
		}
		if mountErr == nil {
			_ = unix.Close(mountFD)
		}
		_ = os.Unsetenv(helperNamespaceReadyEnv)
		_ = os.Unsetenv(helperNamespaceUserFD)
		_ = os.Unsetenv(helperNamespaceMountFD)
	}
	launcher, err := resolveNamespaceLauncher()
	if err != nil {
		return func() {}, fmt.Errorf("%w: create private mount namespace: %v", ErrCapabilityUnavailable, err)
	}
	executable, err := os.Executable()
	if err != nil {
		return func() {}, fmt.Errorf("%w: resolve confinement helper executable: %v", ErrCapabilityUnavailable, err)
	}
	userFD, err := unix.Open("/proc/self/ns/user", unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return func() {}, fmt.Errorf("%w: preserve user namespace: %v", ErrCapabilityUnavailable, err)
	}
	mountFD, err := unix.Open("/proc/self/ns/mnt", unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		_ = unix.Close(userFD)
		return func() {}, fmt.Errorf("%w: preserve mount namespace: %v", ErrCapabilityUnavailable, err)
	}
	closeNamespaceFDs := func() {
		_ = unix.Close(userFD)
		_ = unix.Close(mountFD)
	}
	if err := preserveHelperFDsForNamespace(request, append(opened, userFD, mountFD)); err != nil {
		closeNamespaceFDs()
		return func() {}, err
	}
	if err := os.Setenv(helperNamespaceReadyEnv, "1"); err != nil {
		closeNamespaceFDs()
		return func() {}, fmt.Errorf("%w: prepare private mount namespace: %v", ErrCapabilityUnavailable, err)
	}
	if err := os.Setenv(helperNamespaceUserFD, strconv.Itoa(userFD)); err != nil {
		closeNamespaceFDs()
		_ = os.Unsetenv(helperNamespaceReadyEnv)
		return func() {}, fmt.Errorf("%w: prepare private mount namespace: %v", ErrCapabilityUnavailable, err)
	}
	if err := os.Setenv(helperNamespaceMountFD, strconv.Itoa(mountFD)); err != nil {
		closeNamespaceFDs()
		_ = os.Unsetenv(helperNamespaceReadyEnv)
		_ = os.Unsetenv(helperNamespaceUserFD)
		return func() {}, fmt.Errorf("%w: prepare private mount namespace: %v", ErrCapabilityUnavailable, err)
	}
	args := []string{
		"--user",
		"--map-root-user",
		"--mount",
		"--propagation",
		"private",
		"--",
		executable,
	}
	args = append(args, os.Args[1:]...)
	if err := unix.Exec(launcher, args, os.Environ()); err != nil {
		closeNamespaceFDs()
		_ = os.Unsetenv(helperNamespaceReadyEnv)
		_ = os.Unsetenv(helperNamespaceUserFD)
		_ = os.Unsetenv(helperNamespaceMountFD)
		return func() {}, fmt.Errorf("%w: create private mount namespace: %v", ErrCapabilityUnavailable, err)
	}
	panic("unreachable")
}

func resolveNamespaceLauncher() (string, error) {
	for _, candidate := range []string{"/usr/bin/unshare", "/bin/unshare"} {
		info, err := os.Stat(candidate)
		if err != nil {
			continue
		}
		if info.IsDir() || info.Mode().Perm()&0o111 == 0 {
			continue
		}
		return candidate, nil
	}
	return "", errors.New("unshare is not installed")
}
func resolveMountLauncher() (string, error) {
	for _, candidate := range []string{"/usr/bin/mount", "/bin/mount"} {
		info, err := os.Stat(candidate)
		if err != nil {
			continue
		}
		if info.IsDir() || info.Mode().Perm()&0o111 == 0 {
			continue
		}
		return candidate, nil
	}
	return "", errors.New("mount is not installed")
}

func runHelperCleanup(args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("%w: cleanup channel arguments are not allowed", ErrInvalidRequest)
	}
	if err := authenticateCleanupChannel(helperCleanupControlFD); err != nil {
		return err
	}
	rootName, mounts, err := readCleanupMessage(helperCleanupControlFD)
	if err != nil {
		return err
	}
	if err := validateCleanupDescriptors(helperCleanupRootFD, helperCleanupParentFD, rootName); err != nil {
		return err
	}
	if err := waitForCleanupRelease(helperCleanupControlFD); err != nil {
		return err
	}
	return cleanupMountedAliases(helperCleanupRootFD, helperCleanupParentFD, rootName, mounts)
}

const (
	helperCleanupControlFD = 3
	helperCleanupRootFD    = 4
	helperCleanupParentFD  = 5
	helperCleanupMessage   = "xirang-rsync-cleanup-v1\x00"
)

func authenticateCleanupChannel(fd int) error {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return fmt.Errorf("%w: cleanup channel is unavailable: %v", ErrCapabilityUnavailable, err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFSOCK {
		return fmt.Errorf("%w: cleanup channel is not a socket", ErrCapabilityUnavailable)
	}
	socketType, err := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_TYPE)
	if err != nil || socketType != unix.SOCK_SEQPACKET {
		if err == nil {
			err = fmt.Errorf("unexpected socket type %d", socketType)
		}
		return fmt.Errorf("%w: cleanup channel is not authenticated: %v", ErrCapabilityUnavailable, err)
	}
	peer, err := unix.GetsockoptUcred(fd, unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil {
		return fmt.Errorf("%w: inspect cleanup channel peer: %v", ErrCapabilityUnavailable, err)
	}
	if peer == nil || peer.Pid <= 1 || int(peer.Pid) != os.Getppid() ||
		peer.Uid != uint32(os.Getuid()) || peer.Gid != uint32(os.Getgid()) {
		return fmt.Errorf("%w: cleanup channel peer is not the confinement helper", ErrCapabilityUnavailable)
	}
	self, err := filepath.EvalSymlinks("/proc/self/exe")
	if err != nil {
		return fmt.Errorf("%w: resolve cleanup helper identity: %v", ErrCapabilityUnavailable, err)
	}
	peerExecutable, err := filepath.EvalSymlinks(fmt.Sprintf("/proc/%d/exe", peer.Pid))
	if err != nil || filepath.Clean(peerExecutable) != filepath.Clean(self) {
		if err == nil {
			err = fmt.Errorf("peer executable %q differs from helper %q", peerExecutable, self)
		}
		return fmt.Errorf("%w: cleanup channel peer is not the confinement helper: %v", ErrCapabilityUnavailable, err)
	}
	return nil
}

func readCleanupMessage(fd int) (string, []helperCleanupMount, error) {
	var payload [64 * 1024]byte
	size, err := unix.Read(fd, payload[:])
	if err != nil {
		return "", nil, fmt.Errorf("%w: read cleanup channel: %v", ErrCapabilityUnavailable, err)
	}
	data := payload[:size]
	offset := 0
	take := func(length int) ([]byte, error) {
		if length < 0 || len(data)-offset < length {
			return nil, fmt.Errorf("%w: malformed cleanup channel message", ErrInvalidRequest)
		}
		value := data[offset : offset+length]
		offset += length
		return value, nil
	}
	magic, err := take(len(helperCleanupMessage))
	if err != nil || string(magic) != helperCleanupMessage {
		return "", nil, fmt.Errorf("%w: invalid cleanup channel message", ErrInvalidRequest)
	}
	readUint32 := func() (uint32, error) {
		raw, readErr := take(4)
		if readErr != nil {
			return 0, readErr
		}
		return binary.LittleEndian.Uint32(raw), nil
	}
	rootNameLength, err := readUint32()
	if err != nil || rootNameLength == 0 || rootNameLength > 255 {
		return "", nil, fmt.Errorf("%w: invalid cleanup root name", ErrInvalidRequest)
	}
	rootNameBytes, err := take(int(rootNameLength))
	if err != nil {
		return "", nil, err
	}
	rootName := string(rootNameBytes)
	if !validCleanupComponent(rootName) {
		return "", nil, fmt.Errorf("%w: invalid cleanup root name", ErrInvalidRequest)
	}
	mountCount, err := readUint32()
	if err != nil || mountCount == 0 || mountCount > 16 {
		return "", nil, fmt.Errorf("%w: invalid cleanup mount count", ErrInvalidRequest)
	}
	mounts := make([]helperCleanupMount, 0, mountCount)
	for index := uint32(0); index < mountCount; index++ {
		pathLength, pathErr := readUint32()
		if pathErr != nil || pathLength == 0 || pathLength > 4096 {
			return "", nil, fmt.Errorf("%w: invalid cleanup mount path", ErrInvalidRequest)
		}
		pathBytes, pathErr := take(int(pathLength))
		if pathErr != nil {
			return "", nil, pathErr
		}
		path := string(pathBytes)
		if !validCleanupRelativePath(path) {
			return "", nil, fmt.Errorf("%w: invalid cleanup mount path", ErrInvalidRequest)
		}
		kind, kindErr := take(1)
		if kindErr != nil || kind[0] > 3 {
			return "", nil, fmt.Errorf("%w: invalid cleanup mount type", ErrInvalidRequest)
		}
		mounts = append(mounts, helperCleanupMount{
			path:      path,
			directory: kind[0]&1 != 0,
			mounted:   kind[0]&2 != 0,
		})
	}
	if offset != len(data) {
		return "", nil, fmt.Errorf("%w: trailing cleanup channel data", ErrInvalidRequest)
	}
	return rootName, mounts, nil
}

func waitForCleanupRelease(fd int) error {
	var release [1]byte
	size, err := unix.Read(fd, release[:])
	if err != nil {
		return fmt.Errorf("%w: wait for cleanup release: %v", ErrCapabilityUnavailable, err)
	}
	if size != 0 {
		return fmt.Errorf("%w: unexpected cleanup channel data", ErrInvalidRequest)
	}
	return nil
}

func validCleanupComponent(value string) bool {
	return value != "" && value != "." && value != ".." &&
		filepath.Base(value) == value && !strings.ContainsRune(value, '\x00') &&
		!strings.ContainsRune(value, filepath.Separator)
}

func validCleanupRelativePath(value string) bool {
	clean := filepath.Clean(value)
	return value != "" && !filepath.IsAbs(value) && clean == value &&
		value != "." && value != ".." &&
		!strings.HasPrefix(value, ".."+string(filepath.Separator)) &&
		!strings.ContainsRune(value, '\x00')
}

func validateCleanupDescriptors(rootFD, parentFD int, rootName string) error {
	var rootStat, parentStat unix.Stat_t
	if err := unix.Fstat(rootFD, &rootStat); err != nil {
		return fmt.Errorf("%w: inspect cleanup root descriptor: %v", ErrCapabilityUnavailable, err)
	}
	if rootStat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return fmt.Errorf("%w: cleanup root descriptor is not a directory", ErrCapabilityUnavailable)
	}
	if err := unix.Fstat(parentFD, &parentStat); err != nil {
		return fmt.Errorf("%w: inspect cleanup parent descriptor: %v", ErrCapabilityUnavailable, err)
	}
	if parentStat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return fmt.Errorf("%w: cleanup parent descriptor is not a directory", ErrCapabilityUnavailable)
	}
	checkFD, err := unix.Openat(parentFD, rootName, unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("%w: reopen cleanup root descriptor: %v", ErrCapabilityUnavailable, err)
	}
	defer func() { _ = unix.Close(checkFD) }()
	var checkStat unix.Stat_t
	if err := unix.Fstat(checkFD, &checkStat); err != nil {
		return fmt.Errorf("%w: inspect reopened cleanup root: %v", ErrCapabilityUnavailable, err)
	}
	if checkStat.Dev != rootStat.Dev || checkStat.Ino != rootStat.Ino {
		return fmt.Errorf("%w: cleanup root descriptor changed", ErrCapabilityUnavailable)
	}
	return nil
}

func cleanupMountedAliases(rootFD, parentFD int, rootName string, mounts []helperCleanupMount) error {
	rootPath := fmt.Sprintf("/proc/self/fd/%d", rootFD)
	topLevel := make(map[string]struct{}, len(mounts))
	for index := len(mounts) - 1; index >= 0; index-- {
		mount := mounts[index]
		relative := filepath.Clean(mount.path)
		topLevel[strings.Split(relative, string(filepath.Separator))[0]] = struct{}{}
		target := filepath.Join(rootPath, relative)
		if mount.mounted {
			if err := unix.Unmount(target, unix.MNT_DETACH); err != nil &&
				!errors.Is(err, unix.ENOENT) && !errors.Is(err, unix.EINVAL) {
				return fmt.Errorf("%w: unmount confined operand: %v", ErrCapabilityUnavailable, err)
			}
		}
		flags := 0
		if mount.directory {
			flags = unix.AT_REMOVEDIR
		}
		if err := unix.Unlinkat(rootFD, relative, flags); err != nil &&
			!errors.Is(err, unix.ENOENT) {
			return fmt.Errorf("%w: remove confined mountpoint: %v", ErrCapabilityUnavailable, err)
		}
	}
	for name := range topLevel {
		if err := unix.Unlinkat(rootFD, name, unix.AT_REMOVEDIR); err != nil &&
			!errors.Is(err, unix.ENOENT) {
			return fmt.Errorf("%w: remove confined operand directory: %v", ErrCapabilityUnavailable, err)
		}
	}
	if err := unix.Unlinkat(parentFD, rootName, unix.AT_REMOVEDIR); err != nil &&
		!errors.Is(err, unix.ENOENT) {
		return fmt.Errorf("%w: remove confined alias root: %v", ErrCapabilityUnavailable, err)
	}
	return nil
}

func startHelperCleanupProcess(aliasRoot string, mountedPaths []helperCleanupMount) (*exec.Cmd, io.WriteCloser, error) {
	payload, err := encodeCleanupMessage(aliasRoot, mountedPaths)
	if err != nil {
		return nil, nil, err
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, nil, fmt.Errorf("%w: resolve cleanup helper executable: %v", ErrCapabilityUnavailable, err)
	}
	rootFD, err := unix.Open(aliasRoot, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: open cleanup alias root: %v", ErrCapabilityUnavailable, err)
	}
	parentFD, err := unix.Open(filepath.Dir(aliasRoot), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		_ = unix.Close(rootFD)
		return nil, nil, fmt.Errorf("%w: open cleanup alias parent: %v", ErrCapabilityUnavailable, err)
	}
	channel, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		_ = unix.Close(rootFD)
		_ = unix.Close(parentFD)
		return nil, nil, fmt.Errorf("%w: create cleanup channel: %v", ErrCapabilityUnavailable, err)
	}
	controlParent := os.NewFile(uintptr(channel[0]), "rsync-cleanup-control")
	controlChild := os.NewFile(uintptr(channel[1]), "rsync-cleanup-control-child")
	rootFile := os.NewFile(uintptr(rootFD), "rsync-cleanup-root")
	parentFile := os.NewFile(uintptr(parentFD), "rsync-cleanup-parent")
	if controlParent == nil || controlChild == nil || rootFile == nil || parentFile == nil {
		for _, file := range []*os.File{controlParent, controlChild, rootFile, parentFile} {
			if file != nil {
				_ = file.Close()
			}
		}
		return nil, nil, fmt.Errorf("%w: prepare cleanup descriptors", ErrCapabilityUnavailable)
	}
	command := exec.Command(executable, helperCleanupCommand)
	command.ExtraFiles = []*os.File{controlChild, rootFile, parentFile}
	command.Env = sanitizedEnvironment(nil)
	command.Stdin = nil
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		_ = controlParent.Close()
		_ = controlChild.Close()
		_ = rootFile.Close()
		_ = parentFile.Close()
		return nil, nil, fmt.Errorf("%w: start alias cleanup: %v", ErrCapabilityUnavailable, err)
	}
	_ = controlChild.Close()
	_ = rootFile.Close()
	_ = parentFile.Close()
	if written, writeErr := controlParent.Write(payload); writeErr != nil || written != len(payload) {
		_ = controlParent.Close()
		_ = command.Process.Kill()
		_ = command.Wait()
		if writeErr == nil {
			writeErr = io.ErrShortWrite
		}
		return nil, nil, fmt.Errorf("%w: authenticate alias cleanup: %v", ErrCapabilityUnavailable, writeErr)
	}
	return command, controlParent, nil
}

func encodeCleanupMessage(aliasRoot string, mountedPaths []helperCleanupMount) ([]byte, error) {
	rootName := filepath.Base(filepath.Clean(aliasRoot))
	if !validCleanupComponent(rootName) || len(mountedPaths) == 0 || len(mountedPaths) > 16 {
		return nil, fmt.Errorf("%w: invalid cleanup descriptor set", ErrCapabilityUnavailable)
	}
	payload := make([]byte, 0, 256)
	payload = append(payload, helperCleanupMessage...)
	appendUint32 := func(value uint32) {
		var encoded [4]byte
		binary.LittleEndian.PutUint32(encoded[:], value)
		payload = append(payload, encoded[:]...)
	}
	appendUint32(uint32(len(rootName)))
	payload = append(payload, rootName...)
	appendUint32(uint32(len(mountedPaths)))
	for _, mount := range mountedPaths {
		relative, err := filepath.Rel(aliasRoot, mount.path)
		if err != nil || !validCleanupRelativePath(relative) || len(relative) > 4096 {
			return nil, fmt.Errorf("%w: invalid cleanup mount path", ErrCapabilityUnavailable)
		}
		appendUint32(uint32(len(relative)))
		payload = append(payload, relative...)
		kind := byte(0)
		if mount.directory {
			kind |= 1
		}
		if mount.mounted {
			kind |= 2
		}
		payload = append(payload, kind)
	}
	return payload, nil
}

func namespaceDiffers(fd int, path string) bool {
	if fd < 0 {
		return false
	}
	expectedType := int(unix.CLONE_NEWUSER)
	switch path {
	case "/proc/self/ns/user":
	case "/proc/self/ns/mnt":
		expectedType = int(unix.CLONE_NEWNS)
	default:
		return false
	}
	namespaceType, err := unix.IoctlRetInt(fd, unix.NS_GET_NSTYPE)
	if err != nil || namespaceType != expectedType {
		return false
	}
	currentFD, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return false
	}
	defer func() { _ = unix.Close(currentFD) }()
	currentType, err := unix.IoctlRetInt(currentFD, unix.NS_GET_NSTYPE)
	if err != nil || currentType != expectedType {
		return false
	}
	var before, current unix.Stat_t
	if err := unix.Fstat(fd, &before); err != nil {
		return false
	}
	if err := unix.Fstat(currentFD, &current); err != nil {
		return false
	}
	return before.Dev != current.Dev || before.Ino != current.Ino
}

func preserveHelperFDsForNamespace(request helperRequest, opened []int) error {
	fds := make([]int, 0, len(opened)+len(request.ReadFDs)+len(request.WriteFDs)+len(request.RuntimeReadFDs)+len(request.ExecFDs)+3)
	fds = append(fds, opened...)
	fds = append(fds, request.MountReadFD, request.MountWriteFD, request.MountExecFD)
	fds = append(fds, request.ReadFDs...)
	fds = append(fds, request.WriteFDs...)
	fds = append(fds, request.RuntimeReadFDs...)
	fds = append(fds, request.ExecFDs...)
	seen := make(map[int]struct{}, len(fds))
	for _, fd := range fds {
		if fd < 0 {
			continue
		}
		if _, ok := seen[fd]; ok {
			continue
		}
		seen[fd] = struct{}{}
		flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
		if err != nil {
			return fmt.Errorf("%w: preserve confinement descriptor %d: %v", ErrCapabilityUnavailable, fd, err)
		}
		if flags&unix.FD_CLOEXEC == 0 {
			continue
		}
		if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFD, flags&^unix.FD_CLOEXEC); err != nil {
			return fmt.Errorf("%w: preserve confinement descriptor %d: %v", ErrCapabilityUnavailable, fd, err)
		}
	}
	if err := closeUnlistedHelperFDs(seen); err != nil {
		return err
	}
	return nil
}

func closeUnlistedHelperFDs(keep map[int]struct{}) error {
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return fmt.Errorf("%w: enumerate confinement descriptors: %v", ErrCapabilityUnavailable, err)
	}
	for _, entry := range entries {
		fd, parseErr := strconv.Atoi(entry.Name())
		if parseErr != nil || fd <= 2 {
			continue
		}
		if _, ok := keep[fd]; ok {
			continue
		}
		_ = unix.Close(fd)
	}
	return nil
}
func bindPinnedOperand(fd int, target string, recursive, readOnly bool) error {
	sourceFD, err := unix.Dup(fd)
	if err != nil {
		return fmt.Errorf("%w: preserve pinned operand for mount: %v", ErrCapabilityUnavailable, err)
	}
	sourceFile := os.NewFile(uintptr(sourceFD), "rsync-pinned-operand")
	if sourceFile == nil {
		_ = unix.Close(sourceFD)
		return fmt.Errorf("%w: preserve pinned operand for mount", ErrCapabilityUnavailable)
	}
	defer func() { _ = sourceFile.Close() }()
	mountBinary, err := resolveMountLauncher()
	if err != nil {
		return fmt.Errorf("%w: bind pinned operand: %v", ErrCapabilityUnavailable, err)
	}
	bindMode := "--bind"
	if recursive {
		bindMode = "--rbind"
	}
	mountCommand := exec.Command(mountBinary, bindMode, "/proc/self/fd/3", target)
	mountCommand.ExtraFiles = []*os.File{sourceFile}
	mountCommand.Stdout = io.Discard
	mountCommand.Stderr = io.Discard
	mountCommand.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	if err := mountCommand.Run(); err != nil {
		return fmt.Errorf("%w: bind pinned operand: %v", ErrCapabilityUnavailable, err)
	}
	if !readOnly {
		return nil
	}
	attributes := &unix.MountAttr{Attr_set: 1}
	var flags uint
	if recursive {
		flags = unix.AT_RECURSIVE
	}
	if err := unix.MountSetattr(unix.AT_FDCWD, target, flags, attributes); err != nil {
		_ = unix.Unmount(target, unix.MNT_DETACH)
		return fmt.Errorf("%w: make pinned operand read-only: %v", ErrCapabilityUnavailable, err)
	}
	return nil
}

func helperOperandStart(args []string) int {
	for index, argument := range args {
		if argument == "--" {
			return index + 1
		}
	}
	return 0
}

func rewriteOperandAliases(args []string, aliases []operandAlias) []string {
	if len(aliases) == 0 {
		return args
	}
	result := append([]string(nil), args...)
	directoryRoot := false
	replacePath := func(value string) (string, bool) {
		clean := filepath.Clean(strings.TrimSuffix(value, string(filepath.Separator)))
		trailing := strings.HasSuffix(value, string(filepath.Separator))
		for _, alias := range aliases {
			if clean != alias.original {
				continue
			}
			replacement := alias.alias
			if alias.directoryRoot {
				replacement = alias.alias + string(filepath.Separator) + "." +
					string(filepath.Separator) + filepath.Base(alias.original) + string(filepath.Separator)
				directoryRoot = true
			} else if trailing && replacement != string(filepath.Separator) {
				replacement += string(filepath.Separator)
			}
			return replacement, true
		}
		return value, false
	}
	start := helperOperandStart(result)
	for index, argument := range result {
		if index < start {
			continue
		}
		if replacement, replaced := replacePath(argument); replaced {
			result[index] = replacement
		}
	}
	if directoryRoot {
		result = addTrustedRsyncRelative(result)
	}
	return result
}

func addTrustedRsyncRelative(args []string) []string {
	insertAt := -1
	if len(args) > 0 && args[0] == "--server" {
		insertAt = 1
		if len(args) > insertAt && args[insertAt] == "--sender" {
			insertAt++
		}
	} else {
		for index, argument := range args {
			if argument == "--" {
				insertAt = index
				break
			}
		}
	}
	if insertAt < 0 || insertAt > len(args) {
		return args
	}
	result := make([]string, 0, len(args)+1)
	result = append(result, args[:insertAt]...)
	result = append(result, "--relative")
	result = append(result, args[insertAt:]...)
	return result
}
func runConfinedChild(request helperRequest, aliases []operandAlias) error {
	extraFiles := make([]*os.File, 0, len(aliases))
	for _, alias := range aliases {
		if alias.childFD < 0 {
			continue
		}
		file := os.NewFile(uintptr(alias.fd), fmt.Sprintf("rsync-confined-fd-%d", alias.childFD))
		if file == nil {
			for _, extra := range extraFiles {
				_ = extra.Close()
			}
			return fmt.Errorf("%w: preserve confined operand descriptor", ErrCapabilityUnavailable)
		}
		extraFiles = append(extraFiles, file)
	}
	command := exec.Command(request.Binary, request.Args...)
	command.ExtraFiles = extraFiles
	command.Env = sanitizedEnvironment(os.Environ())
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	command.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	runErr := command.Run()
	for _, file := range extraFiles {
		_ = file.Close()
	}
	if runErr == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		return &helperExitError{code: exitErr.ExitCode()}
	}
	return runErr
}

func helperUsesSSH(args []string) bool {
	for index, argument := range args {
		switch argument {
		case "-e", "--rsh":
			if index+1 < len(args) {
				value := strings.TrimSpace(args[index+1])
				if value == "ssh" || strings.HasPrefix(value, "ssh ") {
					return true
				}
			}
		default:
			if value, ok := strings.CutPrefix(argument, "--rsh="); ok {
				value = strings.TrimSpace(value)
				if value == "ssh" || strings.HasPrefix(value, "ssh ") {
					return true
				}
			}
		}
	}
	return false
}

func runtimeFilePaths(binary string, usesSSH bool) ([]string, error) {
	queue := []string{binary}
	if usesSSH {
		for _, candidate := range []string{"/bin/sh", "/usr/bin/ssh", "/bin/ssh"} {
			if _, err := os.Stat(candidate); err == nil {
				queue = append(queue, candidate)
			}
		}
	}
	seen := make(map[string]struct{}, len(queue))
	files := make([]string, 0, len(queue))
	for len(queue) > 0 {
		path := filepath.Clean(queue[0])
		queue = queue[1:]
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			if path == filepath.Clean(binary) {
				return nil, fmt.Errorf("%w: resolve runtime file %s: %v", ErrCapabilityUnavailable, path, err)
			}
			continue
		}
		resolved = filepath.Clean(resolved)
		if _, ok := seen[resolved]; ok {
			continue
		}
		seen[resolved] = struct{}{}
		files = append(files, resolved)
		elfFile, openErr := elf.Open(resolved)
		if openErr != nil {
			continue
		}
		interpreter := elfInterpreter(elfFile)
		if interpreter != "" {
			queue = append(queue, interpreter)
		}
		libraries, importErr := elfFile.ImportedLibraries()
		_ = elfFile.Close()
		if importErr != nil {
			continue
		}
		for _, library := range libraries {
			if resolvedLibrary, ok := resolveRuntimeLibrary(library); ok {
				queue = append(queue, resolvedLibrary)
			}
		}
	}
	return files, nil
}

func elfInterpreter(file *elf.File) string {
	if file == nil {
		return ""
	}
	for _, program := range file.Progs {
		if program.Type != elf.PT_INTERP {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(program.Open(), 4096))
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(data))
	}
	return ""
}

func resolveRuntimeLibrary(name string) (string, bool) {
	if filepath.IsAbs(name) {
		_, err := os.Stat(name)
		return filepath.Clean(name), err == nil
	}
	for _, root := range []string{"/lib64", "/usr/lib64", "/lib", "/usr/lib", "/lib/x86_64-linux-gnu", "/usr/lib/x86_64-linux-gnu"} {
		candidate := filepath.Join(root, name)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, true
		}
	}
	return "", false
}

func validateHelperCommand(request helperRequest) error {
	if strings.TrimSpace(request.Binary) == "" {
		return fmt.Errorf("%w: helper binary", ErrInvalidRequest)
	}
	if err := validatePathSyntax(request.Binary); err != nil {
		return fmt.Errorf("%w: helper binary: %v", ErrInvalidRequest, err)
	}
	if request.MountReadPath == "" && request.MountWritePath == "" && len(request.ReadRoots) == 0 && len(request.WriteRoots) == 0 {
		return fmt.Errorf("%w: no filesystem boundary", ErrCapabilityUnavailable)
	}
	for _, path := range append(append(append([]string{}, request.ReadRoots...), request.WriteRoots...), request.ExecRoots...) {
		if err := validateAbsolutePath(path); err != nil {
			return err
		}
	}
	for _, path := range []string{request.MountReadPath, request.MountWritePath, request.MountExecPath} {
		if path == "" {
			continue
		}
		if err := validateAbsolutePath(path); err != nil {
			return err
		}
	}
	if request.PreserveDirectoryRoot && request.MountReadPath == "" {
		return fmt.Errorf("%w: directory-root preservation requires a read mount", ErrInvalidRequest)
	}
	return nil
}
func validateHelperRsyncArgs(args []string) error {
	if len(args) > 0 && args[0] == "--server" {
		return validateConfinedRsyncServerArgs(args)
	}
	return validateConfinedRsyncArgs(args)
}

const confinedRsyncServerFlags = "vlogDtprnze.iLsfxCIuvHAXc"

func validateConfinedRsyncServerArgs(args []string) error {
	if len(args) != 4 && len(args) != 5 {
		return fmt.Errorf("%w: unexpected confined Rsync server argument count", ErrInvalidRequest)
	}
	if args[0] != "--server" {
		return fmt.Errorf("%w: confined Rsync server mode is required", ErrInvalidRequest)
	}
	offset := 1
	if len(args) == 5 {
		if args[1] != "--sender" {
			return fmt.Errorf("%w: unexpected confined Rsync server mode", ErrInvalidRequest)
		}
		offset++
	}
	flags := args[offset]
	if len(flags) < 2 || flags[0] != '-' {
		return fmt.Errorf("%w: malformed confined Rsync server flags", ErrInvalidRequest)
	}
	for _, flag := range flags[1:] {
		if !strings.ContainsRune(confinedRsyncServerFlags, flag) {
			return fmt.Errorf("%w: Rsync server flag -%c is not allowed under filesystem confinement", ErrInvalidRequest, flag)
		}
	}
	if args[offset+1] != "." {
		return fmt.Errorf("%w: malformed confined Rsync server working directory", ErrInvalidRequest)
	}
	if err := validateAbsolutePath(args[offset+2]); err != nil {
		return fmt.Errorf("%w: Rsync server operand: %v", ErrInvalidRequest, err)
	}
	return nil
}

func validateAbsolutePath(path string) error {
	if err := validatePathSyntax(path); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	clean := filepath.Clean(path)
	if path == "" || !filepath.IsAbs(clean) {
		return fmt.Errorf("%w: path must be absolute", ErrInvalidRequest)
	}
	return nil
}

func resolveHelperBinary(binary string) (string, error) {
	if filepath.IsAbs(binary) {
		if _, err := os.Stat(binary); err != nil {
			return "", err
		}
		return filepath.Clean(binary), nil
	}
	return exec.LookPath(binary)
}

func openHelperMount(path string, allowMissing bool, roots []string) (string, int, error) {
	clean := filepath.Clean(strings.TrimSpace(path))
	fd, err := openHelperRoot(clean)
	if err == nil {
		return clean, fd, nil
	}
	if !allowMissing || (!errors.Is(err, unix.ENOENT) && !errors.Is(err, unix.ENOTDIR)) {
		return "", -1, err
	}
	// A missing remote destination must be opened through an already-existing
	// configured root. Falling back to an arbitrary parent would widen a write
	// rule to an unrelated sibling tree.
	for _, root := range roots {
		root = filepath.Clean(strings.TrimSpace(root))
		if root == "" {
			continue
		}
		if validateErr := ValidatePath(clean, []string{root}, "remote rsync target"); validateErr != nil {
			continue
		}
		fd, rootErr := openHelperRoot(root)
		if rootErr == nil {
			return root, fd, nil
		}
		if !errors.Is(rootErr, unix.ENOENT) && !errors.Is(rootErr, unix.ENOTDIR) {
			return "", -1, rootErr
		}
	}
	return "", -1, unix.ENOENT
}

func openHelperRoot(path string) (int, error) {
	fd, err := unix.Open(filepath.Clean(path), unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	return fd, nil
}
func landlockReadAccessForFD(fd int) (uint64, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return 0, fmt.Errorf("%w: inspect read descriptor: %v", ErrCapabilityUnavailable, err)
	}
	switch stat.Mode & unix.S_IFMT {
	case unix.S_IFDIR:
		return landlockDataReadAccess, nil
	case unix.S_IFREG:
		return unix.LANDLOCK_ACCESS_FS_READ_FILE, nil
	default:
		return 0, fmt.Errorf("%w: read descriptor must be a regular file or directory", ErrCapabilityUnavailable)
	}
}

func landlockWriteAccessForFD(fd int) (uint64, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return 0, fmt.Errorf("%w: inspect write descriptor: %v", ErrCapabilityUnavailable, err)
	}
	switch stat.Mode & unix.S_IFMT {
	case unix.S_IFDIR:
		return landlockWriteAccess, nil
	case unix.S_IFREG:
		return unix.LANDLOCK_ACCESS_FS_READ_FILE |
			unix.LANDLOCK_ACCESS_FS_WRITE_FILE |
			unix.LANDLOCK_ACCESS_FS_TRUNCATE, nil
	default:
		return 0, fmt.Errorf("%w: write descriptor must be a regular file or directory", ErrCapabilityUnavailable)
	}
}

func validatePinnedPathFD(fd int, roots []string, label string) error {
	if fd < 0 {
		return fmt.Errorf("%w: %s descriptor is unavailable", ErrCapabilityUnavailable, label)
	}
	actual, err := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", fd))
	if err != nil {
		return fmt.Errorf("%w: resolve pinned %s: %v", ErrCapabilityUnavailable, label, err)
	}
	if strings.HasSuffix(actual, " (deleted)") {
		return fmt.Errorf("%w: pinned %s was deleted", ErrCapabilityUnavailable, label)
	}
	actual = filepath.Clean(actual)
	resolvedRoots := make([]string, 0, len(roots))
	for _, root := range roots {
		root = filepath.Clean(strings.TrimSpace(root))
		if resolved, resolveErr := filepath.EvalSymlinks(root); resolveErr == nil {
			root = filepath.Clean(resolved)
		}
		resolvedRoots = append(resolvedRoots, root)
	}
	if err := ValidatePath(actual, resolvedRoots, label); err != nil {
		return fmt.Errorf("%w: pinned %s escapes configured roots", ErrCapabilityUnavailable, label)
	}
	return nil
}

func validateLandlockABI(abi int) error {
	if abi < minimumLandlockABI {
		return fmt.Errorf("%w: Landlock ABI %d is below required ABI %d (TRUNCATE confinement requires ABI %d)", ErrCapabilityUnavailable, abi, minimumLandlockABI, minimumLandlockABI)
	}
	return nil
}

func dropHelperCapabilities() error {
	header := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	var capabilities [2]unix.CapUserData
	if err := unix.Capget(&header, &capabilities[0]); err != nil {
		return fmt.Errorf("%w: inspect helper capabilities: %v", ErrCapabilityUnavailable, err)
	}
	for index := range capabilities {
		capabilities[index] = unix.CapUserData{}
	}
	if err := unix.Capset(&header, &capabilities[0]); err != nil {
		return fmt.Errorf("%w: drop helper capabilities: %v", ErrCapabilityUnavailable, err)
	}
	var remaining [2]unix.CapUserData
	if err := unix.Capget(&header, &remaining[0]); err != nil {
		return fmt.Errorf("%w: verify helper capabilities: %v", ErrCapabilityUnavailable, err)
	}
	for _, value := range remaining {
		if value.Effective != 0 || value.Permitted != 0 || value.Inheritable != 0 {
			return fmt.Errorf("%w: helper retained capabilities", ErrCapabilityUnavailable)
		}
	}
	return nil
}

func installLandlock(rules []landlockRule) error {
	abi, err := landlockABI()
	if err != nil {
		return fmt.Errorf("%w: Landlock version probe: %v", ErrCapabilityUnavailable, err)
	}
	if err := validateLandlockABI(abi); err != nil {
		return err
	}
	attr := unix.LandlockRulesetAttr{Access_fs: landlockHandledAccess}
	ruleset, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, uintptr(unsafe.Pointer(&attr)), unsafe.Sizeof(attr), 0)
	if errno != 0 {
		return fmt.Errorf("%w: create Landlock ruleset: %v", ErrCapabilityUnavailable, errno)
	}
	if ruleset <= 0 {
		return fmt.Errorf("%w: invalid Landlock ruleset", ErrCapabilityUnavailable)
	}
	defer func() { _ = unix.Close(int(ruleset)) }()
	for _, rule := range rules {
		if rule.fd < 0 || rule.allowed == 0 {
			continue
		}
		pathAttr := unix.LandlockPathBeneathAttr{Allowed_access: rule.allowed, Parent_fd: int32(rule.fd)}
		_, _, errno = unix.Syscall6(unix.SYS_LANDLOCK_ADD_RULE, ruleset, uintptr(unix.LANDLOCK_RULE_PATH_BENEATH), uintptr(unsafe.Pointer(&pathAttr)), 0, 0, 0)
		if errno != 0 {
			return fmt.Errorf("%w: add Landlock path rule: %v", ErrCapabilityUnavailable, errno)
		}
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("%w: set no-new-privileges: %v", ErrCapabilityUnavailable, err)
	}
	if _, _, errno = unix.Syscall6(unix.SYS_LANDLOCK_RESTRICT_SELF, ruleset, 0, 0, 0, 0, 0); errno != 0 {
		return fmt.Errorf("%w: activate Landlock ruleset: %v", ErrCapabilityUnavailable, errno)
	}
	return nil
}

func landlockABI() (int, error) {
	version, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, landlockCreateRulesetFlagVersion)
	if errno != 0 {
		return 0, fmt.Errorf("%w: Landlock version probe: %v", ErrCapabilityUnavailable, errno)
	}
	if version == 0 || version > 1<<20 {
		return 0, fmt.Errorf("%w: invalid Landlock ABI version", ErrCapabilityUnavailable)
	}
	return int(version), nil
}

func sanitizedEnvironment(base []string) []string {
	result := make([]string, 0, len(base)+4)
	for _, entry := range base {
		name, _, ok := strings.Cut(entry, "=")
		if !ok || name == "" || strings.ContainsRune(entry, '\x00') {
			continue
		}
		if strings.HasPrefix(name, "RSYNC_") || strings.HasPrefix(name, "LD_") || name == "HOME" || name == "BASH_ENV" || name == "ENV" || name == "CDPATH" || name == "PYTHONPATH" {
			continue
		}
		result = append(result, entry)
	}
	result = append(result, "HOME=/", "PATH=/usr/bin:/bin", "LC_ALL=C", "LANG=C")
	return result
}
