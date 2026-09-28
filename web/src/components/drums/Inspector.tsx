import { useState } from 'react';
import { describeAutosave } from '../../lib/drums/autosave';
import { primary } from '../../lib/drums/model';
import { ARTICULATION_KEYS, ARTICULATIONS, articulationTitle, midiKeyLabel, velTo127 } from '../../lib/drums/taxonomy';
import type { DrumEditorHandle } from '../../player/use-drum-editor';

/** Right column: the selection's fields, its provenance, and the document status. */
export function Inspector({ ed }: { ed: DrumEditorHandle }) {
  const p = primary(ed.state);
  const n = ed.state.selection.length;
  const profile = ed.data?.profile;
  const model = ed.data?.model;
  return (
    <aside className="rk-drums-insp" aria-label="Inspector" data-testid="drums-inspector">
      <div className="rk-drums-insp-title">
        <span className="rk-eyebrow" style={{ fontSize: 'var(--rk-font-size-xs)' }}>
          INSPECTOR
        </span>
        <span className="rk-drums-chip" data-testid="drums-sel-title">
          {!p ? 'NO SELECTION' : n === 1 ? articulationTitle(p.art).toUpperCase() : `${n} HITS`}
        </span>
      </div>
      {!p ? (
        <p className="rk-drums-insp-empty" style={{ margin: 0 }}>
          Click a hit to inspect it. Shift-click adds to the selection, drag on empty space for a marquee. Double-click empty space to add a hit.
        </p>
      ) : (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 'var(--rk-space-5)' }}>
          <label className="rk-drums-field">
            <span>ARTICULATION</span>
            <select value={p.art} onChange={(e) => ed.ops.setSelArt(e.target.value as typeof p.art)} data-testid="drums-sel-art">
              {ARTICULATION_KEYS.map((a) => (
                <option key={a} value={a}>
                  {articulationTitle(a)}
                </option>
              ))}
            </select>
          </label>
          <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 'var(--rk-space-4)' }}>
            <label className="rk-drums-field">
              <span>TIME · S</span>
              <TimeField key={p.id} value={p.t} onCommit={ed.ops.setSelTime} />
            </label>
            <div className="rk-drums-field">
              <span>BAR.BEAT.TICK</span>
              <div className="rk-drums-static" data-testid="drums-sel-pos">
                {ed.grid.hasGrid ? ed.grid.formatPosition(p.t) : '—'}
              </div>
            </div>
          </div>
          <label className="rk-drums-field">
            <div style={{ display: 'flex', justifyContent: 'space-between' }}>
              <span>VELOCITY</span>
              <span style={{ color: 'var(--rk-color-copper)', fontFamily: 'var(--rk-font-mono)', fontSize: 'var(--rk-font-size-sm)', fontWeight: 'var(--rk-font-weight-bold)' }} data-testid="drums-sel-vel">
                {velTo127(p.vel)}
              </span>
            </div>
            <input
              type="range"
              min={1}
              max={127}
              value={velTo127(p.vel)}
              // One undo step per drag or key press sequence, like the velocity lane.
              onPointerDown={() => ed.dispatch({ type: 'gestureBegin' })}
              onPointerUp={() => ed.dispatch({ type: 'gestureEnd' })}
              onKeyDown={() => ed.dispatch({ type: 'gestureBegin' })}
              onKeyUp={() => ed.dispatch({ type: 'gestureEnd' })}
              onBlur={() => ed.dispatch({ type: 'gestureEnd' })}
              onChange={(e) => ed.ops.setSelVel(Number(e.target.value))}
              aria-label="Velocity"
            />
          </label>
          <div className="rk-drums-kvbox">
            <div className="rk-drums-kv">
              <span>SOURCE</span>
              <span data-testid="drums-sel-src">{p.src === 'manual' ? 'MANUAL EDIT' : `${(model?.adapter ?? 'model').toUpperCase()} · full stem`}</span>
            </div>
            <div className="rk-drums-kv">
              <span>SCORE</span>
              <span>—</span>
            </div>
            <div className="rk-drums-kv">
              <span>MIDI OUT</span>
              <span>{profile ? `${midiKeyLabel(profile.notes[p.art])} · ${profile.id.toUpperCase()}` : '—'}</span>
            </div>
            <div className="rk-drums-kv">
              <span>AUDIO LANE</span>
              <span>Drum stem</span>
            </div>
          </div>
          <div style={{ display: 'flex', gap: 'var(--rk-space-3)' }}>
            <button className="rk-btn rk-btn--mono rk-btn--sm" type="button" style={{ flex: 1 }} onClick={ed.ops.deleteSel}>
              DELETE
            </button>
          </div>
        </div>
      )}
      <div style={{ flex: 1 }} />
      <div className="rk-drums-insp-foot">
        <div className="rk-drums-kv">
          <span>MODEL REV</span>
          <span data-testid="drums-model-rev">{model ? `${model.adapter ?? '—'}${model.model ? ` · ${model.model}` : ''}` : '—'}</span>
        </div>
        <div className="rk-drums-kv">
          <span>EDIT REV</span>
          <span data-testid="drums-edit-rev">
            rev {ed.autosave.editRev} · {ed.state.hits.length} hits
          </span>
        </div>
        <div className="rk-drums-save" data-status={ed.autosave.status} data-testid="drums-save">
          <i />
          <span>{describeAutosave(ed.autosave)}</span>
        </div>
        {ed.autosave.status === 'conflict' && (
          <div className="rk-drums-conflict" role="alert" data-testid="drums-conflict">
            These hits were changed in another tab (rev {ed.autosave.conflictRev}). Your last edits here were not saved —{' '}
            <a
              href="#"
              onClick={(e) => {
                e.preventDefault();
                ed.reload();
              }}
            >
              reload
            </a>{' '}
            to continue from the latest revision.
          </div>
        )}
        {ed.data?.stale_model && (
          <div className="rk-drums-conflict" style={{ background: 'color-mix(in srgb, var(--rk-color-copper) 14%, transparent)', borderColor: 'color-mix(in srgb, var(--rk-color-copper) 35%, transparent)' }}>
            The model output changed since this revision was made; your edits are kept.
          </div>
        )}
      </div>
    </aside>
  );
}

/**
 * TIME · S: edits a draft while focused and commits once on blur or Enter
 * (Escape reverts). An empty or unparsable draft never moves the hit.
 */
function TimeField({ value, onCommit }: { value: number; onCommit(t: number): void }) {
  const [draft, setDraft] = useState<string | null>(null);
  const commit = () => {
    if (draft === null) return;
    const raw = draft.trim();
    const t = Number(raw);
    setDraft(null);
    if (raw !== '' && Number.isFinite(t) && t >= 0 && Math.abs(t - value) > 1e-9) onCommit(t);
  };
  return (
    <input
      type="text"
      inputMode="decimal"
      value={draft ?? value.toFixed(3)}
      onFocus={(e) => {
        setDraft(value.toFixed(3));
        e.currentTarget.select();
      }}
      onChange={(e) => setDraft(e.target.value)}
      onBlur={commit}
      onKeyDown={(e) => {
        if (e.key === 'Enter') {
          commit();
          e.currentTarget.blur();
        } else if (e.key === 'Escape') {
          setDraft(null);
          e.currentTarget.blur();
        }
      }}
      aria-label="Time in seconds"
      data-testid="drums-sel-time"
    />
  );
}

export { ARTICULATIONS };
