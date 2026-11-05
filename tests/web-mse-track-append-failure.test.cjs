'use strict';
const test=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm'),path=require('node:path');
const source=fs.readFileSync(path.join(__dirname,'../public/vendor/web/libmedia/26.avplayer.js'),'utf8');
function track(){
  const factories={};
  vm.runInNewContext(source,{self:{webpackChunkAVPlayer:{push:([,m])=>Object.assign(factories,m)}},DOMException});
  const req=id=>({19371:{A:(o,k,v)=>{o[k]=v}},23902:{X$:Object.assign},34117:{z3(){}},90214:require("../web-src/player-mse-append-batch.js")}[id]);
  req.d=(exports,defs)=>{for(const [key,get]of Object.entries(defs))Object.defineProperty(exports,key,{get})};
  const exports={};factories[2633]({},exports,req);return new exports.A();
}
function chunk(onDrop){const b=new Uint8Array(8);b.eioClaim={drop:onDrop};return b}
test('real Track drops an input rejected synchronously, retaining only scalar fault diagnostics',()=>{
  const t=track(),failure=new DOMException('detached','InvalidStateError');let dropped=0;
  t.setSourceBuffer({appendBuffer(){throw failure}});
  assert.throws(()=>t.addBuffer(chunk(()=>dropped++)),e=>e===failure);
  assert.equal(dropped,1);assert.equal(t.operatorQueue.length,0);assert.equal(t.eioInFlight,null);
  assert.equal(t.eioFaultedInput.buffer,undefined);
  t.destroy();assert.equal(dropped,1);
});
test('quota pressure retains the real queued claim until retry or actual queue teardown',()=>{
  const t=track();let dropped=0;
  t.setSourceBuffer({appendBuffer(){throw new DOMException('full','QuotaExceededError')}});
  t.addBuffer(chunk(()=>dropped++));assert.equal(dropped,0);assert.equal(t.operatorQueue.length,1);
  t.destroy();assert.equal(dropped,1);
});
test('real eioAddQueue retires undispatched chunks after append rejection, not previously accepted chunks',()=>{
  const start=source.indexOf('eioAddQueue(t,e,i){'),end=source.indexOf('eioDiscardQueue(t){',start);
  const host=vm.runInNewContext('({'+source.slice(start,end)+'})');host.eioEnd=()=>{};
  const dropped=[0,0,0],chunks=dropped.map((_,i)=>chunk(()=>dropped[i]++));
  let calls=0;
  const t=track();t.setSourceBuffer({appendBuffer(){if(++calls===1)throw new DOMException('closed','InvalidStateError')}});
  const e={eioOwned:{},bufferQueue:{flushChunks:()=>chunks}};
  assert.throws(()=>host.eioAddQueue(t,e),/closed/);
  assert.deepEqual(dropped,[1,1,1]);assert.deepEqual(chunks,[null,null,null]);
  t.destroy();assert.deepEqual(dropped,[1,1,1]);
});

test('strict Track teardown aborts the live append and clears every payload before its claim is dropped',()=>{
  const t=track(),released=[];
  const source={buffered:{length:0},appendBuffer(){},abort(){released.push('abort')}};
  t.setSourceBuffer(source);t.eioOwned={batchInput(){}};
  const first=chunk(()=>{assert.equal(inflight.buffer,null);released.push('first')});
  t.addBuffer(first);const inflight=t.eioInFlight;
  const second=chunk(()=>{assert.equal(queued.buffer,null);released.push('second')});
  t.addBuffer(second);const queued=t.operatorQueue[0];
  t.destroy();
  assert.deepEqual(released,['abort','first','second']);
  assert.equal(t.eioInFlight,null);assert.equal(source.onupdateend,null);assert.equal(t.sourceBuffer,null);
});
test('strict Track refuses teardown completion when native abort failed and its append is still live',()=>{
  const t=track();let released=0;
  const source={buffered:{length:0},appendBuffer(){},abort(){throw new Error('native abort failed')}};
  t.setSourceBuffer(source);t.eioOwned={batchInput(){}};t.addBuffer(chunk(()=>released++));
  assert.throws(()=>t.destroy(),{code:'EIO_MSE_APPEND_ABORT_UNVERIFIED'});
  assert.equal(released,0);assert.ok(t.eioInFlight);
  source.abort=()=>{};t.destroy();assert.equal(released,1);
});
test('strict quota recovery removes played data before retrying the blocked append, even below 20 seconds',()=>{
 const t=track(),events=[];let room=false,released=0,batches=0;
 const source={updating:false,buffered:{length:1,start:()=>0,end:()=>18},
  appendBuffer(){events.push('append');if(!room)throw new DOMException('full','QuotaExceededError');this.updating=true},
  remove(start,end){events.push(['remove',start,end]);room=true;this.updating=true},abort(){}};
 t.eioOwned={batchInput(){batches++}};t.setSourceBuffer(source);
 t.addBuffer(chunk(()=>released++));
 assert.equal(t.isPaused(),1);assert.equal(released,0);
 t.removeBuffer(18);
 assert.deepEqual(events,['append',['remove',0,8]]);
 source.updating=false;source.onupdateend();
 assert.deepEqual(events,['append',['remove',0,8],'append']);
 assert.equal(released,0,'the retried append owns the original input until completion');
 source.updating=false;source.onupdateend();assert.equal(released,1);
 assert.equal(t.eioQuotaExceeded,false);assert.equal(batches,1,'quota retries preserve the existing batch without copying it again');t.destroy();
});

test('strict Track reset clears the old cleanup cursor before a backward seek',()=>{
 const t=track();t.eioOwned={batchInput(){}};
 t.setSourceBuffer({buffered:{length:0},abort(){}});
 t.lastRemoveTime=220;t.eioQuotaExceeded=true;t.reset();
 assert.equal(t.lastRemoveTime,0);assert.equal(t.eioQuotaExceeded,false);
 const removals=[];t.sourceBuffer.remove=(start,end)=>removals.push([start,end]);
 t.eioQuotaExceeded=true;t.removeBuffer(18);
 assert.deepEqual(removals,[[0,8]],'the previous 240-second position cannot suppress cleanup at 18 seconds');
 t.destroy();
});
