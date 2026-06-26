max_steps = 0
for start in range(1, 100001):
    x = start
    steps = 0
    while x > 1:
        if x % 2 == 0:
            x = x // 2
        else:
            x = 3 * x + 1
        steps += 1
    if steps > max_steps:
        max_steps = steps
print(max_steps)
