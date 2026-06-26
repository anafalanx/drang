package main

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestStandalonePayloadRoundTrip(t *testing.T) {
	base := []byte("PRETEND-THIS-IS-THE-DRANG-BINARY-IMAGE")
	src := []byte("say(\"hi\")\n$x := 42\nsay($x * 2)\n")

	// A built standalone = base image + packed payload.
	full := append(append([]byte{}, base...), packPayload(src)...)
	got, found, err := extractPayload(bytes.NewReader(full), int64(len(full)))
	if !found || err != nil {
		t.Fatalf("round-trip: found=%v err=%v", found, err)
	}
	if string(got) != string(src) {
		t.Errorf("round-trip got %q, want %q", got, src)
	}

	// A plain binary (no trailer) is not detected as a standalone.
	if _, found, _ := extractPayload(bytes.NewReader(base), int64(len(base))); found {
		t.Errorf("plain binary should not be detected as a standalone")
	}

	// Magic present but payload corrupt (not valid gzip) -> found=true, error.
	bad := append(append([]byte{}, base...), make([]byte, 40)...) // 40 non-gzip bytes
	footer := make([]byte, sfxFooter)
	binary.LittleEndian.PutUint64(footer[0:8], 30) // claim 30 bytes of "payload"
	binary.LittleEndian.PutUint32(footer[8:12], sfxVersion)
	copy(footer[12:20], sfxMagic)
	bad = append(bad, footer...)
	if _, found, err := extractPayload(bytes.NewReader(bad), int64(len(bad))); !found || err == nil {
		t.Errorf("corrupt payload: want found=true with error, got found=%v err=%v", found, err)
	}

	// Magic present but an incompatible version -> found=true, error.
	verbad := append(append([]byte{}, base...), packPayload(src)...)
	binary.LittleEndian.PutUint32(verbad[len(verbad)-12:len(verbad)-8], sfxVersion+1)
	if _, found, err := extractPayload(bytes.NewReader(verbad), int64(len(verbad))); !found || err == nil {
		t.Errorf("version mismatch: want found=true with error, got found=%v err=%v", found, err)
	}
}
