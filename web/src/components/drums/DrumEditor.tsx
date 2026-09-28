import { GROUP } from '../../lib/drums/taxonomy';
import type { Mixer } from '../../player/use-mixer';
import type { DrumEditorHandle } from '../../player/use-drum-editor';
import { PanelNotice } from '../EmptyState';
import { EditorCanvas } from './EditorCanvas';
import { EditorToolbar } from './EditorToolbar';
import { Inspector } from './Inspector';
import { Overview } from './Overview';
import { RowHeaders } from './RowHeaders';
import { Ruler } from './Ruler';
import { VelocityLane } from './VelocityLane';

/**
 * The DRUM EDITOR tab (design: RehearseKit Mixer v2). Desktop only (≥ 900 px);
 * transport, loop and position are the mixer's.
 */
export function DrumEditor({ ed, m }: { ed: DrumEditorHandle; m: Mixer }) {
  const hasSel = ed.state.selection.length > 0;
  if (ed.status === 'loading') {
    return (
      <div className="rk-drums" aria-busy="true" data-testid="drums-loading">
        <p className="rk-help" style={{ margin: 0 }}>
          Loading the drum hits…
        </p>
      </div>
    );
  }
  if (ed.status === 'error') {
    return (
      <PanelNotice art="error" title="Could not load the drum editor" maxWidth="52ch" actions={<button className="rk-btn rk-btn--primary" type="button" onClick={ed.reload}>Try again</button>}>
        {ed.error}
      </PanelNotice>
    );
  }
  if (ed.status === 'unavailable') {
    return (
      <PanelNotice art="processing" title="No drum transcription for this job" maxWidth="52ch">
        The editor opens on completed jobs whose drum stem was transcribed.
      </PanelNotice>
    );
  }
  const focusLabel = `${GROUP[ed.focus].label}${GROUP[ed.focus].articulations.length > 1 ? ' · all articulations' : ''}`;
  return (
    <>
      <div className="rk-drums" data-testid="drum-editor">
        <EditorToolbar ed={ed} m={m} />
        <div className="rk-drums-grid">
          <div className="rk-drums-overview-label">
            <span className="rk-eyebrow" style={{ fontSize: 'var(--rk-font-size-xs)' }}>
              OVERVIEW · DRUM STEM
            </span>
          </div>
          <Overview ed={ed} m={m} />
          <RowHeaders ed={ed} />
          <div className="rk-drums-canvases">
            <Ruler ed={ed} m={m} />
            <EditorCanvas ed={ed} m={m} />
          </div>
          <div className="rk-drums-vel-head">
            <div>
              <div className="rk-eyebrow" style={{ fontSize: 'var(--rk-font-size-xs)' }}>
                VELOCITY
              </div>
              <div style={{ fontSize: 'var(--rk-font-size-base)', fontWeight: 'var(--rk-font-weight-semibold)', marginTop: 5 }}>{focusLabel}</div>
              <div style={{ fontFamily: 'var(--rk-font-mono)', fontSize: 'var(--rk-font-size-2xs)', color: 'var(--rk-color-ink-muted)', marginTop: 2 }}>drag bars · click-drag to draw</div>
            </div>
            <div className="rk-drums-velbtns">
              <button type="button" onClick={() => ed.ops.velAdd(-10)} disabled={!hasSel}>
                −10
              </button>
              <button type="button" onClick={() => ed.ops.velAdd(10)} disabled={!hasSel}>
                +10
              </button>
              <button type="button" onClick={ed.ops.compress} disabled={!hasSel} title="Scale selection toward 64" style={{ flex: 1.3 }}>
                COMPRESS
              </button>
            </div>
          </div>
          <VelocityLane ed={ed} m={m} />
          <Inspector ed={ed} />
        </div>
        <div className="rk-drums-foot">
          <p>Moving a MIDI hit never moves the audio transient · Quantize only touches selected hits · Alt-drag bypasses snap · Ctrl+wheel zoom, wheel pan · Space play</p>
          <p style={{ whiteSpace: 'nowrap' }}>{ed.autosave.editRev > 0 && ed.data?.doc.exported_rev === ed.autosave.editRev ? `Exported · rev ${ed.autosave.editRev}` : 'Export uses the saved revision and the GM map'}</p>
        </div>
      </div>
      <div className="rk-drums-narrow" data-testid="drums-narrow">
        <PanelNotice art="processing" title="The drum editor needs a wider window" maxWidth="44ch">
          Open this page at 900 px or wider to edit the drum hits. The mixer and the export still work here.
        </PanelNotice>
      </div>
    </>
  );
}
