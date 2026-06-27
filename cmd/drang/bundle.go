package main

// Standalone executables. `drang build script.dr -o app.exe` copies the running
// drang binary and appends the gzip-compressed source followed by a fixed trailer.
// At startup drang inspects its own tail: if the trailer is present it runs the
// embedded program (standalone mode); otherwise it behaves as the normal CLI.
//
// Trailer layout (20 bytes, at the very end of the file):
//   [ payloadLen : uint64 LE ][ version : uint32 LE ][ magic : 8 bytes ]
// The compressed payload sits immediately before the trailer; once decompressed
// it is framed as [ nameLen : uint16 LE ][ source basename ][ source ], so a
// standalone's runtime errors can name the original script (zdr.dr:line:col)
// instead of the executable. (A standalone always carries the matching runtime,
// so the format never needs cross-version compatibility.)

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/anafalanx/drang/internal/parser"
)

const (
	sfxMagic   = "DRANGsfx" // marks an appended standalone payload
	sfxVersion = uint32(2)  // payload format version (v2 frames the source name)
	sfxFooter  = 8 + 4 + 8  // payloadLen + version + magic
)

// embeddedProgram returns the standalone program appended to this executable.
// found reports whether the standalone trailer is present (true => this is a
// built standalone, false => a plain drang binary in normal CLI mode). err is
// non-nil only when the trailer IS present but the payload can't be read
// (corruption or an incompatible build), which the caller should treat as fatal
// rather than silently dropping into CLI mode.
func embeddedProgram() (src []byte, name string, found bool, err error) {
	exe, e := os.Executable()
	if e != nil {
		return nil, "", false, nil
	}
	if real, e := filepath.EvalSymlinks(exe); e == nil {
		exe = real
	}
	f, e := os.Open(exe)
	if e != nil {
		return nil, "", false, nil
	}
	defer f.Close()
	fi, e := f.Stat()
	if e != nil {
		return nil, "", false, nil
	}
	return extractPayload(f, fi.Size())
}

// extractPayload reads an appended standalone payload from r (a file of the given
// total size), returning the embedded source and its original basename. It is the
// I/O-decoupled core of embeddedProgram, exposed for tests.
func extractPayload(r io.ReaderAt, size int64) (src []byte, name string, found bool, err error) {
	if size < int64(sfxFooter) {
		return nil, "", false, nil
	}
	footer := make([]byte, sfxFooter)
	if _, e := r.ReadAt(footer, size-int64(sfxFooter)); e != nil {
		return nil, "", false, nil
	}
	if string(footer[12:20]) != sfxMagic {
		return nil, "", false, nil // plain binary
	}
	// From here the trailer is ours: any problem is a real error.
	if v := binary.LittleEndian.Uint32(footer[8:12]); v != sfxVersion {
		return nil, "", true, fmt.Errorf("standalone payload version %d, this drang understands %d", v, sfxVersion)
	}
	plen := int64(binary.LittleEndian.Uint64(footer[0:8]))
	start := size - int64(sfxFooter) - plen
	if plen < 0 || start < 0 {
		return nil, "", true, fmt.Errorf("standalone payload length out of range")
	}
	comp := make([]byte, plen)
	if _, e := r.ReadAt(comp, start); e != nil {
		return nil, "", true, e
	}
	gz, e := gzip.NewReader(bytes.NewReader(comp))
	if e != nil {
		return nil, "", true, e
	}
	defer gz.Close()
	raw, e := io.ReadAll(gz)
	if e != nil {
		return nil, "", true, e
	}
	// Framing: [uint16 nameLen][name][source].
	if len(raw) < 2 {
		return nil, "", true, fmt.Errorf("standalone payload truncated")
	}
	nlen := int(binary.LittleEndian.Uint16(raw[0:2]))
	if 2+nlen > len(raw) {
		return nil, "", true, fmt.Errorf("standalone payload name length out of range")
	}
	return raw[2+nlen:], string(raw[2 : 2+nlen]), true, nil
}

// standaloneOrigin names the running executable, the fallback origin when an
// embedded payload carries no source name.
func standaloneOrigin() string {
	if exe, err := os.Executable(); err == nil {
		return filepath.Base(exe)
	}
	return "<standalone>"
}

// buildStandalone implements `drang build <script.dr> [-o <output>]`.
func buildStandalone(args []string) {
	var srcPath, outPath string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-o", "--output":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "drang build: -o needs an output path")
				os.Exit(2)
			}
			outPath = args[i+1]
			i++
		default:
			if srcPath != "" {
				fmt.Fprintln(os.Stderr, "drang build: unexpected argument", args[i])
				os.Exit(2)
			}
			srcPath = args[i]
		}
	}
	if srcPath == "" {
		fmt.Fprintln(os.Stderr, "usage: drang build <script.dr> [-o <output>]")
		os.Exit(2)
	}
	src, err := os.ReadFile(srcPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "drang:", err)
		os.Exit(1)
	}
	// Build-time validation: the script must parse, so the standalone is guaranteed
	// to at least load. (Runtime errors are still the program's own concern.)
	p := parser.New(string(src))
	p.ParseProgram()
	if reportParseErrors(p, srcPath) {
		os.Exit(1)
	}
	if outPath == "" {
		outPath = defaultOutput(srcPath)
	}
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "drang: cannot locate the drang binary:", err)
		os.Exit(1)
	}
	if real, e := filepath.EvalSymlinks(exe); e == nil {
		exe = real
	}
	// Never clobber the source or the running interpreter — an easy, irrecoverable
	// mistake (e.g. an `-o` that points back at the script).
	if sameFile(outPath, srcPath) {
		fmt.Fprintf(os.Stderr, "drang build: refusing to overwrite the source file %s — choose a different -o\n", srcPath)
		os.Exit(1)
	}
	if sameFile(outPath, exe) {
		fmt.Fprintln(os.Stderr, "drang build: refusing to overwrite the running drang binary — choose a different -o")
		os.Exit(1)
	}
	n, err := writeStandalone(exe, outPath, filepath.Base(srcPath), src)
	if err != nil {
		fmt.Fprintln(os.Stderr, "drang build:", err)
		os.Exit(1)
	}
	signIfDarwin(outPath)
	fmt.Printf("built %s (%d bytes) from %s\n", outPath, n, srcPath)
}

// sameFile reports whether a and b denote the same file. It compares cleaned
// absolute paths and, when both exist, falls back to os.SameFile (which also
// catches symlinks, hardlinks, and case-insensitive filesystems).
func sameFile(a, b string) bool {
	if aa, err := filepath.Abs(a); err == nil {
		if ab, err := filepath.Abs(b); err == nil && aa == ab {
			return true
		}
	}
	fa, ea := os.Stat(a)
	fb, eb := os.Stat(b)
	return ea == nil && eb == nil && os.SameFile(fa, fb)
}

// signIfDarwin best-effort ad-hoc-signs the output on macOS, where appending the
// payload invalidates the Mach-O signature and an unsigned binary is killed on
// Apple Silicon. On failure it prints the manual command rather than failing the
// build. A no-op on other platforms.
func signIfDarwin(outPath string) {
	if runtime.GOOS != "darwin" {
		return
	}
	if out, err := exec.Command("codesign", "--force", "--sign", "-", outPath).CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "drang build: warning: could not ad-hoc sign %s (%v) — run: codesign -s - %q\n%s", outPath, err, outPath, out)
	}
}

func defaultOutput(srcPath string) string {
	base := filepath.Base(srcPath)
	base = base[:len(base)-len(filepath.Ext(base))]
	if base == "" {
		base = "app"
	}
	if runtime.GOOS == "windows" {
		return base + ".exe"
	}
	return base
}

// writeStandalone copies the runtime binary, appends the packed payload
// (compressed source + trailer), and atomically moves the result into place.
// It writes to a temp file in the destination directory and renames on success,
// so a failed or partial build never truncates an existing file. Returns the
// total output size.
func writeStandalone(runtimeExe, outPath, name string, src []byte) (int64, error) {
	in, err := os.Open(runtimeExe)
	if err != nil {
		return 0, err
	}
	defer in.Close()

	dir := filepath.Dir(outPath)
	if err := os.MkdirAll(dir, 0o755); err != nil { // create the -o parent dir if missing
		return 0, err
	}
	tmp, err := os.CreateTemp(dir, ".drang-build-*")
	if err != nil {
		return 0, err
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		tmp.Close()
		if !committed {
			os.Remove(tmpName)
		}
	}()

	if _, err := io.Copy(tmp, in); err != nil {
		return 0, err
	}
	if _, err := tmp.Write(packPayload(name, src)); err != nil {
		return 0, err
	}
	if err := tmp.Close(); err != nil {
		return 0, err
	}
	if err := os.Chmod(tmpName, 0o755); err != nil {
		return 0, err
	}
	if err := os.Rename(tmpName, outPath); err != nil {
		return 0, err
	}
	committed = true
	if fi, err := os.Stat(outPath); err == nil {
		return fi.Size(), nil
	}
	return 0, nil
}

// packPayload frames the name + source, compresses them, and appends the trailer,
// returning the bytes to add after the runtime binary.
func packPayload(name string, src []byte) []byte {
	if len(name) > 0xffff {
		name = name[:0xffff] // basenames are short; clamp defensively
	}
	var raw bytes.Buffer
	var nl [2]byte
	binary.LittleEndian.PutUint16(nl[:], uint16(len(name)))
	raw.Write(nl[:])
	raw.WriteString(name)
	raw.Write(src)

	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	_, _ = zw.Write(raw.Bytes())
	_ = zw.Close()
	payload := buf.Bytes()
	footer := make([]byte, sfxFooter)
	binary.LittleEndian.PutUint64(footer[0:8], uint64(len(payload)))
	binary.LittleEndian.PutUint32(footer[8:12], sfxVersion)
	copy(footer[12:20], sfxMagic)
	return append(payload, footer...)
}
