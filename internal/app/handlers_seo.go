package app

import (
	"net/http"
	"strings"
	"time"
)

// cacheStaticHandler 为静态资源设置长缓存头。
// 配合模板中的 ?v=N 版本号查询参数实现缓存破除：
// - 浏览器看到 ?v=N 变化时认为 URL 不同，会重新请求；
// - 同一 ?v=N 下浏览器直接使用本地缓存，减少请求。
func cacheStaticHandler(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 仅对 GET/HEAD 请求设置缓存，其他方法不处理。
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		h.ServeHTTP(w, r)
	})
}

// handleSitemap 动态生成 sitemap.xml，包含首页、归档页、所有公开文章与页面。
// 搜索引擎通过 sitemap 发现站点全部可索引内容。
func (a *App) handleSitemap(w http.ResponseWriter, r *http.Request) {
	posts, _ := a.store.ListPosts("post", 0)
	pages, _ := a.store.ListPages()

	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">` + "\n")

	// 首页
	b.WriteString(urlEntry("/", time.Now().Format(time.RFC3339), "1.0", "daily"))
	// 归档页
	b.WriteString(urlEntry("/archive", time.Now().Format(time.RFC3339), "0.6", "weekly"))

	// 所有公开文章
	for _, p := range posts {
		b.WriteString(urlEntry("/post/"+p.Slug, p.PublishedAt.Format(time.RFC3339), "0.8", "monthly"))
	}

	// 所有公开页面
	for _, p := range pages {
		b.WriteString(urlEntry(pageURLForSitemap(p.Slug), p.UpdatedAt.Format(time.RFC3339), "0.5", "monthly"))
	}

	b.WriteString("</urlset>\n")

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = w.Write([]byte(b.String()))
}

// handleRobots 生成 robots.txt，禁止抓取后台与登录注册路径，指向 sitemap。
func (a *App) handleRobots(w http.ResponseWriter, r *http.Request) {
	var b strings.Builder
	b.WriteString("User-agent: *\n")
	b.WriteString("Allow: /\n")
	b.WriteString("Disallow: /admin\n")
	b.WriteString("Disallow: /member\n")
	b.WriteString("Disallow: /login\n")
	b.WriteString("Disallow: /register\n")
	b.WriteString("Disallow: /forgot-password\n")
	b.WriteString("Disallow: /reset-password\n")
	b.WriteString("Disallow: /verify-email\n")
	b.WriteString("Disallow: /post/like\n")
	b.WriteString("Disallow: /post/report\n")
	b.WriteString("Disallow: /board/react\n")
	b.WriteString("Disallow: /board/report\n")
	b.WriteString("Disallow: /report\n")
	b.WriteString("Disallow: /search\n")
	b.WriteString("\nSitemap: /sitemap.xml\n")

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = w.Write([]byte(b.String()))
}

// urlEntry 生成 sitemap 中的 <url> 节点。
func urlEntry(loc, lastmod, priority, changefreq string) string {
	return "  <url>\n    <loc>" + loc + "</loc>\n    <lastmod>" + lastmod + "</lastmod>\n    <priority>" + priority + "</priority>\n    <changefreq>" + changefreq + "</changefreq>\n  </url>\n"
}

// pageURLForSitemap 返回页面的规范 URL 路径。
// 与 view.pageURL 逻辑保持一致，但放在 app 包避免循环依赖。
func pageURLForSitemap(slug string) string {
	switch strings.TrimSpace(slug) {
	case "about":
		return "/about"
	case "say":
		return "/say"
	case "board":
		return "/board"
	default:
		if slug == "" {
			return "/"
		}
		return "/page/" + slug
	}
}
