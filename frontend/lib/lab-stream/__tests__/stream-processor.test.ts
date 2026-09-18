/* eslint-disable @typescript-eslint/no-explicit-any */
import { createRingLayout, CTRL, STATE } from '../ring-layout';
import { SharedRings } from '../shared-ring';

type ProcessorCtor = new (options: { processorOptions: Record<string, unknown> }) => {
  process(inputs: Float32Array[][], outputs: Float32Array[][]): boolean;
  port: { postMessage: jest.Mock; onmessage: ((e: { data: unknown }) => void) | null };
};

let Processor: ProcessorCtor;

beforeAll(() => {
  class FakeAudioWorkletProcessor {
    port = { postMessage: jest.fn(), onmessage: null as ((e: { data: unknown }) => void) | null };
  }
  (globalThis as any).AudioWorkletProcessor = FakeAudioWorkletProcessor;
  (globalThis as any).registerProcessor = (_name: string, cls: ProcessorCtor) => {
    Processor = cls;
  };
  // eslint-disable-next-line @typescript-eslint/no-require-imports
  require('@/public/lab/stream-processor.js');
});

const QUANTUM = 128;

function setup(stemChannels: number[], ringFrames = 512) {
  const layout = createRingLayout(stemChannels, ringFrames);
  const sab = new SharedArrayBuffer(layout.totalBytes);
  const rings = new SharedRings(sab, layout);
  const proc = new Processor({ processorOptions: { sab, layout, ctrl: CTRL, state: STATE } });
  const outputs = stemChannels.map(() => [new Float32Array(QUANTUM), new Float32Array(QUANTUM)]);
  const clearOutputs = () => outputs.forEach((o) => o.forEach((c) => c.fill(0)));
  return { layout, rings, proc, outputs, clearOutputs };
}

const ramp = (n: number, base = 0) => new Float32Array(Array.from({ length: n }, (_, i) => base + i));

describe('lab-stream-processor', () => {
  it('registers and announces readiness', () => {
    const { proc } = setup([2]);
    expect(proc.port.postMessage).toHaveBeenCalledWith({ type: 'ready' });
  });

  it('stays silent and counts nothing while not playing', () => {
    const { proc, rings, outputs } = setup([2]);
    rings.write(0, [ramp(QUANTUM), ramp(QUANTUM)], QUANTUM);
    expect(proc.process([], outputs)).toBe(true);
    expect(rings.readPos()).toBe(0);
    expect(rings.underruns()).toBe(0);
    rings.setState(STATE.PRIMING);
    proc.process([], outputs);
    expect(rings.readPos()).toBe(0);
    expect(rings.underruns()).toBe(0);
  });

  it('copies one quantum per stem in lockstep and advances the cursor', () => {
    const { proc, rings, outputs } = setup([2, 2]);
    rings.write(0, [ramp(QUANTUM), ramp(QUANTUM, 1000)], QUANTUM);
    rings.write(1, [ramp(QUANTUM, 2000), ramp(QUANTUM, 3000)], QUANTUM);
    rings.setState(STATE.PLAYING);
    proc.process([], outputs);
    expect(rings.readPos()).toBe(QUANTUM);
    expect(rings.quanta()).toBe(1);
    expect(outputs[0][0][5]).toBe(5);
    expect(outputs[0][1][5]).toBe(1005);
    expect(outputs[1][0][127]).toBe(2127);
    expect(outputs[1][1][0]).toBe(3000);
  });

  it('counts an underrun and outputs nothing when any stem is short', () => {
    const { proc, rings, outputs, clearOutputs } = setup([2, 2]);
    rings.write(0, [ramp(QUANTUM, 1), ramp(QUANTUM, 1)], QUANTUM);
    rings.write(1, [ramp(100, 1), ramp(100, 1)], 100); // short by 28 frames
    rings.setState(STATE.PLAYING);
    clearOutputs();
    proc.process([], outputs);
    expect(rings.underruns()).toBe(1);
    expect(rings.readPos()).toBe(0);
    expect(outputs[0][0].every((v) => v === 0)).toBe(true);
    // Top up the short stem and the same quantum plays.
    rings.write(1, [ramp(28, 101), ramp(28, 101)], 28);
    proc.process([], outputs);
    expect(rings.underruns()).toBe(1);
    expect(rings.readPos()).toBe(QUANTUM);
    expect(outputs[1][0][127]).toBe(128);
  });

  it('reads across the ring wrap-around', () => {
    const { proc, rings, outputs } = setup([2], 256);
    // Fill 200, consume 200, write 128 more which wraps at 256.
    rings.write(0, [ramp(200), ramp(200)], 200);
    rings.setState(STATE.PLAYING);
    proc.process([], outputs);
    const ctrl = new Int32Array(rings.sab, 0, 16);
    Atomics.store(ctrl, CTRL.READ_POS, 200);
    rings.write(0, [ramp(QUANTUM, 200), ramp(QUANTUM, 200)], QUANTUM);
    proc.process([], outputs);
    expect(outputs[0][0][0]).toBe(200);
    expect(outputs[0][0][55]).toBe(255);
    expect(outputs[0][0][56]).toBe(256);
    expect(outputs[0][0][127]).toBe(327);
    expect(rings.readPos()).toBe(328);
  });

  it('duplicates mono stems to both output channels', () => {
    const { proc, rings, outputs } = setup([1]);
    rings.write(0, [ramp(QUANTUM, 7)], QUANTUM);
    rings.setState(STATE.PLAYING);
    proc.process([], outputs);
    expect(outputs[0][0][3]).toBe(10);
    expect(outputs[0][1][3]).toBe(10);
  });

  it('plays a partial final quantum at EOF, then flags ended without underruns', () => {
    const { proc, rings, outputs, clearOutputs } = setup([2]);
    rings.write(0, [ramp(50, 1), ramp(50, 1)], 50);
    rings.setEof(50);
    rings.setState(STATE.PLAYING);
    clearOutputs();
    proc.process([], outputs);
    expect(rings.readPos()).toBe(50);
    expect(outputs[0][0][49]).toBe(50);
    expect(outputs[0][0][50]).toBe(0);
    expect(rings.ended()).toBe(false);
    proc.process([], outputs);
    expect(rings.ended()).toBe(true);
    expect(rings.underruns()).toBe(0);
    expect(rings.readPos()).toBe(50);
  });

  it('empty stream (seek to end) ends immediately', () => {
    const { proc, rings, outputs } = setup([2]);
    rings.setEof(0);
    rings.setState(STATE.PLAYING);
    proc.process([], outputs);
    expect(rings.ended()).toBe(true);
    expect(rings.underruns()).toBe(0);
  });

  it('acknowledges a flush by resetting the read cursor', () => {
    const { proc, rings, outputs } = setup([2]);
    rings.write(0, [ramp(QUANTUM), ramp(QUANTUM)], QUANTUM);
    rings.setState(STATE.PLAYING);
    proc.process([], outputs);
    expect(rings.readPos()).toBe(QUANTUM);
    rings.setState(STATE.STOPPED);
    proc.port.onmessage!({ data: { type: 'flush', generation: 7 } });
    expect(rings.readPos()).toBe(0);
    expect(proc.port.postMessage).toHaveBeenLastCalledWith({ type: 'flushed', generation: 7 });
    proc.port.onmessage!({ data: { type: 'noise' } });
    expect(proc.port.postMessage).toHaveBeenCalledTimes(2);
  });
});
