package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"
	"xirang/backend/internal/settings"

	"gorm.io/gorm"
)

const (
	recoveryDowngradeEndpoint             = "/api/v1/settings/backup-assets/recovery/downgrade-readiness"
	recoveryDowngradeReceiptSchemaVersion = 1
	recoveryDowngradeKeyDomain            = "xirang/recovery/downgrade/idempotency-key/v1"
	recoveryDowngradeIntentDomain         = "xirang/recovery/downgrade/intent/v1"
	recoveryDowngradeSessionDomain        = "xirang/recovery/downgrade/session/v1"
	recoveryDowngradeReasonMaxBytes       = 2048
	recoveryAdministrationAuditTimeout    = 5 * time.Second
)

var ErrRecoveryDowngradeIdempotencyConflict = errors.New("recovery downgrade idempotency conflict")

type recoveryAdministrationAuditWriter interface {
	Write(context.Context, backupasset.AuditEventInput) (model.BackupAssetAuditEvent, error)
}

func writeRecoveryAdministrationAudit(
	ctx context.Context,
	writer recoveryAdministrationAuditWriter,
	requesterID uint,
	operation string,
	status string,
	count int64,
) {
	if writer == nil || requesterID == 0 || count < 0 {
		return
	}
	auditCtx, cancel := context.WithTimeout(
		context.WithoutCancel(nonNilRecoveryRuntimeContext(ctx)), recoveryAdministrationAuditTimeout,
	)
	defer cancel()
	_, _ = writer.Write(auditCtx, backupasset.AuditEventInput{
		Actor:  backupasset.AuditActor{UserID: requesterID},
		Action: backupasset.AuditActionRecoveryAdministration, Outcome: backupasset.AuditOutcomeSuccess,
		ItemCount: count,
		Fields: map[backupasset.AuditField]any{
			backupasset.AuditFieldOperation: operation,
			backupasset.AuditFieldStatus:    status,
		},
	})
}

// RecoveryDowngradeReadinessRequest is a closed administrative command. Raw
// reason, key, and login-session authority must never be serialized.
type RecoveryDowngradeReadinessRequest struct {
	RequesterID         uint      `json:"-"`
	Endpoint            string    `json:"-"`
	IdempotencyKey      string    `json:"-"`
	SessionJTI          string    `json:"-"`
	SessionRole         string    `json:"-"`
	SessionTokenVersion uint      `json:"-"`
	SessionExpiresAt    time.Time `json:"-"`
	Reason              string    `json:"-"`
}

func (RecoveryDowngradeReadinessRequest) String() string {
	return "RecoveryDowngradeReadinessRequest{}"
}
func (request RecoveryDowngradeReadinessRequest) GoString() string {
	return request.String()
}

type recoveryDowngradeTransitionOwner interface {
	DowngradeReadiness(context.Context) (RecoveryDowngradeReadiness, error)
	InspectDowngradeReadiness(context.Context) (RecoveryDowngradeReadiness, bool, error)
}

type managedRecoveryDowngradeFacade struct {
	db    *gorm.DB
	owner recoveryDowngradeTransitionOwner
	now   func() time.Time
	audit recoveryAdministrationAuditWriter
	mu    sync.Mutex
}

type recoveryDowngradeReceipt struct {
	SchemaVersion    int                         `json:"schema_version"`
	IntentDigest     string                      `json:"intent_digest"`
	SessionDigest    string                      `json:"session_digest"`
	SessionExpiresAt time.Time                   `json:"session_expires_at"`
	Complete         bool                        `json:"complete"`
	Result           *RecoveryDowngradeReadiness `json:"result,omitempty"`
}

type recoveryDowngradeAuthority struct {
	key           string
	intentDigest  string
	sessionDigest string
	expiresAt     time.Time
}

func newManagedRecoveryDowngradeFacade(
	db *gorm.DB,
	owner recoveryDowngradeTransitionOwner,
	now func() time.Time,
) (*managedRecoveryDowngradeFacade, error) {
	if db == nil || owner == nil || now == nil {
		return nil, backupasset.ErrInvalidState
	}
	return &managedRecoveryDowngradeFacade{db: db, owner: owner, now: now}, nil
}

func (facade *managedRecoveryDowngradeFacade) ReplayRecoveryDowngradeReadiness(
	ctx context.Context,
	request RecoveryDowngradeReadinessRequest,
) (RecoveryDowngradeReadiness, bool, error) {
	if facade == nil || facade.db == nil || facade.owner == nil || facade.now == nil || ctx == nil {
		return RecoveryDowngradeReadiness{}, false, backupasset.ErrInvalidState
	}
	authority, err := recoveryDowngradeRequestAuthority(request, facade.now().UTC())
	if err != nil {
		return RecoveryDowngradeReadiness{}, false, err
	}
	facade.mu.Lock()
	defer facade.mu.Unlock()
	receipt, encoded, found, err := facade.loadReceipt(ctx, authority)
	if err != nil || !found {
		return RecoveryDowngradeReadiness{}, false, err
	}
	if receipt.Complete {
		result := *receipt.Result
		result.Replay = true
		return result, true, nil
	}
	result, inspected, inspectErr := facade.owner.InspectDowngradeReadiness(ctx)
	if inspectErr != nil || !inspected {
		return RecoveryDowngradeReadiness{}, false, inspectErr
	}
	if err := facade.completeReceipt(ctx, authority.key, encoded, receipt, result); err != nil {
		return RecoveryDowngradeReadiness{}, false, err
	}
	facade.writeAudit(ctx, request.RequesterID, result)
	result.Replay = true
	return result, true, nil
}

func (facade *managedRecoveryDowngradeFacade) RequestRecoveryDowngradeReadiness(
	ctx context.Context,
	request RecoveryDowngradeReadinessRequest,
) (RecoveryDowngradeReadiness, error) {
	if facade == nil || facade.db == nil || facade.owner == nil || facade.now == nil || ctx == nil {
		return RecoveryDowngradeReadiness{}, backupasset.ErrInvalidState
	}
	authority, err := recoveryDowngradeRequestAuthority(request, facade.now().UTC())
	if err != nil {
		return RecoveryDowngradeReadiness{}, err
	}
	facade.mu.Lock()
	defer facade.mu.Unlock()
	receipt, encoded, found, err := facade.loadReceipt(ctx, authority)
	if err != nil {
		return RecoveryDowngradeReadiness{}, err
	}
	if found && receipt.Complete {
		result := *receipt.Result
		result.Replay = true
		return result, nil
	}
	if !facade.now().UTC().Before(authority.expiresAt) {
		if found {
			return RecoveryDowngradeReadiness{}, ErrRecoveryDowngradeIdempotencyConflict
		}
		return RecoveryDowngradeReadiness{}, backupasset.ErrForbidden
	}
	if found {
		if inspected, ok, inspectErr := facade.owner.InspectDowngradeReadiness(ctx); inspectErr != nil {
			return RecoveryDowngradeReadiness{}, inspectErr
		} else if ok {
			if err := facade.completeReceipt(ctx, authority.key, encoded, receipt, inspected); err != nil {
				return RecoveryDowngradeReadiness{}, err
			}
			facade.writeAudit(ctx, request.RequesterID, inspected)
			inspected.Replay = true
			return inspected, nil
		}
	} else {
		receipt = recoveryDowngradeReceipt{
			SchemaVersion: recoveryDowngradeReceiptSchemaVersion,
			IntentDigest:  authority.intentDigest, SessionDigest: authority.sessionDigest,
			SessionExpiresAt: authority.expiresAt,
		}
		encoded, err = facade.createReceipt(ctx, authority.key, receipt)
		if err != nil {
			return RecoveryDowngradeReadiness{}, err
		}
	}
	result, transitionErr := facade.owner.DowngradeReadiness(ctx)
	if transitionErr != nil {
		inspected, ok, inspectErr := facade.owner.InspectDowngradeReadiness(ctx)
		if inspectErr != nil || !ok {
			return RecoveryDowngradeReadiness{}, transitionErr
		}
		result = inspected
	}
	if err := facade.completeReceipt(ctx, authority.key, encoded, receipt, result); err != nil {
		return RecoveryDowngradeReadiness{}, err
	}
	facade.writeAudit(ctx, request.RequesterID, result)
	result.Replay = false
	return result, nil
}

func (facade *managedRecoveryDowngradeFacade) writeAudit(
	ctx context.Context,
	requesterID uint,
	result RecoveryDowngradeReadiness,
) {
	writeRecoveryAdministrationAudit(
		ctx, facade.audit, requesterID, "downgrade_readiness", string(result.State),
		recoveryDowngradeBlockerCount(result.Blockers),
	)
}

func recoveryDowngradeBlockerCount(blockers RecoveryDowngradeBlockers) int64 {
	values := []int64{
		blockers.Jobs, blockers.Authorities, blockers.SourceLeases, blockers.NodeLeases, blockers.Attempts,
		blockers.ResultSets, blockers.Results, blockers.ContentGrants, blockers.ContentRequests,
		blockers.ContentStreams, blockers.ContentLeases, blockers.OtherRecoveryRows, blockers.ReconciliationBacklog,
	}
	var total int64
	for _, value := range values {
		if value < 0 || value > int64(^uint64(0)>>1)-total {
			return 0
		}
		total += value
	}
	return total
}

func recoveryDowngradeRequestAuthority(
	request RecoveryDowngradeReadinessRequest,
	now time.Time,
) (recoveryDowngradeAuthority, error) {
	if request.RequesterID == 0 || request.Endpoint != recoveryDowngradeEndpoint ||
		!validRecoveryDowngradeOpaque(request.IdempotencyKey, 16, 256) ||
		!validRecoveryDowngradeOpaque(request.SessionJTI, 1, 256) || request.SessionRole != "admin" ||
		request.SessionTokenVersion == 0 || request.SessionExpiresAt.IsZero() || now.IsZero() ||
		!validRecoveryDowngradeReason(request.Reason) {
		return recoveryDowngradeAuthority{}, backupasset.ErrForbidden
	}
	expiresAt := request.SessionExpiresAt.UTC()
	keyDigest := recoveryDowngradeDigest(
		recoveryDowngradeKeyDomain, strconv.FormatUint(uint64(request.RequesterID), 10),
		request.Endpoint, request.IdempotencyKey,
	)
	return recoveryDowngradeAuthority{
		key: settings.RecoveryDowngradeReceiptKeyPrefix + keyDigest,
		intentDigest: recoveryDowngradeDigest(
			recoveryDowngradeIntentDomain, request.Endpoint, request.Reason,
		),
		sessionDigest: recoveryDowngradeDigest(
			recoveryDowngradeSessionDomain, strconv.FormatUint(uint64(request.RequesterID), 10),
			request.SessionJTI, request.SessionRole, strconv.FormatUint(uint64(request.SessionTokenVersion), 10),
			expiresAt.Format(time.RFC3339Nano),
		),
		expiresAt: expiresAt,
	}, nil
}

func validRecoveryDowngradeOpaque(value string, minimum, maximum int) bool {
	if len(value) < minimum || len(value) > maximum || strings.TrimSpace(value) != value || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func validRecoveryDowngradeReason(value string) bool {
	return validRecoveryDowngradeOpaque(value, 1, recoveryDowngradeReasonMaxBytes)
}

func recoveryDowngradeDigest(domain string, values ...string) string {
	buffer := bytes.NewBuffer(nil)
	recoveryDowngradeWriteDigestString(buffer, domain)
	var count [8]byte
	binary.BigEndian.PutUint64(count[:], uint64(len(values)))
	buffer.Write(count[:])
	for _, value := range values {
		recoveryDowngradeWriteDigestString(buffer, value)
	}
	digest := sha256.Sum256(buffer.Bytes())
	return hex.EncodeToString(digest[:])
}

func recoveryDowngradeWriteDigestString(buffer *bytes.Buffer, value string) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	buffer.Write(length[:])
	buffer.WriteString(value)
}

func (facade *managedRecoveryDowngradeFacade) loadReceipt(
	ctx context.Context,
	authority recoveryDowngradeAuthority,
) (recoveryDowngradeReceipt, string, bool, error) {
	var rows []model.SystemSetting
	if err := facade.db.WithContext(ctx).Where("key = ?", authority.key).Limit(2).Find(&rows).Error; err != nil || len(rows) > 1 {
		return recoveryDowngradeReceipt{}, "", false, backupasset.ErrInvalidState
	}
	if len(rows) == 0 {
		return recoveryDowngradeReceipt{}, "", false, nil
	}
	encoded := rows[0].Value
	var receipt recoveryDowngradeReceipt
	if err := json.Unmarshal([]byte(encoded), &receipt); err != nil || !validRecoveryDowngradeReceipt(receipt) {
		return recoveryDowngradeReceipt{}, "", true, backupasset.ErrInvalidState
	}
	canonical, err := json.Marshal(receipt)
	if err != nil || !bytes.Equal(canonical, []byte(encoded)) {
		return recoveryDowngradeReceipt{}, "", true, backupasset.ErrInvalidState
	}
	if subtle.ConstantTimeCompare([]byte(receipt.IntentDigest), []byte(authority.intentDigest)) != 1 {
		return recoveryDowngradeReceipt{}, "", true, ErrRecoveryDowngradeIdempotencyConflict
	}
	if subtle.ConstantTimeCompare([]byte(receipt.SessionDigest), []byte(authority.sessionDigest)) != 1 {
		return recoveryDowngradeReceipt{}, "", true, backupasset.ErrForbidden
	}
	if !facade.now().UTC().Before(receipt.SessionExpiresAt.UTC()) {
		return recoveryDowngradeReceipt{}, "", true, ErrRecoveryDowngradeIdempotencyConflict
	}
	return receipt, encoded, true, nil
}

func validRecoveryDowngradeReceipt(receipt recoveryDowngradeReceipt) bool {
	if receipt.SchemaVersion != recoveryDowngradeReceiptSchemaVersion ||
		!validRecoveryDowngradeDigest(receipt.IntentDigest) || !validRecoveryDowngradeDigest(receipt.SessionDigest) ||
		receipt.SessionExpiresAt.IsZero() || receipt.Complete != (receipt.Result != nil) {
		return false
	}
	return receipt.Result == nil || validRecoveryDowngradeReadiness(*receipt.Result)
}

func validRecoveryDowngradeDigest(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && hex.EncodeToString(decoded) == value
}

func validRecoveryDowngradeReadiness(result RecoveryDowngradeReadiness) bool {
	if result.Replay || !strings.HasPrefix(result.AdmissionGeneration, "recovery-downgrade-") ||
		backupasset.ValidateOpaqueID(strings.TrimPrefix(result.AdmissionGeneration, "recovery-downgrade-")) != nil {
		return false
	}
	switch result.State {
	case RecoveryDowngradePristineAllowed, RecoveryDowngradeBlocked, RecoveryDowngradeForwardFixOnly:
	default:
		return false
	}
	blockers := result.Blockers
	return blockers.Jobs >= 0 && blockers.Authorities >= 0 && blockers.SourceLeases >= 0 &&
		blockers.NodeLeases >= 0 && blockers.Attempts >= 0 && blockers.ResultSets >= 0 && blockers.Results >= 0 &&
		blockers.ContentGrants >= 0 && blockers.ContentRequests >= 0 && blockers.ContentStreams >= 0 &&
		blockers.ContentLeases >= 0 && blockers.OtherRecoveryRows >= 0 && blockers.ReconciliationBacklog >= 0
}

func (facade *managedRecoveryDowngradeFacade) createReceipt(
	ctx context.Context,
	key string,
	receipt recoveryDowngradeReceipt,
) (string, error) {
	encoded, err := json.Marshal(receipt)
	if err != nil {
		return "", backupasset.ErrInvalidState
	}
	if err := facade.db.WithContext(ctx).Create(&model.SystemSetting{Key: key, Value: string(encoded)}).Error; err != nil {
		return "", backupasset.ErrInvalidState
	}
	return string(encoded), nil
}

func (facade *managedRecoveryDowngradeFacade) completeReceipt(
	ctx context.Context,
	key string,
	priorEncoded string,
	receipt recoveryDowngradeReceipt,
	result RecoveryDowngradeReadiness,
) error {
	result.Replay = false
	if !validRecoveryDowngradeReadiness(result) {
		return backupasset.ErrInvalidState
	}
	receipt.Complete = true
	receipt.Result = &result
	encoded, err := json.Marshal(receipt)
	if err != nil {
		return backupasset.ErrInvalidState
	}
	updated := facade.db.WithContext(ctx).Model(&model.SystemSetting{}).
		Where("key = ? AND value = ?", key, priorEncoded).Update("value", string(encoded))
	if updated.Error != nil || updated.RowsAffected != 1 {
		return backupasset.ErrInvalidState
	}
	return nil
}

var _ interface {
	ReplayRecoveryDowngradeReadiness(
		context.Context,
		RecoveryDowngradeReadinessRequest,
	) (RecoveryDowngradeReadiness, bool, error)
	RequestRecoveryDowngradeReadiness(
		context.Context,
		RecoveryDowngradeReadinessRequest,
	) (RecoveryDowngradeReadiness, error)
} = (*managedRecoveryDowngradeFacade)(nil)
