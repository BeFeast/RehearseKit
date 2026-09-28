/**
 * The beat grid of a job as the drum editor sees it: a TypeScript port of
 * internal/pipeline/grid (Go) over its Export JSON, so the ruler, snapping
 * and bar.beat.tick land on exactly the beats the MIDI export uses.
 *
 * Beat positions are uniform in beat units (beat k sits at offset+k); the
 * tempo between two beats is 60/(t[k+1]-t[k]). Before the first beat the
 * first interval is extrapolated, after the last beat the last one. When
 * the map is constant a single median interval is used throughout. `beat`
 * and `seconds` follow grid.go's Beat/Seconds operation for operation so the
 * two sides agree bit for bit on amd64 (the fixtures under
 * internal/pipeline/grid/testdata pin that).
 *
 * Bars: bar 1 starts at beat 0 (the project start). Signature segment i runs
 * from time_signatures[i].beat to time_signatures[i+1].beat with numerator
 * time_signatures[i].numerator; the region before the first signature uses
 * its numerator counted from beat 0, so a lead-in that is not a whole number
 * of bars ends in a short bar rather than throwing.
 */

import type { GridExport, TimeSignature } from './types';

/** Ruler / snap resolution. A beat is a quarter note. */
export type GridDivision = '1/8' | '1/16' | '1/16T' | '1/32';

/** Every division, in menu order. */
export const DIVISIONS: GridDivision[] = ['1/8', '1/16', '1/16T', '1/32'];

/** Grid lines per beat for each division. */
export const SUBDIVISIONS: Record<GridDivision, number> = { '1/8': 2, '1/16': 4, '1/16T': 6, '1/32': 8 };

/** Ticks per quarter note in bar.beat.tick positions. */
export const PPQ = 960;

/** One ruler line. */
export interface GridLine {
  /** Seconds. */
  t: number;
  /** Beat position (offset+k units, as in the Go map). */
  beat: number;
  kind: 'bar' | 'beat' | 'sub';
}

/** A musical position; bar and beat are 1-based, tick is 0..PPQ-1. */
export interface Position {
  bar: number;
  beat: number;
  tick: number;
}

/** The bar containing a beat. */
export interface BarInfo {
  /** 1-based. */
  bar: number;
  /** Beat position of the downbeat. */
  startBeat: number;
  numerator: number;
}

export interface BeatGrid {
  readonly hasGrid: boolean;
  /** Transport tempo (export.bpm), or null without a grid. */
  readonly bpm: number | null;
  /** Second → beat position. Identity without a grid; callers check hasGrid. */
  beat(sec: number): number;
  /** Beat position → second. Identity without a grid. */
  seconds(beat: number): number;
  /** The bar containing a beat; negative beats clamp to bar 1. */
  barAt(beat: number): BarInfo;
  /** bar.beat.tick at a second, tick = round(frac·PPQ) rolling into the next beat at PPQ. Null without a grid. */
  position(sec: number): Position | null;
  /** "12.3.480" with a grid; "0:12.431" (m:ss.mmm, floored to the millisecond) without. */
  formatPosition(sec: number): string;
  /** Ascending lines of the division with t in [t0, t1]. Empty without a grid. */
  lines(t0: number, t1: number, div: GridDivision): GridLine[];
  /** Seconds of the nearest line of the division (nearest in beat space). Identity without a grid. */
  snap(sec: number, div: GridDivision): number;
  /** Seconds one division away from sec in beat space; sec ± 0.01 s without a grid. */
  step(sec: number, div: GridDivision, dir: 1 | -1): number;
  /** Local tempo (60 / the beat interval at that second), or null without a grid. */
  bpmAt(sec: number): number | null;
}

/** A run of bars sharing one numerator. */
interface Segment {
  start: number;
  /** Beat of the next segment, or +Infinity. */
  end: number;
  numerator: number;
  /** 1-based number of the bar starting at `start`. */
  firstBar: number;
}

const NO_GRID_STEP = 0.01;

/**
 * Build the grid over a server export; null (or an export with fewer than
 * two beats, which the Go side never produces) yields the grid-less
 * fallback whose seconds↔beat mapping is the identity.
 */
export function makeGrid(g: GridExport | null): BeatGrid {
  if (!g || !Array.isArray(g.beats) || g.beats.length < 2 || !(g.median > 0)) return noGrid();
  return new ExportGrid(g);
}

// --- ports of grid.go -------------------------------------------------------

/** grid.Map.interval: t[k+1]-t[k] with k clamped to [0, n-2]. */
function interval(b: number[], k: number): number {
  if (k < 0) k = 0;
  if (k >= b.length - 1) k = b.length - 2;
  return b[k + 1] - b[k];
}

/** sort.SearchFloat64s: the first index k with b[k] >= x (b.length when none). */
function searchFloat64s(b: number[], x: number): number {
  let lo = 0;
  let hi = b.length;
  while (lo < hi) {
    const mid = (lo + hi) >>> 1;
    if (b[mid] < x) lo = mid + 1;
    else hi = mid;
  }
  return lo;
}

/**
 * The interval index Beat uses for a second on a variable map: 0 before the
 * first beat, n-2 from the last beat on, else the beat at or before sec.
 */
function intervalIndexAt(b: number[], sec: number): number {
  const n = b.length;
  if (sec < b[0]) return 0;
  if (sec >= b[n - 1]) return n - 2;
  let k = searchFloat64s(b, sec);
  if (k === n || b[k] > sec) k--;
  return k;
}

class ExportGrid implements BeatGrid {
  readonly hasGrid = true;
  readonly bpm: number;
  private readonly beats: number[];
  private readonly offset: number;
  private readonly median: number;
  private readonly constant: boolean;
  private readonly segments: Segment[];

  constructor(g: GridExport) {
    this.beats = g.beats;
    this.offset = g.offset;
    this.median = g.median;
    this.constant = g.constant;
    this.bpm = g.bpm;
    this.segments = buildSegments(g.time_signatures, g.numerator);
  }

  /** grid.Map.Beat. */
  beat(sec: number): number {
    if (this.constant) {
      return this.offset + (sec - this.beats[0]) / this.median;
    }
    const b = this.beats;
    const n = b.length;
    if (sec < b[0]) {
      return this.offset - (b[0] - sec) / interval(b, 0);
    }
    if (sec >= b[n - 1]) {
      return this.offset + (n - 1) + (sec - b[n - 1]) / interval(b, n - 2);
    }
    let k = searchFloat64s(b, sec);
    if (k === n || b[k] > sec) k--;
    return this.offset + k + (sec - b[k]) / interval(b, k);
  }

  /** grid.Map.Seconds. */
  seconds(beat: number): number {
    if (this.constant) {
      return this.beats[0] + (beat - this.offset) * this.median;
    }
    const b = this.beats;
    const n = b.length;
    const k = beat - this.offset;
    if (k < 0) {
      return b[0] + k * interval(b, 0);
    }
    if (k >= n - 1) {
      return b[n - 1] + (k - (n - 1)) * interval(b, n - 2);
    }
    const i = Math.floor(k);
    return b[i] + (k - i) * interval(b, i);
  }

  barAt(beat: number): BarInfo {
    const seg = this.segmentAt(beat);
    if (beat < seg.start) return { bar: seg.firstBar, startBeat: seg.start, numerator: seg.numerator };
    const k = Math.floor((beat - seg.start) / seg.numerator);
    return { bar: seg.firstBar + k, startBeat: seg.start + k * seg.numerator, numerator: seg.numerator };
  }

  position(sec: number): Position {
    const origin = this.segments[0].start;
    const b = Math.max(origin, this.beat(sec));
    let whole = Math.floor(b);
    let tick = Math.round((b - whole) * PPQ);
    if (tick >= PPQ) {
      whole += 1;
      tick = 0;
    }
    const info = this.barAt(whole);
    return { bar: info.bar, beat: Math.floor(whole - info.startBeat) + 1, tick };
  }

  formatPosition(sec: number): string {
    const p = this.position(sec);
    return `${p.bar}.${p.beat}.${String(p.tick).padStart(3, '0')}`;
  }

  lines(t0: number, t1: number, div: GridDivision): GridLine[] {
    const out: GridLine[] = [];
    if (!(t1 >= t0) || !Number.isFinite(t0) || !Number.isFinite(t1)) return out;
    const n = SUBDIVISIONS[div];
    // Admit lines within 1e-9 beat of the edges so a line at exactly t0/t1
    // survives the seconds→beat→seconds round trip. `|| 0` folds the -0 that
    // Math.ceil yields just below zero into +0.
    const k0 = Math.ceil(this.beat(t0) * n - 1e-9) || 0;
    const k1 = Math.floor(this.beat(t1) * n + 1e-9) || 0;
    for (let k = k0; k <= k1; k++) {
      const beat = k / n;
      let kind: GridLine['kind'] = 'sub';
      if (k % n === 0) kind = this.barAt(beat).startBeat === beat ? 'bar' : 'beat';
      // The edge tolerance admits a k whose seconds land a hair outside the
      // window; keep the contract (t within [t0, t1]) by clamping those.
      const t = Math.min(t1, Math.max(t0, this.seconds(beat)));
      out.push({ t, beat, kind });
    }
    return out;
  }

  snap(sec: number, div: GridDivision): number {
    const n = SUBDIVISIONS[div];
    return this.seconds(Math.round(this.beat(sec) * n) / n);
  }

  step(sec: number, div: GridDivision, dir: 1 | -1): number {
    return this.seconds(this.beat(sec) + dir / SUBDIVISIONS[div]);
  }

  bpmAt(sec: number): number {
    if (this.constant) return 60 / this.median;
    return 60 / interval(this.beats, intervalIndexAt(this.beats, sec));
  }

  /** The segment whose [start, end) contains beat; the first for anything before it. */
  private segmentAt(beat: number): Segment {
    const s = this.segments;
    let lo = 0;
    let hi = s.length - 1;
    while (lo < hi) {
      const mid = (lo + hi + 1) >>> 1;
      if (s[mid].start <= beat) lo = mid;
      else hi = mid - 1;
    }
    return s[lo];
  }
}

/**
 * Signature changes → bar segments. Bar 1 starts at beat 0 (or at the first
 * signature when that is not after 0); an empty list falls back to a single
 * signature of `numerator` at beat 0, as grid.Map.TimeSignatures does.
 */
function buildSegments(sigs: TimeSignature[] | undefined, numerator: number): Segment[] {
  const list = (Array.isArray(sigs) ? sigs : [])
    .filter((s) => Number.isFinite(s.beat) && Number.isFinite(s.numerator))
    .map((s) => ({ beat: s.beat, numerator: Math.max(1, Math.floor(s.numerator)) }))
    .sort((a, b) => a.beat - b.beat);
  if (list.length === 0) list.push({ beat: 0, numerator: Math.max(1, Math.floor(numerator) || 4) });
  if (list[0].beat > 0) list.unshift({ beat: 0, numerator: list[0].numerator });
  const out: Segment[] = [];
  let firstBar = 1;
  for (let i = 0; i < list.length; i++) {
    const start = list[i].beat;
    const next = i + 1 < list.length ? list[i + 1].beat : Infinity;
    if (next <= start) continue; // duplicate beat: the later entry wins
    out.push({ start, end: next, numerator: list[i].numerator, firstBar });
    firstBar += Math.ceil((next - start) / list[i].numerator);
  }
  return out;
}

// --- without a grid ---------------------------------------------------------

function noGrid(): BeatGrid {
  const segment: Segment = { start: 0, end: Infinity, numerator: 4, firstBar: 1 };
  return {
    hasGrid: false,
    bpm: null,
    beat: (sec) => sec,
    seconds: (beat) => beat,
    barAt(beat) {
      if (beat <= 0) return { bar: 1, startBeat: 0, numerator: segment.numerator };
      const k = Math.floor(beat / segment.numerator);
      return { bar: 1 + k, startBeat: k * segment.numerator, numerator: segment.numerator };
    },
    position: () => null,
    formatPosition: formatClock,
    lines: () => [],
    snap: (sec) => sec,
    step: (sec, _div, dir) => sec + dir * NO_GRID_STEP,
    bpmAt: () => null,
  };
}

/** m:ss.mmm, floored to the millisecond; negative input reads as 0. */
function formatClock(sec: number): string {
  // 1.005 is stored as 1.00499999…; the epsilon keeps such inputs from reading one ms low.
  const ms = Math.max(0, Math.floor(sec * 1000 + 1e-6));
  const m = Math.floor(ms / 60000);
  const s = Math.floor((ms % 60000) / 1000);
  const rem = ms % 1000;
  return `${m}:${String(s).padStart(2, '0')}.${String(rem).padStart(3, '0')}`;
}
