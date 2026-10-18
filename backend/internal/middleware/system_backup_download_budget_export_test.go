package middleware

import (
	"time"

	"github.com/gin-gonic/gin"
)

// SystemBackupDownloadBudgetForTest gives external consumer tests short, real
// prepare deadlines without changing production defaults or exporting a knob.
func SystemBackupDownloadBudgetForTest(total, prepare time.Duration) gin.HandlerFunc {
	return systemBackupDownloadBudget(total, prepare)
}
