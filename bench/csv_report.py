# CSV group-by report: generate N CSV rows, parse, group-by category summing amount.
cats = ["A", "B", "C", "D", "E", "F"]
n = 200000
seed = 12345
groups = {}
for c in cats:
    groups[c] = 0
for i in range(1, n + 1):
    seed = (seed * 1103515245 + 12345) % 2147483648
    cat = cats[(seed // 256) % 6]
    seed = (seed * 1103515245 + 12345) % 2147483648
    amount = seed % 1000
    row = cat + "," + str(amount)
    parts = row.split(",")
    k = parts[0]
    v = int(parts[1])
    groups[k] = groups[k] + v
for c in cats:
    print(c, groups[c])
