'use strict';
// Selector/declaration contract for the patched vendored ASS stylesheet.
// DOM layout and computed pseudo-element styles were reviewed with a separate
// browser probe; this Node test only reads the artifact CSS literal and asserts
// the contract of the change. It renders nothing, so it is not a browser or
// visual verification of the original media.
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const { join } = require('node:path');

const ARTIFACT = join(__dirname, '..', 'public', 'vendor', 'web', 'libmedia', '630.avplayer.js');
const source = fs.readFileSync(ARTIFACT, 'utf8');
const literal = /\bconst g=('(?:[^'\\]|\\.)*'),p=/.exec(source);
assert.ok(literal, 'vendored stylesheet literal `const g=...,p=` must exist');
const css = vm.runInNewContext(literal[1]);
const defaults = vm.runInNewContext(`(${/,p=(\{[^}]*\})/.exec(source)[1]})`);
const count = (haystack, needle) => haystack.split(needle).length - 1;

test('border style 1 selectors are descendant scoped, three times', () => {
  assert.equal(count(css, '.ASS-dialogue [data-border-style="1"]'), 3);
  assert.equal(count(css, '.ASS-dialogue[data-border-style="1"]'), 0);
});

test('border style 3 selectors are descendant scoped, three times', () => {
  assert.equal(count(css, '.ASS-dialogue [data-border-style="3"]'), 3);
  assert.equal(count(css, '.ASS-dialogue[data-border-style="3"]'), 0);
});

test('patched selectors stay minified and wrap styles keep root scope', () => {
  assert.match(css, /\.ASS-dialogue \[data-border-style="1"\]\{position:relative;&::before/);
  assert.match(css, /\.ASS-dialogue \[data-border-style="3"\]\{padding:/);
  assert.doesNotMatch(css, /\.ASS-dialogue\s{2,}\[/);
  for (const style of ['0', '1', '2', '3']) {
    assert.equal(count(css, `.ASS-dialogue[data-wrap-style="${style}"]`), 1, style);
  }
  assert.equal(count(css, '.ASS-dialogue [data-wrap-style='), 0);
});

test('border style 1 keeps outline, shadow colour and shadow translate', () => {
  assert.ok(css.includes('&::before,&::after{content:attr(data-text);position:absolute;top:0;left:0;z-index:-1;filter:blur('));
  assert.ok(css.includes('&::before{color:var(--ass-shadow-color);-webkit-text-stroke:calc(var(--ass-scale-stroke)*var(--ass-border-width)*1px) var(--ass-shadow-color);transform:translate(calc(var(--ass-scale-stroke)*var(--ass-tag-xshad)*1px),calc(var(--ass-scale-stroke)*var(--ass-tag-yshad)*1px))}'));
  assert.ok(css.includes('&::after{color:var(--ass-border-color);-webkit-text-stroke:calc(var(--ass-scale-stroke)*var(--ass-border-width)*1px) var(--ass-border-color)}'));
  assert.ok(css.includes('{.ASS-dialogue [data-border-style="1"]::after{display:none}}'));
  assert.ok(css.includes('{.ASS-dialogue [data-border-style="1"]::before{display:none}}'));
});

test('nested SVG stroke guard and rotate/scale/skew rules are untouched', () => {
  assert.ok(css.includes('&[data-stroke="svg"]{color:#000;&::before,&::after{opacity:0}}'));
  assert.ok(css.includes('.ASS-dialogue[data-rotate]{transform:perspective(312.5px) rotateY(calc(var(--ass-tag-fry) * 1deg)) rotateX(calc(var(--ass-tag-frx) * 1deg)) rotateZ(calc(var(--ass-tag-frz) * -1deg));&[data-text]{transform-style:preserve-3d;word-break:normal;white-space:nowrap}}'));
  assert.ok(css.includes('.ASS-dialogue[data-scale],.ASS-dialogue[data-skew]{display:inline-block;transform:scale(var(--ass-tag-fscx), var(--ass-tag-fscy)) skew(calc(var(--ass-tag-fax)*1rad),calc(var(--ass-tag-fay) * 1rad));transform-origin:var(--ass-align-h) var(--ass-align-v)}'));
});

test('default style keeps BorderStyle 1 and Outline 1', () => {
  assert.equal(defaults.Name, 'Default');
  assert.equal(defaults.BorderStyle, '1');
  assert.equal(defaults.Outline, '1');
});
