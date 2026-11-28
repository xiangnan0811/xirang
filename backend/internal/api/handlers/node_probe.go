package handlers

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"xirang/backend/internal/database"
	"xirang/backend/internal/model"
	"xirang/backend/internal/nodeprobe"
	"xirang/backend/internal/sshutil"
)

var errNodeProbeStale = errors.New("node probe is stale")

const staleNodeProbeMessage = "节点配置在连接测试期间已变更，本次测试结果已过时，请重新测试"

type nodeProbeState = nodeprobe.State

type nodeProbeSnapshot struct {
	node       model.Node
	seq        int64
	token      string
	auth       []ssh.AuthMethod
	credential sshutil.ResolvedCredential
	authErr    error
}

// nodeProbeTransaction takes the same table/row lock order as rotation. SQLite
// reserves its writer before reading any identity, including on deferred DSNs.
func nodeProbeTransaction(ctx context.Context, db *gorm.DB, fn func(*gorm.DB) error) error {
	var options *sql.TxOptions
	if db.Name() == "postgres" {
		options = &sql.TxOptions{Isolation: sql.LevelReadCommitted}
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if tx.Name() == "postgres" {
			if err := tx.Exec("LOCK TABLE nodes IN ROW EXCLUSIVE MODE").Error; err != nil {
				return err
			}
		} else if tx.Name() == "sqlite" {
			if err := tx.Exec("UPDATE nodes SET id = id WHERE 1 = 0").Error; err != nil {
				return err
			}
		}
		return fn(tx)
	}, options)
}

// nodeProbeWriteTransaction permits cancellation while acquiring locks, but
// keeps the transaction alive once the append is authorized. database/sql
// otherwise rolls back a transaction as soon as its context is canceled,
// potentially releasing the fence during the filesystem write.
func nodeProbeWriteTransaction(ctx context.Context, db *gorm.DB, fn func(*gorm.DB, func() error) error) error {
	txCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	defer cancel()
	finished := make(chan struct{})
	defer close(finished)
	var mu sync.Mutex
	acquiring := true
	go func() {
		select {
		case <-ctx.Done():
			mu.Lock()
			if acquiring {
				cancel()
			}
			mu.Unlock()
		case <-finished:
		}
	}()
	return nodeProbeTransaction(txCtx, db, func(tx *gorm.DB) error {
		return fn(tx, func() error {
			mu.Lock()
			defer mu.Unlock()
			acquiring = false
			return ctx.Err()
		})
	})
}

// lockProbeIdentity locks node before its effective key. Fencing reads only
// metadata; admission additionally needs credentials to construct SSH auth.
func lockProbeIdentity(tx *gorm.DB, id uint, credentials, effectiveKey bool) (model.Node, error) {
	var node model.Node
	query := tx.Clauses(clause.Locking{Strength: "UPDATE"})
	if !credentials {
		query = query.Select("id", "host", "port", "auth_type", "ssh_key_id", "probe_generation", "config_revision")
	}
	if err := query.First(&node, id).Error; err != nil {
		return node, err
	}
	if effectiveKey && node.AuthType == "key" && node.SSHKeyID != nil {
		var key model.SSHKey
		query := tx.Clauses(clause.Locking{Strength: "UPDATE"})
		if !credentials {
			query = query.Select("id", "probe_generation", "expires_at", "last_used_at")
		}
		err := query.First(&key, *node.SSHKeyID).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return node, err
		}
		if err == nil {
			node.SSHKey = &key
		}
	}
	return node, nil
}

func probeIdentityToken(node model.Node, seq int64, now time.Time) string {
	return nodeprobe.IdentityToken(node, seq, now)
}

func admitNodeProbe(ctx context.Context, db *gorm.DB, id uint) (nodeProbeSnapshot, error) {
	return admitNodeProbeExpected(ctx, db, id, nil)
}

func admitNodeProbeExpected(ctx context.Context, db *gorm.DB, id uint, expected *string) (nodeProbeSnapshot, error) {
	var snap nodeProbeSnapshot
	err := nodeProbeTransaction(ctx, db, func(tx *gorm.DB) error {
		if expected != nil {
			var identity model.Node
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("config_revision").First(&identity, id).Error; err != nil {
				return err
			}
			if identity.ConfigRevision != *expected {
				return errNodeProbeStale
			}
		}
		node, err := lockProbeIdentity(tx, id, true, true)
		if err != nil {
			return err
		}
		if err := tx.Exec("INSERT INTO node_probe_state (node_id, admitted_seq, applied_seq, applied_token) VALUES (?, 0, 0, '') ON CONFLICT (node_id) DO NOTHING", id).Error; err != nil {
			return err
		}
		var state nodeProbeState
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&state, "node_id = ?", id).Error; err != nil {
			return err
		}
		if state.AdmittedSeq == int64(^uint64(0)>>1) {
			return fmt.Errorf("node probe sequence exhausted")
		}
		state.AdmittedSeq++
		if err := tx.Model(&nodeProbeState{}).Where("node_id = ?", id).UpdateColumn("admitted_seq", state.AdmittedSeq).Error; err != nil {
			return err
		}
		snap.node, snap.seq = node, state.AdmittedSeq
		snap.token = probeIdentityToken(node, snap.seq, time.Now())
		snap.auth, _, snap.credential, snap.authErr = sshutil.BuildSSHAuthWithKeyForPurpose(node, nil, sshutil.PurposeNodeTest)
		return ctx.Err()
	})
	return snap, err
}

func currentProbeState(tx *gorm.DB, id uint) (model.Node, nodeProbeState, error) {
	node, err := lockProbeIdentity(tx, id, false, true)
	if err != nil {
		return node, nodeProbeState{}, err
	}
	var state nodeProbeState
	err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&state, "node_id = ?", id).Error
	return node, state, err
}

func guardNodeProbe(ctx context.Context, db *gorm.DB, snap nodeProbeSnapshot, applied bool, prepare, write func() error) error {
	return nodeProbeWriteTransaction(ctx, db, func(tx *gorm.DB, authorizeWrite func() error) error {
		node, state, err := currentProbeState(tx, snap.node.ID)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errNodeProbeStale
		}
		if err != nil {
			return err
		}
		if err := prepare(); err != nil {
			return err
		}
		if state.AdmittedSeq != snap.seq || probeIdentityToken(node, snap.seq, time.Now()) != snap.token || (applied && (state.AppliedSeq != snap.seq || state.AppliedToken != snap.token)) {
			return errNodeProbeStale
		}
		if err := authorizeWrite(); err != nil {
			return err
		}
		return write()
	})
}

func applyNodeProbe(ctx context.Context, db *gorm.DB, snap nodeProbeSnapshot, status string, latency int, testedAt time.Time) (bool, error) {
	testedAt = testedAt.UTC()
	lastUsed := false
	err := nodeProbeTransaction(ctx, db, func(tx *gorm.DB) error {
		node, state, err := currentProbeState(tx, snap.node.ID)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errNodeProbeStale
		}
		if err != nil {
			return err
		}
		if state.AdmittedSeq != snap.seq || probeIdentityToken(node, snap.seq, time.Now()) != snap.token {
			return errNodeProbeStale
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := tx.Model(&model.Node{}).Where("id = ?", node.ID).UpdateColumns(map[string]any{"status": status, "connection_latency": latency, "last_seen_at": testedAt}).Error; err != nil {
			return err
		}
		if status == "online" && node.SSHKey != nil && snap.credential.KeyID != nil {
			query := tx.Model(&model.SSHKey{}).Where("id = ?", node.SSHKey.ID)
			if tx.Name() == "sqlite" {
				// SQLite may contain DATETIME values with different offsets, whose
				// lexical order does not necessarily match their instant order.
				if node.SSHKey.LastUsedAt != nil && !testedAt.After(*node.SSHKey.LastUsedAt) {
					query = nil
				}
			} else {
				query = query.Where("last_used_at IS NULL OR last_used_at < ?", testedAt)
			}
			if query != nil {
				result := query.UpdateColumn("last_used_at", testedAt)
				if result.Error != nil {
					return result.Error
				}
				lastUsed = result.RowsAffected > 0
			}
		}
		if err := tx.Model(&nodeProbeState{}).Where("node_id = ?", node.ID).Updates(map[string]any{"applied_seq": snap.seq, "applied_token": snap.token, "applied_status": status}).Error; err != nil {
			return err
		}
		return ctx.Err()
	})
	return lastUsed, err
}

func loadNodeProbeEvidence(tx *gorm.DB, nodes []model.Node) (map[uint]nodeProbeState, error) {
	return nodeprobe.LoadEvidence(tx, nodes)
}

func readNodeWithProbeToken(ctx context.Context, db *gorm.DB, id uint) (model.Node, string, error) {
	var node model.Node
	var token string
	err := database.WithReadonlySnapshot(ctx, db, func(tx *gorm.DB) error {
		// Detail responses historically preload a bound key even in password mode.
		if err := tx.Omit("password", "private_key").Preload("SSHKey", func(keyDB *gorm.DB) *gorm.DB {
			return keyDB.Omit("private_key")
		}).First(&node, id).Error; err != nil {
			return err
		}
		nodes := []model.Node{node}
		states, err := loadNodeProbeEvidence(tx, nodes)
		if err != nil {
			return err
		}
		node = nodes[0]
		state := states[id]
		if state.AppliedToken != "" && state.AppliedSeq == state.AdmittedSeq && state.AppliedToken == probeIdentityToken(node, state.AdmittedSeq, time.Now()) {
			token = state.AppliedToken
		}
		return nil
	})
	return node, token, err
}

func trustProbeSnapshot(ctx context.Context, db *gorm.DB, id uint, supplied string) (nodeProbeSnapshot, error) {
	var snap nodeProbeSnapshot
	err := nodeProbeTransaction(ctx, db, func(tx *gorm.DB) error {
		node, err := lockProbeIdentity(tx, id, false, supplied != "")
		if err != nil {
			return err
		}
		snap.node = node
		if supplied == "" {
			snap.token = node.ProbeGeneration
			return nil
		}
		var state nodeProbeState
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&state, "node_id = ?", id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errNodeProbeStale
			}
			return err
		}
		snap.seq = state.AdmittedSeq
		snap.token = probeIdentityToken(node, snap.seq, time.Now())
		if state.AppliedSeq != snap.seq || state.AppliedToken == "" || supplied != snap.token || state.AppliedToken != supplied {
			return errNodeProbeStale
		}
		return nil
	})
	return snap, err
}

func guardLegacyTrust(ctx context.Context, db *gorm.DB, snap nodeProbeSnapshot, prepare, write func() error) error {
	return nodeProbeWriteTransaction(ctx, db, func(tx *gorm.DB, authorizeWrite func() error) error {
		node, err := lockProbeIdentity(tx, snap.node.ID, false, false)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errNodeProbeStale
		}
		if err != nil {
			return err
		}
		if err := prepare(); err != nil {
			return err
		}
		if node.ProbeGeneration != snap.token || node.Host != snap.node.Host || node.Port != snap.node.Port {
			return errNodeProbeStale
		}
		if err := authorizeWrite(); err != nil {
			return err
		}
		return write()
	})
}
