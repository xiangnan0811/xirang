package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/ssh"
	"gorm.io/gorm"
)

func newMigratedNodeProbeFixture(t *testing.T, engine string) (*gorm.DB, model.Node, model.SSHKey) {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	db := openMigratedRetryTransactionDB(t, engine)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(4)
	key := model.SSHKey{Name: "probe-key", Username: "probe", KeyType: "ed25519", PrivateKey: buildSSHKeyPrivateKeyForHandlerTestType(t, "ed25519"), Fingerprint: "probe-fingerprint"}
	if err := db.Create(&key).Error; err != nil {
		t.Fatal(err)
	}
	node := model.Node{Name: "probe-node", Host: "127.0.0.1", Port: 22, Username: "probe", AuthType: "key", SSHKeyID: &key.ID, BackupDir: "probe-node", Status: "warning"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	return db, node, key
}

func TestNodeProbeLatestAdmissionAndIdentitySQLitePostgres(t *testing.T) {
	retryTransactionForEachEngine(t, func(t *testing.T, engine string) {
		db, node, key := newMigratedNodeProbeFixture(t, engine)
		ctx := context.Background()
		first, err := admitNodeProbe(ctx, db, node.ID)
		if err != nil || first.authErr != nil {
			t.Fatalf("first admission: %v / %v", err, first.authErr)
		}
		var before model.SSHKey
		if err := db.First(&before, key.ID).Error; err != nil {
			t.Fatal(err)
		}
		if before.LastUsedAt != nil {
			t.Fatal("admission wrote last_used_at")
		}
		second, err := admitNodeProbe(ctx, db.Session(&gorm.Session{NewDB: true}), node.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := applyNodeProbe(ctx, db, first, "online", 20, time.Now()); !errors.Is(err, errNodeProbeStale) {
			t.Fatalf("older admitted probe: %v", err)
		}
		at := time.Now().UTC().Truncate(time.Microsecond)
		if used, err := applyNodeProbe(ctx, db, second, "online", 10, at); err != nil || !used {
			t.Fatalf("latest outcome: used=%v err=%v", used, err)
		}
		_, token, err := readNodeWithProbeToken(ctx, db, node.ID)
		if err != nil || token != second.token || len(token) != 64 {
			t.Fatalf("GET token=%q err=%v", token, err)
		}
		if err := db.First(&before, key.ID).Error; err != nil || before.LastUsedAt == nil {
			t.Fatalf("last used was not committed: %+v %v", before, err)
		}

		third, err := admitNodeProbe(ctx, db, node.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, token, err := readNodeWithProbeToken(ctx, db, node.ID); err != nil || token != "" {
			t.Fatalf("new admission must hide prior token: %q %v", token, err)
		}
		if err := db.Model(&model.SSHKey{}).Where("id = ?", key.ID).UpdateColumn("disabled", true).Error; err != nil {
			t.Fatal(err)
		}
		if _, err := applyNodeProbe(ctx, db, third, "offline", 0, time.Now()); !errors.Is(err, errNodeProbeStale) {
			t.Fatalf("key edit must stale: %v", err)
		}
		if err := db.Model(&model.SSHKey{}).Where("id = ?", key.ID).UpdateColumn("disabled", false).Error; err != nil {
			t.Fatal(err)
		}
		fourth, err := admitNodeProbe(ctx, db, node.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Model(&model.Node{}).Where("id = ?", node.ID).UpdateColumn("host", "127.0.0.2").Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Model(&model.Node{}).Where("id = ?", node.ID).UpdateColumn("host", "127.0.0.1").Error; err != nil {
			t.Fatal(err)
		}
		if _, err := applyNodeProbe(ctx, db, fourth, "offline", 0, time.Now()); !errors.Is(err, errNodeProbeStale) {
			t.Fatalf("node ABA must stale: %v", err)
		}
		fifth, err := admitNodeProbe(ctx, db, node.ID)
		if err != nil {
			t.Fatal(err)
		}
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := applyNodeProbe(cancelled, db, fifth, "offline", 0, time.Now()); err == nil {
			t.Fatal("cancelled probe committed")
		}
		var persisted model.Node
		if err := db.First(&persisted, node.ID).Error; err != nil {
			t.Fatal(err)
		}
		if persisted.Status != "online" || persisted.ConnectionLatency != 10 || persisted.LastSeenAt == nil {
			t.Fatalf("stale or cancelled probes changed health: %+v", persisted)
		}
		if err := db.Delete(&model.Node{}, node.ID).Error; err != nil {
			t.Fatal(err)
		}
		if _, err := applyNodeProbe(ctx, db, fifth, "offline", 0, time.Now()); !errors.Is(err, errNodeProbeStale) {
			t.Fatalf("deleted node outcome: %v", err)
		}
		if _, err := admitNodeProbe(ctx, db, node.ID); !errors.Is(err, gorm.ErrRecordNotFound) {
			t.Fatalf("deleted node admission: %v", err)
		}
	})
}

func TestNodeProbeGuardSerializesCredentialChangeSQLitePostgres(t *testing.T) {
	retryTransactionForEachEngine(t, func(t *testing.T, engine string) {
		db, node, key := newMigratedNodeProbeFixture(t, engine)
		ctx := context.Background()
		snap, err := admitNodeProbe(ctx, db, node.ID)
		if err != nil {
			t.Fatal(err)
		}
		entered, release := make(chan struct{}), make(chan struct{})
		var releaseOnce sync.Once
		unblock := func() { releaseOnce.Do(func() { close(release) }) }
		defer unblock()
		guardDone := make(chan error, 1)
		go func() {
			guardDone <- guardNodeProbe(ctx, db, snap, false, func() error { return nil }, func() error { close(entered); <-release; return nil })
		}()
		select {
		case <-entered:
		case <-time.After(3 * time.Second):
			t.Fatal("guard did not acquire identity locks")
		}
		mutationDone := make(chan error, 1)
		go func() {
			mutationDone <- db.Model(&model.SSHKey{}).Where("id = ?", key.ID).UpdateColumn("disabled", true).Error
		}()
		select {
		case err := <-mutationDone:
			t.Fatalf("credential update passed guard: %v", err)
		case <-time.After(150 * time.Millisecond):
		}
		unblock()
		if err := <-guardDone; err != nil {
			t.Fatal(err)
		}
		if err := <-mutationDone; err != nil {
			t.Fatal(err)
		}
		called := false
		if err := guardNodeProbe(ctx, db, snap, false, func() error { return nil }, func() error { called = true; return nil }); !errors.Is(err, errNodeProbeStale) || called {
			t.Fatalf("stale guard ran write: called=%v err=%v", called, err)
		}
	})
}

func TestNodeProbeExpiryChangesToken(t *testing.T) {
	expires := time.Now().Add(time.Minute)
	keyID := uint(7)
	node := model.Node{ID: 9, AuthType: "key", SSHKeyID: &keyID, ProbeGeneration: "node", SSHKey: &model.SSHKey{ID: keyID, ProbeGeneration: "key", ExpiresAt: &expires}}
	before := probeIdentityToken(node, 3, expires.Add(-time.Nanosecond))
	after := probeIdentityToken(node, 3, expires)
	if before == after || len(before) != 64 {
		t.Fatalf("expiry must invalidate token: %q %q", before, after)
	}
	if after != probeIdentityToken(node, 3, expires.Add(time.Hour)) {
		t.Fatal("expired state must remain stable")
	}
}

func TestNodeProbeSharedKeyConcurrentOutcomesSQLitePostgres(t *testing.T) {
	retryTransactionForEachEngine(t, func(t *testing.T, engine string) {
		db, node, key := newMigratedNodeProbeFixture(t, engine)
		other := model.Node{Name: "probe-node-two", Host: "127.0.0.1", Port: 22, Username: "probe", AuthType: "key", SSHKeyID: &key.ID, BackupDir: "probe-node-two"}
		if err := db.Create(&other).Error; err != nil {
			t.Fatal(err)
		}
		a, err := admitNodeProbe(context.Background(), db, node.ID)
		if err != nil {
			t.Fatal(err)
		}
		b, err := admitNodeProbe(context.Background(), db, other.ID)
		if err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		results := make(chan error, 2)
		for _, snap := range []nodeProbeSnapshot{a, b} {
			go func(s nodeProbeSnapshot) {
				<-start
				_, err := applyNodeProbe(context.Background(), db, s, "online", 1, time.Now())
				results <- err
			}(snap)
		}
		close(start)
		for range 2 {
			select {
			case err := <-results:
				if err != nil {
					t.Fatal(fmt.Errorf("shared key outcome: %w", err))
				}
			case <-time.After(5 * time.Second):
				t.Fatal("shared key outcome deadlocked")
			}
		}
	})
}

func TestNodeProbeHandlersReverseOrderAndCancellationSQLitePostgres(t *testing.T) {
	retryTransactionForEachEngine(t, func(t *testing.T, engine string) {
		server := startDockerSSHTestServer(t, nil)
		db, _, _ := newMigratedNodeProbeFixture(t, engine)
		node := seedDockerSSHHandlerNode(t, db, server)
		entered := make(chan int32, 2)
		releaseFirst, releaseThird := make(chan struct{}), make(chan struct{})
		var firstOnce, thirdOnce sync.Once
		unblockFirst := func() { firstOnce.Do(func() { close(releaseFirst) }) }
		unblockThird := func() { thirdOnce.Do(func() { close(releaseThird) }) }
		defer unblockFirst()
		defer unblockThird()
		var attempts atomic.Int32
		server.config.PasswordCallback = func(metadata ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
			n := attempts.Add(1)
			if n == 1 || n == 3 {
				entered <- n
				if n == 1 {
					<-releaseFirst
				} else {
					<-releaseThird
				}
			}
			if metadata.User() != "docker-test" || string(password) != server.password {
				return nil, errors.New("invalid credentials")
			}
			return nil, nil
		}
		invoke := func(ctx context.Context, h *NodeHandler) <-chan *httptest.ResponseRecorder {
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				w := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(w)
				c.Params = gin.Params{{Key: "id", Value: strconv.FormatUint(uint64(node.ID), 10)}}
				c.Request = httptest.NewRequest(http.MethodPost, "/", nil).WithContext(ctx)
				h.TestConnection(c)
				done <- w
			}()
			return done
		}
		first := invoke(context.Background(), NewNodeHandler(db, nil, nil))
		select {
		case n := <-entered:
			if n != 1 {
				t.Fatalf("unexpected admission %d", n)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("first SSH authentication did not start")
		}
		second := <-invoke(context.Background(), NewNodeHandler(db.Session(&gorm.Session{NewDB: true}), nil, nil))
		var secondBody struct {
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal(second.Body.Bytes(), &secondBody); err != nil || second.Code != http.StatusOK || secondBody.Data["ok"] != true {
			t.Fatalf("second outcome: %s %v", second.Body.String(), err)
		}
		unblockFirst()
		old := <-first
		var oldBody struct {
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal(old.Body.Bytes(), &oldBody); err != nil || oldBody.Data["stale"] != true || len(oldBody.Data) != 3 {
			t.Fatalf("old outcome: %s %v", old.Body.String(), err)
		}
		var previous model.Node
		if err := db.First(&previous, node.ID).Error; err != nil {
			t.Fatal(err)
		}
		var auditCount int64
		if err := db.Model(&model.CredentialAuditEvent{}).Where("action = ? AND node_id = ?", "node.credential.test_connection", node.ID).Count(&auditCount).Error; err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		third := invoke(ctx, NewNodeHandler(db, nil, nil))
		select {
		case n := <-entered:
			if n != 3 {
				t.Fatalf("unexpected admission %d", n)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("cancelled SSH authentication did not start")
		}
		cancel()
		select {
		case <-third:
		case <-time.After(3 * time.Second):
			t.Fatal("cancellation did not stop probe")
		}
		unblockThird()
		var after model.Node
		if err := db.First(&after, node.ID).Error; err != nil {
			t.Fatal(err)
		}
		var afterAudit int64
		if err := db.Model(&model.CredentialAuditEvent{}).Where("action = ? AND node_id = ?", "node.credential.test_connection", node.ID).Count(&afterAudit).Error; err != nil {
			t.Fatal(err)
		}
		if previous.LastSeenAt == nil || after.LastSeenAt == nil || after.Status != previous.Status || after.ConnectionLatency != previous.ConnectionLatency || !after.LastSeenAt.Equal(*previous.LastSeenAt) || auditCount != afterAudit {
			t.Fatalf("cancel changed health or audit: before=%+v after=%+v audits=%d/%d", previous, after, auditCount, afterAudit)
		}
	})
}

func TestNodeProbeSharedKeyLastUsedNeverMovesBackwardsSQLitePostgres(t *testing.T) {
	retryTransactionForEachEngine(t, func(t *testing.T, engine string) {
		db, node, key := newMigratedNodeProbeFixture(t, engine)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		check := func(db *gorm.DB) error {
			if engine == "postgres" {
				// Keep the pinned connection and start each GORM operation with a clean statement.
				db = db.Session(&gorm.Session{NewDB: true})
				if err := db.Exec("SET TIME ZONE 'Pacific/Honolulu'").Error; err != nil {
					return err
				}
			}
			snap, err := admitNodeProbe(ctx, db, node.ID)
			if err != nil {
				t.Fatal(err)
			}
			newer := time.Now().UTC().Truncate(time.Microsecond)
			if err := db.Model(&model.SSHKey{}).Where("id = ?", key.ID).UpdateColumn("last_used_at", newer).Error; err != nil {
				t.Fatal(err)
			}
			local := time.FixedZone("east", 8*60*60)
			used, err := applyNodeProbe(ctx, db, snap, "online", 1, newer.Add(-time.Minute).In(local))
			if err != nil || used {
				t.Fatalf("older outcome: updated=%v err=%v", used, err)
			}
			var stored model.SSHKey
			if err := db.First(&stored, key.ID).Error; err != nil || stored.LastUsedAt == nil || !stored.LastUsedAt.Equal(newer) {
				t.Fatalf("last used moved backwards: %v %v", stored.LastUsedAt, err)
			}
			latest := newer.Add(time.Minute)
			used, err = applyNodeProbe(ctx, db, snap, "online", 1, latest.In(local))
			if err != nil || !used {
				t.Fatalf("newer outcome: updated=%v err=%v", used, err)
			}
			if err := db.First(&stored, key.ID).Error; err != nil || stored.LastUsedAt == nil || !stored.LastUsedAt.Equal(latest) {
				t.Fatalf("last used did not advance to UTC instant: %v %v", stored.LastUsedAt, err)
			}
			return nil
		}
		db = db.WithContext(ctx)
		var err error
		if engine == "postgres" {
			err = db.Connection(check)
		} else {
			err = check(db)
		}
		if err != nil {
			t.Fatal(err)
		}
	})
}

func TestNodeProbeSQLiteLastUsedComparesInstantsAcrossOffsets(t *testing.T) {
	db, node, key := newMigratedNodeProbeFixture(t, "sqlite")
	snap, err := admitNodeProbe(context.Background(), db, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	olderLocal := time.Date(2026, 1, 1, 15, 0, 0, 0, time.FixedZone("east", 8*60*60))
	newerUTC := time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name    string
		stored  string
		input   time.Time
		want    time.Time
		updated bool
	}{
		{"newer UTC rejects older local", "2026-01-01 08:00:00+00:00", olderLocal, newerUTC, false},
		{"newer west offset rejects older local", "2026-01-01 01:00:00-07:00", olderLocal, newerUTC, false},
		{"older east offset accepts newer UTC", "2026-01-01 15:00:00+08:00", newerUTC, newerUTC, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := db.Exec("UPDATE ssh_keys SET last_used_at = ? WHERE id = ?", tc.stored, key.ID).Error; err != nil {
				t.Fatal(err)
			}
			used, err := applyNodeProbe(context.Background(), db, snap, "online", 1, tc.input)
			if err != nil || used != tc.updated {
				t.Fatalf("updated=%v want=%v err=%v", used, tc.updated, err)
			}
			var stored model.SSHKey
			if err := db.First(&stored, key.ID).Error; err != nil || stored.LastUsedAt == nil || !stored.LastUsedAt.Equal(tc.want) {
				t.Fatalf("last used=%v want=%v err=%v", stored.LastUsedAt, tc.want, err)
			}
		})
	}
}

func TestNodeProbeDetailReadsWhileWriterHoldsLocksSQLitePostgres(t *testing.T) {
	retryTransactionForEachEngine(t, func(t *testing.T, engine string) {
		db, node, key := newMigratedNodeProbeFixture(t, engine)
		if err := db.Model(&model.Node{}).Where("id = ?", node.ID).UpdateColumn("auth_type", "password").Error; err != nil {
			t.Fatal(err)
		}
		tx := db.Begin()
		if tx.Error != nil {
			t.Fatal(tx.Error)
		}
		defer tx.Rollback()
		if err := tx.Exec("UPDATE nodes SET name = name WHERE id = ?", node.ID).Error; err != nil {
			t.Fatal(err)
		}
		if err := tx.Exec("UPDATE ssh_keys SET name = name WHERE id = ?", key.ID).Error; err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		got, _, err := readNodeWithProbeToken(ctx, db, node.ID)
		if err != nil || got.SSHKey == nil || got.SSHKey.ID != key.ID || got.SSHKey.PrivateKey != "" || got.Password != "" || got.PrivateKey != "" {
			t.Fatalf("detail under writer lock: node=%+v key=%+v err=%v", got, got.SSHKey, err)
		}
	})
}

func TestNodeProbeFencingAndTrustIgnoreUnrelatedCiphertextSQLitePostgres(t *testing.T) {
	retryTransactionForEachEngine(t, func(t *testing.T, engine string) {
		db, node, key := newMigratedNodeProbeFixture(t, engine)
		snap, err := admitNodeProbe(context.Background(), db, node.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Exec("UPDATE nodes SET password = ? WHERE id = ?", "enc:v2:invalid", node.ID).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Exec("UPDATE ssh_keys SET private_key = ? WHERE id = ?", "enc:v2:invalid", key.ID).Error; err != nil {
			t.Fatal(err)
		}
		if err := nodeProbeTransaction(context.Background(), db, func(tx *gorm.DB) error {
			current, err := lockProbeIdentity(tx, node.ID, false, true)
			if err != nil {
				return err
			}
			snap.token = probeIdentityToken(current, snap.seq, time.Now())
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := applyNodeProbe(context.Background(), db, snap, "offline", 0, time.Now()); err != nil {
			t.Fatalf("metadata-only outcome: %v", err)
		}
		for _, token := range []string{"", snap.token} {
			trust, err := trustProbeSnapshot(context.Background(), db, node.ID, token)
			if err != nil {
				t.Fatalf("trust snapshot token=%q: %v", token, err)
			}
			if token == "" {
				err = guardLegacyTrust(context.Background(), db, trust, func() error { return nil }, func() error { return nil })
			} else {
				err = guardNodeProbe(context.Background(), db, trust, true, func() error { return nil }, func() error { return nil })
			}
			if err != nil {
				t.Fatalf("trust guard token=%q: %v", token, err)
			}
		}
		got, token, err := readNodeWithProbeToken(context.Background(), db, node.ID)
		if err != nil || token != snap.token || got.SSHKey == nil || got.SSHKey.PrivateKey != "" {
			t.Fatalf("sanitized detail token=%q key=%+v err=%v", token, got.SSHKey, err)
		}
	})
}

func TestNodeProbeStalledHandshakeTimesOutAndAppliesFailure(t *testing.T) {
	db, node, _ := newMigratedNodeProbeFixture(t, "sqlite")
	t.Setenv("SSH_STRICT_HOST_KEY_CHECKING", "false")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := listener.Close(); err != nil {
			t.Error(err)
		}
	}()
	address := listener.Addr().(*net.TCPAddr)
	if err := db.Model(&model.Node{}).Where("id = ?", node.ID).UpdateColumns(map[string]any{"host": "127.0.0.1", "port": address.Port}).Error; err != nil {
		t.Fatal(err)
	}
	accepted := make(chan struct{})
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			close(accepted)
			_, _ = io.Copy(io.Discard, conn)
			_ = conn.Close()
		}
	}()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "id", Value: strconv.FormatUint(uint64(node.ID), 10)}}
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
	done := make(chan struct{})
	go func() { NewNodeHandler(db, nil, nil).TestConnection(c); close(done) }()
	select {
	case <-accepted:
	case <-time.After(3 * time.Second):
		t.Fatal("SSH server did not accept probe")
	}
	select {
	case <-done:
	case <-time.After(7 * time.Second):
		t.Fatal("stalled SSH handshake did not time out")
	}
	var body struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != http.StatusOK || body.Data["ok"] != false || body.Data["probe_token"] == "" {
		t.Fatalf("timeout response: %s %v", w.Body.String(), err)
	}
	var stored model.Node
	if err := db.First(&stored, node.ID).Error; err != nil || stored.Status != "offline" {
		t.Fatalf("timeout did not apply failure: status=%q err=%v", stored.Status, err)
	}
}

func TestLegacyTrustDoesNotLockBoundKeyPostgres(t *testing.T) {
	db, node, key := newMigratedNodeProbeFixture(t, "postgres")
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	if err := tx.Exec("UPDATE ssh_keys SET name = name WHERE id = ?", key.ID).Error; err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	snap, err := trustProbeSnapshot(ctx, db, node.ID, "")
	if err != nil {
		t.Fatalf("tokenless trust waited on key: %v", err)
	}
	if err := guardLegacyTrust(ctx, db, snap, func() error { return nil }, func() error { return nil }); err != nil {
		t.Fatalf("tokenless trust guard waited on key: %v", err)
	}
}

func TestNodeProbeWriteFenceSurvivesCancellationSQLitePostgres(t *testing.T) {
	retryTransactionForEachEngine(t, func(t *testing.T, engine string) {
		db, node, _ := newMigratedNodeProbeFixture(t, engine)
		for _, legacy := range []bool{false, true} {
			var snap nodeProbeSnapshot
			var err error
			if legacy {
				snap, err = trustProbeSnapshot(context.Background(), db, node.ID, "")
			} else {
				snap, err = admitNodeProbe(context.Background(), db, node.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			done := make(chan error, 1)
			go func() {
				write := func() error { close(entered); <-release; return nil }
				if legacy {
					done <- guardLegacyTrust(ctx, db, snap, func() error { return nil }, write)
				} else {
					done <- guardNodeProbe(ctx, db, snap, false, func() error { return nil }, write)
				}
			}()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				cancel()
				t.Fatal("guard did not authorize write")
			}
			cancel()
			mutation := make(chan error, 1)
			go func() { mutation <- db.Exec("UPDATE nodes SET host = host WHERE id = ?", node.ID).Error }()
			select {
			case err := <-mutation:
				t.Fatalf("node fence released during write (legacy=%v): %v", legacy, err)
			case <-time.After(150 * time.Millisecond):
			}
			unblock()
			if err := <-done; err != nil {
				t.Fatalf("authorized write guard (legacy=%v): %v", legacy, err)
			}
			if err := <-mutation; err != nil {
				t.Fatal(err)
			}
		}
	})
}

func TestNodeProbeWriteGuardRechecksAuthorityAfterPrepare(t *testing.T) {
	retryTransactionForEachEngine(t, func(t *testing.T, engine string) {
		db, node, key := newMigratedNodeProbeFixture(t, engine)
		for _, legacy := range []bool{false, true} {
			var snap nodeProbeSnapshot
			var err error
			if legacy {
				snap, err = trustProbeSnapshot(context.Background(), db, node.ID, "")
			} else {
				snap, err = admitNodeProbe(context.Background(), db, node.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			wrote := false
			prepare := func() error { cancel(); return nil }
			write := func() error { wrote = true; return nil }
			if legacy {
				err = guardLegacyTrust(ctx, db, snap, prepare, write)
			} else {
				err = guardNodeProbe(ctx, db, snap, false, prepare, write)
			}
			if !errors.Is(err, context.Canceled) || wrote {
				t.Fatalf("cancellation during preparation (legacy=%v): wrote=%v err=%v", legacy, wrote, err)
			}
		}

		expires := time.Now().Add(2 * time.Second)
		if err := db.Model(&model.SSHKey{}).Where("id = ?", key.ID).UpdateColumn("expires_at", expires).Error; err != nil {
			t.Fatal(err)
		}
		snap, err := admitNodeProbe(context.Background(), db, node.ID)
		if err != nil {
			t.Fatal(err)
		}
		wrote := false
		err = guardNodeProbe(context.Background(), db, snap, false, func() error {
			if remaining := time.Until(expires); remaining > 0 {
				<-time.After(remaining + time.Millisecond)
			}
			return nil
		}, func() error { wrote = true; return nil })
		if !errors.Is(err, errNodeProbeStale) || wrote {
			t.Fatalf("expiry during preparation: wrote=%v err=%v", wrote, err)
		}
	})
}
