package app

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"blog/internal/data"
)

// renderTemplate 使用共享视图数据渲染指定模板。
func (a *App) renderTemplate(w http.ResponseWriter, name string, data ViewData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := a.templates.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, fmt.Sprintf("template render failed: %v", err), http.StatusInternalServerError)
	}
}

// handleHome 渲染首页。
func (a *App) handleHome(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	viewData := a.baseData(r, "home")
	scope := normalizeHomeScope(r.URL.Query().Get("scope"))
	viewData.HomeScope = scope

	posts, err := a.store.ListPosts("post", 0)
	if err != nil {
		http.Error(w, "failed to load posts", http.StatusInternalServerError)
		return
	}

	users, _ := a.store.ListUsers()
	posts = filterHomePosts(posts, users, viewData.User, scope)
	viewData.PostAuthors = buildHomeAuthorNames(posts, users)

	pages, _ := a.store.ListPages()
	viewData.Posts = posts
	viewData.Pages = pages

	a.renderTemplate(w, "home", viewData)
}

func normalizeHomeScope(raw string) string {
	switch strings.TrimSpace(strings.ToLower(raw)) {
	case "official":
		return "official"
	case "personal":
		return "personal"
	default:
		return "all"
	}
}

func filterHomePosts(posts []data.Post, users []data.User, currentUser *data.User, scope string) []data.Post {
	if scope == "all" {
		return posts
	}

	roleByID := make(map[int64]string, len(users))
	for _, user := range users {
		roleByID[user.ID] = user.Role
	}

	filtered := make([]data.Post, 0, len(posts))
	for _, post := range posts {
		switch scope {
		case "official":
			role := roleByID[post.AuthorID]
			if role == data.RoleOwner || role == data.RoleMaintainer {
				filtered = append(filtered, post)
				continue
			}
			if currentUser != nil && post.AuthorID == currentUser.ID {
				filtered = append(filtered, post)
			}
		case "personal":
			if currentUser != nil && post.AuthorID == currentUser.ID {
				filtered = append(filtered, post)
			}
		default:
			filtered = append(filtered, post)
		}
	}

	return filtered
}

func buildHomeAuthorNames(posts []data.Post, users []data.User) map[int64]string {
	nameByID := make(map[int64]string, len(users))
	for _, user := range users {
		if strings.TrimSpace(user.DisplayName) != "" {
			nameByID[user.ID] = strings.TrimSpace(user.DisplayName)
		}
	}

	result := make(map[int64]string, len(posts))
	for _, post := range posts {
		name := nameByID[post.AuthorID]
		if name == "" {
			name = "Unknown author"
		}
		result[post.AuthorID] = name
	}
	return result
}

func (a *App) handleArchive(w http.ResponseWriter, r *http.Request) {
	posts, err := a.store.ListPosts("post", 0)
	if err != nil {
		http.Error(w, "failed to load archive", http.StatusInternalServerError)
		return
	}
	pages, _ := a.store.ListPages()

	// 按年份分组，用于时间轴展示。
	// ListPosts 默认按发布时间倒序，因此年份也呈倒序排列。
	var groups []ArchiveGroup
	var curYear string
	for _, p := range posts {
		y := p.PublishedAt.Format("2006")
		if y == "" || y == "0001" {
			y = "未归类"
		}
		if y != curYear {
			groups = append(groups, ArchiveGroup{Year: y})
			curYear = y
		}
		groups[len(groups)-1].Posts = append(groups[len(groups)-1].Posts, p)
	}

	data := a.baseData(r, "archive")
	data.Posts = posts
	data.Pages = pages
	data.ArchiveGroups = groups

	a.renderTemplate(w, "archive", data)
}

// handlePost 渲染公开文章页。
func (a *App) handlePost(w http.ResponseWriter, r *http.Request) {
	slug := strings.TrimPrefix(r.URL.Path, "/post/")
	slug = strings.TrimSpace(slug)
	if slug == "" {
		http.NotFound(w, r)
		return
	}

	post, err := a.store.GetPostBySlug(slug)
	if err != nil || post.Kind != "post" || !post.IsPublic {
		http.NotFound(w, r)
		return
	}

	// 浏览量去重：同一会话 24 小时内对同一文章只计 1 次浏览。
	// 通过 Cookie 标记已浏览的文章 ID，避免刷新涨量。
	if !hasViewedRecently(r, post.ID) {
		_ = a.store.IncrementViews(post.ID)
		markViewed(w, r, post.ID)
	}

	pages, _ := a.store.ListPages()

	viewData := a.baseData(r, "post")
	viewData.Post = post
	viewData.Pages = pages
	// SEO：文章页使用"文章标题 - 站名"格式，description 用摘要，canonical 指向规范 URL。
	viewData.SEOTitle = post.Title + " - " + a.site.Title
	if strings.TrimSpace(post.Summary) != "" {
		viewData.SEODescription = post.Summary
	} else {
		viewData.SEODescription = a.site.Subtitle
	}
	viewData.SEOCanonical = "/post/" + post.Slug
	viewData.SEOOGType = "article"
	if post.CoverURL != "" {
		viewData.SEOOGImage = post.CoverURL
	}
	if strings.TrimSpace(r.URL.Query().Get("ok")) == "report" {
		viewData.Flash = "Report submitted."
	}
	if strings.TrimSpace(r.URL.Query().Get("err")) == "report" {
		viewData.Flash = "Report failed. Please try again later."
	}

	a.renderTemplate(w, "post", viewData)
}

// handlePostLike 处理文章点赞切换（AJAX JSON 接口）。
func (a *App) handlePostLike(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	cu, cerr := a.currentUser(r)
	if cerr != nil || cu == nil {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":false,"error":"请先登录"}`))
		return
	}
	if err := r.ParseForm(); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":false,"error":"invalid form"}`))
		return
	}
	postID := strings.TrimSpace(r.FormValue("post_id"))
	id, parseErr := strconv.ParseInt(postID, 10, 64)
	if parseErr != nil {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":false,"error":"invalid post_id"}`))
		return
	}
	count, liked, err := a.store.ToggleLike(id, cu.ID)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":false,"error":"` + err.Error() + `"}`))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"ok":true,"count":%d,"liked":%v}`, count, liked)
}

// handlePage 根据 slug 渲染公开页面。
func (a *App) handlePage(w http.ResponseWriter, r *http.Request) {
	slug := strings.TrimPrefix(r.URL.Path, "/page/")
	slug = strings.TrimSpace(slug)
	if slug == "" {
		http.NotFound(w, r)
		return
	}
	if slug == "board" {
		http.Redirect(w, r, "/board", http.StatusFound)
		return
	}
	a.renderPageBySlug(w, r, slug)
}

// handlePageAlias 将固定 slug 绑定到通用页面渲染逻辑。
func (a *App) handlePageAlias(slug string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a.renderPageBySlug(w, r, slug)
	}
}

func (a *App) renderPageBySlug(w http.ResponseWriter, r *http.Request, slug string) {
	page, err := a.store.GetPostBySlug(slug)
	if err != nil || page.Kind != "page" || !page.IsPublic {
		http.NotFound(w, r)
		return
	}

	pages, _ := a.store.ListPages()

	data := a.baseData(r, "page")
	data.Post = page
	data.Pages = pages
	data.ActiveSlug = page.Slug

	a.renderTemplate(w, "page", data)
}

// handleBoard 渲染留言板页面并处理留言提交。
func (a *App) handleBoard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/board" {
		http.NotFound(w, r)
		return
	}

	page, err := a.store.GetPostBySlug("board")
	if err != nil || page.Kind != "page" || !page.IsPublic {
		http.NotFound(w, r)
		return
	}

	data := a.baseData(r, "page")
	data.Post = page
	data.ActiveSlug = page.Slug
	data.Pages, _ = a.store.ListPages()
	currentUser, _ := a.currentUser(r)
	if currentUser != nil {
		data.CommentFormName = currentUser.DisplayName
	} else {
		data.Flash = "Sign in to post, react, or report on the guestbook."
	}

	switch r.Method {
	case http.MethodGet:
		switch strings.TrimSpace(r.URL.Query().Get("ok")) {
		case "1":
			data.Flash = "Comment submitted."
		case "report":
			data.Flash = "Report submitted."
		}
		if strings.TrimSpace(r.URL.Query().Get("err")) == "report" {
			data.Flash = "Report failed. Please try again later."
		}
	case http.MethodPost:
		if currentUser == nil {
			http.Redirect(w, r, "/login?next=/board", http.StatusFound)
			return
		}
		if err := r.ParseForm(); err != nil {
			data.Flash = "Form parsing failed."
			break
		}

		name := strings.TrimSpace(r.FormValue("name"))
		content := strings.TrimSpace(r.FormValue("content"))
		anonymous := parseBool(r.FormValue("anonymous"))

		data.CommentFormName = name
		data.CommentFormContent = content
		data.CommentAnonymous = anonymous

		if content == "" {
			data.Flash = "Comment content cannot be empty."
			break
		}
		if !anonymous && name == "" && currentUser == nil {
			data.Flash = "Please enter a display name."
			break
		}

		var userID int64
		if currentUser != nil {
			userID = currentUser.ID
			if !anonymous && name == "" {
				name = currentUser.DisplayName
				data.CommentFormName = name
			}
		}

		if _, err := a.store.CreateComment(page.ID, userID, name, content, clientIP(r), anonymous); err != nil {
			data.Flash = "Comment submission failed."
			break
		}

		http.Redirect(w, r, "/board?ok=1#guestbook", http.StatusFound)
		return
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	comments, _ := a.store.ListComments(page.ID)
	data.Comments = comments

	a.renderTemplate(w, "page", data)
}

// handleBoardReact 处理留言板评论的点赞和点踩。
func (a *App) handleBoardReact(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if user, _ := a.currentUser(r); user == nil {
		http.Redirect(w, r, "/login?next=/board", http.StatusFound)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/board#guestbook", http.StatusFound)
		return
	}

	commentID := parseInt64(r.FormValue("comment_id"))
	action := strings.TrimSpace(r.FormValue("action"))
	if commentID == 0 || (action != "like" && action != "dislike") {
		http.Redirect(w, r, "/board#guestbook", http.StatusFound)
		return
	}

	board, err := a.store.GetPostBySlug("board")
	if err != nil || board.Kind != "page" {
		http.Redirect(w, r, "/board#guestbook", http.StatusFound)
		return
	}

	_ = a.store.ReactComment(board.ID, commentID, action)
	http.Redirect(w, r, "/board#guestbook", http.StatusFound)
}

// handleSearch 在当前范围内搜索文章或页面。
func (a *App) handleSearch(w http.ResponseWriter, r *http.Request) {
	keyword := strings.TrimSpace(r.URL.Query().Get("q"))
	from := normalizeSearchFrom(r.URL.Query().Get("from"))

	data := a.baseData(r, "search")
	data.Query = keyword
	data.SearchFrom = from
	data.SearchScope = "post"
	data.SearchScopeLabel = "Posts"
	pages, _ := a.store.ListPages()
	data.Pages = pages

	if isPagePath(from) {
		data.SearchScope = "page"
		data.SearchScopeLabel = "Pages"
	}

	if keyword != "" {
		if data.SearchScope == "page" {
			results, err := a.store.SearchPages(keyword)
			if err != nil {
				http.Error(w, "search failed", http.StatusInternalServerError)
				return
			}
			data.Posts = filterPagesByFrom(results, from)
		} else {
			results, err := a.store.SearchPosts(keyword)
			if err != nil {
				http.Error(w, "search failed", http.StatusInternalServerError)
				return
			}
			var homeSlugs map[string]struct{}
			if from == "/" {
				recentPosts, _ := a.store.ListPosts("post", 6)
				homeSlugs = make(map[string]struct{}, len(recentPosts))
				for _, post := range recentPosts {
					homeSlugs[post.Slug] = struct{}{}
				}
			}
			data.Posts = filterPostsByFrom(results, from, homeSlugs)
		}
	}

	a.renderTemplate(w, "search", data)
}

func normalizeSearchFrom(raw string) string {
	from := strings.TrimSpace(raw)
	if from == "" {
		return "/"
	}
	if !strings.HasPrefix(from, "/") {
		return "/"
	}
	switch {
	case from == "/", from == "/archive", from == "/search", from == "/about", from == "/say", from == "/board":
		return from
	case strings.HasPrefix(from, "/post/"), strings.HasPrefix(from, "/page/"):
		return from
	default:
		return "/"
	}
}

func filterPostsByFrom(posts []data.Post, from string, homeSlugs map[string]struct{}) []data.Post {
	switch {
	case strings.HasPrefix(from, "/post/"):
		slug := strings.TrimSpace(strings.TrimPrefix(from, "/post/"))
		if slug == "" {
			return nil
		}
		var filtered []data.Post
		for _, post := range posts {
			if post.Slug == slug {
				filtered = append(filtered, post)
				break
			}
		}
		return filtered
	case from == "/":
		if len(homeSlugs) == 0 {
			return nil
		}
		var filtered []data.Post
		for _, post := range posts {
			if _, ok := homeSlugs[post.Slug]; ok {
				filtered = append(filtered, post)
			}
		}
		return filtered
	case from == "/archive", from == "/search":
		return posts
	default:
		return nil
	}
}

func filterPagesByFrom(pages []data.Post, from string) []data.Post {
	target := ""
	switch {
	case strings.HasPrefix(from, "/page/"):
		target = strings.TrimSpace(strings.TrimPrefix(from, "/page/"))
	case from == "/about":
		target = "about"
	case from == "/say":
		target = "say"
	case from == "/board":
		target = "board"
	case from == "/search":
		return pages
	}
	if target == "" {
		return nil
	}

	var filtered []data.Post
	for _, page := range pages {
		if page.Slug == target {
			filtered = append(filtered, page)
			break
		}
	}
	return filtered
}

var usernamePattern = regexp.MustCompile(`^[a-zA-Z0-9_]{3,32}$`)

// handleUserLogin 处理用户登录。
func (a *App) handleUserLogin(w http.ResponseWriter, r *http.Request) {
	if user, _ := a.currentUser(r); user != nil {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}

	viewData := a.baseData(r, "login")
	viewData.Pages, _ = a.store.ListPages()
	viewData.AuthNext = sanitizeNextPath(r.URL.Query().Get("next"))

	switch r.Method {
	case http.MethodGet:
		switch strings.TrimSpace(r.URL.Query().Get("ok")) {
		case "reset":
			viewData.Flash = "Password reset complete. Sign in with the new password."
		case "verify":
			viewData.Flash = "Email verification completed."
		}
		if strings.TrimSpace(r.URL.Query().Get("err")) == "verify" {
			viewData.Flash = "Email verification failed. Please check the link."
		}
		a.renderTemplate(w, "login", viewData)
		return
	case http.MethodPost:
		ip := clientIP(r)
		if ok, wait := a.authLimiter.allow(ip, time.Now()); !ok {
			viewData.Flash = fmt.Sprintf("Too many login attempts. Try again in %.0f seconds.", wait.Seconds())
			a.renderTemplate(w, "login", viewData)
			return
		}

		if err := r.ParseForm(); err != nil {
			viewData.Flash = "Form parsing failed."
			a.renderTemplate(w, "login", viewData)
			return
		}

		username := strings.TrimSpace(r.FormValue("username"))
		password := strings.TrimSpace(r.FormValue("password"))
		nextPath := sanitizeNextPath(r.FormValue("next"))
		viewData.AuthFormUsername = username
		viewData.AuthNext = nextPath

		if username == "" || password == "" {
			a.authLimiter.onFailure(ip, time.Now())
			viewData.Flash = "Enter both username and password."
			a.renderTemplate(w, "login", viewData)
			return
		}

		user, err := a.store.Authenticate(username, password)
		if err != nil {
			a.authLimiter.onFailure(ip, time.Now())
			if strings.TrimSpace(err.Error()) != "" && err.Error() != "invalid username or password" {
				viewData.Flash = err.Error()
			} else {
				viewData.Flash = "Invalid username or password."
			}
			a.renderTemplate(w, "login", viewData)
			return
		}

		ua := strings.TrimSpace(r.UserAgent())
		sessionID, expiresAt, err := a.store.CreateSessionWithMeta(user.ID, ip, ua, a.sessionTTL)
		if err != nil {
			viewData.Flash = "Login succeeded, but session creation failed. Please try again."
			a.renderTemplate(w, "login", viewData)
			return
		}

		a.authLimiter.onSuccess(ip)
		_ = a.store.UpdateLastLogin(user.ID, ip)
		setSessionCookie(w, r, sessionID, expiresAt)
		http.Redirect(w, r, nextPath, http.StatusFound)
		return
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
}

// handleUserRegister 处理用户注册。
func (a *App) handleUserRegister(w http.ResponseWriter, r *http.Request) {
	if user, _ := a.currentUser(r); user != nil {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}

	viewData := a.baseData(r, "register")
	viewData.Pages, _ = a.store.ListPages()

	switch r.Method {
	case http.MethodGet:
		a.renderTemplate(w, "register", viewData)
		return
	case http.MethodPost:
		if err := r.ParseForm(); err != nil {
			viewData.Flash = "Form parsing failed."
			a.renderTemplate(w, "register", viewData)
			return
		}

		username := strings.TrimSpace(r.FormValue("username"))
		displayName := strings.TrimSpace(r.FormValue("display_name"))
		email := strings.TrimSpace(strings.ToLower(r.FormValue("email")))
		password := strings.TrimSpace(r.FormValue("password"))
		confirmPassword := strings.TrimSpace(r.FormValue("confirm_password"))

		viewData.AuthFormUsername = username
		viewData.AuthFormDisplayName = displayName

		if !usernamePattern.MatchString(username) {
			viewData.Flash = "Username must be 3-32 characters and use letters, numbers, or underscores only."
			a.renderTemplate(w, "register", viewData)
			return
		}
		if email != "" && !strings.Contains(email, "@") {
			viewData.Flash = "Invalid email address."
			a.renderTemplate(w, "register", viewData)
			return
		}
		if len([]rune(password)) < 6 {
			viewData.Flash = "Password must be at least 6 characters long."
			a.renderTemplate(w, "register", viewData)
			return
		}
		if password != confirmPassword {
			viewData.Flash = "Passwords do not match."
			a.renderTemplate(w, "register", viewData)
			return
		}
		if displayName == "" {
			displayName = username
			viewData.AuthFormDisplayName = displayName
		}

		userID, err := a.store.CreateUserWithRole(username, password, displayName, data.RoleVisitor)
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE constraint failed: users.username") {
				viewData.Flash = "Username already exists."
			} else {
				viewData.Flash = "Registration failed. Please try again later."
			}
			a.renderTemplate(w, "register", viewData)
			return
		}

		if email != "" {
			_ = a.store.SetUserEmail(userID, email, false)
			if token, _, tokenErr := a.store.CreateEmailVerifyToken(userID, email, 24*time.Hour); tokenErr == nil {
				verifyURL := "/verify-email?token=" + url.QueryEscape(token)
				_ = a.store.CreateNotification(userID, "Verify your email", "Open this link to verify your email: "+verifyURL, "email")
			}
		}

		ip := clientIP(r)
		_ = a.store.UpdateLastLogin(userID, ip)
		ua := strings.TrimSpace(r.UserAgent())
		sessionID, expiresAt, err := a.store.CreateSessionWithMeta(userID, ip, ua, a.sessionTTL)
		if err != nil {
			viewData.Flash = "Registration succeeded, but session creation failed. Please sign in again."
			a.renderTemplate(w, "register", viewData)
			return
		}
		setSessionCookie(w, r, sessionID, expiresAt)
		http.Redirect(w, r, "/", http.StatusFound)
		return
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
}

// handleUserLogout 处理用户登出。
func (a *App) handleUserLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		_ = a.store.DeleteSession(cookie.Value)
	}
	clearSessionCookie(w, r)
	http.Redirect(w, r, "/", http.StatusFound)
}

// sanitizeNextPath 只允许安全的相对路径跳转目标。
func sanitizeNextPath(raw string) string {
	next := strings.TrimSpace(raw)
	if next == "" {
		return "/"
	}
	if strings.HasPrefix(next, "//") {
		return "/"
	}
	parsed, err := url.Parse(next)
	if err != nil {
		return "/"
	}
	if parsed.IsAbs() || parsed.Host != "" {
		return "/"
	}
	if !strings.HasPrefix(next, "/") {
		return "/"
	}
	return next
}

func (a *App) handleBoardReport(w http.ResponseWriter, r *http.Request) {
	a.handleContentReport(w, r)
}

// handleContentReport 处理文章和评论举报。
func (a *App) handleContentReport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	user, _ := a.currentUser(r)
	if user == nil {
		nextPath := pickReportNextPath(r, "", "/")
		http.Redirect(w, r, "/login?next="+url.QueryEscape(nextPath), http.StatusFound)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, appendReportStatus(pickReportNextPath(r, "", "/"), false), http.StatusFound)
		return
	}

	reportType := strings.TrimSpace(strings.ToLower(r.FormValue("type")))
	commentID := parseInt64(r.FormValue("comment_id"))
	postID := parseInt64(r.FormValue("post_id"))
	reason := strings.TrimSpace(r.FormValue("reason"))

	if reportType == "" {
		switch {
		case commentID > 0:
			reportType = "comment"
		case postID > 0:
			reportType = "post"
		}
	}

	fallbackNext := "/"
	if reportType == "comment" {
		fallbackNext = "/board#guestbook"
	}
	nextPath := pickReportNextPath(r, r.FormValue("next"), fallbackNext)

	switch reportType {
	case "comment":
		if commentID == 0 {
			http.Redirect(w, r, appendReportStatus(nextPath, false), http.StatusFound)
			return
		}
		if _, err := a.store.CreateCommentReport(commentID, user.ID, reason); err != nil {
			http.Redirect(w, r, appendReportStatus(nextPath, false), http.StatusFound)
			return
		}
		a.notifyReportModerators(
			user.ID,
			"New comment report",
			"The guestbook has a new report waiting in the admin center.",
		)
		http.Redirect(w, r, appendReportStatus(nextPath, true), http.StatusFound)
		return
	case "post":
		if postID == 0 {
			http.Redirect(w, r, appendReportStatus(nextPath, false), http.StatusFound)
			return
		}

		post, postErr := a.store.GetPostByID(postID)
		if postErr == nil && strings.TrimSpace(r.FormValue("next")) == "" {
			nextPath = pickReportNextPath(r, "/post/"+post.Slug, fallbackNext)
		}
		if _, err := a.store.CreatePostReport(postID, user.ID, reason); err != nil {
			http.Redirect(w, r, appendReportStatus(nextPath, false), http.StatusFound)
			return
		}

		content := "A post has been reported. Please review it in the admin center."
		if postErr == nil && strings.TrimSpace(post.Title) != "" {
			content = fmt.Sprintf("Post %q has been reported. Please review it in the admin center.", post.Title)
		}
		a.notifyReportModerators(user.ID, "New post report", content)
		http.Redirect(w, r, appendReportStatus(nextPath, true), http.StatusFound)
		return
	default:
		http.Redirect(w, r, appendReportStatus(nextPath, false), http.StatusFound)
		return
	}
}

// notifyReportModerators 将新的举报通知给站长和维护者。
func (a *App) notifyReportModerators(excludeUserID int64, title, content string) {
	users, err := a.store.ListUsers()
	if err != nil {
		return
	}
	for _, u := range users {
		if u.ID == excludeUserID {
			continue
		}
		if u.Role != data.RoleOwner && u.Role != data.RoleMaintainer {
			continue
		}
		_ = a.store.CreateNotification(u.ID, title, content, "report")
	}
}

// pickReportNextPath 选择举报提交后的跳转目标。
func pickReportNextPath(r *http.Request, rawNext, fallback string) string {
	if n := strings.TrimSpace(rawNext); n != "" {
		return sanitizeNextPath(n)
	}
	if r != nil {
		if referer := strings.TrimSpace(r.Referer()); referer != "" {
			if u, err := url.Parse(referer); err == nil {
				path := strings.TrimSpace(u.Path)
				if u.RawQuery != "" {
					path += "?" + u.RawQuery
				}
				if path != "" {
					return sanitizeNextPath(path)
				}
			}
		}
	}
	return sanitizeNextPath(fallback)
}

// appendReportStatus 为跳转地址附加 ok 或 err 标记。
func appendReportStatus(nextPath string, success bool) string {
	safePath := sanitizeNextPath(nextPath)
	u, err := url.Parse(safePath)
	if err != nil {
		if success {
			return "/?ok=report"
		}
		return "/?err=report"
	}

	q := u.Query()
	if success {
		q.Set("ok", "report")
		q.Del("err")
	} else {
		q.Set("err", "report")
		q.Del("ok")
	}
	u.RawQuery = q.Encode()

	result := u.Path
	if u.RawQuery != "" {
		result += "?" + u.RawQuery
	}
	if u.Fragment != "" {
		result += "#" + u.Fragment
	}
	return result
}

func (a *App) handleForgotPassword(w http.ResponseWriter, r *http.Request) {
	viewData := a.baseData(r, "login")
	viewData.Pages, _ = a.store.ListPages()

	switch r.Method {
	case http.MethodGet:
		a.renderTemplate(w, "forgot_password", viewData)
		return
	case http.MethodPost:
		if err := r.ParseForm(); err != nil {
			viewData.Flash = "Form parsing failed."
			a.renderTemplate(w, "forgot_password", viewData)
			return
		}
		username := strings.TrimSpace(r.FormValue("username"))
		if username == "" {
			viewData.Flash = "Enter a username."
			a.renderTemplate(w, "forgot_password", viewData)
			return
		}
		token, userID, _, err := a.store.CreatePasswordResetToken(username, 30*time.Minute)
		if err != nil {
			viewData.Flash = "Unable to create a password reset notice right now."
			a.renderTemplate(w, "forgot_password", viewData)
			return
		}
		resetURL := "/reset-password?token=" + url.QueryEscape(token)
		_ = a.store.CreateNotification(userID, "Password reset", "Use this link within 30 minutes to reset your password: "+resetURL, "security")
		viewData.Flash = "A password reset notice has been generated."
		a.renderTemplate(w, "forgot_password", viewData)
		return
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
}

func (a *App) handleResetPassword(w http.ResponseWriter, r *http.Request) {
	viewData := a.baseData(r, "login")
	viewData.Pages, _ = a.store.ListPages()
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	if r.Method == http.MethodPost {
		token = strings.TrimSpace(r.FormValue("token"))
	}
	viewData.AuthNext = token

	switch r.Method {
	case http.MethodGet:
		a.renderTemplate(w, "reset_password", viewData)
		return
	case http.MethodPost:
		if err := r.ParseForm(); err != nil {
			viewData.Flash = "Form parsing failed."
			a.renderTemplate(w, "reset_password", viewData)
			return
		}
		newPassword := strings.TrimSpace(r.FormValue("new_password"))
		confirmPassword := strings.TrimSpace(r.FormValue("confirm_password"))
		if len([]rune(newPassword)) < 6 {
			viewData.Flash = "Password must be at least 6 characters long."
			a.renderTemplate(w, "reset_password", viewData)
			return
		}
		if newPassword != confirmPassword {
			viewData.Flash = "Passwords do not match."
			a.renderTemplate(w, "reset_password", viewData)
			return
		}
		if _, err := a.store.ResetPasswordByToken(token, newPassword); err != nil {
			viewData.Flash = err.Error()
			a.renderTemplate(w, "reset_password", viewData)
			return
		}
		http.Redirect(w, r, "/login?ok=reset", http.StatusFound)
		return
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
}

func (a *App) handleVerifyEmail(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	if token == "" {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	userID, email, err := a.store.VerifyEmailByToken(token)
	if err != nil {
		http.Redirect(w, r, "/login?err=verify", http.StatusFound)
		return
	}
	_ = a.store.CreateNotification(userID, "Email verified", "Email "+email+" has been verified.", "email")
	http.Redirect(w, r, "/login?ok=verify", http.StatusFound)
}
