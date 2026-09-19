package demucs

import (
	"bufio"
	"strings"
	"testing"
)

func TestTrackerSingleBar(t *testing.T) {
	tr := NewTracker(1)
	for _, l := range []string{
		"Important: the default model was recently changed to `htdemucs`",
		"Selected model is a bag of 1 models. You will see that many progress bars per track.",
		"Separated tracks will be stored in /tmp/work/htdemucs",
		"Separating track source.wav",
		"  0%|          | 0.0/180.0 [00:00<?, ?seconds/s]",
		" 25%|██▌       | 45.0/180.0 [00:03<00:10, 13.2seconds/s]",
		" 50%|█████     | 90.0/180.0 [00:07<00:07, 12.9seconds/s]",
		"100%|██████████| 180.0/180.0 [00:14<00:00, 12.8seconds/s]",
	} {
		tr.Feed(l)
	}
	if p := tr.Progress(); p != 1 {
		t.Fatalf("progress %v", p)
	}
}

func TestTrackerBagOfModels(t *testing.T) {
	tr := NewTracker(Bars["htdemucs_ft"])
	feed := func(pcts ...int) {
		for _, p := range pcts {
			tr.Feed(strings.Repeat(" ", 3) + itoa(p) + "%|xx| 1/2")
		}
	}
	feed(0, 50, 100)
	if p := tr.Progress(); p != 0.25 {
		t.Fatalf("after bar 1: %v", p)
	}
	feed(3, 50)
	if p := tr.Progress(); p != 0.375 {
		t.Fatalf("mid bar 2: %v", p)
	}
	feed(100, 0, 100, 0, 100)
	if p := tr.Progress(); p != 1 {
		t.Fatalf("end: %v", p)
	}
	// Extra bars beyond the expected count never exceed 1.
	feed(0, 50)
	if p := tr.Progress(); p != 1 {
		t.Fatalf("overflow: %v", p)
	}
}

func TestTrackerLearnsBagSize(t *testing.T) {
	tr := NewTracker(1)
	tr.Feed("Selected model is a bag of 4 models. You will see that many progress bars per track.")
	tr.Feed("100%|x| 1/1")
	if p := tr.Progress(); p != 0.25 {
		t.Fatalf("progress %v", p)
	}
}

func TestScanCRLF(t *testing.T) {
	sc := bufio.NewScanner(strings.NewReader("a\r 10%|\r 20%|\nb\n"))
	sc.Split(scanCRLF)
	var got []string
	for sc.Scan() {
		got = append(got, sc.Text())
	}
	want := []string{"a", " 10%|", " 20%|", "b"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("%q", got)
	}
}

func itoa(n int) string {
	return strings.TrimSpace(strings.Repeat(" ", 0) + string(rune('0'+n/100%10)) + string(rune('0'+n/10%10)) + string(rune('0'+n%10)))
}
