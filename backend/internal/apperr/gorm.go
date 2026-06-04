package apperr

import (
	"errors"
	"strings"

	"gorm.io/gorm"
)

// WrapDBError wraps common GORM/database errors into sentinel errors.
// Usage: if err := db.Create(&node).Error; err != nil { return apperr.WrapDBError(err) }
func WrapDBError(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if strings.Contains(msg, "UNIQUE constraint") || strings.Contains(msg, "duplicate") {
		return errors.Join(ErrDuplicate, err)
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return errors.Join(ErrNotFound, err)
	}
	return err
}
