# raw VM micro-probes

Not part of the representative benchmark suite (see `../order.txt`). These are
CS micro-benchmarks kept only as raw-interpreter probes:

- `fib` — recursive-call overhead (register-mode function, deep recursion).
- `nested_loop` — tight nested integer loops.

They flatter tuned integer VMs (e.g. CPython's) and don't reflect real drang
workloads (log processing, orchestration, glue) — which is what the main suite
measures. Run manually, e.g. `drang raw/fib.dr` vs `python raw/fib.py`.
