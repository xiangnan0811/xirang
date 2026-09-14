//go:build !linux

package rsyncconfinement

import (
	"context"
	"fmt"
	"os/exec"
)

func newConfinedCommand(context.Context, string, string, CommandRequest) (*exec.Cmd, func(), error) {
	return nil, func() {}, fmt.Errorf("%w: filesystem confinement requires Linux Landlock", ErrCapabilityUnavailable)
}
