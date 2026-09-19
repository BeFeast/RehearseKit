/*
 * AudioWorklet processor for the RehearseKit streaming player.
 *
 * Plain ES2020, no imports: served from /stream-processor.js and loaded with
 * audioWorklet.addModule(). The memory layout and control indices come in
 * through processorOptions (see src/player/engine/ring-layout.ts), so this
 * file hardcodes nothing about the SharedArrayBuffer.
 *
 * The processor is deliberately dumb: it consumes a linear stream of frames
 * from N ring buffers in lockstep (one shared read cursor) and outputs each
 * stem on its own stereo output. If any stem lacks a full quantum, all stems
 * stay silent and one underrun is counted, so stems never drift apart.
 * Seek and loop logic live on the main thread (chunk-scheduler.ts).
 *
 * It also writes per-stem meters (peak follower, RMS follower, peak hold)
 * into the shared meter block from the raw stem signal. Release is
 * meter.releaseDbPerS (−20 dB/s); the hold lasts meter.holdSeconds (1.5 s).
 */

/* global AudioWorkletProcessor, registerProcessor, sampleRate */

class LabStreamProcessor extends AudioWorkletProcessor {
  constructor(options) {
    super();
    const { sab, layout, ctrl, state, meter } = options.processorOptions;
    this.ctrl = new Int32Array(sab, layout.ctrlByteOffset, 16);
    this.C = ctrl;
    this.S = state;
    this.mask = layout.mask;
    this.ringFrames = layout.ringFrames;
    this.stems = layout.stems.map((stem) =>
      stem.channelByteOffsets.map((off) => new Float32Array(sab, off, layout.ringFrames)),
    );
    const m = meter || { floats: 3, releaseDbPerS: 20, holdSeconds: 1.5 };
    this.M = m;
    this.meterBlock =
      layout.meterByteOffset !== undefined
        ? new Float32Array(sab, layout.meterByteOffset, this.stems.length * m.floats)
        : null;
    this.holdUntil = new Float64Array(this.stems.length);
    this.now = 0; // frames of audio time processed, for the hold timer
    this.port.onmessage = (event) => {
      const msg = event.data;
      if (msg && msg.type === 'flush') {
        Atomics.store(this.ctrl, this.C.READ_POS, 0);
        Atomics.store(this.ctrl, this.C.ENDED, 0);
        this.port.postMessage({ type: 'flushed', generation: msg.generation });
      }
    };
    this.port.postMessage({ type: 'ready' });
  }

  /** Release every follower by `frames` worth of time (no new signal). */
  releaseMeters(frames) {
    const mb = this.meterBlock;
    if (!mb) return;
    const sr = typeof sampleRate === 'number' ? sampleRate : 48000;
    const decay = Math.pow(10, (-this.M.releaseDbPerS / 20) * (frames / sr));
    const n = this.M.floats;
    this.now += frames;
    for (let i = 0; i < this.stems.length; i++) {
      const o = i * n;
      mb[o] *= decay;
      mb[o + 1] *= decay;
      if (this.now >= this.holdUntil[i]) mb[o + 2] = Math.max(mb[o], mb[o + 2] * decay);
    }
  }

  /** Fold one stem's quantum into its meters. */
  meterStem(i, rings, r, need) {
    const mb = this.meterBlock;
    if (!mb) return;
    const mask = this.mask;
    let peak = 0;
    let sumsq = 0;
    let count = 0;
    for (let c = 0; c < rings.length; c++) {
      const ring = rings[c];
      for (let k = 0; k < need; k++) {
        const v = ring[(r + k) & mask];
        const a = v < 0 ? -v : v;
        if (a > peak) peak = a;
        sumsq += v * v;
      }
      count += need;
    }
    const rms = count > 0 ? Math.sqrt(sumsq / count) : 0;
    const o = i * this.M.floats;
    if (peak > mb[o]) mb[o] = peak;
    if (rms > mb[o + 1]) mb[o + 1] = rms;
    if (peak >= mb[o + 2]) {
      mb[o + 2] = peak;
      const sr = typeof sampleRate === 'number' ? sampleRate : 48000;
      this.holdUntil[i] = this.now + this.M.holdSeconds * sr;
    }
  }

  process(_inputs, outputs) {
    const ctrl = this.ctrl;
    const C = this.C;
    const quantum = outputs.length > 0 && outputs[0].length > 0 ? outputs[0][0].length : 128;
    if (Atomics.load(ctrl, C.STATE) !== this.S.PLAYING) {
      this.releaseMeters(quantum);
      return true;
    }

    const r = Atomics.load(ctrl, C.READ_POS);
    const eof = Atomics.load(ctrl, C.EOF_POS);
    let need = quantum;
    if (eof >= 0) {
      const left = (eof - r) | 0;
      if (left <= 0) {
        Atomics.store(ctrl, C.ENDED, 1);
        this.releaseMeters(quantum);
        return true;
      }
      if (left < need) need = left;
    }

    let avail = 0x7fffffff;
    for (let i = 0; i < this.stems.length; i++) {
      const a = (Atomics.load(ctrl, C.WRITE_POS0 + i) - r) | 0;
      if (a < avail) avail = a;
    }
    if (avail < need) {
      Atomics.add(ctrl, C.UNDERRUNS, 1);
      this.releaseMeters(quantum);
      return true;
    }

    // Release first, then let this quantum's peaks push the followers up.
    this.releaseMeters(need);
    const mask = this.mask;
    for (let i = 0; i < this.stems.length; i++) {
      const rings = this.stems[i];
      const out = outputs[i];
      if (!out) continue;
      for (let c = 0; c < out.length; c++) {
        const ring = rings[c < rings.length ? c : rings.length - 1];
        const dst = out[c];
        for (let k = 0; k < need; k++) dst[k] = ring[(r + k) & mask];
      }
      this.meterStem(i, rings, r, need);
    }

    Atomics.store(ctrl, C.READ_POS, (r + need) | 0);
    Atomics.add(ctrl, C.QUANTA, 1);
    return true;
  }
}

registerProcessor('lab-stream-processor', LabStreamProcessor);
