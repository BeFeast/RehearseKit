import { byteRangeFor, PlayPlan, shouldFetch, validateLoop, MIN_LOOP_FRAMES } from '../chunk-scheduler';

const TOTAL = 100_000;

describe('validateLoop', () => {
  it('clamps and rejects unusable ranges', () => {
    expect(validateLoop(null, TOTAL)).toBeNull();
    expect(validateLoop({ start: 10, end: 5 }, TOTAL)).toBeNull();
    expect(validateLoop({ start: 10, end: 10 + MIN_LOOP_FRAMES - 1 }, TOTAL)).toBeNull();
    expect(validateLoop({ start: -5, end: 200_000 }, TOTAL)).toEqual({ start: 0, end: TOTAL });
    expect(validateLoop({ start: 10.7, end: 5000.2 }, TOTAL)).toEqual({ start: 10, end: 5000 });
  });
});

describe('PlayPlan without loop', () => {
  it('maps a linear play-through and ends at totalFrames', () => {
    const plan = new PlayPlan(1000, TOTAL, null);
    expect(plan.isLooping).toBe(false);
    expect(plan.prefixFrames).toBe(99_000);
    expect(plan.streamLength).toBe(99_000);
    expect(plan.songFrameAt(0)).toBe(1000);
    expect(plan.songFrameAt(98_999)).toBe(99_999);
    expect(plan.songFrameAt(99_000)).toBe(TOTAL);
    expect(plan.songFrameAt(5_000_000)).toBe(TOTAL);
  });

  it('emits chunks up to maxFrames and stops at EOF', () => {
    const plan = new PlayPlan(99_500, TOTAL, null);
    expect(plan.nextChunk(0, 300)).toEqual({ streamStart: 0, songStart: 99_500, frames: 300 });
    expect(plan.nextChunk(300, 300)).toEqual({ streamStart: 300, songStart: 99_800, frames: 200 });
    expect(plan.nextChunk(500, 300)).toBeNull();
  });

  it('seek to end yields an empty stream', () => {
    const plan = new PlayPlan(TOTAL, TOTAL, null);
    expect(plan.streamLength).toBe(0);
    expect(plan.nextChunk(0, 100)).toBeNull();
    expect(plan.songFrameAt(0)).toBe(TOTAL);
    const beyond = new PlayPlan(TOTAL + 50, TOTAL, null);
    expect(beyond.startFrame).toBe(TOTAL);
  });
});

describe('PlayPlan with loop', () => {
  const loop = { start: 20_000, end: 30_000 };

  it('plays the prefix then repeats the loop forever', () => {
    const plan = new PlayPlan(15_000, TOTAL, loop);
    expect(plan.isLooping).toBe(true);
    expect(plan.prefixFrames).toBe(15_000);
    expect(plan.streamLength).toBe(Infinity);
    expect(plan.songFrameAt(0)).toBe(15_000);
    expect(plan.songFrameAt(14_999)).toBe(29_999);
    expect(plan.songFrameAt(15_000)).toBe(20_000);
    expect(plan.songFrameAt(15_000 + 9_999)).toBe(29_999);
    expect(plan.songFrameAt(15_000 + 10_000)).toBe(20_000);
    expect(plan.songFrameAt(15_000 + 12_345_678)).toBe(20_000 + (12_345_678 % 10_000));
  });

  it('never emits a chunk across a segment boundary', () => {
    const plan = new PlayPlan(29_000, TOTAL, loop);
    expect(plan.nextChunk(0, 4_096)).toEqual({ streamStart: 0, songStart: 29_000, frames: 1_000 });
    expect(plan.nextChunk(1_000, 4_096)).toEqual({ streamStart: 1_000, songStart: 20_000, frames: 4_096 });
    expect(plan.nextChunk(1_000 + 9_000, 4_096)).toEqual({ streamStart: 10_000, songStart: 29_000, frames: 1_000 });
    expect(plan.nextChunk(11_000, 4_096)).toEqual({ streamStart: 11_000, songStart: 20_000, frames: 4_096 });
  });

  it('starting inside the loop has a shorter prefix', () => {
    const plan = new PlayPlan(25_000, TOTAL, loop);
    expect(plan.prefixFrames).toBe(5_000);
    expect(plan.songFrameAt(5_000)).toBe(20_000);
  });

  it('starting at or after loop end plays through to the end', () => {
    const plan = new PlayPlan(30_000, TOTAL, loop);
    expect(plan.isLooping).toBe(false);
    expect(plan.streamLength).toBe(70_000);
  });

  it('a loop shorter than the chunk yields many small chunks', () => {
    const plan = new PlayPlan(0, TOTAL, { start: 0, end: MIN_LOOP_FRAMES });
    let pos = 0;
    for (let i = 0; i < 5; i++) {
      const c = plan.nextChunk(pos, 44_100);
      expect(c).not.toBeNull();
      expect(c!.frames).toBe(MIN_LOOP_FRAMES);
      expect(c!.songStart).toBe(0);
      pos += c!.frames;
    }
  });

  it('ignores an invalid loop', () => {
    const plan = new PlayPlan(0, TOTAL, { start: 50, end: 10 });
    expect(plan.isLooping).toBe(false);
  });
});

describe('byteRangeFor', () => {
  const fmt = { dataOffset: 88, blockAlign: 8, totalFrames: 1000 };
  it('computes inclusive byte ranges', () => {
    expect(byteRangeFor(fmt, 0, 10)).toEqual({ start: 88, end: 88 + 80 - 1 });
    expect(byteRangeFor(fmt, 990, 10)).toEqual({ start: 88 + 7920, end: 88 + 8000 - 1 });
  });
  it('rejects out-of-file ranges', () => {
    expect(() => byteRangeFor(fmt, 995, 10)).toThrow();
    expect(() => byteRangeFor(fmt, 0, 0)).toThrow();
  });
});

describe('shouldFetch', () => {
  it('needs room for a whole chunk and lead below the prefetch window', () => {
    expect(shouldFetch(0, 10_000, 1_000, 5_000)).toBe(true);
    expect(shouldFetch(5_000, 10_000, 1_000, 5_000)).toBe(false);
    expect(shouldFetch(0, 999, 1_000, 5_000)).toBe(false);
  });
});
