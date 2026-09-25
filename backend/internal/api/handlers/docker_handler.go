package handlers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"

	"xirang/backend/internal/credentialaudit"
	"xirang/backend/internal/model"
	"xirang/backend/internal/sshutil"

	"xirang/backend/internal/logger"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/ssh"
	"gorm.io/gorm"
)

// DockerVolume 表示一个 Docker 卷。
type DockerVolume struct {
	Name       string `json:"name"`
	Driver     string `json:"driver"`
	Mountpoint string `json:"mountpoint"`
}

const (
	dockerVolumeDiscoveryTimeout = 15 * time.Second
	dockerVolumeCommandTimeout   = 10 * time.Second
	dockerVolumeMaxCount         = 256
	dockerVolumeMaxStdoutBytes   = 512 << 10
	dockerVolumeMaxStderrBytes   = 32 << 10
	dockerVolumeMaxRecordBytes   = 16 << 10
	dockerVolumeMaxNameBytes     = 255
)

var errDockerVolumeDiscovery = errors.New("docker volume discovery failed")

type dockerCommandRunner interface {
	Run(context.Context, sshutil.CommandSpec) (sshutil.CommandResult, error)
}

type dockerCommandExecutionRunner interface {
	OpenExecution(context.Context, sshutil.CommandSpec) (sshutil.CommandExecutionStream, error)
}

type dockerCommandResult struct {
	sshutil.CommandResult
	exitCode      int
	exitCodeKnown bool
}

type dockerVolumesResponse struct {
	Data    []DockerVolume `json:"data"`
	Partial bool           `json:"partial"`
	Warning string         `json:"warning,omitempty"`
}

// DockerHandler 处理 Docker 相关请求。
type DockerHandler struct {
	db *gorm.DB
}

func NewDockerHandler(db *gorm.DB) *DockerHandler {
	return &DockerHandler{db: db}
}

// ListVolumes godoc
// @Summary      列出 Docker 卷
// @Description  通过 SSH 列举远端节点上的 Docker 卷
// @Tags         docker
// @Security     Bearer
// @Produce      json
// @Param        id  path      int  true  "节点 ID"
// @Success      200  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Failure      404  {object}  handlers.Response
// @Failure      502  {object}  handlers.Response
// @Router       /nodes/{id}/docker-volumes [get]
func (h *DockerHandler) ListVolumes(c *gin.Context) {
	nodeID, ok := parseID(c, "id")
	if !ok {
		return
	}

	var node model.Node
	if err := h.db.Preload("SSHKey").First(&node, nodeID).Error; err != nil {
		respondNotFound(c, "节点不存在")
		return
	}

	operationCtx, cancel := context.WithTimeout(c.Request.Context(), dockerVolumeDiscoveryTimeout)
	defer cancel()

	sshClient, credential, err := dialSSHForDocker(operationCtx, node, h.db)
	if err != nil {
		h.writeDockerVolumeAudit(c, node, credential, credentialAuditSSHOutcome("dial", err), "dial", err, 0, false)
		message := "SSH 连接失败"
		var hostKeyErr *sshutil.HostKeyError
		if errors.As(err, &hostKeyErr) {
			message = hostKeyErr.Error()
		}
		respondBadGateway(c, message)
		return
	}
	defer sshClient.Close() //nolint:errcheck // close error not actionable on deferred cleanup

	runner := sshutil.NewSSHCommandRunnerWithTransportClose(sshClient, 1)
	volumes, warning, partial, err := listDockerVolumes(operationCtx, runner)
	if err != nil {
		logger.Log.Error().Err(err).Msg("获取 Docker 卷失败")
		h.writeDockerVolumeAudit(c, node, credential, credentialaudit.OutcomeFailure, "discover", err, len(volumes), true)
		respondBadGateway(c, "获取 Docker 卷失败")
		return
	}

	outcome := credentialaudit.OutcomeSuccess
	if partial {
		outcome = credentialaudit.OutcomeFailure
	}
	h.writeDockerVolumeAudit(c, node, credential, outcome, "discover", nil, len(volumes), partial)
	respondOK(c, dockerVolumesResponse{Data: volumes, Partial: partial, Warning: warning})
}

func (h *DockerHandler) writeDockerVolumeAudit(c *gin.Context, node model.Node, credential sshutil.ResolvedCredential, outcome, stage string, err error, count int, warning bool) {
	fallbackKind, fallbackSource, fallbackKeyID := nodeCredentialFallback(node)
	kind, source, keyID := eventCredentialFields(credential, fallbackKind, fallbackSource)
	if keyID == nil {
		keyID = fallbackKeyID
	}
	event := credentialaudit.Event{
		Action:           "docker_volumes.discover",
		Purpose:          sshutil.PurposeDockerVolumes,
		CredentialKind:   kind,
		CredentialSource: source,
		SSHKeyID:         keyID,
		NodeID:           credentialaudit.PtrUint(node.ID),
		Outcome:          outcome,
		Metadata: map[string]any{
			"stage":       stage,
			"count":       count,
			"has_warning": warning,
		},
	}
	if err != nil {
		event.ErrorMessage = credentialAuditSafeError(stage, err)
	}
	writeCredentialAuditFromGin(c, h.db, event)
}

// dialSSHForDocker 建立 SSH 连接，用于执行 Docker 命令。
func dialSSHForDocker(ctx context.Context, node model.Node, db *gorm.DB) (*ssh.Client, sshutil.ResolvedCredential, error) {
	auth, credential, err := sshutil.BuildSSHAuthForPurpose(node, db, sshutil.PurposeDockerVolumes)
	if err != nil {
		return nil, credential, err
	}
	hostKey, err := sshutil.ResolveSSHHostKeyCallback()
	if err != nil {
		return nil, credential, err
	}

	dialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	addr := net.JoinHostPort(node.Host, strconv.Itoa(node.Port))
	client, err := sshutil.DialSSH(dialCtx, addr, node.Username, auth, hostKey)
	return client, credential, err
}

// dockerVolumeLsEntry 用于解析 docker volume ls --format '{{json .}}' 的输出。
type dockerVolumeLsEntry struct {
	Driver     string `json:"Driver"`
	Name       string `json:"Name"`
	Mountpoint string `json:"Mountpoint"`
}

type dockerVolumeInspectEntry struct {
	Driver     string `json:"Driver"`
	Name       string `json:"Name"`
	Mountpoint string `json:"Mountpoint"`
}

func runDockerCommand(ctx context.Context, runner dockerCommandRunner, spec sshutil.CommandSpec) (dockerCommandResult, error) {
	if executionRunner, ok := runner.(dockerCommandExecutionRunner); ok {
		stream, err := executionRunner.OpenExecution(ctx, spec)
		if err != nil {
			return dockerCommandResult{}, err
		}
		if stream == nil {
			return dockerCommandResult{}, fmt.Errorf("docker command stream unavailable")
		}
		stdout, readErr := io.ReadAll(stream)
		completion, joinErr := stream.Join()
		result := dockerCommandResult{
			CommandResult: sshutil.CommandResult{Stdout: stdout, Stderr: completion.Stderr},
			exitCode:      completion.ExitCode,
			exitCodeKnown: completion.ExitCodeKnown,
		}
		if readErr != nil {
			return result, readErr
		}
		if joinErr != nil {
			return result, joinErr
		}
		return result, nil
	}
	result, err := runner.Run(ctx, spec)
	return dockerCommandResult{CommandResult: result}, err
}

// listDockerVolumes performs one bounded listing and one bounded batch inspect.
// A successful listing with inspect or parse gaps is returned as partial rather
// than being flattened into a complete empty response.
func listDockerVolumes(ctx context.Context, runner dockerCommandRunner) ([]DockerVolume, string, bool, error) {
	if runner == nil {
		return nil, "", false, fmt.Errorf("%w: runner unavailable", errDockerVolumeDiscovery)
	}
	listResult, err := runDockerCommand(ctx, runner, sshutil.CommandSpec{
		Binary:         "docker",
		Args:           []string{"volume", "ls", "--format", "{{json .}}"},
		Timeout:        dockerVolumeCommandTimeout,
		MaxStdoutBytes: dockerVolumeMaxStdoutBytes,
		MaxStderrBytes: dockerVolumeMaxStderrBytes,
		MaxRecordBytes: dockerVolumeMaxRecordBytes,
	})
	if err != nil {
		return nil, "", false, fmt.Errorf("%w: list command: %w", errDockerVolumeDiscovery, err)
	}
	if listResult.exitCodeKnown && listResult.exitCode != 0 {
		return nil, "", false, fmt.Errorf("%w: list command exited with status %d", errDockerVolumeDiscovery, listResult.exitCode)
	}

	entries, parseWarning, limited := parseDockerVolumeList(listResult.Stdout)
	if len(entries) == 0 {
		if parseWarning || limited {
			return []DockerVolume{}, dockerVolumePartialWarning(parseWarning, limited), true, nil
		}
		return []DockerVolume{}, "", false, nil
	}

	volumes := make([]DockerVolume, 0, len(entries))
	inspectNames := make([]string, 0, len(entries))
	for _, entry := range entries {
		volume := DockerVolume{Name: entry.Name, Driver: entry.Driver, Mountpoint: strings.TrimSpace(entry.Mountpoint)}
		if volume.Mountpoint == "" {
			inspectNames = append(inspectNames, entry.Name)
		}
		volumes = append(volumes, volume)
	}
	partial := parseWarning || limited
	warning := dockerVolumePartialWarning(parseWarning, limited)
	if len(inspectNames) == 0 {
		return volumes, warning, partial, nil
	}

	args := make([]string, 0, len(inspectNames)+4)
	args = append(args, "volume", "inspect", "--format", "{{json .}}")
	args = append(args, inspectNames...)
	inspectResult, inspectErr := runDockerCommand(ctx, runner, sshutil.CommandSpec{
		Binary:         "docker",
		Args:           args,
		Timeout:        dockerVolumeCommandTimeout,
		MaxStdoutBytes: dockerVolumeMaxStdoutBytes,
		MaxStderrBytes: dockerVolumeMaxStderrBytes,
		MaxRecordBytes: dockerVolumeMaxRecordBytes,
	})
	if inspectErr != nil {
		return volumes, warning, partial, fmt.Errorf("%w: inspect command: %w", errDockerVolumeDiscovery, inspectErr)
	}
	if inspectResult.exitCodeKnown && inspectResult.exitCode != 0 {
		return volumes, dockerVolumeInspectWarning(), true, nil
	}

	inspectEntries, inspectMalformed, inspectLimited := parseDockerVolumeInspect(inspectResult.Stdout)
	mountpoints := make(map[string]string, len(inspectEntries))
	for _, entry := range inspectEntries {
		if entry.Name == "" || entry.Mountpoint == "" {
			continue
		}
		mountpoints[entry.Name] = strings.TrimSpace(entry.Mountpoint)
	}
	for index := range volumes {
		if volumes[index].Mountpoint != "" {
			continue
		}
		if mountpoint := mountpoints[volumes[index].Name]; mountpoint != "" {
			volumes[index].Mountpoint = mountpoint
			continue
		}
		partial = true
	}
	if inspectMalformed || inspectLimited {
		partial = true
	}
	if partial && warning == "" {
		warning = dockerVolumeInspectWarning()
	}
	return volumes, warning, partial, nil
}
func parseDockerVolumeList(output []byte) ([]dockerVolumeLsEntry, bool, bool) {
	scanner := bufio.NewScanner(bytes.NewReader(output))
	scanner.Buffer(make([]byte, 4<<10), dockerVolumeMaxRecordBytes)
	entries := make([]dockerVolumeLsEntry, 0, minInt(dockerVolumeMaxCount, 32))
	seen := make(map[string]struct{})
	malformed := false
	limited := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if len(entries) >= dockerVolumeMaxCount {
			limited = true
			break
		}
		var entry dockerVolumeLsEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil ||
			!validDockerVolumeName(entry.Name) {
			malformed = true
			continue
		}
		if _, exists := seen[entry.Name]; exists {
			continue
		}
		seen[entry.Name] = struct{}{}
		entries = append(entries, entry)
	}
	if scanner.Err() != nil {
		malformed = true
	}
	return entries, malformed, limited
}

func parseDockerVolumeInspect(output []byte) ([]dockerVolumeInspectEntry, bool, bool) {
	scanner := bufio.NewScanner(bytes.NewReader(output))
	scanner.Buffer(make([]byte, 4<<10), dockerVolumeMaxRecordBytes)
	entries := make([]dockerVolumeInspectEntry, 0, dockerVolumeMaxCount)
	malformed := false
	limited := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if len(entries) >= dockerVolumeMaxCount {
			limited = true
			break
		}
		var entry dockerVolumeInspectEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			malformed = true
			continue
		}
		entries = append(entries, entry)
	}
	if scanner.Err() != nil {
		malformed = true
	}
	return entries, malformed, limited
}

func validDockerVolumeName(name string) bool {
	return len(name) <= dockerVolumeMaxNameBytes && safeDockerName.MatchString(name)
}

func dockerVolumePartialWarning(parseWarning, limited bool) string {
	if limited {
		return "Docker 卷数量超过显示上限，结果不完整"
	}
	if parseWarning {
		return "部分 Docker 卷信息无法解析，结果不完整"
	}
	return ""
}

func dockerVolumeInspectWarning() string {
	return "部分 Docker 卷挂载点无法读取，结果不完整"
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

var safeDockerName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.\-]*$`)
