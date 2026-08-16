package recovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"gorm.io/gorm"
)

type recoveryPlanTargetReader interface {
	ReadDir(context.Context, string, int) ([]os.FileInfo, error)
	OpenRead(string) (io.ReadCloser, error)
	ReadLink(string) (string, error)
}

type recoveryPlanTargetSFTPReader struct{ client *sftp.Client }

func (reader recoveryPlanTargetSFTPReader) ReadDir(ctx context.Context, value string, maxRows int) ([]os.FileInfo, error) {
	if reader.client == nil || ctx == nil || maxRows < 0 {
		return nil, ErrRecoveryTargetUnavailable
	}
	entries, err := reader.client.ReadDirContext(ctx, value)
	if err != nil {
		return nil, err
	}
	if len(entries) > maxRows {
		return nil, ErrRecoveryOperationLimit
	}
	return entries, nil
}

func (reader recoveryPlanTargetSFTPReader) OpenRead(value string) (io.ReadCloser, error) {
	return reader.client.Open(value)
}

func (reader recoveryPlanTargetSFTPReader) ReadLink(value string) (string, error) {
	return reader.client.ReadLink(value)
}

type recoveryPlanTargetEnumerationDependencies struct {
	DB        *gorm.DB
	Roots     RecoveryTargetRootResolver
	Revisions RecoveryNodeRevisionSource
	Sessions  recoveryEligibilityTargetSessionOpener
	Now       func() time.Time
}

type recoveryPlanTargetEnumerationAuthority struct {
	db        *gorm.DB
	roots     RecoveryTargetRootResolver
	revisions RecoveryNodeRevisionSource
	sessions  recoveryEligibilityTargetSessionOpener
	now       func() time.Time
}

// NewRecoveryPlanTargetEnumerationAuthority constructs the only production
// owner of pre-create target enumeration. It uses the strict purpose-exact SSH
// session owner and never performs a target mutation.
func NewRecoveryPlanTargetEnumerationAuthority(
	db *gorm.DB,
	roots RecoveryTargetRootResolver,
	revisions RecoveryNodeRevisionSource,
	now func() time.Time,
) (RecoveryPlanTargetEnumerationAuthority, error) {
	if db == nil || roots == nil || revisions == nil {
		return nil, ErrRecoveryTargetUnavailable
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return newRecoveryPlanTargetEnumerationAuthorityForTest(recoveryPlanTargetEnumerationDependencies{
		DB: db, Roots: roots, Revisions: revisions,
		Sessions: newRecoveryEligibilityTargetProductionSessions(db, revisions, now), Now: now,
	}), nil
}

func newRecoveryPlanTargetEnumerationAuthorityForTest(
	dependencies recoveryPlanTargetEnumerationDependencies,
) *recoveryPlanTargetEnumerationAuthority {
	return &recoveryPlanTargetEnumerationAuthority{
		db: dependencies.DB, roots: dependencies.Roots, revisions: dependencies.Revisions,
		sessions: dependencies.Sessions, now: dependencies.Now,
	}
}

func (authority *recoveryPlanTargetEnumerationAuthority) EnumerateRecoveryPlanTarget(
	ctx context.Context,
	request RecoveryPlanTargetEnumerationRequest,
) (RecoveryPlanTargetEnumeration, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return RecoveryPlanTargetEnumeration{}, err
	}
	if authority == nil || authority.db == nil || authority.roots == nil || authority.revisions == nil ||
		authority.sessions == nil || authority.now == nil || !validRecoveryPlanTargetEnumerationRequest(request) {
		return RecoveryPlanTargetEnumeration{}, ErrRecoveryTargetUnavailable
	}
	now := authority.now().UTC()
	if now.IsZero() || !request.ExpiresAt.UTC().After(now) {
		return RecoveryPlanTargetEnumeration{}, ErrRecoveryTargetUnavailable
	}
	var captured RecoveryEligibilityTargetRootSnapshot
	err := authority.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var captureErr error
		captured, captureErr = authority.captureTargetRootTx(ctx, tx, request)
		return captureErr
	})
	if err != nil {
		return RecoveryPlanTargetEnumeration{}, recoveryPlanTargetEnumerationError(ctx, err)
	}
	session, err := authority.sessions.OpenRecoveryEligibilityTarget(ctx, recoveryEligibilityTargetSessionRequest{
		nodeID: request.TargetNodeID, nodeRevision: captured.NodeRevision,
		credentialRevision: captured.CredentialRevision, purpose: TargetPurposePreflight,
	})
	if err != nil || session == nil {
		if session != nil {
			_ = session.close()
		}
		return RecoveryPlanTargetEnumeration{}, recoveryPlanTargetEnumerationError(ctx, err)
	}
	stopCancellationClose := context.AfterFunc(ctx, func() { _ = session.close() })
	defer stopCancellationClose()
	defer func() { _ = session.close() }()
	identity, identityOK := recoverySourceAuthenticatedNodeIdentity(
		session.hostIdentityProof, session.nodeID, session.registeredNodeEndpoint,
	)
	if !identityOK || identity == "" || identity != session.authenticatedNodeIdentity ||
		session.nodeID != request.TargetNodeID || session.nodeRevision != captured.NodeRevision ||
		session.credentialRevision != captured.CredentialRevision || session.sftp == nil || session.planReader == nil {
		return RecoveryPlanTargetEnumeration{}, ErrRecoveryTargetUnavailable
	}

	anchor := request.Items[0].TargetRelativeLocator
	pathDigest, err := TargetPathDigest(request.TargetRootID, captured.LocatorDigest, anchor)
	if err != nil {
		return RecoveryPlanTargetEnumeration{}, ErrRecoveryTargetUnavailable
	}
	requiredBytes, requiredInodes, ok := recoveryPlanTargetRequirements(request)
	if !ok {
		return RecoveryPlanTargetEnumeration{}, ErrRecoveryOperationLimit
	}
	observationRequest := RecoveryEligibilityTargetObservationRequest{
		Binding: RecoveryAuthorityBinding{
			TargetNodeID: request.TargetNodeID, TargetRootID: request.TargetRootID,
			RootLocatorDigest: captured.LocatorDigest, PathDigest: pathDigest,
			PreflightNodeRevision: captured.NodeRevision, CredentialScopeRevision: captured.CredentialRevision,
			RequiredBytes: requiredBytes, RequiredInodes: requiredInodes,
		},
		TargetRoot: captured, Purpose: TargetPurposePreflight,
		RequiredBytes: requiredBytes, RequiredInodes: requiredInodes,
	}
	firstRoot, err := observeRecoveryEligibilityTargetState(ctx, session.sftp, observationRequest, anchor, session.protectedRoots)
	if err != nil || firstRoot.overlapsXirangRoot ||
		firstRoot.freeBytes < requiredBytes+captured.Policy.ReserveBytes ||
		firstRoot.freeInodes < requiredInodes+captured.Policy.ReserveInodes {
		return RecoveryPlanTargetEnumeration{}, recoveryPlanTargetEnumerationError(ctx, err)
	}
	first, err := enumerateRecoveryPlanTargetOperations(ctx, session, captured, firstRoot, request)
	if err != nil {
		return RecoveryPlanTargetEnumeration{}, recoveryPlanTargetEnumerationError(ctx, err)
	}
	second, err := enumerateRecoveryPlanTargetOperations(ctx, session, captured, firstRoot, request)
	if err != nil {
		return RecoveryPlanTargetEnumeration{}, recoveryPlanTargetEnumerationError(ctx, err)
	}
	secondRoot, err := observeRecoveryEligibilityTargetState(ctx, session.sftp, observationRequest, anchor, session.protectedRoots)
	if err != nil || !firstRoot.sameStableIdentity(secondRoot) ||
		first.OperationSetDigest != second.OperationSetDigest || first.DeleteSetDigest != second.DeleteSetDigest ||
		first.Impact.EstimatedItems != second.Impact.EstimatedItems || first.Impact.EstimatedBytes != second.Impact.EstimatedBytes {
		return RecoveryPlanTargetEnumeration{}, ErrRecoveryTargetChanged
	}
	if err := session.close(); err != nil {
		return RecoveryPlanTargetEnumeration{}, recoveryPlanTargetEnumerationError(ctx, err)
	}
	if err := authority.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, captureErr := authority.captureTargetRootTx(ctx, tx, request)
		if captureErr != nil {
			return captureErr
		}
		if current != captured {
			return ErrRecoveryTargetChanged
		}
		return nil
	}); err != nil {
		return RecoveryPlanTargetEnumeration{}, recoveryPlanTargetEnumerationError(ctx, err)
	}
	return RecoveryPlanTargetEnumeration{
		Target: TargetBinding{
			Mode: request.TargetMode, NodeID: request.TargetNodeID, RootID: request.TargetRootID,
			EncryptedRelativePath: anchor, RootLocatorDigest: captured.LocatorDigest, PathDigest: pathDigest,
			BaseNodeRevision: captured.NodeRevision, CredentialScopeRevision: captured.CredentialRevision,
			RootRevision: firstRoot.rootRevision, FilesystemRevision: firstRoot.filesystemRevision,
		},
		TargetRevision: firstRoot.targetRevision,
		Node: RecoveryPlanTargetNodeEvidence{
			Registered: true, Archived: false, Online: true, Authorized: true,
		},
		Operations: first,
	}, nil
}

func (authority *recoveryPlanTargetEnumerationAuthority) captureTargetRootTx(
	ctx context.Context,
	tx *gorm.DB,
	request RecoveryPlanTargetEnumerationRequest,
) (RecoveryEligibilityTargetRootSnapshot, error) {
	if err := lockRecoveryEligibilityTargetRootRowTx(ctx, tx, request.TargetNodeID, request.TargetRootID); err != nil {
		return RecoveryEligibilityTargetRootSnapshot{}, err
	}
	root, err := authority.roots.ResolveRecoveryTargetRootTx(ctx, tx, request.TargetNodeID, request.TargetRootID)
	if err != nil {
		return RecoveryEligibilityTargetRootSnapshot{}, ErrRecoveryTargetUnavailable
	}
	revisions, err := authority.revisions.ResolveRecoveryNodeRevisionsTx(ctx, tx, request.TargetNodeID, TargetPurposePreflight)
	if err != nil {
		return RecoveryEligibilityTargetRootSnapshot{}, ErrRecoveryTargetUnavailable
	}
	snapshot := recoveryEligibilityTargetRootSnapshot(root, revisions)
	if !recoveryEligibilityTargetRootSnapshotValid(snapshot) || snapshot.NodeID != request.TargetNodeID ||
		snapshot.RootID != request.TargetRootID {
		return RecoveryEligibilityTargetRootSnapshot{}, ErrRecoveryTargetUnavailable
	}
	return snapshot, nil
}

func validRecoveryPlanTargetEnumerationRequest(request RecoveryPlanTargetEnumerationRequest) bool {
	if request.RequesterID == 0 || !validDigest(request.SelectionDigest) || request.TargetMode.Validate() != nil ||
		request.TargetNodeID == 0 || !validBoundedOpaque(request.TargetRootID, targetRootIDMax) ||
		request.ConflictPolicy.Validate() != nil || request.MaxRows <= 0 || request.MaxRows > exactSelectionMaxItems ||
		request.MaxBytes < 0 || len(request.Items) == 0 || len(request.Items) > request.MaxRows || request.ExpiresAt.IsZero() ||
		(request.ConflictPolicy == ConflictExactMirror && request.TargetMode != TargetModeInPlace) {
		return false
	}
	seenRefs := make(map[string]struct{}, len(request.Items))
	seenPaths := make(map[string]struct{}, len(request.Items))
	for _, item := range request.Items {
		if !validTargetRelativeLocator(item.TargetRelativeLocator) || !validDigest(item.ContentDigest) ||
			item.Bytes < 0 || item.DisplayClass != RecoveryDisplayClassRegular ||
			item.AssetRef.RecoveryPointID == "" || item.AssetRef.EntryID == "" {
			return false
		}
		refKey := item.AssetRef.RecoveryPointID + "\x00" + item.AssetRef.EntryID
		if _, duplicate := seenRefs[refKey]; duplicate {
			return false
		}
		if _, duplicate := seenPaths[item.TargetRelativeLocator]; duplicate {
			return false
		}
		seenRefs[refKey] = struct{}{}
		seenPaths[item.TargetRelativeLocator] = struct{}{}
	}
	return true
}

func recoveryPlanTargetRequirements(request RecoveryPlanTargetEnumerationRequest) (int64, int64, bool) {
	var bytes int64
	for _, item := range request.Items {
		if item.Bytes > request.MaxBytes-bytes {
			return 0, 0, false
		}
		bytes += item.Bytes
	}
	return bytes, int64(len(request.Items)), true
}

func enumerateRecoveryPlanTargetOperations(
	ctx context.Context,
	session *recoveryEligibilityTargetSession,
	root RecoveryEligibilityTargetRootSnapshot,
	state recoveryEligibilityTargetState,
	request RecoveryPlanTargetEnumerationRequest,
) (RecoveryOperationProducts, error) {
	operations := make([]RecoveryOperation, 0, request.MaxRows)
	readBudget := request.MaxBytes
	selected := make(map[string]struct{}, len(request.Items))
	keptAncestors := make(map[string]struct{})
	for _, item := range request.Items {
		selected[item.TargetRelativeLocator] = struct{}{}
		for parent := path.Dir(item.TargetRelativeLocator); parent != "."; parent = path.Dir(parent) {
			keptAncestors[parent] = struct{}{}
		}
		prior, priorBytes, present := "", int64(-1), false
		var err error
		if request.TargetMode == TargetModeInPlace {
			prior, priorBytes, present, err = observeRecoveryPlanTargetFile(
				ctx, session, root.Locator, item.TargetRelativeLocator, &readBudget,
			)
			if err != nil {
				return RecoveryOperationProducts{}, err
			}
		}
		kind := RecoveryOperationCreate
		expectedPrior := ExpectedTargetIdentity{Kind: ExpectedTargetAbsent}
		expectedPriorBytes := int64(-1)
		expectedPostDigest := item.ContentDigest
		expectedPostBytes := item.Bytes
		if present {
			expectedPrior = ExpectedTargetIdentity{Kind: ExpectedTargetPresent, Digest: prior}
			expectedPriorBytes = priorBytes
			switch request.ConflictPolicy {
			case ConflictFailOnConflict:
				return RecoveryOperationProducts{}, ErrRecoveryTargetChanged
			case ConflictSkipExisting:
				kind = RecoveryOperationSkip
				expectedPostDigest = prior
				expectedPostBytes = -1
			case ConflictOverwriteSelected, ConflictExactMirror:
				kind = RecoveryOperationOverwrite
			default:
				return RecoveryOperationProducts{}, ErrRecoveryTargetUnavailable
			}
		}
		pathDigest, err := TargetPathDigest(request.TargetRootID, root.LocatorDigest, item.TargetRelativeLocator)
		if err != nil {
			return RecoveryOperationProducts{}, ErrRecoveryTargetUnavailable
		}
		semanticDigest, err := SemanticTargetDigest(request.TargetMode, request.TargetRootID, root.LocatorDigest, item.TargetRelativeLocator)
		if err != nil {
			return RecoveryOperationProducts{}, ErrRecoveryTargetUnavailable
		}
		ref := item.AssetRef
		operations = append(operations, RecoveryOperation{
			Kind: kind, TargetPathDigest: pathDigest, TargetRelativeLocator: item.TargetRelativeLocator,
			SemanticTargetDigest: semanticDigest, ExpectedPrior: expectedPrior,
			ExpectedPostIdentityDigest: expectedPostDigest, ExpectedPostBytes: expectedPostBytes,
			ExpectedPriorBytes: expectedPriorBytes,
			Source:             RecoveryOperationSource{Kind: RecoveryOperationSourceAssetRef, AssetRef: &ref},
			DisplayClass:       item.DisplayClass, EstimatedBytes: item.Bytes,
		})
	}
	if request.ConflictPolicy == ConflictExactMirror {
		inventory, err := enumerateRecoveryPlanTargetInventory(
			ctx, session, root.Locator, state.rootRevision, request.MaxRows, &readBudget, selected, keptAncestors,
		)
		if err != nil {
			return RecoveryOperationProducts{}, err
		}
		for _, entry := range inventory {
			if _, keep := selected[entry.relative]; keep {
				continue
			}
			if _, keep := keptAncestors[entry.relative]; keep {
				continue
			}
			if len(operations) >= request.MaxRows {
				return RecoveryOperationProducts{}, ErrRecoveryOperationLimit
			}
			pathDigest, err := TargetPathDigest(request.TargetRootID, root.LocatorDigest, entry.relative)
			if err != nil {
				return RecoveryOperationProducts{}, ErrRecoveryTargetUnavailable
			}
			semanticDigest, err := SemanticTargetDigest(request.TargetMode, request.TargetRootID, root.LocatorDigest, entry.relative)
			if err != nil {
				return RecoveryOperationProducts{}, ErrRecoveryTargetUnavailable
			}
			operations = append(operations, RecoveryOperation{
				Kind: RecoveryOperationDelete, TargetPathDigest: pathDigest,
				TargetRelativeLocator: entry.relative, SemanticTargetDigest: semanticDigest,
				ExpectedPrior:     ExpectedTargetIdentity{Kind: ExpectedTargetPresent, Digest: entry.identity},
				ExpectedPostBytes: -1, ExpectedPriorBytes: -1,
				Source:       RecoveryOperationSource{Kind: RecoveryOperationSourceNone},
				DisplayClass: entry.class, EstimatedBytes: 0,
			})
		}
	}
	return NewOperationProducts(RecoveryOperationProductsInput{
		TargetMode: request.TargetMode, ConflictPolicy: request.ConflictPolicy, Operations: operations,
		Limits: RecoveryOperationLimits{
			MaxRows: request.MaxRows, MaxItems: request.MaxRows,
			MaxBytes: request.MaxBytes, MaxImpactRows: request.MaxRows,
		},
	})
}

func observeRecoveryPlanTargetFile(
	ctx context.Context,
	session *recoveryEligibilityTargetSession,
	root, relative string,
	budget *int64,
) (string, int64, bool, error) {
	if err := validateRecoveryPlanTargetParents(ctx, session.sftp, root, relative); err != nil {
		return "", 0, false, err
	}
	full := path.Join(root, relative)
	info, err := session.sftp.Lstat(full)
	if err != nil {
		if os.IsNotExist(err) {
			return "", -1, false, nil
		}
		return "", 0, false, ErrRecoveryTargetUnavailable
	}
	if info == nil || !info.Mode().IsRegular() || info.Size() < 0 {
		return "", 0, false, ErrRecoveryTargetChanged
	}
	digest, err := readRecoveryPlanTargetRegular(ctx, session, full, info, budget)
	if err != nil {
		return "", 0, false, err
	}
	return digest, info.Size(), true, nil
}

func validateRecoveryPlanTargetParents(
	ctx context.Context,
	client recoveryEligibilityTargetSFTP,
	root, relative string,
) error {
	current := root
	parts := strings.Split(relative, "/")
	for _, component := range parts[:len(parts)-1] {
		if err := ctx.Err(); err != nil {
			return err
		}
		current = path.Join(current, component)
		info, err := client.Lstat(current)
		if err != nil || info == nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			if os.IsNotExist(err) {
				return nil
			}
			return ErrRecoveryTargetUnavailable
		}
		canonical, err := client.RealPath(current)
		if err != nil || canonical != current {
			return ErrRecoveryTargetChanged
		}
	}
	return nil
}

func readRecoveryPlanTargetRegular(
	ctx context.Context,
	session *recoveryEligibilityTargetSession,
	full string,
	before os.FileInfo,
	budget *int64,
) (string, error) {
	if before.Size() > *budget {
		return "", ErrRecoveryOperationLimit
	}
	reader, err := session.planReader.OpenRead(full)
	if err != nil || reader == nil {
		return "", ErrRecoveryTargetUnavailable
	}
	hasher := sha256.New()
	written, copyErr := io.CopyN(hasher, reader, before.Size()+1)
	closeErr := reader.Close()
	if copyErr != nil && !errors.Is(copyErr, io.EOF) || closeErr != nil || written != before.Size() {
		return "", ErrRecoveryTargetChanged
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	after, err := session.sftp.Lstat(full)
	if err != nil || after == nil || after.Size() != before.Size() || after.Mode() != before.Mode() ||
		!after.ModTime().Equal(before.ModTime()) {
		return "", ErrRecoveryTargetChanged
	}
	*budget -= written
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

type recoveryPlanTargetInventoryEntry struct {
	relative string
	identity string
	class    RecoveryDisplayClass
}

func enumerateRecoveryPlanTargetInventory(
	ctx context.Context,
	session *recoveryEligibilityTargetSession,
	root string,
	rootRevision string,
	maxRows int,
	budget *int64,
	selected map[string]struct{},
	keptAncestors map[string]struct{},
) ([]recoveryPlanTargetInventoryEntry, error) {
	result := make([]recoveryPlanTargetInventoryEntry, 0, maxRows)
	visited := 0
	var walk func(string, string) error
	walk = func(directory, relativeParent string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries, err := session.planReader.ReadDir(ctx, directory, maxRows-visited)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(err, ErrRecoveryOperationLimit) {
				return err
			}
			return ErrRecoveryTargetUnavailable
		}
		sort.Slice(entries, func(left, right int) bool { return entries[left].Name() < entries[right].Name() })
		for _, entry := range entries {
			if entry == nil || entry.Name() == "" || entry.Name() == "." || entry.Name() == ".." || strings.Contains(entry.Name(), "/") {
				return ErrRecoveryTargetUnavailable
			}
			visited++
			if visited > maxRows {
				return ErrRecoveryOperationLimit
			}
			relative := entry.Name()
			if relativeParent != "" {
				relative = path.Join(relativeParent, entry.Name())
			}
			full := path.Join(root, relative)
			current, err := session.sftp.Lstat(full)
			if err != nil || current == nil || current.Name() != entry.Name() {
				return ErrRecoveryTargetChanged
			}
			if _, keep := selected[relative]; keep {
				continue
			}
			if _, keep := keptAncestors[relative]; keep {
				if !current.IsDir() || current.Mode()&os.ModeSymlink != 0 {
					return ErrRecoveryTargetChanged
				}
				if err := walk(full, relative); err != nil {
					return err
				}
				continue
			}
			observation, err := recoveryDeleteEntryMetadata(current)
			if err != nil {
				return err
			}
			class := recoveryDisplayClassForTargetEntry(observation.result.Kind)
			switch observation.result.Kind {
			case TargetEntryRegular:
				observation.payloadFact, err = readRecoveryPlanTargetRegular(ctx, session, full, current, budget)
			case TargetEntrySymlink:
				observation.payloadFact, err = session.planReader.ReadLink(full)
				if err == nil {
					if int64(len(observation.payloadFact)) > *budget {
						err = ErrRecoveryOperationLimit
					} else {
						*budget -= int64(len(observation.payloadFact))
					}
				}
			case TargetEntryDirectory, TargetEntrySpecial:
			default:
				err = ErrRecoveryTargetUnavailable
			}
			if err != nil || class == "" {
				return recoveryPlanTargetEnumerationError(ctx, err)
			}
			identity := recoveryDeleteEntryIdentityDigest(rootRevision, relative, observation)
			if !validDigest(identity) {
				return ErrRecoveryTargetUnavailable
			}
			result = append(result, recoveryPlanTargetInventoryEntry{relative: relative, identity: identity, class: class})
			if observation.result.Kind == TargetEntryDirectory {
				if err := walk(full, relative); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(root, ""); err != nil {
		return nil, err
	}
	return result, nil
}

func recoveryDisplayClassForTargetEntry(kind TargetEntryKind) RecoveryDisplayClass {
	switch kind {
	case TargetEntryRegular:
		return RecoveryDisplayClassRegular
	case TargetEntryDirectory:
		return RecoveryDisplayClassDirectory
	case TargetEntrySymlink:
		return RecoveryDisplayClassLink
	case TargetEntrySpecial:
		return RecoveryDisplayClassSpecial
	default:
		return ""
	}
}

func recoveryPlanTargetEnumerationError(ctx context.Context, err error) error {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	if errors.Is(err, ErrRecoveryOperationLimit) || errors.Is(err, ErrRecoveryImpactLimit) {
		return err
	}
	if errors.Is(err, ErrRecoveryTargetChanged) {
		return ErrRecoveryTargetChanged
	}
	return ErrRecoveryTargetUnavailable
}

var _ RecoveryPlanTargetEnumerationAuthority = (*recoveryPlanTargetEnumerationAuthority)(nil)
var _ recoveryPlanTargetReader = recoveryPlanTargetSFTPReader{}
