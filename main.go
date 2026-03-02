package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/acme/autocert"
	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite" // 纯 Go 实现的 SQLite 驱动
)

// --- 数据模型与内存缓存 ---

type ProxyRule struct {
	Domain    string
	Target    string
	Enabled   bool
	IsHealthy bool
}

type Config struct {
	AdminUser          string
	AdminPass          string
	IsSetup            bool
	GithubClientID     string
	GithubClientSecret string
	GithubUser         string
	ProxyRules         map[string]ProxyRule
}

var (
	db        *sql.DB
	appConfig Config
	configMu  sync.RWMutex
	certMgr   *autocert.Manager
	sessions  = make(map[string]time.Time)
	sessMu    sync.RWMutex
)

// --- 数据库初始化与加载 (核心更新) ---

func initDB() {
	var err error
	db, err = sql.Open("sqlite", "proxy.db")
	if err != nil {
		log.Fatal("无法连接数据库:", err)
	}

	// 1. 创建设置表和规则表
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS settings (
			id TEXT PRIMARY KEY,
			val TEXT
		);
		CREATE TABLE IF NOT EXISTS proxy_rules (
			domain TEXT PRIMARY KEY,
			target TEXT,
			enabled INTEGER
		);
	`)
	if err != nil {
		log.Fatal("初始化数据库表失败:", err)
	}

	// 2. 初始化内存结构
	configMu.Lock()
	defer configMu.Unlock()
	appConfig = Config{ProxyRules: make(map[string]ProxyRule)}

	// 3. 从数据库加载全局设置
	rows, _ := db.Query("SELECT id, val FROM settings")
	defer rows.Close()
	for rows.Next() {
		var id, val string
		rows.Scan(&id, &val)
		switch id {
		case "admin_user": appConfig.AdminUser = val
		case "admin_pass": appConfig.AdminPass = val
		case "is_setup": appConfig.IsSetup, _ = strconv.ParseBool(val)
		case "github_client_id": appConfig.GithubClientID = val
		case "github_client_secret": appConfig.GithubClientSecret = val
		case "github_user": appConfig.GithubUser = val
		}
	}

	// 4. 从数据库加载代理规则
	ruleRows, _ := db.Query("SELECT domain, target, enabled FROM proxy_rules")
	defer ruleRows.Close()
	for ruleRows.Next() {
		var r ProxyRule
		var enabledInt int
		ruleRows.Scan(&r.Domain, &r.Target, &enabledInt)
		r.Enabled = enabledInt == 1
		r.IsHealthy = true // 初始默认健康，交由巡检程序检查
		appConfig.ProxyRules[r.Domain] = r
	}
}

// 保存系统设置到数据库
func saveSettingsToDB() {
	tx, _ := db.Begin()
	tx.Exec("INSERT OR REPLACE INTO settings (id, val) VALUES (?, ?)", "admin_user", appConfig.AdminUser)
	tx.Exec("INSERT OR REPLACE INTO settings (id, val) VALUES (?, ?)", "admin_pass", appConfig.AdminPass)
	tx.Exec("INSERT OR REPLACE INTO settings (id, val) VALUES (?, ?)", "is_setup", strconv.FormatBool(appConfig.IsSetup))
	tx.Exec("INSERT OR REPLACE INTO settings (id, val) VALUES (?, ?)", "github_client_id", appConfig.GithubClientID)
	tx.Exec("INSERT OR REPLACE INTO settings (id, val) VALUES (?, ?)", "github_client_secret", appConfig.GithubClientSecret)
	tx.Exec("INSERT OR REPLACE INTO settings (id, val) VALUES (?, ?)", "github_user", appConfig.GithubUser)
	tx.Commit()
}

// 增/改规则到数据库
func saveRuleToDB(r ProxyRule) {
	enabledInt := 0
	if r.Enabled { enabledInt = 1 }
	db.Exec("INSERT OR REPLACE INTO proxy_rules (domain, target, enabled) VALUES (?, ?, ?)", r.Domain, r.Target, enabledInt)
}

// 从数据库删规则
func deleteRuleFromDB(domain string) {
	db.Exec("DELETE FROM proxy_rules WHERE domain = ?", domain)
}

// --- Let's Encrypt 证书策略 ---

func hostPolicy() autocert.HostPolicy {
	return func(ctx context.Context, host string) error {
		configMu.RLock()
		defer configMu.RUnlock()
		if _, exists := appConfig.ProxyRules[host]; exists {
			return nil
		}
		return fmt.Errorf("acme/autocert: host not configured: %s", host)
	}
}

// --- 后台任务 ---

func triggerSSLRequest(domain string) {
	log.Printf("⏳ 正在为 %s 主动触发 SSL 证书申请...", domain)
	client := &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		Timeout:   15 * time.Second,
	}
	resp, err := client.Get("https://" + domain)
	if err == nil {
		resp.Body.Close()
		log.Printf("✅ %s 的 SSL 证书主动申请成功！", domain)
	}
}

func healthCheckLoop() {
	for {
		configMu.Lock()
		for k, rule := range appConfig.ProxyRules {
			if !rule.Enabled { continue }
			targetUrl, err := url.Parse(rule.Target)
			if err == nil {
				host := targetUrl.Host
				if !strings.Contains(host, ":") {
					if targetUrl.Scheme == "https" { host += ":443" } else { host += ":80" }
				}
				conn, err := net.DialTimeout("tcp", host, 3*time.Second)
				if err != nil { rule.IsHealthy = false } else { rule.IsHealthy = true; conn.Close() }
				appConfig.ProxyRules[k] = rule
			}
		}
		configMu.Unlock()
		time.Sleep(30 * time.Second)
	}
}

// --- 安全与 Session ---

func generateToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{Name: "session_token", Value: token, Expires: time.Now().Add(24 * time.Hour), HttpOnly: true, SameSite: http.SameSiteLaxMode, Path: "/"})
}

func isValidSession(r *http.Request) bool {
	cookie, err := r.Cookie("session_token")
	if err != nil { return false }
	sessMu.RLock()
	defer sessMu.RUnlock()
	expiry, exists := sessions[cookie.Value]
	return exists && time.Now().Before(expiry)
}

// --- GitHub OAuth2 逻辑 ---

func handleGithubLogin(w http.ResponseWriter, r *http.Request) {
	configMu.RLock()
	clientID := appConfig.GithubClientID
	configMu.RUnlock()
	if clientID == "" { http.Error(w, "GitHub OAuth is not configured", http.StatusBadRequest); return }

	state := generateToken()
	http.SetCookie(w, &http.Cookie{Name: "oauth_state", Value: state, Expires: time.Now().Add(5 * time.Minute), HttpOnly: true})
	http.Redirect(w, r, fmt.Sprintf("https://github.com/login/oauth/authorize?client_id=%s&state=%s", clientID, state), http.StatusFound)
}

func handleGithubCallback(w http.ResponseWriter, r *http.Request) {
	configMu.RLock()
	clientID, clientSecret, allowedUser := appConfig.GithubClientID, appConfig.GithubClientSecret, appConfig.GithubUser
	configMu.RUnlock()

	stateCookie, err := r.Cookie("oauth_state")
	if err != nil || r.FormValue("state") != stateCookie.Value { http.Error(w, "State mismatch.", http.StatusForbidden); return }
	code := r.FormValue("code")
	if code == "" { http.Error(w, "Code not found", http.StatusBadRequest); return }

	tokenReqBody := fmt.Sprintf(`{"client_id":"%s","client_secret":"%s","code":"%s"}`, clientID, clientSecret, code)
	req, _ := http.NewRequest("POST", "https://github.com/login/oauth/access_token", bytes.NewBuffer([]byte(tokenReqBody)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil { http.Error(w, "Failed to get token", http.StatusInternalServerError); return }
	defer resp.Body.Close()

	var tokenRes struct { AccessToken string `json:"access_token"` }
	json.NewDecoder(resp.Body).Decode(&tokenRes)
	if tokenRes.AccessToken == "" { http.Error(w, "Invalid token response", http.StatusUnauthorized); return }

	reqUser, _ := http.NewRequest("GET", "https://api.github.com/user", nil)
	reqUser.Header.Set("Authorization", "Bearer "+tokenRes.AccessToken)
	respUser, err := client.Do(reqUser)
	if err != nil { http.Error(w, "Failed to get user info", http.StatusInternalServerError); return }
	defer respUser.Body.Close()

	var userRes struct { Login string `json:"login"` }
	json.NewDecoder(respUser.Body).Decode(&userRes)

	if !strings.EqualFold(userRes.Login, allowedUser) {
		http.Error(w, fmt.Sprintf("Access Denied: GitHub user '%s' is not authorized.", userRes.Login), http.StatusForbidden)
		return
	}

	token := generateToken()
	sessMu.Lock()
	sessions[token] = time.Now().Add(24 * time.Hour)
	sessMu.Unlock()
	setSessionCookie(w, token)
	http.Redirect(w, r, "/", http.StatusFound)
}

// --- 核心反向代理 ---

func proxyHandler(w http.ResponseWriter, r *http.Request) {
	configMu.RLock()
	rule, exists := appConfig.ProxyRules[r.Host]
	configMu.RUnlock()

	if !exists { http.Error(w, "404 - Domain not found", http.StatusNotFound); return }
	if !rule.Enabled { http.Error(w, "503 - Service Disabled", http.StatusServiceUnavailable); return }

	targetUrl, _ := url.Parse(rule.Target)
	proxy := httputil.NewSingleHostReverseProxy(targetUrl)
	d := proxy.Director
	proxy.Director = func(req *http.Request) {
		d(req)
		req.Header.Set("X-Real-IP", r.RemoteAddr)
		req.Header.Set("X-Forwarded-For", r.RemoteAddr)
		req.Header.Set("X-Forwarded-Host", r.Header.Get("Host"))
		req.Header.Set("X-Forwarded-Proto", "https")
	}
	proxy.ServeHTTP(w, r)
}

// --- 路由与控制器 ---

func authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		configMu.RLock()
		isSetup := appConfig.IsSetup
		configMu.RUnlock()

		if !isSetup { http.Redirect(w, r, "/setup", http.StatusFound); return }
		if !isValidSession(r) { http.Redirect(w, r, "/login", http.StatusFound); return }
		next(w, r)
	}
}

func handleSetup(w http.ResponseWriter, r *http.Request) {
	configMu.RLock()
	if appConfig.IsSetup { configMu.RUnlock(); http.Redirect(w, r, "/", http.StatusFound); return }
	configMu.RUnlock()

	if r.Method == "POST" {
		user, pass := r.FormValue("username"), r.FormValue("password")
		if user == "" || pass == "" { tmplSetup.Execute(w, map[string]string{"Error": "不能为空"}); return }
		hash, _ := bcrypt.GenerateFromPassword([]byte(pass), bcrypt.DefaultCost)
		
		configMu.Lock()
		appConfig.AdminUser = user
		appConfig.AdminPass = string(hash)
		appConfig.IsSetup = true
		saveSettingsToDB() // 写入数据库
		configMu.Unlock()
		
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	tmplSetup.Execute(w, nil)
}

func handleLogin(w http.ResponseWriter, r *http.Request) {
	configMu.RLock()
	isSetup, u, p := appConfig.IsSetup, appConfig.AdminUser, appConfig.AdminPass
	ghEnabled := appConfig.GithubClientID != ""
	configMu.RUnlock()

	if !isSetup { http.Redirect(w, r, "/setup", http.StatusFound); return }
	if isValidSession(r) { http.Redirect(w, r, "/", http.StatusFound); return }

	if r.Method == "POST" {
		user, pass := r.FormValue("username"), r.FormValue("password")
		if user != u || bcrypt.CompareHashAndPassword([]byte(p), []byte(pass)) != nil {
			tmplLogin.Execute(w, map[string]interface{}{"Error": "账号或密码错误", "GithubEnabled": ghEnabled})
			return
		}
		token := generateToken()
		sessMu.Lock()
		sessions[token] = time.Now().Add(24 * time.Hour)
		sessMu.Unlock()
		setSessionCookie(w, token)
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	tmplLogin.Execute(w, map[string]interface{}{"GithubEnabled": ghEnabled})
}

func handleSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method == "POST" {
		configMu.Lock()
		if pass := r.FormValue("password"); pass != "" {
			hash, _ := bcrypt.GenerateFromPassword([]byte(pass), bcrypt.DefaultCost)
			appConfig.AdminPass = string(hash)
		}
		appConfig.GithubClientID = strings.TrimSpace(r.FormValue("github_client_id"))
		appConfig.GithubClientSecret = strings.TrimSpace(r.FormValue("github_client_secret"))
		appConfig.GithubUser = strings.TrimSpace(r.FormValue("github_user"))
		saveSettingsToDB() // 写入数据库
		configMu.Unlock()
		
		http.Redirect(w, r, "/settings?success=1", http.StatusFound)
		return
	}
	configMu.RLock()
	data := map[string]interface{}{"Config": appConfig, "Success": r.URL.Query().Get("success") == "1"}
	configMu.RUnlock()
	tmplSettings.Execute(w, data)
}

func handleLogout(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("session_token")
	if err == nil {
		sessMu.Lock()
		delete(sessions, cookie.Value)
		sessMu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: "session_token", Value: "", MaxAge: -1, Path: "/"})
	http.Redirect(w, r, "/login", http.StatusFound)
}

func handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.Method == "POST" {
		action, domain, target := r.FormValue("action"), strings.TrimSpace(r.FormValue("domain")), strings.TrimSpace(r.FormValue("target"))
		configMu.Lock()
		if action == "add" && domain != "" && target != "" {
			if !strings.HasPrefix(target, "http://") && !strings.HasPrefix(target, "https://") { target = "http://" + target }
			newRule := ProxyRule{Domain: domain, Target: target, Enabled: true, IsHealthy: true}
			appConfig.ProxyRules[domain] = newRule
			saveRuleToDB(newRule) // 写入数据库
			go triggerSSLRequest(domain)
		} else if action == "delete" && domain != "" {
			delete(appConfig.ProxyRules, domain)
			deleteRuleFromDB(domain) // 从数据库删除
		} else if action == "toggle" && domain != "" {
			if rule, ok := appConfig.ProxyRules[domain]; ok {
				rule.Enabled = !rule.Enabled
				appConfig.ProxyRules[domain] = rule
				saveRuleToDB(rule) // 写入数据库
			}
		}
		configMu.Unlock()
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	configMu.RLock()
	defer configMu.RUnlock()
	tmplDashboard.Execute(w, map[string]interface{}{"Rules": appConfig.ProxyRules})
}

// --- HTML/CSS 模板 ---
const cssGlobal = `
:root { --primary: #000000; --bg: #f9fafb; --sidebar: #111827; --card-bg: #ffffff; --text: #1f2937; --text-muted: #6b7280; --danger: #ef4444; --success: #10b981; --warning: #f59e0b; }
body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; margin: 0; background-color: var(--bg); color: var(--text); }
.auth-container { display: flex; justify-content: center; align-items: center; min-height: 100vh; }
.card { background: var(--card-bg); padding: 2rem; border-radius: 12px; box-shadow: 0 4px 6px -2px rgba(0,0,0,0.05), border: 1px solid #e5e7eb; }
.form-group label { display: block; margin-bottom: 0.5rem; font-size: 0.875rem; color: var(--text-muted); font-weight: 500;}
.form-control { width: 100%; padding: 0.75rem 1rem; border: 1px solid #d1d5db; border-radius: 8px; box-sizing: border-box; }
.form-control:focus { outline: none; border-color: #9ca3af; box-shadow: 0 0 0 3px rgba(156, 163, 175, 0.2); }
.btn { height: 42px; padding: 0 1.5rem; border-radius: 8px; border: none; cursor: pointer; font-size: 0.875rem; color: white; font-weight: 500; display: inline-flex; justify-content: center; align-items: center; gap: 8px; box-sizing: border-box; text-decoration: none; width: 100%;}
.btn-primary { background-color: var(--primary); }
.btn-danger { background-color: var(--danger); }
.btn-warning { background-color: var(--warning); }
.btn-github { background-color: #24292e; }
.btn-sm { height: auto; padding: 0.4rem 0.8rem; font-size: 0.75rem; width: auto; display: inline-block; }
.layout { display: flex; height: 100vh; }
.sidebar { width: 260px; background-color: var(--sidebar); color: white; padding: 1rem 0; display: flex; flex-direction: column;}
.sidebar a { display: block; padding: 1rem 1.5rem; color: #d1d5db; text-decoration: none; transition: 0.2s;}
.sidebar a:hover, .sidebar a.active { background: #1f2937; color: white; border-left: 4px solid #ffffff; }
.main-content { flex: 1; overflow-y: auto; padding: 2.5rem; }
table { width: 100%; border-collapse: collapse; background: white; border-radius: 12px; overflow: hidden; box-shadow: 0 1px 3px rgba(0,0,0,0.1); border: 1px solid #e5e7eb;}
th { background-color: #f9fafb; padding: 1rem; text-align: left; font-size: 0.75rem; color: var(--text-muted); border-bottom: 1px solid #e5e7eb;}
td { padding: 1rem; border-bottom: 1px solid #e5e7eb; }
.badge { display: inline-block; padding: 0.25rem 0.5rem; border-radius: 4px; font-size: 0.75rem; font-weight: bold; }
.bg-green { background-color: #d1fae5; color: #065f46; }
.bg-red { background-color: #fee2e2; color: #991b1b; }
.bg-gray { background-color: #f3f4f6; color: #374151; }
.divider { display: flex; align-items: center; text-align: center; margin: 1.5rem 0; color: #9ca3af; font-size: 0.875rem;}
.divider::before, .divider::after { content: ''; flex: 1; border-bottom: 1px solid #e5e7eb; }
.divider:not(:empty)::before { margin-right: .5em; }
.divider:not(:empty)::after { margin-left: .5em; }
`
var tmplSetup = template.Must(template.New("s").Parse(`<!DOCTYPE html><html><head><style>`+cssGlobal+`</style></head><body><div class="auth-container"><div class="card" style="width:360px"><h2 style="text-align:center">初始化系统</h2>{{if .Error}}<div style="color:red;text-align:center;margin-bottom:1rem">{{.Error}}</div>{{end}}<form method="POST"><div class="form-group"><label>设置本地管理员账号</label><input type="text" name="username" class="form-control" required placeholder="admin"></div><div class="form-group"><label>设置安全密码</label><input type="password" name="password" class="form-control" required placeholder="••••••••"></div><button type="submit" class="btn btn-primary" style="margin-top:1rem">完成设置并进入控制台</button></form></div></div></body></html>`))
var tmplLogin = template.Must(template.New("l").Parse(`<!DOCTYPE html><html><head><style>`+cssGlobal+`</style></head><body><div class="auth-container"><div class="card" style="width:360px"><h2 style="text-align:center">控制台登录</h2>{{if .Error}}<div style="color:red;text-align:center;margin-bottom:1rem;background:#fee2e2;padding:8px;border-radius:6px">{{.Error}}</div>{{end}}<form method="POST"><div class="form-group"><label>用户名</label><input type="text" name="username" class="form-control" required></div><div class="form-group"><label>密码</label><input type="password" name="password" class="form-control" required></div><button type="submit" class="btn btn-primary" style="margin-top:0.5rem">密码登录</button></form>{{if .GithubEnabled}}<div class="divider">或</div><a href="/auth/github/login" class="btn btn-github"><svg height="20" viewBox="0 0 16 16" width="20" fill="white"><path d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82.64-.18 1.32-.27 2-.27.68 0 1.36.09 2 .27 1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.013 8.013 0 0016 8c0-4.42-3.58-8-8-8z"></path></svg> 使用 GitHub 登录</a>{{end}}</div></div></body></html>`))
var tmplDashboard = template.Must(template.New("d").Parse(`<!DOCTYPE html><html><head><title>代理控制台</title><style>`+cssGlobal+`</style></head><body><div class="layout"><div class="sidebar"><h3 style="text-align:center;margin-bottom:2rem; letter-spacing: 1px;">⚡ PROXY PANEL</h3><a href="/" class="active">🛡️ 代理路由配置</a><a href="/settings">⚙️ 系统安全设置</a><div style="flex:1"></div><a href="/logout" style="text-align:center; color: #fca5a5; margin-bottom: 1rem;">退出登录</a></div><div class="main-content"><div class="card" style="margin-bottom:2rem; background: #f8fafc;"><form method="POST" style="display:flex;gap:1rem;align-items:flex-end"><input type="hidden" name="action" value="add"><div class="form-group" style="flex:1;margin:0"><label>对外访问域名 (需提前解析A记录)</label><input type="text" name="domain" class="form-control" placeholder="例如: app.yourdomain.com" required></div><div class="form-group" style="flex:1;margin:0"><label>内网目标服务 (IP:Port 或 域名)</label><input type="text" name="target" class="form-control" placeholder="例如: 127.0.0.1:3000" required></div><button type="submit" class="btn btn-primary" style="width: auto;">添加并触发 SSL 申请</button></form></div><table><tr><th>外部域名</th><th>内网目标</th><th>代理状态</th><th>节点健康度 (30秒刷新)</th><th>操作</th></tr>{{range .Rules}}<tr><td><a href="https://{{.Domain}}" target="_blank" style="color:var(--text);text-decoration:none;font-weight:600">{{.Domain}} ↗</a></td><td style="color:var(--text-muted)">{{.Target}}</td><td>{{if .Enabled}}<span class="badge bg-green">生效中</span>{{else}}<span class="badge bg-gray">已暂停转发</span>{{end}}</td><td>{{if .IsHealthy}}<span class="badge bg-green">🟢 在线连接正常</span>{{else}}<span class="badge bg-red">🔴 离线 (目标无响应)</span>{{end}}</td><td><form method="POST" style="display:inline"><input type="hidden" name="action" value="toggle"><input type="hidden" name="domain" value="{{.Domain}}"><button type="submit" class="btn btn-sm {{if .Enabled}}btn-warning{{else}}btn-primary{{end}}">{{if .Enabled}}暂停{{else}}启用{{end}}</button></form><form method="POST" style="display:inline;margin-left:5px" onsubmit="return confirm('确认要永久删除该代理规则吗？\n删除后相关的 SSL 证书将不再续期。')"><input type="hidden" name="action" value="delete"><input type="hidden" name="domain" value="{{.Domain}}"><button type="submit" class="btn btn-sm btn-danger">删除</button></form></td></tr>{{else}}<tr><td colspan="5" style="text-align:center;padding:3rem;color:var(--text-muted)">当前没有配置任何代理规则，请在上方添加</td></tr>{{end}}</table></div></div></body></html>`))
var tmplSettings = template.Must(template.New("set").Parse(`<!DOCTYPE html><html><head><title>系统设置</title><style>`+cssGlobal+`</style></head><body><div class="layout"><div class="sidebar"><h3 style="text-align:center;margin-bottom:2rem; letter-spacing: 1px;">⚡ PROXY PANEL</h3><a href="/">🛡️ 代理路由配置</a><a href="/settings" class="active">⚙️ 系统安全设置</a><div style="flex:1"></div><a href="/logout" style="text-align:center; color: #fca5a5; margin-bottom: 1rem;">退出登录</a></div><div class="main-content"><h2 style="margin-top:0">⚙️ 系统安全设置</h2>{{if .Success}}<div style="background:#d1fae5;color:#065f46;padding:12px;border-radius:8px;margin-bottom:1.5rem;font-weight:500">配置已成功保存生效！</div>{{end}}<form method="POST" class="card" style="max-width: 600px;"><h3 style="margin-top:0; border-bottom: 1px solid #e5e7eb; padding-bottom: 10px;">修改本地密码</h3><div class="form-group"><label>新管理密码 (留空则不修改)</label><input type="password" name="password" class="form-control" placeholder="输入新密码"></div><h3 style="margin-top:2rem; border-bottom: 1px solid #e5e7eb; padding-bottom: 10px;">绑定 GitHub OAuth 登录</h3><p style="font-size:0.875rem; color:var(--text-muted); line-height: 1.6;">配置 GitHub 后，可实现免密一键安全登录。请在 GitHub Developer Settings 中创建 OAuth App，并将 <b>Authorization callback URL</b> 设置为:<br><code style="background:#f3f4f6;padding:4px 8px;border-radius:4px;color:var(--primary);user-select:all;">http(s)://你的面板域名或IP:8080/auth/github/callback</code></p><div class="form-group"><label>Client ID</label><input type="text" name="github_client_id" class="form-control" value="{{.Config.GithubClientID}}" placeholder="GitHub OAuth Client ID"></div><div class="form-group"><label>Client Secret</label><input type="password" name="github_client_secret" class="form-control" value="{{.Config.GithubClientSecret}}" placeholder="GitHub OAuth Client Secret"></div><div class="form-group"><label>允许登录的 GitHub 用户名 (重要！)</label><input type="text" name="github_user" class="form-control" value="{{.Config.GithubUser}}" placeholder="你的 GitHub 账号名，例如: torvalds"></div><button type="submit" class="btn btn-primary" style="margin-top: 1rem; width: auto;">保存所有配置</button></form></div></div></body></html>`))


// --- 启动入口 ---


func main() {
	initDB() // <--- 核心：启动时连接 SQLite 数据库并加载内存
	
	certMgr = &autocert.Manager{ Prompt: autocert.AcceptTOS, HostPolicy: hostPolicy(), Cache: autocert.DirCache("certs") }
	go healthCheckLoop()

	go func() {
		panelMux := http.NewServeMux()
		panelMux.HandleFunc("/setup", handleSetup)
		panelMux.HandleFunc("/login", handleLogin)
		panelMux.HandleFunc("/logout", handleLogout)
		panelMux.HandleFunc("/auth/github/login", handleGithubLogin)
		panelMux.HandleFunc("/auth/github/callback", handleGithubCallback)
		panelMux.HandleFunc("/settings", authMiddleware(handleSettings))
		panelMux.HandleFunc("/", authMiddleware(handleDashboard))
		
		log.Println("🚀 面板启动成功: http://0.0.0.0:8080 (使用 SQLite 持久化存储)")
		if err := http.ListenAndServe(":8080", panelMux); err != nil { log.Fatal(err) }
	}()

	go func() { if err := http.ListenAndServe(":80", certMgr.HTTPHandler(nil)); err != nil { log.Fatal(err) } }()

	server := &http.Server{ Addr: ":443", Handler: http.HandlerFunc(proxyHandler), TLSConfig: certMgr.TLSConfig() }
	log.Println("🔒 反向代理启动成功: Port 443")
	if err := server.ListenAndServeTLS("", ""); err != nil { log.Fatal(err) }
}
