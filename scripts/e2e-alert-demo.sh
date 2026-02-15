#!/usr/bin/env bash
set -euo pipefail

API_BASE_URL="${API_BASE_URL:-http://127.0.0.1:8080/api/v1}"
XR_LOGIN_USERNAME="${XR_LOGIN_USERNAME:-${E2E_USERNAME:-admin}}"
XR_LOGIN_PASSWORD="${XR_LOGIN_PASSWORD:-${E2E_PASSWORD:-REDACTED}}"
BAD_HOST="${BAD_HOST:-127.0.0.1}"
BAD_PORT="${BAD_PORT:-65535}"
WEBHOOK_ENDPOINT="${WEBHOOK_ENDPOINT:-http://127.0.0.1:9/xirang-e2e}"
POLL_TIMES="${POLL_TIMES:-10}"
POLL_INTERVAL_SECONDS="${POLL_INTERVAL_SECONDS:-2}"
CLEANUP="${CLEANUP:-1}"

api_root="${API_BASE_URL%/api/v1}"
if [[ "${api_root}" == "${API_BASE_URL}" ]]; then
  api_root="${API_BASE_URL%/}"
fi
HEALTH_URL="${HEALTH_URL:-${api_root}/healthz}"

created_node_id=""
created_integration_id=""

log() {
  printf '[XiRang-E2E] %s\n' "$*"
}

extract_json() {
  local json="$1"
  local path="$2"
  python3 - "$json" "$path" <<'PY'
import json
import sys

raw = sys.argv[1]
path = sys.argv[2]
obj = json.loads(raw)
current = obj
for key in path.split('.'):
    if key == '':
        continue
    if isinstance(current, list):
        idx = int(key)
        current = current[idx]
    else:
        current = current[key]
if isinstance(current, (dict, list)):
    print(json.dumps(current, ensure_ascii=False))
elif current is None:
    print("")
else:
    print(str(current))
PY
}

json_array_len() {
  local json_array="$1"
  python3 - "$json_array" <<'PY'
import json
import sys
arr = json.loads(sys.argv[1])
print(len(arr) if isinstance(arr, list) else 0)
PY
}

api_call() {
  local method="$1"
  local path="$2"
  local body="${3:-}"
  local response

  if [[ -n "$body" ]]; then
    response=$(curl -sS -X "$method" "${API_BASE_URL}${path}" \
      -H "Content-Type: application/json" \
      -H "Authorization: Bearer ${TOKEN:-}" \
      -d "$body" \
      -w $'\n%{http_code}')
  else
    response=$(curl -sS -X "$method" "${API_BASE_URL}${path}" \
      -H "Authorization: Bearer ${TOKEN:-}" \
      -w $'\n%{http_code}')
  fi

  local code body_text
  code="${response##*$'\n'}"
  body_text="${response%$'\n'*}"

  if [[ "$code" != 2* ]]; then
    log "请求失败 ${method} ${path} (HTTP ${code})"
    printf '%s\n' "$body_text"
    return 1
  fi

  printf '%s' "$body_text"
}

cleanup() {
  if [[ "$CLEANUP" != "1" ]]; then
    return 0
  fi

  log "开始清理演示资源..."
  if [[ -n "$created_node_id" ]]; then
    api_call DELETE "/nodes/${created_node_id}" >/dev/null 2>&1 || true
    log "已尝试删除节点: ${created_node_id}"
  fi
  if [[ -n "$created_integration_id" ]]; then
    api_call DELETE "/integrations/${created_integration_id}" >/dev/null 2>&1 || true
    log "已尝试删除通知通道: ${created_integration_id}"
  fi
}

trap cleanup EXIT

log "0/6 检查后端健康状态"
health_code=$(curl -sS -o /tmp/xirang-e2e-health.json -w '%{http_code}' "$HEALTH_URL" || true)
if [[ "$health_code" != "200" ]]; then
  log "后端健康检查失败 (HTTP ${health_code})，请先启动后端服务。"
  cat /tmp/xirang-e2e-health.json 2>/dev/null || true
  exit 1
fi

log "1/6 登录获取令牌"
login_resp=$(curl -sS -X POST "${API_BASE_URL}/auth/login" \
  -H "Content-Type: application/json" \
  -d "{\"username\":\"${XR_LOGIN_USERNAME}\",\"password\":\"${XR_LOGIN_PASSWORD}\"}" \
  -w $'\n%{http_code}')

login_code="${login_resp##*$'\n'}"
login_body="${login_resp%$'\n'*}"
if [[ "$login_code" != 2* ]]; then
  log "登录失败 (HTTP ${login_code})"
  printf '%s\n' "$login_body"
  log "请确认账号参数：XR_LOGIN_USERNAME / XR_LOGIN_PASSWORD"
  exit 1
fi
TOKEN=$(extract_json "$login_body" "token")
if [[ -z "$TOKEN" ]]; then
  log "登录响应中未解析到 token"
  printf '%s\n' "$login_body"
  exit 1
fi

suffix="$(date +%s)-$RANDOM"
integration_name="e2e-webhook-${suffix}"
node_name="e2e-node-${suffix}"

log "2/6 新建通知通道（Webhook，故意不可达用于验证投递失败记录）"
integration_body=$(cat <<JSON
{"type":"webhook","name":"${integration_name}","endpoint":"${WEBHOOK_ENDPOINT}","enabled":true,"fail_threshold":1,"cooldown_minutes":1}
JSON
)
integration_resp=$(api_call POST "/integrations" "$integration_body")
created_integration_id=$(extract_json "$integration_resp" "data.id")
log "通知通道已创建: id=${created_integration_id}, name=${integration_name}"

log "3/6 新建故障演示节点"
node_body=$(cat <<JSON
{"name":"${node_name}","host":"${BAD_HOST}","port":${BAD_PORT},"username":"root","auth_type":"password","password":"invalid-demo-password","tags":"e2e,demo","base_path":"/"}
JSON
)
node_resp=$(api_call POST "/nodes" "$node_body")
created_node_id=$(extract_json "$node_resp" "data.id")
log "演示节点已创建: id=${created_node_id}, host=${BAD_HOST}:${BAD_PORT}"

log "4/6 触发节点连通性探测（预期失败）"
probe_resp=$(api_call POST "/nodes/${created_node_id}/test-connection")
probe_ok=$(extract_json "$probe_resp" "ok")
probe_message=$(extract_json "$probe_resp" "message")
log "探测结果: ok=${probe_ok}, message=${probe_message}"

log "5/6 轮询查询该节点告警"
alert_id=""
for ((i=1; i<=POLL_TIMES; i++)); do
  alerts_resp=$(api_call GET "/alerts?node_id=${created_node_id}&status=open&limit=5")
  data_len=$(json_array_len "$(extract_json "$alerts_resp" "data")")

  if [[ "$data_len" -gt 0 ]]; then
    alert_id=$(extract_json "$alerts_resp" "data.0.id")
    break
  fi

  log "第 ${i}/${POLL_TIMES} 次未查到告警，${POLL_INTERVAL_SECONDS}s 后重试..."
  sleep "$POLL_INTERVAL_SECONDS"
done

if [[ -z "$alert_id" ]]; then
  log "未查询到告警，流程失败。"
  exit 1
fi
log "已捕获告警: alert_id=${alert_id}"

log "6/6 查询告警投递记录"
deliveries_resp=$(api_call GET "/alerts/${alert_id}/deliveries")
deliveries_len=$(json_array_len "$(extract_json "$deliveries_resp" "data")")

if [[ "$deliveries_len" -eq 0 ]]; then
  log "告警存在，但暂无投递记录。可能仍在冷却/阈值窗口。"
else
  first_status=$(extract_json "$deliveries_resp" "data.0.status")
  first_error=$(extract_json "$deliveries_resp" "data.0.error")
  log "首条投递记录: status=${first_status}, error=${first_error}"
fi

cat <<EOT

================= E2E 演示完成 =================
API_BASE_URL:      ${API_BASE_URL}
Integration ID:    ${created_integration_id}
Node ID:           ${created_node_id}
Alert ID:          ${alert_id}
Deliveries Count:  ${deliveries_len}
Cleanup:           ${CLEANUP}
=================================================

提示：
1) 若 Deliveries Count > 0，可直接验证通知发送链路（成功或失败都记录）。
2) 若你要保留资源做手工联调，请设置 CLEANUP=0 重新执行。
3) 账号参数请使用 XR_LOGIN_USERNAME / XR_LOGIN_PASSWORD（避免系统环境变量 USERNAME 冲突）。
4) 示例：CLEANUP=0 API_BASE_URL=http://127.0.0.1:8080/api/v1 XR_LOGIN_USERNAME=admin XR_LOGIN_PASSWORD=REDACTED bash scripts/e2e-alert-demo.sh
EOT
