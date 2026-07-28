package capabilities

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset/processing/capabilityspec"
)

type smokeResourceLimits struct {
	cpuSeconds int
	memory     int64
	file       int64
	processes  int
}

type smokeInvocation struct {
	executable ExecutableID
	profile    ToolArgProfile
	inputMode  ToolInputMode
	args       []string
	limits     smokeResourceLimits
	line       int
}

func TestAssetWorkerSmokeInvocationsMatchBuildInvocationResources(t *testing.T) {
	path := os.Getenv("ASSET_WORKER_SMOKE_PATH")
	if path == "" {
		_, filename, _, ok := runtime.Caller(0)
		if !ok {
			t.Fatal("runtime.Caller failed")
		}
		root := filepath.Dir(filename)
		for range 5 {
			root = filepath.Dir(root)
		}
		path = filepath.Join(root, "scripts", "test-asset-worker.sh")
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read Worker smoke script: %v", err)
	}
	got, err := parseSmokeInvocations(string(payload))
	if err != nil {
		t.Fatal(err)
	}
	want := smokeInvocationExpectations(t)
	if len(got) != len(want) {
		t.Fatalf("Worker smoke invocation count=%d, want %d: %+v", len(got), len(want), got)
	}
	for index, expected := range want {
		if got[index].executable != expected.executable || got[index].profile != expected.profile || got[index].inputMode != expected.inputMode {
			t.Fatalf("Worker smoke invocation %d=%s/%s/%s, want %s/%s/%s", index+1, got[index].executable, got[index].profile, got[index].inputMode, expected.executable, expected.profile, expected.inputMode)
		}
		if !slices.Equal(got[index].args, expected.args) {
			t.Fatalf("Worker smoke invocation %d %s/%s args=%v, want %v (line %d)", index+1, got[index].executable, got[index].profile, got[index].args, expected.args, got[index].line)
		}
		if got[index].limits != expected.limits {
			t.Fatalf("Worker smoke invocation %d %s/%s limits=%+v, want %+v (line %d)", index+1, got[index].executable, got[index].profile, got[index].limits, expected.limits, got[index].line)
		}
	}
}

const (
	smokeInvocationFieldCount  = 11
	smokeInvocationRegionBegin = "# ASSET_TOOL_SMOKE_INVOCATIONS_BEGIN"
	smokeInvocationRegionEnd   = "# ASSET_TOOL_SMOKE_INVOCATIONS_END"
)

func TestParseSmokeInvocationsCountsOnlyExactCallsInsideRegion(t *testing.T) {
	valid := `run_asset_tool vips vips_thumbnail_v1 90 1073741824 8388608 16 path "$job/input.bin" "$job/home" "$job/output" --width=16`
	source := strings.Join([]string{
		"cat <<'GO'",
		valid,
		"GO",
		smokeInvocationRegionBegin,
		"# " + valid,
		"prefix " + valid,
		strings.Replace(valid, "run_asset_tool", "run_asset_tool_decoy", 1),
		valid,
		smokeInvocationRegionEnd,
		valid,
	}, "\n")

	got, err := parseSmokeInvocations(source)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].executable != ExecutableVips || got[0].line != 8 {
		t.Fatalf("structured smoke invocations=%+v", got)
	}
}

func TestParseSmokeInvocationsRejectsMalformedStructuredSyntax(t *testing.T) {
	valid := `run_asset_tool vips vips_thumbnail_v1 90 1073741824 8388608 16 path "$job/input.bin" "$job/home" "$job/output" --width=16`
	wrap := func(call string) string {
		return smokeInvocationRegionBegin + "\n" + call + "\n" + smokeInvocationRegionEnd
	}
	tests := []struct {
		name   string
		source string
	}{
		{name: "missing begin", source: valid + "\n" + smokeInvocationRegionEnd},
		{name: "missing end", source: smokeInvocationRegionBegin + "\n" + valid},
		{name: "nested begin", source: smokeInvocationRegionBegin + "\n" + smokeInvocationRegionBegin + "\n" + valid + "\n" + smokeInvocationRegionEnd},
		{name: "too few fields", source: wrap(`run_asset_tool vips vips_thumbnail_v1 90`)},
		{name: "zero numeric", source: wrap(strings.Replace(valid, " 90 ", " 0 ", 1))},
		{name: "noncanonical numeric", source: wrap(strings.Replace(valid, " 90 ", " 090 ", 1))},
		{name: "positive numeric", source: wrap(strings.Replace(valid, " 90 ", " +90 ", 1))},
		{name: "numeric overflow", source: wrap(strings.Replace(valid, " 90 ", " 999999999999999999999 ", 1))},
		{name: "invalid mode", source: wrap(strings.Replace(valid, " path ", " stream ", 1))},
		{name: "pipe path", source: wrap(strings.Replace(valid, ` path "$job/input.bin" `, ` pipe "$job/input.bin" `, 1))},
		{name: "path sentinel", source: wrap(strings.Replace(valid, ` path "$job/input.bin" `, " path - ", 1))},
		{name: "empty input", source: wrap(strings.Replace(valid, `"$job/input.bin"`, `""`, 1))},
		{name: "single quoted input", source: wrap(strings.Replace(valid, `"$job/input.bin"`, `'$job/input.bin'`, 1))},
		{name: "bare input", source: wrap(strings.Replace(valid, `"$job/input.bin"`, `$job/input.bin`, 1))},
		{name: "multiple quoted paths", source: wrap(strings.Replace(valid, `"$job/home"`, `"$job/extra" "$job/home"`, 1))},
		{name: "control path", source: wrap(strings.Replace(valid, `"$job/input.bin"`, `"$job/in;put.bin"`, 1))},
		{name: "or suffix", source: wrap(valid + " || true")},
		{name: "and suffix", source: wrap(valid + " && true")},
		{name: "semicolon suffix", source: wrap(valid + " ; true")},
		{name: "pipe suffix", source: wrap(valid + " | cat")},
		{name: "unexpected redirect", source: wrap(valid + ` >"$job/other"`)},
		{name: "command substitution", source: wrap(valid + ` --option=$(true)`)},
		{name: "backticks", source: wrap(valid + " --option=`true`")},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if got, err := parseSmokeInvocations(testCase.source); err == nil {
				t.Fatalf("malformed structured smoke accepted: %+v", got)
			}
		})
	}
}

func parseSmokeInvocations(source string) ([]smokeInvocation, error) {
	lines := strings.Split(source, "\n")
	result := make([]smokeInvocation, 0, 14)
	insideRegion := false
	seenRegionBegin := false
	seenRegionEnd := false
	for index, line := range lines {
		switch strings.TrimSpace(line) {
		case smokeInvocationRegionBegin:
			if seenRegionBegin || insideRegion || seenRegionEnd {
				return nil, fmt.Errorf("duplicate or misplaced smoke invocation region begin on line %d", index+1)
			}
			seenRegionBegin = true
			insideRegion = true
			continue
		case smokeInvocationRegionEnd:
			if !insideRegion || seenRegionEnd {
				return nil, fmt.Errorf("smoke invocation region end without begin on line %d", index+1)
			}
			seenRegionEnd = true
			insideRegion = false
			continue
		}
		if !insideRegion {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != "run_asset_tool" {
			continue
		}
		if len(fields) < smokeInvocationFieldCount {
			return nil, fmt.Errorf("malformed run_asset_tool invocation on line %d: got %d fields, want at least %d", index+1, len(fields), smokeInvocationFieldCount)
		}

		cpuSeconds, err := parseSmokePositiveInteger("CPU seconds", fields[3], 32)
		if err != nil {
			return nil, fmt.Errorf("run_asset_tool on line %d: %w", index+1, err)
		}
		memoryBytes, err := parseSmokePositiveInteger("memory bytes", fields[4], 64)
		if err != nil {
			return nil, fmt.Errorf("run_asset_tool on line %d: %w", index+1, err)
		}
		fileBytes, err := parseSmokePositiveInteger("file bytes", fields[5], 64)
		if err != nil {
			return nil, fmt.Errorf("run_asset_tool on line %d: %w", index+1, err)
		}
		processes, err := parseSmokePositiveInteger("processes", fields[6], 32)
		if err != nil {
			return nil, fmt.Errorf("run_asset_tool on line %d: %w", index+1, err)
		}

		inputMode := ToolInputMode(fields[7])
		inputVariable := ""
		switch inputMode {
		case ToolInputPath:
			var ok bool
			inputVariable, ok = parseSmokePathArgument(fields[8], "input.bin")
			if !ok {
				return nil, fmt.Errorf("run_asset_tool on line %d: invalid path input %q", index+1, fields[8])
			}
		case ToolInputPipe:
			if fields[8] != "-" {
				return nil, fmt.Errorf("run_asset_tool on line %d: pipe input must be -, got %q", index+1, fields[8])
			}
		default:
			return nil, fmt.Errorf("run_asset_tool on line %d: invalid input mode %q", index+1, fields[7])
		}
		homeVariable, ok := parseSmokePathArgument(fields[9], "home")
		if !ok {
			return nil, fmt.Errorf("run_asset_tool on line %d: invalid home %q", index+1, fields[9])
		}
		outputVariable, ok := parseSmokePathArgument(fields[10], "output")
		if !ok {
			return nil, fmt.Errorf("run_asset_tool on line %d: invalid output %q", index+1, fields[10])
		}
		if homeVariable != outputVariable || (inputMode == ToolInputPath && inputVariable != homeVariable) {
			return nil, fmt.Errorf("run_asset_tool on line %d: input, home, and output variables do not match", index+1)
		}
		args, err := parseSmokeInvocationSuffix(fields[smokeInvocationFieldCount:])
		if err != nil {
			return nil, fmt.Errorf("run_asset_tool on line %d: %w", index+1, err)
		}
		result = append(result, smokeInvocation{
			executable: ExecutableID(fields[1]), profile: ToolArgProfile(fields[2]), inputMode: inputMode,
			args: args,
			limits: smokeResourceLimits{
				cpuSeconds: int(cpuSeconds), memory: memoryBytes, file: fileBytes, processes: int(processes),
			},
			line: index + 1,
		})
	}
	if !seenRegionBegin || !seenRegionEnd || insideRegion {
		return nil, errors.New("missing complete smoke invocation region")
	}
	return result, nil
}

func parseSmokePositiveInteger(name, value string, bitSize int) (int64, error) {
	parsed, err := strconv.ParseInt(value, 10, bitSize)
	if err != nil || parsed <= 0 || strconv.FormatInt(parsed, 10) != value {
		return 0, fmt.Errorf("invalid %s %q", name, value)
	}
	return parsed, nil
}

func parseSmokePathArgument(value, leaf string) (string, bool) {
	if len(value) < 6 || !strings.HasPrefix(value, `"$`) || !strings.HasSuffix(value, `"`) {
		return "", false
	}
	body := value[2 : len(value)-1]
	separator := strings.IndexByte(body, '/')
	if separator <= 0 || body[separator+1:] != leaf {
		return "", false
	}
	variable := body[:separator]
	if !validSmokeShellIdentifier(variable) {
		return "", false
	}
	return variable, true
}

func validSmokeShellIdentifier(value string) bool {
	for index, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || character == '_' ||
			(index > 0 && character >= '0' && character <= '9') {
			continue
		}
		return false
	}
	return value != ""
}

func parseSmokeInvocationSuffix(fields []string) ([]string, error) {
	seenRedirection := false
	args := make([]string, 0, len(fields))
	for _, field := range fields {
		if validSmokeRedirection(field) {
			seenRedirection = true
			continue
		}
		if seenRedirection {
			return nil, fmt.Errorf("tool argument %q follows redirection", field)
		}
		if strings.ContainsAny(field, "|&;<>#`()\\") || strings.Contains(field, "$(") {
			return nil, fmt.Errorf("shell control token %q", field)
		}
		args = append(args, field)
	}
	return args, nil
}

func validSmokeRedirection(value string) bool {
	if value == ">/dev/null" || value == "2>&1" {
		return true
	}
	if strings.HasPrefix(value, "2>") {
		variable, ok := parseSmokeVariableArgument(value[2:])
		return ok && variable == "media_probe_stderr"
	}
	if strings.HasPrefix(value, ">") {
		target := value[1:]
		if variable, ok := parseSmokeVariableArgument(target); ok {
			return variable == "media_probe_stdout"
		}
		for _, leaf := range []string{"gzip.tar", "xz.tar", "zstd.tar"} {
			if _, ok := parseSmokePathArgument(target, leaf); ok {
				return true
			}
		}
		return false
	}
	if strings.HasPrefix(value, "<") {
		target := value[1:]
		for _, leaf := range []string{"archive.tar.gz", "archive.tar.xz", "archive.tar.zst"} {
			if _, ok := parseSmokePathArgument(target, leaf); ok {
				return true
			}
		}
	}
	return false
}

func parseSmokeVariableArgument(value string) (string, bool) {
	if len(value) < 4 || !strings.HasPrefix(value, `"$`) || !strings.HasSuffix(value, `"`) {
		return "", false
	}
	variable := value[2 : len(value)-1]
	return variable, validSmokeShellIdentifier(variable)
}

func smokeInvocationExpectations(t *testing.T) []smokeInvocation {
	t.Helper()
	type expectation struct {
		executable ExecutableID
		profile    ToolArgProfile
		build      func() (ToolInvocation, error)
	}
	lookup := func(capability, profile string, parameters ToolParameters) func() (ToolInvocation, error) {
		return func() (ToolInvocation, error) {
			value, ok := capabilityspec.Lookup(capability, profile, false)
			if !ok {
				return ToolInvocation{}, fmt.Errorf("profile missing: %s/%s", capability, profile)
			}
			return BuildInvocation(value, parameters)
		}
	}
	archive := func(capability, profile, mediaType string) func() (ToolInvocation, error) {
		return func() (ToolInvocation, error) {
			value, ok := capabilityspec.Lookup(capability, profile, false)
			if !ok {
				return ToolInvocation{}, fmt.Errorf("profile missing: %s/%s", capability, profile)
			}
			return BuildArchiveDecompressInvocation(value, mediaType)
		}
	}
	pdfText := func() (ToolInvocation, error) {
		value, ok := capabilityspec.Lookup(capabilityspec.CapabilityDocumentConvert, capabilityspec.ProfileStaticPagesV1, false)
		if !ok {
			return ToolInvocation{}, errors.New("PDF profile missing")
		}
		return BuildPDFTextInvocation(value)
	}
	cases := []expectation{
		{ExecutableFFProbe, ArgsMediaProbe, lookup(capabilityspec.CapabilityMediaProbe, capabilityspec.ProfileMediaProbeV1, ToolParameters{MediaType: "video/mp4"})},
		{ExecutableVips, ArgsVipsThumbnail, lookup(capabilityspec.CapabilityImageThumbnail, capabilityspec.ProfileRasterThumbnailV1, ToolParameters{Width: 16, Height: 16, Quality: 80})},
		{ExecutableTesseract, ArgsTesseractOCR, lookup(capabilityspec.CapabilityImageOCR, capabilityspec.ProfileTesseractTextV1, ToolParameters{Language: "eng", MediaType: "image/png"})},
		{ExecutablePDFToCairo, ArgsPDFPages, lookup(capabilityspec.CapabilityDocumentConvert, capabilityspec.ProfileStaticPagesV1, ToolParameters{MediaType: "application/pdf"})},
		{ExecutablePDFToText, ArgsPDFText, pdfText},
		{ExecutableLibreOffice, ArgsOfficePDF, lookup(capabilityspec.CapabilityDocumentConvert, capabilityspec.ProfileStaticPagesV1, ToolParameters{MediaType: "application/vnd.oasis.opendocument.text"})},
		{ExecutableLibreOffice, ArgsOfficePDF, lookup(capabilityspec.CapabilityDocumentConvert, capabilityspec.ProfileStaticPagesV1, ToolParameters{MediaType: "application/vnd.openxmlformats-officedocument.wordprocessingml.document"})},
		{ExecutableClamScan, ArgsClamScan, lookup(capabilityspec.CapabilityMalwareScan, capabilityspec.ProfileSignatureScanV1, ToolParameters{MediaType: "application/octet-stream"})},
		{ExecutableClamScan, ArgsClamScan, lookup(capabilityspec.CapabilityMalwareScan, capabilityspec.ProfileSignatureScanV1, ToolParameters{MediaType: "application/octet-stream"})},
		{ExecutableClamScan, ArgsClamScan, lookup(capabilityspec.CapabilityMalwareScan, capabilityspec.ProfileSignatureScanV1, ToolParameters{MediaType: "application/octet-stream"})},
		{ExecutableFFmpeg, ArgsMediaPreview, lookup(capabilityspec.CapabilityMediaTranscode, capabilityspec.ProfileBrowserPreviewV1, ToolParameters{MediaType: "video/mp4"})},
		{ExecutableGzip, ArgsGzipDecompress, archive(capabilityspec.CapabilityArchiveInspect, capabilityspec.ProfileArchiveIndexV1, "application/gzip")},
		{ExecutableXZ, ArgsXZDecompress, archive(capabilityspec.CapabilityArchiveInspect, capabilityspec.ProfileArchiveIndexV1, "application/x-xz")},
		{ExecutableZstd, ArgsZstdDecompress, archive(capabilityspec.CapabilityArchiveInspect, capabilityspec.ProfileArchiveIndexV1, "application/zstd")},
	}
	result := make([]smokeInvocation, 0, len(cases))
	for _, testCase := range cases {
		invocation, err := testCase.build()
		if err != nil {
			t.Fatalf("build %s/%s: %v", testCase.executable, testCase.profile, err)
		}
		result = append(result, smokeInvocation{
			executable: testCase.executable,
			profile:    testCase.profile,
			inputMode:  invocation.InputMode,
			args:       append([]string(nil), invocation.Args...),
			limits: smokeResourceLimits{
				cpuSeconds: int(invocation.Limits.CPUTime / time.Second),
				memory:     invocation.Limits.MaxMemoryBytes, file: invocation.Limits.MaxFileBytes,
				processes: invocation.Limits.MaxProcesses,
			},
		})
	}
	return result
}

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

func TestBuildInvocationCarriesClosedProfileSandboxResourceLimits(t *testing.T) {
	tests := []struct {
		name          string
		capability    string
		profile       string
		parameters    ToolParameters
		wantMemory    int64
		wantFileBytes int64
		wantProcesses int
	}{
		{
			name: "thumbnail", capability: capabilityspec.CapabilityImageThumbnail,
			profile:    capabilityspec.ProfileRasterThumbnailV1,
			parameters: ToolParameters{Width: 320, Height: 180, Quality: 80},
			wantMemory: 1 << 30, wantFileBytes: 8 << 20, wantProcesses: 16,
		},
		{
			name: "document", capability: capabilityspec.CapabilityDocumentConvert,
			profile:    capabilityspec.ProfileStaticPagesV1,
			parameters: ToolParameters{MediaType: "application/pdf"},
			wantMemory: 2 << 30, wantFileBytes: 64 << 20, wantProcesses: 32,
		},
		{
			name: "malware scratch", capability: capabilityspec.CapabilityMalwareScan,
			profile:    capabilityspec.ProfileSignatureScanV1,
			parameters: ToolParameters{MediaType: "application/octet-stream"},
			wantMemory: 2 << 30, wantFileBytes: 1 << 30, wantProcesses: 16,
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			profile, ok := capabilityspec.Lookup(testCase.capability, testCase.profile, false)
			if !ok {
				t.Fatal("profile missing")
			}
			invocation, err := BuildInvocation(profile, testCase.parameters)
			if err != nil {
				t.Fatal(err)
			}
			if invocation.Limits.CPUTime != profile.Limits.WallTime ||
				invocation.Limits.MaxMemoryBytes != testCase.wantMemory ||
				invocation.Limits.MaxFileBytes != testCase.wantFileBytes ||
				invocation.Limits.MaxProcesses != testCase.wantProcesses {
				t.Fatalf("profile sandbox limits=%+v", invocation.Limits)
			}
			invalid := invocation
			invalid.Limits.MaxProcesses++
			if err := invalid.Validate(); !errors.Is(err, ErrInvalidInvocation) {
				t.Fatalf("profile process ceiling error=%v", err)
			}
		})
	}

	profile, ok := capabilityspec.Lookup(
		capabilityspec.CapabilityArchiveInspect,
		capabilityspec.ProfileArchiveIndexV1,
		false,
	)
	if !ok {
		t.Fatal("archive profile missing")
	}
	invocation, err := BuildArchiveDecompressInvocation(profile, "application/gzip")
	if err != nil {
		t.Fatal(err)
	}
	if invocation.Limits.CPUTime != profile.Limits.WallTime ||
		invocation.Limits.MaxMemoryBytes != 1<<30 ||
		invocation.Limits.MaxFileBytes != profile.Limits.MaxOutputBytes ||
		invocation.Limits.MaxProcesses != 4 {
		t.Fatalf("archive sandbox limits=%+v", invocation.Limits)
	}
}

func TestRuntimeEnvironmentPropagatesOnlyClosedSandboxResourceLimits(t *testing.T) {
	limits := ToolLimits{
		WallTime: 90 * time.Second, CPUTime: 45 * time.Second,
		MaxInputBytes: 256 << 20, MaxOutputBytes: 8 << 20,
		MaxMemoryBytes: 1 << 30, MaxFileBytes: 8 << 20, MaxProcesses: 16,
	}
	values := runtimeEnvironment(
		[]string{"HOME=workspace/home", "LANG=C.UTF-8", "LC_ALL=C.UTF-8", "TZ=UTC"},
		"/run/xirang/asset-jobs/job-opaque",
		"/run/xirang/asset-jobs/job-opaque/output",
		ToolInputPath,
		"/run/xirang/asset-jobs/job-opaque/input.bin",
		limits,
	)
	joined := strings.Join(values, "\n")
	for _, expected := range []string{
		"XIRANG_RLIMIT_CPU_SECONDS=45",
		"XIRANG_RLIMIT_MEMORY_BYTES=1073741824",
		"XIRANG_RLIMIT_FSIZE_BYTES=8388608",
		"XIRANG_RLIMIT_PROCESSES=16",
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("runtime environment missing %q: %q", expected, joined)
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
			Limits:           testToolLimits(time.Minute, 2<<30, 8<<30),
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

func TestPDFInvocationsUseNumericPageAndDedicatedTextAllowlists(t *testing.T) {
	profile, ok := capabilityspec.Lookup(
		capabilityspec.CapabilityDocumentConvert,
		capabilityspec.ProfileStaticPagesV1,
		false,
	)
	if !ok {
		t.Fatal("document profile missing")
	}

	pageInvocation, err := BuildInvocation(profile, ToolParameters{MediaType: "application/pdf"})
	if err != nil {
		t.Fatal(err)
	}
	wantPages := make([]string, 0, 30)
	for page := 1; page <= 30; page++ {
		wantPages = append(wantPages, fmt.Sprintf("page-%d.png", page))
	}
	if pageInvocation.ExecutableID != ExecutablePDFToCairo || pageInvocation.ArgProfile != ArgsPDFPages ||
		pageInvocation.OutputSpec.MaximumFiles != 30 || !slices.Equal(pageInvocation.OutputSpec.AllowedNames, wantPages) {
		t.Fatalf("PDF page output allowlist=%+v", pageInvocation.OutputSpec)
	}

	textInvocation, err := BuildPDFTextInvocation(profile)
	if err != nil {
		t.Fatal(err)
	}
	if textInvocation.ExecutableID != ExecutablePDFToText || textInvocation.ArgProfile != ArgsPDFText ||
		textInvocation.InputMode != ToolInputPath || textInvocation.OutputSpec.MaximumFiles != 1 ||
		!slices.Equal(textInvocation.OutputSpec.AllowedNames, []string{"content.txt"}) {
		t.Fatalf("PDF text invocation=%+v", textInvocation)
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
		Limits:       testToolLimits(time.Second, 1024, 1024),
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
			Limits:       testToolLimits(time.Second, int64(len(source)), 1024),
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
		Limits:       testToolLimits(time.Second, int64(len(source)-1), 1024),
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
		Limits:       testToolLimits(time.Second, 1024, 1024),
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
		Limits:     testToolLimits(time.Second, 1024, 1024),
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

	consumed = nil
	result, err = streamer.RunInputStream(context.Background(), invocation, strings.NewReader("delayed"), func(source io.Reader) error {
		time.Sleep(250 * time.Millisecond)
		var readErr error
		consumed, readErr = io.ReadAll(source)
		return readErr
	})
	if err != nil || string(consumed) != "delayed" || result.Stdout != "" || result.StdoutTruncated {
		t.Fatalf("delayed stream result=%+v consumed=%q err=%v", result, consumed, err)
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
		Limits:       testToolLimits(time.Second, 1024, 1024),
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
printf '%s: Finding.Marker FOUND\n' "$XIRANG_INPUT_PATH"
exit 1
`)
	runner := newRunnerForTest(RunnerConfig{
		WorkspaceRoot: root, StdoutLimit: 16 << 10, StderrLimit: 16 << 10, GracePeriod: 100 * time.Millisecond,
	}, func(ExecutableID) (string, error) { return tool, nil })
	invocation := ToolInvocation{
		ExecutableID: ExecutableClamScan, ArgProfile: ArgsClamScan, InputMode: ToolInputPath,
		OutputSpec:       ClosedOutputSpec{MaximumFiles: 1, AllowedNames: []string{"metadata.json"}},
		Limits:           testToolLimits(time.Second, 1024, 1024),
		SuccessExitCodes: []int{0, 1},
	}
	result, err := runner.RunInput(context.Background(), invocation, strings.NewReader("sample"))
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 1 || result.Stdout != "input.bin: Finding.Marker FOUND\n" {
		t.Fatalf("closed positive result=%+v", result)
	}
	invocation.SuccessExitCodes = []int{0}
	if _, err := runner.RunInput(context.Background(), invocation, strings.NewReader("sample")); !errors.Is(err, ErrToolFailed) {
		t.Fatalf("unapproved nonzero exit error=%v", err)
	}
}

func TestRunnerCanonicalizesOnlyExactClamFindingOutput(t *testing.T) {
	tests := []struct {
		name       string
		toolBody   string
		wantError  error
		wantOutput string
	}{
		{
			name:       "exact finding",
			toolBody:   "printf '%s: Test.Signature FOUND\\n' \"$XIRANG_INPUT_PATH\"\nexit 1\n",
			wantOutput: "input.bin: Test.Signature FOUND\n",
		},
		{
			name:      "wrong path",
			toolBody:  "printf '/wrong/input.bin: Test.Signature FOUND\\n'\nexit 1\n",
			wantError: ErrInvalidToolOutput,
		},
		{
			name:      "multiple lines",
			toolBody:  "printf '%s: First.Signature FOUND\\n%s: Second.Signature FOUND\\n' \"$XIRANG_INPUT_PATH\" \"$XIRANG_INPUT_PATH\"\nexit 1\n",
			wantError: ErrInvalidToolOutput,
		},
		{
			name:      "control byte",
			toolBody:  "printf '%s: Test\\001Signature FOUND\\n' \"$XIRANG_INPUT_PATH\"\nexit 1\n",
			wantError: ErrInvalidToolOutput,
		},
		{
			name:      "overlong signature",
			toolBody:  "printf '%s: " + strings.Repeat("A", 129) + " FOUND\\n' \"$XIRANG_INPUT_PATH\"\nexit 1\n",
			wantError: ErrInvalidToolOutput,
		},
		{
			name:      "truncated output",
			toolBody:  "printf '%s: " + strings.Repeat("A", 17<<10) + " FOUND\\n' \"$XIRANG_INPUT_PATH\"\nexit 1\n",
			wantError: ErrInvalidToolOutput,
		},
		{
			name:      "stderr",
			toolBody:  "printf '%s: Test.Signature FOUND\\n' \"$XIRANG_INPUT_PATH\"\nprintf 'diagnostic' >&2\nexit 1\n",
			wantError: ErrInvalidToolOutput,
		},
		{
			name:      "unknown state",
			toolBody:  "printf '%s: OK\\n' \"$XIRANG_INPUT_PATH\"\nexit 1\n",
			wantError: ErrInvalidToolOutput,
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Chmod(root, 0o700); err != nil {
				t.Fatal(err)
			}
			tool := writeFakeTool(t, root, testCase.toolBody)
			runner := newRunnerForTest(RunnerConfig{
				WorkspaceRoot: root, StdoutLimit: 16 << 10, StderrLimit: 16 << 10, GracePeriod: 100 * time.Millisecond,
			}, func(ExecutableID) (string, error) { return tool, nil })
			result, err := runner.RunInput(context.Background(), ToolInvocation{
				ExecutableID: ExecutableClamScan, ArgProfile: ArgsClamScan, InputMode: ToolInputPath,
				OutputSpec:       ClosedOutputSpec{MaximumFiles: 1, AllowedNames: []string{"metadata.json"}},
				Limits:           testToolLimits(time.Second, 1024, 1024),
				SuccessExitCodes: []int{0, 1},
			}, strings.NewReader("sample"))
			if testCase.wantError != nil {
				if !errors.Is(err, testCase.wantError) {
					t.Fatalf("Runner error=%v, want %v", err, testCase.wantError)
				}
				return
			}
			if err != nil || result.ExitCode != 1 || result.Stdout != testCase.wantOutput || result.Stderr != "" {
				t.Fatalf("canonical Clam result=%+v err=%v", result, err)
			}
		})
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

func testToolLimits(wallTime time.Duration, maxInputBytes, maxOutputBytes int64) ToolLimits {
	return ToolLimits{
		WallTime: wallTime, CPUTime: wallTime,
		MaxInputBytes: maxInputBytes, MaxOutputBytes: maxOutputBytes,
		MaxMemoryBytes: 512 << 20, MaxFileBytes: min(maxOutputBytes, int64(256<<20)), MaxProcesses: 4,
	}
}
