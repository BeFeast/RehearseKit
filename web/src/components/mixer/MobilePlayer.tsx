import { useRef, type KeyboardEvent, type PointerEvent as ReactPointerEvent } from 'react';
import type { StemName } from '../../api/types';
import { FADER, formatDb, nudgeDb, UNITY_POSITION } from '../../lib/decibel';
import { formatLoopLabel, formatTimecode } from '../../lib/format';
import { isSilenced } from '../../lib/mix-state';
import type { Mixer } from '../../player/use-mixer';
import { Transport } from './Transport';
import { Waveform } from './Waveform';
import { stemColor } from './Strip';

/**
 * screens/02-job-detail-completed/state-mobile-player: below 560px the
 * console becomes a player — transport, seek, loop, per-stem mute / solo /
 * gain on 44px targets. Same mix state as the desktop console.
 */
export function MobilePlayer({ m, onDownload }: { m: Mixer; onDownload(): void }) {
  const live = m.live.current;
  void m.tick;
  return (
    <div className="rk-mplayer" data-testid="mobile-player">
      <Waveform
        peaks={m.peaks}
        stems={m.stems}
        selected={m.selected}
        duration={m.duration}
        position={live.position}
        loop={m.mix.loop}
        loopEnabled={m.mix.loopEnabled}
        bpm={m.bpm}
        height={88}
        onSeek={m.seek}
        onLoopChange={m.setLoop}
        label="Master waveform"
      />
      <Transport
        compact
        playing={m.playing}
        starting={m.starting}
        position={live.position}
        duration={m.duration}
        bpm={m.bpm}
        loop={m.mix.loop}
        loopEnabled={m.mix.loopEnabled}
        onTogglePlay={m.togglePlay}
        onReturnToStart={m.returnToStart}
        onToggleLoop={m.toggleLoop}
      />
      <div className="rk-mono rk-muted" style={{ fontSize: 'var(--rk-font-size-md)', textAlign: 'center' }}>
        {m.mix.loop ? `LOOP ${formatTimecode(m.mix.loop.start)} → ${formatTimecode(m.mix.loop.end)}${m.bpm ? ` · ${formatLoopLabel(m.mix.loop.start, m.mix.loop.end, m.bpm)}` : ''}` : 'No loop set — press LOOP to drill a section'}
      </div>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 'var(--rk-space-5)' }}>
        {m.stems.map((stem) => (
          <MobileStemRow key={stem} stem={stem} m={m} />
        ))}
      </div>
      <div style={{ display: 'flex', gap: 'var(--rk-space-5)' }}>
        <button className="rk-btn rk-btn--primary rk-btn--block" type="button" onClick={onDownload}>
          Download Package
        </button>
      </div>
      <p className="rk-help" style={{ textAlign: 'center', margin: 0 }}>
        Metering and the master strip are desktop only. Open this job on a laptop for the full console.
      </p>
    </div>
  );
}

function MobileStemRow({ stem, m }: { stem: StemName; m: Mixer }) {
  const s = m.mix.stems[stem];
  const silenced = isSilenced(m.mix, stem);
  const dead = s.muted || silenced;
  const name = stem.toUpperCase();
  const db = FADER.y(s.position);
  const track = useRef<HTMLDivElement>(null);
  const dragging = useRef(false);

  const posFromX = (clientX: number) => {
    const r = track.current!.getBoundingClientRect();
    return Math.max(0, Math.min(1, (clientX - r.left) / r.width));
  };
  const onDown = (e: ReactPointerEvent) => {
    e.preventDefault();
    track.current?.setPointerCapture(e.pointerId);
    dragging.current = true;
    m.dispatch({ type: 'gain', stem, position: posFromX(e.clientX) });
  };
  const onMove = (e: ReactPointerEvent) => {
    if (dragging.current) m.dispatch({ type: 'gain', stem, position: posFromX(e.clientX) });
  };
  const onUp = (e: ReactPointerEvent) => {
    dragging.current = false;
    track.current?.releasePointerCapture(e.pointerId);
  };
  const onKey = (e: KeyboardEvent) => {
    let next: number | null = null;
    if (e.key === 'ArrowRight' || e.key === 'ArrowUp') next = nudgeDb(s.position, e.shiftKey ? 5 : 1);
    else if (e.key === 'ArrowLeft' || e.key === 'ArrowDown') next = nudgeDb(s.position, e.shiftKey ? -5 : -1);
    else if (e.key === 'Home') next = 1;
    else if (e.key === 'End') next = 0;
    else if (e.key === 'Backspace') next = UNITY_POSITION;
    if (next === null) return;
    e.preventDefault();
    e.stopPropagation();
    m.dispatch({ type: 'gain', stem, position: next });
  };

  return (
    <div className="rk-mstem" data-muted={dead ? 'true' : 'false'} data-silenced={silenced ? 'true' : undefined}>
      <button className="rk-mpad" type="button" aria-pressed={s.muted} aria-label={`Mute ${name}`} onClick={() => m.toggleMute(stem)}>
        M
      </button>
      <div>
        <div style={{ display: 'flex', alignItems: 'baseline', justifyContent: 'space-between', gap: 'var(--rk-space-5)', marginBottom: 'var(--rk-space-4)' }}>
          <span className="rk-mono" style={{ fontSize: 'var(--rk-font-size-md)', fontWeight: 700, letterSpacing: 'var(--rk-tracking-wide)' }}>
            {name}
          </span>
          <span className="rk-mono rk-muted" style={{ fontSize: 'var(--rk-font-size-md)' }}>
            {dead ? '-∞ dB' : `${formatDb(db)} dB`}
          </span>
        </div>
        <div
          ref={track}
          className="rk-mgain"
          role="slider"
          tabIndex={0}
          aria-label={`${name} level`}
          aria-valuemin={0}
          aria-valuemax={100}
          aria-valuenow={Math.round(s.position * 100)}
          aria-valuetext={dead ? 'muted' : `${formatDb(db)} dB`}
          onPointerDown={onDown}
          onPointerMove={onMove}
          onPointerUp={onUp}
          onPointerCancel={onUp}
          onKeyDown={onKey}
        >
          <i style={{ width: `${s.position * 100}%`, background: stemColor(stem) }} />
          <b style={{ left: `${s.position * 100}%` }} />
        </div>
      </div>
      <button className="rk-mpad" type="button" aria-pressed={m.mix.solo === stem} aria-label={`Solo ${name}`} style={{ width: 56, ...(m.mix.solo === stem ? { background: 'var(--rk-color-status-warning)', color: '#332c1c' } : {}) }} onClick={() => m.toggleSolo(stem)}>
        S
      </button>
    </div>
  );
}
