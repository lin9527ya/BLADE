#!/usr/bin/env python3
"""wxapkg_fixture.py — 生成合成 wxapkg，用于单测/端到端。

用法：
  python3 wxapkg_fixture.py --out /tmp/app.wxapkg --appid wx1234567890abcdef --sample
  python3 wxapkg_fixture.py --out /tmp/evil.wxapkg --evil
  python3 wxapkg_fixture.py --out /tmp/enc.wxapkg --appid wx... --encrypt

包格式（大端）：
  0xBE + u32 info1 + u32 indexLen + u32 bodyLen + 0xED + u32 fileCount
  + entries(nameLen+name+offset+size) + data
"""
import argparse
import hashlib
import json
import os
import struct

MAGIC = b"V1MMWX"
SALT = b"saltiest"
IV = b"the iv: 16 bytes"


def build(files, pad=True):
    """files: dict[name] = bytes. 返回明文包。"""
    if pad:
        total = sum(len(v) for v in files.values())
        if total < 1200:
            files = dict(files)
            files["__padding__.bin"] = b"\x00" * (2048 - total)
    index = b""
    data = b""
    offset = 14 + 4 + sum(4 + len(n.encode()) + 8 for n in files)  # 头部 14 + fileCount 4
    # 先算 index_len（含 fileCount）
    entries = []
    for name, body in files.items():
        nb = name.encode("utf-8")
        entries.append((nb, body))
    index_len = 4 + sum(4 + len(nb) + 8 for nb, _ in entries)
    data_offset = 14 + index_len
    offset = data_offset
    for nb, body in entries:
        index += struct.pack(">I", len(nb)) + nb + struct.pack(">II", offset, len(body))
        offset += len(body)
        data += body
    header = b"\xbe" + struct.pack(">III", 0, index_len, len(data)) + b"\xed" + struct.pack(">I", len(entries))
    return header + index + data


def encrypt(plain, appid):
    from cryptography.hazmat.primitives.ciphers import Cipher, algorithms, modes
    key = hashlib.pbkdf2_hmac("sha1", appid.encode(), SALT, 1000, 32)
    head = plain[:1023] + b"\x00"
    enc = Cipher(algorithms.AES(key), modes.CBC(IV)).encryptor()
    ct = enc.update(head) + enc.finalize()
    xor = ord(appid[-2]) if len(appid) >= 2 else 0x66
    tail = bytes(b ^ xor for b in plain[1023:])
    return MAGIC + ct + tail


def sample_files():
    app_json = {
        "pages": ["pages/index/index", "pages/user/user", "pages/order/order"],
        "window": {"navigationBarTitleText": "演示小程序"},
        "subPackages": [{"root": "pkgA", "pages": ["pages/detail/detail"]}],
        "plugins": {"myPlugin": {"version": "1.0.0", "provider": "wx1234567890abcdef"}},
    }
    service = """
var app = getApp();
var BASE = "https://api.example.com";
wx.request({ url: BASE + "/api/v1/user/info", method: "GET" });
wx.request({ url: "https://api.example.com/api/v1/order/list?page=1" });
wx.uploadFile({ url: "https://upload.example.com/v1/file", filePath: tmp });
wx.connectSocket({ url: "wss://ws.example.com/socket" });
wx.cloud.init({ env: "prod-1a2b3c" });
wx.cloud.callFunction({ name: "adminDeleteUser", data: { userId: 1 } });
wx.cloud.database().collection("orders").where({ userId: 1 }).get();
var appSecret = "0123456789abcdef0123456789abcdef";
var mapKey = "ABCDE-FGHIJ-KLMNO-PQRST-UVWXY-ZABCD";
var jwt = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.abcdefghijklmnopqrstuvwxyz";
function sign(params) { return md5(params + "secret"); }
"""
    index_wxml = '<view class="title">{{config.total}}</view><view wx:if="{{loading}}">loading</view>'
    user_js = 'Page({ data: { user: null }, onLoad: function(){ wx.request({url:"https://api.example.com/api/v1/user/detail?id=1"}) } })'
    return {
        "app.json": json.dumps(app_json, ensure_ascii=False).encode(),
        "app-service.js": service.encode(),
        "pages/index/index.wxml": index_wxml.encode(),
        "pages/index/index.js": "Page({ data: { config: { total: 0 } } })".encode(),
        "pages/index/index.wxss": b".title{color:#333}",
        "pages/user/user.js": user_js.encode(),
        "pages/order/order.js": b"Page({ data: {} })",
        "pkgA/pages/detail/detail.js": b"Page({ data: { id: 1 } })",
        "workers/worker.js": b"self.onmessage = function(e){ console.log(e.data) }",
    }


def evil_files():
    return {
        "app.json": b'{"pages":["pages/index/index"]}',
        "../evil_poc.txt": b"pwned",
        "/abs/path.txt": b"abs",
        "pages/index/index.js": b"Page({data:{}})",
        "pages/index/index.wxml": b"<view>{{x}}</view>",
        "__padding__.bin": b"\x00" * 2048,
    }


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--out", required=True)
    ap.add_argument("--appid", default="")
    ap.add_argument("--encrypt", action="store_true")
    ap.add_argument("--sample", action="store_true")
    ap.add_argument("--evil", action="store_true")
    args = ap.parse_args()

    files = evil_files() if args.evil else (sample_files() if args.sample else {"app.json": b'{"pages":[]}', "__padding__.bin": b"\x00" * 2048})
    plain = build(files)
    if args.encrypt:
        if not args.appid:
            raise SystemExit("--encrypt 需要 --appid")
        out = encrypt(plain, args.appid)
    else:
        out = plain
    os.makedirs(os.path.dirname(os.path.abspath(args.out)), exist_ok=True)
    with open(args.out, "wb") as f:
        f.write(out)
    print(json.dumps({"ok": True, "out": args.out, "size": len(out),
                      "encrypted": bool(args.encrypt), "files": len(files)}, ensure_ascii=False))


if __name__ == "__main__":
    main()
