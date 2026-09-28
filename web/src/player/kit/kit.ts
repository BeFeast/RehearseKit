import type { Articulation } from '../../lib/drums/taxonomy';

/** web/public/kit/kit.json — the browser preview kit (CC0 subset, see docs/rebuild/drum-kit.md). */
export interface KitLayer {
  file: string;
  /** Velocity range 1..127 this layer covers, inclusive. */
  vel: [number, number];
  /** Linear gain applied on top of the velocity curve. */
  gain: number;
}

export interface KitManifest {
  name: string;
  license: string;
  source: string;
  commit: string;
  sampleRate: number;
  articulations: Partial<Record<Articulation, KitLayer[]>>;
  substitutions?: Record<string, string>;
}

/** A decoded kit: one AudioBuffer per layer file. */
export interface LoadedKit {
  manifest: KitManifest;
  buffers: Map<string, AudioBuffer>;
}

export const KIT_URL = '/kit/kit.json';

/** Articulations that choke an open hi-hat when they sound. */
export const CHOKES: Partial<Record<Articulation, Articulation[]>> = { hhc: ['hho'], hhp: ['hho'] };

/**
 * Fetch the manifest and decode every layer on the given context (the
 * engine's, so buffers share its sample rate). Files are fetched once;
 * a failed file is skipped and reported through `missing`.
 */
export async function loadKit(ctx: BaseAudioContext, url = KIT_URL, fetchImpl: typeof fetch = (i, o) => fetch(i, o)): Promise<LoadedKit & { missing: string[] }> {
  const res = await fetchImpl(url, { credentials: 'same-origin' });
  if (!res.ok) throw new Error(`kit: ${url} → ${res.status}`);
  const manifest = (await res.json()) as KitManifest;
  const base = new URL(url, typeof location !== 'undefined' ? location.href : 'http://localhost/');
  const files = new Set<string>();
  for (const layers of Object.values(manifest.articulations)) for (const l of layers ?? []) files.add(l.file);
  const buffers = new Map<string, AudioBuffer>();
  const missing: string[] = [];
  await Promise.all(
    [...files].map(async (file) => {
      try {
        const r = await fetchImpl(new URL(file, base).toString(), { credentials: 'same-origin' });
        if (!r.ok) throw new Error(String(r.status));
        buffers.set(file, await ctx.decodeAudioData(await r.arrayBuffer()));
      } catch {
        missing.push(file);
      }
    }),
  );
  return { manifest, buffers, missing };
}

/** The layer covering a 1..127 velocity (the last layer when none matches). */
export function layerFor(layers: KitLayer[], vel127: number): KitLayer | null {
  if (layers.length === 0) return null;
  for (const l of layers) if (vel127 >= l.vel[0] && vel127 <= l.vel[1]) return l;
  return layers[layers.length - 1];
}

/**
 * Velocity → linear gain inside a layer: the layer already carries the
 * timbre of its range, so the curve only shades within it (soft end of a
 * layer 0.55, hard end 1.0) — the same shape the prototype used.
 */
export function velocityGain(vel127: number, layer: KitLayer): number {
  const [lo, hi] = layer.vel;
  const f = hi > lo ? (vel127 - lo) / (hi - lo) : 1;
  return (0.55 + 0.45 * Math.max(0, Math.min(1, f))) * layer.gain;
}
