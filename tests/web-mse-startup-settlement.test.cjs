'use strict';
const test=require('node:test');
const assert=require('node:assert/strict');
const load=require('./helpers/mux-lifecycle.cjs');
function deferred(){let resolve,reject;const promise=new Promise((a,b)=>{resolve=a;reject=b});return {promise,resolve,reject}}
function task(){return {eioOwnedResources:{},mediaSource:{}}}
test('real DOM adapter observes startup rejection and reports actual failure, not ready',async()=>{
  const api=load(),t=task(),pending=deferred();
  const event=api.sourceHandler(t,()=>api.run(t,'sourceOpen',()=>pending.promise));
  const ack=api.waitSource(t,()=>t);
  event();pending.reject(Object.assign(new Error('capacity'),{code:'EIO_PROBE_BUDGET_EXHAUSTED'}));
  const result=await ack;
  assert.equal(result.ok,false);assert.equal(result.code,'EIO_PROBE_BUDGET_EXHAUSTED');
  assert.equal(api.gate(t).snapshot().closed,true);
  assert.equal(await api.gate(t).drain(50),true);
});
test('ready barrier waits for the complete producer and cancellation cannot acknowledge a replacement source',async()=>{
  const api=load(),t=task(),pending=deferred();
  const event=api.sourceHandler(t,()=>pending.promise);
  let settled=false;const ack=api.waitSource(t,()=>t).then(r=>{settled=true;return r});
  event();await new Promise(r=>setTimeout(r,30));assert.equal(settled,false);
  t.mediaSource={};pending.resolve();
  assert.equal((await ack).code,'EIO_MSE_STARTUP_CANCELLED');
  const next=api.sourceHandler(t,()=>undefined);next();
  assert.equal((await api.waitSource(t,()=>t)).ok,true);
});
test('closed startup consumes obsolete rejection without poisoning the next run',async()=>{
  const api=load(),t=task(),pending=deferred();
  const event=api.sourceHandler(t,()=>pending.promise);event();
  const ack=api.waitSource(t,()=>t);api.gate(t).close('unregister');
  pending.reject(new Error('old run'));assert.equal((await ack).code,'EIO_MSE_STARTUP_CANCELLED');
  assert.equal(t.eioSourceOpenError,undefined);
});
test('startup timeout reports failure but retains the real pending producer until it exits',async()=>{
  const api=load(),t=task(),pending=deferred();
  api.sourceHandler(t,()=>api.run(t,'sourceOpen',()=>pending.promise))();
  const result=await api.waitSource(t,()=>t,30);
  assert.equal(result.code,'EIO_MSE_STARTUP_TIMEOUT');
  await assert.rejects(api.gate(t).drain(20));
  pending.resolve();await new Promise(r=>setTimeout(r,0));
  assert.equal(await api.gate(t).drain(20),true);
});
