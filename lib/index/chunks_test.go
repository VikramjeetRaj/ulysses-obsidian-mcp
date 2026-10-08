package index

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"testing/iotest"
	"unicode/utf8"
)

func collect(t *testing.T, input string, maxSize int) []string {
	t.Helper()
	got := []string{}
	err := Chunk(strings.NewReader(input), maxSize, func(c string) error {
		got = append(got, c)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestChunk(t *testing.T) {
	for _, tc := range []struct {
		name    string
		input   string
		maxSize int
		want    []string // nil means only the generic checks apply
	}{
		{"empty", "", 100, []string{}},
		{"whitespace only", " \n\n \n", 100, []string{}},
		{"fits in one chunk", "# Title\n\nbody\n", 100, []string{"# Title\n\nbody\n"}},
		{"breaks on lines", "aaaa\nbbbb\ncccc\n", 10, []string{"aaaa\nbbbb\n", "cccc\n"}},
		{"no trailing newline", "aaaa\nbbbb", 6, []string{"aaaa\n", "bbbb"}},
		{"crlf", "aa\r\nbb\r\n", 5, []string{"aa\r\n", "bb\r\n"}},
		{"prefers paragraph break", "aaaa\nbbbb\n\ncccc\ndddd\n", 18, []string{"aaaa\nbbbb\n\n", "cccc\ndddd\n"}},
		{"ignores early paragraph break", "a\n\nbbbbbbbb\ncccccc\n", 16, []string{"a\n\nbbbbbbbb\n", "cccccc\n"}},
		{"long line is split", strings.Repeat("x", 25), 10, nil},
		{"multibyte split", strings.Repeat("é", 50), 7, nil},
		{"line longer than read buffer", strings.Repeat("日本語", 3000) + "\nend\n", 100, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := collect(t, tc.input, tc.maxSize)
			if tc.want != nil {
				if len(got) != len(tc.want) {
					t.Fatalf("chunks = %q, want %q", got, tc.want)
				}
				for i := range got {
					if got[i] != tc.want[i] {
						t.Fatalf("chunks = %q, want %q", got, tc.want)
					}
				}
			}
			for _, c := range got {
				if len(c) > tc.maxSize {
					t.Errorf("chunk of %d bytes exceeds %d", len(c), tc.maxSize)
				}
				if !utf8.ValidString(c) {
					t.Errorf("chunk %q is not valid UTF-8", c)
				}
			}
			if strings.TrimSpace(tc.input) != "" && strings.Join(got, "") != tc.input {
				t.Error("chunks do not rejoin to the input")
			}
		})
	}
}

func TestChunkStopsOnCallbackError(t *testing.T) {
	boom := errors.New("boom")
	calls := 0
	err := Chunk(strings.NewReader("aaaa\nbbbb\ncccc\n"), 5, func(string) error {
		calls++
		return boom
	})
	if !errors.Is(err, boom) || calls != 1 {
		t.Fatalf("err = %v, calls = %d; want boom after 1 call", err, calls)
	}
}

func TestChunkRejectsTinySize(t *testing.T) {
	err := Chunk(strings.NewReader("x"), 3, func(string) error { return nil })
	if err == nil {
		t.Fatal("expected error for size below 4 bytes")
	}
}

func TestChunkAndHash(t *testing.T) {
	input := "# Title\n\n" + strings.Repeat("word ", 500) + "\n\nend\n"
	rev, chunks, err := ChunkAndHash(strings.NewReader(input), 100)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(input))
	if rev != hex.EncodeToString(sum[:]) {
		t.Errorf("revision = %s, want sha256 of the input", rev)
	}
	if len(chunks) < 2 || strings.Join(chunks, "") != input {
		t.Errorf("chunks = %d, rejoined equal = %v", len(chunks), strings.Join(chunks, "") == input)
	}
}

func TestChunkAndHashReadError(t *testing.T) {
	if _, _, err := ChunkAndHash(iotest.ErrReader(errors.New("boom")), 100); err == nil {
		t.Fatal("expected read error")
	}
}
