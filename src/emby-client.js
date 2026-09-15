const axios = require('axios');
const http = require('http');
const https = require('https');
const logger = require('./utils/logger');
const capturedHeaders = require('./utils/captured-headers');
const requestStore = require('./utils/request-store');

// Shared HTTP agents with keepAlive for connection pooling
const keepAliveHttpAgent = new http.Agent({ keepAlive: true, maxSockets: 64, maxFreeSockets: 16 });
const keepAliveHttpsAgent = new https.Agent({ keepAlive: true, maxSockets: 64, maxFreeSockets: 16 });

// Headers to forward from client in passthrough mode
const PASSTHROUGH_HEADERS = [
  'user-agent',
  'x-emby-client', 'x-emby-client-version',
  'x-emby-device-name', 'x-emby-device-id',
  'x-emby-authorization', 'authorization',
  'accept', 'accept-language', 'accept-encoding',
];

const EMBY_CLIENT_HEADERS = {
  'X-Emby-Client': 'Emby Aggregator',
  'X-Emby-Client-Version': '1.0.0',
  'X-Emby-Device-Name': 'EmbyInOne',
  'X-Emby-Device-Id': 'emby-in-one-proxy',
};

const SPOOF_PROFILES = {
  infuse: {
    'User-Agent': 'Infuse/7.7.1 (iPhone; iOS 17.4.1; Scale/3.00)',
    'X-Emby-Client': 'Infuse',
    'X-Emby-Client-Version': '7.7.1',
    'X-Emby-Device-Name': 'iPhone',
    'X-Emby-Device-Id': 'infuse-spoof-id'
  },
  official: {
    'User-Agent': 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Emby/1.0.0',
    'X-Emby-Client': 'Emby Web',
    'X-Emby-Client-Version': '4.8.3.0',
    'X-Emby-Device-Name': 'Chrome Windows',
    'X-Emby-Device-Id': 'official-spoof-id'
  }
};

/**
 * HTTP client wrapper for a single upstream Emby server.
 */
class EmbyClient {
  constructor(serverConfig, serverIndex, allProxies = [], timeouts = {}) {
    this.config = serverConfig;
    this.serverIndex = serverIndex;
    this.name = serverConfig.name;
    this.baseUrl = serverConfig.url;
    this.streamBaseUrl = serverConfig.streamingUrl || this.baseUrl;

    // Validate baseUrl protocol to prevent SSRF
    for (const url of [this.baseUrl, this.streamBaseUrl]) {
      const parsed = new URL(url);
      if (!['http:', 'https:'].includes(parsed.protocol)) {
        throw new Error(`Invalid upstream protocol: ${parsed.protocol}`);
      }
    }
    this.accessToken = serverConfig.apiKey || null;
    this.userId = null;
    this.online = false;
    // 恢复上次成功登录的 passthrough headers
    this._lastSuccessHeaders = capturedHeaders.getServerHeaders(this.name) || null;
    this.timeouts = {
      api: timeouts.api || 30000,
      login: timeouts.login || 10000,
      healthCheck: timeouts.healthCheck || 10000,
    };

    const axiosConfig = {
      baseURL: this.baseUrl,
      timeout: this.timeouts.api,
      httpAgent: keepAliveHttpAgent,
      httpsAgent: keepAliveHttpsAgent,
    };

    if (serverConfig.proxyId) {
      const proxyInfo = allProxies.find(p => p.id === serverConfig.proxyId);
      if (proxyInfo && proxyInfo.url) {
        try {
          const url = new URL(proxyInfo.url);
          axiosConfig.proxy = {
            protocol: url.protocol.replace(':', ''),
            host: url.hostname,
            port: parseInt(url.port),
          };
          if (url.username) {
            axiosConfig.proxy.auth = {
              username: decodeURIComponent(url.username),
              password: decodeURIComponent(url.password)
            };
          }
          logger.info(`[${this.name}] Using proxy: ${url.hostname}:${url.port}`);
        } catch (e) {
          logger.error(`[${this.name}] Invalid proxy URL format for proxy "${proxyInfo.name || proxyInfo.id}"`);
        }
      }
    }

    this.http = axios.create(axiosConfig);
  }

  /**
   * Validate that a path is relative (prevents SSRF by absolute URL injection).
   */
  _validatePath(path) {
    if (typeof path !== 'string') throw new Error('Invalid path');
    if (/^https?:\/\//i.test(path) || path.startsWith('//')) {
      throw new Error('Absolute URLs not allowed');
    }
  }

  /**
   * Get spoof profile headers (non-passthrough modes).
   */
  _getSpoofHeaders() {
    const profile = this.config.spoofClient;
    if (profile && SPOOF_PROFILES[profile]) {
      return { ...SPOOF_PROFILES[profile] };
    }
    return { ...EMBY_CLIENT_HEADERS };
  }

  /**
   * Get the best available headers for passthrough mode.
   * Priority:
   *   1. Current request's live client headers (requires X-Emby-Client)
   *   2. Captured headers for the current proxy token
   *   3. This server's last successful login headers (persisted across restarts)
   *   4. Most recently captured headers from any token
   *   5. Fixed Infuse fallback
   */
  _getPassthroughHeaders() {
    let captured = null;
    let source = 'infuse-fallback';

    const requestContext = requestStore.getStore() || null;
    const liveHeaders = requestContext?.headers || null;
    const proxyToken = requestContext?.proxyToken || null;

    if (liveHeaders && liveHeaders['x-emby-client']) {
      captured = {};
      for (const k of PASSTHROUGH_HEADERS) {
        if (liveHeaders[k]) captured[k] = liveHeaders[k];
      }
      source = 'live-request';
    }

    if (!captured && proxyToken) {
      const saved = capturedHeaders.get(proxyToken);
      if (saved) {
        captured = { ...saved };
        source = 'captured-token';
      }
    }

    if (!captured && this._lastSuccessHeaders) {
      // _lastSuccessHeaders 是登录时构建的完整 headers（大写 key 格式如 User-Agent, X-Emby-Client）
      // 直接返回，不需要再叠加 Infuse 基础
      return { source: 'last-success', headers: { ...this._lastSuccessHeaders } };
    }

    if (!captured) {
      const latest = capturedHeaders.getLatest();
      if (latest) {
        captured = { ...latest };
        source = 'captured-latest';
      }
    }

    const headers = { ...SPOOF_PROFILES.infuse };
    if (captured) {
      if (captured['user-agent']) headers['User-Agent'] = captured['user-agent'];
      if (captured['x-emby-client']) headers['X-Emby-Client'] = captured['x-emby-client'];
      if (captured['x-emby-client-version']) headers['X-Emby-Client-Version'] = captured['x-emby-client-version'];
      if (captured['x-emby-device-name']) headers['X-Emby-Device-Name'] = captured['x-emby-device-name'];
      if (captured['x-emby-device-id']) headers['X-Emby-Device-Id'] = captured['x-emby-device-id'];
      if (captured['accept']) headers['Accept'] = captured['accept'];
      if (captured['accept-language']) headers['Accept-Language'] = captured['accept-language'];
    }

    return { source, headers };
  }

  /**
   * Build headers for an upstream request (internal use).
   */
  _buildRequestHeaders(extraHeaders) {
    let headers;

    if (this.config.spoofClient === 'passthrough') {
      headers = this._getPassthroughHeaders().headers;
    } else {
      headers = this._getSpoofHeaders();
    }

    if (extraHeaders) Object.assign(headers, extraHeaders);
    if (this.accessToken) {
      headers['X-Emby-Token'] = this.accessToken;
    }

    return headers;
  }

  /**
   * Public: get client identity headers (without auth token).
   * Used by streaming/image routes that add the token separately.
   */
  getRequestHeaders() {
    if (this.config.spoofClient === 'passthrough') {
      return this._getPassthroughHeaders().headers;
    }
    return this._getSpoofHeaders();
  }

  /**
   * Login using username/password via AuthenticateByName.
   * Skipped if apiKey is already set.
   */
  async login(overrideHeaders = null) {
    if (this.config.apiKey) {
      this.accessToken = this.config.apiKey;
      logger.info(`[${this.name}] Authenticating with API key`);
      try {
        const resp = await this.request('GET', '/Users/Me');
        if (resp && resp.Id) {
          this.userId = resp.Id;
          this.online = true;
          logger.info(`[${this.name}] API key auth success, userId=${this.userId}`);
        } else {
          throw new Error('Response missing User Id');
        }
      } catch (err) {
        const status = err.response?.status;
        logger.error(`[${this.name}] API key validation failed: ${err.message}${status ? ` (HTTP ${status})` : ''}`);
        this.online = false;
      }
      return;
    }

    const isPassthrough = this.config.spoofClient === 'passthrough';
    let loginHeaders;
    let headerSource;

    if (isPassthrough) {
      if (overrideHeaders && Object.keys(overrideHeaders).length > 0) {
        loginHeaders = { ...SPOOF_PROFILES.infuse };
        if (overrideHeaders['user-agent']) loginHeaders['User-Agent'] = overrideHeaders['user-agent'];
        if (overrideHeaders['x-emby-client']) loginHeaders['X-Emby-Client'] = overrideHeaders['x-emby-client'];
        if (overrideHeaders['x-emby-client-version']) loginHeaders['X-Emby-Client-Version'] = overrideHeaders['x-emby-client-version'];
        if (overrideHeaders['x-emby-device-name']) loginHeaders['X-Emby-Device-Name'] = overrideHeaders['x-emby-device-name'];
        if (overrideHeaders['x-emby-device-id']) loginHeaders['X-Emby-Device-Id'] = overrideHeaders['x-emby-device-id'];
        if (overrideHeaders['accept']) loginHeaders['Accept'] = overrideHeaders['accept'];
        if (overrideHeaders['accept-language']) loginHeaders['Accept-Language'] = overrideHeaders['accept-language'];
        headerSource = 'captured-override';
      } else {
        const pt = this._getPassthroughHeaders();
        loginHeaders = pt.headers;
        headerSource = pt.source;
      }
    } else {
      loginHeaders = this._getSpoofHeaders();
      headerSource = this.config.spoofClient || 'default';
    }

    const clientName = loginHeaders['X-Emby-Client'] || 'Infuse';
    const deviceName = loginHeaders['X-Emby-Device-Name'] || 'iPhone';
    const deviceId = loginHeaders['X-Emby-Device-Id'] || 'emby-in-one';
    const clientVersion = loginHeaders['X-Emby-Client-Version'] || '7.7.1';
    const userAgent = loginHeaders['User-Agent'] || 'unknown';

    logger.info(`[${this.name}] Authenticating: user="${this.config.username}" mode=${isPassthrough ? 'passthrough' : headerSource} source=${headerSource}`);
    logger.info(`[${this.name}] Login identity: Client="${clientName}" Device="${deviceName}" DeviceId="${deviceId}" Version="${clientVersion}"`);
    logger.debug(`[${this.name}] Login User-Agent: ${userAgent}`);

    try {
      const authHeader = `Emby UserId="", Client="${clientName}", Device="${deviceName}", DeviceId="${deviceId}", Version="${clientVersion}"`;
      const reqHeaders = { ...loginHeaders, 'X-Emby-Authorization': authHeader };

      const safeHeaders = { ...reqHeaders };
      if (safeHeaders['X-Emby-Authorization']) {
        safeHeaders['X-Emby-Authorization'] = safeHeaders['X-Emby-Authorization'].substring(0, 50) + '...';
      }
      logger.debug(`[${this.name}] Full login request headers: ${JSON.stringify(safeHeaders)}`);

      const resp = await this.http.post('/Users/AuthenticateByName', {
        Username: this.config.username,
        Pw: this.config.password || '',
      }, {
        headers: reqHeaders,
        timeout: this.timeouts.login,
      });

      if (resp.data && resp.data.AccessToken && resp.data.User) {
        this.accessToken = resp.data.AccessToken;
        this.userId = resp.data.User.Id;
        this.online = true;
        if (isPassthrough) {
          this._lastSuccessHeaders = { ...loginHeaders };
          capturedHeaders.setServerHeaders(this.name, loginHeaders);
          logger.info(`[${this.name}] Login success, userId=${this.userId} (saved working headers, source=${headerSource})`);
        } else {
          logger.info(`[${this.name}] Login success, userId=${this.userId}`);
        }
      } else {
        const bodyStr = JSON.stringify(resp.data || {}).substring(0, 200);
        throw new Error(`Unexpected response structure: ${bodyStr}`);
      }
    } catch (err) {
      const status = err.response?.status;
      const respData = err.response?.data;
      const errorMsg = status ? `HTTP ${status}` : err.message;
      logger.error(`[${this.name}] Login failed: ${errorMsg}`);
      if (status) {
        const msg = typeof respData === 'object' ? (respData?.message || respData?.Message || '') : '';
        if (msg) logger.error(`[${this.name}] Response message: ${msg.substring(0, 200)}`);
      }
      if ((status === 401 || status === 403) && isPassthrough) {
        logger.warn(`[${this.name}] Passthrough ${status} — sent identity: Client="${clientName}", Device="${deviceName}", DeviceId="${deviceId}", UA="${userAgent}"`);
        logger.warn(`[${this.name}] Passthrough ${status} — header source: "${headerSource}". If "infuse-fallback", no matching real client identity was available.`);
      }
      this.online = false;
    }
  }

  /**
   * Make an authenticated request to this upstream server.
   */
  async request(method, path, { params, data, headers: extraHeaders, timeout, signal } = {}) {
    this._validatePath(path);
    const headers = this._buildRequestHeaders(extraHeaders);

    const resp = await this.http.request({
      method,
      url: path,
      params,
      data,
      headers,
      timeout: timeout || this.timeouts.api,
      signal
    });

    return resp.data;
  }

  /**
   * Get a readable stream from the upstream server (for media/image proxying).
   */
  async getStream(path, { params, headers: extraHeaders, timeout } = {}) {
    this._validatePath(path);
    const headers = this._buildRequestHeaders(extraHeaders);
    delete headers['host'];
    delete headers['accept-encoding'];

    const resp = await this.http.request({
      method: 'GET',
      url: path,
      params,
      headers,
      responseType: 'stream',
      timeout: timeout || 0,
      maxRedirects: this.config.followRedirects ? 5 : 0,
    });

    return resp;
  }

  /**
   * Build a full URL to an upstream resource, including auth.
   * Used for redirect mode.
   */
  buildUrl(path, params = {}) {
    const url = new URL(path, this.baseUrl);
    if (this.accessToken) {
      url.searchParams.set('api_key', this.accessToken);
    }
    for (const [k, v] of Object.entries(params)) {
      if (v != null) url.searchParams.set(k, v);
    }
    return url.toString();
  }

  /**
   * Health check: try /System/Info/Public.
   * Uses passthrough headers for passthrough mode to avoid being rejected.
   */
  async healthCheck() {
    const wasBefore = this.online;
    try {
      const headers = (this.config.spoofClient === 'passthrough')
        ? this._getPassthroughHeaders().headers
        : this._getSpoofHeaders();

      await this.http.get('/System/Info/Public', { timeout: this.timeouts.healthCheck, headers });

      if (!this.userId && !this.config.apiKey) {
        logger.info(`[${this.name}] Server reachable but not authenticated, attempting re-login...`);
        await this.login();
      } else {
        this.online = true;
      }
    } catch (err) {
      this.online = false;
      if (wasBefore) {
        const status = err.response?.status;
        const code = err.code;
        logger.debug(`[${this.name}] Health check failed: ${status ? `HTTP ${status}` : code || err.message}`);
      }
    }
    if (wasBefore !== this.online) {
      logger.warn(`[${this.name}] Health status changed: ${wasBefore ? 'ONLINE' : 'OFFLINE'} -> ${this.online ? 'ONLINE' : 'OFFLINE'}`);
    }
    return this.online;
  }
}

module.exports = { EmbyClient };