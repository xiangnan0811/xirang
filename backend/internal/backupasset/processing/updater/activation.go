package updater

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

type activationStage string

const (
	activationAfterJournal activationStage = "after_journal"
	activationAfterSwap    activationStage = "after_swap"
)

type ActivationRequest struct {
	CandidateID            string
	ExpectedOldFingerprint string
	NewFingerprint         string
}

type ActivationReceipt struct {
	SchemaVersion  int    `json:"schema_version"`
	CandidateID    string `json:"candidate_id"`
	OldFingerprint string `json:"old_fingerprint"`
	NewFingerprint string `json:"new_fingerprint"`
	State          string `json:"state"`
}

type activationJournal struct {
	SchemaVersion  int    `json:"schema_version"`
	CandidateID    string `json:"candidate_id"`
	OldFingerprint string `json:"old_fingerprint"`
	NewFingerprint string `json:"new_fingerprint"`
}

type Activator struct {
	root        string
	bundlesRoot string
	activePath  string
	journalPath string
	fault       func(activationStage) error
}

func NewActivator(root string) (*Activator, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || root == string(os.PathSeparator) || strings.ContainsAny(root, "\x00\r\n") {
		return nil, ErrActivationFailed
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != sharedStoreRootMode {
		return nil, ErrActivationFailed
	}
	bundlesRoot := filepath.Join(root, "bundles")
	info, err = os.Lstat(bundlesRoot)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != sharedStoreRootMode {
		return nil, ErrActivationFailed
	}
	return &Activator{
		root: root, bundlesRoot: bundlesRoot,
		activePath: filepath.Join(root, "active"), journalPath: filepath.Join(root, "activation-journal.json"),
	}, nil
}

func (activator *Activator) Activate(ctx context.Context, request ActivationRequest) (ActivationReceipt, error) {
	if activator == nil || ctx == nil || !lowerHex(request.CandidateID, 32) ||
		(request.ExpectedOldFingerprint != "" && !lowerHex(request.ExpectedOldFingerprint, 64)) ||
		!lowerHex(request.NewFingerprint, 64) || request.NewFingerprint == request.ExpectedOldFingerprint {
		return ActivationReceipt{}, ErrActivationFailed
	}
	if err := ctx.Err(); err != nil {
		return ActivationReceipt{}, err
	}
	if err := activator.validateBundleRoot(request.NewFingerprint); err != nil {
		return ActivationReceipt{}, err
	}
	if _, err := os.Lstat(activator.journalPath); err == nil || !errors.Is(err, os.ErrNotExist) {
		return ActivationReceipt{}, ErrActivationFailed
	}
	active, err := activator.ActiveFingerprint()
	if err != nil || active != request.ExpectedOldFingerprint {
		return ActivationReceipt{}, ErrActivationFailed
	}
	journal := activationJournal{
		SchemaVersion: 1, CandidateID: request.CandidateID,
		OldFingerprint: request.ExpectedOldFingerprint, NewFingerprint: request.NewFingerprint,
	}
	if err := activator.writeJournal(journal); err != nil {
		return ActivationReceipt{}, err
	}
	if activator.fault != nil && activator.fault(activationAfterJournal) != nil {
		return ActivationReceipt{}, ErrActivationFailed
	}
	if err := activator.swapActive(request.NewFingerprint); err != nil {
		return ActivationReceipt{}, err
	}
	if activator.fault != nil && activator.fault(activationAfterSwap) != nil {
		return ActivationReceipt{}, ErrActivationFailed
	}
	return ActivationReceipt{
		SchemaVersion: 1, CandidateID: request.CandidateID,
		OldFingerprint: request.ExpectedOldFingerprint, NewFingerprint: request.NewFingerprint, State: "swapped",
	}, nil
}

func (activator *Activator) Recover(ctx context.Context, committedFingerprint string) error {
	if activator == nil || ctx == nil || committedFingerprint != "" && !lowerHex(committedFingerprint, 64) {
		return ErrActivationFailed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	journal, err := activator.readJournal()
	if errors.Is(err, os.ErrNotExist) {
		active, activeErr := activator.ActiveFingerprint()
		if activeErr != nil || active != committedFingerprint {
			return ErrActivationFailed
		}
		return nil
	}
	if err != nil {
		return err
	}
	target := journal.OldFingerprint
	if committedFingerprint == journal.NewFingerprint {
		target = journal.NewFingerprint
	} else if committedFingerprint != journal.OldFingerprint {
		return ErrActivationFailed
	}
	if target != "" {
		if err := activator.validateBundleRoot(target); err != nil {
			return err
		}
	}
	if err := activator.swapActive(target); err != nil {
		return err
	}
	if err := os.Remove(activator.journalPath); err != nil {
		return ErrActivationFailed
	}
	if err := syncDirectory(activator.root); err != nil {
		return ErrActivationFailed
	}
	return nil
}

func (activator *Activator) ActiveFingerprint() (string, error) {
	if activator == nil {
		return "", ErrActivationFailed
	}
	info, err := os.Lstat(activator.activePath)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		return "", ErrActivationFailed
	}
	target, err := os.Readlink(activator.activePath)
	if err != nil || filepath.IsAbs(target) || filepath.Clean(target) != target {
		return "", ErrActivationFailed
	}
	prefix := "bundles" + string(os.PathSeparator)
	if !strings.HasPrefix(target, prefix) {
		return "", ErrActivationFailed
	}
	fingerprint := strings.TrimPrefix(target, prefix)
	if !lowerHex(fingerprint, 64) || strings.Contains(fingerprint, string(os.PathSeparator)) || activator.validateBundleRoot(fingerprint) != nil {
		return "", ErrActivationFailed
	}
	return fingerprint, nil
}

func (activator *Activator) validateBundleRoot(fingerprint string) error {
	if !lowerHex(fingerprint, 64) {
		return ErrActivationFailed
	}
	info, err := os.Lstat(filepath.Join(activator.bundlesRoot, fingerprint))
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o555 {
		return ErrActivationFailed
	}
	return nil
}

func (activator *Activator) writeJournal(journal activationJournal) error {
	payload, err := json.Marshal(journal)
	if err != nil {
		return ErrActivationFailed
	}
	temporary, err := privateTemporaryPath(activator.root, ".activation-")
	if err != nil {
		return err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(temporary)
		}
	}()
	file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return ErrActivationFailed
	}
	written, writeErr := file.Write(payload)
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil || written != len(payload) {
		return ErrActivationFailed
	}
	if err := os.Rename(temporary, activator.journalPath); err != nil {
		return ErrActivationFailed
	}
	cleanup = false
	if err := syncDirectory(activator.root); err != nil {
		return ErrActivationFailed
	}
	return nil
}

func (activator *Activator) readJournal() (activationJournal, error) {
	info, err := os.Lstat(activator.journalPath)
	if err != nil {
		return activationJournal{}, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() <= 0 || info.Size() > 4096 {
		return activationJournal{}, ErrActivationFailed
	}
	payload, err := os.ReadFile(activator.journalPath)
	if err != nil || rejectDuplicateJSONMembers(payload) != nil {
		return activationJournal{}, ErrActivationFailed
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var journal activationJournal
	if decoder.Decode(&journal) != nil || ensureJSONEOF(decoder) != nil || journal.SchemaVersion != 1 ||
		!lowerHex(journal.CandidateID, 32) || journal.OldFingerprint != "" && !lowerHex(journal.OldFingerprint, 64) ||
		!lowerHex(journal.NewFingerprint, 64) || journal.NewFingerprint == journal.OldFingerprint {
		return activationJournal{}, ErrActivationFailed
	}
	canonical, err := json.Marshal(journal)
	if err != nil || !bytes.Equal(canonical, payload) {
		return activationJournal{}, ErrActivationFailed
	}
	return journal, nil
}

func (activator *Activator) swapActive(fingerprint string) error {
	if fingerprint == "" {
		if err := os.Remove(activator.activePath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return ErrActivationFailed
		}
		if err := syncDirectory(activator.root); err != nil {
			return ErrActivationFailed
		}
		return nil
	}
	if err := activator.validateBundleRoot(fingerprint); err != nil {
		return err
	}
	temporary, err := privateTemporaryPath(activator.root, ".active-")
	if err != nil {
		return err
	}
	_ = os.Remove(temporary)
	target := filepath.Join("bundles", fingerprint)
	if err := os.Symlink(target, temporary); err != nil {
		return ErrActivationFailed
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(temporary)
		}
	}()
	if err := syncDirectory(activator.root); err != nil || os.Rename(temporary, activator.activePath) != nil {
		return ErrActivationFailed
	}
	cleanup = false
	if err := syncDirectory(activator.root); err != nil {
		return ErrActivationFailed
	}
	return nil
}

func privateTemporaryPath(root, prefix string) (string, error) {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return "", ErrActivationFailed
	}
	return filepath.Join(root, prefix+hex.EncodeToString(buffer)), nil
}
