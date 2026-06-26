"""drang vs Python benchmark harness.

Runs each bench/<name>.dr and bench/<name>.py as a subprocess, checks they print
the same result (equivalence), and reports the min wall-clock over N repeats.

Usage:  python run.py [repeats]   (default 3)
"""
import subprocess, time, sys, os, math, statistics

HERE = os.path.dirname(os.path.abspath(__file__))
DRANG = r"C:\zmal\_drang\drang.exe"
PY = sys.executable
REPEAT = int(sys.argv[1]) if len(sys.argv) > 1 else 3
TIMEOUT = 300


def run(cmd):
    t0 = time.perf_counter()
    p = subprocess.run(cmd, capture_output=True, text=True, timeout=TIMEOUT)
    return (time.perf_counter() - t0) * 1000.0, p.stdout.strip(), p.stderr.strip(), p.returncode


def best(cmd):
    ms = out = err = None
    rc = 0
    for _ in range(REPEAT):
        m, o, e, r = run(cmd)
        ms = m if ms is None else min(ms, m)
        out, err, rc = o, e, r
    return ms, out, err, rc


def main():
    names = open(os.path.join(HERE, "order.txt")).read().split()
    l3_base = best([DRANG, os.path.join(HERE, "_empty.dr")])[0]
    py_base = best([PY, os.path.join(HERE, "_empty.py")])[0]

    print(f"drang : {DRANG}")
    print(f"python: {PY}")
    print(f"repeat={REPEAT} (min wall-clock ms, lower=faster), timeout={TIMEOUT}s")
    print(f"startup baseline: drang {l3_base:.1f} ms | python {py_base:.1f} ms\n")
    hdr = f"{'benchmark':<14}{'drang ms':>11}{'python ms':>11}{'ratio':>8}  status"
    print(hdr)
    print("-" * (len(hdr) + 6))

    rows = []
    for name in names:
        l3ms, l3out, l3err, l3rc = best([DRANG, os.path.join(HERE, name + ".dr")])
        pyms, pyout, pyerr, pyrc = best([PY, os.path.join(HERE, name + ".py")])
        match = l3out == pyout and l3rc == 0 and pyrc == 0
        ratio = l3ms / pyms if pyms else 0.0
        status = "ok" if match else "MISMATCH"
        line = f"{name:<14}{l3ms:>11.1f}{pyms:>11.1f}{ratio:>7.1f}x  {status}"
        if not match:
            line += f"\n   l3={l3out!r} rc{l3rc} | py={pyout!r} rc{pyrc}"
            if l3err:
                line += f"\n   l3err: {l3err[:160]}"
            if pyerr:
                line += f"\n   pyerr: {pyerr[:160]}"
        print(line)
        rows.append((name, l3ms, pyms, ratio, match))

    oks = [r for r in rows if r[4]]
    if oks:
        ratios = [r[3] for r in oks]
        geo = math.prod(ratios) ** (1.0 / len(ratios))
        print(f"\n{len(oks)}/{len(rows)} matched.  drang is this many times Python's wall-clock:")
        print(f"  min {min(ratios):.1f}x   median {statistics.median(ratios):.1f}x   "
              f"geomean {geo:.1f}x   max {max(ratios):.1f}x")
    if len(oks) != len(rows):
        print(f"\n{len(rows) - len(oks)} MISMATCH(es) — fix before trusting timings.")


main()
