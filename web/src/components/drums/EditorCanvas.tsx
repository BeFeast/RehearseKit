import { useCallback, useEffect, useRef, useState, type PointerEvent as ReactPointerEvent } from 'react';
import { columnsFor } from '../../lib/peaks';
import { AUDIO_LANE_H, articulationAt, laneY, rowAt } from '../../lib/drums/rows';
import { ARTICULATIONS, GROUPS, type Articulation } from '../../lib/drums/taxonomy';
import type { DrumEvent } from '../../lib/drums/types';
import type { Mixer } from '../../player/use-mixer';
import type { DrumEditorHandle } from '../../player/use-drum-editor';
import { diamond, prepareCanvas, readPalette, useElementSize } from './canvas';

const HIT_SIZE = 5.5;
const PICK_RADIUS = 8;

interface Marquee {
  x0: number;
  y0: number;
  x1: number;
  y1: number;
  keep: string[];
}

interface Drag {
  kind: 'move' | 'marquee' | 'erase';
  startX: number;
  startY: number;
  startT: number;
  hit: DrumEvent | null;
  base: Record<string, { t: number; art: Articulation }>;
  moved: boolean;
  single: boolean;
}

/**
 * The main editing surface: the drum stem's waveform at the viewport zoom
 * (from the .pk pyramid), the MIDI rows with grid lines, the hits as
 * diamonds tinted by velocity, selection, marquee and the playhead.
 * Pointer handling follows the tool: select/move (Shift adds, Alt bypasses
 * snap, double-click adds), draw (click adds), erase (click/drag removes).
 */
export function EditorCanvas({ ed, m }: { ed: DrumEditorHandle; m: Mixer }) {
  const host = useRef<HTMLDivElement>(null);
  const canvas = useRef<HTMLCanvasElement>(null);
  const { width } = useElementSize(host);
  const height = ed.layout.total;
  const [marquee, setMarquee] = useState<Marquee | null>(null);
  const drag = useRef<Drag | null>(null);
  const lastAdd = useRef<{ at: number; x: number; y: number } | null>(null);

  const { view, state, layout, grid, division } = ed;
  const drums = m.peaks.drums;
  const pos = m.live.current.position;
  const loop = m.mix.loop;
  const loopOn = m.mix.loopEnabled;
  const selSet = state.selection;

  const xOf = useCallback((t: number) => ((t - view.t0) / view.span) * width, [view, width]);
  const tOf = useCallback((x: number) => view.t0 + (x / width) * view.span, [view, width]);

  const hitXY = useCallback(
    (h: DrumEvent): { x: number; y: number } | null => {
      const y = laneY(layout, h.art);
      if (y === null) return null;
      return { x: xOf(h.t), y };
    },
    [layout, xOf],
  );

  const hitAt = useCallback(
    (x: number, y: number): DrumEvent | null => {
      let best: DrumEvent | null = null;
      let bd = PICK_RADIUS;
      const lo = tOf(x - PICK_RADIUS);
      const hi = tOf(x + PICK_RADIUS);
      for (const h of state.hits) {
        if (h.t < lo) continue;
        if (h.t > hi) break;
        const xy = hitXY(h);
        if (!xy) continue;
        const d = Math.hypot(xy.x - x, xy.y - y);
        if (d < bd) {
          bd = d;
          best = h;
        }
      }
      return best;
    },
    [state.hits, hitXY, tOf],
  );

  // ---- drawing ------------------------------------------------------------------
  useEffect(() => {
    const ctx = prepareCanvas(canvas.current, width, height);
    if (!ctx || !host.current) return;
    const pal = readPalette(host.current);
    const w = width;
    const sel = new Set(selSet);

    // Row backgrounds.
    for (const r of layout.rows) {
      if (r.type === 'midi') {
        ctx.fillStyle = 'rgba(0,0,0,.05)';
        ctx.fillRect(0, r.y, w, r.h);
      }
      if (r.type !== 'audio' && r.group === ed.focus) {
        ctx.fillStyle = 'rgba(168,114,77,.08)';
        ctx.fillRect(0, r.y, w, r.h);
      }
      if (r.type === 'group') {
        const n = GROUPS.find((g) => g.key === r.group)!.articulations.length;
        ctx.fillStyle = 'rgba(0,0,0,.05)';
        for (let i = 1; i < n; i++) ctx.fillRect(0, r.y + (r.h * i) / n, w, 1);
      }
      ctx.fillStyle = 'rgba(0,0,0,.14)';
      ctx.fillRect(0, r.y + r.h - 1, w, 1);
    }

    // Grid lines (beat grid when known, seconds otherwise).
    const t1 = view.t0 + view.span;
    if (grid.hasGrid) {
      const lines = grid.lines(view.t0, t1, division);
      const pxPerLine = lines.length > 1 ? w / lines.length : w;
      for (const l of lines) {
        if (l.kind === 'sub' && pxPerLine < 4) continue;
        ctx.fillStyle = l.kind === 'bar' ? 'rgba(0,0,0,.3)' : l.kind === 'beat' ? 'rgba(0,0,0,.16)' : 'rgba(0,0,0,.07)';
        ctx.fillRect(Math.round(xOf(l.t)), AUDIO_LANE_H, 1, height - AUDIO_LANE_H);
      }
    } else {
      const step = view.span > 20 ? 5 : 1;
      for (let t = Math.ceil(view.t0 / step) * step; t <= t1; t += step) {
        ctx.fillStyle = t % 10 === 0 ? 'rgba(0,0,0,.3)' : 'rgba(0,0,0,.12)';
        ctx.fillRect(Math.round(xOf(t)), AUDIO_LANE_H, 1, height - AUDIO_LANE_H);
      }
    }

    // Drum stem waveform at the viewport zoom (one column per pixel).
    if (drums && w > 0) {
      const rate = drums.sampleRate;
      const f0 = Math.max(0, Math.floor(view.t0 * rate));
      const f1 = Math.min(drums.frames, Math.ceil(t1 * rate));
      const x0 = Math.round(xOf(f0 / rate));
      const x1 = Math.round(xOf(f1 / rate));
      const cols = x1 - x0;
      if (cols > 0) {
        const columns = columnsFor(drums, f0, f1, cols);
        const mid = AUDIO_LANE_H / 2;
        const amp = AUDIO_LANE_H * 0.44;
        ctx.fillStyle = pal.drum.kick;
        ctx.globalAlpha = 0.7;
        for (let i = 0; i < columns.length; i++) {
          const { min, max } = columns[i];
          const top = mid - Math.max(0.6, max * amp);
          const bottom = mid - Math.min(-0.6, min * amp);
          ctx.fillRect(x0 + i, top, 1, Math.max(1.2, bottom - top));
        }
        ctx.globalAlpha = 1;
      }
      ctx.fillStyle = 'rgba(0,0,0,.2)';
      ctx.fillRect(0, AUDIO_LANE_H / 2 - 0.5, w, 1);
    }

    // Loop shading outside the region.
    if (loop) {
      ctx.fillStyle = loopOn ? 'rgba(0,0,0,.08)' : 'rgba(0,0,0,.04)';
      if (loop.start > view.t0) ctx.fillRect(0, 0, Math.max(0, Math.min(w, xOf(loop.start))), height);
      if (loop.end < t1) ctx.fillRect(Math.max(0, Math.min(w, xOf(loop.end))), 0, w, height);
    }

    // Hits: unselected first, selected on top.
    const lo = view.t0 - 0.05;
    const hi = t1 + 0.05;
    const later: DrumEvent[] = [];
    const draw = (h: DrumEvent, selected: boolean) => {
      const xy = hitXY(h);
      if (!xy) return;
      const info = ARTICULATIONS[h.art];
      const color = pal.drum[info.group];
      const size = h.vel < 40 / 127 ? HIT_SIZE - 1 : HIT_SIZE;
      const muted = ed.isRowMuted(h.art);
      ctx.globalAlpha = (0.45 + 0.55 * h.vel) * (muted ? 0.35 : 1);
      ctx.fillStyle = color;
      ctx.strokeStyle = color;
      ctx.lineWidth = 1.5;
      diamond(ctx, xy.x, xy.y, size);
      if (info.hollow) {
        ctx.stroke();
        ctx.globalAlpha *= 0.25;
        ctx.fill();
      } else ctx.fill();
      ctx.globalAlpha = 1;
      if (selected) {
        ctx.strokeStyle = pal.ink;
        ctx.lineWidth = 2;
        diamond(ctx, xy.x, xy.y, size);
        ctx.stroke();
        ctx.strokeStyle = pal.copper;
        ctx.lineWidth = 1;
        ctx.beginPath();
        ctx.arc(xy.x, xy.y, size + 3.5, 0, Math.PI * 2);
        ctx.stroke();
      }
    };
    for (const h of state.hits) {
      if (h.t < lo) continue;
      if (h.t > hi) break;
      if (sel.has(h.id)) later.push(h);
      else draw(h, false);
    }
    for (const h of later) draw(h, true);

    if (marquee) {
      const x = Math.min(marquee.x0, marquee.x1);
      const y = Math.min(marquee.y0, marquee.y1);
      const mw = Math.abs(marquee.x1 - marquee.x0);
      const mh = Math.abs(marquee.y1 - marquee.y0);
      ctx.fillStyle = 'rgba(168,114,77,.12)';
      ctx.strokeStyle = pal.copper;
      ctx.lineWidth = 1;
      ctx.setLineDash([4, 3]);
      ctx.fillRect(x, y, mw, mh);
      ctx.strokeRect(x + 0.5, y + 0.5, mw, mh);
      ctx.setLineDash([]);
    }

    const px = xOf(pos);
    if (px >= 0 && px <= w) {
      ctx.fillStyle = pal.ink;
      ctx.fillRect(Math.round(px) - 0.5, 0, 1.5, height);
    }
  }, [width, height, layout, view, grid, division, drums, loop, loopOn, state.hits, selSet, marquee, pos, ed.focus, ed.isRowMuted, hitXY, xOf, ed]);

  // ---- wheel: pan, Ctrl+wheel zoom (non-passive) ----------------------------------
  useEffect(() => {
    const el = host.current;
    if (!el) return;
    const onWheel = (e: WheelEvent) => {
      e.preventDefault();
      const r = el.getBoundingClientRect();
      const fx = (e.clientX - r.left) / r.width;
      if (e.ctrlKey || e.metaKey) ed.zoomAt(fx, Math.exp(e.deltaY * 0.002));
      else {
        const d = Math.abs(e.deltaX) > Math.abs(e.deltaY) ? e.deltaX : e.deltaY;
        ed.setView(ed.view.t0 + (d / r.width) * ed.view.span, ed.view.span);
      }
    };
    el.addEventListener('wheel', onWheel, { passive: false });
    return () => el.removeEventListener('wheel', onWheel);
  }, [ed]);

  // ---- pointer -------------------------------------------------------------------
  const point = (e: ReactPointerEvent) => {
    const r = host.current!.getBoundingClientRect();
    return { x: e.clientX - r.left, y: e.clientY - r.top };
  };

  const addAt = (x: number, y: number) => {
    const row = rowAt(layout, y);
    const art = articulationAt(row, y);
    if (!art) return;
    ed.ops.addHit(art, ed.snapTime(tOf(x)));
    lastAdd.current = { at: performance.now(), x, y };
  };

  const onDown = (e: ReactPointerEvent) => {
    if (ed.status !== 'ready' || width <= 0) return;
    e.preventDefault();
    (e.currentTarget as HTMLElement).setPointerCapture(e.pointerId);
    const { x, y } = point(e);
    const row = rowAt(layout, y);
    if (row && row.type !== 'audio') ed.setFocus(row.group);
    const hit = hitAt(x, y);
    if (ed.tool === 'erase') {
      ed.dispatch({ type: 'gestureBegin' });
      if (hit) ed.dispatch({ type: 'erase', id: hit.id });
      drag.current = { kind: 'erase', startX: x, startY: y, startT: tOf(x), hit: null, base: {}, moved: false, single: false };
      return;
    }
    if (!hit) {
      if (ed.tool === 'draw') {
        addAt(x, y);
        return;
      }
      // Double-click on empty space adds a hit (select tool).
      const la = lastAdd.current;
      if (e.detail >= 2 || (la && performance.now() - la.at < 400 && Math.hypot(la.x - x, la.y - y) < 6)) {
        addAt(x, y);
        return;
      }
      lastAdd.current = { at: performance.now(), x, y };
      const keep = e.shiftKey ? state.selection : [];
      if (!e.shiftKey) ed.dispatch({ type: 'select', ids: [], mode: 'replace' });
      drag.current = { kind: 'marquee', startX: x, startY: y, startT: tOf(x), hit: null, base: {}, moved: false, single: false };
      setMarquee({ x0: x, y0: y, x1: x, y1: y, keep });
      return;
    }
    // Select / move.
    let ids: string[];
    if (e.shiftKey) {
      ed.dispatch({ type: 'select', ids: [hit.id], mode: 'toggle' });
      ids = state.selection.includes(hit.id) ? state.selection.filter((i) => i !== hit.id) : [...state.selection, hit.id];
    } else if (!state.selection.includes(hit.id)) {
      ed.dispatch({ type: 'select', ids: [hit.id], mode: 'replace' });
      ids = [hit.id];
    } else {
      ed.dispatch({ type: 'select', ids: [hit.id], mode: 'add' });
      ids = state.selection;
    }
    ed.setFocus(ARTICULATIONS[hit.art].group);
    const base: Drag['base'] = {};
    for (const h of state.hits) if (ids.includes(h.id)) base[h.id] = { t: h.t, art: h.art };
    drag.current = { kind: 'move', startX: x, startY: y, startT: tOf(x), hit, base, moved: false, single: ids.length === 1 };
  };

  const onMove = (e: ReactPointerEvent) => {
    const d = drag.current;
    if (!d) return;
    const { x, y } = point(e);
    if (d.kind === 'erase') {
      const h = hitAt(x, y);
      if (h) ed.dispatch({ type: 'erase', id: h.id });
      return;
    }
    if (d.kind === 'marquee') {
      setMarquee((mq) => (mq ? { ...mq, x1: x, y1: y } : mq));
      return;
    }
    if (!d.moved && Math.abs(x - d.startX) < 3 && Math.abs(y - d.startY) < 3) return;
    if (!d.moved) {
      d.moved = true;
      ed.dispatch({ type: 'gestureBegin' });
    }
    let dt = tOf(x) - d.startT;
    if (ed.snap && !e.altKey && d.hit) dt = ed.snapTime(d.hit.t + dt, true) - d.hit.t;
    let art: Articulation | null = null;
    if (d.single) {
      const row = rowAt(layout, y);
      art = articulationAt(row, y);
    }
    ed.dispatch({ type: 'moveTo', base: d.base, dt, art });
  };

  const onUp = (e: ReactPointerEvent) => {
    const d = drag.current;
    drag.current = null;
    (e.currentTarget as HTMLElement).releasePointerCapture(e.pointerId);
    if (!d) return;
    if (d.kind === 'erase') {
      ed.dispatch({ type: 'gestureEnd' });
      return;
    }
    if (d.kind === 'marquee') {
      setMarquee((mq) => {
        if (mq) {
          const xa = Math.min(mq.x0, mq.x1);
          const xb = Math.max(mq.x0, mq.x1);
          const ya = Math.min(mq.y0, mq.y1);
          const yb = Math.max(mq.y0, mq.y1);
          if (xb - xa > 2 || yb - ya > 2) {
            const ids: string[] = [];
            for (const h of state.hits) {
              const xy = hitXY(h);
              if (xy && xy.x >= xa && xy.x <= xb && xy.y >= ya && xy.y <= yb) ids.push(h.id);
            }
            ed.dispatch({ type: 'select', ids: [...mq.keep, ...ids], mode: 'replace' });
          }
        }
        return null;
      });
      return;
    }
    if (d.moved) ed.dispatch({ type: 'gestureEnd' });
  };

  return (
    <div
      ref={host}
      className="rk-drums-main"
      data-tool={ed.tool}
      style={{ height }}
      onPointerDown={onDown}
      onPointerMove={onMove}
      onPointerUp={onUp}
      onPointerCancel={onUp}
      role="application"
      aria-label="Drum hits"
      data-testid="drums-canvas"
    >
      <canvas ref={canvas} aria-hidden="true" />
    </div>
  );
}
