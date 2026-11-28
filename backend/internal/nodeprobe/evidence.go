// Package nodeprobe projects persisted connection-test evidence for node reads.
package nodeprobe

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"strconv"
	"time"

	"gorm.io/gorm"
	"xirang/backend/internal/model"
)

// State is the persisted admission and last completed probe for a node.
type State struct {
	NodeID        uint `gorm:"primaryKey"`
	AdmittedSeq   int64
	AppliedSeq    int64
	AppliedToken  string
	AppliedStatus string
}

func (State) TableName() string { return "node_probe_state" }

// IdentityToken preserves the probe identity encoding used for trust fencing.
func IdentityToken(node model.Node, seq int64, now time.Time) string {
	h := sha256.New()
	write := func(s string) {
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(s)))
		_, _ = h.Write(size[:])
		_, _ = h.Write([]byte(s))
	}
	write(strconv.FormatUint(uint64(node.ID), 10))
	write(node.ProbeGeneration)
	if node.AuthType == "key" && node.SSHKeyID != nil {
		write(strconv.FormatUint(uint64(*node.SSHKeyID), 10))
		if node.SSHKey != nil {
			write("present")
			write(node.SSHKey.ProbeGeneration)
			if node.SSHKey.ExpiresAt != nil && !node.SSHKey.ExpiresAt.After(now) {
				write("expired")
			} else {
				write("valid")
			}
		} else {
			write("missing")
		}
	} else {
		write("none")
	}
	write(strconv.FormatInt(seq, 10))
	return hex.EncodeToString(h.Sum(nil))
}

// LoadEvidence projects completed results using the caller's transaction and
// already-loaded node and key metadata. It returns states for token selection.
func LoadEvidence(tx *gorm.DB, nodes []model.Node) (map[uint]State, error) {
	states := make(map[uint]State, len(nodes))
	const batchSize = 500
	ids := make([]uint, 0, batchSize)
	for start := 0; start < len(nodes); start += batchSize {
		end := start + batchSize
		if end > len(nodes) {
			end = len(nodes)
		}
		ids = ids[:0]
		for i := start; i < end; i++ {
			ids = append(ids, nodes[i].ID)
		}
		var rows []State
		if err := tx.Where("node_id IN ?", ids).Find(&rows).Error; err != nil {
			return nil, err
		}
		for _, state := range rows {
			states[state.NodeID] = state
		}
	}
	now := time.Now()
	for i := range nodes {
		state := states[nodes[i].ID]
		if state.AppliedStatus == "online" || state.AppliedStatus == "offline" {
			nodes[i].ConnectionTest = &model.NodeConnectionTest{
				Status:  state.AppliedStatus,
				Current: state.AppliedToken != "" && state.AppliedToken == IdentityToken(nodes[i], state.AppliedSeq, now),
			}
		}
	}
	return states, nil
}

// LoadKeys loads the response shape and identity metadata in bounded batches.
func LoadKeys(tx *gorm.DB, nodes []model.Node) error {
	const batchSize = 500
	ids := make([]uint, 0, batchSize)
	for start := 0; start < len(nodes); start += batchSize {
		end := start + batchSize
		if end > len(nodes) {
			end = len(nodes)
		}
		ids = ids[:0]
		for i := start; i < end; i++ {
			if nodes[i].SSHKeyID != nil {
				ids = append(ids, *nodes[i].SSHKeyID)
			}
		}
		if len(ids) == 0 {
			continue
		}
		var keys []model.SSHKey
		if err := tx.Omit("private_key").Where("id IN ?", ids).Find(&keys).Error; err != nil {
			return err
		}
		byID := make(map[uint]*model.SSHKey, len(keys))
		for i := range keys {
			byID[keys[i].ID] = &keys[i]
		}
		for i := start; i < end; i++ {
			if nodes[i].SSHKeyID != nil {
				nodes[i].SSHKey = byID[*nodes[i].SSHKeyID]
			}
		}
	}
	return nil
}
