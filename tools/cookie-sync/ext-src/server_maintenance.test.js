'use strict';
const fs=require('node:fs'),path=require('node:path'),vm=require('node:vm');
const fixture=fs.readFileSync(path.join(__dirname,'background.test.js'),'utf8').split('async function main()')[0]
 .replace('setCfg, buildCookieHeader };','setCfg, buildCookieHeader, runScheduledBinding, keepalive };');
const tests=`(async()=>{
 chrome.tabs.query=async()=>[...tabs.values()];
 chrome.cookies.getAllCookieStores=async()=>[{id:'0',tabIds:[...tabs.keys()]}];
 for(const name of ['SAPISID','SID','__Secure-1PSID']) cookies.push(makeCookie(name,'fixture-'+name,'/'));
 await api.bindCurrentPage({tabId:1});
 let reloads=0;chrome.tabs.reload=async()=>{reloads++};
 await api.setCfg({lastRefreshAt:0,lastRotateAt:0});
 assert.equal((await api.sync('manual')).ok,true);
 assert.equal(requests.at(-1).body.maintenance_mode,'server','new imports must request VPS ownership');
 requests.length=0;
 assert.equal((await api.runScheduledBinding()).skipped,true,'VPS-owned sessions must not run browser maintenance');
 assert.equal((await api.keepalive(true)).skipped,true,'manual browser keepalive must not compete with VPS');
 assert.equal(requests.length,0);
 assert.equal(reloads,0,'VPS ownership must not refresh even an old bound tab');
 console.log('PASS: server ownership payload, background suppression and browser-rotation guard');
})().catch(e=>{console.error(e);process.exitCode=1});`;
vm.runInNewContext(fixture+tests,{require,__dirname,console,process,setTimeout,TextEncoder,URL});
