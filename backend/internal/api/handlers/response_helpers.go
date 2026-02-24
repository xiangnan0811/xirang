package handlers

import (
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
)

func respondInternalError(c *gin.Context, err error) {
	if err != nil {
		log.Printf("服务器内部错误(path=%s): %v", c.FullPath(), err)
	}
	c.JSON(http.StatusInternalServerError, gin.H{"error": "服务器内部错误"})
}
