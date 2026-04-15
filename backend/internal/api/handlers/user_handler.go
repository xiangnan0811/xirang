package handlers

import (
	"strings"

	"xirang/backend/internal/auth"
	"xirang/backend/internal/middleware"

	"github.com/gin-gonic/gin"
)

type UserHandler struct {
	authService *auth.Service
}

func NewUserHandler(authService *auth.Service) *UserHandler {
	return &UserHandler{authService: authService}
}

type userResponse struct {
	ID          uint   `json:"id"`
	Username    string `json:"username"`
	Role        string `json:"role"`
	TOTPEnabled bool   `json:"totp_enabled"`
}

type createUserRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
	Role     string `json:"role" binding:"required"`
}

type updateUserRequest struct {
	Role     *string `json:"role"`
	Password *string `json:"password"`
}

// List godoc
// @Summary      列出所有用户
// @Description  返回系统中所有用户列表（admin only）
// @Tags         users
// @Security     Bearer
// @Produce      json
// @Success      200  {object}  handlers.Response{data=[]handlers.userResponse}
// @Failure      401  {object}  handlers.Response
// @Failure      403  {object}  handlers.Response
// @Router       /users [get]
func (h *UserHandler) List(c *gin.Context) {
	users, err := h.authService.ListUsers()
	if err != nil {
		respondInternalError(c, err)
		return
	}

	result := make([]userResponse, 0, len(users))
	for _, item := range users {
		result = append(result, userResponse{
			ID:          item.ID,
			Username:    item.Username,
			Role:        item.Role,
			TOTPEnabled: item.TOTPEnabled,
		})
	}
	respondOK(c, result)
}

// Create godoc
// @Summary      创建用户
// @Description  创建新用户账号（admin only）
// @Tags         users
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        body  body      createUserRequest  true  "创建用户请求"
// @Success      201   {object}  handlers.Response{data=handlers.userResponse}
// @Failure      400   {object}  handlers.Response
// @Failure      401   {object}  handlers.Response
// @Failure      403   {object}  handlers.Response
// @Router       /users [post]
func (h *UserHandler) Create(c *gin.Context) {
	var req createUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondBadRequest(c, "请求参数不合法")
		return
	}

	user, err := h.authService.CreateUser(req.Username, req.Password, req.Role)
	if err != nil {
		respondBadRequest(c, err.Error())
		return
	}

	respondCreated(c, userResponse{
		ID:          user.ID,
		Username:    user.Username,
		Role:        user.Role,
		TOTPEnabled: user.TOTPEnabled,
	})
}

// Update godoc
// @Summary      更新用户
// @Description  更新用户角色或密码（admin only）
// @Tags         users
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        id    path      int                true  "用户 ID"
// @Param        body  body      updateUserRequest  true  "更新用户请求"
// @Success      200   {object}  handlers.Response{data=handlers.userResponse}
// @Failure      400   {object}  handlers.Response
// @Failure      401   {object}  handlers.Response
// @Failure      403   {object}  handlers.Response
// @Router       /users/{id} [put]
func (h *UserHandler) Update(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}

	var req updateUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondBadRequest(c, "请求参数不合法")
		return
	}

	if req.Role != nil {
		trimmedRole := strings.TrimSpace(*req.Role)
		req.Role = &trimmedRole
	}
	if req.Password != nil {
		trimmedPassword := strings.TrimSpace(*req.Password)
		req.Password = &trimmedPassword
	}

	user, err := h.authService.UpdateUser(id, req.Role, req.Password)
	if err != nil {
		respondBadRequest(c, err.Error())
		return
	}

	respondOK(c, userResponse{
		ID:          user.ID,
		Username:    user.Username,
		Role:        user.Role,
		TOTPEnabled: user.TOTPEnabled,
	})
}

// Delete godoc
// @Summary      删除用户
// @Description  删除指定用户账号（admin only，不能删除自己）
// @Tags         users
// @Security     Bearer
// @Produce      json
// @Param        id   path      int  true  "用户 ID"
// @Success      200  {object}  handlers.Response
// @Failure      400  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Failure      403  {object}  handlers.Response
// @Router       /users/{id} [delete]
func (h *UserHandler) Delete(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}

	actorID := c.GetUint(middleware.CtxUserID)
	if err := h.authService.DeleteUser(id, actorID); err != nil {
		respondBadRequest(c, err.Error())
		return
	}

	respondMessage(c, "删除成功")
}
