# 安装墨羽Canvas

安装包不使用付费代码签名证书。Windows 可能显示“未知发布者”或 SmartScreen 提示；macOS 可能要求在“系统设置 → 隐私与安全性”中允许打开。受组织策略或 Smart App Control 限制的设备可能无法运行。请从 [GitHub Releases](https://github.com/glanderness/BeefTV/releases/latest) 下载。

- Windows：运行 `VideoCanvas-vX.Y.Z-windows-amd64-setup.exe`。打开后直接确认安装位置即可一键安装，默认 `%LOCALAPPDATA%\Programs\墨羽Canvas`，覆盖安装会沿用上次的位置；新安装使用“墨羽Canvas”子目录，原有 VideoCanvas / BeefTV 安装目录仅用于原地升级。安装时会自动关闭正在运行的旧版应用，默认创建开始菜单与桌面快捷方式，完成后可直接勾选启动。安装包以当前用户权限运行，无需管理员权限，因此不能写入 `Program Files` 等位置。WebView2 是运行依赖，缺失时应用会提示下载安装。
- macOS：打开对应芯片的 `.dmg`，将 `VideoCanvas.app` 拖到“Applications”，退出磁盘映像后从“应用程序”启动。已有版本时先退出墨羽Canvas，再替换应用。
- Ubuntu 24.04 x64：下载 `VideoCanvas-vX.Y.Z-linux-amd64.deb` 后运行 `sudo apt install ./VideoCanvas-vX.Y.Z-linux-amd64.deb`，从应用菜单启动墨羽Canvas。下载新版 DEB 并再次安装即可升级；DEB 不使用应用内自动更新。

ZIP 仍供便携使用和应用内更新。Windows/macOS 安装后可继续使用应用内更新。旧 ZIP 用户先退出应用，安装新包后用同一系统账户启动；确认旧项目和素材正常，再删除旧程序目录。

应用内更新前会先保存工作区。如果保存失败，可以选择“返回处理”“重试保存”或“仍然更新”。“仍然更新”会关闭并重启应用，尚未保存的修改可能丢失；仍有写入进行时，需要等待保存结束后再更新。

Windows 覆盖安装时如果提示旧程序文件夹被其他程序占用，请关闭正在使用该目录的终端或外部程序，再点击“重试”。仍然失败可以取消安装；如果提示原程序未能恢复，请保留提示中的备份文件夹，不要手动删除。每次安装的日志位于 `%LOCALAPPDATA%\墨羽Canvas\Installer\*.log`，可将本次日志交给支持人员，帮助确认失败的步骤和 Windows 错误码。日志包含本机安装路径，请只发给可信的支持人员。

新安装的项目、草稿、数据库和设置默认存放在独立的用户目录：Windows `%AppData%\MoyuCanvas`；macOS `~/Library/Application Support/MoyuCanvas`；Linux `~/.config/MoyuCanvas`（遵循 `XDG_CONFIG_HOME`）。本地图片、视频、音频等素材默认放在该目录的 `resources` 子目录，也可在桌面端“存储设置”中迁移到其他磁盘；素材目录与数据库目录可分开。已有工作区目录 `VideoCanvas` 或 `BeefTV` 会在新目录不存在时沿用，不会自动搬移。安装、覆盖安装和卸载不删除这些目录。Windows 的 WebView2 缓存 `%AppData%\VideoCanvas.exe` 也会保留。

如果曾通过 `CANVAS_DESKTOP_DATA_DIR` 或 `--data-dir` 指定目录，安装后仍需使用同一目录；不要把工作数据放进程序安装目录。安装包不会搬迁自定义目录，也不会自动找回已经丢失的文件。
