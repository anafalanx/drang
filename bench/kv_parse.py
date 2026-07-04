n = 200000
seed = 12345
total = 0
for i in range(1, n + 1):
    seed = (seed * 1103515245 + 12345) % 2147483648
    a = seed % 1000
    seed = (seed * 1103515245 + 12345) % 2147483648
    b = seed % 1000
    seed = (seed * 1103515245 + 12345) % 2147483648
    c = seed % 1000
    line = "k1=" + str(a) + " k2=" + str(b) + " k3=" + str(c)
    tokens = line.split(" ")
    m = {}
    for tok in tokens:
        kv = tok.split("=")
        m[kv[0]] = kv[1]
    total = total + int(m["k2"])
print(total)
