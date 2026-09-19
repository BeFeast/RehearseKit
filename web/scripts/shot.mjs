#!/usr/bin/env node
// Full-page screenshots of every SPA route at 1280 and 390, light and dark,
// through a headless Chrome's CDP endpoint (docker rk-chrome, host network).
//
// usage: node web/scripts/shot.mjs <cdp-endpoint> <base-url> <out-dir> [--login email:password] [--job <id>] [--anon-job <id>]
//        [--only <name,name>] [--wait <ms>]
//
// Example:
//   node web/scripts/shot.mjs http://127.0.0.1:19222 http://127.0.0.1:8080 /var/tmp/spa-shots \
//     --login admin@example.com:change-me-please --job aaaaaaaa-... --anon-job aaaaaaaa-...
//
// One tab is opened per route; the session cookie is set through the page's
// own fetch('/api/v1/auth/login') so signed-in screens work without a UI
// round-trip.
import fs from 'node:fs';
import path from 'node:path';

const [endpoint, base, outDir, ...rest] = process.argv.slice(2);
if (!endpoint || !base || !outDir) {
  console.error('usage: shot.mjs <cdp-endpoint> <base-url> <out-dir> [--login email:password] [--job id] [--anon-job id] [--only a,b] [--wait ms]');
  process.exit(2);
}
const opt = (k, d) => {
  const i = rest.indexOf(k);
  return i >= 0 ? rest[i + 1] : d;
};
const login = opt('--login', null);
const jobId = opt('--job', null);
const anonJobId = opt('--anon-job', null);
const processingId = opt('--processing-job', null);
const failedId = opt('--failed-job', null);
const only = opt('--only', null)?.split(',');
const WAIT = Number(opt('--wait', 2500));
fs.mkdirSync(outDir, { recursive: true });

const routes = [
  { name: 'landing', path: '/', auth: false },
  { name: 'landing-signed-in', path: '/', auth: true },
  { name: 'jobs-list', path: '/jobs', auth: true },
  { name: 'jobs-list-loading', path: '/jobs', auth: true, wait: 250 },
  { name: 'sign-in', path: '/jobs', auth: false, after: `document.querySelector('[data-testid="sign-in-button"]')?.click()` },
  ...(jobId ? [{ name: 'job-detail-completed', path: `/jobs/${jobId}`, auth: true, wait: 4000 }] : []),
  ...(jobId ? [{ name: 'job-detail-solo', path: `/jobs/${jobId}`, auth: true, wait: 4000, after: `window.__rk?.toggleSolo('drums'); window.__rk?.toggleMute('other'); window.__rk?.dispatch({type:'select', stem:'drums'})` }] : []),
  ...(jobId ? [{ name: 'job-detail-loading', path: `/jobs/${jobId}`, auth: true, wait: 120 }] : []),
  ...(anonJobId ? [{ name: 'job-detail-anonymous', path: `/jobs/${anonJobId}`, auth: false, wait: 4000 }] : []),
  ...(processingId ? [{ name: 'job-detail-processing', path: `/jobs/${processingId}`, auth: true }] : []),
  ...(failedId ? [{ name: 'job-detail-failed', path: `/jobs/${failedId}`, auth: true }] : []),
  { name: 'pending-approval', path: '/pending-approval', auth: false, before: `sessionStorage.setItem('rk.pendingEmail','priya@example.com')` },
  { name: 'profile', path: '/profile', auth: true },
  { name: 'admin-users', path: '/admin/users', auth: true },
  { name: 'not-found', path: '/no/such/page', auth: false },
];

const viewports = [
  { tag: '1280', width: 1280, mobile: false },
  { tag: '390', width: 390, mobile: true },
];
const themes = ['light', 'dark'];

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function openTab(url) {
  const t = await (await fetch(`${endpoint}/json/new?${encodeURIComponent(url)}`, { method: 'PUT' })).json();
  const ws = new WebSocket(t.webSocketDebuggerUrl);
  await new Promise((r) => (ws.onopen = r));
  let id = 0;
  const p = new Map();
  const send = (m, a = {}) =>
    new Promise((res, rej) => {
      const i = ++id;
      p.set(i, { res, rej });
      ws.send(JSON.stringify({ id: i, method: m, params: a }));
    });
  const errors = [];
  ws.onmessage = ({ data }) => {
    const m = JSON.parse(data);
    if (m.id) {
      const q = p.get(m.id);
      p.delete(m.id);
      m.error ? q.rej(new Error(m.error.message)) : q.res(m.result);
      return;
    }
    if (m.method === 'Runtime.exceptionThrown') {
      errors.push((m.params.exceptionDetails.exception?.description ?? m.params.exceptionDetails.text).split('\n')[0]);
    } else if (m.method === 'Runtime.consoleAPICalled' && m.params.type === 'error') {
      errors.push(m.params.args.map((a) => a.value ?? a.description ?? a.type).join(' ').split('\n')[0]);
    }
  };
  const evaluate = async (expression) => {
    const r = await send('Runtime.evaluate', { expression, returnByValue: true, awaitPromise: true });
    if (r.exceptionDetails) throw new Error(r.exceptionDetails.exception?.description ?? r.exceptionDetails.text);
    return r.result.value;
  };
  // Page.close tears the target down before it can answer, so do not wait on it.
  const close = async () => {
    await Promise.race([send('Page.close').catch(() => {}), sleep(400)]);
    ws.close();
  };
  return { send, evaluate, close, errors };
}

// Establish (or clear) the session once per auth mode in a scratch tab; the
// cookie is per browser profile so later tabs inherit it.
async function setAuth(auth) {
  const tab = await openTab(`${base}/healthz`);
  await tab.send('Page.enable');
  await sleep(400);
  if (auth && login) {
    const [email, password] = login.split(':');
    const r = await tab.evaluate(`fetch('/api/v1/auth/login',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({email:${JSON.stringify(email)},password:${JSON.stringify(password)}})}).then(r=>r.status)`);
    if (r !== 200) console.warn('login status', r);
  } else {
    await tab.evaluate(`fetch('/api/v1/auth/logout',{method:'POST'}).then(r=>r.status)`);
  }
  await tab.close();
}

let currentAuth = null;
for (const route of routes) {
  if (only && !only.includes(route.name)) continue;
  if (route.auth && !login) continue;
  if (currentAuth !== route.auth) {
    await setAuth(route.auth);
    currentAuth = route.auth;
  }
  for (const theme of themes) {
    for (const vp of viewports) {
      const file = path.join(outDir, `${route.name}-${vp.tag}-${theme}.png`);
      const tab = await openTab('about:blank');
      try {
        await tab.send('Page.enable');
        await tab.send('Runtime.enable');
        await tab.send('Emulation.setDeviceMetricsOverride', { width: vp.width, height: 900, deviceScaleFactor: 1, mobile: vp.mobile });
        // Theme + any per-route storage before the app boots.
        await tab.send('Page.addScriptToEvaluateOnNewDocument', {
          source: `try{localStorage.setItem('rk.theme',${JSON.stringify(theme)});${route.before ?? ''}}catch(e){}`,
        });
        await tab.send('Page.navigate', { url: `${base}${route.path}` });
        await sleep(route.wait ?? WAIT);
        if (route.after) {
          await tab.evaluate(route.after);
          await sleep(600);
        }
        const dims = JSON.parse(
          await tab.evaluate(`JSON.stringify({h: document.documentElement.scrollHeight, w: document.documentElement.scrollWidth, title: document.title})`),
        );
        await tab.send('Emulation.setDeviceMetricsOverride', { width: vp.width, height: Math.min(Math.max(dims.h, 700), 4000), deviceScaleFactor: 1, mobile: vp.mobile });
        await sleep(150);
        const { data } = await tab.send('Page.captureScreenshot', { format: 'png', captureBeyondViewport: true });
        fs.writeFileSync(file, Buffer.from(data, 'base64'));
        console.log(`${file}  (${dims.w}x${dims.h}) ${dims.title}`);
        for (const e of tab.errors) console.log(`    console/exception: ${e.slice(0, 300)}`);
      } catch (err) {
        console.error(`${file}: ${err.message}`);
      } finally {
        await tab.close();
      }
    }
  }
}
