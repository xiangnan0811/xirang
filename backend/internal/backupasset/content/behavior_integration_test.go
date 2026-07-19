package content

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"xirang/backend/internal/config"
	"xirang/backend/internal/database"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"

	"gorm.io/gorm"
)

func TestContentBehaviorSQLite(t *testing.T) {
	runContentBehaviorContract(t, openContentBehaviorSQLite(t))
}

func TestContentBehaviorPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		if strings.TrimSpace(os.Getenv("REQUIRE_POSTGRES_CONTENT_TEST")) == "1" {
			t.Fatal("TEST_POSTGRES_DSN is required when REQUIRE_POSTGRES_CONTENT_TEST=1")
		}
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	runContentBehaviorContract(t, openContentBehaviorPostgres(t, dsn))
}

type contentBehaviorFixture struct {
	db     *gorm.DB
	engine string
	clock  *budgetTestClock
}

func runContentBehaviorContract(t *testing.T, fixture contentBehaviorFixture) {
	t.Helper()
	seedContentBehaviorParents(t, fixture)

	t.Run("ConcurrentReservationsNeverOversellAnyScope", func(t *testing.T) {
		grant := seedContentBehaviorGrant(t, fixture, "5", 50, 20)
		limits := BudgetLimits{
			Window:   time.Minute,
			Global:   BudgetScopeLimits{WindowBytes: 50, WindowRequests: 5, MaxInFlight: 20},
			Provider: BudgetScopeLimits{WindowBytes: 50, WindowRequests: 5, MaxInFlight: 20},
			User:     BudgetScopeLimits{WindowBytes: 50, WindowRequests: 5, MaxInFlight: 20},
		}
		service := newContentBehaviorBudgetService(t, fixture, limits)
		const contenders = 12
		start := make(chan struct{})
		results := make(chan struct {
			reservation Reservation
			err         error
		}, contenders)
		var ready sync.WaitGroup
		ready.Add(contenders)
		for index := 0; index < contenders; index++ {
			go func(index int) {
				ready.Done()
				<-start
				reservation, err := service.Reserve(context.Background(), ReservationIntent{
					RequestID: fmt.Sprintf("%032x", 1_000+index), GrantID: grant.ID, Method: "GET",
					Range: HTTPRange{Kind: HTTPRangeFull, Length: 10}, ReservedBytes: 10,
				})
				results <- struct {
					reservation Reservation
					err         error
				}{reservation: reservation, err: err}
			}(index)
		}
		ready.Wait()
		close(start)

		var successes []Reservation
		for range contenders {
			result := <-results
			switch {
			case result.err == nil:
				successes = append(successes, result.reservation)
			case errors.Is(result.err, ErrBudgetExhausted):
			default:
				t.Fatalf("%s concurrent reservation error=%v", fixture.engine, result.err)
			}
		}
		if len(successes) != 5 {
			t.Fatalf("%s successful reservations=%d want=5", fixture.engine, len(successes))
		}
		for _, reservation := range successes {
			if _, err := service.Finalize(context.Background(), FinalizeIntent{
				RequestID: reservation.RequestID, ExpectedRequestVersion: reservation.RequestVersion,
				State: RequestSucceeded, HTTPStatus: 200, ProviderBytes: 10, ResponseBytes: 10, EvidenceKnown: true,
			}); err != nil {
				t.Fatalf("%s finalize concurrent reservation: %v", fixture.engine, err)
			}
		}
		assertContentBehaviorCounters(t, fixture, grant, 5, 50, 0, 0)
		assertContentBehaviorAuditCounters(t, fixture, grant, 5, 5, 0, 0)
	})

	clearContentBehaviorState(t, fixture)
	t.Run("ReplayCancelAndCrashAreIdempotentAndConservative", func(t *testing.T) {
		grant := seedContentBehaviorGrant(t, fixture, "c", 100, 4)
		service := newContentBehaviorBudgetService(t, fixture, testBudgetLimits())

		replayed, err := service.Reserve(context.Background(), ReservationIntent{
			RequestID: strings.Repeat("d", 32), GrantID: grant.ID, Method: "GET",
			Range: HTTPRange{Kind: HTTPRangeFull, Length: 10}, ReservedBytes: 10,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := service.Reserve(context.Background(), ReservationIntent{
			RequestID: replayed.RequestID, GrantID: grant.ID, Method: "GET",
			Range: HTTPRange{Kind: HTTPRangeFull, Length: 10}, ReservedBytes: 10,
		}); !errors.Is(err, ErrReservationReplay) {
			t.Fatalf("%s replay error=%v", fixture.engine, err)
		}
		first, err := service.Finalize(context.Background(), FinalizeIntent{
			RequestID: replayed.RequestID, ExpectedRequestVersion: replayed.RequestVersion,
			State: RequestSucceeded, HTTPStatus: 200, ProviderBytes: 1, ResponseBytes: 1, EvidenceKnown: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		duplicate, err := service.Finalize(context.Background(), FinalizeIntent{
			RequestID: replayed.RequestID, ExpectedRequestVersion: replayed.RequestVersion,
			State: RequestSucceeded, HTTPStatus: 200, ProviderBytes: 1, ResponseBytes: 1, EvidenceKnown: true,
		})
		if err != nil || !duplicate.AlreadyFinalized || duplicate.ChargedBytes != first.ChargedBytes {
			t.Fatalf("%s duplicate finalize=%+v err=%v", fixture.engine, duplicate, err)
		}

		canceled, err := service.Reserve(context.Background(), ReservationIntent{
			RequestID: strings.Repeat("e", 32), GrantID: grant.ID, Method: "GET",
			Range: HTTPRange{Kind: HTTPRangeFull, Length: 10}, ReservedBytes: 10,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := service.Finalize(context.Background(), FinalizeIntent{
			RequestID: canceled.RequestID, ExpectedRequestVersion: canceled.RequestVersion,
			State: RequestCanceled, HTTPStatus: 499, FailureCode: RequestFailureClientCanceled,
			ProviderBytes: 4, ResponseBytes: 2, EvidenceKnown: true,
		}); err != nil {
			t.Fatal(err)
		}

		crashed, err := service.Reserve(context.Background(), ReservationIntent{
			RequestID: strings.Repeat("f", 32), GrantID: grant.ID, Method: "GET",
			Range: HTTPRange{Kind: HTTPRangeFull, Length: 10}, ReservedBytes: 10,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := service.Finalize(context.Background(), FinalizeIntent{
			RequestID: crashed.RequestID, ExpectedRequestVersion: crashed.RequestVersion,
			State: RequestReconciled, HTTPStatus: 500, FailureCode: RequestFailureReconciledCrash,
			ProviderBytes: -1, EvidenceKnown: false,
		}); err != nil {
			t.Fatal(err)
		}
		assertContentBehaviorCounters(t, fixture, grant, 3, 15, 0, 0)
		assertContentBehaviorAuditCounters(t, fixture, grant, 3, 1, 0, 2)
	})
}

func assertContentBehaviorAuditCounters(
	t *testing.T,
	fixture contentBehaviorFixture,
	grant model.BackupAssetDeliveryGrant,
	wantRequests, wantSuccess, wantBlocked, wantFailure int64,
) {
	t.Helper()
	var stored model.BackupAssetDeliveryGrant
	if err := fixture.db.First(&stored, "id = ?", grant.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.AuditState != "pending" || stored.AuditRequestCount != wantRequests ||
		stored.AuditSuccessCount != wantSuccess || stored.AuditBlockedCount != wantBlocked ||
		stored.AuditFailureCount != wantFailure || stored.AuditRangeCount != 0 || stored.AuditRangeBytes != 0 {
		t.Fatalf("%s audit counters=%+v", fixture.engine, stored)
	}
}

func openContentBehaviorSQLite(t *testing.T) contentBehaviorFixture {
	t.Helper()
	configureContentBehaviorEnvironment(t)
	clock := &budgetTestClock{now: time.Date(2026, 7, 18, 11, 0, 0, 0, time.UTC)}
	db, err := database.Open(config.Config{DBType: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "content-behavior.db")})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.RunMigrations(db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeContentBehaviorDB(db) })
	return contentBehaviorFixture{db: db, engine: "sqlite", clock: clock}
}

func openContentBehaviorPostgres(t *testing.T, dsn string) contentBehaviorFixture {
	t.Helper()
	configureContentBehaviorEnvironment(t)
	parsed, err := url.Parse(dsn)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		t.Fatalf("TEST_POSTGRES_DSN must be a PostgreSQL URL: %v", err)
	}
	base, err := database.Open(config.Config{DBType: "postgres", PostgresDSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("xirang_content_%d", time.Now().UnixNano())
	if err := base.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	query.Set("timezone", "UTC")
	parsed.RawQuery = query.Encode()
	db, err := database.Open(config.Config{DBType: "postgres", PostgresDSN: parsed.String()})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.RunMigrations(db, "postgres"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		closeContentBehaviorDB(db)
		_ = base.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE").Error
		closeContentBehaviorDB(base)
	})
	return contentBehaviorFixture{
		db: db, engine: "postgres",
		clock: &budgetTestClock{now: time.Date(2026, 7, 18, 11, 0, 0, 0, time.UTC)},
	}
}

func configureContentBehaviorEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_CONTENT_BEHAVIOR_DATA_KEY_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)
}

func seedContentBehaviorParents(t *testing.T, fixture contentBehaviorFixture) {
	t.Helper()
	now := fixture.clock.Now()
	mustContentBehaviorExec(t, fixture, `INSERT INTO users
		(id, username, password_hash, role, totp_secret, totp_enabled, recovery_codes,
		 token_version, onboarded, created_at, updated_at)
		VALUES (?, 'content-behavior-user', 'hash', 'operator', '', ?, '', 0, ?, ?, ?)`, 42, false, true, now, now)
	mustContentBehaviorExec(t, fixture, `INSERT INTO backup_repositories
		(id, provider_kind, repository_identity, display_name, description, version_mode,
		 status, capability_revision, capabilities_json, immutability_level, created_at, updated_at)
		VALUES (?, 'rsync', 'content-behavior-repository', 'content-behavior', '', 'hardlink_tree',
		 'online', 1, '{}', 'xirang_managed', ?, ?)`, strings.Repeat("1", 32), now, now)
	mustContentBehaviorExec(t, fixture, `INSERT INTO recovery_points
		(id, repository_id, producing_task_name_snapshot, producing_node_id_snapshot, producing_node_name_snapshot,
		 lineage_json, encrypted_provider_locator, encrypted_rollback_locator, semantics, state, observed_at,
		 source_fingerprint, manifest_digest_algorithm, manifest_digest, entry_count, logical_bytes,
		 consistency_json, fidelity_json, capability_revision, capabilities_json, immutability_level,
		 physical_availability, hold_state, created_at, updated_at)
		VALUES (?, ?, '', 0, '', '{}', '', '', 'mutable_head', 'observed', ?, 'content-source-v1',
		 'sha256', '', 1, 100, '{}', '{}', 1, '{}', 'mutable', 'online', 'none', ?, ?)`,
		strings.Repeat("2", 32), strings.Repeat("1", 32), now, now, now)
	mustContentBehaviorExec(t, fixture, `INSERT INTO catalog_generations
		(id, recovery_point_id, generation, state, is_active, source_fingerprint,
		 expected_entry_count, written_entry_count, expected_digest, written_digest,
		 error_code, correlation_id, started_at, finished_at, created_at, updated_at)
		VALUES (?, ?, 1, 'complete', ?, 'content-source-v1', 1, 1, '', '', '', '', ?, ?, ?, ?)`,
		strings.Repeat("3", 32), strings.Repeat("2", 32), true, now, now, now, now)
	mustContentBehaviorExec(t, fixture, `INSERT INTO catalog_entries
		(generation_id, entry_id, recovery_point_id, normalized_path, name, entry_type,
		 size, mode, owner, mime_type, fingerprint, fingerprint_strength,
		 encrypted_provider_locator, security_state, created_at)
		VALUES (?, ?, ?, '/content/asset.bin', 'asset.bin', 'file', 100, '', '',
		 'application/octet-stream', 'entry-v1', 'strong', '', 'non_secret', ?)`,
		strings.Repeat("3", 32), strings.Repeat("4", 64), strings.Repeat("2", 32), now)
}

func seedContentBehaviorGrant(
	t *testing.T,
	fixture contentBehaviorFixture,
	marker string,
	maxCumulative, maxInFlight int64,
) model.BackupAssetDeliveryGrant {
	t.Helper()
	now := fixture.clock.Now()
	grantID := strings.Repeat(marker, 32)
	leaseID := strings.Repeat(nextHexMarker(marker), 32)
	deliveryID := strings.Repeat(nextHexMarker(nextHexMarker(marker)), 32)
	mustContentBehaviorExec(t, fixture, `INSERT INTO recovery_point_leases
		(id, recovery_point_id, holder_type, owner_id, attempt_id, fence_token, status,
		 lease_expires_at, absolute_deadline, last_heartbeat_at, created_at, updated_at)
		VALUES (?, ?, 'content_session', ?, ?, ?, 'active', ?, ?, ?, ?, ?)`,
		leaseID, strings.Repeat("2", 32), grantID, strings.Repeat("8", 32), strings.Repeat("9", 64),
		now.Add(5*time.Minute), now.Add(time.Hour), now, now, now)
	mustContentBehaviorExec(t, fixture, `INSERT INTO backup_asset_delivery_grants
		(id, delivery_id, resource_kind, recovery_point_id, catalog_generation_id, entry_id,
		 owner_user_id, session_jti, session_token_version, session_role, session_expires_at,
		 action, method_policy, range_policy, renderer, profile, classification,
		 classification_revision, classification_source_revision, provider_kind,
		 source_fingerprint, entry_fingerprint, fingerprint_strength, representation_etag,
		 source_size, source_modified_at, detected_media_type, representation_source_bytes,
		 representation_size, representation_truncated, cookie_secret_hash,
		 state, lease_id, lease_attempt_id, lease_fence_token_hash,
		 absolute_expires_at, idle_expires_at, idle_ttl_seconds, last_activity_at,
		 max_bytes_per_request, max_cumulative_bytes, max_requests, max_in_flight,
		 version, created_at, updated_at)
		VALUES (?, ?, 'backup_asset', ?, ?, ?, 42, ?, 0, 'operator', ?,
		 'preview', 'get_head', 'single', 'safe_raster', 'raster_v1', 'non_secret',
		 1, 1, 'rsync', 'content-source-v1', 'entry-v1', 'strong', '"content-etag"',
		 100, ?, 'application/octet-stream', 100, 100, ?, ?, 'active', ?, ?, ?,
		 ?, ?, 60, ?, 10, ?, 100, ?, 1, ?, ?)`,
		grantID, deliveryID, strings.Repeat("2", 32), strings.Repeat("3", 32), strings.Repeat("4", 64),
		strings.Repeat("a", 32), now.Add(time.Hour), now, false, strings.Repeat("b", 64), leaseID,
		strings.Repeat("8", 32), strings.Repeat("c", 64), now.Add(30*time.Minute), now.Add(time.Minute), now,
		maxCumulative, maxInFlight, now, now)
	return model.BackupAssetDeliveryGrant{
		ID: grantID, OwnerUserID: 42, ProviderKind: "rsync",
	}
}

func clearContentBehaviorState(t *testing.T, fixture contentBehaviorFixture) {
	t.Helper()
	for _, statement := range []string{
		"DELETE FROM backup_asset_delivery_requests",
		"DELETE FROM backup_asset_delivery_grants",
		"DELETE FROM backup_asset_delivery_usage",
		"DELETE FROM recovery_point_leases WHERE holder_type = 'content_session'",
	} {
		mustContentBehaviorExec(t, fixture, statement)
	}
}

func newContentBehaviorBudgetService(t *testing.T, fixture contentBehaviorFixture, limits BudgetLimits) *BudgetService {
	t.Helper()
	service, err := NewBudgetService(BudgetDependencies{
		DB: fixture.db, Now: fixture.clock.Now,
		Limits: func(context.Context) (BudgetLimits, error) { return limits, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func assertContentBehaviorCounters(
	t *testing.T,
	fixture contentBehaviorFixture,
	grant model.BackupAssetDeliveryGrant,
	requests, delivered, reserved, inFlight int64,
) {
	t.Helper()
	if err := fixture.db.First(&grant, "id = ?", grant.ID).Error; err != nil {
		t.Fatal(err)
	}
	if grant.RequestCount != requests || grant.DeliveredBytes != delivered ||
		grant.ReservedBytes != reserved || grant.InFlight != inFlight {
		t.Fatalf("%s grant counters=%+v", fixture.engine, grant)
	}
	for _, key := range orderedBudgetScopeKeys(grant) {
		var usage model.BackupAssetDeliveryUsage
		if err := fixture.db.First(&usage, "scope_kind = ? AND scope_id = ?", key.Kind, key.ID).Error; err != nil {
			t.Fatal(err)
		}
		if usage.RequestCount != requests || usage.DeliveredBytes != delivered ||
			usage.ReservedBytes != reserved || usage.InFlight != inFlight ||
			usage.RequestCount < 0 || usage.DeliveredBytes < 0 || usage.ReservedBytes < 0 || usage.InFlight < 0 {
			t.Fatalf("%s scope %+v counters=%+v", fixture.engine, key, usage)
		}
	}
}

func mustContentBehaviorExec(t *testing.T, fixture contentBehaviorFixture, statement string, args ...any) {
	t.Helper()
	if err := fixture.db.Exec(statement, args...).Error; err != nil {
		t.Fatalf("%s content behavior SQL failed: %v", fixture.engine, err)
	}
}

func nextHexMarker(marker string) string {
	const digits = "0123456789abcdef"
	index := strings.Index(digits, marker)
	if index < 0 || index == len(digits)-1 {
		return "0"
	}
	return digits[index+1 : index+2]
}

func closeContentBehaviorDB(db *gorm.DB) {
	if db == nil {
		return
	}
	if sqlDB, err := db.DB(); err == nil {
		_ = sqlDB.Close()
	}
}
