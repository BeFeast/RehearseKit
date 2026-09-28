import { ARTICULATIONS, velTo127, type Articulation } from '../../lib/drums/taxonomy';
import type { DrumEvent } from '../../lib/drums/types';
import type { PlayPlan } from '../engine/chunk-scheduler';
import { CHOKES, layerFor, velocityGain, type LoadedKit } from './kit';

/** What the scheduler needs from the engine each tick (see StreamEngine.clock()). */
export interface ClockSnapshot {
  readPos: number;
  clockFrame: number;
  ctxTime: number;
  ctxFrame: number;
  generation: number;
  underruns: number;
}

/** A playing or scheduled voice the scheduler can stop. */
export interface Voice {
  stop(when: number): void;
  onended?: () => void;
}

export interface SchedulerHost {
  sampleRate: number;
  /** Null while the engine is not playing. */
  clock(): ClockSnapshot | null;
  /** The plan the engine is playing (stream frame → song frame). */
  plan(): PlayPlan | null;
  /** Start a sample at context time `when`; returns the voice, or null when the kit lacks the sound. */
  play(art: Articulation, vel127: number, when: number): Voice | null;
}

export interface SchedulerOptions {
  /** How far ahead of the audio clock hits are queued, in seconds. */
  lookaheadSeconds?: number;
  /** A hit later than this (already in the past) is skipped instead of played late. */
  lateToleranceSeconds?: number;
}

// The tick runs on the main thread, which a busy page (canvas redraws at
// 30 fps on a software renderer) can hold for well over 100 ms; the
// lookahead must cover such a gap, and a hit that turns up late within the
// tolerance is played at once rather than dropped.
const DEFAULT_LOOKAHEAD = 0.35;
const DEFAULT_LATE = 0.08;

/**
 * Schedules the edited hits on the engine's audio clock while it plays.
 *
 * The engine consumes a linear stream (a prefix, then the loop region
 * repeated); the audio thread publishes (stream frame, audio frame) pairs,
 * so a hit at song second t lands at a known stream frame in every pass and
 * is queued exactly once per pass — a loop wrap cannot double-trigger it.
 * Every seek/replan bumps the generation: queued voices are cancelled and
 * the cursor restarts at the clock. Edits, row mutes and mode changes call
 * reschedule(), which cancels what has not sounded yet and rescans.
 */
export class KitScheduler {
  private hits: DrumEvent[] = [];
  private muted: (art: Articulation) => boolean = () => false;
  private cursor = -1; // stream frame up to which hits are queued
  private generation = -1;
  private queued: { voice: Voice; when: number; art: Articulation }[] = [];
  private lastOpenHat: { voice: Voice; when: number } | null = null;
  private lastUnderruns = 0;
  /** Hits that fell behind the late tolerance before a tick ran (stats). */
  skipped = 0;
  /** The last queued hits (stats / scripts): song time, stream frame, context time. */
  readonly recent: { art: Articulation; t: number; sf: number; when: number; now: number }[] = [];
  readonly lookahead: number;
  readonly late: number;

  constructor(
    private readonly host: SchedulerHost,
    opts: SchedulerOptions = {},
  ) {
    this.lookahead = opts.lookaheadSeconds ?? DEFAULT_LOOKAHEAD;
    this.late = opts.lateToleranceSeconds ?? DEFAULT_LATE;
  }

  /** The hits to play (sorted by t) and the row-mute predicate. Calls reschedule(). */
  setHits(hits: DrumEvent[], muted: (art: Articulation) => boolean): void {
    this.hits = hits;
    this.muted = muted;
    this.reschedule();
  }

  /** Cancel everything not yet sounding and rescan from the clock on the next tick. */
  reschedule(): void {
    const c = this.host.clock();
    const now = c ? c.ctxTime : 0;
    this.cancelFrom(now);
    this.cursor = -1;
  }

  /** Cancel every queued voice (stop, seek, mode off). */
  stopAll(): void {
    this.cancelFrom(-Infinity);
    this.cursor = -1;
    this.generation = -1;
    this.lastOpenHat = null;
  }

  /** How many voices are queued (tests / stats). */
  get pending(): number {
    return this.queued.length;
  }

  /**
   * Called periodically (~25 ms). Maps the audio clock onto stream frames,
   * queues the hits between the cursor and the lookahead horizon.
   */
  tick(): void {
    const c = this.host.clock();
    const plan = this.host.plan();
    if (!c || !plan) {
      if (this.queued.length) this.stopAll();
      return;
    }
    const sr = this.host.sampleRate;
    if (c.generation !== this.generation) {
      this.cancelFrom(-Infinity);
      this.generation = c.generation;
      this.cursor = -1;
      this.lastOpenHat = null;
    }
    if (c.underruns !== this.lastUnderruns) {
      // The stream stalled: whatever was queued against the old mapping is early now.
      this.lastUnderruns = c.underruns;
      this.cancelFrom(c.ctxTime);
      this.cursor = -1;
    }
    // Audio frame → stream frame, through the last published pair.
    const nowStream = c.readPos + (((c.ctxFrame - c.clockFrame) << 0) | 0);
    // A late tick resumes from as far back as the tolerance allows: those
    // hits play immediately instead of being skipped.
    const floor = nowStream - Math.round(this.late * sr);
    if (this.cursor < 0) this.cursor = nowStream;
    else if (this.cursor < floor) {
      this.skipped += Math.max(0, this.countBetween(plan, this.cursor, floor));
      this.cursor = floor;
    }
    const horizon = nowStream + Math.round(this.lookahead * sr);
    if (horizon <= this.cursor) return;
    // Walk the stream segments between cursor and horizon.
    let pos = this.cursor;
    while (pos < horizon) {
      const chunk = plan.nextChunk(pos, horizon - pos);
      if (!chunk) break;
      const songStart = chunk.songStart;
      const songEnd = chunk.songStart + chunk.frames;
      this.queueRange(songStart / sr, songEnd / sr, chunk.streamStart - songStart, nowStream, c.ctxTime, sr);
      pos = chunk.streamStart + chunk.frames;
    }
    this.cursor = horizon;
    // Drop finished voices from the queue.
    const cutoff = c.ctxTime - 0.05;
    if (this.queued.length && this.queued[0].when < cutoff) this.queued = this.queued.filter((q) => q.when >= cutoff);
  }

  /** Hits between two stream frames (for the skipped counter). */
  private countBetween(plan: PlayPlan, from: number, to: number): number {
    let n = 0;
    let pos = from;
    while (pos < to) {
      const chunk = plan.nextChunk(pos, to - pos);
      if (!chunk) break;
      const t0 = chunk.songStart / this.host.sampleRate;
      const t1 = (chunk.songStart + chunk.frames) / this.host.sampleRate;
      for (let i = lowerBound(this.hits, t0); i < this.hits.length && this.hits[i].t < t1; i++) if (!this.muted(this.hits[i].art)) n++;
      pos = chunk.streamStart + chunk.frames;
    }
    return n;
  }

  /** Queue hits with t in [t0, t1); streamOffset = streamFrame - songFrame for this segment. */
  private queueRange(t0: number, t1: number, streamOffset: number, nowStream: number, ctxNow: number, sr: number): void {
    const hits = this.hits;
    let i = lowerBound(hits, t0);
    for (; i < hits.length; i++) {
      const h = hits[i];
      if (h.t >= t1) break;
      if (this.muted(h.art)) continue;
      const sf = h.t * sr + streamOffset;
      const when = ctxNow + (sf - nowStream) / sr;
      if (when < ctxNow - this.late) continue;
      const voice = this.host.play(h.art, velTo127(h.vel), Math.max(when, ctxNow));
      if (!voice) continue;
      this.recent.push({ art: h.art, t: h.t, sf, when, now: ctxNow });
      if (this.recent.length > 96) this.recent.splice(0, this.recent.length - 96);
      const entry = { voice, when, art: h.art };
      this.queued.push(entry);
      voice.onended = () => {
        const k = this.queued.indexOf(entry);
        if (k >= 0) this.queued.splice(k, 1);
      };
      // Choke: a closed or pedal hat ends the open hat that is still ringing.
      const chokes = CHOKES[h.art];
      if (chokes && this.lastOpenHat && this.lastOpenHat.when < when) {
        this.lastOpenHat.voice.stop(when);
        this.lastOpenHat = null;
      }
      if (h.art === 'hho') this.lastOpenHat = { voice, when };
    }
  }

  private cancelFrom(when: number): void {
    const keep: typeof this.queued = [];
    for (const q of this.queued) {
      if (q.when >= when) q.voice.stop(Math.max(0, when));
      else keep.push(q);
    }
    this.queued = keep;
    if (this.lastOpenHat && this.lastOpenHat.when >= when) this.lastOpenHat = null;
  }
}

/** First index with hits[i].t >= t. */
export function lowerBound(hits: DrumEvent[], t: number): number {
  let lo = 0;
  let hi = hits.length;
  while (lo < hi) {
    const mid = (lo + hi) >> 1;
    if (hits[mid].t < t) lo = mid + 1;
    else hi = mid;
  }
  return lo;
}

/** Build the play() function of a host over a loaded kit and a destination node. */
export function makeKitPlayer(ctx: BaseAudioContext, kit: LoadedKit, out: AudioNode, bus: GainNode): SchedulerHost['play'] {
  return (art, vel127, when) => {
    const layers = kit.manifest.articulations[art];
    const layer = layers ? layerFor(layers, vel127) : null;
    const buffer = layer ? kit.buffers.get(layer.file) : undefined;
    if (!layer || !buffer) return null;
    const src = ctx.createBufferSource();
    src.buffer = buffer;
    const g = ctx.createGain();
    g.gain.value = velocityGain(vel127, layer);
    src.connect(g);
    g.connect(bus);
    bus.connect(out);
    src.start(when);
    const voice: Voice = {
      stop(at) {
        try {
          // A short fade avoids a click on a choke or a cancel.
          g.gain.setTargetAtTime(0, Math.max(at, ctx.currentTime), 0.004);
          src.stop(Math.max(at, ctx.currentTime) + 0.03);
        } catch {
          // already stopped
        }
      },
    };
    src.onended = () => {
      src.disconnect();
      g.disconnect();
      voice.onended?.();
    };
    return voice;
  };
}

export { ARTICULATIONS };
