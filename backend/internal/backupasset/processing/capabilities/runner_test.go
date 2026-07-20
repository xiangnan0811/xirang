package capabilities

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset/processing/capabilityspec"
)

func TestBuildInvocationUsesClosedExecutableArgsAndEnvironment(t *testing.T) {
	profile, ok := capabilityspec.Lookup(capabilityspec.CapabilityImageThumbnail, capabilityspec.ProfileRasterThumbnailV1, false)
	if !ok {
		t.Fatal("thumbnail profile missing")
	}
	invocation, err := BuildInvocation(profile, ToolParameters{Width: 320, Height: 180, Quality: 80})
	if err != nil {
		t.Fatal(err)
	}
	if invocation.ExecutableID != ExecutableVips || invocation.ArgProfile != ArgsVipsThumbnail || invocation.InputMode != ToolInputPath {
		t.Fatalf("unexpected invocation: %+v", invocation)
	}
	for _, value := range append(append([]string(nil), invocation.Args...), invocation.Environment...) {
		lower := strings.ToLower(value)
		for _, forbidden := range []string{"sh -c", "http://", "https://", "proxy=", "ld_preload", "caller", "filename"} {
			if strings.Contains(lower, forbidden) {
				t.Fatalf("invocation contains open-ended value %q in %q", forbidden, value)
			}
		}
	}
	if got := strings.Join(invocation.Environment, "\n"); got != "HOME=workspace/home\nLANG=C.UTF-8\nLC_ALL=C.UTF-8\nTZ=UTC" {
		t.Fatalf("environment=%q", got)
	}

	invalid := []ToolParameters{
		{Width: 0, Height: 180, Quality: 80},
		{Width: 320, Height: 180, Quality: 101},
		{Width: 5000, Height: 180, Quality: 80},
	}
	for index, parameters := range invalid {
		if _, err := BuildInvocation(profile, parameters); !errors.Is(err, ErrInvalidInvocation) {
			t.Fatalf("invalid parameters %d error=%v", index, err)
		}
	}
}

func TestBuildInvocationCoversOnlyPinnedExternalToolProfiles(t *testing.T) {
	tests := []struct {
		capability string
		profile    string
		parameters ToolParameters
		executable ExecutableID
		args       ToolArgProfile
		inputMode  ToolInputMode
	}{
		{capabilityspec.CapabilityImageOCR, capabilityspec.ProfileTesseractTextV1, ToolParameters{Language: "eng", MediaType: "image/png"}, ExecutableTesseract, ArgsTesseractOCR, ToolInputPath},
		{capabilityspec.CapabilityDocumentConvert, capabilityspec.ProfileStaticPagesV1, ToolParameters{MediaType: "application/pdf"}, ExecutablePDFToCairo, ArgsPDFPages, ToolInputPath},
		{capabilityspec.CapabilityDocumentConvert, capabilityspec.ProfileStaticPagesV1, ToolParameters{MediaType: "application/vnd.openxmlformats-officedocument.wordprocessingml.document"}, ExecutableLibreOffice, ArgsOfficePDF, ToolInputPath},
		{capabilityspec.CapabilityMalwareScan, capabilityspec.ProfileSignatureScanV1, ToolParameters{MediaType: "application/octet-stream"}, ExecutableClamScan, ArgsClamScan, ToolInputPath},
		{capabilityspec.CapabilityMediaProbe, capabilityspec.ProfileMediaProbeV1, ToolParameters{MediaType: "video/mp4"}, ExecutableFFProbe, ArgsMediaProbe, ToolInputPath},
		{capabilityspec.CapabilityMediaTranscode, capabilityspec.ProfileBrowserPreviewV1, ToolParameters{MediaType: "video/mp4"}, ExecutableFFmpeg, ArgsMediaPreview, ToolInputPath},
	}
	for _, testCase := range tests {
		profile, ok := capabilityspec.Lookup(testCase.capability, testCase.profile, false)
		if !ok {
			t.Fatalf("profile missing: %s/%s", testCase.capability, testCase.profile)
		}
		invocation, err := BuildInvocation(profile, testCase.parameters)
		if err != nil {
			t.Fatalf("%s/%s: %v", testCase.capability, testCase.profile, err)
		}
		if invocation.ExecutableID != testCase.executable || invocation.ArgProfile != testCase.args || invocation.InputMode != testCase.inputMode {
			t.Fatalf("%s/%s invocation=%+v", testCase.capability, testCase.profile, invocation)
		}
		for _, argument := range invocation.Args {
			if strings.Contains(argument, testCase.parameters.MediaType) || strings.Contains(argument, "http") || strings.Contains(argument, "/tmp") {
				t.Fatalf("%s/%s leaked caller media/path into argv: %q", testCase.capability, testCase.profile, argument)
			}
		}
	}
}

func TestDecompressorInvocationsArePipeOnlyClosedAndNetworkFree(t *testing.T) {
	tests := []struct {
		executable ExecutableID
		profile    ToolArgProfile
	}{
		{executable: ExecutableID("gzip"), profile: ToolArgProfile("gzip_decompress_v1")},
		{executable: ExecutableID("xz"), profile: ToolArgProfile("xz_decompress_v1")},
		{executable: ExecutableID("zstd"), profile: ToolArgProfile("zstd_decompress_v1")},
	}
	for _, testCase := range tests {
		invocation := ToolInvocation{
			ExecutableID: testCase.executable, ArgProfile: testCase.profile, InputMode: ToolInputPipe,
			Environment:      []string{"HOME=workspace/home", "LANG=C.UTF-8", "LC_ALL=C.UTF-8", "TZ=UTC"},
			OutputSpec:       ClosedOutputSpec{MaximumFiles: 1, AllowedNames: []string{"stream.tar"}},
			Limits:           ToolLimits{WallTime: time.Minute, MaxInputBytes: 2 << 30, MaxOutputBytes: 8 << 30, MaxProcesses: 4},
			SuccessExitCodes: []int{0},
		}
		if err := invocation.Validate(); err != nil {
			t.Fatalf("%s/%s closed invocation: %v", testCase.executable, testCase.profile, err)
		}
		for _, value := range append(append([]string(nil), invocation.Args...), invocation.Environment...) {
			lower := strings.ToLower(value)
			if strings.Contains(lower, "http") || strings.Contains(lower, "proxy") || strings.Contains(lower, "path=") || strings.Contains(lower, "sh -c") {
				t.Fatalf("decompressor invocation leaked open value %q", value)
			}
		}
	}
}

func TestOfficeInvocationAllowsOnlyDeterministicLibreOfficeOutput(t *testing.T) {
	profile, ok := capabilityspec.Lookup(
		capabilityspec.CapabilityDocumentConvert,
		capabilityspec.ProfileStaticPagesV1,
		false,
	)
	if !ok {
		t.Fatal("document profile missing")
	}
	invocation, err := BuildInvocation(profile, ToolParameters{
		MediaType: "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	})
	if err != nil {
		t.Fatal(err)
	}
	if invocation.OutputSpec.MaximumFiles != 1 || len(invocation.OutputSpec.AllowedNames) != 1 ||
		invocation.OutputSpec.AllowedNames[0] != "input.pdf" {
		t.Fatalf("LibreOffice output allowlist=%+v", invocation.OutputSpec)
	}
}

func TestRunnerBoundsDiagnosticsClearsEnvironmentAndCleansWorkspace(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	tool := writeFakeTool(t, root, `
printf '%s' "${HTTP_PROXY-}${HTTPS_PROXY-}${LD_PRELOAD-}" >&2
i=0
while [ "$i" -lt 20000 ]; do printf x; i=$((i+1)); done
printf result > "$XIRANG_OUTPUT_DIR/result.bin"
`)
	t.Setenv("HTTP_PROXY", "http://FAKE_PROXY_FOR_TEST_ONLY")
	t.Setenv("HTTPS_PROXY", "https://FAKE_PROXY_FOR_TEST_ONLY")
	t.Setenv("LD_PRELOAD", "/FAKE_LIBRARY_FOR_TEST_ONLY")

	runner := newRunnerForTest(RunnerConfig{
		WorkspaceRoot: root,
		StdoutLimit:   16 << 10,
		StderrLimit:   16 << 10,
		GracePeriod:   100 * time.Millisecond,
	}, func(ExecutableID) (string, error) { return tool, nil })
	result, err := runner.Run(context.Background(), ToolInvocation{
		ExecutableID: ExecutableBuiltinText,
		ArgProfile:   ArgsBuiltinText,
		InputMode:    ToolInputPipe,
		OutputSpec:   ClosedOutputSpec{MaximumFiles: 1, AllowedNames: []string{"result.bin"}},
		Limits:       ToolLimits{WallTime: time.Second, MaxInputBytes: 1024, MaxOutputBytes: 1024, MaxProcesses: 4},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Stdout) != 16<<10 || !result.StdoutTruncated {
		t.Fatalf("stdout len=%d truncated=%v", len(result.Stdout), result.StdoutTruncated)
	}
	if strings.Contains(result.Stderr, "FAKE_PROXY") || strings.Contains(result.Stderr, "FAKE_LIBRARY") {
		t.Fatalf("inherited environment leaked: %q", result.Stderr)
	}
	if len(result.Outputs) != 1 || string(result.Outputs["result.bin"]) != "result" {
		t.Fatalf("unexpected outputs: %#v", result.Outputs)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(tool) {
		t.Fatalf("job workspace not cleaned: %v", entries)
	}
}

func TestRunnerStreamsOrMaterializesBoundedImmutableInput(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	tool := writeFakeTool(t, root, `
if [ "${XIRANG_INPUT_MODE-}" = "pipe" ]; then
  cat > "$XIRANG_OUTPUT_DIR/result.bin"
else
  test -f "$XIRANG_INPUT_PATH"
  test ! -w "$XIRANG_INPUT_PATH"
  cat "$XIRANG_INPUT_PATH" > "$XIRANG_OUTPUT_DIR/result.bin"
fi
`)
	runner := newRunnerForTest(RunnerConfig{
		WorkspaceRoot: root,
		StdoutLimit:   16 << 10,
		StderrLimit:   16 << 10,
		GracePeriod:   100 * time.Millisecond,
	}, func(ExecutableID) (string, error) { return tool, nil })
	source := []byte("immutable-input")
	original := append([]byte(nil), source...)
	for _, mode := range []ToolInputMode{ToolInputPipe, ToolInputPath} {
		invocation := ToolInvocation{
			ExecutableID: ExecutableBuiltinText,
			ArgProfile:   ArgsBuiltinText,
			InputMode:    mode,
			OutputSpec:   ClosedOutputSpec{MaximumFiles: 1, AllowedNames: []string{"result.bin"}},
			Limits: ToolLimits{
				WallTime: time.Second, MaxInputBytes: int64(len(source)), MaxOutputBytes: 1024, MaxProcesses: 4,
			},
		}
		result, err := runner.RunInput(context.Background(), invocation, bytes.NewReader(source))
		if err != nil {
			t.Fatalf("mode %s: %v", mode, err)
		}
		if !bytes.Equal(result.Outputs["result.bin"], source) || !bytes.Equal(source, original) {
			t.Fatalf("mode %s changed source or output: %q / %q", mode, source, result.Outputs["result.bin"])
		}
	}

	invocation := ToolInvocation{
		ExecutableID: ExecutableBuiltinText,
		ArgProfile:   ArgsBuiltinText,
		InputMode:    ToolInputPipe,
		OutputSpec:   ClosedOutputSpec{MaximumFiles: 1, AllowedNames: []string{"result.bin"}},
		Limits:       ToolLimits{WallTime: time.Second, MaxInputBytes: int64(len(source) - 1), MaxOutputBytes: 1024, MaxProcesses: 4},
	}
	if _, err := runner.RunInput(context.Background(), invocation, bytes.NewReader(source)); !errors.Is(err, ErrInputLimit) {
		t.Fatalf("oversized stream error=%v", err)
	}
}

func TestRunnerCancellationKillsProcessGroup(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	tool := writeFakeTool(t, root, `
(sleep 30) &
printf child-started
wait
`)
	runner := newRunnerForTest(RunnerConfig{
		WorkspaceRoot: root,
		StdoutLimit:   16 << 10,
		StderrLimit:   16 << 10,
		GracePeriod:   50 * time.Millisecond,
	}, func(ExecutableID) (string, error) { return tool, nil })
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := runner.Run(ctx, ToolInvocation{
		ExecutableID: ExecutableBuiltinText,
		ArgProfile:   ArgsBuiltinText,
		InputMode:    ToolInputPipe,
		OutputSpec:   ClosedOutputSpec{MaximumFiles: 1, AllowedNames: []string{"result.bin"}},
		Limits:       ToolLimits{WallTime: time.Second, MaxInputBytes: 1024, MaxOutputBytes: 1024, MaxProcesses: 4},
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancellation error=%v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("process tree was not joined promptly: %s", elapsed)
	}
}

func TestRunnerStreamsToolStdoutToConsumerAndJoinsOnCancellation(t *testing.T) {
	type inputStreamer interface {
		RunInputStream(context.Context, ToolInvocation, io.Reader, func(io.Reader) error) (ToolResult, error)
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	tool := writeFakeTool(t, root, `
cat
`)
	runner := newRunnerForTest(RunnerConfig{
		WorkspaceRoot: root, StdoutLimit: 16 << 10, StderrLimit: 16 << 10, GracePeriod: 50 * time.Millisecond,
	}, func(ExecutableID) (string, error) { return tool, nil })
	streamer, ok := any(runner).(inputStreamer)
	if !ok {
		t.Fatal("production runner has no bounded streaming stdout contract")
	}
	invocation := ToolInvocation{
		ExecutableID: ExecutableBuiltinText, ArgProfile: ArgsBuiltinText, InputMode: ToolInputPipe,
		OutputSpec: ClosedOutputSpec{MaximumFiles: 1, AllowedNames: []string{"stream.tar"}},
		Limits:     ToolLimits{WallTime: time.Second, MaxInputBytes: 1024, MaxOutputBytes: 1024, MaxProcesses: 4},
	}
	var consumed []byte
	result, err := streamer.RunInputStream(context.Background(), invocation, strings.NewReader("streamed"), func(source io.Reader) error {
		var readErr error
		consumed, readErr = io.ReadAll(source)
		return readErr
	})
	if err != nil || string(consumed) != "streamed" || result.Stdout != "" || result.StdoutTruncated {
		t.Fatalf("stream result=%+v consumed=%q err=%v", result, consumed, err)
	}

	blocking := writeFakeTool(t, root, `
(sleep 30) &
wait
`)
	runner = newRunnerForTest(RunnerConfig{
		WorkspaceRoot: root, StdoutLimit: 16 << 10, StderrLimit: 16 << 10, GracePeriod: 50 * time.Millisecond,
	}, func(ExecutableID) (string, error) { return blocking, nil })
	streamer = any(runner).(inputStreamer)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = streamer.RunInputStream(ctx, invocation, strings.NewReader("streamed"), func(source io.Reader) error {
		_, copyErr := io.Copy(io.Discard, source)
		return copyErr
	})
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > time.Second {
		t.Fatalf("stream cancellation err=%v elapsed=%s", err, time.Since(started))
	}
}

func TestRunnerKillsProcessGroupAfterToolLeaderExits(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "escaped-descendant")
	tool := writeFakeTool(t, root, fmt.Sprintf(`
trap '' HUP TERM
(
  trap '' HUP TERM
  sleep 0.2
  printf escaped > %q
) >/dev/null 2>&1 &
printf result > "$XIRANG_OUTPUT_DIR/result.bin"
`, marker))
	runner := newRunnerForTest(RunnerConfig{
		WorkspaceRoot: root,
		StdoutLimit:   16 << 10,
		StderrLimit:   16 << 10,
		GracePeriod:   50 * time.Millisecond,
	}, func(ExecutableID) (string, error) { return tool, nil })
	result, err := runner.Run(context.Background(), ToolInvocation{
		ExecutableID: ExecutableBuiltinText,
		ArgProfile:   ArgsBuiltinText,
		InputMode:    ToolInputPipe,
		OutputSpec:   ClosedOutputSpec{MaximumFiles: 1, AllowedNames: []string{"result.bin"}},
		Limits:       ToolLimits{WallTime: time.Second, MaxInputBytes: 1024, MaxOutputBytes: 1024, MaxProcesses: 4},
	})
	if err != nil || string(result.Outputs["result.bin"]) != "result" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	time.Sleep(400 * time.Millisecond)
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("tool descendant survived successful leader exit: %v", err)
	}
}

func TestRunnerTreatsOnlyClosedNonzeroExitAsSuccessfulResult(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	tool := writeFakeTool(t, root, `
printf 'finding marker'
exit 1
`)
	runner := newRunnerForTest(RunnerConfig{
		WorkspaceRoot: root, StdoutLimit: 16 << 10, StderrLimit: 16 << 10, GracePeriod: 100 * time.Millisecond,
	}, func(ExecutableID) (string, error) { return tool, nil })
	invocation := ToolInvocation{
		ExecutableID: ExecutableClamScan, ArgProfile: ArgsClamScan, InputMode: ToolInputPath,
		OutputSpec:       ClosedOutputSpec{MaximumFiles: 1, AllowedNames: []string{"metadata.json"}},
		Limits:           ToolLimits{WallTime: time.Second, MaxInputBytes: 1024, MaxOutputBytes: 1024, MaxProcesses: 4},
		SuccessExitCodes: []int{0, 1},
	}
	result, err := runner.RunInput(context.Background(), invocation, strings.NewReader("sample"))
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 1 || result.Stdout != "finding marker" {
		t.Fatalf("closed positive result=%+v", result)
	}
	invocation.SuccessExitCodes = []int{0}
	if _, err := runner.RunInput(context.Background(), invocation, strings.NewReader("sample")); !errors.Is(err, ErrToolFailed) {
		t.Fatalf("unapproved nonzero exit error=%v", err)
	}
}

func TestProductionRunnerRejectsOrdinaryDiskWorkspace(t *testing.T) {
	_, err := NewRunner(RunnerConfig{
		WorkspaceRoot: t.TempDir(), StdoutLimit: 16 << 10, StderrLimit: 16 << 10, GracePeriod: time.Second,
	})
	if !errors.Is(err, ErrSecureWorkspaceUnavailable) {
		t.Fatalf("ordinary disk workspace error=%v", err)
	}
}

func TestRunnerStartupCleanupRemovesOnlyPrivateOrphanWorkspaces(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	orphan := filepath.Join(root, "job-orphan")
	keep := filepath.Join(root, "operator-data")
	if err := os.Mkdir(orphan, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(orphan, "input.bin"), []byte("private"), 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(keep, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := cleanupOrphanWorkspaces(root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(orphan); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphan workspace remains: %v", err)
	}
	if info, err := os.Stat(keep); err != nil || !info.IsDir() {
		t.Fatalf("unrelated directory was removed: info=%v err=%v", info, err)
	}
}

func writeFakeTool(t *testing.T, root, body string) string {
	t.Helper()
	path := filepath.Join(root, "fake-tool")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nset -eu\n"+body), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}
