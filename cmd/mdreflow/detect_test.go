package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestRefusalReasonSniffTruncation pins issue #15: the 8KB sniff must not
// refuse a valid file because a multi-byte rune straddles the truncation
// boundary, while genuinely corrupt bytes at the tail must still refuse.
func TestRefusalReasonSniffTruncation(t *testing.T) {
	tail := "\n\nOne sentence. Another sentence.\n"
	cases := []struct {
		name    string
		content []byte
		refused bool
	}{
		{
			// "├" (3 bytes) at 8190-8192: the sniff keeps two of its
			// three bytes. The full file is valid UTF-8.
			name:    "rune straddling boundary accepted",
			content: []byte(strings.Repeat("x", 8190) + "├" + tail),
			refused: false,
		},
		{
			// A 4-byte rune positioned to lose its last byte.
			name:    "four-byte rune straddling boundary accepted",
			content: []byte(strings.Repeat("x", 8189) + "𝄞" + tail),
			refused: false,
		},
		{
			name:    "corrupt byte before the boundary still refused",
			content: append(append([]byte(strings.Repeat("x", 100)), 0xff), []byte(strings.Repeat("x", 9000))...),
			refused: true,
		},
		{
			// Four 0xff bytes ending at the boundary: more than one
			// rune's worth of invalid tail, the case the 3-byte back-off
			// bound exists for.
			name:    "corrupt run at the boundary still refused",
			content: append(append([]byte(strings.Repeat("x", 8188)), bytes.Repeat([]byte{0xff}, 4)...), []byte(tail)...),
			refused: true,
		},
		{
			// No truncation: a partial rune at the real end of a short
			// file is real invalidity.
			name:    "short file with truncated rune still refused",
			content: []byte("abc\xe2\x94"),
			refused: true,
		},
		{
			name:    "short valid file accepted",
			content: []byte("hello. world.\n"),
			refused: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reason := refusalReason("doc.md", tc.content, true)
			if got := reason != ""; got != tc.refused {
				t.Errorf("refusalReason = %q; refused=%v, want %v", reason, got, tc.refused)
			}
		})
	}
}
