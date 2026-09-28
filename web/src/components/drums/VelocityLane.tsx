import { useEffect, useRef, type PointerEvent as ReactPointerEvent } from 'react';
import { ARTICULATIONS, GROUP, velFrom127 } from '../../lib/drums/taxonomy';
import type { DrumEvent } from '../../lib/drums/types';
import type { Mixer } from '../../player/use-mixer';
import type { DrumEditorHandle } from '../../player/use-drum-editor';
import { useTheme } from '../../lib/use-theme';
import { prepareCanvas, readPalette, useElementSize } from './canvas';

const PAD = 4;

/** Velocity bars of the focused group's hits in view; click or drag to draw new values (one undo step per drag). */
export function VelocityLane({ ed, m }: { ed: DrumEditorHandle; m: Mixer }) {
  const host = useRef<HTMLDivElement>(null);
  const canvas = useRef<HTMLCanvasElement>(null);
  const { width, height } = useElementSize(host);
  const theme = useTheme();
  const drawing = useRef(false);
  const { view, state, focus } = ed;
  const pos = m.live.current.position;
  const group = GROUP[focus];
  const hits = state.hits;
  const selection = state.selection;

  useEffect(() => {
    void theme; // redraw when the theme switches (colours come from CSS variables)
    const ctx = prepareCanvas(canvas.current, width, height);
    if (!ctx || !host.current) return;
    const pal = readPalette(host.current);
    const w = width;
    const h = height;
    const xOf = (t: number) => ((t - view.t0) / view.span) * w;
    for (const lv of [32, 64, 96]) {
      ctx.fillStyle = pal.line(0.08);
      ctx.fillRect(0, Math.round(PAD + (1 - lv / 127) * (h - 2 * PAD)), w, 1);
    }
    ctx.fillStyle = pal.inkMuted;
    ctx.font = '500 7.5px JetBrains Mono, monospace';
    ctx.textBaseline = 'top';
    ctx.fillText('127', 4, 3);
    ctx.textBaseline = 'bottom';
    ctx.fillText('1', 4, h - 2);
    const bw = Math.max(2, Math.min(6, (0.06 / view.span) * w));
    const sel = new Set(selection);
    const later: { x: number; bh: number }[] = [];
    const color = pal.drum[focus];
    for (const hit of hits) {
      if (hit.t < view.t0 - 0.05) continue;
      if (hit.t > view.t0 + view.span + 0.05) break;
      if (ARTICULATIONS[hit.art].group !== focus) continue;
      const x = xOf(hit.t);
      const bh = hit.vel * (h - 2 * PAD);
      if (sel.has(hit.id)) {
        later.push({ x, bh });
        continue;
      }
      ctx.fillStyle = color;
      ctx.globalAlpha = 0.85;
      ctx.fillRect(x - bw / 2, h - PAD - bh, bw, bh);
      ctx.globalAlpha = 1;
      ctx.beginPath();
      ctx.arc(x, h - PAD - bh, 2.2, 0, Math.PI * 2);
      ctx.fill();
    }
    for (const { x, bh } of later) {
      ctx.fillStyle = pal.ink;
      ctx.fillRect(x - bw / 2, h - PAD - bh, bw, bh);
      ctx.strokeStyle = pal.copper;
      ctx.lineWidth = 1;
      ctx.beginPath();
      ctx.arc(x, h - PAD - bh, 3.5, 0, Math.PI * 2);
      ctx.stroke();
    }
    const px = xOf(pos);
    if (px >= 0 && px <= w) {
      ctx.fillStyle = pal.ink;
      ctx.globalAlpha = 0.6;
      ctx.fillRect(Math.round(px) - 0.5, 0, 1.5, h);
      ctx.globalAlpha = 1;
    }
  }, [theme, width, height, view, hits, selection, focus, pos]);

  const apply = (e: ReactPointerEvent, first: boolean) => {
    const r = host.current!.getBoundingClientRect();
    const x = e.clientX - r.left;
    const y = e.clientY - r.top;
    const v127 = Math.max(1, Math.min(127, Math.round((1 - (y - PAD) / (r.height - 2 * PAD)) * 127)));
    const tol = first ? 6 : 4;
    const vels: Record<string, number> = {};
    let touched = false;
    for (const hit of hits) {
      if (ARTICULATIONS[hit.art].group !== focus) continue;
      const hx = ((hit.t - view.t0) / view.span) * r.width;
      if (Math.abs(hx - x) <= tol) {
        vels[hit.id] = velFrom127(v127);
        touched = true;
      }
    }
    if (touched) ed.dispatch({ type: 'velDraw', vels });
  };

  return (
    <div
      ref={host}
      className="rk-drums-vel"
      role="group"
      aria-label={`Velocity of ${group.label}`}
      onPointerDown={(e) => {
        if (ed.status !== 'ready') return;
        e.preventDefault();
        (e.currentTarget as HTMLElement).setPointerCapture(e.pointerId);
        drawing.current = true;
        ed.dispatch({ type: 'gestureBegin' });
        apply(e, true);
      }}
      onPointerMove={(e) => drawing.current && apply(e, false)}
      onPointerUp={(e) => {
        (e.currentTarget as HTMLElement).releasePointerCapture(e.pointerId);
        if (!drawing.current) return;
        drawing.current = false;
        ed.dispatch({ type: 'gestureEnd' });
      }}
      onPointerCancel={() => {
        if (!drawing.current) return;
        drawing.current = false;
        ed.dispatch({ type: 'gestureEnd' });
      }}
      data-testid="drums-velocity"
    >
      <canvas ref={canvas} aria-hidden="true" />
    </div>
  );
}

export type { DrumEvent };
