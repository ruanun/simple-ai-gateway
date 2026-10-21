#!/usr/bin/env python3
"""检查 pnpm audit 结果：high/critical 必须被未过期的例外覆盖。

用法：python tools/check_pnpm_audit.py --audit audit.json --exceptions .github/audit-exceptions.json
例外条目：{"package": "xlsx", "advisory": "GHSA-...", "expires_on": "2026-12-31", "mitigation": "..."}
"""
import argparse
import datetime as dt
import json
import sys


def load_json(path: str):
    with open(path, "r", encoding="utf-8") as f:
        return json.load(f)


def advisory_ids(vuln: dict) -> list[str]:
    ids: list[str] = []
    for via in vuln.get("via") or []:
        if isinstance(via, str):
            ids.append(via)
        elif isinstance(via, dict):
            for key in ("github_advisory_id", "url", "source", "title", "name"):
                if via.get(key):
                    ids.append(str(via[key]))
    return ids


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--audit", required=True)
    parser.add_argument("--exceptions", required=True)
    args = parser.parse_args()

    try:
        audit = load_json(args.audit)
        exceptions = load_json(args.exceptions).get("exceptions", [])
    except (OSError, json.JSONDecodeError) as exc:
        print(f"无法读取审计/例外文件: {exc}", file=sys.stderr)
        return 1

    today = dt.date.today()
    active = []
    for exc in exceptions:
        try:
            expires = dt.date.fromisoformat(str(exc.get("expires_on", "")))
        except ValueError:
            continue
        if expires >= today:
            active.append(exc)

    failures = []
    for name, vuln in (audit.get("vulnerabilities") or {}).items():
        severity = str(vuln.get("severity", "")).lower()
        if severity not in ("high", "critical"):
            continue
        ids = advisory_ids(vuln)
        covered = any(
            exc.get("package") == name
            and (not ids or str(exc.get("advisory", "")) in ids)
            for exc in active
        )
        if not covered:
            failures.append(f"{name} ({severity}) {', '.join(ids) or '未知 advisory'}")

    if failures:
        print("存在未豁免的 high/critical 依赖漏洞：", file=sys.stderr)
        for item in failures:
            print(f"  - {item}", file=sys.stderr)
        return 1
    print("pnpm audit: 无未豁免的 high/critical 漏洞")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
