package anomaly

import (
	"context"
	"math"
	"strings"
	"testing"

	"xirang/backend/internal/model"
)

func TestCountRansomSuffixHits(t *testing.T) {
	tests := []struct {
		name  string
		paths []string
		want  int
	}{
		{name: "normal files only", paths: []string{"doc.txt", "image.png", "data.csv"}, want: 0},
		{name: "single encrypted file", paths: []string{"doc.txt.encrypted"}, want: 1},
		{name: "multiple suffixes", paths: []string{"a.encrypted", "b.locked", "c.crypt"}, want: 3},
		{name: "case insensitivity", paths: []string{"README.ENCRYPTED", "Data.LOCKED"}, want: 2},
		{name: "mixed normal and ransom", paths: []string{"doc.txt", "secret.doc.encrypted", "photo.png"}, want: 1},
		{name: "ransom suffix in middle", paths: []string{".encrypted.backup"}, want: 0}, // only suffix match
		{name: "wannacry variant", paths: []string{"budget.xlsx.wannacry"}, want: 1},
		{name: "multiple .xxx files", paths: []string{"a.xxx", "b.xxx", "c.xxx"}, want: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CountRansomSuffixHits(tt.paths)
			if got != tt.want {
				t.Errorf("CountRansomSuffixHits() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestCountRansomSuffixHitsEmpty(t *testing.T) {
	got := CountRansomSuffixHits(nil)
	if got != 0 {
		t.Errorf("CountRansomSuffixHits(nil) = %d, want 0", got)
	}
	got = CountRansomSuffixHits([]string{})
	if got != 0 {
		t.Errorf("CountRansomSuffixHits([]) = %d, want 0", got)
	}
}

func TestDiffRecordTotalChanges(t *testing.T) {
	r := DiffRecord{AddedCount: 10, RemovedCount: 20, ChangedCount: 30}
	got := r.TotalChanges()
	want := 60
	if got != want {
		t.Errorf("TotalChanges() = %d, want %d", got, want)
	}
}

func TestCalculateBaseline(t *testing.T) {
	records := []DiffRecord{
		{AddedCount: 10, RemovedCount: 5, ChangedCount: 15},  // total=30
		{AddedCount: 20, RemovedCount: 5, ChangedCount: 5},   // total=30
		{AddedCount: 15, RemovedCount: 10, ChangedCount: 5},  // total=30
		{AddedCount: 5, RemovedCount: 5, ChangedCount: 20},   // total=30
		{AddedCount: 10, RemovedCount: 10, ChangedCount: 10}, // total=30
	}
	// All records have total=30 → mean=30, stddev=0
	baseline := CalculateBaseline(records, 3)
	if baseline == nil {
		t.Fatal("expected non-nil baseline")
		return
	}
	b := *baseline
	if b.Mean != 30.0 {
		t.Errorf("Mean = %f, want 30.0", b.Mean)
	}
	if b.StdDev != 0.0 {
		t.Errorf("StdDev = %f, want 0.0", b.StdDev)
	}
	if b.N != 5 {
		t.Errorf("N = %d, want 5", b.N)
	}
}

func TestCalculateBaselineTooFew(t *testing.T) {
	records := []DiffRecord{
		{AddedCount: 10, RemovedCount: 0, ChangedCount: 0},
		{AddedCount: 20, RemovedCount: 0, ChangedCount: 0},
	}
	// Only 2 records, minSamples=3
	b := CalculateBaseline(records, 3)
	if b != nil {
		t.Error("expected nil baseline for too few records")
	}
}

func TestCalculateBaselineSingleSample(t *testing.T) {
	records := []DiffRecord{
		{AddedCount: 10, RemovedCount: 5, ChangedCount: 15}, // total=30
	}
	baseline := CalculateBaseline(records, 1)
	if baseline == nil {
		t.Fatal("expected non-nil baseline")
		return
	}
	b := *baseline
	if b.Mean != 30.0 {
		t.Errorf("Mean = %f, want 30.0", b.Mean)
	}
	if b.StdDev != 0.0 {
		t.Errorf("StdDev = %f, want 0.0 (single sample)", b.StdDev)
	}
	if b.N != 1 {
		t.Errorf("N = %d, want 1", b.N)
	}
}

func TestCalculateBaselineVaried(t *testing.T) {
	records := []DiffRecord{
		{AddedCount: 10, RemovedCount: 0, ChangedCount: 0}, // total=10
		{AddedCount: 20, RemovedCount: 0, ChangedCount: 0}, // total=20
		{AddedCount: 30, RemovedCount: 0, ChangedCount: 0}, // total=30
		{AddedCount: 40, RemovedCount: 0, ChangedCount: 0}, // total=40
	}
	// mean=(10+20+30+40)/4=25
	// variance=((10-25)^2+(20-25)^2+(30-25)^2+(40-25)^2)/4 = (225+25+25+225)/4 = 500/4 = 125
	// stddev=sqrt(125) ≈ 11.18
	baseline := CalculateBaseline(records, 2)
	if baseline == nil {
		t.Fatal("expected non-nil baseline")
		return
	}
	b := *baseline
	if math.Abs(b.Mean-25.0) > 0.01 {
		t.Errorf("Mean = %f, want 25.0", b.Mean)
	}
	if math.Abs(b.StdDev-11.1803) > 0.01 {
		t.Errorf("StdDev = %f, want ~11.1803", b.StdDev)
	}
}

func TestIsAnomalous(t *testing.T) {
	// baseline mean=50, stddev=10, k=3 → threshold = 50+30 = 80
	baseline := &Baseline{Mean: 50.0, StdDev: 10.0, N: 10}

	tests := []struct {
		name        string
		current     DiffRecord
		k           float64
		wantAnomaly bool
	}{
		{
			name:        "normal churn (70 < 80)",
			current:     DiffRecord{AddedCount: 50, RemovedCount: 10, ChangedCount: 10}, // total=70
			k:           3.0,
			wantAnomaly: false,
		},
		{
			name:        "anomalous churn (90 > 80)",
			current:     DiffRecord{AddedCount: 60, RemovedCount: 20, ChangedCount: 10}, // total=90
			k:           3.0,
			wantAnomaly: true,
		},
		{
			name:        "edge case equal to threshold (80 == 80)",
			current:     DiffRecord{AddedCount: 50, RemovedCount: 20, ChangedCount: 10}, // total=80
			k:           3.0,
			wantAnomaly: false, // strictly greater, not >=
		},
		{
			name:        "k=1 lower threshold (70 > 60)",
			current:     DiffRecord{AddedCount: 50, RemovedCount: 10, ChangedCount: 10}, // total=70
			k:           1.0,
			wantAnomaly: true, // 70 > 50+10=60
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsAnomalous(tt.current, baseline, tt.k)
			if got != tt.wantAnomaly {
				t.Errorf("IsAnomalous() = %v, want %v", got, tt.wantAnomaly)
			}
		})
	}
}

func TestIsAnomalousNilBaseline(t *testing.T) {
	current := DiffRecord{AddedCount: 9999, RemovedCount: 0, ChangedCount: 0}
	// nil baseline → always false (insufficient data)
	if IsAnomalous(current, nil, 3.0) {
		t.Error("expected false for nil baseline")
	}
}

// ---------------------------------------------------------------------------
// parseResticDiff tests
// ---------------------------------------------------------------------------

func TestParseResticDiffNormal(t *testing.T) {
	output := strings.Join([]string{
		"comparing snapshots abc123 to def456...",
		"",
		"+    /home/user/newfile.txt",
		"-    /home/user/oldfile.txt",
		"M    /home/user/changed.txt",
		"",
		"Files:           1 new, 1 removed, 2 changed",
		"Dirs:            0 new, 0 removed",
	}, "\n")

	result := parseResticDiff(output, "abc123", "def456")
	if result.Stats.Added != 1 {
		t.Errorf("Added = %d, want 1", result.Stats.Added)
	}
	if result.Stats.Removed != 1 {
		t.Errorf("Removed = %d, want 1", result.Stats.Removed)
	}
	if result.Stats.Changed != 1 {
		t.Errorf("Changed = %d, want 1", result.Stats.Changed)
	}
	if len(result.Changes) != 3 {
		t.Errorf("len(Changes) = %d, want 3", len(result.Changes))
	}
}

func TestParseResticDiffWithSizeInfo(t *testing.T) {
	output := strings.Join([]string{
		"+    1.234 KiB /home/user/added.txt",
		"-    512 B /home/user/removed.txt",
		"M    100 B 200 B /home/user/grew.txt",
	}, "\n")

	result := parseResticDiff(output, "snap1", "snap2")
	if result.Stats.Added != 1 {
		t.Errorf("Added = %d, want 1", result.Stats.Added)
	}
	if result.Stats.Removed != 1 {
		t.Errorf("Removed = %d, want 1", result.Stats.Removed)
	}
	if result.Stats.Changed != 1 {
		t.Errorf("Changed = %d, want 1", result.Stats.Changed)
	}
	if len(result.Changes) != 3 {
		t.Fatalf("len(Changes) = %d, want 3", len(result.Changes))
	}
	// Verify added file size
	added := result.Changes[0]
	if added.Type != "added" || added.Path != "/home/user/added.txt" {
		t.Errorf("unexpected added change: type=%s path=%s", added.Type, added.Path)
	}
	if added.SizeAfter == nil || *added.SizeAfter != 1263 { // 1.234 * 1024 ≈ 1263
		t.Errorf("added SizeAfter want ~1263, got %v", added.SizeAfter)
	}
}

func TestParseResticDiffWithRansomwarePaths(t *testing.T) {
	output := strings.Join([]string{
		"+    /home/user/important.doc.encrypted",
		"+    /home/user/readme.txt.locked",
		"M    /home/user/normal.txt",
	}, "\n")

	result := parseResticDiff(output, "s1", "s2")
	var paths []string
	for _, ch := range result.Changes {
		paths = append(paths, ch.Path)
	}
	hits := CountRansomSuffixHits(paths)
	if hits != 2 {
		t.Errorf("RansomSuffixHits = %d, want 2", hits)
	}
}

func TestParseResticDiffEmpty(t *testing.T) {
	result := parseResticDiff("", "s1", "s2")
	if len(result.Changes) != 0 {
		t.Errorf("expected empty changes, got %d", len(result.Changes))
	}
	if result.Stats.Added != 0 || result.Stats.Removed != 0 || result.Stats.Changed != 0 {
		t.Error("expected all zero stats for empty diff")
	}
}

// ---------------------------------------------------------------------------
// restic helper function tests
// ---------------------------------------------------------------------------

func TestExtractResticPassword(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"empty", "", ""},
		{"no password field", `{"other":"value"}`, ""},
		{"with password", `{"repository_password":"my-secret"}`, "my-secret"},
		{"empty password", `{"repository_password":""}`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractResticPassword(tt.raw)
			if got != tt.want {
				t.Errorf("extractResticPassword() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestShellEscapeArg(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"simple", "hello", "'hello'"},
		{"with single quote", "it's", "'it'\\''s'"},
		{"path", "/home/user/backup", "'/home/user/backup'"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shellEscapeArg(tt.input)
			if got != tt.want {
				t.Errorf("shellEscapeArg(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseResticSnapshotsJSON(t *testing.T) {
	tests := []struct {
		name    string
		output  string
		wantLen int
		wantErr bool
	}{
		{
			name:    "two snapshots",
			output:  `[{"id":"abc123def456","time":"2024-01-01T00:00:00Z"},{"id":"789abc012def","time":"2024-01-02T00:00:00Z"}]`,
			wantLen: 2,
		},
		{
			name:    "one snapshot",
			output:  `[{"id":"abc123"}]`,
			wantLen: 1,
		},
		{
			name:    "empty array",
			output:  `[]`,
			wantLen: 0,
		},
		{
			name:    "invalid json",
			output:  `not json`,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ids, err := parseResticSnapshotsJSON(tt.output)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(ids) != tt.wantLen {
				t.Errorf("len(ids) = %d, want %d", len(ids), tt.wantLen)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// AnalyzeSnapshotDiff unit tests (non-SSH paths)
// ---------------------------------------------------------------------------

func TestAnalyzeSnapshotDiffNonRestic(t *testing.T) {
	// Non-restic executor should return nil findings, nil error without touching DB.
	findings, err := AnalyzeSnapshotDiff(context.Background(), nil, model.Task{
		ExecutorType: "rsync",
		RsyncTarget:  "/some/path",
	}, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if findings != nil {
		t.Errorf("expected nil findings for non-restic, got %d", len(findings))
	}
}

func TestAnalyzeSnapshotDiffNoTarget(t *testing.T) {
	findings, err := AnalyzeSnapshotDiff(context.Background(), nil, model.Task{
		ExecutorType: "restic",
		RsyncTarget:  "",
	}, 0)
	if err == nil {
		t.Fatal("expected error for empty RsyncTarget")
	}
	if findings != nil {
		t.Errorf("expected nil findings, got %d", len(findings))
	}
}

func TestBuildDiffEnvPrefix(t *testing.T) {
	if got := buildDiffEnvPrefix(""); got != "RESTIC_PASSWORD=''" {
		t.Errorf("empty password: got %q", got)
	}
	got := buildDiffEnvPrefix("test-pass")
	if !strings.HasPrefix(got, "RESTIC_PASSWORD=") {
		t.Errorf("expected RESTIC_PASSWORD= prefix, got %q", got)
	}
}
