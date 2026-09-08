package automation

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"xirang/backend/internal/logger"
	"xirang/backend/internal/model"
	"xirang/backend/internal/policy"

	"gorm.io/gorm"
)

type TaskTriggerer interface {
	TriggerAutomation(taskID uint) (uint, error)
}

type KeyedTaskTriggerer interface {
	TriggerAutomationWithKey(taskID uint, key string) (uint, error)
}

type PolicyController interface {
	PausePolicyNext(ctx context.Context, policyID uint) error
	DisablePolicy(ctx context.Context, policyID uint) error
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
// Execution logs are persisted per rule.
func (d *Dispatcher) Dispatch(ctx context.Context, event Event) error {
	log := logger.Module("automation")

	var rules []model.AutomationRule
	if err := d.db.WithContext(ctx).Where("event_type = ? AND enabled = ?", event.Type, true).Find(&rules).Error; err != nil {
		return fmt.Errorf("automation dispatch: query rules: %w", err)
	}

	for _, rule := range rules {
		if !matchFilter(rule.EventFilter, event.Context) {
			continue
		}
		logEntry := d.executeAction(ctx, rule, event)
		if err := d.db.WithContext(ctx).Create(&logEntry).Error; err != nil {
			log.Warn().Err(err).Uint("rule_id", rule.ID).Msg("automation dispatch: failed to persist execution log")
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
	log := logger.Module("automation")
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

	_ = log // keep import for future structured logging in individual exec methods
	return logEntry
}

func resultFrom(err error) string {
	if err != nil {
		return ResultError
	}
	return ResultSuccess
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

	var runID uint
	if key, ok := evtCtx["_effect_key"].(string); ok && strings.TrimSpace(key) != "" {
		keyed, supported := d.triggerer.(KeyedTaskTriggerer)
		if !supported {
			return "", fmt.Errorf("trigger_task: 任务执行器不支持幂等触发")
		}
		runID, err = keyed.TriggerAutomationWithKey(taskID, key)
	} else {
		runID, err = d.triggerer.TriggerAutomation(taskID)
	}
	if err != nil {
		return "", fmt.Errorf("trigger_task: 触发 Task id=%d 失败: %w", taskID, err)
	}

	detailsBytes, _ := json.Marshal(map[string]interface{}{
		"task_run_id": runID,
		"task_id":     taskID,
	})
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
