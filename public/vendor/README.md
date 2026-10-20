# Vendored panel assets

The admin panel loads **nothing** from a third-party origin. Everything it needs is
in this directory, which is why `middleware.go`'s `adminPanelCSP` can list no remote
source and no `'unsafe-inline'`.

| File | Version | License | Upstream |
|---|---|---|---|
| `vue.global.prod.js` | 3.5.42 | MIT | `https://unpkg.com/vue@3.5.42/dist/vue.global.prod.js` |
| `lucide.min.js` | 1.46.0 | ISC | `https://unpkg.com/lucide@1.46.0/dist/umd/lucide.min.js` |
| `tailwind.css` | built by tailwindcss 3.4.17 | MIT | generated — see below |
| `inter.css` | — | — | generated — see below |
| `inter-latin.woff2` | Inter v20 | SIL OFL 1.1 | `https://fonts.gstatic.com/s/inter/v20/…` |

These files are embedded into the binary (`public/embed.go`), so the panel works
without any of them being present on disk at run time.

## Regenerating

The Web player also uses local assets under `web/`: libmedia 1.3.1 (LGPL-3.0;
see `web/libmedia/README.md`), JASSUB 1.7.17 (MIT wrapper; dependency notices in
`web/JASSUB-COPYRIGHT.txt`) and Noto Sans SC Regular (SIL OFL 1.1;
`web/NotoSansCJK-LICENSE.txt`). The JASSUB UMD and worker/WASM files come from
`https://unpkg.com/jassub@1.7.17/dist/`. The old worker's trailing NUL padding
caused a JavaScript syntax error; the refreshed pinned distribution parses.

The subtitle fallback font comes from
`https://github.com/notofonts/noto-cjk/blob/main/Sans/SubsetOTF/SC/NotoSansSC-Regular.otf`,
SHA-256 `faa6c9df652116dde789d351359f3d7e5d2285a2b2a1f04a2d7244df706d5ea9`.
It is fetched locally only when external ASS is selected. Fonts and workers do
not make requests to third-party origins. Unused FFmpeg resources are retired.

```bash
npm run build:panel     # assets/panel.css + admin and public/web sources -> vendor/tailwind.css
```

`tailwind.css` is **committed**, so running the panel never needs a build step —
changing a class in the admin or Web client does. `assets/panel.css` is the
input: it holds the `@tailwind` directives plus the panel's own rules, which used to
live in an inline `<style>` block in `admin.html` (they had to move out for
`style-src 'self'` to be legal).

## Updating a library

Download the pinned file, replace it here, then check the panel still works in a
browser. Two things to keep in mind:

- The `Content-Security-Policy` in `internal/backend/middleware.go` and the page
  markup are asserted by `internal/backend/admin_panel_assets_test.go`. It fails if
  `admin.html` starts loading anything from another origin, gains an inline
  `<script>`/`<style>`/`style=`/`on*=`, or if a vendored file is served with a MIME
  type `X-Content-Type-Options: nosniff` would reject.
- `vue.global.prod.js` is the build **with** the template compiler, which is what
  the panel needs (it mounts an in-DOM template) and why the CSP keeps
  `'unsafe-eval'`. Switching to `vue.runtime.global.prod.js` would require
  precompiling the template and is a separate change.
- Inter is a variable font: Google serves the **same** file for every weight, so one
  `inter-latin.woff2` covers 300–700 via `font-weight: 300 700`. Only the latin
  subset is vendored — the panel is a Chinese UI and Inter has no CJK glyphs, so the
  rest falls through to the system font.
