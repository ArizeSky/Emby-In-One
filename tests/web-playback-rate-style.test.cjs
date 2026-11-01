'use strict';
/**
 * Static markup/style contract for the player playback-rate trigger.
 *
 * This file only reads web.html and ui.css. It does not render, run a browser,
 * emulate layout, compute styles or measure contrast, so it is not browser
 * evidence and must not be described as such. Logic and app wiring stay in
 * tests/web-playback-rate-ui.test.cjs and tests/web-playback-rate-app.test.cjs.
 */
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

const HTML_PATH = path.join(__dirname, '..', 'public', 'web', 'web.html');
const CSS_PATH = path.join(__dirname, '..', 'public', 'web', 'ui.css');
const html = fs.readFileSync(HTML_PATH, 'utf8');
const css = fs.readFileSync(CSS_PATH, 'utf8');

const RATE_TRIGGER_PATTERN = /<button\b[^>]*\baria-label="播放倍速"[^>]*>[\s\S]*?<\/button>/g;

function triggerElement() {
  const openers = html.match(/<button\b[^>]*\baria-label="播放倍速"[^>]*>/g) || [];
  assert.equal(openers.length, 1, 'exactly one rate trigger button is selectable by ARIA label');
  const elements = html.match(RATE_TRIGGER_PATTERN) || [];
  assert.equal(elements.length, 1, 'the rate trigger button is a complete, unambiguous element');
  return elements[0];
}

function triggerTag() {
  const tag = triggerElement().match(/^<button\b[^>]*>/);
  assert.ok(tag, 'the rate trigger opening tag exists');
  return tag[0];
}

function triggerContent() {
  const element = triggerElement();
  const opening = element.match(/^<button\b[^>]*>/);
  assert.ok(opening, 'the rate trigger opening tag exists');
  const content = element.slice(opening[0].length, -'</button>'.length);
  assert.ok(content.trim(), 'the rate trigger carries its label body');
  return content;
}

function ruleBody(selector) {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  const match = css.match(new RegExp(escaped + '\\s*\\{([^}]*)\\}'));
  assert.ok(match, 'missing CSS rule: ' + selector);
  return match[1];
}

function declarations(body) {
  const out = {};
  for (const part of body.split(';')) {
    const colon = part.indexOf(':');
    if (colon < 0) continue;
    out[part.slice(0, colon).trim()] = part.slice(colon + 1).trim();
  }
  return out;
}

function selectors() {
  const list = [];
  const pattern = /([^{}]+)\{([^{}]*)\}/g;
  let match;
  while ((match = pattern.exec(css)) !== null) {
    list.push(match[1].replace(/\/\*[\s\S]*?\*\//g, '').trim());
  }
  return list;
}

const TEXT_BTN = 'body.eio-web .eio-player .eio-play-btn.eio-play-text-btn';

test('web.html: the rate trigger is a player text button, not a page .eio-btn', () => {
  const trigger = triggerTag();
  assert.match(trigger, /class="eio-play-btn eio-play-text-btn"/);
  assert.match(trigger, /type="button"/);
  assert.doesNotMatch(trigger, /eio-btn-sm/, 'the light page small-button class is gone');
  assert.doesNotMatch(trigger, /\beio-btn\b/, 'the page button class is gone');
});

test('web.html: disabled, busy, ARIA, click and label expressions are preserved', () => {
  const trigger = triggerTag();
  const content = triggerContent();
  assert.match(trigger, /:disabled="!playerAlive \|\| ratePending \|\| playerUI\.busy \|\| trackBusy"/);
  assert.match(trigger, /aria-label="播放倍速"/);
  assert.match(trigger, /aria-haspopup="menu"/);
  assert.match(trigger, /aria-controls="eio-menu-rate"/);
  assert.match(trigger, /:aria-expanded="playerUI\.isMenuOpen\('rate'\) \? 'true' : 'false'"/);
  assert.match(trigger, /@click="playerUI\.toggleMenu\('rate'\)"/);
  // Pending, unconfirmed and confirmed labels stay byte-for-byte identical.
  assert.match(content, /ratePending \? '设置中…' : rateError \? '倍速未确认' : \('倍速 ' \+ playbackRate \+ '×'\)/);
});

test('web.html: the ARIA-selected rate trigger is the player text button that owns the rate menu', () => {
  const element = triggerElement();
  assert.match(element, /^<button\b/);
  assert.match(element, /class="eio-play-btn eio-play-text-btn"/);
  assert.match(element, /aria-controls="eio-menu-rate"/);
  assert.match(element, /<\/button>$/);
  assert.ok(triggerContent().includes('playbackRate'), 'the label body belongs to the rate trigger');
});

test('web.html: the player scope and the five literal rates are unchanged', () => {
  assert.match(html, /<div[^>]*v-if="navPage==='player'"[^>]*class="eio-player"/);
  assert.match(html, /v-for="rate in \[0\.5, 1, 1\.25, 1\.5, 2\]"/);
  assert.match(html, /@click="selectPlaybackRate\(rate\)"/);
  assert.match(html, /:aria-pressed="!rateError && !ratePending && rate === playbackRate \? 'true' : 'false'"/);
  assert.match(html, /v-if="!rateError && !ratePending && rate === playbackRate">✓/);
  assert.match(html, /id="eio-menu-rate" role="menu" aria-label="播放倍速"/);
});

test('ui.css: the text trigger is auto-width, 44px tall, padded, transparent and white', () => {
  const decl = declarations(ruleBody(TEXT_BTN));
  assert.equal(decl.width, 'auto');
  assert.equal(decl.height, 'auto');
  assert.equal(decl['min-width'], '0');
  assert.equal(decl['max-width'], '100%');
  assert.equal(decl['min-height'], '44px');
  assert.equal(decl.padding, '8px 10px');
  assert.equal(decl.background, 'transparent');
  assert.equal(decl.color, '#fff');
  assert.equal(decl['white-space'], 'nowrap', 'the rate label must not be squeezed');
});

test('ui.css: icon player buttons keep their 44x44 square', () => {
  const icon = declarations(ruleBody('.eio-play-btn'));
  assert.equal(icon.width, '44px');
  assert.equal(icon.height, '44px');
});

test('ui.css: hover is a light white wash, focus is explicit, disabled never goes bright', () => {
  const hover = declarations(ruleBody(TEXT_BTN + ':hover'));
  assert.equal(hover.background, 'rgba(255, 255, 255, .12)');
  assert.doesNotMatch(hover.background, /var\(--bg-[23]\)|#fff|white/i);

  const focus = declarations(ruleBody(TEXT_BTN + ':focus-visible'));
  assert.equal(focus.outline, '2px solid var(--ac-text)');
  assert.equal(focus['outline-offset'], '2px');

  const disabled = declarations(ruleBody(
    TEXT_BTN + ':disabled,\nbody.eio-web .eio-player .eio-play-btn.eio-play-text-btn:disabled:hover'));
  assert.equal(disabled.background, 'transparent');
  assert.doesNotMatch(disabled.background, /var\(--bg-[23]\)|#fff|white/i);
});

test('ui.css: the text-button rules stay inside .eio-player and do not touch page buttons', () => {
  const selectorsWithText = selectors().filter(selector => selector.includes('.eio-play-text-btn'));
  assert.ok(selectorsWithText.length >= 4, 'base, hover, focus and disabled rules exist');
  for (const selector of selectorsWithText) {
    for (const part of selector.split(',')) {
      assert.ok(
        part.trim().startsWith('body.eio-web .eio-player '),
        'text-button rule leaked outside the player: ' + selector);
    }
  }

  // The shared page button contract is untouched.
  const pageBtn = declarations(ruleBody('.eio-btn'));
  assert.equal(pageBtn.background, 'var(--bg-2)');
  assert.equal(pageBtn['min-height'], '44px');
  const pageBtnSm = declarations(ruleBody('.eio-btn-sm'));
  assert.equal(pageBtnSm['min-height'], '38px');
  assert.equal(pageBtnSm.padding, '6px 12px');
});

test('ui.css: small screens shrink the label instead of forcing overflow', () => {
  const match = css.match(
    /@media \(max-width: 480px\)\s*\{\s*body\.eio-web \.eio-player \.eio-play-btn\.eio-play-text-btn\s*\{([^}]*)\}/);
  assert.ok(match, 'the small-screen text-button rule exists');
  const small = declarations(match[1]);
  assert.equal(small['font-size'], '12px');
  assert.equal(small.padding, '6px 8px');
  assert.equal(small.width, undefined, 'no fixed width on small screens');
});
