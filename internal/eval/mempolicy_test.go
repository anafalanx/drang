package eval

import "testing"

// TestAvailPhysSanity guards the MEMORYSTATUSEX struct layout / syscall: a wrong field layout
// would silently yield a garbage availPhys (which the limit math would then hide). A correct
// call returns a plausible free-RAM figure, so we assert it succeeds and lands in a sane band.
func TestAvailPhysSanity(t *testing.T) {
	got, ok := availablePhysBytes()
	if !ok {
		t.Fatal("GlobalMemoryStatusEx failed")
	}
	const GiB = 1 << 30
	t.Logf("availablePhysBytes = %d bytes (%.1f GiB)", got, float64(got)/float64(GiB))
	if got < 64*(1<<20) || got > 8*1024*GiB { // 64 MiB .. 8 TiB — anything outside means a bad layout
		t.Errorf("availablePhysBytes = %d, implausible (likely a struct-layout bug)", got)
	}
}

func TestPickMemoryLimit(t *testing.T) {
	const GiB = 1 << 30
	cases := []struct {
		name  string
		avail uint64
		ok    bool
		want  int64
	}{
		{"query-failed", 0, false, memLimitFallback},
		{"zero-avail", 0, true, memLimitFallback},
		{"roomy-64G", 64 * GiB, true, 32 * GiB},          // half of available
		{"mid-16G", 16 * GiB, true, 8 * GiB},             // half
		{"tiny-floored", 300 << 20, true, memLimitFloor}, // 150 MiB half -> floored to 256 MiB
		{"just-above-floor", 600 << 20, true, 300 << 20}, // 300 MiB half, above the floor
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := pickMemoryLimit(c.avail, c.ok); got != c.want {
				t.Errorf("pickMemoryLimit(%d, %v) = %d, want %d", c.avail, c.ok, got, c.want)
			}
		})
	}
}
