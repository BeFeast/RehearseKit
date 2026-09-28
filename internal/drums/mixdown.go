package drums

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"

	"github.com/BeFeast/RehearseKit/internal/jobs"
	"github.com/BeFeast/RehearseKit/internal/pipeline/peaks"
)

// NoDrumsMix is the name of the cached render: every stem but drums, summed
// at unity. It goes into drums.dawproject as the backing track so the
// exported project plays the song with the drums replaced by the MIDI.
const NoDrumsMix = "nodrums"

// mixChunk is the number of frames summed per read.
const mixChunk = 1 << 16

// RenderNoDrums sums the job's non-drum stems into mixes/nodrums.wav and
// returns the path. The file is written once (32-bit float, so the sum of
// five 24-bit stems is stored exactly, clipping or not) and reused; a
// concurrent request for the same job waits for the first render.
func (s *Store) RenderNoDrums(ctx context.Context, j *jobs.Job) (string, error) {
	out, err := s.layout.MixPath(j.ID, NoDrumsMix)
	if err != nil {
		return "", err
	}
	defer s.lock("mix:" + j.ID)()
	if st, err := os.Stat(out); err == nil && st.Size() > 0 {
		return out, nil
	}
	var inputs []string
	for _, st := range j.Stems {
		if st.Name == "drums" {
			continue
		}
		p, err := s.layout.StemPath(j.ID, st.Name)
		if err != nil {
			return "", err
		}
		inputs = append(inputs, p)
	}
	if len(inputs) == 0 {
		return "", errors.New("drums: no stems to mix")
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return "", err
	}
	tmp := out + ".part"
	if err := sumWAVs(ctx, tmp, inputs); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	if err := os.Rename(tmp, out); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return out, nil
}

// sumWAVs writes the sample-wise sum of the inputs as a 32-bit float WAV.
// All inputs must share the sample rate and channel count; the output is as
// long as the longest input (shorter ones contribute silence at the end).
func sumWAVs(ctx context.Context, out string, inputs []string) error {
	type src struct {
		f   *os.File
		dec *peaks.Decoder
	}
	var srcs []src
	defer func() {
		for _, s := range srcs {
			s.f.Close()
		}
	}()
	var rate uint32
	var channels uint16
	var frames uint64
	for _, p := range inputs {
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		dec, err := peaks.OpenWAV(f)
		if err != nil {
			f.Close()
			return fmt.Errorf("%s: %w", filepath.Base(p), err)
		}
		if len(srcs) == 0 {
			rate, channels = dec.SampleRate, dec.Channels
		} else if dec.SampleRate != rate || dec.Channels != channels {
			f.Close()
			return fmt.Errorf("%s: %d Hz/%d ch, want %d Hz/%d ch", filepath.Base(p), dec.SampleRate, dec.Channels, rate, channels)
		}
		if dec.Frames > frames {
			frames = dec.Frames
		}
		srcs = append(srcs, src{f, dec})
	}
	ch := int(channels)
	if frames*uint64(ch)*4 > math.MaxUint32-64 {
		return errors.New("drums: mix longer than a WAV can hold")
	}
	f, err := os.Create(out)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriterSize(f, 1<<20)
	if _, err := w.Write(floatHeader(rate, channels, frames)); err != nil {
		return err
	}
	acc := make([]float64, mixChunk*ch)
	buf := make([]float32, mixChunk*ch)
	raw := make([]byte, mixChunk*ch*4)
	done := make([]bool, len(srcs))
	for written := uint64(0); written < frames; {
		if err := ctx.Err(); err != nil {
			return err
		}
		n := mixChunk
		if left := frames - written; left < uint64(n) {
			n = int(left)
		}
		for i := range acc[:n*ch] {
			acc[i] = 0
		}
		for i, s := range srcs {
			if done[i] {
				continue
			}
			got := 0
			for got < n {
				k, err := s.dec.ReadFrames(buf[got*ch : n*ch])
				if k > 0 {
					for j := got * ch; j < (got+k)*ch; j++ {
						acc[j] += float64(buf[j])
					}
					got += k
				}
				if err == io.EOF {
					done[i] = true
					break
				}
				if err != nil {
					return err
				}
			}
		}
		for i := 0; i < n*ch; i++ {
			binary.LittleEndian.PutUint32(raw[i*4:], math.Float32bits(float32(acc[i])))
		}
		if _, err := w.Write(raw[:n*ch*4]); err != nil {
			return err
		}
		written += uint64(n)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	return f.Sync()
}

// floatHeader is a RIFF/WAVE header for WAVE_FORMAT_IEEE_FLOAT: an
// 18-byte fmt chunk (cbSize 0), the fact chunk non-PCM formats require,
// and the data chunk header.
func floatHeader(rate uint32, channels uint16, frames uint64) []byte {
	blockAlign := uint32(channels) * 4
	dataLen := uint32(frames) * blockAlign
	h := make([]byte, 0, 58)
	le := binary.LittleEndian
	h = append(h, "RIFF"...)
	h = le.AppendUint32(h, 4+(8+18)+(8+4)+(8+dataLen))
	h = append(h, "WAVE"...)
	h = append(h, "fmt "...)
	h = le.AppendUint32(h, 18)
	h = le.AppendUint16(h, 3) // IEEE float
	h = le.AppendUint16(h, channels)
	h = le.AppendUint32(h, rate)
	h = le.AppendUint32(h, rate*blockAlign)
	h = le.AppendUint16(h, uint16(blockAlign))
	h = le.AppendUint16(h, 32)
	h = le.AppendUint16(h, 0) // cbSize
	h = append(h, "fact"...)
	h = le.AppendUint32(h, 4)
	h = le.AppendUint32(h, uint32(frames))
	h = append(h, "data"...)
	h = le.AppendUint32(h, dataLen)
	return h
}
