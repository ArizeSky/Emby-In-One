'use strict';
const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const vm=require('node:vm');
const path=require('node:path');
const source=require('./web-mse-source-graph.cjs')();
const player=fs.readFileSync(path.join(__dirname,'../public/vendor/web/libmedia/avplayer.js'),'utf8');
function between(text,start,end){const a=text.indexOf(start);assert.ok(a>=0,start);const b=text.indexOf(end,a+start.length);assert.ok(b>a,end);return text.slice(a,b);}
/* Extracts the real planner functions (helpers + eioPlanMux + eioPlanOperationWrite)
   for direct-call tests. Stubs only fill unrelated module globals. */
function eioPlanApi(){
 const counter=between(source,'function eioCountOnlyUtf8Length(', '/* EIO-DUAL-MUX-STEP2:BEGIN');
 const helpers=between(source,'/* EIO-DUAL-MUX-STEP2:BEGIN', 'class $ extends');
 return vm.runInNewContext(counter+helpers+';({eioPlanMux,eioPlanOperationWrite,EioMuxCountOnlyWriter})',{r:{o9:'native'},f:{Un:()=>0},R:{A:class{constructor(options){this.options=options}updateCurrentFragment(){}}}});
}
/* Extracts the real eioPacketSteps with a fake packet memory and a fake native
   writer. This is helper-level only: it shows which index the preflight
   predicts and that the helper neither rewrites packet memory nor calls the
   native write path. It is not a real Worker, Demux or mux-writer proof. */
function eioPacketStepsApi(options){
 const packet=options.packet;
 const memory={packetIndex:options.rawPacketIndex,nativeWrites:[]};
 const api=vm.runInNewContext('({'+between(source,'eioPacketSteps(t,p,opts){','eioPlanFor(t,kind,opts){')+'})',{
  h:{Mr:(value,ptr,timeBase)=>value},
  s:{f:{15:ptr=>ptr===packet+32?memory.packetIndex:0,17:()=>void 0===options.dts?0n:options.dts}},
  m:{Dh:-1n},
  eioMsePacketPayload:()=>options.payloadBytes,
  c:{M:{15:(ptr,value)=>{memory.nativeWrites.push([ptr,value]);memory.packetIndex=value}}}
 });
 return{api,memory};
}
function eioPacketTask(streams,tracks){
 return{lastDTS:-1n,oformatContext:{streams,oformat:{options:{fragmentMode:1},context:{currentFragment:{tracks}}}}};
}
function assertScalarDetail(detail){
 assert.ok(detail&&'object'===typeof detail,JSON.stringify(detail));
 for(const [key,value] of Object.entries(detail))assert.ok(value===null||['string','boolean','number'].includes(typeof value),JSON.stringify({key,value}));
 assert.equal(JSON.parse(JSON.stringify(detail)).site,detail.site);
}
test('real codecpar pointer is read as a view, not mislabeled AnnexB',()=>{
 const fn=vm.runInNewContext('('+between(source,'function eioMseExtradataKind(', '/* Actual known compressed')+')',{a:{s3:(ptr,size)=>{assert.equal(ptr,128);assert.equal(size,4);return new Uint8Array([1,2,3,4]);}}});
 assert.equal(fn({streams:[{index:0,codecpar:{codecId:27,extradata:128,extradataSize:4}}]})[0],'avcc');
 assert.equal(fn({streams:[{index:0,codecpar:{codecId:27,extradata:new Uint8Array([0,0,1])}}]})[0],'annexb');
});
test('PSSH scan supports real sideData maps and still rejects present PSSH',()=>{
 const fn=vm.runInNewContext('('+between(source,'function eioPlanScanPssh(', 'function eioPlan')+')',{r:{o9:'native'},f:{Un:()=>0}});
 assert.equal(fn({streams:[{sideData:{}}]}).ok,true);
 assert.equal(fn({streams:[{codecpar:{codedSideData:{24:new Uint8Array([1])}}}]}).ok,false);
});
test('stream shadow preserves sideData map without copying media payloads',()=>{
 const fn=vm.runInNewContext('('+between(source,'function eioPlanShadowStream(', 'function eioPlanScanPssh(')+')',{eioPlanShadowCodecpar:x=>x});
 const bytes=new Uint8Array([1]);const input={index:0,sideData:{2:bytes},codecpar:{},timeBase:{num:1,den:1000}};
 const out=fn(input);assert.notEqual(out.sideData,input.sideData);assert.equal(out.sideData[2],bytes);
});
test('packet planner uses mux context fragment and never mutates real state',()=>{
 const body=between(source,'eioPacketSteps(t,p,opts){','eioPlanFor(t,kind,opts){');
 const obj=vm.runInNewContext('({'+body+'})',{h:{Mr:()=>0n},s:{f:{15:()=>0,17:()=>0n}},m:{Dh:-1n},eioMsePacketPayload:()=>4});
 const track={streamIndex:0,tfdtDelay:0n};const fragment={tracks:[track]};
 const task={oformatContext:{streams:[{index:0,codecpar:{codecType:1},timeBase:{num:1,den:1000}}],oformat:{options:{fragmentMode:1},context:{currentFragment:fragment}}}};
 assert.equal(obj.eioPacketSteps(task,80,{}).ok,true);assert.equal(fragment.tracks[0],track);
});
test('packet plan uses the selected one-stream output index while the raw packet index stays untouched',()=>{
 /* Real Worker shape: the audio Demux packet carries raw index 1 while the
    one-stream output format built by addStream selects output index 0. */
 const packet=64;
 const {api,memory}=eioPacketStepsApi({packet,rawPacketIndex:1,payloadBytes:4,dts:100n});
 const track={streamIndex:0,tfdtDelay:25n};
 const task=eioPacketTask([{index:0,codecpar:{codecType:1},timeBase:{num:1,den:1000}}],[track]);
 const out=api.eioPacketSteps(task,packet,{});
 assert.equal(out.ok,true,JSON.stringify({reason:out.reason,detail:out.detail}));
 const writes=out.steps.filter(step=>step.kind==='write');
 assert.equal(writes.length,1);
 assert.deepEqual(Array.from(writes.map(step=>step.streamIndex)),[0]);
 const fragment=out.steps.filter(step=>step.kind==='fragment');
 assert.equal(fragment.length,1);
 assert.equal(fragment[0].dts,125n);
 assert.equal(memory.packetIndex,1);
 assert.deepEqual(memory.nativeWrites,[]);
 assert.equal(track.streamIndex,0);
});
test('raw and selected output index zero keep the existing zero baseline',()=>{
 const packet=64;
 const {api,memory}=eioPacketStepsApi({packet,rawPacketIndex:0,payloadBytes:4});
 const track={streamIndex:0,tfdtDelay:0n};
 const task=eioPacketTask([{index:0,codecpar:{codecType:1},timeBase:{num:1,den:1000}}],[track]);
 const out=api.eioPacketSteps(task,packet,{});
 assert.equal(out.ok,true,JSON.stringify({reason:out.reason,detail:out.detail}));
 const writes=out.steps.filter(step=>step.kind==='write');
 assert.equal(writes.length,1);
 assert.equal(writes[0].streamIndex,0);
 assert.equal(memory.packetIndex,0);
 assert.deepEqual(memory.nativeWrites,[]);
});
test('multi-output, missing or invalid selected output index refuses before packet mutation',()=>{
 const packet=64;
 const cases=[
  {label:'multi-output',streams:[{index:0,codecpar:{codecType:1},timeBase:{num:1,den:1000}},{index:1,codecpar:{codecType:1},timeBase:{num:1,den:1000}}]},
  {label:'missing',streams:[]},
  {label:'null stream',streams:[null]},
  {label:'missing index',streams:[{codecpar:{codecType:1}}]},
  {label:'negative index',streams:[{index:-1,codecpar:{codecType:1}}]},
  {label:'fractional index',streams:[{index:0.5,codecpar:{codecType:1}}]},
  {label:'string index',streams:[{index:'0',codecpar:{codecType:1}}]},
  {label:'numeric streams',streams:null}
 ];
 for(const fixture of cases){
  const {api,memory}=eioPacketStepsApi({packet,rawPacketIndex:1,payloadBytes:4});
  const track={streamIndex:0,tfdtDelay:0n};
  const out=api.eioPacketSteps(eioPacketTask(fixture.streams,[track]),packet,{});
  assert.equal(out.ok,false,fixture.label);
  assert.equal(out.capability,true,fixture.label);
  assert.equal(out.reason,'output-stream-state-invalid',fixture.label);
  assert.equal(memory.packetIndex,1,fixture.label);
  assert.deepEqual(memory.nativeWrites,[],fixture.label);
  assert.equal(track.streamIndex,0,fixture.label);
 }
});
test('strict teardown rejects encoded worker failures instead of proceeding to IO free',async()=>{
 const body=between(player,'async eioUnregisterChecked(','async stop(');
 const obj=vm.runInNewContext('({'+body+'})');obj.eioSharedMediaDomain={domainId:'fixture'};
 await assert.rejects(obj.eioUnregisterChecked({unregisterTask:async()=>-2097152},'task'),e=>e.code==='EIO_TASK_TEARDOWN_UNVERIFIED');
 await obj.eioUnregisterChecked({unregisterTask:async()=>undefined},'task');
 obj.eioSharedMediaDomain=null;await obj.eioUnregisterChecked({unregisterTask:async()=>-2097152},'task');
});
test('native codecpar PSSH uses codec side-data ABI, not stream packet map',()=>{
 const calls=[];
 const fn=vm.runInNewContext('('+between(source,'function eioPlanScanPssh(', 'function eioPlan')+')',{r:{o9:'native'},f:{Un:(...args)=>{calls.push(args);return 90}}});
 assert.equal(fn({streams:[{codecpar:{native:100},sideData:{25:0}}]}).ok,false);
 assert.deepEqual(calls,[[120,124,24]]);
});
test('extradata bytes count native allocation size and reject malformed sizes',()=>{
 const fn=vm.runInNewContext('('+between(source,'function eioPlanExtradataLength(', 'function eioPlanHeaderExtradataBytes(')+')',{eioPlanIsCount:x=>Number.isSafeInteger(x)&&x>=0});
 assert.equal(fn({extradata:128,extradataSize:31}),31);
 assert.equal(fn({extradata:new Uint8Array(9)}),9);
 assert.equal(fn({extradata:128,extradataSize:-1}),-1);
});
test('zero sample multiplication is valid and real conversion packets fail before mutation',()=>{
 const mul=vm.runInNewContext('('+between(source,'function eioPlanMul(', 'function eioPlanFlushBounded(')+')',{eioPlanIsCount:x=>Number.isSafeInteger(x)&&x>=0});
 assert.equal(mul(0,256),0);
 const body=between(source,'eioPacketCapability(t,p){','/* The packet steps');
 const fn=vm.runInNewContext('({'+body+'})',{s:{f:{15:()=>64}}});
 assert.equal(fn.eioPacketCapability({oformatContext:{streams:[{codecpar:{codecId:27}}]}},80),'packet-annexb-conversion-unmeasured');
 assert.equal(fn.eioPacketCapability({oformatContext:{streams:[{codecpar:{codecId:94226}}]}},80),'packet-webvtt-boxing-unmeasured');
});
test('empty real-shape fragment preflight preserves zero-count arithmetic',()=>{
 const counter=between(source,'function eioCountOnlyUtf8Length(', '/* EIO-DUAL-MUX-STEP2:BEGIN');
 const helpers=between(source,'/* EIO-DUAL-MUX-STEP2:BEGIN', 'class $ extends');
 /* Fake fixture: one AAC stream and one active-but-empty fragment track, with
    count-only writers and no payload. `R.A` substitutes only the format
    emitter: it writes nothing, so no moof/mdat box writer runs (this is not
    real box-writer or real-Worker acceptance). `w` is a nonnumeric stand-in
    for the real module binding w=i(41933); the resident totals must never
    read it. r/f/R keep the original module globals available. */
 const api=vm.runInNewContext(counter+helpers+';({eioPlanMux,EioMuxCountOnlyWriter})',{r:{o9:'native'},f:{Un:()=>0},w:{eioModuleBinding41933:'nonnumeric-sentinel'},R:{A:class{constructor(options){this.options=options}updateCurrentFragment(){}}}});
 const W=api.EioMuxCountOnlyWriter;
 const cp={codecId:86018,codecType:1,extradata:128,extradataSize:3};
 const stream={index:0,codecpar:cp,timeBase:{num:1,den:48000},privData:{lastDts:0n}};
 const track={streamIndex:0,trackId:1,sampleCount:0,ioWriter:new W(),buffers:[],sampleSizes:[],sampleDurations:[],sampleFlags:[],sampleCompositionTimeOffset:[]};
 const fmt={options:{fragment:true,fragmentMode:1},context:{fragment:true,currentFragment:{firstWrote:false,tracks:[track],size:0},cencs:{}}};
 const host={streams:[stream],ioWriter:new W()};
 const plan=extra=>api.eioPlanMux(fmt,'fragment',Object.assign({host,context:fmt.context,writerFactory:size=>new W(size)},extra));
 const snapshot=()=>JSON.stringify({hostPointer:host.ioWriter.pointer,hostPos:String(host.ioWriter.pos),hostSlices:host.ioWriter.eioFlushSlices,trackPointer:track.ioWriter.pointer,trackPos:String(track.ioWriter.pos),trackSlices:track.ioWriter.eioFlushSlices,sampleCount:track.sampleCount,buffers:track.buffers,sampleSizes:track.sampleSizes,extradata:cp.extradata,extradataSize:cp.extradataSize,contextKeys:Object.keys(fmt.context)});
 const before=snapshot();
 const out=plan({queuedChunkBytes:[]});
 assert.equal(out.supported,true,JSON.stringify({reason:out.reason,detail:out.detail}));
 /* Both resident totals charge muxWindowBytes + trackWindowBytes(1 MiB each),
    the queued bytes and the known BSF bytes; a zero-sample track contributes
    no payload and the BSF value is never fabricated. */
 const withBsf=plan({queuedChunkBytes:[13],bsfResidentBytes:9});
 assert.equal(withBsf.supported,true,JSON.stringify({reason:withBsf.reason,detail:withBsf.detail}));
 assert.equal(withBsf.residentBefore,2*1048576+13+9);
 assert.equal(withBsf.residentAfter,2*1048576+13+9);
 assert.deepEqual({residentBytes:withBsf.bsf.residentBytes},{residentBytes:9});
 const zeroBsf=plan({queuedChunkBytes:[13],bsfResidentBytes:0});
 assert.equal(zeroBsf.supported,true,JSON.stringify({reason:zeroBsf.reason,detail:zeroBsf.detail}));
 assert.deepEqual({residentBytes:zeroBsf.bsf.residentBytes},{residentBytes:0});
 assert.equal(zeroBsf.residentBefore,2*1048576+13);
 assert.equal(zeroBsf.residentAfter,2*1048576+13);
 const unknownBsf=plan({queuedChunkBytes:[13],bsfResidentUnknown:true});
 assert.equal(unknownBsf.supported,true,JSON.stringify({reason:unknownBsf.reason,detail:unknownBsf.detail}));
 assert.equal(unknownBsf.bsf,null);
 assert.equal(unknownBsf.residentBefore,null);
 assert.equal(unknownBsf.residentAfter,null);
 assert.equal(unknownBsf.peakBytes,null);
 const overflow=plan({queuedChunkBytes:[Number.MAX_SAFE_INTEGER],bsfResidentBytes:9});
 assert.equal(overflow.supported,false);
 assert.equal(overflow.reason,'plan-overflow');
 assert.equal(overflow.detail.site,'mux.resident-before');
 assert.equal(overflow.detail.queuedBytes,Number.MAX_SAFE_INTEGER);
 assert.equal(Object.prototype.hasOwnProperty.call(overflow.detail,'trackBufferBytes'),false);
 assert.equal(overflow.detail.residentBeforeBytes,-1);
 assertScalarDetail(overflow.detail);
 assert.equal(snapshot(),before);
});
test('diagnostic scalar preserves null, false, zero and nonfinite distinction',()=>{
 const fn=vm.runInNewContext('('+between(source,'function eioPlanDiagnosticScalar(', 'function eioPlanOverflowDetail(')+')');
 assert.equal(fn(null),null);
 assert.equal(fn(false),false);
 assert.equal(fn(true),true);
 assert.ok(Object.is(fn(0),0));
 assert.ok(Object.is(fn(-0),-0));
 assert.equal(fn(NaN),'number:nan');
 assert.equal(fn(Infinity),'number:infinity');
 assert.equal(fn(-Infinity),'number:-infinity');
 assert.equal(fn(undefined),'undefined');
 assert.equal(fn('label'),'label');
 assert.equal(fn(7),7);
 assert.equal(fn(10n),'10');
 assert.equal(fn({}),'object');
});
test('invalid native extradata size refuses the header plan at mux.header-extradata without mutation',()=>{
 const api=eioPlanApi();
 const W=api.EioMuxCountOnlyWriter;
 const cp={codecId:27,codecType:0,extradata:128,extradataSize:-1};
 const stream={index:0,codecpar:cp,timeBase:{num:1,den:1000},sideData:{},privData:{fragIndexes:[]}};
 const context={chapters:null,cencs:{},metadata:{},trexs:[],currentFragment:null};
 const fmt={options:{fragment:true,fragmentMode:1,hasTfra:false},context};
 const host={streams:[stream],ioWriter:new W(1024)};
 const before=JSON.stringify({size:host.ioWriter.size,pointer:host.ioWriter.pointer,pos:String(host.ioWriter.pos),slices:host.ioWriter.eioFlushSlices,context:Object.keys(context)});
 const out=api.eioPlanMux(fmt,'header',{host,context,queuedChunkBytes:[],extradataKind:{0:'avcc'},bsfResidentBytes:0,writerFactory:size=>new W(size)});
 assert.equal(out.supported,false);
 assert.equal(out.reason,'plan-overflow');
 assert.equal(out.detail.site,'mux.header-extradata');
 assert.equal(out.detail.fixedExtradataBytes,-1);
 assert.deepEqual(Object.keys(out.detail).sort(),['fixedExtradataBytes','site','streams']);
 assertScalarDetail(out.detail);
 assert.equal(JSON.stringify({size:host.ioWriter.size,pointer:host.ioWriter.pointer,pos:String(host.ioWriter.pos),slices:host.ioWriter.eioFlushSlices,context:Object.keys(context)}),before);
 assert.equal(cp.extradata,128);
 assert.equal(cp.extradataSize,-1);
});
test('overflowing packet write refuses with a named operation.write site before any payload copy',()=>{
 const api=eioPlanApi();
 const W=api.EioMuxCountOnlyWriter;
 const writer=new W(1024);writer.pointer=1024;
 const track={streamIndex:0,trackId:3,ioWriter:writer,buffers:[],sampleSizes:[],sampleCount:0};
 const task={context:{currentFragment:{tracks:[track],firstWrote:false}}};
 const out=api.eioPlanOperationWrite(task,{streamIndex:0,payloadBytes:Number.MAX_SAFE_INTEGER});
 assert.equal(out.supported,false);
 assert.equal(out.reason,'plan-overflow');
 assert.equal(out.detail.site,'operation.write.sum');
 assert.match(out.detail.site,/^operation\.write\./);
 assert.deepEqual({pointerBytes:out.detail.pointerBytes,payloadBytes:out.detail.payloadBytes,writeBoundBytes:out.detail.writeBoundBytes},{pointerBytes:1024,payloadBytes:Number.MAX_SAFE_INTEGER,writeBoundBytes:-1});
 assertScalarDetail(out.detail);
 assert.equal(writer.pointer,1024);
 assert.equal(writer.eioFlushSlices,null);
 assert.equal(track.sampleCount,0);
 assert.deepEqual(track.buffers,[]);
 assert.equal(task.context.currentFragment.firstWrote,false);
});
test('stub writer that overshoots its window reports operation.write.pointer and copies no payload',()=>{
 /* Clearly marked stub: it only drives the post-write pointer guard. This is a
    guard-boundary test, not a modeled writer and not real-Worker proof. */
 const api=eioPlanApi();
 const state={writes:0,copiedBytes:0,pointer:8,window:64};
 const stub={eioFlushSlices:[],getPos:()=>0n,getPointer:()=>state.pointer,getBufferSize:()=>state.window,reset(){},flush(){},seek(){},seekInline(){},setEndian(){},writeUint8(){},writeUint32(){},writeUint64(){},writeString:()=>0,writeBuffer(chunk){state.writes+=1;state.pointer=state.window+1}};
 const track={streamIndex:0,trackId:9,ioWriter:stub,buffers:[],sampleSizes:[],sampleCount:0};
 const task={context:{currentFragment:{tracks:[track],firstWrote:false}}};
 const out=api.eioPlanOperationWrite(task,{streamIndex:0,payloadBytes:16});
 assert.equal(out.supported,false);
 assert.equal(out.reason,'plan-overflow');
 assert.equal(out.detail.site,'operation.write.pointer');
 assert.deepEqual({pointerBytes:out.detail.pointerBytes,windowBytes:out.detail.windowBytes,payloadBytes:out.detail.payloadBytes},{pointerBytes:65,windowBytes:64,payloadBytes:16});
 assertScalarDetail(out.detail);
 assert.equal(state.writes,1);
 assert.equal(state.copiedBytes,0);
 assert.equal(track.sampleCount,0);
 assert.deepEqual(track.sampleSizes,[]);
});
test('explicit plan detail survives prepare and source-open status propagation',async()=>{
 const prepareBody=between(source,'eioPrepareOperation(t,kind,opts){','eioOperationExecute(t,eioPlan,run){');
 const prepare=vm.runInNewContext('({'+prepareBody+'})',{eioMseError:code=>Object.assign(new Error(code),{code})});
 prepare.eioAbortStaleOperation=()=>{};
 for(const detail of [0,false,null]){
  prepare.eioPlanFor=()=>({ok:false,reason:'plan-overflow',detail});
  assert.throws(()=>prepare.eioPrepareOperation({},'fragment',{}),err=>{
   assert.equal(err.code,'EIO_MSE_PLAN_UNSUPPORTED');
   assert.equal(err.planReason,'plan-overflow');
   assert.equal(Object.is(err.planDetail,detail),true,JSON.stringify({detail}));
   return true
  });
 }
 const handlerBody=between(source,'getSourceOpenHandler(t){','getMimeType(t){');
 const handler=vm.runInNewContext('({'+handlerBody+'})');
 for(const detail of [0,false,null,undefined]){
  const error=new Error('plan refused with a https://upstream.invalid/path');
  error.code='EIO_MSE_PLAN_UNSUPPORTED';error.planReason='plan-overflow';
  if(void 0!==detail)error.planDetail=detail;
  const task={eioOwned:{},syncToKeyframe:async()=>{throw error}};
  handler.syncToKeyframe=task.syncToKeyframe;
  await assert.rejects(handler.getSourceOpenHandler(task)(),err=>err===error);
  assert.equal(task.eioSourceOpenError.code,'EIO_MSE_PLAN_UNSUPPORTED');
  assert.equal(task.eioSourceOpenError.planReason,'plan-overflow');
  assert.equal(Object.is(task.eioSourceOpenError.planDetail,void 0===detail?null:detail),true,JSON.stringify(task.eioSourceOpenError));
  assert.equal(task.eioSourceOpenError.message.includes('upstream.invalid'),false);
 }
});
test('empty EOF preflights before stop or mux and retries without fake completion',()=>{
 const calls=[];let admit=false;
 const body=between(source,'eioFinishLoop(t){','eioOwnedManager(){');
 const obj=vm.runInNewContext('(new (class {'+body+'})())',{Q:{dJ:()=>calls.push('trailer'),bX:()=>calls.push('flush')}});
 Object.assign(obj,{eioPrepareOperation:()=>{calls.push('admit');return admit},eioScheduleWake:()=>calls.push('wake'),eioOperationSeal:()=>calls.push('seal'),eioOperationAbort:()=>calls.push('abort'),eioAddQueue:()=>calls.push('queue')});
 const t={eioOwned:{},loop:{stop:()=>calls.push('stop'),emptyTask:()=>calls.push('empty')},track:{end:()=>calls.push('end')},bufferQueue:{size:4}};
 obj.eioFinishLoop(t);assert.deepEqual(calls,['admit','wake','empty']);assert.equal(t.ended,undefined);
 calls.length=0;admit=true;obj.eioFinishLoop(t);assert.deepEqual(calls,['admit','stop','trailer','flush','queue','seal','end']);
});
