# json_transform: build N record maps, JSON serialize -> parse -> transform.
# Keep records with score >= 500, sum (score + id) from the PARSED ints.
import json

names = ["alice", "bob", "carol", "dave"]
n = 120000
seed = 12345
recs = []
for i in range(1, n + 1):
    seed = (seed * 1103515245 + 12345) % 2147483648
    name = names[seed % 4]
    seed = (seed * 1103515245 + 12345) % 2147483648
    score = seed % 1000
    recs.append({"id": i, "name": name, "score": score})

s = json.dumps(recs)
back = json.loads(s)
total = 0
for r in back:
    if r["score"] >= 500:
        total = total + r["score"] + r["id"]
print(total)
