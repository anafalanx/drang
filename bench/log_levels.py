# LOG-LEVEL COUNTING: build a log line, parse it back, count levels per map.
levels = ["INFO", "WARN", "ERROR", "DEBUG"]
svcs = ["auth", "db", "api", "cache"]
n = 300000
seed = 12345
counts = {}
for L in levels:
    counts[L] = 0
for i in range(1, n + 1):
    seed = (seed * 1103515245 + 12345) % 2147483648
    level = levels[(seed // 256) % 4]
    seed = (seed * 1103515245 + 12345) % 2147483648
    svc = svcs[(seed // 256) % 4]
    line = level + " service=" + svc + " msg=ok"
    parts = line.split(" ")
    got = parts[0]
    counts[got] = counts[got] + 1
total = 0
for L in levels:
    total = total + counts[L]
print(counts["INFO"], counts["WARN"], counts["ERROR"], counts["DEBUG"], total)
