"""Mandelbrot escape-iteration sum in Python, three ways:
    python mandel.py seq      sequential
    python mandel.py threads  ThreadPoolExecutor (GIL-bound: ~no speedup for CPU work)
    python mandel.py mp       ProcessPoolExecutor (real parallelism, process overhead)
Same arithmetic and constants as the drang versions, so the checksum matches.
"""
import sys
from concurrent.futures import ThreadPoolExecutor, ProcessPoolExecutor

W = 700
H = 500
MAXITER = 200
XMIN = -2.5
XMAX = 1.0
YMIN = -1.25
YMAX = 1.25


def compute_row(py):
    y0 = YMIN + (YMAX - YMIN) * py / (H - 1)
    rowsum = 0
    for px in range(W):
        x0 = XMIN + (XMAX - XMIN) * px / (W - 1)
        zx = 0.0
        zy = 0.0
        it = 0
        while it < MAXITER and (zx * zx + zy * zy) <= 4.0:
            xt = zx * zx - zy * zy + x0
            zy = 2.0 * zx * zy + y0
            zx = xt
            it += 1
        rowsum += it
    return rowsum


def main():
    mode = sys.argv[1] if len(sys.argv) > 1 else "seq"
    rows = list(range(H))
    if mode == "seq":
        sums = [compute_row(py) for py in rows]
    elif mode == "threads":
        with ThreadPoolExecutor() as ex:
            sums = list(ex.map(compute_row, rows))
    elif mode == "mp":
        with ProcessPoolExecutor() as ex:
            sums = list(ex.map(compute_row, rows, chunksize=8))
    else:
        raise SystemExit("mode must be seq|threads|mp")
    print(sum(sums))


if __name__ == "__main__":
    main()
