package peaks

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"
)

// writeWAV builds an in-memory WAV. samples are interleaved, normalised.
func writeWAV(t *testing.T, channels int, sampleRate int, bits int, float bool, samples []float32) []byte {
	t.Helper()
	var data bytes.Buffer
	for _, s := range samples {
		switch {
		case float:
			_ = binary.Write(&data, binary.LittleEndian, s)
		case bits == 16:
			_ = binary.Write(&data, binary.LittleEndian, int16(math.Round(float64(s)*32767)))
		case bits == 24:
			v := int32(math.Round(float64(s) * 8388607))
			data.Write([]byte{byte(v), byte(v >> 8), byte(v >> 16)})
		case bits == 32:
			_ = binary.Write(&data, binary.LittleEndian, int32(math.Round(float64(s)*2147483647)))
		default:
			t.Fatalf("bits %d", bits)
		}
	}
	var out bytes.Buffer
	tag := uint16(1)
	if float {
		tag = 3
	}
	blockAlign := channels * bits / 8
	out.WriteString("RIFF")
	_ = binary.Write(&out, binary.LittleEndian, uint32(4+8+16+8+4+8+data.Len())) // includes a LIST chunk
	out.WriteString("WAVE")
	out.WriteString("fmt ")
	_ = binary.Write(&out, binary.LittleEndian, uint32(16))
	_ = binary.Write(&out, binary.LittleEndian, tag)
	_ = binary.Write(&out, binary.LittleEndian, uint16(channels))
	_ = binary.Write(&out, binary.LittleEndian, uint32(sampleRate))
	_ = binary.Write(&out, binary.LittleEndian, uint32(sampleRate*blockAlign))
	_ = binary.Write(&out, binary.LittleEndian, uint16(blockAlign))
	_ = binary.Write(&out, binary.LittleEndian, uint16(bits))
	// An unrelated chunk the reader must skip.
	out.WriteString("LIST")
	_ = binary.Write(&out, binary.LittleEndian, uint32(4))
	out.WriteString("INFO")
	out.WriteString("data")
	_ = binary.Write(&out, binary.LittleEndian, uint32(data.Len()))
	out.Write(data.Bytes())
	return out.Bytes()
}

func TestRampBitExact(t *testing.T) {
	// Mono ramp over 1000 frames: sample i = -1 + 2*i/999. With shift 6
	// each peak covers 64 frames; the last peak (index 15) covers 40.
	const frames = 1000
	samples := make([]float32, frames)
	for i := range samples {
		samples[i] = float32(-1 + 2*float64(i)/(frames-1))
	}
	for _, tc := range []struct {
		bits  int
		float bool
	}{{16, false}, {24, false}, {32, false}, {32, true}} {
		wav := writeWAV(t, 1, 48000, tc.bits, tc.float, samples)
		f, err := Build(bytes.NewReader(wav), nil)
		if err != nil {
			t.Fatalf("%d-bit float=%v: %v", tc.bits, tc.float, err)
		}
		if f.Channels != 1 || f.SampleRate != 48000 || f.Frames != frames {
			t.Fatalf("header %+v", f)
		}
		if len(f.Stages) != 4 || f.Stages[0].Shift != 6 || f.Stages[3].Shift != 15 {
			t.Fatalf("stages %+v", f.Stages)
		}
		st := f.Stages[0]
		if st.NumPeaks != 16 {
			t.Fatalf("numPeaks %d", st.NumPeaks)
		}
		for p := 0; p < 16; p++ {
			lo, hi := p*64, min((p+1)*64, frames)-1
			// The quantised value of a 16-bit ramp differs by at most one
			// LSB of int8 from the float ramp; compare against the decoded
			// sample as the decoder sees it, computed the same way.
			wantMin, wantMax := Quantize(decodeLike(samples[lo], tc.bits, tc.float)), Quantize(decodeLike(samples[hi], tc.bits, tc.float))
			if st.Min(0, p) != wantMin || st.Max(0, p) != wantMax {
				t.Errorf("%d-bit float=%v peak %d: got (%d,%d) want (%d,%d)", tc.bits, tc.float, p, st.Min(0, p), st.Max(0, p), wantMin, wantMax)
			}
		}
		// Coarse stages: stage 9 peak 0 spans fine peaks 0..7, peak 1 spans 8..15.
		s9 := f.Stages[1]
		if s9.NumPeaks != 2 || s9.Min(0, 0) != st.Min(0, 0) || s9.Max(0, 0) != st.Max(0, 7) || s9.Min(0, 1) != st.Min(0, 8) || s9.Max(0, 1) != st.Max(0, 15) {
			t.Errorf("stage 9 %v", s9.Data[0])
		}
		for _, s := range f.Stages[2:] {
			if s.NumPeaks != 1 || s.Min(0, 0) != -127 || s.Max(0, 0) != 127 {
				t.Errorf("stage %d: %v", s.Shift, s.Data[0])
			}
		}
	}
}

// decodeLike reproduces the quantisation the WAV writer and decoder apply.
func decodeLike(s float32, bits int, float bool) float32 {
	switch {
	case float:
		return s
	case bits == 16:
		return float32(int16(math.Round(float64(s)*32767))) / 32768
	case bits == 24:
		return float32(int32(math.Round(float64(s)*8388607))) / 8388608
	default:
		return float32(int32(math.Round(float64(s)*2147483647))) / 2147483648
	}
}

func TestStereoSine(t *testing.T) {
	const frames = 48000 // one second; 750 fine peaks
	samples := make([]float32, 2*frames)
	for i := 0; i < frames; i++ {
		ph := 2 * math.Pi * 440 * float64(i) / 48000
		samples[2*i] = float32(0.5 * math.Sin(ph)) // left: half scale
		samples[2*i+1] = float32(math.Sin(ph))     // right: full scale
	}
	wav := writeWAV(t, 2, 48000, 24, false, samples)
	f, err := Build(bytes.NewReader(wav), nil)
	if err != nil {
		t.Fatal(err)
	}
	if f.Channels != 2 || f.Frames != frames || f.Stages[0].NumPeaks != 750 {
		t.Fatalf("header %+v", f)
	}
	// 440 Hz at 48 kHz: a full period is ~109 frames, so every 64-frame
	// window sees at most half a period; but over 512 frames (stage 9)
	// every window sees full swing.
	s9 := f.Stages[1]
	for p := 0; p < int(s9.NumPeaks); p++ {
		if s9.Min(1, p) > -126 || s9.Max(1, p) < 126 {
			t.Errorf("right peak %d = (%d,%d), want full swing", p, s9.Min(1, p), s9.Max(1, p))
		}
		if s9.Min(0, p) > -62 || s9.Max(0, p) < 62 || s9.Min(0, p) < -64 || s9.Max(0, p) > 64 {
			t.Errorf("left peak %d = (%d,%d), want half swing", p, s9.Min(0, p), s9.Max(0, p))
		}
	}
	// Brute-force check of the finest stage against the raw samples.
	st := f.Stages[0]
	for p := 0; p < int(st.NumPeaks); p++ {
		for c := 0; c < 2; c++ {
			mn, mx := float32(1), float32(-1)
			for i := p * 64; i < min((p+1)*64, frames); i++ {
				v := decodeLike(samples[2*i+c], 24, false)
				mn, mx = min(mn, v), max(mx, v)
			}
			if st.Min(c, p) != Quantize(mn) || st.Max(c, p) != Quantize(mx) {
				t.Fatalf("peak %d ch %d: got (%d,%d) want (%d,%d)", p, c, st.Min(c, p), st.Max(c, p), Quantize(mn), Quantize(mx))
			}
		}
	}
}

func TestRoundTrip(t *testing.T) {
	const frames = 70000
	samples := make([]float32, 2*frames)
	for i := range samples {
		samples[i] = float32(math.Sin(float64(i) * 0.01))
	}
	wav := writeWAV(t, 2, 44100, 16, false, samples)
	f, err := Build(bytes.NewReader(wav), nil)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := Write(&buf, f); err != nil {
		t.Fatal(err)
	}
	// Header layout: 21 + 4*13 = 73 bytes before data.
	if got := f.Stages[0].DataOffset; got != 73 {
		t.Errorf("stage 0 data offset %d, want 73", got)
	}
	if !bytes.HasPrefix(buf.Bytes(), []byte("RKPK\x01\x00\x02\x00")) {
		t.Errorf("header bytes %x", buf.Bytes()[:8])
	}
	wantLen := 73
	for _, s := range f.Stages {
		wantLen += 2 * 2 * int(s.NumPeaks)
	}
	if buf.Len() != wantLen {
		t.Errorf("file length %d, want %d", buf.Len(), wantLen)
	}

	g, err := Read(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if g.Channels != f.Channels || g.SampleRate != f.SampleRate || g.Frames != f.Frames || len(g.Stages) != len(f.Stages) {
		t.Fatalf("header mismatch: %+v vs %+v", g, f)
	}
	for i := range f.Stages {
		a, b := f.Stages[i], g.Stages[i]
		if a.Shift != b.Shift || a.NumPeaks != b.NumPeaks || a.DataOffset != b.DataOffset {
			t.Fatalf("stage %d meta mismatch", i)
		}
		for c := range a.Data {
			if !bytes.Equal(int8sToBytes(a.Data[c]), int8sToBytes(b.Data[c])) {
				t.Fatalf("stage %d channel %d data mismatch", i, c)
			}
		}
	}
	// Channel-major layout: channel 1 of stage 0 starts right after channel 0.
	raw := buf.Bytes()
	off := int(f.Stages[0].DataOffset) + 2*int(f.Stages[0].NumPeaks)
	if int8(raw[off]) != f.Stages[0].Min(1, 0) || int8(raw[off+1]) != f.Stages[0].Max(1, 0) {
		t.Errorf("channel-major layout violated")
	}
	if s := g.StageFor(600); s.Shift != 9 {
		t.Errorf("StageFor(600) = shift %d, want 9", s.Shift)
	}
	if s := g.StageFor(10); s.Shift != 6 {
		t.Errorf("StageFor(10) = shift %d, want 6", s.Shift)
	}
}

func TestReadRejectsGarbage(t *testing.T) {
	if _, err := Read(bytes.NewReader([]byte("RIFF....WAVEfmt ..........................."))); err != ErrBadMagic {
		t.Errorf("err = %v", err)
	}
	if _, err := Read(bytes.NewReader([]byte("RKPK\x02\x00"))); err == nil {
		t.Error("truncated header accepted")
	}
	if _, err := Build(bytes.NewReader([]byte("nope")), nil); err == nil {
		t.Error("non-wav accepted")
	}
}
