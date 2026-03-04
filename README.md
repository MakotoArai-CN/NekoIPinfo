# 🐾 NekoIPinfo

[![Go Version](https://img.shields.io/badge/Go-1.16+-00ADD8?style=flat&logo=go)](https://go.dev/)
[![License: AGPL v3](https://img.shields.io/badge/License-AGPL%20v3-blue.svg)](https://www.gnu.org/licenses/agpl-3.0)
[![Author Blog](https://img.shields.io/badge/Blog-nekopara.uk-ff69b4.svg)](https://www.nekopara.uk)

**NekoIPinfo** 是一个专为极致性能而生的开源 IPv4 归属地查询服务。
它采用 **Nginx 静态托管 + Go 纯净 API + SQLite B-Tree 索引** 的现代化架构，专为低配置（如 2vCPU 1GB RAM）轻量云服务器深度优化。

经测试，在老旧的双核 CPU （测试型号为 i5-2410M ，性能与主流轻量服务器类似）上也能扛住 **6000+ QPS**，内存占用常年保持在 **40MB** 以下，是小微型服务器构建 IP 查询服务的究极解法喵！🚀

---

## 📡 部署示例

你可以访问我搭建的服务来看看这个项目的效果如何喵～

**我的查询实例：**: [NekoIPinfo Demo](https://ip.nekopara.uk)


---

## 📐 架构设计

本项目的核心在于高效的 **B-Tree 索引二分查找**。请求流向如下：
`访客 -> Nginx (限流/HTTPS卸载) -> Go API (校验/内存逻辑) -> SQLite (索引定位)`

---

## ✨ 核心特性

* ⚡ **极致性能**：利用 SQLite B-Tree 索引，将百万级数据的查询开销从 $O(N)$ 降至 $O(\log N)$，查询耗时仅为微秒级。
* 🪶 **超低损耗**：原生 Go 编写，无第三方 Web 框架负担，部署后几乎不占用系统资源。
* 🛡️ **安全加固**：强类型 IPv4 解析校验，配合参数化 SQL 查询，从根源杜绝 SQL 注入与非法请求。
* 🌐 **前后端分离**：前端采用纯净 HTML/CSS（本地化，不依赖外部 CDN），后端提供纯粹 JSON 响应，适配各种反代场景。
* 🚦 **内建防御**：配套提供的 Nginx 配置模板自带频率限制，有效防止 API 被恶意爆破。

---

## 🚀 部署指南

### 1. 数据库准备与性能优化

为了让查询速度飞起来，你需要创建一个经过索引优化的 `ip_info.db` 文件。

**数据表结构：**
```sql
CREATE TABLE ip_info (
    network_start INTEGER NOT NULL, -- IP 段起始值
    network_end INTEGER NOT NULL,   -- IP 段结束值
    ip_info_json TEXT NOT NULL      -- 归属地 JSON 详情
);

```

**🔥 关键步骤（开启性能外挂）：**
导入数据后，请务必建立索引。没有这行命令，性能将下降 80 倍以上：

```sql
CREATE INDEX idx_network_start ON ip_info (network_start);

```

### 2. 后端编译与启动

```bash
# 克隆仓库
git clone [https://github.com/Chocola-X/NekoIPinfo.git](https://github.com/Chocola-X/NekoIPinfo.git)
cd NekoIPinfo

# 编译生成二进制文件
go build -o neko-ip-api main.go

# 启动服务
./neko-ip-api -port 8080 -db ./ip_info.db

```
当然，也可以直接下载编译好的二进制文件直接启动。

**参数说明：**

* `-port`: 监听端口（默认 8080）
* `-db`: 数据库文件路径（默认 ./ip_info.db）

### 3. Nginx 环境配置

将前端文件（`static`文件夹的内容）放置于 `/var/www/nekoipinfo`，并参考以下配置：

无限流版本：

```nginx
server {
    listen 443 ssl;
    server_name ip.nekopara.uk;
    
    set_real_ip_from 0.0.0.0/0;
    real_ip_header X-Forwarded-For;
    
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
带限流版本：
```
# 定义针对 API 的高频限流区：200请求/秒
limit_req_zone $binary_remote_addr zone=api_limit:10m rate=200r/s;
# 定义针对 静态页面 限流区：50请求/秒
limit_req_zone $binary_remote_addr zone=static_limit:10m rate=50r/s;
server {
    listen 443 ssl;
    server_name ip.nekopara.uk;

    # SSL证书和密钥路径
    ssl_certificate /data/certs/nekopara_uk.pem;
    ssl_certificate_key /data/certs/nekopara_uk.key;

    ssl_protocols TLSv1.2 TLSv1.3;
    ssl_prefer_server_ciphers on;

    # --- 通用网页路由 (返回 HTML 错误页) ---
    location / {
        limit_req zone=static_limit burst=50 nodelay;

        root /data/nekoipinfo/static;
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
        # 注意这里写的是 503，因为 limit_req 原生抛出的是 503
        error_page 503 =429 @api_limit_json;
    }
    
    # --- 内部命名 Location：直接返回 JSON 字符串 ---
    # 当上面捕获到 503 后，Nginx 会内部跳转到这里，并以 429 状态码返回内容
    location @api_limit_json {
        internal;
        default_type application/json;
        
        # 直接返回 429 状态码和 JSON 内容
        return 429 '{"code": 429, "msg": "请求速度过快，请稍后重试", "data": null}\n';
    }

    # --- 内部 Location：处理 HTML 错误页 ---
    # 供 location / 使用
    location = /429.html {
        internal;
        root /etc/nginx/error_pages;
        add_header Retry-After 1 always;
        add_header Content-Type text/html always;
    }
}
```
---

## 🛠️ 后台常驻 (Systemd)

创建服务文件 `/etc/systemd/system/neko-ip.service`:

```ini
[Unit]
Description=NekoIPinfo API Service
After=network.target

[Service]
ExecStart=/path/to/neko-ip-api -port 8080 -db /path/to/ip_info.db
WorkingDirectory=/path/to/project
Restart=always
User=www-data

[Install]
WantedBy=multi-user.target

```

当然，也可以使用**TMUX**挂着服务端喵～
---

## 📡 API 示例

**GET** `/ipinfo?ip=1.1.1.1`

**Response:**

```json
{
  "code": 200,
  "msg": "success",
  "data": {
    "ip": "1.1.1.1",
    "country": "澳大利亚",
    "province": "APNIC",
    "city": "Cloudflare",
    "isp": "APNIC Research and Development",
    "latitude": "-33.494000",
    "longitude": "143.210000"
  }
}

```

---

## 🔗 链接

* **GitHub**: [Chocola-X/NekoIPinfo](https://github.com/Chocola-X/NekoIPinfo)
* **Blog**: [nekopara.uk](https://www.nekopara.uk)

---

## 📄 开源协议 (License)

本项目采用 **GNU Affero General Public License v3.0 (AGPL-3.0)** 协议开源。

**这意味着：** 如果你对本项目进行了修改并在云端运行了该服务，你**必须**向访问者公开你修改后的完整源代码。开源精神万岁喵！🐾

