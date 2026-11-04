'use strict';
// Real shipped slot, codec-box parser and repeated-moov P factories; synthetic IO/memory only.
const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const vm=require('node:vm');
const ownership=require('../web-src/player-shared-media-ownership.js');
const resources=require('../web-src/player-owned-media-resources.js');
const mediaHandles=require('../web-src/player-media-resource-handles.js');
function factory(file,id,next){
  const source=fs.readFileSync('public/vendor/web/libmedia/'+file,'utf8');
  const start=source.indexOf(id+':'),end=source.indexOf('},'+next+':',start);
  assert.ok(start>=0&&end>start);
  return vm.runInNewContext('('+source.slice(start+String(id).length+1,end+1)+')');
}
const slotFactory=factory('avplayer.js',67669,67994);
const moovFactory=factory('943.avplayer.js',8732,9581);
const codecs=[
  {id:173,parser:factory('943.avplayer.js',57820,58600),label:'mp4-hevc-hvcc-native'},
  {id:225,parser:factory('943.avplayer.js',93423,94652),label:'mp4-av1-av1c-native'}
];
function fixture(codec){
  const bytes=new Uint8Array(65536),view=new DataView(bytes.buffer),reads={},writes={};
  for(const type of [8,15,20,25]){
    reads[type]=p=>view.getUint32(p,true);
    writes[type]=(p,v)=>view.setUint32(p,v,true);
  }
  reads[17]=p=>view.getBigInt64(p,true);
  writes[17]=(p,v)=>view.setBigInt64(p,v,true);
  let bump=4096;
  const f={bytes,allocs:[],frees:[],claims:[],readCount:0};
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
    34117:{z3:()=>{}},12062:{XC:()=>{}},47354:{XC:()=>{}},54568:{o9:'pointer'},90213:mediaHandles
  };
  function load(fn){
    const out={},req=id=>{assert.ok(modules[id],`module ${id}`);return modules[id];};
    req.d=(out,defs)=>{for(const key in defs)Object.defineProperty(out,key,{get:defs[key]});};
    fn({},out,req);return out;
  }
  function cp(pointer){
    const result={pointer};
    Object.defineProperties(result,{
      extradata:{get:()=>reads[20](pointer+12),set:v=>writes[20](pointer+12,v)},
      extradataSize:{get:()=>reads[15](pointer+16),set:v=>writes[15](pointer+16,v)}
    });
    return result;
  }
  f.cp=cp(256);f.load=load;f.modules=modules;f.makeCp=cp;
  f.slots=modules[67669]=load(slotFactory);f.parse=load(codec.parser).A;
  f.fc={eioAcquireOwnedResource:async(size,label)=>{
    if(f.refuse)return {ok:false};
    const result=owned.acquire(size,label,7);
    f.claims.push({size,label});
    return {...result,generation:7,isCancelled:()=>!!f.cancelled};
  }};
  f.reader=(options={})=>{
    let pos=0n;
    return {getPos:()=>pos,skip:async n=>{pos+=BigInt(n);},
      readUint32:async()=>{pos+=4n;return options.words.shift();},
      readBuffer:async(n,target)=>{
        f.readCount++;
        if(options.io)await options.io();
        if(options.ioError)throw Error('IO failed');
        target.set(options.payload||[1,2,3,4]);pos+=BigInt(n);
        if(options.sliceError)target.slice=()=>{throw Error('slice failed');};
        return target;
      }};
  };
  f.run=(options={})=>f.parse(f.reader(options),{codecpar:f.cp,sideData:options.sideData||{}},
    {size:4},{eioFormatContext:f.fc,foundMoov:!!options.temp});
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
    f.fc={eioBeforeSampleAlloc:()=>{}};
    await assert.rejects(f.run(),/hook missing/);
    assert.equal(f.allocs.length,0);assert.equal(f.readCount,0);
  });
  test(`${codec.id}: exact published bytes credit only after real dn free`,async()=>{
    const f=fixture(codec);await f.run();
    assert.deepEqual(f.claims,[{size:4,label:codec.label}]);
    assert.equal(f.allocs[0].n,4);assert.equal(f.cp.codecId,codec.id);
    assert.equal(f.snapshot().heldBytes,4);
    const data=f.cp.extradata;
    assert.deepEqual([...f.bytes.subarray(data,data+4)],[1,2,3,4]);
    f.slots.dn(256);
    assert.deepEqual(f.frees,[data,256]);assert.equal(f.snapshot().heldBytes,0);
    assert.equal(f.snapshot().countOwnedResources,0);
  });
  test(`${codec.id}: IO failure and cancellation preserve previous slot`,async()=>{
    const f=fixture(codec);await f.run();const old=f.cp.extradata;
    await assert.rejects(f.run({ioError:true}),error=>error.code==='EIO_AVCC_NATIVE_FAILURE');
    assert.equal(f.cp.extradata,old);assert.equal(f.snapshot().heldBytes,4);
    assert.ok(!f.frees.includes(old));
    await assert.rejects(f.run({io:()=>{f.cancelled=true;}}),/CANCELLED/);
    assert.equal(f.cp.extradata,old);assert.equal(f.snapshot().heldBytes,4);
    assert.equal(f.frees.length,2);
    f.cancelled=false;await f.run();
    assert.ok(f.frees.includes(old));assert.equal(f.snapshot().heldBytes,4);
    f.slots.dn(256);assert.equal(f.snapshot().heldBytes,0);
  });
  test(`${codec.id}: failed malloc discards unused claim without read`,async()=>{
    const f=fixture(codec);f.mallocFail=true;
    await assert.rejects(f.run(),/ALLOC_FAILED/);
    assert.equal(f.readCount,0);assert.equal(f.snapshot().heldBytes,0);
  });
  test(`${codec.id}: foundMoov temporary cleans on copy success and exception`,async()=>{
    const f=fixture(codec),sideData={};await f.run({temp:true,sideData});
    assert.deepEqual([...sideData[1]],[1,2,3,4]);assert.equal(f.cp.extradata,0);
    await assert.rejects(f.run({temp:true,sliceError:true}),/slice failed/);
    assert.equal(f.frees.length,2);assert.equal(f.snapshot().heldBytes,0);
  });
  test(`${codec.id}: actual P uses real parser temp then replaces tracked slot`,async()=>{
    const f=fixture(codec);await f.run();const old=f.cp.extradata,P=f.P();
    const existing={index:0,codecpar:f.cp,privData:{trackId:9},sideData:{}};
    Object.assign(f.fc,{streams:[existing],streamIndex:1,
      createStream(){const s={index:1,codecpar:f.makeCp(512),sideData:{}};this.streams.push(s);return s;},
      removeStream(s){this.streams=this.streams.filter(item=>item!==s);}});
    const run=()=>P(f.reader({words:[20,1,12,123],payload:[9,8,7,6]}),f.fc,
      {eioFormatContext:f.fc,foundMoov:true,fragment:true},{size:20});
    await run();
    assert.notEqual(f.cp.extradata,old);assert.ok(f.frees.includes(old));
    assert.equal(f.snapshot().heldBytes,4);assert.equal(f.fc.streams.length,1);
    assert.deepEqual(f.claims.map(c=>c.label),[codec.label,codec.label,codec.label]);
    assert.deepEqual([...f.bytes.subarray(f.cp.extradata,f.cp.extradata+4)],[9,8,7,6]);
    const replacement=f.cp.extradata;
    // Admit parser temp but refuse P's persistent replacement, retaining old native slot.
    const acquire=f.fc.eioAcquireOwnedResource;
    let calls=0;f.fc.eioAcquireOwnedResource=(...args)=>++calls===2?{ok:false}:acquire(...args);
    await assert.rejects(run(),/admission refused/);
    assert.equal(f.cp.extradata,replacement);assert.ok(!f.frees.includes(replacement));
    assert.equal(f.snapshot().heldBytes,4);
    f.slots.dn(256);assert.equal(f.snapshot().heldBytes,0);
  });
}
