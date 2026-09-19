import { formatBpm, formatLoopLabel, formatTimecode } from '../../lib/format';
import { Icon } from '../Icon';

export interface TransportProps {
  playing: boolean;
  starting?: boolean;
  position: number;
  duration: number;
  bpm: number | null;
  loop: { start: number; end: number } | null;
  loopEnabled: boolean;
  disabled?: boolean;
  onTogglePlay(): void;
  onReturnToStart(): void;
  onToggleLoop(): void;
  /** Mobile variant: 44px controls, position/duration only in the LCD. */
  compact?: boolean;
}

/**
 * components/transport-bar. Space / Home / L are handled by the job page,
 * not here, so the shortcuts work while focus is inside the mixer.
 */
export function Transport(p: TransportProps) {
  const size = p.compact ? 44 : 36;
  return (
    <div className="rk-transport" role="group" aria-label="Transport" aria-disabled={p.disabled || undefined} style={p.disabled ? { opacity: 0.45, pointerEvents: 'none' } : undefined}>
      <button
        className="rk-tbtn"
        type="button"
        aria-label={p.loopEnabled && p.loop ? 'Return to loop start' : 'Return to start'}
        onClick={p.onReturnToStart}
        style={p.compact ? { width: size, height: size } : undefined}
        data-testid="transport-home"
      >
        <Icon name="skip-start" size={18} />
      </button>
      <button
        className="rk-tbtn rk-tbtn--play"
        type="button"
        aria-label={p.playing ? 'Pause' : 'Play'}
        aria-pressed={p.playing}
        aria-busy={p.starting || undefined}
        onClick={p.onTogglePlay}
        style={p.compact ? { width: 76, height: size } : undefined}
        data-testid="transport-play"
      >
        {p.playing ? (
          <span style={{ display: 'flex', gap: 4 }}>
            <i style={{ display: 'block', width: 4, height: 15, background: 'currentColor' }} />
            <i style={{ display: 'block', width: 4, height: 15, background: 'currentColor' }} />
          </span>
        ) : (
          <span style={{ display: 'block', width: 0, height: 0, borderLeft: '12px solid currentColor', borderTop: '8px solid transparent', borderBottom: '8px solid transparent' }} />
        )}
      </button>
      <button className="rk-tbtn rk-tbtn--toggle" type="button" aria-pressed={p.loopEnabled} onClick={p.onToggleLoop} style={p.compact ? { height: size } : undefined} data-testid="transport-loop">
        LOOP
      </button>
      <div className="rk-spacer" />
      {p.compact ? (
        <div className="rk-lcd" style={{ padding: 'var(--rk-space-3) var(--rk-space-5)' }} aria-live="off">
          <span className="rk-time" style={{ fontSize: 'var(--rk-font-size-xl)' }} data-testid="lcd-position">
            {formatTimecode(p.position)}
          </span>
          <span className="rk-dim">/{formatTimecode(p.duration)}</span>
        </div>
      ) : (
        <div className="rk-lcd" aria-live="off">
          <span className="rk-time" data-testid="lcd-position">
            {formatTimecode(p.position)}
          </span>
          <span className="rk-dim">/ {formatTimecode(p.duration)}</span>
          <hr />
          {p.bpm ? (
            <>
              <span className="rk-bpm">{formatBpm(p.bpm)}</span>
              <span className="rk-dim" style={{ fontSize: 'var(--rk-font-size-xs)', letterSpacing: 'var(--rk-tracking-wide)' }}>
                BPM
              </span>
            </>
          ) : (
            <span className="rk-dim">BPM —</span>
          )}
          <hr />
          <span className="rk-dim" data-testid="lcd-loop">
            {p.loop ? formatLoopLabel(p.loop.start, p.loop.end, p.bpm) : p.bpm ? 'BARS —' : 'LOOP —'}
          </span>
        </div>
      )}
    </div>
  );
}
