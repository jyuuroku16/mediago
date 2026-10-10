#!/usr/bin/env python3
"""
生成 MediaGo 生产级 macOS 原生应用图标 (方案 A：极简纯色实心下行流)
包括: assets/icon.png, assets/AppIcon.icns, 以及应用包 MediaGo.app/Contents/Resources/AppIcon.icns
"""
import os
import shutil
import subprocess
from PIL import Image

PROJECT_DIR = "/Users/wuzhiying/Personal/MediaGo"
ASSETS_DIR = os.path.join(PROJECT_DIR, "assets")
APP_RESOURCES_DIR = os.path.join(PROJECT_DIR, "MediaGo.app/Contents/Resources")
os.makedirs(ASSETS_DIR, exist_ok=True)
os.makedirs(APP_RESOURCES_DIR, exist_ok=True)

# 方案 A 矢量定义 (采用与系统原生 Accent 蓝 #0071E3 严格一致的纯实色)
svg_content = """<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1024 1024" width="1024" height="1024">
  <defs>
    <filter id="shadow" x="-10%" y="-10%" width="120%" height="125%">
      <feDropShadow dx="0" dy="16" stdDeviation="20" flood-color="#000000" flood-opacity="0.26"/>
    </filter>
  </defs>

  <!-- 100% 纯色实心苹果系统蓝底板 -->
  <rect x="100" y="100" width="824" height="824" rx="185" fill="#0071E3" filter="url(#shadow)"/>

  <!-- 100% 纯白实心主图形 (无渐变、无噪点、平滑圆角几何) -->
  <g fill="#FFFFFF">
    <!-- 箭头立轴 -->
    <rect x="466" y="270" width="92" height="230" rx="24" />
    
    <!-- 箭头大三角形 (圆润、厚实、现代力量感) -->
    <path d="M 320 480 
             C 304 480, 296 498, 308 510 
             L 498 700 
             C 506 708, 518 708, 526 700 
             L 716 510 
             C 728 498, 720 480, 704 480 
             Z" />

    <!-- 底部实心圆角承托槽 -->
    <rect x="300" y="746" width="424" height="42" rx="21" />
  </g>
</svg>"""

def main():
    temp_dir = "/tmp/mediago_prod_icon"
    os.makedirs(temp_dir, exist_ok=True)
    temp_svg = os.path.join(temp_dir, "icon.svg")

    with open(temp_svg, "w") as f:
        f.write(svg_content)

    # 1. 使用 QuickLook 栅格化为原生 1024x1024 PNG
    subprocess.run(["qlmanage", "-t", "-s", "1024", "-o", temp_dir, temp_svg], check=True, capture_output=True)
    gen_png = os.path.join(temp_dir, "icon.svg.png")
    
    im = Image.open(gen_png).convert("RGBA")
    if im.size != (1024, 1024):
        im = im.resize((1024, 1024), Image.Resampling.LANCZOS)

    # 保存 1024x1024 主图
    target_png = os.path.join(ASSETS_DIR, "icon.png")
    im.save(target_png)
    print("✓ 已生成高清主图:", target_png)

    # 2. 生成 macOS 标准 iconset
    iconset_dir = os.path.join(temp_dir, "AppIcon.iconset")
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
        resized = im.resize((sz, sz), Image.Resampling.LANCZOS)
        resized.save(os.path.join(iconset_dir, name))

    # 3. 调用 iconutil 生成 icns
    target_icns = os.path.join(ASSETS_DIR, "AppIcon.icns")
    res = subprocess.run(["iconutil", "-c", "icns", iconset_dir, "-o", target_icns], capture_output=True, text=True)
    if res.returncode == 0:
        print("✓ 已生成 macOS 原生图标包:", target_icns)
    else:
        print("x 生成 icns 失败:", res.stderr)
        return

    # 4. 同步至 MediaGo.app/Contents/Resources
    app_icns = os.path.join(APP_RESOURCES_DIR, "AppIcon.icns")
    shutil.copyfile(target_icns, app_icns)
    print("✓ 已同步至 App Bundle:", app_icns)

    # 同时复制一份到 brain 目录作为 artifact 凭证
    brain_dir = "/Users/wuzhiying/.gemini/antigravity/brain/3bc56233-2663-47e0-b66c-c027147203f9"
    shutil.copyfile(target_png, os.path.join(brain_dir, "mediago_final_icon.png"))
    print("✓ 已归档至 Brain 目录: mediago_final_icon.png")

if __name__ == "__main__":
    main()
