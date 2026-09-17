package middleware

import "github.com/gin-gonic/gin"

// apiResponse mirrors the handler envelope without a middleware/handler cycle.
type apiResponse struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data"`
}

func respondAPIError(c *gin.Context, status int, message string) {
	c.AbortWithStatusJSON(status, apiResponse{Code: status, Message: message})
}
