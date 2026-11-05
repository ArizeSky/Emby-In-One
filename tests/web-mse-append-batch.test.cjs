'use strict';
const test=require('node:test'),assert=require('node:assert/strict');
const core=require('../web-src/player-shared-media-ownership.js');
const owned=require('../web-src/player-owned-media-resources.js');
const {batchInput}=require('../web-src/player-mse-append-batch.js');
function harness(capacityBytes){
  const controller=core.create({capacityBytes,maxEntries:64});
  const manager=owned.createOwnedResources(controller);
  function input(value,extra={}){
    const buffer=new Uint8Array(value),admitted=manager.acquire(buffer.length,'original',0);
    assert.equal(admitted.ok,true);
    let dropped=false;
    return {operator:0,buffer,eioClaim:{drop(){assert.equal(dropped,false);dropped=true;assert.equal(admitted.handle.release({referencesDropped:true}).ok,true)}},...extra};
  }
  return {controller,manager,input};
}
test('append batch charges its copy concurrently, drops originals, and releases only on append completion',()=>{
  const {controller,manager,input}=harness(100);
  const first=input([1,2]),second=input([3,4,5]),boundary={operator:1};
  const queue=[second,boundary,input([6])];
  assert.equal(batchInput(first,queue,manager,0),true);
  assert.deepEqual(Array.from(first.buffer),[1,2,3,4,5]);
  assert.equal(second.buffer,null);assert.equal(second.eioClaim,null);
  assert.deepEqual(queue.map(x=>x.operator),[1,0]);
  const snap=controller.snapshot();
  assert.equal(snap.peakBytes,11,'six original bytes plus five-byte copy coexist');
  assert.equal(snap.heldBytes,6,'five-byte pending append plus untouched final byte');
  first.buffer=null;first.eioClaim.drop();first.eioClaim.drop();
  assert.equal(controller.snapshot().heldBytes,1);
  queue[1].buffer=null;queue[1].eioClaim.drop();
  assert.equal(controller.snapshot().heldBytes,0);
});
test('capacity pressure preserves original queue; consumption creates room and the next batch resumes',()=>{
  const {controller,manager,input}=harness(12);
  const first=input([1,2,3,4]),queue=[input([5,6,7,8]),input([9,10])];
  assert.equal(batchInput(first,queue,manager,0),false);
  assert.equal(queue.length,2);assert.equal(controller.snapshot().heldBytes,10);
  first.buffer=null;first.eioClaim.drop();
  const next=queue.shift();
  assert.equal(batchInput(next,queue,manager,0),true);
  assert.deepEqual(Array.from(next.buffer),[5,6,7,8,9,10]);
  assert.equal(queue.length,0);assert.equal(controller.snapshot().heldBytes,6);
  next.buffer=null;next.eioClaim.drop();assert.equal(controller.snapshot().heldBytes,0);
});
test('append callbacks and timestamp changes retain their exact queue position',()=>{
  const {manager,input}=harness(100);
  for(const boundary of [input([7],{callback(){}}),{operator:2,timestampOffset:3}]){
    const first=input([1]),queue=[boundary,input([8])];
    assert.equal(batchInput(first,queue,manager,0),false);
    assert.equal(queue[0],boundary);assert.deepEqual(Array.from(first.buffer),[1]);
  }
});

test('strict loop backs off before planning and resumes the same packet after queue or budget pressure',()=>{
  const fs=require('node:fs'),vm=require('node:vm');
  const source=fs.readFileSync('public/vendor/web/libmedia/26.avplayer.js','utf8');
  const start=source.indexOf('createLoop(t,e){'),end=source.indexOf('async startMux(t,e)',start);
  let tick,queued=32,pressure=false,plans=0,writes=0,wakes=0,swaps=0,retries=0;
  const api=vm.runInNewContext('({'+source.slice(start,end)+'})',{
    EIO_MSE_APPEND_QUEUE_LIMIT:32,eioMseBufferedDuration:()=>0,eioMseBufferScale:()=>1,
    z:{A:class{constructor(run){tick=run}emptyTask(){}}},C:{A:()=>0},
    h:{Mr:()=>0n},s:{f:{17:()=>0n}},m:{Dh:-1n,i0:{}},c:{M:{15(){},17(){}}}
  });
  Object.assign(api,{eioRunLoopStep:(_,run)=>run(),eioPrepareOperation(){plans++;return pressure?null:{}},
    eioScheduleWake(){wakes++},writeAVPacket(){writes++},eioAddQueue(){},eioOperationSeal(){},swap(){swaps++}});
  const stream={eioOwned:{},backPacket:100,frontPacket:200,lastDTS:-1n,startTimestamp:0n,
    oformatContext:{streams:[{index:0,codecpar:{codecType:2}}]},
    track:{getBufferedDuration:()=>0,getQueueLength:()=>queued,isPaused:()=>queued>=32,enqueue:()=>{retries++}}};
  api.createLoop(stream,{maxBuffer:120,minBuffer:120,currentTime:0,currentTimeNTP:0,playRate:100n,targetRate:100n});
  tick();assert.equal(retries,1,"a full paused Track retries before producer backpressure");assert.equal(plans,0);assert.equal(writes,0);assert.equal(stream.backPacket,100);
  queued=0;pressure=true;tick();assert.equal(plans,1);assert.equal(writes,0);assert.equal(stream.backPacket,100);
  pressure=false;tick();assert.equal(writes,1);assert.equal(swaps,1);assert.equal(wakes,2);
});
test('strict fill targets use native time and retain the same seconds window at faster playback',()=>{
  const fs=require('node:fs'),vm=require('node:vm');
  const source=fs.readFileSync('public/vendor/web/libmedia/26.avplayer.js','utf8');
  const start=source.indexOf('function eioMseBufferTargets('),end=source.indexOf('function eioMseError(',start);
  let clock={known:true,mediaMs:42500};
  const api=vm.runInNewContext(source.slice(start,end)+';({eioMseBufferTargets,eioMseBufferScale,eioMseBufferedDuration})',{
    C:{A:()=>10000},eioReadNativePlaybackClock:()=>clock
  });
  const task={eioDualCache:{enabled:true,maxForwardSeconds:120},playRate:200n,currentTime:10,currentTimeNTP:0};
  assert.equal(api.eioMseBufferTargets(task).minBuffer,120);
  assert.equal(api.eioMseBufferTargets(task).maxBuffer,120);
  assert.equal(api.eioMseBufferScale(task),1);
  const stream={eioOwned:{},track:{getBufferedDuration:seconds=>seconds}};
  assert.equal(api.eioMseBufferedDuration(stream,task),42.5,'confirmed native time replaces wallclock extrapolation');
  delete task.eioDualCache;delete stream.eioOwned;
  assert.equal(api.eioMseBufferTargets(task).maxBuffer,4);
  assert.equal(api.eioMseBufferTargets(task).minBuffer,2);
  assert.equal(api.eioMseBufferScale(task),2);
  assert.equal(api.eioMseBufferedDuration(stream,task),20);
});
