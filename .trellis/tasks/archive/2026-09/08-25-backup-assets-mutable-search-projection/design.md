# Design — Mutable Catalog Search Projection

## Root cause

Catalog generations obtain `ExpectedEntryCount` only from a durable manifest.
Rsync mutable heads intentionally have no manifest, so a successful enumeration
stores `ExpectedEntryCount=0` and the real `WrittenEntryCount`. Search currently
rejects every count mismatch in `loadFrozenProjection` before lease acquisition
or Search-generation creation. The worker retries the same eligible candidate
each minute, while the API correctly reports unavailable/non-authoritative
coverage.

## Selected change

Keep the current worker, key, lease, staging, activation, and API contracts. Add
one package-local eligibility predicate for the Catalog count proof:

- manifest-less `mutable_head`: accept a non-negative written count without an
  expected-count equality claim;
- every manifest-backed generation and every non-mutable point: retain exact
  written/expected equality.

Use that predicate both when freezing the projection and when activation
re-locks the point/Catalog. `beginGeneration` continues to freeze
`ExpectedDocumentCount` from `WrittenEntryCount`; projection streams every exact
Catalog row, and activation still requires physical Search document count,
written document count, and expected document count to match. This makes the
Catalog's completed written proof the mutable input without weakening Search's
own completeness proof.

## Failure and privacy boundaries

- Source/semantics/state/key/fence/count drift returns the existing typed Search
  error and cannot activate staging data.
- No schema/API/settings/frontend changes are needed.
- No Provider command or byte mutation is introduced.
- Tests and evidence use only opaque IDs and counts; production commands never
  print filenames, paths, locators, tokens, or content.

## Verification and rollout

TDD starts with a real SQLite mutable fixture that has two Catalog rows,
`ManifestID=nil`, `ExpectedEntryCount=0`, and `WrittenEntryCount=2`. Preserve a
manifest-backed mismatch negative control and activation drift/count tests.
Run focused repetition/race and the Search package, then independent
`trellis-check`. Deliver through one PR and required CI. Because this production
bug needs a new image, monitor Release Please, GitHub Release, multi-arch Docker
publish, and Docker Hub before the guarded production upgrade. Task 3 remains
paused throughout the release and acceptance.
