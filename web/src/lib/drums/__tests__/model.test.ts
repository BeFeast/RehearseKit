import { describe, expect, it } from 'vitest';
import { HISTORY_LIMIT, editorReducer, fromDoc, initialEditor, primary, selectedHits, toEvents, type EditorAction, type EditorState } from '../model';
import { velFrom127, velTo127, type Articulation } from '../taxonomy';
import type { DrumDoc, DrumEvent } from '../types';

const DURATION = 240.5;

function model(i: number, art: Articulation, t: number, vel: number): DrumEvent {
  return { id: `m${i}`, art, t, vel, src: 'model', model: i };
}

/** Two bars of a rock beat as ADTOF returns them: onsets with float noise, velocities 0..1. */
const SEED: DrumEvent[] = [
  model(0, 'kick', 21.79, 0.694),
  model(1, 'hhc', 21.79, 0.512),
  model(2, 'hhc', 22.04, 0.488),
  model(3, 'snare', 22.29, 0.811),
  model(4, 'hhc', 22.29, 0.503),
  model(5, 'hhc', 22.54, 0.47),
  model(6, 'kick', 22.79, 0.702),
  model(7, 'hhc', 22.79, 0.51),
  model(8, 'kick', 22.917, 0.231), // the false kick
  model(9, 'hhc', 23.04, 0.495),
  model(10, 'snare', 23.29, 0.834),
  model(11, 'tomf', 23.54, 0.612),
  model(12, 'crash', 23.79, 0.9),
];

function doc(events: DrumEvent[]): DrumDoc {
  return {
    version: 1,
    stem: 'drums',
    model_rev: { adapter: 'adtof', model: 'adtof-frame', count: SEED.length, notes_sha256: 'deadbeef' },
    edit_rev: 3,
    exported_rev: 0,
    profile: 'gm',
    updated_at: '2026-09-28T10:00:00Z',
    events,
  };
}

const seeded = () => fromDoc(doc(SEED), DURATION);

function r(state: EditorState, ...actions: EditorAction[]): EditorState {
  return actions.reduce(editorReducer, state);
}

function byId(state: EditorState, id: string): DrumEvent {
  const h = state.hits.find((x) => x.id === id);
  if (!h) throw new Error(`no hit ${id}`);
  return h;
}

const times = (hits: DrumEvent[]) => hits.map((h) => h.t);

describe('D3b acceptance: ghost note, false kick, hat variant, moved snare, round trip', () => {
  it('edits survive the server round trip and untouched model hits keep their floats bit for bit', () => {
    let s = seeded();
    expect(s.nextId).toBe(1);
    expect(s.changes).toBe(0);

    // A ghost snare between the hats at 22.04 and 22.29.
    s = r(s, { type: 'add', art: 'snare', t: 22.165, vel: 0.2 });
    const ghost = primary(s);
    expect(ghost).toStrictEqual({ id: 'u1', art: 'snare', t: 22.165, vel: 0.2, src: 'manual' });
    expect(s.selection).toEqual(['u1']);
    expect(s.lastVel).toBe(0.2);

    // Delete the false kick.
    s = r(s, { type: 'select', ids: ['m8'], mode: 'replace' }, { type: 'delete' });
    expect(s.hits.some((h) => h.id === 'm8')).toBe(false);
    expect(s.selection).toEqual([]);

    // Closed hat → open → back to closed.
    s = r(s, { type: 'select', ids: ['m2'], mode: 'replace' }, { type: 'setArt', art: 'hho' });
    expect(byId(s, 'm2')).toMatchObject({ art: 'hho', src: 'model', model: 2 });
    s = r(s, { type: 'setArt', art: 'hhc' });
    expect(byId(s, 'm2')).toStrictEqual(SEED[2]);

    // Move a snare by +11 ms in one drag gesture (snap off), three pointer moves.
    s = r(s, { type: 'select', ids: ['m3'], mode: 'replace' });
    const pastBefore = s.past.length;
    s = r(s, { type: 'gestureBegin' });
    const base = { m3: { t: SEED[3].t, art: 'snare' as const } };
    for (const dt of [0.004, 0.008, 0.011]) s = r(s, { type: 'moveTo', base, dt, art: 'snare' });
    s = r(s, { type: 'gestureEnd' });
    expect(s.inGesture).toBe(false);
    // The whole drag is one undo step.
    expect(s.past.length).toBe(pastBefore + 1);
    const moved = byId(s, 'm3');
    expect(moved.t).toBe(SEED[3].t + 0.011);
    expect(moved).toMatchObject({ src: 'model', model: 3, art: 'snare' });
    expect(Object.is(moved.vel, SEED[3].vel)).toBe(true);

    // Untouched model hits are the very objects the doc gave us.
    for (const seed of SEED) {
      if (['m2', 'm3', 'm8'].includes(seed.id)) continue;
      expect(byId(s, seed.id)).toBe(seed);
    }

    // Export, "send" through JSON, load again: identical events.
    const out = toEvents(s);
    expect(times(out)).toEqual([...times(out)].sort((a, b) => a - b));
    expect(out.find((e) => e.id === 'u1')).toStrictEqual({ id: 'u1', art: 'snare', t: 22.165, vel: 0.2, src: 'manual' });
    expect(Object.keys(out.find((e) => e.id === 'u1')!)).toEqual(['id', 'art', 't', 'vel', 'src']);
    const wire = JSON.parse(JSON.stringify({ ...doc([]), events: out })) as DrumDoc;
    const back = fromDoc(wire, DURATION);
    expect(toEvents(back)).toStrictEqual(out);
    expect(back.hits).toStrictEqual(out);
    expect(back.nextId).toBe(2);
    for (const seed of SEED) {
      if (seed.id === 'm3' || seed.id === 'm8') continue;
      const h = byId(back, seed.id);
      expect(Object.is(h.t, seed.t)).toBe(true);
      expect(Object.is(h.vel, seed.vel)).toBe(true);
      expect(h.model).toBe(seed.model);
      expect(h.art).toBe(seed.art);
    }
    expect(byId(back, 'm3').t).toBe(SEED[3].t + 0.011);
  });
});

describe('history', () => {
  it('a 40-step drag is one undo step; undo and redo keep the selection', () => {
    let s = r(seeded(), { type: 'select', ids: ['m0', 'm6'], mode: 'replace' });
    const changes0 = s.changes;
    s = r(s, { type: 'gestureBegin' });
    expect(s.inGesture).toBe(true);
    const base = { m0: { t: SEED[0].t, art: 'kick' as const }, m6: { t: SEED[6].t, art: 'kick' as const } };
    for (let i = 1; i <= 40; i++) s = r(s, { type: 'moveTo', base, dt: i * 0.005 });
    s = r(s, { type: 'gestureEnd' });
    expect(s.past).toHaveLength(1);
    expect(s.future).toHaveLength(0);
    expect(s.changes).toBe(changes0 + 40);
    expect(byId(s, 'm0').t).toBe(SEED[0].t + 40 * 0.005);
    expect(byId(s, 'm6').t).toBe(SEED[6].t + 40 * 0.005);
    expect(byId(s, 'm0')).toMatchObject({ src: 'model', model: 0 });

    s = r(s, { type: 'undo' });
    expect(byId(s, 'm0')).toBe(SEED[0]);
    expect(byId(s, 'm6')).toBe(SEED[6]);
    expect(s.selection).toEqual(['m0', 'm6']);
    expect(s.past).toHaveLength(0);
    expect(s.future).toHaveLength(1);
    expect(s.changes).toBe(changes0 + 41);

    s = r(s, { type: 'redo' });
    expect(byId(s, 'm0').t).toBe(SEED[0].t + 40 * 0.005);
    expect(s.selection).toEqual(['m0', 'm6']);
    expect(s.past).toHaveLength(1);
    expect(s.future).toHaveLength(0);
  });

  it('undo drops ids that no longer exist from the selection', () => {
    let s = r(seeded(), { type: 'add', art: 'kick', t: 10 }, { type: 'select', ids: ['m0'], mode: 'add' });
    expect(s.selection).toEqual(['u1', 'm0']);
    s = r(s, { type: 'undo' });
    expect(s.selection).toEqual(['m0']);
    expect(s.hits.some((h) => h.id === 'u1')).toBe(false);
    s = r(s, { type: 'redo' });
    expect(s.selection).toEqual(['m0']);
    expect(byId(s, 'u1').t).toBe(10);
  });

  it('caps the undo stack at HISTORY_LIMIT and refuses to undo past it', () => {
    let s = r(seeded(), { type: 'select', ids: ['m12'], mode: 'replace' });
    for (let i = 0; i < HISTORY_LIMIT + 20; i++) s = r(s, { type: 'nudge', dt: 0.001 });
    expect(s.past).toHaveLength(HISTORY_LIMIT);
    for (let i = 0; i < HISTORY_LIMIT; i++) s = r(s, { type: 'undo' });
    expect(s.past).toHaveLength(0);
    expect(s.future).toHaveLength(HISTORY_LIMIT);
    expect(byId(s, 'm12').t).toBeCloseTo(SEED[12].t + 20 * 0.001, 9);
    expect(r(s, { type: 'undo' })).toBe(s);
  });

  it('a change clears the redo stack; a gesture that changed nothing leaves no history entry', () => {
    let s = r(seeded(), { type: 'add', art: 'kick', t: 10 }, { type: 'undo' });
    expect(s.future).toHaveLength(1);
    s = r(s, { type: 'gestureBegin' }, { type: 'gestureEnd' });
    expect(s.past).toHaveLength(0);
    expect(s.future).toHaveLength(1);
    expect(s.inGesture).toBe(false);
    s = r(s, { type: 'gestureBegin' }, { type: 'moveTo', base: { m0: { t: SEED[0].t, art: 'kick' } }, dt: 0.1 }, { type: 'gestureEnd' });
    expect(s.past).toHaveLength(1);
    expect(s.future).toHaveLength(0);
  });

  it('ignores undo/redo and a second gestureBegin while a gesture is active', () => {
    let s = r(seeded(), { type: 'add', art: 'kick', t: 10 }, { type: 'gestureBegin' });
    expect(r(s, { type: 'undo' })).toBe(s);
    expect(r(s, { type: 'gestureBegin' })).toBe(s);
    s = r(s, { type: 'gestureEnd' });
    expect(r(s, { type: 'gestureEnd' })).toBe(s);
    expect(r(s, { type: 'undo' }).hits.some((h) => h.id === 'u1')).toBe(false);
  });

  it('erase strokes inside a gesture share one undo step; outside they push their own', () => {
    let s = r(seeded(), { type: 'select', ids: ['m5', 'm7'], mode: 'replace' }, { type: 'gestureBegin' });
    s = r(s, { type: 'erase', id: 'm5' }, { type: 'erase', id: 'm6' }, { type: 'erase', id: 'm7' }, { type: 'gestureEnd' });
    expect(s.hits).toHaveLength(SEED.length - 3);
    expect(s.selection).toEqual([]);
    expect(s.past).toHaveLength(1);
    s = r(s, { type: 'undo' });
    expect(s.hits).toHaveLength(SEED.length);
    s = r(s, { type: 'erase', id: 'm0' }, { type: 'erase', id: 'm1' });
    expect(s.past).toHaveLength(2);
    expect(r(s, { type: 'erase', id: 'nope' })).toBe(s);
  });
});

describe('clipboard', () => {
  it('copy is relative to the earliest hit; paste keeps offsets, mints fresh ids, never collides after undo', () => {
    let s = r(seeded(), { type: 'select', ids: ['m5', 'm3', 'm4'], mode: 'replace' }, { type: 'copy' });
    expect(s.clipboard).toEqual([
      { art: 'snare', dt: 0, vel: SEED[3].vel },
      { art: 'hhc', dt: 0, vel: SEED[4].vel },
      { art: 'hhc', dt: SEED[5].t - SEED[3].t, vel: SEED[5].vel },
    ]);
    expect(s.past).toHaveLength(0);

    s = r(s, { type: 'paste', at: 30 });
    expect(s.selection).toEqual(['u1', 'u2', 'u3']);
    const pasted = selectedHits(s);
    expect(times(pasted)).toEqual([30, 30, 30 + (SEED[5].t - SEED[3].t)]);
    expect(pasted.map((h) => h.art)).toEqual(['snare', 'hhc', 'hhc']);
    for (const h of pasted) expect(h).toStrictEqual({ id: h.id, art: h.art, t: h.t, vel: h.vel, src: 'manual' });
    expect(s.nextId).toBe(4);

    s = r(s, { type: 'undo' });
    expect(s.selection).toEqual([]);
    expect(s.nextId).toBe(4);
    s = r(s, { type: 'paste', at: 40 });
    expect(s.selection).toEqual(['u4', 'u5', 'u6']);
    expect(s.future).toHaveLength(0);
    const ids = s.hits.map((h) => h.id);
    expect(new Set(ids).size).toBe(ids.length);
    expect(s.clipboard).toHaveLength(3);
  });

  it('duplicate shifts the selection by offset and selects the copies', () => {
    let s = r(seeded(), { type: 'select', ids: ['m1', 'm0'], mode: 'replace' }, { type: 'duplicate', offset: 2 });
    expect(s.selection).toEqual(['u1', 'u2']);
    const copies = selectedHits(s);
    expect(copies).toStrictEqual([
      { id: 'u1', art: 'kick', t: SEED[0].t + 2, vel: SEED[0].vel, src: 'manual' },
      { id: 'u2', art: 'hhc', t: SEED[1].t + 2, vel: SEED[1].vel, src: 'manual' },
    ]);
    expect(byId(s, 'm0')).toBe(SEED[0]);
    expect(byId(s, 'm1')).toBe(SEED[1]);
    expect(s.hits).toHaveLength(SEED.length + 2);
    expect(times(s.hits)).toEqual([...times(s.hits)].sort((a, b) => a - b));
    // A second duplicate of the copies goes on counting.
    s = r(s, { type: 'duplicate', offset: 2 });
    expect(s.selection).toEqual(['u3', 'u4']);
  });

  it('ids start above the highest manual id in the loaded doc and paste clamps to the duration', () => {
    const extra: DrumEvent = { id: 'u7', art: 'stick', t: 25.5, vel: 0.3, src: 'manual' };
    let s = fromDoc(doc([...SEED, extra]), 30);
    expect(s.nextId).toBe(8);
    s = r(s, { type: 'select', ids: ['u7'], mode: 'replace' }, { type: 'copy' }, { type: 'paste', at: 31 });
    expect(s.selection).toEqual(['u8']);
    expect(byId(s, 'u8').t).toBe(30);
    s = r(s, { type: 'paste', at: -5 });
    expect(byId(s, 'u9').t).toBe(0);
  });
});

describe('quantize', () => {
  const snap = (t: number) => Math.round(t * 4) / 4;

  it('touches only the selection and honours strength', () => {
    let s = r(seeded(), { type: 'select', ids: ['m0'], mode: 'replace' }, { type: 'quantize', snap, strength: 0.5 });
    expect(byId(s, 'm0').t).toBe(SEED[0].t + (snap(SEED[0].t) - SEED[0].t) * 0.5);
    expect(byId(s, 'm0')).toMatchObject({ src: 'model', model: 0 });
    expect(byId(s, 'm1')).toBe(SEED[1]); // same onset, not selected
    for (let i = 2; i < SEED.length; i++) expect(byId(s, `m${i}`)).toBe(SEED[i]);
    s = r(s, { type: 'quantize', snap, strength: 1 });
    expect(byId(s, 'm0').t).toBe(21.75);
    expect(s.past).toHaveLength(2);
  });

  it('strength 0 or an empty selection is a no-op', () => {
    const s = seeded();
    expect(r(s, { type: 'quantize', snap, strength: 0.5 })).toBe(s);
    const sel = r(s, { type: 'select', ids: ['m0'], mode: 'replace' });
    expect(r(sel, { type: 'quantize', snap, strength: 0 })).toBe(sel);
    expect(r(r(sel, { type: 'setTime', t: 21.75 }), { type: 'quantize', snap, strength: 1 }).past).toHaveLength(1);
  });
});

describe('velocity', () => {
  it('velAdd maps through 1..127 and clamps', () => {
    let s = r(seeded(), { type: 'select', ids: ['m12', 'm8'], mode: 'replace' });
    expect(velTo127(SEED[12].vel)).toBe(114);
    expect(velTo127(SEED[8].vel)).toBe(29);
    s = r(s, { type: 'velAdd', delta: 10 });
    expect(byId(s, 'm12').vel).toBe(velFrom127(124));
    expect(byId(s, 'm8').vel).toBe(velFrom127(39));
    s = r(s, { type: 'velAdd', delta: 10 });
    expect(byId(s, 'm12').vel).toBe(1);
    expect(byId(s, 'm8').vel).toBe(velFrom127(49));
    s = r(s, { type: 'velAdd', delta: -200 });
    expect(byId(s, 'm12').vel).toBe(velFrom127(1));
    expect(byId(s, 'm8').vel).toBe(velFrom127(1));
    expect(r(s, { type: 'velAdd', delta: -10 })).toBe(s);
    expect(byId(s, 'm0')).toBe(SEED[0]);
  });

  it('compress pulls toward 64', () => {
    const s = r(seeded(), { type: 'select', ids: ['m12', 'm8'], mode: 'replace' }, { type: 'compress' });
    expect(velTo127(byId(s, 'm12').vel)).toBe(89); // 64 + 50 * 0.5
    expect(velTo127(byId(s, 'm8').vel)).toBe(47); // round(64 - 35 * 0.5) = round(46.5)
    expect(byId(s, 'm12').vel).toBe(velFrom127(89));
  });

  it('setVel clamps to (0, 1], sets lastVel, and feeds the next add', () => {
    let s = r(seeded(), { type: 'select', ids: ['m0'], mode: 'replace' }, { type: 'setVel', vel: 0 });
    expect(byId(s, 'm0').vel).toBe(velFrom127(1));
    expect(s.lastVel).toBe(velFrom127(1));
    s = r(s, { type: 'setVel', vel: 1.5 });
    expect(byId(s, 'm0').vel).toBe(1);
    s = r(s, { type: 'setVel', vel: 0.5 });
    expect(byId(s, 'm0').vel).toBe(0.5);
    expect(s.past).toHaveLength(3);
    s = r(s, { type: 'select', ids: [], mode: 'replace' }, { type: 'setVel', vel: 0.75 });
    expect(s.lastVel).toBe(0.75);
    expect(s.past).toHaveLength(3);
    s = r(s, { type: 'add', art: 'kick', t: 5 });
    expect(byId(s, 'u1').vel).toBe(0.75);
    s = r(s, { type: 'add', art: 'kick', t: 6, vel: 0 });
    expect(byId(s, 'u2').vel).toBe(velFrom127(1));
    expect(s.lastVel).toBe(velFrom127(1));
  });

  it('velDraw inside a gesture is one undo step and clamps', () => {
    let s = r(seeded(), { type: 'gestureBegin' });
    for (let i = 1; i <= 10; i++) s = r(s, { type: 'velDraw', vels: { m1: i / 10, m2: 1 - i / 10, m5: 5, nope: 0.5 } });
    s = r(s, { type: 'gestureEnd' });
    expect(s.past).toHaveLength(1);
    expect(byId(s, 'm1').vel).toBe(1);
    expect(byId(s, 'm2').vel).toBe(velFrom127(1));
    expect(byId(s, 'm5').vel).toBe(1);
    expect(byId(s, 'm1')).toMatchObject({ src: 'model', model: 1, t: SEED[1].t });
    s = r(s, { type: 'undo' });
    expect(byId(s, 'm1')).toBe(SEED[1]);
    expect(byId(s, 'm2')).toBe(SEED[2]);
    expect(s.past).toHaveLength(0);
  });
});

describe('selection', () => {
  it('selectRange filters by articulation, accepts either bound order, and picks the last as primary', () => {
    let s = r(seeded(), { type: 'selectRange', t0: 23, t1: 22, arts: ['hhc'] });
    expect(s.selection).toEqual(['m2', 'm4', 'm5', 'm7']);
    expect(primary(s)?.id).toBe('m7');
    expect(selectedHits(s).map((h) => h.art)).toEqual(['hhc', 'hhc', 'hhc', 'hhc']);
    s = r(s, { type: 'selectRange', t0: 21.79, t1: 21.79 });
    expect(s.selection).toEqual(['m0', 'm1']);
    s = r(s, { type: 'selectRange', t0: 23.5, t1: 24, arts: ['crash', 'tomf'], mode: 'add' });
    expect(s.selection).toEqual(['m0', 'm1', 'm11', 'm12']);
    expect(primary(s)?.id).toBe('m12');
    expect(s.past).toHaveLength(0);
    expect(s.changes).toBe(0);
  });

  it('select: replace/add/toggle, primary is the last id given, unknown ids are dropped', () => {
    let s = r(seeded(), { type: 'select', ids: ['m3', 'm0', 'ghost'], mode: 'replace' });
    expect(s.selection).toEqual(['m3', 'm0']);
    expect(primary(s)).toBe(SEED[0]);
    s = r(s, { type: 'select', ids: ['m5', 'm3'], mode: 'add' });
    expect(s.selection).toEqual(['m0', 'm5', 'm3']);
    expect(primary(s)).toBe(SEED[3]);
    s = r(s, { type: 'select', ids: ['m5', 'm11'], mode: 'toggle' });
    expect(s.selection).toEqual(['m0', 'm3', 'm11']);
    expect(primary(s)).toBe(SEED[11]);
    s = r(s, { type: 'select', ids: ['m11', 'm11'], mode: 'toggle' });
    expect(s.selection).toEqual(['m0', 'm3']);
    expect(selectedHits(s)).toEqual([SEED[0], SEED[3]]);
    s = r(s, { type: 'select', ids: [], mode: 'replace' });
    expect(s.selection).toEqual([]);
    expect(primary(s)).toBeNull();
    expect(selectedHits(s)).toEqual([]);
  });

  it('setTime moves the primary and drags the rest of the selection along', () => {
    let s = r(seeded(), { type: 'select', ids: ['m0', 'm1', 'm3'], mode: 'replace' }, { type: 'setTime', t: 23.29 });
    const dt = 23.29 - SEED[3].t;
    expect(byId(s, 'm3').t).toBe(23.29);
    expect(byId(s, 'm0').t).toBe(SEED[0].t + dt);
    expect(byId(s, 'm1').t).toBe(SEED[1].t + dt);
    expect(byId(s, 'm2')).toBe(SEED[2]);
    s = r(s, { type: 'setTime', t: -3 });
    expect(byId(s, 'm3').t).toBe(0);
    expect(byId(s, 'm0').t).toBe(0);
    expect(times(s.hits)).toEqual([...times(s.hits)].sort((a, b) => a - b));
  });
});

describe('placement and clamps', () => {
  it('add clamps t to [0, duration], nudge and moveTo clamp too; duration <= 0 is unbounded', () => {
    let s = r(initialEditor(10), { type: 'add', art: 'kick', t: 12 }, { type: 'add', art: 'kick', t: -1 });
    expect(times(s.hits)).toEqual([0, 10]);
    expect(s.hits.map((h) => h.id)).toEqual(['u2', 'u1']);
    s = r(s, { type: 'select', ids: ['u1'], mode: 'replace' }, { type: 'nudge', dt: 5 });
    expect(byId(s, 'u1').t).toBe(10);
    s = r(s, { type: 'moveTo', base: { u1: { t: 10, art: 'kick' } }, dt: -20 });
    expect(byId(s, 'u1').t).toBe(0);
    const open = r(initialEditor(0), { type: 'add', art: 'kick', t: 1e6 });
    expect(open.hits[0].t).toBe(1e6);
  });

  it('moveTo re-articulates a single dragged hit and leaves unknown ids alone', () => {
    let s = r(seeded(), { type: 'moveTo', base: { m4: { t: SEED[4].t, art: 'hhc' }, zzz: { t: 1, art: 'kick' } }, dt: 0, art: 'hho' });
    expect(byId(s, 'm4')).toStrictEqual({ ...SEED[4], art: 'hho' });
    expect(s.hits).toHaveLength(SEED.length);
    expect(s.past).toHaveLength(1);
    s = r(s, { type: 'moveTo', base: { m4: { t: SEED[4].t, art: 'hhc' } }, dt: 0, art: null });
    expect(byId(s, 'm4')).toStrictEqual(SEED[4]);
  });

  it('keeps hits stably sorted by t and never re-derives untouched floats', () => {
    const shuffled = [SEED[12], SEED[1], SEED[0], SEED[3], SEED[2]];
    const s = initialEditor(DURATION, shuffled);
    expect(s.hits).toEqual([SEED[1], SEED[0], SEED[2], SEED[3], SEED[12]]);
    expect(shuffled[0]).toBe(SEED[12]); // the input array is not mutated
    const added = r(s, { type: 'add', art: 'kick', t: SEED[0].t });
    expect(added.hits.map((h) => h.id)).toEqual(['m1', 'm0', 'u1', 'm2', 'm3', 'm12']);
    for (const h of added.hits) if (h.src === 'model') expect(SEED).toContain(h);
  });

  it('load replaces hits, clears history and selection, keeps the clipboard, never lowers nextId', () => {
    let s = r(seeded(), { type: 'select', ids: ['m0'], mode: 'replace' }, { type: 'copy' }, { type: 'add', art: 'kick', t: 1 }, { type: 'add', art: 'kick', t: 2 });
    expect(s.nextId).toBe(3);
    const changes = s.changes;
    s = r(s, { type: 'load', hits: [SEED[3], SEED[0]] });
    expect(s.hits).toEqual([SEED[0], SEED[3]]);
    expect(s.selection).toEqual([]);
    expect(s.past).toEqual([]);
    expect(s.future).toEqual([]);
    expect(s.clipboard).toHaveLength(1);
    expect(s.nextId).toBe(3);
    expect(s.changes).toBe(changes + 1);
    s = r(s, { type: 'load', hits: [{ id: 'u9', art: 'ride', t: 3, vel: 0.5, src: 'manual' }] });
    expect(s.nextId).toBe(10);
    expect(r(s, { type: 'add', art: 'kick', t: 4 }).selection).toEqual(['u10']);
  });

  it('toEvents strips the model index from manual hits and keeps it on model hits', () => {
    const s = r(seeded(), { type: 'add', art: 'kick', t: 5 });
    const out = toEvents(s);
    expect(out[0]).toStrictEqual({ id: 'u1', art: 'kick', t: 5, vel: velFrom127(100), src: 'manual' });
    expect(out[1]).toStrictEqual(SEED[0]);
    expect(out[1]).not.toBe(SEED[0]);
    expect(out).toHaveLength(SEED.length + 1);
    expect(s.hits[0]).toBe(byId(s, 'u1')); // the state itself was not touched
  });
});

describe('no-op actions return the same state object', () => {
  it('with nothing selected', () => {
    const s = seeded();
    const noops: EditorAction[] = [
      { type: 'delete' },
      { type: 'undo' },
      { type: 'redo' },
      { type: 'copy' },
      { type: 'paste', at: 1 },
      { type: 'duplicate', offset: 1 },
      { type: 'nudge', dt: 0.1 },
      { type: 'setArt', art: 'kick' },
      { type: 'velAdd', delta: 10 },
      { type: 'compress' },
      { type: 'setTime', t: 3 },
      { type: 'quantize', snap: (t) => t, strength: 1 },
      { type: 'erase', id: 'nope' },
      { type: 'gestureEnd' },
      { type: 'select', ids: [], mode: 'replace' },
      { type: 'select', ids: ['nope'], mode: 'add' },
      { type: 'selectRange', t0: 100, t1: 101 },
      { type: 'moveTo', base: { nope: { t: 1, art: 'kick' } }, dt: 1 },
      { type: 'velDraw', vels: { m0: SEED[0].vel, nope: 0.2 } },
      { type: 'setVel', vel: velFrom127(100) },
    ];
    for (const a of noops) expect(r(s, a), a.type).toBe(s);
  });

  it('with a selection whose values already match', () => {
    const s = r(seeded(), { type: 'select', ids: ['m0', 'm6'], mode: 'replace' });
    const noops: EditorAction[] = [
      { type: 'select', ids: ['m0', 'm6'], mode: 'replace' },
      { type: 'select', ids: ['m6'], mode: 'add' },
      { type: 'selectRange', t0: 0, t1: 22.8, arts: ['kick'], mode: 'add' }, // m8 (22.917) is outside
      { type: 'setArt', art: 'kick' },
      { type: 'nudge', dt: 0 },
      { type: 'setTime', t: SEED[6].t },
      { type: 'moveTo', base: { m0: { t: SEED[0].t, art: 'kick' } }, dt: 0 },
      { type: 'quantize', snap: (t) => t, strength: 0.5 },
      { type: 'velDraw', vels: {} },
      { type: 'velAdd', delta: 0 },
    ];
    for (const a of noops) expect(r(s, a), a.type).toBe(s);
    expect(s.changes).toBe(0);
  });
});

describe('gesture edge cases', () => {
  it('a gesture that changes nothing leaves no history entry, even at the cap', () => {
    let s = seeded();
    for (let i = 0; i < HISTORY_LIMIT + 5; i++) s = editorReducer(s, { type: 'add', art: 'kick', t: 0.01 * i, vel: 0.5 });
    expect(s.past.length).toBe(HISTORY_LIMIT);
    const oldest = s.past[0];
    const changes = s.changes;
    s = editorReducer(s, { type: 'gestureBegin' });
    s = editorReducer(s, { type: 'gestureEnd' });
    expect(s.past.length).toBe(HISTORY_LIMIT);
    expect(s.past[0]).toBe(oldest);
    expect(s.changes).toBe(changes);
  });

  it('a drag that returns to its start leaves no history entry and no change', () => {
    let s = seeded();
    s = editorReducer(s, { type: 'select', ids: ['m3'], mode: 'replace' });
    const before = s.hits;
    const changes = s.changes;
    const pastLen = s.past.length;
    const base = { m3: { t: SEED[3].t, art: 'snare' as const } };
    s = editorReducer(s, { type: 'gestureBegin' });
    s = editorReducer(s, { type: 'moveTo', base, dt: 0.1, art: 'snare' });
    expect(s.past.length).toBe(pastLen + 1);
    s = editorReducer(s, { type: 'moveTo', base, dt: 0, art: 'snare' });
    s = editorReducer(s, { type: 'gestureEnd' });
    expect(s.hits).toBe(before);
    expect(s.past.length).toBe(pastLen);
    expect(s.changes).toBe(changes);
    expect(s.inGesture).toBe(false);
  });
});

describe('gesture rollback at the history cap', () => {
  it('a drag that returns to its start keeps every undo step and the redo stack', () => {
    let s = seeded();
    for (let i = 0; i < HISTORY_LIMIT; i++) s = editorReducer(s, { type: 'add', art: 'kick', t: 0.01 * i, vel: 0.5 });
    s = editorReducer(s, { type: 'undo' });
    expect(s.past.length).toBe(HISTORY_LIMIT - 1);
    s = editorReducer(s, { type: 'add', art: 'snare', t: 3, vel: 0.5 });
    expect(s.past.length).toBe(HISTORY_LIMIT);
    const pastBefore = s.past;
    const oldest = s.past[0];
    s = editorReducer(s, { type: 'undo' });
    const futureBefore = s.future;
    s = editorReducer(s, { type: 'redo' });
    expect(s.past).toStrictEqual(pastBefore);
    const past = s.past;
    const base = { m3: { t: SEED[3].t, art: 'snare' as const } };
    s = editorReducer(s, { type: 'gestureBegin' });
    s = editorReducer(s, { type: 'moveTo', base, dt: 0.1, art: 'snare' });
    expect(s.past[0]).not.toBe(oldest); // the first change evicted the oldest entry
    s = editorReducer(s, { type: 'moveTo', base, dt: 0, art: 'snare' });
    s = editorReducer(s, { type: 'gestureEnd' });
    expect(s.past).toBe(past);
    expect(s.past[0]).toBe(oldest);
    expect(futureBefore.length).toBe(1);
  });
});
