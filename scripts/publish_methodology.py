#!/usr/bin/env python3
"""Publish a methodology directory to a Hive through SwarmJet's MCP endpoint.

Usage:
  SWARMJET_TOKEN=<token> python3 scripts/publish_methodology.py \
      methodologies/pstack <hive-id> [--url http://127.0.0.1:8180/mcp] [--note "..."]

methodology.yaml is sent as the manifest; every other file except README.md
is sent as a methodology file. The token needs a user scope (default or
admin). The script prints the new version.
"""
import argparse
import json
import os
import pathlib
import sys
import urllib.request


def rpc(url, token, body, session=None):
    headers = {
        "Authorization": "Bearer " + token,
        "Content-Type": "application/json",
        "Accept": "application/json, text/event-stream",
    }
    if session:
        headers["Mcp-Session-Id"] = session
    request = urllib.request.Request(url, data=json.dumps(body).encode(), headers=headers, method="POST")
    with urllib.request.urlopen(request, timeout=60) as response:
        raw = response.read().decode()
        session = response.headers.get("Mcp-Session-Id") or session
    if not raw.strip():
        return None, session
    if raw.lstrip().startswith("{"):
        return json.loads(raw), session
    data = [line[5:].strip() for line in raw.splitlines() if line.startswith("data:")]
    return (json.loads(data[-1]) if data else None), session


def collect(directory):
    root = pathlib.Path(directory)
    manifest = (root / "methodology.yaml").read_text(encoding="utf-8")
    files = []
    for path in sorted(root.rglob("*")):
        if not path.is_file():
            continue
        rel = path.relative_to(root).as_posix()
        if rel == "methodology.yaml" or path.name == "README.md":
            continue
        files.append({"path": rel, "content": path.read_text(encoding="utf-8")})
    return manifest, files


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("directory")
    parser.add_argument("hive")
    parser.add_argument("--url", default=os.environ.get("SWARMJET_MCP_URL", "http://127.0.0.1:8180/mcp"))
    parser.add_argument("--note", default="")
    parser.add_argument("--source-url", default="")
    parser.add_argument("--source-ref", default="")
    args = parser.parse_args()
    token = os.environ.get("SWARMJET_TOKEN", "")
    if not token:
        sys.exit("SWARMJET_TOKEN is not set")

    manifest, files = collect(args.directory)
    _, session = rpc(args.url, token, {"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {
        "protocolVersion": "2025-06-18", "capabilities": {}, "clientInfo": {"name": "publish-methodology", "version": "1"}}})
    rpc(args.url, token, {"jsonrpc": "2.0", "method": "notifications/initialized"}, session)
    arguments = {"hiveId": args.hive, "manifest": manifest, "files": files, "note": args.note}
    if args.source_url:
        arguments["source"] = {"url": args.source_url, "ref": args.source_ref, "path": pathlib.Path(args.directory).name}
    result, _ = rpc(args.url, token, {"jsonrpc": "2.0", "id": 2, "method": "tools/call",
                                      "params": {"name": "publish_methodology", "arguments": arguments}}, session)
    payload = result.get("result", result) if result else {}
    if payload.get("isError"):
        sys.exit("publish failed: " + json.dumps(payload.get("content"), ensure_ascii=False))
    out = payload.get("structuredContent") or payload
    methodology = out.get("methodology", out)
    print(f"published version {methodology.get('version')} ({len(files)} files, digest {str(methodology.get('digest', ''))[:12]})")


if __name__ == "__main__":
    main()
