# RehearseKit

**Your Complete Rehearsal Toolkit**

Transform any audio source into a ready-to-use rehearsal project in minutes. RehearseKit automatically separates stems, detects tempo, and generates DAW project files—so you can focus on making music.

[![Live Demo](https://img.shields.io/badge/demo-rehearsekit.uk-blue)](https://rehearsekit.uk)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

---

## ✨ Features

### Core Functionality
- 🎵 **Audio Input**: Upload MP3, WAV, or FLAC files, or paste YouTube URLs
- 🎼 **AI Stem Separation**: Isolate vocals, drums, bass, and other instruments using Demucs
- ⏱️ **Tempo Detection**: Automatic BPM analysis with manual override
- ✂️ **Waveform Trimming**: Visual region selection to process only specific portions
- 🎹 **DAW Integration**: Auto-generated .dawproject files (Studio One, Bitwig, Reaper, Cubase)

### Advanced Features (Stage 3)
- 🎚️ **Professional DAW Mixer**: Multi-channel mixer with vertical faders, solo/mute, and real-time preview
- 🔄 **Quality Upgrade**: One-click reprocessing from fast to high quality mode
- 📦 **Download Packages**: All stems + project file in organized ZIP structure
- 🎨 **Modern UI**: Dark theme with professional DAW-style interface

### Coming Soon (Stage 4)
- 🔐 **Authentication**: Google OAuth + optional email/password
- 👤 **User Accounts**: Job history and personalized experience
- 🚀 **Self-Hosted**: Deploy locally or on TrueNAS for zero cloud costs

---

## 🚀 Quick Start

### Using Docker (Recommended)

1. **Clone and configure**
   ```bash
   git clone https://github.com/BeFeast/RehearseKit.git
   cd RehearseKit
   cp config/.env.example .env
   # The example values are enough for local startup; cloud credentials are optional
   ```

2. **Start all services**
   ```bash
   docker compose build
   docker compose up -d
   ```

3. **Access the application**
   - Frontend: http://localhost:3000
   - Backend API: http://localhost:8000
   - API Docs: http://localhost:8000/docs

4. **Verify the stack**
   ```bash
   ./scripts/smoke.sh
   ```

   The smoke script checks backend dependencies and `/docs`, the frontend root,
   and the websocket health endpoint. Stop the stack with `docker compose down`.

### Prerequisites
- Docker Desktop (Mac) or Docker Engine (Linux/WSL)
- 8GB+ RAM recommended for AI processing
- ~10GB disk space for models

For detailed setup instructions, see [`docs/guides/local-development.md`](docs/guides/local-development.md)

---

## Local development

Install [uv](https://docs.astral.sh/uv/) and [Bun](https://bun.sh/), then run the default local checks from the repository root:

```bash
./scripts/verify.sh
```

This installs lightweight backend test dependencies with uv, then runs backend pytest plus frontend lint and unit tests with Bun. It does not start Docker services or install GPU/model dependencies. The heavier suites are explicit:

```bash
./scripts/verify.sh --e2e  # Playwright end-to-end tests
./scripts/verify.sh --gpu  # Backend tests with full audio/GPU dependencies
```

Run and build each application directly with:

```bash
# Backend (FastAPI)
cp config/.env.example backend/.env
cd backend
uv run --python 3.11 --with-requirements requirements.txt uvicorn app.main:app --reload

# Frontend (Next.js)
cd frontend
bun install --frozen-lockfile
bun run dev
bun run build
```

Targeted test commands:

```bash
./backend/scripts/run_tests.sh
cd frontend && bun run lint && bun run test -- --runInBand
cd frontend && bun run test:e2e  # opt-in; requires Playwright browser support
```

## Rebuild (rk)

A Go rebuild of the backend is in progress: one binary, `rk`, that serves the
API, an embedded SPA and (later) the worker, with Postgres as the only
dependency. Phase 2 (skeleton: config, migrations, sessions, jobs + queue +
SSE, Range-served stems and peaks) lives under `cmd/rk` and `internal/`.
See [docs/rebuild/README.md](docs/rebuild/README.md) for how to run it.

```bash
export RK_DATABASE_URL=postgres://rk:rk@127.0.0.1:15432/rk
go run ./cmd/rk migrate
go run ./cmd/rk create-admin --email you@example.com --password '...'
go run ./cmd/rk serve
```

The legacy stack above keeps working unchanged until the cutover phase.

---

## 📁 Project Structure

```
RehearseKit/
├── frontend/              # Next.js 14 application
├── backend/               # FastAPI application + Celery workers
├── websocket/             # WebSocket service for real-time updates
├── infrastructure/        # Deployment configurations (Terraform, TrueNAS)
├── docs/                  # Documentation
│   ├── guides/            # User and developer guides
│   ├── api/               # API documentation
│   ├── archive/           # Historical documentation
│   └── ideas/             # Feature proposals
├── scripts/               # Deployment and setup scripts
├── config/                # Configuration templates
│   ├── .env.example       # Environment variables template
│   ├── docker-compose.dev.yml
│   └── docker-compose.prod.yml
└── docker-compose.yml     # Local development setup
```

---

## 🏗️ Architecture

### Technology Stack
- **Frontend**: Next.js 14, React Query, WaveSurfer.js, Web Audio API, shadcn/ui
- **Backend**: FastAPI, SQLAlchemy, Celery, Alembic
- **AI/Audio**: Demucs (stem separation), librosa (tempo detection), FFmpeg, yt-dlp
- **Infrastructure**: Docker, PostgreSQL 16, Redis 7
- **Deployment**: TrueNAS SCALE, Cloudflare Tunnel

### Service Architecture
```
┌─────────────┐     ┌─────────────┐     ┌─────────────┐
│   Frontend  │────▶│  Backend    │────▶│   Worker    │
│  (Next.js)  │     │  (FastAPI)  │     │  (Celery)   │
└─────────────┘     └─────────────┘     └─────────────┘
       │                   │                    │
       │            ┌──────┴──────┐            │
       │            │             │            │
       ▼            ▼             ▼            ▼
┌─────────────┐  ┌─────────┐  ┌─────────┐  ┌─────────┐
│  WebSocket  │  │PostgreSQL│  │  Redis  │  │ Storage │
│   Service   │  │    16   │  │    7    │  │  Local  │
└─────────────┘  └─────────┘  └─────────┘  └─────────┘
```

---

## 📖 Documentation

- **[Quick Start Guide](docs/guides/QUICKSTART.md)** - Get started in 5 minutes
- **[Development Guide](docs/guides/DEVELOPMENT_GUIDE.md)** - Contributing and development setup
- **[Deployment Guide](docs/guides/deployment.md)** - Production deployment options
- **[TrueNAS Deployment](docs/guides/truenas-deployment.md)** - Self-hosted setup
- **[Cubase Import Guide](docs/guides/cubase-import-guide.md)** - DAW project import
- **[API Documentation](docs/api/api.md)** - REST API reference

---

## 🚢 Deployment

### TrueNAS SCALE (Recommended for Self-Hosting)
```bash
# See docs/guides/truenas-deployment.md for complete instructions
cd infrastructure/truenas
./deploy.sh
```

### Docker Compose (Production)
```bash
# Use production configuration
docker compose -f config/docker-compose.prod.yml up -d
```

### Deployment Scripts
All deployment scripts are located in `scripts/deployment/`:
- `deploy-truenas.sh` - TrueNAS deployment automation
- `build-and-push.sh` - Build and push Docker images

---

## 🤝 Contributing

We welcome contributions! Please see [CONTRIBUTING.md](CONTRIBUTING.md) for:
- Code of conduct
- Development workflow
- Pull request process
- Coding standards

---

## 📊 Current Status

**Version**: Stage 3 Complete (Stage 4 in progress)  
**Status**: Production Ready ✅  
**Live Demo**: [rehearsekit.uk](https://rehearsekit.uk)

### Stage 3 Features (Completed)
- ✅ Waveform trimming with visual feedback
- ✅ Professional DAW-style mixer
- ✅ Quality upgrade workflow
- ✅ Cubase import integration

### Stage 4 Features (In Progress)
- 🔄 Authentication system (Google OAuth)
- 🔄 User accounts and job ownership
- 🔄 Repository cleanup and reorganization

---

## 📝 License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.

---

## 🙏 Acknowledgments

- **Demucs** - AI-powered stem separation
- **WaveSurfer.js** - Audio waveform visualization
- **shadcn/ui** - Beautiful UI components
- **FastAPI** - Modern Python web framework

---

## 📧 Support

- **Documentation**: [docs/](docs/)
- **Issues**: [GitHub Issues](https://github.com/UnTypeBeats/RehearseKit/issues)
- **Live Site**: [rehearsekit.uk](https://rehearsekit.uk)

---

Built with ❤️ for musicians who want to spend less time on setup and more time making music.
