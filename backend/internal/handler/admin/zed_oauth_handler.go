package admin

import (
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/pkg/zed"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type ZedOAuthHandler struct {
	zedOAuthService *service.ZedOAuthService
}

func NewZedOAuthHandler(zedOAuthService *service.ZedOAuthService) *ZedOAuthHandler {
	return &ZedOAuthHandler{zedOAuthService: zedOAuthService}
}

type ZedAuthURLResponse struct {
	AuthURL   string `json:"auth_url"`
	SessionID string `json:"session_id"`
}

// GenerateAuthURL 生成 Zed 原生登录链接。
//
// 回调固定指向 http://127.0.0.1:<port>，本服务并不监听该端口：浏览器会连接失败，
// 但地址栏保留完整回调 URL，由管理员复制粘贴回来交换。RSA 私钥留在服务端 session
// 中，因此必须与 ExchangeCode 落在同一实例。
func (h *ZedOAuthHandler) GenerateAuthURL(c *gin.Context) {
	if h.zedOAuthService == nil {
		response.BadRequest(c, "Zed OAuth 服务未配置")
		return
	}
	authURL, sessionID, err := h.zedOAuthService.GenerateAuthURL(c.Request.Context())
	if err != nil {
		response.BadRequest(c, "生成授权链接失败: "+err.Error())
		return
	}
	response.Success(c, ZedAuthURLResponse{AuthURL: authURL, SessionID: sessionID})
}

type ZedExchangeCodeRequest struct {
	SessionID string `json:"session_id" binding:"required"`
	// CallbackURL 是管理员从浏览器地址栏粘贴回来的完整回调 URL；
	// 也接受仅包含 user_id / access_token 的裸 query string。
	CallbackURL string `json:"callback_url" binding:"required"`
	// SystemID 必须取自管理员本机 Zed 的 system_id。上游把套餐与试用资格绑定在
	// 账号注册时使用的 system_id 上，填错仍能成功铸造 token，但推理会返回
	// trial_blocked (403)；留空则整个 header 被省略，等于必然不可用。
	SystemID string `json:"system_id" binding:"required"`
}

func (h *ZedOAuthHandler) ExchangeCode(c *gin.Context) {
	if h.zedOAuthService == nil {
		response.BadRequest(c, "Zed OAuth 服务未配置")
		return
	}
	var req ZedExchangeCodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "请求无效: "+err.Error())
		return
	}
	credentials, err := h.zedOAuthService.ExchangeCallback(c.Request.Context(), req.SessionID, req.CallbackURL, req.SystemID)
	if err != nil {
		response.BadRequest(c, "交换回调失败: "+err.Error())
		return
	}
	response.Success(c, credentials)
}

type ZedImportTokenRequest struct {
	// UserID 与 AccessToken 可从已登录的 Zed 客户端凭据中取得，供无法走浏览器
	// 授权流程的场景直接导入。
	UserID      string `json:"user_id" binding:"required"`
	AccessToken string `json:"access_token" binding:"required"`
	SystemID    string `json:"system_id" binding:"required"`
	GitHubLogin string `json:"github_user_login"`
}

func (h *ZedOAuthHandler) ImportToken(c *gin.Context) {
	var req ZedImportTokenRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "请求无效: "+err.Error())
		return
	}

	// binding:"required" rejects "" but not "   ", so trim and validate here as
	// well: this endpoint must not emit a credential set the mint layer will
	// later refuse.
	systemID := strings.TrimSpace(req.SystemID)
	if err := zed.ValidateSystemID(systemID); err != nil {
		response.BadRequest(c, "请求无效: "+err.Error())
		return
	}

	credentials := map[string]any{
		zed.CredentialUserID:      req.UserID,
		zed.CredentialAccessToken: req.AccessToken,
		zed.CredentialSystemID:    systemID,
	}
	if req.GitHubLogin != "" {
		credentials[zed.CredentialGitHubLogin] = req.GitHubLogin
	}
	response.Success(c, credentials)
}
