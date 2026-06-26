import re
rx = re.compile(r"\d{3,}")
count = 0
for i in range(1, 200001):
    line = "item-" + str(i % 1000) + "-end"
    if rx.search(line):
        count += 1
print(count)
