package node

import (
	"context"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
	"time"

	"xirang/backend/internal/apperr"
	"xirang/backend/internal/model"
	"xirang/backend/internal/repository"
)

var nodeHostnameRegexp = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?)*$`)
var consecutiveDashRegexp = regexp.MustCompile(`-{2,}`)

// NodeService encapsulates business logic for node CRUD operations.
type NodeService struct {
	repo repository.NodeRepository
}

// NewNodeService creates a new NodeService backed by the given repository.
func NewNodeService(repo repository.NodeRepository) *NodeService {
	return &NodeService{repo: repo}
}

// CreateNodeInput is the input payload for creating or updating a node.
type CreateNodeInput struct {
	Name             string
	Host             string
	Port             int
	Username         string
	AuthType         string
	Password         string
	PrivateKey       string
	SSHKeyID         *uint
	Tags             string
	Status           string
	BasePath         string
	BackupDir        string
	MaintenanceStart *string
	MaintenanceEnd   *string
	ExpiryDate       *string
	Archived         *bool
	UseSudo          *bool
}

func validationError(msg string) error {
	return fmt.Errorf("%w: %s", apperr.ErrValidation, msg)
}

// Create validates the input and creates a new node. It returns the created
// node with SSHKey preloaded, or an error.
func (s *NodeService) Create(ctx context.Context, input CreateNodeInput) (*model.Node, error) {
	// Set defaults
	if input.Port == 0 {
		input.Port = 22
	}
	if input.AuthType == "" {
		input.AuthType = "key"
	}
	if input.Status == "" {
		input.Status = "offline"
	}

	// Validate
	if err := validateNodeName(input.Name); err != nil {
		return nil, validationError(err.Error())
	}
	input.Host = strings.TrimSpace(input.Host)
	if err := ValidateNodeHostPort(input.Host, input.Port); err != nil {
		return nil, validationError(err.Error())
	}
	if err := s.validateSSHRef(ctx, input); err != nil {
		return nil, validationError(err.Error())
	}

	// Handle backup dir: auto-generate from name if empty
	backupDir := input.BackupDir
	if strings.TrimSpace(backupDir) == "" {
		backupDir = SanitizeBackupDir(input.Name)
	}
	if strings.TrimSpace(backupDir) == "" {
		return nil, validationError("节点名称无法自动生成备份目录标识，请手动指定 backup_dir（仅允许英文字母、数字、连字符、下划线）")
	}
	if err := validateBackupDir(backupDir); err != nil {
		return nil, validationError(err.Error())
	}
	applyTimes, err := optionalTimePatch(input)
	if err != nil {
		return nil, err
	}

	// Build node model
	node := model.Node{
		Name:      input.Name,
		Host:      input.Host,
		Port:      input.Port,
		Username:  input.Username,
		AuthType:  input.AuthType,
		Tags:      input.Tags,
		Status:    input.Status,
		BasePath:  input.BasePath,
		BackupDir: backupDir,
	}

	switch input.AuthType {
	case "password":
		node.Password = input.Password
		node.SSHKeyID = nil
		node.PrivateKey = ""
	case "key":
		node.Password = ""
		node.SSHKeyID = input.SSHKeyID
		if input.SSHKeyID == nil {
			node.PrivateKey = input.PrivateKey
		} else {
			node.PrivateKey = ""
		}
	}

	if applyTimes != nil {
		if err := applyTimes(&node); err != nil {
			return nil, err
		}
	}
	applyOptionalFields(&node, input)

	if err := s.repo.Create(ctx, &node); err != nil {
		if errors.Is(err, repository.ErrNodeSSHKeyNotFound) {
			return nil, validationError("所选密钥不存在，请重新选择")
		}
		err = apperr.WrapDBError(err)
		return nil, err
	}

	// Reload with associations
	nodePtr, err := s.repo.FindByIDWithSSHKey(ctx, node.ID)
	if err != nil {
		return nil, err
	}
	return nodePtr, nil
}

// Update validates the input and updates an existing node. It returns the
// updated node (with SSHKey preloaded) and the previous backup directory
// identifier (for change-warning messages), or an error.
func (s *NodeService) Update(ctx context.Context, id uint, input CreateNodeInput) (*model.Node, string, error) {
	node, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return nil, "", err
	}

	oldBackupDir := node.BackupDir
	updateStatus := input.Status != ""

	// Merge defaults from existing node for unspecified fields
	if input.Port == 0 {
		input.Port = 22
	}
	if input.AuthType == "" {
		input.AuthType = node.AuthType
	}
	if input.Status == "" {
		input.Status = node.Status
	}
	if input.BasePath == "" {
		input.BasePath = node.BasePath
	}
	if strings.TrimSpace(input.BackupDir) == "" {
		input.BackupDir = node.BackupDir
	}
	if input.SSHKeyID == nil {
		input.SSHKeyID = node.SSHKeyID
	}
	if input.Password == "" {
		input.Password = node.Password
	}
	if input.PrivateKey == "" {
		input.PrivateKey = node.PrivateKey
	}

	// Validate
	if err := validateBackupDir(input.BackupDir); err != nil {
		return nil, "", validationError(err.Error())
	}
	if err := validateNodeName(input.Name); err != nil {
		return nil, "", validationError(err.Error())
	}
	input.Host = strings.TrimSpace(input.Host)
	if err := ValidateNodeHostPort(input.Host, input.Port); err != nil {
		return nil, "", validationError(err.Error())
	}
	if err := s.validateSSHRef(ctx, input); err != nil {
		return nil, "", validationError(err.Error())
	}
	applyTimes, err := optionalTimePatch(input)
	if err != nil {
		return nil, "", err
	}

	// Update node fields
	node.Name = input.Name
	node.Host = input.Host
	node.Port = input.Port
	node.Username = input.Username
	node.AuthType = input.AuthType
	node.Tags = input.Tags
	node.Status = input.Status
	node.BasePath = input.BasePath
	node.BackupDir = input.BackupDir

	switch input.AuthType {
	case "password":
		node.Password = input.Password
		node.SSHKeyID = nil
		node.PrivateKey = ""
	case "key":
		node.Password = ""
		node.SSHKeyID = input.SSHKeyID
		if input.SSHKeyID == nil {
			node.PrivateKey = input.PrivateKey
		} else {
			node.PrivateKey = ""
		}
	}

	applyOptionalFields(node, input)

	if err := s.repo.Update(ctx, node, updateStatus, applyTimes); err != nil {
		if errors.Is(err, repository.ErrNodeSSHKeyNotFound) {
			return nil, "", validationError("所选密钥不存在，请重新选择")
		}
		if errors.Is(err, apperr.ErrValidation) {
			return nil, "", err
		}
		err = apperr.WrapDBError(err)
		return nil, "", err
	}

	// Reload with associations
	nodePtr, err := s.repo.FindByIDWithSSHKey(ctx, node.ID)
	if err != nil {
		return nil, "", err
	}
	return nodePtr, oldBackupDir, nil
}

// Delete deletes a node and its associated policy links, tasks, and alerts in
// a single transaction.
func (s *NodeService) Delete(ctx context.Context, id uint) error {
	return s.repo.DeleteWithAssociations(ctx, id)
}

// BatchDelete deletes multiple nodes and their associated records. It returns
// the number of deleted records and the list of IDs that were not found.
func (s *NodeService) BatchDelete(ctx context.Context, ids []uint) (int64, []uint, error) {
	return s.repo.BatchDeleteWithAssociations(ctx, ids)
}

// --- validation helpers ---

func (s *NodeService) validateSSHRef(ctx context.Context, input CreateNodeInput) error {
	switch input.AuthType {
	case "password":
		if input.Password == "" {
			return fmt.Errorf("密码认证模式下请填写密码")
		}
		return nil
	case "key":
		if input.SSHKeyID == nil && input.PrivateKey == "" {
			return fmt.Errorf("密钥认证模式下请选择已有密钥或填写私钥内容")
		}
		if input.SSHKeyID != nil {
			_, err := s.repo.FindSSHKeyByID(ctx, *input.SSHKeyID)
			if err != nil {
				return fmt.Errorf("所选密钥不存在，请重新选择")
			}
		}
		return nil
	default:
		return fmt.Errorf("不支持的认证方式")
	}
}

func validateNodeName(name string) error {
	if strings.ContainsAny(name, "/\\") || strings.Contains(name, "..") || strings.ContainsRune(name, 0) {
		return fmt.Errorf("节点名称不能包含 /、\\、.. 或空字符")
	}
	return nil
}

// ValidateNodeHostPort validates a host and port combination for use as a node address.
// Exported for use by config import validation.
func ValidateNodeHostPort(host string, port int) error {
	trimmedHost := strings.TrimSpace(host)
	if trimmedHost == "" {
		return fmt.Errorf("主机地址不能为空")
	}
	// 拒绝 localhost / 回环地址，防止 SSRF 或误操作管理服务器自身
	lower := strings.ToLower(trimmedHost)
	if lower == "localhost" || lower == "localhost.localdomain" {
		return fmt.Errorf("不允许将管理服务器自身（localhost）添加为节点")
	}
	if ip := net.ParseIP(trimmedHost); ip != nil {
		if ip.IsLoopback() {
			return fmt.Errorf("不允许将回环地址添加为节点")
		}
		if ip.IsUnspecified() {
			return fmt.Errorf("不允许使用未指定地址（0.0.0.0/::）作为节点主机")
		}
		if ip.IsLinkLocalUnicast() {
			return fmt.Errorf("不允许使用链路本地地址作为节点主机")
		}
		if ip.IsMulticast() {
			return fmt.Errorf("不允许使用组播地址作为节点主机")
		}
		if ip.IsInterfaceLocalMulticast() {
			return fmt.Errorf("不允许使用组播地址作为节点主机")
		}
	} else {
		// 不是 IP，检查是否是合法的 hostname
		if len(trimmedHost) > 253 {
			return fmt.Errorf("主机名过长")
		}
		if !nodeHostnameRegexp.MatchString(trimmedHost) {
			return fmt.Errorf("主机地址格式不合法")
		}
	}
	if port < 1 || port > 65535 {
		return fmt.Errorf("端口号必须在 1-65535 之间")
	}
	return nil
}

func validateBackupDir(dir string) error {
	if dir == "" {
		return fmt.Errorf("备份目录标识不能为空")
	}
	if len(dir) > 128 {
		return fmt.Errorf("备份目录标识长度不能超过 128 个字符")
	}
	if strings.ContainsAny(dir, "/\\") || strings.Contains(dir, "..") || strings.ContainsRune(dir, 0) {
		return fmt.Errorf("备份目录标识不能包含 /、\\、.. 或空字符")
	}
	return nil
}

// SanitizeBackupDir derives the canonical backup directory identifier from a node name.
func SanitizeBackupDir(name string) string {
	s := strings.ToLower(name)
	var buf strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			buf.WriteRune(r)
		} else {
			buf.WriteByte('-')
		}
	}
	// collapse consecutive dashes
	result := consecutiveDashRegexp.ReplaceAllString(buf.String(), "-")
	result = strings.Trim(result, "-")
	if len(result) < 2 {
		return ""
	}
	return result
}

// --- helpers ---

type optionalTime struct {
	set   bool
	value *time.Time
}

func parseOptionalTime(raw *string, field string) (optionalTime, error) {
	if raw == nil {
		return optionalTime{}, nil
	}
	if *raw == "" {
		return optionalTime{set: true}, nil
	}
	parsed, err := time.Parse(time.RFC3339, *raw)
	if err != nil {
		return optionalTime{}, validationError(field + " must be an RFC3339 timestamp")
	}
	parsed = parsed.UTC().Truncate(time.Microsecond)
	if parsed.Year() < 0 || parsed.Year() > 9999 {
		return optionalTime{}, validationError(field + " must be an RFC3339 timestamp")
	}
	return optionalTime{set: true, value: &parsed}, nil
}

func optionalTimePatch(input CreateNodeInput) (func(*model.Node) error, error) {
	start, err := parseOptionalTime(input.MaintenanceStart, "maintenance_start")
	if err != nil {
		return nil, err
	}
	end, err := parseOptionalTime(input.MaintenanceEnd, "maintenance_end")
	if err != nil {
		return nil, err
	}
	expiry, err := parseOptionalTime(input.ExpiryDate, "expiry_date")
	if err != nil {
		return nil, err
	}
	if !start.set && !end.set && !expiry.set {
		return nil, nil
	}
	return func(node *model.Node) error {
		maintenanceStart, maintenanceEnd := node.MaintenanceStart, node.MaintenanceEnd
		startChanged := setOptionalTime(&maintenanceStart, start)
		endChanged := setOptionalTime(&maintenanceEnd, end)
		if startChanged || endChanged {
			if (maintenanceStart == nil) != (maintenanceEnd == nil) ||
				(maintenanceStart != nil && !maintenanceEnd.Truncate(time.Microsecond).After(maintenanceStart.Truncate(time.Microsecond))) {
				return validationError("maintenance_start and maintenance_end must both be set with maintenance_end after maintenance_start, or both cleared")
			}
		}
		node.MaintenanceStart = maintenanceStart
		node.MaintenanceEnd = maintenanceEnd
		setOptionalTime(&node.ExpiryDate, expiry)
		return nil
	}, nil
}

func setOptionalTime(current **time.Time, patch optionalTime) bool {
	if !patch.set {
		return false
	}
	if *current == nil && patch.value == nil {
		return false
	}
	if *current != nil && patch.value != nil && (*current).UTC().Truncate(time.Microsecond).Equal(*patch.value) {
		return false
	}
	*current = patch.value
	return true
}

func applyOptionalFields(node *model.Node, input CreateNodeInput) {
	if input.Archived != nil {
		node.Archived = *input.Archived
	}
	if input.UseSudo != nil {
		node.UseSudo = *input.UseSudo
	}
}
