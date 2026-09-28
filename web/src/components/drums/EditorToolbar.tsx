import { formatBpm, formatTimecode } from '../../lib/format';
import { DIVISIONS } from '../../lib/drums/grid';
import { primary } from '../../lib/drums/model';
import type { Mixer } from '../../player/use-mixer';
import { viewLabel, type DrumEditorHandle, type EditorTool } from '../../player/use-drum-editor';
import { errorMessage } from '../../api/client';
import { EditConflict } from '../../lib/drums/types';
import { Icon } from '../Icon';
import { useToast } from '../Toast';

const TOOLS: { id: EditorTool; label: string; title: string }[] = [
  { id: 'select', label: 'SELECT', title: 'Select / move (1)' },
  { id: 'draw', label: 'DRAW', title: 'Draw hits (2)' },
  { id: 'erase', label: 'ERASE', title: 'Erase hits (3)' },
];

/** Transport row + tool row of the drum editor (design: DRUM EDITOR tab, top two bars). */
export function EditorToolbar({ ed, m }: { ed: DrumEditorHandle; m: Mixer }) {
  const { toast } = useToast();
  const pos = m.live.current.position;
  const sel = ed.state.selection.length;
  const hasSel = sel > 0;
  const canQuantize = hasSel && ed.grid.hasGrid;
  const bpmNow = ed.grid.hasGrid ? ed.grid.bpmAt(pos) : m.bpm;
  const p = primary(ed.state);
  void p;
  return (
    <>
      <div className="rk-drums-bar" data-testid="drums-transport">
        <div style={{ display: 'flex', alignItems: 'center', gap: 'var(--rk-space-3)' }}>
          <button className="rk-tbtn" type="button" aria-label={m.mix.loopEnabled && m.mix.loop ? 'Return to loop start' : 'Return to start'} onClick={m.returnToStart} style={{ width: 32, height: 32 }}>
            <Icon name="skip-start" size={16} />
          </button>
          <button className="rk-tbtn rk-tbtn--play" type="button" aria-label={m.playing ? 'Pause' : 'Play'} aria-pressed={m.playing} aria-busy={m.starting || undefined} onClick={m.togglePlay} style={{ width: 58, height: 32 }}>
            {m.playing ? (
              <span style={{ display: 'flex', gap: 4 }}>
                <i style={{ display: 'block', width: 4, height: 13, background: 'currentColor' }} />
                <i style={{ display: 'block', width: 4, height: 13, background: 'currentColor' }} />
              </span>
            ) : (
              <span style={{ display: 'block', width: 0, height: 0, borderLeft: '11px solid currentColor', borderTop: '7px solid transparent', borderBottom: '7px solid transparent' }} />
            )}
          </button>
          <button className="rk-tbtn rk-tbtn--toggle" type="button" aria-pressed={m.mix.loopEnabled} onClick={m.toggleLoop} style={{ height: 32 }}>
            LOOP
          </button>
        </div>
        <div className="rk-drums-lcd" aria-live="off">
          <span className="rk-time" data-testid="drums-lcd-time">
            {formatTimecode(pos)}
          </span>
          <span className="rk-pos" data-testid="drums-lcd-pos">
            {ed.grid.formatPosition(pos)}
          </span>
          <hr />
          <span style={{ fontSize: 'var(--rk-font-size-md)', fontWeight: 'var(--rk-font-weight-bold)' }}>{formatBpm(bpmNow)}</span>
          <span className="rk-dim">BPM{ed.grid.hasGrid ? ` · ${ed.grid.barAt(ed.grid.beat(pos)).numerator}/4` : ''}</span>
        </div>
        <div style={{ flex: 1 }} />
        <div style={{ display: 'flex', alignItems: 'center', gap: 'var(--rk-space-3)' }}>
          <span className="rk-eyebrow" style={{ fontSize: 'var(--rk-font-size-xs)' }}>
            AUDITION
          </span>
          <div className="rk-seg" role="radiogroup" aria-label="Audition source">
            <button type="button" role="radio" aria-checked={ed.mode === 'original'} onClick={() => ed.setMode('original')}>
              ORIGINAL
            </button>
            <button type="button" role="radio" aria-checked={ed.mode === 'midi'} onClick={() => ed.setMode('midi')} disabled title="The sample kit lands in the next update">
              MIDI KIT
            </button>
          </div>
        </div>
        <button
          className="rk-btn rk-btn--primary"
          type="button"
          onClick={() => {
            ed.exportNow()
              .then((rev) => rev !== null && toast({ kind: 'info', title: 'Export started', detail: `drums.mid + drums.dawproject from rev ${rev}.` }))
              .catch((err: unknown) =>
                toast({
                  kind: 'error',
                  title: err instanceof EditConflict ? 'The hits changed in another tab' : 'Export failed',
                  detail: err instanceof EditConflict ? `Reload to export the latest revision (rev ${err.editRev}).` : errorMessage(err),
                }),
              );
          }}
          disabled={ed.status !== 'ready' || ed.exporting || ed.autosave.status === 'conflict' || ed.autosave.status === 'error'}
          title={ed.autosave.status === 'error' ? 'The last save failed; edit again to retry before exporting' : undefined}
          data-testid="drums-export"
        >
          {ed.exporting ? 'Exporting…' : 'Export MIDI + DAW'}
        </button>
      </div>

      <div className="rk-drums-tools" data-testid="drums-tools">
        <div className="rk-seg" role="radiogroup" aria-label="Tool">
          {TOOLS.map((t) => (
            <button key={t.id} type="button" role="radio" aria-checked={ed.tool === t.id} title={t.title} onClick={() => ed.setTool(t.id)}>
              {t.label}
            </button>
          ))}
        </div>
        <div style={{ display: 'flex', gap: 'var(--rk-space-2)' }}>
          <button className="rk-tbtn rk-tbtn--xs" type="button" title="Undo (Ctrl+Z)" onClick={ed.ops.undo} disabled={ed.state.past.length === 0}>
            UNDO
          </button>
          <button className="rk-tbtn rk-tbtn--xs" type="button" title="Redo (Ctrl+Shift+Z)" onClick={ed.ops.redo} disabled={ed.state.future.length === 0}>
            REDO
          </button>
        </div>
        <span className="rk-drums-sep" />
        <button className="rk-tbtn rk-tbtn--xs" type="button" aria-pressed={ed.snap} title="Snap to grid (S) · hold Alt while dragging to bypass" onClick={() => ed.setSnap(!ed.snap)} disabled={!ed.grid.hasGrid}>
          SNAP
        </button>
        <div className="rk-seg" role="radiogroup" aria-label="Grid">
          {DIVISIONS.map((d) => (
            <button key={d} type="button" role="radio" aria-checked={ed.division === d} onClick={() => ed.setDivision(d)} disabled={!ed.grid.hasGrid}>
              {d}
            </button>
          ))}
        </div>
        <span className="rk-drums-sep" />
        <button className="rk-tbtn rk-tbtn--xs" type="button" title="Quantize selection (Q)" onClick={ed.ops.quantize} disabled={!canQuantize}>
          QUANTIZE
        </button>
        <label style={{ display: 'flex', alignItems: 'center', gap: 'var(--rk-space-3)' }}>
          <input className="rk-drums-range" type="range" min={0} max={100} step={5} value={ed.qStrength} onChange={(e) => ed.setQStrength(Number(e.target.value))} aria-label="Quantize strength" />
          <span className="rk-drums-readout" style={{ color: 'var(--rk-color-ink)', minWidth: 32 }}>
            {ed.qStrength}%
          </span>
        </label>
        <span className="rk-drums-sep" />
        <div style={{ display: 'flex', gap: 'var(--rk-space-2)' }}>
          <button className="rk-tbtn rk-tbtn--xs" type="button" title="Copy (Ctrl+C)" onClick={ed.ops.copy} disabled={!hasSel}>
            COPY
          </button>
          <button className="rk-tbtn rk-tbtn--xs" type="button" title="Paste at playhead (Ctrl+V)" onClick={ed.ops.paste} disabled={!ed.state.clipboard?.length}>
            PASTE
          </button>
          <button className="rk-tbtn rk-tbtn--xs" type="button" title="Duplicate after selection (Ctrl+D)" onClick={ed.ops.duplicate} disabled={!hasSel}>
            DUP
          </button>
          <button className="rk-tbtn rk-tbtn--xs" type="button" title="Delete (Del)" onClick={ed.ops.deleteSel} disabled={!hasSel}>
            DEL
          </button>
        </div>
        <div style={{ flex: 1 }} />
        <div style={{ display: 'flex', gap: 'var(--rk-space-2)' }}>
          <button className="rk-tbtn rk-tbtn--xs" type="button" onClick={ed.zoomOut} aria-label="Zoom out" style={{ width: 30 }}>
            −
          </button>
          <button className="rk-tbtn rk-tbtn--xs" type="button" onClick={ed.zoomIn} aria-label="Zoom in" style={{ width: 30 }}>
            +
          </button>
          <button className="rk-tbtn rk-tbtn--xs" type="button" onClick={ed.fitLoop} disabled={!m.mix.loop}>
            FIT LOOP
          </button>
        </div>
        <span className="rk-drums-readout" data-testid="drums-view">
          {formatTimecode(ed.view.t0)} → {formatTimecode(ed.view.t0 + ed.view.span)}
          {ed.grid.hasGrid ? ` · ${viewLabel(ed.view, ed.grid)}` : ''}
        </span>
      </div>
    </>
  );
}
