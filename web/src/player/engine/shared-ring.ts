import { CTRL, METER, METER_FLOATS, STATE, type RingLayout } from './ring-layout';

/** One stem's meter reading, raw (pre-fader), linear 0..1+. */
export interface MeterReading {
  level: number;
  rms: number;
  hold: number;
}

/**
 * Main-thread view of the shared control block, meter block and ring
 * buffers. The AudioWorklet processor (public/stream-processor.js) is the
 * consumer of the rings and the producer of the meters.
 */
export class SharedRings {
  readonly sab: SharedArrayBuffer;
  readonly layout: RingLayout;
  private readonly ctrl: Int32Array;
  private readonly meterBlock: Float32Array;
  private readonly rings: Float32Array[][];

  constructor(sab: SharedArrayBuffer, layout: RingLayout) {
    if (sab.byteLength < layout.totalBytes) throw new Error('SharedArrayBuffer smaller than layout');
    this.sab = sab;
    this.layout = layout;
    this.ctrl = new Int32Array(sab, layout.ctrlByteOffset, 16);
    this.meterBlock = new Float32Array(sab, layout.meterByteOffset, layout.stems.length * METER_FLOATS);
    this.rings = layout.stems.map((stem) =>
      stem.channelByteOffsets.map((off) => new Float32Array(sab, off, layout.ringFrames)),
    );
    // A fresh buffer is all zeros; EOF_POS 0 would mean "empty stream", so
    // mark the stream length unknown until a plan sets it.
    Atomics.store(this.ctrl, CTRL.EOF_POS, -1);
  }

  get stemCount(): number {
    return this.layout.stems.length;
  }

  readPos(): number {
    return Atomics.load(this.ctrl, CTRL.READ_POS);
  }

  writePos(stem: number): number {
    return Atomics.load(this.ctrl, CTRL.WRITE_POS0 + stem);
  }

  /** Frames written but not yet consumed for one stem. */
  buffered(stem: number): number {
    return (this.writePos(stem) - this.readPos()) | 0;
  }

  minBuffered(): number {
    let min = Infinity;
    for (let i = 0; i < this.stemCount; i++) min = Math.min(min, this.buffered(i));
    return min;
  }

  /** Free frames available for writing in one stem's ring. */
  space(stem: number): number {
    return this.layout.ringFrames - this.buffered(stem);
  }

  /**
   * Copy `frames` planar frames into a stem's ring. Refuses (returns false)
   * when the ring lacks room for the whole block; never writes partially.
   */
  write(stem: number, planar: Float32Array[], frames: number): boolean {
    if (frames > this.space(stem)) return false;
    const { ringFrames, mask } = this.layout;
    const w = this.writePos(stem);
    const start = w & mask;
    const first = Math.min(frames, ringFrames - start);
    const channels = this.rings[stem];
    for (let c = 0; c < channels.length; c++) {
      const src = planar[Math.min(c, planar.length - 1)];
      channels[c].set(src.subarray(0, first), start);
      if (first < frames) channels[c].set(src.subarray(first, frames), 0);
    }
    Atomics.store(this.ctrl, CTRL.WRITE_POS0 + stem, (w + frames) | 0);
    return true;
  }

  /** Reset writers after the worklet acknowledged a flush (READ_POS is 0). */
  resetWriters(): void {
    for (let i = 0; i < this.stemCount; i++) Atomics.store(this.ctrl, CTRL.WRITE_POS0 + i, 0);
    Atomics.store(this.ctrl, CTRL.EOF_POS, -1);
    Atomics.store(this.ctrl, CTRL.ENDED, 0);
  }

  setEof(streamFrames: number): void {
    Atomics.store(this.ctrl, CTRL.EOF_POS, streamFrames | 0);
  }

  eofPos(): number {
    return Atomics.load(this.ctrl, CTRL.EOF_POS);
  }

  setState(state: number): void {
    Atomics.store(this.ctrl, CTRL.STATE, state);
  }

  state(): number {
    return Atomics.load(this.ctrl, CTRL.STATE);
  }

  underruns(): number {
    return Atomics.load(this.ctrl, CTRL.UNDERRUNS);
  }

  resetUnderruns(): void {
    Atomics.store(this.ctrl, CTRL.UNDERRUNS, 0);
  }

  ended(): boolean {
    return Atomics.load(this.ctrl, CTRL.ENDED) !== 0;
  }

  quanta(): number {
    return Atomics.load(this.ctrl, CTRL.QUANTA);
  }

  /** Meter reading for one stem as written by the worklet (pre-fader). */
  meter(stem: number): MeterReading {
    const o = stem * METER_FLOATS;
    return {
      level: this.meterBlock[o + METER.LEVEL],
      rms: this.meterBlock[o + METER.RMS],
      hold: this.meterBlock[o + METER.HOLD],
    };
  }

  /** Meter readings for every stem. */
  meters(): MeterReading[] {
    const out: MeterReading[] = [];
    for (let i = 0; i < this.stemCount; i++) out.push(this.meter(i));
    return out;
  }

  /** Test helper: read one channel's ring as a plain array slice. */
  peek(stem: number, channel: number, fromPos: number, frames: number): Float32Array {
    const out = new Float32Array(frames);
    const ring = this.rings[stem][channel];
    for (let i = 0; i < frames; i++) out[i] = ring[(fromPos + i) & this.layout.mask];
    return out;
  }

  static readonly STATE = STATE;
}
