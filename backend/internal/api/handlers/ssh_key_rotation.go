package handlers

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"xirang/backend/internal/credentialaudit"
	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"
	"xirang/backend/internal/sshutil"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/mattn/go-sqlite3"
	"golang.org/x/crypto/ssh"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	sshKeyRotationTotalBudget = 20 * time.Second
	sshKeyRotationSSHBudget   = 15 * time.Second
	sshKeyRotationNodeBudget  = 5 * time.Second
	sshKeyRotationWorkers     = 4
	sshKeyRotationMaxNodes    = 256
	sshKeyRotationBodyLimit   = 1 << 20
)

const (
	sshKeyRotationStatusSaved    = "saved"
	sshKeyRotationStatusNotSaved = "not_saved"

	sshKeyRotationReasonValidationFailed  = "validation_failed"
	sshKeyRotationReasonValidationTimeout = "validation_timeout"
	sshKeyRotationReasonConflict          = "conflict"
	sshKeyRotationReasonScopeBlocked      = "scope_blocked"
	sshKeyRotationReasonTrustUnavailable  = "trust_unavailable"
	sshKeyRotationReasonInventoryLimit    = "inventory_limit"
	sshKeyRotationReasonBusy              = "busy"

	sshKeyRotationResultVerified = "verified"
	sshKeyRotationResultFailed   = "failed"
	sshKeyRotationResultUnknown  = "unknown"

	sshKeyRotationErrorScopeDenied      = "scope_denied"
	sshKeyRotationErrorHostKeyUnknown   = "ssh_host_key_unknown"
	sshKeyRotationErrorHostKeyMismatch  = "ssh_host_key_mismatch"
	sshKeyRotationErrorConnectionFailed = "connection_failed"
	sshKeyRotationErrorTimeout          = "timeout"
	sshKeyRotationErrorNotChecked       = "not_checked"
)

var (
	errSSHKeyRotationConflict       = errors.New("ssh key rotation conflict")
	errSSHKeyRotationSessionInvalid = errors.New("ssh key rotation session invalid")
	errSSHKeyRotationBusy           = errors.New("ssh key rotation lock busy")
	errSSHKeyRotationInternal       = errors.New("ssh key rotation internal error")
	errSSHKeyRotationScopeBlocked   = errors.New("ssh key rotation scope blocked")
	errSSHKeyRotationBodyDeadline   = errors.New("ssh key rotation request body deadline")
)

type sshKeyRotationRequest struct {
	PrivateKey string  `json:"private_key"`
	KeyType    string  `json:"key_type,omitempty"`
	Name       *string `json:"name,omitempty"`
}

// UnmarshalJSON keeps the rotation request closed: only the three approved
// fields are accepted, and null is not a substitute for a required value.
func (r *sshKeyRotationRequest) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if fields == nil {
		return errors.New("rotation request must be an object")
	}
	for field := range fields {
		switch field {
		case "private_key", "key_type", "name":
		default:
			return fmt.Errorf("unknown rotation field %q", field)
		}
	}
	privateKey, ok := fields["private_key"]
	if !ok || bytes.Equal(bytes.TrimSpace(privateKey), []byte("null")) {
		return errors.New("private_key is required")
	}
	if err := json.Unmarshal(privateKey, &r.PrivateKey); err != nil {
		return err
	}
	if keyType, ok := fields["key_type"]; ok {
		if bytes.Equal(bytes.TrimSpace(keyType), []byte("null")) {
			return errors.New("key_type must be a string")
		}
		if err := json.Unmarshal(keyType, &r.KeyType); err != nil {
			return err
		}
	}
	if name, ok := fields["name"]; ok {
		if bytes.Equal(bytes.TrimSpace(name), []byte("null")) {
			return errors.New("name must be a string")
		}
		var value string
		if err := json.Unmarshal(name, &value); err != nil {
			return err
		}
		r.Name = &value
	}
	return nil
}

type sshKeyRotationNodeResult struct {
	NodeID    uint   `json:"node_id"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	ErrorCode string `json:"error_code,omitempty"`
}

type sshKeyRotationResponse struct {
	Status               string                     `json:"status"`
	Reason               string                     `json:"reason"`
	PublicKeyFingerprint string                     `json:"public_key_fingerprint"`
	Results              []sshKeyRotationNodeResult `json:"results"`
}

type sshKeyRotationKeySnapshot struct {
	ID              uint
	Name            string
	Username        string
	KeyType         string
	PrivateKey      string
	Fingerprint     string
	Disabled        bool
	ExpiresAt       *time.Time
	AllowedPurposes string
	AllowedNodeIDs  string
	AllowedNodeTags string
	CreatedAt       time.Time
}

type sshKeyRotationNodeSnapshot struct {
	ID       uint
	Name     string
	Host     string
	Port     int
	Username string
	AuthType string
	SSHKeyID *uint
	Tags     string
	Archived bool
}

type sshKeyRotationSnapshot struct {
	Key   sshKeyRotationKeySnapshot
	Nodes []sshKeyRotationNodeSnapshot
}

type sshKeyRotationAuditState struct {
	nodeCount    int
	successCount int
	failureCount int
	unknownCount int
	saved        bool
	reason       string
}

func (state sshKeyRotationAuditState) outcome() string {
	if state.saved {
		return credentialaudit.OutcomeSuccess
	}
	switch state.reason {
	case sshKeyRotationReasonScopeBlocked,
		sshKeyRotationReasonTrustUnavailable,
		sshKeyRotationReasonInventoryLimit,
		sshKeyRotationReasonBusy:
		return credentialaudit.OutcomeBlocked
	default:
		return credentialaudit.OutcomeFailure
	}
}

func (h *SSHKeyHandler) writeSSHKeyRotationAudit(c *gin.Context, keyID uint, state sshKeyRotationAuditState) {
	if h == nil || h.db == nil {
		return
	}
	auditCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	keyIDCopy := keyID
	// The shared audit sanitizer drops empty strings; retain an explicit safe
	// reason even when the wire result uses an empty reason for success.
	reason := state.reason
	if reason == "" {
		reason = "request_rejected"
		if state.saved {
			reason = "saved"
		}
	}
	writeCredentialAuditFromGin(c, h.db.WithContext(auditCtx), credentialaudit.Event{
		Action:           "ssh_key.rotate",
		Purpose:          sshutil.PurposeSSHKeyTest,
		CredentialKind:   "ssh_key",
		CredentialSource: fmt.Sprintf("ssh_key_id=%d", keyID),
		SSHKeyID:         &keyIDCopy,
		Outcome:          state.outcome(),
		Metadata: map[string]any{
			"node_count":    state.nodeCount,
			"success_count": state.successCount,
			"failure_count": state.failureCount,
			"unknown_count": state.unknownCount,
			"saved":         state.saved,
			"reason":        reason,
		},
	})
}

func rotationRequestContext(c *gin.Context) context.Context {
	if c != nil && c.Request != nil && c.Request.Context() != nil {
		return c.Request.Context()
	}
	return context.Background()
}

func decodeSSHKeyRotationRequest(body []byte) (sshKeyRotationRequest, error) {
	var request sshKeyRotationRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return sshKeyRotationRequest{}, err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return sshKeyRotationRequest{}, errors.New("multiple JSON documents")
		}
		return sshKeyRotationRequest{}, err
	}
	return request, nil
}

func validateSSHKeyRotationSession(ctx context.Context, db *gorm.DB, binding middleware.SessionBinding) error {
	if db == nil {
		return fmt.Errorf("rotation database unavailable")
	}
	if binding.UserID == 0 || strings.TrimSpace(binding.JTI) == "" || binding.Role != "admin" || !binding.ExpiresAt.After(time.Now().UTC()) {
		return errSSHKeyRotationSessionInvalid
	}
	var user struct {
		Role         string `gorm:"column:role"`
		TokenVersion uint   `gorm:"column:token_version"`
	}
	result := db.WithContext(ctx).Table("users").Select("role", "token_version").Where("id = ?", binding.UserID).Take(&user)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return errSSHKeyRotationSessionInvalid
		}
		return fmt.Errorf("validate rotation session user: %w", result.Error)
	}
	if user.Role != "admin" || user.Role != binding.Role || user.TokenVersion != binding.TokenVersion {
		return errSSHKeyRotationSessionInvalid
	}
	var revocation model.TokenRevocation
	result = db.WithContext(ctx).Select("id").Where(
		"token_hash = ? AND expires_at > ?", "jti:"+binding.JTI, time.Now().UTC(),
	).Limit(1).Find(&revocation)
	if result.Error != nil {
		return fmt.Errorf("validate rotation session revocation: %w", result.Error)
	}
	if result.RowsAffected != 0 {
		return errSSHKeyRotationSessionInvalid
	}
	return nil
}

func cloneRotationTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := value.UTC()
	return &copy
}

func snapshotSSHKeyRotationKey(key model.SSHKey) sshKeyRotationKeySnapshot {
	return sshKeyRotationKeySnapshot{
		ID:              key.ID,
		Name:            key.Name,
		Username:        key.Username,
		KeyType:         key.KeyType,
		PrivateKey:      key.PrivateKey,
		Fingerprint:     key.Fingerprint,
		Disabled:        key.Disabled,
		ExpiresAt:       cloneRotationTime(key.ExpiresAt),
		AllowedPurposes: key.AllowedPurposes,
		AllowedNodeIDs:  key.AllowedNodeIDs,
		AllowedNodeTags: key.AllowedNodeTags,
		CreatedAt:       key.CreatedAt.UTC(),
	}
}

func snapshotSSHKeyRotationNode(node model.Node) sshKeyRotationNodeSnapshot {
	var keyID *uint
	if node.SSHKeyID != nil {
		value := *node.SSHKeyID
		keyID = &value
	}
	return sshKeyRotationNodeSnapshot{
		ID:       node.ID,
		Name:     node.Name,
		Host:     node.Host,
		Port:     node.Port,
		Username: node.Username,
		AuthType: node.AuthType,
		SSHKeyID: keyID,
		Tags:     node.Tags,
		Archived: node.Archived,
	}
}

func buildSSHKeyRotationSnapshot(key model.SSHKey, nodes []model.Node) sshKeyRotationSnapshot {
	snapshot := sshKeyRotationSnapshot{
		Key:   snapshotSSHKeyRotationKey(key),
		Nodes: make([]sshKeyRotationNodeSnapshot, 0, len(nodes)),
	}
	for _, node := range nodes {
		snapshot.Nodes = append(snapshot.Nodes, snapshotSSHKeyRotationNode(node))
	}
	return snapshot
}

func loadSSHKeyRotationNodes(db *gorm.DB, ctx context.Context, keyID uint, limit int) ([]model.Node, error) {
	var nodes []model.Node
	query := db.WithContext(ctx).Session(&gorm.Session{SkipHooks: true}).Model(&model.Node{}).
		Select("id", "name", "host", "port", "username", "auth_type", "ssh_key_id", "tags", "archived").
		Where("ssh_key_id = ?", keyID).Order("id ASC")
	if limit > 0 {
		query = query.Limit(limit)
	}
	if err := query.Find(&nodes).Error; err != nil {
		return nil, err
	}
	return nodes, nil
}

func newSSHKeyRotationResults(nodes []model.Node) []sshKeyRotationNodeResult {
	results := make([]sshKeyRotationNodeResult, len(nodes))
	for index, node := range nodes {
		results[index] = sshKeyRotationNodeResult{
			NodeID:    node.ID,
			Name:      node.Name,
			Status:    sshKeyRotationResultUnknown,
			ErrorCode: sshKeyRotationErrorNotChecked,
		}
	}
	return results
}

func markSSHKeyRotationScopeDenied(results []sshKeyRotationNodeResult) {
	for index := range results {
		results[index].Status = sshKeyRotationResultFailed
		results[index].ErrorCode = sshKeyRotationErrorScopeDenied
	}
}

func summarizeSSHKeyRotationResults(results []sshKeyRotationNodeResult) (successCount, failureCount, unknownCount int) {
	for _, result := range results {
		switch result.Status {
		case sshKeyRotationResultVerified:
			successCount++
		case sshKeyRotationResultFailed:
			failureCount++
		default:
			unknownCount++
		}
	}
	return successCount, failureCount, unknownCount
}

func allSSHKeyRotationResultsVerified(results []sshKeyRotationNodeResult) bool {
	for _, result := range results {
		if result.Status != sshKeyRotationResultVerified {
			return false
		}
	}
	return true
}

func sshKeyRotationValidationReason(results []sshKeyRotationNodeResult) string {
	for _, result := range results {
		if result.ErrorCode == sshKeyRotationErrorScopeDenied {
			return sshKeyRotationReasonScopeBlocked
		}
	}
	for _, result := range results {
		if result.Status == sshKeyRotationResultUnknown {
			return sshKeyRotationReasonValidationTimeout
		}
	}
	for _, result := range results {
		if result.Status != sshKeyRotationResultVerified {
			return sshKeyRotationReasonValidationFailed
		}
	}
	return ""
}

func sshKeyRotationDialResult(err error, validationCtx context.Context) (status, errorCode string) {
	if err == nil {
		return sshKeyRotationResultVerified, ""
	}
	var hostKeyErr *sshutil.HostKeyError
	if errors.As(err, &hostKeyErr) {
		switch hostKeyErr.Kind {
		case sshutil.HostKeyMismatch:
			return sshKeyRotationResultFailed, sshKeyRotationErrorHostKeyMismatch
		default:
			return sshKeyRotationResultFailed, sshKeyRotationErrorHostKeyUnknown
		}
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(validationCtx.Err(), context.DeadlineExceeded) || isSSHKeyRotationNetworkTimeout(err) {
		return sshKeyRotationResultUnknown, sshKeyRotationErrorTimeout
	}
	if errors.Is(err, context.Canceled) || errors.Is(validationCtx.Err(), context.Canceled) {
		return sshKeyRotationResultUnknown, sshKeyRotationErrorNotChecked
	}
	return sshKeyRotationResultFailed, sshKeyRotationErrorConnectionFailed
}

func isSSHKeyRotationNetworkTimeout(err error) bool {
	var networkErr net.Error
	return errors.As(err, &networkErr) && networkErr.Timeout()
}

func validateSSHKeyRotationNodes(
	validationCtx context.Context,
	nodes []model.Node,
	candidate model.SSHKey,
	eligible []int,
	authMethods []ssh.AuthMethod,
	hostKeyCallback ssh.HostKeyCallback,
	results []sshKeyRotationNodeResult,
) {
	jobs := make(chan int)
	var workers sync.WaitGroup
	for range sshKeyRotationWorkers {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range jobs {
				if validationCtx.Err() != nil {
					results[index].Status = sshKeyRotationResultUnknown
					results[index].ErrorCode = sshKeyRotationErrorTimeout
					continue
				}
				node := nodes[index]
				node.AuthType = "key"
				candidateID := candidate.ID
				node.SSHKey = &candidate
				node.SSHKeyID = &candidateID
				address := net.JoinHostPort(node.Host, strconv.Itoa(node.Port))
				nodeCtx, cancel := context.WithTimeout(validationCtx, sshKeyRotationNodeBudget)
				client, err := sshutil.DialSSHForRotation(nodeCtx, address, node.Username, authMethods, hostKeyCallback)
				cancel()
				if err == nil {
					_ = client.Close()
					results[index].Status = sshKeyRotationResultVerified
					results[index].ErrorCode = ""
					continue
				}
				status, errorCode := sshKeyRotationDialResult(err, validationCtx)
				results[index].Status = status
				results[index].ErrorCode = errorCode
			}
		}()
	}
	for _, index := range eligible {
		select {
		case jobs <- index:
		case <-validationCtx.Done():
			close(jobs)
			workers.Wait()
			for _, pending := range eligible {
				if results[pending].Status == sshKeyRotationResultUnknown && results[pending].ErrorCode == sshKeyRotationErrorNotChecked {
					results[pending].ErrorCode = sshKeyRotationErrorTimeout
				}
			}
			return
		}
	}
	close(jobs)
	workers.Wait()
	if validationCtx.Err() != nil {
		for _, index := range eligible {
			if results[index].Status == sshKeyRotationResultUnknown && results[index].ErrorCode == sshKeyRotationErrorNotChecked {
				results[index].ErrorCode = sshKeyRotationErrorTimeout
			}
		}
	}
}

func prepareSSHKeyRotationCandidate(current model.SSHKey, request sshKeyRotationRequest) (model.SSHKey, string, error) {
	name := current.Name
	nameProvided := request.Name != nil
	if nameProvided {
		name = strings.TrimSpace(*request.Name)
		if name == "" {
			return model.SSHKey{}, "", errors.New("rotation name is empty")
		}
	}
	normalizedName, username, storedType, preparedKey, err := normalizeSSHKeyInput(name, current.Username, request.KeyType, request.PrivateKey)
	if err != nil {
		return model.SSHKey{}, "", err
	}
	candidate := current
	candidate.Name = normalizedName
	if !nameProvided {
		candidate.Name = current.Name
	}
	candidate.Username = username
	candidate.KeyType = storedType
	candidate.PrivateKey = preparedKey
	candidate.Fingerprint = generateFingerprint(preparedKey)
	publicKey, err := sshutil.DerivePublicKey(preparedKey)
	if err != nil {
		return model.SSHKey{}, "", err
	}
	publicKeyFingerprint := publicKeyFingerprint(publicKey)
	if publicKeyFingerprint == "" {
		return model.SSHKey{}, "", errors.New("derived public key is invalid")
	}
	return candidate, publicKeyFingerprint, nil
}

func sameSSHKeyRotationTime(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}

func sameSSHKeyRotationKey(snapshot sshKeyRotationKeySnapshot, current model.SSHKey) bool {
	return snapshot.ID == current.ID &&
		snapshot.Name == current.Name &&
		snapshot.Username == current.Username &&
		snapshot.KeyType == current.KeyType &&
		snapshot.PrivateKey == current.PrivateKey &&
		snapshot.Fingerprint == current.Fingerprint &&
		snapshot.Disabled == current.Disabled &&
		sameSSHKeyRotationTime(snapshot.ExpiresAt, current.ExpiresAt) &&
		snapshot.AllowedPurposes == current.AllowedPurposes &&
		snapshot.AllowedNodeIDs == current.AllowedNodeIDs &&
		snapshot.AllowedNodeTags == current.AllowedNodeTags &&
		snapshot.CreatedAt.Equal(current.CreatedAt)
}

func sameSSHKeyRotationNode(snapshot sshKeyRotationNodeSnapshot, current model.Node) bool {
	if snapshot.ID != current.ID || snapshot.Name != current.Name || snapshot.Host != current.Host ||
		snapshot.Port != current.Port || snapshot.Username != current.Username || snapshot.AuthType != current.AuthType ||
		snapshot.Tags != current.Tags || snapshot.Archived != current.Archived {
		return false
	}
	if snapshot.SSHKeyID == nil || current.SSHKeyID == nil {
		return snapshot.SSHKeyID == nil && current.SSHKeyID == nil
	}
	return *snapshot.SSHKeyID == *current.SSHKeyID
}

func sameSSHKeyRotationNodes(snapshot []sshKeyRotationNodeSnapshot, current []model.Node) bool {
	if len(snapshot) != len(current) {
		return false
	}
	for index, node := range current {
		if !sameSSHKeyRotationNode(snapshot[index], node) {
			return false
		}
	}
	return true
}

func isSSHKeyRotationLockError(err error) bool {
	if err == nil {
		return false
	}
	var postgresErr *pgconn.PgError
	if errors.As(err, &postgresErr) {
		return postgresErr.Code == "55P03" || postgresErr.Code == "40P01"
	}
	var sqliteErr sqlite3.Error
	if errors.As(err, &sqliteErr) {
		return sqliteErr.Code == sqlite3.ErrBusy || sqliteErr.Code == sqlite3.ErrLocked
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "database is locked") || strings.Contains(message, "database table is locked")
}

func isSSHKeyRotationLockErrorForTx(tx *gorm.DB, err error) bool {
	if isSSHKeyRotationLockError(err) {
		return true
	}
	return tx != nil && tx.Name() == "sqlite" &&
		(errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled))
}

func lockSSHKeyRotationInventory(tx *gorm.DB) error {
	switch tx.Name() {
	case "postgres":
		if err := tx.Exec("SET LOCAL lock_timeout = '1500ms'").Error; err != nil {
			return err
		}
		if err := tx.Exec("LOCK TABLE nodes IN SHARE ROW EXCLUSIVE MODE").Error; err != nil {
			if isSSHKeyRotationLockErrorForTx(tx, err) {
				return fmt.Errorf("%w: %v", errSSHKeyRotationBusy, err)
			}
			return err
		}
	case "sqlite":
		if err := tx.Exec("UPDATE nodes SET id = id WHERE 1 = 0").Error; err != nil {
			if isSSHKeyRotationLockErrorForTx(tx, err) {
				return fmt.Errorf("%w: %v", errSSHKeyRotationBusy, err)
			}
			return err
		}
	}
	return nil
}

func lockAndValidateSSHKeyRotationSession(ctx context.Context, tx *gorm.DB, binding middleware.SessionBinding) error {
	if binding.UserID == 0 || strings.TrimSpace(binding.JTI) == "" || binding.Role != "admin" || !binding.ExpiresAt.After(time.Now().UTC()) {
		return errSSHKeyRotationSessionInvalid
	}
	var user struct {
		Role         string `gorm:"column:role"`
		TokenVersion uint   `gorm:"column:token_version"`
	}
	result := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "SHARE"}).Table("users").
		Select("role", "token_version").Where("id = ?", binding.UserID).Take(&user)
	if result.Error != nil {
		if isSSHKeyRotationLockErrorForTx(tx, result.Error) {
			return fmt.Errorf("%w: %v", errSSHKeyRotationBusy, result.Error)
		}
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return errSSHKeyRotationSessionInvalid
		}
		return result.Error
	}
	if user.Role != "admin" || user.Role != binding.Role || user.TokenVersion != binding.TokenVersion {
		return errSSHKeyRotationSessionInvalid
	}
	if tx.Name() == "postgres" {
		if err := tx.Exec("LOCK TABLE token_revocations IN SHARE MODE").Error; err != nil {
			if isSSHKeyRotationLockErrorForTx(tx, err) {
				return fmt.Errorf("%w: %v", errSSHKeyRotationBusy, err)
			}
			return err
		}
	}
	var revocation model.TokenRevocation
	result = tx.WithContext(ctx).Select("id").Where(
		"token_hash = ? AND expires_at > ?", "jti:"+binding.JTI, time.Now().UTC(),
	).Limit(1).Find(&revocation)
	if result.Error != nil {
		if isSSHKeyRotationLockErrorForTx(tx, result.Error) {
			return fmt.Errorf("%w: %v", errSSHKeyRotationBusy, result.Error)
		}
		return result.Error
	}
	if result.RowsAffected != 0 {
		return errSSHKeyRotationSessionInvalid
	}
	return nil
}

func loadSSHKeyRotationNodesForCommit(ctx context.Context, tx *gorm.DB, keyID uint) ([]model.Node, error) {
	var nodes []model.Node
	result := tx.WithContext(ctx).Session(&gorm.Session{SkipHooks: true}).Model(&model.Node{}).
		Select("id", "name", "host", "port", "username", "auth_type", "ssh_key_id", "tags", "archived").
		Where("ssh_key_id = ?", keyID).Order("id ASC").Find(&nodes)
	if result.Error != nil {
		return nil, result.Error
	}
	return nodes, nil
}

func commitSSHKeyRotationTx(
	ctx context.Context,
	tx *gorm.DB,
	binding middleware.SessionBinding,
	snapshot sshKeyRotationSnapshot,
	candidate model.SSHKey,
	encryptedPrivateKey string,
) error {
	if err := lockSSHKeyRotationInventory(tx); err != nil {
		return err
	}
	var currentKey model.SSHKey
	keyResult := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", snapshot.Key.ID).First(&currentKey)
	if keyResult.Error != nil {
		if isSSHKeyRotationLockErrorForTx(tx, keyResult.Error) {
			return fmt.Errorf("%w: %v", errSSHKeyRotationBusy, keyResult.Error)
		}
		if errors.Is(keyResult.Error, gorm.ErrRecordNotFound) {
			return errSSHKeyRotationConflict
		}
		return keyResult.Error
	}
	currentNodes, err := loadSSHKeyRotationNodesForCommit(ctx, tx, snapshot.Key.ID)
	if err != nil {
		if isSSHKeyRotationLockErrorForTx(tx, err) {
			return fmt.Errorf("%w: %v", errSSHKeyRotationBusy, err)
		}
		return err
	}
	if !sameSSHKeyRotationKey(snapshot.Key, currentKey) || !sameSSHKeyRotationNodes(snapshot.Nodes, currentNodes) {
		return errSSHKeyRotationConflict
	}
	if err := lockAndValidateSSHKeyRotationSession(ctx, tx, binding); err != nil {
		return err
	}
	if err := sshutil.ValidateSSHKeyPurpose(candidate, sshutil.PurposeSSHKeyTest); err != nil {
		return errSSHKeyRotationScopeBlocked
	}
	for _, node := range currentNodes {
		if err := sshutil.ValidateSSHKeyScope(candidate, node, sshutil.PurposeSSHKeyTest); err != nil {
			return errSSHKeyRotationScopeBlocked
		}
	}
	if candidate.ID != snapshot.Key.ID {
		return errSSHKeyRotationConflict
	}
	if !binding.ExpiresAt.After(time.Now().UTC()) {
		return errSSHKeyRotationSessionInvalid
	}
	updates := map[string]any{
		"name":        candidate.Name,
		"key_type":    candidate.KeyType,
		"private_key": encryptedPrivateKey,
		"fingerprint": candidate.Fingerprint,
		"updated_at":  time.Now().UTC(),
	}
	updateResult := tx.Model(&model.SSHKey{}).Where("id = ?", snapshot.Key.ID).Updates(updates)
	if updateResult.Error != nil {
		if isSSHKeyDuplicateError(updateResult.Error) {
			return fmt.Errorf("%w: duplicate key name", errSSHKeyRotationConflict)
		}
		if isSSHKeyRotationLockErrorForTx(tx, updateResult.Error) {
			return fmt.Errorf("%w: %v", errSSHKeyRotationBusy, updateResult.Error)
		}
		return updateResult.Error
	}
	if updateResult.RowsAffected != 1 {
		if updateResult.RowsAffected == 0 {
			return errSSHKeyRotationConflict
		}
		return fmt.Errorf("rotation update affected unexpected rows: %d", updateResult.RowsAffected)
	}
	return nil
}

// commitSSHKeyRotationSQLiteOneShot intentionally performs no retry. BEGIN
// errors and callback errors followed by a successful rollback are known not
// to have committed and may be classified as busy. A Commit error is
// deliberately returned unchanged because its outcome is ambiguous.
func commitSSHKeyRotationSQLiteOneShot(
	ctx context.Context,
	db *gorm.DB,
	binding middleware.SessionBinding,
	snapshot sshKeyRotationSnapshot,
	candidate model.SSHKey,
	encryptedPrivateKey string,
) error {
	tx := db.WithContext(ctx).Begin()
	if tx.Error != nil {
		if isSSHKeyRotationLockError(tx.Error) || errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(ctx.Err(), context.Canceled) {
			return fmt.Errorf("%w: %v", errSSHKeyRotationBusy, tx.Error)
		}
		return tx.Error
	}
	bodyErr := commitSSHKeyRotationTx(ctx, tx, binding, snapshot, candidate, encryptedPrivateKey)
	if bodyErr != nil {
		rollbackErr := tx.Rollback().Error
		if rollbackErr != nil {
			return fmt.Errorf("SQLite rotation rollback failed after body error: %v; rollback: %v", bodyErr, rollbackErr)
		}
		if isSSHKeyRotationLockErrorForTx(tx, bodyErr) {
			return fmt.Errorf("%w: %v", errSSHKeyRotationBusy, bodyErr)
		}
		return bodyErr
	}
	// Never classify Commit as busy: SQLite may have committed before the
	// driver reported an error.
	return tx.Commit().Error
}

func discardSSHKeyRotationSQLiteConnection(conn *sql.Conn) {
	if conn == nil {
		return
	}
	_ = conn.Raw(func(any) error { return driver.ErrBadConn })
}

// commitSSHKeyRotationSQLite pins one SQL connection, disables only that
// connection's busy wait for the one-shot transaction, and restores the
// production PRAGMA before the connection returns to the pool.
func commitSSHKeyRotationSQLite(
	ctx context.Context,
	db *gorm.DB,
	binding middleware.SessionBinding,
	snapshot sshKeyRotationSnapshot,
	candidate model.SSHKey,
	encryptedPrivateKey string,
) (resultErr error) {
	return db.WithContext(ctx).Connection(func(connDB *gorm.DB) (resultErr error) {
		conn, ok := connDB.Statement.ConnPool.(*sql.Conn)
		if !ok || conn == nil {
			return errors.New("sqlite rotation did not receive a pinned SQL connection")
		}
		var originalBusyTimeout int64
		if err := connDB.WithContext(ctx).Raw("PRAGMA busy_timeout").Scan(&originalBusyTimeout).Error; err != nil {
			return err
		}
		defer func() {
			restoreCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
			defer cancel()
			restoreErr := connDB.WithContext(restoreCtx).Exec(fmt.Sprintf("PRAGMA busy_timeout = %d", originalBusyTimeout)).Error
			if restoreErr == nil {
				return
			}
			discardSSHKeyRotationSQLiteConnection(conn)
			restoreErr = fmt.Errorf("restore SQLite busy timeout: %w", restoreErr)
			if resultErr == nil {
				resultErr = restoreErr
				return
			}
			resultErr = errors.Join(resultErr, restoreErr)
		}()
		if err := connDB.WithContext(ctx).Exec("PRAGMA busy_timeout = 0").Error; err != nil {
			return err
		}
		return commitSSHKeyRotationSQLiteOneShot(ctx, connDB, binding, snapshot, candidate, encryptedPrivateKey)
	})
}

func commitSSHKeyRotation(
	ctx context.Context,
	db *gorm.DB,
	binding middleware.SessionBinding,
	snapshot sshKeyRotationSnapshot,
	candidate model.SSHKey,
) error {
	if db == nil {
		return errors.New("rotation database unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	encryptedPrivateKey, err := secure.EncryptIfNeeded(candidate.PrivateKey)
	if err != nil {
		return fmt.Errorf("encrypt rotated SSH key: %w", err)
	}
	if db.Name() == "sqlite" {
		return commitSSHKeyRotationSQLite(ctx, db, binding, snapshot, candidate, encryptedPrivateKey)
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return commitSSHKeyRotationTx(ctx, tx, binding, snapshot, candidate, encryptedPrivateKey)
	})
}

func readSSHKeyRotationBody(c *gin.Context, ctx context.Context) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if c == nil || c.Request == nil || c.Request.Body == nil {
		return nil, errors.New("rotation request body unavailable")
	}
	// ResponseController follows Gin's Unwrap method to the server connection.
	controller := http.NewResponseController(c.Writer)
	deadline, hasDeadline := ctx.Deadline()
	deadlineSet := false
	if hasDeadline {
		if err := controller.SetReadDeadline(deadline); err != nil {
			if !errors.Is(err, http.ErrNotSupported) {
				return nil, fmt.Errorf("%w: set request read deadline: %v", errSSHKeyRotationBodyDeadline, err)
			}
		} else {
			deadlineSet = true
		}
	}

	var readDone, watcherDone chan struct{}
	if deadlineSet {
		readDone = make(chan struct{})
		watcherDone = make(chan struct{})
		go func() {
			defer close(watcherDone)
			select {
			case <-ctx.Done():
				_ = controller.SetReadDeadline(time.Now())
			case <-readDone:
			}
		}()
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, sshKeyRotationBodyLimit)
	body, readErr := io.ReadAll(c.Request.Body)
	closeErr := c.Request.Body.Close()
	if closeErr != nil {
		if readErr == nil {
			readErr = closeErr
		} else {
			readErr = errors.Join(readErr, closeErr)
		}
	}
	if deadlineSet {
		close(readDone)
		<-watcherDone
		if restoreErr := controller.SetReadDeadline(time.Time{}); restoreErr != nil {
			restoreErr = fmt.Errorf("%w: restore request read deadline: %v", errSSHKeyRotationBodyDeadline, restoreErr)
			if readErr == nil {
				return body, restoreErr
			}
			return body, errors.Join(readErr, restoreErr)
		}
	}
	return body, readErr
}

// Rotate godoc
// @Summary      预验证并轮换 SSH Key
// @Description  在保存前使用候选私钥验证完整关联节点库存，并以短事务重新核对后原子保存
// @Tags         ssh-keys
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        id    path      int                           true  "SSH Key ID"
// @Param        body  body      handlers.sshKeyRotationRequest  true  "候选 SSH Key"
// @Success      200  {object}  handlers.Response{data=handlers.sshKeyRotationResponse}
// @Failure      400  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Failure      403  {object}  handlers.Response
// @Failure      404  {object}  handlers.Response
// @Failure      413  {object}  handlers.Response
// @Failure      500  {object}  handlers.Response
// @Router       /ssh-keys/{id}/rotate [post]
func (h *SSHKeyHandler) Rotate(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	totalCtx, cancelTotal := context.WithTimeout(rotationRequestContext(c), sshKeyRotationTotalBudget)
	defer cancelTotal()

	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	binding, bindingOK := middleware.CurrentSessionBinding(c)
	if !bindingOK || binding.UserID == 0 || strings.TrimSpace(binding.JTI) == "" {
		respondUnauthorized(c, "会话无效")
		return
	}
	if binding.Role != "admin" || middleware.CurrentRole(c) != "admin" {
		respondForbidden(c, "权限不足")
		return
	}

	auditState := sshKeyRotationAuditState{}
	defer func() { h.writeSSHKeyRotationAudit(c, id, auditState) }()
	if err := validateSSHKeyRotationSession(totalCtx, h.db, binding); err != nil {
		if errors.Is(err, errSSHKeyRotationSessionInvalid) {
			respondUnauthorized(c, "会话无效")
			return
		}
		respondInternalError(c, errSSHKeyRotationInternal)
		return
	}
	if c.Request == nil || c.Request.Body == nil {
		auditState.reason = sshKeyRotationReasonValidationFailed
		respondBadRequest(c, "请求参数不合法")
		return
	}
	body, err := readSSHKeyRotationBody(c, totalCtx)
	if err != nil {
		if errors.Is(err, errSSHKeyRotationBodyDeadline) {
			auditState.reason = sshKeyRotationReasonValidationFailed
			respondInternalError(c, errSSHKeyRotationInternal)
			return
		}
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			auditState.reason = sshKeyRotationReasonValidationFailed
			respondPayloadTooLarge(c, sshKeyPreviewTooLargeMessage)
			return
		}
		if totalCtx.Err() != nil || isSSHKeyRotationNetworkTimeout(err) {
			auditState.reason = sshKeyRotationReasonValidationTimeout
		} else {
			auditState.reason = sshKeyRotationReasonValidationFailed
		}
		respondBadRequest(c, "请求参数不合法")
		return
	}
	request, err := decodeSSHKeyRotationRequest(body)
	if err != nil || strings.TrimSpace(request.PrivateKey) == "" {
		auditState.reason = sshKeyRotationReasonValidationFailed
		respondBadRequest(c, "请求参数不合法")
		return
	}
	if request.Name != nil && strings.TrimSpace(*request.Name) == "" {
		auditState.reason = sshKeyRotationReasonValidationFailed
		respondBadRequest(c, "名称不能为空")
		return
	}

	var currentKey model.SSHKey
	keyResult := h.db.WithContext(totalCtx).First(&currentKey, id)
	if keyResult.Error != nil {
		if errors.Is(keyResult.Error, gorm.ErrRecordNotFound) {
			respondNotFound(c, "ssh key 不存在")
			return
		}
		respondInternalError(c, errSSHKeyRotationInternal)
		return
	}
	candidate, publicKeyFingerprint, err := prepareSSHKeyRotationCandidate(currentKey, request)
	if err != nil {
		auditState.reason = sshKeyRotationReasonValidationFailed
		respondBadRequest(c, "候选密钥无效")
		return
	}

	nodes, err := loadSSHKeyRotationNodes(h.db, totalCtx, id, sshKeyRotationMaxNodes+1)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(totalCtx.Err(), context.DeadlineExceeded) {
			auditState.reason = sshKeyRotationReasonValidationTimeout
			respondOK(c, sshKeyRotationResponse{
				Status:               sshKeyRotationStatusNotSaved,
				Reason:               sshKeyRotationReasonValidationTimeout,
				PublicKeyFingerprint: publicKeyFingerprint,
				Results:              []sshKeyRotationNodeResult{},
			})
			return
		}
		respondInternalError(c, errSSHKeyRotationInternal)
		return
	}
	auditState.nodeCount = len(nodes)
	results := newSSHKeyRotationResults(nodes)
	if err := sshutil.ValidateSSHKeyPurpose(candidate, sshutil.PurposeSSHKeyTest); err != nil {
		markSSHKeyRotationScopeDenied(results)
		auditState.reason = sshKeyRotationReasonScopeBlocked
		auditState.successCount, auditState.failureCount, auditState.unknownCount = summarizeSSHKeyRotationResults(results)
		respondOK(c, sshKeyRotationResponse{
			Status:               sshKeyRotationStatusNotSaved,
			Reason:               sshKeyRotationReasonScopeBlocked,
			PublicKeyFingerprint: publicKeyFingerprint,
			Results:              results,
		})
		return
	}
	if len(nodes) > sshKeyRotationMaxNodes {
		auditState.reason = sshKeyRotationReasonInventoryLimit
		auditState.successCount, auditState.failureCount, auditState.unknownCount = summarizeSSHKeyRotationResults(results)
		respondOK(c, sshKeyRotationResponse{
			Status:               sshKeyRotationStatusNotSaved,
			Reason:               sshKeyRotationReasonInventoryLimit,
			PublicKeyFingerprint: publicKeyFingerprint,
			Results:              results,
		})
		return
	}
	if err := totalCtx.Err(); err != nil {
		auditState.reason = sshKeyRotationReasonValidationTimeout
		auditState.successCount, auditState.failureCount, auditState.unknownCount = summarizeSSHKeyRotationResults(results)
		respondOK(c, sshKeyRotationResponse{
			Status:               sshKeyRotationStatusNotSaved,
			Reason:               sshKeyRotationReasonValidationTimeout,
			PublicKeyFingerprint: publicKeyFingerprint,
			Results:              results,
		})
		return
	}

	snapshot := buildSSHKeyRotationSnapshot(currentKey, nodes)
	eligible := make([]int, 0, len(nodes))
	for index, node := range nodes {
		if err := sshutil.ValidateSSHKeyScope(candidate, node, sshutil.PurposeSSHKeyTest); err != nil {
			results[index].Status = sshKeyRotationResultFailed
			results[index].ErrorCode = sshKeyRotationErrorScopeDenied
			continue
		}
		eligible = append(eligible, index)
	}
	if len(eligible) > 0 {
		first := nodes[eligible[0]]
		first.AuthType = "key"
		candidateID := candidate.ID
		first.SSHKey = &candidate
		first.SSHKeyID = &candidateID
		authMethods, _, authErr := sshutil.BuildSSHAuthForPurpose(first, nil, sshutil.PurposeSSHKeyTest)
		if authErr != nil {
			for _, index := range eligible {
				results[index].Status = sshKeyRotationResultFailed
				results[index].ErrorCode = sshKeyRotationErrorConnectionFailed
			}
		} else {
			hostKeyCallback, hostKeyErr := sshutil.ResolveSSHRotationHostKeyCallback()
			if hostKeyErr != nil {
				for _, index := range eligible {
					results[index].Status = sshKeyRotationResultUnknown
					results[index].ErrorCode = sshKeyRotationErrorNotChecked
				}
				auditState.reason = sshKeyRotationReasonTrustUnavailable
				for _, result := range results {
					if result.ErrorCode == sshKeyRotationErrorScopeDenied {
						auditState.reason = sshKeyRotationReasonScopeBlocked
						break
					}
				}
			} else {
				validationCtx, cancelValidation := context.WithTimeout(totalCtx, sshKeyRotationSSHBudget)
				validateSSHKeyRotationNodes(validationCtx, nodes, candidate, eligible, authMethods, hostKeyCallback, results)
				cancelValidation()
			}
		}
	}

	auditState.successCount, auditState.failureCount, auditState.unknownCount = summarizeSSHKeyRotationResults(results)
	if auditState.reason == "" && !allSSHKeyRotationResultsVerified(results) {
		auditState.reason = sshKeyRotationValidationReason(results)
	}
	if auditState.reason != "" || !allSSHKeyRotationResultsVerified(results) {
		if auditState.reason == "" {
			auditState.reason = sshKeyRotationReasonValidationFailed
		}
		respondOK(c, sshKeyRotationResponse{
			Status:               sshKeyRotationStatusNotSaved,
			Reason:               auditState.reason,
			PublicKeyFingerprint: publicKeyFingerprint,
			Results:              results,
		})
		return
	}
	if err := totalCtx.Err(); err != nil {
		auditState.reason = sshKeyRotationReasonValidationTimeout
		respondOK(c, sshKeyRotationResponse{
			Status:               sshKeyRotationStatusNotSaved,
			Reason:               sshKeyRotationReasonValidationTimeout,
			PublicKeyFingerprint: publicKeyFingerprint,
			Results:              results,
		})
		return
	}

	commitCtx, cancelCommit := context.WithTimeout(totalCtx, 2*time.Second)
	commitErr := commitSSHKeyRotation(commitCtx, h.db, binding, snapshot, candidate)
	cancelCommit()
	if commitErr != nil {
		switch {
		case errors.Is(commitErr, errSSHKeyRotationSessionInvalid):
			auditState.reason = "request_rejected"
			respondUnauthorized(c, "会话无效")
			return
		case errors.Is(commitErr, errSSHKeyRotationScopeBlocked):
			auditState.reason = sshKeyRotationReasonScopeBlocked
		case errors.Is(commitErr, errSSHKeyRotationConflict):
			auditState.reason = sshKeyRotationReasonConflict
		case errors.Is(commitErr, errSSHKeyRotationBusy):
			auditState.reason = sshKeyRotationReasonBusy
		default:
			respondInternalError(c, errSSHKeyRotationInternal)
			return
		}
		respondOK(c, sshKeyRotationResponse{
			Status:               sshKeyRotationStatusNotSaved,
			Reason:               auditState.reason,
			PublicKeyFingerprint: publicKeyFingerprint,
			Results:              results,
		})
		return
	}
	auditState.saved = true
	auditState.reason = ""
	respondOK(c, sshKeyRotationResponse{
		Status:               sshKeyRotationStatusSaved,
		Reason:               "",
		PublicKeyFingerprint: publicKeyFingerprint,
		Results:              results,
	})
}
