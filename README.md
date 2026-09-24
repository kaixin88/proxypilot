# ProxyPilot · 一键代理控制台

> Windows 10 / 11 通用的图形化代理工具。**开箱即用、零配置**：
> 自动获取云端节点、自动剔除失效节点、一键开关系统代理，
> 支持「全局 / 智能分流 / 自定义规则」三种模式。
> 程序为**独立原生窗口**，不依赖任何外部浏览器。

本项目参考 `Chrome153_AllNew_2026.9.12` 项目中的节点更新机制实现：

| 原项目文件 | 机制 |
|---|---|
| `Xray\ip_Update\ip_N.bat` | 从云端拉取第 N 组节点配置，失败则切换备用镜像 |
| `clash.meta\ip_Update\ip_N.bat` | 同上，拉取 Clash.Meta 的 `config.yaml` |
| `hysteria / hysteria2 / singbox / juicity / mieru / naiveproxy` | 各自的 ip 分组配置 |

原项目每个脚本都用 `wget` 依次尝试两个镜像：

- 主源 `https://gitlab.com/free9999/ipupdate/-/raw/master/backup/img/1/2/ipp/<dir>/<n>/<file>`
- 备用 `https://www.67867867.xyz/Alvin9999/PAC/refs/heads/master/backup/img/1/2/ipp/<dir>/<n>/<file>`

ProxyPilot 把这些「ip 更新源」**全部并发抓取**（共 26 个来源），**跨源合并去重**，
因此能一次性拿到全部协议的全部可用节点，而不是像原脚本那样只能选一组。

> 注意：远端目录名与本地目录名并不完全一致（本地是 `clash.meta\`，远端是 `clash.meta2\`），
> 程序内已按远端实际路径配置。

---

## 功能

### 1. 自动获取节点
- 并发抓取全部云端源（每源含主源 + 2 个备用镜像），任一可用即成功。
- 解析 8 种配置格式：Clash.Meta YAML、Xray JSON、sing-box JSON、
  hysteria JSON、hysteria2 JSON、juicity JSON、naiveproxy JSON、mieru JSON。
- 按「协议 + 服务器 + 端口」指纹去重合并。
- **上次的节点会被保留**：某次抓取失败也不会把已有节点清空。
- **协议感知探测**：TCP 类节点测 TCP 握手时延；QUIC/UDP 类节点（hysteria 等）
  改用 UDP 可达性探测，避免用 TCP 误判成不可用。
- 不可用的节点自动剔除，可用节点按延迟从低到高排序。

### 2. 一键开关代理
- 点击「开启代理」：启动代理内核 → 设置 Windows 系统代理。
- 点击「关闭代理」：停止内核 → **精确还原**你原来的系统代理设置（含原本的值与开关状态）。
- 退出程序时会自动关闭代理并还原，不留残留。

### 3. 三种代理模式
| 模式 | 说明 |
|---|---|
| 🌍 全局代理 | 除 `localhost`、`127.*`、`10.*`、`172.16-31.*`、`192.168.*` 等本地/内网外，其余全部走代理 |
| 🚀 智能分流 | 通过本地 PAC + 国内域名后缀表判断，**只有国外网址走代理**，国内直连 |
| 🎯 自定义规则 | 只有规则列表命中的目标走代理，支持 `域名`、`*.通配符`、`IP`、`CIDR` 网段 |

规则示例：
```
google.com                 # 域名后缀匹配
*.githubusercontent.com    # 通配符匹配
1.1.1.1                    # 精确 IP
104.16.0.0/12              # CIDR 网段
# 以 # 开头是注释
```
规则保存在本地文件中，修改后**立即生效**，无需重开代理。

### 4. 协议兼容自动处理
程序内置内核能力表：遇到当前内核不支持的协议节点时，会**自动切换到支持的内核**，
再不行则自动挑选一个兼容节点；界面上不支持的节点会**置灰并提示**，不会让你踩坑。

---

## 使用方法

### 第一步：解压即用
把 `ProxyPilot-win.zip` 解压到任意目录，你会看到：

```
ProxyPilot.exe        ← 双击运行（原生窗口，无需浏览器）
ProxyPilot-debug.exe  ← 排错用，带控制台输出
bin\
  clash.meta.exe      ← 已内置代理内核（Mihomo，支持全部 18 种协议）
README.md
```

**无需任何手动配置**，内核已经随包附带。程序启动时若检测到同目录没有内核，
会尝试从自带资源中释放一份出来。

### 第二步：使用
双击 `ProxyPilot.exe` 弹出程序窗口，然后：

1. 点右上角 **获取节点** → 自动拉取云端全部节点并测速排序。
2. 在节点列表里点选一个节点（置灰的表示当前内核不支持，换一个即可）。
3. 选择代理模式（全局 / 智能分流 / 自定义规则）。
4. 点 **开启代理**。用完点 **关闭代理**，或直接关闭窗口 / 点「退出程序」。

> **再次双击 `ProxyPilot.exe`**：如果程序已经在运行，会直接把已有窗口
> 带到前台，不会重复启动、也不会打不开。

---

## 目录结构

```
ProxyPilot.exe
bin\                     ← 代理内核（已内置 clash.meta.exe）
data\
  nodes.json             ← 节点缓存与可用状态
  runtime\               ← 运行时生成的配置
README.md
```

---

## 常见问题

**Q: 双击后没反应 / 程序起不来？**
- 先看程序目录下有没有 `crash.log`，里面有详细报错。
- 如果之前已经打开过一次，第二次双击会把已有窗口带到前台。
- 极少数精简版系统缺少 **WebView2 运行时**，此时程序会弹窗提示并自动改用浏览器打开控制台；
  也可自行安装「Microsoft Edge WebView2 运行时」后重试。

**Q: 窗口里提示「未找到内核」？**
把 `clash.meta.exe` 放进程序目录的 `bin\` 文件夹即可（正常情况下压缩包已自带）。
界面里点「打开内核目录」可直接跳转。

**Q: 节点全是「不可用」？**
说明当前网络环境无法直连这些服务器（需要先有其他网络通道），或云端节点已过期。
可点「获取节点」重新拉取；若长期不可用，请关注节点来源项目的更新。

**Q: 关闭代理后上网异常？**
正常不会。程序关闭时会还原开启前的系统代理设置。若异常，可手动到
「Windows 设置 → 网络和 Internet → 代理」里关闭「使用代理服务器」并清空「自动配置脚本」。

**Q: 支持 Win7 吗？**
不支持，仅 Windows 10 / 11。

---

## 编译

需要 **cgo**（界面基于 WebView2，用于生成原生窗口）：

```bash
# 需要 MinGW-w64（含 g++）
set CGO_ENABLED=1
go build -trimpath -ldflags "-s -w -H=windowsgui" -o ProxyPilot.exe .
```

或直接推送代码到 GitHub，`Actions` 会自动用 `windows-latest` 编译并输出
`ProxyPilot.exe`、`ProxyPilot-debug.exe` 与打包好内核的 `ProxyPilot-win.zip`。

---

## 技术说明

- Go + WebView2 原生窗口（`github.com/webview/webview_go`），**不依赖外部浏览器**。
- 代理内核内置 Mihomo (Clash.Meta) v1.19.31，实测支持全部 18 种协议
  （`ss / ssr / vmess / vless / trojan / snell / http / socks5 / hysteria / hysteria2 /
  tuic / anytls / mieru / wireguard / ssh / direct / naiveproxy / juicity / shadowquic`）。
- 内核配置强制 `geodata-mode` 本地模式，避免联网下载 GeoIP 数据导致启动卡死。
- 系统代理通过写注册表 `HKCU\...\Internet Settings` + 调用 `InternetSetOptionW` 刷新，
  与系统「网络设置」完全一致。
- PAC 服务内置在程序中（`http://127.0.0.1:7899/proxy.pac`），规则热更新。
- 单实例运行：重复启动会把已有窗口带到前台（Win32 `AttachThreadInput` + `SetForegroundWindow`）。
