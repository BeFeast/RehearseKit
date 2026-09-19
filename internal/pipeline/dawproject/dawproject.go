// Package dawproject writes a DAWproject 1.0 archive
// (https://github.com/bitwig/dawproject): a zip holding project.xml,
// metadata.xml and the referenced audio files. One audio track per stem,
// each with a single clip spanning the song, warped linearly from seconds
// onto the beat grid; a master track; the detected tempo in Transport.
//
// When the tempo is unknown the project is written at 120 BPM (the format
// requires a value) and metadata.xml carries a Comment saying so; the
// warps still map the whole file onto the corresponding number of beats,
// so audio plays at the original speed regardless.
package dawproject

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
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
// strict readers (Cubase) are happy.

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
	ID    string   `xml:"id,attr"`
	Lanes xmlLanes `xml:"Lanes"`
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
	Time      string   `xml:"time,attr"`
	Duration  string   `xml:"duration,attr"`
	PlayStart string   `xml:"playStart,attr"`
	Name      string   `xml:"name,attr"`
	Warps     xmlWarps `xml:"Warps"`
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

// ProjectXML renders project.xml.
func ProjectXML(p Project) ([]byte, error) {
	p.defaults()
	bpm, _ := p.bpm()
	doc := xmlProject{
		Version:     "1.0",
		Application: xmlApplication{Name: p.Generator, Version: p.GeneratorVersion},
		Transport: xmlTransport{
			Tempo:         xmlRealParam{Max: f6(999), Min: f6(20), Unit: "bpm", Value: f6(bpm), ID: "tempo", Name: "Tempo"},
			TimeSignature: xmlTimeSigParam{Denominator: 4, Numerator: 4, ID: "timesig"},
		},
		Arrangement: xmlArrangement{ID: "arrangement", Lanes: xmlLanes{TimeUnit: "beats", ID: "lanes"}},
	}
	const masterChannel = "master-channel"
	for _, st := range p.Stems {
		trackID := "track-" + st.Name
		chID := "channel-" + st.Name
		doc.Structure.Tracks = append(doc.Structure.Tracks, xmlTrack{
			ContentType: "audio", Loaded: true, ID: trackID, Name: TrackName(st.Name), Color: stemColors[st.Name],
			Channel: xmlChannel{
				AudioChannels: p.Channels, Destination: masterChannel, Role: "regular", Solo: false, ID: chID,
				Mute:   xmlBoolParam{Value: false, ID: chID + "-mute", Name: "Mute"},
				Pan:    xmlRealParam{Max: f6(1), Min: f6(0), Unit: "normalized", Value: f6(0.5), ID: chID + "-pan", Name: "Pan"},
				Volume: xmlRealParam{Max: f6(2), Min: f6(0), Unit: "linear", Value: f6(1), ID: chID + "-volume", Name: "Volume"},
			},
		})
		dur := st.Duration
		if dur <= 0 {
			dur = p.DurationSeconds
		}
		beats := dur * bpm / 60
		doc.Arrangement.Lanes.Lanes = append(doc.Arrangement.Lanes.Lanes, xmlLanes{
			Track: trackID, ID: "lanes-" + st.Name,
			Clips: &xmlClips{ID: "clips-" + st.Name, Clips: []xmlClip{{
				Time: f6(0), Duration: f6(beats), PlayStart: f6(0), Name: TrackName(st.Name),
				Warps: xmlWarps{
					ContentTimeUnit: "seconds", TimeUnit: "beats",
					Audio: xmlAudio{Algorithm: "stretch", Channels: p.Channels, Duration: f6(dur), SampleRate: p.SampleRate,
						File: xmlFile{Path: AudioPath(st.Name)}},
					Warps: []xmlWarp{{Time: f6(0), ContentTime: f6(0)}, {Time: f6(beats), ContentTime: f6(dur)}},
				},
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
	return marshal(doc)
}

// MetadataXML renders metadata.xml.
func MetadataXML(p Project) ([]byte, error) {
	p.defaults()
	m := xmlMetaData{Title: p.Name, Year: strconv.Itoa(p.Year)}
	if bpm, ok := p.bpm(); !ok {
		m.Comment = fmt.Sprintf("Tempo was not detected with enough confidence; the project tempo is a placeholder of %g BPM. Generated by %s.", bpm, p.Generator)
	} else {
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
	add := func(name string, data []byte) error {
		f, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: time.Now()})
		if err != nil {
			return err
		}
		_, err = f.Write(data)
		return err
	}
	if err := add("project.xml", proj); err != nil {
		return err
	}
	if err := add("metadata.xml", meta); err != nil {
		return err
	}
	for _, st := range p.Stems {
		if st.Path == "" {
			continue
		}
		src, err := os.Open(st.Path)
		if err != nil {
			return err
		}
		f, err := zw.CreateHeader(&zip.FileHeader{Name: AudioPath(st.Name), Method: zip.Store, Modified: time.Now()})
		if err != nil {
			src.Close()
			return err
		}
		if _, err := io.Copy(f, src); err != nil {
			src.Close()
			return err
		}
		src.Close()
	}
	return zw.Close()
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
