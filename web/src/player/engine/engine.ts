import { PlayPlan, validateLoop, type LoopRange } from './chunk-scheduler';
import { assertPartial, rangeHeader } from './range-utils';
import {
  createRingLayout,
  CTRL,
  METER_FLOATS,
  METER_HOLD_SECONDS,
  METER_RELEASE_DB_PER_S,
  STATE,
} from './ring-layout';
import { SharedRings, type MeterReading } from './shared-ring';
import { StemStreamer, type StemStreamStats } from './stem-stream';
import { HEADER_PROBE_BYTES, parseWavHeader, type WavFormat } from './wav-header';

export type EngineState = 'idle' | 'ready' | 'priming' | 'playing' | 'stopped' | 'ended' | 'error';

export interface StemSource {
  name: string;
  url: string;
  fmt: WavFormat;
  fileSize: number;
}

export interface EngineOptions {
  stems: StemSource[];
  /** Ring capacity per channel in frames (power of two). Default 2^18 ≈ 5.5 s @ 48 kHz. */
  ringFrames?: number;
  chunkSeconds?: number;
  firstChunkSeconds?: number;
  prefetchSeconds?: number;
  minFillSeconds?: number;
  workletUrl?: string;
  fetchImpl?: typeof fetch;
}

/** Loop region in seconds, half-open [start, end). */
export interface LoopSeconds {
  start: number;
  end: number;
}

export interface MasterMeter {
  left: MeterReading;
  right: MeterReading;
}

export interface EngineStats {
  state: EngineState;
  contextState: string;
  sampleRate: number;
  generation: number;
  underruns: number;
  quanta: number;
  consumedFrames: number;
  ringFrames: number;
  ringFill: number[];
  streams: StemStreamStats[];
  requests: number;
  bytes: number;
  lastSeekMs: number;
  baseLatency: number;
  outputLatency: number;
  sabBytes: number;
  error: string | null;
  /** Raw (pre-fader) per-stem meters, see ring-layout. */
  meters: MeterReading[];
}

interface WorkletMessage {
  type: 'ready' | 'flushed';
  generation?: number;
}

const DEFAULT_WORKLET_URL = '/stream-processor.js';
const MASTER_FFT = 1024;

/**
 * Probe each stem with a single range request: proves the server honours
 * Range, yields the file size and the WAV format.
 */
export async function probeStems(
  stems: { name: string; url: string }[],
  fetchImpl: typeof fetch = (input, init) => fetch(input, init),
): Promise<StemSource[]> {
  return Promise.all(
    stems.map(async ({ name, url }) => {
      const end = HEADER_PROBE_BYTES - 1;
      const res = await fetchImpl(url, { headers: { Range: rangeHeader(0, end) }, cache: 'no-store' });
      const cr = assertPartial(res.status, res.headers.get('content-range'), 0, end);
      const buf = await res.arrayBuffer();
      const fmt = parseWavHeader(buf, cr.total);
      return { name, url, fmt, fileSize: cr.total };
    }),
  );
}

/**
 * Multi-stem streaming player. UI-agnostic: the UI polls `stats()`,
 * `position`, `meters()` and `masterMeter()` from requestAnimationFrame and
 * subscribes to state changes.
 *
 * Solo is exclusive. A stem is *silenced* when another stem is soloed; that
 * is a distinct state from muted (the UI draws them differently) but both
 * give an effective gain of 0.
 */
export class StreamEngine {
  readonly stems: StemSource[];
  readonly sampleRate: number;
  readonly totalFrames: number;
  private readonly opts: Required<Omit<EngineOptions, 'stems' | 'fetchImpl'>> & { fetchImpl?: typeof fetch };
  private ctx: AudioContext | null = null;
  private node: AudioWorkletNode | null = null;
  private gains: GainNode[] = [];
  private master: GainNode | null = null;
  private analysers: AnalyserNode[] = [];
  private analyserBuf: Float32Array<ArrayBuffer> | null = null;
  private masterFollow: MasterMeter = { left: zeroMeter(), right: zeroMeter() };
  private masterHoldUntil = [0, 0];
  private masterLastAt = 0;
  private rings: SharedRings | null = null;
  private streamers: StemStreamer[] = [];
  private plan: PlayPlan | null = null;
  private region: LoopRange | null = null;
  private loopOn = false;
  private generation = 0;
  private anchorFrame = 0;
  private stateValue: EngineState = 'idle';
  private timer: ReturnType<typeof setInterval> | null = null;
  private pendingFlush: number | null = null;
  private seekStartedAt = 0;
  private lastSeekMs = 0;
  private errorMessage: string | null = null;
  private masterValue = 1;
  private muted: boolean[] = [];
  private soloed: boolean[] = [];
  private gainValues: number[] = [];
  private listeners = new Set<(state: EngineState) => void>();
  private resumeInFlight: Promise<void> | null = null;

  constructor(options: EngineOptions) {
    if (options.stems.length === 0) throw new Error('at least one stem is required');
    this.stems = options.stems;
    const first = options.stems[0].fmt;
    for (const s of options.stems) {
      if (s.fmt.sampleRate !== first.sampleRate) {
        throw new Error(`sample rate mismatch: ${s.name} is ${s.fmt.sampleRate}, expected ${first.sampleRate}`);
      }
    }
    this.sampleRate = first.sampleRate;
    this.totalFrames = Math.min(...options.stems.map((s) => s.fmt.totalFrames));
    this.opts = {
      ringFrames: options.ringFrames ?? 1 << 18,
      chunkSeconds: options.chunkSeconds ?? 1,
      firstChunkSeconds: options.firstChunkSeconds ?? 0.5,
      prefetchSeconds: options.prefetchSeconds ?? 4,
      minFillSeconds: options.minFillSeconds ?? 0.5,
      workletUrl: options.workletUrl ?? DEFAULT_WORKLET_URL,
      fetchImpl: options.fetchImpl,
    };
    this.muted = options.stems.map(() => false);
    this.soloed = options.stems.map(() => false);
    this.gainValues = options.stems.map(() => 1);
  }

  get state(): EngineState {
    return this.stateValue;
  }

  get initialised(): boolean {
    return this.ctx !== null;
  }

  get duration(): number {
    return this.totalFrames / this.sampleRate;
  }

  get isPlaying(): boolean {
    return this.stateValue === 'playing' || this.stateValue === 'priming';
  }

  /** The loop the plan is using right now (frames), or null when off. */
  get loopRange(): LoopRange | null {
    return this.loopOn ? this.region : null;
  }

  /** The stored loop region in seconds, whether or not LOOP is engaged. */
  get loopRegion(): LoopSeconds | null {
    return this.region ? { start: this.region.start / this.sampleRate, end: this.region.end / this.sampleRate } : null;
  }

  get loopEnabled(): boolean {
    return this.loopOn;
  }

  /** Current song position in seconds. */
  get position(): number {
    if (this.isPlaying && this.plan && this.rings) {
      return this.plan.songFrameAt(this.rings.readPos()) / this.sampleRate;
    }
    return this.anchorFrame / this.sampleRate;
  }

  subscribe(listener: (state: EngineState) => void): () => void {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  }

  /** Must be called from a user gesture (AudioContext autoplay policy). */
  async init(): Promise<void> {
    if (this.ctx) return;
    if (typeof SharedArrayBuffer === 'undefined' || !globalThis.crossOriginIsolated) {
      throw new Error('SharedArrayBuffer unavailable: page is not cross-origin isolated (COOP/COEP headers missing)');
    }
    const ctx = new AudioContext({ sampleRate: this.sampleRate, latencyHint: 'playback' });
    if (ctx.sampleRate !== this.sampleRate) {
      await ctx.close();
      throw new Error(`AudioContext refused sample rate ${this.sampleRate} (got ${ctx.sampleRate})`);
    }
    await ctx.audioWorklet.addModule(this.opts.workletUrl);
    const layout = createRingLayout(
      this.stems.map((s) => s.fmt.channels),
      this.opts.ringFrames,
    );
    const sab = new SharedArrayBuffer(layout.totalBytes);
    const rings = new SharedRings(sab, layout);
    rings.resetWriters();
    const node = new AudioWorkletNode(ctx, 'lab-stream-processor', {
      numberOfInputs: 0,
      numberOfOutputs: this.stems.length,
      outputChannelCount: this.stems.map(() => 2),
      processorOptions: {
        sab,
        layout,
        ctrl: CTRL,
        state: STATE,
        meter: { floats: METER_FLOATS, releaseDbPerS: METER_RELEASE_DB_PER_S, holdSeconds: METER_HOLD_SECONDS },
      },
    });
    node.port.onmessage = (event: MessageEvent<WorkletMessage>) => this.onWorkletMessage(event.data);
    ctx.onstatechange = () => {
      if (ctx.state !== 'running' && this.isPlaying) {
        this.resumeContext();
      }
      this.notify();
    };
    const master = ctx.createGain();
    master.gain.value = 0;
    master.connect(ctx.destination);
    // Master L/R metering: split the summed signal and analyse each side.
    const splitter = ctx.createChannelSplitter(2);
    master.connect(splitter);
    this.analysers = [0, 1].map((ch) => {
      const a = ctx.createAnalyser();
      a.fftSize = MASTER_FFT;
      a.smoothingTimeConstant = 0;
      splitter.connect(a, ch);
      return a;
    });
    this.analyserBuf = new Float32Array(MASTER_FFT);
    this.gains = this.stems.map((_, i) => {
      const g = ctx.createGain();
      g.gain.value = this.effectiveGain(i);
      node.connect(g, i, 0);
      g.connect(master);
      return g;
    });
    const chunkFrames = Math.round(this.opts.chunkSeconds * this.sampleRate);
    const firstChunkFrames = Math.round(this.opts.firstChunkSeconds * this.sampleRate);
    const prefetchFrames = Math.round(this.opts.prefetchSeconds * this.sampleRate);
    this.streamers = this.stems.map(
      (s, index) =>
        new StemStreamer({
          url: s.url,
          fmt: s.fmt,
          index,
          rings,
          chunkFrames,
          firstChunkFrames,
          prefetchFrames,
          fetchImpl: this.opts.fetchImpl,
          onChunk: () => this.tick(),
          onError: (err) => this.fail(`${s.name}: ${err.message}`),
        }),
    );
    this.ctx = ctx;
    this.node = node;
    this.master = master;
    this.rings = rings;
    await ctx.resume();
    this.setState('ready');
  }

  play(): void {
    this.ensureInit();
    if (this.isPlaying) return;
    if (this.stateValue === 'ended' || this.anchorFrame >= this.totalFrames) {
      this.anchorFrame = this.loopOn && this.region ? this.region.start : 0;
    }
    this.startFrom(this.anchorFrame);
  }

  stop(): void {
    if (!this.ctx || !this.rings) return;
    if (this.stateValue === 'stopped' || this.stateValue === 'ready') return;
    const frame = this.currentFrame();
    this.haltStream();
    this.anchorFrame = frame;
    this.setState('stopped');
  }

  toggle(): void {
    if (this.isPlaying) this.stop();
    else this.play();
  }

  /** Seek to a song position in seconds. Allowed before init(). */
  seek(seconds: number): void {
    const frame = Math.max(0, Math.min(Math.round(seconds * this.sampleRate), this.totalFrames));
    if (this.isPlaying) {
      this.startFrom(frame);
      return;
    }
    this.anchorFrame = frame;
    if (this.stateValue === 'ended') this.setState('stopped');
    this.notify();
  }

  /** Home: loop A when LOOP is engaged, otherwise 0:00. */
  returnToStart(): void {
    this.seek(this.loopOn && this.region ? this.region.start / this.sampleRate : 0);
  }

  /**
   * Set the loop region in seconds and engage it. Returns false (and keeps
   * the previous region) when the range is unusable.
   */
  setLoop(range: LoopSeconds | null): boolean {
    if (range === null) {
      this.region = null;
      this.loopOn = false;
      this.replan();
      return true;
    }
    if (!this.setLoopRegion(range)) return false;
    this.setLoopEnabled(true);
    return true;
  }

  /** Store a loop region without changing whether LOOP is engaged. */
  setLoopRegion(range: LoopSeconds): boolean {
    const valid = validateLoop(
      { start: Math.round(range.start * this.sampleRate), end: Math.round(range.end * this.sampleRate) },
      this.totalFrames,
    );
    if (!valid) return false;
    this.region = valid;
    if (this.loopOn) this.replan();
    else this.notify();
    return true;
  }

  setLoopEnabled(on: boolean): void {
    const next = on && this.region !== null;
    if (next === this.loopOn) return;
    this.loopOn = next;
    this.replan();
  }

  /** Master bus gain (linear, 0..2). Ramps with the transport. */
  setMasterGain(value: number): void {
    this.masterValue = Math.max(0, Math.min(value, 2));
    if (this.master && this.ctx && this.stateValue === 'playing') {
      this.master.gain.setTargetAtTime(this.masterValue, this.ctx.currentTime, 0.01);
    }
  }

  get masterGain(): number {
    return this.masterValue;
  }

  setGain(index: number, value: number): void {
    this.gainValues[index] = Math.max(0, Math.min(value, 2));
    this.applyGain(index);
  }

  setMute(index: number, muted: boolean): void {
    this.muted[index] = muted;
    this.applyGain(index);
  }

  /**
   * Exclusive solo: engaging one stem clears any other. `on` omitted toggles.
   * Every other stem becomes silenced while a solo is live.
   */
  setSolo(index: number, on?: boolean): void {
    const next = on ?? !this.soloed[index];
    for (let i = 0; i < this.soloed.length; i++) this.soloed[i] = i === index && next;
    this.applyAllGains();
  }

  clearSolo(): void {
    this.soloed.fill(false);
    this.applyAllGains();
  }

  isMuted(index: number): boolean {
    return this.muted[index];
  }

  isSoloed(index: number): boolean {
    return this.soloed[index];
  }

  get anySolo(): boolean {
    return this.soloed.some(Boolean);
  }

  /** Silenced by another stem's solo — not the same as muted. */
  isSilenced(index: number): boolean {
    return this.anySolo && !this.soloed[index];
  }

  gain(index: number): number {
    return this.gainValues[index];
  }

  /** Raw per-stem meters from the worklet (pre-fader). Zeros before init. */
  meters(): MeterReading[] {
    if (this.rings) return this.rings.meters();
    return this.stems.map(() => zeroMeter());
  }

  /**
   * Post-fader master meter (left/right) with the same release and hold as
   * the stem meters, computed from AnalyserNodes on the master bus.
   */
  masterMeter(): MasterMeter {
    if (!this.ctx || !this.analyserBuf) return this.masterFollow;
    const now = performance.now();
    const dt = this.masterLastAt ? (now - this.masterLastAt) / 1000 : 0;
    this.masterLastAt = now;
    const decay = Math.pow(10, (-METER_RELEASE_DB_PER_S / 20) * dt);
    const sides: (keyof MasterMeter)[] = ['left', 'right'];
    sides.forEach((side, ch) => {
      const a = this.analysers[ch];
      const buf = this.analyserBuf!;
      a.getFloatTimeDomainData(buf);
      let peak = 0;
      let sumsq = 0;
      for (let i = 0; i < buf.length; i++) {
        const v = buf[i];
        const abs = v < 0 ? -v : v;
        if (abs > peak) peak = abs;
        sumsq += v * v;
      }
      const rms = Math.sqrt(sumsq / buf.length);
      const m = this.masterFollow[side];
      m.level = Math.max(peak, m.level * decay);
      m.rms = Math.max(rms, m.rms * decay);
      if (peak >= m.hold) {
        m.hold = peak;
        this.masterHoldUntil[ch] = now + METER_HOLD_SECONDS * 1000;
      } else if (now >= this.masterHoldUntil[ch]) {
        m.hold = Math.max(m.level, m.hold * decay);
      }
    });
    return this.masterFollow;
  }

  stats(): EngineStats {
    const rings = this.rings;
    const streams = this.streamers.map((s) => ({ ...s.stats }));
    return {
      state: this.stateValue,
      contextState: this.ctx?.state ?? 'none',
      sampleRate: this.sampleRate,
      generation: this.generation,
      underruns: rings?.underruns() ?? 0,
      quanta: rings?.quanta() ?? 0,
      consumedFrames: rings?.readPos() ?? 0,
      ringFrames: this.opts.ringFrames,
      ringFill: rings ? this.stems.map((_, i) => rings.buffered(i) / this.opts.ringFrames) : [],
      streams,
      requests: streams.reduce((n, s) => n + s.requests, 0),
      bytes: streams.reduce((n, s) => n + s.bytes, 0),
      lastSeekMs: this.lastSeekMs,
      baseLatency: this.ctx?.baseLatency ?? 0,
      outputLatency: this.ctx?.outputLatency ?? 0,
      sabBytes: rings?.layout.totalBytes ?? 0,
      error: this.errorMessage,
      meters: this.meters(),
    };
  }

  async destroy(): Promise<void> {
    this.haltStream();
    this.node?.disconnect();
    this.node = null;
    this.streamers = [];
    if (this.ctx) {
      await this.ctx.close().catch(() => undefined);
      this.ctx = null;
    }
    this.setState('idle');
  }

  // ---- internals -------------------------------------------------------

  private ensureInit(): void {
    if (!this.ctx || !this.rings) throw new Error('engine not initialised; call init() first');
  }

  private effectiveGain(index: number): number {
    return this.muted[index] || this.isSilenced(index) ? 0 : this.gainValues[index];
  }

  private applyGain(index: number): void {
    const g = this.gains[index];
    if (!g || !this.ctx) return;
    g.gain.setTargetAtTime(this.effectiveGain(index), this.ctx.currentTime, 0.01);
  }

  private applyAllGains(): void {
    for (let i = 0; i < this.gains.length; i++) this.applyGain(i);
    this.notify();
  }

  private currentFrame(): number {
    if (this.isPlaying && this.plan && this.rings) {
      return this.plan.songFrameAt(this.rings.readPos());
    }
    return this.anchorFrame;
  }

  private replan(): void {
    if (this.isPlaying) this.startFrom(this.currentFrame());
    else this.notify();
  }

  /** Silence and stop the worklet, abort fetches; leaves anchorFrame untouched. */
  private haltStream(): void {
    this.generation += 1;
    this.pendingFlush = null;
    if (this.master && this.ctx) this.master.gain.setTargetAtTime(0, this.ctx.currentTime, 0.005);
    this.rings?.setState(STATE.STOPPED);
    for (const s of this.streamers) s.abort();
    if (this.timer) {
      clearInterval(this.timer);
      this.timer = null;
    }
  }

  /**
   * Chrome suspends an AudioContext under its autoplay policy or when the
   * output device is interrupted; the worklet's flush acknowledgement only
   * runs while the audio thread is running, so every transport start must
   * make sure the context is resumed or the engine would sit in 'priming'.
   */
  private resumeContext(): void {
    const ctx = this.ctx;
    if (!ctx || ctx.state === 'running' || this.resumeInFlight) return;
    this.resumeInFlight = ctx
      .resume()
      .catch((err: unknown) => {
        if (this.stateValue === 'error') return;
        this.fail(`AudioContext resume failed: ${err instanceof Error ? err.message : String(err)}`);
      })
      .finally(() => {
        this.resumeInFlight = null;
      });
  }

  private startFrom(frame: number): void {
    if (!this.node || !this.rings) return;
    this.seekStartedAt = performance.now();
    this.resumeContext();
    this.haltStream();
    this.anchorFrame = frame;
    this.errorMessage = null;
    this.setState('priming');
    const generation = this.generation;
    this.pendingFlush = generation;
    // Give the master ramp a few ms before the worklet resets its cursor.
    setTimeout(() => {
      if (this.pendingFlush !== generation || !this.node) return;
      this.node.port.postMessage({ type: 'flush', generation });
    }, 20);
  }

  private onWorkletMessage(msg: WorkletMessage): void {
    if (msg.type !== 'flushed') return;
    if (msg.generation !== this.generation || this.pendingFlush !== msg.generation) return;
    if (!this.rings) return;
    this.pendingFlush = null;
    this.rings.resetWriters();
    this.plan = new PlayPlan(this.anchorFrame, this.totalFrames, this.loopRange);
    if (Number.isFinite(this.plan.streamLength)) this.rings.setEof(this.plan.streamLength);
    for (const s of this.streamers) s.restart(this.plan, this.generation);
    this.rings.setState(STATE.PRIMING);
    this.timer = setInterval(() => this.tick(), 50);
    this.tick();
  }

  private tick(): void {
    if (!this.rings || !this.plan) return;
    if (!this.isPlaying) return;
    for (const s of this.streamers) s.tick();
    if (this.stateValue === 'priming') {
      const minFill = Math.round(this.opts.minFillSeconds * this.sampleRate);
      const allEof = this.streamers.every((s) => s.stats.eofReached);
      const needed = Number.isFinite(this.plan.streamLength) ? Math.min(minFill, this.plan.streamLength) : minFill;
      if (this.rings.minBuffered() >= needed || allEof) {
        this.rings.setState(STATE.PLAYING);
        if (this.master && this.ctx) this.master.gain.setTargetAtTime(this.masterValue, this.ctx.currentTime, 0.01);
        this.lastSeekMs = performance.now() - this.seekStartedAt;
        this.setState('playing');
      }
    }
    if (this.stateValue === 'playing' && this.rings.ended()) {
      this.haltStream();
      this.anchorFrame = this.totalFrames;
      this.setState('ended');
    }
  }

  private fail(message: string): void {
    this.errorMessage = message;
    this.haltStream();
    this.setState('error');
  }

  private setState(state: EngineState): void {
    this.stateValue = state;
    this.notify();
  }

  private notify(): void {
    for (const l of this.listeners) l(this.stateValue);
  }
}

function zeroMeter(): MeterReading {
  return { level: 0, rms: 0, hold: 0 };
}
