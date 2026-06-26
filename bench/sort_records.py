n = 100000
recs = []
seed = 999
for i in range(1, n + 1):
    seed = (seed * 1103515245 + 12345) % 2147483648
    recs.append({"id": i, "key": seed % 100000})
s = sorted(recs, key=lambda r: r["key"])
print(s[0]["key"] + s[n - 1]["key"])
