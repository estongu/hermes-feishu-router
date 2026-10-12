// Package platform 负责登录 Hermes 管理平台并查询目标容器的外部访问凭据。
package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/your-github-username/hermes-feishu-router/internal/model"
)

var ErrUserNotFound = errors.New("平台中未找到匹配 remark_name 的用户")

// exchangeRequest 是交换接口要求的最小请求体。external_jwt 不会写入数据库或日志。
type exchangeRequest struct {
	ExternalJWT string `json:"external_jwt"`
}

// exchangeResponse 是交换接口的最小响应体，只读取可用于 Studio 的 token。
type exchangeResponse struct {
	Token string `json:"token"`
}

// Credentials 是平台登录页所需账号密码，仅从运行配置读取。
type Credentials struct {
	Username string
	Password string
}

// User 是 /api/hermes/users 响应中 Hub 路由所需的最小字段集合。
type User struct {
	Port        int    `json:"port"`
	RemarkName  string `json:"remark_name"`
	ExternalJWT string `json:"external_jwt"`
}

// Client 复用登录后的 Cookie 会话访问平台用户列表。
type Client struct {
	baseURL string
	creds   Credentials
	client  *http.Client
}

// AccessResolver 通过管理平台为每条消息动态获取目标 Studio 的地址与短期访问令牌。
// 不缓存 token，避免令牌跨用户或跨容器复用。
type AccessResolver struct {
	platformBaseURL string
	credentials     Credentials
}

// NewAccessResolver 创建动态 Studio 访问解析器。
func NewAccessResolver(platformBaseURL string, credentials Credentials) (*AccessResolver, error) {
	if _, err := NewClient(platformBaseURL, credentials); err != nil {
		return nil, err
	}
	return &AccessResolver{platformBaseURL: strings.TrimRight(strings.TrimSpace(platformBaseURL), "/"), credentials: credentials}, nil
}

// Resolve 登录管理平台、精确匹配 remark_name，并在其容器中交换 short-lived Studio token。
func (r *AccessResolver) Resolve(ctx context.Context, username string) (model.StudioAccess, error) {
	if strings.TrimSpace(username) == "" {
		return model.StudioAccess{}, fmt.Errorf("路由 username 不能为空")
	}
	client, err := NewClient(r.platformBaseURL, r.credentials)
	if err != nil {
		return model.StudioAccess{}, err
	}
	if err := client.Login(ctx); err != nil {
		return model.StudioAccess{}, err
	}
	user, err := client.FindUserAccess(ctx, username)
	if err != nil {
		return model.StudioAccess{}, err
	}
	studioBaseURL, err := StudioBaseURL(r.platformBaseURL, user.Port)
	if err != nil {
		return model.StudioAccess{}, err
	}
	token, err := ExchangeExternalJWT(ctx, studioBaseURL+"/api/auth/exchange-external-jwt", user.ExternalJWT)
	if err != nil {
		return model.StudioAccess{}, err
	}
	return model.StudioAccess{BaseURL: studioBaseURL, Token: token}, nil
}

// NewClient 创建平台客户端。输入为平台根地址和登录凭据。
func NewClient(baseURL string, credentials Credentials) (*Client, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("非法平台地址")
	}
	if strings.TrimSpace(credentials.Username) == "" || credentials.Password == "" {
		return nil, fmt.Errorf("平台登录凭据不完整")
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("创建 Cookie 容器: %w", err)
	}
	return &Client{baseURL: strings.TrimRight(parsed.String(), "/"), creds: credentials, client: &http.Client{Jar: jar, Timeout: 15 * time.Second}}, nil
}

// Login 向 POST /login 提交 application/x-www-form-urlencoded 表单并保留会话 Cookie。
func (c *Client) Login(ctx context.Context) error {
	form := url.Values{"username": {c.creds.Username}, "password": {c.creds.Password}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/login", strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("创建平台登录请求: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("请求平台登录接口: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("平台登录失败，HTTP 状态 %d", response.StatusCode)
	}
	return nil
}

// FindUserAccess 读取用户列表，并按路由表 username 精确匹配 remark_name。
// 输出仅含目标端口与 external_jwt，不持久化该 JWT。
func (c *Client) FindUserAccess(ctx context.Context, username string) (User, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/hermes/users", nil)
	if err != nil {
		return User{}, fmt.Errorf("创建用户列表请求: %w", err)
	}
	response, err := c.client.Do(req)
	if err != nil {
		return User{}, fmt.Errorf("请求用户列表: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return User{}, fmt.Errorf("读取用户列表失败，HTTP 状态 %d", response.StatusCode)
	}
	var users []User
	if err := json.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(&users); err != nil {
		return User{}, fmt.Errorf("解析用户列表: %w", err)
	}
	for _, user := range users {
		if user.RemarkName != username {
			continue
		}
		if user.Port < 1 || user.Port > 65535 || strings.TrimSpace(user.ExternalJWT) == "" {
			return User{}, fmt.Errorf("用户 %q 缺少有效容器访问凭据", username)
		}
		return user, nil
	}
	return User{}, ErrUserNotFound
}

// ExchangeExternalJWT 将管理平台返回的 external_jwt 换取目标 Ekko Studio 可用的访问令牌。
// exchangeURL 为完整接口地址，externalJWT 为用户列表中的 external_jwt，返回值仅应在当前请求中使用。
func ExchangeExternalJWT(ctx context.Context, exchangeURL, externalJWT string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(exchangeURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("非法令牌交换地址")
	}
	if strings.TrimSpace(externalJWT) == "" {
		return "", fmt.Errorf("external_jwt 不能为空")
	}
	payload, err := json.Marshal(exchangeRequest{ExternalJWT: externalJWT})
	if err != nil {
		return "", fmt.Errorf("编码令牌交换请求: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, parsed.String(), bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("创建令牌交换请求: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return "", fmt.Errorf("请求令牌交换接口: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("令牌交换失败，HTTP 状态 %d", response.StatusCode)
	}
	var result exchangeResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result); err != nil {
		return "", fmt.Errorf("解析令牌交换响应: %w", err)
	}
	if strings.TrimSpace(result.Token) == "" {
		return "", fmt.Errorf("令牌交换响应缺少 token")
	}
	return result.Token, nil
}

// StudioBaseURL 根据管理平台根地址和用户所属容器端口生成 Studio 根地址。
// platformBaseURL 只用于取得协议和主机，port 必须来自 /api/hermes/users 响应。
func StudioBaseURL(platformBaseURL string, port int) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(platformBaseURL))
	if err != nil || parsed.Scheme == "" || parsed.Hostname() == "" {
		return "", fmt.Errorf("非法平台地址")
	}
	if port < 1 || port > 65535 {
		return "", fmt.Errorf("非法 Studio 端口")
	}
	return (&url.URL{Scheme: parsed.Scheme, Host: net.JoinHostPort(parsed.Hostname(), strconv.Itoa(port))}).String(), nil
}

// ExchangeExternalJWTForUser 将指定用户的 external_jwt 交换为其所属 Studio 容器的访问令牌。
// 返回的 token 仅用于当次请求，不应持久化或记录到日志。
func ExchangeExternalJWTForUser(ctx context.Context, platformBaseURL string, user User) (string, error) {
	studioBaseURL, err := StudioBaseURL(platformBaseURL, user.Port)
	if err != nil {
		return "", err
	}
	return ExchangeExternalJWT(ctx, studioBaseURL+"/api/auth/exchange-external-jwt", user.ExternalJWT)
}
