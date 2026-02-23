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
