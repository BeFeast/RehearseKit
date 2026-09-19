import { FADER, formatDb } from '../../lib/decibel';
import { formatBpm } from '../../lib/format';
import { Fader, Meter, Scale } from './Strip';

export interface MasterStripProps {
  position: number;
  bpm: number | null;
  bitDepth: number | null;
  sampleRate: number | null;
  left: number;
  right: number;
  leftHold: number;
  rightHold: number;
  soloLive: boolean;
  selected: boolean;
  disabled?: boolean;
  onPosition(v: number): void;
  onClearSolo(): void;
  onSelect(): void;
}

/** Master strip: BPM tile in place of knobs, stereo meter pair, CLR SOLO. */
export function MasterStrip(p: MasterStripProps) {
  const db = FADER.y(p.position);
  const sub = p.sampleRate ? `${p.bitDepth ?? 24} BIT / ${(p.sampleRate / 1000).toFixed(p.sampleRate % 1000 ? 1 : 0)} kHz` : 'MASTER BUS';
  return (
    <section className="rk-strip rk-strip--master" data-selected={p.selected ? 'true' : 'false'} aria-label="Master strip" onClick={p.onSelect} data-testid="strip-master">
      <div className="rk-strip-head">
        <div className="rk-strip-led" style={{ background: 'var(--rk-color-copper)', boxShadow: '0 0 7px color-mix(in srgb, var(--rk-color-copper) 70%, transparent)' }} />
        <div className="rk-strip-name">MASTER</div>
        <div className="rk-strip-sub">{sub}</div>
      </div>
      <div className="rk-knobs" style={{ gridTemplateColumns: '1fr', paddingTop: 'var(--rk-space-6)' }}>
        <div
          style={{
            width: '100%',
            padding: 'var(--rk-space-3) var(--rk-space-4)',
            borderRadius: 'var(--rk-radius-sm)',
            background: 'var(--rk-color-lcd)',
            boxShadow: 'inset 0 1px 3px rgba(0,0,0,.2)',
            textAlign: 'center',
          }}
        >
          <div
            style={{
              fontFamily: 'var(--rk-font-mono)',
              fontSize: p.bpm ? 'var(--rk-font-size-base)' : 'var(--rk-font-size-xs)',
              fontWeight: 700,
              letterSpacing: p.bpm ? undefined : 'var(--rk-tracking-wide)',
              color: p.bpm ? 'var(--rk-color-copper)' : 'var(--rk-color-ink-muted)',
              lineHeight: p.bpm ? undefined : '17.5px',
            }}
            data-testid="master-bpm"
          >
            {p.bpm ? formatBpm(p.bpm) : 'NOT DETECTED'}
          </div>
          <div style={{ fontFamily: 'var(--rk-font-mono)', fontSize: 7.5, letterSpacing: 'var(--rk-tracking-wider)', color: 'var(--rk-color-ink-muted)', marginTop: 'var(--rk-space-1)' }}>
            DETECTED BPM
          </div>
        </div>
      </div>
      <div className="rk-faderrow">
        <Scale />
        <Fader position={p.position} label="Master level" valueText={`${formatDb(db)} dB`} onChange={p.onPosition} master disabled={p.disabled} />
        <div style={{ display: 'flex', gap: 3 }} aria-hidden="true">
          <Meter level={p.left} hold={p.leftHold} color="var(--rk-color-status-success)" narrow gradient />
          <Meter level={p.right} hold={p.rightHold} color="var(--rk-color-status-success)" narrow gradient />
        </div>
      </div>
      <div className="rk-dbrow">
        <b style={{ color: 'var(--rk-color-copper)' }}>{formatDb(db)}</b>
        <span>dB</span>
      </div>
      <div className="rk-sm-row">
        <button
          className="rk-sm"
          type="button"
          data-kind="solo"
          aria-pressed={p.soloLive}
          disabled={p.disabled}
          onClick={(e) => {
            e.stopPropagation();
            p.onClearSolo();
          }}
          data-testid="clear-solo"
        >
          CLR SOLO
        </button>
      </div>
      <div className="rk-striptag" style={{ background: 'color-mix(in srgb, var(--rk-color-copper) 34%, transparent)', color: 'var(--rk-color-copper)', borderTopColor: 'var(--rk-color-copper)' }}>
        MASTER
      </div>
    </section>
  );
}
