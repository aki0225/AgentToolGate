package model

import "encoding/json"

// BuiltinToolInputs 返回每个 workspace 都应自动具备的最小演示工具。
// 这里集中定义是为了让 MemoryStore、PostgresStore 和新建 workspace 流程保持一致。
func BuiltinToolInputs(workspaceID string) []CreateToolInput {
	return []CreateToolInput{
		{
			WorkspaceID:      workspaceID,
			Namespace:        "agent_guard",
			Name:             "evaluate",
			DisplayName:      "Agent Guard Evaluate",
			Description:      "Internal local action firewall audit entrypoint.",
			OperationType:    "evaluate",
			RiskLevel:        "low",
			RequiresApproval: false,
			InputSchemaJSON:  json.RawMessage(`{"type":"object"}`),
			OutputSchemaJSON: json.RawMessage(`{"type":"object"}`),
			Enabled:          true,
		},
		{
			WorkspaceID:      workspaceID,
			Namespace:        "mock",
			Name:             "echo",
			DisplayName:      "Mock Echo",
			Description:      "Echoes arguments back for demo flows.",
			OperationType:    "mock",
			RiskLevel:        "low",
			RequiresApproval: false,
			InputSchemaJSON:  json.RawMessage(`{"type":"object"}`),
			OutputSchemaJSON: json.RawMessage(`{"type":"object"}`),
			Enabled:          true,
		},
		{
			WorkspaceID:      workspaceID,
			Namespace:        "database",
			Name:             "query",
			DisplayName:      "Database Query",
			Description:      "Executes a guarded read-only PostgreSQL SELECT query against the demo datasource.",
			OperationType:    "read",
			RiskLevel:        "medium",
			RequiresApproval: false,
			InputSchemaJSON: json.RawMessage(`{
				"type":"object",
				"properties":{
					"datasource":{"type":"string","default":"local_postgres"},
					"sql":{"type":"string"}
				},
				"required":["datasource","sql"]
			}`),
			OutputSchemaJSON: json.RawMessage(`{
				"type":"object",
				"properties":{
					"datasource":{"type":"string"},
					"sql":{"type":"string"},
					"tables":{"type":"array","items":{"type":"string"}},
					"maxRows":{"type":"integer"},
					"rowCount":{"type":"integer"},
					"rows":{"type":"array","items":{"type":"object"}}
				}
			}`),
			Enabled: true,
		},
		{
			WorkspaceID:      workspaceID,
			Namespace:        "github",
			Name:             "list_repos",
			DisplayName:      "GitHub List Repositories",
			Description:      "Lists repositories allowed by the GitHub connector repo whitelist.",
			OperationType:    "read",
			RiskLevel:        "low",
			RequiresApproval: false,
			InputSchemaJSON:  json.RawMessage(`{"type":"object","properties":{}}`),
			OutputSchemaJSON: json.RawMessage(`{
				"type":"object",
				"properties":{
					"repositories":{"type":"array","items":{"type":"object"}}
				}
			}`),
			Enabled: true,
		},
		{
			WorkspaceID:      workspaceID,
			Namespace:        "github",
			Name:             "get_pull_request",
			DisplayName:      "GitHub Get Pull Request",
			Description:      "Reads a pull request from an allowed GitHub repository.",
			OperationType:    "read",
			RiskLevel:        "medium",
			RequiresApproval: false,
			InputSchemaJSON: json.RawMessage(`{
				"type":"object",
				"properties":{
					"owner":{"type":"string"},
					"repo":{"type":"string"},
					"pullNumber":{"type":"integer","minimum":1}
				},
				"required":["owner","repo","pullNumber"]
			}`),
			OutputSchemaJSON: json.RawMessage(`{
				"type":"object",
				"properties":{
					"owner":{"type":"string"},
					"repo":{"type":"string"},
					"number":{"type":"integer"},
					"title":{"type":"string"},
					"state":{"type":"string"},
					"htmlUrl":{"type":"string"},
					"userLogin":{"type":"string"},
					"headRef":{"type":"string"},
					"baseRef":{"type":"string"}
				}
			}`),
			Enabled: true,
		},
		{
			WorkspaceID:      workspaceID,
			Namespace:        "github",
			Name:             "create_issue",
			DisplayName:      "GitHub Create Issue",
			Description:      "Creates an issue in an allowed GitHub repository after approval.",
			OperationType:    "create",
			RiskLevel:        "medium",
			RequiresApproval: true,
			InputSchemaJSON: json.RawMessage(`{
				"type":"object",
				"properties":{
					"owner":{"type":"string"},
					"repo":{"type":"string"},
					"title":{"type":"string"},
					"body":{"type":"string"}
				},
				"required":["owner","repo","title"]
			}`),
			OutputSchemaJSON: json.RawMessage(`{
				"type":"object",
				"properties":{
					"owner":{"type":"string"},
					"repo":{"type":"string"},
					"number":{"type":"integer"},
					"title":{"type":"string"},
					"htmlUrl":{"type":"string"},
					"state":{"type":"string"}
				}
			}`),
			Enabled: true,
		},
		{
			WorkspaceID:      workspaceID,
			Namespace:        "http",
			Name:             "request",
			DisplayName:      "HTTP Request",
			Description:      "Calls an allowlisted internal HTTP endpoint through the governed runtime.",
			OperationType:    "read",
			RiskLevel:        "medium",
			RequiresApproval: false,
			InputSchemaJSON: json.RawMessage(`{
				"type":"object",
				"properties":{
					"method":{"type":"string","default":"GET"},
					"url":{"type":"string"},
					"headers":{"type":"object"},
					"body":{}
				},
				"required":["method","url"]
			}`),
			OutputSchemaJSON: json.RawMessage(`{
				"type":"object",
				"properties":{
					"method":{"type":"string"},
					"url":{"type":"string"},
					"statusCode":{"type":"integer"},
					"headers":{"type":"object"},
					"body":{},
					"bodyTruncated":{"type":"boolean"}
				}
			}`),
			Enabled: true,
		},
	}
}
