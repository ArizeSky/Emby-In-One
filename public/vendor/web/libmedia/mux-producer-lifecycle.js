"use strict";
(self.webpackChunkAVPlayer=self.webpackChunkAVPlayer||[]).push([[90212],{
90198:(module,exports,__webpack_require__)=>{
  const api=__webpack_require__(90197);
  function gate(task){
    if(!task||!task.eioOwnedResources)return null;
    return task.eioMuxProducers||(task.eioMuxProducers=api.create());
  }
  function run(task,label,callback){
    const owner=gate(task);
    if(!owner)return callback();
    const ticket=owner.enter(label);
    try{
      const value=callback();
      if(value&&typeof value.then==='function')return Promise.resolve(value).finally(()=>ticket.finish());
      ticket.finish();return value;
    }catch(error){ticket.finish();throw error}
  }
  function waitCallback(task,register){
    const waiting=new Promise((resolve,reject)=>{
      const waits=task.eioMuxCallbackWaits||(task.eioMuxCallbackWaits=new Set());
      let sink=resolve,timer;
      function finish(error){
        if(!sink)return;
        const done=sink;sink=null;clearTimeout(timer);waits.delete(cancel);
        if(error)reject(error);else done();
      }
      function cancel(){const error=new Error('EIO_MUX_CALLBACK_CANCELLED');error.code=error.message;finish(error)}
      waits.add(cancel);
      timer=setTimeout(()=>{const error=new Error('EIO_MUX_CALLBACK_TIMEOUT');error.code=error.message;finish(error)},5000);
      try{register(()=>finish())}catch(error){finish(error)}
    });
    // A sibling callback may be registered before another awaited mux flush.
    // Observe early cancellation now; consumers still receive the rejection.
    waiting.catch(()=>{});
    return waiting;
  }
  function cancelWaits(task){
    if(task&&task.eioMuxCallbackWaits)for(const cancel of Array.from(task.eioMuxCallbackWaits))cancel();
  }
  function sourceHandler(task,callback){
    const startup={result:null};
    task.eioSourceStartup=startup;
    const source=task.mediaSource;
    let started=false;
    return (...args)=>{
      const owner=gate(task);
      if(owner){
        if(task.mediaSource!==source||started||owner.snapshot().retired||owner.snapshot().closed)return;
        started=true;
        startup.epoch=owner.snapshot().epoch;
      }
      if(!owner)return callback(...args);
      const failed=error=>{
        if(task.mediaSource!==source||task.eioSourceStartup!==startup||owner.snapshot().retired||owner.snapshot().closed||owner.snapshot().epoch!==startup.epoch)return;
        const code=error&&typeof error.code==='string'&&/^(?:EIO_[A-Z0-9_]+|capacity-exceeded|busy|entries-exhausted)$/.test(error.code)?error.code:'EIO_MSE_STARTUP_FAILED';
        task.eioSourceOpenError={code,message:code};
        owner.close('sourceOpen-failed');
        startup.result={ok:false,code};
      };
      // DOM event dispatch does not observe a returned rejected promise.
      try{return Promise.resolve(callback(...args)).then(()=>{if(task.mediaSource===source&&task.eioSourceStartup===startup&&!owner.snapshot().closed&&!owner.snapshot().retired&&owner.snapshot().epoch===startup.epoch)startup.result={ok:true}},failed)}catch(error){failed(error)}
    };
  }
  function transition(task,callback){
    if(!gate(task))return callback();
    const previous=task.eioMuxTransition||Promise.resolve();
    const current=previous.catch(()=>{}).then(callback);
    task.eioMuxTransition=current;
    return current;
  }
  async function quiesce(host,task,label){
    try{return await quiesceInternal(host,task,label)}catch(error){
      if(task)task.eioPullLastError={stage:label,code:error&&error.code||'EIO_MUX_QUIESCE_FAILED',message:'mux quiescence failed'};
      throw error;
    }
  }
  async function quiesceInternal(host,task,label){
    const owner=gate(task);
    if(!owner)return;
    owner.close(label);
    cancelWaits(task);
    if((label==='restart'||label==='unregister')&&task.mediaSource)task.mediaSource.onsourceopen=null;
    const lifetime=host.eioPullLifetime(task),streams=[];
    for(const stream of [task.audio,task.video]){
      if(!stream)continue;
      host.eioCancelWake(stream);
      if(stream.loop)stream.loop.stop();
      if(stream.eioOwned){lifetime.revoke(stream,label);streams.push(stream)}
    }
    // Revocation alone does not settle an IPC pull waiting on demux IO or
    // admission. Cancel that exact outstanding request at its owner; a late
    // positive result still goes through the packet ownership reclaim path.
    await Promise.all(streams.map(async stream=>{
      if(!stream.pullIPC)return;
      let timer;
      try{
        const ack=await Promise.race([stream.pullIPC.request('eioCancelPull'),new Promise(resolve=>{timer=setTimeout(()=>resolve(0),2000)})]);
        if(ack!==1){const error=new Error('EIO_MUX_PULL_CANCEL_UNVERIFIED');error.code=error.message;throw error}
      }finally{clearTimeout(timer)}
    }));
    // Pull settlement cannot stand in for completion of sourceOpen/startMux or
    // afterSeek. Keep admission closed until their entire promises have exited.
    const pulled=await lifetime.drain(streams,5000);
    if(!pulled.ok){const error=new Error('EIO_MUX_PULL_SETTLE_TIMEOUT');error.code=error.message;throw error}
    await owner.drain(5000);
  }
  async function mutate(task,label,callback){
    const owner=gate(task);
    if(!owner)return callback();
    const token=owner.beginMutation(label);
    task.eioMuxMutation=token;
    try{token.guard();const value=await callback(token);token.guard();return value}
    finally{task.eioMuxMutation=null;token.finish()}
  }
  function requireMutation(task,token){
    if(!gate(task))return;
    if(!token||token!==task.eioMuxMutation){const error=new Error('EIO_MUX_MUTATION_REQUIRED');error.code=error.message;throw error}
    task.eioMuxMutation.guard();
  }
  async function waitSource(task,current,timeoutMs=30000){
    const owner=gate(task),startup=task&&task.eioSourceStartup,source=task&&task.mediaSource;
    if(!owner||!startup)return {ok:false,code:'EIO_MSE_STARTUP_MISSING'};
    const epoch=owner.snapshot().epoch;
    const deadline=Date.now()+timeoutMs;
    for(;;){
      if(current()!==task||task.mediaSource!==source||task.eioSourceStartup!==startup)
        return {ok:false,code:'EIO_MSE_STARTUP_CANCELLED'};
      if(startup.result&&startup.result.ok===false)return startup.result;
      const state=owner.snapshot();
      if(state.closed||state.retired||state.epoch!==epoch)return {ok:false,code:'EIO_MSE_STARTUP_CANCELLED'};
      if(startup.result)return startup.result;
      if(Date.now()>=deadline){owner.close('sourceOpen-timeout');return {ok:false,code:'EIO_MSE_STARTUP_TIMEOUT'}}
      await new Promise(resolve=>setTimeout(resolve,25));
    }
  }
  module.exports={gate,run,waitCallback,cancelWaits,sourceHandler,waitSource,transition,quiesce,mutate,requireMutation};
}
}]);
