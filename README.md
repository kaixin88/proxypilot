# ProxyPilot · 一键代理控制台

> Windows 10 / 11 通用的图形化代理工具。自动获取云端节点、自动剔除失效节点，
> 一键开关系统代理，支持「全局 / 智能分流 / 自定义规则」三种模式。

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
- 解析 7 种配置格式：Clash.Meta YAML、Xray JSON、sing-box JSON、
  hysteria JSON、hysteria2 JSON、juicity JSON、naiveproxy JSON、mieru JSON。
- 按「协议 + 服务器 + 端口」指纹去重合并。
- **上次的节点会被保留**：某次抓取失败也不会把已有节点清空。
- **协议感知探测**：TCP 类节点测 TCP 握手时延；QUIC/UDP 类节点（hysteria 等）
  改用 UDP 可达性探测，避免用 TCP 误判成不可用。
- 不可用的节点自动剔除，可用节点按延迟从低到高排序。

> 实测效果：26/26 个来源可用，合并出 14 个节点，覆盖
> `hysteria`、`hysteria2`、`juicity`、`mieru`、`naiveproxy`、`vless` 六种协议。

### 2. 一键开关代理
- 点击「开启代理」：启动代理内核 → 设置 Windows 系统代理。
- 点击「关闭代理」：停止内核 → **精确还原**你原来的系统代理设置（含原本的值与开关状态）。

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

---

## 使用方法

### 第一步：放置代理内核
ProxyPilot 自身只负责「节点管理 + 系统代理 + 规则分流」，
真正转发流量的是内核程序。请把下面**任意一个**内核放到程序目录下的 `bin\` 文件夹：

- `clash.meta.exe`（推荐，支持协议最全，含 hysteria / hysteria2）
  - 可从你已有的 `Chrome153_AllNew_2026.9.12\clash.meta\` 目录复制，
    重命名为 `clash.meta.exe`
- `xray.exe`（支持 vless / vmess / trojan / ss）
  - 可从 `Chrome153_AllNew_2026.9.12\Xray\` 复制，同时把
    `geoip.dat`、`geosite.dat` 一起放到 `bin\`（分流规则需要）

> 程序启动界面会显示是否检测到内核。没有内核也能打开界面、获取和浏览节点，
> 只是无法真正开启代理。

### 第二步：运行
双击 `ProxyPilot.exe`，浏览器会自动打开控制台（`http://127.0.0.1:17987`）。
若未自动打开，手动访问该地址即可。

### 第三步：使用
1. 点右上角 **获取节点** → 自动拉取云端全部节点并测速排序。
2. 在节点列表里点选一个节点。
3. 选择代理模式。
4. 点 **开启代理**。用完点 **关闭代理**。

---

## 目录结构

```
ProxyPilot.exe
bin\                     ← 放内核（clash.meta.exe 或 xray.exe）
data\
  nodes.json             ← 节点缓存与可用状态
  runtime\               ← 运行时生成的配置
README.md
```

---

## 常见问题

**Q: 点开启代理提示「未找到内核」？**
把 `clash.meta.exe` 或 `xray.exe` 放进 `bin\` 目录即可，界面里点「打开内核目录」可直接跳转。

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

```bash
go build -trimpath -ldflags "-s -w -H=windowsgui" -o ProxyPilot.exe .
```

或直接推送代码到 GitHub，`Actions` 会自动编译并输出 `ProxyPilot.exe` 与 `ProxyPilot-win.zip` 压缩包。

---

## 技术说明

- 纯 Go 实现，`amd64` 静态编译，无外部依赖，单文件运行。
- 系统代理通过写注册表 `HKCU\...\Internet Settings` + 调用 `InternetSetOptionW` 刷新，与系统「网络设置」完全一致。
- PAC 服务内置在程序中（`http://127.0.0.1:7899/proxy.pac`），规则热更新。
- 界面为内嵌的本地网页，打开即用，无需安装。
