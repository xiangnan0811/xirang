package handlers

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	configImportPreviewMaxBodyBytes   = 10 << 20
	configImportPreviewTimeout        = 10 * time.Second
	configImportPreviewCleanupTimeout = 2 * time.Second
	configImportPreviewMaxItems       = 100
)

type configImportPreviewEntity struct {
	Entity            string `json:"entity"`
	Input             int    `json:"input"`
	Created           int    `json:"created"`
	Updated           int    `json:"updated"`
	Skipped           int    `json:"skipped"`
	Rejected          int    `json:"rejected"`
	PendingValidation int    `json:"pending_validation"`
	DisabledImported  int    `json:"disabled_imported"`
}

type configImportPreviewSystemSettings struct {
	WillWrite     bool `json:"will_write"`
	Created       int  `json:"created"`
	Updated       int  `json:"updated"`
	AppliesInSkip bool `json:"applies_in_skip"`
}

type configImportPreviewSecret struct {
	Entity  string `json:"entity"`
	Index   int    `json:"index"`
	Field   string `json:"field"`
	Present bool   `json:"present"`
	Action  string `json:"action"`
}

type configImportPreviewAsset struct {
	Entity string `json:"entity"`
	Input  int    `json:"input"`
	Status string `json:"status"`
}

// configImportPreviewResult is intentionally a projection of the shared import
// decision. It contains no provisional identities, settings values, or secret
// material. Its counts describe the current read snapshot only; apply repeats
// all validation under its existing locking and grant boundaries.
type configImportPreviewResult struct {
	Conflict                string                            `json:"conflict"`
	Basis                   string                            `json:"basis"`
	Entities                []configImportPreviewEntity       `json:"entities"`
	Warnings                []configImportWarning             `json:"warnings"`
	PendingChecks           []configImportPendingCheck        `json:"pending_checks"`
	PendingChecksTruncated  int                               `json:"pending_checks_truncated"`
	WarningsTruncated       int                               `json:"warnings_truncated"`
	SystemSettings          configImportPreviewSystemSettings `json:"system_settings"`
	Secrets                 []configImportPreviewSecret       `json:"secrets"`
	SecretsTruncated        int                               `json:"secrets_truncated"`
	AssetGraph              []configImportPreviewAsset        `json:"asset_graph"`
	RequiresApplyValidation bool                              `json:"requires_apply_validation"`
}

type configImportPreviewBodyReadResult struct {
	body []byte
	err  error
}

var errConfigImportPreviewUnsupportedDatabase = errors.New("config import preview database dialect is unsupported")

// PreviewImport godoc
// @Summary      预览配置导入
// @Description  在当前只读数据库快照上评估配置导入候选；不执行写入、秘密解密、step-up 或凭据授权。返回值仅为提交时可能变化的预估。
// @Tags         config
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        conflict  query     string  false  "冲突策略（skip 默认/overwrite）"
// @Param        body      body      object  true   "配置 JSON 数据"
// @Success      200  {object}  handlers.Response{data=configImportPreviewResult}
// @Failure      400  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Failure      403  {object}  handlers.Response
// @Failure      503  {object}  handlers.Response
// @Router       /config/import/preview [post]
func (h *ConfigHandler) PreviewImport(c *gin.Context) {
	if c == nil || c.Request == nil {
		return
	}
	if h == nil {
		respondServiceUnavailable(c, "导入预览暂不可用")
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), configImportPreviewTimeout)
	defer cancel()

	conflict := c.DefaultQuery("conflict", "skip")
	if conflict != "skip" && conflict != "overwrite" {
		respondBadRequest(c, "conflict 参数仅支持 skip 或 overwrite")
		return
	}

	body, err := readConfigImportPreviewBody(ctx, c)
	if err != nil {
		if isConfigImportPreviewBodyTooLarge(err) {
			respondBadRequest(c, "导入文件超过 10MB 限制")
			return
		}
		var networkErr net.Error
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil || (errors.As(err, &networkErr) && networkErr.Timeout()) {
			respondServiceUnavailable(c, "导入预览已取消或超时")
			return
		}
		respondBadRequest(c, "读取导入数据失败")
		return
	}
	if err := ctx.Err(); err != nil {
		respondServiceUnavailable(c, "导入预览已取消或超时")
		return
	}

	input, err := prepareConfigImportInput(ctx, h, body, conflict)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
			respondServiceUnavailable(c, "导入预览已取消或超时")
			return
		}
		respondConfigImportPreviewValidationError(c, err)
		return
	}
	if err := ctx.Err(); err != nil {
		respondServiceUnavailable(c, "导入预览已取消或超时")
		return
	}

	var decision configImportDecision
	err = withConfigImportPreviewReadonly(ctx, h.db, func(readDB *gorm.DB) error {
		snapshot, snapshotErr := loadConfigImportSnapshot(ctx, readDB, input)
		if snapshotErr != nil {
			return snapshotErr
		}
		var decisionErr error
		decision, decisionErr = evaluateConfigImport(ctx, input, snapshot)
		return decisionErr
	})
	if err != nil {
		if errors.Is(err, errConfigAssetGraphInvalid) || errors.Is(err, errConfigImportTargetConflict) {
			respondConfigImportPreviewValidationError(c, err)
			return
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			respondServiceUnavailable(c, "导入预览已取消或超时")
			return
		}
		respondServiceUnavailable(c, "导入预览暂不可用")
		return
	}
	if err := ctx.Err(); err != nil {
		respondServiceUnavailable(c, "导入预览已取消或超时")
		return
	}
	result, err := projectConfigImportPreviewContext(ctx, decision)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			respondServiceUnavailable(c, "导入预览已取消或超时")
			return
		}
		respondServiceUnavailable(c, "导入预览暂不可用")
		return
	}
	respondOK(c, result)
}

func readConfigImportPreviewBody(ctx context.Context, c *gin.Context) ([]byte, error) {
	if c == nil || c.Request == nil {
		return nil, errors.New("config import preview request is unavailable")
	}
	body := c.Request.Body
	if body == nil {
		body = http.NoBody
	}
	// A nil ResponseWriter keeps MaxBytesReader from retaining or touching the
	// Gin writer if a late body read crosses the limit. The request body itself
	// remains owned by this handler and is joined before returning.
	limited := http.MaxBytesReader(nil, body, configImportPreviewMaxBodyBytes)
	c.Request.Body = limited

	var readDeadlineController *http.ResponseController
	readDeadlineSet := false
	if deadline, ok := ctx.Deadline(); ok {
		readDeadlineController = http.NewResponseController(c.Writer)
		if err := readDeadlineController.SetReadDeadline(deadline); err == nil {
			readDeadlineSet = true
		}
	}
	if readDeadlineSet {
		defer func() {
			_ = readDeadlineController.SetReadDeadline(time.Time{})
		}()
	}

	resultCh := make(chan configImportPreviewBodyReadResult, 1)
	go func() {
		payload, err := io.ReadAll(limited)
		resultCh <- configImportPreviewBodyReadResult{body: payload, err: err}
	}()

	select {
	case result := <-resultCh:
		return result.body, result.err
	case <-ctx.Done():
		// Request-body Close is the standard net/http cancellation boundary.
		// Join the reader before returning so no goroutine can later touch the
		// request or its response writer after this handler has responded.
		if readDeadlineSet {
			_ = readDeadlineController.SetReadDeadline(time.Now())
		}
		_ = limited.Close()
		<-resultCh
		return nil, ctx.Err()
	}
}

func isConfigImportPreviewBodyTooLarge(err error) bool {
	var maxBytesErr *http.MaxBytesError
	return errors.As(err, &maxBytesErr)
}

func respondConfigImportPreviewValidationError(c *gin.Context, err error) {
	if errors.Is(err, errConfigAssetGraphInvalid) {
		respondBadRequest(c, "导入数据无效")
		return
	}
	if errors.Is(err, errConfigImportTargetConflict) {
		respondBadRequest(c, "导入任务的备份目标存在重叠或归属不明")
		return
	}
	message := err.Error()
	if strings.Contains(message, "system-setting") || strings.Contains(message, "系统设置") {
		respondBadRequest(c, "导入系统设置无效")
		return
	}
	respondBadRequest(c, "无效的导入数据")
}

// withConfigImportPreviewReadonly binds GORM to one dedicated connection and
// issues the transaction controls explicitly. This is important for the
// production SQLite DSN, whose default _txlock=immediate would otherwise turn
// a normal GORM Transaction into a writer reservation. PostgreSQL gets a
// repeatable-read, read-only snapshot. The callback is only given SELECT-safe
// shared decision functions; no import persistence path is reachable here.
func withConfigImportPreviewReadonly(ctx context.Context, db *gorm.DB, fn func(*gorm.DB) error) (retErr error) {
	if db == nil || fn == nil {
		return errors.New("config import preview database is unavailable")
	}
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("config import preview database is unavailable: %w", err)
	}
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		return fmt.Errorf("config import preview database connection is unavailable: %w", err)
	}
	defer func() { retErr = errors.Join(retErr, conn.Close()) }()

	dialect := strings.ToLower(strings.TrimSpace(db.Name()))
	beginSQL := "BEGIN"
	switch dialect {
	case "sqlite", "sqlite3":
		// Explicit BEGIN, rather than database/sql BeginTx, is deliberate:
		// go-sqlite3's _txlock=immediate driver setting must not promote this
		// read transaction to a writer reservation.
	case "postgres", "postgresql":
		beginSQL = "BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY"
	default:
		return errConfigImportPreviewUnsupportedDatabase
	}

	transactionOpen := false
	rollback := func() error {
		if !transactionOpen {
			return nil
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), configImportPreviewCleanupTimeout)
		_, rollbackErr := conn.ExecContext(cleanupCtx, "ROLLBACK")
		cancel()
		transactionOpen = false
		if rollbackErr == nil {
			return nil
		}

		// A failed rollback may leave the raw transaction open. Mark the
		// database/sql connection bad so it cannot return to the pool carrying
		// that transaction state.
		discardErr := conn.Raw(func(any) error {
			return driver.ErrBadConn
		})
		rollbackFailure := fmt.Errorf("config import preview rollback failed: %w", rollbackErr)
		if discardErr == nil {
			return errors.Join(rollbackFailure, errors.New("config import preview connection discard was not confirmed"))
		}
		if !errors.Is(discardErr, driver.ErrBadConn) {
			return errors.Join(rollbackFailure, fmt.Errorf("config import preview connection discard failed: %w", discardErr))
		}
		return rollbackFailure
	}
	defer func() {
		if cleanupErr := rollback(); cleanupErr != nil {
			retErr = errors.Join(retErr, cleanupErr)
		}
	}()

	// A canceled BEGIN may have reached the server even when Exec reports an
	// error. Always clean up that ambiguous outcome before returning the pool
	// connection.
	transactionOpen = true
	if _, err := conn.ExecContext(ctx, beginSQL); err != nil {
		return fmt.Errorf("config import preview transaction is unavailable: %w", err)
	}

	readDB := db.Session(&gorm.Session{NewDB: true, Context: ctx})
	readDB.Statement.ConnPool = conn
	if err := fn(readDB); err != nil {
		return errors.Join(err, rollback())
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(err, rollback())
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return errors.Join(fmt.Errorf("config import preview transaction commit failed: %w", err), rollback())
	}
	transactionOpen = false
	return nil
}

var configImportPreviewEntityOrder = [...]string{
	configImportEntityNodes,
	configImportEntitySSHKeys,
	configImportEntityPolicies,
	configImportEntityTasks,
	configImportEntitySystemSettings,
}

func projectConfigImportPreviewContext(ctx context.Context, decision configImportDecision) (configImportPreviewResult, error) {
	if err := configImportContextErr(ctx); err != nil {
		return configImportPreviewResult{}, err
	}
	result := configImportPreviewResult{
		Conflict:                decision.input.conflict,
		Basis:                   "current_snapshot",
		Entities:                make([]configImportPreviewEntity, 0, len(configImportPreviewEntityOrder)),
		Warnings:                make([]configImportWarning, 0, configImportPreviewMaxItems),
		PendingChecks:           make([]configImportPendingCheck, 0, configImportPreviewMaxItems),
		Secrets:                 make([]configImportPreviewSecret, 0, configImportPreviewMaxItems),
		AssetGraph:              projectConfigImportPreviewAssets(decision.input.envelope),
		RequiresApplyValidation: true,
	}

	for _, entity := range configImportPreviewEntityOrder {
		if err := configImportContextErr(ctx); err != nil {
			return configImportPreviewResult{}, err
		}
		counts := configImportDecisionCountsFor(&decision, entity)
		inputCount := counts.Input
		if entity == configImportEntitySystemSettings {
			// The public input count is tied to the raw envelope. The normalized
			// settings plan is only the evaluator's legal write plan.
			inputCount = len(decision.input.envelope.Classic.SystemSettings)
		}
		result.Entities = append(result.Entities, configImportPreviewEntity{
			Entity:            entity,
			Input:             nonNegativeConfigImportPreviewCount(inputCount),
			Created:           nonNegativeConfigImportPreviewCount(counts.Created),
			Updated:           nonNegativeConfigImportPreviewCount(counts.Updated),
			Skipped:           nonNegativeConfigImportPreviewCount(counts.Skipped),
			Rejected:          nonNegativeConfigImportPreviewCount(counts.Rejected),
			PendingValidation: nonNegativeConfigImportPreviewCount(counts.PendingValidation),
			DisabledImported:  nonNegativeConfigImportPreviewCount(counts.DisabledImported),
		})
	}
	settings := configImportDecisionCountsFor(&decision, configImportEntitySystemSettings)
	result.SystemSettings = configImportPreviewSystemSettings{
		WillWrite:     len(decision.input.envelope.Classic.SystemSettings) > 0,
		Created:       nonNegativeConfigImportPreviewCount(settings.Created),
		Updated:       nonNegativeConfigImportPreviewCount(settings.Updated),
		AppliesInSkip: true,
	}

	for _, warning := range decision.warnings {
		if err := configImportContextErr(ctx); err != nil {
			return configImportPreviewResult{}, err
		}
		if !validConfigImportPreviewWarning(warning) {
			continue
		}
		if len(result.Warnings) < configImportPreviewMaxItems {
			warning.Name = sanitizeConfigImportWarningName(warning.Name)
			result.Warnings = append(result.Warnings, warning)
			continue
		}
		result.WarningsTruncated++
	}
	result.WarningsTruncated += nonNegativeConfigImportPreviewCount(decision.warningsTruncated)

	seenPending := make(map[configImportPendingCheck]struct{}, len(decision.pendingChecks))
	for _, check := range decision.pendingChecks {
		if err := configImportContextErr(ctx); err != nil {
			return configImportPreviewResult{}, err
		}
		if !validConfigImportPreviewPendingCheck(check) {
			continue
		}
		items := decision.itemDecisions[check.Entity]
		if check.Index < 0 || check.Index >= len(items) {
			continue
		}
		item := items[check.Index]
		if item.Action != configImportActionPending || item.PendingCode != check.Code {
			continue
		}
		if _, exists := seenPending[check]; exists {
			continue
		}
		seenPending[check] = struct{}{}
		if len(result.PendingChecks) < configImportPreviewMaxItems {
			result.PendingChecks = append(result.PendingChecks, check)
			continue
		}
		result.PendingChecksTruncated++
	}

	if err := projectConfigImportPreviewSecretsContext(ctx, &result, decision); err != nil {
		return configImportPreviewResult{}, err
	}
	return result, nil
}

func nonNegativeConfigImportPreviewCount(value int) int {
	if value < 0 {
		return 0
	}
	return value
}

func validConfigImportPreviewWarning(warning configImportWarning) bool {
	if warning.Index < 0 || !validConfigImportPreviewEntity(warning.Entity) {
		return false
	}
	switch warning.Code {
	case configImportWarningInvalidInput,
		configImportWarningInvalidScope,
		configImportWarningUnresolvedNodeScope,
		configImportWarningInvalidPrivateKey,
		configImportWarningMissingPrivateKey,
		configImportWarningMissingPassword,
		configImportWarningMissingInlineKey,
		configImportWarningUnresolvedSSHKey,
		configImportWarningDuplicateName,
		configImportWarningInvalidReference,
		configImportWarningReferenceConflict:
		return true
	default:
		return false
	}
}

func validConfigImportPreviewPendingCheck(check configImportPendingCheck) bool {
	if check.Index < 0 || !validConfigImportPreviewEntity(check.Entity) {
		return false
	}
	switch check.Code {
	case configImportPendingRetainedSecret, configImportPendingAllocatedID, configImportPendingReference:
		return true
	default:
		return false
	}
}

func validConfigImportPreviewEntity(entity string) bool {
	switch entity {
	case configImportEntityNodes, configImportEntitySSHKeys, configImportEntityPolicies, configImportEntityTasks, configImportEntitySystemSettings:
		return true
	default:
		return false
	}
}

func projectConfigImportPreviewAssets(envelope configImportEnvelope) []configImportPreviewAsset {
	assets := []configImportPreviewAsset{
		{Entity: "backup_repositories", Input: len(envelope.Graph.BackupRepositories)},
		{Entity: "task_repository_links", Input: len(envelope.Graph.TaskRepositoryLinks)},
		{Entity: "backup_retention_policies", Input: len(envelope.Graph.BackupRetentionPolicies)},
		{Entity: "recovery_point_holds", Input: len(envelope.Graph.RecoveryPointHolds)},
	}
	for index := range assets {
		if assets[index].Input == 0 {
			assets[index].Status = "not_present"
			continue
		}
		if strings.TrimSpace(envelope.Version) != configExportVersion2 {
			assets[index].Status = "unsupported"
			continue
		}
		assets[index].Status = "unknown"
	}
	return assets
}

func projectConfigImportPreviewSecretsContext(ctx context.Context, result *configImportPreviewResult, decision configImportDecision) error {
	if result == nil {
		return nil
	}
	appendSecret := func(entity string, index int, field string, record map[string]interface{}) error {
		if err := configImportContextErr(ctx); err != nil {
			return err
		}
		if len(result.Secrets) >= configImportPreviewMaxItems {
			result.SecretsTruncated++
			return nil
		}
		present := importedConfigSecretPresent(record, field)
		action := configImportPreviewSecretAction(decision, entity, index, field, present)
		result.Secrets = append(result.Secrets, configImportPreviewSecret{
			Entity: entity, Index: index, Field: field, Present: present, Action: action,
		})
		return nil
	}
	for index, record := range decision.input.envelope.Classic.Nodes {
		if err := appendSecret(configImportEntityNodes, index, "password", record); err != nil {
			return err
		}
		if err := appendSecret(configImportEntityNodes, index, "private_key", record); err != nil {
			return err
		}
	}
	for index, record := range decision.input.envelope.Classic.SSHKeys {
		if err := appendSecret(configImportEntitySSHKeys, index, "private_key", record); err != nil {
			return err
		}
	}
	return nil
}

func configImportPreviewSecretAction(decision configImportDecision, entity string, index int, field string, inputPresent bool) string {
	item := configImportPreviewItemDecision(decision, entity, index)
	if item == nil {
		return "rejected"
	}
	switch item.Action {
	case configImportActionSkip:
		return "skipped"
	case configImportActionReject:
		return "rejected"
	}
	// Existing nodes deliberately retain persisted password/private-key
	// material on overwrite; those input fields are not an apply operation.
	// Presence still reports what the input contained, while action reports
	// the effective secret outcome.
	if entity == configImportEntityNodes && configImportPreviewExistingCandidate(decision, entity, index) {
		if configImportPreviewRetainedSecretPresent(decision, entity, index, field) {
			return "preserved"
		}
		return "missing"
	}
	if inputPresent {
		return "supplied"
	}
	if configImportPreviewRetainedSecretPresent(decision, entity, index, field) {
		return "preserved"
	}
	return "missing"
}

func configImportPreviewExistingCandidate(decision configImportDecision, entity string, index int) bool {
	switch entity {
	case configImportEntityNodes:
		for _, candidate := range decision.nodeCandidates {
			if candidate != nil && candidate.index == index {
				return candidate.existing
			}
		}
	case configImportEntitySSHKeys:
		for _, candidate := range decision.keyCandidates {
			if candidate != nil && candidate.index == index {
				return candidate.existing
			}
		}
	}
	return false
}

func configImportPreviewItemDecision(decision configImportDecision, entity string, index int) *configImportItemDecision {
	items := configImportItemDecisionsFor(&decision, entity)
	if index < 0 || index >= len(items) {
		return nil
	}
	return &items[index]
}

func configImportPreviewRetainedSecretPresent(decision configImportDecision, entity string, index int, field string) bool {
	if decision.snapshot.credentials == nil {
		return false
	}
	var id uint
	switch entity {
	case configImportEntityNodes:
		for _, candidate := range decision.nodeCandidates {
			if candidate != nil && candidate.index == index && candidate.existing {
				id = candidate.row.ID
				break
			}
		}
		switch field {
		case "password":
			return decision.snapshot.credentials.nodePasswordPresent[id]
		case "private_key":
			return decision.snapshot.credentials.nodePrivateKeyPresent[id]
		}
	case configImportEntitySSHKeys:
		for _, candidate := range decision.keyCandidates {
			if candidate != nil && candidate.index == index && candidate.existing {
				id = candidate.row.ID
				break
			}
		}
		if field == "private_key" {
			return decision.snapshot.credentials.keyPrivateKeyPresent[id]
		}
	}
	return false
}
