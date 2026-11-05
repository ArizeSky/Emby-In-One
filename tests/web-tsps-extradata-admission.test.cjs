'use strict';

/*
 * TS/PS extradata admission (E1-E7 wiring) tests.
 *
 * Behavioral: the real shipped 67669 factory accepts the new mpegts/mpegps
 * labels without falling back to the mp4 default, charges the shared
 * controller, and installs the payload into codecpar.
 *
 * Source-composition: the shipped 502/850 demux chunks route every
 * codecpar.extradata production point through eioExtradataReplaceBytes
 * with the mpegts/mpegps labels and the struct-address symbol (never a
 * string property), and checkExtradata is async and awaited at both call
 * sites. These are minified-bundle wiring assertions; the runtime path is
 * covered by the browser fixture evidence recorded in the handover.
 */

const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const vm=require('node:vm');
const ownershipCore=require('../web-src/player-shared-media-ownership.js');
const ownedResources=require('../web-src/player-owned-media-resources.js');
const mediaHandles=require('../web-src/player-media-resource-handles.js');

const base='public/vendor/web/libmedia/';
function factory(file,id,next){
  const source=fs.readFileSync(base+file,'utf8');
  const start=source.indexOf(id+':');
  const end=source.indexOf('},'+next+':',start);
  assert.ok(start>=0&&end>start);
  return vm.runInNewContext('('+source.slice(start+String(id).length+1,end+1)+')');
}

const slotFactory=factory('avplayer.js',67669,67994);

function fixture(){
  const buffer=new Uint8Array(65536),view=new DataView(buffer.buffer);
  const reads={},writes={};
  for(const type of [8,15,20,25]){
    reads[type]=p=>view.getUint32(p,true);
    writes[type]=(p,v)=>view.setUint32(p,v,true);
  }
  let bump=4096;
  const f={buffer,claims:[]};
  const modules={
    80209:{JW:(p,n)=>buffer.subarray(p,p+n),lW:(p,n,bytes)=>buffer.set(bytes,p),
      sO:(p,v,n)=>buffer.fill(v,p,p+n)},
    56238:{f:reads},6927:{M:writes},
    46626:{sY:n=>{const p=bump;bump+=n+16;return p;},Eb:()=>{},aI:()=>{}},
    57216:{bl:()=>{}},46564:{NZ:()=>{}},29688:{N_:-1},
    34117:{z3:()=>{}},54568:{o9:'pointer'},
    90213:mediaHandles
  };
  function load(fn){
    const out={};const req=id=>{assert.ok(modules[id],`module ${id}`);return modules[id];};
    req.d=(out,defs)=>{for(const key in defs)Object.defineProperty(out,key,{get:defs[key]});};
    fn({},out,req);return out;
  }
  f.slots=modules[67669]=load(slotFactory);
  f.cp={pointer:256};
  Object.defineProperties(f.cp,{
    extradata:{get:()=>reads[20](268),set:v=>writes[20](268,v)},
    extradataSize:{get:()=>reads[15](272),set:v=>writes[15](272,v)}
  });
  f.fc={eioAcquireOwnedResource:async(size,label)=>{
    const claim={size,label};f.claims.push(claim);
    return {ok:true,generation:7,isCancelled:()=>false,
      handle:{release:proof=>{
        assert.equal(proof.referencesDropped,true);
        return {ok:true};
      }}};
  }};
  return f;
}

const TS_PS_LABELS=[
  'mpegts-aac-native','mpegts-h264-native','mpegts-hevc-native',
  'mpegts-vvc-native','mpegts-opus-native',
  'mpegps-aac-native','mpegps-h264-native','mpegps-hevc-native',
  'mpegps-vvc-native','mpegps-opus-native'
];

test('the 67669 whitelist accepts every mpegts/mpegps label without fallback',async()=>{
  for(const label of TS_PS_LABELS){
    const f=fixture();
    const bytes=Uint8Array.from([1,2,3,4]);
    await f.slots.eioExtradataReplaceBytes(f.fc,f.cp.pointer,bytes,label);
    assert.equal(f.claims.length,1);
    assert.equal(f.claims[0].label,label,
      label+' must be admitted under its own label, not the mp4 fallback');
    assert.equal(f.claims[0].size,4);
    assert.equal(f.cp.extradataSize,4);
    assert.deepEqual(Array.from(f.buffer.subarray(f.cp.extradata,f.cp.extradata+4)),
      [1,2,3,4]);
    f.slots.dn(f.cp.pointer);
  }
});

test('an unknown label still falls back to the mp4 default label',async()=>{
  const f=fixture();
  await f.slots.eioExtradataReplaceBytes(f.fc,f.cp.pointer,
    Uint8Array.from([9]),'not-a-known-label');
  assert.equal(f.claims[0].label,'mp4-h264-avcc-native');
  f.slots.dn(f.cp.pointer);
});

test('the shipped 502 TS demux routes every extradata production point through admission',()=>{
  const src=fs.readFileSync(base+'502.avplayer.js','utf8');
  for(const label of ['mpegts-aac-native','mpegts-h264-native',
    'mpegts-hevc-native','mpegts-vvc-native','mpegts-opus-native']){
    assert.ok(src.includes('"'+label+'"'),'label '+label);
  }
  assert.ok(src.includes('eioNative=a(67669)'),'67669 import');
  assert.ok(src.includes('eioPtr=a(54568)'),'struct-address symbol import');
  assert.equal(src.split('await eioNative.eioExtradataReplaceBytes(').length-1,5,
    'five admission call sites: checkExtradata + h264 + hevc + vvc + opus');
  assert.ok(src.includes('async checkExtradata(eioFc,e,t)'),'async checkExtradata');
  assert.equal(src.split('await this.checkExtradata(e,t,r)').length-1,2,
    'both checkExtradata call sites awaited');
  assert.ok(!src.includes('.codecpar.pointer,'),'no string pointer access remains');
});

test('the shipped 850 PS demux routes every extradata production point through admission',()=>{
  const src=fs.readFileSync(base+'850.avplayer.js','utf8');
  for(const label of ['mpegps-aac-native','mpegps-h264-native',
    'mpegps-hevc-native','mpegps-vvc-native','mpegps-opus-native']){
    assert.ok(src.includes('"'+label+'"'),'label '+label);
  }
  assert.ok(src.includes('eioNative=a(67669)'),'67669 import');
  assert.equal(src.split('await eioNative.eioExtradataReplaceBytes(').length-1,4,
    'four admission call sites: checkExtradata + h264 + hevc + vvc');
  assert.ok(src.includes('async checkExtradata(eioFc,e,t)'),'async checkExtradata');
  assert.equal(src.split('await this.checkExtradata(e,a,s)').length-1,2,
    'both checkExtradata call sites awaited');
  assert.ok(!src.includes('.codecpar.pointer,'),'no string pointer access remains');
});

test('the avplayer whitelist literal carries the new labels',()=>{
  const src=fs.readFileSync(base+'avplayer.js','utf8');
  const anchor='const eioLabels = [';
  const start=src.indexOf(anchor);
  assert.ok(start>=0);
  const end=src.indexOf('];',start);
  const literal=src.slice(start,end);
  for(const label of TS_PS_LABELS){
    assert.ok(literal.includes('"'+label+'"'),'whitelist '+label);
  }
});
