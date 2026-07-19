package content

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"xirang/backend/internal/backupasset"
)

const (
	DeliveryCookieName = "xirang_asset_delivery"
	cookieSecretBytes  = 32
	maxCookieHeaderLen = 4096
	maxIDAttempts      = 4
)

type GrantDeadlineInput struct {
	Now              time.Time
	SessionExpiresAt time.Time
	ProfileExpiresAt time.Time
	LeaseDeadline    time.Time
	ProofExpiresAt   *time.Time
	IdleTTL          time.Duration
}

type GrantDeadlines struct {
	AbsoluteExpiresAt time.Time
	IdleExpiresAt     time.Time
}

type TicketMaterial struct {
	GrantID          string `json:"-"`
	DeliveryID       string `json:"-"`
	CookieSecret     string `json:"-"`
	CookieSecretHash string `json:"-"`
}

func NewTicketMaterial() (TicketMaterial, error) {
	return newTicketMaterialFrom(rand.Reader)
}

func newTicketMaterialFrom(source io.Reader) (TicketMaterial, error) {
	grantID, err := readOpaqueID(source)
	if err != nil {
		return TicketMaterial{}, fmt.Errorf("generate grant id: %w", err)
	}
	deliveryID := ""
	for attempt := 0; attempt < maxIDAttempts; attempt++ {
		deliveryID, err = readOpaqueID(source)
		if err != nil {
			return TicketMaterial{}, fmt.Errorf("generate delivery id: %w", err)
		}
		if deliveryID != grantID {
			break
		}
	}
	if deliveryID == grantID {
		return TicketMaterial{}, fmt.Errorf("generate delivery id: repeated identifier collision")
	}
	secret := make([]byte, cookieSecretBytes)
	if _, err := io.ReadFull(source, secret); err != nil {
		return TicketMaterial{}, fmt.Errorf("generate cookie secret: %w", err)
	}
	cookieSecret := "v1." + base64.RawURLEncoding.EncodeToString(secret)
	sum := sha256.Sum256([]byte(cookieSecret))
	return TicketMaterial{
		GrantID: grantID, DeliveryID: deliveryID, CookieSecret: cookieSecret,
		CookieSecretHash: hex.EncodeToString(sum[:]),
	}, nil
}

func ResolveGrantDeadlines(input GrantDeadlineInput) (GrantDeadlines, error) {
	if input.Now.IsZero() || input.SessionExpiresAt.IsZero() || input.ProfileExpiresAt.IsZero() ||
		input.LeaseDeadline.IsZero() || input.IdleTTL <= 0 {
		return GrantDeadlines{}, ErrInvalidDeliveryProduct
	}
	now := input.Now.UTC()
	boundaries := []time.Time{
		input.SessionExpiresAt.UTC(), input.ProfileExpiresAt.UTC(), input.LeaseDeadline.UTC(),
	}
	if input.ProofExpiresAt != nil {
		boundaries = append(boundaries, input.ProofExpiresAt.UTC())
	}
	absolute := boundaries[0]
	for _, boundary := range boundaries {
		if !boundary.After(now) {
			return GrantDeadlines{}, ErrInvalidDeliveryProduct
		}
		if boundary.Before(absolute) {
			absolute = boundary
		}
	}
	idle := now.Add(input.IdleTTL)
	if idle.After(absolute) {
		idle = absolute
	}
	return GrantDeadlines{AbsoluteExpiresAt: absolute, IdleExpiresAt: idle}, nil
}

func VerifyCookieSecret(expectedHash, cookieSecret string) bool {
	if !validCookieSecret(cookieSecret) || len(expectedHash) != sha256.Size*2 || strings.ToLower(expectedHash) != expectedHash {
		return false
	}
	expected, err := hex.DecodeString(expectedHash)
	if err != nil || len(expected) != sha256.Size {
		return false
	}
	actual := sha256.Sum256([]byte(cookieSecret))
	return subtle.ConstantTimeCompare(expected, actual[:]) == 1
}

func NewDeliveryCookie(deliveryID, secret string, expiresAt time.Time, secure bool) (*http.Cookie, error) {
	if backupasset.ValidateOpaqueID(deliveryID) != nil || !validCookieSecret(secret) || expiresAt.IsZero() {
		return nil, ErrInvalidDeliveryCookie
	}
	return &http.Cookie{
		Name: DeliveryCookieName, Value: secret,
		Path:    "/api/v1/asset-content/" + deliveryID,
		Expires: expiresAt.UTC(), HttpOnly: true, Secure: secure,
		SameSite: http.SameSiteStrictMode,
	}, nil
}

func ParseDeliveryCookie(rawHeader string) (string, error) {
	if rawHeader == "" || len(rawHeader) > maxCookieHeaderLen || strings.ContainsAny(rawHeader, "\r\n\x00") {
		return "", ErrInvalidDeliveryCookie
	}
	request := &http.Request{Header: http.Header{"Cookie": []string{rawHeader}}}
	count := 0
	value := ""
	for _, cookie := range request.Cookies() {
		if cookie.Name != DeliveryCookieName {
			continue
		}
		count++
		value = cookie.Value
	}
	if count != 1 || !validCookieSecret(value) {
		return "", ErrInvalidDeliveryCookie
	}
	return value, nil
}

func readOpaqueID(source io.Reader) (string, error) {
	raw := make([]byte, 16)
	if _, err := io.ReadFull(source, raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

func validCookieSecret(value string) bool {
	if len(value) != len("v1.")+base64.RawURLEncoding.EncodedLen(cookieSecretBytes) || !strings.HasPrefix(value, "v1.") {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(value, "v1."))
	return err == nil && len(raw) == cookieSecretBytes
}
