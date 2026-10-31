(function (root, factory) {
  'use strict';
  var api = factory();
  if (typeof module === 'object' && module.exports) {
    module.exports = api;
  }
  if (root) {
    root.EIOThemePlayerControls = api;
  }
})(typeof window !== 'undefined' ? window : (typeof globalThis !== 'undefined' ? globalThis : this), function () {
  'use strict';

  var HEX6 = /^#[0-9a-fA-F]{6}$/;
  var COLOR_FIELDS = ['played', 'buffered', 'track', 'thumb'];
  var MIN_CONTRAST = 3;

  var DEFAULT_PALETTE = {
    played: '#3b82f6',
    buffered: '#f3f4f6',
    track: '#111827',
    thumb: '#3b82f6'
  };

  function hasOwn(obj, key) {
    return Object.prototype.hasOwnProperty.call(obj, key);
  }

  // Native Object constructor source, used to recognise the real Object
  // constructor of any realm without relying on constructor.name (spoofable).
  var NATIVE_OBJECT_SOURCE = Function.prototype.toString.call(Object);

  // Plain-object check in the spirit of lodash isPlainObject: cross-realm
  // ordinary JSON objects are accepted, while Date/Array/class instances and
  // objects with a custom prototype chain are rejected.
  function isPlainObject(value) {
    if (value === null || typeof value !== 'object') return false;
    if (Array.isArray(value)) return false;
    var proto = Object.getPrototypeOf(value);
    // Null-prototype objects are plain.
    if (proto === null) return true;
    if (!Object.prototype.hasOwnProperty.call(proto, 'constructor')) return false;
    var ctor = proto.constructor;
    if (typeof ctor !== 'function') return false;
    if (Function.prototype.toString.call(ctor) !== NATIVE_OBJECT_SOURCE) return false;
    // The prototype itself must be a root prototype (its own proto is null).
    return Object.getPrototypeOf(proto) === null;
  }

  function normalizeHex(value) {
    if (typeof value !== 'string') return null;
    if (!HEX6.test(value)) return null;
    return value.toLowerCase();
  }

  function channelToLinear(c) {
    var v = c / 255;
    return v <= 0.03928 ? v / 12.92 : Math.pow((v + 0.055) / 1.055, 2.4);
  }

  function relativeLuminance(hex) {
    var r = parseInt(hex.slice(1, 3), 16);
    var g = parseInt(hex.slice(3, 5), 16);
    var b = parseInt(hex.slice(5, 7), 16);
    return (
      0.2126 * channelToLinear(r) +
      0.7152 * channelToLinear(g) +
      0.0722 * channelToLinear(b)
    );
  }

  function contrastRatio(hexA, hexB) {
    var la = relativeLuminance(hexA);
    var lb = relativeLuminance(hexB);
    return (Math.max(la, lb) + 0.05) / (Math.min(la, lb) + 0.05);
  }

  function pairsMeetContrast(p) {
    return (
      contrastRatio(p.played, p.buffered) >= MIN_CONTRAST &&
      contrastRatio(p.played, p.track) >= MIN_CONTRAST &&
      contrastRatio(p.buffered, p.track) >= MIN_CONTRAST &&
      contrastRatio(p.thumb, p.track) >= MIN_CONTRAST
    );
  }

  // Exactly the four fields, all own, all native #rrggbb strings.
  function normalizeControls(raw) {
    var keys = Object.keys(raw);
    if (keys.length !== COLOR_FIELDS.length) return null;
    var controls = {};
    for (var i = 0; i < COLOR_FIELDS.length; i++) {
      var key = COLOR_FIELDS[i];
      if (!hasOwn(raw, key)) return null;
      var hex = normalizeHex(raw[key]);
      if (hex === null) return null;
      controls[key] = hex;
    }
    if (!pairsMeetContrast(controls)) return null;
    return controls;
  }

  function resolveVersion(input, builtin) {
    if (builtin) {
      if (!hasOwn(input, 'themeApiVersion')) return 0;
      var bv = input.themeApiVersion;
      if (bv === undefined) return 0;
      if (bv === 0) return 0;
      return null;
    }
    if (!hasOwn(input, 'themeApiVersion')) return 1;
    var v = input.themeApiVersion;
    if (v === undefined) return 1;
    if (v === 1 || v === 2) return v;
    return null;
  }

  function normalizeThemeFields(input, builtin) {
    if (!isPlainObject(input)) return null;
    if (builtin !== undefined && typeof builtin !== 'boolean') return null;

    var version = resolveVersion(input, !!builtin);
    if (version === null) return null;

    var hasControls = hasOwn(input, 'playerControls');

    if (version === 0) {
      if (hasControls) return null;
      return { themeApiVersion: 0 };
    }

    if (version === 1) {
      // Even an own playerControls with null/undefined is unsupported here.
      if (hasControls) return null;
      return { themeApiVersion: 1 };
    }

    // version 2: block optional, but never defaulted in.
    if (!hasControls) return { themeApiVersion: 2 };
    var raw = input.playerControls;
    if (!isPlainObject(raw)) return null;
    var controls = normalizeControls(raw);
    if (controls === null) return null;
    return { themeApiVersion: 2, playerControls: controls };
  }

  return {
    normalizeThemeFields: normalizeThemeFields,
    DEFAULT_PALETTE: DEFAULT_PALETTE,
    MIN_CONTRAST: MIN_CONTRAST,
    contrastRatio: contrastRatio
  };
});
