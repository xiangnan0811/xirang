package capabilities

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

const productionSandboxBinary = "/usr/local/bin/asset-tool-sandbox"

type executableResolver func(ExecutableID) (string, error)

type SandboxExecution struct {
	Executable   string
	Args         []string
	Workspace    string
	InputMode    ToolInputMode
	InputPath    string
	OutputDir    string
	HomeDir      string
	MaxProcesses int
}

func ExecuteSandbox(request SandboxExecution) error {
	if err := validateSandboxExecution(request); err != nil {
		return err
	}
	return executeSandbox(request)
}

func validateSandboxExecution(value SandboxExecution) error {
	if !allowedSandboxExecutable(value.Executable) || !cleanWorkspaceRoot(value.Workspace) ||
		!strings.HasPrefix(filepath.Base(value.Workspace), "job-") ||
		value.OutputDir != filepath.Join(value.Workspace, "output") || value.HomeDir != filepath.Join(value.Workspace, "home") ||
		value.MaxProcesses <= 0 || value.MaxProcesses > 128 || len(value.Args) == 0 || len(value.Args) > 64 {
		return ErrInvalidInvocation
	}
	switch value.InputMode {
	case ToolInputPipe:
		if value.InputPath != "" {
			return ErrInvalidInvocation
		}
	case ToolInputPath:
		if value.InputPath != filepath.Join(value.Workspace, "input.bin") {
			return ErrInvalidInvocation
		}
	default:
		return ErrInvalidInvocation
	}
	for _, argument := range value.Args {
		lower := strings.ToLower(argument)
		if argument == "" || len(argument) > 4096 || strings.ContainsAny(argument, "\x00\r\n") ||
			strings.Contains(lower, "http://") || strings.Contains(lower, "https://") || strings.Contains(lower, "sh -c") {
			return ErrInvalidInvocation
		}
	}
	return nil
}

func allowedSandboxExecutable(value string) bool {
	switch value {
	case "/usr/bin/vips", "/usr/bin/tesseract", "/usr/bin/pdftocairo", "/usr/bin/pdftotext",
		"/usr/lib/libreoffice/program/soffice.bin", "/usr/bin/clamscan", "/usr/bin/ffprobe", "/usr/bin/ffmpeg",
		"/bin/gzip", "/usr/bin/xz", "/usr/bin/zstd":
		return true
	default:
		return false
	}
}

func productionExecutableResolver(ExecutableID) (string, error) {
	info, err := os.Stat(productionSandboxBinary)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o022 != 0 {
		return "", ErrSecureWorkspaceUnavailable
	}
	return productionSandboxBinary, nil
}

func cleanWorkspaceRoot(root string) bool {
	return filepath.IsAbs(root) && filepath.Clean(root) == root && root != "/" && !strings.ContainsAny(root, "\x00\r\n")
}

func sanitizeProcessError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, os.ErrPermission) {
		return ErrSecureWorkspaceUnavailable
	}
	return ErrToolFailed
}
