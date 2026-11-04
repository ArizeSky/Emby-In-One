'use strict';
// Shipped factories only: real slots, box parsers, codec readers and repeated-moov P.
// No candidate source or environment overlay is loaded by this test.
const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const vm=require('node:vm');
const ownership=require('../web-src/player-shared-media-ownership.js');
const resources=require('../web-src/player-owned-media-resources.js');
const mediaHandles=require('../web-src/player-media-resource-handles.js');
function factory(file,id){
  const source=fs.readFileSync('public/vendor/web/libmedia/'+file,'utf8');
  const start=source.indexOf(id+':');
  const match=/},\d+:/.exec(source.slice(start));
  const end=match?start+match.index:source.indexOf('}},r={}',start);
  assert.ok(start>=0&&end>start);
  return vm.runInNewContext('('+source.slice(start+String(id).length+1,end+1)+')',{Uint8Array,DataView});
}
const slotFactory=factory('avplayer.js',67669);
const moovFactory=factory('943.avplayer.js',8732);
const codecs=[
  {id:196,box:32790,next:35218,codec:25721,label:'mp4-vvc-vvcc-native',
    payload:[248,0,0,0,0,0],header:[0,0,0,0]},
  {id:167,box:89204,next:90642,codec:9581,label:'mp4-vp9-vpcc-native',
    payload:[2,31,0x82,1,2,3],header:[0,0,0,0]},
  {id:86028,box:23557,next:23716,codec:73448,label:'mp4-flac-dfla-native',
    payload:[...Array(10).fill(0),0x0b,0xb8,0x02,0xf0,...Array(20).fill(0)],
    header:[0,0,0,0,0x80,0,0,34]}
];
for(const codec of codecs){
  codec.parser=factory('943.avplayer.js',codec.box);
  codec.size=codec.header.length+codec.payload.length;
}
function fixture(codec){
  const bytes=new Uint8Array(65536),view=new DataView(bytes.buffer),reads={},writes={};
  for(const type of [8,15,20,25]){
    reads[type]=p=>view.getUint32(p,true);
    writes[type]=(p,v)=>view.setUint32(p,v,true);
  }
  reads[17]=p=>view.getBigInt64(p,true);
  writes[17]=(p,v)=>view.setBigInt64(p,v,true);
  let bump=4096;
  const f={bytes,allocs:[],frees:[],claims:[],readCount:0,metadataReads:0,skips:[],readSizes:[]};
  const controller=ownership.create({capacityBytes:1024,maxEntries:8});
  const owned=resources.createOwnedResources(controller);
  f.snapshot=()=>controller.snapshot();
  function free(p){f.frees.push(p);}
  const modules={
    80209:{JW:(p,n)=>bytes.subarray(p,p+n),lW:(p,n,b)=>bytes.set(b,p),sO:(p,v,n)=>bytes.fill(v,p,p+n)},
    56238:{f:reads},6927:{M:writes},
    46626:{sY:n=>{if(f.mallocFail)return 0;const p=bump;bump+=n+16;f.allocs.push({p,n});return p;},
      Eb:free,aI:p=>{free(reads[20](p));writes[20](p,0);}},
    57216:{bl:()=>{}},46564:{NZ:()=>{}},29688:{N_:-1},
    34117:{z3:()=>{},R8:()=>{}},54568:{o9:'pointer'},90213:mediaHandles
  };
  function load(fn){
    const out={},req=id=>{assert.ok(modules[id],`module ${id}`);return modules[id];};
    req.d=(out,defs)=>{for(const key in defs)Object.defineProperty(out,key,{get:defs[key]});};
    fn({},out,req);return out;
  }
  function cp(pointer){
    const result={pointer,chLayout:{}};
    Object.defineProperties(result,{
      extradata:{get:()=>reads[20](pointer+12),set:v=>writes[20](pointer+12,v)},
      extradataSize:{get:()=>reads[15](pointer+16),set:v=>writes[15](pointer+16,v)}
    });
    return result;
  }
  f.cp=cp(256);f.load=load;f.modules=modules;f.makeCp=cp;
  f.slots=modules[67669]=load(slotFactory);
  // Load genuine codec XC and its reader dependencies; unused exports remain dormant.
  for(const id of [65564,23226,64347,83796,22105,25979])modules[id]={};
  modules[19371]={A:(o,k,v)=>{o[k]=v;}};modules[18528]={};
  modules[41662]={encode:s=>new TextEncoder().encode(s)};
  modules[70704]=load(factory('avplayer.js',70704));
  modules[99248]=load(factory('avplayer.js',99248));
  modules[54809]=load(factory('avplayer.js',54809));
  modules[99817]=load(factory('avplayer.js',99817));
  modules[25721]=load(factory('avplayer.js',25721));
  modules[9581]=load(factory('avplayer.js',9581));
  modules[73448]=load(factory('943.avplayer.js',73448));
  f.parse=load(codec.parser).A;
  f.fc={eioAcquireOwnedResource:async(size,label)=>{
    f.acquireMetadata=f.metadataReads;
    if(f.refuse)return {ok:false};
    const result=owned.acquire(size,label,7);
    f.claims.push({size,label});
    return {...result,generation:7,isCancelled:()=>!!f.cancelled};
  }};
  f.reader=(options={})=>{
    let pos=0n;const input=Uint8Array.from(options.input||[...(options.words?Array(16).fill(0):[]),...codec.header,...(options.payload||codec.payload)]);
    return {getPos:()=>pos,skip:async n=>{if(options.tailError&&pos>=BigInt(codec.size))throw Error('tail skip failed');f.skips.push(n);pos+=BigInt(n);},
      readUint8:async()=>{f.metadataReads++;return input[Number(pos++)];},
      readUint24:async()=>{f.metadataReads++;const p=Number(pos);pos+=3n;
        return input[p]*65536+input[p+1]*256+input[p+2];},
      readUint32:async()=>{pos+=4n;return options.words.shift();},
      readBuffer:async(n,target)=>{
        assert.equal(f.allocs.at(-1).n,n);
        if(f.fc.eioAcquireOwnedResource)assert.ok(f.claims.length>0,'native read follows acquisition');
        f.readCount++;f.readSizes.push(n);
        if(options.io)await options.io();
        if(options.ioError)throw Error('IO failed');
        target.set(input.subarray(Number(pos),Number(pos)+n));pos+=BigInt(n);
        if(options.sliceError)target.slice=()=>{throw Error('slice failed');};
        return target;
      }};
  };
  f.run=(options={})=>f.parse(f.reader(options),{codecpar:f.cp,sideData:options.sideData||{}},
    {size:options.size===undefined?codec.size:options.size},{eioFormatContext:f.fc,foundMoov:!!options.temp});
  f.P=()=>{
    for(const id of [23716,8332,59436,23905,28320,3031,66293,52617,23902,87114,86299,35924,85626])modules[id]={};
    modules[37766]={A:tag=>tag==='trak'?1:2};
    modules[78215]={A:()=>({trackId:9})};
    // B dispatches the actual codec parser, which creates and discards its real temporary claim.
    modules[97238]={A:{123:f.parse}};
    return load(moovFactory).wB;
  };
  return f;
}
for(const codec of codecs){
  test(`${codec.id}: admission refuses before malloc/read, including missing hooks`,async()=>{
    const f=fixture(codec);f.refuse=true;
    await assert.rejects(f.run(),/admission refused/);
    assert.equal(f.allocs.length,0);assert.equal(f.readCount,0);
    assert.equal(f.metadataReads,codec.id===86028?2:0);
    f.fc={eioBeforeSampleAlloc:()=>{}};
    await assert.rejects(f.run(),/hook missing/);
    assert.equal(f.allocs.length,0);assert.equal(f.readCount,0);
  });
  test(`${codec.id}: exact published bytes credit only after real dn free`,async()=>{
    const f=fixture(codec);await f.run();
    assert.deepEqual(f.claims,[{size:codec.payload.length,label:codec.label}]);
    assert.equal(f.allocs[0].n,codec.payload.length);assert.equal(f.cp.codecId,codec.id);
    assert.deepEqual(f.readSizes,[codec.payload.length]);
    assert.equal(f.acquireMetadata,codec.id===86028?2:0);
    if(codec.id===167){assert.equal(f.cp.profile,2);assert.equal(f.cp.level,31);}
    if(codec.id===86028){assert.equal(f.cp.sampleRate,48000);assert.equal(f.cp.bitsPerRawSample,16);
      assert.equal(f.cp.chLayout.nbChannels,2);}
    assert.equal(f.snapshot().heldBytes,codec.payload.length);
    const data=f.cp.extradata;
    assert.deepEqual([...f.bytes.subarray(data,data+codec.payload.length)],codec.payload);
    f.slots.dn(256);
    assert.deepEqual(f.frees,[data,256]);assert.equal(f.snapshot().heldBytes,0);
    assert.equal(f.snapshot().countOwnedResources,0);
  });
  test(`${codec.id}: IO failure and cancellation preserve previous slot`,async()=>{
    const f=fixture(codec);await f.run();const old=f.cp.extradata;
    await assert.rejects(f.run({ioError:true}),error=>error.code==='EIO_AVCC_NATIVE_FAILURE');
    assert.equal(f.cp.extradata,old);assert.equal(f.snapshot().heldBytes,codec.payload.length);
    assert.ok(!f.frees.includes(old));
    await assert.rejects(f.run({io:()=>{f.cancelled=true;}}),/CANCELLED/);
    assert.equal(f.cp.extradata,old);assert.equal(f.snapshot().heldBytes,codec.payload.length);
    assert.equal(f.frees.length,2);
    f.cancelled=false;await f.run();
    assert.ok(f.frees.includes(old));assert.equal(f.snapshot().heldBytes,codec.payload.length);
    f.slots.dn(256);assert.equal(f.snapshot().heldBytes,0);
  });
  test(`${codec.id}: failed malloc discards unused claim without read`,async()=>{
    const f=fixture(codec);f.mallocFail=true;
    await assert.rejects(f.run(),/ALLOC_FAILED/);
    assert.equal(f.readCount,0);assert.equal(f.snapshot().heldBytes,0);
  });
  test(`${codec.id}: foundMoov temporary cleans on copy success and exception`,async()=>{
    const f=fixture(codec),sideData={};await f.run({temp:true,sideData});
    assert.deepEqual([...sideData[1]],codec.payload);assert.equal(f.cp.extradata,0);
    await assert.rejects(f.run({temp:true,sliceError:true}),/slice failed/);
    assert.equal(f.frees.length,2);assert.equal(f.snapshot().heldBytes,0);
  });
  test(`${codec.id}: actual P uses real parser temp then replaces tracked slot`,async()=>{
    const f=fixture(codec);await f.run();const old=f.cp.extradata,P=f.P();
    const existing={index:0,codecpar:f.cp,privData:{trackId:9},sideData:{}};
    Object.assign(f.fc,{streams:[existing],streamIndex:1,
      createStream(){const s={index:1,codecpar:f.makeCp(512),sideData:{}};this.streams.push(s);return s;},
      removeStream(s){this.streams=this.streams.filter(item=>item!==s);}});
    const run=()=>P(f.reader({words:[codec.size+16,1,codec.size+8,123]}),f.fc,
      {eioFormatContext:f.fc,foundMoov:true,fragment:true},{size:codec.size+16});
    await run();
    assert.notEqual(f.cp.extradata,old);assert.ok(f.frees.includes(old));
    assert.equal(f.snapshot().heldBytes,codec.payload.length);assert.equal(f.fc.streams.length,1);
    assert.deepEqual(f.claims.map(c=>c.label),[codec.label,codec.label,codec.label]);
    assert.deepEqual([...f.bytes.subarray(f.cp.extradata,f.cp.extradata+codec.payload.length)],codec.payload);
    const replacement=f.cp.extradata;
    // Admit parser temp but refuse P's persistent replacement, retaining old native slot.
    const acquire=f.fc.eioAcquireOwnedResource;
    let calls=0;f.fc.eioAcquireOwnedResource=(...args)=>++calls===2?{ok:false}:acquire(...args);
    await assert.rejects(run(),/admission refused/);
    assert.equal(f.cp.extradata,replacement);assert.ok(!f.frees.includes(replacement));
    assert.equal(f.snapshot().heldBytes,codec.payload.length);
    f.slots.dn(256);assert.equal(f.snapshot().heldBytes,0);
  });
}

test('fixed box prefix bounds precede all metadata IO and native admission',async()=>{
  for(const codec of codecs){
    for(const size of [codec.id===86028?7:4,NaN,4294967296]){
      const f=fixture(codec);
      await assert.rejects(f.run({size}),e=>String(e.code).startsWith('EIO_AVCC_'));
      assert.equal(f.metadataReads,0);assert.deepEqual(f.skips,[]);
      assert.equal(f.claims.length,0);assert.equal(f.allocs.length,0);assert.equal(f.readCount,0);
    }
  }
});
test('dfLa metadata length validation precedes payload acquisition; extra blocks remain skipped',async()=>{
  const codec=codecs[2],f=fixture(codec);
  await assert.rejects(f.run({size:41}),e=>String(e.code).startsWith('EIO_AVCC_'));
  assert.equal(f.metadataReads,2);assert.equal(f.claims.length,0);assert.equal(f.readCount,0);
  // Preserve legacy handling of an unsupported first block: log and skip, never allocate.
  const input=[0,0,0,0,0x81,0,0,34,...codec.payload];
  await f.run({input});assert.equal(f.allocs.length,0);assert.equal(f.readCount,0);
  const g=fixture(codec);await g.run({size:codec.size+3,input:[...codec.header,...codec.payload,7,8,9]});
  assert.equal(g.skips.at(-1),3);assert.deepEqual(g.readSizes,[34]);g.slots.dn(256);
  assert.equal(g.snapshot().heldBytes,0);
});
test('non-strict legacy conversion still invokes real codec readers without a JS ownership claim',async()=>{
  for(const codec of codecs){
    const f=fixture(codec);f.fc={};await f.run();const old=f.cp.extradata;
    await f.run();assert.ok(f.frees.includes(old));
    assert.deepEqual([...f.bytes.subarray(f.cp.extradata,f.cp.extradata+codec.payload.length)],codec.payload);
    const sideData={};await f.run({temp:true,sideData});
    assert.deepEqual([...sideData[1]],codec.payload);assert.deepEqual(f.claims,[]);
    f.slots.dn(256);assert.equal(f.snapshot().heldBytes,0);
  }
});

test('real VP9 codec reader failure discards new payload without replacing old native slot',async()=>{
  const f=fixture(codecs[1]);await f.run();const old=f.cp.extradata;
  Object.defineProperty(f.cp,'profile',{set(){throw Error('codec parse failed');},configurable:true});
  await assert.rejects(f.run(),e=>e.code==='EIO_AVCC_NATIVE_FAILURE');
  assert.equal(f.cp.extradata,old);assert.equal(f.cp.extradataSize,6);
  assert.ok(!f.frees.includes(old));assert.equal(f.frees.length,1);
  assert.equal(f.snapshot().heldBytes,6);f.slots.dn(256);
  assert.equal(f.snapshot().heldBytes,0);
});

test('dfLa trailing metadata skip failure retains old slot and frees only the new payload',async()=>{
  const f=fixture(codecs[2]);await f.run();const old=f.cp.extradata;
  await assert.rejects(f.run({size:45,tailError:true}),e=>e.code==='EIO_AVCC_NATIVE_FAILURE');
  assert.equal(f.cp.extradata,old);assert.ok(!f.frees.includes(old));
  assert.equal(f.frees.length,1);assert.equal(f.snapshot().heldBytes,34);
  f.slots.dn(256);assert.equal(f.snapshot().heldBytes,0);
});
