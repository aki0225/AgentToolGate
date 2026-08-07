package otelcollector

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	collectortrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

const defaultShutdownTimeout = 3 * time.Second

// Collector 是仅绑定 loopback 的最小 OTLP gRPC trace 接收器。
// 它不落盘、不暴露 span 原文，只记录导出次数与 synthetic secret 是否命中。
type Collector struct {
	collectortrace.UnimplementedTraceServiceServer

	listener net.Listener
	server   *grpc.Server
	secrets  [][]byte
	notify   chan struct{}

	mu                sync.Mutex
	exportCount       int
	sensitiveDetected bool
	closeOnce         sync.Once
	closeErr          error
}

func Start(secrets []string) (*Collector, error) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("启动 OTLP loopback collector 失败：%w", err)
	}
	host, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		_ = listener.Close()
		return nil, fmt.Errorf("OTLP collector 未绑定到 loopback")
	}
	if _, err := strconv.Atoi(port); err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("OTLP collector 端口无效")
	}

	collector := &Collector{
		listener: listener,
		server:   grpc.NewServer(),
		notify:   make(chan struct{}, 1),
	}
	for _, secret := range secrets {
		if trimmed := strings.TrimSpace(secret); trimmed != "" {
			collector.secrets = append(collector.secrets, []byte(trimmed))
		}
	}
	collectortrace.RegisterTraceServiceServer(collector.server, collector)
	go func() {
		_ = collector.server.Serve(listener)
	}()
	return collector, nil
}

func (c *Collector) Endpoint() string {
	if c == nil || c.listener == nil {
		return ""
	}
	return c.listener.Addr().String()
}

func (c *Collector) Export(
	_ context.Context,
	request *collectortrace.ExportTraceServiceRequest,
) (*collectortrace.ExportTraceServiceResponse, error) {
	raw, err := proto.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("编码 OTLP trace 请求失败：%w", err)
	}

	c.mu.Lock()
	c.exportCount++
	for _, secret := range c.secrets {
		if len(secret) > 0 && bytes.Contains(raw, secret) {
			c.sensitiveDetected = true
			break
		}
	}
	c.mu.Unlock()
	select {
	case c.notify <- struct{}{}:
	default:
	}
	return &collectortrace.ExportTraceServiceResponse{}, nil
}

func (c *Collector) ExportCount() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.exportCount
}

func (c *Collector) SensitiveValueDetected() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sensitiveDetected
}

func (c *Collector) WaitForExports(ctx context.Context, minimum int) error {
	if c == nil {
		return fmt.Errorf("OTLP collector 不能为空")
	}
	if minimum < 1 {
		return fmt.Errorf("OTLP export 最小数量必须大于 0")
	}
	for {
		if c.ExportCount() >= minimum {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("等待 OTLP trace 导出失败：%w", ctx.Err())
		case <-c.notify:
		}
	}
}

func (c *Collector) Close() error {
	if c == nil {
		return nil
	}
	c.closeOnce.Do(func() {
		done := make(chan struct{})
		go func() {
			c.server.GracefulStop()
			close(done)
		}()
		timer := time.NewTimer(defaultShutdownTimeout)
		defer timer.Stop()
		select {
		case <-done:
		case <-timer.C:
			c.server.Stop()
			c.closeErr = fmt.Errorf("停止 OTLP collector 超时")
		}
		if err := c.listener.Close(); err != nil && !strings.Contains(strings.ToLower(err.Error()), "closed") {
			c.closeErr = fmt.Errorf("关闭 OTLP collector listener 失败：%w", err)
		}
	})
	return c.closeErr
}
