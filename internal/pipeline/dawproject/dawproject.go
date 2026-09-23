// Package dawproject writes a DAWproject 1.0 archive
// (https://github.com/bitwig/dawproject): a zip holding project.xml,
// metadata.xml and the referenced audio files. One audio track per stem,
// each with a single clip spanning the song, warped from seconds onto the
// beat grid; a master track; the tempo in Transport.
//
// Without a grid (Project.Grid nil) the clip is warped linearly from the
// single detected tempo; when the tempo is unknown the project is written
// at 120 BPM (the format requires a value) and metadata.xml carries a
// Comment saying so. The warps map the whole file onto the corresponding
// number of beats either way, so audio plays at the original speed.
//
// With a grid the project carries the transcription: a warp per beat, a
// stepped TempoAutomation (Bitwig form: two linear points per beat), a
// TimeSignatureAutomation when the numerator changes, Markers, and one
// notes track per transcribed stem. Element order in Arrangement follows
// the schema: Lanes, Markers, TempoAutomation, TimeSignatureAutomation.
package dawproject

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"encoding/xml"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/BeFeast/RehearseKit/internal/pipeline/grid"
)

// DefaultBPM is used in project.xml when no tempo was detected.
const DefaultBPM = 120.0

// Stem is one audio file to put on its own track.
type Stem struct {
	Name string // "vocals"; also the track name (capitalised) and audio/<name>.wav
	Path string // local WAV file to copy into the archive
	// Duration in seconds; when 0 Project.DurationSeconds is used.
	Duration float64
}

// Marker is a named position in beats.
type Marker struct {
	Beat  float64
	Name  string
	Color string
}

// Note is one note event; Beat and Duration are project-absolute beats.
type Note struct {
	Beat     float64
	Duration float64
	Key      int
	Velocity float64 // 0..1
}

// NoteTrack is a notes track for a transcribed stem.
type NoteTrack struct {
	Stem    string // colour and id come from the stem name
	Name    string // track name; default "<Stem> MIDI"
	Channel int    // MIDI channel written on every note (0..15; 9 for drums)
	Notes   []Note
}

// Project describes the archive to write.
type Project struct {
	Name            string
	BPM             *float64
	DurationSeconds float64
	SampleRate      int
	Channels        int
	Stems           []Stem
	Year            int
	// Generator names the application (default "RehearseKit").
	Generator string
	// Version of the generator (default "rk").
	GeneratorVersion string

	// Grid, when set, replaces BPM as the source of the tempo map.
	Grid *grid.Map
	// Markers are written only when non-empty.
	Markers []Marker
	// NoteTracks are appended after the audio tracks, before the master.
	NoteTracks []NoteTrack
}

// Track colours, from the legacy generator.
var stemColors = map[string]string{
	"vocals": "#FF6B9D",
	"drums":  "#FFA500",
	"bass":   "#4169E1",
	"guitar": "#32CD32",
	"piano":  "#9370DB",
	"other":  "#808080",
}

func f6(v float64) string { return strconv.FormatFloat(v, 'f', 6, 64) }

// XML model. Element and attribute order follow the reference exports so
// strict readers (Cubase) are happy. New optional elements are pointers
// with omitempty so the flag-off output is unchanged byte for byte.

type xmlProject struct {
	XMLName     xml.Name       `xml:"Project"`
	Version     string         `xml:"version,attr"`
	Application xmlApplication `xml:"Application"`
	Transport   xmlTransport   `xml:"Transport"`
	Structure   xmlStructure   `xml:"Structure"`
	Arrangement xmlArrangement `xml:"Arrangement"`
}

type xmlApplication struct {
	Name    string `xml:"name,attr"`
	Version string `xml:"version,attr"`
}

type xmlTransport struct {
	Tempo         xmlRealParam    `xml:"Tempo"`
	TimeSignature xmlTimeSigParam `xml:"TimeSignature"`
}

type xmlRealParam struct {
	Max   string `xml:"max,attr"`
	Min   string `xml:"min,attr"`
	Unit  string `xml:"unit,attr"`
	Value string `xml:"value,attr"`
	ID    string `xml:"id,attr"`
	Name  string `xml:"name,attr,omitempty"`
}

type xmlTimeSigParam struct {
	Denominator int    `xml:"denominator,attr"`
	Numerator   int    `xml:"numerator,attr"`
	ID          string `xml:"id,attr"`
}

type xmlStructure struct {
	Tracks []xmlTrack `xml:"Track"`
}

type xmlTrack struct {
	ContentType string     `xml:"contentType,attr"`
	Loaded      bool       `xml:"loaded,attr"`
	ID          string     `xml:"id,attr"`
	Name        string     `xml:"name,attr"`
	Color       string     `xml:"color,attr,omitempty"`
	Channel     xmlChannel `xml:"Channel"`
}

type xmlChannel struct {
	AudioChannels int          `xml:"audioChannels,attr"`
	Destination   string       `xml:"destination,attr,omitempty"`
	Role          string       `xml:"role,attr"`
	Solo          bool         `xml:"solo,attr"`
	ID            string       `xml:"id,attr"`
	Name          string       `xml:"name,attr,omitempty"`
	Mute          xmlBoolParam `xml:"Mute"`
	Pan           xmlRealParam `xml:"Pan"`
	Volume        xmlRealParam `xml:"Volume"`
}

type xmlBoolParam struct {
	Value bool   `xml:"value,attr"`
	ID    string `xml:"id,attr"`
	Name  string `xml:"name,attr,omitempty"`
}

type xmlArrangement struct {
	ID                      string      `xml:"id,attr"`
	Lanes                   xmlLanes    `xml:"Lanes"`
	Markers                 *xmlMarkers `xml:"Markers,omitempty"`
	TempoAutomation         *xmlPoints  `xml:"TempoAutomation,omitempty"`
	TimeSignatureAutomation *xmlPoints  `xml:"TimeSignatureAutomation,omitempty"`
}

type xmlLanes struct {
	TimeUnit string     `xml:"timeUnit,attr,omitempty"`
	Track    string     `xml:"track,attr,omitempty"`
	ID       string     `xml:"id,attr"`
	Lanes    []xmlLanes `xml:"Lanes,omitempty"`
	Clips    *xmlClips  `xml:"Clips,omitempty"`
}

type xmlClips struct {
	ID    string    `xml:"id,attr"`
	Clips []xmlClip `xml:"Clip"`
}

type xmlClip struct {
	Time      string    `xml:"time,attr"`
	Duration  string    `xml:"duration,attr"`
	PlayStart string    `xml:"playStart,attr"`
	Name      string    `xml:"name,attr"`
	Warps     *xmlWarps `xml:"Warps,omitempty"`
	Notes     *xmlNotes `xml:"Notes,omitempty"`
}

type xmlWarps struct {
	ContentTimeUnit string    `xml:"contentTimeUnit,attr"`
	TimeUnit        string    `xml:"timeUnit,attr"`
	Audio           xmlAudio  `xml:"Audio"`
	Warps           []xmlWarp `xml:"Warp"`
}

type xmlAudio struct {
	Algorithm  string  `xml:"algorithm,attr,omitempty"`
	Channels   int     `xml:"channels,attr"`
	Duration   string  `xml:"duration,attr"`
	SampleRate int     `xml:"sampleRate,attr"`
	File       xmlFile `xml:"File"`
}

type xmlFile struct {
	Path string `xml:"path,attr"`
}

type xmlWarp struct {
	Time        string `xml:"time,attr"`
	ContentTime string `xml:"contentTime,attr"`
}

type xmlNotes struct {
	ID    string    `xml:"id,attr"`
	Notes []xmlNote `xml:"Note"`
}

type xmlNote struct {
	Time     string `xml:"time,attr"`
	Duration string `xml:"duration,attr"`
	Channel  int    `xml:"channel,attr"`
	Key      int    `xml:"key,attr"`
	Vel      string `xml:"vel,attr"`
	Rel      string `xml:"rel,attr"`
}

type xmlMarkers struct {
	ID      string      `xml:"id,attr"`
	Markers []xmlMarker `xml:"Marker"`
}

type xmlMarker struct {
	Time  string `xml:"time,attr"`
	Name  string `xml:"name,attr"`
	Color string `xml:"color,attr,omitempty"`
}

// xmlPoints is TempoAutomation / TimeSignatureAutomation: Target first,
// then points of one kind.
type xmlPoints struct {
	Unit    string            `xml:"unit,attr,omitempty"`
	ID      string            `xml:"id,attr"`
	Target  xmlTarget         `xml:"Target"`
	Real    []xmlRealPoint    `xml:"RealPoint,omitempty"`
	TimeSig []xmlTimeSigPoint `xml:"TimeSignaturePoint,omitempty"`
}

type xmlTarget struct {
	Parameter string `xml:"parameter,attr"`
}

type xmlRealPoint struct {
	Time          string `xml:"time,attr"`
	Value         string `xml:"value,attr"`
	Interpolation string `xml:"interpolation,attr"`
}

type xmlTimeSigPoint struct {
	Time        string `xml:"time,attr"`
	Numerator   int    `xml:"numerator,attr"`
	Denominator int    `xml:"denominator,attr"`
}

type xmlMetaData struct {
	XMLName xml.Name `xml:"MetaData"`
	Title   string   `xml:"Title"`
	Year    string   `xml:"Year,omitempty"`
	Comment string   `xml:"Comment,omitempty"`
}

func (p *Project) defaults() {
	if p.Generator == "" {
		p.Generator = "RehearseKit"
	}
	if p.GeneratorVersion == "" {
		p.GeneratorVersion = "rk"
	}
	if p.SampleRate == 0 {
		p.SampleRate = 48000
	}
	if p.Channels == 0 {
		p.Channels = 2
	}
	if p.Year == 0 {
		p.Year = time.Now().Year()
	}
}

// bpm returns the tempo to write and whether it was detected.
func (p *Project) bpm() (float64, bool) {
	if p.Grid != nil {
		return p.Grid.BPM(), true
	}
	if p.BPM != nil && *p.BPM > 0 {
		return *p.BPM, true
	}
	return DefaultBPM, false
}

// TrackName capitalises a stem name for display.
func TrackName(stem string) string {
	if stem == "" {
		return ""
	}
	return strings.ToUpper(stem[:1]) + stem[1:]
}

// AudioPath is the archive path of a stem.
func AudioPath(stem string) string { return "audio/" + stem + ".wav" }

// beat maps seconds to project beats: the grid when present, else the
// single tempo.
func (p *Project) beat(sec, bpm float64) float64 {
	if p.Grid != nil {
		return p.Grid.Beat(sec)
	}
	return sec * bpm / 60
}

func channel(id, dest string, channels int) xmlChannel {
	return xmlChannel{
		AudioChannels: channels, Destination: dest, Role: "regular", Solo: false, ID: id,
		Mute:   xmlBoolParam{Value: false, ID: id + "-mute", Name: "Mute"},
		Pan:    xmlRealParam{Max: f6(1), Min: f6(0), Unit: "normalized", Value: f6(0.5), ID: id + "-pan", Name: "Pan"},
		Volume: xmlRealParam{Max: f6(2), Min: f6(0), Unit: "linear", Value: f6(1), ID: id + "-volume", Name: "Volume"},
	}
}

// ProjectXML renders project.xml.
func ProjectXML(p Project) ([]byte, error) {
	p.defaults()
	bpm, _ := p.bpm()
	numerator := 4
	if p.Grid != nil {
		numerator = p.Grid.Numerator()
	}
	doc := xmlProject{
		Version:     "1.0",
		Application: xmlApplication{Name: p.Generator, Version: p.GeneratorVersion},
		Transport: xmlTransport{
			Tempo:         xmlRealParam{Max: f6(999), Min: f6(20), Unit: "bpm", Value: f6(bpm), ID: "tempo", Name: "Tempo"},
			TimeSignature: xmlTimeSigParam{Denominator: grid.Denominator, Numerator: numerator, ID: "timesig"},
		},
		Arrangement: xmlArrangement{ID: "arrangement", Lanes: xmlLanes{TimeUnit: "beats", ID: "lanes"}},
	}
	const masterChannel = "master-channel"
	// Every clip starts where second 0 falls on the grid (0 without one).
	start := p.beat(0, bpm)
	for _, st := range p.Stems {
		trackID := "track-" + st.Name
		chID := "channel-" + st.Name
		doc.Structure.Tracks = append(doc.Structure.Tracks, xmlTrack{
			ContentType: "audio", Loaded: true, ID: trackID, Name: TrackName(st.Name), Color: stemColors[st.Name],
			Channel: channel(chID, masterChannel, p.Channels),
		})
		dur := st.Duration
		if dur <= 0 {
			dur = p.DurationSeconds
		}
		beats := p.beat(dur, bpm) - start
		var warps []xmlWarp
		if p.Grid != nil {
			for _, w := range p.Grid.Warps(dur) {
				warps = append(warps, xmlWarp{Time: f6(w.Beat - start), ContentTime: f6(w.Seconds)})
			}
		} else {
			warps = []xmlWarp{{Time: f6(0), ContentTime: f6(0)}, {Time: f6(beats), ContentTime: f6(dur)}}
		}
		doc.Arrangement.Lanes.Lanes = append(doc.Arrangement.Lanes.Lanes, xmlLanes{
			Track: trackID, ID: "lanes-" + st.Name,
			Clips: &xmlClips{ID: "clips-" + st.Name, Clips: []xmlClip{{
				Time: f6(start), Duration: f6(beats), PlayStart: f6(0), Name: TrackName(st.Name),
				Warps: &xmlWarps{
					ContentTimeUnit: "seconds", TimeUnit: "beats",
					Audio: xmlAudio{Algorithm: "stretch", Channels: p.Channels, Duration: f6(dur), SampleRate: p.SampleRate,
						File: xmlFile{Path: AudioPath(st.Name)}},
					Warps: warps,
				},
			}}},
		})
	}
	for _, nt := range p.NoteTracks {
		name := nt.Name
		if name == "" {
			name = TrackName(nt.Stem) + " MIDI"
		}
		trackID := "track-" + nt.Stem + "-midi"
		chID := "channel-" + nt.Stem + "-midi"
		doc.Structure.Tracks = append(doc.Structure.Tracks, xmlTrack{
			ContentType: "notes", Loaded: true, ID: trackID, Name: name, Color: stemColors[nt.Stem],
			Channel: channel(chID, masterChannel, p.Channels),
		})
		end := p.beat(p.DurationSeconds, bpm)
		notes := &xmlNotes{ID: "notes-" + nt.Stem}
		for _, n := range nt.Notes {
			notes.Notes = append(notes.Notes, xmlNote{
				Time: f6(n.Beat - start), Duration: f6(n.Duration), Channel: nt.Channel, Key: n.Key,
				Vel: f6(clamp01(n.Velocity)), Rel: f6(0.5),
			})
			if e := n.Beat + n.Duration; e > end {
				end = e
			}
		}
		doc.Arrangement.Lanes.Lanes = append(doc.Arrangement.Lanes.Lanes, xmlLanes{
			Track: trackID, ID: "lanes-" + nt.Stem + "-midi",
			Clips: &xmlClips{ID: "clips-" + nt.Stem + "-midi", Clips: []xmlClip{{
				Time: f6(start), Duration: f6(end - start), PlayStart: f6(0), Name: name, Notes: notes,
			}}},
		})
	}
	doc.Structure.Tracks = append(doc.Structure.Tracks, xmlTrack{
		ContentType: "audio notes", Loaded: true, ID: "master", Name: "Master",
		Channel: xmlChannel{
			AudioChannels: p.Channels, Role: "master", Solo: false, ID: masterChannel,
			Mute:   xmlBoolParam{Value: false, ID: masterChannel + "-mute", Name: "Mute"},
			Pan:    xmlRealParam{Max: f6(1), Min: f6(0), Unit: "normalized", Value: f6(0.5), ID: masterChannel + "-pan", Name: "Pan"},
			Volume: xmlRealParam{Max: f6(2), Min: f6(0), Unit: "linear", Value: f6(1), ID: masterChannel + "-volume", Name: "Volume"},
		},
	})
	if len(p.Markers) > 0 {
		m := &xmlMarkers{ID: "markers"}
		for _, mk := range p.Markers {
			m.Markers = append(m.Markers, xmlMarker{Time: f6(mk.Beat), Name: mk.Name, Color: mk.Color})
		}
		doc.Arrangement.Markers = m
	}
	if p.Grid != nil {
		if pts := p.Grid.TempoPoints(); len(pts) > 0 {
			ta := &xmlPoints{Unit: "bpm", ID: "tempo-automation", Target: xmlTarget{Parameter: "tempo"}}
			for _, pt := range pts {
				ta.Real = append(ta.Real, xmlRealPoint{Time: f6(pt.Beat), Value: f6(pt.BPM), Interpolation: "linear"})
			}
			doc.Arrangement.TempoAutomation = ta
		}
		if sigs := p.Grid.TimeSignatures(); len(sigs) > 1 {
			ts := &xmlPoints{ID: "timesig-automation", Target: xmlTarget{Parameter: "timesig"}}
			for _, s := range sigs {
				ts.TimeSig = append(ts.TimeSig, xmlTimeSigPoint{Time: f6(s.Beat), Numerator: s.Numerator, Denominator: s.Denominator})
			}
			doc.Arrangement.TimeSignatureAutomation = ts
		}
	}
	return marshal(doc)
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// MetadataXML renders metadata.xml.
func MetadataXML(p Project) ([]byte, error) {
	p.defaults()
	m := xmlMetaData{Title: p.Name, Year: strconv.Itoa(p.Year)}
	switch bpm, ok := p.bpm(); {
	case p.Grid != nil:
		lo, hi := p.Grid.Range()
		if p.Grid.Constant {
			m.Comment = fmt.Sprintf("Tempo map from beat tracking: %s BPM, constant. Generated by %s.", strconv.FormatFloat(bpm, 'f', 2, 64), p.Generator)
		} else {
			m.Comment = fmt.Sprintf("Tempo map from beat tracking: %s BPM (%s–%s), tempo automation per beat. Generated by %s.",
				strconv.FormatFloat(bpm, 'f', 2, 64), strconv.FormatFloat(lo, 'f', 1, 64), strconv.FormatFloat(hi, 'f', 1, 64), p.Generator)
		}
	case !ok:
		m.Comment = fmt.Sprintf("Tempo was not detected with enough confidence; the project tempo is a placeholder of %g BPM. Generated by %s.", bpm, p.Generator)
	default:
		m.Comment = fmt.Sprintf("Detected tempo %s BPM. Generated by %s.", strconv.FormatFloat(bpm, 'f', -1, 64), p.Generator)
	}
	return marshal(m)
}

func marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString(xml.Header)
	enc := xml.NewEncoder(&buf)
	enc.Indent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	buf.WriteByte('\n')
	return buf.Bytes(), nil
}

// Write streams the .dawproject archive to w: project.xml, metadata.xml
// and audio/<name>.wav for each stem (stored, not deflated: WAV barely
// compresses and DAWs read the audio directly).
//
// Entries are written with CreateRaw and sizes/CRC computed up front, so
// no entry needs a data descriptor: Cubase's importer rejects archives
// that use them (bitwig/dawproject#101). The WAVs are read twice.
func Write(w io.Writer, p Project) error {
	p.defaults()
	proj, err := ProjectXML(p)
	if err != nil {
		return err
	}
	meta, err := MetadataXML(p)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(w)
	now := time.Now()
	addDeflated := func(name string, data []byte) error {
		var comp bytes.Buffer
		fw, err := flate.NewWriter(&comp, flate.DefaultCompression)
		if err != nil {
			return err
		}
		if _, err := fw.Write(data); err != nil {
			return err
		}
		if err := fw.Close(); err != nil {
			return err
		}
		f, err := zw.CreateRaw(&zip.FileHeader{
			Name: name, Method: zip.Deflate, Modified: now,
			CRC32: crc32.ChecksumIEEE(data), CompressedSize64: uint64(comp.Len()), UncompressedSize64: uint64(len(data)),
		})
		if err != nil {
			return err
		}
		_, err = f.Write(comp.Bytes())
		return err
	}
	if err := addDeflated("project.xml", proj); err != nil {
		return err
	}
	if err := addDeflated("metadata.xml", meta); err != nil {
		return err
	}
	for _, st := range p.Stems {
		if st.Path == "" {
			continue
		}
		if err := addStored(zw, AudioPath(st.Name), st.Path, now); err != nil {
			return err
		}
	}
	return zw.Close()
}

// addStored copies a file into the archive uncompressed, after a first pass
// for its size and CRC.
func addStored(zw *zip.Writer, name, path string, now time.Time) error {
	src, err := os.Open(path)
	if err != nil {
		return err
	}
	defer src.Close()
	h := crc32.NewIEEE()
	size, err := io.Copy(h, src)
	if err != nil {
		return err
	}
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		return err
	}
	f, err := zw.CreateRaw(&zip.FileHeader{
		Name: name, Method: zip.Store, Modified: now,
		CRC32: h.Sum32(), CompressedSize64: uint64(size), UncompressedSize64: uint64(size),
	})
	if err != nil {
		return err
	}
	n, err := io.Copy(f, src)
	if err != nil {
		return err
	}
	if n != size {
		return fmt.Errorf("%s changed while archiving (%d of %d bytes)", path, n, size)
	}
	return nil
}

// WriteFile writes the archive to path via a temp file and rename.
func WriteFile(path string, p Project) error {
	tmp := path + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if err := Write(f, p); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}
