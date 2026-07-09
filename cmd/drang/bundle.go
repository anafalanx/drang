package main

// Standalone executables. `drang build script.dr [--web dir] [-o app.exe]` copies
// this drang binary and appends the gzip-compressed source — and, optionally, a
// tree of web/ assets that serve() can serve from memory — followed by a fixed
// trailer. At startup drang inspects its own tail: if the trailer is present it
// runs the embedded program (standalone mode); otherwise it behaves as the CLI.
//
// Trailer layout (20 bytes, at the very end of the file):
//   [ payloadLen : uint64 LE ][ version : uint32 LE ][ magic : 8 bytes ]
// The compressed payload sits immediately before the trailer; once decompressed
// it is framed as:
//   [ nameLen : uint16 LE ][ source basename ]
//   [ srcLen  : uint32 LE ][ source ]
//   [ nAssets : uint32 LE ]  then nAssets × ( [ pathLen : uint16 LE ][ path ]
//                                             [ dataLen : uint32 LE ][ data ] )
// so a standalone's runtime errors can name the original script (zdr.dr:line:col)
// and serve() can serve the bundled assets from memory. A standalone always
// carries the matching runtime, so the format never needs cross-version compat.

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/anafalanx/drang/internal/parser"
)

const (
	sfxMagic   = "DRANGsfx" // marks an appended standalone payload
	sfxVersion = uint32(3)  // payload format version (v3 adds the web/ asset tree)
	sfxFooter  = 8 + 4 + 8  // payloadLen + version + magic
)

// embeddedProgram returns the standalone program (and any bundled web assets)
// appended to this executable. found reports whether the trailer is present
// (true => built standalone, false => plain drang CLI). err is non-nil only when
// the trailer IS present but the payload can't be read (corruption or an
// incompatible build), which the caller should treat as fatal.
func embeddedProgram() (src []byte, name string, assets map[string][]byte, found bool, err error) {
	exe, e := os.Executable()
	if e != nil {
		return nil, "", nil, false, nil
	}
	if real, e := filepath.EvalSymlinks(exe); e == nil {
		exe = real
	}
	f, e := os.Open(exe)
	if e != nil {
		return nil, "", nil, false, nil
	}
	defer f.Close()
	fi, e := f.Stat()
	if e != nil {
		return nil, "", nil, false, nil
	}
	return extractPayload(f, fi.Size())
}

// extractPayload reads an appended standalone payload from r (a file of the given
// total size), returning the embedded source, its basename, and any bundled web
// assets. It is the I/O-decoupled core of embeddedProgram, exposed for tests.
func extractPayload(r io.ReaderAt, size int64) (src []byte, name string, assets map[string][]byte, found bool, err error) {
	if size < int64(sfxFooter) {
		return nil, "", nil, false, nil
	}
	footer := make([]byte, sfxFooter)
	if _, e := r.ReadAt(footer, size-int64(sfxFooter)); e != nil {
		return nil, "", nil, false, nil
	}
	if string(footer[12:20]) != sfxMagic {
		return nil, "", nil, false, nil // plain binary
	}
	// From here the trailer is ours: any problem is a real error.
	if v := binary.LittleEndian.Uint32(footer[8:12]); v != sfxVersion {
		return nil, "", nil, true, fmt.Errorf("standalone payload version %d, this drang understands %d", v, sfxVersion)
	}
	plen := int64(binary.LittleEndian.Uint64(footer[0:8]))
	start := size - int64(sfxFooter) - plen
	if plen < 0 || start < 0 {
		return nil, "", nil, true, fmt.Errorf("standalone payload length out of range")
	}
	comp := make([]byte, plen)
	if _, e := r.ReadAt(comp, start); e != nil {
		return nil, "", nil, true, e
	}
	gz, e := gzip.NewReader(bytes.NewReader(comp))
	if e != nil {
		return nil, "", nil, true, e
	}
	defer gz.Close()
	raw, e := io.ReadAll(gz)
	if e != nil {
		return nil, "", nil, true, e
	}
	return unframePayload(raw)
}

// unframePayload decodes the decompressed [name][source][assets] framing, with a
// bounds check on every field so a truncated/garbled payload is a clean error.
func unframePayload(raw []byte) (src []byte, name string, assets map[string][]byte, found bool, err error) {
	c := &byteCursor{buf: raw}
	nameB, ok := c.bytesU16()
	if !ok {
		return nil, "", nil, true, fmt.Errorf("standalone payload truncated (name)")
	}
	srcB, ok := c.bytesU32()
	if !ok {
		return nil, "", nil, true, fmt.Errorf("standalone payload truncated (source)")
	}
	nAssets, ok := c.u32()
	if !ok {
		return nil, "", nil, true, fmt.Errorf("standalone payload truncated (asset count)")
	}
	if nAssets > 0 {
		assets = make(map[string][]byte, nAssets)
	}
	for i := uint32(0); i < nAssets; i++ {
		pathB, ok := c.bytesU16()
		if !ok {
			return nil, "", nil, true, fmt.Errorf("standalone payload truncated (asset path)")
		}
		dataB, ok := c.bytesU32()
		if !ok {
			return nil, "", nil, true, fmt.Errorf("standalone payload truncated (asset data)")
		}
		assets[string(pathB)] = append([]byte(nil), dataB...) // own the bytes (raw may be reused)
	}
	return srcB, string(nameB), assets, true, nil
}

// byteCursor reads little-endian, length-prefixed fields from a byte slice.
type byteCursor struct {
	buf []byte
	off int
}

func (c *byteCursor) u16() (uint16, bool) {
	if c.off+2 > len(c.buf) {
		return 0, false
	}
	v := binary.LittleEndian.Uint16(c.buf[c.off : c.off+2])
	c.off += 2
	return v, true
}
func (c *byteCursor) u32() (uint32, bool) {
	if c.off+4 > len(c.buf) {
		return 0, false
	}
	v := binary.LittleEndian.Uint32(c.buf[c.off : c.off+4])
	c.off += 4
	return v, true
}
func (c *byteCursor) bytesU16() ([]byte, bool) {
	n, ok := c.u16()
	if !ok || c.off+int(n) > len(c.buf) {
		return nil, false
	}
	b := c.buf[c.off : c.off+int(n)]
	c.off += int(n)
	return b, true
}
func (c *byteCursor) bytesU32() ([]byte, bool) {
	n, ok := c.u32()
	if !ok || c.off+int(n) > len(c.buf) {
		return nil, false
	}
	b := c.buf[c.off : c.off+int(n)]
	c.off += int(n)
	return b, true
}

// standaloneOrigin names the running executable, the fallback origin when an
// embedded payload carries no source name.
func standaloneOrigin() string {
	if exe, err := os.Executable(); err == nil {
		return filepath.Base(exe)
	}
	return "<standalone>"
}

// buildStandalone implements `drang build <script.dr> [--web <dir>] [-o <output>]`.
func buildStandalone(args []string) {
	var srcPath, outPath, webDir string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-o", "--output":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "drang build: -o needs an output path")
				os.Exit(2)
			}
			outPath = args[i+1]
			i++
		case "--web":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "drang build: --web needs a directory")
				os.Exit(2)
			}
			webDir = args[i+1]
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
		fmt.Fprintln(os.Stderr, "usage: drang build <script.dr> [--web <dir>] [-o <output>]")
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
	var assets map[string][]byte
	if webDir != "" {
		a, err := readWebDir(webDir)
		if err != nil {
			fmt.Fprintln(os.Stderr, "drang build:", err)
			os.Exit(1)
		}
		assets = a
	}
	if outPath == "" {
		outPath = defaultOutput(srcPath)
	}
	// The base binary is always this running drang — the appended payload is just data.
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
		fmt.Fprintln(os.Stderr, "drang build: refusing to overwrite the runtime binary — choose a different -o")
		os.Exit(1)
	}
	n, err := writeStandalone(exe, outPath, filepath.Base(srcPath), src, assets)
	if err != nil {
		fmt.Fprintln(os.Stderr, "drang build:", err)
		os.Exit(1)
	}
	if len(assets) > 0 {
		fmt.Printf("built %s (%d bytes, %d embedded asset(s)) from %s\n", outPath, n, len(assets), srcPath)
	} else {
		fmt.Printf("built %s (%d bytes) from %s\n", outPath, n, srcPath)
	}
}

// readWebDir reads a directory tree into a map keyed by forward-slash path
// relative to dir. Directories and non-regular files (symlinks, devices) are
// skipped; an empty or missing directory is an error.
func readWebDir(dir string) (map[string][]byte, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("--web %s: %w", dir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("--web %s: not a directory", dir)
	}
	out := map[string][]byte{}
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = data
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("--web %s: no files found", dir)
	}
	return out, nil
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

func defaultOutput(srcPath string) string {
	base := filepath.Base(srcPath)
	base = base[:len(base)-len(filepath.Ext(base))]
	if base == "" {
		base = "app"
	}
	return base + ".exe"
}

// writeStandalone copies the runtime binary, appends the packed payload
// (compressed source + assets + trailer), and atomically moves the result into
// place. It writes to a temp file in the destination directory and renames on
// success, so a failed or partial build never truncates an existing file.
func writeStandalone(runtimeExe, outPath, name string, src []byte, assets map[string][]byte) (int64, error) {
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
	if _, err := tmp.Write(packPayload(name, src, assets)); err != nil {
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

// packPayload frames the name + source + assets, compresses them, and appends the
// trailer, returning the bytes to add after the runtime binary. Assets are packed
// in sorted path order, so a given input builds byte-identically.
func packPayload(name string, src []byte, assets map[string][]byte) []byte {
	if len(name) > 0xffff {
		name = name[:0xffff] // basenames are short; clamp defensively
	}
	var raw bytes.Buffer
	putU16(&raw, len(name))
	raw.WriteString(name)
	putU32(&raw, len(src))
	raw.Write(src)

	paths := make([]string, 0, len(assets))
	for p := range assets {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	putU32(&raw, len(paths))
	for _, p := range paths {
		putU16(&raw, len(p))
		raw.WriteString(p)
		putU32(&raw, len(assets[p]))
		raw.Write(assets[p])
	}

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

func putU16(b *bytes.Buffer, n int) {
	var x [2]byte
	binary.LittleEndian.PutUint16(x[:], uint16(n))
	b.Write(x[:])
}
func putU32(b *bytes.Buffer, n int) {
	var x [4]byte
	binary.LittleEndian.PutUint32(x[:], uint32(n))
	b.Write(x[:])
}
