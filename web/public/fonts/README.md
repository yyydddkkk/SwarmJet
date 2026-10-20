# 离线字体

所有字体随前端打包，无需 Google Fonts CSS 或外网服务。字体来自 Google Fonts 与 IBM 的官方仓库，固定提交、下载地址和输入 SHA-256 见 [sources.json](sources.json)。每个 family 的 SIL Open Font License 1.1 在相应 `*-OFL.txt`。

Noto Serif SC / Noto Sans SC 子集包含当前 `web/src/**/*.{ts,tsx,css}` 中全部实际字符，GB/T 2312-1980 一级汉字3755字（Python标准 `gb2312` 编解码器，B0A1..D7F9区位，末尾未分配区位跳过），基本拉丁、常用中文及全角标点。罕见仓库名、消息正文仍可使用系统回退字。

Noto / Source Serif 4 限制到400..500字重，其他轴取默认。JetBrains Mono为400实例。IM Fell English保留正体与斜体。IBM 的许可证保留字体名 Plex，所以 **IBM Plex Sans采用官方完整静态WOFF2，原样复制、未经子集或实例化**，保留原家族名；正体和斜体各含400/500。CSS声明的字重和文件实际字重一致。没有新增前端运行时依赖。

生成命令（仓库根目录）：

```sh
uv run --with fonttools==4.67.0 --with brotli==1.2.0 python web/scripts/subset-fonts.py
```

脚本会在 `/tmp/swarmjet-font-sources/` 缓存原始TTF，校验已记录的SHA-256，按固定仓库提交取源；原TTF与中间文件不提交、不进入public。新增中文界面文字后重新生成即可。字体授权原文和源清单应随产物分发。

| 文件 | 字节数 |
|---|---:|
| `IBMPlexSans-Italic.woff2` | 67,220 |
| `IBMPlexSans-Medium.woff2` | 66,740 |
| `IBMPlexSans-MediumItalic.woff2` | 71,204 |
| `IBMPlexSans-Regular.woff2` | 63,020 |
| `IMFeENit28P.woff2` | 66,276 |
| `IMFeENrm28P.woff2` | 65,200 |
| `JetBrainsMono[wght].woff2` | 20,700 |
| `NotoSansSC[wght].woff2` | 912,848 |
| `NotoSerifSC[wght].woff2` | 1,166,372 |
| `SourceSerif4-Italic[opsz,wght].woff2` | 36,860 |
| `SourceSerif4[opsz,wght].woff2` | 35,984 |

总WOFF2：2,572,424 字节。
