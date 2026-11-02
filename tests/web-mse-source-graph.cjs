'use strict';
/* Source-extraction compatibility for the real split webpack modules.
 * This reads shipped source bodies, not rewritten helpers or a fake planner.
 * Function bodies are preserved exactly. Runtime module loading/Worker
 * collection is tested separately; this composition is only for method tests. */
const assert=require('node:assert/strict');
const fs=require('node:fs');
const path=require('node:path');
module.exports=function readMseSourceGraph(){
  const root=path.join(__dirname,'../public/vendor/web/libmedia');
  const pipeline=fs.readFileSync(path.join(root,'26.avplayer.js'),'utf8');
  const planner=fs.readFileSync(path.join(root,'mse-mux-planner.js'),'utf8');
  const start=planner.indexOf('/* EIO-DUAL-MUX-STEP1:BEGIN');
  const end=planner.indexOf('module.exports={',start);
  assert.ok(start>=0&&end>start,'shipped planner source body boundaries');
  const importStart=pipeline.indexOf('/* Separate pure planner module;');
  const importEnd=pipeline.indexOf('}=i(90200);',importStart);
  assert.ok(importStart>=0&&importEnd>importStart,'pipeline imports actual planner module');
  const body=planner.slice(start,end);
  // Preserve the original helper ordering for old extraction boundaries:
  // count/planning functions follow the codecpar inspection helpers.
  const withoutImport=pipeline.slice(0,importStart)+pipeline.slice(importEnd+'}=i(90200);'.length);
  const insertion=withoutImport.indexOf('/* EIO-MSE-PROGRESS:BEGIN');
  assert.ok(insertion>=0,'pipeline observation boundary');
  return withoutImport.slice(0,insertion)+body+withoutImport.slice(insertion);
};
