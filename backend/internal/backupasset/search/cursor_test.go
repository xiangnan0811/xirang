package search

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestCursorBindsAuthorizationScopeQueryGenerationAndProof(t *testing.T) {
	codec, clock := newSearchCursorHarness(t)
	binding := validSearchCursorBinding()
	token, err := codec.Encode(context.Background(), binding)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	decoded, err := codec.Decode(context.Background(), token, binding.withoutAnchor())
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if decoded.AnchorID != binding.AnchorID {
		t.Fatalf("decoded anchor=%q, want %q", decoded.AnchorID, binding.AnchorID)
	}

	mutations := []struct {
		name   string
		mutate func(*SearchCursorBinding)
	}{
		{name: "user", mutate: func(value *SearchCursorBinding) { value.UserID++ }},
		{name: "role", mutate: func(value *SearchCursorBinding) { value.Role = "operator" }},
		{name: "sort", mutate: func(value *SearchCursorBinding) { value.Sort = SearchSortNameAsc }},
		{name: "query", mutate: func(value *SearchCursorBinding) { value.QueryHMAC = strings.Repeat("1", 64) }},
		{name: "scope", mutate: func(value *SearchCursorBinding) { value.ScopeDigest = strings.Repeat("2", 64) }},
		{name: "selection", mutate: func(value *SearchCursorBinding) { value.SelectionDigest = strings.Repeat("3", 64) }},
		{name: "projection", mutate: func(value *SearchCursorBinding) { value.ProjectionDigest = strings.Repeat("4", 64) }},
		{name: "classification", mutate: func(value *SearchCursorBinding) { value.ClassificationDigest = strings.Repeat("5", 64) }},
		{name: "tag", mutate: func(value *SearchCursorBinding) { value.TagDigest = strings.Repeat("8", 64) }},
		{name: "search key", mutate: func(value *SearchCursorBinding) { value.SearchKeyVersion++ }},
		{name: "proof", mutate: func(value *SearchCursorBinding) { value.ProofDigest = strings.Repeat("6", 64) }},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			expected := binding.withoutAnchor()
			mutation.mutate(&expected)
			if _, err := codec.Decode(context.Background(), token, expected); !errors.Is(err, ErrStaleCursor) {
				t.Fatalf("got %v, want ErrStaleCursor", err)
			}
		})
	}
	clock.now = clock.now.Add(16 * time.Minute)
	if _, err := codec.Decode(context.Background(), token, binding.withoutAnchor()); !errors.Is(err, ErrStaleCursor) {
		t.Fatalf("expired cursor got %v, want ErrStaleCursor", err)
	}
}

func TestCursorPayloadContainsOnlyOpaqueDigestsAndAnchor(t *testing.T) {
	codec, _ := newSearchCursorHarness(t)
	binding := validSearchCursorBinding()
	token, err := codec.Encode(context.Background(), binding)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.Split(token, ".")[0])
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	for _, forbidden := range []string{"quarterly report", "Docs/Secret", "private-tag", "normalized_path", "path_sort_key", "name_sort_key", "token_hmac"} {
		if strings.Contains(string(payload), forbidden) {
			t.Fatalf("cursor payload leaked %q: %s", forbidden, payload)
		}
	}
	if !strings.Contains(string(payload), binding.AnchorID) || !strings.Contains(string(payload), binding.QueryHMAC) {
		t.Fatalf("cursor payload lacks opaque anchor/digest: %s", payload)
	}
}

type searchCursorClock struct{ now time.Time }

func newSearchCursorHarness(t *testing.T) (*CursorCodec, *searchCursorClock) {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_SEARCH_CURSOR_DATA_KEY_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared&_loc=UTC", strings.ReplaceAll(t.Name(), "/", "_"))), &gorm.Config{})
	if err != nil {
		t.Fatalf("open cursor DB: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("open cursor SQL DB: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&model.WrappedDomainKey{}); err != nil {
		t.Fatalf("migrate cursor key: %v", err)
	}
	clock := &searchCursorClock{now: time.Date(2026, 7, 18, 6, 0, 0, 0, time.UTC)}
	ring := backupasset.NewKeyring(db, func() time.Time { return clock.now })
	if _, err := ring.Ensure(context.Background(), backupasset.KeyDomainCursorSigning); err != nil {
		t.Fatalf("ensure cursor key: %v", err)
	}
	return NewCursorCodec(ring, func() time.Time { return clock.now }, 15*time.Minute), clock
}

func validSearchCursorBinding() SearchCursorBinding {
	return SearchCursorBinding{
		UserID: 91, Role: "admin", Sort: SearchSortRelevance,
		QueryHMAC: strings.Repeat("a", 64), ScopeDigest: strings.Repeat("b", 64),
		SelectionDigest: strings.Repeat("c", 64), ProjectionDigest: strings.Repeat("d", 64),
		ClassificationDigest: strings.Repeat("e", 64), TagDigest: strings.Repeat("7", 64), SearchKeyVersion: 1,
		ProofDigest: strings.Repeat("f", 64), AnchorID: strings.Repeat("9", 64),
	}
}

func (binding SearchCursorBinding) withoutAnchor() SearchCursorBinding {
	binding.AnchorID = ""
	return binding
}
