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
	"time"

	"xirang/backend/internal/backupasset/processing/capabilities"
)

var errSandboxContract = errors.New("asset tool sandbox contract rejected")

const libreOfficeVersionRC = "/usr/lib/libreoffice/program/versionrc"

type sandboxEnvironment struct {
	Workspace    string
	InputMode    string
	InputPath    string
	OutputDir    string
	HomeDir      string
	CPUSeconds   int64
	MemoryBytes  int64
	FileBytes    int64
	MaxProcesses int
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
	if invocation.Profile == "office_pdf_v1" {
		versionPayload, err := os.ReadFile(libreOfficeVersionRC)
		if err != nil || prepareLibreOfficeProfile(environment.HomeDir, versionPayload) != nil {
			return errSandboxContract
		}
	}
	request, err := sandboxExecutionForInvocation(invocation, environment)
	if err != nil {
		return err
	}
	return capabilities.ExecuteSandbox(request)
}

func sandboxExecutionForInvocation(
	invocation sandboxInvocation,
	environment sandboxEnvironment,
) (capabilities.SandboxExecution, error) {
	resources := capabilities.SandboxResourceLimits{
		CPUTime:        time.Duration(environment.CPUSeconds) * time.Second,
		MaxMemoryBytes: environment.MemoryBytes, MaxFileBytes: environment.FileBytes,
		MaxProcesses: environment.MaxProcesses,
	}
	profile := capabilities.ToolArgProfile(invocation.Profile)
	if err := resources.ValidateFor(profile); err != nil {
		return capabilities.SandboxExecution{}, errSandboxContract
	}
	return capabilities.SandboxExecution{
		Executable: invocation.Executable, Profile: profile, Args: invocation.Args,
		Workspace: environment.Workspace, InputMode: capabilities.ToolInputMode(environment.InputMode),
		InputPath: environment.InputPath, OutputDir: environment.OutputDir, HomeDir: environment.HomeDir,
		CPUTime: resources.CPUTime, MaxMemoryBytes: resources.MaxMemoryBytes,
		MaxFileBytes: resources.MaxFileBytes, MaxProcesses: resources.MaxProcesses,
	}, nil
}

func sandboxEnvironmentFromProcess() sandboxEnvironment {
	output := os.Getenv("XIRANG_OUTPUT_DIR")
	return sandboxEnvironment{
		Workspace: filepath.Dir(output), InputMode: os.Getenv("XIRANG_INPUT_MODE"),
		InputPath: os.Getenv("XIRANG_INPUT_PATH"), OutputDir: output, HomeDir: os.Getenv("HOME"),
		CPUSeconds:   parseSandboxEnvironmentInt64("XIRANG_RLIMIT_CPU_SECONDS"),
		MemoryBytes:  parseSandboxEnvironmentInt64("XIRANG_RLIMIT_MEMORY_BYTES"),
		FileBytes:    parseSandboxEnvironmentInt64("XIRANG_RLIMIT_FSIZE_BYTES"),
		MaxProcesses: int(parseSandboxEnvironmentInt64("XIRANG_RLIMIT_PROCESSES")),
	}
}

func parseSandboxEnvironmentInt64(name string) int64 {
	value, err := strconv.ParseInt(os.Getenv(name), 10, 64)
	if err != nil || value <= 0 {
		return 0
	}
	return value
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
			return finishSandboxInvocation(invocation, environment)
		case *executableID == "xz" && *profile == "xz_decompress_v1":
			invocation.Executable = "/usr/bin/xz"
			invocation.Args = []string{"-dc"}
			return finishSandboxInvocation(invocation, environment)
		case *executableID == "zstd" && *profile == "zstd_decompress_v1":
			invocation.Executable = "/usr/bin/zstd"
			invocation.Args = []string{"-dcq"}
			return finishSandboxInvocation(invocation, environment)
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
			"thumbnail", environment.InputPath,
			filepath.Join(environment.OutputDir, "thumbnail.png") + "[strip,Q=" + strconv.Itoa(*quality) + "]",
			strconv.Itoa(*width), "--height=" + strconv.Itoa(*height), "--size=both",
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
		invocation.Args = []string{"-png", "-f", "1", "-l", "30", environment.InputPath, filepath.Join(environment.OutputDir, "page")}
	case *executableID == "pdftotext" && *profile == "pdf_text_v1":
		if *width != 0 || *height != 0 || *quality != 0 || *language != "" {
			return sandboxInvocation{}, errSandboxContract
		}
		invocation.Executable = "/usr/bin/pdftotext"
		invocation.Args = []string{"-f", "1", "-l", "30", "-enc", "UTF-8", "-nopgbrk", environment.InputPath, filepath.Join(environment.OutputDir, "content.txt")}
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
		invocation.Args = []string{
			"--no-summary",
			"--stdout",
			"--infected",
			"--max-filesize=1073741824",
			"--max-scansize=1073741824",
			"--max-files=100000",
			"--max-recursion=16",
			"--alert-exceeds-max=yes",
			"--database=/var/lib/xirang/asset-worker-bundles/active/clamav",
			"input.bin",
		}
	case *executableID == "ffprobe" && *profile == "media_probe_v1":
		if *width != 0 || *height != 0 || *quality != 0 || *language != "" {
			return sandboxInvocation{}, errSandboxContract
		}
		invocation.Executable = "/usr/bin/ffprobe"
		invocation.Args = []string{
			"-v", "error",
			"-show_entries", "stream=index,codec_type,codec_name,width,height,duration:format=duration",
			"-of", "json",
			environment.InputPath,
		}
	case *executableID == "ffmpeg" && *profile == "media_preview_v1":
		if *width != 0 || *height != 0 || *quality != 0 || *language != "" {
			return sandboxInvocation{}, errSandboxContract
		}
		invocation.Executable = "/usr/bin/ffmpeg"
		invocation.Args = []string{
			"-nostdin", "-v", "error", "-filter_threads", "1", "-filter_complex_threads", "1",
			"-protocol_whitelist", "file,pipe", "-i", environment.InputPath,
			"-t", "1800", "-map", "0:v:0?", "-map", "0:a:0?",
			"-vf", "scale=w=1920:h=1080:force_original_aspect_ratio=decrease",
			"-c:v", "libx264", "-preset", "veryfast", "-crf", "28", "-maxrate", "4000k", "-bufsize", "8000k",
			"-pix_fmt", "yuv420p", "-threads", "1",
			"-c:a", "aac", "-b:a", "128k", "-ar", "48000", "-ac", "2",
			"-map_metadata", "-1", "-map_chapters", "-1", "-sn", "-dn", "-movflags", "+faststart",
			"-y", filepath.Join(environment.OutputDir, "preview.mp4"),
		}
	default:
		return sandboxInvocation{}, errSandboxContract
	}
	return finishSandboxInvocation(invocation, environment)
}

func prepareLibreOfficeProfile(home string, versionPayload []byte) error {
	if !cleanAbsolutePath(home) || len(versionPayload) == 0 || len(versionPayload) > 64<<10 {
		return errSandboxContract
	}
	buildID := ""
	for _, line := range strings.Split(string(versionPayload), "\n") {
		if !strings.HasPrefix(line, "buildid=") {
			continue
		}
		if buildID != "" {
			return errSandboxContract
		}
		buildID = strings.TrimPrefix(line, "buildid=")
	}
	if buildID == "" || len(buildID) > 64 {
		return errSandboxContract
	}
	for _, character := range buildID {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || strings.ContainsRune("().:+_-", character) {
			continue
		}
		return errSandboxContract
	}
	extensions := filepath.Join(home, "user", "extensions")
	if err := os.MkdirAll(extensions, 0o700); err != nil {
		return errSandboxContract
	}
	for _, directory := range []string{filepath.Join(home, "user"), extensions} {
		info, err := os.Lstat(directory)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
			return errSandboxContract
		}
	}
	path := filepath.Join(extensions, "buildid")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return errSandboxContract
	}
	payload := []byte(buildID + "\n")
	written, writeErr := file.Write(payload)
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil || written != len(payload) {
		return errSandboxContract
	}
	return nil
}

func finishSandboxInvocation(invocation sandboxInvocation, environment sandboxEnvironment) (sandboxInvocation, error) {
	if _, err := sandboxExecutionForInvocation(invocation, environment); err != nil {
		return sandboxInvocation{}, errSandboxContract
	}
	return invocation, nil
}

func validSandboxEnvironment(value sandboxEnvironment) bool {
	if !cleanAbsolutePath(value.Workspace) || value.Workspace == string(os.PathSeparator) ||
		!strings.HasPrefix(filepath.Base(value.Workspace), "job-") ||
		value.OutputDir != filepath.Join(value.Workspace, "output") || value.HomeDir != filepath.Join(value.Workspace, "home") ||
		value.CPUSeconds <= 0 || value.CPUSeconds > int64((2*time.Hour)/time.Second) ||
		value.MemoryBytes < 64<<20 || value.MemoryBytes > 16<<30 ||
		value.FileBytes <= 0 || value.FileBytes > 8<<30 ||
		value.MaxProcesses <= 0 || value.MaxProcesses > 128 {
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
