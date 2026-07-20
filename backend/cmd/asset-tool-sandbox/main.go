package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"xirang/backend/internal/backupasset/processing/capabilities"
)

var errSandboxContract = errors.New("asset tool sandbox contract rejected")

type sandboxEnvironment struct {
	Workspace string
	InputMode string
	InputPath string
	OutputDir string
	HomeDir   string
}

type sandboxInvocation struct {
	Executable string
	Profile    string
	Args       []string
}

func main() {
	if err := runAssetToolSandbox(os.Args[1:], sandboxEnvironmentFromProcess()); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "asset tool sandbox rejected invocation")
		os.Exit(1)
	}
}

func runAssetToolSandbox(args []string, environment sandboxEnvironment) error {
	return runAssetToolSandboxWithExecutor(args, environment, executeSandboxInvocation)
}

func runAssetToolSandboxWithExecutor(
	args []string,
	environment sandboxEnvironment,
	executor func(sandboxInvocation, sandboxEnvironment) error,
) error {
	if executor == nil {
		return errSandboxContract
	}
	invocation, err := parseSandboxInvocation(args, environment)
	if err != nil {
		return err
	}
	return executor(invocation, environment)
}

func executeSandboxInvocation(invocation sandboxInvocation, environment sandboxEnvironment) error {
	return capabilities.ExecuteSandbox(capabilities.SandboxExecution{
		Executable: invocation.Executable, Args: invocation.Args, Workspace: environment.Workspace,
		InputMode: capabilities.ToolInputMode(environment.InputMode), InputPath: environment.InputPath,
		OutputDir: environment.OutputDir, HomeDir: environment.HomeDir, MaxProcesses: 64,
	})
}

func sandboxEnvironmentFromProcess() sandboxEnvironment {
	output := os.Getenv("XIRANG_OUTPUT_DIR")
	return sandboxEnvironment{
		Workspace: filepath.Dir(output), InputMode: os.Getenv("XIRANG_INPUT_MODE"),
		InputPath: os.Getenv("XIRANG_INPUT_PATH"), OutputDir: output, HomeDir: os.Getenv("HOME"),
	}
}

func parseSandboxInvocation(args []string, environment sandboxEnvironment) (sandboxInvocation, error) {
	if !validSandboxEnvironment(environment) {
		return sandboxInvocation{}, errSandboxContract
	}
	flags := flag.NewFlagSet("asset-tool-sandbox", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	executableID := flags.String("executable-id", "", "")
	profile := flags.String("arg-profile", "", "")
	width := flags.Int("width", 0, "")
	height := flags.Int("height", 0, "")
	quality := flags.Int("quality", 0, "")
	language := flags.String("language", "", "")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return sandboxInvocation{}, errSandboxContract
	}
	invocation := sandboxInvocation{Profile: *profile}
	if environment.InputMode == "pipe" && *width == 0 && *height == 0 && *quality == 0 && *language == "" {
		switch {
		case *executableID == "gzip" && *profile == "gzip_decompress_v1":
			invocation.Executable = "/bin/gzip"
			invocation.Args = []string{"-dc"}
			return invocation, nil
		case *executableID == "xz" && *profile == "xz_decompress_v1":
			invocation.Executable = "/usr/bin/xz"
			invocation.Args = []string{"-dc"}
			return invocation, nil
		case *executableID == "zstd" && *profile == "zstd_decompress_v1":
			invocation.Executable = "/usr/bin/zstd"
			invocation.Args = []string{"-dcq"}
			return invocation, nil
		}
	}
	if environment.InputMode != "path" {
		return sandboxInvocation{}, errSandboxContract
	}
	switch {
	case *executableID == "vips" && *profile == "vips_thumbnail_v1":
		if *width <= 0 || *width > 4096 || *height <= 0 || *height > 4096 || *quality <= 0 || *quality > 100 || *language != "" {
			return sandboxInvocation{}, errSandboxContract
		}
		invocation.Executable = "/usr/bin/vips"
		invocation.Args = []string{
			"thumbnail", environment.InputPath, filepath.Join(environment.OutputDir, "thumbnail.png"),
			strconv.Itoa(*width), "--height=" + strconv.Itoa(*height), "--size=both", "--strip", "--Q=" + strconv.Itoa(*quality),
		}
	case *executableID == "vips" && *profile == "vips_raster_normalize_v1":
		if *width != 0 || *height != 0 || *quality != 0 || *language != "" {
			return sandboxInvocation{}, errSandboxContract
		}
		invocation.Executable = "/usr/bin/vips"
		invocation.Args = []string{
			"copy", environment.InputPath, filepath.Join(environment.OutputDir, "normalized.png") + "[strip]",
		}
	case *executableID == "tesseract" && *profile == "tesseract_ocr_v1":
		if *width != 0 || *height != 0 || *quality != 0 || (*language != "eng" && *language != "chi_sim" && *language != "eng+chi_sim") {
			return sandboxInvocation{}, errSandboxContract
		}
		invocation.Executable = "/usr/bin/tesseract"
		invocation.Args = []string{environment.InputPath, filepath.Join(environment.OutputDir, "ocr"), "-l", *language, "--psm", "6", "txt"}
	case *executableID == "pdftocairo" && *profile == "pdf_pages_v1":
		if *width != 0 || *height != 0 || *quality != 0 || *language != "" {
			return sandboxInvocation{}, errSandboxContract
		}
		invocation.Executable = "/usr/bin/pdftocairo"
		invocation.Args = []string{"-png", "-f", "1", "-l", "30", "-singlefile", environment.InputPath, filepath.Join(environment.OutputDir, "page-01")}
	case *executableID == "libreoffice" && *profile == "office_pdf_v1":
		if *width != 0 || *height != 0 || *quality != 0 || *language != "" {
			return sandboxInvocation{}, errSandboxContract
		}
		invocation.Executable = "/usr/lib/libreoffice/program/soffice.bin"
		invocation.Args = []string{
			"--headless", "--nologo", "--nodefault", "--nolockcheck", "--norestore",
			"-env:UserInstallation=file://" + environment.HomeDir, "--convert-to", "pdf", "--outdir", environment.OutputDir, environment.InputPath,
		}
	case *executableID == "clamscan" && *profile == "clam_scan_v1":
		if *width != 0 || *height != 0 || *quality != 0 || *language != "" {
			return sandboxInvocation{}, errSandboxContract
		}
		invocation.Executable = "/usr/bin/clamscan"
		invocation.Args = []string{"--no-summary", "--stdout", "--database=/var/lib/xirang/asset-worker-bundles/active/clamav", environment.InputPath}
	case *executableID == "ffprobe" && *profile == "media_probe_v1":
		if *width != 0 || *height != 0 || *quality != 0 || *language != "" {
			return sandboxInvocation{}, errSandboxContract
		}
		invocation.Executable = "/usr/bin/ffprobe"
		invocation.Args = []string{"-v", "error", "-show_format", "-show_streams", "-of", "json", environment.InputPath}
	case *executableID == "ffmpeg" && *profile == "media_preview_v1":
		if *width != 0 || *height != 0 || *quality != 0 || *language != "" {
			return sandboxInvocation{}, errSandboxContract
		}
		invocation.Executable = "/usr/bin/ffmpeg"
		invocation.Args = []string{
			"-nostdin", "-v", "error", "-protocol_whitelist", "file,pipe", "-i", environment.InputPath,
			"-t", "1800", "-vf", "scale=w=1920:h=1080:force_original_aspect_ratio=decrease", "-movflags", "+faststart",
			"-y", filepath.Join(environment.OutputDir, "preview.mp4"),
		}
	default:
		return sandboxInvocation{}, errSandboxContract
	}
	return invocation, nil
}

func validSandboxEnvironment(value sandboxEnvironment) bool {
	if !cleanAbsolutePath(value.Workspace) || value.Workspace == string(os.PathSeparator) ||
		!strings.HasPrefix(filepath.Base(value.Workspace), "job-") ||
		value.OutputDir != filepath.Join(value.Workspace, "output") || value.HomeDir != filepath.Join(value.Workspace, "home") {
		return false
	}
	switch value.InputMode {
	case "pipe":
		return value.InputPath == ""
	case "path":
		return value.InputPath == filepath.Join(value.Workspace, "input.bin")
	default:
		return false
	}
}

func cleanAbsolutePath(value string) bool {
	return filepath.IsAbs(value) && filepath.Clean(value) == value && !strings.ContainsAny(value, "\x00\r\n")
}
