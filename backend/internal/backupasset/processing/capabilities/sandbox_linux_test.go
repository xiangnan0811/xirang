//go:build linux

package capabilities

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"xirang/backend/internal/backupasset/processing/capabilityspec"

	"golang.org/x/sys/unix"
)

func TestRuntimeClosureAttestationRejectsTamperedOrWrongArchitectureEvidence(t *testing.T) {
	runtimeRoot := t.TempDir()
	t.Cleanup(func() {
		_ = filepath.Walk(runtimeRoot, func(path string, info os.FileInfo, err error) error {
			if err == nil {
				_ = os.Chmod(path, 0o700)
			}
			return nil
		})
	})
	runtimeFile := filepath.Join(runtimeRoot, "asset-worker")
	if err := os.WriteFile(runtimeFile, []byte("worker-v1"), 0o555); err != nil {
		t.Fatal(err)
	}
	runtimeLink := filepath.Join(runtimeRoot, "asset-worker-link")
	if err := os.Symlink(filepath.Base(runtimeFile), runtimeLink); err != nil {
		t.Fatal(err)
	}
	manifest := runtimeClosureManifest{
		SchemaVersion: 1,
		Platform:      "linux/amd64",
		Files: []runtimeClosureFile{
			{Kind: "regular", Path: runtimeFile, Mode: 0o555, Size: int64(len("worker-v1")), SHA256: testSHA256Hex([]byte("worker-v1"))},
			{Kind: "symlink", Path: runtimeLink, Mode: 0o777, Size: int64(len(filepath.Base(runtimeFile))), SHA256: testSHA256Hex([]byte(filepath.Base(runtimeFile)))},
		},
	}
	manifestPayload, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifestDigest := testSHA256Hex(manifestPayload)
	attestations := runtimeClosureAttestations{
		SchemaVersion: 1,
		Attestations: []runtimeClosureAttestation{
			{Platform: "linux/amd64", RuntimeManifestSHA256: manifestDigest},
			{Platform: "linux/arm64", RuntimeManifestSHA256: strings.Repeat("b", 64)},
		},
	}
	attestationPayload, err := json.Marshal(attestations)
	if err != nil {
		t.Fatal(err)
	}
	attestationDigest := testSHA256Hex(attestationPayload)
	if _, err := verifyBoundRuntimeClosureAttestationPayloads(
		manifestPayload, attestationPayload, attestationDigest, "linux", "amd64",
	); err != nil {
		t.Fatalf("exact signed attestation payload rejected: %v", err)
	}
	replacement := attestations
	replacement.Attestations = append([]runtimeClosureAttestation(nil), attestations.Attestations...)
	replacement.Attestations[1].RuntimeManifestSHA256 = strings.Repeat("c", 64)
	replacementPayload, err := json.Marshal(replacement)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifyBoundRuntimeClosureAttestationPayloads(
		manifestPayload, replacementPayload, attestationDigest, "linux", "amd64",
	); err == nil {
		t.Fatal("locally valid replacement attestation escaped the signed payload digest binding")
	}

	got, err := verifyRuntimeClosureAttestationPayloads(manifestPayload, attestationPayload, "linux", "amd64")
	if err != nil || got != manifestDigest {
		t.Fatalf("verified runtime closure digest=%q err=%v, want %q", got, err, manifestDigest)
	}
	if err := os.Remove(runtimeLink); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("unattested-target", runtimeLink); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyRuntimeClosureAttestationPayloads(manifestPayload, attestationPayload, "linux", "amd64"); err == nil {
		t.Fatal("runtime symlink target drift accepted")
	}
	if err := os.Remove(runtimeLink); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Base(runtimeFile), runtimeLink); err != nil {
		t.Fatal(err)
	}

	if err := os.Chmod(runtimeFile, 0o444); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyRuntimeClosureAttestationPayloads(manifestPayload, attestationPayload, "linux", "amd64"); err == nil {
		t.Fatal("runtime file mode drift accepted")
	}
	if err := os.Chmod(runtimeFile, 0o555); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(runtimeFile, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runtimeFile, []byte("worker-v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(runtimeFile, 0o555); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyRuntimeClosureAttestationPayloads(manifestPayload, attestationPayload, "linux", "amd64"); err == nil {
		t.Fatal("runtime file content drift accepted")
	}

	if _, err := verifyRuntimeClosureAttestationPayloads(manifestPayload, attestationPayload, "linux", "arm64"); err == nil {
		t.Fatal("runtime manifest for the wrong architecture accepted")
	}
	missingArchitecture := attestations
	missingArchitecture.Attestations = missingArchitecture.Attestations[:1]
	missingPayload, err := json.Marshal(missingArchitecture)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifyRuntimeClosureAttestationPayloads(manifestPayload, missingPayload, "linux", "amd64"); err == nil {
		t.Fatal("incomplete cross-architecture attestation set accepted")
	}
	ready := evaluateToolchainPreflight(context.Background(), productionToolchainInventory(), matchingToolchainInspection(productionToolchainInventory()))
	ready = gateCapabilitiesByRuntimeClosure(ready, ErrInvalidInvocation)
	for capability, available := range ready {
		if available {
			t.Fatalf("wrong-architecture runtime evidence left %s advertised", capability)
		}
	}
}

func TestBuildAndWriteRuntimeClosureManifestCoversRegularFilesAndSymlinkTargets(t *testing.T) {
	root := t.TempDir()
	regularPath := filepath.Join(root, "bin", "tool")
	if err := os.MkdirAll(filepath.Dir(regularPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(regularPath, []byte("tool-v1"), 0o555); err != nil {
		t.Fatal(err)
	}
	symlinkPath := filepath.Join(root, "bin", "tool-link")
	if err := os.Symlink("tool", symlinkPath); err != nil {
		t.Fatal(err)
	}
	excludedRoot := filepath.Join(root, "runtime")
	if err := os.Mkdir(excludedRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(excludedRoot, "socket-state"), []byte("mutable"), 0o600); err != nil {
		t.Fatal(err)
	}

	payload, err := buildRuntimeClosureManifest("linux/amd64", []string{root}, []string{excludedRoot})
	if err != nil {
		t.Fatal(err)
	}
	var manifest runtimeClosureManifest
	if err := decodeCanonicalToolchainJSON(payload, maximumRuntimeClosureBytes, &manifest); err != nil {
		t.Fatalf("runtime closure is not canonical: %v", err)
	}
	if manifest.SchemaVersion != 1 || manifest.Platform != "linux/amd64" || len(manifest.Files) != 2 {
		t.Fatalf("runtime closure=%+v", manifest)
	}
	want := []runtimeClosureFile{
		{Kind: "regular", Path: regularPath, Mode: 0o555, Size: int64(len("tool-v1")), SHA256: testSHA256Hex([]byte("tool-v1"))},
		{Kind: "symlink", Path: symlinkPath, Mode: 0o777, Size: int64(len("tool")), SHA256: testSHA256Hex([]byte("tool"))},
	}
	for index := range want {
		if manifest.Files[index] != want[index] {
			t.Fatalf("runtime closure file %d=%+v, want %+v", index, manifest.Files[index], want[index])
		}
	}

	output := filepath.Join(root, "runtime-closure.v1.json")
	if err := writeRuntimeClosureManifest(output, "linux/amd64", []string{root}, []string{excludedRoot, output}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(output)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o444 {
		t.Fatalf("runtime closure output info=%v err=%v", info, err)
	}
	written, err := os.ReadFile(output)
	if err != nil || !bytes.Equal(written, payload) {
		t.Fatalf("runtime closure output changed: bytes=%d err=%v", len(written), err)
	}
}

func TestProductionRuntimeClosureExcludesOnlyReviewedSystemMetadata(t *testing.T) {
	want := []string{
		ProductionRuntimeClosureManifestPath,
		"/dev", "/proc", "/run", "/sys", "/tmp", "/var/tmp",
		"/etc/hostname", "/etc/hosts", "/etc/resolv.conf", "/etc/shadow",
		"/var/lib/xirang/asset-worker-bundles", "/var/lib/xirang/asset-worker-inbox",
		"/var/lib/xirang-asset-runtime",
	}
	got := productionRuntimeClosureExcludedPaths()
	if len(got) != len(want) {
		t.Fatalf("production runtime exclusions=%v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("production runtime exclusion %d=%q, want %q", index, got[index], want[index])
		}
	}
	for _, forbidden := range []string{"/etc/crontabs/root", "/etc/shadow-", "/lib/apk/db/lock"} {
		if runtimeClosurePathExcluded(forbidden, got) {
			t.Fatalf("removable runtime file %q was hidden from the signed closure", forbidden)
		}
	}
}

func testSHA256Hex(payload []byte) string {
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func TestProductionToolchainInventoryFingerprintAndPartialPreflight(t *testing.T) {
	inventory := productionToolchainInventory()
	fingerprint, err := toolchainFingerprint(inventory)
	if err != nil || len(fingerprint) != 64 {
		t.Fatalf("toolchain fingerprint=%q err=%v", fingerprint, err)
	}
	reordered := cloneToolchainInventory(inventory)
	for left, right := 0, len(reordered.Components)-1; left < right; left, right = left+1, right-1 {
		reordered.Components[left], reordered.Components[right] = reordered.Components[right], reordered.Components[left]
	}
	reorderedFingerprint, err := toolchainFingerprint(reordered)
	if err != nil || reorderedFingerprint != fingerprint {
		t.Fatalf("canonical reordered fingerprint=%q err=%v, want %q", reorderedFingerprint, err, fingerprint)
	}
	mutated := cloneToolchainInventory(inventory)
	mutated.Components[0].Revision += "-changed"
	mutatedFingerprint, err := toolchainFingerprint(mutated)
	if err != nil || mutatedFingerprint == fingerprint {
		t.Fatalf("changed inventory fingerprint=%q err=%v, base %q", mutatedFingerprint, err, fingerprint)
	}

	inspection := matchingToolchainInspection(inventory)
	ready := evaluateToolchainPreflight(context.Background(), inventory, inspection)
	if len(ready) != 10 {
		t.Fatalf("matching toolchain ready profiles=%v", ready)
	}
	for capability, available := range ready {
		if !available {
			t.Fatalf("matching toolchain removed %s: %v", capability, ready)
		}
	}

	inspection.Packages["tesseract-ocr"] = "0-mismatch"
	delete(inspection.Assets, "/usr/share/fonts/noto/NotoSans-Regular.ttf")
	inspection.Probes["ffmpeg-codecs"] = "aac mp3 vorbis"
	ready = evaluateToolchainPreflight(context.Background(), inventory, inspection)
	for _, capability := range []string{
		capabilityspec.CapabilityImageOCR,
		capabilityspec.CapabilityDocumentConvert,
		capabilityspec.CapabilityMediaProbe,
		capabilityspec.CapabilityMediaTranscode,
	} {
		if ready[capability] {
			t.Fatalf("mismatched component left %s ready: %v", capability, ready)
		}
	}
	for _, capability := range []string{
		capabilityspec.CapabilityImageThumbnail,
		capabilityspec.CapabilityTextExtract,
		capabilityspec.CapabilityMalwareScan,
		capabilityspec.CapabilityArchiveInspect,
		capabilityspec.CapabilityArchiveExtractEntry,
		capabilityspec.CapabilitySecretClassify,
	} {
		if !ready[capability] {
			t.Fatalf("unaffected capability %s was removed: %v", capability, ready)
		}
	}
}

func matchingToolchainInspection(inventory toolchainInventory) toolchainInspection {
	inspection := toolchainInspection{
		Packages: make(map[string]string),
		Assets:   make(map[string]bool),
		Probes:   make(map[string]string),
	}
	for _, packageValue := range inventory.Packages {
		inspection.Packages[packageValue.Name] = packageValue.Version
	}
	for _, component := range inventory.Components {
		for _, asset := range component.Assets {
			inspection.Assets[asset.Path] = true
		}
		for _, probe := range component.Probes {
			inspection.Probes[probe.ID] = strings.Join(probe.Required, " ")
		}
	}
	return inspection
}

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

type sandboxTestFileInfo struct {
	os.FileInfo
	stat syscall.Stat_t
}

func (info sandboxTestFileInfo) Sys() any {
	return &info.stat
}

type sandboxModeFileInfo struct {
	os.FileInfo
	mode os.FileMode
}

func (info sandboxModeFileInfo) Mode() os.FileMode {
	return info.mode
}

func TestLinuxOfficePrivateTmpfsContract(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	directoryInfo, err := os.Lstat(directory)
	if err != nil {
		t.Fatal(err)
	}
	valid := mountInfo{
		MountPoint: "/tmp",
		Filesystem: "tmpfs",
		Options:    map[string]bool{"rw": true, "noexec": true, "nosuid": true, "nodev": true},
	}
	if err := validatePrivateTmpfsMount("/tmp", valid, directoryInfo); err != nil {
		t.Fatalf("valid private tmpfs rejected: %v", err)
	}

	link := filepath.Join(t.TempDir(), "tmp-link")
	if err := os.Symlink(directory, link); err != nil {
		t.Fatal(err)
	}
	linkInfo, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	stat := *(directoryInfo.Sys().(*syscall.Stat_t))
	tests := []struct {
		name  string
		mount mountInfo
		info  os.FileInfo
	}{
		{name: "covering parent mount", mount: mountInfo{MountPoint: "/", Filesystem: valid.Filesystem, Options: valid.Options}, info: directoryInfo},
		{name: "ordinary disk", mount: mountInfo{MountPoint: valid.MountPoint, Filesystem: "overlay", Options: valid.Options}, info: directoryInfo},
		{name: "symlink", mount: valid, info: linkInfo},
	}
	for _, missing := range []string{"rw", "noexec", "nosuid", "nodev"} {
		options := map[string]bool{"rw": true, "noexec": true, "nosuid": true, "nodev": true}
		delete(options, missing)
		tests = append(tests, struct {
			name  string
			mount mountInfo
			info  os.FileInfo
		}{name: "missing " + missing, mount: mountInfo{MountPoint: valid.MountPoint, Filesystem: valid.Filesystem, Options: options}, info: directoryInfo})
	}
	wrongMode := sandboxTestFileInfo{FileInfo: sandboxModeFileInfo{FileInfo: directoryInfo, mode: os.ModeDir | 0o755}, stat: stat}
	tests = append(tests, struct {
		name  string
		mount mountInfo
		info  os.FileInfo
	}{name: "wrong mode", mount: valid, info: wrongMode})
	wrongUID := sandboxTestFileInfo{FileInfo: directoryInfo, stat: stat}
	wrongUID.stat.Uid++
	tests = append(tests, struct {
		name  string
		mount mountInfo
		info  os.FileInfo
	}{name: "wrong uid", mount: valid, info: wrongUID})
	wrongGID := sandboxTestFileInfo{FileInfo: directoryInfo, stat: stat}
	wrongGID.stat.Gid++
	tests = append(tests, struct {
		name  string
		mount mountInfo
		info  os.FileInfo
	}{name: "wrong gid", mount: valid, info: wrongGID})

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validatePrivateTmpfsMount("/tmp", test.mount, test.info); !errors.Is(err, ErrSecureWorkspaceUnavailable) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestLinuxInspectWorkspaceMountRejectsDuplicateExactMounts(t *testing.T) {
	const rootMount = "20 1 0:1 / / rw,relatime - overlay overlay rw\n"
	const tmpMount = "21 20 0:2 / /tmp rw,nosuid,nodev,noexec - tmpfs tmpfs rw,nosuid,nodev,noexec\n"

	mount, err := inspectWorkspaceMountReader("/tmp", strings.NewReader(rootMount+tmpMount))
	if err != nil {
		t.Fatalf("single exact mount rejected: %v", err)
	}
	if mount.MountPoint != "/tmp" || mount.Filesystem != "tmpfs" {
		t.Fatalf("mount=%+v", mount)
	}

	if _, err := inspectWorkspaceMountReader("/tmp", strings.NewReader(rootMount+tmpMount+tmpMount)); !errors.Is(err, ErrSecureWorkspaceUnavailable) {
		t.Fatalf("duplicate exact mount error=%v", err)
	}
}

func TestLinuxSandboxExecutionContractClosesPathsAndNetworkSyscalls(t *testing.T) {
	workspace := filepath.Join(string(os.PathSeparator), "run", "xirang", "asset-jobs", "job-opaque")
	request := SandboxExecution{
		Executable: "/usr/bin/vips",
		Profile:    ArgsVipsThumbnail,
		Args: []string{
			"thumbnail", filepath.Join(workspace, "input.bin"), filepath.Join(workspace, "output", "thumbnail.png"), "320",
		},
		Workspace: workspace, InputMode: ToolInputPath, InputPath: filepath.Join(workspace, "input.bin"),
		OutputDir: filepath.Join(workspace, "output"), HomeDir: filepath.Join(workspace, "home"),
		CPUTime: 90 * time.Second, MaxMemoryBytes: 1 << 30, MaxFileBytes: 8 << 20, MaxProcesses: 16,
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
	policy := linuxSandboxPolicy(request.Profile)
	for _, syscallNumber := range []uint32{
		unix.SYS_SOCKET, unix.SYS_SOCKETPAIR, unix.SYS_CONNECT, unix.SYS_BIND, unix.SYS_LISTEN,
		unix.SYS_ACCEPT, unix.SYS_ACCEPT4, unix.SYS_SENDTO, unix.SYS_SENDMSG, unix.SYS_RECVFROM,
		unix.SYS_RECVMSG, unix.SYS_MOUNT, unix.SYS_UMOUNT2, unix.SYS_PTRACE, unix.SYS_BPF,
		unix.SYS_PERF_EVENT_OPEN, unix.SYS_SETNS, unix.SYS_UNSHARE, unix.SYS_CLONE3,
		unix.SYS_SETSID, unix.SYS_SETPGID,
	} {
		if !policy.DeniedSyscalls[syscallNumber] {
			t.Fatalf("sandbox policy permits syscall %d", syscallNumber)
		}
	}
	if policy.DeniedSyscalls[unix.SYS_CLONE] {
		t.Fatal("sandbox must preserve ordinary process/thread clone under RLIMIT_NPROC")
	}
	for _, flag := range []uint32{
		unix.CLONE_NEWTIME, unix.CLONE_NEWCGROUP, unix.CLONE_NEWNS, unix.CLONE_NEWUTS,
		unix.CLONE_NEWIPC, unix.CLONE_NEWUSER, unix.CLONE_NEWPID, unix.CLONE_NEWNET,
	} {
		if policy.DeniedCloneFlags&flag == 0 {
			t.Fatalf("sandbox permits clone namespace flag %#x", flag)
		}
	}

	values := sandboxRlimitValues(request)
	want := map[int]uint64{
		unix.RLIMIT_CORE:   0,
		unix.RLIMIT_NOFILE: 64,
		unix.RLIMIT_CPU:    90,
		unix.RLIMIT_AS:     1 << 30,
		unix.RLIMIT_FSIZE:  8 << 20,
		unix.RLIMIT_NPROC:  16,
	}
	if len(values) != len(want) {
		t.Fatalf("rlimit count=%d values=%+v", len(values), values)
	}
	for _, value := range values {
		if want[value.Resource] != value.Value {
			t.Fatalf("rlimit resource=%d value=%d want=%d", value.Resource, value.Value, want[value.Resource])
		}
		delete(want, value.Resource)
	}
	if len(want) != 0 {
		t.Fatalf("missing rlimits=%+v", want)
	}
}

func TestLinuxSeccompBlocksNamespaceCloneAndClone3(t *testing.T) {
	if os.Getenv("XIRANG_SECCOMP_NAMESPACE_TEST") == "1" {
		runtime.LockOSThread()
		if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
			os.Exit(10)
		}
		if err := applySeccomp(linuxSandboxPolicy(ArgsVipsThumbnail)); err != nil {
			os.Exit(11)
		}
		_, _, errno := unix.RawSyscall(unix.SYS_CLONE, uintptr(unix.CLONE_NEWNS|unix.SIGCHLD), 0, 0)
		if !errors.Is(errno, unix.EPERM) {
			os.Exit(12)
		}
		_, _, errno = unix.RawSyscall(unix.SYS_CLONE3, 0, 0, 0)
		if !errors.Is(errno, unix.EPERM) {
			os.Exit(13)
		}
		_, _, errno = unix.RawSyscall(unix.SYS_CLONE, uintptr(unix.CLONE_THREAD), 0, 0)
		if errors.Is(errno, unix.EPERM) || !errors.Is(errno, unix.EINVAL) {
			os.Exit(14)
		}
		os.Exit(0)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestLinuxSeccompBlocksNamespaceCloneAndClone3$")
	command.Env = append(os.Environ(), "XIRANG_SECCOMP_NAMESPACE_TEST=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("seccomp namespace subprocess: %v output=%q", err, output)
	}
}

func TestLinuxSandboxMemoryLimitAllowsHelperToInstallSeccomp(t *testing.T) {
	if os.Getenv("XIRANG_SANDBOX_MEMORY_LIMIT_TEST") == "1" {
		runtime.LockOSThread()
		prepared, err := prepareSandboxExecve("/bin/true", []string{"/bin/true"}, []string{"LANG=C.UTF-8"})
		if err != nil {
			os.Exit(9)
		}
		request := SandboxExecution{
			CPUTime: time.Minute, MaxMemoryBytes: 1 << 30,
			MaxFileBytes: 1 << 20, MaxProcesses: 4,
		}
		if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
			os.Exit(8)
		}
		if err := applySeccomp(linuxSandboxPolicy(ArgsVipsThumbnail)); err != nil {
			os.Exit(10)
		}
		if err := applySandboxRlimits(request); err != nil {
			os.Exit(11)
		}
		if err := execPreparedSandbox(prepared); err != nil {
			os.Exit(12)
		}
		os.Exit(13)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestLinuxSandboxMemoryLimitAllowsHelperToInstallSeccomp$")
	command.Env = append(os.Environ(), "XIRANG_SANDBOX_MEMORY_LIMIT_TEST=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("sandbox helper could not finish setup under its memory limit: %v output=%q", err, output)
	}
}

func TestLinuxLandlockExecutesOnlySelectedBinaryAndReadsFixedBundle(t *testing.T) {
	workspace := filepath.Join(string(os.PathSeparator), "run", "xirang", "asset-jobs", "job-opaque")
	request := SandboxExecution{
		Executable: "/usr/bin/clamscan",
		Profile:    ArgsClamScan,
		Args:       []string{"--no-summary", filepath.Join(workspace, "input.bin")},
		Workspace:  workspace, InputMode: ToolInputPath, InputPath: filepath.Join(workspace, "input.bin"),
		OutputDir: filepath.Join(workspace, "output"), HomeDir: filepath.Join(workspace, "home"),
		CPUTime: 10 * time.Minute, MaxMemoryBytes: 2 << 30, MaxFileBytes: 64 << 10, MaxProcesses: 16,
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

func TestLinuxSandboxEnvironmentScopesToolTemporaryFilesToPrivateHome(t *testing.T) {
	officeHome := "/run/xirang/asset-jobs/job-office/home"
	office := SandboxExecution{
		Profile: ArgsOfficePDF, HomeDir: officeHome,
		OutputDir: "/run/xirang/asset-jobs/job-office/output", InputMode: ToolInputPath,
		InputPath: "/run/xirang/asset-jobs/job-office/input.bin",
	}
	officeEnvironment := sandboxExecEnvironment(office)
	wantTMPDIR := "TMPDIR=" + officeHome
	tmpdirCount := 0
	for _, value := range officeEnvironment {
		if value == wantTMPDIR {
			tmpdirCount++
		}
		if strings.HasPrefix(value, "TMP=") || strings.HasPrefix(value, "TEMP=") || value == "TMPDIR=/tmp" {
			t.Fatalf("Office inherited unsafe temporary environment %q: %v", value, officeEnvironment)
		}
	}
	if tmpdirCount != 1 {
		t.Fatalf("Office TMPDIR count=%d environment=%v", tmpdirCount, officeEnvironment)
	}

	clam := office
	clam.Profile = ArgsClamScan
	clam.HomeDir = "/run/xirang/asset-jobs/job-clam/home"
	clam.OutputDir = "/run/xirang/asset-jobs/job-clam/output"
	clam.InputPath = "/run/xirang/asset-jobs/job-clam/input.bin"
	clamEnvironment := sandboxExecEnvironment(clam)
	wantTMPDIR = "TMPDIR=" + clam.HomeDir
	tmpdirCount = 0
	for _, value := range clamEnvironment {
		if value == wantTMPDIR {
			tmpdirCount++
		}
		if strings.HasPrefix(value, "TMP=") || strings.HasPrefix(value, "TEMP=") || value == "TMPDIR=/tmp" {
			t.Fatalf("ClamAV inherited unsafe temporary environment %q: %v", value, clamEnvironment)
		}
	}
	if tmpdirCount != 1 {
		t.Fatalf("ClamAV TMPDIR count=%d environment=%v", tmpdirCount, clamEnvironment)
	}

	nonOffice := office
	nonOffice.Profile = ArgsVipsThumbnail
	for _, value := range sandboxExecEnvironment(nonOffice) {
		if strings.HasPrefix(value, "TMPDIR=") || strings.HasPrefix(value, "TMP=") || strings.HasPrefix(value, "TEMP=") {
			t.Fatalf("non-Office environment gained temporary override %q", value)
		}
	}
}

func TestLinuxOfficePolicyAndLandlockContract(t *testing.T) {
	policy := linuxSandboxPolicy(ArgsOfficePDF)
	if !policy.UnixSocketOnly {
		t.Fatal("Office socket creation must be restricted to AF_UNIX")
	}
	for _, syscallNumber := range []uint32{
		unix.SYS_SOCKET, unix.SYS_BIND, unix.SYS_LISTEN, unix.SYS_ACCEPT, unix.SYS_ACCEPT4,
	} {
		if policy.DeniedSyscalls[syscallNumber] {
			t.Fatalf("Office policy denies required local socket syscall %d", syscallNumber)
		}
	}
	for _, syscallNumber := range []uint32{
		unix.SYS_SOCKETPAIR, unix.SYS_CONNECT, unix.SYS_SENDTO, unix.SYS_SENDMSG, unix.SYS_SENDMMSG,
		unix.SYS_RECVFROM, unix.SYS_RECVMSG, unix.SYS_RECVMMSG,
	} {
		if !policy.DeniedSyscalls[syscallNumber] {
			t.Fatalf("Office policy permits network-capable syscall %d", syscallNumber)
		}
	}

	request := SandboxExecution{Profile: ArgsOfficePDF}
	var tmpAccess, urandomAccess, fontsAccess uint64
	tmpRoots := 0
	urandomRoots := 0
	fontsRoots := 0
	for _, root := range sandboxLandlockRoots(request, 3) {
		switch root.path {
		case "/tmp":
			tmpRoots++
			tmpAccess = root.access
		case "/dev/urandom":
			urandomRoots++
			urandomAccess = root.access
		case "/etc/fonts":
			fontsRoots++
			fontsAccess = root.access
		}
	}
	if tmpRoots != 1 {
		t.Fatalf("Office /tmp Landlock root count=%d", tmpRoots)
	}
	want := uint64(unix.LANDLOCK_ACCESS_FS_READ_DIR | unix.LANDLOCK_ACCESS_FS_MAKE_SOCK | unix.LANDLOCK_ACCESS_FS_REMOVE_FILE)
	if tmpAccess != want {
		t.Fatalf("Office /tmp Landlock access=%#x want=%#x", tmpAccess, want)
	}
	for _, forbidden := range []uint64{
		unix.LANDLOCK_ACCESS_FS_EXECUTE,
		unix.LANDLOCK_ACCESS_FS_READ_FILE,
		unix.LANDLOCK_ACCESS_FS_WRITE_FILE,
		unix.LANDLOCK_ACCESS_FS_MAKE_DIR,
		unix.LANDLOCK_ACCESS_FS_MAKE_REG,
	} {
		if tmpAccess&forbidden != 0 {
			t.Fatalf("Office /tmp Landlock permits forbidden access %#x", forbidden)
		}
	}
	if urandomRoots != 1 {
		t.Fatalf("Office /dev/urandom Landlock root count=%d", urandomRoots)
	}
	if urandomAccess != unix.LANDLOCK_ACCESS_FS_READ_FILE {
		t.Fatalf("Office /dev/urandom Landlock access=%#x want=%#x", urandomAccess, uint64(unix.LANDLOCK_ACCESS_FS_READ_FILE))
	}
	if fontsRoots != 1 {
		t.Fatalf("Office /etc/fonts Landlock root count=%d", fontsRoots)
	}
	wantFonts := uint64(unix.LANDLOCK_ACCESS_FS_READ_DIR | unix.LANDLOCK_ACCESS_FS_READ_FILE)
	if fontsAccess != wantFonts {
		t.Fatalf("Office /etc/fonts Landlock access=%#x want=%#x", fontsAccess, wantFonts)
	}
}

func TestLinuxOfficeSeccompAllowsAFUNIXServerButDeniesOtherSocketOperations(t *testing.T) {
	if os.Getenv("XIRANG_OFFICE_SECCOMP_TEST") == "1" {
		runtime.LockOSThread()
		if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
			os.Exit(10)
		}
		if err := applySeccomp(linuxSandboxPolicy(ArgsOfficePDF)); err != nil {
			os.Exit(11)
		}
		fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, 0)
		if err != nil {
			os.Exit(12)
		}
		if err := unix.Bind(fd, &unix.SockaddrUnix{Name: os.Getenv("XIRANG_OFFICE_SECCOMP_SOCKET")}); err != nil {
			os.Exit(13)
		}
		if err := unix.Listen(fd, 1); err != nil {
			os.Exit(14)
		}
		if accepted, _, err := unix.Accept4(fd, unix.SOCK_CLOEXEC); !errors.Is(err, unix.EAGAIN) {
			if err == nil {
				_ = unix.Close(accepted)
			}
			os.Exit(15)
		}
		if pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0); !errors.Is(err, unix.EPERM) {
			if err == nil {
				_ = unix.Close(pair[0])
				_ = unix.Close(pair[1])
			}
			os.Exit(16)
		}
		if inetFD, err := unix.Socket(unix.AF_INET, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0); !errors.Is(err, unix.EPERM) {
			if err == nil {
				_ = unix.Close(inetFD)
			}
			os.Exit(17)
		}
		if err := unix.Connect(fd, &unix.SockaddrUnix{Name: os.Getenv("XIRANG_OFFICE_SECCOMP_SOCKET")}); !errors.Is(err, unix.EPERM) {
			os.Exit(18)
		}
		if _, _, errno := unix.RawSyscall6(unix.SYS_SENDMMSG, uintptr(fd), 0, 0, 0, 0, 0); !errors.Is(errno, unix.EPERM) {
			os.Exit(19)
		}
		if _, _, errno := unix.RawSyscall6(unix.SYS_RECVMMSG, uintptr(fd), 0, 0, 0, 0, 0); !errors.Is(errno, unix.EPERM) {
			os.Exit(20)
		}
		os.Exit(0)
	}

	socketPath := filepath.Join(os.TempDir(), "xirang-office-"+strconv.Itoa(os.Getpid())+".sock")
	t.Cleanup(func() { _ = os.Remove(socketPath) })
	command := exec.Command(os.Args[0], "-test.run=^TestLinuxOfficeSeccompAllowsAFUNIXServerButDeniesOtherSocketOperations$")
	command.Env = append(os.Environ(),
		"XIRANG_OFFICE_SECCOMP_TEST=1",
		"XIRANG_OFFICE_SECCOMP_SOCKET="+socketPath,
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("Office seccomp contract failed: %v output=%q", err, output)
	}
}
