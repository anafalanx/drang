n = 100000
m = {}
for i in range(1, n + 1):
    m["k" + str(i)] = i
s = 0
seed = 7
for q in range(n):
    seed = (seed * 1103515245 + 12345) % 2147483648
    key = "k" + str((seed % n) + 1)
    if key in m:
        s += m[key]
print(s)
