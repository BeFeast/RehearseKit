package youtube

import "testing"

func TestParseVideoID(t *testing.T) {
	const id = "dQw4w9WgXcQ"
	tests := []struct {
		name string
		in   string
		want string // empty means ErrInvalidURL
	}{
		{"watch", "https://www.youtube.com/watch?v=" + id, id},
		{"watch bare host", "https://youtube.com/watch?v=" + id, id},
		{"watch mobile", "http://m.youtube.com/watch?v=" + id + "&t=42s", id},
		{"watch with playlist", "https://www.youtube.com/watch?v=" + id + "&list=PLx&index=3", id},
		{"watch v not first", "https://www.youtube.com/watch?feature=share&v=" + id, id},
		{"music", "https://music.youtube.com/watch?v=" + id + "&si=abc", id},
		{"shorts", "https://www.youtube.com/shorts/" + id, id},
		{"shorts trailing slash + query", "https://youtube.com/shorts/" + id + "/?feature=share", id},
		{"embed", "https://www.youtube.com/embed/" + id, id},
		{"live", "https://www.youtube.com/live/" + id + "?si=x", id},
		{"legacy v", "https://www.youtube.com/v/" + id, id},
		{"youtu.be", "https://youtu.be/" + id, id},
		{"youtu.be with query", "https://youtu.be/" + id + "?t=10", id},
		{"no scheme", "youtu.be/" + id, id},
		{"no scheme www", "www.youtube.com/watch?v=" + id, id},
		{"uppercase host", "HTTPS://WWW.YOUTUBE.COM/watch?v=" + id, id},
		{"whitespace", "  https://youtu.be/" + id + "\n", id},
		{"host with port", "https://www.youtube.com:443/watch?v=" + id, id},

		{"empty", "", ""},
		{"not youtube", "https://vimeo.com/123456", ""},
		{"lookalike host", "https://youtube.com.evil.example/watch?v=" + id, ""},
		{"suffix lookalike", "https://notyoutube.com/watch?v=" + id, ""},
		{"ftp scheme", "ftp://youtube.com/watch?v=" + id, ""},
		{"javascript scheme", "javascript:alert(1)", ""},
		{"watch without v", "https://www.youtube.com/watch", ""},
		{"watch short id", "https://www.youtube.com/watch?v=abc", ""},
		{"watch long id", "https://www.youtube.com/watch?v=" + id + "x", ""},
		{"id with bad chars", "https://youtu.be/dQw4w9WgX$Q", ""},
		{"channel page", "https://www.youtube.com/@somechannel", ""},
		{"playlist only", "https://www.youtube.com/playlist?list=PLx", ""},
		{"youtu.be root", "https://youtu.be/", ""},
		{"unknown path", "https://www.youtube.com/feed/" + id, ""},
		{"userinfo trick", "https://youtube.com@evil.example/watch?v=" + id, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseVideoID(tc.in)
			if tc.want == "" {
				if err == nil {
					t.Fatalf("ParseVideoID(%q) = %q, want error", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseVideoID(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("ParseVideoID(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestCanonicalURL(t *testing.T) {
	if got := CanonicalURL("dQw4w9WgXcQ"); got != "https://www.youtube.com/watch?v=dQw4w9WgXcQ" {
		t.Fatal(got)
	}
}
