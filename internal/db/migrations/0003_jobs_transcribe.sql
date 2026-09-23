-- Transcribe S1: owner-only option that adds beat grid + per-stem MIDI to a
-- high6 job. Additive, default false, so an older server keeps working.
ALTER TABLE jobs ADD COLUMN transcribe boolean NOT NULL DEFAULT false;
