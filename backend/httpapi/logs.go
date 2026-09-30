package httpapi

import (
	"net/http"
	"strconv"

	"github.com/aceaura/model-surge-upstream/backend/apperr"
	"github.com/aceaura/model-surge-upstream/backend/ringlog"
)

// listLogs 增量拉取进程日志：?since=<seq> 只返回该 seq 之后的条目。
// 客户端轮询时自带 cursor，响应里再回一个 next 省去客户端自己记尾号。
func (h handler) listLogs(w http.ResponseWriter, r *http.Request) {
	var since int64
	if s := r.URL.Query().Get("since"); s != "" {
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil || v < 0 {
			writeCode(w, apperr.InvalidRequest, "since must be a non-negative integer")
			return
		}
		since = v
	}
	entries := ringlog.Since(since)
	writeJSON(w, http.StatusOK, map[string]any{
		"entries": entries,
		"next":    ringlog.LastSeq(),
	})
}

// clearLogs 清空环形缓冲（日志页的「清空」）。
func (h handler) clearLogs(w http.ResponseWriter, _ *http.Request) {
	ringlog.Clear()
	w.WriteHeader(http.StatusNoContent)
}
