#!/usr/bin/env python3
"""wxapkg_safe.py — 微信小程序包安全解析/解密/提取（零依赖优先）。

设计目标：
- V1MMWX 解密（PBKDF2-HMAC-SHA1 + AES-256-CBC + XOR）；
- 严格索引校验与路径清洗，拒绝 ../、绝对路径、盘符、UNC、NUL、超长名；
- 兼容 indexLen 含/不含 fileCount、offset 绝对/相对、数据区重叠；
- 只读包内容，不执行包内任何代码。

用法：
  python3 wxapkg_safe.py info    --in <pkg>
  python3 wxapkg_safe.py decrypt --in <pkg> --appid wx... --out <plain>
  python3 wxapkg_safe.py extract --in <pkg> --out <dir> [--max-files N] [--max-total BYTES]

输出：stdout 一行 JSON；错误时 ok=false + code。
"""
import argparse
import hashlib
import json
import os
import re
import struct
import subprocess
import sys

MAGIC_ENC = b"V1MMWX"
MAC_TAG = b"WAPkgEncryptedTagForMac"
SALT = b"saltiest"
IV = b"the iv: 16 bytes"
DEFAULT_XOR = 0x66

MAX_FILES = 100000
MAX_SINGLE = 128 * 1024 * 1024
MAX_TOTAL = 1024 * 1024 * 1024
MAX_NAME = 1024

WINDOWS_RESERVED = {
    "CON", "PRN", "AUX", "NUL",
    *{f"COM{i}" for i in range(1, 10)},
    *{f"LPT{i}" for i in range(1, 10)},
}


class PkgError(Exception):
    def __init__(self, code, message):
        super().__init__(message)
        self.code = code
        self.message = message


def u32(data, off):
    return struct.unpack_from(">I", data, off)[0]


def ensure(cond, code, message):
    if not cond:
        raise PkgError(code, message)


def derive_key(appid):
    return hashlib.pbkdf2_hmac("sha1", appid.encode("utf-8"), SALT, 1000, 32)


def aes_cbc_decrypt(key, data):
    """AES-256-CBC 解密，无 padding 处理。优先 cryptography，退回 openssl CLI。"""
    try:
        from cryptography.hazmat.primitives.ciphers import Cipher, algorithms, modes
        dec = Cipher(algorithms.AES(key), modes.CBC(IV)).decryptor()
        return dec.update(data) + dec.finalize()
    except ImportError:
        try:
            p = subprocess.run(
                ["openssl", "enc", "-aes-256-cbc", "-d", "-nopad",
                 "-K", key.hex(), "-iv", IV.hex()],
                input=data, stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=False)
        except FileNotFoundError:
            raise PkgError("no_aes", "缺少 cryptography 与 openssl，无法解密")
        if p.returncode != 0:
            raise PkgError("aes_failed", "openssl 解密失败: %s" % p.stderr.decode("utf-8", "replace")[:200])
        return p.stdout


def decrypt_v1mmwx(data, appid):
    ensure(len(appid) >= 2, "bad_appid", "AppID 至少 2 个字符")
    ensure(len(data) >= 6 + 1024, "truncated", "V1MMWX 数据不足 1030 字节")
    key = derive_key(appid)
    head = aes_cbc_decrypt(key, data[6:6 + 1024])[:1023]
    xor_key = ord(appid[-2])
    tail = bytes(b ^ xor_key for b in data[6 + 1024:])
    plain = head + tail
    ensure(plain[0] == 0xBE and plain[13] == 0xED, "bad_plain", "解密结果缺少 0xBE/0xED 标记（AppID 可能不对）")
    return plain


def detect(data):
    if data[:6] == MAGIC_ENC:
        return "v1mmwx"
    if len(data) >= 16 and data[:1] == b"\xbe" and data[13:14] == b"\xed":
        return "plain"
    if data.endswith(MAC_TAG):
        return "mac_legacy"
    return "unknown"


def sanitize_name(name):
    """返回安全相对路径；不安全则抛 unsafe_name。"""
    ensure(isinstance(name, str) and name != "", "unsafe_name", "空文件名")
    ensure("\x00" not in name, "unsafe_name", "文件名含 NUL")
    n = name.replace("\\", "/")
    ensure(not n.startswith("//"), "unsafe_name", "UNC 路径: %r" % name)
    ensure(not re.match(r"^[A-Za-z]:", n), "unsafe_name", "盘符路径: %r" % name)
    n = n.lstrip("/")
    ensure(n != "", "unsafe_name", "空文件名")
    parts = []
    for seg in n.split("/"):
        if seg in ("", "."):
            continue
        ensure(seg != "..", "unsafe_name", "路径含 ..: %r" % name)
        ensure(len(seg.encode("utf-8")) <= 255, "unsafe_name", "路径段过长: %r" % name)
        ensure(not any(ord(ch) < 32 for ch in seg), "unsafe_name", "路径含控制字符: %r" % name)
        parts.append(seg)
    ensure(parts, "unsafe_name", "空文件名")
    base = parts[-1].split(".")[0].upper()
    ensure(base not in WINDOWS_RESERVED, "unsafe_name", "Windows 保留名: %r" % name)
    rel = "/".join(parts)
    ensure(len(rel.encode("utf-8")) <= MAX_NAME, "unsafe_name", "文件名过长: %r" % name)
    return rel


def parse_wxapkg(data):
    ensure(len(data) >= 18, "truncated", "数据不足 18 字节")
    ensure(data[0] == 0xBE and data[13] == 0xED, "bad_magic", "缺少 0xBE/0xED 标记")
    index_len = u32(data, 5)
    body_len = u32(data, 9)
    size = len(data)
    ok_len = size in (14 + index_len + body_len, 18 + index_len + body_len)
    ensure(ok_len, "bad_length",
           "长度不自洽: size=%d index=%d body=%d" % (size, index_len, body_len))
    file_count = u32(data, 14)
    ensure(0 < file_count <= MAX_FILES, "bad_index", "文件数异常: %d" % file_count)

    pos = 18
    entries = []
    for _ in range(file_count):
        ensure(pos + 4 <= size, "bad_index", "索引截断(nameLen)")
        nlen = u32(data, pos)
        pos += 4
        ensure(0 < nlen <= MAX_NAME and pos + nlen <= size, "bad_index", "文件名长度异常: %d" % nlen)
        raw_name = data[pos:pos + nlen]
        pos += nlen
        try:
            name = raw_name.decode("utf-8")
        except UnicodeDecodeError:
            raise PkgError("bad_index", "文件名不是合法 UTF-8")
        name = sanitize_name(name)
        ensure(pos + 8 <= size, "bad_index", "索引截断(offset/size)")
        off = u32(data, pos)
        sz = u32(data, pos + 4)
        pos += 8
        ensure(sz <= MAX_SINGLE, "too_large", "单文件过大: %s (%d)" % (name, sz))
        entries.append({"name": name, "offset": off, "size": sz})

    candidates = []
    if pos == 14 + index_len:
        candidates.append(14 + index_len)
    if pos == 18 + index_len:
        candidates.append(18 + index_len)
    ensure(candidates, "bad_index_end",
           "索引结束位置异常: pos=%d indexLen=%d" % (pos, index_len))
    data_start = candidates[0]

    # offset 基准：绝对（从文件头）或相对（从数据区起点）。
    mode = "absolute"
    if entries:
        first = entries[0]["offset"]
        if first not in candidates:
            if first + data_start <= size:
                mode = "relative"
            else:
                raise PkgError("bad_offset", "首文件 offset 越界: %d" % first)
    for e in entries:
        base = 0 if mode == "absolute" else data_start
        abs_off = base + e["offset"]
        ensure(abs_off >= 0 and abs_off + e["size"] <= size, "bad_offset",
               "文件越界: %s off=%d size=%d" % (e["name"], abs_off, e["size"]))
        e["abs_offset"] = abs_off

    return {
        "index_len": index_len,
        "body_len": body_len,
        "file_count": file_count,
        "offset_mode": mode,
        "data_start": data_start,
        "entries": entries,
    }


def cmd_info(args):
    data = open(args.input, "rb").read()
    kind = detect(data)
    info = {"ok": True, "kind": kind, "size": len(data), "sha256": hashlib.sha256(data).hexdigest()}
    if kind == "plain":
        parsed = parse_wxapkg(data)
        info.update({
            "file_count": parsed["file_count"],
            "index_len": parsed["index_len"],
            "body_len": parsed["body_len"],
            "offset_mode": parsed["offset_mode"],
            "entries": [{"name": e["name"], "size": e["size"]} for e in parsed["entries"][:50]],
        })
    elif kind == "mac_legacy":
        info["note"] = "macOS <3.8 旧加密包，需要 Frida 提取密钥后另行解密"
    return info


def cmd_decrypt(args):
    data = open(args.input, "rb").read()
    kind = detect(data)
    if kind == "plain":
        plain = data
    elif kind == "v1mmwx":
        ensure(args.appid, "bad_appid", "V1MMWX 解密需要 --appid（小程序 AppID，不是 wxid）")
        plain = decrypt_v1mmwx(data, args.appid)
    elif kind == "mac_legacy":
        raise PkgError("mac_legacy", "macOS <3.8 旧加密包，需要 Frida 提取密钥")
    else:
        raise PkgError("unknown_format", "无法识别的包格式（既非明文也非 V1MMWX）")
    out = args.out or (os.path.splitext(args.input)[0] + ".plain.wxapkg")
    os.makedirs(os.path.dirname(os.path.abspath(out)), exist_ok=True)
    with open(out, "wb") as f:
        f.write(plain)
    return {"ok": True, "kind": kind, "out": out, "size": len(plain),
            "sha256": hashlib.sha256(plain).hexdigest()}


def cmd_extract(args):
    data = open(args.input, "rb").read()
    kind = detect(data)
    if kind == "v1mmwx":
        ensure(args.appid, "bad_appid", "V1MMWX 提取需要 --appid")
        data = decrypt_v1mmwx(data, args.appid)
        kind = "plain"
    elif kind == "mac_legacy":
        raise PkgError("mac_legacy", "macOS <3.8 旧加密包，需要 Frida 提取密钥")
    elif kind != "plain":
        raise PkgError("unknown_format", "无法识别的包格式")
    parsed = parse_wxapkg(data)

    max_files = args.max_files or MAX_FILES
    max_total = args.max_total or MAX_TOTAL
    ensure(len(parsed["entries"]) <= max_files, "too_many", "文件数超过上限: %d" % len(parsed["entries"]))
    total = sum(e["size"] for e in parsed["entries"])
    ensure(total <= max_total, "too_large", "解压总量超过上限: %d" % total)

    out_root = os.path.abspath(args.out)
    os.makedirs(out_root, exist_ok=True)
    written = 0
    for e in parsed["entries"]:
        dst = os.path.abspath(os.path.join(out_root, e["name"]))
        if os.path.commonpath([out_root, dst]) != out_root:
            raise PkgError("unsafe_name", "路径越界: %s" % e["name"])
        os.makedirs(os.path.dirname(dst), exist_ok=True)
        with open(dst, "wb") as f:
            f.write(data[e["abs_offset"]:e["abs_offset"] + e["size"]])
        written += 1
    return {"ok": True, "kind": kind, "out": out_root, "files": written,
            "total_bytes": total, "offset_mode": parsed["offset_mode"],
            "sha256": hashlib.sha256(data).hexdigest()}


def main():
    ap = argparse.ArgumentParser(description="微信小程序包安全解析/解密/提取")
    sub = ap.add_subparsers(dest="cmd", required=True)

    p = sub.add_parser("info")
    p.add_argument("--in", dest="input", required=True)

    p = sub.add_parser("decrypt")
    p.add_argument("--in", dest="input", required=True)
    p.add_argument("--appid", default="")
    p.add_argument("--out", default="")

    p = sub.add_parser("extract")
    p.add_argument("--in", dest="input", required=True)
    p.add_argument("--out", required=True)
    p.add_argument("--appid", default="")
    p.add_argument("--max-files", type=int, default=0)
    p.add_argument("--max-total", type=int, default=0)

    args = ap.parse_args()
    try:
        if args.cmd == "info":
            result = cmd_info(args)
        elif args.cmd == "decrypt":
            result = cmd_decrypt(args)
        else:
            result = cmd_extract(args)
    except PkgError as e:
        print(json.dumps({"ok": False, "code": e.code, "error": e.message}, ensure_ascii=False))
        sys.exit(2)
    except Exception as e:  # noqa: BLE001 - 统一结构化错误
        print(json.dumps({"ok": False, "code": "internal", "error": str(e)}, ensure_ascii=False))
        sys.exit(2)
    print(json.dumps(result, ensure_ascii=False))


if __name__ == "__main__":
    main()
