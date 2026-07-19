# Child 9 Browser And Visual Verification

## 1. Controlled environment

- Vite URL: `http://127.0.0.1:4174`
- Browser: headless Chromium through CDP on local port 9223.
- Route under test: the real protected `/app/backups/...` router and AppShell,
  not an isolated component render.
- Data: committed synthetic API fixture plus a temporary uncommitted CDP Fetch
  interceptor. No real asset, hostname, credential, proof, ticket, or user
  profile was used.
- Synthetic content plane: one opaque
  `/api/v1/asset-content/<32-hex>` URL fulfilled with bounded text.
- Temporary screenshots remain under `/tmp/xirang-child9-visual/` and are not
  product artifacts.

## 2. Viewport evidence

| Viewport | Route/flow and measured result | Reviewed screenshot |
|---|---|---|
| 1440x900 desktop | Routed exact repository/RP/entry; workspace 1136x672; stable tracks 288/432/416; 21 virtual DOM options represent 128 rows; fixed 288 px preview viewport; opaque sandboxed frame loaded; body width 1440/1440. | `/tmp/xirang-child9-visual/final-verification-desktop-1440x900.png` |
| 1440x900 preference desktop | Stored grid + 320/480 preferences with route layout omitted canonicalized to `layout=grid`; the constrained 1136 px workspace measured 320/420/396 tracks with no workspace or body overflow. | `/tmp/xirang-child9-visual/final-preferences-desktop-1440x900.png` |
| 1200x900 intermediate | Exact selected entry becomes a continuous 896 px inspector rather than compressed/clipped tracks; inspector right edge equals workspace right edge; body width 1200/1200. | `/tmp/xirang-child9-visual/final-verification-intermediate-1200x900.png` |
| 900x900 intermediate | Context Dialog, result flow, and continuous inspector were reviewed; no three-column compression or portal overlap. | `/tmp/xirang-child9-visual/intermediate-context-dialog-900x900.png`, `/tmp/xirang-child9-visual/intermediate-inspector-900x900.png` |
| 390x844 mobile | Full inspector is 358 px wide inside a 390 px viewport; tabs and fixed actions fit without page overflow; long asset name truncates without colliding with previous/next/close controls. | `/tmp/xirang-child9-visual/final-verification-mobile-390x844.png` |

Additional reviewed scenes covered overview parity, list/grid, saved-search
overlay, evidence/diff, recovery context, English dark theme, and Chinese light
theme. Representative files include
`desktop-overview-final-1440x900.png`,
`desktop-saved-search-overlay-1440x900.png`,
`desktop-dark-diff-1440x900.png`, and
`mobile-zh-light-390x844.png`.

## 3. Interaction and focus checks

- Desktop deep link selected the exact composite asset while preserving
  repository/task/RP context and rendered 128 ordered results through a bounded
  virtual DOM.
- Starting without a layout query hydrated the stored grid preference and
  canonical-replaced only `layout=grid`. Switching list then grid updated the
  route and the one versioned preference record without issuing an API request.
- Loading preview issued one exact delivery-ticket request, then the native
  frame consumed only the opaque same-origin content URL. The frame remained
  sandboxed, bounded, and non-overlapping.
- Inspector ArrowRight changed the route to `inspectorTab=metadata`; after the
  controlled render, focus was
  `backup-assets-inspector-tab-metadata`, `aria-selected=true`, and its panel
  was visible.
- At 390 px, closing the inspector removed only `entryId`, preserved the other
  route context, restored focus to the exact result option, and retained the
  result scroll anchor.
- Opening the mobile Context Dialog moved focus into the portal. Closing it
  returned focus to the `打开资产上下文` trigger.
- Previous/next, close, toolbar, tab, and portal controls retained stable
  bounds; long multilingual names and status text did not overlap.
- Light/dark representative scenes and color-independent text/icon status were
  manually reviewed. No new color tokens were introduced. Unit axe tests cover
  semantic violations; jsdom's documented contrast exemption remains unchanged.

## 4. Console and network result

A multi-navigation report contained one canceled `Document` request caused by
deliberately replacing the page during viewport navigation; it was marked
`canceled=true` and was not an API/content failure. After the final page was
stable, events were cleared and preview was loaded again. The final functional
report contained:

```text
console:       0
exceptions:    0
failed:        0
unhandled API: 0
bad responses: 0
API requests:  delivery-ticket POST + opaque content GET
```

For the final preference/route-only interaction, events were cleared before
the list/grid toggles. Its independent report contained:

```text
console:       0
exceptions:    0
failed:        0
unhandled API: 0
bad responses: 0
API requests:  0
```

No blank scene, horizontal page overflow, clipped inspector, portal stacking
error, unhandled route/API request, or preview overlap remained after the two
responsive/focus regression fixes.

## 5. Accessible local handoff

While the validation processes remain active, the synthetic route is available
at:

`http://127.0.0.1:4174/app/backups/data?repositoryId=11111111111111111111111111111111&taskId=71&recoveryPointId=33333333333333333333333333333333&entryId=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa&layout=grid`

This URL contains only opaque IDs and a non-sensitive positive task ID. It does
not contain query text, path/name, ticket URL, proof, reason, or bulk identity.
