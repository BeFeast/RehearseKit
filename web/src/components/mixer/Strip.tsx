import { useRef, useState, type KeyboardEvent, type PointerEvent as ReactPointerEvent } from 'react';
import type { StemName } from '../../api/types';
import { FADER, FADER_TICKS, formatDb, meterHeight, nudgeDb, UNITY_POSITION } from '../../lib/decibel';

export const FADER_H = 182;
export const CAP_H = 22;
export const TRAVEL = FADER_H - CAP_H;

const STEM_VAR: Record<StemName, string> = {
  vocals: '--rk-color-stem-vocals',
  drums: '--rk-color-stem-drums',
  bass: '--rk-color-stem-bass',
  other: '--rk-color-stem-other',
  guitar: '--rk-color-stem-guitar',
  piano: '--rk-color-stem-piano',
};

export function stemColor(stem: StemName): string {
  return `var(${STEM_VAR[stem]})`;
}

export interface StripProps {
  stem: StemName;
  caption: string;
  position: number;
  muted: boolean;
  /** Excluded by another strip's solo — drawn differently from muted. */
  silenced: boolean;
  soloed: boolean;
  selected: boolean;
  /** Post-fader linear level and peak hold, read from the engine each frame. */
  level: number;
  hold: number;
  index: number;
  disabled?: boolean;
  onPosition(v: number): void;
  onToggleMute(): void;
  onToggleSolo(): void;
  onSelect(): void;
}

/** components/stem-track-row without the EQ/SEND knob block (v1 decision). */
export function Strip(p: StripProps) {
  const dead = p.muted || p.silenced;
  const db = FADER.y(p.position);
  const name = p.stem.toUpperCase();
  const color = stemColor(p.stem);
  return (
    <section
      className="rk-strip"
      data-selected={p.selected ? 'true' : 'false'}
      data-dead={dead ? 'true' : undefined}
      data-muted={p.muted ? 'true' : undefined}
      data-silenced={p.silenced ? 'true' : undefined}
      aria-label={`${name} channel strip`}
      onClick={p.onSelect}
      data-testid={`strip-${p.stem}`}
    >
      <div className="rk-strip-head">
        <div className="rk-strip-led" style={{ background: color, boxShadow: `0 0 6px color-mix(in srgb, ${color} 65%, transparent)` }} />
        <div className="rk-strip-name">{name}</div>
        <div className="rk-strip-sub">{p.caption}</div>
      </div>
      <div className="rk-faderrow">
        <Scale />
        <Fader
          position={p.position}
          label={`${name} level`}
          valueText={dead ? (p.muted ? 'muted' : 'silenced by solo') : `${formatDb(db)} dB`}
          onChange={p.onPosition}
          disabled={p.disabled}
        />
        <Meter level={dead ? 0 : p.level} hold={dead ? 0 : p.hold} color={color} />
      </div>
      <div className="rk-dbrow">
        <b data-testid={`db-${p.stem}`}>{dead ? '-∞' : formatDb(db)}</b>
        <span>dB</span>
      </div>
      <div className="rk-sm-row">
        <button
          className="rk-sm"
          data-kind="solo"
          type="button"
          aria-pressed={p.soloed}
          aria-label={`Solo ${name}`}
          disabled={p.disabled}
          onClick={(e) => {
            e.stopPropagation();
            p.onToggleSolo();
          }}
        >
          S
        </button>
        <button
          className="rk-sm"
          data-kind="mute"
          type="button"
          aria-pressed={p.muted}
          aria-label={`Mute ${name}`}
          disabled={p.disabled}
          onClick={(e) => {
            e.stopPropagation();
            p.onToggleMute();
          }}
        >
          M
        </button>
      </div>
      <div className="rk-striptag" style={{ background: `color-mix(in srgb, ${color} 62%, transparent)`, color: 'var(--rk-color-on-copper)' }}>
        {name}
      </div>
      <span className="visually-hidden" aria-live="polite">
        {p.silenced ? `${name} silenced by solo` : ''}
      </span>
    </section>
  );
}

/** The mono dB column beside the fader; tick positions follow the fader law. */
export function Scale() {
  return (
    <div className="rk-scale" aria-hidden="true">
      {FADER_TICKS.map((db) => (
        <span key={db} style={{ bottom: tickBottom(db) }}>
          {db}
        </span>
      ))}
      <span style={{ bottom: 7 }}>-&#8734;</span>
    </div>
  );
}

/** Pixel offset from the strip bottom for a tick label at `db` (centre of the cap). */
export function tickBottom(db: number): number {
  return Math.round(FADER.x(db) * TRAVEL + CAP_H / 2 - 4);
}

export interface FaderProps {
  position: number;
  label: string;
  valueText: string;
  onChange(v: number): void;
  master?: boolean;
  disabled?: boolean;
}

/** 40×182 hit area, 160px travel, copper cap; keyboard per components/slider/spec.md. */
export function Fader(p: FaderProps) {
  const el = useRef<HTMLDivElement>(null);
  const [dragging, setDragging] = useState(false);
  const start = useRef<{ y: number; pos: number } | null>(null);

  const posFromY = (clientY: number) => {
    const r = el.current!.getBoundingClientRect();
    const y = clientY - r.top;
    return Math.max(0, Math.min(1, 1 - (y - CAP_H / 2) / TRAVEL));
  };

  const onDown = (e: ReactPointerEvent) => {
    if (p.disabled) return;
    e.preventDefault();
    e.stopPropagation();
    el.current?.setPointerCapture(e.pointerId);
    el.current?.focus();
    // Grab the cap where it is; clicking the track jumps there.
    const r = el.current!.getBoundingClientRect();
    const capTop = r.top + (1 - p.position) * TRAVEL;
    const onCap = e.clientY >= capTop && e.clientY <= capTop + CAP_H;
    if (!onCap) p.onChange(posFromY(e.clientY));
    start.current = { y: e.clientY, pos: onCap ? p.position : posFromY(e.clientY) };
    setDragging(true);
  };
  const onMove = (e: ReactPointerEvent) => {
    if (!dragging || !start.current) return;
    const dy = e.clientY - start.current.y;
    const fine = e.shiftKey ? 0.25 : 1;
    p.onChange(Math.max(0, Math.min(1, start.current.pos - (dy * fine) / TRAVEL)));
  };
  const onUp = (e: ReactPointerEvent) => {
    if (!dragging) return;
    el.current?.releasePointerCapture(e.pointerId);
    setDragging(false);
    start.current = null;
  };
  const onKey = (e: KeyboardEvent) => {
    if (p.disabled) return;
    let next: number | null = null;
    switch (e.key) {
      case 'ArrowUp':
        next = nudgeDb(p.position, e.shiftKey ? 5 : 1);
        break;
      case 'ArrowDown':
        next = nudgeDb(p.position, e.shiftKey ? -5 : -1);
        break;
      case 'PageUp':
        next = nudgeDb(p.position, 10);
        break;
      case 'PageDown':
        next = nudgeDb(p.position, -10);
        break;
      case 'Home':
        next = 1;
        break;
      case 'End':
        next = 0;
        break;
      case 'Backspace':
      case 'Delete':
        next = UNITY_POSITION;
        break;
      default:
        return;
    }
    e.preventDefault();
    e.stopPropagation();
    p.onChange(next);
  };

  const db = FADER.y(p.position);
  return (
    <div
      ref={el}
      className="rk-fader"
      role="slider"
      tabIndex={p.disabled ? -1 : 0}
      aria-label={p.label}
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={Math.round(p.position * 100)}
      aria-valuetext={p.valueText}
      aria-disabled={p.disabled || undefined}
      data-dragging={dragging ? 'true' : undefined}
      onPointerDown={onDown}
      onPointerMove={onMove}
      onPointerUp={onUp}
      onPointerCancel={onUp}
      onKeyDown={onKey}
      onDoubleClick={(e) => {
        e.stopPropagation();
        if (!p.disabled) p.onChange(UNITY_POSITION);
      }}
      onClick={(e) => e.stopPropagation()}
      title={Number.isFinite(db) ? `${formatDb(db)} dB` : '-∞ dB'}
    >
      <div className="rk-fader-track" />
      {FADER_TICKS.map((t) => (
        <hr key={t} className={t === -3 || t === -12 ? 'minor' : undefined} style={{ bottom: tickBottom(t) + 3 }} />
      ))}
      <div className={p.master ? 'rk-cap rk-cap--master' : 'rk-cap'} style={{ bottom: p.position * TRAVEL }}>
        <i />
      </div>
    </div>
  );
}

export interface MeterProps {
  /** Linear 0..1+ */
  level: number;
  hold?: number;
  color: string;
  narrow?: boolean;
  /** Master meters use the green→amber→red gradient. */
  gradient?: boolean;
}

/** components/level-meter: 8px-on/4px-off LED mask over a tinted fill. */
export function Meter({ level, hold = 0, color, narrow = false, gradient = false }: MeterProps) {
  const h = meterHeight(level) * 100;
  const hh = meterHeight(hold) * 100;
  const fill = gradient
    ? { background: 'linear-gradient(to top,var(--rk-color-status-success) 0%,var(--rk-color-status-success) 58%,var(--rk-color-status-warning) 78%,var(--rk-color-status-danger) 100%)', backgroundSize: '100% 178px', backgroundPosition: 'bottom' }
    : { background: color };
  return (
    <div className={narrow ? 'rk-meter rk-meter--narrow' : 'rk-meter'} aria-hidden="true">
      <b />
      <i style={{ height: `${h}%`, ...fill }} />
      {hh > 1 && (
        <i
          style={{
            height: 2,
            bottom: `calc(${hh}% - 1px)`,
            maskImage: 'none',
            WebkitMaskImage: 'none',
            background: gradient ? (hh > 78 ? 'var(--rk-color-status-danger)' : hh > 58 ? 'var(--rk-color-status-warning)' : 'var(--rk-color-status-success)') : color,
          }}
        />
      )}
    </div>
  );
}
