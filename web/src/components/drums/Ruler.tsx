import { useEffect, useRef, useState, type PointerEvent as ReactPointerEvent } from 'react';
import { formatTimecode } from '../../lib/format';
import type { Mixer } from '../../player/use-mixer';
import type { DrumEditorHandle } from '../../player/use-drum-editor';
import { useTheme } from '../../lib/use-theme';
import { prepareCanvas, readPalette, useElementSize } from './canvas';

/** Bars/beats (from the grid) and seconds above the rows; click or drag to seek. */
export function Ruler({ ed, m }: { ed: DrumEditorHandle; m: Mixer }) {
  const host = useRef<HTMLDivElement>(null);
  const canvas = useRef<HTMLCanvasElement>(null);
  const { width, height } = useElementSize(host);
  const theme = useTheme();
  const [dragging, setDragging] = useState(false);
  const { view, grid } = ed;
  const pos = m.live.current.position;
  const loop = m.mix.loop;

  useEffect(() => {
    void theme; // redraw when the theme switches (colours come from CSS variables)
    const ctx = prepareCanvas(canvas.current, width, height);
    if (!ctx || !host.current) return;
    const pal = readPalette(host.current);
    const w = width;
    const h = height;
    const xOf = (t: number) => ((t - view.t0) / view.span) * w;
    const t1 = view.t0 + view.span;
    ctx.textBaseline = 'top';
    if (grid.hasGrid) {
      const lines = grid.lines(view.t0, t1, '1/8').filter((l) => l.kind !== 'sub');
      const beats = lines.length || 1;
      const pxBeat = w / beats;
      for (const l of lines) {
        const x = Math.round(xOf(l.t));
        const isBar = l.kind === 'bar';
        ctx.fillStyle = isBar ? pal.line(0.4) : pal.line(0.18);
        ctx.fillRect(x, isBar ? 0 : 6, 1, isBar ? 14 : 8);
        const p = grid.position(l.t);
        if (!p) continue;
        if (isBar) {
          ctx.fillStyle = pal.ink;
          ctx.font = '700 9.5px JetBrains Mono, monospace';
          ctx.fillText(String(p.bar), x + 4, 2);
        } else if (pxBeat > 34) {
          ctx.fillStyle = pal.inkMuted;
          ctx.font = '500 8px JetBrains Mono, monospace';
          ctx.fillText(`${p.bar}.${p.beat}`, x + 3, 3);
        }
      }
    }
    // Seconds.
    const steps = [0.1, 0.25, 0.5, 1, 2, 5, 10, 30, 60];
    const step = steps.find((s) => (s / view.span) * w >= 56) ?? 60;
    ctx.fillStyle = pal.line(0.16);
    ctx.fillRect(0, 15, w, 1);
    for (let t = Math.ceil(view.t0 / step) * step; t <= t1; t += step) {
      const x = Math.round(xOf(t));
      ctx.fillStyle = pal.line(0.25);
      ctx.fillRect(x, 16, 1, 5);
      ctx.fillStyle = pal.inkMuted;
      ctx.font = '500 8px JetBrains Mono, monospace';
      ctx.fillText(step < 1 ? `${t.toFixed(2)}s` : formatTimecode(t), x + 3, 18);
    }
    if (loop) {
      const la = Math.max(0, Math.min(w, xOf(loop.start)));
      const lb = Math.max(0, Math.min(w, xOf(loop.end)));
      if (lb > la) {
        ctx.fillStyle = pal.copper;
        ctx.globalAlpha = m.mix.loopEnabled ? 0.5 : 0.25;
        ctx.fillRect(la, h - 3, lb - la, 3);
        ctx.globalAlpha = 1;
      }
    }
    const px = xOf(pos);
    if (px >= 0 && px <= w) {
      ctx.fillStyle = pal.ink;
      ctx.beginPath();
      ctx.moveTo(px - 5, 0);
      ctx.lineTo(px + 5, 0);
      ctx.lineTo(px, 7);
      ctx.closePath();
      ctx.fill();
      ctx.fillRect(Math.round(px) - 0.5, 0, 1.5, h);
    }
  }, [theme, width, height, view, grid, pos, loop, m.mix.loopEnabled]);

  const seekAt = (e: ReactPointerEvent) => {
    const r = host.current!.getBoundingClientRect();
    const t = view.t0 + ((e.clientX - r.left) / r.width) * view.span;
    m.seek(Math.max(0, t));
  };

  return (
    <div
      ref={host}
      className="rk-drums-ruler"
      role="slider"
      aria-label="Position"
      aria-valuemin={0}
      aria-valuemax={m.duration}
      aria-valuenow={pos}
      tabIndex={-1}
      onPointerDown={(e) => {
        e.preventDefault();
        (e.currentTarget as HTMLElement).setPointerCapture(e.pointerId);
        setDragging(true);
        seekAt(e);
      }}
      onPointerMove={(e) => dragging && seekAt(e)}
      onPointerUp={(e) => {
        (e.currentTarget as HTMLElement).releasePointerCapture(e.pointerId);
        setDragging(false);
      }}
      onPointerCancel={() => setDragging(false)}
      data-testid="drums-ruler"
    >
      <canvas ref={canvas} aria-hidden="true" />
    </div>
  );
}
