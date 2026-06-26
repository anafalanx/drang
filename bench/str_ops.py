line = "alpha,beta,gamma,delta,epsilon,zeta"
total = 0
for i in range(100000):
    parts = line.split(",")
    joined = "-".join(p.upper() for p in parts)
    total += len(joined)
print(total)
