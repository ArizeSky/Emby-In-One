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

```bash
npm run build:panel     # assets/panel.css + public/admin.html + public/admin.js -> vendor/tailwind.css
```

`tailwind.css` is **committed**, so running the panel never needs a build step —
only changing a class in `admin.html`/`admin.js` does. `assets/panel.css` is the
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
