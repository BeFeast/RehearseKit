package jobs

// Stage progress bands (design contract): each stage owns a slice of the
// 0..100 job progress so a client can render one bar across the pipeline.
//
//	converting  0–14
//	analyzing  14–28
//	separating 28–76
//	finalizing 76–89
//	packaging  89–99
//	completed  100
const (
	ProgressConverting = 0
	ProgressAnalyzing  = 14
	ProgressSeparating = 28
	ProgressFinalizing = 76
	ProgressPackaging  = 89
	ProgressPackaged   = 99
	ProgressCompleted  = 100
)

// StageStart returns the progress at which status begins.
func StageStart(status string) int16 {
	switch status {
	case StatusConverting:
		return ProgressConverting
	case StatusAnalyzing:
		return ProgressAnalyzing
	case StatusSeparating:
		return ProgressSeparating
	case StatusFinalizing:
		return ProgressFinalizing
	case StatusPackaging:
		return ProgressPackaging
	case StatusCompleted:
		return ProgressCompleted
	}
	return 0
}

// StageEnd returns the progress at which status hands over to the next one.
func StageEnd(status string) int16 {
	switch status {
	case StatusConverting:
		return ProgressAnalyzing
	case StatusAnalyzing:
		return ProgressSeparating
	case StatusSeparating:
		return ProgressFinalizing
	case StatusFinalizing:
		return ProgressPackaging
	case StatusPackaging:
		return ProgressPackaged
	case StatusCompleted:
		return ProgressCompleted
	}
	return 0
}

// StageProgress maps a within-stage fraction (0..1) onto the job progress
// band of status.
func StageProgress(status string, fraction float64) int16 {
	if fraction < 0 {
		fraction = 0
	} else if fraction > 1 {
		fraction = 1
	}
	lo, hi := StageStart(status), StageEnd(status)
	return lo + int16(float64(hi-lo)*fraction+0.5)
}

// StatusMessage is the progress-bar caption for a status, verbatim from the
// legacy frontend (components/job-card.tsx getStatusMessage) so the new SPA
// can show the same copy.
func StatusMessage(status string, progress int16) string {
	switch status {
	case StatusPending:
		return "Queued for processing..."
	case StatusConverting:
		return "Converting audio to WAV format..."
	case StatusAnalyzing:
		return "Analyzing tempo and detecting BPM..."
	case StatusSeparating:
		if progress < 40 {
			return "Loading AI model..."
		}
		if progress < 70 {
			return "Separating stems with AI (this takes 2-5 minutes)..."
		}
		return "Finalizing stem separation..."
	case StatusFinalizing:
		return "Embedding metadata into stems..."
	case StatusPackaging:
		return "Creating download package..."
	}
	return "Processing..."
}

// StatusDetails is the secondary line under the progress bar (legacy
// getStatusDetails, verbatim).
func StatusDetails(status string) string {
	switch status {
	case StatusPending:
		return "Your job is in the queue and will start soon"
	case StatusConverting:
		return "Converting to 24-bit/48kHz professional format"
	case StatusAnalyzing:
		return "Using librosa to detect tempo and beats"
	case StatusSeparating:
		return "Using Demucs AI to separate vocals, drums, bass, and other instruments"
	case StatusFinalizing:
		return "Adding tempo information to each stem file"
	case StatusPackaging:
		return "Bundling stems and creating DAWproject file"
	}
	return ""
}

// FourStems and SixStems are the Demucs output names per model family.
var (
	FourStems = []string{"vocals", "drums", "bass", "other"}
	SixStems  = []string{"vocals", "drums", "bass", "other", "guitar", "piano"}
)

// ModelFor maps a quality preset to its Demucs model and stem names.
func ModelFor(quality string) (model string, stems []string) {
	switch quality {
	case QualityHigh:
		return "htdemucs_ft", FourStems
	case QualityHigh6:
		return "htdemucs_6s", SixStems
	default:
		return "htdemucs", FourStems
	}
}
