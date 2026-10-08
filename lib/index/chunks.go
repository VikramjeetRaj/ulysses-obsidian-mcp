package index

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// DefaultChunkSize is a reasonable maximum chunk size in bytes.
const DefaultChunkSize = 4096

// ChunkAndHash reads r once and returns the SHA-256 hex digest of its content (the note's
// revision, in the same form the vault uses) and its chunks of at most maxSize bytes.
// Call it before PutNote so reading happens outside the database transaction.
func ChunkAndHash(r io.Reader, maxSize int) (revision string, chunks []string, err error) {
	h := sha256.New()
	err = Chunk(io.TeeReader(r, h), maxSize, func(chunk string) error {
		chunks = append(chunks, chunk)
		return nil
	})
	if err != nil {
		return "", nil, err
	}
	// Chunk stops reading at EOF, so everything has passed through the hash.
	return hex.EncodeToString(h.Sum(nil)), chunks, nil
}

// Chunk reads r and passes it to fn as chunks of at most maxSize bytes, in order, as they are
// produced, so a large note is never held in memory. Chunks end at a paragraph break (a blank
// line) when one falls in the second half of the chunk, otherwise at a line break. A line longer
// than maxSize is split. Chunks always break on rune boundaries, and concatenating them gives
// back the input apart from chunks that are only whitespace, which are dropped.
// Chunk stops and returns the error if fn fails.
func Chunk(r io.Reader, maxSize int, fn func(chunk string) error) error {
	if maxSize < utf8.UTFMax {
		return fmt.Errorf("chunk size must be at least %d bytes", utf8.UTFMax)
	}
	c := chunker{maxSize: maxSize, fn: fn}
	br := bufio.NewReaderSize(r, 4096)
	var carry []byte // incomplete rune left over from a line longer than the read buffer
	for {
		line, err := br.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			keep := validPrefix(line)
			if err := c.add(string(carry) + string(line[:keep])); err != nil {
				return err
			}
			carry = append(carry[:0], line[keep:]...)
			continue
		}
		if len(carry) > 0 || len(line) > 0 {
			s := string(carry) + string(line)
			carry = carry[:0]
			if err := c.add(s); err != nil {
				return err
			}
			if strings.TrimSpace(s) == "" && strings.HasSuffix(s, "\n") {
				c.lastBreak = c.cur.Len()
			}
		}
		if err == io.EOF {
			return c.flush()
		}
		if err != nil {
			return fmt.Errorf("read note: %w", err)
		}
	}
}

type chunker struct {
	maxSize   int
	fn        func(string) error
	cur       strings.Builder
	lastBreak int // length of cur up to and including its last blank line, or 0
}

// add appends piece to the current chunk, emitting chunks as they fill.
func (c *chunker) add(piece string) error {
	if c.cur.Len() > 0 && c.cur.Len()+len(piece) > c.maxSize {
		if err := c.split(); err != nil {
			return err
		}
		if c.cur.Len() > 0 && c.cur.Len()+len(piece) > c.maxSize {
			if err := c.flush(); err != nil {
				return err
			}
		}
	}
	for len(piece) > c.maxSize {
		end := c.maxSize
		for !utf8.RuneStart(piece[end]) {
			end--
		}
		c.cur.WriteString(piece[:end])
		if err := c.flush(); err != nil {
			return err
		}
		piece = piece[end:]
	}
	c.cur.WriteString(piece)
	return nil
}

// split emits the current chunk up to its last paragraph break if that is past the halfway
// point, keeping the rest as the start of the next chunk; otherwise it emits everything.
func (c *chunker) split() error {
	if c.lastBreak < c.maxSize/2 {
		return c.flush()
	}
	s := c.cur.String()
	head, tail := s[:c.lastBreak], s[c.lastBreak:]
	c.cur.Reset()
	c.cur.WriteString(tail)
	c.lastBreak = 0
	return c.emit(head)
}

func (c *chunker) flush() error {
	s := c.cur.String()
	c.cur.Reset()
	c.lastBreak = 0
	return c.emit(s)
}

func (c *chunker) emit(s string) error {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return c.fn(s)
}

// validPrefix returns the length of b without a trailing incomplete UTF-8 sequence.
func validPrefix(b []byte) int {
	for k := 1; k <= utf8.UTFMax && k <= len(b); k++ {
		if i := len(b) - k; utf8.RuneStart(b[i]) {
			if utf8.FullRune(b[i:]) {
				return len(b)
			}
			return i
		}
	}
	return len(b)
}
