package otelcollector

import (
	"context"
	"testing"
	"time"

	collectortrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	common "go.opentelemetry.io/proto/otlp/common/v1"
	resource "go.opentelemetry.io/proto/otlp/resource/v1"
	trace "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func TestCollectorCapturesExportsWithoutExposingPayload(t *testing.T) {
	const secret = "otel-synthetic-secret"
	collector, err := Start([]string{secret})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(func() {
		if err := collector.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})

	connection, err := grpc.NewClient(
		collector.Endpoint(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient() error = %v", err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	client := collectortrace.NewTraceServiceClient(connection)
	_, err = client.Export(context.Background(), &collectortrace.ExportTraceServiceRequest{
		ResourceSpans: []*trace.ResourceSpans{{
			Resource: &resource.Resource{
				Attributes: []*common.KeyValue{{
					Key: "safe.attribute",
					Value: &common.AnyValue{
						Value: &common.AnyValue_StringValue{StringValue: "safe"},
					},
				}},
			},
		}},
	})
	if err != nil {
		t.Fatalf("Export() error = %v", err)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := collector.WaitForExports(waitCtx, 1); err != nil {
		t.Fatalf("WaitForExports() error = %v", err)
	}
	if collector.ExportCount() != 1 || collector.SensitiveValueDetected() {
		t.Fatalf("collector 状态异常：count=%d sensitive=%v", collector.ExportCount(), collector.SensitiveValueDetected())
	}

	_, err = client.Export(context.Background(), &collectortrace.ExportTraceServiceRequest{
		ResourceSpans: []*trace.ResourceSpans{{
			Resource: &resource.Resource{
				Attributes: []*common.KeyValue{{
					Key: "unsafe.attribute",
					Value: &common.AnyValue{
						Value: &common.AnyValue_StringValue{StringValue: secret},
					},
				}},
			},
		}},
	})
	if err != nil {
		t.Fatalf("second Export() error = %v", err)
	}
	if !collector.SensitiveValueDetected() {
		t.Fatal("collector 必须识别 synthetic secret")
	}
}

func TestCollectorRejectsInvalidWaitAndNilAccessorsAreSafe(t *testing.T) {
	var collector *Collector
	if collector.Endpoint() != "" || collector.ExportCount() != 0 || collector.SensitiveValueDetected() {
		t.Fatal("nil collector accessor 必须安全")
	}
	if err := collector.Close(); err != nil {
		t.Fatalf("nil Close() error = %v", err)
	}

	instance, err := Start(nil)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	if err := instance.WaitForExports(context.Background(), 0); err == nil {
		t.Fatal("minimum=0 必须被拒绝")
	}
}
