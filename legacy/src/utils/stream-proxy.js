const http = require('http');
const https = require('https');
const { URL } = require('url');
const logger = require('./logger');
const requestStore = require('./request-store');

function sanitizeUrl(urlStr) {
  try {
    const u = new URL(urlStr);
    u.searchParams.delete('api_key');
    u.searchParams.delete('ApiKey');
    return u.toString();
  } catch {
    return urlStr.replace(/[?&]api_key=[^&]*/gi, '');
  }
}

/**
 * Rewrite HLS manifest (.m3u8) content so that segment/playlist URLs
 * point back to the proxy via relative paths instead of absolute origins.
 *
 * @param {string} content - Raw m3u8 text from upstream
 * @param {string} upstreamBase - Upstream manifest URL used to resolve relative lines
 * @param {string} proxyToken - Proxy auth token to append as api_key
 */
function rewriteM3u8(content, upstreamBase, proxyToken) {
  return content.replace(/^(?!#)(.+)$/gm, (line) => {
    const trimmed = line.trim();
    if (!trimmed) return trimmed;

    let resolvedUrl;
    try {
      resolvedUrl = new URL(trimmed, upstreamBase);
    } catch (_err) {
      return trimmed;
    }

    resolvedUrl.searchParams.delete('api_key');
    resolvedUrl.searchParams.delete('ApiKey');
    if (proxyToken) {
      resolvedUrl.searchParams.set('api_key', proxyToken);
    }

    return `${resolvedUrl.pathname}${resolvedUrl.search}`;
  });
}

/**
 * Proxy an upstream stream response to the client.
 * Uses Node's built-in http/https for proper backpressure and cleanup.
 * Now supports automatic redirect following and custom headers.
 */
function proxyStream(upstreamUrl, token, req, res, extraHeaders = {}, followCount = 0) {
  if (followCount > 5) {
    logger.error(`Too many redirects for ${sanitizeUrl(upstreamUrl)}`);
    if (!res.headersSent) res.status(502).json({ message: 'Too many redirects' });
    return Promise.resolve();
  }

  return new Promise((resolve) => {
    let settled = false;
    function done() {
      if (!settled) { settled = true; resolve(); }
    }

    let parsed;
    try {
      parsed = new URL(upstreamUrl);
    } catch (e) {
      logger.error(`Invalid stream URL: ${sanitizeUrl(upstreamUrl)}`);
      if (!res.headersSent) res.status(502).json({ message: 'Invalid upstream URL' });
      return done();
    }

    const isHttps = parsed.protocol === 'https:';
    const lib = isHttps ? https : http;

    const options = {
      hostname: parsed.hostname,
      port: parsed.port || (isHttps ? 443 : 80),
      path: parsed.pathname + parsed.search,
      method: 'GET',
      headers: { ...extraHeaders },
    };

    if (req.headers.range) options.headers['Range'] = req.headers.range;
    if (req.headers.accept) options.headers['Accept'] = req.headers.accept;
    if (token) options.headers['X-Emby-Token'] = token;

    if (!options.headers['User-Agent'] && !options.headers['user-agent']) {
      const requestContext = requestStore.getStore();
      const liveHeaders = requestContext?.headers || null;
      if (liveHeaders && liveHeaders['user-agent']) {
        options.headers['User-Agent'] = liveHeaders['user-agent'];
      }
    }

    const safeLogHeaders = { ...options.headers };
    if (safeLogHeaders['X-Emby-Token']) safeLogHeaders['X-Emby-Token'] = safeLogHeaders['X-Emby-Token'].substring(0, 8) + '...';
    logger.debug(`Stream proxy: ${parsed.hostname}${parsed.pathname.substring(0, 60)} headers=${JSON.stringify(safeLogHeaders)}`);

    const upstreamReq = lib.request(options, (upstreamRes) => {
      const statusCode = upstreamRes.statusCode;

      if (statusCode === 401 || statusCode === 403) {
        logger.warn(`Stream ${statusCode} from ${parsed.hostname}: path=${parsed.pathname.substring(0, 80)} sentHeaders=${JSON.stringify(Object.keys(options.headers))}`);
      }

      if ([301, 302, 303, 307, 308].includes(statusCode) && upstreamRes.headers.location) {
        let redirectUrl = upstreamRes.headers.location;
        if (!redirectUrl.startsWith('http')) {
          redirectUrl = new URL(redirectUrl, upstreamUrl).toString();
        }
        upstreamRes.destroy();
        return proxyStream(redirectUrl, token, req, res, extraHeaders, followCount + 1).then(done);
      }

      if (res.headersSent) {
        upstreamRes.destroy();
        return done();
      }

      res.status(statusCode);

      const forwardHeaders = [
        'content-type', 'content-length', 'content-range',
        'accept-ranges', 'content-disposition', 'cache-control',
        'etag', 'last-modified', 'transfer-encoding',
      ];
      for (const h of forwardHeaders) {
        if (upstreamRes.headers[h]) res.set(h, upstreamRes.headers[h]);
      }

      function cleanup() {
        if (!upstreamRes.destroyed) upstreamRes.destroy();
        if (!upstreamReq.destroyed) upstreamReq.destroy();
        done();
      }

      req.on('close', cleanup);
      req.on('aborted', cleanup);
      res.on('close', cleanup);

      const contentType = upstreamRes.headers['content-type'] || '';
      const isM3u8 = contentType.includes('mpegurl') || parsed.pathname.endsWith('.m3u8');

      if (isM3u8) {
        const MAX_M3U8_SIZE = 2 * 1024 * 1024;
        res.removeHeader('content-length');
        let body = '';
        let bodyLen = 0;
        upstreamRes.setEncoding('utf8');
        upstreamRes.on('data', chunk => {
          bodyLen += chunk.length;
          if (bodyLen > MAX_M3U8_SIZE) {
            upstreamRes.destroy();
            if (!res.headersSent) res.status(502).json({ message: 'M3U8 response too large' });
            done();
            return;
          }
          body += chunk;
        });
        upstreamRes.on('end', () => {
          const rewritten = rewriteM3u8(body, upstreamUrl, req._proxyToken);
          res.set('content-type', 'application/x-mpegURL');
          res.end(rewritten);
          done();
        });
      } else {
        upstreamRes.pipe(res);
        upstreamRes.on('end', done);
      }
      upstreamRes.on('error', (err) => {
        logger.error(`Upstream stream error: ${err.message}`);
        if (!res.headersSent) res.status(502).end();
        done();
      });
    });

    upstreamReq.on('error', (err) => {
      logger.error(`Stream upstream request error: ${err.message}`);
      if (!res.headersSent) res.status(502).json({ message: 'Failed to proxy stream' });
      done();
    });

    upstreamReq.end();
  });
}

/**
 * Build the full upstream URL for a stream request.
 * Uses streamBaseUrl if available (for servers with different streaming domains).
 */
function buildStreamUrl(client, path, queryParams = {}) {
  const base = client.streamBaseUrl || client.baseUrl;
  const url = new URL(path, base);
  if (client.accessToken) {
    url.searchParams.set('api_key', client.accessToken);
  }
  for (const [k, v] of Object.entries(queryParams)) {
    if (v != null && k !== 'api_key' && k !== 'ApiKey') {
      url.searchParams.set(k, v);
    }
  }
  return url.toString();
}

module.exports = { proxyStream, buildStreamUrl, rewriteM3u8 };