#!/usr/bin/env python3
"""
insomnia5_to_lazycurl.py

Converts an Insomnia v5 export (per-workspace .yaml files, the kind you get from
Insomnia's "Export" button on a single workspace) into stuff LazyCurl understands:

  - a Postman v2.1 collection per *.insomnia.rest/5.0 collection file
      -> lazycurl import postman <file>
  - LazyCurl environment json files per *environment.insomnia.rest/5.0 file
    (base environment + each subEnvironment merged on top of it)
      -> drop into <project>/.lazycurl/environments/

Requires: pip install pyyaml

Usage:
  python3 insomnia5_to_lazycurl.py <dir_of_yaml_exports_or_single_file> <out_dir>
"""
import sys
import os
import re
import json
import glob
import datetime


def json_default(o):
    if isinstance(o, (datetime.date, datetime.datetime)):
        return o.isoformat()
    return str(o)

try:
    import yaml
except ImportError:
    print("needs pyyaml: python3 -m pip install pyyaml --break-system-packages")
    sys.exit(1)


def convert_headers(headers):
    return [
        {"key": h.get("name", ""), "value": h.get("value", ""), "disabled": h.get("disabled", False)}
        for h in headers or []
    ]


def convert_body(body):
    if not body:
        return {}
    mime = body.get("mimeType", "")
    if "json" in mime or "text" in mime or mime == "":
        return {"mode": "raw", "raw": body.get("text", "")}
    if "form" in mime and body.get("params"):
        return {
            "mode": "urlencoded",
            "urlencoded": [
                {"key": p.get("name", ""), "value": p.get("value", "")}
                for p in body.get("params", [])
            ],
        }
    return {"mode": "raw", "raw": body.get("text", "")}


def convert_auth(auth):
    if not auth:
        return None
    t = auth.get("type")
    if t == "bearer":
        return {"type": "bearer", "bearer": [{"key": "token", "value": auth.get("token", ""), "type": "string"}]}
    if t == "basic":
        return {
            "type": "basic",
            "basic": [
                {"key": "username", "value": auth.get("username", ""), "type": "string"},
                {"key": "password", "value": auth.get("password", ""), "type": "string"},
            ],
        }
    return None


def is_request(node):
    return isinstance(node, dict) and "url" in node and "children" not in node


def build_node(node):
    # folder
    if "children" in node:
        return {"name": node.get("name", "Folder"), "item": [build_node(c) for c in node.get("children", [])]}
    # request
    req = {
        "method": node.get("method", "GET"),
        "header": convert_headers(node.get("headers")),
        "body": convert_body(node.get("body")),
        "url": {"raw": node.get("url", ""), "host": [node.get("url", "")]},
    }
    auth = convert_auth(node.get("authentication"))
    if auth:
        req["auth"] = auth
    return {"name": node.get("name", "Untitled"), "request": req}


def convert_collection(doc, out_dir, base_name):
    root = doc.get("collection", [])
    items = [build_node(n) for n in root] if isinstance(root, list) else [build_node(root)]
    collection = {
        "info": {
            "name": doc.get("name", base_name),
            "schema": "https://schema.getpostman.com/json/collection/v2.1.0/collection.json",
        },
        "item": items,
    }
    fname = re.sub(r"[^a-zA-Z0-9_-]+", "_", base_name) + "_collection.json"
    path = os.path.join(out_dir, fname)
    with open(path, "w") as f:
        json.dump(collection, f, indent=2, default=json_default)
    return path


def env_to_lazycurl(name, data, secret_keys=()):
    variables = {}
    for k, v in (data or {}).items():
        variables[k] = {
            "value": "" if v is None else str(v),
            "secret": k in secret_keys,
            "active": True,
        }
    return {"name": name, "description": "", "variables": variables}


SECRET_HINTS = re.compile(r"(token|secret|apikey|api_key|password|pass|auth|jwt|key)$", re.I)


def convert_environments(doc, out_dir, base_name):
    env = doc.get("environments", {})
    if not env:
        return []
    base_data = env.get("data", {}) or {}
    base_name_label = env.get("name", base_name)
    written = []

    def secrets_for(data):
        return tuple(k for k in data if SECRET_HINTS.search(k))

    base_lc = env_to_lazycurl(f"{base_name} - {base_name_label}", base_data, secrets_for(base_data))
    subs = env.get("subEnvironments", []) or []
    if not subs:
        fname = re.sub(r"[^a-zA-Z0-9_-]+", "_", base_lc["name"]).lower() + ".json"
        path = os.path.join(out_dir, fname)
        with open(path, "w") as f:
            json.dump(base_lc, f, indent=2, default=json_default)
        written.append(path)
        return written

    for sub in subs:
        merged = dict(base_data)
        merged.update(sub.get("data", {}) or {})
        name = f"{base_name} - {sub.get('name', 'Environment')}"
        lc = env_to_lazycurl(name, merged, secrets_for(merged))
        fname = re.sub(r"[^a-zA-Z0-9_-]+", "_", name).lower() + ".json"
        path = os.path.join(out_dir, fname)
        with open(path, "w") as f:
            json.dump(lc, f, indent=2, default=json_default)
        written.append(path)
    return written


def main():
    if len(sys.argv) < 3:
        print("usage: insomnia5_to_lazycurl.py <dir_or_file.yaml> <out_dir>")
        sys.exit(1)

    src, out_dir = sys.argv[1], sys.argv[2]
    files = [src] if os.path.isfile(src) else glob.glob(os.path.join(src, "*.yaml")) + glob.glob(os.path.join(src, "*.yml"))

    collections_out = os.path.join(out_dir, "collections")
    envs_out = os.path.join(out_dir, "environments")
    os.makedirs(collections_out, exist_ok=True)
    os.makedirs(envs_out, exist_ok=True)

    n_col, n_env = 0, 0
    for path in files:
        with open(path) as f:
            doc = yaml.safe_load(f)
        if not doc or "type" not in doc:
            continue
        base_name = os.path.splitext(os.path.basename(path))[0]
        base_name = re.sub(r"-wrk_[a-f0-9]+$", "", base_name)
        if doc["type"].startswith("collection.insomnia.rest"):
            p = convert_collection(doc, collections_out, base_name)
            print(f"collection -> {p}")
            n_col += 1
        elif doc["type"].startswith("environment.insomnia.rest"):
            paths = convert_environments(doc, envs_out, base_name)
            for p in paths:
                print(f"environment -> {p}")
            n_env += len(paths)

    print(f"\n{n_col} collection(s), {n_env} environment file(s) written to {out_dir}")
    print("next: lazycurl import postman <collections/*.json>, then copy environments/*.json into .lazycurl/environments/")


if __name__ == "__main__":
    main()
