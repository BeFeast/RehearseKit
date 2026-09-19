// Package wavcheck validates that a file is the WAV format stems must have:
// 24-bit integer PCM, 48 kHz, stereo, with at least one frame.
package wavcheck

import (
	"fmt"
	"os"

	"github.com/BeFeast/RehearseKit/internal/pipeline/peaks"
)

// Required stem format.
const (
	SampleRate = 48000
	BitDepth   = 24
	Channels   = 2
)

// Info is the parsed header of a checked file.
type Info struct {
	Frames     int64
	SampleRate int32
	BitDepth   int16
	Channels   int16
	Bytes      int64
}

// Duration returns the audio length in seconds.
func (i Info) Duration() float64 { return float64(i.Frames) / float64(i.SampleRate) }

// Read parses the header of any supported WAV without enforcing the stem format.
func Read(path string) (Info, error) {
	f, err := os.Open(path)
	if err != nil {
		return Info{}, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return Info{}, err
	}
	d, err := peaks.OpenWAV(f)
	if err != nil {
		return Info{}, err
	}
	return Info{
		Frames: int64(d.Frames), SampleRate: int32(d.SampleRate), BitDepth: int16(d.BitDepth),
		Channels: int16(d.Channels), Bytes: st.Size(),
	}, nil
}

// Check parses the header and enforces the stem format.
func Check(path string) (Info, error) {
	info, err := Read(path)
	if err != nil {
		return info, err
	}
	if info.SampleRate != SampleRate || info.BitDepth != BitDepth || info.Channels != Channels {
		return info, fmt.Errorf("wav: got %d-bit/%d Hz/%d ch, want %d-bit/%d Hz/%d ch",
			info.BitDepth, info.SampleRate, info.Channels, BitDepth, SampleRate, Channels)
	}
	if info.Frames == 0 {
		return info, fmt.Errorf("wav: no audio frames")
	}
	return info, nil
}
