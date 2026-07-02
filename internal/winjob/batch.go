package winjob

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"golang.org/x/sys/windows"
)

// IsBatchTarget reports whether exe is a Windows batch script. CreateProcess does not execute a
// batch file itself — it hands the command line to cmd.exe — and cmd.exe re-parses that line with
// rules that syscall.EscapeArg (the CommandLineToArgvW convention) does not satisfy. So a batch
// file MUST NOT be launched by handing it to CreateProcess directly with an EscapeArg command
// line: an argument containing a double quote can close cmd's quoting, after which '&', '|', '<',
// '>' become live command operators — arbitrary command execution (CVE-2024-24576, "BatBadBut").
// Batch targets are launched through an explicitly, defensively quoted cmd.exe instead
// (see makeBatchCmdLine).
func IsBatchTarget(exe string) bool {
	switch strings.ToLower(filepath.Ext(exe)) {
	case ".bat", ".cmd":
		return true
	}
	return false
}

// comspecPath returns the absolute path to cmd.exe, resolved from DRANG's OWN environment and the
// system directory — never from a child's custom env, which could point ComSpec at a malicious
// "cmd.exe" and turn the batch-file mitigation into an execution vector of its own.
func comspecPath() (string, error) {
	if c := os.Getenv("ComSpec"); c != "" && filepath.IsAbs(c) {
		return c, nil
	}
	if sysDir, err := windows.GetSystemDirectory(); err == nil && sysDir != "" {
		return filepath.Join(sysDir, "cmd.exe"), nil
	}
	if sysRoot := os.Getenv("SystemRoot"); sysRoot != "" {
		return filepath.Join(sysRoot, "System32", "cmd.exe"), nil
	}
	return "", fmt.Errorf("winjob: cannot locate cmd.exe to run a batch file")
}

// makeBatchCmdLine builds a `cmd.exe /e:ON /v:OFF /d /c "<script> <args...>"` command line that
// runs a batch script safely (the CVE-2024-24576 mitigation, ported from Rust's std::process, the
// reference fix). The whole `/c` payload is wrapped in an OUTER quote pair; the script and each
// argument are individually quoted; embedded quotes are doubled ("") — cmd's own literal-quote
// convention, NOT the CommandLineToArgvW `\"` — with backslash runs doubled so the batch's own
// argv parse still round-trips; and each '%' is rewritten `%%cd:~,%` (a zero-length substring of
// the always-present %cd%) so cmd cannot expand %VAR% into the argument. `/e:ON` enables the
// command extensions that the %-trick relies on; `/v:OFF` disables !VAR! delayed expansion; `/d`
// skips AutoRun registry commands.
//
// The command line begins with the literal token `cmd.exe`, which cmd sees as its own program name
// and ignores when parsing switches; the caller passes the resolved, trusted cmd.exe path as
// CreateProcess's application name so the real interpreter runs regardless of PATH.
//
// It returns an error for inputs that cannot be made safe: a script path containing '"' or ending
// in '\', a NUL byte anywhere, or an argument containing a carriage return or newline (which cmd
// would use to truncate the line).
func makeBatchCmdLine(script string, args []string) (string, error) {
	if strings.ContainsRune(script, '"') || strings.HasSuffix(script, `\`) {
		return "", fmt.Errorf("winjob: batch script path %q may not contain a quote or end with a backslash", script)
	}
	if strings.IndexByte(script, 0) >= 0 {
		return "", fmt.Errorf("winjob: batch script path contains a NUL byte")
	}
	var b strings.Builder
	b.WriteString(`cmd.exe /e:ON /v:OFF /d /c "`) // opens the outer /c quote
	b.WriteByte('"')                               // opens the script's own quote
	b.WriteString(script)
	b.WriteByte('"') // closes the script's quote
	for _, a := range args {
		if strings.IndexByte(a, 0) >= 0 {
			return "", fmt.Errorf("winjob: batch argument contains a NUL byte: %q", a)
		}
		if strings.ContainsAny(a, "\r\n") {
			return "", fmt.Errorf("winjob: batch argument contains a newline: %q", a)
		}
		b.WriteByte(' ')
		appendBatchArg(&b, a)
	}
	b.WriteByte('"') // closes the outer /c quote
	return b.String(), nil
}

// batchUnquoted is the set of ASCII symbols that do NOT force an argument to be quoted; every other
// non-alphanumeric ASCII character (and any control character) does. This "quote unless known-safe"
// allowlist is Rust's, and is deliberately conservative.
const batchUnquoted = `#$*+-./:?@\_`

// appendBatchArg appends arg to b using Rust's std::process batch-argument quoting. See
// makeBatchCmdLine for the scheme; the caller has already rejected NUL / CR / LF.
func appendBatchArg(b *strings.Builder, arg string) {
	quote := arg == "" || strings.HasSuffix(arg, `\`)
	if !quote {
		for _, r := range arg {
			asciiNeedsQuote := r < 0x80 && !(isASCIIAlphaNumeric(r) || strings.ContainsRune(batchUnquoted, r))
			if asciiNeedsQuote || unicode.IsControl(r) {
				quote = true
				break
			}
		}
	}
	if quote {
		b.WriteByte('"')
	}
	// '\\' '"' '%' '\r' are ASCII and never appear inside a UTF-8 multibyte sequence, so this byte
	// walk matches Rust's UTF-16-code-unit walk for the characters it acts on; other bytes pass
	// through unchanged.
	backslashes := 0
	for i := 0; i < len(arg); i++ {
		c := arg[i]
		if c == '\\' {
			backslashes++
		} else {
			if c == '"' {
				// Double the preceding backslash run to 2n, then double the quote to escape it.
				for k := 0; k < backslashes; k++ {
					b.WriteByte('\\')
				}
				b.WriteByte('"')
			} else if c == '%' || c == '\r' {
				b.WriteString("%%cd:~,")
			}
			backslashes = 0
		}
		b.WriteByte(c)
	}
	if quote {
		// Double the trailing backslash run to 2n before the closing quote.
		for k := 0; k < backslashes; k++ {
			b.WriteByte('\\')
		}
		b.WriteByte('"')
	}
}

func isASCIIAlphaNumeric(r rune) bool {
	return (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}
