package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"runtime"

	"golang.org/x/term"
)

func supportsControllingTerminal(goos string) bool {
	switch goos {
	case "aix", "darwin", "dragonfly", "freebsd", "illumos", "linux", "netbsd", "openbsd", "solaris":
		return true
	default:
		return false
	}
}

func readTerminalSecrets(resetPassword bool, stdin *os.File) (string, string, error) {
	if !supportsControllingTerminal(runtime.GOOS) {
		return "", "", errors.New("unsupported controlling terminal")
	}
	if resetPassword && (stdin == nil || !term.IsTerminal(int(stdin.Fd()))) {
		return "", "", errors.New("password reset requires terminal stdin")
	}
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return "", "", errors.New("controlling terminal unavailable")
	}
	defer func() { _ = tty.Close() }()
	if !term.IsTerminal(int(tty.Fd())) {
		return "", "", errors.New("controlling terminal unavailable")
	}
	confirmation, err := readSecret(tty, "Confirm the displayed target and reset scope by entering XIRANG_BREAK_GLASS_CONFIRMATION: ")
	if err != nil {
		return "", "", err
	}
	defer clear(confirmation)
	if !resetPassword {
		return string(confirmation), "", nil
	}
	first, err := readSecret(tty, "New password: ")
	if err != nil {
		return "", "", err
	}
	defer clear(first)
	second, err := readSecret(tty, "Repeat new password: ")
	if err != nil {
		return "", "", err
	}
	defer clear(second)
	if !bytes.Equal(first, second) {
		return "", "", errors.New("password entries do not match")
	}
	// Deliberately no TrimSpace: policy validation and bcrypt hashing of these
	// same raw password bytes belong to bootstrap.RecoverAccount.
	return string(confirmation), string(first), nil
}

func readSecret(tty *os.File, prompt string) ([]byte, error) {
	if _, err := fmt.Fprint(tty, prompt); err != nil {
		return nil, errors.New("terminal output failed")
	}
	value, err := term.ReadPassword(int(tty.Fd()))
	_, newlineErr := fmt.Fprintln(tty)
	if err != nil || newlineErr != nil {
		clear(value)
		return nil, errors.New("terminal input failed")
	}
	return value, nil
}
