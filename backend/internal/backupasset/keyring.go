package backupasset

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type KeyDomain string

const (
	KeyDomainEntryIdentity            KeyDomain = "entry_identity"
	KeyDomainCursorSigning            KeyDomain = "cursor_signing"
	KeyDomainAuditFingerprint         KeyDomain = "audit_fingerprint"
	KeyDomainRecoveryCleanupOwnership KeyDomain = "recovery_cleanup_ownership"
	KeyDomainSearchToken              KeyDomain = "search_token"
	KeyDomainDerivedStore             KeyDomain = "derived_store"
)

var RequiredKeyDomains = []KeyDomain{
	KeyDomainEntryIdentity,
	KeyDomainCursorSigning,
	KeyDomainAuditFingerprint,
	KeyDomainRecoveryCleanupOwnership,
}

type DomainKeyState string

const (
	DomainKeyActive     DomainKeyState = "active"
	DomainKeyVerifyOnly DomainKeyState = "verify_only"
	DomainKeyRetired    DomainKeyState = "retired"
	DomainKeyLost       DomainKeyState = "lost"
)

type DomainKeyMaterial struct {
	ID          string
	Domain      KeyDomain
	Version     int
	State       DomainKeyState
	Key         []byte
	ActivatedAt time.Time
	VerifyUntil *time.Time
}

type RebuildableKeyTransition struct {
	Domain          KeyDomain
	PreviousVersion int
	NextVersion     int
}

type RebuildableKeyInvalidator func(context.Context, *gorm.DB, RebuildableKeyTransition) error

type Keyring struct {
	db  *gorm.DB
	now func() time.Time
}

func NewKeyring(db *gorm.DB, now func() time.Time) *Keyring {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Keyring{db: db, now: now}
}

func (keyring *Keyring) EnsureRequiredDomains(ctx context.Context) (map[KeyDomain]DomainKeyMaterial, error) {
	materials := make(map[KeyDomain]DomainKeyMaterial, len(RequiredKeyDomains))
	for _, domain := range RequiredKeyDomains {
		material, err := keyring.Ensure(ctx, domain)
		if err != nil {
			return nil, err
		}
		materials[domain] = material
	}
	return materials, nil
}

func (keyring *Keyring) Ensure(ctx context.Context, domain KeyDomain) (DomainKeyMaterial, error) {
	if err := keyring.validate(domain); err != nil {
		return DomainKeyMaterial{}, err
	}
	if material, err := keyring.Active(ctx, domain); err == nil {
		return material, nil
	} else if errors.Is(err, ErrKeyLost) {
		return DomainKeyMaterial{}, err
	}

	var lastErr error
	for attempt := 0; attempt < 12; attempt++ {
		var created model.WrappedDomainKey
		err := keyring.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			rows, err := loadDomainKeyRows(tx, domain, true)
			if err != nil {
				return err
			}
			for _, row := range rows {
				switch DomainKeyState(row.State) {
				case DomainKeyActive:
					created = row
					return nil
				case DomainKeyLost:
					return fmt.Errorf("%w: domain %s", ErrKeyLost, domain)
				}
			}
			if len(rows) > 0 {
				return fmt.Errorf("%w: domain %s has no active key", ErrKeyUnavailable, domain)
			}

			row, err := keyring.newRow(domain, 1, DomainKeyActive, nil)
			if err != nil {
				return err
			}
			if err := tx.Create(&row).Error; err != nil {
				return fmt.Errorf("create wrapped domain key: %w", err)
			}
			created = row
			return nil
		})
		if err == nil {
			return keyring.material(created)
		}
		if errors.Is(err, ErrKeyLost) || errors.Is(err, ErrKeyUnavailable) {
			return DomainKeyMaterial{}, err
		}
		lastErr = err
		// A concurrent transaction may have won the partial-unique insert.
		if active, activeErr := keyring.Active(ctx, domain); activeErr == nil {
			return active, nil
		} else if errors.Is(activeErr, ErrKeyLost) {
			return DomainKeyMaterial{}, activeErr
		}
		if !retryableKeyringConflict(err) {
			return DomainKeyMaterial{}, fmt.Errorf("ensure domain key: %w", err)
		}
		delay := time.Duration(attempt+1) * time.Millisecond
		select {
		case <-ctx.Done():
			return DomainKeyMaterial{}, fmt.Errorf("ensure domain key: %w", ctx.Err())
		case <-time.After(delay):
		}
	}
	return DomainKeyMaterial{}, fmt.Errorf("ensure domain key after retries: %w", lastErr)
}

func (keyring *Keyring) Active(ctx context.Context, domain KeyDomain) (DomainKeyMaterial, error) {
	if err := keyring.validate(domain); err != nil {
		return DomainKeyMaterial{}, err
	}
	var row model.WrappedDomainKey
	err := keyring.db.WithContext(ctx).
		Where("domain = ? AND state = ?", domain, DomainKeyActive).
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return DomainKeyMaterial{}, keyring.missingDomainError(ctx, domain)
	}
	if err != nil {
		return DomainKeyMaterial{}, fmt.Errorf("load active domain key: %w", err)
	}
	return keyring.material(row)
}

func (keyring *Keyring) ByVersion(ctx context.Context, domain KeyDomain, version int) (DomainKeyMaterial, error) {
	if err := keyring.validate(domain); err != nil {
		return DomainKeyMaterial{}, err
	}
	if version <= 0 {
		return DomainKeyMaterial{}, fmt.Errorf("%w: invalid key version", ErrKeyUnavailable)
	}
	var row model.WrappedDomainKey
	err := keyring.db.WithContext(ctx).
		Where("domain = ? AND version = ?", domain, version).
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return DomainKeyMaterial{}, fmt.Errorf("%w: domain key version is missing", ErrKeyUnavailable)
	}
	if err != nil {
		return DomainKeyMaterial{}, fmt.Errorf("load domain key version: %w", err)
	}
	return keyring.material(row)
}

func (keyring *Keyring) Rotate(ctx context.Context, domain KeyDomain, verifyFor time.Duration) (DomainKeyMaterial, error) {
	if err := keyring.validate(domain); err != nil {
		return DomainKeyMaterial{}, err
	}
	if domain == KeyDomainEntryIdentity || domain == KeyDomainRecoveryCleanupOwnership || domain == KeyDomainSearchToken {
		return DomainKeyMaterial{}, fmt.Errorf("%w: domain %s is installation-stable", ErrKeyRotationProhibited, domain)
	}
	if domain == KeyDomainCursorSigning && (verifyFor <= 0 || verifyFor > 7*24*time.Hour) {
		return DomainKeyMaterial{}, fmt.Errorf("%w: cursor overlap must be within 7 days", ErrInvalidState)
	}

	var created model.WrappedDomainKey
	err := keyring.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		rows, err := loadDomainKeyRows(tx, domain, true)
		if err != nil {
			return err
		}
		var active *model.WrappedDomainKey
		maxVersion := 0
		for index := range rows {
			row := &rows[index]
			if row.Version > maxVersion {
				maxVersion = row.Version
			}
			if DomainKeyState(row.State) == DomainKeyActive {
				active = row
			}
		}
		if active == nil {
			for _, row := range rows {
				if DomainKeyState(row.State) == DomainKeyLost {
					return fmt.Errorf("%w: domain %s", ErrKeyLost, domain)
				}
			}
			return fmt.Errorf("%w: domain %s has no active key", ErrKeyUnavailable, domain)
		}
		if _, err := keyring.material(*active); err != nil {
			return err
		}

		now := keyring.utcNow()
		updates := map[string]any{
			"state":        DomainKeyVerifyOnly,
			"verify_until": nil,
			"updated_at":   now,
		}
		if domain == KeyDomainCursorSigning {
			until := now.Add(verifyFor)
			updates["verify_until"] = until
		}
		result := tx.Model(&model.WrappedDomainKey{}).
			Where("id = ? AND state = ?", active.ID, DomainKeyActive).
			Updates(updates)
		if result.Error != nil {
			return fmt.Errorf("demote active domain key: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("%w: active key changed during rotation", ErrConflict)
		}

		row, err := keyring.newRow(domain, maxVersion+1, DomainKeyActive, nil)
		if err != nil {
			return err
		}
		if err := tx.Create(&row).Error; err != nil {
			return fmt.Errorf("create rotated domain key: %w", err)
		}
		created = row
		return nil
	})
	if err != nil {
		return DomainKeyMaterial{}, err
	}
	return keyring.material(created)
}

func (keyring *Keyring) ReplaceRebuildable(
	ctx context.Context,
	domain KeyDomain,
	invalidate RebuildableKeyInvalidator,
) (DomainKeyMaterial, error) {
	if err := keyring.validateRebuildableTransition(domain, invalidate); err != nil {
		return DomainKeyMaterial{}, err
	}
	var created model.WrappedDomainKey
	err := keyring.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		rows, err := loadDomainKeyRows(tx, domain, true)
		if err != nil {
			return err
		}
		var active *model.WrappedDomainKey
		maxVersion := 0
		for index := range rows {
			row := &rows[index]
			if row.Version > maxVersion {
				maxVersion = row.Version
			}
			if DomainKeyState(row.State) == DomainKeyActive {
				active = row
			}
		}
		if maxVersion == 0 {
			return fmt.Errorf("%w: rebuildable domain has not been initialized", ErrKeyUnavailable)
		}
		if active == nil {
			latest := rows[len(rows)-1]
			if DomainKeyState(latest.State) != DomainKeyLost {
				return fmt.Errorf("%w: rebuildable domain has no active or lost key", ErrKeyUnavailable)
			}
		} else if _, err := keyring.material(*active); err != nil {
			return err
		}

		transition := RebuildableKeyTransition{
			Domain: domain, PreviousVersion: maxVersion, NextVersion: maxVersion + 1,
		}
		if err := invalidate(ctx, tx, transition); err != nil {
			return fmt.Errorf("invalidate rebuildable domain: %w", err)
		}
		if active != nil {
			result := tx.Model(&model.WrappedDomainKey{}).
				Where("id = ? AND state = ?", active.ID, DomainKeyActive).
				Updates(map[string]any{
					"state":        DomainKeyRetired,
					"verify_until": nil,
					"updated_at":   keyring.utcNow(),
				})
			if result.Error != nil {
				return fmt.Errorf("retire rebuildable domain key: %w", result.Error)
			}
			if result.RowsAffected != 1 {
				return fmt.Errorf("%w: active rebuildable key changed", ErrConflict)
			}
		}
		created, err = keyring.newRow(domain, transition.NextVersion, DomainKeyActive, nil)
		if err != nil {
			return err
		}
		if err := tx.Create(&created).Error; err != nil {
			return fmt.Errorf("create replacement domain key: %w", err)
		}
		return nil
	})
	if err != nil {
		return DomainKeyMaterial{}, err
	}
	return keyring.material(created)
}

func (keyring *Keyring) RewrapAll(ctx context.Context) (int64, error) {
	if keyring == nil || keyring.db == nil {
		return 0, fmt.Errorf("%w: keyring database is unavailable", ErrKeyUnavailable)
	}
	return keyring.rewrapDomains(ctx, nil)
}

// RewrapDomains rotates the master-key envelope only for the named domains.
// Runtime composition uses this to keep an optional rebuildable domain failure
// from blocking unrelated Core domains while preserving RewrapAll's atomic
// all-domain maintenance contract.
func (keyring *Keyring) RewrapDomains(ctx context.Context, domains ...KeyDomain) (int64, error) {
	if keyring == nil || keyring.db == nil {
		return 0, fmt.Errorf("%w: keyring database is unavailable", ErrKeyUnavailable)
	}
	if len(domains) == 0 {
		return 0, fmt.Errorf("%w: at least one key domain is required", ErrKeyUnavailable)
	}
	seen := make(map[KeyDomain]struct{}, len(domains))
	for _, domain := range domains {
		if err := keyring.validate(domain); err != nil {
			return 0, err
		}
		if _, duplicate := seen[domain]; duplicate {
			return 0, fmt.Errorf("%w: duplicate key domain", ErrInvalidState)
		}
		seen[domain] = struct{}{}
	}
	return keyring.rewrapDomains(ctx, domains)
}

func (keyring *Keyring) rewrapDomains(ctx context.Context, domains []KeyDomain) (int64, error) {
	var count int64
	err := keyring.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var rows []model.WrappedDomainKey
		query := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("state <> ?", DomainKeyLost)
		if len(domains) > 0 {
			query = query.Where("domain IN ?", domains)
		}
		if err := query.Order("domain ASC, version ASC").Find(&rows).Error; err != nil {
			return fmt.Errorf("load domain keys for rewrap: %w", err)
		}
		for _, row := range rows {
			material, err := keyring.unwrapPersistedMaterial(row)
			if err != nil {
				return err
			}
			wrapped, err := secure.WrapDomainKey(row.Domain, row.Version, material.Key)
			if err != nil {
				return fmt.Errorf("%w: wrap domain %s version %d", ErrKeyUnavailable, row.Domain, row.Version)
			}
			result := tx.Model(&model.WrappedDomainKey{}).
				Where("id = ? AND domain = ? AND version = ?", row.ID, row.Domain, row.Version).
				Updates(map[string]any{
					"wrapped_key":              wrapped.Envelope,
					"wrap_algorithm":           wrapped.Algorithm,
					"wrapping_key_fingerprint": wrapped.KEKFingerprint,
					"updated_at":               keyring.utcNow(),
				})
			if result.Error != nil {
				return fmt.Errorf("persist rewrapped domain key: %w", result.Error)
			}
			if result.RowsAffected != 1 {
				return fmt.Errorf("%w: domain key changed during rewrap", ErrConflict)
			}
			count++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return count, nil
}

func (keyring *Keyring) MarkLost(ctx context.Context, domain KeyDomain, version int) error {
	if err := keyring.validate(domain); err != nil {
		return err
	}
	if domain == KeyDomainSearchToken || domain == KeyDomainDerivedStore {
		return fmt.Errorf("%w: domain %s requires coordinated invalidation", ErrKeyRotationProhibited, domain)
	}
	if version <= 0 {
		return fmt.Errorf("%w: invalid key version", ErrKeyUnavailable)
	}
	now := keyring.utcNow()
	result := keyring.db.WithContext(ctx).Model(&model.WrappedDomainKey{}).
		Where("domain = ? AND version = ? AND state <> ?", domain, version, DomainKeyLost).
		Updates(map[string]any{
			"state":        DomainKeyLost,
			"verify_until": nil,
			"lost_at":      now,
			"updated_at":   now,
		})
	if result.Error != nil {
		return fmt.Errorf("mark domain key lost: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("%w: domain key version is missing or already lost", ErrKeyUnavailable)
	}
	return nil
}

func (keyring *Keyring) MarkRebuildableLost(
	ctx context.Context,
	domain KeyDomain,
	version int,
	invalidate RebuildableKeyInvalidator,
) error {
	if err := keyring.validateRebuildableTransition(domain, invalidate); err != nil {
		return err
	}
	if version <= 0 {
		return fmt.Errorf("%w: invalid key version", ErrKeyUnavailable)
	}
	return keyring.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		rows, err := loadDomainKeyRows(tx, domain, true)
		if err != nil {
			return err
		}
		var target *model.WrappedDomainKey
		for index := range rows {
			if rows[index].Version == version && DomainKeyState(rows[index].State) == DomainKeyActive {
				target = &rows[index]
				break
			}
		}
		if target == nil {
			return fmt.Errorf("%w: domain key version is not active", ErrKeyUnavailable)
		}
		transition := RebuildableKeyTransition{Domain: domain, PreviousVersion: version}
		if err := invalidate(ctx, tx, transition); err != nil {
			return fmt.Errorf("invalidate lost rebuildable domain: %w", err)
		}
		now := keyring.utcNow()
		result := tx.Model(&model.WrappedDomainKey{}).
			Where("id = ? AND version = ? AND state = ?", target.ID, version, DomainKeyActive).
			Updates(map[string]any{
				"state": DomainKeyLost, "verify_until": nil, "lost_at": now, "updated_at": now,
			})
		if result.Error != nil {
			return fmt.Errorf("mark rebuildable domain key lost: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("%w: rebuildable key changed during loss", ErrConflict)
		}
		return nil
	})
}

func (keyring *Keyring) validateRebuildableTransition(domain KeyDomain, invalidate RebuildableKeyInvalidator) error {
	if err := keyring.validate(domain); err != nil {
		return err
	}
	if domain != KeyDomainSearchToken && domain != KeyDomainDerivedStore {
		return fmt.Errorf("%w: domain %s is not rebuildable", ErrKeyRotationProhibited, domain)
	}
	if invalidate == nil {
		return fmt.Errorf("%w: rebuildable invalidation callback is required", ErrInvalidState)
	}
	return nil
}

func (keyring *Keyring) newRow(domain KeyDomain, version int, state DomainKeyState, verifyUntil *time.Time) (model.WrappedDomainKey, error) {
	id, err := NewOpaqueID()
	if err != nil {
		return model.WrappedDomainKey{}, err
	}
	plaintext := make([]byte, secure.DomainKeySize)
	if _, err := io.ReadFull(rand.Reader, plaintext); err != nil {
		return model.WrappedDomainKey{}, fmt.Errorf("generate domain key: %w", err)
	}
	wrapped, err := secure.WrapDomainKey(string(domain), version, plaintext)
	if err != nil {
		return model.WrappedDomainKey{}, fmt.Errorf("wrap new domain key: %w", err)
	}
	now := keyring.utcNow()
	return model.WrappedDomainKey{
		ID:                     id,
		Domain:                 string(domain),
		Version:                version,
		State:                  string(state),
		WrappedKey:             wrapped.Envelope,
		WrapAlgorithm:          wrapped.Algorithm,
		WrappingKeyFingerprint: wrapped.KEKFingerprint,
		ActivatedAt:            now,
		VerifyUntil:            verifyUntil,
		CreatedAt:              now,
		UpdatedAt:              now,
	}, nil
}

func (keyring *Keyring) material(row model.WrappedDomainKey) (DomainKeyMaterial, error) {
	material, err := keyring.unwrapPersistedMaterial(row)
	if err != nil {
		return DomainKeyMaterial{}, err
	}
	if material.State == DomainKeyRetired ||
		(material.State == DomainKeyVerifyOnly && material.VerifyUntil != nil && !keyring.utcNow().Before(material.VerifyUntil.UTC())) {
		return DomainKeyMaterial{}, fmt.Errorf("%w: domain key version is outside its verification window", ErrKeyUnavailable)
	}
	return material, nil
}

func (keyring *Keyring) unwrapPersistedMaterial(row model.WrappedDomainKey) (DomainKeyMaterial, error) {
	domain := KeyDomain(row.Domain)
	state := DomainKeyState(row.State)
	if !validKeyDomains[domain] || !validDomainKeyStates[state] || row.Version <= 0 || ValidateOpaqueID(row.ID) != nil {
		return DomainKeyMaterial{}, fmt.Errorf("%w: invalid persisted domain key", ErrKeyUnavailable)
	}
	if state == DomainKeyLost {
		return DomainKeyMaterial{}, fmt.Errorf("%w: domain %s version %d", ErrKeyLost, domain, row.Version)
	}
	plaintext, err := secure.UnwrapDomainKey(row.Domain, row.Version, secure.WrappedDomainKey{
		Envelope:       row.WrappedKey,
		Algorithm:      row.WrapAlgorithm,
		KEKFingerprint: row.WrappingKeyFingerprint,
	})
	if err != nil {
		return DomainKeyMaterial{}, fmt.Errorf("%w: domain %s version %d", ErrKeyUnavailable, domain, row.Version)
	}
	verifyUntil := row.VerifyUntil
	if verifyUntil != nil {
		utc := verifyUntil.UTC()
		verifyUntil = &utc
	}
	return DomainKeyMaterial{
		ID:          row.ID,
		Domain:      domain,
		Version:     row.Version,
		State:       state,
		Key:         plaintext,
		ActivatedAt: row.ActivatedAt.UTC(),
		VerifyUntil: verifyUntil,
	}, nil
}

func (keyring *Keyring) missingDomainError(ctx context.Context, domain KeyDomain) error {
	var row model.WrappedDomainKey
	err := keyring.db.WithContext(ctx).
		Where("domain = ?", domain).
		Order("version DESC").
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("%w: domain %s has not been initialized", ErrKeyUnavailable, domain)
	}
	if err != nil {
		return fmt.Errorf("load domain key history: %w", err)
	}
	if DomainKeyState(row.State) == DomainKeyLost {
		return fmt.Errorf("%w: domain %s", ErrKeyLost, domain)
	}
	return fmt.Errorf("%w: domain %s has no active key", ErrKeyUnavailable, domain)
}

func (keyring *Keyring) validate(domain KeyDomain) error {
	if keyring == nil || keyring.db == nil {
		return fmt.Errorf("%w: keyring database is unavailable", ErrKeyUnavailable)
	}
	if !validKeyDomains[domain] {
		return fmt.Errorf("%w: unknown key domain", ErrKeyUnavailable)
	}
	return nil
}

func (keyring *Keyring) utcNow() time.Time {
	return keyring.now().UTC()
}

func loadDomainKeyRows(tx *gorm.DB, domain KeyDomain, lock bool) ([]model.WrappedDomainKey, error) {
	query := tx.Where("domain = ?", domain).Order("version ASC")
	if lock {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var rows []model.WrappedDomainKey
	if err := query.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("load domain key rows: %w", err)
	}
	return rows, nil
}

var (
	validKeyDomains = setOf(
		KeyDomainEntryIdentity, KeyDomainCursorSigning, KeyDomainAuditFingerprint,
		KeyDomainRecoveryCleanupOwnership, KeyDomainSearchToken, KeyDomainDerivedStore,
	)
	validDomainKeyStates = setOf(DomainKeyActive, DomainKeyVerifyOnly, DomainKeyRetired, DomainKeyLost)
)

func retryableKeyringConflict(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "locked") || strings.Contains(message, "busy") || strings.Contains(message, "unique")
}
