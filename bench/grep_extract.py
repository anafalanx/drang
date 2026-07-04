import re

levels = ["INFO", "WARN", "ERROR", "DEBUG"]
rx = re.compile(r"^ERROR code=(\d+)")
seed = 123456789
matchCount = 0
codeSum = 0
for i in range(1, 300001):
    seed = (seed * 1103515245 + 12345) % 2147483648
    level = levels[seed % 4]
    code = seed % 1000
    line = level + " code=" + str(code)
    m = rx.match(line)
    if m:
        codeSum += int(m.group(1))
        matchCount += 1
print(matchCount, codeSum)
