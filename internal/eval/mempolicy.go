package eval

import (
	"os"
	"runtime/debug"
	"unsafe"

	"golang.org/x/sys/windows"
)

// GC / memory policy for one-shot runs.
//
// drang scripts are usually short-lived: they allocate, produce output, and exit. Go's default
// GC (GOGC=100) is tuned for memory-frugal, long-running servers — it collects every time the
// heap doubles, which is more often than a short-lived CLI needs. So a `drang script.dr` /
// `-e` / `-n`/`-p` run RELAXES the GC (a higher GOGC → collect less often) and installs a SOFT
// MEMORY LIMIT, sized from available physical RAM, as an OOM backstop.
//
// Why relax rather than disable: turning GC fully OFF backfires. A garbage-heavy script — which
// is most of what drang does (text munging) — then piles transient objects into an ever-growing
// heap, its working set falls out of CPU cache, and every allocation starts touching cold
// memory. Measured on a garbage-heavy loop, GC-off ran ~55% SLOWER than the default; a relaxed
// GOGC=400 ran ~9% FASTER across the whole benchmark suite (up to ~25% on the garbage-heavy
// case). Still collecting keeps the heap small and cache-hot — just less often. The sweet spot
// is "collect less," not "don't collect."
//
// Scope and honesty:
//   - The limit bounds drang's OWN Go runtime footprint (heap + goroutine stacks + runtime
//     structures — Sys minus HeapReleased), not the heap alone. It does NOT bound the child
//     processes drang spawns: those are separate address spaces, bounded by the Job Object
//     {max_memory}/{max_job_memory} caps — which is why the sizing leaves generous headroom for
//     them and the OS rather than claiming all free RAM.
//   - The REPL keeps default GC: it is long-lived and interactive. This policy is applied only on
//     the one-shot execution paths.
//   - Program semantics are unaffected — arithmetic, strings, control flow, output. The one
//     program-observable consequence is that the GC really is relaxed, so the introspection knob
//     drang_gc() reports the relaxed baseline (relaxedGCPercent) instead of Go's default 100 on a
//     one-shot run. That is an honest reflection of the policy, not a separate behavior change:
//     hiding it (reporting 100 while running at 400) would break the documented
//     `$old := drang_gc(...); ...; drang_gc($old)` save/restore idiom, silently undoing the policy.
//
// Overrides: if GOGC or GOMEMLIMIT is set in the environment, the user has taken control and the
// auto-policy is skipped entirely. drang_gc(...) can retune GOGC at runtime; the soft limit stays
// as a backstop regardless.

// x/sys/windows does not expose GlobalMemoryStatusEx, so bind it directly from kernel32.
var (
	modkernel32              = windows.NewLazySystemDLL("kernel32.dll")
	procGlobalMemoryStatusEx = modkernel32.NewProc("GlobalMemoryStatusEx")
)

// memoryStatusEx mirrors the Win32 MEMORYSTATUSEX layout.
type memoryStatusEx struct {
	length               uint32
	memoryLoad           uint32
	totalPhys            uint64
	availPhys            uint64
	totalPageFile        uint64
	availPageFile        uint64
	totalVirtual         uint64
	availVirtual         uint64
	availExtendedVirtual uint64
}

// availablePhysBytes returns the currently-free physical RAM, or (0, false) if the query fails.
func availablePhysBytes() (uint64, bool) {
	var m memoryStatusEx
	m.length = uint32(unsafe.Sizeof(m))
	r, _, _ := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&m)))
	if r == 0 {
		return 0, false
	}
	return m.availPhys, true
}

const (
	memLimitFloor    = 256 << 20 // never cap drang's heap below 256 MiB, even on a tight machine
	memLimitFallback = 1 << 30   // 1 GiB, used only if the RAM query fails
)

// pickMemoryLimit sizes the soft heap limit from available physical RAM: half of it — leaving
// the other half for child processes, the OS, and other apps — floored so a small or loaded
// machine still gives the interpreter a workable heap before GC engages. It is a BACKSTOP, not
// a budget: on a roomy machine the limit sits far above any normal script, so GC effectively
// never runs; on a constrained one it is lower, so collection kicks in sooner. Either way it
// only prevents unbounded growth.
func pickMemoryLimit(availPhys uint64, ok bool) int64 {
	if !ok || availPhys == 0 {
		return memLimitFallback
	}
	lim := availPhys / 2
	if lim < memLimitFloor {
		lim = memLimitFloor
	}
	return int64(lim) // availPhys/2 cannot exceed MaxInt64 on real hardware
}

// relaxedGCPercent is the GOGC the one-shot policy uses: collect at ~5x heap growth instead of
// Go's default 2x, so a short-lived run does fewer collections. Empirically ~9% faster geomean
// across the bench suite, with the memory limit preventing runaway growth. (Fully off — GOGC=-1
// — is measurably WORSE on garbage-heavy work; see the file comment.)
const relaxedGCPercent = 400

// ApplyStartupGCPolicy installs the one-shot GC/memory policy described above. It is a no-op if
// GOGC or GOMEMLIMIT is set in the environment. It returns the soft limit it set and whether it
// applied, for optional diagnostics.
func ApplyStartupGCPolicy() (limit int64, applied bool) {
	if os.Getenv("GOGC") != "" || os.Getenv("GOMEMLIMIT") != "" {
		return 0, false
	}
	limit = pickMemoryLimit(availablePhysBytes())
	debug.SetMemoryLimit(limit)          // OOM backstop, sized from free RAM
	debug.SetGCPercent(relaxedGCPercent) // collect less often — a short run doesn't need frugality
	return limit, true
}
