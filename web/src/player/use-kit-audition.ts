import { useCallback, useEffect, useRef, useState } from 'react';
import { positionToGain } from '../lib/decibel';
import { isSilenced } from '../lib/mix-state';
import { velTo127, type Articulation } from '../lib/drums/taxonomy';
import type { DrumEvent } from '../lib/drums/types';
import type { DrumEditorHandle } from './use-drum-editor';
import type { Mixer } from './use-mixer';
import { loadKit, type LoadedKit } from './kit/kit';
import { KitScheduler, makeKitPlayer, type SchedulerHost } from './kit/scheduler';

export interface KitAudition {
  /** 'idle' until the first MIDI KIT use, then loading/ready/error. */
  status: 'idle' | 'loading' | 'ready' | 'error';
  error: string | null;
  /** Play these hits now, in order relative to the earliest (the inspector's AUDITION). */
  auditionNow(hits: DrumEvent[]): void;
}

const TICK_MS = 25;
const MAX_AUDITION = 12;

/**
 * MIDI KIT audition: while the editor is in 'midi' mode and the mixer plays,
 * the drum stem is force-muted and the edited hits sound through the sample
 * kit, scheduled on the engine's AudioContext against the stream clock.
 * Leaving the mode (or the tab) restores the stem and cancels everything.
 *
 * The Mixer handle is a new object every animation frame, so effects read
 * it through a ref and depend only on the values that matter (mode, engine
 * state, kit status, hits, mutes, the DRUMS strip).
 */
export function useKitAudition(m: Mixer, ed: DrumEditorHandle, active: boolean): KitAudition {
  const [status, setStatus] = useState<KitAudition['status']>('idle');
  const [error, setError] = useState<string | null>(null);
  const mRef = useRef(m);
  mRef.current = m;
  const kitRef = useRef<LoadedKit | null>(null);
  const loadingRef = useRef<Promise<void> | null>(null);
  const schedRef = useRef<KitScheduler | null>(null);
  const busRef = useRef<GainNode | null>(null);
  const drumsIndex = m.stems.indexOf('drums');
  const midiMode = active && ed.mode === 'midi';
  const engineState = m.engineState;
  // The scheduler is tied to the engine instance (created on the first
  // Play, replaced when the page moves to another job), not to its state.
  const engine = m.engine();
  const hitsRef = useRef(ed.state.hits);
  hitsRef.current = ed.state.hits;
  const mutedRef = useRef(ed.isRowMuted);
  mutedRef.current = ed.isRowMuted;

  // Force-mute the drum stem while the kit stands in for it.
  useEffect(() => {
    const e = mRef.current.engine();
    if (!e || drumsIndex < 0) return;
    e.setForcedMute(drumsIndex, midiMode);
    return () => e.setForcedMute(drumsIndex, false);
  }, [midiMode, drumsIndex, engineState]);

  // Load the kit on the engine's context the first time MIDI KIT is used.
  // The load is never cancelled by a re-render: it completes once.
  useEffect(() => {
    if (!midiMode || kitRef.current || loadingRef.current) return;
    const ctx = mRef.current.engine()?.context;
    if (!ctx) return;
    setStatus('loading');
    setError(null);
    loadingRef.current = loadKit(ctx)
      .then((kit) => {
        kitRef.current = kit;
        setStatus('ready');
        if (kit.missing.length) setError(`${kit.missing.length} sample(s) failed to load`);
      })
      .catch((err: unknown) => {
        setStatus('error');
        setError(err instanceof Error ? err.message : String(err));
      })
      .finally(() => {
        loadingRef.current = null;
      });
  }, [midiMode, engineState]);

  // The scheduler lives while the mode is on and the kit is ready.
  useEffect(() => {
    const e = mRef.current.engine();
    if (!midiMode || status !== 'ready' || !e || !e.context || !e.auxInput || !kitRef.current) return;
    const ctx = e.context;
    const bus = ctx.createGain();
    busRef.current = bus;
    const inner = makeKitPlayer(ctx, kitRef.current, e.auxInput, bus);
    const stats = { played: 0, missing: 0 };
    const play: SchedulerHost['play'] = (art, vel, when) => {
      const v = inner(art, vel, when);
      if (v) stats.played++;
      else stats.missing++;
      return v;
    };
    const host: SchedulerHost = {
      sampleRate: ctx.sampleRate,
      clock: () => e.clock(),
      plan: () => e.playPlan,
      play,
    };
    const sched = new KitScheduler(host);
    sched.setHits(hitsRef.current, mutedRef.current);
    schedRef.current = sched;
    const gaps = { max: 0, last: performance.now() };
    const timer = setInterval(() => {
      const now = performance.now();
      gaps.max = Math.max(gaps.max, now - gaps.last);
      gaps.last = now;
      sched.tick();
    }, TICK_MS);
    // Debug hook for scripts (web/scripts/kit-verify.mjs) and the console.
    const dbg = { stats, pending: () => sched.pending, skipped: () => sched.skipped, recent: () => sched.recent.slice(), maxTickGapMs: () => Math.round(gaps.max), clock: () => e.clock() };
    (window as unknown as { __rkKit?: typeof dbg }).__rkKit = dbg;
    return () => {
      clearInterval(timer);
      sched.stopAll();
      schedRef.current = null;
      busRef.current = null;
      bus.disconnect();
      delete (window as unknown as { __rkKit?: typeof dbg }).__rkKit;
    };
  }, [midiMode, status, engine]);

  // Hits and row mutes: reschedule on change.
  useEffect(() => {
    schedRef.current?.setHits(ed.state.hits, ed.isRowMuted);
  }, [ed.state.hits, ed.isRowMuted, midiMode, status]);

  // The kit bus follows the DRUMS strip: fader, mute, solo.
  const mix = m.mix;
  useEffect(() => {
    const bus = busRef.current;
    const ctx = mRef.current.engine()?.context;
    if (!bus || !ctx) return;
    const s = mix.stems.drums;
    const dead = !s || s.muted || isSilenced(mix, 'drums');
    bus.gain.setTargetAtTime(dead ? 0 : positionToGain(s.position), ctx.currentTime, 0.01);
  }, [mix, midiMode, status]);

  // Status for scripts and the console.
  useEffect(() => {
    (window as unknown as { __rkKitStatus?: { status: string; error: string | null } }).__rkKitStatus = { status, error };
  }, [status, error]);

  // AUDITION plays at once, transport stopped or not: through the engine's
  // direct output (master level, no transport gate) and the DRUMS strip level.
  const auditionNow = useCallback((hits: DrumEvent[]) => {
    const e = mRef.current.engine();
    const ctx = e?.context;
    const out = e?.directInput;
    const kit = kitRef.current;
    if (!ctx || !out || !kit || hits.length === 0) return;
    void ctx.resume();
    const bus = ctx.createGain();
    const s = mRef.current.mix.stems.drums;
    bus.gain.value = !s || s.muted || isSilenced(mRef.current.mix, 'drums') ? 0 : positionToGain(s.position);
    const play = makeKitPlayer(ctx, kit, out, bus);
    const list = hits.slice(0, MAX_AUDITION);
    const t0 = Math.min(...list.map((h) => h.t));
    const now = ctx.currentTime + 0.02;
    for (const h of list) play(h.art as Articulation, velTo127(h.vel), now + (h.t - t0));
    setTimeout(() => bus.disconnect(), (Math.max(...list.map((h) => h.t)) - t0 + 4) * 1000);
  }, []);

  // The inspector's AUDITION is live only in MIDI KIT with the kit loaded.
  return { status: midiMode ? status : 'idle', error, auditionNow };
}
