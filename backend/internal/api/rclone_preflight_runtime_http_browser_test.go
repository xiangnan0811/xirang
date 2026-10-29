package api

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/model"
)

type rcloneBrowserIdentity struct {
	ID       uint   `json:"id"`
	Username string `json:"username"`
	Token    string `json:"token"`
}

type rcloneBrowserGate struct {
	mu      sync.Mutex
	taskID  uint
	claimed bool
	held    bool
	release chan struct{}
}

func (gate *rcloneBrowserGate) arm(id uint) bool {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if gate.release != nil {
		return false
	}
	gate.taskID, gate.claimed, gate.held, gate.release = id, false, false, make(chan struct{})
	return true
}

func (gate *rcloneBrowserGate) take(id uint) chan struct{} {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if gate.release == nil || gate.taskID != id || gate.claimed {
		return nil
	}
	gate.claimed = true
	return gate.release
}

func (gate *rcloneBrowserGate) markHeld(release chan struct{}) {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if gate.release == release {
		gate.held = true
	}
}

func (gate *rcloneBrowserGate) state() (uint, bool) {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	return gate.taskID, gate.held
}

func (gate *rcloneBrowserGate) releaseTask(id uint) bool {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if gate.release == nil || gate.taskID != id || !gate.held {
		return false
	}
	close(gate.release)
	gate.taskID, gate.claimed, gate.held, gate.release = 0, false, false, nil
	return true
}

func (gate *rcloneBrowserGate) stop() {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if gate.release != nil {
		close(gate.release)
		gate.taskID, gate.claimed, gate.held, gate.release = 0, false, false, nil
	}
}

func rcloneBrowserTaskID(path string) uint {
	const prefix, suffix = "/api/v1/tasks/", "/rclone-versioning/preflights"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return 0
	}
	id, err := strconv.ParseUint(strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix), 10, 32)
	if err != nil {
		return 0
	}
	return uint(id)
}

// TestRclonePreflightBrowserFixture is a deliberately opt-in, isolated server.
// Its authenticated control surface is test-only and never enters production.
func TestRclonePreflightBrowserFixture(t *testing.T) {
	dir := os.Getenv("XIRANG_RCLONE_BROWSER_FIXTURE_DIR")
	if dir == "" {
		t.Skip("XIRANG_RCLONE_BROWSER_FIXTURE_DIR is not set")
	}
	absolute, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal("invalid browser fixture directory")
	}
	info, err := os.Lstat(absolute)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		t.Fatal("browser fixture requires an existing private directory")
	}
	if os.Getenv("RCLONE_TEST_BINARY") == "" {
		t.Fatal("RCLONE_TEST_BINARY is required for the browser fixture")
	}
	t.Setenv("TMPDIR", absolute)
	binary := requireRealRclone(t)
	node := startRcloneHTTPSSH(t)
	boundary := newRcloneHTTPNativeBoundary(provider.RcloneNativeSSES3V1)
	fixture := newRcloneHTTPFixture(t, binary, boundary.dependencies(t, binary, true))

	source, legacy, managed := makeRcloneHTTPPaths(t)
	portableID := fixture.addTask(node, "portable-browser-legacy", source, legacy)
	config, managedLocator := rcloneSFTPConfig(node), "archive:"+managed
	managedSource, managedLegacy, managedRoot := makeRcloneHTTPPaths(t)
	managedID := fixture.addTask(node, "portable-browser-managed", managedSource, managedLegacy)
	managedRevision := fixture.portableCandidate(managedID, config, "archive:"+managedRoot)
	ready := fixture.preflight(managedID, managedRevision, backupasset.PublicationVersionedPrefix)
	if ready.Purpose != backupasset.RclonePreflightPurposeActivation || ready.Summary.State != backupasset.RcloneStateReady {
		t.Fatal("browser managed task did not complete the real portable preflight")
	}
	fixture.activate(managedID, managedRevision, ready.PreflightID, http.StatusOK)

	nativeSource, nativeLegacy, _ := makeRcloneHTTPPaths(t)
	nativeID := fixture.addTask(node, "native-browser", nativeSource, nativeLegacy)
	identity := func(username, role string) rcloneBrowserIdentity {
		t.Helper()
		user := model.User{Username: username, PasswordHash: fixture.admin.PasswordHash, Role: role, Onboarded: true}
		if err := fixture.db.Create(&user).Error; err != nil {
			t.Fatal("create browser identity")
		}
		token, err := fixture.jwt.GenerateToken(user)
		if err != nil {
			t.Fatal("create browser identity token")
		}
		return rcloneBrowserIdentity{ID: user.ID, Username: user.Username, Token: token}
	}
	alternate := identity("rclone-fixture-second-admin", "admin")
	viewer := identity("rclone-fixture-viewer", "viewer")

	random := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, random); err != nil {
		t.Fatal("create browser fixture control token")
	}
	controlToken := hex.EncodeToString(random)
	gate := &rcloneBrowserGate{}
	stop := make(chan struct{})
	var stopOnce sync.Once
	validTask := func(id uint) bool { return id == portableID || id == managedID || id == nativeID }
	var advanced time.Duration
	var advanceMu sync.Mutex
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/__fixture/") {
			provided := r.Header.Get("X-Fixture-Token")
			if subtle.ConstantTimeCompare([]byte(provided), []byte(controlToken)) != 1 {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			switch {
			case r.URL.Path == "/__fixture/advance" && r.Method == http.MethodPost:
				var payload struct {
					Duration string `json:"duration"`
				}
				decoder := json.NewDecoder(io.LimitReader(r.Body, 1025))
				decoder.DisallowUnknownFields()
				if decoder.Decode(&payload) != nil {
					http.Error(w, "invalid request", http.StatusBadRequest)
					return
				}
				delta, err := time.ParseDuration(payload.Duration)
				advanceMu.Lock()
				defer advanceMu.Unlock()
				if err != nil || delta <= 0 || delta > 2*time.Hour || advanced+delta > 7*24*time.Hour {
					http.Error(w, "invalid duration", http.StatusBadRequest)
					return
				}
				fixture.clock.Advance(delta)
				advanced += delta
				_ = json.NewEncoder(w).Encode(map[string]any{"now": fixture.clock.Now()})
			case r.URL.Path == "/__fixture/hold" && r.Method == http.MethodGet:
				id, held := gate.state()
				_ = json.NewEncoder(w).Encode(map[string]any{"task_id": id, "held": held})
			case (r.URL.Path == "/__fixture/hold" || r.URL.Path == "/__fixture/release") && r.Method == http.MethodPost:
				var payload struct {
					TaskID uint `json:"task_id"`
				}
				decoder := json.NewDecoder(io.LimitReader(r.Body, 1025))
				decoder.DisallowUnknownFields()
				if decoder.Decode(&payload) != nil || !validTask(payload.TaskID) {
					http.Error(w, "invalid task", http.StatusBadRequest)
					return
				}
				ok := false
				if r.URL.Path == "/__fixture/hold" {
					ok = gate.arm(payload.TaskID)
				} else {
					ok = gate.releaseTask(payload.TaskID)
				}
				if !ok {
					http.Error(w, "gate conflict", http.StatusConflict)
					return
				}
				w.WriteHeader(http.StatusNoContent)
			case r.URL.Path == "/__fixture/shutdown" && r.Method == http.MethodPost:
				gate.stop()
				w.WriteHeader(http.StatusNoContent)
				stopOnce.Do(func() { close(stop) })
			default:
				http.NotFound(w, r)
			}
			return
		}
		var release chan struct{}
		if r.Method == http.MethodPost {
			release = gate.take(rcloneBrowserTaskID(r.URL.Path))
		}
		if release == nil {
			fixture.router.ServeHTTP(w, r)
			return
		}
		response := httptest.NewRecorder()
		fixture.router.ServeHTTP(response, r)
		gate.markHeld(release)
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		for key, values := range response.Header() {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(response.Code)
		_, _ = w.Write(response.Body.Bytes())
	})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("listen for browser fixture")
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	serverErr := make(chan error, 1)
	go func() { serverErr <- server.Serve(listener) }()
	t.Cleanup(func() {
		gate.stop()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	})

	descriptor := map[string]any{
		"url": "http://" + listener.Addr().String(), "control_token": controlToken,
		"admin":           rcloneBrowserIdentity{ID: fixture.admin.ID, Username: fixture.admin.Username, Token: fixture.token},
		"alternate_admin": alternate, "viewer": viewer,
		"portable": map[string]any{"legacy_task_id": portableID, "managed_task_id": managedID, "target_remote": "archive", "managed_root_locator": managedLocator, "bound_config": config},
		"native":   map[string]any{"task_id": nativeID, "region": "us-east-1", "bucket": rcloneNativeHTTPBucket, "managed_prefix": rcloneNativeHTTPPrefix, "role_arn": rcloneNativeHTTPRole, "bootstrap_mode": "workload_chain"},
	}
	file, err := os.OpenFile(filepath.Join(absolute, "descriptor.json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal("create private browser descriptor")
	}
	encodeErr := json.NewEncoder(file).Encode(descriptor)
	closeErr := file.Close()
	if encodeErr != nil || closeErr != nil {
		t.Fatal("write private browser descriptor")
	}
	t.Log("Rclone browser fixture ready")
	select {
	case <-stop:
	case err := <-serverErr:
		if !errors.Is(err, http.ErrServerClosed) {
			t.Fatal("browser fixture server stopped unexpectedly")
		}
	}
}
