package app

import (
	"html/template"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"blog/internal/data"
)

var uploadBackgroundNamePattern = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

// baseData 组装模板渲染需要的基础数据。
func (a *App) baseData(r *http.Request, nav string) ViewData {
	settings, publicPages, stats := a.loadCommonData()
	siteBackground := normalizeBackgroundSetting(settings["site_background"], allowedSiteBackgrounds, defaultSiteBackground)
	adminBackground := normalizeBackgroundSetting(settings["admin_background"], allowedAdminBackgrounds, defaultAdminBackground)
	settings["site_background"] = siteBackground
	settings["admin_background"] = adminBackground

	var user = (*data.User)(nil)
	sidebarName := "访客"
	sidebarPosts := stats.Posts
	sidebarPages := len(publicPages)
	sidebarViews := stats.Views
	searchScope := "post"
	searchScopeLabel := "文章"
	searchFrom := "/"

	if r != nil {
		if u, err := a.currentUser(r); err == nil {
			user = u
		}
		searchFrom = r.URL.Path
		// 在搜索页继续检索时，沿用原始来源页面，保持“当前页面搜索”语义。
		if r.URL.Path == "/search" {
			if from := strings.TrimSpace(r.URL.Query().Get("from")); strings.HasPrefix(from, "/") {
				searchFrom = from
			}
		}
		if isPagePath(r.URL.Path) {
			searchScope = "page"
			searchScopeLabel = "页面"
		}
	}

	// 前台头像卡片中的统计信息，登录后统一展示“当前用户自己的数据”，
	// 保持与访问者后台的统计口径一致，避免前后台数字不一致造成困惑。
	if user != nil {
		sidebarName = user.DisplayName
		ownPosts, _ := a.store.ListPostsByAuthor("post", user.ID)
		ownPages, _ := a.store.ListPostsByAuthor("page", user.ID)
		sidebarPosts = len(ownPosts)
		sidebarPages = len(ownPages)
		sidebarViews = 0
		for _, post := range ownPosts {
			sidebarViews += int(post.Views)
		}
	}

	unreadCount := 0
	if user != nil {
		if n, err := a.store.CountUnreadNotifications(user.ID); err == nil {
			unreadCount = n
		}
	}

	return ViewData{
		Site:               a.site,
		Settings:           settings,
		SiteBackgroundCSS:  template.CSS(siteBackground),
		AdminBackgroundCSS: template.CSS(adminBackground),
		User:               user,
		Nav:                nav,
		Stats:              stats,
		SearchFrom:         searchFrom,
		SearchScope:        searchScope,
		SearchScopeLabel:   searchScopeLabel,
		SidebarName:        sidebarName,
		SidebarPosts:       sidebarPosts,
		SidebarPages:       sidebarPages,
		SidebarViews:       sidebarViews,
		UnreadCount:        unreadCount,
		// 统一在这里计算权限，模板渲染可直接判断，避免每个 handler 重复逻辑。
		CanManageContent: canManageContent(user),
		CanManageUsers:   canManageUsers(user),
		CanAccessMember:  canAccessMember(user),
		BackendHome:      backendHome(user),
	}
}

// normalizeBackgroundSetting 统一规范背景配置值：
// 1) 允许白名单中的纯色/渐变/内置图；
// 2) 允许本地上传目录中的图片背景；
// 3) 其他值一律回退到默认值，防止无效值导致背景不生效。
func normalizeBackgroundSetting(raw string, allowed map[string]struct{}, fallback string) string {
	choice := strings.TrimSpace(raw)
	if choice == "" {
		return fallback
	}
	if _, ok := allowed[choice]; ok {
		return choice
	}
	if isAllowedUploadBackground(choice) {
		return choice
	}
	return fallback
}

// isAllowedUploadBackground 校验上传背景字符串格式，仅允许 /static/uploads/backgrounds 下的文件。
func isAllowedUploadBackground(value string) bool {
	value = strings.TrimSpace(value)
	type format struct {
		prefix   string
		suffixes []string
	}
	formats := []format{
		{
			prefix: "url('/static/uploads/backgrounds/",
			suffixes: []string{
				"') center / cover no-repeat fixed",
				"') center top / cover no-repeat fixed",
			},
		},
		{
			prefix: "url(/static/uploads/backgrounds/",
			suffixes: []string{
				") center / cover no-repeat fixed",
				") center top / cover no-repeat fixed",
			},
		},
	}

	for _, f := range formats {
		if !strings.HasPrefix(value, f.prefix) {
			continue
		}
		for _, suffix := range f.suffixes {
			if strings.HasSuffix(value, suffix) {
				name := strings.TrimSuffix(strings.TrimPrefix(value, f.prefix), suffix)
				return uploadBackgroundNamePattern.MatchString(name)
			}
		}
	}
	return false
}

// isPagePath 判断当前请求是否属于“页面板块”。
func isPagePath(path string) bool {
	switch {
	case strings.HasPrefix(path, "/page/"):
		return true
	case path == "/about", path == "/say", path == "/board":
		return true
	default:
		return false
	}
}

// parseInt64 安全解析表单中的数字字段。
func parseInt64(value string) int64 {
	parsed, _ := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	return parsed
}

// parseBool 解析复选框值。
func parseBool(value string) bool {
	v := strings.TrimSpace(strings.ToLower(value))
	return v == "1" || v == "true" || v == "on" || v == "yes"
}

// parseTime 解析表单中的时间字符串。
func parseTime(value string) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}
	}
	t, err := time.Parse("2006-01-02 15:04", value)
	if err != nil {
		return time.Time{}
	}
	return t
}

// parseIDList 把“1,2,3”这类顺序字符串解析成 ID 切片。
// 该函数用于页面管理拖拽排序保存，自动去掉空值、非法值和重复值。
func parseIDList(raw string) []int64 {
	parts := strings.Split(strings.TrimSpace(raw), ",")
	result := make([]int64, 0, len(parts))
	seen := make(map[int64]struct{}, len(parts))
	for _, part := range parts {
		id := parseInt64(part)
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	return result
}

// clientIP 尽量提取真实客户端 IP（兼容反向代理）。
func clientIP(r *http.Request) string {
	if r == nil {
		return ""
	}

	// 1) 优先读取 X-Forwarded-For 的第一个地址。
	// 这个头通常由 Nginx/Caddy 等代理写入，最左侧是原始客户端 IP。
	if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); forwarded != "" {
		parts := strings.Split(forwarded, ",")
		if len(parts) > 0 {
			return strings.TrimSpace(parts[0])
		}
	}

	// 2) 如果没有 X-Forwarded-For，再尝试 X-Real-IP。
	if realIP := strings.TrimSpace(r.Header.Get("X-Real-IP")); realIP != "" {
		return realIP
	}

	// 3) 最后回退到 RemoteAddr（格式通常是 ip:port）。
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err == nil {
		return host
	}
	// 极端情况下 RemoteAddr 可能不含端口，直接返回原值。
	return strings.TrimSpace(r.RemoteAddr)
}

// canManageContent 表示是否可访问文章/页面/留言/外观等管理模块。
func canManageContent(user *data.User) bool {
	if user == nil {
		return false
	}
	return user.Role == data.RoleOwner || user.Role == data.RoleMaintainer
}

// canManageUsers 表示是否可访问“用户管理”模块。
func canManageUsers(user *data.User) bool {
	if user == nil {
		return false
	}
	return user.Role == data.RoleOwner
}

// canAccessMember 表示是否应进入访问者后台。
func canAccessMember(user *data.User) bool {
	if user == nil {
		return false
	}
	return user.Role == data.RoleVisitor
}

// backendHome 统一计算“后台入口”。
// 访问者进入 /member，其余有后台权限的角色进入 /admin。
func backendHome(user *data.User) string {
	if user == nil {
		return "/admin"
	}
	if user.Role == data.RoleVisitor {
		return "/member"
	}
	return "/admin"
}
