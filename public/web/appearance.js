/* Emby-in-One Web client - appearance entry points (T1).
 *
 * One controller coordinates the theme engine with the login, desktop and
 * mobile entry points. It only changes appearance state: no routing, auth,
 * player load/destroy/seek or subtitle work happens here.
 */
(function () {
  'use strict';

  const root = typeof window !== 'undefined' ? window : globalThis;
  const MODE_OPTIONS = Object.freeze([
    { id: 'system', label: '跟随系统', hint: '只跟随系统明暗设置，不按时间切换' },
    { id: 'light', label: '明亮' },
    { id: 'dark', label: '暗色' },
  ]);

  function createTheme(options) {
    if (options.theme) return options.theme;
    if (!root.EIOTheme || typeof root.EIOTheme.createController !== 'function') {
      throw new Error('EIOTheme is required before EIOAppearance');
    }
    return root.EIOTheme.createController(options.themeOptions || options);
  }

  function createController(options) {
    options = options || {};
    const Vue = root.Vue || {};
    const theme = createTheme(options);
    const initialPreference = theme.getPreference() || { themeId: null, mode: 'dark' };
    const initialResolved = theme.getResolved() || { variant: 'dark', themeId: 'builtin', notice: '' };
    const seed = {
      open: false,
      themeId: initialPreference.themeId,
      mode: initialPreference.mode,
      resolvedVariant: initialResolved.variant,
      resolvedThemeId: initialResolved.themeId,
      themeOptions: theme.getThemeOptions ? theme.getThemeOptions() : [],
      modeOptions: MODE_OPTIONS.slice(),
      notice: initialResolved.notice || '',
      remoteStatus: theme.getRemoteStatus ? theme.getRemoteStatus() : 'local',
      saving: false,
    };
    const state = typeof Vue.reactive === 'function' ? Vue.reactive(seed) : seed;
    let unsubscribe = null;
    let destroyed = false;
    let keydownAttached = false;
    let saveTimer = null;
    let saveChain = Promise.resolve();
    // Bumped on every session entry/leave (including re-entering the same ID).
    // The theme generation is also part of the epoch so a direct theme switch
    // is observed as well.
    let identityEpoch = 0;

    function currentEpoch() {
      const generation = typeof theme.getGeneration === 'function' ? theme.getGeneration() : 0;
      const identity = typeof theme.getIdentity === 'function' ? theme.getIdentity() : null;
      return { local: identityEpoch, generation: generation, identity: identity };
    }

    function epochMatches(epoch) {
      if (destroyed || !epoch) return false;
      if (epoch.local !== identityEpoch) return false;
      const generation = typeof theme.getGeneration === 'function' ? theme.getGeneration() : 0;
      if (epoch.generation !== generation) return false;
      const identity = typeof theme.getIdentity === 'function' ? theme.getIdentity() : null;
      return epoch.identity === identity;
    }

    function sync() {
      if (destroyed) return;
      const preference = theme.getPreference() || { themeId: null, mode: 'dark' };
      const resolved = theme.getResolved() || { variant: 'dark', themeId: 'builtin', notice: '' };
      state.themeId = preference.themeId;
      state.mode = preference.mode;
      state.resolvedVariant = resolved.variant;
      state.resolvedThemeId = resolved.themeId;
      state.themeOptions = theme.getThemeOptions ? theme.getThemeOptions() : [];
      state.notice = resolved.notice || '';
      state.remoteStatus = theme.getRemoteStatus ? theme.getRemoteStatus() : 'local';
    }

    function onDocumentKeydown(event) {
      if (!destroyed && event && event.key === 'Escape' && state.open) state.open = false;
    }

    function attachKeydown() {
      if (keydownAttached || destroyed) return;
      if (root.document && typeof root.document.addEventListener === 'function') {
        root.document.addEventListener('keydown', onDocumentKeydown);
        keydownAttached = true;
      }
    }

    function detachKeydown() {
      if (!keydownAttached) return;
      keydownAttached = false;
      if (root.document && typeof root.document.removeEventListener === 'function') {
        root.document.removeEventListener('keydown', onDocumentKeydown);
      }
    }

    function open() {
      if (destroyed) return;
      state.open = true;
      attachKeydown();
    }
    function close() { if (!destroyed) state.open = false; }
    function toggle() {
      if (destroyed) return;
      if (state.open) close(); else open();
    }

    function clearSaveTimer() {
      if (saveTimer == null) return;
      clearTimeout(saveTimer);
      saveTimer = null;
    }

    function canPersist() {
      if (typeof theme.saveRemote !== 'function') return false;
      const identity = typeof theme.getIdentity === 'function' ? theme.getIdentity() : null;
      if (!identity) return false;
      const preference = typeof theme.getPreference === 'function' ? theme.getPreference() : null;
      // A CAS PUT needs the positive revision a GET seeded. Without one the
      // change stays local instead of guessing a revision.
      if (!preference || !(preference.revision >= 1)) return false;
      const status = typeof theme.getRemoteStatus === 'function' ? theme.getRemoteStatus() : 'local';
      return status !== 'loading' && status !== 'local';
    }

    function scheduleSave() {
      clearSaveTimer();
      if (destroyed || !canPersist()) return;
      const epoch = currentEpoch();
      saveTimer = setTimeout(function () {
        saveTimer = null;
        if (!epochMatches(epoch)) return;
        enqueueSave(epoch);
      }, 220);
    }

    function afterChange() {
      sync();
      scheduleSave();
    }

    function runSave(epoch) {
      if (destroyed || !epochMatches(epoch)) return Promise.resolve({ saved: false, stale: true });
      state.saving = true;
      return Promise.resolve(theme.saveRemote({ timeoutMs: 3000 })).then(function (result) {
        // Every completion must belong to the session that started it. An old
        // account may not touch the new account's notice, saving flag or
        // trigger a refresh for the new account.
        if (destroyed || !epochMatches(epoch)) return result;
        sync();
        state.saving = false;
        if (result && result.saved === true) {
          state.notice = '已保存到账号';
          return result;
        }
        if (result && result.superseded === true) {
          // The server accepted an older selection; the queued save for the
          // latest local intent still has to run.
          state.notice = '已保存较早的选择，最新选择尚未保存';
          return result;
        }
        if (result && result.stale === true) return result;
        if (result && result.error && result.error.status === 409) {
          // A revision conflict is a real server change: reload once and show
          // the server state. Never pretend it was saved and never retry in a
          // loop.
          state.notice = '外观已在其他设备修改，正在重新加载';
          return Promise.resolve(theme.refreshRemote({ timeoutMs: 3000 })).then(function () {
            if (destroyed || !epochMatches(epoch)) return result;
            sync();
            state.notice = '外观已在其他设备修改，已重新加载';
            return result;
          }, function () {
            if (destroyed || !epochMatches(epoch)) return result;
            state.notice = '外观已在其他设备修改，请刷新重试';
            return result;
          });
        }
        if (result && result.error && result.error.name === 'AbortError') {
          state.notice = '保存超时，已保留本机选择';
          return result;
        }
        state.notice = '未能保存到账号，已保留本机选择';
        return result;
      }, function () {
        if (!destroyed && epochMatches(epoch)) {
          sync();
          state.saving = false;
          state.notice = '未能保存到账号，已保留本机选择';
        }
        return { saved: false };
      });
    }

    function enqueueSave(epoch) {
      const task = function () {
        if (!epochMatches(epoch)) return { saved: false, stale: true };
        return runSave(epoch);
      };
      saveChain = saveChain.then(task, task);
      return saveChain;
    }

    function persist() {
      if (destroyed) return Promise.resolve({ saved: false });
      clearSaveTimer();
      return enqueueSave(currentEpoch());
    }

    function chooseMode(mode) {
      if (destroyed) return;
      if (!MODE_OPTIONS.some(function (option) { return option.id === mode; })) return;
      theme.setPreference({ mode: mode });
      afterChange();
    }

    function chooseTheme(themeId) {
      if (destroyed) return;
      theme.setPreference({ themeId: themeId == null || themeId === '' ? null : themeId });
      afterChange();
    }

    function resetBuiltin() {
      if (destroyed) return;
      theme.setPreference({ themeId: 'builtin' });
      afterChange();
      state.notice = '已恢复内置主题';
    }

    function enterSession(userId) {
      if (destroyed) return;
      clearSaveTimer();
      identityEpoch += 1;
      state.saving = false;
      theme.enterSession(userId);
      sync();
    }

    function leaveSession() {
      if (destroyed) return;
      clearSaveTimer();
      identityEpoch += 1;
      state.saving = false;
      theme.enterSession(null);
      sync();
    }

    function syncIdentity(userId) {
      if (destroyed) return;
      clearSaveTimer();
      const before = typeof theme.getIdentity === 'function' ? theme.getIdentity() : null;
      state.saving = false;
      theme.syncIdentity(userId);
      const after = typeof theme.getIdentity === 'function' ? theme.getIdentity() : null;
      if (after !== before) identityEpoch += 1;
      sync();
    }

    function refreshRemote(remoteOptions) {
      if (destroyed) return Promise.resolve();
      const epoch = currentEpoch();
      return Promise.resolve(theme.refreshRemote(remoteOptions)).then(function (value) {
        if (!destroyed && epochMatches(epoch)) sync();
        return value;
      }, function () {
        if (!destroyed && epochMatches(epoch)) sync();
        return undefined;
      });
    }

    function destroy() {
      if (destroyed) return;
      destroyed = true;
      clearSaveTimer();
      state.saving = false;
      detachKeydown();
      if (unsubscribe) unsubscribe();
      unsubscribe = null;
      if (theme.destroy) theme.destroy();
    }

    if (typeof theme.subscribe === 'function') unsubscribe = theme.subscribe(sync);

    return {
      state: state,
      theme: theme,
      open: open,
      close: close,
      toggle: toggle,
      chooseMode: chooseMode,
      chooseTheme: chooseTheme,
      resetBuiltin: resetBuiltin,
      enterSession: enterSession,
      leaveSession: leaveSession,
      syncIdentity: syncIdentity,
      refreshRemote: refreshRemote,
      save: persist,
      destroy: destroy,
      isDestroyed: function () { return destroyed; },
    };
  }

  const EioAppearance = {
    name: 'EioAppearance',
    props: {
      controller: { type: Object, required: true },
      surface: { type: String, default: 'sidebar' },
    },
    computed: {
      surfaceClass() { return 'is-' + (this.surface || 'sidebar'); },
      schemeLabel() {
        const resolved = this.controller.state.resolvedVariant === 'light' ? '明亮' : '暗色';
        return '当前显示：' + resolved;
      },
      themeLabel() {
        const options = this.controller.state.themeOptions || [];
        const selected = options.find(function (option) { return option.id === this.controller.state.themeId; }, this);
        return selected ? selected.label : '外观';
      },
      syncLabel() {
        const source = this.controller.theme;
        let identity = null;
        try { identity = source && source.getIdentity ? source.getIdentity() : null; }
        catch (e) { identity = null; }
        if (!identity) return '未登录：仅保存到本机';
        if (this.controller.state.remoteStatus === 'ok') return '已连接：按账号保存';
        if (this.controller.state.remoteStatus === 'loading') return '正在同步账号外观…';
        return '离线：本次仅保存到本机';
      },
    },
    template: `
      <div class="eio-appearance" :class="surfaceClass" @click.stop>
        <button class="eio-btn eio-btn-sm eio-appearance-trigger" type="button"
                :aria-expanded="controller.state.open ? 'true' : 'false'"
                aria-haspopup="dialog" :title="themeLabel" @click="controller.toggle()">
          <svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor"
               stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
            <circle cx="12" cy="12" r="4"></circle>
            <path d="M12 2v2M12 20v2M4.93 4.93l1.42 1.42M17.66 17.66l1.41 1.41M2 12h2M20 12h2M4.93 19.07l1.42-1.42M17.66 6.34l1.41-1.41"></path>
          </svg>
          <span>外观</span>
        </button>
        <div v-if="controller.state.open" class="eio-appearance-panel" role="dialog"
             aria-label="外观设置" @click.stop @keydown.esc="controller.close()">
          <div class="eio-appearance-head">
            <strong>外观</strong>
            <button class="eio-appearance-close" type="button" aria-label="关闭外观设置"
                    @click="controller.close()">&#10005;</button>
          </div>
          <div class="eio-appearance-group">
            <div class="eio-appearance-label">主题</div>
            <select class="eio-select eio-appearance-select"
                    :value="controller.state.themeId == null ? '' : controller.state.themeId"
                    aria-label="选择主题"
                    @change="controller.chooseTheme($event.target.value || null)">
              <option v-for="option in controller.state.themeOptions" :key="String(option.id)"
                      :value="option.id == null ? '' : option.id">{{ option.label }}</option>
            </select>
          </div>
          <div class="eio-appearance-group">
            <div class="eio-appearance-label">明暗</div>
            <div class="eio-appearance-modes" role="group" aria-label="明暗模式">
              <button v-for="option in controller.state.modeOptions" :key="option.id" type="button"
                      class="eio-appearance-mode" :class="{ active: controller.state.mode === option.id }"
                      :aria-pressed="controller.state.mode === option.id ? 'true' : 'false'"
                      :title="option.hint || option.label" @click="controller.chooseMode(option.id)">
                {{ option.label }}
              </button>
            </div>
            <div class="eio-appearance-resolved">{{ schemeLabel }}</div>
          </div>
          <div v-if="controller.state.saving" class="eio-appearance-resolved" role="status">
            正在保存到账号…
          </div>
          <div v-if="controller.state.notice" class="eio-appearance-notice" role="status">
            {{ controller.state.notice }}
          </div>
          <div class="eio-appearance-foot">
            <button class="eio-appearance-reset" type="button" @click="controller.resetBuiltin()">
              恢复内置主题
            </button>
            <span class="eio-appearance-meta">{{ syncLabel }}</span>
          </div>
        </div>
      </div>`,
  };

  function register(app) {
    if (!app || typeof app.component !== 'function') return;
    app.component('EioAppearance', EioAppearance);
  }

  root.EIOAppearance = {
    MODE_OPTIONS: MODE_OPTIONS,
    component: EioAppearance,
    createController: createController,
    register: register,
  };
})();
