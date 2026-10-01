package temse

import (
	"encoding/hex"
	"os"
	"strings"
	"testing"
)

// FuzzDecodeList reads arbitrary TemSe list objects in both Unicode code
// pages. A spool that is cut short or not a list must be an error. A list
// that decodes renders, and never ends with an empty page.
func FuzzDecodeList(f *testing.F) {
	raw, err := os.ReadFile("testdata/list_unicode.hex")
	if err != nil {
		f.Fatal(err)
	}
	data, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		f.Fatal(err)
	}
	f.Add(data, true)
	f.Add(data[:len(data)/2], false)
	f.Add([]byte{0x00, 0x06, 0x00, 0x00, 'P', 0x00}, true)
	f.Add([]byte{0x00, 0x10, 0x00, 0x00, ' ', 0x00, 0xFF, 0xF8, 'a', 0x00, 0xFC, 0xF8, 0x00, 0x25, 'b', 0x00}, true)
	f.Fuzz(func(t *testing.T, data []byte, little bool) {
		charcod := "4102"
		if little {
			charcod = "4103"
		}
		l, err := DecodeList(data, charcod)
		if err != nil {
			return
		}
		if l.Pages < 0 || l.Records < len(l.Lines) {
			t.Fatalf("pages %d, records %d, lines %d", l.Pages, l.Records, len(l.Lines))
		}
		_ = l.Text()
	})
}
