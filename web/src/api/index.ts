import { ApiError, API_BASE, request } from './client';
import type { Job, JobListStatus, MixPayload, Page, PublicConfig, Quality, User, YouTubePreview } from './types';
import type { MixState } from '../lib/mix-state';

export type { MixPayload };

// ---- config / health -------------------------------------------------------

export const getConfig = () => request<PublicConfig>('/config');

export async function health(): Promise<'ok' | 'degraded' | 'down'> {
  try {
    const r = await fetch('/readyz', { cache: 'no-store' });
    if (r.ok) return 'ok';
    return r.status === 503 ? 'degraded' : 'down';
  } catch {
    return 'down';
  }
}

// ---- auth ----------------------------------------------------------------

export const me = () => request<User>('/auth/me');

export const login = (email: string, password: string) =>
  request<User>('/auth/login', { method: 'POST', body: { email, password } });

export const register = (email: string, password: string, name: string) =>
  request<User>('/auth/register', { method: 'POST', body: { email, password, name } });

export const logout = () => request<void>('/auth/logout', { method: 'POST' });

export const googleSignIn = (credential: string) =>
  request<User>('/auth/google', { method: 'POST', body: { credential } });

// ---- jobs ----------------------------------------------------------------

export interface ListJobsParams {
  status?: JobListStatus;
  q?: string;
  page?: number;
  page_size?: number;
}

export function listJobs(p: ListJobsParams = {}) {
  const qs = new URLSearchParams();
  if (p.status) qs.set('status', p.status);
  if (p.q) qs.set('q', p.q);
  if (p.page) qs.set('page', String(p.page));
  if (p.page_size) qs.set('page_size', String(p.page_size));
  const s = qs.toString();
  return request<Page<Job>>(`/jobs${s ? `?${s}` : ''}`);
}

export const getJob = (id: string) => request<Job>(`/jobs/${id}`, { jobId: id });

export const cancelJob = (id: string) => request<Job>(`/jobs/${id}/cancel`, { method: 'POST', jobId: id });

export const deleteJob = (id: string) => request<void>(`/jobs/${id}`, { method: 'DELETE', jobId: id });

export const claimJob = (id: string, claimToken: string) =>
  request<Job>(`/jobs/${id}/claim`, { method: 'POST', body: { claim_token: claimToken } });

export interface CreateJobInput {
  file?: File;
  input_url?: string;
  project_name?: string;
  quality: Quality;
  /** Beat grid + MIDI; the server requires quality high6 and the feature. */
  transcribe?: boolean;
}

export interface UploadProgress {
  loaded: number;
  total: number;
}

/**
 * POST /jobs as multipart through XHR so the upload reports progress. The
 * returned promise resolves to the created job; abort() cancels the transfer.
 */
export function createJob(
  input: CreateJobInput,
  onProgress?: (p: UploadProgress) => void,
): { promise: Promise<Job>; abort: () => void } {
  const fd = new FormData();
  if (input.file) fd.append('file', input.file, input.file.name);
  if (input.input_url) fd.append('input_url', input.input_url);
  if (input.project_name) fd.append('project_name', input.project_name);
  fd.append('quality', input.quality);
  if (input.transcribe) fd.append('transcribe', '1');
  const xhr = new XMLHttpRequest();
  const promise = new Promise<Job>((resolve, reject) => {
    xhr.open('POST', `${API_BASE}/jobs`);
    xhr.withCredentials = true;
    xhr.responseType = 'text';
    xhr.upload.onprogress = (e) => {
      if (onProgress && e.lengthComputable) onProgress({ loaded: e.loaded, total: e.total });
    };
    xhr.onerror = () => reject(new ApiError(0, 'network', 'The upload failed — check your connection and try again.'));
    xhr.onabort = () => reject(new ApiError(0, 'aborted', 'Upload cancelled'));
    xhr.onload = () => {
      let data: unknown = null;
      try {
        data = xhr.responseText ? JSON.parse(xhr.responseText) : null;
      } catch {
        data = null;
      }
      if (xhr.status >= 200 && xhr.status < 300) {
        resolve(data as Job);
        return;
      }
      const env = (data ?? {}) as { code?: string; message?: string };
      reject(new ApiError(xhr.status, env.code ?? '', env.message ?? `upload failed (${xhr.status})`));
    };
    xhr.send(fd);
  });
  return { promise, abort: () => xhr.abort() };
}

export const downloadUrl = (id: string) => `${API_BASE}/jobs/${id}/download`;

/** Probe the download route; the phase-2 API answers 404 until phase 3. */
export async function downloadAvailable(id: string): Promise<boolean> {
  try {
    const r = await fetch(downloadUrl(id), { method: 'HEAD', credentials: 'same-origin' });
    return r.ok;
  } catch {
    return false;
  }
}

// ---- mix state (route lands with phase 3; localStorage stands in) --------

const mixKey = (id: string) => `rk.mix.${id}`;

export async function getMix(id: string): Promise<MixPayload | null> {
  try {
    const r = await request<MixPayload | null>(`/jobs/${id}/mix`, { jobId: id });
    if (r && typeof r === 'object') return r;
  } catch (err) {
    if (!(err instanceof ApiError && err.unavailable)) throw err;
  }
  try {
    const raw = localStorage.getItem(mixKey(id));
    return raw ? (JSON.parse(raw) as MixPayload) : null;
  } catch {
    return null;
  }
}

export async function putMix(id: string, state: MixState): Promise<void> {
  try {
    localStorage.setItem(mixKey(id), JSON.stringify(state));
  } catch {
    // ignore
  }
  try {
    await request<void>(`/jobs/${id}/mix`, { method: 'PUT', body: state, jobId: id });
  } catch (err) {
    if (err instanceof ApiError && err.unavailable) return;
    throw err;
  }
}

// ---- youtube -------------------------------------------------------------

export const youtubePreview = (url: string) =>
  request<YouTubePreview>('/youtube/preview', { method: 'POST', body: { url } });

// ---- profile -------------------------------------------------------------

export interface ProfilePatch {
  name?: string;
  avatar_url?: string | null;
}

export async function getProfile(): Promise<User> {
  try {
    return await request<User>('/profile');
  } catch (err) {
    if (err instanceof ApiError && err.unavailable) return me();
    throw err;
  }
}

export const patchProfile = (patch: ProfilePatch) => request<User>('/profile', { method: 'PATCH', body: patch });

// ---- admin ---------------------------------------------------------------

export interface ListUsersParams {
  status?: 'all' | 'pending' | 'active' | 'inactive';
  q?: string;
  page?: number;
  page_size?: number;
}

export function listUsers(p: ListUsersParams = {}) {
  const qs = new URLSearchParams();
  if (p.status) qs.set('status', p.status);
  if (p.q) qs.set('q', p.q);
  if (p.page) qs.set('page', String(p.page));
  if (p.page_size) qs.set('page_size', String(p.page_size));
  const s = qs.toString();
  return request<Page<User>>(`/admin/users${s ? `?${s}` : ''}`);
}

export const approveUser = (id: string) => request<User>(`/admin/users/${id}/approve`, { method: 'POST' });
export const deactivateUser = (id: string) => request<User>(`/admin/users/${id}/deactivate`, { method: 'POST' });
export const setUserRole = (id: string, role: 'user' | 'admin') =>
  request<User>(`/admin/users/${id}/role`, { method: 'POST', body: { role } });
