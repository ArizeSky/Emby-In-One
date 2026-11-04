'use strict';
const test=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
const source=fs.readFileSync('public/vendor/web/libmedia/26.avplayer.js','utf8');
function method(start,end){const a=source.indexOf(start),b=source.indexOf(end,a);assert.ok(a>=0&&b>a);return source.slice(a,b)}
test('actual pause clears wake scheduling without cancelling an in-flight seek admission operation', {timeout:2000}, async()=>{
  const host=vm.runInNewContext('({'+[
    method('async pause(t){','async unpause(t){'),
    method('eioPauseWake(t){','eioCancelWake(t){'),
    method('async eioRunOperationWait(t,kind,opts,run){','eioScheduleWake(t){')
  ].join(',')+'})',{setTimeout,clearTimeout,EIO_MSE_RETRY_MS:25,eioMseError:code=>Object.assign(new Error(code),{code}),C:{A:()=>1},T:{h2:assert.fail,Yz(){}},L:'test'});
  let acquired=0,stopped=0;
  const stream={eioOpGeneration:4,eioWaitTimer:setTimeout(()=>assert.fail('paused wake must be cleared'),1000),loop:{stop(){stopped++}}};
  const task={audio:stream};host.tasks=new Map([[7,task]]);
  Object.assign(host,{eioAbortStaleOperation(){},eioPlanFor:()=>({ok:true,plan:{}}),eioOperationAcquire:()=>++acquired===1?null:{},eioOperationOpen(){},eioOperationSeal(){},eioOperationAbort(){assert.fail('operation must not be aborted')}});
  const waiting=host.eioRunOperationWait(stream,'packet',{},()=>42);
  await host.pause(7);
  assert.equal(task.pausing,true);assert.equal(stopped,1);assert.equal(stream.eioWaitTimer,null);
  assert.equal(stream.eioOpGeneration,4,'pause is not a destructive generation transition');
  assert.equal(await waiting,42);
});
