//go:build !linux

package rsyncconfinement

import "fmt"

func executeHelper(helperRequest) error {
	return fmt.Errorf("%w: filesystem confinement requires Linux Landlock", ErrCapabilityUnavailable)
}

func runHelperCleanup([]string) error {
	return fmt.Errorf("%w: filesystem confinement requires Linux Landlock", ErrCapabilityUnavailable)
}
