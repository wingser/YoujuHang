# -*- coding: utf-8 -*-
import base64, struct, zlib, sys
sys.stdout.reconfigure(encoding='utf-8')

def b64d(s):
    s = s.strip()
    return base64.b64decode(s + '=' * (-len(s) % 4))

def try_decompress(raw):
    for wbits in (zlib.MAX_WBITS, -zlib.MAX_WBITS, 16 + zlib.MAX_WBITS, 0):
        try: return zlib.decompress(raw, wbits)
        except Exception: pass
    return raw

req_b64 = "AwII9KXvAhIgODFjYzQwNzFiYjUwYWI3MDRmODllNWI0M2YzOTkxZWY="
raw = b64d(req_b64)
print(f"req raw_len={len(raw)} hex={raw.hex()}")
if len(raw) >= 2:
    mid = struct.unpack('<H', raw[:2])[0]
    print(f"req mid={mid}")

resp_b64 = "CAGiBoQBCgYIseoBECgKBg..."  # incomplete, just for testing
