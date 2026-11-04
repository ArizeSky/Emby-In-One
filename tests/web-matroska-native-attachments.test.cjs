'use strict';
// Shipped factories only; synthetic buffers and real final-reference ownership.
const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const vm=require('node:vm');
const ownership=require('../web-src/player-shared-media-ownership.js');
const owned=require('../web-src/player-owned-media-resources.js');
const mediaHandles=require('../web-src/player-media-resource-handles.js');
const base='public/vendor/web/libmedia/';
const matroska=fs.readFileSync(base+'573.avplayer.js','utf8');
const native=fs.readFileSync(base+'avplayer.js','utf8');
function factory(source,id){
  const start=source.indexOf(id+':');assert.ok(start>=0);
  const rest=source.slice(start+String(id).length+1);
  const boundary=/},\d+:\(e,t,[ai]\)=>\{/.exec(rest);assert.ok(boundary,`boundary ${id}`);
  return vm.runInNewContext('('+rest.slice(0,boundary.index+1)+')',{Uint8Array,DataView,TextDecoder,TextEncoder});
}
function fixture(kind="font"){
  const codecId=0,payload=new Uint8Array([1,2,3,4]);
  const buffer=new Uint8Array(65536),view=new DataView(buffer.buffer),reads={},writes={};
  for(const type of [8,15,20,25]){reads[type]=p=>view.getUint32(p,true);writes[type]=(p,v)=>view.setUint32(p,v,true);}
  reads[17]=p=>view.getBigInt64(p,true);writes[17]=(p,v)=>view.setBigInt64(p,v,true);
  const f={allocs:[],copies:[],frees:[],labels:[],xc:[],cancel:false,refuse:false};let bump=4096;
  f.buffer=buffer;f.reads=reads;f.writes=writes;
  const free=p=>{assert.ok(!f.frees.includes(p),'no duplicate native free');f.frees.push(p);};
  const controller=ownership.create({capacityBytes:1024,maxEntries:16});
  const resources=owned.createOwnedResources(controller);
  f.controller=controller;
  const alloc=n=>{const p=bump;bump+=n+16;f.allocs.push({p,n});return p;};
  const modules={
    23902:{X$:Object.assign},19371:{A:(obj,key,value)=>{obj[key]=value;}},54568:{o9:'pointer'},90213:mediaHandles,
    80209:{s3:(p,n)=>buffer.subarray(p,p+n),JW:(p,n)=>buffer.subarray(p,p+n),
      lW:(p,n,bytes)=>{f.copies.push({p,n});buffer.set(bytes,p);if(f.onCopy)f.onCopy();},Mr:(to,from,n)=>buffer.copyWithin(to,from,from+n),sO:(p,v,n)=>buffer.fill(v,p,p+n)},
    56238:{f:reads},6927:{M:writes},
    46626:{sY:alloc,Gy:n=>{const p=alloc(n);buffer.fill(0,p,p+n);return p;},Eb:free,
      aI:p=>{free(reads[20](p));writes[20](p,0);}},
    57216:{bl:()=>{}},46564:{NZ:()=>{}},29688:{N_:-1,Dh:-1n},
    35734:{A:class{}},85183:{eP:{TEST:codecId},Nk:{TEST:codecId},xz:{"image/png":61}},
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
  // Synthetic WASM atomics and facade bridge only. AVBuffer 13486 and packet
  // 46564 are real; final-free credit runs from the real Q7 last-ref callback.
  modules[33817]={M_:(p,v)=>writes[15](p,v),WQ:(p,v)=>{const old=reads[15](p);writes[15](p,old+v);return old;},
    jb:(p,v)=>{const old=reads[15](p);writes[15](p,old-v);return old;}};
  modules[90187]={prepareDataFree:key=>controller.prepareFinalFree(key),
    commitDataFree:r=>{if(r.ok){assert.ok(f.frees.includes(r.ticket.data),'real data free precedes credit');
      assert.equal(controller.commitFinalFree(r.ticket,{dataFreed:true}).ok,true);}},dataFreeFailed:()=>{}};
  f.modules=modules;f.slots=modules[67669]=load(factory(native,67669));
  modules[13486]=load(factory(native,13486));
  f.packets=modules[46564]=load(factory(native,46564));
  const Format=load(factory(matroska,25248)).default;
  f.format=new Format();
  const cp={pointer:256,chLayout:{},flags:0};
  Object.defineProperties(cp,{
    extradata:{get:()=>reads[20](268),set:v=>writes[20](268,v)},
    extradataSize:{get:()=>reads[15](272),set:v=>writes[15](272,v)}
  });f.cp=cp;
  const stream=f.stream={index:0,codecpar:cp,metadata:{},timeBase:{num:1,den:1000},disposition:0};
  f.fc={metadata:{},chapters:[],streams:[stream],createStream:()=>stream,
    eioAcquireOwnedResource:async(size,label)=>{
      f.labels.push(label);
      if(f.refuse)return {ok:false};
      return {...resources.acquire(size,label,7),generation:7,isCancelled:()=>f.cancel};
    }};
  f.payload=payload;
  f.attachment={name:'synthetic',mime:kind==='picture'?'image/png':'font/ttf',data:{size:4n,data:payload}};
  f.format.context={header:{docType:'matroska'},attachments:{entry:[f.attachment]}};
  const begin='/*__EIO_DUAL_CACHE_DEMUX_BEGIN__*/',end='/*__EIO_DUAL_CACHE_DEMUX_END__*/';
  const helper=f.helper=vm.runInNewContext(native.slice(native.indexOf(begin)+begin.length,native.indexOf(end))+';__eioDualDemux',{
    o:{f:reads},Map,Math,Number,isFinite,performance:{now:()=>0},setTimeout,clearTimeout});
  const ledger=helper.make({demuxBytes:1024,maxPacketBytes:100});
  Object.assign(ledger,{phase:1,packetBytes:1024,probeBytes:1024});
  f.task={eioLedger:ledger,eioSharedController:controller};
  f.events=[];
  f.fc.eioBeforeSampleAlloc=async info=>{
    f.events.push('before');if(f.wait)await f.wait;if(f.refuse)return null;
    return f.token=helper.before(f.task,info);
  };
  f.fc.eioAfterSampleAlloc=(t,info)=>{f.events.push('after');helper.after(f.task,t,info);if(f.afterThrows)throw Error('after');};
  f.fc.eioReleaseSampleAlloc=(t,info)=>{f.events.push('release');return helper.release(f.task,t,info);};
  f.fc.eioIsSampleAllocCancelled=t=>f.cancel||helper.isCancelled(f.task,t);
  f.run=()=>f.format.analyzeStreams(f.fc);
  f.bytes=()=>[...buffer.subarray(cp.extradata,cp.extradata+cp.extradataSize)];
  return f;
}

const bounded={timeout:2000};
test('attachment length cannot wrap or exceed available payload before native allocation',bounded,async()=>{
  for(const kind of ['font','picture'])for(const size of [-1n,4294967296n,5n]){
    const f=fixture(kind);f.format.context.attachments.entry[0].data.size=size;
    await assert.rejects(f.run,e=>e.code==='EIO_AVCC_NATIVE_ATTACHMENT_SIZE');
    assert.equal(f.allocs.length,0);assert.equal(f.copies.length,0);
    assert.deepEqual(f.events,[]);assert.deepEqual(f.labels,[]);
    assert.equal(f.controller.snapshot().heldBytes,0);
  }
});
test('font native slot uses real manager, no early format-destroy credit; dn frees',bounded,async()=>{
  const f=fixture();await f.run();
  assert.deepEqual(f.labels,['matroska-attachment-native']);assert.deepEqual(f.bytes(),[1,2,3,4]);
  assert.equal(f.controller.snapshot().heldBytes,4);assert.equal(f.fc.eioAvccNativeBindings,undefined);
  const data=f.cp.extradata;await f.format.destroy(f.fc);
  assert.equal(f.controller.snapshot().heldBytes,4);assert.ok(!f.frees.includes(data));
  f.slots.dn(256);assert.deepEqual(f.frees,[data,256]);assert.equal(f.controller.snapshot().heldBytes,0);
});
test('font refusal/cancel preserve prior native slot before malloc/copy',bounded,async()=>{
  const f=fixture();await f.run();const old=f.cp.extradata;
  f.refuse=true;await assert.rejects(f.run,/admission refused/);
  f.refuse=false;f.cancel=true;await assert.rejects(f.run,/cancelled/);
  assert.equal(f.cp.extradata,old);assert.equal(f.allocs.length,1);assert.equal(f.copies.length,1);
  assert.equal(f.controller.snapshot().heldBytes,4);f.slots.dn(256);assert.equal(f.controller.snapshot().heldBytes,0);
});
test('picture admission rejects before data malloc and copy, without native slot',bounded,async()=>{
  const f=fixture('picture');f.refuse=true;
  await assert.rejects(f.run,e=>e.code==='EIO_AVCC_NATIVE_HEADER_PICTURE');
  assert.equal(f.copies.length,0);
  assert.equal(f.allocs.filter(a=>a.n===4).length,0);assert.deepEqual(f.labels,[]);
  assert.deepEqual(f.events,['before']);assert.equal(f.controller.snapshot().heldBytes,0);
});
test('picture cancellation while awaiting real before hook never allocates payload',bounded,async()=>{
  const f=fixture('picture');let resolve;f.wait=new Promise(r=>resolve=r);
  const result=f.run();await new Promise(r=>setImmediate(r));assert.deepEqual(f.events,['before']);
  f.cancel=true;resolve();await assert.rejects(()=>result,e=>e.code==='EIO_AVCC_NATIVE_HEADER_PICTURE');
  assert.equal(f.copies.length,0);assert.deepEqual(f.events,['before','release']);
  assert.equal(f.controller.snapshot().heldBytes,0);
});
test('picture actual NX/rN/Up/Q7 last-ref free, not format destroy, retires quota',bounded,async()=>{
  const f=fixture('picture');await f.run();
  assert.deepEqual(f.events,['before','after']);assert.equal(f.token.state,'HANDED');
  assert.equal(f.controller.snapshot().heldBytes,4);assert.deepEqual(f.labels,[]);
  const packet=f.stream.attachedPic,data=f.reads[20](packet+24);
  assert.deepEqual([...f.buffer.subarray(data,data+4)],[1,2,3,4]);
  const clone=f.packets._5();f.packets.rN(clone,packet);
  await f.format.destroy(f.fc);assert.equal(f.controller.snapshot().heldBytes,4);
  f.packets.Up(packet);assert.equal(f.controller.snapshot().heldBytes,4);assert.ok(!f.frees.includes(data));
  f.packets.Up(clone);assert.ok(f.frees.includes(data));assert.equal(f.controller.snapshot().heldBytes,0);
});
test('picture throwing after hook is non-EOF, real cleanup retires bound data',bounded,async()=>{
  const f=fixture('picture');f.afterThrows=true;
  await assert.rejects(f.run,e=>e.code==='EIO_AVCC_NATIVE_HEADER_PICTURE');
  assert.equal(f.controller.snapshot().heldBytes,0);assert.ok(!f.events.includes('release'));
});
test('real readHeader maps picture hook exception to LR even when IO error is zero',bounded,async()=>{
  const f=fixture('picture');f.refuse=true;
  const ids=[440786851,408125543,423732329,524531317];
  f.modules[27228]={aL:async()=>ids.shift(),Qb:async()=>10n,
    Pb:async(_fc,_size,syntax)=>syntax==='attachments'?f.format.context.attachments:{docType:'matroska',maxIdLength:4,maxSizeLength:8},
    qp:'attachments',jt:'header'};
  const out={},req=id=>f.modules[id]||{};req.r=()=>{};
  req.d=(o,defs)=>{for(const k in defs)Object.defineProperty(o,k,{get:defs[k]});};
  factory(matroska,25248)({},out,req);
  const format=new out.default();format.context={header:{maxIdLength:4,maxSizeLength:8},isLive:true,firstCluster:-1n};
  f.fc.ioReader={error:0,getPos:()=>1n,seek:async()=>{}};
  assert.equal(await format.readHeader(f.fc),-1094995529);
  assert.equal(f.fc.eioDualCacheError.code,'EIO_AVCC_NATIVE_HEADER_PICTURE');assert.equal(f.copies.length,0);
});

test('attachments await serially and retain imported packet reads/writes plus metadata scope',bounded,async()=>{
  const f=fixture('picture');let resolve;f.wait=new Promise(r=>resolve=r);
  const second={index:1,codecpar:{pointer:512},metadata:{},timeBase:{num:1,den:1000},disposition:0};
  Object.defineProperties(second.codecpar,{
    extradata:{get:()=>f.reads[20](524),set:v=>f.writes[20](524,v)},
    extradataSize:{get:()=>f.reads[15](528),set:v=>f.writes[15](528,v)}});
  f.format.context.attachments.entry.push({name:'font',mime:'font/ttf',description:'font fixture',data:{size:4n,data:f.payload}});
  f.format.context.tags={entry:[{tag:[{name:'COMMENT',string:'synthetic comment'}]}]};
  f.format.context.info={muxingApp:'synthetic mux',title:'fixture'};
  let created=0;f.fc.createStream=()=>++created===1?f.stream:second;
  const result=f.run();await new Promise(r=>setImmediate(r));
  assert.equal(created,1,'second stream not created while first admission pending');assert.equal(f.copies.length,0);
  resolve();await result;assert.equal(created,2);
  assert.equal(f.reads[15](f.stream.attachedPic+32),0);assert.equal(f.reads[15](f.stream.attachedPic+36)&1,1);
  assert.deepEqual(f.fc.metadata,{COMMENT:'synthetic comment',encoder:'synthetic mux',title:'fixture'});
  assert.deepEqual(second.metadata,{title:'font',mime:'font/ttf',description:'font fixture'});
  assert.equal(f.controller.snapshot().heldBytes,8);
  f.packets.Up(f.stream.attachedPic);assert.equal(f.controller.snapshot().heldBytes,4);
  f.slots.dn(512);assert.equal(f.controller.snapshot().heldBytes,0);
});
