#!/usr/bin/env python3
"""从 LibreHardwareMonitorLib 0.9.3 NuGet 包中提取带签名的 WinRing0x64.sys。

MSI EC 风扇直控（internal/msifan）需要 WinRing0 内核驱动做端口 IO。
该驱动的原版签名二进制（GlobalSign / OpenLibSys 作者签名）曾作为内嵌资源
随 LibreHardwareMonitorLib <= 0.9.3 分发，本脚本以可复现方式将其提取出来，
避免在仓库中提交二进制。

用法: python3 scripts/fetch_winring0.py [输出目录，默认 build/bin]
"""

import io
import re
import sys
import urllib.request
import zipfile
import zlib
from pathlib import Path

NUGET_URL = "https://www.nuget.org/api/v2/package/LibreHardwareMonitorLib/0.9.3"


def extract_winring0(dll: bytes) -> bytes:
    """在 DLL 中扫描 gzip 资源，返回 x64 版 WinRing0 驱动。"""
    i = -1
    while True:
        i = dll.find(b"\x1f\x8b\x08", i + 1)
        if i < 0:
            break
        try:
            out = zlib.decompressobj(31).decompress(dll[i:])
        except zlib.error:
            continue
        if out[:2] != b"MZ" or b"WinRing0" not in out:
            continue
        pe = int.from_bytes(out[0x3C:0x40], "little")
        machine = int.from_bytes(out[pe + 4 : pe + 6], "little")
        if machine == 0x8664:  # AMD64
            return out
    raise RuntimeError("x64 WinRing0 driver resource not found in DLL")


def verify_signed(drv: bytes) -> None:
    pe = int.from_bytes(drv[0x3C:0x40], "little")
    dd = pe + 0x18 + 0x70  # PE32+ 数据目录
    sec_len = int.from_bytes(drv[dd + 4 * 8 + 4 : dd + 4 * 8 + 8], "little")
    if sec_len == 0 or b"GlobalSign" not in drv:
        raise RuntimeError("driver is not digitally signed; refusing to output")


def main() -> None:
    out_dir = Path(sys.argv[1]) if len(sys.argv) > 1 else Path("build/bin")
    out_dir.mkdir(parents=True, exist_ok=True)
    target = out_dir / "WinRing0x64.sys"

    print(f"Downloading {NUGET_URL} ...")
    with urllib.request.urlopen(NUGET_URL) as resp:
        pkg = resp.read()
    with zipfile.ZipFile(io.BytesIO(pkg)) as zf:
        name = next(n for n in zf.namelist() if re.fullmatch(r"lib/net472/LibreHardwareMonitorLib\.dll", n))
        dll = zf.read(name)

    drv = extract_winring0(dll)
    verify_signed(drv)
    target.write_bytes(drv)
    print(f"OK: {target} ({len(drv)} bytes, GlobalSign-signed)")


if __name__ == "__main__":
    main()
