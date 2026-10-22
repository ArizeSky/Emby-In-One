package backend

// rootPageHTML is the small landing page served at "/". It keeps the original
// card-style look (centered white card, icon, title, subtitle, buttons) and now
// offers the two supported entry points — the web player and the admin panel —
// with fixed, same-origin paths and no token. It uses plain inline CSS and
// anchors, so it needs no Vue, remote fonts or JavaScript, and carries no login
// credentials.
const rootPageHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Emby-In-One</title>
<style>
*{box-sizing:border-box}
body{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,Helvetica,Arial,sans-serif;background:#f8fafc;color:#1e293b;margin:0;min-height:100vh;display:flex;align-items:center;justify-content:center;padding:24px}
.box{background:#fff;border:1px solid #e2e8f0;border-radius:12px;box-shadow:0 1px 3px rgba(15,23,42,.06),0 1px 2px rgba(15,23,42,.04);padding:2.5rem 3rem;text-align:center;max-width:400px;width:100%}
.icon{font-size:3rem;line-height:1;margin-bottom:1rem}
.title{font-size:1.4rem;font-weight:700;color:#1e293b;margin:0 0 .5rem}
.sub{color:#64748b;font-size:.95rem;line-height:1.6;margin:0 0 1.75rem}
.actions{display:flex;justify-content:center;gap:.75rem;flex-wrap:wrap}
.btn{display:inline-flex;align-items:center;justify-content:center;min-height:44px;padding:.6rem 1.4rem;border-radius:8px;text-decoration:none;font-weight:600;font-size:.9rem;border:1px solid transparent;transition:background-color .15s ease,border-color .15s ease}
.btn-primary{background:#2563eb;color:#fff}
.btn-primary:hover{background:#1d4ed8}
.btn-secondary{background:#fff;color:#334155;border-color:#cbd5e1}
.btn-secondary:hover{background:#f1f5f9}
.btn:focus-visible{outline:2px solid #15803d;outline-offset:3px}
@media(max-width:360px){.box{padding:2rem 1.5rem}.actions{flex-direction:column}.btn{width:100%}}
</style>
</head>
<body>
<main class="box">
<div class="icon" aria-hidden="true">🎬</div>
<h1 class="title">Emby-In-One</h1>
<p class="sub">Emby 客户端可使用当前地址连接。</p>
<nav class="actions" aria-label="应用入口">
<a class="btn btn-primary" href="/web/">Web 客户端</a>
<a class="btn btn-secondary" href="/admin/">管理面板</a>
</nav>
</main>
</body>
</html>`
