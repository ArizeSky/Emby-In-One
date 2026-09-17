const ALLOW_METHODS = 'GET, POST, PUT, DELETE, OPTIONS';
const ALLOW_HEADERS = 'Content-Type, Authorization, X-Emby-Token, X-Emby-Authorization, X-Emby-Client, X-Emby-Client-Version, X-Emby-Device-Name, X-Emby-Device-Id';

function isAdminApiPath(reqPath = '') {
  return reqPath.startsWith('/admin/api/');
}

function applyCorsHeaders(req, res) {
  if (isAdminApiPath(req.path || '')) {
    // Admin API: 仅允许同源请求
    const origin = req.headers.origin;
    const host = req.headers.host;
    if (origin && host) {
      try {
        const originHost = new URL(origin).host;
        if (originHost === host) {
          res.header('Access-Control-Allow-Origin', origin);
          res.header('Access-Control-Allow-Methods', ALLOW_METHODS);
          res.header('Access-Control-Allow-Headers', ALLOW_HEADERS);
        }
      } catch {}
    }
    // 无 Origin 头 = 同源请求或非浏览器请求，允许通过
  } else {
    // Emby 客户端路由: 宽松 CORS
    res.header('Access-Control-Allow-Origin', '*');
    res.header('Access-Control-Allow-Methods', ALLOW_METHODS);
    res.header('Access-Control-Allow-Headers', ALLOW_HEADERS);
  }
}

module.exports = {
  ALLOW_METHODS,
  ALLOW_HEADERS,
  applyCorsHeaders,
  isAdminApiPath,
};
