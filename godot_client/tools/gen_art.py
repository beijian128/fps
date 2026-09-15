#!/usr/bin/env python3
"""生成 ui/ 用到的简单美术资源（程序化，不依赖任何外部素材）。

为什么是程序化而不是 AI 出图：这个项目的美术本来就全是程序化的（`body_entity.gd` 搭模型、
`sfx.gd` 合成音效），界面图标又是「简单形状」这一类 —— 用脚本画出来的可复现、可 diff、
没有素材授权问题。换成 AI 图时只需要替换 `assets/ui/*.png` 同名文件，界面代码一行不用改。

运行：python godot_client/tools/gen_art.py
依赖：Pillow（`pip install pillow`）

所有图形都按 4 倍超采样绘制后再缩回目标尺寸 —— 直接按目标尺寸画多边形，边缘会全是锯齿，
而界面里这些图标本来就小，锯齿一眼就能看见。
"""

from __future__ import annotations

import os
from PIL import Image, ImageDraw, ImageFilter

SS = 4  # 超采样倍数

HERE = os.path.dirname(os.path.abspath(__file__))
OUT_DIR = os.path.normpath(os.path.join(HERE, "..", "assets", "ui"))

# 与 theme/tokens.gd 对齐的调色板（这里必须写死：出图脚本不属于运行时主题系统）
BG_DARK = (11, 13, 16)
BG_MID = (18, 21, 26)
GRID = (38, 44, 52)
ACCENT = (255, 176, 32)
STEEL = (139, 149, 163)
STEEL_DARK = (85, 96, 109)
SUCCESS = (78, 194, 122)
DANGER = (255, 95, 86)


def canvas(w: int, h: int, color=None) -> Image.Image:
    img = Image.new("RGBA", (w * SS, h * SS), color or (0, 0, 0, 0))
    return img


def down(img: Image.Image, w: int, h: int) -> Image.Image:
    return img.resize((w, h), Image.LANCZOS)


def save(img: Image.Image, name: str) -> None:
    os.makedirs(OUT_DIR, exist_ok=True)
    path = os.path.join(OUT_DIR, name)
    img.save(path, "PNG")
    print("wrote", os.path.relpath(path, os.path.join(HERE, "..", "..")))


# ---- 大厅背景 ----

def lobby_background(w: int = 1920, h: int = 1080) -> Image.Image:
    """近黑斜向渐变 + 细网格 + 一道琥珀斜光 + 四角压暗。

    它要能当所有界面的底：不能让画面中间太花（上面要叠卡片与文字），所以结构只用
    低频渐变与一格 32px 的网格，亮部集中在右上，左侧留给导航与卡片。
    """
    img = canvas(w, h)
    d = ImageDraw.Draw(img)
    W, H = w * SS, h * SS
    # 渐变：左上深、右下略亮
    for y in range(H):
        t = y / H
        c = tuple(int(BG_DARK[i] + (BG_MID[i] - BG_DARK[i]) * t) for i in range(3))
        d.line([(0, y), (W, y)], fill=(c[0], c[1], c[2], 255))
    # 网格：每 32px 一条，越靠右下越淡（对角遮罩）
    step = 32 * SS
    for x in range(0, W, step):
        d.line([(x, 0), (x, H)], fill=(*GRID, 40))
    for y in range(0, H, step):
        d.line([(0, y), (W, y)], fill=(*GRID, 40))
    # 斜光：一条宽琥珀带，透明度低到只当氛围
    glow = Image.new("RGBA", (W, H), (0, 0, 0, 0))
    gd = ImageDraw.Draw(glow)
    gd.polygon([(int(W * 0.62), 0), (int(W * 0.86), 0), (int(W * 0.44), H), (int(W * 0.20), H)],
               fill=(*ACCENT, 22))
    glow = glow.filter(ImageFilter.GaussianBlur(60 * SS))
    img = Image.alpha_composite(img, glow)
    # 暗角：把注意力压回中间
    vig = Image.new("L", (W, H), 0)
    vd = ImageDraw.Draw(vig)
    vd.ellipse([-W * 0.25, -H * 0.35, W * 1.25, H * 1.35], fill=210)
    vig = vig.filter(ImageFilter.GaussianBlur(120 * SS))
    shade = Image.new("RGBA", (W, H), (0, 0, 0, 255))
    img = Image.composite(img, Image.alpha_composite(img, shade), vig)
    return down(img, w, h)


# ---- 头像 ----

def avatar(w: int = 256, h: int = 256) -> Image.Image:
    """默认头像：头盔剪影 + 琥珀面罩条。

    刻意做成剪影而不是人脸：玩家名下的头像要能被 `self_modulate` 整块染色，
    有五官的图一染色就脏了。
    """
    img = canvas(w, h)
    d = ImageDraw.Draw(img)
    W, H = w * SS, h * SS
    # 肩
    d.polygon([(W * 0.08, H * 0.98), (W * 0.20, H * 0.74), (W * 0.80, H * 0.74), (W * 0.92, H * 0.98)],
              fill=(*STEEL_DARK, 255))
    # 头盔
    d.ellipse([W * 0.24, H * 0.16, W * 0.76, H * 0.72], fill=(*STEEL, 255))
    # 面罩
    d.rounded_rectangle([W * 0.30, H * 0.40, W * 0.70, H * 0.54], radius=int(W * 0.04),
                        fill=(*BG_DARK, 255))
    # 目镜
    d.rounded_rectangle([W * 0.34, H * 0.44, W * 0.66, H * 0.50], radius=int(W * 0.02),
                        fill=(*ACCENT, 255))
    # 天线
    d.line([(W * 0.74, H * 0.30), (W * 0.86, H * 0.16)], fill=(*STEEL_DARK, 255), width=int(W * 0.02))
    return down(img, w, h)


# ---- 道具图标 ----

def _icon_base(w: int, h: int) -> tuple[Image.Image, ImageDraw.ImageDraw]:
    img = canvas(w, h)
    return img, ImageDraw.Draw(img)


def item_rifle(w: int = 256, h: int = 128) -> Image.Image:
    img, d = _icon_base(w, h)
    W, H = w * SS, h * SS
    body = (38, 54, 74)          # 枪身散热片：比钢色暗一档
    d.polygon([(W * 0.06, H * 0.46), (W * 0.86, H * 0.42), (W * 0.94, H * 0.50),
               (W * 0.86, H * 0.56), (W * 0.06, H * 0.54)], fill=(*STEEL, 255))
    d.rectangle([W * 0.28, H * 0.36, W * 0.60, H * 0.46], fill=(*body, 255))       # 导轨
    d.rectangle([W * 0.40, H * 0.56, W * 0.52, H * 0.80], fill=(*STEEL_DARK, 255))  # 弹匣
    d.polygon([(W * 0.10, H * 0.54), (W * 0.26, H * 0.54), (W * 0.22, H * 0.86),
               (W * 0.08, H * 0.86)], fill=(*STEEL_DARK, 255))                      # 握把
    d.polygon([(W * 0.58, H * 0.34), (W * 0.68, H * 0.34), (W * 0.66, H * 0.44),
               (W * 0.56, H * 0.44)], fill=(*ACCENT, 255))                          # 准星
    return down(img, w, h)


def item_pistol(w: int = 192, h: int = 128) -> Image.Image:
    img, d = _icon_base(w, h)
    W, H = w * SS, h * SS
    d.polygon([(W * 0.10, H * 0.34), (W * 0.88, H * 0.34), (W * 0.88, H * 0.52),
               (W * 0.20, H * 0.52)], fill=(*STEEL, 255))
    d.polygon([(W * 0.26, H * 0.52), (W * 0.46, H * 0.52), (W * 0.40, H * 0.92),
               (W * 0.20, H * 0.92)], fill=(*STEEL_DARK, 255))
    d.line([(W * 0.30, H * 0.56), (W * 0.42, H * 0.88)], fill=(*BG_DARK, 255), width=int(W * 0.02))
    d.polygon([(W * 0.80, H * 0.24), (W * 0.86, H * 0.24), (W * 0.86, H * 0.34),
               (W * 0.80, H * 0.34)], fill=(*ACCENT, 255))
    return down(img, w, h)


def item_shotgun(w: int = 256, h: int = 128) -> Image.Image:
    img, d = _icon_base(w, h)
    W, H = w * SS, h * SS
    d.rectangle([W * 0.06, H * 0.42, W * 0.92, H * 0.54], fill=(*STEEL, 255))
    d.rectangle([W * 0.06, H * 0.55, W * 0.55, H * 0.63], fill=(*STEEL_DARK, 255))   # 泵
    d.polygon([(W * 0.30, H * 0.63), (W * 0.46, H * 0.63), (W * 0.34, H * 0.92),
               (W * 0.16, H * 0.92)], fill=(*STEEL_DARK, 255))                        # 枪托
    d.rectangle([W * 0.52, H * 0.63, W * 0.62, H * 0.78], fill=(*STEEL, 255))         # 握把
    d.rectangle([W * 0.86, H * 0.38, W * 0.92, H * 0.46], fill=(*ACCENT, 255))        # 枪口
    return down(img, w, h)


def item_medkit(w: int = 192, h: int = 128) -> Image.Image:
    img, d = _icon_base(w, h)
    W, H = w * SS, h * SS
    d.rounded_rectangle([W * 0.10, H * 0.28, W * 0.90, H * 0.92], radius=int(W * 0.04),
                        fill=(*STEEL_DARK, 255))
    d.rounded_rectangle([W * 0.10, H * 0.28, W * 0.90, H * 0.44], radius=int(W * 0.03),
                        fill=(*STEEL, 255))
    d.rectangle([W * 0.44, H * 0.50, W * 0.56, H * 0.82], fill=(*SUCCESS, 255))
    d.rectangle([W * 0.30, H * 0.60, W * 0.70, H * 0.72], fill=(*SUCCESS, 255))
    d.rectangle([W * 0.42, H * 0.18, W * 0.58, H * 0.28], fill=(*ACCENT, 255))        # 提手
    return down(img, w, h)


def item_bag(w: int = 192, h: int = 160) -> Image.Image:
    """背包入口图标：主包体 + 两个侧袋 + 顶盖扣带。"""
    img, d = _icon_base(w, h)
    W, H = w * SS, h * SS
    d.rounded_rectangle([W * 0.22, H * 0.26, W * 0.78, H * 0.94], radius=int(W * 0.06),
                        fill=(*STEEL_DARK, 255))
    d.rounded_rectangle([W * 0.06, H * 0.44, W * 0.24, H * 0.86], radius=int(W * 0.03),
                        fill=(*STEEL, 255))
    d.rounded_rectangle([W * 0.76, H * 0.44, W * 0.94, H * 0.86], radius=int(W * 0.03),
                        fill=(*STEEL, 255))
    d.rounded_rectangle([W * 0.30, H * 0.16, W * 0.70, H * 0.36], radius=int(W * 0.04),
                        fill=(*STEEL, 255))
    d.rectangle([W * 0.46, H * 0.40, W * 0.54, H * 0.70], fill=(*ACCENT, 255))
    d.rectangle([W * 0.36, H * 0.50, W * 0.64, H * 0.60], fill=(*ACCENT, 255))
    return down(img, w, h)


def logo_mark(w: int = 64, h: int = 64) -> Image.Image:
    """顶栏品牌标记：琥珀色双箭头（推进方向），比一个圆点更像战术界面。"""
    img, d = _icon_base(w, h)
    W, H = w * SS, h * SS
    d.polygon([(W * 0.12, H * 0.20), (W * 0.50, H * 0.50), (W * 0.12, H * 0.80),
               (W * 0.12, H * 0.60), (W * 0.32, H * 0.50), (W * 0.12, H * 0.40)],
              fill=(*ACCENT, 255))
    d.polygon([(W * 0.46, H * 0.20), (W * 0.84, H * 0.50), (W * 0.46, H * 0.80),
               (W * 0.46, H * 0.60), (W * 0.66, H * 0.50), (W * 0.46, H * 0.40)],
              fill=(*ACCENT, 160))
    return down(img, w, h)


def crosshair_marker(w: int = 64, h: int = 64) -> Image.Image:
    """大厅/结算页角落用的准星装饰标记（比纯文字更贴题）。"""
    img, d = _icon_base(w, h)
    W, H = w * SS, h * SS
    c = W / 2
    gap, arm, wid = W * 0.10, W * 0.24, int(W * 0.04)
    col = (*STEEL_DARK, 255)
    d.line([(c - gap - arm, c), (c - gap, c)], fill=col, width=wid)
    d.line([(c + gap, c), (c + gap + arm, c)], fill=col, width=wid)
    d.line([(c, c - gap - arm), (c, c - gap)], fill=col, width=wid)
    d.line([(c, c + gap), (c, c + gap + arm)], fill=col, width=wid)
    d.ellipse([c - wid, c - wid, c + wid, c + wid], fill=(*DANGER, 255))
    return down(img, w, h)


def main() -> None:
    save(lobby_background(), "bg_lobby.png")
    save(avatar(), "avatar_default.png")
    save(item_rifle(), "item_rifle.png")
    save(item_pistol(), "item_pistol.png")
    save(item_shotgun(), "item_shotgun.png")
    save(item_medkit(), "item_medkit.png")
    save(item_bag(), "item_bag.png")
    save(logo_mark(), "logo_mark.png")
    save(crosshair_marker(), "crosshair_mark.png")


if __name__ == "__main__":
    main()
