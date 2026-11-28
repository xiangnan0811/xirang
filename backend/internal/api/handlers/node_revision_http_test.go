package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"
	nodepkg "xirang/backend/internal/node"
	gormrepo "xirang/backend/internal/repository/gorm"
	"xirang/backend/internal/secure"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func revisionHTTPRouter(db *gorm.DB, role string, userID uint) *gin.Engine {
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.CtxRole, role)
		c.Set(middleware.CtxUserID, userID)
		c.Next()
	})
	h := NewNodeHandler(db, nil, nodepkg.NewNodeService(gormrepo.NewNodeRepository(db)))
	r.GET("/nodes", h.List)
	r.GET("/nodes/:id", middleware.OwnershipNodeCheck(db), h.Get)
	r.PUT("/nodes/:id", h.Update)
	r.POST("/nodes/:id/test-connection", h.TestConnection)
	return r
}

func revisionHTTPRequest(t *testing.T, r *gin.Engine, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var data []byte
	if body != nil {
		var err error
		data, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)
	return resp
}

func revisionHTTPNode(t *testing.T, r *gin.Engine, path string) (model.Node, string) {
	t.Helper()
	resp := revisionHTTPRequest(t, r, http.MethodGet, path, nil)
	var envelope struct {
		Data struct {
			model.Node
			ProbeToken string `json:"probe_token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &envelope); err != nil || resp.Code != http.StatusOK {
		t.Fatalf("GET %s: status=%d body=%s err=%v", path, resp.Code, resp.Body.String(), err)
	}
	return envelope.Data.Node, envelope.Data.ProbeToken
}

func TestNodeRevisionTwoWritersHTTPDualDatabase(t *testing.T) {
	retryTransactionForEachEngine(t, func(t *testing.T, engine string) {
		t.Setenv("DATA_ENCRYPTION_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
		secure.ResetForTesting()
		t.Cleanup(secure.ResetForTesting)
		db, initial, key := newMigratedNodeProbeFixture(t, engine)
		initial.Host = "192.0.2.10"
		if err := db.Model(&model.Node{}).Where("id = ?", initial.ID).UpdateColumn("host", initial.Host).Error; err != nil {
			t.Fatal(err)
		}
		r := revisionHTTPRouter(db, "admin", 0)
		path := fmt.Sprintf("/nodes/%d", initial.ID)
		first, _ := revisionHTTPNode(t, r, path)
		second, _ := revisionHTTPNode(t, r, path)
		if len(first.ConfigRevision) != 32 || first.ConfigRevision != second.ConfigRevision {
			t.Fatalf("initial revisions: %q / %q", first.ConfigRevision, second.ConfigRevision)
		}
		payload := func(name string, revision any) map[string]any {
			return map[string]any{"name": name, "host": initial.Host, "port": initial.Port, "username": initial.Username, "auth_type": "key", "ssh_key_id": key.ID, "expected_revision": revision}
		}
		for _, bad := range []any{"", strings.Repeat("A", 32), "bad", 12} {
			resp := revisionHTTPRequest(t, r, http.MethodPut, path, payload("invalid", bad))
			if resp.Code != http.StatusBadRequest {
				t.Fatalf("invalid revision %v: %d %s", bad, resp.Code, resp.Body.String())
			}
		}
		start := make(chan struct{})
		responses := make([]*httptest.ResponseRecorder, 2)
		var wg sync.WaitGroup
		for i, name := range []string{"writer-one", "writer-two"} {
			wg.Add(1)
			go func(i int, name string) {
				defer wg.Done()
				<-start
				responses[i] = revisionHTTPRequest(t, r, http.MethodPut, path, payload(name, first.ConfigRevision))
			}(i, name)
		}
		close(start)
		wg.Wait()
		var winner model.Node
		var conflicts int
		for _, resp := range responses {
			switch resp.Code {
			case http.StatusOK:
				var result struct {
					Data struct {
						Node model.Node `json:"node"`
					} `json:"data"`
				}
				if err := json.Unmarshal(resp.Body.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				winner = result.Data.Node
			case http.StatusConflict:
				var result struct {
					Data struct {
						ErrorCode string `json:"error_code"`
					} `json:"data"`
				}
				if err := json.Unmarshal(resp.Body.Bytes(), &result); err != nil || result.Data.ErrorCode != "node_revision_conflict" {
					t.Fatalf("conflict: %s %v", resp.Body.String(), err)
				}
				conflicts++
			default:
				t.Fatalf("unexpected writer response %d: %s", resp.Code, resp.Body.String())
			}
		}
		stored, _ := revisionHTTPNode(t, r, path)
		if conflicts != 1 || winner.ID != initial.ID || winner.Name != stored.Name || winner.ConfigRevision != stored.ConfigRevision || stored.ConfigRevision == first.ConfigRevision {
			t.Fatalf("writer results winner=%+v stored=%+v conflicts=%d", winner, stored, conflicts)
		}
		stale := revisionHTTPRequest(t, r, http.MethodPut, path, payload("rejected", first.ConfigRevision))
		after, _ := revisionHTTPNode(t, r, path)
		if stale.Code != http.StatusConflict || !reflect.DeepEqual(after, stored) {
			t.Fatalf("rejected update changed node: %d before=%+v after=%+v", stale.Code, stored, after)
		}
		legacy := payload("legacy-null", nil)
		if resp := revisionHTTPRequest(t, r, http.MethodPut, path, legacy); resp.Code != http.StatusOK {
			t.Fatalf("null revision legacy update: %d %s", resp.Code, resp.Body.String())
		}
		delete(legacy, "expected_revision")
		legacy["name"] = "legacy-omitted"
		if resp := revisionHTTPRequest(t, r, http.MethodPut, path, legacy); resp.Code != http.StatusOK {
			t.Fatalf("omitted revision legacy update: %d %s", resp.Code, resp.Body.String())
		}
		other := model.Node{Name: "backup-conflict-node", Host: "192.0.2.80", Port: 22, Username: "other", AuthType: "password", BackupDir: "backup-conflict"}
		if err := db.Create(&other).Error; err != nil {
			t.Fatal(err)
		}
		legacy["backup_dir"] = other.BackupDir
		duplicate := revisionHTTPRequest(t, r, http.MethodPut, path, legacy)
		if duplicate.Code != http.StatusConflict || strings.Contains(duplicate.Body.String(), "node_revision_conflict") {
			t.Fatalf("backup identifier conflict: %d %s", duplicate.Code, duplicate.Body.String())
		}
	})
}

func TestNodeProbeEvidenceAndRevisionGuardHTTPDualDatabase(t *testing.T) {
	retryTransactionForEachEngine(t, func(t *testing.T, engine string) {
		t.Setenv("DATA_ENCRYPTION_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
		secure.ResetForTesting()
		t.Cleanup(secure.ResetForTesting)
		db, initial, key := newMigratedNodeProbeFixture(t, engine)
		initial.Host = "192.0.2.10"
		if err := db.Model(&model.Node{}).Where("id = ?", initial.ID).UpdateColumn("host", initial.Host).Error; err != nil {
			t.Fatal(err)
		}
		r := revisionHTTPRouter(db, "admin", 0)
		path := fmt.Sprintf("/nodes/%d", initial.ID)
		before, _ := revisionHTTPNode(t, r, path)
		if before.ConnectionTest != nil || before.Status != "warning" {
			t.Fatalf("legacy status became probe evidence: %+v", before)
		}
		// A pre-upgrade state with no applied_status must not invent evidence.
		if err := db.Exec("INSERT INTO node_probe_state (node_id, admitted_seq, applied_seq, applied_token, applied_status) VALUES (?, 1, 1, 'old', '')", initial.ID).Error; err != nil {
			t.Fatal(err)
		}
		before, _ = revisionHTTPNode(t, r, path)
		if before.ConnectionTest != nil {
			t.Fatalf("historical state invented evidence: %+v", before.ConnectionTest)
		}
		inflight, err := admitNodeProbe(context.Background(), db, initial.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Model(&model.Node{}).Where("id = ?", initial.ID).UpdateColumn("name", "renamed").Error; err != nil {
			t.Fatal(err)
		}
		var stateBefore nodeProbeState
		if err := db.First(&stateBefore, "node_id = ?", initial.ID).Error; err != nil {
			t.Fatal(err)
		}
		resp := revisionHTTPRequest(t, r, http.MethodPost, path+"/test-connection?expected_revision="+before.ConfigRevision, nil)
		var stale struct {
			Data struct {
				Stale bool `json:"stale"`
			} `json:"data"`
		}
		if err := json.Unmarshal(resp.Body.Bytes(), &stale); err != nil || resp.Code != http.StatusOK || !stale.Data.Stale {
			t.Fatalf("stale automatic probe: %d %s %v", resp.Code, resp.Body.String(), err)
		}
		var stateAfter nodeProbeState
		if err := db.First(&stateAfter, "node_id = ?", initial.ID).Error; err != nil || stateAfter != stateBefore {
			t.Fatalf("stale request allocated probe: before=%+v after=%+v err=%v", stateBefore, stateAfter, err)
		}
		for _, bad := range []string{"", "ABC", strings.Repeat("A", 32)} {
			resp := revisionHTTPRequest(t, r, http.MethodPost, path+"/test-connection?expected_revision="+bad, nil)
			if resp.Code != http.StatusBadRequest {
				t.Fatalf("invalid query %q: %d %s", bad, resp.Code, resp.Body.String())
			}
		}
		if _, err := applyNodeProbe(context.Background(), db, inflight, "online", 12, time.Now()); err != nil {
			t.Fatalf("rejected admission superseded in-flight probe: %v", err)
		}
		got, token := revisionHTTPNode(t, r, path)
		if got.ConnectionTest == nil || !got.ConnectionTest.Current || got.ConnectionTest.Status != "online" || token != inflight.token {
			t.Fatalf("completed probe: %+v token=%q", got.ConnectionTest, token)
		}
		manualStatus := map[string]any{
			"name": "renamed", "host": initial.Host, "port": initial.Port,
			"username": initial.Username, "auth_type": "key", "ssh_key_id": key.ID,
			"status": "warning",
		}
		manualStatus["expected_revision"] = got.ConfigRevision
		updated := revisionHTTPRequest(t, r, http.MethodPut, path, manualStatus)
		var updateResult struct {
			Data struct {
				Node model.Node `json:"node"`
			} `json:"data"`
		}
		if err := json.Unmarshal(updated.Body.Bytes(), &updateResult); err != nil || updated.Code != http.StatusOK {
			t.Fatalf("status update: %d %s %v", updated.Code, updated.Body.String(), err)
		}
		if updateResult.Data.Node.ConnectionTest == nil || updateResult.Data.Node.ConnectionTest.Status != "online" || !updateResult.Data.Node.ConnectionTest.Current {
			t.Fatalf("status update lost completed evidence: %+v", updateResult.Data.Node)
		}
		got, _ = revisionHTTPNode(t, r, path)
		if got.Status != "warning" || got.ConnectionTest == nil || got.ConnectionTest.Status != "online" || !got.ConnectionTest.Current {
			t.Fatalf("manual status obscured evidence: %+v", got)
		}
		if updateResult.Data.Node.ConfigRevision != got.ConfigRevision {
			t.Fatalf("update revision differs from read: %q / %q", updateResult.Data.Node.ConfigRevision, got.ConfigRevision)
		}
		pending, err := admitNodeProbe(context.Background(), db, initial.ID)
		if err != nil {
			t.Fatal(err)
		}
		got, token = revisionHTTPNode(t, r, path)
		if got.ConnectionTest == nil || !got.ConnectionTest.Current || token != "" {
			t.Fatalf("pending probe evidence=%+v token=%q", got.ConnectionTest, token)
		}
		manualStatus["name"] = "renamed-while-pending"
		manualStatus["expected_revision"] = got.ConfigRevision
		updated = revisionHTTPRequest(t, r, http.MethodPut, path, manualStatus)
		updateResult.Data.Node = model.Node{}
		if err := json.Unmarshal(updated.Body.Bytes(), &updateResult); err != nil || updated.Code != http.StatusOK || updateResult.Data.Node.ConnectionTest == nil || !updateResult.Data.Node.ConnectionTest.Current || updateResult.Data.Node.ConnectionTest.Status != "online" {
			t.Fatalf("pending probe update evidence: %d %s %v", updated.Code, updated.Body.String(), err)
		}
		if _, err := applyNodeProbe(context.Background(), db, pending, "offline", 0, time.Now()); err != nil {
			t.Fatal(err)
		}
		if err := db.Model(&model.SSHKey{}).Where("id = ?", key.ID).UpdateColumn("disabled", true).Error; err != nil {
			t.Fatal(err)
		}
		got, token = revisionHTTPNode(t, r, path)
		if got.ConnectionTest == nil || got.ConnectionTest.Status != "offline" || got.ConnectionTest.Current || token != "" {
			t.Fatalf("key edit evidence=%+v token=%q", got.ConnectionTest, token)
		}
		// A valid revision is admitted and completes without dialing when the key is disabled.
		var admittedBefore nodeProbeState
		if err := db.First(&admittedBefore, "node_id = ?", initial.ID).Error; err != nil {
			t.Fatal(err)
		}
		resp = revisionHTTPRequest(t, r, http.MethodPost, path+"/test-connection?expected_revision="+got.ConfigRevision, nil)
		var completed struct {
			Data struct {
				OK         bool   `json:"ok"`
				Stale      bool   `json:"stale"`
				ProbeToken string `json:"probe_token"`
			} `json:"data"`
		}
		var admittedAfter nodeProbeState
		decodeErr := json.Unmarshal(resp.Body.Bytes(), &completed)
		stateErr := db.First(&admittedAfter, "node_id = ?", initial.ID).Error
		if decodeErr != nil || stateErr != nil || resp.Code != http.StatusOK || completed.Data.OK || completed.Data.Stale || completed.Data.ProbeToken == "" || admittedAfter.AdmittedSeq != admittedBefore.AdmittedSeq+1 || admittedAfter.AppliedSeq != admittedAfter.AdmittedSeq || admittedAfter.AppliedToken != completed.Data.ProbeToken {
			t.Fatalf("valid revision probe: %d %s before=%+v after=%+v decode=%v state=%v", resp.Code, resp.Body.String(), admittedBefore, admittedAfter, decodeErr, stateErr)
		}
		// An applied result follows connection settings, while unrelated names do not.
		if err := db.Model(&model.SSHKey{}).Where("id = ?", key.ID).UpdateColumn("disabled", false).Error; err != nil {
			t.Fatal(err)
		}
		latest, err := admitNodeProbe(context.Background(), db, initial.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := applyNodeProbe(context.Background(), db, latest, "offline", 0, time.Now()); err != nil {
			t.Fatal(err)
		}
		if err := db.Model(&model.Node{}).Where("id = ?", initial.ID).UpdateColumn("name", "another-name").Error; err != nil {
			t.Fatal(err)
		}
		got, _ = revisionHTTPNode(t, r, path)
		if got.ConnectionTest == nil || !got.ConnectionTest.Current {
			t.Fatalf("name edit invalidated evidence: %+v", got.ConnectionTest)
		}
		manualStatus["host"] = "192.0.2.50"
		manualStatus["expected_revision"] = got.ConfigRevision
		updated = revisionHTTPRequest(t, r, http.MethodPut, path, manualStatus)
		updateResult.Data.Node = model.Node{}
		if err := json.Unmarshal(updated.Body.Bytes(), &updateResult); err != nil || updated.Code != http.StatusOK || updateResult.Data.Node.ConnectionTest == nil || updateResult.Data.Node.ConnectionTest.Current || updateResult.Data.Node.ConnectionTest.Status != "offline" {
			t.Fatalf("connection update evidence: %d %s %v", updated.Code, updated.Body.String(), err)
		}
		got, _ = revisionHTTPNode(t, r, path)
		if got.ConnectionTest == nil || got.ConnectionTest.Current || got.ConnectionTest.Status != "offline" {
			t.Fatalf("host edit evidence: %+v", got.ConnectionTest)
		}
		// Simulate a result applied before the key expired; no key update occurs
		// between that historical token and the read at the current time.
		expiredAt := time.Now().Add(-time.Hour)
		if err := db.Model(&model.SSHKey{}).Where("id = ?", key.ID).UpdateColumn("expires_at", expiredAt).Error; err != nil {
			t.Fatal(err)
		}
		expired, err := admitNodeProbe(context.Background(), db, initial.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := applyNodeProbe(context.Background(), db, expired, "offline", 0, time.Now()); err != nil {
			t.Fatal(err)
		}
		historicalToken := probeIdentityToken(expired.node, expired.seq, expiredAt.Add(-time.Hour))
		if err := db.Model(&nodeProbeState{}).Where("node_id = ?", initial.ID).UpdateColumn("applied_token", historicalToken).Error; err != nil {
			t.Fatal(err)
		}
		got, token = revisionHTTPNode(t, r, path)
		if got.ConnectionTest == nil || got.ConnectionTest.Current || token != "" {
			t.Fatalf("expired key evidence=%+v token=%q", got.ConnectionTest, token)
		}
		list := revisionHTTPRequest(t, r, http.MethodGet, "/nodes", nil)
		var inventory struct {
			Data []model.Node `json:"data"`
		}
		if err := json.Unmarshal(list.Body.Bytes(), &inventory); err != nil || list.Code != http.StatusOK || len(inventory.Data) != 1 || inventory.Data[0].ConnectionTest == nil || inventory.Data[0].ConnectionTest.Current || inventory.Data[0].ConnectionTest.Status != "offline" {
			t.Fatalf("inventory evidence: %d %s %v", list.Code, list.Body.String(), err)
		}
		// List and detail must not decrypt credential columns, even for bound keys.
		if err := db.Exec("UPDATE nodes SET password = ? WHERE id = ?", "enc:v2:invalid", initial.ID).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Exec("UPDATE ssh_keys SET private_key = ? WHERE id = ?", "enc:v2:invalid", key.ID).Error; err != nil {
			t.Fatal(err)
		}
		user := model.User{Username: "node-evidence-operator", PasswordHash: "unused", Role: "operator"}
		if err := db.Create(&user).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&model.NodeOwner{NodeID: initial.ID, UserID: user.ID}).Error; err != nil {
			t.Fatal(err)
		}
		other := model.Node{Name: "other-node", Host: "192.0.2.40", Port: 22, Username: "other", AuthType: "password", BackupDir: "other-node"}
		if err := db.Create(&other).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Model(&model.Node{}).Where("id = ?", initial.ID).UpdateColumn("archived", true).Error; err != nil {
			t.Fatal(err)
		}
		operator := revisionHTTPRouter(db, "operator", user.ID)
		list = revisionHTTPRequest(t, operator, http.MethodGet, "/nodes", nil)
		inventory.Data = nil
		if err := json.Unmarshal(list.Body.Bytes(), &inventory); err != nil || list.Code != http.StatusOK || len(inventory.Data) != 0 {
			t.Fatalf("operator archived filter: %d %s %v", list.Code, list.Body.String(), err)
		}
		list = revisionHTTPRequest(t, operator, http.MethodGet, "/nodes?include_archived=true", nil)
		if err := json.Unmarshal(list.Body.Bytes(), &inventory); err != nil || list.Code != http.StatusOK || len(inventory.Data) != 1 || inventory.Data[0].ID != initial.ID || inventory.Data[0].ConnectionTest == nil {
			t.Fatalf("operator ownership filter: %d %s %v", list.Code, list.Body.String(), err)
		}
		_, _ = revisionHTTPNode(t, operator, path)
		if revisionHTTPRequest(t, operator, http.MethodGet, fmt.Sprintf("/nodes/%d", other.ID), nil).Code != http.StatusForbidden {
			t.Fatal("operator read an unowned node")
		}
		keys := make([]model.SSHKey, 1000)
		for i := range keys {
			name := fmt.Sprintf("inventory-key-%04d", i)
			keys[i] = model.SSHKey{Name: name, Username: "reader", KeyType: "ed25519", Fingerprint: name}
		}
		if err := db.CreateInBatches(&keys, 20).Error; err != nil {
			t.Fatal(err)
		}
		bulk := make([]model.Node, len(keys))
		for i := range bulk {
			name := fmt.Sprintf("inventory-%04d", i)
			bulk[i] = model.Node{Name: name, Host: "192.0.2.90", Port: 22, Username: "reader", AuthType: "key", SSHKeyID: &keys[i].ID, BackupDir: name}
		}
		if err := db.CreateInBatches(&bulk, 20).Error; err != nil {
			t.Fatal(err)
		}
		owners := make([]model.NodeOwner, len(bulk))
		for i := range bulk {
			owners[i] = model.NodeOwner{NodeID: bulk[i].ID, UserID: user.ID}
		}
		if err := db.CreateInBatches(&owners, 100).Error; err != nil {
			t.Fatal(err)
		}
		list = revisionHTTPRequest(t, r, http.MethodGet, "/nodes?include_archived=true", nil)
		inventory.Data = nil
		if err := json.Unmarshal(list.Body.Bytes(), &inventory); err != nil || list.Code != http.StatusOK || len(inventory.Data) != len(bulk)+2 || inventory.Data[0].ConnectionTest == nil {
			t.Fatalf("large inventory: count=%d status=%d err=%v", len(inventory.Data), list.Code, err)
		}
		for _, item := range inventory.Data[2:] {
			if item.SSHKey == nil || item.SSHKeyID == nil || item.SSHKey.ID != *item.SSHKeyID {
				t.Fatalf("large inventory missing key: %+v", item)
			}
		}
		list = revisionHTTPRequest(t, operator, http.MethodGet, "/nodes?include_archived=true", nil)
		inventory.Data = nil
		if err := json.Unmarshal(list.Body.Bytes(), &inventory); err != nil || list.Code != http.StatusOK || len(inventory.Data) != len(bulk)+1 || inventory.Data[0].ID != initial.ID {
			t.Fatalf("large owned inventory: count=%d status=%d err=%v", len(inventory.Data), list.Code, err)
		}
		for _, item := range inventory.Data[1:] {
			if item.SSHKey == nil || item.SSHKeyID == nil || item.SSHKey.ID != *item.SSHKeyID {
				t.Fatalf("owned inventory missing key: %+v", item)
			}
		}
		list = revisionHTTPRequest(t, revisionHTTPRouter(db, "operator", 0), http.MethodGet, "/nodes?include_archived=true", nil)
		inventory.Data = nil
		if err := json.Unmarshal(list.Body.Bytes(), &inventory); err != nil || list.Code != http.StatusOK || len(inventory.Data) != 0 {
			t.Fatalf("operator without user ID: %d %s %v", list.Code, list.Body.String(), err)
		}
	})
}
