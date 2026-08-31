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

// ErrMigrationDirty 表示 schema_migrations.dirty=1。上一次迁移可能只执行了一部分，
// 服务必须在任何 schema 修复或前向迁移之前拒绝启动，交由备份恢复或离线核验流程处理。
var ErrMigrationDirty = errors.New("schema_migrations.dirty=1，前次迁移未正常完成，拒绝启动")

// ErrMigrationPrecondition means live data cannot safely be represented by the
// next migration. The migration runner rejects before golang-migrate marks the
// target version dirty, so an audited data repair can be retried directly.
var ErrMigrationPrecondition = errors.New("migration precondition failed; startup refused")

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

	// 在启动 migrator 之前先检查 schema_migrations.dirty 状态。dirty=1 表示前次迁移
	// 失败且 golang-migrate 没有自动恢复 —— 继续 Up() 会基于损坏的 schema 写入更多
	// 数据，最坏情况导致不可恢复的双时区污染（参考 migration 000050 风险说明）。
	// 这里直接读底层 sqlDB 而非 migrator 句柄，因为 migrator.Version() 在 dirty 时
	// 会返回 dirty=true 但调用方仍然有可能继续走 Up；显式 fast-fail 才是预期行为。
	dirty, version, err := checkMigrationDirty(sqlDB, dbType)
	if err != nil {
		return fmt.Errorf("检查 schema_migrations dirty 状态失败: %w", err)
	}
	if dirty {
		log.Printf("FATAL: schema_migrations.dirty=1, version=%d. 拒绝启动；请从校验备份恢复或执行离线核验", version)
		return fmt.Errorf("%w (version=%d)", ErrMigrationDirty, version)
	}
	if err := validateMinimumRecoverySchema(sqlDB, dbType, version); err != nil {
		return err
	}
	if err := preflightDrillDurableRecoveryMigration(sqlDB, dbType, version); err != nil {
		return err
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
		// golang-migrate 在 Up() 内部还会做一次 dirty 检查。即便状态在上面的
		// 只读检查后发生变化，这里也必须保持 fail-closed，绝不 Force 或重试。
		var dirtyErr migrate.ErrDirty
		if errors.As(err, &dirtyErr) {
			return fmt.Errorf("%w (version=%d)", ErrMigrationDirty, dirtyErr.Version)
		}
		return fmt.Errorf("执行迁移失败: %w", err)
	}

	versionAfter, dirtyAfter, err := m.Version()
	if err != nil {
		return fmt.Errorf("读取迁移后版本失败: %w", err)
	}
	if dirtyAfter {
		return fmt.Errorf("%w (version=%d)", ErrMigrationDirty, versionAfter)
	}
	if err := validateMinimumRecoverySchema(sqlDB, dbType, int64(versionAfter)); err != nil {
		return err
	}
	log.Printf("数据库迁移完成，当前版本: %d, dirty: %v", versionAfter, dirtyAfter)

	return nil
}

func preflightDrillDurableRecoveryMigration(db *sql.DB, dbType string, version int64) error {
	if version >= drillDurableRecoverySchemaVersion {
		return nil
	}
	taskRunsExist, err := migrationRelationExists(db, dbType, "task_runs", "table")
	if err != nil {
		return fmt.Errorf("%w (version=%d, reason=catalog_query_failed)", ErrMigrationPrecondition, version)
	}
	if !taskRunsExist {
		return nil
	}

	var duplicateTaskCount int64
	err = db.QueryRow(`
		SELECT COUNT(*)
		FROM (
			SELECT task_id
			FROM task_runs
			WHERE trigger_type = 'drill'
			  AND status IN ('pending', 'running', 'retrying')
			GROUP BY task_id
			HAVING COUNT(*) > 1
		) AS duplicate_active_drills`).Scan(&duplicateTaskCount)
	if err != nil {
		return fmt.Errorf("%w (version=%d, reason=catalog_query_failed)", ErrMigrationPrecondition, version)
	}
	if duplicateTaskCount > 0 {
		return fmt.Errorf("%w (version=%d, reason=duplicate_active_drill); terminalize each TaskRun/Evidence pair atomically before retrying", ErrMigrationPrecondition, version)
	}
	return nil
}

// preMigrationFixups 处理那些无法用纯 SQL 表达的、与历史 schema 漂移相关的
// 一次性修复。这些修复必须幂等（重复运行无副作用），并且要在 golang-migrate
// 的版本化迁移之前执行，因为后续迁移可能假设修复已完成。
func preMigrationFixups(db *sql.DB, dbType string) error {
	return fixupLegacyPolicyBwlimit(db, dbType)
}

// checkMigrationDirty 直接读 schema_migrations 表判断当前迁移是否处于 dirty 状态。
// 表不存在视为 dirty=false（全新部署）。
//
// 返回 (dirty, version, err)。当 dirty=true 时，调用方应拒绝启动。
func checkMigrationDirty(db *sql.DB, dbType string) (bool, int64, error) {
	var existsQuery string
	switch dbType {
	case "sqlite":
		existsQuery = "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='schema_migrations'"
	case "postgres":
		// Resolve this identifier through the same search_path as the read below.
		existsQuery = "SELECT COUNT(*) FROM pg_catalog.pg_class WHERE oid = pg_catalog.to_regclass('schema_migrations')"
	default:
		return false, 0, fmt.Errorf("不支持的数据库类型: %s", dbType)
	}

	var tableCount int
	if err := db.QueryRow(existsQuery).Scan(&tableCount); err != nil {
		return false, 0, fmt.Errorf("查询 schema_migrations 表是否存在失败: %w", err)
	}
	if tableCount == 0 {
		// 全新部署：还没有迁移表，自然不可能 dirty
		return false, 0, nil
	}

	// schema_migrations 在 golang-migrate 里只有一行（最新版本）。
	// SQLite/PG 列结构相同：(version BIGINT/INTEGER, dirty BOOLEAN/INTEGER)。
	// 注意 SQLite 把 dirty 存为 0/1 整数；PG 存为 boolean，sql.Scan 都能拿到 bool。
	row := db.QueryRow("SELECT version, dirty FROM schema_migrations LIMIT 1")
	var version int64
	var dirty bool
	if err := row.Scan(&version, &dirty); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// 表存在但为空：迁移从未跑过，不是 dirty
			return false, 0, nil
		}
		return false, 0, fmt.Errorf("读取 schema_migrations 行失败: %w", err)
	}
	return dirty, version, nil
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
