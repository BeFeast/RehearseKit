-- 0001_init: RehearseKit rebuild schema (design contract, data model section).
-- Enums are CHECK constraints so they can be extended without ALTER TYPE.

CREATE EXTENSION IF NOT EXISTS citext WITH SCHEMA public;

CREATE TABLE users (
    id            uuid PRIMARY KEY,
    email         public.citext NOT NULL UNIQUE,
    name          text NOT NULL DEFAULT '',
    avatar_url    text,
    provider      text NOT NULL CHECK (provider IN ('password', 'google')),
    password_hash text,
    role          text NOT NULL DEFAULT 'user' CHECK (role IN ('user', 'admin')),
    status        text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'active', 'inactive')),
    created_at    timestamptz NOT NULL DEFAULT now(),
    last_login_at timestamptz
);

CREATE TABLE sessions (
    id         bytea PRIMARY KEY,
    user_id    uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    user_agent text NOT NULL DEFAULT ''
);
CREATE INDEX sessions_user_id_idx ON sessions (user_id);
CREATE INDEX sessions_expires_at_idx ON sessions (expires_at);

CREATE TABLE jobs (
    id               uuid PRIMARY KEY,
    owner_id         uuid REFERENCES users (id),
    claim_token_hash bytea,
    project_name     text NOT NULL,
    input_type       text NOT NULL CHECK (input_type IN ('upload', 'youtube')),
    input_url        text,
    source_filename  text,
    quality          text NOT NULL CHECK (quality IN ('fast', 'high', 'high6')),
    status           text NOT NULL DEFAULT 'pending' CHECK (status IN (
                         'pending', 'converting', 'analyzing', 'separating',
                         'finalizing', 'packaging', 'completed', 'failed', 'cancelled')),
    stage_progress   smallint NOT NULL DEFAULT 0 CHECK (stage_progress BETWEEN 0 AND 100),
    error            text,
    detected_bpm     numeric(6, 2),
    duration_seconds numeric(10, 3),
    sample_rate      integer,
    channels         smallint,
    created_at       timestamptz NOT NULL DEFAULT now(),
    started_at       timestamptz,
    completed_at     timestamptz,
    expires_at       timestamptz NOT NULL
);
CREATE INDEX jobs_pending_idx ON jobs (created_at) WHERE status = 'pending';
CREATE INDEX jobs_owner_created_idx ON jobs (owner_id, created_at DESC);
CREATE INDEX jobs_expires_at_idx ON jobs (expires_at);

CREATE TABLE stems (
    job_id      uuid NOT NULL REFERENCES jobs (id) ON DELETE CASCADE,
    name        text NOT NULL CHECK (name IN ('vocals', 'drums', 'bass', 'other', 'guitar', 'piano')),
    path        text NOT NULL,
    bytes       bigint NOT NULL,
    frames      bigint NOT NULL,
    sample_rate integer NOT NULL,
    bit_depth   smallint NOT NULL,
    channels    smallint NOT NULL,
    peaks_path  text,
    PRIMARY KEY (job_id, name)
);

CREATE TABLE job_events (
    id       bigserial PRIMARY KEY,
    job_id   uuid NOT NULL REFERENCES jobs (id) ON DELETE CASCADE,
    at       timestamptz NOT NULL DEFAULT now(),
    status   text NOT NULL,
    progress smallint NOT NULL DEFAULT 0 CHECK (progress BETWEEN 0 AND 100),
    message  text NOT NULL DEFAULT ''
);
CREATE INDEX job_events_job_id_id_idx ON job_events (job_id, id);

CREATE TABLE mix_states (
    job_id     uuid NOT NULL REFERENCES jobs (id) ON DELETE CASCADE,
    subject    text NOT NULL,
    state      jsonb NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (job_id, subject)
);

CREATE TABLE gpu_leases (
    id           uuid PRIMARY KEY,
    job_id       uuid NOT NULL REFERENCES jobs (id) ON DELETE CASCADE,
    runner_id    text NOT NULL,
    leased_at    timestamptz NOT NULL DEFAULT now(),
    heartbeat_at timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL,
    state        text NOT NULL DEFAULT 'active' CHECK (state IN ('active', 'completed', 'failed', 'expired'))
);
CREATE INDEX gpu_leases_job_id_idx ON gpu_leases (job_id);
CREATE INDEX gpu_leases_active_idx ON gpu_leases (expires_at) WHERE state = 'active';
