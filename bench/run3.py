"""Three-way benchmark harness: two drang binaries vs Python.

Runs each bench/<name>.dr on drang A and drang B, and bench/<name>.py on Python,
checks all three print identical output (an equivalence gate), and reports the min
wall-clock over N repeats with ratios. Handy for comparing two drang versions
(e.g. a released binary vs a build under test) against Python in one pass.

Usage:   python run3.py <drangA> <drangB> [repeats]
         (Python is whatever interpreter runs this script; default repeats = 5.)
Example: python run3.py drang_0.7.exe ../drang.exe 5
         -> A = 0.7, B = current build; each min-of-5, with B-vs-A speedup.

For a plain drang-vs-Python run, use run.py instead.
"""
import subprocess, time, sys, os, math, statistics

HERE = os.path.dirname(os.path.abspath(__file__))
if len(sys.argv) < 3:
    sys.exit("usage: python run3.py <drangA> <drangB> [repeats]")
DRA = os.path.abspath(sys.argv[1])
DRB = os.path.abspath(sys.argv[2])
PY = sys.executable
REPEAT = int(sys.argv[3]) if len(sys.argv) > 3 else 5
TIMEOUT = 300


def norm(s):
    # Line-ending agnostic: Windows CPython prints CRLF, drang prints LF.
    return s.replace("\r\n", "\n").replace("\r", "\n").strip()


def run(cmd):
    t0 = time.perf_counter()
    p = subprocess.run(cmd, capture_output=True, text=True, timeout=TIMEOUT)
    return (time.perf_counter() - t0) * 1000.0, norm(p.stdout), p.returncode


def best(cmd):
    ms = out = None
    rc = 0
    for _ in range(REPEAT):
        m, o, r = run(cmd)
        ms = m if ms is None else min(ms, m)
        out, rc = o, r
    return ms, out, rc


def main():
    names = open(os.path.join(HERE, "order.txt")).read().split()
    print(f"A      = {DRA}")
    print(f"B      = {DRB}")
    print(f"python = {PY}")
    print(f"repeat={REPEAT} (min wall-clock ms, lower=faster), timeout={TIMEOUT}s\n")
    print(f"{'benchmark':<15}{'A ms':>9}{'B ms':>9}{'py ms':>9}{'B:py':>8}{'A:py':>8}{'B/A':>8}  status")
    print("-" * 80)
    rows = []
    for n in names:
        ma, oa, ra = best([DRA, os.path.join(HERE, n + ".dr")])
        mb, ob, rb = best([DRB, os.path.join(HERE, n + ".dr")])
        mp, op, rp = best([PY, os.path.join(HERE, n + ".py")])
        match = (oa == ob == op) and ra == 0 and rb == 0 and rp == 0
        bp = mb / mp if mp else 0.0
        ap = ma / mp if mp else 0.0
        spd = ma / mb if mb else 0.0
        print(f"{n:<15}{ma:>9.1f}{mb:>9.1f}{mp:>9.1f}{bp:>7.2f}x{ap:>7.2f}x{spd:>7.2f}x  {'ok' if match else 'MISMATCH'}")
        if not match:
            print(f"   A={oa[:50]!r} B={ob[:50]!r} py={op[:50]!r}  rc {ra}/{rb}/{rp}")
        rows.append((n, ma, mb, mp, bp, ap, spd, match))

    oks = [r for r in rows if r[7]]
    if oks:
        b = [r[4] for r in oks]
        a = [r[5] for r in oks]
        s = [r[6] for r in oks]
        g = lambda x: math.prod(x) ** (1.0 / len(x))
        print(f"\n{len(oks)}/{len(rows)} matched (A == B == python output).")
        print(f"B vs Python:    min {min(b):.2f}x  median {statistics.median(b):.2f}x  geomean {g(b):.2f}x  max {max(b):.2f}x")
        print(f"A vs Python:    min {min(a):.2f}x  median {statistics.median(a):.2f}x  geomean {g(a):.2f}x  max {max(a):.2f}x")
        print(f"B speedup vs A: min {min(s):.2f}x  median {statistics.median(s):.2f}x  geomean {g(s):.2f}x  max {max(s):.2f}x")
    if len(oks) != len(rows):
        print(f"\n{len(rows) - len(oks)} MISMATCH(es) — fix before trusting timings.")


main()
