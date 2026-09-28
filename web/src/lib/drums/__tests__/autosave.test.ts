import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { Autosave, describeAutosave, type AutosaveState } from '../autosave';
import { EditConflict, type DrumEvent, type DrumSaveResponse } from '../types';

interface Deferred<T> {
  promise: Promise<T>;
  resolve: (value: T) => void;
  reject: (reason: unknown) => void;
}

function deferred<T>(): Deferred<T> {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

interface Call {
  baseRev: number;
  events: DrumEvent[];
  reply: Deferred<DrumSaveResponse>;
}

/** An Autosave over a save() whose every call is recorded and answered by hand. */
function harness(initialRev = 0) {
  const calls: Call[] = [];
  const states: AutosaveState[] = [];
  const save = vi.fn((baseRev: number, events: DrumEvent[]) => {
    const reply = deferred<DrumSaveResponse>();
    calls.push({ baseRev, events, reply });
    return reply.promise;
  });
  const auto = new Autosave(initialRev, { save, onChange: (s) => states.push(s) });
  return { auto, calls, states, save };
}

const ev = (id: string, t: number): DrumEvent => ({ id, art: 'kick', t, vel: 0.8, src: 'manual' });
const A = [ev('a', 1)];
const AB = [ev('a', 1), ev('b', 2)];
const ABC = [ev('a', 1), ev('b', 2), ev('c', 3)];

const ok = (edit_rev: number): DrumSaveResponse => ({ edit_rev, updated_at: `2026-09-28T12:00:0${edit_rev}Z` });

/** Let the promise chain behind an answered save() run to completion (no timers involved). */
async function settle(): Promise<void> {
  for (let i = 0; i < 8; i++) await Promise.resolve();
}

beforeEach(() => vi.useFakeTimers());
afterEach(() => vi.useRealTimers());

describe('Autosave', () => {
  it('debounces: three updates within 800 ms make one PUT with the last events and base_rev = initialRev', async () => {
    const { auto, calls, states, save } = harness(3);
    auto.update(A);
    vi.advanceTimersByTime(300);
    auto.update(AB);
    vi.advanceTimersByTime(300);
    auto.update(ABC);
    vi.advanceTimersByTime(799);
    expect(save).not.toHaveBeenCalled();
    expect(auto.state).toMatchObject({ status: 'pending', pendingChanges: true, editRev: 3 });
    vi.advanceTimersByTime(1);
    expect(save).toHaveBeenCalledTimes(1);
    expect(calls[0].baseRev).toBe(3);
    expect(calls[0].events).toBe(ABC);
    expect(auto.state).toMatchObject({ status: 'saving', pendingChanges: false });
    calls[0].reply.resolve(ok(4));
    await settle();
    expect(auto.state).toMatchObject({ status: 'saved', editRev: 4, savedAt: ok(4).updated_at, pendingChanges: false, error: null, conflictRev: null });
    // onChange fired once per transition, each time with a fresh object; earlier snapshots are not mutated.
    expect(states.map((s) => s.status)).toEqual(['pending', 'saving', 'saved']);
    expect(new Set(states).size).toBe(3);
    expect(states[2]).toBe(auto.state);
  });

  it('coalesces edits made during an in-flight PUT into exactly one more PUT with the new base_rev', async () => {
    const { auto, calls, save } = harness(0);
    auto.update(A);
    vi.advanceTimersByTime(800);
    expect(save).toHaveBeenCalledTimes(1);
    auto.update(AB);
    expect(auto.state).toMatchObject({ status: 'saving', pendingChanges: true });
    auto.update(ABC);
    vi.advanceTimersByTime(5000);
    expect(save).toHaveBeenCalledTimes(1); // never two concurrent PUTs
    calls[0].reply.resolve(ok(1));
    await settle();
    expect(save).toHaveBeenCalledTimes(2);
    expect(calls[1].baseRev).toBe(1);
    expect(calls[1].events).toBe(ABC);
    expect(auto.state).toMatchObject({ status: 'saving', editRev: 1, pendingChanges: false });
    calls[1].reply.resolve(ok(2));
    await settle();
    expect(auto.state).toMatchObject({ status: 'saved', editRev: 2, savedAt: ok(2).updated_at, pendingChanges: false });
    vi.advanceTimersByTime(5000);
    expect(save).toHaveBeenCalledTimes(2);
  });

  it('stops on a conflict until reset(), then saves with the reloaded revision', async () => {
    const { auto, calls, save } = harness(4);
    auto.update(A);
    vi.advanceTimersByTime(800);
    calls[0].reply.reject(new EditConflict(5));
    await settle();
    expect(auto.state).toMatchObject({ status: 'conflict', conflictRev: 5, editRev: 4, pendingChanges: false, error: null });
    auto.update(AB);
    expect(auto.state).toMatchObject({ status: 'conflict', pendingChanges: false });
    vi.advanceTimersByTime(5000);
    await auto.flush();
    expect(save).toHaveBeenCalledTimes(1);
    auto.reset(5);
    expect(auto.state).toMatchObject({ status: 'idle', editRev: 5, conflictRev: null, pendingChanges: false, savedAt: null });
    auto.update(AB);
    vi.advanceTimersByTime(800);
    expect(save).toHaveBeenCalledTimes(2);
    expect(calls[1].baseRev).toBe(5);
    expect(calls[1].events).toBe(AB);
  });

  it('drops edits queued behind a PUT that conflicts', async () => {
    const { auto, calls, save } = harness(0);
    auto.update(A);
    vi.advanceTimersByTime(800);
    auto.update(AB);
    calls[0].reply.reject(new EditConflict(9));
    await settle();
    vi.advanceTimersByTime(5000);
    expect(save).toHaveBeenCalledTimes(1);
    expect(auto.state).toMatchObject({ status: 'conflict', conflictRev: 9, editRev: 0, pendingChanges: false });
  });

  it('keeps the events after a network error and retries with the same base_rev on the next update or flush', async () => {
    const { auto, calls, save } = harness(2);
    auto.update(A);
    vi.advanceTimersByTime(800);
    calls[0].reply.reject(new Error('network down'));
    await settle();
    expect(auto.state).toMatchObject({ status: 'error', error: 'network down', editRev: 2, pendingChanges: true });
    vi.advanceTimersByTime(5000);
    expect(save).toHaveBeenCalledTimes(1); // no automatic retry loop
    auto.update(A);
    expect(auto.state).toMatchObject({ status: 'pending', error: null, pendingChanges: true });
    vi.advanceTimersByTime(800);
    expect(save).toHaveBeenCalledTimes(2);
    expect(calls[1].baseRev).toBe(2);
    expect(calls[1].events).toBe(A);
    calls[1].reply.reject(new Error('still down'));
    await settle();
    expect(auto.state).toMatchObject({ status: 'error', error: 'still down', pendingChanges: true });
    // flush() retries the held events without waiting for another edit.
    const f = auto.flush();
    expect(save).toHaveBeenCalledTimes(3);
    expect(calls[2].baseRev).toBe(2);
    expect(calls[2].events).toBe(A);
    calls[2].reply.resolve(ok(3));
    await f;
    expect(auto.state).toMatchObject({ status: 'saved', editRev: 3, error: null, pendingChanges: false });
  });

  it('flush() resolves once the in-flight and the queued saves are done', async () => {
    const { auto, calls, save } = harness(0);
    auto.update(A);
    vi.advanceTimersByTime(800);
    auto.update(AB);
    let done = false;
    const f = auto.flush().then(() => {
      done = true;
    });
    await settle();
    expect(done).toBe(false);
    calls[0].reply.resolve(ok(1));
    await settle();
    expect(save).toHaveBeenCalledTimes(2);
    expect(calls[1].baseRev).toBe(1);
    expect(done).toBe(false);
    calls[1].reply.resolve(ok(2));
    await f;
    expect(auto.state).toMatchObject({ status: 'saved', editRev: 2, pendingChanges: false });
  });

  it('flush() cancels the debounce and PUTs at once; with nothing pending it resolves immediately', async () => {
    const { auto, calls, save } = harness(0);
    await auto.flush();
    expect(save).not.toHaveBeenCalled();
    auto.update(A);
    const f = auto.flush();
    expect(save).toHaveBeenCalledTimes(1);
    expect(calls[0].baseRev).toBe(0);
    vi.advanceTimersByTime(5000);
    expect(save).toHaveBeenCalledTimes(1);
    calls[0].reply.resolve(ok(1));
    await f;
    expect(auto.state).toMatchObject({ status: 'saved', editRev: 1 });
    await auto.flush();
    expect(save).toHaveBeenCalledTimes(1);
  });

  it('dispose() cancels the debounce and silences every later callback', async () => {
    const { auto, states, save } = harness(0);
    auto.update(A);
    const seen = states.length;
    auto.dispose();
    vi.advanceTimersByTime(5000);
    expect(save).not.toHaveBeenCalled();
    auto.update(AB);
    vi.advanceTimersByTime(5000);
    expect(save).not.toHaveBeenCalled();
    expect(states.length).toBe(seen);

    // A PUT already in flight finishes but neither reports nor triggers the queued save; a waiting flush() is released.
    const h = harness(0);
    h.auto.update(A);
    vi.advanceTimersByTime(800);
    h.auto.update(AB);
    const f = h.auto.flush();
    const before = h.states.length;
    h.auto.dispose();
    await f;
    h.calls[0].reply.resolve(ok(1));
    await settle();
    expect(h.save).toHaveBeenCalledTimes(1);
    expect(h.states.length).toBe(before);
  });

  it('reset() during an in-flight PUT keeps the reloaded revision when the stale response lands', async () => {
    const { auto, calls, save } = harness(0);
    auto.update(A);
    vi.advanceTimersByTime(800);
    auto.reset(10);
    expect(auto.state).toMatchObject({ status: 'idle', editRev: 10 });
    auto.update(AB); // queued behind the stale PUT — still only one request in flight
    expect(save).toHaveBeenCalledTimes(1);
    calls[0].reply.resolve(ok(1));
    await settle();
    expect(save).toHaveBeenCalledTimes(2);
    expect(calls[1].baseRev).toBe(10);
    expect(calls[1].events).toBe(AB);
    expect(auto.state).toMatchObject({ status: 'saving', editRev: 10 });
  });

  it('uses the injected timer functions', () => {
    const setTimer = vi.fn(() => 'tid');
    const clearTimer = vi.fn();
    const auto = new Autosave(0, { save: vi.fn(), onChange: () => undefined, debounceMs: 50, setTimer, clearTimer });
    auto.update(A);
    expect(setTimer).toHaveBeenCalledWith(expect.any(Function), 50);
    auto.update(AB);
    expect(clearTimer).toHaveBeenCalledWith('tid');
    auto.dispose();
    expect(clearTimer).toHaveBeenCalledTimes(2);
  });
});

describe('describeAutosave', () => {
  const base: AutosaveState = { status: 'idle', editRev: 0, savedAt: null, error: null, conflictRev: null, pendingChanges: false };

  it('renders the inspector line for every status', () => {
    expect(describeAutosave(base)).toBe('Autosave · rev 0');
    expect(describeAutosave({ ...base, status: 'pending', pendingChanges: true })).toBe('Unsaved changes');
    expect(describeAutosave({ ...base, status: 'saving' })).toBe('Saving…');
    expect(describeAutosave({ ...base, status: 'saved', editRev: 7, savedAt: '2026-09-28T12:00:07Z' })).toBe('Autosaved · rev 7');
    expect(describeAutosave({ ...base, status: 'conflict', conflictRev: 8 })).toBe('Changed in another tab — reload');
    expect(describeAutosave({ ...base, status: 'error', error: 'network down', pendingChanges: true })).toBe('Save failed: network down');
  });
});

describe('Autosave robustness', () => {
  it('survives a throwing onChange listener: the chain settles and later edits still save', async () => {
    const calls: number[] = [];
    let resolveSave!: (r: DrumSaveResponse) => void;
    const save = vi.fn((baseRev: number) => {
      calls.push(baseRev);
      return new Promise<DrumSaveResponse>((res) => {
        resolveSave = res;
      });
    });
    let threw = false;
    const as = new Autosave(1, {
      save,
      onChange: (s) => {
        if (s.status === 'saved' && !threw) {
          threw = true;
          throw new Error('render blew up');
        }
      },
    });
    const unhandled = vi.fn();
    process.on('uncaughtException', unhandled);
    as.update(A);
    await vi.advanceTimersByTimeAsync(800);
    expect(calls).toEqual([1]);
    resolveSave({ edit_rev: 2, updated_at: 'now' });
    await vi.advanceTimersByTimeAsync(10);
    process.off('uncaughtException', unhandled);
    as.update(AB);
    await vi.advanceTimersByTimeAsync(800);
    expect(calls).toEqual([1, 2]);
    resolveSave({ edit_rev: 3, updated_at: 'now' });
    await as.flush();
    expect(as.state.editRev).toBe(3);
  });

  it('an update made while a failure is being reported is scheduled, not stranded', async () => {
    let rejectSave!: (e: unknown) => void;
    let resolveSave!: (r: DrumSaveResponse) => void;
    const save = vi.fn(
      (_baseRev: number) =>
        new Promise<DrumSaveResponse>((res, rej) => {
          resolveSave = res;
          rejectSave = rej;
        }),
    );
    let reentered = false;
    const as = new Autosave(1, {
      save,
      onChange: (s) => {
        if (s.status === 'error' && !reentered) {
          reentered = true;
          as.update(AB);
        }
      },
    });
    as.update(A);
    await vi.advanceTimersByTimeAsync(800);
    expect(save).toHaveBeenCalledTimes(1);
    rejectSave(new Error('boom'));
    await vi.advanceTimersByTimeAsync(10);
    expect(as.state.pendingChanges).toBe(true);
    await vi.advanceTimersByTimeAsync(800);
    expect(save).toHaveBeenCalledTimes(2);
    expect(save.mock.calls[1][0]).toBe(1);
    resolveSave({ edit_rev: 2, updated_at: 'now' });
    await as.flush();
    expect(as.state.status).toBe('saved');
  });
});
