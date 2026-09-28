import { describe, expect, it } from 'vitest';
import { layerFor, velocityGain, type KitLayer } from '../kit';

const layers: KitLayer[] = [
  { file: 'kick-1.flac', vel: [1, 50], gain: 1 },
  { file: 'kick-2.flac', vel: [51, 100], gain: 1 },
  { file: 'kick-3.flac', vel: [101, 127], gain: 0.9 },
];

describe('kit layers', () => {
  it('picks the layer covering the velocity, the last one when none matches', () => {
    expect(layerFor(layers, 1)!.file).toBe('kick-1.flac');
    expect(layerFor(layers, 50)!.file).toBe('kick-1.flac');
    expect(layerFor(layers, 51)!.file).toBe('kick-2.flac');
    expect(layerFor(layers, 127)!.file).toBe('kick-3.flac');
    expect(layerFor(layers, 200)!.file).toBe('kick-3.flac');
    expect(layerFor([], 64)).toBeNull();
  });

  it('shades the gain within a layer from 0.55 to 1.0 times the layer gain', () => {
    expect(velocityGain(51, layers[1])).toBeCloseTo(0.55, 6);
    expect(velocityGain(100, layers[1])).toBeCloseTo(1, 6);
    expect(velocityGain(127, layers[2])).toBeCloseTo(0.9, 6);
    expect(velocityGain(64, { file: 'x', vel: [64, 64], gain: 1 })).toBe(1);
  });
});
