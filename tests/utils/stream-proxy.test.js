const test = require('node:test');
const assert = require('node:assert/strict');
const { rewriteM3u8 } = require('../../src/utils/stream-proxy');

test('rewriteM3u8 rewrites relative segment URLs to proxy-relative paths', () => {
  const input = '#EXTM3U\nsegment1.ts\n';
  const output = rewriteM3u8(input, 'https://upstream.example/Videos/123/master.m3u8', 'proxy-token');

  assert.match(output, /\/Videos\/123\/segment1\.ts\?api_key=proxy-token/);
  assert.doesNotMatch(output, /localhost/i);
  assert.doesNotMatch(output, /^#EXTM3U\nhttps?:\/\//m);
});

test('rewriteM3u8 rewrites absolute upstream URLs to proxy-relative paths', () => {
  const input = '#EXTM3U\nhttps://cdn.example/Videos/123/hls1/main/seg.ts?foo=1&api_key=upstream\n';
  const output = rewriteM3u8(input, 'https://upstream.example/Videos/123/master.m3u8', 'proxy-token');

  assert.match(output, /\/Videos\/123\/hls1\/main\/seg\.ts\?foo=1&api_key=proxy-token/);
  assert.doesNotMatch(output, /localhost/i);
  assert.doesNotMatch(output, /cdn\.example/i);
});
