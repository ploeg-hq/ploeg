#!/usr/bin/env python3
"""Import docs/backlog.md into Vikunja as grooming-ready tickets — no LLM involved.

Joins the numbered items in docs/backlog.md with scripts/backlog/metadata.tsv
(priority band, theme, impact, effort/time/uncertainty) and creates one task per
item via the Vikunja REST API, in grooming order (P0 first), with TipTap-safe
HTML descriptions and the board-convention labels:

  theme/<x> · impact/<H|M|L> · effort/<S|M|L> · time/<hours|days|weeks>
  · uncertainty/<low|med|high> · needs-refinement

Priority mapping: P0->5 P1->4 P2->3 P3->2 (Vikunja shows 4+ as urgent).

Usage:
  export VIKUNJA_URL=https://vikunja.example.com
  export VIKUNJA_TOKEN=tk_...          # API token with task/label write scope
  python3 scripts/backlog/import_backlog.py --project-id 42 --dry-run
  python3 scripts/backlog/import_backlog.py --project-id 42

Idempotency: existing task titles in the project are fetched first; items whose
"NNN ·" prefix already exists are skipped, so re-runs only create what's missing.

API notes: paths follow current Vikunja (projects-based). On old instances that
still use /lists, adjust TASKS_PATH. Label attach is one call per label; the
whole import is ~700 requests — --sleep throttles politely.
"""

import argparse
import html
import json
import os
import re
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

PRIORITY = {"P0": 5, "P1": 4, "P2": 3, "P3": 2}
IMPACT = {"H": "impact/H", "M": "impact/M", "L": "impact/L"}
ITEM_RE = re.compile(r"^(\d{1,3})\.\s+\*\*(.+?)\*\*\s*[—-]\s*(.*)$")


def api(method, path, token, base, body=None):
    url = base.rstrip("/") + "/api/v1" + path
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(url, data=data, method=method)
    req.add_header("Authorization", "Bearer " + token)
    req.add_header("Content-Type", "application/json")
    with urllib.request.urlopen(req) as resp:
        payload = resp.read()
    return json.loads(payload) if payload else None


def parse_backlog(path):
    items = {}
    current = None
    for raw in open(path, encoding="utf-8"):
        line = raw.rstrip("\n")
        m = ITEM_RE.match(line.strip())
        if m:
            num, title, text = int(m.group(1)), m.group(2), m.group(3)
            items[num] = {"title": title, "text": text}
            current = num
        elif current is not None and line.startswith("   ") and line.strip():
            items[current]["text"] += " " + line.strip()
        else:
            current = None
    return items


def parse_metadata(path):
    rows = []
    for raw in open(path, encoding="utf-8"):
        line = raw.strip()
        if not line or line.startswith("#"):
            continue
        item, band, theme, impact, effort, tim, unc = line.split("\t")
        rows.append(
            {
                "item": int(item),
                "band": band,
                "theme": theme,
                "impact": impact,
                "effort": effort,
                "time": tim,
                "uncertainty": unc,
            }
        )
    return rows


def md_inline_to_html(text):
    out = html.escape(text)
    out = re.sub(r"\*\[research(: [^\]]+)?\]\*", lambda m: "(research" + (m.group(1) or "") + ")", out)
    out = re.sub(r"\*\*(.+?)\*\*", r"<strong>\1</strong>", out)
    out = re.sub(r"`([^`]+)`", r"<code>\1</code>", out)
    return out


def build_description(meta, entry):
    code = " · ".join(
        [meta["band"], meta["impact"], meta["effort"], meta["time"], meta["uncertainty"]]
    )
    parts = [
        "<p><strong>%s</strong> — <code>[%s]</code></p>" % (meta["theme"], code),
        "<p><strong>Context</strong> — %s</p>" % md_inline_to_html(entry["text"]),
        "<p>Source: <code>docs/backlog.md</code> item %d. Grooming to DoR still needed: "
        "problem evidence, acceptance criteria, approach, verification, gates.</p>" % meta["item"],
    ]
    flags = []
    if meta["effort"] == "L":
        flags.append("effort L — name the first shippable slice or split before <code>ready</code>.")
    if meta["uncertainty"] == "high":
        flags.append("uncertainty high — needs a spike or explicit de-risk step; never <code>agent-ready</code> as-is.")
    if flags:
        parts.append("<p><em>Grooming flags:</em> " + " ".join(flags) + "</p>")
    return "".join(parts)


def labels_for(meta):
    return [
        "theme/" + meta["theme"],
        IMPACT[meta["impact"]],
        "effort/" + meta["effort"],
        "time/" + meta["time"],
        "uncertainty/" + meta["uncertainty"],
        "needs-refinement",
    ]


def fetch_all(path_tmpl, token, base):
    results, page = [], 1
    while True:
        batch = api("GET", path_tmpl % page, token, base) or []
        results.extend(batch)
        if len(batch) < 50:
            return results
        page += 1


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--project-id", type=int, required=True)
    ap.add_argument("--backlog", default="docs/backlog.md")
    ap.add_argument("--metadata", default="scripts/backlog/metadata.tsv")
    ap.add_argument("--dry-run", action="store_true")
    ap.add_argument("--limit", type=int, default=0, help="create only the first N (0 = all)")
    ap.add_argument("--sleep", type=float, default=0.05)
    args = ap.parse_args()

    entries = parse_backlog(args.backlog)
    rows = parse_metadata(args.metadata)

    missing = [r["item"] for r in rows if r["item"] not in entries]
    dupes = len(rows) != len({r["item"] for r in rows})
    if missing or dupes:
        sys.exit("metadata/backlog mismatch: missing items %s, dupes=%s" % (missing, dupes))
    if args.limit:
        rows = rows[: args.limit]

    if args.dry_run:
        for rank, meta in enumerate(rows, 1):
            e = entries[meta["item"]]
            print(
                "%3d  %-3s prio=%d  %03d · %s  [%s]"
                % (rank, meta["band"], PRIORITY[meta["band"]], meta["item"], e["title"], ", ".join(labels_for(meta)))
            )
        print("\n%d tasks would be created." % len(rows))
        return

    base = os.environ.get("VIKUNJA_URL") or sys.exit("set VIKUNJA_URL")
    token = os.environ.get("VIKUNJA_TOKEN") or sys.exit("set VIKUNJA_TOKEN")

    label_ids = {}
    for lbl in fetch_all("/labels?page=%d", token, base):
        label_ids[lbl["title"]] = lbl["id"]
    needed = sorted({l for meta in rows for l in labels_for(meta)})
    for title in needed:
        if title not in label_ids:
            created = api("PUT", "/labels", token, base, {"title": title})
            label_ids[title] = created["id"]
            print("created label %s (id %d)" % (title, created["id"]))
            time.sleep(args.sleep)

    existing = set()
    for t in fetch_all("/projects/" + str(args.project_id) + "/tasks?page=%d", token, base):
        m = re.match(r"^(\d{3}) ·", t.get("title", ""))
        if m:
            existing.add(int(m.group(1)))

    made = 0
    for meta in rows:
        n = meta["item"]
        if n in existing:
            print("skip %03d (already on board)" % n)
            continue
        e = entries[n]
        task = api(
            "PUT",
            "/projects/%d/tasks" % args.project_id,
            token,
            base,
            {
                "title": "%03d · %s" % (n, e["title"]),
                "description": build_description(meta, e),
                "priority": PRIORITY[meta["band"]],
            },
        )
        for lbl in labels_for(meta):
            api("PUT", "/tasks/%d/labels" % task["id"], token, base, {"label_id": label_ids[lbl]})
            time.sleep(args.sleep)
        made += 1
        print("created %03d · %s (task id %d)" % (n, e["title"], task["id"]))
        time.sleep(args.sleep)

    print("\ndone: %d created, %d skipped as existing." % (made, len(existing & {r['item'] for r in rows})))


if __name__ == "__main__":
    try:
        main()
    except urllib.error.HTTPError as err:
        sys.exit("HTTP %d on %s: %s" % (err.code, err.url, err.read().decode(errors="replace")[:300]))
