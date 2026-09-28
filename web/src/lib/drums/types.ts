import type { Articulation } from './taxonomy';

/** One hit as stored in edits/drums.json (and as the editor holds it). */
export interface DrumEvent {
  id: string;
  art: Articulation;
  /** Absolute seconds. Untouched model hits keep the notes file's value bit for bit. */
  t: number;
  /** 0..1 (the editor shows round(vel*127)). */
  vel: number;
  src: 'model' | 'manual';
  /** Index into notes/drums.json for src = 'model'. */
  model?: number;
}

export interface ModelRev {
  adapter?: string;
  model?: string;
  count: number;
  notes_sha256: string;
}

/** edits/drums.json. */
export interface DrumDoc {
  version: 1;
  stem: 'drums';
  model_rev: ModelRev;
  edit_rev: number;
  exported_rev: number;
  profile: string;
  updated_at: string;
  events: DrumEvent[];
}

export interface DrumProfile {
  id: string;
  name: string;
  notes: Record<Articulation, number>;
}

export interface TimeSignature {
  beat: number;
  numerator: number;
  denominator: number;
}

/** grid.Map.Export() — see internal/pipeline/grid. */
export interface GridExport {
  constant: boolean;
  offset: number;
  median: number;
  beats: number[];
  bpm: number;
  numerator: number;
  time_signatures: TimeSignature[];
}

/** GET /jobs/{id}/drums/edits. */
export interface DrumEditsResponse {
  doc: DrumDoc;
  profile: DrumProfile;
  grid: GridExport | null;
  grid_error?: string;
  model: ModelRev;
  stale_model: boolean;
  duration: number;
  warnings: string[];
}

/** PUT /jobs/{id}/drums/edits → 200. */
export interface DrumSaveResponse {
  edit_rev: number;
  updated_at: string;
}

/** Thrown by the API layer on 409 edit_conflict; carries the server's revision. */
export class EditConflict extends Error {
  constructor(public readonly editRev: number) {
    super(`edit revision ${editRev} is current on the server`);
    this.name = 'EditConflict';
  }
}

/** analysis.json (only what the editor reads). */
export interface AnalysisSummary {
  version: number;
  grid: { beats: number[]; downbeats: number[]; source: string; model?: string } | null;
  grid_error?: string;
  instruments: Record<string, { status: 'ok' | 'failed' | 'skipped'; reason?: string; adapter?: string; model?: string; notes: number; seconds?: number }>;
  runner?: string;
  device?: string;
}
