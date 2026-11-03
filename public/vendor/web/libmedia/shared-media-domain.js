"use strict";
/* Physical WASM domain and true-last-free registry facade. */
(self.webpackChunkAVPlayer=self.webpackChunkAVPlayer||[]).push([[90202],{
90187:(module,exports,__webpack_require__)=>{
const core=__webpack_require__(90190);
const EIO_DOMAIN_CONTROL_WORDS=8;
const EIO_DOMAIN_CONTROL_BYTES=32;
const EIO_DOMAIN_NONCE_WORDS=4;
const EIO_DOMAIN_SCRATCH_BASE=4;
const EIO_DOMAIN_ACTOR_SLOTS=4;
const EIO_DOMAIN_PROBE_BASE=0x6e10;
const eioDomainOwned=new Map();
function eioDomainError(code,message){
  const error=new Error(message||code);
  error.code=code;
  return error;
}
function eioDomainMismatch(message){
  return eioDomainError('EIO_MEMORY_DOMAIN_MISMATCH',message);
}
function eioIsInteger(value){
  return typeof value==='number'&&Number.isFinite(value)&&Number.isInteger(value);
}
function eioIsInt32(value){
  return eioIsInteger(value)&&value>=-2147483648&&value<=2147483647;
}
function eioIsSharedWasmMemory(value){
  return value instanceof WebAssembly.Memory&&value.buffer instanceof SharedArrayBuffer;
}
function eioRuntimeMemory(){
  const runtime=__webpack_require__(11286);
  const memory=runtime&&runtime.ce;
  if(!eioIsSharedWasmMemory(memory)){
    throw eioDomainError('unsupported','the runtime WebAssembly.Memory must be shared');
  }
  return memory;
}
function eioWasmAllocator(){
  return __webpack_require__(46626);
}
function eioDrawNonce(){
  const cryptoObject=typeof globalThis!=='undefined'&&globalThis?globalThis.crypto:null;
  if(!cryptoObject||typeof cryptoObject.getRandomValues!=='function'){
    throw eioDomainError('unsupported','crypto.getRandomValues is required for a domain nonce');
  }
  const nonce=new Int32Array(EIO_DOMAIN_NONCE_WORDS);
  for(let attempt=0;attempt<2;attempt+=1){
    cryptoObject.getRandomValues(nonce);
    let nonZero=false;
    for(let index=0;index<EIO_DOMAIN_NONCE_WORDS;index+=1){
      if(nonce[index]!==0){nonZero=true;break;}
    }
    if(nonZero)return nonce;
  }
  throw eioDomainError('EIO_MEMORY_DOMAIN_NONCE','crypto.getRandomValues returned an all-zero domain nonce twice');
}
function createDomainDescriptor(options){
  if(!options||typeof options!=='object'){
    throw eioDomainError('invalid-argument','createDomainDescriptor requires { capacityBytes, maxEntries, domainId }');
  }
  const domainId=options.domainId;
  if(typeof domainId!=='string'||domainId.length===0||domainId.length>200){
    throw eioDomainError('invalid-argument','domainId must be a non-empty string of at most 200 characters');
  }
  const memory=eioRuntimeMemory();
  const controller=core.create({capacityBytes:options.capacityBytes,maxEntries:options.maxEntries});
  const allocator=eioWasmAllocator();
  const proofPointer=allocator.Gy(EIO_DOMAIN_CONTROL_BYTES);
  if(!proofPointer){
    throw eioDomainError('unsupported','unable to allocate the shared domain control block');
  }
  try{
    if(!eioIsInteger(proofPointer)||proofPointer<=0||proofPointer%4!==0||proofPointer+EIO_DOMAIN_CONTROL_BYTES>memory.buffer.byteLength){
      throw eioDomainError('unsupported','the allocator returned an invalid shared control block pointer');
    }
    const nonce=eioDrawNonce();
    const controlView=new Int32Array(memory.buffer,proofPointer,EIO_DOMAIN_CONTROL_WORDS);
    controlView.fill(0);
    for(let index=0;index<EIO_DOMAIN_NONCE_WORDS;index+=1){
      controlView[index]=nonce[index];
    }
    const descriptor=Object.freeze(Object.assign({},controller.descriptor,{
      domainId:domainId,
      wasmMemory:memory,
      proofPointer:proofPointer,
      nonce:Array.from(nonce),
      ...(options.nativeClock===true?{nativeClockSab:__webpack_require__(90194).create(Array.from(nonce)).sab}:{})
    }));
    eioDomainOwned.set(descriptor,{controller:controller,proofPointer:proofPointer,memory:memory,state:'owned'});
    return descriptor;
  }catch(error){
    try{allocator.Eb(proofPointer);}catch(releaseError){}
    throw error;
  }
}
function attachDomainDescriptor(descriptor,actorSlot){
  if(!eioIsInteger(actorSlot)||actorSlot<0||actorSlot>=EIO_DOMAIN_ACTOR_SLOTS){
    throw eioDomainError('invalid-argument','actorSlot must be an integer in [0,3]');
  }
  const controller=core.attach(descriptor);
  const runtime=__webpack_require__(11286);
  const actualMemory=runtime&&runtime.ce;
  const expectedMemory=descriptor.wasmMemory;
  if(!eioIsSharedWasmMemory(expectedMemory)||!eioIsSharedWasmMemory(actualMemory)){
    throw eioDomainMismatch('the descriptor memory and the runtime memory must both be shared WebAssembly.Memory');
  }
  const proofPointer=descriptor.proofPointer;
  if(!eioIsInteger(proofPointer)||proofPointer<=0||proofPointer>0xffffffff||proofPointer%4!==0){
    throw eioDomainMismatch('the descriptor control pointer is not a valid aligned wasm32 pointer');
  }
  if(proofPointer+EIO_DOMAIN_CONTROL_BYTES>expectedMemory.buffer.byteLength||proofPointer+EIO_DOMAIN_CONTROL_BYTES>actualMemory.buffer.byteLength){
    throw eioDomainMismatch('the descriptor control block is outside one of the memories');
  }
  const nonce=descriptor.nonce;
  if(!Array.isArray(nonce)||nonce.length!==EIO_DOMAIN_NONCE_WORDS){
    throw eioDomainMismatch('the descriptor nonce is invalid');
  }
  let nonZero=false;
  for(let index=0;index<EIO_DOMAIN_NONCE_WORDS;index+=1){
    if(!eioIsInt32(nonce[index])){
      throw eioDomainMismatch('the descriptor nonce is invalid');
    }
    if(nonce[index]!==0){nonZero=true;}
  }
  if(!nonZero){
    throw eioDomainMismatch('the descriptor nonce is invalid');
  }
  const expectedView=new Int32Array(expectedMemory.buffer,proofPointer,EIO_DOMAIN_CONTROL_WORDS);
  const actualView=new Int32Array(actualMemory.buffer,proofPointer,EIO_DOMAIN_CONTROL_WORDS);
  for(let index=0;index<EIO_DOMAIN_NONCE_WORDS;index+=1){
    if(Atomics.load(expectedView,index)!==nonce[index]||Atomics.load(actualView,index)!==nonce[index]){
      throw eioDomainMismatch('the descriptor nonce does not match both memory domains');
    }
  }
  const scratchIndex=EIO_DOMAIN_SCRATCH_BASE+actorSlot;
  if(Atomics.load(expectedView,scratchIndex)!==0||Atomics.load(actualView,scratchIndex)!==0){
    throw eioDomainMismatch('the descriptor actor scratch word is not zero');
  }
  const marker=EIO_DOMAIN_PROBE_BASE+actorSlot;
  try{
    Atomics.store(expectedView,scratchIndex,marker);
    if(Atomics.load(actualView,scratchIndex)!==marker){
      throw eioDomainMismatch('the descriptor memory is not backed by the runtime memory domain');
    }
  }finally{
    Atomics.store(expectedView,scratchIndex,0);
  }
  return controller;
}
function destroyDomainDescriptor(descriptor){
  const record=eioDomainOwned.get(descriptor);
  if(!record){
    throw eioDomainError('EIO_DOMAIN_NOT_OWNED','only a descriptor returned by createDomainDescriptor in this realm can be destroyed');
  }
  if(record.state!=='owned'){
    throw eioDomainError('EIO_DOMAIN_FREE_UNVERIFIED','a previous control block release was not verified; refusing to free it again');
  }
  const heldBytes=record.controller.heldBytes();
  if(heldBytes!==0){
    throw eioDomainError('EIO_DOMAIN_STILL_HELD','the domain still holds compressed-media bytes; stop every actor and release every ownership record first');
  }
  /* Invalidate the control nonce before the allocator call so that a copied
   * descriptor cannot be attached even if it outlives the owner. This is not a
   * media byte credit and it does not replace the caller's obligation to stop
   * every actor before destroy. */
  const controlView=new Int32Array(record.memory.buffer,record.proofPointer,EIO_DOMAIN_CONTROL_WORDS);
  for(let index=0;index<EIO_DOMAIN_NONCE_WORDS;index+=1){
    Atomics.store(controlView,index,0);
  }
  record.state='closing';
  try{
    eioWasmAllocator().Eb(record.proofPointer);
  }catch(error){
    /* The allocator may already have released the block before throwing.
     * Never retry the free; keep the record as an explicit unverified fault. */
    record.state='faulted';
    throw error;
  }
  eioDomainOwned.delete(descriptor);
  return true;
}
const eioDomainFacade={};
Object.keys(core).forEach(function(key){
  eioDomainFacade[key]=core[key];
});
eioDomainFacade.createDomainDescriptor=createDomainDescriptor;
eioDomainFacade.attachDomainDescriptor=attachDomainDescriptor;
eioDomainFacade.destroyDomainDescriptor=destroyDomainDescriptor;
/* Direct true-last-free release facade. The registry module is required ONLY on
 * the first registerDomain call, so an ordinary no-opt-in load of this factory
 * never requires/instantiates 90188, never touches SharedArrayBuffer/WebCrypto
 * and never allocates. The require below stays explicit so the generated
 * libmedia worker dependency collector can see module 90188. */
let eioReleaseRegistry=null;
const EIO_RELEASE_DOMAIN_UNKNOWN='EIO_DOMAIN_UNKNOWN';
const EIO_RELEASE_TICKET_INVALID='EIO_RELEASE_TICKET_INVALID';
function eioReleaseApi(){
  if(eioReleaseRegistry===null){
    eioReleaseRegistry=__webpack_require__(90188).createReleaseRegistry(eioDomainFacade);
  }
  return eioReleaseRegistry;
}
function eioReleaseEmptySnapshot(){
  return Object.freeze({registeredDomains:0,faultCount:0,lastFault:null,domains:Object.freeze([])});
}
function eioReleaseUnknownTicket(){
  return {ok:false,code:EIO_RELEASE_TICKET_INVALID};
}
function registerReleaseDomain(descriptor,actorSlot){
  return eioReleaseApi().registerDomain(descriptor,actorSlot);
}
function unregisterReleaseDomain(domainId){
  if(eioReleaseRegistry===null)return {ok:false,code:EIO_RELEASE_DOMAIN_UNKNOWN};
  return eioReleaseRegistry.unregisterDomain(domainId);
}
function prepareReleaseDataFree(payload){
  if(eioReleaseRegistry===null)return null;
  return eioReleaseRegistry.prepareDataFree(payload);
}
function commitReleaseDataFree(opaque){
  if(eioReleaseRegistry===null){
    return opaque===null||opaque===undefined?{ok:true,tracked:false}:eioReleaseUnknownTicket();
  }
  return eioReleaseRegistry.commitDataFree(opaque);
}
function failReleaseDataFree(opaque){
  if(eioReleaseRegistry===null){
    return opaque===null||opaque===undefined?{ok:true,tracked:false}:eioReleaseUnknownTicket();
  }
  return eioReleaseRegistry.dataFreeFailed(opaque);
}
function releaseRegistrySnapshot(){
  if(eioReleaseRegistry===null)return eioReleaseEmptySnapshot();
  return eioReleaseRegistry.getReleaseRegistrySnapshot();
}
eioDomainFacade.registerDomain=registerReleaseDomain;
eioDomainFacade.unregisterDomain=unregisterReleaseDomain;
eioDomainFacade.prepareDataFree=prepareReleaseDataFree;
eioDomainFacade.commitDataFree=commitReleaseDataFree;
eioDomainFacade.dataFreeFailed=failReleaseDataFree;
eioDomainFacade.getReleaseRegistrySnapshot=releaseRegistrySnapshot;
module.exports=Object.freeze(eioDomainFacade);
/*__EIO_SHARED_MEDIA_DOMAIN_FACTORY_END__*/
}
}]);
