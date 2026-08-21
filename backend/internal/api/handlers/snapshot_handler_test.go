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

func TestLegacySnapshotReadsAreGoneWithoutProviderIO(t *testing.T) {
	session := &snapshotLineageSession{mode: publication.LineageCompatibility}
	restic := &snapshotResticFake{snapshots: []executor.ResticSnapshot{{ID: strings.Repeat("a", 64)}}}
	router, taskEntity := setupSnapshotHandlerRouter(t, session, restic)

	for _, path := range []string{
		fmt.Sprintf("/tasks/%d/snapshots", taskEntity.ID),
		fmt.Sprintf("/tasks/%d/snapshots/%s/files?path=/", taskEntity.ID, strings.Repeat("a", 12)),
	} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusGone || !strings.Contains(response.Body.String(), "备份资产") {
			t.Fatalf("legacy read %s status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
	if restic.listCalls != 0 || restic.taggedCalls != 0 || restic.filesCalls != 0 {
		t.Fatalf("retired reads reached provider list=%d tagged=%d files=%d", restic.listCalls, restic.taggedCalls, restic.filesCalls)
	}
}

func TestSnapshotRestoreRequiresFeatureLive(t *testing.T) {
	fullID := strings.Repeat("a", 64)
	session := &snapshotLineageSession{mode: publication.LineageExact, points: []publication.CommittedPoint{{RecoveryPointID: strings.Repeat("1", 32), FullNativeID: fullID}}}
	restic := &snapshotResticFake{}
	router, taskEntity := setupSnapshotHandlerRouterWithLive(t, session, restic, func() (bool, error) { return false, nil })

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, fmt.Sprintf("/tasks/%d/snapshots/%s/restore", taskEntity.ID, fullID[:12]), strings.NewReader(`{"includes":["/file"],"targetPath":"/tmp/recovery"}`)))
	if response.Code != http.StatusForbidden || restic.restoreCalls != 0 {
		t.Fatalf("not-live restore status=%d calls=%d body=%s", response.Code, restic.restoreCalls, response.Body.String())
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
	return setupSnapshotHandlerRouterWithLive(t, session, restic, func() (bool, error) { return true, nil })
}

func setupSnapshotHandlerRouterWithLive(t *testing.T, session publication.LineageSession, restic LegacyResticSnapshots, featureLive func() (bool, error)) (*gin.Engine, model.Task) {
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
	handler := NewSnapshotHandler(db, &snapshotGuardFake{session: session}, restic).WithFeatureLive(featureLive)
	router := gin.New()
	router.GET("/tasks/:id/snapshots", handler.ListSnapshots)
	router.GET("/tasks/:id/snapshots/:sid/files", handler.ListFiles)
	router.POST("/tasks/:id/snapshots/:sid/restore", handler.Restore)
	return router, taskEntity
}

var _ publication.LineageSession = (*snapshotLineageSession)(nil)
var _ publication.LineageGuard = (*snapshotGuardFake)(nil)
var _ LegacyResticSnapshots = (*snapshotResticFake)(nil)
