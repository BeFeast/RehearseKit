import { ARTICULATIONS, GROUPS, type Articulation, type GroupKey } from './taxonomy';

/** Row heights in CSS pixels (design: DRUM EDITOR tab, rows panel). */
export const AUDIO_LANE_H = 72;
export const GROUP_ROW_H = 44;
export const MIDI_ROW_H = 28;

/**
 * One drawn row of the editor: the single drum audio lane, a collapsed
 * group (its articulations stacked as sub-lanes), or one articulation of an
 * expanded group.
 */
export type Row =
  | { type: 'audio'; key: 'audio'; y: number; h: number }
  | { type: 'group'; key: GroupKey; group: GroupKey; y: number; h: number }
  | { type: 'midi'; key: Articulation; group: GroupKey; art: Articulation; y: number; h: number };

export interface RowLayout {
  rows: Row[];
  total: number;
}

/** The rows for the given expansion state, with their vertical geometry. */
export function layoutRows(open: Partial<Record<GroupKey, boolean>>): RowLayout {
  const rows: Row[] = [{ type: 'audio', key: 'audio', y: 0, h: AUDIO_LANE_H }];
  let y = AUDIO_LANE_H;
  for (const g of GROUPS) {
    if (g.articulations.length > 1 && open[g.key]) {
      for (const a of g.articulations) {
        rows.push({ type: 'midi', key: a, group: g.key, art: a, y, h: MIDI_ROW_H });
        y += MIDI_ROW_H;
      }
    } else {
      rows.push({ type: 'group', key: g.key, group: g.key, y, h: GROUP_ROW_H });
      y += GROUP_ROW_H;
    }
  }
  return { rows, total: y };
}

/** Vertical centre of the lane an articulation is drawn on, or null when hidden. */
export function laneY(layout: RowLayout, art: Articulation): number | null {
  const info = ARTICULATIONS[art];
  for (const r of layout.rows) {
    if (r.type === 'midi' && r.art === art) return r.y + r.h / 2;
    if (r.type === 'group' && r.group === info.group) return r.y + (r.h * (info.index + 0.5)) / info.count;
  }
  return null;
}

/** The row under a y coordinate. */
export function rowAt(layout: RowLayout, y: number): Row | null {
  for (const r of layout.rows) if (y >= r.y && y < r.y + r.h) return r;
  return null;
}

/** The articulation a click at (row, y) targets: the sub-lane of a collapsed group, or the row's own. */
export function articulationAt(row: Row | null, y: number): Articulation | null {
  if (!row || row.type === 'audio') return null;
  if (row.type === 'midi') return row.art;
  const g = GROUPS.find((x) => x.key === row.group)!;
  const n = g.articulations.length;
  const i = Math.max(0, Math.min(n - 1, Math.floor(((y - row.y) / row.h) * n)));
  return g.articulations[i];
}
