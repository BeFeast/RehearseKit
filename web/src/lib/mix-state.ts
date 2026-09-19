/**
 * Per-job mix state: what the mixer remembers between visits. Kept small and
 * serialisable — it is PUT to /jobs/{id}/mix (debounced) and mirrored in
 * localStorage so the state survives the API not having the route yet.
 */

export interface StemMix {
  /** Fader position 0..1 (see lib/decibel). */
  position: number;
  muted: boolean;
}

export interface MixState {
  version: 1;
  stems: Record<string, StemMix>;
  /** Index of the soloed stem by name, or null. */
  solo: string | null;
  loop: { start: number; end: number } | null;
  loopEnabled: boolean;
  /** Selected strip by stem name; null = master. */
  selected: string | null;
  masterPosition: number;
}

export type MixAction =
  | { type: 'gain'; stem: string; position: number }
  | { type: 'mute'; stem: string; muted: boolean }
  | { type: 'solo'; stem: string | null }
  | { type: 'loop'; loop: { start: number; end: number } | null }
  | { type: 'loopEnabled'; enabled: boolean }
  | { type: 'select'; stem: string | null }
  | { type: 'master'; position: number }
  | { type: 'reset'; stems: string[]; unity: number }
  | { type: 'load'; state: MixState };

export function initialMix(stems: string[], unity: number): MixState {
  const s: Record<string, StemMix> = {};
  for (const name of stems) s[name] = { position: unity, muted: false };
  return { version: 1, stems: s, solo: null, loop: null, loopEnabled: false, selected: null, masterPosition: unity };
}

export function mixReducer(state: MixState, action: MixAction): MixState {
  switch (action.type) {
    case 'gain': {
      const cur = state.stems[action.stem];
      if (!cur) return state;
      const position = clamp01(action.position);
      if (cur.position === position) return state;
      return { ...state, stems: { ...state.stems, [action.stem]: { ...cur, position } } };
    }
    case 'mute': {
      const cur = state.stems[action.stem];
      if (!cur || cur.muted === action.muted) return state;
      return { ...state, stems: { ...state.stems, [action.stem]: { ...cur, muted: action.muted } } };
    }
    case 'solo':
      if (state.solo === action.stem) return state;
      if (action.stem !== null && !state.stems[action.stem]) return state;
      return { ...state, solo: action.stem };
    case 'loop':
      if (action.loop && action.loop.end <= action.loop.start) return state;
      return { ...state, loop: action.loop, loopEnabled: action.loop ? state.loopEnabled : false };
    case 'loopEnabled':
      if (!state.loop && action.enabled) return state;
      if (state.loopEnabled === action.enabled) return state;
      return { ...state, loopEnabled: action.enabled };
    case 'select':
      if (state.selected === action.stem) return state;
      return { ...state, selected: action.stem };
    case 'master':
      return { ...state, masterPosition: clamp01(action.position) };
    case 'reset':
      return initialMix(action.stems, action.unity);
    case 'load':
      return sanitize(action.state, Object.keys(state.stems), state.stems[Object.keys(state.stems)[0]]?.position ?? 1);
    default:
      return state;
  }
}

/** Is stem silenced by another stem's solo? (Distinct from muted.) */
export function isSilenced(state: MixState, stem: string): boolean {
  return state.solo !== null && state.solo !== stem;
}

function clamp01(x: number): number {
  return Number.isFinite(x) ? Math.max(0, Math.min(1, x)) : 0;
}

/** Bring untrusted state (API or localStorage) into shape for these stems. */
export function sanitize(raw: unknown, stems: string[], unity: number): MixState {
  const base = initialMix(stems, unity);
  if (!raw || typeof raw !== 'object') return base;
  const r = raw as Partial<MixState>;
  const out: MixState = { ...base };
  if (r.stems && typeof r.stems === 'object') {
    for (const name of stems) {
      const s = (r.stems as Record<string, Partial<StemMix>>)[name];
      if (s && typeof s === 'object') {
        out.stems[name] = {
          position: typeof s.position === 'number' ? clamp01(s.position) : unity,
          muted: Boolean(s.muted),
        };
      }
    }
  }
  out.solo = typeof r.solo === 'string' && stems.includes(r.solo) ? r.solo : null;
  if (r.loop && typeof r.loop === 'object' && typeof r.loop.start === 'number' && typeof r.loop.end === 'number' && r.loop.end > r.loop.start) {
    out.loop = { start: Math.max(0, r.loop.start), end: r.loop.end };
  }
  out.loopEnabled = Boolean(r.loopEnabled) && out.loop !== null;
  out.selected = typeof r.selected === 'string' && stems.includes(r.selected) ? r.selected : null;
  out.masterPosition = typeof r.masterPosition === 'number' ? clamp01(r.masterPosition) : unity;
  return out;
}
