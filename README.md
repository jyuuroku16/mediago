# MediaGo (Mac 视频资源发现与下载管理器)

MediaGo 是一款专为 macOS 设计的现代化桌面视频资源发现、下载与媒体库管理工具。主程序基于 **Go + MyGo 原生 Metal GPU UI** 打造，具备极低系统资源占用、毫秒级启动与流畅的交互体验；内置专用 **WKWebView 浏览器** 支持官方登录态保留与媒体请求自动嗅探。

---

## 核心架构与功能

1. **双窗口原生架构**：
   - **主窗口（MyGo Native UI）**：Metal GPU 渲染原生侧栏、下载任务表格、实时速度与进度、Finder / Quick Look 联动。
   - **专用浏览器（WKWebView）**：官方网页登录态持久化、DOM 与资源定时嗅探，候选媒体自动推送至主窗口。
2. **两级并发下载引擎**：
   - **HTTP Range 分段引擎**：纯 Go 实现，支持 `206 Partial Content` 自动探测、分段并发下载、断点续传与 EWMA 平滑测速。
   - **HLS 流媒体引擎**：Master/Media Playlist 变体流解析、多分片并发拉取、调用本地 `ffmpeg` 无损 remux 封装为 MP4。
   - **两级并发控制**：控制任务级最大并发数与单域名并发限制，内置指数退避重试，防 429 限流。
3. **媒体发现与解析**：
   - URL 规范化过滤各种分享/追踪参数。
   - 受控调用 `yt-dlp` 解析各视频站点候选规格（清晰度、格式、编码、预估大小）。
4. **数据持久化与媒体库**：
   - 零 cgo 的纯 Go SQLite（启用 WAL 模式），保证线程安全与自愈迁移。
   - 默认存储至 `~/Movies/MediaGo/`，支持状态过滤、搜索、Finder 定位与废纸篓安全删除。

---

## 项目结构

```text
MediaGo/
├── MediaGo.app/          # 最终构建产物（已完成 codesign 本地签名的 macOS App）
├── cmd/
│   └── mediago/
│       └── main.go       # 应用程序入口
├── internal/
│   ├── browser/          # WKWebView 专用浏览器与嗅探逻辑
│   ├── discovery/        # URL 规范化与 yt-dlp 视频解析器
│   ├── engine/           # HTTP Range 分段引擎、HLS 引擎与两级并发调度器
│   ├── library/          # 本地媒体库管理（Finder、Quick Look、废纸篓）
│   ├── model/            # 核心数据模型（Task, Candidate, Asset, Status）
│   ├── storage/          # SQLite WAL 模式存储与 Schema 迁移
│   ├── testmedia/        # 本地确定性测试 HTTP 服务（Range, 429, m3u8）
│   └── ui/               # 基于 MyGo Native UI 的主窗口交互与渲染
├── scripts/
│   └── build_app.sh      # 一键编译并打包生成 MediaGo.app
└── tests/
    └── e2e_test.go       # 端到端生命周期自动化集成测试
```

---

## 测试与构建

### 1. 运行全部自动化测试
```bash
go test -v ./...
```

### 2. 一键打包 macOS 原生 App
```bash
./scripts/build_app.sh
```
构建成功后将在项目根目录下生成 `MediaGo.app`。

### 3. 运行应用
可以通过 Finder 双击 `MediaGo.app` 启动，或在终端执行：
```bash
open MediaGo.app
```
或直接运行二进制：
```bash
./MediaGo.app/Contents/MacOS/MediaGo
```
