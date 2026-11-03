/* Controlled compressed JS payload transport. Inert until explicitly created.
 * No GC/reset credit: release requires sender detachment and a matching receiver
 * ACK produced only after intrinsic detachment of every received backing.
 * This does not cover native clones, mux output, or media-time admission.
 */
(function (root, factory) {
  'use strict';
  if (typeof module === 'object' && module.exports) module.exports = factory();
  else root.EioPayloadTransfer = factory();
})(typeof globalThis !== 'undefined' ? globalThis : this, function () {
  'use strict';
  var arrayPrototype = Object.getPrototypeOf(Uint8Array.prototype);
  var byteLength = Object.getOwnPropertyDescriptor(arrayPrototype, 'byteLength').get;
  var byteOffset = Object.getOwnPropertyDescriptor(arrayPrototype, 'byteOffset').get;
  var backing = Object.getOwnPropertyDescriptor(arrayPrototype, 'buffer').get;
  var bufferLength = Object.getOwnPropertyDescriptor(ArrayBuffer.prototype, 'byteLength').get;
  var resizable = Object.getOwnPropertyDescriptor(ArrayBuffer.prototype, 'resizable');
  var detached = Object.getOwnPropertyDescriptor(ArrayBuffer.prototype, 'detached');
  var detach = ArrayBuffer.prototype.transferToFixedLength;
  var copy = Uint8Array.prototype.set;

  function error(code) {
    var value = new Error(code);
    value.code = code;
    return value;
  }

  function capability() {
    if (typeof detach !== 'function' || !detached || typeof detached.get !== 'function') {
      throw error('EIO_PAYLOAD_DETACH_UNSUPPORTED');
    }
  }

  function count(value) { return Number.isSafeInteger(value) && value >= 0; }

  function alias(view) {
    if (!ArrayBuffer.isView(view)) throw error('EIO_PAYLOAD_VIEW_INVALID');
    // This is a fixed Uint8Array alias, not a compressed-data copy. Intrinsics
    // ignore own properties/subclass getters on the supplied view.
    var size, offset, buffer;
    try {
      size = byteLength.call(view);
      offset = byteOffset.call(view);
      buffer = backing.call(view);
      if (!count(size) || !count(offset)) throw error('EIO_PAYLOAD_VIEW_INVALID');
      return { view: new Uint8Array(buffer, offset, size), bytes: size, buffer: buffer };
    } catch (_) { throw error('EIO_PAYLOAD_VIEW_INVALID'); }
  }

  function list(views) {
    if (!Array.isArray(views) || views.length > 65536) throw error('EIO_PAYLOAD_LIST_INVALID');
    var records = [], total = 0;
    for (var index = 0; index < views.length; index += 1) {
      var record = alias(views[index]);
      if (!count(total + record.bytes)) throw error('EIO_PAYLOAD_SIZE_INVALID');
      total += record.bytes;
      records.push(record);
    }
    return { records: records, bytes: total };
  }

  function isDetached(buffer) { return detached.get.call(buffer) === true; }

  function detachAll(buffers) {
    for (var index = 0; index < buffers.length; index += 1) {
      if (!isDetached(buffers[index])) detach.call(buffers[index], 0);
      if (!isDetached(buffers[index]) || bufferLength.call(buffers[index]) !== 0) {
        throw error('EIO_PAYLOAD_DETACH_UNVERIFIED');
      }
    }
  }

  function receive(views, identity) {
    capability();
    if (!identity || typeof identity.instance !== 'string' || !/^[a-f0-9]{32}$/.test(identity.instance) || !Number.isSafeInteger(identity.operation) || identity.operation <= 0 ||
        typeof identity.domain !== 'string' || !identity.domain || !count(identity.bytes)) {
      throw error('EIO_PAYLOAD_IDENTITY_INVALID');
    }
    // Capture primitives before destructive mutation; later getters cannot
    // change the ACK's identity or claimed byte count.
    var instance = identity.instance;
    var operation = identity.operation, domain = identity.domain, expected = identity.bytes;
    var measured = list(views), buffers = [], seen = new Set();
    if (measured.bytes !== expected) throw error('EIO_PAYLOAD_SIZE_MISMATCH');
    for (var index = 0; index < measured.records.length; index += 1) {
      var record = measured.records[index], buffer = record.buffer;
      // Reject shared, detached, oversized, resizable or multiply represented
      // backing before detaching anything. Each transfer owns one exact buffer.
      var size;
      try { size = bufferLength.call(buffer); }
      catch (_) { throw error('EIO_PAYLOAD_BACKING_INVALID'); }
      if (isDetached(buffer) || (resizable && resizable.get.call(buffer)) ||
          size !== record.bytes || seen.has(buffer)) throw error('EIO_PAYLOAD_BACKING_INVALID');
      seen.add(buffer);
      buffers.push(buffer);
    }
    detachAll(buffers);
    return Object.freeze({ eioPayloadAck: 1, instance: instance, operation: operation, domain: domain,
      bytes: expected, buffers: buffers.length, detached: true });
  }

  function create(options) {
    capability();
    if (!options || !options.resources || typeof options.resources.acquire !== 'function' ||
        typeof options.domain !== 'string' || !options.domain) throw error('EIO_PAYLOAD_OPTIONS_INVALID');
    var resources = options.resources, domain = options.domain;
    if (!globalThis.crypto || typeof globalThis.crypto.getRandomValues !== 'function') {
      throw error('EIO_PAYLOAD_IDENTITY_UNSUPPORTED');
    }
    var nonce = new Uint32Array(4);
    globalThis.crypto.getRandomValues(nonce);
    var instance = Array.from(nonce).map(function (word) { return word.toString(16).padStart(8, '0'); }).join('');
    var next = 0;
    var live = new Map();

    function prepare(views, generation) {
      var measured = list(views);
      if (next === Number.MAX_SAFE_INTEGER) throw error('EIO_PAYLOAD_OPERATION_EXHAUSTED');
      var operation = ++next;
      // Nothing below this point can allocate compressed backing without the
      // exact reservation already installed in the actual resource controller.
      var acquired = resources.acquire(measured.bytes, 'codecpar-js-transfer', generation);
      if (!acquired || acquired.ok !== true || !acquired.handle) throw error('EIO_PAYLOAD_ADMISSION_REFUSED');
      var handle = acquired.handle, payloads = [], buffers = [];
      var record = { state: 'copying', bytes: measured.bytes, operation: operation, fault: null };
      live.set(operation, record);
      function release() {
        record.state = 'releasing'; // poison before a possibly reentrant callback
        try {
          var result = handle.release({ referencesDropped: true });
          if (!result || result.ok !== true) throw error('EIO_PAYLOAD_RELEASE_UNVERIFIED');
        } catch (failure) {
          record.state = 'retained';
          throw failure;
        }
        record.state = 'released';
        live.delete(operation);
      }
      var output = null;
      try {
        for (var index = 0; index < measured.records.length; index += 1) {
          var input = measured.records[index];
          if (byteLength.call(input.view) !== input.bytes) throw error('EIO_PAYLOAD_SOURCE_CHANGED');
          output = new Uint8Array(input.bytes);
          buffers.push(backing.call(output));
          copy.call(output, input.view);
          payloads.push(output);
        }
      } catch (failure) {
        record.state = 'retained';
        record.fault = 'EIO_PAYLOAD_COPY_FAILED';
        // No transfer has started. All created backings are still private;
        // detach them before releasing even this partial-copy reservation.
        try {
          if (output) detachAll([backing.call(output)]);
          detachAll(buffers);
          release();
        } catch (_) { record.state = 'retained'; }
        throw failure;
      }
      record.state = 'ready';
      Object.freeze(payloads);
      Object.freeze(buffers);
      var identity = Object.freeze({ instance: instance, operation: operation, domain: domain, bytes: measured.bytes });
      return Object.freeze({ identity: identity, payloads: payloads, buffers: buffers,
        cancel: function () {
          if (record.state !== 'ready') return false;
          record.state = 'retained';
          // Already transferred means a remote owner may still hold the bytes.
          if (buffers.some(isDetached)) throw error('EIO_PAYLOAD_CANCEL_OWNERSHIP_UNPROVEN');
          detachAll(buffers);
          release();
          return true;
        },
        send: async function (send) {
          if (record.state !== 'ready' || typeof send !== 'function') throw error('EIO_PAYLOAD_STATE_INVALID');
          record.state = 'sending';
          var ack;
          try {
            ack = await send(payloads, buffers, identity);
            if (!ack || ack.eioPayloadAck !== 1 || ack.instance !== instance || ack.operation !== operation || ack.domain !== domain ||
                ack.bytes !== measured.bytes || ack.buffers !== buffers.length || ack.detached !== true) {
              throw error('EIO_PAYLOAD_ACK_UNVERIFIED');
            }
            for (var index = 0; index < buffers.length; index += 1) {
              if (!isDetached(buffers[index])) throw error('EIO_PAYLOAD_SENDER_STILL_OWNS');
            }
            release();
            return true;
          } catch (failure) {
            // A rejected send may already have transferred some/all buffers.
            // Neither timeout, failure nor task reset is detachment proof.
            record.state = 'retained';
            record.fault = 'EIO_PAYLOAD_SEND_UNVERIFIED';
            throw failure;
          }
        }
      });
    }

    return Object.freeze({ prepare: prepare, snapshot: function () {
      var held = 0, pending = 0, retained = 0;
      live.forEach(function (record) {
        held += record.bytes;
        pending += 1;
        if (record.state === 'retained') retained += 1;
      });
      return Object.freeze({ heldBytes: held, pending: pending, retained: retained, complete: false });
    } });
  }

  return Object.freeze({ create: create, receive: receive });
});
