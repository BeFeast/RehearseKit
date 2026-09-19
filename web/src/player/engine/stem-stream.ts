import { byteRangeFor, shouldFetch, type PlayPlan } from './chunk-scheduler';
import { decodeInterleaved } from './pcm-decode';
import { assertPartial, rangeHeader } from './range-utils';
import type { SharedRings } from './shared-ring';
import type { WavFormat } from './wav-header';

export interface StemStreamerOptions {
  url: string;
  fmt: WavFormat;
  index: number;
  rings: SharedRings;
  chunkFrames: number;
  firstChunkFrames: number;
  prefetchFrames: number;
  fetchImpl?: typeof fetch;
  onChunk?: () => void;
  onError?: (error: Error) => void;
}

export interface StemStreamStats {
  requests: number;
  bytes: number;
  inFlight: boolean;
  eofReached: boolean;
  streamPos: number;
  lastFetchMs: number;
  maxFetchMs: number;
}

/**
 * Pulls one stem through HTTP range requests into its ring buffer, following
 * a PlayPlan. At most one request is in flight per stem; results that belong
 * to a superseded generation (seek/stop happened meanwhile) are dropped.
 */
export class StemStreamer {
  private readonly o: StemStreamerOptions;
  private readonly fetchImpl: typeof fetch;
  private readonly scratch: Float32Array[];
  private plan: PlayPlan | null = null;
  private generation = 0;
  private streamPos = 0;
  private firstChunk = true;
  private controller: AbortController | null = null;
  private eofReached = false;
  readonly stats: StemStreamStats = {
    requests: 0,
    bytes: 0,
    inFlight: false,
    eofReached: false,
    streamPos: 0,
    lastFetchMs: 0,
    maxFetchMs: 0,
  };

  constructor(options: StemStreamerOptions) {
    this.o = options;
    this.fetchImpl = options.fetchImpl ?? ((input, init) => fetch(input, init));
    const scratchFrames = Math.max(options.chunkFrames, options.firstChunkFrames);
    this.scratch = Array.from({ length: options.fmt.channels }, () => new Float32Array(scratchFrames));
  }

  restart(plan: PlayPlan, generation: number): void {
    this.abort();
    this.plan = plan;
    this.generation = generation;
    this.streamPos = 0;
    this.firstChunk = true;
    this.eofReached = false;
    this.stats.eofReached = false;
    this.stats.streamPos = 0;
  }

  abort(): void {
    if (this.controller) {
      this.controller.abort();
      this.controller = null;
    }
    this.stats.inFlight = false;
    this.plan = null;
  }

  /** Issue the next range request when the ring has room and the lead is short. */
  tick(): void {
    if (!this.plan || this.controller || this.eofReached) return;
    const maxFrames = this.firstChunk ? this.o.firstChunkFrames : this.o.chunkFrames;
    const chunk = this.plan.nextChunk(this.streamPos, maxFrames);
    if (!chunk) {
      this.eofReached = true;
      this.stats.eofReached = true;
      return;
    }
    const { rings, index } = this.o;
    if (!shouldFetch(rings.buffered(index), rings.space(index), chunk.frames, this.o.prefetchFrames)) return;
    void this.fetchChunk(chunk.songStart, chunk.frames, this.generation);
  }

  private async fetchChunk(songStart: number, frames: number, generation: number): Promise<void> {
    const controller = new AbortController();
    this.controller = controller;
    this.stats.inFlight = true;
    const t0 = performance.now();
    try {
      const { fmt, url, rings, index } = this.o;
      const { start, end } = byteRangeFor(fmt, songStart, frames);
      const res = await this.fetchImpl(url, {
        headers: { Range: rangeHeader(start, end) },
        signal: controller.signal,
        cache: 'no-store',
      });
      assertPartial(res.status, res.headers.get('content-range'), start, end);
      const buf = await res.arrayBuffer();
      if (generation !== this.generation || controller.signal.aborted) return;
      const expected = frames * fmt.blockAlign;
      if (buf.byteLength !== expected) {
        throw new Error(`short range body: got ${buf.byteLength} bytes, expected ${expected}`);
      }
      decodeInterleaved(buf, 0, frames, fmt, this.scratch);
      if (!rings.write(index, this.scratch, frames)) {
        throw new Error('ring buffer overflow (scheduler asked for more than the free space)');
      }
      this.streamPos += frames;
      this.firstChunk = false;
      const ms = performance.now() - t0;
      this.stats.requests += 1;
      this.stats.bytes += buf.byteLength;
      this.stats.lastFetchMs = ms;
      this.stats.maxFetchMs = Math.max(this.stats.maxFetchMs, ms);
      this.stats.streamPos = this.streamPos;
      this.o.onChunk?.();
    } catch (err) {
      if (controller.signal.aborted) return;
      this.o.onError?.(err instanceof Error ? err : new Error(String(err)));
    } finally {
      if (this.controller === controller) {
        this.controller = null;
        this.stats.inFlight = false;
      }
    }
  }
}
