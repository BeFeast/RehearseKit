import { readdirSync, readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, expect, it } from 'vitest';
import { DIVISIONS, PPQ, SUBDIVISIONS, makeGrid, type GridLine } from '../grid';
import type { GridExport } from '../types';

/** testdata/export-<name>.json as internal/pipeline/grid/export_test.go writes it. */
interface Fixture {
  name: string;
  duration: number;
  export: GridExport;
  samples: { seconds: number; beat: number }[];
}

const TESTDATA = resolve(__dirname, '../../../../../internal/pipeline/grid/testdata');

function loadFixtures(): Fixture[] {
  return readdirSync(TESTDATA)
    .filter((f) => /^export-.*\.json$/.test(f))
    .sort()
    .map((f) => JSON.parse(readFileSync(resolve(TESTDATA, f), 'utf8')) as Fixture);
}

const fixtures = loadFixtures();

/** 8 bars of 4/4 at 120 BPM whose first beat (beat 4, bar 2) is at 0.25 s; beat 0 is a lead-in bar before the audio. */
const CONST_120: GridExport = {
  constant: true,
  offset: 4,
  median: 0.5,
  beats: Array.from({ length: 32 }, (_, k) => 0.25 + 0.5 * k),
  bpm: 120,
  numerator: 4,
  time_signatures: [{ beat: 4, numerator: 4, denominator: 4 }],
};

/** Two bars of 4/4 then 3/4, beat 0 at 0 s, 120 BPM. */
const CHANGE_4_3: GridExport = {
  constant: true,
  offset: 0,
  median: 0.5,
  beats: Array.from({ length: 32 }, (_, k) => 0.5 * k),
  bpm: 120,
  numerator: 4,
  time_signatures: [
    { beat: 0, numerator: 4, denominator: 4 },
    { beat: 8, numerator: 3, denominator: 4 },
  ],
};

describe('parity with the Go grid (testdata/export-*.json)', () => {
  it('finds the fixtures', () => {
    expect(fixtures.length).toBeGreaterThan(0);
    expect(fixtures.map((f) => f.name)).toContain('clip45');
  });

  for (const fx of fixtures) {
    describe(fx.name, () => {
      const grid = makeGrid(fx.export);

      it('has a grid and the transport tempo of the export', () => {
        expect(grid.hasGrid).toBe(true);
        expect(grid.bpm).toBe(fx.export.bpm);
        expect(grid.beat(0)).toBeGreaterThanOrEqual(0);
      });

      it(`beat() matches Go on ${fx.samples.length} samples`, () => {
        for (const s of fx.samples) {
          expect(Math.abs(grid.beat(s.seconds) - s.beat)).toBeLessThanOrEqual(1e-9);
        }
      });

      it('seconds() inverts every sample', () => {
        for (const s of fx.samples) {
          expect(Math.abs(grid.seconds(s.beat) - s.seconds)).toBeLessThanOrEqual(1e-9);
        }
      });

      it('lands exactly on the cleaned beats', () => {
        const b = fx.export.beats;
        for (const k of [0, 1, Math.floor(b.length / 2), b.length - 2, b.length - 1]) {
          expect(grid.beat(b[k])).toBe(fx.export.offset + k);
          expect(grid.seconds(fx.export.offset + k)).toBe(b[k]);
        }
      });

      it('bpmAt is 60 over the local interval', () => {
        const b = fx.export.beats;
        const k = Math.floor(b.length / 2);
        const mid = (b[k] + b[k + 1]) / 2;
        expect(grid.bpmAt(mid)).toBeCloseTo(60 / (b[k + 1] - b[k]), 9);
        expect(grid.bpmAt(-1)).toBeCloseTo(60 / (b[1] - b[0]), 9);
        expect(grid.bpmAt(fx.duration + 10)).toBeCloseTo(60 / (b[b.length - 1] - b[b.length - 2]), 9);
      });

      it('ruler lines are ascending and snap is idempotent', () => {
        for (const div of DIVISIONS) {
          const lines = grid.lines(0, fx.duration, div);
          expect(lines.length).toBeGreaterThan(0);
          for (let i = 1; i < lines.length; i++) {
            expect(lines[i].t).toBeGreaterThan(lines[i - 1].t);
            expect(lines[i].beat - lines[i - 1].beat).toBeCloseTo(1 / SUBDIVISIONS[div], 9);
          }
          expect(lines[0].t).toBeGreaterThanOrEqual(-1e-9);
          expect(lines[lines.length - 1].t).toBeLessThanOrEqual(fx.duration + 1e-9);
          const snapped = grid.snap(fx.duration / 3, div);
          expect(grid.snap(snapped, div)).toBeCloseTo(snapped, 9);
        }
      });
    });
  }
});

describe('clip45 bars follow its time signatures', () => {
  const fx = fixtures.find((f) => f.name === 'clip45');
  it.skipIf(!fx)('lead-in bar, six bars of 4/4, a 1/4 bar, then 4/4', () => {
    // time_signatures: [{beat 4, 4/4}, {beat 28, 1/4}, {beat 29, 4/4}]
    const grid = makeGrid(fx!.export);
    expect(grid.barAt(0)).toEqual({ bar: 1, startBeat: 0, numerator: 4 });
    expect(grid.barAt(4)).toEqual({ bar: 2, startBeat: 4, numerator: 4 });
    expect(grid.barAt(27.5)).toEqual({ bar: 7, startBeat: 24, numerator: 4 });
    expect(grid.barAt(28)).toEqual({ bar: 8, startBeat: 28, numerator: 1 });
    expect(grid.barAt(29)).toEqual({ bar: 9, startBeat: 29, numerator: 4 });
    expect(grid.barAt(33)).toEqual({ bar: 10, startBeat: 33, numerator: 4 });
    // Second 0 is at beat 3.9565…: last beat of the lead-in bar.
    const p0 = grid.position(0)!;
    expect(p0.bar).toBe(1);
    expect(p0.beat).toBe(4);
    expect(grid.formatPosition(grid.seconds(29))).toBe('9.1.000');
  });
});

describe('constant grid: 8 bars 4/4 at 120 from 0.25 s', () => {
  const grid = makeGrid(CONST_120);

  it('maps the first beat to beat 4 and back', () => {
    expect(grid.hasGrid).toBe(true);
    expect(grid.bpm).toBe(120);
    expect(grid.beat(0.25)).toBe(4);
    expect(grid.seconds(4)).toBe(0.25);
    expect(grid.beat(0)).toBe(3.5);
    expect(grid.bpmAt(0)).toBe(120);
    expect(grid.bpmAt(10)).toBe(120);
  });

  it('positions: bar 2 starts at 0.25 s, ticks are 960 per beat', () => {
    expect(grid.position(0.25)).toEqual({ bar: 2, beat: 1, tick: 0 });
    expect(grid.formatPosition(0.25)).toBe('2.1.000');
    expect(grid.position(0.25 + 0.125)).toEqual({ bar: 2, beat: 1, tick: 240 });
    expect(grid.position(0)).toEqual({ bar: 1, beat: 4, tick: 480 });
    expect(grid.formatPosition(0)).toBe('1.4.480');
    expect(grid.position(2.25)).toEqual({ bar: 3, beat: 1, tick: 0 });
    // One tick is 1/PPQ beat = 0.5/PPQ s at 120 BPM.
    expect(grid.formatPosition(0.25 + 3 * 0.5 + 0.5 / PPQ)).toBe('2.4.001');
  });

  it('a tick that rounds to 960 rolls into the next beat (and bar)', () => {
    expect(grid.position(grid.seconds(4.99999999))).toEqual({ bar: 2, beat: 2, tick: 0 });
    expect(grid.position(grid.seconds(7.99999999))).toEqual({ bar: 3, beat: 1, tick: 0 });
  });

  it('negative seconds clamp to bar 1', () => {
    expect(grid.position(-100)).toEqual({ bar: 1, beat: 1, tick: 0 });
    expect(grid.barAt(-3)).toEqual({ bar: 1, startBeat: 0, numerator: 4 });
  });

  it('barAt numbers bars from beat 0', () => {
    expect(grid.barAt(0)).toEqual({ bar: 1, startBeat: 0, numerator: 4 });
    expect(grid.barAt(3.99)).toEqual({ bar: 1, startBeat: 0, numerator: 4 });
    expect(grid.barAt(4)).toEqual({ bar: 2, startBeat: 4, numerator: 4 });
    expect(grid.barAt(7.9)).toEqual({ bar: 2, startBeat: 4, numerator: 4 });
    expect(grid.barAt(8)).toEqual({ bar: 3, startBeat: 8, numerator: 4 });
    expect(grid.barAt(100)).toEqual({ bar: 26, startBeat: 100, numerator: 4 });
  });

  it('lines(0.25, 2.25, 1/16): bars at both ends, beats between, three subs per beat', () => {
    const lines = grid.lines(0.25, 2.25, '1/16');
    expect(lines).toHaveLength(17);
    for (let i = 1; i < lines.length; i++) {
      expect(lines[i].t - lines[i - 1].t).toBeCloseTo(0.125, 12);
    }
    const byKind = (kind: GridLine['kind']) => lines.filter((l) => l.kind === kind).map((l) => l.t);
    expect(byKind('bar')).toEqual([0.25, 2.25]);
    expect(byKind('beat')).toEqual([0.75, 1.25, 1.75]);
    expect(byKind('sub')).toHaveLength(12);
    expect(byKind('sub')).toContain(0.375);
    expect(byKind('sub')).not.toContain(0.75);
    expect(lines[0].beat).toBe(4);
    expect(lines[16].beat).toBe(8);
  });

  it('lines respect the division and the window', () => {
    expect(grid.lines(0.25, 2.25, '1/8')).toHaveLength(9);
    expect(grid.lines(0.25, 2.25, '1/16T')).toHaveLength(25);
    expect(grid.lines(0.25, 2.25, '1/32')).toHaveLength(33);
    expect(grid.lines(0.26, 0.74, '1/8').map((l) => l.t)).toEqual([0.5]);
    expect(grid.lines(1, 0.5, '1/8')).toEqual([]);
    // Before the audio the lead-in bar is extrapolated at the same tempo.
    const lead = grid.lines(-1.75, 0.25, '1/8');
    expect(lead[0]).toEqual({ t: -1.75, beat: 0, kind: 'bar' });
    expect(lead[lead.length - 1]).toEqual({ t: 0.25, beat: 4, kind: 'bar' });
  });

  it('snap picks the nearest line in beat space', () => {
    expect(grid.snap(0.3, '1/16')).toBe(0.25);
    expect(grid.snap(0.33, '1/16')).toBe(0.375);
    expect(grid.snap(0.33, '1/8')).toBe(0.25);
    expect(grid.snap(0.49, '1/8')).toBe(0.5);
    expect(grid.snap(0.375, '1/16')).toBe(0.375);
  });

  it('step moves one division in beat space', () => {
    expect(grid.step(0.25, '1/8', 1)).toBe(0.5);
    expect(grid.step(0.5, '1/8', -1)).toBe(0.25);
    expect(grid.step(0.25, '1/16T', 1)).toBeCloseTo(0.25 + 0.5 / 6, 12);
    expect(grid.step(0.25, '1/32', 1)).toBeCloseTo(0.25 + 0.5 / 8, 12);
    expect(grid.step(0.3, '1/16', 1)).toBeCloseTo(0.425, 12);
  });
});

describe('time-signature change: 4/4 then 3/4', () => {
  const grid = makeGrid(CHANGE_4_3);

  it('numbers bars across the change', () => {
    expect(grid.barAt(0)).toEqual({ bar: 1, startBeat: 0, numerator: 4 });
    expect(grid.barAt(7)).toEqual({ bar: 2, startBeat: 4, numerator: 4 });
    expect(grid.barAt(8)).toEqual({ bar: 3, startBeat: 8, numerator: 3 });
    expect(grid.barAt(10.99)).toEqual({ bar: 3, startBeat: 8, numerator: 3 });
    expect(grid.barAt(11)).toEqual({ bar: 4, startBeat: 11, numerator: 3 });
    expect(grid.barAt(14)).toEqual({ bar: 5, startBeat: 14, numerator: 3 });
    expect(grid.barAt(-1)).toEqual({ bar: 1, startBeat: 0, numerator: 4 });
  });

  it('positions and ruler follow the new bar length', () => {
    expect(grid.position(grid.seconds(12.5))).toEqual({ bar: 4, beat: 2, tick: 480 });
    expect(grid.formatPosition(grid.seconds(12.5))).toBe('4.2.480');
    expect(grid.formatPosition(grid.seconds(7.5))).toBe('2.4.480');
    const bars = grid.lines(grid.seconds(4), grid.seconds(14), '1/8').filter((l) => l.kind === 'bar');
    expect(bars.map((l) => l.beat)).toEqual([4, 8, 11, 14]);
    const beats = grid.lines(grid.seconds(8), grid.seconds(11), '1/8').filter((l) => l.kind === 'beat');
    expect(beats.map((l) => l.beat)).toEqual([9, 10]);
  });

  it('a lead-in that is not a whole number of bars ends in a short bar', () => {
    const g = makeGrid({ ...CONST_120, offset: 5, time_signatures: [{ beat: 5, numerator: 4, denominator: 4 }] });
    expect(g.barAt(3.5)).toEqual({ bar: 1, startBeat: 0, numerator: 4 });
    expect(g.barAt(4.5)).toEqual({ bar: 2, startBeat: 4, numerator: 4 });
    expect(g.barAt(5)).toEqual({ bar: 3, startBeat: 5, numerator: 4 });
    expect(g.barAt(9)).toEqual({ bar: 4, startBeat: 9, numerator: 4 });
    expect(g.position(g.seconds(4.5))).toEqual({ bar: 2, beat: 1, tick: 480 });
  });

  it('tolerates empty, unsorted and degenerate signature lists', () => {
    const none = makeGrid({ ...CHANGE_4_3, numerator: 3, time_signatures: [] });
    expect(none.barAt(3)).toEqual({ bar: 2, startBeat: 3, numerator: 3 });
    const reversed = makeGrid({ ...CHANGE_4_3, time_signatures: [...CHANGE_4_3.time_signatures].reverse() });
    expect(reversed.barAt(11)).toEqual(grid.barAt(11));
    const zero = makeGrid({ ...CHANGE_4_3, time_signatures: [{ beat: 0, numerator: 0, denominator: 4 }] });
    expect(zero.barAt(2)).toEqual({ bar: 3, startBeat: 2, numerator: 1 });
  });
});

describe('without a grid', () => {
  const grid = makeGrid(null);

  it('reports no grid and falls back to identities', () => {
    expect(grid.hasGrid).toBe(false);
    expect(grid.bpm).toBeNull();
    expect(grid.beat(2.5)).toBe(2.5);
    expect(grid.seconds(2.5)).toBe(2.5);
    expect(grid.position(3)).toBeNull();
    expect(grid.bpmAt(3)).toBeNull();
    expect(grid.lines(0, 10, '1/16')).toEqual([]);
    expect(grid.snap(1.234, '1/8')).toBe(1.234);
    expect(grid.step(1, '1/16', 1)).toBeCloseTo(1.01, 12);
    expect(grid.step(1, '1/16', -1)).toBeCloseTo(0.99, 12);
  });

  it('formats a clock, floored to the millisecond', () => {
    expect(grid.formatPosition(12.4315)).toBe('0:12.431');
    expect(grid.formatPosition(0)).toBe('0:00.000');
    expect(grid.formatPosition(75.5)).toBe('1:15.500');
    expect(grid.formatPosition(3600.001)).toBe('60:00.001');
    expect(grid.formatPosition(-2)).toBe('0:00.000');
  });

  it('barAt still counts 4/4 bars from 0 without throwing', () => {
    expect(grid.barAt(0)).toEqual({ bar: 1, startBeat: 0, numerator: 4 });
    expect(grid.barAt(9)).toEqual({ bar: 3, startBeat: 8, numerator: 4 });
    expect(grid.barAt(-1)).toEqual({ bar: 1, startBeat: 0, numerator: 4 });
  });

  it('an export with fewer than two beats is treated as no grid', () => {
    expect(makeGrid({ ...CONST_120, beats: [0.25] }).hasGrid).toBe(false);
    expect(makeGrid({ ...CONST_120, median: 0 }).hasGrid).toBe(false);
  });
});
