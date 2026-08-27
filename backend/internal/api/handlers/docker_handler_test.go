package handlers

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"xirang/backend/internal/credentialaudit"
	"xirang/backend/internal/model"
	"xirang/backend/internal/sshutil"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func openDockerHandlerTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+handlerTestDBName(t)+"?mode=memory&cache=shared&_loc=UTC"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&model.CredentialAuditEvent{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func TestDockerVolumeAuditDoesNotPersistRemoteOutputOrVolumeNames(t *testing.T) {
	db := openDockerHandlerTestDB(t)
	h := NewDockerHandler(db)
	keyID := uint(9)
	node := model.Node{ID: 5, Name: "docker-node", Host: "10.40.0.5", AuthType: "key", SSHKeyID: &keyID}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("userID", uint(101))
	c.Set("username", "alice")
	c.Set("role", "operator")
	c.Request = httptest.NewRequest("GET", "/nodes/5/docker-volumes", nil)

	h.writeDockerVolumeAudit(c, node, sshutil.ResolvedCredential{Kind: "ssh_key", Source: "ssh_key_id=9", KeyID: &keyID}, credentialaudit.OutcomeFailure, "list", errors.New("docker list failed: output: FAKE_DOCKER_OUTPUT_FOR_TEST_ONLY volume-prod-data"), 3, true)

	var event model.CredentialAuditEvent
	if err := db.First(&event).Error; err != nil {
		t.Fatalf("load audit event: %v", err)
	}
	if event.Action != "docker_volumes.discover" || event.Purpose != sshutil.PurposeDockerVolumes || event.Outcome != credentialaudit.OutcomeFailure {
		t.Fatalf("unexpected docker audit event: %+v", event)
	}
	if event.NodeID == nil || *event.NodeID != node.ID || event.SSHKeyID == nil || *event.SSHKeyID != keyID {
		t.Fatalf("expected node/key ids in audit event: %+v", event)
	}
	if strings.Contains(event.Metadata, "FAKE_DOCKER_OUTPUT_FOR_TEST_ONLY") || strings.Contains(event.Metadata, "volume-prod-data") || strings.Contains(event.ErrorMessage, "FAKE_DOCKER_OUTPUT_FOR_TEST_ONLY") {
		t.Fatalf("docker audit must not persist output or volume names: metadata=%s error=%s", event.Metadata, event.ErrorMessage)
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(event.Metadata), &metadata); err != nil {
		t.Fatalf("metadata json: %v", err)
	}
	if metadata["count"] == nil || metadata["has_warning"] != true || metadata["stage"] != "list" {
		t.Fatalf("safe docker audit metadata missing expected fields: %#v", metadata)
	}
}
