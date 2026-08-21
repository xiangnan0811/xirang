package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestSnapshotDiffResolvesOnlyCommittedFullIDs(t *testing.T) {
	left := strings.Repeat("a", 64)
	right := strings.Repeat("b", 64)
	session := &snapshotLineageSession{mode: publication.LineageExact, points: []publication.CommittedPoint{
		{RecoveryPointID: strings.Repeat("1", 32), FullNativeID: left},
		{RecoveryPointID: strings.Repeat("2", 32), FullNativeID: right},
	}}
	runner := &snapshotDiffRunnerFake{output: "+ /new-file\n"}
	router, taskEntity := setupSnapshotDiffHandlerRouter(t, session, runner)

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/tasks/%d/snapshots/diff?snap1=%s&snap2=%s", taskEntity.ID, left[:12], right[:12]), nil))
	if response.Code != http.StatusGone || runner.calls != 0 {
		t.Fatalf("retired diff status=%d calls=%d body=%s", response.Code, runner.calls, response.Body.String())
	}
}

func TestSnapshotDiffRejectsCrossTaskOrUnknownPrefixBeforeRunner(t *testing.T) {
	session := &snapshotLineageSession{mode: publication.LineageExact, resolveErr: errors.New("not a committed task point")}
	runner := &snapshotDiffRunnerFake{}
	router, taskEntity := setupSnapshotDiffHandlerRouter(t, session, runner)

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/tasks/%d/snapshots/diff?snap1=%s&snap2=%s", taskEntity.ID, strings.Repeat("a", 12), strings.Repeat("b", 12)), nil))
	if response.Code != http.StatusGone || runner.calls != 0 {
		t.Fatalf("retired diff reached runner: status=%d calls=%d body=%s", response.Code, runner.calls, response.Body.String())
	}
}

func TestSnapshotDiffRollbackSafeDisabledKeepsExactGuard(t *testing.T) {
	left := strings.Repeat("a", 64)
	right := strings.Repeat("b", 64)
	session := &snapshotLineageSession{mode: publication.LineageExact, points: []publication.CommittedPoint{{RecoveryPointID: strings.Repeat("1", 32), FullNativeID: left}, {RecoveryPointID: strings.Repeat("2", 32), FullNativeID: right}}}
	runner := &snapshotDiffRunnerFake{output: "+ /exact-only\n"}
	router, taskEntity := setupSnapshotDiffHandlerRouter(t, session, runner)

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/tasks/%d/snapshots/diff?snap1=%s&snap2=%s", taskEntity.ID, left[:12], right[:12]), nil))
	if response.Code != http.StatusGone || runner.calls != 0 {
		t.Fatalf("retired diff status=%d calls=%d body=%s", response.Code, runner.calls, response.Body.String())
	}
}

type snapshotDiffRunnerFake struct {
	output string
	err    error
	calls  int
	left   string
	right  string
}

func (runner *snapshotDiffRunnerFake) RunSnapshotDiff(_ context.Context, _ model.Task, left, right string) (string, error) {
	runner.calls++
	runner.left = left
	runner.right = right
	return runner.output, runner.err
}

func setupSnapshotDiffHandlerRouter(t *testing.T, session publication.LineageSession, runner SnapshotDiffRunner) (*gin.Engine, model.Task) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared&_loc=UTC", strings.ReplaceAll(t.Name(), "/", "_"))), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Task{}, &model.Node{}, &model.SSHKey{}); err != nil {
		t.Fatal(err)
	}
	taskEntity := model.Task{Name: "restic-diff", ExecutorType: "restic", Status: "pending"}
	if err := db.Create(&taskEntity).Error; err != nil {
		t.Fatal(err)
	}
	handler := NewSnapshotDiffHandler(db, &snapshotGuardFake{session: session}, runner)
	router := gin.New()
	router.GET("/tasks/:id/snapshots/diff", handler.Diff)
	return router, taskEntity
}

var _ SnapshotDiffRunner = (*snapshotDiffRunnerFake)(nil)
