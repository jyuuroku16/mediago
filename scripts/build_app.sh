#!/bin/bash
set -e

PROJECT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
APP_NAME="MediaGo"
BUNDLE_DIR="$PROJECT_DIR/$APP_NAME.app"
MACOS_DIR="$BUNDLE_DIR/Contents/MacOS"
RESOURCES_DIR="$BUNDLE_DIR/Contents/Resources"

echo "=== 正在构建 $APP_NAME.app ==="

# 1. 清理并创建目录结构
rm -rf "$BUNDLE_DIR"
mkdir -p "$MACOS_DIR"
mkdir -p "$RESOURCES_DIR"

# 拷贝 macOS App 图标
if [ -f "$PROJECT_DIR/assets/AppIcon.icns" ]; then
    echo "-> 拷贝 AppIcon.icns 到 Resources..."
    cp "$PROJECT_DIR/assets/AppIcon.icns" "$RESOURCES_DIR/AppIcon.icns"
fi

# 2. 编译 Go 二进制
echo "-> 编译 Go 可执行程序..."
cd "$PROJECT_DIR"
go build -ldflags="-s -w" -o "$MACOS_DIR/$APP_NAME" ./cmd/mediago
chmod +x "$MACOS_DIR/$APP_NAME"

# 3. 生成 Info.plist
echo "-> 写入 Info.plist..."
cat << 'EOF' > "$BUNDLE_DIR/Contents/Info.plist"
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>CFBundleExecutable</key>
    <string>MediaGo</string>
    <key>CFBundleIconFile</key>
    <string>AppIcon</string>
    <key>CFBundleIdentifier</key>
    <string>com.mediago.macos</string>
    <key>CFBundleName</key>
    <string>MediaGo</string>
    <key>CFBundleDisplayName</key>
    <string>MediaGo</string>
    <key>CFBundlePackageType</key>
    <string>APPL</string>
    <key>CFBundleShortVersionString</key>
    <string>1.0.0</string>
    <key>CFBundleVersion</key>
    <string>1</string>
    <key>LSMinimumSystemVersion</key>
    <string>12.0</string>
    <key>NSHighResolutionCapable</key>
    <true/>
    <key>NSSupportsAutomaticGraphicsSwitching</key>
    <true/>
    <key>NSAppTransportSecurity</key>
    <dict>
        <key>NSAllowsArbitraryLoads</key>
        <true/>
    </dict>
</dict>
</plist>
EOF

# 4. 执行本地 ad-hoc 签名
echo "-> 执行 codesign 本地签名..."
codesign --force --deep --sign - "$BUNDLE_DIR"

echo "=== 构建完成: $BUNDLE_DIR ==="
