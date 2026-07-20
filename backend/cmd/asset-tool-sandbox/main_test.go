package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseSandboxInvocationUsesOnlyClosedToolProfileAndWorkspacePaths(t *testing.T) {
	workspace := filepath.Join(string(os.PathSeparator), "run", "xirang", "asset-jobs", "job-opaque")
	environment := sandboxEnvironment{
		Workspace: workspace,
		InputMode: "path",
		InputPath: filepath.Join(workspace, "input.bin"),
		OutputDir: filepath.Join(workspace, "output"),
		HomeDir:   filepath.Join(workspace, "home"),
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
	joined := strings.Join(invocation.Args, " ")
	if !strings.Contains(joined, environment.InputPath) || !strings.Contains(joined, filepath.Join(environment.OutputDir, "thumbnail.png")) {
		t.Fatalf("workspace input/output not bound into closed argv: %q", joined)
	}
	if strings.Contains(joined, "http://") || strings.Contains(joined, "https://") || strings.Contains(joined, "sh -c") {
		t.Fatalf("open-ended argv accepted: %q", joined)
	}
}

func TestParseSandboxInvocationRejectsCallerSelectedCommandsPathsAndFlags(t *testing.T) {
	workspace := filepath.Join(string(os.PathSeparator), "run", "xirang", "asset-jobs", "job-opaque")
	validEnvironment := sandboxEnvironment{
		Workspace: workspace,
		InputMode: "path",
		InputPath: filepath.Join(workspace, "input.bin"),
		OutputDir: filepath.Join(workspace, "output"),
		HomeDir:   filepath.Join(workspace, "home"),
	}
	cases := []struct {
		args        []string
		environment sandboxEnvironment
	}{
		{args: []string{"--executable-id=/bin/sh", "--arg-profile=vips_thumbnail_v1"}, environment: validEnvironment},
		{args: []string{"--executable-id=vips", "--arg-profile=caller_selected"}, environment: validEnvironment},
		{args: []string{"--executable-id=vips", "--arg-profile=vips_thumbnail_v1", "--width=320", "--height=180", "--quality=80", "--url=https://example.invalid"}, environment: validEnvironment},
		{args: []string{"--executable-id=vips", "--arg-profile=vips_thumbnail_v1", "--width=320", "--height=180", "--quality=80"}, environment: sandboxEnvironment{Workspace: workspace, InputMode: "path", InputPath: "/etc/passwd", OutputDir: filepath.Join(workspace, "output"), HomeDir: filepath.Join(workspace, "home")}},
		{args: []string{"--executable-id=vips", "--arg-profile=vips_thumbnail_v1", "--width=320", "--height=180", "--quality=80"}, environment: sandboxEnvironment{Workspace: workspace, InputMode: "path", InputPath: filepath.Join(workspace, "input.bin"), OutputDir: "/tmp/output", HomeDir: filepath.Join(workspace, "home")}},
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
		Workspace: workspace,
		InputMode: "path",
		InputPath: filepath.Join(workspace, "input.bin"),
		OutputDir: filepath.Join(workspace, "output"),
		HomeDir:   filepath.Join(workspace, "home"),
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

func TestParseSandboxInvocationCoversPinnedExternalProfiles(t *testing.T) {
	workspace := filepath.Join(string(os.PathSeparator), "run", "xirang", "asset-jobs", "job-opaque")
	environment := sandboxEnvironment{
		Workspace: workspace, InputMode: "path", InputPath: filepath.Join(workspace, "input.bin"),
		OutputDir: filepath.Join(workspace, "output"), HomeDir: filepath.Join(workspace, "home"),
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

func TestParseSandboxInvocationUsesFixedPipeOnlyDecompressorProfiles(t *testing.T) {
	workspace := filepath.Join(string(os.PathSeparator), "run", "xirang", "asset-jobs", "job-opaque")
	environment := sandboxEnvironment{
		Workspace: workspace, InputMode: "pipe", OutputDir: filepath.Join(workspace, "output"),
		HomeDir: filepath.Join(workspace, "home"),
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
