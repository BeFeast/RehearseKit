#!/usr/bin/env node
// Drive the DRUM EDITOR's MIDI KIT audition in a headless Chrome over CDP:
// sign in, open /jobs/<id>?tab=drums, play, switch AUDITION to MIDI KIT,
// solo the drums so only the sample kit reaches the master meter, then
// seek and loop and count what the scheduler queued. Talks to the SPA through
// window.__rk (use-mixer), window.__rkDrums (use-drum-editor) and
// window.__rkKit (use-kit-audition).
//
// usage: node web/scripts/kit-verify.mjs <cdp-endpoint> <base-url> <job-id> --login email:password [--at <sec>] [--json <file>]
const [endpoint, base, jobId, ...rest] = process.argv.slice(2);
if (!endpoint || !base || !jobId) {
  console.error('usage: kit-verify.mjs <cdp-endpoint> <base-url> <job-id> --login email:password [--at sec] [--json file]');
  process.exit(2);
}
const opt = (k, d) => {
  const i = rest.indexOf(k);
  return i >= 0 ? rest[i + 1] : d;
};
const login = opt('--login', null);
const AT = Number(opt('--at', 60));
const JSON_OUT = opt('--json', null);
import fs from 'node:fs';

// Sign in from a scratch tab (the cookie is per browser profile), then open
// the job page in a fresh tab: a COOP/COEP page must be loaded as the
// tab's first document to be cross-origin isolated for SharedArrayBuffer.
if (login) {
  const scratch = await (await fetch(`${endpoint}/json/new?${encodeURIComponent(base + '/healthz')}`, { method: 'PUT' })).json();
  const sws = new WebSocket(scratch.webSocketDebuggerUrl);
  await new Promise((r) => (sws.onopen = r));
  const [email, password] = login.split(':');
  const expr = `fetch('/api/v1/auth/login',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({email:${JSON.stringify(email)},password:${JSON.stringify(password)}})}).then(r=>r.status)`;
  await new Promise((r) => setTimeout(r, 400));
  const st = await new Promise((resolve) => {
    sws.onmessage = ({ data }) => {
      const m = JSON.parse(data);
      if (m.id === 1) resolve(m.result?.result?.value);
    };
    sws.send(JSON.stringify({ id: 1, method: 'Runtime.evaluate', params: { expression: expr, returnByValue: true, awaitPromise: true } }));
  });
  console.log('login', st);
  sws.close();
  await fetch(`${endpoint}/json/close/${scratch.id}`).catch(() => {});
}
const res = await fetch(`${endpoint}/json/new?${encodeURIComponent(`${base}/jobs/${jobId}?tab=drums`)}`, { method: 'PUT' });
const target = await res.json();
const ws = new WebSocket(target.webSocketDebuggerUrl);
await new Promise((r) => (ws.onopen = r));
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
  }
};
let exitCode = 0;
const out = { steps: [], consoleErrors };
try {
  await send('Runtime.enable');
  await send('Page.enable');
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
  await waitFor(`!!window.__rkDrums && window.__rkDrums.handle.status === 'ready'`, 30000, 'drum editor');
  console.log(ts(), 'isolated:', await evaluate('crossOriginIsolated'));
  await waitFor(`!!window.__rk && !window.__rk.peaksLoading`, 30000, 'peaks');
  const hits = await evaluate(`window.__rkDrums.handle.state.hits.length`);
  console.log(ts(), 'editor ready, hits', hits, 'tab', await evaluate(`document.querySelector('[data-testid="tab-drums"]')?.getAttribute('aria-selected')`));
  // Start from a known mix state whatever the previous run left behind.
  await evaluate(`(window.__rk.dispatch({type:'loopEnabled', enabled:false}), window.__rk.setSolo(null))`);
  await evaluate(`window.__rk.seek(${AT})`);
  // A trusted click (CDP input event) carries user activation, so the
  // AudioContext may start under any autoplay policy.
  const rect = await evaluate(`(() => { const b = document.querySelector('.rk-drums-bar button[aria-label="Play"]'); const r = b.getBoundingClientRect(); return { x: r.left + r.width / 2, y: r.top + r.height / 2 }; })()`);
  for (const type of ['mousePressed', 'mouseReleased']) await send('Input.dispatchMouseEvent', { type, x: rect.x, y: rect.y, button: 'left', clickCount: 1 });
  try {
    await waitFor(`window.__rk.engineState === 'playing' || window.__rk.engineState === 'error'`, 30000, 'playing');
  } catch (err) {
    console.log(ts(), 'engine state', await evaluate(`window.__rk.engineState`), 'ctx', await evaluate(`window.__rk.engine()?.context?.state ?? null`), 'stats', JSON.stringify(await evaluate(`window.__rk.stats()`)).slice(0, 400));
    throw err;
  }
  if (await evaluate(`window.__rk.engineError`)) throw new Error('engine: ' + (await evaluate(`window.__rk.engineError`)));
  const drumsIdx = await evaluate(`window.__rk.stems.indexOf('drums')`);
  const sample = async (label) => {
    const s = await evaluate(`(() => {
      const m = window.__rk; const k = window.__rkKit; const e = m.engine(); const st = m.stats();
      const lv = m.live.current;
      return { label: ${JSON.stringify(label)}, mode: window.__rkDrums.handle.mode, kitStatus: window.__rkKitStatus?.status ?? null, kitError: window.__rkKitStatus?.error ?? null,
        played: k ? k.stats.played : null, missing: k ? k.stats.missing : null, pending: k ? k.pending() : null, skipped: k ? k.skipped() : null, maxTickGapMs: k ? k.maxTickGapMs() : null,
        clock: k ? k.clock() : null, forcedMute: e ? e.isForcedMuted(${drumsIdx}) : null, underruns: st?.underruns ?? null,
        position: +lv.position.toFixed(2), master: +Math.max(lv.master.left, lv.master.right).toFixed(3), solo: m.mix.solo, state: m.engineState,
        loop: m.mix.loop, loopEnabled: m.mix.loopEnabled };
    })()`);
    console.log(ts(), JSON.stringify(s));
    out.steps.push(s);
    return s;
  };
  await sleep(1500);
  await sample('original-playing');
  // Solo the drums: from here the master meter shows the drum stem (ORIGINAL) or the kit (MIDI KIT) alone.
  await evaluate(`window.__rk.setSolo('drums')`);
  await sleep(1500);
  const orig = await sample('original-solo');
  // Switch to the kit.
  await evaluate(`window.__rkDrums.handle.setMode('midi')`);
  try {
    await waitFor(`!!window.__rkKit`, 30000, 'kit loaded + scheduler');
  } catch (err) {
    console.log(ts(), 'kit status', JSON.stringify(await evaluate(`window.__rkKitStatus ?? null`)));
    throw err;
  }
  await sleep(500);
  const first = await sample('midi-armed');
  let peakKit = 0;
  const played0 = first.played;
  for (let i = 0; i < 12; i++) {
    await sleep(500);
    const s = await sample('midi-solo');
    peakKit = Math.max(peakKit, s.master);
  }
  const afterPlay = await sample('midi-after-6s');
  const playedPerSec = (afterPlay.played - played0) / 6.5;
  // Expected: the unmuted hits between the two positions (free run, no loop).
  const expectedFree = await evaluate(`window.__rkDrums.handle.state.hits.filter((h) => h.t >= ${first.position} && h.t < ${afterPlay.position} && !window.__rkDrums.handle.isRowMuted(h.art)).length`);
  // Seek: queued voices cancelled, counting continues at the new place.
  await evaluate(`window.__rk.seek(${AT + 40})`);
  await sleep(1500);
  const afterSeek = await sample('midi-after-seek');
  await sleep(2000);
  const afterSeek2 = await sample('midi-after-seek+2s');
  // Loop 2 s: passes repeat the same hits, once per pass.
  const inLoop = await evaluate(`window.__rkDrums.handle.state.hits.filter((h) => h.t >= ${AT + 40} && h.t < ${AT + 42} && !window.__rkDrums.handle.isRowMuted(h.art)).length`);
  // Engage the loop, then seek into it (a playhead past loop.end plays on to the song's end by design).
  await evaluate(`(window.__rk.setLoop(${AT + 40}, ${AT + 42}), window.__rk.dispatch({type:'loopEnabled', enabled:true}))`);
  await evaluate(`window.__rk.seek(${AT + 40})`);
  await sleep(1200);
  const loopStart = await sample('midi-loop-start');
  await sleep(6000);
  const loopEnd = await sample('midi-loop-6s');
  const recent = await evaluate(`window.__rkKit.recent().slice(-40).map((r) => [r.art, +r.t.toFixed(2), +(r.when - r.now).toFixed(3), r.sf])`);
  console.log(ts(), 'recent', JSON.stringify(recent));
  const passes = (loopEnd.clock.readPos - loopStart.clock.readPos) / (2 * 48000);
  const perPass = (loopEnd.played - loopStart.played) / passes;
  // Back to ORIGINAL: kit gone, stem unmuted.
  await evaluate(`window.__rkDrums.handle.setMode('original')`);
  await sleep(800);
  const back = await sample('original-again');
  await evaluate(`window.__rk.stop()`);
  const summary = {
    hits,
    kitStatus: first.kitStatus,
    forcedMuteInMidi: first.forcedMute,
    forcedMuteAfter: back.forcedMute,
    kitGoneAfter: back.played === null,
    peakMasterKitOnly: peakKit,
    peakMasterOriginalSolo: orig.master,
    playedPerSecond: +playedPerSec.toFixed(2),
    playedFreeRun: afterPlay.played - played0,
    expectedFreeRun: expectedFree,
    missing: afterPlay.missing,
    seekResetPending: afterSeek.pending,
    playedAfterSeek: afterSeek2.played - afterSeek.played,
    hitsInLoop: inLoop,
    loopPasses: +passes.toFixed(2),
    playedPerLoopPass: +perPass.toFixed(2),
    underruns: loopEnd.underruns,
    skipped: loopEnd.skipped,
    maxTickGapMs: loopEnd.maxTickGapMs,
    consoleErrors: consoleErrors.length,
  };
  out.summary = summary;
  console.log(ts(), 'summary', JSON.stringify(summary));
  const ok = summary.kitStatus === 'ready' && summary.forcedMuteInMidi === true && summary.forcedMuteAfter === false && summary.kitGoneAfter && summary.peakMasterKitOnly > 0.02 && summary.playedPerSecond > 0 && summary.missing === 0 && Math.abs(summary.playedFreeRun - summary.expectedFreeRun) <= 2 && Math.abs(summary.playedPerLoopPass - summary.hitsInLoop) <= 1;
  console.log(ts(), ok ? 'KIT OK' : 'KIT CHECK FAILED');
  if (!ok) exitCode = 5;
  if (JSON_OUT) fs.writeFileSync(JSON_OUT, JSON.stringify(out, null, 2));
} catch (err) {
  console.error(ts(), 'FAILED:', err?.stack ?? err);
  exitCode = 1;
} finally {
  await shutdown(exitCode);
}
