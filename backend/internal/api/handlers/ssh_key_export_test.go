package handlers

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/csv"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"
	"xirang/backend/internal/sshutil"

	"golang.org/x/crypto/ssh"
)

func TestSSHKeyExportPreservesLegacyFieldsAndStandardFingerprintAcrossEncodings(t *testing.T) {
	db := openSSHKeyHandlerTestDB(t)
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)

	// One generated fixture key, represented three ways: SSH identity is stable,
	// while the historical digest remains specific to its stored private text.
	privateKey, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal("generate fixture RSA key:", err)
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal("encode fixture PKCS#8 key:", err)
	}
	openSSH, err := ssh.MarshalPrivateKey(privateKey, "")
	if err != nil {
		t.Fatal("encode fixture OpenSSH key:", err)
	}
	publicKey, err := ssh.NewPublicKey(&privateKey.PublicKey)
	if err != nil {
		t.Fatal("encode fixture public key:", err)
	}
	expectedPublic := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(publicKey)))
	expectedFingerprint := ssh.FingerprintSHA256(publicKey)
	encodings := []string{
		string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)})),
		string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8})),
		string(pem.EncodeToMemory(openSSH)),
		"FAKE_INVALID_PRIVATE_KEY_FOR_TEST_ONLY",
		"",
	}
	names := []string{"rsa-pkcs1", "rsa-pkcs8", "rsa-openssh", "legacy,\"invalid\"", "legacy-empty"}
	createdAt := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	updatedAt := createdAt.Add(time.Hour)
	keys := make([]model.SSHKey, 0, len(encodings))
	legacyDigests := make(map[string]bool)
	for i, material := range encodings {
		key := model.SSHKey{
			Name: names[i], Username: "fixture-user", KeyType: "rsa", PrivateKey: material,
			Fingerprint: generateFingerprint(sshutil.NormalizePrivateKeyMaterial(material)),
			CreatedAt:   createdAt, UpdatedAt: updatedAt,
		}
		if err := db.Create(&key).Error; err != nil {
			t.Fatal("store fixture SSH key:", err)
		}
		keys = append(keys, key)
		if i < 3 {
			legacyDigests[key.Fingerprint] = true
		}
	}
	if len(legacyDigests) != 3 {
		t.Fatal("different private encodings must retain different historical digests")
	}
	router := newSSHKeyHandlerRouter(db, "admin", 1)

	t.Run("csv appends one column without changing existing values or BOM", func(t *testing.T) {
		resp := requestSSHKeyVisibility(router, http.MethodGet, "/ssh-keys/export?format=csv")
		if resp.Code != http.StatusOK {
			t.Fatalf("CSV status=%d", resp.Code)
		}
		bom := []byte{0xEF, 0xBB, 0xBF}
		if !bytes.HasPrefix(resp.Body.Bytes(), bom) {
			t.Fatal("CSV must retain its UTF-8 BOM")
		}
		rows, err := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(resp.Body.Bytes(), bom))).ReadAll()
		if err != nil {
			t.Fatal("parse exported CSV:", err)
		}
		header := []string{"id", "name", "username", "key_type", "fingerprint", "public_key", "created_at", "updated_at", "public_key_fingerprint"}
		if len(rows) != len(keys)+1 || !reflect.DeepEqual(rows[0], header) {
			t.Fatal("CSV must retain all rows and the legacy header order with the standard fingerprint appended")
		}
		for i, key := range keys {
			public, fingerprint := "", ""
			if i < 3 {
				public, fingerprint = expectedPublic, expectedFingerprint
			}
			want := []string{strconv.FormatUint(uint64(key.ID), 10), key.Name, key.Username, key.KeyType, key.Fingerprint, public, createdAt.Format(time.RFC3339), updatedAt.Format(time.RFC3339), fingerprint}
			if !reflect.DeepEqual(rows[i+1], want) {
				t.Errorf("CSV row %d changed legacy values or public-key identity", i)
			}
		}
	})

	t.Run("json matches GET and omits unavailable public fields", func(t *testing.T) {
		resp := requestSSHKeyVisibility(router, http.MethodGet, "/ssh-keys/export?format=json")
		if resp.Code != http.StatusOK {
			t.Fatalf("JSON status=%d", resp.Code)
		}
		var exported []map[string]json.RawMessage
		if err := json.Unmarshal(resp.Body.Bytes(), &exported); err != nil {
			t.Fatal("parse exported JSON:", err)
		}
		if len(exported) != len(keys) {
			t.Fatalf("JSON rows=%d, want %d", len(exported), len(keys))
		}
		for i, key := range keys {
			get := requestSSHKeyVisibility(router, http.MethodGet, fmt.Sprintf("/ssh-keys/%d", key.ID))
			if get.Code != http.StatusOK {
				t.Fatalf("GET row %d status=%d", i, get.Code)
			}
			var envelope struct {
				Data map[string]json.RawMessage `json:"data"`
			}
			if err := json.Unmarshal(get.Body.Bytes(), &envelope); err != nil {
				t.Fatal("parse GET response:", err)
			}
			if !reflect.DeepEqual(exported[i], envelope.Data) {
				t.Errorf("exported JSON row %d differs from GET", i)
			}
			var digest string
			if err := json.Unmarshal(exported[i]["fingerprint"], &digest); err != nil || digest != key.Fingerprint {
				t.Errorf("JSON row %d must retain its historical digest", i)
			}
			if _, present := exported[i]["private_key"]; present {
				t.Fatal("JSON must not expose private key material")
			}
			if i < 3 {
				var fingerprint, public string
				if err := json.Unmarshal(exported[i]["public_key_fingerprint"], &fingerprint); err != nil || fingerprint != expectedFingerprint {
					t.Errorf("JSON row %d must share the standard RSA fingerprint", i)
				}
				if err := json.Unmarshal(exported[i]["public_key"], &public); err != nil || public != expectedPublic {
					t.Errorf("JSON row %d must share the real RSA public key", i)
				}
			} else {
				for _, field := range []string{"public_key", "public_key_fingerprint"} {
					if _, present := exported[i][field]; present {
						t.Errorf("legacy row %d must omit unavailable %s", i, field)
					}
				}
			}
		}
	})

	t.Run("authorized_keys contains only real public key lines and existing name comments", func(t *testing.T) {
		resp := requestSSHKeyVisibility(router, http.MethodGet, "/ssh-keys/export?format=authorized_keys")
		if resp.Code != http.StatusOK {
			t.Fatalf("authorized_keys status=%d", resp.Code)
		}
		lines := strings.Split(strings.TrimSpace(resp.Body.String()), "\n")
		if len(lines) != 3 {
			t.Fatalf("authorized_keys lines=%d, want 3", len(lines))
		}
		for i, line := range lines {
			parsed, comment, _, rest, err := ssh.ParseAuthorizedKey([]byte(line))
			if err != nil || len(rest) != 0 {
				t.Fatalf("line %d must be a real authorized key", i)
			}
			if ssh.FingerprintSHA256(parsed) != expectedFingerprint || comment != keys[i].Name {
				t.Errorf("line %d must retain public-key identity and its name comment", i)
			}
			if line != expectedPublic+" "+keys[i].Name {
				t.Errorf("line %d changed the existing authorized_keys format", i)
			}
		}
	})
}
