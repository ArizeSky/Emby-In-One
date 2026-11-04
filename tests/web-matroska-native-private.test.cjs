'use strict';
// Default: shipped factories. Optional local candidate validation (VM only):
// nice -n 19 taskset -c <allowed-cpu> node tests/web-matroska-native-private.test.cjs --candidate=/var/tmp/eio-matroska-private-candidate.json
// Candidate mode also applies the TWO declared parent prerequisites to 67669 in memory.
const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const vm=require('node:vm');
const ownership=require('../public/web/player-shared-media-ownership.js');
const owned=require('../public/web/player-owned-media-resources.js');
const base='public/vendor/web/libmedia/';
const candidateArg=process.argv.find(a=>a.startsWith('--candidate='));
let matroska=fs.readFileSync(base+'573.avplayer.js','utf8');
let native=fs.readFileSync(base+'avplayer.js','utf8');
function replaceOnce(s,old,value){assert.equal(s.split(old).length,2,old.slice(0,80));return s.replace(old,value);}
if(candidateArg){
  const candidate=JSON.parse(fs.readFileSync(candidateArg.slice(12),'utf8'));
  for(const edit of candidate.edits)matroska=replaceOnce(matroska,edit.oldFragment,edit.newFragment);
  native=replaceOnce(native,'["mp4-hevc-hvcc-native", "mp4-av1-av1c-native", "mp4-opus-dops-native"].includes(label)',
    '["mp4-hevc-hvcc-native", "mp4-av1-av1c-native", "mp4-opus-dops-native", "matroska-codec-private-native"].includes(label)');
  native=replaceOnce(native,'if (record) fc.eioAvccNativeBindings =',
    'if (record && label !== "matroska-codec-private-native") fc.eioAvccNativeBindings =');
}
function factory(source,id){
  const start=source.indexOf(id+':');assert.ok(start>=0);
  const rest=source.slice(start+String(id).length+1);
  const boundary=/},\d+:\(e,t,[ai]\)=>\{/.exec(rest);assert.ok(boundary,`boundary ${id}`);
  return vm.runInNewContext('('+rest.slice(0,boundary.index+1)+')',{Uint8Array,DataView,TextDecoder,TextEncoder});
}
function fixture(codecId=27,payload=new Uint8Array([1,66,0,31,9,8])){
  const buffer=new Uint8Array(65536),view=new DataView(buffer.buffer),reads={},writes={};
  for(const type of [8,15,20,25]){reads[type]=p=>view.getUint32(p,true);writes[type]=(p,v)=>view.setUint32(p,v,true);}
  reads[17]=p=>view.getBigInt64(p,true);writes[17]=(p,v)=>view.setBigInt64(p,v,true);
  const f={allocs:[],copies:[],frees:[],labels:[],xc:[],cancel:false,refuse:false};let bump=4096;
  const free=p=>{assert.ok(!f.frees.includes(p),'no duplicate native free');f.frees.push(p);};
  const controller=ownership.create({capacityBytes:1024,maxEntries:16});
  const resources=owned.createOwnedResources(controller);
  f.controller=controller;
  const modules={
    19371:{A:(obj,key,value)=>{obj[key]=value;}},54568:{o9:'pointer'},
    80209:{s3:(p,n)=>buffer.subarray(p,p+n),JW:(p,n)=>buffer.subarray(p,p+n),
      lW:(p,n,bytes)=>{f.copies.push({p,n});buffer.set(bytes,p);if(f.onCopy)f.onCopy();},sO:(p,v,n)=>buffer.fill(v,p,p+n)},
    56238:{f:reads},6927:{M:writes},
    46626:{sY:n=>{const p=bump;bump+=n+16;f.allocs.push({p,n});return p;},Eb:free,
      aI:p=>{free(reads[20](p));writes[20](p,0);}},
    57216:{bl:()=>{}},46564:{NZ:()=>{}},29688:{N_:-1,Dh:-1n},
    35734:{A:class{}},85183:{eP:{TEST:codecId},Nk:{TEST:codecId}},
    83796:{__:(values,fn)=>values.forEach(fn)},99817:{Bs:()=>false},
    34117:{z3:()=>{}},35924:{LR:-1094995529},
    41662:{decode:bytes=>new TextDecoder().decode(bytes),encode:s=>new TextEncoder().encode(s)},
    18528:{A:(_type,arrays)=>new Uint8Array(arrays.flatMap(a=>[...a]))},
    20642:{XC:(_stream,bytes)=>f.xc.push([...bytes])},
    73448:{ob:34,XC:(_stream,bytes)=>f.xc.push([...bytes])}
  };
  // Unused imported dependencies are inert; parser, byte reader, native slot,
  // and shared owned-resource manager are actual shipped implementations.
  const load=fn=>{const out={};const req=id=>modules[id]||{};
    req.d=(out,defs)=>{for(const key in defs)Object.defineProperty(out,key,{get:defs[key]});};req.r=()=>{};
    fn({},out,req);return out;};
  f.modules=modules;f.slots=modules[67669]=load(factory(native,67669));
  modules[70704]=load(factory(native,70704));
  const Format=load(factory(matroska,25248)).default;
  f.format=new Format();
  const cp={pointer:256,chLayout:{},flags:0};
  Object.defineProperties(cp,{
    extradata:{get:()=>reads[20](268),set:v=>writes[20](268,v)},
    extradataSize:{get:()=>reads[15](272),set:v=>writes[15](272,v)}
  });f.cp=cp;
  const stream={codecpar:cp,metadata:{},timeBase:{},disposition:0};
  f.track={codecId:'TEST',type:codecId===86028?2:1,codecPrivate:{size:BigInt(payload.length),data:payload}};
  f.fc={metadata:{},chapters:[],streams:[stream],createStream:()=>stream,
    eioAcquireOwnedResource:async(size,label)=>{
      f.labels.push(label);
      if(f.refuse)return {ok:false};
      return {...resources.acquire(size,label,7),generation:7,isCancelled:()=>f.cancel};
    }};
  f.format.context={header:{docType:'matroska'},tracks:{entry:[f.track]}};
  f.run=()=>f.format.analyzeStreams(f.fc);
  f.bytes=()=>[...buffer.subarray(cp.extradata,cp.extradata+cp.extradataSize)];
  return f;
}
test('real analyzeStreams copies H264 to one persistent owned native slot, actual dn frees once',async()=>{
  const f=fixture();await f.run();
  assert.deepEqual(f.labels,['matroska-codec-private-native']);
  assert.equal(f.allocs.length,1);assert.equal(f.copies.length,1);
  assert.deepEqual(f.bytes(),[1,66,0,31,9,8]);assert.deepEqual(f.xc,[f.bytes()]);
  assert.equal(f.controller.snapshot().heldBytes,6);
  assert.equal(f.fc.eioAvccNativeBindings,undefined,'Matroska must not count H264 avcC bindings');
  const data=f.cp.extradata;f.slots.dn(256);
  assert.deepEqual(f.frees,[data,256]);assert.equal(f.controller.snapshot().heldBytes,0);
  assert.equal(f.track.codecPrivate.data.length,6,'privData JS alias is not native-freed');
});
test('FLAC final streaminfo trim precedes the sole owned allocation/copy (non-symmetric subview)',async()=>{
  const streaminfo=Uint8Array.from({length:34},(_,i)=>(i*7+3)&255);
  const envelope=new Uint8Array(53);envelope.fill(211);envelope.set([102,76,97,67,128,0,0,34],5);envelope.set(streaminfo,13);
  const f=fixture(86028,envelope.subarray(5,47));await f.run();
  assert.deepEqual(f.bytes(),[...streaminfo]);assert.deepEqual(f.xc,[[...streaminfo]]);
  assert.deepEqual(f.allocs.map(a=>a.n),[34]);assert.deepEqual(f.copies.map(a=>a.n),[34]);
  assert.deepEqual(f.labels,['matroska-codec-private-native']);assert.equal(f.frees.length,0);
  assert.equal(f.controller.snapshot().heldBytes,34);f.slots.dn(256);
  assert.equal(f.controller.snapshot().heldBytes,0);
});
test('refusal and strict missing hook reject before malloc/copy, keep old slot',async()=>{
  const f=fixture();await f.run();const old=f.cp.extradata;f.refuse=true;
  await assert.rejects(async()=>f.run(),/admission refused/);
  assert.equal(f.cp.extradata,old);assert.equal(f.allocs.length,1);assert.equal(f.copies.length,1);
  delete f.fc.eioAcquireOwnedResource;f.fc.eioBeforeSampleAlloc=()=>{};
  await assert.rejects(async()=>f.run(),/hook missing/);assert.equal(f.allocs.length,1);
  f.slots.dn(256);assert.equal(f.controller.snapshot().heldBytes,0);
});
test('cancelled admission preserves old slot and releases only new claim',async()=>{
  const f=fixture();await f.run();const old=f.cp.extradata;f.cancel=true;
  await assert.rejects(async()=>f.run(),/cancelled/);
  assert.equal(f.cp.extradata,old);assert.equal(f.allocs.length,1);assert.equal(f.copies.length,1);
  assert.equal(f.frees.length,0);assert.equal(f.controller.snapshot().heldBytes,6);
  f.slots.dn(256);assert.deepEqual(f.frees,[old,256]);assert.equal(f.controller.snapshot().heldBytes,0);
});
test('undefined admission hook retains baseline native copy',async()=>{
  const f=fixture();delete f.fc.eioAcquireOwnedResource;await f.run();
  assert.deepEqual(f.bytes(),[1,66,0,31,9,8]);assert.equal(f.allocs.length,1);
  assert.equal(f.controller.snapshot().heldBytes,0);f.slots.dn(256);
});

test('copy exception and cancellation after native copy discard unpublished bytes, preserve old slot',async()=>{
  const f=fixture();await f.run();const old=f.cp.extradata;
  f.onCopy=()=>{throw Error('synthetic copy failure');};
  await assert.rejects(async()=>f.run(),/copy failure/);
  assert.equal(f.cp.extradata,old);assert.equal(f.controller.snapshot().heldBytes,6);
  f.onCopy=()=>{f.cancel=true;};
  await assert.rejects(async()=>f.run(),/not publishable/);
  assert.equal(f.cp.extradata,old);assert.equal(f.controller.snapshot().heldBytes,6);
  assert.equal(f.frees.length,2);assert.ok(!f.frees.includes(old));
  f.slots.dn(256);assert.equal(f.controller.snapshot().heldBytes,0);
});
test('actual format readHeader maps EIO_AVCC to non-EOF failure even with ioReader.error zero',async()=>{
  const f=fixture();f.refuse=true;
  const ids=[440786851,408125543,374648427,524531317];
  const tracks=f.format.context.tracks;
  f.modules[27228]={aL:async()=>ids.shift(),Qb:async()=>10n,
    Pb:async(_fc,_size,syntax)=>syntax==='tracks'?tracks:{docType:'matroska',maxIdLength:4,maxSizeLength:8},
    Wy:'tracks',jt:'header'};
  // The factory captures module imports when instantiated; update the same imported
  // parser object rather than substituting readHeader/analyzeStreams algorithms.
  // Recreate fixture's format with this dependency through a fresh factory load.
  f.modules[23902]={X$:Object.assign};
  const out={},req=id=>f.modules[id]||{};
  req.d=(o,defs)=>{for(const k in defs)Object.defineProperty(o,k,{get:defs[k]});};req.r=()=>{};
  factory(matroska,25248)({},out,req);
  const format=new out.default();format.context={header:{maxIdLength:4,maxSizeLength:8},isLive:true,firstCluster:-1n};
  f.fc.ioReader={error:0,getPos:()=>1n,seek:async()=>{}};
  const result=await format.readHeader(f.fc);
  assert.equal(result,-1094995529);assert.equal(f.fc.ioReader.error,0);
  assert.equal(f.fc.eioDualCacheError.code,'EIO_AVCC_NATIVE_OWNERSHIP');
  assert.equal(f.allocs.length,0);assert.equal(f.copies.length,0);
});
test('track CodecPrivate preserves image attachment imports and metadata/chapter scope',async()=>{
  const f=fixture();
  const reads=f.modules[56238].f,writes=f.modules[6927].M;
  const image=new Uint8Array([137,80,78,71,13,10,26,10]);
  const packet=1024,packetPayload=[];
  // Nonzero preexisting flags prove the attachment branch READS n.f, not just writes.
  writes[15](packet+36,8);
  f.modules[85183].xz={'image/png':61};
  Object.assign(f.modules[29688],{SF:1000000});
  Object.assign(f.modules[46564],{
    _5:()=>packet,
    NX:(ptr,data,size)=>packetPayload.push({ptr,data,size})
  });
  // Reinstantiate only to capture the new packet dependency; analyzeStreams itself
  // and the CodecPrivate allocation/copy/owned-slot path remain shipped code.
  const out={},req=id=>f.modules[id]||{};
  req.d=(o,defs)=>{for(const k in defs)Object.defineProperty(o,k,{get:defs[k]});};req.r=()=>{};
  factory(matroska,25248)({},out,req);
  f.format=new out.default();
  const trackStream=f.fc.streams[0];trackStream.index=0;
  const cover={index:1,codecpar:{},metadata:{},disposition:0};
  let created=0;
  f.fc.createStream=()=>{
    created++;
    if(created===1)return trackStream;
    assert.equal(created,2,'only one track and one attachment');
    f.fc.streams.push(cover);return cover;
  };
  const attachment={name:'cover.png',mime:'image/png',description:'synthetic cover',
    data:{size:BigInt(image.length),data:image}};
  const dateUTC=new Uint8Array(8);
  new DataView(dateUTC.buffer).setBigUint64(0,2000000n);
  f.format.context={
    header:{docType:'matroska'},tracks:{entry:[f.track]},
    attachments:{entry:[attachment]},
    info:{timestampScale:1000000,duration:120,muxingApp:'fixture muxer',title:'fixture title',dateUTC:{data:dateUTC}},
    tags:{entry:[{tag:[{name:'COMMENT',string:'fixture comment'}]}]},
    chapters:{entry:[{atom:[
      {uid:11n,start:0n,display:{title:'Opening',language:'eng'}},
      {uid:12n,start:5n,end:9n}
    ]}]}
  };
  await f.run();
  assert.equal(created,2);
  assert.deepEqual(f.labels,['matroska-codec-private-native']);
  assert.deepEqual(f.bytes(),[1,66,0,31,9,8]);
  assert.deepEqual(f.xc,[f.bytes()]);
  assert.equal(cover.privData,attachment);
  assert.deepEqual(cover.metadata,{title:'cover.png',mime:'image/png',description:'synthetic cover'});
  assert.equal(cover.codecpar.codecType,0);assert.equal(cover.codecpar.codecId,61);
  assert.equal(cover.disposition,1024);assert.equal(cover.attachedPic,packet);
  assert.equal(reads[15](packet+32),1,'packet stream index');
  assert.equal(reads[15](packet+36),9,'imported n.f preserves existing packet flags');
  assert.equal(packetPayload.length,1);
  const payload=packetPayload[0];
  assert.equal(payload.ptr,packet);assert.equal(payload.size,image.length);
  assert.deepEqual([...f.modules[80209].s3(payload.data,payload.size)],[...image]);
  assert.equal(f.allocs.length,2);assert.equal(f.copies.length,2);
  assert.deepEqual(trackStream.timeBase,{num:1,den:1000});assert.equal(trackStream.duration,120n);
  assert.deepEqual(f.fc.metadata,{COMMENT:'fixture comment',encoder:'fixture muxer',title:'fixture title',
    creationTime:'2001-01-01T00:00:00.002Z'});
  // Normalize only VM realm prototypes; values/BigInts must remain exact.
  assert.deepEqual(f.fc.chapters.map(c=>({id:c.id,start:c.start,end:c.end,
    num:c.timeBase.num,den:c.timeBase.den,title:c.metadata.title,language:c.metadata.language})),[
    {id:11n,start:0n,end:5n,num:1,den:1000000000,title:'Opening',language:'eng'},
    {id:12n,start:5n,end:9n,num:1,den:1000000000,title:'',language:''}
  ]);
  f.slots.dn(256);assert.equal(f.controller.snapshot().heldBytes,0);
  f.modules[46626].Eb(payload.data);
});
