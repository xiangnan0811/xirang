package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"xirang/backend/internal/credentialaudit"
	"xirang/backend/internal/model"
	"xirang/backend/internal/sshutil"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func openDockerHandlerTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+handlerTestDBName(t)+"?mode=memory&cache=shared&_loc=UTC"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&model.CredentialAuditEvent{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func TestDockerVolumeAuditDoesNotPersistRemoteOutputOrVolumeNames(t *testing.T) {
	db := openDockerHandlerTestDB(t)
	h := NewDockerHandler(db)
	keyID := uint(9)
	node := model.Node{ID: 5, Name: "docker-node", Host: "10.40.0.5", AuthType: "key", SSHKeyID: &keyID}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("userID", uint(101))
	c.Set("username", "alice")
	c.Set("role", "operator")
	c.Request = httptest.NewRequest("GET", "/nodes/5/docker-volumes", nil)

	h.writeDockerVolumeAudit(c, node, sshutil.ResolvedCredential{Kind: "ssh_key", Source: "ssh_key_id=9", KeyID: &keyID}, credentialaudit.OutcomeFailure, "list", errors.New("docker list failed: output: FAKE_DOCKER_OUTPUT_FOR_TEST_ONLY volume-prod-data"), 3, true)

	var event model.CredentialAuditEvent
	if err := db.First(&event).Error; err != nil {
		t.Fatalf("load audit event: %v", err)
	}
	if event.Action != "docker_volumes.discover" || event.Purpose != sshutil.PurposeDockerVolumes || event.Outcome != credentialaudit.OutcomeFailure {
		t.Fatalf("unexpected docker audit event: %+v", event)
	}
	if event.NodeID == nil || *event.NodeID != node.ID || event.SSHKeyID == nil || *event.SSHKeyID != keyID {
		t.Fatalf("expected node/key ids in audit event: %+v", event)
	}
	if strings.Contains(event.Metadata, "FAKE_DOCKER_OUTPUT_FOR_TEST_ONLY") || strings.Contains(event.Metadata, "volume-prod-data") || strings.Contains(event.ErrorMessage, "FAKE_DOCKER_OUTPUT_FOR_TEST_ONLY") {
		t.Fatalf("docker audit must not persist output or volume names: metadata=%s error=%s", event.Metadata, event.ErrorMessage)
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(event.Metadata), &metadata); err != nil {
		t.Fatalf("metadata json: %v", err)
	}
	if metadata["count"] == nil || metadata["has_warning"] != true || metadata["stage"] != "list" {
		t.Fatalf("safe docker audit metadata missing expected fields: %#v", metadata)
	}
}

type dockerRunnerFake struct {
	results []sshutil.CommandResult
	errs    []error
	specs   []sshutil.CommandSpec
	block   bool
}

func (runner *dockerRunnerFake) Run(ctx context.Context, spec sshutil.CommandSpec) (sshutil.CommandResult, error) {
	runner.specs = append(runner.specs, spec)
	if runner.block {
		<-ctx.Done()
		return sshutil.CommandResult{}, ctx.Err()
	}
	index := len(runner.specs) - 1
	if index < len(runner.errs) && runner.errs[index] != nil {
		return sshutil.CommandResult{}, runner.errs[index]
	}
	if index < len(runner.results) {
		return runner.results[index], nil
	}
	return sshutil.CommandResult{}, nil
}

type dockerExecutionStreamFake struct {
	reader     *bytes.Reader
	completion sshutil.CommandCompletion
	joinErr    error
}

func (stream *dockerExecutionStreamFake) Read(value []byte) (int, error) {
	return stream.reader.Read(value)
}

func (stream *dockerExecutionStreamFake) Join() (sshutil.CommandCompletion, error) {
	return stream.completion, stream.joinErr
}

func (*dockerExecutionStreamFake) Cancel() error {
	return nil
}

type dockerExecutionRunnerFake struct {
	*dockerRunnerFake
	exitCodes []int
	joinErrs  []error
}

func (runner *dockerExecutionRunnerFake) OpenExecution(_ context.Context, spec sshutil.CommandSpec) (sshutil.CommandExecutionStream, error) {
	runner.specs = append(runner.specs, spec)
	index := len(runner.specs) - 1
	if index < len(runner.errs) && runner.errs[index] != nil {
		return nil, runner.errs[index]
	}
	result := sshutil.CommandResult{}
	if index < len(runner.results) {
		result = runner.results[index]
	}
	exitCode := 0
	if index < len(runner.exitCodes) {
		exitCode = runner.exitCodes[index]
	}
	var joinErr error
	if index < len(runner.joinErrs) {
		joinErr = runner.joinErrs[index]
	}
	return &dockerExecutionStreamFake{
		reader: bytes.NewReader(result.Stdout),
		completion: sshutil.CommandCompletion{
			ExitCode:      exitCode,
			ExitCodeKnown: true,
			Stderr:        result.Stderr,
		},
		joinErr: joinErr,
	}, nil
}

func TestListDockerVolumesUsesBoundedBatchInspect(t *testing.T) {
	runner := &dockerExecutionRunnerFake{
		dockerRunnerFake: &dockerRunnerFake{results: []sshutil.CommandResult{
			{Stdout: []byte(`{"Name":"prod-data","Driver":"local","Mountpoint":""}` + "\n" + `{"Name":"cache","Driver":"local","Mountpoint":"/var/lib/cache"}` + "\n")},
			{Stdout: []byte(`{"Name":"prod-data","Driver":"local","Mountpoint":"/var/lib/docker/volumes/prod-data/_data"}` + "\n")},
		}},
		exitCodes: []int{0, 0},
	}

	volumes, warning, partial, err := listDockerVolumes(context.Background(), runner)
	if err != nil {
		t.Fatalf("list Docker volumes: %v", err)
	}
	if partial || warning != "" {
		t.Fatalf("complete discovery marked partial=%v warning=%q", partial, warning)
	}
	if len(volumes) != 2 || volumes[0].Mountpoint == "" || volumes[1].Mountpoint != "/var/lib/cache" {
		t.Fatalf("unexpected volumes: %+v", volumes)
	}
	if len(runner.specs) != 2 {
		t.Fatalf("runner calls=%d want list plus one batch inspect", len(runner.specs))
	}
	if runner.specs[0].MaxStdoutBytes != dockerVolumeMaxStdoutBytes ||
		runner.specs[0].MaxStderrBytes != dockerVolumeMaxStderrBytes ||
		runner.specs[0].MaxRecordBytes != dockerVolumeMaxRecordBytes {
		t.Fatalf("list limits=%+v", runner.specs[0])
	}
	if got := runner.specs[1].Args; len(got) != 5 || got[0] != "volume" || got[1] != "inspect" || got[4] != "prod-data" {
		t.Fatalf("batch inspect args=%v", got)
	}
}

func TestListDockerVolumesFailsOnNonzeroListExit(t *testing.T) {
	runner := &dockerExecutionRunnerFake{
		dockerRunnerFake: &dockerRunnerFake{
			results: []sshutil.CommandResult{{Stdout: []byte(`{"Name":"prod-data","Driver":"local","Mountpoint":""}` + "\n")}},
		},
		exitCodes: []int{7},
	}
	_, _, _, err := listDockerVolumes(context.Background(), runner)
	if !errors.Is(err, errDockerVolumeDiscovery) {
		t.Fatalf("nonzero list exit err=%v", err)
	}
}

func TestListDockerVolumesReturnsPartialWhenBatchInspectFails(t *testing.T) {
	runner := &dockerExecutionRunnerFake{
		dockerRunnerFake: &dockerRunnerFake{results: []sshutil.CommandResult{
			{Stdout: []byte(`{"Name":"prod-data","Driver":"local","Mountpoint":""}` + "\n")},
		}},
		exitCodes: []int{0, 1},
	}

	volumes, warning, partial, err := listDockerVolumes(context.Background(), runner)
	if err != nil {
		t.Fatalf("partial discovery failed: %v", err)
	}
	if !partial || warning == "" || len(volumes) != 1 || volumes[0].Mountpoint != "" {
		t.Fatalf("partial result=%+v warning=%q partial=%v", volumes, warning, partial)
	}
}

func TestListDockerVolumesFailsClosedOnUnknownInspectCompletion(t *testing.T) {
	runner := &dockerExecutionRunnerFake{
		dockerRunnerFake: &dockerRunnerFake{results: []sshutil.CommandResult{
			{Stdout: []byte(`{"Name":"prod-data","Driver":"local","Mountpoint":""}` + "\n")},
		}},
		joinErrs: []error{nil, errors.New("FAKE_DOCKER_UNKNOWN_COMPLETION_FOR_TEST_ONLY")},
	}
	_, _, _, err := listDockerVolumes(context.Background(), runner)
	if !errors.Is(err, errDockerVolumeDiscovery) {
		t.Fatalf("unknown inspect completion err=%v", err)
	}
}

func TestListDockerVolumesFailsClosedOnCancellationAndCommandOutputError(t *testing.T) {
	runner := &dockerRunnerFake{block: true}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, _, _, err := listDockerVolumes(ctx, runner)
	if !errors.Is(err, errDockerVolumeDiscovery) || time.Since(started) > time.Second {
		t.Fatalf("canceled discovery err=%v elapsed=%s", err, time.Since(started))
	}

	limited := &dockerRunnerFake{errs: []error{sshutil.ErrCommandOutputLimit}}
	_, _, _, err = listDockerVolumes(context.Background(), limited)
	if !errors.Is(err, errDockerVolumeDiscovery) {
		t.Fatalf("output-limited discovery err=%v", err)
	}
}

func TestListDockerVolumesMarksVolumeBudgetOverflowPartial(t *testing.T) {
	var output strings.Builder
	for index := range dockerVolumeMaxCount + 1 {
		output.WriteString(`{"Name":"vol-`)
		output.WriteString(string(rune('a' + index%26)))
		output.WriteString(`-`)
		_, _ = fmt.Fprintf(&output, "%d", index)
		output.WriteString(`","Driver":"local","Mountpoint":"/mnt"}`)
		output.WriteByte('\n')
	}
	runner := &dockerRunnerFake{results: []sshutil.CommandResult{{Stdout: []byte(output.String())}}}
	volumes, warning, partial, err := listDockerVolumes(context.Background(), runner)
	if err != nil {
		t.Fatalf("budget-limited listing: %v", err)
	}
	if !partial || warning == "" || len(volumes) != dockerVolumeMaxCount {
		t.Fatalf("budget result len=%d warning=%q partial=%v", len(volumes), warning, partial)
	}
}
