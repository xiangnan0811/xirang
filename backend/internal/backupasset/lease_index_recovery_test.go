package backupasset

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"xirang/backend/internal/model"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestIndexLeaseReclamationPostgres(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		if os.Getenv("REQUIRE_POSTGRES_LEASE_TEST") == "1" {
			t.Fatal("TEST_POSTGRES_DSN required")
		}
		t.Skip("TEST_POSTGRES_DSN not configured")
	}
	parsed, err := url.Parse(dsn)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		t.Fatal("TEST_POSTGRES_DSN must be a PostgreSQL URL")
	}
	base, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal("open PostgreSQL fixture failed")
	}
	schema := fmt.Sprintf("xirang_index_lease_%d", time.Now().UnixNano())
	if err := base.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := base.Exec("DROP SCHEMA " + schema + " CASCADE").Error; err != nil {
			t.Error(err)
		}
		if sqlDB, err := base.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	db, err := gorm.Open(postgres.Open(parsed.String()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal("open scoped PostgreSQL fixture failed")
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	if err := db.AutoMigrate(&model.RecoveryPointLease{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE UNIQUE INDEX lease_active_owner ON recovery_point_leases(recovery_point_id, holder_type, owner_id) WHERE status = 'active'`).Error; err != nil {
		t.Fatal(err)
	}
	for _, holder := range []LeaseHolderType{LeaseHolderCatalogBuild, LeaseHolderSearchIndex} {
		t.Run(string(holder), func(t *testing.T) {
			ctx := context.Background()
			clock := &leaseTestClock{now: time.Date(2026, 9, 16, 0, 0, 0, 0, time.UTC)}
			service, err := NewLeaseService(db, clock.Now, standardLeaseConfig())
			if err != nil {
				t.Fatal(err)
			}
			request := standardAcquireLeaseRequest()
			request.HolderType = holder
			old, err := service.Acquire(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			clock.Advance(5 * time.Minute)
			injected := errors.New("caller rollback")
			if err := db.Transaction(func(tx *gorm.DB) error {
				if _, err := service.AcquireTx(ctx, tx, request); err != nil {
					return err
				}
				return injected
			}); !errors.Is(err, injected) {
				t.Fatalf("rollback: %v", err)
			}
			var row model.RecoveryPointLease
			if err := db.First(&row, "id = ?", old.ID).Error; err != nil {
				t.Fatal(err)
			}
			if row.Status != string(LeaseActive) {
				t.Fatal("rollback expired original slot")
			}
			start := make(chan struct{})
			results := make(chan error, 8)
			var group sync.WaitGroup
			for range 8 {
				group.Go(func() { <-start; _, err := service.Acquire(ctx, request); results <- err })
			}
			close(start)
			group.Wait()
			close(results)
			winners := 0
			for err := range results {
				if err == nil {
					winners++
				} else if !errors.Is(err, ErrLeaseHeld) {
					t.Fatalf("unexpected competing acquisition error: %v", err)
				}
			}
			if winners != 1 {
				t.Fatalf("concurrent winners=%d", winners)
			}
			if err := service.ValidateFence(ctx, old.Fence); !errors.Is(err, ErrLeaseFenceLost) {
				t.Fatalf("old fence: %v", err)
			}
			var count int64
			if err := db.Model(&model.RecoveryPointLease{}).Where("holder_type = ? AND status = ?", holder, LeaseActive).Count(&count).Error; err != nil {
				t.Fatal(err)
			}
			if count != 1 {
				t.Fatalf("active slots=%d", count)
			}
		})
		t.Run(string(holder)+"/renewal wins blocked reclamation", func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			clock := &leaseTestClock{now: time.Date(2026, 9, 16, 0, 0, 0, 0, time.UTC)}
			service, err := NewLeaseService(db, clock.Now, standardLeaseConfig())
			if err != nil {
				t.Fatal(err)
			}
			request := standardAcquireLeaseRequest()
			request.HolderType, request.OwnerID = holder, "renewal-winner"
			old, err := service.Acquire(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			clock.Advance(4 * time.Minute)
			renewTx := db.WithContext(ctx).Begin()
			if renewTx.Error != nil {
				t.Fatal(renewTx.Error)
			}
			defer renewTx.Rollback()
			var renewPID int
			if err := renewTx.Raw("SELECT pg_backend_pid()").Scan(&renewPID).Error; err != nil {
				t.Fatal(err)
			}
			if _, err := service.RenewTx(ctx, renewTx, old.Fence); err != nil {
				t.Fatal(err)
			}
			// The contender sees the old expired heartbeat until the earlier
			// renewal commits. Its UPDATE must recheck the renewed row afterward.
			clock.Advance(2 * time.Minute)
			pid := make(chan int, 1)
			result := make(chan error, 1)
			go func() {
				result <- db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
					var contenderPID int
					if err := tx.Raw("SELECT pg_backend_pid()").Scan(&contenderPID).Error; err != nil {
						return err
					}
					pid <- contenderPID
					_, err := service.AcquireTx(ctx, tx, request)
					return err
				})
			}()
			var contenderPID int
			select {
			case contenderPID = <-pid:
			case <-ctx.Done():
				t.Fatal("contender did not begin")
			}
			for {
				var blocked bool
				if err := base.WithContext(ctx).Raw("SELECT ? = ANY(pg_blocking_pids(?))", renewPID, contenderPID).Scan(&blocked).Error; err != nil {
					t.Fatal(err)
				}
				if blocked {
					break
				}
				select {
				case err := <-result:
					t.Fatalf("reclamation did not wait on renewal: %v", err)
				case <-ctx.Done():
					t.Fatal("no database lock overlap observed")
				case <-time.After(time.Millisecond):
				}
			}
			if err := renewTx.Commit().Error; err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-result:
				if !errors.Is(err, ErrLeaseHeld) {
					t.Fatalf("renewed lease reclaimed: %v", err)
				}
			case <-ctx.Done():
				t.Fatal("reclamation did not finish")
			}
			if err := service.ValidateFence(ctx, old.Fence); err != nil {
				t.Fatalf("renewed owner lost its fence: %v", err)
			}
		})
	}
}

type rejectIndexLeaseAdmission struct{ err error }

func (admission rejectIndexLeaseAdmission) ValidateLeaseAdmissionTx(context.Context, *gorm.DB, AcquireLeaseRequest) error {
	return admission.err
}

func TestIndexLeaseReclamationAdmissionFailureDoesNotMutate(t *testing.T) {
	for _, holder := range []LeaseHolderType{LeaseHolderCatalogBuild, LeaseHolderSearchIndex} {
		t.Run(string(holder), func(t *testing.T) {
			service, clock, db := newLeaseTestHarness(t, standardLeaseConfig())
			request := standardAcquireLeaseRequest()
			request.HolderType = holder
			old, err := service.Acquire(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			clock.Advance(5 * time.Minute)
			denied := errors.New("lifecycle admission denied")
			service.SetLifecycleLeaseAdmission(rejectIndexLeaseAdmission{err: denied})
			// Commit the caller transaction despite refusal to prove admission
			// precedes any reclamation, rather than relying on rollback to hide it.
			if err := db.Transaction(func(tx *gorm.DB) error {
				_, err := service.AcquireTx(context.Background(), tx, request)
				if !errors.Is(err, denied) {
					t.Fatalf("admission result: %v", err)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			var rows []model.RecoveryPointLease
			if err := db.Find(&rows).Error; err != nil {
				t.Fatal(err)
			}
			if len(rows) != 1 || rows[0].ID != old.ID || rows[0].Status != string(LeaseActive) {
				t.Fatal("rejected admission reclaimed the old slot")
			}
		})
	}
}

func TestIndexLeaseAcquireRecoversExpiredOwnerAfterRestart(t *testing.T) {
	for _, holder := range []LeaseHolderType{LeaseHolderCatalogBuild, LeaseHolderSearchIndex} {
		for _, elapsed := range []time.Duration{5 * time.Minute, 2 * time.Hour} {
			t.Run(string(holder)+"/"+elapsed.String(), func(t *testing.T) {
				ctx := context.Background()
				service, clock, db := newLeaseTestHarness(t, standardLeaseConfig())
				request := standardAcquireLeaseRequest()
				request.HolderType = holder
				old, err := service.Acquire(ctx, request)
				if err != nil {
					t.Fatal(err)
				}
				otherRequest := request
				otherRequest.OwnerID = "other-owner"
				other, err := service.Acquire(ctx, otherRequest)
				if err != nil {
					t.Fatal(err)
				}
				clock.Advance(elapsed)
				restarted, err := NewLeaseService(db, clock.Now, standardLeaseConfig())
				if err != nil {
					t.Fatal(err)
				}
				fresh, err := restarted.Acquire(ctx, request)
				if err != nil {
					t.Fatalf("restart must recover expired index owner: %v", err)
				}
				if fresh.ID == old.ID || fresh.Fence.AttemptID == old.Fence.AttemptID || fresh.Fence.FenceToken == old.Fence.FenceToken {
					t.Fatal("restart reused abandoned attempt identity")
				}
				var stored, untouched model.RecoveryPointLease
				if err := db.First(&stored, "id = ?", old.ID).Error; err != nil {
					t.Fatal(err)
				}
				if err := db.First(&untouched, "id = ?", other.ID).Error; err != nil {
					t.Fatal(err)
				}
				if stored.Status != string(LeaseExpired) || untouched.Status != string(LeaseActive) {
					t.Fatal("reclamation escaped the exact owner slot")
				}
				if _, err := restarted.Acquire(ctx, request); !errors.Is(err, ErrLeaseHeld) {
					t.Fatalf("live owner accepted: %v", err)
				}
				if _, err := restarted.Renew(ctx, old.Fence); err == nil {
					t.Fatal("old fence renewed")
				}
				if err := restarted.Release(ctx, old.Fence); err == nil {
					t.Fatal("old fence released")
				}
				if err := restarted.ValidateFence(ctx, old.Fence); err == nil {
					t.Fatal("old fence remains valid")
				}
				if err := restarted.ValidateFence(ctx, fresh.Fence); err != nil {
					t.Fatalf("old operation damaged new fence: %v", err)
				}
			})
		}
	}
}

func TestIndexLeaseReclamationRollsBackWithCaller(t *testing.T) {
	service, clock, db := newLeaseTestHarness(t, standardLeaseConfig())
	ctx := context.Background()
	request := standardAcquireLeaseRequest()
	old, err := service.Acquire(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(5 * time.Minute)
	injected := errors.New("injected caller failure")
	err = db.Transaction(func(tx *gorm.DB) error {
		if _, err := service.AcquireTx(ctx, tx, request); err != nil {
			return err
		}
		return injected
	})
	if !errors.Is(err, injected) {
		t.Fatalf("caller did not reach rollback: %v", err)
	}
	var rows []model.RecoveryPointLease
	if err := db.Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != old.ID || rows[0].Status != string(LeaseActive) {
		t.Fatal("rollback changed old lease or retained replacement")
	}
}

func TestIndexLeaseReclamationPreservesExplicitDeadlineAndOtherHolders(t *testing.T) {
	ctx := context.Background()
	service, clock, db := newLeaseTestHarness(t, standardLeaseConfig())
	request := standardAcquireLeaseRequest()
	request.AbsoluteDeadline = clock.Now().Add(time.Hour)
	old, err := service.Acquire(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(5 * time.Minute)
	changed := request
	changed.AbsoluteDeadline = request.AbsoluteDeadline.Add(time.Hour)
	if _, err := service.Acquire(ctx, changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed deadline accepted: %v", err)
	}
	var row model.RecoveryPointLease
	if err := db.First(&row, "id = ?", old.ID).Error; err != nil {
		t.Fatal(err)
	}
	if row.Status != string(LeaseActive) {
		t.Fatal("rejected admission mutated old lease")
	}
	fresh, err := service.Acquire(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if !fresh.AbsoluteDeadline.Equal(old.AbsoluteDeadline) {
		t.Fatal("explicit deadline extended")
	}
	clock.Advance(time.Hour)
	if _, err := service.Acquire(ctx, request); !errors.Is(err, ErrLeaseDeadlineExceeded) {
		t.Fatalf("expired explicit deadline accepted: %v", err)
	}
	for holder := range validLeaseHolderTypes {
		if holder == LeaseHolderCatalogBuild || holder == LeaseHolderSearchIndex {
			continue
		}
		request := standardAcquireLeaseRequest()
		request.HolderType = holder
		lease, err := service.Acquire(ctx, request)
		if err != nil {
			t.Fatal(err)
		}
		clock.Advance(5 * time.Minute)
		if _, err := service.Acquire(ctx, request); !errors.Is(err, ErrLeaseHeld) {
			t.Fatalf("%s unexpectedly reclaimed: %v", holder, err)
		}
		if _, err := service.Takeover(ctx, TakeoverLeaseRequest{LeaseID: lease.ID, OwnerID: lease.OwnerID}); err != nil {
			t.Fatalf("%s takeover broken: %v", holder, err)
		}
	}
}
