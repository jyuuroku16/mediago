#!/usr/bin/env python3
"""
生成 MediaGo macOS AppIcon.icns 与 assets/icon.png
"""
import os
import subprocess
from PIL import Image, ImageDraw, ImageFilter

SCRIPT_DIR = os.path.dirname(os.path.abspath(__file__))
PROJECT_DIR = os.path.dirname(SCRIPT_DIR)
ASSETS_DIR = os.path.join(PROJECT_DIR, "assets")
os.makedirs(ASSETS_DIR, exist_ok=True)

def build_icon(src_image_path):
    im = Image.open(src_image_path).convert("RGBA")
    
    # 裁剪中心 squircle 区域
    crop_box = (150, 150, 874, 874)
    cropped = im.crop(crop_box).resize((824, 824), Image.Resampling.LANCZOS)

    # 1024x1024 透明画布
    canvas = Image.new("RGBA", (1024, 1024), (0, 0, 0, 0))

    # macOS squircle 遮罩
    mask = Image.new("L", (824, 824), 0)
    draw = ImageDraw.Draw(mask)
    draw.rounded_rectangle([(0, 0), (824, 824)], radius=185, fill=255)

    # 柔和底部阴影
    shadow = Image.new("RGBA", (1024, 1024), (0, 0, 0, 0))
    shadow_mask = Image.new("L", (824, 824), 0)
    sdraw = ImageDraw.Draw(shadow_mask)
    sdraw.rounded_rectangle([(0, 0), (824, 824)], radius=185, fill=110)
    shadow.paste((0, 0, 0, 110), (100, 112), mask=shadow_mask)
    shadow = shadow.filter(ImageFilter.GaussianBlur(16))

    canvas = Image.alpha_composite(canvas, shadow)
    canvas.paste(cropped, (100, 100), mask=mask)

    out_png = os.path.join(ASSETS_DIR, "icon.png")
    canvas.save(out_png)
    print("✓ 已生成 1024x1024 图标:", out_png)

    # 生成 iconset 与 icns
    iconset_dir = "/tmp/AppIcon.iconset"
    os.makedirs(iconset_dir, exist_ok=True)

    sizes = [
        (16, "icon_16x16.png"),
        (32, "icon_16x16@2x.png"),
        (32, "icon_32x32.png"),
        (64, "icon_32x32@2x.png"),
        (128, "icon_128x128.png"),
        (256, "icon_128x128@2x.png"),
        (256, "icon_256x256.png"),
        (512, "icon_256x256@2x.png"),
        (512, "icon_512x512.png"),
        (1024, "icon_512x512@2x.png")
    ]

    for sz, name in sizes:
        resized = canvas.resize((sz, sz), Image.Resampling.LANCZOS)
        resized.save(os.path.join(iconset_dir, name))

    out_icns = os.path.join(ASSETS_DIR, "AppIcon.icns")
    res = subprocess.run(["iconutil", "-c", "icns", iconset_dir, "-o", out_icns], capture_output=True, text=True)
    if res.returncode == 0:
        print("✓ 已生成 macOS 原生图标包:", out_icns)
    else:
        print("x 生成 icns 失败:", res.stderr)

if __name__ == "__main__":
    src = "/Users/wuzhiying/.gemini/antigravity/brain/3bc56233-2663-47e0-b66c-c027147203f9/mediago_app_icon_1791647875819.jpg"
    build_icon(src)
