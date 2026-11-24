/**
 * Gemini Cookie Sync — background service worker (MV3)
 * ---------------------------------------------------
 * 职责 (在服务器自带的 Chromium 里运行，随 profile 常驻)：
 *   1) 保活：每 10 分钟刷新一次已打开的 gemini.google.com 页面，让 Google 正常
 *      轮换 __Secure-1PSIDTS / SIDCC 等会话 cookie，避免会话静默失效。
 *   2) 取 cookie：用 chrome.cookies API 读取当前 profile 里真实、已轮换过的
 *      Gemini Web 会话 cookie (SID/HSID/SSID/APISID/SAPISID/__Secure-*)。
 *   3) 入池：每 30 分钟 (以及登录成功/手动触发时)把 cookie 推送到宿主机上的
 *      gemini-web2api 控制器 /cookie-sync，由控制器写入容器内 SQLite cookie 池。
 *
 * ★ 风控规避 (v1.0.5 起)★
 *   不再「每次抓取都新开一个页面」——那样短时间内在同一个 Google 账号下反复
 *   产生新会话，极易触发风控 (会话被踢、出现 sorry 页)。现在改为：
 *     · 复用同一个 Gemini 标签页，用 chrome.tabs.reload() 的方式刷新，
 *       让服务端重新下发轮换 cookie; 
 *     · 每次刷新后进入 refreshCooldownSec (默认 120 秒)冷却窗口，
 *       窗口内只做 cookie 提取，绝不做任何导航; 
 *     · 只有冷却结束、且仍未拿到可用 cookie 时，才允许再刷新一次。
 *
 * 配置存在 chrome.storage.local：
 *   controller         控制器地址，默认 http://127.0.0.1:9280
 *   profile            本 profile 在池中的标识，默认 browser1
 *   token              控制器共享密钥 (可空)
 *   refreshCooldownSec 刷新冷却窗口 (秒)，默认 120
 *   autoKeepalive / keepalivePeriodMin / autoSyncMin / enabled
 */

'use strict';

function formatServerDetail(body, status) {
  if (status === 401) return 'API key chưa được cấu hình hoặc không hợp lệ. Hãy nhập API key của máy chủ rồi bấm Lưu.';
  const error = body && body.error;
  const message = (typeof error === 'string' ? error : error && (error.message || error.detail))
    || (body && (body.detail || body.message));
  if (typeof message !== 'string' || !message.trim()) return 'HTTP ' + status;
  if (message === 'cookie \u5df2\u5165\u5e93\u5e76\u5b8c\u6210\u6a21\u578b\u6821\u9a8c') {
    return 'Đã thêm cookie vào pool và kiểm tra mô hình thành công';
  }
  return message;
}

const DEFAULTS = {
  controller: 'http://localhost:8083',
  // pushMode: controller = 推给同机/同网控制器; service = 直推 gemini-web2api
  // 服务端 (远程场景必需，端点 /api/browser/ingest，用 Bearer API key 鉴权)。
  pushMode: 'service',
  maintenanceMode: 'server',
  profile: 'firefox',
  token: '',
  enabled: true,
  autoKeepalive: true,
  keepalivePeriodMin: 10,
  autoSyncMin: 30,
  refreshCooldownSec: 120,
  lastSyncAt: 0,
  lastSyncOk: false,
  lastSyncDetail: '',
  lastKeepaliveAt: 0,
  lastKeepaliveDetail: '',
  lastRotateAt: 0,
  lastRefreshAt: 0,
  lastRefreshDetail: '',
  lastTabId: 0,          // 复用的 Gemini 标签页 (含被重定向到 sorry 页的情况)
  lastTabUrl: '',
  // 单个扩展实例只绑定一个明确的 Gemini 标签页/账号槽位。
  boundTabId: 0,
  boundTabUrl: '',
  boundAuthuser: 0,
  // Email read from the bound page itself (WIZ_global_data.oPEP7c). It is
  // re-checked before every ingest and verified again by the server.
  boundEmail: '',
  boundStoreId: '',
  boundAt: 0,
  bindings: [],
  lastReadPageAt: 0,
  lastReadPageDetail: '',
};

const CLIENT = 'gw2a-ext/1.2.1-server-maintenance';

const ALARM_KEEPALIVE = 'gw2a-keepalive';
const ALARM_SYNC = 'gw2a-sync';
const ALARM_STATUS = 'gw2a-status';
const GEMINI_URL = 'https://gemini.google.com/app';
// 尾部斜杠可选 (https://gemini.google.com 不带斜杠也常出现)，
// 否则会对「已存在的 Gemini 页」判 false，转去 tabs.update 甚至新建。
const GEMINI_URL_RE = /^https:\/\/gemini\.google\.com(\/|$)/;

// 被 Google 风控拦下时的落地页 (www.google.com/sorry/...)。这种页面同样是
// 「本 profile 的那个 Gemini 标签页」，必须复用而不是另开新页。
const SORRY_URL_RE = /^https:\/\/(www\.)?google\.com\/sorry\//;

function isGeminiUrl(url) {
  return GEMINI_URL_RE.test(String(url || ''));
}

function isGeminiOrSorryUrl(url) {
  const s = String(url || '');
  return isGeminiUrl(s) || SORRY_URL_RE.test(s);
}

/**
 * Parse an explicit Google account slot from /u/N or ?authuser=N.
 * Returns null when the URL names no numeric slot (plain /app); the page
 * session index is then used instead of silently assuming the default.
 */
function authuserFromUrl(url) {
  const s = String(url || '');
  const m = s.match(/\/u\/(\d+)(?:\/|$)/) || s.match(/[?&]authuser=(\d+)/);
  if (!m) return null;
  const n = Number(m[1]);
  return Number.isInteger(n) && n >= 0 && n <= 9 ? n : null;
}

function authuserLabel(n) {
  return Number(n) > 0 ? '/u/' + Number(n) : 'Tài khoản mặc định /u/0';
}

function sameEmail(a, b) {
  return String(a || '').trim().toLowerCase() === String(b || '').trim().toLowerCase();
}

/**
 * Read the signed-in identity of exactly one tab from the page (MAIN world).
 * WIZ_global_data.oPEP7c is the account email, QrtxK the session index.
 * No navigation, no reload, no other tabs.
 */
function executeTabCode(tabId, code) {
  return new Promise((resolve, reject) => {
    chrome.tabs.executeScript(tabId, { code }, (res) => {
      if (chrome.runtime.lastError) reject(new Error(chrome.runtime.lastError.message));
      else resolve(res);
    });
  });
}

async function probePageIdentity(tabId) {
  try {
    const code = `(() => {
      const rawWin = window.wrappedJSObject || window;
      const w = (rawWin && rawWin.WIZ_global_data) || {};
      const raw = w.QrtxK == null ? '' : String(w.QrtxK);
      const slot = /^\\d$/.test(raw) ? Number(raw) : null;
      let email = typeof w.oPEP7c === 'string' && w.oPEP7c.indexOf('@') > 0 ? String(w.oPEP7c) : '';
      if (!email) {
        const nodes = document.querySelectorAll('a[aria-label*="@"], [data-email]');
        for (const el of nodes) {
          const text = (el.getAttribute('data-email') || '') + ' ' + (el.getAttribute('aria-label') || '');
          const m = text.match(/[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\\.[A-Za-z]{2,}/);
          if (m) { email = m[0]; break; }
        }
      }
      const body = (document.body && document.body.innerText || '').slice(0, 1200);
      return {
        url: location.href,
        title: document.title || '',
        email,
        slot,
        has_token: typeof w.SNlM0e === 'string' && w.SNlM0e.length > 0,
        signed_out: /Sign in to save activity|^Sign in$/m.test(body),
      };
    })()`;
    const res = await executeTabCode(tabId, code);
    return (res && res[0]) || { error: 'Trang không trả về danh tính' };
  } catch (e) {
    return { error: String((e && e.message) || e) };
  }
}

/** An explicit URL slot wins; otherwise use the page's own session index. */
function resolveSlot(url, probe) {
  const fromUrl = authuserFromUrl(url);
  if (fromUrl !== null) return { slot: fromUrl, source: 'url' };
  if (probe && Number.isInteger(probe.slot)) return { slot: probe.slot, source: 'page' };
  return { slot: 0, source: 'default' };
}

// 池里真正需要的 cookie (顺序即写入池的顺序，SAPISID 放最前便于人工核对)
const COOKIE_ORDER = [
  'SAPISID', '__Secure-1PAPISID', '__Secure-3PAPISID', 'APISID',
  'SID', 'HSID', 'SSID', '__Secure-1PSID', '__Secure-3PSID',
  '__Secure-1PSIDTS', '__Secure-3PSIDTS', '__Secure-1PSIDCC', '__Secure-3PSIDCC',
  'SIDCC', 'LSID', '__Host-1PLSID', '__Host-3PLSID',
  'ACCOUNT_CHOOSER', 'NID', 'COMPASS',
];
// 登录判定用「任一组命中」而不是「全部命中」：
// 不同 Google 站点下发的 cookie 组合不完全一致 (例如只拿到 SAPISID+__Secure-1PSID
// 而没有 SID)，硬性要求全部存在会把已登录会话误判成未登录。
const AUTH_ANY = [
  ['SAPISID', 'SID', '__Secure-1PSID'],   // 组 1：任一存在
  ['__Secure-1PSID', '__Secure-3PSID'],   // 组 2：任一存在
];

// 同一时刻只允许一个 sync/keepalive 在跑，避免「刷新 → onUpdated → sync」
// 这类回调互相触发、连环刷新页面。
let BUSY = false;
let BUSY_SINCE = 0;
const BUSY_MAX_MS = 150000;   // 锁最长持有时间; 超过则视为悬挂，自动释放

/** 拿锁; 若上一轮已悬挂超过 BUSY_MAX_MS 则自动释放并接管。 */
function acquireBusy() {
  if (BUSY && Date.now() - BUSY_SINCE < BUSY_MAX_MS) return false;
  if (BUSY) log('Phát hiện tác vụ treo (đã giữ khóa ' +
    Math.round((Date.now() - BUSY_SINCE) / 1000) + ' giây), tiếp quản');
  BUSY = true;
  BUSY_SINCE = Date.now();
  return true;
}

function releaseBusy() { BUSY = false; BUSY_SINCE = 0; }

// ---------------------------------------------------------------- helpers

let bindingContext = null;
const BINDING_FIELDS = ['boundTabId','boundTabUrl','boundAuthuser','boundEmail','boundStoreId','boundAt',
  'lastSyncAt','lastSyncOk','lastSyncDetail','lastKeepaliveAt','lastKeepaliveDetail',
  'lastRotateAt','lastRefreshAt','lastRefreshDetail','lastTabId','lastTabUrl',
  'lastAttemptAt','nextAttemptAt','maintenanceState'];

function bindingList(c) {
  const list = Array.isArray(c.bindings) ? c.bindings.filter(b => b && b.boundEmail).map(b => ({...b})) : [];
  if (!list.length && c.boundEmail) {
    const old = {};
    for (const key of BINDING_FIELDS) if (c[key] !== undefined) old[key] = c[key];
    list.push(old);
  }
  const unique = new Map();
  for (const b of list) unique.set(String(b.boundEmail).trim().toLowerCase(), b);
  return [...unique.values()];
}

async function rawCfg() {
  const got = await chrome.storage.local.get(DEFAULTS);
  return Object.assign({}, DEFAULTS, got);
}

async function cfg() {
  const got = await rawCfg();
  return bindingContext ? {...got, ...bindingContext} : got;
}

async function setCfg(patch, globalOnly) {
  const current = await rawCfg();
  const list = bindingList(current);
  if (bindingContext && !globalOnly) {
    const index = list.findIndex(b => sameEmail(b.boundEmail, bindingContext.boundEmail));
    if (index < 0) throw new Error('Liên kết đã bị gỡ; dừng đồng bộ');
    Object.assign(bindingContext, patch);
    Object.assign(list[index], patch);
    await chrome.storage.local.set({bindings:list});
    return cfg();
  }
  if (!Object.prototype.hasOwnProperty.call(patch,'bindings') && current.boundEmail) {
    const item = list.find(b => sameEmail(b.boundEmail,current.boundEmail));
    if (item) {
      for (const key of BINDING_FIELDS) if (key in patch) item[key] = patch[key];
      patch = {...patch, bindings:list};
    }
  }
  await chrome.storage.local.set(patch);
  return cfg();
}

async function selectBinding(email) {
  if (BUSY) return {ok:false,detail:'Đang đồng bộ; vui lòng chờ rồi chọn lại'};
  const c = await rawCfg();
  const item = bindingList(c).find(b => sameEmail(b.boundEmail,email));
  if (!item) return {ok:false,detail:'Không tìm thấy tài khoản đã liên kết'};
  const fields = {};
  for (const key of BINDING_FIELDS) fields[key] = item[key] ?? DEFAULTS[key] ?? 0;
  await setCfg(fields,true);
  return {ok:true};
}

async function bindOpenTabs() {
  if (BUSY) return {ok:false,detail:'Đang đồng bộ; vui lòng chờ'};
  const before=await rawCfg();
  const results=[];
  for (const tab of await chrome.tabs.query({url:'https://gemini.google.com/*'})) {
    if (isGeminiUrl(tab.url)) results.push(await bindCurrentPage({tabId:tab.id}));
  }
  if (before.boundEmail) await selectBinding(before.boundEmail);
  const count=results.filter(r=>r.ok).length;
  return {ok:count>0,detail:'Đã liên kết '+count+'/'+results.length+' tab Gemini; tab chưa đăng nhập sẽ được bỏ qua'};
}

async function runBinding(binding, action, manual) {
  if (!acquireBusy()) return {ok:false,detail:'Tác vụ trước vẫn đang chạy'};
  try {
    const c = await rawCfg();
    const item = bindingList(c).find(b => sameEmail(b.boundEmail,binding.boundEmail));
    if (!item) return {ok:false,detail:'Liên kết đã bị gỡ'};
    bindingContext = {...item};
    let result;
    try {
      const bound = await getBoundGeminiTab();
      if (!bound.ok) result = {ok:false,detail:bound.detail};
      else result = action === 'keepalive' ? await keepalive(!!manual,true) : await sync(manual ? 'manual' : 'scheduled',true);
    } catch(e) { result = {ok:false,detail:String(e.message || e)}; }
    await setCfg({lastAttemptAt:Date.now(),nextAttemptAt:Date.now()+Math.max(1,Number(c.keepalivePeriodMin)||10)*60000,
      maintenanceState:result.ok ? 'ok' : 'needs_attention',lastSyncOk:!!result.ok,lastSyncDetail:result.detail || ''});
    return {...result,email:item.boundEmail};
  } finally { bindingContext=null;releaseBusy(); }
}

async function runScheduledBinding() {
  const c=await rawCfg();
  if (c.maintenanceMode === 'server') return {ok:true,skipped:true,detail:'VPS tự duy trì; tiện ích không tải lại hoặc gửi cookie theo lịch'};
  if (!c.enabled) return {ok:false,skipped:true,detail:'Tiện ích đã tắt'};
  const now=Date.now();
  const due=bindingList(c).filter(b => !b.nextAttemptAt || b.nextAttemptAt<=now)
    .sort((a,b)=>(a.lastAttemptAt||0)-(b.lastAttemptAt||0));
  if (!due.length) return {ok:true,skipped:true};
  return runBinding(due[0],c.autoKeepalive ? 'keepalive' : 'sync',false);
}

async function syncAllBindings() {
  const c=await rawCfg();
  const list=bindingList(c);
  const results=[];
  for (let i=0;i<Math.min(list.length,10);i++) {
    if (i) await sleep(12000); // Respect the VPS's configured six requests/minute.
    results.push(await runBinding(list[i],'sync',true));
  }
  return {ok:results.length>0 && results.every(r=>r.ok),results,
    detail:'Đồng bộ '+results.filter(r=>r.ok).length+'/'+results.length+' tài khoản; các tài khoản còn lại tiếp tục theo lịch'};
}

function log(...a) {
  console.log('[gw2a-ext]', ...a);
}

function sleep(ms) {
  return new Promise((r) => setTimeout(r, ms));
}

function cooldownMs(c) {
  const s = Number((c && c.refreshCooldownSec) || 0) || 120;
  return Math.max(10, Math.min(3600, s)) * 1000;
}

function sha1Hex(str) {
  const buf = new TextEncoder().encode(str);
  return crypto.subtle.digest('SHA-1', buf).then((h) => {
    return Array.from(new Uint8Array(h)).map((b) => b.toString(16).padStart(2, '0')).join('');
  });
}

/** 当前 profile 的标识：优先用配置，其次用默认名。 */
async function profileName() {
  const c = await cfg();
  return c.profile || 'browser1';
}

// ---------------------------------------------------------------- cookies

/** 站点列表：逐项 get() 时依次尝试，能命中登录 cookie 就算成功。 */
const READ_URLS = [
  'https://gemini.google.com/',
  'https://accounts.google.com/',
  'https://www.google.com/',
  'https://google.com/',
];

/** Match the same host/path rules Chrome applies to a request for this URL. */
function cookieCoversUrl(ck, url) {
  const host = url.hostname.toLowerCase();
  const domain = String(ck.domain || '').replace(/^\./, '').toLowerCase();
  if (ck.hostOnly ? host !== domain : host !== domain && !host.endsWith('.' + domain)) return false;
  if (ck.secure && url.protocol !== 'https:') return false;
  const path = ck.path || '/';
  const target = url.pathname || '/';
  return target === path || (target.startsWith(path) &&
    (path.endsWith('/') || target.charAt(path.length) === '/'));
}

/**
 * Read only cookies applicable to the bound Gemini URL and its cookie store.
 * Google accounts in one store may share root-path cookies; the bound URL's
 * /u/N slot is still required to select the account on upstream requests.
 */
async function readGoogleCookies(storeId, scopedUrl) {
  const scope = scopedUrl && isGeminiUrl(scopedUrl) ? new URL(scopedUrl) : null;
  if (scopedUrl && !scope) return new Map();
  const opts = (base) => {
    if (!storeId) return base;
    return Object.assign({}, base, { storeId: String(storeId) });
  };
  const seen = new Map();
  const score = (x) => (x.domain === '.google.com' ? 2 : 0) + (x.httpOnly ? 2 : 0) + (x.secure ? 1 : 0);
  const put = (ck) => {
    if (!ck || !ck.name) return;
    if (scope && !cookieCoversUrl(ck, scope)) return;
    const prev = seen.get(ck.name);
    if (!prev) { seen.set(ck.name, ck); return; }
    if (!prev.value && ck.value) { seen.set(ck.name, ck); return; }
    if (scope) {
      const prevLength = (prev.path || '/').length;
      const nextLength = (ck.path || '/').length;
      if (prevLength !== nextLength) {
        if (nextLength > prevLength) seen.set(ck.name, ck);
        return;
      }
    }
    if (score(ck) > score(prev)) seen.set(ck.name, ck);
  };
  const push = (list) => { for (const ck of list) put(ck); };

  const urls = scope ? [scope.href] : READ_URLS;
  try { push(await chrome.cookies.getAll(opts({ domain: '.google.com' }))); } catch (e) { log('getAll google.com failed', e); }
  for (const u of urls) {
    try { push(await chrome.cookies.getAll(opts({ url: u }))); } catch (e) {}
  }
  if (!scope) {
    try { push(await chrome.cookies.getAll(opts({}))); } catch (e) {}
  }

  // getAll can omit legacy auth cookies or a more specific path. In scoped
  // mode get({url,name}) must also run for names already seen at path=/.
  const wanted = scope ? COOKIE_ORDER : COOKIE_ORDER.filter((n) => {
    const ck = seen.get(n);
    return !ck || !ck.value;
  });
  if (wanted.length) {
    for (const name of wanted) {
      for (const u of urls) {
        try {
          const ck = await chrome.cookies.get(opts({ url: u, name }));
          if (ck && ck.value) { put(ck); break; }
        } catch (e) {}
      }
    }
    const still = wanted.filter((n) => !(seen.get(n) || {}).value);
    if (still.length) log('Vẫn thiếu cookie sau khi đọc bổ sung:', still.join(','));
  }
  return seen;
}

/** 组装成 Cookie 头字符串 + 结构体。 */
function buildCookieHeader(map) {
  const parts = [];
  const obj = {};
  for (const name of COOKIE_ORDER) {
    const ck = map.get(name);
    if (ck && ck.value) { parts.push(name + '=' + ck.value); obj[name] = ck.value; }
  }
  // 兜底：把剩下未列出的 google 会话类 cookie 也带上 (跳过统计类)
  for (const [name, ck] of map) {
    if (obj[name] || !ck.value) continue;
    if (/^(_ga|_gcl|_gid|OTZ|COMPASS)/.test(name)) continue;
    parts.push(name + '=' + ck.value);
    obj[name] = ck.value;
  }
  return { header: parts.join('; '), obj };
}

function cookieSummary(map) {
  const now = Date.now() / 1000;
  const out = {};
  for (const name of COOKIE_ORDER) {
    const ck = map.get(name);
    if (!ck) continue;
    out[name] = {
      len: (ck.value || '').length,
      expires: ck.expirationDate || null,
      expired: !!(ck.expirationDate && ck.expirationDate < now),
    };
  }
  return out;
}

function isLoggedIn(map) {
  const now = Date.now() / 1000;
  const alive = (k) => {
    const ck = map.get(k);
    if (!ck || !ck.value) return false;
    if (ck.expirationDate && ck.expirationDate < now) return false;
    return true;
  };
  for (const group of AUTH_ANY) {
    if (group.some(alive)) return true;
  }
  return false;
}

// ---------------------------------------------------------------- tabs

/** Return the cookie store containing a tab. Chrome stores cookies per
 * profile/incognito store, not per tab; tabIds is the only reliable bridge. */
async function cookieStoreIdForTab(tabId) {
  try {
    const t = await chrome.tabs.get(Number(tabId));
    if (t && t.cookieStoreId) return String(t.cookieStoreId);
  } catch (e) {}
  try {
    if (chrome.cookies.getAllCookieStores) {
      const stores = await chrome.cookies.getAllCookieStores();
      for (const store of stores || []) {
        if ((store.tabIds || []).some((id) => Number(id) === Number(tabId))) {
          return String(store.id || '');
        }
      }
    }
  } catch (e) { log('getAllCookieStores failed', e); }
  return '';
}

/**
 * Resolve an explicitly saved identity; reattach only after email/store/slot verification.
 */
async function getBoundGeminiTab() {
  const c = await cfg();
  return resolveSavedBinding(c);
}

async function resolveSavedBinding(c, readOnly) {
  const validate = async (tab) => {
    const url=String(tab?.url||'');
    if (!isGeminiUrl(url)) return null;
    const storeId=await cookieStoreIdForTab(tab.id);
    if (c.boundStoreId && storeId!==String(c.boundStoreId)) return null;
    const identity=await probePageIdentity(tab.id);
    if (!identity.has_token || !identity.email || !sameEmail(identity.email,c.boundEmail)) return null;
    const resolved=resolveSlot(url,identity);
    if (resolved.source==='url' && Number.isInteger(identity.slot) && resolved.slot!==identity.slot) return null;
    return {ok:true,tab,url,storeId,authuser:resolved.slot,email:identity.email};
  };
  if (!c.boundEmail) return {ok:false,detail:'Chưa liên kết tab Gemini'};
  let original=null;
  try {original=await chrome.tabs.get(Number(c.boundTabId));} catch(e) {}
  let found=original ? await validate(original) : null;
  if (!found && chrome.tabs.query) {
    const candidates=[];
    for (const tab of await chrome.tabs.query({url:'https://gemini.google.com/*'})) {
      if (tab.id===original?.id) continue;
      const match=await validate(tab);
      if (match) candidates.push(match);
    }
    if (candidates.length===1) found=candidates[0];
    else if (candidates.length>1) return {ok:false,detail:'Có nhiều tab cùng tài khoản; hãy liên kết lại tab muốn dùng'};
  }
  if (!found) {
    let actual='';
    if (original && isGeminiUrl(original.url)) actual=(await probePageIdentity(original.id)).email||'';
    return {ok:false,detail:actual ? 'Tài khoản trên tab đã đổi thành '+actual+'; chưa đồng bộ tài khoản cũ' :
      'Không còn tab đăng nhập đúng tài khoản; mở lại Gemini rồi liên kết lại'};
  }
  if (!readOnly && (found.tab.id!==c.boundTabId || found.authuser!==c.boundAuthuser)) {
    await setCfg({boundTabId:found.tab.id,boundTabUrl:found.url,boundAuthuser:found.authuser,boundStoreId:found.storeId});
  }
  return found;
}

async function findGeminiTab() {
  const bound = await getBoundGeminiTab();
  return bound.ok ? bound.tab : null;
}

/**
 * Return the one tab that was active when the popup was opened. Some Chrome
 * builds report the extension popup context as `lastFocusedWindow`, so try
 * the popup's current window first and then the last focused normal window.
 * We never query all tabs or choose an inactive Gemini page.
 */
async function getPopupActiveTab() {
  try {
    const tabs = await chrome.tabs.query({ active: true, currentWindow: true });
    if (tabs && tabs[0] && tabs[0].id != null) return tabs[0];
  } catch (e) {}
  try {
    const tabs = await chrome.tabs.query({ active: true, lastFocusedWindow: true });
    if (tabs && tabs[0] && tabs[0].id != null) return tabs[0];
  } catch (e) {}
  try {
    const win = await chrome.windows.getLastFocused({ populate: true });
    if (win && win.type === 'normal') {
      const tab = (win.tabs || []).find((item) => item && item.active && item.id != null);
      if (tab) return tab;
    }
  } catch (e) {}
  return null;
}

/** Bind the explicit popup tab only. This function never navigates or reloads it. */
async function bindCurrentPage(request) {
  if (BUSY) return {ok:false,detail:'Đang đồng bộ; vui lòng chờ rồi liên kết lại'};
  const requestedId = Number(request && (request.tabId || request.tab_id)) || 0;
  let tab = null;
  if (requestedId) {
    try {
      tab = await chrome.tabs.get(requestedId);
    } catch (e) {
      return { ok: false, detail: 'Tab đã đóng; hãy quay lại Gemini và thử lại' };
    }
  } else {
    tab = await getPopupActiveTab();
  }
  const url = String((tab && tab.url) || '');
  if (!tab || tab.id == null) {
    return { ok: false, detail: 'Không tìm được tab hiện tại; hãy mở tiện ích từ tab Gemini' };
  }
  if (!isGeminiUrl(url)) {
    return {
      ok: false,
      detail: 'Tab hiện tại không phải Gemini (địa chỉ: ' + (url || 'không đọc được') + ')',
      url,
      tab_id: tab.id,
    };
  }
  const probe = await probePageIdentity(tab.id);
  if (probe.error) {
    return { ok: false, detail: 'Không đọc được tài khoản trên trang (' + probe.error + '); hãy tải lại tab Gemini và thử lại', url, tab_id: tab.id };
  }
  if (probe.signed_out || !probe.has_token) {
    return { ok: false, detail: 'Trang chưa đăng nhập Gemini (thiếu token phiên); chưa liên kết', url, tab_id: tab.id };
  }
  if (!probe.email) {
    return { ok: false, detail: 'Không xác nhận được email tài khoản; chưa liên kết', url, tab_id: tab.id };
  }
  const resolved = resolveSlot(url, probe);
  if (resolved.source === 'url' && Number.isInteger(probe.slot) && probe.slot !== resolved.slot) {
    return {
      ok: false,
      detail: 'Địa chỉ trang là ' + authuserLabel(resolved.slot) + ', nhưng phiên trên trang là ' +
        authuserLabel(probe.slot) + '; hãy liên kết ở đúng trang của tài khoản',
      url, tab_id: tab.id,
    };
  }
  const authuser = resolved.slot;
  const storeId = await cookieStoreIdForTab(tab.id);
  const patch = {
    boundTabId: tab.id,
    boundTabUrl: url,
    boundAuthuser: authuser,
    boundEmail: probe.email,
    boundStoreId: storeId,
    boundAt: Date.now(),
    lastTabId: tab.id,
    lastTabUrl: url,
  };
  const previous = await rawCfg();
  const list=bindingList(previous);
  const existing=list.findIndex(b=>sameEmail(b.boundEmail,probe.email));
  const entry={...(existing>=0?list[existing]:{}),...patch,maintenanceState:'linked',nextAttemptAt:0};
  if (existing>=0) list[existing]=entry; else list.push(entry);
  const c = await setCfg({...patch,bindings:list},true);
  const page = await inspectBoundPage();
  return {
    ok: true, config: c, page,
    detail: 'Đã liên kết ' + probe.email + ' (' + authuserLabel(authuser) +
      (resolved.source === 'page' ? ', nhận diện từ trang' : '') + ')',
  };
}

async function unbindCurrentPage() {
  if (BUSY) return {ok:false,detail:'Đang đồng bộ; vui lòng chờ rồi hủy liên kết'};
  const previous=await rawCfg();
  const remaining=bindingList(previous).filter(b=>!sameEmail(b.boundEmail,previous.boundEmail));
  const c = await setCfg({
    bindings:remaining,
    boundTabId: 0,
    boundTabUrl: '',
    boundAuthuser: 0,
    boundEmail: '',
    boundStoreId: '',
    boundAt: 0,
    lastTabId: 0,
    lastTabUrl: '',
  });
  return { ok: true, config: c, detail: 'Đã hủy liên kết tab' };
}

/** Read visible identity from the bound page without navigation or reload. */
async function inspectBoundPage() {
  const bound = await getBoundGeminiTab();
  if (!bound.ok) return { ok: false, detail: bound.detail, url: bound.url || '' };
  const probe = await probePageIdentity(bound.tab.id);
  const mismatch = !!(probe.email && bound.email && !sameEmail(probe.email, bound.email));
  return {
    ok: true,
    tab_id: bound.tab.id,
    url: bound.url,
    authuser: bound.authuser,
    store_id: bound.storeId || '',
    email: probe.email || '',
    bound_email: bound.email || '',
    page_slot: Number.isInteger(probe.slot) ? probe.slot : null,
    account_mismatch: mismatch,
    has_token: !!probe.has_token,
    title: probe.title || '',
    signed_out: !!probe.signed_out,
    error: probe.error || null,
  };
}

/** Read the bound page and the shared cookie jar, never refreshing the page. */
async function readCurrentPage() {
  const page = await inspectBoundPage();
  if (!page.ok) {
    await setCfg({ lastReadPageAt: Date.now(), lastReadPageDetail: page.detail || 'Đọc thất bại' });
    return page;
  }
  const map = await readGoogleCookies(page.store_id || '', page.url);
  const logged = isLoggedIn(map);
  const cookie = buildCookieHeader(map);
  const fingerprint = cookie.header ? await sha1Hex(cookie.header) : '';
  const detail = page.signed_out
    ? 'Tab liên kết chưa đăng nhập'
    : page.account_mismatch
      ? 'Tài khoản trên tab đã đổi thành ' + page.email + ' (khi liên kết là ' + page.bound_email + '); hãy liên kết lại'
      : 'Đã đọc tab liên kết, không tải lại; ' + (page.email || 'Không đọc được email từ trang') + ' · ' + authuserLabel(page.authuser);
  await setCfg({ lastReadPageAt: Date.now(), lastReadPageDetail: detail });
  return {
    ok: true,
    page,
    logged_in: logged,
    cookie_count: map.size,
    summary: cookieSummary(map),
    cookie_fingerprint: fingerprint,
    detail,
  };
}
/** 等某个标签页加载完成 (或超时)。 */
function waitTabComplete(tabId, timeoutMs) {
  return new Promise((resolve) => {
    let done = false;
    const finish = () => { if (!done) { done = true; resolve(); } };
    const to = setTimeout(finish, timeoutMs || 30000);
    const onUpd = (id, info) => {
      if (id === tabId && info.status === 'complete') {
        chrome.tabs.onUpdated.removeListener(onUpd);
        clearTimeout(to);
        setTimeout(finish, 1500); // 留一点时间让 Set-Cookie 落盘
      }
    };
    chrome.tabs.onUpdated.addListener(onUpd);
  });
}

/** Refresh only the bound tab. No binding means no navigation and no new tab. */
async function refreshGeminiTab(force) {
  const c = await cfg();
  const cd = cooldownMs(c);
  const last = Number(c.lastRefreshAt) || 0;
  const since = Date.now() - last;
  const bound = await getBoundGeminiTab();
  if (!bound.ok) return { refreshed: false, error: bound.detail, tab: bound.tab };
  if (!force && last && since < cd) {
    return {
      refreshed: false,
      cooldown_left_ms: cd - since,
      tab: bound.tab,
      authuser: bound.authuser,
    };
  }
  const tab = bound.tab;

  // 先记时间戳：整个加载 + 冷却窗口内都不允许再次刷新
  await setCfg({ lastRefreshAt: Date.now() });

  try {
    // The tab was validated above. Reloading a sorry page is still limited to
    // the same tab; we never navigate it to a different account or URL.
    await chrome.tabs.reload(tab.id);
  } catch (e) {
    await setCfg({ lastRefreshDetail: 'Tải lại thất bại: ' + e.message });
    return { refreshed: false, error: 'Tải lại thất bại: ' + e.message, tab };
  }

  await waitTabComplete(tab.id, 30000);
  // 记住这个标签页：下次直接复用它，绝不再新建
  let finalUrl = '';
  try { const t = await chrome.tabs.get(tab.id); finalUrl = (t && t.url) || ''; } catch (e) {}
  const sorry = SORRY_URL_RE.test(finalUrl);
  const finalSlot = authuserFromUrl(finalUrl);
  if (!sorry && finalSlot !== null && finalSlot !== bound.authuser) {
    await setCfg({ lastRefreshDetail: 'Chỉ số tài khoản đổi sau khi tải lại; dừng tự động' });
    return { refreshed: false, error: 'Chỉ số tài khoản đổi sau khi tải lại; hãy liên kết lại', tab };
  }
  await setCfg({
    lastTabId: tab.id,
    lastTabUrl: finalUrl,
    lastRefreshDetail: 'Tải lại tab liên kết'
      + (sorry ? ' (chuyển đến trang kiểm tra Google; chỉ đọc cookie)' : '')
      + ' @ ' + new Date().toISOString(),
  });
  log('refresh bound gemini tab', tab.id, '(reload)',
      sorry ? '[trang kiểm tra Google]' : '');
  return { refreshed: true, created: false, sorry, tab, authuser: bound.authuser };
}

/**
 * 在冷却窗口内反复提取 cookie：刷新后 Google 需要几秒才把新的
 * __Secure-1PSIDTS 写回 cookie store，所以这里以 3 秒为间隔轮询，
 * 直到拿到完整登录 cookie 或窗口耗尽。整个过程中不做任何导航。
 */
async function collectWithinCooldown(maxMs, storeId, scopedUrl) {
  const budget = Math.max(3000, maxMs || 60000);
  const deadline = Date.now() + budget;
  let map = await readGoogleCookies(storeId, scopedUrl);
  if (isLoggedIn(map)) return { map, waited_ms: 0, exhausted: false };
  while (Date.now() < deadline) {
    await sleep(3000);
    map = await readGoogleCookies(storeId, scopedUrl);   // 每次调用都会刷新 SW 的空闲计时
    if (isLoggedIn(map)) {
      return { map, waited_ms: budget - Math.max(0, deadline - Date.now()), exhausted: false };
    }
  }
  return { map, waited_ms: budget, exhausted: true };
}

/**
 * 保活：刷新 gemini 页面 (受 120 秒冷却约束)，并在页面里探活，
 * 让服务端下发新的 __Secure-1PSIDTS/SIDCC (浏览器会自动写回 cookie store)。
 */
async function keepalive(manual, _internal) {
  if ((await cfg()).maintenanceMode === 'server') return {ok:false,skipped:true,detail:'VPS tự duy trì. Dùng nút Duy trì hoạt động trong trang quản trị để kiểm tra gia hạn.'};
  const c = await cfg();
  if (!c.enabled && !manual) return { ok: false, detail: 'Tiện ích đã tắt' };
  const own = !_internal;
  if (own && !acquireBusy()) return { ok: false, detail: 'Tác vụ trước vẫn đang chạy' };
  try {
    // 注意：即便手动触发也不绕过 120 秒冷却，否则连续点按会连环刷新页面
    const r = await refreshGeminiTab(false);
    const tab = r.tab || (await findGeminiTab());
    if (!tab) return { ok: false, detail: r.error || 'Không có tab Gemini khả dụng' };

    let probe = null;
    if (r.refreshed) {
      try {
        const res = await executeTabCode(tab.id, `(() => {
          const html = document.documentElement.innerHTML || '';
          const m = html.match(/SNlM0e\\s*[:=]\\s*"(.*?)"/) || html.match(/SNlM0e["']?\\s*:\\s*"([^"]+)"/);
          const txt = (document.body && document.body.innerText || '').slice(0, 600);
          return {
            url: location.href,
            title: document.title,
            snlm0e: !!m,
            signed_out: /Sign in to save activity|^Sign in$/m.test(txt),
          };
        })()`);
        probe = res && res[0];
      } catch (e) {
        probe = { error: e.message };
      }
    }

    const bound = await getBoundGeminiTab();
    const map = bound.ok ? await readGoogleCookies(bound.storeId, bound.url) : new Map();
    const logged = isLoggedIn(map);
    const detail = (r.refreshed
      ? 'Đã tải lại trang'
      : 'Đang chờ (còn ' + Math.ceil((r.cooldown_left_ms || 0) / 1000) + ' giây; không tải lại)')
      + '; ' + (logged ? 'Phiên hợp lệ' : 'Chưa tìm thấy đủ cookie đăng nhập');
    const out = { ok: logged, refreshed: !!r.refreshed, logged_in: logged, probe, detail };
    await setCfg({ lastKeepaliveAt: Date.now(), lastKeepaliveDetail: detail });
    log('keepalive', out);
    if (logged) {
      const synced=await sync('keepalive',true);
      out.ok=!!synced.ok;
      out.detail=synced.detail || detail;
    }
    return out;
  } finally {
    if (own) releaseBusy();
  }
}

// ---------------------------------------------------------------- sync

/**
 * 把当前 cookie 推送到控制器 (由控制器写入容器内 cookie 池)。
 *
 * 节奏 (风控友好)：
 *   1) 距上次刷新 ≥ 120s → 刷新一次页面，然后在 120s 冷却窗口里轮询提取; 
 *   2) 距上次刷新 < 60s → 完全不导航，直接提取当前 cookie; 
 *   3) 提取不到登录 cookie 时，直接跳过入池 (不反复刷新)。
 */
async function sync(reason, _internal) {
  const c = await cfg();
  if (!c.enabled && reason !== 'manual' && reason !== 'host') {
    return { ok: false, detail: 'Tiện ích đã tắt' };
  }
  const bound = await getBoundGeminiTab();
  if (!bound.ok) {
    const detail = bound.detail || 'Chưa liên kết Gemini; tạm nghỉ';
    await setCfg({ lastSyncAt: Date.now(), lastSyncOk: false, lastSyncDetail: detail });
    return { ok: false, detail };
  }
  const own = !_internal;                 // 内部调用 (keepalive)不重复加锁
  if (own) {
    if (!acquireBusy()) return { ok: false, detail: 'Tác vụ trước vẫn đang chạy' };
  }
  try {
    const cd = cooldownMs(c);
    const last = Number(c.lastRefreshAt) || 0;
    const since = Date.now() - last;
    let refreshed = false;
    let map = await readGoogleCookies(bound.storeId, bound.url);
    let logged = isLoggedIn(map);

    if (c.maintenanceMode !== 'server' && (!logged || since >= cd)) {
      // 冷却已过 (或当前读不到登录 cookie)：允许刷新一次
      // 一律不 force：手动触发也受 120 秒冷却约束，防止连点造成连环刷新
      const r = await refreshGeminiTab(false);
      refreshed = !!r.refreshed;   // 冷却未过时 r.refreshed=false，只提取不导航
      if (refreshed) {
        const got = await collectWithinCooldown(cd, bound.storeId, bound.url);
        map = got.map;
        logged = isLoggedIn(map);
        if (got.exhausted && !logged) log('Hết thời gian chờ nhưng chưa đọc được cookie đăng nhập');
      } else {
        map = await readGoogleCookies(bound.storeId, bound.url);
        logged = isLoggedIn(map);
      }
    } else {
      log('Đang chờ, đọc cookie và bỏ qua tải lại (còn ' +
          Math.ceil((cd - since) / 1000) + 's)');
    }

    if (!map.size) return { ok: false, detail: 'Không đọc được cookie Google' };
    if (!logged) {
      const r = { ok: false, detail: 'Chưa đăng nhập (thiếu SID/SAPISID/__Secure-1PSID); bỏ qua thêm vào pool' };
      await setCfg({ lastSyncAt: Date.now(), lastSyncOk: false, lastSyncDetail: r.detail });
      return r;
    }

    const current = await getBoundGeminiTab();
    if (!current.ok || current.tab.id !== bound.tab.id || current.authuser !== bound.authuser) {
      return { ok: false, detail: 'Tab liên kết đã đổi; chưa thêm vào pool. Hãy liên kết lại' };
    }
    const identity = await probePageIdentity(bound.tab.id);
    if (!bound.email) {
      const r = { ok: false, detail: 'Liên kết cũ thiếu email; hãy bấm Liên kết tab lại' };
      await setCfg({ lastSyncAt: Date.now(), lastSyncOk: false, lastSyncDetail: r.detail });
      return r;
    }
    if (!identity.email) {
      const r = { ok: false, detail: 'Không đọc được tài khoản trên tab liên kết (' + (identity.error || 'Không đọc được email từ trang') + '); chưa thêm vào pool' };
      await setCfg({ lastSyncAt: Date.now(), lastSyncOk: false, lastSyncDetail: r.detail });
      return r;
    }
    if (!sameEmail(identity.email, bound.email)) {
      const r = { ok: false, detail: 'Tài khoản trên tab đã đổi thành ' + identity.email + '; chưa thêm vào pool. Hãy liên kết lại' };
      await setCfg({ lastSyncAt: Date.now(), lastSyncOk: false, lastSyncDetail: r.detail });
      return r;
    }
    const { header } = buildCookieHeader(map);
    const sapisid = (map.get('SAPISID') || {}).value || '';
    const ts = Math.floor(Date.now() / 1000);
    const origin = 'https://gemini.google.com';
    const sapisidhash = sapisid
      ? ts + '_' + (await sha1Hex(ts + ' ' + sapisid + ' ' + origin))
      : '';
    const profile = await profileName();

    const payload = {
      // Google 多账号共用 cookie jar，真正决定账号的是绑定页的 URL 槽位。
      account: String(bound.authuser),
      page_authuser: bound.authuser,
      bound_tab_id: bound.tab.id,
      bound_url: bound.url,
      // Server re-checks that /u/<page_authuser>/app really is this account.
      account_email: bound.email,
      profile,
      verify_model: false,
      maintenance_mode: c.maintenanceMode === 'server' ? 'server' : 'browser',
      reason: reason || 'auto',
      url: bound.url,
      ts,
      cookie: header,
      sapisidhash,
      refreshed,
      rotated: Date.now() - (Number(c.lastRotateAt) || 0) < 120000,
      logged_in: true,
      user_agent: navigator.userAgent,
      summary: cookieSummary(map),
      client: CLIENT,
    };

    // ── 推送目标 (两种模式，2026-09-17 新增远程模式)────────────────────
    //
    // controller 模式 (默认)：推给本机/同网的 Chromium 控制器
    //   http://<controller>:9280/cookie-sync (无鉴权，靠内网限制)
    //
    // service 模式 (远程服务器)：直推 gemini-web2api 服务端
    //   http://<server>:8083/api/browser/ingest (Bearer API key 鉴权)
    //   用于「浏览器在你自己电脑、服务在远端服务器」的场景 —— 这是
    //   远程接入的**唯一可行路径**：控制器只监听服务器内网，外部够不着。
    const mode = (c.pushMode === 'service') ? 'service' : 'controller';
    const base = (c.controller || '').replace(/\/+$/, '');
    const url = mode === 'service' ? (base + '/api/browser/ingest') : (base + '/cookie-sync');
    let r;
    try {
      const headers = { 'Content-Type': 'application/json' };
      if (mode === 'service') {
        headers['X-GW2A-Ext-Mode'] = mode;
        if (c.token) headers['Authorization'] = 'Bearer ' + c.token;
      } else if (c.token) {
        headers['X-GW2A-Ext-Mode'] = mode;
        headers['X-GW2A-Token'] = c.token;
      }
      const resp = await fetch(url, { method: 'POST', headers, body: JSON.stringify(payload) });
      const text = await resp.text();
      let body = null;
      try { body = JSON.parse(text); } catch (e) { body = { raw: text }; }
      r = { ok: resp.ok && !(body && (body.error || body.ok === false || (body.verify && body.verify.ok === false))), http: resp.status, body, mode };
      if (!r.ok) {
        const why = formatServerDetail(body, resp.status);
        r.detail = (mode === 'service' ? 'Máy chủ trả về ' : 'Bộ điều khiển trả về ') + resp.status + ' ' + why;
      } else {
        r.detail = formatServerDetail(body, resp.status);
        // 服务端会在入库后用默认模型校验，把结论带回面板
        if (body && body.verify && body.verify.ok === false) {
          r.detail += ' · Kiểm tra chưa đạt: ' + (body.verify.detail || '');
        }
      }
    } catch (e) {
      r = { ok: false, detail: 'Gửi thất bại: ' + e.message + ' (đích ' + url + ')' };
    }

    await setCfg({
      lastSyncAt: Date.now(),
      lastSyncOk: !!r.ok,
      lastSyncDetail: r.detail || '',
    });
    log('sync', r);
    return r;
  } finally {
    if (own) releaseBusy();
  }
}

// ---------------------------------------------------------------- status + alarms

/** Send a lightweight liveness report. This never refreshes or navigates the
 * bound page; it only reads the bound store and tells the server the mode. */
async function reportExtensionStatus(detail) {
  const c = await cfg();
  if (!c.enabled) return { ok: false, detail: 'Tiện ích đã tắt' };
  // Liveness must not depend on a binding: an unbound extension is still
  // alive, it just has nothing to capture. Report it as logged_in=false.
  const bound = await getBoundGeminiTab();
  const map = bound.ok ? await readGoogleCookies(bound.storeId, bound.url) : new Map();
  const mode = c.pushMode === 'service' ? 'service' : 'controller';
  const base = (c.controller || '').replace(/\/+$/, '');
  const url = mode === 'service'
    ? base + '/api/browser/extension-status'
    : base + '/extension-status';
  const headers = { 'Content-Type': 'application/json', 'X-GW2A-Ext-Mode': mode };
  if (mode === 'service' && c.token) headers.Authorization = 'Bearer ' + c.token;
  if (mode === 'controller' && c.token) headers['X-GW2A-Token'] = c.token;
  const payload = {
    profile: await profileName(),
    push_mode: mode,
    logged_in: bound.ok && isLoggedIn(map),
    cookie_count: map.size,
    account: bound.ok ? String(bound.authuser) : '',
    page_authuser: bound.ok ? bound.authuser : null,
    bound_tab_id: bound.ok ? bound.tab.id : 0,
    bound_url: bound.ok ? bound.url : '',
    account_email: bound.ok ? (bound.email || '') : '',
    detail: bound.ok ? (detail || 'Trạng thái tiện ích định kỳ') : ((detail || 'Trạng thái tiện ích định kỳ') + ' (' + (bound.detail || 'Chưa liên kết') + ')'),
    client: CLIENT,
  };
  try {
    const resp = await fetch(url, { method: 'POST', headers, body: JSON.stringify(payload) });
    const body = await resp.text();
    if (!resp.ok) return { ok: false, http: resp.status, detail: body.slice(0, 180) };
    return { ok: true, http: resp.status };
  } catch (e) {
    return { ok: false, detail: String((e && e.message) || e) };
  }
}

async function ensureAlarms() {
  const c = await cfg();
  await chrome.alarms.clear(ALARM_KEEPALIVE);
  await chrome.alarms.clear(ALARM_SYNC);
  await chrome.alarms.clear(ALARM_STATUS);
  if (!c.enabled) return;
  if (c.autoKeepalive) {
    chrome.alarms.create(ALARM_KEEPALIVE, {
      delayInMinutes: 0.2,
      periodInMinutes: 1,
    });
  }
  chrome.alarms.create(ALARM_SYNC, {
    delayInMinutes: 1,
    periodInMinutes: Math.max(1, Number(c.autoSyncMin) || 30),
  });
  chrome.alarms.create(ALARM_STATUS, {
    delayInMinutes: 0.5,
    periodInMinutes: 5,
  });
}

chrome.alarms.onAlarm.addListener(async (a) => {
  try {
    if (a.name === ALARM_KEEPALIVE) {
      await runScheduledBinding();
    } else if (a.name === ALARM_SYNC) {
      const c = await cfg();
      // 先刷新 gemini 页面让 Google 轮换会话 cookie，keepalive 内部会紧接着 sync; 
      // 保活关闭时退化为直接 sync。
      if (c.enabled) await runScheduledBinding();
    } else if (a.name === ALARM_STATUS) {
      const r = await reportExtensionStatus('Báo trạng thái định kỳ');
      if (!r.ok && r.detail && !/Tiện ích đã tắt/.test(r.detail)) log('status heartbeat failed', r);
    }
  } catch (e) { log('alarm error', e); }
});

// ---------------------------------------------------------------- lifecycle

chrome.runtime.onInstalled.addListener(async (d) => {
  log('installed', d.reason);
  await ensureAlarms();
  // Installation/reload must never navigate an existing Gemini page. The
  // user explicitly starts capture with bind/read/sync from the popup.
});

chrome.runtime.onStartup.addListener(async () => {
  await ensureAlarms();
  try { await runScheduledBinding(); } catch (e) {}
  try { await reportExtensionStatus('Trạng thái khi khởi động'); } catch (e) {}
});

// gemini 页面加载完成时顺手同步 (登录后立刻入池)。
// 注意：sync 内部有 60 秒冷却保护，这里不会引起连环刷新。
chrome.tabs.onUpdated.addListener(async (tabId, info, tab) => {
  if (info.status !== 'complete') return;
  const current=await rawCfg();
  if (current.maintenanceMode === 'server') return;
  const saved=bindingList(current).find(b=>b.boundTabId===tabId);
  if (!saved || !tab) return;
  if (!isGeminiUrl(tab.url || '')) return;
  if (BUSY) return;                       // 自己刷新引起的加载，直接忽略
  try {
    const c = await cfg();
    if (!c.enabled) return;
    if (!saved.nextAttemptAt || saved.nextAttemptAt<=Date.now()) await runBinding(saved,'sync',false);
  } catch (e) { log('onUpdated sync error', e); }
});

chrome.runtime.onMessage.addListener((msg, sender, sendResponse) => {
  (async () => {
    try {
      if (BUSY && msg?.type !== 'status') {
        sendResponse({ok:false,detail:'Đang duy trì một tài khoản; vui lòng thử lại sau'});
        return;
      }
      switch (msg && msg.type) {
        case 'status': {
          const raw = await rawCfg();
          const selected=bindingList(raw).find(b=>sameEmail(b.boundEmail,raw.boundEmail));
          const c = selected ? {...raw,...selected} : raw;
          const bound = await resolveSavedBinding(c,true);
          const map = bound.ok ? await readGoogleCookies(bound.storeId, bound.url) : new Map();
          const last = Number(c.lastRefreshAt) || 0;
          const cd = cooldownMs(c);
          sendResponse({
            ok: true, config: c, logged_in: isLoggedIn(map),
            incognito_allowed: await new Promise((resolve) => {
              try {
                if (chrome.extension && chrome.extension.isAllowedIncognitoAccess) {
                  chrome.extension.isAllowedIncognitoAccess((v) => resolve(!!v));
                } else resolve(true);
              } catch (e) { resolve(true); }
            }),
            cookie_count: map.size, summary: cookieSummary(map),
            cooldown_left_ms: Math.max(0, cd - (Date.now() - last)),
            bindings: bindingList(await rawCfg()),
            bound: bound.ok ? {
              tab_id: bound.tab.id,
              url: bound.url,
              authuser: bound.authuser,
              store_id: bound.storeId || '',
              email: bound.email || '',
              detail: 'Đã liên kết ' + (bound.email ? bound.email + ' · ' : '') + authuserLabel(bound.authuser),
            } : { tab_id: 0, url: '', authuser: null, detail: bound.detail },
          });
          break;
        }
        case 'setConfig':
          sendResponse({ ok: true, config: await setCfg(msg.patch || {},true) });
          await ensureAlarms();
          break;
        case 'keepalive':
          { const c=await rawCfg();const item=bindingList(c).find(b=>sameEmail(b.boundEmail,c.boundEmail));
            sendResponse(item ? await runBinding(item,'keepalive',true) : {ok:false,detail:'Chưa chọn tài khoản liên kết'}); }
          break;
        case 'selectBinding':
          sendResponse(await selectBinding(msg.email));
          break;
        case 'bindOpenTabs':
          sendResponse(await bindOpenTabs());
          break;
        case 'syncAll':
          sendResponse(await syncAllBindings());
          break;
        case 'bindCurrentPage':
          sendResponse(await bindCurrentPage(msg));
          break;
        case 'unbindCurrentPage':
          sendResponse(await unbindCurrentPage());
          break;
        case 'readPage':
          sendResponse(await readCurrentPage());
          break;
        case 'sync':
          sendResponse(await sync('manual'));
          break;
        case 'syncStatus':
          sendResponse(await reportExtensionStatus('Gửi trạng thái thủ công'));
          break;
        default:
          sendResponse({ ok: false, detail: 'unknown message ' + (msg && msg.type) });
      }
    } catch (e) {
      sendResponse({ ok: false, detail: String((e && e.message) || e) });
    }
  })();
  return true; // async response
});

ensureAlarms().catch((e) => log('ensureAlarms failed', e));

// 宿主页 (控制器托管的引导页)远程控制入口：
// chrome.runtime.sendMessage(EXT_ID, {...}) 即可触发保活/入池，无需用户点击。
