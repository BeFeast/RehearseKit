import { describe, expect, it } from 'vitest';
import { initialMix, isSilenced, mixReducer, sanitize, type MixState } from '../mix-state';

const stems = ['vocals', 'drums', 'bass', 'other'];
const UNITY = 0.84;

describe('mixReducer', () => {
  const base = () => initialMix(stems, UNITY);

  it('starts every stem at unity, unmuted, no solo, no loop', () => {
    const s = base();
    expect(Object.keys(s.stems)).toEqual(stems);
    expect(s.stems.vocals).toEqual({ position: UNITY, muted: false });
    expect(s.solo).toBeNull();
    expect(s.loop).toBeNull();
    expect(s.loopEnabled).toBe(false);
    expect(s.selected).toBeNull();
  });

  it('sets gain (clamped) and mute per stem, ignoring unknown stems', () => {
    let s = mixReducer(base(), { type: 'gain', stem: 'drums', position: 1.7 });
    expect(s.stems.drums.position).toBe(1);
    s = mixReducer(s, { type: 'gain', stem: 'drums', position: -1 });
    expect(s.stems.drums.position).toBe(0);
    s = mixReducer(s, { type: 'mute', stem: 'bass', muted: true });
    expect(s.stems.bass.muted).toBe(true);
    expect(s.stems.bass.position).toBe(UNITY);
    const same = mixReducer(s, { type: 'gain', stem: 'piano', position: 0.5 });
    expect(same).toBe(s);
  });

  it('solo is exclusive and silences the others without muting them', () => {
    let s = mixReducer(base(), { type: 'solo', stem: 'drums' });
    expect(s.solo).toBe('drums');
    expect(isSilenced(s, 'vocals')).toBe(true);
    expect(isSilenced(s, 'drums')).toBe(false);
    expect(s.stems.vocals.muted).toBe(false);
    s = mixReducer(s, { type: 'solo', stem: 'bass' });
    expect(s.solo).toBe('bass');
    s = mixReducer(s, { type: 'solo', stem: null });
    expect(s.solo).toBeNull();
    expect(isSilenced(s, 'vocals')).toBe(false);
  });

  it('loop range must be forward; enabling needs a range; clearing disables', () => {
    let s = mixReducer(base(), { type: 'loopEnabled', enabled: true });
    expect(s.loopEnabled).toBe(false);
    s = mixReducer(s, { type: 'loop', loop: { start: 10, end: 5 } });
    expect(s.loop).toBeNull();
    s = mixReducer(s, { type: 'loop', loop: { start: 46, end: 84 } });
    s = mixReducer(s, { type: 'loopEnabled', enabled: true });
    expect(s.loopEnabled).toBe(true);
    s = mixReducer(s, { type: 'loop', loop: null });
    expect(s.loop).toBeNull();
    expect(s.loopEnabled).toBe(false);
  });

  it('selection and master fader', () => {
    let s = mixReducer(base(), { type: 'select', stem: 'other' });
    expect(s.selected).toBe('other');
    s = mixReducer(s, { type: 'select', stem: null });
    expect(s.selected).toBeNull();
    s = mixReducer(s, { type: 'master', position: 0.3 });
    expect(s.masterPosition).toBe(0.3);
  });

  it('returns the same object for no-op actions', () => {
    const s = base();
    expect(mixReducer(s, { type: 'mute', stem: 'vocals', muted: false })).toBe(s);
    expect(mixReducer(s, { type: 'solo', stem: null })).toBe(s);
    expect(mixReducer(s, { type: 'select', stem: null })).toBe(s);
  });
});

describe('sanitize', () => {
  it('brings API/localStorage state into shape for the current stems', () => {
    const raw = {
      stems: { vocals: { position: 0.2, muted: true }, drums: { position: 5 }, piano: { position: 0.1 } },
      solo: 'piano',
      loop: { start: 10, end: 20 },
      loopEnabled: true,
      selected: 'drums',
      masterPosition: 0.9,
    };
    const s = sanitize(raw, stems, UNITY);
    expect(s.stems.vocals).toEqual({ position: 0.2, muted: true });
    expect(s.stems.drums).toEqual({ position: 1, muted: false });
    expect(s.stems.bass).toEqual({ position: UNITY, muted: false });
    expect('piano' in s.stems).toBe(false);
    expect(s.solo).toBeNull();
    expect(s.loop).toEqual({ start: 10, end: 20 });
    expect(s.loopEnabled).toBe(true);
    expect(s.selected).toBe('drums');
    expect(s.masterPosition).toBe(0.9);
  });

  it('tolerates garbage', () => {
    expect(sanitize(null, stems, UNITY)).toEqual(initialMix(stems, UNITY));
    expect(sanitize('x', stems, UNITY)).toEqual(initialMix(stems, UNITY));
    const s = sanitize({ loop: { start: 5, end: 5 }, loopEnabled: true } as Partial<MixState>, stems, UNITY);
    expect(s.loop).toBeNull();
    expect(s.loopEnabled).toBe(false);
  });
});
