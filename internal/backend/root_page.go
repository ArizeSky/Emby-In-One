package backend

// rootPageHTML is the small landing page served at "/". It offers the two
// supported entry points — the web player and the admin panel — with fixed,
// same-origin paths and no token. It uses plain inline CSS and anchors, so it
// needs no Vue, remote fonts or JavaScript, and carries no login credentials.
const rootPageHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Emby-In-One</title>
<style>
*{box-sizing:border-box}
body{font-family:sans-serif;background:#f8fafc;color:#1e293b;margin:0;min-height:100vh;display:grid;place-items:center;padding:24px}
main{width:100%;max-width:480px;text-align:center}
h1{font-size:28px;line-height:1.3;margin:0 0 16px}
p{font-size:15px;line-height:1.6;color:#64748b;margin:0 0 24px}
nav{display:flex;justify-content:center;gap:12px;flex-wrap:wrap}
a{display:inline-flex;align-items:center;justify-content:center;min-height:44px;padding:10px 20px;border:1px solid #cbd5e1;border-radius:6px;color:#334155;background:#fff;text-decoration:none;font-size:15px}
a.primary{background:#2563eb;border-color:#2563eb;color:#fff}
a:focus-visible{outline:2px solid #15803d;outline-offset:3px}
@media(max-width:360px){nav{flex-direction:column}}
</style>
</head>
<body><main>
<h1>Emby-In-One</h1>
<p>Emby 客户端可使用当前地址连接。</p>
<nav aria-label="应用入口">
<a class="primary" href="/web/">Web 客户端</a>
<a href="/admin/">管理面板</a>
</nav>
</main></body></html>`
