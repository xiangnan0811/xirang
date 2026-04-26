package alerting

import (
	"testing"

	"xirang/backend/internal/model"
)

// TestDefaultRaiser_ImplementsRaiser is a compile-time + runtime check that
// DefaultRaiser satisfies the Raiser interface. If a future change removes
// or renames a method, this test will refuse to build.
func TestDefaultRaiser_ImplementsRaiser(t *testing.T) {
	db := openAlertingTestDB(t)
	if err := db.AutoMigrate(&model.Node{}, &model.Alert{}, &model.Integration{}, &model.AlertDelivery{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	var _ Raiser = DefaultRaiser{DB: db}
}
