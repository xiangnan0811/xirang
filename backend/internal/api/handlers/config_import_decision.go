package handlers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"xirang/backend/internal/credentialaudit"
	"xirang/backend/internal/cronutil"
	"xirang/backend/internal/logger"
	"xirang/backend/internal/model"
	nodePkg "xirang/backend/internal/node"
	policyPkg "xirang/backend/internal/policy"
	"xirang/backend/internal/settings"
	taskPkg "xirang/backend/internal/task"
)

func configImportContextErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

// configImportAction is deliberately closed. The preview handler projects this
// exact set of actions and never infers one from a count.
type configImportAction string

const (
	configImportActionCreate  configImportAction = "create"
	configImportActionUpdate  configImportAction = "update"
	configImportActionSkip    configImportAction = "skip"
	configImportActionReject  configImportAction = "reject"
	configImportActionPending configImportAction = "pending_validation"
)

type configImportTargetSource string

const (
	configImportTargetExplicit      configImportTargetSource = "explicit"
	configImportTargetManualDefault configImportTargetSource = "manual_default"
	configImportTargetPolicyDefault configImportTargetSource = "policy_default"
)

const (
	configImportPendingRetainedSecret = "retained_secret"
	configImportPendingAllocatedID    = "allocated_identity"
	configImportPendingReference      = "pending_reference"
)

type configImportPendingCheck struct {
	Entity string `json:"entity"`
	Index  int    `json:"index"`
	Code   string `json:"code"`
}

// configImportItemDecision is the action ledger consumed by preview and
// apply. Every input index has exactly one row, including rejected and
// skipped entries that do not have a typed persistence candidate.
type configImportItemDecision struct {
	Index            int
	Name             string
	Action           configImportAction
	PendingCode      string
	DisabledImported bool
}

// configImportInput is the immutable, validated request projection. It is
// shared by preview and apply; neither path decodes a second representation.
type configImportInput struct {
	envelope           configImportEnvelope
	conflict           string
	settingsPlan       []configImportSetting
	foundationSettings map[string]string
	preRejected        map[string]map[int]bool
	settingsSvc        *settings.Service
	lockedCredentials  *configImportCredentialInventory
}

type configImportPolicyCandidate struct {
	index       int
	name        string
	source      map[string]interface{}
	row         model.Policy
	existing    bool
	action      configImportAction
	provisional uint
	rejected    bool
}

// configImportSnapshot contains only fields needed to make the import
// decision. All queries use SkipHooks and omit persisted secret/configuration
// values. Apply reads any retained secret only after the credential inventory
// lock is held, in persistConfigImportDecision.
type configImportSnapshot struct {
	credentials    *configImportCredentialInventory
	policies       []model.Policy
	policiesByID   map[uint]model.Policy
	policiesByName map[string][]model.Policy
	tasks          []model.Task
	tasksByID      map[uint]model.Task
	tasksByKey     map[importTaskKey][]model.Task
	settings       map[string]bool
	targetClaims   []policyPkg.TargetOwner
}

type configImportDecision struct {
	input             configImportInput
	snapshot          configImportSnapshot
	nodeCandidates    []*configImportNodeCandidate
	keyCandidates     []*configImportSSHKeyCandidate
	policyCandidates  []*configImportPolicyCandidate
	taskCandidates    []*configImportTaskCandidate
	pendingChecks     []configImportPendingCheck
	itemDecisions     map[string][]configImportItemDecision
	warnings          []configImportWarning
	warningsTruncated int
	skipped           int
	rejected          int
	nodeIDMap         map[uint]uint
	keyIDMap          map[uint]uint
	policyIDMap       map[uint]uint
	taskIDMap         map[uint]uint
}

func prepareConfigImportInput(ctx context.Context, h *ConfigHandler, body []byte, conflict string) (configImportInput, error) {
	if err := configImportContextErr(ctx); err != nil {
		return configImportInput{}, err
	}
	if conflict != "skip" && conflict != "overwrite" {
		return configImportInput{}, fmt.Errorf("conflict 参数仅支持 skip 或 overwrite")
	}
	envelope, err := decodeConfigImportEnvelope(body)
	if err != nil {
		return configImportInput{}, err
	}
	if err := configImportContextErr(ctx); err != nil {
		return configImportInput{}, err
	}
	if err := validateConfigImportEnvelope(envelope); err != nil {
		return configImportInput{}, err
	}
	if err := configImportContextErr(ctx); err != nil {
		return configImportInput{}, err
	}
	settingsPlan, foundationSettings, err := h.normalizeImportSettings(envelope.Classic.SystemSettings)
	if err != nil {
		return configImportInput{}, err
	}
	if err := configImportContextErr(ctx); err != nil {
		return configImportInput{}, err
	}
	preRejected := make(map[string]map[int]bool)
	markPreRejected := func(entity string, index int) {
		indexes := preRejected[entity]
		if indexes == nil {
			indexes = make(map[int]bool)
			preRejected[entity] = indexes
		}
		indexes[index] = true
	}
	for index, nodeData := range envelope.Classic.Nodes {
		if err := configImportContextErr(ctx); err != nil {
			return configImportInput{}, err
		}
		if len(validateNodeImportData(nodeData, index)) > 0 {
			markPreRejected(configImportEntityNodes, index)
		}
	}
	for index, policyData := range envelope.Classic.Policies {
		if err := configImportContextErr(ctx); err != nil {
			return configImportInput{}, err
		}
		invalid := false
		if source, ok := policyData["source_path"].(string); ok && source != "" {
			invalid = validateImportPath(source) != nil
		}
		if target, ok := policyData["target_path"].(string); ok && target != "" {
			invalid = invalid || validateImportPath(target) != nil
		}
		if invalid {
			markPreRejected(configImportEntityPolicies, index)
		}
	}
	for index, taskData := range envelope.Classic.Tasks {
		if err := configImportContextErr(ctx); err != nil {
			return configImportInput{}, err
		}
		executorType := readStringField(taskData, "executor_type")
		executorConfig := readStringField(taskData, "executor_config")
		if importedRsyncConfigRequiresDisconnect(executorType, executorConfig) {
			continue
		}
		managedRclone := importedRcloneConfigRequiresDisconnect(executorType, executorConfig)
		invalid := false
		if source, ok := taskData["rsync_source"].(string); ok && source != "" {
			invalid = validateImportPath(source) != nil
		}
		if target, ok := taskData["rsync_target"].(string); ok && target != "" && !managedRclone {
			invalid = invalid || validateImportPath(target) != nil
		}
		if invalid {
			markPreRejected(configImportEntityTasks, index)
		}
	}
	if err := configImportContextErr(ctx); err != nil {
		return configImportInput{}, err
	}
	return configImportInput{
		envelope:           envelope,
		conflict:           conflict,
		settingsPlan:       append([]configImportSetting(nil), settingsPlan...),
		foundationSettings: cloneStringMap(foundationSettings),
		preRejected:        preRejected,
		settingsSvc:        h.settingsSvc,
	}, nil
}

func loadConfigImportSnapshot(ctx context.Context, db *gorm.DB, input configImportInput) (configImportSnapshot, error) {
	var snapshot configImportSnapshot
	if err := configImportContextErr(ctx); err != nil {
		return snapshot, err
	}
	if db == nil {
		return snapshot, fmt.Errorf("config import snapshot database is unavailable")
	}
	var inventory *configImportCredentialInventory
	if input.lockedCredentials != nil {
		var cloneErr error
		inventory, cloneErr = cloneConfigImportCredentialInventoryContext(ctx, input.lockedCredentials)
		if cloneErr != nil {
			return snapshot, cloneErr
		}
	} else {
		var err error
		inventory, err = loadConfigImportCredentialInventory(ctx, db, input.envelope.Classic, false)
		if err != nil {
			return snapshot, err
		}
	}
	if err := configImportContextErr(ctx); err != nil {
		return snapshot, err
	}
	snapshot.credentials = inventory
	snapshot.policiesByID = make(map[uint]model.Policy)
	snapshot.policiesByName = make(map[string][]model.Policy)
	snapshot.tasksByID = make(map[uint]model.Task)
	snapshot.tasksByKey = make(map[importTaskKey][]model.Task)
	snapshot.settings = make(map[string]bool)

	if len(input.envelope.Classic.Policies) > 0 || len(input.envelope.Classic.Tasks) > 0 {
		var policies []model.Policy
		query := db.WithContext(ctx).Session(&gorm.Session{SkipHooks: true}).Model(&model.Policy{})
		if err := query.Select("id", "name", "source_path", "target_path", "cron_spec").Order("id").Find(&policies).Error; err != nil {
			return snapshot, fmt.Errorf("读取导入策略快照失败: %w", err)
		}
		snapshot.policies = policies
		for _, policy := range policies {
			if err := configImportContextErr(ctx); err != nil {
				return snapshot, err
			}
			snapshot.policiesByID[policy.ID] = policy
			name := strings.TrimSpace(policy.Name)
			if name != "" {
				snapshot.policiesByName[name] = append(snapshot.policiesByName[name], policy)
			}
		}
	}
	if err := configImportContextErr(ctx); err != nil {
		return snapshot, err
	}
	if len(input.envelope.Classic.Tasks) > 0 {
		var tasks []model.Task
		query := db.WithContext(ctx).Session(&gorm.Session{SkipHooks: true}).Model(&model.Task{})
		if err := query.Select(
			"id", "name", "node_id", "policy_id", "depends_on_task_id", "rsync_source",
			"rsync_target", "executor_type", "cron_spec", "cron_override", "status",
			"source", "enabled", "archived_at",
		).Order("id").Find(&tasks).Error; err != nil {
			return snapshot, fmt.Errorf("读取导入任务快照失败: %w", err)
		}
		snapshot.tasks = tasks
		for _, task := range tasks {
			if err := configImportContextErr(ctx); err != nil {
				return snapshot, err
			}
			snapshot.tasksByID[task.ID] = task
			key := buildImportTaskKey(task.Name, task.NodeID)
			snapshot.tasksByKey[key] = append(snapshot.tasksByKey[key], task)
			target := strings.TrimSpace(task.RsyncTarget)
			if target != "" && filepath.IsAbs(target) {
				claim := policyPkg.TargetOwner{NodeID: task.NodeID, TaskID: task.ID, Target: target}
				if task.PolicyID != nil {
					claim.PolicyID = *task.PolicyID
				}
				snapshot.targetClaims = append(snapshot.targetClaims, claim)
			}
		}
	}
	if err := configImportContextErr(ctx); err != nil {
		return snapshot, err
	}
	if len(input.settingsPlan) > 0 {
		keys := make([]string, 0, len(input.settingsPlan))
		for _, setting := range input.settingsPlan {
			if err := configImportContextErr(ctx); err != nil {
				return snapshot, err
			}
			keys = append(keys, setting.key)
		}
		var rows []model.SystemSetting
		if err := db.WithContext(ctx).Session(&gorm.Session{SkipHooks: true}).Select("key").Where("key IN ?", keys).Find(&rows).Error; err != nil {
			return snapshot, fmt.Errorf("读取导入系统设置快照失败: %w", err)
		}
		for _, row := range rows {
			if err := configImportContextErr(ctx); err != nil {
				return snapshot, err
			}
			snapshot.settings[row.Key] = true
		}
	}
	if err := configImportContextErr(ctx); err != nil {
		return snapshot, err
	}
	return snapshot, nil
}

func cloneConfigImportCredentialInventoryContext(ctx context.Context, source *configImportCredentialInventory) (*configImportCredentialInventory, error) {
	if source == nil {
		return nil, nil
	}
	if err := configImportContextErr(ctx); err != nil {
		return nil, err
	}
	clone := &configImportCredentialInventory{
		nodes:                 append([]model.Node(nil), source.nodes...),
		keys:                  append([]model.SSHKey(nil), source.keys...),
		nodePasswordPresent:   make(map[uint]bool, len(source.nodePasswordPresent)),
		nodePrivateKeyPresent: make(map[uint]bool, len(source.nodePrivateKeyPresent)),
		keyPrivateKeyPresent:  make(map[uint]bool, len(source.keyPrivateKeyPresent)),
	}
	for id, present := range source.nodePasswordPresent {
		if err := configImportContextErr(ctx); err != nil {
			return nil, err
		}
		clone.nodePasswordPresent[id] = present
	}
	for id, present := range source.nodePrivateKeyPresent {
		if err := configImportContextErr(ctx); err != nil {
			return nil, err
		}
		clone.nodePrivateKeyPresent[id] = present
	}
	for id, present := range source.keyPrivateKeyPresent {
		if err := configImportContextErr(ctx); err != nil {
			return nil, err
		}
		clone.keyPrivateKeyPresent[id] = present
	}
	if err := clone.rebuildIndexesContext(ctx); err != nil {
		return nil, err
	}
	if err := configImportContextErr(ctx); err != nil {
		return nil, err
	}
	return clone, nil
}

func cloneConfigImportSnapshotContext(ctx context.Context, source configImportSnapshot) (configImportSnapshot, error) {
	credentials, err := cloneConfigImportCredentialInventoryContext(ctx, source.credentials)
	if err != nil {
		return configImportSnapshot{}, err
	}
	clone := configImportSnapshot{
		credentials:    credentials,
		policies:       append([]model.Policy(nil), source.policies...),
		tasks:          append([]model.Task(nil), source.tasks...),
		settings:       make(map[string]bool, len(source.settings)),
		targetClaims:   append([]policyPkg.TargetOwner(nil), source.targetClaims...),
		policiesByID:   make(map[uint]model.Policy, len(source.policiesByID)),
		policiesByName: make(map[string][]model.Policy, len(source.policiesByName)),
		tasksByID:      make(map[uint]model.Task, len(source.tasksByID)),
		tasksByKey:     make(map[importTaskKey][]model.Task, len(source.tasksByKey)),
	}
	for key, value := range source.settings {
		if err := configImportContextErr(ctx); err != nil {
			return configImportSnapshot{}, err
		}
		clone.settings[key] = value
	}
	for id, row := range source.policiesByID {
		if err := configImportContextErr(ctx); err != nil {
			return configImportSnapshot{}, err
		}
		clone.policiesByID[id] = row
	}
	for name, rows := range source.policiesByName {
		if err := configImportContextErr(ctx); err != nil {
			return configImportSnapshot{}, err
		}
		clone.policiesByName[name] = append([]model.Policy(nil), rows...)
	}
	for id, row := range source.tasksByID {
		if err := configImportContextErr(ctx); err != nil {
			return configImportSnapshot{}, err
		}
		clone.tasksByID[id] = row
	}
	for key, rows := range source.tasksByKey {
		if err := configImportContextErr(ctx); err != nil {
			return configImportSnapshot{}, err
		}
		clone.tasksByKey[key] = append([]model.Task(nil), rows...)
	}
	if err := configImportContextErr(ctx); err != nil {
		return configImportSnapshot{}, err
	}
	return clone, nil
}

func initializeConfigImportItemDecisionsContext(ctx context.Context, input configImportInput, snapshot configImportSnapshot) (map[string][]configImportItemDecision, error) {
	items := map[string][]configImportItemDecision{
		configImportEntityNodes:          make([]configImportItemDecision, len(input.envelope.Classic.Nodes)),
		configImportEntitySSHKeys:        make([]configImportItemDecision, len(input.envelope.Classic.SSHKeys)),
		configImportEntityPolicies:       make([]configImportItemDecision, len(input.envelope.Classic.Policies)),
		configImportEntityTasks:          make([]configImportItemDecision, len(input.envelope.Classic.Tasks)),
		configImportEntitySystemSettings: make([]configImportItemDecision, len(input.settingsPlan)),
	}
	initialize := func(entity string, records []map[string]interface{}, field string) error {
		for index, record := range records {
			if err := configImportContextErr(ctx); err != nil {
				return err
			}
			items[entity][index] = configImportItemDecision{
				Index: index, Name: readStringField(record, field), Action: configImportActionReject,
			}
		}
		return nil
	}
	for _, item := range []struct {
		entity  string
		records []map[string]interface{}
		field   string
	}{
		{configImportEntityNodes, input.envelope.Classic.Nodes, "name"},
		{configImportEntitySSHKeys, input.envelope.Classic.SSHKeys, "name"},
		{configImportEntityPolicies, input.envelope.Classic.Policies, "name"},
		{configImportEntityTasks, input.envelope.Classic.Tasks, "name"},
	} {
		if err := initialize(item.entity, item.records, item.field); err != nil {
			return nil, err
		}
	}
	for index, setting := range input.settingsPlan {
		if err := configImportContextErr(ctx); err != nil {
			return nil, err
		}
		action := configImportActionCreate
		if snapshot.settings[setting.key] {
			action = configImportActionUpdate
		}
		items[configImportEntitySystemSettings][index] = configImportItemDecision{
			Index: index, Name: setting.key, Action: action,
		}
	}
	return items, nil
}

func setConfigImportItemDecision(
	decision *configImportDecision,
	entity string,
	index int,
	name string,
	action configImportAction,
	pendingCode string,
	disabledImported bool,
) {
	if decision == nil || index < 0 {
		return
	}
	rows := decision.itemDecisions[entity]
	if index >= len(rows) {
		return
	}
	row := &rows[index]
	row.Index = index
	if name != "" {
		row.Name = name
	}
	row.Action = action
	row.PendingCode = pendingCode
	row.DisabledImported = disabledImported
	decision.itemDecisions[entity] = rows
}

func configImportItemDecisionsFor(decision *configImportDecision, entity string) []configImportItemDecision {
	if decision == nil {
		return nil
	}
	return decision.itemDecisions[entity]
}

type configImportDecisionCounts struct {
	Input             int
	Created           int
	Updated           int
	Skipped           int
	Rejected          int
	PendingValidation int
	DisabledImported  int
}

func configImportDecisionCountsFor(decision *configImportDecision, entity string) configImportDecisionCounts {
	var counts configImportDecisionCounts
	for _, item := range configImportItemDecisionsFor(decision, entity) {
		counts.Input++
		switch item.Action {
		case configImportActionCreate:
			counts.Created++
		case configImportActionUpdate:
			counts.Updated++
		case configImportActionSkip:
			counts.Skipped++
		case configImportActionReject:
			counts.Rejected++
		case configImportActionPending:
			counts.PendingValidation++
		}
		if item.DisabledImported {
			counts.DisabledImported++
		}
	}
	return counts
}

func evaluateConfigImport(ctx context.Context, input configImportInput, snapshot configImportSnapshot) (configImportDecision, error) {
	if err := configImportContextErr(ctx); err != nil {
		return configImportDecision{}, err
	}
	if snapshot.credentials == nil {
		return configImportDecision{}, fmt.Errorf("config import credential snapshot is unavailable")
	}
	workingSnapshot, err := cloneConfigImportSnapshotContext(ctx, snapshot)
	if err != nil {
		return configImportDecision{}, err
	}
	itemDecisions, err := initializeConfigImportItemDecisionsContext(ctx, input, workingSnapshot)
	if err != nil {
		return configImportDecision{}, err
	}
	decision := configImportDecision{
		input: input, snapshot: workingSnapshot,
		itemDecisions: itemDecisions,
		nodeIDMap:     make(map[uint]uint), keyIDMap: make(map[uint]uint),
		policyIDMap: make(map[uint]uint), taskIDMap: make(map[uint]uint),
	}
	accumulator := newConfigImportAccumulator()
	nodeIDs, err := workingSnapshot.credentials.nodeIDsContext(ctx)
	if err != nil {
		return configImportDecision{}, err
	}
	keyIDs, err := workingSnapshot.credentials.keyIDsContext(ctx)
	if err != nil {
		return configImportDecision{}, err
	}
	policyIDs, err := configImportPolicyIDsContext(ctx, workingSnapshot)
	if err != nil {
		return configImportDecision{}, err
	}
	taskIDs, err := configImportTaskIDsContext(ctx, workingSnapshot)
	if err != nil {
		return configImportDecision{}, err
	}
	nodeAllocator := newConfigImportIdentityAllocator(nodeIDs)
	keyAllocator := newConfigImportIdentityAllocator(keyIDs)
	policyAllocator := newConfigImportIdentityAllocator(policyIDs)
	taskAllocator := newConfigImportIdentityAllocator(taskIDs)

	if err := evaluateConfigImportCredentials(ctx, &decision, accumulator, nodeAllocator, keyAllocator); err != nil {
		return configImportDecision{}, err
	}
	if err := evaluateConfigImportPolicies(ctx, &decision, accumulator, policyAllocator); err != nil {
		return configImportDecision{}, err
	}
	if err := evaluateConfigImportTasks(ctx, &decision, accumulator, taskAllocator); err != nil {
		return configImportDecision{}, err
	}
	if err := pruneConfigImportPendingChecks(ctx, &decision); err != nil {
		return configImportDecision{}, err
	}
	if err := configImportContextErr(ctx); err != nil {
		return configImportDecision{}, err
	}
	result := accumulator.finalize()
	decision.warnings = append([]configImportWarning(nil), result.Warnings...)
	decision.warningsTruncated = result.WarningsTruncated
	decision.skipped = result.Skipped
	decision.rejected = result.Rejected
	return decision, nil
}

func persistConfigImportDecision(ctx context.Context, tx *gorm.DB, decision *configImportDecision, accumulator *configImportAccumulator) error {
	if tx == nil || decision == nil || accumulator == nil {
		return fmt.Errorf("config import decision persistence is unavailable")
	}
	if decision.input.settingsSvc == nil && len(decision.input.settingsPlan) > 0 {
		return fmt.Errorf("config import settings service is unavailable")
	}
	seedConfigImportAccumulator(accumulator, decision)
	if err := persistConfigImportNodes(ctx, tx, decision, accumulator); err != nil {
		return err
	}
	failedKeys, err := persistConfigImportSSHKeys(tx, decision, accumulator)
	if err != nil {
		return err
	}
	if err := persistConfigImportNodeRelations(tx, decision, failedKeys, accumulator); err != nil {
		return err
	}
	propagateConfigImportRejectedNodeTasks(decision, accumulator)
	if err := persistConfigImportPolicies(ctx, tx, decision, accumulator); err != nil {
		return err
	}
	if err := persistConfigImportTasks(ctx, tx, decision, accumulator); err != nil {
		return err
	}
	for _, setting := range decision.input.settingsPlan {
		if err := decision.input.settingsSvc.UpdateWithTxContext(ctx, tx, setting.key, setting.value); err != nil {
			return fmt.Errorf("保存导入系统设置失败: %w", err)
		}
		if decision.snapshot.settings[setting.key] {
			accumulator.updated(configImportEntitySystemSettings)
		} else {
			accumulator.created(configImportEntitySystemSettings, false)
		}
	}
	return nil
}

func seedConfigImportAccumulator(dst *configImportAccumulator, decision *configImportDecision) {
	if dst == nil || decision == nil {
		return
	}
	if dst.warningKeys == nil {
		dst.warningKeys = make(map[configImportWarningKey]struct{})
	}
	for _, warning := range decision.warnings {
		key := configImportWarningKey{entity: warning.Entity, index: warning.Index, code: warning.Code}
		if _, exists := dst.warningKeys[key]; exists {
			continue
		}
		dst.warningKeys[key] = struct{}{}
		if len(dst.result.Warnings) < configImportWarningLimit {
			dst.result.Warnings = append(dst.result.Warnings, warning)
		}
	}
	dst.result.WarningsTruncated += decision.warningsTruncated
	dst.result.Skipped += decision.skipped
	dst.result.Rejected += decision.rejected
}

func evaluateConfigImportCredentials(ctx context.Context, decision *configImportDecision, accumulator *configImportAccumulator, nodeAllocator, keyAllocator *configImportIdentityAllocator) error {
	if err := configImportContextErr(ctx); err != nil {
		return err
	}
	input := decision.input
	inventory := decision.snapshot.credentials
	duplicateNodeIndexes, err := configImportDuplicateNameIndexesContext(ctx, input.envelope.Classic.Nodes)
	if err != nil {
		return err
	}
	duplicateKeyIndexes, err := configImportDuplicateNameIndexesContext(ctx, input.envelope.Classic.SSHKeys)
	if err != nil {
		return err
	}
	duplicateNodeNames, err := configImportDuplicateNameSetContext(ctx, input.envelope.Classic.Nodes)
	if err != nil {
		return err
	}
	duplicateKeyNames, err := configImportDuplicateNameSetContext(ctx, input.envelope.Classic.SSHKeys)
	if err != nil {
		return err
	}

	for index, nodeData := range input.envelope.Classic.Nodes {
		if err := configImportContextErr(ctx); err != nil {
			return err
		}
		name, _ := nodeData["name"].(string)
		name = strings.TrimSpace(name)
		reference, referenceCode := parseImportedNodeSSHKeyName(nodeData)
		targetRows := inventory.nodesByName[name]
		if name == "" {
			accumulator.rejected(configImportEntityNodes, index, name, configImportWarningInvalidInput)
			setConfigImportItemDecision(decision, configImportEntityNodes, index, name, configImportActionReject, "", false)
			continue
		}
		if duplicateNodeIndexes[index] || len(targetRows) > 1 {
			code := configImportWarningDuplicateName
			accumulator.rejected(configImportEntityNodes, index, name, code)
			setConfigImportItemDecision(decision, configImportEntityNodes, index, name, configImportActionReject, "", false)
			continue
		}
		if len(targetRows) == 1 && input.conflict != "overwrite" {
			accumulator.skipped()
			setConfigImportItemDecision(decision, configImportEntityNodes, index, name, configImportActionSkip, "", false)
			decision.nodeCandidates = append(decision.nodeCandidates, &configImportNodeCandidate{
				index: index, name: name, source: nodeData, row: targetRows[0], existing: true,
				skipped: true, action: configImportActionSkip, reference: reference, referenceCode: referenceCode,
			})
			continue
		}
		if input.preRejected[configImportEntityNodes][index] {
			accumulator.rejected(configImportEntityNodes, index, name, configImportWarningInvalidInput)
			setConfigImportItemDecision(decision, configImportEntityNodes, index, name, configImportActionReject, "", false)
			continue
		}
		if len(targetRows) == 1 {
			if referenceCode != "" && referenceCode != configImportWarningUnresolvedSSHKey {
				accumulator.rejectedWithWarnings(configImportEntityNodes, index, name, importedNodeReferenceWarningCodes(reference, referenceCode, nodeData, false)...)
				setConfigImportItemDecision(decision, configImportEntityNodes, index, name, configImportActionReject, "", false)
				continue
			}
			if reference.explicitNull {
				accumulator.rejectedWithWarnings(configImportEntityNodes, index, name, configImportWarningUnresolvedSSHKey)
				setConfigImportItemDecision(decision, configImportEntityNodes, index, name, configImportActionReject, "", false)
				continue
			}
			rowCandidate := targetRows[0]
			applyImportedNodeFields(&rowCandidate, nodeData)
			if err := validateImportedNodeCandidate(rowCandidate); err != nil {
				accumulator.rejected(configImportEntityNodes, index, name, configImportWarningInvalidInput)
				setConfigImportItemDecision(decision, configImportEntityNodes, index, name, configImportActionReject, "", false)
				continue
			}
			candidate := &configImportNodeCandidate{
				index: index, name: name, source: nodeData, row: rowCandidate, existing: true,
				action: configImportActionUpdate, reference: reference, referenceCode: referenceCode,
			}
			if inventory.nodePasswordPresent[candidate.row.ID] || inventory.nodePrivateKeyPresent[candidate.row.ID] {
				candidate.action = configImportActionPending
				candidate.pendingCode = configImportPendingRetainedSecret
				decision.pendingChecks = appendPendingCheck(decision.pendingChecks, configImportEntityNodes, index, configImportPendingRetainedSecret)
			}
			setConfigImportItemDecision(decision, configImportEntityNodes, index, name, candidate.action, candidate.pendingCode, false)
			decision.nodeCandidates = append(decision.nodeCandidates, candidate)
			continue
		}
		newNode := model.Node{Name: name, Status: "offline", Port: 22, AuthType: "key"}
		applyImportedNodeFields(&newNode, nodeData)
		if strings.TrimSpace(newNode.AuthType) == "" {
			newNode.AuthType = "key"
		}
		if password, ok := nodeData["password"].(string); ok {
			newNode.Password = password
		}
		if privateKey, ok := nodeData["private_key"].(string); ok {
			newNode.PrivateKey = privateKey
		}
		if strings.TrimSpace(newNode.BackupDir) == "" {
			newNode.BackupDir = nodePkg.SanitizeBackupDir(name)
		}
		if err := validateImportedNodeCandidate(newNode); err != nil {
			accumulator.rejected(configImportEntityNodes, index, name, configImportWarningInvalidInput)
			setConfigImportItemDecision(decision, configImportEntityNodes, index, name, configImportActionReject, "", false)
			continue
		}
		provisional, err := nodeAllocator.NextContext(ctx)
		if err != nil {
			return err
		}
		newNode.ID = provisional
		newNode.SSHKeyID = nil
		inventory.replaceNode(newNode)
		inventory.nodePasswordPresent[provisional] = strings.TrimSpace(newNode.Password) != ""
		inventory.nodePrivateKeyPresent[provisional] = strings.TrimSpace(newNode.PrivateKey) != ""
		setConfigImportItemDecision(decision, configImportEntityNodes, index, name, configImportActionCreate, "", false)
		decision.nodeCandidates = append(decision.nodeCandidates, &configImportNodeCandidate{
			index: index, name: name, source: nodeData, row: newNode, provisional: provisional, action: configImportActionCreate,
			baseCreated: true, reference: reference, referenceCode: referenceCode,
		})
	}

	for index, keyData := range input.envelope.Classic.SSHKeys {
		if err := configImportContextErr(ctx); err != nil {
			return err
		}
		name, _ := keyData["name"].(string)
		name = strings.TrimSpace(name)
		reference, referenceCode := parseImportedSSHKeyAllowedNodeNames(keyData)
		legacyScope, legacyCode := parseImportedSSHKeyScope(keyData)
		if reference.present && referenceCode == "" && !reference.explicitNull && !reference.invalid {
			if legacyCode == configImportWarningUnresolvedNodeScope {
				legacyCode = ""
			}
			legacyScope.allowedNodeIDs = nil
		}
		targetRows := inventory.keysByName[name]
		if name == "" {
			accumulator.rejected(configImportEntitySSHKeys, index, name, configImportWarningInvalidInput)
			setConfigImportItemDecision(decision, configImportEntitySSHKeys, index, name, configImportActionReject, "", false)
			continue
		}
		if duplicateKeyIndexes[index] || len(targetRows) > 1 {
			accumulator.rejected(configImportEntitySSHKeys, index, name, configImportWarningDuplicateName)
			setConfigImportItemDecision(decision, configImportEntitySSHKeys, index, name, configImportActionReject, "", false)
			continue
		}
		if len(targetRows) == 1 && input.conflict != "overwrite" {
			accumulator.skipped()
			setConfigImportItemDecision(decision, configImportEntitySSHKeys, index, name, configImportActionSkip, "", false)
			addImportedSSHKeyReferenceWarnings(accumulator, index, name, reference, referenceCode, legacyCode, inventory, duplicateNodeNames)
			decision.keyCandidates = append(decision.keyCandidates, &configImportSSHKeyCandidate{
				index: index, name: name, source: keyData, row: targetRows[0], existing: true,
				action: configImportActionSkip, reference: reference, referenceCode: referenceCode,
				legacyScope: legacyScope, legacyCode: legacyCode,
			})
			continue
		}
		candidate := &configImportSSHKeyCandidate{
			index: index, name: name, source: keyData, action: configImportActionCreate,
			reference: reference, referenceCode: referenceCode, legacyScope: legacyScope, legacyCode: legacyCode,
		}
		if len(targetRows) == 1 {
			candidate.existing = true
			candidate.action = configImportActionUpdate
			candidate.row = targetRows[0]
		}
		if candidate.existing && (referenceCode != "" || legacyCode != "") {
			candidate.rejected = true
			candidate.action = configImportActionReject
			accumulator.rejectedWithWarnings(configImportEntitySSHKeys, index, name, importedSSHKeyReferenceWarningCodes(reference, referenceCode, legacyCode)...)
			setConfigImportItemDecision(decision, configImportEntitySSHKeys, index, name, configImportActionReject, "", false)
			decision.keyCandidates = append(decision.keyCandidates, candidate)
			continue
		}
		if reference.present && len(reference.names) > 0 {
			ids, ok := resolveImportedNodeNameScope(reference.names, inventory, duplicateNodeNames)
			if !ok {
				if candidate.existing {
					candidate.rejected = true
					candidate.action = configImportActionReject
					accumulator.rejectedWithWarnings(configImportEntitySSHKeys, index, name, configImportWarningUnresolvedNodeScope)
					setConfigImportItemDecision(decision, configImportEntitySSHKeys, index, name, configImportActionReject, "", false)
					decision.keyCandidates = append(decision.keyCandidates, candidate)
					continue
				}
				candidate.referenceCode = configImportWarningUnresolvedNodeScope
			} else {
				candidate.legacyScope.allowedNodeIDs = &ids
				candidate.nameScopeResolved = true
			}
		} else if reference.present && reference.explicitEmpty && referenceCode == "" {
			empty := ""
			candidate.legacyScope.allowedNodeIDs = &empty
		}
		if _, err := importedSSHKeySelectedType(keyData, candidate.row.KeyType); err != nil {
			candidate.rejected = true
			candidate.action = configImportActionReject
			accumulator.rejectedWithWarnings(configImportEntitySSHKeys, index, name, configImportWarningInvalidInput)
			setConfigImportItemDecision(decision, configImportEntitySSHKeys, index, name, configImportActionReject, "", false)
			decision.keyCandidates = append(decision.keyCandidates, candidate)
			continue
		}
		pending, err := prepareConfigImportSSHKeyCandidateRow(
			candidate, candidate.existing && inventory.keyPrivateKeyPresent[candidate.row.ID], false,
		)
		if err != nil {
			candidate.rejected = true
			candidate.action = configImportActionReject
			accumulator.rejectedWithWarnings(configImportEntitySSHKeys, index, name, configImportWarningInvalidPrivateKey)
			setConfigImportItemDecision(decision, configImportEntitySSHKeys, index, name, configImportActionReject, "", false)
			decision.keyCandidates = append(decision.keyCandidates, candidate)
			continue
		}
		if pending {
			candidate.action = configImportActionPending
			candidate.pendingCode = configImportPendingRetainedSecret
			decision.pendingChecks = appendPendingCheck(decision.pendingChecks, configImportEntitySSHKeys, index, configImportPendingRetainedSecret)
		} else if strings.TrimSpace(candidate.row.PrivateKey) == "" {
			accumulator.warning(configImportEntitySSHKeys, index, name, configImportWarningMissingPrivateKey)
		}
		if candidate.referenceCode != "" || candidate.legacyCode != "" {
			for _, code := range importedSSHKeyReferenceWarningCodes(candidate.reference, candidate.referenceCode, candidate.legacyCode) {
				accumulator.warning(configImportEntitySSHKeys, index, name, code)
			}
		}
		if !candidate.existing {
			provisional, err := keyAllocator.NextContext(ctx)
			if err != nil {
				return err
			}
			candidate.provisional = provisional
			candidate.row.ID = provisional
			inventory.replaceKey(candidate.row)
			inventory.keyPrivateKeyPresent[provisional] = strings.TrimSpace(candidate.row.PrivateKey) != ""
		}
		setConfigImportItemDecision(decision, configImportEntitySSHKeys, index, name, candidate.action, candidate.pendingCode, !candidate.existing && candidate.row.Disabled)
		decision.keyCandidates = append(decision.keyCandidates, candidate)
	}

	for _, candidate := range decision.nodeCandidates {
		if err := configImportContextErr(ctx); err != nil {
			return err
		}
		if candidate == nil {
			continue
		}
		if candidate.skipped {
			_, resolved := resolveImportedNodeKeyReference(candidate.reference, candidate.referenceCode, inventory, duplicateKeyNames)
			addImportedNodeReferenceWarnings(accumulator, candidate.index, candidate.name, candidate.source, candidate.reference, candidate.referenceCode, resolved)
			continue
		}
		if candidate.rejected {
			continue
		}
		resolvedKey, resolved := resolveImportedNodeKeyReference(candidate.reference, candidate.referenceCode, inventory, duplicateKeyNames)
		if candidate.existing && !resolved && nodeReferenceRequiresExistingResolution(candidate.reference, candidate.referenceCode) {
			candidate.rejected = true
			candidate.action = configImportActionReject
			accumulator.rejectedWithWarnings(configImportEntityNodes, candidate.index, candidate.name, importedNodeReferenceWarningCodes(candidate.reference, candidate.referenceCode, candidate.source, false)...)
			setConfigImportItemDecision(decision, configImportEntityNodes, candidate.index, candidate.name, configImportActionReject, "", false)
			continue
		}
		if resolved {
			candidate.row.SSHKeyID = &resolvedKey.ID
			if configImportKeyPending(decision, resolvedKey.ID) && candidate.pendingCode == "" {
				candidate.action = configImportActionPending
				candidate.pendingCode = configImportPendingReference
				decision.pendingChecks = appendPendingCheck(decision.pendingChecks, configImportEntityNodes, candidate.index, configImportPendingReference)
			}
		} else if candidate.reference.present && candidate.reference.explicitEmpty {
			candidate.row.SSHKeyID = nil
		}
		passwordPresent := importedConfigSecretPresent(candidate.source, "password")
		privateKeyPresent := importedConfigSecretPresent(candidate.source, "private_key")
		if candidate.existing {
			passwordPresent = inventory.nodePasswordPresent[candidate.row.ID]
			privateKeyPresent = inventory.nodePrivateKeyPresent[candidate.row.ID]
		}
		addImportedNodeCredentialWarningsWithPresence(
			accumulator, candidate.index, candidate.name, candidate.source, candidate.row,
			passwordPresent, privateKeyPresent,
		)
		if !resolved && !candidate.existing {
			addImportedNodeReferenceWarnings(accumulator, candidate.index, candidate.name, candidate.source, candidate.reference, candidate.referenceCode, false)
		}
		setConfigImportItemDecision(decision, configImportEntityNodes, candidate.index, candidate.name, candidate.action, candidate.pendingCode, false)
	}
	return nil
}

func evaluateConfigImportPolicies(ctx context.Context, decision *configImportDecision, accumulator *configImportAccumulator, allocator *configImportIdentityAllocator) error {
	if err := configImportContextErr(ctx); err != nil {
		return err
	}
	input := decision.input
	for index, data := range input.envelope.Classic.Policies {
		if err := configImportContextErr(ctx); err != nil {
			return err
		}
		name, _ := data["name"].(string)
		name = strings.TrimSpace(name)
		if name == "" {
			accumulator.rejected(configImportEntityPolicies, index, name, configImportWarningInvalidInput)
			setConfigImportItemDecision(decision, configImportEntityPolicies, index, name, configImportActionReject, "", false)
			continue
		}
		targetRows := decision.snapshot.policiesByName[name]
		if len(targetRows) > 1 {
			accumulator.rejected(configImportEntityPolicies, index, name, configImportWarningDuplicateName)
			setConfigImportItemDecision(decision, configImportEntityPolicies, index, name, configImportActionReject, "", false)
			continue
		}
		if len(targetRows) == 1 && input.conflict != "overwrite" {
			accumulator.skipped()
			setConfigImportItemDecision(decision, configImportEntityPolicies, index, name, configImportActionSkip, "", false)
			continue
		}
		if input.preRejected[configImportEntityPolicies][index] {
			accumulator.rejected(configImportEntityPolicies, index, name, configImportWarningInvalidInput)
			setConfigImportItemDecision(decision, configImportEntityPolicies, index, name, configImportActionReject, "", false)
			continue
		}
		candidate := &configImportPolicyCandidate{index: index, name: name, source: data}
		if len(targetRows) == 1 {
			candidate.existing = true
			candidate.action = configImportActionUpdate
			candidate.row = targetRows[0]
		} else {
			candidate.action = configImportActionCreate
			candidate.row = model.Policy{
				Name: name, MaxConcurrent: 1, RetentionDays: 7, RetentionMode: "simple",
				Enabled: false, VerifyEnabled: true, HookTimeoutSeconds: 300, MaxRetries: 2,
				RetryBaseSeconds: 30, DrillRestorePath: "/tmp/xirang-drill", DrillAutoCleanup: true,
			}
		}
		if err := applyImportedPolicyFields(&candidate.row, data); err != nil {
			candidate.rejected = true
			candidate.action = configImportActionReject
			accumulator.rejected(configImportEntityPolicies, index, name, configImportWarningInvalidInput)
			setConfigImportItemDecision(decision, configImportEntityPolicies, index, name, configImportActionReject, "", false)
			decision.policyCandidates = append(decision.policyCandidates, candidate)
			continue
		}
		if !candidate.existing {
			provisional, err := allocator.NextContext(ctx)
			if err != nil {
				return err
			}
			candidate.provisional = provisional
			candidate.row.ID = provisional
		}
		setConfigImportItemDecision(decision, configImportEntityPolicies, index, name, candidate.action, "", false)
		decision.policyCandidates = append(decision.policyCandidates, candidate)
		decision.snapshot.policiesByName[name] = []model.Policy{candidate.row}
		decision.snapshot.policiesByID[candidate.row.ID] = candidate.row
	}
	return nil
}

func evaluateConfigImportTasks(ctx context.Context, decision *configImportDecision, accumulator *configImportAccumulator, allocator *configImportIdentityAllocator) error {
	if err := configImportContextErr(ctx); err != nil {
		return err
	}
	input := decision.input
	nodeByName := decision.snapshot.credentials.nodesByName
	policyByName := decision.snapshot.policiesByName
	for _, candidate := range decision.policyCandidates {
		if err := configImportContextErr(ctx); err != nil {
			return err
		}
		if candidate == nil || candidate.rejected {
			continue
		}
		policyByName[candidate.name] = append([]model.Policy(nil), candidate.row)
	}
	duplicateCandidates := make(map[importTaskKey]*configImportTaskCandidate)
	for index, data := range input.envelope.Classic.Tasks {
		if err := configImportContextErr(ctx); err != nil {
			return err
		}
		name := strings.TrimSpace(readStringField(data, "name"))
		if name == "" {
			accumulator.rejected(configImportEntityTasks, index, name, configImportWarningInvalidInput)
			continue
		}
		nodeID := uint(0)
		if nodeName := readStringField(data, "node_name"); nodeName != "" {
			if rows := nodeByName[nodeName]; len(rows) == 1 {
				nodeID = rows[0].ID
			}
		}
		if nodeID == 0 {
			accumulator.rejected(configImportEntityTasks, index, name, configImportWarningInvalidInput)
			continue
		}
		key := buildImportTaskKey(name, nodeID)
		rows := decision.snapshot.tasksByKey[key]
		var existingTask model.Task
		existingFound := len(rows) > 0
		if existingFound {
			existingTask = rows[0]
		}
		previous := duplicateCandidates[key]
		if (existingFound || previous != nil) && input.conflict != "overwrite" {
			accumulator.skipped()
			setConfigImportItemDecision(decision, configImportEntityTasks, index, name, configImportActionSkip, "", false)
			continue
		}
		if input.preRejected[configImportEntityTasks][index] {
			accumulator.rejected(configImportEntityTasks, index, name, configImportWarningInvalidInput)
			continue
		}
		policyID, policyFound, policySpecified := resolveConfigImportPolicyID(data, policyByName, decision.snapshot.policiesByID)
		if policySpecified && !policyFound {
			accumulator.rejected(configImportEntityTasks, index, name, configImportWarningInvalidInput)
			continue
		}
		var policyPtr *uint
		if policyFound {
			policyPtr = &policyID
		}
		req := taskPkg.CreateTaskInput{
			Name: name, NodeID: nodeID, PolicyID: policyPtr,
			Command: readStringField(data, "command"), RsyncSource: readStringField(data, "rsync_source"),
			RsyncTarget: readStringField(data, "rsync_target"), ExecutorType: readStringField(data, "executor_type"),
			ExecutorConfig: readStringField(data, "executor_config"), CronSpec: readStringField(data, "cron_spec"),
		}
		dependencyKey, hasDependency := resolveConfigImportDependencyKey(data, nodeByName, nodeID)
		if importTaskFieldSpecified(data, "depends_on_task_name") && !hasDependency {
			accumulator.rejected(configImportEntityTasks, index, name, configImportWarningInvalidInput)
			continue
		}
		importedCronOverride, hasImportedCronOverride := readImportedBoolField(data, "cron_override")
		explicitCronSpec := req.CronSpec
		_, hasExplicitCronSpec := data["cron_spec"]
		targetSource := configImportTargetExplicit
		pendingCode := ""
		if policyFound {
			policy, ok := resolveConfigImportPolicyRow(policyID, policyByName, decision.snapshot.policiesByID)
			if !ok {
				accumulator.rejected(configImportEntityTasks, index, name, configImportWarningInvalidInput)
				continue
			}
			if strings.TrimSpace(req.RsyncSource) == "" {
				req.RsyncSource = policy.SourcePath
			}
			if strings.TrimSpace(req.RsyncTarget) == "" {
				targetSource = configImportTargetPolicyDefault
				if configImportIdentityProvisionalPolicy(policyID, decision.policyCandidates) || configImportIdentityProvisional(nodeID, decision.nodeCandidates) {
					pendingCode = configImportPendingAllocatedID
				} else {
					req.RsyncTarget = policyPkg.PolicyNodeTargetPath(policy.TargetPath, policy.ID, req.NodeID)
				}
			}
			if strings.TrimSpace(req.CronSpec) == "" {
				req.CronSpec = policy.CronSpec
			}
		}
		if hasImportedCronOverride && importedCronOverride && hasExplicitCronSpec {
			req.CronSpec = explicitCronSpec
		}
		taskPkg.TrimTaskInput(&req)
		taskPkg.InferTaskExecutor(&req, "")
		if strings.EqualFold(strings.TrimSpace(req.ExecutorType), "restic") {
			migrated, err := taskPkg.NormalizeImportedResticConfig(req.ExecutorConfig)
			if err != nil {
				accumulator.rejected(configImportEntityTasks, index, name, configImportWarningInvalidInput)
				continue
			}
			req.ExecutorConfig = migrated
		}
		managedRsync := importedRsyncConfigRequiresDisconnect(req.ExecutorType, req.ExecutorConfig)
		managedRclone := importedRcloneConfigRequiresDisconnect(req.ExecutorType, req.ExecutorConfig)
		if managedRsync {
			req.RsyncSource, req.RsyncTarget = "", ""
			req.ExecutorConfig = canonicalLegacyRsyncImportConfig()
		} else if managedRclone {
			req.RsyncTarget = ""
			req.ExecutorConfig = canonicalLegacyRcloneImportConfig()
		} else if strings.TrimSpace(req.RsyncTarget) == "" {
			if req.ExecutorType == "command" {
				targetSource = configImportTargetExplicit
			} else if req.PolicyID != nil {
				if configImportIdentityProvisionalPolicy(*req.PolicyID, decision.policyCandidates) || configImportIdentityProvisional(req.NodeID, decision.nodeCandidates) {
					pendingCode = configImportPendingAllocatedID
				} else {
					policy, ok := resolveConfigImportPolicyRow(*req.PolicyID, policyByName, decision.snapshot.policiesByID)
					if ok {
						req.RsyncTarget = policyPkg.PolicyNodeTargetPath(policy.TargetPath, *req.PolicyID, req.NodeID)
					}
				}
			} else if configImportIdentityProvisional(req.NodeID, decision.nodeCandidates) {
				targetSource = configImportTargetManualDefault
				pendingCode = configImportPendingAllocatedID
			} else {
				targetSource = configImportTargetManualDefault
				taskPkg.AutoGenerateTarget(ctx, nil, &req)
			}
		}
		if hasDependency && strings.TrimSpace(explicitCronSpec) == "" {
			req.CronSpec = ""
		}
		var validationErr error
		if managedRsync {
			validationErr = taskPkg.ValidateDisconnectedImportedRsyncTask(req)
		} else if managedRclone {
			validationErr = taskPkg.ValidateDisconnectedImportedRcloneTask(req)
		} else if pendingCode != "" {
			validationErr = taskPkg.ValidateTaskInputWithoutTarget(req)
		} else {
			validationErr = taskPkg.ValidateTaskInput(req)
		}
		if validationErr != nil {
			if pendingCode == "" && !taskPkg.IsTaskValidationError(validationErr) {
				return fmt.Errorf("校验导入任务失败: %w", validationErr)
			}
			accumulator.rejected(configImportEntityTasks, index, name, configImportWarningInvalidInput)
			continue
		}
		baseTask := existingTask
		existing := existingFound
		if previous != nil {
			baseTask = previous.task
			existing = previous.existing
		}
		candidateTask := baseTask
		var provisional uint
		if !existing {
			var err error
			provisional, err = allocator.NextContext(ctx)
			if err != nil {
				return err
			}
			candidateTask = model.Task{
				ID: provisional, Name: req.Name, NodeID: req.NodeID, PolicyID: req.PolicyID,
				Command: req.Command, RsyncSource: req.RsyncSource, RsyncTarget: req.RsyncTarget,
				ExecutorType: req.ExecutorType, ExecutorConfig: req.ExecutorConfig, CronSpec: req.CronSpec,
				Status: "pending", Source: readStringField(data, "source"), Enabled: !managedRsync && !managedRclone,
			}
			if candidateTask.Source == "" {
				candidateTask.Source = "manual"
			}
		} else {
			candidateTask.DependsOnTaskID = nil
			candidateTask.Command, candidateTask.RsyncSource, candidateTask.RsyncTarget = req.Command, req.RsyncSource, req.RsyncTarget
			candidateTask.ExecutorType, candidateTask.ExecutorConfig, candidateTask.CronSpec = req.ExecutorType, req.ExecutorConfig, req.CronSpec
			candidateTask.Source = readStringField(data, "source")
		}
		if hasImportedCronOverride {
			candidateTask.CronOverride = importedCronOverride
		}
		if managedRsync || managedRclone {
			candidateTask.Enabled = false
		} else if enabled, ok := readImportedBoolField(data, "enabled"); ok {
			candidateTask.Enabled = enabled
		}
		explicitEnabled, hasExplicitEnabled := readImportedBoolField(data, "enabled")
		if !existing && hasExplicitEnabled && !managedRsync && !managedRclone {
			candidateTask.Enabled = explicitEnabled
		}
		candidate := &configImportTaskCandidate{
			task: candidateTask, provisional: provisional, req: req, dependencyKey: dependencyKey, hasDependency: hasDependency,
			taskIndex: index, name: name, existing: existing, action: configImportActionCreate,
			pendingCode: pendingCode, targetSource: targetSource, hasImportedCronOverride: hasImportedCronOverride,
			importedCronOverride: importedCronOverride, managedRsync: managedRsync, managedRclone: managedRclone,
			hasExplicitEnabled: hasExplicitEnabled, requestedEnabled: candidateTask.Enabled,
			previousCronSpec: strings.TrimSpace(baseTask.CronSpec),
		}
		if existing {
			candidate.action = configImportActionUpdate
		}
		if pendingCode == "" && configImportNodePending(decision, req.NodeID) {
			candidate.pendingCode = configImportPendingReference
			pendingCode = configImportPendingReference
		}
		if pendingCode != "" {
			candidate.action = configImportActionPending
			decision.pendingChecks = appendPendingCheck(decision.pendingChecks, configImportEntityTasks, index, pendingCode)
		}
		setConfigImportItemDecision(decision, configImportEntityTasks, index, name, candidate.action, candidate.pendingCode, false)
		if previous != nil && input.conflict == "overwrite" {
			setConfigImportItemDecision(decision, configImportEntityTasks, previous.taskIndex, previous.name, configImportActionSkip, "", false)
			*previous = *candidate
			candidate = previous
		} else {
			decision.taskCandidates = append(decision.taskCandidates, candidate)
		}
		duplicateCandidates[key] = candidate
	}
	candidateIDs := make(map[importTaskKey]uint, len(decision.taskCandidates))
	candidateRows := make(map[uint]model.Task, len(decision.taskCandidates))
	for _, candidate := range decision.taskCandidates {
		if err := configImportContextErr(ctx); err != nil {
			return err
		}
		if candidate == nil || candidate.rejected {
			continue
		}
		key := buildImportTaskKey(candidate.task.Name, candidate.task.NodeID)
		candidateIDs[key] = candidate.task.ID
		candidateRows[candidate.task.ID] = candidate.task
	}
	for _, candidate := range decision.taskCandidates {
		if err := configImportContextErr(ctx); err != nil {
			return err
		}
		if candidate == nil || candidate.rejected || !candidate.hasDependency {
			continue
		}
		dependencyID, ok := candidateIDs[candidate.dependencyKey]
		if !ok {
			rows := decision.snapshot.tasksByKey[candidate.dependencyKey]
			if len(rows) > 0 && rows[0].ArchivedAt == nil {
				dependencyID = rows[0].ID
				ok = true
			}
		}
		if !ok || dependencyID == 0 {
			candidate.rejected = true
			candidate.action = configImportActionReject
			accumulator.rejected(configImportEntityTasks, candidate.taskIndex, candidate.name, configImportWarningInvalidInput)
			setConfigImportItemDecision(decision, configImportEntityTasks, candidate.taskIndex, candidate.name, configImportActionReject, "", false)
			continue
		}
		candidate.dependencyID = &dependencyID
		candidate.req.DependsOnTaskID = &dependencyID
		candidate.task.DependsOnTaskID = &dependencyID
		candidateRows[candidate.task.ID] = candidate.task
		if dependency, exists := candidateRows[dependencyID]; exists && dependency.ID != 0 {
			for _, other := range decision.taskCandidates {
				if err := configImportContextErr(ctx); err != nil {
					return err
				}
				if other != nil && other.task.ID == dependency.ID && other.pendingCode != "" && candidate.pendingCode == "" {
					candidate.pendingCode = configImportPendingReference
					candidate.action = configImportActionPending
					decision.pendingChecks = appendPendingCheck(decision.pendingChecks, configImportEntityTasks, candidate.taskIndex, configImportPendingReference)
					setConfigImportItemDecision(decision, configImportEntityTasks, candidate.taskIndex, candidate.name, configImportActionPending, configImportPendingReference, false)
				}
			}
		}
	}
	changed := true
	for changed {
		if err := configImportContextErr(ctx); err != nil {
			return err
		}
		changed = false
		for _, candidate := range decision.taskCandidates {
			if err := configImportContextErr(ctx); err != nil {
				return err
			}
			if candidate == nil || candidate.rejected || candidate.dependencyID == nil {
				continue
			}
			for _, dependency := range decision.taskCandidates {
				if err := configImportContextErr(ctx); err != nil {
					return err
				}
				if dependency == nil || dependency.task.ID != *candidate.dependencyID {
					continue
				}
				if dependency.rejected {
					candidate.rejected = true
					candidate.action = configImportActionReject
					accumulator.rejected(configImportEntityTasks, candidate.taskIndex, candidate.name, configImportWarningInvalidInput)
					setConfigImportItemDecision(decision, configImportEntityTasks, candidate.taskIndex, candidate.name, configImportActionReject, "", false)
					changed = true
				} else if dependency.pendingCode != "" && candidate.pendingCode == "" {
					candidate.pendingCode = configImportPendingReference
					candidate.action = configImportActionPending
					decision.pendingChecks = appendPendingCheck(decision.pendingChecks, configImportEntityTasks, candidate.taskIndex, configImportPendingReference)
					setConfigImportItemDecision(decision, configImportEntityTasks, candidate.taskIndex, candidate.name, configImportActionPending, configImportPendingReference, false)
					changed = true
				}
				break
			}
		}
	}
	if err := validateConfigImportTaskGraph(ctx, decision, candidateRows, accumulator); err != nil {
		return err
	}
	return nil
}

func validateConfigImportTaskGraph(ctx context.Context, decision *configImportDecision, candidateRows map[uint]model.Task, accumulator *configImportAccumulator) error {
	if err := configImportContextErr(ctx); err != nil {
		return err
	}
	state := make(map[uint]uint8, len(candidateRows))
	for _, candidate := range decision.taskCandidates {
		if err := configImportContextErr(ctx); err != nil {
			return err
		}
		if candidate == nil || candidate.rejected {
			continue
		}
		path := make([]uint, 0, 8)
		current := candidate.task.ID
		valid := true
		for current != 0 {
			if err := configImportContextErr(ctx); err != nil {
				return err
			}
			if state[current] != 0 {
				valid = state[current] == 2
				break
			}
			state[current] = 1
			path = append(path, current)
			if row, ok := candidateRows[current]; ok {
				if row.DependsOnTaskID == nil {
					current = 0
				} else {
					current = *row.DependsOnTaskID
				}
				continue
			}
			row, ok := decision.snapshot.tasksByID[current]
			if !ok || row.ArchivedAt != nil {
				valid = false
				break
			}
			if row.DependsOnTaskID == nil {
				current = 0
			} else {
				current = *row.DependsOnTaskID
			}
		}
		status := uint8(2)
		if !valid {
			status = 3
			candidate.rejected = true
			candidate.action = configImportActionReject
			accumulator.rejected(configImportEntityTasks, candidate.taskIndex, candidate.name, configImportWarningInvalidInput)
			setConfigImportItemDecision(decision, configImportEntityTasks, candidate.taskIndex, candidate.name, configImportActionReject, "", false)
		}
		for _, id := range path {
			if err := configImportContextErr(ctx); err != nil {
				return err
			}
			state[id] = status
		}
	}
	// Validate references against the loaded, read-only closure. The evaluator
	// deliberately does not retain a *gorm.DB or instantiate a repository that
	// could fall back to an unloaded query.
	nodeIDs := make(map[uint]struct{}, len(decision.snapshot.credentials.nodes))
	for _, node := range decision.snapshot.credentials.nodes {
		if err := configImportContextErr(ctx); err != nil {
			return err
		}
		nodeIDs[node.ID] = struct{}{}
	}
	policyIDs := make(map[uint]struct{}, len(decision.snapshot.policiesByID))
	for id := range decision.snapshot.policiesByID {
		if err := configImportContextErr(ctx); err != nil {
			return err
		}
		policyIDs[id] = struct{}{}
	}
	for _, candidate := range decision.taskCandidates {
		if err := configImportContextErr(ctx); err != nil {
			return err
		}
		if candidate == nil || candidate.rejected {
			continue
		}
		if _, ok := nodeIDs[candidate.req.NodeID]; !ok {
			candidate.rejected = true
			candidate.action = configImportActionReject
			accumulator.rejected(configImportEntityTasks, candidate.taskIndex, candidate.name, configImportWarningInvalidInput)
			setConfigImportItemDecision(decision, configImportEntityTasks, candidate.taskIndex, candidate.name, configImportActionReject, "", false)
			continue
		}
		if candidate.req.PolicyID != nil {
			if _, ok := policyIDs[*candidate.req.PolicyID]; !ok {
				candidate.rejected = true
				candidate.action = configImportActionReject
				accumulator.rejected(configImportEntityTasks, candidate.taskIndex, candidate.name, configImportWarningInvalidInput)
				setConfigImportItemDecision(decision, configImportEntityTasks, candidate.taskIndex, candidate.name, configImportActionReject, "", false)
				continue
			}
		}
		if candidate.dependencyID == nil {
			continue
		}
		if strings.TrimSpace(candidate.req.CronSpec) != "" || *candidate.dependencyID == candidate.task.ID {
			candidate.rejected = true
			candidate.action = configImportActionReject
			accumulator.rejected(configImportEntityTasks, candidate.taskIndex, candidate.name, configImportWarningInvalidInput)
			setConfigImportItemDecision(decision, configImportEntityTasks, candidate.taskIndex, candidate.name, configImportActionReject, "", false)
			continue
		}
		if _, ok := candidateRows[*candidate.dependencyID]; ok {
			continue
		}
		if row, ok := decision.snapshot.tasksByID[*candidate.dependencyID]; !ok || row.ArchivedAt != nil {
			candidate.rejected = true
			candidate.action = configImportActionReject
			accumulator.rejected(configImportEntityTasks, candidate.taskIndex, candidate.name, configImportWarningInvalidInput)
			setConfigImportItemDecision(decision, configImportEntityTasks, candidate.taskIndex, candidate.name, configImportActionReject, "", false)
		}
	}
	if err := propagateConfigImportTaskDependencyResults(ctx, decision, accumulator); err != nil {
		return err
	}
	claims := append([]policyPkg.TargetOwner(nil), decision.snapshot.targetClaims...)
	for _, candidate := range decision.taskCandidates {
		if err := configImportContextErr(ctx); err != nil {
			return err
		}
		if candidate == nil || candidate.rejected || candidate.pendingCode != "" || candidate.existing {
			continue
		}
		if err := validateConfigImportNewTaskTarget(candidate, &claims); err != nil {
			return err
		}
	}
	return nil
}
func propagateConfigImportTaskDependencyResults(ctx context.Context, decision *configImportDecision, accumulator *configImportAccumulator) error {
	if decision == nil || accumulator == nil {
		return nil
	}
	changed := true
	for changed {
		if err := configImportContextErr(ctx); err != nil {
			return err
		}
		changed = false
		for _, candidate := range decision.taskCandidates {
			if err := configImportContextErr(ctx); err != nil {
				return err
			}
			if candidate == nil || candidate.rejected || candidate.dependencyID == nil {
				continue
			}
			var dependency *configImportTaskCandidate
			for _, possible := range decision.taskCandidates {
				if err := configImportContextErr(ctx); err != nil {
					return err
				}
				if possible != nil && possible.task.ID == *candidate.dependencyID {
					dependency = possible
					break
				}
			}
			if dependency == nil {
				continue
			}
			if dependency.rejected {
				candidate.rejected = true
				candidate.action = configImportActionReject
				accumulator.rejected(configImportEntityTasks, candidate.taskIndex, candidate.name, configImportWarningInvalidInput)
				setConfigImportItemDecision(decision, configImportEntityTasks, candidate.taskIndex, candidate.name, configImportActionReject, "", false)
				changed = true
				continue
			}
			if dependency.pendingCode != "" && candidate.pendingCode == "" {
				candidate.pendingCode = configImportPendingReference
				candidate.action = configImportActionPending
				decision.pendingChecks = appendPendingCheck(decision.pendingChecks, configImportEntityTasks, candidate.taskIndex, configImportPendingReference)
				setConfigImportItemDecision(decision, configImportEntityTasks, candidate.taskIndex, candidate.name, configImportActionPending, configImportPendingReference, false)
				changed = true
			}
		}
	}
	return nil
}

func persistConfigImportNodes(ctx context.Context, tx *gorm.DB, decision *configImportDecision, accumulator *configImportAccumulator) error {
	for _, candidate := range decision.nodeCandidates {
		if candidate == nil || candidate.rejected || candidate.action == configImportActionSkip {
			continue
		}
		if !candidate.existing {
			provisional := candidate.provisional
			if provisional == 0 {
				return fmt.Errorf("导入节点临时身份不可用")
			}
			row := candidate.row
			row.ID = 0
			row.SSHKeyID = nil
			if err := tx.WithContext(ctx).Create(&row).Error; err != nil {
				return fmt.Errorf("创建导入节点失败: %w", err)
			}
			candidate.row = row
			decision.snapshot.credentials.remapNodeIdentity(provisional, row)
			decisionNodeIDMap(decision)[provisional] = row.ID
			accumulator.recordCreatedID(configImportEntityNodes, row.ID)
			accumulator.created(configImportEntityNodes, false)
		}
	}
	return nil
}

func persistConfigImportSSHKeys(tx *gorm.DB, decision *configImportDecision, accumulator *configImportAccumulator) (map[string]bool, error) {
	failed := make(map[string]bool)
	provisionalNodeIDs := configImportProvisionalNodeIDs(decision.nodeCandidates)
	for _, candidate := range decision.keyCandidates {
		if candidate == nil || candidate.rejected || candidate.action == configImportActionSkip {
			continue
		}
		provisional := candidate.provisional
		if !candidate.existing {
			if provisional == 0 {
				return nil, fmt.Errorf("导入 SSH 密钥临时身份不可用")
			}
			if candidate.nameScopeResolved {
				if err := remapConfigImportSSHKeyScope(candidate, decisionNodeIDMap(decision), provisionalNodeIDs); err != nil {
					return nil, err
				}
			}
			if err := createImportedSSHKey(tx, candidate, accumulator); err != nil {
				return nil, err
			}
			if candidate.rejected {
				failed[candidate.name] = true
				continue
			}
			decision.snapshot.credentials.remapKeyIdentity(provisional, candidate.row)
			decisionKeyIDMap(decision)[provisional] = candidate.row.ID
			continue
		}
		var existing model.SSHKey
		if err := tx.Session(&gorm.Session{SkipHooks: true}).Where("id = ?", candidate.row.ID).First(&existing).Error; err != nil {
			return nil, fmt.Errorf("读取导入 SSH 密钥失败: %w", err)
		}
		candidate.row = existing
		if candidate.nameScopeResolved {
			if err := remapConfigImportSSHKeyScope(candidate, decisionNodeIDMap(decision), provisionalNodeIDs); err != nil {
				return nil, err
			}
		}
		if err := applyImportedSSHKeyCandidate(tx, decision.snapshot.credentials, candidate, accumulator); err != nil {
			return nil, err
		}
		if candidate.rejected {
			failed[candidate.name] = true
			if candidate.pendingCode != "" {
				// applyImportedSSHKeyCandidate already emitted the safe warning.
				if candidate.action != configImportActionReject {
					candidate.action = configImportActionReject
				}
			}
			continue
		}
	}
	return failed, nil
}

func persistConfigImportNodeRelations(tx *gorm.DB, decision *configImportDecision, failedKeys map[string]bool, accumulator *configImportAccumulator) error {
	ctx := context.Background()
	if tx != nil && tx.Statement != nil && tx.Statement.Context != nil {
		ctx = tx.Statement.Context
	}
	duplicateSSHKeyNames, err := configImportDuplicateNameSetContext(ctx, decision.input.envelope.Classic.SSHKeys)
	if err != nil {
		return err
	}
	for _, candidate := range decision.nodeCandidates {
		if candidate == nil || candidate.rejected || candidate.action == configImportActionSkip {
			continue
		}
		resolvedKey, resolved := resolveImportedNodeKeyReference(candidate.reference, candidate.referenceCode, decision.snapshot.credentials, duplicateSSHKeyNames)
		if resolved && failedKeys[candidate.reference.name] {
			if candidate.existing {
				candidate.rejected = true
				candidate.action = configImportActionReject
				accumulator.rejectedWithWarnings(configImportEntityNodes, candidate.index, candidate.name, configImportWarningUnresolvedSSHKey)
				setConfigImportItemDecision(decision, configImportEntityNodes, candidate.index, candidate.name, configImportActionReject, "", false)
				continue
			}
			resolved = false
			accumulator.warning(configImportEntityNodes, candidate.index, candidate.name, configImportWarningUnresolvedSSHKey)
		}
		var node model.Node
		if !candidate.existing {
			node = candidate.row
		} else {
			loaded, err := loadImportedNodeForOverwrite(tx, candidate.row.ID)
			if err != nil {
				return fmt.Errorf("读取导入节点失败: %w", err)
			}
			if err := decryptImportedNodeSecrets(&loaded); err != nil {
				candidate.rejected = true
				candidate.action = configImportActionReject
				accumulator.rejected(configImportEntityNodes, candidate.index, candidate.name, configImportWarningInvalidInput)
				continue
			}
			applyImportedNodeFields(&loaded, candidate.source)
			if err := validateImportedNodeCandidate(loaded); err != nil {
				candidate.rejected = true
				candidate.action = configImportActionReject
				accumulator.rejected(configImportEntityNodes, candidate.index, candidate.name, configImportWarningInvalidInput)
				continue
			}
			node = loaded
		}
		if resolved {
			id := resolvedKey.ID
			if configImportKeyIdentityProvisional(id, decision.keyCandidates) {
				resolvedID, ok := decisionKeyIDMap(decision)[id]
				if !ok || resolvedID == 0 {
					return fmt.Errorf("导入节点引用的 SSH 密钥临时身份未解析")
				}
				id = resolvedID
			}
			node.SSHKeyID = &id
		} else if candidate.reference.present {
			node.SSHKeyID = nil
		}
		if !candidate.existing {
			if node.SSHKeyID != nil {
				if err := tx.Model(&model.Node{}).Where("id = ?", node.ID).Update("ssh_key_id", *node.SSHKeyID).Error; err != nil {
					return fmt.Errorf("回填导入节点 SSH 密钥失败: %w", err)
				}
			}
			candidate.row = node
			continue
		}
		if err := tx.WithContext(tx.Statement.Context).Save(&node).Error; err != nil {
			return fmt.Errorf("保存导入节点失败: %w", err)
		}
		candidate.row = node
		accumulator.updated(configImportEntityNodes)
	}
	return nil
}

func propagateConfigImportRejectedNodeTasks(decision *configImportDecision, accumulator *configImportAccumulator) {
	if decision == nil || accumulator == nil {
		return
	}
	rejectedNodes := make(map[uint]struct{})
	for _, candidate := range decision.nodeCandidates {
		if candidate != nil && candidate.rejected && candidate.row.ID != 0 {
			rejectedNodes[candidate.row.ID] = struct{}{}
		}
	}
	if len(rejectedNodes) == 0 {
		return
	}
	for _, candidate := range decision.taskCandidates {
		if candidate == nil || candidate.rejected || candidate.pendingCode == "" {
			continue
		}
		if _, rejected := rejectedNodes[candidate.req.NodeID]; !rejected {
			continue
		}
		candidate.rejected = true
		candidate.action = configImportActionReject
		accumulator.rejected(configImportEntityTasks, candidate.taskIndex, candidate.name, configImportWarningInvalidInput)
		setConfigImportItemDecision(decision, configImportEntityTasks, candidate.taskIndex, candidate.name, configImportActionReject, "", false)
	}
}

func persistConfigImportPolicies(ctx context.Context, tx *gorm.DB, decision *configImportDecision, accumulator *configImportAccumulator) error {
	provisionalIDs := make(map[uint]struct{}, len(decision.policyCandidates))
	for _, candidate := range decision.policyCandidates {
		if candidate != nil && candidate.provisional != 0 {
			provisionalIDs[candidate.provisional] = struct{}{}
		}
	}
	for _, candidate := range decision.policyCandidates {
		if err := configImportContextErr(ctx); err != nil {
			return err
		}
		if candidate == nil || candidate.rejected || candidate.action == configImportActionSkip {
			continue
		}
		if !candidate.existing {
			provisional := candidate.provisional
			if provisional == 0 {
				return fmt.Errorf("导入策略临时身份不可用")
			}
			row := candidate.row
			row.ID = 0
			if err := model.CreatePolicyWithExplicitValues(tx.WithContext(ctx), &row, model.PolicyCreateExplicitColumns()...); err != nil {
				return fmt.Errorf("创建导入策略失败: %w", err)
			}
			candidate.row = row
			decision.snapshot.policiesByID[row.ID] = row
			decision.snapshot.policiesByName[candidate.name] = []model.Policy{row}
			decisionPolicyIDMap(decision)[provisional] = row.ID
			accumulator.created(configImportEntityPolicies, false)
			continue
		}
		originalID := candidate.row.ID
		var policyID uint
		if _, provisional := provisionalIDs[originalID]; provisional {
			resolved, ok := decisionPolicyIDMap(decision)[originalID]
			if !ok || resolved == 0 {
				return fmt.Errorf("导入策略临时身份未解析")
			}
			policyID = resolved
		} else {
			policyID = resolveConfigImportID(originalID, decisionPolicyIDMap(decision))
		}
		if policyID == 0 {
			return fmt.Errorf("导入策略身份不可用")
		}
		var row model.Policy
		if err := tx.WithContext(ctx).First(&row, policyID).Error; err != nil {
			return fmt.Errorf("读取导入策略失败: %w", err)
		}
		if err := applyImportedPolicyFields(&row, candidate.source); err != nil {
			accumulator.rejected(configImportEntityPolicies, candidate.index, candidate.name, configImportWarningInvalidInput)
			continue
		}
		if err := tx.WithContext(ctx).Save(&row).Error; err != nil {
			return fmt.Errorf("保存导入策略失败: %w", err)
		}
		if originalID != row.ID {
			decisionPolicyIDMap(decision)[originalID] = row.ID
		}
		candidate.row = row
		decision.snapshot.policiesByID[row.ID] = row
		decision.snapshot.policiesByName[candidate.name] = []model.Policy{row}
		accumulator.updated(configImportEntityPolicies)
	}
	return nil
}

func persistConfigImportTasks(ctx context.Context, tx *gorm.DB, decision *configImportDecision, accumulator *configImportAccumulator) error {
	if len(decision.taskCandidates) == 0 {
		return nil
	}
	nodeIDs := decisionNodeIDMap(decision)
	policyIDs := decisionPolicyIDMap(decision)
	for _, candidate := range decision.taskCandidates {
		if err := configImportContextErr(ctx); err != nil {
			return err
		}
		if candidate == nil || candidate.rejected {
			continue
		}
		if configImportIdentityProvisional(candidate.req.NodeID, decision.nodeCandidates) {
			resolved, ok := nodeIDs[candidate.req.NodeID]
			if !ok || resolved == 0 {
				candidate.rejected = true
				candidate.action = configImportActionReject
				accumulator.rejected(configImportEntityTasks, candidate.taskIndex, candidate.name, configImportWarningInvalidInput)
				continue
			}
			candidate.req.NodeID = resolved
		} else if !configImportInventoryHasNodeID(decision.snapshot.credentials, candidate.req.NodeID) {
			candidate.rejected = true
			candidate.action = configImportActionReject
			accumulator.rejected(configImportEntityTasks, candidate.taskIndex, candidate.name, configImportWarningInvalidInput)
			continue
		}
		if candidate.req.PolicyID != nil {
			policyID := *candidate.req.PolicyID
			if configImportIdentityProvisionalPolicy(policyID, decision.policyCandidates) {
				resolved, ok := policyIDs[policyID]
				if !ok || resolved == 0 {
					candidate.rejected = true
					candidate.action = configImportActionReject
					accumulator.rejected(configImportEntityTasks, candidate.taskIndex, candidate.name, configImportWarningInvalidInput)
					continue
				}
				candidate.req.PolicyID = &resolved
			} else if _, ok := decision.snapshot.policiesByID[policyID]; !ok {
				candidate.rejected = true
				candidate.action = configImportActionReject
				accumulator.rejected(configImportEntityTasks, candidate.taskIndex, candidate.name, configImportWarningInvalidInput)
				continue
			}
		}
		if candidate.dependencyID != nil {
			resolved := resolveConfigImportID(*candidate.dependencyID, decisionTaskIDMap(decision))
			candidate.dependencyID = &resolved
		}
		if candidate.pendingCode != "" {
			if err := materializeConfigImportTaskTarget(ctx, candidate, decision); err != nil {
				if taskPkg.IsTaskValidationError(err) {
					candidate.rejected = true
					candidate.action = configImportActionReject
					accumulator.rejected(configImportEntityTasks, candidate.taskIndex, candidate.name, configImportWarningInvalidInput)
					continue
				}
				return err
			}
			candidate.pendingCode = ""
			if candidate.action == configImportActionPending {
				candidate.action = configImportActionCreate
				if candidate.existing {
					candidate.action = configImportActionUpdate
				}
			}
		} else if err := materializeConfigImportTaskTarget(ctx, candidate, decision); err != nil {
			if taskPkg.IsTaskValidationError(err) {
				candidate.rejected = true
				candidate.action = configImportActionReject
				accumulator.rejected(configImportEntityTasks, candidate.taskIndex, candidate.name, configImportWarningInvalidInput)
				continue
			}
			return err
		}
		candidate.req.DependsOnTaskID = candidate.dependencyID
		candidate.task.NodeID = candidate.req.NodeID
		candidate.task.PolicyID = candidate.req.PolicyID
		candidate.task.Command = candidate.req.Command
		candidate.task.RsyncSource = candidate.req.RsyncSource
		candidate.task.RsyncTarget = candidate.req.RsyncTarget
		candidate.task.ExecutorType = candidate.req.ExecutorType
		candidate.task.ExecutorConfig = candidate.req.ExecutorConfig
		candidate.task.CronSpec = candidate.req.CronSpec
		// Dependency references are written only after every task has its
		// real database identity. Keep the edge in the overlay used by the
		// graph validator; candidate.task itself remains SQL-safe.
		candidate.task.DependsOnTaskID = nil
	}
	candidateRows := make(map[uint]model.Task, len(decision.taskCandidates))
	for _, candidate := range decision.taskCandidates {
		if err := configImportContextErr(ctx); err != nil {
			return err
		}
		if candidate != nil && !candidate.rejected {
			row := candidate.task
			row.DependsOnTaskID = candidate.dependencyID
			candidateRows[row.ID] = row
		}
	}
	if err := validateConfigImportTaskGraph(ctx, decision, candidateRows, accumulator); err != nil {
		return err
	}
	for _, candidate := range decision.taskCandidates {
		if err := configImportContextErr(ctx); err != nil {
			return err
		}
		if candidate == nil || candidate.rejected {
			continue
		}
		if candidate.existing {
			var row model.Task
			if err := tx.WithContext(ctx).First(&row, candidate.task.ID).Error; err != nil {
				return fmt.Errorf("读取导入任务失败: %w", err)
			}
			row.NodeID, row.PolicyID = candidate.task.NodeID, candidate.task.PolicyID
			row.DependsOnTaskID = nil
			row.Command, row.RsyncSource, row.RsyncTarget = candidate.task.Command, candidate.task.RsyncSource, candidate.task.RsyncTarget
			row.ExecutorType, row.ExecutorConfig, row.CronSpec = candidate.task.ExecutorType, candidate.task.ExecutorConfig, candidate.task.CronSpec
			row.CronOverride, row.Source = candidate.task.CronOverride, candidate.task.Source
			if candidate.managedRsync || candidate.managedRclone {
				row.Enabled = false
			} else if candidate.hasExplicitEnabled {
				row.Enabled = candidate.requestedEnabled
			}
			if err := applyImportedTaskCronCursor(tx, &row, candidate.previousCronSpec, row.CronSpec); err != nil {
				return err
			}
			if err := tx.WithContext(ctx).Save(&row).Error; err != nil {
				return fmt.Errorf("保存导入任务失败: %w", err)
			}
			candidate.task = row
			decisionTaskIDMap(decision)[candidate.task.ID] = candidate.task.ID
			accumulator.updated(configImportEntityTasks)
			continue
		}
		row := candidate.task
		provisional := candidate.provisional
		if provisional == 0 {
			return fmt.Errorf("导入任务临时身份不可用")
		}
		row.ID = 0
		if row.Enabled {
			row.NextRunAt = cronutil.Next(row.CronSpec)
		}
		requestedEnabled := row.Enabled
		if err := tx.WithContext(ctx).Create(&row).Error; err != nil {
			return fmt.Errorf("创建导入任务失败: %w", err)
		}
		if candidate.hasExplicitEnabled || candidate.managedRsync || candidate.managedRclone {
			if err := tx.WithContext(ctx).Model(&model.Task{}).Where("id = ?", row.ID).Update("enabled", requestedEnabled).Error; err != nil {
				return fmt.Errorf("恢复导入任务启用状态失败: %w", err)
			}
		}
		candidate.task = row
		decisionTaskIDMap(decision)[provisional] = row.ID
		accumulator.created(configImportEntityTasks, false)
	}
	for _, candidate := range decision.taskCandidates {
		if err := configImportContextErr(ctx); err != nil {
			return err
		}
		if candidate == nil || candidate.rejected || candidate.dependencyID == nil {
			continue
		}
		dependencyID := *candidate.dependencyID
		resolved := dependencyID
		if configImportTaskIdentityProvisional(dependencyID, decision.taskCandidates) {
			var ok bool
			resolved, ok = decisionTaskIDMap(decision)[dependencyID]
			if !ok || resolved == 0 {
				return fmt.Errorf("导入任务依赖的临时身份未解析")
			}
		}
		if err := tx.WithContext(ctx).Model(&model.Task{}).Where("id = ?", candidate.task.ID).Update("depends_on_task_id", resolved).Error; err != nil {
			return fmt.Errorf("更新导入任务依赖失败: %w", err)
		}
	}
	return nil
}

func materializeConfigImportTaskTarget(ctx context.Context, candidate *configImportTaskCandidate, decision *configImportDecision) error {
	if candidate == nil {
		return fmt.Errorf("config import task candidate is unavailable")
	}
	if candidate.managedRsync || candidate.managedRclone || candidate.req.ExecutorType == "command" {
		if candidate.managedRsync {
			candidate.req.RsyncSource, candidate.req.RsyncTarget = "", ""
			candidate.req.ExecutorConfig = canonicalLegacyRsyncImportConfig()
		} else if candidate.managedRclone {
			candidate.req.RsyncTarget = ""
			candidate.req.ExecutorConfig = canonicalLegacyRcloneImportConfig()
		}
		return validateConfigImportTaskCandidateRequest(candidate)
	}
	if strings.TrimSpace(candidate.req.RsyncTarget) == "" {
		if candidate.req.PolicyID != nil {
			policy, ok := decision.snapshot.policiesByID[*candidate.req.PolicyID]
			for _, p := range decision.policyCandidates {
				if p != nil && p.row.ID == *candidate.req.PolicyID {
					policy, ok = p.row, true
				}
			}
			if ok {
				candidate.req.RsyncTarget = policyPkg.PolicyNodeTargetPath(policy.TargetPath, *candidate.req.PolicyID, candidate.req.NodeID)
			}
		} else {
			taskPkg.AutoGenerateTarget(ctx, nil, &candidate.req)
		}
	}
	return validateConfigImportTaskCandidateRequest(candidate)
}

func validateConfigImportTaskCandidateRequest(candidate *configImportTaskCandidate) error {
	if candidate == nil {
		return fmt.Errorf("config import task candidate is unavailable")
	}
	switch {
	case candidate.managedRsync:
		return taskPkg.ValidateDisconnectedImportedRsyncTask(candidate.req)
	case candidate.managedRclone:
		return taskPkg.ValidateDisconnectedImportedRcloneTask(candidate.req)
	default:
		return taskPkg.ValidateTaskInput(candidate.req)
	}
}

func validateConfigImportTarget(req taskPkg.CreateTaskInput, taskID uint, claims *[]policyPkg.TargetOwner) error {
	target := strings.TrimSpace(req.RsyncTarget)
	if target == "" || !filepath.IsAbs(target) {
		return nil
	}
	owner := policyPkg.TargetOwner{NodeID: req.NodeID, TaskID: taskID, Target: target}
	if req.PolicyID != nil {
		owner.PolicyID = *req.PolicyID
	}
	if _, err := policyPkg.ValidateTargetOwnership(target, owner, *claims); err != nil {
		return fmt.Errorf("%w: 导入任务 %q 的备份目标存在重叠或归属不明: %w", errConfigImportTargetConflict, req.Name, err)
	}
	*claims = append(*claims, owner)
	return nil
}
func validateConfigImportNewTaskTarget(candidate *configImportTaskCandidate, claims *[]policyPkg.TargetOwner) error {
	if candidate == nil || candidate.existing {
		return nil
	}
	return validateConfigImportTarget(candidate.req, 0, claims)
}

func resolveConfigImportPolicyID(data map[string]interface{}, byName map[string][]model.Policy, byID map[uint]model.Policy) (uint, bool, bool) {
	if importTaskFieldSpecified(data, "policy_name") {
		name := strings.TrimSpace(readStringField(data, "policy_name"))
		rows := byName[name]
		if len(rows) == 1 {
			return rows[0].ID, true, true
		}
		return 0, false, true
	}
	if importTaskFieldSpecified(data, "policy_id") {
		if id, valid := normalizeUintValue(data["policy_id"]); valid {
			if _, exists := byID[id]; exists {
				return id, true, true
			}
		}
		return 0, false, true
	}
	return 0, false, false
}

func resolveConfigImportPolicyRow(id uint, byName map[string][]model.Policy, byID map[uint]model.Policy) (model.Policy, bool) {
	if row, ok := byID[id]; ok {
		return row, true
	}
	for _, rows := range byName {
		for _, row := range rows {
			if row.ID == id {
				return row, true
			}
		}
	}
	return model.Policy{}, false
}

func resolveConfigImportDependencyKey(data map[string]interface{}, nodesByName map[string][]model.Node, ownNodeID uint) (importTaskKey, bool) {
	name := readStringField(data, "depends_on_task_name")
	if name == "" {
		return importTaskKey{}, false
	}
	if nodeName := readStringField(data, "depends_on_task_node_name"); nodeName != "" {
		rows := nodesByName[nodeName]
		if len(rows) != 1 {
			return importTaskKey{}, false
		}
		return buildImportTaskKey(name, rows[0].ID), true
	}
	if ownNodeID != 0 {
		return buildImportTaskKey(name, ownNodeID), true
	}
	return importTaskKey{}, false
}
func configImportNodePending(decision *configImportDecision, id uint) bool {
	if decision == nil {
		return false
	}
	for _, candidate := range decision.nodeCandidates {
		if candidate != nil && !candidate.rejected && candidate.row.ID == id && candidate.action == configImportActionPending {
			return true
		}
	}
	return false
}
func configImportKeyPending(decision *configImportDecision, id uint) bool {
	if decision == nil {
		return false
	}
	for _, candidate := range decision.keyCandidates {
		if candidate != nil && !candidate.rejected && candidate.row.ID == id && candidate.action == configImportActionPending {
			return true
		}
	}
	return false
}
func configImportInventoryHasNodeID(inventory *configImportCredentialInventory, id uint) bool {
	if inventory == nil || id == 0 {
		return false
	}
	if inventory.nodesByID != nil {
		_, exists := inventory.nodesByID[id]
		return exists
	}
	for _, row := range inventory.nodes {
		if row.ID == id {
			return true
		}
	}
	return false
}

func configImportIdentityProvisional(id uint, candidates []*configImportNodeCandidate) bool {
	for _, candidate := range candidates {
		if candidate != nil && candidate.provisional != 0 && candidate.provisional == id {
			return true
		}
	}
	return false
}

func configImportIdentityProvisionalPolicy(id uint, candidates []*configImportPolicyCandidate) bool {
	for _, candidate := range candidates {
		if candidate != nil && candidate.provisional != 0 && candidate.provisional == id {
			return true
		}
	}
	return false
}

func configImportKeyIdentityProvisional(id uint, candidates []*configImportSSHKeyCandidate) bool {
	for _, candidate := range candidates {
		if candidate != nil && candidate.provisional != 0 && candidate.provisional == id {
			return true
		}
	}
	return false
}

func configImportTaskIdentityProvisional(id uint, candidates []*configImportTaskCandidate) bool {
	for _, candidate := range candidates {
		if candidate != nil && candidate.provisional != 0 && candidate.provisional == id {
			return true
		}
	}
	return false
}

func configImportProvisionalNodeIDs(candidates []*configImportNodeCandidate) map[uint]struct{} {
	ids := make(map[uint]struct{})
	for _, candidate := range candidates {
		if candidate != nil && candidate.provisional != 0 {
			ids[candidate.provisional] = struct{}{}
		}
	}
	return ids
}

func importedConfigSecretString(data map[string]interface{}, field string) (string, bool) {
	raw, ok := data[field]
	if !ok || raw == nil {
		return "", false
	}
	value, valid := raw.(string)
	if !valid {
		return "", true
	}
	return strings.TrimSpace(value), false
}

func importedConfigSecretPresent(data map[string]interface{}, field string) bool {
	value, invalid := importedConfigSecretString(data, field)
	return !invalid && value != ""
}

func addImportedNodeCredentialWarningsWithPresence(accumulator *configImportAccumulator, index int, name string, source map[string]interface{}, nodeItem model.Node, passwordPresent, privateKeyPresent bool) {
	if importedNodeKeyReferenceUnresolved(source, nodeItem) {
		accumulator.warning(configImportEntityNodes, index, name, configImportWarningUnresolvedSSHKey)
	}
	switch strings.ToLower(strings.TrimSpace(nodeItem.AuthType)) {
	case "password":
		if !passwordPresent {
			accumulator.warning(configImportEntityNodes, index, name, configImportWarningMissingPassword)
		}
	case "key", "ssh_key":
		if nodeItem.SSHKeyID == nil && !privateKeyPresent {
			accumulator.warning(configImportEntityNodes, index, name, configImportWarningMissingInlineKey)
		}
	}
}

func appendPendingCheck(checks []configImportPendingCheck, entity string, index int, code string) []configImportPendingCheck {
	return append(checks, configImportPendingCheck{Entity: entity, Index: index, Code: code})
}
func pruneConfigImportPendingChecks(ctx context.Context, decision *configImportDecision) error {
	if decision == nil || len(decision.pendingChecks) == 0 {
		return nil
	}
	pruned := make([]configImportPendingCheck, 0, len(decision.pendingChecks))
	seen := make(map[configImportPendingCheck]struct{}, len(decision.pendingChecks))
	for _, check := range decision.pendingChecks {
		if err := configImportContextErr(ctx); err != nil {
			return err
		}
		items := decision.itemDecisions[check.Entity]
		if check.Index < 0 || check.Index >= len(items) {
			continue
		}
		item := items[check.Index]
		if item.Action != configImportActionPending || item.PendingCode != check.Code {
			continue
		}
		if _, exists := seen[check]; exists {
			continue
		}
		seen[check] = struct{}{}
		pruned = append(pruned, check)
	}
	decision.pendingChecks = pruned
	return nil
}

func cloneStringMap(input map[string]string) map[string]string {
	if input == nil {
		return nil
	}
	output := make(map[string]string, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

type configImportIdentityAllocator struct {
	used map[uint]struct{}
	next uint
}

func newConfigImportIdentityAllocator(used map[uint]struct{}) *configImportIdentityAllocator {
	copySet := make(map[uint]struct{}, len(used))
	for id := range used {
		copySet[id] = struct{}{}
	}
	return &configImportIdentityAllocator{used: copySet, next: ^uint(0)}
}

func (a *configImportIdentityAllocator) NextContext(ctx context.Context) (uint, error) {
	if a == nil {
		return 0, fmt.Errorf("config import identity allocator is unavailable")
	}
	for a.next != 0 {
		if err := configImportContextErr(ctx); err != nil {
			return 0, err
		}
		id := a.next
		a.next--
		if _, exists := a.used[id]; exists {
			continue
		}
		a.used[id] = struct{}{}
		return id, nil
	}
	return 0, fmt.Errorf("config import identity space exhausted")
}

func (inventory *configImportCredentialInventory) nodeIDsContext(ctx context.Context) (map[uint]struct{}, error) {
	if inventory == nil {
		return nil, nil
	}
	ids := make(map[uint]struct{}, len(inventory.nodes))
	for _, row := range inventory.nodes {
		if err := configImportContextErr(ctx); err != nil {
			return nil, err
		}
		ids[row.ID] = struct{}{}
	}
	return ids, nil
}

func (inventory *configImportCredentialInventory) keyIDsContext(ctx context.Context) (map[uint]struct{}, error) {
	if inventory == nil {
		return nil, nil
	}
	ids := make(map[uint]struct{}, len(inventory.keys))
	for _, row := range inventory.keys {
		if err := configImportContextErr(ctx); err != nil {
			return nil, err
		}
		ids[row.ID] = struct{}{}
	}
	return ids, nil
}

func configImportPolicyIDsContext(ctx context.Context, snapshot configImportSnapshot) (map[uint]struct{}, error) {
	ids := make(map[uint]struct{}, len(snapshot.policies))
	for _, row := range snapshot.policies {
		if err := configImportContextErr(ctx); err != nil {
			return nil, err
		}
		ids[row.ID] = struct{}{}
	}
	return ids, nil
}

func configImportTaskIDsContext(ctx context.Context, snapshot configImportSnapshot) (map[uint]struct{}, error) {
	ids := make(map[uint]struct{}, len(snapshot.tasks))
	for _, row := range snapshot.tasks {
		if err := configImportContextErr(ctx); err != nil {
			return nil, err
		}
		ids[row.ID] = struct{}{}
	}
	return ids, nil
}

func decisionNodeIDMap(decision *configImportDecision) map[uint]uint {
	if decision.nodeIDMap == nil {
		decision.nodeIDMap = make(map[uint]uint)
	}
	return decision.nodeIDMap
}

func decisionKeyIDMap(decision *configImportDecision) map[uint]uint {
	if decision.keyIDMap == nil {
		decision.keyIDMap = make(map[uint]uint)
	}
	return decision.keyIDMap
}

func decisionPolicyIDMap(decision *configImportDecision) map[uint]uint {
	if decision.policyIDMap == nil {
		decision.policyIDMap = make(map[uint]uint)
	}
	return decision.policyIDMap
}

func decisionTaskIDMap(decision *configImportDecision) map[uint]uint {
	if decision.taskIDMap == nil {
		decision.taskIDMap = make(map[uint]uint)
	}
	return decision.taskIDMap
}

func resolveConfigImportID(id uint, mapping map[uint]uint) uint {
	if resolved, ok := mapping[id]; ok {
		return resolved
	}
	return id
}

func remapConfigImportSSHKeyScope(candidate *configImportSSHKeyCandidate, mapping map[uint]uint, provisionalIDs map[uint]struct{}) error {
	if candidate == nil || candidate.legacyScope.allowedNodeIDs == nil {
		return nil
	}
	raw := strings.TrimSpace(*candidate.legacyScope.allowedNodeIDs)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	for index, part := range parts {
		id, ok := normalizeUintValue(strings.TrimSpace(part))
		if !ok {
			continue
		}
		if _, provisional := provisionalIDs[id]; provisional {
			resolved, exists := mapping[id]
			if !exists || resolved == 0 {
				return fmt.Errorf("导入 SSH 密钥范围引用的节点临时身份未解析")
			}
			parts[index] = fmt.Sprintf("%d", resolved)
			continue
		}
		if resolved, exists := mapping[id]; exists {
			parts[index] = fmt.Sprintf("%d", resolved)
		}
	}
	joined := strings.Join(parts, ",")
	candidate.legacyScope.allowedNodeIDs = &joined
	return nil
}

func lockConfigImportPolicyTaskInventory(tx *gorm.DB, input configImportInput) error {
	if tx == nil {
		return fmt.Errorf("config import policy/task lock transaction is unavailable")
	}
	if len(input.envelope.Classic.Policies) == 0 && len(input.envelope.Classic.Tasks) == 0 {
		return nil
	}
	query := tx.Session(&gorm.Session{SkipHooks: true})
	if !strings.EqualFold(tx.Name(), "sqlite") {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if len(input.envelope.Classic.Policies) > 0 || len(input.envelope.Classic.Tasks) > 0 {
		var policyRows []model.Policy
		if err := query.Session(&gorm.Session{}).Model(&model.Policy{}).Select("id").Order("id").Find(&policyRows).Error; err != nil {
			return fmt.Errorf("锁定导入策略失败: %w", err)
		}
	}
	if len(input.envelope.Classic.Tasks) > 0 {
		var taskRows []model.Task
		if err := query.Session(&gorm.Session{}).Model(&model.Task{}).Select("id").Order("id").Find(&taskRows).Error; err != nil {
			return fmt.Errorf("锁定导入任务依赖失败: %w", err)
		}
	}
	return nil
}

// Import godoc
// @Summary      导入配置
// @Tags         config
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        conflict  query     string  false  "冲突策略（skip 默认/overwrite）"
// @Param        body      body      object  true   "配置 JSON 数据"
// @Success      200  {object}  handlers.Response{data=configImportResult}
// @Failure      400  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Router       /config/import [post]
func (h *ConfigHandler) Import(c *gin.Context) {
	conflict := c.DefaultQuery("conflict", "skip")
	if conflict != "skip" && conflict != "overwrite" {
		respondBadRequest(c, "conflict 参数仅支持 skip 或 overwrite")
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 10<<20)
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			respondBadRequest(c, "导入文件超过 10MB 限制")
			return
		}
		respondBadRequest(c, "读取导入数据失败")
		return
	}
	input, err := prepareConfigImportInput(c.Request.Context(), h, body, conflict)
	if err != nil {
		logger.Module("config").Warn().Msg("配置导入数据校验失败")
		if errors.Is(err, errConfigAssetGraphInvalid) {
			respondBadRequest(c, "导入数据无效")
		} else if strings.Contains(err.Error(), "system-setting") || strings.Contains(err.Error(), "系统设置") {
			respondBadRequest(c, "导入系统设置无效")
		} else {
			respondBadRequest(c, "无效的导入数据")
		}
		return
	}
	accumulator := newConfigImportAccumulator()
	rollbackJournal := newConfigImportRollbackJournal(h.db, h.settingsSvc)
	persistImport := func(persistCtx context.Context) error {
		var rollbackSnapshot *configImportRollbackSnapshot
		err := h.db.WithContext(persistCtx).Transaction(func(tx *gorm.DB) error {
			if len(input.envelope.Classic.Tasks) > 0 {
				if err := policyPkg.LockTargetOwnershipSpace(tx); err != nil {
					return fmt.Errorf("锁定导入任务备份目标失败: %w", err)
				}
			}
			var lockedInventory *configImportCredentialInventory
			if len(input.envelope.Classic.Nodes) > 0 || len(input.envelope.Classic.SSHKeys) > 0 || len(input.envelope.Classic.Tasks) > 0 {
				lockedInventory, err = lockConfigImportCredentialInventory(tx, input.envelope.Classic)
				if err != nil {
					return err
				}
			}
			if err := lockConfigImportPolicyTaskInventory(tx, input); err != nil {
				return err
			}
			decisionInput := input
			decisionInput.lockedCredentials = lockedInventory
			snapshot, err := loadConfigImportSnapshot(persistCtx, tx, decisionInput)
			if err != nil {
				return err
			}
			decision, err := evaluateConfigImport(persistCtx, decisionInput, snapshot)
			if err != nil {
				return err
			}
			rollbackSnapshot, err = captureConfigImportRollbackSnapshot(persistCtx, tx, input.envelope.Classic, input.settingsPlan, input.envelope)
			if err != nil {
				return err
			}
			if err := persistConfigImportDecision(persistCtx, tx, &decision, accumulator); err != nil {
				return err
			}
			if input.envelope.Version == configExportVersion2 {
				if err := h.importBackupAssetGraph(tx, input.envelope, configImportActorID(c)); err != nil {
					return err
				}
			}
			rollbackSnapshot.nodes.registerOwnedCreated(accumulator.createdNodeIDs)
			rollbackSnapshot.sshKeys.registerOwnedCreated(accumulator.createdSSHKeyIDs)
			return rollbackSnapshot.seal(persistCtx, tx)
		})
		if err == nil {
			rollbackJournal.install(rollbackSnapshot)
		}
		return err
	}
	importErr := h.persistConfigImport(c.Request.Context(), input.foundationSettings, persistImport, rollbackJournal.Restore)
	if importErr != nil {
		if errors.Is(importErr, errConfigImportTargetConflict) {
			respondBadRequest(c, "导入任务的备份目标存在重叠或归属不明")
			return
		}
		if errors.Is(importErr, errConfigAssetGraphConflict) {
			respondConflict(c, "导入的备份资产图与本地身份冲突")
			return
		}
		if respondBackupAssetEnablementConflict(c, importErr) {
			return
		}
		if errors.Is(importErr, errConfigAssetGraphInvalid) {
			respondBadRequest(c, "导入数据无效")
			return
		}
		respondInternalError(c, importErr)
		return
	}
	result := accumulator.finalize()
	writeCredentialAuditFromGin(c, h.db, credentialaudit.Event{
		Action: "config.import", Purpose: "config_import", CredentialKind: "system_import", CredentialSource: "settings.import",
		Outcome: credentialaudit.OutcomeSuccess,
		Metadata: map[string]any{
			"stage": "success", "node_count": result.Nodes, "key_count": result.SSHKeys,
			"policy_count": result.Policies, "task_count": result.Tasks, "settings_count": result.SystemSettings,
			"created_count": result.Created, "updated_count": result.Updated,
			"rejected_count": result.Rejected, "skipped_count": result.Skipped,
		},
	})
	respondOK(c, result)
}
