"use strict";
/* Strict MSE pull/swap/seek runtime. Chunk 90206 / module 90192.
   Packet/ticket identity, reclaim and drain live in 90193 (chunk 90207). */
(self.webpackChunkAVPlayer=self.webpackChunkAVPlayer||[]).push([[90206],{
90192:(module,exports,__webpack_require__)=>{
/*!
 * mse-pull-lifetime.js
 *
 * Native-algorithm half of the strict MSE pull lifetime. This module keeps the
 * actual pull/swap/sync/seek/source-open ordering and native field writes, and
 * delegates every ticket, epoch, packet identity, exact-once release/reclaim and
 * bounded drain decision to the real 90193 ownership module. It mints no
 * quiescence token, ACK, byte credit or second ledger.
 */
var __eioPacketOwnership = __webpack_require__(90193);

(function (root, factory) {
  'use strict';
  if (typeof module === 'object' && module && module.exports) {
    module.exports = factory(__eioPacketOwnership);
  } else if (typeof define === 'function' && define.amd) {
    define([], function () { return factory(root.EioMsePacketOwnership); });
  } else {
    root.EioMsePullLifetime = factory(root.EioMsePacketOwnership);
  }
})(typeof globalThis !== 'undefined' ? globalThis : this, function (ownershipApi) {
  'use strict';

  var VERSION = 1;
  var KIND = 'eio-mse-pull-lifetime/v1';
  var DEFAULT_DRAIN_MS = 1500;
  var STALE = ownershipApi && ownershipApi.STALE;

  function codedError(code, message) {
    var error = new Error(message || code);
    error.code = code;
    return error;
  }

  function checkDependencies(deps) {
    if (!deps || typeof deps !== 'object') throw codedError('EIO_MSE_PULL_DEPS_INVALID');
    if (!deps.packetPool || typeof deps.packetPool.release !== 'function' ||
        typeof deps.packetPool.alloc !== 'function') {
      throw codedError('EIO_MSE_PULL_POOL_INVALID');
    }
    if (typeof deps.now !== 'function') throw codedError('EIO_MSE_PULL_CLOCK_INVALID');
    var native = deps.native;
    if (!native || typeof native !== 'object') throw codedError('EIO_MSE_PULL_NATIVE_INVALID');
    var required = [
      'readI32', 'readU32', 'readBigInt', 'writeI32', 'writeBigInt',
      'scale', 'scaleInt', 'minBig', 'maxBig', 'diffBig',
      'getSideData', 'setSideData', 'setPacketData', 'allocBytes',
      'writeBytes', 'readBytes', 'copyMemory', 'copyPacket', 'isKeyframe',
      'isRandomAccessVideo', 'codecparId', 'releaseCodecpar', 'videoTimeBase', 'delay'
    ];
    for (var index = 0; index < required.length; index += 1) {
      if (typeof native[required[index]] !== 'function') {
        throw codedError('EIO_MSE_PULL_NATIVE_INVALID', required[index]);
      }
    }
    var millisecondsTimeBase = native.millisecondsTimeBase;
    if (!millisecondsTimeBase || 'object' !== typeof millisecondsTimeBase ||
        'number' !== typeof millisecondsTimeBase.num ||
        'number' !== typeof millisecondsTimeBase.den ||
        !Number.isFinite(millisecondsTimeBase.num) || !Number.isFinite(millisecondsTimeBase.den) ||
        millisecondsTimeBase.num <= 0 || millisecondsTimeBase.den <= 0) {
      throw codedError('EIO_MSE_PULL_NATIVE_INVALID', 'millisecondsTimeBase');
    }
    if ('bigint' !== typeof native.noTimestamp) {
      throw codedError('EIO_MSE_PULL_NATIVE_INVALID', 'noTimestamp');
    }
    var timeBaseIndex = native.timeBaseIndex;
    if (!(('symbol' === typeof timeBaseIndex) || ('string' === typeof timeBaseIndex) ||
        ('number' === typeof timeBaseIndex && Number.isFinite(timeBaseIndex)))) {
      throw codedError('EIO_MSE_PULL_NATIVE_INVALID', 'timeBaseIndex');
    }
    return { packetPool: deps.packetPool, now: deps.now, native: native };
  }

  function createMsePullLifetime(dependencies) {
    var deps = checkDependencies(dependencies);
    var pool = deps.packetPool;
    var native = deps.native;
    if (!ownershipApi || typeof ownershipApi.createMsePacketOwnership !== 'function') {
      throw codedError('EIO_MUX_PULL_OWNERSHIP_MODULE_MISSING');
    }
    var ownership = ownershipApi.createMsePacketOwnership({ packetPool: pool });
    var isStrict = ownership.isStrict;
    var begin = ownership.begin;
    var beginTickets = ownership.beginTickets;
    var revoke = ownership.revoke;
    var stale = ownership.stale;
    var check = ownership.check;
    var transferSlot = ownership.transferSlot;
    var transferQueue = ownership.transferQueue;
    var takeQueue = ownership.takeQueue;
    var moveSlot = ownership.moveSlot;
    var moveSlotToQueue = ownership.moveSlotToQueue;
    var register = ownership.register;
    var releaseSlot = ownership.releaseSlot;
    var releaseQueue = ownership.releaseQueue;
    var prepareTeardown = ownership.prepareTeardown;
    var releasePulled = ownership.releasePulled;
    function complete(value, reason) {
      if (!value) return;
      if (Object.prototype.hasOwnProperty.call(value, 'audio') &&
          Object.prototype.hasOwnProperty.call(value, 'video')) {
        if (value.audio) ownership.complete(value.audio, reason);
        if (value.video) ownership.complete(value.video, reason);
      } else ownership.complete(value, reason);
    }
    var reclaim = ownership.reclaim;
    var drain = ownership.drain;
    var closePool = ownership.closePool;
    var fail = ownership.fail;
    var finishTickets = ownership.finishTickets;
    var guard = ownership.guard;

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

    function readDts(pointer) { return native.readBigInt(pointer + 16); }
    function readPts(pointer) { return native.readBigInt(pointer + 8); }
    function readFlags(pointer) { return native.readI32(pointer + 36); }

    function faultCode(error, fallback) {
      if (error && typeof error.code === 'string' && error.code.length <= 64) return error.code;
      return fallback;
    }

    function surfaceFault(stream, error, fallback) {
      if (!stream) return;
      stream.eioPullFault = { code: faultCode(error, fallback), kind: 'pull' };
    }

    function ownershipUnproven(stream, reason) {
      var cause = stream && stream.eioPullFault && stream.eioPullFault.code ||
        'EIO_MUX_PULL_RELEASE_FAILED';
      var error = codedError('EIO_MUX_PULL_OWNERSHIP_UNPROVEN');
      error.planReason = 'ownership-unproven';
      error.cause = cause;
      error.reason = reason || null;
      surfaceFault(stream, error, 'EIO_MUX_PULL_OWNERSHIP_UNPROVEN');
      return error;
    }

    var EOF_CODE = -1048576;

    function nativeFailure(ticket, value, context) {
      var stream = ticket && ticket.stream;
      var error = codedError('EIO_MUX_PULL_NATIVE_FAILURE');
      error.planReason = 'native-pull-negative';
      error.nativeCode = value;
      error.context = context || null;
      fail(ticket, error.code, error);
      surfaceFault(stream, error, error.code);
      reclaim(ticket, 'native-failure');
      return error;
    }

    function requireEof(ticket, value, context) {
      if (!ticket) return;
      if (value === EOF_CODE) return;
      throw nativeFailure(ticket, value, context);
    }

    function swap(task, stream, host) {
      if (!isStrict(stream)) return host.eioSwapBaseline(stream, task);
      if (stream.eioSwapPending) return false;
      var oldBack = stream.backPacket;
      if (oldBack) {
        if (!releaseSlot(stream, 'back', 'swap-back')) throw ownershipUnproven(stream, 'swap-back');
        stream.backPacket = 0;
      }
      if (!stream.frontBuffered) return false;
      if (stream.frontPacket > 0 && !moveSlot(stream, 'front', 'back')) {
        throw ownershipUnproven(stream, 'swap-move-front');
      }
      if (!stream.frontPacket && stream.eioPullSlotOwners && stream.eioPullSlotOwners.front) {
        throw ownershipUnproven(stream, 'swap-front-association');
      }
      stream.frontBuffered = false;
      var ticket = begin(stream, 'swap');
      if (!ticket) return false;
      stream.eioSwapPending = ticket;
      var started = deps.now();
      var promise = host.pullAVPacket(stream, task, ticket);
      promise.then(function (value) {
        try {
          swapValue(task, stream, host, ticket, value, started);
        } catch (error) {
          fail(ticket, 'EIO_MUX_PULL_SWAP_FAILED', error);
          surfaceFault(stream, error, 'EIO_MUX_PULL_SWAP_FAILED');
          reclaim(ticket, 'swap-fault');
          if (stream.eioSwapPending === ticket) stream.eioSwapPending = null;
        }
      }, function (error) {
        fail(ticket, 'EIO_MUX_PULL_SWAP_FAILED', error);
        surfaceFault(stream, error, 'EIO_MUX_PULL_SWAP_FAILED');
        reclaim(ticket, 'swap-error');
        if (stream.eioSwapPending === ticket) stream.eioSwapPending = null;
      });
      return true;
    }

    function swapValue(task, stream, host, ticket, value, started) {
      // pullAVPacket has already registered positive results before returning.
      // Re-registering here would turn the same pull into a duplicate-pointer fault.
      if (value === STALE) {
        reclaim(ticket, 'swap-stale');
        if (stream.eioSwapPending === ticket) stream.eioSwapPending = null;
        return;
      }
      if ('number' !== typeof value) {
        fail(ticket, 'EIO_MUX_PULL_INVALID_RESULT');
        reclaim(ticket, 'swap-invalid');
        if (stream.eioSwapPending === ticket) stream.eioSwapPending = null;
        return;
      }
      if (value < 0) {
        requireEof(ticket, value, 'swap');
        if (!stale(ticket)) {
          stream.packetEnded = true;
          stream.frontPacket = 0;
          if (stream.eioPullSlotOwners) stream.eioPullSlotOwners.front = null;
        }
        reclaim(ticket, 'swap-eof');
        if (stream.eioSwapPending === ticket) stream.eioSwapPending = null;
        return;
      }
      if (stale(ticket)) {
        reclaim(ticket, 'swap-stale');
        if (stream.eioSwapPending === ticket) stream.eioSwapPending = null;
        return;
      }
      var elapsed = deps.now() - started;
      if (elapsed > 5) stream.startTimestamp += BigInt(Math.floor(elapsed));
      if (!transferSlot(ticket, value, 'front')) {
        fail(ticket, 'EIO_MUX_PULL_TRANSFER_FAILED');
        reclaim(ticket, 'swap-transfer-refused');
        if (stream.eioSwapPending === ticket) stream.eioSwapPending = null;
        return;
      }
      stream.frontBuffered = true;
      if (ticket.seekSync && stream.seekSync === ticket.seekSync) {
        stream.seekSync();
        stream.seekSync = null;
      }
      if (stream.eioSwapPending === ticket) stream.eioSwapPending = null;
      complete(ticket, 'swap-settled');
      if (!stream.backPacket) host.swap(stream, task);
    }

    async function pullAVPacket(stream, task, host, ticket) {
      if (!isStrict(stream)) return host.eioPullAVPacketBaseline(stream, task);
      if (!ticket) throw codedError('EIO_MUX_PULL_TICKET_REQUIRED');
      var queue = stream.pullQueue;
      if (stale(ticket)) return STALE;
      if (queue.ended && !queue.queue.length) return -1048576;
      var packet = 0;
      if (queue.queue.length) {
        packet = takeQueue(stream, ticket);
        if (!packet) {
          fail(ticket, 'EIO_MUX_PULL_OWNERSHIP_CONFLICT');
          reclaim(ticket, 'queue-identity');
          return STALE;
        }
      } else {
        packet = await host.pullAVPacketInternal(task, stream.pullIPC);
        packet = check(ticket, packet);
        if (packet === STALE) return STALE;
      }
      if ('number' !== typeof packet) {
        fail(ticket, 'EIO_MUX_PULL_INVALID_RESULT');
        reclaim(ticket, 'pull-invalid');
        throw codedError('EIO_MUX_PULL_INVALID_RESULT');
      }
      if (packet < 0) {
        if (stale(ticket)) return STALE;
        requireEof(ticket, packet, 'pull');
        queue.ended = true;
        return -1048576;
      }
      if (readDts(packet) < BigInt(0)) return packet;
      if (stale(ticket)) return STALE;
      if (readDts(packet) < queue.lastDTS) host.logDts(packet, queue.lastDTS);
      if (stale(ticket)) return STALE;
      native.writeBigInt(packet + 48, native.noTimestamp);
      if ('audio' === stream.type && readDts(packet) > BigInt(0)) {
        if (stale(ticket)) return STALE;
        native.writeBigInt(packet + 8, readDts(packet));
      } else if ('video' === stream.type && !task.isLive) {
        if (readFlags(packet) & 1 && native.isRandomAccessVideo(stream, packet)) {
          if (readPts(packet) < queue.lastPTS) {
            host.logPts(packet, queue.lastPTS);
            if (stale(ticket)) return STALE;
            native.writeBigInt(packet + 8, queue.lastPTS + (readDts(packet) - queue.lastDTS));
            var lookahead = await host.pullAVPacketInternal(task, stream.pullIPC);
            lookahead = check(ticket, lookahead);
            if (lookahead === STALE) return STALE;
            if (lookahead < 0) {
              if (stale(ticket)) return STALE;
              requireEof(ticket, lookahead, 'lookahead');
              queue.ended = true;
            } else {
              if (!transferQueue(ticket, lookahead, queue.queue)) {
                fail(ticket, 'EIO_MUX_PULL_OWNERSHIP_CONFLICT');
                reclaim(ticket, 'lookahead-queue');
                return STALE;
              }
              var minimum = readPts(lookahead);
              var original = readPts(lookahead);
              for (;;) {
                var next = await host.pullAVPacketInternal(task, stream.pullIPC);
                next = check(ticket, next);
                if (next === STALE) return STALE;
                if (next < 0) {
                  if (stale(ticket)) return STALE;
                  requireEof(ticket, next, 'lookahead');
                  queue.ended = true;
                  break;
                }
                if (!transferQueue(ticket, next, queue.queue)) {
                  fail(ticket, 'EIO_MUX_PULL_OWNERSHIP_CONFLICT');
                  reclaim(ticket, 'lookahead-queue');
                  return STALE;
                }
                if (readPts(next) > original) break;
                minimum = native.minBig(minimum, readPts(next));
              }
              if (stale(ticket)) return STALE;
              if (minimum < readPts(packet)) queue.diff = readDts(packet) - queue.lastDTS;
            }
          } else {
            if (stale(ticket)) return STALE;
            queue.diff = BigInt(0);
          }
        } else {
          if (stale(ticket)) return STALE;
          native.writeBigInt(packet + 8, readPts(packet) + queue.diff);
        }
      }
      if (stale(ticket)) return STALE;
      if (readPts(packet) > queue.lastPTS) queue.lastPTS = readPts(packet);
      queue.lastDTS = readDts(packet);
      if ('audio' === stream.type && queue.useSampleRateTimeBase) {
        if (stale(ticket)) return STALE;
        var output = outputOf(stream);
        if (output !== ticket.output || timeBaseOf(output) !== ticket.timeBase) {
          reclaim(ticket, 'timebase-identity');
          return STALE;
        }
        if (queue.frameCount === native.noTimestamp) {
          queue.frameCount = native.scale(readDts(packet), packet + 72, output.timeBase);
          native.writeBigInt(packet + 16, queue.frameCount);
          native.writeBigInt(packet + 8, queue.frameCount);
        } else {
          queue.frameCount = queue.frameCount + BigInt(0 | native.readI32(stream.codecpar + 144));
          native.writeBigInt(packet + 16, queue.frameCount);
          native.writeBigInt(packet + 8, queue.frameCount);
        }
        native.copyMemory(packet + 72, output.timeBase[native.timeBaseIndex], 8);
      }
      return packet;
    }

    async function syncToKeyframe(task, host, tickets) {
      var ok = true;
      var sideData = null;
      var video = task.video;
      var audio = task.audio;
      var videoTicket = tickets ? tickets.video : null;
      var audioTicket = tickets ? tickets.audio : null;
      if ((isStrict(video) && !videoTicket) || (isStrict(audio) && !audioTicket)) {
        throw codedError('EIO_MUX_PULL_STALE');
      }
      if (video) {
        for (;;) {
          var videoPacket = await host.pullAVPacket(video, task, videoTicket);
          if (videoPacket === STALE) return STALE;
          if (videoTicket && stale(videoTicket)) return STALE;
          if (videoPacket < 0) {
            requireEof(videoTicket, videoPacket, 'sync-video');
            if (!videoTicket || !stale(videoTicket)) {
              video.packetEnded = true;
              video.backPacket = 0;
            }
            break;
          }
          if (videoTicket) {
            if (!transferSlot(videoTicket, videoPacket, 'back')) {
              fail(videoTicket, 'EIO_MUX_PULL_TRANSFER_FAILED');
              reclaim(videoTicket, 'sync-video-slot');
              return STALE;
            }
          } else {
            video.backPacket = videoPacket;
          }
          if (audio && !audio.packetEnded && (!audio.backPacket ||
              native.scale(readPts(audio.backPacket), audio.backPacket + 72, native.millisecondsTimeBase) <
              native.scale(readPts(video.backPacket), video.backPacket + 72, native.millisecondsTimeBase))) {
            for (;;) {
              if (audio.backPacket > 0) {
                var side = native.getSideData(audio.backPacket, 1);
                if (side) sideData = native.readBytes(native.readU32(side), native.readU32(side + 4)).slice();
                if (audioTicket) { releaseSlot(audio, 'back', 'sync-audio'); audio.backPacket = 0; }
                else pool.release(audio.backPacket);
              }
              var audioPacket = await host.pullAVPacket(audio, task, audioTicket);
              if (audioPacket === STALE) return STALE;
              if (audioTicket && stale(audioTicket)) return STALE;
              if (audioPacket < 0) {
                requireEof(audioTicket, audioPacket, 'sync-audio');
                if (!audioTicket || !stale(audioTicket)) {
                  audio.packetEnded = true;
                  audio.backPacket = 0;
                }
                break;
              }
              if (audioTicket) {
                if (!transferSlot(audioTicket, audioPacket, 'back')) {
                  fail(audioTicket, 'EIO_MUX_PULL_TRANSFER_FAILED');
                  reclaim(audioTicket, 'sync-audio-slot');
                  return STALE;
                }
              } else {
                audio.backPacket = audioPacket;
              }
              if (native.scale(readPts(audio.backPacket), audio.backPacket + 72, native.millisecondsTimeBase) >
                  native.scale(readPts(video.backPacket), video.backPacket + 72, native.millisecondsTimeBase)) break;
            }
          }
          if (native.isKeyframe(video.backPacket) || native.isRandomAccessVideo(video, video.backPacket)) break;
          ok = false;
          if (videoTicket) { releaseSlot(video, 'back', 'sync-video'); video.backPacket = 0; }
          else pool.release(video.backPacket);
        }
      }
      if (audio && audio.backPacket && !native.getSideData(audio.backPacket, 1) && sideData) {
        var bytes = native.allocBytes(sideData.length);
        native.writeBytes(bytes, sideData.length, sideData);
        native.setSideData(audio.backPacket, 1, bytes, sideData.length);
      }
      return ok;
    }

    async function beforeSeek(task, host) {
      var streams = [];
      if (isStrict(task.audio)) streams.push(task.audio);
      if (isStrict(task.video)) streams.push(task.video);
      host.eioCancelWake(task.audio);
      host.eioCancelWake(task.video);
      for (var index = 0; index < streams.length; index += 1) {
        var stream = streams[index];
        if (stream.loop && typeof stream.loop.stop === 'function') stream.loop.stop();
        revoke(stream, 'beforeSeek');
      }
      var drained = await drain(streams, DEFAULT_DRAIN_MS);
      if (!drained.ok) {
        var error = codedError('EIO_MUX_PULL_SETTLE_TIMEOUT');
        error.planReason = 'pull-pending';
        error.pending = drained.pending;
        throw error;
      }
      var waits = [];
      if (task.audio && !isStrict(task.audio) && (task.audio.ended || task.audio.frontBuffered ||
          waits.push(new Promise(function (resolve) { task.audio.seekSync = resolve; })))) {}
      if (task.video && !isStrict(task.video) && (task.video.ended || task.video.frontBuffered ||
          waits.push(new Promise(function (resolve) { task.video.seekSync = resolve; })))) {}
      await Promise.all(waits);
      if (task.audio) {
        if (isStrict(task.audio)) { if (task.audio.loop) task.audio.loop.stop(); }
        else task.audio.loop.stop();
      }
      if (task.video) {
        if (isStrict(task.video)) { if (task.video.loop) task.video.loop.stop(); }
        else task.video.loop.stop();
      }
      await beforeSeekStream(task, task.audio, host, 'audio');
      await beforeSeekStream(task, task.video, host, 'video');
      task.seeking = true;
      host.logSeekEnd(task.taskId);
    }

    async function beforeSeekStream(task, stream, host, type) {
      if (!stream) return;
      if (!stream.enableRawMpeg) await host.eioAfterSeekFlush(stream);
      host.eioDiscardQueue(stream);
      if (host.removeAllBufferNeeded()) {
        await new Promise(function (resolve) { stream.track.removeAllBuffer(function () { resolve(); }); });
      }
      stream.track.reset();
      if (isStrict(stream)) {
        if (stream.backPacket) {
          if (!releaseSlot(stream, 'back', 'beforeSeek-back')) throw ownershipUnproven(stream, 'beforeSeek-back');
          stream.backPacket = 0;
        }
        if (stream.frontPacket) {
          if (!releaseSlot(stream, 'front', 'beforeSeek-front')) throw ownershipUnproven(stream, 'beforeSeek-front');
          stream.frontPacket = 0;
        }
        if (!releaseQueue(stream, 'beforeSeek-queue')) throw ownershipUnproven(stream, 'beforeSeek-queue');
      } else {
        stream.packetEnded = false;
        stream.timestampOffsetUpdated = false;
        if (stream.backPacket) {
          pool.release(stream.backPacket);
          stream.backPacket = 0;
        }
        if (stream.frontPacket) {
          pool.release(stream.frontPacket);
          stream.frontPacket = 0;
        }
        if (stream.pullQueue.queue.length) {
          stream.pullQueue.queue.forEach(function (packet) { pool.release(packet); });
        }
        stream.pullQueue.queue.length = 0;
      }
      if (isStrict(stream)) {
        stream.packetEnded = false;
        stream.timestampOffsetUpdated = false;
      }
      stream.pullQueue.ended = false;
      stream.pullQueue.index = 0;
      stream.pullQueue.lastPTS = BigInt(0);
      stream.pullQueue.lastDTS = BigInt(0);
      stream.pullQueue.diff = BigInt(0);
      stream.pullQueue.frameCount = native.noTimestamp;
      stream.lastDTS = native.noTimestamp;
    }

    async function afterSeek(task, host, seekTime, tickets) {
      if (isStrict(task.audio)) revoke(task.audio, 'afterSeek');
      if (isStrict(task.video)) revoke(task.video, 'afterSeek');
      var owned = tickets || beginTickets(task, 'afterSeek');
      var audioTicket = owned.audio;
      var videoTicket = owned.video;
      if ((isStrict(task.audio) && !audioTicket) || (isStrict(task.video) && !videoTicket)) {
        finishTickets(owned, 'afterSeek-begin');
        throw codedError('EIO_MUX_PULL_STALE');
      }
      var a = seekTime;
      var r = seekTime;
      var completed = false;
      var startedLoops = [];
      function startOwnedLoop(stream, ticket) {
        stream.loop.start();
        startedLoops.push({ stream: stream, loop: stream.loop, epoch: ticket ? ticket.gen : stream.eioPullEpoch });
      }
      function stopLoopsStartedByThisRun() {
        for (var index = 0; index < startedLoops.length; index += 1) {
          var started = startedLoops[index];
          if (started.stream.loop !== started.loop) continue;
          if (started.epoch !== started.stream.eioPullEpoch) continue;
          try { started.loop.stop(); } catch (ignored) {}
        }
      }
      try {
        var synced = await host.syncToKeyframe(task, owned);
        guard(owned, 'afterSeek-sync');
        if (synced === STALE) throw codedError('EIO_MUX_PULL_STALE');
        if (task.audio && task.audio.backPacket > 0) {
          guard(owned, 'afterSeek-audio-back');
          if (!synced || seekTime < BigInt(0)) {
            a = native.scale(readPts(task.audio.backPacket), task.audio.backPacket + 72, native.millisecondsTimeBase);
          }
          native.writeI32(task.audio.backPacket + 32, outputOf(task.audio).index);
          await host.eioAfterSeekWrite(task.audio, task.audio.backPacket);
          guard(owned, 'afterSeek-audio-write');
          if (audioTicket) releaseSlot(task.audio, 'back', 'afterSeek-back');
          else pool.release(task.audio.backPacket);
          task.audio.backPacket = 0;
        }
        if (task.video && task.video.backPacket > 0) {
          guard(owned, 'afterSeek-video-back');
          if (!synced || seekTime < BigInt(0)) {
            r = native.scale(readPts(task.video.backPacket), task.video.backPacket + 72, native.millisecondsTimeBase);
          }
          native.writeI32(task.video.backPacket + 32, outputOf(task.video).index);
          await host.eioAfterSeekWrite(task.video, task.video.backPacket);
          guard(owned, 'afterSeek-video-write');
          if (videoTicket) releaseSlot(task.video, 'back', 'afterSeek-back');
          else pool.release(task.video.backPacket);
          task.video.backPacket = 0;
        }
        var origin = native.maxBig(a, r);
        var audioFilled = !task.audio;
        var videoFilled = !task.video;
        while (!audioFilled || !videoFilled) {
          if (!audioFilled) {
            if (task.audio.packetEnded) {
              audioFilled = true;
            } else {
              var audioPacket = await host.pullAVPacket(task.audio, task, audioTicket);
              if (audioPacket === STALE) throw codedError('EIO_MUX_PULL_STALE');
              if (audioPacket < 0) {
                requireEof(audioTicket, audioPacket, 'afterSeek-audio');
                task.audio.packetEnded = true;
                task.audio.backPacket = 0;
                audioFilled = true;
              } else if (native.scale(readPts(audioPacket), audioPacket + 72, native.millisecondsTimeBase) >=
                  origin + task.cacheDuration) {
                audioFilled = true;
                if (audioTicket) {
                  if (!transferSlot(audioTicket, audioPacket, 'back')) throw codedError('EIO_MUX_PULL_TRANSFER_FAILED');
                } else task.audio.backPacket = audioPacket;
              } else {
                guard(owned, 'afterSeek-audio-fill');
                if (audioTicket) {
                  if (!transferSlot(audioTicket, audioPacket, 'back')) throw codedError('EIO_MUX_PULL_TRANSFER_FAILED');
                } else task.audio.backPacket = audioPacket;
                native.writeI32(task.audio.backPacket + 32, outputOf(task.audio).index);
                await host.eioAfterSeekWrite(task.audio, task.audio.backPacket);
                if (!task.audio.enableRawMpeg) await host.eioAfterSeekFlush(task.audio);
                guard(owned, 'afterSeek-audio-flush');
                host.eioAddQueue(task.audio.track, task.audio);
                if (audioTicket) releaseSlot(task.audio, 'back', 'afterSeek-audio');
                else pool.release(task.audio.backPacket);
                task.audio.backPacket = 0;
              }
            }
          }
          if (!videoFilled) {
            if (task.video.packetEnded) {
              videoFilled = true;
            } else {
              var videoPacket = await host.pullAVPacket(task.video, task, videoTicket);
              if (videoPacket === STALE) throw codedError('EIO_MUX_PULL_STALE');
              if (videoPacket < 0) {
                requireEof(videoTicket, videoPacket, 'afterSeek-video');
                task.video.packetEnded = true;
                task.video.backPacket = 0;
                videoFilled = true;
              } else if (native.scale(readPts(videoPacket), videoPacket + 72, native.millisecondsTimeBase) >=
                  origin + task.cacheDuration) {
                videoFilled = true;
                if (videoTicket) {
                  if (!transferSlot(videoTicket, videoPacket, 'back')) throw codedError('EIO_MUX_PULL_TRANSFER_FAILED');
                } else task.video.backPacket = videoPacket;
              } else {
                guard(owned, 'afterSeek-video-fill');
                if (videoTicket) {
                  if (!transferSlot(videoTicket, videoPacket, 'back')) throw codedError('EIO_MUX_PULL_TRANSFER_FAILED');
                } else task.video.backPacket = videoPacket;
                native.writeI32(task.video.backPacket + 32, outputOf(task.video).index);
                await host.eioAfterSeekWrite(task.video, task.video.backPacket);
                if (!task.video.enableRawMpeg) await host.eioAfterSeekFlush(task.video);
                guard(owned, 'afterSeek-video-flush');
                host.eioAddQueue(task.video.track, task.video);
                if (videoTicket) releaseSlot(task.video, 'back', 'afterSeek-video');
                else pool.release(task.video.backPacket);
                task.video.backPacket = 0;
              }
            }
          }
        }
        var callbacks = [];
        if (task.audio) {
          if (!task.audio.enableRawMpeg) await host.eioAfterSeekFlush(task.audio);
          guard(owned, 'afterSeek-audio-flush-all');
          callbacks.push(new Promise(function (resolve) {
            host.eioAddQueue(task.audio.track, task.audio, function () { resolve(); });
          }));
        }
        if (task.video) {
          if (!task.video.enableRawMpeg) await host.eioAfterSeekFlush(task.video);
          guard(owned, 'afterSeek-video-flush-all');
          callbacks.push(new Promise(function (resolve) {
            host.eioAddQueue(task.video.track, task.video, function () { resolve(); });
          }));
        }
        await Promise.all(callbacks);
        guard(owned, 'afterSeek-callbacks');
        if (task.audio && !task.audio.packetEnded) {
          if (task.audio.backPacket <= 0) {
            var audioBack = await host.pullAVPacket(task.audio, task, audioTicket);
            if (audioBack === STALE) throw codedError('EIO_MUX_PULL_STALE');
            if (audioBack < 0) {
              requireEof(audioTicket, audioBack, 'afterSeek-audio-back');
              task.audio.packetEnded = true;
              task.audio.backPacket = 0;
            } else if (audioTicket) {
              if (!transferSlot(audioTicket, audioBack, 'back')) throw codedError('EIO_MUX_PULL_TRANSFER_FAILED');
            } else task.audio.backPacket = audioBack;
          }
          if (!task.audio.packetEnded) {
            var audioFront = await host.pullAVPacket(task.audio, task, audioTicket);
            if (audioFront === STALE) throw codedError('EIO_MUX_PULL_STALE');
            if (audioFront < 0) {
              requireEof(audioTicket, audioFront, 'afterSeek-audio-front');
              task.audio.frontPacket = 0;
              task.audio.packetEnded = true;
              task.audio.frontBuffered = false;
            } else if (audioTicket) {
              if (!transferSlot(audioTicket, audioFront, 'front')) throw codedError('EIO_MUX_PULL_TRANSFER_FAILED');
              task.audio.packetEnded = false;
              task.audio.frontBuffered = true;
            } else {
              task.audio.frontPacket = audioFront;
              task.audio.packetEnded = false;
              task.audio.frontBuffered = true;
            }
            guard(owned, 'afterSeek-audio-final');
            task.audio.startTimestamp = BigInt(Math.floor(deps.now())) -
              (a + task.cacheDuration + native.scaleInt(task.audio.startPTS,
                outputOf(task.audio).timeBase, native.millisecondsTimeBase)) * BigInt(100) / task.playRate;
            if (task.pausing) task.pauseTimestamp = deps.now();
            else startOwnedLoop(task.audio, audioTicket);
          }
        }
        if (task.video && !task.video.packetEnded) {
          if (task.video.backPacket <= 0) {
            var videoBack = await host.pullAVPacket(task.video, task, videoTicket);
            if (videoBack === STALE) throw codedError('EIO_MUX_PULL_STALE');
            if (videoBack < 0) {
              requireEof(videoTicket, videoBack, 'afterSeek-video-back');
              task.video.packetEnded = true;
              task.video.backPacket = 0;
            } else if (videoTicket) {
              if (!transferSlot(videoTicket, videoBack, 'back')) throw codedError('EIO_MUX_PULL_TRANSFER_FAILED');
            } else task.video.backPacket = videoBack;
          }
          if (!task.video.packetEnded) {
            var videoFront = await host.pullAVPacket(task.video, task, videoTicket);
            if (videoFront === STALE) throw codedError('EIO_MUX_PULL_STALE');
            if (videoFront < 0) {
              requireEof(videoTicket, videoFront, 'afterSeek-video-front');
              task.video.packetEnded = true;
              task.video.frontBuffered = false;
              task.video.frontPacket = 0;
            } else if (videoTicket) {
              if (!transferSlot(videoTicket, videoFront, 'front')) throw codedError('EIO_MUX_PULL_TRANSFER_FAILED');
              task.video.packetEnded = false;
              task.video.frontBuffered = true;
            } else {
              task.video.frontPacket = videoFront;
              task.video.packetEnded = false;
              task.video.frontBuffered = true;
            }
            guard(owned, 'afterSeek-video-final');
            task.video.startTimestamp = BigInt(Math.floor(deps.now())) -
              (r + task.cacheDuration + native.scaleInt(task.video.startPTS,
                outputOf(task.video).timeBase, native.millisecondsTimeBase)) * BigInt(100) / task.playRate;
            if (task.pausing) task.pauseTimestamp = deps.now();
            else startOwnedLoop(task.video, videoTicket);
          }
        }
        await Promise.all([
          new Promise(function (resolve) {
            if (task.audio) task.audio.track.insertEnqueueCallback(resolve); else resolve();
          }),
          new Promise(function (resolve) {
            if (task.video) task.video.track.insertEnqueueCallback(resolve); else resolve();
          })
        ]);
        guard(owned, 'afterSeek-insert');
        var start = 0;
        var end = 0;
        if (task.audio) {
          if (task.video) {
            start = Math.max(task.audio.track.getBufferedStart(), task.video.track.getBufferedStart());
            end = Math.min(task.audio.track.getBufferedEnd(), task.video.track.getBufferedEnd());
          } else {
            start = task.audio.track.getBufferedStart();
            end = task.audio.track.getBufferedEnd();
          }
        } else if (task.video) {
          start = task.video.track.getBufferedStart();
          end = task.video.track.getBufferedEnd();
        }
        var value = host.seekSeconds(origin);
        if (value < start || value > end) value = Math.abs(value - start) > Math.abs(value - end) ? end : start;
        task.currentTimeNTP = deps.now();
        task.currentTime = value;
        task.seeking = false;
        host.logSeekDone(value, task.taskId);
        completed = true;
        return value;
      } catch (error) {
        stopLoopsStartedByThisRun();
        throw error;
      } finally {
        if (completed) complete(owned, 'afterSeek');
        else finishTickets(owned, 'afterSeek');
      }
    }

    async function sourceOpen(task, host, startTime) {
      var tickets = beginTickets(task, 'sourceOpen');
      var completed = false;
      try {
        if ((isStrict(task.audio) && !tickets.audio) || (isStrict(task.video) && !tickets.video)) {
          throw codedError('EIO_MUX_PULL_STALE');
        }
        await host.syncToKeyframe(task, tickets);
        guard(tickets, 'sourceOpen-sync');
        task.eioSourceOpenStage = 'synced';
        var u = startTime;
        var p = startTime;
        if (task.audio && !task.audio.backPacket && !task.audio.packetEnded) {
          var audioBack = await host.pullAVPacket(task.audio, task, tickets.audio);
          if (audioBack === STALE) throw codedError('EIO_MUX_PULL_STALE');
          if (audioBack < 0) {
            requireEof(tickets.audio, audioBack, 'sourceOpen-audio-initial');
            task.audio.packetEnded = true; task.audio.backPacket = 0;
          } else if (tickets.audio) {
            if (!transferSlot(tickets.audio, audioBack, 'back')) throw codedError('EIO_MUX_PULL_TRANSFER_FAILED');
          } else task.audio.backPacket = audioBack;
        }
        if (task.video && !task.video.backPacket && task.video.packetEnded) {
          var videoBack = await host.pullAVPacket(task.video, task, tickets.video);
          if (videoBack === STALE) throw codedError('EIO_MUX_PULL_STALE');
          if (videoBack < 0) {
            requireEof(tickets.video, videoBack, 'sourceOpen-video-initial');
            task.video.packetEnded = true; task.video.backPacket = 0;
          } else if (tickets.video) {
            if (!transferSlot(tickets.video, videoBack, 'back')) throw codedError('EIO_MUX_PULL_TRANSFER_FAILED');
          } else task.video.backPacket = videoBack;
        }
        guard(tickets, 'sourceOpen-initial-pulls');
        if (task.audio && task.audio.backPacket > 0) {
          u = native.maxBig(u, native.scale(readDts(task.audio.backPacket), task.audio.backPacket + 72,
            native.millisecondsTimeBase));
        }
        if (task.video && task.video.backPacket > 0) {
          p = native.maxBig(p, native.scale(readDts(task.video.backPacket), task.video.backPacket + 72,
            native.millisecondsTimeBase));
        }
        if (task.audio && task.video && native.diffBig(u, p) > BigInt(1000)) {
          if (p > u) {
            var shift = native.scaleInt(u, native.millisecondsTimeBase,
              native.videoTimeBase(task.video.backPacket));
            native.writeBigInt(task.video.backPacket + 48, readDts(task.video.backPacket) - shift);
            native.writeBigInt(task.video.backPacket + 16, shift);
            native.writeBigInt(task.video.backPacket + 8, shift);
            p = u;
          } else if (u > p && 86018 === outputOf(task.audio).codecpar.codecId &&
              2 === outputOf(task.audio).codecpar.profile &&
              outputOf(task.audio).codecpar.chLayout.nbChannels < 3) {
            var synthetic = pool.alloc();
            native.copyPacket(synthetic, task.audio.backPacket);
            if (tickets.audio) {
              if (!register(tickets.audio, synthetic, 'synthetic')) throw codedError('EIO_MUX_PULL_OWNERSHIP_CONFLICT');
            }
            var channels = outputOf(task.audio).codecpar.chLayout.nbChannels;
            var payload = 1 === channels ? new Uint8Array([0, 200, 0, 128, 35, 128]) :
              new Uint8Array([33, 0, 73, 144, 2, 25, 0, 35, 128]);
            var bytes = native.allocBytes(payload.length);
            native.writeBytes(bytes, payload.length, payload);
            native.setPacketData(synthetic, bytes, payload.length);
            native.writeBigInt(synthetic + 16, native.scaleInt(p, native.millisecondsTimeBase,
              native.videoTimeBase(synthetic)));
            native.writeBigInt(synthetic + 8, native.scaleInt(p, native.millisecondsTimeBase,
              native.videoTimeBase(synthetic)));
            var frameDuration = BigInt(1024e3) / BigInt(0 | outputOf(task.audio).codecpar.sampleRate);
            var frames = Number(BigInt.asIntN(32, (u - p) / frameDuration));
            var nextTime = readDts(synthetic) + native.scaleInt(frameDuration,
              native.millisecondsTimeBase, native.videoTimeBase(task.audio.backPacket));
            for (var index = 0; index < frames - 1; index += 1) {
              var filler = pool.alloc();
              native.copyPacket(filler, synthetic);
              native.writeBigInt(filler + 16, nextTime);
              native.writeBigInt(filler + 8, nextTime);
              if (tickets.audio) {
                if (!register(tickets.audio, filler, 'synthetic')) throw codedError('EIO_MUX_PULL_OWNERSHIP_CONFLICT');
                if (!transferQueue(tickets.audio, filler, task.audio.pullQueue.queue)) {
                  throw codedError('EIO_MUX_PULL_OWNERSHIP_CONFLICT');
                }
              } else task.audio.pullQueue.queue.push(filler);
              nextTime += native.scaleInt(frameDuration, native.millisecondsTimeBase,
                native.videoTimeBase(task.audio.backPacket));
            }
            if (tickets.audio) {
              if (!moveSlotToQueue(task.audio, 'back', task.audio.pullQueue.queue)) {
                throw codedError('EIO_MUX_PULL_TRANSFER_FAILED');
              }
              if (!transferSlot(tickets.audio, synthetic, 'back')) {
                throw codedError('EIO_MUX_PULL_TRANSFER_FAILED');
              }
            } else {
              task.audio.pullQueue.queue.push(task.audio.backPacket);
              task.audio.backPacket = synthetic;
            }
            u = p;
          }
        }
        guard(tickets, 'sourceOpen-sync-block');
        var writes = [];
        if (task.audio) {
          task.audio.track.setSourceBuffer(host.createSourceBuffer(task.mediaSource,
            outputOf(task.audio).codecpar[native.timeBaseIndex]));
          if (!task.audio.enableRawMpeg) host.eioHeaderOperation(task.audio);
          writes.push(host.startMux(task.audio, task, native.maxBig(u, p), tickets.audio));
        }
        if (task.video) {
          task.video.track.setSourceBuffer(host.createSourceBuffer(task.mediaSource,
            outputOf(task.video).codecpar[native.timeBaseIndex]));
          if (!task.video.enableRawMpeg) host.eioHeaderOperation(task.video);
          writes.push(host.startMux(task.video, task, native.maxBig(u, p), tickets.video));
        }
        await Promise.all(writes);
        guard(tickets, 'sourceOpen-startMux');
        var audioStarted = false;
        var videoStarted = false;
        if (task.audio) {
          var ab = await host.pullAVPacket(task.audio, task, tickets.audio);
          if (ab === STALE) throw codedError('EIO_MUX_PULL_STALE');
          if (ab < 0) requireEof(tickets.audio, ab, 'sourceOpen-audio-back');
          if (ab > 0) {
            if (tickets.audio) {
              if (!transferSlot(tickets.audio, ab, 'back')) throw codedError('EIO_MUX_PULL_TRANSFER_FAILED');
            } else task.audio.backPacket = ab;
            audioStarted = true;
            var af = await host.pullAVPacket(task.audio, task, tickets.audio);
            if (af === STALE) throw codedError('EIO_MUX_PULL_STALE');
            if (af < 0) requireEof(tickets.audio, af, 'sourceOpen-audio-front');
            if (af > 0) {
              if (tickets.audio) {
                if (!transferSlot(tickets.audio, af, 'front')) throw codedError('EIO_MUX_PULL_TRANSFER_FAILED');
              } else task.audio.frontPacket = af;
              task.audio.frontBuffered = true;
            } else {
              task.audio.frontPacket = 0;
              task.audio.packetEnded = true;
            }
          } else task.audio.packetEnded = true;
          if (!task.audio.enableRawMpeg) host.eioMuxFlushProbe(task.audio);
          host.eioAddQueue(task.audio.track, task.audio);
        }
        if (task.video) {
          var vb = await host.pullAVPacket(task.video, task, tickets.video);
          if (vb === STALE) throw codedError('EIO_MUX_PULL_STALE');
          if (vb < 0) requireEof(tickets.video, vb, 'sourceOpen-video-back');
          if (vb > 0) {
            if (tickets.video) {
              if (!transferSlot(tickets.video, vb, 'back')) throw codedError('EIO_MUX_PULL_TRANSFER_FAILED');
            } else task.video.backPacket = vb;
            videoStarted = true;
            var vf = await host.pullAVPacket(task.video, task, tickets.video);
            if (vf === STALE) throw codedError('EIO_MUX_PULL_STALE');
            if (vf < 0) requireEof(tickets.video, vf, 'sourceOpen-video-front');
            if (vf > 0) {
              if (tickets.video) {
                if (!transferSlot(tickets.video, vf, 'front')) throw codedError('EIO_MUX_PULL_TRANSFER_FAILED');
              } else task.video.frontPacket = vf;
              task.video.frontBuffered = true;
            } else {
              task.video.frontPacket = 0;
              task.video.packetEnded = true;
            }
          } else task.video.packetEnded = true;
          if (!task.video.enableRawMpeg) host.eioMuxFlushProbe(task.video);
          host.eioAddQueue(task.video.track, task.video);
        }
        guard(tickets, 'sourceOpen-post-pull');
        if (task.audio) {
          if (audioStarted) { host.createLoop(task.audio, task); task.audio.loop.start(); }
          else task.audio.track.end();
        }
        if (task.video) {
          if (videoStarted) { host.createLoop(task.video, task); task.video.loop.start(); }
          else task.video.track.end();
        }
        await native.delay(100);
        guard(tickets, 'sourceOpen-delay');
        var bufferedStart = 0;
        if (task.audio) {
          bufferedStart = task.video ? Math.max(task.audio.track.getBufferedStart(),
            task.video.track.getBufferedStart()) : task.audio.track.getBufferedStart();
        } else if (task.video) bufferedStart = task.video.track.getBufferedStart();
        task.currentTimeNTP = deps.now();
        if (startTime > BigInt(0)) {
          task.currentTime = host.seekSeconds(startTime);
          task.controlIPCPort.notify('seek', { time: task.currentTime });
        } else if (host.timeNoticeNeeded(bufferedStart, task)) {
          task.currentTime = bufferedStart;
          task.currentTimeNTP = deps.now();
          task.controlIPCPort.notify('seek', { time: bufferedStart });
        }
        task.eioSourceOpenStage = 'ready';
        completed = true;
      } catch (error) {
        task.eioSourceOpenError = {
          planDetail: error && undefined !== error.planDetail ? error.planDetail : null,
          planReason: error && error.planReason || null,
          code: error && error.code || null,
          message: String(error && error.message || error)
            .replace(/https?:\/\/\S+/g, '[redacted-url]').slice(0, 240)
        };
        throw error;
      } finally {
        if (completed) complete(tickets, 'sourceOpen');
        else finishTickets(tickets, 'sourceOpen');
      }
    }

    async function startMux(stream, task, host, startTime, ticket) {
      if (!isStrict(stream)) return host.eioStartMuxBaseline(stream, task, startTime);
      var own = false;
      if (!ticket) {
        ticket = begin(stream, 'startMux');
        own = true;
      }
      if (!ticket) throw codedError('EIO_MUX_PULL_STALE');
      var completed = false;
      try {
        var packet = 0;
        var firstDts = native.noTimestamp;
        var firstPts = native.noTimestamp;
        var lastDts = BigInt(0);
        var duration = task.cacheDuration;
        var timeBase = outputOf(stream).timeBase;
        if (stream.backPacket > 0) {
          if (stale(ticket)) throw codedError('EIO_MUX_PULL_STALE');
          firstDts = readDts(stream.backPacket);
          firstPts = readPts(stream.backPacket);
          if (startTime > firstPts) duration += startTime - native.scaleInt(firstDts, timeBase, native.millisecondsTimeBase);
          native.writeI32(stream.backPacket + 32, outputOf(stream).index);
          host.eioProbeWrite(stream, stream.backPacket);
          if (stale(ticket)) throw codedError('EIO_MUX_PULL_STALE');
          if (!releaseSlot(stream, 'back', 'startMux-back')) throw ownershipUnproven(stream, 'startMux-back');
          stream.backPacket = 0;
        }
        while (firstDts === native.noTimestamp ||
            native.scaleInt(lastDts - firstDts, timeBase, native.millisecondsTimeBase) < duration) {
          packet = await host.pullAVPacket(stream, task, ticket);
          if (packet === STALE) throw codedError('EIO_MUX_PULL_STALE');
          if (packet < 0) {
            requireEof(ticket, packet, 'startMux');
            if (!stale(ticket)) stream.packetEnded = true;
            break;
          }
          if (stale(ticket)) throw codedError('EIO_MUX_PULL_STALE');
          lastDts = readDts(packet);
          if (firstDts === native.noTimestamp) { firstDts = lastDts; firstPts = readPts(packet); }
          if (readPts(packet) < firstPts) firstPts = readPts(packet);
          native.writeI32(packet + 32, outputOf(stream).index);
          host.eioProbeWrite(stream, packet);
          if (stale(ticket)) throw codedError('EIO_MUX_PULL_STALE');
          if (!releasePulled(ticket, packet, 'startMux-packet')) {
            throw codedError('EIO_MUX_PULL_RELEASE_FAILED');
          }
        }
        if (stale(ticket)) throw codedError('EIO_MUX_PULL_STALE');
        if (firstPts !== native.noTimestamp && stream.startPTS <= BigInt(0)) stream.startPTS = firstPts;
        stream.pullQueue.diff = BigInt(0);
        task.currentTimeNTP = deps.now();
        task.currentTime = 0;
        stream.startTimestamp = BigInt(Math.floor(deps.now())) -
          native.scaleInt(lastDts, timeBase, native.millisecondsTimeBase) * BigInt(100) / task.playRate;
        completed = true;
      } finally {
        if (own) {
          if (completed) complete(ticket, 'startMux');
          else reclaim(ticket, 'startMux');
        }
      }
    }

    async function unregister(task, host) {
      var streams = [];
      if (isStrict(task.audio)) streams.push(task.audio);
      if (isStrict(task.video)) streams.push(task.video);
      if (task.audio) {
        host.eioCancelWake(task.audio);
        host.eioAbortStaleOperation(task.audio);
        task.audio.eioReleased = true;
      }
      if (task.video) {
        host.eioCancelWake(task.video);
        host.eioAbortStaleOperation(task.video);
        task.video.eioReleased = true;
      }
      for (var index = 0; index < streams.length; index += 1) revoke(streams[index], 'unregister');
      if (task.audio && task.audio.loop && typeof task.audio.loop.stop === 'function') task.audio.loop.stop();
      if (task.video && task.video.loop && typeof task.video.loop.stop === 'function') task.video.loop.stop();
      var drained = await drain(streams, DEFAULT_DRAIN_MS);
      if (!drained.ok) {
        task.eioPullTeardownUnsettled = { code: 'EIO_MUX_PULL_TEARDOWN_UNSETTLED', pending: drained.pending };
        var unsettled = codedError('EIO_MUX_PULL_TEARDOWN_UNSETTLED');
        unsettled.planReason = 'pull-pending';
        unsettled.pending = drained.pending;
        throw unsettled;
      }
      task.eioPullTeardownUnsettled = null;
      for (var teardownIndex = 0; teardownIndex < streams.length; teardownIndex += 1) {
        var proof = prepareTeardown(streams[teardownIndex], 'unregister');
        if (!proof.ok) {
          task.eioPullTeardownUnsettled = {
            code: 'EIO_MUX_PULL_TEARDOWN_UNSETTLED',
            planReason: proof.planReason,
            cause: proof.code,
            kind: proof.kind,
            slot: proof.slot,
            pending: proof.pending
          };
          var unproven = codedError('EIO_MUX_PULL_TEARDOWN_UNSETTLED');
          unproven.planReason = proof.planReason || 'ownership-unproven';
          unproven.cause = proof.code;
          unproven.pending = proof.pending;
          throw unproven;
        }
      }
      for (var poolIndex = 0; poolIndex < streams.length; poolIndex += 1) closePool(streams[poolIndex]);
      if (task.audio && task.audio.loop) {
        await task.audio.oformatContext.destroy();
        task.audio.loop.destroy();
      }
      if (task.video && task.video.loop) {
        await task.video.oformatContext.destroy();
        task.video.loop.destroy();
      }
      if (task.audio) await teardownStream(task, task.audio, host);
      if (task.video) await teardownStream(task, task.video, host);
      if (task.controlIPCPort) task.controlIPCPort.destroy();
      host.tasks.delete(task.taskId);
    }

    async function teardownStream(task, stream, host) {
      if (stream.pullIPC) stream.pullIPC.destroy();
      if (!isStrict(stream) && stream.pullQueue.queue.length) {
        stream.pullQueue.queue.forEach(function (packet) { pool.release(packet); });
        stream.pullQueue.queue.length = 0;
      }
      if (stream.codecpar) native.releaseCodecpar(stream.codecpar);
      host.eioReleaseStream(stream);
    }

    function resetResource(stream, task) {
      if (!isStrict(stream)) return false;
      revoke(stream, 'resetResource');
      releaseSlot(stream, 'front', 'resetResource-front');
      releaseSlot(stream, 'back', 'resetResource-back');
      releaseQueue(stream, 'resetResource-queue');
      stream.frontPacket = 0;
      stream.backPacket = 0;
      return true;
    }

    return Object.freeze({
      VERSION: VERSION,
      KIND: KIND,
      STALE: STALE,
      DEFAULT_DRAIN_MS: DEFAULT_DRAIN_MS,
      isStrict: isStrict,
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
      releasePulled: releasePulled,
      complete: complete,
      reclaim: reclaim,
      drain: drain,
      closePool: closePool,
      fail: fail,
      finishTickets: finishTickets,
      guard: guard,
      swap: swap,
      pullAVPacket: pullAVPacket,
      syncToKeyframe: syncToKeyframe,
      beforeSeek: beforeSeek,
      afterSeek: afterSeek,
      startMux: startMux,
      sourceOpen: sourceOpen,
      unregister: unregister,
      resetResource: resetResource
    });
  }

  return Object.freeze({
    VERSION: VERSION,
    KIND: KIND,
    STALE: STALE,
    DEFAULT_DRAIN_MS: DEFAULT_DRAIN_MS,
    createMsePullLifetime: createMsePullLifetime
  });
});
}
}]);
