package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/backupasset/repository"
)

var ErrAdmissionNotInitialized = errors.New("backup asset admission is not initialized")

type AdmissionControllerDependencies struct {
	Foundation *backupasset.FoundationService
	History    *repository.ManagedHistoryResolver
}

// AdmissionController owns runtime mode selection. Its barrier owns the
// command-drain mechanics, while ManagedHistoryResolver remains the lower
// persisted fact source so neither package depends on the other cyclically.
type AdmissionController struct {
	foundation *backupasset.FoundationService
	history    *repository.ManagedHistoryResolver
	barrier    *admissionBarrier

	mu          sync.RWMutex
	initialized bool
}

func NewAdmissionController(dependencies AdmissionControllerDependencies) (*AdmissionController, error) {
	if dependencies.Foundation == nil || dependencies.History == nil {
		return nil, fmt.Errorf("%w: admission controller dependencies are unavailable", backupasset.ErrInvalidState)
	}
	barrier, err := newAdmissionBarrier(publication.AdmissionPristineLegacy)
	if err != nil {
		return nil, err
	}
	return &AdmissionController{foundation: dependencies.Foundation, history: dependencies.History, barrier: barrier}, nil
}

func (controller *AdmissionController) Initialize(ctx context.Context) error {
	if controller == nil {
		return fmt.Errorf("%w: admission controller is unavailable", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	target, err := controller.disabledMode(ctx)
	if err != nil {
		return err
	}
	return controller.initializeTo(target)
}

func (controller *AdmissionController) InitializeManaged(ctx context.Context) error {
	if controller == nil {
		return fmt.Errorf("%w: admission controller is unavailable", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return controller.initializeTo(publication.AdmissionManaged)
}

func (controller *AdmissionController) InitializeDisabled(ctx context.Context) error {
	if controller == nil {
		return fmt.Errorf("%w: admission controller is unavailable", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	target, err := controller.disabledMode(ctx)
	if err != nil {
		return err
	}
	return controller.initializeTo(target)
}

func (controller *AdmissionController) initializeTo(target publication.AdmissionMode) error {
	controller.mu.RLock()
	alreadyInitialized := controller.initialized
	controller.mu.RUnlock()
	if alreadyInitialized {
		return nil
	}
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if controller.initialized {
		return nil
	}
	controller.barrier.mu.Lock()
	if controller.barrier.active != 0 || controller.barrier.transitioning || controller.barrier.stopping {
		controller.barrier.mu.Unlock()
		return fmt.Errorf("%w: admission barrier changed before initialization", backupasset.ErrInvalidState)
	}
	controller.barrier.mode = target
	controller.barrier.mu.Unlock()
	controller.initialized = true
	return nil
}

func (controller *AdmissionController) CurrentMode() (publication.AdmissionMode, error) {
	if err := controller.requireInitialized(); err != nil {
		return "", err
	}
	mode, _ := controller.barrier.current()
	return mode, nil
}

func (controller *AdmissionController) Acquire(ctx context.Context, operation publication.ResticOperation) (publication.AdmissionToken, error) {
	if err := controller.requireInitialized(); err != nil {
		return nil, err
	}
	return controller.barrier.Acquire(ctx, operation)
}

func (controller *AdmissionController) TransitionFeature(ctx context.Context, enabled bool, persist func() error) error {
	if err := controller.requireInitialized(); err != nil {
		return err
	}
	if persist == nil {
		return fmt.Errorf("%w: feature transition persistence is unavailable", backupasset.ErrInvalidState)
	}
	return controller.barrier.transitionResolve(ctx, func() (publication.AdmissionMode, error) {
		if enabled {
			return publication.AdmissionManaged, nil
		}
		return controller.disabledMode(ctx)
	}, persist)
}

func (controller *AdmissionController) PrepareApplicationDowngrade(ctx context.Context, downgrade func() error) error {
	return controller.prepareDowngrade(ctx, downgrade)
}

func (controller *AdmissionController) PrepareSchemaDown(ctx context.Context, down func() error) error {
	return controller.prepareDowngrade(ctx, down)
}

func (controller *AdmissionController) Stop(ctx context.Context) error {
	if err := controller.requireInitialized(); err != nil {
		return err
	}
	return controller.barrier.stop(ctx)
}

// StopAccepting rejects new Restic operation tokens without waiting for
// existing ones. The caller must subsequently call Stop with a bounded
// shutdown context to join admitted command lifecycles.
func (controller *AdmissionController) StopAccepting() {
	if controller == nil {
		return
	}
	controller.mu.RLock()
	initialized := controller.initialized
	controller.mu.RUnlock()
	if initialized {
		controller.barrier.stopAccepting()
	}
}

func (controller *AdmissionController) disabledMode(ctx context.Context) (publication.AdmissionMode, error) {
	history, err := controller.history.HasInstallationManagedHistory(ctx)
	if err != nil {
		return "", err
	}
	lease, err := controller.history.HasActivePublicationLease(ctx)
	if err != nil {
		return "", err
	}
	if history || lease {
		return publication.AdmissionRollbackSafe, nil
	}
	return publication.AdmissionPristineLegacy, nil
}

func (controller *AdmissionController) prepareDowngrade(ctx context.Context, callback func() error) error {
	if err := controller.requireInitialized(); err != nil {
		return err
	}
	if callback == nil {
		return fmt.Errorf("%w: downgrade callback is unavailable", backupasset.ErrInvalidState)
	}
	return controller.barrier.transitionResolve(ctx, func() (publication.AdmissionMode, error) {
		history, err := controller.history.HasInstallationManagedHistory(ctx)
		if err != nil {
			return "", err
		}
		lease, err := controller.history.HasActivePublicationLease(ctx)
		if err != nil {
			return "", err
		}
		if history || lease {
			return "", fmt.Errorf("%w: managed history or active publication lease prohibits downgrade", backupasset.ErrConflict)
		}
		mode, _ := controller.barrier.current()
		return mode, nil
	}, callback)
}

func (controller *AdmissionController) requireInitialized() error {
	if controller == nil {
		return fmt.Errorf("%w: admission controller is unavailable", backupasset.ErrInvalidState)
	}
	controller.mu.RLock()
	initialized := controller.initialized
	controller.mu.RUnlock()
	if !initialized {
		return ErrAdmissionNotInitialized
	}
	return nil
}

var _ publication.Admission = (*AdmissionController)(nil)
var _ publication.FeatureTransitioner = (*AdmissionController)(nil)
