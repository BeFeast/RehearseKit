import { createRingLayout, CTRL, STATE, MAX_STEMS } from '../ring-layout';
import { SharedRings } from '../shared-ring';

function makeRings(channels: number[], ringFrames = 16) {
  const layout = createRingLayout(channels, ringFrames);
  const sab = new SharedArrayBuffer(layout.totalBytes);
  return { rings: new SharedRings(sab, layout), layout, sab };
}

const ramp = (n: number, base = 0) => new Float32Array(Array.from({ length: n }, (_, i) => base + i));

describe('createRingLayout', () => {
  it('lays out planar channels after the control block', () => {
    const layout = createRingLayout([2, 1], 8);
    expect(layout.stems[0].channelByteOffsets).toEqual([64, 96]);
    expect(layout.stems[1].channelByteOffsets).toEqual([128]);
    expect(layout.totalBytes).toBe(160);
    expect(layout.mask).toBe(7);
  });
  it('validates inputs', () => {
    expect(() => createRingLayout([2], 12)).toThrow(/power of two/);
    expect(() => createRingLayout([], 8)).toThrow(/stem count/);
    expect(() => createRingLayout(new Array(MAX_STEMS + 1).fill(2), 8)).toThrow(/stem count/);
    expect(() => createRingLayout([3], 8)).toThrow(/channels/);
  });
});

describe('SharedRings', () => {
  it('writes, wraps and exposes counters', () => {
    const { rings } = makeRings([2], 16);
    expect(rings.buffered(0)).toBe(0);
    expect(rings.space(0)).toBe(16);
    expect(rings.write(0, [ramp(10), ramp(10, 100)], 10)).toBe(true);
    expect(rings.buffered(0)).toBe(10);
    expect(rings.write(0, [ramp(10, 10), ramp(10, 110)], 10)).toBe(false); // no room
    // consumer reads 8 frames
    const ctrl = new Int32Array(rings.sab, 0, 16);
    Atomics.store(ctrl, CTRL.READ_POS, 8);
    expect(rings.space(0)).toBe(14);
    expect(rings.write(0, [ramp(10, 10), ramp(10, 110)], 10)).toBe(true); // wraps at 16
    expect(rings.writePos(0)).toBe(20);
    expect(Array.from(rings.peek(0, 0, 8, 12))).toEqual([8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19]);
    expect(Array.from(rings.peek(0, 1, 16, 4))).toEqual([116, 117, 118, 119]);
  });

  it('duplicates a mono source into stereo rings', () => {
    const { rings } = makeRings([2], 8);
    rings.write(0, [ramp(4)], 4);
    expect(Array.from(rings.peek(0, 1, 0, 4))).toEqual([0, 1, 2, 3]);
  });

  it('tracks minBuffered across stems and resets writers', () => {
    const { rings } = makeRings([2, 2, 1], 8);
    rings.write(0, [ramp(4), ramp(4)], 4);
    rings.write(1, [ramp(2), ramp(2)], 2);
    expect(rings.minBuffered()).toBe(0);
    rings.write(2, [ramp(6)], 6);
    expect(rings.minBuffered()).toBe(2);
    rings.setEof(123);
    rings.setState(STATE.PLAYING);
    expect(rings.eofPos()).toBe(123);
    expect(rings.state()).toBe(STATE.PLAYING);
    rings.resetWriters();
    expect(rings.writePos(0)).toBe(0);
    expect(rings.writePos(2)).toBe(0);
    expect(rings.eofPos()).toBe(-1);
    expect(rings.ended()).toBe(false);
    expect(rings.underruns()).toBe(0);
    expect(rings.quanta()).toBe(0);
  });

  it('rejects an undersized buffer', () => {
    const layout = createRingLayout([2], 8);
    expect(() => new SharedRings(new SharedArrayBuffer(16), layout)).toThrow(/smaller/);
  });
});
