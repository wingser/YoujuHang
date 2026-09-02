#!/usr/bin/env python3
import subprocess
import json
import re

TSHARK = r"D:\Program Files\Wireshark\tshark.exe"
PCAP = r"d:\Git\YoujuHang\captures\mall_20260826154707_00001_20260826154708.pcapng"

def get_response_bytes(stream_id):
    r = subprocess.run([TSHARK, "-r", PCAP, "-Y", f"tcp.stream == {stream_id}",
                        "-T", "json", "-e", "tcp.payload", "-e", "ip.src"],
                       capture_output=True, text=True)
    frames = json.loads(r.stdout)
    response_bytes = b""
    for frame in frames:
        layers = frame.get("_source", {}).get("layers", {})
        src = layers.get("ip.src", [""])[0]
        payload = layers.get("tcp.payload", [""])[0]
        if src == "112.92.61.2" and payload:
            h = payload.replace(":", "")
            try:
                response_bytes += bytes.fromhex(h)
            except:
                pass
    return response_bytes

def decode_chunked(data):
    header_end = data.find(b"\r\n\r\n")
    if header_end < 0:
        return b""
    body = data[header_end + 4:]
    pos = 0
    chunks = []
    while pos < len(body):
        crlf = body.find(b"\r\n", pos)
        if crlf < 0:
            break
        size_hex = body[pos:crlf].decode("ascii", "replace").strip()
        try:
            size = int(size_hex, 16)
        except:
            break
        if size == 0:
            break
        chunk_start = crlf + 2
        chunk_end = chunk_start + size
        chunks.append(body[chunk_start:chunk_end])
        pos = chunk_end + 2
    return b"".join(chunks)

def main():
    # Stream 75 response
    data = get_response_bytes(75)
    html = decode_chunked(data).decode("utf-8", errors="replace")
    print(f"HTML total: {len(html)} chars")

    # Find package-related IDs and info
    print("\n=== Search for package IDs ===")
    patterns = [
        r'data-id="(\d+)"',
        r'package_id="(\d+)"',
        r'id="(\d+)"[^>]*package',
        r'ajax_get_package[^>]*id[=:]\s*(\d+)',
        r'gift_id["\']?\s*[=:]\s*(\d+)',
    ]
    for pat in patterns:
        matches = re.findall(pat, html, re.IGNORECASE)
        if matches:
            print(f"  {pat}: {matches[:20]}")

    # Find sections with package names
    keywords = ["微信绑定", "vip专属", "svip专属", "ssvip专属", "礼包", "package"]
    for kw in keywords:
        idx = html.lower().find(kw)
        if idx >= 0:
            snippet = html[max(0, idx - 200):idx + 500]
            print(f"\n=== {kw} ===")
            print(snippet)

    # Also save full HTML for inspection
    with open(r"d:\Git\YoujuHang\captures\mall_page.html", "w", encoding="utf-8") as f:
        f.write(html)
    print(f"\nFull HTML saved to mall_page.html")

if __name__ == "__main__":
    main()
