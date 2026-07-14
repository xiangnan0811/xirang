package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/model"
	"xirang/backend/internal/task/executor"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestSnapshotListExactUsesCommittedTaskPointIntersection(t *testing.T) {
	allowed := []publication.CommittedPoint{{RecoveryPointID: "11111111111111111111111111111111", FullNativeID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", CapturedAt: time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)}}
	snapshots := []executor.ResticSnapshot{
		{ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{ID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		{ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
	}
	got := filterCommittedResticSnapshots(snapshots, allowed)
	if len(got) != 1 || got[0].ID != allowed[0].FullNativeID {
		t.Fatalf("exact snapshots=%+v", got)
	}
}

func TestSnapshotHandlerConstructorsUseTypedRuntimePorts(t *testing.T) {
	for _, check := range []struct {
		file      string
		signature string
	}{
		{"snapshot_handler.go", "func NewSnapshotHandler(db *gorm.DB, guard publication.LineageGuard, restic LegacyResticSnapshots) *SnapshotHandler"},
		{"snapshot_diff_handler.go", "func NewSnapshotDiffHandler(db *gorm.DB, guard publication.LineageGuard, runner SnapshotDiffRunner) *SnapshotDiffHandler"},
	} {
		contents, err := os.ReadFile(check.file)
		if err != nil {
			t.Fatal(err)
		}
		source := string(contents)
		if strings.Contains(source, "dependencies ...any") || !strings.Contains(source, check.signature) {
			t.Fatalf("%s does not expose the typed runtime constructor", check.file)
		}
	}
}

func TestSnapshotListPristineCompatibilityPreservesLegacyResults(t *testing.T) {
	session := &snapshotLineageSession{mode: publication.LineageCompatibility}
	restic := &snapshotResticFake{snapshots: []executor.ResticSnapshot{{ID: strings.Repeat("a", 64)}, {ID: strings.Repeat("b", 64)}}}
	router, taskEntity := setupSnapshotHandlerRouter(t, session, restic)

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/tasks/%d/snapshots", taskEntity.ID), nil))
	if response.Code != http.StatusOK || restic.listCalls != 1 || restic.taggedCalls != 0 {
		t.Fatalf("pristine snapshot list status=%d list=%d tagged=%d body=%s", response.Code, restic.listCalls, restic.taggedCalls, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), strings.Repeat("a", 64)) || !strings.Contains(response.Body.String(), strings.Repeat("b", 64)) {
		t.Fatalf("pristine list lost legacy snapshots: %s", response.Body.String())
	}
}

func TestSnapshotListExactUsesLinkTagAndIntersectsCommittedTaskPoints(t *testing.T) {
	allowed := strings.Repeat("a", 64)
	session := &snapshotLineageSession{mode: publication.LineageExact, linkTag: "xirang.link.v1.11111111111111111111111111111111", points: []publication.CommittedPoint{{RecoveryPointID: strings.Repeat("1", 32), FullNativeID: allowed}}}
	restic := &snapshotResticFake{taggedSnapshots: []executor.ResticSnapshot{{ID: allowed}, {ID: strings.Repeat("b", 64)}, {ID: allowed}}}
	router, taskEntity := setupSnapshotHandlerRouter(t, session, restic)

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/tasks/%d/snapshots", taskEntity.ID), nil))
	if response.Code != http.StatusOK || restic.listCalls != 0 || restic.taggedCalls != 1 || restic.linkTag != session.linkTag {
		t.Fatalf("exact snapshot list status=%d list=%d tagged=%d tag=%q body=%s", response.Code, restic.listCalls, restic.taggedCalls, restic.linkTag, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), allowed) || strings.Contains(response.Body.String(), strings.Repeat("b", 64)) {
		t.Fatalf("exact list leaked a foreign snapshot: %s", response.Body.String())
	}
}

func TestSnapshotListNeverReturnsOtherTaskManualOrUncommittedSnapshot(t *testing.T) {
	allowed := strings.Repeat("a", 64)
	session := &snapshotLineageSession{mode: publication.LineageExact, linkTag: "xirang.link.v1.11111111111111111111111111111111", points: []publication.CommittedPoint{{RecoveryPointID: strings.Repeat("1", 32), FullNativeID: allowed}}}
	restic := &snapshotResticFake{taggedSnapshots: []executor.ResticSnapshot{{ID: allowed}, {ID: strings.Repeat("b", 64)}, {ID: strings.Repeat("c", 64)}}}
	router, taskEntity := setupSnapshotHandlerRouter(t, session, restic)

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/tasks/%d/snapshots", taskEntity.ID), nil))
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), strings.Repeat("b", 64)) || strings.Contains(response.Body.String(), strings.Repeat("c", 64)) {
		t.Fatalf("exact list returned unowned/manual/uncommitted snapshot: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestSnapshotFilesResolvesShortPrefixOnlyInsideTaskCommittedSet(t *testing.T) {
	fullID := strings.Repeat("a", 64)
	session := &snapshotLineageSession{mode: publication.LineageExact, points: []publication.CommittedPoint{{RecoveryPointID: strings.Repeat("1", 32), FullNativeID: fullID}}}
	restic := &snapshotResticFake{entries: []executor.ResticEntry{{Name: "file", Path: "/file"}}}
	router, taskEntity := setupSnapshotHandlerRouter(t, session, restic)

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/tasks/%d/snapshots/%s/files?path=/", taskEntity.ID, fullID[:12]), nil))
	if response.Code != http.StatusOK || restic.filesCalls != 1 || restic.snapshotID != fullID {
		t.Fatalf("exact files status=%d calls=%d id=%q body=%s", response.Code, restic.filesCalls, restic.snapshotID, response.Body.String())
	}
}

func TestSnapshotFilesRejectsAmbiguousCrossTaskAndUnknownPrefixBeforeProvider(t *testing.T) {
	fullID := strings.Repeat("a", 64)
	session := &snapshotLineageSession{mode: publication.LineageExact, points: []publication.CommittedPoint{{RecoveryPointID: strings.Repeat("1", 32), FullNativeID: fullID}}, resolveErr: errors.New("not owned")}
	restic := &snapshotResticFake{}
	router, taskEntity := setupSnapshotHandlerRouter(t, session, restic)

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/tasks/%d/snapshots/%s/files", taskEntity.ID, strings.Repeat("b", 12)), nil))
	if response.Code != http.StatusBadRequest || restic.filesCalls != 0 {
		t.Fatalf("unowned prefix reached provider: status=%d calls=%d body=%s", response.Code, restic.filesCalls, response.Body.String())
	}
}

func TestSnapshotRestoreUsesResolvedFullIDAndHoldsAdmissionThroughJoinAndResponse(t *testing.T) {
	fullID := strings.Repeat("a", 64)
	session := &snapshotLineageSession{mode: publication.LineageExact, points: []publication.CommittedPoint{{RecoveryPointID: strings.Repeat("1", 32), FullNativeID: fullID}}}
	restic := &snapshotResticFake{restoreStarted: make(chan struct{}), restoreRelease: make(chan struct{})}
	router, taskEntity := setupSnapshotHandlerRouter(t, session, restic)

	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, fmt.Sprintf("/tasks/%d/snapshots/%s/restore", taskEntity.ID, fullID[:12]), strings.NewReader(`{"includes":["/file"],"targetPath":"/tmp/recovery"}`)))
		close(done)
	}()
	select {
	case <-restic.restoreStarted:
	case <-time.After(time.Second):
		t.Fatal("restore did not reach the provider")
	}
	if atomic.LoadInt32(&session.closed) != 0 {
		t.Fatal("lineage admission closed before restore command joined")
	}
	close(restic.restoreRelease)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("restore response did not finish")
	}
	if response.Code != http.StatusOK || restic.restoreCalls != 1 || restic.snapshotID != fullID || atomic.LoadInt32(&session.closed) != 1 {
		t.Fatalf("restore status=%d calls=%d id=%q closes=%d body=%s", response.Code, restic.restoreCalls, restic.snapshotID, atomic.LoadInt32(&session.closed), response.Body.String())
	}
}

func TestSnapshotHandlersRollbackSafeDisabledKeepsExactGuard(t *testing.T) {
	session := &snapshotLineageSession{mode: publication.LineageExact, linkTag: "xirang.link.v1.11111111111111111111111111111111"}
	restic := &snapshotResticFake{taggedSnapshots: []executor.ResticSnapshot{{ID: strings.Repeat("b", 64)}}}
	router, taskEntity := setupSnapshotHandlerRouter(t, session, restic)

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/tasks/%d/snapshots", taskEntity.ID), nil))
	if response.Code != http.StatusOK || restic.listCalls != 0 || restic.taggedCalls != 1 || strings.Contains(response.Body.String(), strings.Repeat("b", 64)) {
		t.Fatalf("rollback-safe disabled list reopened legacy results: status=%d list=%d tagged=%d body=%s", response.Code, restic.listCalls, restic.taggedCalls, response.Body.String())
	}
}

type snapshotLineageSession struct {
	mode       publication.LineageMode
	linkTag    string
	points     []publication.CommittedPoint
	resolveErr error
	closed     int32
}

func (session *snapshotLineageSession) Mode() publication.LineageMode { return session.mode }
func (*snapshotLineageSession) RepositoryID() string                  { return strings.Repeat("1", 32) }
func (session *snapshotLineageSession) LinkTag() string               { return session.linkTag }
func (session *snapshotLineageSession) CommittedPoints() []publication.CommittedPoint {
	return append([]publication.CommittedPoint(nil), session.points...)
}
func (session *snapshotLineageSession) ResolveNativeID(prefix string) (string, error) {
	if session.resolveErr != nil {
		return "", session.resolveErr
	}
	for _, point := range session.points {
		if strings.HasPrefix(point.FullNativeID, strings.ToLower(prefix)) {
			return point.FullNativeID, nil
		}
	}
	return "", errors.New("unknown snapshot")
}
func (*snapshotLineageSession) CurrentAndPrevious(string) (publication.CommittedPoint, *publication.CommittedPoint, error) {
	return publication.CommittedPoint{}, nil, errors.New("not used")
}
func (*snapshotLineageSession) ListEntries(context.Context, string, provider.EntryLocator, provider.PageRequest) (provider.EntryPage, error) {
	return provider.EntryPage{}, errors.New("not used")
}
func (session *snapshotLineageSession) Close() error {
	atomic.AddInt32(&session.closed, 1)
	return nil
}

type snapshotGuardFake struct {
	session   publication.LineageSession
	operation publication.ResticOperation
}

func (guard *snapshotGuardFake) Begin(_ context.Context, _ uint, operation publication.ResticOperation) (publication.LineageSession, error) {
	guard.operation = operation
	return guard.session, nil
}

type snapshotResticFake struct {
	snapshots       []executor.ResticSnapshot
	taggedSnapshots []executor.ResticSnapshot
	entries         []executor.ResticEntry
	listCalls       int
	taggedCalls     int
	filesCalls      int
	restoreCalls    int
	linkTag         string
	snapshotID      string
	restoreStarted  chan struct{}
	restoreRelease  chan struct{}
}

func (fake *snapshotResticFake) ListSnapshots(context.Context, model.Task) ([]executor.ResticSnapshot, error) {
	fake.listCalls++
	return append([]executor.ResticSnapshot(nil), fake.snapshots...), nil
}
func (fake *snapshotResticFake) ListSnapshotsByLinkTag(_ context.Context, _ model.Task, tag string) ([]executor.ResticSnapshot, error) {
	fake.taggedCalls++
	fake.linkTag = tag
	return append([]executor.ResticSnapshot(nil), fake.taggedSnapshots...), nil
}
func (fake *snapshotResticFake) ListFiles(_ context.Context, _ model.Task, snapshotID, _ string) ([]executor.ResticEntry, error) {
	fake.filesCalls++
	fake.snapshotID = snapshotID
	return append([]executor.ResticEntry(nil), fake.entries...), nil
}
func (fake *snapshotResticFake) RestoreFiles(_ context.Context, _ model.Task, snapshotID string, _ []string, _ string) error {
	fake.restoreCalls++
	fake.snapshotID = snapshotID
	if fake.restoreStarted != nil {
		close(fake.restoreStarted)
	}
	if fake.restoreRelease != nil {
		<-fake.restoreRelease
	}
	return nil
}

func setupSnapshotHandlerRouter(t *testing.T, session publication.LineageSession, restic LegacyResticSnapshots) (*gin.Engine, model.Task) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared&_loc=UTC", strings.ReplaceAll(t.Name(), "/", "_"))), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Task{}, &model.Node{}, &model.SSHKey{}, &model.CredentialAuditEvent{}); err != nil {
		t.Fatal(err)
	}
	taskEntity := model.Task{Name: "restic-snapshots", ExecutorType: "restic", Status: "pending"}
	if err := db.Create(&taskEntity).Error; err != nil {
		t.Fatal(err)
	}
	handler := NewSnapshotHandler(db, &snapshotGuardFake{session: session}, restic)
	router := gin.New()
	router.GET("/tasks/:id/snapshots", handler.ListSnapshots)
	router.GET("/tasks/:id/snapshots/:sid/files", handler.ListFiles)
	router.POST("/tasks/:id/snapshots/:sid/restore", handler.Restore)
	return router, taskEntity
}

var _ publication.LineageSession = (*snapshotLineageSession)(nil)
var _ publication.LineageGuard = (*snapshotGuardFake)(nil)
var _ LegacyResticSnapshots = (*snapshotResticFake)(nil)
