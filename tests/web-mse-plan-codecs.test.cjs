'use strict';

/*
 * Planner codec-table tests: EIO_PLAN_CODECS must mirror the muxer's declared
 * Capabilities (module 90197/90198) so that every codec the muxer can write
 * is plannable. The count-only plan measures the real box writers through the
 * count-only writer factory, so the codec gate is the only codec-specific
 * refusal left; a codec the muxer writes but the table omits fails the whole
 * dual-cache playback with EIO_MSE_PLAN_UNSUPPORTED (codec-unsupported).
 */

const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const vm=require('node:vm');

const source=fs.readFileSync('public/vendor/web/libmedia/mse-mux-planner.js','utf8');

function extractTable(){
  const marker='const EIO_PLAN_CODECS=';
  const start=source.indexOf(marker);
  assert.ok(start>=0,'EIO_PLAN_CODECS must stay extractable');
  const open=source.indexOf('{',start);
  let depth=0;
  for(let i=open;i<source.length;i++){
    if(source[i]==='{')depth+=1;
    else if(source[i]==='}'){
      depth-=1;
      if(depth===0){
        const literal=source.slice(open,i+1);
        return vm.runInNewContext('('+literal+')');
      }
    }
  }
  assert.fail('unbalanced EIO_PLAN_CODECS literal');
}

function extractTrackLabel(table){
  const marker='function eioPlanTrackLabel(t,e){';
  const start=source.indexOf(marker);
  assert.ok(start>=0,'eioPlanTrackLabel must stay extractable');
  const open=source.indexOf('{',start);
  let depth=0;
  for(let i=open;i<source.length;i++){
    if(source[i]==='{')depth+=1;
    else if(source[i]==='}'){
      depth-=1;
      if(depth===0){
        const body=source.slice(start,i+1);
        return vm.runInNewContext('('+body+')',{EIO_PLAN_CODECS:table,Object});
      }
    }
  }
  assert.fail('unbalanced eioPlanTrackLabel body');
}

/* The muxer's declared Capabilities (module 90197/90198 writer set). */
const MUXER_CAPABILITIES={
  27:0,173:0,196:0,12:0,167:0,225:0,
  86018:1,86017:1,86028:1,86076:1,86021:1,86051:1,86019:1,86056:1,
  94226:3,94213:3
};

test('the plan codec table mirrors the muxer Capabilities exactly',()=>{
  const table=extractTable();
  assert.deepEqual(Object.keys(table).map(Number).sort((a,b)=>a-b),
    Object.keys(MUXER_CAPABILITIES).map(Number).sort((a,b)=>a-b),
    'every muxer-writable codec must be plannable');
  for(const [codecId,codecType] of Object.entries(MUXER_CAPABILITIES)){
    const entry=table[codecId];
    assert.ok(entry,'codec '+codecId+' entry');
    assert.equal(entry.codecType,codecType,'codec '+codecId+' codecType');
    assert.equal(typeof entry.label,'string','codec '+codecId+' label');
    assert.ok(entry.label.length>0,'codec '+codecId+' non-empty label');
  }
});

test('eioPlanTrackLabel accepts matching types and refuses mismatches',()=>{
  const table=extractTable();
  const label=extractTrackLabel(table);
  assert.equal(label(27,0),'h264-avcc');
  assert.equal(label(86028,1),'flac');
  assert.equal(label(173,0),'hevc-hvc1');
  assert.equal(label(27,1),null,'h264 on an audio stream is a mismatch');
  assert.equal(label(86028,0),null,'flac on a video stream is a mismatch');
  assert.equal(label(9999,0),null,'unknown codec refused');
  assert.equal(label(94226,3),'wvtt','subtitle track label');
});
