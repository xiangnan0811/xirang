//go:build linux

package capabilities

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestLinuxWorkspaceContractRequiresPrivateNoexecTmpfs(t *testing.T) {
	info := mountInfo{Filesystem: "tmpfs", Options: map[string]bool{"rw": true, "noexec": true, "nosuid": true, "nodev": true}}
	if err := validateWorkspaceMount(info); err != nil {
		t.Fatalf("valid mount rejected: %v", err)
	}
	for _, missing := range []string{"noexec", "nosuid", "nodev"} {
		invalid := info
		invalid.Options = map[string]bool{"rw": true, "noexec": true, "nosuid": true, "nodev": true}
		delete(invalid.Options, missing)
		if err := validateWorkspaceMount(invalid); !errors.Is(err, ErrSecureWorkspaceUnavailable) {
			t.Fatalf("missing %s error=%v", missing, err)
		}
	}
	invalid := info
	invalid.Filesystem = "ext4"
	if err := validateWorkspaceMount(invalid); !errors.Is(err, ErrSecureWorkspaceUnavailable) {
		t.Fatalf("ordinary disk error=%v", err)
	}
}

func TestLinuxSandboxExecutionContractClosesPathsAndNetworkSyscalls(t *testing.T) {
	workspace := filepath.Join(string(os.PathSeparator), "run", "xirang", "asset-jobs", "job-opaque")
	request := SandboxExecution{
		Executable: "/usr/bin/vips",
		Args: []string{
			"thumbnail", filepath.Join(workspace, "input.bin"), filepath.Join(workspace, "output", "thumbnail.png"), "320",
		},
		Workspace: workspace, InputMode: ToolInputPath, InputPath: filepath.Join(workspace, "input.bin"),
		OutputDir: filepath.Join(workspace, "output"), HomeDir: filepath.Join(workspace, "home"), MaxProcesses: 16,
	}
	if err := validateSandboxExecution(request); err != nil {
		t.Fatalf("valid sandbox execution rejected: %v", err)
	}
	for index, mutate := range []func(*SandboxExecution){
		func(value *SandboxExecution) { value.Executable = "/bin/sh" },
		func(value *SandboxExecution) { value.InputPath = "/etc/passwd" },
		func(value *SandboxExecution) { value.OutputDir = "/tmp/output" },
		func(value *SandboxExecution) { value.Args = append(value.Args, "https://example.invalid") },
	} {
		invalid := request
		invalid.Args = append([]string(nil), request.Args...)
		mutate(&invalid)
		if err := validateSandboxExecution(invalid); !errors.Is(err, ErrInvalidInvocation) {
			t.Fatalf("unsafe execution %d error=%v", index, err)
		}
	}
	policy := linuxSandboxPolicy()
	for _, syscallNumber := range []uint32{
		unix.SYS_SOCKET, unix.SYS_SOCKETPAIR, unix.SYS_CONNECT, unix.SYS_BIND, unix.SYS_LISTEN,
		unix.SYS_ACCEPT, unix.SYS_ACCEPT4, unix.SYS_SENDTO, unix.SYS_SENDMSG, unix.SYS_RECVFROM,
		unix.SYS_RECVMSG, unix.SYS_MOUNT, unix.SYS_UMOUNT2, unix.SYS_PTRACE, unix.SYS_BPF,
		unix.SYS_PERF_EVENT_OPEN, unix.SYS_SETNS, unix.SYS_UNSHARE, unix.SYS_SETSID, unix.SYS_SETPGID,
	} {
		if !policy.DeniedSyscalls[syscallNumber] {
			t.Fatalf("sandbox policy permits syscall %d", syscallNumber)
		}
	}
}

func TestLinuxLandlockExecutesOnlySelectedBinaryAndReadsFixedBundle(t *testing.T) {
	workspace := filepath.Join(string(os.PathSeparator), "run", "xirang", "asset-jobs", "job-opaque")
	request := SandboxExecution{
		Executable: "/usr/bin/clamscan",
		Args:       []string{"--no-summary", filepath.Join(workspace, "input.bin")},
		Workspace:  workspace, InputMode: ToolInputPath, InputPath: filepath.Join(workspace, "input.bin"),
		OutputDir: filepath.Join(workspace, "output"), HomeDir: filepath.Join(workspace, "home"), MaxProcesses: 16,
	}
	roots := sandboxLandlockRoots(request, 3)
	access := make(map[string]uint64, len(roots))
	for _, root := range roots {
		access[root.path] = root.access
	}
	if access[request.Executable]&unix.LANDLOCK_ACCESS_FS_EXECUTE == 0 {
		t.Fatal("selected tool binary is not executable")
	}
	for _, root := range []string{"/usr", "/lib", "/lib64", "/etc/ld.so.cache", request.InputPath} {
		if access[root]&unix.LANDLOCK_ACCESS_FS_EXECUTE != 0 {
			t.Fatalf("read-only Landlock root %q is executable", root)
		}
	}
	const bundleRoot = "/var/lib/xirang/asset-worker-bundles/active/clamav"
	if access[bundleRoot]&unix.LANDLOCK_ACCESS_FS_READ_FILE == 0 || access[bundleRoot]&unix.LANDLOCK_ACCESS_FS_EXECUTE != 0 {
		t.Fatalf("ClamAV bundle access=%#x", access[bundleRoot])
	}
}
