import json


def merge(a, b):
    r = dict(a)
    for k, v in b.items():
        r[k] = merge(a[k], v) if isinstance(v, dict) and isinstance(a.get(k), dict) else v
    return r


out = {}
for name in ("defaults.json", "prod.json", "local.json"):
    out = merge(out, json.load(open(name)))
json.dump(out, open("effective.json", "w"), indent=2)
