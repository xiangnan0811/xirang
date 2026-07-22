//go:build !linux

package capabilities

import (
	"os"
	"os/exec"
	"syscall"
	"time"
)

type mountInfo struct {
	Filesystem string
	Options    map[string]bool
}

func runtimeClosureManifestOwnerOK(os.FileInfo) bool { return false }

func inspectWorkspaceMount(string) (mountInfo, error) {
	return mountInfo{}, ErrSecureWorkspaceUnavailable
}
func validateWorkspaceMount(mountInfo) error { return ErrSecureWorkspaceUnavailable }
func configureToolProcess(*exec.Cmd)         {}
func signalToolProcessGroup(process *os.Process, signal syscall.Signal) error {
	if process == nil {
		return nil
	}
	return process.Signal(signal)
}

func killAndWaitToolProcessGroup(process *os.Process, _ time.Duration) error {
	if process == nil {
		return nil
	}
	return process.Kill()
}

func executeSandbox(SandboxExecution) error {
	return ErrSecureWorkspaceUnavailable
}
