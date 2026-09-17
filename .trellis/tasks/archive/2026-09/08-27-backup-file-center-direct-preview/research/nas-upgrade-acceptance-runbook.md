# NAS upgrade, rollback, and usable-preview acceptance runbook

Status: prepared; target release tag, manifest digest, and revision are filled
only after the merged release and official multi-architecture image have passed.

This runbook intentionally contains no credential, token, proof, Provider
locator, backup path, repository/recovery-point/entry identifier, asset name, or
asset content. Production evidence records only bounded status values and
operator acceptance booleans.

## Fixed deployment boundary

- Operator context: root, already positioned at `/volume2/docker/xirang`.
- Compose file: `/volume2/docker/xirang/docker-compose.yaml`.
- Current rollback image: `linnea7171/xirang:v0.51.0`.
- Target image: `linnea7171/xirang:<released-stable-tag>`; never `latest`.
- External health entry: `http://127.0.0.1:19927/healthz`.
- Container health entry: `http://127.0.0.1:10761/healthz`.
- Expected schema: `72:0`; this task contains no model or migration change.
- Expected active task runs: `0`.
- Expected node-log collectors: `0` before, during, and after acceptance.
- The live command blocks must not invoke the shell builtins `test`, `[` or
  `[[`, change directories, switch users, or elevate identity. Use `case`,
  exact counts, and explicit absolute targets instead.

## Release facts required before any NAS write

Record these public values from completed GitHub/Docker workflows:

1. stable GitHub Release tag;
2. merged revision;
3. official `linnea7171/xirang:<tag>` manifest-list digest;
4. successful amd64 and arm64 build, scan, provenance, and tag publication;
5. equality of the stable `v<tag>`, `<tag>`, and `latest` public digests.

If any value is missing or inconsistent, stop before contacting the NAS.

## Stage A — read-only preflight

Issue one bounded command block that derives and prints only these facts:

- root UID equals zero;
- Compose parses and resolves exactly one current rollback image;
- the compose file contains exactly one old tag and no target tag;
- container `xirang` uses the rollback image and is running, healthy, with zero
  restarts;
- external and internal health endpoints both return 200;
- schema state is exactly `72:0` and SQLite integrity is `ok`;
- `backup_assets.enabled` is true;
- active `pending|running|retrying` task-run count is zero;
- node-log collector count is zero;
- `/volume2/docker/xirang` has at least 1 GiB available;
- the old local image still has a RepoDigest and therefore remains a usable
  rollback target;
- the target manifest is publicly reachable.

The block collapses these into one exact preflight key. A mismatch prints only
the bounded fields above and stops; it does not query or print repository,
recovery-point, entry, path, or content identity.

## Stage B — pull and verify the target image

Run a timed pull of the exact stable target tag without changing Compose or the
running container. Verify:

- pull exit is zero;
- the local RepoDigest equals the published manifest-list digest exactly once;
- OCI revision and version labels equal the merged revision and release version;
- the running container remains on `v0.51.0`, healthy, with zero restarts.

Only the exit, digest-match count, public revision/version labels, and bounded
container state are returned.

## Stage C — create and verify a consistent database backup

Use the image-provided `/usr/local/bin/backup-db.sh /backup/db` entrypoint. Count
matching backup files before and after, require a delta of one, validate the
newest sidecar checksum, and read only its `schema_migrations` state. The exact
gate is:

`backup exit 0 : file delta 1 : checksum exit 0 : schema 72:0`.

Do not print the generated backup filename. A failed backup stops the upgrade;
it never modifies Compose or the running container.

## Stage D — snapshot and validate the Compose edit

1. Revalidate Stage A's current-image/container facts plus Stage B's digest and
   Stage C's backup key.
2. Copy the exact Compose file to a timestamped sibling snapshot using `cp -p`.
3. Replace only the single anchored Core image line from `v0.51.0` to the exact
   stable target tag.
4. Require old-tag count zero, new-tag count one, Compose parse exit zero, and
   one resolved target image.
5. If any assertion fails, restore the snapshot immediately and stop before a
   container rebuild.

The existing container must still report `v0.51.0|running|healthy|0` at the end
of this stage.

## Stage E — rebuild with automatic rollback

Record an upgrade UTC timestamp, then run a bounded
`docker compose -f /volume2/docker/xirang/docker-compose.yaml up -d xirang`.
Wait at most 90 seconds for container health.

The success key requires all of:

- Compose exit zero and health wait exit zero;
- configured image equals the exact target tag;
- container status `running`, health `healthy`, restart count zero;
- running image ID equals the pulled target image ID;
- external 19927 and internal 10761 health both return 200;
- schema remains exactly `72:0`.

On any mismatch:

1. print only the bounded success-key values;
2. restore the Compose snapshot;
3. require restored Compose parse success, exactly one resolved rollback image,
   and unchanged schema `72:0`;
4. rebuild only service `xirang` and wait for healthy;
5. report rollback exits plus bounded image/status/health/restart state;
6. stop. Never retry the failed upgrade automatically and never restore the
   database unless a separately diagnosed migration/data failure explicitly
   requires it.

## Stage F — post-upgrade operational acceptance

Before opening a browser, prove:

- target image, running/healthy state, zero restarts, and both health endpoints;
- schema `72:0` and SQLite integrity `ok`;
- `backup_assets.enabled=true`;
- active task runs remain zero;
- collectors remain zero;
- bounded counts for panic/fatal/migration/error markers since the upgrade
  timestamp are zero. Never return raw log lines.

Any failure invokes the Stage E Compose rollback while schema is still `72:0`.

## Stage G — authorized LAN HTTP product acceptance

Use the existing authenticated browser session over the authorized LAN HTTP
origin. Do not copy filenames, paths, content, tickets, URLs, or screenshots
into evidence. Record only the following booleans:

1. `generic_text_readable=true`: one representative generic-MIME YAML/config,
   JSON/TOML/log/code/text asset renders faithful readable text, not hex.
2. `true_binary_non_text=true`: one real binary remains hex or a bounded typed
   unsupported result and is never decoded as text.
3. `native_safe_render=true`: one signature-proven image, PDF, audio, or video
   still uses its safe native renderer.
4. `single_activation_preview=true`: one pointer activation issues and displays
   preview without a second Load/Refresh action.
5. `rapid_switch_stale_free=true`: rapid A-to-B switching clears A immediately,
   cancels/ignores its late completion, and never flashes A after B owns preview.
6. `typed_errors_bounded=true`: a denied/unsupported case shows localized,
   retry-appropriate UI without raw server text or private parameters.
7. `desktop_mobile_a11y_ok=true`: desktop split/focused reading and mobile Back
   restore the expected focus/origin; keyboard activation and checkbox selection
   remain distinct.

After the browser pass, repeat Stage F. Final acceptance requires every boolean
true, clean health/DB/log facts, and `collectors=0`.

## Stop boundary

Node-log P1 must not start from this runbook. A completed usable-content preview
acceptance only makes that separate task eligible for its own explicit approval;
it does not authorize collectors or any node-log deployment.
