# v0.50.4 real-data console runbook

Run browser blocks only from DevTools on the already signed-in, same-origin v0.50.4 Xirang page. Never paste the token, password, TOTP, step-up proof, Rsync path, file name, or preview content into chat or task evidence.

## A. Session-scoped API helper

This reads the existing session token into a closure. The global object exposes only the `api` function, not the token.

```js
(() => {
  const token = sessionStorage.getItem("xirang-auth-token");
  if (!token) throw new Error("当前页没有登录会话，请先正常登录线上息壤。");

  const api = async (path, init = {}) => {
    const headers = new Headers(init.headers || {});
    headers.set("Accept", "application/json");
    headers.set("Authorization", `Bearer ${token}`);
    if (init.body !== undefined) headers.set("Content-Type", "application/json");
    const response = await fetch(`/api/v1${path}`, {
      cache: "no-store",
      credentials: "same-origin",
      ...init,
      headers,
    });
    const payload = await response.json().catch(() => null);
    return {
      http: response.status,
      requestId: response.headers.get("X-Request-ID") || response.headers.get("X-Request-Id"),
      payload,
    };
  };

  Object.defineProperty(globalThis, "xrV0504", {
    configurable: true,
    value: Object.freeze({ api }),
  });
  console.log({ helper_ready: true, token_printed: false });
})();
```

Expected: `{ helper_ready: true, token_printed: false }`.

## B. Read-only candidate inventory

```js
(async () => {
  const { api } = globalThis.xrV0504 || {};
  if (!api) throw new Error("请先执行 A 段。");

  const [health, me, readiness, repositories, tasks] = await Promise.all([
    fetch("/healthz", { cache: "no-store", credentials: "same-origin" }),
    api("/me"),
    api("/settings/backup-assets/ga/readiness"),
    api("/backup-repositories?limit=200"),
    api("/tasks?page=1&page_size=100&sort=-id"),
  ]);

  const taskRows = Array.isArray(tasks.payload?.data) ? tasks.payload.data : [];
  const candidates = taskRows
    .filter((task) => String(task.executor_type).toLowerCase() === "rsync")
    .map((task) => ({
      task_id: task.id,
      node_id: task.node_id,
      enabled: task.enabled === true,
      task_status: task.status,
      verify_status: task.verify_status,
      last_run_at: task.last_run_at ?? null,
      local_absolute_target: typeof task.rsync_target === "string" && task.rsync_target.startsWith("/"),
      publication_mode: task.rsync_publication?.mode ?? null,
      publication_state: task.rsync_publication?.state ?? null,
      publication_reason: task.rsync_publication?.reason_code ?? null,
    }));

  const counts = readiness.payload?.data?.counts ?? {};
  const repoItems = Array.isArray(repositories.payload?.data?.items) ? repositories.payload.data.items : [];
  console.log({
    preflight_http: {
      healthz: health.status,
      me: me.http,
      readiness: readiness.http,
      repositories: repositories.http,
      tasks: tasks.http,
    },
    role: me.payload?.data?.user?.role ?? null,
    ga: {
      status: readiness.payload?.data?.status ?? null,
      inventory_complete: readiness.payload?.data?.inventory_complete === true,
      export_root_valid: readiness.payload?.data?.export_root_valid === true,
      key_domains_ready: readiness.payload?.data?.key_domains_ready === true,
      candidates: counts.candidates ?? null,
      conflicts: counts.conflicts ?? null,
      unsupported: counts.unsupported ?? null,
      capability_gaps: counts.capability_gaps ?? null,
    },
    repository_count: repoItems.length,
    rsync_candidate_count: candidates.length,
  });
  console.table(candidates);
})();
```

Expected before first connect: all HTTP 200, role `admin`, repository count 0, and at least one enabled Rsync row with `local_absolute_target=true` and the safe legacy projection `legacy_mutable / legacy / legacy`. The GA inventory's `no_repository` capability gap is a separate readiness classification and is not the Task response's publication reason.

## C. Lock one exact Task with a second read-only preflight

Enter only the numeric Task ID selected from B. This block does not write.

```js
(async () => {
  const token = sessionStorage.getItem("xirang-auth-token");
  if (!token) throw new Error("当前页没有登录会话，请先正常登录线上息壤。");
  const api = async (path, init = {}) => {
    const headers = new Headers(init.headers || {});
    headers.set("Accept", "application/json");
    headers.set("Authorization", `Bearer ${token}`);
    if (init.body !== undefined) headers.set("Content-Type", "application/json");
    const response = await fetch(`/api/v1${path}`, {
      cache: "no-store",
      credentials: "same-origin",
      ...init,
      headers,
    });
    const payload = await response.json().catch(() => null);
    return {
      http: response.status,
      requestId: response.headers.get("X-Request-ID") || response.headers.get("X-Request-Id"),
      payload,
    };
  };
  const raw = prompt("输入已批准的 Rsync task_id（仅数字）", "3");
  if (!/^[1-9][0-9]*$/.test(raw || "")) throw new Error("task_id 无效。");
  const taskId = Number(raw);
  if (!Number.isSafeInteger(taskId)) throw new Error("task_id 超出安全范围。");

  const [me, taskResult, runsResult, filesResult, repositories] = await Promise.all([
    api("/me"),
    api(`/tasks/${taskId}`),
    api(`/tasks/${taskId}/runs?page=1&page_size=20&sort_by=created_at&sort_order=desc`),
    api(`/tasks/${taskId}/backup-files?path=%2F`),
    api("/backup-repositories?limit=200"),
  ]);

  const task = taskResult.payload?.data ?? {};
  const runs = Array.isArray(runsResult.payload?.data) ? runsResult.payload.data : [];
  const entries = Array.isArray(filesResult.payload?.data?.entries) ? filesResult.payload.data.entries : [];
  const repoItems = Array.isArray(repositories.payload?.data?.items) ? repositories.payload.data.items : [];
  const activeStatuses = new Set(["pending", "running", "retrying"]);
  const activeRuns = runs.filter((run) => activeStatuses.has(run.status));
  const lastSuccess = runs.find((run) => run.status === "success") ?? null;
  const localTarget = typeof task.rsync_target === "string" && task.rsync_target.startsWith("/");
  const role = me.payload?.data?.user?.role ?? null;
  const publication = task.rsync_publication ?? {};
  const legacyPublication = publication.mode === "legacy_mutable" && publication.state === "legacy" && publication.reason_code === "legacy";

  const preflightOK =
    me.http === 200 && role === "admin" &&
    taskResult.http === 200 && runsResult.http === 200 && filesResult.http === 200 && repositories.http === 200 &&
    repoItems.length === 0 && String(task.executor_type).toLowerCase() === "rsync" && task.enabled === true &&
    task.status === "success" && localTarget && legacyPublication &&
    activeRuns.length === 0 && lastSuccess !== null && entries.length > 0;

  console.log({
    target_preflight_ok: preflightOK,
    task_id: taskId,
    node_id: task.node_id ?? null,
    role,
    executor_type: task.executor_type ?? null,
    enabled: task.enabled === true,
    task_status: task.status ?? null,
    verify_status: task.verify_status ?? null,
    local_absolute_target: localTarget,
    publication_mode: task.rsync_publication?.mode ?? null,
    publication_state: task.rsync_publication?.state ?? null,
    publication_reason: task.rsync_publication?.reason_code ?? null,
    active_run_count: activeRuns.length,
    recent_success_found: lastSuccess !== null,
    recent_success_finished_at: lastSuccess?.finished_at ?? null,
    backup_root_entry_count: entries.length,
    backup_root_truncated: filesResult.payload?.data?.truncated === true,
    repository_count: repoItems.length,
    request_ids: {
      task: taskResult.requestId,
      runs: runsResult.requestId,
      files: filesResult.requestId,
      repositories: repositories.requestId,
    },
  });

  if (!preflightOK) {
    delete globalThis.xrV0504Selection;
    throw new Error("只读预检未通过；不要执行 Connect。");
  }
  Object.defineProperty(globalThis, "xrV0504", {
    configurable: true,
    value: Object.freeze({ api }),
  });
  Object.defineProperty(globalThis, "xrV0504Selection", {
    configurable: true,
    value: Object.freeze({ taskId }),
  });
})();
```

Expected: `target_preflight_ok=true`, active run count 0, recent success found, backup root entry count greater than 0, repository count 0.

## D. One authorized Connect write

Run only after C passes and the exact Task ID has been reviewed. The block re-runs the critical preflight immediately before displaying the confirmation prompt. It sends exactly one Connect request and never retries.

```js
(async () => {
  const { api } = globalThis.xrV0504;
  const taskId = globalThis.xrV0504Selection?.taskId;
  if (!api || taskId !== 3) throw new Error("缺少已通过且精确锁定 task_id=3 的 C 段状态。");

  const [health, me, taskResult, runsResult, filesResult, repositories] = await Promise.all([
    fetch("/healthz", { cache: "no-store", credentials: "same-origin" }),
    api("/me"),
    api(`/tasks/${taskId}`),
    api(`/tasks/${taskId}/runs?page=1&page_size=20&sort_by=created_at&sort_order=desc`),
    api(`/tasks/${taskId}/backup-files?path=%2F`),
    api("/backup-repositories?limit=200"),
  ]);

  const task = taskResult.payload?.data ?? {};
  const runs = Array.isArray(runsResult.payload?.data) ? runsResult.payload.data : [];
  const entries = Array.isArray(filesResult.payload?.data?.entries) ? filesResult.payload.data.entries : [];
  const repoItems = Array.isArray(repositories.payload?.data?.items) ? repositories.payload.data.items : [];
  const active = runs.some((run) => ["pending", "running", "retrying"].includes(run.status));
  const role = me.payload?.data?.user?.role ?? null;
  const publication = task.rsync_publication ?? {};
  const legacyPublication = publication.mode === "legacy_mutable" && publication.state === "legacy" && publication.reason_code === "legacy";
  const guard = health.status === 200 && me.http === 200 && role === "admin" &&
    taskResult.http === 200 && runsResult.http === 200 && filesResult.http === 200 && repositories.http === 200 &&
    repoItems.length === 0 && String(task.executor_type).toLowerCase() === "rsync" && task.enabled === true &&
    task.status === "success" && typeof task.rsync_target === "string" && task.rsync_target.startsWith("/") &&
    legacyPublication && !active &&
    runs.some((run) => run.status === "success") && entries.length > 0;
  if (!guard) throw new Error("写入前状态已漂移；Connect 已阻止。");
  if (!confirm(`即将通过正式 API 单次接入 Rsync task_id=${taskId}。确认继续？`)) {
    throw new Error("用户取消；未写入。");
  }

  const connected = await api("/backup-repositories/connect", {
    method: "POST",
    body: JSON.stringify({ task_id: taskId }),
  });
  const repository = connected.payload?.data?.Repository ?? connected.payload?.data?.repository ?? null;
  const mutablePoint = connected.payload?.data?.MutablePoint ?? connected.payload?.data?.mutable_point ?? null;
  console.log({
    connect_http: connected.http,
    connect_api_code: connected.payload?.code ?? null,
    connect_request_id: connected.requestId,
    repository_id: repository?.id ?? null,
    recovery_point_id: mutablePoint?.id ?? null,
    provider_kind: repository?.provider_kind ?? null,
    repository_status: repository?.status ?? null,
    recovery_point_semantics: mutablePoint?.semantics ?? null,
    recovery_point_state: mutablePoint?.state ?? null,
  });
  if (connected.http !== 200) throw new Error("Connect 非 200；不要自动重试。");
  if (!/^[0-9a-f]{32}$/.test(repository?.id || "")) {
    throw new Error("Connect 200 但 repository ID 无效；停止并保留 request ID，不执行其他写入。");
  }
  const repositoryId = repository.id;
  const recoveryPointId = mutablePoint?.id ?? "";
  Object.defineProperty(globalThis, "xrV0504Connected", {
    configurable: true,
    value: Object.freeze({ taskId, repositoryId, recoveryPointId }),
  });
  if (!/^[0-9a-f]{32}$/.test(recoveryPointId)) {
    throw new Error("Connect 200 但 recovery point ID 无效；精确 repository ID 已保留，可评估 G 段回退。");
  }

  const [detail, points] = await Promise.all([
    api(`/backup-repositories/${repositoryId}`),
    api(`/backup-repositories/${repositoryId}/recovery-points?limit=200&sort=created_desc`),
  ]);
  const lineages = Array.isArray(detail.payload?.data?.lineages) ? detail.payload.data.lineages : [];
  const pointItems = Array.isArray(points.payload?.data?.items) ? points.payload.data.items : [];
  const activeLink = lineages.find((item) => item.source === "task_link" && item.task_id === taskId && item.active === true) ?? null;
  const listedPoint = pointItems.find((item) => item.id === recoveryPointId) ?? null;
  const postconditionOK = detail.http === 200 && points.http === 200 &&
    detail.payload?.data?.provider_kind === "rsync" && detail.payload?.data?.status === "online" &&
    detail.payload?.data?.access_active === true && activeLink?.publication_mode === "legacy_mutable" &&
    listedPoint?.semantics === "mutable_head" && listedPoint?.state === "observed" &&
    listedPoint?.lineage?.producing_task_id === taskId;

  console.log({
    connect_postcondition_ok: postconditionOK,
    repository_http: detail.http,
    repository_status: detail.payload?.data?.status ?? null,
    access_active: detail.payload?.data?.access_active === true,
    active_task_link: activeLink === null ? null : {
      task_repository_link_id: activeLink.task_repository_link_id ?? null,
      task_id: activeLink.task_id,
      node_id: activeLink.node_id ?? null,
      publication_mode: activeLink.publication_mode ?? null,
      active: activeLink.active === true,
    },
    recovery_point_http: points.http,
    recovery_point_count: pointItems.length,
    selected_point: listedPoint === null ? null : {
      id: listedPoint.id,
      semantics: listedPoint.semantics,
      state: listedPoint.state,
      producing_task_id: listedPoint.lineage?.producing_task_id ?? null,
    },
  });

  if (!postconditionOK) throw new Error("Connect 已提交，但后置证据未通过；不要继续写入，评估 G 段回退。");
})();
```

## E. Condition-poll Catalog and Search

This performs reads only. Catalog polling allows 17.5 minutes; search polling allows 95 seconds after Catalog completion.

```js
(async () => {
  const { api } = globalThis.xrV0504 || {};
  const state = globalThis.xrV0504Connected;
  if (!api || state?.taskId !== 3 ||
      state?.repositoryId !== "0d7d7b3098bdad32426a0807b2a8ee42" ||
      state?.recoveryPointId !== "e35fca267e10c228ee6858dcadb787ad") {
    throw new Error("缺少已验证且精确锁定的 D 段状态。");
  }
  const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
  let catalog = null;

  for (let attempt = 1; attempt <= 70; attempt += 1) {
    const result = await api(`/recovery-points/${state.recoveryPointId}/catalog-status`);
    const data = result.payload?.data ?? {};
    const snapshot = {
      attempt,
      http: result.http,
      request_id: result.requestId,
      generation_state: data.generation?.state ?? null,
      latest_build_state: data.latest_build?.state ?? null,
      latest_build_error_code: data.latest_build?.error_code ?? null,
      coverage: data.coverage?.status ?? null,
      indexed_entries: data.coverage?.indexed_entries ?? 0,
      expected_entries: data.coverage?.expected_entries ?? null,
      content_available: data.content_availability?.available === true,
      list_permitted: data.permissions?.list === true,
    };
    if (attempt === 1 || attempt % 4 === 0 || snapshot.coverage === "complete" || snapshot.latest_build_state === "failed") {
      console.log({ catalog_poll: snapshot });
    }
    if (snapshot.latest_build_state === "failed") throw new Error("Catalog build failed；记录稳定 error code 后停止。");
    if (result.http === 200 && snapshot.generation_state === "complete" && snapshot.coverage === "complete" &&
        snapshot.indexed_entries > 0 && snapshot.content_available && snapshot.list_permitted) {
      catalog = snapshot;
      break;
    }
    await sleep(15000);
  }
  if (catalog === null) throw new Error("Catalog 在允许窗口内未达到完成条件；不要重启催化。");

  const body = JSON.stringify({
    query: {
      schema_version: 1,
      root: { op: "type", values: ["file"] },
      scope: { mode: "exact_points", recovery_point_ids: [state.recoveryPointId] },
      sort: "name_asc",
      limit: 25,
    },
  });
  let search = null;
  for (let attempt = 1; attempt <= 19; attempt += 1) {
    const result = await api("/asset-search", { method: "POST", body });
    const data = result.payload?.data ?? {};
    const items = Array.isArray(data.items) ? data.items : [];
    const indexes = Array.isArray(data.indexes) ? data.indexes : [];
    const snapshot = {
      attempt,
      http: result.http,
      request_id: result.requestId,
      coverage: data.coverage?.status ?? null,
      index_coverage: indexes[0]?.coverage ?? null,
      index_staleness: indexes[0]?.staleness ?? null,
      result_count: items.length,
      total: data.total ?? null,
      total_relation: data.total_relation ?? null,
      authoritative_empty: data.authoritative_empty ?? null,
      metadata_capable: data.capabilities?.metadata === true,
      list_permitted: data.permissions?.list === true,
    };
    console.log({ search_poll: snapshot });
    if (result.http === 200 && ["complete", "partial"].includes(snapshot.coverage) &&
        ["complete", "partial"].includes(snapshot.index_coverage) && snapshot.index_staleness === "fresh" &&
        snapshot.metadata_capable && snapshot.list_permitted && items.length > 0) {
      search = { result, first: items[0], snapshot };
      break;
    }
    await sleep(5000);
  }
  if (search === null) throw new Error("Search 在允许窗口内未返回真实 file；停止验收。");

  const ref = search.first.ref ?? {};
  if (ref.recovery_point_id !== state.recoveryPointId || !/^[0-9a-f]{64}$/.test(ref.entry_id || "") ||
      search.first.asset?.entry_type !== "file") {
    throw new Error("Search 返回的 AssetRef 无效。");
  }
  const params = new URLSearchParams({
    view: "search",
    repositoryId: state.repositoryId,
    taskId: String(state.taskId),
    recoveryPointId: state.recoveryPointId,
    entryId: ref.entry_id,
  });
  const uiRoute = `/app/backups/data?${params.toString()}`;
  console.log({
    real_asset_search_ok: true,
    search_http: search.snapshot.http,
    repository_id: state.repositoryId,
    recovery_point_id: state.recoveryPointId,
    entry_id: ref.entry_id,
    entry_metadata: {
      entry_type: search.first.asset?.entry_type ?? null,
      size: search.first.asset?.size ?? null,
      mime_type: search.first.asset?.mime_type ?? null,
      modified_at: search.first.asset?.modified_at ?? null,
    },
    ui_route: uiRoute,
  });
  Object.defineProperty(globalThis, "xrV0504Asset", {
    configurable: true,
    value: Object.freeze({ ...state, entryId: ref.entry_id, uiRoute }),
  });
})();
```

Expected: Catalog complete with positive indexed entries and content available; search HTTP 200 with at least one file and a 64-hex entry ID.

### E1. One-shot durable failure projection

Run this only after E has stopped on `latest_build.state=failed`. It performs exactly one GET against the already connected recovery point, prints a flat allow-listed JSON string so the error code cannot be hidden by a collapsed console object, and does not trigger a build, Search, restart, retry, or Disconnect.

```js
(async () => {
  const repositoryId = "0d7d7b3098bdad32426a0807b2a8ee42";
  const recoveryPointId = "e35fca267e10c228ee6858dcadb787ad";
  const token = sessionStorage.getItem("xirang-auth-token");
  if (!token) throw new Error("当前页没有登录会话，请先正常登录线上息壤。");

  const response = await fetch(
    `/api/v1/recovery-points/${recoveryPointId}/catalog-status`,
    {
      method: "GET",
      cache: "no-store",
      credentials: "same-origin",
      headers: {
        Accept: "application/json",
        Authorization: `Bearer ${token}`,
      },
    },
  );
  const requestId =
    response.headers.get("X-Request-ID") ||
    response.headers.get("X-Request-Id");
  const payload = await response.json().catch(() => null);
  const data = payload?.data ?? {};
  const latest = data.latest_build ?? {};
  const evidence = {
    diagnostic: "catalog_failure_status",
    http: response.status,
    request_id: requestId,
    repository_id: repositoryId,
    recovery_point_id: recoveryPointId,
    generation_state: data.generation?.state ?? null,
    latest_build_id: latest.id ?? null,
    latest_build_sequence: latest.sequence ?? null,
    latest_build_state: latest.state ?? null,
    latest_build_error_code: latest.error_code ?? null,
    latest_build_correlation_id: latest.correlation_id ?? null,
    latest_build_started_at: latest.started_at ?? null,
    latest_build_finished_at: latest.finished_at ?? null,
    coverage: data.coverage?.status ?? null,
    indexed_entries: data.coverage?.indexed_entries ?? 0,
    expected_entries: data.coverage?.expected_entries ?? null,
    content_available: data.content_availability?.available === true,
    list_permitted: data.permissions?.list === true,
  };

  console.log(
    `XR_CATALOG_FAILURE_EVIDENCE\n${JSON.stringify(evidence, null, 2)}`,
  );

  if (
    response.status !== 200 ||
    latest.state !== "failed" ||
    typeof latest.error_code !== "string" ||
    latest.error_code.length === 0
  ) {
    throw new Error("未取得稳定 Catalog failure code；停止，不执行其他动作。");
  }
})();
```

Expected: one `XR_CATALOG_FAILURE_EVIDENCE` JSON string with HTTP 200, `latest_build_state: "failed"`, and a non-empty `latest_build_error_code`. Return only that printed JSON; it contains no token, locator, path, file name, or raw error text.

### E2. One-shot automatic-retry observation

After the next normal 15-minute Catalog scan window, run this once. It performs four GETs and projects only stable status, timing, counts, and request IDs. It does not request or trigger a Catalog retry.

```js
(async () => {
  const { api } = globalThis.xrV0504 || {};
  const repositoryId = "0d7d7b3098bdad32426a0807b2a8ee42";
  const recoveryPointId = "e35fca267e10c228ee6858dcadb787ad";
  if (!api) throw new Error("原标签页会话状态已丢失；停止，不执行其他动作。");

  const [catalog, repository, task, runs] = await Promise.all([
    api(`/recovery-points/${recoveryPointId}/catalog-status`),
    api(`/backup-repositories/${repositoryId}`),
    api("/tasks/3"),
    api("/tasks/3/runs?page=1&page_size=20&sort_by=created_at&sort_order=desc"),
  ]);
  const status = catalog.payload?.data ?? {};
  const generation = status.generation ?? {};
  const latest = status.latest_build ?? {};
  const runRows = Array.isArray(runs.payload?.data) ? runs.payload.data : [];
  const activeRuns = runRows.filter((run) =>
    ["pending", "running", "retrying"].includes(run.status),
  );
  const newestSuccess =
    runRows.find((run) => run.status === "success") ?? null;

  const evidence = {
    diagnostic: "catalog_automatic_retry_observation",
    observed_at_utc: new Date().toISOString(),
    catalog_http: catalog.http,
    catalog_request_id: catalog.requestId,
    generation_id: generation.id ?? null,
    generation_sequence: generation.sequence ?? null,
    generation_state: generation.state ?? null,
    generation_finished_at: generation.finished_at ?? null,
    latest_build_id: latest.id ?? null,
    latest_build_sequence: latest.sequence ?? null,
    latest_build_state: latest.state ?? null,
    latest_build_error_code: latest.error_code ?? null,
    latest_build_started_at: latest.started_at ?? null,
    latest_build_finished_at: latest.finished_at ?? null,
    coverage: status.coverage?.status ?? null,
    indexed_entries: status.coverage?.indexed_entries ?? 0,
    expected_entries: status.coverage?.expected_entries ?? null,
    content_available: status.content_availability?.available === true,
    list_permitted: status.permissions?.list === true,
    repository_http: repository.http,
    repository_request_id: repository.requestId,
    repository_status: repository.payload?.data?.status ?? null,
    repository_access_active:
      repository.payload?.data?.access_active === true,
    task_http: task.http,
    task_request_id: task.requestId,
    task_status: task.payload?.data?.status ?? null,
    task_last_run_at: task.payload?.data?.last_run_at ?? null,
    runs_http: runs.http,
    runs_request_id: runs.requestId,
    active_run_count: activeRuns.length,
    newest_success_finished_at: newestSuccess?.finished_at ?? null,
  };

  console.log(
    `XR_CATALOG_RETRY_OBSERVATION\n${JSON.stringify(evidence, null, 2)}`,
  );
})();
```

Expected outcomes are either an active `complete` generation with positive indexed entries, or a newer failed build whose stable code and timing can be compared with sequence 1. Return only the printed JSON.

## F. UI and final read-only checks

Open the `ui_route` printed by E. Verify the selected asset shows metadata and the Preview tab renders content with the product-selected renderer. If the UI classifies it as `secret` or `unknown`, complete the normal step-up only inside the UI. Report only:

```text
metadata_visible=true
preview_visible=true
preview_renderer=one of escaped_text, safe_raster, same_origin_pdf, native_audio, native_video, metadata_hex
step_up_required=true or false
```

Do not share the asset name, path, content, password, TOTP, or proof.

Then return to the original DevTools tab (where A is still defined) and check all node-log collectors remain disabled without printing log paths:

```js
(async () => {
  const { api } = globalThis.xrV0504;
  if (!api) throw new Error("请在仍保留 A 段状态的原标签页执行。");
  const [health, nodes] = await Promise.all([
    fetch("/healthz", { cache: "no-store", credentials: "same-origin" }),
    api("/nodes"),
  ]);
  const nodeRows = Array.isArray(nodes.payload?.data) ? nodes.payload.data : [];
  const configs = await Promise.all(nodeRows.map((node) => api(`/nodes/${node.id}/log-config`)));
  const enabled = configs.filter((result) => {
    const data = result.payload?.data ?? {};
    return data.log_journalctl_enabled === true || (Array.isArray(data.log_paths) && data.log_paths.length > 0);
  });
  console.log({
    healthz_http: health.status,
    nodes_http: nodes.http,
    node_count: nodeRows.length,
    log_config_http_all_200: configs.every((result) => result.http === 200),
    remaining_collectors: enabled.length,
  });
})();
```

Expected: health 200, all config reads 200, `remaining_collectors=0`.

On the production host, inspect only the selected v0.50.4 container and emit counts, not raw logs:

```bash
XR_CONTAINER_IDS="$(docker ps --filter 'name=^/xirang$' --filter 'ancestor=linnea7171/xirang:v0.50.4' --format '{{.ID}}')"
test "$(printf '%s\n' "$XR_CONTAINER_IDS" | sed '/^$/d' | wc -l)" -eq 1
XR_CONTAINER_ID="$XR_CONTAINER_IDS"
docker inspect --format 'status={{.State.Status}} health={{if .State.Health}}{{.State.Health.Status}}{{end}} restarts={{.RestartCount}} image={{.Config.Image}}' "$XR_CONTAINER_ID"
XR_CRITICAL_COUNT="$(docker logs --since 30m "$XR_CONTAINER_ID" 2>&1 | grep -Eci '"level":"(error|fatal|panic)"|backup[_ -]?asset.*(failed|error|critical)|queue_full|fetch_failed' || true)"
printf 'critical_matches=%s\n' "$XR_CRITICAL_COUNT"
```

Expected: running, healthy, restarts 0, exact v0.50.4 image, `critical_matches=0`.

## G. Failure-only rollback

Do not run after successful acceptance. Run only after a failed/aborted acceptance when D returned HTTP 200 and the exact repository ID is present in `xrV0504Connected`.

```js
(async () => {
  const { api } = globalThis.xrV0504;
  const state = globalThis.xrV0504Connected;
  if (!api || !/^[0-9a-f]{32}$/.test(state?.repositoryId || "")) throw new Error("没有可回退的精确仓库。");
  const detail = await api(`/backup-repositories/${state.repositoryId}`);
  console.log({
    rollback_preflight_http: detail.http,
    repository_id: state.repositoryId,
    repository_status: detail.payload?.data?.status ?? null,
    access_active: detail.payload?.data?.access_active === true,
  });
  if (detail.http !== 200 || detail.payload?.data?.access_active !== true) throw new Error("回退只读预检未通过。");
  if (!confirm(`将撤销 repository_id=${state.repositoryId} 的访问授权与活动关联；不会删除备份文件。确认？`)) {
    throw new Error("用户取消；未写入。");
  }
  const disconnected = await api(`/backup-repositories/${state.repositoryId}/disconnect`, { method: "POST" });
  const repository = disconnected.payload?.data?.Repository ?? disconnected.payload?.data?.repository ?? null;
  console.log({
    disconnect_http: disconnected.http,
    disconnect_api_code: disconnected.payload?.code ?? null,
    disconnect_request_id: disconnected.requestId,
    repository_id: repository?.id ?? state.repositoryId,
    repository_status: repository?.status ?? null,
  });
  if (disconnected.http !== 200) throw new Error("Disconnect 非 200；不要自动重试。");
})();
```
