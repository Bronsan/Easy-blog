package view

import (
    "fmt"
    "html/template"
    "os"
    "path/filepath"
    "strings"
    "time"

    "github.com/gomarkdown/markdown"
    "github.com/gomarkdown/markdown/parser"
    "github.com/microcosm-cc/bluemonday"
)

// LoadTemplates 读取模板目录中的所有 HTML 文件，并注入常用模板函数。
func LoadTemplates(dir string) (*template.Template, error) {
    entries, err := os.ReadDir(dir)
    if err != nil {
        return nil, fmt.Errorf("读取模板目录失败: %w", err)
    }

    var files []string
    for _, entry := range entries {
        if entry.IsDir() {
            continue
        }
        name := entry.Name()
        if strings.HasSuffix(name, ".html") {
            files = append(files, filepath.Join(dir, name))
        }
    }
    if len(files) == 0 {
        return nil, fmt.Errorf("模板目录为空: %s", dir)
    }

    funcMap := template.FuncMap{
        "formatDate":   formatDate,
        "formatYear":   formatYear,
        "formatMonth":  formatMonth,
        "year":         func() int { return time.Now().Year() },
        "markdown":     markdownToHTML,
        "pageURL":      pageURL,
        "add":          func(a, b int) int { return a + b },
        "min":          func(a, b int) int { if a < b { return a }; return b },
        "parseUA":      parseUA,
    }

    tmpl, err := template.New("base").Funcs(funcMap).ParseFiles(files...)
    if err != nil {
        return nil, fmt.Errorf("解析模板失败: %w", err)
    }
    return tmpl, nil
}

// formatDate 将时间格式化为常见的博客日期样式。
func formatDate(t time.Time) string {
    if t.IsZero() {
        return ""
    }
    return t.Format("2006-01-02 15:04")
}

// formatYear 返回年份字符串，用于归档页按年分组。
func formatYear(t time.Time) string {
    if t.IsZero() {
        return ""
    }
    return t.Format("2006")
}

// formatMonth 返回中文月份标签，用于归档页时间轴节点。
func formatMonth(t time.Time) string {
    if t.IsZero() {
        return ""
    }
    return t.Format("01月")
}

// markdownPolicy 是用于清理 Markdown 渲染输出的 HTML 白名单策略。
// 允许常见排版标签与属性，剥离 script/iframe/style 等危险元素，
// 同时为外链统一添加 rel="nofollow noopener" 并强制 target="_blank"。
var markdownPolicy = buildMarkdownPolicy()

func buildMarkdownPolicy() *bluemonday.Policy {
    p := bluemonday.UGCPolicy()
    // 额外允许代码块相关标签与 class（用于语法高亮）
    p.AllowElements("pre", "code", "kbd", "samp", "var")
    p.AllowAttrs("class").Matching(bluemonday.SpaceSeparatedTokens).OnElements("pre", "code", "span", "div")
    // 允许标题锚点 id（gomarkdown AutoHeadingIDs 会生成）
    p.AllowAttrs("id").OnElements("h1", "h2", "h3", "h4", "h5", "h6")
    // 外链统一添加安全属性
    p.RequireNoFollowOnLinks(true)
    p.RequireNoReferrerOnLinks(true)
    p.AddTargetBlankToFullyQualifiedLinks(true)
    return p
}

// markdownToHTML 将 Markdown 文本转换为安全的 HTML。
// 渲染流程：gomarkdown 生成 HTML → bluemonday 消毒 → template.HTML。
// 这一层消毒是必要的纵深防御：gomarkdown 默认允许内联 HTML，
// 用户内容中若含 <script> 等标签会原样输出导致存储型 XSS。
func markdownToHTML(content string) template.HTML {
    if strings.TrimSpace(content) == "" {
        return ""
    }

    extensions := parser.CommonExtensions | parser.AutoHeadingIDs | parser.FencedCode
    p := parser.NewWithExtensions(extensions)
    raw := markdown.ToHTML([]byte(content), p, nil)
    sanitized := markdownPolicy.SanitizeBytes(raw)
    return template.HTML(sanitized)
}

// pageURL 处理页面的固定别名，保持地址简洁。
func pageURL(slug string) string {
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

// parseUA 将 User-Agent 字符串解析为"浏览器 / 系统"的简洁描述。
func parseUA(ua string) string {
    ua = strings.TrimSpace(ua)
    if ua == "" {
        return ""
    }
    lower := strings.ToLower(ua)

    // 1) 解析操作系统
    osInfo := "未知系统"
    switch {
    case strings.Contains(lower, "windows nt 10"):
        osInfo = "Windows 10/11"
    case strings.Contains(lower, "windows nt 6.3"):
        osInfo = "Windows 8.1"
    case strings.Contains(lower, "windows nt 6.2"):
        osInfo = "Windows 8"
    case strings.Contains(lower, "windows nt 6.1"):
        osInfo = "Windows 7"
    case strings.Contains(lower, "windows"):
        osInfo = "Windows"
    case strings.Contains(lower, "mac os x") || strings.Contains(lower, "macintosh"):
        osInfo = "macOS"
    case strings.Contains(lower, "iphone") || strings.Contains(lower, "ipad"):
        osInfo = "iOS"
    case strings.Contains(lower, "android"):
        osInfo = "Android"
    case strings.Contains(lower, "linux"):
        osInfo = "Linux"
    }

    // 2) 解析浏览器（注意顺序：Edge/OPR 在 Chrome 之前判断）
    browser := "未知浏览器"
    switch {
    case strings.Contains(lower, "edg/"):
        browser = "Edge"
    case strings.Contains(lower, "opr/") || strings.Contains(lower, "opera"):
        browser = "Opera"
    case strings.Contains(lower, "firefox/"):
        browser = "Firefox"
    case strings.Contains(lower, "chrome/"):
        browser = "Chrome"
    case strings.Contains(lower, "safari/"):
        browser = "Safari"
    }

    return browser + " / " + osInfo
}
