package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/mathaix/openclawmachines/backend/internal/store"
	"github.com/mathaix/openclawmachines/backend/pkg/crypto"
)

type workspaceIntegrationHTTPRequest struct {
	Method       string                                     `json:"method,omitempty"`
	URL          string                                     `json:"url,omitempty"`
	Path         string                                     `json:"path,omitempty"`
	Headers      map[string]string                          `json:"headers,omitempty"`
	HeaderParams map[string]workspaceIntegrationHTTPValue   `json:"header_params,omitempty"`
	PathParams   map[string]workspaceIntegrationHTTPValue   `json:"path_params,omitempty"`
	Query        map[string]workspaceIntegrationHTTPValue   `json:"query,omitempty"`
	Body         *workspaceIntegrationHTTPBody              `json:"body,omitempty"`
	Auth         *workspaceIntegrationHTTPAuth              `json:"auth,omitempty"`
	Response     *workspaceIntegrationHTTPResponseTransform `json:"response,omitempty"`
}

// workspaceIntegrationHTTPBody is a data-driven request-body spec. The manifest
// is stored as JSON and executed generically, so the body must be expressible
// here rather than in per-tool Go code.
type workspaceIntegrationHTTPBody struct {
	// Encoding selects how the body is built: "json" (default) assembles a JSON
	// object from Fields; "gmail_raw" builds an RFC-822 message from the to/
	// subject/body args and wraps it as {"raw": base64url(...)} for Gmail send;
	// "json_arg" serializes one argument directly as the JSON request body; and
	// "graphql" sends a persisted query template with the tool args as variables.
	Encoding string                              `json:"encoding,omitempty"`
	Name     string                              `json:"name,omitempty"`
	Required bool                                `json:"required,omitempty"`
	Query    string                              `json:"query,omitempty"`
	Fields   []workspaceIntegrationHTTPBodyField `json:"fields,omitempty"`
}

type workspaceIntegrationHTTPBodyField struct {
	// Path is a dotted key into the JSON object, e.g. "start.dateTime".
	Path  string                        `json:"path"`
	Value workspaceIntegrationHTTPValue `json:"value"`
	// Array wraps the resolved value in a single-element array (e.g. Drive parents).
	Array bool `json:"array,omitempty"`
	// Raw preserves the resolved JSON value instead of stringifying it.
	Raw bool `json:"raw,omitempty"`
	// Required rejects the call when the resolved value is empty.
	Required bool `json:"required,omitempty"`
}

type workspaceIntegrationHTTPValue struct {
	Source      string      `json:"source,omitempty"`
	Name        string      `json:"name,omitempty"`
	Value       interface{} `json:"value,omitempty"`
	Default     interface{} `json:"default,omitempty"`
	Enum        []string    `json:"enum,omitempty"`
	Allow       []string    `json:"allow,omitempty"`
	AllowConfig string      `json:"allow_config,omitempty"`
	Min         *int        `json:"min,omitempty"`
	Max         *int        `json:"max,omitempty"`
	OmitEmpty   bool        `json:"omit_empty,omitempty"`
	Format      string      `json:"format,omitempty"`
	Template    string      `json:"template,omitempty"`
}

type workspaceIntegrationHTTPAuth struct {
	Type     string `json:"type,omitempty"`
	Required bool   `json:"required,omitempty"`
	Scheme   string `json:"scheme,omitempty"`
	TokenURL string `json:"token_url,omitempty"`
	Header   string `json:"header,omitempty"`
	ClientID string `json:"client_id,omitempty"`
	Resource string `json:"resource,omitempty"`
}

type workspaceIntegrationHTTPResponseTransform struct {
	Fields           map[string]string `json:"fields,omitempty"`
	Wrap             string            `json:"wrap,omitempty"`
	ExcludeIfPresent string            `json:"exclude_if_present,omitempty"`
}

var workspaceIntegrationOAuthRefreshLocks sync.Map

const workspaceIntegrationHTTPMaxResponseBytes = 1 << 20

// workspaceIntegrationRateLimitBackoff is the pause before the single retry on a
// 429 from upstream. A var so tests can set it to zero.
var workspaceIntegrationRateLimitBackoff = 750 * time.Millisecond

// workspaceIntegrationMaxRateLimitRetryAfter caps upstream Retry-After so one
// tool call cannot pin an agent request for an unbounded interval.
var workspaceIntegrationMaxRateLimitRetryAfter = 5 * time.Second

// workspaceIntegrationUpstreamError carries the upstream HTTP status and a short,
// whitespace-collapsed snippet of the response so failures are diagnosable
// instead of an opaque "tool call failed".
type workspaceIntegrationUpstreamError struct {
	StatusCode   int
	Snippet      string
	RetryAfterMS *int
	Retryable    bool
	Terminal     bool
}

func (e *workspaceIntegrationUpstreamError) Error() string {
	if e.Snippet != "" {
		return fmt.Sprintf("http integration upstream returned %d: %s", e.StatusCode, e.Snippet)
	}
	return fmt.Sprintf("http integration upstream returned %d", e.StatusCode)
}

// workspaceIntegrationUpstreamSnippet extracts a short, single-line reason from an
// upstream error body, preferring a Google-style {"error":{"status","message"}}
// shape, and caps the length to avoid leaking large payloads into logs/errors.
func workspaceIntegrationUpstreamSnippet(body []byte) string {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return ""
	}
	var parsed struct {
		Error struct {
			Status  string `json:"status"`
			Message string `json:"message"`
			Errors  []struct {
				Reason string `json:"reason"`
			} `json:"errors"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &parsed); err == nil && parsed.Error.Message != "" {
		message := parsed.Error.Message
		if parsed.Error.Status != "" {
			message = parsed.Error.Status + ": " + message
		}
		reasons := make([]string, 0, len(parsed.Error.Errors))
		seen := map[string]bool{}
		for _, item := range parsed.Error.Errors {
			reason := strings.TrimSpace(item.Reason)
			if reason == "" || seen[reason] {
				continue
			}
			seen[reason] = true
			reasons = append(reasons, reason)
		}
		if len(reasons) > 0 {
			message += " (reason: " + strings.Join(reasons, ", ") + ")"
		}
		return workspaceIntegrationCollapseSnippet(message, 256)
	}
	return workspaceIntegrationCollapseSnippet(trimmed, 256)
}

func workspaceIntegrationCollapseSnippet(text string, max int) string {
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > max {
		return text[:max] + "…"
	}
	return text
}

func callWorkspaceIntegrationHTTPClient(allowInsecure bool) *http.Client {
	return workspaceIntegrationHTTPClient(allowInsecure)
}

func (s *Server) callHTTPWorkspaceIntegration(ctx context.Context, integration store.WorkspaceIntegration, tool workspaceIntegrationManifestTool, args map[string]interface{}) (map[string]interface{}, error) {
	if tool.Request == nil {
		return nil, fmt.Errorf("http tool %q is missing request mapping", tool.Name)
	}
	reqSpec := tool.Request
	config, err := workspaceIntegrationConfigMap(integration)
	if err != nil {
		return nil, err
	}
	endpoint, err := buildWorkspaceIntegrationHTTPRequestURL(integration, reqSpec, config, args)
	if err != nil {
		return nil, err
	}
	if err := workspaceIntegrationEndpointSafe(endpoint, s.allowInsecureWorkspaceIntegrationEndpoints); err != nil {
		return nil, err
	}
	method := strings.ToUpper(strings.TrimSpace(reqSpec.Method))
	if method == "" {
		method = http.MethodGet
	}
	bodyBytes, contentType, err := buildWorkspaceIntegrationHTTPRequestBody(reqSpec.Body, config, args)
	if err != nil {
		return nil, err
	}
	telemetry := workspaceIntegrationCallTelemetryFromContext(ctx)
	doRequest := func(forceOAuthRefresh bool) (int, []byte, http.Header, error) {
		var bodyReader io.Reader
		if bodyBytes != nil {
			bodyReader = bytes.NewReader(bodyBytes)
		}
		req, err := http.NewRequestWithContext(ctx, method, endpoint, bodyReader)
		if err != nil {
			return 0, nil, nil, err
		}
		req.Header.Set("Accept", "application/json")
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		for name, value := range reqSpec.Headers {
			if strings.TrimSpace(name) != "" {
				req.Header.Set(name, value)
			}
		}
		for name, valueSpec := range reqSpec.HeaderParams {
			headerName := strings.TrimSpace(name)
			if headerName == "" {
				continue
			}
			value, omit, err := resolveWorkspaceIntegrationHTTPValue(valueSpec, config, args)
			if err != nil {
				return 0, nil, nil, fmt.Errorf("resolve header param %q: %w", headerName, err)
			}
			if omit || strings.TrimSpace(value) == "" {
				continue
			}
			if strings.ContainsAny(value, "\r\n") {
				return 0, nil, nil, fmt.Errorf("header param %q must not contain newlines", headerName)
			}
			req.Header.Set(headerName, value)
		}
		if err := s.applyWorkspaceIntegrationHTTPAuth(ctx, req, integration, reqSpec.Auth, forceOAuthRefresh); err != nil {
			return 0, nil, nil, err
		}

		requestStart := time.Now()
		resp, err := callWorkspaceIntegrationHTTPClient(s.allowInsecureWorkspaceIntegrationEndpoints).Do(req)
		telemetry.addUpstreamLatency(time.Since(requestStart))
		if err != nil {
			return 0, nil, nil, err
		}
		defer func() { _ = resp.Body.Close() }()
		body, err := readWorkspaceIntegrationHTTPResponseBody(resp.Body)
		if err != nil {
			return 0, nil, nil, err
		}
		return resp.StatusCode, body, resp.Header.Clone(), nil
	}
	statusCode, body, headers, err := doRequest(false)
	if err != nil {
		return nil, err
	}
	if statusCode == http.StatusUnauthorized && workspaceIntegrationHTTPAuthIsOAuth(reqSpec.Auth) {
		statusCode, body, headers, err = doRequest(true)
		if err != nil {
			return nil, err
		}
	}
	// One bounded retry on read-safe upstream rate limiting. Write tools surface
	// as terminal so the caller does not duplicate a side effect.
	if statusCode == http.StatusTooManyRequests {
		delay := workspaceIntegrationRateLimitRetryDelay(headers.Get("Retry-After"), time.Now())
		retryAfterMS := int(delay.Milliseconds())
		if telemetry != nil {
			telemetry.RetryAfterMS = &retryAfterMS
		}
		if !workspaceIntegrationHTTPRetryAllowed(tool, method) {
			if telemetry != nil {
				telemetry.Retryable = false
				telemetry.Terminal = true
			}
			return nil, &workspaceIntegrationUpstreamError{
				StatusCode:   statusCode,
				Snippet:      workspaceIntegrationUpstreamSnippet(body),
				RetryAfterMS: &retryAfterMS,
				Retryable:    false,
				Terminal:     true,
			}
		}
		if telemetry != nil {
			telemetry.RetryCount++
			telemetry.Retryable = true
		}
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		statusCode, body, headers, err = doRequest(false)
		if err != nil {
			return nil, err
		}
	}
	if statusCode < 200 || statusCode >= 300 {
		var retryAfterMS *int
		retryable := false
		terminal := true
		if statusCode == http.StatusTooManyRequests {
			if telemetry != nil && telemetry.RetryAfterMS != nil {
				retryAfterMS = telemetry.RetryAfterMS
			} else {
				delay := workspaceIntegrationRateLimitRetryDelay(headers.Get("Retry-After"), time.Now())
				value := int(delay.Milliseconds())
				retryAfterMS = &value
			}
			retryable = workspaceIntegrationHTTPRetryAllowed(tool, method)
			terminal = !retryable
			if telemetry != nil {
				telemetry.Retryable = retryable
				telemetry.Terminal = terminal
			}
		}
		return nil, &workspaceIntegrationUpstreamError{
			StatusCode:   statusCode,
			Snippet:      workspaceIntegrationUpstreamSnippet(body),
			RetryAfterMS: retryAfterMS,
			Retryable:    retryable,
			Terminal:     terminal,
		}
	}
	var decoded interface{}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil {
		return nil, fmt.Errorf("decode http integration response: %w", err)
	}
	return transformWorkspaceIntegrationHTTPResponse(decoded, reqSpec.Response)
}

func readWorkspaceIntegrationHTTPResponseBody(body io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, workspaceIntegrationHTTPMaxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > workspaceIntegrationHTTPMaxResponseBytes {
		return nil, fmt.Errorf("http integration response exceeds %d bytes", workspaceIntegrationHTTPMaxResponseBytes)
	}
	return data, nil
}

func workspaceIntegrationHTTPAuthIsOAuth(authSpec *workspaceIntegrationHTTPAuth) bool {
	return authSpec != nil && strings.EqualFold(strings.TrimSpace(authSpec.Type), "oauth")
}

func workspaceIntegrationHTTPRetryAllowed(tool workspaceIntegrationManifestTool, method string) bool {
	if workspaceIntegrationManifestToolAccess(tool) == "write" {
		return false
	}
	switch strings.ToUpper(strings.TrimSpace(method)) {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return true
	case http.MethodPost:
		return workspaceIntegrationManifestToolAccess(tool) == "read"
	default:
		return false
	}
}

func workspaceIntegrationRateLimitRetryDelay(retryAfter string, now time.Time) time.Duration {
	retryAfter = strings.TrimSpace(retryAfter)
	if retryAfter == "" {
		return workspaceIntegrationRateLimitBackoff
	}
	if seconds, err := strconv.Atoi(retryAfter); err == nil {
		return workspaceIntegrationCapRetryDelay(time.Duration(seconds) * time.Second)
	}
	if at, err := http.ParseTime(retryAfter); err == nil {
		return workspaceIntegrationCapRetryDelay(at.Sub(now))
	}
	return workspaceIntegrationRateLimitBackoff
}

func workspaceIntegrationCapRetryDelay(delay time.Duration) time.Duration {
	if delay < 0 {
		return 0
	}
	if delay > workspaceIntegrationMaxRateLimitRetryAfter {
		return workspaceIntegrationMaxRateLimitRetryAfter
	}
	return delay
}

func workspaceIntegrationConfigMap(integration store.WorkspaceIntegration) (map[string]interface{}, error) {
	if len(integration.Config) == 0 {
		return map[string]interface{}{}, nil
	}
	var config map[string]interface{}
	if err := json.Unmarshal(integration.Config, &config); err != nil {
		return nil, fmt.Errorf("parse workspace integration config: %w", err)
	}
	return config, nil
}

func buildWorkspaceIntegrationHTTPRequestURL(integration store.WorkspaceIntegration, reqSpec *workspaceIntegrationHTTPRequest, config, args map[string]interface{}) (string, error) {
	raw := strings.TrimSpace(reqSpec.URL)
	if raw == "" {
		if integration.Endpoint == nil || strings.TrimSpace(*integration.Endpoint) == "" {
			return "", fmt.Errorf("http integration %q is missing endpoint", integration.Slug)
		}
		base, err := url.Parse(strings.TrimRight(strings.TrimSpace(*integration.Endpoint), "/"))
		if err != nil {
			return "", fmt.Errorf("invalid http integration endpoint: %w", err)
		}
		path := strings.TrimSpace(reqSpec.Path)
		if path != "" {
			base.Path = strings.TrimRight(base.Path, "/") + "/" + strings.TrimLeft(path, "/")
		}
		raw = base.String()
	}
	for name, valueSpec := range reqSpec.PathParams {
		value, omit, err := resolveWorkspaceIntegrationHTTPValue(valueSpec, config, args)
		if err != nil {
			return "", fmt.Errorf("resolve path param %q: %w", name, err)
		}
		if omit {
			return "", fmt.Errorf("path param %q is required", name)
		}
		escapedValue := url.PathEscape(value)
		raw = strings.ReplaceAll(raw, "{"+name+"}", escapedValue)
		raw = strings.ReplaceAll(raw, "%7B"+name+"%7D", escapedValue)
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid http integration request url: %w", err)
	}
	values := parsed.Query()
	for name, valueSpec := range reqSpec.Query {
		value, omit, err := resolveWorkspaceIntegrationHTTPValue(valueSpec, config, args)
		if err != nil {
			return "", fmt.Errorf("resolve query param %q: %w", name, err)
		}
		if omit {
			continue
		}
		values.Set(name, value)
	}
	parsed.RawQuery = values.Encode()
	return parsed.String(), nil
}

// buildWorkspaceIntegrationHTTPRequestBody assembles the request body bytes and
// content type from the body spec. Returns (nil, "", nil) when there is no body.
func buildWorkspaceIntegrationHTTPRequestBody(spec *workspaceIntegrationHTTPBody, config, args map[string]interface{}) ([]byte, string, error) {
	if spec == nil {
		return nil, "", nil
	}
	switch strings.ToLower(strings.TrimSpace(spec.Encoding)) {
	case "gmail_raw":
		return buildWorkspaceIntegrationGmailRawBody(args)
	case "json_arg":
		name := strings.TrimSpace(spec.Name)
		if name == "" {
			name = "body"
		}
		raw := args[name]
		if isEmptyWorkspaceIntegrationHTTPValue(raw) {
			if spec.Required {
				return nil, "", fmt.Errorf("request body argument %q is required", name)
			}
			return nil, "", nil
		}
		data, err := json.Marshal(raw)
		if err != nil {
			return nil, "", fmt.Errorf("marshal request body argument %q: %w", name, err)
		}
		return data, "application/json", nil
	case "graphql":
		query := strings.TrimSpace(spec.Query)
		if query == "" {
			return nil, "", errors.New("graphql request body is missing query")
		}
		data, err := json.Marshal(map[string]interface{}{
			"query":     query,
			"variables": args,
		})
		if err != nil {
			return nil, "", fmt.Errorf("marshal graphql request body: %w", err)
		}
		return data, "application/json", nil
	case "", "json":
		root := map[string]interface{}{}
		for _, field := range spec.Fields {
			var payload interface{}
			if field.Raw {
				value, omit, err := resolveWorkspaceIntegrationHTTPRawValue(field.Value, config, args)
				if err != nil {
					return nil, "", fmt.Errorf("resolve body field %q: %w", field.Path, err)
				}
				if isEmptyWorkspaceIntegrationHTTPValue(value) {
					if field.Required {
						return nil, "", fmt.Errorf("body field %q is required", field.Path)
					}
					if omit {
						continue
					}
				}
				payload = value
				if field.Array {
					payload = []interface{}{value}
				}
			} else {
				value, omit, err := resolveWorkspaceIntegrationHTTPValue(field.Value, config, args)
				if err != nil {
					return nil, "", fmt.Errorf("resolve body field %q: %w", field.Path, err)
				}
				if strings.TrimSpace(value) == "" {
					if field.Required {
						return nil, "", fmt.Errorf("body field %q is required", field.Path)
					}
					if omit {
						continue
					}
				}
				payload = value
				if field.Array {
					payload = []interface{}{value}
				}
			}
			if err := setNestedJSONField(root, field.Path, payload); err != nil {
				return nil, "", err
			}
		}
		data, err := json.Marshal(root)
		if err != nil {
			return nil, "", fmt.Errorf("marshal request body: %w", err)
		}
		return data, "application/json", nil
	default:
		return nil, "", fmt.Errorf("unsupported body encoding %q", spec.Encoding)
	}
}

func resolveWorkspaceIntegrationHTTPRawValue(spec workspaceIntegrationHTTPValue, config, args map[string]interface{}) (interface{}, bool, error) {
	var raw interface{}
	switch strings.ToLower(strings.TrimSpace(spec.Source)) {
	case "", "literal":
		raw = spec.Value
	case "arg":
		raw = args[spec.Name]
	case "args":
		raw = args
	case "config":
		raw = config[spec.Name]
	default:
		return nil, false, fmt.Errorf("unsupported raw value source %q", spec.Source)
	}
	if isEmptyWorkspaceIntegrationHTTPValue(raw) {
		raw = spec.Default
	}
	if isEmptyWorkspaceIntegrationHTTPValue(raw) {
		return nil, spec.OmitEmpty, nil
	}
	return raw, false, nil
}

// setNestedJSONField writes value into root following a dotted path, creating
// intermediate objects as needed.
func setNestedJSONField(root map[string]interface{}, path string, value interface{}) error {
	parts := strings.Split(path, ".")
	current := root
	for i, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return fmt.Errorf("invalid body field path %q", path)
		}
		if i == len(parts)-1 {
			current[part] = value
			return nil
		}
		next, ok := current[part].(map[string]interface{})
		if !ok {
			next = map[string]interface{}{}
			current[part] = next
		}
		current = next
	}
	return nil
}

// buildWorkspaceIntegrationGmailRawBody builds a minimal RFC-822 message from the
// to/subject/body args and wraps it as {"raw": base64url(...)} for the Gmail
// users.messages.send endpoint. Header values are rejected if they contain CR/LF
// to prevent header injection.
func buildWorkspaceIntegrationGmailRawBody(args map[string]interface{}) ([]byte, string, error) {
	to := strings.TrimSpace(workspaceIntegrationArgString(args["to"]))
	subject := strings.TrimSpace(workspaceIntegrationArgString(args["subject"]))
	body := workspaceIntegrationArgString(args["body"])
	if to == "" {
		return nil, "", errors.New("gmail send requires \"to\"")
	}
	if strings.ContainsAny(to, "\r\n") || strings.ContainsAny(subject, "\r\n") {
		return nil, "", errors.New("gmail send headers must not contain newlines")
	}
	message := "To: " + to + "\r\n" +
		"Subject: " + subject + "\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=\"UTF-8\"\r\n" +
		"\r\n" + body
	raw := base64.RawURLEncoding.EncodeToString([]byte(message))
	data, err := json.Marshal(map[string]string{"raw": raw})
	if err != nil {
		return nil, "", fmt.Errorf("marshal gmail send body: %w", err)
	}
	return data, "application/json", nil
}

func workspaceIntegrationArgString(raw interface{}) string {
	switch value := raw.(type) {
	case nil:
		return ""
	case string:
		return value
	case json.Number:
		return value.String()
	default:
		return fmt.Sprintf("%v", value)
	}
}

func resolveWorkspaceIntegrationHTTPValue(spec workspaceIntegrationHTTPValue, config, args map[string]interface{}) (string, bool, error) {
	var raw interface{}
	switch strings.ToLower(strings.TrimSpace(spec.Source)) {
	case "", "literal":
		raw = spec.Value
	case "arg":
		raw = args[spec.Name]
	case "config":
		raw = config[spec.Name]
	case "now":
		switch strings.ToLower(strings.TrimSpace(spec.Format)) {
		case "", "rfc3339":
			return time.Now().UTC().Format(time.RFC3339), false, nil
		default:
			return "", false, fmt.Errorf("unsupported now format %q", spec.Format)
		}
	default:
		return "", false, fmt.Errorf("unsupported value source %q", spec.Source)
	}
	if isEmptyWorkspaceIntegrationHTTPValue(raw) {
		raw = spec.Default
	}
	if isEmptyWorkspaceIntegrationHTTPValue(raw) {
		return "", spec.OmitEmpty, nil
	}
	value, err := stringifyWorkspaceIntegrationHTTPValue(raw, spec)
	if err != nil {
		return "", false, err
	}
	if len(spec.Enum) > 0 {
		for _, allowed := range spec.Enum {
			if value == allowed {
				return value, false, nil
			}
		}
		return "", false, fmt.Errorf("invalid value %q", value)
	}
	if err := validateWorkspaceIntegrationHTTPAllowedValue(value, spec, config); err != nil {
		return "", false, err
	}
	if strings.TrimSpace(spec.Template) != "" {
		value = strings.ReplaceAll(spec.Template, "{value}", value)
	}
	return value, false, nil
}

func validateWorkspaceIntegrationHTTPAllowedValue(value string, spec workspaceIntegrationHTTPValue, config map[string]interface{}) error {
	allowed, err := workspaceIntegrationHTTPAllowedValues(spec, config)
	if err != nil {
		return err
	}
	if len(allowed) == 0 {
		return nil
	}
	if _, ok := allowed[value]; ok {
		return nil
	}
	return fmt.Errorf("value %q is not allowed", value)
}

func workspaceIntegrationHTTPAllowedValues(spec workspaceIntegrationHTTPValue, config map[string]interface{}) (map[string]struct{}, error) {
	allowed := make(map[string]struct{})
	var addAllowed func(raw interface{}) error
	addAllowed = func(raw interface{}) error {
		switch value := raw.(type) {
		case nil:
			return nil
		case string:
			trimmed := strings.TrimSpace(value)
			if trimmed != "" {
				allowed[trimmed] = struct{}{}
			}
		case []string:
			for _, item := range value {
				if err := addAllowed(item); err != nil {
					return err
				}
			}
		case []interface{}:
			for _, item := range value {
				if err := addAllowed(item); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("allowlist %q must contain strings, got %T", spec.AllowConfig, raw)
		}
		return nil
	}
	for _, value := range spec.Allow {
		if err := addAllowed(value); err != nil {
			return nil, err
		}
	}
	if spec.AllowConfig != "" {
		if err := addAllowed(config[spec.AllowConfig]); err != nil {
			return nil, err
		}
	}
	return allowed, nil
}

func isEmptyWorkspaceIntegrationHTTPValue(value interface{}) bool {
	if value == nil {
		return true
	}
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text) == ""
	}
	return false
}

func stringifyWorkspaceIntegrationHTTPValue(raw interface{}, spec workspaceIntegrationHTTPValue) (string, error) {
	if spec.Min != nil || spec.Max != nil {
		value, err := intWorkspaceIntegrationHTTPValue(raw)
		if err != nil {
			return "", err
		}
		if spec.Min != nil && value < *spec.Min {
			value = *spec.Min
		}
		if spec.Max != nil && value > *spec.Max {
			value = *spec.Max
		}
		return strconv.Itoa(value), nil
	}
	switch value := raw.(type) {
	case string:
		return strings.TrimSpace(value), nil
	case json.Number:
		return value.String(), nil
	case float64:
		if value == float64(int(value)) {
			return strconv.Itoa(int(value)), nil
		}
		return strconv.FormatFloat(value, 'f', -1, 64), nil
	case int:
		return strconv.Itoa(value), nil
	case bool:
		return strconv.FormatBool(value), nil
	default:
		return fmt.Sprintf("%v", value), nil
	}
}

func intWorkspaceIntegrationHTTPValue(raw interface{}) (int, error) {
	switch value := raw.(type) {
	case int:
		return value, nil
	case int64:
		return int(value), nil
	case float64:
		return int(value), nil
	case json.Number:
		parsed, err := value.Int64()
		return int(parsed), err
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		return parsed, err
	default:
		return 0, fmt.Errorf("expected integer value, got %T", raw)
	}
}

func (s *Server) applyWorkspaceIntegrationHTTPAuth(ctx context.Context, req *http.Request, integration store.WorkspaceIntegration, authSpec *workspaceIntegrationHTTPAuth, forceOAuthRefresh bool) error {
	if authSpec == nil || strings.TrimSpace(authSpec.Type) == "" || strings.EqualFold(authSpec.Type, "none") {
		return nil
	}
	header := strings.TrimSpace(authSpec.Header)
	if header == "" {
		header = "Authorization"
	}
	switch strings.ToLower(strings.TrimSpace(authSpec.Type)) {
	case "bearer":
		token, err := s.workspaceIntegrationStoredSecret(ctx, integration, authSpec.Required)
		if err != nil {
			return err
		}
		if token == "" {
			return nil
		}
		scheme := strings.TrimSpace(authSpec.Scheme)
		if scheme == "" {
			scheme = "Bearer"
		}
		req.Header.Set(header, scheme+" "+token)
		return nil
	case "api_key_header":
		token, err := s.workspaceIntegrationStoredSecret(ctx, integration, authSpec.Required)
		if err != nil {
			return err
		}
		if token == "" {
			return nil
		}
		req.Header.Set(header, token)
		return nil
	case "oauth":
		token, err := s.workspaceIntegrationOAuthAccessTokenForAuth(ctx, integration, authSpec, forceOAuthRefresh)
		if err != nil {
			return err
		}
		req.Header.Set(header, "Bearer "+token)
		return nil
	default:
		return fmt.Errorf("unsupported http integration auth type %q", authSpec.Type)
	}
}

func (s *Server) workspaceIntegrationOAuthAccessTokenForAuth(ctx context.Context, integration store.WorkspaceIntegration, authSpec *workspaceIntegrationHTTPAuth, forceRefresh bool) (string, error) {
	if authSpec == nil {
		return "", errors.New("oauth auth spec not configured")
	}
	clientID := strings.TrimSpace(authSpec.ClientID)
	clientSecret := ""
	resource := strings.TrimSpace(authSpec.Resource)
	if clientID == "" {
		clientID, clientSecret = s.oauthClientCredentialsForProvider(integration.Slug)
	}
	if strings.EqualFold(strings.TrimSpace(integration.CredentialRefKind), "connection") {
		return s.workspaceIntegrationOAuthConnectionAccessTokenWithClient(ctx, integration.CredentialRefID, authSpec.TokenURL, clientID, clientSecret, resource, forceRefresh)
	}
	return s.workspaceIntegrationOAuthAccessTokenWithClient(ctx, integration.ID, authSpec.TokenURL, clientID, clientSecret, resource, forceRefresh)
}

func (s *Server) workspaceIntegrationStoredSecret(ctx context.Context, integration store.WorkspaceIntegration, required bool) (string, error) {
	if strings.EqualFold(strings.TrimSpace(integration.CredentialRefKind), "connection") {
		return s.workspaceIntegrationStoredConnectionSecret(ctx, integration.CredentialRefID, required)
	}
	return s.workspaceIntegrationStoredLegacySecret(ctx, integration.ID, required)
}

func (s *Server) workspaceIntegrationStoredLegacySecret(ctx context.Context, integrationID string, required bool) (string, error) {
	credential, err := s.store.GetWorkspaceIntegrationCredential(ctx, integrationID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) && !required {
			return "", nil
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return "", errors.New("workspace integration credential not configured")
		}
		return "", fmt.Errorf("load workspace integration credential: %w", err)
	}
	if credential == nil || credential.SecretEnc == "" {
		if required {
			return "", errors.New("workspace integration credential not configured")
		}
		return "", nil
	}
	if s.secretKey == "" {
		return "", errors.New("SECRET_ENCRYPTION_KEY not configured")
	}
	token, err := crypto.Decrypt(credential.SecretEnc, s.secretKey)
	if err != nil {
		return "", fmt.Errorf("decrypt workspace integration credential: %w", err)
	}
	return token, nil
}

func (s *Server) workspaceIntegrationStoredConnectionSecret(ctx context.Context, connectionID string, required bool) (string, error) {
	reader, ok := s.store.(workspaceIntegrationConnectionCredentialReader)
	if !ok {
		if required {
			return "", errors.New("workspace integration credential not configured")
		}
		return "", nil
	}
	credential, err := reader.GetWorkspaceIntegrationConnectionCredential(ctx, connectionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) && !required {
			return "", nil
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return "", errors.New("workspace integration credential not configured")
		}
		return "", fmt.Errorf("load workspace integration connection credential: %w", err)
	}
	if credential == nil || credential.SecretEnc == "" {
		if required {
			return "", errors.New("workspace integration credential not configured")
		}
		return "", nil
	}
	if s.secretKey == "" {
		return "", errors.New("SECRET_ENCRYPTION_KEY not configured")
	}
	token, err := crypto.Decrypt(credential.SecretEnc, s.secretKey)
	if err != nil {
		return "", fmt.Errorf("decrypt workspace integration credential: %w", err)
	}
	return token, nil
}

func (s *Server) workspaceIntegrationOAuthAccessToken(ctx context.Context, providerSlug, integrationID, tokenURL string, forceRefresh bool) (string, error) {
	clientID, clientSecret := s.oauthClientCredentialsForProvider(providerSlug)
	return s.workspaceIntegrationOAuthAccessTokenWithClient(ctx, integrationID, tokenURL, clientID, clientSecret, "", forceRefresh)
}

func (s *Server) workspaceIntegrationOAuthAccessTokenWithClient(ctx context.Context, integrationID, tokenURL, clientID, clientSecret, resource string, forceRefresh bool) (string, error) {
	tokenURL = strings.TrimSpace(tokenURL)
	if tokenURL == "" {
		return "", errors.New("oauth token endpoint not configured")
	}
	credential, err := s.loadWorkspaceIntegrationOAuthCredential(ctx, integrationID)
	if err != nil {
		return "", err
	}
	if !forceRefresh && credential.ExpiresAt != nil && credential.ExpiresAt.After(time.Now().Add(1*time.Minute)) {
		return s.decryptWorkspaceIntegrationOAuthAccessToken(credential)
	}
	if credential.RefreshEnc == nil || *credential.RefreshEnc == "" {
		return s.decryptWorkspaceIntegrationOAuthAccessToken(credential)
	}

	lock := workspaceIntegrationOAuthRefreshLock(integrationID)
	lock.Lock()
	defer lock.Unlock()

	credential, err = s.loadWorkspaceIntegrationOAuthCredential(ctx, integrationID)
	if err != nil {
		return "", err
	}
	if !forceRefresh && credential.ExpiresAt != nil && credential.ExpiresAt.After(time.Now().Add(1*time.Minute)) {
		return s.decryptWorkspaceIntegrationOAuthAccessToken(credential)
	}
	refreshToken, err := crypto.Decrypt(*credential.RefreshEnc, s.secretKey)
	if err != nil {
		return "", fmt.Errorf("decrypt workspace oauth refresh token: %w", err)
	}
	form := url.Values{}
	form.Set("client_id", clientID)
	if strings.TrimSpace(clientSecret) != "" {
		form.Set("client_secret", clientSecret)
	}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	if strings.TrimSpace(resource) != "" {
		form.Set("resource", strings.TrimSpace(resource))
	}
	tokenResp, err := s.workspaceIntegrationOAuthTokenRequest(ctx, tokenURL, form)
	if err != nil {
		return "", err
	}
	if tokenResp.RefreshToken == "" {
		tokenResp.RefreshToken = refreshToken
	}
	if err := s.updateWorkspaceOAuthCredential(ctx, credential.IntegrationID, tokenResp); err != nil {
		return "", err
	}
	return tokenResp.AccessToken, nil
}

func (s *Server) workspaceIntegrationOAuthConnectionAccessTokenWithClient(ctx context.Context, connectionID, tokenURL, clientID, clientSecret, resource string, forceRefresh bool) (string, error) {
	tokenURL = strings.TrimSpace(tokenURL)
	if tokenURL == "" {
		return "", errors.New("oauth token endpoint not configured")
	}
	credential, err := s.loadWorkspaceIntegrationOAuthConnectionCredential(ctx, connectionID)
	if err != nil {
		return "", err
	}
	if !forceRefresh && credential.ExpiresAt != nil && credential.ExpiresAt.After(time.Now().Add(1*time.Minute)) {
		return s.decryptWorkspaceIntegrationOAuthConnectionAccessToken(credential)
	}
	if credential.RefreshEnc == nil || *credential.RefreshEnc == "" {
		return s.decryptWorkspaceIntegrationOAuthConnectionAccessToken(credential)
	}

	lock := workspaceIntegrationOAuthRefreshLock("connection:" + connectionID)
	lock.Lock()
	defer lock.Unlock()

	credential, err = s.loadWorkspaceIntegrationOAuthConnectionCredential(ctx, connectionID)
	if err != nil {
		return "", err
	}
	if !forceRefresh && credential.ExpiresAt != nil && credential.ExpiresAt.After(time.Now().Add(1*time.Minute)) {
		return s.decryptWorkspaceIntegrationOAuthConnectionAccessToken(credential)
	}
	refreshToken, err := crypto.Decrypt(*credential.RefreshEnc, s.secretKey)
	if err != nil {
		return "", fmt.Errorf("decrypt workspace oauth refresh token: %w", err)
	}
	form := url.Values{}
	form.Set("client_id", clientID)
	if strings.TrimSpace(clientSecret) != "" {
		form.Set("client_secret", clientSecret)
	}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	if strings.TrimSpace(resource) != "" {
		form.Set("resource", strings.TrimSpace(resource))
	}
	tokenResp, err := s.workspaceIntegrationOAuthTokenRequest(ctx, tokenURL, form)
	if err != nil {
		return "", err
	}
	if tokenResp.RefreshToken == "" {
		tokenResp.RefreshToken = refreshToken
	}
	if err := s.updateWorkspaceOAuthConnectionCredential(ctx, credential.ConnectionID, tokenResp); err != nil {
		return "", err
	}
	return tokenResp.AccessToken, nil
}

func workspaceIntegrationOAuthRefreshLock(integrationID string) *sync.Mutex {
	value, _ := workspaceIntegrationOAuthRefreshLocks.LoadOrStore(integrationID, &sync.Mutex{})
	return value.(*sync.Mutex)
}

func (s *Server) loadWorkspaceIntegrationOAuthCredential(ctx context.Context, integrationID string) (*store.WorkspaceIntegrationCredential, error) {
	credential, err := s.store.GetWorkspaceIntegrationCredential(ctx, integrationID)
	if err != nil {
		return nil, fmt.Errorf("load workspace oauth credential: %w", err)
	}
	if credential == nil || credential.SecretEnc == "" {
		return nil, errors.New("workspace oauth credential not configured")
	}
	if s.secretKey == "" {
		return nil, errors.New("SECRET_ENCRYPTION_KEY not configured")
	}
	return credential, nil
}

func (s *Server) decryptWorkspaceIntegrationOAuthAccessToken(credential *store.WorkspaceIntegrationCredential) (string, error) {
	accessToken, err := crypto.Decrypt(credential.SecretEnc, s.secretKey)
	if err != nil {
		return "", fmt.Errorf("decrypt workspace oauth access token: %w", err)
	}
	return accessToken, nil
}

func (s *Server) loadWorkspaceIntegrationOAuthConnectionCredential(ctx context.Context, connectionID string) (*store.WorkspaceIntegrationConnectionCredential, error) {
	reader, ok := s.store.(workspaceIntegrationConnectionCredentialReader)
	if !ok {
		return nil, errors.New("workspace oauth credential not configured")
	}
	credential, err := reader.GetWorkspaceIntegrationConnectionCredential(ctx, connectionID)
	if err != nil {
		return nil, fmt.Errorf("load workspace oauth credential: %w", err)
	}
	if credential == nil || credential.SecretEnc == "" {
		return nil, errors.New("workspace oauth credential not configured")
	}
	if s.secretKey == "" {
		return nil, errors.New("SECRET_ENCRYPTION_KEY not configured")
	}
	return credential, nil
}

func (s *Server) decryptWorkspaceIntegrationOAuthConnectionAccessToken(credential *store.WorkspaceIntegrationConnectionCredential) (string, error) {
	accessToken, err := crypto.Decrypt(credential.SecretEnc, s.secretKey)
	if err != nil {
		return "", fmt.Errorf("decrypt workspace oauth access token: %w", err)
	}
	return accessToken, nil
}

func (s *Server) updateWorkspaceOAuthCredential(ctx context.Context, integrationID string, tokenResp workspaceIntegrationOAuthTokenResponse) error {
	return s.persistWorkspaceOAuthCredential(ctx, integrationID, tokenResp, workspaceOAuthCredentialPersistOptions{})
}

func (s *Server) updateWorkspaceOAuthConnectionCredential(ctx context.Context, connectionID string, tokenResp workspaceIntegrationOAuthTokenResponse) error {
	return s.persistWorkspaceOAuthConnectionCredential(ctx, connectionID, tokenResp, workspaceOAuthCredentialPersistOptions{})
}

type workspaceOAuthCredentialPersistOptions struct {
	PreserveRefreshOnEmpty bool
}

func (s *Server) persistWorkspaceOAuthCredential(ctx context.Context, integrationID string, tokenResp workspaceIntegrationOAuthTokenResponse, opts workspaceOAuthCredentialPersistOptions) error {
	if s.secretKey == "" {
		return errors.New("SECRET_ENCRYPTION_KEY not configured")
	}
	accessEnc, err := crypto.Encrypt(tokenResp.AccessToken, s.secretKey)
	if err != nil {
		return fmt.Errorf("encrypt workspace oauth access token: %w", err)
	}
	var refreshEnc *string
	if tokenResp.RefreshToken != "" {
		encrypted, err := crypto.Encrypt(tokenResp.RefreshToken, s.secretKey)
		if err != nil {
			return fmt.Errorf("encrypt workspace oauth refresh token: %w", err)
		}
		refreshEnc = &encrypted
	} else if opts.PreserveRefreshOnEmpty {
		existing, err := s.store.GetWorkspaceIntegrationCredential(ctx, integrationID)
		switch {
		case err == nil && existing != nil:
			refreshEnc = existing.RefreshEnc
		case err == nil, errors.Is(err, pgx.ErrNoRows):
		default:
			return fmt.Errorf("load existing workspace oauth credential: %w", err)
		}
	}
	tokenType := tokenResp.TokenType
	if tokenType == "" {
		tokenType = "Bearer"
	}
	var expiresAt *time.Time
	if tokenResp.ExpiresIn > 0 {
		expires := time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
		expiresAt = &expires
	}
	return s.saveWorkspaceIntegrationCredentialAndConnectionCredentials(ctx, integrationID, accessEnc, refreshEnc, &tokenType, expiresAt)
}

func (s *Server) persistWorkspaceOAuthConnectionCredential(ctx context.Context, connectionID string, tokenResp workspaceIntegrationOAuthTokenResponse, opts workspaceOAuthCredentialPersistOptions) error {
	if s.secretKey == "" {
		return errors.New("SECRET_ENCRYPTION_KEY not configured")
	}
	writer, ok := s.store.(workspaceIntegrationConnectionCredentialWriter)
	if !ok {
		return errors.New("workspace oauth credential not configured")
	}
	accessEnc, err := crypto.Encrypt(tokenResp.AccessToken, s.secretKey)
	if err != nil {
		return fmt.Errorf("encrypt workspace oauth access token: %w", err)
	}
	var refreshEnc *string
	if tokenResp.RefreshToken != "" {
		encrypted, err := crypto.Encrypt(tokenResp.RefreshToken, s.secretKey)
		if err != nil {
			return fmt.Errorf("encrypt workspace oauth refresh token: %w", err)
		}
		refreshEnc = &encrypted
	} else if opts.PreserveRefreshOnEmpty {
		reader, ok := s.store.(workspaceIntegrationConnectionCredentialReader)
		if !ok {
			return errors.New("workspace oauth credential not configured")
		}
		existing, err := reader.GetWorkspaceIntegrationConnectionCredential(ctx, connectionID)
		switch {
		case err == nil && existing != nil:
			refreshEnc = existing.RefreshEnc
		case err == nil, errors.Is(err, pgx.ErrNoRows):
		default:
			return fmt.Errorf("load existing workspace oauth credential: %w", err)
		}
	}
	tokenType := tokenResp.TokenType
	if tokenType == "" {
		tokenType = "Bearer"
	}
	var expiresAt *time.Time
	if tokenResp.ExpiresIn > 0 {
		expires := time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
		expiresAt = &expires
	}
	return writer.SetWorkspaceIntegrationConnectionCredential(ctx, &store.WorkspaceIntegrationConnectionCredential{
		ConnectionID: connectionID,
		SecretEnc:    accessEnc,
		RefreshEnc:   refreshEnc,
		TokenType:    &tokenType,
		ExpiresAt:    expiresAt,
	})
}

func transformWorkspaceIntegrationHTTPResponse(decoded interface{}, transform *workspaceIntegrationHTTPResponseTransform) (map[string]interface{}, error) {
	if transform == nil {
		if object, ok := decoded.(map[string]interface{}); ok {
			return object, nil
		}
		return map[string]interface{}{"value": decoded}, nil
	}
	if items, ok := decoded.([]interface{}); ok {
		out := make([]map[string]interface{}, 0, len(items))
		for _, item := range items {
			object, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			if transform.ExcludeIfPresent != "" {
				if value, ok := readWorkspaceIntegrationHTTPField(object, transform.ExcludeIfPresent); ok && value != nil {
					continue
				}
			}
			out = append(out, projectWorkspaceIntegrationHTTPFields(object, transform.Fields))
		}
		wrap := strings.TrimSpace(transform.Wrap)
		if wrap == "" {
			wrap = "items"
		}
		return map[string]interface{}{wrap: out}, nil
	}
	object, ok := decoded.(map[string]interface{})
	if !ok {
		return map[string]interface{}{"value": decoded}, nil
	}
	if len(transform.Fields) == 0 {
		return object, nil
	}
	return projectWorkspaceIntegrationHTTPFields(object, transform.Fields), nil
}

func projectWorkspaceIntegrationHTTPFields(object map[string]interface{}, fields map[string]string) map[string]interface{} {
	out := make(map[string]interface{}, len(fields))
	for target, source := range fields {
		if strings.TrimSpace(source) == "" {
			source = target
		}
		if value, ok := readWorkspaceIntegrationHTTPField(object, source); ok {
			out[target] = value
		}
	}
	return out
}

func readWorkspaceIntegrationHTTPField(object map[string]interface{}, path string) (interface{}, bool) {
	var current interface{} = object
	for _, part := range strings.Split(path, ".") {
		next, ok := current.(map[string]interface{})
		if !ok {
			return nil, false
		}
		current, ok = next[part]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func mustWorkspaceIntegrationToolManifest(tools []workspaceIntegrationManifestTool) json.RawMessage {
	data, err := json.Marshal(tools)
	if err != nil {
		panic(err)
	}
	return data
}

func workspaceIntegrationIntPtr(value int) *int {
	return &value
}
