package app

import (
	"html/template"
	"net/http"
	"sync"
	"time"

	"blog/internal/data"
)

// Config 汇总应用启动所需的外部依赖。
type Config struct {
	Store      *data.Store
	Templates  *template.Template
	StaticDir  string
	Site       SiteConfig
	SessionTTL time.Duration

	AdminUser string
	AdminPass string
	AdminName string
}

// SiteConfig 表示站点的固定展示信息。
type SiteConfig struct {
	Title    string
	Subtitle string
	MottoCN  string
	MottoEN  string
	ICP      string
	Gongan   string
	Footer   string
}

// ViewData 统一传递给模板的数据结构。
type ViewData struct {
	Site     SiteConfig
	Settings map[string]string
	// SiteBackgroundCSS / AdminBackgroundCSS 使用 template.CSS 输出经过白名单校验的背景值，
	// 避免被模板安全过滤器降级后导致“背景设置不生效”。
	SiteBackgroundCSS  template.CSS
	AdminBackgroundCSS template.CSS
	User               *data.User

	Posts []data.Post
	Post  *data.Post
	Pages []data.Post
	Users []data.User
	// SelectedUser 用于“用户管理”页展示当前点开的用户详细信息。
	SelectedUser *data.User

	Comments           []data.Comment
	CommentReports     []data.CommentReport
	PostReports        []data.PostReport
	AvatarRequests     []data.AvatarRequest
	AuditLogs          []data.AuditLog
	Notifications      []data.Notification
	Sessions           []data.SessionInfo
	CommentFormName    string
	CommentFormContent string
	CommentAnonymous   bool
	// AuthFormUsername / AuthFormDisplayName 用于登录、注册失败时回填用户输入，减少重复输入成本。
	AuthFormUsername    string
	AuthFormDisplayName string
	// AuthNext 用于记录登录后的目标跳转地址。
	AuthNext string

	Query            string
	SearchFrom       string
	SearchScope      string
	SearchScopeLabel string
	HomeScope        string
	ActiveSlug       string
	Nav              string
	Stats            Stats
	SidebarName      string
	SidebarPosts     int
	SidebarPages     int
	SidebarViews     int
	UnreadCount      int

	CanManageContent bool
	CanManageUsers   bool
	CanAccessMember  bool
	BackendHome      string

	Flash string

	// PostAuthors 用于前台列表按文章作者 ID 显示“发布者名称”。
	PostAuthors map[int64]string
}

// Stats 后台统计数据。
type Stats struct {
	Posts int
	Views int
}

// App 聚合所有运行时依赖，便于在 Handler 中共享。
type App struct {
	store      *data.Store
	templates  *template.Template
	staticDir  string
	site       SiteConfig
	sessionTTL time.Duration

	adminUser string
	adminPass string
	adminName string

	// authLimiter 用于登录接口的内存限流，防止暴力破解密码。
	authLimiter *authRateLimiter

	cacheMu        sync.RWMutex
	cacheTTL       time.Duration
	cachedSettings map[string]string
	cachedPages    []data.Post
	cachedStats    Stats
	cachedLoadedAt time.Time
}

// New 创建应用实例。
func New(cfg Config) *App {
	return &App{
		store:      cfg.Store,
		templates:  cfg.Templates,
		staticDir:  cfg.StaticDir,
		site:       cfg.Site,
		sessionTTL: cfg.SessionTTL,
		adminUser:  cfg.AdminUser,
		adminPass:  cfg.AdminPass,
		adminName:  cfg.AdminName,
		authLimiter: newAuthRateLimiter(
			5,              // 失败 5 次
			10*time.Minute, // 在 10 分钟窗口内统计
			15*time.Minute, // 触发后封禁 15 分钟
		),
		cacheTTL: 5 * time.Second,
	}
}

// Routes 组装路由与静态资源处理。
func (a *App) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir(a.staticDir))))

	mux.HandleFunc("/", a.handleHome)
	mux.HandleFunc("/post/", a.handlePost)
	mux.HandleFunc("/archive", a.handleArchive)
	mux.HandleFunc("/page/", a.handlePage)
	mux.HandleFunc("/about", a.handlePageAlias("about"))
	mux.HandleFunc("/say", a.handlePageAlias("say"))
	mux.HandleFunc("/board", a.handleBoard)
	mux.HandleFunc("/board/react", a.handleBoardReact)
	mux.HandleFunc("/board/report", a.handleBoardReport)
	mux.HandleFunc("/report", a.handleContentReport)
	mux.HandleFunc("/post/report", a.handleContentReport)
	mux.HandleFunc("/search", a.handleSearch)
	mux.HandleFunc("/login", a.handleUserLogin)
	mux.HandleFunc("/register", a.handleUserRegister)
	mux.HandleFunc("/verify-email", a.handleVerifyEmail)
	mux.HandleFunc("/forgot-password", a.handleForgotPassword)
	mux.HandleFunc("/reset-password", a.handleResetPassword)
	mux.HandleFunc("/logout", a.handleUserLogout)

	mux.HandleFunc("/admin/login", a.handleAdminLogin)
	mux.HandleFunc("/admin/logout", a.handleAdminLogout)

	mux.HandleFunc("/admin", a.requireRoles(data.RoleOwner, data.RoleMaintainer)(a.handleAdminEntry))
	mux.HandleFunc("/admin/posts", a.requireRoles(data.RoleOwner, data.RoleMaintainer)(a.handleAdminPosts))
	mux.HandleFunc("/admin/posts/new", a.requireRoles(data.RoleOwner, data.RoleMaintainer)(a.handleAdminPostNew))
	mux.HandleFunc("/admin/posts/edit", a.requireRoles(data.RoleOwner, data.RoleMaintainer)(a.handleAdminPostEdit))
	mux.HandleFunc("/admin/posts/delete", a.requireRoles(data.RoleOwner, data.RoleMaintainer)(a.handleAdminPostDelete))
	mux.HandleFunc("/admin/pages", a.requireRoles(data.RoleOwner, data.RoleMaintainer)(a.handleAdminPages))
	mux.HandleFunc("/admin/pages/reorder", a.requireRoles(data.RoleOwner, data.RoleMaintainer)(a.handleAdminPagesReorder))
	mux.HandleFunc("/admin/pages/new", a.requireRoles(data.RoleOwner)(a.handleAdminPageNew))
	mux.HandleFunc("/admin/pages/edit", a.requireRoles(data.RoleOwner, data.RoleMaintainer)(a.handleAdminPageEdit))
	mux.HandleFunc("/admin/pages/delete", a.requireRoles(data.RoleOwner)(a.handleAdminPageDelete))
	mux.HandleFunc("/admin/pages/board-comments", a.requireRoles(data.RoleOwner, data.RoleMaintainer)(a.handleAdminBoardComments))
	mux.HandleFunc("/admin/pages/board-comments/toggle", a.requireRoles(data.RoleOwner, data.RoleMaintainer)(a.handleAdminCommentToggle))
	mux.HandleFunc("/admin/pages/board-comments/delete", a.requireRoles(data.RoleOwner, data.RoleMaintainer)(a.handleAdminCommentDelete))
	mux.HandleFunc("/admin/comments", a.requireRoles(data.RoleOwner, data.RoleMaintainer)(a.redirectAdminComments))
	mux.HandleFunc("/admin/comments/toggle", a.requireRoles(data.RoleOwner, data.RoleMaintainer)(a.handleAdminCommentToggle))
	mux.HandleFunc("/admin/comments/delete", a.requireRoles(data.RoleOwner, data.RoleMaintainer)(a.handleAdminCommentDelete))
	mux.HandleFunc("/admin/appearance", a.requireRoles(data.RoleOwner, data.RoleMaintainer)(a.handleAdminAppearance))
	mux.HandleFunc("/admin/appearance/upload", a.requireRoles(data.RoleOwner, data.RoleMaintainer)(a.handleAdminAppearanceUpload))
	mux.HandleFunc("/admin/avatar-reviews", a.requireRoles(data.RoleOwner, data.RoleMaintainer)(a.handleAdminAvatarReviews))
	mux.HandleFunc("/admin/reports", a.requireRoles(data.RoleOwner, data.RoleMaintainer)(a.handleAdminReports))
	mux.HandleFunc("/admin/users", a.requireRoles(data.RoleOwner)(a.handleAdminUsers))

	mux.HandleFunc("/member", a.requireRoles(data.RoleVisitor)(a.handleMemberProfile))
	mux.HandleFunc("/member/posts", a.requireRoles(data.RoleVisitor)(a.handleMemberPosts))
	mux.HandleFunc("/member/posts/new", a.requireRoles(data.RoleVisitor)(a.handleMemberPostNew))
	mux.HandleFunc("/member/posts/edit", a.requireRoles(data.RoleVisitor)(a.handleMemberPostEdit))
	mux.HandleFunc("/member/posts/delete", a.requireRoles(data.RoleVisitor)(a.handleMemberPostDelete))
	mux.HandleFunc("/member/pages", a.requireRoles(data.RoleVisitor)(a.handleMemberPages))
	mux.HandleFunc("/member/pages/reorder", a.requireRoles(data.RoleVisitor)(a.handleMemberPagesReorder))
	mux.HandleFunc("/member/pages/new", a.requireRoles(data.RoleVisitor)(a.handleMemberPageNew))
	mux.HandleFunc("/member/pages/edit", a.requireRoles(data.RoleVisitor)(a.handleMemberPageEdit))
	mux.HandleFunc("/member/pages/delete", a.requireRoles(data.RoleVisitor)(a.handleMemberPageDelete))
	mux.HandleFunc("/member/comments", a.requireRoles(data.RoleVisitor)(a.handleMemberComments))
	mux.HandleFunc("/member/comments/edit", a.requireRoles(data.RoleVisitor)(a.handleMemberCommentEdit))
	mux.HandleFunc("/member/comments/delete", a.requireRoles(data.RoleVisitor)(a.handleMemberCommentDelete))
	mux.HandleFunc("/member/appearance", a.requireRoles(data.RoleVisitor)(a.handleMemberAppearance))
	mux.HandleFunc("/member/notifications", a.requireRoles(data.RoleVisitor)(a.handleMemberNotifications))
	mux.HandleFunc("/member/notifications/read", a.requireRoles(data.RoleVisitor)(a.handleMemberNotificationsRead))
	mux.HandleFunc("/member/sessions", a.requireRoles(data.RoleVisitor)(a.handleMemberSessions))
	mux.HandleFunc("/member/sessions/revoke", a.requireRoles(data.RoleVisitor)(a.handleMemberSessionRevoke))
	mux.HandleFunc("/member/profile", a.requireRoles(data.RoleVisitor)(a.handleMemberProfile))
	mux.HandleFunc("/member/profile/avatar", a.requireRoles(data.RoleVisitor)(a.handleMemberAvatarUpload))

	// 统一加上安全中间件，避免每个 handler 重复写安全逻辑。
	return a.securityHeaders(a.csrfGuard(mux))
}
