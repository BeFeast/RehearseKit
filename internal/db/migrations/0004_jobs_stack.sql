-- #35 model stacks by entitlement. stack = the entitlement the job was created
-- under (public | internal); model = the separation recipe it runs
-- (internal/models/manifest.json). Existing jobs all ran Demucs, so they are
-- internal. The defaults keep an older server able to insert jobs after a
-- rollback (it only runs Demucs, which is internal); the new server always
-- sets both. A public job can never carry a Demucs or BS-RoFormer SW recipe.
ALTER TABLE jobs ADD COLUMN stack text NOT NULL DEFAULT 'internal' CHECK (stack IN ('public', 'internal'));
ALTER TABLE jobs ADD COLUMN model text;
UPDATE jobs SET model = CASE quality WHEN 'high' THEN 'htdemucs_ft' WHEN 'high6' THEN 'htdemucs_6s' ELSE 'htdemucs' END;
ALTER TABLE jobs DROP CONSTRAINT jobs_quality_check;
ALTER TABLE jobs ADD CONSTRAINT jobs_quality_check CHECK (quality IN ('fast', 'high', 'high6', 'hifi'));
ALTER TABLE jobs ADD CONSTRAINT jobs_public_model_check CHECK (
    stack = 'internal' OR (model IS NOT NULL AND model NOT IN ('htdemucs', 'htdemucs_ft', 'htdemucs_6s', 'kim+bs_rofo_sw')));
