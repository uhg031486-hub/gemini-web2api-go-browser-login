'use strict';
const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const read = name => fs.readFileSync(path.join(__dirname, name), 'utf8');

test('Vietnamese popup keeps all action targets and hides the API key', () => {
  const html = read('popup.html');
  assert.match(html, /<html lang="vi">/);
  assert.match(html, /type="password" id="token"/);
  for (const id of ['btnSave','btnBind','btnReadPage','btnUnbind','btnStatusSync','btnKa','btnSync']) {
    assert.match(html, new RegExp(`id="${id}"`));
  }
  assert.match(html, /Thêm vào pool/);
  assert.doesNotMatch(html, /[\u4e00-\u9fff]/);
});

test('All extension-owned runtime messages are translated', () => {
  for (const name of ['popup.js','background.js']) {
    const code = read(name).replace(/\/\*[\s\S]*?\*\//g,'').replace(/^\s*\/\/.*$/gm,'');
    const strings = code.match(/'(?:\\.|[^'\\\n])*'|"(?:\\.|[^"\\\n])*"/g) || [];
    for (const literal of strings) assert.doesNotMatch(literal, /[\u4e00-\u9fff]/, `${name}: untranslated literal`);
  }
});

test('Cookie access allows Google and configurable server endpoints', () => {
  const manifest = JSON.parse(read('manifest.json'));
  assert.deepEqual(
    manifest.host_permissions.sort(),
    ['https://*.google.com/*', 'http://localhost/*', 'http://127.0.0.1/*', 'http://*/*', 'https://*/*'].sort()
  );
  assert.equal(manifest.externally_connectable, undefined);
  assert.doesNotMatch(read('background.js'), /onMessageExternal\.addListener/);
});

test('Defaults use localhost service with no embedded credential', () => {
  const code = read('background.js');
  const prefix = code.slice(0, code.indexOf('const CLIENT'));
  const context = {};
  vm.runInNewContext(prefix + '\nglobalThis.defaults = DEFAULTS;', context);
  assert.equal(context.defaults.controller, 'http://localhost:8083');
  assert.equal(context.defaults.pushMode, 'service');
  assert.equal(context.defaults.token, '');
});

test('Server messages show actionable authentication errors and nested error text', () => {
  const code = read('background.js');
  const context = {};
  vm.runInNewContext(code.slice(0, code.indexOf('const DEFAULTS')) + '\nglobalThis.formatter = typeof formatServerDetail === "function" ? formatServerDetail : null;', context);
  assert.equal(typeof context.formatter, 'function');
  assert.match(context.formatter({error:{message:'Invalid API key'}},401), /API key.*Lưu/);
  assert.equal(context.formatter({error:{message:'Fixture rejected'}},400), 'Fixture rejected');
  assert.equal(context.formatter({error:'Fixture string error'},500), 'Fixture string error');
  assert.equal(context.formatter(null,502), 'HTTP 502');
  assert.equal(context.formatter({detail:'cookie \u5df2\u5165\u5e93\u5e76\u5b8c\u6210\u6a21\u578b\u6821\u9a8c'},200), 'Đã thêm cookie vào pool và kiểm tra mô hình thành công');
});
