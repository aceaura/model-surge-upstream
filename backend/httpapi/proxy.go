package httpapi

import (
	"net/http"
	"strings"

	"github.com/aceaura/model-surge-upstream/backend/proxysettings"
)

// 代理转发面配置的管理接口。与账号凭据不同，api_key 读取时回真实值：
// 管理面持钥者需要把它拷进第三方客户端（Cursor/VS Code 等），
// 脱敏则无法拷贝——这与截图中「显示真实值」的交互一致。

type proxySettingsRequest struct {
	APIKey  string `json:"api_key"`
	Port    int    `json:"port"`
	LanOpen bool   `json:"lan_open"`
}

func (h handler) getProxySettings(w http.ResponseWriter, r *http.Request) {
	s, err := h.ProxySettings.Get(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"settings": s})
}

func (h handler) putProxySettings(w http.ResponseWriter, r *http.Request) {
	var req proxySettingsRequest
	if !decodeBody(w, r, &req) {
		return
	}
	s := proxysettings.Settings{
		APIKey:  strings.TrimSpace(req.APIKey),
		Port:    req.Port,
		LanOpen: req.LanOpen,
	}
	if err := proxysettings.Validate(s); err != nil {
		writeError(w, err)
		return
	}
	// 先应用再落库：应用失败（典型是端口被占）时配置本就没生效，
	// 写库只会让下次启动再撞一次同样的错。
	if err := h.ProxyApply.Apply(s); err != nil {
		writeError(w, err)
		return
	}
	if err := h.ProxySettings.Put(r.Context(), s); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"settings": s})
}
