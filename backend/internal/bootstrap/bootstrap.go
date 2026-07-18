package bootstrap

import (
	"fmt"
	"os"
	"strings"

	"xirang/backend/internal/auth"
	"xirang/backend/internal/database"
	"xirang/backend/internal/logger"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"

	"gorm.io/gorm"
)

var bootLog = logger.Module("bootstrap")

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

	// Integration: Endpoint, Secret, ProxyURL
	n, err = reEncryptColumns(noHooks, "integrations", map[string]string{
		"endpoint":  "endpoint",
		"secret":    "secret",
		"proxy_url": "proxy_url",
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

	// Policy: PreHook, PostHook, drill verify scripts (may embed secrets)
	n, err = reEncryptColumns(noHooks, "policies", map[string]string{
		"pre_hook":          "pre_hook",
		"post_hook":         "post_hook",
		"drill_pre_verify":  "drill_pre_verify",
		"drill_verify":      "drill_verify",
		"drill_post_verify": "drill_post_verify",
	})
	if err != nil {
		return fmt.Errorf("policies 迁移失败: %w", err)
	}
	total += n

	// AppCredential: Config
	n, err = reEncryptColumns(noHooks, "app_credentials", map[string]string{
		"config": "config",
	})
	if err != nil {
		return fmt.Errorf("app_credentials 迁移失败: %w", err)
	}
	total += n

	// SystemSetting: sensitive runtime settings persisted through settings.Service.
	n, err = reEncryptSystemSettings(noHooks)
	if err != nil {
		return fmt.Errorf("system_settings 迁移失败: %w", err)
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

	for _, encryptedOverlay := range []struct {
		table   string
		columns map[string]string
	}{
		{"backup_asset_saved_searches", map[string]string{"encrypted_ast": "encrypted_ast"}},
		{"backup_asset_favorites", map[string]string{"encrypted_label": "encrypted_label"}},
		{"backup_asset_tag_definitions", map[string]string{"encrypted_name": "encrypted_name"}},
		{"backup_asset_overlay_idempotency", map[string]string{"encrypted_request_fingerprint": "encrypted_request_fingerprint"}},
	} {
		n, err = reEncryptColumns(noHooks, encryptedOverlay.table, encryptedOverlay.columns)
		if err != nil {
			return fmt.Errorf("%s 迁移失败: %w", encryptedOverlay.table, err)
		}
		total += n
	}

	// Drill scripts were historically stored as plaintext. Encrypt them even when
	// no enc:v1: residual remains (reEncryptColumns only rewrites v1 ciphertext).
	n, err = encryptPlaintextPolicyDrillScripts(noHooks)
	if err != nil {
		return fmt.Errorf("policies drill 明文加密失败: %w", err)
	}
	total += n

	if total > 0 {
		bootLog.Info().Int("fields", total).Msg("加密迁移完成（v1→v2 / 明文→enc）")
	}
	return nil
}

// EncryptPlaintextPolicyDrillScripts encrypts non-empty, non-encrypted
// drill_pre_verify / drill_verify / drill_post_verify values left from builds
// before those fields entered model hooks. Idempotent — skips enc:v1:/enc:v2:
// and empty strings. Safe to run on every startup.
func EncryptPlaintextPolicyDrillScripts(db *gorm.DB) error {
	noHooks := db.Session(&gorm.Session{SkipHooks: true})
	n, err := encryptPlaintextPolicyDrillScripts(noHooks)
	if err != nil {
		return err
	}
	if n > 0 {
		bootLog.Info().Int("fields", n).Msg("已加密策略演练校验脚本文段（历史明文）")
	}
	return nil
}

var encryptedSystemSettingKeys = []string{
	"metrics.remote_bearer_token",
	"smtp.password",
}

// encryptPlaintextPolicyDrillScripts encrypts plain drill script columns.
// Loads rows fully before updating to avoid SQLite "table is locked" while a
// streaming Rows cursor is open.
func encryptPlaintextPolicyDrillScripts(db *gorm.DB) (int, error) {
	type drillRow struct {
		ID              uint
		DrillPreVerify  string `gorm:"column:drill_pre_verify"`
		DrillVerify     string `gorm:"column:drill_verify"`
		DrillPostVerify string `gorm:"column:drill_post_verify"`
	}
	var rows []drillRow
	if err := db.Table("policies").
		Select("id", "drill_pre_verify", "drill_verify", "drill_post_verify").
		Find(&rows).Error; err != nil {
		// Table may not exist yet during early bootstrap tests.
		if strings.Contains(err.Error(), "no such table") || strings.Contains(strings.ToLower(err.Error()), "does not exist") {
			return 0, nil
		}
		return 0, err
	}

	updated := 0
	for _, row := range rows {
		fields := []struct {
			col string
			val string
		}{
			{"drill_pre_verify", row.DrillPreVerify},
			{"drill_verify", row.DrillVerify},
			{"drill_post_verify", row.DrillPostVerify},
		}
		updates := map[string]interface{}{}
		for _, f := range fields {
			// Do not TrimSpace — migration must preserve leading/trailing script content.
			val := f.val
			if val == "" || secure.IsEncrypted(val) {
				continue
			}
			// EncryptString (not EncryptIfNeeded) so whitespace-only scripts are also
			// sealed rather than treated as empty by EncryptIfNeeded's TrimSpace.
			enc, err := secure.EncryptString(val)
			if err != nil {
				return updated, fmt.Errorf("policies id=%d 列 %s 明文加密失败: %w", row.ID, f.col, err)
			}
			updates[f.col] = enc
			updated++
		}
		if len(updates) > 0 {
			if err := db.Table("policies").Where("id = ?", row.ID).Updates(updates).Error; err != nil {
				return updated, fmt.Errorf("policies id=%d 更新失败: %w", row.ID, err)
			}
		}
	}
	return updated, nil
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

	type pendingUpdate struct {
		id      any
		updates map[string]any
	}
	pending := make([]pendingUpdate, 0)
	for rows.Next() {
		// 动态扫描
		values := make([]interface{}, len(cols))
		var id any
		values[0] = &id
		strPtrs := make([]*string, len(cols)-1)
		for i := range strPtrs {
			strPtrs[i] = new(string)
			values[i+1] = strPtrs[i]
		}
		if err := rows.Scan(values...); err != nil {
			return 0, err
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
				return 0, fmt.Errorf("表 %s id=%v 列 %s 重加密失败: %w", table, id, col, err)
			}
			if changed {
				updates[col] = newVal
			}
		}

		if len(updates) > 0 {
			pending = append(pending, pendingUpdate{id: id, updates: updates})
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}

	updated := 0
	for _, item := range pending {
		if err := db.Table(table).Where("id = ?", item.id).Updates(item.updates).Error; err != nil {
			return updated, fmt.Errorf("表 %s id=%v 更新失败: %w", table, item.id, err)
		}
		updated += len(item.updates)
	}
	return updated, nil
}

func reEncryptSystemSettings(db *gorm.DB) (int, error) {
	type settingRow struct {
		Key   string
		Value string
	}
	var rows []settingRow
	if err := db.Table("system_settings").Select("key", "value").Where("key IN ?", encryptedSystemSettingKeys).Find(&rows).Error; err != nil {
		return 0, err
	}

	updated := 0
	for _, row := range rows {
		if !secure.IsV1Encrypted(row.Value) {
			continue
		}
		newVal, changed, err := secure.ReEncryptV1Value(row.Value)
		if err != nil {
			return updated, fmt.Errorf("system_settings key=%s 重加密失败: %w", row.Key, err)
		}
		if !changed {
			continue
		}
		if err := db.Table("system_settings").Where("key = ?", row.Key).Update("value", newVal).Error; err != nil {
			return updated, fmt.Errorf("system_settings key=%s 更新失败: %w", row.Key, err)
		}
		updated++
	}
	return updated, nil
}

// HasV1EncryptedData 快速检查是否存在 v1 加密数据。
// 查询失败时 fail-closed 返回 true，促使启动路径尝试迁移而非假定干净。
func HasV1EncryptedData(db *gorm.DB) bool {
	n, err := CountV1EncryptedData(db)
	if err != nil {
		bootLog.Warn().Err(err).Msg("统计 v1 加密残留失败，将尝试迁移路径")
		return true
	}
	return n > 0
}

// CountPlaintextPolicyDrillScripts returns the number of non-empty drill script
// fields that are not yet enc:v1:/enc:v2: ciphertext. Used by encryption-status.
// DB errors are returned so callers cannot treat "query failed" as zero residual.
func CountPlaintextPolicyDrillScripts(db *gorm.DB) (int64, error) {
	columns := []string{"drill_pre_verify", "drill_verify", "drill_post_verify"}
	var total int64
	for _, col := range columns {
		var count int64
		// Non-empty and not already encrypted (prefix check is sufficient).
		if err := db.Table("policies").
			Where(col+" <> '' AND "+col+" NOT LIKE ? AND "+col+" NOT LIKE ?", "enc:v1:%", "enc:v2:%").
			Count(&count).Error; err != nil {
			return 0, fmt.Errorf("统计明文 drill 字段 %s 失败: %w", col, err)
		}
		total += count
	}
	return total, nil
}

// CountV1EncryptedData 返回所有受加密保护字段中仍以 enc:v1: 开头的记录总数。
// 与 HasV1EncryptedData 不同，此函数遍历所有列、不在第一条命中即返回，方便
// 运维通过监控接口（GET /system/encryption-status）观察 V1 残留消减进度，
// 作为后续退役 V1 解密支持的判定依据。DB 查询失败返回 error。
func CountV1EncryptedData(db *gorm.DB) (int64, error) {
	tables := []struct {
		table   string
		columns []string
	}{
		{"nodes", []string{"password", "private_key"}},
		{"ssh_keys", []string{"private_key"}},
		{"integrations", []string{"endpoint", "secret", "proxy_url"}},
		{"tasks", []string{"executor_config"}},
		{"policies", []string{"pre_hook", "post_hook", "drill_pre_verify", "drill_verify", "drill_post_verify"}},
		{"app_credentials", []string{"config"}},
		{"users", []string{"totp_secret", "recovery_codes"}},
		{"backup_asset_saved_searches", []string{"encrypted_ast"}},
		{"backup_asset_favorites", []string{"encrypted_label"}},
		{"backup_asset_tag_definitions", []string{"encrypted_name"}},
		{"backup_asset_overlay_idempotency", []string{"encrypted_request_fingerprint"}},
	}

	var total int64
	for _, t := range tables {
		for _, col := range t.columns {
			var count int64
			if err := db.Table(t.table).Where(col+" LIKE ?", "enc:v1:%").Count(&count).Error; err != nil {
				if isMissingRelation(err) {
					continue
				}
				return 0, fmt.Errorf("统计 %s.%s v1 残留失败: %w", t.table, col, err)
			}
			total += count
		}
	}

	var sensitiveSettingsCount int64
	if err := db.Table("system_settings").Where("key IN ? AND value LIKE ?", encryptedSystemSettingKeys, "enc:v1:%").Count(&sensitiveSettingsCount).Error; err != nil {
		if !isMissingRelation(err) {
			return 0, fmt.Errorf("统计 system_settings v1 残留失败: %w", err)
		}
	} else {
		total += sensitiveSettingsCount
	}
	return total, nil
}

func isMissingRelation(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "no such table") || strings.Contains(s, "does not exist")
}
