package report

import (
	"fmt"

	evalrunner "agenttoolgate/evaluation/internal/runner"
)

func renderReports(document evalrunner.Document) ([]fileArtifact, error) {
	junit, err := renderJUnit(document)
	if err != nil {
		return nil, err
	}
	markdown, err := renderMarkdown(document)
	if err != nil {
		return nil, err
	}
	html, err := renderHTML(document)
	if err != nil {
		return nil, err
	}
	artifacts := []fileArtifact{
		{path: JUnitFileName, mediaType: mediaTypeXML, bytes: junit},
		{path: SummaryFileName, mediaType: mediaTypeMarkdown, bytes: markdown},
		{path: HTMLFileName, mediaType: mediaTypeHTML, bytes: html},
	}
	seen := make(map[string]struct{}, len(artifacts))
	for _, artifact := range artifacts {
		wantMediaType, err := manifestMediaType(artifact.path)
		if err != nil || wantMediaType != artifact.mediaType || len(artifact.bytes) == 0 {
			return nil, fmt.Errorf("报告产物无效：%s", artifact.path)
		}
		if _, exists := seen[artifact.path]; exists {
			return nil, fmt.Errorf("报告产物路径重复：%s", artifact.path)
		}
		seen[artifact.path] = struct{}{}
	}
	return artifacts, nil
}
