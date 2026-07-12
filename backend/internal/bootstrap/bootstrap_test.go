package bootstrap

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func openBootstrapTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_loc=UTC", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	return db
}

func TestSeedUsersRequiresAdminInitialPassword(t *testing.T) {
	db := openBootstrapTestDB(t)
	if err := db.AutoMigrate(&model.User{}); err != nil {
		t.Fatalf("初始化用户表失败: %v", err)
	}

	t.Setenv("ADMIN_INITIAL_PASSWORD", "")
	if err := SeedUsers(db); err == nil {
		t.Fatalf("期望缺少 ADMIN_INITIAL_PASSWORD 时返回错误")
	}
}

func TestSeedUsersCreatesAdminOnly(t *testing.T) {
	db := openBootstrapTestDB(t)
	if err := db.AutoMigrate(&model.User{}); err != nil {
		t.Fatalf("初始化用户表失败: %v", err)
	}

	t.Setenv("ADMIN_INITIAL_PASSWORD", "StrongAdmin#2026")
	if err := SeedUsers(db); err != nil {
		t.Fatalf("初始化用户失败: %v", err)
	}

	var users []model.User
	if err := db.Order("id asc").Find(&users).Error; err != nil {
		t.Fatalf("查询用户失败: %v", err)
	}
	if len(users) != 1 {
		t.Fatalf("期望仅创建 admin 用户，实际数量: %d", len(users))
	}
	if users[0].Username != "admin" || users[0].Role != "admin" {
		t.Fatalf("期望仅存在 admin/admin 用户，实际: %+v", users[0])
	}
	if strings.TrimSpace(users[0].PasswordHash) == "" {
		t.Fatalf("期望 admin 密码哈希不为空")
	}

	if err := SeedUsers(db); err != nil {
		t.Fatalf("重复执行 SeedUsers 不应报错，实际: %v", err)
	}
	var count int64
	if err := db.Model(&model.User{}).Count(&count).Error; err != nil {
		t.Fatalf("统计用户失败: %v", err)
	}
	if count != 1 {
		t.Fatalf("重复执行后用户数量应保持 1，实际: %d", count)
	}
}

func TestSeedUsersAllowsMissingPasswordWhenAdminAlreadyExists(t *testing.T) {
	db := openBootstrapTestDB(t)
	if err := db.AutoMigrate(&model.User{}); err != nil {
		t.Fatalf("初始化用户表失败: %v", err)
	}

	t.Setenv("ADMIN_INITIAL_PASSWORD", "StrongAdmin#2026")
	if err := SeedUsers(db); err != nil {
		t.Fatalf("首次初始化用户失败: %v", err)
	}

	t.Setenv("ADMIN_INITIAL_PASSWORD", "")
	if err := SeedUsers(db); err != nil {
		t.Fatalf("admin 已存在时不应强制要求 ADMIN_INITIAL_PASSWORD，实际: %v", err)
	}
}

func TestAutoMigrateIncludesTaskTrafficSample(t *testing.T) {
	db := openBootstrapTestDB(t)

	if err := AutoMigrate(db, "sqlite"); err != nil {
		t.Fatalf("AutoMigrate 失败: %v", err)
	}

	if !db.Migrator().HasTable(&model.TaskTrafficSample{}) {
		t.Fatalf("期望 AutoMigrate 创建 task_traffic_samples 表")
	}
}

func TestMigrateEncryptionV1ToV2IncludesSensitiveSettings(t *testing.T) {
	key := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", key)
	secure.ResetForTesting()

	db := openBootstrapTestDB(t)
	if err := db.AutoMigrate(
		&model.Node{},
		&model.SSHKey{},
		&model.Integration{},
		&model.Task{},
		&model.Policy{},
		&model.AppCredential{},
		&model.User{},
		&model.SystemSetting{},
	); err != nil {
		t.Fatalf("初始化加密迁移测试表失败: %v", err)
	}
	legacyValue := encryptV1ForTest(t, key, "FAKE_SMTP_PASSWORD_FOR_TEST_ONLY")
	if err := db.Create(&model.SystemSetting{Key: "smtp.password", Value: legacyValue}).Error; err != nil {
		t.Fatalf("写入 v1 设置失败: %v", err)
	}

	if got, err := CountV1EncryptedData(db); err != nil {
		t.Fatalf("统计 v1 失败: %v", err)
	} else if got != 1 {
		t.Fatalf("迁移前应统计到 1 条 v1 设置，实际 %d", got)
	}
	if err := MigrateEncryptionV1ToV2(db); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}

	var row model.SystemSetting
	if err := db.First(&row, "key = ?", "smtp.password").Error; err != nil {
		t.Fatalf("读取迁移后设置失败: %v", err)
	}
	if !strings.HasPrefix(row.Value, "enc:v2:") {
		t.Fatalf("期望迁移为 enc:v2，实际 %q", row.Value)
	}
	plain, err := secure.DecryptString(row.Value)
	if err != nil {
		t.Fatalf("解密迁移后设置失败: %v", err)
	}
	if plain != "FAKE_SMTP_PASSWORD_FOR_TEST_ONLY" {
		t.Fatalf("迁移后明文不匹配: %q", plain)
	}
	if got, err := CountV1EncryptedData(db); err != nil {
		t.Fatalf("统计 v1 失败: %v", err)
	} else if got != 0 {
		t.Fatalf("迁移后不应残留 v1 设置，实际 %d", got)
	}
}

func TestEncryptPlaintextPolicyDrillScripts(t *testing.T) {
	key := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", key)
	secure.ResetForTesting()

	db := openBootstrapTestDB(t)
	if err := db.AutoMigrate(&model.Policy{}); err != nil {
		t.Fatalf("migrate policies: %v", err)
	}
	// Leading/trailing whitespace must be preserved (no TrimSpace on migrate).
	const prePlain = "  echo pre-plain  "
	const midPlain = "echo mid-plain"
	const postPlain = "\techo post-plain\n"
	// Insert raw plaintext (skip hooks) — simulates pre-hook deployments.
	if err := db.Session(&gorm.Session{SkipHooks: true}).Exec(
		`INSERT INTO policies (name, source_path, target_path, cron_spec, drill_pre_verify, drill_verify, drill_post_verify, enabled, retention_mode)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"plain-drill", "/src", "/dst", "0 * * * *",
		prePlain, midPlain, postPlain,
		true, "simple",
	).Error; err != nil {
		t.Fatalf("seed plain drill scripts: %v", err)
	}

	if got, err := CountPlaintextPolicyDrillScripts(db); err != nil {
		t.Fatalf("count plain before: %v", err)
	} else if got != 3 {
		t.Fatalf("before encrypt expected 3 plaintext fields, got %d", got)
	}

	if err := EncryptPlaintextPolicyDrillScripts(db); err != nil {
		t.Fatalf("encrypt plain: %v", err)
	}
	// Second run is a no-op.
	if err := EncryptPlaintextPolicyDrillScripts(db); err != nil {
		t.Fatalf("encrypt plain second pass: %v", err)
	}

	if got, err := CountPlaintextPolicyDrillScripts(db); err != nil {
		t.Fatalf("count plain after: %v", err)
	} else if got != 0 {
		t.Fatalf("after encrypt expected 0 plaintext fields, got %d", got)
	}

	var raw struct {
		Pre  string `gorm:"column:drill_pre_verify"`
		Mid  string `gorm:"column:drill_verify"`
		Post string `gorm:"column:drill_post_verify"`
	}
	if err := db.Session(&gorm.Session{SkipHooks: true}).Table("policies").
		Select("drill_pre_verify, drill_verify, drill_post_verify").
		Where("name = ?", "plain-drill").Scan(&raw).Error; err != nil {
		t.Fatalf("scan: %v", err)
	}
	for name, v := range map[string]string{"pre": raw.Pre, "mid": raw.Mid, "post": raw.Post} {
		if !strings.HasPrefix(v, "enc:v2:") {
			t.Fatalf("%s expected enc:v2 at rest, got %q", name, v)
		}
		if strings.Contains(v, "plain") {
			t.Fatalf("%s still contains plaintext: %q", name, v)
		}
	}

	var loaded model.Policy
	if err := db.Where("name = ?", "plain-drill").First(&loaded).Error; err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.DrillPreVerify != prePlain || loaded.DrillVerify != midPlain || loaded.DrillPostVerify != postPlain {
		t.Fatalf("AfterFind must preserve exact script bytes: pre=%q mid=%q post=%q",
			loaded.DrillPreVerify, loaded.DrillVerify, loaded.DrillPostVerify)
	}
}

func encryptV1ForTest(t *testing.T, rawKey string, value string) string {
	t.Helper()
	key, err := base64.StdEncoding.DecodeString(rawKey)
	if err != nil {
		t.Fatalf("解析测试加密密钥失败: %v", err)
	}
	block, err := aes.NewCipher(key[:32])
	if err != nil {
		t.Fatalf("创建测试 cipher 失败: %v", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("创建测试 gcm 失败: %v", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	ciphertext := gcm.Seal(nil, nonce, []byte(value), nil)
	packed := append(nonce, ciphertext...)
	return "enc:v1:" + base64.StdEncoding.EncodeToString(packed)
}
