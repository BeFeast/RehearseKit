/*
 * AudioWorklet processor for the RehearseKit streaming playback lab.
 *
 * Plain ES2020, no imports: served from /lab/stream-processor.js and loaded
 * with audioWorklet.addModule(). The memory layout and control indices come in
 * through processorOptions (see frontend/lib/lab-stream/ring-layout.ts), so
 * this file hardcodes nothing about the SharedArrayBuffer.
 *
 * The processor is deliberately dumb: it consumes a linear stream of frames
 * from N ring buffers in lockstep (one shared read cursor) and outputs each
 * stem on its own stereo output. If any stem lacks a full quantum, all stems
 * stay silent and one underrun is counted, so stems never drift apart.
 * Seek and loop logic live on the main thread (chunk-scheduler.ts).
 */

/* global AudioWorkletProcessor, registerProcessor */

class LabStreamProcessor extends AudioWorkletProcessor {
  constructor(options) {
    super();
    const { sab, layout, ctrl, state } = options.processorOptions;
    this.ctrl = new Int32Array(sab, layout.ctrlByteOffset, 16);
    this.C = ctrl;
    this.S = state;
    this.mask = layout.mask;
    this.ringFrames = layout.ringFrames;
    this.stems = layout.stems.map((stem) =>
      stem.channelByteOffsets.map((off) => new Float32Array(sab, off, layout.ringFrames)),
    );
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

  process(_inputs, outputs) {
    const ctrl = this.ctrl;
    const C = this.C;
    if (Atomics.load(ctrl, C.STATE) !== this.S.PLAYING) return true;

    const r = Atomics.load(ctrl, C.READ_POS);
    const eof = Atomics.load(ctrl, C.EOF_POS);
    const quantum = outputs.length > 0 && outputs[0].length > 0 ? outputs[0][0].length : 128;
    let need = quantum;
    if (eof >= 0) {
      const left = (eof - r) | 0;
      if (left <= 0) {
        Atomics.store(ctrl, C.ENDED, 1);
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
      return true;
    }

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
    }

    Atomics.store(ctrl, C.READ_POS, (r + need) | 0);
    Atomics.add(ctrl, C.QUANTA, 1);
    return true;
  }
}

registerProcessor('lab-stream-processor', LabStreamProcessor);
