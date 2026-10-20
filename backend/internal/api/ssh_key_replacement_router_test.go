package api

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/auth"
	"xirang/backend/internal/database"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"
	"xirang/backend/internal/sshutil"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/ssh"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

const sshKeyReplacementRouterSecret = "FAKE_SSH_REPLACEMENT_ROUTER_SIGNING_SECRET_FOR_TEST_ONLY"
const sshKeyReplacementRouterAction = auth.StepUpAction("ssh_key.replace")

type sshKeyReplacementRouterActor struct {
	user    model.User
	token   string
	session *auth.Claims
}

type sshKeyReplacementRouterFixture struct {
	db      *gorm.DB
	manager *auth.JWTManager
	router  *gin.Engine
	actors  map[string]sshKeyReplacementRouterActor
}

type sshKeyReplacementRouterRoute struct {
	name   string
	method string
	suffix string
}

var sshKeyReplacementRouterRoutes = []sshKeyReplacementRouterRoute{
	{name: "update", method: http.MethodPut},
	{name: "rotate", method: http.MethodPost, suffix: "/rotate"},
}

// A distinct type deliberately has no SSHKey AfterFind hook: snapshots include
// the stored ciphertext, not a decrypted/re-encrypted approximation of it.
type sshKeyReplacementStoredKey model.SSHKey

func newSSHKeyReplacementRouterFixture(t *testing.T) sshKeyReplacementRouterFixture {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_SSH_REPLACEMENT_ROUTER_DATA_KEY_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)
	gin.SetMode(gin.TestMode)
	dsn := filepath.Join(t.TempDir(), "ssh-replacement.sqlite") + "?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=on&_loc=UTC"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open replacement router database: %v", err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal("get replacement router database pool")
	}
	pool.SetMaxOpenConns(4)
	t.Cleanup(func() { _ = pool.Close() })
	if err := database.RunMigrations(db, "sqlite"); err != nil {
		t.Fatalf("migrate replacement router database: %v", err)
	}
	manager := auth.NewJWTManager(sshKeyReplacementRouterSecret, time.Hour)
	manager.SetDB(db)
	fixture := sshKeyReplacementRouterFixture{db: db, manager: manager, actors: make(map[string]sshKeyReplacementRouterActor)}
	for _, entry := range []struct {
		name string
		role string
		totp bool
	}{
		{"admin", "admin", true},
		{"other-admin", "admin", true},
		{"no-totp", "admin", false},
		{"operator", "operator", true},
		{"viewer", "viewer", true},
	} {
		user := model.User{
			Username: "ssh-replacement-" + entry.name, PasswordHash: "FAKE_PASSWORD_HASH_FOR_TEST_ONLY",
			Role: entry.role, TOTPEnabled: entry.totp, TokenVersion: 3,
		}
		if entry.totp {
			secret, err := auth.GenerateTOTPSecret("test", user.Username)
			if err != nil {
				t.Fatal("generate replacement router TOTP fixture")
			}
			user.TOTPSecret = secret.Secret()
		}
		if err := db.Create(&user).Error; err != nil {
			t.Fatalf("create replacement router actor: %v", err)
		}
		token, err := manager.GenerateToken(user)
		if err != nil {
			t.Fatal("generate replacement router primary token")
		}
		session, err := manager.ParseToken(token)
		if err != nil {
			t.Fatal("parse replacement router primary token")
		}
		fixture.actors[entry.name] = sshKeyReplacementRouterActor{user: user, token: token, session: session}
	}
	fixture.router = NewRouter(Dependencies{DB: db, JWTManager: manager})
	return fixture
}

func sshKeyReplacementRouterMaterial(t *testing.T) (string, map[string]string) {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal("generate replacement router key")
	}
	openSSH := func(comment string) string {
		block, err := ssh.MarshalPrivateKey(private, comment)
		if err != nil {
			t.Fatal("marshal replacement router OpenSSH key")
		}
		return string(pem.EncodeToMemory(block))
	}
	original := openSSH("original fixture comment")
	der, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		t.Fatal("marshal replacement router PKCS8 key")
	}
	return original, map[string]string{
		"same-text":                 original,
		"different-openssh-comment": openSSH("different fixture comment"),
		"crlf":                      strings.ReplaceAll(original, "\n", "\r\n"),
		"pkcs8":                     string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
	}
}

func (f sshKeyReplacementRouterFixture) key(t *testing.T, material string) sshKeyReplacementStoredKey {
	t.Helper()
	sum := sha256.Sum256([]byte(material))
	lastUsed := time.Date(2024, time.January, 2, 3, 4, 5, 0, time.UTC)
	key := model.SSHKey{
		Name: strings.ReplaceAll(t.Name(), "/", "-"), Username: "original-user", KeyType: "ed25519",
		PrivateKey: material, Fingerprint: "SHA256:" + base64.StdEncoding.EncodeToString(sum[:]),
		LastUsedAt: &lastUsed, AllowedPurposes: sshutil.PurposeSSHKeyTest,
	}
	if err := f.db.Create(&key).Error; err != nil {
		t.Fatalf("create replacement router key: %v", err)
	}
	stored := f.stored(t, key.ID)
	if material != "" && !secure.IsEncrypted(stored.PrivateKey) {
		t.Fatal("replacement fixture did not store encrypted private material")
	}
	return stored
}

func (f sshKeyReplacementRouterFixture) stored(t *testing.T, id uint) sshKeyReplacementStoredKey {
	t.Helper()
	var stored sshKeyReplacementStoredKey
	if err := f.db.Table("ssh_keys").Where("id = ?", id).Take(&stored).Error; err != nil {
		t.Fatalf("read replacement router storage snapshot: %v", err)
	}
	return stored
}

// Sign exact production-shaped claims with the fixture manager's signing key.
// Keeping this independent of the action registry lets the baseline run expose
// missing route enforcement before ssh_key.replace is registered. No middleware
// identity/session context or proof verifier is mocked.
func (f sshKeyReplacementRouterFixture) proof(t *testing.T, actor sshKeyReplacementRouterActor, mutate func(*auth.Claims)) string {
	t.Helper()
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		t.Fatal("generate replacement proof ID")
	}
	now := time.Now().UTC().Truncate(time.Second)
	claims := auth.Claims{
		UserID: actor.user.ID, Username: actor.user.Username, Role: actor.user.Role,
		TokenVersion: actor.user.TokenVersion, Purpose: auth.PurposeStepUp,
		StepUpAction: sshKeyReplacementRouterAction, SessionID: actor.session.ID,
		RegisteredClaims: jwt.RegisteredClaims{
			ID: hex.EncodeToString(id), Subject: fmt.Sprint(actor.user.ID),
			IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(5 * time.Minute)),
		},
	}
	if mutate != nil {
		mutate(&claims)
	}
	proof, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(sshKeyReplacementRouterSecret))
	if err != nil {
		t.Fatal("sign replacement router proof")
	}
	return proof
}

func (f sshKeyReplacementRouterFixture) request(t *testing.T, route sshKeyReplacementRouterRoute, id uint, token, proof string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal("marshal replacement router request")
	}
	request := httptest.NewRequest(route.method, fmt.Sprintf("/api/v1/ssh-keys/%d%s", id, route.suffix), bytes.NewReader(encoded))
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if proof != "" {
		request.Header.Set("X-Xirang-Step-Up", proof)
	}
	response := httptest.NewRecorder()
	f.router.ServeHTTP(response, request)
	if strings.Contains(response.Body.String(), "PRIVATE KEY") || proof != "" && strings.Contains(response.Body.String(), proof) {
		t.Error("replacement response disclosed private material or proof")
	}
	return response
}

func sshKeyReplacementRouterBody(route sshKeyReplacementRouterRoute, name, material string) map[string]any {
	body := map[string]any{"name": name + "-edited", "key_type": "ed25519", "private_key": material}
	if route.method == http.MethodPut {
		body["username"] = "requested-new-user"
		body["allowed_node_tags"] = "changed-scope"
	}
	return body
}

func assertSSHKeyReplacementChallenge(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	var envelope struct {
		Code int `json:"code"`
		Data struct {
			ErrorCode string `json:"error_code"`
			TTL       int    `json:"proof_ttl_seconds"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Error("replacement challenge is not a JSON envelope")
		return
	}
	if envelope.Code != http.StatusForbidden || envelope.Data.ErrorCode != "STEP_UP_REQUIRED" || envelope.Data.TTL != 300 {
		t.Error("replacement denial is not the standard 300-second step-up challenge")
	}
	if response.Header().Get("Cache-Control") != "private, no-store" {
		t.Error("replacement challenge must not be cached")
	}
}

func assertSSHKeyReplacementSuccess(t *testing.T, response *httptest.ResponseRecorder, route sshKeyReplacementRouterRoute, id uint) {
	t.Helper()
	if response.Code != http.StatusOK {
		t.Errorf("replacement HTTP status = %d, want 200", response.Code)
	}
	var envelope struct {
		Code int `json:"code"`
		Data struct {
			ID                uint   `json:"id"`
			Status            string `json:"status"`
			Fingerprint       string `json:"fingerprint"`
			PublicFingerprint string `json:"public_key_fingerprint"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal("replacement success is not a JSON envelope")
	}
	if envelope.Code != http.StatusOK {
		t.Error("replacement response is not a success envelope")
	}
	if route.method == http.MethodPut {
		if envelope.Data.ID != id || envelope.Data.Fingerprint == "" || envelope.Data.PublicFingerprint == "" {
			t.Error("replacement update did not return the safe saved key with derived public fingerprint")
		}
	} else if envelope.Data.Status != "saved" || envelope.Data.PublicFingerprint == "" {
		t.Error("zero-node replacement rotation did not return saved with a public fingerprint")
	}
	if response.Header().Get("Cache-Control") != "private, no-store" {
		t.Error("replacement success must not be cached")
	}
}

func TestSSHKeyReplacementRoutes(t *testing.T) {
	f := newSSHKeyReplacementRouterFixture(t)
	original, _ := sshKeyReplacementRouterMaterial(t)
	candidate, _ := sshKeyReplacementRouterMaterial(t)
	admin := f.actors["admin"]
	otherSessionToken, err := f.manager.GenerateToken(admin.user)
	if err != nil {
		t.Fatal("generate second primary session")
	}
	otherSession, err := f.manager.ParseToken(otherSessionToken)
	if err != nil || otherSession.ID == admin.session.ID {
		t.Fatal("second primary session must have an independent JTI")
	}
	for _, route := range sshKeyReplacementRouterRoutes {
		t.Run(route.name, func(t *testing.T) {
			for _, testCase := range []struct {
				name   string
				actor  string
				proof  func(*testing.T) string
				status int
			}{
				{name: "anonymous", status: http.StatusUnauthorized},
				{name: "operator", actor: "operator", status: http.StatusForbidden},
				{name: "viewer", actor: "viewer", status: http.StatusForbidden},
				{name: "missing-proof", actor: "admin", status: http.StatusForbidden},
				{name: "malformed-proof", actor: "admin", status: http.StatusForbidden, proof: func(*testing.T) string { return "not-a-proof" }},
				{name: "wrong-signature", actor: "admin", status: http.StatusForbidden, proof: func(t *testing.T) string {
					proof := f.proof(t, admin, nil)
					parts := strings.Split(proof, ".")
					parts[2] = base64.RawURLEncoding.EncodeToString(make([]byte, sha256.Size))
					return strings.Join(parts, ".")
				}},
				{name: "export-action", actor: "admin", status: http.StatusForbidden, proof: func(t *testing.T) string {
					return f.proof(t, admin, func(c *auth.Claims) { c.StepUpAction = auth.StepUpActionSSHKeyExport })
				}},
				{name: "import-action", actor: "admin", status: http.StatusForbidden, proof: func(t *testing.T) string {
					return f.proof(t, admin, func(c *auth.Claims) { c.StepUpAction = auth.StepUpActionConfigImport })
				}},
				{name: "expired-proof", actor: "admin", status: http.StatusForbidden, proof: func(t *testing.T) string {
					return f.proof(t, admin, func(c *auth.Claims) {
						c.IssuedAt = jwt.NewNumericDate(time.Now().UTC().Add(-6 * time.Minute).Truncate(time.Second))
						c.ExpiresAt = jwt.NewNumericDate(c.IssuedAt.Add(5 * time.Minute))
					})
				}},
				{name: "other-user", actor: "admin", status: http.StatusForbidden, proof: func(t *testing.T) string { return f.proof(t, f.actors["other-admin"], nil) }},
				{name: "same-user-other-session", actor: "admin", status: http.StatusForbidden, proof: func(t *testing.T) string {
					return f.proof(t, admin, func(c *auth.Claims) { c.SessionID = otherSession.ID })
				}},
				{name: "missing-session", actor: "admin", status: http.StatusForbidden, proof: func(t *testing.T) string {
					return f.proof(t, admin, func(c *auth.Claims) { c.SessionID = "" })
				}},
				{name: "primary-token-as-proof", actor: "admin", status: http.StatusForbidden, proof: func(*testing.T) string { return admin.token }},
				{name: "totp-disabled-missing-proof", actor: "no-totp", status: http.StatusForbidden},
				{name: "totp-disabled", actor: "no-totp", status: http.StatusForbidden, proof: func(t *testing.T) string { return f.proof(t, f.actors["no-totp"], nil) }},
			} {
				t.Run(testCase.name, func(t *testing.T) {
					before := f.key(t, original)
					proof := ""
					if testCase.proof != nil {
						proof = testCase.proof(t)
					}
					response := f.request(t, route, before.ID, f.actors[testCase.actor].token, proof, sshKeyReplacementRouterBody(route, before.Name, candidate))
					if response.Code != testCase.status {
						t.Errorf("replacement denial HTTP status = %d, want %d", response.Code, testCase.status)
					}
					if !reflect.DeepEqual(before, f.stored(t, before.ID)) {
						t.Error("denied replacement changed stored ciphertext, fingerprint, metadata, scope, or timestamps")
					}
					if testCase.actor == "admin" || testCase.actor == "no-totp" {
						assertSSHKeyReplacementChallenge(t, response)
					}
				})
			}
			t.Run("current-session-proof", func(t *testing.T) {
				before := f.key(t, original)
				response := f.request(t, route, before.ID, admin.token, f.proof(t, admin, nil), sshKeyReplacementRouterBody(route, before.Name, candidate))
				assertSSHKeyReplacementSuccess(t, response, route, before.ID)
				assertSSHKeyReplacementMaterialSaved(t, f, before, candidate)
			})
		})
	}
}

func assertSSHKeyReplacementMaterialSaved(t *testing.T, f sshKeyReplacementRouterFixture, before sshKeyReplacementStoredKey, candidate string) {
	t.Helper()
	after := f.stored(t, before.ID)
	if after.PrivateKey == before.PrivateKey || !secure.IsEncrypted(after.PrivateKey) || after.Fingerprint == before.Fingerprint {
		t.Error("authorized replacement did not update encrypted private material and fingerprint")
	}
	plaintext, err := secure.DecryptIfNeeded(after.PrivateKey)
	if err != nil {
		t.Fatal("decrypt authorized replacement")
	}
	actual, err := ssh.ParsePrivateKey([]byte(plaintext))
	if err != nil {
		t.Fatal("parse authorized replacement")
	}
	wanted, err := ssh.ParsePrivateKey([]byte(candidate))
	if err != nil {
		t.Fatal("parse expected replacement fixture")
	}
	if !bytes.Equal(actual.PublicKey().Marshal(), wanted.PublicKey().Marshal()) {
		t.Error("authorized replacement saved the wrong private material")
	}
	if after.Name != before.Name+"-edited" || !reflect.DeepEqual(after.LastUsedAt, before.LastUsedAt) || !after.CreatedAt.Equal(before.CreatedAt) {
		t.Error("authorized replacement lost submitted metadata or overwrote last-used/creation time")
	}
}

func TestSSHKeyReplacementUnchangedMaterial(t *testing.T) {
	f := newSSHKeyReplacementRouterFixture(t)
	original, equivalents := sshKeyReplacementRouterMaterial(t)
	for _, route := range sshKeyReplacementRouterRoutes {
		for _, actor := range []string{"admin", "no-totp"} {
			for encoding, material := range equivalents {
				for _, proof := range []string{"", "invalid-but-irrelevant-proof"} {
					proofName := "no-proof"
					if proof != "" {
						proofName = "invalid-proof"
					}
					t.Run(route.name+"/"+actor+"/"+encoding+"/"+proofName, func(t *testing.T) {
						before := f.key(t, original)
						body := sshKeyReplacementRouterBody(route, before.Name, material)
						// Keep the original scope: this scenario exercises only key
						// equality and metadata, not an unrelated scope restriction.
						delete(body, "allowed_node_tags")
						body["name"] = before.Name + "-edited"
						response := f.request(t, route, before.ID, f.actors[actor].token, proof, body)
						assertSSHKeyReplacementSuccess(t, response, route, before.ID)
						after := f.stored(t, before.ID)
						if after.Name != body["name"] {
							t.Error("unchanged-material request did not save metadata")
						}
						if after.PrivateKey != before.PrivateKey || after.Fingerprint != before.Fingerprint || !reflect.DeepEqual(after.LastUsedAt, before.LastUsedAt) || !after.CreatedAt.Equal(before.CreatedAt) {
							t.Error("equivalent private material rewrote ciphertext, legacy fingerprint, last-used, or creation time")
						}
					})
				}
			}
		}
	}
}

func TestSSHKeyReplacementMetadataWithoutTOTP(t *testing.T) {
	f := newSSHKeyReplacementRouterFixture(t)
	original, _ := sshKeyReplacementRouterMaterial(t)
	route := sshKeyReplacementRouterRoutes[0]
	for _, mode := range []string{"omitted", "null", "empty"} {
		for _, proof := range []string{"", "invalid-but-irrelevant-proof"} {
			proofName := "no-proof"
			if proof != "" {
				proofName = "invalid-proof"
			}
			t.Run(mode+"/"+proofName, func(t *testing.T) {
				before := f.key(t, original)
				body := map[string]any{"name": before.Name + "-edited", "username": "edited-user", "key_type": "auto"}
				switch mode {
				case "null":
					body["private_key"] = nil
				case "empty":
					body["private_key"] = ""
				}
				response := f.request(t, route, before.ID, f.actors["no-totp"].token, proof, body)
				assertSSHKeyReplacementSuccess(t, response, route, before.ID)
				after := f.stored(t, before.ID)
				if after.Name != body["name"] || after.Username != "edited-user" {
					t.Error("metadata-only edit did not save requested metadata")
				}
				if after.PrivateKey != before.PrivateKey || after.Fingerprint != before.Fingerprint || !reflect.DeepEqual(after.LastUsedAt, before.LastUsedAt) || !after.CreatedAt.Equal(before.CreatedAt) {
					t.Error("metadata-only edit rewrote stored secret, fingerprint, last-used, or creation time")
				}
			})
		}
	}
}

func TestSSHKeyReplacementHistoricalMaterial(t *testing.T) {
	f := newSSHKeyReplacementRouterFixture(t)
	candidate, _ := sshKeyReplacementRouterMaterial(t)
	admin := f.actors["admin"]
	for _, route := range sshKeyReplacementRouterRoutes {
		for _, historical := range []string{"", "damaged historical plaintext"} {
			historyName := "empty"
			if historical != "" {
				historyName = "unparseable"
			}
			t.Run(route.name+"/"+historyName, func(t *testing.T) {
				before := f.key(t, historical)
				body := sshKeyReplacementRouterBody(route, before.Name, candidate)
				response := f.request(t, route, before.ID, admin.token, "", body)
				if response.Code != http.StatusForbidden {
					t.Errorf("historical secret repair HTTP status = %d, want 403", response.Code)
				}
				assertSSHKeyReplacementChallenge(t, response)
				if !reflect.DeepEqual(before, f.stored(t, before.ID)) {
					t.Error("unauthorized historical repair changed private material, digest, or metadata")
				}
				// A distinct target keeps the positive case independent of a
				// baseline implementation incorrectly saving the denied request.
				t.Run("authorized", func(t *testing.T) {
					before := f.key(t, historical)
					response := f.request(t, route, before.ID, admin.token, f.proof(t, admin, nil), sshKeyReplacementRouterBody(route, before.Name, candidate))
					assertSSHKeyReplacementSuccess(t, response, route, before.ID)
					assertSSHKeyReplacementMaterialSaved(t, f, before, candidate)
				})
			})
		}
	}
}
