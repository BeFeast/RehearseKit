/** Wire types for /api/v1 (snake_case, as the Go handlers emit them). */

export type JobStatus =
  | 'pending'
  | 'converting'
  | 'analyzing'
  | 'separating'
  | 'finalizing'
  | 'packaging'
  | 'completed'
  | 'failed'
  | 'cancelled';

export type Quality = 'fast' | 'high' | 'high6';

export type StemName = 'vocals' | 'drums' | 'bass' | 'other' | 'guitar' | 'piano';

export interface Stem {
  name: StemName;
  bytes: number;
  frames: number;
  sample_rate: number;
  bit_depth: number;
  channels: number;
  stream_url: string;
  peaks_url: string | null;
}

export interface Job {
  id: string;
  owner_id: string | null;
  project_name: string;
  input_type: 'upload' | 'youtube';
  input_url: string | null;
  source_filename: string | null;
  quality: Quality;
  status: JobStatus;
  stage_progress: number;
  error: string | null;
  detected_bpm: number | null;
  duration_seconds: number | null;
  sample_rate: number | null;
  channels: number | null;
  created_at: string;
  started_at: string | null;
  completed_at: string | null;
  expires_at: string;
  stems: Stem[];
  /** Only on the create response for anonymous jobs. */
  claim_token?: string;
}

export interface JobEvent {
  status: JobStatus;
  progress: number;
  message: string;
  at: string;
}

export interface Page<T> {
  items: T[];
  page: number;
  page_size: number;
  total: number;
}

export type UserRole = 'user' | 'admin';
export type UserStatus = 'pending' | 'active' | 'inactive';

export interface User {
  id: string;
  email: string;
  name: string;
  avatar_url: string | null;
  provider: 'password' | 'google';
  role: UserRole;
  status: UserStatus;
  created_at: string;
  last_login_at: string | null;
}

export interface QualityInfo {
  id: Quality;
  label: string;
  model: string;
  stems: number;
}

export interface PublicConfig {
  google_client_id: string;
  google_sign_in: boolean;
  max_upload_bytes: number;
  anon_retention_hours: number;
  job_retention_days: number;
  qualities: QualityInfo[];
}

export interface YouTubePreview {
  title: string;
  thumbnail_url: string | null;
  duration_seconds: number | null;
  channel: string | null;
  url: string;
}

export type JobListStatus = 'all' | 'active' | 'completed' | 'failed';

/** Opaque mix-state document as stored by GET/PUT /jobs/{id}/mix. */
export type MixPayload = Record<string, unknown>;
