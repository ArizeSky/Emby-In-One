'use strict';
const test=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm'),path=require('node:path');
const source=fs.readFileSync(path.join(__dirname,'../public/vendor/web/libmedia/26.avplayer.js'),'utf8');
function track(){
  const factories={};
  vm.runInNewContext(source,{self:{webpackChunkAVPlayer:{push:([,m])=>Object.assign(factories,m)}},DOMException});
  const req=id=>({19371:{A:(o,k,v)=>{o[k]=v}},23902:{X$:Object.assign},34117:{z3(){}}}[id]);
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
