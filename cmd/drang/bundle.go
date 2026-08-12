package main

// Standalone executables. `drang build script.dr [--web dir] [--gui] [-o app.exe]` copies
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

	"github.com/anafalanx/drang/internal/eval"
	"github.com/anafalanx/drang/internal/parser"
)

const (
	sfxMagic   = "DRANGsfx" // marks an appended standalone payload
	sfxVersion = uint32(3)  // payload format version (v3 adds the web/ asset tree)
	sfxFooter  = 8 + 4 + 8  // payloadLen + version + magic

	maxStandaloneCompressedBytes = int64(128 << 20)
	maxStandalonePayloadBytes    = int64(256 << 20)
	maxWebAssetBytes             = int64(64 << 20)
	maxWebTotalBytes             = int64(192 << 20)
	maxWebAssetCount             = uint32(65_535)
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
	if plen > maxStandaloneCompressedBytes {
		return nil, "", nil, true, fmt.Errorf("standalone compressed payload exceeds the %d MiB limit", maxStandaloneCompressedBytes>>20)
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
	raw, e := readAllLimited(gz, maxStandalonePayloadBytes, "standalone decompressed payload")
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
	if int64(len(srcB)) > maxSourceBytes {
		return nil, "", nil, true, fmt.Errorf("standalone source exceeds the %d MiB limit", maxSourceBytes>>20)
	}
	nAssets, ok := c.u32()
	if !ok {
		return nil, "", nil, true, fmt.Errorf("standalone payload truncated (asset count)")
	}
	if nAssets > maxWebAssetCount {
		return nil, "", nil, true, fmt.Errorf("standalone payload has too many assets (%d; limit %d)", nAssets, maxWebAssetCount)
	}
	if nAssets > 0 {
		assets = make(map[string][]byte, nAssets)
	}
	var assetTotal int64
	for i := uint32(0); i < nAssets; i++ {
		pathB, ok := c.bytesU16()
		if !ok {
			return nil, "", nil, true, fmt.Errorf("standalone payload truncated (asset path)")
		}
		dataB, ok := c.bytesU32()
		if !ok {
			return nil, "", nil, true, fmt.Errorf("standalone payload truncated (asset data)")
		}
		if int64(len(dataB)) > maxWebAssetBytes {
			return nil, "", nil, true, fmt.Errorf("standalone web asset exceeds the %d MiB limit", maxWebAssetBytes>>20)
		}
		assetTotal += int64(len(dataB))
		if assetTotal > maxWebTotalBytes {
			return nil, "", nil, true, fmt.Errorf("standalone web assets exceed the %d MiB total limit", maxWebTotalBytes>>20)
		}
		path := string(pathB)
		if _, exists := assets[path]; exists {
			return nil, "", nil, true, fmt.Errorf("standalone payload contains duplicate asset path %q", path)
		}
		assets[path] = append([]byte(nil), dataB...) // own the bytes (raw may be reused)
	}
	if c.off != len(c.buf) {
		return nil, "", nil, true, fmt.Errorf("standalone payload has %d trailing byte(s)", len(c.buf)-c.off)
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

// buildStandalone implements `drang build <script.dr> [--web <dir>] [--gui] [-o <output>]`.
func buildStandalone(args []string) {
	var srcPath, outPath, webDir string
	var gui bool
	options := true
	seen := map[string]bool{}
	for i := 0; i < len(args); i++ {
		if options && args[i] == "--" {
			options = false
			continue
		}
		if !options {
			if srcPath != "" {
				fmt.Fprintln(os.Stderr, "drang build: unexpected argument", args[i])
				os.Exit(2)
			}
			srcPath = args[i]
			continue
		}
		switch args[i] {
		case "-o", "--output":
			if seen["output"] {
				fmt.Fprintln(os.Stderr, "drang build: output option specified more than once")
				os.Exit(2)
			}
			seen["output"] = true
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "drang build: -o needs an output path")
				os.Exit(2)
			}
			outPath = args[i+1]
			i++
		case "--web":
			if seen["web"] {
				fmt.Fprintln(os.Stderr, "drang build: --web specified more than once")
				os.Exit(2)
			}
			seen["web"] = true
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "drang build: --web needs a directory")
				os.Exit(2)
			}
			webDir = args[i+1]
			i++
		case "--gui":
			if gui {
				fmt.Fprintln(os.Stderr, "drang build: --gui specified more than once")
				os.Exit(2)
			}
			gui = true
		default:
			if len(args[i]) > 0 && args[i][0] == '-' {
				fmt.Fprintf(os.Stderr, "drang build: unknown option %q (use -- before a source path beginning with '-')\n", args[i])
				os.Exit(2)
			}
			if srcPath != "" {
				fmt.Fprintln(os.Stderr, "drang build: unexpected argument", args[i])
				os.Exit(2)
			}
			srcPath = args[i]
		}
	}
	if srcPath == "" {
		fmt.Fprintln(os.Stderr, "usage: drang build <script.dr> [--web <dir>] [--gui] [-o <output>]")
		os.Exit(2)
	}
	src, err := readFileLimited(srcPath, maxSourceBytes, "source file")
	if err != nil {
		fmt.Fprintln(os.Stderr, "drang:", err)
		os.Exit(1)
	}
	// Build-time validation: the script must parse, so the standalone is guaranteed
	// to at least load. (Runtime errors are still the program's own concern.)
	p := parser.New(string(src))
	prog := p.ParseProgram()
	if reportParseErrors(p, srcPath) {
		os.Exit(1)
	}
	eval.WarnProgramLints(prog, string(src), srcPath, p.Comments(), os.Stderr)
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
	n, err := writeStandalone(exe, outPath, filepath.Base(srcPath), src, assets, gui)
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
	var total int64
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		if uint32(len(out)) >= maxWebAssetCount {
			return fmt.Errorf("asset count exceeds the %d-file limit", maxWebAssetCount)
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if len(rel) > int(^uint16(0)) {
			return fmt.Errorf("asset path is too long to bundle: %s", rel)
		}
		data, err := readFileLimited(p, maxWebAssetBytes, "web asset "+rel)
		if err != nil {
			return err
		}
		total += int64(len(data))
		if total > maxWebTotalBytes {
			return fmt.Errorf("web assets exceed the %d MiB total limit", maxWebTotalBytes>>20)
		}
		out[rel] = data
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
func writeStandalone(runtimeExe, outPath, name string, src []byte, assets map[string][]byte, gui bool) (int64, error) {
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
	if gui {
		if err := setPESubsystem(tmp, imageSubsystemWindowsGUI); err != nil {
			return 0, fmt.Errorf("--gui: %w", err)
		}
	}
	payload, err := packPayload(name, src, assets)
	if err != nil {
		return 0, err
	}
	if _, err := tmp.Write(payload); err != nil {
		return 0, err
	}
	if err := tmp.Sync(); err != nil {
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

const (
	imageSubsystemWindowsGUI = uint16(2)
	peSubsystemOffset        = int64(68) // from either PE32 or PE32+ optional header
)

// setPESubsystem changes the Windows PE Optional Header's Subsystem field in the
// temporary standalone copy. The running interpreter remains a console program;
// only an explicitly requested --gui output is changed.
func setPESubsystem(f *os.File, subsystem uint16) error {
	const (
		dosPEOffset     = int64(0x3c)
		coffHeaderSize  = int64(20)
		peSignatureSize = int64(4)
	)

	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if fi.Size() < dosPEOffset+4 {
		return fmt.Errorf("runtime is not a PE image (DOS header is truncated)")
	}
	var dosMagic [2]byte
	if _, err := f.ReadAt(dosMagic[:], 0); err != nil || string(dosMagic[:]) != "MZ" {
		return fmt.Errorf("runtime is not a PE image (MZ signature missing)")
	}
	var dword [4]byte
	if _, err := f.ReadAt(dword[:], dosPEOffset); err != nil {
		return fmt.Errorf("read PE header offset: %w", err)
	}
	peOffset := int64(binary.LittleEndian.Uint32(dword[:]))
	optionalOffset := peOffset + peSignatureSize + coffHeaderSize
	subsystemOffset := optionalOffset + peSubsystemOffset
	if peOffset < 0 || subsystemOffset+2 > fi.Size() {
		return fmt.Errorf("runtime is not a PE image (optional header is truncated)")
	}
	var signature [4]byte
	if _, err := f.ReadAt(signature[:], peOffset); err != nil || string(signature[:]) != "PE\x00\x00" {
		return fmt.Errorf("runtime is not a PE image (signature missing)")
	}
	var coff [20]byte
	if _, err := f.ReadAt(coff[:], peOffset+peSignatureSize); err != nil {
		return fmt.Errorf("read PE COFF header: %w", err)
	}
	optionalSize := int64(binary.LittleEndian.Uint16(coff[16:18]))
	if optionalSize < peSubsystemOffset+2 || optionalOffset+optionalSize > fi.Size() {
		return fmt.Errorf("runtime is not a PE image (declared optional header is truncated)")
	}
	var magic [2]byte
	if _, err := f.ReadAt(magic[:], optionalOffset); err != nil {
		return fmt.Errorf("read PE optional header: %w", err)
	}
	switch binary.LittleEndian.Uint16(magic[:]) {
	case 0x10b, 0x20b: // PE32, PE32+
	default:
		return fmt.Errorf("unsupported PE optional-header magic 0x%x", binary.LittleEndian.Uint16(magic[:]))
	}
	var field [2]byte
	if _, err := f.ReadAt(field[:], subsystemOffset); err != nil {
		return fmt.Errorf("read PE subsystem: %w", err)
	}
	current := binary.LittleEndian.Uint16(field[:])
	if current != 2 && current != 3 {
		return fmt.Errorf("runtime PE subsystem is %d, expected Windows GUI (2) or console (3)", current)
	}
	binary.LittleEndian.PutUint16(field[:], subsystem)
	if _, err := f.WriteAt(field[:], subsystemOffset); err != nil {
		return fmt.Errorf("write PE subsystem: %w", err)
	}
	return nil
}

// packPayload frames the name + source + assets, compresses them, and appends the
// trailer, returning the bytes to add after the runtime binary. Assets are packed
// in sorted path order, so a given input builds byte-identically.
func packPayload(name string, src []byte, assets map[string][]byte) ([]byte, error) {
	if len(name) > int(^uint16(0)) {
		return nil, fmt.Errorf("standalone source name is too long")
	}
	if int64(len(src)) > maxSourceBytes {
		return nil, fmt.Errorf("standalone source exceeds the %d MiB limit", maxSourceBytes>>20)
	}
	if uint64(len(src)) > uint64(^uint32(0)) {
		return nil, fmt.Errorf("standalone source is too large for the payload format")
	}
	if uint64(len(assets)) > uint64(maxWebAssetCount) {
		return nil, fmt.Errorf("standalone has too many assets (%d; limit %d)", len(assets), maxWebAssetCount)
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
	rawSize := uint64(2+len(name)) + uint64(4+len(src)) + 4
	var assetTotal int64
	for _, p := range paths {
		if len(p) > int(^uint16(0)) {
			return nil, fmt.Errorf("asset path is too long to bundle: %s", p)
		}
		if int64(len(assets[p])) > maxWebAssetBytes || uint64(len(assets[p])) > uint64(^uint32(0)) {
			return nil, fmt.Errorf("web asset %s exceeds the %d MiB limit", p, maxWebAssetBytes>>20)
		}
		assetTotal += int64(len(assets[p]))
		if assetTotal > maxWebTotalBytes {
			return nil, fmt.Errorf("web assets exceed the %d MiB total limit", maxWebTotalBytes>>20)
		}
		rawSize += uint64(2+len(p)) + uint64(4+len(assets[p]))
		if rawSize > uint64(maxStandalonePayloadBytes) {
			return nil, fmt.Errorf("standalone payload exceeds the %d MiB decompressed limit", maxStandalonePayloadBytes>>20)
		}
	}
	putU32(&raw, len(paths))
	for _, p := range paths {
		putU16(&raw, len(p))
		raw.WriteString(p)
		putU32(&raw, len(assets[p]))
		raw.Write(assets[p])
	}

	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	if _, err := zw.Write(raw.Bytes()); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	payload := buf.Bytes()
	if int64(len(payload)) > maxStandaloneCompressedBytes {
		return nil, fmt.Errorf("standalone compressed payload exceeds the %d MiB limit", maxStandaloneCompressedBytes>>20)
	}
	footer := make([]byte, sfxFooter)
	binary.LittleEndian.PutUint64(footer[0:8], uint64(len(payload)))
	binary.LittleEndian.PutUint32(footer[8:12], sfxVersion)
	copy(footer[12:20], sfxMagic)
	return append(payload, footer...), nil
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
