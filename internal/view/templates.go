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
        "formatDate": formatDate,
        "year":       func() int { return time.Now().Year() },
        "markdown":   markdownToHTML,
        "pageURL":    pageURL,
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

// markdownToHTML 将 Markdown 文本转换为安全的 HTML。
func markdownToHTML(content string) template.HTML {
    if strings.TrimSpace(content) == "" {
        return ""
    }

    extensions := parser.CommonExtensions | parser.AutoHeadingIDs | parser.FencedCode
    p := parser.NewWithExtensions(extensions)
    html := markdown.ToHTML([]byte(content), p, nil)
    return template.HTML(html)
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
