package database

import (
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestStateFixtureDSNSelectsRequestedDatabase(t *testing.T) {
	cases := map[string]string{
		"path only":        "postgres://u:p@127.0.0.1:5432/xirang_test?sslmode=disable&application_name=ci",
		"dbname":           "postgres://u:p@127.0.0.1:5432/xirang_test?dbname=xirang_test&sslmode=disable&application_name=ci",
		"database":         "postgres://u:p@127.0.0.1:5432/xirang_test?database=xirang_test&sslmode=disable&application_name=ci",
		"both spellings":   "postgres://u:p@127.0.0.1:5432/xirang_test?dbname=xirang_test&database=other&sslmode=disable&application_name=ci",
		"no path selector": "postgres://u:p@127.0.0.1:5432?database=xirang_test&sslmode=disable&application_name=ci",
	}
	for name, dsn := range cases {
		t.Run(name, func(t *testing.T) {
			config, err := pgx.ParseConfig(postgresStateDSN(t, dsn, "clone_db", "fixture_schema"))
			if err != nil {
				t.Fatal(err)
			}
			if config.Database != "clone_db" {
				t.Fatalf("database=%q, want clone_db", config.Database)
			}
			if config.User != "u" || config.Password != "p" || config.Host != "127.0.0.1" || config.Port != 5432 ||
				config.RuntimeParams["application_name"] != "ci" || config.RuntimeParams["search_path"] != "fixture_schema" ||
				config.RuntimeParams["timezone"] != "UTC" || config.TLSConfig != nil {
				t.Fatalf("connection parameters not preserved: %+v runtime=%v", config.Config, config.RuntimeParams)
			}
		})
	}
}
