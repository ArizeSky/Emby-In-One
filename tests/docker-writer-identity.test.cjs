'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { spawnSync } = require('node:child_process');
const source = fs.readFileSync(path.join(__dirname, '../eio-docker.sh'), 'utf8');
const start = source.indexOf('check_writers(){');
const end = source.indexOf('\n}\n', start) + 3;
const fn = source.slice(start, end);
test('writer guard excludes exact full self ID and rejects other overlapping writable mounts', () => {
  const dir = '/tmp/eio-writer-fixture', id = 'a'.repeat(64), other = 'b'.repeat(64);
  for (const [kind, expected] of [['self', 0], ['unrelated', 0], ['shared', 1], ['parent', 1], ['read-only', 0]]) {
    const own = JSON.stringify([{ Mounts: [{ Type: 'bind', Source: dir + '/config', RW: true }] }]);
    const foreignSource = kind === 'parent' ? dir : kind === 'shared' || kind === 'read-only' ? dir + '/data' : '/tmp/unrelated';
    const foreign = JSON.stringify([{ Mounts: [{ Type: 'bind', Source: foreignSource, RW: kind !== 'read-only' }] }]);
    const script = `set -euo pipefail
PROJECT_DIR='${dir}'
fail(){ echo "$*" >&2; exit 1; }
systemctl(){ return 1; }
python3(){ ${process.platform === 'win32' ? 'shift; command python -c ' + "'import sys,os,posixpath;os.path=posixpath;code=sys.argv.pop(1);exec(code)'" : 'command python3'} "$@"; }
docker(){
  if [[ "$1" == ps ]]; then
    if [[ " $* " == *' --no-trunc '* ]]; then printf '%s\n' '${id}'; else printf '%s\n' '${id.slice(0,12)}'; fi
    ${kind === 'self' ? ':' : `printf '%s\n' '${other}'`}
  elif [[ "$1" == inspect ]]; then
    if [[ "$2" == '${id}' ]]; then printf '%s\n' '${own}'; else printf '%s\n' '${foreign}'; fi
  else return 2; fi
}
${fn}
check_writers '${id}'
`;
    const result = spawnSync('bash', ['-c', script], { encoding: 'utf8', env: { ...process.env, MSYS2_ARG_CONV_EXCL: '*' }, timeout: 10000 });
    assert.equal(result.status, expected, `${kind}: ${result.stderr || result.error || ''}`);
  }
});
