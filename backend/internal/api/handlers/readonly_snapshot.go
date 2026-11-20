package handlers

import (
	"context"

	"xirang/backend/internal/database"

	"gorm.io/gorm"
)

func withReadonlySnapshot(ctx context.Context, db *gorm.DB, fn func(*gorm.DB) error) error {
	return database.WithReadonlySnapshot(ctx, db, fn)
}
