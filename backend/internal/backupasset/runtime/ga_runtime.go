package runtime

import (
	"context"
	"fmt"
	"strings"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/catalog"
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

func featureLive(foundation *backupasset.FoundationService, enablement ga.ReadinessSource) (bool, error) {
	if foundation == nil {
		return false, fmt.Errorf("%w: backup asset foundation unavailable", backupasset.ErrInvalidState)
	}
	requested, err := foundation.FeatureEnabled()
	if err != nil {
		return false, err
	}
	if !requested {
		return false, nil
	}
	if enablement == nil {
		return false, nil
	}
	snapshot, err := enablement.CurrentReadiness(context.Background())
	if err != nil {
		return false, err
	}
	if err := ga.EvaluateEnablement(snapshot); err != nil {
		return false, nil
	}
	return true, nil
}

func (runtime *Runtime) FeatureLive() (bool, error) {
	if runtime == nil {
		return false, fmt.Errorf("%w: backup asset runtime unavailable", backupasset.ErrInvalidState)
	}
	live, err := featureLive(runtime.foundation, runtime.enablement)
	observeFeatureGates(runtime, live && err == nil)
	return live, err
}

func observeFeatureGates(runtime *Runtime, live bool) {
	if runtime == nil || runtime.gaMetrics == nil {
		return
	}
	requested := false
	if runtime.foundation != nil {
		if value, enabledErr := runtime.foundation.FeatureEnabled(); enabledErr == nil {
			requested = value
		}
	}
	runtime.gaMetrics.SetFeatureGates(requested, live)
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

// WithFoundation attaches the settings-backed foundation used by FeatureLive
// and handler config. Tests use this to prove requested-true / live-false.
func (runtime *Runtime) WithFoundation(foundation *backupasset.FoundationService) *Runtime {
	if runtime != nil {
		runtime.foundation = foundation
	}
	return runtime
}

// WithCatalogService attaches a production Catalog service so HTTP tests can
// prove FeatureLive without the FeatureDisabled stub.
func (runtime *Runtime) WithCatalogService(service *catalog.Service) *Runtime {
	if runtime != nil {
		runtime.catalogService = service
	}
	return runtime
}

type staticReadiness struct {
	snapshot ga.ReadinessSnapshot
}

func (source staticReadiness) CurrentReadiness(context.Context) (ga.ReadinessSnapshot, error) {
	return source.snapshot, nil
}

// ExistingInstallReadyUnacked is an existing install that is inventory-ready
// but has no Admin ack. FeatureLive stays false even when the setting is on.
func ExistingInstallReadyUnacked() ga.ReadinessSource {
	return staticReadiness{snapshot: ga.ReadinessSnapshot{
		Class:             ga.InstallationExisting,
		Status:            ga.ReadinessReady,
		InventoryComplete: true,
		InventoryDigest:   "current-digest",
		ExportRootValid:   true,
		KeyDomainsReady:   true,
	}}
}

// FreshInstallReady is a fresh install that may become live when requested.
func FreshInstallReady() ga.ReadinessSource {
	return staticReadiness{snapshot: ga.ReadinessSnapshot{
		Class:             ga.InstallationFresh,
		Status:            ga.ReadinessReady,
		InventoryComplete: true,
		InventoryDigest:   "test-enablement-digest",
		ExportRootValid:   true,
		KeyDomainsReady:   true,
	}}
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
