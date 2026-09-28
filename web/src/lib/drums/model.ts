/**
 * The drum editor's pure edit model: a reducer over hits with selection,
 * in-memory undo/redo, a clipboard, and gesture commits (a drag is one undo
 * step). No DOM, no timers — the page owns autosave (it watches `changes`)
 * and the server owns validation (internal/drums/doc.go); this mirrors the
 * server's rules so a save never bounces: t in [0, duration], vel in (0, 1],
 * manual hits carry no model index.
 *
 * Model hits are never re-derived: an untouched hit keeps the object the
 * notes file gave us, so t and vel survive bit for bit. A moved or
 * re-articulated model hit stays src 'model' with its index (the inspector
 * shows where it came from). New hits are src 'manual' with ids 'u<n>';
 * `nextId` only ever grows within a session, so an id is never reused even
 * after an undo.
 */

import { velFrom127, velTo127, type Articulation } from './taxonomy';
import type { DrumDoc, DrumEvent } from './types';

/** One clipboard entry: offset from the earliest copied hit (dt >= 0). */
export interface ClipItem {
  art: Articulation;
  dt: number;
  vel: number;
}

export interface EditorState {
  /** Always sorted by t ascending (stable: equal times keep insertion order). */
  hits: DrumEvent[];
  /** Ids in selection order; the last one is the primary. Never contains an unknown id. */
  selection: string[];
  /** Undo stack of previous `hits` arrays, oldest first; capped at HISTORY_LIMIT. */
  past: DrumEvent[][];
  /** Redo stack, next first. */
  future: DrumEvent[][];
  clipboard: ClipItem[] | null;
  /** Velocity (0..1) the next added hit gets; follows add and setVel. */
  lastVel: number;
  /** Next manual id number ('u<n>'). Monotonic for the session. */
  nextId: number;
  /** Track length in seconds; <= 0 means unbounded. */
  duration: number;
  /** Between gestureBegin and gestureEnd: hits changes share one history entry (pushed by the first change). */
  inGesture: boolean;
  /** The hits, history and change count when the gesture began, restored exactly for a gesture that nets no change. */
  gestureFrom: DrumEvent[] | null;
  gestureChanges: number;
  gesturePast: DrumEvent[][] | null;
  gestureFuture: DrumEvent[][] | null;
  /** Increments on every hits change, including undo/redo — the autosave watches it. */
  changes: number;
}

export type SelectMode = 'replace' | 'add' | 'toggle';

export type EditorAction =
  /** Replace hits; clears history and selection, keeps the clipboard. */
  | { type: 'load'; hits: DrumEvent[] }
  /** New manual hit; vel defaults to lastVel; selects it; sets lastVel. */
  | { type: 'add'; art: Articulation; t: number; vel?: number }
  /** Erase tool: remove one hit (inside a gesture this shares the gesture's undo step). */
  | { type: 'erase'; id: string }
  /** Delete the selection. */
  | { type: 'delete' }
  /** Primary = last id given. 'add' moves already-selected ids to the end. */
  | { type: 'select'; ids: string[]; mode: SelectMode }
  /** Marquee / select-all-in-view: hits with t in [t0, t1] (either order), optionally limited to arts. Default mode 'replace'. */
  | { type: 'selectRange'; t0: number; t1: number; arts?: Articulation[]; mode?: 'replace' | 'add' }
  /**
   * Begin pushes one history entry (if no gesture is active); hits changes inside the gesture do
   * not push; end sorts and clears inGesture. A gesture that changed nothing leaves no history entry.
   */
  | { type: 'gestureBegin' }
  | { type: 'gestureEnd' }
  /**
   * Absolute placement from the drag's base: for each id in base, t = clamp(base.t + dt),
   * art = action.art ?? base.art. The caller passes art only when a single hit is dragged.
   */
  | { type: 'moveTo'; base: Record<string, { t: number; art: Articulation }>; dt: number; art?: Articulation | null }
  /** Re-articulate the selection. */
  | { type: 'setArt'; art: Articulation }
  /** Set the selection's velocity (0..1) and lastVel. */
  | { type: 'setVel'; vel: number }
  /** Add delta (in 1..127 units, e.g. ±10) to the selection's velocity, clamped 1..127. */
  | { type: 'velAdd'; delta: number }
  /** Selection: v127 = round(64 + (v127 - 64) * 0.5). */
  | { type: 'compress' }
  /** Set vel (0..1) per id — velocity lane drawing, usually inside a gesture. */
  | { type: 'velDraw'; vels: Record<string, number> }
  /** Move the primary to t and the rest of the selection by the same dt. */
  | { type: 'setTime'; t: number }
  /** Selection only: t += (snap(t) - t) * strength. Strength >= 1 lands exactly on snap(t). */
  | { type: 'quantize'; snap: (t: number) => number; strength: number }
  /** Shift the selection by dt seconds. */
  | { type: 'nudge'; dt: number }
  /** Clipboard = selection relative to its earliest hit. */
  | { type: 'copy' }
  /** Paste the clipboard at `at` (+ each item's dt) as new manual hits; selects them. */
  | { type: 'paste'; at: number }
  /** Copy the selection shifted by offset seconds (the caller computes the bar length); selects the copies. */
  | { type: 'duplicate'; offset: number }
  /** Selection is filtered to surviving ids. Ignored inside a gesture. */
  | { type: 'undo' }
  | { type: 'redo' };

export const HISTORY_LIMIT = 100;

/** Lowest velocity the server accepts (it rejects 0). */
const VEL_MIN = velFrom127(1);
const DEFAULT_VEL = velFrom127(100);
const MANUAL_ID = /^u(\d+)$/;

export function initialEditor(duration: number, hits: DrumEvent[] = []): EditorState {
  return {
    hits: sortHits(hits.slice()),
    selection: [],
    past: [],
    future: [],
    clipboard: null,
    lastVel: DEFAULT_VEL,
    nextId: nextManualId(hits, 1),
    duration: Number.isFinite(duration) ? duration : 0,
    inGesture: false,
    gestureFrom: null,
    gestureChanges: 0,
    gesturePast: null,
    gestureFuture: null,
    changes: 0,
  };
}

/** Editor state over doc.events (stable-sorted by t; the event objects are kept as given). */
export function fromDoc(doc: DrumDoc, duration: number): EditorState {
  return initialEditor(duration, doc.events);
}

/** Sorted copy of the hits as the server wants them: manual hits carry no model index, no undefined fields. */
export function toEvents(state: EditorState): DrumEvent[] {
  return sortHits(state.hits.map((h) => {
    const e: DrumEvent = { id: h.id, art: h.art, t: h.t, vel: h.vel, src: h.src };
    if (h.src === 'model' && h.model !== undefined) e.model = h.model;
    return e;
  }));
}

/** The primary (last selected) hit, or null. */
export function primary(state: EditorState): DrumEvent | null {
  const id = state.selection[state.selection.length - 1];
  if (id === undefined) return null;
  return state.hits.find((h) => h.id === id) ?? null;
}

/** Selected hits in selection order (the last one is the primary). */
export function selectedHits(state: EditorState): DrumEvent[] {
  if (state.selection.length === 0) return [];
  const byId = indexById(state.hits);
  const out: DrumEvent[] = [];
  for (const id of state.selection) {
    const h = byId.get(id);
    if (h) out.push(h);
  }
  return out;
}

/** Pure; returns the same object when the action changes nothing. */
export function editorReducer(state: EditorState, action: EditorAction): EditorState {
  switch (action.type) {
    case 'load':
      return {
        ...state,
        hits: sortHits(action.hits.slice()),
        selection: [],
        past: [],
        future: [],
        nextId: nextManualId(action.hits, state.nextId),
        inGesture: false,
        gestureFrom: null,
        gestureChanges: 0,
        gesturePast: null,
        gestureFuture: null,
        changes: state.changes + 1,
      };

    case 'add': {
      const vel = clampVel(action.vel ?? state.lastVel);
      const id = `u${state.nextId}`;
      const hit: DrumEvent = { id, art: action.art, t: clampT(action.t, state.duration), vel, src: 'manual' };
      return commit(state, [...state.hits, hit], { selection: [id], lastVel: vel, nextId: state.nextId + 1 });
    }

    case 'erase': {
      const i = state.hits.findIndex((h) => h.id === action.id);
      if (i < 0) return state;
      const hits = state.hits.slice();
      hits.splice(i, 1);
      return commit(state, hits, { selection: without(state.selection, new Set([action.id])) });
    }

    case 'delete': {
      if (state.selection.length === 0) return state;
      const sel = new Set(state.selection);
      return commit(state, state.hits.filter((h) => !sel.has(h.id)), { selection: [] });
    }

    case 'select': {
      const known = indexById(state.hits);
      const given = dedupe(action.ids.filter((id) => known.has(id)));
      let selection: string[];
      if (action.mode === 'replace') {
        selection = given;
      } else if (action.mode === 'add') {
        selection = [...without(state.selection, new Set(given)), ...given];
      } else {
        selection = state.selection.slice();
        for (const id of given) {
          const i = selection.indexOf(id);
          if (i >= 0) selection.splice(i, 1);
          else selection.push(id);
        }
      }
      return sameIds(selection, state.selection) ? state : { ...state, selection };
    }

    case 'selectRange': {
      const lo = Math.min(action.t0, action.t1);
      const hi = Math.max(action.t0, action.t1);
      const arts = action.arts ? new Set<Articulation>(action.arts) : null;
      const inRange: string[] = [];
      for (const h of state.hits) {
        if (h.t < lo) continue;
        if (h.t > hi) break;
        if (arts && !arts.has(h.art)) continue;
        inRange.push(h.id);
      }
      const selection = action.mode === 'add' ? [...without(state.selection, new Set(inRange)), ...inRange] : inRange;
      return sameIds(selection, state.selection) ? state : { ...state, selection };
    }

    case 'gestureBegin':
      if (state.inGesture) return state;
      // The history entry is pushed by the first change inside the gesture
      // (commit), so a click that changes nothing costs no undo step.
      return { ...state, inGesture: true, gestureFrom: state.hits, gestureChanges: state.changes, gesturePast: state.past, gestureFuture: state.future };

    case 'gestureEnd': {
      if (!state.inGesture) return state;
      const from = state.gestureFrom;
      const done = { inGesture: false, gestureFrom: null, gesturePast: null, gestureFuture: null };
      if (from === state.hits) return { ...state, ...done };
      // A drag that came back to where it started: restore the history as it
      // was (including an entry the first change evicted at the cap) and the
      // change count, so nothing is saved and no undo step is lost.
      if (from && sameHits(from, state.hits)) {
        return { ...state, ...done, hits: from, past: state.gesturePast ?? state.past, future: state.gestureFuture ?? state.future, changes: state.gestureChanges };
      }
      return { ...state, ...done, hits: isSorted(state.hits) ? state.hits : sortHits(state.hits.slice()) };
    }

    case 'moveTo': {
      const art = action.art ?? null;
      return commitIfChanged(state, (h) => {
        const b = action.base[h.id];
        if (!b) return h;
        const t = clampT(b.t + action.dt, state.duration);
        const a = art ?? b.art;
        return t === h.t && a === h.art ? h : { ...h, t, art: a };
      });
    }

    case 'setArt': {
      const sel = new Set(state.selection);
      return commitIfChanged(state, (h) => (sel.has(h.id) && h.art !== action.art ? { ...h, art: action.art } : h));
    }

    case 'setVel': {
      const vel = clampVel(action.vel);
      const sel = new Set(state.selection);
      const next = commitIfChanged(state, (h) => (sel.has(h.id) && h.vel !== vel ? { ...h, vel } : h), { lastVel: vel });
      if (next !== state) return next;
      return state.lastVel === vel ? state : { ...state, lastVel: vel };
    }

    case 'velAdd': {
      if (!Number.isFinite(action.delta) || action.delta === 0) return state;
      const sel = new Set(state.selection);
      return commitIfChanged(state, (h) => {
        if (!sel.has(h.id)) return h;
        const vel = velFrom127(velTo127(h.vel) + action.delta);
        return vel === h.vel ? h : { ...h, vel };
      });
    }

    case 'compress': {
      const sel = new Set(state.selection);
      return commitIfChanged(state, (h) => {
        if (!sel.has(h.id)) return h;
        const vel = velFrom127(Math.round(64 + (velTo127(h.vel) - 64) * 0.5));
        return vel === h.vel ? h : { ...h, vel };
      });
    }

    case 'velDraw':
      return commitIfChanged(state, (h) => {
        const raw = action.vels[h.id];
        if (raw === undefined) return h;
        const vel = clampVel(raw);
        return vel === h.vel ? h : { ...h, vel };
      });

    case 'setTime': {
      const p = primary(state);
      if (!p) return state;
      const target = clampT(action.t, state.duration);
      const dt = target - p.t;
      if (dt === 0) return state;
      const sel = new Set(state.selection);
      return commitIfChanged(state, (h) => {
        if (!sel.has(h.id)) return h;
        const t = h.id === p.id ? target : clampT(h.t + dt, state.duration);
        return t === h.t ? h : { ...h, t };
      });
    }

    case 'quantize': {
      if (state.selection.length === 0 || !(action.strength > 0)) return state;
      const sel = new Set(state.selection);
      const full = action.strength >= 1;
      return commitIfChanged(state, (h) => {
        if (!sel.has(h.id)) return h;
        const target = action.snap(h.t);
        if (!Number.isFinite(target)) return h;
        const t = clampT(full ? target : h.t + (target - h.t) * action.strength, state.duration);
        return t === h.t ? h : { ...h, t };
      });
    }

    case 'nudge': {
      if (state.selection.length === 0 || !action.dt) return state;
      const sel = new Set(state.selection);
      return commitIfChanged(state, (h) => {
        if (!sel.has(h.id)) return h;
        const t = clampT(h.t + action.dt, state.duration);
        return t === h.t ? h : { ...h, t };
      });
    }

    case 'copy': {
      const sel = new Set(state.selection);
      const picked = state.hits.filter((h) => sel.has(h.id));
      if (picked.length === 0) return state;
      const t0 = picked[0].t;
      return { ...state, clipboard: picked.map((h) => ({ art: h.art, dt: h.t - t0, vel: h.vel })) };
    }

    case 'paste': {
      if (!state.clipboard || state.clipboard.length === 0) return state;
      const added = state.clipboard.map((c, i): DrumEvent => ({
        id: `u${state.nextId + i}`,
        art: c.art,
        t: clampT(action.at + c.dt, state.duration),
        vel: clampVel(c.vel),
        src: 'manual',
      }));
      return commit(state, [...state.hits, ...added], { selection: added.map((h) => h.id), nextId: state.nextId + added.length });
    }

    case 'duplicate': {
      const sel = new Set(state.selection);
      const picked = state.hits.filter((h) => sel.has(h.id));
      if (picked.length === 0) return state;
      const added = picked.map((h, i): DrumEvent => ({
        id: `u${state.nextId + i}`,
        art: h.art,
        t: clampT(h.t + action.offset, state.duration),
        vel: h.vel,
        src: 'manual',
      }));
      return commit(state, [...state.hits, ...added], { selection: added.map((h) => h.id), nextId: state.nextId + added.length });
    }

    case 'undo': {
      if (state.inGesture || state.past.length === 0) return state;
      const hits = state.past[state.past.length - 1];
      return {
        ...state,
        hits,
        selection: keepKnown(state.selection, hits),
        past: state.past.slice(0, -1),
        future: [state.hits, ...state.future],
        changes: state.changes + 1,
      };
    }

    case 'redo': {
      if (state.inGesture || state.future.length === 0) return state;
      const hits = state.future[0];
      return {
        ...state,
        hits,
        selection: keepKnown(state.selection, hits),
        past: pushPast(state.past, state.hits),
        future: state.future.slice(1),
        changes: state.changes + 1,
      };
    }

    default:
      return state;
  }
}

/**
 * Record a hits change: sort, bump `changes`, and — outside a gesture — push
 * the previous hits onto `past` and drop `future`. Inside a gesture the entry
 * was pushed at gestureBegin; `future` is dropped on the first real change.
 */
function commit(state: EditorState, hits: DrumEvent[], patch: Partial<EditorState> = {}): EditorState {
  const next: EditorState = { ...state, ...patch, hits: sortHits(hits), changes: state.changes + 1 };
  if (state.inGesture) {
    // First change of the gesture: one history entry for the whole drag.
    if (state.gestureFrom === state.hits) next.past = pushPast(state.past, state.hits);
    if (state.future.length > 0) next.future = [];
    return next;
  }
  next.past = pushPast(state.past, state.hits);
  next.future = [];
  return next;
}

/** Same hits, field by field (a gesture that returned to its start). */
function sameHits(a: DrumEvent[], b: DrumEvent[]): boolean {
  if (a.length !== b.length) return false;
  const byId = indexById(b);
  for (const h of a) {
    const o = byId.get(h.id);
    if (!o || o.t !== h.t || o.art !== h.art || o.vel !== h.vel) return false;
  }
  return true;
}

/** Map every hit through `f`; commit only if some hit came back as a different object. */
function commitIfChanged(state: EditorState, f: (h: DrumEvent) => DrumEvent, patch: Partial<EditorState> = {}): EditorState {
  let changed = false;
  const hits = state.hits.map((h) => {
    const n = f(h);
    if (n !== h) changed = true;
    return n;
  });
  return changed ? commit(state, hits, patch) : state;
}

function pushPast(past: DrumEvent[][], hits: DrumEvent[]): DrumEvent[][] {
  const next = past.length >= HISTORY_LIMIT ? past.slice(past.length - HISTORY_LIMIT + 1) : past.slice();
  next.push(hits);
  return next;
}

/** Stable in-place sort by t (Array.prototype.sort is stable). Returns the same array. */
function sortHits(hits: DrumEvent[]): DrumEvent[] {
  return isSorted(hits) ? hits : hits.sort((a, b) => a.t - b.t);
}

function isSorted(hits: DrumEvent[]): boolean {
  for (let i = 1; i < hits.length; i++) if (hits[i].t < hits[i - 1].t) return false;
  return true;
}

function indexById(hits: DrumEvent[]): Map<string, DrumEvent> {
  const m = new Map<string, DrumEvent>();
  for (const h of hits) m.set(h.id, h);
  return m;
}

/** max existing 'u<n>' + 1, but never below `floor` (ids are never reused within a session). */
function nextManualId(hits: DrumEvent[], floor: number): number {
  let next = floor;
  for (const h of hits) {
    const m = MANUAL_ID.exec(h.id);
    if (m) next = Math.max(next, Number(m[1]) + 1);
  }
  return next;
}

function clampT(t: number, duration: number): number {
  if (!Number.isFinite(t)) return 0;
  if (t < 0) return 0;
  if (duration > 0 && t > duration) return duration;
  return t;
}

/** (0, 1] as the server wants it; the floor is velFrom127(1). */
function clampVel(v: number): number {
  if (!Number.isFinite(v)) return VEL_MIN;
  return Math.max(VEL_MIN, Math.min(1, v));
}

function dedupe(ids: string[]): string[] {
  return Array.from(new Set(ids));
}

function without(ids: string[], drop: Set<string>): string[] {
  return drop.size === 0 ? ids : ids.filter((id) => !drop.has(id));
}

/** Selection minus ids that are not in `hits` (same array when nothing is dropped). */
function keepKnown(selection: string[], hits: DrumEvent[]): string[] {
  if (selection.length === 0) return selection;
  const ids = new Set(hits.map((h) => h.id));
  const kept = selection.filter((id) => ids.has(id));
  return kept.length === selection.length ? selection : kept;
}

function sameIds(a: string[], b: string[]): boolean {
  if (a.length !== b.length) return false;
  for (let i = 0; i < a.length; i++) if (a[i] !== b[i]) return false;
  return true;
}
