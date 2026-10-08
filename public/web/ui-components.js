/* Emby-in-One Web client — reusable Vue components (v1.4.4-rc3).
 *
 * Registered on the app before mount. Components are presentation-only:
 * they never call api()/apiRaw() and never add identity headers. The caller
 * injects data and handles side effects through emitted events.
 */
(function () {
  'use strict';

  const STATE_TEXT = {
    loading: '正在加载…',
    slow: '加载较慢，请稍候…',
    empty: '暂无内容',
    error: '加载失败',
  };

  const EioPosterCard = {
    name: 'EioPosterCard',
    props: {
      item: { type: Object, required: true },
      imageSrc: { type: String, default: '' },
      detailHref: { type: String, default: '' },
      showProgress: { type: Boolean, default: false },
      sub: { type: String, default: '' },
    },
    emits: ['play'],
    data() { return { failed: false }; },
    watch: { imageSrc() { this.failed = false; } },
    computed: {
      initial() { return String(this.item.Name || '?').trim().charAt(0) || '?'; },
      ratio() {
        const duration = this.item.RunTimeTicks || 0;
        if (!duration) return 0;
        const pos = this.item.UserData && this.item.UserData.PlaybackPositionTicks || 0;
        return Math.max(0, Math.min(1, pos / duration));
      },
      played() { return !!(this.item.UserData && this.item.UserData.Played); },
      showBar() { return this.showProgress && !this.played && this.ratio > 0 && this.ratio < 0.95; },
    },
    template: `
      <article class="eio-card">
        <a class="eio-card-link" :href="detailHref" :aria-label="item.Name"></a>
        <div class="eio-card-media">
          <img v-if="imageSrc && !failed" :src="imageSrc" :alt="item.Name" loading="lazy" @error="failed = true">
          <div v-else class="eio-card-placeholder">{{ initial }}</div>
          <span v-if="played" class="eio-card-played">&#10003;</span>
          <button class="eio-card-play" type="button" :aria-label="'播放 ' + item.Name" @click="$emit('play', item)">
            <svg viewBox="0 0 24 24" width="18" height="18" fill="currentColor" aria-hidden="true"><path d="M8 5v14l11-7z"/></svg>
          </button>
        </div>
        <div class="eio-card-body">
          <div class="eio-card-title" :title="item.Name">{{ item.Name }}</div>
          <div v-if="sub" class="eio-card-sub">{{ sub }}</div>
          <div v-else-if="item.ProductionYear" class="eio-card-sub">{{ item.ProductionYear }}</div>
          <div v-if="showBar" class="eio-card-progress"><i :style="{ width: (ratio * 100) + '%' }"></i></div>
        </div>
      </article>`,
  };

  const EioStateBlock = {
    name: 'EioStateBlock',
    props: {
      state: { type: String, default: 'loading' },
      message: { type: String, default: '' },
      actionLabel: { type: String, default: '' },
    },
    emits: ['retry'],
    computed: {
      text() { return this.message || STATE_TEXT[this.state] || ''; },
      isError() { return this.state === 'error'; },
      isBusy() { return this.state === 'loading' || this.state === 'slow'; },
    },
    template: `
      <div class="eio-state" :class="{ error: isError }">
        <div v-if="isBusy" class="eio-spinner"></div>
        <div class="eio-state-msg">{{ text }}</div>
        <div v-if="isError" class="eio-state-error-actions">
          <button class="eio-btn eio-btn-primary" type="button" @click="$emit('retry')">{{ actionLabel || '重试' }}</button>
          <slot name="actions"></slot>
        </div>
        <button v-else-if="actionLabel" class="eio-btn eio-btn-sm" type="button" @click="$emit('retry')">{{ actionLabel }}</button>
      </div>`,
  };

  const EioToastStack = {
    name: 'EioToastStack',
    props: { items: { type: Array, default: () => [] } },
    emits: ['dismiss', 'hold', 'release'],
    template: `
      <div class="eio-toasts" role="region" aria-label="通知">
        <div v-for="item in items" :key="item.id" class="eio-toast" :class="item.kind"
             :role="item.kind === 'error' ? 'alert' : 'status'" aria-atomic="true"
             @mouseenter="$emit('hold', item.id)" @mouseleave="$emit('release', item.id)"
             @focusin="$emit('hold', item.id)" @focusout="$emit('release', item.id)">
          <span class="eio-toast-msg">{{ item.message }}</span>
          <button class="eio-toast-close" type="button" aria-label="关闭通知" @click="$emit('dismiss', item.id)">&#10005;</button>
        </div>
      </div>`,
  };

  const EioTrackMenu = {
    name: 'EioTrackMenu',
    props: {
      label: { type: String, default: '' },
      options: { type: Array, default: () => [] },
      value: { type: [String, Number], default: null },
      disabled: { type: Boolean, default: false },
      open: { type: Boolean, default: false },
      menuId: { type: String, default: '' },
    },
    emits: ['toggle', 'select', 'close'],
    template: `
      <div class="eio-menu-btn">
        <button type="button" class="eio-play-btn" :aria-label="label"
                :aria-expanded="open ? 'true' : 'false'" :aria-controls="menuId"
                :disabled="disabled" @click="$emit('toggle')">
          <slot name="trigger">{{ label }}</slot>
        </button>
        <div v-if="open" class="eio-menu" :id="menuId" role="menu" @keydown.esc="$emit('close')">
          <button v-for="opt in options" :key="opt.value" type="button" role="menuitemradio"
                  :aria-checked="opt.value === value ? 'true' : 'false'"
                  class="eio-menu-item" :class="{ selected: opt.value === value }"
                  :title="opt.label" @click="$emit('select', opt.value)">
            <span class="eio-menu-check" aria-hidden="true">{{ opt.value === value ? '\\u2713' : '' }}</span>
            <span class="eio-menu-label">{{ opt.label }}</span>
            <span v-if="opt.meta" class="eio-muted">{{ opt.meta }}</span>
          </button>
        </div>
      </div>`,
  };

  window.EIOComponents = {
    register(app) {
      if (!app || typeof app.component !== 'function') return;
      app.component('EioPosterCard', EioPosterCard);
      app.component('EioStateBlock', EioStateBlock);
      app.component('EioToastStack', EioToastStack);
      app.component('EioTrackMenu', EioTrackMenu);
    },
  };
})();
