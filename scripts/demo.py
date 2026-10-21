#!/usr/bin/env python3
"""Run real CLI subprocesses against the fictional supplier and save a demo."""

import argparse
import json
import os
from pathlib import Path
import shutil
import socket
import subprocess
import tempfile
import time
from urllib.error import URLError
from urllib.request import urlopen

ROOT = Path(__file__).resolve().parents[1]


def summary(run):
    c = run["counts"]
    return [
        f'status={run["status"]}  pages={c["pages"]}  next_cursor={run["next_cursor"]!r}',
        f'added={c["added"]}  updated={c["updated"]}  withdrawn={c["withdrawn"]}',
        f'duplicate={c["duplicates"]}  stale={c["stale"]}  rejected={c["rejected"]}',
        f'open_issues={run["open_issues"]}  attempts={run["attempts"]}',
    ]


def product_lines(products):
    return [
        f'{p["sku"]:6} v{p["version"]:<3} '
        + ("WITHDRAWN (tombstone retained)" if p["withdrawn"] else f'{p["name"]} | {p["price_cents"]} {p["currency"]} cents')
        for p in products
    ]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", default="bin/catalog.exe" if os.name == "nt" else "bin/catalog")
    parser.add_argument("--go", default=shutil.which("go"), help="Go executable for the final verification")
    args = parser.parse_args()
    binary = Path(args.binary).resolve()
    if not binary.is_file():
        parser.error("Build the Go binary first; see README.md")
    (ROOT / ".demo-work").mkdir(exist_ok=True)
    captures, scenes = [], []

    def scene(title, caption, lines, narration):
        scenes.append(dict(title=title, caption=caption, lines=lines, narration=narration, seconds=20))
        print("\n" + title + "\n" + caption)
        print("\n".join(lines))

    with tempfile.TemporaryDirectory(prefix="session-", dir=ROOT / ".demo-work") as work:
        work = Path(work)
        with socket.socket() as listener:
            listener.bind(("127.0.0.1", 0))
            port = listener.getsockname()[1]
        source = f"http://127.0.0.1:{port}"
        db = work / "catalog.db"
        supplier_cmd = [str(binary), "supplier", "--addr", f"127.0.0.1:{port}", "--fail-update", "delta", "--fail-cursor", "1", "--fail-count", "1"]
        with (work / "supplier.log").open("w", encoding="utf-8") as log:
            server = subprocess.Popen(supplier_cmd, cwd=ROOT, stdout=log, stderr=log)
            try:
                deadline = time.monotonic() + 15
                while True:
                    try:
                        with urlopen(source + "/healthz", timeout=1) as response:
                            if response.status == 200:
                                break
                    except (URLError, OSError):
                        pass
                    if server.poll() is not None or time.monotonic() > deadline:
                        raise RuntimeError("Fictional supplier failed to start")
                    time.sleep(0.05)

                def call(*options, expected=0, database=None):
                    cmd = [str(binary), *options, "--db", str(database or db)]
                    if options[0] == "start":
                        cmd += ["--source", source]
                    result = subprocess.run(cmd, cwd=ROOT, capture_output=True, text=True, encoding="utf-8", timeout=30)
                    if result.returncode != expected:
                        raise AssertionError(f"{cmd}: expected exit {expected}, got {result.returncode}\n{result.stdout}\n{result.stderr}")
                    payload = json.loads(result.stdout)
                    captures.append(dict(command=[p.replace(str(work), "$DEMO_DIR") for p in cmd], exit_code=result.returncode, output=payload, stderr=result.stderr))
                    return payload

                scene("Supplier catalog updates", "A local shop catalog. Fictional Northstar data. One Go binary.", [
                    "$ catalog help", "", "start / resume     Import batches without losing progress", "status             Is it complete? What failed?", "changes            Before-and-after product history", "issues             Rejected data and resolution history", "products --all     Active products and withdrawal tombstones", "", "Go + embedded bbolt database; no external database service.", "Each command below runs in a separate application process.",
                ], "This is a supplier catalog updater written in Go. Staff can start updates, check their progress, review changes, and investigate data problems. The demonstration uses a fictional Northstar supplier. One binary runs the importer or the mock supplier, and an embedded database keeps the local catalog and recovery checkpoints.")

                base = call("start", "--update", "baseline")
                assert base["status"] == "completed" and base["counts"]["added"] == 3
                products = call("products", "--all")
                scene("01 / Start with an existing catalog", "The baseline adds three products across two batches.", ["$ catalog start --update baseline", *summary(base), "", "$ catalog products --all", *product_lines(products)], "First, we import the baseline. It contains three products delivered in two batches: a notebook, a lamp, and a mug. The run reports completed only after the supplier explicitly ends the feed. Its counts and product list are persisted, so later supplier failures will not erase this existing catalog.")

                delta = call("start", "--update", "delta", expected=1)
                delta_id = delta["id"]
                assert delta["status"] == "blocked" and delta["counts"]["pages"] == 1 and delta["next_cursor"] == "1"
                scene("02 / A supplier outage mid-update", "Batch 0 commits; batch 1 returns a deliberate HTTP 503.", ["$ catalog start --update delta", *summary(delta), "", f'last_error: {delta["last_error"]}', "", "exit code: 1", "The run is blocked; it has not been reported as complete."], "The next update changes the mug and withdraws the lamp. It also repeats one record and sends an older mug version. After the first batch commits, the fictional supplier returns an HTTP five oh three error. The application exits with a blocked run, keeps the next cursor, and does not claim completion.")

                saved = call("status", "--run", delta_id)
                products = call("products", "--all")
                assert len(products) == 3 and next(p for p in products if p["sku"] == "LAMP")["withdrawn"]
                scene("03 / Existing data survives", "A new CLI process reads the committed catalog and saved cursor.", ["$ catalog status --run DELTA_RUN", *summary(saved), "", "$ catalog products --all", *product_lines(products), "", "The notebook survives. MUG v3 wins. LAMP v4 stays withdrawn."], "These commands run in new application processes. The saved status still points to batch one, and all three original identities remain in the database. The changed mug is at version three. The lamp is retained as a version four withdrawal tombstone, allowing later stale active records to be rejected instead of restoring it.")

                resumed = call("resume", "--run", delta_id, expected=2)
                assert resumed["counts"]["pages"] == 3 and resumed["open_issues"] == 2 and resumed["status"] == "finished_with_issues"
                scene("04 / Resume from the checkpoint", "Batch 0 is not fetched or counted again. Data issues stay visible.", ["$ catalog resume --run DELTA_RUN", *summary(resumed), "", "next_cursor=None: explicit terminal batch received", "status=finished_with_issues: two records still need attention", "exit code: 2"], "The supplier has recovered, so we resume the same run. Processing starts at the saved cursor, preserving the completed first batch. All three batches are now consumed. However, one equal version has conflicting details and another record has a negative price. The status is finished with issues, with a distinct exit code of two.")

                changes = call("changes", "--run", delta_id)
                issues = call("issues", "--run", delta_id)
                assert len(changes) == 4 and len(issues) == 2
                change_lines = []
                for change in changes:
                    before = change.get("before")
                    old = "absent" if before is None else f'v{before["version"]}'
                    change_lines.append(f'{change["kind"]:10} {change["after"]["sku"]:6} {old:6} -> v{change["after"]["version"]}')
                scene("05 / Review changes and problems", "Audit records keep before/after values; issues retain the rejected payload.", ["$ catalog changes --run DELTA_RUN", *change_lines, "", "$ catalog issues --run DELTA_RUN", *[f'{i["sku"]:6} v{i["version"]}: {i["code"]}' for i in issues], "", "Conflicting data never overwrites the accepted product."], "Staff can inspect the exact changes and the records requiring attention. The audit includes each product's previous and resulting values. Issues retain the original payload, batch cursor, and position. An equal version conflict never overwrites accepted content. There is no trustworthy tie breaker, so the supplier must provide a higher version to resolve it.")

                fixed = call("start", "--update", "corrections")
                open_issues = call("issues")
                resolved = call("issues", "--run", delta_id, "--all")
                repaired = call("status", "--run", delta_id)
                assert open_issues == [] and repaired["status"] == "completed" and all(i.get("resolved_at") for i in resolved)
                scene("06 / Resolve outstanding problems", "A separate correction update supplies valid, strictly newer versions.", ["$ catalog start --update corrections", *summary(fixed), "", "$ catalog issues", "[]", "", "$ catalog status --run DELTA_RUN", f'status={repaired["status"]}  open_issues={repaired["open_issues"]}', "Historical issues and their resolution remain in issues --all."], "A correction update supplies mug version four and valid ink version two. These accepted higher versions resolve the older issues in the same transaction as their product changes. The outstanding issue list is now empty and the earlier delta run becomes completed. Its historical rejection counts and resolution records remain available for review.")

                replay = call("start", "--update", "delta")
                products = call("products", "--all")
                assert replay["counts"]["added"] == replay["counts"]["updated"] == replay["counts"]["withdrawn"] == replay["counts"]["restored"] == 0
                assert len(products) == 5 and len([p for p in products if not p["withdrawn"]]) == 4
                assert next(p for p in products if p["sku"] == "LAMP")["version"] == 4
                scene("07 / Replay the complete update", "No duplicate products, version regression, or withdrawal resurrection.", ["$ catalog start --update delta", *summary(replay), "", "$ catalog products --all", *product_lines(products)], "Now we import the entire delta again as a new run. Its records are duplicates or older versions, so there are zero product mutations. There are still five identities, including one withdrawn lamp, and four active products. Replaying the old invalid ink record does not reopen an issue because a valid newer version already supersedes it.")

                restart_db = work / "restart.db"
                stopped = call("start", "--update", "baseline", "--max-pages", "1", database=restart_db, expected=3)
                checkpoint = call("status", "--run", stopped["id"], database=restart_db)
                restarted = call("resume", "--run", stopped["id"], database=restart_db)
                assert checkpoint["status"] == "interrupted" and checkpoint["counts"]["pages"] == 1
                assert restarted["status"] == "completed" and restarted["counts"]["pages"] == 2 and restarted["counts"]["added"] == 3
                scene("08 / Stop, restart, and finish", "A fresh database isolates the interruption demonstration.", ["$ catalog start --update baseline --max-pages 1", *summary(stopped), "exit code: 3; application process stopped", "", "$ catalog resume --run RESTART_RUN", *summary(restarted), "Two pages total. Three additions total. No completed work lost."], "A separate database demonstrates an intentional interruption. The application stops after one committed batch and exits with code three. We then launch a new process and resume that run. It imports only the remaining batch. The final totals are two pages and three additions, proving that previously completed work was neither lost nor counted twice.")

                verification = ["go test ./... -count=1", "go vet ./..."]
                if args.go:
                    for options in [["test", "./...", "-count=1"], ["vet", "./..."]]:
                        result = subprocess.run([args.go, *options], cwd=ROOT, capture_output=True, text=True, encoding="utf-8", timeout=120)
                        if result.returncode:
                            raise RuntimeError(result.stdout + result.stderr)
                        captures.append(dict(command=["go", *options], exit_code=0, output=result.stdout, stderr=result.stderr))
                    verification = ["$ go test ./... -count=1", "PASS: CLI, version rules, HTTP, transaction and crash tests", "$ go vet ./...", "PASS"]
                scene("09 / Why recovery is safe", "One atomic commit contains product changes AND the next cursor.", [*verification, "", "Real kill tests: during fetch and an uncommitted transaction", "Assumption: immutable replayable feeds, per-SKU versions", "One local CLI process at a time; manual retry after outages", "", "README.md: run instructions, assumptions, unfinished work", "docs/DESIGN.md: protocol and failure boundaries", "AI use: Codex assisted code, tests, documentation and this demo."], "The recovery boundary is one database transaction containing product changes, audit records, issues, and the next cursor. Tests also kill real processes during a fetch and an uncommitted transaction. The documented assumptions include immutable replayable feeds and per product versions. Codex assisted implementation, tests, documentation, and this reproducible demonstration. The README describes remaining production work.")
            finally:
                server.terminate()
                try:
                    server.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    server.kill()
                    server.wait(timeout=5)

    output = ROOT / "docs/demo-transcript.json"
    output.write_text(json.dumps(dict(format_version=1, note="Compact scene views are derived from the full CLI outputs below. Temporary paths are replaced with $DEMO_DIR.", scenes=scenes, captures=captures), indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
    print(f"\nAll demo assertions passed. Transcript: {output}")


if __name__ == "__main__":
    main()
