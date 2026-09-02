#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""提取商城 HTTP 请求/响应内容"""
import subprocess
import gzip
import io

TSHARK = r"D:\Program Files\Wireshark\tshark.exe"
PCAP = r"d:\Git\YoujuHang\captures\mall_20260826154707_00001_20260826154708.pcapng"

def hex_to_bytes(h):
    h = h.strip().replace(":", "")
    try:
        return bytes.fromhex(h)
    except:
        return b""

def run_tshark(args):
    r = subprocess.run([TSHARK] + args, capture_output=True, text=True,
                       encoding="utf-8", errors="replace")
    return r.stdout, r.stderr

def extract_stream_payload(stream_id):
    """提取指定 stream 的所有 TCP payload 字节"""
    out, _ = run_tshark([
        "-r", PCAP, "-Y", f"tcp.stream == {stream_id}",
        "-T", "fields", "-e", "tcp.payload"
    ])
    all_bytes = b""
    for line in out.strip().splitlines():
        line = line.strip()
        if not line:
            continue
        b = hex_to_bytes(line)
        all_bytes += b
    return all_bytes

def decode_chunked_gzip(data):
    """解码 chunked + gzip 响应"""
    # 先找到 HTTP header 结束位置
    header_end = data.find(b"\r\n\r\n")
    if header_end < 0:
        return None, "no header end found"
    body = data[header_end + 4:]

    # Chunked decode
    chunks = []
    pos = 0
    while pos < len(body):
        # 找 chunk size (hex)
        crlf = body.find(b"\r\n", pos)
        if crlf < 0:
            break
        size_hex = body[pos:crlf].decode('ascii', errors='replace').strip()
        try:
            size = int(size_hex, 16)
        except:
            break
        if size == 0:
            break
        chunk_start = crlf + 2
        chunk_end = chunk_start + size
        chunks.append(body[chunk_start:chunk_end])
        pos = chunk_end + 2  # skip \r\n after chunk data

    decoded = b"".join(chunks)

    # Gzip decompress
    if decoded[:2] == b"\x1f\x8b":
        try:
            decoded = gzip.decompress(decoded)
        except Exception as e:
            return decoded, f"gzip failed: {e}"
    return decoded, "ok"

def main():
    # Stream 75: POST / (good_class=package)
    print("=" * 70)
    print("STREAM 75: POST / 响应体 (chunked + gzip?)")
    print("=" * 70)
    data = extract_stream_payload(75)
    body, status = decode_chunked_gzip(data)
    print(f"decode status: {status}")
    if body:
        text = body.decode('utf-8', errors='replace')
        # 找是否包含关键词
        for kw in ["时间范围", "领取", "成功", "过期", "已领取"]:
            idx = text.find(kw)
            if idx >= 0:
                start = max(0, idx - 100)
                end = min(len(text), idx + 200)
                print(f"\n>>> FOUND '{kw}':")
                print(text[start:end])
        print(f"\n--- full body (first 3000 chars) ---")
        print(text[:3000])

    # Stream 86: POST ajax_get_package
    print("\n" + "=" * 70)
    print("STREAM 86: POST ajax_get_package 响应体")
    print("=" * 70)
    data = extract_stream_payload(86)
    body, status = decode_chunked_gzip(data)
    print(f"decode status: {status}")
    if body:
        text = body.decode('utf-8', errors='replace')
        for kw in ["时间范围", "领取", "成功", "过期", "已领取", "ret", "code", "msg"]:
            idx = text.find(kw)
            if idx >= 0:
                start = max(0, idx - 50)
                end = min(len(text), idx + 200)
                print(f"\n>>> FOUND '{kw}':")
                print(text[start:end])
        print(f"\n--- full body ---")
        print(text[:5000])

    # 列出所有 POST 请求到 gotvgmall
    print("\n" + "=" * 70)
    print("所有 POST 请求到 gotvgmall")
    print("=" * 70)
    out, _ = run_tshark([
        "-r", PCAP,
        "-Y", "http.request.method == POST and (http.host contains gotvgmall or ip.dst contains 112.92.61)",
        "-T", "fields",
        "-e", "frame.time_relative",
        "-e", "tcp.stream",
        "-e", "http.request.uri",
        "-e", "http.request.body",
    ])
    for line in out.strip().splitlines():
        print(line)

if __name__ == "__main__":
    main()
