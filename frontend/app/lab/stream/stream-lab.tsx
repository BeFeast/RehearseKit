'use client';

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useSearchParams } from 'next/navigation';
import { probeStems, StreamEngine, type EngineState, type EngineStats, type StemSource } from '@/lib/lab-stream/engine';
import { baselineBytes, formatMB, readMemory, type MemorySample } from '@/lib/lab-stream/memory';

const DEFAULT_STEM_URL = 'http://localhost:8010';

interface LabWindow extends Window {
  __labStream?: {
    engine: StreamEngine | null;
    stats: () => EngineStats | null;
    memory: () => Promise<MemorySample>;
  };
}

function fmtTime(seconds: number): string {
  if (!Number.isFinite(seconds)) return '--:--.-';
  const m = Math.floor(seconds / 60);
  const s = seconds - m * 60;
  return `${m}:${s.toFixed(1).padStart(4, '0')}`;
}

export function StreamLab() {
  const params = useSearchParams();
  const jobId = params.get('job') ?? '';
  const stemBase = (params.get('api') ?? process.env.NEXT_PUBLIC_LAB_STEM_URL ?? DEFAULT_STEM_URL).replace(/\/$/, '');
  const stemsParam = params.get('stems');

  const engineRef = useRef<StreamEngine | null>(null);
  const [sources, setSources] = useState<StemSource[] | null>(null);
  const [engineState, setEngineState] = useState<EngineState>('idle');
  const [stats, setStats] = useState<EngineStats | null>(null);
  const [memory, setMemory] = useState<MemorySample>({});
  const [position, setPosition] = useState(0);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [loopStart, setLoopStart] = useState('');
  const [loopEnd, setLoopEnd] = useState('');
  const [seekTarget, setSeekTarget] = useState('');
  const [gainUi, setGainUi] = useState<number[]>([]);
  const [muteUi, setMuteUi] = useState<boolean[]>([]);
  const [isolated, setIsolated] = useState<boolean | null>(null);

  useEffect(() => {
    setIsolated(typeof window !== 'undefined' ? window.crossOriginIsolated : null);
  }, []);

  // Expose the engine for CDP-driven measurements.
  useEffect(() => {
    const w = window as LabWindow;
    w.__labStream = {
      engine: engineRef.current,
      stats: () => engineRef.current?.stats() ?? null,
      memory: readMemory,
    };
    return () => {
      delete w.__labStream;
    };
  }, [engineState]);

  useEffect(() => {
    return () => {
      void engineRef.current?.destroy();
    };
  }, []);

  // Stats polling + position via rAF.
  useEffect(() => {
    if (engineState === 'idle') return;
    let raf = 0;
    const loop = () => {
      const e = engineRef.current;
      if (e) setPosition(e.position);
      raf = requestAnimationFrame(loop);
    };
    raf = requestAnimationFrame(loop);
    const timer = setInterval(() => {
      const e = engineRef.current;
      if (e) setStats(e.stats());
    }, 250);
    const memTimer = setInterval(() => {
      void readMemory().then(setMemory);
    }, 2000);
    void readMemory().then(setMemory);
    return () => {
      cancelAnimationFrame(raf);
      clearInterval(timer);
      clearInterval(memTimer);
    };
  }, [engineState]);

  const baseline = useMemo(() => (sources ? baselineBytes(sources.map((s) => s.fmt)) : 0), [sources]);

  const load = useCallback(async () => {
    setBusy(true);
    setError(null);
    try {
      let names: string[];
      if (stemsParam) {
        names = stemsParam.split(',').map((s) => s.trim()).filter(Boolean);
      } else {
        const res = await fetch(`${stemBase}/stems/${jobId}`);
        if (!res.ok) throw new Error(`manifest ${res.status} from ${stemBase}/stems/${jobId}`);
        const manifest = (await res.json()) as { stems: { name: string }[] };
        names = manifest.stems.map((s) => s.name);
      }
      if (names.length === 0) throw new Error('no stems to play');
      const probed = await probeStems(names.map((name) => ({ name, url: `${stemBase}/stems/${jobId}/${name}` })));
      const engine = new StreamEngine({ stems: probed });
      engine.subscribe((s) => {
        setEngineState(s);
        setStats(engine.stats());
      });
      await engine.init();
      engineRef.current = engine;
      setSources(probed);
      setGainUi(probed.map(() => 1));
      setMuteUi(probed.map(() => false));
      setLoopStart('0');
      setLoopEnd(Math.min(8, engine.duration).toFixed(1));
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  }, [jobId, stemBase, stemsParam]);

  const engine = engineRef.current;
  const duration = engine?.duration ?? 0;
  const transportReady = engine !== null && engineState !== 'idle' && engineState !== 'error';

  const onSeekSlider = (value: number) => {
    engine?.seek(value);
  };

  const applyLoop = () => {
    if (!engine) return;
    const ok = engine.setLoop({ start: Number(loopStart), end: Number(loopEnd) });
    if (!ok) setError('Loop range rejected: end must be after start (≥ 1024 frames) and inside the song.');
    else setError(null);
  };

  return (
    <div className="mx-auto max-w-5xl space-y-6 p-6 font-mono text-sm">
      <header className="space-y-1">
        <h1 className="text-xl font-semibold">Streaming playback lab</h1>
        <p className="text-muted-foreground">
          Lossless stems via HTTP range requests → SharedArrayBuffer ring → AudioWorklet. No full decode, no
          resampling, no lossy shortcuts.
        </p>
      </header>

      <section className="grid grid-cols-2 gap-x-6 gap-y-1 rounded border p-4 md:grid-cols-4">
        <div>job</div>
        <div className="truncate" title={jobId}>{jobId || '(missing ?job=)'}</div>
        <div>stem server</div>
        <div className="truncate" title={stemBase}>{stemBase}</div>
        <div>crossOriginIsolated</div>
        <div data-testid="lab-isolated">{isolated === null ? '…' : String(isolated)}</div>
        <div>SharedArrayBuffer</div>
        <div>{typeof SharedArrayBuffer !== 'undefined' ? 'yes' : 'no'}</div>
      </section>

      {!engine && (
        <button
          type="button"
          data-testid="lab-load"
          disabled={busy || !jobId || isolated === false}
          onClick={() => void load()}
          className="rounded bg-kit-blue px-4 py-2 font-semibold text-white disabled:opacity-50"
        >
          {busy ? 'Loading…' : 'Load stems'}
        </button>
      )}

      {error && (
        <div data-testid="lab-error" className="rounded border border-red-500 bg-red-50 p-3 text-red-700">
          {error}
        </div>
      )}

      {engine && sources && (
        <>
          <section className="space-y-3 rounded border p-4">
            <div className="flex flex-wrap items-center gap-3">
              <button
                type="button"
                data-testid="lab-play"
                disabled={!transportReady || engineState === 'playing' || engineState === 'priming'}
                onClick={() => engine.play()}
                className="rounded bg-emerald-600 px-4 py-2 font-semibold text-white disabled:opacity-50"
              >
                Play
              </button>
              <button
                type="button"
                data-testid="lab-stop"
                disabled={!transportReady || engineState === 'stopped' || engineState === 'ready'}
                onClick={() => engine.stop()}
                className="rounded bg-slate-700 px-4 py-2 font-semibold text-white disabled:opacity-50"
              >
                Stop
              </button>
              <span data-testid="lab-state" className="rounded bg-slate-100 px-2 py-1">
                {engineState}
              </span>
              <span data-testid="lab-position" className="tabular-nums">
                {fmtTime(position)} / {fmtTime(duration)}
              </span>
            </div>
            <input
              type="range"
              min={0}
              max={duration}
              step={0.1}
              value={Math.min(position, duration)}
              onChange={(e) => onSeekSlider(Number(e.target.value))}
              className="w-full"
              aria-label="position"
            />
            <div className="flex flex-wrap items-center gap-2">
              <label>
                seek to (s){' '}
                <input
                  value={seekTarget}
                  onChange={(e) => setSeekTarget(e.target.value)}
                  className="w-24 rounded border px-2 py-1"
                  data-testid="lab-seek-input"
                />
              </label>
              <button
                type="button"
                data-testid="lab-seek"
                onClick={() => engine.seek(Number(seekTarget))}
                className="rounded border px-3 py-1"
              >
                Seek
              </button>
              <button
                type="button"
                data-testid="lab-seek-end"
                onClick={() => engine.seek(duration)}
                className="rounded border px-3 py-1"
              >
                Seek to end
              </button>
              <span className="mx-2 border-l" />
              <label>
                loop start (s){' '}
                <input value={loopStart} onChange={(e) => setLoopStart(e.target.value)} className="w-24 rounded border px-2 py-1" data-testid="lab-loop-start" />
              </label>
              <label>
                end (s){' '}
                <input value={loopEnd} onChange={(e) => setLoopEnd(e.target.value)} className="w-24 rounded border px-2 py-1" data-testid="lab-loop-end" />
              </label>
              <button type="button" data-testid="lab-loop-set" onClick={applyLoop} className="rounded border px-3 py-1">
                Set loop
              </button>
              <button type="button" data-testid="lab-loop-clear" onClick={() => engine.setLoop(null)} className="rounded border px-3 py-1">
                Clear loop
              </button>
              <span data-testid="lab-loop">
                {engine.loopRange
                  ? `loop ${fmtTime(engine.loopRange.start / engine.sampleRate)}–${fmtTime(engine.loopRange.end / engine.sampleRate)}`
                  : 'no loop'}
              </span>
            </div>
          </section>

          <section className="rounded border">
            <table className="w-full text-left">
              <thead className="bg-slate-50">
                <tr>
                  <th className="p-2">stem</th>
                  <th className="p-2">format</th>
                  <th className="p-2">mute</th>
                  <th className="p-2">gain</th>
                  <th className="p-2">ring</th>
                  <th className="p-2">req / MB</th>
                  <th className="p-2">last / max fetch</th>
                </tr>
              </thead>
              <tbody>
                {sources.map((s, i) => {
                  const st = stats?.streams[i];
                  return (
                    <tr key={s.name} className="border-t" data-testid={`lab-stem-${s.name}`}>
                      <td className="p-2 font-semibold">{s.name}</td>
                      <td className="p-2">
                        {s.fmt.isFloat ? '32f' : `${s.fmt.bitsPerSample}i`} / {s.fmt.sampleRate} Hz / {s.fmt.channels}ch /{' '}
                        {formatMB(s.fileSize, 0)}
                      </td>
                      <td className="p-2">
                        <input
                          type="checkbox"
                          checked={muteUi[i] ?? false}
                          aria-label={`mute ${s.name}`}
                          onChange={(e) => {
                            engine.setMute(i, e.target.checked);
                            setMuteUi((m) => m.map((v, k) => (k === i ? e.target.checked : v)));
                          }}
                        />
                      </td>
                      <td className="p-2">
                        <input
                          type="range"
                          min={0}
                          max={1.5}
                          step={0.01}
                          value={gainUi[i] ?? 1}
                          aria-label={`gain ${s.name}`}
                          onChange={(e) => {
                            const v = Number(e.target.value);
                            engine.setGain(i, v);
                            setGainUi((g) => g.map((x, k) => (k === i ? v : x)));
                          }}
                        />{' '}
                        {(gainUi[i] ?? 1).toFixed(2)}
                      </td>
                      <td className="p-2 tabular-nums">{stats ? `${Math.round((stats.ringFill[i] ?? 0) * 100)}%` : '–'}</td>
                      <td className="p-2 tabular-nums">{st ? `${st.requests} / ${(st.bytes / 1048576).toFixed(1)}` : '–'}</td>
                      <td className="p-2 tabular-nums">{st ? `${st.lastFetchMs.toFixed(0)} / ${st.maxFetchMs.toFixed(0)} ms` : '–'}</td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </section>

          <section className="grid grid-cols-2 gap-x-6 gap-y-1 rounded border p-4 md:grid-cols-4">
            <div>underruns</div>
            <div data-testid="lab-underruns" className="font-semibold">{stats?.underruns ?? 0}</div>
            <div>quanta rendered</div>
            <div data-testid="lab-quanta">{stats?.quanta ?? 0}</div>
            <div>consumed frames</div>
            <div>{stats?.consumedFrames ?? 0}</div>
            <div>last seek → sound</div>
            <div data-testid="lab-seek-ms">{stats ? `${stats.lastSeekMs.toFixed(0)} ms` : '–'}</div>
            <div>requests / bytes</div>
            <div>{stats ? `${stats.requests} / ${formatMB(stats.bytes)}` : '–'}</div>
            <div>ring buffers (SAB)</div>
            <div>{stats ? formatMB(stats.sabBytes) : '–'}</div>
            <div>context / latency</div>
            <div>{stats ? `${stats.contextState} / ${(stats.outputLatency * 1000).toFixed(0)} ms` : '–'}</div>
            <div>generation</div>
            <div>{stats?.generation ?? 0}</div>
            <div>JS heap used</div>
            <div data-testid="lab-heap">{memory.jsHeapUsedMB !== undefined ? `${memory.jsHeapUsedMB.toFixed(1)} MB` : 'n/a'}</div>
            <div>UA memory (incl. SAB)</div>
            <div data-testid="lab-ua-memory">{memory.uaMemoryMB !== undefined ? `${memory.uaMemoryMB.toFixed(1)} MB` : 'n/a'}</div>
            <div>baseline: full decode</div>
            <div data-testid="lab-baseline">{formatMB(baseline, 0)}</div>
            <div>stems × duration</div>
            <div>
              {sources.length} × {fmtTime(duration)}
            </div>
          </section>
        </>
      )}
    </div>
  );
}
