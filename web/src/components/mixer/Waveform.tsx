import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type PointerEvent as ReactPointerEvent } from 'react';
import type { StemName } from '../../api/types';
import { columnsFor, mergeColumns, type Column, type PeaksFile } from '../../lib/peaks';
import { snapToBeat } from '../../lib/format';

export const MIN_LOOP_SECONDS = 2;

export interface WaveformProps {
  peaks: Partial<Record<StemName, PeaksFile>>;
  stems: StemName[];
  /** null = master mix. */
  selected: StemName | null;
  duration: number;
  position: number;
  loop: { start: number; end: number } | null;
  loopEnabled: boolean;
  bpm: number | null;
  height?: number;
  disabled?: boolean;
  onSeek(seconds: number): void;
  onLoopChange(start: number, end: number): void;
  /** Called while a drag is in progress (scrub or handle) with the pointer time. */
  onScrub?(seconds: number | null): void;
  label?: string;
}

const STEM_VAR: Record<StemName, string> = {
  vocals: '--rk-color-stem-vocals',
  drums: '--rk-color-stem-drums',
  bass: '--rk-color-stem-bass',
  other: '--rk-color-stem-other',
  guitar: '--rk-color-stem-guitar',
  piano: '--rk-color-stem-piano',
};

/**
 * Master waveform: canvas drawn from the .pk mip pyramid (3px bars on a 4px
 * grid like the handoff SVGs), played region, copper loop region with
 * draggable grips, and the playhead. Click/drag seeks; handles snap to the
 * beat grid when a BPM is known.
 */
export function Waveform(p: WaveformProps) {
  const box = useRef<HTMLDivElement>(null);
  const canvas = useRef<HTMLCanvasElement>(null);
  const [width, setWidth] = useState(0);
  const [drag, setDrag] = useState<{ kind: 'scrub' | 'a' | 'b'; t: number } | null>(null);
  const height = p.height ?? 118;

  useLayoutEffect(() => {
    const el = box.current;
    if (!el) return;
    const ro = new ResizeObserver(([e]) => setWidth(Math.round(e.contentRect.width)));
    ro.observe(el);
    setWidth(Math.round(el.getBoundingClientRect().width));
    return () => ro.disconnect();
  }, []);

  const columns = useMemo<Column[]>(() => {
    if (width <= 0) return [];
    const n = Math.max(1, Math.floor(width / 4));
    const files = (p.selected ? [p.selected] : p.stems).map((s) => p.peaks[s]).filter((f): f is PeaksFile => Boolean(f));
    if (files.length === 0) return [];
    const sets = files.map((f) => columnsFor(f, 0, f.frames, n));
    return p.selected ? sets[0] : mergeColumns(sets);
  }, [width, p.peaks, p.stems, p.selected]);

  const color = p.selected ? `var(${STEM_VAR[p.selected]})` : 'var(--rk-color-wave-main)';

  useEffect(() => {
    const c = canvas.current;
    if (!c || width <= 0) return;
    const dpr = window.devicePixelRatio || 1;
    c.width = Math.round(width * dpr);
    c.height = Math.round(height * dpr);
    const ctx = c.getContext('2d');
    if (!ctx) return;
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.clearRect(0, 0, width, height);
    const fill = getComputedStyle(c).getPropertyValue('color') || '#8b8272';
    ctx.fillStyle = fill;
    const mid = height / 2;
    const amp = height * 0.44;
    for (let i = 0; i < columns.length; i++) {
      const { min, max } = columns[i];
      const top = mid - Math.max(1.05, max * amp);
      const bottom = mid - Math.min(-1.05, min * amp);
      ctx.fillRect(i * 4, top, 3, Math.max(2.1, bottom - top));
    }
    ctx.globalAlpha = 0.35;
    ctx.fillRect(0, mid - 0.5, width, 1);
    ctx.globalAlpha = 1;
  }, [columns, width, height, color]);

  const timeAt = useCallback(
    (clientX: number) => {
      const el = box.current;
      if (!el || p.duration <= 0) return 0;
      const r = el.getBoundingClientRect();
      const x = Math.max(0, Math.min(r.width, clientX - r.left));
      return (x / r.width) * p.duration;
    },
    [p.duration],
  );

  const pct = (t: number) => (p.duration > 0 ? `${Math.max(0, Math.min(100, (t / p.duration) * 100))}%` : '0%');

  // ---- pointer handling ------------------------------------------------------
  const lastSeek = useRef(0);
  const onDown = (kind: 'scrub' | 'a' | 'b') => (e: ReactPointerEvent) => {
    if (p.disabled || p.duration <= 0) return;
    e.preventDefault();
    e.stopPropagation();
    (e.currentTarget as HTMLElement).setPointerCapture(e.pointerId);
    const t = timeAt(e.clientX);
    if (kind === 'scrub') {
      p.onSeek(t);
      lastSeek.current = performance.now();
    }
    setDrag({ kind, t: kind === 'scrub' ? t : kind === 'a' ? p.loop!.start : p.loop!.end });
    p.onScrub?.(t);
  };
  const onMove = (e: ReactPointerEvent) => {
    if (!drag) return;
    let t = timeAt(e.clientX);
    if (drag.kind === 'scrub') {
      p.onScrub?.(t);
      if (performance.now() - lastSeek.current > 150) {
        p.onSeek(t);
        lastSeek.current = performance.now();
      }
      setDrag({ kind: 'scrub', t });
      return;
    }
    if (!p.loop) return;
    t = snapToBeat(t, p.bpm);
    if (drag.kind === 'a') t = Math.max(0, Math.min(t, p.loop.end - MIN_LOOP_SECONDS));
    else t = Math.min(p.duration, Math.max(t, p.loop.start + MIN_LOOP_SECONDS));
    setDrag({ kind: drag.kind, t });
    p.onScrub?.(t);
  };
  const onUp = (e: ReactPointerEvent) => {
    if (!drag) return;
    (e.currentTarget as HTMLElement).releasePointerCapture(e.pointerId);
    if (drag.kind === 'scrub') p.onSeek(timeAt(e.clientX));
    else if (p.loop) {
      if (drag.kind === 'a') p.onLoopChange(drag.t, p.loop.end);
      else p.onLoopChange(p.loop.start, drag.t);
    }
    setDrag(null);
    p.onScrub?.(null);
  };

  const loopA = drag?.kind === 'a' ? drag.t : p.loop?.start;
  const loopB = drag?.kind === 'b' ? drag.t : p.loop?.end;
  const playhead = drag?.kind === 'scrub' ? drag.t : p.position;

  return (
    <div
      ref={box}
      className="rk-wave"
      style={{ height, color }}
      data-scrubbing={drag?.kind === 'scrub' ? 'true' : undefined}
      aria-label={p.label ?? 'Waveform'}
      role="group"
      onPointerDown={onDown('scrub')}
      onPointerMove={onMove}
      onPointerUp={onUp}
      onPointerCancel={onUp}
      data-testid="waveform"
    >
      <canvas ref={canvas} aria-hidden="true" />
      <div className="rk-wave-played" style={{ width: pct(playhead) }} />
      {p.loop && loopA !== undefined && loopB !== undefined && (
        <>
          <div className="rk-wave-loop" data-enabled={p.loopEnabled ? 'true' : 'false'} style={{ left: pct(loopA), width: `calc(${pct(loopB)} - ${pct(loopA)})` }} />
          <div
            className="rk-loop-handle"
            data-enabled={p.loopEnabled ? 'true' : 'false'}
            style={{ left: pct(loopA) }}
            role="slider"
            tabIndex={p.disabled ? -1 : 0}
            aria-label="Loop start"
            aria-valuemin={0}
            aria-valuemax={p.duration}
            aria-valuenow={loopA}
            onPointerDown={onDown('a')}
            onPointerMove={onMove}
            onPointerUp={onUp}
            onKeyDown={(e) => handleKeys(e, loopA, (t) => p.onLoopChange(Math.max(0, Math.min(t, p.loop!.end - MIN_LOOP_SECONDS)), p.loop!.end), p.bpm)}
          >
            <b />
            <i />
          </div>
          <div
            className="rk-loop-handle"
            data-enabled={p.loopEnabled ? 'true' : 'false'}
            style={{ left: pct(loopB) }}
            role="slider"
            tabIndex={p.disabled ? -1 : 0}
            aria-label="Loop end"
            aria-valuemin={0}
            aria-valuemax={p.duration}
            aria-valuenow={loopB}
            onPointerDown={onDown('b')}
            onPointerMove={onMove}
            onPointerUp={onUp}
            onKeyDown={(e) => handleKeys(e, loopB, (t) => p.onLoopChange(p.loop!.start, Math.min(p.duration, Math.max(t, p.loop!.start + MIN_LOOP_SECONDS))), p.bpm)}
          >
            <b />
            <i />
          </div>
        </>
      )}
      <div className="rk-playhead" style={{ left: pct(playhead) }} />
    </div>
  );
}

function handleKeys(e: React.KeyboardEvent, value: number, set: (t: number) => void, bpm: number | null) {
  const step = bpm ? 60 / bpm : 1;
  const big = e.shiftKey ? step * 4 : step;
  if (e.key === 'ArrowLeft' || e.key === 'ArrowDown') {
    e.preventDefault();
    e.stopPropagation();
    set(value - big);
  } else if (e.key === 'ArrowRight' || e.key === 'ArrowUp') {
    e.preventDefault();
    e.stopPropagation();
    set(value + big);
  }
}
