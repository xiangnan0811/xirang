package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"xirang/backend/internal/alerting"
	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/content"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/backupasset/recovery"
	"xirang/backend/internal/backupasset/repository"
	"xirang/backend/internal/logger"
	"xirang/backend/internal/model"
	"xirang/backend/internal/settings"
	"xirang/backend/internal/sshutil"

	"golang.org/x/crypto/ssh"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const recoveryRuntimeTransitionTimeout = 30 * time.Second

const (
	managedRecoveryNodeRevisionDomain        = "xirang/recovery/runtime-node-revision/v1"
	managedRecoveryCredentialRevisionDomain  = "xirang/recovery/runtime-credential-revision/v1"
	managedRecoveryReconciliationAlertDomain = "xirang/recovery/runtime-reconciliation-alert/v1"
	managedRecoveryReconciliationAlertPrefix = "XR-RECOVERY-RECONCILE-"
)

var managedRecoveryCredentialPurposes = []string{
	sshutil.PurposeRecoveryPreflight,
	sshutil.PurposeRecoveryWrite,
	sshutil.PurposeRecoveryVerify,
	sshutil.PurposeRecoveryResultRead,
	sshutil.PurposeRecoveryCleanup,
	sshutil.PurposeRecoveryReconcile,
	sshutil.PurposeRecoveryTargetRootRegistration,
}

type managedRecoveryNodeRevisionSource struct {
	now func() time.Time
}

func newManagedRecoveryNodeRevisionSource(now func() time.Time) *managedRecoveryNodeRevisionSource {
	return &managedRecoveryNodeRevisionSource{now: now}
}

func (source *managedRecoveryNodeRevisionSource) ResolveRecoveryNodeRevisionsTx(
	ctx context.Context,
	tx *gorm.DB,
	nodeID uint,
	purpose recovery.TargetPurpose,
) (recovery.RecoveryNodeRevisionSnapshot, error) {
	if source == nil || source.now == nil || ctx == nil || tx == nil || nodeID == 0 ||
		!managedRecoveryPurposeAllowed(purpose) {
		return recovery.RecoveryNodeRevisionSnapshot{}, recovery.ErrRecoveryTargetUnavailable
	}
	if err := ctx.Err(); err != nil {
		return recovery.RecoveryNodeRevisionSnapshot{}, err
	}
	authorityNow := source.now().UTC()
	if authorityNow.IsZero() {
		return recovery.RecoveryNodeRevisionSnapshot{}, recovery.ErrRecoveryTargetUnavailable
	}
	var node model.Node
	loaded := tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("id = ?", nodeID).Limit(1).Find(&node)
	if loaded.Error != nil || loaded.RowsAffected != 1 || node.ID != nodeID || node.Archived ||
		node.AuthType != "key" || node.SSHKeyID == nil || *node.SSHKeyID == 0 ||
		strings.TrimSpace(node.Password) != "" || strings.TrimSpace(node.PrivateKey) != "" {
		return recovery.RecoveryNodeRevisionSnapshot{}, recovery.ErrRecoveryTargetUnavailable
	}
	var key model.SSHKey
	loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("id = ?", *node.SSHKeyID).Limit(1).Find(&key)
	if loaded.Error != nil || loaded.RowsAffected != 1 || key.ID != *node.SSHKeyID {
		return recovery.RecoveryNodeRevisionSnapshot{}, recovery.ErrRecoveryTargetUnavailable
	}
	canonicalPurposes, canonicalNodes, exact := managedRecoveryCredentialScope(node, key, authorityNow)
	if !exact {
		return recovery.RecoveryNodeRevisionSnapshot{}, recovery.ErrRecoveryTargetUnavailable
	}
	preparedKey, _, err := sshutil.ValidateAndPreparePrivateKey(key.PrivateKey, key.KeyType)
	if err != nil || preparedKey != key.PrivateKey {
		return recovery.RecoveryNodeRevisionSnapshot{}, recovery.ErrRecoveryTargetUnavailable
	}
	if _, err := ssh.ParsePrivateKey([]byte(preparedKey)); err != nil ||
		managedRecoveryPrivateKeyFingerprint(preparedKey) != key.Fingerprint {
		return recovery.RecoveryNodeRevisionSnapshot{}, recovery.ErrRecoveryTargetUnavailable
	}

	nodeRevision := managedRecoveryRevisionDigest(
		managedRecoveryNodeRevisionDomain,
		strconv.FormatUint(uint64(node.ID), 10), node.Host, strconv.Itoa(node.Port), node.Username,
		node.AuthType, strconv.FormatUint(uint64(key.ID), 10), strconv.FormatBool(node.Archived),
	)
	credentialRevision := managedRecoveryRevisionDigest(
		managedRecoveryCredentialRevisionDomain,
		strconv.FormatUint(uint64(key.ID), 10), key.Username, key.KeyType, key.Fingerprint,
		strconv.FormatBool(key.Disabled), managedRecoveryOptionalTime(key.ExpiresAt),
		canonicalPurposes, canonicalNodes, "",
	)
	return recovery.RecoveryNodeRevisionSnapshot{
		NodeRevision: nodeRevision, CredentialRevision: credentialRevision,
	}, nil
}

func managedRecoveryPurposeAllowed(purpose recovery.TargetPurpose) bool {
	return slices.Contains(managedRecoveryCredentialPurposes, string(purpose))
}

func managedRecoveryCredentialScope(node model.Node, key model.SSHKey, now time.Time) (string, string, bool) {
	if key.ID == 0 || key.Disabled || (key.ExpiresAt != nil && !key.ExpiresAt.After(now)) ||
		strings.TrimSpace(key.PrivateKey) == "" || strings.TrimSpace(key.Fingerprint) == "" ||
		strings.TrimSpace(key.AllowedNodeTags) != "" {
		return "", "", false
	}
	normalizedPurposes, err := sshutil.NormalizePurposeList(key.AllowedPurposes)
	if err != nil {
		return "", "", false
	}
	purposeSet := strings.Split(normalizedPurposes, ",")
	if len(purposeSet) != len(managedRecoveryCredentialPurposes) {
		return "", "", false
	}
	for _, purpose := range managedRecoveryCredentialPurposes {
		if !slices.Contains(purposeSet, purpose) {
			return "", "", false
		}
	}
	normalizedNodes, err := sshutil.NormalizeNodeIDList(key.AllowedNodeIDs)
	if err != nil || normalizedNodes != strconv.FormatUint(uint64(node.ID), 10) {
		return "", "", false
	}
	return strings.Join(managedRecoveryCredentialPurposes, ","), normalizedNodes, true
}

func managedRecoveryPrivateKeyFingerprint(privateKey string) string {
	digest := sha256.Sum256([]byte(privateKey))
	return "SHA256:" + base64.StdEncoding.EncodeToString(digest[:])
}

func managedRecoveryOptionalTime(value *time.Time) string {
	if value == nil {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func managedRecoveryRevisionDigest(domain string, values ...string) string {
	buffer := bytes.NewBuffer(nil)
	managedRecoveryWriteDigestString(buffer, domain)
	var count [8]byte
	binary.BigEndian.PutUint64(count[:], uint64(len(values)))
	buffer.Write(count[:])
	for _, value := range values {
		managedRecoveryWriteDigestString(buffer, value)
	}
	digest := sha256.Sum256(buffer.Bytes())
	return hex.EncodeToString(digest[:])
}

func managedRecoveryWriteDigestString(buffer *bytes.Buffer, value string) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	buffer.Write(length[:])
	buffer.WriteString(value)
}

// These adapters deliberately keep admission closed until the missing live
// capability, policy/finding, and root revision registries have production
// sources. Constructing the graph must not turn frozen plan data into current
// authority.
type managedRecoveryUnavailablePreflightEvidenceAuthority struct{}

func (managedRecoveryUnavailablePreflightEvidenceAuthority) ObserveRecoveryPreflightEvidence(
	ctx context.Context,
	_ recovery.PreflightExternalEvidenceRequest,
) (recovery.PreflightExternalEvidenceObservation, error) {
	if ctx != nil && ctx.Err() != nil {
		return recovery.PreflightExternalEvidenceObservation{}, ctx.Err()
	}
	return recovery.PreflightExternalEvidenceObservation{}, recovery.ErrTargetPreflightUnavailable
}

type managedRecoveryUnavailableAuthorityRevalidator struct{}

func (managedRecoveryUnavailableAuthorityRevalidator) ObserveRecoveryAuthority(
	ctx context.Context,
	_ recovery.RecoveryAuthorityBinding,
) (recovery.RecoveryAuthorityObservation, error) {
	if ctx != nil && ctx.Err() != nil {
		return recovery.RecoveryAuthorityObservation{}, ctx.Err()
	}
	return recovery.RecoveryAuthorityObservation{}, recovery.ErrRecoveryTargetUnavailable
}

func (managedRecoveryUnavailableAuthorityRevalidator) RevalidateRecoveryAuthorityTx(
	ctx context.Context,
	_ *gorm.DB,
	_ recovery.RecoveryAuthorityBinding,
	_ recovery.RecoveryAuthorityObservation,
) error {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return recovery.ErrRecoveryTargetChanged
}

type managedRecoveryUnavailableReconciliationRevisionSource struct{}

func (managedRecoveryUnavailableReconciliationRevisionSource) ResolveRecoveryReconciliationRevisionsTx(
	ctx context.Context,
	_ *gorm.DB,
	_ uint,
	_ string,
) (recovery.RecoveryReconciliationRevisionSnapshot, error) {
	if ctx != nil && ctx.Err() != nil {
		return recovery.RecoveryReconciliationRevisionSnapshot{}, ctx.Err()
	}
	return recovery.RecoveryReconciliationRevisionSnapshot{}, recovery.ErrRecoveryReconciliationUnavailable
}

func managedRecoveryKnownUnavailableProductionAuthorities(
	dependencies managedRecoveryGraphBuildDependencies,
) bool {
	_, preflightUnavailable := dependencies.PreflightEvidence.(managedRecoveryUnavailablePreflightEvidenceAuthority)
	if !preflightUnavailable {
		_, preflightUnavailable = dependencies.PreflightEvidence.(*managedRecoveryUnavailablePreflightEvidenceAuthority)
	}
	_, revalidatorUnavailable := dependencies.AuthorityRevalidator.(managedRecoveryUnavailableAuthorityRevalidator)
	if !revalidatorUnavailable {
		_, revalidatorUnavailable = dependencies.AuthorityRevalidator.(*managedRecoveryUnavailableAuthorityRevalidator)
	}
	_, reconciliationUnavailable := dependencies.ReconciliationRevisions.(managedRecoveryUnavailableReconciliationRevisionSource)
	if !reconciliationUnavailable {
		_, reconciliationUnavailable = dependencies.ReconciliationRevisions.(*managedRecoveryUnavailableReconciliationRevisionSource)
	}
	return preflightUnavailable || revalidatorUnavailable || reconciliationUnavailable
}

type managedRecoveryReconciliationFindingSink struct {
	dispatcher *alerting.Dispatcher
}

type managedRecoveryUnavailableReconciliationFindingSink struct{}

func (managedRecoveryUnavailableReconciliationFindingSink) NotifyRecoveryReconciliation(
	context.Context,
	recovery.RecoveryReconciliationAlert,
) error {
	return recovery.ErrRecoveryReconciliationUnavailable
}

func newManagedRecoveryReconciliationFindingSink(
	dispatcher *alerting.Dispatcher,
) recovery.RecoveryReconciliationFindingSink {
	if dispatcher == nil || dispatcher.DB == nil {
		return nil
	}
	return &managedRecoveryReconciliationFindingSink{dispatcher: dispatcher}
}

func (sink *managedRecoveryReconciliationFindingSink) NotifyRecoveryReconciliation(
	ctx context.Context,
	alert recovery.RecoveryReconciliationAlert,
) error {
	if sink == nil || sink.dispatcher == nil || sink.dispatcher.DB == nil || ctx == nil ||
		alert.NodeID == 0 || strings.TrimSpace(alert.RootID) == "" {
		return recovery.ErrRecoveryReconciliationUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	errorCode := managedRecoveryReconciliationErrorCode(alert.NodeID, alert.RootID)
	if alert.State == recovery.RecoveryReconciliationClear {
		if err := sink.dispatcher.ResolveAlertsByErrorCode(errorCode, "Recovery reconciliation cleared"); err != nil {
			return recovery.ErrRecoveryReconciliationUnavailable
		}
		return nil
	}
	if alert.State != recovery.RecoveryReconciliationBlocked {
		return recovery.ErrRecoveryReconciliationUnavailable
	}
	var existing int64
	if err := sink.dispatcher.DB.WithContext(ctx).Model(&model.Alert{}).
		Where("node_id = ? AND error_code = ? AND status IN ?", alert.NodeID, errorCode, []string{"open", "acked"}).
		Limit(1).Count(&existing).Error; err != nil {
		return recovery.ErrRecoveryReconciliationUnavailable
	}
	if existing > 0 {
		return nil
	}
	_, _, err := sink.dispatcher.RaiseAnomalyAlert(alerting.AnomalyAlertInput{
		NodeID: alert.NodeID, Severity: "warning", ErrorCode: errorCode,
		Message: "Recovery reconciliation requires attention",
	})
	if err != nil {
		return recovery.ErrRecoveryReconciliationUnavailable
	}
	return nil
}

func managedRecoveryReconciliationErrorCode(nodeID uint, rootID string) string {
	digest := managedRecoveryRevisionDigest(
		managedRecoveryReconciliationAlertDomain,
		strconv.FormatUint(uint64(nodeID), 10), rootID,
	)
	return managedRecoveryReconciliationAlertPrefix + strings.ToUpper(digest[:16])
}

type managedRecoveryRsyncRestoreDependencies struct {
	Resolver provider.RsyncRestoreSourceResolver
	Writer   provider.RsyncTargetWriter
	Runner   provider.RsyncRestoreRunner
}

type managedRecoveryRsyncRestorePort struct {
	*repository.RsyncRestorePort
	resolver provider.RsyncRestoreSourceResolver
	writer   provider.RsyncTargetWriter
	runner   provider.RsyncRestoreRunner
}

func newManagedRecoveryRsyncRestorePort(
	dependencies managedRecoveryRsyncRestoreDependencies,
) (*managedRecoveryRsyncRestorePort, error) {
	if nilManagedRecoveryDependency(dependencies.Resolver) || nilManagedRecoveryDependency(dependencies.Writer) ||
		nilManagedRecoveryDependency(dependencies.Runner) {
		return nil, fmt.Errorf("%w: Recovery Rsync restore dependencies unavailable", backupasset.ErrInvalidState)
	}
	return &managedRecoveryRsyncRestorePort{
		RsyncRestorePort: repository.NewRsyncRestorePort(
			dependencies.Resolver, dependencies.Writer, dependencies.Runner,
		),
		resolver: dependencies.Resolver,
		writer:   dependencies.Writer,
		runner:   dependencies.Runner,
	}, nil
}

func nilManagedRecoveryDependency(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

type managedRecoveryRuntimeDependencies struct {
	Build              func(context.Context, backupasset.RecoveryConfig) (*managedRecoveryGraph, error)
	Validate           func(context.Context, backupasset.RecoveryConfig) error
	Install            func(*managedRecoveryPublication, *managedRecoveryGraph) error
	Publication        *managedRecoveryPublication
	ReceiptOwner       managedRecoveryReceiptOwner
	DowngradeInspector managedRecoveryDowngradeInspector
}

type managedRecoveryGraphBuildDependencies struct {
	DB                      *gorm.DB
	Settings                *settings.Service
	Now                     func() time.Time
	Metrics                 recovery.Metrics
	CleanupWorkerID         string
	CleanupNewTimer         func(time.Duration) managedRecoveryTimer
	SourceLeases            *backupasset.LeaseService
	NodeAdmission           recovery.RecoveryNodeAdmission
	NodeRevisions           recovery.RecoveryNodeRevisionSource
	PreflightEvidence       recovery.RecoveryPreflightExternalEvidenceAuthority
	AuthorityRevalidator    recovery.RecoveryAuthorityRevalidator
	PlanSecurity            recovery.RecoveryPlanSecurityAuthority
	WorkspaceKeys           *backupasset.Keyring
	Audit                   recovery.RecoveryAuthorizationAuditWriter
	ContentLifecycle        recovery.RecoveryResultContentLifecycle
	SourceResolver          provider.RsyncRestoreSourceResolver
	Dialer                  *sshutil.NodeDialer
	ReconciliationRevisions recovery.RecoveryReconciliationRevisionSource
	ReconciliationFindings  recovery.RecoveryReconciliationFindingSink
}

type managedRecoveryReceiptOwner interface {
	Run(context.Context)
	Shutdown(context.Context) error
	PrepareSchemaDown(context.Context, func() error) error
}

type managedRecoveryDowngradeReconciler interface {
	ReconcileDowngradeReadiness(
		context.Context,
		recovery.RecoveryDowngradeReconciliationRequest,
	) (recovery.RecoveryReconciliationResult, error)
}

type managedRecoveryDowngradeInspector interface {
	SnapshotRecoveryDowngradeBlockers(context.Context) (managedRecoveryDowngradeSnapshot, error)
}

type managedRecoveryAuthorizationBackend interface {
	ReplayAuthorization(
		context.Context,
		recovery.RecoveryAuthorizationRequest,
	) (recovery.RecoveryAuthorizationResult, bool, error)
	Authorize(
		context.Context,
		recovery.RecoveryAuthorizationRequest,
	) (recovery.RecoveryAuthorizationResult, error)
}

type managedRecoveryDeleteHandoffValidator interface {
	ValidateDeleteAuthorizationHandoff(
		context.Context,
		recovery.RecoveryAuthorizationRequest,
		recovery.RecoveryAuthorizationResult,
	) (bool, error)
}

type managedRecoveryResultBackend interface {
	content.RecoveryResultAuthorizer
	content.RecoveryResultSourceResolver
}

type managedRecoveryDowngradeSnapshot struct {
	UseLatch bool
	Blockers RecoveryDowngradeBlockers
}

type RecoveryDowngradeReadinessState string

const (
	RecoveryDowngradePristineAllowed RecoveryDowngradeReadinessState = "pristine_downgrade_allowed"
	RecoveryDowngradeBlocked         RecoveryDowngradeReadinessState = "blocked"
	RecoveryDowngradeForwardFixOnly  RecoveryDowngradeReadinessState = "forward_fix_only"
)

type RecoveryDowngradeBlockers struct {
	Jobs                  int64 `json:"jobs"`
	Authorities           int64 `json:"authorities"`
	SourceLeases          int64 `json:"source_leases"`
	NodeLeases            int64 `json:"node_leases"`
	Attempts              int64 `json:"attempts"`
	ResultSets            int64 `json:"result_sets"`
	Results               int64 `json:"results"`
	ContentGrants         int64 `json:"content_grants"`
	ContentRequests       int64 `json:"content_requests"`
	ContentStreams        int64 `json:"content_streams"`
	ContentLeases         int64 `json:"content_leases"`
	OtherRecoveryRows     int64 `json:"other_recovery_rows"`
	ReconciliationBacklog int64 `json:"reconciliation_backlog"`
}

func (blockers RecoveryDowngradeBlockers) any() bool {
	return blockers.Jobs > 0 || blockers.Authorities > 0 || blockers.SourceLeases > 0 ||
		blockers.NodeLeases > 0 || blockers.Attempts > 0 || blockers.ResultSets > 0 ||
		blockers.Results > 0 || blockers.ContentGrants > 0 || blockers.ContentRequests > 0 ||
		blockers.ContentStreams > 0 || blockers.ContentLeases > 0 ||
		blockers.OtherRecoveryRows > 0 || blockers.ReconciliationBacklog > 0
}

type RecoveryDowngradeReadiness struct {
	State               RecoveryDowngradeReadinessState `json:"state"`
	AdmissionGeneration string                          `json:"admission_generation"`
	Blockers            RecoveryDowngradeBlockers       `json:"blockers"`
	Replay              bool                            `json:"replay"`
}

type managedRecoveryDowngradeDBInspector struct {
	db *gorm.DB
}

func managedRecoveryClaimSchedulerRowID() string {
	return recovery.ClaimSchedulerRowID()
}

func managedRecoveryTakeoverSchedulerRowID() string {
	return recovery.TakeoverSchedulerRowID()
}

func newManagedRecoveryDowngradeDBInspector(db *gorm.DB) (*managedRecoveryDowngradeDBInspector, error) {
	if db == nil || db.Dialector == nil || (db.Name() != "sqlite" && db.Name() != "postgres") {
		return nil, fmt.Errorf("%w: Recovery downgrade database unavailable", backupasset.ErrInvalidState)
	}
	return &managedRecoveryDowngradeDBInspector{db: db}, nil
}

func (inspector *managedRecoveryDowngradeDBInspector) SnapshotRecoveryDowngradeBlockers(
	ctx context.Context,
) (managedRecoveryDowngradeSnapshot, error) {
	if inspector == nil || inspector.db == nil {
		return managedRecoveryDowngradeSnapshot{}, fmt.Errorf("%w: Recovery downgrade database unavailable", backupasset.ErrInvalidState)
	}
	ctx = nonNilRecoveryRuntimeContext(ctx)
	var snapshot managedRecoveryDowngradeSnapshot
	err := inspector.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		count := func(destination *int64, table, where string, arguments ...any) error {
			query := tx.WithContext(ctx).Table(table)
			if where != "" {
				query = query.Where(where, arguments...)
			}
			return query.Count(destination).Error
		}
		var latchCount int64
		if err := count(&latchCount, "backup_asset_recovery_evidence", "kind = ?", recovery.RecoverySchemaUseLatchID); err != nil {
			return err
		}
		snapshot.UseLatch = latchCount > 0
		queries := []struct {
			destination *int64
			table       string
			where       string
			arguments   []any
		}{
			{&snapshot.Blockers.Jobs, "backup_asset_recovery_jobs", "", nil},
			{&snapshot.Blockers.Authorities, "backup_asset_recovery_grants", "", nil},
			{&snapshot.Blockers.SourceLeases, "recovery_point_leases", "holder_type = ? AND status = ?", []any{"recovery_job", backupasset.LeaseActive}},
			{&snapshot.Blockers.NodeLeases, "backup_asset_recovery_node_leases", "", nil},
			{&snapshot.Blockers.Attempts, "backup_asset_recovery_attempts", "", nil},
			{&snapshot.Blockers.ResultSets, "backup_asset_recovery_result_sets", "", nil},
			{&snapshot.Blockers.Results, "backup_asset_recovery_results", "", nil},
			{&snapshot.Blockers.ContentGrants, "backup_asset_delivery_grants", "resource_kind = ?", []any{content.DeliveryResourceRecoveryResult}},
		}
		for _, query := range queries {
			if err := count(query.destination, query.table, query.where, query.arguments...); err != nil {
				return err
			}
		}
		if err := tx.WithContext(ctx).Table("backup_asset_delivery_requests AS request_row").
			Joins("JOIN backup_asset_delivery_grants AS grant_row ON grant_row.id = request_row.grant_id").
			Where("grant_row.resource_kind = ?", content.DeliveryResourceRecoveryResult).
			Count(&snapshot.Blockers.ContentRequests).Error; err != nil {
			return err
		}
		if err := tx.WithContext(ctx).Table("recovery_point_leases AS lease_row").
			Joins("JOIN backup_asset_delivery_grants AS grant_row ON grant_row.id = lease_row.owner_id").
			Where("lease_row.holder_type = ? AND grant_row.resource_kind = ?",
				backupasset.LeaseHolderContentSession, content.DeliveryResourceRecoveryResult).
			Count(&snapshot.Blockers.ContentLeases).Error; err != nil {
			return err
		}
		if err := tx.WithContext(ctx).Table("backup_asset_delivery_requests AS request_row").
			Joins("JOIN backup_asset_delivery_grants AS grant_row ON grant_row.id = request_row.grant_id").
			Where("grant_row.resource_kind = ? AND request_row.state IN ?", content.DeliveryResourceRecoveryResult,
				[]string{string(content.RequestReserved), string(content.RequestStreaming)}).
			Count(&snapshot.Blockers.ContentStreams).Error; err != nil {
			return err
		}
		for _, table := range []string{
			"backup_asset_recovery_plans", "backup_asset_recovery_plan_items", "backup_asset_recovery_preflights",
			"backup_asset_recovery_job_items", "backup_asset_recovery_checkpoints",
		} {
			var rows int64
			if err := count(&rows, table, "", nil); err != nil {
				return err
			}
			snapshot.Blockers.OtherRecoveryRows += rows
		}
		var evidenceRows int64
		if err := count(&evidenceRows, "backup_asset_recovery_evidence", `NOT (
			kind = 'scheduler_state' AND ((id = ? AND scheduler_scope = 'claim') OR (id = ? AND scheduler_scope = 'takeover'))
		) AND kind <> ?`, managedRecoveryClaimSchedulerRowID(), managedRecoveryTakeoverSchedulerRowID(), recovery.RecoverySchemaUseLatchID); err != nil {
			return err
		}
		snapshot.Blockers.OtherRecoveryRows += evidenceRows
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelSerializable, ReadOnly: true})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return managedRecoveryDowngradeSnapshot{}, ctxErr
		}
		return managedRecoveryDowngradeSnapshot{}, fmt.Errorf("%w: Recovery downgrade snapshot unavailable", backupasset.ErrInvalidState)
	}
	return snapshot, nil
}

type managedRecoveryGraph struct {
	admissionEnabled       bool
	application            *recovery.ApplicationService
	plan                   *recovery.PlanService
	preflight              *recovery.PreflightService
	authorization          managedRecoveryAuthorizationBackend
	target                 recovery.TargetPort
	workerCoordinator      *recovery.WorkerCoordinator
	deleteHandoffValidator managedRecoveryDeleteHandoffValidator
	worker                 *managedRecoveryWorker
	resultLifecycle        *recovery.ResultLifecycleService
	cleanup                *managedRecoveryCleanupOwner
	resultDelivery         managedRecoveryResultBackend
	reconciliation         *recovery.RecoveryReconciliationService
	downgradeReconciler    managedRecoveryDowngradeReconciler
	reconciliationOwner    *managedRecoveryReconciliationOwner
	reconciliationState    recovery.RecoveryReconciliationState
	rsyncRestorePort       *managedRecoveryRsyncRestorePort

	workerCoordinatorSourceResolver provider.RsyncRestoreSourceResolver
	reconcileMetadata               func(context.Context) error
	stopClaims                      func()
	cancelJoinAttempts              func(context.Context) error
	fenceOwnership                  func(context.Context) error
	revokeDrainDelivery             func(context.Context) error
	shutdownLifecycle               func(context.Context) error
	run                             func(context.Context)

	stopClaimsOnce         sync.Once
	runMu                  sync.Mutex
	runCancel              context.CancelFunc
	runDone                chan struct{}
	deliveryShutdownActive atomic.Bool
}

// managedRecoveryAPIFacade is the narrow live-graph seam used by Task 9
// handlers. Admission mutations acquire the enabled graph; read/cancel/cleanup
// projections remain owned by Recovery's API service.
type managedRecoveryAPIFacade struct {
	publication *managedRecoveryPublication
	api         *recovery.APIService
}

func (facade *managedRecoveryAPIFacade) CreatePlan(
	ctx context.Context,
	request recovery.CreatePlanIntentRequest,
) (recovery.CreatePlanResult, error) {
	if facade == nil || facade.publication == nil {
		return recovery.CreatePlanResult{}, recovery.ErrRecoveryPlanUnavailable
	}
	graph, release, ok := facade.publication.acquireAdmission()
	if !ok || graph.application == nil {
		return recovery.CreatePlanResult{}, recovery.ErrRecoveryPlanUnavailable
	}
	defer release()
	return graph.application.CreatePlan(ctx, request)
}

func (facade *managedRecoveryAPIFacade) Preflight(
	ctx context.Context,
	request recovery.RecoveryPreflightRequest,
) (recovery.RecoveryPreflightView, error) {
	if facade == nil || facade.publication == nil {
		return recovery.RecoveryPreflightView{}, recovery.ErrTargetPreflightUnavailable
	}
	graph, release, ok := facade.publication.acquireAdmission()
	if !ok || graph.application == nil {
		return recovery.RecoveryPreflightView{}, recovery.ErrTargetPreflightUnavailable
	}
	defer release()
	return graph.application.Preflight(ctx, request)
}

func (facade *managedRecoveryAPIFacade) CancelJob(
	ctx context.Context,
	requesterID uint,
	jobID string,
	expectedRevision uint64,
) (recovery.RecoveryJobView, error) {
	if facade == nil || facade.publication == nil || facade.api == nil {
		return recovery.RecoveryJobView{}, recovery.ErrRecoveryAPIUnavailable
	}
	graph, release, ok := facade.publication.acquire()
	if !ok || graph.workerCoordinator == nil {
		return recovery.RecoveryJobView{}, recovery.ErrRecoveryAPIUnavailable
	}
	defer release()
	if err := graph.workerCoordinator.CancelOwnedJob(ctx, recovery.CancelRecoveryJobRequest{
		RequesterID: requesterID, JobID: jobID, ExpectedRevision: expectedRevision,
	}); err != nil {
		if errors.Is(err, recovery.ErrRecoveryWorkerObjectNotFound) {
			return recovery.RecoveryJobView{}, recovery.ErrRecoveryAPIObjectNotFound
		}
		if errors.Is(err, recovery.ErrRecoveryWorkerFenceLost) {
			return recovery.RecoveryJobView{}, recovery.ErrRecoveryAPIConflict
		}
		return recovery.RecoveryJobView{}, recovery.ErrRecoveryAPIUnavailable
	}
	return facade.api.ProjectJob(ctx, requesterID, jobID)
}

func (facade *managedRecoveryAPIFacade) RetainRecoveryResults(
	ctx context.Context,
	request recovery.RetainRecoveryResultsRequest,
) (recovery.RetainedRecoveryResultSet, error) {
	if facade == nil || facade.publication == nil {
		return recovery.RetainedRecoveryResultSet{}, recovery.ErrRecoveryResultUnavailable
	}
	graph, release, ok := facade.publication.acquire()
	if !ok || graph.resultLifecycle == nil {
		return recovery.RetainedRecoveryResultSet{}, recovery.ErrRecoveryResultUnavailable
	}
	defer release()
	return graph.resultLifecycle.Retain(ctx, request)
}

func (facade *managedRecoveryAPIFacade) RequestResultCleanup(
	ctx context.Context,
	request recovery.RecoveryResultCleanupRequest,
) (recovery.RecoveryResultCleanupView, error) {
	if facade == nil || facade.api == nil {
		return recovery.RecoveryResultCleanupView{}, recovery.ErrRecoveryAPIUnavailable
	}
	return facade.api.RequestResultCleanup(ctx, request)
}

type managedRecoveryReconciliationOwner struct {
	reconciler managedRecoveryDowngradeReconciler
	generation string
	cadence    time.Duration
	newTimer   func(time.Duration) managedRecoveryTimer
	record     func(recovery.RecoveryReconciliationState)

	mu      sync.Mutex
	running bool
}

type managedRecoveryReconciliationOwnerDependencies struct {
	Reconciler managedRecoveryDowngradeReconciler
	Generation string
	Cadence    time.Duration
	NewTimer   func(time.Duration) managedRecoveryTimer
	Record     func(recovery.RecoveryReconciliationState)
}

func newManagedRecoveryReconciliationOwner(
	dependencies managedRecoveryReconciliationOwnerDependencies,
) (*managedRecoveryReconciliationOwner, error) {
	if nilManagedRecoveryDependency(dependencies.Reconciler) ||
		strings.TrimSpace(dependencies.Generation) == "" || dependencies.Cadence <= 0 ||
		dependencies.Record == nil {
		return nil, fmt.Errorf("%w: Recovery reconciliation owner dependencies unavailable", backupasset.ErrInvalidState)
	}
	if dependencies.NewTimer == nil {
		dependencies.NewTimer = func(duration time.Duration) managedRecoveryTimer {
			return managedRecoveryStdTimer{timer: time.NewTimer(duration)}
		}
	}
	return &managedRecoveryReconciliationOwner{
		reconciler: dependencies.Reconciler, generation: dependencies.Generation,
		cadence: dependencies.Cadence, newTimer: dependencies.NewTimer, record: dependencies.Record,
	}, nil
}

func (owner *managedRecoveryReconciliationOwner) Run(ctx context.Context) {
	if owner == nil || !owner.beginRun() {
		return
	}
	defer owner.finishRun()
	ctx = nonNilRecoveryRuntimeContext(ctx)
	timer := owner.newTimer(owner.cadence)
	if timer == nil {
		owner.record(recovery.RecoveryReconciliationBlocked)
		return
	}
	defer timer.Stop()

	for {
		if !owner.runPass(ctx) {
			return
		}
		timer.Reset(owner.cadence)
		select {
		case <-ctx.Done():
			return
		case <-timer.Chan():
		}
	}
}

func (owner *managedRecoveryReconciliationOwner) runPass(ctx context.Context) bool {
	result, err := owner.reconciler.ReconcileDowngradeReadiness(
		ctx, recovery.RecoveryDowngradeReconciliationRequest{AdmissionGeneration: owner.generation},
	)
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return false
		}
		owner.record(recovery.RecoveryReconciliationBlocked)
		return true
	}
	owner.record(managedRecoveryReconciliationResultState(result))
	return true
}

func managedRecoveryReconciliationResultState(
	result recovery.RecoveryReconciliationResult,
) recovery.RecoveryReconciliationState {
	if result.State == recovery.RecoveryReconciliationClear && result.Complete &&
		len(result.Findings) == 0 && result.NextCursor == "" {
		return recovery.RecoveryReconciliationClear
	}
	return recovery.RecoveryReconciliationBlocked
}

func (owner *managedRecoveryReconciliationOwner) beginRun() bool {
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.running {
		return false
	}
	owner.running = true
	return true
}

func (owner *managedRecoveryReconciliationOwner) finishRun() {
	owner.mu.Lock()
	owner.running = false
	owner.mu.Unlock()
}

func (graph *managedRecoveryGraph) setReconciliationState(state recovery.RecoveryReconciliationState) {
	graph.runMu.Lock()
	graph.reconciliationState = state
	graph.runMu.Unlock()
}

func (graph *managedRecoveryGraph) currentReconciliationState() recovery.RecoveryReconciliationState {
	graph.runMu.Lock()
	defer graph.runMu.Unlock()
	return graph.reconciliationState
}

type managedRecoveryCleanupOwner struct {
	lifecycle     managedRecoveryCleanupLifecycle
	workerID      string
	cadence       time.Duration
	batchSize     int
	retryBase     time.Duration
	retryMaxDelay time.Duration
	newTimer      func(time.Duration) managedRecoveryTimer
}

type managedRecoveryCleanupLifecycle interface {
	ListScheduledCleanupCandidates(context.Context, int) ([]recovery.ScheduledCleanupCandidate, error)
	ClaimScheduledCleanup(context.Context, recovery.ClaimRecoveryResultCleanupRequest) (recovery.RecoveryResultCleanupClaim, error)
	RevokeRecoveryResultCleanup(context.Context, recovery.RecoveryResultCleanupClaim) (recovery.RecoveryResultCleanupClaim, error)
	DrainRecoveryResultCleanup(context.Context, recovery.RecoveryResultCleanupClaim) (recovery.RecoveryResultCleanupClaim, error)
	ValidateRecoveryResultCleanup(context.Context, recovery.RecoveryResultCleanupClaim) (recovery.RecoveryResultCleanupClaim, error)
	AdvanceRecoveryResultCleanup(context.Context, recovery.RecoveryResultCleanupClaim) (recovery.RecoveryCleanupProgress, error)
	ClaimWorkspaceCleanup(context.Context, recovery.ClaimRecoveryWorkspaceCleanupRequest) (recovery.RecoveryWorkspaceCleanupClaim, error)
	RevokeRecoveryWorkspaceCleanup(context.Context, recovery.RecoveryWorkspaceCleanupClaim) (recovery.RecoveryWorkspaceCleanupClaim, error)
	DrainRecoveryWorkspaceCleanup(context.Context, recovery.RecoveryWorkspaceCleanupClaim) (recovery.RecoveryWorkspaceCleanupClaim, error)
	ValidateRecoveryWorkspaceCleanup(context.Context, recovery.RecoveryWorkspaceCleanupClaim) (recovery.RecoveryWorkspaceCleanupClaim, error)
	AdvanceRecoveryWorkspaceCleanup(context.Context, recovery.RecoveryWorkspaceCleanupClaim) (recovery.RecoveryCleanupProgress, error)
}

var _ managedRecoveryCleanupLifecycle = (*recovery.ResultLifecycleService)(nil)

type managedRecoveryCleanupOwnerDependencies struct {
	Lifecycle     managedRecoveryCleanupLifecycle
	WorkerID      string
	Cadence       time.Duration
	BatchSize     int
	RetryBase     time.Duration
	RetryMaxDelay time.Duration
	NewTimer      func(time.Duration) managedRecoveryTimer
}

func newManagedRecoveryCleanupOwner(
	dependencies managedRecoveryCleanupOwnerDependencies,
) (*managedRecoveryCleanupOwner, error) {
	if dependencies.Lifecycle == nil ||
		strings.TrimSpace(dependencies.WorkerID) == "" || len(dependencies.WorkerID) > 64 ||
		dependencies.Cadence <= 0 || dependencies.BatchSize <= 0 || dependencies.BatchSize > 1000 ||
		dependencies.RetryBase <= 0 || dependencies.RetryMaxDelay < dependencies.RetryBase {
		return nil, fmt.Errorf("%w: Recovery cleanup owner dependencies unavailable", backupasset.ErrInvalidState)
	}
	if dependencies.NewTimer == nil {
		dependencies.NewTimer = func(duration time.Duration) managedRecoveryTimer {
			return managedRecoveryStdTimer{timer: time.NewTimer(duration)}
		}
	}
	return &managedRecoveryCleanupOwner{
		lifecycle: dependencies.Lifecycle,
		workerID:  dependencies.WorkerID, cadence: dependencies.Cadence, batchSize: dependencies.BatchSize,
		retryBase: dependencies.RetryBase, retryMaxDelay: dependencies.RetryMaxDelay,
		newTimer: dependencies.NewTimer,
	}, nil
}

func (owner *managedRecoveryCleanupOwner) Run(ctx context.Context) {
	if owner == nil || owner.newTimer == nil {
		return
	}
	ctx = nonNilRecoveryRuntimeContext(ctx)
	timer := owner.newTimer(owner.cadence)
	if timer == nil {
		return
	}
	defer timer.Stop()
	retryDelay := owner.retryBase
	for {
		_, err := owner.runPass(ctx)
		if ctx.Err() != nil {
			return
		}
		nextDelay := owner.cadence
		if err != nil {
			nextDelay = retryDelay
			retryDelay = nextManagedRecoveryRetryDelay(retryDelay, owner.retryMaxDelay)
		} else {
			retryDelay = owner.retryBase
		}
		timer.Reset(nextDelay)
		select {
		case <-ctx.Done():
			return
		case <-timer.Chan():
		}
	}
}

func (owner *managedRecoveryCleanupOwner) runPass(ctx context.Context) (int, error) {
	if owner == nil || owner.lifecycle == nil ||
		owner.batchSize <= 0 || strings.TrimSpace(owner.workerID) == "" {
		return 0, fmt.Errorf("%w: Recovery cleanup owner unavailable", backupasset.ErrInvalidState)
	}
	ctx = nonNilRecoveryRuntimeContext(ctx)
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	candidates, err := owner.lifecycle.ListScheduledCleanupCandidates(ctx, owner.batchSize)
	if err != nil {
		return 0, fmt.Errorf("load Recovery cleanup candidates: %w", err)
	}
	var passErr error
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return len(candidates), errors.Join(passErr, err)
		}
		switch candidate.Kind {
		case recovery.ScheduledCleanupResultSet:
			err = owner.processResult(ctx, candidate.ID)
		case recovery.ScheduledCleanupWorkspace:
			err = owner.processWorkspace(ctx, candidate.ID)
		default:
			err = fmt.Errorf("%w: Recovery cleanup candidate unavailable", backupasset.ErrInvalidState)
		}
		if err != nil && !errors.Is(err, recovery.ErrRecoveryResultCleanupConflict) {
			passErr = errors.Join(passErr, err)
		}
	}
	return len(candidates), passErr
}

func (owner *managedRecoveryCleanupOwner) processResult(ctx context.Context, resultSetID string) error {
	claim, err := owner.lifecycle.ClaimScheduledCleanup(ctx, recovery.ClaimRecoveryResultCleanupRequest{
		ResultSetID: resultSetID, WorkerID: owner.workerID,
	})
	if err != nil {
		return err
	}
	if claim.Phase == recovery.CleanupPhaseClaimed {
		claim, err = owner.lifecycle.RevokeRecoveryResultCleanup(ctx, claim)
	}
	if err == nil && claim.Phase == recovery.CleanupPhaseRevoked {
		claim, err = owner.lifecycle.DrainRecoveryResultCleanup(ctx, claim)
	}
	if err == nil && claim.Phase == recovery.CleanupPhaseDrained {
		claim, err = owner.lifecycle.ValidateRecoveryResultCleanup(ctx, claim)
	}
	if err == nil && (claim.Phase == recovery.CleanupPhaseValidated ||
		claim.Phase == recovery.CleanupPhaseDeleteStarted || claim.Phase == recovery.CleanupPhaseDeleted) {
		_, err = owner.lifecycle.AdvanceRecoveryResultCleanup(ctx, claim)
	}
	return err
}

func (owner *managedRecoveryCleanupOwner) processWorkspace(ctx context.Context, jobID string) error {
	claim, err := owner.lifecycle.ClaimWorkspaceCleanup(ctx, recovery.ClaimRecoveryWorkspaceCleanupRequest{
		JobID: jobID, WorkerID: owner.workerID,
	})
	if err != nil {
		return err
	}
	if claim.Phase == recovery.CleanupPhaseClaimed {
		claim, err = owner.lifecycle.RevokeRecoveryWorkspaceCleanup(ctx, claim)
	}
	if err == nil && claim.Phase == recovery.CleanupPhaseRevoked {
		claim, err = owner.lifecycle.DrainRecoveryWorkspaceCleanup(ctx, claim)
	}
	if err == nil && claim.Phase == recovery.CleanupPhaseDrained {
		claim, err = owner.lifecycle.ValidateRecoveryWorkspaceCleanup(ctx, claim)
	}
	if err == nil && (claim.Phase == recovery.CleanupPhaseValidated ||
		claim.Phase == recovery.CleanupPhaseDeleteStarted || claim.Phase == recovery.CleanupPhaseDeleted) {
		_, err = owner.lifecycle.AdvanceRecoveryWorkspaceCleanup(ctx, claim)
	}
	return err
}

func buildManagedRecoveryGraph(
	ctx context.Context,
	config backupasset.RecoveryConfig,
	dependencies managedRecoveryGraphBuildDependencies,
) (*managedRecoveryGraph, error) {
	if dependencies.DB == nil || dependencies.Now == nil ||
		nilManagedRecoveryDependency(dependencies.NodeRevisions) ||
		nilManagedRecoveryDependency(dependencies.NodeAdmission) || dependencies.WorkspaceKeys == nil ||
		nilManagedRecoveryDependency(dependencies.ContentLifecycle) ||
		dependencies.Dialer == nil || strings.TrimSpace(dependencies.CleanupWorkerID) == "" {
		return nil, fmt.Errorf("%w: Recovery production authorities unavailable", backupasset.ErrInvalidState)
	}
	ctx = nonNilRecoveryRuntimeContext(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	planPolicy := recovery.PlanPolicy{
		MaxSelectionItems: int64(config.MaxSelectionItems), MaxLogicalBytes: config.MaxLogicalBytes,
	}
	preflightPolicy := recovery.PreflightPolicy{TTL: config.PreflightTTL}
	workerPolicy := recovery.WorkerPolicy{
		LeaseRenewMargin: config.LeaseRenewMargin, ExecutionTimeout: config.ExecutionTimeout,
	}
	reconciliationPolicy := recovery.ReconciliationPolicy{FindingLimit: config.ReconciliationFindingLimit}

	target, err := recovery.NewProductionTarget(recovery.ProductionTargetDependencies{
		DB: dependencies.DB, Revisions: dependencies.NodeRevisions,
		Dialer: dependencies.Dialer, WorkspaceKeys: dependencies.WorkspaceKeys,
	})
	if err != nil {
		return nil, fmt.Errorf("build Recovery target: %w", err)
	}
	resultLifecycle, err := recovery.NewResultLifecycleService(recovery.ResultLifecycleDependencies{
		DB: dependencies.DB, Now: dependencies.Now, Audit: dependencies.Audit,
		WorkspaceKeys:       dependencies.WorkspaceKeys,
		DefaultPlaintextTTL: config.ResultDefaultTTL, RetainHardCap: config.ResultRetainHardCap,
		NodeAdmission: dependencies.NodeAdmission, ContentLifecycle: dependencies.ContentLifecycle,
		Target: target, CleanupLeaseTTL: config.CleanupLeaseTTL,
	})
	if err != nil {
		return nil, fmt.Errorf("build Recovery result lifecycle: %w", err)
	}
	cleanup, err := newManagedRecoveryCleanupOwner(managedRecoveryCleanupOwnerDependencies{
		Lifecycle: resultLifecycle,
		WorkerID:  dependencies.CleanupWorkerID, Cadence: config.CleanupCadence,
		BatchSize: config.CleanupBatchSize, RetryBase: config.CleanupRetryBase,
		RetryMaxDelay: config.CleanupRetryMaxDelay, NewTimer: dependencies.CleanupNewTimer,
	})
	if err != nil {
		return nil, fmt.Errorf("build Recovery cleanup owner: %w", err)
	}
	resultResolver, err := recovery.NewRecoveryResultResolver(recovery.RecoveryResultResolverDependencies{
		DB: dependencies.DB, Now: dependencies.Now,
	})
	if err != nil {
		return nil, fmt.Errorf("build Recovery result resolver: %w", err)
	}
	resultDelivery, err := recovery.NewRecoveryResultDeliveryAdapter(recovery.RecoveryResultDeliveryAdapterDependencies{
		Resolver: resultResolver, Target: target, Now: dependencies.Now, ReadPermitTTL: config.ResultReadPermitTTL,
	})
	if err != nil {
		return nil, fmt.Errorf("build Recovery result delivery: %w", err)
	}
	if dependencies.Settings == nil || nilManagedRecoveryDependency(dependencies.ReconciliationRevisions) ||
		nilManagedRecoveryDependency(dependencies.Audit) {
		return nil, fmt.Errorf("%w: Recovery reconciliation authorities unavailable", backupasset.ErrInvalidState)
	}
	if !config.Enabled && nilManagedRecoveryDependency(dependencies.ReconciliationFindings) {
		dependencies.ReconciliationFindings = managedRecoveryUnavailableReconciliationFindingSink{}
	}
	if nilManagedRecoveryDependency(dependencies.ReconciliationFindings) {
		return nil, fmt.Errorf("%w: Recovery reconciliation authorities unavailable", backupasset.ErrInvalidState)
	}
	reconciliationTarget, ok := target.(recovery.TargetReconciliationPort)
	if !ok {
		return nil, fmt.Errorf("%w: Recovery reconciliation target unavailable", backupasset.ErrInvalidState)
	}
	reconciliation, err := recovery.NewRecoveryReconciliationService(recovery.RecoveryReconciliationServiceDependencies{
		DB: dependencies.DB, Now: dependencies.Now, Roots: dependencies.Settings,
		Revisions: dependencies.ReconciliationRevisions, Keys: dependencies.WorkspaceKeys,
		Target: reconciliationTarget, Audit: dependencies.Audit, Findings: dependencies.ReconciliationFindings,
		Policy: reconciliationPolicy,
	})
	if err != nil {
		return nil, fmt.Errorf("build Recovery reconciliation service: %w", err)
	}
	if !config.Enabled {
		generationID, err := backupasset.NewOpaqueID()
		if err != nil {
			return nil, fmt.Errorf("build Recovery disabled reconciliation generation: %w", err)
		}
		graph := &managedRecoveryGraph{
			admissionEnabled: false, target: target, resultLifecycle: resultLifecycle,
			cleanup: cleanup, resultDelivery: resultDelivery, reconciliation: reconciliation,
			downgradeReconciler: reconciliation,
			reconciliationState: recovery.RecoveryReconciliationBlocked,
		}
		graph.revokeDrainDelivery = managedRecoveryDeliveryShutdown(
			graph, dependencies.DB, dependencies.ContentLifecycle, dependencies.Now, config.ResultDrainTimeout,
		)
		graph.shutdownLifecycle = graph.stopRun
		reconciliationOwner, err := newManagedRecoveryReconciliationOwner(
			managedRecoveryReconciliationOwnerDependencies{
				Reconciler: reconciliation, Generation: generationID, Cadence: config.CleanupCadence,
				NewTimer: dependencies.CleanupNewTimer, Record: graph.setReconciliationState,
			},
		)
		if err != nil {
			return nil, fmt.Errorf("build Recovery reconciliation owner: %w", err)
		}
		graph.reconciliationOwner = reconciliationOwner
		graph.reconcileMetadata = func(ctx context.Context) error {
			result, reconcileErr := graph.downgradeReconciler.ReconcileDowngradeReadiness(
				ctx, recovery.RecoveryDowngradeReconciliationRequest{AdmissionGeneration: generationID},
			)
			if errors.Is(reconcileErr, recovery.ErrRecoveryReconciliationUnavailable) {
				// Keep maintenance publication available while making authority
				// absence explicit. Never turn an unavailable scan into clear.
				graph.setReconciliationState(recovery.RecoveryReconciliationBlocked)
				return nil
			}
			if reconcileErr != nil {
				return reconcileErr
			}
			graph.setReconciliationState(managedRecoveryReconciliationResultState(result))
			return nil
		}
		graph.run = func(ctx context.Context) {
			var owners sync.WaitGroup
			owners.Add(2)
			go func() {
				defer owners.Done()
				cleanup.Run(ctx)
			}()
			go func() {
				defer owners.Done()
				reconciliationOwner.Run(ctx)
			}()
			owners.Wait()
		}
		return graph, nil
	}
	if dependencies.Settings == nil || dependencies.SourceLeases == nil ||
		nilManagedRecoveryDependency(dependencies.Metrics) ||
		nilManagedRecoveryDependency(dependencies.PreflightEvidence) ||
		nilManagedRecoveryDependency(dependencies.AuthorityRevalidator) ||
		nilManagedRecoveryDependency(dependencies.PlanSecurity) ||
		nilManagedRecoveryDependency(dependencies.Audit) ||
		nilManagedRecoveryDependency(dependencies.SourceResolver) ||
		nilManagedRecoveryDependency(dependencies.ReconciliationRevisions) ||
		nilManagedRecoveryDependency(dependencies.ReconciliationFindings) {
		return nil, fmt.Errorf("%w: Recovery production authorities unavailable", backupasset.ErrInvalidState)
	}
	if managedRecoveryKnownUnavailableProductionAuthorities(dependencies) {
		return nil, fmt.Errorf("%w: Recovery production authorities unavailable", backupasset.ErrInvalidState)
	}
	plan, err := recovery.NewPlanService(recovery.PlanServiceDependencies{
		DB: dependencies.DB, Now: dependencies.Now, Audit: dependencies.Audit,
		TargetRootResolver: dependencies.Settings,
		Policy:             planPolicy, PreflightPolicy: preflightPolicy,
	})
	if err != nil {
		return nil, fmt.Errorf("build Recovery plan service: %w", err)
	}
	externalEvidence, err := recovery.NewRecoveryPreflightExternalEvidenceAdapter(dependencies.PreflightEvidence)
	if err != nil {
		return nil, fmt.Errorf("build Recovery preflight evidence adapter: %w", err)
	}
	evaluator, err := recovery.NewTargetPreflightEvaluator(target, externalEvidence)
	if err != nil {
		return nil, fmt.Errorf("build Recovery preflight evaluator: %w", err)
	}
	preflight, err := recovery.NewPreflightService(recovery.PreflightServiceDependencies{
		DB: dependencies.DB, Now: dependencies.Now, Audit: dependencies.Audit,
		Evaluator: evaluator, Policy: preflightPolicy,
	})
	if err != nil {
		return nil, fmt.Errorf("build Recovery preflight service: %w", err)
	}
	selections, err := recovery.NewSourceValidator(dependencies.DB)
	if err != nil {
		return nil, fmt.Errorf("build Recovery source selection authority: %w", err)
	}
	applicationPlans, err := recovery.NewRecoveryApplicationPlanAuthority(dependencies.DB)
	if err != nil {
		return nil, fmt.Errorf("build Recovery application plan authority: %w", err)
	}
	targetEnumeration, err := recovery.NewRecoveryPlanTargetEnumerationAuthority(
		dependencies.DB, dependencies.Settings, dependencies.NodeRevisions, dependencies.Now,
	)
	if err != nil {
		return nil, fmt.Errorf("build Recovery target enumeration authority: %w", err)
	}
	materializer, err := recovery.NewProductionApplicationMaterializer(
		recovery.ProductionApplicationMaterializerDependencies{
			Selections: selections, Plans: applicationPlans,
			Security: dependencies.PlanSecurity,
			Targets:  targetEnumeration,
			Policy: recovery.RecoveryApplicationMaterializationPolicy{
				MaxSelectionItems:  config.MaxSelectionItems,
				MaxLogicalBytes:    config.MaxLogicalBytes,
				MaxTargetRows:      config.ScanLimit,
				MaxTargetBytes:     config.MaxLogicalBytes,
				ObservationTimeout: recoveryRuntimeTransitionTimeout,
				PreflightTTL:       config.PreflightTTL,
			},
			Now: dependencies.Now,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("build Recovery application materializer: %w", err)
	}
	application, err := recovery.NewApplicationService(recovery.RecoveryApplicationServiceDependencies{
		Materializer: materializer, Plans: plan, Preflights: preflight,
	})
	if err != nil {
		return nil, fmt.Errorf("build Recovery application service: %w", err)
	}
	authorization, err := recovery.NewAuthorizationService(recovery.AuthorizationServiceDependencies{
		DB: dependencies.DB, Now: dependencies.Now, Metrics: dependencies.Metrics, SourceLeases: dependencies.SourceLeases,
		NodeAdmission: dependencies.NodeAdmission, LiveRevalidator: dependencies.AuthorityRevalidator,
		LocatorKeys: dependencies.WorkspaceKeys, AuditWriter: dependencies.Audit,
		ReceiptReplayTTL: config.ReceiptReplayTTL, WriteGrantTTL: config.WriteGrantTTL,
		DeleteGrantTTL: config.DeleteGrantTTL, NodeLeaseTTL: config.LeaseTTL,
		Policy: workerPolicy,
	})
	if err != nil {
		return nil, fmt.Errorf("build Recovery authorization service: %w", err)
	}
	workerCoordinator, err := recovery.NewWorkerCoordinator(recovery.WorkerCoordinatorDependencies{
		DB: dependencies.DB, Audit: dependencies.Audit, SourceLeases: dependencies.SourceLeases,
		LiveRevalidator: dependencies.AuthorityRevalidator, WorkspaceKeys: dependencies.WorkspaceKeys,
		Target: target, SourceResolver: dependencies.SourceResolver, Now: dependencies.Now,
		LeaseTTL: config.LeaseTTL, ScanLimit: config.ScanLimit,
	})
	if err != nil {
		return nil, fmt.Errorf("build Recovery worker coordinator: %w", err)
	}
	restoreBridge, err := newManagedRecoveryClaimRestoreBridge(
		dependencies.DB, workerCoordinator, dependencies.Now,
	)
	if err != nil {
		return nil, fmt.Errorf("build Recovery restore bridge: %w", err)
	}
	restorePort, err := newManagedRecoveryRsyncRestorePort(managedRecoveryRsyncRestoreDependencies{
		Resolver: dependencies.SourceResolver, Writer: workerCoordinator.RsyncTargetWriter(), Runner: restoreBridge,
	})
	if err != nil {
		return nil, fmt.Errorf("build Recovery Repository restore port: %w", err)
	}
	workerID, err := backupasset.NewOpaqueID()
	if err != nil {
		return nil, fmt.Errorf("build Recovery worker identity: %w", err)
	}
	claimExecutor := &managedRecoveryResolvedClaimExecutor{builder: restoreBridge, restorePort: restorePort}
	worker, err := newManagedRecoveryWorker(managedRecoveryWorkerDependencies{
		Coordinator: workerCoordinator, Executor: claimExecutor, WorkerID: "recovery-worker-" + workerID,
		WorkerConcurrency: config.WorkerConcurrency, TakeoverCadence: config.TakeoverCadence,
		RetryBase: config.RetryBase, RetryMaxDelay: config.RetryMaxDelay,
		Policy: workerPolicy, Now: dependencies.Now,
	})
	if err != nil {
		return nil, fmt.Errorf("build Recovery worker: %w", err)
	}
	generationID, err := backupasset.NewOpaqueID()
	if err != nil {
		return nil, fmt.Errorf("build Recovery reconciliation generation: %w", err)
	}
	graph := &managedRecoveryGraph{
		admissionEnabled: true,
		application:      application, plan: plan, preflight: preflight, authorization: authorization, target: target,
		workerCoordinator: workerCoordinator, deleteHandoffValidator: workerCoordinator, worker: worker,
		resultLifecycle: resultLifecycle, cleanup: cleanup, resultDelivery: resultDelivery, reconciliation: reconciliation,
		downgradeReconciler: reconciliation,
		rsyncRestorePort:    restorePort, workerCoordinatorSourceResolver: dependencies.SourceResolver,
		reconciliationState: recovery.RecoveryReconciliationBlocked,
		stopClaims:          worker.StopAccepting,
		cancelJoinAttempts:  worker.CancelAndJoin,
		fenceOwnership:      worker.FenceActiveClaims,
	}
	graph.revokeDrainDelivery = managedRecoveryDeliveryShutdown(
		graph, dependencies.DB, dependencies.ContentLifecycle, dependencies.Now, config.ResultDrainTimeout,
	)
	graph.shutdownLifecycle = graph.stopRun
	reconciliationOwner, err := newManagedRecoveryReconciliationOwner(
		managedRecoveryReconciliationOwnerDependencies{
			Reconciler: reconciliation, Generation: generationID, Cadence: config.CleanupCadence,
			NewTimer: dependencies.CleanupNewTimer, Record: graph.setReconciliationState,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("build Recovery reconciliation owner: %w", err)
	}
	graph.reconciliationOwner = reconciliationOwner
	graph.reconcileMetadata = func(ctx context.Context) error {
		if _, reconcileErr := workerCoordinator.ReconcilePermanentCleanupKeyLoss(ctx); reconcileErr != nil {
			return reconcileErr
		}
		result, reconcileErr := graph.downgradeReconciler.ReconcileDowngradeReadiness(
			ctx, recovery.RecoveryDowngradeReconciliationRequest{AdmissionGeneration: generationID},
		)
		if reconcileErr != nil {
			return reconcileErr
		}
		graph.setReconciliationState(managedRecoveryReconciliationResultState(result))
		return nil
	}
	graph.run = func(ctx context.Context) {
		ctx = nonNilRecoveryRuntimeContext(ctx)
		var owners sync.WaitGroup
		owners.Add(3)
		go func() {
			defer owners.Done()
			worker.Run(ctx)
		}()
		go func() {
			defer owners.Done()
			cleanup.Run(ctx)
		}()
		go func() {
			defer owners.Done()
			reconciliationOwner.Run(ctx)
		}()
		owners.Wait()
	}
	return graph, nil
}

type managedRecoveryPublication struct {
	mu     sync.Mutex
	graph  *managedRecoveryGraph
	active int
	change chan struct{}
}

func newManagedRecoveryPublication() *managedRecoveryPublication {
	return &managedRecoveryPublication{change: make(chan struct{})}
}

func (publication *managedRecoveryPublication) publish(graph *managedRecoveryGraph) error {
	if publication == nil || graph == nil {
		return fmt.Errorf("%w: Recovery graph publication unavailable", backupasset.ErrInvalidState)
	}
	publication.mu.Lock()
	defer publication.mu.Unlock()
	if publication.graph != nil {
		return fmt.Errorf("%w: Recovery graph already published", backupasset.ErrConflict)
	}
	publication.graph = graph
	publication.signalLocked()
	return nil
}

func (publication *managedRecoveryPublication) unpublish() *managedRecoveryGraph {
	if publication == nil {
		return nil
	}
	publication.mu.Lock()
	defer publication.mu.Unlock()
	graph := publication.graph
	publication.graph = nil
	publication.signalLocked()
	return graph
}

func (publication *managedRecoveryPublication) acquire() (*managedRecoveryGraph, func(), bool) {
	if publication == nil {
		return nil, func() {}, false
	}
	publication.mu.Lock()
	graph := publication.graph
	if graph == nil {
		publication.mu.Unlock()
		return nil, func() {}, false
	}
	publication.active++
	publication.mu.Unlock()
	var once sync.Once
	return graph, func() {
		once.Do(func() {
			publication.mu.Lock()
			publication.active--
			if publication.active == 0 {
				publication.signalLocked()
			}
			publication.mu.Unlock()
		})
	}, true
}

func (publication *managedRecoveryPublication) acquireAdmission() (*managedRecoveryGraph, func(), bool) {
	graph, release, ok := publication.acquire()
	if !ok {
		return nil, release, false
	}
	if !graph.admissionEnabled {
		release()
		return nil, func() {}, false
	}
	return graph, release, true
}

func (publication *managedRecoveryPublication) waitIdle(ctx context.Context) error {
	if publication == nil {
		return nil
	}
	ctx = nonNilRecoveryRuntimeContext(ctx)
	for {
		publication.mu.Lock()
		if publication.active == 0 {
			publication.mu.Unlock()
			return nil
		}
		change := publication.change
		publication.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-change:
		}
	}
}

func (publication *managedRecoveryPublication) current() *managedRecoveryGraph {
	if publication == nil {
		return nil
	}
	publication.mu.Lock()
	defer publication.mu.Unlock()
	return publication.graph
}

func (publication *managedRecoveryPublication) signalLocked() {
	close(publication.change)
	publication.change = make(chan struct{})
}

type managedRecoveryAuthorizationFacade struct {
	publication *managedRecoveryPublication
}

func (facade *managedRecoveryAuthorizationFacade) ReplayAuthorization(
	ctx context.Context,
	request recovery.RecoveryAuthorizationRequest,
) (recovery.RecoveryAuthorizationResult, bool, error) {
	if facade == nil || facade.publication == nil {
		return recovery.RecoveryAuthorizationResult{}, false, recovery.ErrRecoveryPlanUnavailable
	}
	graph, release, ok := facade.publication.acquireAdmission()
	if !ok || graph.authorization == nil {
		return recovery.RecoveryAuthorizationResult{}, false, recovery.ErrRecoveryPlanUnavailable
	}
	defer release()
	result, found, err := graph.authorization.ReplayAuthorization(ctx, request)
	if err != nil || !found || request.Operation != recovery.AuthorizationReceiptDeleteAuthorize {
		return result, found, err
	}
	if err := offerManagedRecoveryDeleteAuthorization(ctx, graph, request, result); err != nil {
		return recovery.RecoveryAuthorizationResult{}, false, err
	}
	return result, true, nil
}

func (facade *managedRecoveryAuthorizationFacade) Authorize(
	ctx context.Context,
	request recovery.RecoveryAuthorizationRequest,
) (recovery.RecoveryAuthorizationResult, error) {
	if facade == nil || facade.publication == nil {
		return recovery.RecoveryAuthorizationResult{}, recovery.ErrRecoveryPlanUnavailable
	}
	graph, release, ok := facade.publication.acquireAdmission()
	if !ok || graph.authorization == nil {
		return recovery.RecoveryAuthorizationResult{}, recovery.ErrRecoveryPlanUnavailable
	}
	defer release()
	result, err := graph.authorization.Authorize(ctx, request)
	if err == nil && request.Operation == recovery.AuthorizationReceiptDeleteAuthorize {
		err = offerManagedRecoveryDeleteAuthorization(ctx, graph, request, result)
	} else if err == nil && result.JobID != "" && graph.worker != nil {
		graph.worker.TryWake(result.JobID)
	}
	return result, err
}

func offerManagedRecoveryDeleteAuthorization(
	ctx context.Context,
	graph *managedRecoveryGraph,
	request recovery.RecoveryAuthorizationRequest,
	result recovery.RecoveryAuthorizationResult,
) error {
	if graph == nil || graph.deleteHandoffValidator == nil || graph.worker == nil {
		return recovery.ErrAuthorizationUnavailable
	}
	consumed, err := graph.deleteHandoffValidator.ValidateDeleteAuthorizationHandoff(ctx, request, result)
	if err != nil {
		return recovery.ErrAuthorizationUnavailable
	}
	if consumed {
		return nil
	}
	if !graph.worker.offerDeleteAuthorization(managedRecoveryDeleteAuthorizationHandoff{
		JobID: request.JobID, PlanID: request.PlanID, CheckpointID: request.CheckpointID,
		AttemptID: request.AttemptID, ExpectedPlanRevision: request.ExpectedPlanRevision,
		GrantID: result.GrantID, GrantSecret: request.GrantSecret,
	}) {
		return recovery.ErrAuthorizationUnavailable
	}
	return nil
}

type managedRecoveryResultFacade struct {
	publication *managedRecoveryPublication
}

func (facade *managedRecoveryResultFacade) AuthorizeRecoveryResult(
	ctx context.Context,
	actor content.DeliveryActor,
	ref content.RecoveryResultRef,
	action content.DeliveryAction,
) (content.AuthorizedRecoveryResult, error) {
	graph, release, ok := facade.acquire()
	if !ok {
		return content.AuthorizedRecoveryResult{}, recovery.ErrRecoveryResultUnavailable
	}
	defer release()
	return graph.resultDelivery.AuthorizeRecoveryResult(ctx, actor, ref, action)
}

func (facade *managedRecoveryResultFacade) AuthorizeRecoveryResultIssue(
	ctx context.Context,
	actor content.DeliveryActor,
	ref content.RecoveryResultRef,
	action content.DeliveryAction,
) (content.AuthorizedRecoveryResult, func(), error) {
	graph, release, ok := facade.acquire()
	if !ok {
		return content.AuthorizedRecoveryResult{}, func() {}, recovery.ErrRecoveryResultUnavailable
	}
	result, err := graph.resultDelivery.AuthorizeRecoveryResult(ctx, actor, ref, action)
	if err != nil {
		release()
		return content.AuthorizedRecoveryResult{}, func() {}, err
	}
	return result, release, nil
}

func (facade *managedRecoveryResultFacade) ReauthorizeRecoveryResult(
	ctx context.Context,
	actor content.DeliveryActor,
	expected content.AuthorizedRecoveryResult,
	action content.DeliveryAction,
) error {
	graph, release, ok := facade.acquire()
	if !ok {
		return recovery.ErrRecoveryResultUnavailable
	}
	defer release()
	return graph.resultDelivery.ReauthorizeRecoveryResult(ctx, actor, expected, action)
}

func (facade *managedRecoveryResultFacade) OpenRecoveryResultSource(
	ctx context.Context,
	request content.RecoveryResultSourceRequest,
) (content.SourceSession, error) {
	graph, release, ok := facade.acquire()
	if !ok {
		return nil, recovery.ErrRecoveryResultUnavailable
	}
	defer release()
	return graph.resultDelivery.OpenRecoveryResultSource(ctx, request)
}

func (facade *managedRecoveryResultFacade) acquire() (*managedRecoveryGraph, func(), bool) {
	if facade == nil || facade.publication == nil {
		return nil, func() {}, false
	}
	graph, release, ok := facade.publication.acquire()
	if !ok || graph.resultDelivery == nil {
		if ok {
			release()
		}
		return nil, func() {}, false
	}
	return graph, release, true
}

type managedRecoveryRuntime struct {
	build              func(context.Context, backupasset.RecoveryConfig) (*managedRecoveryGraph, error)
	validate           func(context.Context, backupasset.RecoveryConfig) error
	install            func(*managedRecoveryPublication, *managedRecoveryGraph) error
	publication        *managedRecoveryPublication
	receiptOwner       managedRecoveryReceiptOwner
	downgradeInspector managedRecoveryDowngradeInspector

	transitionMu        sync.Mutex
	mu                  sync.Mutex
	graph               *managedRecoveryGraph
	config              backupasset.RecoveryConfig
	initialized         bool
	change              chan struct{}
	runMu               sync.Mutex
	runCancel           context.CancelFunc
	runDone             chan struct{}
	accepting           atomic.Bool
	stopped             atomic.Bool
	shutdownOnce        sync.Once
	shutdownErr         error
	downgradeFenced     bool
	downgradeGeneration string
	downgradeReadiness  *RecoveryDowngradeReadiness
}

func newManagedRecoveryRuntime(dependencies managedRecoveryRuntimeDependencies) (*managedRecoveryRuntime, error) {
	if dependencies.Build == nil {
		return nil, fmt.Errorf("%w: Recovery runtime dependencies unavailable", backupasset.ErrInvalidState)
	}
	publication := dependencies.Publication
	if publication == nil {
		publication = newManagedRecoveryPublication()
	}
	managed := &managedRecoveryRuntime{
		build: dependencies.Build, validate: dependencies.Validate, install: dependencies.Install,
		publication: publication, receiptOwner: dependencies.ReceiptOwner,
		downgradeInspector: dependencies.DowngradeInspector, change: make(chan struct{}),
	}
	if managed.validate == nil {
		managed.validate = func(context.Context, backupasset.RecoveryConfig) error { return nil }
	}
	if managed.install == nil {
		managed.install = func(publication *managedRecoveryPublication, graph *managedRecoveryGraph) error {
			return publication.publish(graph)
		}
	}
	managed.accepting.Store(true)
	return managed, nil
}

func (runtime *managedRecoveryRuntime) Startup(ctx context.Context) error {
	return runtime.StartupWithConfig(ctx, backupasset.RecoveryConfig{Enabled: true})
}

func (runtime *managedRecoveryRuntime) StartupWithConfig(ctx context.Context, config backupasset.RecoveryConfig) error {
	if runtime == nil || runtime.stopped.Load() || !runtime.accepting.Load() {
		return fmt.Errorf("%w: Recovery runtime unavailable", backupasset.ErrInvalidState)
	}
	runtime.transitionMu.Lock()
	defer runtime.transitionMu.Unlock()
	if runtime.stopped.Load() || !runtime.accepting.Load() {
		return fmt.Errorf("%w: Recovery runtime unavailable", backupasset.ErrInvalidState)
	}
	runtime.mu.Lock()
	started := runtime.graph != nil
	runtime.mu.Unlock()
	if started {
		return nil
	}
	ctx = nonNilRecoveryRuntimeContext(ctx)
	if err := runtime.validate(ctx, config); err != nil {
		return fmt.Errorf("validate Recovery runtime: %w", err)
	}
	graph, err := runtime.build(ctx, config)
	if err != nil {
		return fmt.Errorf("build Recovery runtime: %w", err)
	}
	if graph == nil || graph.reconcileMetadata == nil {
		return fmt.Errorf("%w: Recovery runtime graph unavailable", backupasset.ErrInvalidState)
	}
	if err := graph.reconcileMetadata(ctx); err != nil {
		return fmt.Errorf("reconcile Recovery metadata: %w", err)
	}
	if runtime.stopped.Load() || !runtime.accepting.Load() {
		return fmt.Errorf("%w: Recovery runtime stopped during startup", backupasset.ErrInvalidState)
	}
	graph.deliveryShutdownActive.Store(graph.revokeDrainDelivery != nil)
	if err := runtime.install(runtime.publication, graph); err != nil {
		graph.deliveryShutdownActive.Store(false)
		return err
	}
	runtime.mu.Lock()
	runtime.graph = graph
	runtime.config = config
	runtime.initialized = true
	runtime.signalChangedLocked()
	runtime.mu.Unlock()
	return nil
}

func (runtime *managedRecoveryRuntime) StopAccepting() {
	if runtime == nil {
		return
	}
	runtime.accepting.Store(false)
	runtime.transitionMu.Lock()
	graph := runtime.publication.unpublish()
	if graph == nil {
		runtime.mu.Lock()
		graph = runtime.graph
		runtime.mu.Unlock()
	}
	if graph != nil {
		graph.stopClaimsOnce.Do(func() {
			if graph.stopClaims != nil {
				graph.stopClaims()
			}
		})
	}
	runtime.transitionMu.Unlock()
}

func (runtime *managedRecoveryRuntime) Run(ctx context.Context) {
	if runtime == nil {
		return
	}
	ctx = nonNilRecoveryRuntimeContext(ctx)
	runCtx, cancel := context.WithCancel(ctx)
	runtime.runMu.Lock()
	if runtime.stopped.Load() || runtime.runDone != nil {
		runtime.runMu.Unlock()
		cancel()
		return
	}
	runDone := make(chan struct{})
	runtime.runCancel = cancel
	runtime.runDone = runDone
	runtime.runMu.Unlock()
	defer close(runDone)
	defer cancel()
	var owners sync.WaitGroup
	if runtime.receiptOwner != nil {
		owners.Add(1)
		go func() {
			defer owners.Done()
			runtime.receiptOwner.Run(runCtx)
		}()
	}
	for {
		runtime.mu.Lock()
		graph, change := runtime.graph, runtime.change
		runtime.mu.Unlock()
		if runtime.stopped.Load() {
			owners.Wait()
			return
		}
		if graph == nil || graph.run == nil {
			select {
			case <-runCtx.Done():
				owners.Wait()
				return
			case <-change:
				continue
			}
		}
		graphDone := graph.startRun(runCtx)
		select {
		case <-runCtx.Done():
			_ = graph.stopRun(context.Background())
			owners.Wait()
			return
		case <-change:
			_ = graph.stopRun(context.Background())
		case <-graphDone:
			select {
			case <-runCtx.Done():
				owners.Wait()
				return
			case <-change:
			}
		}
	}
}

func (runtime *managedRecoveryRuntime) TransitionSettings(
	ctx context.Context,
	config backupasset.RecoveryConfig,
	persist func() error,
) error {
	return runtime.TransitionSettingsWithRestore(ctx, config, persist, func() error { return nil })
}

func (runtime *managedRecoveryRuntime) TransitionSettingsWithRestore(
	ctx context.Context,
	config backupasset.RecoveryConfig,
	persist func() error,
	restorePersisted func() error,
) error {
	return runtime.transitionWithRestore(ctx, config, false, persist, restorePersisted)
}

func (runtime *managedRecoveryRuntime) TransitionCurrentWithRestore(
	ctx context.Context,
	persist func() error,
	restorePersisted func() error,
) error {
	return runtime.transitionWithRestore(
		ctx, backupasset.RecoveryConfig{}, true, persist, restorePersisted,
	)
}

func (runtime *managedRecoveryRuntime) transitionWithRestore(
	ctx context.Context,
	config backupasset.RecoveryConfig,
	useCurrentConfig bool,
	persist func() error,
	restorePersisted func() error,
) error {
	if runtime == nil || persist == nil || restorePersisted == nil ||
		runtime.stopped.Load() || !runtime.accepting.Load() {
		return fmt.Errorf("%w: Recovery settings transition unavailable", backupasset.ErrInvalidState)
	}
	ctx = nonNilRecoveryRuntimeContext(ctx)
	runtime.transitionMu.Lock()
	defer runtime.transitionMu.Unlock()
	if runtime.stopped.Load() || !runtime.accepting.Load() {
		return fmt.Errorf("%w: Recovery runtime unavailable", backupasset.ErrInvalidState)
	}
	runtime.mu.Lock()
	previousGraph, previousConfig := runtime.graph, runtime.config
	initialized := runtime.initialized
	downgradeFenced := runtime.downgradeFenced
	runtime.mu.Unlock()
	if useCurrentConfig {
		if !initialized {
			return fmt.Errorf("%w: Recovery runtime has no installed current configuration", backupasset.ErrInvalidState)
		}
		config = previousConfig
	}
	if downgradeFenced {
		return fmt.Errorf("%w: Recovery downgrade transition is fenced", backupasset.ErrInvalidState)
	}
	if err := runtime.validate(ctx, config); err != nil {
		return fmt.Errorf("validate Recovery settings transition: %w", err)
	}
	discardCandidate := func(candidate *managedRecoveryGraph) error {
		discardCtx, cancel := context.WithTimeout(context.Background(), recoveryRuntimeTransitionTimeout)
		defer cancel()
		return shutdownManagedRecoveryGraph(discardCtx, candidate)
	}
	if previousGraph == nil {
		if err := persist(); err != nil {
			return runtime.restoreManagedRecoveryTransitionLocked(err, previousConfig, restorePersisted, true)
		}
		candidate, err := runtime.prepareGraph(ctx, config)
		if err != nil {
			return runtime.restoreManagedRecoveryTransitionLocked(err, previousConfig, restorePersisted, true)
		}
		if err := runtime.publishPreparedGraph(candidate, config); err != nil {
			return runtime.restoreManagedRecoveryTransitionLocked(
				errors.Join(err, discardCandidate(candidate)), previousConfig, restorePersisted, true,
			)
		}
		return nil
	}
	runtime.publication.unpublish()
	if err := runtime.publication.waitIdle(ctx); err != nil {
		_ = runtime.publication.publish(previousGraph)
		return err
	}
	if err := shutdownManagedRecoveryGraph(ctx, previousGraph); err != nil {
		runtime.fenceFailedRecoveryRestorationLocked()
		return fmt.Errorf("%w: Recovery runtime owner join could not be proven", backupasset.ErrInvalidState)
	}
	runtime.mu.Lock()
	if runtime.graph == previousGraph {
		runtime.graph = nil
		runtime.signalChangedLocked()
	}
	runtime.mu.Unlock()
	if err := persist(); err != nil {
		return runtime.restoreManagedRecoveryTransitionLocked(err, previousConfig, restorePersisted, true)
	}
	candidate, err := runtime.prepareGraph(ctx, config)
	if err != nil {
		return runtime.restoreManagedRecoveryTransitionLocked(err, previousConfig, restorePersisted, true)
	}
	if err := runtime.publishPreparedGraph(candidate, config); err != nil {
		return runtime.restoreManagedRecoveryTransitionLocked(
			errors.Join(err, discardCandidate(candidate)), previousConfig, restorePersisted, true,
		)
	}
	return nil
}

func (runtime *managedRecoveryRuntime) restoreManagedRecoveryTransitionLocked(
	transitionErr error,
	previousConfig backupasset.RecoveryConfig,
	restorePersisted func() error,
	persistMayHaveChanged bool,
) error {
	var restoreErr error
	if persistMayHaveChanged {
		restoreErr = restorePersisted()
	}
	if restoreErr == nil {
		restoreErr = runtime.restoreManagedRecoveryGraphLocked(previousConfig)
	}
	if restoreErr != nil {
		runtime.fenceFailedRecoveryRestorationLocked()
	}
	return errors.Join(transitionErr, restoreErr)
}

func (runtime *managedRecoveryRuntime) fenceFailedRecoveryRestorationLocked() {
	runtime.publication.unpublish()
	runtime.mu.Lock()
	runtime.graph = nil
	runtime.downgradeFenced = true
	if runtime.downgradeGeneration == "" {
		runtime.downgradeGeneration = "recovery-restoration-failed"
	}
	runtime.signalChangedLocked()
	runtime.mu.Unlock()
}

func (runtime *managedRecoveryRuntime) DowngradeReadiness(
	ctx context.Context,
) (RecoveryDowngradeReadiness, error) {
	if runtime == nil || runtime.downgradeInspector == nil || runtime.stopped.Load() || !runtime.accepting.Load() {
		return RecoveryDowngradeReadiness{}, fmt.Errorf("%w: Recovery downgrade readiness unavailable", backupasset.ErrInvalidState)
	}
	ctx = nonNilRecoveryRuntimeContext(ctx)
	runtime.transitionMu.Lock()
	defer runtime.transitionMu.Unlock()
	runtime.mu.Lock()
	graph, config := runtime.graph, runtime.config
	if graph == nil || config.Enabled || runtime.downgradeFenced {
		runtime.mu.Unlock()
		return RecoveryDowngradeReadiness{}, fmt.Errorf("%w: Recovery downgrade readiness requires disabled admission", backupasset.ErrInvalidState)
	}
	generationID, err := backupasset.NewOpaqueID()
	if err != nil {
		runtime.mu.Unlock()
		return RecoveryDowngradeReadiness{}, err
	}
	generation := "recovery-downgrade-" + generationID
	runtime.downgradeFenced = true
	runtime.downgradeGeneration = generation
	runtime.mu.Unlock()

	graph.stopClaimsOnce.Do(func() {
		if graph.stopClaims != nil {
			graph.stopClaims()
		}
	})
	if err := graph.stopRun(ctx); err != nil {
		return RecoveryDowngradeReadiness{}, err
	}
	snapshot, err := runtime.downgradeInspector.SnapshotRecoveryDowngradeBlockers(ctx)
	if err != nil {
		return RecoveryDowngradeReadiness{}, err
	}
	result := RecoveryDowngradeReadiness{
		State: RecoveryDowngradeBlocked, AdmissionGeneration: generation, Blockers: snapshot.Blockers,
	}
	if snapshot.UseLatch {
		result.State = RecoveryDowngradeForwardFixOnly
	} else {
		if graph.downgradeReconciler == nil {
			return RecoveryDowngradeReadiness{}, fmt.Errorf("%w: Recovery downgrade reconciliation unavailable", backupasset.ErrInvalidState)
		}
		reconciliation, reconcileErr := graph.downgradeReconciler.ReconcileDowngradeReadiness(
			ctx, recovery.RecoveryDowngradeReconciliationRequest{AdmissionGeneration: generation},
		)
		if reconcileErr != nil {
			return RecoveryDowngradeReadiness{}, reconcileErr
		}
		if reconciliation.State != recovery.RecoveryReconciliationClear || !reconciliation.Complete ||
			len(reconciliation.Findings) != 0 || reconciliation.NextCursor != "" {
			result.Blockers.ReconciliationBacklog = 1
		}
		if !result.Blockers.any() {
			result.State = RecoveryDowngradePristineAllowed
		}
	}
	runtime.mu.Lock()
	stored := result
	runtime.downgradeReadiness = &stored
	runtime.mu.Unlock()
	return result, nil
}

func (runtime *managedRecoveryRuntime) InspectDowngradeReadiness(
	ctx context.Context,
) (RecoveryDowngradeReadiness, bool, error) {
	if runtime == nil || ctx == nil {
		return RecoveryDowngradeReadiness{}, false, backupasset.ErrInvalidState
	}
	if err := ctx.Err(); err != nil {
		return RecoveryDowngradeReadiness{}, false, err
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if !runtime.downgradeFenced || runtime.downgradeReadiness == nil {
		return RecoveryDowngradeReadiness{}, false, nil
	}
	return *runtime.downgradeReadiness, true, nil
}

func (runtime *managedRecoveryRuntime) prepareGraph(
	ctx context.Context,
	config backupasset.RecoveryConfig,
) (*managedRecoveryGraph, error) {
	graph, err := runtime.build(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("build Recovery runtime: %w", err)
	}
	if graph == nil || graph.reconcileMetadata == nil {
		return nil, fmt.Errorf("%w: Recovery runtime graph unavailable", backupasset.ErrInvalidState)
	}
	if err := graph.reconcileMetadata(ctx); err != nil {
		_ = shutdownManagedRecoveryGraph(context.Background(), graph)
		return nil, fmt.Errorf("reconcile Recovery metadata: %w", err)
	}
	return graph, nil
}

func (runtime *managedRecoveryRuntime) publishPreparedGraph(
	graph *managedRecoveryGraph,
	config backupasset.RecoveryConfig,
) error {
	if graph == nil {
		return fmt.Errorf("%w: Recovery runtime graph unavailable", backupasset.ErrInvalidState)
	}
	graph.deliveryShutdownActive.Store(graph.revokeDrainDelivery != nil)
	if err := runtime.install(runtime.publication, graph); err != nil {
		graph.deliveryShutdownActive.Store(false)
		return err
	}
	runtime.mu.Lock()
	runtime.graph = graph
	runtime.config = config
	runtime.initialized = true
	runtime.signalChangedLocked()
	runtime.mu.Unlock()
	return nil
}

func (runtime *managedRecoveryRuntime) installWithConfigLocked(
	ctx context.Context,
	config backupasset.RecoveryConfig,
) error {
	graph, err := runtime.prepareGraph(ctx, config)
	if err != nil {
		return err
	}
	return runtime.publishPreparedGraph(graph, config)
}

func (runtime *managedRecoveryRuntime) restoreManagedRecoveryGraphLocked(
	config backupasset.RecoveryConfig,
) error {
	recoveryCtx, cancel := context.WithTimeout(context.Background(), recoveryRuntimeTransitionTimeout)
	defer cancel()
	return runtime.installWithConfigLocked(recoveryCtx, config)
}

func shutdownManagedRecoveryGraph(ctx context.Context, graph *managedRecoveryGraph) error {
	if graph == nil {
		return nil
	}
	graph.stopClaimsOnce.Do(func() {
		if graph.stopClaims != nil {
			graph.stopClaims()
		}
	})
	var errs []error
	stoppedFallbackRun := false
	if graph.cancelJoinAttempts != nil {
		errs = append(errs, graph.cancelJoinAttempts(ctx))
	} else if graph.shutdownLifecycle == nil {
		errs = append(errs, graph.stopRun(ctx))
		stoppedFallbackRun = true
	}
	if graph.fenceOwnership != nil {
		errs = append(errs, graph.fenceOwnership(ctx))
	}
	if graph.revokeDrainDelivery != nil {
		errs = append(errs, graph.revokeDrainDelivery(ctx))
	}
	if graph.shutdownLifecycle != nil {
		errs = append(errs, graph.shutdownLifecycle(ctx))
	} else if !stoppedFallbackRun {
		errs = append(errs, graph.stopRun(ctx))
	}
	return errors.Join(errs...)
}

const managedRecoveryDeliveryShutdownBatchSize = 100

func managedRecoveryDeliveryShutdown(
	graph *managedRecoveryGraph,
	db *gorm.DB,
	lifecycle recovery.RecoveryResultContentLifecycle,
	now func() time.Time,
	drainTimeout time.Duration,
) func(context.Context) error {
	if graph == nil || db == nil || lifecycle == nil || now == nil || drainTimeout <= 0 {
		return nil
	}
	return func(ctx context.Context) error {
		if !graph.deliveryShutdownActive.CompareAndSwap(true, false) {
			return nil
		}
		ctx, cancel := context.WithTimeout(nonNilRecoveryRuntimeContext(ctx), drainTimeout)
		defer cancel()
		cursor := ""
		var shutdownErr error
		for {
			if err := ctx.Err(); err != nil {
				return errors.Join(shutdownErr, err)
			}
			var jobIDs []string
			query := db.WithContext(ctx).
				Table((model.BackupAssetDeliveryGrant{}).TableName()).
				Select("DISTINCT recovery_job_id").
				Where("resource_kind = ? AND recovery_job_id IS NOT NULL AND recovery_job_id <> '' AND state IN ?",
					content.DeliveryResourceRecoveryResult,
					[]string{string(content.DeliveryIssued), string(content.DeliveryActive), string(content.DeliveryDraining), string(content.DeliveryRevoked)}).
				Order("recovery_job_id ASC").Limit(managedRecoveryDeliveryShutdownBatchSize)
			if cursor != "" {
				query = query.Where("recovery_job_id > ?", cursor)
			}
			if err := query.Pluck("recovery_job_id", &jobIDs).Error; err != nil {
				return errors.Join(shutdownErr, err)
			}
			if len(jobIDs) == 0 {
				return shutdownErr
			}
			at := now().UTC()
			if at.IsZero() {
				return errors.Join(shutdownErr, backupasset.ErrInvalidState)
			}
			if err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				for _, jobID := range jobIDs {
					if backupasset.ValidateOpaqueID(jobID) != nil {
						return backupasset.ErrInvalidState
					}
					if err := lifecycle.RevokeRecoveryResultGrantsTx(
						ctx, tx, jobID, content.RecoveryResultCleanupReason, at,
					); err != nil {
						return err
					}
				}
				return nil
			}); err != nil {
				return errors.Join(shutdownErr, err)
			}
			for _, jobID := range jobIDs {
				if err := lifecycle.CancelRecoveryResultReads(jobID); err != nil {
					shutdownErr = errors.Join(shutdownErr, err)
				}
				if err := lifecycle.DrainRecoveryResult(ctx, jobID); err != nil {
					shutdownErr = errors.Join(shutdownErr, err)
				}
			}
			cursor = jobIDs[len(jobIDs)-1]
		}
	}
}

func (graph *managedRecoveryGraph) startRun(ctx context.Context) <-chan struct{} {
	graph.runMu.Lock()
	defer graph.runMu.Unlock()
	if graph.runDone != nil {
		return graph.runDone
	}
	runCtx, cancel := context.WithCancel(nonNilRecoveryRuntimeContext(ctx))
	done := make(chan struct{})
	graph.runCancel = cancel
	graph.runDone = done
	go func() {
		defer close(done)
		graph.run(runCtx)
	}()
	return done
}

func (graph *managedRecoveryGraph) stopRun(ctx context.Context) error {
	if graph == nil {
		return nil
	}
	graph.runMu.Lock()
	cancel, done := graph.runCancel, graph.runDone
	if cancel != nil {
		cancel()
	}
	graph.runMu.Unlock()
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-nonNilRecoveryRuntimeContext(ctx).Done():
		return nonNilRecoveryRuntimeContext(ctx).Err()
	}
}

func (runtime *managedRecoveryRuntime) PrepareSchemaDown(ctx context.Context, callback func() error) error {
	if runtime == nil || callback == nil {
		return fmt.Errorf("%w: Recovery schema drain unavailable", backupasset.ErrInvalidState)
	}
	ctx = nonNilRecoveryRuntimeContext(ctx)
	runtime.StopAccepting()
	if err := runtime.publication.waitIdle(ctx); err != nil {
		return err
	}
	runtime.mu.Lock()
	graph := runtime.graph
	runtime.mu.Unlock()
	if err := shutdownManagedRecoveryGraph(ctx, graph); err != nil {
		return err
	}
	runtime.mu.Lock()
	if runtime.graph == graph {
		runtime.graph = nil
		runtime.signalChangedLocked()
	}
	runtime.mu.Unlock()
	if runtime.receiptOwner != nil {
		return runtime.receiptOwner.PrepareSchemaDown(ctx, callback)
	}
	return callback()
}

func (runtime *managedRecoveryRuntime) signalChangedLocked() {
	close(runtime.change)
	runtime.change = make(chan struct{})
}

func (runtime *managedRecoveryRuntime) Shutdown(ctx context.Context) error {
	if runtime == nil {
		return nil
	}
	ctx = nonNilRecoveryRuntimeContext(ctx)
	runtime.shutdownOnce.Do(func() {
		runtime.stopped.Store(true)
		runtime.StopAccepting()
		var errs []error
		idle := true
		if err := runtime.publication.waitIdle(ctx); err != nil {
			errs = append(errs, err)
			idle = false
		}
		runtime.mu.Lock()
		graph := runtime.graph
		runtime.mu.Unlock()
		if idle && graph != nil {
			errs = append(errs, shutdownManagedRecoveryGraph(ctx, graph))
		}
		if runtime.receiptOwner != nil {
			errs = append(errs, runtime.receiptOwner.Shutdown(ctx))
		}
		runtime.runMu.Lock()
		runCancel, runDone := runtime.runCancel, runtime.runDone
		if runCancel != nil {
			runCancel()
		}
		runtime.runMu.Unlock()
		runtime.mu.Lock()
		runtime.signalChangedLocked()
		runtime.mu.Unlock()
		if runDone != nil {
			select {
			case <-runDone:
			case <-ctx.Done():
				errs = append(errs, ctx.Err())
			}
		}
		runtime.shutdownErr = errors.Join(errs...)
	})
	return runtime.shutdownErr
}

type managedRecoveryWorkerCoordinator interface {
	ClaimNext(context.Context, string) (recovery.RecoveryWorkerClaim, bool, error)
	TakeoverExpired(context.Context, string) (recovery.RecoveryWorkerClaim, bool, error)
}

type managedRecoveryHeartbeatCoordinator interface {
	Heartbeat(context.Context, recovery.RecoveryWorkerClaim) (recovery.RecoveryWorkerClaim, error)
}

type managedRecoveryClaimFencer interface {
	CancelJob(context.Context, string) error
}

type managedRecoveryClaimExecutor interface {
	ExecuteResolvedClaim(context.Context, recovery.RecoveryWorkerClaim) error
}

type managedRecoveryDeleteGrantContextKey struct{}

type managedRecoveryDeleteAuthorizationPause struct {
	JobID                string
	PlanID               string
	CheckpointID         string
	AttemptID            string
	ExpectedPlanRevision uint64
}

func (*managedRecoveryDeleteAuthorizationPause) Error() string {
	return "managed Recovery delete authorization paused"
}

func (pause managedRecoveryDeleteAuthorizationPause) valid() bool {
	return backupasset.ValidateOpaqueID(pause.JobID) == nil && backupasset.ValidateOpaqueID(pause.PlanID) == nil &&
		backupasset.ValidateOpaqueID(pause.CheckpointID) == nil && backupasset.ValidateOpaqueID(pause.AttemptID) == nil &&
		pause.ExpectedPlanRevision > 0
}

type managedRecoveryDeleteAuthorizationHandoff struct {
	JobID                string
	PlanID               string
	CheckpointID         string
	AttemptID            string
	ExpectedPlanRevision uint64
	GrantID              string
	GrantSecret          string
}

type managedRecoveryDeleteAuthorizationFingerprint struct {
	JobID                string
	PlanID               string
	CheckpointID         string
	AttemptID            string
	ExpectedPlanRevision uint64
	GrantID              string
	SecretDigest         [sha256.Size]byte
}

func managedRecoveryDeleteAuthorizationFingerprintFor(
	handoff managedRecoveryDeleteAuthorizationHandoff,
) managedRecoveryDeleteAuthorizationFingerprint {
	return managedRecoveryDeleteAuthorizationFingerprint{
		JobID: handoff.JobID, PlanID: handoff.PlanID, CheckpointID: handoff.CheckpointID,
		AttemptID: handoff.AttemptID, ExpectedPlanRevision: handoff.ExpectedPlanRevision,
		GrantID: handoff.GrantID, SecretDigest: sha256.Sum256([]byte(handoff.GrantSecret)),
	}
}

func (handoff managedRecoveryDeleteAuthorizationHandoff) valid() bool {
	if backupasset.ValidateOpaqueID(handoff.JobID) != nil || backupasset.ValidateOpaqueID(handoff.PlanID) != nil ||
		backupasset.ValidateOpaqueID(handoff.CheckpointID) != nil ||
		backupasset.ValidateOpaqueID(handoff.AttemptID) != nil ||
		backupasset.ValidateOpaqueID(handoff.GrantID) != nil || handoff.ExpectedPlanRevision == 0 ||
		len(handoff.GrantSecret) != 43 || strings.TrimSpace(handoff.GrantSecret) != handoff.GrantSecret {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(handoff.GrantSecret)
	return err == nil && len(decoded) == 32 && base64.RawURLEncoding.EncodeToString(decoded) == handoff.GrantSecret
}

func managedRecoveryDeleteGrantSecret(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	secret, _ := ctx.Value(managedRecoveryDeleteGrantContextKey{}).(string)
	return secret
}

type managedRecoveryRestoreRequestBuilder interface {
	BuildRsyncRestoreRequest(context.Context, recovery.RecoveryWorkerClaim) (provider.RestoreRequest, error)
	ReleaseRsyncRestoreRequest(recovery.RecoveryWorkerClaim)
}

type managedRecoveryRestoreExecutionPort interface {
	Execute(context.Context, provider.RestoreRequest, provider.RestoreProgress) (provider.RestoreResult, error)
}

type managedRecoveryResolvedClaimExecutor struct {
	builder     managedRecoveryRestoreRequestBuilder
	restorePort managedRecoveryRestoreExecutionPort
}

type managedRecoveryDeletePauseTaker interface {
	TakeDeleteAuthorizationPause(recovery.RecoveryWorkerClaim) (managedRecoveryDeleteAuthorizationPause, bool)
}

func (executor *managedRecoveryResolvedClaimExecutor) ExecuteResolvedClaim(
	ctx context.Context,
	claim recovery.RecoveryWorkerClaim,
) error {
	if executor == nil || nilManagedRecoveryDependency(executor.builder) ||
		nilManagedRecoveryDependency(executor.restorePort) || backupasset.ValidateOpaqueID(claim.JobID) != nil {
		return recovery.ErrInvalidRecoveryWorker
	}
	ctx = nonNilRecoveryRuntimeContext(ctx)
	request, err := executor.builder.BuildRsyncRestoreRequest(ctx, claim)
	if err != nil {
		return err
	}
	defer executor.builder.ReleaseRsyncRestoreRequest(claim)
	_, err = executor.restorePort.Execute(ctx, request, provider.RestoreProgress{})
	if err != nil {
		return err
	}
	if taker, ok := executor.builder.(managedRecoveryDeletePauseTaker); ok {
		if pause, found := taker.TakeDeleteAuthorizationPause(claim); found {
			return &pause
		}
	}
	return nil
}

type managedRecoveryClaimRestoreBridge struct {
	db          *gorm.DB
	coordinator *recovery.WorkerCoordinator
	now         func() time.Time

	mu     sync.Mutex
	claims map[string]recovery.RecoveryWorkerClaim
	pauses map[string]managedRecoveryDeleteAuthorizationPause
}

func newManagedRecoveryClaimRestoreBridge(
	db *gorm.DB,
	coordinator *recovery.WorkerCoordinator,
	now func() time.Time,
) (*managedRecoveryClaimRestoreBridge, error) {
	if db == nil || coordinator == nil || now == nil {
		return nil, fmt.Errorf("%w: Recovery restore bridge unavailable", backupasset.ErrInvalidState)
	}
	return &managedRecoveryClaimRestoreBridge{
		db: db, coordinator: coordinator, now: now,
		claims: make(map[string]recovery.RecoveryWorkerClaim),
		pauses: make(map[string]managedRecoveryDeleteAuthorizationPause),
	}, nil
}

func managedRecoveryClaimKey(claim recovery.RecoveryWorkerClaim) string {
	return claim.JobID + "\x00" + claim.AttemptID + "\x00" + claim.NodeLeaseID
}

func managedRecoveryProviderSessionExpiry(claim recovery.RecoveryWorkerClaim) time.Time {
	return claim.AbsoluteDeadline.UTC()
}

func (bridge *managedRecoveryClaimRestoreBridge) BuildRsyncRestoreRequest(
	ctx context.Context,
	claim recovery.RecoveryWorkerClaim,
) (provider.RestoreRequest, error) {
	if bridge == nil || bridge.db == nil || bridge.coordinator == nil || bridge.now == nil ||
		backupasset.ValidateOpaqueID(claim.JobID) != nil || backupasset.ValidateOpaqueID(claim.AttemptID) != nil ||
		backupasset.ValidateOpaqueID(claim.NodeLeaseID) != nil || !claim.LeaseExpiresAt.After(bridge.now().UTC()) {
		return provider.RestoreRequest{}, recovery.ErrInvalidRecoveryWorker
	}
	ctx = nonNilRecoveryRuntimeContext(ctx)
	var job model.BackupAssetRecoveryJob
	var plan model.BackupAssetRecoveryPlan
	var preflight model.BackupAssetRecoveryPreflight
	var planItems []model.BackupAssetRecoveryPlanItem
	var catalogEntries []model.CatalogEntry
	err := bridge.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		loaded := tx.WithContext(ctx).Where("id = ?", claim.JobID).Limit(1).Find(&job)
		if loaded.Error != nil || loaded.RowsAffected != 1 || job.PlanID == "" ||
			job.State != string(recovery.JobStateRunning) || job.TransitionRevision != claim.TransitionRevision {
			return recovery.ErrRecoveryWorkerFenceLost
		}
		loaded = tx.WithContext(ctx).Where("id = ?", job.PlanID).Limit(1).Find(&plan)
		if loaded.Error != nil || loaded.RowsAffected != 1 || plan.State != string(recovery.PlanStateExecuted) {
			return recovery.ErrRecoveryWorkerFenceLost
		}
		loaded = tx.WithContext(ctx).Where("id = ? AND plan_id = ?", job.PreflightID, plan.ID).Limit(1).Find(&preflight)
		if loaded.Error != nil || loaded.RowsAffected != 1 || preflight.Revision != job.PreflightRevision ||
			preflight.TargetRevision != job.TargetChainRevision {
			return recovery.ErrRecoveryWorkerFenceLost
		}
		loaded = tx.WithContext(ctx).Where("plan_id = ?", plan.ID).Order("ordinal ASC, id ASC").Find(&planItems)
		if loaded.Error != nil || len(planItems) == 0 {
			return recovery.ErrRecoveryWorkerFenceLost
		}
		entryIDs := make([]string, len(planItems))
		for index := range planItems {
			entryIDs[index] = planItems[index].EntryID
		}
		loaded = tx.WithContext(ctx).Where(
			"generation_id = ? AND recovery_point_id = ? AND entry_id IN ?",
			plan.CatalogGenerationID, plan.RecoveryPointID, entryIDs,
		).Find(&catalogEntries)
		if loaded.Error != nil || len(catalogEntries) != len(planItems) {
			return recovery.ErrRecoverySourceChanged
		}
		return nil
	})
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return provider.RestoreRequest{}, contextErr
		}
		return provider.RestoreRequest{}, err
	}
	ref, err := recovery.NewRsyncRestoreSourceRef(plan)
	if err != nil {
		return provider.RestoreRequest{}, recovery.ErrRecoverySourceChanged
	}
	target, err := provider.NewRestoreTarget(
		plan.TargetNodeID, plan.TargetRootID, plan.RootLocatorDigest, plan.PathDigest,
		plan.RootRevision, job.TargetChainRevision,
	)
	if err != nil {
		return provider.RestoreRequest{}, recovery.ErrRecoveryWorkerFenceLost
	}
	entryByID := make(map[string]model.CatalogEntry, len(catalogEntries))
	for _, entry := range catalogEntries {
		entryByID[entry.EntryID] = entry
	}
	entries := make([]provider.RestoreEntry, len(planItems))
	var totalBytes int64
	var maxEntryBytes int64
	for index, item := range planItems {
		entry, exists := entryByID[item.EntryID]
		assetRef := backupasset.AssetRef{RecoveryPointID: item.RecoveryPointID, EntryID: item.EntryID}
		if !exists || item.Ordinal != index || item.RecoveryPointID != plan.RecoveryPointID ||
			item.CatalogGenerationID != plan.CatalogGenerationID ||
			entry.EntryType != string(backupasset.CatalogEntryFile) || entry.Size < 0 {
			return provider.RestoreRequest{}, recovery.ErrRecoverySourceChanged
		}
		entries[index] = provider.RestoreEntry{
			AssetRef: assetRef, Type: backupasset.CatalogEntryFile, ExpectedSize: entry.Size,
			TargetObjectDigest: item.RelativePathDigest,
		}
		totalBytes += entry.Size
		maxEntryBytes = max(maxEntryBytes, entry.Size)
	}
	maxBytes := max(totalBytes, int64(1))
	maxEntryBytes = max(maxEntryBytes, int64(1))
	fence := provider.RestoreFence{
		JobID: claim.JobID, AttemptID: claim.AttemptID, NodeLeaseID: claim.NodeLeaseID,
		AttemptFence: claim.AttemptFence, NodeFence: claim.NodeFence,
		ExpectedTargetRevision: job.TargetChainRevision,
	}
	checkpoint := provider.RestoreCheckpoint{
		ID: claim.AttemptID, OperationDigest: plan.OperationSetDigest,
		PriorTargetRevision: job.TargetChainRevision, VerifiedTargetIdentityDigest: plan.PathDigest,
		VerifiedTargetRevision: job.TargetChainRevision, VerifiedBytes: 0,
		AttemptFence: claim.AttemptFence, NodeFence: claim.NodeFence,
	}
	request := provider.RestoreRequest{
		Version: provider.RestoreRequestSchemaV1, Provider: backupasset.ProviderRsync,
		Entries: entries, Target: target,
		Limits:         provider.RestoreLimits{MaxEntries: len(entries), MaxBytes: maxBytes, MaxEntryBytes: maxEntryBytes},
		ConflictPolicy: provider.RestoreConflictPolicy(plan.ConflictPolicy), Fence: fence, Checkpoint: checkpoint,
		MutationPermit: provider.TargetMutationPermit{
			TargetBindingDigest: target.BindingDigest, UseLatchID: provider.RestoreSchemaUseLatchID,
			JobID: claim.JobID, AttemptID: claim.AttemptID, NodeLeaseID: claim.NodeLeaseID,
			AttemptFence: claim.AttemptFence, NodeFence: claim.NodeFence,
			ExpectedTargetRevision: job.TargetChainRevision,
			Session: provider.TargetSession{
				ID: claim.AttemptID, Purpose: provider.TargetPurposeWrite,
				CredentialRevision: plan.CredentialScopeRevision,
				ExpiresAt:          managedRecoveryProviderSessionExpiry(claim),
			},
		},
		Rsync: &provider.RsyncRestoreRequest{ManifestDigest: ref.ManifestDigest, SourceRef: ref},
	}
	if request.ValidateRsyncResolutionIntent() != nil ||
		request.MutationPermit.ValidateAt(bridge.now().UTC(), request.Target, request.Fence) != nil {
		return provider.RestoreRequest{}, recovery.ErrRecoveryWorkerFenceLost
	}
	bridge.mu.Lock()
	bridge.claims[managedRecoveryClaimKey(claim)] = claim
	bridge.mu.Unlock()
	return request, nil
}

func (bridge *managedRecoveryClaimRestoreBridge) ReleaseRsyncRestoreRequest(
	claim recovery.RecoveryWorkerClaim,
) {
	if bridge == nil {
		return
	}
	bridge.mu.Lock()
	delete(bridge.claims, managedRecoveryClaimKey(claim))
	delete(bridge.pauses, managedRecoveryClaimKey(claim))
	bridge.mu.Unlock()
}

func (bridge *managedRecoveryClaimRestoreBridge) TakeDeleteAuthorizationPause(
	claim recovery.RecoveryWorkerClaim,
) (managedRecoveryDeleteAuthorizationPause, bool) {
	if bridge == nil {
		return managedRecoveryDeleteAuthorizationPause{}, false
	}
	key := managedRecoveryClaimKey(claim)
	bridge.mu.Lock()
	pause, found := bridge.pauses[key]
	delete(bridge.pauses, key)
	bridge.mu.Unlock()
	return pause, found
}

func (bridge *managedRecoveryClaimRestoreBridge) claimForExecute(
	call provider.RsyncRestoreExecuteCall,
) (recovery.RecoveryWorkerClaim, bool) {
	if bridge == nil {
		return recovery.RecoveryWorkerClaim{}, false
	}
	key := call.Fence.JobID + "\x00" + call.Fence.AttemptID + "\x00" + call.Fence.NodeLeaseID
	bridge.mu.Lock()
	claim, exists := bridge.claims[key]
	bridge.mu.Unlock()
	return claim, exists && claim.AttemptFence == call.Fence.AttemptFence &&
		claim.NodeFence == call.Fence.NodeFence
}

type managedRecoveryPinnedSource struct {
	provider.RsyncRestoreSource
}

func (managedRecoveryPinnedSource) Close() error { return nil }

func (bridge *managedRecoveryClaimRestoreBridge) Execute(
	ctx context.Context,
	call provider.RsyncRestoreExecuteCall,
) (provider.RsyncRestoreRunnerResult, error) {
	claim, exists := bridge.claimForExecute(call)
	if !exists || call.Source == nil {
		return provider.RsyncRestoreRunnerResult{}, recovery.ErrRecoveryWorkerFenceLost
	}
	if call.TargetWriter == nil {
		return provider.RsyncRestoreRunnerResult{}, recovery.ErrRecoveryWorkerFenceLost
	}
	// The bridge contract is also exercised as a package-boundary smoke test
	// without a database-backed coordinator. That path may only forward the
	// already declared stream; production bridges always have db/coordinator
	// state and use the full fenced Recovery execution below.
	if bridge.db == nil {
		if len(call.Entries) != 1 || call.Entries[0].Type != backupasset.CatalogEntryFile {
			return provider.RsyncRestoreRunnerResult{}, recovery.ErrRecoveryWorkerFenceLost
		}
		stream, err := call.Source.OpenDeclaredRegular(ctx, call.Entries[0])
		if err != nil {
			return provider.RsyncRestoreRunnerResult{}, err
		}
		writeErr := call.TargetWriter.WriteDeclaredRegular(ctx, provider.RsyncTargetWriteCall{
			Target: call.Target, Entry: call.Entries[0], Source: stream,
			Permit: call.Permit,
		})
		if closeErr := stream.Close(); writeErr == nil && closeErr != nil {
			writeErr = closeErr
		}
		if writeErr != nil {
			return provider.RsyncRestoreRunnerResult{}, writeErr
		}
		return provider.RsyncRestoreRunnerResult{Checkpoint: call.Checkpoint}, nil
	}
	deleteGrantSecret := managedRecoveryDeleteGrantSecret(ctx)
	var err error
	if deleteGrantSecret == "" {
		err = bridge.coordinator.ExecuteClaimWithRsyncTargetWriter(
			ctx, claim, managedRecoveryPinnedSource{RsyncRestoreSource: call.Source}, call.TargetWriter, call.Target,
		)
	} else {
		err = bridge.coordinator.ExecuteClaimWithRsyncTargetWriterAndDeleteGrant(
			ctx, claim, managedRecoveryPinnedSource{RsyncRestoreSource: call.Source}, call.TargetWriter, call.Target,
			deleteGrantSecret,
		)
	}
	if err != nil {
		return provider.RsyncRestoreRunnerResult{}, err
	}
	if deleteGrantSecret == "" {
		pause, paused, pauseErr := bridge.currentDeleteAuthorizationPause(ctx, claim)
		if pauseErr != nil {
			return provider.RsyncRestoreRunnerResult{}, pauseErr
		}
		if paused {
			bridge.mu.Lock()
			bridge.pauses[managedRecoveryClaimKey(claim)] = pause
			bridge.mu.Unlock()
			return provider.RsyncRestoreRunnerResult{Checkpoint: call.Checkpoint}, nil
		}
	}
	return provider.RsyncRestoreRunnerResult{Checkpoint: call.Checkpoint}, nil
}

func (bridge *managedRecoveryClaimRestoreBridge) currentDeleteAuthorizationPause(
	ctx context.Context,
	claim recovery.RecoveryWorkerClaim,
) (managedRecoveryDeleteAuthorizationPause, bool, error) {
	if bridge == nil || bridge.db == nil || bridge.now == nil {
		return managedRecoveryDeleteAuthorizationPause{}, false, recovery.ErrRecoveryWorkerFenceLost
	}
	var owner struct {
		RequesterID uint `gorm:"column:requester_id"`
	}
	loaded := bridge.db.WithContext(ctx).Table((model.BackupAssetRecoveryJob{}).TableName()+" AS jobs").
		Select("plans.requester_id").
		Joins("JOIN "+(model.BackupAssetRecoveryPlan{}).TableName()+" AS plans ON plans.id = jobs.plan_id").
		Where("jobs.id = ?", claim.JobID).Limit(1).Find(&owner)
	if loaded.Error != nil || loaded.RowsAffected != 1 || owner.RequesterID == 0 {
		return managedRecoveryDeleteAuthorizationPause{}, false, recovery.ErrRecoveryWorkerFenceLost
	}
	api, err := recovery.NewAPIService(recovery.APIServiceDependencies{DB: bridge.db, Now: bridge.now})
	if err != nil {
		return managedRecoveryDeleteAuthorizationPause{}, false, recovery.ErrRecoveryWorkerFenceLost
	}
	view, err := api.ProjectJob(ctx, owner.RequesterID, claim.JobID)
	if err != nil {
		return managedRecoveryDeleteAuthorizationPause{}, false, recovery.ErrRecoveryWorkerFenceLost
	}
	if view.DeleteCheckpoint == nil {
		return managedRecoveryDeleteAuthorizationPause{}, false, nil
	}
	revision, err := strconv.ParseUint(view.DeleteCheckpoint.ExpectedPlanRevision, 10, 64)
	if err != nil || view.DeleteCheckpoint.AttemptID != claim.AttemptID {
		return managedRecoveryDeleteAuthorizationPause{}, false, recovery.ErrRecoveryWorkerFenceLost
	}
	pause := managedRecoveryDeleteAuthorizationPause{
		JobID: claim.JobID, PlanID: view.PlanID, CheckpointID: view.DeleteCheckpoint.ID,
		AttemptID: claim.AttemptID, ExpectedPlanRevision: revision,
	}
	if !pause.valid() {
		return managedRecoveryDeleteAuthorizationPause{}, false, recovery.ErrRecoveryWorkerFenceLost
	}
	return pause, true, nil
}

func (*managedRecoveryClaimRestoreBridge) Preflight(
	context.Context,
	provider.RsyncRestorePreflightCall,
) (provider.RsyncRestoreRunnerEvidence, error) {
	return provider.RsyncRestoreRunnerEvidence{}, provider.ErrRsyncRestoreUnavailable
}

func (*managedRecoveryClaimRestoreBridge) Verify(
	context.Context,
	provider.RsyncRestoreVerifyCall,
) (provider.RsyncRestoreRunnerEvidence, error) {
	return provider.RsyncRestoreRunnerEvidence{}, provider.ErrRsyncRestoreUnavailable
}

func (*managedRecoveryClaimRestoreBridge) Reconcile(
	context.Context,
	provider.RsyncRestoreReconcileCall,
) (provider.RsyncRestoreRunnerEvidence, error) {
	return provider.RsyncRestoreRunnerEvidence{}, provider.ErrRsyncRestoreUnavailable
}

type managedRecoveryWorkerDependencies struct {
	Coordinator       managedRecoveryWorkerCoordinator
	Executor          managedRecoveryClaimExecutor
	WorkerID          string
	WorkerConcurrency int
	TakeoverCadence   time.Duration
	RetryBase         time.Duration
	RetryMaxDelay     time.Duration
	NewTimer          func(time.Duration) managedRecoveryTimer
	Policy            recovery.WorkerPolicy
	Now               func() time.Time
	NewHeartbeatTimer func(time.Duration) managedRecoveryTimer
}

type managedRecoveryTimer interface {
	Chan() <-chan time.Time
	Reset(time.Duration) bool
	Stop() bool
}

type managedRecoveryStdTimer struct {
	timer *time.Timer
}

func (timer managedRecoveryStdTimer) Chan() <-chan time.Time {
	return timer.timer.C
}

func (timer managedRecoveryStdTimer) Reset(duration time.Duration) bool {
	return timer.timer.Reset(duration)
}

func (timer managedRecoveryStdTimer) Stop() bool {
	return timer.timer.Stop()
}

func stopAndDrainManagedRecoveryTimer(timer managedRecoveryTimer) {
	if timer == nil || timer.Stop() {
		return
	}
	select {
	case <-timer.Chan():
	default:
	}
}

func nextManagedRecoveryRetryDelay(current, maximum time.Duration) time.Duration {
	if current < maximum {
		return min(current*2, maximum)
	}
	return current
}

type managedRecoveryWorker struct {
	coordinator       managedRecoveryWorkerCoordinator
	executor          managedRecoveryClaimExecutor
	workerID          string
	concurrency       int
	takeoverCadence   time.Duration
	retryBase         time.Duration
	retryMaxDelay     time.Duration
	newTimer          func(time.Duration) managedRecoveryTimer
	heartbeat         managedRecoveryHeartbeatCoordinator
	fencer            managedRecoveryClaimFencer
	policy            recovery.WorkerPolicy
	now               func() time.Time
	newHeartbeatTimer func(time.Duration) managedRecoveryTimer
	wake              chan string
	stop              chan struct{}
	stopOnce          sync.Once
	stopping          atomic.Bool
	runMu             sync.Mutex
	runCancel         context.CancelFunc
	runDone           chan struct{}
	activeMu          sync.Mutex
	activeClaims      map[string]recovery.RecoveryWorkerClaim
	deleteHandoffs    map[string]chan managedRecoveryDeleteAuthorizationHandoff
	deletePauses      map[string]managedRecoveryDeleteAuthorizationPause
	deleteOffers      map[string]managedRecoveryDeleteAuthorizationFingerprint
}

func newManagedRecoveryWorker(dependencies managedRecoveryWorkerDependencies) (*managedRecoveryWorker, error) {
	heartbeat, heartbeatOK := dependencies.Coordinator.(managedRecoveryHeartbeatCoordinator)
	fencer, fencerOK := dependencies.Coordinator.(managedRecoveryClaimFencer)
	if dependencies.Coordinator == nil || dependencies.WorkerID == "" ||
		dependencies.WorkerConcurrency <= 0 || dependencies.WorkerConcurrency > 256 ||
		dependencies.TakeoverCadence <= 0 || dependencies.RetryBase <= 0 ||
		dependencies.RetryMaxDelay < dependencies.RetryBase || dependencies.Policy.Validate() != nil ||
		!heartbeatOK || heartbeat == nil || !fencerOK || fencer == nil {
		return nil, fmt.Errorf("%w: Recovery worker dependencies unavailable", backupasset.ErrInvalidState)
	}
	if dependencies.NewTimer == nil {
		dependencies.NewTimer = func(duration time.Duration) managedRecoveryTimer {
			return managedRecoveryStdTimer{timer: time.NewTimer(duration)}
		}
	}
	if dependencies.Now == nil {
		dependencies.Now = func() time.Time { return time.Now().UTC() }
	}
	if dependencies.NewHeartbeatTimer == nil {
		dependencies.NewHeartbeatTimer = func(duration time.Duration) managedRecoveryTimer {
			return managedRecoveryStdTimer{timer: time.NewTimer(duration)}
		}
	}
	return &managedRecoveryWorker{
		coordinator: dependencies.Coordinator, executor: dependencies.Executor, workerID: dependencies.WorkerID,
		concurrency: dependencies.WorkerConcurrency, takeoverCadence: dependencies.TakeoverCadence,
		retryBase: dependencies.RetryBase, retryMaxDelay: dependencies.RetryMaxDelay, newTimer: dependencies.NewTimer,
		heartbeat: heartbeat, fencer: fencer, policy: dependencies.Policy, now: dependencies.Now,
		newHeartbeatTimer: dependencies.NewHeartbeatTimer,
		wake:              make(chan string, 1), stop: make(chan struct{}),
		activeClaims:   make(map[string]recovery.RecoveryWorkerClaim),
		deleteHandoffs: make(map[string]chan managedRecoveryDeleteAuthorizationHandoff),
		deletePauses:   make(map[string]managedRecoveryDeleteAuthorizationPause),
		deleteOffers:   make(map[string]managedRecoveryDeleteAuthorizationFingerprint),
	}, nil
}

func (worker *managedRecoveryWorker) TryWake(jobID string) bool {
	if worker == nil || worker.stopping.Load() || backupasset.ValidateOpaqueID(jobID) != nil {
		return false
	}
	select {
	case worker.wake <- jobID:
		return true
	default:
		return false
	}
}

func (worker *managedRecoveryWorker) Run(ctx context.Context) {
	if worker == nil {
		return
	}
	ctx, cancel := context.WithCancel(nonNilRecoveryRuntimeContext(ctx))
	worker.runMu.Lock()
	if worker.runDone != nil {
		worker.runMu.Unlock()
		cancel()
		return
	}
	runDone := make(chan struct{})
	worker.runCancel = cancel
	worker.runDone = runDone
	worker.runMu.Unlock()
	defer close(runDone)
	takeoverTimer := worker.newTimer(worker.takeoverCadence)
	if takeoverTimer == nil {
		cancel()
		return
	}
	defer takeoverTimer.Stop()
	type retrySchedule struct {
		timer managedRecoveryTimer
		tick  <-chan time.Time
		delay time.Duration
	}
	claimRetry := retrySchedule{delay: worker.retryBase}
	takeoverRetry := retrySchedule{delay: worker.retryBase}
	type executionResult struct{ err error }
	executionResults := make(chan executionResult, worker.concurrency)
	var attempts sync.WaitGroup
	active := 0
	claimPending := false
	takeoverPending := false

	scheduleRetry := func(schedule *retrySchedule) {
		if schedule.timer == nil {
			schedule.timer = worker.newTimer(schedule.delay)
			if schedule.timer == nil {
				return
			}
		} else {
			schedule.timer.Reset(schedule.delay)
		}
		schedule.tick = schedule.timer.Chan()
		schedule.delay = nextManagedRecoveryRetryDelay(schedule.delay, worker.retryMaxDelay)
	}
	resetRetry := func(schedule *retrySchedule) {
		stopAndDrainManagedRecoveryTimer(schedule.timer)
		schedule.delay = worker.retryBase
		schedule.tick = nil
	}
	startExecution := func(claim recovery.RecoveryWorkerClaim) {
		active++
		worker.trackActiveClaim(claim)
		attempts.Add(1)
		go func() {
			defer attempts.Done()
			err := worker.executeClaim(ctx, claim, true, nil)
			if ctx.Err() == nil {
				worker.finishActiveClaim(claim)
			}
			executionResults <- executionResult{err: err}
		}()
	}
	fillClaims := func() {
		for claimPending && active < worker.concurrency {
			claim, found, err := worker.coordinator.ClaimNext(ctx, worker.workerID)
			if err != nil {
				claimPending = false
				scheduleRetry(&claimRetry)
				return
			}
			resetRetry(&claimRetry)
			if !found {
				claimPending = false
				return
			}
			startExecution(claim)
		}
	}
	tryTakeover := func() {
		if !takeoverPending || active >= worker.concurrency {
			return
		}
		takeoverPending = false
		claim, found, err := worker.coordinator.TakeoverExpired(ctx, worker.workerID)
		if err != nil {
			scheduleRetry(&takeoverRetry)
			return
		}
		resetRetry(&takeoverRetry)
		if found {
			startExecution(claim)
		}
		takeoverTimer.Reset(worker.takeoverCadence)
	}
	defer func() {
		cancel()
		attempts.Wait()
		for _, schedule := range []*retrySchedule{&claimRetry, &takeoverRetry} {
			if schedule.timer != nil {
				schedule.timer.Stop()
			}
		}
	}()
	for {
		fillClaims()
		tryTakeover()
		select {
		case <-ctx.Done():
			return
		case <-worker.stop:
			return
		case <-worker.wake:
			claimPending = true
		case <-takeoverTimer.Chan():
			takeoverPending = true
		case <-claimRetry.tick:
			claimRetry.tick = nil
			claimPending = true
		case <-takeoverRetry.tick:
			takeoverRetry.tick = nil
			takeoverPending = true
		case result := <-executionResults:
			active--
			_ = result.err
		}
	}
}

func (worker *managedRecoveryWorker) executeClaim(
	ctx context.Context,
	claim recovery.RecoveryWorkerClaim,
	found bool,
	claimErr error,
) error {
	if worker == nil || worker.executor == nil || claimErr != nil || !found {
		return claimErr
	}
	ctx = nonNilRecoveryRuntimeContext(ctx)
	now := worker.now().UTC()
	if claim.AbsoluteDeadline.IsZero() || claim.LeaseExpiresAt.IsZero() ||
		!now.Before(claim.AbsoluteDeadline.UTC()) ||
		claim.LeaseExpiresAt.UTC().After(claim.AbsoluteDeadline.UTC()) {
		fenceErr := worker.fencer.CancelJob(context.WithoutCancel(ctx), claim.JobID)
		return errors.Join(recovery.ErrRecoveryWorkerFenceLost, fenceErr)
	}
	claimCtx, cancel := context.WithDeadline(ctx, claim.AbsoluteDeadline.UTC())
	defer cancel()
	current := claim
	var heartbeatTimer managedRecoveryTimer
	var heartbeatTick <-chan time.Time
	if delay, scheduled := nextManagedRecoveryHeartbeat(current, now, worker.policy.LeaseRenewMargin); scheduled {
		heartbeatTimer = worker.newHeartbeatTimer(delay)
		if heartbeatTimer == nil {
			cancel()
			fenceErr := worker.fencer.CancelJob(context.WithoutCancel(ctx), claim.JobID)
			return errors.Join(fmt.Errorf("%w: Recovery heartbeat timer unavailable", backupasset.ErrInvalidState), fenceErr)
		}
		heartbeatTick = heartbeatTimer.Chan()
	}
	if heartbeatTimer != nil {
		defer stopAndDrainManagedRecoveryTimer(heartbeatTimer)
	}

	executionDone := make(chan error, 1)
	executionActive := false
	startExecution := func(executionClaim recovery.RecoveryWorkerClaim, secret string) {
		executionCtx := claimCtx
		if secret != "" {
			executionCtx = context.WithValue(executionCtx, managedRecoveryDeleteGrantContextKey{}, secret)
		}
		go func() {
			executionDone <- worker.executor.ExecuteResolvedClaim(executionCtx, executionClaim)
		}()
		executionActive = true
	}
	startExecution(claim, "")
	var deleteHandoff <-chan managedRecoveryDeleteAuthorizationHandoff
	for {
		select {
		case err := <-executionDone:
			executionActive = false
			var pause *managedRecoveryDeleteAuthorizationPause
			if errors.As(err, &pause) && pause != nil && pause.valid() &&
				pause.JobID == current.JobID && pause.AttemptID == current.AttemptID {
				worker.recordDeleteAuthorizationPause(*pause)
				deleteHandoff = worker.deleteAuthorizationHandoffChannel(claim)
				if deleteHandoff == nil {
					return recovery.ErrRecoveryWorkerFenceLost
				}
				continue
			}
			return err
		case handoff := <-deleteHandoff:
			deleteHandoff = nil
			if handoff.JobID != current.JobID || handoff.AttemptID != current.AttemptID || !handoff.valid() {
				return recovery.ErrRecoveryWorkerFenceLost
			}
			worker.consumeDeleteAuthorizationHandoff(current.JobID)
			startExecution(current, handoff.GrantSecret)
			handoff.GrantSecret = ""
		case <-claimCtx.Done():
			cancel()
			if executionActive {
				<-executionDone
			}
			return claimCtx.Err()
		case <-heartbeatTick:
			renewed, err := worker.heartbeat.Heartbeat(claimCtx, current)
			if err != nil {
				cancel()
				fenceErr := worker.fencer.CancelJob(context.WithoutCancel(ctx), claim.JobID)
				if executionActive {
					<-executionDone
				}
				return errors.Join(err, fenceErr)
			}
			now := worker.now().UTC()
			if renewed.JobID != current.JobID || renewed.AttemptID != current.AttemptID ||
				renewed.NodeLeaseID != current.NodeLeaseID || renewed.WorkerID != current.WorkerID ||
				renewed.AttemptFence != current.AttemptFence || renewed.NodeFence != current.NodeFence ||
				renewed.TransitionRevision != current.TransitionRevision || renewed.SourceFence != current.SourceFence ||
				!renewed.AbsoluteDeadline.UTC().Equal(current.AbsoluteDeadline.UTC()) ||
				!renewed.LeaseExpiresAt.UTC().After(now) ||
				renewed.LeaseExpiresAt.UTC().After(current.AbsoluteDeadline.UTC()) {
				cancel()
				fenceErr := worker.fencer.CancelJob(context.WithoutCancel(ctx), claim.JobID)
				if executionActive {
					<-executionDone
				}
				return errors.Join(recovery.ErrRecoveryWorkerFenceLost, fenceErr)
			}
			current = renewed
			worker.updateActiveClaim(current)
			nextDelay, scheduled := nextManagedRecoveryHeartbeat(current, now, worker.policy.LeaseRenewMargin)
			if !scheduled {
				heartbeatTick = nil
				continue
			}
			heartbeatTimer.Reset(nextDelay)
		}
	}
}

func nextManagedRecoveryHeartbeat(
	claim recovery.RecoveryWorkerClaim,
	now time.Time,
	margin time.Duration,
) (time.Duration, bool) {
	leaseExpiresAt := claim.LeaseExpiresAt.UTC()
	absoluteDeadline := claim.AbsoluteDeadline.UTC()
	now = now.UTC()
	if margin <= 0 || !now.Before(leaseExpiresAt) || !now.Before(absoluteDeadline) ||
		leaseExpiresAt.After(absoluteDeadline) {
		return 0, false
	}
	dueAt := leaseExpiresAt.Add(-margin)
	if !dueAt.Before(absoluteDeadline) {
		return 0, false
	}
	if dueAt.After(now) {
		return dueAt.Sub(now), true
	}
	if !leaseExpiresAt.Before(absoluteDeadline) {
		return 0, false
	}
	return 0, true
}

func (worker *managedRecoveryWorker) StopAccepting() {
	if worker == nil {
		return
	}
	worker.stopping.Store(true)
	worker.stopOnce.Do(func() { close(worker.stop) })
}

func (worker *managedRecoveryWorker) CancelAndJoin(ctx context.Context) error {
	if worker == nil {
		return nil
	}
	worker.StopAccepting()
	worker.runMu.Lock()
	cancel, done := worker.runCancel, worker.runDone
	worker.runMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done == nil {
		return nil
	}
	ctx = nonNilRecoveryRuntimeContext(ctx)
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (worker *managedRecoveryWorker) FenceActiveClaims(ctx context.Context) error {
	if worker == nil {
		return nil
	}
	fencer, ok := worker.coordinator.(managedRecoveryClaimFencer)
	if !ok {
		return fmt.Errorf("%w: Recovery worker fencing unavailable", backupasset.ErrInvalidState)
	}
	worker.activeMu.Lock()
	jobIDs := make([]string, 0, len(worker.activeClaims))
	for jobID := range worker.activeClaims {
		jobIDs = append(jobIDs, jobID)
	}
	worker.activeMu.Unlock()
	slices.Sort(jobIDs)
	var errs []error
	for _, jobID := range jobIDs {
		err := fencer.CancelJob(nonNilRecoveryRuntimeContext(ctx), jobID)
		if !errors.Is(err, recovery.ErrRecoveryWorkerFenceLost) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (worker *managedRecoveryWorker) trackActiveClaim(claim recovery.RecoveryWorkerClaim) {
	worker.activeMu.Lock()
	worker.activeClaims[claim.JobID] = claim
	worker.deleteHandoffs[claim.JobID] = make(chan managedRecoveryDeleteAuthorizationHandoff, 1)
	delete(worker.deletePauses, claim.JobID)
	delete(worker.deleteOffers, claim.JobID)
	worker.activeMu.Unlock()
}

func (worker *managedRecoveryWorker) updateActiveClaim(claim recovery.RecoveryWorkerClaim) {
	worker.activeMu.Lock()
	if _, active := worker.activeClaims[claim.JobID]; active {
		worker.activeClaims[claim.JobID] = claim
	}
	worker.activeMu.Unlock()
}

func (worker *managedRecoveryWorker) finishActiveClaim(claim recovery.RecoveryWorkerClaim) {
	worker.activeMu.Lock()
	delete(worker.activeClaims, claim.JobID)
	delete(worker.deleteHandoffs, claim.JobID)
	delete(worker.deletePauses, claim.JobID)
	delete(worker.deleteOffers, claim.JobID)
	worker.activeMu.Unlock()
}

func (worker *managedRecoveryWorker) deleteAuthorizationHandoffChannel(
	claim recovery.RecoveryWorkerClaim,
) <-chan managedRecoveryDeleteAuthorizationHandoff {
	worker.activeMu.Lock()
	defer worker.activeMu.Unlock()
	active, ok := worker.activeClaims[claim.JobID]
	if !ok || active.AttemptID != claim.AttemptID || active.NodeLeaseID != claim.NodeLeaseID ||
		active.AttemptFence != claim.AttemptFence || active.NodeFence != claim.NodeFence {
		return nil
	}
	return worker.deleteHandoffs[claim.JobID]
}

func (worker *managedRecoveryWorker) offerDeleteAuthorization(
	handoff managedRecoveryDeleteAuthorizationHandoff,
) bool {
	if worker == nil || worker.stopping.Load() || !handoff.valid() {
		return false
	}
	worker.activeMu.Lock()
	defer worker.activeMu.Unlock()
	claim, active := worker.activeClaims[handoff.JobID]
	slot := worker.deleteHandoffs[handoff.JobID]
	pause, paused := worker.deletePauses[handoff.JobID]
	fingerprint := managedRecoveryDeleteAuthorizationFingerprintFor(handoff)
	if !active || claim.AttemptID != handoff.AttemptID {
		return false
	}
	if offered, exists := worker.deleteOffers[handoff.JobID]; exists {
		return offered == fingerprint
	}
	if !paused ||
		pause.JobID != handoff.JobID || pause.PlanID != handoff.PlanID ||
		pause.CheckpointID != handoff.CheckpointID || pause.AttemptID != handoff.AttemptID ||
		pause.ExpectedPlanRevision != handoff.ExpectedPlanRevision {
		return false
	}
	if slot == nil {
		return false
	}
	select {
	case slot <- handoff:
		worker.deleteOffers[handoff.JobID] = fingerprint
		return true
	default:
		return false
	}
}

func (worker *managedRecoveryWorker) consumeDeleteAuthorizationHandoff(jobID string) {
	worker.activeMu.Lock()
	delete(worker.deleteHandoffs, jobID)
	delete(worker.deletePauses, jobID)
	worker.activeMu.Unlock()
}

func (worker *managedRecoveryWorker) recordDeleteAuthorizationPause(
	pause managedRecoveryDeleteAuthorizationPause,
) {
	worker.activeMu.Lock()
	worker.deletePauses[pause.JobID] = pause
	worker.activeMu.Unlock()
}

func reconcilePermanentCleanupKeyLossBeforeReturn(
	keyErr error,
	dependencies Dependencies,
	foundation *backupasset.FoundationService,
) error {
	if !errors.Is(keyErr, backupasset.ErrKeyLost) && !errors.Is(keyErr, backupasset.ErrKeyUnavailable) {
		return keyErr
	}
	if foundation == nil || dependencies.DB == nil || dependencies.Now == nil {
		logger.Module("backupasset.recovery").Warn().
			Str("stage", "permanent_cleanup_key_reconciliation_dependencies").
			Msg("恢复清理密钥永久不可用，启动对账依赖缺失")
		return keyErr
	}
	config, err := foundation.RecoveryAuthorizationConfig()
	if err != nil {
		logger.Module("backupasset.recovery").Warn().
			Str("stage", "permanent_cleanup_key_reconciliation_config").
			Msg("恢复清理密钥永久不可用，启动对账配置不可用")
		return keyErr
	}
	if _, err := recovery.ReconcilePermanentCleanupKeyLoss(
		context.Background(), dependencies.DB, dependencies.Now().UTC(), config.ReceiptReaperBatchSize,
	); err != nil {
		logger.Module("backupasset.recovery").Warn().
			Str("stage", "permanent_cleanup_key_reconciliation").
			Msg("恢复清理密钥永久不可用，启动对账失败")
	}
	return keyErr
}

// RecoveryAuthorizationReceiptReaper is the narrow maintenance seam exposed
// by the Recovery authorization service. It intentionally carries no feature-
// admission dependency: retention must continue while admission is disabled.
type RecoveryAuthorizationReceiptReaper interface {
	ReapAuthorizationReceipts(context.Context, int) (int, error)
}

type RecoveryAuthorizationReceiptOwnerDependencies struct {
	Foundation *backupasset.FoundationService
	Reaper     RecoveryAuthorizationReceiptReaper
	After      func(time.Duration) <-chan time.Time
}

// RecoveryAuthorizationReceiptOwner owns one process-wide bounded receipt
// maintenance loop. Runtime composition starts it only after metadata
// reconciliation and joins it before schema drain.
type RecoveryAuthorizationReceiptOwner struct {
	foundation *backupasset.FoundationService
	reaper     RecoveryAuthorizationReceiptReaper
	after      func(time.Duration) <-chan time.Time
	config     backupasset.RecoveryAuthorizationConfig

	mu        sync.Mutex
	running   bool
	stopping  bool
	runCancel context.CancelFunc
	runDone   chan struct{}
}

func NewRecoveryAuthorizationReceiptOwner(
	dependencies RecoveryAuthorizationReceiptOwnerDependencies,
) (*RecoveryAuthorizationReceiptOwner, error) {
	if dependencies.Foundation == nil || dependencies.Reaper == nil {
		return nil, fmt.Errorf("%w: Recovery authorization receipt owner dependencies unavailable", backupasset.ErrInvalidState)
	}
	config, err := dependencies.Foundation.RecoveryAuthorizationConfig()
	if err != nil || !validRecoveryAuthorizationReceiptOwnerConfig(config) {
		return nil, fmt.Errorf("%w: Recovery authorization receipt owner config unavailable", backupasset.ErrInvalidState)
	}
	if dependencies.After == nil {
		dependencies.After = time.After
	}
	return &RecoveryAuthorizationReceiptOwner{
		foundation: dependencies.Foundation,
		reaper:     dependencies.Reaper,
		after:      dependencies.After,
		config:     config,
	}, nil
}

// Run performs one immediate bounded pass and then follows the dynamic
// maintenance cadence. A failed pass is non-terminal and is retried on the next
// tick. Concurrent Run calls never create a second owner loop.
func (owner *RecoveryAuthorizationReceiptOwner) Run(ctx context.Context) {
	if owner == nil {
		return
	}
	runCtx, cancel, done, ok := owner.beginRun(ctx)
	if !ok {
		return
	}
	defer owner.finishRun(cancel, done)

	config := owner.config
	owner.runPass(runCtx, config.ReceiptReaperBatchSize)
	for {
		select {
		case <-runCtx.Done():
			return
		case <-owner.after(config.ReceiptReaperCadence):
			next, err := owner.foundation.RecoveryAuthorizationConfig()
			if err == nil && validRecoveryAuthorizationReceiptOwnerConfig(next) {
				config = next
			} else {
				logger.Module("backupasset.recovery").Warn().
					Str("stage", "authorization_receipt_reaper_config").
					Msg("恢复授权回执清理配置不可用，保留上一有效快照")
			}
			owner.runPass(runCtx, config.ReceiptReaperBatchSize)
		}
	}
}

func (owner *RecoveryAuthorizationReceiptOwner) runPass(ctx context.Context, limit int) {
	_, err := owner.reaper.ReapAuthorizationReceipts(ctx, limit)
	if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		logger.Module("backupasset.recovery").Warn().
			Str("stage", "authorization_receipt_reaper").
			Msg("恢复授权回执清理失败")
	}
}

// Shutdown cancels the active bounded pass and joins the owner within the
// caller's deadline. It is idempotent and permanently prevents a later Run.
func (owner *RecoveryAuthorizationReceiptOwner) Shutdown(ctx context.Context) error {
	if owner == nil {
		return nil
	}
	ctx = nonNilRecoveryRuntimeContext(ctx)
	owner.mu.Lock()
	owner.stopping = true
	cancel, done := owner.runCancel, owner.runDone
	owner.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// PrepareSchemaDown enforces the required join-before-drain ordering. A failed
// join never invokes the schema callback.
func (owner *RecoveryAuthorizationReceiptOwner) PrepareSchemaDown(
	ctx context.Context,
	drain func() error,
) error {
	if drain == nil {
		return fmt.Errorf("%w: Recovery schema drain callback unavailable", backupasset.ErrInvalidState)
	}
	if err := owner.Shutdown(ctx); err != nil {
		return err
	}
	return drain()
}

func (owner *RecoveryAuthorizationReceiptOwner) beginRun(
	ctx context.Context,
) (context.Context, context.CancelFunc, chan struct{}, bool) {
	ctx = nonNilRecoveryRuntimeContext(ctx)
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.running || owner.stopping {
		return nil, nil, nil, false
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	owner.running = true
	owner.runCancel = cancel
	owner.runDone = done
	return runCtx, cancel, done, true
}

func (owner *RecoveryAuthorizationReceiptOwner) finishRun(cancel context.CancelFunc, done chan struct{}) {
	cancel()
	owner.mu.Lock()
	if owner.runDone == done {
		owner.running = false
		owner.runCancel = nil
	}
	owner.mu.Unlock()
	close(done)
}

func validRecoveryAuthorizationReceiptOwnerConfig(config backupasset.RecoveryAuthorizationConfig) bool {
	return config.ReceiptReaperCadence > 0 &&
		config.ReceiptReaperBatchSize > 0 && config.ReceiptReaperBatchSize <= 1000
}

func nonNilRecoveryRuntimeContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

var _ interface {
	Run(context.Context)
	Shutdown(context.Context) error
	PrepareSchemaDown(context.Context, func() error) error
} = (*RecoveryAuthorizationReceiptOwner)(nil)
