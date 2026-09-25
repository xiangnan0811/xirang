//go:build linux

package sshutil

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

// signalOpenSSHProbeGroup kills a probe process group; replaced in tests.
var signalOpenSSHProbeGroup = func(pid int) error {
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}

// runOpenSSHProbe runs the registration probe in its own process group.
// ProxyCommand and ProxyJump helpers are children of ssh and survive when only
// ssh is killed, so cancellation, timeout and normal exit all kill the whole
// group. ssh is waited for without being reaped first, so its pid — the group
// id — cannot be reused by an unrelated process before the group is killed.
// Group signals are only ever sent while that holds: once the final kill is
// issued, cancellation is disabled under the same lock before ssh is reaped.
func runOpenSSHProbe(ctx context.Context, binary string, args []string) {
	var mu sync.Mutex
	signalable := true
	signalGroup := func(pid int) error {
		mu.Lock()
		defer mu.Unlock()
		if !signalable {
			return os.ErrProcessDone
		}
		return signalOpenSSHProbeGroup(pid)
	}

	probe := exec.CommandContext(ctx, binary, args...)
	probe.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	probe.Cancel = func() error { return signalGroup(probe.Process.Pid) }
	if err := probe.Start(); err != nil {
		return
	}
	pid := probe.Process.Pid
	var info unix.Siginfo
	for {
		if err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT, nil); !errors.Is(err, unix.EINTR) {
			break
		}
	}
	// Final kill and disabling further signals form one critical section, so
	// no Cancel can signal between them or after the reap below.
	mu.Lock()
	_ = signalOpenSSHProbeGroup(pid)
	signalable = false
	mu.Unlock()
	_ = probe.Wait()
}
