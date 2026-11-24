'use strict';

const $ = (id) => document.getElementById(id);

function fmtTs(ms) {
  if (!ms) return '—';
  const d = new Date(ms);
  const p = (n) => String(n).padStart(2, '0');
  return `${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`;
}

function authLabel(n) {
  const v = Number(n) || 0;
  return v > 0 ? '/u/' + v : 'Tài khoản mặc định /u/0';
}

function renderCookieSummary(summary) {
  if (!summary || typeof summary !== 'object') return '—';
  return Object.keys(summary).map((name) => {
    const x = summary[name] || {};
    return `${name}: ${x.len || 0}`;
  }).join(' · ') || '—';
}

function send(type, extra) {
  return new Promise((resolve) => {
    chrome.runtime.sendMessage(Object.assign({ type }, extra || {}), (r) => {
      if (chrome.runtime.lastError) resolve({ ok: false, detail: chrome.runtime.lastError.message });
      else resolve(r || { ok: false, detail: 'no response' });
    });
  });
}

function popupActiveTab() {
  return new Promise((resolve) => {
    chrome.tabs.query({ active: true, currentWindow: true }, (tabs) => {
      if (chrome.runtime.lastError) {
        resolve({ ok: false, detail: chrome.runtime.lastError.message });
        return;
      }
      const tab = tabs && tabs[0];
      if (!tab || tab.id == null) {
        resolve({ ok: false, detail: 'Không đọc được tab hiện tại' });
        return;
      }
      resolve({ ok: true, tab });
    });
  });
}

// 哪些字段是用户正在编辑的 (有焦点或被改过但没保存)。
// 周期渲染只刷新状态区，绝不能覆盖这些字段 —— 否则用户填一半就被重置
//  (2026-09-17 线上事故：渲染循环把「推送模式」每次都打回存储值，根本配不了)。
let editingSince = 0;
const EDITABLE_IDS = ['profile', 'pushMode', 'controller', 'token',
  'keepalivePeriodMin', 'autoSyncMin', 'refreshCooldownSec'];

for (const id of EDITABLE_IDS) {
  const el = document.getElementById(id);
  if (!el) continue;
  el.addEventListener('focus', () => { editingSince = Date.now(); });
  el.addEventListener('input', () => { editingSince = Date.now(); });
  el.addEventListener('change', () => { editingSince = Date.now(); });
}

// 用户最后一次交互 (编辑/保存)后 90 秒内不回填表单字段; 
// 超过后认为用户已离开，恢复周期同步。
function formFrozen() {
  return editingSince > 0 && (Date.now() - editingSince) < 90000;
}

async function render() {
  const r = await send('status');
  if (!r.ok) { $('login').textContent = 'Đọc thất bại: ' + r.detail; return; }
  const c = r.config || {};
  const serverOwned = c.maintenanceMode !== 'browser';
  $('maintenanceOwner').textContent = serverOwned ? 'VPS tự duy trì' : 'Trình duyệt duy trì';
  if ($('incognitoState')) $('incognitoState').textContent = r.incognito_allowed ? 'Đã bật ✓' : 'Chưa bật (vào about:addons bật Allow)';
  for (const id of ['autoKeepalive','keepalivePeriodMin','autoSyncMin','refreshCooldownSec','btnKa']) $(id).disabled=serverOwned;
  const bindings = r.bindings || [];
  $('bindingCount').textContent=String(bindings.length);
  const select=$('bindingSelect');
  if (document.activeElement!==select) {
    select.replaceChildren();
    const placeholder=document.createElement('option');placeholder.value='';placeholder.textContent='Chọn tài khoản';select.append(placeholder);
    for (const item of bindings) {
      const option=document.createElement('option');option.value=item.boundEmail;option.textContent=item.boundEmail;
      option.selected=item.boundEmail===c.boundEmail;select.append(option);
    }
  }
  const rows=$('bindingRows');rows.replaceChildren();
  for (const item of bindings) {
    const line=document.createElement('div');line.style.cssText='padding:7px 0;border-bottom:1px solid var(--line);overflow-wrap:anywhere';
    const email=document.createElement('div');email.textContent=item.boundEmail;
    const status=document.createElement('div');status.className='hint';
    status.textContent=(item.maintenanceState==='ok' ? 'Đã xác minh' : item.maintenanceState==='needs_attention' ? 'Cần kiểm tra' : 'Chờ duy trì')+' · '+fmtTs(item.lastSyncAt);
    if(item.lastSyncDetail) {const detail=document.createElement('div');detail.className='hint';detail.textContent=item.lastSyncDetail;line.append(email,status,detail);} else line.append(email,status);
    rows.append(line);
  }

  // 状态区始终刷新 (只写 textContent，不碰表单)
  $('login').textContent = r.logged_in ? 'Đã đăng nhập ✓' : 'Chưa đăng nhập / thiếu cookie phiên';
  $('dot').className = 'dot ' + (r.logged_in ? 'ok' : 'bad');
  $('ckcount').textContent = r.cookie_count + ' cookie Google';

  $('lastka').textContent = fmtTs(c.lastKeepaliveAt) + (c.lastKeepaliveDetail ? ' · ' + c.lastKeepaliveDetail : '');
  $('lastsync').textContent = fmtTs(c.lastSyncAt);
  $('syncdetail').textContent = (c.lastSyncOk ? '✓ ' : '✗ ') + (c.lastSyncDetail || '—');

  const b = r.bound || {};
  $('boundState').textContent = b.tab_id ? 'Đã liên kết' : (b.detail || 'Chưa liên kết');
  $('boundAuth').textContent = b.authuser == null ? '—' : authLabel(b.authuser);
  $('boundEmail').textContent = b.email || '—';
  $('boundStore').textContent = b.store_id || '—';
  $('boundUrl').textContent = b.url || '—';
  if (!b.tab_id) $('pageDetail').textContent = b.detail || 'Hãy liên kết tab Gemini trước';

  // 表单字段只在「用户不在编辑中」时回填，避免覆盖输入
  if (formFrozen()) return;

  $('profile').value = c.profile || '';
  $('pushMode').value = c.pushMode === 'controller' ? 'controller' : 'service';
  $('controller').value = c.controller || '';
  $('token').value = c.token || '';
  $('enabled').checked = !!c.enabled;
  $('autoKeepalive').checked = !!c.autoKeepalive;
  $('keepalivePeriodMin').value = c.keepalivePeriodMin || 10;
  $('autoSyncMin').value = c.autoSyncMin || 30;
  $('refreshCooldownSec').value = c.refreshCooldownSec || 120;
}

async function readPage() {
  const b = $('btnReadPage');
  b.disabled = true; b.textContent = 'Đang đọc…';
  try {
    const r = await send('readPage');
    if (!r.ok) {
      $('pageDetail').textContent = r.detail || 'Đọc thất bại';
      return r;
    }
    const p = r.page || {};
    $('boundState').textContent = 'Đã liên kết · chưa tải lại';
    $('boundAuth').textContent = p.authuser == null ? '—' : authLabel(p.authuser);
    $('boundUrl').textContent = p.url || '—';
    $('pageEmail').textContent = (p.email || 'Không đọc được email từ trang') + (p.account_mismatch ? ' (khác tài khoản đã liên kết)' : '');
    $('pageDetail').textContent = r.detail || 'Đã đọc';
    $('cookieFingerprint').textContent = r.cookie_fingerprint ? r.cookie_fingerprint.slice(0, 16) : '—';
    $('cookieSummary').textContent = renderCookieSummary(r.summary);
    return r;
  } finally {
    b.disabled = false; b.textContent = 'Kiểm tra';
  }
}

async function save() {
  await send('setConfig', {
    patch: {
      profile: $('profile').value.trim(),
      pushMode: $('pushMode').value === 'controller' ? 'controller' : 'service',
      controller: $('controller').value.trim() || 'http://localhost:8083',
      token: $('token').value.trim(),
      enabled: $('enabled').checked,
      autoKeepalive: $('autoKeepalive').checked,
      keepalivePeriodMin: Number($('keepalivePeriodMin').value) || 10,
      autoSyncMin: Number($('autoSyncMin').value) || 30,
      refreshCooldownSec: Number($('refreshCooldownSec').value) || 120,
    },
  });
  editingSince = 0; // 保存成功后解除冻结，下一次 render 回填已保存的值
  await render();
}

$('btnSave').addEventListener('click', save);
$('bindingSelect').addEventListener('change',async () => {
  if (!$('bindingSelect').value) return;
  const r=await send('selectBinding',{email:$('bindingSelect').value});
  $('batchDetail').textContent=r.detail || '';
  await render();
});
$('btnSyncAll').addEventListener('click',async () => {
  const b=$('btnSyncAll');b.disabled=true;b.textContent='Đang đồng bộ theo thứ tự…';
  try {const r=await send('syncAll');$('batchDetail').textContent=r.detail || 'Chưa có kết quả';await render();}
  finally {b.disabled=false;b.textContent='Đồng bộ các tài khoản';}
});
$('btnBindOpen').addEventListener('click',async () => {
  const b=$('btnBindOpen');b.disabled=true;
  try {const r=await send('bindOpenTabs');$('batchDetail').textContent=r.detail || '';await render();}
  finally {b.disabled=false;}
});
$('btnBind').addEventListener('click', async () => {
  const b = $('btnBind');
  b.disabled = true; b.textContent = 'Đang liên kết…';
  try {
    const active = await popupActiveTab();
    if (!active.ok) {
      $('pageDetail').textContent = active.detail || 'Không đọc được trang hiện tại';
      return;
    }
    const r = await send('bindCurrentPage', {
      tabId: active.tab.id,
      tabUrl: active.tab.url || '',
      windowId: active.tab.windowId,
    });
    $('pageDetail').textContent = r.detail || (r.ok ? 'Đã liên kết' : 'Liên kết thất bại');
    if (r.ok) await readPage();
    await render();
  } finally { b.disabled = false; b.textContent = 'Liên kết tab'; }
});
$('btnReadPage').addEventListener('click', readPage);
$('btnStatusSync').addEventListener('click', async () => {
  const b = $('btnStatusSync');
  b.disabled = true; b.textContent = 'Đang đồng bộ…';
  try {
    const r = await send('syncStatus');
    $('pageDetail').textContent = r.ok ? 'Đã gửi trạng thái đến máy chủ' : (r.detail || 'Đồng bộ trạng thái thất bại');
    await render();
  } finally { b.disabled = false; b.textContent = 'Gửi trạng thái'; }
});
$('btnUnbind').addEventListener('click', async () => {
  const b = $('btnUnbind');
  b.disabled = true;
  try { await send('unbindCurrentPage'); await render(); }
  finally { b.disabled = false; }
});
$('btnKa').addEventListener('click', async () => {
  $('btnKa').disabled = true; $('btnKa').textContent = 'Đang giữ phiên…';
  await send('keepalive');
  $('btnKa').disabled = false; $('btnKa').textContent = 'Giữ phiên';
  await render();
});
$('btnSync').addEventListener('click', async () => {
  $('btnSync').disabled = true; $('btnSync').textContent = 'Đang thêm…';
  await send('sync');
  $('btnSync').disabled = false; $('btnSync').textContent = 'Thêm vào pool';
  await render();
});

render();
// 状态区刷新周期。15 秒足够 (Cookie 数/登录态变化很慢)，也更少打扰。
// 渲染函数自身有表单冻结保护，不会覆盖用户正在编辑的字段。
setInterval(render, 15000);
