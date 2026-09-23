package dawproject

import (
	"archive/zip"
	"bufio"
	"encoding/binary"
	"flag"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BeFeast/RehearseKit/internal/pipeline/grid"
)

var synthetic = flag.String("synthetic", "", "write the synthetic transcribe .dawproject (click stems, tempo/time-signature changes, marker, notes) to this path")

// transcribeBeats is 8 bars of 4/4 at 120, 4 bars of 3/4 at 120, then
// 8 bars of 4/4 at 140, starting 0.25 s in.
func transcribeBeats() (beats, downs []float64, end float64) {
	t := 0.25
	bar := func(n int, bpm float64) {
		downs = append(downs, t)
		for i := 0; i < n; i++ {
			beats = append(beats, t)
			t += 60 / bpm
		}
	}
	for i := 0; i < 8; i++ {
		bar(4, 120)
	}
	for i := 0; i < 4; i++ {
		bar(3, 120)
	}
	for i := 0; i < 8; i++ {
		bar(4, 140)
	}
	beats = append(beats, t)
	downs = append(downs, t)
	return beats, downs, t + 1
}

func transcribeFixture(t *testing.T) Project {
	t.Helper()
	beats, downs, end := transcribeBeats()
	m, err := grid.Build(beats, downs, end)
	if err != nil {
		t.Fatal(err)
	}
	p := Project{
		Name: "Transcribe Fixture", DurationSeconds: end, Year: 2026, GeneratorVersion: "test", Grid: m,
		Stems: []Stem{{Name: "drums"}, {Name: "bass"}},
		Markers: []Marker{
			{Beat: m.Beat(downs[0]), Name: "Intro"},
			{Beat: m.Beat(downs[8]), Name: "3/4 part", Color: "#FF0000"},
			{Beat: m.Beat(downs[12]), Name: "140 BPM"},
		},
	}
	// Quarter notes on every beat for the bass; kick on downbeats + snare
	// on beat 3 for the drums.
	var bass, drums []Note
	for k, sec := range beats[:len(beats)-1] {
		b := m.Beat(sec)
		bass = append(bass, Note{Beat: b, Duration: 0.9, Key: 40 + k%12, Velocity: 0.8})
	}
	for i := 0; i+1 < len(downs); i++ {
		b := m.Beat(downs[i])
		drums = append(drums, Note{Beat: b, Duration: 0.25, Key: 36, Velocity: 1})
		drums = append(drums, Note{Beat: b + 2, Duration: 0.25, Key: 38, Velocity: 0.9})
	}
	p.NoteTracks = []NoteTrack{{Stem: "bass", Channel: 0, Notes: bass}, {Stem: "drums", Channel: 9, Notes: drums}}
	return p
}

func TestTranscribeGolden(t *testing.T) {
	p := transcribeFixture(t)
	got, err := ProjectXML(p)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "transcribe.xml", got)
	s := string(got)
	// Arrangement order: Lanes, Markers, TempoAutomation, TimeSignatureAutomation.
	iL, iM, iT, iS := strings.Index(s, "<Lanes "), strings.Index(s, "<Markers "), strings.Index(s, "<TempoAutomation "), strings.Index(s, "<TimeSignatureAutomation ")
	if !(iL < iM && iM < iT && iT < iS) || iL < 0 {
		t.Fatalf("arrangement order %d %d %d %d", iL, iM, iT, iS)
	}
	for _, want := range []string{
		`<TempoAutomation unit="bpm" id="tempo-automation">`,
		`<Target parameter="tempo">`,
		`<Target parameter="timesig">`,
		`<TimeSignaturePoint time="36.000000" numerator="3" denominator="4">`,
		`<TimeSignaturePoint time="48.000000" numerator="4" denominator="4">`,
		`<Track contentType="notes" loaded="true" id="track-bass-midi" name="Bass MIDI" color="#4169E1">`,
		`<Marker time="36.000000" name="3/4 part" color="#FF0000">`,
		`<Notes id="notes-drums">`,
		`channel="9" key="36" vel="1.000000" rel="0.500000">`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("project.xml lacks %s", want)
		}
	}
	// Master stays last.
	if li := strings.LastIndex(s, "<Track "); !strings.HasPrefix(s[li:], `<Track contentType="audio notes" loaded="true" id="master"`) {
		t.Error("master track is not last")
	}
	// Tempo steps at beat 48 (start of the 140 part): the pair 120 → 140.
	if !strings.Contains(s, `<RealPoint time="48.000000" value="120.000000" interpolation="linear"></RealPoint>`+"\n      "+
		`<RealPoint time="48.000000" value="140.000000" interpolation="linear"></RealPoint>`) {
		t.Error("no 120→140 step at beat 48")
	}
	meta, _ := MetadataXML(p)
	if !strings.Contains(string(meta), "Tempo map from beat tracking") {
		t.Errorf("metadata: %s", meta)
	}
	xsdCheck(t, got)
}

// xsdCheck validates project.xml against the DAWproject schema when
// xmllint is installed (libxml2-utils).
func xsdCheck(t *testing.T, projectXML []byte) {
	t.Helper()
	xmllint, err := exec.LookPath("xmllint")
	if err != nil {
		t.Skip("xmllint not installed")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "project.xml")
	if err := os.WriteFile(path, projectXML, 0o644); err != nil {
		t.Fatal(err)
	}
	xsd, _ := filepath.Abs(filepath.Join("testdata", "Project.xsd"))
	out, err := exec.Command(xmllint, "--noout", "--schema", xsd, path).CombinedOutput()
	if err != nil {
		t.Fatalf("xmllint: %v\n%s", err, out)
	}
}

func TestFlagOffGoldenValidatesAgainstXSD(t *testing.T) {
	got, err := ProjectXML(fixture())
	if err != nil {
		t.Fatal(err)
	}
	xsdCheck(t, got)
}

func TestArchiveHasNoDataDescriptors(t *testing.T) {
	dir := t.TempDir()
	p := fixture()
	p.Stems = nil
	path := filepath.Join(dir, "x.wav")
	if err := writeClick(path, []float64{0.1, 0.6}, nil, 1); err != nil {
		t.Fatal(err)
	}
	p.Stems = []Stem{{Name: "drums", Path: path}}
	out := filepath.Join(dir, "p.dawproject")
	if err := WriteFile(out, p); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.OpenReader(out)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	for _, f := range zr.File {
		if f.Flags&0x8 != 0 {
			t.Errorf("%s uses a data descriptor", f.Name)
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("%s: %v", f.Name, err)
		}
		buf := make([]byte, f.UncompressedSize64+1)
		n, _ := rc.Read(buf)
		rc.Close()
		if uint64(n) != f.UncompressedSize64 && f.UncompressedSize64 > 0 && n == 0 {
			t.Errorf("%s: read %d of %d", f.Name, n, f.UncompressedSize64)
		}
	}
}

// TestSynthetic writes the manual Bitwig check file when -synthetic is set.
func TestSynthetic(t *testing.T) {
	if *synthetic == "" {
		t.Skip("set -synthetic <path>")
	}
	p := transcribeFixture(t)
	beats, downs, end := transcribeBeats()
	dir := t.TempDir()
	for i := range p.Stems {
		path := filepath.Join(dir, p.Stems[i].Name+".wav")
		if err := writeClick(path, beats, downs, end); err != nil {
			t.Fatal(err)
		}
		p.Stems[i].Path = path
		p.Stems[i].Duration = end
	}
	p.Name = "RehearseKit S1 synthetic"
	p.GeneratorVersion = "rk/s1-synthetic"
	if err := WriteFile(*synthetic, p); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %s", *synthetic)
}

// writeClick writes a 24-bit/48 kHz stereo WAV of duration seconds with a
// 10 ms click at each beat (louder and higher on downbeats).
func writeClick(path string, beats, downbeats []float64, duration float64) error {
	const rate, channels = 48000, 2
	frames := int64(duration * rate)
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	dataLen := frames * channels * 3
	hdr := make([]byte, 44)
	copy(hdr[0:], "RIFF")
	binary.LittleEndian.PutUint32(hdr[4:], uint32(36+dataLen))
	copy(hdr[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(hdr[16:], 16)
	binary.LittleEndian.PutUint16(hdr[20:], 1)
	binary.LittleEndian.PutUint16(hdr[22:], channels)
	binary.LittleEndian.PutUint32(hdr[24:], rate)
	binary.LittleEndian.PutUint32(hdr[28:], rate*channels*3)
	binary.LittleEndian.PutUint16(hdr[32:], channels*3)
	binary.LittleEndian.PutUint16(hdr[34:], 24)
	copy(hdr[36:], "data")
	binary.LittleEndian.PutUint32(hdr[40:], uint32(dataLen))
	w.Write(hdr)
	down := map[int64]bool{}
	for _, d := range downbeats {
		down[int64(d*rate)] = true
	}
	type click struct {
		start int64
		freq  float64
		amp   float64
	}
	var clicks []click
	for _, b := range beats {
		s := int64(b * rate)
		c := click{s, 1000, 0.4}
		if down[s] {
			c.freq, c.amp = 1500, 0.8
		}
		clicks = append(clicks, c)
	}
	const clickLen = 480 // 10 ms
	ci := 0
	buf := make([]byte, 6)
	for i := int64(0); i < frames; i++ {
		v := 0.0
		for ci < len(clicks) && clicks[ci].start+clickLen <= i {
			ci++
		}
		if ci < len(clicks) && i >= clicks[ci].start {
			k := float64(i - clicks[ci].start)
			env := 1 - k/clickLen
			v = clicks[ci].amp * env * math.Sin(2*math.Pi*clicks[ci].freq*k/rate)
		}
		s := int32(v * 8388607)
		for c := 0; c < channels; c++ {
			buf[c*3] = byte(s)
			buf[c*3+1] = byte(s >> 8)
			buf[c*3+2] = byte(s >> 16)
		}
		w.Write(buf)
	}
	return w.Flush()
}
