# Linux 安装

Linux x64 包面向 Ubuntu 24.04，使用 GTK 3 和 WebKitGTK 4.1。其他发行版尚未完成运行验证；ARM64 不在当前发行范围内。

推荐下载 `VideoCanvas-vX.Y.Z-linux-amd64.deb`，安装时会自动补齐依赖：

```bash
sudo apt install ./VideoCanvas-vX.Y.Z-linux-amd64.deb
```

安装完成后从应用菜单启动墨羽Canvas。新版仍用上述命令安装；卸载使用 `sudo apt remove videocanvas`，不会删除用户数据。DEB 由包管理器管理，不使用应用内自动更新。

使用便携 ZIP 时，先安装运行依赖：

```bash
sudo apt-get update
sudo apt-get install -y libgtk-3-0t64 libwebkit2gtk-4.1-0 gstreamer1.0-plugins-good gstreamer1.0-plugins-bad gstreamer1.0-libav ffmpeg fonts-noto-cjk unzip
```

下载 `VideoCanvas-vX.Y.Z-linux-amd64.zip`，解压到当前用户可写的目录，然后运行：

```bash
unzip VideoCanvas-vX.Y.Z-linux-amd64.zip
./VideoCanvas-linux/VideoCanvas
```

保留整个 `VideoCanvas-linux` 目录及其名称。内置 Node、Agent、官方插件和兼容 CLI `cli/beeftv` 都在这个目录中，不需要安装全局 Node。自动更新会整组替换程序文件，并保留失败时的恢复副本，因此安装目录的父目录也需要当前用户的写入权限。

新安装的工作数据默认放在 `~/.config/MoyuCanvas`（设置 `XDG_CONFIG_HOME` 时遵循该目录）；已有 `~/.config/VideoCanvas` 或 `~/.config/BeefTV` 工作区会在新目录不存在时沿用。不要将工作数据放在程序目录中。外部 Agent 可以通过随包 `VideoCanvas-linux/cli/beeftv` 接入。

本包需要图形桌面会话。无图形桌面的服务器请使用独立的后端部署方式。
