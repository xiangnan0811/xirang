//go:build !linux

package sshutil

import (
	"context"
	"os/exec"
	"time"
)

// runOpenSSHProbe is the non-Linux fallback: only ssh itself is terminated on
// cancellation. Production deployments run on Linux, which reaps the whole
// probe process group (see openssh_probe_linux.go).
func runOpenSSHProbe(ctx context.Context, binary string, args []string) {
	probe := exec.CommandContext(ctx, binary, args...)
	probe.WaitDelay = 2 * time.Second
	_ = probe.Run()
}
