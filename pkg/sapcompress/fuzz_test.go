package sapcompress

import (
	"path/filepath"
	"strings"
	"testing"
)

// FuzzDecompress feeds arbitrary bytes to the LZH and LZC decoders. These
// bytes come straight out of a cluster table row, so a truncated or corrupt
// fragment must be an error. A stream that decodes holds exactly the length
// its header promised.
func FuzzDecompress(f *testing.F) {
	names, _ := filepath.Glob("testdata/*.hex")
	for _, name := range names {
		if strings.HasPrefix(filepath.Base(name), "big.") {
			continue // 55 KB; the fuzzer mutates small inputs far faster
		}
		f.Add(load(f, filepath.Base(name)))
	}
	f.Add([]byte{})
	f.Add([]byte{0x05, 0, 0, 0, 0x12, 0x1f, 0x9d, 0x02, 0x00})
	f.Add([]byte{0x05, 0, 0, 0, 0x10, 0x1f, 0x9d, 0x90, 0x41, 0x42})
	f.Fuzz(func(t *testing.T, data []byte) {
		h, herr := ParseHeader(data)
		out, err := Decompress(data)
		if err != nil {
			return
		}
		if herr != nil {
			t.Fatalf("Decompress succeeded on a header ParseHeader refused: %v", herr)
		}
		if len(out) != h.Length {
			t.Fatalf("header promised %d bytes, got %d", h.Length, len(out))
		}
	})
}
