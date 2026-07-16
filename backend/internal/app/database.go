package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"agenttoolgate/backend/internal/model"
	"agenttoolgate/backend/internal/telemetry"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/attribute"
)

const (
	defaultDatabaseQueryDatasource = "local_postgres"
	defaultDatabaseQueryMaxRows    = 100
	hardDatabaseQueryMaxRows       = 1000
	defaultDatabaseQueryTimeout    = 3 * time.Second
	hardDatabaseQueryTimeout       = 30 * time.Second
)

var (
	bannedDatabaseQueryKeyword = regexp.MustCompile(`(?i)\b(insert|update|delete|drop|alter|create|truncate|merge|copy|grant|revoke|vacuum|call|do|execute|into)\b`)
	bannedDatabaseQueryFunc    = regexp.MustCompile(`(?i)\b(setval|nextval|set_config|pg_notify|pg_sleep|pg_advisory_lock|pg_advisory_xact_lock|pg_advisory_lock_shared|pg_advisory_xact_lock_shared|pg_advisory_unlock|pg_advisory_unlock_all|pg_advisory_unlock_shared|lo_import|lo_export|lo_create|lo_unlink|pg_read_file|pg_read_binary_file|pg_ls_dir|pg_stat_file|pg_cancel_backend|pg_terminate_backend|pg_reload_conf|pg_rotate_logfile)\s*\(`)
	databaseRowLockClause      = regexp.MustCompile(`(?i)\bfor\s+(update|no\s+key\s+update|share|key\s+share)\b`)
	dollarQuotePattern         = regexp.MustCompile(`\$[A-Za-z_][A-Za-z0-9_]*\$|\$\$`)
	databaseTableNamePattern   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)?$`)
	databaseSimpleProjection   = regexp.MustCompile(`(?i)^\s*(?:[A-Za-z_][A-Za-z0-9_]*\.)?([A-Za-z_][A-Za-z0-9_]*)(?:\s+(?:AS\s+)?([A-Za-z_][A-Za-z0-9_]*))?\s*$`)
)

type databaseQueryArgs struct {
	Datasource string `json:"datasource"`
	SQL        string `json:"sql"`
}

type guardedDatabaseSQL struct {
	OriginalSQL string
	ExecutedSQL string
	MaxRows     int
	Tables      []string
}

type databaseAllowedTable struct {
	Schema string
	Name   string
}

type databaseAllowedTables struct {
	byKey   map[string]databaseAllowedTable
	ordered []databaseAllowedTable
}

type databaseSchemaResponse struct {
	Datasource string                `json:"datasource"`
	Tables     []databaseTableSchema `json:"tables"`
	Message    string                `json:"message,omitempty"`
}

type databaseTableSchema struct {
	Schema  string                 `json:"schema"`
	Table   string                 `json:"table"`
	Columns []databaseColumnSchema `json:"columns"`
}

type databaseColumnSchema struct {
	Name     string `json:"name"`
	DataType string `json:"dataType"`
	Masked   bool   `json:"masked"`
}

func (a *App) executeDatabaseQuery(ctx context.Context, tool model.Tool, workspaceID string, decodedArgs any) (resultPayload map[string]any, resultJSON json.RawMessage, err error) {
	ctx, span := telemetry.StartSpan(ctx, "connector.database.query", attribute.String("tool.key", tool.Key()))
	defer func() {
		if err != nil {
			telemetry.RecordError(span, err)
		}
		span.End()
	}()

	_ = tool
	_ = workspaceID

	args, err := parseDatabaseQueryArgs(decodedArgs)
	if err != nil {
		return nil, nil, err
	}

	datasource := strings.TrimSpace(a.cfg.DatabaseQueryDatasource)
	if datasource == "" {
		datasource = defaultDatabaseQueryDatasource
	}
	if strings.TrimSpace(args.Datasource) == "" {
		args.Datasource = datasource
	}
	span.SetAttributes(attribute.String("db.datasource", args.Datasource))
	if args.Datasource != datasource {
		return nil, nil, badRequest(fmt.Sprintf("unsupported datasource %q", args.Datasource))
	}

	allowedTables, err := parseDatabaseAllowedTables(a.cfg.DatabaseQueryAllowedTables)
	if err != nil {
		return nil, nil, err
	}

	guarded, err := guardDatabaseSQL(args.SQL, effectiveDatabaseQueryMaxRows(a.cfg.DatabaseQueryMaxRows), allowedTables)
	if err != nil {
		return nil, nil, err
	}
	span.SetAttributes(attribute.String("db.sql", redactDatabaseSQLLiterals(guarded.ExecutedSQL)))

	dsn := effectiveDatabaseQueryDSN(a.cfg.DatabaseQueryURL, a.cfg.DatabaseURL)
	if dsn == "" {
		return nil, nil, errors.New("database query datasource is not configured")
	}

	timeout := effectiveDatabaseQueryTimeout(time.Duration(a.cfg.DatabaseQueryTimeoutMs) * time.Millisecond)
	queryCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	pool, err := openDatabaseQueryPool(queryCtx, dsn)
	if err != nil {
		return nil, nil, err
	}
	defer pool.Close()

	rows, err := pool.Query(queryCtx, guarded.ExecutedSQL)
	if err != nil {
		return nil, nil, databaseQueryErr(queryCtx, timeout, err)
	}
	defer rows.Close()

	resultRows, err := collectDatabaseRows(rows)
	if err != nil {
		return nil, nil, databaseQueryErr(queryCtx, timeout, err)
	}
	span.SetAttributes(attribute.Int("db.row_count", len(resultRows)))

	resultPayload = map[string]any{
		"datasource": args.Datasource,
		"sql":        redactDatabaseSQLLiterals(guarded.ExecutedSQL),
		"maxRows":    guarded.MaxRows,
		"rowCount":   len(resultRows),
		"rows":       resultRows,
		"tables":     guarded.Tables,
	}
	resultJSON, err = json.Marshal(resultPayload)
	if err != nil {
		return nil, nil, err
	}
	return resultPayload, resultJSON, nil
}

func (a *App) handleDatabaseSchema(w http.ResponseWriter, r *http.Request) {
	reqCtx, ok := requestContextFrom(r.Context())
	if !ok {
		a.respondError(w, errors.New("missing request context"))
		return
	}
	if err := requireViewDatabaseSchema(reqCtx); err != nil {
		a.respondError(w, err)
		return
	}

	datasource := strings.TrimSpace(a.cfg.DatabaseQueryDatasource)
	if datasource == "" {
		datasource = defaultDatabaseQueryDatasource
	}
	requestedDatasource := strings.TrimSpace(r.URL.Query().Get("datasource"))
	if requestedDatasource == "" {
		requestedDatasource = datasource
	}
	if requestedDatasource != datasource {
		a.respondError(w, badRequest(fmt.Sprintf("unsupported datasource %q", requestedDatasource)))
		return
	}

	allowedTables, err := parseDatabaseAllowedTables(a.cfg.DatabaseQueryAllowedTables)
	if err != nil {
		a.respondError(w, err)
		return
	}
	if allowedTables.empty() {
		writeJSON(w, http.StatusOK, databaseSchemaResponse{
			Datasource: requestedDatasource,
			Tables:     []databaseTableSchema{},
			Message:    "DATABASE_QUERY_ALLOWED_TABLES is not configured.",
		})
		return
	}

	dsn := effectiveDatabaseQueryDSN(a.cfg.DatabaseQueryURL, a.cfg.DatabaseURL)
	if dsn == "" {
		a.respondError(w, errors.New("database query datasource is not configured"))
		return
	}

	timeout := effectiveDatabaseQueryTimeout(time.Duration(a.cfg.DatabaseQueryTimeoutMs) * time.Millisecond)
	queryCtx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()

	pool, err := openDatabaseQueryPool(queryCtx, dsn)
	if err != nil {
		a.respondError(w, err)
		return
	}
	defer pool.Close()

	schema, err := introspectDatabaseSchema(queryCtx, pool, requestedDatasource, allowedTables)
	if err != nil {
		a.respondError(w, databaseQueryErr(queryCtx, timeout, err))
		return
	}
	writeJSON(w, http.StatusOK, schema)
}

func openDatabaseQueryPool(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	poolConfig, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse database query datasource: %w", err)
	}
	poolConfig.MaxConns = 1
	poolConfig.MinConns = 0

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("connect database query datasource: %w", err)
	}
	return pool, nil
}

func effectiveDatabaseQueryDSN(databaseQueryURL, databaseURL string) string {
	// DATABASE_QUERY_URL 是专用只读数据源；未配置时保留 Week 3/4 demo 对 DATABASE_URL 的兼容回退。
	if dsn := strings.TrimSpace(databaseQueryURL); dsn != "" {
		return dsn
	}
	return strings.TrimSpace(databaseURL)
}

func introspectDatabaseSchema(ctx context.Context, pool *pgxpool.Pool, datasource string, allowedTables databaseAllowedTables) (databaseSchemaResponse, error) {
	conditions := make([]string, 0, len(allowedTables.ordered))
	args := make([]any, 0, len(allowedTables.ordered)*2)
	for _, table := range allowedTables.ordered {
		conditions = append(conditions, fmt.Sprintf("(table_schema = $%d AND table_name = $%d)", len(args)+1, len(args)+2))
		args = append(args, table.Schema, table.Name)
	}

	query := fmt.Sprintf(`
		SELECT table_schema, table_name, column_name, data_type
		FROM information_schema.columns
		WHERE %s
		ORDER BY table_schema, table_name, ordinal_position
	`, strings.Join(conditions, " OR "))

	rows, err := pool.Query(ctx, query, args...)
	if err != nil {
		return databaseSchemaResponse{}, err
	}
	defer rows.Close()

	byTable := map[string]*databaseTableSchema{}
	for rows.Next() {
		var schemaName, tableName, columnName, dataType string
		if err := rows.Scan(&schemaName, &tableName, &columnName, &dataType); err != nil {
			return databaseSchemaResponse{}, err
		}
		key := strings.ToLower(schemaName + "." + tableName)
		table, exists := byTable[key]
		if !exists {
			table = &databaseTableSchema{
				Schema:  schemaName,
				Table:   tableName,
				Columns: []databaseColumnSchema{},
			}
			byTable[key] = table
		}
		table.Columns = append(table.Columns, databaseColumnSchema{
			Name:     columnName,
			DataType: dataType,
			Masked:   isSensitiveDatabaseColumn(columnName),
		})
	}
	if err := rows.Err(); err != nil {
		return databaseSchemaResponse{}, err
	}

	tables := make([]databaseTableSchema, 0, len(byTable))
	for _, key := range allowedTables.keys() {
		if table, exists := byTable[key]; exists {
			tables = append(tables, *table)
		}
	}
	return databaseSchemaResponse{
		Datasource: datasource,
		Tables:     tables,
	}, nil
}

func parseDatabaseQueryArgs(decodedArgs any) (databaseQueryArgs, error) {
	raw, err := json.Marshal(decodedArgs)
	if err != nil {
		return databaseQueryArgs{}, badRequest("database.query arguments must be a JSON object")
	}
	var args databaseQueryArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return databaseQueryArgs{}, badRequest("database.query arguments must include datasource and sql")
	}
	args.Datasource = strings.TrimSpace(args.Datasource)
	args.SQL = strings.TrimSpace(args.SQL)
	if args.SQL == "" {
		return databaseQueryArgs{}, badRequest("sql is required")
	}
	return args, nil
}

func redactDatabaseQueryInputForAudit(value json.RawMessage) json.RawMessage {
	redacted := redactJSONByKey(value)
	var obj map[string]any
	if err := json.Unmarshal(redacted, &obj); err != nil {
		return redacted
	}
	if sqlText, ok := obj["sql"].(string); ok {
		obj["sql"] = redactDatabaseSQLLiterals(sqlText)
	}
	raw, err := json.Marshal(obj)
	if err != nil {
		return redacted
	}
	return raw
}

func redactDatabaseSQLLiterals(sql string) string {
	trimmed := strings.TrimSpace(sql)
	if trimmed == "" {
		return ""
	}
	var builder strings.Builder
	builder.Grow(len(trimmed))
	inSingleQuote := false
	inDoubleQuote := false

	for index := 0; index < len(trimmed); index++ {
		ch := trimmed[index]
		switch {
		case inSingleQuote:
			if ch == '\'' {
				if index+1 < len(trimmed) && trimmed[index+1] == '\'' {
					index++
					continue
				}
				inSingleQuote = false
			}
			continue
		case inDoubleQuote:
			builder.WriteByte(ch)
			if ch == '"' {
				if index+1 < len(trimmed) && trimmed[index+1] == '"' {
					builder.WriteByte(trimmed[index+1])
					index++
					continue
				}
				inDoubleQuote = false
			}
			continue
		}

		switch {
		case databaseDollarQuoteDelimiterAt(trimmed, index) != "":
			delimiter := databaseDollarQuoteDelimiterAt(trimmed, index)
			builder.WriteByte('?')
			contentStart := index + len(delimiter)
			closeOffset := strings.Index(trimmed[contentStart:], delimiter)
			if closeOffset < 0 {
				index = len(trimmed) - 1
				continue
			}
			index = contentStart + closeOffset + len(delimiter) - 1
		case ch == '\'':
			builder.WriteByte('?')
			inSingleQuote = true
		case ch == '"':
			builder.WriteByte(ch)
			inDoubleQuote = true
		case isDatabaseNumericLiteralStart(trimmed, index):
			builder.WriteByte('?')
			index = consumeDatabaseNumericLiteral(trimmed, index) - 1
		default:
			builder.WriteByte(ch)
		}
	}
	return strings.Join(strings.Fields(builder.String()), " ")
}

func databaseDollarQuoteDelimiterAt(sql string, index int) string {
	if index >= len(sql) || sql[index] != '$' {
		return ""
	}
	if index+1 < len(sql) && sql[index+1] == '$' {
		return "$$"
	}
	end := index + 1
	if end >= len(sql) || !isDatabaseDollarQuoteTagStart(sql[end]) {
		return ""
	}
	for end < len(sql) && isDatabaseDollarQuoteTagByte(sql[end]) {
		end++
	}
	if end < len(sql) && sql[end] == '$' {
		return sql[index : end+1]
	}
	return ""
}

func isDatabaseDollarQuoteTagStart(ch byte) bool {
	return ch == '_' || ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z'
}

func isDatabaseDollarQuoteTagByte(ch byte) bool {
	return isDatabaseDollarQuoteTagStart(ch) || ch >= '0' && ch <= '9'
}

func isDatabaseNumericLiteralStart(sql string, index int) bool {
	ch := sql[index]
	if ch == '-' {
		if index+1 >= len(sql) || sql[index+1] < '0' || sql[index+1] > '9' {
			return false
		}
	} else if ch < '0' || ch > '9' {
		return false
	}
	if index > 0 {
		prev := rune(sql[index-1])
		if isDatabaseIdentifierChar(prev) || prev == '.' {
			return false
		}
	}
	return true
}

func consumeDatabaseNumericLiteral(sql string, index int) int {
	if sql[index] == '-' {
		index++
	}
	for index < len(sql) && sql[index] >= '0' && sql[index] <= '9' {
		index++
	}
	if index+1 < len(sql) && sql[index] == '.' && sql[index+1] >= '0' && sql[index+1] <= '9' {
		index++
		for index < len(sql) && sql[index] >= '0' && sql[index] <= '9' {
			index++
		}
	}
	if index+1 < len(sql) && (sql[index] == 'e' || sql[index] == 'E') {
		next := index + 1
		if sql[next] == '+' || sql[next] == '-' {
			next++
		}
		if next < len(sql) && sql[next] >= '0' && sql[next] <= '9' {
			index = next + 1
			for index < len(sql) && sql[index] >= '0' && sql[index] <= '9' {
				index++
			}
		}
	}
	return index
}

func parseDatabaseAllowedTables(rawTables []string) (databaseAllowedTables, error) {
	result := databaseAllowedTables{
		byKey:   map[string]databaseAllowedTable{},
		ordered: []databaseAllowedTable{},
	}
	for _, raw := range rawTables {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		table, err := normalizeDatabaseTableName(raw)
		if err != nil {
			return databaseAllowedTables{}, err
		}
		key := table.Schema + "." + table.Name
		if _, exists := result.byKey[key]; exists {
			continue
		}
		result.byKey[key] = table
		result.ordered = append(result.ordered, table)
	}
	sort.Slice(result.ordered, func(i, j int) bool {
		left := result.ordered[i].Schema + "." + result.ordered[i].Name
		right := result.ordered[j].Schema + "." + result.ordered[j].Name
		return left < right
	})
	return result, nil
}

func normalizeDatabaseTableName(raw string) (databaseAllowedTable, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return databaseAllowedTable{}, nil
	}
	if !databaseTableNamePattern.MatchString(trimmed) {
		return databaseAllowedTable{}, badRequest(fmt.Sprintf("invalid allowed table %q", raw))
	}
	parts := strings.Split(strings.ToLower(trimmed), ".")
	if len(parts) == 1 {
		return databaseAllowedTable{Schema: "public", Name: parts[0]}, nil
	}
	return databaseAllowedTable{Schema: parts[0], Name: parts[1]}, nil
}

func (t databaseAllowedTables) empty() bool {
	return len(t.byKey) == 0
}

func (t databaseAllowedTables) contains(table string) bool {
	if t.empty() {
		return false
	}
	_, ok := t.byKey[strings.ToLower(strings.TrimSpace(table))]
	return ok
}

func (t databaseAllowedTables) keys() []string {
	keys := make([]string, 0, len(t.ordered))
	for _, table := range t.ordered {
		keys = append(keys, table.Schema+"."+table.Name)
	}
	return keys
}

func guardDatabaseSQL(sql string, maxRows int, allowedTables databaseAllowedTables) (guardedDatabaseSQL, error) {
	original := strings.TrimSpace(sql)
	if original == "" {
		return guardedDatabaseSQL{}, badRequest("sql is required")
	}
	if allowedTables.empty() {
		return guardedDatabaseSQL{}, badRequest("database query allowed table whitelist is not configured")
	}

	statement, err := singleStatementSQL(original)
	if err != nil {
		return guardedDatabaseSQL{}, err
	}

	masked, err := maskDatabaseSQLForGuard(statement)
	if err != nil {
		return guardedDatabaseSQL{}, err
	}
	normalized := strings.ToLower(strings.Join(strings.Fields(masked), " "))
	fields := strings.Fields(normalized)
	if len(fields) == 0 || fields[0] != "select" {
		return guardedDatabaseSQL{}, badRequest("only SELECT queries are allowed")
	}
	if dollarQuotePattern.MatchString(masked) {
		return guardedDatabaseSQL{}, badRequest("dollar-quoted SQL blocks are not allowed")
	}
	if bannedDatabaseQueryKeyword.MatchString(masked) {
		return guardedDatabaseSQL{}, badRequest("write or DDL keywords are not allowed")
	}
	if bannedDatabaseQueryFunc.MatchString(masked) {
		return guardedDatabaseSQL{}, badRequest("side-effect functions are not allowed")
	}
	if databaseRowLockClause.MatchString(masked) {
		return guardedDatabaseSQL{}, badRequest("row locking clauses are not allowed")
	}
	if err := rejectSensitiveDatabaseProjectionAliases(masked); err != nil {
		return guardedDatabaseSQL{}, err
	}
	tables, err := extractDatabaseTables(masked)
	if err != nil {
		return guardedDatabaseSQL{}, err
	}
	for _, table := range tables {
		if !allowedTables.contains(table) {
			return guardedDatabaseSQL{}, badRequest(fmt.Sprintf("table %s is not allowed", table))
		}
	}

	effectiveMaxRows := effectiveDatabaseQueryMaxRows(maxRows)
	// 这里唯一允许拼接 SQL 的地方：原始 SQL 已通过保守 guard，LIMIT 是受控整数。
	executedSQL := fmt.Sprintf("SELECT * FROM (%s) AS agt_query LIMIT %d", statement, effectiveMaxRows)
	return guardedDatabaseSQL{
		OriginalSQL: statement,
		ExecutedSQL: executedSQL,
		MaxRows:     effectiveMaxRows,
		Tables:      tables,
	}, nil
}

func rejectSensitiveDatabaseProjectionAliases(maskedSQL string) error {
	for _, segment := range databaseSelectListSegments(maskedSQL) {
		if !databaseSegmentHasSensitiveIdentifier(segment) {
			continue
		}
		match := databaseSimpleProjection.FindStringSubmatch(segment)
		if len(match) == 0 {
			return badRequest("sensitive columns must be selected directly or aliased to a sensitive field name")
		}
		columnName := match[1]
		alias := strings.TrimSpace(match[2])
		if !isSensitiveDatabaseColumn(columnName) {
			return badRequest("sensitive columns must be selected directly or aliased to a sensitive field name")
		}
		if alias != "" && !isSensitiveDatabaseColumn(alias) {
			return badRequest("sensitive columns must be selected directly or aliased to a sensitive field name")
		}
	}
	return nil
}

func databaseSegmentHasSensitiveIdentifier(segment string) bool {
	for _, token := range tokenizeDatabaseSQL(segment) {
		if token == "," || token == "(" || token == ")" {
			continue
		}
		if isSensitiveDatabaseColumn(token) {
			return true
		}
	}
	return false
}

func databaseSelectListSegments(maskedSQL string) []string {
	selectStart := databaseSelectKeywordEnd(maskedSQL)
	if selectStart < 0 {
		return []string{}
	}
	selectList := maskedSQL[selectStart:]
	if fromIndex := topLevelDatabaseKeywordIndex(selectList, "from"); fromIndex >= 0 {
		selectList = selectList[:fromIndex]
	}

	segments := make([]string, 0)
	start := 0
	depth := 0
	for index, ch := range selectList {
		switch ch {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				segments = append(segments, strings.TrimSpace(selectList[start:index]))
				start = index + 1
			}
		}
	}
	segments = append(segments, strings.TrimSpace(selectList[start:]))
	return segments
}

func databaseSelectKeywordEnd(sql string) int {
	trimmedLeft := strings.TrimLeft(sql, " \t\r\n")
	if len(trimmedLeft) < len("select") {
		return -1
	}
	offset := len(sql) - len(trimmedLeft)
	lower := strings.ToLower(trimmedLeft)
	if !strings.HasPrefix(lower, "select") {
		return -1
	}
	if len(trimmedLeft) > len("select") && isDatabaseIdentifierChar(rune(trimmedLeft[len("select")])) {
		return -1
	}
	return offset + len("select")
}

func topLevelDatabaseKeywordIndex(sql string, keyword string) int {
	depth := 0
	for index := 0; index < len(sql); {
		ch := rune(sql[index])
		switch {
		case ch == '(':
			depth++
			index++
		case ch == ')':
			if depth > 0 {
				depth--
			}
			index++
		case isDatabaseIdentifierChar(ch):
			start := index
			for index < len(sql) && isDatabaseIdentifierChar(rune(sql[index])) {
				index++
			}
			if depth == 0 && strings.EqualFold(sql[start:index], keyword) {
				return start
			}
		default:
			index++
		}
	}
	return -1
}

func isDatabaseIdentifierChar(ch rune) bool {
	return ch == '_' || ch >= '0' && ch <= '9' || ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z'
}

func extractDatabaseTables(maskedSQL string) ([]string, error) {
	tokens := tokenizeDatabaseSQL(maskedSQL)
	tables := map[string]struct{}{}
	inFromClause := false
	expectTable := false

	for _, token := range tokens {
		lower := strings.ToLower(token)
		switch {
		case lower == "from" || lower == "join":
			inFromClause = true
			expectTable = true
			continue
		case isDatabaseSQLClauseStop(lower):
			inFromClause = false
			expectTable = false
			continue
		case inFromClause && lower == ",":
			expectTable = true
			continue
		case inFromClause && lower == "on":
			inFromClause = false
			expectTable = false
			continue
		case expectTable:
			table, err := normalizeDatabaseTableToken(token)
			if err != nil {
				return nil, err
			}
			tables[table] = struct{}{}
			expectTable = false
		}
	}

	if expectTable {
		return nil, badRequest("could not identify table after FROM or JOIN")
	}
	result := make([]string, 0, len(tables))
	for table := range tables {
		result = append(result, table)
	}
	sort.Strings(result)
	return result, nil
}

func tokenizeDatabaseSQL(sql string) []string {
	tokens := make([]string, 0)
	var current strings.Builder
	flush := func() {
		if current.Len() == 0 {
			return
		}
		tokens = append(tokens, current.String())
		current.Reset()
	}
	for _, ch := range sql {
		switch {
		case ch == ',' || ch == '(' || ch == ')':
			flush()
			tokens = append(tokens, string(ch))
		case ch == '.' || ch == '_' || ch >= '0' && ch <= '9' || ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z':
			current.WriteRune(ch)
		default:
			flush()
		}
	}
	flush()
	return tokens
}

func normalizeDatabaseTableToken(token string) (string, error) {
	trimmed := strings.TrimSpace(token)
	if trimmed == "(" {
		return "", badRequest("subqueries in FROM or JOIN are not supported by the table whitelist guard")
	}
	table, err := normalizeDatabaseTableName(trimmed)
	if err != nil {
		return "", err
	}
	if table.Schema == "" || table.Name == "" {
		return "", badRequest("could not identify table after FROM or JOIN")
	}
	return table.Schema + "." + table.Name, nil
}

func isDatabaseSQLClauseStop(token string) bool {
	switch token {
	case "where", "group", "order", "limit", "offset", "having", "union", "intersect", "except", "returning", ")":
		return true
	default:
		return false
	}
}

func singleStatementSQL(sql string) (string, error) {
	semicolons, err := semicolonPositionsOutsideLiterals(sql)
	if err != nil {
		return "", err
	}
	switch len(semicolons) {
	case 0:
		return strings.TrimSpace(sql), nil
	case 1:
		position := semicolons[0]
		if strings.TrimSpace(sql[position+1:]) != "" {
			return "", badRequest("multiple SQL statements are not allowed")
		}
		return strings.TrimSpace(sql[:position]), nil
	default:
		return "", badRequest("multiple SQL statements are not allowed")
	}
}

func semicolonPositionsOutsideLiterals(sql string) ([]int, error) {
	positions := make([]int, 0)
	inSingleQuote := false
	inDoubleQuote := false

	for index := 0; index < len(sql); index++ {
		ch := sql[index]

		switch {
		case inSingleQuote:
			if ch == '\'' {
				if index+1 < len(sql) && sql[index+1] == '\'' {
					index++
					continue
				}
				inSingleQuote = false
			}
			continue
		case inDoubleQuote:
			if ch == '"' {
				if index+1 < len(sql) && sql[index+1] == '"' {
					index++
					continue
				}
				inDoubleQuote = false
			}
			continue
		}

		if ch == '-' && index+1 < len(sql) && sql[index+1] == '-' {
			return nil, badRequest("SQL comments are not allowed")
		}
		if ch == '/' && index+1 < len(sql) && sql[index+1] == '*' {
			return nil, badRequest("SQL comments are not allowed")
		}
		if ch == '\'' {
			inSingleQuote = true
			continue
		}
		if ch == '"' {
			inDoubleQuote = true
			continue
		}
		if ch == ';' {
			positions = append(positions, index)
		}
	}

	if inSingleQuote || inDoubleQuote {
		return nil, badRequest("unterminated SQL string or identifier literal")
	}
	return positions, nil
}

func maskDatabaseSQLForGuard(sql string) (string, error) {
	var builder strings.Builder
	builder.Grow(len(sql))
	inSingleQuote := false
	inDoubleQuote := false

	for index := 0; index < len(sql); index++ {
		ch := sql[index]

		switch {
		case inSingleQuote:
			builder.WriteByte(' ')
			if ch == '\'' {
				if index+1 < len(sql) && sql[index+1] == '\'' {
					builder.WriteByte(' ')
					index++
					continue
				}
				inSingleQuote = false
			}
			continue
		case inDoubleQuote:
			builder.WriteByte(' ')
			if ch == '"' {
				if index+1 < len(sql) && sql[index+1] == '"' {
					builder.WriteByte(' ')
					index++
					continue
				}
				inDoubleQuote = false
			}
			continue
		}

		if ch == '-' && index+1 < len(sql) && sql[index+1] == '-' {
			return "", badRequest("SQL comments are not allowed")
		}
		if ch == '/' && index+1 < len(sql) && sql[index+1] == '*' {
			return "", badRequest("SQL comments are not allowed")
		}
		if ch == '\'' {
			builder.WriteByte(' ')
			inSingleQuote = true
			continue
		}
		if ch == '"' {
			builder.WriteByte(' ')
			inDoubleQuote = true
			continue
		}
		builder.WriteByte(ch)
	}

	if inSingleQuote || inDoubleQuote {
		return "", badRequest("unterminated SQL string or identifier literal")
	}
	return builder.String(), nil
}

func collectDatabaseRows(rows pgx.Rows) ([]map[string]any, error) {
	fields := rows.FieldDescriptions()
	result := make([]map[string]any, 0)
	for rows.Next() {
		values, err := rows.Values()
		if err != nil {
			return nil, err
		}
		item := make(map[string]any, len(fields))
		for index, field := range fields {
			var value any
			if index < len(values) {
				value = databaseJSONValue(field.Name, values[index])
			}
			item[field.Name] = value
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func databaseJSONValue(columnName string, value any) any {
	if isSensitiveDatabaseColumn(columnName) {
		return "[REDACTED]"
	}
	switch typed := value.(type) {
	case nil:
		return nil
	case []byte:
		return string(typed)
	case time.Time:
		return typed.Format(time.RFC3339Nano)
	}
	if _, err := json.Marshal(value); err == nil {
		return value
	}
	return fmt.Sprint(value)
}

func isSensitiveDatabaseColumn(columnName string) bool {
	normalized := strings.ToLower(strings.TrimSpace(columnName))
	if normalized == "" {
		return false
	}
	sensitiveTokens := []string{
		"password",
		"passwd",
		"secret",
		"token",
		"api_key",
		"access_key",
		"private_key",
		"authorization",
		"cookie",
		"email",
		"phone",
	}
	for _, token := range sensitiveTokens {
		if normalized == token || strings.Contains(normalized, token) {
			return true
		}
	}
	return false
}

func effectiveDatabaseQueryMaxRows(value int) int {
	if value <= 0 {
		return defaultDatabaseQueryMaxRows
	}
	if value > hardDatabaseQueryMaxRows {
		return hardDatabaseQueryMaxRows
	}
	return value
}

func effectiveDatabaseQueryTimeout(value time.Duration) time.Duration {
	if value <= 0 {
		return defaultDatabaseQueryTimeout
	}
	if value > hardDatabaseQueryTimeout {
		return hardDatabaseQueryTimeout
	}
	return value
}

func databaseQueryErr(ctx context.Context, timeout time.Duration, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("database query timed out after %s", timeout)
	}
	return err
}
