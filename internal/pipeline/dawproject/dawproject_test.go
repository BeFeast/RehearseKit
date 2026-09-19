package dawproject

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BeFeast/RehearseKit/internal/pipeline/wavtest"
)

var update = flag.Bool("update", false, "rewrite golden files")

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v (run with -update)", path, err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s differs from golden\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

func fixture() Project {
	bpm := 136.0
	return Project{
		Name: "Fixture Song", BPM: &bpm, DurationSeconds: 180, Year: 2026, GeneratorVersion: "test",
		Stems: []Stem{{Name: "vocals"}, {Name: "drums"}, {Name: "bass"}, {Name: "other"}},
	}
}

func TestProjectXMLGolden(t *testing.T) {
	got, err := ProjectXML(fixture())
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "project.xml", got)
	// Well-formed and structurally what Bitwig/Cubase expect.
	var doc struct {
		XMLName   xml.Name `xml:"Project"`
		Version   string   `xml:"version,attr"`
		Transport struct {
			Tempo struct {
				Value string `xml:"value,attr"`
			} `xml:"Tempo"`
		} `xml:"Transport"`
		Structure struct {
			Tracks []struct {
				ID      string `xml:"id,attr"`
				Channel struct {
					Role        string `xml:"role,attr"`
					Destination string `xml:"destination,attr"`
					ID          string `xml:"id,attr"`
				} `xml:"Channel"`
			} `xml:"Track"`
		} `xml:"Structure"`
		Arrangement struct {
			Lanes struct {
				TimeUnit string `xml:"timeUnit,attr"`
				Lanes    []struct {
					Track string `xml:"track,attr"`
					Clips struct {
						Clip []struct {
							Duration string `xml:"duration,attr"`
							Warps    struct {
								ContentTimeUnit string `xml:"contentTimeUnit,attr"`
								Audio           struct {
									File struct {
										Path string `xml:"path,attr"`
									} `xml:"File"`
								} `xml:"Audio"`
								Warp []struct {
									Time        string `xml:"time,attr"`
									ContentTime string `xml:"contentTime,attr"`
								} `xml:"Warp"`
							} `xml:"Warps"`
						} `xml:"Clip"`
					} `xml:"Clips"`
				} `xml:"Lanes"`
			} `xml:"Lanes"`
		} `xml:"Arrangement"`
	}
	if err := xml.Unmarshal(got, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Version != "1.0" || doc.Transport.Tempo.Value != "136.000000" {
		t.Fatalf("header %+v", doc)
	}
	if len(doc.Structure.Tracks) != 5 || doc.Structure.Tracks[4].Channel.Role != "master" {
		t.Fatalf("tracks %+v", doc.Structure.Tracks)
	}
	master := doc.Structure.Tracks[4].Channel.ID
	for _, tr := range doc.Structure.Tracks[:4] {
		if tr.Channel.Destination != master {
			t.Errorf("track %s routes to %q, want master %q", tr.ID, tr.Channel.Destination, master)
		}
	}
	if doc.Arrangement.Lanes.TimeUnit != "beats" || len(doc.Arrangement.Lanes.Lanes) != 4 {
		t.Fatalf("arrangement %+v", doc.Arrangement)
	}
	l := doc.Arrangement.Lanes.Lanes[0]
	clip := l.Clips.Clip[0]
	if l.Track != "track-vocals" || clip.Duration != "408.000000" || clip.Warps.ContentTimeUnit != "seconds" ||
		clip.Warps.Audio.File.Path != "audio/vocals.wav" || len(clip.Warps.Warp) != 2 ||
		clip.Warps.Warp[1].Time != "408.000000" || clip.Warps.Warp[1].ContentTime != "180.000000" {
		t.Fatalf("clip %+v", clip)
	}
}

func TestMetadataGoldenAndNullTempo(t *testing.T) {
	got, err := MetadataXML(fixture())
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "metadata.xml", got)

	p := fixture()
	p.BPM = nil
	proj, _ := ProjectXML(p)
	if !strings.Contains(string(proj), `value="120.000000"`) {
		t.Fatalf("null tempo should fall back to 120:\n%s", proj)
	}
	// 180 s at 120 BPM = 360 beats: the warps keep real time.
	if !strings.Contains(string(proj), `<Warp time="360.000000" contentTime="180.000000">`) {
		t.Fatalf("warp for null tempo:\n%s", proj)
	}
	meta, _ := MetadataXML(p)
	if !strings.Contains(string(meta), "not detected") {
		t.Fatalf("metadata should flag the placeholder tempo:\n%s", meta)
	}
}

func TestWriteArchive(t *testing.T) {
	dir := t.TempDir()
	p := fixture()
	p.Stems = nil
	for _, n := range []string{"vocals", "drums"} {
		path := filepath.Join(dir, n+".wav")
		if _, err := wavtest.Write(path, wavtest.Options{Seconds: 0.05}); err != nil {
			t.Fatal(err)
		}
		p.Stems = append(p.Stems, Stem{Name: n, Path: path})
	}
	out := filepath.Join(dir, "project.dawproject")
	if err := WriteFile(out, p); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.OpenReader(out)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	names := map[string]*zip.File{}
	for _, f := range zr.File {
		names[f.Name] = f
	}
	for _, want := range []string{"project.xml", "metadata.xml", "audio/vocals.wav", "audio/drums.wav"} {
		if names[want] == nil {
			t.Fatalf("archive lacks %s (has %v)", want, keys(names))
		}
	}
	if names["audio/vocals.wav"].Method != zip.Store {
		t.Error("audio should be stored, not deflated")
	}
	rc, _ := names["project.xml"].Open()
	b, _ := io.ReadAll(rc)
	rc.Close()
	if !strings.Contains(string(b), `<File path="audio/vocals.wav">`) {
		t.Fatalf("project.xml in archive:\n%s", b)
	}
	if _, err := os.Stat(out + ".part"); err == nil {
		t.Fatal("temp file left behind")
	}
}

func keys(m map[string]*zip.File) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
