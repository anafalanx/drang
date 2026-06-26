"""Parallelism showdown: lang3 pmap vs Python (sequential / threads / processes).

CPU-bound Mandelbrot over independent rows. lang3 parallelizes with one word
(map -> pmap). Python's natural threading is GIL-bound (no speedup for CPU work);
real parallelism needs multiprocessing (separate processes + pickling).

Usage: python run.py [repeats]   (default 3)
"""
import subprocess, time, sys, os

HERE = os.path.dirname(os.path.abspath(__file__))
LANG3 = r"C:\zmal\_lang3\lang3.exe"
PY = sys.executable
REPEAT = int(sys.argv[1]) if len(sys.argv) > 1 else 3
TIMEOUT = 600

CONFIGS = [
    ("lang3 seq (map)",    [LANG3, os.path.join(HERE, "mandel_seq.l3")]),
    ("lang3 par (pmap)",   [LANG3, os.path.join(HERE, "mandel_par.l3")]),
    ("python seq",         [PY, os.path.join(HERE, "mandel.py"), "seq"]),
    ("python threads",     [PY, os.path.join(HERE, "mandel.py"), "threads"]),
    ("python mp",          [PY, os.path.join(HERE, "mandel.py"), "mp"]),
]


def best(cmd):
    ms = out = None
    rc = 0
    for _ in range(REPEAT):
        t0 = time.perf_counter()
        p = subprocess.run(cmd, capture_output=True, text=True, timeout=TIMEOUT)
        m = (time.perf_counter() - t0) * 1000.0
        ms = m if ms is None else min(ms, m)
        out, rc = p.stdout.strip(), p.returncode
    return ms, out, rc


def main():
    print(f"cores: {os.cpu_count()}   repeat={REPEAT} (min wall-clock ms)")
    print(f"lang3 : {LANG3}")
    print(f"python: {PY}\n")
    res = {}
    checks = set()
    hdr = f"{'config':<20}{'ms':>10}{'vs py-seq':>11}"
    print(hdr)
    print("-" * len(hdr))
    for label, cmd in CONFIGS:
        ms, out, rc = best(cmd)
        res[label] = ms
        checks.add(out if rc == 0 else f"ERR rc{rc}")
        print(f"{label:<20}{ms:>10.1f}", end="")
        if "python seq" in res:
            print(f"{ms / res['python seq']:>10.2f}x")
        else:
            print(f"{'—':>11}")

    print()
    if len(checks) == 1:
        print(f"all 5 produced the same checksum: {checks.pop()}")
    else:
        print(f"!! CHECKSUM MISMATCH across configs: {checks}")

    def sp(a, b):
        return res[a] / res[b] if res.get(b) else 0.0

    print("\nspeedup from going parallel (higher = better):")
    print(f"  lang3   map -> pmap        : {sp('lang3 seq (map)', 'lang3 par (pmap)'):.1f}x")
    print(f"  python  seq -> threads     : {sp('python seq', 'python threads'):.1f}x   (GIL: ~1x expected)")
    print(f"  python  seq -> processes   : {sp('python seq', 'python mp'):.1f}x")
    print("\nlang3 pmap vs Python, wall-clock (lower lang3 = better):")
    print(f"  vs python seq     : {sp('lang3 par (pmap)', 'python seq'):.2f}x")
    print(f"  vs python threads : {sp('lang3 par (pmap)', 'python threads'):.2f}x")
    print(f"  vs python mp      : {sp('lang3 par (pmap)', 'python mp'):.2f}x")


main()
