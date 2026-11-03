'use strict';
const test=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const vm=require('node:vm');
const path=require('node:path');
function load(){
  const modules={},cache={};
  const self={webpackChunkAVPlayer:{push:([,factories])=>Object.assign(modules,factories)}};
  const context=vm.createContext({self,console,setTimeout,clearTimeout});
  for(const file of ['mux-producers.js','mux-producer-lifecycle.js'])vm.runInContext(fs.readFileSync(path.join(__dirname,'../public/vendor/web/libmedia',file),'utf8'),context);
  function requireModule(id){if(cache[id])return cache[id].exports;const module=cache[id]={exports:{}};modules[id](module,module.exports,requireModule);return module.exports}
  return requireModule(90198);
}
function deferred(){let resolve;const promise=new Promise(r=>{resolve=r});return {resolve,promise}}
test('real mux adapter waits for the whole sourceOpen producer beyond settled pulls before mutation',async()=>{
  const api=load(),resume=deferred(),entered=deferred(),events=[];
  const task={eioOwnedResources:{},audio:{eioOwned:{},loop:{stop:()=>events.push('stop')}}};
  const host={eioCancelWake:()=>events.push('cancel'),eioPullLifetime:()=>({revoke:()=>events.push('revoke'),drain:async()=>{events.push('pull-settled');return {ok:true}}})};
  const producer=api.run(task,'sourceOpen',async()=>{entered.resolve();await resume.promise;events.push('old-producer-exit')});
  await entered.promise;
  let replaced=false;
  const mutation=(async()=>{await api.quiesce(host,task,'reAdd');return api.mutate(task,'reAdd',async token=>{api.requireMutation(task,token);replaced=true;events.push('destroy-old');await Promise.resolve();events.push('install-new')})})();
  await new Promise(r=>setTimeout(r,5));
  assert.equal(replaced,false);
  assert.ok(events.includes('pull-settled'));
  assert.throws(()=>api.run(task,'late-sourceOpen',()=>assert.fail('closed admission')));
  resume.resolve();await producer;await mutation;
  assert.ok(events.indexOf('old-producer-exit')<events.indexOf('destroy-old'));
  assert.equal(task.eioMuxMutation,null);
  api.gate(task).reopen();assert.equal(api.run(task,'new-loop',()=>17),17);
});
test('actual adapter retains exclusive mutation across await and rejects competing reAdd',async()=>{
  const api=load(),task={eioOwnedResources:{}},resume=deferred();
  api.gate(task).close('seek');
  const first=api.mutate(task,'reAdd',async token=>{api.requireMutation(task,token);await resume.promise;api.requireMutation(task,token)});
  await assert.rejects(api.mutate(task,'unregister',()=>assert.fail('overlapping mutation')));
  assert.throws(()=>api.gate(task).reopen());
  resume.resolve();await first;assert.equal(task.eioMuxMutation,null);
  await assert.rejects(api.mutate(task,'failure',()=>{throw new Error('destroy failed')}),/destroy failed/);
  assert.equal(api.gate(task).snapshot().closed,true);
});
test('queued destructive transitions cannot invalidate a destroy that is already committing',async()=>{
  const api=load(),task={eioOwnedResources:{}},started=deferred(),resume=deferred(),events=[];
  const restart=api.transition(task,async()=>{
    api.gate(task).close('restart');
    await api.mutate(task,'restart',async token=>{events.push('destroy');started.resolve();await resume.promise;api.requireMutation(task,token);events.push('replace')});
    api.gate(task).reopen();
  });
  await started.promise;
  const unregister=api.transition(task,async()=>{api.gate(task).close('unregister');await api.mutate(task,'unregister',()=>events.push('unregister'));api.gate(task).retire()});
  assert.deepEqual(events,['destroy']);resume.resolve();await restart;await unregister;
  assert.deepEqual(events,['destroy','replace','unregister']);
});
test('source identity and one-shot initialization reject old queued and duplicate sourceopen events',async()=>{
  const api=load(),task={eioOwnedResources:{},mediaSource:{}},resume=deferred();let initialized=0;
  const old=api.sourceHandler(task,()=>{initialized++});task.mediaSource={};old();assert.equal(initialized,0);
  const current=api.sourceHandler(task,async()=>{initialized++;await resume.promise});
  const first=current();assert.equal(current(),undefined);assert.equal(initialized,1);resume.resolve();await first;
  api.gate(task).close('beforeSeek');
  assert.equal(api.sourceHandler(task,()=>assert.fail('closed'))(),undefined);
  api.gate(task).retire();assert.equal(api.sourceHandler(task,()=>assert.fail('retired'))(),undefined);
});
