#!/usr/bin/env node
// Drive the job-detail mixer in a headless Chrome over CDP and record
// underruns, meter movement and transport behaviour. Adapted from
// scripts/lab/stream-measure.mjs; talks to the SPA through window.__rk
// (the debug hook exported by src/player/use-mixer.ts).
//
// usage: node web/scripts/playback-verify.mjs <cdp-endpoint> <job-url> [options]
//   --duration <s>        observation time after Play (default 60)
//   --poll <s>            sampling interval (default 5)
//   --seek <sec>@<t>      seek to <sec> at t seconds after Play (repeatable)
//   --loop <a>,<b>@<t>    set loop [a,b) seconds at t (repeatable)
//   --loop-off@<t>        disengage the loop at t
//   --home@<t>            return-to-start at t
//   --solo <n>@<t>        toggle solo on stem n (1-based) at t
//   --stop@<t> / --play@<t>
//   --shot <file.png>     screenshot at the end
//   --json <file>         write samples + summary as JSON
//
// The rk-chrome container runs with --autoplay-policy=no-user-gesture-required,
// so Play can be triggered from script.
import fs from 'node:fs';

const [endpoint, url, ...rest] = process.argv.slice(2);
if (!endpoint || !url) {
  console.error('usage: playback-verify.mjs <cdp-endpoint> <job-url> [options]');
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
  else if (a === '--solo') actions.push({ at: at(rest[i + 1]), kind: 'solo', arg: Number(rest[i + 1].split('@')[0]) });
  else if (a === '--loop') {
    const [range, t] = rest[i + 1].split('@');
    const [s, e] = range.split(',').map(Number);
    actions.push({ at: Number(t), kind: 'loop', arg: [s, e] });
  } else if (a.startsWith('--loop-off@')) actions.push({ at: at(a), kind: 'loop-off' });
  else if (a.startsWith('--home@')) actions.push({ at: at(a), kind: 'home' });
  else if (a.startsWith('--stop@')) actions.push({ at: at(a), kind: 'stop' });
  else if (a.startsWith('--play@')) actions.push({ at: at(a), kind: 'play' });
}
actions.sort((a, b) => a.at - b.at);

const res = await fetch(`${endpoint}/json/new?${encodeURIComponent(url)}`, { method: 'PUT' });
const target = await res.json();
const ws = new WebSocket(target.webSocketDebuggerUrl);
await new Promise((r) => (ws.onopen = r));

// Close the tab no matter how the run ends. A tab left behind keeps the
// lab page's audio + render loop alive under swiftshader and burns ~10 CPU
// cores until someone notices (maestro, 2026-09-23). The close goes over the
// HTTP endpoint so it works even when the CDP socket is already gone.
class Exit extends Error {
  constructor(code) {
    super(`exit ${code}`);
    this.code = code;
  }
}
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
let closing = false;
const shutdown = async (code) => {
  if (closing) return;
  closing = true;
  await Promise.race([fetch(`${endpoint}/json/close/${target.id}`).catch(() => {}), sleep(1500)]);
  try {
    ws.close();
  } catch {}
  process.exit(code);
};
for (const sig of ['SIGINT', 'SIGTERM']) process.on(sig, () => void shutdown(130));
process.on('uncaughtException', (err) => {
  console.error('FAILED:', err?.stack ?? err);
  void shutdown(1);
});
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
    void shutdown(3);
  }
};
let exitCode = 0;
try {
  await send('Runtime.enable');
  await send('Page.enable');
  await send('Inspector.enable');
  await send('Emulation.setDeviceMetricsOverride', { width: 1280, height: 1400, deviceScaleFactor: 1, mobile: false });

  const evaluate = async (expression) => {
    const r = await send('Runtime.evaluate', { expression, returnByValue: true, awaitPromise: true });
    if (r.exceptionDetails) throw new Error(r.exceptionDetails.exception?.description ?? r.exceptionDetails.text);
    return r.result.value;
  };
  const waitFor = async (expression, timeoutMs, label) => {
    const start = Date.now();
    while (Date.now() - start < timeoutMs) {
      if (await evaluate(expression)) return;
      await sleep(200);
    }
    throw new Error(`timeout waiting for ${label}`);
  };

  const SAMPLE = `(() => {
    const m = window.__rk; if (!m) return null;
    const s = m.stats(); const lv = m.live.current;
    const mem = performance.memory ? +(performance.memory.usedJSHeapSize / 1048576).toFixed(1) : null;
    return {
      state: m.engineState, position: +lv.position.toFixed(2), duration: +m.duration.toFixed(2),
      underruns: s?.underruns ?? null, quanta: s?.quanta ?? null, requests: s?.requests ?? null,
      bytesMB: s ? +(s.bytes / 1048576).toFixed(1) : null, lastSeekMs: s?.lastSeekMs ?? null,
      ringFill: s?.ringFill?.map((x) => Math.round(x * 100)) ?? null,
      maxFetchMs: s ? Math.max(...s.streams.map((x) => x.maxFetchMs)) : null,
      levels: lv.levels.map((x) => +x.toFixed(3)), master: [+lv.master.left.toFixed(3), +lv.master.right.toFixed(3)],
      lcd: document.querySelector('[data-testid="lcd-position"]')?.textContent ?? null,
      loop: m.mix.loop, loopEnabled: m.mix.loopEnabled, solo: m.mix.solo,
      error: m.engineError, jsHeapMB: mem, isolated: crossOriginIsolated,
    };
  })()`;

  await waitFor(`!!window.__rk`, 30000, 'mixer mounted');
  console.log(ts(), 'isolated:', await evaluate('crossOriginIsolated'));
  await waitFor(`!window.__rk.peaksLoading`, 30000, 'peaks');
  console.log(ts(), 'peaks loaded:', await evaluate('Object.keys(window.__rk.peaks).join(",")'));
  const playAt = Date.now();
  console.log(ts(), 'play:', await evaluate(`(() => { document.querySelector('[data-testid="transport-play"]').click(); return 'clicked'; })()`));
  await waitFor(`window.__rk.engineState === 'playing' || window.__rk.engineState === 'error'`, 30000, 'playing');
  const s0 = await evaluate(SAMPLE);
  if (s0.error) {
    console.log(ts(), 'ENGINE ERROR:', s0.error);
    throw new Exit(4);
  }
  console.log(ts(), `first sound after ${Date.now() - playAt} ms (engine lastSeekMs=${s0.lastSeekMs})`);
  const warmupUnderruns = s0.underruns;

  const samples = [];
  let meterFrames = 0;
  let nextAction = 0;
  const startedAt = Date.now();
  while ((Date.now() - startedAt) / 1000 < DURATION) {
    await sleep(POLL * 1000);
    const elapsed = (Date.now() - startedAt) / 1000;
    while (nextAction < actions.length && actions[nextAction].at <= elapsed) {
      const a = actions[nextAction++];
      let r;
      if (a.kind === 'seek') r = await evaluate(`window.__rk.seek(${a.arg}), 'seek ${a.arg}'`);
      else if (a.kind === 'loop') r = await evaluate(`(window.__rk.setLoop(${a.arg[0]}, ${a.arg[1]}), window.__rk.dispatch({type:'loopEnabled', enabled:true}), 'loop ${a.arg[0]}-${a.arg[1]}')`);
      else if (a.kind === 'loop-off') r = await evaluate(`window.__rk.dispatch({type:'loopEnabled', enabled:false}), 'loop off'`);
      else if (a.kind === 'home') r = await evaluate(`window.__rk.returnToStart(), 'home'`);
      else if (a.kind === 'solo') r = await evaluate(`window.__rk.toggleSolo(window.__rk.stems[${a.arg - 1}]), 'solo ${a.arg}'`);
      else if (a.kind === 'stop') r = await evaluate(`window.__rk.stop(), 'stop'`);
      else if (a.kind === 'play') r = await evaluate(`window.__rk.play(), 'play'`);
      console.log(ts(), 'action:', r);
      await sleep(1500);
      console.log(ts(), 'after action:', JSON.stringify(await evaluate(SAMPLE)));
    }
    const s = await evaluate(SAMPLE);
    s.t = +elapsed.toFixed(1);
    if (s.levels.some((x) => x > 0.01)) meterFrames++;
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
    underrunsDuringRun: (samples.at(-1)?.underruns ?? 0) - warmupUnderruns,
    samplesWithMeterMovement: meterFrames,
    samples: samples.length,
    maxJsHeapMB: Math.max(...samples.map((s) => s.jsHeapMB ?? 0)),
    requests: samples.at(-1)?.requests,
    bytesMB: samples.at(-1)?.bytesMB,
    maxFetchMs: samples.at(-1)?.maxFetchMs,
    finalState: samples.at(-1)?.state,
    finalPosition: samples.at(-1)?.position,
    finalLcd: samples.at(-1)?.lcd,
    consoleErrors: consoleErrors.length,
  };
  console.log(ts(), 'summary', JSON.stringify(summary));
  if (SHOT) {
    const { data } = await send('Page.captureScreenshot', { format: 'png' });
    fs.writeFileSync(SHOT, Buffer.from(data, 'base64'));
    console.log(ts(), 'screenshot', SHOT);
  }
  if (JSON_OUT) fs.writeFileSync(JSON_OUT, JSON.stringify({ summary, samples, consoleErrors }, null, 2));
} catch (err) {
  if (err instanceof Exit) exitCode = err.code;
  else {
    console.error(ts(), 'FAILED:', err?.stack ?? err);
    exitCode = 1;
  }
} finally {
  await shutdown(exitCode);
}
