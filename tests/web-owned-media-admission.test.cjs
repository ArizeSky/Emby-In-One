'use strict';
// Actual vendored methods + real ownership core. Pending reads/packet IDs here
// are controlled shims, NOT generated-Worker or media-format playback evidence.
const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const path=require('node:path');
const vm=require('node:vm');
const root=path.join(__dirname,'..');
const source=fs.readFileSync(path.join(root,'public/vendor/web/libmedia/avplayer.js'),'utf8');
const core=require('../web-src/player-shared-media-ownership.js');
const owned=require('../web-src/player-owned-media-resources.js');
function helper(){
  const b=source.indexOf('/*__EIO_DUAL_CACHE_DEMUX_BEGIN__*/');
  const e=source.indexOf('/*__EIO_DUAL_CACHE_DEMUX_END__*/',b);
  assert.ok(b>=0&&e>b);
  return vm.runInNewContext(source.slice(b,e)+';__eioDualDemux',{setTimeout,clearTimeout,Map,Set,Math,Number,isFinite,performance});
}
test('owned admission waits before acquiring, wakes on capacity release and abort, probe exhausts finitely',{timeout:2000},async()=>{
  const h=helper(),ctl=core.create({capacityBytes:100,maxEntries:16}),manager=owned.createOwnedResources(ctl);
  const task={eioLedger:{phase:2,generation:1},formatContext:{eioOwnedResources:manager},eioSharedController:ctl};
  const old=manager.acquire(80,'modeled-old-backing',1);assert.equal(old.ok,true);
  let settled=false;
  const waiting=h.acquireOwned(task,70,'modeled-next-binary').then(r=>(settled=true,r));
  await new Promise(r=>setTimeout(r,40));
  assert.equal(settled,false);assert.equal(ctl.snapshot().heldBytes,80);
  assert.equal(old.handle.release({referencesDropped:true}).ok,true);
  const admitted=await waiting;assert.equal(admitted.ok,true);assert.equal(ctl.snapshot().heldBytes,70);
  assert.equal(admitted.isCancelled(),false);
  const blocked=h.acquireOwned(task,50,'cancelled');
  task.eioLedger.phase=4;h.wakeBatch(task);
  assert.equal((await blocked).code,'EIO_OWNED_ADMISSION_CANCELLED');
  assert.equal(admitted.isCancelled(),true);assert.equal(ctl.snapshot().heldBytes,70);
  task.eioLedger.phase=1;
  assert.equal((await h.acquireOwned(task,50,'probe')).code,'EIO_PROBE_BUDGET_EXHAUSTED');
  assert.equal((await h.acquireOwned(task,101,'oversized')).code,'EIO_OWNED_ATOMIC_RESOURCE_TOO_LARGE');
  admitted.handle.release({referencesDropped:true});assert.equal(ctl.snapshot().heldBytes,0);
});
test('actual doDemux discards old-generation completions without fake EOF or error notification',{timeout:2000},async()=>{
  const b=source.indexOf('async doDemux(e,t){'),e=source.indexOf('doAttachedPicture(e){',b);
  assert.ok(b>=0&&e>b);
  for(const mode of ['abort','generation','real-error','baseline-error']){
    let resolveRead;const pending=new Promise(r=>resolveRead=r),freed=[],notifications=[];
    const task={eioLedger:mode==='baseline-error'?undefined:{phase:2,generation:1},formatContext:{},avpacketPool:{alloc:()=>123,release:p=>freed.push(p)},cacheRequests:new Map(),controlIPCPort:{notify:(...x)=>notifications.push(x)}};
    const pipeline=vm.runInNewContext('({'+source.slice(b,e)+'})',{E:{Cj:()=>pending},M:{z3(){}},$:''});
    const turn=pipeline.doDemux(task,10);
    if(mode==='abort')task.eioLedger.phase=4;
    if(mode==='generation')task.eioLedger.generation++;
    resolveRead(-2);
    assert.equal(await turn,mode==='abort'||mode==='generation'?-1048572:-2);
    assert.deepEqual(freed,[123]);
    assert.equal(notifications.length,mode==='real-error'||mode==='baseline-error'?1:0);
    assert.equal(task.demuxEnded,undefined,'read cancellation itself does not assert EOF');
  }
});
test('producer stop proof waits for tracked open/analysis and refuses unresolved ownership cleanup',{timeout:2000},async()=>{
  const b=source.indexOf('eioRunProducer(e,run){'),e=source.indexOf('async openStream(e,...args)',b);
  assert.ok(b>=0&&e>b);
  const pipeline=vm.runInNewContext('new (class{'+source.slice(b,e)+'})',{Set,Promise,setTimeout:(f,ms)=>setTimeout(f,ms===2000?20:ms),clearTimeout});
  const task={eioLedger:{phase:2,generation:1}};pipeline.tasks=new Map([['task',task]]);
  let finish,entered=false;
  const open=pipeline.eioRunProducer('task',()=>{entered=true;return new Promise(r=>finish=r)});
  await Promise.resolve();assert.equal(entered,true);assert.equal(task.eioProducerOperations.size,1);
  task.eioLedger.phase=4;
  await assert.rejects(pipeline.eioRequireProducersStopped(task),{code:'EIO_PRODUCER_STOP_UNVERIFIED'});
  assert.equal(task.eioDualStopAck.source,false);assert.equal(task.eioProducerOperations.size,1);
  finish(0);await open;await pipeline.eioRequireProducersStopped(task);
  assert.equal(task.eioDualStopAck.source,true);assert.equal(task.eioProducerOperations.size,0);
  let reopened=false;assert.equal(await pipeline.eioRunProducer('task',()=>{reopened=true}),-1048572);
  assert.equal(reopened,false);
});
