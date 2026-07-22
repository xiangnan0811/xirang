package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestParseSandboxInvocationUsesOnlyClosedToolProfileAndWorkspacePaths(t *testing.T) {
	workspace := filepath.Join(string(os.PathSeparator), "run", "xirang", "asset-jobs", "job-opaque")
	environment := sandboxEnvironment{
		Workspace:  workspace,
		InputMode:  "path",
		InputPath:  filepath.Join(workspace, "input.bin"),
		OutputDir:  filepath.Join(workspace, "output"),
		HomeDir:    filepath.Join(workspace, "home"),
		CPUSeconds: 60, MemoryBytes: 512 << 20, FileBytes: 64 << 10, MaxProcesses: 4,
	}
	invocation, err := parseSandboxInvocation([]string{
		"--executable-id=vips", "--arg-profile=vips_thumbnail_v1",
		"--width=320", "--height=180", "--quality=80",
	}, environment)
	if err != nil {
		t.Fatal(err)
	}
	if invocation.Executable != "/usr/bin/vips" || invocation.Profile != "vips_thumbnail_v1" {
		t.Fatalf("unexpected fixed invocation: %+v", invocation)
	}
	wantOutput := filepath.Join(environment.OutputDir, "thumbnail.png") + "[strip,Q=80]"
	if !containsSandboxArgument(invocation.Args, wantOutput) ||
		containsSandboxArgument(invocation.Args, "--strip") || containsSandboxArgument(invocation.Args, "--Q=80") {
		t.Fatalf("thumbnail invocation did not use closed libvips output options: %+v", invocation)
	}
	joined := strings.Join(invocation.Args, " ")
	if !strings.Contains(joined, environment.InputPath) || !strings.Contains(joined, filepath.Join(environment.OutputDir, "thumbnail.png")) {
		t.Fatalf("workspace input/output not bound into closed argv: %q", joined)
	}
	if strings.Contains(joined, "http://") || strings.Contains(joined, "https://") || strings.Contains(joined, "sh -c") {
		t.Fatalf("open-ended argv accepted: %q", joined)
	}
}

func TestParseSandboxInvocationFixesMediaEncodingAndMetadataPolicy(t *testing.T) {
	workspace := filepath.Join(string(os.PathSeparator), "run", "xirang", "asset-jobs", "job-opaque")
	environment := sandboxEnvironment{
		Workspace: workspace, InputMode: "path", InputPath: filepath.Join(workspace, "input.bin"),
		OutputDir: filepath.Join(workspace, "output"), HomeDir: filepath.Join(workspace, "home"),
		CPUSeconds: 1800, MemoryBytes: 4 << 30, FileBytes: 512 << 20, MaxProcesses: 32,
	}
	invocation, err := parseSandboxInvocation([]string{
		"--executable-id=ffmpeg", "--arg-profile=media_preview_v1",
	}, environment)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"-c:v", "libx264", "-threads", "1", "-filter_threads", "1", "-filter_complex_threads",
		"-preset", "veryfast", "-crf", "28", "-maxrate", "4000k", "-bufsize", "8000k",
		"-pix_fmt", "yuv420p", "-c:a", "aac", "-b:a", "128k", "-map_metadata", "-1",
		"-map_chapters", "-sn", "-dn",
	} {
		if !containsSandboxArgument(invocation.Args, required) {
			t.Fatalf("media preview invocation omitted closed argument %q: %+v", required, invocation)
		}
	}
	if invocation.Args[len(invocation.Args)-1] != filepath.Join(environment.OutputDir, "preview.mp4") {
		t.Fatalf("media preview output path is not fixed: %+v", invocation)
	}
}

func TestParseSandboxInvocationUsesClosedMediaProbeProjection(t *testing.T) {
	workspace := filepath.Join(string(os.PathSeparator), "run", "xirang", "asset-jobs", "job-opaque")
	environment := sandboxEnvironment{
		Workspace: workspace, InputMode: "path", InputPath: filepath.Join(workspace, "input.bin"),
		OutputDir: filepath.Join(workspace, "output"), HomeDir: filepath.Join(workspace, "home"),
		CPUSeconds: 120, MemoryBytes: 1 << 30, FileBytes: 1 << 20, MaxProcesses: 16,
	}
	invocation, err := parseSandboxInvocation([]string{
		"--executable-id=ffprobe", "--arg-profile=media_probe_v1",
	}, environment)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"-v", "error",
		"-show_entries", "stream=index,codec_type,codec_name,width,height,duration:format=duration",
		"-of", "json",
		environment.InputPath,
	}
	if invocation.Executable != "/usr/bin/ffprobe" || !slices.Equal(invocation.Args, want) {
		t.Fatalf("ffprobe invocation=%+v, want executable=/usr/bin/ffprobe args=%q", invocation, want)
	}
}

func TestParseSandboxInvocationRejectsCallerSelectedCommandsPathsAndFlags(t *testing.T) {
	workspace := filepath.Join(string(os.PathSeparator), "run", "xirang", "asset-jobs", "job-opaque")
	validEnvironment := sandboxEnvironment{
		Workspace:  workspace,
		InputMode:  "path",
		InputPath:  filepath.Join(workspace, "input.bin"),
		OutputDir:  filepath.Join(workspace, "output"),
		HomeDir:    filepath.Join(workspace, "home"),
		CPUSeconds: 60, MemoryBytes: 512 << 20, FileBytes: 64 << 10, MaxProcesses: 4,
	}
	cases := []struct {
		args        []string
		environment sandboxEnvironment
	}{
		{args: []string{"--executable-id=/bin/sh", "--arg-profile=vips_thumbnail_v1"}, environment: validEnvironment},
		{args: []string{"--executable-id=vips", "--arg-profile=caller_selected"}, environment: validEnvironment},
		{args: []string{"--executable-id=vips", "--arg-profile=vips_thumbnail_v1", "--width=320", "--height=180", "--quality=80", "--url=https://example.invalid"}, environment: validEnvironment},
		{args: []string{"--executable-id=vips", "--arg-profile=vips_thumbnail_v1", "--width=320", "--height=180", "--quality=80"}, environment: sandboxEnvironment{Workspace: workspace, InputMode: "path", InputPath: "/etc/passwd", OutputDir: filepath.Join(workspace, "output"), HomeDir: filepath.Join(workspace, "home"), CPUSeconds: 60, MemoryBytes: 512 << 20, FileBytes: 64 << 10, MaxProcesses: 4}},
		{args: []string{"--executable-id=vips", "--arg-profile=vips_thumbnail_v1", "--width=320", "--height=180", "--quality=80"}, environment: sandboxEnvironment{Workspace: workspace, InputMode: "path", InputPath: filepath.Join(workspace, "input.bin"), OutputDir: "/tmp/output", HomeDir: filepath.Join(workspace, "home"), CPUSeconds: 60, MemoryBytes: 512 << 20, FileBytes: 64 << 10, MaxProcesses: 4}},
	}
	for index, testCase := range cases {
		if _, err := parseSandboxInvocation(testCase.args, testCase.environment); err == nil {
			t.Fatalf("unsafe sandbox case %d accepted: %+v", index, testCase)
		}
	}
}

func TestRunAssetToolSandboxPassesClosedInvocationToSingleExecutor(t *testing.T) {
	workspace := filepath.Join(string(os.PathSeparator), "run", "xirang", "asset-jobs", "job-opaque")
	environment := sandboxEnvironment{
		Workspace:  workspace,
		InputMode:  "path",
		InputPath:  filepath.Join(workspace, "input.bin"),
		OutputDir:  filepath.Join(workspace, "output"),
		HomeDir:    filepath.Join(workspace, "home"),
		CPUSeconds: 90, MemoryBytes: 1 << 30, FileBytes: 8 << 20, MaxProcesses: 16,
	}
	called := false
	err := runAssetToolSandboxWithExecutor([]string{
		"--executable-id=vips", "--arg-profile=vips_thumbnail_v1",
		"--width=320", "--height=180", "--quality=80",
	}, environment, func(invocation sandboxInvocation, gotEnvironment sandboxEnvironment) error {
		called = true
		if invocation.Executable != "/usr/bin/vips" || gotEnvironment != environment {
			t.Fatalf("executor received open or changed contract: %+v / %+v", invocation, gotEnvironment)
		}
		return nil
	})
	if err != nil || !called {
		t.Fatalf("closed executor err=%v called=%v", err, called)
	}
}

func TestSandboxExecutionForInvocationForwardsClosedProfileResourceLimits(t *testing.T) {
	workspace := filepath.Join(string(os.PathSeparator), "run", "xirang", "asset-jobs", "job-opaque")
	environment := sandboxEnvironment{
		Workspace: workspace, InputMode: "path", InputPath: filepath.Join(workspace, "input.bin"),
		OutputDir: filepath.Join(workspace, "output"), HomeDir: filepath.Join(workspace, "home"),
		CPUSeconds: 90, MemoryBytes: 1 << 30, FileBytes: 8 << 20, MaxProcesses: 16,
	}
	invocation, err := parseSandboxInvocation([]string{
		"--executable-id=vips", "--arg-profile=vips_thumbnail_v1",
		"--width=320", "--height=180", "--quality=80",
	}, environment)
	if err != nil {
		t.Fatal(err)
	}
	request, err := sandboxExecutionForInvocation(invocation, environment)
	if err != nil {
		t.Fatal(err)
	}
	if request.CPUTime != 90*time.Second || request.MaxMemoryBytes != 1<<30 ||
		request.MaxFileBytes != 8<<20 || request.MaxProcesses != 16 {
		t.Fatalf("sandbox execution limits=%+v", request)
	}

	tooBroad := environment
	tooBroad.MaxProcesses++
	if _, err := parseSandboxInvocation([]string{
		"--executable-id=vips", "--arg-profile=vips_thumbnail_v1",
		"--width=320", "--height=180", "--quality=80",
	}, tooBroad); err == nil {
		t.Fatal("helper accepted process limit above the closed profile")
	}
}

func TestParseSandboxInvocationCoversPinnedExternalProfiles(t *testing.T) {
	workspace := filepath.Join(string(os.PathSeparator), "run", "xirang", "asset-jobs", "job-opaque")
	environment := sandboxEnvironment{
		Workspace: workspace, InputMode: "path", InputPath: filepath.Join(workspace, "input.bin"),
		OutputDir: filepath.Join(workspace, "output"), HomeDir: filepath.Join(workspace, "home"),
		CPUSeconds: 60, MemoryBytes: 512 << 20, FileBytes: 64 << 10, MaxProcesses: 4,
	}
	tests := []struct {
		args       []string
		executable string
	}{
		{[]string{"--executable-id=tesseract", "--arg-profile=tesseract_ocr_v1", "--language=eng"}, "/usr/bin/tesseract"},
		{[]string{"--executable-id=pdftocairo", "--arg-profile=pdf_pages_v1"}, "/usr/bin/pdftocairo"},
		{[]string{"--executable-id=libreoffice", "--arg-profile=office_pdf_v1"}, "/usr/lib/libreoffice/program/soffice.bin"},
		{[]string{"--executable-id=clamscan", "--arg-profile=clam_scan_v1"}, "/usr/bin/clamscan"},
		{[]string{"--executable-id=ffprobe", "--arg-profile=media_probe_v1"}, "/usr/bin/ffprobe"},
		{[]string{"--executable-id=ffmpeg", "--arg-profile=media_preview_v1"}, "/usr/bin/ffmpeg"},
	}
	for _, testCase := range tests {
		invocation, err := parseSandboxInvocation(testCase.args, environment)
		if err != nil {
			t.Fatalf("%v: %v", testCase.args, err)
		}
		if invocation.Executable != testCase.executable || len(invocation.Args) == 0 {
			t.Fatalf("%v produced %+v", testCase.args, invocation)
		}
		for _, argument := range invocation.Args {
			if strings.Contains(argument, "http") || strings.Contains(argument, "tcp") || strings.Contains(argument, "concat") {
				t.Fatalf("network/open protocol argument in %+v", invocation)
			}
		}
		if testCase.executable == "/usr/bin/clamscan" {
			const database = "--database=/var/lib/xirang/asset-worker-bundles/active/clamav"
			if !containsSandboxArgument(invocation.Args, database) {
				t.Fatalf("ClamAV profile did not bind the active read-only bundle: %+v", invocation)
			}
		}
	}
}

func TestParseSandboxInvocationUsesClosedClamAVPolicy(t *testing.T) {
	workspace := filepath.Join(string(os.PathSeparator), "run", "xirang", "asset-jobs", "job-opaque")
	environment := sandboxEnvironment{
		Workspace: workspace, InputMode: "path", InputPath: filepath.Join(workspace, "input.bin"),
		OutputDir: filepath.Join(workspace, "output"), HomeDir: filepath.Join(workspace, "home"),
		CPUSeconds: 600, MemoryBytes: 2 << 30, FileBytes: 1 << 30, MaxProcesses: 16,
	}
	invocation, err := parseSandboxInvocation([]string{
		"--executable-id=clamscan", "--arg-profile=clam_scan_v1",
	}, environment)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
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
	if invocation.Executable != "/usr/bin/clamscan" || !slices.Equal(invocation.Args, want) {
		t.Fatalf("ClamAV invocation=%+v, want executable=/usr/bin/clamscan args=%q", invocation, want)
	}
}

func TestPrepareLibreOfficeProfileSeedsOnlyValidatedBuildID(t *testing.T) {
	home := t.TempDir()
	if err := prepareLibreOfficeProfile(home, []byte("[Version]\nbuildid=580(Build:1)\nUpdateURL=\n")); err != nil {
		t.Fatalf("prepare LibreOffice profile: %v", err)
	}
	buildIDPath := filepath.Join(home, "user", "extensions", "buildid")
	payload, err := os.ReadFile(buildIDPath)
	if err != nil || string(payload) != "580(Build:1)\n" {
		t.Fatalf("build ID payload=%q err=%v", payload, err)
	}
	for _, path := range []string{filepath.Join(home, "user"), filepath.Join(home, "user", "extensions")} {
		info, statErr := os.Lstat(path)
		if statErr != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
			t.Fatalf("profile directory %q info=%v err=%v", path, info, statErr)
		}
	}
	info, err := os.Lstat(buildIDPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("build ID file info=%v err=%v", info, err)
	}

	for index, invalid := range [][]byte{
		[]byte("buildid=580(Build:1)\nbuildid=581(Build:1)\n"),
		[]byte("buildid=../../unsafe\n"),
		[]byte("UpdateURL=\n"),
	} {
		if err := prepareLibreOfficeProfile(t.TempDir(), invalid); err == nil {
			t.Fatalf("invalid version payload %d accepted", index)
		}
	}
}

func TestParseSandboxInvocationUsesClosedPDFPageAndTextCommands(t *testing.T) {
	workspace := filepath.Join(string(os.PathSeparator), "run", "xirang", "asset-jobs", "job-opaque")
	environment := sandboxEnvironment{
		Workspace: workspace, InputMode: "path", InputPath: filepath.Join(workspace, "input.bin"),
		OutputDir: filepath.Join(workspace, "output"), HomeDir: filepath.Join(workspace, "home"),
		CPUSeconds: 600, MemoryBytes: 2 << 30, FileBytes: 64 << 20, MaxProcesses: 32,
	}
	tests := []struct {
		name       string
		arguments  []string
		executable string
		toolArgs   []string
	}{
		{
			name: "bounded page rasterization", arguments: []string{"--executable-id=pdftocairo", "--arg-profile=pdf_pages_v1"},
			executable: "/usr/bin/pdftocairo",
			toolArgs: []string{
				"-png", "-f", "1", "-l", "30", environment.InputPath,
				filepath.Join(environment.OutputDir, "page"),
			},
		},
		{
			name: "bounded UTF-8 text extraction", arguments: []string{"--executable-id=pdftotext", "--arg-profile=pdf_text_v1"},
			executable: "/usr/bin/pdftotext",
			toolArgs: []string{
				"-f", "1", "-l", "30", "-enc", "UTF-8", "-nopgbrk",
				environment.InputPath, filepath.Join(environment.OutputDir, "content.txt"),
			},
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			invocation, err := parseSandboxInvocation(testCase.arguments, environment)
			if err != nil {
				t.Fatal(err)
			}
			if invocation.Executable != testCase.executable || !slices.Equal(invocation.Args, testCase.toolArgs) {
				t.Fatalf("PDF invocation=%+v, want executable=%q args=%q", invocation, testCase.executable, testCase.toolArgs)
			}
			if containsSandboxArgument(invocation.Args, "-singlefile") {
				t.Fatalf("PDF invocation unexpectedly limits output to one page: %+v", invocation)
			}
		})
	}
}

func TestParseSandboxInvocationUsesFixedPipeOnlyDecompressorProfiles(t *testing.T) {
	workspace := filepath.Join(string(os.PathSeparator), "run", "xirang", "asset-jobs", "job-opaque")
	environment := sandboxEnvironment{
		Workspace: workspace, InputMode: "pipe", OutputDir: filepath.Join(workspace, "output"),
		HomeDir:    filepath.Join(workspace, "home"),
		CPUSeconds: 60, MemoryBytes: 512 << 20, FileBytes: 64 << 10, MaxProcesses: 4,
	}
	tests := []struct {
		id         string
		profile    string
		executable string
	}{
		{id: "gzip", profile: "gzip_decompress_v1", executable: "/bin/gzip"},
		{id: "xz", profile: "xz_decompress_v1", executable: "/usr/bin/xz"},
		{id: "zstd", profile: "zstd_decompress_v1", executable: "/usr/bin/zstd"},
	}
	for _, testCase := range tests {
		t.Run(testCase.id, func(t *testing.T) {
			invocation, err := parseSandboxInvocation([]string{
				"--executable-id=" + testCase.id,
				"--arg-profile=" + testCase.profile,
			}, environment)
			if err != nil {
				t.Fatal(err)
			}
			if invocation.Executable != testCase.executable || invocation.Profile != testCase.profile || len(invocation.Args) == 0 {
				t.Fatalf("decompressor invocation=%+v", invocation)
			}
			joined := strings.ToLower(strings.Join(invocation.Args, " "))
			for _, forbidden := range []string{"http://", "https://", "tcp", "udp", "sh -c", environment.InputPath} {
				if forbidden != "" && strings.Contains(joined, strings.ToLower(forbidden)) {
					t.Fatalf("decompressor argv contains %q: %+v", forbidden, invocation)
				}
			}
		})
	}
}

func containsSandboxArgument(arguments []string, expected string) bool {
	for _, argument := range arguments {
		if argument == expected {
			return true
		}
	}
	return false
}
