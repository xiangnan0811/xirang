package automation

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"xirang/backend/internal/logger"
	"xirang/backend/internal/model"
	"xirang/backend/internal/policy"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type TaskTriggerer interface {
	TriggerAutomation(taskID uint) (uint, error)
}

// TransactionalTaskTriggerer reserves an automation TaskRun in the caller's
// transaction. The durable effect row is the idempotency marker; effect keys
// are not part of this reservation API.
type TransactionalTaskTriggerer interface {
	ReserveAutomationRunTx(context.Context, *gorm.DB, uint) (uint, error)
}

type PolicyController interface {
	PausePolicyNext(ctx context.Context, policyID uint) error
	DisablePolicy(ctx context.Context, policyID uint) error
}

// TransactionalPolicyController applies policy controls without committing or
// touching a process-local scheduler. The caller owns the outer transaction.
type TransactionalPolicyController interface {
	PausePolicyNextTx(context.Context, *gorm.DB, uint) error
	DisablePolicyTx(context.Context, *gorm.DB, uint) ([]uint, error)
}

// PolicyScheduleReconciler runs only after a durable policy transaction has
// committed. Disable uses the task IDs returned by DisablePolicyTx.
type PolicyScheduleReconciler interface {
	ReconcilePolicySchedules(uint, []uint) error
}

// Dispatcher matches events to enabled rules and executes their actions.
type Dispatcher struct {
	db               *gorm.DB
	triggerer        TaskTriggerer
	policyController PolicyController
}

// NewDispatcher creates a Dispatcher with the given DB.
func NewDispatcher(db *gorm.DB) *Dispatcher {
	return &Dispatcher{db: db}
}

func (d *Dispatcher) SetTaskTriggerer(triggerer TaskTriggerer) {
	d.triggerer = triggerer
}

func (d *Dispatcher) SetPolicyController(controller PolicyController) {
	d.policyController = controller
}

// Dispatch finds matching enabled rules for the event and executes their actions.
// Execution logs are persisted per rule. Errors are returned to the caller;
// an action or log failure is never acknowledged as a successful dispatch.
func (d *Dispatcher) Dispatch(ctx context.Context, event Event) error {
	if d == nil || d.db == nil {
		return fmt.Errorf("automation dispatch unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	var rules []model.AutomationRule
	if err := d.db.WithContext(ctx).Where("event_type = ? AND enabled = ?", event.Type, true).Find(&rules).Error; err != nil {
		return fmt.Errorf("automation dispatch: query rules: %w", err)
	}

	var firstErr error
	for _, rule := range rules {
		if !matchFilter(rule.EventFilter, event.Context) {
			continue
		}
		logEntry := d.executeAction(ctx, rule, event)
		if err := d.db.WithContext(ctx).Create(&logEntry).Error; err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("automation dispatch: persist rule %d execution log: %w", rule.ID, err)
			}
			continue
		}
		if logEntry.Result != ResultSuccess && firstErr == nil {
			firstErr = fmt.Errorf("automation rule %d failed: %s", rule.ID, logEntry.Error)
		}
	}
	return firstErr
}

type taskRunAutomationEffect struct {
	Rule  model.AutomationRule `json:"rule"`
	Event Event                `json:"event"`
}

// DispatchTaskRunEffect consumes one durable automation parent or per-rule
// child effect. The child path commits action, log, and effect acknowledgement
// together so a crash or log failure cannot replay a successful action.
func (d *Dispatcher) DispatchTaskRunEffect(ctx context.Context, event Event, effect model.TaskRunEffect) error {
	if d == nil || d.db == nil {
		return fmt.Errorf("automation durable dispatch unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	switch effect.EffectType {
	case model.TaskRunEffectTypeAutomation:
		return d.fanoutTaskRunAutomationEffect(ctx, event, effect)
	case model.TaskRunEffectTypeAutomationRule:
		return d.executeTaskRunAutomationRuleEffect(ctx, effect)
	default:
		return fmt.Errorf("unsupported automation effect type %q", effect.EffectType)
	}
}

func markTaskRunEffectSucceededTx(tx *gorm.DB, effectID uint, claimedBy string) error {
	if tx == nil || effectID == 0 || strings.TrimSpace(claimedBy) == "" {
		return fmt.Errorf("automation effect success transition unavailable")
	}
	now := time.Now().UTC()
	result := tx.Model(&model.TaskRunEffect{}).
		Where("id = ? AND status = ? AND claimed_by = ?", effectID, model.TaskRunEffectStatusRunning, claimedBy).
		Updates(map[string]interface{}{
			"status":            model.TaskRunEffectStatusSucceeded,
			"claimed_by":        "",
			"claim_lease_until": nil,
			"last_error":        "",
			"updated_at":        now,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("automation effect %d claim lost", effectID)
	}
	return nil
}

func (d *Dispatcher) fanoutTaskRunAutomationEffect(ctx context.Context, event Event, effect model.TaskRunEffect) error {
	return d.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current model.TaskRunEffect
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", effect.ID).Limit(1).Find(&current)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		if current.Status == model.TaskRunEffectStatusSucceeded {
			return nil
		}
		if current.Status != model.TaskRunEffectStatusRunning || current.ClaimedBy != effect.ClaimedBy {
			return fmt.Errorf("automation parent effect %d claim lost", effect.ID)
		}

		var payload automationTaskRunEventPayload
		if err := json.Unmarshal([]byte(current.Payload), &payload); err != nil {
			return fmt.Errorf("decode automation effect: %w", err)
		}
		if payload.Context == nil {
			payload.Context = make(map[string]interface{})
		}
		event = Event{Type: payload.EventType, Context: payload.Context}
		var rules []model.AutomationRule
		if err := tx.Where("event_type = ? AND enabled = ?", event.Type, true).Find(&rules).Error; err != nil {
			return fmt.Errorf("automation durable dispatch: query rules: %w", err)
		}
		for _, rule := range rules {
			if !matchFilter(rule.EventFilter, event.Context) {
				continue
			}
			childPayload, err := json.Marshal(taskRunAutomationEffect{Rule: rule, Event: event})
			if err != nil {
				return fmt.Errorf("encode automation rule %d effect: %w", rule.ID, err)
			}
			child := model.TaskRunEffect{
				TaskRunID:  current.TaskRunID,
				EffectKey:  fmt.Sprintf("%s:rule:%d", current.EffectKey, rule.ID),
				EffectType: model.TaskRunEffectTypeAutomationRule,
				Payload:    string(childPayload),
				Status:     model.TaskRunEffectStatusPending,
			}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&child).Error; err != nil {
				return fmt.Errorf("persist automation rule %d effect: %w", rule.ID, err)
			}
		}
		return markTaskRunEffectSucceededTx(tx, current.ID, current.ClaimedBy)
	})
}

type automationTaskRunEventPayload struct {
	EventType string                 `json:"event_type"`
	Context   map[string]interface{} `json:"context"`
}

func (d *Dispatcher) executeTaskRunAutomationRuleEffect(ctx context.Context, effect model.TaskRunEffect) error {
	var payload taskRunAutomationEffect
	if err := json.Unmarshal([]byte(effect.Payload), &payload); err != nil {
		return fmt.Errorf("decode automation rule effect: %w", err)
	}
	if payload.Rule.ID == 0 {
		return fmt.Errorf("automation rule effect has no rule id")
	}
	var postCommit []func() error
	if err := d.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current model.TaskRunEffect
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", effect.ID).Limit(1).Find(&current)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		if current.Status == model.TaskRunEffectStatusSucceeded {
			return nil
		}
		if current.Status != model.TaskRunEffectStatusRunning || current.ClaimedBy != effect.ClaimedBy {
			return fmt.Errorf("automation rule effect %d claim lost", effect.ID)
		}
		action, err := d.executeActionTx(ctx, tx, payload.Rule, payload.Event)
		if err != nil {
			return err
		}
		if err := tx.Create(&action.log).Error; err != nil {
			return fmt.Errorf("automation rule %d: persist execution log: %w", payload.Rule.ID, err)
		}
		postCommit = action.postCommit
		return markTaskRunEffectSucceededTx(tx, current.ID, current.ClaimedBy)
	}); err != nil {
		return err
	}
	for _, callback := range postCommit {
		if callback == nil {
			continue
		}
		if err := callback(); err != nil {
			logger.Module("automation").Warn().Uint("rule_id", payload.Rule.ID).Err(err).
				Msg("automation rule post-commit reconciliation failed")
		}
	}
	return nil
}

// matchFilter parses the filter JSON and checks that every key matches the
// corresponding value in ctx. This is a simple AND match.
func matchFilter(filterJSON string, ctx map[string]interface{}) bool {
	if strings.TrimSpace(filterJSON) == "" || filterJSON == "{}" {
		return true
	}

	var filter map[string]interface{}
	if err := json.Unmarshal([]byte(filterJSON), &filter); err != nil {
		return false
	}
	if len(filter) == 0 {
		return true
	}

	for key, filterVal := range filter {
		ctxVal, ok := ctx[key]
		if !ok {
			return false
		}
		if !valueMatches(filterVal, ctxVal) {
			return false
		}
	}
	return true
}

// valueMatches checks if two values are equal. Both are interface{} so we need
// to handle numeric types flexibly: a JSON number like 1 unmarshals as float64
// while context may have int/uint.
func valueMatches(filterVal, ctxVal interface{}) bool {
	if filterVal == nil || ctxVal == nil {
		return filterVal == ctxVal
	}

	// Fast path: direct equality
	if filterVal == ctxVal {
		return true
	}

	// String comparison
	fs, fOk := filterVal.(string)
	cs, cOk := ctxVal.(string)
	if fOk && cOk {
		return fs == cs
	}

	// Try numeric comparison: convert both to float64
	fNum, fIsNum := toFloat64(filterVal)
	cNum, cIsNum := toFloat64(ctxVal)
	if fIsNum && cIsNum {
		return fNum == cNum
	}

	// Fallback: string representation
	return fmt.Sprint(filterVal) == fmt.Sprint(ctxVal)
}

func toFloat64(v interface{}) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint:
		return float64(n), true
	case uint64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

// executeAction runs the action specified by the rule and returns a log entry.
func (d *Dispatcher) executeAction(ctx context.Context, rule model.AutomationRule, event Event) model.AutomationRuleLog {
	logEntry := model.AutomationRuleLog{
		RuleID:     rule.ID,
		EventType:  event.Type,
		ActionType: rule.ActionType,
	}

	switch rule.ActionType {
	case ActionPausePolicy:
		err := d.execPausePolicy(ctx, rule.ActionConfig, event.Context)
		logEntry.Result = resultFrom(err)
		if err != nil {
			logEntry.Error = err.Error()
		}

	case ActionDisablePolicy:
		err := d.execDisablePolicy(ctx, rule.ActionConfig, event.Context)
		logEntry.Result = resultFrom(err)
		if err != nil {
			logEntry.Error = err.Error()
		}

	case ActionTriggerTask:
		details, err := d.execTriggerTask(ctx, rule.ActionConfig, event.Context)
		logEntry.Result = resultFrom(err)
		if err != nil {
			logEntry.Error = err.Error()
		} else if details != "" {
			logEntry.Details = details
		}

	case ActionSendNotification:
		details := d.execSendNotification(rule.ActionConfig, event.Context)
		logEntry.Result = ResultSuccess
		logEntry.Details = details

	default:
		logEntry.Result = ResultError
		logEntry.Error = fmt.Sprintf("未知动作类型: %s", rule.ActionType)
	}

	return logEntry
}

type transactionalAction struct {
	log        model.AutomationRuleLog
	postCommit []func() error
}

func (d *Dispatcher) executeActionTx(
	ctx context.Context,
	tx *gorm.DB,
	rule model.AutomationRule,
	event Event,
) (transactionalAction, error) {
	action := transactionalAction{log: model.AutomationRuleLog{
		RuleID:     rule.ID,
		EventType:  event.Type,
		ActionType: rule.ActionType,
	}}
	switch rule.ActionType {
	case ActionPausePolicy:
		if err := d.execPausePolicyTx(ctx, tx, rule.ActionConfig, event.Context); err != nil {
			action.log.Result = ResultError
			action.log.Error = err.Error()
			return action, err
		}
		action.log.Result = ResultSuccess
	case ActionDisablePolicy:
		policyID, taskIDs, err := d.execDisablePolicyTx(ctx, tx, rule.ActionConfig, event.Context)
		if err != nil {
			action.log.Result = ResultError
			action.log.Error = err.Error()
			return action, err
		}
		action.log.Result = ResultSuccess
		if reconciler, ok := d.policyController.(PolicyScheduleReconciler); ok && len(taskIDs) > 0 {
			reconcileIDs := append([]uint(nil), taskIDs...)
			action.postCommit = append(action.postCommit, func() error {
				return reconciler.ReconcilePolicySchedules(policyID, reconcileIDs)
			})
		}
	case ActionTriggerTask:
		details, err := d.execTriggerTaskTx(ctx, tx, rule.ActionConfig, event.Context)
		if err != nil {
			action.log.Result = ResultError
			action.log.Error = err.Error()
			return action, err
		}
		action.log.Result = ResultSuccess
		action.log.Details = details
	case ActionSendNotification:
		action.log.Result = ResultSuccess
		action.log.Details = d.execSendNotification(rule.ActionConfig, event.Context)
	default:
		err := fmt.Errorf("未知动作类型: %s", rule.ActionType)
		action.log.Result = ResultError
		action.log.Error = err.Error()
		return action, err
	}
	return action, nil
}

func resultFrom(err error) string {
	if err != nil {
		return ResultError
	}
	return ResultSuccess
}

func (d *Dispatcher) execPausePolicyTx(
	ctx context.Context,
	tx *gorm.DB,
	actionConfig string,
	evtCtx map[string]interface{},
) error {
	cfg := renderConfig(actionConfig, evtCtx)
	policyID := parseConfigUint(cfg, "policy_id")
	if policyID == 0 {
		return fmt.Errorf("pause_policy: policy_id 缺失或解析失败 (config=%s)", actionConfig)
	}
	if d.policyController != nil {
		controller, ok := d.policyController.(TransactionalPolicyController)
		if !ok {
			return fmt.Errorf("pause_policy: policy controller lacks transactional control")
		}
		if err := controller.PausePolicyNextTx(ctx, tx, policyID); err != nil {
			return fmt.Errorf("pause_policy: %w", err)
		}
	} else if err := policy.NewControlService(d.db, nil).PauseNextTx(ctx, tx, policyID); err != nil {
		return fmt.Errorf("pause_policy: %w", err)
	}
	return nil
}

func (d *Dispatcher) execDisablePolicyTx(
	ctx context.Context,
	tx *gorm.DB,
	actionConfig string,
	evtCtx map[string]interface{},
) (uint, []uint, error) {
	cfg := renderConfig(actionConfig, evtCtx)
	policyID := parseConfigUint(cfg, "policy_id")
	if policyID == 0 {
		return 0, nil, fmt.Errorf("disable_policy: policy_id 缺失或解析失败 (config=%s)", actionConfig)
	}
	if d.policyController != nil {
		controller, ok := d.policyController.(TransactionalPolicyController)
		if !ok {
			return 0, nil, fmt.Errorf("disable_policy: policy controller lacks transactional control")
		}
		taskIDs, err := controller.DisablePolicyTx(ctx, tx, policyID)
		if err != nil {
			return 0, nil, fmt.Errorf("disable_policy: %w", err)
		}
		return policyID, taskIDs, nil
	}
	taskIDs, err := policy.NewControlService(d.db, nil).DisableTx(ctx, tx, policyID)
	if err != nil {
		return 0, nil, fmt.Errorf("disable_policy: %w", err)
	}
	return policyID, taskIDs, nil
}

func (d *Dispatcher) execTriggerTaskTx(
	ctx context.Context,
	tx *gorm.DB,
	actionConfig string,
	evtCtx map[string]interface{},
) (detailsJSON string, err error) {
	cfg := renderConfig(actionConfig, evtCtx)
	taskID := parseConfigUint(cfg, "task_id")
	if taskID == 0 {
		return "", fmt.Errorf("trigger_task: task_id 缺失或解析失败 (config=%s)", actionConfig)
	}
	if d.triggerer == nil {
		return "", fmt.Errorf("trigger_task: 任务执行器未初始化")
	}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	default:
	}
	triggerer, ok := d.triggerer.(TransactionalTaskTriggerer)
	if !ok {
		return "", fmt.Errorf("trigger_task: 任务执行器不支持事务幂等触发")
	}
	runID, err := triggerer.ReserveAutomationRunTx(ctx, tx, taskID)
	if err != nil {
		return "", fmt.Errorf("trigger_task: 触发 Task id=%d 失败: %w", taskID, err)
	}
	detailsBytes, err := json.Marshal(map[string]interface{}{
		"task_run_id": runID,
		"task_id":     taskID,
	})
	if err != nil {
		return "", fmt.Errorf("trigger_task: encode details: %w", err)
	}
	return string(detailsBytes), nil
}

// execPausePolicy sets policy.SkipNext = true for the policy_id in config.
func (d *Dispatcher) execPausePolicy(ctx context.Context, actionConfig string, evtCtx map[string]interface{}) error {
	log := logger.Module("automation")
	cfg := renderConfig(actionConfig, evtCtx)
	policyID := parseConfigUint(cfg, "policy_id")
	if policyID == 0 {
		return fmt.Errorf("pause_policy: policy_id 缺失或解析失败 (config=%s)", actionConfig)
	}
	if d.policyController != nil {
		if err := d.policyController.PausePolicyNext(ctx, policyID); err != nil {
			return fmt.Errorf("pause_policy: %w", err)
		}
	} else if err := policy.NewControlService(d.db, nil).PauseNext(ctx, policyID); err != nil {
		return fmt.Errorf("pause_policy: %w", err)
	}
	log.Info().Uint("policy_id", policyID).Msg("automation: policy paused (skip_next=true)")
	return nil
}

// execDisablePolicy disables a policy and durably clears generated cron specs.
func (d *Dispatcher) execDisablePolicy(ctx context.Context, actionConfig string, evtCtx map[string]interface{}) error {
	log := logger.Module("automation")
	cfg := renderConfig(actionConfig, evtCtx)
	policyID := parseConfigUint(cfg, "policy_id")
	if policyID == 0 {
		return fmt.Errorf("disable_policy: policy_id 缺失或解析失败 (config=%s)", actionConfig)
	}
	if d.policyController != nil {
		if err := d.policyController.DisablePolicy(ctx, policyID); err != nil {
			return fmt.Errorf("disable_policy: %w", err)
		}
	} else if err := policy.NewControlService(d.db, nil).Disable(ctx, policyID); err != nil {
		return fmt.Errorf("disable_policy: %w", err)
	}
	log.Info().Uint("policy_id", policyID).Msg("automation: policy disabled")
	return nil
}

// execTriggerTask triggers a task through the runtime manager so the run enters
// the same execution path as manual/cron triggers instead of leaving a pending
// TaskRun row behind.
func (d *Dispatcher) execTriggerTask(ctx context.Context, actionConfig string, evtCtx map[string]interface{}) (detailsJSON string, err error) {
	log := logger.Module("automation")
	cfg := renderConfig(actionConfig, evtCtx)
	taskID := parseConfigUint(cfg, "task_id")
	if taskID == 0 {
		return "", fmt.Errorf("trigger_task: task_id 缺失或解析失败 (config=%s)", actionConfig)
	}
	if d.triggerer == nil {
		return "", fmt.Errorf("trigger_task: 任务执行器未初始化")
	}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	default:
	}
	runID, err := d.triggerer.TriggerAutomation(taskID)
	if err != nil {
		return "", fmt.Errorf("trigger_task: 触发 Task id=%d 失败: %w", taskID, err)
	}
	detailsBytes, err := json.Marshal(map[string]interface{}{
		"task_run_id": runID,
		"task_id":     taskID,
	})
	if err != nil {
		return "", fmt.Errorf("trigger_task: encode details: %w", err)
	}
	log.Info().Uint("task_id", taskID).Uint("task_run_id", runID).Msg("automation: task triggered")
	return string(detailsBytes), nil
}

// execSendNotification builds a log details entry. Actual notification
// delivery will be handled in PR2 via the alerting package.
func (d *Dispatcher) execSendNotification(actionConfig string, evtCtx map[string]interface{}) (detailsJSON string) {
	log := logger.Module("automation")
	// Parse message template from action_config, render it with event context.
	msgTmpl := parseConfigString(actionConfig, "message")
	if msgTmpl == "" {
		// Fallback: use the entire actionConfig as the message.
		msgTmpl = actionConfig
	}
	message := renderTemplate(msgTmpl, evtCtx)
	log.Info().Str("message", message).Msg("automation: notification action logged")
	b, _ := json.Marshal(map[string]interface{}{
		"message": message,
	})
	return string(b)
}

// renderTemplate replaces {{.VarName}} placeholders with values from ctx.
func renderTemplate(tmpl string, ctx map[string]interface{}) string {
	if !strings.Contains(tmpl, "{{.") {
		return tmpl
	}
	re := regexp.MustCompile(`\{\{\.(\w+)\}\}`)
	return re.ReplaceAllStringFunc(tmpl, func(match string) string {
		sub := re.FindStringSubmatch(match)
		if len(sub) < 2 {
			return match
		}
		key := sub[1]
		val, ok := ctx[key]
		if !ok {
			return match
		}
		return fmt.Sprint(val)
	})
}

// renderConfig renders templates in an action_config JSON string
// and returns the rendered JSON.
func renderConfig(actionConfig string, evtCtx map[string]interface{}) string {
	return renderTemplate(actionConfig, evtCtx)
}

// parseConfigString extracts a string value from a JSON config map.
func parseConfigString(cfgJSON string, key string) string {
	if strings.TrimSpace(cfgJSON) == "" {
		return ""
	}
	var cfg map[string]interface{}
	if err := json.Unmarshal([]byte(cfgJSON), &cfg); err != nil {
		return ""
	}
	val, ok := cfg[key]
	if !ok {
		return ""
	}
	s, ok := val.(string)
	if !ok {
		return ""
	}
	return s
}

// parseConfigUint extracts a uint value from a JSON config map.
func parseConfigUint(cfgJSON string, key string) uint {
	if strings.TrimSpace(cfgJSON) == "" {
		return 0
	}
	var cfg map[string]interface{}
	if err := json.Unmarshal([]byte(cfgJSON), &cfg); err != nil {
		return 0
	}
	val, ok := cfg[key]
	if !ok {
		return 0
	}
	switch v := val.(type) {
	case float64:
		return uint(v)
	case string:
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			return 0
		}
		return uint(n)
	case json.Number:
		n, err := v.Float64()
		if err != nil {
			return 0
		}
		return uint(n)
	default:
		return 0
	}
}
