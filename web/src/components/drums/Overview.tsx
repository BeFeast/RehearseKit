import { useEffect, useRef, useState, type PointerEvent as ReactPointerEvent } from 'react';
import { columnsFor } from '../../lib/peaks';
import type { Mixer } from '../../player/use-mixer';
import type { DrumEditorHandle } from '../../player/use-drum-editor';
import { useTheme } from '../../lib/use-theme';
import { prepareCanvas, readPalette, useElementSize } from './canvas';

/** The whole drum stem in one strip with the loop, the viewport frame and the playhead; drag to scroll the view. */
export function Overview({ ed, m }: { ed: DrumEditorHandle; m: Mixer }) {
  const host = useRef<HTMLDivElement>(null);
  const canvas = useRef<HTMLCanvasElement>(null);
  const { width, height } = useElementSize(host);
  const theme = useTheme();
  const [dragging, setDragging] = useState(false);
  const drums = m.peaks.drums;
  const duration = m.duration;
  const { view } = ed;
  const pos = m.live.current.position;
  const loop = m.mix.loop;

  useEffect(() => {
    void theme; // redraw when the theme switches (colours come from CSS variables)
    const ctx = prepareCanvas(canvas.current, width, height);
    if (!ctx || !host.current || duration <= 0) return;
    const pal = readPalette(host.current);
    const w = width;
    const h = height;
    const mid = h / 2;
    if (drums) {
      const n = Math.max(1, Math.floor(w / 3));
      const cols = columnsFor(drums, 0, drums.frames, n);
      ctx.fillStyle = pal.wave;
      for (let i = 0; i < cols.length; i++) {
        const { min, max } = cols[i];
        const top = mid - Math.max(0.5, max * (h * 0.44));
        const bottom = mid - Math.min(-0.5, min * (h * 0.44));
        ctx.fillRect(i * 3, top, 2, Math.max(1, bottom - top));
      }
    }
    if (loop) {
      ctx.fillStyle = 'rgba(168,114,77,.14)';
      ctx.fillRect((loop.start / duration) * w, 0, ((loop.end - loop.start) / duration) * w, h);
    }
    const vx = (view.t0 / duration) * w;
    const vw = Math.max(3, (view.span / duration) * w);
    ctx.fillStyle = 'rgba(255,255,255,.18)';
    ctx.fillRect(vx, 0, vw, h);
    ctx.strokeStyle = pal.copper;
    ctx.lineWidth = 1.5;
    ctx.strokeRect(vx + 0.75, 0.75, vw - 1.5, h - 1.5);
    ctx.fillStyle = pal.ink;
    ctx.fillRect((pos / duration) * w - 0.5, 0, 1.5, h);
  }, [theme, width, height, drums, duration, view, pos, loop]);

  const go = (e: ReactPointerEvent) => {
    const r = host.current!.getBoundingClientRect();
    const t = ((e.clientX - r.left) / r.width) * duration;
    ed.setView(t - view.span / 2, view.span);
  };

  return (
    <div
      ref={host}
      className="rk-drums-overview"
      aria-label="Drum stem overview"
      role="group"
      onPointerDown={(e) => {
        e.preventDefault();
        (e.currentTarget as HTMLElement).setPointerCapture(e.pointerId);
        setDragging(true);
        go(e);
      }}
      onPointerMove={(e) => dragging && go(e)}
      onPointerUp={(e) => {
        (e.currentTarget as HTMLElement).releasePointerCapture(e.pointerId);
        setDragging(false);
      }}
      onPointerCancel={() => setDragging(false)}
      data-testid="drums-overview"
    >
      <canvas ref={canvas} aria-hidden="true" />
    </div>
  );
}
