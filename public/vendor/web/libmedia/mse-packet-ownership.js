"use strict";
/* Strict-owned MSE ticket/epoch and packet identity ownership. The chunk
   registers module 90193; module 90192 requires it through the real webpack
   loader so a standalone 90206 load cannot bypass packet ownership. This
   module never mints a quiescence token, ACK or ledger credit. */
(self.webpackChunkAVPlayer=self.webpackChunkAVPlayer||[]).push([[90207],{
90193:(module,exports,__webpack_require__)=>{
/*!
 * mse-packet-ownership.js
 *
 * Ticket/epoch identity, packet ownership records, slot/queue transfers,
 * exact-once reclaim and bounded revocation drains for the MSE worker.
 * Baseline streams (no eioOwned) are not touched by this module.
 *
 * Actual AVPacketPool ABI (module 61457): release(pointer) returns the AVPacket
 * to the real pool and returns void; the pool may recycle its header and
 * releases the payload through its own native path. "No throw" is the accepted
 * completion signal for the real pool, while an explicit false/null/refused
 * result is retained. This module is not a second ledger: it only remembers
 * which exact packet record was handed to that real pool.
 */
(function (root, factory) {
  'use strict';
  if (typeof module === 'object' && module && module.exports) {
    module.exports = factory();
  } else if (typeof define === 'function' && define.amd) {
    define([], factory);
  } else {
    root.EioMsePacketOwnership = factory();
  }
})(typeof globalThis !== 'undefined' ? globalThis : this, function () {
  'use strict';

  var VERSION = 1;
  var KIND = 'eio-mse-packet-ownership/v1';
  var DEFAULT_DRAIN_MS = 1500;
  var STALE = Object.freeze({ eioPullStale: true, code: 'EIO_MUX_PULL_STALE' });

  function codedError(code, message) {
    var error = new Error(message || code);
    error.code = code;
    return error;
  }

  function scalarCode(value, fallback) {
    try {
      if (value && typeof value.code === 'string' && value.code.length <= 64) return value.code;
    } catch (ignored) {}
    return fallback;
  }

  function checkDependencies(deps) {
    if (!deps || typeof deps !== 'object') throw codedError('EIO_MSE_OWNERSHIP_DEPS_INVALID');
    if (!deps.packetPool || typeof deps.packetPool.release !== 'function') {
      throw codedError('EIO_MSE_OWNERSHIP_POOL_INVALID');
    }
    return { packetPool: deps.packetPool };
  }

  function createMsePacketOwnership(dependencies) {
    var deps = checkDependencies(dependencies);
    var pool = deps.packetPool;
    var nextTicketId = 1;

    function isStrict(stream) {
      return !!(stream && stream.eioOwned);
    }

    function ensure(stream) {
      if (!stream.eioPullTickets) stream.eioPullTickets = new Set();
      if ('number' !== typeof stream.eioPullEpoch) stream.eioPullEpoch = 0;
      if (!stream.eioPullPackets) stream.eioPullPackets = new Map();
      if (!stream.eioPullSlotOwners) stream.eioPullSlotOwners = { back: null, front: null };
      if (!Array.isArray(stream.eioPullQueueOwners)) stream.eioPullQueueOwners = [];
      if ('boolean' !== typeof stream.eioPullPoolClosed) stream.eioPullPoolClosed = false;
      if (undefined === stream.eioSwapPending) stream.eioSwapPending = null;
      return stream;
    }

    function streamOf(ticket) {
      return ticket && ticket.stream ? ticket.stream : null;
    }

    function outputOf(stream) {
      try {
        return stream && stream.oformatContext && stream.oformatContext.streams
          ? stream.oformatContext.streams[0] || null : null;
      } catch (error) {
        return null;
      }
    }

    function timeBaseOf(output) {
      return output ? output.timeBase : null;
    }

    function settle(ticket) {
      if (!ticket || ticket.state === 'settled') return;
      ticket.state = 'settled';
      var resolve = ticket.settle;
      ticket.settle = null;
      ticket.settledOutput = null;
      ticket.settledTimeBase = null;
      ticket.settledCodecpar = null;
      ticket.stream = null;
      ticket.packets = [];
      ticket.queued = [];
      if (resolve) resolve();
    }

    function begin(stream, kind) {
      if (!isStrict(stream)) return null;
      ensure(stream);
      if (stream.eioReleased === true || stream.eioPullPoolClosed === true) return null;
      if (kind === 'swap' && stream.eioSwapPending !== null) return null;
      var output = outputOf(stream);
      var settleResolve = null;
      var settled = new Promise(function (resolve) { settleResolve = resolve; });
      var ticket = {
        id: nextTicketId++,
        stream: stream,
        kind: kind,
        gen: stream.eioPullEpoch || 0,
        opGen: stream.eioOpGeneration || 0,
        output: output,
        timeBase: timeBaseOf(output),
        codecpar: stream.codecpar,
        streamIndex: stream.streamIndex,
        state: 'pending',
        revoked: false,
        revokeReason: null,
        packets: [],
        queued: [],
        errorCode: null,
        seekSync: null,
        settle: settleResolve,
        settled: settled
      };
      stream.eioPullTickets.add(ticket);
      return ticket;
    }

    function beginTickets(task, kind) {
      var tickets = { audio: null, video: null };
      if (!task) return tickets;
      if (isStrict(task.audio)) tickets.audio = begin(task.audio, kind);
      if (isStrict(task.video)) tickets.video = begin(task.video, kind);
      return tickets;
    }

    function stale(ticket) {
      if (!ticket) return false;
      var stream = streamOf(ticket);
      if (ticket.revoked || ticket.state !== 'pending') return true;
      if (!stream || stream.eioReleased === true || stream.eioPullPoolClosed === true) return true;
      if (ticket.gen !== (stream.eioPullEpoch || 0)) return true;
      var output = outputOf(stream);
      if (output !== ticket.output) return true;
      if (ticket.output && ticket.output.timeBase !== ticket.timeBase) return true;
      if (stream.codecpar !== ticket.codecpar) return true;
      if (stream.streamIndex !== ticket.streamIndex) return true;
      return false;
    }

    function findRecord(stream, pointer) {
      if (!stream || !stream.eioPullPackets) return null;
      return stream.eioPullPackets.get(pointer) || null;
    }

    function register(ticket, pointer, reason) {
      var stream = streamOf(ticket);
      if (!stream || !(pointer > 0)) return null;
      ensure(stream);
      var existing = findRecord(stream, pointer);
      if (existing) {
        ticket.errorCode = ticket.errorCode || 'EIO_MUX_PULL_OWNERSHIP_CONFLICT';
        existing.reason = existing.reason || 'duplicate-live-pointer';
        return null;
      }
      var record = {
        pointer: pointer,
        ticketId: ticket.id,
        ticket: ticket,
        state: stream.eioPullPoolClosed ? 'retained' : 'owned',
        slot: null,
        queue: null,
        reason: stream.eioPullPoolClosed ? 'pool-closed' : (reason || null)
      };
      stream.eioPullPackets.set(pointer, record);
      ticket.packets.push(record);
      return record;
    }

    function check(ticket, value) {
      if (!ticket) return value;
      var positive = 'number' === typeof value && value > 0;
      var record = null;
      /* Correct order: register the just-returned positive pointer first, then
         decide staleness and reclaim that exact record. Reversing this order
         would leak the packet resolved by a revoked continuation. */
      if (positive) record = register(ticket, value, 'pulled');
      if (positive && !record) {
        fail(ticket, 'EIO_MUX_PULL_OWNERSHIP_CONFLICT');
        reclaim(ticket, 'duplicate-pointer');
        throw codedError('EIO_MUX_PULL_OWNERSHIP_CONFLICT');
      }
      if (stale(ticket)) {
        reclaim(ticket, 'stale');
        return STALE;
      }
      return value;
    }

    function transferSlot(ticket, pointer, slot) {
      var stream = streamOf(ticket);
      if (!stream || !(pointer > 0)) throw codedError('EIO_MUX_PULL_INVALID_RESULT');
      if (stale(ticket)) {
        reclaim(ticket, 'stale-transfer');
        throw codedError('EIO_MUX_PULL_STALE');
      }
      var record = findRecord(stream, pointer);
      if (!record || record.ticketId !== ticket.id || record.state !== 'owned' ||
          record.slot !== null || record.queue !== null) {
        fail(ticket, 'EIO_MUX_PULL_OWNERSHIP_CONFLICT');
        throw codedError('EIO_MUX_PULL_OWNERSHIP_CONFLICT');
      }
      var owners = stream.eioPullSlotOwners || (stream.eioPullSlotOwners = { back: null, front: null });
      if (owners[slot] && owners[slot] !== record) {
        fail(ticket, 'EIO_MUX_PULL_OWNERSHIP_CONFLICT');
        throw codedError('EIO_MUX_PULL_OWNERSHIP_CONFLICT');
      }
      var field = slot + 'Packet';
      if (stream[field] && stream[field] !== pointer) {
        fail(ticket, 'EIO_MUX_PULL_OWNERSHIP_CONFLICT');
        throw codedError('EIO_MUX_PULL_OWNERSHIP_CONFLICT');
      }
      owners[slot] = record;
      record.state = 'transferred';
      record.slot = slot;
      record.queue = null;
      stream[field] = pointer;
      return true;
    }

    function transferQueue(ticket, pointer, queue) {
      var stream = streamOf(ticket);
      if (!stream || !(pointer > 0) || !Array.isArray(queue)) {
        throw codedError('EIO_MUX_PULL_INVALID_RESULT');
      }
      if (stale(ticket)) {
        reclaim(ticket, 'stale-queue');
        throw codedError('EIO_MUX_PULL_STALE');
      }
      var record = findRecord(stream, pointer);
      if (!record || record.ticketId !== ticket.id || record.state !== 'owned' ||
          record.slot !== null || record.queue !== null) {
        fail(ticket, 'EIO_MUX_PULL_OWNERSHIP_CONFLICT');
        throw codedError('EIO_MUX_PULL_OWNERSHIP_CONFLICT');
      }
      var owners = stream.eioPullQueueOwners || (stream.eioPullQueueOwners = []);
      if (owners.length !== queue.length || owners.indexOf(record) >= 0) {
        fail(ticket, 'EIO_MUX_PULL_OWNERSHIP_CONFLICT');
        throw codedError('EIO_MUX_PULL_OWNERSHIP_CONFLICT');
      }
      record.state = 'transferred';
      record.slot = 'queue';
      record.queue = queue;
      if (ticket.queued.indexOf(record) < 0) ticket.queued.push(record);
      owners.push(record);
      queue.push(pointer);
      return true;
    }

    function moveSlot(stream, from, to) {
      var owners = stream.eioPullSlotOwners;
      var pointer = stream[from + 'Packet'];
      if (!owners || !(pointer > 0) || from === to) return false;
      var record = owners[from];
      if (!record || record.pointer !== pointer || record.state !== 'transferred' ||
          record.slot !== from || record.queue !== null) return false;
      if (!stream.eioPullPackets || stream.eioPullPackets.get(pointer) !== record) return false;
      if (owners[to] && owners[to] !== record) return false;
      if (stream[to + 'Packet'] && stream[to + 'Packet'] !== pointer) return false;
      owners[from] = null;
      owners[to] = record;
      record.slot = to;
      stream[to + 'Packet'] = pointer;
      stream[from + 'Packet'] = 0;
      return true;
    }

    function moveSlotToQueue(stream, slot, queue) {
      var owners = stream.eioPullSlotOwners;
      var queueOwners = stream.eioPullQueueOwners;
      var pointer = stream[slot + 'Packet'];
      if (!owners || !queueOwners || !Array.isArray(queue) || !(pointer > 0)) return false;
      var record = owners[slot];
      if (!record || record.pointer !== pointer || record.state !== 'transferred' ||
          record.slot !== slot || record.queue !== null) return false;
      if (!stream.eioPullPackets || stream.eioPullPackets.get(pointer) !== record) return false;
      if (queueOwners.length !== queue.length || queueOwners.indexOf(record) >= 0) {
        throw codedError('EIO_MUX_PULL_OWNERSHIP_CONFLICT');
      }
      owners[slot] = null;
      stream[slot + 'Packet'] = 0;
      record.slot = 'queue';
      record.queue = queue;
      if (record.ticket && record.ticket.queued && record.ticket.queued.indexOf(record) < 0) {
        record.ticket.queued.push(record);
      }
      queueOwners.push(record);
      queue.push(pointer);
      return true;
    }

    function takeQueue(stream, ticket) {
      var queue = stream && stream.pullQueue && stream.pullQueue.queue;
      if (!queue || !queue.length) return null;
      var owners = stream.eioPullQueueOwners;
      if (!owners || owners.length !== queue.length) {
        fail(ticket, 'EIO_MUX_PULL_OWNERSHIP_CONFLICT');
        throw codedError('EIO_MUX_PULL_OWNERSHIP_CONFLICT');
      }
      var pointer = queue[0];
      var record = owners[0];
      if (!record || record.pointer !== pointer || record.state !== 'transferred' ||
          record.slot !== 'queue' || record.queue !== queue ||
          !stream.eioPullPackets || stream.eioPullPackets.get(pointer) !== record) {
        fail(ticket, 'EIO_MUX_PULL_OWNERSHIP_CONFLICT');
        throw codedError('EIO_MUX_PULL_OWNERSHIP_CONFLICT');
      }
      queue.shift();
      owners.shift();
      record.slot = null;
      record.queue = null;
      record.state = 'owned';
      record.ticketId = ticket.id;
      record.ticket = ticket;
      for (var index = 0; index < ticket.packets.length; index += 1) {
        if (ticket.packets[index] === record) return pointer;
      }
      ticket.packets.push(record);
      return pointer;
    }

    function releaseResultAccepted(result) {
      /* Real 61457 AVPacketPool.release returns void. A boolean/object ABI is
         accepted only when it positively confirms completion. */
      if (result === undefined) return true;
      if (result === true) return true;
      if (result && 'object' === typeof result && result.ok === true) return true;
      return false;
    }

    function setFault(stream, code, kind, slot) {
      if (!stream) return;
      if (!stream.eioPullFault) {
        stream.eioPullFault = {
          code: code || 'EIO_MUX_PULL_OWNERSHIP_CONFLICT',
          kind: kind || 'ownership',
          slot: slot || null
        };
      }
    }

    function slotAssociation(stream, owners, record, slot, pointer) {
      return stream.eioPullSlotOwners === owners &&
        owners[slot] === record &&
        stream[slot + 'Packet'] === pointer &&
        record.slot === slot &&
        record.queue === null;
    }

    function queueAssociation(stream, owners, queue, record, index, pointer) {
      return !!stream.pullQueue &&
        stream.pullQueue.queue === queue &&
        stream.eioPullQueueOwners === owners &&
        owners[index] === record &&
        queue[index] === pointer &&
        record.queue === queue &&
        record.slot === 'queue';
    }

    function releaseRecord(stream, record, reason) {
      if (!isStrict(stream) || !record) return false;
      if (record.state === 'freed' || record.state === 'retained' || record.state === 'closing') return false;
      if (stream.eioPullPoolClosed === true) {
        record.state = 'retained';
        record.reason = 'pool-closed';
        return false;
      }
      var map = stream.eioPullPackets;
      var pointer = record.pointer;
      if (!map || !(pointer > 0) || map.get(pointer) !== record) {
        record.state = 'retained';
        record.reason = 'identity-mismatch';
        setFault(stream, 'EIO_MUX_PULL_OWNERSHIP_CONFLICT', 'identity-mismatch');
        return false;
      }
      record.state = 'closing';
      record.reason = reason || null;
      var result = null;
      try {
        result = pool.release(pointer);
      } catch (error) {
        record.state = 'retained';
        record.reason = scalarCode(error, 'EIO_MUX_PULL_RELEASE_FAILED');
        setFault(stream, record.reason, 'release');
        return false;
      }
      if (!releaseResultAccepted(result)) {
        record.state = 'retained';
        record.reason = scalarCode(result, 'EIO_MUX_PULL_RELEASE_REFUSED');
        setFault(stream, record.reason, 'release');
        return false;
      }
      record.state = 'freed';
      if (stream.eioPullPackets !== map || map.get(pointer) !== record) {
        setFault(stream, 'EIO_MUX_PULL_OWNERSHIP_CONFLICT', 'release-association');
        return false;
      }
      map.delete(pointer);
      return true;
    }

    function releaseSlot(stream, slot, reason) {
      if (!isStrict(stream)) return false;
      var owners = stream.eioPullSlotOwners;
      var record = owners ? owners[slot] : null;
      var pointer = stream[slot + 'Packet'];
      if (!record) {
        /* The boolean proves a safe association: true means the exact record
           was released or the slot is genuinely vacant, not that a native
           free happened. Unknown strict ownership with a present pointer is
           never permission to call the pool. */
        if (pointer > 0) {
          stream.eioPullLastUnknownSlot = slot;
          setFault(stream, 'EIO_MUX_PULL_OWNERSHIP_CONFLICT', 'unknown-slot', slot);
          return false;
        }
        return true;
      }
      if (!(pointer > 0) || record.pointer !== pointer) {
        setFault(stream, 'EIO_MUX_PULL_OWNERSHIP_CONFLICT', 'slot-pointer', slot);
        return false;
      }
      if (record.state !== 'transferred') {
        setFault(stream, 'EIO_MUX_PULL_OWNERSHIP_CONFLICT', 'slot-state', slot);
        return false;
      }
      if (record.slot !== slot) {
        setFault(stream, 'EIO_MUX_PULL_OWNERSHIP_CONFLICT', 'slot-index', slot);
        return false;
      }
      if (record.queue !== null) {
        setFault(stream, 'EIO_MUX_PULL_OWNERSHIP_CONFLICT', 'slot-queue', slot);
        return false;
      }
      if (!stream.eioPullPackets || stream.eioPullPackets.get(pointer) !== record) {
        setFault(stream, 'EIO_MUX_PULL_OWNERSHIP_CONFLICT', 'slot-map', slot);
        return false;
      }
      var released = releaseRecord(stream, record, reason);
      if (!released) return false;
      if (!slotAssociation(stream, owners, record, slot, pointer)) {
        setFault(stream, 'EIO_MUX_PULL_OWNERSHIP_CONFLICT', 'slot-association', slot);
        return false;
      }
      owners[slot] = null;
      return true;
    }

    function releaseQueue(stream, reason) {
      if (!isStrict(stream)) return false;
      var queue = stream.pullQueue && stream.pullQueue.queue;
      if (!queue) return true;
      var owners = stream.eioPullQueueOwners;
      if (!Array.isArray(owners)) owners = [];
      if (queue.length !== owners.length) {
        setFault(stream, 'EIO_MUX_PULL_OWNERSHIP_CONFLICT', 'queue-length');
        return false;
      }
      if (!queue.length) return true;
      for (var index = 0; index < owners.length; index += 1) {
        var record = owners[index];
        var pointer = queue[index];
        if (!record) {
          if (pointer > 0) {
            setFault(stream, 'EIO_MUX_PULL_OWNERSHIP_CONFLICT', 'unknown-queue');
            return false;
          }
          continue;
        }
        if (!(pointer > 0) || record.pointer !== pointer) {
          setFault(stream, 'EIO_MUX_PULL_OWNERSHIP_CONFLICT', 'queue-pointer');
          return false;
        }
        if (record.state !== 'transferred') {
          setFault(stream, 'EIO_MUX_PULL_OWNERSHIP_CONFLICT', 'queue-state');
          return false;
        }
        if (record.slot !== 'queue') {
          setFault(stream, 'EIO_MUX_PULL_OWNERSHIP_CONFLICT', 'queue-slot');
          return false;
        }
        if (record.queue !== queue) {
          setFault(stream, 'EIO_MUX_PULL_OWNERSHIP_CONFLICT', 'queue-reference');
          return false;
        }
        if (!stream.eioPullPackets || stream.eioPullPackets.get(pointer) !== record) {
          setFault(stream, 'EIO_MUX_PULL_OWNERSHIP_CONFLICT', 'queue-map');
          return false;
        }
      }
      for (var releaseIndex = 0; releaseIndex < owners.length; releaseIndex += 1) {
        var releasedRecord = owners[releaseIndex];
        var releasedPointer = queue[releaseIndex];
        if (!releasedRecord) continue;
        if (!releaseRecord(stream, releasedRecord, reason)) return false;
        if (!queueAssociation(stream, owners, queue, releasedRecord, releaseIndex, releasedPointer)) {
          setFault(stream, 'EIO_MUX_PULL_OWNERSHIP_CONFLICT', 'queue-association');
          return false;
        }
      }
      if (stream.eioPullQueueOwners !== owners || !stream.pullQueue ||
          stream.pullQueue.queue !== queue || queue.length !== owners.length) {
        setFault(stream, 'EIO_MUX_PULL_OWNERSHIP_CONFLICT', 'queue-association');
        return false;
      }
      for (var finalIndex = 0; finalIndex < owners.length; finalIndex += 1) {
        var finalRecord = owners[finalIndex];
        var finalPointer = queue[finalIndex];
        if (!finalRecord) {
          if (finalPointer > 0) {
            setFault(stream, 'EIO_MUX_PULL_OWNERSHIP_CONFLICT', 'unknown-queue');
            return false;
          }
          continue;
        }
        if (finalRecord.pointer !== finalPointer || finalRecord.state !== 'freed' ||
            finalRecord.slot !== 'queue' || finalRecord.queue !== queue) {
          setFault(stream, 'EIO_MUX_PULL_OWNERSHIP_CONFLICT', 'queue-association');
          return false;
        }
        if (stream.eioPullPackets && stream.eioPullPackets.get(finalPointer) === finalRecord) {
          setFault(stream, 'EIO_MUX_PULL_OWNERSHIP_CONFLICT', 'queue-map');
          return false;
        }
      }
      queue.length = 0;
      owners.length = 0;
      return true;
    }

    function prepareTeardown(stream, reason) {
      if (!isStrict(stream)) return { ok: true, pending: 0, code: null };
      ensure(stream);
      var failure = null;
      var slots = ['back', 'front'];
      for (var slotIndex = 0; slotIndex < slots.length; slotIndex += 1) {
        var slot = slots[slotIndex];
        var owners = stream.eioPullSlotOwners;
        var record = owners ? owners[slot] : null;
        var pointer = stream[slot + 'Packet'];
        if (!record) {
          if (pointer > 0) {
            stream.eioPullLastUnknownSlot = slot;
            setFault(stream, 'EIO_MUX_PULL_OWNERSHIP_CONFLICT', 'unknown-slot', slot);
            failure = failure || { code: 'EIO_MUX_PULL_OWNERSHIP_CONFLICT', kind: 'slot', slot: slot };
          }
          continue;
        }
        if (!releaseSlot(stream, slot, reason || ('teardown-' + slot))) {
          failure = failure || {
            code: (stream.eioPullFault && stream.eioPullFault.code) ||
              record.reason || 'EIO_MUX_PULL_RELEASE_FAILED',
            kind: 'slot',
            slot: slot
          };
          continue;
        }
        if (stream.eioPullSlotOwners === owners && owners[slot] === null &&
            stream[slot + 'Packet'] === pointer) {
          stream[slot + 'Packet'] = 0;
        } else {
          setFault(stream, 'EIO_MUX_PULL_OWNERSHIP_CONFLICT', 'slot-association', slot);
          failure = failure || {
            code: 'EIO_MUX_PULL_OWNERSHIP_CONFLICT', kind: 'slot', slot: slot
          };
        }
      }
      var queue = stream.pullQueue && stream.pullQueue.queue;
      if (queue) {
        if (!releaseQueue(stream, reason || 'teardown-queue')) {
          failure = failure || {
            code: (stream.eioPullFault && stream.eioPullFault.code) ||
              'EIO_MUX_PULL_OWNERSHIP_CONFLICT',
            kind: 'queue',
            slot: 'queue'
          };
        }
      }
      var remaining = stream.eioPullPackets ? stream.eioPullPackets.size : 0;
      if (remaining > 0) {
        failure = failure || { code: 'EIO_MUX_PULL_OWNERSHIP_UNPROVEN', kind: 'packet-map' };
      }
      if (stream.eioPullFault && !failure) {
        failure = {
          code: stream.eioPullFault.code || 'EIO_MUX_PULL_OWNERSHIP_UNPROVEN',
          kind: stream.eioPullFault.kind || 'fault'
        };
      }
      return failure ? {
        ok: false,
        pending: remaining,
        code: failure.code,
        planReason: 'ownership-unproven',
        kind: failure.kind,
        slot: failure.slot || null
      } : { ok: true, pending: 0, code: null };
    }

    function releasePulled(ticket, pointer, reason, expectedRecord) {
      var stream = streamOf(ticket);
      if (!stream || !ticket) return false;
      if (pointer && 'object' === typeof pointer) {
        expectedRecord = pointer;
        pointer = expectedRecord.pointer;
      }
      if (reason && 'object' === typeof reason) {
        if (!expectedRecord) expectedRecord = reason;
        reason = 'string' === typeof arguments[3] ? arguments[3] : null;
      }
      for (var index = 0; index < ticket.packets.length; index += 1) {
        var record = ticket.packets[index];
        if (record.pointer === pointer && record.ticketId === ticket.id && record.state === 'owned') {
          if (expectedRecord && record !== expectedRecord) return false;
          return releaseRecord(stream, record, reason);
        }
      }
      return false;
    }

    function releaseTicketPackets(ticket, reason) {
      var stream = streamOf(ticket);
      if (!stream) return false;
      var owners = stream.eioPullQueueOwners;
      for (var queuedIndex = 0; queuedIndex < ticket.queued.length; queuedIndex += 1) {
        var queued = ticket.queued[queuedIndex];
        if (!queued || queued.state !== 'transferred') continue;
        if (!owners || queued.slot !== 'queue' || !queued.queue) {
          setFault(stream, 'EIO_MUX_PULL_OWNERSHIP_CONFLICT', 'queue-association');
          continue;
        }
        var queue = queued.queue;
        if (owners.length !== queue.length) {
          setFault(stream, 'EIO_MUX_PULL_OWNERSHIP_CONFLICT', 'queue-length');
          continue;
        }
        if (!(queued.pointer > 0) || !stream.eioPullPackets ||
            stream.eioPullPackets.get(queued.pointer) !== queued) {
          setFault(stream, 'EIO_MUX_PULL_OWNERSHIP_CONFLICT', 'queue-map');
          continue;
        }
        var found = -1;
        for (var index = 0; index < owners.length; index += 1) {
          if (owners[index] === queued) {
            found = index;
            break;
          }
        }
        if (found < 0 || queue[found] !== queued.pointer) {
          setFault(stream, 'EIO_MUX_PULL_OWNERSHIP_CONFLICT', 'queue-association');
          continue;
        }
        owners.splice(found, 1);
        queue.splice(found, 1);
        queued.queue = null;
        queued.slot = null;
        queued.state = 'owned';
      }
      var changed = false;
      for (var packetIndex = 0; packetIndex < ticket.packets.length; packetIndex += 1) {
        var record = ticket.packets[packetIndex];
        if (record.ticketId !== ticket.id) continue;
        if (record.state === 'owned' && releaseRecord(stream, record, reason || 'reclaim')) changed = true;
      }
      return changed;
    }

    function complete(ticket, reason) {
      if (!ticket || ticket.state === 'settled' || ticket.state === 'reclaiming') return;
      var stream = streamOf(ticket);
      ticket.state = 'completing';
      if (stream) {
        /* Normal completion keeps stream-owned slot/queue records alive. Only a
           packet still owned solely by this ticket is released here. */
        for (var index = 0; index < ticket.packets.length; index += 1) {
          var record = ticket.packets[index];
          if (record.ticketId !== ticket.id) continue;
          if (record.state === 'owned') releaseRecord(stream, record, reason || 'complete');
        }
        stream.eioPullTickets.delete(ticket);
        if (stream.eioSwapPending === ticket) stream.eioSwapPending = null;
        if (ticket.seekSync && stream.seekSync === ticket.seekSync) stream.seekSync = null;
        ticket.seekSync = null;
      }
      settle(ticket);
    }

    function reclaim(ticket, reason) {
      if (!ticket) return;
      if (ticket.state === 'settled') return;
      if (ticket.state === 'reclaiming') return;
      var stream = streamOf(ticket);
      ticket.state = 'reclaiming';
      if (stream) {
        releaseTicketPackets(ticket, reason);
        stream.eioPullTickets.delete(ticket);
        if (stream.eioSwapPending === ticket) stream.eioSwapPending = null;
        if (ticket.seekSync && stream.seekSync === ticket.seekSync) stream.seekSync = null;
        ticket.seekSync = null;
      }
      settle(ticket);
    }

    function fail(ticket, code, error) {
      if (!ticket || ticket.state === 'settled') return;
      ticket.errorCode = scalarCode(error, code || 'EIO_MUX_PULL_FAILED');
    }

    function revoke(stream, reason) {
      if (!isStrict(stream)) return false;
      ensure(stream);
      stream.eioPullEpoch = (stream.eioPullEpoch || 0) + 1;
      var tickets = stream.eioPullTickets;
      tickets.forEach(function (ticket) {
        if (ticket.state === 'pending') {
          ticket.revoked = true;
          ticket.revokeReason = reason || null;
        }
      });
      if (stream.eioSwapPending && stream.eioSwapPending.state === 'pending') {
        stream.eioSwapPending.revoked = true;
        stream.eioSwapPending.revokeReason = reason || null;
      }
      tickets.forEach(function (ticket) {
        if (ticket.seekSync && stream.seekSync === ticket.seekSync) stream.seekSync = null;
        ticket.seekSync = null;
      });
      return true;
    }

    function drain(streams, timeoutMs) {
      var tickets = [];
      var list = streams || [];
      for (var index = 0; index < list.length; index += 1) {
        var stream = list[index];
        if (!isStrict(stream) || !stream.eioPullTickets) continue;
        stream.eioPullTickets.forEach(function (ticket) {
          if (ticket.state === 'pending' && tickets.indexOf(ticket) < 0) tickets.push(ticket);
        });
      }
      if (!tickets.length) return Promise.resolve({ ok: true, pending: 0 });
      var bound = 'number' === typeof timeoutMs && timeoutMs > 0 ? timeoutMs : DEFAULT_DRAIN_MS;
      var timer = null;
      var timeout = new Promise(function (resolve) {
        timer = setTimeout(function () { resolve(false); }, bound);
      });
      var settled = Promise.all(tickets.map(function (ticket) { return ticket.settled; }))
        .then(function () { return true; }, function () { return false; });
      return Promise.race([settled, timeout]).then(function (ok) {
        return { ok: ok, pending: ok ? 0 : tickets.filter(function (t) { return t.state === 'pending'; }).length };
      }).then(function (result) {
        if (timer !== null) clearTimeout(timer);
        timer = null;
        return result;
      }, function () {
        if (timer !== null) clearTimeout(timer);
        timer = null;
        return { ok: false, pending: tickets.length };
      });
    }

    function closePool(stream) {
      if (!isStrict(stream)) return;
      ensure(stream);
      stream.eioPullPoolClosed = true;
    }

    function finishTickets(tickets, reason) {
      if (!tickets) return;
      if (tickets.audio) reclaim(tickets.audio, reason);
      if (tickets.video) reclaim(tickets.video, reason);
    }

    function guard(tickets, reason) {
      var failed = (tickets.audio && stale(tickets.audio)) || (tickets.video && stale(tickets.video));
      if (failed) {
        finishTickets(tickets, reason);
        throw codedError('EIO_MUX_PULL_STALE');
      }
    }

    return Object.freeze({
      VERSION: VERSION,
      KIND: KIND,
      STALE: STALE,
      DEFAULT_DRAIN_MS: DEFAULT_DRAIN_MS,
      isStrict: isStrict,
      ensure: ensure,
      begin: begin,
      beginTickets: beginTickets,
      revoke: revoke,
      stale: stale,
      check: check,
      transferSlot: transferSlot,
      transferQueue: transferQueue,
      takeQueue: takeQueue,
      moveSlot: moveSlot,
      moveSlotToQueue: moveSlotToQueue,
      register: register,
      releaseSlot: releaseSlot,
      releaseQueue: releaseQueue,
      prepareTeardown: prepareTeardown,
      releasePulled: releasePulled,
      complete: complete,
      reclaim: reclaim,
      drain: drain,
      closePool: closePool,
      fail: fail,
      finishTickets: finishTickets,
      guard: guard
    });
  }

  return Object.freeze({
    VERSION: VERSION,
    KIND: KIND,
    STALE: STALE,
    DEFAULT_DRAIN_MS: DEFAULT_DRAIN_MS,
    createMsePacketOwnership: createMsePacketOwnership
  });
});
}
}]);
