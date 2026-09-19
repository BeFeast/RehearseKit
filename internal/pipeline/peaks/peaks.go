// Package peaks writes and reads RehearseKit waveform peak files (".pk").
//
// The format is a min/max mip pyramid over the PCM frames of one WAV file,
// little-endian throughout:
//
//	magic     "RKPK"
//	u16       version (1)
//	u16       channels
//	u32       sampleRate
//	u64       frames
//	u8        stages
//	stages ×  { u8 shift; u32 numPeaks; u64 dataOffset }
//
// Each stage covers 2^shift frames per peak and numPeaks = ceil(frames /
// 2^shift). Stage data at dataOffset is laid out channel-major:
//
//	channels × numPeaks × (int8 min, int8 max)
//
// so peak p of channel c sits at dataOffset + (c*numPeaks + p)*2. Samples
// are mapped to int8 as round(clamp(x, -1, 1) * 127). The default pyramid
// uses shifts 6, 9, 12 and 15 (64 … 32768 frames per peak); coarser stages
// are derived from the finest one, which is exact because rounding is
// monotonic.
package peaks

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
)

// Magic is the file signature.
const Magic = "RKPK"

// Version is the format version written by this package.
const Version = 1

// DefaultShifts is the stage pyramid required by the design contract.
var DefaultShifts = []uint8{6, 9, 12, 15}

const (
	headerSize = 4 + 2 + 2 + 4 + 8 + 1 // 21 bytes
	stageSize  = 1 + 4 + 8             // 13 bytes
)

// Errors.
var (
	ErrBadMagic   = errors.New("peaks: bad magic")
	ErrBadVersion = errors.New("peaks: unsupported version")
	ErrCorrupt    = errors.New("peaks: corrupt file")
)

// Stage is one mip level.
type Stage struct {
	Shift      uint8
	NumPeaks   uint32
	DataOffset uint64
	// Data holds, per channel, NumPeaks pairs of (min, max).
	Data [][]int8
}

// Min returns the minimum of peak p on channel ch.
func (s *Stage) Min(ch, p int) int8 { return s.Data[ch][2*p] }

// Max returns the maximum of peak p on channel ch.
func (s *Stage) Max(ch, p int) int8 { return s.Data[ch][2*p+1] }

// File is a decoded peaks file.
type File struct {
	Channels   uint16
	SampleRate uint32
	Frames     uint64
	Stages     []Stage
}

// StageFor returns the finest stage whose frames-per-peak (2^shift) does
// not exceed framesPerPixel, or the finest stage when none qualifies.
func (f *File) StageFor(framesPerPixel float64) *Stage {
	best := &f.Stages[0]
	for i := range f.Stages {
		if float64(uint64(1)<<f.Stages[i].Shift) <= framesPerPixel {
			best = &f.Stages[i]
		}
	}
	return best
}

// Quantize maps a normalised sample to the int8 peak scale.
func Quantize(x float32) int8 {
	if x > 1 {
		x = 1
	} else if x < -1 {
		x = -1
	}
	return int8(math.Round(float64(x) * 127))
}

// numPeaks returns ceil(frames / 2^shift).
func numPeaks(frames uint64, shift uint8) uint64 {
	if frames == 0 {
		return 0
	}
	return (frames + (uint64(1) << shift) - 1) >> shift
}

// Build computes the pyramid for the given shifts from a WAV stream. The
// audio is read in blocks; only the finest stage (2 bytes per peak per
// channel) is held in memory.
func Build(wav io.ReadSeeker, shifts []uint8) (*File, error) {
	if len(shifts) == 0 {
		shifts = DefaultShifts
	}
	for i := 1; i < len(shifts); i++ {
		if shifts[i] <= shifts[i-1] {
			return nil, errors.New("peaks: shifts must be strictly increasing")
		}
	}
	dec, err := OpenWAV(wav)
	if err != nil {
		return nil, err
	}
	ch := int(dec.Channels)
	if ch == 0 || ch > 64 {
		return nil, fmt.Errorf("peaks: unsupported channel count %d", ch)
	}
	if dec.Frames > math.MaxUint32<<shifts[0] {
		return nil, errors.New("peaks: too many frames for u32 peak counts")
	}

	// Finest stage, accumulated frame block by frame block.
	base := shifts[0]
	baseN := numPeaks(dec.Frames, base)
	fine := make([][]int8, ch)
	for c := range fine {
		fine[c] = make([]int8, 2*baseN)
	}
	perPeak := uint64(1) << base
	block := make([]float32, ch*4096)
	var frame uint64
	curMin := make([]float32, ch)
	curMax := make([]float32, ch)
	resetCur := func() {
		for c := 0; c < ch; c++ {
			curMin[c], curMax[c] = math.MaxFloat32, -math.MaxFloat32
		}
	}
	resetCur()
	flush := func(p uint64) {
		for c := 0; c < ch; c++ {
			fine[c][2*p] = Quantize(curMin[c])
			fine[c][2*p+1] = Quantize(curMax[c])
		}
	}
	for {
		n, err := dec.ReadFrames(block)
		for i := 0; i < n; i++ {
			for c := 0; c < ch; c++ {
				v := block[i*ch+c]
				if v < curMin[c] {
					curMin[c] = v
				}
				if v > curMax[c] {
					curMax[c] = v
				}
			}
			frame++
			if frame%perPeak == 0 {
				flush(frame/perPeak - 1)
				resetCur()
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	if frame != dec.Frames {
		return nil, fmt.Errorf("peaks: wav declared %d frames, read %d", dec.Frames, frame)
	}
	if frame%perPeak != 0 {
		flush(frame / perPeak)
	}

	f := &File{Channels: uint16(ch), SampleRate: dec.SampleRate, Frames: dec.Frames}
	f.Stages = append(f.Stages, Stage{Shift: base, NumPeaks: uint32(baseN), Data: fine})
	for _, sh := range shifts[1:] {
		prev := &f.Stages[len(f.Stages)-1]
		f.Stages = append(f.Stages, downsample(prev, sh))
	}
	return f, nil
}

// downsample derives a coarser stage from a finer one.
func downsample(prev *Stage, shift uint8) Stage {
	ratio := uint64(1) << (shift - prev.Shift)
	n := uint64(prev.NumPeaks)
	out := numPeaks(n, shift-prev.Shift)
	st := Stage{Shift: shift, NumPeaks: uint32(out), Data: make([][]int8, len(prev.Data))}
	for c, src := range prev.Data {
		dst := make([]int8, 2*out)
		for p := uint64(0); p < out; p++ {
			lo, hi := p*ratio, min((p+1)*ratio, n)
			mn, mx := src[2*lo], src[2*lo+1]
			for q := lo + 1; q < hi; q++ {
				mn = min(mn, src[2*q])
				mx = max(mx, src[2*q+1])
			}
			dst[2*p], dst[2*p+1] = mn, mx
		}
		st.Data[c] = dst
	}
	return st
}

// Write serialises f. Data offsets are computed here.
func Write(w io.Writer, f *File) error {
	if len(f.Stages) == 0 || len(f.Stages) > 255 {
		return errors.New("peaks: need 1..255 stages")
	}
	bw := bufio.NewWriter(w)
	offset := uint64(headerSize + stageSize*len(f.Stages))
	hdr := make([]byte, headerSize)
	copy(hdr, Magic)
	binary.LittleEndian.PutUint16(hdr[4:], Version)
	binary.LittleEndian.PutUint16(hdr[6:], f.Channels)
	binary.LittleEndian.PutUint32(hdr[8:], f.SampleRate)
	binary.LittleEndian.PutUint64(hdr[12:], f.Frames)
	hdr[20] = uint8(len(f.Stages))
	if _, err := bw.Write(hdr); err != nil {
		return err
	}
	for i := range f.Stages {
		st := &f.Stages[i]
		st.DataOffset = offset
		var rec [stageSize]byte
		rec[0] = st.Shift
		binary.LittleEndian.PutUint32(rec[1:], st.NumPeaks)
		binary.LittleEndian.PutUint64(rec[5:], st.DataOffset)
		if _, err := bw.Write(rec[:]); err != nil {
			return err
		}
		offset += uint64(f.Channels) * uint64(st.NumPeaks) * 2
	}
	for _, st := range f.Stages {
		if len(st.Data) != int(f.Channels) {
			return fmt.Errorf("peaks: stage %d has %d channels, want %d", st.Shift, len(st.Data), f.Channels)
		}
		for _, chData := range st.Data {
			if len(chData) != 2*int(st.NumPeaks) {
				return fmt.Errorf("peaks: stage %d channel data length %d, want %d", st.Shift, len(chData), 2*st.NumPeaks)
			}
			if _, err := bw.Write(int8sToBytes(chData)); err != nil {
				return err
			}
		}
	}
	return bw.Flush()
}

// Read parses a whole peaks file.
func Read(r io.ReadSeeker) (*File, error) {
	f, err := ReadHeader(r)
	if err != nil {
		return nil, err
	}
	for i := range f.Stages {
		if err := f.LoadStage(r, i); err != nil {
			return nil, err
		}
	}
	return f, nil
}

// ReadHeader parses the header and stage table without loading stage data.
func ReadHeader(r io.ReadSeeker) (*File, error) {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	hdr := make([]byte, headerSize)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return nil, ErrCorrupt
	}
	if string(hdr[:4]) != Magic {
		return nil, ErrBadMagic
	}
	if v := binary.LittleEndian.Uint16(hdr[4:]); v != Version {
		return nil, fmt.Errorf("%w: %d", ErrBadVersion, v)
	}
	f := &File{
		Channels:   binary.LittleEndian.Uint16(hdr[6:]),
		SampleRate: binary.LittleEndian.Uint32(hdr[8:]),
		Frames:     binary.LittleEndian.Uint64(hdr[12:]),
	}
	stages := int(hdr[20])
	if f.Channels == 0 || stages == 0 {
		return nil, ErrCorrupt
	}
	table := make([]byte, stageSize*stages)
	if _, err := io.ReadFull(r, table); err != nil {
		return nil, ErrCorrupt
	}
	for i := 0; i < stages; i++ {
		rec := table[i*stageSize:]
		f.Stages = append(f.Stages, Stage{
			Shift:      rec[0],
			NumPeaks:   binary.LittleEndian.Uint32(rec[1:]),
			DataOffset: binary.LittleEndian.Uint64(rec[5:]),
		})
	}
	return f, nil
}

// LoadStage reads stage i's data into f.Stages[i].Data.
func (f *File) LoadStage(r io.ReadSeeker, i int) error {
	st := &f.Stages[i]
	if st.DataOffset > math.MaxInt64 {
		return ErrCorrupt
	}
	if _, err := r.Seek(int64(st.DataOffset), io.SeekStart); err != nil {
		return err
	}
	st.Data = make([][]int8, f.Channels)
	buf := make([]byte, 2*int(st.NumPeaks))
	for c := range st.Data {
		if _, err := io.ReadFull(r, buf); err != nil {
			return ErrCorrupt
		}
		st.Data[c] = bytesToInt8s(buf)
	}
	return nil
}

func int8sToBytes(s []int8) []byte {
	b := make([]byte, len(s))
	for i, v := range s {
		b[i] = byte(v)
	}
	return b
}

func bytesToInt8s(b []byte) []int8 {
	s := make([]int8, len(b))
	for i, v := range b {
		s[i] = int8(v)
	}
	return s
}
