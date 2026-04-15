package database

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"gorm.io/gorm"
)

var dbQueryDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
	Name:    "db_query_duration_seconds",
	Help:    "Database query duration in seconds",
	Buckets: prometheus.DefBuckets,
}, []string{"operation"})

func RegisterMetricsCallbacks(db *gorm.DB) {
	for _, op := range []string{"create", "query", "update", "delete", "raw"} {
		callbackName := "metrics:" + op
		switch op {
		case "create":
			db.Callback().Create().Before("gorm:create").Register(callbackName+":before", setStartTime)
			db.Callback().Create().After("gorm:create").Register(callbackName+":after", recordDuration("create"))
		case "query":
			db.Callback().Query().Before("gorm:query").Register(callbackName+":before", setStartTime)
			db.Callback().Query().After("gorm:query").Register(callbackName+":after", recordDuration("query"))
		case "update":
			db.Callback().Update().Before("gorm:update").Register(callbackName+":before", setStartTime)
			db.Callback().Update().After("gorm:update").Register(callbackName+":after", recordDuration("update"))
		case "delete":
			db.Callback().Delete().Before("gorm:delete").Register(callbackName+":before", setStartTime)
			db.Callback().Delete().After("gorm:delete").Register(callbackName+":after", recordDuration("delete"))
		case "raw":
			db.Callback().Raw().Before("gorm:raw").Register(callbackName+":before", setStartTime)
			db.Callback().Raw().After("gorm:raw").Register(callbackName+":after", recordDuration("raw"))
		}
	}
}

const metricsStartTimeKey = "metrics:start_time"

func setStartTime(db *gorm.DB) {
	db.Set(metricsStartTimeKey, time.Now())
}

func recordDuration(operation string) func(*gorm.DB) {
	return func(db *gorm.DB) {
		if start, ok := db.Get(metricsStartTimeKey); ok {
			if startTime, ok := start.(time.Time); ok {
				dbQueryDuration.WithLabelValues(operation).Observe(time.Since(startTime).Seconds())
			}
		}
	}
}
