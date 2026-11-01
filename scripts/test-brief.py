#!/usr/bin/env python3
"""Run go test with a concise success or bounded failure diagnostics."""
import json
import subprocess
import sys
import tempfile

args = sys.argv[1:] or ["./..."]
with tempfile.TemporaryFile(mode="w+t", encoding="utf-8") as stdout, tempfile.TemporaryFile(mode="w+t", encoding="utf-8") as stderr:
    try:
        result = subprocess.run(["go", "test", "-json", *args], stdout=stdout, stderr=stderr, check=False)
    except OSError as exc:
        print(f"BLOCKED: could not run go test: {exc}", file=sys.stderr)
        sys.exit(1)
    stdout.seek(0)
    events = []
    failed = set()
    passed_tests = 0
    skipped_tests = 0
    for line in stdout:
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            events.append((None, line.rstrip()))
            continue
        key = (event.get("Package", ""), event.get("Test", ""))
        if key[1] and event.get("Action") == "pass":
            passed_tests += 1
        if key[1] and event.get("Action") == "skip":
            skipped_tests += 1
        if event.get("Action") == "fail":
            failed.add(key)
        if event.get("Action") == "output":
            events.append((key, event.get("Output", "").rstrip()))

    if result.returncode == 0:
        if passed_tests == 0:
            print(f"BLOCKED: no tests passed (skipped: {skipped_tests}); go test {' '.join(args)}")
            sys.exit(1)
        suffix = f"; skipped: {skipped_tests}" if skipped_tests else ""
        print(f"PASS: go test {' '.join(args)}{suffix}")
        sys.exit(0)

    print(f"FAIL: go test {' '.join(args)} (exit {result.returncode})")
    lines = []
    for key, text in events:
        if key is None or key in failed or (key[1] == "" and (key[0], "") in failed):
            if text:
                lines.extend(text.splitlines())
    stderr.seek(0)
    lines.extend(stderr.read().splitlines())
    for line in lines[-120:]:
        print(line[:1000])
    if len(lines) > 120:
        print(f"[earlier {len(lines) - 120} lines omitted]")
    if not lines:
        print("No diagnostic output; rerun the affected test directly.")
    sys.exit(result.returncode if result.returncode > 0 else 1)
