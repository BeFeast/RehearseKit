# State of the repository

**Audit date:** 2026-07-17

**Issue:** #3 — reconcile PRD versus implemented state

**Scope:** current `main` code, the Stage 1/pre-MVP baseline, Stage 2, Stage 3, and the PRD commitments those stages were intended to deliver

## Executive verdict

RehearseKit contains a substantial working application skeleton and most of the Stage 2 and Stage 3 user interface. It is not accurate to describe the checked-in repository as PRD-complete or as a locally reproducible production stack.

- The upload/YouTube → Celery → conversion → tempo detection → Demucs → DAWproject → ZIP path is present in code.
- Stage 2 waveform preview and the Stage 3 trimming, reprocessing, mixer, and package-layout work are present, with important cancellation, deletion, validation, and test gaps.
- The PRD promises six stems, confidence-scored tempo detection, embedded tempo metadata, and a Cubase `.cpr` project. Current code produces four stems, no confidence score, does not actually write the BPM tag, and generates `.dawproject` instead.
- The TrueNAS compose file is valid YAML/Compose, but it has no build definitions, depends on external database/Redis/storage/GPU infrastructure, and references an unavailable GPU-worker image. It did not build or start as a clean local stack during this audit.
- Historical documents contain useful implementation history, but their “complete”, “production ready”, and “all services healthy” claims should not be treated as current verification. Examples include `docs/START_HERE.md`, `docs/archive/deployment/STATUS.md`, `docs/archive/stage-3/FINAL_DEPLOYMENT_STATUS.md`, and `docs/STAGE_4_COMPLETE.md`.

No backlog issues were created as part of this audit. The proposals below are for product-owner promotion only.

## Method and verdict definitions

The PRD ends at `PRD.md` line 451 in the middle of the UI section and does not itself label Stage 1, 2, or 3. This audit therefore uses:

- **Stage 1 / pre-MVP:** the core PRD features and the pre-MVP inventory in `docs/archive/stage-3/PROJECT_STATUS_STAGE_3.md`.
- **Stage 2:** the feature list in `docs/ideas/mvp-stage-2.md` and `docs/archive/deployment/MVP_STAGE_2_PROGRESS.md`.
- **Stage 3:** the four-feature list in `docs/ideas/mvp-stage-2.md` and the Stage 3 archive documents.

Verdicts mean:

- **Implemented:** the current code contains the claimed behavior end to end. Lack of strong tests is called out separately.
- **Partial:** a useful portion exists, but the PRD behavior, reliability, safety, or integration is incomplete.
- **Missing:** no effective implementation exists, or the code is only a placeholder.

Paths under “Evidence” point to current code or tests. Archive documents are not used as implementation proof.

## Stage 1 / pre-MVP feature audit

| PRD or stage feature | Verdict | Current evidence and gap | Test evidence |
|---|---|---|---|
| Upload FLAC through drag-and-drop or file browser | **Implemented** | `frontend/components/audio-uploader.tsx` implements both interactions; `backend/app/api/jobs.py` accepts the upload; `backend/app/services/storage.py` persists it. MP3 and WAV are also accepted. | UI-only format assertions exist in `frontend/e2e/complete-flow.spec.ts`; there is no backend upload test or fixture-driven processing test. |
| YouTube URL input and audio extraction | **Implemented** | `frontend/components/audio-uploader.tsx` provides the URL flow; `backend/app/api/youtube.py`, `backend/app/services/youtube_preview.py`, and `backend/app/services/audio.py` use yt-dlp for preview and acquisition. | `frontend/e2e/job-creation.spec.ts` and `frontend/e2e/complete-flow.spec.ts` reference a live YouTube URL, so they are network-dependent and not hermetic. |
| Convert input to 24-bit/48 kHz WAV | **Implemented** | FFmpeg commands in `backend/app/services/audio.py` use `pcm_s24le` and `48000` for conversion, trimming, and final stems. | No tests cover the command construction or inspect generated audio properties. |
| Input validation: size and format verification | **Partial** | `frontend/components/audio-uploader.tsx` and `backend/app/api/jobs.py` check filename extensions for MP3/WAV/FLAC. There is no content sniffing, decoded-audio validation, or enforced file-size limit. The FastAPI constructor value in `backend/app/main.py` is not a substitute for an ingress/body-size policy. | `frontend/e2e/complete-flow.spec.ts` only checks the input `accept` attribute; there are no malicious/mismatched file or size-boundary tests. |
| Project name and Fast/High processing selection | **Implemented** | `frontend/components/audio-uploader.tsx`, `backend/app/models/job.py`, and `backend/app/services/audio.py` pass the project name and select `htdemucs` versus `htdemucs_ft`. | Form serialization is covered in `frontend/utils/__tests__/api.test.ts`; the actual model selection is untested. |
| Optional manual BPM override | **Partial** | The field is stored by `backend/app/api/jobs.py` and used by `backend/app/tasks/audio_processing.py`, and the client supports serializing it in `frontend/utils/api.ts`. There is no manual BPM input in `frontend/components/audio-uploader.tsx`. | Serialization is covered in `frontend/utils/__tests__/api.test.ts`; no API validation or pipeline behavior test exists. |
| Tempo detection with confidence scoring | **Partial** | `backend/app/services/audio.py` calls `librosa.beat.beat_track` and stores a BPM. No confidence value is calculated, stored, or shown. | No tempo-service tests exist. |
| Six stems: vocals, drums, bass, guitars, keys, other | **Partial** | `backend/app/services/audio.py` explicitly runs standard four-stem Demucs output; `backend/app/api/jobs.py` only serves vocals, drums, bass, and other. Guitar and keys remain combined in `other.wav`. | No separation-output tests exist. `docs/archive/deployment/stem-separation-limitations.md` documents the limitation but is not a test. |
| Embed tempo metadata in every stem | **Missing** | `backend/app/services/audio.py::embed_tempo_metadata` opens each WAV but writes no tag. `TBPM` and `ID3` are imported but unused. Failures are swallowed and processing continues. | No metadata tests exist. |
| Generate Cubase `.cpr` project | **Missing** | `backend/app/services/cubase.py` generates an open `.dawproject` ZIP, not a Cubase `.cpr`. This may be a valid product change, but `PRD.md` was never reconciled to it. | No generator tests parse the project or validate it against a format/schema. Historical manual Cubase claims are in `docs/guides/cubase-import-guide.md`. |
| DAW project contains named stems, tempo, mixer defaults, panning, colors, and 48 kHz settings | **Implemented** | `backend/app/services/cubase.py` creates tracks, tempo, sample rate, volume, center pan, colors, and aligned audio clips in the `.dawproject`. | No automated project-content test exists. |
| ZIP package with project and individual stems | **Implemented** | `backend/app/tasks/audio_processing.py` invokes `backend/app/services/audio.py::create_package`, which adds a project folder, `stems/`, and import documentation. | `frontend/e2e/download.spec.ts` tests only when a pre-existing completed job is available and otherwise skips; package contents are not asserted. |
| Asynchronous processing queue and persistent job state | **Implemented** | `backend/app/celery_app.py`, `backend/app/tasks/audio_processing.py`, `backend/app/models/job.py`, and migration `backend/alembic/versions/001_create_jobs_table.py` provide Celery/Redis dispatch and PostgreSQL job state. | Backend tests cover authentication/security only; there are no job/task tests. |
| Handle multiple queued jobs concurrently | **Partial** | Multiple records/tasks can be enqueued, but the audited TrueNAS compose command in `infrastructure/truenas/docker-compose.truenas.yml` fixes the GPU worker at concurrency 1. There is no concurrency/load test. | None. |
| Real-time processing stage/progress updates | **Partial** | `backend/app/tasks/audio_processing.py` publishes Redis updates and `frontend/utils/websocket.ts`, `frontend/components/job-card.tsx`, and `frontend/app/jobs/[id]/page.tsx` contain a client. No checked-in backend WebSocket server subscribes to Redis or exposes `/ws/jobs/{id}/progress`; the compose file relies on a separately published WebSocket image. The job list polls every five seconds in `frontend/components/processing-queue.tsx`, so status can still refresh without real-time delivery. The PRD's SSE fallback is also absent. | WebSocket client behavior is unit-tested in `frontend/utils/__tests__/websocket.test.ts`; `frontend/e2e/complete-flow.spec.ts` contains an assertion-free WebSocket placeholder. |
| Estimated time remaining | **Missing** | The UI contains static explanatory timing text in `frontend/components/audio-uploader.tsx` and status messages in `frontend/components/job-card.tsx`; no per-job ETA is calculated or returned. | None. |
| Job history with metadata | **Implemented** | `backend/app/api/jobs.py` lists persisted jobs newest-first; `frontend/app/jobs/page.tsx`, `frontend/components/processing-queue.tsx`, and `frontend/app/jobs/[id]/page.tsx` present history/details. | `frontend/components/__tests__/processing-queue.test.tsx` and API client tests cover list rendering/client behavior. E2E history checks accept zero cards and do not prove stored history. |
| Re-download completed packages | **Implemented** | Download handling exists in `backend/app/api/jobs.py`, `frontend/components/job-card.tsx`, and the job details page. Availability still depends on files remaining in storage. | `frontend/e2e/download.spec.ts` conditionally skips without an existing completed job. |
| Automatic configurable retention cleanup | **Missing** | `JOB_RETENTION_DAYS` exists in `backend/app/core/config.py`, but there is no scheduled task. `backend/app/api/jobs.py` still has a file-deletion TODO. | None. |
| Health endpoint | **Implemented** | `backend/app/api/health.py` checks PostgreSQL and Redis; `backend/app/main.py` mounts `/api/health`. | `frontend/e2e/basic.spec.ts` expects a live healthy backend; no isolated backend health tests exist. |

## Stage 2 feature audit

| Stage 2 feature | Verdict | Current evidence and gap | Test evidence |
|---|---|---|---|
| Waveform for uploaded and fetched YouTube audio | **Implemented** | `frontend/components/audio-waveform.tsx` wraps WaveSurfer; `frontend/components/audio-uploader.tsx` supplies local object URLs and YouTube preview URLs. | The audio components are excluded from Jest coverage in `frontend/jest.config.ts`; no Playwright fixture verifies waveform readiness. |
| Play/pause, seek, volume, and keyboard playback controls | **Implemented** | Controls and spacebar behavior are in `frontend/components/audio-waveform.tsx`. | No direct component or E2E audio-control test exists. |
| YouTube two-step fetch → preview → process flow | **Implemented** | `frontend/components/audio-uploader.tsx`, `backend/app/api/youtube.py`, and `backend/app/services/youtube_preview.py` implement preview IDs and subsequent job creation. | Existing E2E specs still click “Start Processing” without first completing the required fetch step, so they do not reliably verify the current flow. |
| Source playback on job details | **Implemented** | `backend/app/api/jobs.py` serves `/source`; `frontend/app/jobs/[id]/page.tsx` renders `AudioWaveform` for the stored source. GCS preview returns 501, so this is local-storage-only. | No test covers source playback or the GCS limitation. |
| Cancel button with confirmation | **Partial** | UI confirmation exists in `frontend/components/job-card.tsx` and the details page. `backend/app/api/jobs.py` only changes database status; it explicitly does not revoke/terminate the Celery task, which can continue writing results. | No cancellation tests exist. |
| Delete button with confirmation | **Partial** | UI and record deletion exist in `frontend/components/job-card.tsx`, `frontend/app/jobs/[id]/page.tsx`, and `backend/app/api/jobs.py`. Associated source, stem, and package files are not deleted. | API client deletion is unit-tested in `frontend/utils/__tests__/api.test.ts`; server/file cleanup is untested. |
| Runtime API URL and browser-safe download fixes | **Implemented** | Central runtime URL logic is in `frontend/utils/api.ts`; blob-download fallback is in `frontend/components/job-card.tsx`. | API URL/client branches have unit coverage in `frontend/utils/__tests__/api.test.ts`; browser download tests remain data-dependent. |

## Stage 3 feature audit

| Stage 3 feature | Verdict | Current evidence and gap | Test evidence |
|---|---|---|---|
| Waveform region trimming with persisted start/end and FFmpeg processing | **Partial** | Regions UI is in `frontend/components/audio-waveform.tsx`; fields are added by `backend/alembic/versions/002_add_trim_fields.py`; `backend/app/tasks/audio_processing.py` calls `trim_audio`. The API does not validate start < end or bounds, and `trim_start || undefined` in `frontend/components/audio-uploader.tsx` drops a valid zero start. | FormData serialization, including zero, is unit-tested in `frontend/utils/__tests__/api.test.ts`, but the uploader bug, API validation, and FFmpeg result are untested. |
| Reprocess a completed job in high quality without re-upload | **Partial** | `backend/app/api/jobs.py` reuses the stored source and settings; `frontend/app/jobs/[id]/page.tsx` exposes the action. The new job does not copy `user_id`, quality values are not safely validated, and there are no authorization checks. | No reprocess tests exist. |
| Professional synchronized stem mixer with volume, mute, solo, and channel waveform | **Implemented** | `frontend/components/stem-mixer.tsx` loads the four stem endpoints through Web Audio, synchronizes playback, and implements faders/mute/solo; the details page mounts it for completed jobs. Custom mix export was explicitly deferred and is not counted as part of this verdict. | No mixer component or E2E tests exist. |
| Cubase-compatible package folder around the DAWproject | **Implemented** | `backend/app/services/audio.py::create_package` writes `ProjectName/ProjectName.dawproject` plus separate stems and guides. | No ZIP-layout test exists; evidence of Cubase interoperability is manual/historical only. |

## Other PRD commitments affecting stabilization

These requirements are not cleanly assigned to one historical stage, but are explicit in `PRD.md` and affect whether Stages 1–3 can be called complete.

| PRD commitment | Verdict | Evidence and gap |
|---|---|---|
| Individual stem downloads | **Partial** | `backend/app/api/jobs.py` exposes individual stem responses for the mixer, but there is no explicit download UI and only four stem names are supported. |
| Display result file sizes and durations | **Missing** | Job schemas/models do not store result duration or file sizes, and the details page does not display them. |
| Re-run with different settings without re-uploading | **Partial** | High-quality reprocess exists, but users cannot choose arbitrary changed settings and ownership is lost. |
| Jobs survive server restarts/resume processing | **Partial** | Job records persist, but there is no task reconciliation/resume mechanism for work interrupted by a worker restart. |
| Automatic retry for transient failures, three attempts | **Missing** | `backend/app/tasks/audio_processing.py` has no `autoretry_for`, `self.retry`, or retry policy. |
| System status indicator in the header | **Missing** | A backend health endpoint exists, but `frontend/components/layout/header.tsx` does not display it. |
| Clear recovery guidance for processing errors | **Partial** | Failed job text is shown, but backend exception strings are stored directly and the UI provides no structured recovery action. |
| Dark mode | **Partial** | `frontend/app/providers.tsx` configures `next-themes`, and components include dark styles, but the app defaults to light and no user-facing theme control was found. |
| Mobile responsiveness and basic semantic controls | **Partial** | Responsive Tailwind layouts and Radix primitives are present. There is no WCAG audit, automated accessibility scan, or meaningful screen-reader/keyboard coverage for the audio interfaces. |

## Test and verification confidence

The repository has a sizeable frontend unit suite and authentication/security backend tests, but its test inventory overstates coverage of the audio product:

- `backend/tests/test_auth.py` and `backend/tests/test_security.py` do not cover jobs, storage, Celery tasks, FFmpeg, Demucs, metadata, DAWproject generation, or packaging.
- `frontend/e2e/complete-flow.spec.ts` explicitly omits audio fixtures, contains placeholder assertions/comments, and conditionally skips based on pre-existing jobs.
- `frontend/e2e/download.spec.ts` requires a pre-existing completed job.
- `frontend/e2e/cloud-test.spec.ts` targets a historical external deployment rather than a controlled local test environment.
- `frontend/playwright.config.ts` starts only the frontend; its global setup expects an independently running healthy backend, database, and Redis.
- The repository contains a WebSocket client and Redis publisher but no checked-in WebSocket server implementation, so the real-time path cannot be rebuilt from source.
- Tracked `frontend/playwright-report/` output and `frontend/debug-profile-page.png` are historical generated/debug artifacts and should be handled in a separate cleanup issue, not used as current evidence.

The most trustworthy existing automated coverage is frontend utility/component behavior and backend authentication/security behavior. The core audio journey currently relies on code inspection and historical manual reports.

## Docker Compose build and local-start verification

Audited file: `infrastructure/truenas/docker-compose.truenas.yml`.

The host had Docker Engine 29.1.3 but no installed Compose plugin. A temporary Docker Compose v5.3.1 binary outside the repository was used. Placeholder local-only configuration values were supplied; no production configuration or deployment target was accessed.

| Check | Result |
|---|---|
| `docker compose -f infrastructure/truenas/docker-compose.truenas.yml config --quiet` | **Pass.** The compose model is syntactically valid and resolves four services: backend, WebSocket, frontend, and GPU worker. |
| `docker compose -f infrastructure/truenas/docker-compose.truenas.yml build` | **Not buildable.** Exit 0 with `No services to build`; every service uses `image:` and none has `build:`. |
| Registry manifest availability | **Partial.** Public manifests exist for `kossoy/rehearsekit-frontend:latest`, `kossoy/rehearsekit-backend:latest`, and `kossoy/rehearsekit-websocket:latest`. No manifest exists for the unqualified `rehearsekit-gpu-worker:latest` image referenced by the compose file. |
| Clean `up -d --pull never` | **Fail.** Exit 1 because required images were absent locally. The created compose network was removed immediately after the check. |
| Could a normal pull make the full file start? | **No, as checked in.** The GPU-worker image cannot be pulled and the compose file provides no way to build it. |

Even after fixing the worker image, this file is production-host-specific rather than a self-contained local stack:

- PostgreSQL and Redis are external URLs; they are not services in the file.
- Storage is a fixed TrueNAS bind mount.
- The worker reserves an NVIDIA GPU.
- Fixed host ports and fixed container names make parallel/local developer stacks harder.

**Compose verdict:** valid configuration, but **does not build and does not start as a clean local stack**. This conflicts with current claims in `docs/START_HERE.md`, `docs/archive/deployment/STATUS.md`, and Stage 3 deployment archives. It does not disprove that a separately prepared TrueNAS host once ran these services; it means the repository cannot reproduce that state from the audited compose file alone.

## Ordered stabilization backlog proposals

Each proposal is intended to fit one worker issue. The product owner should promote, revise, or reject these; this audit did not create issues.

1. **Make the GPU worker obtainable.** Add a pinned registry image that exists, or a `build:` definition/Dockerfile target for `gpu-worker`; verify `compose pull` or `compose build` from a clean machine.
2. **Restore a source-built WebSocket service.** Add the missing Redis subscriber/WebSocket server, health endpoint, Docker build target, and an integration test for one published progress update.
3. **Add a portable local compose profile/override.** Provide PostgreSQL and Redis services, a named/local storage volume, optional GPU settings, and non-conflicting names/ports while leaving the TrueNAS production file intact.
4. **Add a compose smoke check.** In CI, render the compose model, build all buildable services, start dependency services, and assert backend/WebSocket health without deploying.
5. **Add a clean-database migration smoke test.** Run Alembic from revision 001 through head against a fresh PostgreSQL database and stop relying on `Base.metadata.create_all` to mask migration drift.
6. **Remove the default admin credential path.** Change `backend/scripts/create_admin.py` to require an explicit secret or create an OAuth-only admin; add a regression test and update the auth guide.
7. **Enforce job ownership on every job route.** Filter list/get/download/source/stem/reprocess/cancel/delete by the authenticated user (with an explicit anonymous-job policy), preserve `user_id` during reprocess, and add authorization tests.
8. **Create backend job API tests.** Cover upload/URL validation, quality/manual BPM parsing, job creation, list/detail/download errors, and task dispatch with mocked storage/Celery.
9. **Create audio service and package unit tests.** Mock subprocesses and verify FFmpeg formats, Demucs model selection, four expected outputs, DAWproject XML contents, and ZIP folder layout.
10. **Make Playwright hermetic.** Add small legal/generated audio fixtures, start backend/PostgreSQL/Redis with the test stack, mock YouTube, create its own jobs, and remove conditional passes/skips based on external state.
11. **Implement real cancellation.** Store Celery task IDs, revoke/terminate safely, prevent a cancelled task from later marking itself complete, and test the race.
12. **Implement complete job/file deletion.** Remove source, stem, and package objects before deleting the row, handle shared reprocess sources safely, and test local and GCS storage modes.
13. **Implement retention cleanup.** Add a scheduled Celery task using `JOB_RETENTION_DAYS`, reuse the file-deletion service, and cover cutoff/idempotency behavior.
14. **Implement and verify BPM metadata writing.** Write a DAW-readable BPM tag (or adopt a documented alternative), fail or warn explicitly, and inspect the output tag in a unit test.
15. **Validate uploads and trim ranges.** Enforce decoded media type/content, size limits, supported extension/content agreement, finite BPM, non-negative trim values, start < end, and duration bounds; fix the zero-start uploader bug.
16. **Expose manual BPM and result metadata in the UI.** Add the PRD manual override field and show effective BPM, source/result duration, and package/stem sizes from API data.
17. **Reconcile the stem contract.** Product decision: implement six distinct stems, or revise the PRD/UI/docs to the supported four-stem contract. Follow with matching API and package tests.
18. **Reconcile the DAW project contract.** Product decision: deliver Cubase `.cpr`, or formally replace that PRD requirement with `.dawproject`; add a schema/content validator and one documented manual Cubase acceptance test.
19. **Add retry and interrupted-job reconciliation.** Define transient exceptions, three-attempt backoff, idempotent processing steps, and startup reconciliation for stranded active jobs.
20. **Add observable, safe error reporting.** Store stable error codes and user-facing recovery text instead of raw exception strings; retain detailed diagnostics only in server logs.
21. **Reconcile current documentation.** Update `PRD.md`, `docs/README.md`, `docs/START_HERE.md`, Stage 4 summaries, and deployment guides so stem count, project format, compose topology, auth status, and verification dates agree with tested reality.
22. **Remove tracked generated/debug artifacts.** Delete or ignore historical Playwright reports and debug screenshots, and document how fresh reports are produced without committing them.

## Audit verification record

- `bash .maestro/verify.sh` — passed; the generated script identifies all three acceptance criteria as manual-verification requirements.
- `uv run --no-project python -m compileall -q backend/app backend/tests` — passed.
- `bun run type-check` from `frontend/` — passed.
- `bun run test --runInBand utils/__tests__/api.test.ts utils/__tests__/websocket.test.ts components/__tests__/processing-queue.test.tsx` — 3 suites and 136 tests passed.
- Evidence-path check — all repository paths cited by this document existed at audit time.
- `git diff --check` — passed.

## Bottom line

The repository is best described as a feature-rich, partially stabilized MVP. The Stage 2 and Stage 3 UI work is largely present, while several original PRD outputs are substituted or incomplete and the core pipeline lacks automated proof. The first stabilization priority is reproducible infrastructure and security boundaries, followed by core-pipeline tests and then product-contract reconciliation for stems, metadata, and Cubase output.
