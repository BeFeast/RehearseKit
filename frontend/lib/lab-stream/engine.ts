import { PlayPlan, validateLoop, type LoopRange } from './chunk-scheduler';
import { assertPartial, rangeHeader } from './range-utils';
import { createRingLayout, CTRL, STATE } from './ring-layout';
import { SharedRings } from './shared-ring';
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
  /** Ring capacity per channel in frames (power of two). Default 2^18 ≈ 5.9 s @ 44.1 kHz. */
  ringFrames?: number;
  chunkSeconds?: number;
  firstChunkSeconds?: number;
  prefetchSeconds?: number;
  minFillSeconds?: number;
  workletUrl?: string;
  fetchImpl?: typeof fetch;
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
}

interface WorkletMessage {
  type: 'ready' | 'flushed';
  generation?: number;
}

const DEFAULT_WORKLET_URL = '/lab/stream-processor.js';

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

export class StreamEngine {
  readonly stems: StemSource[];
  readonly sampleRate: number;
  readonly totalFrames: number;
  private readonly opts: Required<Omit<EngineOptions, 'stems' | 'fetchImpl'>> & { fetchImpl?: typeof fetch };
  private ctx: AudioContext | null = null;
  private node: AudioWorkletNode | null = null;
  private gains: GainNode[] = [];
  private master: GainNode | null = null;
  private rings: SharedRings | null = null;
  private streamers: StemStreamer[] = [];
  private plan: PlayPlan | null = null;
  private loop: LoopRange | null = null;
  private generation = 0;
  private anchorFrame = 0;
  private stateValue: EngineState = 'idle';
  private timer: ReturnType<typeof setInterval> | null = null;
  private pendingFlush: number | null = null;
  private seekStartedAt = 0;
  private lastSeekMs = 0;
  private errorMessage: string | null = null;
  private muted: boolean[] = [];
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
    this.gainValues = options.stems.map(() => 1);
  }

  get state(): EngineState {
    return this.stateValue;
  }

  get duration(): number {
    return this.totalFrames / this.sampleRate;
  }

  get loopRange(): LoopRange | null {
    return this.loop;
  }

  /** Current song position in seconds. */
  get position(): number {
    if ((this.stateValue === 'playing' || this.stateValue === 'priming') && this.plan && this.rings) {
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
      processorOptions: { sab, layout, ctrl: CTRL, state: STATE },
    });
    node.port.onmessage = (event: MessageEvent<WorkletMessage>) => this.onWorkletMessage(event.data);
    ctx.onstatechange = () => {
      if (ctx.state !== 'running' && (this.stateValue === 'priming' || this.stateValue === 'playing')) {
        this.resumeContext();
      }
      this.notify();
    };
    const master = ctx.createGain();
    master.gain.value = 0;
    master.connect(ctx.destination);
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
    if (this.stateValue === 'playing' || this.stateValue === 'priming') return;
    if (this.stateValue === 'ended' || this.anchorFrame >= this.totalFrames) this.anchorFrame = 0;
    this.startFrom(this.anchorFrame);
  }

  stop(): void {
    this.ensureInit();
    if (this.stateValue === 'stopped' || this.stateValue === 'ready') return;
    const frame = this.currentFrame();
    this.haltStream();
    this.anchorFrame = frame;
    this.setState('stopped');
  }

  seek(seconds: number): void {
    this.ensureInit();
    const frame = Math.max(0, Math.min(Math.round(seconds * this.sampleRate), this.totalFrames));
    if (this.stateValue === 'playing' || this.stateValue === 'priming') {
      this.startFrom(frame);
      return;
    }
    this.anchorFrame = frame;
    if (this.stateValue === 'ended') this.setState('stopped');
    this.notify();
  }

  /** Returns false (and keeps the previous range) when the range is unusable. */
  setLoop(range: { start: number; end: number } | null): boolean {
    this.ensureInit();
    if (range === null) {
      this.loop = null;
    } else {
      const valid = validateLoop(
        { start: Math.round(range.start * this.sampleRate), end: Math.round(range.end * this.sampleRate) },
        this.totalFrames,
      );
      if (!valid) return false;
      this.loop = valid;
    }
    if (this.stateValue === 'playing' || this.stateValue === 'priming') this.startFrom(this.currentFrame());
    else this.notify();
    return true;
  }

  setGain(index: number, value: number): void {
    this.gainValues[index] = Math.max(0, Math.min(value, 2));
    this.applyGain(index);
  }

  setMute(index: number, muted: boolean): void {
    this.muted[index] = muted;
    this.applyGain(index);
  }

  isMuted(index: number): boolean {
    return this.muted[index];
  }

  gain(index: number): number {
    return this.gainValues[index];
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
    return this.muted[index] ? 0 : this.gainValues[index];
  }

  private applyGain(index: number): void {
    const g = this.gains[index];
    if (!g || !this.ctx) return;
    g.gain.setTargetAtTime(this.effectiveGain(index), this.ctx.currentTime, 0.01);
  }

  private currentFrame(): number {
    if ((this.stateValue === 'playing' || this.stateValue === 'priming') && this.plan && this.rings) {
      return this.plan.songFrameAt(this.rings.readPos());
    }
    return this.anchorFrame;
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
    this.plan = new PlayPlan(this.anchorFrame, this.totalFrames, this.loop);
    if (Number.isFinite(this.plan.streamLength)) this.rings.setEof(this.plan.streamLength);
    for (const s of this.streamers) s.restart(this.plan, this.generation);
    this.rings.setState(STATE.PRIMING);
    this.timer = setInterval(() => this.tick(), 50);
    this.tick();
  }

  private tick(): void {
    if (!this.rings || !this.plan) return;
    if (this.stateValue !== 'priming' && this.stateValue !== 'playing') return;
    for (const s of this.streamers) s.tick();
    if (this.stateValue === 'priming') {
      const minFill = Math.round(this.opts.minFillSeconds * this.sampleRate);
      const allEof = this.streamers.every((s) => s.stats.eofReached);
      const needed = Number.isFinite(this.plan.streamLength) ? Math.min(minFill, this.plan.streamLength) : minFill;
      if (this.rings.minBuffered() >= needed || allEof) {
        this.rings.setState(STATE.PLAYING);
        if (this.master && this.ctx) this.master.gain.setTargetAtTime(1, this.ctx.currentTime, 0.01);
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
