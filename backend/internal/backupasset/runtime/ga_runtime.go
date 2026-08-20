package runtime

import (
	"context"
	"fmt"
	"strings"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/ga"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/settings"

	"gorm.io/gorm"
)

type gaRuntimeInput struct {
	DB        *gorm.DB
	Now       func() time.Time
	Mutations ga.ProviderMutationSurface
}

func composeGARuntime(input gaRuntimeInput) (*ga.InventoryService, error) {
	if input.DB == nil {
		return nil, fmt.Errorf("%w: backup asset inventory runtime composition is unavailable", backupasset.ErrInvalidState)
	}
	mutations := input.Mutations
	if mutations == nil {
		mutations = forbiddenGAMutations{}
	}
	return ga.NewInventoryService(ga.InventoryDependencies{
		DB: input.DB, Now: input.Now, Mutations: mutations,
	}), nil
}

func composeGAReadiness(db *gorm.DB, settingsService *settings.Service, keyring *backupasset.Keyring) ga.ReadinessSource {
	return ga.NewDatabaseReadiness(ga.DatabaseReadinessDependencies{
		DB: db,
		ExportValid: func(context.Context) (bool, error) {
			if settingsService == nil {
				return false, nil
			}
			if err := settings.ValidateBackupAssetFoundationConfig(backupAssetFoundationValues(settingsService)); err != nil {
				return false, nil
			}
			return true, nil
		},
		KeysReady: func(ctx context.Context) (bool, error) {
			if keyring == nil {
				return false, nil
			}
			if ctx == nil {
				ctx = context.Background()
			}
			if _, err := keyring.EnsureRequiredDomains(ctx); err != nil {
				return false, nil
			}
			return true, nil
		},
	})
}

func backupAssetFoundationValues(svc *settings.Service) map[string]string {
	values := map[string]string{}
	if svc == nil {
		return values
	}
	for _, definition := range svc.Registry() {
		if strings.HasPrefix(definition.Key, "backup_assets.") {
			values[definition.Key] = svc.GetEffective(definition.Key)
		}
	}
	return values
}

func (runtime *Runtime) RunInventory(ctx context.Context) (ga.AdminReport, error) {
	if runtime == nil || runtime.inventory == nil || runtime.enablement == nil {
		return ga.AdminReport{}, fmt.Errorf("%w: backup asset GA runtime unavailable", backupasset.ErrInvalidState)
	}
	document, err := runtime.inventory.DryRun(ctx)
	if err != nil {
		ga.ObserveInventory(runtime.gaMetrics, ga.InventoryDocument{}, ga.InventoryResultFailed)
		return ga.AdminReport{}, err
	}
	snapshot, err := runtime.enablement.CurrentReadiness(ctx)
	if err != nil {
		ga.ObserveInventory(runtime.gaMetrics, document, ga.InventoryResultFailed)
		return ga.AdminReport{}, err
	}
	if err := runtime.inventory.MaterializeReadiness(ctx, snapshot); err != nil {
		ga.ObserveInventory(runtime.gaMetrics, document, ga.InventoryResultFailed)
		return ga.AdminReport{}, err
	}
	ga.ObserveInventory(runtime.gaMetrics, document, ga.InventoryResultComplete)
	ga.ObserveReadiness(runtime.gaMetrics, snapshot)
	return ga.AdminReport{Snapshot: snapshot, Inventory: document}, nil
}

func (runtime *Runtime) Readiness(ctx context.Context) (ga.AdminReport, error) {
	if runtime == nil || runtime.enablement == nil {
		return ga.AdminReport{}, fmt.Errorf("%w: backup asset GA runtime unavailable", backupasset.ErrInvalidState)
	}
	snapshot, err := runtime.enablement.CurrentReadiness(ctx)
	if err != nil {
		return ga.AdminReport{}, err
	}
	document := ga.InventoryDocument{}
	if runtime.inventory != nil {
		document, err = runtime.inventory.Latest(ctx)
		if err != nil {
			return ga.AdminReport{}, err
		}
	}
	if document.Digest != "" {
		ga.ObserveInventory(runtime.gaMetrics, document, ga.InventoryResultComplete)
	}
	ga.ObserveReadiness(runtime.gaMetrics, snapshot)
	return ga.AdminReport{Snapshot: snapshot, Inventory: document}, nil
}

func (runtime *Runtime) Acknowledge(ctx context.Context, actorID uint, digest string) (ga.AdminReport, error) {
	if runtime == nil || runtime.inventory == nil || runtime.enablement == nil {
		return ga.AdminReport{}, fmt.Errorf("%w: backup asset GA runtime unavailable", backupasset.ErrInvalidState)
	}
	if err := runtime.inventory.Acknowledge(ctx, actorID, digest); err != nil {
		return ga.AdminReport{}, err
	}
	return runtime.Readiness(ctx)
}

func EnablementRuntime(readiness ga.ReadinessSource, inner publication.FeatureTransitioner) *Runtime {
	return &Runtime{
		enablement:     readiness,
		transitioner:   inner,
		contentManager: gaSilentContentManager{},
		exportManager:  gaSilentExportManager{},
		gaMetrics:      ga.NoopMetrics{},
	}
}

type gaSilentContentManager struct{}

func (gaSilentContentManager) Startup(context.Context) error { return nil }
func (gaSilentContentManager) PrepareEnable(context.Context) error {
	return nil
}
func (gaSilentContentManager) PrepareDisable(context.Context) error { return nil }
func (gaSilentContentManager) SetReady(bool)                        {}
func (gaSilentContentManager) StopAccepting()                       {}
func (gaSilentContentManager) Run(context.Context)                  {}
func (gaSilentContentManager) Shutdown(context.Context) error       { return nil }
func (gaSilentContentManager) PrepareSchemaDown(_ context.Context, down func() error) error {
	if down == nil {
		return nil
	}
	return down()
}

type gaSilentExportManager struct{}

func (gaSilentExportManager) Startup(context.Context) error { return nil }
func (gaSilentExportManager) Ready() bool                   { return true }
func (gaSilentExportManager) TransitionSettings(_ context.Context, _ bool, _ backupasset.ExportConfig, persist func() error) error {
	if persist == nil {
		return nil
	}
	return persist()
}
func (gaSilentExportManager) Service() *managedExportServiceFacade   { return nil }
func (gaSilentExportManager) Delivery() *managedExportDeliveryFacade { return nil }
func (gaSilentExportManager) StopAccepting()                         {}
func (gaSilentExportManager) Run(context.Context)                    {}
func (gaSilentExportManager) Shutdown(context.Context) error         { return nil }
func (gaSilentExportManager) PrepareSchemaDown(_ context.Context, down func() error) error {
	if down == nil {
		return nil
	}
	return down()
}

type forbiddenGAMutations struct{}

func (forbiddenGAMutations) OpenProvider(context.Context, string) error {
	return errInventoryMustNotCallProvider
}

func (forbiddenGAMutations) DiscoverImport(context.Context) error {
	return errInventoryMustNotCallProvider
}

func (forbiddenGAMutations) Rebuild(context.Context) error {
	return errInventoryMustNotCallProvider
}

func (forbiddenGAMutations) Purge(context.Context) error {
	return errInventoryMustNotCallProvider
}

var errInventoryMustNotCallProvider = fmt.Errorf("inventory dry-run must not call provider import, rebuild, or purge")
