words = "apple banana cherry date apple fig banana apple grape cherry".split()
counts = {}
total = 200000
for i in range(total):
    w = words[i % len(words)]
    counts[w] = counts.get(w, 0) + 1
print(len(counts), sum(counts.values()))
