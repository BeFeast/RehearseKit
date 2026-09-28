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
 */
export function useKitAudition(m: Mixer, ed: DrumEditorHandle, active: boolean): KitAudition {
  const [status, setStatus] = useState<KitAudition['status']>('idle');
  const [error, setError] = useState<string | null>(null);
  const kitRef = useRef<LoadedKit | null>(null);
  const schedRef = useRef<KitScheduler | null>(null);
  const busRef = useRef<GainNode | null>(null);
  const playRef = useRef<SchedulerHost['play'] | null>(null);
  const drumsIndex = m.stems.indexOf('drums');
  const midiMode = active && ed.mode === 'midi';

  // Force-mute the drum stem while the kit stands in for it.
  useEffect(() => {
    const e = m.engine();
    if (!e || drumsIndex < 0) return;
    e.setForcedMute(drumsIndex, midiMode);
    return () => e.setForcedMute(drumsIndex, false);
  }, [m, midiMode, drumsIndex, m.engineState]);

  // Load the kit on the engine's context the first time MIDI KIT is used.
  useEffect(() => {
    if (!midiMode) return;
    const e = m.engine();
    const ctx = e?.context;
    if (!ctx || kitRef.current || status === 'loading') return;
    let cancelled = false;
    setStatus('loading');
    setError(null);
    (async () => {
      try {
        const kit = await loadKit(ctx);
        if (cancelled) return;
        kitRef.current = kit;
        setStatus('ready');
        if (kit.missing.length) setError(`${kit.missing.length} sample(s) failed to load`);
      } catch (err) {
        if (cancelled) return;
        setStatus('error');
        setError(err instanceof Error ? err.message : String(err));
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [midiMode, m, m.engineState, status]);

  // The scheduler lives while the mode is on and the kit is ready.
  useEffect(() => {
    const e = m.engine();
    if (!midiMode || status !== 'ready' || !e || !e.context || !e.auxInput || !kitRef.current) return;
    const ctx = e.context;
    const bus = ctx.createGain();
    busRef.current = bus;
    const play = makeKitPlayer(ctx, kitRef.current, e.auxInput, bus);
    playRef.current = play;
    const host: SchedulerHost = {
      sampleRate: ctx.sampleRate,
      clock: () => e.clock(),
      plan: () => e.playPlan,
      play,
    };
    const sched = new KitScheduler(host);
    schedRef.current = sched;
    const timer = setInterval(() => sched.tick(), TICK_MS);
    return () => {
      clearInterval(timer);
      sched.stopAll();
      schedRef.current = null;
      playRef.current = null;
      busRef.current = null;
      bus.disconnect();
    };
  }, [midiMode, status, m, m.engineState]);

  // Hits and row mutes: reschedule on change.
  useEffect(() => {
    schedRef.current?.setHits(ed.state.hits, ed.isRowMuted);
  }, [ed.state.hits, ed.isRowMuted, midiMode, status]);

  // The kit bus follows the DRUMS strip: fader, mute, solo.
  useEffect(() => {
    const bus = busRef.current;
    const e = m.engine();
    if (!bus || !e?.context) return;
    const s = m.mix.stems.drums;
    const dead = !s || s.muted || isSilenced(m.mix, 'drums');
    bus.gain.setTargetAtTime(dead ? 0 : positionToGain(s.position), e.context.currentTime, 0.01);
  }, [m, m.mix, midiMode, status]);

  const auditionNow = useCallback(
    (hits: DrumEvent[]) => {
      const e = m.engine();
      const ctx = e?.context;
      const play = playRef.current;
      if (!ctx || !play || hits.length === 0) return;
      const list = hits.slice(0, MAX_AUDITION);
      const t0 = Math.min(...list.map((h) => h.t));
      const now = ctx.currentTime + 0.02;
      for (const h of list) play(h.art as Articulation, velTo127(h.vel), now + (h.t - t0));
    },
    [m],
  );

  return { status, error, auditionNow };
}
