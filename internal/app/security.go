package app

import (
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// authAttemptState 记录某个 IP 的登录失败状态。
type authAttemptState struct {
	failCount   int
	windowStart time.Time
	blockedTill time.Time
}

// authRateLimiter 是一个简单的内存限流器，用于登录接口防爆破。
//
// 设计目标：
// 1) 不引入外部依赖，部署简单；
// 2) 支持“窗口内失败次数”与“触发后封禁时长”；
// 3) 允许登录成功后自动清零，减少误伤。
type authRateLimiter struct {
	mu sync.Mutex

	maxFails int
	window   time.Duration
	blockFor time.Duration
	attempts map[string]authAttemptState
}

func newAuthRateLimiter(maxFails int, window, blockFor time.Duration) *authRateLimiter {
	return &authRateLimiter{
		maxFails: maxFails,
		window:   window,
		blockFor: blockFor,
		attempts: make(map[string]authAttemptState),
	}
}

// allow 判断该 IP 当前是否允许继续尝试登录。
func (l *authRateLimiter) allow(ip string, now time.Time) (bool, time.Duration) {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		// 无法识别来源 IP 时不直接拦截，避免误伤内网代理场景。
		return true, 0
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	state, ok := l.attempts[ip]
	if !ok {
		return true, 0
	}
	if now.Before(state.blockedTill) {
		return false, state.blockedTill.Sub(now)
	}
	return true, 0
}

// onFailure 在登录失败时更新计数并决定是否封禁该 IP。
func (l *authRateLimiter) onFailure(ip string, now time.Time) {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	state, ok := l.attempts[ip]
	if !ok || now.Sub(state.windowStart) > l.window {
		state = authAttemptState{
			failCount:   1,
			windowStart: now,
			blockedTill: time.Time{},
		}
		l.attempts[ip] = state
		return
	}

	state.failCount++
	if state.failCount >= l.maxFails {
		state.blockedTill = now.Add(l.blockFor)
		// 触发封禁后重置窗口计数，防止下一窗口受到历史计数影响。
		state.failCount = 0
		state.windowStart = now
	}
	l.attempts[ip] = state
}

// onSuccess 在登录成功时清空该 IP 的失败记录。
func (l *authRateLimiter) onSuccess(ip string) {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.attempts, ip)
}

// csrfGuard 对所有“修改类请求”执行同源校验，作为 CSRF 防护。
//
// 防护策略：
// 1) 仅校验可能改数据的方法：POST/PUT/PATCH/DELETE；
// 2) 优先校验 Origin；若无 Origin，再校验 Referer；
// 3) 两者都缺失时放行（兼容部分客户端），但现代浏览器通常会带其中之一。
func (a *App) csrfGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
			if !sameOriginRequest(r) {
				http.Error(w, "跨站请求已被拦截", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// securityHeaders 统一设置常见安全响应头。
func (a *App) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Content-Security-Policy", "frame-ancestors 'self';")
		next.ServeHTTP(w, r)
	})
}

func sameOriginRequest(r *http.Request) bool {
	if r == nil {
		return false
	}

	targetHost := strings.TrimSpace(r.Host)
	if targetHost == "" {
		return false
	}

	if origin := strings.TrimSpace(r.Header.Get("Origin")); origin != "" {
		u, err := url.Parse(origin)
		if err != nil {
			return false
		}
		return strings.EqualFold(u.Host, targetHost)
	}

	if referer := strings.TrimSpace(r.Header.Get("Referer")); referer != "" {
		u, err := url.Parse(referer)
		if err != nil {
			return false
		}
		return strings.EqualFold(u.Host, targetHost)
	}

	return true
}
