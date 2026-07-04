"""Parallel CPU fan-out: for each of N items compute a heavy per-item checksum
by iterating an LCG K times, then sum all per-item checksums (order-independent).

Python's GIL forces PROCESSES for CPU parallelism, so this uses
multiprocessing.Pool(processes=os.cpu_count()).map with a top-level worker and
the __main__ guard (required on Windows). The checksum sum matches the drang
pmap (goroutine) version exactly.
"""
import os
from multiprocessing import Pool

N = 4000
K = 15000


def worker(i):
    acc = i
    for j in range(K):
        acc = (acc * 1103515245 + j) % 2147483648
    return acc


def main():
    items = list(range(N))
    with Pool(processes=os.cpu_count()) as pool:
        sums = pool.map(worker, items)
    print(sum(sums))


if __name__ == "__main__":
    main()
