import { useLayoutEffect, useState, type RefObject } from 'react';

/** Width of an element, tracked through ResizeObserver. */
export function useElementSize(ref: RefObject<HTMLElement | null>): { width: number; height: number } {
  const [size, setSize] = useState({ width: 0, height: 0 });
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    const update = () => {
      const r = el.getBoundingClientRect();
      setSize((s) => (s.width === Math.round(r.width) && s.height === Math.round(r.height) ? s : { width: Math.round(r.width), height: Math.round(r.height) }));
    };
    const ro = new ResizeObserver(update);
    ro.observe(el);
    update();
    return () => ro.disconnect();
  }, [ref]);
  return size;
}

/** Size a canvas for the device pixel ratio and return a cleared 2D context in CSS pixels. */
export function prepareCanvas(canvas: HTMLCanvasElement | null, width: number, height: number): CanvasRenderingContext2D | null {
  if (!canvas || width <= 0 || height <= 0) return null;
  const dpr = window.devicePixelRatio || 1;
  const w = Math.round(width * dpr);
  const h = Math.round(height * dpr);
  if (canvas.width !== w || canvas.height !== h) {
    canvas.width = w;
    canvas.height = h;
  }
  const ctx = canvas.getContext('2d');
  if (!ctx) return null;
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  ctx.clearRect(0, 0, width, height);
  return ctx;
}

/** Theme colours the canvases need, read once per draw from the CSS variables. */
export interface Palette {
  ink: string;
  inkMuted: string;
  copper: string;
  wave: string;
  ledOff: string;
  drum: Record<string, string>;
}

const DRUM_VARS: Record<string, string> = {
  kick: '--rk-color-drum-kick',
  snare: '--rk-color-drum-snare',
  hh: '--rk-color-drum-hh',
  toms: '--rk-color-drum-toms',
  ride: '--rk-color-drum-ride',
  crash: '--rk-color-drum-crash',
};

export function readPalette(el: Element): Palette {
  const cs = getComputedStyle(el);
  const v = (name: string, fallback: string) => cs.getPropertyValue(name).trim() || fallback;
  const drum: Record<string, string> = {};
  for (const [k, name] of Object.entries(DRUM_VARS)) drum[k] = v(name, '#888');
  return {
    ink: v('--rk-color-ink', '#2f2a24'),
    inkMuted: v('--rk-color-ink-muted', '#726a5d'),
    copper: v('--rk-color-copper', '#a8724d'),
    wave: v('--rk-color-wave-main', '#8b8272'),
    ledOff: v('--rk-color-led-off', '#bbb2a3'),
    drum,
  };
}

/** Diamond hit marker path. */
export function diamond(ctx: CanvasRenderingContext2D, x: number, y: number, size: number): void {
  ctx.beginPath();
  ctx.moveTo(x, y - size);
  ctx.lineTo(x + size, y);
  ctx.lineTo(x, y + size);
  ctx.lineTo(x - size, y);
  ctx.closePath();
}
