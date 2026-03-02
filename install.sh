#!/bin/bash

# =========================================================
#  Go Proxy Panel - One-Click Installer (Auto IP & GitHub Proxy)
#  System: Debian/Ubuntu (Systemd) & Alpine (OpenRC)
# =========================================================

# --- 基础配置 ---
GITHUB_REPO="jinhuaitao/PROXY-PANEL"
INSTALL_DIR="/opt/proxypanel"
BIN_PATH="$INSTALL_DIR/proxypanel"
SERVICE_NAME="proxypanel"

# --- 颜色与样式配置 ---
RED='\033[31m'
GREEN='\033[32m'
YELLOW='\033[33m'
BLUE='\033[34m'
CYAN='\033[36m'
BOLD='\033[1m'
PLAIN='\033[0m'

# 图标定义
ICON_SUCCESS="✅"
ICON_FAIL="❌"
ICON_WARN="⚠️"
ICON_INFO="ℹ️"
ICON_ROCKET="🚀"
ICON_TRASH="🗑️"
ICON_GLOBE="🌍"

# --- UI 辅助函数 ---

clear_screen() {
    clear
}

print_line() {
    echo -e "${BLUE}————————————————————————————————————————————————————${PLAIN}"
}

print_logo() {
    clear_screen
    echo -e "${CYAN}${BOLD}"
    echo "    ____                           ____                  __ "
    echo "   / __ \_________  _  ____  __   / __ \____ _____  ___  / / "
    echo "  / /_/ / ___/ __ \| |/_/ / / /  / /_/ / __ \`/ __ \/ _ \/ /  "
    echo " / ____/ /  / /_/ />  </ /_/ /  / ____/ /_/ / / / /  __/ /   "
    echo "/_/   /_/   \____/_/|_|\__, /  /_/    \__,_/_/ /_/\___/_/    "
    echo "                      /____/                                 "
    echo -e "${PLAIN}"
    echo -e "   ${YELLOW}Go Proxy Panel - 轻量级反代与自动化 SSL 面板${PLAIN}"
    print_line
}

log_info() {
    echo -e "${BLUE}[${ICON_INFO}] ${PLAIN} $1"
}

log_success() {
    echo -e "${GREEN}[${ICON_SUCCESS}] ${PLAIN} $1"
}

log_error() {
    echo -e "${RED}[${ICON_FAIL}] ${PLAIN} $1"
}

log_warn() {
    echo -e "${YELLOW}[${ICON_WARN}] ${PLAIN} $1"
}

# --- 系统检查 ---

check_root() {
    if [ "$(id -u)" != "0" ]; then
        log_error "面板需要绑定 80/443 端口，请使用 root 用户运行此脚本！"
        exit 1
    fi
}

check_dependencies() {
    if ! command -v wget >/dev/null; then
        log_info "正在安装必要组件 (wget)..."
        if [ -f /etc/alpine-release ]; then
            apk add --no-cache wget >/dev/null 2>&1
        elif [ -f /etc/debian_version ]; then
            apt-get update >/dev/null 2>&1 && apt-get install -y wget >/dev/null 2>&1
        fi
        log_success "组件安装完成"
    fi
}

# --- 核心功能 ---

install_proxypanel() {
    print_logo
    echo -e "${BOLD}正在开始安装 Proxy Panel...${PLAIN}\n"
    
    check_dependencies

    # 创建工作目录
    if [ ! -d "$INSTALL_DIR" ]; then
        mkdir -p "$INSTALL_DIR"
        log_info "创建程序运行目录: $INSTALL_DIR"
    fi

    # 自动识别架构并拼接你提供的 GitHub 代理加速链接
    ARCH=$(uname -m)
    BASE_URL="https://jht126.eu.org/https://github.com/${GITHUB_REPO}/releases/latest/download"
    
    case "$ARCH" in
        x86_64)
            # 修复：使用全小写的文件名
            DOWNLOAD_URL="${BASE_URL}/proxypanel-linux-amd64"
            ;;
        aarch64|arm64)
            # 修复：使用全小写的文件名
            DOWNLOAD_URL="${BASE_URL}/proxypanel-linux-arm64"
            ;;
        *)
            log_error "不支持的系统架构: $ARCH"
            exit 1
            ;;
    esac
    
    log_info "检测到系统架构: $ARCH"
    log_info "下载节点: Github 加速代理 (jht126.eu.org)"

    # 1. 下载 (增加 -L 参数以支持可能存在的重定向)
    log_info "正在下载主程序..."
    wget -q --show-progress -O "$BIN_PATH" "$DOWNLOAD_URL"
    
    # 检查文件大小，防止下载到带有错误信息的空文件/HTML页面
    FILE_SIZE=$(stat -c%s "$BIN_PATH" 2>/dev/null || stat -f%z "$BIN_PATH" 2>/dev/null)
    if [ $? -ne 0 ] || [ "$FILE_SIZE" -lt 1000000 ]; then
        log_error "下载失败或文件不完整。请检查代理链接是否有效或 Release 是否已发布。"
        log_info "尝试下载的地址: $DOWNLOAD_URL"
        rm -f "$BIN_PATH"
        read -p "按回车键返回..."
        return
    fi
    
    chmod +x "$BIN_PATH"
    log_success "下载成功，已安装至: ${CYAN}$BIN_PATH${PLAIN}"

    # 2. 配置服务
    log_info "正在配置系统服务..."
    
    if [ -f /etc/alpine-release ]; then
        # Alpine OpenRC
        cat > /etc/init.d/$SERVICE_NAME <<EOF
#!/sbin/openrc-run
name="proxypanel"
command="$BIN_PATH"
command_background=true
directory="$INSTALL_DIR"
pidfile="/run/${SERVICE_NAME}.pid"

depend() {
    need net
    after firewall
}
EOF
        chmod +x /etc/init.d/$SERVICE_NAME
        rc-update add $SERVICE_NAME default >/dev/null 2>&1
        service $SERVICE_NAME restart >/dev/null 2>&1
        log_success "Alpine OpenRC 服务配置完成"

    elif command -v systemctl >/dev/null; then
        # Debian Systemd
        cat > /etc/systemd/system/${SERVICE_NAME}.service <<EOF
[Unit]
Description=Go Proxy Panel Service
After=network.target

[Service]
Type=simple
WorkingDirectory=$INSTALL_DIR
ExecStart=$BIN_PATH
Restart=always
User=root
LimitNOFILE=65535

[Install]
WantedBy=multi-user.target
EOF
        systemctl daemon-reload
        systemctl enable $SERVICE_NAME >/dev/null 2>&1
        systemctl restart $SERVICE_NAME
        log_success "Systemd 服务配置完成"
    else
        log_warn "未识别的初始化系统，仅完成了文件下载，未配置自启。"
    fi

    # 3. 获取 IP 地址
    log_info "正在检测服务器公网 IP 地址..."
    SERVER_IP=$(wget -qO- -t1 -T2 ipv4.icanhazip.com)
    if [ -z "$SERVER_IP" ]; then
        SERVER_IP=$(wget -qO- -t1 -T2 ifconfig.me)
    fi
    if [ -z "$SERVER_IP" ]; then
        SERVER_IP="[你的服务器IP]"
    fi

    echo ""
    print_line
    echo -e " ${ICON_ROCKET} ${GREEN}Go Proxy Panel 安装并启动成功！${PLAIN}"
    print_line
    echo -e " 🟢 运行状态: ${GREEN}Active (后台运行中)${PLAIN}"
    echo -e " 📁 数据目录: ${CYAN}$INSTALL_DIR${PLAIN} (配置和数据库均保存在此)"
    echo -e " ${ICON_GLOBE} 登录面板: ${CYAN}${BOLD}http://${SERVER_IP}:8080${PLAIN}"
    echo ""
    echo -e " ${YELLOW}【重要提示】${PLAIN}"
    echo -e " 1. 请务必在服务器防火墙/安全组中放行 ${CYAN}80${PLAIN} 和 ${CYAN}443${PLAIN} 端口！"
    echo -e " 2. 只有端口放通且域名解析正确，自动 SSL 证书才能成功申请。"
    print_line
    echo ""
    read -p "按回车键返回主菜单..."
}

uninstall_proxypanel() {
    print_logo
    echo -e "${BOLD}正在卸载 Proxy Panel...${PLAIN}\n"

    # 停止并删除服务
    if [ -f /etc/alpine-release ]; then
        if [ -f /etc/init.d/$SERVICE_NAME ]; then
            service $SERVICE_NAME stop >/dev/null 2>&1
            rc-update del $SERVICE_NAME default >/dev/null 2>&1
            rm -f /etc/init.d/$SERVICE_NAME
            log_success "服务已停止并移除 (OpenRC)"
        fi
    elif command -v systemctl >/dev/null; then
        if [ -f /etc/systemd/system/${SERVICE_NAME}.service ]; then
            systemctl stop $SERVICE_NAME >/dev/null 2>&1
            systemctl disable $SERVICE_NAME >/dev/null 2>&1
            rm -f /etc/systemd/system/${SERVICE_NAME}.service
            systemctl daemon-reload
            log_success "服务已停止并移除 (Systemd)"
        fi
    fi

    # 询问是否保留数据
    echo ""
    read -p "❓ 是否保留数据库(proxy.db)和证书数据？(Y/n): " keep_data
    if [[ "$keep_data" == "n" || "$keep_data" == "N" ]]; then
        rm -rf "$INSTALL_DIR"
        log_success "主程序及所有配置数据均已彻底删除。"
    else
        rm -f "$BIN_PATH"
        log_success "仅删除了核心程序，数据库配置保存在 $INSTALL_DIR 目录中。"
    fi

    echo ""
    print_line
    echo -e " ${ICON_TRASH} ${GREEN}Proxy Panel 卸载流程执行完毕。${PLAIN}"
    print_line
    echo ""
    read -p "按回车键返回主菜单..."
}

# --- 菜单系统 ---

show_menu() {
    check_root
    while true; do
        print_logo
        echo -e " ${GREEN}1.${PLAIN} 安装 Proxy Panel ${YELLOW}(Install)${PLAIN}"
        echo -e " ${GREEN}2.${PLAIN} 卸载 Proxy Panel ${YELLOW}(Uninstall)${PLAIN}"
        echo -e " ${GREEN}0.${PLAIN} 退出脚本 ${YELLOW}(Exit)${PLAIN}"
        echo ""
        print_line
        echo ""
        read -p " 请输入选项 [0-2]: " choice
        
        case "$choice" in
            1) install_proxypanel ;;
            2) uninstall_proxypanel ;;
            0) exit 0 ;;
            *) echo -e "\n${RED}输入无效，请重新输入...${PLAIN}"; sleep 1 ;;
        esac
    done
}

# --- 入口处理 ---

if [ "$1" == "install" ]; then
    check_root
    install_proxypanel
    exit 0
elif [ "$1" == "uninstall" ]; then
    check_root
    uninstall_proxypanel
    exit 0
else
    show_menu
fi
