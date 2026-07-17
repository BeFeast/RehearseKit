# RehearseKit Quick Start Guide

Get RehearseKit up and running in 5 minutes!

## Prerequisites

- Docker Desktop (Mac M1) or Docker Engine (WSL)
- 4GB+ RAM available for Docker
- Internet connection (for downloading audio processing models)

## Start RehearseKit

```bash
# Clone the repository
git clone https://github.com/BeFeast/RehearseKit.git
cd RehearseKit

# Configure local defaults and start all services
cp config/.env.example .env
docker compose build
docker compose up -d
```

Wait 30-60 seconds for all services to start. You'll see:
```
✓ PostgreSQL healthy
✓ Redis healthy
✓ Backend API started
✓ Worker ready
✓ Frontend compiled
✓ WebSocket service ready
```

## Access the Application

Open your browser to:
- **Frontend**: http://localhost:3000
- **API Docs**: http://localhost:8000/docs
- **API Health**: http://localhost:8000/api/health

Verify every public service after startup:

```bash
./scripts/smoke.sh
```

## Create Your First Job

### Option 1: Via Web Interface

1. Go to http://localhost:3000
2. Click "YouTube URL" tab
3. Paste a YouTube music video URL
4. Enter a project name
5. Choose quality (Fast for testing)
6. Click "Start Processing"
7. Watch real-time progress!

### Option 2: Via API (curl)

```bash
curl -X POST http://localhost:8000/api/jobs/create \
  -F "project_name=My Song" \
  -F "quality_mode=fast" \
  -F "input_url=https://www.youtube.com/watch?v=YOUR_VIDEO_ID"
```

## Monitor Progress

### Via API

```bash
# Get job ID from creation response
JOB_ID="your-job-id-here"

# Check status
curl http://localhost:8000/api/jobs/$JOB_ID | jq '.status, .progress_percent'

# List all jobs
curl http://localhost:8000/api/jobs | jq '.jobs[].status'
```

### Via Web Interface

- Go to http://localhost:3000/jobs
- See all jobs with real-time progress bars
- Click on a job for detailed view

## Download Results

When the job shows `COMPLETED` status:

### Via Web Interface

1. Click the "Download" button on the completed job
2. Get a ZIP file containing:
   - Individual stems (vocals.wav, drums.wav, bass.wav, other.wav)
   - DAWproject file (import into Cubase/Bitwig/Studio One)
   - README with instructions

### Via API

```bash
# Get download URL
curl http://localhost:8000/api/jobs/$JOB_ID/download

# Download the package
wget -O my_song.zip "$(curl -s http://localhost:8000/api/jobs/$JOB_ID/download | jq -r '.url')"
```

### Via File System (Local Development)

```bash
# Packages are stored in tmp/storage/
ls -lh tmp/storage/*.zip

# Copy to your desktop
cp tmp/storage/YOUR_JOB_ID.zip ~/Desktop/
```

## Import into Your DAW

1. Unzip the downloaded package
2. Open Cubase, Bitwig Studio, or PreSonus Studio One
3. Import the `.dawproject` file
4. All stems will load with:
   - ✅ Correct tempo set
   - ✅ Tracks named and color-coded
   - ✅ Audio aligned to start
   - ✅ Mixer levels configured

**Or** manually import stems from the `stems/` folder.

## Processing Times

For a 3-minute song:
- **Fast mode**: ~5-8 minutes
- **High quality mode**: ~12-18 minutes

Steps breakdown:
- Converting: ~5 seconds
- Analyzing (tempo): ~1 second  
- Separating stems: 80% of total time
- Packaging: ~5 seconds

## Troubleshooting

### Services won't start

```bash
# Check Docker resources
docker system info | grep -E "CPUs|Memory"

# Restart services
docker compose restart

# Full rebuild
docker compose down
docker compose up --build
```

### Job stuck in PENDING

```bash
# Check worker status
docker compose logs worker | tail -50

# Restart worker
docker compose restart worker
```

### Job failed

```bash
# Get error message
curl http://localhost:8000/api/jobs/$JOB_ID | jq '.error_message'

# Check worker logs
docker compose logs worker | grep -A 10 $JOB_ID
```

### Port already in use

```bash
# Mac: Find and kill process
lsof -ti:3000 | xargs kill -9

# Change port in docker-compose.yml
# Edit ports: - "3001:3000" instead of "3000:3000"
```

## Example Workflow

Complete example from start to finish:

```bash
# 1. Start services
docker compose up -d

# 2. Create a job
JOB_ID=$(curl -s -X POST http://localhost:8000/api/jobs/create \
  -F "project_name=My Cover Song" \
  -F "quality_mode=fast" \
  -F "input_url=https://www.youtube.com/watch?v=dQw4w9WgXcQ" \
  | jq -r '.id')

echo "Job created: $JOB_ID"

# 3. Monitor until complete (check every 10 seconds)
while true; do
  STATUS=$(curl -s http://localhost:8000/api/jobs/$JOB_ID | jq -r '.status')
  PROGRESS=$(curl -s http://localhost:8000/api/jobs/$JOB_ID | jq -r '.progress_percent')
  
  echo "Status: $STATUS ($PROGRESS%)"
  
  if [[ "$STATUS" == "COMPLETED" ]]; then
    echo "✅ Job completed!"
    break
  elif [[ "$STATUS" == "FAILED" ]]; then
    echo "❌ Job failed"
    curl -s http://localhost:8000/api/jobs/$JOB_ID | jq '.error_message'
    break
  fi
  
  sleep 10
done

# 4. Download the package
cp tmp/storage/$JOB_ID.zip ~/Desktop/my_cover_song.zip

# 5. Unzip and import to your DAW
unzip ~/Desktop/my_cover_song.zip -d ~/Desktop/my_cover_song/
echo "Import the .dawproject file into Cubase/Bitwig/Studio One!"
```

## What's in the Package?

```
my_song_RehearseKit.zip/
├── stems/
│   ├── vocals.wav    (isolated vocals, 24-bit/48kHz)
│   ├── drums.wav     (isolated drums)
│   ├── bass.wav      (isolated bass)
│   └── other.wav     (guitars, keys, synths, etc.)
├── My Song.dawproject (DAW project file with all stems)
└── README.txt        (instructions)
```

## Next Steps

- Try different songs and genres
- Experiment with Fast vs High Quality modes
- Create rehearsal tracks for your band
- Learn songs by isolating specific instruments
- Build covers by studying original arrangements

## Support

- **Documentation**: See `docs/` folder
- **API Reference**: http://localhost:8000/docs
- **Issues**: https://github.com/UnTypeBeats/RehearseKit/issues

---

Built with ❤️ for musicians who want to spend less time on setup and more time making music.
