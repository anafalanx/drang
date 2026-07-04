# str_template: report formatting / string generation benchmark
names = ["alice", "bob", "carol", "dave", "eve"]
n = 200000
seed = 12345
total = 0
for i in range(1, n + 1):
    seed = (seed * 1103515245 + 12345) % 2147483648
    name = names[seed % 5]
    seed = (seed * 1103515245 + 12345) % 2147483648
    amount = seed % 10000
    line = f"#{i} {name}: ${amount}.00"
    total = total + len(line)
print(total)
