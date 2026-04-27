package database

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"log"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/database/sqlite3"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"gorm.io/gorm"
)

//go:embed migrations/sqlite/*.sql
var sqliteMigrationsFS embed.FS

//go:embed migrations/postgres/*.sql
var postgresMigrationsFS embed.FS

// RunMigrations 使用 golang-migrate 执行版本化数据库迁移。
func RunMigrations(db *gorm.DB, dbType string) error {
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("获取底层 sql.DB 失败: %w", err)
	}

	var fs embed.FS
	var subdir string
	switch dbType {
	case "sqlite":
		fs = sqliteMigrationsFS
		subdir = "migrations/sqlite"
	case "postgres":
		fs = postgresMigrationsFS
		subdir = "migrations/postgres"
	default:
		return fmt.Errorf("不支持的数据库类型: %s", dbType)
	}

	if err := preMigrationFixups(sqlDB, dbType); err != nil {
		return fmt.Errorf("执行迁移前置修复失败: %w", err)
	}

	source, err := iofs.New(fs, subdir)
	if err != nil {
		return fmt.Errorf("加载迁移文件失败: %w", err)
	}
	// 只关闭迁移文件源；不关闭数据库驱动，以免关闭调用方传入的 sql.DB 连接。
	defer func() {
		if srcErr := source.Close(); srcErr != nil {
			log.Printf("关闭迁移源失败: %v", srcErr)
		}
	}()

	var m *migrate.Migrate

	switch dbType {
	case "sqlite":
		driver, driverErr := sqlite3.WithInstance(sqlDB, &sqlite3.Config{})
		if driverErr != nil {
			return fmt.Errorf("创建 sqlite3 迁移驱动失败: %w", driverErr)
		}
		m, err = migrate.NewWithInstance("iofs", source, "sqlite3", driver)
	case "postgres":
		driver, driverErr := pgx.WithInstance(sqlDB, &pgx.Config{})
		if driverErr != nil {
			return fmt.Errorf("创建 postgres 迁移驱动失败: %w", driverErr)
		}
		m, err = migrate.NewWithInstance("iofs", source, "pgx5", driver)
	}

	if err != nil {
		return fmt.Errorf("初始化迁移器失败: %w", err)
	}

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("执行迁移失败: %w", err)
	}

	version, dirty, _ := m.Version()
	log.Printf("数据库迁移完成，当前版本: %d, dirty: %v", version, dirty)

	return nil
}

// preMigrationFixups 处理那些无法用纯 SQL 表达的、与历史 schema 漂移相关的
// 一次性修复。这些修复必须幂等（重复运行无副作用），并且要在 golang-migrate
// 的版本化迁移之前执行，因为后续迁移可能假设修复已完成。
func preMigrationFixups(db *sql.DB, dbType string) error {
	return fixupLegacyPolicyBwlimit(db, dbType)
}

// fixupLegacyPolicyBwlimit 修复 policies 表的 bwlimit 列名漂移。
//
// 背景：早期版本的代码用 GORM AutoMigrate 创建 policies 表，BwLimit 字段被
// 自动转成 snake_case 列名 bw_limit。后来引入 golang-migrate，基线迁移
// 000001 写的是 bwlimit（无下划线），并在模型上加了 column:bwlimit 显式
// 标签。新建库走得通，但既存的 legacy 库仍然保留 bw_limit 列名，导致
// GORM 的 SELECT/UPDATE 报 "no such column: bwlimit"。
//
// 这个修复仅在检测到漂移时（旧列存在 + 新列缺失）执行重命名，对全新库
// 是无操作。
func fixupLegacyPolicyBwlimit(db *sql.DB, dbType string) error {
	var oldQuery, newQuery string
	switch dbType {
	case "sqlite":
		oldQuery = "SELECT COUNT(*) FROM pragma_table_info('policies') WHERE name='bw_limit'"
		newQuery = "SELECT COUNT(*) FROM pragma_table_info('policies') WHERE name='bwlimit'"
	case "postgres":
		oldQuery = "SELECT COUNT(*) FROM information_schema.columns WHERE table_name='policies' AND column_name='bw_limit'"
		newQuery = "SELECT COUNT(*) FROM information_schema.columns WHERE table_name='policies' AND column_name='bwlimit'"
	default:
		return nil
	}

	var oldExists, newExists int
	if err := db.QueryRow(oldQuery).Scan(&oldExists); err != nil {
		return fmt.Errorf("查询 policies.bw_limit 是否存在失败: %w", err)
	}
	if err := db.QueryRow(newQuery).Scan(&newExists); err != nil {
		return fmt.Errorf("查询 policies.bwlimit 是否存在失败: %w", err)
	}

	if oldExists == 1 && newExists == 0 {
		if _, err := db.Exec("ALTER TABLE policies RENAME COLUMN bw_limit TO bwlimit"); err != nil {
			return fmt.Errorf("重命名 policies.bw_limit -> bwlimit 失败: %w", err)
		}
		log.Println("已修复 legacy 列名: policies.bw_limit -> bwlimit")
	}
	return nil
}
