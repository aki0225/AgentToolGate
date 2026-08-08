package report

import (
	"bytes"
	"encoding/xml"
	"strings"
	"testing"

	"agenttoolgate/evaluation/internal/metrics"
	"agenttoolgate/evaluation/internal/model"
)

func TestRenderReportsEscapesContentAndPreservesDecisions(t *testing.T) {
	_, document := reportFixture()
	document.Results[0].ExpectedDecision = []model.Decision{model.DecisionAsk}
	document.Results[0].ActualDecision = model.DecisionAsk
	document.Results[1].ActualDecision = model.DecisionDenyWithTicket
	payload := `<script>alert("proof")</script>|[unsafe] https://example.invalid/reference`
	document.Results[1].Signals = []string{payload}
	document.Results[1].FailureReason = payload
	document.Metrics = metrics.Aggregate(document.Results)

	artifacts, err := renderReports(document)
	if err != nil {
		t.Fatalf("renderReports() error = %v", err)
	}
	if len(artifacts) != 3 {
		t.Fatalf("artifacts=%d", len(artifacts))
	}
	byPath := make(map[string][]byte, len(artifacts))
	for _, artifact := range artifacts {
		byPath[artifact.path] = artifact.bytes
	}

	junitRaw := byPath[JUnitFileName]
	var junit junitSuites
	if err := xml.Unmarshal(junitRaw, &junit); err != nil {
		t.Fatalf("JUnit XML 无效：%v", err)
	}
	if junit.Tests != 3 || junit.Failures != 1 || junit.Skipped != 1 {
		t.Fatalf("JUnit 汇总异常：%+v", junit)
	}
	if !bytes.Contains(junitRaw, []byte("ask")) || !bytes.Contains(junitRaw, []byte("deny_with_ticket")) {
		t.Fatalf("JUnit 丢失决策语义：%s", junitRaw)
	}
	if bytes.Contains(junitRaw, []byte("<script>")) || !bytes.Contains(junitRaw, []byte("&lt;script&gt;")) {
		t.Fatalf("JUnit 转义异常：%s", junitRaw)
	}

	markdown := string(byPath[SummaryFileName])
	if strings.Contains(markdown, "<script>") ||
		!strings.Contains(markdown, "&lt;script&gt;") ||
		!strings.Contains(markdown, `\|`) ||
		!strings.Contains(markdown, "deny_with_ticket") {
		t.Fatalf("Markdown 转义或决策语义异常：%s", markdown)
	}

	html := string(byPath[HTMLFileName])
	if strings.Contains(html, "<script>") ||
		!strings.Contains(html, "&lt;script&gt;") ||
		!strings.Contains(html, "deny_with_ticket") ||
		externalHTMLResourcePattern.MatchString(html) {
		t.Fatalf("HTML 转义、决策语义或离线约束异常：%s", html)
	}
}

func TestValidateReportArtifactSizesRejectsEmptyAndOversizedReports(t *testing.T) {
	for _, reports := range [][]fileArtifact{
		{{path: JUnitFileName}},
		{{path: HTMLFileName, bytes: make([]byte, maxResultsBytes+1)}},
	} {
		if err := validateReportArtifactSizes(reports); err == nil {
			t.Fatalf("非法报告大小必须被拒绝：%d", len(reports[0].bytes))
		}
	}
}
