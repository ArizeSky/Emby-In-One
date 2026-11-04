'use strict';
// Real shipped codec-slot, dOps, byte-order and repeated-moov factories.
const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const vm=require('node:vm');
const ownership=require('../web-src/player-shared-media-ownership.js');
const resources=require('../web-src/player-owned-media-resources.js');
function source(file,id){
  const text=fs.readFileSync('public/vendor/web/libmedia/'+file,'utf8');
  const start=text.indexOf(id+':');
  assert.ok(start>=0);
  const rest=text.slice(start),end=/},\d+:/.exec(rest);
  assert.ok(end);
  return rest.slice(String(id).length+1,end.index+1);
}
function compile(text){return vm.runInNewContext('('+text+')',{Uint8Array,DataView});}
let slotSource=source('avplayer.js',67669),pSource=source('943.avplayer.js',8732);
const slotFactory=compile(slotSource),pFactory=compile(pSource);
const parserFactory=compile(source('943.avplayer.js',35928));
const readerFactory=compile(source('avplayer.js',70704));
const writerFactory=compile(source('avplayer.js',99248));
const label='mp4-opus-dops-native';
// Asymmetric unsigned fields also cover negative output-gain bit preservation.
const dops=[0,2,0x12,0x34,0x01,0x02,0x03,0x04,0xfe,0xdc,0];
const head=[...Buffer.from('OpusHead'),1,2,0x34,0x12,0x04,0x03,0x02,0x01,0xdc,0xfe,0];
function fixture(){
  const bytes=new Uint8Array(65536),view=new DataView(bytes.buffer),reads={},writes={};
  for(const type of [8,15,20,25]){
    reads[type]=p=>view.getUint32(p,true);
    writes[type]=(p,v)=>view.setUint32(p,v,true);
  }
  reads[17]=p=>view.getBigInt64(p,true);writes[17]=(p,v)=>view.setBigInt64(p,v,true);
  let bump=4096;
  const f={bytes,allocs:[],frees:[],claims:[],readCount:0,skips:[],readSizes:[]};
  const controller=ownership.create({capacityBytes:1024,maxEntries:8});
  const owned=resources.createOwnedResources(controller);
  f.snapshot=()=>controller.snapshot();
  function free(p){f.frees.push(p);}
  const modules={
    80209:{JW:(p,n)=>{const b=bytes.subarray(p,p+n);if(f.sliceError)b.slice=()=>{throw Error('slice failed');};return b;},
      lW:(p,n,b)=>bytes.set(b,p),sO:(p,v,n)=>bytes.fill(v,p,p+n)},
    56238:{f:reads},6927:{M:writes},
    46626:{sY:n=>{if(f.mallocFail)return 0;const p=bump;bump+=n+16;f.allocs.push({p,n});return p;},
      Eb:free,aI:p=>{free(reads[20](p));writes[20](p,0);}},
    57216:{bl:()=>{}},46564:{NZ:()=>{}},29688:{N_:-1},
    34117:{z3:()=>{},R8:()=>{}},17689:{XC:()=>{}},54568:{o9:'pointer'},
    19371:{A:(o,k,v)=>{o[k]=v;}},18528:{},41662:{encode:s=>new TextEncoder().encode(s)}
  };
  function load(fn){
    const out={},req=id=>{assert.ok(modules[id],`module ${id}`);return modules[id];};
    req.d=(out,defs)=>{for(const key in defs)Object.defineProperty(out,key,{get:defs[key]});};
    fn({},out,req);return out;
  }
  function cp(pointer){
    const result={pointer};Object.defineProperties(result,{
      extradata:{get:()=>reads[20](pointer+12),set:v=>writes[20](pointer+12,v)},
      extradataSize:{get:()=>reads[15](pointer+16),set:v=>writes[15](pointer+16,v)}
    });return result;
  }
  f.cp=cp(256);f.slots=modules[67669]=load(slotFactory);
  modules[70704]=load(readerFactory);modules[99248]=load(writerFactory);
  const Writer=modules[99248].A;
  modules[99248]={A:class extends Writer{writeUint16(v){if(f.transformError)throw Error('transform failed');super.writeUint16(v);}}};
  f.parse=load(parserFactory).A;
  f.fc={eioAcquireOwnedResource:async(size,label)=>{
    f.claims.push({size,label});if(f.refuse)return {ok:false};
    return {...owned.acquire(size,label,7),generation:7,isCancelled:()=>!!f.cancelled};
  }};
  f.reader=(options={})=>{
    let pos=0n;
    return {getPos:()=>pos,skip:async n=>{f.skips.push(n);pos+=BigInt(n);},
      readUint32:async()=>{pos+=4n;return options.words.shift();},
      readBuffer:async(n,target)=>{
        f.readCount++;f.readSizes.push(n);if(options.io)await options.io();
        if(options.ioError)throw Error('IO failed');
        target.set((options.payload||dops).slice(1));pos+=BigInt(n);return target;
      }};
  };
  f.run=(options={})=>f.parse(f.reader(options),{codecpar:f.cp,sideData:options.sideData||{}},
    {size:options.size===undefined?11:options.size},{eioFormatContext:f.fc,foundMoov:!!options.temp});
  f.P=()=>{
    for(const id of [23716,8332,59436,23905,28320,3031,66293,52617,23902,87114,86299,35924,85626])modules[id]={};
    modules[37766]={A:tag=>tag==='trak'?1:2};modules[78215]={A:()=>({trackId:9})};
    modules[97238]={A:{123:f.parse}};
    const existing={index:0,codecpar:f.cp,privData:{trackId:9},sideData:{}};
    Object.assign(f.fc,{streams:[existing],streamIndex:1,
      createStream(){const s={index:1,codecpar:cp(512),sideData:{}};this.streams.push(s);return s;},
      removeStream(s){this.streams=this.streams.filter(item=>item!==s);}});
    const P=load(pFactory).wB;
    return ()=>P(f.reader({words:[27,1,19,123]}),f.fc,
      {eioFormatContext:f.fc,foundMoov:true,fragment:true},{size:27});
  };
  return f;
}
test('dOps: invalid size refuses before admission, malloc, skip or read',async()=>{
  for(const size of [0,10,-1,11.5,NaN,Number.MAX_SAFE_INTEGER,4294967288]){
    const f=fixture();await assert.rejects(f.run({size}),error=>String(error.code).startsWith('EIO_AVCC_'));
    assert.equal(f.claims.length,0);assert.equal(f.allocs.length,0);assert.equal(f.readCount,0);assert.deepEqual(f.skips,[]);
  }
});
test('dOps: admission refusal and missing strict hook precede malloc/read',async()=>{
  const f=fixture();f.refuse=true;await assert.rejects(f.run(),/admission refused/);
  assert.equal(f.allocs.length,0);assert.equal(f.readCount,0);assert.deepEqual(f.skips,[]);
  f.fc={eioBeforeSampleAlloc:()=>{}};await assert.rejects(f.run(),/hook missing/);
  assert.equal(f.allocs.length,0);assert.equal(f.readCount,0);
});
test('dOps: exact native OpusHead conversion and real terminal dn free',async()=>{
  const f=fixture();await f.run();const data=f.cp.extradata;
  assert.deepEqual(f.claims,[{size:19,label}]);assert.equal(f.allocs[0].n,19);
  assert.deepEqual(f.readSizes,[10]);assert.deepEqual([...f.bytes.subarray(data,data+19)],head);
  assert.equal(f.cp.codecId,86076);assert.equal(f.cp.extradataSize,19);assert.equal(f.snapshot().heldBytes,19);
  f.slots.dn(256);assert.deepEqual(f.frees,[data,256]);assert.equal(f.snapshot().heldBytes,0);
  assert.equal(f.snapshot().countOwnedResources,0);
});
test('dOps: IO, cancellation and transform failure discard new native claim, retain old',async()=>{
  const f=fixture();await f.run();const old=f.cp.extradata;
  for(const mode of ['io','cancel','transform']){
    f.transformError=mode==='transform';
    await assert.rejects(f.run({ioError:mode==='io',io:()=>{f.cancelled=mode==='cancel';}}),
      error=>error.code==='EIO_AVCC_NATIVE_FAILURE');
    assert.equal(f.cp.extradata,old);assert.equal(f.cp.extradataSize,19);
    assert.ok(!f.frees.includes(old));assert.equal(f.snapshot().heldBytes,19);
    f.cancelled=false;f.transformError=false;
  }
  assert.equal(f.frees.length,3);await f.run();assert.ok(f.frees.includes(old));
  f.slots.dn(256);assert.equal(f.snapshot().heldBytes,0);
});
test('dOps: failed malloc discards unused claim without reader movement',async()=>{
  const f=fixture();f.mallocFail=true;await assert.rejects(f.run(),/ALLOC_FAILED/);
  assert.equal(f.readCount,0);assert.deepEqual(f.skips,[]);assert.equal(f.snapshot().heldBytes,0);
});
test('dOps: foundMoov native temp is freed after JS copy and copy failure',async()=>{
  const f=fixture(),sideData={};await f.run({temp:true,sideData});
  assert.deepEqual([...sideData[1]],head);assert.equal(f.cp.extradata,0);
  assert.equal(f.frees.length,1);assert.equal(f.snapshot().heldBytes,0);
  f.sliceError=true;await assert.rejects(f.run({temp:true}),/slice failed/);
  assert.equal(f.frees.length,2);assert.equal(f.snapshot().heldBytes,0);
});
test('dOps: non-strict legal input retains legacy bytes and cleanup behavior',async()=>{
  const f=fixture();f.fc={};await f.run();const old=f.cp.extradata;
  assert.deepEqual([...f.bytes.subarray(old,old+19)],head);await f.run();assert.ok(f.frees.includes(old));
  f.slots.dn(256);assert.equal(f.snapshot().heldBytes,0);
});
test('dOps: real P replacement admits converted length and refuses without freeing old',async()=>{
  const f=fixture();await f.run();const old=f.cp.extradata,run=f.P();await run();
  const replacement=f.cp.extradata;assert.notEqual(replacement,old);assert.ok(f.frees.includes(old));
  assert.deepEqual(f.claims,[{size:19,label},{size:19,label},{size:19,label}]);
  assert.deepEqual([...f.bytes.subarray(replacement,replacement+19)],head);
  assert.equal(f.cp.extradataSize,19);assert.equal(f.snapshot().heldBytes,19);assert.equal(f.fc.streams.length,1);
  const acquire=f.fc.eioAcquireOwnedResource;let calls=0;
  f.fc.eioAcquireOwnedResource=(...args)=>++calls===2?{ok:false}:acquire(...args);
  await assert.rejects(run(),/admission refused/);
  assert.equal(f.cp.extradata,replacement);assert.ok(!f.frees.includes(replacement));
  assert.equal(f.snapshot().heldBytes,19);f.slots.dn(256);assert.equal(f.snapshot().heldBytes,0);
});
