# Design

## Work ownership
Task backend owns task service/manager/scheduler/cronutil/task_handler and its direct test contracts. Auth/discovery owns auth/middleware/docker handler. Restic owns legacy password creator, every password creation caller and Restic format semantics. Rsync containment owns executor.go, dedicated confinement package/helper command, policy execution integration, deployment. Frontend is integrated after task response/update contract is fixed; generated Swagger belongs to main.

## Shared contracts
- Cron parsing stays five fields plus current robfig descriptors. Only typed invalid-configuration errors may be skipped during startup reconciliation.
- Task editing uses an update-specific presence-aware request. Omitted fields retain; explicit empty cron clears; policy/dependency null unlinks. No creation hydration on ordinary edits. Resolve/merge against the transaction-locked fresh row. Safe settings are whitelisted; secrets expose configured status only. Use exact decimal string UpdatedAt UnixNano revision consistent with existing task CAS, with stale revision returning 409. Migrate all direct callers/tests; do not preserve obsolete alternate semantics.
- Restic settings projection distinguishes repository format selection from protection. No boolean establishes immutable status; legacy append_only requires explicit transition to truthful format semantics, without claiming backend verification or adding a pretend storage integration.
- Rsync allowlists are filesystem boundaries, not textual prefixes. Shared lexical validation is usable by task/import/policy and execution. Enforcement belongs in the filesystem that resolves the path and must remain active during transfer and destination creation. A versioned one-shot helper is allowed; it is not a daemon. Empty configuration preserves unrestricted behavior; missing capability with configured restrictions is an error. Design/test remote invocation, privilege and runtime-file allowances; never claim realpath preflight closes races.
- Secret creation uses controlled stdin and unchanged private-file ownership/cleanup contracts; shared SSH runner changes have one writer only.
- Discovery uses existing bounded SSH runner, one total operation context and explicit resource limits. Partial/failure outcomes remain identifiable.

## Integration
No overlapping file writers. Workers send integration requests to owner/main. Main adjudicates evidence, performs actual-surface acceptance, generates docs, and owns independent dual-review gate. No production or release actions.
