package report

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"agenttoolgate/evaluation/internal/model"
	evalrunner "agenttoolgate/evaluation/internal/runner"
)

type junitSuites struct {
	XMLName   xml.Name     `xml:"testsuites"`
	Name      string       `xml:"name,attr"`
	Tests     int          `xml:"tests,attr"`
	Failures  int          `xml:"failures,attr"`
	Skipped   int          `xml:"skipped,attr"`
	Time      string       `xml:"time,attr"`
	Timestamp string       `xml:"timestamp,attr"`
	Suites    []junitSuite `xml:"testsuite"`
}

type junitSuite struct {
	Name      string          `xml:"name,attr"`
	Tests     int             `xml:"tests,attr"`
	Failures  int             `xml:"failures,attr"`
	Skipped   int             `xml:"skipped,attr"`
	Time      string          `xml:"time,attr"`
	Timestamp string          `xml:"timestamp,attr"`
	Cases     []junitTestCase `xml:"testcase"`
}

type junitTestCase struct {
	Name       string          `xml:"name,attr"`
	Classname  string          `xml:"classname,attr"`
	Time       string          `xml:"time,attr"`
	Properties junitProperties `xml:"properties"`
	Failure    *junitMessage   `xml:"failure,omitempty"`
	Skipped    *junitMessage   `xml:"skipped,omitempty"`
}

type junitProperties struct {
	Items []junitProperty `xml:"property"`
}

type junitProperty struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value,attr"`
}

type junitMessage struct {
	Message string `xml:"message,attr"`
	Type    string `xml:"type,attr,omitempty"`
	Body    string `xml:",chardata"`
}

func renderJUnit(document evalrunner.Document) ([]byte, error) {
	report := buildJUnit(document)
	var buffer bytes.Buffer
	buffer.WriteString(xml.Header)
	encoder := xml.NewEncoder(&buffer)
	encoder.Indent("", "  ")
	if err := encoder.Encode(report); err != nil {
		return nil, fmt.Errorf("生成 JUnit 失败：%w", err)
	}
	if err := encoder.Close(); err != nil {
		return nil, fmt.Errorf("结束 JUnit 编码失败：%w", err)
	}
	buffer.WriteByte('\n')

	var decoded junitSuites
	decoder := xml.NewDecoder(bytes.NewReader(buffer.Bytes()))
	if err := decoder.Decode(&decoded); err != nil {
		return nil, fmt.Errorf("复核 JUnit 失败：%w", err)
	}
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("复核 JUnit 尾部失败：%w", err)
		}
		if text, ok := token.(xml.CharData); !ok || strings.TrimSpace(string(text)) != "" {
			return nil, fmt.Errorf("JUnit 包含根元素之外的内容")
		}
	}
	if !reflect.DeepEqual(decoded, report) {
		return nil, fmt.Errorf("JUnit 编解码语义不一致")
	}
	return buffer.Bytes(), nil
}

func buildJUnit(document evalrunner.Document) junitSuites {
	resultsBySuite := make(map[string][]model.Result)
	for _, result := range document.Results {
		resultsBySuite[result.Suite] = append(resultsBySuite[result.Suite], result)
	}
	suiteNames := make([]string, 0, len(resultsBySuite))
	for suite := range resultsBySuite {
		suiteNames = append(suiteNames, suite)
	}
	sort.Strings(suiteNames)

	report := junitSuites{
		XMLName:   xml.Name{Local: "testsuites"},
		Name:      "AgentToolGate Agent Safety Evaluation",
		Tests:     document.Metrics.CaseCount,
		Failures:  document.Metrics.FailedCount,
		Skipped:   document.Metrics.SkippedCount,
		Timestamp: document.StartedAt,
		Suites:    make([]junitSuite, 0, len(suiteNames)),
	}
	var totalDurationMS float64
	for _, suiteName := range suiteNames {
		results := resultsBySuite[suiteName]
		suite := junitSuite{
			Name:      suiteName,
			Tests:     len(results),
			Timestamp: document.StartedAt,
			Cases:     make([]junitTestCase, 0, len(results)),
		}
		var suiteDurationMS float64
		for _, result := range results {
			suite.Cases = append(suite.Cases, junitCase(result))
			suiteDurationMS += result.DurationMS
			switch result.Status {
			case model.ResultFailed:
				suite.Failures++
			case model.ResultSkipped:
				suite.Skipped++
			}
		}
		suite.Time = durationSeconds(suiteDurationMS)
		totalDurationMS += suiteDurationMS
		report.Suites = append(report.Suites, suite)
	}
	report.Time = durationSeconds(totalDurationMS)
	return report
}

func junitCase(result model.Result) junitTestCase {
	view := caseResultView(result)
	properties := []junitProperty{
		{Name: "status", Value: view.Status},
		{Name: "category", Value: view.Category},
		{Name: "platform", Value: string(result.Platform)},
		{Name: "entry", Value: view.Entry},
		{Name: "expected_decision", Value: view.ExpectedDecision},
		{Name: "actual_decision", Value: view.ActualDecision},
		{Name: "risk_level", Value: view.RiskLevel},
		{Name: "signals", Value: view.Signals},
		{Name: "side_effect", Value: view.SideEffect},
		{Name: "upstream_calls_before_approval", Value: strconv.Itoa(view.UpstreamCalls)},
	}
	if view.EvidencePath != "" {
		properties = append(properties,
			junitProperty{Name: "evidence_path", Value: view.EvidencePath},
			junitProperty{Name: "evidence_sha256", Value: view.EvidenceSHA256},
		)
	}
	testCase := junitTestCase{
		Name:      result.CaseID,
		Classname: result.Suite,
		Time:      durationSeconds(result.DurationMS),
		Properties: junitProperties{
			Items: properties,
		},
	}
	switch result.Status {
	case model.ResultFailed:
		testCase.Failure = &junitMessage{
			Message: result.FailureReason,
			Type:    "agenttoolgate.evaluation",
			Body:    result.FailureReason,
		}
	case model.ResultSkipped:
		testCase.Skipped = &junitMessage{
			Message: result.SkipReason,
			Body:    result.SkipReason,
		}
	}
	return testCase
}

func durationSeconds(durationMS float64) string {
	return strings.TrimRight(strings.TrimRight(strconv.FormatFloat(durationMS/1000, 'f', 6, 64), "0"), ".")
}
