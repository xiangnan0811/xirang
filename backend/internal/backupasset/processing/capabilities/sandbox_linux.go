//go:build linux

package capabilities

import (
	"bufio"
	"errors"
	"io"
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
	MountPoint string
	Filesystem string
	Options    map[string]bool
}

type sandboxPolicy struct {
	DeniedSyscalls   map[uint32]bool
	DeniedCloneFlags uint32
	UnixSocketOnly   bool
}

const seccompArgumentZeroOffset = 16

func runtimeClosureManifestOwnerOK(info os.FileInfo) bool {
	if info == nil {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == 0 && stat.Gid == 0
}

func linuxSandboxPolicy(profile ToolArgProfile) sandboxPolicy {
	denied := []uint32{
		unix.SYS_CONNECT, unix.SYS_SENDTO, unix.SYS_SENDMSG, unix.SYS_SENDMMSG,
		unix.SYS_RECVFROM, unix.SYS_RECVMSG, unix.SYS_RECVMMSG,
		unix.SYS_MOUNT, unix.SYS_UMOUNT2, unix.SYS_PTRACE, unix.SYS_BPF,
		unix.SYS_PERF_EVENT_OPEN, unix.SYS_SETNS, unix.SYS_UNSHARE, unix.SYS_KEXEC_LOAD,
		unix.SYS_INIT_MODULE, unix.SYS_FINIT_MODULE, unix.SYS_DELETE_MODULE, unix.SYS_REBOOT,
		unix.SYS_SWAPON, unix.SYS_SWAPOFF, unix.SYS_SETSID, unix.SYS_SETPGID, unix.SYS_CLONE3,
	}
	result := sandboxPolicy{
		DeniedSyscalls: make(map[uint32]bool, len(denied)),
		DeniedCloneFlags: uint32(unix.CLONE_NEWTIME | unix.CLONE_NEWCGROUP | unix.CLONE_NEWNS |
			unix.CLONE_NEWUTS | unix.CLONE_NEWIPC | unix.CLONE_NEWUSER | unix.CLONE_NEWPID | unix.CLONE_NEWNET),
	}
	if profile == ArgsOfficePDF {
		result.UnixSocketOnly = true
		denied = append(denied, unix.SYS_SOCKETPAIR)
	} else {
		denied = append(denied, unix.SYS_SOCKET, unix.SYS_SOCKETPAIR, unix.SYS_BIND, unix.SYS_LISTEN, unix.SYS_ACCEPT, unix.SYS_ACCEPT4)
	}
	for _, number := range denied {
		result.DeniedSyscalls[number] = true
	}
	return result
}

func executeSandbox(request SandboxExecution) error {
	runtime.LockOSThread()
	if err := validateSandboxFilesystem(request); err != nil {
		return err
	}
	environment := sandboxExecEnvironment(request)
	arguments := append([]string{request.Executable}, request.Args...)
	prepared, err := prepareSandboxExecve(request.Executable, arguments, environment)
	if err != nil {
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
	if err := applySeccomp(linuxSandboxPolicy(request.Profile)); err != nil {
		return ErrSecureWorkspaceUnavailable
	}
	if err := unix.CloseRange(3, ^uint(0), 0); err != nil && !errors.Is(err, unix.ENOSYS) {
		return ErrSecureWorkspaceUnavailable
	}
	if err := applySandboxRlimits(request); err != nil {
		return ErrSecureWorkspaceUnavailable
	}
	if err := execPreparedSandbox(prepared); err != nil {
		return ErrToolFailed
	}
	return nil
}

func sandboxExecEnvironment(request SandboxExecution) []string {
	environment := []string{
		"HOME=" + request.HomeDir,
		"LANG=C.UTF-8",
		"LC_ALL=C.UTF-8",
		"TZ=UTC",
		"XIRANG_OUTPUT_DIR=" + request.OutputDir,
		"XIRANG_INPUT_MODE=" + string(request.InputMode),
	}
	if request.Profile == ArgsOfficePDF || request.Profile == ArgsClamScan {
		environment = append(environment, "TMPDIR="+request.HomeDir)
	}
	if request.InputMode == ToolInputPath {
		environment = append(environment, "XIRANG_INPUT_PATH="+request.InputPath)
	}
	return environment
}

type preparedSandboxExecve struct {
	executable  *byte
	arguments   []*byte
	environment []*byte
}

func prepareSandboxExecve(executable string, arguments, environment []string) (preparedSandboxExecve, error) {
	if executable == "" || len(arguments) == 0 || arguments[0] != executable {
		return preparedSandboxExecve{}, ErrInvalidInvocation
	}
	executablePointer, err := unix.BytePtrFromString(executable)
	if err != nil {
		return preparedSandboxExecve{}, ErrInvalidInvocation
	}
	argumentPointers := make([]*byte, len(arguments)+1)
	for index, argument := range arguments {
		argumentPointers[index], err = unix.BytePtrFromString(argument)
		if err != nil {
			return preparedSandboxExecve{}, ErrInvalidInvocation
		}
	}
	environmentPointers := make([]*byte, len(environment)+1)
	for index, value := range environment {
		environmentPointers[index], err = unix.BytePtrFromString(value)
		if err != nil {
			return preparedSandboxExecve{}, ErrInvalidInvocation
		}
	}
	return preparedSandboxExecve{
		executable: executablePointer, arguments: argumentPointers, environment: environmentPointers,
	}, nil
}

func execPreparedSandbox(value preparedSandboxExecve) error {
	if value.executable == nil || len(value.arguments) < 2 || len(value.environment) == 0 {
		return ErrInvalidInvocation
	}
	_, _, errno := unix.RawSyscall(
		unix.SYS_EXECVE,
		uintptr(unsafe.Pointer(value.executable)),
		uintptr(unsafe.Pointer(&value.arguments[0])),
		uintptr(unsafe.Pointer(&value.environment[0])),
	)
	runtime.KeepAlive(value)
	if errno != 0 {
		return errno
	}
	return nil
}

func validateSandboxFilesystem(request SandboxExecution) error {
	info, err := inspectWorkspaceMount(request.Workspace)
	if err != nil || validateWorkspaceMount(info) != nil {
		return ErrSecureWorkspaceUnavailable
	}
	if request.Profile == ArgsOfficePDF {
		tmpMount, mountErr := inspectWorkspaceMount("/tmp")
		tmpInfo, infoErr := os.Lstat("/tmp")
		if mountErr != nil || infoErr != nil || validatePrivateTmpfsMount("/tmp", tmpMount, tmpInfo) != nil {
			return ErrSecureWorkspaceUnavailable
		}
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

type sandboxRlimitValue struct {
	Resource int
	Value    uint64
}

func sandboxRlimitValues(request SandboxExecution) []sandboxRlimitValue {
	return []sandboxRlimitValue{
		{Resource: unix.RLIMIT_CORE, Value: 0},
		{Resource: unix.RLIMIT_NOFILE, Value: 64},
		{Resource: unix.RLIMIT_CPU, Value: uint64(request.CPUTime / time.Second)},
		{Resource: unix.RLIMIT_FSIZE, Value: uint64(request.MaxFileBytes)},
		{Resource: unix.RLIMIT_NPROC, Value: uint64(request.MaxProcesses)},
		{Resource: unix.RLIMIT_AS, Value: uint64(request.MaxMemoryBytes)},
	}
}

func applySandboxRlimits(request SandboxExecution) error {
	limits := sandboxRlimitValues(request)
	for _, limit := range limits {
		if err := unix.Setrlimit(limit.Resource, &unix.Rlimit{Cur: limit.Value, Max: limit.Value}); err != nil {
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
	if policy.UnixSocketOnly {
		filters = appendUnixSocketDomainFilter(filters, uint32(unix.SYS_SOCKET))
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
	if policy.DeniedCloneFlags != 0 {
		filters = append(filters,
			unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jf: 4, K: uint32(unix.SYS_CLONE)},
			unix.SockFilter{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: seccompArgumentZeroOffset},
			unix.SockFilter{Code: unix.BPF_ALU | unix.BPF_AND | unix.BPF_K, K: policy.DeniedCloneFlags},
			unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jt: 1, K: 0},
			unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM)},
		)
	}
	filters = append(filters, unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ALLOW})
	program := unix.SockFprog{Len: uint16(len(filters)), Filter: &filters[0]}
	return unix.Prctl(unix.PR_SET_SECCOMP, unix.SECCOMP_MODE_FILTER, uintptr(unsafe.Pointer(&program)), 0, 0)
}

func appendUnixSocketDomainFilter(filters []unix.SockFilter, syscallNumber uint32) []unix.SockFilter {
	return append(filters,
		unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jf: 4, K: syscallNumber},
		unix.SockFilter{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: seccompArgumentZeroOffset},
		unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jt: 1, K: uint32(unix.AF_UNIX)},
		unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM)},
		unix.SockFilter{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 0},
	)
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
	if request.Profile == ArgsOfficePDF {
		roots = append(roots,
			landlockRoot{
				path: "/tmp",
				access: uint64(unix.LANDLOCK_ACCESS_FS_READ_DIR | unix.LANDLOCK_ACCESS_FS_MAKE_SOCK |
					unix.LANDLOCK_ACCESS_FS_REMOVE_FILE),
			},
			landlockRoot{path: "/dev/urandom", access: readFile},
			landlockRoot{path: "/etc/fonts", access: readTree},
		)
	}
	return roots
}

func inspectWorkspaceMount(root string) (mountInfo, error) {
	file, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return mountInfo{}, ErrSecureWorkspaceUnavailable
	}
	defer func() { _ = file.Close() }()
	return inspectWorkspaceMountReader(root, file)
}

func inspectWorkspaceMountReader(root string, reader io.Reader) (mountInfo, error) {
	clean := filepath.Clean(root)
	bestMount := ""
	bestMatches := 0
	var best mountInfo
	scanner := bufio.NewScanner(reader)
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
		if (clean != mountPoint && !strings.HasPrefix(clean, strings.TrimSuffix(mountPoint, "/")+"/")) || len(mountPoint) < len(bestMount) {
			continue
		}
		if mountPoint == bestMount {
			bestMatches++
			continue
		}
		options := make(map[string]bool)
		for _, group := range []string{fields[5], fields[separator+2]} {
			for _, option := range strings.Split(group, ",") {
				options[option] = true
			}
		}
		bestMount = mountPoint
		bestMatches = 1
		best = mountInfo{MountPoint: mountPoint, Filesystem: fields[separator+1], Options: options}
	}
	if scanner.Err() != nil || bestMount == "" || bestMatches != 1 {
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

func validatePrivateTmpfsMount(path string, mount mountInfo, info os.FileInfo) error {
	if filepath.Clean(path) != path || mount.MountPoint != path || validateWorkspaceMount(mount) != nil ||
		info == nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		return ErrSecureWorkspaceUnavailable
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() || int(stat.Gid) != os.Getegid() {
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
