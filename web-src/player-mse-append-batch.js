/*! Charged batching of adjacent fMP4 append inputs. */
(function(root,factory){
  if(typeof module==='object'&&module.exports)module.exports=factory();
  else root.EioMseAppendBatch=factory();
})(typeof globalThis!=='undefined'?globalThis:this,function(){
  'use strict';
  const MAX_BYTES=1048576,MAX_ITEMS=32;
  // Queue order is a media contract: never cross remove, timestamp, MIME or
  // callback operations, and never copy raw unowned inputs on this path.
  function batchInput(input,queue,manager,generation){
    if(!input||!input.buffer||!input.eioClaim||input.callback||!manager)return false;
    let bytes=input.buffer.byteLength,count=0;
    if(bytes>=MAX_BYTES)return false;
    while(count<queue.length&&count<MAX_ITEMS-1){
      const next=queue[count];
      if(next.operator!==0||!next.buffer||!next.eioClaim||next.callback)break;
      if(bytes+next.buffer.byteLength>MAX_BYTES)break;
      bytes+=next.buffer.byteLength;count++;
    }
    if(!count)return false;
    // A refusal needs no waiting: appending the original input consumes and
    // releases a real claim, creating room for the next append attempt.
    const admitted=manager.acquire(bytes,'mse-append-batch',generation);
    if(!admitted||admitted.ok!==true)return false;
    const handle=admitted.handle;
    let merged;
    try{
      merged=new Uint8Array(bytes);
      merged.set(input.buffer);
      let offset=input.buffer.byteLength;
      for(let j=0;j<count;j++){
        merged.set(queue[j].buffer,offset);offset+=queue[j].buffer.byteLength;
      }
    }catch(error){
      merged=null;handle.release({referencesDropped:true});throw error;
    }
    const originals=[{buffer:input.buffer,eioClaim:input.eioClaim},...queue.splice(0,count)];
    let released=false;
    input.buffer=merged;
    input.eioClaim={drop(){
      if(released)return;
      released=true;
      // Core failures stay charged and visible in the manager snapshot.
      handle.release({referencesDropped:true});
    }};
    for(let j=0;j<originals.length;j++){
      const item=originals[j],claim=item.eioClaim;
      item.buffer=null;item.eioClaim=null;originals[j]=null;
      claim.drop();
    }
    return true;
  }
  return Object.freeze({batchInput,MAX_ITEMS});
});
