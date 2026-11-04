'use strict';
// Read only shipped factories; no candidate overlays or external artifacts.
// Synthetic descriptors; real shipped parser/P/codec-slot/XC factories and shared owners.
const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const vm=require('node:vm');
const ownership=require('../public/web/player-shared-media-ownership.js');
const resources=require('../public/web/player-owned-media-resources.js');
function source(file,id){
  const text=fs.readFileSync('public/vendor/web/libmedia/'+file,'utf8');
  const start=text.indexOf(id+':(');assert.ok(start>=0);
  const rest=text.slice(start),end=/},(\d+):/.exec(rest);assert.ok(end);
  const body=rest.slice(String(id).length+1,end.index+1);
  return vm.runInNewContext('('+body+')',{Uint8Array,DataView});
}
const slotFactory=source('avplayer.js',67669),parserFactory=source('943.avplayer.js',97409);
const pFactory=source('943.avplayer.js',8732),mapFactory=source('943.avplayer.js',52617);
const aacFactory=source('943.avplayer.js',43963),opusFactory=source('943.avplayer.js',17689);
const readerFactory=source('avplayer.js',70704),writerFactory=source('avplayer.js',99248);
const label='mp4-esds-native';
function length(n){
  const out=[n&127];while(n>>>=7)out.unshift(128|(n&127));return out;
}
function descriptor(tag,bytes){return [tag,...length(bytes.length),...bytes];}
function esds(oti=64,payload=[0x12,0x10]){
  const decoder=descriptor(4,[oti,0x15,0,0,0,0,0,0,0,0,0,0,0,...descriptor(5,payload)]);
  return Uint8Array.from([0,0,0,0,...descriptor(3,[0,1,0,...decoder,6,1,2])]);
}
function fixture(){
  const bytes=new Uint8Array(131072),view=new DataView(bytes.buffer),reads={},writes={};
  for(const type of [8,15,20,25]){
    reads[type]=p=>view.getUint32(p,true);writes[type]=(p,v)=>view.setUint32(p,v,true);
  }
  reads[17]=p=>view.getBigInt64(p,true);writes[17]=(p,v)=>view.setBigInt64(p,v,true);
  let bump=4096;
  const f={bytes,allocs:[],frees:[],claims:[],payloadReads:0,metadataReads:0,copies:0,logs:[]};
  const controller=ownership.create({capacityBytes:16384,maxEntries:16});
  const owned=resources.createOwnedResources(controller);f.snapshot=()=>controller.snapshot();
  function free(p){assert.ok(!f.frees.includes(p),'native free once: '+p);f.frees.push(p);}
  const modules={
    80209:{JW:(p,n)=>{const b=bytes.subarray(p,p+n);if(f.sliceError)b.slice=()=>{throw Error('slice failed');};return b;},
      lW:(p,n,b)=>{f.copies++;bytes.set(b,p);},sO:(p,v,n)=>bytes.fill(v,p,p+n)},
    56238:{f:reads},6927:{M:writes},
    46626:{sY:n=>{if(f.mallocFail)return 0;const p=bump;bump+=n+16;f.allocs.push({p,n});return p;},
      Eb:free,aI:p=>{free(reads[20](p));writes[20](p,0);}},
    57216:{bl:()=>{}},46564:{NZ:()=>{}},29688:{N_:-1},
    34117:{z3:(...args)=>f.logs.push(args),R8:(...args)=>f.logs.push(args)},
    54568:{o9:'pointer'},54809:{},98901:{k:()=>3840},18528:{},
    19371:{A:(o,k,v)=>{o[k]=v;}},41662:{encode:s=>new TextEncoder().encode(s)},
    37766:{A:tag=>tag==='trak'?1:2}
  };
  function load(factory){
    const out={},req=id=>{assert.ok(modules[id],`missing module ${id}`);return modules[id];};
    req.d=(out,defs)=>{for(const key in defs)Object.defineProperty(out,key,{get:defs[key]});};
    factory({},out,req);return out;
  }
  function cp(pointer){
    const result={pointer,chLayout:{}};
    for(const [key,offset] of [['codecId',4],['extradata',12],['extradataSize',16]])
      Object.defineProperty(result,key,{get:()=>reads[20](pointer+offset),set:v=>writes[20](pointer+offset,v)});
    // Actual pointer layout stores undefined codec ID as zero, but JS struct
    // accessor semantics for unknown OTI are not claimed by this fixture.
    return result;
  }
  f.cp=cp(256);f.slots=modules[67669]=load(slotFactory);
  modules[70704]=load(readerFactory);modules[99248]=load(writerFactory);
  modules[43963]=load(aacFactory);modules[17689]=load(opusFactory);
  f.mapping=modules[52617]=load(mapFactory);f.parse=load(parserFactory).A;
  f.fc={eioAcquireOwnedResource:async(size,label)=>{
    f.claims.push({size,label});if(f.refuse)return {ok:false};
    return {...owned.acquire(size,label,7),generation:7,isCancelled:()=>!!f.cancelled};
  }};
  f.reader=(input,options={})=>{
    let pos=0;
    function take(n){assert.ok(n>=0&&pos+n<=input.length,'bounded reader');const data=input.subarray(pos,pos+n);pos+=n;return data;}
    return {getPos:()=>BigInt(pos),skip:async n=>{take(n);},
      readUint8:async()=>{if(options.tailError&&f.payloadReads)throw Error('tail IO failed');f.metadataReads++;return take(1)[0];},
      readUint32:async()=>{f.metadataReads++;return new DataView(take(4).buffer,input.byteOffset+pos-4,4).getUint32(0);},
      readBuffer:async(n,target)=>{f.payloadReads++;if(options.ioError)throw Error('IO failed');
        if(options.cancel)f.cancelled=true;target.set(take(n));return target;}};
  };
  f.run=(options={})=>{const input=options.input||esds(options.oti,options.payload);
    return f.parse(f.reader(input,options),{codecpar:f.cp,sideData:options.sideData||{}},
      {size:input.length},{eioFormatContext:f.fc,foundMoov:!!options.temp});};
  f.P=(oti=64,payload=[0x12,0x10])=>{
    for(const id of [23716,8332,59436,23905,28320,3031,66293,52617,23902,87114,86299,35924,85626])
      if(!modules[id])modules[id]={};
    modules[78215]={A:()=>({trackId:9})};modules[97238]={A:{123:f.parse}};
    const existing={index:0,codecpar:f.cp,privData:{trackId:9},sideData:{}};
    Object.assign(f.fc,{streams:[existing],streamIndex:1,
      createStream(){const s={index:1,codecpar:cp(512),sideData:{}};this.streams.push(s);return s;},
      removeStream(s){this.streams=this.streams.filter(item=>item!==s);}});
    const P=load(pFactory).wB,input=esds(oti,payload);
    const data=new Uint8Array(input.length+16),dv=new DataView(data.buffer);
    dv.setUint32(0,data.length);dv.setUint32(4,1);dv.setUint32(8,input.length+8);dv.setUint32(12,123);data.set(input,16);
    return ()=>P(f.reader(data),f.fc,{eioFormatContext:f.fc,foundMoov:true,fragment:true},{size:data.length});
  };
  f.native=()=>[...bytes.subarray(f.cp.extradata,f.cp.extradata+f.cp.extradataSize)];
  return f;
}
test('esds refusal after metadata, before final native malloc/copy/read; missing strict hook',async()=>{
  const f=fixture();f.refuse=true;await assert.rejects(f.run(),/admission refused/);
  assert.ok(f.metadataReads>0);assert.deepEqual(f.claims,[{size:2,label}]);
  assert.equal(f.allocs.length,0);assert.equal(f.payloadReads,0);assert.equal(f.copies,0);
  f.fc={eioBeforeSampleAlloc:()=>{}};await assert.rejects(f.run(),/hook missing/);
  assert.equal(f.allocs.length,0);assert.equal(f.payloadReads,0);
});
test('esds AAC exact final bytes persist; actual XC fields and real dn free once',async()=>{
  const f=fixture();await f.run();const data=f.cp.extradata;
  assert.deepEqual(f.claims,[{size:2,label}]);assert.deepEqual(f.allocs,[{p:data,n:2}]);
  assert.deepEqual(f.native(),[0x12,0x10]);assert.equal(f.cp.codecId,86018);
  assert.equal(f.cp.profile,2);assert.equal(f.cp.sampleRate,44100);
  assert.equal(f.cp.chLayout.nbChannels,2);assert.equal(f.cp.frameSize,1024);
  assert.equal(f.snapshot().heldBytes,2);f.slots.dn(256);
  assert.deepEqual(f.frees,[data,256]);assert.equal(f.snapshot().heldBytes,0);
  assert.equal(f.snapshot().countOwnedResources,0);
});
test('esds IO/cancel/copy/malloc failure keeps published old native slot',async()=>{
  const f=fixture();await f.run();const old=f.cp.extradata;
  for(const mode of ['io','tail','cancel','slice','malloc']){
    f.sliceError=mode==='slice';f.mallocFail=mode==='malloc';
    await assert.rejects(f.run({ioError:mode==='io',tailError:mode==='tail',cancel:mode==='cancel'}));
    assert.equal(f.cp.extradata,old);assert.equal(f.cp.extradataSize,2);
    assert.ok(!f.frees.includes(old));assert.equal(f.snapshot().heldBytes,2);
    f.cancelled=false;f.sliceError=false;f.mallocFail=false;
  }
  await f.run();assert.ok(f.frees.includes(old));assert.equal(f.snapshot().heldBytes,2);
  f.slots.dn(256);assert.equal(f.snapshot().heldBytes,0);
});
test('esds foundMoov sideData replacement and copy failure free temporary native once',async()=>{
  const f=fixture(),sideData={1:Uint8Array.of(9)};
  await f.run({temp:true,sideData});assert.deepEqual([...sideData[1]],[0x12,0x10]);
  assert.equal(f.cp.extradata,0);assert.equal(f.frees.length,1);assert.equal(f.snapshot().heldBytes,0);
  const previous=sideData[1];f.sliceError=true;
  await assert.rejects(f.run({temp:true,sideData}),/slice failed/);
  assert.equal(sideData[1],previous);assert.equal(f.frees.length,2);assert.equal(f.snapshot().heldBytes,0);
});
test('esds real P repeated fragments use owned replacement and refuse before old free',async()=>{
  const f=fixture();await f.run();const old=f.cp.extradata,run=f.P();await run();
  const replacement=f.cp.extradata;assert.notEqual(replacement,old);assert.ok(f.frees.includes(old));
  assert.deepEqual(f.claims,[{size:2,label},{size:2,label},{size:2,label}]);
  assert.deepEqual(f.native(),[0x12,0x10]);assert.equal(f.snapshot().heldBytes,2);
  assert.equal(f.fc.streams.length,1);assert.deepEqual([...f.fc.streams[0].sideData[1]],[0x12,0x10]);
  const acquire=f.fc.eioAcquireOwnedResource;let calls=0;
  f.fc.eioAcquireOwnedResource=(...args)=>++calls===2?{ok:false}:acquire(...args);
  const before=f.allocs.length;await assert.rejects(run(),/admission refused/);
  assert.equal(f.allocs.length,before+1,'only temporary foundMoov allocation');
  assert.equal(f.cp.extradata,replacement);assert.ok(!f.frees.includes(replacement));
  assert.equal(f.snapshot().heldBytes,2);f.slots.dn(256);assert.equal(f.snapshot().heldBytes,0);
});
test('esds actual Q9 IDs are parsed, not assumed AAC; exact long descriptor lengths',async()=>{
  // This proves byte ownership / descriptor traversal, NOT decoder support.
  for(const [oti,codecId] of [[32,12],[33,27],[35,173],[51,196],[177,167],[64,86018],
    [102,86018],[103,86018],[104,86018],[105,86017],[107,86017],[193,86028],[221,86021],[0,0]]){
    const f=fixture(),payload=Array(130).fill(0);payload[0]=0x12;payload[1]=0x10;
    await f.run({oti,payload});assert.equal(f.cp.codecId,codecId);assert.equal(f.mapping.Q9[oti],codecId);
    assert.deepEqual(f.native(),payload);assert.deepEqual(f.claims,[{size:130,label}]);
    f.slots.dn(256);assert.equal(f.snapshot().heldBytes,0);
  }
});
test('esds Opus actual XC retains original header interpretation (no dOps conversion)',async()=>{
  const f=fixture(),payload=[...Buffer.from('OpusHead'),1,2,0x34,0x12,0x80,0xbb,0,0,0,0,0];
  await f.run({oti:173,payload});assert.equal(f.cp.codecId,86076);
  assert.deepEqual(f.native(),payload);assert.equal(f.cp.chLayout.nbChannels,2);
  assert.equal(f.cp.initialPadding,0x1234);assert.equal(f.cp.sampleRate,48000);
  assert.equal(f.cp.seekPreroll,3840);f.slots.dn(256);assert.equal(f.snapshot().heldBytes,0);
});
test('esds original zero/unknown descriptor skips remain allocation-free',async()=>{
  for(const input of [Uint8Array.from([0,0,0,0,3,0]),Uint8Array.from([0,0,0,0,9,2,0,0])]){
    const f=fixture();await f.run({input});assert.equal(f.allocs.length,0);assert.equal(f.claims.length,0);
  }
});
test('esds non-strict path still replaces without leak and dn clears native',async()=>{
  const f=fixture();f.fc={};await f.run();const old=f.cp.extradata;
  await f.run();assert.ok(f.frees.includes(old));assert.deepEqual(f.native(),[0x12,0x10]);
  f.slots.dn(256);assert.equal(f.frees.length,3);
});

test('esds actual P covers other newly routed Q9 IDs without naked replacement',async()=>{
  for(const [oti,codecId] of [[32,12],[105,86017],[107,86017],[221,86021],[0,0]]){
    const f=fixture();await f.run({oti});const old=f.cp.extradata;
    await f.P(oti)();assert.equal(f.cp.codecId,codecId);
    assert.ok(f.frees.includes(old));assert.equal(f.snapshot().heldBytes,2);
    assert.deepEqual(f.claims,[{size:2,label},{size:2,label},{size:2,label}]);
    f.slots.dn(256);assert.equal(f.snapshot().heldBytes,0);
  }
});
test('esds independent slot refuses whole-codecpar owner before native malloc/read',async()=>{
  const f=fixture(),calls=[];
  f.slots.eioCodecparAdopt(256,{bytes:0,bind:()=>true,
    release:()=>{calls.push('release');return true;},retainFailure:()=>calls.push('retain')},0);
  await assert.rejects(f.run(),/whole codecpar already owned/);
  assert.equal(f.allocs.length,0);assert.equal(f.payloadReads,0);assert.equal(f.claims.length,0);
  f.slots.dn(256);assert.deepEqual(calls,['release']);assert.deepEqual(f.frees,[256]);
});

test('esds multiple descriptors keep XC order and publish only last payload after all IO',async()=>{
  const f=fixture(),first=esds(),second=esds(64,[0x11,0x90]);
  const input=Uint8Array.from([...first,...second.slice(4)]);
  await f.run({input});assert.deepEqual(f.native(),[0x11,0x90]);
  assert.equal(f.cp.sampleRate,48000);assert.equal(f.cp.chLayout.nbChannels,2);
  assert.equal(f.allocs.length,2);assert.deepEqual(f.frees,[f.allocs[0].p]);
  assert.equal(f.snapshot().heldBytes,2);f.slots.dn(256);assert.equal(f.snapshot().heldBytes,0);
});
