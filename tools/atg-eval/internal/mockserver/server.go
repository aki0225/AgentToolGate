package mockserver

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"agenttoolgate/evaluation/internal/redact"
)

const defaultMaxBodyBytes int64 = 64 * 1024

type Options struct {
	MaxBodyBytes   int64
	ResponseStatus int
	ResponseBody   string
	Redactor       *redact.Redactor
}

type RequestRecord struct {
	Method            string            `json:"method"`
	Path              string            `json:"path"`
	Query             string            `json:"query,omitempty"`
	Headers           map[string]string `json:"headers"`
	Body              string            `json:"body,omitempty"`
	BodyBytes         int               `json:"bodyBytes"`
	SensitiveDetected bool              `json:"sensitiveDetected"`
	Truncated         bool              `json:"truncated"`
}

type Server struct {
	listener net.Listener
	server   *http.Server
	redactor *redact.Redactor
	maxBody  int64

	mu       sync.Mutex
	requests []RequestRecord
}

func New(options Options) (*Server, error) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("启动 loopback mock server 失败：%w", err)
	}
	host, _, err := net.SplitHostPort(listener.Addr().String())
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		listener.Close()
		return nil, fmt.Errorf("mock server 未绑定到 loopback：%s", listener.Addr())
	}

	maxBody := options.MaxBodyBytes
	if maxBody <= 0 {
		maxBody = defaultMaxBodyBytes
	}
	responseStatus := options.ResponseStatus
	if responseStatus == 0 {
		responseStatus = http.StatusOK
	}
	responseBody := options.ResponseBody
	if responseBody == "" {
		responseBody = `{"ok":true}`
	}
	redactor := options.Redactor
	if redactor == nil {
		redactor = redact.New(redact.Options{})
	}

	result := &Server{
		listener: listener,
		redactor: redactor,
		maxBody:  maxBody,
	}
	result.server = &http.Server{
		Handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			result.handle(writer, request, responseStatus, responseBody)
		}),
		ReadHeaderTimeout: 3 * time.Second,
	}
	go func() {
		_ = result.server.Serve(listener)
	}()
	return result, nil
}

func (s *Server) URL() string {
	return "http://" + s.listener.Addr().String()
}

func (s *Server) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := s.server.Shutdown(ctx); err != nil {
		_ = s.listener.Close()
		return fmt.Errorf("关闭 mock server 失败：%w", err)
	}
	return nil
}

func (s *Server) Requests() []RequestRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]RequestRecord, len(s.requests))
	copy(result, s.requests)
	return result
}

func (s *Server) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

func ValidateLoopbackURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("URL 无效：%w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("URL scheme 必须为 http 或 https")
	}
	if parsed.User != nil {
		return fmt.Errorf("URL 不能包含用户名或密码")
	}
	host := parsed.Hostname()
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("URL host 必须是 loopback IP 字面量")
	}
	port := parsed.Port()
	if port == "" {
		return fmt.Errorf("URL 必须显式指定端口")
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return fmt.Errorf("URL 端口无效")
	}
	return nil
}

func (s *Server) handle(writer http.ResponseWriter, request *http.Request, responseStatus int, responseBody string) {
	if !remoteIsLoopback(request.RemoteAddr) {
		http.Error(writer, "forbidden", http.StatusForbidden)
		return
	}

	body, readErr := io.ReadAll(io.LimitReader(request.Body, s.maxBody+1))
	truncated := int64(len(body)) > s.maxBody
	if truncated {
		body = body[:s.maxBody]
	}
	rawPath := request.URL.EscapedPath()
	rawQuery := request.URL.RawQuery
	sensitiveDetected := s.redactor.ContainsSensitiveText(string(body)) ||
		s.redactor.ContainsSensitiveText(rawPath) ||
		s.redactor.ContainsSensitiveText(rawQuery)
	for key, values := range request.Header {
		if redact.SensitiveKey(key) {
			sensitiveDetected = true
			break
		}
		for _, value := range values {
			if s.redactor.ContainsSensitiveText(value) {
				sensitiveDetected = true
				break
			}
		}
	}
	record := RequestRecord{
		Method:            request.Method,
		Path:              s.redactor.Text(rawPath),
		Query:             s.redactor.Text(rawQuery),
		Headers:           s.redactor.Headers(request.Header),
		Body:              s.redactor.Text(string(body)),
		BodyBytes:         len(body),
		SensitiveDetected: sensitiveDetected,
		Truncated:         truncated,
	}
	s.mu.Lock()
	s.requests = append(s.requests, record)
	s.mu.Unlock()

	if readErr != nil {
		http.Error(writer, "读取请求失败", http.StatusBadRequest)
		return
	}
	if truncated {
		http.Error(writer, "请求体过大", http.StatusRequestEntityTooLarge)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(responseStatus)
	_, _ = io.WriteString(writer, responseBody)
}

func remoteIsLoopback(remoteAddress string) bool {
	host, _, err := net.SplitHostPort(strings.TrimSpace(remoteAddress))
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
