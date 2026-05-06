# Event-Trigger Automation (事件触发-动作编排)

## Overview

Event-trigger automation is a lightweight rule engine that connects platform events to actions. When a specified event occurs (e.g., backup failure, anomaly detected) and the event matches optional filter conditions, the system automatically executes the configured action (e.g., pause a policy, send a notification).

This enables self-healing operations: detect problems and respond automatically without manual intervention.

## How Rules Work

Each automation rule defines:

1. **Event Type** -- what to watch for
2. **Event Filter** (optional) -- key-value conditions that must match for the action to fire
3. **Action Type** -- what to do
4. **Action Config** -- parameters for the action, with support for template variables extracted from the event context

```
Event occurs → Check enabled rules → Match event type → Check filter → Execute action → Record log
```

## Available Event Types

| event_type          | Description                    | Available Filter Fields          |
|---------------------|--------------------------------|----------------------------------|
| `anomaly_detected`  | Anomaly event raised           | detector, metric, severity       |
| `backup_failed`     | Backup task run failed         | policy_id, node_id, executor_type |
| `backup_succeeded`  | Backup task run succeeded      | policy_id, node_id, executor_type |
| `drill_failed`      | Recovery drill failed          | policy_id                        |
| `node_offline`      | Node went offline              | node_id                          |
| `node_disk_high`    | Node disk usage exceeded threshold | node_id                      |

## Available Action Types

| action_type          | Description                         | Config Parameters       |
|----------------------|-------------------------------------|-------------------------|
| `pause_policy`       | Pause a policy (sets SkipNext=true) | policy_id               |
| `disable_policy`     | Disable a policy                    | policy_id               |
| `trigger_task`       | Trigger a task execution            | task_id                 |
| `send_notification`  | Send an additional notification     | message                 |

## Template Variables

Action config values support Go-style template variables that are automatically extracted from the event context at execution time:

| Variable        | Description                           | Available in events                    |
|-----------------|---------------------------------------|----------------------------------------|
| `{{.PolicyID}}` | The policy ID from the event context  | backup_failed, backup_succeeded, drill_failed |
| `{{.TaskID}}`   | The task ID from the event context    | backup_failed, backup_succeeded        |
| `{{.NodeID}}`   | The node ID from the event context    | node_offline, node_disk_high, backup_failed, backup_succeeded |

These are replaced with actual IDs when the rule is triggered, so one rule can apply to different resources.

## Examples

### Example 1: Pause policy on ransomware anomaly

**Rule:**
- Event Type: `anomaly_detected`
- Filter: `{ "metric": "ransomware_pattern" }`
- Action Type: `pause_policy`
- Action Config: `{ "policy_id": "{{.PolicyID}}" }`

**Behavior:** When an anomaly is detected with the ransomware pattern metric, the affected backup policy is automatically paused.

### Example 2: Notify on backup failure

**Rule:**
- Event Type: `backup_failed`
- Action Type: `send_notification`
- Action Config: `{ "message": "Backup failed for policy {{.PolicyID}} on node {{.NodeID}}" }`

**Behavior:** When any backup task fails, a notification is sent with the relevant IDs embedded in the message.

### Example 3: Critical disk alert triggers emergency task

**Rule:**
- Event Type: `node_disk_high`
- Filter: `{ "node_id": "5" }`
- Action Type: `trigger_task`
- Action Config: `{ "task_id": "12" }`

**Behavior:** When node 5's disk usage exceeds the threshold, trigger task #12 (e.g., a cleanup script).

## Managing Rules

### Web UI

Navigate to **Automation Rules** (自动化规则) in the sidebar under the "Automate" group. From there you can:

- **Create**: Click "New Rule" and fill in the form (event type, optional filters, action type, action parameters, enabled toggle)
- **Edit**: Click the pencil icon on any row
- **Delete**: Click the trash icon -- a confirmation dialog will appear
- **Enable/Disable**: Toggle the switch inline -- disabled rules won't fire

### API

Rules are managed via the `/api/v1/automation-rules` endpoints:

| Method   | Path                            | Description          |
|----------|---------------------------------|----------------------|
| `GET`    | `/automation-rules`             | List all rules       |
| `POST`   | `/automation-rules`             | Create a rule        |
| `GET`    | `/automation-rules/:id`         | Get rule details     |
| `PUT`    | `/automation-rules/:id`         | Update a rule        |
| `DELETE` | `/automation-rules/:id`         | Delete a rule        |

### Request/Response Format

**Create rule example:**

```json
POST /api/v1/automation-rules
{
  "name": "Ransomware pause policy",
  "event_type": "anomaly_detected",
  "event_filter": { "metric": "ransomware_pattern" },
  "action_type": "pause_policy",
  "action_config": { "policy_id": "{{.PolicyID}}" },
  "enabled": true
}
```

**Response:**

```json
{
  "code": 0,
  "message": "ok",
  "data": {
    "id": 1,
    "name": "Ransomware pause policy",
    "event_type": "anomaly_detected",
    "event_filter": { "metric": "ransomware_pattern" },
    "action_type": "pause_policy",
    "action_config": { "policy_id": "{{.PolicyID}}" },
    "enabled": true,
    "created_at": "2026-05-05T10:00:00Z",
    "updated_at": "2026-05-05T10:00:00Z"
  }
}
```

## Execution Logs

Rule execution results are recorded in the `automation_rule_logs` table (backend-only for now). Each log entry includes:

- `rule_id` -- which rule was triggered
- `event_type` / `action_type` -- what happened
- `result` -- success or failure
- `error` -- error message if failed

This data is currently accessible via direct database query or future API endpoints.

## Limitations (Out of Scope for MVP)

- Complex filter expressions (AND/OR, time windows) -- filters are simple key-value equality
- Webhook action type -- only the 4 built-in actions are available
- Custom event types -- only the 6 built-in event types are available
- Frontend execution log viewer -- not yet implemented
