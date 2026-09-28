import { ARTICULATIONS, GROUP, midiKeyLabel, type Articulation } from '../../lib/drums/taxonomy';
import type { DrumEditorHandle } from '../../player/use-drum-editor';

/** The row list left of the canvases: disclosure, colour, label, MIDI key, S/M. */
export function RowHeaders({ ed }: { ed: DrumEditorHandle }) {
  const profile = ed.data?.profile;
  const noteOf = (a: Articulation) => (profile ? midiKeyLabel(profile.notes[a]) : '—');
  return (
    <div className="rk-drums-heads" data-testid="drums-rows">
      <div className="rk-drums-heads-title">
        <span className="rk-eyebrow" style={{ fontSize: 'var(--rk-font-size-xs)' }}>
          ROWS
        </span>
        <span className="rk-drums-readout">{profile ? `${profile.name.toUpperCase()} MAP` : ''}</span>
      </div>
      {ed.layout.rows.map((r) => {
        if (r.type === 'audio') {
          return (
            <div className="rk-drums-row" data-kind="audio" key={r.key} style={{ height: r.h }}>
              <span className="rk-drums-swatch" style={{ background: 'var(--rk-color-wave-main)' }} />
              <span className="rk-drums-row-label">
                <b>Drum stem</b>
                <small>AUDIO · DEMUCS HT</small>
              </span>
            </div>
          );
        }
        const g = GROUP[r.group];
        const isMidi = r.type === 'midi';
        const key = r.key;
        const mute = Boolean(ed.rowMute[key]);
        const solo = ed.rowSolo === key;
        const label = isMidi ? ARTICULATIONS[r.art].label : g.label;
        const sub = isMidi ? `MIDI · ${noteOf(r.art)}` : g.articulations.length > 1 ? g.articulations.map((a) => ARTICULATIONS[a].label).join(' · ') : `MIDI · ${noteOf(g.articulations[0])}`;
        return (
          <div className="rk-drums-row" data-kind={r.type} data-focus={ed.focus === r.group} key={key} style={{ height: r.h }} onClick={() => ed.setFocus(r.group)} role="button" tabIndex={-1}>
            {!isMidi && g.articulations.length > 1 ? (
              <button
                className="rk-drums-disc"
                type="button"
                aria-label={ed.open[r.group] ? `Collapse ${g.label}` : `Expand ${g.label}`}
                aria-expanded={Boolean(ed.open[r.group])}
                onClick={(e) => {
                  e.stopPropagation();
                  ed.toggleOpen(r.group);
                }}
              >
                {ed.open[r.group] ? '▾' : '▸'}
              </button>
            ) : (
              <span style={{ width: isMidi ? 0 : 14 }} />
            )}
            <span className="rk-drums-swatch" style={{ background: `var(${g.colorVar})` }} />
            <span className="rk-drums-row-label">
              <b>{label}</b>
              <small>{sub}</small>
            </span>
            <button
              className="rk-drums-sm"
              type="button"
              data-on={solo ? 'solo' : undefined}
              title="MIDI solo"
              aria-pressed={solo}
              onClick={(e) => {
                e.stopPropagation();
                ed.toggleRowSolo(key);
              }}
            >
              S
            </button>
            <button
              className="rk-drums-sm"
              type="button"
              data-on={mute ? 'mute' : undefined}
              title="MIDI mute"
              aria-pressed={mute}
              onClick={(e) => {
                e.stopPropagation();
                ed.toggleRowMute(key);
              }}
            >
              M
            </button>
          </div>
        );
      })}
    </div>
  );
}
