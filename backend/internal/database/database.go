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

func Open(cfg config.Config) (*gorm.DB, error) {
	switch cfg.DBType {
	case "sqlite":
		db, err := gorm.Open(sqlite.Open(cfg.SQLitePath), &gorm.Config{})
		if err != nil {
			return nil, fmt.Errorf("连接 sqlite 失败: %w", err)
		}
		if err := configurePool(db); err != nil {
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
