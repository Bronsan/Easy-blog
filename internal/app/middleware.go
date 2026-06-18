package app

import (
	"net/http"
	"strings"
	"time"

	"blog/internal/data"
)

const sessionCookieName = "blog_session"

// currentUser 根据会话 Cookie 获取当前登录用户。
func (a *App) currentUser(r *http.Request) (*data.User, error) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return nil, err
	}

	userID, expiresAt, err := a.store.GetSession(cookie.Value)
	if err != nil {
		return nil, err
	}

	if time.Now().Unix() > expiresAt {
		_ = a.store.DeleteSession(cookie.Value)
		return nil, http.ErrNoCookie
	}

	user, err := a.store.GetUserByID(userID)
	if err != nil {
		return nil, err
	}
	if user.IsBanned {
		_ = a.store.DeleteSession(cookie.Value)
		return nil, http.ErrNoCookie
	}
	return user, nil
}

// requireLogin 包裹后台接口，未登录时跳转至登录页。
func (a *App) requireLogin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, err := a.currentUser(r)
		if err != nil || user == nil {
			http.Redirect(w, r, "/admin/login", http.StatusFound)
			return
		}
		next(w, r)
	}
}

// requireRoles 限制后台接口可访问角色；未满足时返回 403。
func (a *App) requireRoles(roles ...string) func(http.HandlerFunc) http.HandlerFunc {
	allowed := make(map[string]struct{}, len(roles))
	for _, role := range roles {
		allowed[role] = struct{}{}
	}

	return func(next http.HandlerFunc) http.HandlerFunc {
		return a.requireLogin(func(w http.ResponseWriter, r *http.Request) {
			user, err := a.currentUser(r)
			if err != nil || user == nil {
				http.Redirect(w, r, "/admin/login", http.StatusFound)
				return
			}
			if _, ok := allowed[user.Role]; !ok {
				// 访问者访问 /admin 时，直接引导到访问者后台，避免在权限墙上来回试错。
				if user.Role == data.RoleVisitor && strings.HasPrefix(r.URL.Path, "/admin") {
					http.Redirect(w, r, "/member", http.StatusFound)
					return
				}
				// 非访问者误入 /member 时，统一引导回主后台。
				if user.Role != data.RoleVisitor && strings.HasPrefix(r.URL.Path, "/member") {
					http.Redirect(w, r, "/admin", http.StatusFound)
					return
				}
				http.Error(w, "无权限访问该模块", http.StatusForbidden)
				return
			}
			next(w, r)
		})
	}
}

// setSessionCookie 写入登录会话 Cookie。
func setSessionCookie(w http.ResponseWriter, sessionID string, expiresAt int64) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    sessionID,
		Path:     "/",
		Expires:  time.Unix(expiresAt, 0),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// clearSessionCookie 清除 Cookie。
func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		Expires:  time.Unix(0, 0),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}
