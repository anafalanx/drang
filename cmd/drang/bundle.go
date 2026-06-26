package main

// Standalone executables. `drang build script.dr -o app.exe` copies the running
// drang binary and appends the gzip-compressed source followed by a fixed trailer.
// At startup drang inspects its own tail: if the trailer is present it runs the
// embedded program (standalone mode); otherwise it behaves as the normal CLI.
//
// Trailer layout (20 bytes, at the very end of the file):
//   [ payloadLen : uint64 LE ][ version : uint32 LE ][ magic : 8 bytes ]
// The compressed payload sits immediately before the trailer.

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/anafalanx/drang/internal/parser"
)

const (
	sfxMagic   = "DRANGsfx" // marks an appended standalone payload
	sfxVersion = uint32(1)  // payload format version
	sfxFooter  = 8 + 4 + 8  // payloadLen + version + magic
)

// embeddedProgram returns the standalone program appended to this executable.
// found reports whether the standalone trailer is present (true => this is a
// built standalone, false => a plain drang binary in normal CLI mode). err is
// non-nil only when the trailer IS present but the payload can't be read
// (corruption or an incompatible build), which the caller should treat as fatal
// rather than silently dropping into CLI mode.
func embeddedProgram() (src []byte, found bool, err error) {
	exe, e := os.Executable()
	if e != nil {
		return nil, false, nil
	}
	if real, e := filepath.EvalSymlinks(exe); e == nil {
		exe = real
	}
	f, e := os.Open(exe)
	if e != nil {
		return nil, false, nil
	}
	defer f.Close()
	fi, e := f.Stat()
	if e != nil {
		return nil, false, nil
	}
	return extractPayload(f, fi.Size())
}

// extractPayload reads an appended standalone payload from r (a file of the given
// total size). It is the I/O-decoupled core of embeddedProgram, exposed for tests.
func extractPayload(r io.ReaderAt, size int64) (src []byte, found bool, err error) {
	if size < int64(sfxFooter) {
		return nil, false, nil
	}
	footer := make([]byte, sfxFooter)
	if _, e := r.ReadAt(footer, size-int64(sfxFooter)); e != nil {
		return nil, false, nil
	}
	if string(footer[12:20]) != sfxMagic {
		return nil, false, nil // plain binary
	}
	// From here the trailer is ours: any problem is a real error.
	if v := binary.LittleEndian.Uint32(footer[8:12]); v != sfxVersion {
		return nil, true, fmt.Errorf("standalone payload version %d, this drang understands %d", v, sfxVersion)
	}
	plen := int64(binary.LittleEndian.Uint64(footer[0:8]))
	start := size - int64(sfxFooter) - plen
	if plen < 0 || start < 0 {
		return nil, true, fmt.Errorf("standalone payload length out of range")
	}
	comp := make([]byte, plen)
	if _, e := r.ReadAt(comp, start); e != nil {
		return nil, true, e
	}
	gz, e := gzip.NewReader(bytes.NewReader(comp))
	if e != nil {
		return nil, true, e
	}
	defer gz.Close()
	src, e = io.ReadAll(gz)
	if e != nil {
		return nil, true, e
	}
	return src, true, nil
}

// standaloneOrigin names the running executable for error messages.
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
	n, err := writeStandalone(exe, outPath, src)
	if err != nil {
		fmt.Fprintln(os.Stderr, "drang build:", err)
		os.Exit(1)
	}
	fmt.Printf("built %s (%d bytes) from %s\n", outPath, n, srcPath)
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

// writeStandalone copies the runtime binary to outPath, then appends the packed
// payload (compressed source + trailer). Returns the total output size.
func writeStandalone(runtimeExe, outPath string, src []byte) (int64, error) {
	in, err := os.Open(runtimeExe)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	out, err := os.OpenFile(outPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return 0, err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return 0, err
	}
	if _, err := out.Write(packPayload(src)); err != nil {
		return 0, err
	}
	if fi, err := out.Stat(); err == nil {
		return fi.Size(), nil
	}
	return 0, nil
}

// packPayload compresses src and appends the trailer, returning the bytes to add
// after the runtime binary: gzip(src) followed by the 20-byte trailer.
func packPayload(src []byte) []byte {
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	_, _ = zw.Write(src)
	_ = zw.Close()
	payload := buf.Bytes()
	footer := make([]byte, sfxFooter)
	binary.LittleEndian.PutUint64(footer[0:8], uint64(len(payload)))
	binary.LittleEndian.PutUint32(footer[8:12], sfxVersion)
	copy(footer[12:20], sfxMagic)
	return append(payload, footer...)
}
