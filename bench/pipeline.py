# Functional data pipeline: generate N ints via LCG,
# then filter (x%3==0) -> map (x*x %1000000) -> sum.
n = 800000
data = []
seed = 12345
for i in range(1, n + 1):
    seed = (seed * 1103515245 + 12345) % 2147483648
    data.append(seed % 1000000)
kept = [x for x in data if x % 3 == 0]
squared = [(x * x) % 1000000 for x in kept]
total = sum(squared)
print(total)
