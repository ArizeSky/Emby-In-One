"use strict";
/* EIO count-only mux planning. Loaded as a real webpack dependency of chunk 26
 * and collected as a real module by generated Workers; no embedded source copy. */
(self.webpackChunkAVPlayer=self.webpackChunkAVPlayer||[]).push([[90200],{
90200:(module,exports,i)=>{
const r=i(54568),f=i(46564),R=i(88560);
/* EIO-DUAL-MUX-STEP1:BEGIN count-only writer
   Side-effect-free measurement twin of module 74817 IOWriter: identical pos /
   pointer / flush window state machine, no backing store, no payload copy. */
function eioCountOnlyUtf8Length(t){
  let e=0;
  for(let i=0;i<t.length;i++){
    const a=t.charCodeAt(i);
    if(a<128)e+=1;
    else if(a<2048)e+=2;
    else if(a>=55296&&a<=56319&&i+1<t.length){
      const r=t.charCodeAt(i+1);
      if(r>=56320&&r<=57343){e+=4;i++}else e+=3
    }else e+=3
  }
  return e
}
class EioMuxCountOnlyWriter{
  constructor(t){
    const e=void 0===t?1048576:t;
    if(!Number.isSafeInteger(e)||e<8){
      const i=new RangeError("EIO_MUX_COUNT_ONLY_SIZE");
      throw i.code="EIO_MUX_COUNT_ONLY_SIZE",i
    }
    this.size=e;
    this.pointer=0;
    this.pos=BigInt(0);
    this.littleEndian=!1;
    this.error=0;
    this.eioFlushSlices=null
  }
  eioRecordSlice(t){
    t>0&&Array.isArray(this.eioFlushSlices)&&this.eioFlushSlices.push(t)
  }
  eioAdvance(t){
    if(!(t>0))return;
    let e=t;
    if(this.remainingLength()<e){
      for(;e>0;){
        this.eioRecordSlice(this.pointer);
        this.pointer=0;
        const i=Math.min(this.size,e);
        this.pointer+=i;
        this.pos+=BigInt(i);
        e-=i
      }
    }else{
      this.pointer+=e;
      this.pos+=BigInt(e)
    }
  }
  eioNeed(t){
    if(this.remainingLength()<t){
      this.eioRecordSlice(this.pointer);
      this.pointer=0
    }
  }
  writeUint8(t){this.eioNeed(1);this.pointer+=1;this.pos+=BigInt(1)}
  writeUint16(t){this.eioNeed(2);this.pointer+=2;this.pos+=BigInt(2)}
  writeUint24(t){
    this.writeUint8(t>>>16);
    this.writeUint8(t>>>8);
    this.writeUint8(t)
  }
  writeUint32(t){this.eioNeed(4);this.pointer+=4;this.pos+=BigInt(4)}
  writeUint64(t){this.eioNeed(8);this.pointer+=8;this.pos+=BigInt(8)}
  writeInt8(t){this.eioNeed(1);this.pointer+=1;this.pos+=BigInt(1)}
  writeInt16(t){this.eioNeed(2);this.pointer+=2;this.pos+=BigInt(2)}
  writeInt24(t){this.writeUint24(t)}
  writeInt32(t){this.eioNeed(4);this.pointer+=4;this.pos+=BigInt(4)}
  writeInt64(t){this.eioNeed(8);this.pointer+=8;this.pos+=BigInt(8)}
  writeFloat(t){this.eioNeed(4);this.pointer+=4;this.pos+=BigInt(4)}
  writeDouble(t){this.eioNeed(8);this.pointer+=8;this.pos+=BigInt(8)}
  writeBuffer(t){
    if(!t||!t.length)return;
    this.eioAdvance(t.length)
  }
  writeString(t){
    const e=eioCountOnlyUtf8Length(String(t));
    this.eioAdvance(e);
    return e
  }
  getPointer(){return this.pointer}
  getPos(){return this.pos}
  remainingLength(){return this.size-this.pointer}
  getBufferSize(){return this.size}
  setEndian(t){this.littleEndian=!t}
  flush(){
    this.eioRecordSlice(this.pointer);
    this.pointer=0
  }
  flushToPos(t){this.pointer=0}
  seek(t){
    this.flush();
    this.pos=t
  }
  seekInline(t){
    const e=this.pointer;
    this.pointer=Math.max(0,Math.min(this.size,t));
    this.pos+=BigInt(this.pointer-e)
  }
  skip(t){
    const e=this.pointer;
    this.pointer=Math.min(this.size,this.pointer+t);
    this.pos+=BigInt(this.pointer-e)
  }
  back(t){
    const e=this.pointer;
    this.pointer=Math.max(0,this.pointer-t);
    this.pos+=BigInt(this.pointer-e)
  }
  reset(){
    this.pointer=0;
    this.pos=BigInt(0);
    this.error=0
  }
  getBuffer(){
    const t=new Error("EIO_MUX_COUNT_ONLY_NO_BUFFER");
    t.code="EIO_MUX_COUNT_ONLY_NO_BUFFER";
    throw t
  }
}
/* EIO-DUAL-MUX-STEP1:END count-only writer */
/* EIO-DUAL-MUX-STEP2:BEGIN mux plan helpers (copied from proven B surfaces) */
/* The plan codec table mirrors the muxer's declared Capabilities (module
   90197/90198): every codec the muxer can write is plannable, because the
   plan measures the real box writers through the count-only writer factory.
   codecType must match the stream's codecpar.codecType (0 video, 1 audio,
   3 subtitle). A codec outside this set is refused with codec-unsupported. */
const EIO_PLAN_CODECS={
  27:{label:"h264-avcc",codecType:0},
  173:{label:"hevc-hvc1",codecType:0},
  196:{label:"vvc-vvc1",codecType:0},
  12:{label:"mpeg4-mp4v",codecType:0},
  167:{label:"vp9-vp09",codecType:0},
  225:{label:"av1-av01",codecType:0},
  86018:{label:"aac",codecType:1},
  86017:{label:"mp3",codecType:1},
  86028:{label:"flac",codecType:1},
  86076:{label:"opus",codecType:1},
  86021:{label:"vorbis",codecType:1},
  86051:{label:"speex",codecType:1},
  86019:{label:"ac-3",codecType:1},
  86056:{label:"ec-3",codecType:1},
  94226:{label:"wvtt",codecType:3},
  94213:{label:"tx3g",codecType:3}
};
const EIO_PLAN_METADATA_MAX_ENTRIES=1<<16;
const EIO_PLAN_MAX_FLUSHES=1<<16;
const EIO_PLAN_BOX_BASE_BYTES=4096;
const EIO_PLAN_BOX_BYTES_PER_STREAM=8192;
const EIO_PLAN_BOX_BYTES_PER_SAMPLE=256;
const EIO_PLAN_ASSUMPTIONS=Object.freeze([
  "moof/moov/mfra bytes are measured by the real box writers",
  "worksetBytes sums only media-bearing allocation sites; JS metadata clones are entry-capped, not a media byte budget",
  "metadata entry and flush work bounds are anti-hang guards, not byte reserves",
  "eioPlanWrite covers the per-packet track writer copies of a preroll batch"
]);
/* Stable refusal diagnostics. A plan-overflow detail carries the guard site and
   only the immediate scalar operands of that guard, so a refusal names the exact
   comparison instead of an inferred generated stack frame. Values degrade to
   plain scalars: null, boolean, short label, finite number, decimal BigInt
   string; NaN/infinity keep that distinction as a number label; any other shape
   degrades to its typeof. No buffers, pointers, payloads or shadow state. */
function eioPlanDiagnosticScalar(t){
  try{
    if(null===t)return null;
    const e=typeof t;
    if("number"===e){
      if(Number.isFinite(t))return t;
      return Number.isNaN(t)?"number:nan":t>0?"number:infinity":"number:-infinity"
    }
    if("boolean"===e)return t;
    if("string"===e)return t.length>64?t.slice(0,64):t;
    if("bigint"===e)return t.toString();
    if("undefined"===e)return"undefined";
    return e
  }catch(err){return"unreadable"}
}
function eioPlanOverflowDetail(t,e){
  const i={site:"string"==typeof t?t:"unknown-site"};
  if(e&&"object"==typeof e){
    let a;
    try{a=Object.keys(e)}catch(err){a=[]}
    for(let r=0;r<a.length;r++){
      const n=a[r];
      if("site"===n)continue;
      try{i[n]=eioPlanDiagnosticScalar(e[n])}catch(err){i[n]="unreadable"}
    }
  }
  return Object.freeze(i)
}
function eioPlanUnsupported(t,e){
  return Object.freeze({
    supported:!1,
    reason:t,
    detail:void 0===e?null:e,
    kind:null,
    sites:Object.freeze([]),
    tracks:Object.freeze([]),
    mux:null,
    fragment:null,
    queue:null,
    bsf:null,
    fixedExtradataBytes:0,
    residentBefore:0,
    residentAfter:0,
    worksetBytes:0,
    peakBytes:0,
    metadataEntries:0,
    metadataEntryLimit:EIO_PLAN_METADATA_MAX_ENTRIES,
    assumptions:EIO_PLAN_ASSUMPTIONS,
    unmeasured:Object.freeze([])
  })
}
function eioPlanSite(t,e,i,a){
  return Object.freeze({name:t,bytes:e,allocationBytes:i,note:void 0===a?null:a})
}
function eioPlanIsCount(t){
  return"number"==typeof t&&Number.isSafeInteger(t)&&t>=0
}
function eioPlanSum(t){
  if(!t)return 0;
  let e=0;
  for(let i=0;i<t.length;i++){
    if(!eioPlanIsCount(t[i]))return-1;
    if(e>Number.MAX_SAFE_INTEGER-t[i])return-1;
    e+=t[i]
  }
  return e
}
function eioPlanAdd(t,e){
  if(!eioPlanIsCount(t)||!eioPlanIsCount(e))return-1;
  return t>Number.MAX_SAFE_INTEGER-e?-1:t+e
}
function eioPlanMul(t,e){
  if(!eioPlanIsCount(t)||!eioPlanIsCount(e)||e<=0)return-1;
  return t>Math.floor(Number.MAX_SAFE_INTEGER/e)?-1:t*e
}
function eioPlanFlushBounded(t,e){
  if(!eioPlanIsCount(t)||!eioPlanIsCount(e)||e<=0)return!1;
  return Math.floor(t/e)<EIO_PLAN_MAX_FLUSHES
}
function eioPlanTrackLabel(t,e){
  if(!Object.prototype.hasOwnProperty.call(EIO_PLAN_CODECS,t))return null;
  const i=EIO_PLAN_CODECS[t];
  if(i.codecType!==e)return null;
  return i.label
}
function eioPlanIsCountOnlyWriter(t){
  return!!(t&&"object"==typeof t&&
    "function"==typeof t.getPos&&
    "function"==typeof t.getPointer&&
    "function"==typeof t.getBufferSize&&
    "function"==typeof t.reset&&
    "function"==typeof t.flush&&
    "function"==typeof t.seek&&
    "function"==typeof t.seekInline&&
    "function"==typeof t.setEndian&&
    "function"==typeof t.writeUint8&&
    "function"==typeof t.writeUint32&&
    "function"==typeof t.writeUint64&&
    "function"==typeof t.writeString&&
    "function"==typeof t.writeBuffer&&
    void 0===t.buffer&&void 0===t.data)
}
function eioPlanNewWriter(t,e){
  if("function"!=typeof t)return null;
  let i=null;
  try{i=t(e)}catch(err){return null}
  if(!eioPlanIsCountOnlyWriter(i))return null;
  i.eioFlushSlices=[];
  return i
}
function eioPlanSeedWriter(t,e,i){
  t.reset();
  t.seek(BigInt(e-i));
  if(i>0)t.writeBuffer({length:i})
}
function eioPlanShadowRational(t){
  if(!t||"object"!=typeof t)return void 0;
  return{num:t.num,den:t.den}
}
function eioPlanShadowCodecpar(t){
  const e=t||{},i=e.chLayout||{};
  return{
    codecId:e.codecId,
    codecType:e.codecType,
    codecTag:e.codecTag,
    flags:e.flags,
    format:e.format,
    profile:e.profile,
    level:e.level,
    width:e.width,
    height:e.height,
    sampleAspectRatio:eioPlanShadowRational(e.sampleAspectRatio),
    colorRange:e.colorRange,
    colorPrimaries:e.colorPrimaries,
    colorTrc:e.colorTrc,
    colorSpace:e.colorSpace,
    frameSize:e.frameSize,
    sampleRate:e.sampleRate,
    framerate:e.framerate,
    [r.o9]:e[r.o9],
    extradata:e.extradata,
    extradataSize:e.extradataSize,
    bitsPerCodedSample:e.bitsPerCodedSample,
    bitsPerRawSample:e.bitsPerRawSample,
    initialPadding:e.initialPadding,
    chLayout:{nbChannels:i.nbChannels}
  }
}
function eioPlanShadowCenc(t){
  if(!t)return null;
  return{
    sampleCount:t.sampleCount,
    defaultSampleInfoSize:t.defaultSampleInfoSize,
    useSubsamples:t.useSubsamples,
    offset:t.offset,
    offsetPos:t.offsetPos,
    sampleSizes:(t.sampleSizes||[]).slice(),
    sampleInfoOffset:(t.sampleInfoOffset||[]).slice(),
    sampleEncryption:(t.sampleEncryption||[]).slice()
  }
}
function eioPlanShadowTrack(t,e,i){
  return{
    trackId:t.trackId,
    streamIndex:t.streamIndex,
    windowBytes:i,
    sampleCount:t.sampleCount||0,
    sampleSizes:(t.sampleSizes||[]).slice(),
    sampleDurations:(t.sampleDurations||[]).slice(),
    sampleFlags:(t.sampleFlags||[]).slice(),
    sampleCompositionTimeOffset:(t.sampleCompositionTimeOffset||[]).slice(),
    firstSampleFlags:t.firstSampleFlags,
    defaultSampleDuration:t.defaultSampleDuration,
    defaultSampleSize:t.defaultSampleSize,
    defaultSampleFlags:t.defaultSampleFlags,
    baseIsMoof:t.baseIsMoof,
    baseDataOffset:t.baseDataOffset,
    baseMediaDecodeTime:t.baseMediaDecodeTime,
    dataOffset:t.dataOffset,
    dataOffsetPos:t.dataOffsetPos,
    lastFragIndexDts:t.lastFragIndexDts,
    cenc:eioPlanShadowCenc(t.cenc),
    buffers:(t.sampleCount||0)>0?[{length:e}]:[],
    ioWriter:null
  }
}
function eioPlanShadowStream(t){
  const e=t.privData||{};
  const i=Object.assign({},e);
  i.fragIndexes=e.fragIndexes?e.fragIndexes.slice():[];
  return{
    index:t.index,
    disposition:t.disposition,
    attachedPic:t.attachedPic,
    metadata:t.metadata||{},
    codecpar:eioPlanShadowCodecpar(t.codecpar),
    timeBase:{num:t.timeBase&&t.timeBase.num,den:t.timeBase&&t.timeBase.den},
    sideData:t.sideData?(Array.isArray(t.sideData)?t.sideData.slice():Object.assign({},t.sideData)):[],
    privData:i
  }
}
function eioPlanScanPssh(t){
  for(let e=0;e<t.streams.length;e++){
    try{
      const cp=t.streams[e]&&t.streams[e].codecpar;
      const ptr=cp&&cp[r.o9];
      if(ptr&&((0,f.Un)(ptr+20,ptr+24,24)))return{ok:!1,reason:"header-pssh-present-unmeasured",detail:e};
      const coded=cp&&cp.codedSideData;
      if(coded&&((Array.isArray(coded)&&coded.some(x=>x&&x.type===24))||(!Array.isArray(coded)&&coded[24])))return{ok:!1,reason:"header-pssh-present-unmeasured",detail:e}
    }catch(err){return{ok:!1,reason:"header-pssh-scan-unmeasured",detail:e}}
  }
  return{ok:!0}
}
function eioPlanExtradataLength(cp){
  const data=cp&&cp.extradata;
  if(!data)return 0;
  const size=typeof data==="number"?cp.extradataSize:data.length;
  return eioPlanIsCount(size)?size:-1
}
function eioPlanHeaderExtradataBytes(t){
  let e=0;
  for(let i=0;i<t.streams.length;i++){
    const n=t.streams[i]||{},s=n.codecpar||{};
    let o,c;
    try{
      o=n.sideData&&n.sideData[1]&&n.sideData[1].length;
      c=eioPlanExtradataLength(s)
    }catch(err){return-1}
    const d=[c,o];
    for(let u=0;u<d.length;u++){
      if(void 0===d[u]||null===d[u]||!1===d[u])continue;
      if(!eioPlanIsCount(d[u]))return-1;
      e=eioPlanAdd(e,d[u]);
      if(e<0)return-1
    }
  }
  return e
}
/* EIO-DUAL-MUX-STEP2:END mux plan helpers */
/* The shadow mux instance is created from the real format's options but owns a
   count-only writer factory, so a plan never allocates payload or windows. */
function eioPlanCreateShadow(t,e){
  const i=Object.assign({},t&&t.options||{});
  delete i.eioWriterFactory;
  const n=new R.A(i);
  /* Private planning-only marker: this shadow never owns runtime media. */
  n.eioCountOnlyPlan=!0;
  n.eioOwned={writerFactory:e};
  return n
}
/* One measured emit on a real or already-shadowed format state. */
function eioPlanMux(t,e,i){
  const a=t&&t.options||{};
  if(!i||"object"!=typeof i)return eioPlanUnsupported("request-invalid");
  if(!a.fragment)return eioPlanUnsupported("fragment-mode-required");
  if(a.onFragmentStart)return eioPlanUnsupported("onFragmentStart-unmeasured");
  if("trailer"===e&&a.hasTfra)return eioPlanUnsupported("trailer-mfra-unmeasured");
  if(i.annexb)return eioPlanUnsupported("annexb-conversion-unmeasured");
  const r=i.eioState||null;
  const s=r?r.host:i.host;
  if(!s||"object"!=typeof s||!Array.isArray(s.streams))return eioPlanUnsupported("host-invalid");
  if(!eioPlanIsCount(s.streams.length)||s.streams.length>EIO_PLAN_METADATA_MAX_ENTRIES)return eioPlanUnsupported("metadata-entries-exceeded",s.streams.length);
  const n=r?r.context:i.context;
  if(!n||"object"!=typeof n)return eioPlanUnsupported("context-invalid");
  let o=r?r.writer:null,d=0;
  if(o)d=o.eioFlushSlices.length;
  else{
    const m=s.ioWriter;
    if(!m||"function"!=typeof m.getBufferSize||"function"!=typeof m.getPointer||"function"!=typeof m.getPos)return eioPlanUnsupported("mux-writer-invalid");
    const g=m.getBufferSize(),k=m.getPointer(),w=Number(m.getPos());
    if(!eioPlanIsCount(g)||g<=0)return eioPlanUnsupported("mux-writer-state-invalid");
    if(!eioPlanIsCount(k)||k>g)return eioPlanUnsupported("mux-writer-state-invalid");
    if(!eioPlanIsCount(w)||w<k)return eioPlanUnsupported("mux-writer-state-invalid");
    o=eioPlanNewWriter(i.writerFactory,g);
    if(!o)return eioPlanUnsupported("needs-count-only-writer");
    eioPlanSeedWriter(o,w,k);
    if(o.eioFlushSlices.length!==0)return eioPlanUnsupported("plan-seed-flushed");
    if(Number(o.getPos())!==w||o.getPointer()!==k)return eioPlanUnsupported("plan-seed-mismatch")
  }
  const muxWindowBytes=o.getBufferSize();
  let muxPointerBytes=o.getPointer();
  const muxPosBytes=Number(o.getPos());
  if(!eioPlanIsCount(muxWindowBytes)||muxWindowBytes<=0)return eioPlanUnsupported("mux-writer-state-invalid");
  if(!eioPlanIsCount(muxPointerBytes)||muxPointerBytes>muxWindowBytes)return eioPlanUnsupported("mux-writer-state-invalid");
  if(!eioPlanIsCount(muxPosBytes)||muxPosBytes<muxPointerBytes)return eioPlanUnsupported("mux-writer-state-invalid");
  const u=i.queuedChunkBytes||[];
  if(!eioPlanIsCount(u.length)||u.length>EIO_PLAN_METADATA_MAX_ENTRIES)return eioPlanUnsupported("metadata-entries-exceeded",u.length);
  const queuedBytes=eioPlanSum(u);
  if(queuedBytes<0)return eioPlanUnsupported("queue-state-invalid");
  if(void 0!==i.bsfResidentBytes&&!eioPlanIsCount(i.bsfResidentBytes))return eioPlanUnsupported("bsf-resident-invalid");
  const bsfUnknown=!0===i.bsfResidentUnknown&&void 0===i.bsfResidentBytes;
  if(!bsfUnknown&&t.annexb2AvccFilter&&void 0===i.bsfResidentBytes)return eioPlanUnsupported("bsf-resident-unmeasured");
  /* Missing BSF residency stays null and is omitted from the informational
     resident totals; it is never fabricated as a zero-byte measurement. */
  const bsfKnown=!bsfUnknown&&eioPlanIsCount(i.bsfResidentBytes);
  const bsfResidentBytes=bsfKnown?i.bsfResidentBytes:null;
  const f="header"===e,g="trailer"===e;
  if(f&&void 0!==i.runtimeInitCodecparBytes&&!eioPlanIsCount(i.runtimeInitCodecparBytes))return eioPlanUnsupported("init-codecpar-shape-invalid");
  let k=0;
  if(f){
    k=eioPlanHeaderExtradataBytes(s);
    if(k<0)return eioPlanUnsupported("plan-overflow",eioPlanOverflowDetail("mux.header-extradata",{streams:s.streams.length,fixedExtradataBytes:k}))
  }
  if(f){
    const w=i.extradataKind||{};
    for(let e=0;e<s.streams.length;e++){
      const i=s.streams[e],r=i.codecpar||{};
      if(1024&(i.disposition||0))return eioPlanUnsupported("header-attached-picture-unsupported",i.index);
      if(!eioPlanTrackLabel(r.codecId,r.codecType))return eioPlanUnsupported("codec-unsupported",r.codecId);
      if("annexb"===w[i.index])return eioPlanUnsupported("annexb-extradata-conversion",i.index);
      if(27===r.codecId&&"avcc"!==w[i.index])return eioPlanUnsupported("extradata-kind-unknown",i.index);
      if(i.metadata&&i.metadata.title)return eioPlanUnsupported("header-title-encode-unmeasured",i.index);
      if(i.metadata&&"string"==typeof i.metadata.compressorName&&i.metadata.compressorName.length>0)return eioPlanUnsupported("header-compressor-name-encode-unmeasured",i.index);
      if(i.metadata&&i.metadata.encryption)return eioPlanUnsupported("header-encrypted-stsd-unmeasured",i.index)
    }
    if(n.chapters&&n.chapters.length)return eioPlanUnsupported("header-chapters-unmeasured");
    const b=eioPlanScanPssh(s);
    if(!b.ok)return eioPlanUnsupported(b.reason,b.detail);
    if(a.encryption)return eioPlanUnsupported("header-encrypted-stsd-unmeasured","options.encryption");
    const v=Object.keys(n.cencs||{});
    if(v.length)return eioPlanUnsupported("header-encrypted-stsd-unmeasured",v[0]);
    const U=n.metadata||{},I=Object.keys(U);
    for(let t=0;t<I.length;t++){
      if("encoder"===I[t].toLowerCase())continue;
      if("string"==typeof U[I[t]])return eioPlanUnsupported("header-metadata-encode-unmeasured",I[t])
    }
  }
  if(f&&!bsfUnknown&&void 0===i.bsfResidentBytes)return eioPlanUnsupported("bsf-resident-unmeasured");
  const P=n.currentFragment;
  if(!f&&(!P||!Array.isArray(P.tracks)))return eioPlanUnsupported("fragment-state-invalid");
  const y=P&&Array.isArray(P.tracks)?P.tracks:[];
  if(!eioPlanIsCount(y.length)||y.length>EIO_PLAN_METADATA_MAX_ENTRIES)return eioPlanUnsupported("metadata-entries-exceeded",y.length);
  const S=!f&&!!P.firstWrote,T=[],C=[];
  let D=0,A=0,M=0,E=0,z=0;
  let F=s.streams.length;
  if(!eioPlanIsCount(F)||F>EIO_PLAN_METADATA_MAX_ENTRIES)return eioPlanUnsupported("metadata-entries-exceeded",F);
  for(let t=0;t<s.streams.length;t++){
    const e=s.streams[t],i=e.sideData;
    if(i&&i.length){
      F=eioPlanAdd(F,i.length);
      if(F<0||F>EIO_PLAN_METADATA_MAX_ENTRIES)return eioPlanUnsupported("metadata-entries-exceeded",F)
    }
    const a=e.privData&&e.privData.fragIndexes;
    if(a&&a.length){
      F=eioPlanAdd(F,a.length);
      if(F<0||F>EIO_PLAN_METADATA_MAX_ENTRIES)return eioPlanUnsupported("metadata-entries-exceeded",F)
    }
  }
  F=eioPlanAdd(F,(n.trexs||[]).length);
  if(F<0||F>EIO_PLAN_METADATA_MAX_ENTRIES)return eioPlanUnsupported("metadata-entries-exceeded",F);
  if(!f){
    for(let t=0;t<y.length;t++){
      const e=y[t];
      let i=null;
      for(let t=0;t<s.streams.length;t++)if(s.streams[t].index===e.streamIndex)i=s.streams[t];
      if(!i)return eioPlanUnsupported("track-stream-missing",e.streamIndex);
      if(1024&(i.disposition||0))continue;
      const a=i.codecpar||{},r=eioPlanTrackLabel(a.codecId,a.codecType);
      if(!r)return eioPlanUnsupported("codec-unsupported",a.codecId);
      const o=e.ioWriter;
      if(!o||"function"!=typeof o.getBufferSize||"function"!=typeof o.getPointer)return eioPlanUnsupported("track-writer-invalid",e.trackId);
      const d=o.getBufferSize(),u=o.getPointer();
      if(!eioPlanIsCount(d)||d<=0||!eioPlanIsCount(u)||u>d)return eioPlanUnsupported("track-writer-state-invalid",e.trackId);
      z=eioPlanAdd(z,d);
      if(z<0)return eioPlanUnsupported("plan-overflow",eioPlanOverflowDetail("mux.track-window-sum",{trackId:e.trackId,windowBytes:d,trackWindowBytes:z}));
      const g=e.buffers||[],k=[];
      for(let t=0;t<g.length;t++){
        if(!g[t]||!eioPlanIsCount(g[t].length))return eioPlanUnsupported("track-buffers-invalid",e.trackId);
        k.push(g[t].length)
      }
      const w=eioPlanSum(k);
      if(w<0)return eioPlanUnsupported("track-buffers-invalid",e.trackId);
      const b=e.sampleCount||0,A2=e.sampleSizes||[],M2=eioPlanSum(A2);
      if(M2<0)return eioPlanUnsupported("track-sample-sizes-invalid",e.trackId);
      const E2=e.sampleDurations||[],V=eioPlanSum([1,g.length,A2.length,E2.length,(e.sampleFlags||[]).length,(e.sampleCompositionTimeOffset||[]).length]);
      if(V<0)return eioPlanUnsupported("metadata-entries-exceeded",e.trackId);
      F=eioPlanAdd(F,V);
      if(F<0||F>EIO_PLAN_METADATA_MAX_ENTRIES)return eioPlanUnsupported("metadata-entries-exceeded",e.trackId);
      if(e.cenc){
        const t=e.cenc.sampleEncryption||[];
        let i=eioPlanSum([1,(e.cenc.sampleSizes||[]).length,(e.cenc.sampleInfoOffset||[]).length,t.length]);
        if(i<0)return eioPlanUnsupported("metadata-entries-exceeded",e.trackId);
        F=eioPlanAdd(F,i);
        if(F<0||F>EIO_PLAN_METADATA_MAX_ENTRIES)return eioPlanUnsupported("metadata-entries-exceeded",e.trackId);
        for(let i=0;i<t.length;i++){
          const n=t[i]&&t[i].subsamples;
          if(n&&n.length){
            F=eioPlanAdd(F,n.length);
            if(F<0||F>EIO_PLAN_METADATA_MAX_ENTRIES)return eioPlanUnsupported("metadata-entries-exceeded",e.trackId)
          }
        }
      }
      if(0===b){
        if(S&&(w>0||u>0||A2.length>0))return eioPlanUnsupported("inactive-track-pending-bytes",e.trackId);
        T.push(Object.freeze({trackId:e.trackId,streamIndex:e.streamIndex,codec:r,active:!1,sampleCount:0,payloadBytes:0,sideDataBytes:0,concatBytes:0,chunkCount:0,flushSliceBytes:0,windowBytes:d}));
        C.push(r?eioPlanShadowTrack(e,0,d):null);
        continue
      }
      const q=eioPlanAdd(w,u);
      if(q<0)return eioPlanUnsupported("plan-overflow",eioPlanOverflowDetail("mux.track-payload-sum",{trackId:e.trackId,bufferBytes:w,pointerBytes:u,payloadBytes:q}));
      if(S){
        if(M2!==q)return eioPlanUnsupported("track-payload-invariant",{trackId:e.trackId,sampleSizes:M2,payloadBytes:q});
        if(q<=0)return eioPlanUnsupported("zero-payload-track",e.trackId);
        if(E2.length!==b&&E2.length!==b-1)return eioPlanUnsupported("durations-incomplete",e.trackId)
      }
      const Y=S?q:0,W=S?k.length+(u>0?1:0):0,j=W>=2?Y:0,X=S?u:0;
      D=eioPlanAdd(D,Y);A=eioPlanAdd(A,S?w:0);M=eioPlanAdd(M,X);E=eioPlanAdd(E,j);
      if(D<0||A<0||M<0||E<0)return eioPlanUnsupported("plan-overflow",eioPlanOverflowDetail("mux.track-bytes-sum",{trackId:e.trackId,mdatPayloadBytes:D,sideDataBytes:A,flushSliceBytes:M,concatBytes:E}));
      T.push(Object.freeze({trackId:e.trackId,streamIndex:e.streamIndex,codec:r,active:!0,sampleCount:b,payloadBytes:Y,sideDataBytes:0,concatBytes:j,chunkCount:W,flushSliceBytes:X,windowBytes:d}));
      C.push(eioPlanShadowTrack(e,Y,d))
    }
  }
  const N=eioPlanMul(s.streams.length,EIO_PLAN_BOX_BYTES_PER_STREAM);
  if(N<0)return eioPlanUnsupported("plan-overflow",eioPlanOverflowDetail("mux.box-bound-streams",{streams:s.streams.length,boxBoundStreamBytes:N}));
  let Q=eioPlanAdd(eioPlanAdd(muxPointerBytes,EIO_PLAN_BOX_BASE_BYTES),N);
  if(Q<0)return eioPlanUnsupported("plan-overflow",eioPlanOverflowDetail("mux.box-bound-total",{pointerBefore:muxPointerBytes,boxBaseBytes:EIO_PLAN_BOX_BASE_BYTES,boxBoundStreamBytes:N,boxBoundBytes:Q}));
  if(!f){
    let t=0;
    for(let e=0;e<y.length;e++){
      const i=y[e].sampleCount||0;
      if(!eioPlanIsCount(i))return eioPlanUnsupported("plan-overflow",eioPlanOverflowDetail("mux.box-bound-sample-count",{trackId:y[e].trackId,index:e,sampleCount:i}));
      t=eioPlanAdd(t,i);
      if(t<0)return eioPlanUnsupported("plan-overflow",eioPlanOverflowDetail("mux.box-bound-sample-sum",{trackId:y[e].trackId,sampleCount:i,sampleCountTotal:t}))
    }
    const i=eioPlanMul(t,EIO_PLAN_BOX_BYTES_PER_SAMPLE);
    if(i<0)return eioPlanUnsupported("plan-overflow",eioPlanOverflowDetail("mux.box-bound-samples",{sampleCountTotal:t,boxBoundSampleBytes:i}));
    const n=eioPlanMul(F,8);
    if(n<0)return eioPlanUnsupported("plan-overflow",eioPlanOverflowDetail("mux.box-bound-metadata",{metadataEntries:F,boxBoundMetadataBytes:n}));
    Q=eioPlanAdd(Q,eioPlanAdd(D,eioPlanAdd(i,n)));
    if(g)Q=eioPlanAdd(Q,8);
    if(Q<0)return eioPlanUnsupported("plan-overflow",eioPlanOverflowDetail("mux.box-bound-total-fragment",{boxBoundBytes:Q,mdatPayloadBytes:D,boxBoundSampleBytes:i,boxBoundMetadataBytes:n,mfraBytes:g?8:0}))
  }else{
    for(let t=0;t<s.streams.length;t++){
      const e=s.streams[t],i=e.codecpar||{},n=[eioPlanExtradataLength(i),e.sideData&&e.sideData[1]&&e.sideData[1].length];
      for(let e=0;e<n.length;e++){
        if(void 0===n[e])continue;
        if(!eioPlanIsCount(n[e]))return eioPlanUnsupported("metadata-entries-exceeded",s.streams[t].index);
        Q=eioPlanAdd(Q,n[e]);
        if(Q<0)return eioPlanUnsupported("plan-overflow",eioPlanOverflowDetail("mux.box-bound-total-header",{streamIndex:s.streams[t].index,extradataField:e,extradataBytes:n[e],boxBoundBytes:Q}))
      }
    }
  }
  if(!eioPlanFlushBounded(Q,muxWindowBytes))return eioPlanUnsupported("plan-work-exceeded",{writeBoundBytes:Q,windowBytes:muxWindowBytes,maxFlushes:EIO_PLAN_MAX_FLUSHES});
  let O,aa,na,sa,oa;
  if(r){O=r.shadow;aa=r.streams;na=r.context;sa=r.host
  }else{
    O=eioPlanCreateShadow(t,i.writerFactory);
    aa=[];
    try{for(let t=0;t<s.streams.length;t++)aa.push(eioPlanShadowStream(s.streams[t]))}catch(err){return eioPlanUnsupported("codecpar-getter-unmeasured",String(err&&err.message||err))}
    const ha={},da=n.cencs||{},ua=Object.keys(da);
    for(let t=0;t<ua.length;t++)ha[ua[t]]=Object.assign({},da[ua[t]]);
    na=Object.assign({},n,{boxsPositionInfo:[],cencs:ha,trexs:(n.trexs||[]).slice(),use64Mdat:!1,ac3Info:void 0,currentChunk:void 0,metadata:n.metadata||s.metadata,chapters:void 0===n.chapters?s.chapters:n.chapters});
    sa={ioWriter:o,streams:aa,metadata:na.metadata,chapters:na.chapters,getStreamByIndex:function(t){for(let e=0;e<aa.length;e++)if(aa[e].index===t)return aa[e];return null},getStreamByMediaType:function(t){for(let e=0;e<aa.length;e++){const i=aa[e];if(i.codecpar.codecType===t&&!(1024&(i.disposition||0)))return i}return null}}
  }
  sa.ioWriter=o;
  if(!f){
    for(let t=0;t<C.length;t++)C[t].ioWriter=eioPlanNewWriter(i.writerFactory,C[t].windowBytes||muxWindowBytes);
    na.currentFragment={pos:BigInt(0),currentTrack:null,sequence:P.sequence,size:P.size,tracks:C,firstWrote:!!P.firstWrote}
  }
  O.context=na;
  try{
    if(f)O.writeHeader(sa);
    else if(g)O.writeTrailer(sa);
    else if(void 0===i.dts)O.updateCurrentFragment(sa);
    else O.updateCurrentFragment(sa,i.dts)
  }catch(err){
    return eioPlanUnsupported("shadow-run-failed",String(err&&err.message||err))
  }
  const ca=o.eioFlushSlices.slice(d),la=eioPlanSum(ca);
  if(la<0)return eioPlanUnsupported("plan-overflow",eioPlanOverflowDetail("mux.output-slices",{sliceCount:ca.length,sliceBytes:la,pointerBefore:muxPointerBytes,positionBefore:muxPosBytes}));
  const pa=Number(o.getPos());
  if(!eioPlanIsCount(pa))return eioPlanUnsupported("plan-overflow",eioPlanOverflowDetail("mux.output-position",{position:pa,pointerBefore:muxPointerBytes,positionBefore:muxPosBytes}));
  const fa=pa-muxPosBytes;
  if(!eioPlanIsCount(fa))return eioPlanUnsupported("plan-overflow",eioPlanOverflowDetail("mux.output-delta",{position:pa,positionBefore:muxPosBytes,deltaBytes:fa}));
  const ga=o.getPointer();
  if(!eioPlanIsCount(ga))return eioPlanUnsupported("plan-overflow",eioPlanOverflowDetail("mux.output-pointer",{position:pa,pointerAfter:ga}));
  const ba=eioPlanAdd(muxPointerBytes,fa);
  if(ba<0)return eioPlanUnsupported("plan-overflow",eioPlanOverflowDetail("mux.output-pointer-bound",{pointerBefore:muxPointerBytes,deltaBytes:fa,pointerBoundBytes:ba}));
  const va=ba-ga;
  if(va<0||va>la)return eioPlanUnsupported("queue-flush-mismatch",{pointerBefore:muxPointerBytes,emitted:fa,pointerAfter:ga,sliceBytes:la});
  const Ua=la-va;
  const Ia=(f||!S)?0:Number(na.currentFragment.size||0);
  if(!eioPlanIsCount(Ia))return eioPlanUnsupported("plan-overflow",eioPlanOverflowDetail("mux.fragment-size",{moofBytes:Ia,fragmentEmitted:S}));
  const Pa=S?eioPlanAdd(D,8):0;
  if(Pa<0)return eioPlanUnsupported("plan-overflow",eioPlanOverflowDetail("mux.mdat-header",{mdatPayloadBytes:D,mdatHeaderBytes:Pa}));
  const ya=g?8:0;
  const Sa=f?fa:eioPlanSum([Ia,Pa,ya]);
  if(Sa<0)return eioPlanUnsupported("plan-overflow",eioPlanOverflowDetail("mux.fragment-total",{moofBytes:Ia,mdatHeaderBytes:Pa,mfraBytes:ya,fragmentBytes:Sa}));
  if(!f&&Sa!==fa)return eioPlanUnsupported("shadow-byte-mismatch",{planned:Sa,emitted:fa});
  let Aa=0;
  if(f){
    const t=na.currentFragment;
    if(t&&Array.isArray(t.tracks)){
      for(let e=0;e<t.tracks.length;e++){
        const i=t.tracks[e].ioWriter;
        if(i&&"function"==typeof i.getBufferSize){
          const n=i.getBufferSize();
          if(!eioPlanIsCount(n)||n<=0)return eioPlanUnsupported("plan-overflow",eioPlanOverflowDetail("mux.header-track-window",{trackIndex:e,windowBytes:n}));
          Aa=eioPlanAdd(Aa,n);
          if(Aa<0)return eioPlanUnsupported("plan-overflow",eioPlanOverflowDetail("mux.header-track-window-sum",{trackIndex:e,windowBytes:n,trackWindowBytes:Aa}))
        }
      }
    }
  }
  const Ma=eioPlanAdd(queuedBytes,va);
  if(Ma<0)return eioPlanUnsupported("plan-overflow",eioPlanOverflowDetail("mux.queue-total",{queueBefore:queuedBytes,appendBytes:va,queueAfter:Ma}));
  const Ea=u.length+ca.length;
  const za=Ea>=2?Ma:0;
  const Fa=[];
  if(f){
    Fa.push(eioPlanSite("header-write",fa,0,"ftyp+moov bytes written into the resident mux window"));
    Fa.push(eioPlanSite("header-track-window",Aa,Aa,"per-fragment-track IOWriter window created by writeHeader"));
    Fa.push(eioPlanSite("header-extradata",k,0,"pre-owned codecpar extradata / sideData[1] read by avcC/esds; no payload copy"));
    if(void 0!==i.runtimeInitCodecparBytes)Fa.push(eioPlanSite("init-bsf-codecpar-clone",i.runtimeInitCodecparBytes,i.runtimeInitCodecparBytes,"exact native payload clone before runtime BSF init; metadata excluded"))
  }else{
    Fa.push(eioPlanSite("moof-write",Ia,0,"measured by the real module 54070 moof writer"));
    Fa.push(eioPlanSite("mdat-header",S?8:0,0,null));
    Fa.push(eioPlanSite("mdat-payload",D,0,"already resident in track buffers / window"));
    Fa.push(eioPlanSite("track-emit-flush-slice",M,M,"pending track window copied by the fragment flush"));
    Fa.push(eioPlanSite("track-concat",E,E,"module 18528 concat: 0 bytes for a single chunk (identity)"));
    if(g)Fa.push(eioPlanSite("mfra-write",8,0,"hasTfra off: 8 byte mfra box"))
  }
  Fa.push(eioPlanSite("mux-flush-slice",la,la,"one onFlush(...).slice() copy per mux window flush"));
  Fa.push(eioPlanSite("queue-append",va,0,"new portions of the mux flush slices; backfill re-flushes overwrite already queued bytes"));
  Fa.push(eioPlanSite("queue-merge-concat",za,za,"BufferQueue.flush() merge in eioAddMux; 0 for a single chunk"));
  let Ba=0;
  for(let t=0;t<Fa.length;t++){
    Ba=eioPlanAdd(Ba,Fa[t].allocationBytes);
    if(Ba<0)return eioPlanUnsupported("plan-overflow",eioPlanOverflowDetail("mux.workset-sum",{siteIndex:t,siteName:Fa[t].name,allocationBytes:Fa[t].allocationBytes,worksetBytes:Ba}))
  }
  const Ca=bsfKnown?eioPlanSum([muxWindowBytes,z,A,queuedBytes,bsfResidentBytes,k]):null;
  if(bsfKnown&&Ca<0)return eioPlanUnsupported("plan-overflow",eioPlanOverflowDetail("mux.resident-before",{windowBytes:muxWindowBytes,trackWindowBytes:z,sideDataBytes:A,queuedBytes:queuedBytes,bsfResidentBytes:bsfResidentBytes,fixedExtradataBytes:k,residentBeforeBytes:Ca}));
  const Da=bsfKnown?eioPlanSum([muxWindowBytes,f?Aa:z,Ma,bsfResidentBytes,k]):null;
  if(bsfKnown&&Da<0)return eioPlanUnsupported("plan-overflow",eioPlanOverflowDetail("mux.resident-after",{windowBytes:muxWindowBytes,trackWindowBytes:f?Aa:z,queueAfter:Ma,bsfResidentBytes:bsfResidentBytes,fixedExtradataBytes:k,residentAfterBytes:Da}));
  const Ga=bsfKnown?eioPlanAdd(Ca,Ba):null;
  if(bsfKnown&&Ga<0)return eioPlanUnsupported("plan-overflow",eioPlanOverflowDetail("mux.peak-sum",{residentBeforeBytes:Ca,worksetBytes:Ba,peakBytes:Ga}));
  const Ha=r?r.trackPlans||[]:T;
  const Ja=["eioPlanHeader measures writeHeader only; OMov.init BSF/inputCodecpar/avpacket residency is caller-supplied bsfResidentBytes"].concat(bsfUnknown?["bsf-resident-unknown: filter input extradata residency is informational only and not charged"]:[]);
  return Object.freeze({
    supported:!0,
    reason:null,
    detail:null,
    kind:e,
    sites:Object.freeze(Fa),
    tracks:Object.freeze(Ha),
    mux:Object.freeze({windowBytes:muxWindowBytes,pointerBefore:muxPointerBytes,posBefore:muxPosBytes,posAfter:pa,emittedBytes:fa,slices:Object.freeze(ca),sliceBytes:la,newFlushBytes:va,reflushBytes:Ua,pointerAfter:ga}),
    fragment:Object.freeze({emitted:S,moofBytes:Ia,mdatHeaderBytes:S?8:0,mdatPayloadBytes:D,trackFlushSliceBytes:M,concatBytes:E}),
    queue:Object.freeze({before:queuedBytes,chunksBefore:u.length,appendBytes:va,after:Ma,mergeConcatBytes:za}),
    bsf:bsfKnown?Object.freeze({residentBytes:bsfResidentBytes}):null,
    fixedExtradataBytes:k,
    residentBefore:Ca,
    residentAfter:Da,
    worksetBytes:Ba,
    peakBytes:Ga,
    metadataEntries:F,
    metadataEntryLimit:EIO_PLAN_METADATA_MAX_ENTRIES,
    assumptions:EIO_PLAN_ASSUMPTIONS,
    unmeasured:Object.freeze(Ja),
    eioState:r||{shadow:O,streams:aa,context:na,host:sa,writer:o,trackPlans:T}
  })
}
/* Sequential operation plan: every step runs on the same evolving shadow, so a
   gap flush, the packet close, the payload write and the trailer are measured
   in their real order instead of being double counted. */
function eioPlanOperation(t,e){
  if(!e||"object"!=typeof e)return eioPlanUnsupported("request-invalid");
  const i=Array.isArray(e.steps)?e.steps:null;
  if(!i||!i.length)return eioPlanUnsupported("operation-steps-invalid");
  if(i.length>EIO_PLAN_METADATA_MAX_ENTRIES)return eioPlanUnsupported("operation-steps-invalid");
  const n=Object.assign({},e);
  delete n.steps;
  if(1===i.length&&"header"===i[0].kind)return eioPlanMux(t,"header",Object.assign({},n,i[0]));
  let s=null,o=0,a=[],r=[],d=null,u=null,c=null,l=["operation workset is a reservation upper bound, not measured live backing"],p=null;
  for(let e=0;e<i.length;e++){
    const f=i[e]||{};
    if("write"===f.kind){
      const t=eioPlanOperationWrite(s,f);
      if(!t)return eioPlanUnsupported("operation-write-state-invalid");
      if(!t.supported)return t;
      a.push.apply(a,t.sites);
      o=eioPlanAdd(o,t.worksetBytes);
      r.push(Object.freeze({kind:"write",streamIndex:f.streamIndex,payloadBytes:f.payloadBytes,worksetBytes:t.worksetBytes}));
      if(o<0)return eioPlanUnsupported("plan-overflow",eioPlanOverflowDetail("operation.sum-step",{stepIndex:e,stepKind:"write",stepWorksetBytes:t.worksetBytes,totalWorksetBytes:o}));
      continue
    }
    if("muxflush"===f.kind){
      if(!s||!s.writer)return eioPlanUnsupported("operation-muxflush-state-invalid");
      const eioPtr=s.writer.getPointer();
      if(!eioPlanIsCount(eioPtr))return eioPlanUnsupported("plan-overflow",eioPlanOverflowDetail("operation.muxflush-pointer",{stepIndex:e,pointerBytes:eioPtr}));
      const eioFlushSite=eioPlanSite("forced-mux-flush",eioPtr,eioPtr,"explicit ioWriter.flush() of the pending mux window");
      a.push(eioFlushSite);
      o=eioPlanAdd(o,eioPtr);
      if(o<0)return eioPlanUnsupported("plan-overflow",eioPlanOverflowDetail("operation.muxflush-sum",{stepIndex:e,pointerBytes:eioPtr,totalWorksetBytes:o}));
      s.writer.flush();
      if(eioPtr>0){
        const eioQueue=(n.queuedChunkBytes||[]).slice();
        eioQueue.push(eioPtr);
        n.queuedChunkBytes=eioQueue
      }
      r.push(Object.freeze({kind:"muxflush",bytes:eioPtr,worksetBytes:eioPtr}));
      continue
    }
    const g=f.kind;
    const k=Object.assign({},n,{eioState:s||void 0});
    if(void 0!==f.dts)k.dts=f.dts;else delete k.dts;
    const w=eioPlanMux(t,g,k);
    if(!w.supported)return w;
    a.push.apply(a,w.sites);
    o=eioPlanAdd(o,w.worksetBytes);
    if(o<0)return eioPlanUnsupported("plan-overflow",eioPlanOverflowDetail("operation.mux-step-sum",{stepIndex:e,stepKind:g,stepWorksetBytes:w.worksetBytes,totalWorksetBytes:o}));
    if(w.bsf)d=w.bsf;
    p=w.peakBytes;
    c=(c||0)+1;
    r.push(Object.freeze({kind:g,dts:void 0===f.dts?null:String(f.dts),worksetBytes:w.worksetBytes,queueAppendBytes:w.queue?w.queue.appendBytes:0}));
    s=w.eioState;
    const b=n.queuedChunkBytes||[];
    const v=b.slice();
    const U=w.mux&&w.mux.slices||[];
    for(let t=0;t<U.length;t++)v.push(U[t]);
    n.queuedChunkBytes=v
  }
  const f=eioPlanSite("operation-reservation",o,o,"single owned reservation for every measured copy in this synchronous operation");
  a.push(f);
  return Object.freeze({
    supported:!0,
    reason:null,
    detail:null,
    kind:"operation",
    steps:Object.freeze(r),
    sites:Object.freeze(a),
    tracks:Object.freeze([]),
    mux:null,
    fragment:null,
    queue:null,
    bsf:null===d?null:d,
    fixedExtradataBytes:0,
    residentBefore:null,
    residentAfter:null,
    worksetBytes:o,
    reservedUpperBound:o,
    peakBytes:null,
    metadataEntries:0,
    metadataEntryLimit:EIO_PLAN_METADATA_MAX_ENTRIES,
    assumptions:EIO_PLAN_ASSUMPTIONS,
    unmeasured:Object.freeze(l),
    measured:!1
  })
}
function eioPlanOperationWrite(t,e){
  if(!t||!t.context||!t.context.currentFragment)return eioPlanUnsupported("operation-write-state-invalid");
  const i=t.context.currentFragment.tracks||[];
  let n=null;
  for(let t=0;t<i.length;t++)if(i[t].streamIndex===e.streamIndex)n=i[t];
  if(!n)return eioPlanUnsupported("track-stream-missing",e.streamIndex);
  const s=n.ioWriter;
  if(!s||!eioPlanIsCountOnlyWriter(s))return eioPlanUnsupported("track-writer-invalid",n.trackId);
  const o=s.getBufferSize(),a=s.getPointer();
  if(!eioPlanIsCount(o)||o<=0||!eioPlanIsCount(a)||a>o)return eioPlanUnsupported("track-writer-state-invalid",n.trackId);
  const r=e.payloadBytes;
  if(!eioPlanIsCount(r)||r<=0)return eioPlanUnsupported("packet-bytes-invalid");
  const d=eioPlanAdd(a,r);
  if(d<0)return eioPlanUnsupported("plan-overflow",eioPlanOverflowDetail("operation.write.sum",{streamIndex:e.streamIndex,pointerBytes:a,payloadBytes:r,writeBoundBytes:d}));
  if(!eioPlanFlushBounded(d,o))return eioPlanUnsupported("plan-work-exceeded",{writeBoundBytes:d,windowBytes:o,maxFlushes:EIO_PLAN_MAX_FLUSHES});
  const u=s.eioFlushSlices.length;
  s.writeBuffer({length:r});
  const c=s.eioFlushSlices.slice(u);
  const l=eioPlanSum(c);
  if(l<0)return eioPlanUnsupported("plan-overflow",eioPlanOverflowDetail("operation.write.slice-sum",{streamIndex:e.streamIndex,sliceCount:c.length,sliceBytes:l}));
  const p=s.getPointer();
  if(!eioPlanIsCount(p)||p>o)return eioPlanUnsupported("plan-overflow",eioPlanOverflowDetail("operation.write.pointer",{streamIndex:e.streamIndex,pointerBytes:p,windowBytes:o,payloadBytes:r}));
  if(!Array.isArray(n.buffers))n.buffers=[];
  for(let t=0;t<c.length;t++)n.buffers.push({length:c[t]});
  if(!Array.isArray(n.sampleSizes))n.sampleSizes=[];
  n.sampleSizes.push(r);
  n.sampleCount=(n.sampleCount||0)+1;
  if(!n.sampleFlags) n.sampleFlags=[];
  if(!n.sampleDurations)n.sampleDurations=[];
  if(!n.sampleCompositionTimeOffset)n.sampleCompositionTimeOffset=[];
  t.context.currentFragment.firstWrote=!0;
  const f=[eioPlanSite("track-write-slice",l,l,"one onFlush(pointer) copy per track window overflow while writing the packet"),eioPlanSite("track-write-window",p,0,"packet bytes left in the resident track window")];
  let g=0;
  for(let t=0;t<f.length;t++){
    g=eioPlanAdd(g,f[t].allocationBytes);
    if(g<0)return eioPlanUnsupported("plan-overflow",eioPlanOverflowDetail("operation.write.sites-sum",{siteIndex:t,siteName:f[t].name,allocationBytes:f[t].allocationBytes,worksetBytes:g}))
  }
  return Object.freeze({supported:!0,reason:null,detail:null,kind:"write",sites:Object.freeze(f),tracks:Object.freeze([]),mux:null,fragment:null,queue:null,bsf:null,fixedExtradataBytes:0,residentBefore:null,residentAfter:null,worksetBytes:g,peakBytes:null,metadataEntries:0,metadataEntryLimit:EIO_PLAN_METADATA_MAX_ENTRIES,assumptions:EIO_PLAN_ASSUMPTIONS,unmeasured:Object.freeze([]),measured:!1})
}

module.exports={
  EioMuxCountOnlyWriter,eioPlanDiagnosticScalar,eioPlanOverflowDetail,
  eioPlanUnsupported,eioPlanSite,eioPlanIsCount,eioPlanSum,eioPlanAdd,eioPlanMul,
  eioPlanOperation,eioPlanMux,eioPlanOperationWrite,
  EIO_PLAN_METADATA_MAX_ENTRIES,EIO_PLAN_ASSUMPTIONS
};
}
}]);
