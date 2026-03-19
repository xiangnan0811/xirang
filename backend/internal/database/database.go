package database

import (
	"fmt"
	"time"

	"xirang/backend/internal/config"

	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func configurePool(db *gorm.DB) error {
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	sqlDB.SetMaxOpenConns(25)
	sqlDB.SetMaxIdleConns(10)
	sqlDB.SetConnMaxLifetime(5 * time.Minute)
	return nil
}

func applySQLitePragmas(db *gorm.DB) error {
	if err := db.Exec("PRAGMA foreign_keys=ON;").Error; err != nil {
		return fmt.Errorf("设置 SQLite foreign_keys 失败: %w", err)
	}
	if err := db.Exec("PRAGMA journal_mode=WAL;").Error; err != nil {
		return fmt.Errorf("设置 SQLite WAL 模式失败: %w", err)
	}
	if err := db.Exec("PRAGMA busy_timeout=5000;").Error; err != nil {
		return fmt.Errorf("设置 SQLite busy_timeout 失败: %w", err)
	}
	if err := db.Exec("PRAGMA synchronous=NORMAL;").Error; err != nil {
		return fmt.Errorf("设置 SQLite synchronous 失败: %w", err)
	}
	return nil
}

func configureSQLitePool(db *gorm.DB) error {
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	// SQLite 同一时刻只允许一个写入者，多连接会导致 "database is locked"。
	// 单连接保证所有 PRAGMA 生效且写入串行化。
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	sqlDB.SetConnMaxLifetime(5 * time.Minute)
	return nil
}

func Open(cfg config.Config) (*gorm.DB, error) {
	switch cfg.DBType {
	case "sqlite":
		db, err := gorm.Open(sqlite.Open(cfg.SQLitePath), &gorm.Config{})
		if err != nil {
			return nil, fmt.Errorf("连接 sqlite 失败: %w", err)
		}
		if err := applySQLitePragmas(db); err != nil {
			return nil, err
		}
		if err := configureSQLitePool(db); err != nil {
			return nil, fmt.Errorf("配置连接池失败: %w", err)
		}
		return db, nil
	case "postgres":
		db, err := gorm.Open(postgres.Open(cfg.PostgresDSN), &gorm.Config{})
		if err != nil {
			return nil, fmt.Errorf("连接 postgres 失败: %w", err)
		}
		if err := configurePool(db); err != nil {
			return nil, fmt.Errorf("配置连接池失败: %w", err)
		}
		return db, nil
	default:
		return nil, fmt.Errorf("不支持的 DB 类型: %s", cfg.DBType)
	}
}
