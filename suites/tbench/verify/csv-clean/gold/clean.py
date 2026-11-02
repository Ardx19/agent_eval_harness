import csv
seen, rows = set(), []
for r in csv.DictReader(open("people.csv")):
    r = {k: v.strip() for k, v in r.items()}
    if not r["age"]:
        continue
    key = tuple(r.values())
    if key in seen:
        continue
    seen.add(key)
    rows.append(r)
rows.sort(key=lambda r: r["name"])
with open("people_clean.csv", "w", newline="") as f:
    w = csv.DictWriter(f, fieldnames=["name", "age", "city"], lineterminator="\n")
    w.writeheader()
    w.writerows(rows)
