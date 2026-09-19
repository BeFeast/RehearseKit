-- 0002: at most one active GPU lease per job. Claiming runs under FOR UPDATE
-- SKIP LOCKED on the jobs row, but a NOT EXISTS subquery is not re-checked
-- after the lock wait in READ COMMITTED, so the database enforces the
-- invariant and the claimer retries on a unique violation.

CREATE UNIQUE INDEX gpu_leases_one_active_idx ON gpu_leases (job_id) WHERE state = 'active';
