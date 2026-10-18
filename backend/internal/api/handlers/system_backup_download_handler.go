package handlers

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"xirang/backend/internal/auth"
	"xirang/backend/internal/credentialaudit"
	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	systemBackupAuthorizationTimeout  = 500 * time.Millisecond
	systemBackupAuthorizationInterval = 5 * time.Second
	systemBackupWriteIdleTimeout      = 30 * time.Second
)

var (
	errSystemBackupAuthorizationUnavailable = errors.New("backup authorization unavailable")
	errSystemBackupIntegrity                = errors.New("backup integrity failure")
)

// One handler is constructed per router; both sources share its two permits.
type SystemBackupDownloadHandler struct {
	db         *gorm.DB
	jwtManager *auth.JWTManager
	permits    chan struct{}
}

func NewSystemBackupDownloadHandler(db *gorm.DB, jwtManager *auth.JWTManager) *SystemBackupDownloadHandler {
	return &SystemBackupDownloadHandler{db: db, jwtManager: jwtManager, permits: make(chan struct{}, 2)}
}

func systemBackupDownloadGrantMatch() credentialGrantMatch {
	return credentialGrantMatch{Action: CredentialGrantActionSystemBackupDownload, Purpose: CredentialGrantPurposeSystemBackupDownload}
}

// validateSession deliberately rechecks the original grant, never a replacement.
// Session verification errors are opaque: we do not classify human error text.
func (h *SystemBackupDownloadHandler) validateSession(ctx context.Context, token string, grantID uint) error {
	ctx, cancel := context.WithTimeout(ctx, systemBackupAuthorizationTimeout)
	defer cancel()
	claims, err := authorizeRealtimeToken(ctx, token, h.jwtManager, h.db.WithContext(ctx), realtimeAuthRequirements{Role: "admin"})
	if err != nil {
		return errSystemBackupAuthorizationUnavailable
	}
	var grant model.CredentialAccessGrant
	err = applyCredentialGrantMatch(h.db.WithContext(ctx), systemBackupDownloadGrantMatch()).
		Where("id = ? AND requester_user_id = ? AND requester_role = ? AND action = ? AND purpose = ?", grantID, claims.UserID, claims.Role, CredentialGrantActionSystemBackupDownload, CredentialGrantPurposeSystemBackupDownload).
		Where("status IN ? AND expires_at > ?", []string{CredentialGrantStatusActive, CredentialGrantStatusApproved}, time.Now().UTC()).
		First(&grant).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrCredentialGrantRequired
	}
	if err != nil {
		return errSystemBackupAuthorizationUnavailable
	}
	return nil
}

type systemBackupDownloadResult struct {
	source   systemBackupSource
	engine   string
	filename string
	size     int64
	sent     int64
	grantID  uint
	outcome  string
	stage    string
	reason   string
}

func (h *SystemBackupDownloadHandler) auditResult(c *gin.Context, result *systemBackupDownloadResult) {
	// Cancellation must not suppress the result, nor may audit hold a permit
	// indefinitely. Deliberately do not include request headers or raw errors.
	ctx, cancel := context.WithTimeout(context.Background(), systemBackupAuthorizationTimeout)
	defer cancel()
	if result.outcome == credentialaudit.OutcomeBlocked && (c.Writer.Status() == http.StatusGatewayTimeout || c.Request.Context().Err() != nil) {
		result.outcome, result.reason = credentialaudit.OutcomeFailure, "budget_exhausted"
	}
	_ = credentialaudit.Write(h.db.WithContext(ctx), credentialaudit.Event{
		UserID: middleware.CurrentUserID(c), Username: c.GetString(middleware.CtxUsername), Role: middleware.CurrentRole(c),
		Action: CredentialGrantActionSystemBackupDownload, Purpose: CredentialGrantPurposeSystemBackupDownload,
		CredentialKind: credentialGrantKind, CredentialSource: credentialGrantSource, Outcome: result.outcome,
		Metadata: map[string]any{"source": string(result.source), "engine": result.engine, "filename": result.filename, "size": result.size, "sent_bytes": result.sent, "stage": result.stage, "reason_code": result.reason, "grant_id": result.grantID},
	})
}

func systemBackupDownloadError(c *gin.Context, status int, code string) {
	if middleware.HandleSystemBackupDownloadBudgetFailure(c) {
		return
	}
	c.AbortWithStatusJSON(status, Response{Code: status, Message: "备份下载未完成", Data: systemBackupErrorData{ErrorCode: code}})
}

func systemBackupStrongIfMatch(header http.Header) (string, int) {
	values := header.Values("If-Match")
	if len(values) == 0 {
		return "", http.StatusPreconditionRequired
	}
	if len(values) != 1 || len(values[0]) != 66 || values[0][0] != '"' || values[0][65] != '"' {
		return "", http.StatusBadRequest
	}
	digest := values[0][1:65]
	for _, char := range digest {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return "", http.StatusBadRequest
		}
	}
	return digest, 0
}

// Download godoc
// @Summary      下载已校验的系统数据库备份
// @Description  仅管理员；要求 system.backup_download proof 与 system_backup_download 授权。固定所选摘要，同一文件描述符双遍校验后输出数据库和 basename SHA256 文件；不含加密密钥。最大 4 GiB，准备 120 秒、总计 10 分钟且受原会话与授权有效期限制；传输中错误会中止，不补 JSON 或 tar 尾部。
// @Tags         system
// @Security     Bearer
// @Produce      application/x-tar
// @Param        source path string true "备份来源" Enums(web,cron)
// @Param        filename path string true "受管备份 basename"
// @Param        If-Match header string true "带双引号的 64 位小写 SHA256 强 ETag"
// @Param        X-Xirang-Step-Up header string true "system.backup_download proof"
// @Success      200 {file} binary "USTAR 数据库及 SHA256 文件"
// @Failure      400 {object} handlers.Response "非法请求或不支持 Range"
// @Failure      401 {object} handlers.Response
// @Failure      403 {object} handlers.Response
// @Failure      404 {object} handlers.Response
// @Failure      409 {object} handlers.Response "来源未配置、文件未就绪或完整性校验失败"
// @Failure      412 {object} handlers.Response "所选摘要已变化"
// @Failure      413 {object} handlers.Response
// @Failure      428 {object} handlers.Response "缺少 If-Match"
// @Failure      429 {object} handlers.Response "下载容量已满；Retry-After: 5"
// @Failure      501 {object} handlers.Response
// @Failure      503 {object} handlers.Response
// @Failure      504 {object} handlers.Response "准备超时且仍可写入错误"
// @Router       /system/backups/{source}/{filename}/download [get]
func (h *SystemBackupDownloadHandler) Download(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	result := systemBackupDownloadResult{outcome: credentialaudit.OutcomeBlocked, stage: "admission", reason: "authorization_required"}
	defer func() {
		if recovered := recover(); recovered != nil {
			result.outcome = credentialaudit.OutcomeFailure
			h.auditResult(c, &result)
			panic(recovered)
		}
		h.auditResult(c, &result)
	}()
	if middleware.HandleSystemBackupDownloadBudgetFailure(c) {
		return
	}
	budget, ok := middleware.SystemBackupDownloadBudgetFromContext(c)
	if !ok {
		result.outcome, result.reason = credentialaudit.OutcomeFailure, "budget_unavailable"
		systemBackupDownloadError(c, http.StatusServiceUnavailable, "BACKUP_AUTHORIZATION_UNAVAILABLE")
		return
	}
	if !EnforceStepUp(c, h.db.WithContext(c.Request.Context()), h.jwtManager, auth.StepUpActionSystemBackupDownload, CredentialGrantPurposeSystemBackupDownload, "system_backup_download") {
		if c.Writer.Status() == http.StatusServiceUnavailable {
			result.outcome, result.reason = credentialaudit.OutcomeFailure, "authorization_unavailable"
		}
		return
	}
	claims := claimsFromGinContext(c)
	grant, err := findActiveCredentialGrant(c.Request.Context(), h.db.WithContext(c.Request.Context()), claims, systemBackupDownloadGrantMatch())
	if middleware.HandleSystemBackupDownloadBudgetFailure(c) {
		result.outcome, result.reason = credentialaudit.OutcomeFailure, "budget_exhausted"
		return
	}
	if err != nil {
		if isCredentialGrantDenialError(err) {
			auditCtx, cancel := context.WithTimeout(c.Request.Context(), systemBackupAuthorizationTimeout)
			writeCredentialGrantBlockedAudit(c, h.db.WithContext(auditCtx), claims, systemBackupDownloadGrantMatch(), credentialGrantStatusForError(err))
			cancel()
			if middleware.HandleSystemBackupDownloadBudgetFailure(c) {
				return
			}
			respondCredentialGrantRequired(c, credentialGrantStatusForError(err))
		} else {
			result.outcome, result.reason = credentialaudit.OutcomeFailure, "authorization_unavailable"
			systemBackupDownloadError(c, http.StatusServiceUnavailable, "BACKUP_AUTHORIZATION_UNAVAILABLE")
		}
		return
	}
	result.grantID = grant.ID
	auditCtx, cancelAudit := context.WithTimeout(c.Request.Context(), systemBackupAuthorizationTimeout)
	writeCredentialGrantAuditWithClaims(c, h.db.WithContext(auditCtx), claims, *grant, credentialaudit.OutcomeSuccess, "use", "active")
	cancelAudit()
	if middleware.HandleSystemBackupDownloadBudgetFailure(c) {
		return
	}
	session, validSession := middleware.CurrentSessionBinding(c)
	if !validSession {
		result.outcome, result.reason = credentialaudit.OutcomeFailure, "authorization_unavailable"
		systemBackupDownloadError(c, http.StatusServiceUnavailable, "BACKUP_AUTHORIZATION_UNAVAILABLE")
		return
	}
	expiresAt := session.ExpiresAt
	if grant.ExpiresAt.Before(expiresAt) {
		expiresAt = grant.ExpiresAt
	}
	if err := budget.LimitDeadline(c, expiresAt); err != nil {
		result.outcome, result.reason = credentialaudit.OutcomeFailure, "deadline_unavailable"
		systemBackupDownloadError(c, http.StatusServiceUnavailable, "BACKUP_AUTHORIZATION_UNAVAILABLE")
		return
	}
	source, name := systemBackupSource(c.Param("source")), c.Param("filename")
	if (source != systemBackupSourceWeb && source != systemBackupSourceCron) || name == "" || strings.ContainsAny(name, "/\\\x00%") || name == "." || name == ".." {
		result.reason = "request_invalid"
		systemBackupDownloadError(c, http.StatusBadRequest, "BACKUP_REQUEST_INVALID")
		return
	}
	digest, status := systemBackupStrongIfMatch(c.Request.Header)
	if status != 0 {
		code := "BACKUP_REQUEST_INVALID"
		if status == http.StatusPreconditionRequired {
			code = "BACKUP_PRECONDITION_REQUIRED"
		}
		result.reason = "precondition_invalid"
		systemBackupDownloadError(c, status, code)
		return
	}
	if _, present := c.Request.Header["Range"]; present {
		result.reason = "range_unsupported"
		systemBackupDownloadError(c, http.StatusBadRequest, "BACKUP_RANGE_UNSUPPORTED")
		return
	}
	directory, engine, err := resolveSystemBackupSource(h.db, source)
	if middleware.HandleSystemBackupDownloadBudgetFailure(c) {
		return
	}
	if err != nil {
		result.reason = "source_unavailable"
		respondSystemBackupError(c, err)
		return
	}
	if !isManagedSystemBackupFilename(source, engine, name) {
		result.reason = "request_invalid"
		systemBackupDownloadError(c, http.StatusBadRequest, "BACKUP_REQUEST_INVALID")
		return
	}
	result.source, result.engine, result.filename = source, engine, name
	select {
	case h.permits <- struct{}{}:
		defer func() { <-h.permits }()
	default:
		result.reason = "busy"
		c.Header("Retry-After", "5")
		systemBackupDownloadError(c, http.StatusTooManyRequests, "BACKUP_DOWNLOAD_BUSY")
		return
	}
	result.outcome, result.stage, result.reason = credentialaudit.OutcomeFailure, "prepare", "not_ready"
	pair, err := pinSystemBackupFilePair(directory, source, engine, name)
	if pair != nil {
		defer func() { _ = pair.Close() }()
	}
	if middleware.HandleSystemBackupDownloadBudgetFailure(c) {
		return
	}
	if err != nil {
		switch {
		case errors.Is(err, errSystemBackupNotFound):
			systemBackupDownloadError(c, http.StatusNotFound, "BACKUP_NOT_FOUND")
		case errors.Is(err, errSystemBackupNotReady):
			systemBackupDownloadError(c, http.StatusConflict, "BACKUP_NOT_READY")
		default:
			systemBackupDownloadError(c, http.StatusServiceUnavailable, "BACKUP_DIRECTORY_UNAVAILABLE")
		}
		return
	}
	result.size = pair.Info.Size()
	if result.size > systemBackupMaxDataBytes {
		result.reason = "too_large"
		systemBackupDownloadError(c, http.StatusRequestEntityTooLarge, "BACKUP_TOO_LARGE")
		return
	}
	if pair.SHA256 != digest {
		result.reason = "changed"
		systemBackupDownloadError(c, http.StatusPreconditionFailed, "BACKUP_CHANGED")
		return
	}
	var buffer [32 << 10]byte
	if err := systemBackupReadPass(c.Request.Context(), pair, nil, buffer[:]); err != nil {
		result.reason = "integrity_failed"
		systemBackupDownloadError(c, http.StatusConflict, "BACKUP_INTEGRITY_FAILED")
		return
	}
	if middleware.HandleSystemBackupDownloadBudgetFailure(c) {
		return
	}
	if _, err := pair.File.Seek(0, io.SeekStart); err != nil {
		result.reason = "read_failed"
		systemBackupDownloadError(c, http.StatusConflict, "BACKUP_NOT_READY")
		return
	}
	// Validate USTAR encoding before touching the response body.
	checksum := pair.SHA256 + "  " + name + "\n"
	dataHeader := systemBackupTarHeader(name, result.size)
	checksumHeader := systemBackupTarHeader(name+".sha256", int64(len(checksum)))
	probe := tar.NewWriter(io.Discard)
	if err := probe.WriteHeader(dataHeader); err != nil {
		result.reason = "archive_invalid"
		systemBackupDownloadError(c, http.StatusConflict, "BACKUP_NOT_READY")
		return
	}
	probe = tar.NewWriter(io.Discard)
	if err := probe.WriteHeader(checksumHeader); err != nil {
		result.reason = "archive_invalid"
		systemBackupDownloadError(c, http.StatusConflict, "BACKUP_NOT_READY")
		return
	}
	token := ""
	if parts := strings.SplitN(c.GetHeader("Authorization"), " ", 2); len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
		token = strings.TrimSpace(parts[1])
	}
	if err := h.validateSession(c.Request.Context(), token, grant.ID); err != nil {
		result.reason = "authorization_lost"
		if middleware.HandleSystemBackupDownloadBudgetFailure(c) {
			return
		}
		if isCredentialGrantDenialError(err) {
			respondCredentialGrantRequired(c, "required")
		} else {
			systemBackupDownloadError(c, http.StatusServiceUnavailable, "BACKUP_AUTHORIZATION_UNAVAILABLE")
		}
		return
	}
	if !budget.BeginTransfer(c) {
		return
	}
	result.stage, result.reason = "transfer", "transfer_failed"
	writer := &systemBackupTransferWriter{c: c, absolute: budget.AbsoluteDeadline(), handler: h, token: token, grantID: grant.ID, lastCheck: time.Now(), sent: &result.sent}
	c.Header("Content-Type", "application/x-tar")
	c.Header("Content-Disposition", `attachment; filename="`+string(source)+"-"+name+`.tar"`)
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("X-Accel-Buffering", "no")
	c.Header("ETag", `"`+digest+`"`)
	c.Header("Content-Length", strconv.FormatInt(systemBackupPackagedSize(result.size), 10))
	archive := tar.NewWriter(writer)
	// Never close the archive on a failure: checksum and footer certify only
	// the bytes from the second pass, not the earlier preparation pass.
	if err := archive.WriteHeader(dataHeader); err != nil {
		panic(http.ErrAbortHandler)
	}
	if err := systemBackupReadPass(c.Request.Context(), pair, archive, buffer[:]); err != nil {
		panic(http.ErrAbortHandler)
	}
	if err := writer.checkAuthorization(true); err != nil {
		result.reason = "authorization_lost"
		panic(http.ErrAbortHandler)
	}
	if err := archive.WriteHeader(checksumHeader); err != nil {
		panic(http.ErrAbortHandler)
	}
	if _, err := archive.Write([]byte(checksum)); err != nil {
		panic(http.ErrAbortHandler)
	}
	if err := writer.checkAuthorization(true); err != nil {
		result.reason = "authorization_lost"
		panic(http.ErrAbortHandler)
	}
	if err := archive.Close(); err != nil {
		panic(http.ErrAbortHandler)
	}
	underlying := systemBackupUnderlyingWriter(c.Writer)
	controller := http.NewResponseController(underlying)
	if err := writer.refreshDeadline(controller); err != nil {
		result.reason = "deadline_unavailable"
		panic(http.ErrAbortHandler)
	}
	if err := controller.Flush(); err != nil {
		result.reason = "flush_failed"
		panic(http.ErrAbortHandler)
	}
	result.outcome, result.stage, result.reason = credentialaudit.OutcomeSuccess, "complete", "written"
}

func systemBackupTarHeader(name string, size int64) *tar.Header {
	return &tar.Header{Name: name, Size: size, Mode: 0600, Typeflag: tar.TypeReg, ModTime: time.Unix(0, 0).UTC(), Format: tar.FormatUSTAR}
}

// Both passes read exactly the pinned initial size; an append cannot turn this
// into an unbounded read. Stat and one extra byte detect changes and growth.
func systemBackupReadPass(ctx context.Context, pair *systemBackupFilePair, destination io.Writer, buffer []byte) error {
	hash := sha256.New()
	remaining := pair.Info.Size()
	for remaining > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		count := int64(len(buffer))
		if remaining < count {
			count = remaining
		}
		n, err := io.ReadFull(pair.File, buffer[:int(count)])
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		_, _ = hash.Write(buffer[:n])
		if destination != nil {
			written, err := destination.Write(buffer[:n])
			if err != nil {
				return err
			}
			if written != n {
				return io.ErrShortWrite
			}
		}
		remaining -= int64(n)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var extra [1]byte
	if n, err := pair.File.Read(extra[:]); n != 0 || err != io.EOF {
		return errSystemBackupIntegrity
	}
	info, err := pair.File.Stat()
	if err != nil || !cronBackupFileInfoMatches(pair.Info, info) || hex.EncodeToString(hash.Sum(nil)) != pair.SHA256 {
		return errSystemBackupIntegrity
	}
	return nil
}

func systemBackupUnderlyingWriter(writer http.ResponseWriter) http.ResponseWriter {
	for {
		unwrapper, ok := writer.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return writer
		}
		writer = unwrapper.Unwrap()
	}
}

type systemBackupTransferWriter struct {
	c         *gin.Context
	absolute  time.Time
	handler   *SystemBackupDownloadHandler
	token     string
	grantID   uint
	lastCheck time.Time
	sent      *int64
}

func (w *systemBackupTransferWriter) checkAuthorization(force bool) error {
	if err := w.c.Request.Context().Err(); err != nil {
		return err
	}
	if !time.Now().Before(w.absolute) {
		return context.DeadlineExceeded
	}
	if force || time.Since(w.lastCheck) >= systemBackupAuthorizationInterval {
		if err := w.handler.validateSession(w.c.Request.Context(), w.token, w.grantID); err != nil {
			return err
		}
		w.lastCheck = time.Now()
	}
	return nil
}

func (w *systemBackupTransferWriter) refreshDeadline(controller *http.ResponseController) error {
	if err := w.checkAuthorization(false); err != nil {
		return err
	}
	deadline := time.Now().Add(systemBackupWriteIdleTimeout)
	if w.absolute.Before(deadline) {
		deadline = w.absolute
	}
	return controller.SetWriteDeadline(deadline)
}

func (w *systemBackupTransferWriter) Write(data []byte) (int, error) {
	controller := http.NewResponseController(systemBackupUnderlyingWriter(w.c.Writer))
	if err := w.refreshDeadline(controller); err != nil {
		return 0, err
	}
	n, err := w.c.Writer.Write(data)
	*w.sent += int64(n)
	return n, err
}
