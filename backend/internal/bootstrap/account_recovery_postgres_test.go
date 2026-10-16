package bootstrap

import (
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func openAccountRecoveryPostgres(t *testing.T) *gorm.DB {
	t.Helper()
	rawDSN := os.Getenv("TEST_POSTGRES_DSN")
	if rawDSN == "" {
		t.Skip("TEST_POSTGRES_DSN is required for real PostgreSQL account recovery evidence")
	}
	parsed, err := url.Parse(rawDSN)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		t.Fatal("TEST_POSTGRES_DSN must be a PostgreSQL URL")
	}
	root, err := gorm.Open(postgres.Open(rawDSN), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal("open recovery PostgreSQL root connection")
	}
	rootSQL, err := root.DB()
	if err != nil {
		t.Fatal("get PostgreSQL root pool")
	}
	t.Cleanup(func() { _ = rootSQL.Close() })
	id, err := GenerateAccountRecoveryOperationID()
	if err != nil {
		t.Fatal("generate exclusive recovery schema ID")
	}
	schema := "account_recovery_test_" + id
	if err := root.Exec(fmt.Sprintf(`CREATE SCHEMA "%s"`, schema)).Error; err != nil {
		t.Fatal("create exclusive recovery schema")
	}
	t.Cleanup(func() {
		if err := root.Exec(fmt.Sprintf(`DROP SCHEMA "%s" CASCADE`, schema)).Error; err != nil {
			t.Error("drop exclusive recovery schema")
		}
	})
	query := parsed.Query()
	query.Set("search_path", schema)
	query.Set("timezone", "UTC")
	parsed.RawQuery = query.Encode()
	db, err := gorm.Open(postgres.Open(parsed.String()), &gorm.Config{Logger: logger.Discard, NowFunc: func() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }})
	if err != nil {
		t.Fatal("open isolated recovery PostgreSQL pool")
	}
	finishAccountRecoveryFixture(t, db)
	return db
}

func TestAccountRecoveryPostgres(t *testing.T) {
	testAccountRecovery(t, openAccountRecoveryPostgres)
}
