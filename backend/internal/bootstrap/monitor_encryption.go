package bootstrap

import (
	"fmt"

	"xirang/backend/internal/secure"

	"gorm.io/gorm"
)

// EncryptServiceMonitorHeaders seals historical service-monitor header JSON
// that predates the ServiceMonitor model hook. It is idempotent, operates on a
// SkipHooks query so raw ciphertext is never decrypted into a model, and runs
// inside one transaction so a key or write failure fails closed without leaving
// a partially migrated fleet.
//
// The startup caller must abort readiness when this returns an error. The
// migration also upgrades any enc:v1 values left by an older key rotation.
func EncryptServiceMonitorHeaders(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("service monitor encryption requires a database")
	}
	noHooks := db.Session(&gorm.Session{SkipHooks: true})
	updated, err := encryptServiceMonitorHeaders(noHooks)
	if err != nil {
		return err
	}
	if updated > 0 {
		bootLog.Info().Int("fields", updated).Msg("已加密服务监控请求头（历史明文/v1）")
	}
	return nil
}

// CountPlaintextServiceMonitorHeaders returns historical non-empty values that
// are neither enc:v1 nor enc:v2. Missing tables are treated as empty so this
// helper is safe during early bootstrap tests.
func CountPlaintextServiceMonitorHeaders(db *gorm.DB) (int64, error) {
	if db == nil {
		return 0, fmt.Errorf("service monitor encryption requires a database")
	}
	var count int64
	err := db.Session(&gorm.Session{SkipHooks: true}).Table("service_monitors").
		Where("http_headers <> '' AND http_headers NOT LIKE ? AND http_headers NOT LIKE ?", "enc:v1:%", "enc:v2:%").
		Count(&count).Error
	if err != nil && isMissingRelation(err) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("统计 service_monitors.http_headers 明文失败: %w", err)
	}
	return count, nil
}

type serviceMonitorHeaderRow struct {
	ID      uint   `gorm:"column:id"`
	Headers string `gorm:"column:http_headers"`
}

func encryptServiceMonitorHeaders(db *gorm.DB) (int, error) {
	returnCount := 0
	err := db.Transaction(func(tx *gorm.DB) error {
		var rows []serviceMonitorHeaderRow
		if err := tx.Table("service_monitors").Select("id", "http_headers").Find(&rows).Error; err != nil {
			if isMissingRelation(err) {
				return nil
			}
			return fmt.Errorf("读取 service_monitors.http_headers 失败: %w", err)
		}
		for _, row := range rows {
			value := row.Headers
			if value == "" {
				continue
			}
			var encrypted string
			var changed bool
			if secure.IsV1Encrypted(value) {
				var err error
				encrypted, changed, err = secure.ReEncryptV1Value(value)
				if err != nil {
					return fmt.Errorf("service_monitors id=%d 请求头 v1 重加密失败: %w", row.ID, err)
				}
			} else if secure.IsEncrypted(value) {
				continue
			} else {
				var err error
				encrypted, err = secure.EncryptString(value)
				if err != nil {
					return fmt.Errorf("service_monitors id=%d 请求头明文加密失败: %w", row.ID, err)
				}
				changed = true
			}
			if !changed {
				continue
			}
			result := tx.Table("service_monitors").Where("id = ?", row.ID).Update("http_headers", encrypted)
			if result.Error != nil {
				return fmt.Errorf("service_monitors id=%d 请求头更新失败: %w", row.ID, result.Error)
			}

			if result.RowsAffected != 1 {
				return fmt.Errorf("service_monitors id=%d 请求头更新行数异常", row.ID)
			}
			returnCount++
		}
		return nil
	})
	return returnCount, err
}
