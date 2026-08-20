package ga

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

var (
	ErrEnablementBlocked       = errors.New("backup asset enablement blocked")
	ErrEnablementAckRequired   = errors.New("backup asset enablement requires acknowledgment")
	ErrInvalidInventoryDigest  = errors.New("backup asset inventory digest is invalid")
	ErrInventoryDigestMismatch = errors.New("backup asset inventory digest mismatch")
	ErrAcknowledgeNotRequired  = errors.New("backup asset acknowledgment is not required")
)

type InstallationClass string

const (
	InstallationFresh    InstallationClass = "fresh"
	InstallationExisting InstallationClass = "existing"
)

type ReadinessStatus string

const (
	ReadinessUnknown      ReadinessStatus = "unknown"
	ReadinessBlocked      ReadinessStatus = "blocked"
	ReadinessReady        ReadinessStatus = "ready"
	ReadinessAcknowledged ReadinessStatus = "acknowledged"
)

type ReadinessSnapshot struct {
	Class              InstallationClass
	Status             ReadinessStatus
	InventoryComplete  bool
	InventoryDigest    string
	AcknowledgedDigest string
	ExportRootValid    bool
	KeyDomainsReady    bool
}

type ReadinessSource interface {
	CurrentReadiness(ctx context.Context) (ReadinessSnapshot, error)
}

type DatabaseReadinessDependencies struct {
	DB          *gorm.DB
	ExportValid func(context.Context) (bool, error)
	KeysReady   func(context.Context) (bool, error)
}

type DatabaseReadiness struct {
	db          *gorm.DB
	exportValid func(context.Context) (bool, error)
	keysReady   func(context.Context) (bool, error)
}

func NewDatabaseReadiness(dependencies DatabaseReadinessDependencies) *DatabaseReadiness {
	return &DatabaseReadiness{
		db: dependencies.DB, exportValid: dependencies.ExportValid, keysReady: dependencies.KeysReady,
	}
}

func (source *DatabaseReadiness) CurrentReadiness(ctx context.Context) (ReadinessSnapshot, error) {
	if source == nil || source.db == nil {
		return ReadinessSnapshot{}, fmt.Errorf("%w: readiness unavailable", ErrEnablementBlocked)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	snapshot := ReadinessSnapshot{Class: InstallationFresh, Status: ReadinessBlocked}
	exportValid, err := optionalReadinessProbe(ctx, source.exportValid)
	if err != nil {
		return ReadinessSnapshot{}, fmt.Errorf("%w: %w", ErrEnablementBlocked, err)
	}
	keysReady, err := optionalReadinessProbe(ctx, source.keysReady)
	if err != nil {
		return ReadinessSnapshot{}, fmt.Errorf("%w: %w", ErrEnablementBlocked, err)
	}
	snapshot.ExportRootValid = exportValid
	snapshot.KeyDomainsReady = keysReady

	var installation model.BackupAssetInstallation
	result := source.db.WithContext(ctx).Where("slot = ?", 1).Limit(1).Find(&installation)
	if result.Error != nil {
		return ReadinessSnapshot{}, fmt.Errorf("%w: %w", ErrEnablementBlocked, result.Error)
	}
	if result.RowsAffected == 0 {
		return snapshot, nil
	}
	snapshot.Class = InstallationClass(installation.Class)
	snapshot.InventoryDigest = strings.TrimSpace(installation.InventoryDigest)
	if installation.Readiness == string(ReadinessAcknowledged) && snapshot.InventoryDigest != "" {
		snapshot.AcknowledgedDigest = snapshot.InventoryDigest
	}

	var run model.BackupAssetInventoryRun
	runResult := source.db.WithContext(ctx).Order("created_at DESC, id DESC").Limit(1).Find(&run)
	if runResult.Error != nil {
		return ReadinessSnapshot{}, fmt.Errorf("%w: %w", ErrEnablementBlocked, runResult.Error)
	}
	snapshot.InventoryComplete = runResult.RowsAffected > 0 &&
		run.Status == InventoryRunComplete &&
		strings.TrimSpace(run.Digest) == snapshot.InventoryDigest &&
		snapshot.InventoryDigest != ""

	switch {
	case !snapshot.InventoryComplete || !snapshot.ExportRootValid || !snapshot.KeyDomainsReady:
		snapshot.Status = ReadinessBlocked
	case snapshot.Class == InstallationExisting && snapshot.AcknowledgedDigest == snapshot.InventoryDigest && snapshot.InventoryDigest != "":
		snapshot.Status = ReadinessAcknowledged
	default:
		snapshot.Status = ReadinessReady
	}
	return snapshot, nil
}

func optionalReadinessProbe(ctx context.Context, probe func(context.Context) (bool, error)) (bool, error) {
	if probe == nil {
		return false, nil
	}
	return probe(ctx)
}

type EnablementGateDependencies struct {
	Readiness ReadinessSource
	Inner     publication.FeatureTransitioner
}

type EnablementGate struct {
	readiness ReadinessSource
	inner     publication.FeatureTransitioner
}

func NewEnablementGate(dependencies EnablementGateDependencies) *EnablementGate {
	return &EnablementGate{readiness: dependencies.Readiness, inner: dependencies.Inner}
}

func (gate *EnablementGate) TransitionFeature(ctx context.Context, enabled bool, persist func() error) error {
	if gate == nil || gate.inner == nil {
		return fmt.Errorf("enablement gate unavailable")
	}
	if enabled {
		if err := gate.AuthorizeEnable(ctx); err != nil {
			return err
		}
	}
	return gate.inner.TransitionFeature(ctx, enabled, persist)
}

func (gate *EnablementGate) AuthorizeEnable(ctx context.Context) error {
	if gate == nil || gate.readiness == nil {
		return fmt.Errorf("%w: readiness unavailable", ErrEnablementBlocked)
	}
	snapshot, err := gate.readiness.CurrentReadiness(ctx)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrEnablementBlocked, err)
	}
	return EvaluateEnablement(snapshot)
}

func EvaluateEnablement(snapshot ReadinessSnapshot) error {
	if !snapshot.InventoryComplete || !snapshot.ExportRootValid || !snapshot.KeyDomainsReady {
		return ErrEnablementBlocked
	}
	switch snapshot.Class {
	case InstallationFresh:
		if snapshot.Status != ReadinessReady && snapshot.Status != ReadinessAcknowledged {
			return ErrEnablementBlocked
		}
		return nil
	case InstallationExisting:
		if snapshot.InventoryDigest == "" || snapshot.AcknowledgedDigest != snapshot.InventoryDigest {
			if snapshot.Status == ReadinessReady || snapshot.Status == ReadinessAcknowledged {
				return ErrEnablementAckRequired
			}
			return ErrEnablementBlocked
		}
		if snapshot.Status != ReadinessAcknowledged {
			return ErrEnablementAckRequired
		}
		return nil
	default:
		return ErrEnablementBlocked
	}
}

func (gate *EnablementGate) PrepareApplicationDowngrade(ctx context.Context, downgrade func() error) error {
	if gate == nil || gate.inner == nil {
		return fmt.Errorf("enablement gate unavailable")
	}
	return gate.inner.PrepareApplicationDowngrade(ctx, downgrade)
}

func (gate *EnablementGate) PrepareSchemaDown(ctx context.Context, down func() error) error {
	if gate == nil || gate.inner == nil {
		return fmt.Errorf("enablement gate unavailable")
	}
	return gate.inner.PrepareSchemaDown(ctx, down)
}

var _ publication.FeatureTransitioner = (*EnablementGate)(nil)
