import { describe, expect, it } from 'vitest';
import { Decibel, FADER, dbToGain, formatDb, gainToDb, gainToPosition, meterHeight, nudgeDb, positionToGain, UNITY_POSITION } from '../decibel';

describe('Decibel mapping (−∞ … +6 dB, −9 dB at centre)', () => {
  it('hits the three anchor points', () => {
    expect(FADER.y(0)).toBe(Number.NEGATIVE_INFINITY);
    expect(FADER.y(0.5)).toBeCloseTo(-9, 6);
    expect(FADER.y(1)).toBeCloseTo(6, 6);
  });

  it('is monotonic and invertible on the open interval', () => {
    let prev = Number.NEGATIVE_INFINITY;
    for (let x = 0.01; x <= 0.99; x += 0.01) {
      const db = FADER.y(x);
      expect(db).toBeGreaterThan(prev);
      expect(FADER.x(db)).toBeCloseTo(x, 6);
      prev = db;
    }
  });

  it('clamps positions for out-of-range dB', () => {
    expect(FADER.x(-200)).toBe(0);
    expect(FADER.x(Number.NEGATIVE_INFINITY)).toBe(0);
    expect(FADER.x(40)).toBe(1);
  });

  it('puts unity below the top of the travel', () => {
    // −96 / −9 / +6 puts 0 dB at about 73 % of the travel
    expect(UNITY_POSITION).toBeGreaterThan(0.65);
    expect(UNITY_POSITION).toBeLessThan(0.85);
    expect(FADER.y(UNITY_POSITION)).toBeCloseTo(0, 6);
  });

  it('matches the openDAW default curve shape', () => {
    const d = new Decibel(-72, -12, 0);
    expect(d.y(0.5)).toBeCloseTo(-12, 6);
    expect(d.y(1)).toBeCloseTo(0, 6);
    expect(d.y(0.25)).toBeLessThan(-12);
  });
});

describe('gain helpers', () => {
  it('converts dB and linear gain both ways', () => {
    expect(dbToGain(0)).toBe(1);
    expect(dbToGain(-6)).toBeCloseTo(0.501, 3);
    expect(dbToGain(Number.NEGATIVE_INFINITY)).toBe(0);
    expect(gainToDb(1)).toBe(0);
    expect(gainToDb(0)).toBe(Number.NEGATIVE_INFINITY);
    expect(gainToDb(2)).toBeCloseTo(6.02, 2);
  });

  it('round-trips fader position through gain', () => {
    for (const x of [0.1, 0.3, 0.5, 0.84, 1]) {
      expect(gainToPosition(positionToGain(x))).toBeCloseTo(x, 6);
    }
    expect(positionToGain(0)).toBe(0);
  });

  it('formats readouts like the design (−1.7, +0.0, -∞)', () => {
    expect(formatDb(-1.74)).toBe('-1.7');
    expect(formatDb(0)).toBe('0.0');
    expect(formatDb(0.04)).toBe('0.0');
    expect(formatDb(3.5)).toBe('+3.5');
    expect(formatDb(Number.NEGATIVE_INFINITY)).toBe('-∞');
  });

  it('nudges by whole dB and clamps', () => {
    const x = FADER.x(-6);
    expect(FADER.y(nudgeDb(x, 1))).toBeCloseTo(-5, 6);
    expect(FADER.y(nudgeDb(x, -5))).toBeCloseTo(-11, 6);
    expect(nudgeDb(1, 1)).toBe(1);
    expect(nudgeDb(0, -1)).toBe(0);
    expect(FADER.y(nudgeDb(0, 1))).toBeCloseTo(FADER.min + 1, 6);
  });

  it('meter height follows dBFS from −60 to 0', () => {
    expect(meterHeight(0)).toBe(0);
    expect(meterHeight(1)).toBe(1);
    expect(meterHeight(0.001)).toBeCloseTo(0, 3);
    expect(meterHeight(dbToGain(-30))).toBeCloseTo(0.5, 6);
    expect(meterHeight(2)).toBe(1);
  });
});
