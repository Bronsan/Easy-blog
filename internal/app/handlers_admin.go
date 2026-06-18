package app

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"blog/internal/data"
)

const (
	// defaultSiteBackground 是前台默认背景的 CSS 值。
	defaultSiteBackground = "url('/static/img/paper-bg.svg') center top / cover no-repeat fixed"
	// defaultAdminBackground 是后台默认背景的 CSS 值。
	defaultAdminBackground = "linear-gradient(180deg, #f6f6f2, #eceff3)"
	// maxBackgroundUploadBytes 限制背景图上传大小。
	maxBackgroundUploadBytes = 10 << 20 // 10 MB
)

// allowedSiteBackgrounds 和 allowedAdminBackgrounds 定义允许使用的背景值。
var (
	allowedSiteBackgrounds = map[string]struct{}{
		"#f6f0e6": {},
		"#eaf4f1": {},
		"#f3eef8": {},
		"linear-gradient(135deg, #fbe8cc, #e4d0b0)":                          {},
		"linear-gradient(135deg, #dff3ee, #c3e1d8)":                          {},
		"linear-gradient(135deg, #e7ecff, #d4daf5)":                          {},
		"url('/static/img/paper-bg.svg') center top / cover no-repeat fixed": {},
		"url('/static/img/hero-bg.svg') center / cover no-repeat fixed":      {},
		"url('/static/img/cover-1.svg') center / cover no-repeat fixed":      {},
	}
	allowedAdminBackgrounds = map[string]struct{}{
		"#f5f7f6": {},
		"#f3f4f8": {},
		"#f7f3ec": {},
		"linear-gradient(180deg, #f6f6f2, #eceff3)":                          {},
		"linear-gradient(180deg, #eef7f3, #e3efe8)":                          {},
		"linear-gradient(180deg, #f7f2e9, #efe8da)":                          {},
		"url('/static/img/paper-bg.svg') center top / cover no-repeat fixed": {},
		"url('/static/img/hero-bg.svg') center / cover no-repeat fixed":      {},
	}
	allowedBackgroundUploadExts = map[string]struct{}{
		".jpg":  {},
		".jpeg": {},
		".png":  {},
		".gif":  {},
		".webp": {},
		".svg":  {},
	}
)

// handleAdminLogin 处理后台登录。
func (a *App) handleAdminLogin(w http.ResponseWriter, r *http.Request) {
	viewData := a.baseData(r, "admin-login")

	switch r.Method {
	case http.MethodGet:
		a.renderTemplate(w, "admin_login", viewData)
		return
	case http.MethodPost:
		ip := clientIP(r)
		if ok, wait := a.authLimiter.allow(ip, time.Now()); !ok {
			viewData.Flash = fmt.Sprintf("Too many login attempts. Try again in %.0f seconds.", wait.Seconds())
			a.renderTemplate(w, "admin_login", viewData)
			return
		}

		if err := r.ParseForm(); err != nil {
			viewData.Flash = "Form parsing failed."
			a.renderTemplate(w, "admin_login", viewData)
			return
		}

		username := strings.TrimSpace(r.FormValue("username"))
		password := strings.TrimSpace(r.FormValue("password"))
		if username == "" || password == "" {
			a.authLimiter.onFailure(ip, time.Now())
			viewData.Flash = "Enter both username and password."
			a.renderTemplate(w, "admin_login", viewData)
			return
		}

		user, err := a.store.Authenticate(username, password)
		if err != nil {
			a.authLimiter.onFailure(ip, time.Now())
			viewData.Flash = "Invalid username or password."
			a.renderTemplate(w, "admin_login", viewData)
			return
		}

		ua := strings.TrimSpace(r.UserAgent())
		sessionID, expiresAt, err := a.store.CreateSessionWithMeta(user.ID, ip, ua, a.sessionTTL)
		if err != nil {
			viewData.Flash = "Login succeeded, but session creation failed."
			a.renderTemplate(w, "admin_login", viewData)
			return
		}

		a.authLimiter.onSuccess(ip)
		_ = a.store.UpdateLastLogin(user.ID, ip)
		setSessionCookie(w, sessionID, expiresAt)
		if user.Role == data.RoleVisitor {
			http.Redirect(w, r, "/member", http.StatusFound)
			return
		}
		http.Redirect(w, r, "/admin", http.StatusFound)
		return
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
}

// handleAdminLogout 处理后台登出。
func (a *App) handleAdminLogout(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(sessionCookieName)
	if err == nil {
		_ = a.store.DeleteSession(cookie.Value)
	}
	clearSessionCookie(w)
	http.Redirect(w, r, "/admin/login", http.StatusFound)
}

// handleAdminEntry 将已登录的后台用户重定向到合适的入口页。
func (a *App) handleAdminEntry(w http.ResponseWriter, r *http.Request) {
	user, _ := a.currentUser(r)
	if user == nil {
		http.Redirect(w, r, "/admin/login", http.StatusFound)
		return
	}
	if user.Role == data.RoleOwner {
		http.Redirect(w, r, "/admin/users", http.StatusFound)
		return
	}
	http.Redirect(w, r, "/admin/pages", http.StatusFound)
}

func (a *App) handleAdminDashboard(w http.ResponseWriter, r *http.Request) {
	posts, views, _ := a.store.GetStats()
	data := a.baseData(r, "admin")
	data.Stats = Stats{Posts: posts, Views: views}
	data.Pages, _ = a.store.ListPages()
	a.renderTemplate(w, "admin_dashboard", data)
}

// handleAdminPosts 渲染后台文章列表。
func (a *App) handleAdminPosts(w http.ResponseWriter, r *http.Request) {
	posts, err := a.store.ListPostsAdmin("post")
	if err != nil {
		http.Error(w, "failed to load posts", http.StatusInternalServerError)
		return
	}
	data := a.baseData(r, "admin-posts")
	data.Posts = posts
	a.renderTemplate(w, "admin_posts", data)
}

// handleAdminPostNew 新建文章。
func (a *App) handleAdminPostNew(w http.ResponseWriter, r *http.Request) {
	data := a.baseData(r, "admin-post-new")

	switch r.Method {
	case http.MethodGet:
		a.renderTemplate(w, "admin_post_form", data)
		return
	case http.MethodPost:
		if err := r.ParseForm(); err != nil {
			data.Flash = "Form parsing failed."
			a.renderTemplate(w, "admin_post_form", data)
			return
		}

		post := buildPostFromForm(r, "post")
		user, _ := a.currentUser(r)
		if user != nil {
			post.AuthorID = user.ID
		}

		if _, err := a.store.CreatePost(post); err != nil {
			data.Flash = "Failed to create post."
			data.Post = post
			a.renderTemplate(w, "admin_post_form", data)
			return
		}

		http.Redirect(w, r, "/admin/posts", http.StatusFound)
		return
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
}

// handleAdminPostEdit 编辑已有文章。
func (a *App) handleAdminPostEdit(w http.ResponseWriter, r *http.Request) {
	id := parseInt64(r.URL.Query().Get("id"))
	if id == 0 {
		http.NotFound(w, r)
		return
	}

	post, err := a.store.GetPostByID(id)
	if err != nil || post.Kind != "post" {
		http.NotFound(w, r)
		return
	}

	data := a.baseData(r, "admin-post-edit")

	switch r.Method {
	case http.MethodGet:
		data.Post = post
		a.renderTemplate(w, "admin_post_form", data)
		return
	case http.MethodPost:
		if err := r.ParseForm(); err != nil {
			data.Flash = "Form parsing failed."
			data.Post = post
			a.renderTemplate(w, "admin_post_form", data)
			return
		}

		updated := buildPostFromForm(r, "post")
		updated.ID = post.ID
		updated.Kind = "post"
		updated.Views = post.Views
		if updated.PublishedAt.IsZero() {
			updated.PublishedAt = post.PublishedAt
		}

		if err := a.store.UpdatePost(updated); err != nil {
			data.Flash = "Failed to update post."
			data.Post = updated
			a.renderTemplate(w, "admin_post_form", data)
			return
		}

		http.Redirect(w, r, "/admin/posts", http.StatusFound)
		return
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
}

// handleAdminPostDelete 删除文章。
func (a *App) handleAdminPostDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	id := parseInt64(r.FormValue("id"))
	if id == 0 {
		http.NotFound(w, r)
		return
	}
	_ = a.store.DeletePost(id)
	http.Redirect(w, r, "/admin/posts", http.StatusFound)
}

// handleAdminPages 渲染后台页面列表。
func (a *App) handleAdminPages(w http.ResponseWriter, r *http.Request) {
	pages, err := a.store.ListPostsAdmin("page")
	if err != nil {
		http.Error(w, "failed to load pages", http.StatusInternalServerError)
		return
	}
	data := a.baseData(r, "admin-pages")
	data.Posts = pages
	if r.URL.Query().Get("ok") == "1" {
		data.Flash = "Page order updated."
	}
	a.renderTemplate(w, "admin_pages", data)
}

// handleAdminPagesReorder 更新页面排序。
func (a *App) handleAdminPagesReorder(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ids := parseIDList(r.FormValue("ids"))
	if len(ids) == 0 {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := a.store.ReorderPages(ids); err != nil {
		http.Error(w, "failed to reorder pages", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin/pages?ok=1", http.StatusFound)
}

// handleAdminPageNew 新建页面。
func (a *App) handleAdminPageNew(w http.ResponseWriter, r *http.Request) {
	viewData := a.baseData(r, "admin-pages")

	switch r.Method {
	case http.MethodGet:
		a.renderTemplate(w, "admin_page_form", viewData)
		return
	case http.MethodPost:
		if err := r.ParseForm(); err != nil {
			viewData.Flash = "Form parsing failed."
			a.renderTemplate(w, "admin_page_form", viewData)
			return
		}

		page := buildPostFromForm(r, "page")
		user, _ := a.currentUser(r)
		if user != nil {
			page.AuthorID = user.ID
		}
		if _, err := a.store.CreatePost(page); err != nil {
			viewData.Flash = "Failed to create page."
			viewData.Post = page
			a.renderTemplate(w, "admin_page_form", viewData)
			return
		}
		http.Redirect(w, r, "/admin/pages?ok=1", http.StatusFound)
		return
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
}

// handleAdminPageEdit 编辑已有页面。
func (a *App) handleAdminPageEdit(w http.ResponseWriter, r *http.Request) {
	id := parseInt64(r.URL.Query().Get("id"))
	if id == 0 {
		http.NotFound(w, r)
		return
	}

	page, err := a.store.GetPostByID(id)
	if err != nil || page.Kind != "page" {
		http.NotFound(w, r)
		return
	}

	data := a.baseData(r, "admin-page-edit")

	switch r.Method {
	case http.MethodGet:
		data.Post = page
		a.renderTemplate(w, "admin_page_form", data)
		return
	case http.MethodPost:
		if err := r.ParseForm(); err != nil {
			data.Flash = "Form parsing failed."
			data.Post = page
			a.renderTemplate(w, "admin_page_form", data)
			return
		}

		updated := buildPostFromForm(r, "page")
		updated.ID = page.ID
		updated.Kind = "page"
		updated.Views = page.Views
		if updated.PublishedAt.IsZero() {
			updated.PublishedAt = page.PublishedAt
		}

		if err := a.store.UpdatePost(updated); err != nil {
			data.Flash = "Failed to update page."
			data.Post = updated
			a.renderTemplate(w, "admin_page_form", data)
			return
		}

		http.Redirect(w, r, "/admin/pages", http.StatusFound)
		return
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
}

// handleAdminPageDelete 删除页面。
func (a *App) handleAdminPageDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	id := parseInt64(r.FormValue("id"))
	if id == 0 {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	_ = a.store.DeletePost(id)
	http.Redirect(w, r, "/admin/pages?ok=1", http.StatusFound)
}

// handleAdminUsers 管理用户、角色、密码与封禁状态。
func (a *App) handleAdminUsers(w http.ResponseWriter, r *http.Request) {
	viewData := a.baseData(r, "admin-users")
	actor, _ := a.currentUser(r)
	selectedID := parseInt64(r.URL.Query().Get("uid"))

	if actor == nil || actor.Role != data.RoleOwner {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	switch r.Method {
	case http.MethodGet:
		a.populateAdminUsersData(&viewData, selectedID)
		a.renderTemplate(w, "admin_users", viewData)
		return
	case http.MethodPost:
		if err := r.ParseForm(); err != nil {
			viewData.Flash = "Form parsing failed."
			a.populateAdminUsersData(&viewData, selectedID)
			a.renderTemplate(w, "admin_users", viewData)
			return
		}

		formID := parseInt64(r.FormValue("id"))
		if formID > 0 {
			selectedID = formID
		}

		ownerPassword := strings.TrimSpace(r.FormValue("owner_password"))
		if a.store.VerifyUserPassword(actor.ID, ownerPassword) != nil {
			viewData.Flash = "Owner password verification failed."
			a.populateAdminUsersData(&viewData, selectedID)
			a.renderTemplate(w, "admin_users", viewData)
			return
		}

		action := strings.TrimSpace(r.FormValue("action"))
		var (
			targetID    int64
			auditAction string
			auditDetail string
			success     bool
		)

		switch action {
		case "create":
			username := strings.TrimSpace(r.FormValue("username"))
			displayName := strings.TrimSpace(r.FormValue("display_name"))
			password := strings.TrimSpace(r.FormValue("password"))
			role := strings.TrimSpace(r.FormValue("role"))

			if username == "" || displayName == "" || password == "" {
				viewData.Flash = "Username, display name, and password are required."
			} else if id, err := a.store.CreateUserWithRole(username, password, displayName, role); err != nil {
				viewData.Flash = "Failed to create user."
			} else {
				viewData.Flash = "User created."
				selectedID = id
				targetID = id
				auditAction = "user_create"
				auditDetail = "created user username=" + username + " role=" + role
				success = true
			}
		case "update_profile":
			id := parseInt64(r.FormValue("id"))
			username := strings.TrimSpace(r.FormValue("username"))
			displayName := strings.TrimSpace(r.FormValue("display_name"))

			if id == 0 || username == "" || displayName == "" {
				viewData.Flash = "Invalid request."
			} else if id == actor.ID {
				viewData.Flash = "You cannot edit your own profile from this screen."
			} else if err := a.store.UpdateUserProfile(id, username, displayName); err != nil {
				viewData.Flash = "Failed to update user profile."
			} else {
				viewData.Flash = "User profile updated."
				selectedID = id
				targetID = id
				auditAction = "user_update_profile"
				auditDetail = "updated user profile username=" + username + " display_name=" + displayName
				success = true
			}
		case "update_role":
			id := parseInt64(r.FormValue("id"))
			role := strings.TrimSpace(r.FormValue("role"))

			if id == 0 {
				viewData.Flash = "Invalid request."
			} else if id == actor.ID {
				viewData.Flash = "You cannot change your own role."
			} else if err := a.store.UpdateUserRole(id, role); err != nil {
				viewData.Flash = "Failed to update user role."
			} else {
				viewData.Flash = "User role updated."
				selectedID = id
				targetID = id
				auditAction = "user_update_role"
				auditDetail = "updated user role role=" + role
				success = true
			}
		case "reset_password":
			id := parseInt64(r.FormValue("id"))
			newPassword := strings.TrimSpace(r.FormValue("new_password"))

			if id == 0 || newPassword == "" {
				viewData.Flash = "Invalid request."
			} else if len([]rune(newPassword)) < 6 {
				viewData.Flash = "Password must be at least 6 characters long."
			} else if id == actor.ID {
				viewData.Flash = "You cannot reset your own password from this screen."
			} else if err := a.store.UpdateUserPassword(id, newPassword); err != nil {
				viewData.Flash = "Failed to reset user password."
			} else {
				viewData.Flash = "User password reset."
				selectedID = id
				targetID = id
				auditAction = "user_reset_password"
				auditDetail = "reset user password"
				success = true
			}
		case "set_ban":
			id := parseInt64(r.FormValue("id"))
			ban := parseBool(r.FormValue("ban"))
			reason := strings.TrimSpace(r.FormValue("ban_reason"))
			if id == 0 {
				viewData.Flash = "Invalid request."
			} else if id == actor.ID {
				viewData.Flash = "You cannot ban your own account."
			} else if err := a.store.UpdateUserBan(id, ban, reason); err != nil {
				viewData.Flash = "Failed to update ban status."
			} else {
				selectedID = id
				targetID = id
				auditAction = "user_update_ban"
				if ban {
					viewData.Flash = "User banned."
					auditDetail = "banned user reason=" + reason
					_ = a.store.CreateNotification(id, "Account banned", "Your account has been banned. Reason: "+reason, "security")
				} else {
					viewData.Flash = "User unbanned."
					auditDetail = "unbanned user"
					_ = a.store.CreateNotification(id, "Account restored", "Your account can now sign in again.", "security")
				}
				success = true
			}
		case "delete_user":
			id := parseInt64(r.FormValue("id"))
			if id == 0 {
				viewData.Flash = "Invalid request."
				break
			}
			if id == actor.ID {
				viewData.Flash = "You cannot delete your own account."
				break
			}
			targetUser, err := a.store.GetUserByID(id)
			if err != nil {
				viewData.Flash = "Failed to load user."
				break
			}
			if err := a.store.DeleteUser(id); err != nil {
				viewData.Flash = "Failed to delete user."
				selectedID = id
			} else {
				viewData.Flash = "User deleted."
				selectedID = 0
				targetID = actor.ID
				auditAction = "user_delete"
				auditDetail = "deleted user id=" + fmt.Sprintf("%d", id) + " username=" + targetUser.Username
				success = true
			}
		default:
			viewData.Flash = "Unknown action."
		}

		if success && targetID > 0 {
			_ = a.store.CreateAuditLog(actor.ID, targetID, auditAction, auditDetail, clientIP(r))
		}

		a.populateAdminUsersData(&viewData, selectedID)
		a.renderTemplate(w, "admin_users", viewData)
		return
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
}

// populateAdminUsersData 加载用户列表、当前选中用户和最近的审计日志。
func (a *App) populateAdminUsersData(viewData *ViewData, selectedID int64) {
	if viewData == nil {
		return
	}

	users, err := a.store.ListUsers()
	if err != nil {
		if viewData.Flash == "" {
			viewData.Flash = "Failed to load users."
		}
	} else {
		viewData.Users = users
	}

	if selectedID > 0 {
		selectedUser, err := a.store.GetUserByID(selectedID)
		if err != nil {
			if viewData.Flash == "" {
				viewData.Flash = "Failed to load the selected user."
			}
		} else {
			viewData.SelectedUser = selectedUser
		}
	}

	viewData.AuditLogs, _ = a.store.ListAuditLogs(50)
}

// handleAdminBoardComments 管理后台留言板评论。
func (a *App) handleAdminBoardComments(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	board, err := a.store.GetPostBySlug("board")
	if err != nil || board.Kind != "page" {
		http.Error(w, "guestbook page not found", http.StatusInternalServerError)
		return
	}

	comments, err := a.store.ListCommentsAdmin(board.ID)
	if err != nil {
		http.Error(w, "failed to load guestbook comments", http.StatusInternalServerError)
		return
	}

	data := a.baseData(r, "admin-pages")
	data.Post = board
	data.Comments = comments
	if r.URL.Query().Get("ok") == "1" {
		data.Flash = "Comment moderation updated."
	}

	a.renderTemplate(w, "admin_board_comments", data)
}

// redirectAdminComments 将旧评论入口重定向到留言板评论页。
func (a *App) redirectAdminComments(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/admin/pages/board-comments", http.StatusFound)
}

// handleAdminCommentToggle 隐藏或显示留言板评论。
func (a *App) handleAdminCommentToggle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	id := parseInt64(r.FormValue("id"))
	action := strings.TrimSpace(r.FormValue("action"))
	if id == 0 || (action != "hide" && action != "show") {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	if err := a.store.SetCommentHidden(id, action == "hide"); err != nil {
		http.Error(w, "failed to update comment visibility", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/admin/pages/board-comments?ok=1", http.StatusFound)
}

// handleAdminCommentDelete 删除留言板评论。
func (a *App) handleAdminCommentDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	id := parseInt64(r.FormValue("id"))
	if id == 0 {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	if err := a.store.DeleteComment(id); err != nil {
		http.Error(w, "failed to delete comment", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/admin/pages/board-comments?ok=1", http.StatusFound)
}

// handleAdminAppearance 保存前台和后台背景设置。
func (a *App) handleAdminAppearance(w http.ResponseWriter, r *http.Request) {
	viewData := a.baseData(r, "admin-appearance")

	switch r.Method {
	case http.MethodGet:
		if r.URL.Query().Get("ok") == "1" {
			viewData.Flash = "Appearance settings saved."
		}
		a.renderTemplate(w, "admin_appearance", viewData)
		return
	case http.MethodPost:
		if err := r.ParseForm(); err != nil {
			viewData.Flash = "Form parsing failed."
			a.renderTemplate(w, "admin_appearance", viewData)
			return
		}

		siteBackground := sanitizeBackgroundChoice(
			r.FormValue("site_background"),
			allowedSiteBackgrounds,
			defaultSiteBackground,
		)
		adminBackground := sanitizeBackgroundChoice(
			r.FormValue("admin_background"),
			allowedAdminBackgrounds,
			defaultAdminBackground,
		)

		if err := a.store.SetSetting("site_background", siteBackground); err != nil {
			viewData.Flash = "Failed to save site background."
			a.renderTemplate(w, "admin_appearance", viewData)
			return
		}
		if err := a.store.SetSetting("admin_background", adminBackground); err != nil {
			viewData.Flash = "Failed to save admin background."
			a.renderTemplate(w, "admin_appearance", viewData)
			return
		}

		a.invalidateCommonCache()
		http.Redirect(w, r, "/admin/appearance?ok=1", http.StatusFound)
		return
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
}

// handleAdminAppearanceUpload 保存背景图片并应用到指定区域。
func (a *App) handleAdminAppearanceUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if err := r.ParseMultipartForm(maxBackgroundUploadBytes); err != nil {
		a.renderAdminAppearanceError(w, r, "Upload failed: invalid multipart form.")
		return
	}

	target := strings.TrimSpace(r.FormValue("target"))
	if target != "site" && target != "admin" {
		a.renderAdminAppearanceError(w, r, "Upload failed: invalid target.")
		return
	}

	file, header, err := r.FormFile("background_image")
	if err != nil {
		a.renderAdminAppearanceError(w, r, "Upload failed: no file selected.")
		return
	}
	defer file.Close()

	ext := strings.ToLower(filepath.Ext(header.Filename))
	if _, ok := allowedBackgroundUploadExts[ext]; !ok {
		a.renderAdminAppearanceError(w, r, "Upload failed: allowed types are jpg, jpeg, png, gif, webp, and svg.")
		return
	}

	uploadDir := filepath.Join(a.staticDir, "uploads", "backgrounds")
	if err := os.MkdirAll(uploadDir, 0o755); err != nil {
		a.renderAdminAppearanceError(w, r, "Upload failed: could not create upload directory.")
		return
	}

	fileName := fmt.Sprintf("%s-%d%s", target, time.Now().UnixNano(), ext)
	savePath := filepath.Join(uploadDir, fileName)
	out, err := os.Create(savePath)
	if err != nil {
		a.renderAdminAppearanceError(w, r, "Upload failed: could not create output file.")
		return
	}

	limited := io.LimitedReader{R: file, N: maxBackgroundUploadBytes + 1}
	written, copyErr := io.Copy(out, &limited)
	closeErr := out.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.Remove(savePath)
		a.renderAdminAppearanceError(w, r, "Upload failed: could not save file.")
		return
	}
	if written > maxBackgroundUploadBytes {
		_ = os.Remove(savePath)
		a.renderAdminAppearanceError(w, r, "Upload failed: file size exceeds 10MB.")
		return
	}

	bgValue := fmt.Sprintf("url(/static/uploads/backgrounds/%s) center / cover no-repeat fixed", fileName)
	key := "site_background"
	if target == "admin" {
		key = "admin_background"
	}

	if err := a.store.SetSetting(key, bgValue); err != nil {
		_ = os.Remove(savePath)
		a.renderAdminAppearanceError(w, r, "Upload failed: could not save background setting.")
		return
	}

	a.invalidateCommonCache()
	http.Redirect(w, r, "/admin/appearance?ok=1", http.StatusFound)
}

func (a *App) renderAdminAppearanceError(w http.ResponseWriter, r *http.Request, flash string) {
	viewData := a.baseData(r, "admin-appearance")
	viewData.Flash = flash
	a.renderTemplate(w, "admin_appearance", viewData)
}
func (a *App) handleAdminAvatarReviews(w http.ResponseWriter, r *http.Request) {
	viewData := a.baseData(r, "admin-avatar-reviews")
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	if status == "" {
		status = data.AvatarStatusPending
	}

	switch r.Method {
	case http.MethodGet:
		requests, err := a.store.ListAvatarRequests(status, 200)
		if err != nil {
			http.Error(w, "failed to load avatar review requests", http.StatusInternalServerError)
			return
		}
		viewData.AvatarRequests = requests
		if r.URL.Query().Get("ok") == "1" {
			viewData.Flash = "Avatar review updated."
		}
		viewData.Settings["avatar_review_status"] = status
		a.renderTemplate(w, "admin_avatar_reviews", viewData)
		return
	case http.MethodPost:
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		requestID := parseInt64(r.FormValue("request_id"))
		action := strings.TrimSpace(r.FormValue("action"))
		reviewNote := strings.TrimSpace(r.FormValue("review_note"))
		status = strings.TrimSpace(r.FormValue("status"))
		if status == "" {
			status = data.AvatarStatusPending
		}
		if requestID == 0 || (action != "approve" && action != "reject") {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		user, _ := a.currentUser(r)
		if user == nil {
			http.Redirect(w, r, "/admin/login", http.StatusFound)
			return
		}
		approved := action == "approve"
		if err := a.store.ReviewAvatarRequest(requestID, user.ID, approved, reviewNote); err != nil {
			viewData.Flash = "Failed to review avatar request."
			requests, _ := a.store.ListAvatarRequests(status, 200)
			viewData.AvatarRequests = requests
			viewData.Settings["avatar_review_status"] = status
			a.renderTemplate(w, "admin_avatar_reviews", viewData)
			return
		}

		http.Redirect(w, r, "/admin/avatar-reviews?status="+status+"&ok=1", http.StatusFound)
		return
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
}

func (a *App) handleAdminReports(w http.ResponseWriter, r *http.Request) {
	viewData := a.baseData(r, "admin-reports")
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	if _, exists := r.URL.Query()["status"]; !exists {
		status = data.CommentReportStatusPending
	}
	viewData.Settings["report_status"] = status

	loadReports := func() error {
		commentReports, err := a.store.ListCommentReports(status, 200)
		if err != nil {
			return err
		}
		postReports, err := a.store.ListPostReports(status, 200)
		if err != nil {
			return err
		}
		viewData.CommentReports = commentReports
		viewData.PostReports = postReports
		return nil
	}

	switch r.Method {
	case http.MethodGet:
		if err := loadReports(); err != nil {
			http.Error(w, "failed to load reports", http.StatusInternalServerError)
			return
		}
		if r.URL.Query().Get("ok") == "1" {
			viewData.Flash = "Report processed."
		}
		a.renderTemplate(w, "admin_reports", viewData)
		return
	case http.MethodPost:
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Form parsing failed.", http.StatusBadRequest)
			return
		}

		reportID := parseInt64(r.FormValue("report_id"))
		reportType := strings.TrimSpace(strings.ToLower(r.FormValue("report_type")))
		action := strings.TrimSpace(r.FormValue("action"))
		reviewNote := strings.TrimSpace(r.FormValue("review_note"))
		status = strings.TrimSpace(r.FormValue("status"))
		viewData.Settings["report_status"] = status

		if reportID == 0 || (action != "approve" && action != "reject") {
			http.Error(w, "invalid parameters", http.StatusBadRequest)
			return
		}

		user, _ := a.currentUser(r)
		if user == nil {
			http.Redirect(w, r, "/admin/login", http.StatusFound)
			return
		}

		var err error
		switch reportType {
		case "post":
			err = a.store.ReviewPostReport(reportID, user.ID, action == "approve", reviewNote)
		case "", "comment":
			err = a.store.ReviewCommentReport(reportID, user.ID, action == "approve", reviewNote)
		default:
			http.Error(w, "invalid report type", http.StatusBadRequest)
			return
		}
		if err != nil {
			viewData.Flash = "Failed to process report."
			_ = loadReports()
			a.renderTemplate(w, "admin_reports", viewData)
			return
		}

		http.Redirect(w, r, "/admin/reports?status="+status+"&ok=1", http.StatusFound)
		return
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
}

// sanitizeBackgroundChoice 规范化背景选择值。
func sanitizeBackgroundChoice(raw string, allowed map[string]struct{}, fallback string) string {
	return normalizeBackgroundSetting(raw, allowed, fallback)
}

// buildPostFromForm 从表单构建文章对象。
func buildPostFromForm(r *http.Request, kind string) *data.Post {
	isPublic := parseBool(r.FormValue("is_public"))
	publishedAt := parseTime(r.FormValue("published_at"))

	return &data.Post{
		Title:       strings.TrimSpace(r.FormValue("title")),
		Slug:        strings.TrimSpace(r.FormValue("slug")),
		Summary:     strings.TrimSpace(r.FormValue("summary")),
		Content:     strings.TrimSpace(r.FormValue("content")),
		Kind:        kind,
		CoverURL:    strings.TrimSpace(r.FormValue("cover_url")),
		PublishedAt: publishedAt,
		IsPublic:    isPublic,
	}
}
