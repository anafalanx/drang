package eval

import (
	"os"
	"strings"
	"syscall"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

// IsTerminal reports whether f is connected to an interactive terminal (rather than a pipe or a
// file). On Windows a terminal is either a real console — GetConsoleMode succeeds on the handle —
// or an MSYS2/Cygwin pseudo-terminal (mintty, Git Bash), which is a named pipe that GetConsoleMode
// rejects but whose name identifies it as a pty. This replaces a coarse os.ModeCharDevice check
// that mis-reported those ptys, so bare `drang` in Git Bash now starts the REPL.
func IsTerminal(f *os.File) bool {
	if f == nil {
		return false
	}
	h := windows.Handle(f.Fd())
	var mode uint32
	if windows.GetConsoleMode(h, &mode) == nil {
		return true // a real Windows console
	}
	return isCygwinPTY(h)
}

// isCygwinPTY reports whether h is an MSYS2/Cygwin pseudo-terminal: a named pipe whose name looks
// like \msys-<id>-ptyN-... or \cygwin-<id>-ptyN-... (the mintty / Git-Bash tty transport).
func isCygwinPTY(h windows.Handle) bool {
	if ft, err := windows.GetFileType(h); err != nil || ft != windows.FILE_TYPE_PIPE {
		return false
	}
	// FILE_NAME_INFO is a uint32 byte-length followed by the UTF-16 name; size the buffer for a
	// generous pipe-name length.
	var buf [4 + syscall.MAX_PATH*2]byte
	if err := windows.GetFileInformationByHandleEx(h, windows.FileNameInfo, &buf[0], uint32(len(buf))); err != nil {
		return false
	}
	n := *(*uint32)(unsafe.Pointer(&buf[0])) / 2 // byte length -> UTF-16 units
	if n == 0 || int(n) > syscall.MAX_PATH {
		return false
	}
	name := strings.ToLower(string(utf16.Decode((*[syscall.MAX_PATH]uint16)(unsafe.Pointer(&buf[4]))[:n:n])))
	return (strings.Contains(name, "msys-") || strings.Contains(name, "cygwin-")) && strings.Contains(name, "-pty")
}

// EnableUTF8Console sets the console output code page to UTF-8 so drang's UTF-8 string output
// renders correctly on a stock console (which otherwise uses a legacy OEM code page and shows
// mojibake for non-ASCII text). It acts only when stdout or stderr is a real console; redirected
// output gets the raw UTF-8 bytes regardless, so piping to a file or another program is
// unaffected. The change persists for the console after exit (UTF-8 is a safe state to leave it
// in). Best-effort: a failure is ignored.
func EnableUTF8Console() {
	const cpUTF8 = 65001
	if IsTerminal(os.Stdout) || IsTerminal(os.Stderr) {
		_ = windows.SetConsoleOutputCP(cpUTF8)
	}
}
