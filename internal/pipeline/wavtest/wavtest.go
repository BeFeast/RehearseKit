// Package wavtest writes small synthetic WAV files for tests.
package wavtest

import (
	"bufio"
	"encoding/binary"
	"math"
	"os"
)

// Options describes the file to write.
type Options struct {
	SampleRate int     // default 48000
	Channels   int     // default 2
	BitDepth   int     // 16 or 24; default 24
	Seconds    float64 // default 1
	Frequency  float64 // Hz; default 440
	Amplitude  float64 // 0..1; default 0.5
}

func (o *Options) defaults() {
	if o.SampleRate == 0 {
		o.SampleRate = 48000
	}
	if o.Channels == 0 {
		o.Channels = 2
	}
	if o.BitDepth == 0 {
		o.BitDepth = 24
	}
	if o.Seconds == 0 {
		o.Seconds = 1
	}
	if o.Frequency == 0 {
		o.Frequency = 440
	}
	if o.Amplitude == 0 {
		o.Amplitude = 0.5
	}
}

// Write creates a PCM WAV with a sine tone at path. Returns the frame count.
func Write(path string, o Options) (int64, error) {
	o.defaults()
	frames := int64(o.Seconds * float64(o.SampleRate))
	bps := o.BitDepth / 8
	blockAlign := o.Channels * bps
	dataLen := frames * int64(blockAlign)
	f, err := os.Create(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	hdr := make([]byte, 44)
	copy(hdr[0:], "RIFF")
	binary.LittleEndian.PutUint32(hdr[4:], uint32(36+dataLen))
	copy(hdr[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(hdr[16:], 16)
	binary.LittleEndian.PutUint16(hdr[20:], 1)
	binary.LittleEndian.PutUint16(hdr[22:], uint16(o.Channels))
	binary.LittleEndian.PutUint32(hdr[24:], uint32(o.SampleRate))
	binary.LittleEndian.PutUint32(hdr[28:], uint32(o.SampleRate*blockAlign))
	binary.LittleEndian.PutUint16(hdr[32:], uint16(blockAlign))
	binary.LittleEndian.PutUint16(hdr[34:], uint16(o.BitDepth))
	copy(hdr[36:], "data")
	binary.LittleEndian.PutUint32(hdr[40:], uint32(dataLen))
	if _, err := w.Write(hdr); err != nil {
		return 0, err
	}
	sample := make([]byte, bps)
	for i := int64(0); i < frames; i++ {
		v := o.Amplitude * math.Sin(2*math.Pi*o.Frequency*float64(i)/float64(o.SampleRate))
		for c := 0; c < o.Channels; c++ {
			switch o.BitDepth {
			case 16:
				binary.LittleEndian.PutUint16(sample, uint16(int16(v*32767)))
			case 24:
				n := int32(v * 8388607)
				sample[0], sample[1], sample[2] = byte(n), byte(n>>8), byte(n>>16)
			default:
				binary.LittleEndian.PutUint32(sample, uint32(int32(v*2147483647)))
			}
			if _, err := w.Write(sample); err != nil {
				return 0, err
			}
		}
	}
	return frames, w.Flush()
}
