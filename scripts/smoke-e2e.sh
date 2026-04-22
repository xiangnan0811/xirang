#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://127.0.0.1:5173/api/v1}"
ADMIN_USERNAME="${ADMIN_USERNAME:-}"
ADMIN_PASSWORD="${ADMIN_PASSWORD:-}"
RUN_ID="$(date +%Y%m%d%H%M%S)-$RANDOM"
PREFIX="smoke-${RUN_ID}"

TOKEN=""
INTEGRATION_ID=""
SSH_KEY_ID=""
NODE_ID=""
POLICY_ID=""
TASK_ID=""

HTTP_CODE=""
HTTP_BODY=""

log() {
  printf '\n[smoke] %s\n' "$1"
}

require_credentials() {
  if [[ -z "${ADMIN_USERNAME}" || -z "${ADMIN_PASSWORD}" ]]; then
    echo "[smoke][error] 缺少管理员凭据：请显式设置 ADMIN_USERNAME 和 ADMIN_PASSWORD"
    echo "[smoke][error] 示例：ADMIN_USERNAME=admin ADMIN_PASSWORD='<strong-password>' bash scripts/smoke-e2e.sh"
    exit 2
  fi
}

api_call() {
  local method="$1"
  local path="$2"
  local body="${3-}"

  local -a curl_args
  curl_args=(-sS --max-time 20 -X "$method" "$BASE_URL$path" -w $'\n%{http_code}')

  if [[ -n "$TOKEN" ]]; then
    curl_args+=(-H "Authorization: Bearer $TOKEN")
  fi

  if [[ -n "$body" ]]; then
    curl_args+=(-H "Content-Type: application/json" -d "$body")
  fi

  local response
  response="$(curl "${curl_args[@]}")"
  HTTP_CODE="$(printf '%s' "$response" | tail -n1)"
  HTTP_BODY="$(printf '%s' "$response" | sed '$d')"
}

assert_status() {
  local expected="$1"
  if [[ "$HTTP_CODE" != "$expected" ]]; then
    echo "[smoke][error] 期望状态码 $expected，实际 $HTTP_CODE"
    echo "[smoke][error] 响应体:"
    echo "$HTTP_BODY"
    exit 1
  fi
}

json_get() {
  local expr="$1"
  JSON_INPUT="$HTTP_BODY" python - "$expr" <<'PY'
import json
import os
import sys

expr = sys.argv[1]
raw = os.environ.get("JSON_INPUT", "")
if not raw:
    print("")
    raise SystemExit(0)

data = json.loads(raw)
value = data
for part in expr.split('.'):
    if isinstance(value, list):
        value = value[int(part)]
    else:
        value = value[part]
if isinstance(value, bool):
    print("true" if value else "false")
elif value is None:
    print("")
else:
    print(value)
PY
}

cleanup() {
  set +e

  if [[ -n "$TASK_ID" ]]; then
    log "清理任务 #$TASK_ID"
    api_call DELETE "/tasks/${TASK_ID}"
  fi

  if [[ -n "$POLICY_ID" ]]; then
    log "清理策略 #$POLICY_ID"
    api_call DELETE "/policies/${POLICY_ID}"
  fi

  if [[ -n "$NODE_ID" ]]; then
    log "清理节点 #$NODE_ID"
    api_call DELETE "/nodes/${NODE_ID}"
  fi

  if [[ -n "$SSH_KEY_ID" ]]; then
    log "清理 SSH Key #$SSH_KEY_ID"
    api_call DELETE "/ssh-keys/${SSH_KEY_ID}"
  fi

  if [[ -n "$INTEGRATION_ID" ]]; then
    log "清理通知通道 #$INTEGRATION_ID"
    api_call DELETE "/integrations/${INTEGRATION_ID}"
  fi
}

trap cleanup EXIT

require_credentials

log "登录管理员账号"
api_call POST "/auth/login" "{\"username\":\"${ADMIN_USERNAME}\",\"password\":\"${ADMIN_PASSWORD}\"}"
assert_status 200
TOKEN="$(json_get token)"
if [[ -z "$TOKEN" ]]; then
  echo "[smoke][error] 登录成功但未获得 token"
  echo "[smoke][error] 响应体: $HTTP_BODY"
  exit 1
fi

log "新增通知通道"
api_call POST "/integrations" "{\"type\":\"webhook\",\"name\":\"${PREFIX}-integration\",\"endpoint\":\"https://example.com/hook\",\"enabled\":true,\"fail_threshold\":2,\"cooldown_minutes\":5}"
assert_status 201
INTEGRATION_ID="$(json_get data.id)"

log "新增 SSH Key"
api_call POST "/ssh-keys" "{\"name\":\"${PREFIX}-key\",\"username\":\"root\",\"private_key\":\"-----BEGIN OPENSSH PRIVATE KEY-----\\n${PREFIX}\\n-----END OPENSSH PRIVATE KEY-----\"}"
assert_status 201
SSH_KEY_ID="$(json_get data.id)"

log "新增节点（使用 ssh_key_id）"
api_call POST "/nodes" "{\"name\":\"${PREFIX}-node\",\"host\":\"203.0.113.10\",\"port\":22,\"username\":\"root\",\"auth_type\":\"key\",\"ssh_key_id\":${SSH_KEY_ID},\"tags\":\"prod,smoke\",\"base_path\":\"/\"}"
assert_status 201
NODE_ID="$(json_get data.id)"

log "编辑节点"
api_call PUT "/nodes/${NODE_ID}" "{\"name\":\"${PREFIX}-node-updated\",\"host\":\"203.0.113.11\",\"port\":22,\"username\":\"root\",\"auth_type\":\"key\",\"ssh_key_id\":${SSH_KEY_ID},\"tags\":\"prod,smoke,updated\",\"status\":\"warning\",\"base_path\":\"/data\"}"
assert_status 200

log "节点连通性测试（允许失败结果，但接口需可用）"
api_call POST "/nodes/${NODE_ID}/test-connection"
assert_status 200

log "新增策略"
api_call POST "/policies" "{\"name\":\"${PREFIX}-policy\",\"source_path\":\"/var/data\",\"target_path\":\"/backup/data\",\"cron_spec\":\"0 */2 * * *\",\"enabled\":true}"
assert_status 201
POLICY_ID="$(json_get data.id)"

log "编辑策略"
api_call PUT "/policies/${POLICY_ID}" "{\"name\":\"${PREFIX}-policy-v2\",\"source_path\":\"/var/data\",\"target_path\":\"/backup/data\",\"cron_spec\":\"30 */3 * * *\",\"enabled\":true}"
assert_status 200

log "新增任务"
api_call POST "/tasks" "{\"name\":\"${PREFIX}-task\",\"node_id\":${NODE_ID},\"policy_id\":${POLICY_ID},\"executor_type\":\"rsync\",\"rsync_source\":\"/var/data\",\"rsync_target\":\"/backup/data\",\"cron_spec\":\"30 */3 * * *\"}"
assert_status 201
TASK_ID="$(json_get data.id)"

log "任务列表筛选与排序"
api_call GET "/tasks?keyword=${PREFIX}&node_id=${NODE_ID}&policy_id=${POLICY_ID}&sort=-id&limit=10"
assert_status 200

log "手动触发任务"
api_call POST "/tasks/${TASK_ID}/trigger"
assert_status 202

log "查询任务日志"
api_call GET "/tasks/${TASK_ID}/logs?limit=20"
assert_status 200

log "通知投递统计"
api_call GET "/alerts/delivery-stats?hours=24"
assert_status 200

log "审计日志查询（含时间范围）"
FROM_TS="$(date -u -d '-24 hours' +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -v-24H +%Y-%m-%dT%H:%M:%SZ)"
TO_TS="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
api_call GET "/audit-logs?method=POST&from=${FROM_TS}&to=${TO_TS}&limit=20"
assert_status 200

log "审计日志 CSV 导出"
EXPORT_HEADERS="$(mktemp)"
EXPORT_BODY="$(mktemp)"
curl -sS --max-time 20 -D "$EXPORT_HEADERS" -o "$EXPORT_BODY" -H "Authorization: Bearer $TOKEN" "${BASE_URL}/audit-logs/export?from=${FROM_TS}&to=${TO_TS}&limit=20" >/dev/null
if ! grep -q " 200 " "$EXPORT_HEADERS"; then
  echo "[smoke][error] 导出接口非 200"
  cat "$EXPORT_HEADERS"
  exit 1
fi
if ! head -n1 "$EXPORT_BODY" | grep -q "^id,created_at,username,role,method,path,status_code,client_ip,user_agent"; then
  echo "[smoke][error] CSV 表头不符合预期"
  head -n5 "$EXPORT_BODY"
  exit 1
fi

log "✅ 冒烟验证通过：登录/节点/SSHKey/策略/任务/通知统计/审计导出链路正常"

# === P5b: silence smoke test ===
log "=== P5b: silence smoke test ==="

# Compute timestamps (portable across macOS BSD date and Linux GNU date)
NOW=$(date -u +%Y-%m-%dT%H:%M:%SZ)
HOUR_LATER=$(date -u -v+1H +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -d '+1 hour' +%Y-%m-%dT%H:%M:%SZ)

# Create silence for node 1, category probe_down, next hour
api_call POST "/silences" \
  "{\"name\":\"smoke\",\"match_node_id\":1,\"match_category\":\"probe_down\",\"match_tags\":[],\"starts_at\":\"${NOW}\",\"ends_at\":\"${HOUR_LATER}\",\"note\":\"smoke\"}"
assert_status 201
SILENCE_ID="$(json_get data.id)"
if [[ -z "$SILENCE_ID" ]]; then
  echo "[smoke][error] FAIL: silence create — response: $HTTP_BODY"
  exit 1
fi
trap 'if [ -n "${SILENCE_ID:-}" ]; then api_call DELETE "/silences/${SILENCE_ID}" >/dev/null 2>&1 || true; fi' EXIT
log "  created silence $SILENCE_ID"

# Revoke silence (DELETE → soft-delete, sets ends_at=now)
api_call DELETE "/silences/${SILENCE_ID}"
assert_status 200
log "  revoked silence $SILENCE_ID"

# Verify silence no longer appears in the active list
api_call GET "/silences?active=true"
assert_status 200
# Use json_get to check whether the revoked id still appears among active silences.
# Project envelope is {Code, Message, Data:[{id,...},...]}; we search the raw body
# for the id string rather than parsing nested arrays (portable, no jq needed).
if printf '%s' "$HTTP_BODY" | grep -q "\"id\":${SILENCE_ID}[^0-9]"; then
  echo "[smoke][error] FAIL: silence $SILENCE_ID still active after revoke"
  echo "[smoke][error] response: $HTTP_BODY"
  exit 1
fi

log "PASS: silence smoke"

# === P5d-1: SLO smoke test ===
log "=== P5d-1: SLO smoke test ==="

api_call POST "/slos" '{"name":"smoke-slo","metric_type":"availability","match_tags":[],"threshold":0.99,"window_days":28,"enabled":true}'
assert_status 201
SLO_ID=$(json_get data.id)
if [ -z "${SLO_ID:-}" ] || [ "$SLO_ID" = "null" ]; then
  echo "[smoke][error] FAIL: slo create — response: $HTTP_BODY"
  exit 1
fi
log "  created slo $SLO_ID"

# Register cleanup trap (chains with any existing trap)
trap 'if [ -n "${SLO_ID:-}" ]; then api_call DELETE "/slos/${SLO_ID}" "" >/dev/null 2>&1 || true; fi' EXIT

api_call GET "/slos/$SLO_ID/compliance" ""
assert_status 200

api_call GET "/slos/compliance-summary" ""
assert_status 200

api_call DELETE "/slos/$SLO_ID" ""
assert_status 204
SLO_ID=""  # Clear so trap doesn't double-delete

api_call GET "/slos" ""
assert_status 200

log "PASS: SLO smoke"

# === P5c: node log config + global log settings smoke test ===
log "=== P5c: node log config smoke test ==="

log "P5c: GET /nodes/\$NODE_ID/log-config → 200"
api_call GET "/nodes/${NODE_ID}/log-config"
assert_status 200

log "P5c: PATCH /nodes/\$NODE_ID/log-config 有效配置 → 200，并验证 log_retention_days=14"
api_call PATCH "/nodes/${NODE_ID}/log-config" \
  "{\"log_paths\":[\"/var/log/app.log\"],\"log_journalctl_enabled\":false,\"log_retention_days\":14}"
assert_status 200
ACTUAL_RETENTION="$(json_get log_retention_days)"
if [[ "$ACTUAL_RETENTION" != "14" ]]; then
  echo "[smoke][error] FAIL: log_retention_days 期望 14，实际 ${ACTUAL_RETENTION}"
  echo "[smoke][error] 响应体: $HTTP_BODY"
  exit 1
fi
log "P5c: log_retention_days 验证通过（实际值 ${ACTUAL_RETENTION}）"

log "P5c: PATCH /nodes/\$NODE_ID/log-config 黑名单路径 /etc/passwd → 400"
api_call PATCH "/nodes/${NODE_ID}/log-config" \
  "{\"log_paths\":[\"/etc/passwd\"],\"log_journalctl_enabled\":false,\"log_retention_days\":7}"
assert_status 400

log "P5c: GET /settings/logs → 200"
api_call GET "/settings/logs"
assert_status 200

log "P5c: PATCH /settings/logs default_retention_days=45 → 200"
api_call PATCH "/settings/logs" "{\"default_retention_days\":45}"
assert_status 200

log "PASS: P5c node log config smoke"

# === P5d-2: custom dashboards smoke test ===
log "=== P5d-2: custom dashboards smoke test ==="

DASHBOARD_ID=""
PANEL_ID=""

# 1. 创建看板
log "P5d-2: POST /dashboards → 200"
api_call POST "/dashboards" "{\"name\":\"smoke-dash-${RUN_ID}\",\"description\":\"smoke\",\"time_range\":\"1h\",\"auto_refresh_seconds\":30}"
assert_status 200
DASHBOARD_ID="$(json_get data.id)"
if [[ -z "$DASHBOARD_ID" || "$DASHBOARD_ID" == "null" ]]; then
  echo "[smoke][error] FAIL: dashboard create — response: $HTTP_BODY"
  exit 1
fi
log "  created dashboard $DASHBOARD_ID"

# 2. 新增面板
log "P5d-2: POST /dashboards/:id/panels → 200"
api_call POST "/dashboards/${DASHBOARD_ID}/panels" \
  "{\"title\":\"CPU smoke\",\"chart_type\":\"line\",\"metric\":\"node.cpu\",\"filters\":{\"node_ids\":[${NODE_ID}]},\"aggregation\":\"avg\",\"layout_x\":0,\"layout_y\":0,\"layout_w\":6,\"layout_h\":4}"
assert_status 200
PANEL_ID="$(json_get data.id)"
if [[ -z "$PANEL_ID" || "$PANEL_ID" == "null" ]]; then
  echo "[smoke][error] FAIL: panel create — response: $HTTP_BODY"
  exit 1
fi
log "  created panel $PANEL_ID"

# 3. 面板查询（合法指标）
log "P5d-2: POST /dashboards/panel-query node.cpu → 200"
NOW_TS="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
HOUR_AGO="$(date -u -v-1H +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -d '-1 hour' +%Y-%m-%dT%H:%M:%SZ)"
api_call POST "/dashboards/panel-query" \
  "{\"metric\":\"node.cpu\",\"filters\":{\"node_ids\":[${NODE_ID}]},\"aggregation\":\"avg\",\"start\":\"${HOUR_AGO}\",\"end\":\"${NOW_TS}\"}"
assert_status 200

# 4. 更新布局
log "P5d-2: PUT /dashboards/:id/panels/layout → 200"
api_call PUT "/dashboards/${DASHBOARD_ID}/panels/layout" \
  "{\"items\":[{\"id\":${PANEL_ID},\"layout_x\":2,\"layout_y\":0,\"layout_w\":8,\"layout_h\":5}]}"
assert_status 200

# 5. 面板查询（无效指标）→ 400
log "P5d-2: POST /dashboards/panel-query bogus metric → 400"
api_call POST "/dashboards/panel-query" \
  "{\"metric\":\"bogus.metric\",\"filters\":{},\"aggregation\":\"avg\",\"start\":\"${HOUR_AGO}\",\"end\":\"${NOW_TS}\"}"
assert_status 400

# 6. 删除看板
log "P5d-2: DELETE /dashboards/:id → 200"
api_call DELETE "/dashboards/${DASHBOARD_ID}"
assert_status 200
DASHBOARD_ID=""  # 防止重复清理

log "PASS: P5d-2 custom dashboards smoke"

# === P5e: escalation policy smoke test ===
log "=== P5e: escalation policy smoke test ==="

ESCALATION_ID=""

# 1. Create escalation policy
log "P5e: POST /escalation-policies → 201"
api_call POST "/escalation-policies" \
  "{\"name\":\"smoke-esc-${RUN_ID}\",\"description\":\"smoke\",\"min_severity\":\"warning\",\"enabled\":true,\"levels\":[{\"delay_minutes\":1,\"integration_ids\":[],\"severity_override\":\"\",\"tags\":[]}]}"
assert_status 201
ESCALATION_ID="$(json_get data.id)"
if [[ -z "$ESCALATION_ID" || "$ESCALATION_ID" == "null" ]]; then
  echo "[smoke][error] FAIL: escalation policy create — response: $HTTP_BODY"
  exit 1
fi
log "  created escalation policy $ESCALATION_ID"

# Register cleanup trap
trap 'if [ -n "${ESCALATION_ID:-}" ]; then api_call DELETE "/escalation-policies/${ESCALATION_ID}" "" >/dev/null 2>&1 || true; fi' EXIT

# 2. GET escalation policy by ID
log "P5e: GET /escalation-policies/\$ESCALATION_ID → 200"
api_call GET "/escalation-policies/${ESCALATION_ID}"
assert_status 200

# 4. List escalation policies
log "P5e: GET /escalation-policies → 200"
api_call GET "/escalation-policies"
assert_status 200

# 5. Wait >30s for engine tick then check alert escalation events (if any open alerts exist)
log "P5e: sleeping 35s for engine tick..."
sleep 35

api_call GET "/alerts?node_id=${NODE_ID}&status=open"
assert_status 200
# If any open alerts exist, verify the escalation-events endpoint is reachable
if printf '%s' "$HTTP_BODY" | grep -q '"id":[0-9]'; then
  ALERT_ID="$(json_get data.0.id 2>/dev/null || true)"
  if [[ -n "$ALERT_ID" && "$ALERT_ID" != "null" && "$ALERT_ID" != "" ]]; then
    log "P5e: GET /alerts/\$ALERT_ID/escalation-events → 200"
    api_call GET "/alerts/${ALERT_ID}/escalation-events"
    assert_status 200
  fi
fi

# 6. Malformed body → 400
log "P5e: POST /escalation-policies with malformed body → 400"
api_call POST "/escalation-policies" "{\"name\":\"\"}"
assert_status 400

# 7. DELETE escalation policy
log "P5e: DELETE /escalation-policies/\$ESCALATION_ID → 200"
api_call DELETE "/escalation-policies/${ESCALATION_ID}"
assert_status 200
ESCALATION_ID=""  # prevent double-cleanup

log "PASS: P5e escalation policy smoke"

# === P5f: anomaly detection smoke test ===
log "=== P5f: anomaly detection ==="

# Toggle via settings
api_call PATCH "/settings" '{"anomaly.enabled":"true"}'
assert_status 200

# Per-node events (should respond 200 even if empty)
api_call GET "/nodes/${NODE_ID}/anomaly-events" ""
assert_status 200

# Global list with severity filter
api_call GET "/anomaly-events?severity=critical&limit=10" ""
assert_status 200

# Invalid severity → 400
api_call GET "/anomaly-events?severity=bogus" ""
assert_status 400

# Invalid detector → 400
api_call GET "/anomaly-events?detector=bogus" ""
assert_status 400

log "PASS: anomaly"
