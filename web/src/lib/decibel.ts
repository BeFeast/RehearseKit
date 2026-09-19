/**
 * Fader law. Position x in [0, 1] (0 = bottom of travel) maps to dB through
 * the same three-point curve openDAW uses for its volume faders: a hyperbola
 * fixed by the dB value at the bottom, the centre and the top of the travel.
 * The RehearseKit fader runs −∞ … +6 dB with −9 dB at the centre.
 */
export class Decibel {
  readonly min: number;
  readonly max: number;
  private readonly a: number;
  private readonly b: number;
  private readonly c: number;

  /**
   * @param min dB at x = 0 (the fader floor; x = 0 itself reads −∞)
   * @param mid dB at x = 0.5
   * @param max dB at x = 1
   */
  constructor(min: number, mid: number, max: number) {
    this.min = min;
    this.max = max;
    const min2 = min * min;
    const max2 = max * max;
    const mid2 = mid * mid;
    const tmp0 = min + max - 2.0 * mid;
    const tmp1 = max - mid;
    this.a = ((2.0 * max - mid) * min - mid * max) / tmp0;
    this.b =
      (tmp1 * min2 + (mid2 - max2) * min + mid * max2 - mid2 * max) /
      (min2 + (2.0 * max - 4.0 * mid) * min + max2 - 4.0 * mid * max + 4.0 * mid2);
    this.c = -tmp1 / tmp0;
  }

  /** dB for a fader position. */
  y(x: number): number {
    if (x <= 0.0) return Number.NEGATIVE_INFINITY;
    if (x >= 1.0) return this.max;
    return this.a - this.b / (x + this.c);
  }

  /** Fader position for a dB value. */
  x(y: number): number {
    if (this.min >= y || !Number.isFinite(y)) return 0.0;
    if (this.max <= y) return 1.0;
    return -this.b / (y - this.a) - this.c;
  }
}

/** −∞ … +6 dB with −9 dB at the centre of the travel. */
export const FADER = new Decibel(-96, -9, 6);

/** Scale ticks drawn beside the fader, in dB. */
export const FADER_TICKS = [0, -3, -6, -12, -24] as const;

export function dbToGain(db: number): number {
  if (!Number.isFinite(db)) return 0;
  return Math.pow(10, db / 20);
}

export function gainToDb(gain: number): number {
  if (gain <= 0) return Number.NEGATIVE_INFINITY;
  return 20 * Math.log10(gain);
}

/** Fader position → linear gain. */
export function positionToGain(x: number): number {
  return dbToGain(FADER.y(x));
}

/** Linear gain → fader position. */
export function gainToPosition(gain: number): number {
  return FADER.x(gainToDb(gain));
}

/** "−1.7", "+0.0", "-∞" — the readout under the fader (no unit). */
export function formatDb(db: number, digits = 1): string {
  if (!Number.isFinite(db)) return '-∞';
  const rounded = Number(db.toFixed(digits));
  const sign = rounded > 0 ? '+' : rounded < 0 ? '-' : '';
  const abs = Math.abs(rounded).toFixed(digits);
  return `${sign}${abs}`;
}

/** Nudge a position by whole dB steps (keyboard). Returns the new position. */
export function nudgeDb(x: number, deltaDb: number): number {
  const db = FADER.y(x);
  if (!Number.isFinite(db)) {
    return deltaDb > 0 ? FADER.x(FADER.min + deltaDb) : 0;
  }
  const next = Math.max(FADER.min, Math.min(FADER.max, db + deltaDb));
  return next <= FADER.min ? 0 : FADER.x(next);
}

/** Unity gain position (0 dB). */
export const UNITY_POSITION = FADER.x(0);

/**
 * Meter law: linear level → 0..1 height. 0 dBFS at the top, −60 dBFS at
 * the bottom, so the LEDs light in proportion to the dB scale rather than
 * the raw amplitude.
 */
export function meterHeight(level: number, floorDb = -60): number {
  if (level <= 0) return 0;
  const db = gainToDb(level);
  return Math.max(0, Math.min(1, (db - floorDb) / -floorDb));
}
