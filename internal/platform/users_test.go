package platform

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
)

// TestFindUserAccess 验证平台登录 Cookie 被复用于用户列表，并按 remark_name 精确选择目标凭据。
func TestFindUserAccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/login":
			if request.Method != http.MethodPost {
				t.Fatalf("login method = %s", request.Method)
			}
			if err := request.ParseForm(); err != nil || request.Form.Get("username") != "platform-hub" {
				t.Fatal("unexpected login form")
			}
			http.SetCookie(writer, &http.Cookie{Name: "session", Value: "ok", Path: "/"})
			writer.WriteHeader(http.StatusOK)
		case "/api/hermes/users":
			cookie, err := request.Cookie("session")
			if err != nil || cookie.Value != "ok" {
				writer.WriteHeader(http.StatusUnauthorized)
				return
			}
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`[{"port":16020,"remark_name":"test-user","external_jwt":"jwt-value"}]`))
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client, err := NewClient(server.URL, Credentials{Username: "platform-hub", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Login(context.Background()); err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	user, err := client.FindUserAccess(context.Background(), "test-user")
	if err != nil {
		t.Fatalf("FindUserAccess() error = %v", err)
	}
	if user.Port != 16020 || user.ExternalJWT != "jwt-value" {
		t.Fatalf("user = %#v", user)
	}
}

// TestFindUserAccessMissing 验证路由表配置的 username 不存在时会返回稳定错误。
func TestFindUserAccessMissing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/login" {
			http.SetCookie(writer, &http.Cookie{Name: "session", Value: "ok", Path: "/"})
			return
		}
		_, _ = writer.Write([]byte(`[]`))
	}))
	defer server.Close()
	client, _ := NewClient(server.URL, Credentials{Username: "platform-hub", Password: "secret"})
	_ = client.Login(context.Background())
	if _, err := client.FindUserAccess(context.Background(), "不存在"); err != ErrUserNotFound {
		t.Fatalf("error = %v", err)
	}
}

// TestExchangeExternalJWT 验证将 external_jwt 放入 JSON 请求体，并只返回 Studio token。
func TestExchangeExternalJWT(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/api/auth/exchange-external-jwt" {
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		var input struct {
			ExternalJWT string `json:"external_jwt"`
		}
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil || input.ExternalJWT != "external-jwt" {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"token":"studio-jwt","userId":1}`))
	}))
	defer server.Close()

	token, err := ExchangeExternalJWT(context.Background(), server.URL+"/api/auth/exchange-external-jwt", "external-jwt")
	if err != nil {
		t.Fatalf("ExchangeExternalJWT() error = %v", err)
	}
	if token != "studio-jwt" {
		t.Fatalf("token = %q", token)
	}
}

// TestStudioBaseURL 验证端口来自用户列表后会覆盖管理平台端口，且保留协议。
func TestStudioBaseURL(t *testing.T) {
	got, err := StudioBaseURL("https://platform.example.internal:8080/", 16140)
	if err != nil {
		t.Fatalf("StudioBaseURL() error = %v", err)
	}
	if got != "https://platform.example.internal:16140" {
		t.Fatalf("StudioBaseURL() = %q", got)
	}
}

// TestAccessResolver 验证一次解析会登录平台、获取用户 external_jwt，并在该用户端口交换 Studio token。
func TestAccessResolver(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/login":
			http.SetCookie(writer, &http.Cookie{Name: "session", Value: "ok", Path: "/"})
		case "/api/hermes/users":
			if _, err := request.Cookie("session"); err != nil {
				writer.WriteHeader(http.StatusUnauthorized)
				return
			}
			parsed, _ := url.Parse(server.URL)
			port, _ := strconv.Atoi(parsed.Port())
			_, _ = writer.Write([]byte(`[{"port":` + strconv.Itoa(port) + `,"remark_name":"test-user","external_jwt":"external-jwt"}]`))
		case "/api/auth/exchange-external-jwt":
			var input exchangeRequest
			if err := json.NewDecoder(request.Body).Decode(&input); err != nil || input.ExternalJWT != "external-jwt" {
				writer.WriteHeader(http.StatusBadRequest)
				return
			}
			_, _ = writer.Write([]byte(`{"token":"studio-token"}`))
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	resolver, err := NewAccessResolver(server.URL, Credentials{Username: "hub", Password: "secret"})
	if err != nil {
		t.Fatalf("NewAccessResolver() error = %v", err)
	}
	access, err := resolver.Resolve(context.Background(), "test-user")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if access.BaseURL != server.URL || access.Token != "studio-token" {
		t.Fatalf("access = %#v", access)
	}
}
