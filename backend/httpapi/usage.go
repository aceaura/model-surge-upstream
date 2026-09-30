package httpapi

import (
	"net/http"
	"strconv"
	"time"

	"github.com/aceaura/model-surge-upstream/backend/apperr"
	"github.com/aceaura/model-surge-upstream/backend/store"
)

// usageFilter 从查询串解析用量过滤条件。start/end 为 RFC3339；
// 缺省即不限该侧，由客户端按「当天/7 天/30 天/全部」自行换算后传入。
func usageFilter(r *http.Request) (store.UsageFilter, error) {
	var f store.UsageFilter
	q := r.URL.Query()
	for _, spec := range []struct {
		key string
		dst *time.Time
	}{
		{"start", &f.Start},
		{"end", &f.End},
	} {
		if v := q.Get(spec.key); v != "" {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				return f, apperr.New(apperr.InvalidRequest, spec.key+" must be RFC3339")
			}
			*spec.dst = t
		}
	}
	f.Model = q.Get("model")
	f.Account = q.Get("account")
	f.Source = q.Get("source")
	return f, nil
}

// usageSummary 区间总指标：请求数/成功数/四桶/真实消耗/命中率。
func (h handler) usageSummary(w http.ResponseWriter, r *http.Request) {
	f, err := usageFilter(r)
	if err != nil {
		writeError(w, err)
		return
	}
	t, err := h.Usage.UsageSummary(r.Context(), f)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// usageTrend 趋势分桶。granularity=hour|day；缺省按跨度自动：
// 一天以内按小时，更长按天——与 CC Switch 使用趋势的默认口径一致。
func (h handler) usageTrend(w http.ResponseWriter, r *http.Request) {
	f, err := usageFilter(r)
	if err != nil {
		writeError(w, err)
		return
	}
	g := r.URL.Query().Get("granularity")
	switch g {
	case "hour", "day":
	case "":
		g = "day"
		if !f.Start.IsZero() && !f.End.IsZero() && f.End.Sub(f.Start) <= 24*time.Hour {
			g = "hour"
		}
	default:
		writeCode(w, apperr.InvalidRequest, "granularity must be hour or day")
		return
	}
	buckets, err := h.Usage.UsageTrend(r.Context(), f, g)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"granularity": g, "buckets": buckets})
}

// usageModels 按命名模型聚合，供「模型统计」页签。
func (h handler) usageModels(w http.ResponseWriter, r *http.Request) {
	f, err := usageFilter(r)
	if err != nil {
		writeError(w, err)
		return
	}
	groups, err := h.Usage.UsageByModel(r.Context(), f)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": groups})
}

// usageAccounts 按账号聚合，供「账号统计」页签。
func (h handler) usageAccounts(w http.ResponseWriter, r *http.Request) {
	f, err := usageFilter(r)
	if err != nil {
		writeError(w, err)
		return
	}
	groups, err := h.Usage.UsageByAccount(r.Context(), f)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"accounts": groups})
}

// usageLogs 请求明细分页，供「请求日志」页签。
func (h handler) usageLogs(w http.ResponseWriter, r *http.Request) {
	f, err := usageFilter(r)
	if err != nil {
		writeError(w, err)
		return
	}
	q := r.URL.Query()
	limit, offset := 50, 0
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 || n > 500 {
			writeCode(w, apperr.InvalidRequest, "limit must be in 1..500")
			return
		}
		limit = n
	}
	if v := q.Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeCode(w, apperr.InvalidRequest, "offset must be a non-negative integer")
			return
		}
		offset = n
	}
	logs, total, err := h.Usage.UsageLogs(r.Context(), f, limit, offset)
	if err != nil {
		writeError(w, err)
		return
	}
	if logs == nil {
		logs = []store.UsageLog{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"logs": logs, "total": total})
}
