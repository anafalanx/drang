from functools import reduce
a = list(range(1, 500001))
r = reduce(lambda acc, x: acc + x, filter(lambda x: x % 3 == 0, map(lambda x: x * 2, a)), 0)
print(r)
