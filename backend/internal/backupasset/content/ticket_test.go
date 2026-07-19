package content

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/model"
)

func TestTicketMaterialSeparatesIDsAndStoresOnlySecretHash(t *testing.T) {
	seed := append(bytes.Repeat([]byte{0x11}, 16), bytes.Repeat([]byte{0x22}, 16)...)
	seed = append(seed, bytes.Repeat([]byte{0x33}, 32)...)
	material, err := newTicketMaterialFrom(bytes.NewReader(seed))
	if err != nil {
		t.Fatalf("newTicketMaterialFrom() error = %v", err)
	}
	if len(material.GrantID) != 32 || len(material.DeliveryID) != 32 || material.GrantID == material.DeliveryID {
		t.Fatalf("ticket IDs are not independent opaque IDs: %+v", material)
	}
	if !strings.HasPrefix(material.CookieSecret, "v1.") || len(material.CookieSecretHash) != 64 {
		t.Fatalf("cookie secret/hash shape invalid: %+v", material)
	}
	if !VerifyCookieSecret(material.CookieSecretHash, material.CookieSecret) || VerifyCookieSecret(material.CookieSecretHash, material.CookieSecret+"x") {
		t.Fatal("cookie secret constant-time verification contract failed")
	}
	encoded, err := json.Marshal(material)
	if err != nil {
		t.Fatalf("marshal material: %v", err)
	}
	for _, private := range []string{material.CookieSecret, material.CookieSecretHash, material.GrantID} {
		if bytes.Contains(encoded, []byte(private)) {
			t.Fatalf("private ticket material leaked through JSON: %s", encoded)
		}
	}
}

func TestTicketMaterialRejectsIdentifierCollision(t *testing.T) {
	seed := append(bytes.Repeat([]byte{0x11}, 16), bytes.Repeat([]byte{0x11}, 16)...)
	seed = append(seed, bytes.Repeat([]byte{0x22}, 16)...)
	seed = append(seed, bytes.Repeat([]byte{0x33}, 32)...)
	material, err := newTicketMaterialFrom(bytes.NewReader(seed))
	if err != nil {
		t.Fatalf("newTicketMaterialFrom() collision retry error = %v", err)
	}
	if material.GrantID == material.DeliveryID {
		t.Fatal("grant and delivery IDs collided")
	}
}

func TestResolveGrantDeadlinesUsesEarliestUTCBoundary(t *testing.T) {
	now := time.Date(2026, 7, 18, 8, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	proof := now.Add(90 * time.Second)
	deadlines, err := ResolveGrantDeadlines(GrantDeadlineInput{
		Now:              now,
		SessionExpiresAt: now.Add(10 * time.Minute),
		ProfileExpiresAt: now.Add(2 * time.Minute),
		LeaseDeadline:    now.Add(5 * time.Minute),
		ProofExpiresAt:   &proof,
		IdleTTL:          time.Minute,
	})
	if err != nil {
		t.Fatalf("ResolveGrantDeadlines() error = %v", err)
	}
	if !deadlines.AbsoluteExpiresAt.Equal(proof.UTC()) || !deadlines.IdleExpiresAt.Equal(now.Add(time.Minute).UTC()) {
		t.Fatalf("deadlines = %+v", deadlines)
	}
	if deadlines.AbsoluteExpiresAt.Location() != time.UTC || deadlines.IdleExpiresAt.Location() != time.UTC {
		t.Fatal("deadlines must be normalized to UTC")
	}

	for _, input := range []GrantDeadlineInput{
		{},
		{Now: now, SessionExpiresAt: now, ProfileExpiresAt: now.Add(time.Minute), LeaseDeadline: now.Add(time.Minute), IdleTTL: time.Second},
		{Now: now, SessionExpiresAt: now.Add(time.Minute), ProfileExpiresAt: now.Add(time.Minute), LeaseDeadline: now.Add(time.Minute)},
	} {
		if _, err := ResolveGrantDeadlines(input); err == nil {
			t.Fatalf("invalid deadline input %+v unexpectedly succeeded", input)
		}
	}
}

func TestDeliveryModelsHaveNoJSONProjection(t *testing.T) {
	payload, err := json.Marshal(model.BackupAssetDeliveryGrant{
		ID: "internal", DeliveryID: "public", SessionJTI: "session", CookieSecretHash: "hash",
	})
	if err != nil {
		t.Fatalf("marshal delivery grant: %v", err)
	}
	if string(payload) != "{}" {
		t.Fatalf("delivery grant exposed private JSON: %s", payload)
	}
}

func TestDeliveryCookieHasExactSecurityAttributes(t *testing.T) {
	deliveryID := strings.Repeat("a", 32)
	expires := time.Date(2026, 7, 18, 9, 2, 3, 0, time.UTC)
	cookie, err := NewDeliveryCookie(deliveryID, "v1."+strings.Repeat("A", 43), expires, true)
	if err != nil {
		t.Fatalf("NewDeliveryCookie() error = %v", err)
	}
	if cookie.Name != DeliveryCookieName || cookie.Path != "/api/v1/asset-content/"+deliveryID || cookie.Domain != "" ||
		!cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteStrictMode || !cookie.Expires.Equal(expires) {
		t.Fatalf("cookie attributes = %+v", cookie)
	}
}

func TestParseDeliveryCookieRejectsDuplicatesAndMalformedValues(t *testing.T) {
	valid := "v1." + strings.Repeat("A", 43)
	if got, err := ParseDeliveryCookie(DeliveryCookieName + "=" + valid); err != nil || got != valid {
		t.Fatalf("ParseDeliveryCookie() got=%q err=%v", got, err)
	}
	for _, header := range []string{
		DeliveryCookieName + "=" + valid + "; " + DeliveryCookieName + "=" + valid,
		DeliveryCookieName + "=bad",
		DeliveryCookieName + "=" + valid + "x",
		"other=value",
	} {
		if _, err := ParseDeliveryCookie(header); err == nil {
			t.Fatalf("malformed/duplicate cookie accepted: %q", header)
		}
	}
}
