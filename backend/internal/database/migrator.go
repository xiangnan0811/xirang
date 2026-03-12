package database

import (
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
