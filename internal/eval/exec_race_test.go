package eval

import (
	"bytes"
	"sync"
	"testing"
	"time"

	"github.com/anafalanx/drang/internal/value"
)

// TestRunSharedWriterRace is the D1 regression: when run()'s stdout/stderr are a shared
// non-*os.File sink (a buffer here, an embedding sink in the future), the per-child stdio copiers
// must serialize on outMu (via lockedShared) rather than racing the common Writer. Eight run()s
// fire concurrently — each floods the shared buffer — so under `go test -race` this fails if the
// copiers write the sink unsynchronized. (os.Stdout/os.Stderr take the lock-free *os.File path and
// never hit this; the mutex only guards buffer/embedding sinks.)
func TestRunSharedWriterRace(t *testing.T) {
	var buf bytes.Buffer // shared, non-*os.File: the case lockedShared must fence
	oldOut, oldErr := stdout, stderr
	stdout, stderr = &buf, &buf
	defer func() { stdout, stderr = oldOut, oldErr }()

	const workers = 8
	errs := make([]error, workers)
	oks := make([]bool, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Flood stderr (== the shared buffer) with many small writes so the copiers overlap.
			v, err := builtinRun([]value.Value{
				value.MakeStr("cmd"), value.MakeStr("/c"),
				value.MakeStr("for /L %n in (1,1,200) do @echo x>&2"),
			})
			errs[i] = err
			oks[i] = err == nil && v.Truthy()
		}(i)
	}
	wg.Wait()

	for i := 0; i < workers; i++ {
		if errs[i] != nil {
			t.Fatalf("worker %d: builtinRun aborted: %v", i, errs[i])
		}
		if !oks[i] {
			t.Errorf("worker %d: run did not report success", i)
		}
	}
	// Sanity: the children actually wrote to the shared sink (else there was no race to catch).
	if buf.Len() < 1000 {
		t.Fatalf("shared sink got only %d bytes; the loop command produced no output", buf.Len())
	}
}

// TestPipeIntermediateTimeout is the D2 regression: a timeout in a NON-last pipeline stage must
// govern the pipeline's status. The producer ignores its pipe (>nul) so it keeps running until the
// {timeout} tree-kills it (code 124); the consumer exits 0 immediately. Keying only off the last
// stage (bash exit semantics) would report success and silently swallow the producer's timeout —
// the aggregate must surface 124 instead.
func TestPipeIntermediateTimeout(t *testing.T) {
	argvs := [][]string{
		{"cmd", "/c", "ping -n 30 127.0.0.1 >nul"}, // slow, output discarded: runs until killed
		{"cmd", "/c", "exit 0"},                    // exits 0 at once, ignoring stdin
	}
	start := time.Now()
	r := runPipeline(argvs, execOpts{timeout: 500 * time.Millisecond})
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("pipeline ran %v; the intermediate timeout did not tree-kill the producer", elapsed)
	}
	if !r.IsErr() {
		t.Fatalf("pipeline swallowed the intermediate timeout: got %v (%s), want a 124 Err", r.Display(), r.TypeName())
	}
	if r.ErrCode() != 124 {
		t.Errorf("intermediate-timeout code = %d, want 124", r.ErrCode())
	}
}
