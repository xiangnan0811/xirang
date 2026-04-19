package handlers

import (
	"database/sql"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// AdminMetricsHandler exposes aggregator diagnostic endpoints to admins.
type AdminMetricsHandler struct{ db *gorm.DB }

func NewAdminMetricsHandler(db *gorm.DB) *AdminMetricsHandler {
	return &AdminMetricsHandler{db: db}
}

// scanLatestBucket queries MAX(bucket_start) from the given table.
// It handles both Postgres (native time) and SQLite (string) drivers.
func scanLatestBucket(db *gorm.DB, table string) *time.Time {
	// Try Postgres path first via sql.NullTime.
	var nt sql.NullTime
	if err := db.Raw("SELECT MAX(bucket_start) FROM "+table).Scan(&nt).Error; err == nil && nt.Valid {
		t := nt.Time.UTC()
		return &t
	}

	// SQLite fallback: MAX returns a string (or nil).
	var raw *string
	if err := db.Raw("SELECT MAX(bucket_start) FROM "+table).Scan(&raw).Error; err != nil || raw == nil || *raw == "" {
		return nil
	}
	formats := []string{
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05Z",
		time.RFC3339Nano,
		time.RFC3339,
	}
	for _, f := range formats {
		if t, err := time.Parse(f, *raw); err == nil {
			t = t.UTC()
			return &t
		}
	}
	return nil
}

// RollupStatus returns latest bucket time and lag seconds for hourly + daily.
func (h *AdminMetricsHandler) RollupStatus(c *gin.Context) {
	now := time.Now().UTC()

	hourlyLatest := scanLatestBucket(h.db, "node_metric_samples_hourly")
	dailyLatest := scanLatestBucket(h.db, "node_metric_samples_daily")

	tier := func(latest *time.Time) gin.H {
		if latest == nil || latest.IsZero() {
			return gin.H{"latest_bucket": nil, "lag_seconds": nil}
		}
		lagSec := int(now.Sub(*latest).Seconds())
		return gin.H{"latest_bucket": latest, "lag_seconds": lagSec}
	}

	c.JSON(http.StatusOK, gin.H{
		"hourly": tier(hourlyLatest),
		"daily":  tier(dailyLatest),
	})
}
