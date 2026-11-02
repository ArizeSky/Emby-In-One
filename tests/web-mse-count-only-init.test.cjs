'use strict';
const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const vm=require('node:vm');
const path=require('node:path');
const source=require('./web-mse-source-graph.cjs')();

/* The vendor bundle is minified. These tests extract the shipped method text
   with balanced braces and evaluate that text verbatim; the fixture never
   restates or rewrites the init body. */
function balancedBlock(text,openIndex){
 assert.equal(text[openIndex],'{');
 let depth=0;
 for(let i=openIndex;i<text.length;i++){
  const ch=text[i];
  if(ch==='{')depth+=1;
  else if(ch==='}'){
   depth-=1;
   if(depth===0)return text.slice(openIndex,i+1)
  }
 }
 assert.fail('unbalanced block in vendor source');
}
/* ACTUAL module 88560 M.init(t): the method extracted from the shipped
   OIsobmffFormat class, not a copy. */
function actualInitMethod(){
 const moduleStart=source.indexOf('88560:(t,e,i)=>{');
 assert.ok(moduleStart>=0,'module 88560 must stay in the scoped bundle');
 const nameIndex=source.indexOf('init(t){',moduleStart);
 assert.ok(nameIndex>=0,'module 88560 init(t) must stay extractable');
 const body=balancedBlock(source,nameIndex+'init(t)'.length);
 const method='init(t)'+body;
 /* Guard that the extract is the gated init and not an unrelated method. */
 assert.match(method,/this\.eioCountOnlyPlan\s*!==\s*(?:!0|true)/);
 assert.match(method,/annexb2AvccFilter/);
 assert.match(method,/this\.avpacket=/);
 assert.match(method,/t\.ioWriter\.setEndian\(!0\)/);
 assert.match(method,/this\.context\.metadata=t\.metadata/);
 assert.match(method,/this\.context\.chapters=t\.chapters/);
 return method
}
/* ACTUAL eioPlanCreateShadow: extracted from the planner section. */
function actualShadowCreator(){
 const start=source.indexOf('function eioPlanCreateShadow(');
 assert.ok(start>=0,'eioPlanCreateShadow must exist');
 const open=source.indexOf('{',start);
 assert.ok(open>start);
 return source.slice(start,open)+balancedBlock(source,open)
}
/* Fake native allocator / filter boundary. These counters show which native
   calls the real shipped method body would make; they are NOT an actual
   Worker, native allocator or BSF acceptance test. */
function initFixture(){
 const counters=[];
 class FakeAnnexb2AvccFilter{
  constructor(reverseSps){counters.push({kind:'filter-constructor',reverseSps})}
  init(codecpar,timeBase){counters.push({kind:'filter-init',codecpar,timeBase})}
 }
 const api=vm.runInNewContext('({'+actualInitMethod()+'})',{
  m:{A:FakeAnnexb2AvccFilter},
  r:{o9:'nativeIndex'},
  b:{_5:()=>{counters.push({kind:'packet-allocate'});return 0x5150}}
 });
 const host=streams=>({
  ioWriter:{setEndian(value){counters.push({kind:'io-endian',value})}},
  streams,
  metadata:{encoder:'fixture-encoder'},
  chapters:[{title:'fixture-chapter'}]
 });
 return {counters,host,init:api.init}
}
const VIDEO_H264={codecId:27,codecType:0,nativeIndex:11};
const VIDEO_HEVC={codecId:173,codecType:0,nativeIndex:12};
const AUDIO_AAC={codecId:86018,codecType:1,nativeIndex:31};
function stream(codecpar){return {codecpar,timeBase:{num:1,den:1000,nativeIndex:22}}}
function kinds(counters){return counters.map(entry=>entry.kind)}

test('count-only shadow init allocates no BSF/filter/packet and keeps header context',()=>{
 const f=initFixture();
 const instance={options:{reverseSpsInAvcc:true},context:{},eioCountOnlyPlan:true};
 const host=f.host([stream(VIDEO_H264)]);
 assert.equal(f.init.call(instance,host),0);
 assert.deepEqual(kinds(f.counters),['io-endian']);
 assert.equal(f.counters[0].value,true);
 assert.equal(instance.annexb2AvccFilter,undefined);
 assert.equal(instance.avpacket,undefined);
 assert.equal(instance.context.metadata,host.metadata);
 assert.equal(instance.context.chapters,host.chapters);
});

test('baseline video init preserves original constructor, filter init and packet allocation order',()=>{
 const f=initFixture();
 const instance={options:{reverseSpsInAvcc:false},context:{}};
 const host=f.host([stream(VIDEO_H264),stream(VIDEO_HEVC)]);
 assert.equal(f.init.call(instance,host),0);
 assert.deepEqual(kinds(f.counters),['io-endian','filter-constructor','filter-init','packet-allocate']);
 assert.equal(f.counters[0].value,true);
 assert.equal(f.counters[1].reverseSps,false);
 assert.deepEqual({codecpar:f.counters[2].codecpar,timeBase:f.counters[2].timeBase},{codecpar:11,timeBase:22});
 /* The original loop allocates one filter for the first AnnexB/HEVC codec
    only; the second video stream must not create another filter. */
 assert.equal(f.counters.filter(entry=>entry.kind==='filter-constructor').length,1);
 assert.ok(instance.annexb2AvccFilter);
 assert.equal(instance.avpacket,0x5150);
 assert.equal(instance.context.metadata,host.metadata);
 assert.equal(instance.context.chapters,host.chapters);
});

test('baseline AAC-only and repeated video/AAC scenarios keep their original order',()=>{
 for(const video of [true,false]){
  for(let attempt=0;attempt<2;attempt++){
   const f=initFixture();
   const instance={options:{reverseSpsInAvcc:true},context:{}};
   const host=f.host([stream(video?VIDEO_H264:AUDIO_AAC)]);
   assert.equal(f.init.call(instance,host),0,video?'video':'AAC');
   assert.deepEqual(kinds(f.counters),video
    ?['io-endian','filter-constructor','filter-init','packet-allocate']
    :['io-endian','packet-allocate'],video?'video':'AAC');
   assert.equal(f.counters[0].value,true);
   if(video)assert.ok(instance.annexb2AvccFilter);
   else assert.equal(instance.annexb2AvccFilter,undefined);
  }
 }
});

test('only an exact true planning marker skips; missing, false and other markers allocate',()=>{
 const cases=[
  ['missing',undefined,true],
  ['false',false,true],
  ['zero',0,true],
  ['truthy non-true',1,true],
  ['exact true',true,false]
 ];
 for(const [label,value,allocates] of cases){
  const f=initFixture();
  const instance={options:{reverseSpsInAvcc:false},context:{}};
  if(void 0!==value)instance.eioCountOnlyPlan=value;
  const host=f.host([stream(VIDEO_H264)]);
  assert.equal(f.init.call(instance,host),0,label);
  assert.deepEqual(kinds(f.counters),allocates
   ?['io-endian','filter-constructor','filter-init','packet-allocate']
   :['io-endian'],label);
  assert.equal(instance.context.metadata,host.metadata,label);
  assert.equal(instance.context.chapters,host.chapters,label);
 }
});

test('actual eioPlanCreateShadow marks only its shadow and does not mutate real options/context',()=>{
 const f=initFixture();
 let constructedOptions=null;
 class FakeFormat{
  constructor(options){constructedOptions=options;this.options=Object.assign({},options);this.context={metadata:null,chapters:null}}
 }
 FakeFormat.prototype.init=f.init;
 const creator=vm.runInNewContext('('+actualShadowCreator()+')',{R:{A:FakeFormat}});
 const writerFactory=()=>({eioFlushSlices:[]});
 const realOptions={fragment:true,hasTfra:false,reverseSpsInAvcc:true,eioWriterFactory:writerFactory};
 const realOptionsSnapshot=Object.assign({},realOptions);
 const realContext={metadata:{encoder:'real-format'},chapters:null,seed:1};
 const realFormat={options:realOptions,context:realContext};
 const realContextKeys=Object.keys(realContext);

 const shadow=creator(realFormat,writerFactory);
 assert.ok(shadow instanceof FakeFormat);
 /* The marker is attached after construction and only to the instance; it is
    never passed to the format constructor as an option. */
 assert.notEqual(constructedOptions,realOptions);
 assert.equal(constructedOptions.eioWriterFactory,undefined);
 assert.equal(constructedOptions.eioCountOnlyPlan,undefined);
 assert.equal(shadow.eioCountOnlyPlan,true);
 assert.equal(Object.prototype.hasOwnProperty.call(shadow,'eioCountOnlyPlan'),true);
 assert.equal(shadow.options.eioCountOnlyPlan,undefined);
 assert.equal(shadow.options.eioWriterFactory,undefined);
 assert.equal(shadow.eioOwned.writerFactory,writerFactory);
 assert.notEqual(shadow.options,realOptions);

 /* Only the returned shadow is marked: the real format, its options object
    and its context are untouched, including the caller's writer factory. */
 assert.equal(realFormat.eioCountOnlyPlan,undefined);
 assert.equal(realOptions.eioCountOnlyPlan,undefined);
 assert.equal(realOptions.eioWriterFactory,writerFactory);
 assert.deepEqual(realOptions,realOptionsSnapshot);
 assert.equal(realFormat.context,realContext);
 assert.deepEqual(Object.keys(realContext),realContextKeys);
 assert.equal(realContext.metadata.encoder,'real-format');

 /* The marked shadow runs the actual extracted init without native clones. */
 f.counters.length=0;
 assert.equal(shadow.init(f.host([stream(VIDEO_H264)])),0);
 assert.deepEqual(kinds(f.counters),['io-endian']);
 assert.equal(shadow.avpacket,undefined);

 /* An unmarked instance built from the same real options (and holding an
    ioWriter) still allocates; the marker is not inferred from writer state. */
 const baseline=new FakeFormat(realOptions);
 baseline.ioWriter={setEndian(){}};
 f.counters.length=0;
 assert.equal(baseline.init(f.host([stream(VIDEO_H264)])),0);
 assert.deepEqual(kinds(f.counters),['io-endian','filter-constructor','filter-init','packet-allocate']);
 assert.ok(baseline.annexb2AvccFilter);
 assert.equal(baseline.avpacket,0x5150);
 assert.equal(baseline.eioCountOnlyPlan,undefined);

 /* The marker is independent of the count-only writer factory presence. */
 const shadowWithoutFactory=creator({options:{fragment:true},context:{}},undefined);
 assert.equal(shadowWithoutFactory.eioCountOnlyPlan,true);
 assert.equal(shadowWithoutFactory.options.eioCountOnlyPlan,undefined);
});

test('real runtime init remains allocating: runtime BSF residency is caller-supplied and uncharged',()=>{
 /* Method-boundary limitation, not Worker acceptance. The real runtime path
    (main module 67669 Q.ho) calls module 88560 init on the real format context
    without this planning marker, so it still allocates the Annexb2Avcc input
    codecpar/cache and the output AVPacket. BSF residency therefore remains an
    unmeasured member that callers must supply as bsfResidentBytes; the
    "init-bsf-avpacket" incomplete reason is unchanged. */
 const f=initFixture();
 const runtime={options:{reverseSpsInAvcc:false},context:{}};
 assert.equal(f.init.call(runtime,f.host([stream(VIDEO_H264)])),0);
 assert.deepEqual(kinds(f.counters),['io-endian','filter-constructor','filter-init','packet-allocate']);
 assert.ok(runtime.annexb2AvccFilter);
 assert.equal(runtime.avpacket,0x5150);
});
