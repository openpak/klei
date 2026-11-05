#!/usr/bin/env python3
"""Build the Rewards import list for Don't Starve Together's character skins (card openpak/prds#141).

Reads the game's own scripts (extract RomFS /scripts with base + update NSP, see docs/protocol.md 4a) and the RomFS file
listing, and writes the JSON body of POST /internal/v1/admin/games/dst/import:

    python3 gen_catalog.py <scripts dir> <romfs tree.txt> > dst-import.json

A skin is a CreatePrefabSkin(...) entry of type "base" in prefabs/skinprefabs.lua; its display name is STRINGS.SKIN_NAMES in
skin_strings.lua. Only item keys and names are used, no art.

  * deliverable: the skin's build (anim/dynamic/<normal_skin>.zip) is in the installed game's RomFS, so the client can draw it.
  * rarity and origin from the game's rarity: Heirloom* -> legendary, Elegant -> epic, Complimentary -> common (sold for money
    in the original game: origin "store", priced in points); Event and Loyal -> event (earned, not sold); Character is the
    default skin every player owns and is left out.
"""
import json, re, sys

scripts, tree = sys.argv[1], sys.argv[2]
sk = open(f"{scripts}/prefabs/skinprefabs.lua", encoding="utf8", errors="replace").read()
names_src = open(f"{scripts}/skin_strings.lua", encoding="utf8", errors="replace").read()
i = names_src.index("STRINGS.SKIN_NAMES =")
j = names_src.index("STRINGS.SKIN_DESCRIPTIONS =")
names = dict(re.findall(r'^\s*([a-z0-9_]+)\s*=\s*"((?:[^"\\]|\\.)*)"', names_src[i:j], re.M))
files = set(l.split()[-1] for l in open(tree, encoding="utf8", errors="replace") if l.startswith("File"))

RARITY = {"HeirloomElegant": ("legendary", "store"), "HeirloomDistinguished": ("legendary", "store"),
          "HeirloomSpiffy": ("legendary", "store"), "HeirloomClassy": ("legendary", "store"),
          "Elegant": ("epic", "store"), "Distinguished": ("epic", "store"), "Spiffy": ("rare", "store"), "Classy": ("rare", "store"),
          "Complimentary": ("common", "promo"), "Common": ("common", "store"),
          "Event": ("event", "event"), "Loyal": ("event", "earned"), "Reward": ("event", "earned"),
          "Timeless": ("event", "earned"), "ProofOfPurchase": ("event", "earned")}
out, skipped = [], {}
for key, body in re.findall(r'CreatePrefabSkin\("([a-z0-9_]+)",\s*\{(.*?)\n\}\)\)', sk, re.S):
    t = re.search(r'\btype\s*=\s*"(\w+)"', body)
    if not t or t.group(1) != "base":
        continue
    r = re.search(r'\brarity\s*=\s*"(\w+)"', body)
    rarity = r.group(1) if r else ""
    if rarity == "Character" or rarity not in RARITY:
        skipped[rarity or "none"] = skipped.get(rarity or "none", 0) + 1
        continue
    normal = re.search(r'normal_skin\s*=\s*"([a-z0-9_]+)"', body)
    build = normal.group(1) if normal else key
    mod = re.search(r'rarity_modifier\s*=\s*"(\w+)"', body)
    name = names.get(key) or key
    if mod:
        name += f" ({mod.group(1)})"   # the woven variant shares the base skin's name
    base = re.search(r'base_prefab\s*=\s*"([a-z0-9_]+)"', body)
    who = base.group(1).capitalize() if base else ""
    r_op, origin = RARITY[rarity]
    out.append({"game_ref": key, "slot": "skin", "name": f"{who}: {name}" if who else name, "rarity": r_op, "origin": origin,
                "deliverable": f"/anim/dynamic/{build}.zip" in files, "platforms": ["switch"]})
out.sort(key=lambda x: x["game_ref"])
json.dump({"catalog_rev": "dst-1.29.0", "items": out}, sys.stdout, indent=0)
print(f"skins {len(out)} deliverable {sum(1 for x in out if x['deliverable'])} skipped {skipped}", file=sys.stderr)
