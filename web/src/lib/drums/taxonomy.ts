/**
 * The drum editor's row taxonomy: six groups, twelve articulations. Mirrors
 * internal/drums/taxonomy.go — keys are the wire values of `art` in
 * edits/drums.json. MIDI keys are NOT here: they come from the profile the
 * server sends with the edit document (GM today).
 */

export type Articulation = 'kick' | 'snare' | 'stick' | 'hhc' | 'hho' | 'hhp' | 'tomh' | 'tomm' | 'tomf' | 'ride' | 'bell' | 'crash';
export type GroupKey = 'kick' | 'snare' | 'hh' | 'toms' | 'ride' | 'crash';

export interface ArticulationInfo {
  key: Articulation;
  label: string;
  group: GroupKey;
  /** Drawn hollow (a variant of the group's main voice). */
  hollow: boolean;
  /** Index within the group and the group's size, for row geometry. */
  index: number;
  count: number;
}

export interface GroupInfo {
  key: GroupKey;
  label: string;
  /** CSS custom property carrying the group colour. */
  colorVar: string;
  articulations: Articulation[];
}

export const GROUPS: GroupInfo[] = [
  { key: 'kick', label: 'Kick', colorVar: '--rk-color-drum-kick', articulations: ['kick'] },
  { key: 'snare', label: 'Snare', colorVar: '--rk-color-drum-snare', articulations: ['snare', 'stick'] },
  { key: 'hh', label: 'Hi-Hat', colorVar: '--rk-color-drum-hh', articulations: ['hhc', 'hho', 'hhp'] },
  { key: 'toms', label: 'Toms', colorVar: '--rk-color-drum-toms', articulations: ['tomh', 'tomm', 'tomf'] },
  { key: 'ride', label: 'Ride', colorVar: '--rk-color-drum-ride', articulations: ['ride', 'bell'] },
  { key: 'crash', label: 'Crash', colorVar: '--rk-color-drum-crash', articulations: ['crash'] },
];

const LABELS: Record<Articulation, string> = {
  kick: 'Kick', snare: 'Snare', stick: 'Side Stick', hhc: 'Closed', hho: 'Open', hhp: 'Pedal',
  tomh: 'High', tomm: 'Mid', tomf: 'Floor', ride: 'Bow', bell: 'Bell', crash: 'Crash',
};
const HOLLOW: Partial<Record<Articulation, true>> = { stick: true, hho: true, bell: true };

export const ARTICULATIONS: Record<Articulation, ArticulationInfo> = Object.fromEntries(
  GROUPS.flatMap((g) => g.articulations.map((a, i) => [a, { key: a, label: LABELS[a], group: g.key, hollow: Boolean(HOLLOW[a]), index: i, count: g.articulations.length }])),
) as Record<Articulation, ArticulationInfo>;

export const ARTICULATION_KEYS = Object.keys(ARTICULATIONS) as Articulation[];

export const GROUP: Record<GroupKey, GroupInfo> = Object.fromEntries(GROUPS.map((g) => [g.key, g])) as Record<GroupKey, GroupInfo>;

export function isArticulation(s: string): s is Articulation {
  return Object.prototype.hasOwnProperty.call(ARTICULATIONS, s);
}

/** "Snare · Side Stick", or just "Kick" for single-voice groups. */
export function articulationTitle(a: Articulation): string {
  const info = ARTICULATIONS[a];
  const g = GROUP[info.group];
  return g.articulations.length > 1 ? `${g.label} · ${info.label}` : g.label;
}

const NOTE_NAMES = ['C', 'C#', 'D', 'D#', 'E', 'F', 'F#', 'G', 'G#', 'A', 'A#', 'B'];
/** "C1 36" — MIDI key with its note name (C3 = 60 convention). */
export function midiKeyLabel(key: number): string {
  return `${NOTE_NAMES[key % 12]}${Math.floor(key / 12) - 2} ${key}`;
}

/** 0..1 → 1..127 as the editor shows it. */
export function velTo127(vel: number): number {
  return Math.max(1, Math.min(127, Math.round(vel * 127)));
}
/** 1..127 → 0..1 as the server stores it. */
export function velFrom127(v: number): number {
  return Math.max(1, Math.min(127, Math.round(v))) / 127;
}
