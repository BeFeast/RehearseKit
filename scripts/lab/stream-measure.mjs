#!/usr/bin/env node
// Drive the streaming playback lab in a headless Chrome over CDP and record
// underruns, memory and transport timing.
//
// usage: node scripts/lab/stream-measure.mjs <cdp-endpoint> <lab-url> [options]
//   --duration <s>        total observation time after Play (default 60)
//   --poll <s>            sampling interval (default 5)
//   --seek <sec>@<t>      seek to <sec> at t seconds after Play (repeatable)
//   --loop <a>,<b>@<t>    set loop [a,b) seconds at t (repeatable)
//   --clear-loop@<t>      clear the loop at t (repeatable)
//   --seek-end@<t>        seek to the end at t
//   --stop@<t> / --play@<t>
//   --shot <file.png>     screenshot at the end
//   --json <file>         write all samples + summary as JSON
//
// Example headless Chrome: docker run -d --network host chromedp/headless-shell:latest \
//   --remote-debugging-port=19222 --remote-debugging-address=127.0.0.1 \
//   --autoplay-policy=no-user-gesture-required --no-sandbox
import fs from 'node:fs';

const [endpoint, url, ...rest] = process.argv.slice(2);
if (!endpoint || !url) {
  console.error('usage: stream-measure.mjs <cdp-endpoint> <lab-url> [options]');
  process.exit(2);
}
const opt = (k, d) => {
  const i = rest.indexOf(k);
  return i >= 0 ? rest[i + 1] : d;
};
const DURATION = Number(opt('--duration', 60));
const POLL = Number(opt('--poll', 5));
const SHOT = opt('--shot', null);
const JSON_OUT = opt('--json', null);

const actions = [];
for (let i = 0; i < rest.length; i++) {
  const a = rest[i];
  const at = (s) => Number(s.split('@')[1]);
  if (a === '--seek') actions.push({ at: at(rest[i + 1]), kind: 'seek', arg: Number(rest[i + 1].split('@')[0]) });
  else if (a === '--loop') {
    const [range, t] = rest[i + 1].split('@');
    const [s, e] = range.split(',').map(Number);
    actions.push({ at: Number(t), kind: 'loop', arg: [s, e] });
  } else if (a.startsWith('--clear-loop@')) actions.push({ at: at(a), kind: 'clear-loop' });
  else if (a.startsWith('--seek-end@')) actions.push({ at: at(a), kind: 'seek-end' });
  else if (a.startsWith('--stop@')) actions.push({ at: at(a), kind: 'stop' });
  else if (a.startsWith('--play@')) actions.push({ at: at(a), kind: 'play' });
}
actions.sort((a, b) => a.at - b.at);

const res = await fetch(`${endpoint}/json/new?${encodeURIComponent(url)}`, { method: 'PUT' });
const target = await res.json();
const ws = new WebSocket(target.webSocketDebuggerUrl);
await new Promise((r) => (ws.onopen = r));
let id = 0;
const pending = new Map();
const send = (method, params = {}) =>
  new Promise((resolve, reject) => {
    const mid = ++id;
    pending.set(mid, { resolve, reject });
    ws.send(JSON.stringify({ id: mid, method, params }));
  });
const t0 = Date.now();
const ts = () => `[${((Date.now() - t0) / 1000).toFixed(1)}s]`;
const consoleErrors = [];
ws.onmessage = ({ data }) => {
  const msg = JSON.parse(data);
  if (msg.id) {
    const p = pending.get(msg.id);
    pending.delete(msg.id);
    msg.error ? p.reject(new Error(msg.error.message)) : p.resolve(msg.result);
    return;
  }
  if (msg.method === 'Runtime.consoleAPICalled' && ['error', 'warning'].includes(msg.params.type)) {
    const text = msg.params.args.map((a) => a.value ?? a.description ?? a.type).join(' ');
    consoleErrors.push(text);
    console.log(ts(), `console.${msg.params.type}:`, text.slice(0, 300));
  } else if (msg.method === 'Runtime.exceptionThrown') {
    const d = msg.params.exceptionDetails;
    const text = d.exception?.description ?? d.text;
    consoleErrors.push(text);
    console.log(ts(), 'EXCEPTION:', text.slice(0, 400));
  } else if (msg.method === 'Inspector.targetCrashed') {
    console.log(ts(), '!!! TARGET CRASHED');
    process.exit(3);
  }
};
await send('Runtime.enable');
await send('Page.enable');
await send('Inspector.enable');

const evaluate = async (expression) => {
  const r = await send('Runtime.evaluate', { expression, returnByValue: true, awaitPromise: true });
  if (r.exceptionDetails) throw new Error(r.exceptionDetails.exception?.description ?? r.exceptionDetails.text);
  return r.result.value;
};
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const waitFor = async (expression, timeoutMs, label) => {
  const start = Date.now();
  while (Date.now() - start < timeoutMs) {
    if (await evaluate(expression)) return;
    await sleep(200);
  }
  throw new Error(`timeout waiting for ${label}`);
};
const click = (testid) => evaluate(`(() => { const b = document.querySelector('[data-testid="${testid}"]'); if (!b) return 'missing'; b.click(); return 'ok'; })()`);

const SAMPLE = `(async () => {
  const s = window.__labStream?.stats() ?? null;
  const m = await window.__labStream?.memory();
  const e = window.__labStream?.engine;
  return {
    state: s?.state, position: e?.position, duration: e?.duration, underruns: s?.underruns, quanta: s?.quanta,
    requests: s?.requests, bytesMB: s ? +(s.bytes / 1048576).toFixed(1) : null, lastSeekMs: s?.lastSeekMs,
    ringFill: s?.ringFill?.map((x) => Math.round(x * 100)), maxFetchMs: s ? Math.max(...s.streams.map((x) => x.maxFetchMs)) : null,
    inFlight: s ? s.streams.filter((x) => x.inFlight).length : null, error: s?.error ?? null,
    jsHeapMB: m?.jsHeapUsedMB && +m.jsHeapUsedMB.toFixed(1), uaMB: m?.uaMemoryMB && +m.uaMemoryMB.toFixed(1),
    isolated: crossOriginIsolated,
  };
})()`;

await waitFor(`!!document.querySelector('[data-testid="lab-load"]')`, 30000, 'lab page');
console.log(ts(), 'isolated:', await evaluate('crossOriginIsolated'));
console.log(ts(), 'load:', await click('lab-load'));
await waitFor(`!!(window.__labStream && window.__labStream.engine) || !!document.querySelector('[data-testid="lab-error"]')`, 30000, 'engine');
const loadError = await evaluate(`document.querySelector('[data-testid="lab-error"]')?.textContent ?? null`);
if (loadError) {
  console.log(ts(), 'LOAD ERROR:', loadError);
  process.exit(4);
}
console.log(ts(), 'baseline:', await evaluate(`document.querySelector('[data-testid="lab-baseline"]')?.textContent`));
const playAt = Date.now();
console.log(ts(), 'play:', await click('lab-play'));
await waitFor(`window.__labStream.stats()?.state === 'playing'`, 15000, 'playing');
console.log(ts(), `first sound after ${Date.now() - playAt} ms (engine lastSeekMs=${await evaluate('window.__labStream.stats().lastSeekMs')})`);
// Warm-up underruns (before PLAYING) are not counted by design; note the count now.
const warmupUnderruns = await evaluate('window.__labStream.stats().underruns');

const samples = [];
let nextAction = 0;
const startedAt = Date.now();
while ((Date.now() - startedAt) / 1000 < DURATION) {
  await sleep(POLL * 1000);
  const elapsed = (Date.now() - startedAt) / 1000;
  while (nextAction < actions.length && actions[nextAction].at <= elapsed) {
    const a = actions[nextAction++];
    const e = 'window.__labStream.engine';
    let r;
    if (a.kind === 'seek') r = await evaluate(`${e}.seek(${a.arg}), 'seek ${a.arg}'`);
    else if (a.kind === 'seek-end') r = await evaluate(`${e}.seek(${e}.duration), 'seek end'`);
    else if (a.kind === 'loop') r = await evaluate(`${e}.setLoop({start:${a.arg[0]},end:${a.arg[1]}}) ? 'loop ${a.arg[0]}-${a.arg[1]}' : 'loop rejected'`);
    else if (a.kind === 'clear-loop') r = await evaluate(`${e}.setLoop(null), 'loop cleared'`);
    else if (a.kind === 'stop') r = await evaluate(`${e}.stop(), 'stop'`);
    else if (a.kind === 'play') r = await evaluate(`${e}.play(), 'play'`);
    console.log(ts(), 'action:', r);
    await sleep(1500);
    console.log(ts(), 'after action:', JSON.stringify(await evaluate(SAMPLE)));
  }
  const s = await evaluate(SAMPLE);
  s.t = +elapsed.toFixed(1);
  samples.push(s);
  console.log(ts(), JSON.stringify(s));
  if (s.error) {
    console.log(ts(), 'ENGINE ERROR:', s.error);
    break;
  }
}

const summary = {
  url,
  durationS: DURATION,
  warmupUnderruns,
  finalUnderruns: samples.at(-1)?.underruns,
  maxJsHeapMB: Math.max(...samples.map((s) => s.jsHeapMB ?? 0)),
  maxUaMB: Math.max(...samples.map((s) => s.uaMB ?? 0)),
  requests: samples.at(-1)?.requests,
  bytesMB: samples.at(-1)?.bytesMB,
  maxFetchMs: samples.at(-1)?.maxFetchMs,
  finalState: samples.at(-1)?.state,
  finalPosition: samples.at(-1)?.position,
  consoleErrors: consoleErrors.length,
};
console.log(ts(), 'summary', JSON.stringify(summary));
if (SHOT) {
  const { data } = await send('Page.captureScreenshot', { format: 'png' });
  fs.writeFileSync(SHOT, Buffer.from(data, 'base64'));
  console.log(ts(), 'screenshot', SHOT);
}
if (JSON_OUT) fs.writeFileSync(JSON_OUT, JSON.stringify({ summary, samples, consoleErrors }, null, 2));
await send('Page.close').catch(() => {});
ws.close();
