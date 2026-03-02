# ⚡ PROXY-PANEL (Go 极简反代面板)

![Go Version](https://img.shields.io/badge/Go-1.21+-00ADD8?style=flat&logo=go)
![License](https://img.shields.io/badge/License-MIT-blue.svg)
![Release](https://img.shields.io/github/v/release/jinhuaitao/PROXY-PANEL)

**PROXY-PANEL** 是一个使用 Go 语言编写的极轻量级、零外部依赖的**反向代理与自动化 SSL 证书管理面板**。

告别繁琐的 Nginx 配置和复杂的 acme.sh 脚本！只需运行一个单文件，即可获得一个拥有现代 Web UI 的企业级微网关。非常适合个人开发者、工作室以及自托管（Self-hosting）爱好者使用。

## ✨ 核心特性

- 🪶 **极致轻量，零依赖**：基于 Go 原生编写，内置 SQLite 数据库（纯 Go 驱动，无 CGO 烦恼）。不需要安装 Nginx、MySQL 或其他任何环境。
- 🔒 **全自动 HTTPS (Let's Encrypt)**：添加域名后自动触发证书申请，并在过期前 30 天自动静默续期。
- 🎨 **现代化的 Web 控制台**：告别简陋的弹窗登录。提供美观的数据看板、规则启停开关和实时状态指示灯。
- 🛡️ **GitHub OAuth 一键登录**：支持绑定 GitHub 账号，实现免密且极其安全的后台访问。
- 🩺 **智能健康检查**：后台每 30 秒自动巡检后端节点（Target），内网服务宕机面板立刻飙红报警。
- 🚀 **完美转发**：原生支持 WebSocket 转发，自动透传 `X-Real-IP`、`X-Forwarded-For` 等访客真实 IP 头。

---

## 📸 界面预览

*(提示：建议在这里放两张截图，一张登录页，一张控制台主页，图片可以上传到仓库的 docs 文件夹或图床)*
- **仪表盘界面**：`![Dashboard](./docs/dashboard.png)`
- **安全登录与设置**：`![Settings](./docs/settings.png)`

---

## 🚀 极速安装 (推荐)

我们提供了一键安装脚本，自动识别系统架构（支持 Debian/Ubuntu 的 Systemd 和 Alpine 的 OpenRC），并自动配置为后台守护进程开机自启。

请在 root 用户下执行以下命令：

```
curl -o install.sh https://raw.githubusercontent.com/jinhuaitao/PROXY-PANEL/main/install.sh && chmod +x install.sh  && ./install.sh
```

安装后的默认配置：
面板访问地址：http://<你的服务器IP>:8080

运行数据目录：/opt/proxypanel/ (包含 proxy.db 数据库和 certs/ 证书目录)

服务管理命令：

启动：systemctl start proxypanel

停止：systemctl stop proxypanel

重启：systemctl restart proxypanel

状态：systemctl status proxypanel

⚠️ 重要注意事项 (必读)
端口放行：面板的自动 SSL 申请强依赖于 HTTP-01 验证。你必须在服务器的安全组/防火墙中放行 80 和 443 端口。

域名解析：在面板中添加新代理规则前，请确保该域名（A 记录）已经正确解析到了本服务器的公网 IP。

安全建议：管理面板默认运行在 8080 端口。在生产环境中，建议通过云服务商的安全组策略，将 8080 端口限制为仅允许你的办公 IP 访问，以确保绝对安全。

⚙️ 绑定 GitHub 免密登录
为了提升安全性，建议在首次使用密码登录后，进入「系统安全设置」绑定 GitHub OAuth：

前往 GitHub Developer Settings 创建一个新的 OAuth App。

Homepage URL 填写你的面板地址（如 http://ip:8080）。

Authorization callback URL 填写：http://ip:8080/auth/github/callback。

将获取到的 Client ID 和 Client Secret 填入本面板的设置中。

务必填写允许登录的 GitHub 用户名（如你自己的 ID），防止他人登录。

🛠️ 从源码编译
如果你想进行二次开发，或者自行编译二进制文件：

Bash
# 1. 克隆代码
git clone [https://github.com/jinhuaitao/PROXY-PANEL.git](https://github.com/jinhuaitao/PROXY-PANEL.git)

cd PROXY-PANEL

# 2. 下载依赖
go mod tidy

# 3. 编译 (禁用 CGO，方便跨平台)
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "-s -w" -o proxypanel-linux-amd64 main.go

📄 License
本项目采用 MIT License 开源协议。


***

### 下一步建议
这篇文档非常契合你这个项目的调性。你需要我帮你把刚刚那个 `install.sh` 脚本的最终版本，以及 `build.yml` 的内容做最后的核对，确保你 push 到 GitHub 时所有流程都能无缝对接吗？
