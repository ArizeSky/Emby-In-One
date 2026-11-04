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

function fixture(){
  const controller=ownership.create({capacityBytes:1024,maxEntries:16});
  const resources=owned.createOwnedResources(controller);
  const f={controller,cancel:false,reads:[]};
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
  f.fc={ioReader:{setEndian(){},readBuffer:async size=>{f.reads.push(size);return new Uint8Array(size);}},
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
