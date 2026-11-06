#!/usr/bin/env python3
"""Build the Rewards import list for Don't Starve Together's character skins (card openpak/prds#141).

Reads the game's own scripts (extract RomFS /scripts with base + update NSP, see docs/protocol.md 4a) and the RomFS file
listing, and writes the JSON bodies of POST /internal/v1/admin/games/dst/import (send in batches of 100):

    python3 gen_catalog.py <scripts dir> <romfs tree.txt> <out dir>

writes dst-import.json (skins), dst-packs-import.json (what the Shop sells) and dst-packs.json (pack -> items it gives)

A skin is a CreatePrefabSkin(...) entry of type "base" in prefabs/skinprefabs.lua; its display name is STRINGS.SKIN_NAMES in
skin_strings.lua. Only item keys and names are used, no art.

  * deliverable: the skin's build (anim/dynamic/<normal_skin>.zip) is in the installed game's RomFS, so the client can draw it.
  * rarity and origin from the game's rarity: Heirloom* -> legendary, Elegant -> epic, Complimentary -> common (sold for money
    in the original game: origin "store", priced in points); Event and Loyal -> event (earned, not sold); Character is the
    default skin every player owns and is left out.
"""
import json, re, sys

scripts, tree, outdir = sys.argv[1], sys.argv[2], sys.argv[3]
sk = open(f"{scripts}/prefabs/skinprefabs.lua", encoding="utf8", errors="replace").read()
names_src = open(f"{scripts}/skin_strings.lua", encoding="utf8", errors="replace").read()
i = names_src.index("STRINGS.SKIN_NAMES =")
j = names_src.index("STRINGS.SKIN_DESCRIPTIONS =")
names = dict(re.findall(r'^\s*([a-z0-9_]+)\s*=\s*"((?:[^"\\]|\\.)*)"', names_src[i:j], re.M))
files = set(l.split()[-1] for l in open(tree, encoding="utf8", errors="replace") if l.startswith("File"))

RARITY = {"HeirloomElegant": ("legendary", "earned"), "HeirloomDistinguished": ("legendary", "earned"),
          "HeirloomSpiffy": ("legendary", "earned"), "HeirloomClassy": ("legendary", "earned"),
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

# Packs: what the in-game Shop sells. MISC_ITEMS entries of type "purchase" (scripts/misc_items.lua); buying one gives its
# output_items. The Rewards catalogue sells the pack, so both shops are one catalogue.
misc = open(f"{scripts}/misc_items.lua", encoding="utf8", errors="replace").read()
prefab_rarity = {}
for key, body in re.findall(r'CreatePrefabSkin\("([a-z0-9_]+)",\s*\{(.*?)\n\}\)\)', sk, re.S):
    r = re.search(r'\brarity\s*=\s*"(\w+)"', body)
    if r:
        prefab_rarity[key] = r.group(1)
clothing = open(f"{scripts}/clothing.lua", encoding="utf8", errors="replace").read()
build_of = {k: m.group(1) for k, b in re.findall(r'^\t([a-z0-9_]+) =\n\t\{(.*?)\n\t\},', clothing, re.M | re.S)
            for m in [re.search(r'build_name_override = "([a-z0-9_]+)"', b)] if m}
restricted = {k for k, b in re.findall(r'CreatePrefabSkin\("([a-z0-9_]+)",\s*\{(.*?)\n\}\)\)', sk, re.S) if "is_restricted = true" in b}
base_of = {k: m.group(1) for k, b in re.findall(r'CreatePrefabSkin\("([a-z0-9_]+)",\s*\{(.*?)\n\}\)\)', sk, re.S)
           if re.search(r'\btype\s*=\s*"base"', b) for m in [re.search(r'base_prefab\s*=\s*"([a-z0-9_]+)"', b)] if m}
ORDER = ["legendary", "epic", "rare", "common"]
packs, pack_items = {}, []
for key, body in re.findall(r'^\t(pack_[a-z0-9_]+) =\n\t\{(.*?)\n\t\},', misc, re.M | re.S):
    if 'type = "purchase"' not in body:
        continue
    m = re.search(r'output_items = \{([^}]*)\}', body)
    outs = re.findall(r'"([a-z0-9_]+)"', m.group(1)) if m else []
    if not outs:
        continue
    tiers = [RARITY[prefab_rarity[o]][0] for o in outs if prefab_rarity.get(o) in RARITY]
    rarity = next((t for t in ORDER if t in tiers), "rare")
    skin_ok = {x["game_ref"]: x["deliverable"] for x in out}

    def has_build(k):  # clothing names its build (build_name_override); a woven variant ("...p") draws the base item's
        b = build_of.get(k, k)
        return f"/anim/dynamic/{b}.zip" in files or (b.endswith("p") and f"/anim/dynamic/{b[:-1]}.zip" in files)

    ok = all(skin_ok[o] if o in skin_ok else has_build(o) for o in outs if o in skin_ok or o.split("_")[0] in ("body", "hand", "legs", "feet"))
    packs[key] = outs
    # The game's own purchase rules (skinsutils.lua IsPackRestrictedDueToOwnership): a chest with a base skin of a restricted
    # character (is_restricted: Wanda, Wormwood, Wortox, Wurt) needs that character owned, unless the chest includes the character.
    included = {base_of[o] for o in outs if o in restricted}
    needs = sorted({base_of[o] + "_none" for o in outs if o in base_of and base_of[o] not in included and base_of[o] + "_none" in restricted})
    # Sold for money = shown in the in-game Shop (display_atlas). A chest with no shop picture is a gift for owning another
    # product (rarity Loyal: Hot Lava, Oxygen Not Included, Reign of Giants, Shipwrecked, Hamlet, PSN): earned, never sold here.
    sold = 'display_atlas' in body
    pack_items.append({"game_ref": key, "slot": "pack", "name": names.get(key) or key, "rarity": rarity, "origin": "store" if sold else "earned",
                       "deliverable": ok, "platforms": ["switch"], "grants": outs, "requires": needs})
pack_items.sort(key=lambda x: x["game_ref"])
json.dump({"catalog_rev": "dst-1.29.0", "items": out}, open(f"{outdir}/dst-import.json", "w"), indent=0)
json.dump({"catalog_rev": "dst-1.29.0", "items": pack_items}, open(f"{outdir}/dst-packs-import.json", "w"), indent=0)
json.dump(packs, open(f"{outdir}/dst-packs.json", "w"), indent=0, sort_keys=True)
print(f"skins {len(out)} deliverable {sum(1 for x in out if x['deliverable'])} skipped {skipped}; packs {len(pack_items)} deliverable {sum(1 for x in pack_items if x['deliverable'])}", file=sys.stderr)
