import { useCallback, useEffect, useMemo, useReducer, useRef, useState } from 'react';
import * as api from '../api';
import { ApiError, errorMessage } from '../api/client';
import { Autosave, type AutosaveState } from '../lib/drums/autosave';
import { DIVISIONS, makeGrid, type BeatGrid, type GridDivision } from '../lib/drums/grid';
import { editorReducer, fromDoc, initialEditor, primary, selectedHits, toEvents, type EditorAction, type EditorState } from '../lib/drums/model';
import { layoutRows, type RowLayout } from '../lib/drums/rows';
import { ARTICULATIONS, GROUP, GROUPS, velFrom127, type Articulation, type GroupKey } from '../lib/drums/taxonomy';
import { EditConflict, type DrumEditsResponse } from '../lib/drums/types';
import type { Mixer } from './use-mixer';

export type EditorTool = 'select' | 'draw' | 'erase';
export type AuditionMode = 'original' | 'midi';

export interface EditorView {
  /** Left edge of the visible range in seconds. */
  t0: number;
  /** Visible length in seconds. */
  span: number;
}

export const MIN_SPAN = 0.5;
/** Shift+arrow nudge in seconds. */
export const FINE_NUDGE = 0.005;
/** Nudge without a grid. */
export const COARSE_NUDGE = 0.05;

export interface DrumEditorHandle {
  status: 'loading' | 'ready' | 'unavailable' | 'error';
  error: string | null;
  data: DrumEditsResponse | null;
  grid: BeatGrid;
  state: EditorState;
  dispatch: (a: EditorAction) => void;
  layout: RowLayout;
  view: EditorView;
  setView(t0: number, span: number): void;
  zoomAt(fx: number, factor: number): void;
  zoomIn(): void;
  zoomOut(): void;
  fitLoop(): void;
  tool: EditorTool;
  setTool(t: EditorTool): void;
  snap: boolean;
  setSnap(on: boolean): void;
  division: GridDivision;
  setDivision(d: GridDivision): void;
  qStrength: number;
  setQStrength(v: number): void;
  focus: GroupKey;
  setFocus(g: GroupKey): void;
  open: Partial<Record<GroupKey, boolean>>;
  toggleOpen(g: GroupKey): void;
  rowMute: Record<string, boolean>;
  rowSolo: string | null;
  toggleRowMute(key: string): void;
  toggleRowSolo(key: string): void;
  /** Whether hits of an articulation are muted by the MIDI row S/M state. */
  isRowMuted(art: Articulation): boolean;
  mode: AuditionMode;
  setMode(m: AuditionMode): void;
  autosave: AutosaveState;
  /** Reload the document from the server (after a conflict). */
  reload(): void;
  /** Flush the autosave and download the export of the current revision. */
  exportNow(): Promise<void>;
  exporting: boolean;
  /** Snap helper honouring the SNAP toggle (force = ignore the toggle). */
  snapTime(t: number, force?: boolean): number;
  /** Seconds one grid step (or COARSE_NUDGE without a grid) away from t. */
  gridStep(t: number, dir: 1 | -1): number;
  /** Toolbar operations on the selection. */
  ops: {
    undo(): void;
    redo(): void;
    deleteSel(): void;
    copy(): void;
    paste(): void;
    duplicate(): void;
    quantize(): void;
    nudge(dir: 1 | -1, fine: boolean): void;
    velAdd(delta: number): void;
    compress(): void;
    selectAllInView(): void;
    setSelArt(a: Articulation): void;
    setSelVel(v127: number): void;
    setSelTime(t: number): void;
    addHit(art: Articulation, t: number): void;
  };
  /** Editor-context keyboard handler; returns true when the key was consumed. */
  handleKey(e: KeyboardEvent): boolean;
}

const DEFAULT_SPAN = 8;

/**
 * State of the DRUM EDITOR tab: the edit document from the server (seeded
 * from the model output), the pure edit model with undo/redo, the beat
 * grid, the viewport and tool state, and the autosave that PUTs every
 * change back. Transport, loop and position come from the shared Mixer.
 */
export function useDrumEditor(m: Mixer, active: boolean, enabled = true): DrumEditorHandle {
  const jobId = m.job.id;
  const duration = m.duration;
  const [data, setData] = useState<DrumEditsResponse | null>(null);
  const [status, setStatus] = useState<DrumEditorHandle['status']>('loading');
  const [error, setError] = useState<string | null>(null);
  const [state, dispatch] = useReducer(editorReducer, duration, (d) => initialEditor(d));
  const [view, setViewState] = useState<EditorView>({ t0: 0, span: Math.min(DEFAULT_SPAN, Math.max(MIN_SPAN, duration || DEFAULT_SPAN)) });
  const [tool, setTool] = useState<EditorTool>('select');
  const [snap, setSnap] = useState(false);
  const [division, setDivision] = useState<GridDivision>('1/16');
  const [qStrength, setQStrength] = useState(100);
  const [focus, setFocus] = useState<GroupKey>('snare');
  const [open, setOpen] = useState<Partial<Record<GroupKey, boolean>>>({});
  const [rowMute, setRowMute] = useState<Record<string, boolean>>({});
  const [rowSolo, setRowSolo] = useState<string | null>(null);
  const [mode, setMode] = useState<AuditionMode>('original');
  const [autosave, setAutosaveState] = useState<AutosaveState>({ status: 'idle', editRev: 0, savedAt: null, error: null, conflictRev: null, pendingChanges: false });
  const [exporting, setExporting] = useState(false);
  const [loadSeq, setLoadSeq] = useState(0);
  const autosaveRef = useRef<Autosave | null>(null);
  const stateRef = useRef(state);
  stateRef.current = state;
  const viewRef = useRef(view);
  viewRef.current = view;

  const grid = useMemo(() => makeGrid(data?.grid ?? null), [data?.grid]);
  const layout = useMemo(() => layoutRows(open), [open]);

  // ---- load the document ---------------------------------------------------
  useEffect(() => {
    let cancelled = false;
    if (!enabled) {
      setStatus('unavailable');
      return;
    }
    setStatus('loading');
    setError(null);
    (async () => {
      try {
        const res = await api.getDrumEdits(jobId);
        if (cancelled) return;
        setData(res);
        const loaded = fromDoc(res.doc, res.duration || duration);
        dispatch({ type: 'load', hits: loaded.hits });
        // Open on the first hit (an intro without drums would show an empty view).
        const first = loaded.hits[0];
        if (first) {
          const span = viewRef.current.span;
          setViewState((v) => (v.t0 > 0 ? v : { t0: Math.max(0, first.t - Math.min(1, span / 4)), span }));
        }
        autosaveRef.current?.dispose();
        const as = new Autosave(res.doc.edit_rev, {
          save: (baseRev, events) => api.putDrumEdits(jobId, baseRev, events),
          onChange: (s) => setAutosaveState(s),
        });
        autosaveRef.current = as;
        setAutosaveState({ ...as.state });
        setStatus('ready');
      } catch (err) {
        if (cancelled) return;
        if (err instanceof ApiError && (err.status === 404 || err.status === 409)) setStatus('unavailable');
        else {
          setStatus('error');
          setError(errorMessage(err, 'Could not load the drum edits.'));
        }
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [jobId, duration, loadSeq, enabled]);

  useEffect(() => () => autosaveRef.current?.dispose(), []);

  // Every change of the hits after the load is pushed to the autosave
  // (debounced there). The load itself is not a change: opening the page
  // must not write a revision.
  const baseline = useRef<number | null>(null);
  useEffect(() => {
    if (status !== 'ready') {
      baseline.current = null;
      return;
    }
    if (baseline.current === null) {
      baseline.current = state.changes;
      return;
    }
    if (state.changes === baseline.current || state.inGesture) return;
    baseline.current = state.changes;
    autosaveRef.current?.update(toEvents(state));
  }, [state, status]);

  // ---- view -------------------------------------------------------------------
  const setView = useCallback(
    (t0: number, span: number) => {
      const max = Math.max(MIN_SPAN, duration || DEFAULT_SPAN);
      const s = Math.max(MIN_SPAN, Math.min(max, span));
      const start = Math.max(0, Math.min(t0, max - s));
      setViewState((v) => (v.t0 === start && v.span === s ? v : { t0: start, span: s }));
    },
    [duration],
  );
  const zoomAt = useCallback(
    (fx: number, factor: number) => {
      const v = viewRef.current;
      const span = v.span * factor;
      setView(v.t0 + fx * v.span - fx * Math.max(MIN_SPAN, Math.min(duration || DEFAULT_SPAN, span)), span);
    },
    [setView, duration],
  );
  const zoomIn = useCallback(() => zoomAt(0.5, 0.6), [zoomAt]);
  const zoomOut = useCallback(() => zoomAt(0.5, 1.6), [zoomAt]);
  const fitLoop = useCallback(() => {
    const loop = m.mix.loop;
    if (!loop) return;
    setView(loop.start - 0.1, loop.end - loop.start + 0.2);
  }, [m.mix.loop, setView]);

  // Follow the playhead while the tab is active and audio plays.
  useEffect(() => {
    if (!active || !m.playing) return;
    const pos = m.live.current.position;
    const v = viewRef.current;
    if (pos >= v.t0 && pos < v.t0 + v.span) return;
    setView(pos - v.span * 0.02, v.span);
  }, [active, m.playing, m.tick, m.live, setView]);

  // ---- rows -------------------------------------------------------------------
  const toggleOpen = useCallback((g: GroupKey) => setOpen((o) => ({ ...o, [g]: !o[g] })), []);
  const toggleRowMute = useCallback((key: string) => setRowMute((r) => ({ ...r, [key]: !r[key] })), []);
  const toggleRowSolo = useCallback((key: string) => setRowSolo((s) => (s === key ? null : key)), []);
  const isRowMuted = useCallback(
    (art: Articulation) => {
      const g = ARTICULATIONS[art].group;
      if (rowMute[art] || rowMute[g]) return true;
      return rowSolo !== null && rowSolo !== art && rowSolo !== g;
    },
    [rowMute, rowSolo],
  );

  // ---- helpers ----------------------------------------------------------------
  const snapTime = useCallback(
    (t: number, force?: boolean) => {
      if (!(snap || force) || !grid.hasGrid) return t;
      return grid.snap(t, division);
    },
    [snap, grid, division],
  );
  const gridStep = useCallback(
    (t: number, dir: 1 | -1) => (grid.hasGrid ? grid.step(t, division, dir) : t + dir * COARSE_NUDGE),
    [grid, division],
  );
  const position = useCallback(() => m.live.current.position, [m.live]);

  // A grid step depends on where the selection sits (variable tempo): take
  // it at the primary hit.
  const nudgeSel = useCallback(
    (dir: 1 | -1, fine: boolean) => {
      if (fine) {
        dispatch({ type: 'nudge', dt: dir * FINE_NUDGE });
        return;
      }
      const p = primary(stateRef.current);
      if (!p) return;
      dispatch({ type: 'nudge', dt: gridStep(p.t, dir) - p.t });
    },
    [gridStep],
  );

  // ---- operations -------------------------------------------------------------
  const ops = useMemo<DrumEditorHandle['ops']>(
    () => ({
      undo: () => dispatch({ type: 'undo' }),
      redo: () => dispatch({ type: 'redo' }),
      deleteSel: () => dispatch({ type: 'delete' }),
      copy: () => dispatch({ type: 'copy' }),
      paste: () => dispatch({ type: 'paste', at: snapTime(position(), true) }),
      duplicate: () => {
        const sel = selectedHits(stateRef.current);
        if (sel.length === 0) return;
        const ts = sel.map((h) => h.t);
        const min = Math.min(...ts);
        const max = Math.max(...ts);
        // One bar at the selection start (or 2 s without a grid), rounded up to the selection's length.
        let bar = 2;
        if (grid.hasGrid) {
          const b = grid.beat(min);
          const info = grid.barAt(b);
          bar = grid.seconds(info.startBeat + info.numerator) - grid.seconds(info.startBeat);
        }
        const offset = Math.max(bar, Math.ceil((max - min + 0.001) / bar) * bar);
        dispatch({ type: 'duplicate', offset });
      },
      quantize: () => {
        if (!grid.hasGrid) return;
        dispatch({ type: 'quantize', snap: (t) => grid.snap(t, division), strength: qStrength / 100 });
      },
      nudge: nudgeSel,
      velAdd: (delta) => dispatch({ type: 'velAdd', delta }),
      compress: () => dispatch({ type: 'compress' }),
      selectAllInView: () => {
        const v = viewRef.current;
        dispatch({ type: 'selectRange', t0: v.t0, t1: v.t0 + v.span, arts: GROUP[focus].articulations });
      },
      setSelArt: (a) => {
        dispatch({ type: 'setArt', art: a });
        setFocus(ARTICULATIONS[a].group);
      },
      setSelVel: (v127) => dispatch({ type: 'setVel', vel: velFrom127(v127) }),
      setSelTime: (t) => dispatch({ type: 'setTime', t }),
      addHit: (art, t) => {
        dispatch({ type: 'add', art, t });
        setFocus(ARTICULATIONS[art].group);
      },
    }),
    [snapTime, position, grid, division, qStrength, focus, nudgeSel],
  );

  // ---- reload / export ---------------------------------------------------------
  const reload = useCallback(() => setLoadSeq((n) => n + 1), []);

  const exportNow = useCallback(async () => {
    const as = autosaveRef.current;
    if (!as || status !== 'ready') return;
    setExporting(true);
    try {
      await as.flush();
      if (as.state.status === 'conflict') throw new EditConflict(as.state.conflictRev ?? as.state.editRev);
      if (as.state.status === 'error') throw new Error(as.state.error ?? 'save failed');
      submitExport(jobId, as.state.editRev);
    } finally {
      setExporting(false);
    }
  }, [jobId, status]);

  // ---- keyboard -----------------------------------------------------------------
  const handleKey = useCallback(
    (e: KeyboardEvent): boolean => {
      const mod = e.metaKey || e.ctrlKey;
      const k = e.key.toLowerCase();
      if (mod && k === 'z') {
        if (e.shiftKey) ops.redo();
        else ops.undo();
        return true;
      }
      if (mod && k === 'y') {
        ops.redo();
        return true;
      }
      if (mod && k === 'c') {
        ops.copy();
        return true;
      }
      if (mod && k === 'v') {
        ops.paste();
        return true;
      }
      if (mod && k === 'd') {
        ops.duplicate();
        return true;
      }
      if (mod && k === 'a') {
        ops.selectAllInView();
        return true;
      }
      if (mod) return false;
      switch (k) {
        case 'delete':
        case 'backspace':
          ops.deleteSel();
          return true;
        case 'q':
          ops.quantize();
          return true;
        case 's':
          setSnap((s) => !s);
          return true;
        case '1':
          setTool('select');
          return true;
        case '2':
          setTool('draw');
          return true;
        case '3':
          setTool('erase');
          return true;
        case 'arrowleft':
          nudgeSel(-1, e.shiftKey);
          return true;
        case 'arrowright':
          nudgeSel(1, e.shiftKey);
          return true;
        default:
          return false;
      }
    },
    [ops, nudgeSel],
  );

  const handle: DrumEditorHandle = {
    status,
    error,
    data,
    grid,
    state,
    dispatch,
    layout,
    view,
    setView,
    zoomAt,
    zoomIn,
    zoomOut,
    fitLoop,
    tool,
    setTool,
    snap,
    setSnap,
    division,
    setDivision,
    qStrength,
    setQStrength,
    focus,
    setFocus,
    open,
    toggleOpen,
    rowMute,
    rowSolo,
    toggleRowMute,
    toggleRowSolo,
    isRowMuted,
    mode,
    setMode,
    autosave,
    reload,
    exportNow,
    exporting,
    snapTime,
    gridStep,
    ops,
    handleKey,
  };
  // Debug hook for scripts (web/scripts/shot.mjs) and the console.
  useEffect(() => {
    if (!enabled) return;
    const dbg = {
      handle,
      selectFirstInView() {
        const v = viewRef.current;
        const h = stateRef.current.hits.find((x) => x.t >= v.t0 && x.t <= v.t0 + v.span);
        if (h) dispatch({ type: 'select', ids: [h.id], mode: 'replace' });
      },
    };
    (window as unknown as { __rkDrums?: typeof dbg }).__rkDrums = dbg;
    return () => {
      delete (window as unknown as { __rkDrums?: typeof dbg }).__rkDrums;
    };
  });
  return handle;
}

/** Bar length label helper for the toolbar ("bars 12–14"). */
export function viewLabel(view: EditorView, grid: BeatGrid): string {
  const a = grid.formatPosition(view.t0).split('.')[0];
  const b = grid.formatPosition(view.t0 + view.span).split('.')[0];
  return grid.hasGrid ? `bars ${a}–${b}` : '';
}

export { DIVISIONS, GROUPS };

/**
 * Submit the export as a form POST into a hidden iframe: the browser
 * downloads the zip natively (no blob in memory for a 200 MB archive) and
 * a JSON error, if any, lands in the iframe where we can read it.
 */
function submitExport(jobId: string, editRev: number): void {
  const name = 'rk-export-frame';
  let frame = document.querySelector<HTMLIFrameElement>(`iframe[name="${name}"]`);
  if (!frame) {
    frame = document.createElement('iframe');
    frame.name = name;
    frame.hidden = true;
    frame.setAttribute('aria-hidden', 'true');
    document.body.appendChild(frame);
  }
  const form = document.createElement('form');
  form.method = 'post';
  form.action = api.drumExportUrl(jobId);
  form.target = name;
  form.hidden = true;
  const input = document.createElement('input');
  input.type = 'hidden';
  input.name = 'edit_rev';
  input.value = String(editRev);
  form.appendChild(input);
  document.body.appendChild(form);
  form.submit();
  form.remove();
}
