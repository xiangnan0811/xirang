package database

import (
	"crypto/rand"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	pgxmigrate "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"
)

type postgresStateTemplate struct {
	name   string
	schema string
	ready  bool
}

type postgresStateServer struct {
	admin     *sql.DB
	canCreate bool
	templates map[uint]postgresStateTemplate
}

var postgresStateFixtures struct {
	sync.Mutex
	nonce   string
	next    uint64
	servers map[string]*postgresStateServer
	logOnce sync.Once
}

func TestMain(m *testing.M) {
	code := m.Run()
	for _, server := range postgresStateFixtures.servers {
		for _, template := range server.templates {
			if _, err := server.admin.Exec("DROP DATABASE " + pgx.Identifier{template.name}.Sanitize()); err != nil {
				fmt.Fprintf(os.Stderr, "drop PostgreSQL fixture template %s: %v\n", template.name, err)
				code = 1
			}
		}
		if err := server.admin.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "close PostgreSQL fixture admin connection: %v\n", err)
			code = 1
		}
	}
	os.Exit(code)
}

// openStateAt is only for tests whose starting point is a pristine schema; the
// migration itself must not be the operation under test.
func (fixture migrationFixture) openStateAt(t *testing.T, version uint) (*migrate.Migrate, *sql.DB) {
	t.Helper()
	if fixture.engine != "postgres" {
		return fixture.openAt(t, version)
	}
	if os.Getenv("XIRANG_TEST_PG_TEMPLATE") == "0" {
		postgresStateFixtures.logOnce.Do(func() { t.Log("PostgreSQL template fixtures disabled; using migrations per test") })
		return fixture.openAt(t, version)
	}
	postgresStateFixtures.Lock()
	defer postgresStateFixtures.Unlock()
	if postgresStateFixtures.servers == nil {
		var nonce [8]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			t.Fatalf("generate PostgreSQL fixture run nonce: %v", err)
		}
		postgresStateFixtures.nonce = fmt.Sprintf("%x", nonce)
		postgresStateFixtures.servers = make(map[string]*postgresStateServer)
	}
	server := postgresStateFixtures.servers[fixture.dsn]
	if server == nil {
		admin, err := openPostgresSQLDB(fixture.dsn)
		if err != nil {
			t.Fatalf("open PostgreSQL fixture admin connection: %v", err)
		}
		var canCreate bool
		if err := admin.QueryRow(`SELECT rolcreatedb OR rolsuper FROM pg_roles WHERE rolname = current_user`).Scan(&canCreate); err != nil {
			_ = admin.Close()
			t.Fatalf("check PostgreSQL CREATEDB privilege: %v", err)
		}
		server = &postgresStateServer{admin: admin, canCreate: canCreate, templates: make(map[uint]postgresStateTemplate)}
		postgresStateFixtures.servers[fixture.dsn] = server
	}
	if !server.canCreate {
		postgresStateFixtures.logOnce.Do(func() { t.Log("PostgreSQL role lacks CREATEDB; using migrations per test") })
		return fixture.openAt(t, version)
	}
	template, ok := server.templates[version]
	if !ok {
		postgresStateFixtures.next++
		template = postgresStateTemplate{
			name:   fmt.Sprintf("xirang_%s_t_%d", postgresStateFixtures.nonce, postgresStateFixtures.next),
			schema: fmt.Sprintf("xirang_backup_asset_%d", time.Now().UnixNano()),
		}
		if _, err := server.admin.Exec("CREATE DATABASE " + pgx.Identifier{template.name}.Sanitize()); err != nil {
			t.Fatalf("create PostgreSQL fixture template: %v", err)
		}
		server.templates[version] = template
		buildPostgresStateTemplate(t, fixture.dsn, template, version)
		template.ready = true
		server.templates[version] = template
	}
	if !template.ready {
		t.Fatal("PostgreSQL fixture template migration did not complete")
	}
	postgresStateFixtures.next++
	clone := fmt.Sprintf("xirang_%s_c_%d", postgresStateFixtures.nonce, postgresStateFixtures.next)
	if _, err := server.admin.Exec("CREATE DATABASE " + pgx.Identifier{clone}.Sanitize() + " TEMPLATE " + pgx.Identifier{template.name}.Sanitize()); err != nil {
		t.Fatalf("clone PostgreSQL fixture template: %v", err)
	}
	t.Cleanup(func() {
		if _, err := server.admin.Exec("DROP DATABASE " + pgx.Identifier{clone}.Sanitize()); err != nil {
			t.Errorf("drop PostgreSQL fixture clone %s: %v", clone, err)
		}
	})
	migrator, db := openPostgresStateMigrator(t, fixture.dsn, clone, template.schema)
	t.Cleanup(func() {
		sourceErr, databaseErr := migrator.Close()
		if sourceErr != nil || databaseErr != nil {
			t.Errorf("close PostgreSQL fixture migrator: source=%v database=%v", sourceErr, databaseErr)
		}
	})
	return migrator, db
}

func postgresStateDSN(t *testing.T, dsn, database, schema string) string {
	t.Helper()
	parsed, err := url.Parse(dsn)
	if err != nil || parsed.Scheme == "" {
		t.Fatalf("TEST_POSTGRES_DSN must be a PostgreSQL URL: %v", err)
	}
	parsed.Path = "/" + database
	parsed.RawPath = ""
	query := parsed.Query()
	// pgx applies query settings after the URL path, and accepts both spellings.
	query.Del("dbname")
	query.Del("database")
	query.Set("search_path", schema)
	query.Set("timezone", "UTC")
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func openPostgresStateMigrator(t *testing.T, dsn, database, schema string) (*migrate.Migrate, *sql.DB) {
	t.Helper()
	db, err := openPostgresSQLDB(postgresStateDSN(t, dsn, database, schema))
	if err != nil {
		t.Fatalf("open PostgreSQL fixture database: %v", err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		t.Fatalf("ping PostgreSQL fixture database: %v", err)
	}
	source, err := iofs.New(postgresMigrationsFS, "migrations/postgres")
	if err != nil {
		_ = db.Close()
		t.Fatalf("open embedded PostgreSQL migrations: %v", err)
	}
	driver, err := pgxmigrate.WithInstance(db, &pgxmigrate.Config{})
	if err != nil {
		_ = source.Close()
		_ = db.Close()
		t.Fatalf("create PostgreSQL migration driver: %v", err)
	}
	migrator, err := migrate.NewWithInstance("iofs", source, "pgx5", driver)
	if err != nil {
		_ = source.Close()
		_ = driver.Close()
		t.Fatalf("create PostgreSQL migrator: %v", err)
	}
	return migrator, db
}

func buildPostgresStateTemplate(t *testing.T, dsn string, template postgresStateTemplate, version uint) {
	t.Helper()
	admin, err := openPostgresSQLDB(postgresStateDSN(t, dsn, template.name, "public"))
	if err != nil {
		t.Fatalf("open PostgreSQL template: %v", err)
	}
	if _, err := admin.Exec("CREATE SCHEMA " + pgx.Identifier{template.schema}.Sanitize()); err != nil {
		_ = admin.Close()
		t.Fatalf("create PostgreSQL template schema: %v", err)
	}
	if err := admin.Close(); err != nil {
		t.Fatalf("close PostgreSQL template schema connection: %v", err)
	}
	migrator, _ := openPostgresStateMigrator(t, dsn, template.name, template.schema)
	defer func() {
		sourceErr, databaseErr := migrator.Close()
		if sourceErr != nil || databaseErr != nil {
			t.Fatalf("close PostgreSQL template migrator: source=%v database=%v", sourceErr, databaseErr)
		}
	}()
	migrateToBackupAssetVersion(t, migrator, version)
}
