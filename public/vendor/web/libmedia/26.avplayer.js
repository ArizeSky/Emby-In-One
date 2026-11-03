"use strict";(self.webpackChunkAVPlayer=self.webpackChunkAVPlayer||[]).push([[26],{26:(t,e,i)=>{i.r(e),i.d(e,{default:()=>$});var a=i(80209),r=i(54568),s=i(56238),n=i(49103),o=i(55982),c=i(6927),d=i(64017),u=i(35924),f=i(46564),l=i(46626),p=i(26669),h=i(98901),m=i(29688),g=i(67669),k=i(23226),w=i(41933),b=i(20296),v=i(61457),U=i(20642),I=i(12062),P=i(22694),y=i(19784),S=i(937),A=i(51091),B=i(84777),x=i(83446),T=i(34117),C=i(44905),D=i(74817),M=i(74872),E=i(56289),z=i(57595),F=i(37765),N=i(15716),Q=i(14307),O=i(88962),R=i(88560),W=i(37766),_=i(2633),V=i(84044),q=i(16627);const L="packages/avplayer/src/mse/MSEPipeline.ts";const __eioSharedMediaDomainApi=i(90187);
function eioReadNativePlaybackClock(domain){
  return domain&&domain.nativeClockSab?i(90194).read(domain.nativeClockSab,domain.nonce):null;
}
/* Separate pure planner module; chunk loader resolves it before module 26. */
const {EioMuxCountOnlyWriter,eioPlanDiagnosticScalar,eioPlanOverflowDetail,
  eioPlanUnsupported,eioPlanSite,eioPlanIsCount,eioPlanSum,eioPlanAdd,eioPlanMul,
  eioPlanOperation,eioPlanMux,eioPlanOperationWrite,
  EIO_PLAN_METADATA_MAX_ENTRIES,EIO_PLAN_ASSUMPTIONS}=i(90200);
/* EIO-MSE-OWNED:BEGIN -- fixed mux writer windows, mux output copies and the
   SourceBuffer input references are charged to the shared ownership core that
   registerEioSharedMediaDomain(domain,2) attached to this task. Without that
   attachment every helper below is inert, so the original code path (no
   manager, no refusal, no snapshot fields) stays byte-identical. */
var __eioMseOwnedValue=null,__eioMseOwnedTried=!1;
function __eioMseOwnedRequire(){
  if(!__eioMseOwnedTried){
    __eioMseOwnedTried=!0;
    try{__eioMseOwnedValue=i(90189)||null}catch(err){__eioMseOwnedValue=null}
  }
  return __eioMseOwnedValue
}
const EIO_MSE_FIXED_WINDOW=1048576;
const EIO_MSE_CONSERVATIVE_LABEL="mse-conservative-writer-window";
const EIO_MSE_INCOMPLETE=Object.freeze(["bsf-resident","stream-codecpar-clones","extradata","other-conversion","native-clock","init-bsf-avpacket","plan-reservation"]);
const EIO_MSE_INCOMPLETE_REASONS=Object.freeze({
  "bsf-resident":"Annexb2Avcc filter cache/packets are not measured yet",
  "stream-codecpar-clones":"only persistent NATIVE input codecpar payload clones and strict serialized-input codecpar payload clones from addStream/reAddStream are admitted and charged before allocation; initial serialization payload copies (Wu), mux-output codecpar clones and in-place replacement lifetime remain uncovered",
  "extradata":"extradata copies are charged only at the box writer copy site",
  "other-conversion":"only the isobmff fragment mux copy class is covered",
  "native-clock":"SourceBuffer internal bytes and the native clock are excluded",
  "init-bsf-avpacket":"OMov.init native input codecpar payload clone is leased in the header scope; BSF cache/filter, AVPacket and conversion ownership remain unproved and uncharged",
  "plan-reservation":"the owned scope is a planner reservation upper bound, not measured live backing"
});
function eioMseError(t){const e=new Error(t);return e.code=t,e}
/* A claim is one controlled JS alias of a charged allocation. refs counts the
   live aliases (queue views, in-flight appendBuffer input); the core credit is
   requested exactly once, when the last alias is dropped. */
function eioMseDropClaim(t){
  if(!t||t.dropped===!0)return;
  t.refs-=1;
  if(t.refs>0)return;
  t.dropped=!0;
  const e=t.handle;
  if(e){
    let r=null;
    try{r=e.release({referencesDropped:!0})}catch(err){r=null}
    if(r&&r.ok===!0)t.handle=null;
    else{t.claimReleaseFailed=!0;t.claimReleaseError=r&&"string"==typeof r.code?r.code:"EIO_OWNED_RELEASE_FAILED"}
  }
  const i=t.scope;
  if(i){
    i.refs-=1;
    if(i.closed===!0&&i.refs<=0)eioMseReleaseScope(i)
  }
}
function eioMseNewClaim(t,e){
  const i={handle:t||null,scope:e||null,refs:1,dropped:!1};
  i.drop=function(){eioMseDropClaim(i)};
  return i
}
/* EIO-MSE-ATOMIC:BEGIN -- planner-backed single-acquisition mux operations.
   One mux operation (header, one packet, one forced flush, trailer) is charged
   as ONE owned scope acquired before any real mutation. Inside an active
   strict operation the copy/writer factories only CONSUME that scope; a copy
   beyond the planned residual is an implementation fault
   (EIO_MSE_PLAN_UNDERRUN), never a capacity-pressure retry. */
const EIO_MSE_PLAN_LABEL="mse-plan-workset";
const EIO_MSE_RETRY_MS=25;
const EIO_MSE_PRESSURE_CODES=Object.freeze(["capacity-exceeded","busy","entries-exhausted"]);
function eioMseIsPressure(t){return EIO_MSE_PRESSURE_CODES.indexOf(t)>=0}
/* A scope may only be reported free after the manager confirmed the release.
   A refused release keeps the handle and the failure visible. */
function eioMseReleaseScope(t){
  if(!t)return!1;
  if(t.releaseFailed===!0)return!1;
  const e=t.handle;
  if(!e){t.freed=!0;return!0}
  let i=null;
  try{i=e.release({referencesDropped:!0})}catch(err){i=null}
  if(i&&i.ok===!0){t.handle=null;t.freed=!0;return!0}
  t.releaseFailed=!0;
  t.releaseError=i&&"string"==typeof i.code?i.code:"EIO_OWNED_RELEASE_FAILED";
  return!1
}
function eioMseCountOnlyFactory(t){return new EioMuxCountOnlyWriter(t)}
/* Real BufferQueue chunk backing view lengths, exactly what a merge would copy. */
function eioMseQueueChunkBytes(t){
  const e=t&&t.bufferQueue&&t.bufferQueue.queue;
  if(!e)return[];
  const i=[];
  for(let n=0;n<e.length;n++){
    const s=e[n];
    if(!s||"number"!=typeof s.length||!Number.isSafeInteger(s.length))return null;
    i.push(s.length)
  }
  return i
}
function eioMsePacketPayload(t){
  if(!t||t<=0)return 0;
  const e=s.f[15](t+28);
  return Number.isSafeInteger(e)&&e>0?e:0
}
/* Header extradata kind from the real codecpar: H.264 AVCC is identified by the
   avcC first byte 1; anything else stays AnnexB and is refused by the planner. */
function eioMseExtradataKind(t){
  const e={};
  const i=t&&t.streams||[];
  for(let n=0;n<i.length;n++){
    const s=i[n],o=s&&s.codecpar||{};
    let c=null;
    try{c=o.extradata||null}catch(err){return null}
    const d=c&&(typeof c==="number"?o.extradataSize:c.length)>0?(typeof c==="number"?(0,a.s3)(c,o.extradataSize)[0]:c[0]):-1;
    e[s.index]=27===o.codecId?(1===d?"avcc":"annexb"):"copy"
  }
  return e
}
/* Actual known compressed extradata of the AnnexB->AVCC filter input codecpar
   (a byte copy of the stream codecpar). This is informational residentBefore
   only; it is never added as a workset charge. */
function eioMseBsfExtradataBytes(t,e){
  if(!e||!e.annexb2AvccFilter)return 0;
  let i=0;
  const n=t&&t.streams||[];
  for(let o=0;o<n.length;o++){
    const c=n[o]&&n[o].codecpar||{};
    let d=null;
    try{d=c.extradata||null}catch(err){return -1}
    const u=d&&"number"==typeof d.length?d.length:(Number.isSafeInteger(c.extradataSize)?c.extradataSize:0);
    if(!Number.isSafeInteger(u)||u<0)return -1;
    i+=u;
    if(!Number.isSafeInteger(i))return -1
  }
  return i
}
/* EIO-MSE-PROGRESS:BEGIN read-only observation helpers. Every helper degrades an
   absent, non-scalar or unreadable property to null instead of fabricating a
   false/zero measurement; nothing here writes to the observed objects. */
function eioProgressValue(t,e){try{return t?t[e]:null}catch(err){return null}}
function eioProgressBool(t,e){try{const i=t[e];return"boolean"==typeof i?i:null}catch(err){return null}}
function eioProgressPending(t,e){try{const i=t[e];return"number"==typeof i?i>0:null}catch(err){return null}}
function eioProgressNumber(t,e){try{const i=t[e];return"number"==typeof i&&Number.isFinite(i)?i:null}catch(err){return null}}
function eioProgressDecimal(t,e){
  try{
    const i=t[e];
    if("bigint"==typeof i)return i.toString();
    return"number"==typeof i&&Number.isSafeInteger(i)?String(i):null
  }catch(err){return null}
}
function eioProgressText(t,e){try{const i=t[e];return"string"==typeof i?i:null}catch(err){return null}}
function eioProgressMessage(t){
  try{return String(t&&t.message||t).replace(/https?:\/\/\S+/g,"[redacted-url]").slice(0,240)}
  catch(err){return"unreadable"}
}
/* The raw existing C.A() clock source is Date.now wallclock: it is neither
   native confirmed playback time nor guaranteed monotonic. */
function eioProgressClockMs(){try{const t=(0,C.A)();return"number"==typeof t&&Number.isFinite(t)?t:null}catch(err){return null}}
function eioProgressQueueLength(t){try{const e=t&&t.queue;return Array.isArray(e)?e.length:null}catch(err){return null}}
function eioProgressLoopStarted(t){
  try{
    if(!t||"function"!=typeof t.isStarted)return null;
    const e=t.isStarted();
    return"boolean"==typeof e?e:null
  }catch(err){return null}
}
/* The pending packet pointer itself never leaves the worker: only the DTS read
   from the real AVPacket, rescaled to milliseconds, is observed. */
function eioProgressBackPacketDts(t){
  try{
    const e=t.backPacket;
    if("number"!=typeof e||!(e>0))return null;
    const i=(0,h.Mr)(s.f[17](e+16),e+72,m.i0);
    if("bigint"==typeof i)return i.toString();
    return"number"==typeof i&&Number.isSafeInteger(i)?String(i):null
  }catch(err){return null}
}
/* Strict loop record: an explicit code/planReason/planDetail is kept as-is,
   an absent one stays null even when a stale value would read as zero/false. */
function eioProgressLoopFailure(t){
  let e=null,i=null,a=null,r=null;
  try{e=t&&"object"==typeof t?t:null}catch(err){e=null}
  try{i=e&&void 0!==e.code&&null!==e.code?String(e.code):null}catch(err){i=null}
  try{a=e&&void 0!==e.planReason?e.planReason:null}catch(err){a=null}
  try{r=e&&void 0!==e.planDetail?e.planDetail:null}catch(err){r=null}
  return{code:i,message:eioProgressMessage(t),planReason:a,planDetail:r}
}
function eioProgressLoopError(t){
  try{
    if(!t||"object"!=typeof t)return null;
    return{
      code:"string"==typeof t.code?t.code:null,
      message:"string"==typeof t.message?t.message:null,
      planReason:void 0===t.planReason?null:t.planReason,
      planDetail:void 0===t.planDetail?null:t.planDetail
    }
  }catch(err){return null}
}
/* EIO-MSE-PROGRESS:END */

class $ extends V.A{constructor(){super()}
eioPullLifetime(t){
  if(!t)return null;
  if(!t.eioPullLifetime){
    const eioPullApi=i(90192);
    if(!eioPullApi||"function"!=typeof eioPullApi.createMsePullLifetime)throw eioMseError("EIO_MUX_PULL_MODULE_MISSING");
    t.eioPullLifetime=eioPullApi.createMsePullLifetime({
      packetPool:t.avpacketPool,now:C.A,
      native:{
        readI32:p=>s.f[15](p),readU32:p=>s.f[20](p),readBigInt:p=>s.f[17](p),
        writeI32:(p,v)=>c.M[15](p,v),writeBigInt:(p,v)=>c.M[17](p,v),
        scale:(v,p,tb)=>(0,h.Mr)(v,p,tb),scaleInt:(v,a,b)=>(0,h.k)(v,a,b),
        minBig:(a,b)=>x.jk(a,b),maxBig:(a,b)=>x.T9(a,b),diffBig:(a,b)=>x.tn(a,b),
        millisecondsTimeBase:m.i0,noTimestamp:m.Dh,timeBaseIndex:r.o9,
        getSideData:(p,type)=>(0,f.rU)(p,type),setSideData:(p,type,d,n)=>(0,f.Ow)(p,type,d,n),setPacketData:(p,d,n)=>(0,f.NX)(p,d,n),
        allocBytes:n=>(0,l.sY)(n),writeBytes:(d,n,v)=>(0,a.lW)(d,n,v),
        readBytes:(p,n)=>(0,a.s3)(p,n),copyMemory:(d,p,n)=>(0,a.Mr)(d,p,n),
        copyPacket:(dst,src)=>(0,f.rN)(dst,src),isKeyframe:p=>1&s.f[15](p+36),
        isRandomAccessVideo:(st,p)=>27===s.f[15](st.codecpar+4)&&U.mW(p,s.f[20](st.codecpar+12)?1+(3&k.r8(s.f[20](st.codecpar+12)+4)):4)||173===s.f[15](st.codecpar+4)&&I.mW(p,s.f[20](st.codecpar+12)?1+(3&k.r8(s.f[20](st.codecpar+12)+21)):4),
        codecparId:st=>s.f[15](st.codecpar+4),releaseCodecpar:p=>(0,g.dn)(p),
        videoTimeBase:p=>(0,n.A)(p,o.Q),delay:ms=>new E.A(ms/1000)
      }
    })
  }
  return t.eioPullLifetime
}
eioPullHasStrict(t){return!!(t&&(t.audio&&t.audio.eioOwned||t.video&&t.video.eioOwned))}
seekSeconds(t){return P.yw(t)}
timeNoticeNeeded(t,e){return S.A.safari||A.A.ios||t>.1||e.playRate>BigInt(100)}
removeAllBufferNeeded(){return S.A.safari||A.A.ios}
logSeekEnd(t){T.Yz(`before seek end taskId: ${t}`,L,1432)}
logSeekDone(t,e){T.Yz(`after seek end, seekTime: ${t} taskId: ${e}`,L,1670)}
logDts(t,e){T.R8(`got packet with dts ${s.f[17](t+16)}, which is earlier then the last packet(${e})`,L,627)}
logPts(t,e){T.R8(`got video packet with pts ${s.f[17](t+8)}, which is earlier then the last packet(${e}), try to fix it!`,L,664)}
async syncToKeyframe(t,eioTickets){
  if(eioTickets||this.eioPullHasStrict(t)){
    const eioPull=this.eioPullLifetime(t);const own=!eioTickets;const tickets=eioTickets||eioPull.beginTickets(t,"syncToKeyframe");let completed=!1;
    try{const value=await eioPull.syncToKeyframe(t,this,tickets);completed=!0;return value}finally{if(own){if(completed)eioPull.complete(tickets,"syncToKeyframe");else eioPull.finishTickets(tickets,"syncToKeyframe")}}
  }
  let e=!0;if(t.video){let i;for(;;){if(t.video.backPacket=await this.pullAVPacket(t.video,t),t.video.backPacket<0){t.video.packetEnded=!0,t.video.backPacket=0;break}if(t.audio&&!t.audio.packetEnded&&(!t.audio.backPacket||(0,h.Mr)(s.f[17](t.audio.backPacket+8),t.audio.backPacket+72,m.i0)<(0,h.Mr)(s.f[17](t.video.backPacket+8),t.video.backPacket+72,m.i0)))for(;;){if(t.audio.backPacket>0){const e=(0,f.rU)(t.audio.backPacket,1);e&&(i=(0,a.s3)(s.f[20](e),s.f[25](e+4)).slice()),t.avpacketPool.release(t.audio.backPacket)}if(t.audio.backPacket=await this.pullAVPacket(t.audio,t),t.audio.backPacket<0){t.audio.packetEnded=!0,t.audio.backPacket=0;break}if((0,h.Mr)(s.f[17](t.audio.backPacket+8),t.audio.backPacket+72,m.i0)>(0,h.Mr)(s.f[17](t.video.backPacket+8),t.video.backPacket+72,m.i0))break}if(1&s.f[15](t.video.backPacket+36)||27===s.f[15](t.video.codecpar+4)&&U.mW(t.video.backPacket,s.f[20](t.video.codecpar+12)?1+(3&k.r8(s.f[20](t.video.codecpar+12)+4)):4)||173===s.f[15](t.video.codecpar+4)&&I.mW(t.video.backPacket,s.f[20](t.video.codecpar+12)?1+(3&k.r8(s.f[20](t.video.codecpar+12)+21)):4))break;e=!1,t.avpacketPool.release(t.video.backPacket)}if(t.audio&&t.audio.backPacket&&!(0,f.fQ)(t.audio.backPacket,1)&&i){const e=(0,l.sY)(i.length);(0,a.lW)(e,i.length,i),(0,f.Ow)(t.audio.backPacket,1,e,i.length)}}return e}getSourceOpenHandler(t){let e=arguments.length>1&&void 0!==arguments[1]?arguments[1]:BigInt(0);return async()=>{if(t&&(t.audio&&t.audio.eioOwned||t.video&&t.video.eioOwned))return this.eioPullLifetime(t).sourceOpen(t,this,e);try{var i,d;await this.syncToKeyframe(t);t.eioSourceOpenStage="synced";let u=e,p=e;if(!t.audio||t.audio.backPacket||t.audio.packetEnded||(t.audio.backPacket=await this.pullAVPacket(t.audio,t),t.audio.backPacket<0&&(t.audio.packetEnded=!0,t.audio.backPacket=0)),t.video&&!t.video.backPacket&&t.video.packetEnded&&(t.video.backPacket=await this.pullAVPacket(t.video,t),t.video.backPacket<0&&(t.video.packetEnded=!0,t.video.backPacket=0)),(null===(i=t.audio)||void 0===i?void 0:i.backPacket)>0&&(u=x.T9(u,(0,h.Mr)(s.f[17](t.audio.backPacket+16),t.audio.backPacket+72,m.i0))),(null===(d=t.video)||void 0===d?void 0:d.backPacket)>0&&(p=x.T9(p,(0,h.Mr)(s.f[17](t.video.backPacket+16),t.video.backPacket+72,m.i0))),t.audio&&t.video&&x.tn(u-p)>1e3)if(p>u){const e=(0,h.k)(u,m.i0,(0,n.A)(t.video.backPacket+72,o.Q));c.M[17](t.video.backPacket+48,s.f[17](t.video.backPacket+16)-e),c.M[17](t.video.backPacket+16,e),c.M[17](t.video.backPacket+8,e),p=u}else if(u>p&&86018===t.audio.oformatContext.streams[0].codecpar.codecId&&2===t.audio.oformatContext.streams[0].codecpar.profile&&t.audio.oformatContext.streams[0].codecpar.chLayout.nbChannels<3){const e=t.avpacketPool.alloc();(0,f.rN)(e,t.audio.backPacket);const i=1===t.audio.oformatContext.streams[0].codecpar.chLayout.nbChannels?new Uint8Array([0,200,0,128,35,128]):new Uint8Array([33,0,73,144,2,25,0,35,128]),r=(0,l.sY)(i.length);(0,a.lW)(r,i.length,i),(0,f.NX)(e,r,i.length),c.M[17](e+16,(0,h.k)(p,m.i0,(0,n.A)(e+72,o.Q))),c.M[17](e+8,(0,h.k)(p,m.i0,(0,n.A)(e+72,o.Q)));const d=BigInt(1024e3)/BigInt(0|t.audio.oformatContext.streams[0].codecpar.sampleRate),g=Number(BigInt.asIntN(32,(u-p)/d));let k=s.f[17](e+16)+(0,h.k)(d,m.i0,(0,n.A)(t.audio.backPacket+72,o.Q));for(let i=0;i<g-1;i++){const i=t.avpacketPool.alloc();(0,f.rN)(i,e),c.M[17](i+16,k),c.M[17](i+8,k),t.audio.pullQueue.queue.push(i),k+=(0,h.k)(d,m.i0,(0,n.A)(t.audio.backPacket+72,o.Q))}t.audio.pullQueue.queue.push(t.audio.backPacket),t.audio.backPacket=e,u=p}const g=[];t.audio&&(t.audio.track.setSourceBuffer(this.createSourceBuffer(t.mediaSource,t.audio.oformatContext.streams[0].codecpar[r.o9])),t.audio.enableRawMpeg||(this.eioHeaderOperation(t.audio)),g.push(this.startMux(t.audio,t,x.T9(u,p)))),t.video&&(t.video.track.setSourceBuffer(this.createSourceBuffer(t.mediaSource,t.video.oformatContext.streams[0].codecpar[r.o9])),t.video.enableRawMpeg||(this.eioHeaderOperation(t.video)),g.push(this.startMux(t.video,t,x.T9(u,p)))),await Promise.all(g);let k=!1,w=!1;t.audio&&(t.audio.backPacket=await this.pullAVPacket(t.audio,t),t.audio.backPacket>0?(k=!0,t.audio.frontPacket=await this.pullAVPacket(t.audio,t),t.audio.frontPacket>0?t.audio.frontBuffered=!0:(t.audio.frontPacket=0,t.audio.packetEnded=!0)):t.audio.packetEnded=!0,t.audio.enableRawMpeg||this.eioMuxFlushProbe(t.audio),this.eioAddQueue(t.audio.track,t.audio)),t.video&&(t.video.backPacket=await this.pullAVPacket(t.video,t),t.video.backPacket>0?(w=!0,t.video.frontPacket=await this.pullAVPacket(t.video,t),t.video.frontPacket>0?t.video.frontBuffered=!0:(t.video.frontPacket=0,t.video.packetEnded=!0)):t.video.packetEnded=!0,t.video.enableRawMpeg||this.eioMuxFlushProbe(t.video),this.eioAddQueue(t.video.track,t.video)),t.audio&&(k?(this.createLoop(t.audio,t),t.audio.loop.start()):t.audio.track.end()),t.video&&(w?(this.createLoop(t.video,t),t.video.loop.start()):t.video.track.end()),await new E.A(.1);let b=0;t.audio?b=t.video?Math.max(t.audio.track.getBufferedStart(),t.video.track.getBufferedStart()):t.audio.track.getBufferedStart():t.video&&(b=t.video.track.getBufferedStart()),t.currentTimeNTP=(0,C.A)(),e>BigInt(0)?(t.currentTime=P.yw(e),t.controlIPCPort.notify("seek",{time:t.currentTime})):(S.A.safari||A.A.ios||b>.1||t.playRate>BigInt(100))&&(t.currentTime=b,t.currentTimeNTP=(0,C.A)(),t.controlIPCPort.notify("seek",{time:b}));t.eioSourceOpenStage="ready"}catch(err){t.eioSourceOpenError={planDetail:err&&void 0!==err.planDetail?err.planDetail:null,planReason:err&&err.planReason||null,code:err&&err.code||null,message:String(err&&err.message||err).replace(/https?:\/\/\S+/g,"[redacted-url]").slice(0,240)};throw err}}}getMimeType(t){let e="";return 1===s.f[15](t)?e=(0,w.A)(t):0===s.f[15](t)&&(e=(0,b.A)(t)),e||T.h2("invalid stream",L,515),e}createSourceBuffer(t,e){return t.addSourceBuffer(this.getMimeType(e))}mixExtradata(t,e,i){const s=t.oformatContext.streams[0].codecpar;s.extradata&&(0,l.Eb)(s.extradata),s.extradata=(0,l.sY)(i),(0,a.Mr)(s.extradata,e,i),s.extradataSize=i,27===s.codecId?U.XC(t.oformatContext.streams[0],(0,a.s3)(e,i)):173===s.codecId&&I.XC(t.oformatContext.streams[0],(0,a.s3)(e,i)),t.track.changeMimeType(this.getMimeType(s[r.o9]),t.enableRawMpeg?"sequence":"segments");this.eioReleaseWriters(t.eioOwned);const n=new R.A({fragmentMode:1,fragment:!0,fastOpen:!0,mp4Mode:0,defaultBaseIsMoof:!0,reverseSpsInAvcc:!0,hasTfra:!1});n.eioOwned=t.eioOwned,t.oformatContext.oformat=n,t.timestampOffsetUpdated=!1,t.enableRawMpeg||(Q.ho(t.oformatContext),Q.bx(t.oformatContext))}checkEnableEncryption(t,e){if(t.oformatContext.streams[0].metadata.encryption){const i=(0,f.fQ)(e,25);if(!i&&!t.ignoreEncryption||i&&t.ignoreEncryption){t.track.changeMimeType(this.getMimeType(t.oformatContext.streams[0].codecpar[r.o9]),t.enableRawMpeg?"sequence":"segments");this.eioReleaseWriters(t.eioOwned);const e=new R.A({fragmentMode:1,fragment:!0,fastOpen:!0,mp4Mode:0,defaultBaseIsMoof:!0,reverseSpsInAvcc:!0,ignoreEncryption:!i,hasTfra:!1});e.eioOwned=t.eioOwned,t.ignoreEncryption=!i,t.oformatContext.oformat=e,t.timestampOffsetUpdated=!1,t.enableRawMpeg||(Q.ho(t.oformatContext),Q.bx(t.oformatContext))}}}async pullAVPacketInternal(t,e){const i=await e.request("pull");if(B.ai(i)||(0,y.A)(i))return i;{const e=t.avpacketPool.alloc();return(0,p.r0)(i,e),e}}async pullAVPacket(t,e,eioTicket){if(t.eioOwned)return this.eioPullLifetime(e).pullAVPacket(t,e,this,eioTicket);const i=t.pullQueue;if(i.ended&&!i.queue.length)return-1048576;const n=i.queue.length?i.queue.shift():await this.pullAVPacketInternal(e,t.pullIPC);if(n<0)return i.ended=!0,-1048576;if(s.f[17](n+16)<BigInt(0))return n;if(s.f[17](n+16)<i.lastDTS&&T.R8(`got packet with dts ${s.f[17](n+16)}, which is earlier then the last packet(${i.lastDTS})`,L,627),c.M[17](n+48,m.Dh),"audio"===t.type&&s.f[17](n+16)>0)c.M[17](n+8,s.f[17](n+16));else if("video"===t.type&&!e.isLive)if(1&s.f[15](n+36)&&(27===s.f[15](e.video.codecpar+4)&&U.mW(n,s.f[20](e.video.codecpar+12)?1+(3&k.r8(s.f[20](e.video.codecpar+12)+4)):4)||173===s.f[15](e.video.codecpar+4)&&I.mW(n,s.f[20](e.video.codecpar+12)?1+(3&k.r8(s.f[20](e.video.codecpar+12)+21)):4)))if(s.f[17](n+8)<i.lastPTS){T.R8(`got video packet with pts ${s.f[17](n+8)}, which is earlier then the last packet(${i.lastPTS}), try to fix it!`,L,664),c.M[17](n+8,i.lastPTS+(s.f[17](n+16)-i.lastDTS));const a=await this.pullAVPacketInternal(e,t.pullIPC);if(a<0)i.ended=!0;else{i.queue.push(a);let r=s.f[17](a+8);const o=s.f[17](a+8);for(;;){const a=await this.pullAVPacketInternal(e,t.pullIPC);if(a<0){i.ended=!0;break}if(i.queue.push(a),s.f[17](a+8)>o)break;r=x.jk(r,s.f[17](a+8))}r<s.f[17](n+8)&&(i.diff=s.f[17](n+16)-i.lastDTS)}}else i.diff=BigInt(0);else c.M[17](n+8,s.f[17](n+8)+i.diff);return s.f[17](n+8)>i.lastPTS&&(i.lastPTS=s.f[17](n+8)),i.lastDTS=s.f[17](n+16),"audio"===t.type&&i.useSampleRateTimeBase&&(i.frameCount===m.Dh?(i.frameCount=(0,h.Mr)(s.f[17](n+16),n+72,t.oformatContext.streams[0].timeBase),c.M[17](n+16,i.frameCount),c.M[17](n+8,i.frameCount)):(i.frameCount=i.frameCount+BigInt(0|s.f[15](t.codecpar+144)),c.M[17](n+16,i.frameCount),c.M[17](n+8,i.frameCount)),(0,a.Mr)(n+72,t.oformatContext.streams[0].timeBase[r.o9],8)),n}writeAVPacket(t,e){let i=arguments.length>2&&void 0!==arguments[2]&&arguments[2];if(e.eioOwned&&(!e.eioActiveOperation||e.eioActiveOperation.open!==!0))throw eioMseError("EIO_MSE_PLAN_MISSING");if(e.enableRawMpeg){if(!e.timestampOffsetUpdated){const i=(0,h.Mr)(s.f[17](t+8),t+72,m.i0);e.track.updateTimestampOffset(Number(BigInt.asIntN(32,i))/1e3),e.timestampOffsetUpdated=!0}const eioRaw=(0,a.s3)(s.f[20](t+24),s.f[15](t+28));e.bufferQueue.write(e.eioOwned?this.eioCopyChunk(e.eioOwned,eioRaw,"mse-raw-packet"):eioRaw.slice())}else{if("video"===e.type&&1&s.f[15](t+36)||"audio"===e.type){const i=(0,f.rU)(t,1);i&&function(t,e,i,a){if(e!==a)return!0;let r=!1;for(let a=0;a<e;a++)if(s.f[2](t+a)!==s.f[2](i+a)){r=!0;break}return r}(e.oformatContext.streams[0].codecpar.extradata,e.oformatContext.streams[0].codecpar.extradataSize,s.f[20](i),0|s.f[25](i+4))&&this.mixExtradata(e,s.f[20](i),0|s.f[25](i+4))}this.checkEnableEncryption(e,t),Q.XA(e.oformatContext,t),i&&e.oformatContext.ioWriter.flush()}}swap(t,e){if(t.eioOwned)return this.eioPullLifetime(e).swap(e,t,this);if(t.backPacket&&e.avpacketPool.release(t.backPacket),t.backPacket=0,!t.frontBuffered)return!1;t.backPacket=t.frontPacket,t.frontPacket=0,t.frontBuffered=!1;const i=(0,C.A)();return this.pullAVPacket(t,e).then((a=>{if(a<0)return t.packetEnded=!0,void(t.frontPacket=0);const r=(0,C.A)()-i;r>5&&(t.startTimestamp+=BigInt(Math.floor(r))),t.frontPacket=a,t.frontBuffered=!0,t.seekSync&&(t.seekSync(),t.seekSync=null),t.backPacket||this.swap(t,e)})),!0}createLoop(t,e){t.loop=new z.A((()=>{return this.eioRunLoopStep(t,()=>{const i=t.track.getBufferedDuration(e.currentTime+((0,C.A)()-e.currentTimeNTP)/1e3);if(i>e.maxBuffer*(e.playRate>BigInt(100)?Number(e.playRate)/100:1))return t.loop.emptyTask(),void this.checkWaiting(e);if(!t.backPacket)return void(t.packetEnded&&!t.frontPacket?this.eioFinishLoop(t):t.loop.emptyTask());if(t.eioOwned){t.eioOwnedWait=!0;const eioLoopOp=this.eioPrepareOperation(t,t.enableRawMpeg?"raw":"packet",{packet:t.backPacket,gapFlush:!0,eof:!!(t.packetEnded&&!t.frontPacket),flush:!0});if(!eioLoopOp){this.eioScheduleWake(t);return void t.loop.emptyTask()}}t.eioOwnedWait=!1;let a=t.backPacket;const r=(0,h.Mr)(s.f[17](a+16),a+72,m.i0);if(t.lastDTS!==m.Dh&&r-t.lastDTS>1e3&&(Q.bX(t.oformatContext),t.startTimestamp-=r-t.lastDTS),t.lastDTS=r,e.enableJitterBuffer&&(s.f[15](e.stats+244)?s.f[15](e.stats+32)/s.f[15](e.stats+244)*1e3:s.f[15](e.stats+232)?s.f[15](e.stats+120)/s.f[15](e.stats+232)*1e3:0)<=s.f[15](e.stats+280)&&this.setPlayRate(e.taskId,1),r*BigInt(100)/e.playRate+t.startTimestamp-BigInt(Math.floor((0,C.A)()))<=0||i<e.minBuffer*(e.playRate>BigInt(100)?Number(e.playRate)/100:1)){t.track.isPaused()&&t.track.enqueue(),c.M[15](a+32,t.oformatContext.streams[0].index),this.writeAVPacket(a,t,!0),this.eioAddQueue(t.track,t);const i=t.oformatContext.streams[0].codecpar.codecType;if(0===i?(c.M[17](e.stats+144,s.f[17](e.stats+144)+BigInt(1)),c.M[17](e.stats+160,s.f[17](e.stats+160)+BigInt(1))):1===i&&(c.M[17](e.stats+56,s.f[17](e.stats+56)+BigInt(1)),c.M[17](e.stats+72,s.f[17](e.stats+72)+BigInt(1))),e.playRate!==e.targetRate&&(t.startTimestamp=BigInt(Math.floor((0,C.A)()))-r*BigInt(100)/e.targetRate,e.playRate=e.targetRate),t.packetEnded&&!t.frontPacket)return t.ended=!0,t.loop.stop(),t.enableRawMpeg||(Q.dJ(t.oformatContext),Q.bX(t.oformatContext)),t.bufferQueue.size&&this.eioAddQueue(t.track,t),this.eioOperationSeal(t),void t.track.end();this.eioOperationSeal(t),this.swap(t,e)}else this.eioOperationSeal(t),t.loop.emptyTask()})}),0,0)}async startMux(t,e){let i,a=arguments.length>2&&void 0!==arguments[2]?arguments[2]:BigInt(0),eioTicket=arguments.length>3?arguments[3]:null;if(t.eioOwned)return this.eioPullLifetime(e).startMux(t,e,this,a,eioTicket);let r=m.Dh,n=m.Dh,o=BigInt(0),d=e.cacheDuration;const u=t.oformatContext.streams[0].timeBase;for(t.backPacket>0&&(r=s.f[17](t.backPacket+16),n=s.f[17](t.backPacket+8),a>n&&(d+=a-(0,h.k)(r,u,m.i0)),c.M[15](t.backPacket+32,t.oformatContext.streams[0].index),this.eioProbeWrite(t,t.backPacket),e.avpacketPool.release(t.backPacket),t.backPacket=0);r===m.Dh||(0,h.k)(o-r,u,m.i0)<d;){if(i=await this.pullAVPacket(t,e),i<0){t.packetEnded=!0;break}o=s.f[17](i+16),r===m.Dh&&(r=o,n=s.f[17](i+8)),s.f[17](i+8)<n&&(n=s.f[17](i+8)),c.M[15](i+32,t.oformatContext.streams[0].index),this.eioProbeWrite(t,i),e.avpacketPool.release(i)}n!==m.Dh&&t.startPTS<=BigInt(0)&&(t.startPTS=n),t.pullQueue.diff=BigInt(0),e.currentTimeNTP=(0,C.A)(),e.currentTime=0,t.startTimestamp=BigInt(Math.floor((0,C.A)()))-(0,h.k)(o,u,m.i0)*BigInt(100)/e.playRate}resetResource(t,e){if(t&&t.eioOwned)this.eioPullLifetime(e).resetResource(t,e);this.eioCancelWake(t),this.eioAbortStaleOperation(t),this.eioDiscardQueue(t),t.track&&t.track.eioDropQueue&&t.track.eioDropQueue(),t.oformatContext.oformat.destroy(t.oformatContext);const i=new R.A({fragmentMode:1,fragment:!0,fastOpen:!0,mp4Mode:0,defaultBaseIsMoof:!0,reverseSpsInAvcc:!0,hasTfra:!1});i.eioOwned=t.eioOwned,t.oformatContext.oformat=i;const a=new _.A;a.eioOwned=t.eioOwned,this.eioEnd(t.eioOwned);a.onQuotaExceededError=()=>{t.startTimestamp-=BigInt(100)},a.onEnded=()=>{e.audio&&!e.audio.ended||e.video&&!e.video.ended||(e.mediaSource.endOfStream(),T.pq(`muxer ended, taskId: ${e.taskId}`,L,994))},t.track=a,t.packetEnded=!1,t.ended=!1,t.startTimestamp=BigInt(0),t.frontBuffered=!1,t.seekSync=null,t.loop&&(t.loop.destroy(),t.loop=null),t.frontPacket&&(e.avpacketPool.release(t.frontPacket),t.frontPacket=0),t.backPacket&&(e.avpacketPool.release(t.backPacket),t.backPacket=0),t.pullQueue.ended=!1,t.pullQueue.index=0,t.pullQueue.frameCount=m.Dh,t.pullQueue.lastPTS=BigInt(0),t.pullQueue.lastDTS=BigInt(0),t.pullQueue.diff=BigInt(0),t.lastDTS=m.Dh}async addStream(t,e,i,a,n,o,d){const f=this.tasks.get(t);if(f){const k=this.eioCloneStreamCodecpar(f,i);const eioOwned=this.eioCreateStream(f,"mux");
let eioFixedClaim=null;
if(eioOwned){
  let eioFixed=null;
  try{eioFixed=this.eioAcquire(eioOwned,EIO_MSE_FIXED_WINDOW,"mse-mux-window")}
  catch(err){this.eioDiscardUninstalledClone(k);throw err}
  if(eioFixed.ok!==!0){
    eioOwned.refusals+=1;eioOwned.lastRefusal=eioFixed.code||"EIO_PROBE_BUDGET_EXHAUSTED";
    this.eioDiscardUninstalledClone(k);
    throw eioMseError("EIO_PROBE_BUDGET_EXHAUSTED")
  }
  eioFixedClaim=eioMseNewClaim(eioFixed.handle,null),eioOwned.fixedClaim=eioFixedClaim
}
const w=new D.A(1048576),b=(0,O.fG)(),v=new R.A({fragmentMode:1,fragment:!0,fastOpen:!0,mp4Mode:0,defaultBaseIsMoof:!0,reverseSpsInAvcc:!0,hasTfra:!1}),U=new M.A;
v.eioOwned=eioOwned,
w.onFlush=t=>(U.write(eioOwned?this.eioCopyChunk(eioOwned,t,"mse-mux-output"):t.slice()),0),w.onSeek=t=>U.seek(t)?0:u.lh;const I=b.createStream();(0,g.Yi)(I.codecpar[r.o9],k),I.metadata=n,86017===s.f[15](k+4)&&(I.codecpar.codecTag=(0,W.A)(".mp3"));const P=1===s.f[15](k)&&s.f[15](k+144)&&!f.isLive&&(0,h.lb)(a)>(0,h.lb)({num:1,den:s.f[15](k+136)});P?(I.timeBase.den=s.f[15](k+136),I.timeBase.num=1):(I.timeBase.den=a.den,I.timeBase.num=a.num),b.oformat=v,b.ioWriter=w;const B=new _.A,C={type:"audio",codecpar:k,ioWriter:w,oformatContext:b,oformat:v,track:B,bufferQueue:U,streamIndex:e,pullIPC:new N.Ay(d),loop:null,eioOwned:eioOwned,eioFixedClaim:eioFixedClaim,frontPacket:0,backPacket:0,frontBuffered:!1,startTimestamp:BigInt(0),packetEnded:!1,ended:!1,seekSync:null,startPTS:o,lastDTS:m.Dh,pullQueue:{queue:[],index:0,frameCount:m.Dh,diff:BigInt(0),lastPTS:BigInt(0),lastDTS:BigInt(0),ended:!1,useSampleRateTimeBase:P},enableRawMpeg:86017===s.f[15](k+4)&&!S.A.firefox,timestampOffsetUpdated:!1,ignoreEncryption:!1};B.onQuotaExceededError=()=>{C.startTimestamp-=BigInt(100)},B.onEnded=()=>{f.audio&&!f.audio.ended||f.video&&!f.video.ended||(f.mediaSource.endOfStream(),T.pq(`muxer ended, taskId: ${f.taskId}`,L,1142))},1===s.f[15](k)?(f.audio=C,c.M[15](f.stats+4,s.f[15](k+136)),c.M[15](f.stats,s.f[15](k+116))):0===s.f[15](k)&&(C.type="video",f.video=C,c.M[15](f.stats+84,s.f[15](k+56)),c.M[15](f.stats+88,s.f[15](k+60)),s.f[15](f.stats+84)*s.f[15](f.stats+88)>8294400&&(S.A.safari||A.A.ios)&&(f.cacheDuration=x.T9(BigInt(3e3),f.cacheDuration))),T.Yz(`add stream ${e}, taskId: ${t}`,L,1162)}else T.h2("task not found",L,1165)}async reAddStream(t,e,i,a,n,o){const c=this.tasks.get(t);if(c){const d=this.eioCloneStreamCodecpar(c,i);const u=1===s.f[15](d)?c.audio:c.video;if(u){this.eioDiscardQueue(u),await new Promise((t=>{u.track.removeAllBuffer((()=>{t()}))})),u.track.reset();const t=new R.A({fragmentMode:1,fragment:!0,fastOpen:!0,mp4Mode:0,defaultBaseIsMoof:!0,reverseSpsInAvcc:!0,hasTfra:!1});t.eioOwned=u.eioOwned,u.oformatContext.oformat=t,u.codecpar&&(0,g.dn)(u.codecpar),u.codecpar=d,u.streamIndex=e,u.startPTS=o;const i=u.oformatContext.streams[0];(0,g.Yi)(i.codecpar[r.o9],d),i.metadata=n,86017===s.f[15](d+4)&&(i.codecpar.codecTag=(0,W.A)(".mp3"));const f=1===s.f[15](d)&&s.f[15](d+144)&&!c.isLive&&(0,h.lb)(a)>(0,h.lb)({num:1,den:s.f[15](d+136)});f?(i.timeBase.den=s.f[15](d+136),i.timeBase.num=1):(i.timeBase.den=a.den,i.timeBase.num=a.num),u.enableRawMpeg=86017===s.f[15](d+4)&&!S.A.firefox,u.timestampOffsetUpdated=!1,u.pullQueue.useSampleRateTimeBase=f;try{u.track.changeMimeType(this.getMimeType(i.codecpar[r.o9]),u.enableRawMpeg?"sequence":"segments")}catch(t){if(!S.A.firefox||t.message.indexOf("An attempt was made to use an object that is not")<0)throw t}u.enableRawMpeg||(Q.ho(u.oformatContext),Q.bx(u.oformatContext))}else(0,g.dn)(d);T.Yz(`readd stream ${e}, taskId: ${t}`,L,1268)}else T.h2("task not found",L,1271)}async pause(t){const e=this.tasks.get(t);var i,a,r,s;e?(null!==(i=e.audio)&&void 0!==i&&i.loop||null!==(a=e.video)&&void 0!==a&&a.loop||T.h2("task has not played",L,1279),e.pausing=!0,e.pauseTimestamp=(0,C.A)(),this.eioCancelWake(e.audio),this.eioCancelWake(e.video),null===(r=e.audio)||void 0===r||r.loop.stop(),null===(s=e.video)||void 0===s||s.loop.stop(),e.fakePlayTimer&&e.fakePlayTimer.stop(),T.Yz(`pause taskId: ${t}`,L,1289)):T.h2("task not found",L,1292)}async unpause(t){const e=this.tasks.get(t);var i,a;e?(null!==(i=e.audio)&&void 0!==i&&i.loop||null!==(a=e.video)&&void 0!==a&&a.loop||T.h2("task has not played",L,1300),e.pausing=!1,e.seeking||(e.audio&&(e.audio.startTimestamp+=BigInt(Math.floor((0,C.A)()-e.pauseTimestamp)),e.audio.loop.start()),e.video&&(e.video.startTimestamp+=BigInt(Math.floor((0,C.A)()-e.pauseTimestamp)),e.video.loop.start())),e.fakePlayTimer&&e.fakePlayTimer.start(),T.Yz(`unpause taskId: ${t}`,L,1318)):T.h2("task not found",L,1321)}async beforeSeek(t){const e=this.tasks.get(t);if(!e)return;if(this.eioPullHasStrict(e)){try{await this.eioPullLifetime(e).beforeSeek(e,this)}catch(error){this.eioRecordPullError(e,"beforeSeek",error);throw error}return{eioAck:1,pullSettled:!0}}{var i,a;const r=[];this.eioCancelWake(e.audio),this.eioCancelWake(e.video);e.audio&&(e.audio.ended||e.audio.frontBuffered||r.push(new Promise((t=>{e.audio.seekSync=t})))),e.video&&(e.video.ended||e.video.frontBuffered||r.push(new Promise((t=>{e.video.seekSync=t})))),await Promise.all(r),e.seeking=!0,null===(i=e.audio)||void 0===i||i.loop.stop(),null===(a=e.video)||void 0===a||a.loop.stop(),e.audio&&(e.audio.enableRawMpeg||await this.eioAfterSeekFlush(e.audio),this.eioDiscardQueue(e.audio),(S.A.safari||A.A.ios)&&await new Promise((t=>{e.audio.track.removeAllBuffer((()=>{t()}))})),e.audio.track.reset(),e.audio.packetEnded=!1,e.audio.timestampOffsetUpdated=!1,e.audio.backPacket&&(e.avpacketPool.release(e.audio.backPacket),e.audio.backPacket=0),e.audio.frontPacket&&(e.avpacketPool.release(e.audio.frontPacket),e.audio.frontPacket=0),e.audio.pullQueue.queue.length&&e.audio.pullQueue.queue.forEach((t=>{e.avpacketPool.release(t)})),e.audio.pullQueue.queue.length=0,e.audio.pullQueue.ended=!1,e.audio.pullQueue.index=0,e.audio.pullQueue.lastPTS=BigInt(0),e.audio.pullQueue.lastDTS=BigInt(0),e.audio.pullQueue.diff=BigInt(0),e.audio.pullQueue.frameCount=m.Dh,e.audio.lastDTS=m.Dh),e.video&&(e.video.enableRawMpeg||await this.eioAfterSeekFlush(e.video),this.eioDiscardQueue(e.video),(S.A.safari||A.A.ios)&&await new Promise((t=>{e.video.track.removeAllBuffer((()=>{t()}))})),e.video.track.reset(),e.video.packetEnded=!1,e.video.timestampOffsetUpdated=!1,e.video.backPacket&&(e.avpacketPool.release(e.video.backPacket),e.video.backPacket=0),e.video.frontPacket&&(e.avpacketPool.release(e.video.frontPacket),e.video.frontPacket=0),e.video.pullQueue.queue.length&&e.video.pullQueue.queue.forEach((t=>{e.avpacketPool.release(t)})),e.video.pullQueue.queue.length=0,e.video.pullQueue.ended=!1,e.video.pullQueue.index=0,e.video.pullQueue.diff=BigInt(0),e.video.pullQueue.lastPTS=BigInt(0),e.video.pullQueue.lastDTS=BigInt(0),e.video.lastDTS=m.Dh),T.Yz(`before seek end taskId: ${t}`,L,1432)}}async afterSeek(t,e){const i=this.tasks.get(t);if(!i)return 0;if(this.eioPullHasStrict(i)){const l=await this.eioPullLifetime(i).afterSeek(i,this,e);if("number"!=typeof l||!Number.isFinite(l)||l<0)throw eioMseError("EIO_MUX_SEEK_RESULT_INVALID");return{eioAck:1,seekTime:l}}{let a=e,r=e,n=await this.syncToKeyframe(i);i.audio&&i.audio.backPacket>0&&((!n||e<BigInt(0))&&(a=(0,h.Mr)(s.f[17](i.audio.backPacket+8),i.audio.backPacket+72,m.i0)),c.M[15](i.audio.backPacket+32,i.audio.oformatContext.streams[0].index),await this.eioAfterSeekWrite(i.audio,i.audio.backPacket),i.avpacketPool.release(i.audio.backPacket),i.audio.backPacket=0),i.video&&i.video.backPacket>0&&((!n||e<BigInt(0))&&(r=(0,h.Mr)(s.f[17](i.video.backPacket+8),i.video.backPacket+72,m.i0)),c.M[15](i.video.backPacket+32,i.video.oformatContext.streams[0].index),await this.eioAfterSeekWrite(i.video,i.video.backPacket),i.avpacketPool.release(i.video.backPacket),i.video.backPacket=0);const o=x.T9(a,r);let audioFilled=!i.audio,videoFilled=!i.video;for(;!audioFilled||!videoFilled;){if(!audioFilled){if(i.audio.packetEnded){audioFilled=!0}else{i.audio.backPacket=await this.pullAVPacket(i.audio,i);if(i.audio.backPacket<0){i.audio.packetEnded=!0;i.audio.backPacket=0;audioFilled=!0}else if((0,h.Mr)(s.f[17](i.audio.backPacket+8),i.audio.backPacket+72,m.i0)>=o+i.cacheDuration){audioFilled=!0}else{c.M[15](i.audio.backPacket+32,i.audio.oformatContext.streams[0].index);await this.eioAfterSeekWrite(i.audio,i.audio.backPacket);i.audio.enableRawMpeg||await this.eioAfterSeekFlush(i.audio);this.eioAddQueue(i.audio.track,i.audio);i.avpacketPool.release(i.audio.backPacket);i.audio.backPacket=0}}}if(!videoFilled){if(i.video.packetEnded){videoFilled=!0}else{i.video.backPacket=await this.pullAVPacket(i.video,i);if(i.video.backPacket<0){i.video.packetEnded=!0;i.video.backPacket=0;videoFilled=!0}else if((0,h.Mr)(s.f[17](i.video.backPacket+8),i.video.backPacket+72,m.i0)>=o+i.cacheDuration){videoFilled=!0}else{c.M[15](i.video.backPacket+32,i.video.oformatContext.streams[0].index);await this.eioAfterSeekWrite(i.video,i.video.backPacket);i.video.enableRawMpeg||await this.eioAfterSeekFlush(i.video);this.eioAddQueue(i.video.track,i.video);i.avpacketPool.release(i.video.backPacket);i.video.backPacket=0}}}}const d=[];i.audio&&(i.audio.enableRawMpeg||await this.eioAfterSeekFlush(i.audio),d.push(new Promise((t=>{this.eioAddQueue(i.audio.track,i.audio,(()=>{t()}))})))),i.video&&(i.video.enableRawMpeg||await this.eioAfterSeekFlush(i.video),d.push(new Promise((t=>{this.eioAddQueue(i.video.track,i.video,(()=>{t()}))})))),await Promise.all(d),i.audio&&!i.audio.packetEnded&&(i.audio.backPacket<=0&&(i.audio.backPacket=await this.pullAVPacket(i.audio,i),i.audio.backPacket<0&&(i.audio.packetEnded=!0,i.audio.backPacket=0)),i.audio.packetEnded||(i.audio.frontPacket=await this.pullAVPacket(i.audio,i),i.audio.frontPacket<0?(i.audio.frontPacket=0,i.audio.packetEnded=!0,i.audio.frontBuffered=!1):(i.audio.packetEnded=!1,i.audio.frontBuffered=!0),i.audio.startTimestamp=BigInt(Math.floor((0,C.A)()))-(a+i.cacheDuration+(0,h.k)(i.audio.startPTS,i.audio.oformatContext.streams[0].timeBase,m.i0))*BigInt(100)/i.playRate,i.pausing?i.pauseTimestamp=(0,C.A)():i.audio.loop.start())),i.video&&!i.video.packetEnded&&(i.video.backPacket<=0&&(i.video.backPacket=await this.pullAVPacket(i.video,i),i.video.backPacket<0&&(i.video.packetEnded=!0,i.video.backPacket=0)),i.video.packetEnded||(i.video.frontPacket=await this.pullAVPacket(i.video,i),i.video.frontPacket<0?(i.video.packetEnded=!0,i.video.frontBuffered=!1,i.video.frontPacket=0):(i.video.packetEnded=!1,i.video.frontBuffered=!0),i.video.startTimestamp=BigInt(Math.floor((0,C.A)()))-(r+i.cacheDuration+(0,h.k)(i.video.startPTS,i.video.oformatContext.streams[0].timeBase,m.i0))*BigInt(100)/i.playRate,i.pausing?i.pauseTimestamp=(0,C.A)():i.video.loop.start())),await Promise.all([new Promise((t=>{i.audio?i.audio.track.insertEnqueueCallback(t):t()})),new Promise((t=>{i.video?i.video.track.insertEnqueueCallback(t):t()}))]);let u=0,f=0;i.audio?i.video?(u=Math.max(i.audio.track.getBufferedStart(),i.video.track.getBufferedStart()),f=Math.min(i.audio.track.getBufferedEnd(),i.video.track.getBufferedEnd())):(u=i.audio.track.getBufferedStart(),f=i.audio.track.getBufferedEnd()):i.video&&(u=i.video.track.getBufferedStart(),f=i.video.track.getBufferedEnd());let l=P.yw(o);return l>=u&&l<=f||(l=Math.abs(l-u)>Math.abs(l-f)?f:u),i.currentTimeNTP=(0,C.A)(),i.currentTime=l,i.seeking=!1,T.Yz(`after seek end, seekTime: ${l} taskId: ${t}`,L,1670),l}return 0}async setPlayRate(t,e){const i=this.tasks.get(t);if(i){if(i.enableJitterBuffer){let t=s.f[15](i.stats+244)?s.f[15](i.stats+32)/s.f[15](i.stats+244)*1e3:s.f[15](i.stats+232)?s.f[15](i.stats+120)/s.f[15](i.stats+232)*1e3:0;t&&t<=s.f[15](i.stats+280)&&(e=1)}i.targetRate=BigInt(Math.floor(Math.floor(100*e))),i.enableJitterBuffer||(i.audio&&i.audio.loop&&i.audio.loop.resetInterval(),i.video&&i.video.loop&&i.video.loop.resetInterval())}}async restart(t){const e=this.tasks.get(t);if(e){e.audio&&this.resetResource(e.audio,e),e.video&&this.resetResource(e.video,e);const t=new((0,q.A)());t.onsourceopen=this.getSourceOpenHandler(e),e.mediaSource=t}}async setCurrentTime(t,e){const i=this.tasks.get(t);i?(i.audio&&i.audio.track.removeBuffer(e),i.video&&s.f[15](i.stats+92)>0&&(i.video.track.setMediaBufferMax(Math.max(i.video.track.getMediaBufferMax(),Math.ceil(s.f[15](i.stats+92)/1e3*1.5),10)),i.video.track.removeBuffer(e)),i.currentTime=e,i.currentTimeNTP=(0,C.A)()):T.h2("task not found",L,1743)}async checkWaiting(t){let e=-1,i=-1;const a=t.currentTime+((0,C.A)()-t.currentTimeNTP)/1e3;if(t.audio){const i=t.audio.track.getSourceBuffer();i&&2===i.buffered.length&&i.buffered.end(1)-i.buffered.start(1)>=t.maxBuffer&&i.buffered.end(0)<a&&t.currentTime<i.buffered.start(1)&&(e=i.buffered.start(1))}if(t.video){const e=t.video.track.getSourceBuffer();e&&2===e.buffered.length&&e.buffered.end(1)-e.buffered.start(1)>=t.maxBuffer&&e.buffered.end(0)<a&&t.currentTime<e.buffered.start(1)&&(i=e.buffered.start(1))}if(t.audio&&t.video){if(e>0&&i>0){const a=Math.min(e,i);t.currentTime=a,t.controlIPCPort.notify("seek",{time:a})}}else e>0?(t.currentTime=e,t.controlIPCPort.notify("seek",{time:e})):i>0&&(t.currentTime=i,t.controlIPCPort.notify("seek",{time:i}))}async getMediaSource(t){const e=this.tasks.get(t);if(e)return e.mediaSource.handle?(this.getMediaSource.transfer.push(e.mediaSource.handle),e.mediaSource.handle):e.mediaSource;T.h2("task not found",L,1808)}async isManagedMediaSource(t){const e=this.tasks.get(t);if(e)return"function"==typeof ManagedMediaSource&&e.mediaSource instanceof ManagedMediaSource;T.h2("task not found",L,1819)}createTask(t,e){const i=new N.Ay(t.controlPort),a=new((0,q.A)()),r={...t,mediaSource:a,audio:null,video:null,playRate:BigInt(100),targetRate:BigInt(100),pauseTimestamp:0,seeking:!1,pausing:!1,controlIPCPort:i,currentTime:0,currentTimeNTP:0,cacheDuration:BigInt(Math.floor(500)),avpacketPool:new v.A((0,n.A)(t.avpacketList,d.A),t.avpacketListMutex),maxBuffer:t.isLive?1:4,minBuffer:t.isLive?.5:2,visibilityHidden:!1,fakePlayTimer:null};return this.tasks.set(t.taskId,r),a.onsourceopen=this.getSourceOpenHandler(r,e),i.on(N.Wo,(e=>{"visibilitychange"===e.method&&this.setVisibilityHidden(t.taskId,e.params.visibilityHidden)})),0}async setVisibilityHidden(t,e){const i=this.tasks.get(t);i&&(i.visibilityHidden=e,e?i.isLive&&i.video&&!i.audio&&(i.fakePlayTimer&&i.fakePlayTimer.destroy(),i.fakePlayTimer=new F.A((()=>{this.setCurrentTime(i.taskId,i.currentTime+((0,C.A)()-i.currentTimeNTP)/1e3)}),0,1e3),i.fakePlayTimer.start()):i.fakePlayTimer&&(i.fakePlayTimer.destroy(),i.fakePlayTimer=null,i.video.track.getBufferedTime()>(i.isLive?2:4)&&(this.setCurrentTime(i.taskId,Math.max(i.video.track.getBufferedEnd()-(i.isLive?1:4),i.video.track.getBufferedStart())),i.controlIPCPort.notify("seek",{time:i.currentTime}))))}async registerTask(t){let e=arguments.length>1&&void 0!==arguments[1]?arguments[1]:BigInt(0);return this.tasks.has(t.taskId)?u.lh:this.createTask(t,e)}async registerEioSharedMediaDomain(t,e){const n=this.tasks.get(t);if(!n)return 0;if(!e||typeof e!=="object"||typeof e.domainId!=="string"||!e.domainId){n.eioSharedDomainError="EIO_SHARED_DOMAIN_ATTACH_FAILED";return 0}if(n.eioSharedMediaDomain&&n.eioSharedMediaDomain.domainId!==e.domainId){n.eioSharedDomainError="EIO_SHARED_DOMAIN_CONFIG_CHANGED";return 0}if(n.eioSharedMediaDomain&&n.eioSharedMediaDomain.domainId===e.domainId&&n.eioOwnedResources)return 1;try{const t=__eioSharedMediaDomainApi.registerDomain(e,2);n.eioSharedController=t,n.eioSharedMediaDomain=e,n.eioSharedDomainError=null,n.eioOwnedResources=this.eioCreateOwnedResources(t);if(!n.eioOwnedResources){n.eioSharedDomainError="EIO_OWNED_RESOURCES_UNAVAILABLE";return 0}return 1}catch(t){n.eioSharedDomainError="EIO_SHARED_DOMAIN_ATTACH_FAILED";return 0}}getEioSharedMediaSnapshot(t){const e=this.tasks.get(t);if(!e||!e.eioSharedController||!e.eioSharedMediaDomain)return null;let i=null;try{const t=__eioSharedMediaDomainApi.getReleaseRegistrySnapshot();i=t&&t.domains?t.domains.find((t=>t.domainId===e.eioSharedMediaDomain.domainId))||null:null}catch(t){}
let eioCodecparLeases=null;
try{eioCodecparLeases=e.eioCodecparLeases&&"function"==typeof e.eioCodecparLeases.snapshot?e.eioCodecparLeases.snapshot():null}catch(t){}
return{domainId:e.eioSharedMediaDomain.domainId,ownership:e.eioSharedController.snapshot(),release:i,owned:this.eioOwnedSnapshot(e),streams:this.eioStreamSnapshot(e),codecparLeases:eioCodecparLeases,nativePlaybackClock:eioReadNativePlaybackClock(e.eioSharedMediaDomain),incomplete:EIO_MSE_INCOMPLETE.slice(),incompleteReasons:EIO_MSE_INCOMPLETE.map((t=>EIO_MSE_INCOMPLETE_REASONS[t]||t)),conservativeWorkset:{label:EIO_MSE_PLAN_LABEL,bytes:EIO_MSE_FIXED_WINDOW,measured:!1},workset:this.eioWorksetSnapshot(e),complete:!1}}
/* EOF can arrive with no backPacket (including an empty stream). It must
   acquire the final mux operation before stopping the consumer or writing boxes. */
eioFinishLoop(t){
  if(t.eioOwned&&!t.enableRawMpeg&&!this.eioPrepareOperation(t,"trailer",{})){
    this.eioScheduleWake(t);t.loop.emptyTask();return
  }
  try{
    t.ended=!0;t.loop.stop();
    if(!t.enableRawMpeg){Q.dJ(t.oformatContext);Q.bX(t.oformatContext)}
    if(t.bufferQueue.size)this.eioAddQueue(t.track,t);
    this.eioOperationSeal(t);
    t.track.end()
  }catch(err){this.eioOperationAbort(t,err);throw err}
}
/* EIO-MSE-LOOP-ERROR:BEGIN strict-only steady-loop diagnostic capture. A stream
   without an owned attachment executes the callback directly. A strict throw is
   recorded for observation and rethrown unchanged (same error object): nothing
   is swallowed, retried, dropped, released, reset or retimed, and a later tick
   does not clear the record. */
eioRunLoopStep(t,run){
  if(!t.eioOwned)return run();
  try{return run()}
  catch(err){
    t.eioLoopError=eioProgressLoopFailure(err);
    throw err
  }
}
/* EIO-MSE-LOOP-ERROR:END */
/* EIO-MSE-PROGRESS:RPC read-only steady-loop observation. Only scalars that
   actually exist on the live task/stream/track at the sample instant are
   reported (task.currentTime is explicitly the cached value, not native
   confirmed playback time); absent or unreadable values stay null and no
   pointer, address, codecpar, buffer, plan or extrapolated media clock is
   returned. A single failing getter degrades its own field, never the RPC. */
async getEioMSEProgress(taskId){
  let t=null;
  try{t=this.tasks.get(taskId)||null}catch(err){t=null}
  if(!t)return null;
  return{
    taskId:taskId,
    sampledClockMs:eioProgressClockMs(),
    playRateScaled:eioProgressDecimal(t,"playRate"),
    cachedCurrentTimeSec:eioProgressNumber(t,"currentTime"),
    streams:{
      audio:this.eioMseProgressStream(eioProgressValue(t,"audio")),
      video:this.eioMseProgressStream(eioProgressValue(t,"video"))
    }
  }
}
eioMseProgressStream(t){
  if(!t||"object"!=typeof t)return null;
  const e={
    type:eioProgressText(t,"type"),
    ended:eioProgressBool(t,"ended"),
    packetEnded:eioProgressBool(t,"packetEnded"),
    frontBuffered:eioProgressBool(t,"frontBuffered"),
    backPacketPresent:eioProgressPending(t,"backPacket"),
    frontPacketPresent:eioProgressPending(t,"frontPacket"),
    pullQueueLength:eioProgressQueueLength(eioProgressValue(t,"pullQueue")),
    loopStarted:eioProgressLoopStarted(eioProgressValue(t,"loop")),
    lastDtsMs:eioProgressDecimal(t,"lastDTS"),
    startTimestampMs:eioProgressDecimal(t,"startTimestamp"),
    backPacketDtsMs:eioProgressBackPacketDts(t),
    eioLoopError:eioProgressLoopError(eioProgressValue(t,"eioLoopError")),
    bufferedRanges:null,
    rangesError:null,
    sourceUpdating:null
  };
  this.eioMseProgressBuffered(t,e);
  return e
}
eioMseProgressBuffered(t,e){
  try{
    const i=eioProgressValue(t,"track");
    const a=i&&"function"==typeof i.getSourceBuffer?i.getSourceBuffer():null;
    if(!a)return;
    e.sourceUpdating="boolean"==typeof a.updating?a.updating:null;
    const r=a.buffered;
    if(!r||"number"!=typeof r.length)return;
    const n=[];
    for(let o=0;o<r.length;o++)n.push({start:r.start(o),end:r.end(o)});
    e.bufferedRanges=n
  }catch(err){
    e.bufferedRanges=null;
    e.rangesError=eioProgressMessage(err)
  }
}
eioOwnedManager(){const t=__eioMseOwnedRequire();return t&&"function"==typeof t.createOwnedResources?t:null}
eioCreateOwnedResources(t){const e=this.eioOwnedManager();if(!e||!t)return null;try{return e.createOwnedResources(t)||null}catch(err){return null}}

eioRecordPullError(task,stage,error){
  task.eioPullLastError={stage,code:error&&error.code||null,message:String(error&&error.message||error).replace(/https?:\/\/\S+/g,"[redacted-url]").slice(0,240)};
}
async getEioPullStatus(id){
  const task=this.tasks.get(id);
  if(!task)return null;
  const streamStatus=stream=>stream?{
    tickets:stream.eioPullTickets?Array.from(stream.eioPullTickets,t=>({state:t.state,errorCode:t.errorCode||null})):[],
    packets:stream.eioPullPackets?stream.eioPullPackets.size:0,
    front:stream.frontPacket>0,back:stream.backPacket>0,frontBuffered:stream.frontBuffered,
    fault:stream.eioPullFault||null
  }:null;
  return{error:task.eioPullLastError||null,audio:streamStatus(task.audio),video:streamStatus(task.video)};
}
async getEioSourceOpenStatus(id){const t=this.tasks.get(id);return t?{stage:t.eioSourceOpenStage||null,error:t.eioSourceOpenError||null,mediaState:t.mediaSource&&t.mediaSource.readyState}:null}
eioOwnedSnapshot(t){try{return t.eioOwnedResources&&"function"==typeof t.eioOwnedResources.snapshot?t.eioOwnedResources.snapshot():null}catch(err){return null}}
eioStreamSnapshot(t){const e=[];["audio","video"].forEach((i=>{const n=t[i];if(!n)return;const s=n.eioOwned;e.push({type:n.type,attached:!!s,fixedWindowClaim:!!(s&&s.fixedClaim),trackWindowWriters:s?s.writers.filter((t=>!!t)).length:0,scope:s&&s.scope?{bytes:s.scope.bytes,remaining:s.scope.remaining,refs:s.scope.refs,closed:s.scope.closed,kind:s.scope.kind||null,atomic:!!s.scope.atomic,fault:s.scope.fault||null,underruns:s.scope.underruns||0,freed:!!s.scope.freed,releaseFailed:!!s.scope.releaseFailed,releaseError:s.scope.releaseError||null}:null,reservation:s&&s.lastPlan?s.lastPlan:null,refusals:s?s.refusals:0,lastRefusal:s?s.lastRefusal:null,conservativeWorksetMeasured:!1,incomplete:EIO_MSE_INCOMPLETE.slice()})}));return e}
eioCreateStream(t,e){
  const i=t&&t.eioOwnedResources;
  if(!i)return null;
  const n=this,s={manager:i,generation:Number(t.eioOwnedGeneration)||0,taskId:t.taskId,type:"mse-"+e,writers:[],fixedClaim:null,scope:null,refusals:0,lastRefusal:null};
  s.copy=function(t,e){return n.eioCopyChunk(s,t,e)};
  s.writerFactory=function(t){return n.eioCreateWriter(s,t)};
  s.nativeCodecparLease=function(t){return n.eioNativeCodecparLease(s,t)};
  s.releaseWriters=function(){n.eioReleaseWriters(s)};
  return s
}
eioAcquire(t,e,i){
  if(!t||!t.manager)return{ok:!1,code:"EIO_OWNED_RESOURCES_UNAVAILABLE"};
  try{const n=t.manager.acquire(e,i,t.generation);return n&&"object"==typeof n?n:{ok:!1,code:"EIO_OWNED_RESOURCES_UNAVAILABLE"}}catch(err){return{ok:!1,code:"EIO_OWNED_RESOURCES_UNAVAILABLE"}}
}
/* Conservative workset admission for one mux operation batch. It reserves the
   real writer window (a concrete allocation this code makes), never a guessed
   multiplier; it is reported as conservative/unmeasured, not as actualBytes. */
eioBegin(t){
  if(!t)return!0;
  if(t.scope&&t.scope.closed!==!0)return!0;
  const e=this.eioAcquire(t,EIO_MSE_FIXED_WINDOW,EIO_MSE_CONSERVATIVE_LABEL);
  if(e.ok!==!0){t.refusals+=1;t.lastRefusal=e.code||"EIO_MSE_WORKSET_REFUSED";return!1}
  t.scope={handle:e.handle,bytes:EIO_MSE_FIXED_WINDOW,remaining:EIO_MSE_FIXED_WINDOW,refs:0,closed:!1};
  return!0
}
eioBeginProbe(t){if(!t||!t.eioOwned)return;if(!this.eioBegin(t.eioOwned))throw eioMseError("EIO_PROBE_BUDGET_EXHAUSTED")}
eioEnd(t){
  if(!t||!t.scope||t.scope.closed===!0)return;
  if(t.eioActiveOperation&&t.eioActiveOperation.scope===t.scope&&t.eioActiveOperation.open===!0)return;
  t.scope.closed=!0;
  if(t.scope.refs<=0)eioMseReleaseScope(t.scope)
}
eioCopyChunk(t,e,i){
  if(!t)return e.slice();
  const n=e.byteLength;
  const eioOp=t.eioActiveOperation;
  if(eioOp&&eioOp.open===!0){
    const s=eioOp.scope;
    if(!s||s.atomic!==!0||s.closed===!0||eioOp.owner!==t){
      t.refusals+=1;
      t.lastRefusal="EIO_MSE_PLAN_MISSING";
      throw eioMseError("EIO_MSE_PLAN_MISSING")
    }
    if(!(s.remaining>=n)){
      t.refusals+=1;
      s.underruns=(s.underruns||0)+1;
      s.fault="EIO_MSE_PLAN_UNDERRUN";
      t.lastRefusal="EIO_MSE_PLAN_UNDERRUN";
      throw eioMseError("EIO_MSE_PLAN_UNDERRUN")
    }
    s.remaining-=n;s.refs+=1;
    const c=e.slice();
    c.eioClaim=eioMseNewClaim(null,s);
    return c
  }
  const s=t.scope&&t.scope.closed!==!0&&t.scope.atomic!==!0?t.scope:null;
  let o=null;
  if(s&&s.remaining>=n)s.remaining-=n,s.refs+=1;
  else{
    const h=this.eioAcquire(t,n,i||"mse-output-copy");
    if(h.ok!==!0){t.refusals+=1;t.lastRefusal=h.code||"EIO_MSE_OUTPUT_REFUSED";throw eioMseError(h.code||"EIO_MSE_OUTPUT_REFUSED")}
    o=h.handle
  }
  const h=e.slice();
  h.eioClaim=eioMseNewClaim(o,o?null:s);
  return h
}
eioNativeCodecparLease(t,e){
  const bytes=g.eioPayloadBytes(e);
  if(!eioPlanIsCount(bytes))throw eioMseError("EIO_CODECPAR_LEASE_INVALID");
  const eioOp=t&&t.eioActiveOperation;
  if(!eioOp||eioOp.open!==!0||eioOp.owner!==t||eioOp.scope!==t.scope||!eioOp.scope||eioOp.scope.atomic!==!0||eioOp.scope.closed===!0){
    if(t){t.refusals+=1;t.lastRefusal="EIO_MSE_PLAN_MISSING"}
    throw eioMseError("EIO_MSE_PLAN_MISSING")
  }
  const eioScope=eioOp.scope;
  if(!(eioScope.remaining>=bytes)){
    t.refusals+=1;
    eioScope.underruns=(eioScope.underruns||0)+1;
    eioScope.fault="EIO_MSE_PLAN_UNDERRUN";
    t.lastRefusal="EIO_MSE_PLAN_UNDERRUN";
    throw eioMseError("EIO_MSE_PLAN_UNDERRUN")
  }
  eioScope.remaining-=bytes;
  eioScope.refs+=1;
  const eioClaim=eioMseNewClaim(null,eioScope);
  let eioBound=!1,eioReleased=!1,eioFailed=!1;
  return{
    bytes:bytes,
    bind(eioSummary){
      if(eioBound||eioReleased||eioFailed)return!1;
      if(!eioSummary||eioSummary.copiedBytes!==bytes)return!1;
      eioBound=!0;
      return!0
    },
    release(eioProof){
      if(!eioBound||eioReleased||eioFailed)return!1;
      if(!eioProof||eioProof.referencesDropped!==!0)return!1;
      eioReleased=!0;
      eioClaim.drop();
      return eioScope.releaseFailed!==!0
    },
    retainFailure(eioDetail){
      eioFailed=!0;
      eioScope.retained=!0;
      const eioCode=eioDetail&&"string"==typeof eioDetail.code?eioDetail.code:"EIO_CODECPAR_CLONE_UNPROVEN";
      eioScope.fault=eioScope.fault||eioCode;
      return!1
    }
  }
}
eioCloneStreamCodecpar(task,source){
  const eioOwned=task&&task.eioOwnedResources;
  /* Baseline (no shared domain): byte-identical to the original
     Gy -> pointer discriminator -> 2-arg Yi (native) or 2-arg T8 (serialized).
     No 90191 require, no measurement, no lease, no snapshot. */
  if(!eioOwned){
    const eioDest=(0,l.Gy)(168);
    if((0,y.A)(source))(0,g.Yi)(eioDest,source);
    else(0,p.T8)(source,eioDest);
    return eioDest
  }
  if(!(0,y.A)(source))return this.eioSerializedCodecparClone(task,source);
  /* Native pointer: the real read-only scanner measures inside acquire(), and
     the charge exists before Gy(168)/Yi(dest,source,lease). */
  const eioLease=this.eioCodecparLeasesFor(task).acquire(source,task.eioOwnedGeneration||0,"mse-stream-codecpar");
  let eioDest=0;
  try{
    eioDest=(0,l.Gy)(168);
    if(eioLease&&!eioDest)throw eioMseError("EIO_CODECPAR_ALLOC_FAILED");
    (0,g.Yi)(eioDest,source,eioLease)
  }catch(err){
    try{eioLease.retainFailure({code:"EIO_CODECPAR_CLONE_UNPROVEN"})}catch(ignored){}
    throw err
  }
  return eioDest
}
/* One 90191 manager per task, shared by the native and the serialized branch. */
eioCodecparLeasesFor(task){
  return task.eioCodecparLeases||(task.eioCodecparLeases=i(90191).createCodecparLeases({resources:task.eioOwnedResources,measureNativePayload:g.eioPayloadBytes}))
}
/* Strict serialized input (module 26669 T8): exact read-only measurement of a
   frozen plain snapshot, admission BEFORE Gy(168), T8(snapshot,dest,exact),
   then eioCodecparAdopt() attaches the real dn()-credited lease to dest.
   Any failure keeps the charge (retainFailure) and never frees the destination
   or fabricates a credit: partial native pointer fields are unproved. */
eioSerializedCodecparClone(task,source){
  const eioSnapshot=this.eioSerializedCodecparSnapshot(source);
  const eioBytes=this.eioSerializedCodecparPayloadBytes(eioSnapshot);
  const eioLeases=this.eioCodecparLeasesFor(task);
  if("function"!=typeof eioLeases.acquireMeasured||"function"!=typeof g.eioCodecparAdopt)
    throw eioMseError("EIO_CODECPAR_LEASE_MEASURED_UNSUPPORTED");
  const eioLease=eioLeases.acquireMeasured(eioBytes,task.eioOwnedGeneration||0,"mse-stream-codecpar");
  let eioDest=0;
  try{
    eioDest=(0,l.Gy)(168);
    if(!eioDest)throw eioMseError("EIO_CODECPAR_ALLOC_FAILED");
    (0,p.T8)(eioSnapshot,eioDest,!0);
    g.eioCodecparAdopt(eioDest,eioLease,eioBytes)
  }catch(err){
    try{eioLease.retainFailure({code:"EIO_CODECPAR_CLONE_UNPROVEN"})}catch(ignored){}
    throw err
  }
  return eioDest
}
/* Frozen plain metadata snapshot of exactly the fields module 26669 T8 reads.
   Every live property is read once inside try/catch. Scalar fields must be
   inert primitives: an object valueOf/toPrimitive could run inside a T8 setter
   before the payload malloc and mutate the measured views. The compressed
   payload views are canonical fixed-length Uint8Array ALIASES over the same
   backing bytes - never sliced, copied, preallocated or scanned here. Anything
   T8 could not consume safely is refused before any allocation. */
eioSerializedCodecparSnapshot(source){
  if(!source||"object"!=typeof source)throw eioMseError("EIO_CODECPAR_LEASE_SHAPE_UNSUPPORTED");
  let eioRead=null;
  try{
    eioRead={
      codecType:source.codecType,codecId:source.codecId,codecTag:source.codecTag,
      format:source.format,bitrate:source.bitrate,
      bitsPerCodedSample:source.bitsPerCodedSample,bitsPerRawSample:source.bitsPerRawSample,
      profile:source.profile,level:source.level,width:source.width,height:source.height,
      fieldOrder:source.fieldOrder,colorRange:source.colorRange,
      colorPrimaries:source.colorPrimaries,colorTrc:source.colorTrc,
      colorSpace:source.colorSpace,chromaLocation:source.chromaLocation,
      videoDelay:source.videoDelay,sampleRate:source.sampleRate,blockAlign:source.blockAlign,
      frameSize:source.frameSize,initialPadding:source.initialPadding,
      trailingPadding:source.trailingPadding,seekPreroll:source.seekPreroll,flags:source.flags,
      extradata:source.extradata,codedSideData:source.codedSideData,
      sampleAspectRatio:source.sampleAspectRatio,framerate:source.framerate,
      chLayout:source.chLayout
    }
  }catch(err){
    throw eioMseError("EIO_CODECPAR_LEASE_SHAPE_UNSUPPORTED")
  }
  const eioScalars=[
    "codecType","codecId","codecTag","format","bitrate","bitsPerCodedSample",
    "bitsPerRawSample","profile","level","width","height","fieldOrder",
    "colorRange","colorPrimaries","colorTrc","colorSpace","chromaLocation",
    "videoDelay","sampleRate","blockAlign","frameSize","initialPadding",
    "trailingPadding","seekPreroll","flags"
  ];
  for(let eioIndex=0;eioIndex<eioScalars.length;eioIndex+=1)
    this.eioSerializedScalar(eioRead[eioScalars[eioIndex]]);
  const eioSampleAspectRatio=this.eioSerializedPlainFields(eioRead.sampleAspectRatio,["den","num"]);
  const eioFramerate=this.eioSerializedPlainFields(eioRead.framerate,["den","num"]);
  const eioChLayout=this.eioSerializedPlainFields(eioRead.chLayout,["order","nbChannels","u"]);
  /* CUSTOM (order 2) owns a native map. T8/raw native clones do not deep-copy
     that ownership; refuse before claiming or allocating a strict input. */
  if(eioChLayout.order!==0&&eioChLayout.order!==1&&eioChLayout.order!==3)
    throw eioMseError("EIO_CODECPAR_LAYOUT_UNSUPPORTED");
  const eioSides=eioRead.codedSideData;
  if(!Array.isArray(eioSides))throw eioMseError("EIO_CODECPAR_LEASE_SHAPE_UNSUPPORTED");
  let eioCount=0;
  try{eioCount=eioSides.length}catch(err){throw eioMseError("EIO_CODECPAR_LEASE_SHAPE_UNSUPPORTED")}
  if(!Number.isSafeInteger(eioCount)||eioCount<0||eioCount>65536)
    throw eioMseError("EIO_CODECPAR_LEASE_SHAPE_UNSUPPORTED");
  const eioCodedSideData=[];
  for(let eioIndex=0;eioIndex<eioCount;eioIndex+=1){
    let eioEntry=null;
    try{
      eioEntry=eioSides[eioIndex];
      if(!eioEntry||"object"!=typeof eioEntry)throw eioMseError("EIO_CODECPAR_LEASE_SHAPE_UNSUPPORTED")
    }catch(err){
      throw eioMseError("EIO_CODECPAR_LEASE_SHAPE_UNSUPPORTED")
    }
    let eioType=null,eioData=null;
    try{eioType=eioEntry.type;eioData=eioEntry.data}catch(err){
      throw eioMseError("EIO_CODECPAR_LEASE_SHAPE_UNSUPPORTED")
    }
    eioCodedSideData.push(Object.freeze({
      type:this.eioSerializedScalar(eioType),data:this.eioSerializedPayloadAlias(eioData)
    }))
  }
  const eioExtradata=eioRead.extradata;
  const eioExtradataAlias=null===eioExtradata||void 0===eioExtradata?
    eioExtradata:this.eioSerializedPayloadAlias(eioExtradata);
  return Object.freeze({
    codecType:eioRead.codecType,codecId:eioRead.codecId,codecTag:eioRead.codecTag,
    format:eioRead.format,bitrate:eioRead.bitrate,
    bitsPerCodedSample:eioRead.bitsPerCodedSample,bitsPerRawSample:eioRead.bitsPerRawSample,
    profile:eioRead.profile,level:eioRead.level,width:eioRead.width,height:eioRead.height,
    fieldOrder:eioRead.fieldOrder,colorRange:eioRead.colorRange,
    colorPrimaries:eioRead.colorPrimaries,colorTrc:eioRead.colorTrc,
    colorSpace:eioRead.colorSpace,chromaLocation:eioRead.chromaLocation,
    videoDelay:eioRead.videoDelay,sampleRate:eioRead.sampleRate,blockAlign:eioRead.blockAlign,
    frameSize:eioRead.frameSize,initialPadding:eioRead.initialPadding,
    trailingPadding:eioRead.trailingPadding,seekPreroll:eioRead.seekPreroll,flags:eioRead.flags,
    extradata:eioExtradataAlias,codedSideData:Object.freeze(eioCodedSideData),
    sampleAspectRatio:eioSampleAspectRatio,framerate:eioFramerate,chLayout:eioChLayout
  })
}
/* Inert scalar copy: only primitives T8 can write without invoking user code.
   null/undefined are legal; object, function and symbol values are refused. */
eioSerializedScalar(eioValue){
  const eioType=typeof eioValue;
  if("number"===eioType||"bigint"===eioType||"string"===eioType||
     "boolean"===eioType||"undefined"===eioType||null===eioValue)return eioValue;
  throw eioMseError("EIO_CODECPAR_LEASE_SHAPE_UNSUPPORTED")
}
/* Canonical fixed-length Uint8Array alias over the same backing bytes. Hostile
   own/prototype length, byteLength and BYTES_PER_ELEMENT getters on the source
   view are never read: only the intrinsic %TypedArray% accessors are called,
   and the element count must equal the byte length (one-byte elements only).
   DataView/proxies and detached buffers fail an intrinsic read or the explicit
   constructor and are refused before admission. Zero-length views are valid. */
eioSerializedPayloadAlias(eioView){
  const eioBuffer=this.eioSerializedTypedArrayIntrinsic(eioView,"buffer");
  const eioByteOffset=this.eioSerializedTypedArrayIntrinsic(eioView,"byteOffset");
  const eioByteLength=this.eioSerializedTypedArrayIntrinsic(eioView,"byteLength");
  const eioElements=this.eioSerializedTypedArrayIntrinsic(eioView,"length");
  if(!Number.isSafeInteger(eioByteOffset)||eioByteOffset<0||
     !Number.isSafeInteger(eioByteLength)||eioByteLength<0||eioByteLength>0x7fffffff||
     eioElements!==eioByteLength)
    throw eioMseError("EIO_CODECPAR_LEASE_SIZE_UNSUPPORTED");
  try{
    return new Uint8Array(eioBuffer,eioByteOffset,eioByteLength)
  }catch(err){
    throw eioMseError("EIO_CODECPAR_LEASE_SIZE_UNSUPPORTED")
  }
}
/* Intrinsic accessor call; bypasses subclass prototypes and own properties. */
eioSerializedTypedArrayIntrinsic(eioView,eioName){
  try{
    const eioPrototype=Object.getPrototypeOf(Uint8Array.prototype);
    const eioDescriptor=Object.getOwnPropertyDescriptor(eioPrototype,eioName);
    if(!eioDescriptor||"function"!=typeof eioDescriptor.get)
      throw eioMseError("EIO_CODECPAR_LEASE_SIZE_UNSUPPORTED");
    return eioDescriptor.get.call(eioView)
  }catch(err){
    throw eioMseError("EIO_CODECPAR_LEASE_SIZE_UNSUPPORTED")
  }
}
/* Plain frozen copy of the nested codecpar sub-objects T8 dereferences. */
eioSerializedPlainFields(value,names){
  if(!value||"object"!=typeof value)throw eioMseError("EIO_CODECPAR_LEASE_SHAPE_UNSUPPORTED");
  let eioFields=null;
  try{
    eioFields={};
    for(let eioIndex=0;eioIndex<names.length;eioIndex+=1)
      eioFields[names[eioIndex]]=this.eioSerializedScalar(value[names[eioIndex]])
  }catch(err){
    throw eioMseError("EIO_CODECPAR_LEASE_SHAPE_UNSUPPORTED")
  }
  return Object.freeze(eioFields)
}
/* Exact compressed-payload bytes T8 will copy: extradata + every side payload.
   Only view lengths are read; no payload byte and no payload copy is touched. */
eioSerializedCodecparPayloadBytes(eioSnapshot){
  const eioExtradata=eioSnapshot.extradata;
  let eioBytes=0;
  if(null!==eioExtradata&&void 0!==eioExtradata)eioBytes=this.eioSerializedPayloadLength(eioExtradata);
  const eioSides=eioSnapshot.codedSideData;
  for(let eioIndex=0;eioIndex<eioSides.length;eioIndex+=1){
    const eioLength=this.eioSerializedPayloadLength(eioSides[eioIndex].data);
    if(eioBytes>Number.MAX_SAFE_INTEGER-eioLength)
      throw eioMseError("EIO_CODECPAR_LEASE_SIZE_UNSUPPORTED");
    eioBytes+=eioLength
  }
  return eioBytes
}
eioSerializedPayloadLength(eioView){
  const eioLength=this.eioSerializedTypedArrayIntrinsic(eioView,"byteLength");
  if(!Number.isSafeInteger(eioLength)||eioLength<0||eioLength>0x7fffffff)
    throw eioMseError("EIO_CODECPAR_LEASE_SIZE_UNSUPPORTED");
  return eioLength
}
/* Best-effort cleanup of a clone that was never installed in a stream record.
   A registered-but-retained destination refuses dn(); that throw is swallowed
   and the charge stays retained, so this can never fabricate a credit. */
eioDiscardUninstalledClone(eioClone){
  if(!eioClone)return;
  try{(0,g.dn)(eioClone)}catch(ignored){}
}
eioCreateWriter(t,e){
  const i=e||EIO_MSE_FIXED_WINDOW;
  if(!t)return new D.A(i);
  const eioOp=t.eioActiveOperation;
  if(eioOp&&eioOp.open===!0){
    const a=eioOp.scope;
    if(!a||a.atomic!==!0||a.closed===!0||eioOp.owner!==t){
      t.refusals+=1;
      t.lastRefusal="EIO_MSE_PLAN_MISSING";
      throw eioMseError("EIO_MSE_PLAN_MISSING")
    }
    if(!(a.remaining>=i)){
      t.refusals+=1;
      a.underruns=(a.underruns||0)+1;
      a.fault="EIO_MSE_PLAN_UNDERRUN";
      t.lastRefusal="EIO_MSE_PLAN_UNDERRUN";
      throw eioMseError("EIO_MSE_PLAN_UNDERRUN")
    }
    a.remaining-=i;a.refs+=1;
    const c=new D.A(i);
    c.eioWindowClaim=eioMseNewClaim(null,a);
    t.writers.push(c);
    return c
  }
  const n=this.eioAcquire(t,i,t.type+"-window");
  if(n.ok!==!0){t.refusals+=1;t.lastRefusal=n.code||"EIO_PROBE_BUDGET_EXHAUSTED";throw eioMseError(n.code||"EIO_PROBE_BUDGET_EXHAUSTED")}
  const s=new D.A(i);
  s.eioWindowClaim=eioMseNewClaim(n.handle,null);
  t.writers.push(s);
  return s
}
eioReleaseWriters(t){
  if(!t)return;
  for(let e=0;e<t.writers.length;e++){
    const i=t.writers[e];
    if(!i)continue;
    t.writers[e]=null;
    try{i.buffer=null;i.data=null;i.onFlush=null;i.onSeek=null}catch(err){}
    const n=i.eioWindowClaim;
    i.eioWindowClaim=null;
    n&&n.drop()
  }
  t.writers.length=0
}
eioAddQueue(t,e,i){
  const n=e.bufferQueue;
  if(!e.eioOwned){t.addBuffer(n.flush(),i);return}
  const s=n.flushChunks();
  this.eioEnd(e.eioOwned);
  if(!s.length){i&&i();return}
  for(let o=0;o<s.length;o++)t.addBuffer(s[o],o===s.length-1?i:null)
}
eioDiscardQueue(t){
  const e=t&&t.bufferQueue;
  if(!e)return;
  if(!t.eioOwned){e.flush();return}
  const i=e.discardChunks();
  for(let n=0;n<i.length;n++){const s=i[n];s&&s.eioClaim&&s.eioClaim.drop()}
  this.eioEnd(t.eioOwned)
}
eioReleaseStream(t){
  if(!t||!t.eioOwned)return;
  t.eioReleased=!0;
  this.eioCancelWake(t);
  this.eioAbortStaleOperation(t);
  const e=t.eioOwned;
  this.eioDiscardQueue(t);
  this.eioReleaseWriters(e);
  const i=t.ioWriter;
  if(i){try{i.buffer=null;i.data=null;i.onFlush=null;i.onSeek=null}catch(err){}}
  const n=e.fixedClaim;
  e.fixedClaim=null;
  n&&n.drop();
  t.track&&t.track.eioDropQueue&&t.track.eioDropQueue();
  this.eioEnd(e)
}
/* --- planner-backed atomic mux operations --------------------------------
   The plan is always built (and the single owned scope acquired) BEFORE the
   first real mutation of the operation. A pressure refusal leaves the packet
   and every stat/DTS/timestamp untouched. */
eioPlanBaseRequest(t){
  const eioCtx=t&&t.oformatContext;
  const eioFmt=eioCtx&&eioCtx.oformat;
  if(!eioCtx||!eioFmt||!eioFmt.context)return null;
  const eioQueued=eioMseQueueChunkBytes(t);
  if(!eioQueued)return null;
  return{host:eioCtx,context:eioFmt.context,queuedChunkBytes:eioQueued,writerFactory:eioMseCountOnlyFactory}
}
/* Conversion / extradata / encryption changes rebuild the format or the
   extradata outside the copy plan, so they are capability errors raised before
   any mutation instead of capacity-pressure refusals. */
eioPacketCapability(t,p){
  if(!t||!p||t.enableRawMpeg)return null;
  const eioCtx=t.oformatContext;
  const eioStream=eioCtx&&eioCtx.streams&&eioCtx.streams[0];
  if(!eioStream)return "stream-missing";
  const eioCodecpar=eioStream.codecpar||{};
  if([27,173,196].includes(eioCodecpar.codecId)&&(64&s.f[15](p+36)))return "packet-annexb-conversion-unmeasured";
  if(eioCodecpar.codecId===94226)return "packet-webvtt-boxing-unmeasured";
  if(("video"===t.type&&1&s.f[15](p+36))||"audio"===t.type){
    const eioSide=(0,f.rU)(p,1);
    if(eioSide){
      const eioNewPtr=s.f[20](eioSide),eioNewSize=0|s.f[25](eioSide+4);
      const eioOld=eioCodecpar.extradata,eioOldSize=0|eioCodecpar.extradataSize;
      let eioDifferent=eioOldSize!==eioNewSize;
      if(!eioDifferent&&eioOldSize>0&&eioOld){
        for(let i=0;i<eioOldSize;i++)if(s.f[2](eioOld+i)!==s.f[2](eioNewPtr+i)){eioDifferent=!0;break}
      }
      if(eioDifferent)return "extradata-change-unmeasured"
    }
  }
  if(eioStream.metadata&&eioStream.metadata.encryption&&(0,f.fQ)(p,25)){
    const eioEnc=(0,f.fQ)(p,25);
    if((!eioEnc&&!t.ignoreEncryption)||(eioEnc&&t.ignoreEncryption))return "encryption-change-unmeasured"
  }
  return null
}
/* The packet steps mirror the real order inside one synchronous loop tick:
   optional gap flush, optional fragment close, the payload write, then (at EOF)
   the trailer and its trailing flush. */
eioPacketSteps(t,p,opts){
  const eioCtx=t.oformatContext,eioFmt=eioCtx.oformat,eioCf=eioFmt.context&&eioFmt.context.currentFragment;
  if(!eioCf||!Array.isArray(eioCf.tracks))return{ok:!1,capability:!0,reason:"fragment-state-invalid"};
  const eioSteps=[];
  const eioMs=(0,h.Mr)(s.f[17](p+16),p+72,m.i0);
  if(opts&&opts.gapFlush===!0&&t.lastDTS!==m.Dh&&eioMs-t.lastDTS>1000)eioSteps.push({kind:"flush"});
  /* addStream builds an independent one-stream output format per loop stream,
     so the planned write index is the selected OUTPUT stream index and never
     the raw Demux packet index. The real loop still normalizes the packet index
     before its actual write; this preflight reads no packet index and writes
     nothing. */
  const eioStreams=eioCtx&&eioCtx.streams;
  if(!Array.isArray(eioStreams)||1!==eioStreams.length)return{ok:!1,capability:!0,reason:"output-stream-state-invalid"};
  const eioStream=eioStreams[0];
  const eioIndex=eioStream&&eioStream.index;
  if(!Number.isSafeInteger(eioIndex)||eioIndex<0)return{ok:!1,capability:!0,reason:"output-stream-state-invalid"};
  const eioTrack=eioCf.tracks.find((eioT=>eioT.streamIndex===eioIndex));
  if(eioTrack){
    let eioDts=(0,h.Mr)(s.f[17](p+16),p+72,eioStream.timeBase);
    const eioTfdt=eioTrack.tfdtDelay===m.Dh?(eioDts<BigInt(0)?-eioDts:BigInt(0)):eioTrack.tfdtDelay;
    eioDts=eioDts+eioTfdt;
    const eioType=eioStream.codecpar.codecType;
    const eioClose=(0===eioFmt.options.fragmentMode&&(0===eioType&&1&s.f[15](p+36)||eioCtx.audioOnly&&(0,h.k)(eioDts-eioTrack.baseMediaDecodeTime,eioStream.timeBase,m.i0)>=eioFmt.options.minFragmentLength))||1===eioFmt.options.fragmentMode;
    if(eioClose)eioSteps.push({kind:"fragment",dts:1===eioCf.tracks.length?eioDts:void 0})
  }
  const eioPayload=eioMsePacketPayload(p);
  if(eioPayload>0)eioSteps.push({kind:"write",streamIndex:eioIndex,payloadBytes:eioPayload});
  if(opts&&opts.eof===!0){eioSteps.push({kind:"trailer"});eioSteps.push({kind:"flush"})}
  if(opts&&opts.flush===!0)eioSteps.push({kind:"muxflush"});
  if(!eioSteps.length)eioSteps.push({kind:"flush"});
  return{ok:!0,steps:eioSteps}
}
eioPlanFor(t,kind,opts){
  const eioCtx=t&&t.oformatContext,eioFmt=eioCtx&&eioCtx.oformat;
  const eioBase=this.eioPlanBaseRequest(t);
  if(!eioBase||!eioFmt)return{ok:!1,capability:!0,reason:"plan-request-unavailable"};
  const eioReq=Object.assign({},eioBase,{steps:[]});
  const eioBsf=eioMseBsfExtradataBytes(eioCtx,eioFmt);
  if(eioBsf>=0&&eioFmt.annexb2AvccFilter)eioReq.bsfResidentBytes=eioBsf;
  else eioReq.bsfResidentUnknown=!0;
  if("header"===kind){
    const eioKinds=eioMseExtradataKind(eioCtx);
    if(!eioKinds)return{ok:!1,capability:!0,reason:"extradata-unreadable"};
    eioReq.extradataKind=eioKinds;
    if(!eioFmt.annexb2AvccFilter){
      const eioInitStreams=eioCtx&&eioCtx.streams;
      if(!Array.isArray(eioInitStreams))return{ok:!1,capability:!0,reason:"init-codecpar-shape-invalid"};
      let eioInitStream=null;
      for(let i=0;i<eioInitStreams.length;i++){
        const eioStream=eioInitStreams[i];
        const eioCodecId=eioStream&&eioStream.codecpar&&eioStream.codecpar.codecId;
        if(27===eioCodecId||173===eioCodecId||196===eioCodecId){eioInitStream=eioStream;break}
      }
      let eioInitBytes=0;
      if(eioInitStream){
        let eioInitPtr=null;
        try{eioInitPtr=eioInitStream.codecpar[r.o9]}catch(err){return{ok:!1,capability:!0,reason:"init-codecpar-shape-invalid"}}
        if(!Number.isSafeInteger(eioInitPtr)||eioInitPtr<=0)return{ok:!1,capability:!0,reason:"init-codecpar-shape-invalid"};
        try{eioInitBytes=g.eioPayloadBytes(eioInitPtr)}catch(err){return{ok:!1,capability:!0,reason:"init-codecpar-shape-invalid"}}
        if(!eioPlanIsCount(eioInitBytes))return{ok:!1,capability:!0,reason:"init-codecpar-shape-invalid"}
      }
      eioReq.runtimeInitCodecparBytes=eioInitBytes
    }
    eioReq.steps=[{kind:"header"}]
  }else if("raw"===kind){
    const eioPayload=eioMsePacketPayload(opts&&opts.packet);
    const eioZero=eioPayload===0;
    return{ok:!0,upperBound:eioPayload,plan:Object.freeze({supported:!0,reason:null,detail:null,kind:"raw",steps:Object.freeze([Object.freeze({kind:"raw",payloadBytes:eioPayload})]),sites:Object.freeze([eioPlanSite("raw-packet-copy",eioPayload,eioPayload,"raw MPEG audio packet slice copied into the BufferQueue")]),worksetBytes:eioPayload,reservedUpperBound:eioPayload,zeroPayload:eioZero,measured:!1,unmeasured:Object.freeze([eioZero?"zero-payload raw operation reserves a valid zero-cap scope that forbids any allocation":"raw path copies the packet payload only"])})}
  }else if("packet"===kind){
    const eioCapability=this.eioPacketCapability(t,opts&&opts.packet);
    if(eioCapability)return{ok:!1,capability:!0,reason:eioCapability};
    const eioSteps=this.eioPacketSteps(t,opts&&opts.packet,opts);
    if(!eioSteps.ok)return eioSteps;
    eioReq.steps=eioSteps.steps
  }else if("trailer"===kind)eioReq.steps=[{kind:"trailer"}];
  else if("flush"===kind)eioReq.steps=[{kind:"flush"}];
  else return{ok:!1,capability:!0,reason:"operation-kind-invalid"};
  const eioPlan=eioPlanOperation(eioFmt,eioReq);
  if(!eioPlan||eioPlan.supported!==!0)return{ok:!1,capability:!0,reason:eioPlan&&eioPlan.reason||"plan-unsupported",detail:eioPlan&&eioPlan.detail};
  const eioUpper=eioPlanIsCount(eioPlan.reservedUpperBound)?eioPlan.reservedUpperBound:eioPlan.worksetBytes;
  if(!eioPlanIsCount(eioUpper))return{ok:!1,capability:!0,reason:"plan-invalid"};
  return{ok:!0,plan:eioPlan,upperBound:eioUpper}
}
eioOperationAcquire(t,eioPlan){
  if(!t||!t.eioOwned)return null;
  const eioOwner=t.eioOwned;
  const eioBytes=eioPlanIsCount(eioPlan.reservedUpperBound)?eioPlan.reservedUpperBound:eioPlan.worksetBytes;
  if(!eioPlanIsCount(eioBytes))throw eioMseError("EIO_MSE_PLAN_INVALID");
  const eioResult=this.eioAcquire(eioOwner,eioBytes,EIO_MSE_PLAN_LABEL);
  if(eioResult.ok===!0&&eioResult.handle)return{handle:eioResult.handle,bytes:eioBytes};
  const eioCode=eioResult.code||"EIO_MSE_WORKSET_REFUSED";
  eioOwner.refusals+=1;
  eioOwner.lastRefusal=eioCode;
  if(eioMseIsPressure(eioCode))return null;
  throw eioMseError(eioCode)
}
eioOperationOpen(t,eioPlan,eioHandle){
  const eioOwner=t.eioOwned;
  if(t.eioActiveOperation&&t.eioActiveOperation.open===!0)this.eioOperationAbort(t,eioMseError("EIO_MSE_PLAN_MISSING"));
  const eioPlanSites=eioPlan.sites||[];
  const eioSites=[];
  let eioSiteBytes=0;
  for(let i=0;i<eioPlanSites.length;i++){
    const eioSite=eioPlanSites[i];
    if(!eioSite)continue;
    eioSites.push(Object.freeze({name:eioSite.name,bytes:eioSite.bytes,allocationBytes:eioSite.allocationBytes}));
    eioSiteBytes=eioPlanAdd(eioSiteBytes,eioSite.allocationBytes);
    if(eioSiteBytes<0){eioSiteBytes=0;break}
  }
  const eioScope={handle:eioHandle.handle,bytes:eioHandle.bytes,remaining:eioHandle.bytes,refs:0,closed:!1,atomic:!0,kind:eioPlan.kind||"operation",steps:eioPlan.steps||[],fault:null,retained:!1};
  eioOwner.scope=eioScope;
  eioOwner.lastPlan={label:EIO_MSE_PLAN_LABEL,kind:eioScope.kind,reservedUpperBound:eioHandle.bytes,siteBytes:eioSiteBytes,siteCount:eioSites.length,steps:eioScope.steps,sites:eioSites,measured:!1,exact:!1,unmeasured:eioPlan.unmeasured||[]};
  const eioOp={owner:eioOwner,scope:eioScope,open:!0};
  eioOwner.eioActiveOperation=eioOp;
  t.eioActiveOperation=eioOp;
  return eioScope
}
eioOperationSeal(t){
  const eioOp=t&&t.eioActiveOperation;
  if(!eioOp)return;
  t.eioActiveOperation=null;
  eioOp.open=!1;
  if(eioOp.owner&&eioOp.owner.eioActiveOperation===eioOp)eioOp.owner.eioActiveOperation=null;
  const eioScope=eioOp.scope;
  if(!eioScope)return;
  eioScope.closed=!0;
  if(eioScope.refs<=0)eioMseReleaseScope(eioScope)
}
/* A throw keeps the charge: a partial alias set is unproven, so the scope is
   retained and its fault stays visible instead of ending with zero refs. */
eioOperationAbort(t,err){
  const eioOp=t&&t.eioActiveOperation;
  if(!eioOp)return;
  t.eioActiveOperation=null;
  eioOp.open=!1;
  if(eioOp.owner&&eioOp.owner.eioActiveOperation===eioOp)eioOp.owner.eioActiveOperation=null;
  const eioScope=eioOp.scope;
  if(!eioScope)return;
  eioScope.fault=eioScope.fault||(err&&err.code)||"EIO_MSE_OPERATION_FAILED";
  eioScope.retained=!0;
  if(t.eioOwned)t.eioOwned.lastRefusal=eioScope.fault
}
/* A synchronous loop tick cannot leave an operation open for the next await.
   Abort such a stale open operation before planning anything against its
   already-mutated state, and keep its charge visible. */
eioAbortStaleOperation(t){
  if(t&&t.eioActiveOperation&&t.eioActiveOperation.open===!0)this.eioOperationAbort(t,eioMseError("EIO_MSE_PLAN_MISSING"))
}
/* Sync preflight+acquire. Returns null on ordinary pressure without touching
   any resource state; every other failure is a coded capability fault. */
eioPrepareOperation(t,kind,opts){
  this.eioAbortStaleOperation(t);
  const eioPlanned=this.eioPlanFor(t,kind,opts);
  if(!eioPlanned.ok){
    const eioErr=eioMseError("EIO_MSE_PLAN_UNSUPPORTED");
    eioErr.planReason=eioPlanned.reason;eioErr.planDetail=eioPlanned.detail;
    eioErr.capability=!0;
    throw eioErr
  }
  const eioHandle=this.eioOperationAcquire(t,eioPlanned.plan);
  if(!eioHandle)return null;
  this.eioOperationOpen(t,eioPlanned.plan,eioHandle);
  return eioPlanned.plan
}
eioOperationExecute(t,eioPlan,run){
  const eioHandle=this.eioOperationAcquire(t,eioPlan);
  if(!eioHandle)return{ok:!1,pressure:!0};
  this.eioOperationOpen(t,eioPlan,eioHandle);
  try{
    const eioValue=run();
    this.eioOperationSeal(t);
    return{ok:!0,value:eioValue}
  }catch(err){
    this.eioOperationAbort(t,err);
    throw err
  }
}
eioRunOperation(t,kind,opts,run){
  this.eioAbortStaleOperation(t);
  const eioPlanned=this.eioPlanFor(t,kind,opts);
  if(!eioPlanned.ok){
    const eioErr=eioMseError("EIO_MSE_PLAN_UNSUPPORTED");
    eioErr.planReason=eioPlanned.reason;eioErr.planDetail=eioPlanned.detail;
    eioErr.capability=!0;
    throw eioErr
  }
  return this.eioOperationExecute(t,eioPlanned.plan,run)
}
/* Async consumer-active path: repeated 25ms preflights with generation and
   cancellation checks, never a fatal capacity error. */
async eioRunOperationWait(t,kind,opts,run){
  const eioGeneration=t.eioOpGeneration||0;
  for(;;){
    this.eioAbortStaleOperation(t);
    if((t.eioOpGeneration||0)!==eioGeneration||t.eioReleased===!0)throw eioMseError("EIO_MSE_OPERATION_CANCELLED");
    const eioPlanned=this.eioPlanFor(t,kind,opts);
    if(!eioPlanned.ok){
      const eioErr=eioMseError("EIO_MSE_PLAN_UNSUPPORTED");
      eioErr.planReason=eioPlanned.reason;
      eioErr.planDetail=eioPlanned.detail;
      eioErr.capability=!0;
      throw eioErr
    }
    const eioHandle=this.eioOperationAcquire(t,eioPlanned.plan);
    if(eioHandle){
      this.eioOperationOpen(t,eioPlanned.plan,eioHandle);
      try{
        const eioValue=run();
        this.eioOperationSeal(t);
        return eioValue
      }catch(err){
        this.eioOperationAbort(t,err);
        throw err
      }
    }
    await new Promise((eioResolve=>setTimeout(eioResolve,EIO_MSE_RETRY_MS)))
  }
}
eioScheduleWake(t){
  if(!t||t.eioWaitTimer||!t.loop)return;
  t.eioWaitTimer=setTimeout((()=>{
    t.eioWaitTimer=null;
    const eioLoop=t.loop;
    if(eioLoop&&"function"==typeof eioLoop.isStarted&&eioLoop.isStarted())eioLoop.restart()
  }),EIO_MSE_RETRY_MS)
}
eioCancelWake(t){
  if(!t)return;
  if(t.eioWaitTimer){clearTimeout(t.eioWaitTimer);t.eioWaitTimer=null}
  t.eioOpGeneration=(t.eioOpGeneration||0)+1
}
eioWorksetSnapshot(t){
  const eioStreams={};
  ["audio","video"].forEach((eioType=>{
    const eioRes=t[eioType];
    const eioOwner=eioRes&&eioRes.eioOwned;
    if(!eioOwner){eioStreams[eioType]=null;return}
    eioStreams[eioType]={
      attached:!0,
      reservedUpperBound:eioOwner.scope&&eioOwner.scope.atomic?eioOwner.scope.bytes:null,
      kind:eioOwner.scope?eioOwner.scope.kind||null:null,
      remaining:eioOwner.scope&&eioOwner.scope.atomic?eioOwner.scope.remaining:null,
      retained:!!(eioOwner.scope&&eioOwner.scope.retained),
      fault:eioOwner.scope?eioOwner.scope.fault||null:null,
      lastPlan:eioOwner.lastPlan||null,
      measured:!1,
      exact:!1
    }
  }));
  const eioAudio=eioStreams.audio,eioVideo=eioStreams.video;
  return{
    label:EIO_MSE_PLAN_LABEL,
    measured:!1,
    exact:!1,
    kind:(eioAudio&&eioAudio.kind)||(eioVideo&&eioVideo.kind)||null,
    reservedUpperBound:(eioAudio&&eioPlanIsCount(eioAudio.reservedUpperBound)?eioAudio.reservedUpperBound:0)+(eioVideo&&eioPlanIsCount(eioVideo.reservedUpperBound)?eioVideo.reservedUpperBound:0),
    streams:eioStreams,
    assumptions:EIO_PLAN_ASSUMPTIONS,
    incomplete:EIO_MSE_INCOMPLETE.slice(),
    incompleteReasons:EIO_MSE_INCOMPLETE.map((t=>EIO_MSE_INCOMPLETE_REASONS[t]||t))
  }
}
/* Header: one scope covers the exact header plan and both Q.ho (init) and
   Q.bx (writeHeader), so no track window or mux copy can start unplanned. */
eioHeaderOperation(t){
  if(!t||t.enableRawMpeg)return;
  if(!t.eioOwned){
    Q.ho(t.oformatContext);
    Q.bx(t.oformatContext);
    return;
  }
  const eioPrepared=this.eioPrepareOperation(t,"header",{});
  if(!eioPrepared)throw eioMseError("EIO_PROBE_BUDGET_EXHAUSTED");
  try{
    Q.ho(t.oformatContext);
    Q.bx(t.oformatContext);
    this.eioOperationSeal(t)
  }catch(err){
    this.eioOperationAbort(t,err);
    throw err
  }
}
/* startMux runs before any consumer exists: a refusal is a finite probe
   exhaustion, never an unbounded await. */
eioProbeWrite(t,packet){
  if(!t||!t.eioOwned){this.writeAVPacket(packet,t);return}
  const eioPrepared=this.eioPrepareOperation(t,t.enableRawMpeg?"raw":"packet",{packet:packet,eof:!1,flush:!1});
  if(!eioPrepared)throw eioMseError("EIO_PROBE_BUDGET_EXHAUSTED");
  try{
    this.writeAVPacket(packet,t);
    this.eioOperationSeal(t)
  }catch(err){
    this.eioOperationAbort(t,err);
    throw err
  }
}
/* Explicit mux writer flush: exactly the pending window copy, no fragment
   emit is planned (the caller does not run one here). */
eioMuxFlushProbe(t){
  if(!t||!t.eioOwned){t.oformatContext.ioWriter.flush();return}
  const eioWriter=t.oformatContext.ioWriter;
  const eioPointer=eioWriter.getPointer();
  if(!Number.isSafeInteger(eioPointer)||eioPointer<0)throw eioMseError("EIO_MSE_PLAN_INVALID");
  const eioPlan=Object.freeze({
    supported:!0,
    reason:null,
    detail:null,
    kind:"muxflush",
    steps:Object.freeze([Object.freeze({kind:"muxflush"})]),
    sites:Object.freeze([eioPlanSite("mux-writer-flush",eioPointer,eioPointer,"explicit ioWriter.flush() of the pending mux window")]),
    worksetBytes:eioPointer,
    reservedUpperBound:eioPointer,
    measured:!1,
    unmeasured:Object.freeze(["explicit writer flush site only"])
  });
  const eioHandle=this.eioOperationAcquire(t,eioPlan);
  if(!eioHandle)throw eioMseError("EIO_PROBE_BUDGET_EXHAUSTED");
  this.eioOperationOpen(t,eioPlan,eioHandle);
  try{
    eioWriter.flush();
    this.eioOperationSeal(t)
  }catch(err){
    this.eioOperationAbort(t,err);
    throw err
  }
}
/* Consumer-active seek/fill writes and flushes await at most one 25ms preflight
   step at a time; a seek or unregister cancels the wait via the generation. */
async eioAfterSeekWrite(t,packet){
  if(!t||!t.eioOwned){this.writeAVPacket(packet,t);return}
  await this.eioRunOperationWait(t,t.enableRawMpeg?"raw":"packet",{packet:packet,eof:!1,flush:!1},(()=>{this.writeAVPacket(packet,t)}))
}
async eioAfterSeekFlush(t){
  if(!t||!t.eioOwned){Q.bX(t.oformatContext);return}
  await this.eioRunOperationWait(t,"flush",{},(()=>{Q.bX(t.oformatContext)}))
}
async unregisterTask(t){const e=this.tasks.get(t);if(!e)return;if(this.eioPullHasStrict(e))return this.eioPullLifetime(e).unregister(e,this);e&&(e.audio&&(this.eioCancelWake(e.audio),this.eioAbortStaleOperation(e.audio),e.audio.eioReleased=!0),e.video&&(this.eioCancelWake(e.video),this.eioAbortStaleOperation(e.video),e.video.eioReleased=!0),e.audio&&(e.audio.loop&&(await e.audio.oformatContext.destroy(),e.audio.loop.destroy()),e.audio.pullIPC&&e.audio.pullIPC.destroy(),e.audio.pullQueue.queue.length&&(e.audio.pullQueue.queue.forEach((t=>{e.avpacketPool.release(t)})),e.audio.pullQueue.queue.length=0),e.audio.codecpar&&(0,g.dn)(e.audio.codecpar)),e.video&&(e.video.loop&&(await e.video.oformatContext.destroy(),e.video.loop.destroy()),e.video.pullIPC&&e.video.pullIPC.destroy(),e.video.pullQueue.queue.length&&(e.video.pullQueue.queue.forEach((t=>{e.avpacketPool.release(t)})),e.video.pullQueue.queue.length=0),e.video.codecpar&&(0,g.dn)(e.video.codecpar)),this.eioReleaseStream(e.audio),this.eioReleaseStream(e.video),e.controlIPCPort&&e.controlIPCPort.destroy(),this.tasks.delete(t))}}},2633:(t,e,i)=>{i.d(e,{A:()=>c});var a,r=i(19371),s=i(23902),n=i(34117);!function(t){t[t.ADD=0]="ADD",t[t.REMOVE=1]="REMOVE",t[t.UPDATE_TIMESTAMP_OFFSET=2]="UPDATE_TIMESTAMP_OFFSET",t[t.CALLBACK=3]="CALLBACK",t[t.CHANGE_MIME_TYPE=4]="CHANGE_MIME_TYPE"}(a||(a={}));const o={mediaBufferMax:10};class c{constructor(){let t=arguments.length>0&&void 0!==arguments[0]?arguments[0]:{};(0,r.A)(this,"sourceBuffer",void 0),(0,r.A)(this,"operatorQueue",void 0),(0,r.A)(this,"updating",void 0),(0,r.A)(this,"lastRemoveTime",void 0),(0,r.A)(this,"paddingCallback",void 0),(0,r.A)(this,"options",void 0),(0,r.A)(this,"ending",void 0),(0,r.A)(this,"onQuotaExceededError",void 0),(0,r.A)(this,"onEnded",void 0),this.options=s.X$({},o,t),this.operatorQueue=[],this.updating=!1,this.lastRemoveTime=0,this.ending=!1,this.eioInFlight=null,this.eioFaultedInput=null}setSourceBuffer(t){this.sourceBuffer&&(this.sourceBuffer.onupdateend=null,this.sourceBuffer.onerror=null),this.sourceBuffer=t,this.sourceBuffer.onupdateend=()=>{const eioSettled=this.eioInFlight;this.eioInFlight=null,eioSettled&&eioSettled.eioClaim&&eioSettled.eioClaim.drop(),this.paddingCallback&&(this.paddingCallback(),this.paddingCallback=null),this.operatorQueue&&this.operatorQueue.length?this.enqueue():(this.updating=!1,this.ending&&this.onEnded&&this.onEnded())},this.sourceBuffer.onerror=t=>{const eioFailed=this.eioInFlight;this.eioInFlight=null,eioFailed&&eioFailed.eioClaim&&eioFailed.eioClaim.drop(),n.z3(`track update buffer error: ${t}`,"packages/avrender/src/track/Track.ts",112)}}changeMimeType(t,e){this.sourceBuffer&&(this.operatorQueue.push({operator:a.CHANGE_MIME_TYPE,type:t,mode:e}),this.updating||this.enqueue())}enqueue(){if(this.operatorQueue.length){const t=this.operatorQueue.shift();if(t.operator===a.ADD)try{this.sourceBuffer.appendBuffer(t.buffer),this.updating=!0,this.eioInFlight=t,t.callback&&(this.paddingCallback=t.callback)}catch(e){if(!(e instanceof DOMException&&"QuotaExceededError"===e.name)){this.eioFaultedInput=t;throw e}this.operatorQueue.unshift(t),this.onQuotaExceededError&&this.onQuotaExceededError(),this.updating=!1}else t.operator===a.REMOVE?(this.sourceBuffer.remove(t.start,t.end),this.updating=!0,t.callback&&(this.paddingCallback=t.callback)):t.operator===a.UPDATE_TIMESTAMP_OFFSET?(this.sourceBuffer.timestampOffset=t.timestampOffset,t.callback&&t.callback(),this.operatorQueue.length?this.enqueue():this.updating=!1):t.operator===a.CALLBACK?(t.callback&&t.callback(),this.operatorQueue.length?this.enqueue():this.updating=!1):t.operator===a.CHANGE_MIME_TYPE&&(this.sourceBuffer.changeType(t.type),this.sourceBuffer.mode=t.mode,this.sourceBuffer.timestampOffset=0)}}addBuffer(t,e){t?(this.operatorQueue.push({operator:a.ADD,buffer:t,callback:e,eioClaim:t&&t.eioClaim||null}),this.updating||this.enqueue()):e&&e()}insertEnqueueCallback(t){this.operatorQueue.push({operator:a.CALLBACK,callback:t}),this.updating||this.enqueue()}updateTimestampOffset(t,e){this.operatorQueue.push({operator:a.UPDATE_TIMESTAMP_OFFSET,timestampOffset:t,callback:e}),this.updating||this.enqueue()}removeBuffer(t,e){this.ending||(t=Math.floor(t))-this.lastRemoveTime<this.options.mediaBufferMax<<1||(this.operatorQueue.push({operator:a.REMOVE,start:this.lastRemoveTime,end:t-this.options.mediaBufferMax,callback:e}),this.updating||this.enqueue(),this.lastRemoveTime=t-this.options.mediaBufferMax)}removeAllBuffer(t){this.sourceBuffer.buffered.length?(this.operatorQueue.push({operator:a.REMOVE,start:this.sourceBuffer.buffered.start(0),end:this.sourceBuffer.buffered.end(this.sourceBuffer.buffered.length-1),callback:t}),this.updating||this.enqueue()):t&&t()}end(){this.ending=!0,this.updating||this.operatorQueue.length||this.onEnded&&this.onEnded()}stop(){if(this.sourceBuffer){try{this.sourceBuffer.abort(),this.updating=!1;const eioAborted=this.eioInFlight;this.eioInFlight=null,eioAborted&&eioAborted.eioClaim&&eioAborted.eioClaim.drop()}catch(t){}try{this.sourceBuffer.buffered.length&&(this.updating?this.operatorQueue.push({operator:a.REMOVE,start:this.sourceBuffer.buffered.start(0),end:this.sourceBuffer.buffered.end(this.sourceBuffer.buffered.length-1)}):(this.sourceBuffer.remove(this.sourceBuffer.buffered.start(0),this.sourceBuffer.buffered.end(this.sourceBuffer.buffered.length-1)),this.updating=!0)),this.paddingCallback&&(this.paddingCallback(),this.paddingCallback=null)}catch(t){}}}reset(){this.stop(),this.eioDropQueue(),this.operatorQueue.length=0,this.ending=!1}eioDropQueue(){const t=this.operatorQueue;if(!t)return;for(let e=0;e<t.length;e++){const i=t[e];i&&i.eioClaim&&i.eioClaim.drop()}}isPaused(){return!this.updating&&this.operatorQueue.length}getQueueLength(){return this.operatorQueue.length}getBufferedTime(){return this.sourceBuffer&&this.sourceBuffer.buffered.length?this.sourceBuffer.buffered.end(this.sourceBuffer.buffered.length-1)-this.sourceBuffer.buffered.start(0):0}getBufferedStart(){return this.sourceBuffer&&this.sourceBuffer.buffered.length?this.sourceBuffer.buffered.start(0):0}getBufferedEnd(){return this.sourceBuffer&&this.sourceBuffer.buffered.length?this.sourceBuffer.buffered.end(this.sourceBuffer.buffered.length-1):0}getBufferedDuration(t){if(this.sourceBuffer&&this.sourceBuffer.buffered.length){let e=0;try{for(let i=0;i<this.sourceBuffer.buffered.length;i++){const a=Math.max(t,this.sourceBuffer.buffered.start(i));e+=Math.max(t,this.sourceBuffer.buffered.end(i))-a}}catch(t){}return e}return 0}getSourceBuffer(){return this.sourceBuffer}setMediaBufferMax(t){this.options.mediaBufferMax=t}getMediaBufferMax(){return this.options.mediaBufferMax}destroy(){this.stop(),this.eioDropQueue(),this.operatorQueue=null,this.sourceBuffer&&(this.sourceBuffer.onupdateend=this.sourceBuffer.onerror=null,this.sourceBuffer=null)}}},2843:(t,e,i)=>{i.d(e,{A:()=>o});var a=i(19371),r=i(56238),s=i(46626),n=i(67669);class o{constructor(){(0,a.A)(this,"inCodecpar",void 0),(0,a.A)(this,"inTimeBase",void 0),(0,a.A)(this,"outCodecpar",void 0)}init(t,e){let eioCodecparLease;if(this.eioOwned){if("function"!=typeof this.eioOwned.nativeCodecparLease){const eioError=new Error("EIO_MSE_PLAN_MISSING");eioError.code="EIO_MSE_PLAN_MISSING";throw eioError}eioCodecparLease=this.eioOwned.nativeCodecparLease(t)}return this.inCodecpar=(0,s.Gy)(168),eioCodecparLease?(0,n.Yi)(this.inCodecpar,t,eioCodecparLease):(0,n.Yi)(this.inCodecpar,t),this.inTimeBase={den:r.f[15](e+4),num:r.f[15](e)},0}destroy(){this.inCodecpar&&((0,n.dn)(this.inCodecpar),this.inCodecpar=0)}}},8433:(t,e,i)=>{i.d(e,{A:()=>o});var a=i(32057),r=i(11984),s=i(29688),n=i(98901);function o(t,e,i){const o=e.privData,c=(0,n.k)((0,r.A)(o),e.timeBase,{den:i.timescale,num:1}),d=e.metadata.creationTime||0,u=e.metadata.modificationTime||0,f=o.layer||0,l=o.alternateGroup||0;let p=e.codecpar.width>0?e.codecpar.width:0,h=e.codecpar.height>0?e.codecpar.height:0;const m=(0,n.lb)(e.codecpar.sampleAspectRatio);0!==m&&m!==1/0&&(p=Math.floor(p*m)),p<s.zg&&(p<<=16),h<s.zg&&(h<<=16);let g=c<BigInt(s.f7)?0:1;g=d<s.f7?0:1,g=u<s.f7?0:1;let k=2;1&o.flags&&(k|=1),t.writeUint32(1===g?100:92),t.writeString("tkhd"),t.writeUint8(g),t.writeUint24(k),1===g?(t.writeUint64(d),t.writeUint64(u)):(t.writeUint32(Number(d)),t.writeUint32(Number(u))),t.writeUint32(o.trackId),t.writeUint32(0),1===g?t.writeUint64(c):t.writeUint32(Number(c)),t.writeUint32(0),t.writeUint32(0),t.writeInt16(f),t.writeInt16(o.perStreamGrouping?l:e.codecpar.codecType),1===e.codecpar.codecType?t.writeInt16(256):t.writeInt16(0),t.writeInt16(0);const w=e.metadata.matrix;w?(0,a.A)(t,w[0],w[1],w[3],w[4],w[6],w[7]):(0,a.A)(t,1,0,0,1,0,0),t.writeUint32(p),t.writeUint32(h)}},9235:(t,e,i)=>{function a(t,e,i){const a=i.currentFragment.tracks.find((t=>t.trackId===e.privData.trackId)),r=i.cencs?i.cencs[a.trackId]:null,s=t.getPos();t.writeUint32(0),t.writeString("senc"),t.writeUint32(a.cenc.useSubsamples?2:0),t.writeUint32(a.cenc.sampleEncryption.length),a.cenc.offset=Number(t.getPos()-i.currentFragment.pos),a.cenc.sampleEncryption.forEach((e=>{r.defaultPerSampleIVSize&&t.writeBuffer(e.iv),a.cenc.useSubsamples&&(t.writeUint16(e.subsamples.length),e.subsamples.forEach((e=>{t.writeUint16(e.bytesOfClearData),t.writeUint32(e.bytesOfProtectedData)})))})),i.boxsPositionInfo.push({pos:s,type:"senc",size:Number(t.getPos()-s)})}i.d(e,{A:()=>a})},9993:(t,e,i)=>{function a(t,e,i){const a=e.privData.chunkOffsets||[];t.writeUint32(16+4*a.length),t.writeString("stco"),t.writeUint8(0),t.writeUint24(0),t.writeUint32(a.length);for(let e=0;e<a.length;e++)t.writeUint32(Number(a[e]))}i.d(e,{A:()=>a})},10516:(t,e,i)=>{function a(t,e,i){const a=e.sideData[28];if(a)t.writeUint32(12+a.length),t.writeString("colr"),t.writeString("prof"),t.writeBuffer(a);else{const i=2===e.codecpar.colorRange;t.writeUint32(19),t.writeString("colr"),t.writeString("nclx"),t.writeUint16(e.codecpar.colorPrimaries),t.writeUint16(e.codecpar.colorTrc),t.writeUint16(e.codecpar.colorSpace),t.writeUint8(i?128:0)}}i.d(e,{A:()=>a})},11429:(t,e,i)=>{i.d(e,{A:()=>r});var a=i(22105);function r(t,e,i){const r=i.ac3Info,s=new a.A(2+(34*(r.numIndSub+1)+7>>3));s.writeU(13,r.dataRate),s.writeU(3,r.numIndSub);for(let t=0;t<r.numIndSub;t++)s.writeU(2,r.substream[t].fscod),s.writeU(5,r.substream[t].bsid),s.writeU(1,0),s.writeU(1,0),s.writeU(3,r.substream[t].bsmod),s.writeU(3,r.substream[t].acmod),s.writeU(1,r.substream[t].lfeon),s.writeU(5,0),s.writeU(4,r.substream[t].numDepSub),r.substream[t].numDepSub?s.writeU(9,r.substream[t].chanLoc):s.writeU(1,0);s.padding();const n=s.getPointer();t.writeUint32(8+n),t.writeString("dec3"),t.writeBuffer(s.getBuffer().subarray(0,n))}},11984:(t,e,i)=>{i.d(e,{A:()=>r});var a=i(29688);function r(t){return t.startPts!==a.Dh?t.lastPts-t.startPts:t.startDts===a.Dh?t.lastPts:t.lastPts-(t.startDts+BigInt(0|t.startCT))}},14307:(t,e,i)=>{i.d(e,{XA:()=>g,bX:()=>w,bx:()=>m,dJ:()=>k,ho:()=>h});var a=i(56238),r=i(6927),s=i(87903),n=i(35924),o=i(29688),c=i(83796),d=i(23902),u=i(83446),f=i(34117);const l="packages/avformat/src/mux.ts",p={zeroStart:!1,nonnegative:!1};function h(t){let e=arguments.length>1&&void 0!==arguments[1]?arguments[1]:{};const i=d.X$({},p,e);t.ioWriter||f.h2("need ioWriter",l,65),t.oformat||f.h2("need oformat",l,68),t.options=i,t.privateData2={firstPts:new Map,firstDts:new Map,dtsPtsDelta:new Map};let a=t.oformat.getCapabilities();if(a)for(let e=0;e<t.streams.length;e++){if(1024&t.streams[e].disposition)continue;const i=t.streams[e].codecpar.codecId,r=t.streams[e].codecpar.codecType;if(16===t.oformat.type){if(i<65536||i>69683)return f.z3(`format ${(0,s.L_)(t.oformat.type)} not support codecId ${(0,s.eG)(t.streams[e].codecpar.codecType,i)}`,l,90),n.r8}else if(2!==r&&4!==r&&!c.zy(a,i))return f.z3(`format ${(0,s.L_)(t.oformat.type)} not support codecId ${(0,s.eG)(t.streams[e].codecpar.codecType,i)}`,l,98),n.r8}return t.oformat.init(t)}function m(t){return t.oformat.writeHeader(t),0}function g(t,e){const i=t.privateData2;return i.firstDts.has(a.f[15](e+32))||i.firstDts.set(a.f[15](e+32),a.f[17](e+16)===o.Dh?BigInt(0):a.f[17](e+16)),i.firstPts.has(a.f[15](e+32))||(i.firstPts.set(a.f[15](e+32),a.f[17](e+8)===o.Dh?BigInt(0):a.f[17](e+8)),i.dtsPtsDelta.set(a.f[15](e+32),u.jk(i.firstDts.get(a.f[15](e+32)),i.firstPts.get(a.f[15](e+32))))),(t.options.zeroStart||t.options.nonnegative&&(i.firstDts.get(a.f[15](e+32))<0||i.firstPts.get(a.f[15](e+32))<0))&&(r.M[17](e+16,a.f[17](e+16)-i.dtsPtsDelta.get(a.f[15](e+32))),r.M[17](e+8,a.f[17](e+8)-i.dtsPtsDelta.get(a.f[15](e+32)))),t.oformat.writeAVPacket(t,e)}function k(t){return t.oformat.writeTrailer(t),0}function w(t){t.oformat.flush(t)}},15933:(t,e,i)=>{function a(t,e,i){const a=e.privData.stssSampleNumbers;t.writeUint32(16+4*a.length),t.writeString("stss"),t.writeUint8(0),t.writeUint24(0),t.writeUint32(a.length);for(let e=0;e<a.length;e++)t.writeUint32(a[e])}i.d(e,{A:()=>a})},20444:(t,e,i)=>{i.d(e,{A:()=>n});var a=i(80209),r=i(12062),s=i(99817);function n(t,e,i){let n;i.fragment&&e.sideData[1]?(n=e.sideData[1],delete e.sideData[1]):e.codecpar.extradata&&(n=(0,a.s3)(e.codecpar.extradata,e.codecpar.extradataSize)),n&&s.Bs(n)&&(n=r.S1(n)),t.writeUint32(8+(n?n.length:0)),t.writeString("hvcC"),n&&t.writeBuffer(n)}},22582:(t,e,i)=>{function a(t,e,i){const a=i.currentFragment.tracks.find((t=>t.trackId===e.privData.trackId));let r=57;a.baseIsMoof&&(r&=-2,r|=131072);const s=t.getPos();t.writeUint32(0),t.writeString("tfhd"),t.writeUint8(0),t.writeUint24(r),t.writeUint32(a.trackId),1&r&&t.writeUint64(a.baseDataOffset),8&r&&t.writeUint32(a.defaultSampleDuration),16&r&&t.writeUint32(a.defaultSampleSize),32&r&&t.writeUint32(a.defaultSampleFlags),i.boxsPositionInfo.push({pos:s,type:"tfhd",size:Number(t.getPos()-s)})}i.d(e,{A:()=>a})},25675:(t,e,i)=>{function a(t,e,i){const a=i.currentFragment.tracks.find((t=>t.trackId===e.privData.trackId)),r=t.getPos();t.writeUint32(0),t.writeString("saiz"),t.writeUint32(0),t.writeUint8(a.cenc.sampleSizes.length?0:a.cenc.defaultSampleInfoSize),t.writeUint32(a.cenc.sampleCount),a.cenc.sampleSizes.length&&a.cenc.sampleSizes.forEach((e=>{t.writeUint8(e)})),i.boxsPositionInfo.push({pos:r,type:"saiz",size:Number(t.getPos()-r)})}i.d(e,{A:()=>a})},28917:(t,e,i)=>{i.d(e,{A:()=>c});var a=i(3031),r=i(37766),s=i(84777),n=i(23902),o=i(41662);function c(t,e,i){const c=t.getPos();t.writeUint32(0),t.writeString("meta"),t.writeUint32(0),t.writeUint32(33),t.writeString("hdlr"),t.writeUint32(0),t.writeString("dhlr"),t.writeString(i.useMetadataTags?"mdta":"mdir"),t.writeUint32(0),t.writeUint32(0),t.writeUint32(0),t.writeUint8(0);let d=[],u=[];const f=n.BE(a.c);function l(t){return i.useMetadataTags?(d.push(t),d.length):f[t]||4===t.length?(0,r.A)(f[t]||t):void 0}if(u.push({key:l("encoder"),value:"libmedia-v1.3.1"}),i.covr&&u.push({key:l("covr"),value:i.covr.data,type:i.covr.type}),i.metadata&&n.__(i.metadata,((t,e)=>{if((s.Yj(t)||s.ai(t)||t instanceof Uint8Array)&&"encoder"!==e.toLocaleLowerCase()){let i=l(e);i&&u.push({key:i,value:t})}})),d.length&&(t.writeUint32(16+d.reduce(((t,e)=>t+(8+e.length)),0)),t.writeString("keys"),t.writeUint32(0),t.writeUint32(d.length),d.forEach((e=>{t.writeUint32(e.length+8),t.writeString("mdta"),t.writeString(e)}))),u.length){const e=t.getPos();t.writeUint32(0),t.writeString("ilst"),u.forEach((e=>{let i,a=0,r=0;if(s.ai(e.value))a=Math.trunc(e.value)===e.value?e.value<0?21:22:23,r=4;else if(e.value instanceof Uint8Array){var n;a=null!==(n=e.type)&&void 0!==n?n:0,r=e.value.length}else{if(!s.Yj(e.value))return;a=1,i=o.encode(e.value),r=i.length}t.writeUint32(24+r),t.writeUint32(e.key),t.writeUint32(16+r),t.writeString("data"),t.writeUint32(a),t.writeUint32(0),0===a||e.value instanceof Uint8Array?t.writeBuffer(e.value):1===a?t.writeBuffer(i):21===a?t.writeInt32(e.value):22===a?t.writeUint32(e.value):23===a&&t.writeFloat(e.value)})),i.boxsPositionInfo.push({pos:e,type:"ilst",size:Number(t.getPos()-e)})}i.boxsPositionInfo.push({pos:c,type:"meta",size:Number(t.getPos()-c)})}},31641:(t,e,i)=>{i.d(e,{A:()=>n});var a=i(80209),r=i(20642),s=i(99817);function n(t,e,i){let n;i.fragment&&e.sideData[1]?(n=e.sideData[1],delete e.sideData[1]):e.codecpar.extradata&&(n=(0,a.s3)(e.codecpar.extradata,e.codecpar.extradataSize)),n&&s.Bs(n)&&(n=r.S1(n)),t.writeUint32(8+(n?n.length:0)),t.writeString("avcC"),n&&t.writeBuffer(n)}},32057:(t,e,i)=>{function a(t,e,i,a,r,s,n){t.writeInt32(Math.floor(65536*e)),t.writeInt32(Math.floor(65536*i)),t.writeInt32(0),t.writeInt32(Math.floor(65536*a)),t.writeInt32(Math.floor(65536*r)),t.writeInt32(0),t.writeInt32(Math.floor(65536*s)),t.writeInt32(Math.floor(65536*n)),t.writeInt32(1073741824)}i.d(e,{A:()=>a})},32476:(t,e,i)=>{i.d(e,{A:()=>r});var a=i(19371);class r{constructor(){(0,a.A)(this,"type",-1)}async destroy(t){}}(0,a.A)(r,"Capabilities",[])},34814:(t,e,i)=>{function a(t,e,i,a){const r=t.getPos(),s=t.getPointer(),n=r-BigInt(Math.floor(s));let o=!1;switch(e<r&&e>=n?(t.seekInline(s+Number(e-r)),o=!0):t.seek(e),a){case"uint8":t.writeUint8(255&i);break;case"int8":t.writeInt8(i);break;case"uint16":t.writeUint16(65535&i);break;case"int16":t.writeInt16(i);break;case"uint32":t.writeUint32(i>>>0);break;case"int32":t.writeInt32(i);break;case"uint64":t.writeUint64(BigInt.asUintN(64,i));break;case"int64":t.writeInt64(i);break;case"float":t.writeFloat(i);break;case"double":t.writeDouble(i)}o?t.seekInline(s):t.seek(r)}i.d(e,{A:()=>a})},35131:(t,e,i)=>{i.d(e,{BB:()=>r,Ip:()=>s,Jl:()=>a});const a={1:function(t,e){return[{type:"tkhd"},{type:"mdia",children:[{type:"mdhd"},{type:"hdlr"},{type:"minf",children:[{type:"smhd"},t.isom?{type:"minf_hdlr"}:null,{type:"dinf",children:[{type:"dref",children:[{type:"url "}]}]},{type:"stbl",children:[{type:"stsd"},{type:"stts"},{type:"stsc"},{type:"stsz"},{type:"stco"}]}]}]}]},0:function(t,e){return[{type:"tkhd"},{type:"mdia",children:[{type:"mdhd"},{type:"hdlr"},{type:"minf",children:[{type:"vmhd"},t.isom?{type:"minf_hdlr"}:null,{type:"dinf",children:[{type:"dref",children:[{type:"url "}]}]},{type:"stbl",children:[{type:"stsd"},{type:"stts"},{type:"stsc"},{type:"stsz"},{type:"stco"}]}]}]}]},3:function(t,e){return[{type:"tkhd"},{type:"mdia",children:[{type:"mdhd"},{type:"hdlr"},{type:"minf",children:[{type:94232===e.codecpar.codecId?"sthd":"nmhd"},t.isom?{type:"minf_hdlr"}:null,{type:"dinf",children:[{type:"dref",children:[{type:"url "}]}]},{type:"stbl",children:[{type:"stsd"},{type:"stts"},{type:"stsc"},{type:"stsz"},{type:"stco"}]}]}]}]}},r={1:function(t,e){return[{type:"tkhd"},{type:"edts"},{type:"mdia",children:[{type:"mdhd"},{type:"hdlr"},{type:"minf",children:[{type:"smhd"},t.isom?{type:"minf_hdlr"}:null,{type:"dinf",children:[{type:"dref",children:[{type:"url "}]}]},{type:"stbl",children:[{type:"stsd"},{type:"stts"},{type:"stsc"},{type:"stsz"},{type:t.use64Mdat?"co64":"stco"}]}]}]}]},0:function(t,e){let i=!0;if(27===e.codecpar.codecId||173===e.codecpar.codecId||196===e.codecpar.codecId){const t=e.privData;2&e.codecpar.flags&&(!t.cttsSampleCounts||!t.cttsSampleCounts.length||1===t.cttsSampleCounts.length&&0===t.cttsSampleOffsets[0])&&(i=!1)}return[{type:"tkhd"},{type:"edts"},{type:"mdia",children:[{type:"mdhd"},{type:"hdlr"},{type:"minf",children:[{type:"vmhd"},t.isom?{type:"minf_hdlr"}:null,{type:"dinf",children:[{type:"dref",children:[{type:"url "}]}]},{type:"stbl",children:[{type:"stsd"},{type:"stts"},{type:"stss"},i?{type:"ctts"}:null,{type:"stsc"},{type:"stsz"},{type:t.use64Mdat?"co64":"stco"}]}]}]}]},3:function(t,e){return[{type:"tkhd"},{type:"edts"},{type:"mdia",children:[{type:"mdhd"},{type:"hdlr"},{type:"minf",children:[{type:94232===e.codecpar.codecId?"sthd":"nmhd"},t.isom?{type:"minf_hdlr"}:null,{type:"dinf",children:[{type:"dref",children:[{type:"url "}]}]},{type:"stbl",children:[{type:"stsd"},{type:"stts"},{type:"stsc"},{type:"stsz"},{type:t.use64Mdat?"co64":"stco"}]}]}]}]}},s=function(t){return[{type:"tfhd"},{type:"tfdt"},{type:"trun"},t.cenc&&(t.cenc.defaultSampleInfoSize||t.cenc.sampleSizes.length)?{type:"saiz"}:null,t.cenc&&(t.cenc.defaultSampleInfoSize||t.cenc.sampleSizes.length)?{type:"saio"}:null,t.cenc?{type:"senc"}:null]}},35476:(t,e,i)=>{i.d(e,{A:()=>r});var a=i(80209);function r(t,e,i){var r;t.writeUint32(12+(null!==(r=e.codecpar.extradataSize)&&void 0!==r?r:0)),t.writeString("vpcC"),t.writeUint8(1),t.writeUint24(0),i.fragment&&e.sideData[1]?(t.writeBuffer(e.sideData[1]),delete e.sideData[1]):e.codecpar.extradata&&t.writeBuffer((0,a.s3)(e.codecpar.extradata,e.codecpar.extradataSize))}},39695:(t,e,i)=>{i.d(e,{A:()=>r});var a=i(80209);function r(t,e,i){var r;t.writeUint32(8+(null!==(r=e.codecpar.extradataSize)&&void 0!==r?r:0)),t.writeString("av1C"),i.fragment&&e.sideData[1]?(t.writeBuffer(e.sideData[1]),delete e.sideData[1]):e.codecpar.extradata&&t.writeBuffer((0,a.s3)(e.codecpar.extradata,e.codecpar.extradataSize))}},40475:(t,e,i)=>{i.d(e,{A:()=>r});var a=i(83796);function r(t,e){const i=t.getPos(),r=t.getPointer(),s=i-BigInt(Math.floor(r)),n=[];a.__(e.boxsPositionInfo,(e=>{e.pos<i&&e.pos>=s?(t.seekInline(r+Number(e.pos-i)),t.writeUint32(e.size)):n.push(e)})),t.seekInline(r),n.length&&(a.__(n,(e=>{t.seek(e.pos),t.writeUint32(e.size)})),t.seek(i)),e.boxsPositionInfo=[]}},40924:(t,e,i)=>{function a(t,e,i){t.writeUint32(16),t.writeString("pasp"),t.writeUint32(1),t.writeUint32(1)}i.d(e,{A:()=>a})},41067:(t,e,i)=>{function a(t,e,i){var a,r,s;const n=i.currentFragment.tracks.find((t=>t.trackId===e.privData.trackId)),o=i.trexs.find((t=>{t.trackId,e.privData.trackId})),c=null!==(a=null==o?void 0:o.duration)&&void 0!==a?a:0,d=null!==(r=null==o?void 0:o.size)&&void 0!==r?r:0,u=null!==(s=null==o?void 0:o.flags)&&void 0!==s?s:0;t.writeUint32(32),t.writeString("trex"),t.writeUint8(0),t.writeUint24(0),t.writeUint32(n.trackId),t.writeUint32(1),t.writeUint32(c),t.writeUint32(d),t.writeUint32(u)}i.d(e,{A:()=>a})},41287:(t,e,i)=>{i.d(e,{A:()=>n});var a=i(84777),r=i(41662),s=i(98901);function n(t,e,i){const n=t.getPos();t.writeUint32(0),t.writeString("chpl"),t.writeUint32(16777216),t.writeUint32(0);const o=i.chapters;t.writeUint8(o.length),o.forEach((e=>{t.writeUint64((0,s.k)(e.start,e.timeBase,{num:1,den:1e7}));let i=e.metadata.title;if(i&&a.Yj(i)){let e=r.encode(i);e.length>255&&(e=e.slice(0,255)),t.writeUint8(e.length),t.writeBuffer(e)}else t.writeUint8(0)})),i.boxsPositionInfo.push({pos:n,type:"chpl",size:Number(t.getPos()-n)})}},43621:(t,e,i)=>{function a(t,e,i){t.writeUint32(20),t.writeString("vmhd"),t.writeUint8(0),t.writeUint24(1),t.writeUint64(BigInt(0))}i.d(e,{A:()=>a})},43628:(t,e,i)=>{function a(t,e,i){t.writeUint32(16),t.writeString("smhd"),t.writeUint32(0),t.writeUint16(0),t.writeUint16(0)}i.d(e,{A:()=>a})},44523:(t,e,i)=>{function a(t,e,i){t.writeUint32(16),t.writeString("mfhd"),t.writeUint8(0),t.writeUint24(0),t.writeUint32(i.currentFragment.sequence)}i.d(e,{A:()=>a})},44593:(t,e,i)=>{i.d(e,{A:()=>r});var a=i(22105);function r(t,e,i){t.writeUint32(11),t.writeString("dac3");const r=new a.A(3),s=i.ac3Info;r.writeU(2,s.substream[0].fscod),r.writeU(5,s.substream[0].bsid),r.writeU(3,s.substream[0].bsmod),r.writeU(3,s.substream[0].acmod),r.writeU(1,s.substream[0].lfeon),r.writeU(5,s.ac3BitrateCode),r.writeU(5,0),t.writeBuffer(r.getBuffer())}},44753:(t,e,i)=>{function a(t,e,i){const a=e.privData,r=i.currentFragment.tracks.find((t=>t.trackId===a.trackId)),s=r.firstSampleFlags||0,n=r.dataOffset||0,o=r.sampleDurations,c=r.sampleSizes,d=r.sampleFlags,u=r.sampleCompositionTimeOffset,f=r.sampleCount,l=o.length>0,p=c.length>0,h=d.length>0,m=u.length>0,g=0!==s;let k=1;g&&(k|=4),l&&(k|=256),p&&(k|=512),h&&(k|=1024),m&&(k|=2048);const w=t.getPos();t.writeUint32(0),t.writeString("trun"),t.writeUint8(1),t.writeUint24(k),t.writeUint32(f),r.dataOffsetPos=t.getPos(),t.writeInt32(n),g&&t.writeUint32(s);for(let e=0;e<f;e++)l&&t.writeUint32(o[e]||0),p&&t.writeUint32(c[e]||0),h&&t.writeUint32(d[e]||0),m&&t.writeInt32(u[e]||0);i.boxsPositionInfo.push({pos:w,type:"trun",size:Number(t.getPos()-w)})}i.d(e,{A:()=>a})},52189:(t,e,i)=>{i.d(e,{A:()=>n});var a=i(11984),r=i(84777),s=i(29688);function n(t,e,i){const n=e.privData,o=(0,a.A)(n),c=e.metadata.creationTime||0,d=e.metadata.modificationTime||0;let u=21956,f=e.metadata.language;f&&r.Yj(f)&&f.length>=3&&(u=(f.charCodeAt(0)-96&31)<<10|(f.charCodeAt(1)-96&31)<<5|f.charCodeAt(2)-96&31);let l=o<BigInt(s.f7)?0:1;l=c<s.f7?0:1,l=d<s.f7?0:1,t.writeUint32(1===l?44:32),t.writeString("mdhd"),t.writeUint8(l),t.writeUint24(0),1===l?(t.writeUint64(c),t.writeUint64(d)):(t.writeUint32(Number(c)),t.writeUint32(Number(d))),t.writeUint32(e.timeBase.den),1===l?t.writeUint64(o):t.writeUint32(Number(o)),t.writeUint16(u),t.writeUint16(0)}},52706:(t,e,i)=>{function a(t){let e=arguments.length>1&&void 0!==arguments[1]?arguments[1]:0;if(!t)return!1;if(t.length<2)return!0;let i=t[e],a=e+1;for(;a<t.length&&i===t[a];a++);return a===t.length}i.d(e,{A:()=>a})},54070:(t,e,i)=>{i.d(e,{Ro:()=>w,dI:()=>k,l7:()=>m,yT:()=>b});var a=i(54568),r=i(56238),s=i(37766),n=i(23716),o=i(76902),c=i(35131),d=i(40475),u=i(83796),f=i(41662),l=i(80209),p=i(85626),h=i(46564);function m(t,e){t.flush();const i=t.getPointer(),a=t.getPos();t.writeUint32(0),t.writeUint32((0,s.A)("ftyp")),t.writeUint32(e.majorBrand||(0,s.A)("isom")),t.writeUint32(e.minorVersion||512),u.__(e.compatibleBrand,(e=>{t.writeUint32(e)})),function(t,e,i){const a=t.getPointer();t.seekInline(e),t.writeUint32(i),t.seekInline(a)}(t,i,Number(t.getPos()-a)),e.isom?(t.writeUint32(8),t.writeUint32((0,s.A)("wide"))):e.fragment||(t.writeUint32(8),t.writeUint32((0,s.A)("free")))}function g(t,e,i,a){u.__(e,(e=>{if(!e)return!0;if(o.A[e.type])o.A[e.type](t,i,a);else if(e.children){const r=t.getPos();t.writeUint32(0),t.writeUint32((0,s.A)(e.type)),g(t,e.children,i,a),a.boxsPositionInfo.push({pos:r,type:e.type,size:Number(t.getPos()-r)})}else!function(t,e){const i=u.zy(n._l,e);t.writeUint32(i?12:8),t.writeUint32((0,s.A)(e)),i&&t.writeUint32(0)}(t,e.type)}))}function k(t,e,i){var n;const m=t.getPos();t.writeUint32(0),t.writeUint32((0,s.A)("moov")),o.A.mvhd(t,null,i),u.__(e.streams,(e=>{if(1024&e.disposition||1!==e.codecpar.codecType&&0!==e.codecpar.codecType&&3!==e.codecpar.codecType)return!0;const a=t.getPos();t.writeUint32(0),t.writeUint32((0,s.A)("trak")),g(t,i.fragment?c.Jl[e.codecpar.codecType](i,e):c.BB[e.codecpar.codecType](i,e),e,i);const r=e.metadata.title;if(r){const e=f.encode(r);t.writeUint32(16+e.length),t.writeUint32((0,s.A)("udta")),t.writeUint32(8+e.length),t.writeUint32((0,s.A)("name")),t.writeBuffer(e)}i.boxsPositionInfo.push({pos:a,type:"trak",size:Number(t.getPos()-a)})}));let k=t.getPos();if(t.writeUint32(0),t.writeUint32((0,s.A)("udta")),o.A.meta(t,null,i),null!==(n=i.chapters)&&void 0!==n&&n.length&&o.A.chpl(t,null,i),i.boxsPositionInfo.push({pos:k,type:"udta",size:Number(t.getPos()-k)}),i.fragment){const n=t.getPos();t.writeUint32(0),t.writeUint32((0,s.A)("mvex")),u.__(e.streams,(e=>{if(1024&e.disposition||1!==e.codecpar.codecType&&0!==e.codecpar.codecType&&3!==e.codecpar.codecType)return!0;o.A.trex(t,e,i)})),i.boxsPositionInfo.push({pos:n,type:"mvex",size:Number(t.getPos()-n)}),i.ignoreEncryption||function(t,e,i){const n=[];u.__(e.streams,(t=>{const e=(0,h.Un)(t.codecpar[a.o9]+20,t.codecpar[a.o9]+24,24);e&&(0,p.ZK)((0,l.s3)(r.f[20](e),r.f[25](e+4))).forEach((t=>{(function(t){if(!n.length)return!1;for(let e=0;e<n.length;e++){const i=n[e];if(!u.FB(i.systemId,t.systemId))return!1;if(!i.keyIds||!t.keyIds)return!1;if(i.keyIds.length!==t.keyIds.length)return!1;for(let e=0;e<i.keyIds.length;e++)if(!u.FB(i.keyIds[e],t.keyIds[e]))return!1;if(!u.FB(i.data,t.data))return!1}return!0})(t)||n.push(t)}))})),n.length&&n.forEach((e=>{const a=t.getPos();t.writeUint32(0),t.writeUint32((0,s.A)("pssh")),t.writeUint8(1),t.writeUint24(0),t.writeBuffer(e.systemId),t.writeUint32(e.keyIds.length),e.keyIds.forEach((e=>{t.writeBuffer(e)})),t.writeUint32(e.data.length),t.writeBuffer(e.data),i.boxsPositionInfo.push({pos:a,type:"pssh",size:Number(t.getPos()-a)})}))}(t,e,i)}i.boxsPositionInfo.push({pos:m,type:"moov",size:Number(t.getPos()-m)}),(0,d.A)(t,i)}function w(t,e,i){const a=t.getPos();t.writeUint32(0),t.writeUint32((0,s.A)("moof")),o.A.mfhd(t,null,i),u.__(i.currentFragment.tracks,(a=>{if(!a.sampleCount)return!0;const r=t.getPos();t.writeUint32(0),t.writeUint32((0,s.A)("traf"));const n=e.streams.find((t=>t.privData.trackId===a.trackId));g(t,(0,c.Ip)(a),n,i),i.boxsPositionInfo.push({pos:r,type:"traf",size:Number(t.getPos()-r)})}));const r=Number(t.getPos()-a);i.boxsPositionInfo.push({pos:a,type:"moof",size:r}),i.currentFragment.size=r}function b(t,e,i){let a=24;u.__(i.currentFragment.tracks,(t=>{const i=e.streams.find((e=>e.index===t.streamIndex)).privData;i.fragIndexes.length&&(a+=24+19*i.fragIndexes.length)})),t.writeUint32(a),t.writeString("mfra"),u.__(i.currentFragment.tracks,(i=>{const a=e.streams.find((t=>t.index===i.streamIndex)).privData;a.fragIndexes.length&&(t.writeUint32(24+19*a.fragIndexes.length),t.writeString("tfra"),t.writeUint8(1),t.writeUint24(0),t.writeUint32(i.trackId),t.writeUint32(0),t.writeUint32(a.fragIndexes.length),a.fragIndexes.forEach((e=>{t.writeUint64(e.time),t.writeUint64(e.pos),t.writeUint8(1),t.writeUint8(1),t.writeUint8(1)})))})),t.writeUint32(16),t.writeString("mfro"),t.writeUint32(0),t.writeUint32(a)}},57170:(t,e,i)=>{function a(t,e,i){const a=t.getPos();t.writeUint32(0),t.writeString("hdlr"),t.writeUint8(0),t.writeUint24(0);let r="dhlr",s="url ",n="DataHandler";e&&(r="mhlr",1===e.codecpar.codecType?(s="soun",n="SoundHandler"):0===e.codecpar.codecType?(s="vide",n="VideoHandler"):3===e.codecpar.codecType?(s="text",n="SubtitleHandler"):(e.metadata.handlerName&&(n=e.metadata.handlerName),e.metadata.hdlrType&&(s=e.metadata.hdlrType))),t.writeString(r),t.writeString(s),t.writeUint32(0),t.writeUint32(0),t.writeUint32(0),e&&!i.isom||t.writeUint8(n.length),t.writeString(n),e&&!i.isom&&t.writeUint8(0),i.boxsPositionInfo.push({pos:a,type:"hdlr",size:Number(t.getPos()-a)})}i.d(e,{A:()=>a})},58030:(t,e,i)=>{function a(t,e,i){const a=e.privData.sampleSizes;t.writeUint32(20+4*a.length),t.writeString("stsz"),t.writeUint8(0),t.writeUint24(0),t.writeUint32(0),t.writeUint32(a.length);for(let e=0;e<a.length;e++)t.writeUint32(a[e])}i.d(e,{A:()=>a})},60033:(t,e,i)=>{i.d(e,{A:()=>n});var a=i(52617),r=i(80209);function s(t,e,i){t.writeUint8(e);for(let e=3;e>0;e--)t.writeUint8(i>>7*e|128);t.writeUint8(127&i)}function n(t,e,i){const n=e.privData,o=e.codecpar.extradata?5+e.codecpar.extradataSize:0,c=t.getPos();t.writeUint32(0),t.writeString("esds"),t.writeUint32(0),s(t,3,21+o+5+1),t.writeUint16(n.trackId),t.writeUint8(0),s(t,4,13+o),(86016===e.codecpar.codecId||86017===e.codecpar.codecId)&&e.codecpar.sampleRate>24e3?t.writeUint8(107):t.writeUint8(a.zs[e.codecpar.codecId]),94208===e.codecpar.codecId?t.writeUint8(225):1===e.codecpar.codecType?t.writeUint8(21):t.writeUint8(17),t.writeUint24(0),t.writeUint32(0),t.writeUint32(0),e.codecpar.extradata&&(s(t,5,e.codecpar.extradataSize),t.writeBuffer((0,r.s3)(e.codecpar.extradata,e.codecpar.extradataSize))),s(t,6,1),t.writeUint8(2),i.boxsPositionInfo.push({pos:c,type:"esds",size:Number(t.getPos()-c)})}},62029:(t,e,i)=>{i.d(e,{A:()=>s});var a=i(34117);const r="packages/avformat/src/formats/isobmff/writing/stsc.ts";function s(t,e,i){const s=e.privData,n=s.stscFirstChunk,o=s.stscSamplesPerChunk,c=s.stscSampleDescriptionIndex;n.length===o.length&&n.length===c.length||a.R8("ctts firstChunk's length is not match samplesPerChunk's length or sampleDescriptionIndex's length",r,42);const d=Math.min(n.length,o.length,c.length);t.writeUint32(16+12*d),t.writeString("stsc"),t.writeUint8(0),t.writeUint24(0),t.writeUint32(d);for(let e=0;e<d;e++)t.writeUint32(n[e]),t.writeUint32(o[e]),t.writeUint32(c[e])}},64276:(t,e,i)=>{i.d(e,{A:()=>r});var a=i(57170);function r(t,e,i){(0,a.A)(t,null,i)}},65584:(t,e,i)=>{function a(t,e,i){const a=i.currentFragment.tracks.find((t=>t.trackId===e.privData.trackId));t.writeUint32(20),t.writeString("saio"),t.writeUint32(0),t.writeUint32(1),a.cenc.offsetPos=t.getPos(),t.writeUint32(0)}i.d(e,{A:()=>a})},69829:(t,e,i)=>{i.d(e,{A:()=>o});var a=i(34117),r=i(80209),s=i(73448);const n="packages/avformat/src/formats/isobmff/writing/dfla.ts";function o(t,e,i){let o;i.fragment&&e.sideData[1]?(o=e.sideData[1],delete e.sideData[1]):e.codecpar.extradata&&(o=(0,r.s3)(e.codecpar.extradata,e.codecpar.extradataSize)),o&&o.length===s.ob?(t.writeUint32(o.length+16),t.writeString("dfLa"),t.writeUint8(0),t.writeUint24(0),t.writeUint8(128),t.writeUint24(o.length),t.writeBuffer(o)):a.z3("invalid extradata",n,55)}},71422:(t,e,i)=>{i.d(e,{A:()=>s});var a=i(34117);const r="packages/avformat/src/formats/isobmff/writing/ctts.ts";function s(t,e,i){const s=e.privData,n=s.cttsSampleCounts||[],o=s.cttsSampleOffsets||[];n.length!==o.length&&a.R8("ctts sampleCounts's length is not match sampleOffsets's length",r,39);const c=Math.min(n.length,o.length);t.writeUint32(16+8*c),t.writeString("ctts"),t.writeUint8(1),t.writeUint24(0),t.writeUint32(c);for(let e=0;e<c;e++)t.writeUint32(n[e]),t.writeInt32(o[e])}},74817:(t,e,i)=>{i.d(e,{A:()=>s});var a=i(19371),r=i(41662);class s{constructor(){let t=arguments.length>0&&void 0!==arguments[0]?arguments[0]:1048576,e=!(arguments.length>1&&void 0!==arguments[1])||arguments[1],i=arguments.length>2?arguments[2]:void 0;if((0,a.A)(this,"data",void 0),(0,a.A)(this,"buffer",void 0),(0,a.A)(this,"pointer",void 0),(0,a.A)(this,"pos",void 0),(0,a.A)(this,"size",void 0),(0,a.A)(this,"littleEndian",void 0),(0,a.A)(this,"error",void 0),(0,a.A)(this,"onFlush",void 0),(0,a.A)(this,"onSeek",void 0),this.pointer=0,this.pos=BigInt(0),this.size=t,this.littleEndian=!e,this.error=0,i&&i.view)this.size=i.length,this.buffer=i,this.data=i.view;else if(i&&!i.byteOffset)this.size=i.length,this.buffer=i,this.data=new DataView(this.buffer.buffer);else{if(i)throw new Error("not support subarray of ArrayBuffer");this.buffer=new Uint8Array(this.size),this.data=new DataView(this.buffer.buffer)}}writeUint8(t){this.remainingLength()<1&&this.flush(),this.data.setUint8(this.pointer,t),this.pointer++,this.pos++}writeUint16(t){this.remainingLength()<2&&this.flush(),this.data.setUint16(this.pointer,t,this.littleEndian),this.pointer+=2,this.pos+=BigInt(2)}writeUint24(t){this.remainingLength()<3&&this.flush();const e=(16711680&t)>>16,i=(65280&t)>>8,a=255&t;this.littleEndian?(this.writeUint8(a),this.writeUint8(i),this.writeUint8(e)):(this.writeUint8(e),this.writeUint8(i),this.writeUint8(a))}writeUint32(t){this.remainingLength()<4&&this.flush(),this.data.setUint32(this.pointer,t,this.littleEndian),this.pointer+=4,this.pos+=BigInt(4)}writeUint64(t){this.remainingLength()<8&&this.flush(),this.data.setBigUint64(this.pointer,t,this.littleEndian),this.pointer+=8,this.pos+=BigInt(8)}writeInt8(t){this.remainingLength()<1&&this.flush(),this.data.setInt8(this.pointer,t),this.pointer++,this.pos++}writeInt16(t){this.remainingLength()<2&&this.flush(),this.data.setInt16(this.pointer,t,this.littleEndian),this.pointer+=2,this.pos+=BigInt(2)}writeInt24(t){this.writeUint24(t<0?t+16777216:t)}writeInt32(t){this.remainingLength()<4&&this.flush(),this.data.setInt32(this.pointer,t,this.littleEndian),this.pointer+=4,this.pos+=BigInt(4)}writeInt64(t){this.remainingLength()<8&&this.flush(),this.data.setBigInt64(this.pointer,t,this.littleEndian),this.pointer+=8,this.pos+=BigInt(8)}writeFloat(t){this.remainingLength()<4&&this.flush(),this.data.setFloat32(this.pointer,t,this.littleEndian),this.pointer+=4,this.pos+=BigInt(4)}writeDouble(t){this.remainingLength()<8&&this.flush(),this.data.setFloat64(this.pointer,t,this.littleEndian),this.pointer+=8,this.pos+=BigInt(8)}getPointer(){return this.pointer}getPos(){return this.pos}remainingLength(){return this.size-this.pointer}writeBuffer(t){if(!t.length)return;let e=t.length;if(this.remainingLength()<e){let i=0;for(;e>0;){this.flush();const a=Math.min(this.size,e);this.buffer.set(t.subarray(i,i+a),this.pointer),this.pointer+=a,this.pos+=BigInt(a),i+=a,e-=a}}else this.buffer.set(t,this.pointer),this.pointer+=e,this.pos+=BigInt(e)}writeString(t){const e=r.encode(t);return this.writeBuffer(e),e.length}flush(){if(!this.onFlush)throw this.error=-1048574,Error("IOWriter error, flush failed because of no flush callback");if(this.pointer){const t=this.onFlush(this.buffer.subarray(0,this.pointer));if(0!==t)throw this.error=t,Error("IOWriter error, flush failed")}this.pointer=0}flushToPos(t){if(!this.onFlush)throw this.error=-1048574,Error("IOWriter error, flush failed because of no flush callback");if(this.pointer){const e=this.onFlush(this.buffer.subarray(0,this.pointer),t);if(0!==e)throw this.error=e,Error("IOWriter error, flush failed")}this.pointer=0}seek(t){if(!this.onSeek)throw this.error=-1048574,Error("IOWriter error, seek failed because of no seek callback");this.flush();const e=this.onSeek(t);if(0!==e)throw this.error=e,Error("IOWriter error, seek failed");this.pos=t}seekInline(t){const e=this.pointer;this.pointer=Math.max(0,Math.min(this.size,t)),this.pos+=BigInt(this.pointer-e)}skip(t){const e=this.pointer;this.pointer=Math.min(this.size,this.pointer+t),this.pos+=BigInt(this.pointer-e)}back(t){const e=this.pointer;this.pointer=Math.max(0,this.pointer-t),this.pos+=BigInt(this.pointer-e)}getBuffer(){return this.buffer.subarray(0,this.pointer)}setEndian(t){this.littleEndian=!t}reset(){this.pointer=0,this.pos=BigInt(0),this.error=0}getBufferSize(){return this.size}}},74872:(t,e,i)=>{i.d(e,{A:()=>s});var a=i(19371),r=i(18528);class s{constructor(){(0,a.A)(this,"queue",void 0),(0,a.A)(this,"pos",void 0),(0,a.A)(this,"startPos",void 0),(0,a.A)(this,"endPos",void 0),(0,a.A)(this,"index",void 0),(0,a.A)(this,"offset",void 0),this.queue=[],this.startPos=BigInt(0),this.endPos=BigInt(0),this.pos=BigInt(0)}write(t){if(!t||!t.length){t&&t.eioClaim&&t.eioClaim.drop();return}if(this.pos===this.endPos)this.queue.push(t),this.endPos+=BigInt(t.length),this.pos+=BigInt(t.length);else if(t.length<this.queue[this.index].length-this.offset)this.queue[this.index].set(t,this.offset),this.offset+=t.length,this.pos+=BigInt(t.length),this.offset===this.queue[this.index].length&&(this.index++,this.offset=0),this.index===this.queue.length&&(this.index=-1,this.offset=-1,this.pos=this.endPos),t.eioClaim&&t.eioClaim.drop();else{let e=0;for(;e<t.length;){const i=Math.min(this.queue[this.index].length-this.offset,t.length-e);if(this.queue[this.index].set(t.subarray(e,e+i),this.offset),e+=i,this.offset+=i,this.pos+=BigInt(i),this.offset===this.queue[this.index].length){if(this.index+1===this.queue.length){if(e<t.length){const i=t.subarray(t.length-e);i.eioClaim=t.eioClaim,i.eioClaim&&(i.eioClaim.refs+=1),this.queue.push(i),this.endPos+=BigInt(i.length)}this.pos=this.endPos,this.index=-1,this.offset=-1;break}this.index++,this.offset=0}}t.eioClaim&&t.eioClaim.drop()}}seek(t){if(t<this.startPos||t>this.endPos)return!1;this.pos=t,this.index=-1,this.offset=-1;let e=this.startPos;for(let i=0;i<this.queue.length;i++){if(t<=e+BigInt(this.queue[i].length)){this.index=i,this.offset=Number(t-e);break}e+=BigInt(this.queue[i].length)}return this.index<0&&(this.pos=this.endPos),!0}flush(){this.startPos=this.endPos,this.pos=this.endPos,this.index=-1,this.offset=-1;const t=(0,r.A)(Uint8Array,this.queue);return this.queue.length=0,t}flushChunks(){this.startPos=this.endPos,this.pos=this.endPos,this.index=-1,this.offset=-1;const t=this.queue;return this.queue=[],t}discardChunks(){return this.flushChunks()}get size(){return this.queue.length}}},75873:(t,e,i)=>{i.d(e,{A:()=>s});var a=i(60033),r=i(80209);function s(t,e,i){const s=t.getPos();t.writeUint32(0),t.writeString("wave"),86035!==e.codecpar.codecId&&(t.writeUint32(12),t.writeString("frma"),t.writeUint32(e.codecpar.codecTag)),86018===e.codecpar.codecId?(t.writeUint32(12),t.writeString("mp4a"),t.writeUint32(0),(0,a.A)(t,e,i)):i.fragment&&e.sideData[1]?(t.writeBuffer(e.sideData[1]),delete e.sideData[1]):e.codecpar.extradata&&t.writeBuffer((0,r.s3)(e.codecpar.extradata,e.codecpar.extradataSize)),t.writeUint32(8),t.writeUint32(0),i.boxsPositionInfo.push({pos:s,type:"wave",size:Number(t.getPos()-s)})}},76091:(t,e,i)=>{i.d(e,{A:()=>s});var a=i(32057),r=i(29688);function s(t,e,i){const s=i.duration,n=i.creationTime||0,o=i.modificationTime||0,c=i.timescale||0;let d=i.nextTrackId||1;i.fragment&&(d=2);let u=s<BigInt(r.f7)?0:1;u=n<r.f7?0:1,u=o<r.f7?0:1,t.writeUint32(1===u?120:108),t.writeString("mvhd"),t.writeUint8(u),t.writeUint24(0),1===u?(t.writeUint64(n),t.writeUint64(o)):(t.writeUint32(Number(n)),t.writeUint32(Number(o))),t.writeUint32(c),1===u?t.writeUint64(s):t.writeUint32(Number(s)),t.writeUint32(65536),t.writeUint16(256),t.writeUint16(0),t.writeUint32(0),t.writeUint32(0),(0,a.A)(t,1,0,0,1,0,0),t.writeUint32(0),t.writeUint32(0),t.writeUint32(0),t.writeUint32(0),t.writeUint32(0),t.writeUint32(0),t.writeUint32(d)}},76902:(t,e,i)=>{i.d(e,{A:()=>M});var a=i(92782),r=i(71422),s=i(15933),n=i(58030),o=i(62029),c=i(9993),d=i(87608),u=i(52189),f=i(76091),l=i(8433),p=i(57170),h=i(76928),m=i(43621),g=i(81364),k=i(43628),w=i(77789),b=i(98075),v=i(80505),U=i(41067),I=i(44523),P=i(22582),y=i(76938),S=i(44753),A=i(64276),B=i(25675),x=i(65584),T=i(9235),C=i(28917),D=i(41287);const M={stts:a.A,ctts:r.A,stss:s.A,stsz:n.A,stsc:o.A,stco:c.A,co64:d.A,mdhd:u.A,mvhd:f.A,tkhd:l.A,hdlr:p.A,stsd:h.A,vmhd:m.A,edts:g.A,smhd:k.A,nmhd:w.A,sthd:b.A,dref:v.A,trex:U.A,mfhd:I.A,tfhd:P.A,tfdt:y.A,trun:S.A,minf_hdlr:A.A,saio:x.A,saiz:B.A,senc:T.A,meta:C.A,chpl:D.A}},76928:(t,e,i)=>{i.d(e,{A:()=>B});var a=i(80209),r=i(31641),s=i(20444),n=i(95414),o=i(35476),c=i(39695),d=i(69829),u=i(98552),f=i(60033),l=i(10516),p=i(40924),h=i(77632),m=i(75873),g=i(44593),k=i(11429),w=i(28320),b=i(37766);const v={27:"avc1",173:"hvc1",196:"vvc1",225:"av01",167:"vp09",86019:"ac-3",86056:"ec-3",94226:"wvtt",94232:"stpp",94213:"tx3g"};function U(t){if(t.codecTag)return(0,w.A)(t.codecTag);let e=v[t.codecId];return e||(e=0===t.codecType?"mp4v":1===t.codecType?86076===t.codecId?"Opus":86028===t.codecId?"fLaC":"mp4a":3===t.codecType?"text":"none"),e}function I(t,e,i){const a=e.privData,r=i.cencs[a.trackId],s=t.getPos();t.writeUint32(0),t.writeString("sinf"),t.writeUint32(12),t.writeString("frma"),t.writeString(U(e.codecpar)),t.writeUint32(20),t.writeString("schm"),t.writeUint32(0),t.writeUint32(r.schemeType||(0,b.A)("cenc")),t.writeUint32(r.schemeVersion||65536),t.writeUint32(16+r.defaultKeyId.length+8+(r.defaultConstantIV?1+r.defaultConstantIV.length:0)),t.writeString("schi"),t.writeUint32(16+r.defaultKeyId.length+(r.defaultConstantIV?1+r.defaultConstantIV.length:0)),t.writeString("tenc"),r.cryptByteBlock||r.skipByteBlock||r.pattern?t.writeUint8(1):t.writeUint8(0),t.writeUint24(0),t.writeUint8(0),t.writeUint8((15&(r.cryptByteBlock||0))<<4|15&(r.skipByteBlock||0)),t.writeUint8(1),t.writeUint8(r.defaultPerSampleIVSize),t.writeBuffer(r.defaultKeyId),r.defaultConstantIV&&(t.writeUint8(r.defaultConstantIV.length),t.writeBuffer(r.defaultConstantIV)),i.boxsPositionInfo.push({pos:s,type:"sinf",size:Number(t.getPos()-s)})}function P(t,e,i){const a=i.isom?1:0;t.writeUint32(0),t.writeUint16(0),t.writeUint16(1),t.writeUint16(a),t.writeUint16(0),t.writeUint32(0),i.isom?(t.writeUint16(e.codecpar.chLayout.nbChannels),65541===e.codecpar.codecId||65540===e.codecpar.codecId?t.writeUint16(8):69643===e.codecpar.codecId?t.writeUint16(e.codecpar.bitsPerCodedSample):t.writeUint16(16),t.writeUint16(-2)):(t.writeUint16(e.codecpar.chLayout.nbChannels),86028===e.codecpar.codecId||86032===e.codecpar.codecId?t.writeUint16(e.codecpar.bitsPerRawSample):t.writeUint16(16),t.writeUint16(0)),t.writeUint16(0),86076===e.codecpar.codecId?t.writeUint16(48e3):86060===e.codecpar.codecId?t.writeUint32(e.codecpar.sampleRate):t.writeUint16(e.codecpar.sampleRate),86060!==e.codecpar.codecId&&t.writeUint16(0),1===a&&(t.writeUint32(e.codecpar.frameSize),t.writeUint32(0),t.writeUint32(0),t.writeUint32(2))}function y(t,e,i){const a=U(e.codecpar);!i.isom||86018!==e.codecpar.codecId&&86019!==e.codecpar.codecId&&86056!==e.codecpar.codecId&&73728!==e.codecpar.codecId&&86032!==e.codecpar.codecId&&69638!==e.codecpar.codecId&&69633!==e.codecpar.codecId&&86035!==e.codecpar.codecId?86028===e.codecpar.codecId?(0,d.A)(t,e,i):86076===e.codecpar.codecId?(0,u.A)(t,e,i):86019===e.codecpar.codecId?(0,g.A)(t,e,i):86056===e.codecpar.codecId?(0,k.A)(t,e,i):"mp4a"==a&&(0,f.A)(t,e,i):(0,m.A)(t,e,i)}function S(t,e,i){const a=13==e.codecpar.codecId&&15==e.codecpar.format||13==e.codecpar.codecId&&1==e.codecpar.format||202==e.codecpar.codecId||203==e.codecpar.codecId||156==e.codecpar.codecId||127==e.codecpar.codecId;t.writeUint32(0),t.writeUint16(0),t.writeUint16(1),t.writeUint16(a?2:0),t.writeUint16(0),i.isom?(t.writeString("FFMP"),13===e.codecpar.codecId||a?(t.writeUint32(0),t.writeUint32(1024)):(t.writeUint32(512),t.writeUint32(512))):(t.writeUint32(0),t.writeUint32(0),t.writeUint32(0)),t.writeUint16(e.codecpar.width),t.writeUint16(e.codecpar.height),t.writeUint32(4718592),t.writeUint32(4718592),t.writeUint32(0),t.writeUint16(1);let r=e.metadata.compressorName||"";if(r=r.slice(0,31),t.writeUint8(r.length),t.writeString(r),r.length<31){let e=31-r.length;for(;e>0;)t.writeUint8(0),e--}i.isom&&e.codecpar.bitsPerCodedSample?t.writeUint16(e.codecpar.bitsPerCodedSample):t.writeUint16(24),t.writeUint16(65535)}function A(t,e,i){"mp4v"===U(e.codecpar)?(0,f.A)(t,e,i):27===e.codecpar.codecId?(0,r.A)(t,e,i):173===e.codecpar.codecId?(0,s.A)(t,e,i):196===e.codecpar.codecId?(0,n.A)(t,e,i):167===e.codecpar.codecId?(0,o.A)(t,e,i):225===e.codecpar.codecId&&(0,c.A)(t,e,i)}function B(t,e,i){const r=t.getPos();t.writeUint32(0),t.writeString("stsd");const s=e.privData,n=i.cencs&&i.cencs[s.trackId]&&(1===e.codecpar.codecType||0===e.codecpar.codecType)&&!i.ignoreEncryption;t.writeUint8(0),t.writeUint24(0),t.writeUint32(n?2:1),1===e.codecpar.codecType?(n&&function(t,e,i){const a=t.getPos();t.writeUint32(0),t.writeString("enca"),P(t,e,i),I(t,e,i),y(t,e,i),i.boxsPositionInfo.push({pos:a,type:"enca",size:Number(t.getPos()-a)})}(t,e,i),function(t,e,i){const a=t.getPos(),r=U(e.codecpar);t.writeUint32(0),t.writeString(r),P(t,e,i),y(t,e,i),i.isom||(0,h.A)(t,e,i),i.boxsPositionInfo.push({pos:a,type:r,size:Number(t.getPos()-a)})}(t,e,i)):0===e.codecpar.codecType?(n&&function(t,e,i){const a=t.getPos();t.writeUint32(0),t.writeString("encv"),S(t,e,i),I(t,e,i),A(t,e,i),i.boxsPositionInfo.push({pos:a,type:"encv",size:Number(t.getPos()-a)})}(t,e,i),function(t,e,i){const a=t.getPos(),r=U(e.codecpar);t.writeUint32(0),t.writeString(r),S(t,e,i),A(t,e,i),(0,l.A)(t,e,i),(0,p.A)(t,e,i),i.isom||(0,h.A)(t,e,i),i.boxsPositionInfo.push({pos:a,type:r,size:Number(t.getPos()-a)})}(t,e,i)):3===e.codecpar.codecType&&function(t,e,i){const r=t.getPos(),s=U(e.codecpar);t.writeUint32(0),t.writeString(s),t.writeUint32(0),t.writeUint16(0),t.writeUint16(1),94208===e.codecpar.codecId?(0,f.A)(t,e,i):e.codecpar.extradata&&t.writeBuffer((0,a.s3)(e.codecpar.extradata,e.codecpar.extradataSize)),i.isom||(0,h.A)(t,e,i),i.boxsPositionInfo.push({pos:r,type:s,size:Number(t.getPos()-r)})}(t,e,i),i.boxsPositionInfo.push({pos:r,type:"esds",size:Number(t.getPos()-r)})}},76938:(t,e,i)=>{function a(t,e,i){const a=i.currentFragment.tracks.find((t=>t.trackId===e.privData.trackId)),r=a?a.baseMediaDecodeTime:BigInt(0);t.writeUint32(20),t.writeString("tfdt"),t.writeUint8(1),t.writeUint24(0),t.writeUint64(r)}i.d(e,{A:()=>a})},77632:(t,e,i)=>{function a(t,e,i){t.writeUint32(20),t.writeString("btrt"),t.writeUint32(0),t.writeUint32(0),t.writeUint32(0)}i.d(e,{A:()=>a})},77789:(t,e,i)=>{function a(t,e,i){t.writeUint32(12),t.writeString("nmhd"),t.writeUint32(0)}i.d(e,{A:()=>a})},80505:(t,e,i)=>{function a(t,e,i){t.writeUint32(28),t.writeString("dref"),t.writeUint32(0),t.writeUint32(1),t.writeUint32(12),t.writeString("url "),t.writeUint32(1)}i.d(e,{A:()=>a})},81364:(t,e,i)=>{i.d(e,{A:()=>n});var a=i(11984),r=i(29688),s=i(98901);function n(t,e,i){const n=e.privData;let o=(0,s.k)((0,a.A)(n),e.timeBase,{den:i.timescale,num:1}),c=n.startCT;const d=(0,s.k)(n.startDts+BigInt(Math.floor(c)),e.timeBase,{den:i.timescale,num:1});let u=o<r.go?0:1;u|=d<r.go?0:1;const f=1+(d>0?1:0),l=24+f*(1===u?20:12);t.writeUint32(l),t.writeString("edts"),t.writeUint32(l-8),t.writeString("elst"),t.writeUint8(u),t.writeUint24(0),t.writeUint32(f),d>0?(1===u?(t.writeUint64(d),t.writeInt64(r.Dh)):(t.writeUint32(Number(d)),t.writeInt32(-1)),t.writeUint32(65536)):(c=-Math.min(Number(n.startDts),0),o+=d),i.fragment&&(o=BigInt(0)),1===u?(t.writeUint64(o),t.writeInt64(BigInt(Math.floor(c)))):(t.writeUint32(Number(o)),t.writeInt32(c)),t.writeUint32(65536)}},84724:(t,e,i)=>{i.d(e,{A:()=>m});var a=i(19371),r=i(56238),s=i(6927),n=i(2843),o=i(80209),c=i(34117),d=i(35924),u=i(46564),f=i(46626),l=i(20642),p=i(12062),h=i(25721);class m extends n.A{constructor(){let t=arguments.length>0&&void 0!==arguments[0]&&arguments[0];super(),(0,a.A)(this,"cache",void 0),(0,a.A)(this,"cached",void 0),(0,a.A)(this,"reverseSps",void 0),this.reverseSps=t}init(t,e){return super.init(t,e),this.cache=(0,u._5)(),this.cached=!1,0}destroy(){super.destroy(),(0,u.Qe)(this.cache),this.cache=0}sendAVPacket(t){if(64&r.f[15](t+36)){let e;(0,u.zu)(this.cache,t);const i=(0,o.JW)(r.f[20](t+24),r.f[15](t+28));if(27===r.f[15](this.inCodecpar+4)?e=l.oT(i,this.reverseSps):173===r.f[15](this.inCodecpar+4)?e=p.oT(i,this.reverseSps):196===r.f[15](this.inCodecpar+4)?e=h.oT(i,this.reverseSps):c.h2(`not support for codecId: ${r.f[15](this.inCodecpar+4)}`,"packages/avformat/src/bsf/h2645/Annexb2AvccFilter.ts",114),s.M[15](this.cache+36,-65&r.f[15](this.cache+36)),(0,u.NX)(this.cache,e.bufferPointer,e.length),e.key&&s.M[15](this.cache+36,1|r.f[15](this.cache+36)),e.extradata){const t=(0,f.sY)(e.extradata.length);(0,o.lW)(t,e.extradata.length,e.extradata),(0,u.Ow)(this.cache,1,t,e.extradata.length)}}else(0,u.rN)(this.cache,t);return this.cached=!0,0}receiveAVPacket(t){return this.cached?((0,u.Up)(t),(0,u.rN)(t,this.cache),this.cached=!1,0):d.LR}reset(){return 0}}},87608:(t,e,i)=>{function a(t,e,i){const a=e.privData.chunkOffsets||[];t.writeUint32(16+8*a.length),t.writeString("co64"),t.writeUint8(0),t.writeUint24(0),t.writeUint32(a.length);for(let e=0;e<a.length;e++)t.writeUint64(a[e])}i.d(e,{A:()=>a})},88560:(t,e,i)=>{i.d(e,{A:()=>M});var a=i(19371),r=i(54568),s=i(56238),n=i(32476),o=i(62743),c=i(37766),d=i(54070),u=i(78215),f=i(23905),l=i(40475),p=i(34814),h=i(52706),m=i(84724),g=i(11984),k=i(80209),w=i(85626),b=i(46564),v=i(98901),U=i(29688),I=i(56901),P=i(84777),y=i(83796),S=i(23902),A=i(83446),B=i(34117),x=i(18528),T=i(74817);const C="packages/avformat/src/formats/OIsobmffFormat.ts",D={fragmentMode:0,mp4Mode:0,fragment:!1,fastOpen:!1,defaultBaseIsMoof:!1,reverseSpsInAvcc:!1,ignoreEncryption:!1,minFragmentLength:5e3,minFragmentIndexLength:5e3,hasTfra:!0,useMetadataTags:!1};class M extends n.A{constructor(){let t=arguments.length>0&&void 0!==arguments[0]?arguments[0]:{};super(),(0,a.A)(this,"type",1),(0,a.A)(this,"context",void 0),(0,a.A)(this,"options",void 0),(0,a.A)(this,"annexb2AvccFilter",void 0),(0,a.A)(this,"avpacket",void 0),this.options=S.X$({},D,t),this.context=(0,o.A)()}init(t){return t.ioWriter.setEndian(!0),this.eioCountOnlyPlan!==!0&&(t.streams.forEach((t=>{this.annexb2AvccFilter||27!==t.codecpar.codecId&&173!==t.codecpar.codecId&&196!==t.codecpar.codecId||(this.annexb2AvccFilter=new m.A(this.options.reverseSpsInAvcc),this.eioOwned&&(this.annexb2AvccFilter.eioOwned=this.eioOwned),this.annexb2AvccFilter.init(t.codecpar[r.o9],t.timeBase[r.o9]))})),this.avpacket=(0,b._5)()),this.context.metadata=t.metadata,this.context.chapters=t.chapters,0}async destroy(t){this.annexb2AvccFilter&&(this.annexb2AvccFilter.destroy(),this.annexb2AvccFilter=null),this.avpacket&&((0,b.Qe)(this.avpacket),this.avpacket=0),this.eioOwned&&this.eioOwned.releaseWriters&&this.eioOwned.releaseWriters()}enableStreams(t){const e=[],i=[];for(let t=0;t<5;t++)e[t]=0,i[t]=-1;y.__(t.streams,((t,a)=>{if(-1===t.codecpar.codecType||t.codecpar.codecType>=5||1024&t.disposition)return!0;i[t.codecpar.codecType]<0&&(i[t.codecpar.codecType]=a),1&t.disposition&&(e[t.codecpar.codecType]++,t.privData.flags|=1)}));for(let a=0;a<5;a++)switch(a){case 0:case 1:case 3:e[a]>1&&(t.streams[e[a]].privData.perStreamGrouping=!0),!e[a]&&i[a]>=0&&(t.streams[i[a]].privData.flags|=1)}}writeHeader(t){this.context.majorBrand=(0,c.A)("isom"),this.context.minorVersion=512,this.context.compatibleBrand=[(0,c.A)("isom")],this.context.timescale=1e3,this.context.ignoreEncryption=this.options.ignoreEncryption,this.context.useMetadataTags=this.options.useMetadataTags;const e=t.streams.find((t=>1024&t.disposition&&(7===t.codecpar.codecId||61===t.codecpar.codecId||78===t.codecpar.codecId)));if(e&&e.attachedPic&&(this.context.covr={type:27,data:(0,b.iI)(e.attachedPic)},61===e.codecpar.codecId?this.context.covr.type=14:7===e.codecpar.codecId&&(this.context.covr.type=13)),this.options.fragment&&(this.context.compatibleBrand.push((0,c.A)("iso6")),this.context.fragment=!0),1===this.options.mp4Mode&&(this.context.isom=!0,this.context.majorBrand=(0,c.A)("qt  "),this.context.compatibleBrand=[this.context.majorBrand]),1!==this.options.mp4Mode){this.context.compatibleBrand.push((0,c.A)("iso2"));const e=t.getStreamByMediaType(0);e&&27===e.codecpar.codecId&&this.context.compatibleBrand.push((0,c.A)("avc1")),this.context.compatibleBrand.push((0,c.A)("mp41"))}if(d.l7(t.ioWriter,this.context),this.context.holdMoovPos=t.ioWriter.getPos(),this.options.fragment)this.context.currentFragment={pos:BigInt(0),currentTrack:null,sequence:1,tracks:[],size:0,firstWrote:!1},y.__(t.streams,((t,e)=>{const i=(0,u.A)();if(t.privData=i,i.chunkOffsets=[],i.cttsSampleCounts=[],i.cttsSampleOffsets=[],i.stscFirstChunk=[],i.stscSamplesPerChunk=[],i.stscSampleDescriptionIndex=[],i.stssSampleNumbers=[],i.sampleSizes=[],i.sttsSampleCounts=[],i.sttsSampleDeltas=[],i.alternateGroup=e,!(1024&t.disposition)){const e=(0,f.A)();e.baseIsMoof=this.options.defaultBaseIsMoof,e.streamIndex=t.index,e.trackId=this.context.nextTrackId++,i.trackId=e.trackId;const a=this.options.encryption||t.metadata.encryption;if(a){const t=this.context.cencs||{};t[e.trackId]={schemeType:a.schemeType,schemeVersion:a.schemeVersion,cryptByteBlock:a.cryptByteBlock,skipByteBlock:a.skipByteBlock,defaultConstantIV:a.constantIV,isProtected:1,defaultPerSampleIVSize:a.perSampleIVSize,defaultKeyId:a.kid,pattern:!!a.pattern},this.context.cencs=t}const eioWriter=this.eioOwned&&this.eioOwned.writerFactory?this.eioOwned.writerFactory(1048576):new T.A;e.ioWriter=eioWriter,e.ioWriter.onFlush=t=>(e.buffers.push(this.eioOwned&&this.eioOwned.copy?this.eioOwned.copy(t,"mse-track-output"):t.slice()),0),this.context.currentFragment.tracks.push(e)}})),this.enableStreams(t),d.dI(t.ioWriter,t,this.context),t.ioWriter.flush();else{y.__(t.streams,((t,e)=>{const i=(0,u.A)();t.privData=i,i.trackId=this.context.nextTrackId++,i.chunkOffsets=[],i.cttsSampleCounts=[],i.cttsSampleOffsets=[],i.stscFirstChunk=[],i.stscSamplesPerChunk=[],i.stscSampleDescriptionIndex=[],i.stssSampleNumbers=[],i.sampleSizes=[],i.sttsSampleCounts=[],i.sttsSampleDeltas=[],i.alternateGroup=e})),this.enableStreams(t);const e=t.ioWriter.getPos();t.ioWriter.writeUint32(0),t.ioWriter.writeUint32((0,c.A)("mdat")),this.context.boxsPositionInfo.push({pos:e,type:"mdat",size:0})}return t.getStreamByMediaType(0)||(this.context.audioOnly=!0),0}updateCurrentChunk(t){let e=this.context.currentChunk;if(!e||!e.sampleCount)return;const i=t.streams.find((t=>t.index===e.streamIndex)).privData;i.chunkCount++,i.chunkOffsets.push(e.pos),i.stscFirstChunk.length?i.lastStscCount!==e.sampleCount&&(i.stscFirstChunk.push(i.chunkCount),i.stscSamplesPerChunk.push(e.sampleCount),i.stscSampleDescriptionIndex.push(1),i.lastStscCount=e.sampleCount):(i.stscFirstChunk.push(i.chunkCount),i.stscSamplesPerChunk.push(e.sampleCount),i.stscSampleDescriptionIndex.push(1),i.lastStscCount=e.sampleCount)}updateCurrentFragment(t,e){if(e!=null&&this.context.currentFragment.tracks.length===1){const stream=t.streams.find(stream=>stream.index===this.context.currentFragment.tracks[0].streamIndex);if(stream&&e<=stream.privData.lastDts)e=void 0}if(this.context.currentFragment.firstWrote){y.__(this.context.currentFragment.tracks,(i=>{const a=t.streams.find((t=>t.index===i.streamIndex));if(!i.sampleCount||!a)return!0;const r=a.privData;var s,n;i.baseDataOffset=t.ioWriter.getPos(),this.context.currentFragment.pos=t.ioWriter.getPos(),i.sampleDurations.length?e&&i.sampleDurations.length===i.sampleSizes.length-1&&i.sampleDurations.push(Number(e-r.lastDts)):1===a.codecpar.codecType?e?i.sampleDurations.push(Number(e-r.lastDts)):a.codecpar.frameSize>0?i.sampleDurations.push(Number((0,v.k)(BigInt(Math.floor(a.codecpar.frameSize/a.codecpar.sampleRate*U.SF)),U.KR,a.timeBase))):(a.codecpar.codecId,i.sampleDurations.push(Number((0,v.k)(BigInt(Math.floor(1024/a.codecpar.sampleRate*U.SF)),U.KR,a.timeBase)))):0===a.codecpar.codecType?e?i.sampleDurations.push(Number(e-r.lastDts)):(0,v.lb)(a.codecpar.framerate)>0?i.sampleDurations.push(Number((0,v.k)(BigInt(Math.floor(1/(0,v.lb)(a.codecpar.framerate)*U.SF)),U.KR,a.timeBase))):i.sampleDurations.push(a.timeBase.den/(30*a.timeBase.num)>>>0):i.sampleDurations.push(0),r.lastDuration=i.sampleDurations[i.sampleSizes.length-1],(1===i.sampleFlags.length||(0,h.A)(i.sampleFlags,1))&&(i.firstSampleFlags=i.sampleFlags[0],i.defaultSampleFlags=null!==(s=i.sampleFlags[1])&&void 0!==s?s:i.firstSampleFlags,i.sampleFlags=[]),(1===i.sampleSizes.length||(0,h.A)(i.sampleSizes))&&(i.defaultSampleSize=i.sampleSizes[0],i.sampleSizes=[]),(1===i.sampleDurations.length||(0,h.A)(i.sampleDurations))&&(i.defaultSampleDuration=i.sampleDurations[0],i.sampleDurations=[]),i.cenc&&(1===i.cenc.sampleSizes.length||(0,h.A)(i.cenc.sampleSizes))&&(i.cenc.defaultSampleInfoSize=i.cenc.sampleSizes[0],i.cenc.sampleSizes=[]),1===a.codecpar.codecType?i.defaultSampleFlags=33554432:i.sampleFlags.length&&(i.defaultSampleFlags=i.sampleFlags[0]),i.sampleSizes.length&&(i.defaultSampleSize=i.sampleSizes[0]),i.sampleDurations.length&&(i.defaultSampleDuration=i.sampleDurations[0]),this.options.hasTfra&&(!r.fragIndexes.length||0===this.options.fragmentMode||(0,v.k)(i.baseDataOffset-i.lastFragIndexDts,a.timeBase,U.i0)>=this.options.minFragmentIndexLength)&&(r.fragIndexes.push({pos:i.baseDataOffset,time:i.baseMediaDecodeTime+BigInt(Math.floor(null!==(n=i.sampleCompositionTimeOffset[0])&&void 0!==n?n:0))}),i.lastFragIndexDts=r.lastDts)})),t.ioWriter.flush(),this.options.onFragmentStart&&this.options.onFragmentStart(),d.Ro(t.ioWriter,t,this.context);let i=this.context.currentFragment.size+8;const a=[];let r=8;y.__(this.context.currentFragment.tracks,(e=>{if(!e.sampleCount)return!0;e.ioWriter.flush();let s=0;for(let t=0;t<e.buffers.length;t++)s+=e.buffers[t].length;e.dataOffset=i,i+=s,r+=s;for(let t=0;t<e.buffers.length;t++)a.push(e.buffers[t]);(0,p.A)(t.ioWriter,e.dataOffsetPos,e.dataOffset,"int32"),e.cenc&&e.cenc.offsetPos&&(0,p.A)(t.ioWriter,e.cenc.offsetPos,e.cenc.offset,"int32"),e.buffers=[],e.sampleFlags=[],e.sampleSizes=[],e.sampleDurations=[],e.sampleCompositionTimeOffset=[],e.sampleCount=0,e.firstSampleFlags=0,e.cenc=null})),t.ioWriter.writeUint32(r),t.ioWriter.writeUint32((0,c.A)("mdat")),y.__(a,(e=>{t.ioWriter.writeBuffer(e),e.eioClaim&&e.eioClaim.drop()})),(0,l.A)(t.ioWriter,this.context),t.ioWriter.flush(),this.context.currentFragment.firstWrote=!1,this.context.currentFragment.sequence++}}handleEAC3(t,e){this.context.ac3Info||(this.context.ac3Info={done:!1,numBlocks:0,dataRate:0,ac3BitrateCode:-1,numIndSub:0,substream:[]});const i=this.context.ac3Info,a=I.R((0,k.s3)(s.f[20](t+24),s.f[15](t+28)));if(P.ai(a))i.done=!0;else if(i.dataRate=Math.max(i.dataRate,a.bitrate/1e3),i.ac3BitrateCode=Math.max(i.ac3BitrateCode,a.ac3BitrateCode),!i.done){if(a.bitstreamId<=10&&0!=a.substreamId)return;if(0===a.frameType||2==a.frameType){var r;if(a.substreamId>i.numIndSub+1)return;if(a.substreamId==i.numIndSub+1)return;if(a.substreamId<i.numIndSub||0==a.substreamId&&null!==(r=i.substream[0])&&void 0!==r&&r.bsid)return void(i.done=!0)}else if(0!=a.substreamId)return;if(i.substream[a.substreamId]||(i.substream[a.substreamId]={fscod:0,bsid:0,bsmod:0,acmod:0,lfeon:0,numDepSub:0,chanLoc:0}),i.substream[a.substreamId].fscod=a.srCode,i.substream[a.substreamId].bsid=a.bitstreamId,i.substream[a.substreamId].bsmod=a.bitstreamMode,i.substream[a.substreamId].acmod=a.channelMode,i.substream[a.substreamId].lfeon=a.lfeOn,86019===e.codecpar.codecId)return void(i.done=!0)}}writeTrackData(t,e,i,a){if(94226===a.codecpar.codecId){const r=a.privData;if(r.lastDuration>0){const n=r.lastDts+BigInt(Math.floor(r.lastDuration));if(n<i&&(0,v.k)(i-n,a.timeBase,U.i0)>200){t.writeUint32(8),t.writeString("vtte");const a=Number(i-n);if(this.context.fragment){const t=this.context.currentFragment.tracks.find((t=>t.streamIndex===s.f[15](e+32)));t.sampleCount++,t.sampleSizes.length||(t.baseMediaDecodeTime=n),t.sampleDurations.push(a),t.sampleSizes.push(8)}else{const t=Number(n-r.lastDts);this.context.currentChunk.sampleCount++,r.sampleSizes.push(8),r.sttsSampleDeltas[r.sttsSampleDeltas.length-1]===t?r.sttsSampleCounts[r.sttsSampleCounts.length-1]++:(r.sttsSampleCounts.push(1),r.sttsSampleDeltas.push(t))}r.lastPts=n,r.lastDts=n,r.lastDuration=a}}const n=(0,b.rU)(e,16),o=(0,b.rU)(e,17);let c=s.f[15](e+28)+8;return n&&(c+=8+(0|s.f[25](n+4))),o&&(c+=8+(0|s.f[25](o+4))),t.writeUint32(8+c),t.writeString("vttc"),n&&(t.writeUint32(8+s.f[25](n+4)),t.writeString("iden"),t.writeBuffer((0,k.s3)(s.f[20](n),s.f[25](n+4)))),o&&(t.writeUint32(8+s.f[25](o+4)),t.writeString("sttg"),t.writeBuffer((0,k.s3)(s.f[20](o),s.f[25](o+4)))),t.writeUint32(8+s.f[15](e+28)),t.writeString("payl"),t.writeBuffer((0,b.iI)(e)),8+c}return t.writeBuffer((0,b.iI)(e)),s.f[15](e+28)}writeAVPacket(t,e){if(!s.f[15](e+28))return B.R8(`packet's size is 0: ${s.f[15](e+32)}, ignore it`,C,781),0;const i=t.getStreamByIndex(s.f[15](e+32));if(!i||1024&i.disposition)return void B.R8(`can not found the stream width the avpacket's streamIndex: ${s.f[15](e+32)}, ignore it`,C,788);const a=i.privData;let r=(0,v.Mr)(s.f[17](e+16),e+72,i.timeBase),n=(0,v.Mr)(s.f[17](e+8)!==U.Dh?s.f[17](e+8):s.f[17](e+16),e+72,i.timeBase);const o=s.f[17](e+48)!==U.Dh?(0,v.Mr)(s.f[17](e+48),e+72,i.timeBase):U.Dh;if((27===i.codecpar.codecId||173===i.codecpar.codecId||196===i.codecpar.codecId)&&64&s.f[15](e+36)?(this.annexb2AvccFilter.sendAVPacket(e),this.annexb2AvccFilter.receiveAVPacket(this.avpacket),e=this.avpacket):86019!==i.codecpar.codecId&&86056!==i.codecpar.codecId||this.context.ac3Info&&this.context.ac3Info.done||this.handleEAC3(e,i),this.context.fragment){const c=this.context.currentFragment.tracks.find((t=>t.streamIndex===s.f[15](e+32)));if(c){c.tfdtDelay===U.Dh&&(c.tfdtDelay=r<0?-r:BigInt(0),n<0&&(c.trunPtsDelay=c.tfdtDelay)),r+=c.tfdtDelay,n+=c.trunPtsDelay,(0===this.options.fragmentMode&&(0===i.codecpar.codecType&&1&s.f[15](e+36)||this.context.audioOnly&&(0,v.k)(r-c.baseMediaDecodeTime,i.timeBase,U.i0)>=this.options.minFragmentLength)||1===this.options.fragmentMode)&&(1===this.context.currentFragment.tracks.length?this.updateCurrentFragment(t,r):this.updateCurrentFragment(t));const d=this.writeTrackData(c.ioWriter,e,r,i);if(c.sampleSizes.length||(c.baseMediaDecodeTime=r),c.sampleSizes.length&&(!c.sampleDurations[c.sampleSizes.length-1]||c.sampleDurations[c.sampleSizes.length-1]<=0)&&(c.sampleDurations[c.sampleSizes.length-1]=Number(r-a.lastDts)),o>0&&c.sampleDurations.push(Number(o)),c.sampleSizes.push(d),0===i.codecpar.codecType){let t=0;1&s.f[15](e+36)?t|=33554432:t|=16842752,c.sampleCompositionTimeOffset.push(Number((n!==U.Dh?n:r)-r)),c.sampleFlags.push(t)}const u=(0,b.Un)(e+40,e+44,25),f=this.context.cencs&&this.context.cencs[a.trackId];if(u&&f){const t=(0,w.p$)((0,k.s3)(s.f[20](u),s.f[25](u+4)));c.cenc||(c.cenc={sampleCount:0,defaultSampleInfoSize:0,sampleSizes:[],offset:0,useSubsamples:!1,sampleInfoOffset:[],sampleEncryption:[]}),t.subsamples.length&&(!c.cenc.useSubsamples&&c.cenc.sampleSizes.length&&(c.cenc.sampleSizes=c.cenc.sampleSizes.map((t=>t+2))),c.cenc.useSubsamples=!0),c.cenc.sampleCount++,c.cenc.sampleSizes.push(f.defaultPerSampleIVSize+(c.cenc.useSubsamples?2+6*t.subsamples.length:0)),c.cenc.sampleEncryption.push({iv:t.iv,subsamples:t.subsamples})}c.sampleCount++,a.lastPts=A.T9(a.lastPts,n+(o!==U.Dh?o:BigInt(0))),a.lastDts=r,o>0&&(a.lastDuration=Number(o)),this.context.currentFragment.firstWrote=!0}else B.R8(`can not found track width streamIndex ${s.f[15](e+32)}, ignore it`,C,926)}else{const c=t.ioWriter.getPos();let d=this.context.currentChunk;if(d?d.streamIndex!==s.f[15](e+32)?(this.updateCurrentChunk(t),d.streamIndex=s.f[15](e+32),d.sampleCount=1,d.pos=c):d.sampleCount++:d=this.context.currentChunk={pos:c,streamIndex:s.f[15](e+32),sampleCount:1},a.sampleSizes.push(this.writeTrackData(t.ioWriter,e,r,i)),0===i.codecpar.codecType&&1&s.f[15](e+36)&&a.stssSampleNumbers.push(a.sampleSizes.length),a.firstWrote){const t=Number(r-a.lastDts);a.sttsSampleDeltas[a.sttsSampleDeltas.length-1]===t?a.sttsSampleCounts[a.sttsSampleCounts.length-1]++:(a.sttsSampleCounts.push(1),a.sttsSampleDeltas.push(t))}else a.startDts=r,a.startCT=Number((n!==U.Dh?n:r)-r),a.firstWrote=!0;if(n>=0&&(a.startPts===U.Dh?a.startPts=n:a.startPts=A.jk(a.startPts,n)),0===i.codecpar.codecType){const t=Number((n!==U.Dh?n:r)-r);a.cttsSampleCounts.length&&a.cttsSampleOffsets[a.cttsSampleOffsets.length-1]===t?a.cttsSampleCounts[a.cttsSampleCounts.length-1]++:(a.cttsSampleCounts.push(1),a.cttsSampleOffsets.push(t))}a.lastPts=A.T9(a.lastPts,n+(o!==U.Dh?o:BigInt(0))),a.lastDts=r,a.lastDuration=Number(o)}return 0}writeTrailer(t){if(this.context.fragment)y.__(this.context.currentFragment.tracks,(e=>{const i=t.streams.find((t=>t.index===e.streamIndex)).privData;e.sampleCount&&(e.sampleDurations.length?e.sampleDurations.push(e.sampleDurations[e.sampleDurations.length-1]):e.sampleDurations=[i.lastDuration])})),this.updateCurrentFragment(t),this.options.hasTfra?d.yT(t.ioWriter,t,this.context):(t.ioWriter.writeUint32(8),t.ioWriter.writeString("mfra")),t.ioWriter.flush();else{this.updateCurrentChunk(t);let e=BigInt(0);y.__(t.streams,(t=>{if(1024&t.disposition)return!0;const i=t.privData;i.sampleSizes.length&&(i.sttsSampleDeltas.length?i.lastDuration>0&&i.lastDuration!==i.sttsSampleDeltas[i.sttsSampleDeltas.length-1]?(i.sttsSampleCounts.push(1),i.sttsSampleDeltas.push(i.lastDuration)):i.sttsSampleCounts[i.sttsSampleCounts.length-1]++:(i.sttsSampleCounts=[1],i.sttsSampleDeltas=[0]));const a=(0,v.k)((0,g.A)(i),t.timeBase,{den:1e3,num:1});a>e&&(e=a)})),this.context.duration=e;const i=this.context.boxsPositionInfo[this.context.boxsPositionInfo.length-1];if("mdat"!==i.type&&B.z3("last box is not mdat",C,1053),i.size=Number(t.ioWriter.getPos()-i.pos),i.size>U.f7){const e=t.ioWriter.getPos();t.ioWriter.seek(i.pos-BigInt(8)),t.ioWriter.writeUint32(1),t.ioWriter.writeUint32((0,c.A)("mdat")),t.ioWriter.writeUint64(BigInt(i.size)+BigInt(8)),t.ioWriter.seek(e),this.context.boxsPositionInfo.pop(),this.context.use64Mdat=!0}if((0,l.A)(t.ioWriter,this.context),this.options.fastOpen){t.ioWriter.flush();let e=[];const i=t.ioWriter.onFlush;t.ioWriter.onFlush=t=>(e.push(this.eioOwned&&this.eioOwned.copy?this.eioOwned.copy(t,"mse-trailer-output"):t.slice()),0),d.dI(t.ioWriter,t,this.context),t.ioWriter.flush();let a=(0,x.A)(Uint8Array,e);y.__(t.streams,(t=>{const e=t.privData;if(e.chunkOffsets.length)for(let t=0;t<e.chunkOffsets.length;t++)e.chunkOffsets[t]+=BigInt(Math.floor(a.length))})),e=[],d.dI(t.ioWriter,t,this.context),t.ioWriter.flush(),a=(0,x.A)(Uint8Array,e),i&&i(a,this.context.holdMoovPos),t.ioWriter.onFlush=i}else d.dI(t.ioWriter,t,this.context),t.ioWriter.flush()}return 0}flush(t){return this.options.fragment&&(y.__(this.context.currentFragment.tracks,(e=>{const i=t.streams.find((t=>t.index===e.streamIndex)).privData;e.sampleCount&&(e.sampleDurations.length?e.sampleDurations.push(e.sampleDurations[e.sampleDurations.length-1]):e.sampleDurations=[i.lastDuration])})),this.updateCurrentFragment(t)),t.ioWriter.flush(),0}getCapabilities(){return M.Capabilities}}(0,a.A)(M,"Capabilities",[86021,86076,86018,86017,86028,86051,86019,86056,167,225,27,173,196,12,94226,94213])},92782:(t,e,i)=>{i.d(e,{A:()=>s});var a=i(34117);const r="packages/avformat/src/formats/isobmff/writing/stts.ts";function s(t,e,i){const s=e.privData,n=s.sttsSampleCounts||[],o=s.sttsSampleDeltas||[];n.length!==o.length&&a.R8("stts sampleCounts's length is not match sampleDeltas's length",r,39);const c=Math.min(n.length,o.length);t.writeUint32(16+8*c),t.writeString("stts"),t.writeUint8(0),t.writeUint24(0),t.writeUint32(c);for(let e=0;e<c;e++)t.writeUint32(n[e]),t.writeInt32(o[e])}},95414:(t,e,i)=>{i.d(e,{A:()=>n});var a=i(80209),r=i(25721),s=i(99817);function n(t,e,i){let n;i.fragment&&e.sideData[1]?(n=e.sideData[1],delete e.sideData[1]):e.codecpar.extradata&&(n=(0,a.s3)(e.codecpar.extradata,e.codecpar.extradataSize)),n&&s.Bs(n)&&(n=r.S1(n)),t.writeUint32(12+(n?n.length:0)),t.writeString("vvcC"),t.writeUint8(0),t.writeUint24(0),n&&t.writeBuffer(n)}},98075:(t,e,i)=>{function a(t,e,i){t.writeUint32(12),t.writeString("sthd"),t.writeUint32(0)}i.d(e,{A:()=>a})},98552:(t,e,i)=>{i.d(e,{A:()=>s});var a=i(70704),r=i(80209);function s(t,e,i){let s;if(i.fragment&&e.sideData[1]?(s=e.sideData[1],delete e.sideData[1]):e.codecpar.extradata&&(s=(0,r.s3)(e.codecpar.extradata,e.codecpar.extradataSize)),!s||s.length<19)t.writeUint32(19),t.writeString("dOps"),t.writeUint8(0),t.writeUint8(e.codecpar.chLayout.nbChannels),t.writeUint16(e.codecpar.initialPadding),t.writeUint32(e.codecpar.sampleRate),t.writeUint16(0),t.writeUint8(0);else{const e=new a.A(s,!1);t.writeUint32(s.length),t.writeString("dOps"),t.writeUint8(0),e.seek(9),t.writeUint8(e.readUint8()),t.writeUint16(e.readUint16()),t.writeUint32(e.readUint32()),t.writeUint16(e.readUint16()),t.writeBuffer(s.subarray(18))}}}}]);