#!/usr/bin/env python3
"""アプリアイコンを生成する (依存なし: Python 標準ライブラリのみ)。

生成物
  android/app/src/main/res/drawable/ic_launcher_foreground.xml
  android/app/src/main/res/drawable/ic_launcher_background.xml
  android/app/src/main/res/mipmap-anydpi-v26/ic_launcher.xml (+ _round)
  android/app/src/main/res/mipmap-{m,h,xh,xxh,xxxh}dpi/ic_launcher.png
  android/fastlane/metadata/android/*/images/icon.png (512x512)

意匠: Nocturne アクセント (#6B5FD6) の地に、白の受話器と
その上を渡る「橋」のアーチ。アーチ = Tunnel 越しの経路。

ベクタ (VectorDrawable) と PNG を同じパスデータから作るので、
両者の見た目は一致する。PNG は 4x スーパーサンプリングのスキャンライン塗り。
"""

import math
import os
import re
import struct
import zlib

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
RES = os.path.join(ROOT, "android", "app", "src", "main", "res")
FASTLANE = os.path.join(ROOT, "android", "fastlane", "metadata", "android")

VIEWPORT = 108.0          # アダプティブアイコンの論理サイズ
SAFE = 72.0               # マスクされずに残る中心領域
ACCENT = (0x6B, 0x5F, 0xD6)   # nocturne_accent
FG = (0xFF, 0xFF, 0xFF)

# --- 受話器 (Material "call" の 24x24 パス) -------------------------------
HANDSET_24 = (
    "M6.62,10.79c1.44,2.83 3.76,5.14 6.59,6.59l2.2,-2.2c0.27,-0.27 0.67,-0.36 "
    "1.02,-0.24 1.12,0.37 2.33,0.57 3.57,0.57 0.55,0 1,0.45 1,1V20c0,0.55 "
    "-0.45,1 -1,1 -9.39,0 -17,-7.61 -17,-17 0,-0.55 0.45,-1 1,-1h3.5c0.55,0 "
    "1,0.45 1,1 0,1.25 0.2,2.45 0.57,3.57 0.11,0.35 0.03,0.74 -0.25,1.02l-2.2,2.2z"
)
HANDSET_SCALE = 2.05
HANDSET_CX, HANDSET_CY = 54.0, 64.0   # 108 viewport 内の配置中心

# --- 橋のアーチ -----------------------------------------------------------
ARCH_CX, ARCH_CY = 54.0, 58.0   # アーチの中心 (下向きに開く弧)
ARCH_R = 26.0                   # 半径
ARCH_W = 5.2                    # 線幅
ARCH_FROM, ARCH_TO = 202.0, 338.0   # 角度 (度, 0=右, 反時計回り)
PIER_R = 3.9                    # 両端の橋脚 (円)


# ====== パスのパース・平坦化 ==============================================

NUM = re.compile(r"[-+]?[0-9]*\.?[0-9]+(?:[eE][-+]?[0-9]+)?")


def parse_path(d):
    """SVG パス (M/L/H/V/C/Z の絶対・相対) を輪郭 [[(x,y),...],...] にする。"""
    contours, cur = [], []
    x = y = sx = sy = 0.0
    i, n = 0, len(d)
    cmd = None
    while i < n:
        ch = d[i]
        if ch in " ,\t\r\n":
            i += 1
            continue
        if ch.isalpha():
            cmd = ch
            i += 1
            if cmd in "Zz":
                if cur:
                    contours.append(cur)
                    cur = []
                x, y = sx, sy
                continue
        # cmd に必要な数だけ数値を読む
        need = {"M": 2, "m": 2, "L": 2, "l": 2, "H": 1, "h": 1,
                "V": 1, "v": 1, "C": 6, "c": 6}[cmd]
        vals = []
        while len(vals) < need:
            m = NUM.match(d, i)
            if not m:
                raise ValueError(f"パス解析失敗 @{i}: {d[i:i+20]!r}")
            vals.append(float(m.group()))
            i = m.end()
            while i < n and d[i] in " ,\t\r\n":
                i += 1
        if cmd in "Mm":
            if cur:
                contours.append(cur)
            x, y = (vals[0], vals[1]) if cmd == "M" else (x + vals[0], y + vals[1])
            sx, sy = x, y
            cur = [(x, y)]
            cmd = "L" if cmd == "M" else "l"   # 以降の暗黙コマンドは lineto
        elif cmd in "Ll":
            x, y = (vals[0], vals[1]) if cmd == "L" else (x + vals[0], y + vals[1])
            cur.append((x, y))
        elif cmd in "Hh":
            x = vals[0] if cmd == "H" else x + vals[0]
            cur.append((x, y))
        elif cmd in "Vv":
            y = vals[0] if cmd == "V" else y + vals[0]
            cur.append((x, y))
        elif cmd in "Cc":
            if cmd == "C":
                x1, y1, x2, y2, x3, y3 = vals
            else:
                x1, y1 = x + vals[0], y + vals[1]
                x2, y2 = x + vals[2], y + vals[3]
                x3, y3 = x + vals[4], y + vals[5]
            cur.extend(flatten_cubic(x, y, x1, y1, x2, y2, x3, y3))
            x, y = x3, y3
    if cur:
        contours.append(cur)
    return contours


def flatten_cubic(x0, y0, x1, y1, x2, y2, x3, y3, steps=16):
    pts = []
    for k in range(1, steps + 1):
        t = k / steps
        u = 1 - t
        pts.append((
            u*u*u*x0 + 3*u*u*t*x1 + 3*u*t*t*x2 + t*t*t*x3,
            u*u*u*y0 + 3*u*u*t*y1 + 3*u*t*t*y2 + t*t*t*y3,
        ))
    return pts


def transform(contours, scale, cx, cy):
    """24 viewport のパスを 108 viewport の (cx,cy) 中心へ移す。"""
    off = 12.0 * scale
    return [[(cx - off + px * scale, cy - off + py * scale) for px, py in c]
            for c in contours]


def arch_contour():
    """下向きに開くアーチ (太さのある弧) を 1 本の閉輪郭にする。"""
    steps = 96
    outer, inner = [], []
    ro, ri = ARCH_R + ARCH_W / 2, ARCH_R - ARCH_W / 2
    for k in range(steps + 1):
        a = math.radians(ARCH_FROM + (ARCH_TO - ARCH_FROM) * k / steps)
        outer.append((ARCH_CX + ro * math.cos(a), ARCH_CY + ro * math.sin(a)))
        inner.append((ARCH_CX + ri * math.cos(a), ARCH_CY + ri * math.sin(a)))
    return outer + inner[::-1]


def circle_contour(cx, cy, r, steps=48):
    return [(cx + r * math.cos(2 * math.pi * k / steps),
             cy + r * math.sin(2 * math.pi * k / steps)) for k in range(steps)]


def pier_centers():
    out = []
    for deg in (ARCH_FROM, ARCH_TO):
        a = math.radians(deg)
        out.append((ARCH_CX + ARCH_R * math.cos(a), ARCH_CY + ARCH_R * math.sin(a)))
    return out


def foreground_contours():
    cs = transform(parse_path(HANDSET_24), HANDSET_SCALE, HANDSET_CX, HANDSET_CY)
    cs.append(arch_contour())
    for cx, cy in pier_centers():
        cs.append(circle_contour(cx, cy, PIER_R))
    return cs


# ====== ラスタライズ ======================================================

def rasterize(contours, size, view, ss=4):
    """nonzero 塗りのカバレッジ [0..1] を size*size 返す。view = 論理サイズ。

    nonzero にするのは、アーチと橋脚の円が重なるため。even-odd だと
    重なった部分が切り欠きになる。
    """
    w = size * ss
    cov = [0.0] * (size * size)
    k = w / view
    edges = []
    for c in contours:
        for i in range(len(c)):
            x0, y0 = c[i]
            x1, y1 = c[(i + 1) % len(c)]
            if y0 != y1:
                edges.append((y0 * k, x0 * k, y1 * k, x1 * k,
                              1 if y1 > y0 else -1))
    if not edges:
        return cov
    inv = 1.0 / (ss * ss)
    for row in range(w):
        yc = row + 0.5
        xs = []
        for y0, x0, y1, x1, wdir in edges:
            if (y0 <= yc < y1) or (y1 <= yc < y0):
                xs.append((x0 + (yc - y0) * (x1 - x0) / (y1 - y0), wdir))
        if not xs:
            continue
        xs.sort()
        spans, wind, start = [], 0, 0.0
        for xv, wdir in xs:
            if wind == 0:
                start = xv
            wind += wdir
            if wind == 0 and xv > start:
                spans.append((start, xv))
        orow = row // ss
        base = orow * size
        for a, b in spans:
            ia, ib = int(math.floor(a)), int(math.ceil(b))
            for px in range(max(ia, 0), min(ib, w)):
                seg = min(b, px + 1.0) - max(a, float(px))
                if seg > 0:
                    cov[base + px // ss] += seg * inv
    return [min(1.0, v) for v in cov]


def rounded_square(size, radius_ratio=0.22):
    r = size * radius_ratio
    cov = [0.0] * (size * size)
    for y in range(size):
        for x in range(size):
            dx = max(r - (x + 0.5), (x + 0.5) - (size - r), 0.0)
            dy = max(r - (y + 0.5), (y + 0.5) - (size - r), 0.0)
            d = math.hypot(dx, dy)
            cov[y * size + x] = 1.0 if d <= r - 0.5 else (0.0 if d >= r + 0.5 else r + 0.5 - d)
    return cov


def write_png(path, size, rgba_rows):
    raw = b"".join(b"\x00" + bytes(row) for row in rgba_rows)

    def chunk(tag, data):
        c = tag + data
        return struct.pack(">I", len(data)) + c + struct.pack(">I", zlib.crc32(c))

    png = (b"\x89PNG\r\n\x1a\n"
           + chunk(b"IHDR", struct.pack(">IIBBBBB", size, size, 8, 6, 0, 0, 0))
           + chunk(b"IDAT", zlib.compress(raw, 9))
           + chunk(b"IEND", b""))
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "wb") as f:
        f.write(png)


def render_launcher_png(path, size):
    """ランチャーが見せる姿 (角丸マスク + 前景) を PNG にする。"""
    # 前景は 108 viewport のうち中心 72 が見える → 拡大率 108/72
    over = int(round(size * VIEWPORT / SAFE))
    over += over % 2
    fg = rasterize(foreground_contours(), over, VIEWPORT)
    off = (over - size) // 2
    mask = rounded_square(size)
    rows = []
    for y in range(size):
        row = bytearray()
        for x in range(size):
            m = mask[y * size + x]
            f = fg[(y + off) * over + (x + off)]
            r = ACCENT[0] + (FG[0] - ACCENT[0]) * f
            g = ACCENT[1] + (FG[1] - ACCENT[1]) * f
            b = ACCENT[2] + (FG[2] - ACCENT[2]) * f
            row += bytes((int(r), int(g), int(b), int(round(m * 255))))
        rows.append(row)
    write_png(path, size, rows)


# ====== VectorDrawable 出力 ==============================================

def path_data(contours):
    out = []
    for c in contours:
        out.append("M%.2f,%.2f" % c[0] + "".join("L%.2f,%.2f" % p for p in c[1:]) + "Z")
    return "".join(out)


VEC = """<?xml version="1.0" encoding="utf-8"?>
<!-- scripts/gen-icon.py が生成する。手で編集しない。 -->
<vector xmlns:android="http://schemas.android.com/apk/res/android"
    android:width="108dp"
    android:height="108dp"
    android:viewportWidth="108"
    android:viewportHeight="108">
    <path
        android:fillColor="%s"
        android:fillType="nonZero"
        android:pathData="%s" />
</vector>
"""

ADAPTIVE = """<?xml version="1.0" encoding="utf-8"?>
<!-- scripts/gen-icon.py が生成する。手で編集しない。 -->
<adaptive-icon xmlns:android="http://schemas.android.com/apk/res/android">
    <background android:drawable="@drawable/ic_launcher_background" />
    <foreground android:drawable="@drawable/ic_launcher_foreground" />
    <monochrome android:drawable="@drawable/ic_launcher_foreground" />
</adaptive-icon>
"""


def write_text(path, s):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w") as f:
        f.write(s)


def main():
    hexacc = "#FF%02X%02X%02X" % ACCENT
    write_text(os.path.join(RES, "drawable", "ic_launcher_foreground.xml"),
               VEC % ("#FFFFFFFF", path_data(foreground_contours())))
    write_text(os.path.join(RES, "drawable", "ic_launcher_background.xml"),
               VEC % (hexacc, "M0,0L108,0L108,108L0,108Z"))
    for name in ("ic_launcher.xml", "ic_launcher_round.xml"):
        write_text(os.path.join(RES, "mipmap-anydpi-v26", name), ADAPTIVE)
    for dpi, px in (("mdpi", 48), ("hdpi", 72), ("xhdpi", 96),
                    ("xxhdpi", 144), ("xxxhdpi", 192)):
        render_launcher_png(os.path.join(RES, f"mipmap-{dpi}", "ic_launcher.png"), px)
        print(f"  mipmap-{dpi}/ic_launcher.png ({px}px)")
    for loc in ("en-US", "ja"):
        p = os.path.join(FASTLANE, loc, "images", "icon.png")
        render_launcher_png(p, 512)
        print(f"  fastlane/{loc}/images/icon.png (512px)")
    print("アイコン生成完了")


if __name__ == "__main__":
    main()
