package bootstrap

import (
	"fmt"
	"log"
	"os"
	"strings"

	"xirang/backend/internal/auth"
	"xirang/backend/internal/database"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"

	"gorm.io/gorm"
)

func AutoMigrate(db *gorm.DB, dbType string) error {
	return database.RunMigrations(db, dbType)
}

func SeedUsers(db *gorm.DB) error {
	var count int64
	if err := db.Model(&model.User{}).Where("username = ?", "admin").Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}

	adminPassword := strings.TrimSpace(os.Getenv("ADMIN_INITIAL_PASSWORD"))
	if adminPassword == "" {
		return fmt.Errorf("ADMIN_INITIAL_PASSWORD 不能为空")
	}
	if err := auth.ValidatePasswordStrength(adminPassword); err != nil {
		return fmt.Errorf("ADMIN_INITIAL_PASSWORD 强度不足: %w", err)
	}

	hash, err := auth.HashPassword(adminPassword)
	if err != nil {
		return fmt.Errorf("生成用户密码哈希失败: %w", err)
	}
	user := model.User{
		Username:     "admin",
		PasswordHash: hash,
		Role:         "admin",
	}
	if err := db.Create(&user).Error; err != nil {
		return err
	}
	return nil
}

// MigrateEncryptionV1ToV2 将所有 enc:v1: 加密字段重新加密为 enc:v2:（argon2id KDF）。
// 幂等操作——仅处理 v1 数据，v2 数据跳过。
func MigrateEncryptionV1ToV2(db *gorm.DB) error {
	noHooks := db.Session(&gorm.Session{SkipHooks: true})
	total := 0

	// Node: Password, PrivateKey
	n, err := reEncryptColumns(noHooks, "nodes", map[string]string{
		"password":    "password",
		"private_key": "private_key",
	})
	if err != nil {
		return fmt.Errorf("nodes 迁移失败: %w", err)
	}
	total += n

	// SSHKey: PrivateKey
	n, err = reEncryptColumns(noHooks, "ssh_keys", map[string]string{
		"private_key": "private_key",
	})
	if err != nil {
		return fmt.Errorf("ssh_keys 迁移失败: %w", err)
	}
	total += n

	// Integration: Endpoint, Secret
	n, err = reEncryptColumns(noHooks, "integrations", map[string]string{
		"endpoint": "endpoint",
		"secret":   "secret",
	})
	if err != nil {
		return fmt.Errorf("integrations 迁移失败: %w", err)
	}
	total += n

	// Task: ExecutorConfig
	n, err = reEncryptColumns(noHooks, "tasks", map[string]string{
		"executor_config": "executor_config",
	})
	if err != nil {
		return fmt.Errorf("tasks 迁移失败: %w", err)
	}
	total += n

	// User: TOTPSecret, RecoveryCodes
	n, err = reEncryptColumns(noHooks, "users", map[string]string{
		"totp_secret":    "totp_secret",
		"recovery_codes": "recovery_codes",
	})
	if err != nil {
		return fmt.Errorf("users 迁移失败: %w", err)
	}
	total += n

	if total > 0 {
		log.Printf("info: 加密迁移完成，共更新 %d 个字段（v1 → v2）", total)
	}
	return nil
}

// reEncryptColumns 对指定表的指定列进行 v1→v2 重加密，返回更新字段数。
func reEncryptColumns(db *gorm.DB, table string, columns map[string]string) (int, error) {
	// 构建 SELECT 列表
	cols := []string{"id"}
	for col := range columns {
		cols = append(cols, col)
	}

	rows, err := db.Table(table).Select(cols).Rows()
	if err != nil {
		return 0, err
	}
	defer rows.Close() //nolint:errcheck

	updated := 0
	for rows.Next() {
		// 动态扫描
		values := make([]interface{}, len(cols))
		var id uint
		values[0] = &id
		strPtrs := make([]*string, len(cols)-1)
		for i := range strPtrs {
			strPtrs[i] = new(string)
			values[i+1] = strPtrs[i]
		}
		if err := rows.Scan(values...); err != nil {
			return updated, err
		}

		updates := map[string]interface{}{}
		// 按 cols 顺序（跳过 id）匹配
		i := 0
		for _, col := range cols[1:] {
			val := *strPtrs[i]
			i++
			if !secure.IsV1Encrypted(val) {
				continue
			}
			newVal, changed, err := secure.ReEncryptV1Value(val)
			if err != nil {
				return updated, fmt.Errorf("表 %s id=%d 列 %s 重加密失败: %w", table, id, col, err)
			}
			if changed {
				updates[col] = newVal
				updated++
			}
		}

		if len(updates) > 0 {
			if err := db.Table(table).Where("id = ?", id).Updates(updates).Error; err != nil {
				return updated, fmt.Errorf("表 %s id=%d 更新失败: %w", table, id, err)
			}
		}
	}
	return updated, nil
}

// HasV1EncryptedData 快速检查是否存在 v1 加密数据。
func HasV1EncryptedData(db *gorm.DB) bool {
	return CountV1EncryptedData(db) > 0
}

// CountV1EncryptedData 返回所有受加密保护字段中仍以 enc:v1: 开头的记录总数。
// 与 HasV1EncryptedData 不同，此函数遍历所有列、不在第一条命中即返回，方便
// 运维通过监控接口（GET /system/encryption-status）观察 V1 残留消减进度，
// 作为后续退役 V1 解密支持的判定依据。
func CountV1EncryptedData(db *gorm.DB) int64 {
	tables := []struct {
		table   string
		columns []string
	}{
		{"nodes", []string{"password", "private_key"}},
		{"ssh_keys", []string{"private_key"}},
		{"integrations", []string{"endpoint", "secret"}},
		{"tasks", []string{"executor_config"}},
		{"users", []string{"totp_secret", "recovery_codes"}},
	}

	var total int64
	for _, t := range tables {
		for _, col := range t.columns {
			var count int64
			if err := db.Table(t.table).Where(col+" LIKE ?", "enc:v1:%").Count(&count).Error; err == nil {
				total += count
			}
		}
	}
	return total
}

