package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"xirang/backend/internal/auth"
	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/content"
	"xirang/backend/internal/middleware"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const maxBackupContentTicketBytes = 4 << 10

type BackupContentService interface {
	Issue(context.Context, content.IssueRequest) (content.IssuedTicket, error)
	Serve(context.Context, content.GatewayRequest, http.ResponseWriter) error
	RevokeSession(context.Context, string, string) error
}

type BackupContentHandlerConfig struct {
	TicketTimeout         time.Duration
	AllowInsecureLoopback bool
}

type BackupContentHandlerConfigSource func(context.Context) (BackupContentHandlerConfig, error)

type BackupContentHandler struct {
	service      BackupContentService
	db           *gorm.DB
	jwtManager   *auth.JWTManager
	configSource BackupContentHandlerConfigSource
}

type backupContentTicketPayload struct {
	SchemaVersion int                     `json:"schema_version" minimum:"1" maximum:"1" example:"1"`
	Action        content.DeliveryAction  `json:"action" enums:"preview,download"`
	Renderer      content.Renderer        `json:"renderer" enums:"escaped_text,safe_raster,same_origin_pdf,native_audio,native_video,metadata_hex,attachment"`
	Profile       content.RendererProfile `json:"profile" enums:"text_v1,raster_v1,pdf_v1,audio_v1,video_v1,hex_v1,original_v1"`
}

func NewBackupContentHandler(
	service BackupContentService,
	db *gorm.DB,
	jwtManager *auth.JWTManager,
	configSource BackupContentHandlerConfigSource,
) *BackupContentHandler {
	return &BackupContentHandler{service: service, db: db, jwtManager: jwtManager, configSource: configSource}
}

type featureDisabledBackupContentService struct{}

func NewFeatureDisabledBackupContentService() BackupContentService {
	return featureDisabledBackupContentService{}
}

func (featureDisabledBackupContentService) Issue(context.Context, content.IssueRequest) (content.IssuedTicket, error) {
	return content.IssuedTicket{}, content.ErrContentFeatureDisabled
}

func (featureDisabledBackupContentService) Serve(context.Context, content.GatewayRequest, http.ResponseWriter) error {
	return content.ErrContentNotFound
}

func (featureDisabledBackupContentService) RevokeSession(context.Context, string, string) error {
	return nil
}

func NewFeatureDisabledBackupContentHandlerConfigSource() BackupContentHandlerConfigSource {
	return func(context.Context) (BackupContentHandlerConfig, error) {
		return BackupContentHandlerConfig{TicketTimeout: 20 * time.Second}, nil
	}
}

// Issue creates a secured, cookie-scoped backup asset delivery ticket.
// @Summary      创建备份资产内容交付票据
// @Description  只接受 URI 中的精确 backup AssetRef；普通非敏感预览无需二次验证，secret/unknown 预览与原件下载分别要求精确 step-up。成功响应只包含无授权能力的同源 content_url，Cookie secret 仅通过精确 Path 的 HttpOnly Strict Cookie 返回。
// @Tags         backup-assets
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        id               path      string                      true   "恢复点 opaque ID"
// @Param        entryId          path      string                      true   "Catalog entry opaque ID"
// @Param        X-Xirang-Step-Up header    string                      false  "asset.secret_reveal 或 asset.download 精确 proof"
// @Param        body             body      backupContentTicketPayload  true   "闭合 renderer/profile 请求；不接受资源 locator"
// @Success      200              {object}  handlers.Response{data=content.TicketDescriptor}
// @Failure      400              {object}  handlers.Response
// @Failure      401              {object}  handlers.Response
// @Failure      403              {object}  handlers.Response
// @Failure      404              {object}  handlers.Response
// @Failure      409              {object}  handlers.Response
// @Failure      413              {object}  handlers.Response
// @Failure      503              {object}  handlers.Response
// @Router       /recovery-points/{id}/entries/{entryId}/delivery-tickets [post]
func (handler *BackupContentHandler) Issue(c *gin.Context) {
	if c == nil || c.Request == nil || c.Request.URL == nil || c.Request.URL.RawQuery != "" {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	config, ok := handler.loadConfig(c.Request.Context())
	if !ok {
		respondServiceUnavailable(c, "备份内容服务暂不可用")
		return
	}
	var payload backupContentTicketPayload
	if decodeStrictBackupContentJSON(c, &payload) != nil || !validBackupContentTicketPayload(payload) {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	ref := backupasset.AssetRef{
		RecoveryPointID: strings.TrimSpace(c.Param("recoveryPointId")),
		EntryID:         strings.TrimSpace(c.Param("entryId")),
	}
	if ref.RecoveryPointID == "" {
		ref.RecoveryPointID = strings.TrimSpace(c.Param("id"))
	}
	if backupasset.ValidateAssetRef(ref) != nil {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	binding, exists := middleware.CurrentSessionBinding(c)
	actor := content.DeliveryActor{
		UserID: middleware.CurrentUserID(c), Username: c.GetString(middleware.CtxUsername), Role: middleware.CurrentRole(c),
	}
	if !exists || actor.UserID == 0 || binding.UserID != actor.UserID || binding.Role != actor.Role ||
		backupasset.ValidateOpaqueID(binding.JTI) != nil || !binding.ExpiresAt.After(time.Now()) {
		respondUnauthorized(c, "会话无效")
		return
	}
	proof, proofOK := handler.deliveryProof(c, payload, actor)
	if !proofOK {
		return
	}
	secureCookie, err := backupContentSecureCookie(c.Request, config.AllowInsecureLoopback)
	if err != nil {
		respondServiceUnavailable(c, "需要安全传输")
		return
	}
	if handler == nil || handler.service == nil {
		respondServiceUnavailable(c, "备份内容服务暂不可用")
		return
	}
	ticketCtx, cancel := context.WithTimeout(c.Request.Context(), config.TicketTimeout)
	defer cancel()
	ticket, err := handler.service.Issue(ticketCtx, content.IssueRequest{
		Actor: actor,
		Session: content.DeliverySession{
			JTI: binding.JTI, UserID: binding.UserID, Role: binding.Role,
			TokenVersion: binding.TokenVersion, ExpiresAt: binding.ExpiresAt.UTC(),
		},
		Ref: ref, Action: payload.Action, Renderer: payload.Renderer, Profile: payload.Profile,
		Proof: proof, SecureCookie: secureCookie,
	})
	if err != nil {
		respondBackupContentIssueError(c, err)
		return
	}
	if !validIssuedBackupContentTicket(ticket, payload, secureCookie) {
		respondServiceUnavailable(c, "备份内容服务暂不可用")
		return
	}
	http.SetCookie(c.Writer, ticket.Cookie)
	respondOK(c, ticket.Descriptor)
}

// Serve is the explicit binary-response exception to JSON response helpers.
// @Summary      读取备份资产票据内容
// @Description  Cookie-only 同源内容路由；禁止 Authorization 与 query。支持 HEAD、完整 GET、单个 normal/open/suffix Range 和 If-Range；multipart Range 返回 416。content_url 中的 delivery ID 本身不授权。
// @Tags         backup-assets
// @Produce      octet-stream
// @Param        deliveryId  path    string  true   "无授权能力的 opaque delivery ID"
// @Param        Cookie      header  string  true   "精确 Path 的 xirang_asset_delivery HttpOnly Cookie"
// @Param        Range       header  string  false  "单个 bytes Range；multipart 不支持"
// @Param        If-Range    header  string  false  "强 ETag 或 HTTP date"
// @Success      200         {file}  binary
// @Success      206         {file}  binary
// @Failure      404         {string} string
// @Failure      412         {string} string
// @Failure      413         {string} string
// @Failure      416         {string} string
// @Failure      429         {string} string
// @Failure      503         {string} string
// @Router       /asset-content/{deliveryId} [get]
// @Router       /asset-content/{deliveryId} [head]
func (handler *BackupContentHandler) Serve(c *gin.Context) {
	if c == nil || c.Request == nil {
		return
	}
	setBackupContentSecurityHeaders(c.Writer.Header())
	request := c.Request
	deliveryID := strings.TrimSpace(c.Param("deliveryId"))
	canonicalPath := "/api/v1/asset-content/" + deliveryID
	if (request.Method != http.MethodGet && request.Method != http.MethodHead) || request.URL == nil ||
		request.URL.Path != canonicalPath || request.URL.RawQuery != "" || len(request.Header.Values("Authorization")) != 0 ||
		!validBackupContentBrowserRequest(request) {
		c.Status(http.StatusForbidden)
		return
	}
	if backupasset.ValidateOpaqueID(deliveryID) != nil || handler == nil || handler.service == nil {
		c.Status(http.StatusNotFound)
		return
	}
	rawCookie := strings.Join(request.Header.Values("Cookie"), "; ")
	err := handler.service.Serve(request.Context(), content.GatewayRequest{
		DeliveryID: deliveryID, Method: request.Method, RawCookie: rawCookie,
		RangeHeaders: request.Header.Values("Range"), IfRangeHeaders: request.Header.Values("If-Range"),
	}, c.Writer)
	if err == nil || c.Writer.Written() {
		return
	}
	switch {
	case errors.Is(err, content.ErrContentBudgetExceeded):
		c.Header("Retry-After", "1")
		c.Status(http.StatusTooManyRequests)
	case errors.Is(err, content.ErrContentUnavailable), errors.Is(err, content.ErrContentAuditUnavailable):
		c.Status(http.StatusServiceUnavailable)
	default:
		c.Status(http.StatusNotFound)
	}
}

func (handler *BackupContentHandler) Reject(c *gin.Context) {
	if c == nil {
		return
	}
	setBackupContentSecurityHeaders(c.Writer.Header())
	c.Status(http.StatusNotFound)
}

func (handler *BackupContentHandler) loadConfig(ctx context.Context) (BackupContentHandlerConfig, bool) {
	if handler == nil || handler.configSource == nil {
		return BackupContentHandlerConfig{}, false
	}
	config, err := handler.configSource(ctx)
	return config, err == nil && config.TicketTimeout >= time.Second && config.TicketTimeout <= 25*time.Second
}

func (handler *BackupContentHandler) deliveryProof(
	c *gin.Context,
	payload backupContentTicketPayload,
	actor content.DeliveryActor,
) (*content.StepUpProof, bool) {
	rawProof := strings.TrimSpace(c.GetHeader(StepUpHeaderName))
	expected := auth.StepUpActionAssetSecretReveal
	required := false
	if payload.Action == content.DeliveryDownload {
		expected = auth.StepUpActionAssetDownload
		required = true
	}
	if rawProof == "" {
		if required {
			respondStepUpRequired(c)
			return nil, false
		}
		return nil, true
	}
	claims, err := validateStepUpProof(handler.db, handler.jwtManager, rawProof, actor.UserID, actor.Role, expected)
	if err != nil || claims == nil || claims.ExpiresAt == nil || backupasset.ValidateOpaqueID(claims.ID) != nil {
		if errors.Is(err, ErrStepUpVerifierUnavailable) {
			respondServiceUnavailable(c, "二次验证服务暂不可用")
		} else {
			respondStepUpRequired(c)
		}
		return nil, false
	}
	return &content.StepUpProof{Action: claims.StepUpAction, ID: claims.ID, ExpiresAt: claims.ExpiresAt.UTC()}, true
}

func decodeStrictBackupContentJSON(c *gin.Context, target any) error {
	if c == nil || c.Request == nil || c.Request.Body == nil {
		return io.ErrUnexpectedEOF
	}
	payload, err := io.ReadAll(io.LimitReader(c.Request.Body, maxBackupContentTicketBytes+1))
	if err != nil || len(payload) == 0 || len(payload) > maxBackupContentTicketBytes {
		return io.ErrUnexpectedEOF
	}
	if err := rejectDuplicateBackupContentJSON(payload); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	return nil
}

func rejectDuplicateBackupContentJSON(payload []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if err := walkBackupContentJSON(decoder, token, 0); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	return nil
}

func walkBackupContentJSON(decoder *json.Decoder, token json.Token, depth int) error {
	if depth > 16 || token == nil {
		return io.ErrUnexpectedEOF
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		return nil
	}
	var closing json.Delim
	switch delimiter {
	case '{':
		closing = '}'
		members := make(map[string]bool)
		for decoder.More() {
			nameToken, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := nameToken.(string)
			if !ok || members[name] {
				return io.ErrUnexpectedEOF
			}
			members[name] = true
			value, err := decoder.Token()
			if err != nil {
				return err
			}
			if err := walkBackupContentJSON(decoder, value, depth+1); err != nil {
				return err
			}
		}
	case '[':
		closing = ']'
		for decoder.More() {
			value, err := decoder.Token()
			if err != nil {
				return err
			}
			if err := walkBackupContentJSON(decoder, value, depth+1); err != nil {
				return err
			}
		}
	default:
		return io.ErrUnexpectedEOF
	}
	end, err := decoder.Token()
	if err != nil || end != closing {
		return io.ErrUnexpectedEOF
	}
	return nil
}

func validBackupContentTicketPayload(payload backupContentTicketPayload) bool {
	if payload.SchemaVersion != 1 {
		return false
	}
	switch payload.Action {
	case content.DeliveryPreview:
		return payload.Renderer != content.RendererAttachment && validBackupContentRendererProfile(payload.Renderer, payload.Profile)
	case content.DeliveryDownload:
		return payload.Renderer == content.RendererAttachment && payload.Profile == content.ProfileOriginalV1
	default:
		return false
	}
}

func validBackupContentRendererProfile(renderer content.Renderer, profile content.RendererProfile) bool {
	switch renderer {
	case content.RendererEscapedText:
		return profile == content.ProfileTextV1
	case content.RendererSafeRaster:
		return profile == content.ProfileRasterV1
	case content.RendererSameOriginPDF:
		return profile == content.ProfilePDFV1
	case content.RendererNativeAudio:
		return profile == content.ProfileAudioV1
	case content.RendererNativeVideo:
		return profile == content.ProfileVideoV1
	case content.RendererMetadataHex:
		return profile == content.ProfileHexV1
	default:
		return false
	}
}

func validIssuedBackupContentTicket(
	ticket content.IssuedTicket,
	payload backupContentTicketPayload,
	secure bool,
) bool {
	if ticket.Cookie == nil || ticket.Descriptor.SchemaVersion != 1 || ticket.Descriptor.Action != payload.Action ||
		ticket.Descriptor.Renderer != payload.Renderer || ticket.Descriptor.Profile != payload.Profile ||
		ticket.Descriptor.ContentURL != ticket.Cookie.Path || ticket.Cookie.Name != content.DeliveryCookieName ||
		ticket.Cookie.Domain != "" || !ticket.Cookie.HttpOnly || ticket.Cookie.Secure != secure ||
		ticket.Cookie.SameSite != http.SameSiteStrictMode || !ticket.Cookie.Expires.Equal(ticket.Descriptor.ExpiresAt) ||
		!ticket.Descriptor.ExpiresAt.After(time.Now()) || ticket.Descriptor.IdleExpiresAt.After(ticket.Descriptor.ExpiresAt) {
		return false
	}
	parsed, err := url.Parse(ticket.Descriptor.ContentURL)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		!strings.HasPrefix(parsed.Path, "/api/v1/asset-content/") || parsed.Path != ticket.Cookie.Path {
		return false
	}
	_, err = content.ParseDeliveryCookie(ticket.Cookie.Name + "=" + ticket.Cookie.Value)
	return err == nil
}

func backupContentSecureCookie(request *http.Request, allowInsecureLoopback bool) (bool, error) {
	scheme, ok := backupContentEffectiveScheme(request)
	if !ok {
		return false, content.ErrContentUnavailable
	}
	if scheme == "https" {
		return true, nil
	}
	if !allowInsecureLoopback || request == nil || request.URL == nil || request.TLS != nil ||
		len(request.Header.Values("Forwarded")) != 0 || !backupContentLoopbackRemote(request.RemoteAddr) ||
		!backupContentLocalhost(request.Host) {
		return false, content.ErrContentUnavailable
	}
	return false, nil
}

func backupContentEffectiveScheme(request *http.Request) (string, bool) {
	if request == nil {
		return "", false
	}
	forwarded := request.Header.Values("X-Forwarded-Proto")
	if len(forwarded) > 1 {
		return "", false
	}
	scheme := "http"
	if request.TLS != nil {
		scheme = "https"
	}
	if len(forwarded) == 1 {
		value := strings.TrimSpace(strings.ToLower(forwarded[0]))
		if value != "http" && value != "https" || request.TLS != nil && value != "https" {
			return "", false
		}
		scheme = value
	}
	return scheme, true
}

func backupContentLoopbackRemote(remote string) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

func backupContentLocalhost(hostport string) bool {
	host := hostport
	if parsed, _, err := net.SplitHostPort(hostport); err == nil {
		host = parsed
	}
	host = strings.Trim(strings.ToLower(host), "[]")
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func validBackupContentBrowserRequest(request *http.Request) bool {
	if request == nil {
		return false
	}
	if values := request.Header.Values("Sec-Fetch-Site"); len(values) > 1 ||
		len(values) == 1 && values[0] != "same-origin" && values[0] != "none" {
		return false
	}
	origins := request.Header.Values("Origin")
	if len(origins) > 1 {
		return false
	}
	if len(origins) == 0 {
		return true
	}
	scheme, ok := backupContentEffectiveScheme(request)
	if !ok {
		return false
	}
	origin, err := url.Parse(origins[0])
	return err == nil && origin.Scheme == scheme && origin.Host == request.Host && origin.User == nil &&
		(origin.Path == "" || origin.Path == "/") && origin.RawQuery == "" && origin.Fragment == "" &&
		origins[0] == scheme+"://"+request.Host
}

func setBackupContentSecurityHeaders(header http.Header) {
	for _, name := range []string{
		"Access-Control-Allow-Origin", "Access-Control-Allow-Credentials", "Access-Control-Allow-Headers", "Access-Control-Allow-Methods",
	} {
		header.Del(name)
	}
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Cross-Origin-Resource-Policy", "same-origin")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("Content-Security-Policy", "sandbox; default-src 'none'; frame-ancestors 'self'; object-src 'none'")
	header.Set("X-Frame-Options", "SAMEORIGIN")
	header.Set("Cache-Control", "private, no-store")
	header.Set("Content-Encoding", "identity")
}

func respondBackupContentIssueError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, backupasset.ErrForbidden):
		respondForbidden(c, "权限不足")
	case errors.Is(err, backupasset.ErrNotFound), errors.Is(err, content.ErrContentNotFound):
		respondNotFound(c, "备份资产不存在")
	case errors.Is(err, content.ErrInvalidDeliveryProduct), errors.Is(err, content.ErrInvalidBrokerRequest):
		respondBadRequest(c, "请求参数不合法")
	case errors.Is(err, content.ErrContentFeatureDisabled), errors.Is(err, content.ErrContentAuditUnavailable),
		errors.Is(err, content.ErrContentAuditBacklogFull), errors.Is(err, content.ErrContentUnavailable),
		errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		respondServiceUnavailable(c, "备份内容服务暂不可用")
	default:
		respondServiceUnavailable(c, "备份内容服务暂不可用")
	}
}
