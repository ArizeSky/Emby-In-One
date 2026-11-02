'use strict';
/* Method/boundary tests for the real BSF init codecpar clone lease. These
   extract the shipped planner, owner lease and base-init text and execute it
   with fake memory/allocator/owned handles only. They do NOT run a real Worker,
   native BSF, MSE SourceBuffer or end-to-end playback. The parent actual-Worker
   checkpoint remains required before claiming runtime integration. */
const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const vm=require('node:vm');
const path=require('node:path');
const source=require('./web-mse-source-graph.cjs')();

function balancedBlock(text,openIndex){
 assert.equal(text[openIndex],'{');
 let depth=0;
 for(let i=openIndex;i<text.length;i++){
  const ch=text[i];
  if(ch==='{')depth+=1;
  else if(ch==='}'){depth-=1;if(depth===0)return text.slice(openIndex,i+1)}
 }
 assert.fail('unbalanced block in vendor source');
}
function between(text,start,end){
 const a=text.indexOf(start);
 assert.ok(a>=0,start);
 const b=text.indexOf(end,a+start.length);
 assert.ok(b>a,end);
 return text.slice(a,b)
}
function functionText(signature){
 const start=source.indexOf(signature);
 assert.ok(start>=0,signature);
 const open=start+signature.length-1;
 assert.equal(source[open],'{');
 return source.slice(start,open)+balancedBlock(source,open)
}
function methodText(signature){return functionText(signature)}

class FakeFormat{
 constructor(options){this.options=Object.assign({},options);this.context={}}
 writeHeader(host){host.ioWriter.writeBuffer({length:24});host.ioWriter.flush()}
 updateCurrentFragment(){}
}

function plannerApi(scanner){
 const helpers=between(source,'function eioMseQueueChunkBytes(', 'class $ extends');
 const base=methodText('eioPlanBaseRequest(t){');
 const plan=methodText('eioPlanFor(t,kind,opts){');
 return vm.runInNewContext(functionText('function eioMseCountOnlyFactory(t){')+helpers+';({'+base+','+plan+',eioPlanMux,EioMuxCountOnlyWriter})',{
  r:{o9:'nativeIndex'},
  f:{Un:()=>0},
  R:{A:FakeFormat},
  g:{eioPayloadBytes:scanner||(()=>0)}
 });
}

function headerFixture(api){
 const cp={codecId:27,codecType:0,extradata:new Uint8Array([1,2,3,4]),extradataSize:4,nativeIndex:0x400};
 const stream={index:0,codecpar:cp,timeBase:{num:1,den:1000,nativeIndex:0x500},sideData:{},privData:{fragIndexes:[]},metadata:{}};
 const context={chapters:null,cencs:{},metadata:{},trexs:[],currentFragment:{tracks:[],firstWrote:!1,sequence:1,size:0}};
 const fmt={options:{fragment:!0,fragmentMode:1,hasTfra:!1},context};
 const host={streams:[stream],ioWriter:new api.EioMuxCountOnlyWriter(4096),oformat:fmt,metadata:{},chapters:null};
 return{cp,stream,context,fmt,host,task:{oformatContext:host,bufferQueue:{queue:[]}}}
}
function headerPlan(api,runtimeInitCodecparBytes){
 const fixture=headerFixture(api);
 const request={
  host:fixture.host,
  context:fixture.context,
  queuedChunkBytes:[],
  extradataKind:{0:'avcc'},
  bsfResidentBytes:0,
  writerFactory:size=>new api.EioMuxCountOnlyWriter(size)
 };
 if(arguments.length>1)request.runtimeInitCodecparBytes=runtimeInitCodecparBytes;
 return api.eioPlanMux(fixture.fmt,'header',request)
}
function initSites(plan){return (plan.sites||[]).filter(site=>site&&site.name==='init-bsf-codecpar-clone')}

function planDiagnostic(plan){return JSON.stringify({reason:plan&&plan.reason,detail:plan&&plan.detail});}
test('one header scope adds the exact 9-byte init clone to the unchanged writer workset',()=>{
 const api=plannerApi();
 const baseline=headerPlan(api);
 const withInit=headerPlan(api,9);
 assert.equal(baseline.supported,!0,planDiagnostic(baseline));
 assert.equal(withInit.supported,!0,planDiagnostic(withInit));
 const site=initSites(withInit)[0];
 assert.ok(site,'init clone site present');
 assert.equal(site.bytes,9);
 assert.equal(site.allocationBytes,9);
 assert.equal(site.note,'exact native payload clone before runtime BSF init; metadata excluded');
 assert.equal(initSites(baseline).length,0,'omitted request field is not a measured zero');
 assert.equal(withInit.worksetBytes-baseline.worksetBytes,9);
 assert.equal(withInit.sites.length,baseline.sites.length+1);
 assert.deepEqual(
  withInit.sites.filter(item=>item.name!=='init-bsf-codecpar-clone').map(item=>[item.name,item.bytes,item.allocationBytes]),
  baseline.sites.map(item=>[item.name,item.bytes,item.allocationBytes])
 );
 const zero=headerPlan(api,0);
 assert.equal(zero.supported,!0,planDiagnostic(zero));
 assert.equal(initSites(zero).length,1);
 assert.equal(initSites(zero)[0].bytes,0);
 assert.equal(zero.worksetBytes,baseline.worksetBytes);
 const invalid=headerPlan(api,1.5);
 assert.equal(invalid.supported,!1);
 assert.equal(invalid.reason,'init-codecpar-shape-invalid');
});

test('actual eioPlanFor scans only the first eligible codecpar when no filter exists',()=>{
 const calls=[];
 const api=plannerApi(pointer=>{calls.push(pointer);return 9});
 const fixture=headerFixture(api);
 const planned=api.eioPlanFor(fixture.task,'header',{});
 assert.equal(planned.ok,!0,planDiagnostic(planned));
 assert.deepEqual(calls,[0x400]);
 assert.equal(initSites(planned.plan)[0].bytes,9);
 assert.equal(planned.upperBound,planned.plan.worksetBytes);

 const filtered=headerFixture(api);
 filtered.fmt.annexb2AvccFilter={};
 calls.length=0;
 const noNewFilter=api.eioPlanFor(filtered.task,'header',{});
 assert.equal(noNewFilter.ok,!0,planDiagnostic(noNewFilter));
 assert.deepEqual(calls,[],'an existing filter needs no new init clone');
 assert.equal(initSites(noNewFilter.plan).length,0);

 const audioOnly=headerFixture(api);
 audioOnly.stream.codecpar={codecId:86018,codecType:1,extradata:new Uint8Array([1]),extradataSize:1,nativeIndex:0x600};
 calls.length=0;
 const noEligible=api.eioPlanFor(audioOnly.task,'header',{});
 assert.equal(noEligible.ok,!0,planDiagnostic(noEligible));
 assert.deepEqual(calls,[]);
 assert.equal(initSites(noEligible.plan)[0].bytes,0,'explicit zero proves no init clone');
});

test('invalid native init shape or scanner failure refuses before the real init',()=>{
 const invalidPointer=[];
 const pointerApi=plannerApi(pointer=>{invalidPointer.push(pointer);return 9});
 const pointerFixture=headerFixture(pointerApi);
 pointerFixture.stream.codecpar.nativeIndex=0;
 const pointerOut=pointerApi.eioPlanFor(pointerFixture.task,'header',{});
 assert.equal(pointerOut.ok,!1);
 assert.equal(pointerOut.capability,!0);
 assert.equal(pointerOut.reason,'init-codecpar-shape-invalid');
 assert.deepEqual(invalidPointer,[],'invalid pointer is not scanned');

 const scannerApi=plannerApi(()=>{
  const error=new Error('fake scanner failure');
  error.code='EIO_CODECPAR_LEASE_INVALID';
  throw error;
 });
 const scannerFixture=headerFixture(scannerApi);
 const scannerOut=scannerApi.eioPlanFor(scannerFixture.task,'header',{});
 assert.equal(scannerOut.ok,!1);
 assert.equal(scannerOut.capability,!0);
 assert.equal(scannerOut.reason,'init-codecpar-shape-invalid');
 assert.equal(scannerOut.detail,undefined,'sanitized refusal carries no pointer detail');
});

function leaseApi(scanner){
 const method=methodText('eioNativeCodecparLease(t,e){');
 const claimHelpers=between(source,'function eioMseDropClaim(', 'function eioMseCountOnlyFactory(');
 return vm.runInNewContext(
  functionText('function eioMseError(t){')+
  claimHelpers+
  functionText('function eioPlanIsCount(t){')+
  ';({'+method+'})',
  {g:{eioPayloadBytes:scanner}}
 )
}
function leaseOwner(options){
 options=options||{};
 const releases=[];
 const scope={
  bytes:options.bytes===undefined?32:options.bytes,
  remaining:options.remaining===undefined?32:options.remaining,
  refs:0,
  closed:!!options.closed,
  atomic:!0,
  kind:'operation',
  fault:null,
  retained:!1
 };
 scope.handle={release(proof){releases.push(proof);return options.releaseResult||{ok:!0}}};
 const owner={
  scope,
  refusals:0,
  lastRefusal:null,
  manager:{acquireCalls:0,acquire(){this.acquireCalls+=1;return{ok:!0,handle:{release(){return{ok:!0}}}}}}
 };
 owner.eioActiveOperation={owner,scope,open:!0};
 return{owner,scope,releases}
}

test('owner lease consumes one scope claim before any native allocation and never re-acquires',()=>{
 const api=leaseApi(()=>9);
 const fixture=leaseOwner();
 const lease=api.eioNativeCodecparLease(fixture.owner,0x400);
 assert.equal(lease.bytes,9);
 assert.deepEqual(Object.keys(lease).sort(),['bind','bytes','release','retainFailure']);
 assert.equal(fixture.scope.remaining,23);
 assert.equal(fixture.scope.refs,1,'scope refs increment exactly once');
 assert.equal(fixture.owner.manager.acquireCalls,0,'no second manager acquire');
 assert.equal(lease.bind({copiedBytes:9}),!0);
 assert.equal(lease.bind({copiedBytes:9}),!1,'repeat bind is false');
 assert.equal(lease.release({referencesDropped:!1}),!1);
 assert.equal(lease.release({referencesDropped:!0}),!0);
 assert.equal(fixture.releases.length,0,'an open scope is not released by this alias');
 assert.equal(fixture.scope.refs,0);
 assert.equal(lease.release({referencesDropped:!0}),!1,'repeat release is false');

 const zeroApi=leaseApi(()=>0);
 const zeroFixture=leaseOwner({bytes:0,remaining:0});
 const zeroLease=zeroApi.eioNativeCodecparLease(zeroFixture.owner,0x400);
 assert.equal(zeroLease.bind({copiedBytes:0}),!0,'exact zero binds');
 assert.equal(zeroLease.bind({copiedBytes:!1}),!1);
 const nullLease=zeroApi.eioNativeCodecparLease(zeroFixture.owner,0x400);
 assert.equal(nullLease.bind(null),!1);
 assert.equal(nullLease.bind({copiedBytes:!1}),!1);
 assert.equal(nullLease.bind({copiedBytes:0}),!0);
});

test('byte underrun and missing active scope refuse without native allocation',()=>{
 let allocs=0;
 const api=leaseApi(()=>9);
 const short=leaseOwner({remaining:8});
 assert.throws(()=>api.eioNativeCodecparLease(short.owner,0x400),error=>{
  assert.equal(error.code,'EIO_MSE_PLAN_UNDERRUN');
  return!0
 });
 assert.equal(allocs,0);
 assert.equal(short.scope.remaining,8);
 assert.equal(short.scope.refs,0);
 assert.equal(short.scope.underruns,1);
 assert.equal(short.scope.fault,'EIO_MSE_PLAN_UNDERRUN');
 assert.equal(short.owner.lastRefusal,'EIO_MSE_PLAN_UNDERRUN');

 const missing=leaseOwner();
 delete missing.owner.eioActiveOperation;
 assert.throws(()=>api.eioNativeCodecparLease(missing.owner,0x400),error=>{
  assert.equal(error.code,'EIO_MSE_PLAN_MISSING');
  return!0
 });
 assert.equal(allocs,0);
 assert.equal(missing.scope.remaining,32);
 assert.equal(missing.scope.refs,0);
 assert.equal(missing.owner.manager.acquireCalls,0);
});

test('retained failure keeps the claim and credit without dropping or releasing',()=>{
 const api=leaseApi(()=>9);
 const fixture=leaseOwner();
 const lease=api.eioNativeCodecparLease(fixture.owner,0x400);
 fixture.scope.closed=!0; // Acquisition precedes operation closure; retention is tested afterward.
 lease.retainFailure({code:'EIO_CODECPAR_CLONE_UNPROVEN'});
 assert.equal(fixture.scope.retained,!0);
 assert.equal(fixture.scope.fault,'EIO_CODECPAR_CLONE_UNPROVEN');
 assert.equal(fixture.scope.remaining,23);
 assert.equal(fixture.scope.refs,1);
 assert.equal(fixture.releases.length,0);
 assert.equal(lease.bind({copiedBytes:9}),!1,'failed lease never binds');
 assert.equal(lease.release({referencesDropped:!0}),!1,'failed lease never releases');
 assert.equal(fixture.releases.length,0);
 assert.equal(fixture.scope.refs,1);
 lease.retainFailure({code:'EIO_CODECPAR_RELEASE_UNVERIFIED'});
 assert.equal(fixture.scope.fault,'EIO_CODECPAR_CLONE_UNPROVEN','first stable fault is preserved');
});

test('scoped release final failure is not reported as success and then retains',()=>{
 const api=leaseApi(()=>9);
 const fixture=leaseOwner({releaseResult:{ok:!1,code:'EIO_OWNED_RELEASE_FAILED'}});
 const lease=api.eioNativeCodecparLease(fixture.owner,0x400);
 fixture.scope.closed=!0; // The last alias releases after the admitted operation closes.
 assert.equal(lease.bind({copiedBytes:9}),!0);
 assert.equal(lease.release({referencesDropped:!0}),!1,'the scope release failure wins');
 assert.equal(fixture.scope.releaseFailed,!0);
 assert.equal(fixture.releases.length,1);
 lease.retainFailure({code:'EIO_CODECPAR_RELEASE_UNVERIFIED'});
 assert.equal(fixture.scope.retained,!0);
 assert.equal(fixture.scope.fault,'EIO_CODECPAR_RELEASE_UNVERIFIED');
 assert.equal(fixture.releases.length,1,'retainFailure does not drop or release again');
 assert.equal(lease.release({referencesDropped:!0}),!1);
 assert.equal(fixture.releases.length,1);
});

function baseInitMethod(){
 const moduleStart=source.indexOf('2843:(t,e,i)=>{');
 assert.ok(moduleStart>=0,'module 2843 must stay in the scoped bundle');
 const classStart=source.indexOf('class o{constructor(){',moduleStart);
 assert.ok(classStart>=0,'module 2843 class must stay extractable');
 const start=source.indexOf('init(t,e){',classStart);
 assert.ok(start>=0,'module 2843 init must stay extractable');
 const open=start+'init(t,e)'.length;
 return 'init(t,e)'+balancedBlock(source,open)
}
function baseInitApi(order){
 return vm.runInNewContext('({'+baseInitMethod()+'})',{
  s:{Gy(size){order.push(['malloc',size]);return 0x1000}},
  n:{Yi(dest,src,lease){order.push(lease===undefined?['yi',dest,src]:['yi',dest,src,lease])}},
  r:{f:{15(pointer){order.push(['read',pointer]);return pointer===0x904?25:1000}}}
 })
}

test('base init baseline keeps Gy then two-argument Yi then timebase order',()=>{
 const order=[];
 const api=baseInitApi(order);
 const instance={};
 assert.equal(api.init.call(instance,42,0x900),0);
 assert.deepEqual(order,[['malloc',168],['yi',0x1000,42],['read',0x904],['read',0x900]]);
 assert.equal(instance.inTimeBase.den,25);
 assert.equal(instance.inTimeBase.num,1000);
});

test('strict base init calls the owner lease with the source pointer before Gy and passes the third Yi argument',()=>{
 const order=[];
 const lease={bytes:9,bind(){return!0},release(){return!0},retainFailure(){}};
 const api=baseInitApi(order);
 const instance={eioOwned:{nativeCodecparLease(pointer){order.push(['lease',pointer]);return lease}}};
 assert.equal(api.init.call(instance,42,0x900),0);
 assert.deepEqual(order,[['lease',42],['malloc',168],['yi',0x1000,42,lease],['read',0x904],['read',0x900]]);
 assert.equal(order[2].length,4,'strict Yi receives the lease as its third argument');
});

test('strict base init never falls back when the owner lease method is missing',()=>{
 const order=[];
 const api=baseInitApi(order);
 assert.throws(()=>api.init.call({eioOwned:{}},42,0x900),error=>{
  assert.equal(error.code,'EIO_MSE_PLAN_MISSING');
  return!0
 });
 assert.deepEqual(order,[],'no allocation, clone or timebase mutation before the refusal');
});

test('fake native free model runs lease release only after the one native free',()=>{
 /* FAKE native allocator/free boundary: this models module 67669 dn ordering;
    it is not a real native free, Worker, or BSF acceptance test. */
 const order=[];
 let freed=0;
 const lease={
  bytes:9,
  bind(summary){order.push(['bind',summary.copiedBytes]);return!0},
  release(proof){order.push(['release',proof.referencesDropped]);return!0},
  retainFailure(){order.push(['retain'])}
 };
 const api=baseInitApi(order);
 const instance={eioOwned:{nativeCodecparLease(pointer){order.push(['lease',pointer]);return lease}}};
 api.init.call(instance,42,0x900);
 lease.bind({copiedBytes:9});
 order.push(['free',instance.inCodecpar]);freed+=1;
 lease.release({referencesDropped:!0});
 assert.equal(freed,1);
 assert.deepEqual(order.map(item=>item[0]),['lease','malloc','yi','read','read','bind','free','release']);
 assert.deepEqual(order[7],['release',!0]);
});

function bsfInitMethod(){
 const moduleStart=source.indexOf('88560:(t,e,i)=>{');
 assert.ok(moduleStart>=0,'module 88560 must stay in the scoped bundle');
 const start=source.indexOf('init(t){',moduleStart);
 assert.ok(start>=0,'module 88560 init must stay extractable');
 const open=start+'init(t)'.length;
 return 'init(t)'+balancedBlock(source,open)
}
function bsfInitApi(order){
 class FakeAnnexb2AvccFilter{
  constructor(reverseSps){order.push(['filter-constructor',reverseSps])}
  init(codecpar,timeBase){order.push(['filter-init',this.eioOwned,codecpar,timeBase])}
 }
 return vm.runInNewContext('({'+bsfInitMethod()+'})',{
  m:{A:FakeAnnexb2AvccFilter},
  r:{o9:'nativeIndex'},
  b:{_5(){order.push(['packet']);return 0x5150}}
 })
}
function bsfHost(){
 return{
  ioWriter:{setEndian(value){}},
  streams:[{codecpar:{codecId:27,nativeIndex:11},timeBase:{nativeIndex:22}}],
  metadata:{encoder:'fixture'},
  chapters:[]
 }
}

test('actual BSF init attaches the owner before filter.init only on the strict runtime format',()=>{
 const strictOrder=[];
 const strictApi=bsfInitApi(strictOrder);
 const owner={nativeCodecparLease(){}};
 const strict={options:{reverseSpsInAvcc:!0},context:{},eioOwned:owner};
 assert.equal(strictApi.init.call(strict,bsfHost()),0);
 assert.deepEqual(strictOrder,[['filter-constructor',!0],['filter-init',owner,11,22],['packet']]);
 assert.equal(strict.annexb2AvccFilter.eioOwned,owner);

 const baselineOrder=[];
 const baselineApi=bsfInitApi(baselineOrder);
 const baseline={options:{reverseSpsInAvcc:!1},context:{}};
 assert.equal(baselineApi.init.call(baseline,bsfHost()),0);
 assert.deepEqual(baselineOrder,[['filter-constructor',!1],['filter-init',undefined,11,22],['packet']]);
 assert.equal(Object.prototype.hasOwnProperty.call(baseline.annexb2AvccFilter,'eioOwned'),!1);
});

test('actual header plan integration consumes the init reserve before the metadata allocation and clone',()=>{
 const order=[];
 const api=leaseApi(()=>9);
 const fixture=leaseOwner();
 const leaseOwnerWithMethod={
  ...fixture.owner,
  nativeCodecparLease(pointer){
   order.push(['lease',pointer]);
   return api.eioNativeCodecparLease(leaseOwnerWithMethod,pointer)
  }
 };
 leaseOwnerWithMethod.eioActiveOperation={owner:leaseOwnerWithMethod,scope:fixture.scope,open:!0};
 let remainingAtAlloc=null;
 const baseText=baseInitMethod();
 const module=vm.runInNewContext('({'+baseText+'})',{
  s:{Gy(size){remainingAtAlloc=fixture.scope.remaining;order.push(['malloc',size]);return 0x1000}},
  n:{Yi(dest,src,lease){order.push(['clone',dest,src,lease.bytes]);assert.equal(lease.bind({copiedBytes:9}),!0)}},
  r:{f:{15(pointer){order.push(['read',pointer]);return pointer===0x904?25:1000}}}
 });
 order.push(['scope-before',fixture.scope.remaining]);
 const instance={eioOwned:leaseOwnerWithMethod};
 const baseTextCheck=baseInitMethod();
 assert.match(baseTextCheck,/nativeCodecparLease/);
 assert.equal(module.init.call(instance,0x400,0x900),0);
 assert.deepEqual(order[0],['scope-before',32]);
 assert.deepEqual(order[1],['lease',0x400]);
 assert.equal(remainingAtAlloc,23,'reserve is consumed before the 168-byte metadata allocation');
 assert.deepEqual(order[2],['malloc',168]);
 assert.deepEqual(order[3],['clone',0x1000,0x400,9]);
 assert.equal(fixture.owner.manager.acquireCalls,0,'no second acquire in the integrated path');
});

test('init-bsf-avpacket reason text reports partial coverage without unknown-BSF zero',()=>{
 assert.match(source,/"init-bsf-avpacket":"[^"]*native input codecpar payload clone is leased in the header scope/);
 assert.match(source,/"init-bsf-avpacket":"[^"]*BSF cache\/filter, AVPacket and conversion ownership remain unproved and uncharged/);
});

test('incomplete coverage explicitly includes persistent stream clones and replacement lifetime',()=>{
 const keys=source.match(/const EIO_MSE_INCOMPLETE=Object\.freeze\((\[[^;]+\])\);/);
 assert.ok(keys,'actual shipped incomplete-key list');
 assert.ok(JSON.parse(keys[1]).includes('stream-codecpar-clones'));
 assert.match(source,/"stream-codecpar-clones":"[^\"]*NATIVE input[^\"]*addStream\/reAddStream[^\"]*admitted and charged[^\"]*serialized-input clones, mux-output codecpar clones and in-place replacement lifetime remain uncovered/);
});
