package main

import (
	"fmt"
	"io"
	"os"
)

const (
	// Source is parsed as one in-memory string by the current lexer/parser. Bound it
	// before allocation so an accidental device/huge file cannot exhaust the process.
	maxSourceBytes = int64(64 << 20)
)

// readAllLimited reads at most limit bytes plus one sentinel byte. The caller gets
// a stable, descriptive error instead of an unbounded allocation.
func readAllLimited(r io.Reader, limit int64, what string) ([]byte, error) {
	if limit < 0 {
		return nil, fmt.Errorf("%s has an invalid read limit", what)
	}
	lr := &io.LimitedReader{R: r, N: limit + 1}
	b, err := io.ReadAll(lr)
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%s exceeds the %d MiB limit", what, limit>>20)
	}
	return b, nil
}

func readFileLimited(path string, limit int64, what string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if fi, err := f.Stat(); err == nil && fi.Size() > limit {
		return nil, fmt.Errorf("%s exceeds the %d MiB limit", what, limit>>20)
	}
	return readAllLimited(f, limit, what)
}
