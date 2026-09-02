# -*- coding: utf-8 -*-
import base64, sys
sys.path.insert(0, r"d:\Git\YoujuHang\tools")
from proto_decode import parse_msg_str

# stream 5 响应（70B base64）
b64b = bytes.fromhex("43414769426d554b42676978366745514b416f47434c66714152414b436767496b4534597338444158676f46434a4e4f47486f4b41676749436759494478445f325145534341")
print("b64 text:", b64b.decode())
s = b64b.decode().replace("-", "+").replace("_", "/")
s += "=" * (-len(s) % 4)
dec = base64.b64decode(s, validate=False)
print("decoded %d bytes: %s" % (len(dec), dec.hex()))
print(parse_msg_str(dec))

# 尝试 gzip
import gzip
try:
    g = gzip.decompress(dec)
    print("gzip ok:", len(g), g.hex())
    print(parse_msg_str(g))
except Exception as e:
    print("not gzip:", e)

# stream 7 响应（48B）
b7 = bytes.fromhex("0801a206510a0808904e189bc8c05e22120803120e08e81010901c180128d186bad4062a")
print("\nstream7 resp:", b7.hex())
print(parse_msg_str(b7))
