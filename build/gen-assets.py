#!/usr/bin/env python3
"""ブランド素材（ファビコン・OGP画像）を web/src/ へ生成する。

素材はデザインの一部なので、バイナリを直接いじるのではなく
このスクリプトを直して再生成する。サイトの配色（style.css の
--primary / --background / --foreground）と、ヘッダーの虫眼鏡
アイコン（index.html の brand-icon）に揃えてある。

必要なもの: Pillow と日本語フォント（IPAゴシック）。
  sudo apt install fonts-ipafont-gothic && pip install Pillow
手元PCでのみ実行する。VPS では動かさない（生成物を deploy.sh が運ぶ）。

  python3 build/gen-assets.py
"""

import os
from PIL import Image, ImageDraw, ImageFont

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
OUT = os.path.join(ROOT, "web", "src")

# style.css の :root と同じ値
PRIMARY = (0, 113, 227)
BACKGROUND = (242, 242, 245)
FOREGROUND = (29, 29, 31)
QUIET = (110, 110, 115)
CARD = (255, 255, 255)

FONT_PATH = "/usr/share/fonts/opentype/ipafont-gothic/ipagp.ttf"
SS = 4  # スーパーサンプリング倍率（アンチエイリアス目的）


def font(size):
    return ImageFont.truetype(FONT_PATH, size)


def magnifier(d, x, y, size, color):
    """虫眼鏡アイコンを (x, y) を左上とする size 四方に描く。
    index.html の brand-icon（circle cx=11 cy=11 r=7 / 線 20,20→16.2,16.2）と同じ形。"""
    s = size / 24.0
    w = max(1, round(2.2 * s))
    cx, cy, r = x + 11 * s, y + 11 * s, 7 * s
    d.ellipse([cx - r, cy - r, cx + r, cy + r], outline=color, width=w)
    d.line([x + 16.2 * s, y + 16.2 * s, x + 20 * s, y + 20 * s], fill=color, width=w)
    # 角丸の代わりに端点へ円を置く（Pillow の line には線端指定がない）
    for px, py in ((x + 16.2 * s, y + 16.2 * s), (x + 20 * s, y + 20 * s)):
        d.ellipse([px - w / 2, py - w / 2, px + w / 2, py + w / 2], fill=color)


def app_icon(size):
    """角丸の青地に白い虫眼鏡。ファビコン・apple-touch-icon 共通の図案。"""
    n = size * SS
    img = Image.new("RGBA", (n, n), (0, 0, 0, 0))
    d = ImageDraw.Draw(img)
    d.rounded_rectangle([0, 0, n - 1, n - 1], radius=int(n * 0.22), fill=PRIMARY)
    pad = n * 0.19
    magnifier(d, pad, pad, n - pad * 2, CARD)
    return img.resize((size, size), Image.LANCZOS)


def build_icons():
    # ICO は 16/32/48 を1ファイルに束ねる（ブラウザとWindowsが使い分ける）
    ico = app_icon(256)
    ico.save(os.path.join(OUT, "favicon.ico"),
             sizes=[(16, 16), (32, 32), (48, 48)])
    app_icon(180).save(os.path.join(OUT, "apple-touch-icon.png"))
    print("  favicon.ico / apple-touch-icon.png")


def build_og():
    """1200x630 の OGP 画像。SNS のタイムライン上では縮小されるので、
    文字は大きく・要素は少なくする。"""
    W, H = 1200 * SS, 630 * SS
    img = Image.new("RGB", (W, H), BACKGROUND)
    d = ImageDraw.Draw(img)

    # 中央のカード（サイト本体の .card と同じ見た目）
    m = 48 * SS
    d.rounded_rectangle([m, m, W - m, H - m], radius=28 * SS, fill=CARD,
                        outline=(226, 226, 230), width=2 * SS)

    icon = 132 * SS
    ix, iy = 96 * SS, 150 * SS
    d.rounded_rectangle([ix, iy, ix + icon, iy + icon], radius=int(icon * 0.22), fill=PRIMARY)
    magnifier(d, ix + icon * 0.19, iy + icon * 0.19, icon * 0.62, CARD)

    tx = ix + icon + 46 * SS
    d.text((tx, 152 * SS), "KakologInfo", font=font(86 * SS), fill=FOREGROUND,
           stroke_width=SS, stroke_fill=FOREGROUND)
    d.text((tx, 258 * SS), "5ch（2ch）過去ログ スレタイ検索", font=font(46 * SS), fill=QUIET)

    d.line([96 * SS, 380 * SS, W - 96 * SS, 380 * SS], fill=(232, 232, 236), width=2 * SS)
    d.text((96 * SS, 424 * SS), "1.3億件超のスレッドタイトルを、", font=font(50 * SS), fill=FOREGROUND)
    d.text((96 * SS, 492 * SS), "板・期間で絞り込んで検索", font=font(50 * SS), fill=FOREGROUND)

    d.text((W - 96 * SS, 500 * SS), "kakolog.info", font=font(42 * SS), fill=PRIMARY, anchor="rs")

    img.resize((1200, 630), Image.LANCZOS).save(os.path.join(OUT, "og.png"), optimize=True)
    print("  og.png")


if __name__ == "__main__":
    if not os.path.exists(FONT_PATH):
        raise SystemExit("日本語フォントが見つかりません: " + FONT_PATH)
    print("生成先:", OUT)
    build_icons()
    build_og()
