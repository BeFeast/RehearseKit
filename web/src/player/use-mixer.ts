import { useCallback, useEffect, useMemo, useReducer, useRef, useState } from 'react';
import * as api from '../api';
import type { Job, StemName } from '../api/types';
import { positionToGain, UNITY_POSITION } from '../lib/decibel';
import { initialMix, isSilenced, mixReducer, sanitize, type MixAction, type MixState } from '../lib/mix-state';
import { parsePeaks, type PeaksFile } from '../lib/peaks';
import { probeStems, StreamEngine, type EngineState } from './engine/engine';

export const STEM_ORDER: StemName[] = ['vocals', 'drums', 'bass', 'other', 'guitar', 'piano'];

/** Values the UI reads every animation frame (never trigger renders themselves). */
export interface LiveValues {
  position: number;
  /** Post-fader linear level per stem (0 when muted or silenced). */
  levels: number[];
  /** Post-fader peak hold per stem. */
  holds: number[];
  master: { left: number; right: number; leftHold: number; rightHold: number };
}

export interface Mixer {
  job: Job;
  stems: StemName[];
  mix: MixState;
  /** mix.selected / mix.solo narrowed to the job's stems. */
  selected: StemName | null;
  solo: StemName | null;
  dispatch: (a: MixAction) => void;
  mixLoaded: boolean;
  duration: number;
  bpm: number | null;
  live: React.RefObject<LiveValues>;
  /** Bumps at ~30 fps while audio moves; subscribe by reading it. */
  tick: number;
  playing: boolean;
  engineState: EngineState;
  engineError: string | null;
  /** Engine probe/init in flight (first Play). */
  starting: boolean;
  peaks: Partial<Record<StemName, PeaksFile>>;
  peaksLoading: boolean;
  togglePlay(): void;
  play(): void;
  stop(): void;
  seek(seconds: number): void;
  nudge(deltaSeconds: number): void;
  returnToStart(): void;
  toggleLoop(): void;
  setLoop(start: number, end: number): void;
  setSolo(stem: StemName | null): void;
  toggleSolo(stem: StemName): void;
  toggleMute(stem: StemName): void;
  clearSolo(): void;
  stats(): ReturnType<StreamEngine['stats']> | null;
  engine(): StreamEngine | null;
}

const PERSIST_MS = 600;

/**
 * Wires the streaming engine, the per-job mix state and the peaks files into
 * one handle the mixer and the mobile player share. The engine is created on
 * the first Play (AudioContext needs a gesture); everything the user does
 * before that is kept in mix state and applied at init.
 */
export function useMixer(job: Job): Mixer {
  const stems = useMemo(
    () => STEM_ORDER.filter((n) => job.stems.some((s) => s.name === n)),
    [job.stems],
  );
  const duration = useMemo(() => {
    if (job.duration_seconds) return job.duration_seconds;
    const s = job.stems[0];
    return s && s.sample_rate ? s.frames / s.sample_rate : 0;
  }, [job.duration_seconds, job.stems]);
  const bpm = job.detected_bpm && job.detected_bpm > 0 ? job.detected_bpm : null;

  const [mix, dispatch] = useReducer(mixReducer, undefined, () => initialMix(stems, UNITY_POSITION));
  const [mixLoaded, setMixLoaded] = useState(false);
  const [engineState, setEngineState] = useState<EngineState>('idle');
  const [engineError, setEngineError] = useState<string | null>(null);
  const [starting, setStarting] = useState(false);
  const [peaks, setPeaks] = useState<Partial<Record<StemName, PeaksFile>>>({});
  const [peaksLoading, setPeaksLoading] = useState(true);
  const [tick, setTick] = useState(0);

  const engineRef = useRef<StreamEngine | null>(null);
  const mixRef = useRef(mix);
  mixRef.current = mix;
  const pendingPosition = useRef(0);
  const live = useRef<LiveValues>({
    position: 0,
    levels: stems.map(() => 0),
    holds: stems.map(() => 0),
    master: { left: 0, right: 0, leftHold: 0, rightHold: 0 },
  });
  const animateUntil = useRef(0);

  // ---- mix state: load once, persist debounced ----------------------------
  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const saved = await api.getMix(job.id);
        if (!cancelled && saved) dispatch({ type: 'load', state: sanitize(saved, stems, UNITY_POSITION) });
      } catch {
        // stay with the initial mix
      } finally {
        if (!cancelled) setMixLoaded(true);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [job.id, stems]);

  useEffect(() => {
    if (!mixLoaded) return;
    const t = setTimeout(() => void api.putMix(job.id, mix).catch(() => undefined), PERSIST_MS);
    return () => clearTimeout(t);
  }, [mix, mixLoaded, job.id]);

  // ---- peaks -------------------------------------------------------------
  useEffect(() => {
    let cancelled = false;
    setPeaksLoading(true);
    (async () => {
      const out: Partial<Record<StemName, PeaksFile>> = {};
      await Promise.all(
        job.stems.map(async (s) => {
          if (!s.peaks_url) return;
          try {
            const r = await fetch(s.peaks_url, { credentials: 'same-origin', headers: claimHeaders(job.id) });
            if (!r.ok) return;
            out[s.name] = parsePeaks(await r.arrayBuffer());
          } catch {
            // waveform stays empty for this stem
          }
        }),
      );
      if (!cancelled) {
        setPeaks(out);
        setPeaksLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [job.id, job.stems]);

  // ---- engine sync ---------------------------------------------------------
  const applyMix = useCallback(
    (e: StreamEngine, m: MixState) => {
      stems.forEach((name, i) => {
        const s = m.stems[name];
        e.setGain(i, positionToGain(s.position));
        e.setMute(i, s.muted);
      });
      const soloIdx = m.solo ? stems.indexOf(m.solo as StemName) : -1;
      if (soloIdx >= 0) e.setSolo(soloIdx, true);
      else e.clearSolo();
      e.setMasterGain(positionToGain(m.masterPosition));
      // Only touch the loop when it actually changed: setLoopRegion replans
      // (restarts the streams) while LOOP is engaged.
      const cur = e.loopRegion;
      if (!m.loop) {
        if (cur) e.setLoop(null);
      } else if (!cur || Math.abs(cur.start - m.loop.start) > 1e-3 || Math.abs(cur.end - m.loop.end) > 1e-3) {
        e.setLoopRegion(m.loop);
      }
      e.setLoopEnabled(m.loopEnabled);
    },
    [stems],
  );

  useEffect(() => {
    const e = engineRef.current;
    if (e) applyMix(e, mix);
  }, [mix, applyMix]);

  useEffect(
    () => () => {
      const e = engineRef.current;
      engineRef.current = null;
      if (e) void e.destroy();
    },
    [job.id],
  );

  const ensureEngine = useCallback(async (): Promise<StreamEngine> => {
    if (engineRef.current) return engineRef.current;
    setStarting(true);
    setEngineError(null);
    try {
      const fetchImpl: typeof fetch = (input, init) =>
        fetch(input, { ...init, credentials: 'same-origin', headers: { ...(init?.headers as Record<string, string>), ...claimHeaders(job.id) } });
      const sources = await probeStems(
        stems.map((name) => {
          const s = job.stems.find((x) => x.name === name)!;
          return { name, url: s.stream_url };
        }),
        fetchImpl,
      );
      const e = new StreamEngine({ stems: sources, fetchImpl });
      e.subscribe((s) => {
        setEngineState(s);
        if (s === 'error') setEngineError(e.stats().error);
        if (s === 'ended') animateUntil.current = performance.now() + 2500;
      });
      applyMix(e, mixRef.current);
      e.seek(pendingPosition.current);
      await e.init();
      engineRef.current = e;
      return e;
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err);
      setEngineError(msg);
      setEngineState('error');
      throw err;
    } finally {
      setStarting(false);
    }
  }, [applyMix, job.id, job.stems, stems]);

  // ---- animation frame ------------------------------------------------------
  useEffect(() => {
    let raf = 0;
    let last = 0;
    const loop = (now: number) => {
      raf = requestAnimationFrame(loop);
      const e = engineRef.current;
      const m = mixRef.current;
      const lv = live.current;
      if (e) {
        lv.position = e.position;
        const meters = e.meters();
        stems.forEach((name, i) => {
          const s = m.stems[name];
          const dead = s.muted || isSilenced(m, name);
          const g = dead ? 0 : positionToGain(s.position);
          lv.levels[i] = (meters[i]?.level ?? 0) * g;
          lv.holds[i] = (meters[i]?.hold ?? 0) * g;
        });
        const mm = e.masterMeter();
        lv.master.left = mm.left.level;
        lv.master.right = mm.right.level;
        lv.master.leftHold = mm.left.hold;
        lv.master.rightHold = mm.right.hold;
        if (e.isPlaying) animateUntil.current = now + 2500;
      } else {
        lv.position = pendingPosition.current;
      }
      if (now < animateUntil.current && now - last >= 32) {
        last = now;
        setTick((t) => (t + 1) | 0);
      }
    };
    raf = requestAnimationFrame(loop);
    return () => cancelAnimationFrame(raf);
  }, [stems]);

  const poke = useCallback(() => {
    animateUntil.current = Math.max(animateUntil.current, performance.now() + 300);
    setTick((t) => (t + 1) | 0);
  }, []);

  // ---- transport -----------------------------------------------------------
  const play = useCallback(() => {
    void ensureEngine()
      .then((e) => {
        e.play();
        poke();
      })
      .catch(() => undefined);
  }, [ensureEngine, poke]);

  const stop = useCallback(() => {
    engineRef.current?.stop();
    poke();
  }, [poke]);

  const playing = engineState === 'playing' || engineState === 'priming';

  const togglePlay = useCallback(() => {
    if (playing) stop();
    else play();
  }, [playing, play, stop]);

  const seek = useCallback(
    (seconds: number) => {
      const t = Math.max(0, Math.min(duration, seconds));
      pendingPosition.current = t;
      engineRef.current?.seek(t);
      poke();
    },
    [duration, poke],
  );

  const nudge = useCallback(
    (delta: number) => {
      const cur = engineRef.current ? engineRef.current.position : pendingPosition.current;
      seek(cur + delta);
    },
    [seek],
  );

  const returnToStart = useCallback(() => {
    const m = mixRef.current;
    seek(m.loopEnabled && m.loop ? m.loop.start : 0);
  }, [seek]);

  const toggleLoop = useCallback(() => {
    const m = mixRef.current;
    if (!m.loop) {
      // No range yet: default to the bar (or 8 s) around the playhead.
      const pos = engineRef.current ? engineRef.current.position : pendingPosition.current;
      const len = bpm ? (60 / bpm) * 16 : 8;
      const start = Math.max(0, Math.min(pos, duration - Math.min(len, duration)));
      const end = Math.min(duration, start + len);
      if (end - start < 0.1) return;
      dispatch({ type: 'loop', loop: { start, end } });
      dispatch({ type: 'loopEnabled', enabled: true });
      return;
    }
    dispatch({ type: 'loopEnabled', enabled: !m.loopEnabled });
  }, [bpm, duration]);

  const setLoop = useCallback((start: number, end: number) => dispatch({ type: 'loop', loop: { start, end } }), []);
  const setSolo = useCallback((stem: StemName | null) => dispatch({ type: 'solo', stem }), []);
  const toggleSolo = useCallback((stem: StemName) => dispatch({ type: 'solo', stem: mixRef.current.solo === stem ? null : stem }), []);
  const toggleMute = useCallback((stem: StemName) => dispatch({ type: 'mute', stem, muted: !mixRef.current.stems[stem]?.muted }), []);
  const clearSolo = useCallback(() => dispatch({ type: 'solo', stem: null }), []);

  const handle: Mixer = {
    job,
    stems,
    mix,
    selected: (mix.selected as StemName | null) ?? null,
    solo: (mix.solo as StemName | null) ?? null,
    dispatch,
    mixLoaded,
    duration,
    bpm,
    live,
    tick,
    playing,
    engineState,
    engineError,
    starting,
    peaks,
    peaksLoading,
    togglePlay,
    play,
    stop,
    seek,
    nudge,
    returnToStart,
    toggleLoop,
    setLoop,
    setSolo,
    toggleSolo,
    toggleMute,
    clearSolo,
    stats: () => engineRef.current?.stats() ?? null,
    engine: () => engineRef.current,
  };
  // Debug hook for scripts (web/scripts/playback-verify.mjs) and the console.
  useEffect(() => {
    (window as unknown as { __rk?: Mixer }).__rk = handle;
    return () => {
      delete (window as unknown as { __rk?: Mixer }).__rk;
    };
  });
  return handle;
}

function claimHeaders(jobId: string): Record<string, string> {
  try {
    const raw = localStorage.getItem('rk.claims');
    const c = raw ? (JSON.parse(raw) as Record<string, string>) : {};
    return c[jobId] ? { 'X-Claim-Token': c[jobId] } : {};
  } catch {
    return {};
  }
}
