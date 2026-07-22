package updater

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestActivationJournalRecoversCommittedSwapOrRollsBackUncommittedSwap(t *testing.T) {
	root := newStoreTestRoot(t)
	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	storedBundles := []VerifiedBundle{
		verifiedBundleForStore(t, []BundleFilePayload{{Path: "bundle.dat", Mode: 0o444, Content: []byte("old-bundle")}}),
		verifiedBundleForStore(t, []BundleFilePayload{{Path: "bundle.dat", Mode: 0o444, Content: []byte("new-bundle")}}),
	}
	for _, bundle := range storedBundles {
		if _, err := store.StoreBundle(context.Background(), bundle); err != nil {
			t.Fatal(err)
		}
	}
	oldFingerprint := storedBundles[0].BundleFingerprint
	newFingerprint := storedBundles[1].BundleFingerprint
	activator, err := NewActivator(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := activator.Activate(context.Background(), ActivationRequest{
		CandidateID: strings.Repeat("1", 32), ExpectedOldFingerprint: "", NewFingerprint: oldFingerprint,
	}); err != nil {
		t.Fatal(err)
	}
	if err := activator.Recover(context.Background(), oldFingerprint); err != nil {
		t.Fatal(err)
	}

	activator.fault = func(stage activationStage) error {
		if stage == activationAfterSwap {
			return errors.New("simulated crash after pointer rename")
		}
		return nil
	}
	if _, err := activator.Activate(context.Background(), ActivationRequest{
		CandidateID: strings.Repeat("2", 32), ExpectedOldFingerprint: oldFingerprint, NewFingerprint: newFingerprint,
	}); !errors.Is(err, ErrActivationFailed) {
		t.Fatalf("post-swap crash error=%v", err)
	}
	if active, err := activator.ActiveFingerprint(); err != nil || active != newFingerprint {
		t.Fatalf("post-swap active=%q err=%v", active, err)
	}
	activator.fault = nil
	if err := activator.Recover(context.Background(), oldFingerprint); err != nil {
		t.Fatal(err)
	}
	if active, err := activator.ActiveFingerprint(); err != nil || active != oldFingerprint {
		t.Fatalf("rollback active=%q err=%v", active, err)
	}

	if _, err := activator.Activate(context.Background(), ActivationRequest{
		CandidateID: strings.Repeat("3", 32), ExpectedOldFingerprint: oldFingerprint, NewFingerprint: newFingerprint,
	}); err != nil {
		t.Fatal(err)
	}
	if err := activator.Recover(context.Background(), newFingerprint); err != nil {
		t.Fatal(err)
	}
	if active, err := activator.ActiveFingerprint(); err != nil || active != newFingerprint {
		t.Fatalf("committed recovery active=%q err=%v", active, err)
	}
	if _, err := activator.Activate(context.Background(), ActivationRequest{
		CandidateID: strings.Repeat("4", 32), ExpectedOldFingerprint: oldFingerprint, NewFingerprint: oldFingerprint,
	}); !errors.Is(err, ErrActivationFailed) {
		t.Fatalf("stale expected-old activation error=%v", err)
	}
}

func TestActivationCrashBeforeSwapLeavesOldPointerRecoverable(t *testing.T) {
	root := newStoreTestRoot(t)
	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	bundle := verifiedBundleForStore(t, []BundleFilePayload{{Path: "bundle.dat", Mode: 0o444, Content: []byte("bundle")}})
	fingerprint := bundle.BundleFingerprint
	if _, err := store.StoreBundle(context.Background(), bundle); err != nil {
		t.Fatal(err)
	}
	activator, err := NewActivator(root)
	if err != nil {
		t.Fatal(err)
	}
	activator.fault = func(stage activationStage) error {
		if stage == activationAfterJournal {
			return errors.New("simulated crash before pointer rename")
		}
		return nil
	}
	if _, err := activator.Activate(context.Background(), ActivationRequest{
		CandidateID: strings.Repeat("5", 32), ExpectedOldFingerprint: "", NewFingerprint: fingerprint,
	}); !errors.Is(err, ErrActivationFailed) {
		t.Fatalf("pre-swap crash error=%v", err)
	}
	if active, err := activator.ActiveFingerprint(); err != nil || active != "" {
		t.Fatalf("pre-swap active=%q err=%v", active, err)
	}
	activator.fault = nil
	if err := activator.Recover(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if active, err := activator.ActiveFingerprint(); err != nil || active != "" {
		t.Fatalf("recovered empty active=%q err=%v", active, err)
	}
}
