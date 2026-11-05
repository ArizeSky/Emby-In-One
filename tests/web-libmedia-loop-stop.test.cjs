'use strict';
const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const vm=require('node:vm');
function LoopClass(){
 const text=fs.readFileSync('public/vendor/web/libmedia/avplayer.js','utf8');
 const begin=text.indexOf('57595:')+6,end=text.indexOf('},57708:',begin)+1;
 const factory=vm.runInNewContext('('+text.slice(begin,end)+')',{setTimeout,clearTimeout});
 class Clock { constructor(){this.started=false} start(){this.started=true} stop(){this.started=false} isStarted(){return this.started} }
 const modules={19371:{A:(o,k,v)=>{o[k]=v}},57708:{A:Clock},44905:{A:()=>0}},out={};
 const req=id=>modules[id];req.d=(o,defs)=>{for(const k in defs)Object.defineProperty(o,k,{get:defs[k]})};
 factory({},out,req);return out.A;
}
test('real async LoopTask joins overlapping seek and teardown stop requests at the same tick',{timeout:2000},async()=>{
 const Loop=LoopClass();let finish;
 const loop=new Loop(()=>new Promise(resolve=>{finish=resolve}),0,0,true,false);
 loop.start();loop.clock.onClock(()=>{});
 assert.equal(loop.processing,true);
 let stops=0;
 const seek=loop.stopBeforeNextTick().then(()=>{stops++});
 const destroy=loop.stopBeforeNextTick().then(()=>{stops++});
 await Promise.resolve();assert.equal(stops,0);
 finish(true);await Promise.all([seek,destroy]);
 assert.equal(stops,2);assert.equal(loop.isStarted(),false);assert.equal(loop.clock.isStarted(),false);
 loop.start();await loop.stopBeforeNextTick();
 assert.equal(loop.isStarted(),false,'an idle loop can stop immediately without waiting for another tick');
});
test('real LoopTask joins an active async tick even when the clock was already stopped',{timeout:2000},async()=>{
 const Loop=LoopClass();let finish;
 const loop=new Loop(()=>new Promise(resolve=>{finish=resolve}),0,0,true,false);
 loop.start();loop.clock.onClock(()=>{});loop.clock.stop();
 let stopped=false;
 const pending=loop.stopBeforeNextTick().then(()=>{stopped=true});
 await Promise.resolve();assert.equal(stopped,false);
 finish(false);await pending;
 assert.equal(loop.isStarted(),false);assert.equal(loop.processing,false);
});
