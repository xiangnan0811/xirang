//go:build linux

package rsyncconfinement

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateLandlockABIRequiresTruncateABI3(t *testing.T) {
	if err := validateLandlockABI(3); err != nil {
		t.Fatalf("ABI 3 rejected: %v", err)
	}
	for _, abi := range []int{0, 1, 2} {
		err := validateLandlockABI(abi)
		if !errors.Is(err, ErrCapabilityUnavailable) {
			t.Fatalf("ABI %d error=%v, want capability-unavailable", abi, err)
		}
		message := err.Error()
		if !strings.Contains(message, "ABI") || !strings.Contains(message, "TRUNCATE") || !strings.Contains(message, "3") {
			t.Fatalf("ABI %d diagnostic=%q, want ABI/TRUNCATE/3 details", abi, message)
		}
	}
}

func TestRemovedLinkDestGrammarRejected(t *testing.T) {
	for _, option := range []string{
		"--mount-link-dest=/tmp/parent",
		"--mount-link-dest-fd=3",
	} {
		args := []string{
			"--protocol=2",
			"--binary=/usr/bin/rsync",
			option,
			"--",
			"-a",
			"/source",
			"/target",
		}
		if _, err := parseHelperRequest(args); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("removed helper option %q error=%v, want ErrInvalidRequest", option, err)
		}
	}
	for _, args := range [][]string{
		{"--link-dest=/tmp/parent", "--", "/source", "/target"},
		{"--link-dest", "/tmp/parent", "--", "/source", "/target"},
	} {
		if err := validateConfinedRsyncArgs(args); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("removed Rsync option %q error=%v, want ErrInvalidRequest", args[0], err)
		}
	}
}
