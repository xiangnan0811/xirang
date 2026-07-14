package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/publication"
)

var ErrAdmissionStopped = errors.New("backup asset admission is stopping")

type admissionBarrier struct {
	mu            sync.Mutex
	mode          publication.AdmissionMode
	generation    uint64
	active        int
	transitioning bool
	stopping      bool
	changed       chan struct{}
}

func newAdmissionBarrier(mode publication.AdmissionMode) (*admissionBarrier, error) {
	if err := publication.ValidateAdmissionMode(mode); err != nil {
		return nil, err
	}
	return &admissionBarrier{mode: mode, generation: 1, changed: make(chan struct{})}, nil
}

func (barrier *admissionBarrier) Acquire(ctx context.Context, operation publication.ResticOperation) (publication.AdmissionToken, error) {
	if barrier == nil {
		return nil, fmt.Errorf("%w: admission barrier is unavailable", backupasset.ErrInvalidState)
	}
	if err := publication.ValidateResticOperation(operation); err != nil {
		return nil, err
	}
	barrier.mu.Lock()
	defer barrier.mu.Unlock()
	for barrier.transitioning {
		if err := barrier.waitLocked(ctx); err != nil {
			return nil, err
		}
	}
	if barrier.stopping {
		return nil, fmt.Errorf("%w: new %s token rejected", ErrAdmissionStopped, operation)
	}
	barrier.active++
	return &admissionToken{
		barrier:    barrier,
		mode:       barrier.mode,
		generation: barrier.generation,
		operation:  operation,
	}, nil
}

func (barrier *admissionBarrier) transition(ctx context.Context, target publication.AdmissionMode, persist func() error) error {
	return barrier.transitionResolve(ctx, func() (publication.AdmissionMode, error) { return target, nil }, persist)
}

// transitionResolve keeps admission closed while it drains the current
// generation, rechecks safety state, persists the transition, and only then
// publishes a new immutable mode/generation to subsequent tokens.
func (barrier *admissionBarrier) transitionResolve(ctx context.Context, resolveTarget func() (publication.AdmissionMode, error), persist func() error) error {
	if barrier == nil || resolveTarget == nil || persist == nil {
		return fmt.Errorf("%w: admission transition is unavailable", backupasset.ErrInvalidState)
	}
	if err := barrier.beginTransition(ctx); err != nil {
		return err
	}
	target, err := resolveTarget()
	if err == nil {
		err = publication.ValidateAdmissionMode(target)
	}
	if err == nil {
		err = ctx.Err()
	}
	if err == nil {
		err = persist()
	}
	barrier.finishTransition(target, err == nil)
	return err
}

func (barrier *admissionBarrier) beginTransition(ctx context.Context) error {
	barrier.mu.Lock()
	for barrier.transitioning {
		if err := barrier.waitLocked(ctx); err != nil {
			barrier.mu.Unlock()
			return err
		}
	}
	if barrier.stopping {
		barrier.mu.Unlock()
		return fmt.Errorf("%w: transition rejected", ErrAdmissionStopped)
	}
	barrier.transitioning = true
	barrier.signalLocked()
	for barrier.active > 0 {
		if err := barrier.waitLocked(ctx); err != nil {
			barrier.transitioning = false
			barrier.signalLocked()
			barrier.mu.Unlock()
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		barrier.transitioning = false
		barrier.signalLocked()
		barrier.mu.Unlock()
		return err
	}
	barrier.mu.Unlock()
	return nil
}

func (barrier *admissionBarrier) finishTransition(target publication.AdmissionMode, apply bool) {
	barrier.mu.Lock()
	if apply {
		barrier.mode = target
		barrier.generation++
	}
	barrier.transitioning = false
	barrier.signalLocked()
	barrier.mu.Unlock()
}

func (barrier *admissionBarrier) stop(ctx context.Context) error {
	if barrier == nil {
		return fmt.Errorf("%w: admission barrier is unavailable", backupasset.ErrInvalidState)
	}
	barrier.mu.Lock()
	for barrier.transitioning {
		if err := barrier.waitLocked(ctx); err != nil {
			barrier.mu.Unlock()
			return err
		}
	}
	barrier.stopping = true
	barrier.signalLocked()
	for barrier.active > 0 {
		if err := barrier.waitLocked(ctx); err != nil {
			barrier.mu.Unlock()
			return err
		}
	}
	barrier.mu.Unlock()
	return nil
}

// stopAccepting closes the admission gate immediately but deliberately does
// not wait for existing command lifecycles. Runtime shutdown performs that
// bounded drain later through stop(ctx), after HTTP producers are stopped.
func (barrier *admissionBarrier) stopAccepting() {
	if barrier == nil {
		return
	}
	barrier.mu.Lock()
	if !barrier.stopping {
		barrier.stopping = true
		barrier.signalLocked()
	}
	barrier.mu.Unlock()
}

func (barrier *admissionBarrier) waitLocked(ctx context.Context) error {
	changed := barrier.changed
	barrier.mu.Unlock()
	select {
	case <-ctx.Done():
		barrier.mu.Lock()
		return ctx.Err()
	case <-changed:
		barrier.mu.Lock()
		return nil
	}
}

func (barrier *admissionBarrier) signalLocked() {
	close(barrier.changed)
	barrier.changed = make(chan struct{})
}

func (barrier *admissionBarrier) closeToken() {
	barrier.mu.Lock()
	defer barrier.mu.Unlock()
	if barrier.active == 0 {
		return
	}
	barrier.active--
	barrier.signalLocked()
}

func (barrier *admissionBarrier) current() (publication.AdmissionMode, uint64) {
	barrier.mu.Lock()
	defer barrier.mu.Unlock()
	return barrier.mode, barrier.generation
}

func (barrier *admissionBarrier) activeCount() int {
	barrier.mu.Lock()
	defer barrier.mu.Unlock()
	return barrier.active
}

func (barrier *admissionBarrier) isStopping() bool {
	barrier.mu.Lock()
	defer barrier.mu.Unlock()
	return barrier.stopping
}

type admissionToken struct {
	barrier    *admissionBarrier
	mode       publication.AdmissionMode
	generation uint64
	operation  publication.ResticOperation
	once       sync.Once
}

func (token *admissionToken) Generation() uint64 { return token.generation }
func (token *admissionToken) Mode() publication.AdmissionMode {
	return token.mode
}
func (token *admissionToken) Operation() publication.ResticOperation { return token.operation }
func (token *admissionToken) Close() error {
	if token == nil || token.barrier == nil {
		return nil
	}
	token.once.Do(token.barrier.closeToken)
	return nil
}

var _ publication.Admission = (*admissionBarrier)(nil)
var _ publication.AdmissionToken = (*admissionToken)(nil)
