funcs = []
for i in range(1, 100001):
    funcs.append(lambda x, i=i: x + i)
s = 0
for f in funcs:
    s += f(10)
print(s)
