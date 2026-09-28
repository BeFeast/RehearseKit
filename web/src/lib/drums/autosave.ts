/**
 * Debounced, revision-chained autosave for the drum editor. Framework-free:
 * the hook feeds it events with update() and mirrors `state` into React.
 *
 * Server contract (PUT /jobs/{id}/drums/edits): {base_rev, events} is accepted
 * only when base_rev equals the current edit_rev and answers {edit_rev,
 * updated_at}; a stale base_rev is a 409 that the API layer throws as
 * EditConflict. The revision line must never fork, so:
 *
 *   - at most one PUT is in flight — edits made meanwhile are coalesced and
 *     sent right after it, with the edit_rev the response returned;
 *   - after a conflict the autosave stops (update() is ignored) until the
 *     caller reloads the document and calls reset(rev);
 *   - any other failure keeps the events pending and the next update() or
 *     flush() retries with the same base_rev — there is no retry loop.
 */
import { EditConflict, type DrumEvent, type DrumSaveResponse } from './types';

export type AutosaveStatus = 'idle' | 'pending' | 'saving' | 'saved' | 'conflict' | 'error';

export interface AutosaveState {
  status: AutosaveStatus;
  /** Revision the next PUT sends as base_rev. */
  editRev: number;
  /** updated_at of the last successful save (ISO string), null until one lands or after reset(). */
  savedAt: string | null;
  /** Message of the failed save while status is 'error'. */
  error: string | null;
  /** The server's edit_rev from the 409 while status is 'conflict'. */
  conflictRev: number | null;
  /** Events not yet on the server: debouncing, queued behind an in-flight PUT, or held after an error. */
  pendingChanges: boolean;
}

export interface AutosaveOptions {
  save(baseRev: number, events: DrumEvent[]): Promise<DrumSaveResponse>;
  /** Called on every state transition with a fresh object (never after dispose()). */
  onChange(state: AutosaveState): void;
  /** Quiet time after the last update() before the PUT fires. Default 800. */
  debounceMs?: number;
  /** Injectable timers for tests; default globalThis.setTimeout / clearTimeout. */
  setTimer?: (fn: () => void, ms: number) => unknown;
  clearTimer?: (id: unknown) => void;
}

const DEFAULT_DEBOUNCE_MS = 800;

export class Autosave {
  private current: AutosaveState;
  /** Latest events not yet handed to save(). */
  private pending: DrumEvent[] | null = null;
  private timer: unknown = null;
  private inFlight: Promise<void> | null = null;
  private queuedWhileInFlight = false;
  private waiters: Array<() => void> = [];
  private disposed = false;
  /** Bumped by reset() and dispose(): a PUT started under an older generation may not touch the state. */
  private generation = 0;
  private readonly debounceMs: number;
  private readonly setTimer: (fn: () => void, ms: number) => unknown;
  private readonly clearTimer: (id: unknown) => void;

  constructor(initialRev: number, private readonly opts: AutosaveOptions) {
    this.current = { status: 'idle', editRev: initialRev, savedAt: null, error: null, conflictRev: null, pendingChanges: false };
    this.debounceMs = opts.debounceMs ?? DEFAULT_DEBOUNCE_MS;
    this.setTimer = opts.setTimer ?? ((fn, ms) => globalThis.setTimeout(fn, ms));
    this.clearTimer = opts.clearTimer ?? ((id) => globalThis.clearTimeout(id as ReturnType<typeof globalThis.setTimeout>));
  }

  /** Current state; the same object until the next transition. */
  get state(): AutosaveState {
    return this.current;
  }

  /** Schedule a (debounced) save of these events. Ignored after a conflict until reset(). */
  update(events: DrumEvent[]): void {
    if (this.disposed || this.current.status === 'conflict') return;
    this.pending = events;
    this.cancelTimer();
    if (this.inFlight !== null) {
      // Coalesce: the in-flight PUT's completion sends these with the fresh edit_rev.
      this.queuedWhileInFlight = true;
      this.setState({ status: this.current.status === 'saving' ? 'saving' : 'pending', pendingChanges: true, error: null });
      return;
    }
    this.setState({ status: 'pending', pendingChanges: true, error: null });
    this.timer = this.setTimer(() => {
      this.timer = null;
      this.startSave();
    }, this.debounceMs);
  }

  /** Cancel the debounce and save now if anything is pending. Resolves once nothing is pending or a conflict/error stopped the chain; never rejects. */
  flush(): Promise<void> {
    this.cancelTimer();
    if (!this.disposed && this.inFlight === null && this.pending !== null) this.startSave();
    if (this.inFlight === null) return Promise.resolve();
    return new Promise((resolve) => this.waiters.push(resolve));
  }

  /** After the caller reloaded the document: drop pending edits and the conflict, continue from `rev`. */
  reset(rev: number): void {
    if (this.disposed) return;
    this.cancelTimer();
    this.pending = null;
    this.generation++;
    this.setState({ status: 'idle', editRev: rev, savedAt: null, error: null, conflictRev: null, pendingChanges: false });
  }

  /** Cancel timers and silence callbacks; a PUT already in flight is left to finish but ignored. */
  dispose(): void {
    if (this.disposed) return;
    this.disposed = true;
    this.cancelTimer();
    this.pending = null;
    this.generation++;
    this.resolveWaiters();
  }

  private startSave(): void {
    if (this.disposed || this.inFlight !== null || this.pending === null) return;
    const events = this.pending;
    this.pending = null;
    const baseRev = this.current.editRev;
    const gen = this.generation;
    this.setState({ status: 'saving', pendingChanges: false, error: null });
    let request: Promise<DrumSaveResponse>;
    try {
      request = this.opts.save(baseRev, events);
    } catch (err) {
      request = Promise.reject(err);
    }
    this.inFlight = request
      .then(
        (res) => this.onSaved(res, gen),
        (err: unknown) => this.onFailed(err, events, gen),
      )
      .then(
        (proceed) => this.settle(proceed),
        // A throwing onChange listener must not leave a phantom request in flight.
        () => this.settle(false),
      );
  }

  private settle(proceed: boolean): void {
    this.inFlight = null;
    const queued = this.queuedWhileInFlight;
    this.queuedWhileInFlight = false;
    if (!this.disposed && this.pending !== null) {
      if (proceed) this.startSave();
      else if (queued && this.current.status !== 'conflict' && this.timer === null) {
        // update() arrived while the failure was being reported (held failed
        // events wait for the next update or flush, as documented).
        this.timer = this.setTimer(() => {
          this.timer = null;
          this.startSave();
        }, this.debounceMs);
      }
    }
    if (this.inFlight === null) this.resolveWaiters();
  }

  /** Returns whether queued events may be sent next. */
  private onSaved(res: DrumSaveResponse, gen: number): boolean {
    if (gen !== this.generation) return true;
    const queued = this.pending !== null;
    this.setState({ status: queued ? 'pending' : 'saved', editRev: res.edit_rev, savedAt: res.updated_at, pendingChanges: queued });
    return true;
  }

  private onFailed(err: unknown, events: DrumEvent[], gen: number): boolean {
    if (gen !== this.generation) return true;
    if (err instanceof EditConflict) {
      // Another tab (or a reload) moved the line on; anything queued here would fork it.
      this.pending = null;
      this.setState({ status: 'conflict', conflictRev: err.editRev, error: null, pendingChanges: false });
      return false;
    }
    // Hold the failed events unless newer ones arrived meanwhile; the next update() or flush() retries with the same base_rev.
    if (this.pending === null) this.pending = events;
    this.setState({ status: 'error', error: errorMessage(err), pendingChanges: true });
    return false;
  }

  private setState(patch: Partial<AutosaveState>): void {
    if (this.disposed) return;
    const keys = Object.keys(patch) as (keyof AutosaveState)[];
    if (keys.every((k) => patch[k] === this.current[k])) return;
    this.current = { ...this.current, ...patch };
    try {
      this.opts.onChange(this.current);
    } catch (err) {
      // A listener's failure (a render error) is not the autosave's; keep the chain alive.
      queueMicrotask(() => {
        throw err;
      });
    }
  }

  private cancelTimer(): void {
    if (this.timer === null) return;
    this.clearTimer(this.timer);
    this.timer = null;
  }

  private resolveWaiters(): void {
    const waiters = this.waiters;
    this.waiters = [];
    for (const resolve of waiters) resolve();
  }
}

function errorMessage(err: unknown): string {
  if (err instanceof Error) return err.message || err.name;
  return String(err);
}

/** The inspector's one-line autosave status. */
export function describeAutosave(s: AutosaveState): string {
  switch (s.status) {
    case 'conflict':
      return 'Changed in another tab — reload';
    case 'error':
      return `Save failed: ${s.error ?? 'unknown error'}`;
    case 'saving':
      return 'Saving…';
    case 'pending':
      return 'Unsaved changes';
    case 'saved':
      return `Autosaved · rev ${s.editRev}`;
    case 'idle':
      return s.savedAt !== null ? `Autosaved · rev ${s.editRev}` : `Autosave · rev ${s.editRev}`;
  }
}
