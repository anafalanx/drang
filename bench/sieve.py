n = 1000000
sieve = [True] * (n + 1)
count = 0
for i in range(2, n + 1):
    if sieve[i]:
        count += 1
        j = i * i
        while j <= n:
            sieve[j] = False
            j += i
print(count)
