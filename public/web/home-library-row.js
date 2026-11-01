/* Emby-in-One Web client — home library horizontal row (presentation only).
 *
 * Registered by app.js through `window.EIOHomeLibraryRow.register(app)`.
 * The component never calls api()/query()/localStorage: the parent injects a
 * frozen row object and the image URL helper, and handles ensure/retry/play.
 *
 * Preview loading is strictly on demand: one IntersectionObserver per idle
 * row with rootMargin 0px emits `ensure` once, then unobserves. A browser
 * without IntersectionObserver gets a manual button instead (never a batch
 * fetch). All observer/scroll/resize/focusin/requestAnimationFrame resources
 * are released in beforeUnmount; late nextTick callbacks check `destroyed`
 * before touching the DOM.
 */
(function () {
  'use strict';

  const SKELETON_COUNT = 6;

  const EioHomeLibraryRow = {
    name: 'EioHomeLibraryRow',
    props: {
      section: { type: Object, required: true },
      imageSrc: { type: Function, required: true },
    },
    emits: ['ensure', 'retry', 'play'],
    data() {
      return {
        canLeft: false,
        canRight: false,
        hasOverflow: false,
        ensureEmitted: false,
        manualSent: false,
        destroyed: false,
        skeletonCount: SKELETON_COUNT,
        observerSupported: typeof window !== 'undefined'
          && typeof window.IntersectionObserver === 'function',
      };
    },
    computed: {
      libraryHref() {
        const key = this.section && this.section.key != null ? String(this.section.key) : '';
        return '#/library/' + encodeURIComponent(key);
      },
      items() {
        return this.section && Array.isArray(this.section.items) ? this.section.items : [];
      },
      status() {
        return (this.section && this.section.status) || 'idle';
      },
      showSkeletons() {
        return !this.items.length && (this.status === 'idle' || this.status === 'loading');
      },
      showEmpty() {
        return !this.items.length && this.status === 'empty';
      },
      showInitialError() {
        return !this.items.length && this.status === 'error';
      },
      showPartialNotice() {
        return this.status === 'partial';
      },
      showErrorWithItems() {
        return this.items.length > 0 && this.status === 'error';
      },
      showManualLoad() {
        return !this.observerSupported && this.status === 'idle' && !this.manualSent;
      },
      errorText() {
        return (this.section && this.section.error) || '加载失败';
      },
    },
    watch: {
      // The parent may keep the same keyed v-for node but replace the row
      // object (reset) or move it back to idle. Both identity and status must
      // re-arm observation, not just section.key.
      section() { this.resetObservation(); },
      'section.key'() { this.resetObservation(); },
      'section.status'() { this.resetObservation(); },
      'section.items'() { this.scheduleMeasure(); },
    },
    mounted() {
      this.destroyed = false;
      this.scrollerEl = this.$refs.scroller || null;
      if (this.scrollerEl) {
        this.scrollerEl.addEventListener('scroll', this.onScroll, { passive: true });
        this.scrollerEl.addEventListener('focusin', this.onFocusIn);
      }
      if (typeof window !== 'undefined') {
        window.addEventListener('resize', this.onResize, { passive: true });
      }
      this.setupResizeObserver();
      this.$nextTick(() => {
        if (this.destroyed) return;
        this.syncObserver();
        this.measure();
      });
    },
    beforeUnmount() {
      this.destroyed = true;
      this.cancelMeasureFrame();
      if (this.observer) { this.observer.disconnect(); this.observer = null; }
      if (this.resizeObserver) { this.resizeObserver.disconnect(); this.resizeObserver = null; }
      if (this.scrollerEl) {
        this.scrollerEl.removeEventListener('scroll', this.onScroll);
        this.scrollerEl.removeEventListener('focusin', this.onFocusIn);
        this.scrollerEl = null;
      }
      if (typeof window !== 'undefined') {
        window.removeEventListener('resize', this.onResize);
      }
    },
    methods: {
      resetObservation() {
        this.manualSent = false;
        this.ensureEmitted = this.status !== 'idle';
        this.$nextTick(() => {
          if (this.destroyed) return;
          this.syncObserver();
          this.measure();
        });
      },
      syncObserver() {
        if (this.destroyed || !this.observerSupported) return;
        const target = this.$el;
        if (!target) return;
        if (this.status !== 'idle') {
          if (this.observer) this.observer.unobserve(target);
          return;
        }
        if (!this.observer) {
          this.observer = new window.IntersectionObserver(this.onIntersect, {
            rootMargin: '0px',
            threshold: 0,
          });
        }
        // Re-observe the current root; resetObservation already cleared the
        // one-shot emit gate for this idle lifecycle.
        this.observer.unobserve(target);
        this.observer.observe(target);
      },
      onIntersect(entries) {
        if (this.destroyed) return;
        for (let i = 0; i < entries.length; i += 1) {
          const entry = entries[i];
          if (!entry || !entry.isIntersecting) continue;
          if (this.status !== 'idle' || this.ensureEmitted) continue;
          this.ensureEmitted = true;
          this.$emit('ensure', this.section.key);
          if (this.observer) this.observer.unobserve(entry.target || this.$el);
        }
      },
      manualEnsure() {
        if (this.destroyed || this.manualSent) return;
        if (this.status !== 'idle') return;
        this.manualSent = true;
        this.$emit('ensure', this.section.key);
        // If the controller did not move the row out of idle (for example the
        // page went inactive before the request started), keep the manual
        // entry available instead of leaving a permanently hidden button.
        this.$nextTick(() => {
          if (this.destroyed) return;
          if (this.status === 'idle') this.manualSent = false;
        });
      },
      setupResizeObserver() {
        if (typeof window === 'undefined' || typeof window.ResizeObserver !== 'function') return;
        const target = this.$refs.scroller;
        if (!target) return;
        try {
          this.resizeObserver = new window.ResizeObserver(() => { this.measure(); });
          this.resizeObserver.observe(target);
        } catch (e) {
          this.resizeObserver = null;
        }
      },
      onResize() {
        this.measure();
      },
      onScroll() {
        this.measure();
      },
      scheduleMeasure() {
        if (this.destroyed) return;
        this.cancelMeasureFrame();
        const raf = typeof window !== 'undefined' ? window.requestAnimationFrame : null;
        if (typeof raf !== 'function') {
          this.measure();
          return;
        }
        this.measureFrame = raf.call(window, () => {
          this.measureFrame = null;
          if (!this.destroyed) this.measure();
        });
      },
      cancelMeasureFrame() {
        if (this.measureFrame == null) return;
        const caf = typeof window !== 'undefined' ? window.cancelAnimationFrame : null;
        if (typeof caf === 'function') caf.call(window, this.measureFrame);
        this.measureFrame = null;
      },
      measure() {
        if (this.destroyed) return;
        const el = this.$refs.scroller;
        if (!el) {
          this.hasOverflow = false;
          this.canLeft = false;
          this.canRight = false;
          return;
        }
        const scrollLeft = Number(el.scrollLeft) || 0;
        const clientWidth = Number(el.clientWidth) || 0;
        const scrollWidth = Number(el.scrollWidth) || 0;
        this.hasOverflow = scrollWidth > clientWidth + 1;
        this.canLeft = scrollLeft > 1;
        this.canRight = scrollLeft + clientWidth < scrollWidth - 1;
      },
      pageBy(direction) {
        if (this.destroyed) return;
        const el = this.$refs.scroller;
        if (!el) return;
        const amount = Math.max(200, Math.round((Number(el.clientWidth) || 0) * 0.9));
        const reduced = typeof window !== 'undefined' && typeof window.matchMedia === 'function'
          && window.matchMedia('(prefers-reduced-motion: reduce)').matches;
        const behavior = reduced ? 'auto' : 'smooth';
        const delta = direction < 0 ? -amount : amount;
        if (typeof el.scrollBy === 'function') el.scrollBy({ left: delta, behavior });
        else el.scrollLeft = (Number(el.scrollLeft) || 0) + delta;
        this.scheduleMeasure();
      },
      // Keep focus visible inside the horizontal track only. Never call
      // scrollIntoView and never touch the page's vertical scroll position.
      onFocusIn(event) {
        if (this.destroyed) return;
        const el = this.$refs.scroller;
        const target = event && event.target;
        if (!el || !target || target === el || !el.contains(target)) return;
        if (typeof target.getBoundingClientRect !== 'function'
          || typeof el.getBoundingClientRect !== 'function') return;
        const targetRect = target.getBoundingClientRect();
        const scrollerRect = el.getBoundingClientRect();
        if (targetRect.left < scrollerRect.left) {
          el.scrollLeft = (Number(el.scrollLeft) || 0) - (scrollerRect.left - targetRect.left) - 12;
        } else if (targetRect.right > scrollerRect.right) {
          el.scrollLeft = (Number(el.scrollLeft) || 0) + (targetRect.right - scrollerRect.right) + 12;
        }
      },
      retry() {
        if (this.destroyed) return;
        this.$emit('retry', this.section.key);
      },
    },
    template: `
      <div class="eio-home-lib-row">
        <div class="eio-row-head eio-home-lib-head">
          <a class="eio-row-title eio-home-lib-title" :href="libraryHref">{{ section.title }}</a>
          <a class="eio-row-more eio-home-lib-more" :href="libraryHref">查看全部</a>
        </div>
        <div class="eio-home-lib-viewport">
          <button v-show="hasOverflow" type="button" class="eio-home-lib-nav eio-home-lib-nav-left"
                  :disabled="!canLeft" aria-label="向左滚动" @click="pageBy(-1)">
            <svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor"
                 stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
              <polyline points="15 18 9 12 15 6"/>
            </svg>
          </button>
          <div ref="scroller" class="eio-home-lib-track">
            <eio-poster-card v-for="it in items" :key="it.Id" :item="it" :image-src="imageSrc(it, 300)"
                             :detail-href="'#/item/' + it.Id" @play="$emit('play', it)"></eio-poster-card>
            <div v-if="showSkeletons" class="eio-home-lib-skeletons" aria-hidden="true">
              <div v-for="n in skeletonCount" :key="n" class="eio-skeleton eio-home-lib-skeleton"></div>
            </div>
            <a class="eio-home-lib-enter" :href="libraryHref">
              <svg viewBox="0 0 24 24" width="20" height="20" fill="none" stroke="currentColor"
                   stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                <line x1="5" y1="12" x2="19" y2="12"/>
                <polyline points="12 5 19 12 12 19"/>
              </svg>
              <span>进入媒体库</span>
              <span class="eio-home-lib-enter-name">{{ section.title }}</span>
            </a>
          </div>
          <button v-show="hasOverflow" type="button" class="eio-home-lib-nav eio-home-lib-nav-right"
                  :disabled="!canRight" aria-label="向右滚动" @click="pageBy(1)">
            <svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor"
                 stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
              <polyline points="9 18 15 12 9 6"/>
            </svg>
          </button>
        </div>
        <p v-if="showEmpty" class="eio-home-lib-state">该库暂无内容</p>
        <div v-if="showInitialError" class="eio-home-lib-state eio-home-lib-error">
          <span>{{ errorText }}</span>
          <button type="button" class="eio-btn eio-btn-sm" @click="retry">重试</button>
        </div>
        <div v-if="showPartialNotice" class="eio-home-lib-state eio-home-lib-partial">
          <span>预览可能不完整</span>
          <button type="button" class="eio-btn eio-btn-sm" @click="retry">重试</button>
        </div>
        <div v-if="showErrorWithItems" class="eio-home-lib-state eio-home-lib-error">
          <span>{{ errorText }}</span>
          <button type="button" class="eio-btn eio-btn-sm" @click="retry">重试</button>
        </div>
        <div v-if="showManualLoad" class="eio-home-lib-state">
          <button type="button" class="eio-btn eio-btn-sm" @click="manualEnsure">加载内容</button>
        </div>
      </div>`,
  };

  window.EIOHomeLibraryRow = {
    register(app) {
      if (!app || typeof app.component !== 'function') return;
      app.component('EioHomeLibraryRow', EioHomeLibraryRow);
    },
  };
})();
