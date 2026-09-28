import { describe, expect, it } from 'vitest';
import type { DrumEvent } from '../../../lib/drums/types';
import { PlayPlan } from '../../engine/chunk-scheduler';
import { KitScheduler, lowerBound, type ClockSnapshot, type SchedulerHost, type Voice } from '../scheduler';

const SR = 48000;

interface Played {
  art: string;
  vel: number;
  when: number;
  stopped: number | null;
}

/**
 * A fake engine: the audio clock advances by `advance(seconds)`; the stream
 * position follows the plan (prefix then loop) unless stalled.
 */
function fakeHost() {
  let plan: PlayPlan | null = null;
  let generation = 0;
  let ctxFrame = 1000; // audio clock never starts at 0
  let streamAtCtx = 0; // stream frame at ctxFrame
  let underruns = 0;
  let playing = false;
  const played: Played[] = [];
  const host: SchedulerHost = {
    sampleRate: SR,
    clock: (): ClockSnapshot | null =>
      playing && plan ? { readPos: streamAtCtx, clockFrame: ctxFrame, ctxTime: ctxFrame / SR, ctxFrame, generation, underruns } : null,
    plan: () => (playing ? plan : null),
    play: (art, vel, when) => {
      const p: Played = { art, vel, when, stopped: null };
      played.push(p);
      const v: Voice = { stop: (at) => (p.stopped = at) };
      return v;
    },
  };
  return {
    host,
    played,
    start(startSec: number, total: number, loop: { start: number; end: number } | null) {
      plan = new PlayPlan(Math.round(startSec * SR), Math.round(total * SR), loop ? { start: Math.round(loop.start * SR), end: Math.round(loop.end * SR) } : null);
      generation++;
      streamAtCtx = 0;
      playing = true;
    },
    stop() {
      playing = false;
      generation++;
    },
    advance(sec: number, stall = false) {
      const f = Math.round(sec * SR);
      ctxFrame += f;
      if (!stall) streamAtCtx += f;
      else underruns++;
    },
    get ctxTime() {
      return ctxFrame / SR;
    },
  };
}

const hit = (id: string, art: DrumEvent['art'], t: number, vel = 0.8): DrumEvent => ({ id, art, t, vel, src: 'manual' });

describe('KitScheduler', () => {
  it('queues each hit once at the audio time it falls on, ahead of the clock', () => {
    const f = fakeHost();
    const s = new KitScheduler(f.host, { lookaheadSeconds: 0.12 });
    s.setHits([hit('a', 'kick', 1.0), hit('b', 'snare', 1.05), hit('c', 'kick', 3.0)], () => false);
    f.start(0.95, 10, null);
    s.tick(); // now stream 0 = song 0.95; horizon 0.12 s → hits at 1.0 and 1.05
    expect(f.played.map((p) => p.art)).toEqual(['kick', 'snare']);
    expect(f.played[0].when).toBeCloseTo(f.ctxTime + 0.05, 6);
    expect(f.played[1].when).toBeCloseTo(f.ctxTime + 0.1, 6);
    expect(f.played[0].vel).toBe(102);
    // Later ticks never queue them again.
    for (let i = 0; i < 10; i++) {
      f.advance(0.025);
      s.tick();
    }
    expect(f.played.length).toBe(2);
    // ... until the clock approaches the third hit.
    f.advance(1.7);
    s.tick();
    expect(f.played.length).toBe(3);
    expect(f.played[2].when).toBeCloseTo(f.ctxTime + (3.0 - (0.95 + 0.025 * 10 + 1.7)), 6);
  });

  it('a loop wrap re-queues the hits of the region each pass, never twice per pass', () => {
    const f = fakeHost();
    const s = new KitScheduler(f.host, { lookaheadSeconds: 0.1 });
    s.setHits([hit('a', 'kick', 2.0), hit('b', 'snare', 2.5)], () => false);
    f.start(1.95, 10, { start: 2.0, end: 2.6 }); // prefix 0.65 s, then loop 0.6 s
    const whens: number[] = [];
    for (let i = 0; i < 100; i++) {
      s.tick();
      f.advance(0.025);
    }
    for (const p of f.played) whens.push(p.when);
    // 2.5 s of playback: prefix (kick@2.0, snare@2.5) then 3 full loops (kick, snare each) plus a partial.
    const kicks = f.played.filter((p) => p.art === 'kick');
    const snares = f.played.filter((p) => p.art === 'snare');
    expect(kicks.length).toBeGreaterThanOrEqual(4);
    expect(snares.length).toBeGreaterThanOrEqual(3);
    // Times strictly increase and consecutive kicks are one loop apart.
    for (let i = 1; i < whens.length; i++) expect(whens[i]).toBeGreaterThan(whens[i - 1]);
    for (let i = 1; i < kicks.length; i++) expect(kicks[i].when - kicks[i - 1].when).toBeCloseTo(0.6, 6);
    // Nothing was cancelled.
    expect(f.played.every((p) => p.stopped === null)).toBe(true);
  });

  it('a seek (new generation) cancels queued voices and restarts at the clock', () => {
    const f = fakeHost();
    const s = new KitScheduler(f.host, { lookaheadSeconds: 0.12 });
    s.setHits([hit('a', 'kick', 1.0), hit('b', 'kick', 5.0)], () => false);
    f.start(0.95, 10, null);
    s.tick();
    expect(f.played.length).toBe(1);
    f.start(4.95, 10, null); // seek
    s.tick();
    expect(f.played[0].stopped).not.toBeNull();
    expect(f.played.length).toBe(2);
    expect(f.played[1].when).toBeCloseTo(f.ctxTime + 0.05, 6);
  });

  it('an edit reschedules: cancelled voices are replaced by the new hits', () => {
    const f = fakeHost();
    const s = new KitScheduler(f.host, { lookaheadSeconds: 0.12 });
    s.setHits([hit('a', 'kick', 1.0)], () => false);
    f.start(0.95, 10, null);
    s.tick();
    expect(f.played.length).toBe(1);
    s.setHits([hit('a', 'kick', 1.0), hit('u1', 'snare', 1.02)], () => false);
    s.tick();
    expect(f.played[0].stopped).not.toBeNull();
    expect(f.played.slice(1).map((p) => p.art)).toEqual(['kick', 'snare']);
  });

  it('row mutes skip hits; a stall cancels and requeues; stop cancels everything', () => {
    const f = fakeHost();
    const s = new KitScheduler(f.host, { lookaheadSeconds: 0.12 });
    s.setHits([hit('a', 'kick', 1.0), hit('b', 'snare', 1.05)], (art) => art === 'snare');
    f.start(0.95, 10, null);
    s.tick();
    expect(f.played.map((p) => p.art)).toEqual(['kick']);
    f.advance(0.01, true); // underrun: audio clock moved, stream did not
    s.tick();
    expect(f.played[0].stopped).not.toBeNull();
    expect(f.played.length).toBe(2);
    f.stop();
    s.tick();
    expect(f.played[1].stopped).not.toBeNull();
    expect(s.pending).toBe(0);
  });

  it('a closed or pedal hat chokes the open hat that is still ringing', () => {
    const f = fakeHost();
    const s = new KitScheduler(f.host, { lookaheadSeconds: 0.2 });
    s.setHits([hit('o', 'hho', 1.0), hit('c', 'hhc', 1.1), hit('o2', 'hho', 1.15)], () => false);
    f.start(0.98, 10, null);
    s.tick();
    const open = f.played.find((p) => p.art === 'hho')!;
    expect(open.stopped).toBeCloseTo(f.ctxTime + 0.12, 6);
    const open2 = f.played.filter((p) => p.art === 'hho')[1];
    expect(open2.stopped).toBeNull();
  });

  it('lowerBound finds the first hit at or after t', () => {
    const hits = [hit('a', 'kick', 1), hit('b', 'kick', 2), hit('c', 'kick', 2), hit('d', 'kick', 3)];
    expect(lowerBound(hits, 0)).toBe(0);
    expect(lowerBound(hits, 2)).toBe(1);
    expect(lowerBound(hits, 2.5)).toBe(3);
    expect(lowerBound(hits, 9)).toBe(4);
  });
});
