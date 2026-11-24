'use strict';
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const fixture = fs.readFileSync(path.join(__dirname,'background.test.js'),'utf8')
  .split('async function main()')[0]
  .replace('setCfg, buildCookieHeader };','setCfg, buildCookieHeader, runBinding, runScheduledBinding, syncAllBindings, unbindCurrentPage, selectBinding, bindingList };');
const tests = `
(async () => {
  state.maintenanceMode='browser'; // Exercise legacy browser-owned maintenance explicitly.
  chrome.tabs.query = async () => [...tabs.values()];
  chrome.cookies.getAllCookieStores = async () => [{id:'0',tabIds:[...tabs.keys()]}];
  for (const name of ['SAPISID','SID','__Secure-1PSID']) cookies.push(makeCookie(name,'fixture-'+name,'/'));
  await api.bindCurrentPage({tabId:1});
  await api.bindCurrentPage({tabId:2});
  assert.equal(state.bindings.length,2,'binding another account must retain the previous link');
  const first=state.bindings.find(b=>b.boundEmail==='first@example.test');
  const second=state.bindings.find(b=>b.boundEmail==='second@example.test');
  for (const item of state.bindings) Object.assign(item,{lastRefreshAt:Date.now(),lastRotateAt:Date.now()});
  const selectedBefore=state.boundEmail;
  requests.length=0;
  assert.equal((await api.runBinding(first,'sync')).ok,true);
  assert.equal((await api.runBinding(second,'sync')).ok,true);
  assert.deepEqual(requests.map(r=>r.body.account_email),['first@example.test','second@example.test']);
  assert.deepEqual(requests.map(r=>r.body.page_authuser),[0,1]);
  assert.equal(requests[0].body.verify_model,false,'background renewal must not invoke model generation');
  assert.equal(state.boundEmail,selectedBefore,'background queue must not change the popup selection');
  assert.ok(state.bindings.every(b=>b.lastSyncOk));

  // Reassociate only the previously authorized email and correct cookie store.
  tabs.delete(1);tabs.set(7,{id:7,url:'https://gemini.google.com/app'});
  pages.set(7,{email:'first@example.test',slot:0,has_token:true});
  assert.equal((await api.runBinding(first,'sync')).ok,true);
  assert.equal(state.bindings.find(b=>b.boundEmail===first.boundEmail).boundTabId,7);
  pages.set(7,{email:'other@example.test',slot:0,has_token:true});
  requests.length=0;
  assert.equal((await api.runBinding(state.bindings.find(b=>b.boundEmail===first.boundEmail),'sync')).ok,false);
  assert.equal(requests.length,0,'must not upload cookies for a different identity');
  assert.equal((await api.runBinding(state.bindings.find(b=>b.boundEmail===second.boundEmail),'sync')).ok,true,'one bad link must not block another account');
  await api.selectBinding(second.boundEmail);
  await api.unbindCurrentPage();
  assert.equal(state.bindings.length,1,'unlink removes only the selected account');
  assert.equal(state.bindings[0].boundEmail,first.boundEmail);

  // Migrate existing single binding without losing it or duplicating it.
  state.bindings=undefined;
  Object.assign(state,{boundTabId:2,boundEmail:'second@example.test',boundStoreId:'0',boundAuthuser:1,boundAt:1});
  assert.equal(api.bindingList(state).length,1);
  await api.bindCurrentPage({tabId:2});
  assert.equal(state.bindings.length,1);
  await api.setCfg({lastRefreshAt:Date.now(),lastRotateAt:Date.now()});
  requests.length=0;
  assert.equal((await api.runScheduledBinding()).ok,true);
  assert.equal(requests.length,1,'scheduler processes one account per tick');
  assert.equal((await api.runScheduledBinding()).skipped,true,'fresh link is not repeatedly renewed');
  chrome.cookies.getAllCookieStores = async () => [{id:'1',tabIds:[...tabs.keys()]}];
  requests.length=0;
  assert.equal((await api.runBinding(state.bindings[0],'sync')).ok,false,'must reject a different cookie store');
  assert.equal(requests.length,0);
  chrome.cookies.getAllCookieStores = async () => [{id:'0',tabIds:[...tabs.keys()]}];
  const parallel=await Promise.all([api.runBinding(state.bindings[0],'sync'),api.runBinding(state.bindings[0],'sync')]);
  assert.equal(parallel.filter(r=>r.ok).length,1,'concurrent maintenance is serialized');
  console.log('PASS: multi-binding persistence, identity/store isolation, verified reattachment, independent refresh state, migration and scheduler');
})().catch(e=>{console.error(e);process.exitCode=1});
`;
vm.runInNewContext(fixture+tests,{require,__dirname,console,process,setTimeout,TextEncoder,URL});
