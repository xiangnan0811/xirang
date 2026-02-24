package handlers

import (
	"net/http"
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
	ID       uint   `json:"id"`
	Username string `json:"username"`
	Role     string `json:"role"`
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

func (h *UserHandler) List(c *gin.Context) {
	users, err := h.authService.ListUsers()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "服务器内部错误"})
		return
	}

	result := make([]userResponse, 0, len(users))
	for _, item := range users {
		result = append(result, userResponse{
			ID:       item.ID,
			Username: item.Username,
			Role:     item.Role,
		})
	}
	c.JSON(http.StatusOK, gin.H{"data": result})
}

func (h *UserHandler) Create(c *gin.Context) {
	var req createUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数不合法"})
		return
	}

	user, err := h.authService.CreateUser(req.Username, req.Password, req.Role)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, gin.H{"data": userResponse{
		ID:       user.ID,
		Username: user.Username,
		Role:     user.Role,
	}})
}

func (h *UserHandler) Update(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}

	var req updateUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数不合法"})
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
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"data": userResponse{
		ID:       user.ID,
		Username: user.Username,
		Role:     user.Role,
	}})
}

func (h *UserHandler) Delete(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}

	actorID := c.GetUint(middleware.CtxUserID)
	if err := h.authService.DeleteUser(id, actorID); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "删除成功"})
}
