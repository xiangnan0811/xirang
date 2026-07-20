//go:build linux

package capabilities

import (
	"bufio"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

type mountInfo struct {
	Filesystem string
	Options    map[string]bool
}

type sandboxPolicy struct {
	DeniedSyscalls map[uint32]bool
}

func linuxSandboxPolicy() sandboxPolicy {
	denied := []uint32{
		unix.SYS_SOCKET, unix.SYS_SOCKETPAIR, unix.SYS_CONNECT, unix.SYS_BIND, unix.SYS_LISTEN,
		unix.SYS_ACCEPT, unix.SYS_ACCEPT4, unix.SYS_SENDTO, unix.SYS_SENDMSG, unix.SYS_RECVFROM,
		unix.SYS_RECVMSG, unix.SYS_MOUNT, unix.SYS_UMOUNT2, unix.SYS_PTRACE, unix.SYS_BPF,
		unix.SYS_PERF_EVENT_OPEN, unix.SYS_SETNS, unix.SYS_UNSHARE, unix.SYS_KEXEC_LOAD,
		unix.SYS_INIT_MODULE, unix.SYS_FINIT_MODULE, unix.SYS_DELETE_MODULE, unix.SYS_REBOOT,
		unix.SYS_SWAPON, unix.SYS_SWAPOFF, unix.SYS_SETSID, unix.SYS_SETPGID,
	}
	result := sandboxPolicy{DeniedSyscalls: make(map[uint32]bool, len(denied))}
	for _, number := range denied {
		result.DeniedSyscalls[number] = true
	}
	return result
}

func executeSandbox(request SandboxExecution) error {
	if err := validateSandboxFilesystem(request); err != nil {
		return err
	}
	if err := applySandboxRlimits(request.MaxProcesses); err != nil {
		return ErrSecureWorkspaceUnavailable
	}
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		return ErrSecureWorkspaceUnavailable
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return ErrSecureWorkspaceUnavailable
	}
	if err := applyLandlock(request); err != nil {
		return ErrSecureWorkspaceUnavailable
	}
	if err := applySeccomp(linuxSandboxPolicy()); err != nil {
		return ErrSecureWorkspaceUnavailable
	}
	environment := []string{
		"HOME=" + request.HomeDir,
		"LANG=C.UTF-8",
		"LC_ALL=C.UTF-8",
		"TZ=UTC",
		"XIRANG_OUTPUT_DIR=" + request.OutputDir,
		"XIRANG_INPUT_MODE=" + string(request.InputMode),
	}
	if request.InputMode == ToolInputPath {
		environment = append(environment, "XIRANG_INPUT_PATH="+request.InputPath)
	}
	arguments := append([]string{request.Executable}, request.Args...)
	if err := unix.CloseRange(3, ^uint(0), 0); err != nil && !errors.Is(err, unix.ENOSYS) {
		return ErrSecureWorkspaceUnavailable
	}
	if err := unix.Exec(request.Executable, arguments, environment); err != nil {
		return ErrToolFailed
	}
	return nil
}

func validateSandboxFilesystem(request SandboxExecution) error {
	info, err := inspectWorkspaceMount(request.Workspace)
	if err != nil || validateWorkspaceMount(info) != nil {
		return ErrSecureWorkspaceUnavailable
	}
	for _, directory := range []string{request.Workspace, request.OutputDir, request.HomeDir} {
		info, err := os.Lstat(directory)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 || !ownedByCurrentUID(info) {
			return ErrSecureWorkspaceUnavailable
		}
	}
	if request.InputMode == ToolInputPath {
		info, err := os.Lstat(request.InputPath)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o400 || !ownedByCurrentUID(info) {
			return ErrSecureWorkspaceUnavailable
		}
	}
	tool, err := os.Lstat(request.Executable)
	if err != nil || !tool.Mode().IsRegular() || tool.Mode().Perm()&0o022 != 0 {
		return ErrSecureWorkspaceUnavailable
	}
	return nil
}

func ownedByCurrentUID(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Geteuid()
}

func applySandboxRlimits(maxProcesses int) error {
	limits := []struct {
		resource int
		value    uint64
	}{
		{unix.RLIMIT_CORE, 0},
		{unix.RLIMIT_NOFILE, 64},
		{unix.RLIMIT_NPROC, uint64(maxProcesses)},
	}
	for _, limit := range limits {
		if err := unix.Setrlimit(limit.resource, &unix.Rlimit{Cur: limit.value, Max: limit.value}); err != nil {
			return err
		}
	}
	return nil
}

func applySeccomp(policy sandboxPolicy) error {
	architecture, ok := sandboxAuditArchitecture()
	if !ok {
		return ErrSecureWorkspaceUnavailable
	}
	filters := []unix.SockFilter{
		{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 4},
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jt: 1, Jf: 0, K: architecture},
		{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_KILL_PROCESS},
		{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 0},
	}
	numbers := make([]int, 0, len(policy.DeniedSyscalls))
	for number := range policy.DeniedSyscalls {
		numbers = append(numbers, int(number))
	}
	sort.Ints(numbers)
	for _, number := range numbers {
		filters = append(filters,
			unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jf: 1, K: uint32(number)},
			unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM)},
		)
	}
	filters = append(filters, unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ALLOW})
	program := unix.SockFprog{Len: uint16(len(filters)), Filter: &filters[0]}
	return unix.Prctl(unix.PR_SET_SECCOMP, unix.SECCOMP_MODE_FILTER, uintptr(unsafe.Pointer(&program)), 0, 0)
}

func sandboxAuditArchitecture() (uint32, bool) {
	switch runtime.GOARCH {
	case "amd64":
		return unix.AUDIT_ARCH_X86_64, true
	case "arm64":
		return unix.AUDIT_ARCH_AARCH64, true
	default:
		return 0, false
	}
}

func applyLandlock(request SandboxExecution) error {
	abi, _, errno := unix.Syscall6(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, unix.LANDLOCK_CREATE_RULESET_VERSION, 0, 0, 0)
	if errno != 0 || abi < 1 {
		return ErrSecureWorkspaceUnavailable
	}
	handled := sandboxLandlockHandledAccess(int(abi))
	attribute := unix.LandlockRulesetAttr{Access_fs: handled}
	fd, _, errno := unix.Syscall6(unix.SYS_LANDLOCK_CREATE_RULESET, uintptr(unsafe.Pointer(&attribute)), unsafe.Sizeof(attribute), 0, 0, 0, 0)
	if errno != 0 {
		return errno
	}
	rulesetFD := int(fd)
	defer func() { _ = unix.Close(rulesetFD) }()
	for _, root := range sandboxLandlockRoots(request, int(abi)) {
		if root.path == "" {
			continue
		}
		if _, err := os.Stat(root.path); errors.Is(err, os.ErrNotExist) && strings.HasPrefix(root.path, "/lib") {
			continue
		} else if err != nil {
			return err
		}
		pathFD, err := unix.Open(root.path, unix.O_PATH|unix.O_CLOEXEC, 0)
		if err != nil {
			return err
		}
		rule := unix.LandlockPathBeneathAttr{Allowed_access: root.access, Parent_fd: int32(pathFD)}
		_, _, ruleErrno := unix.Syscall6(unix.SYS_LANDLOCK_ADD_RULE, uintptr(rulesetFD), unix.LANDLOCK_RULE_PATH_BENEATH,
			uintptr(unsafe.Pointer(&rule)), 0, 0, 0)
		_ = unix.Close(pathFD)
		if ruleErrno != 0 {
			return ruleErrno
		}
	}
	_, _, errno = unix.Syscall6(unix.SYS_LANDLOCK_RESTRICT_SELF, uintptr(rulesetFD), 0, 0, 0, 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}

type landlockRoot struct {
	path   string
	access uint64
}

func sandboxLandlockHandledAccess(abi int) uint64 {
	_, _, _, writable := sandboxLandlockAccess(abi)
	return uint64(unix.LANDLOCK_ACCESS_FS_EXECUTE|unix.LANDLOCK_ACCESS_FS_READ_FILE|unix.LANDLOCK_ACCESS_FS_READ_DIR) | writable
}

func sandboxLandlockAccess(abi int) (readFile, readTree, executeFile, writableTree uint64) {
	readFile = unix.LANDLOCK_ACCESS_FS_READ_FILE
	readTree = readFile | unix.LANDLOCK_ACCESS_FS_READ_DIR
	executeFile = readFile | unix.LANDLOCK_ACCESS_FS_EXECUTE
	writableTree = readTree | unix.LANDLOCK_ACCESS_FS_WRITE_FILE | unix.LANDLOCK_ACCESS_FS_REMOVE_DIR |
		unix.LANDLOCK_ACCESS_FS_REMOVE_FILE | unix.LANDLOCK_ACCESS_FS_MAKE_CHAR | unix.LANDLOCK_ACCESS_FS_MAKE_DIR |
		unix.LANDLOCK_ACCESS_FS_MAKE_REG | unix.LANDLOCK_ACCESS_FS_MAKE_SOCK | unix.LANDLOCK_ACCESS_FS_MAKE_FIFO |
		unix.LANDLOCK_ACCESS_FS_MAKE_BLOCK | unix.LANDLOCK_ACCESS_FS_MAKE_SYM
	if abi >= 2 {
		writableTree |= unix.LANDLOCK_ACCESS_FS_REFER
	}
	if abi >= 3 {
		writableTree |= unix.LANDLOCK_ACCESS_FS_TRUNCATE
	}
	return readFile, readTree, executeFile, writableTree
}

func sandboxLandlockRoots(request SandboxExecution, abi int) []landlockRoot {
	readFile, readTree, executeFile, writableTree := sandboxLandlockAccess(abi)
	roots := []landlockRoot{
		{path: request.Executable, access: executeFile},
		{path: request.InputPath, access: readFile},
		{path: "/usr", access: readTree},
		{path: "/lib", access: readTree},
		{path: "/lib64", access: readTree},
		{path: "/etc/ld.so.cache", access: readFile},
		{path: "/lib/ld-musl-x86_64.so.1", access: executeFile},
		{path: "/lib/ld-musl-aarch64.so.1", access: executeFile},
		{path: request.HomeDir, access: writableTree},
		{path: request.OutputDir, access: writableTree},
	}
	if request.Executable == "/usr/bin/clamscan" {
		roots = append(roots, landlockRoot{
			path:   "/var/lib/xirang/asset-worker-bundles/active/clamav",
			access: readTree,
		})
	}
	return roots
}

func inspectWorkspaceMount(root string) (mountInfo, error) {
	file, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return mountInfo{}, ErrSecureWorkspaceUnavailable
	}
	defer func() { _ = file.Close() }()
	clean := filepath.Clean(root)
	bestMount := ""
	var best mountInfo
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		separator := -1
		for index, field := range fields {
			if field == "-" {
				separator = index
				break
			}
		}
		if separator < 6 || separator+3 > len(fields) {
			continue
		}
		mountPoint := strings.ReplaceAll(fields[4], `\040`, " ")
		if clean != mountPoint && !strings.HasPrefix(clean, strings.TrimSuffix(mountPoint, "/")+"/") || len(mountPoint) < len(bestMount) {
			continue
		}
		options := make(map[string]bool)
		for _, group := range []string{fields[5], fields[separator+2]} {
			for _, option := range strings.Split(group, ",") {
				options[option] = true
			}
		}
		bestMount = mountPoint
		best = mountInfo{Filesystem: fields[separator+1], Options: options}
	}
	if scanner.Err() != nil || bestMount == "" {
		return mountInfo{}, ErrSecureWorkspaceUnavailable
	}
	return best, nil
}

func validateWorkspaceMount(info mountInfo) error {
	if info.Filesystem != "tmpfs" || !info.Options["rw"] || !info.Options["noexec"] || !info.Options["nosuid"] || !info.Options["nodev"] {
		return ErrSecureWorkspaceUnavailable
	}
	return nil
}

func configureToolProcess(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func signalToolProcessGroup(process *os.Process, signal syscall.Signal) error {
	if process == nil {
		return nil
	}
	err := syscall.Kill(-process.Pid, signal)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func killAndWaitToolProcessGroup(process *os.Process, maximum time.Duration) error {
	if process == nil {
		return nil
	}
	if err := signalToolProcessGroup(process, syscall.SIGKILL); err != nil {
		return err
	}
	deadline := time.Now().Add(maximum)
	for {
		err := syscall.Kill(-process.Pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		if err != nil {
			return err
		}
		if !time.Now().Before(deadline) {
			return ErrToolFailed
		}
		time.Sleep(time.Millisecond)
	}
}
