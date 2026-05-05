package handlers

import (
	"encoding/json"
	"fmt"
	"strings"

	"xirang/backend/internal/model"
	"xirang/backend/internal/profile"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// knownAppCredentialTypes 允许的凭据类型。
var knownAppCredentialTypes = map[string]bool{
	"mysql": true, "postgres": true, "mongodb": true, "redis": true,
	"docker-mysql": true, "docker-postgres": true, "docker-mongodb": true, "docker-redis": true,
}

// hostFirstCredentialTypes 容器类 type 需提供 container_name 而不提供 host/port。
var containerCredentialTypes = map[string]bool{
	"docker-mysql": true, "docker-postgres": true, "docker-mongodb": true, "docker-redis": true,
}

type AppCredentialHandler struct {
	db *gorm.DB
}

func NewAppCredentialHandler(db *gorm.DB) *AppCredentialHandler {
	return &AppCredentialHandler{db: db}
}

type appCredentialRequest struct {
	Type        string `json:"type" binding:"required"`
	Name        string `json:"name" binding:"required"`
	Description string `json:"description"`
	Host        string `json:"host"`
	Port        string `json:"port"`
	User        string `json:"user"`
	Password    string `json:"password"`
	// ContainerName 容器类必须。
	ContainerName string `json:"container_name"`
}

type appCredentialResponse struct {
	ID            uint                   `json:"id"`
	Name          string                 `json:"name"`
	Type          string                 `json:"type"`
	Description   string                 `json:"description"`
	Config        map[string]interface{} `json:"config"`
	HasPassword   bool                   `json:"has_password"`
	ReferenceCount int64                 `json:"reference_count"`
	CreatedAt     string                 `json:"created_at"`
	UpdatedAt     string                 `json:"updated_at"`
}

func sanitizeAppCredential(item *model.AppCredential, refCount int64) appCredentialResponse {
	createdAt := ""
	updatedAt := ""
	if !item.CreatedAt.IsZero() {
		createdAt = item.CreatedAt.Format("2006-01-02 15:04:05")
	}
	if !item.UpdatedAt.IsZero() {
		updatedAt = item.UpdatedAt.Format("2006-01-02 15:04:05")
	}
	return appCredentialResponse{
		ID:             item.ID,
		Name:           item.Name,
		Type:           item.Type,
		Description:    item.Description,
		Config:         item.SanitizedConfig(),
		HasPassword:    item.HasPassword,
		ReferenceCount: refCount,
		CreatedAt:      createdAt,
		UpdatedAt:      updatedAt,
	}
}

func buildConfigJSON(req appCredentialRequest) string {
	cfg := map[string]interface{}{}
	if req.Host != "" {
		cfg["host"] = req.Host
	}
	if req.Port != "" {
		cfg["port"] = req.Port
	}
	if req.User != "" {
		cfg["user"] = req.User
	}
	if req.Password != "" {
		cfg["password"] = req.Password
	}
	if req.ContainerName != "" {
		cfg["container_name"] = req.ContainerName
	}
	b, _ := json.Marshal(cfg)
	return string(b)
}

func validateCredentialRequest(req appCredentialRequest) error {
	req.Type = strings.TrimSpace(strings.ToLower(req.Type))
	req.Name = strings.TrimSpace(req.Name)
	if req.Type == "" || req.Name == "" {
		return fmt.Errorf("type 和 name 为必填项")
	}
	if !knownAppCredentialTypes[req.Type] {
		return fmt.Errorf("不支持的凭据类型: %s", req.Type)
	}
	isContainer := containerCredentialTypes[req.Type]
	if isContainer && strings.TrimSpace(req.ContainerName) == "" {
		return fmt.Errorf("容器化类型 (%s) 必须提供 container_name", req.Type)
	}
	return nil
}

func setHasPassword(item *model.AppCredential) {
	var raw map[string]interface{}
	if err := json.Unmarshal([]byte(item.Config), &raw); err != nil {
		return
	}
	if pw, ok := raw["password"]; ok && pw != "" {
		item.HasPassword = true
	}
}

func countCredentialReferences(db *gorm.DB, credentialID uint) (int64, error) {
	var count int64
	err := db.Model(&model.Policy{}).Where("app_credential_id = ?", credentialID).Count(&count).Error
	return count, err
}

// List godoc
// @Summary      列出凭据
// @Description  返回所有应用凭据列表（password 已脱敏）
// @Tags         app-credentials
// @Security     Bearer
// @Produce      json
// @Success      200  {object}  handlers.Response{data=[]appCredentialResponse}
// @Failure      401  {object}  handlers.Response
// @Router       /app-credentials [get]
func (h *AppCredentialHandler) List(c *gin.Context) {
	var items []model.AppCredential
	if err := h.db.Order("id asc").Find(&items).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	out := make([]appCredentialResponse, 0, len(items))
	for i := range items {
		setHasPassword(&items[i])
		refCount, _ := countCredentialReferences(h.db, items[i].ID)
		out = append(out, sanitizeAppCredential(&items[i], refCount))
	}
	respondOK(c, out)
}

// Get godoc
// @Summary      获取凭据详情
// @Description  返回单个凭据的详细信息（password 已脱敏）
// @Tags         app-credentials
// @Security     Bearer
// @Produce      json
// @Param        id   path      int  true  "凭据 ID"
// @Success      200  {object}  handlers.Response{data=appCredentialResponse}
// @Failure      401  {object}  handlers.Response
// @Failure      404  {object}  handlers.Response
// @Router       /app-credentials/{id} [get]
func (h *AppCredentialHandler) Get(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var item model.AppCredential
	if err := h.db.First(&item, id).Error; err != nil {
		respondNotFound(c, "凭据不存在")
		return
	}
	setHasPassword(&item)
	refCount, _ := countCredentialReferences(h.db, id)
	respondOK(c, sanitizeAppCredential(&item, refCount))
}

// Create godoc
// @Summary      创建凭据
// @Description  创建新的应用凭据（password 加密入库）
// @Tags         app-credentials
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        body  body      appCredentialRequest  true  "创建凭据请求"
// @Success      201   {object}  handlers.Response{data=appCredentialResponse}
// @Failure      400   {object}  handlers.Response
// @Failure      401   {object}  handlers.Response
// @Router       /app-credentials [post]
func (h *AppCredentialHandler) Create(c *gin.Context) {
	var req appCredentialRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	req.Type = strings.TrimSpace(strings.ToLower(req.Type))
	req.Name = strings.TrimSpace(req.Name)
	if err := validateCredentialRequest(req); err != nil {
		respondBadRequest(c, err.Error())
		return
	}

	item := model.AppCredential{
		Name:        req.Name,
		Type:        req.Type,
		Description: strings.TrimSpace(req.Description),
		Config:      buildConfigJSON(req),
	}
	if err := h.db.Create(&item).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	item.HasPassword = req.Password != ""
	respondCreated(c, sanitizeAppCredential(&item, 0))
}

// Update godoc
// @Summary      更新凭据
// @Description  完整更新凭据配置（password 为空字符串则不修改）
// @Tags         app-credentials
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        id    path      int                   true  "凭据 ID"
// @Param        body  body      appCredentialRequest  true  "更新凭据请求"
// @Success      200   {object}  handlers.Response{data=appCredentialResponse}
// @Failure      400   {object}  handlers.Response
// @Failure      401   {object}  handlers.Response
// @Failure      404   {object}  handlers.Response
// @Router       /app-credentials/{id} [put]
func (h *AppCredentialHandler) Update(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var req appCredentialRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	req.Type = strings.TrimSpace(strings.ToLower(req.Type))
	req.Name = strings.TrimSpace(req.Name)
	if err := validateCredentialRequest(req); err != nil {
		respondBadRequest(c, err.Error())
		return
	}

	var item model.AppCredential
	if err := h.db.First(&item, id).Error; err != nil {
		respondNotFound(c, "凭据不存在")
		return
	}

	setHasPassword(&item)
	hadPassword := item.HasPassword

	// 保存旧 config map（用于级联更新时对比 hook 是否被用户修改过）
	var oldConfigMap map[string]interface{}
	json.Unmarshal([]byte(item.Config), &oldConfigMap) //nolint:errcheck

	newCfg := buildConfigJSON(req)
	// 密码为空字符串 → 保留原密码
	if req.Password == "" && hadPassword {
		var oldCfg map[string]interface{}
		if err := json.Unmarshal([]byte(item.Config), &oldCfg); err == nil {
			if pw, ok := oldCfg["password"]; ok {
				var newCfgMap map[string]interface{}
				json.Unmarshal([]byte(newCfg), &newCfgMap) //nolint:errcheck
				newCfgMap["password"] = pw
				b, _ := json.Marshal(newCfgMap)
				newCfg = string(b)
			}
		}
	}

	// 解析新 config map（用于级联渲染）
	var newConfigMap map[string]interface{}
	json.Unmarshal([]byte(newCfg), &newConfigMap) //nolint:errcheck

	item.Name = req.Name
	item.Type = req.Type
	item.Description = strings.TrimSpace(req.Description)
	item.Config = newCfg

	err := h.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Save(&item).Error; err != nil {
			return err
		}
		// #5 级联更新：重新渲染所有引用此 credential 的 Policy 的 hook
		return cascadePolicyHooks(tx, id, oldConfigMap, newConfigMap)
	})
	if err != nil {
		respondInternalError(c, err)
		return
	}
	item.HasPassword = hadPassword || req.Password != ""
	refCount, _ := countCredentialReferences(h.db, id)
	respondOK(c, sanitizeAppCredential(&item, refCount))
}

// cascadePolicyHooks 查询所有引用 credentialID 的 Policy，对其中 app_profile 非空的，
// 用旧 config 重新渲染 hook 并与当前存储的 hook 比对。若当前 hook 与旧渲染值一致（说明
// 未被用户手动修改），则更新为新渲染值；否则保留用户手动编辑的内容。
func cascadePolicyHooks(db *gorm.DB, credentialID uint, oldConfig, newConfig map[string]interface{}) error {
	var policies []model.Policy
	if err := db.Where("app_credential_id = ? AND app_profile != ''", credentialID).Find(&policies).Error; err != nil {
		return err
	}
	for _, p := range policies {
		// 用旧 config 重新渲染，判断当前 hook 是否匹配旧渲染值
		oldPre, oldPost, err := profile.RenderHooks(p.AppProfile, oldConfig)
		if err != nil {
			continue // 渲染失败跳过，不阻塞 credential 更新
		}
		newPre, newPost, err := profile.RenderHooks(p.AppProfile, newConfig)
		if err != nil {
			continue
		}
		needsSave := false
		if p.PreHook == oldPre && newPre != oldPre {
			p.PreHook = newPre
			needsSave = true
		}
		if p.PostHook == oldPost && newPost != oldPost {
			p.PostHook = newPost
			needsSave = true
		}
		if needsSave {
			if err := db.Save(&p).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

// ListProfiles godoc
// @Summary      列出应用感知备份 profile
// @Description  返回所有 8 个内置 profile 的公开信息（schema 供前端动态渲染表单，不含 hook 模板）
// @Tags         app-credentials
// @Security     Bearer
// @Produce      json
// @Success      200  {object}  handlers.Response{data=[]profile.ProfileDefinition}
// @Failure      401  {object}  handlers.Response
// @Router       /app-credentials/profiles [get]
func (h *AppCredentialHandler) ListProfiles(c *gin.Context) {
	respondOK(c, profile.ListProfiles())
}

// Delete godoc
// @Summary      删除凭据
// @Description  删除指定凭据（有 Policy 引用时阻止删除）
// @Tags         app-credentials
// @Security     Bearer
// @Produce      json
// @Param        id   path      int  true  "凭据 ID"
// @Success      200  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Failure      404  {object}  handlers.Response
// @Failure      409  {object}  handlers.Response
// @Router       /app-credentials/{id} [delete]
func (h *AppCredentialHandler) Delete(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var item model.AppCredential
	if err := h.db.First(&item, id).Error; err != nil {
		respondNotFound(c, "凭据不存在")
		return
	}
	refCount, err := countCredentialReferences(h.db, id)
	if err != nil {
		respondInternalError(c, err)
		return
	}
	if refCount > 0 {
		respondConflict(c, fmt.Sprintf("该凭据被 %d 个备份策略引用，无法删除", refCount))
		return
	}
	if err := h.db.Delete(&item).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	respondMessage(c, "deleted")
}
