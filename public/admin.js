const { createApp } = Vue;
createApp({
  data() {
    return {
      isLoggedIn: false, loginForm: { user: '', pass: '' }, loginError: '',
      showSidebar: false,
      clientInfo: null,
      currentPage: 'dashboard', pages: [
        {id:'dashboard', name:'系统概览', icon:'layout-dashboard'},
        {id:'servers', name:'上游节点', icon:'server'},
        {id:'ua', name:'UA 管理', icon:'fingerprint'},
        {id:'users', name:'用户管理', icon:'users'},
        {id:'proxies', name:'网络代理', icon:'shield'},
        {id:'settings', name:'全局设置', icon:'settings'},
        {id:'logs', name:'运行日志', icon:'terminal'}
      ],
      stats: { upstreamCount: 0, upstreamOnline: 0, idMappings: { mappingCount: 0, persistent: true }, upstream: [] },
      upstreamList: [], proxyList: [],
      settings: { serverName: '', playbackMode: 'proxy', adminUsername: '', adminPassword: '', currentPassword: '', timeouts: { api: 30000, global: 15000, login: 10000, healthCheck: 10000, healthInterval: 60000, searchGracePeriod: 3000, metadataGracePeriod: 3000, latestGracePeriod: 0 } },
      settingsLoadState: 'loading', settingsLoadedOnce: false, settingsError: '', settingsRequestID: 0,
      listErrors: { upstream: '', proxies: '', users: '', logs: '' },
      adminUsernameOriginal: '',   // 服务端当前的管理员用户名，用于判断这次保存是否真的改了用户名
      logs: [], isLoadingLogs: false, saveSuccess: false,
      logLevelFilter: 'ALL', logSearch: '',
      showModal: false, editIndex: null, serverForm: {},
      showProxyModal: false, proxyForm: { name: '', url: '' },
      proxyTestState: { loading: false, result: null },
      userList: [], showUserModal: false, editUserId: null, userForm: { username: '', password: '', enabled: true, allowedServers: [], maxParentalRating: 0, blockUnratedItems: false },
      userLibraryGroups: [], adminHomeOpen: false, adminLibraryGroups: [], libraryGroupsLoading: false,
      uaTab: 'resident', uaRecent: [], uaResident: [], uaServers: [],
      uaLoading: false, uaError: '', uaDetail: null,
      uaSaveDialog: null, uaReplaceDialog: null, uaAssignmentDialog: null, uaPending: {},
      aggregationPending: {},
    };
  },
  computed: { pageTitle() { return this.pages.find(p => p.id === this.currentPage)?.name || '管理面板'; },
    dashStats() { return [
      {label:'上游服务器', val:this.stats.upstreamCount, icon:'server', bg:'bg-blue-50 text-blue-600'},
      {label:'在线节点', val:this.stats.upstreamOnline, icon:'activity', bg:'bg-green-50 text-green-600'},
      {label:'ID 映射数', val:this.stats.idMappings.mappingCount, icon:'link', bg:'bg-purple-50 text-purple-600'},
      {label:'存储引擎', val:this.stats.idMappings.persistent?'SQLite':'Memory', icon:'database', bg:'bg-orange-50 text-orange-600'}
    ];},
    filteredLogs() {
      return this.logs.filter(l => {
        if (this.logLevelFilter !== 'ALL' && l.level.toUpperCase() !== this.logLevelFilter) return false;
        if (this.logSearch && !l.message.toLowerCase().includes(this.logSearch.toLowerCase())) return false;
        return true;
      });
    }
  },
  watch: {
    currentPage(v) { this.stopUAReconnectPoll(); if(this._refreshTimer) clearTimeout(this._refreshTimer); this.abortRefresh(); this._refreshTimer = setTimeout(()=>{this.refresh(); this.$nextTick(() => lucide.createIcons());}, 50); }
  },
  mounted() { const t = localStorage.getItem('eio_admin_token'); if(t) this.checkAuth(t); this.$nextTick(() => lucide.createIcons()); },
  methods: {
    navigateTo(id) { this.currentPage = id; this.showSidebar = false; },
    async api(path, opts = {}) {
      const t = localStorage.getItem('eio_admin_token');
      const h = { 'Content-Type': 'application/json' };
      if(t) h['X-Emby-Token'] = t;
      const r = await fetch(path, { ...opts, headers: h });
      if (!r.ok) {
        let data = {};
        try { data = await r.json(); } catch(e) {}
        if (r.status === 401) this.logout();
        const message = data && (data.error || data.message);
        const err = new Error(message || ('HTTP ' + r.status));
        err.status = r.status; err.data = data;
        throw err;
      }
      if (r.status === 204) return { success: true };
      let body;
      try { body = await r.text(); } catch(e) {
        const err = new Error('HTTP ' + r.status + ': response body could not be read'); err.status = r.status; throw err;
      }
      if (!body.trim()) { const err = new Error('HTTP ' + r.status + ': empty response'); err.status = r.status; throw err; }
      try { return JSON.parse(body); } catch(e) {
        const err = new Error('HTTP ' + r.status + ': invalid JSON response'); err.status = r.status; throw err;
      }
    },
    async checkAuth() { try { const s = await this.api('/admin/api/status'); if(!s || s.error) { this.logout(); return; } this.isLoggedIn = true; this.refresh(); this.refreshClientInfo(); } catch(e) { /* api() already logs out on 401; a transient failure (server restart, network blip) keeps the stored token so a refresh can retry */ } },
    async doLogin() { try {
      const r = await fetch('/Users/AuthenticateByName', { method: 'POST', headers: {'Content-Type':'application/json'}, body: JSON.stringify({Username:this.loginForm.user, Pw:this.loginForm.pass}) });
      if(!r.ok) throw new Error(); const data = await r.json();
      localStorage.setItem('eio_admin_token', data.AccessToken);
      try { await this.api('/admin/api/status'); } catch(e) { localStorage.removeItem('eio_admin_token'); this.loginError = '需要管理员权限'; return; }
      this.isLoggedIn = true; this.refresh(); this.refreshClientInfo();
    } catch(e) { this.loginError = '用户名或密码错误'; } },
    logout() { this.stopUAReconnectPoll(); this.isLoggedIn = false; localStorage.removeItem('eio_admin_token'); },
    async copyClientUA() {
      if (!this.clientInfo || !this.clientInfo.userAgent) return;
      try { await navigator.clipboard.writeText(this.clientInfo.userAgent); alert('UA 已复制到剪贴板'); } catch(e) {}
    },
    // Page switches abort the previous page's in-flight refreshes: without
    // this, a late response overwrote whatever the new page had already
    // loaded (or emptied its lists through the error path).
    abortRefresh() { if (this._refreshAbort) { this._refreshAbort.abort(); this._refreshAbort = null; } },
    refreshSignal() { if (!this._refreshAbort) this._refreshAbort = new AbortController(); return this._refreshAbort.signal; },
    isAbort(e) { return e && e.name === 'AbortError'; },
    refresh() {
      this.abortRefresh();
      const signal = this.refreshSignal();
      if(this.currentPage === 'dashboard') this.refreshDashboard(signal);
      if(this.currentPage === 'servers') this.refreshServers(signal);
      if(this.currentPage === 'users') this.refreshUsers(signal);
      if(this.currentPage === 'proxies') this.refreshProxies(signal);
      if(this.currentPage === 'settings') this.refreshSettings(signal);
      if(this.currentPage === 'logs') this.refreshLogs(signal);
      if(this.currentPage === 'ua') this.refreshUAs(signal);
    },
    async refreshDashboard(signal) { try { this.stats = await this.api('/admin/api/status', { signal }); } catch(e) { if (this.isAbort(e)) return; } this.refreshClientInfo(signal); this.$nextTick(()=>lucide.createIcons()); },
    async refreshClientInfo(signal) { try { this.clientInfo = await this.api('/admin/api/client-info', { signal }); } catch(e) { if (this.isAbort(e)) return; } this.$nextTick(()=>lucide.createIcons()); },
    async refreshServers(signal) { try { this.upstreamList = await this.api('/admin/api/upstream', { signal }); this.listErrors.upstream = ''; } catch(e) { if (this.isAbort(e)) return; this.listErrors.upstream = e.message || '加载服务器列表失败'; } try { await this.refreshProxies(signal); } catch(e) {} try { const ua = await this.api('/admin/api/ua', { signal }); this.uaResident = ua.resident || []; this.uaServers = ua.servers || []; } catch(e) {} this.$nextTick(()=>lucide.createIcons()); },
    async refreshProxies(signal) { try { this.proxyList = await this.api('/admin/api/proxies', { signal }); this.listErrors.proxies = ''; } catch(e) { if (!this.isAbort(e)) this.listErrors.proxies = e.message || '加载代理列表失败'; } this.$nextTick(()=>lucide.createIcons()); },
    async refreshSettings(signal) {
      const requestID = ++this.settingsRequestID;
      this.settingsLoadState = 'loading';
      this.settingsError = '';
      this.saveSuccess = false;
      try {
        const s = await this.api('/admin/api/settings', { signal });
        if (requestID !== this.settingsRequestID) return;
        if (!s || typeof s !== 'object' || Array.isArray(s)) throw new Error('设置响应格式无效');
        this.settings = { ...this.settings, ...s, adminPassword: '', currentPassword: '', timeouts: s.timeouts || this.settings.timeouts };
        this.adminUsernameOriginal = s.adminUsername || '';
        this.settingsLoadedOnce = true;
        this.settingsLoadState = 'loaded';
      } catch(e) {
        if (requestID !== this.settingsRequestID || this.isAbort(e)) return;
        this.settingsLoadState = 'error';
        this.settingsError = e.message || '加载设置失败';
      }
    },
    async saveSettings() {
      if (!this.settingsLoadedOnce || this.settingsLoadState === 'loading') { alert('设置尚未成功加载，无法保存'); return; }
      const nextUsername = (this.settings.adminUsername || '').trim();
      const usernameChanged = !!nextUsername && nextUsername !== this.adminUsernameOriginal;
      const passwordChanged = !!this.settings.adminPassword;
      if ((usernameChanged || passwordChanged) && !this.settings.currentPassword) { alert('修改管理员用户名或密码时，必须输入当前密码'); return; }
      if (passwordChanged && (this.settings.adminPassword.length < 8 || this.settings.adminPassword.length > 128)) { alert('密码长度必须介于 8 和 128 之间'); return; }
      try {
        const res = await this.api('/admin/api/settings', { method:'PUT', body:JSON.stringify(this.settings) });
        this.saveSuccess = true;
        this.adminUsernameOriginal = this.settings.adminUsername || this.adminUsernameOriginal;
        this.settings.adminPassword = ''; this.settings.currentPassword = '';
        setTimeout(()=>this.saveSuccess=false,3000);
      } catch(e) { alert('保存失败：' + (e.message || '未知错误')); }
    },
    async refreshLogs(signal) { this.isLoadingLogs = true; try { this.logs = await this.api('/admin/api/logs?limit=500', { signal }); this.listErrors.logs = ''; this.$nextTick(() => { const b=this.$refs.logBox; if(b) b.scrollTop=b.scrollHeight; lucide.createIcons(); }); } catch(e) { if (!this.isAbort(e)) this.listErrors.logs = e.message || '加载日志失败'; } finally { this.isLoadingLogs=false; } },
    async downloadLogs() {
      const t = localStorage.getItem('eio_admin_token');
      const res = await fetch('/admin/api/logs/download', { headers: t ? { 'X-Emby-Token': t } : {} });
      if (!res.ok) { alert('下载失败'); return; }
      const blob = await res.blob();
      const a = document.createElement('a');
      a.href = URL.createObjectURL(blob);
      a.download = 'emby-in-one.log';
      a.click();
      URL.revokeObjectURL(a.href);
    },
    async clearLogs() { if(!confirm('确认清空所有日志？')) return; try { await this.api('/admin/api/logs', { method:'DELETE' }); this.logs = []; } catch(e) { alert('清空失败：' + (e.message || '未知错误')); } },
    getProxyName(id) { const p = this.proxyList.find(x => x.id === id); return p ? p.name : '不使用'; },
    openAddServer() { this.editID = null; this.editIndex = null; this.serverForm = { name:'', url:'', streamingUrlsText:'', authType:'password', spoofClient:'passthrough', uaProfileId:'', expectedIdentityBinding:null, followRedirects:true, proxyId:null, priorityMetadata:false, directRedirect:false, pagedScan:false, maxConcurrent:0, customUserAgent:'', customClient:'', customClientVersion:'', customDeviceName:'', customDeviceId:'' }; this.showModal = true; this.refreshServerUAOptions(); },
    editServer(s) { this.editID = s.id || s.index; this.editIndex = s.index; this.serverForm = { ...s, password:'', apiKey:'', maxConcurrent: s.maxConcurrent || 0, uaProfileId: s.uaProfileId || '', expectedIdentityBinding: { spoofClient: s.spoofClient, uaProfileId: s.uaProfileId || '' }, streamingUrlsText: (s.streamingUrls && s.streamingUrls.length ? s.streamingUrls : (s.streamingUrl ? [s.streamingUrl] : [])).join('\n'), customUserAgent: s.customUserAgent || '', customClient: s.customClient || '', customClientVersion: s.customClientVersion || '', customDeviceName: s.customDeviceName || '', customDeviceId: s.customDeviceId || '' }; this.showModal = true; this.refreshServerUAOptions(); },
    async saveServer() {
      const target = this.editID !== null && this.editID !== undefined ? this.editID : this.editIndex;
      const m = target === null || target === undefined ? 'POST' : 'PUT';
      const payload = { ...this.serverForm, streamingUrls: (this.serverForm.streamingUrlsText || '').split(/[\n,]/).map(x => x.trim()).filter(x => x !== '') };
      delete payload.streamingUrlsText;
      delete payload.aggregationEnabled;
      payload.uaProfileId = this.serverForm.uaProfileId || '';
      if (payload.spoofClient === 'resident') {
        const resident = (this.uaResident || []).find(r => r.id === payload.uaProfileId);
        if (!resident) { alert('请选择一个常驻 UA'); return; }
        payload.uaExpectedProfileRevision = resident.revision;
      }
      try {
        const res = await this.api('/admin/api/upstream' + (target===null||target===undefined?'':'/'+target), { method:m, body:JSON.stringify(payload) });
        if (res.warning) { alert('提示：' + res.warning); }
        this.showModal = false;
        await this.refreshServers();
        await this.refreshDashboard();
      } catch (e) {
        if (e.status === 409) { alert('保存冲突：' + (e.message || '数据已变化，请刷新后重试')); }
        else { alert('保存失败：' + (e.message || '未知错误')); }
      }
    },
    async deleteServer(id) { if(!confirm('删除服务器？')) return; try { await this.api('/admin/api/upstream/'+id, { method:'DELETE' }); await this.refreshServers(); } catch(e) { alert('删除失败：' + (e.message || '未知错误')); } },
    async reconnectServer(id) { try { await this.api('/admin/api/upstream/'+id+'/reconnect', { method:'POST' }); await this.refreshServers(); } catch(e) { alert('重连失败：' + (e.message || '未知错误')); } },
    async reorder(from, to) { try { await this.api('/admin/api/upstream/reorder', { method:'POST', body:JSON.stringify({fromIndex:from, toIndex:to}) }); await this.refreshServers(); } catch(e) { alert('排序失败：' + (e.message || '未知错误')); } },
    async setServerAggregation(s) {
      const id = s.id;
      if (!id || this.aggregationPending[id]) return;
      const enabled = s.aggregationEnabled === false;
      this.aggregationPending[id] = true;
      try {
        const result = await this.api(
          '/admin/api/upstream/' + encodeURIComponent(id) + '/aggregation',
          { method: 'PATCH', body: JSON.stringify({ aggregationEnabled: enabled }) }
        );
        this.abortRefresh();
        this.upstreamList = this.upstreamList.map(row => row.id === id
          ? { ...row, aggregationEnabled: result.aggregationEnabled } : row);
        this.stats.upstream = (this.stats.upstream || []).map(row => row.id === id
          ? { ...row, aggregationEnabled: result.aggregationEnabled } : row);
        if (this.currentPage === 'servers') await this.refreshServers(this.refreshSignal());
        if (this.currentPage === 'dashboard') await this.refreshDashboard(this.refreshSignal());
      } catch (e) {
        alert('聚合设置失败：' + (e.message || '未知错误'));
      } finally {
        delete this.aggregationPending[id];
        this.$nextTick(() => lucide.createIcons());
      }
    },
    openAddProxy() { this.proxyForm = { name:'', url:'' }; this.proxyTestState = { loading: false, result: null }; this.showProxyModal = true; },
    async testProxy(proxyUrl, targetUrl) {
      try {
        const res = await this.api('/admin/api/proxies/test', { method:'POST', body:JSON.stringify({ proxyUrl, targetUrl }) });
        return res;
      } catch(e) {
        return { success: false, latency: 0, error: (e && e.message) ? e.message : '请求失败' };
      }
    },
    async testProxyById(proxyId, targetUrl) {
      try {
        const res = await this.api('/admin/api/proxies/test', { method:'POST', body:JSON.stringify({ proxyId, targetUrl }) });
        return res;
      } catch(e) {
        return { success: false, latency: 0, error: (e && e.message) ? e.message : '请求失败' };
      }
    },
    async testProxyModal() {
      if (!this.proxyForm.url) { alert('请先填写代理 URL'); return; }
      this.proxyTestState.loading = true;
      this.proxyTestState.result = null;
      const r = await this.testProxy(this.proxyForm.url, 'https://www.google.com');
      this.proxyTestState.loading = false;
      this.proxyTestState.result = r;
    },
    async testProxyCard(p) {
      p.testing = true;
      const r1 = await this.testProxyById(p.id, 'https://www.google.com');
      const bound = this.upstreamList.find(s => s.proxyId === p.id);
      let msg = r1.success ? `谷歌连通: ✓ ${r1.latency}ms` : `谷歌连通: ✗ ${r1.error || 'HTTP '+r1.statusCode}`;
      if (bound) {
        const r2 = await this.testProxyById(p.id, bound.url);
        msg += '\n' + (r2.success ? `${bound.name}: ✓ ${r2.latency}ms` : `${bound.name}: ✗ ${r2.error || 'HTTP '+r2.statusCode}`);
      }
      p.testing = false;
      alert(msg);
    },
    async saveProxy() { try { await this.api('/admin/api/proxies', { method:'POST', body:JSON.stringify(this.proxyForm) }); this.showProxyModal = false; await this.refreshProxies(); } catch(e) { alert('添加失败：' + (e.message || '未知错误')); } },
    async deleteProxy(id) { if(!confirm('删除代理？')) return; try { await this.api('/admin/api/proxies/'+id, { method:'DELETE' }); await this.refreshProxies(); } catch(e) { alert('删除失败：' + (e.message || '未知错误')); } },
    async refreshUsers(signal) { try { this.userList = await this.api('/admin/api/users', { signal }); this.listErrors.users = ''; } catch(e) { if (this.isAbort(e)) return; this.listErrors.users = e.message || '加载用户列表失败'; } try { this.upstreamList = await this.api('/admin/api/upstream', { signal }); this.listErrors.upstream = ''; } catch(e) { if (this.isAbort(e)) return; this.listErrors.upstream = e.message || '加载服务器列表失败'; } this.$nextTick(()=>lucide.createIcons()); },
    openAddUser() { this.editUserId = null; this.userForm = { username:'', password:'', enabled:true, allowedServers:[], maxParentalRating:0, blockUnratedItems:false }; this.userLibraryGroups = []; this.showUserModal = true; this.$nextTick(()=>lucide.createIcons()); },
    editUser(u) {
      this.editUserId = u.id;
      this.userForm = { username:u.username, password:'', enabled:u.enabled, allowedServers: u.allowedServers ? [...u.allowedServers] : [], maxParentalRating: u.maxParentalRating || 0, blockUnratedItems: !!u.blockUnratedItems };
      this.showUserModal = true;
      // 折叠列表：只渲染服务器骨架，点击条目时才按需拉取库列表，避免服务器多时打开卡顿。
      const scope = this.userForm.allowedServers.length > 0
        ? this.upstreamList.filter(s => this.userForm.allowedServers.includes(s.id))
        : this.upstreamList;
      this.userLibraryGroups = this.buildLibraryGroupSkeletons(scope, u.hiddenLibraries);
      this.$nextTick(()=>lucide.createIcons());
    },
    async saveUser() {
      try {
        if (this.editUserId) {
          const body = {};
          if (this.userForm.username) body.username = this.userForm.username;
          if (this.userForm.password && (this.userForm.password.length < 8 || this.userForm.password.length > 128)) { alert('密码长度必须介于 8 和 128 之间'); return; }
          if (this.userForm.password) body.password = this.userForm.password;
          body.enabled = this.userForm.enabled;
          body.allowedServers = this.userForm.allowedServers.length > 0 ? this.userForm.allowedServers : null;
          body.maxParentalRating = parseInt(this.userForm.maxParentalRating || 0, 10);
          body.blockUnratedItems = !!this.userForm.blockUnratedItems;
          // 离线服务器不提交 key（后端保持原配置），在线服务器提交勾选结果（含空数组 = 全部显示）。
          body.hiddenLibraries = this.groupHiddenPayload(this.userLibraryGroups);
          await this.api('/admin/api/users/' + this.editUserId, { method:'PUT', body:JSON.stringify(body) });
        } else {
          if (!this.userForm.username || !this.userForm.password) { alert('用户名和密码不能为空'); return; }
          if (this.userForm.password.length < 8 || this.userForm.password.length > 128) { alert('密码长度必须介于 8 和 128 之间'); return; }
          const body = { username:this.userForm.username, password:this.userForm.password, allowedServers: this.userForm.allowedServers.length > 0 ? this.userForm.allowedServers : null, maxParentalRating: parseInt(this.userForm.maxParentalRating || 0, 10), blockUnratedItems: !!this.userForm.blockUnratedItems };
          await this.api('/admin/api/users', { method:'POST', body:JSON.stringify(body) });
        }
        this.showUserModal = false; await this.refreshUsers();
      } catch(e) { alert('保存失败：' + (e.message || '未知错误')); }
    },
    // 折叠列表骨架：只含服务器元信息与已存隐藏项，库列表等展开时再按需拉取。
    buildLibraryGroupSkeletons(servers, hidden) {
      hidden = hidden || {};
      return this.decorateLibraryGroups((servers || []).map(s => ({
        serverId: s.id,
        name: s.name,
        online: !!s.online,
        libraries: [],
        hiddenIds: (hidden[s.id] || []).slice(),
        loaded: false, // 本次面板是否成功拉取过；未成功拉取则不提交 key，后端保留其已存配置
      })));
    },
    decorateLibraryGroups(groups) {
      for (const g of (groups || [])) { g.expanded = false; g.loading = false; g.error = ''; }
      return groups;
    },
    // 展开/折叠某个服务器；首次展开时按需拉取库列表，之后复用内存结果。
    async toggleLibraryGroup(g) {
      if (g.expanded) { g.expanded = false; return; }
      g.expanded = true;
      if (!g.loaded && g.libraries.length === 0 && !g.loading) await this.fetchLibraryGroup(g);
      this.$nextTick(()=>lucide.createIcons());
    },
    async fetchLibraryGroup(g, refresh) {
      if (g.loading) return;
      g.loading = true; g.error = '';
      try {
        const libraries = await this.api('/admin/api/upstream/' + g.serverId + '/libraries' + (refresh ? '?refresh=1' : ''));
        g.libraries = libraries || [];
        g.online = true;
        g.loaded = true;
        g.hiddenIds = g.hiddenIds.filter(id => g.libraries.some(l => l.id === id));
      } catch (e) {
        g.online = false;
        g.error = (e && e.message) ? e.message : '加载失败';
      } finally {
        g.loading = false;
        this.$nextTick(()=>lucide.createIcons());
      }
    },
    async refreshLibraryGroup(g) { await this.fetchLibraryGroup(g, true); },
    toggleGroupAll(g, val) { g.hiddenIds = val ? g.libraries.map(l => l.id) : []; },
    isGroupAllHidden(g) { return g.libraries.length > 0 && g.libraries.every(l => g.hiddenIds.includes(l.id)); },
    groupHiddenPayload(groups) {
      const payload = {};
      for (const g of groups) {
        if (!g.loaded || g.error) continue; // 未成功加载的服务器不提交 key，后端不触碰其已存配置
        payload[g.serverId] = g.hiddenIds.slice();
      }
      return payload;
    },
    async openAdminHome() {
      this.adminHomeOpen = true;
      this.adminLibraryGroups = [];
      this.libraryGroupsLoading = true;
      try {
        const hidden = (await this.api('/admin/api/home-libraries')).hidden || {};
        this.adminLibraryGroups = this.buildLibraryGroupSkeletons(this.upstreamList, hidden);
      } finally {
        this.libraryGroupsLoading = false;
        this.$nextTick(()=>lucide.createIcons());
      }
    },
    async saveAdminHome() {
      try {
        await this.api('/admin/api/home-libraries', { method:'PUT', body:JSON.stringify({ hidden: this.groupHiddenPayload(this.adminLibraryGroups) }) });
        this.adminHomeOpen = false;
      } catch(e) { alert('保存失败：' + (e.message || '未知错误')); }
    },
    async toggleUser(u) { try { const enabled = !u.enabled; await this.api('/admin/api/users/' + u.id, { method:'PUT', body:JSON.stringify({enabled}) }); await this.refreshUsers(); } catch(e) { alert('操作失败：' + (e.message || '未知错误')); } },
    async deleteUser(id) { if(!confirm('删除用户？该操作不可撤销。')) return; try { await this.api('/admin/api/users/' + id, { method:'DELETE' }); await this.refreshUsers(); } catch(e) { alert('删除失败：' + (e.message || '未知错误')); } },
    async refreshServerUAOptions() { try { const ua = await this.api('/admin/api/ua'); this.uaResident = ua.resident || []; this.uaServers = ua.servers || []; } catch(e) {} },
    async refreshUAs(signal) {
      this.uaLoading = true; this.uaError = '';
      try {
        const data = await this.api('/admin/api/ua', { signal });
        this.uaRecent = data.recent || []; this.uaResident = data.resident || []; this.uaServers = data.servers || [];
      } catch(e) {
        if (this.isAbort(e)) return;
        this.uaError = e.message || '加载失败'; this.uaRecent = []; this.uaResident = []; this.uaServers = [];
      } finally { this.uaLoading = false; this.$nextTick(()=>lucide.createIcons()); }
    },
    formatUATime(v) { if(!v) return '—'; const d = new Date(v); return isNaN(d.getTime()) ? '—' : d.toLocaleString(); },
    uaAgeText(item) { if(!item || !item.ageKnown) return '采集时间未知，建议重新采集'; return '采集于 ' + item.ageDays + ' 天前'; },
    uaSummary(item) { const ua = (item && item.userAgent) || ''; return ua.length > 60 ? ua.slice(0, 60) + '…' : ua; },
    uaClientLabel(item) { if(!item) return '未知客户端'; const parts = []; if(item.client) parts.push(item.client); if(item.clientVersion) parts.push(item.clientVersion); return parts.join(' ') || '未知客户端'; },
    boundServerNames(servers) { return (servers || []).map(s => s.name).join('、'); },
    residentOptionLabel(r) { return r.name + ' · ' + this.uaClientLabel(r) + (r.isOld ? '（采集较早）' : ''); },
    selectedResidentIsOld() { if(this.serverForm.spoofClient !== 'resident') return false; const r = (this.uaResident || []).find(x => x.id === this.serverForm.uaProfileId); return !!(r && r.isOld); },
    selectedResidentMissing() {
      if (this.serverForm.spoofClient !== 'resident') return false;
      if (!this.serverForm.uaProfileId) return false;
      return !(this.uaResident || []).some(x => x.id === this.serverForm.uaProfileId);
    },
    openUADetail(item) { this.uaDetail = item; this.$nextTick(()=>lucide.createIcons()); },
    async copyText(text) { if(!text) return; try { await navigator.clipboard.writeText(text); } catch(e) {} },
    openSaveResident(recent) { this.uaSaveDialog = { rename: false, recentId: recent.id, name: this.uaClientLabel(recent) }; },
    openRenameResident(r) { this.uaSaveDialog = { rename: true, id: r.id, name: r.name, expectedRevision: r.revision }; },
    async saveRecentUA() {
      if (this.uaPending.save) return;
      if(!this.uaSaveDialog) return; const d = this.uaSaveDialog; this.uaPending.save = true;
      try { const res = await this.api('/admin/api/ua/resident', { method:'POST', body: JSON.stringify({ recentId: d.recentId, name: d.name }) }); this.uaSaveDialog = null; await this.refreshUAs(); if (res.alreadySaved) alert('该身份已存在常驻 UA'); }
      catch(e) { alert('保存失败：' + e.message); }
      finally { this.uaPending.save = false; }
    },
    async renameResidentUA() {
      if (this.uaPending.save) return;
      const d = this.uaSaveDialog; if(!d || !d.rename) return; this.uaPending.save = true;
      try { await this.api('/admin/api/ua/resident/' + d.id, { method:'PATCH', body: JSON.stringify({ name: d.name, expectedRevision: d.expectedRevision }) }); this.uaSaveDialog = null; await this.refreshUAs(); }
      catch(e) { alert('重命名失败：' + e.message); }
      finally { this.uaPending.save = false; }
    },
    openReplaceResident(r) { this.uaReplaceDialog = { id: r.id, name: r.name, revision: r.revision, recentId: '' }; },
    async replaceResidentUA() {
      if (this.uaPending.replace) return;
      const d = this.uaReplaceDialog; if(!d) return; if(!d.recentId) { alert('请选择一条最近采集'); return; } this.uaPending.replace = true;
      try { await this.api('/admin/api/ua/resident/' + d.id + '/replace', { method:'POST', body: JSON.stringify({ recentId: d.recentId, expectedRevision: d.revision }) }); this.uaReplaceDialog = null; await this.refreshUAs(); await this.refreshServers(); this.startUAReconnectPoll(); }
      catch(e) { alert('更新失败：' + e.message); }
      finally { this.uaPending.replace = false; }
    },
    async deleteResidentUA(r) {
      if (this.uaPending.delete) return;
      if (!confirm('删除常驻 UA「' + r.name + '」？')) return;
      this.uaPending.delete = true;
      try { await this.api('/admin/api/ua/resident/' + r.id, { method: 'DELETE', body: JSON.stringify({ expectedRevision: r.revision }) }); await this.refreshUAs(); }
      catch (e) { if (e.status === 409 && e.data && e.data.boundServers) { alert('该常驻 UA 正在被以下服务器引用，无法删除：' + e.data.boundServers.map(s => s.name).join('、')); } else { alert('删除失败：' + e.message); } }
      finally { this.uaPending.delete = false; }
    },
    openUAAssignments(r) {
      const bound = {}; (r.boundServers || []).forEach(s => { bound[s.id] = true; });
      this.uaAssignmentDialog = { id: r.id, name: r.name, revision: r.revision,
        rows: (this.uaServers || []).map(s => ({ id: s.id, name: s.name, currentSpoof: s.spoofClient, currentProfile: s.uaProfileId || '', bound: !!bound[s.id], checked: !!bound[s.id] })) };
    },
    async saveUAAssignments() {
      if (this.uaPending.assign) return;
      const d = this.uaAssignmentDialog; if(!d) return;
      const assignServerIds = []; const releaseServerIds = []; const expectedBindings = [];
      for (const row of d.rows) {
        expectedBindings.push({ serverId: row.id, spoofClient: row.currentSpoof, uaProfileId: row.currentProfile || '' });
        if (row.checked && !row.bound) assignServerIds.push(row.id);
        if (!row.checked && row.bound) releaseServerIds.push(row.id);
      }
      if (assignServerIds.length === 0 && releaseServerIds.length === 0) { alert('没有变更'); return; }
      this.uaPending.assign = true;
      try { await this.api('/admin/api/ua/resident/' + d.id + '/assignments', { method:'POST', body: JSON.stringify({ assignServerIds, releaseServerIds, expectedProfileRevision: d.revision, expectedBindings }) }); this.uaAssignmentDialog = null; await this.refreshUAs(); await this.refreshServers(); this.startUAReconnectPoll(); }
      catch(e) { alert('保存失败：' + e.message); }
      finally { this.uaPending.assign = false; }
    },
    // 方案 §8.1：绑定或更新成功后存在重连任务时，每 2 秒短时刷新服务器
    // 状态，最多 30 秒；离页或登出取消。轮询停止不等于任务失败，仍在
    // 重连的状态保留，用户可手动刷新。
    startUAReconnectPoll() {
      this.stopUAReconnectPoll();
      const deadline = Date.now() + 30000;
      this._uaPollTimer = setInterval(async () => {
        if (!this.isLoggedIn || Date.now() >= deadline) { this.stopUAReconnectPoll(); return; }
        try { this.upstreamList = await this.api('/admin/api/upstream'); this.$nextTick(() => lucide.createIcons()); } catch (e) { /* 轮询失败不弹窗，下一轮重试 */ }
      }, 2000);
    },
    stopUAReconnectPoll() { if (this._uaPollTimer) { clearInterval(this._uaPollTimer); this._uaPollTimer = null; } },
  }
}).mount('#app');
