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
        {id:'users', name:'用户管理', icon:'users'},
        {id:'proxies', name:'网络代理', icon:'shield'},
        {id:'settings', name:'全局设置', icon:'settings'},
        {id:'logs', name:'运行日志', icon:'terminal'}
      ],
      stats: { upstreamCount: 0, upstreamOnline: 0, idMappings: { mappingCount: 0, persistent: true }, upstream: [] },
      upstreamList: [], proxyList: [],
      settings: { serverName: '', playbackMode: 'proxy', adminUsername: '', adminPassword: '', currentPassword: '', timeouts: { api: 30000, global: 15000, login: 10000, healthCheck: 10000, healthInterval: 60000, searchGracePeriod: 3000, metadataGracePeriod: 3000, latestGracePeriod: 0 } },
      logs: [], isLoadingLogs: false, saveSuccess: false,
      logLevelFilter: 'ALL', logSearch: '',
      showModal: false, editIndex: null, serverForm: {},
      showProxyModal: false, proxyForm: { name: '', url: '' },
      proxyTestState: { loading: false, result: null },
      userList: [], showUserModal: false, editUserId: null, userForm: { username: '', password: '', enabled: true, allowedServers: [] }
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
    currentPage(v) { if(this._refreshTimer) clearTimeout(this._refreshTimer); this._refreshTimer = setTimeout(()=>{this.refresh(); this.$nextTick(() => lucide.createIcons());}, 50); }
  },
  mounted() { const t = localStorage.getItem('eio_token'); if(t) this.checkAuth(t); this.$nextTick(() => lucide.createIcons()); },
  methods: {
    navigateTo(id) { this.currentPage = id; this.showSidebar = false; },
    async api(path, opts = {}) {
      const t = localStorage.getItem('eio_token');
      const h = { 'Content-Type': 'application/json' };
      if(t) h['X-Emby-Token'] = t;
      const r = await fetch(path, { ...opts, headers: h });
      if(r.status === 401) { this.logout(); throw new Error('Unauthorized'); }
      if(r.status === 204 || r.headers.get('content-length') === '0') return { success: true };
      const data = await r.json();
      if(!r.ok) throw new Error(data.error || data.message || ('HTTP ' + r.status));
      return data;
    },
    async checkAuth() { try { const s = await this.api('/admin/api/status'); if(!s || s.error) { this.logout(); return; } this.isLoggedIn = true; this.refresh(); this.refreshClientInfo(); } catch(e) { this.logout(); } },
    async doLogin() { try {
      const r = await fetch('/Users/AuthenticateByName', { method: 'POST', headers: {'Content-Type':'application/json'}, body: JSON.stringify({Username:this.loginForm.user, Pw:this.loginForm.pass}) });
      if(!r.ok) throw new Error(); const data = await r.json();
      localStorage.setItem('eio_token', data.AccessToken);
      try { await this.api('/admin/api/status'); } catch(e) { localStorage.removeItem('eio_token'); this.loginError = '需要管理员权限'; return; }
      this.isLoggedIn = true; this.refresh(); this.refreshClientInfo();
    } catch(e) { this.loginError = '用户名或密码错误'; } },
    logout() { this.isLoggedIn = false; localStorage.removeItem('eio_token'); },
    async copyClientUA() {
      if (!this.clientInfo || !this.clientInfo.userAgent) return;
      try { await navigator.clipboard.writeText(this.clientInfo.userAgent); alert('UA 已复制到剪贴板'); } catch(e) {}
    },
    refresh() {
      if(this.currentPage === 'dashboard') this.refreshDashboard();
      if(this.currentPage === 'servers') this.refreshServers();
      if(this.currentPage === 'users') this.refreshUsers();
      if(this.currentPage === 'proxies') this.refreshProxies();
      if(this.currentPage === 'settings') this.refreshSettings();
      if(this.currentPage === 'logs') this.refreshLogs();
    },
    async refreshDashboard() { try { this.stats = await this.api('/admin/api/status'); } catch(e) {} this.refreshClientInfo(); this.$nextTick(()=>lucide.createIcons()); },
    async refreshClientInfo() { try { this.clientInfo = await this.api('/admin/api/client-info'); } catch(e) {} this.$nextTick(()=>lucide.createIcons()); },
    async refreshServers() { try { this.upstreamList = await this.api('/admin/api/upstream'); } catch(e) { this.upstreamList = []; } try { await this.refreshProxies(); } catch(e) {} this.$nextTick(()=>lucide.createIcons()); },
    async refreshProxies() { try { this.proxyList = await this.api('/admin/api/proxies'); } catch(e) { this.proxyList = []; } this.$nextTick(()=>lucide.createIcons()); },
    async refreshSettings() { try { const s = await this.api('/admin/api/settings'); this.settings = { ...this.settings, ...s, adminPassword: '', currentPassword: '', timeouts: s.timeouts || this.settings.timeouts }; } catch(e) {} },
    async saveSettings() { try { const res = await this.api('/admin/api/settings', { method:'PUT', body:JSON.stringify(this.settings) }); this.saveSuccess = true; this.settings.adminPassword = ''; this.settings.currentPassword = ''; setTimeout(()=>this.saveSuccess=false,3000); } catch(e) { alert('保存失败：' + (e.message || '未知错误')); } },
    async refreshLogs() { this.isLoadingLogs = true; try { this.logs = await this.api('/admin/api/logs?limit=500'); this.$nextTick(() => { const b=this.$refs.logBox; if(b) b.scrollTop=b.scrollHeight; lucide.createIcons(); }); } catch(e) { this.logs = []; } finally { this.isLoadingLogs=false; } },
    downloadLogs() {
      const t = localStorage.getItem('eio_token');
      const a = document.createElement('a');
      a.href = '/admin/api/logs/download' + (t ? '?api_key=' + encodeURIComponent(t) : '');
      a.download = 'emby-in-one.log'; a.click();
    },
    async clearLogs() { if(!confirm('确认清空所有日志？')) return; try { await this.api('/admin/api/logs', { method:'DELETE' }); this.logs = []; } catch(e) { alert('清空失败：' + (e.message || '未知错误')); } },
    getProxyName(id) { const p = this.proxyList.find(x => x.id === id); return p ? p.name : '不使用'; },
    openAddServer() { this.editIndex = null; this.serverForm = { name:'', url:'', streamingUrl:'', authType:'password', spoofClient:'none', followRedirects:true, proxyId:null, priorityMetadata:false, maxConcurrent:0, customUserAgent:'', customClient:'', customClientVersion:'', customDeviceName:'', customDeviceId:'' }; this.showModal = true; },
    editServer(s) { this.editIndex = s.index; this.serverForm = { ...s, password:'', apiKey:'', maxConcurrent: s.maxConcurrent || 0, streamingUrl: s.streamingUrl || '', customUserAgent: s.customUserAgent || '', customClient: s.customClient || '', customClientVersion: s.customClientVersion || '', customDeviceName: s.customDeviceName || '', customDeviceId: s.customDeviceId || '' }; this.showModal = true; },
    async saveServer() {
      const m = this.editIndex === null ? 'POST' : 'PUT';
      try {
        const res = await this.api('/admin/api/upstream' + (this.editIndex===null?'':'/'+this.editIndex), { method:m, body:JSON.stringify(this.serverForm) });
        if (res.warning) { alert('提示：' + res.warning); }
        this.showModal = false;
        await this.refreshServers();
        await this.refreshDashboard();
      } catch (e) {
        alert('保存失败：' + (e.message || '未知错误'));
      }
    },
    async deleteServer(idx) { if(!confirm('删除服务器？')) return; try { await this.api('/admin/api/upstream/'+idx, { method:'DELETE' }); await this.refreshServers(); } catch(e) { alert('删除失败：' + (e.message || '未知错误')); } },
    async reconnectServer(idx) { try { await this.api('/admin/api/upstream/'+idx+'/reconnect', { method:'POST' }); await this.refreshServers(); } catch(e) { alert('重连失败：' + (e.message || '未知错误')); } },
    async reorder(from, to) { try { await this.api('/admin/api/upstream/reorder', { method:'POST', body:JSON.stringify({fromIndex:from, toIndex:to}) }); await this.refreshServers(); } catch(e) { alert('排序失败：' + (e.message || '未知错误')); } },
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
    async refreshUsers() { try { this.userList = await this.api('/admin/api/users'); } catch(e) { this.userList = []; } try { this.upstreamList = await this.api('/admin/api/upstream'); } catch(e) {} this.$nextTick(()=>lucide.createIcons()); },
    openAddUser() { this.editUserId = null; this.userForm = { username:'', password:'', enabled:true, allowedServers:[] }; this.showUserModal = true; this.$nextTick(()=>lucide.createIcons()); },
    editUser(u) { this.editUserId = u.id; this.userForm = { username:u.username, password:'', enabled:u.enabled, allowedServers: u.allowedServers ? [...u.allowedServers] : [] }; this.showUserModal = true; this.$nextTick(()=>lucide.createIcons()); },
    async saveUser() {
      try {
        if (this.editUserId) {
          const body = {};
          if (this.userForm.username) body.username = this.userForm.username;
          if (this.userForm.password) body.password = this.userForm.password;
          body.enabled = this.userForm.enabled;
          body.allowedServers = this.userForm.allowedServers.length > 0 ? this.userForm.allowedServers : null;
          await this.api('/admin/api/users/' + this.editUserId, { method:'PUT', body:JSON.stringify(body) });
        } else {
          if (!this.userForm.username || !this.userForm.password) { alert('用户名和密码不能为空'); return; }
          const body = { username:this.userForm.username, password:this.userForm.password, allowedServers: this.userForm.allowedServers.length > 0 ? this.userForm.allowedServers : null };
          await this.api('/admin/api/users', { method:'POST', body:JSON.stringify(body) });
        }
        this.showUserModal = false; await this.refreshUsers();
      } catch(e) { alert('保存失败：' + (e.message || '未知错误')); }
    },
    async toggleUser(u) { try { const enabled = !u.enabled; await this.api('/admin/api/users/' + u.id, { method:'PUT', body:JSON.stringify({enabled}) }); await this.refreshUsers(); } catch(e) { alert('操作失败：' + (e.message || '未知错误')); } },
    async deleteUser(id) { if(!confirm('删除用户？该操作不可撤销。')) return; try { await this.api('/admin/api/users/' + id, { method:'DELETE' }); await this.refreshUsers(); } catch(e) { alert('删除失败：' + (e.message || '未知错误')); } }
  }
}).mount('#app');
