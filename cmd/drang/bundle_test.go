package main

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStandalonePayloadRoundTrip(t *testing.T) {
	base := []byte("PRETEND-THIS-IS-THE-DRANG-BINARY-IMAGE")
	src := []byte("say(\"hi\")\n$x := 42\nsay($x * 2)\n")

	// A built standalone = base image + packed payload; name + source round-trip.
	full := append(append([]byte{}, base...), packPayload("demo.dr", src, nil)...)
	got, name, _, found, err := extractPayload(bytes.NewReader(full), int64(len(full)))
	if !found || err != nil {
		t.Fatalf("round-trip: found=%v err=%v", found, err)
	}
	if string(got) != string(src) {
		t.Errorf("round-trip source got %q, want %q", got, src)
	}
	if name != "demo.dr" {
		t.Errorf("round-trip name got %q, want demo.dr", name)
	}

	// A plain binary (no trailer) is not detected as a standalone.
	if _, _, _, found, _ := extractPayload(bytes.NewReader(base), int64(len(base))); found {
		t.Errorf("plain binary should not be detected as a standalone")
	}

	// Magic present but payload corrupt (not valid gzip) -> found=true, error.
	bad := append(append([]byte{}, base...), make([]byte, 40)...) // 40 non-gzip bytes
	footer := make([]byte, sfxFooter)
	binary.LittleEndian.PutUint64(footer[0:8], 30) // claim 30 bytes of "payload"
	binary.LittleEndian.PutUint32(footer[8:12], sfxVersion)
	copy(footer[12:20], sfxMagic)
	bad = append(bad, footer...)
	if _, _, _, found, err := extractPayload(bytes.NewReader(bad), int64(len(bad))); !found || err == nil {
		t.Errorf("corrupt payload: want found=true with error, got found=%v err=%v", found, err)
	}

	// Magic present but an incompatible version -> found=true, error.
	verbad := append(append([]byte{}, base...), packPayload("demo.dr", src, nil)...)
	binary.LittleEndian.PutUint32(verbad[len(verbad)-12:len(verbad)-8], sfxVersion+1)
	if _, _, _, found, err := extractPayload(bytes.NewReader(verbad), int64(len(verbad))); !found || err == nil {
		t.Errorf("version mismatch: want found=true with error, got found=%v err=%v", found, err)
	}
}

func TestBundleAssetsRoundTrip(t *testing.T) {
	base := []byte("PRETEND-DRANG-BINARY")
	src := []byte("serve({routes: {}})\n")
	assets := map[string][]byte{
		"index.html":  []byte("<h1>hi</h1>"),
		"css/app.css": []byte("body{color:red}"),
		"logo.png":    {0x89, 0x50, 0x4e, 0x47, 0x00, 0xff}, // arbitrary binary
	}
	full := append(append([]byte{}, base...), packPayload("tool.dr", src, assets)...)
	gotSrc, name, gotAssets, found, err := extractPayload(bytes.NewReader(full), int64(len(full)))
	if !found || err != nil {
		t.Fatalf("round-trip: found=%v err=%v", found, err)
	}
	if string(gotSrc) != string(src) || name != "tool.dr" {
		t.Errorf("source/name round-trip got (%q, %q)", gotSrc, name)
	}
	if len(gotAssets) != len(assets) {
		t.Fatalf("asset count got %d, want %d", len(gotAssets), len(assets))
	}
	for p, want := range assets {
		if got, ok := gotAssets[p]; !ok || !bytes.Equal(got, want) {
			t.Errorf("asset %q: got %v (ok=%v), want %v", p, got, ok, want)
		}
	}

	// No assets -> an empty asset set, still a clean round-trip.
	full2 := append(append([]byte{}, base...), packPayload("tool.dr", src, nil)...)
	_, _, a2, found2, err2 := extractPayload(bytes.NewReader(full2), int64(len(full2)))
	if !found2 || err2 != nil || len(a2) != 0 {
		t.Errorf("no-asset round-trip: found=%v err=%v assets=%d", found2, err2, len(a2))
	}
}

func TestWriteStandaloneRoundTrip(t *testing.T) {
	dir := t.TempDir()
	rt := filepath.Join(dir, "runtime.bin")
	if err := os.WriteFile(rt, []byte("FAKE-RUNTIME-IMAGE-BYTES"), 0o755); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "app.exe")
	src := []byte("say(\"embedded\")\n$x := 7\n")
	if _, err := writeStandalone(rt, out, "tool.dr", src, nil); err != nil {
		t.Fatalf("writeStandalone: %v", err)
	}
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fi, _ := f.Stat()
	got, name, _, found, err := extractPayload(f, fi.Size())
	if !found || err != nil {
		t.Fatalf("extract after write: found=%v err=%v", found, err)
	}
	if string(got) != string(src) || name != "tool.dr" {
		t.Errorf("round-trip got (%q, %q), want (%q, tool.dr)", got, name, src)
	}
	// The atomic write must not leave temp files behind.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".drang-build-") {
			t.Errorf("leftover temp file: %s", e.Name())
		}
	}

	// build -o into a missing subdirectory creates the parent dir.
	out2 := filepath.Join(dir, "nested", "deep", "app.exe")
	if _, err := writeStandalone(rt, out2, "tool.dr", src, nil); err != nil {
		t.Fatalf("writeStandalone into a missing dir: %v", err)
	}
	if _, err := os.Stat(out2); err != nil {
		t.Errorf("expected %s to exist after build: %v", out2, err)
	}
}

func TestSameFile(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.dr")
	if err := os.WriteFile(a, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !sameFile(a, a) {
		t.Error("identical paths should be sameFile")
	}
	// A non-cleaned form of the same path.
	noisy := filepath.Join(dir, "sub", "..", "a.dr")
	if !sameFile(a, noisy) {
		t.Errorf("%q and %q should be sameFile", a, noisy)
	}
	if sameFile(a, filepath.Join(dir, "b.dr")) {
		t.Error("distinct paths should not be sameFile")
	}
}
