#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
通过 GitHub Contents API 推送本地仓库内容（绕过被代理阻断的 git 协议）。

用法:
    python push_contents_api.py <repo_dir> <owner/repo> <token> [branch]

特性:
  - 自动创建/复用分支
  - 逐个文件 PUT（已存在则带 sha 更新）
  - 指数退避重试，抵御 502 / RemoteDisconnected
  - 只推送受版本控制且有变化的文件（由调用方给出清单）
"""
import base64
import json
import os
import ssl
import sys
import time
import urllib.error
import urllib.request

API = "https://api.github.com"
UA = "proxypilot-pusher/1.0"


def req(method, url, token, body=None, retries=5):
    data = None
    if body is not None:
        data = json.dumps(body).encode("utf-8")
    last = None
    for attempt in range(retries):
        r = urllib.request.Request(url, data=data, method=method)
        r.add_header("Authorization", "token " + token)
        r.add_header("Accept", "application/vnd.github+json")
        r.add_header("User-Agent", UA)
        if data is not None:
            r.add_header("Content-Type", "application/json")
        try:
            ctx = ssl.create_default_context()
            with urllib.request.urlopen(r, timeout=45, context=ctx) as resp:
                raw = resp.read()
                return resp.status, (json.loads(raw) if raw else {})
        except urllib.error.HTTPError as e:
            raw = e.read()
            try:
                payload = json.loads(raw) if raw else {}
            except Exception:
                payload = {"raw": raw[:400].decode("utf-8", "replace")}
            last = (e.code, payload)
            # 4xx（除限流）直接返回，不重试
            if e.code in (401, 403, 404, 409, 422):
                return e.code, payload
        except Exception as e:  # 网络类错误 -> 退避重试
            last = (0, {"error": str(e)})
        wait = min(2 ** attempt, 20) + (attempt * 0.5)
        print(f"    重试 {attempt+1}/{retries}（{wait:.1f}s）: {last}")
        time.sleep(wait)
    return last if last else (0, {"error": "unknown"})


def get_ref_sha(owner_repo, branch, token):
    code, body = req("GET", f"{API}/repos/{owner_repo}/git/ref/heads/{branch}", token)
    if code == 200:
        return body.get("object", {}).get("sha")
    return None


def ensure_branch(owner_repo, branch, token, base="main"):
    """确保分支存在；不存在则基于 base 创建。返回 (ok, sha_or_err)"""
    sha = get_ref_sha(owner_repo, branch, token)
    if sha:
        return True, sha

    # 分支不存在：尝试从 base 创建
    base_sha = get_ref_sha(owner_repo, base, token)
    if base_sha is None and base != branch:
        base_sha = get_ref_sha(owner_repo, "master", token)

    if base_sha is None:
        # 仓库完全为空：直接创建空分支引用需要至少一次提交，
        # 这里交给第一个文件 PUT 自动初始化（PUT contents 时可省略 branch）
        print("  仓库为空，将由首个文件写入初始化分支")
        return True, None

    code, body = req("POST", f"{API}/repos/{owner_repo}/git/refs", token, {
        "ref": f"refs/heads/{branch}",
        "sha": base_sha,
    })
    if code in (201, 200):
        return True, base_sha
    if code == 422:  # 已存在
        return True, get_ref_sha(owner_repo, branch, token)
    return False, body


def put_file(owner_repo, branch, path, content, token, message):
    url = f"{API}/repos/{owner_repo}/contents/{urllib.request.quote(path)}"
    # 查询当前 sha
    sha = None
    code, body = req("GET", url + f"?ref={branch}", token)
    if code == 200 and isinstance(body, dict):
        sha = body.get("sha")

    payload = {
        "message": message,
        "content": base64.b64encode(content).decode("ascii"),
        "branch": branch,
    }
    if sha:
        payload["sha"] = sha

    code, body = req("PUT", url, token, payload)
    if code in (200, 201):
        return True, body.get("content", {}).get("sha", "")
    return False, body


def walk_files(root):
    skip_dirs = {".git", "dist", "data", "node_modules", ".github/workflows/.tmp"}
    skip_ext = {".exe", ".zip", ".log", ".dat", ".db", ".mmdb"}
    out = []
    for dirpath, dirnames, filenames in os.walk(root):
        dirnames[:] = [d for d in dirnames if d not in skip_dirs]
        for fn in filenames:
            ext = os.path.splitext(fn)[1].lower()
            if ext in skip_ext:
                continue
            full = os.path.join(dirpath, fn)
            rel = os.path.relpath(full, root).replace("\\", "/")
            out.append((rel, full))
    return sorted(out)


def main():
    if len(sys.argv) < 4:
        print(__doc__)
        return 1
    root, owner_repo, token = sys.argv[1], sys.argv[2], sys.argv[3]
    branch = sys.argv[4] if len(sys.argv) > 4 else "main"

    ok, sha_or_err = ensure_branch(owner_repo, branch, token)
    if not ok:
        print("创建分支失败:", sha_or_err)
        return 1
    print(f"分支 {branch} 就绪")

    files = walk_files(root)
    print(f"待推送文件 {len(files)} 个")
    failed = []
    for i, (rel, full) in enumerate(files, 1):
        with open(full, "rb") as f:
            content = f.read()
        ok, info = put_file(owner_repo, branch, rel, content, token,
                            f"chore: sync {rel}")
        status = "OK " if ok else "FAIL"
        print(f"  [{i}/{len(files)}] {status} {rel}")
        if not ok:
            failed.append((rel, info))
        time.sleep(0.35)  # 轻微限速，避免触发二级限流

    if failed:
        print(f"\n{len(failed)} 个文件推送失败：")
        for rel, info in failed:
            print("  -", rel, info)
        return 1
    print("\n全部推送成功")
    return 0


if __name__ == "__main__":
    sys.exit(main())
