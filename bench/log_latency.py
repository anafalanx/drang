svcs = ["auth", "db", "api", "cache"]
n = 200000
seed = 12345
counts = {}
sums = {}
for s in svcs:
    counts[s] = 0
    sums[s] = 0
for i in range(1, n + 1):
    seed = (seed * 1103515245 + 12345) % 2147483648
    svc = svcs[seed % 4]
    latency = seed % 2000
    line = "service=" + svc + " latency=" + str(latency) + "ms"
    parts = line.split(" ")
    svc2 = parts[0].split("=")[1]
    latstr = parts[1].split("=")[1]
    latnum = int(latstr.replace("ms", ""))
    counts[svc2] = counts[svc2] + 1
    sums[svc2] = sums[svc2] + latnum
for s in svcs:
    print(s, counts[s], sums[s])
