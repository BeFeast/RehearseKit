package peaks

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
)

// WAV format tags.
const (
	formatPCM        = 1
	formatFloat      = 3
	formatExtensible = 0xFFFE
)

// Decoder streams normalised float32 frames out of a RIFF/WAVE file. It
// supports 16/24/32-bit integer PCM and 32-bit IEEE float, plain or
// WAVE_FORMAT_EXTENSIBLE.
type Decoder struct {
	Channels   uint16
	SampleRate uint32
	BitDepth   uint16
	Float      bool
	Frames     uint64

	r         io.Reader
	frameSize int
	remaining uint64 // frames left to read
	buf       []byte
}

// OpenWAV parses the RIFF header and positions the reader at the first
// sample of the data chunk.
func OpenWAV(rs io.ReadSeeker) (*Decoder, error) {
	if _, err := rs.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	var riff [12]byte
	if _, err := io.ReadFull(rs, riff[:]); err != nil {
		return nil, fmt.Errorf("wav: short header: %w", err)
	}
	if string(riff[:4]) != "RIFF" || string(riff[8:12]) != "WAVE" {
		return nil, errors.New("wav: not a RIFF/WAVE file")
	}
	d := &Decoder{r: rs}
	haveFmt := false
	for {
		var ch [8]byte
		if _, err := io.ReadFull(rs, ch[:]); err != nil {
			return nil, errors.New("wav: no data chunk")
		}
		id := string(ch[:4])
		size := binary.LittleEndian.Uint32(ch[4:])
		switch id {
		case "fmt ":
			if size < 16 {
				return nil, errors.New("wav: fmt chunk too small")
			}
			body := make([]byte, size)
			if _, err := io.ReadFull(rs, body); err != nil {
				return nil, err
			}
			tag := binary.LittleEndian.Uint16(body[0:])
			d.Channels = binary.LittleEndian.Uint16(body[2:])
			d.SampleRate = binary.LittleEndian.Uint32(body[4:])
			d.BitDepth = binary.LittleEndian.Uint16(body[14:])
			if tag == formatExtensible {
				if size < 40 {
					return nil, errors.New("wav: extensible fmt chunk too small")
				}
				tag = binary.LittleEndian.Uint16(body[24:]) // first 2 bytes of SubFormat GUID
			}
			switch {
			case tag == formatPCM && (d.BitDepth == 16 || d.BitDepth == 24 || d.BitDepth == 32):
			case tag == formatFloat && d.BitDepth == 32:
				d.Float = true
			default:
				return nil, fmt.Errorf("wav: unsupported format tag %d / %d-bit", tag, d.BitDepth)
			}
			if d.Channels == 0 {
				return nil, errors.New("wav: zero channels")
			}
			d.frameSize = int(d.Channels) * int(d.BitDepth/8)
			haveFmt = true
			if size%2 == 1 {
				if _, err := rs.Seek(1, io.SeekCurrent); err != nil {
					return nil, err
				}
			}
		case "data":
			if !haveFmt {
				return nil, errors.New("wav: data chunk before fmt chunk")
			}
			dataLen := uint64(size)
			if size == 0 || size == 0xFFFFFFFF {
				// Streaming writers leave the size unset; use what is left.
				pos, err := rs.Seek(0, io.SeekCurrent)
				if err != nil {
					return nil, err
				}
				end, err := rs.Seek(0, io.SeekEnd)
				if err != nil {
					return nil, err
				}
				if _, err := rs.Seek(pos, io.SeekStart); err != nil {
					return nil, err
				}
				dataLen = uint64(end - pos)
			}
			d.Frames = dataLen / uint64(d.frameSize)
			d.remaining = d.Frames
			d.buf = make([]byte, d.frameSize*4096)
			return d, nil
		default:
			skip := int64(size)
			if size%2 == 1 {
				skip++
			}
			if _, err := rs.Seek(skip, io.SeekCurrent); err != nil {
				return nil, err
			}
		}
	}
}

// ReadFrames fills dst (interleaved, len must be a multiple of Channels)
// with normalised samples in [-1, 1] and returns the number of frames read.
// It returns io.EOF once all frames have been delivered.
func (d *Decoder) ReadFrames(dst []float32) (int, error) {
	ch := int(d.Channels)
	want := len(dst) / ch
	if want == 0 {
		return 0, errors.New("wav: destination too small")
	}
	if d.remaining == 0 {
		return 0, io.EOF
	}
	if uint64(want) > d.remaining {
		want = int(d.remaining)
	}
	if want*d.frameSize > len(d.buf) {
		want = len(d.buf) / d.frameSize
	}
	raw := d.buf[:want*d.frameSize]
	n, err := io.ReadFull(d.r, raw)
	frames := n / d.frameSize
	d.decode(raw[:frames*d.frameSize], dst)
	d.remaining -= uint64(frames)
	if err != nil {
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			// Truncated file: report what we have, then EOF.
			d.remaining = 0
			if frames == 0 {
				return 0, io.EOF
			}
			return frames, nil
		}
		return frames, err
	}
	return frames, nil
}

func (d *Decoder) decode(raw []byte, dst []float32) {
	switch {
	case d.Float:
		for i := 0; i+4 <= len(raw); i += 4 {
			dst[i/4] = math.Float32frombits(binary.LittleEndian.Uint32(raw[i:]))
		}
	case d.BitDepth == 16:
		for i := 0; i+2 <= len(raw); i += 2 {
			dst[i/2] = float32(int16(binary.LittleEndian.Uint16(raw[i:]))) / 32768
		}
	case d.BitDepth == 24:
		for i := 0; i+3 <= len(raw); i += 3 {
			v := int32(uint32(raw[i])<<8|uint32(raw[i+1])<<16|uint32(raw[i+2])<<24) >> 8
			dst[i/3] = float32(v) / 8388608
		}
	case d.BitDepth == 32:
		for i := 0; i+4 <= len(raw); i += 4 {
			dst[i/4] = float32(int32(binary.LittleEndian.Uint32(raw[i:]))) / 2147483648
		}
	}
}
