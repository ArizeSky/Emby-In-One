'use strict';
const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const vm=require('node:vm');
const ownership=require('../web-src/player-shared-media-ownership.js');
const owned=require('../web-src/player-owned-media-resources.js');
const mediaHandles=require('../web-src/player-media-resource-handles.js');

const source=fs.readFileSync('public/vendor/web/libmedia/573.avplayer.js','utf8');
const start=source.indexOf('25248:');
const end=source.indexOf('},27228:',start);
assert.ok(start>=0&&end>start);
const factory=vm.runInNewContext('('+source.slice(start+'25248:'.length,end+1)+')');

function fixture(readGate){
  const controller=ownership.create({capacityBytes:1024,maxEntries:16});
  const resources=owned.createOwnedResources(controller);
  const f={controller,cancel:false,reads:[],gateUsed:false};
  const modules={
    19371:{A:(obj,key,value)=>{obj[key]=value;}},54568:{o9:'pointer'},
    29688:{N_:-1,Dh:-1n},34117:{z3:()=>{},R8:()=>{}},
    35734:{A:class{}},90213:mediaHandles
  };
  const load=fn=>{const out={};const req=id=>modules[id]||{};
    req.d=(target,defs)=>{for(const key in defs)Object.defineProperty(target,key,{get:defs[key]});};
    req.r=()=>{};fn({},out,req);return out;};
  const Format=load(factory).default;
  f.format=new Format();
  f.fc={ioReader:{setEndian(){},readBuffer:async size=>{
    f.reads.push(size);
    if(readGate&&!f.gateUsed){f.gateUsed=true;await readGate;}
    return new Uint8Array(size);
  }},
    eioOwnedResources:resources,
    eioAcquireOwnedResource:async(size,label)=>({
      ...resources.acquire(size,label,7),generation:7,isCancelled:()=>f.cancel
    })};
  f.format.init(f.fc);
  f.api=f.fc.eioMatroskaOwned;
  return f;
}

test('573 adapter adopts read handles and releases through common finalRelease',async()=>{
  const f=fixture();
  const read=await f.api.readBinary(16,'matroska-test');
  assert.equal(read.data.length,16);
  assert.equal(typeof read.handle.finalRelease,'function');
  assert.equal(f.controller.snapshot().heldBytes,16);
  assert.equal(f.api.release(read.handle,'test'),1);
  assert.equal(f.controller.snapshot().heldBytes,0);
  assert.equal(f.api.release(read.handle,'again'),0);
});

test('573 adapter cancellation and destroy release every adopted read',async()=>{
  const f=fixture();
  f.cancel=true;
  await assert.rejects(f.api.readBinary(8,'cancel'),
    error=>error.eioCancelled===true||error.code==='EIO_OWNED_ADMISSION_CANCELLED');
  assert.equal(f.controller.snapshot().heldBytes,0);
  f.cancel=false;
  await f.api.readBinary(8,'a');
  await f.api.readBinary(8,'b');
  assert.equal(f.controller.snapshot().heldBytes,16);
  assert.equal(f.api.releaseAll('destroy'),2);
  assert.equal(f.controller.snapshot().heldBytes,0);
  await f.api.readBinary(8,'c');
  assert.equal(f.controller.snapshot().heldBytes,8);
  await f.format.destroy(f.fc);
  assert.equal(f.controller.snapshot().heldBytes,0);
});

test('573 pending read holds one retain lease; releaseAll skips it until settled',
  {timeout:5000},async()=>{
    let releaseRead;
    const gate=new Promise(resolve=>{releaseRead=resolve;});
    const f=fixture(gate);
    const pending=f.api.readBinary(16,'pending');
    await new Promise(resolve=>setImmediate(resolve));
    let snap=f.api.snapshot();
    assert.equal(snap.liveCount,1);
    assert.equal(snap.liveBytes,16);
    assert.equal(snap.retainedLeases,1,'the in-flight read holds exactly one lease');
    assert.equal(f.api.releaseAll('destroy'),0,'releaseAll must not release a pending read');
    snap=f.api.snapshot();
    assert.equal(snap.liveCount,1);
    assert.equal(f.controller.snapshot().heldBytes,16);

    releaseRead();
    const read=await pending;
    assert.equal(read.data.length,16);
    snap=f.api.snapshot();
    assert.equal(snap.liveCount,1);
    assert.equal(snap.retainedLeases,0,'a settled read drops its lease');
    assert.equal(f.api.release(read.handle,'done'),1);
    snap=f.api.snapshot();
    assert.equal(snap.liveCount,0);
    assert.equal(f.controller.snapshot().heldBytes,0);
    assert.equal(snap.releaseFailedCount,0);
  });

test('573 explicit release wins over an outstanding lease (old pending/unproven semantics)',
  {timeout:5000},async()=>{
    let releaseRead;
    const gate=new Promise(resolve=>{releaseRead=resolve;});
    const f=fixture(gate);
    const pending=f.api.readBinary(16,'lease-release');
    await new Promise(resolve=>setImmediate(resolve));
    let snap=f.api.snapshot();
    assert.equal(snap.retainedLeases,1);

    let settled=null;
    pending.then(r=>{settled=r;},()=>{settled='rejected';});
    assert.equal(f.api.releaseAll('destroy'),0);
    releaseRead();
    await pending.catch(()=>{});
    await new Promise(resolve=>setImmediate(resolve));
    snap=f.api.snapshot();
    assert.equal(snap.liveCount,1,'releaseAll must not have released the leased read');

    const handle=settled&&settled.handle;
    assert.ok(handle,'the settled read still returns its handle');
    assert.equal(f.api.release(handle,'explicit'),1,'explicit release drops the lease and releases');
    assert.equal(f.controller.snapshot().heldBytes,0);
    snap=f.api.snapshot();
    assert.equal(snap.liveCount,0);
    assert.equal(snap.retainedLeases,0);
  });

test('573 failed read keeps the charge and the handle stays inspectable',async()=>{
  const f=fixture();
  f.fc.ioReader.readBuffer=async()=>{throw new Error('io failed');};
  await assert.rejects(f.api.readBinary(16,'io-fail'),/io failed/);
  assert.equal(f.controller.snapshot().heldBytes,16,'an unproven read keeps its charge');
  const snap=f.api.snapshot();
  assert.equal(snap.liveCount,1);
  assert.equal(snap.retainedLeases,1,'the unproven handle keeps its lease');
  assert.equal(f.api.releaseAll('destroy'),0);
  assert.equal(f.controller.snapshot().heldBytes,16);
  await f.format.destroy(f.fc);
  assert.equal(f.controller.snapshot().heldBytes,16,
    'destroy retains an unproven read instead of faking a release');
});

test('573 destroy releases settled reads and retains leased ones',async()=>{
  let releaseRead;
  const gate=new Promise(resolve=>{releaseRead=resolve;});
  const f=fixture(gate);
  const pending=f.api.readBinary(8,'pending');
  await new Promise(resolve=>setImmediate(resolve));
  const settled=await f.api.readBinary(8,'settled');
  assert.equal(f.controller.snapshot().heldBytes,16);
  await f.format.destroy(f.fc);
  assert.equal(f.controller.snapshot().heldBytes,8,'only the settled read was released');
  releaseRead();
  await pending.catch(()=>{});
  assert.equal(f.controller.snapshot().heldBytes,0,
    'a read settling after destroy releases through its own cancelled path');
  const snap=f.api.snapshot();
  assert.equal(snap.liveCount,0);
  assert.equal(snap.retainedLeases,0);
});
