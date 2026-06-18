package app

import (
	"time"

	"blog/internal/data"
)

// loadCommonData 从缓存或数据库读取前后台共用数据（设置、页面、统计）。
func (a *App) loadCommonData() (map[string]string, []data.Post, Stats) {
	a.cacheMu.RLock()
	if !a.cachedLoadedAt.IsZero() && time.Since(a.cachedLoadedAt) < a.cacheTTL {
		settingsCopy := make(map[string]string, len(a.cachedSettings))
		for k, v := range a.cachedSettings {
			settingsCopy[k] = v
		}
		pagesCopy := make([]data.Post, 0, len(a.cachedPages))
		for _, p := range a.cachedPages {
			pagesCopy = append(pagesCopy, p)
		}
		statsCopy := a.cachedStats
		a.cacheMu.RUnlock()
		return settingsCopy, pagesCopy, statsCopy
	}
	a.cacheMu.RUnlock()

	settings, _ := a.store.GetSettings()
	posts, views, _ := a.store.GetStats()
	pages, _ := a.store.ListPages()

	a.cacheMu.Lock()
	a.cachedSettings = settings
	a.cachedPages = pages
	a.cachedStats = Stats{Posts: posts, Views: views}
	a.cachedLoadedAt = time.Now()
	a.cacheMu.Unlock()

	settingsCopy := make(map[string]string, len(settings))
	for k, v := range settings {
		settingsCopy[k] = v
	}
	pagesCopy := make([]data.Post, 0, len(pages))
	for _, p := range pages {
		pagesCopy = append(pagesCopy, p)
	}
	return settingsCopy, pagesCopy, Stats{Posts: posts, Views: views}
}

// invalidateCommonCache 在写操作后主动失效缓存，避免看到旧数据。
func (a *App) invalidateCommonCache() {
	a.cacheMu.Lock()
	a.cachedLoadedAt = time.Time{}
	a.cacheMu.Unlock()
}
