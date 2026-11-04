'use strict';
// Real shipped factories, synthetic native memory/controller; no upstream or server.
const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const vm=require('node:vm');
const ownershipCore=require('../web-src/player-shared-media-ownership.js');
const ownedResources=require('../web-src/player-owned-media-resources.js');
const mediaHandles=require('../web-src/player-media-resource-handles.js');
const base='public/vendor/web/libmedia/';
function factory(file,id,next){
  const source=fs.readFileSync(base+file,'utf8');
  const start=source.indexOf(id+':');
  const end=source.indexOf('},'+next+':',start);
  assert.ok(start>=0&&end>start);
  return vm.runInNewContext('('+source.slice(start+String(id).length+1,end+1)+')');
}
const slotFactory=factory('avplayer.js',67669,67994);
const parserFactory=factory('943.avplayer.js',85369,85626);
const moovFactory=factory('943.avplayer.js',8732,9581);
test('avcc native destruction accepts the real shared owned-resource release result', async()=>{
  const f=fixture();
  const controller=ownershipCore.create({capacityBytes:1024,maxEntries:8});
  const resources=ownedResources.createOwnedResources(controller);
  f.fc.eioAcquireOwnedResource=async(size,label)=>({
    ...resources.acquire(size,label,7),generation:7,isCancelled:()=>false
  });
  await f.run();
  assert.equal(controller.snapshot().heldBytes,4);
  f.slots.dn(f.cp.pointer);
  assert.equal(controller.snapshot().heldBytes,0);
  assert.equal(controller.snapshot().countOwnedResources,0);
});
function fixture(){
  const buffer=new Uint8Array(65536),view=new DataView(buffer.buffer);
  const reads={},writes={};
  for(const type of [8,15,20,25]){
    reads[type]=p=>view.getUint32(p,true);
    writes[type]=(p,v)=>view.setUint32(p,v,true);
  }
  reads[17]=p=>view.getBigInt64(p,true);
  writes[17]=(p,v)=>view.setBigInt64(p,v,true);
  let bump=4096;
  const f={buffer,allocs:[],frees:[],claims:[],cancelled:false,failFree:false,failRelease:false};
  function free(p){
    if(f.failFree)throw Error('free failed');
    f.frees.push(p);
  }
  const modules={
    80209:{JW:(p,n)=>buffer.subarray(p,p+n),lW:(p,n,bytes)=>buffer.set(bytes,p),sO:(p,v,n)=>buffer.fill(v,p,p+n)},
    56238:{f:reads},6927:{M:writes},
    46626:{sY:n=>{const p=bump;bump+=n+16;f.allocs.push({p,n});return p;},Eb:free,
      aI:p=>{free(reads[20](p));writes[20](p,0);}},
    57216:{bl:()=>{}},46564:{NZ:()=>{}},29688:{N_:-1},
    34117:{z3:()=>{}},20642:{XC:()=>{}},54568:{o9:'pointer'},
    90213:mediaHandles
  };
  function load(fn){
    const out={};const req=id=>{assert.ok(modules[id],`module ${id}`);return modules[id];};
    req.d=(out,defs)=>{for(const key in defs)Object.defineProperty(out,key,{get:defs[key]});};
    fn({},out,req);return out;
  }
  f.load=load;f.modules=modules;
  f.slots=modules[67669]=load(slotFactory);
  f.parse=load(parserFactory).A;
  f.cp={pointer:256};
  Object.defineProperties(f.cp,{
    extradata:{get:()=>reads[20](268),set:v=>writes[20](268,v)},
    extradataSize:{get:()=>reads[15](272),set:v=>writes[15](272,v)}
  });
  f.fc={eioAcquireOwnedResource:async(size,label)=>{
    if(f.refuse)return {ok:false};
    const claim={size,label,releases:0};f.claims.push(claim);
    return {ok:true,generation:7,isCancelled:()=>f.cancelled,
      handle:{release:proof=>{assert.equal(proof.referencesDropped,true);claim.releases++;
        if(f.throwRelease)throw Error('release failed');
        return {ok:!f.failRelease};}}};
  }};
  f.run=async(options={})=>{
    let pos=0n;
    const reader={getPos:()=>pos,skip:async n=>{pos+=BigInt(n);},readBuffer:async(n,target)=>{
      if(options.io)await options.io();
      if(options.ioError)throw Error('IO failed');
      target.set([1,2,3,4]);pos+=BigInt(n);
      if(options.sliceError)target.slice=()=>{throw Error('slice failed');};
      return target;
    }};
    return f.parse(reader,{codecpar:f.cp,sideData:options.sideData||{}},{size:4},
      {eioFormatContext:f.fc,foundMoov:!!options.temp});
  };
  return f;
}
test('admission refusal and missing hook fail before native allocation',async()=>{
  const f=fixture();f.refuse=true;
  await assert.rejects(f.run(),/admission refused/);assert.equal(f.allocs.length,0);
  f.fc={eioBeforeSampleAlloc:()=>{}};
  await assert.rejects(f.run(),/hook missing/);assert.equal(f.allocs.length,0);
});
test('persistent exact native bytes survive until actual terminal free',async()=>{
  const f=fixture();await f.run();
  assert.equal(f.claims[0].size,4);assert.equal(f.claims[0].releases,0);
  assert.equal(f.cp.extradataSize,4);
  assert.deepEqual([...f.buffer.subarray(f.cp.extradata,f.cp.extradata+4)],[1,2,3,4]);
  const data=f.cp.extradata;f.slots.dn(256);
  assert.deepEqual(f.frees,[data,256]);assert.equal(f.claims[0].releases,1);
});
test('replacement preserves old allocation through IO failure and frees only on success',async()=>{
  const f=fixture();await f.run();const old=f.cp.extradata;
  await assert.rejects(f.run({ioError:true}),/IO failed/);
  assert.equal(f.cp.extradata,old);assert.equal(f.claims[0].releases,0);
  await f.run({io:()=>{assert.equal(f.cp.extradata,old);assert.ok(!f.frees.includes(old));}});
  assert.ok(f.frees.includes(old));assert.equal(f.claims[0].releases,1);
  assert.equal(f.claims[1].releases,1);assert.equal(f.claims[2].releases,0);
});
test('generation cancellation during IO discards only unpublished new claim',async()=>{
  const f=fixture();await f.run();const old=f.cp.extradata;
  await assert.rejects(f.run({io:()=>{f.cancelled=true;}}),/CANCELLED/);
  assert.equal(f.cp.extradata,old);assert.equal(f.claims[0].releases,0);
  assert.equal(f.claims[1].releases,1);assert.ok(!f.frees.includes(old));
});
test('failed native free retains claim; terminal retries cannot free or credit again',async()=>{
  const f=fixture();await f.run();f.failFree=true;
  assert.throws(()=>f.slots.dn(256),/free failed/);
  f.failFree=false;assert.throws(()=>f.slots.dn(256),/mismatch/);
  assert.equal(f.frees.length,0);assert.equal(f.claims[0].releases,0);
});
for(const mode of ['failRelease','throwRelease'])test(`${mode} after native free cannot retry credit or re-free`,async()=>{
  const f=fixture();await f.run();f[mode]=true;
  assert.throws(()=>f.slots.dn(256),/release/);
  assert.equal(f.frees.length,1);assert.equal(f.claims[0].releases,1);
  assert.throws(()=>f.slots.dn(256),/mismatch/);
  assert.equal(f.frees.length,1);assert.equal(f.claims[0].releases,1);
});
test('whole codecpar adoption and replacement reject independently owned header',async()=>{
  const f=fixture();await f.run();
  assert.throws(()=>f.slots.Yi(256,512),/independent/);
  assert.throws(()=>f.slots.eioCodecparAdopt(256,{},4),/independent/);
  assert.throws(()=>f.slots.eioCodecparReplace(256,512,{}),/independent/);
  assert.equal(f.claims[0].releases,0);
});
test('foundMoov temp freed after JS slice succeeds or throws, copy remains uncovered',async()=>{
  const f=fixture(),sideData={};await f.run({temp:true,sideData});
  assert.deepEqual([...sideData[1]],[1,2,3,4]);assert.equal(f.cp.extradata,0);
  assert.equal(f.claims[0].releases,1);
  await assert.rejects(f.run({temp:true,sliceError:true}),/slice failed/);
  assert.equal(f.claims[1].releases,1);assert.equal(f.frees.length,2);
});
test('actual fragment moov P replaces a tracked H264 slot through admission',async()=>{
  const f=fixture();await f.run();const old=f.cp.extradata;
  for(const id of [23716,8332,59436,23905,28320,3031,66293,52617,23902,87114,86299,35924,85626])f.modules[id]={};
  f.modules[37766]={A:tag=>tag==='trak'?1:2};
  f.modules[78215]={A:()=>({trackId:9})};
  f.modules[97238]={A:{123:async(reader,stream)=>{
    stream.sideData[1]=new Uint8Array([9,8,7,6]);
  }}};
  const P=f.load(moovFactory).wB;
  const existing={index:0,codecpar:f.cp,privData:{trackId:9},sideData:{}};
  Object.assign(f.fc,{streams:[existing],streamIndex:1,
    createStream(){const stream={index:1,codecpar:{},sideData:{}};this.streams.push(stream);return stream;},
    removeStream(stream){this.streams=this.streams.filter(item=>item!==stream);}});
  let pos=0n;const values=[16,1,8,123];
  const reader={getPos:()=>pos,readUint32:async()=>{pos+=4n;return values.shift();}};
  await P(reader,f.fc,{foundMoov:true,fragment:true},{size:16});
  assert.notEqual(f.cp.extradata,old);assert.equal(f.claims[0].releases,1);
  assert.equal(f.claims[1].releases,0);assert.equal(f.claims[1].size,4);
  assert.deepEqual([...f.buffer.subarray(f.cp.extradata,f.cp.extradata+4)],[9,8,7,6]);
  const replacement=f.cp.extradata,freed=f.frees.length,allocs=f.allocs.length;
  f.refuse=true;pos=0n;values.push(16,1,8,123);
  await assert.rejects(P(reader,f.fc,{foundMoov:true,fragment:true},{size:16}),/admission refused/);
  assert.equal(f.cp.extradata,replacement);assert.equal(f.frees.length,freed);
  assert.equal(f.allocs.length,allocs);
  f.slots.dn(256);assert.equal(f.claims[1].releases,1);
});
test('pointer identity mismatch fails closed without freeing stale identity',async()=>{
  const f=fixture();await f.run();f.cp.extradata=8192;
  assert.throws(()=>f.slots.dn(256),/mismatch/);
  assert.equal(f.frees.length,0);assert.equal(f.claims[0].releases,0);
});
