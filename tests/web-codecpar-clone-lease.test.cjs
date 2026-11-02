'use strict';
/* Simulated allocator/method-boundary tests for module 67669. Fake WASM memory and allocators only; not real BSF/MSE/global-cap proof. */
const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const vm=require('node:vm');
const path=require('node:path');
const source=fs.readFileSync(path.join(__dirname,'../public/vendor/web/libmedia/avplayer.js'),'utf8');

const ADDR=Object.freeze({dest:0x100,dest2:0x200,src:0x400,extra:0x800,sideTable:0x900,side1:0xa00,side2:0xb00});
const MEMORY_SIZE=0x20000;

function moduleFactoryText(text){
 const start=text.indexOf('67669:(e,t,i)=>{');
 assert.ok(start>=0,'module 67669 not found');
 const end=text.indexOf('},67994:',start);
 assert.ok(end>start,'module 67669 end not found');
 /* end+1 keeps the factory body's own closing brace: the matched '},' is the end of the arrow-function body, so excluding it yields an unbalanced factory. */
 return text.slice(start+'67669:'.length,end+1);
}

function makeMemory(){
 const buffer=new Uint8Array(MEMORY_SIZE);
 const view=new DataView(buffer.buffer);
 let bump=0x3000;
 let allocCalls=0;
 const mem={
  buffer,view,reads:[],writes:[],allocs:[],frees:[],failedFrees:[],copies:[],memsets:[],
  failAllocNumbers:new Set(),throwFreeFor:new Set(),freeError:null,
  reset(){this.reads.length=0;this.writes.length=0;this.allocs.length=0;this.frees.length=0;this.failedFrees.length=0;this.copies.length=0;this.memsets.length=0;},
  read(ptr,size){if(!Number.isInteger(ptr)||ptr<0||!Number.isInteger(size)||size<0||ptr+size>MEMORY_SIZE)throw new RangeError('fake memory read out of range');this.reads.push({ptr,size});},
  write(ptr,size){if(!Number.isInteger(ptr)||ptr<0||!Number.isInteger(size)||size<0||ptr+size>MEMORY_SIZE)throw new RangeError('fake memory write out of range');this.writes.push({ptr,size});},
  getI8(ptr){this.read(ptr,1);return view.getInt8(ptr);},
  getI32(ptr){this.read(ptr,4);return view.getInt32(ptr,true);},
  getU32(ptr){this.read(ptr,4);return view.getUint32(ptr,true);},
  getBigInt64(ptr){this.read(ptr,8);return view.getBigInt64(ptr,true);},
  setI8(ptr,value){this.write(ptr,1);view.setInt8(ptr,value);},
  setI32(ptr,value){this.write(ptr,4);view.setInt32(ptr,value,true);},
  setU32(ptr,value){this.write(ptr,4);view.setUint32(ptr,value,true);},
  setBigInt64(ptr,value){this.write(ptr,8);view.setBigInt64(ptr,value,true);},
  copy(dest,src,size){this.read(src,size);this.write(dest,size);buffer.copyWithin(dest,src,src+size);this.copies.push({dest,src,size});},
  memset(ptr,value,size){this.write(ptr,size);buffer.fill(value,ptr,ptr+size);this.memsets.push({ptr,value,size});},
  allocate(size,how){allocCalls+=1;if(this.failAllocNumbers.has(allocCalls)){this.allocs.push({ptr:0,size,how,failed:true});return 0;}const ptr=bump;bump+=Math.max(1,size);if(bump>MEMORY_SIZE)throw new RangeError('fake allocator exhausted');this.allocs.push({ptr,size,how});if(how==='calloc')buffer.fill(0,ptr,ptr+size);return ptr;},
  free(ptr){if(!ptr)throw new Error('free(0)');if(this.throwFreeFor.has(ptr)){this.failedFrees.push(ptr);throw this.freeError;}this.frees.push(ptr);},
  pokeI8(ptr,value){view.setInt8(ptr,value);},
  pokeI32(ptr,value){view.setInt32(ptr,value,true);},
  pokeU32(ptr,value){view.setUint32(ptr,value,true);},
  pokeBigInt64(ptr,value){view.setBigInt64(ptr,value,true);},
  pokeBytes(ptr,bytes){buffer.set(bytes,ptr);},
  bytes(ptr,size){return Array.from(buffer.slice(ptr,ptr+size));}
 };
 return mem;
}

function loadModule67669(memory){
 const factoryText=moduleFactoryText(source);
 const getters={8:ptr=>memory.getI8(ptr),15:ptr=>memory.getI32(ptr),17:ptr=>memory.getBigInt64(ptr),20:ptr=>memory.getU32(ptr),25:ptr=>memory.getU32(ptr)};
 const setters={8:(ptr,value)=>memory.setI8(ptr,value),15:(ptr,value)=>memory.setI32(ptr,value),17:(ptr,value)=>memory.setBigInt64(ptr,value),20:(ptr,value)=>memory.setU32(ptr,value),25:(ptr,value)=>memory.setU32(ptr,value)};
 const requireStub=id=>{
  if(id===80209)return{Mr:(dest,src,size)=>memory.copy(dest,src,size),sO:(ptr,value,size)=>memory.memset(ptr,value,size)};
  if(id===56238)return{f:getters};
  if(id===6927)return{M:setters};
  if(id===46626)return{Eb:ptr=>memory.free(ptr),sY:size=>memory.allocate(size,'malloc'),Gy:size=>memory.allocate(size,'calloc'),aI:ptr=>{const value=memory.getU32(ptr);memory.free(value);memory.setU32(ptr,0);}};
  if(id===57216)return{bl:()=>{}};
  if(id===46564)return{NZ:(ptr,countPtr)=>{const table=memory.getU32(ptr);const count=memory.getI32(countPtr);if(table){for(let index=0;index<count;index++)memory.free(memory.getU32(table+12*index));memory.free(table);}memory.setU32(ptr,0);memory.setI32(countPtr,0);}};
  if(id===29688)return{N_:0};
  throw new Error('unexpected module id '+id);
 };
 requireStub.d=(target,definitions)=>{for(const key of Object.keys(definitions))target[key]=definitions[key]();};
 const factory=vm.runInNewContext('('+factoryText+')',{});
 const exports={};
 factory({},exports,requireStub);
 return exports;
}

function setupSource(memory,options={}){
 const extra=options.extra===undefined?[1,2,3]:options.extra;
 const sides=options.sides===undefined?[{bytes:[4,5],type:2},{bytes:[6,7,8,9],type:3}]:options.sides;
 memory.pokeI32(ADDR.src,7);
 memory.pokeI32(ADDR.src+4,8);
 memory.pokeI8(ADDR.src+8,9);
 memory.pokeI32(ADDR.src+28,11);
 memory.pokeBigInt64(ADDR.src+32,12n);
 memory.pokeI32(ADDR.src+40,13);
 memory.pokeI32(ADDR.src+136,14);
 const extraPtr=options.extradataPointer===undefined?(extra?ADDR.extra:0):options.extradataPointer;
 const extraSize=options.extradataSize===undefined?(extra?extra.length:0):options.extradataSize;
 memory.pokeU32(ADDR.src+12,extraPtr);
 memory.pokeI32(ADDR.src+16,extraSize);
 if(extra)memory.pokeBytes(ADDR.extra,extra);
 const sidePtr=options.sidePointer===undefined?(sides.length?ADDR.sideTable:0):options.sidePointer;
 const sideCount=options.sideCount===undefined?sides.length:options.sideCount;
 memory.pokeU32(ADDR.src+20,sidePtr);
 memory.pokeI32(ADDR.src+24,sideCount);
 const payloadRanges=[];
 if(extra&&extra.length)payloadRanges.push([ADDR.extra,ADDR.extra+extra.length]);
 sides.forEach((side,index)=>{
  const entry=ADDR.sideTable+12*index;
  const payload=ADDR.side1+index*0x100;
  memory.pokeU32(entry,payload);
  memory.pokeU32(entry+4,side.bytes.length);
  memory.pokeI32(entry+8,side.type);
  memory.pokeBytes(payload,side.bytes);
  if(side.bytes.length)payloadRanges.push([payload,payload+side.bytes.length]);
 });
 const bytes=(extra?extra.length:0)+sides.reduce((sum,side)=>sum+side.bytes.length,0);
 return{extra,sides,extraPtr,extraSize,sidePtr,sideCount,payloadRanges,bytes};
}

function makeLease(bytes,options={}){
 const calls={bind:[],release:[],retain:[]};
 const lease={
  bytes,
  bind(value){calls.bind.push(value);if(options.bindThrow)throw options.bindThrow;return options.bindResult===undefined?true:options.bindResult;},
  release(value){calls.release.push(value);if(options.releaseThrow)throw options.releaseThrow;return options.releaseResult===undefined?true:options.releaseResult;},
  retainFailure(value){calls.retain.push(value);if(options.retainThrow)throw options.retainThrow;}
 };
 if(options.mutate)options.mutate(lease);
 return{lease,calls};
}

function expectCode(fn,code){
 try{fn();}catch(error){assert.equal(error.code,code);return error;}
 assert.fail('expected error code '+code);
}

function assertNoPayloadReads(memory,startIndex,payloadRanges){
 for(const access of memory.reads.slice(startIndex)){
  for(const [start,end] of payloadRanges)assert.ok(access.ptr+access.size<=start||access.ptr>=end,'payload read '+JSON.stringify(access));
 }
}

function assertDestinationSideData(memory,fixture,dest=ADDR.dest){
 const table=memory.getU32(dest+20);
 assert.ok(table>0,'side table copied');
 assert.equal(memory.getI32(dest+24),fixture.sides.length);
 fixture.sides.forEach((side,index)=>{
  const entry=table+12*index;
  const payload=memory.getU32(entry);
  assert.ok(payload>0,'side payload copied');
  assert.equal(memory.getU32(entry+4),side.bytes.length);
  assert.equal(memory.getI32(entry+8),side.type);
  assert.deepEqual(memory.bytes(payload,side.bytes.length),side.bytes);
 });
}

test('baseline native clone/free remain unchanged and never call lease callbacks',()=>{
 const memory=makeMemory();
 const api=loadModule67669(memory);
 const fixture=setupSource(memory);
 api.Yi(ADDR.dest,ADDR.src);
 assert.equal(memory.getI32(ADDR.dest),7);
 assert.equal(memory.getI32(ADDR.dest+4),8);
 assert.equal(memory.getI8(ADDR.dest+8),9);
 assert.equal(memory.getI32(ADDR.dest+28),11);
 assert.equal(memory.getBigInt64(ADDR.dest+32),12n);
 const extra=memory.getU32(ADDR.dest+12);
 assert.ok(extra>0);
 assert.equal(memory.getI32(ADDR.dest+16),fixture.extra.length);
 assert.deepEqual(memory.bytes(extra,fixture.extra.length),fixture.extra);
 assertDestinationSideData(memory,fixture);
 assert.equal(memory.frees.length,0);
 assert.equal(memory.allocs.length,4);
 api.dn(ADDR.dest);
 assert.equal(memory.getU32(ADDR.dest+12),0);
 assert.equal(memory.getU32(ADDR.dest+20),0);
 assert.equal(memory.getI32(ADDR.dest+24),0);
 assert.deepEqual(Array.from(memory.frees),Array.from([extra,memory.allocs[2].ptr,memory.allocs[3].ptr,memory.allocs[1].ptr,ADDR.dest]));
});

test('optional undefined lease is byte-for-byte compatible with baseline',()=>{
 const memory=makeMemory();
 const api=loadModule67669(memory);
 const fixture=setupSource(memory);
 const spy=makeLease(fixture.bytes);
 api.Yi(ADDR.dest2,ADDR.src,undefined);
 assert.equal(spy.calls.bind.length,0);
 assert.equal(spy.calls.release.length,0);
 assert.equal(spy.calls.retain.length,0);
 const extra=memory.getU32(ADDR.dest2+12);
 assert.ok(extra>0);
 assert.equal(memory.getI32(ADDR.dest2+16),fixture.extra.length);
 assert.deepEqual(memory.bytes(extra,fixture.extra.length),fixture.extra);
 assertDestinationSideData(memory,fixture,ADDR.dest2);
 assert.equal(memory.frees.length,0);
 api.dn(ADDR.dest2);
});

test('valid exact extradata plus two side payloads binds once and releases only after original free',()=>{
 const memory=makeMemory();
 const api=loadModule67669(memory);
 const fixture=setupSource(memory);
 assert.equal(fixture.bytes,9);
 const {lease,calls}=makeLease(fixture.bytes);
 let freesAtRelease=-1;
 const release=lease.release;
 lease.release=function(value){freesAtRelease=memory.frees.length;return release.call(this,value);};
 api.Yi(ADDR.dest,ADDR.src,lease);
 assert.equal(calls.bind.length,1);
 assert.deepEqual(Array.from(Object.keys(calls.bind[0])),['copiedBytes']);
 assert.equal(calls.bind[0].copiedBytes,9);
 assert.equal(calls.release.length,0);
 assert.equal(calls.retain.length,0);
 assert.equal(memory.frees.length,0);
 const extra=memory.getU32(ADDR.dest+12);
 assert.equal(memory.getI32(ADDR.dest+16),fixture.extra.length);
 assert.deepEqual(memory.bytes(extra,fixture.extra.length),fixture.extra);
 assertDestinationSideData(memory,fixture);
 api.dn(ADDR.dest);
 assert.equal(calls.release.length,1);
 assert.equal(freesAtRelease,5);
 assert.deepEqual(Array.from(Object.keys(calls.release[0])),['referencesDropped']);
 assert.equal(calls.release[0].referencesDropped,true);
 assert.ok(memory.frees.includes(extra));
 assert.ok(memory.frees.includes(ADDR.dest));
 assert.equal(memory.frees.length,5);
});

test('wrong lease size rejects before destination writes, allocations and payload reads',()=>{
 const memory=makeMemory();
 const api=loadModule67669(memory);
 const fixture=setupSource(memory);
 const {lease}=makeLease(fixture.bytes+1);
 const writes=memory.writes.length;
 const allocs=memory.allocs.length;
 const copies=memory.copies.length;
 const reads=memory.reads.length;
 expectCode(()=>api.Yi(ADDR.dest,ADDR.src,lease),'EIO_CODECPAR_LEASE_SIZE_MISMATCH');
 assert.equal(memory.writes.length,writes);
 assert.equal(memory.allocs.length,allocs);
 assert.equal(memory.copies.length,copies);
 assertNoPayloadReads(memory,reads,fixture.payloadRanges);
});

test('malformed leases reject before mutation or allocation',()=>{
 const memory=makeMemory();
 const api=loadModule67669(memory);
 setupSource(memory);
 const badLeases=[
  null,
  {},
  {bytes:0,bind(){},release(){}},
  {bytes:-1,bind(){},release(){},retainFailure(){}},
  {bytes:1.5,bind(){},release(){},retainFailure(){}},
  {bytes:9,bind:'bind',release(){},retainFailure(){}},
  {bytes:9,bind(){},release:null,retainFailure(){}},
  {bytes:9,bind(){},release(){},retainFailure:0}
 ];
 for(const lease of badLeases){
  const writes=memory.writes.length;
  const allocs=memory.allocs.length;
  expectCode(()=>api.Yi(ADDR.dest,ADDR.src,lease),'EIO_CODECPAR_LEASE_INVALID');
  assert.equal(memory.writes.length,writes);
  assert.equal(memory.allocs.length,allocs);
 }
});

test('dirty destination extradata pointer, extradata size, side pointer or side count rejects before writes and allocation',()=>{
 for(const field of [12,16,20,24]){
  const memory=makeMemory();
  const api=loadModule67669(memory);
  const fixture=setupSource(memory);
  if(field===24)memory.pokeI32(ADDR.dest+field,1);
  else memory.pokeU32(ADDR.dest+field,0x7000);
  const {lease}=makeLease(fixture.bytes);
  const writes=memory.writes.length;
  const allocs=memory.allocs.length;
  expectCode(()=>api.Yi(ADDR.dest,ADDR.src,lease),'EIO_CODECPAR_LEASE_INVALID');
  assert.equal(memory.writes.length,writes);
  assert.equal(memory.allocs.length,allocs);
 }
});

test('registered destination rejects both leased and baseline replacement before writes and allocation',()=>{
 const memory=makeMemory();
 const api=loadModule67669(memory);
 const fixture=setupSource(memory);
 const first=makeLease(fixture.bytes);
 api.Yi(ADDR.dest,ADDR.src,first.lease);
 const writes=memory.writes.length;
 const allocs=memory.allocs.length;
 const second=makeLease(fixture.bytes);
 expectCode(()=>api.Yi(ADDR.dest,ADDR.src,second.lease),'EIO_CODECPAR_LEASE_DEST_BUSY');
 expectCode(()=>api.Yi(ADDR.dest,ADDR.src),'EIO_CODECPAR_LEASE_DEST_BUSY');
 assert.equal(memory.writes.length,writes);
 assert.equal(memory.allocs.length,allocs);
 assert.equal(second.calls.bind.length,0);
 api.dn(ADDR.dest);
 assert.equal(first.calls.release.length,1);
});

test('every explicit allocation failure retains the clone and prevents free or credit',()=>{
 for(const failAt of [1,2,3,4]){
  const memory=makeMemory();
  memory.failAllocNumbers.add(failAt);
  const api=loadModule67669(memory);
  const fixture=setupSource(memory);
  const {lease,calls}=makeLease(fixture.bytes);
  expectCode(()=>api.Yi(ADDR.dest,ADDR.src,lease),'EIO_CODECPAR_ALLOC_FAILED');
  assert.equal(calls.bind.length,0);
  assert.equal(calls.release.length,0);
  assert.equal(calls.retain.length,1);
  assert.equal(calls.retain[0].code,'EIO_CODECPAR_CLONE_UNPROVEN');
  assert.equal(memory.frees.length,0);
  expectCode(()=>api.dn(ADDR.dest),'EIO_CODECPAR_CLONE_UNPROVEN');
  assert.equal(memory.frees.length,0);
  assert.equal(calls.release.length,0);
  const allocs=memory.allocs.length;
  expectCode(()=>api.Yi(ADDR.dest,ADDR.src),'EIO_CODECPAR_LEASE_DEST_BUSY');
  assert.equal(memory.allocs.length,allocs);
 }
});

test('bind false or throw retains the clone and rethrows the required error',()=>{
 {
  const memory=makeMemory();
  const api=loadModule67669(memory);
  const fixture=setupSource(memory);
  const {lease,calls}=makeLease(fixture.bytes,{bindResult:false});
  expectCode(()=>api.Yi(ADDR.dest,ADDR.src,lease),'EIO_CODECPAR_LEASE_BIND_FAILED');
  assert.equal(calls.release.length,0);
  assert.equal(calls.retain.length,1);
  assert.equal(calls.retain[0].code,'EIO_CODECPAR_CLONE_UNPROVEN');
  assert.equal(memory.frees.length,0);
  expectCode(()=>api.dn(ADDR.dest),'EIO_CODECPAR_CLONE_UNPROVEN');
  assert.equal(memory.frees.length,0);
 }
 {
  const memory=makeMemory();
  const api=loadModule67669(memory);
  const fixture=setupSource(memory);
  const bindError=new Error('bind exploded');
  const {lease,calls}=makeLease(fixture.bytes,{bindThrow:bindError});
  const caught=expectCode(()=>api.Yi(ADDR.dest,ADDR.src,lease),undefined);
  assert.equal(caught,bindError);
  assert.equal(calls.release.length,0);
  assert.equal(calls.retain.length,1);
  assert.equal(calls.retain[0].code,'EIO_CODECPAR_CLONE_UNPROVEN');
  assert.equal(memory.frees.length,0);
  expectCode(()=>api.dn(ADDR.dest),'EIO_CODECPAR_CLONE_UNPROVEN');
  assert.equal(memory.frees.length,0);
 }
});

test('reentrant dn from bind is refused while the record is not yet bound and frees or releases nothing early',()=>{
 const memory=makeMemory();
 const api=loadModule67669(memory);
 const fixture=setupSource(memory);
 const {lease,calls}=makeLease(fixture.bytes,{mutate:target=>{
  target.bind=function(value){
   calls.bind.push(value);
   const freesBefore=memory.frees.length;
   let refusal=null;
   try{api.dn(ADDR.dest);}catch(error){refusal=error;}
   assert.ok(refusal,'reentrant dn is refused during bind');
   assert.equal(refusal.code,'EIO_CODECPAR_CLONE_UNPROVEN');
   assert.equal(calls.release.length,0);
   assert.equal(memory.frees.length,freesBefore);
   return true;
  };
 }});
 assert.equal(api.Yi(ADDR.dest,ADDR.src,lease),undefined);
 assert.equal(calls.bind.length,1);
 assert.deepEqual(Array.from(Object.keys(calls.bind[0])),['copiedBytes']);
 assert.equal(calls.bind[0].copiedBytes,fixture.bytes);
 assert.equal(calls.release.length,0);
 assert.equal(calls.retain.length,0);
 assert.equal(memory.frees.length,0);
 const extra=memory.getU32(ADDR.dest+12);
 assert.ok(extra>0);
 assert.equal(memory.getI32(ADDR.dest+16),fixture.extra.length);
 assert.deepEqual(memory.bytes(extra,fixture.extra.length),fixture.extra);
 assertDestinationSideData(memory,fixture);
 api.dn(ADDR.dest);
 assert.equal(calls.release.length,1);
 assert.deepEqual(Array.from(Object.keys(calls.release[0])),['referencesDropped']);
 assert.equal(calls.release[0].referencesDropped,true);
 assert.ok(memory.frees.includes(extra));
 assert.ok(memory.frees.includes(ADDR.dest));
 assert.equal(memory.frees.length,5);
});

test('reentrant dn inside a bind that returns false stays retained with no free, no release and no baseline bypass',()=>{
 const memory=makeMemory();
 const api=loadModule67669(memory);
 const fixture=setupSource(memory);
 const {lease,calls}=makeLease(fixture.bytes,{mutate:target=>{
  target.bind=function(value){
   calls.bind.push(value);
   const freesBefore=memory.frees.length;
   expectCode(()=>api.dn(ADDR.dest),'EIO_CODECPAR_CLONE_UNPROVEN');
   assert.equal(calls.release.length,0);
   assert.equal(memory.frees.length,freesBefore);
   return false;
  };
 }});
 expectCode(()=>api.Yi(ADDR.dest,ADDR.src,lease),'EIO_CODECPAR_LEASE_BIND_FAILED');
 assert.equal(calls.bind.length,1);
 assert.equal(calls.release.length,0);
 assert.equal(calls.retain.length,1);
 assert.equal(calls.retain[0].code,'EIO_CODECPAR_CLONE_UNPROVEN');
 assert.equal(memory.frees.length,0);
 const extra=memory.getU32(ADDR.dest+12);
 assert.ok(extra>0);
 assert.equal(memory.getI32(ADDR.dest+16),fixture.extra.length);
 assertDestinationSideData(memory,fixture);
 const allocs=memory.allocs.length;
 expectCode(()=>api.Yi(ADDR.dest,ADDR.src),'EIO_CODECPAR_LEASE_DEST_BUSY');
 assert.equal(memory.allocs.length,allocs);
 expectCode(()=>api.dn(ADDR.dest),'EIO_CODECPAR_CLONE_UNPROVEN');
 assert.equal(memory.frees.length,0);
 assert.equal(calls.release.length,0);
 assert.equal(calls.retain.length,1);
});

test('original dn throw retains with free-unproven, rethrows same error and never releases or frees again',()=>{
 const memory=makeMemory();
 const api=loadModule67669(memory);
 const fixture=setupSource(memory);
 const {lease,calls}=makeLease(fixture.bytes);
 api.Yi(ADDR.dest,ADDR.src,lease);
 const freeError=new Error('original free exploded');
 memory.freeError=freeError;
 memory.throwFreeFor.add(ADDR.dest);
 const caught=expectCode(()=>api.dn(ADDR.dest),undefined);
 assert.equal(caught,freeError);
 assert.equal(calls.release.length,0);
 assert.equal(calls.retain.length,1);
 assert.equal(calls.retain[0].code,'EIO_CODECPAR_FREE_UNPROVEN');
 assert.equal(memory.failedFrees.length,1);
 expectCode(()=>api.dn(ADDR.dest),'EIO_CODECPAR_FREE_UNPROVEN');
 assert.equal(memory.failedFrees.length,1);
 assert.equal(calls.release.length,0);
});

test('release false or throw after successful free retains without a second free',()=>{
 {
  const memory=makeMemory();
  const api=loadModule67669(memory);
  const fixture=setupSource(memory);
  const {lease,calls}=makeLease(fixture.bytes,{releaseResult:false});
  api.Yi(ADDR.dest,ADDR.src,lease);
  expectCode(()=>api.dn(ADDR.dest),'EIO_CODECPAR_RELEASE_UNVERIFIED');
  assert.equal(calls.release.length,1);
  assert.equal(calls.retain.length,1);
  assert.equal(calls.retain[0].code,'EIO_CODECPAR_RELEASE_UNVERIFIED');
  const frees=memory.frees.length;
  assert.ok(memory.frees.includes(ADDR.dest));
  expectCode(()=>api.dn(ADDR.dest),'EIO_CODECPAR_RELEASE_UNVERIFIED');
  assert.equal(memory.frees.length,frees);
  assert.equal(calls.release.length,1);
 }
 {
  const memory=makeMemory();
  const api=loadModule67669(memory);
  const fixture=setupSource(memory);
  const releaseError=new Error('release exploded');
  const {lease,calls}=makeLease(fixture.bytes,{releaseThrow:releaseError});
  api.Yi(ADDR.dest,ADDR.src,lease);
  const caught=expectCode(()=>api.dn(ADDR.dest),undefined);
  assert.equal(caught,releaseError);
  assert.equal(calls.release.length,1);
  assert.equal(calls.retain.length,1);
  assert.equal(calls.retain[0].code,'EIO_CODECPAR_RELEASE_UNVERIFIED');
  const frees=memory.frees.length;
  expectCode(()=>api.dn(ADDR.dest),'EIO_CODECPAR_RELEASE_UNVERIFIED');
  assert.equal(memory.frees.length,frees);
  assert.equal(calls.release.length,1);
 }
});

test('read-only payload scanner returns the exact metadata-derived sum without writes, allocations or copies',()=>{
 const memory=makeMemory();
 const api=loadModule67669(memory);
 const fixture=setupSource(memory);
 const writes=memory.writes.length;
 const allocs=memory.allocs.length;
 const copies=memory.copies.length;
 const reads=memory.reads.length;
 assert.equal(api.eioPayloadBytes(ADDR.src),fixture.bytes);
 assert.equal(memory.writes.length,writes);
 assert.equal(memory.allocs.length,allocs);
 assert.equal(memory.copies.length,copies);
 assertNoPayloadReads(memory,reads,fixture.payloadRanges);
});

test('read-only payload scanner rejects malformed source metadata without writes, allocations or copies',()=>{
 const cases=[
  {label:'zero extradata pointer with positive length',apply:memory=>{memory.pokeU32(ADDR.src+12,0);memory.pokeI32(ADDR.src+16,3);}},
  {label:'negative extradata length',apply:memory=>memory.pokeI32(ADDR.src+16,-1)},
  {label:'side count above cap',apply:memory=>memory.pokeI32(ADDR.src+24,65537)},
  {label:'side count with zero table pointer',apply:memory=>memory.pokeU32(ADDR.src+20,0)},
  {label:'positive side size with zero payload pointer',apply:memory=>memory.pokeU32(ADDR.sideTable,0)}
 ];
 for(const item of cases){
  const memory=makeMemory();
  const api=loadModule67669(memory);
  setupSource(memory);
  item.apply(memory);
  const writes=memory.writes.length;
  const allocs=memory.allocs.length;
  const copies=memory.copies.length;
  expectCode(()=>api.eioPayloadBytes(ADDR.src),'EIO_CODECPAR_LEASE_INVALID');
  assert.equal(memory.writes.length,writes,item.label);
  assert.equal(memory.allocs.length,allocs,item.label);
  assert.equal(memory.copies.length,copies,item.label);
 }
 const memory=makeMemory();
 const api=loadModule67669(memory);
 expectCode(()=>api.eioPayloadBytes(0),'EIO_CODECPAR_LEASE_INVALID');
});
