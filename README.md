# 🐾 NekoIPinfo

[![Go Version](https://img.shields.io/badge/Go-1.16+-00ADD8?style=flat&logo=go)](https://go.dev/)
[![License: AGPL v3](https://img.shields.io/badge/License-AGPL%20v3-blue.svg)](https://www.gnu.org/licenses/agpl-3.0)
[![Author Blog](https://img.shields.io/badge/Blog-nekopara.uk-ff69b4.svg)](https://www.nekopara.uk)

**NekoIPinfo** 是一个专为极致性能而生的开源 IP 归属地查询服务。带有一点点可爱的猫娘风格喵 🐱~

它采用 **Nginx 静态托管 + Go 纯净 API + Pebble KV 引擎** 的现代化架构，同时支持 **IPv4 和 IPv6** 查询，专为低配置（如 2vCPU 1GB RAM）轻量云服务器深度优化。

经测试，在老旧的双核 CPU（测试型号为 i5-2410M，性能与主流轻量服务器类似）上也能扛住 **5 000+ QPS**，内存占用保持在 **40MB** 以下（默认不开启数据库内存缓存的情况下），是小微型服务器构建 IP 查询服务的究极解法喵！🚀

V1.2.0空载状态内存占用不到2M，高并发后约莫**80M**左右的占用。8核心16线程的 R7-5800H 环境下，内存开销在**600M**的以内就可以达到**150 000+ QPS**，最新一版中，项目支持`-mem=fast`以及`-mem=full`，分别为加载索引至内存，以及数据库全量加载至内存，如果数据库很大，不推荐使用全量加载，全量加载到内存的情况下占用大约是数据库大小的10-13倍，即使是空载内存占用也是数据库大小的10-13倍，可以带来1.2-1.5倍的QPS提升。V2.0.0 新增**资源限制**功能（`-max-mem` / `-max-cpu`），可智能限制内存和 CPU 占用，避免在低配 VPS 上因资源溢出被 OOM Killer 杀掉；同时新增**版本管理**功能（`-v` / `-update`），支持一键在线更新；底层存储从 JSON 文本改为**二进制紧凑编码**，数据库体积缩小约 30%~60%。

---

## 📡 部署示例

你可以访问我搭建的服务来看看这个项目的效果如何喵～

**我的查询实例：** [NekoIPinfo Demo](https://ip.nekopara.uk)

---

## 📐 架构设计

本项目采用高性能 **Pebble KV 引擎**（CockroachDB 出品的 LSM-Tree 存储引擎），将 IP 段以 128-bit 键值对形式存储，数据负载使用二进制紧凑编码（替代 JSON 文本），查询复杂度为 $O(\log N)$，耗时仅为微秒级。

请求流向如下：

```BASH
访客 -> Nginx (限流/HTTPS卸载) -> Go API (校验/内存逻辑) -> Pebble KV (索引定位)
```

---

## ✨ 核心特性

* ⚡ **极致性能**：基于 fasthttp 框架 + Pebble KV 引擎，查询耗时微秒级，轻松达到万级 QPS。
* 🌐 **双栈支持**：完整支持 IPv4 和 IPv6 地址查询，内部统一以 128-bit 键存储。
* 🪶 **超低损耗**：原生 Go 编写，无第三方 Web 框架负担，部署后几乎不占用系统资源。
* 🛡️ **安全加固**：强类型 IP 解析校验，从根源杜绝非法请求。
* 🔧 **一键生成**：内置 `dbgen` 工具，支持直接导入 MMDB、CSV 和旧版 SQLite 格式数据库，无需手动建表。
* 📊 **内建测压**：自带 `bench` 压力测试工具，支持自动寻峰和手动模式。
* 🔄 **增量更新**：支持 City / ASN 单独更新，自动备份，变更日志独立存储。
* 🚦 **内建防御**：配套提供的 Nginx 配置模板自带频率限制，有效防止 API 被恶意爆破。
* 🧠 **资源限制**：支持 `-max-mem` / `-max-cpu` 参数，智能限制内存和 CPU 占用，防止 OOM，适配低配 VPS。
* 🔃 **版本管理**：内置版本检测与一键在线更新（`-v` 查看 / `-update` 自动更新），无需手动下载替换。
* 📦 **紧凑存储**：底层使用二进制编码替代 JSON 文本存储，数据库体积大幅缩小，兼容读取旧版 JSON 格式数据库。

---

## 💻 API 调用说明

后端提供纯净的 RESTful API 接口，返回标准 JSON 格式数据。

### 1. 查询指定 IP

**GET** `/ipinfo?ip={IP地址}`

支持 IPv4 和 IPv6 地址。

**请求示例：** `/ipinfo?ip=8.8.8.8`

**返回响应：**

```json
{
    "code": 200,
    "msg": "success",
    "data": {
        "ip": "8.8.8.8",
        "country": "美国",
        "province": "加利福尼亚州",
        "city": "圣克拉拉",
        "isp": "Google",
        "latitude": "37.386052",
        "longitude": "-122.083851"
    }
}
```

### 2. 获取访客自身 IP（自动检测）

如果在请求时不传递 `ip` 参数，API 将自动解析并返回**请求者本身**的 IP 信息！

**GET** `/ipinfo`

**返回响应：**

```json
{
    "code": 200,
    "msg": "success",
    "data": {
        "ip": "119.8.185.128",
        "country": "新加坡",
        "province": "新加坡",
        "city": "",
        "isp": "huawei.com",
        "latitude": "1.352083",
        "longitude": "103.819836"
    }
}
```

---

## 🚀 部署指南

### 1. 数据库准备

本项目使用 Pebble KV 引擎存储 IP 数据。内置的 `dbgen` 工具可以直接将 MMDB、CSV 或旧版 SQLite 格式的 IP 数据库转换为项目所需的 Pebble 数据库。

**数据源下载：**

| 数据源 | 格式 | 用途 | 下载地址 |
|--------|------|------|----------|
| DB-IP City Lite | MMDB | 地理位置（国家/省份/城市/经纬度） | <https://download.db-ip.com/free/dbip-city-lite-2026-03.mmdb.gz> |
| DB-IP ASN Lite | MMDB | ISP/运营商信息 | <https://download.db-ip.com/free/dbip-asn-lite-2026-03.mmdb.gz> |
| DB-IP City Lite | CSV | 地理位置（备选格式） | <https://download.db-ip.com/free/dbip-city-lite-2026-03.csv.gz> |
| MaxMind GeoLite2 City | MMDB | 地理位置（备选来源） | <https://dev.maxmind.com/geoip/signup> |
| MaxMind GeoLite2 ASN | MMDB | ISP/运营商信息（备选来源） | <https://dev.maxmind.com/geoip/signup> |

> **💡 提示：** 免费版 City Lite 数据库不包含 ISP 字段，需要额外传入 ASN 数据库才能获得完整的运营商信息。推荐同时下载 City 和 ASN 两个免费库进行合并喵。

**使用 dbgen 生成数据库：**

```bash
# 最简用法：自动检测当前目录下的 MMDB 文件并生成
./nekoipinfo-dbgen

# 推荐：City + ASN 合并（完整支持所有字段）
./nekoipinfo-dbgen -input dbip-city-lite-2026-03.mmdb -asn dbip-asn-lite-2026-03.mmdb

# 仅使用 City 库（ISP 字段为空）
./nekoipinfo-dbgen -input dbip-city-lite-2026-03.mmdb

# CSV 格式 + ASN 合并
./nekoipinfo-dbgen -input dbip-city-lite-2026-03.csv -asn dbip-asn-lite-2026-03.mmdb

# MaxMind 数据源
./nekoipinfo-dbgen -input GeoLite2-City.mmdb -asn GeoLite2-ASN.mmdb

# 从旧版 SQLite 数据库迁移
./nekoipinfo-dbgen -sqlite old_ip_info.db
./nekoipinfo-dbgen -sqlite old_ip_info.db -asn dbip-asn-lite-2026-03.mmdb
```

**增量更新：**

```bash
# 自动检测 MMDB 并增量更新（相同记录跳过）
./nekoipinfo-dbgen -update

# 强制覆盖更新
./nekoipinfo-dbgen -update -overwrite

# 仅更新 ISP 字段（ASN 单独更新）
./nekoipinfo-dbgen -asn dbip-asn-lite-2026-03.mmdb -update

# 仅更新地理位置字段（City 单独更新，不覆盖已有 ISP）
./nekoipinfo-dbgen -input dbip-city-lite-2026-03.mmdb -update

# City + ASN 同时更新
./nekoipinfo-dbgen -input dbip-city-lite-2026-03.mmdb -asn dbip-asn-lite-2026-03.mmdb -update
```

> **⚠️ 注意：** 增量更新前会自动备份现有数据库到 `ip_info_backup/` 目录，变更日志独立存储在 `ip_info_changelog/` 目录中。

**dbgen 参数说明：**

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `-input` | 无 | 输入文件路径，支持 `.mmdb` 和 `.csv` 格式 |
| `-asn` | 无 | ASN 数据库路径（`.mmdb` 格式），用于补充 ISP/运营商信息 |
| `-sqlite` | 无 | 旧版 SQLite 数据库路径（迁移用） |
| `-out` | `ip_info` | 输出的 Pebble 数据库目录路径 |
| `-update` | `false` | 增量更新模式（已有相同记录跳过） |
| `-overwrite` | `false` | 强制覆盖已有记录（配合 `-update` 使用） |
| `-dump` | `false` | 导出/查看数据库统计信息 |
| `-csv` | 无 | 导出为 CSV 文件路径（配合 `-dump` 使用） |
| `-sqlite-out` | 无 | 导出为 SQLite 文件路径（配合 `-dump` 使用） |
| `-sample` | `0` | 预览前 N 条数据（配合 `-dump` 使用） |
| `-logdb` | 无 | 日志数据库目录路径（查看日志统计） |
| `-logout` | 无 | 日志导出输出路径（`.csv`） |
| `-no-color` | `false` | 禁用终端彩色输出 |
| `-help` | - | 显示帮助信息 |

### 2. 编译

> Windows 用户 推荐搭配 [llvm-mingw-20260224-ucrt-x86_64](https://github.com/mstorsjo/llvm-mingw/releases/tag/20260224) 进行编译

**方式一：全平台交叉编译**

```bash
git clone https://github.com/Chocola-X/NekoIPinfo.git
cd NekoIPinfo
make
```

编译产物输出到 `build/` 目录，按操作系统分目录存放：

```bash
build/
├── Linux/
│   ├── nekoipinfo_x86_64
│   ├── nekoipinfo_x86
│   ├── nekoipinfo_arm64
│   ├── nekoipinfo_arm
│   ├── nekoipinfo_mips
│   ├── nekoipinfo_mipsle
│   ├── nekoipinfo_mips64
│   ├── nekoipinfo_mips64le
│   ├── nekoipinfo_loong64
│   ├── nekoipinfo_riscv64
│   ├── nekoipinfo_ppc64
│   ├── nekoipinfo_ppc64le
│   ├── nekoipinfo_s390x
│   ├── nekoipinfo-bench_*
│   └── nekoipinfo-dbgen_*
├── macOS/
│   ├── nekoipinfo_x86_64
│   ├── nekoipinfo_arm64
│   ├── nekoipinfo-bench_*
│   └── nekoipinfo-dbgen_*
└── Windows/
    ├── nekoipinfo_x86_64.exe
    ├── nekoipinfo_x86.exe
    ├── nekoipinfo_arm64.exe
    ├── nekoipinfo-bench_*.exe
    └── nekoipinfo-dbgen_*.exe
```

每个目录包含三个二进制文件（对应架构后缀）：

* `nekoipinfo` — 主程序（API 服务）
* `nekoipinfo-bench` — 压力测试工具
* `nekoipinfo-dbgen` — 数据库生成/更新工具

**方式二：仅编译当前平台**

```bash
make auto
```

**方式三：指定平台编译**

```bash
make linux      # 仅编译 Linux 全架构
make darwin     # 仅编译 macOS 全架构
make windows    # 仅编译 Windows 全架构
```

**其他 Make 命令：**

| 命令 | 说明 |
|------|------|
| `make` | 全平台交叉编译 |
| `make auto` | 自动检测当前平台编译 |
| `make linux` | 仅编译 Linux 全架构 |
| `make darwin` | 仅编译 macOS 全架构 |
| `make windows` | 仅编译 Windows 全架构 |
| `make clean` | 清理 build 目录 |
| `make deps` | 下载依赖 |
| `make test` | 运行测试 |
| `make fmt` | 格式化代码 |
| `make vet` | 静态检查 |
| `make help` | 显示帮助 |

**支持的目标平台：**

| 系统 | 架构 |
|------|------|
| Linux | x86_64, x86, arm64, arm, mips, mipsle, mips64, mips64le, loong64, riscv64, ppc64, ppc64le, s390x |
| macOS | x86_64 (Intel), arm64 (Apple Silicon) |
| Windows | x86_64, x86, arm64 |

### 3. 启动服务

```bash
./nekoipinfo -port 8080 -db ./ip_info
```

当然，也可以直接下载 [Release](https://github.com/Chocola-X/NekoIPinfo/releases) 页面编译好的二进制文件直接启动。

**主程序参数说明：**

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `-port` | `8080` | API 服务监听端口 |
| `-db` | `ip_info` | Pebble 数据库目录路径 |
| `-mem` | `off` | 内存模式：`off`=纯 Pebble 实时查询，`fast`=索引驻内存 + LRU 缓存，`full`=全量内存 + 二分查找。单独使用 `-mem` 不带值时等同于 `-mem=full` |
| `-log` | 未开启 | 日志控制：`-log` 或 `-log=true` 仅控制台输出，`-log=0` 永久持久化存储，`-log=N` 保留 N 天 |
| `-logdir` | `ip_info_log` | 日志数据库存储目录 |
| `-static` | `false` | 启用内嵌静态文件服务（无需 Nginx 托管前端） |
| `-staticdir` | `static` | 静态文件目录路径 |
| `-max-mem` | 无 | 最大内存限制，支持数字+单位（如 `512M`、`1G`、`256MB`）或百分比（如 `50%`、`80%`）。不带此参数时不限制 |
| `-max-cpu` | 无 | 最大 CPU 限制，支持核心数（如 `2`、`4`）或百分比（如 `50%`、`75%`）。单核 CPU 仅支持百分比。不带此参数时不限制 |
| `-v` / `-version` | - | 显示版本信息，并自动检测是否有新版本可用 |
| `-update` | - | 自动检查并更新到最新版本。带版本号时强制更新到指定版本（如 `-update=1.3.1`） |
| `-no-color` | `false` | 禁用终端彩色输出 |

---

## 🧠 资源限制

在低配 VPS（如 1核 512MB）上运行时，可以使用 `-max-mem` 和 `-max-cpu` 参数来限制资源占用，避免被系统 OOM Killer 杀掉。

### 内存限制 `-max-mem`

| 格式 | 示例 | 说明 |
|------|------|------|
| 数字 + 单位 | `-max-mem=512M` | 限制最大内存为 512 MB |
| 数字 + 单位 | `-max-mem=1G` | 限制最大内存为 1 GB |
| 数字 + 单位 | `-max-mem=256MB` | 限制最大内存为 256 MB |
| 百分比 | `-max-mem=50%` | 限制为系统总内存的 50% |
| 百分比 | `-max-mem=80%` | 限制为系统总内存的 80% |

**工作原理：**
- 通过 Go 运行时的 `debug.SetMemoryLimit` 设置软限制（设定值的 85%）
- 后台监控协程每 3 秒检查一次内存使用量
- 达到 80% 阈值时触发温和 GC
- 达到 90% 阈值时触发强制 GC + 释放系统内存
- 微调逻辑避免频繁 GC 导致性能波动（GC 冷却时间 5~15 秒）

**安全策略：**
- 设置的内存超过系统总内存时，自动忽略该参数
- 不带此参数时保持现有行为，不做任何限制

### CPU 限制 `-max-cpu`

| 格式 | 示例 | 说明 |
|------|------|------|
| 核心数 | `-max-cpu=2` | 限制使用 2 个 CPU 核心 |
| 核心数 | `-max-cpu=1` | 限制使用 1 个 CPU 核心 |
| 百分比 | `-max-cpu=50%` | 限制使用 50% 的 CPU 核心（向下取整，最少 1 核） |
| 百分比 | `-max-cpu=75%` | 限制使用 75% 的 CPU 核心 |

**工作原理：**
- 通过 `runtime.GOMAXPROCS` 限制 Go 调度器可用的核心数
- 同时自动调整 HTTP 服务器的最大并发连接数

**安全策略：**
- 单核 CPU 仅支持百分比格式，设置核心数时自动忽略
- 设置的核心数超过系统实际核心数时，自动忽略该参数
- 不带此参数时保持现有行为，不做任何限制

### 使用示例

```bash
# 限制内存 256MB + 限制 CPU 1 核（适合 512MB VPS）
./nekoipinfo -port 8080 -db ip_info -max-mem=256M -max-cpu=1

# 限制内存为系统的 60%（适合与其他服务共存的场景）
./nekoipinfo -port 8080 -db ip_info -max-mem=60%

# 限制 CPU 为系统的一半
./nekoipinfo -port 8080 -db ip_info -max-cpu=50%

# 搭配全量内存模式 + 资源限制（兼顾性能与安全）
./nekoipinfo -port 8080 -db ip_info -mem=full -max-mem=1G -max-cpu=2
```

---

## 🔃 版本管理

### 查看版本

```bash
./nekoipinfo -v
# 或
./nekoipinfo -version
```

输出示例：

```
NekoIPinfo v2.0.0
  OS/Arch: linux/amd64
  Go:      go1.25.8
  最新版本: 2.0.0 (已是最新)
```

如果有新版本可用：

```
NekoIPinfo v2.0.0
  OS/Arch: linux/amd64
  Go:      go1.25.8
  最新版本: 2.0.1 (有新版本可用！使用 -update 更新)
```

### 自动更新

```bash
# 自动检查并更新到最新版本
./nekoipinfo -update

# 强制更新到指定版本（不比较版本号）
./nekoipinfo -update=1.3.1
./nekoipinfo -update=v1.3.1
```

更新过程会自动：
1. 从 GitHub Releases 下载适用于当前系统和架构的二进制文件
2. 备份旧文件
3. 原地替换为新文件
4. 完成后提示重新启动

---

## 📊 压力测试工具

项目内置了专用的压力测试工具 `nekoipinfo-bench`，支持手动模式和自动寻峰模式。

**基本用法：**

```bash
# 使用默认参数测试本机服务
./nekoipinfo-bench

# 指定目标服务器和端口
./nekoipinfo-bench -ip 192.168.1.100 -port 8080

# 测试远程 HTTPS 服务
./nekoipinfo-bench -ip ip.nekopara.uk -port 443 -scheme https

# 使用完整地址（优先级最高）
./nekoipinfo-bench -host http://127.0.0.1:8080

# 自动寻峰模式
./nekoipinfo-bench -ip 127.0.0.1 -port 8080 -auto

# 固定 IP 查询模式
./nekoipinfo-bench -random=false -query-ip 1.1.1.1

# 自定义并发和持续时间
./nekoipinfo-bench -ip 127.0.0.1 -port 8080 -c 500 -d 30

# 自定义 API 路径
./nekoipinfo-bench -ip 127.0.0.1 -port 8080 -path /ipinfo
```

**bench 参数说明：**

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `-ip` | `127.0.0.1` | 目标服务器 IP 或域名 |
| `-port` | `8080` | 目标服务器端口 |
| `-scheme` | `http` | 请求协议（`http` 或 `https`） |
| `-host` | 无 | API 服务完整地址，优先级最高（如 `http://127.0.0.1:8080`） |
| `-path` | `/ipinfo` | API 接口路径 |
| `-c` | CPU×32 | 并发协程数 |
| `-d` | `10` | 测试持续时间（秒） |
| `-random` | `true` | 是否使用随机 IP 查询 |
| `-query-ip` | `8.8.8.8` | 固定查询 IP（`-random=false` 时生效） |
| `-auto` | `false` | 自动寻峰模式，逐步提高并发直到找到最高 QPS |

---

## 🌐 Nginx 环境配置

将前端文件（`static` 文件夹的内容）放置于 `/var/www/nekoipinfo`，并参考以下配置：

**无限流版本：**

```nginx
server {
    listen 443 ssl;
    server_name ip.nekopara.uk;
    
    # 仅当你在使用 CDN 时开启以下两行获取真实 IP
    # set_real_ip_from 0.0.0.0/0;
    # real_ip_header X-Forwarded-For;
    
    # SSL证书和密钥路径
    ssl_certificate /data/certs/nekopara_uk.pem;
    ssl_certificate_key /data/certs/nekopara_uk.key;

    ssl_protocols TLSv1.2 TLSv1.3;
    ssl_prefer_server_ciphers on;

    location / {
        root /var/www/nekoipinfo;
        index index.html;
        gzip on;
        gzip_types text/plain text/css application/json application/javascript;
    }
    
    location /ipinfo {
        proxy_pass http://127.0.0.1:8080;
        # 必须透传真实 IP
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header Host $http_host;
    }
}
```

**带限流版本：**

```conf
# 定义针对 API 的高频限流区：200请求/秒
limit_req_zone $binary_remote_addr zone=api_limit:10m rate=200r/s;
# 定义针对 静态页面 限流区：50请求/秒
limit_req_zone $binary_remote_addr zone=static_limit:10m rate=50r/s;

server {
    listen 443 ssl;
    server_name ip.nekopara.uk;
    
    # 仅当你在使用 CDN 时开启以下两行获取真实 IP
    # set_real_ip_from 0.0.0.0/0;
    # real_ip_header X-Forwarded-For;
    
    # SSL证书和密钥路径
    ssl_certificate /data/certs/nekopara_uk.pem;
    ssl_certificate_key /data/certs/nekopara_uk.key;

    ssl_protocols TLSv1.2 TLSv1.3;
    ssl_prefer_server_ciphers on;

    # --- 通用网页路由 (返回 HTML 错误页) ---
    location / {
        limit_req zone=static_limit burst=50 nodelay;

        root /var/www/nekoipinfo;
        index index.html;
        gzip on;
        gzip_types text/plain text/css application/json application/javascript;
        
        # 捕获默认的 503，转换为 429 并返回 HTML
        error_page 503 =429 /429.html;
    }
    
    # --- API 接口路由 (返回 JSON 错误页) ---
    location /ipinfo {
        # 使用新定义的 api_limit (200r/s)
        limit_req zone=api_limit burst=400 nodelay;

        proxy_pass http://127.0.0.1:8080;
        
        # 必须透传真实 IP
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header Host $http_host;

        # 捕获默认的 503，转换为 429 并跳转到 @api_limit_json
        error_page 503 =429 @api_limit_json;
    }
    
    # --- 内部命名 Location：直接返回 JSON 字符串 ---
    location @api_limit_json {
        internal;
        default_type application/json;
        return 429 '{"code": 429, "msg": "请求速度过快，请稍后重试", "data": null}\n';
    }

    # --- 内部 Location：处理 HTML 错误页 ---
    location = /429.html {
        internal;
        root /etc/nginx/error_pages;
        add_header Retry-After 1 always;
        add_header Content-Type text/html always;
    }
}
```

**提示：** 如果你的服务器位于 Cloudflare 等 CDN 背后，需要在 server 块内设置 `set_real_ip_from 0.0.0.0/0;` 和 `real_ip_header X-Forwarded-For;`。如果你直接暴露在公网，不要进行配置，以免被伪造 IP！

---

## 🛠️ 后台常驻 (Systemd)

创建服务文件 `/etc/systemd/system/neko-ip.service`:

```ini
[Unit]
Description=NekoIPinfo API Service
After=network.target

[Service]
ExecStart=/path/to/nekoipinfo -port 8080 -db /path/to/ip_info
WorkingDirectory=/path/to/project
Restart=always
User=www-data

[Install]
WantedBy=multi-user.target
```

带资源限制的配置示例：

```ini
[Unit]
Description=NekoIPinfo API Service
After=network.target

[Service]
ExecStart=/path/to/nekoipinfo -port 8080 -db /path/to/ip_info -max-mem=256M -max-cpu=1
WorkingDirectory=/path/to/project
Restart=always
User=www-data

[Install]
WantedBy=multi-user.target
```

```bash
# 启用并启动服务
sudo systemctl daemon-reload
sudo systemctl enable neko-ip
sudo systemctl start neko-ip

# 查看状态
sudo systemctl status neko-ip

# 查看日志
sudo journalctl -u neko-ip -f
```

当然，也可以使用 **TMUX** 挂着服务端喵～

---

## 🔗 链接

* **GitHub**: [Chocola-X/NekoIPinfo](https://github.com/Chocola-X/NekoIPinfo)
* **Blog**: [nekopara.uk](https://www.nekopara.uk)

---

## 📄 开源协议 (License)

本项目采用 **GNU Affero General Public License v3.0 (AGPL-3.0)** 协议开源。

**这意味着：** 如果你对本项目进行了修改并在云端运行了该服务，你**必须**向访问者公开你修改后的完整源代码。开源精神万岁喵！🐾
