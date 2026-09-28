import { act, renderHook } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { Job } from '../../api/types';
import type { DrumEditsResponse, DrumEvent } from '../../lib/drums/types';
import type { Mixer } from '../use-mixer';

const putDrumEdits = vi.fn();
const getDrumEdits = vi.fn();
vi.mock('../../api', () => ({
  getDrumEdits: (...a: unknown[]) => getDrumEdits(...a),
  putDrumEdits: (...a: unknown[]) => putDrumEdits(...a),
  drumExportUrl: (id: string) => `/api/v1/jobs/${id}/drums/export`,
}));

import { useDrumEditor } from '../use-drum-editor';

const job = { id: '0f5b8c2e-1d3a-4b7c-9e0f-1a2b3c4d5e6f', stems: [], duration_seconds: 30, detected_bpm: 120 } as unknown as Job;

function fakeMixer(): Mixer {
  return {
    job,
    stems: [],
    mix: { version: 1, stems: {}, solo: null, loop: null, loopEnabled: false, selected: null, masterPosition: 0.84 },
    selected: null,
    solo: null,
    dispatch: () => undefined,
    mixLoaded: true,
    duration: 30,
    bpm: 120,
    live: { current: { position: 0, levels: [], holds: [], master: { left: 0, right: 0, leftHold: 0, rightHold: 0 } } },
    tick: 0,
    playing: false,
    engineState: 'idle',
    engineError: null,
    starting: false,
    peaks: {},
    peaksLoading: false,
    togglePlay: () => undefined,
    play: () => undefined,
    stop: () => undefined,
    seek: () => undefined,
    nudge: () => undefined,
    returnToStart: () => undefined,
    toggleLoop: () => undefined,
    setLoop: () => undefined,
    setSolo: () => undefined,
    toggleSolo: () => undefined,
    toggleMute: () => undefined,
    clearSolo: () => undefined,
    stats: () => null,
    engine: () => null,
  };
}

const events: DrumEvent[] = [
  { id: 'm0', art: 'kick', t: 7.43, vel: 0.73, src: 'model', model: 0 },
  { id: 'm1', art: 'hhc', t: 7.43, vel: 0.694, src: 'model', model: 1 },
  { id: 'm2', art: 'snare', t: 7.93, vel: 0.758, src: 'model', model: 2 },
];

function response(rev = 0): DrumEditsResponse {
  return {
    doc: { version: 1, stem: 'drums', model_rev: { adapter: 'adtof', model: 'adtof_frame_rnn', count: 3, notes_sha256: 'x' }, edit_rev: rev, exported_rev: null, profile: 'gm', updated_at: '2026-09-28T12:00:00Z', events },
    profile: { id: 'gm', name: 'General MIDI', notes: { kick: 36, snare: 38, stick: 37, hhc: 42, hho: 46, hhp: 44, tomh: 48, tomm: 47, tomf: 43, ride: 51, bell: 53, crash: 49 } },
    grid: null,
    grid_error: 'no grid',
    model: { adapter: 'adtof', model: 'adtof_frame_rnn', count: 3, notes_sha256: 'x' },
    stale_model: false,
    duration: 30,
    warnings: [],
  };
}

describe('useDrumEditor', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    getDrumEdits.mockReset();
    putDrumEdits.mockReset();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it('loads the document without writing a revision, and opens the view on the first hit', async () => {
    getDrumEdits.mockResolvedValue(response(2));
    const m = fakeMixer();
    const { result } = renderHook(() => useDrumEditor(m, true));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10);
    });
    expect(result.current.status).toBe('ready');
    expect(result.current.state.hits).toHaveLength(3);
    expect(result.current.autosave.editRev).toBe(2);
    expect(result.current.view.t0).toBeCloseTo(7.43 - 1, 5);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(3000);
    });
    expect(putDrumEdits).not.toHaveBeenCalled();
    expect(result.current.autosave.status).toBe('idle');
  });

  it('saves an edit against the loaded revision and keeps untouched model hits bit for bit', async () => {
    getDrumEdits.mockResolvedValue(response(2));
    putDrumEdits.mockResolvedValue({ edit_rev: 3, updated_at: '2026-09-28T12:01:00Z' });
    const m = fakeMixer();
    const { result } = renderHook(() => useDrumEditor(m, true));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10);
    });
    expect(result.current.status).toBe('ready');
    act(() => result.current.ops.addHit('snare', 7.6875));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2000);
    });
    expect(putDrumEdits).toHaveBeenCalledTimes(1);
    const [id, baseRev, sent] = putDrumEdits.mock.calls[0] as [string, number, DrumEvent[]];
    expect(id).toBe(job.id);
    expect(baseRev).toBe(2);
    expect(sent).toHaveLength(4);
    const ghost = sent.find((e) => e.src === 'manual')!;
    expect(ghost.art).toBe('snare');
    expect(ghost.t).toBe(7.6875);
    for (const e of events) {
      const s = sent.find((x) => x.id === e.id)!;
      expect(Object.is(s.t, e.t)).toBe(true);
      expect(Object.is(s.vel, e.vel)).toBe(true);
    }
    expect(result.current.autosave.editRev).toBe(3);
  });

  it('does not fetch when disabled', async () => {
    const m = fakeMixer();
    const { result } = renderHook(() => useDrumEditor(m, false, false));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10);
    });
    expect(getDrumEdits).not.toHaveBeenCalled();
    expect(result.current.status).toBe('unavailable');
  });
});
