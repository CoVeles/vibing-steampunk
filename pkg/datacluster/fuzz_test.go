package datacluster

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// FuzzParse decodes arbitrary cluster blobs. A cluster is read out of a table
// row (INDX, BALDAT, STXL ...) and may be one fragment short, from a codepage
// this code does not know, or simply not a cluster: that is an error, never a
// panic. The parsed cluster is walked the way the CLI renders it.
func FuzzParse(f *testing.F) {
	names, _ := filepath.Glob("testdata/*.hex")
	for _, name := range names {
		f.Add(loadHex(f, filepath.Base(name)))
	}
	f.Add([]byte{0xFF, 0x06, 0x02, 0x01, 0x01, 0x02, 0x80, 0x00, '4', '1', '0', '3', 0, 0, 0, 0})
	f.Fuzz(func(t *testing.T, blob []byte) {
		c, err := Parse(blob)
		if err != nil {
			return
		}
		if c == nil {
			t.Fatal("Parse returned neither a cluster nor an error")
		}
		_ = fmt.Sprintf("%+v", c)
	})
}

// FuzzReadExport reads SE16-style exports. The seed is the fixture laid out
// the way SE16 and SE16N write it.
func FuzzReadExport(f *testing.F) {
	whole := loadHex(f, "indx_plain.hex")
	h := strings.ToUpper(hex.EncodeToString(whole))
	f.Add([]byte("MANDT,RELID,SRTFD,SRTF2,LOEKZ,AEDAT,USERA,CLUSTR,CLUSTD\r\n" +
		"001,ZV,OTHER,0,,20260904,TESTUSER," + fmt.Sprint(len(whole)) + "," + h + "\r\n"))
	f.Add([]byte("RELID;SRTFD;SRTF2;CLUSTR;CLUSTD\nZV;VSPFIX;0;" + fmt.Sprint(len(whole)) + ";" + strings.ToLower(h) + "\n"))
	f.Add([]byte("RELID|SRTF2|CLUSTR|CLUSTD\nZV|1|2|FF00\nZV|0|2|FF06\n"))
	f.Add([]byte("\ufeff\n\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		recs, err := ReadExport(bytes.NewReader(data), "LOEKZ", "AEDAT", "USERA")
		if err != nil {
			return
		}
		for _, r := range recs {
			_, _ = Parse(r.Blob)
		}
	})
}
