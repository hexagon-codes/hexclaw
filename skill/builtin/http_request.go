package builtin

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/hexagon-codes/hexclaw/skill"
)

// BrowserArtifactWriter 将本次响应流交给统一产物存储，返回可交付的归属引用。
type BrowserArtifactWriter func(context.Context, string, string, io.Reader) (map[string]any, error)

// BrowserFileOpener 按本轮有效权限和既有附件归属解析输入，不另建文件事实源。
type BrowserFileOpener func(context.Context, string) (io.ReadCloser, error)

// HTTPRequestAuth 是已解析的本次认证，仅驻留内存，不出现在结果和日志中。
type HTTPRequestAuth struct {
	Type     string
	Token    string
	Username string
	Password string
	In       string
	Name     string
	Value    string
	Source   string
}

// BrowserAuthResolver 使用明确引用或目标服务已有绑定，不借用其他服务的凭据。
type BrowserAuthResolver func(context.Context, string, string) (HTTPRequestAuth, error)

// BrowserAttemptObserver 在每次物理请求前登记身份；登记失败时不得派发。
type BrowserAttemptObserver func(context.Context, string, string) (context.Context, func(*skill.Result, error) error, error)

type BrowserSkillOption func(*BrowserSkill)

func WithBrowserArtifactWriter(writer BrowserArtifactWriter) BrowserSkillOption {
	return func(s *BrowserSkill) { s.artifactWriter = writer }
}

func WithBrowserFileOpener(opener BrowserFileOpener) BrowserSkillOption {
	return func(s *BrowserSkill) { s.fileOpener = opener }
}

func WithBrowserAuthResolver(resolver BrowserAuthResolver) BrowserSkillOption {
	return func(s *BrowserSkill) { s.authResolver = resolver }
}

func WithBrowserAttemptObserver(observer BrowserAttemptObserver) BrowserSkillOption {
	return func(s *BrowserSkill) { s.attemptObserver = observer }
}

// SetArtifactWriter 在启动装配阶段接通已注册实例的产物存储，不与 Execute 并发调用。
func (s *BrowserSkill) SetArtifactWriter(writer BrowserArtifactWriter) {
	s.artifactWriter = writer
}

// SetFileOpener 在启动装配阶段接通本轮权限与附件解析，不与 Execute 并发调用。
func (s *BrowserSkill) SetFileOpener(opener BrowserFileOpener) {
	s.fileOpener = opener
}

// SetAuthResolver 在启动装配阶段接通既有服务认证，不与 Execute 并发调用。
func (s *BrowserSkill) SetAuthResolver(resolver BrowserAuthResolver) {
	s.authResolver = resolver
}

// SetAttemptObserver 在启动装配阶段接通物理尝试回执，不与 Execute 并发调用。
func (s *BrowserSkill) SetAttemptObserver(observer BrowserAttemptObserver) {
	s.attemptObserver = observer
}

func (s *BrowserSkill) beginHTTPAttempt(ctx context.Context, method, target string) (context.Context, func(*skill.Result, error) error, error) {
	if s.attemptObserver != nil {
		attemptCtx, done, err := s.attemptObserver(ctx, method, target)
		if attemptCtx == nil {
			attemptCtx = ctx
		}
		if done == nil {
			done = func(*skill.Result, error) error { return nil }
		}
		return attemptCtx, done, err
	}
	return ctx, func(*skill.Result, error) error { return nil }, nil
}

func completeHTTPAttempt(result *skill.Result, resultErr error, done func(*skill.Result, error) error) (*skill.Result, error) {
	if finishErr := done(result, resultErr); finishErr != nil {
		detail := skill.ToolExecutionDetail{Outcome: "failure"}
		if result != nil && result.Detail != nil {
			detail = *result.Detail
		} else {
			var executionErr *skill.ExecutionError
			if errors.As(resultErr, &executionErr) {
				detail = executionErr.Detail
			}
		}
		if detail.Outcome == "" || detail.Outcome == "success" {
			detail.Outcome = "failure"
		}
		detail.Phase, detail.Reason = "persist_receipt", "receipt_store"
		if result == nil {
			result = &skill.Result{}
		}
		result.Detail = &detail
		return result, &skill.ExecutionError{Detail: detail, Err: &httpPublicError{phase: "persist_receipt", reason: "receipt_store", cause: errors.Join(resultErr, finishErr)}}
	}
	return result, resultErr
}

func (s *BrowserSkill) doRequest(ctx context.Context, target string, args map[string]any) (result *skill.Result, resultErr error) {
	method := strings.ToUpper(strings.TrimSpace(stringHTTPArg(args, "method")))
	if method == "" {
		method = http.MethodGet
	}
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
	default:
		return nil, fmt.Errorf("unsupported HTTP method")
	}
	parsed, err := url.Parse(target)
	if err != nil || parsed.Hostname() == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, fmt.Errorf("invalid HTTP URL")
	}
	query := parsed.Query()
	if rawQuery, exists := args["query"]; exists {
		pairs, err := httpQueryPairs(rawQuery)
		if err != nil {
			return nil, err
		}
		for _, pair := range pairs {
			query.Add(pair[0], pair[1])
		}
	}
	parsed.RawQuery = query.Encode()
	body, contentType, cleanup, err := s.requestBody(ctx, args)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	req, err := http.NewRequestWithContext(ctx, method, parsed.String(), body)
	if err != nil {
		return nil, fmt.Errorf("invalid HTTP request")
	}
	req.Header.Set("User-Agent", "HexClaw/1.0")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if err := applyHTTPHeaders(req.Header, args["headers"]); err != nil {
		return nil, err
	}
	switch stringHTTPArg(args, "body_kind") {
	case "file":
		req.GetBody = func() (io.ReadCloser, error) { return s.openHTTPRequestFile(ctx, stringHTTPArg(args, "file")) }
	case "multipart":
		if file, ok := body.(*os.File); ok {
			req.GetBody = func() (io.ReadCloser, error) { return os.Open(file.Name()) }
		}
	}
	authSource, err := s.authenticateHTTPRequest(ctx, req, args)
	if err != nil {
		return nil, err
	}
	initialOrigin := httpOrigin(req.URL)
	resp, req, done, err := s.dispatchHTTPRequest(req)
	defer func() { result, resultErr = completeHTTPAttempt(result, resultErr, done) }()
	if err != nil {
		var failure *skill.ExecutionError
		if errors.As(err, &failure) {
			facts := map[string]any{"method": req.Method, "url": safeHTTPRequestURL(req.URL), "response_complete": false, "auth_source": authSource}
			if replay := httpTransportReplayFacts(err); replay != nil {
				facts["transport_replay"] = replay
			}
			return &skill.Result{Detail: &failure.Detail, Data: facts}, err
		}
		return nil, err
	}
	defer resp.Body.Close()
	if httpOrigin(req.URL) != initialOrigin {
		authSource = "redirect_target"
	}
	return s.readAPIResponse(req.Context(), req, resp, authSource)
}

func stringHTTPArg(args map[string]any, name string) string {
	value, _ := args[name].(string)
	return value
}

func httpQueryPairs(raw any) ([][2]string, error) {
	var result [][2]string
	switch values := raw.(type) {
	case []any:
		for _, item := range values {
			pair, ok := item.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("query entries must contain name and value")
			}
			name, nameOK := pair["name"].(string)
			value, valueOK := pair["value"].(string)
			if !nameOK || !valueOK {
				return nil, fmt.Errorf("query name and value must be strings")
			}
			result = append(result, [2]string{name, value})
		}
	case map[string]any:
		for name, rawValue := range values {
			for _, value := range httpFieldValues(rawValue) {
				result = append(result, [2]string{name, value})
			}
		}
	case nil:
	default:
		return nil, fmt.Errorf("query must be an array of name/value pairs")
	}
	return result, nil
}

func httpFieldValues(raw any) []string {
	switch values := raw.(type) {
	case []any:
		out := make([]string, 0, len(values))
		for _, value := range values {
			out = append(out, fmt.Sprint(value))
		}
		return out
	case []string:
		return values
	default:
		return []string{fmt.Sprint(raw)}
	}
}

func applyHTTPHeaders(header http.Header, raw any) error {
	if raw == nil {
		return nil
	}
	headers, ok := raw.(map[string]any)
	if !ok {
		return fmt.Errorf("headers must be an object")
	}
	for name, value := range headers {
		header.Del(name)
		for _, text := range httpFieldValues(value) {
			header.Add(name, text)
		}
	}
	return nil
}

func (s *BrowserSkill) requestBody(ctx context.Context, args map[string]any) (io.Reader, string, func(), error) {
	kind := strings.ToLower(stringHTTPArg(args, "body_kind"))
	contentType := stringHTTPArg(args, "content_type")
	cleanup := func() {}
	var reader io.Reader
	var defaultType string
	switch kind {
	case "", "none":
		return nil, contentType, cleanup, nil
	case "json":
		value, exists := args["json"]
		if !exists {
			return nil, "", cleanup, fmt.Errorf("json body is required")
		}
		data, err := json.Marshal(value)
		if err != nil {
			return nil, "", cleanup, fmt.Errorf("invalid JSON body")
		}
		reader, defaultType = bytes.NewReader(data), "application/json"
	case "text":
		value, ok := args["text"].(string)
		if !ok {
			return nil, "", cleanup, fmt.Errorf("text body is required")
		}
		reader, defaultType = strings.NewReader(value), "text/plain; charset=utf-8"
	case "form":
		fields, err := httpFormValues(args["form"])
		if err != nil {
			return nil, "", cleanup, err
		}
		reader, defaultType = strings.NewReader(fields.Encode()), "application/x-www-form-urlencoded"
	case "file":
		file, err := s.openHTTPRequestFile(ctx, stringHTTPArg(args, "file"))
		if err != nil {
			return nil, "", cleanup, err
		}
		reader, defaultType = file, "application/octet-stream"
		cleanup = func() { _ = file.Close() }
	case "multipart":
		if contentType != "" {
			return nil, "", cleanup, fmt.Errorf("multipart content type is generated with its boundary")
		}
		return s.multipartRequestBody(ctx, args)
	default:
		return nil, "", cleanup, fmt.Errorf("unsupported HTTP body_kind")
	}
	if contentType == "" {
		contentType = defaultType
	}
	return reader, contentType, cleanup, nil
}

func httpFormValues(raw any) (url.Values, error) {
	values := url.Values{}
	if raw == nil {
		return values, nil
	}
	fields, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("form must be an object")
	}
	for name, value := range fields {
		for _, text := range httpFieldValues(value) {
			values.Add(name, text)
		}
	}
	return values, nil
}

func (s *BrowserSkill) openHTTPRequestFile(ctx context.Context, path string) (io.ReadCloser, error) {
	if path == "" {
		return nil, fmt.Errorf("input file reference is required")
	}
	if s.fileOpener == nil {
		return nil, fmt.Errorf("HTTP input file resolver is unavailable")
	}
	file, err := s.fileOpener(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("HTTP input file could not be opened")
	}
	return file, nil
}

func (s *BrowserSkill) multipartRequestBody(ctx context.Context, args map[string]any) (io.Reader, string, func(), error) {
	// 先准备可关闭的临时流，避免网络提前失败留下阻塞的 pipe 写入协程。
	file, err := os.CreateTemp("", "hexclaw-http-multipart-*")
	if err != nil {
		return nil, "", func() {}, fmt.Errorf("prepare multipart body failed")
	}
	cleanup := func() { _ = file.Close(); _ = os.Remove(file.Name()) }
	fail := func(err error) (io.Reader, string, func(), error) { cleanup(); return nil, "", func() {}, err }
	writer := multipart.NewWriter(file)
	fields, err := httpFormValues(args["form"])
	if err != nil {
		return fail(err)
	}
	for name, values := range fields {
		for _, value := range values {
			if err := writer.WriteField(name, value); err != nil {
				return fail(fmt.Errorf("prepare multipart field failed"))
			}
		}
	}
	if raw, exists := args["files"]; exists {
		files, ok := raw.([]any)
		if !ok {
			return fail(fmt.Errorf("files must be an array"))
		}
		for _, rawFile := range files {
			input, ok := rawFile.(map[string]any)
			if !ok || stringHTTPArg(input, "name") == "" {
				return fail(fmt.Errorf("each multipart file needs a name and path"))
			}
			source, err := s.openHTTPRequestFile(ctx, stringHTTPArg(input, "path"))
			if err != nil {
				return fail(err)
			}
			name := stringHTTPArg(input, "filename")
			if name == "" {
				name = filepath.Base(stringHTTPArg(input, "path"))
			}
			header := textproto.MIMEHeader{}
			header.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{"name": stringHTTPArg(input, "name"), "filename": name}))
			contentType := stringHTTPArg(input, "content_type")
			if contentType == "" {
				contentType = "application/octet-stream"
			}
			header.Set("Content-Type", contentType)
			part, err := writer.CreatePart(header)
			if err == nil {
				_, err = io.Copy(part, &httpContextReader{ctx: ctx, reader: source})
			}
			closeErr := source.Close()
			if err != nil || closeErr != nil {
				return fail(fmt.Errorf("prepare multipart file failed"))
			}
		}
	}
	if err := writer.Close(); err != nil {
		return fail(fmt.Errorf("complete multipart body failed"))
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return fail(fmt.Errorf("rewind multipart body failed"))
	}
	return file, writer.FormDataContentType(), cleanup, nil
}

type httpContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *httpContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func (s *BrowserSkill) authenticateHTTPRequest(ctx context.Context, req *http.Request, args map[string]any) (string, error) {
	reference := stringHTTPArg(args, "auth_ref")
	explicit, provided, err := parseHTTPRequestAuth(args["auth"])
	if err != nil {
		return "", err
	}
	var resolved HTTPRequestAuth
	if s.authResolver != nil && (reference != "" || !provided && req.Header.Get("Authorization") == "") {
		var err error
		resolved, err = s.authResolver(ctx, req.URL.String(), reference)
		if err != nil {
			return "", fmt.Errorf("HTTP authentication reference is unavailable or needs authorization")
		}
	} else if reference != "" && s.authResolver == nil {
		return "", fmt.Errorf("HTTP authentication reference resolver is unavailable")
	}
	override := stringHTTPArg(args, "auth_override")
	if override != "" && override != "explicit" && override != "reference" {
		return "", fmt.Errorf("auth_override must be explicit or reference")
	}
	if provided && resolved.Type != "" && resolved.Type != "none" {
		if !sameHTTPRequestAuth(explicit, resolved) && override == "" {
			return "", fmt.Errorf("conflicting authentication sources; clarify explicit or reference before sending")
		}
		if override != "reference" {
			resolved = explicit
		}
	} else if provided {
		resolved = explicit
	}
	if resolved.Source == "" {
		if provided && override != "reference" {
			resolved.Source = "task"
		} else if reference != "" {
			resolved.Source = reference
		} else if req.Header.Get("Authorization") != "" {
			resolved.Source = "task_headers"
		} else {
			resolved.Source = "none"
		}
	}
	if reference == "" && !provided && override == "" {
		// 目标服务的默认绑定只补齐未明确提供的认证；不能覆盖任务自身的 Header/Query。
		override = "explicit"
	}
	if err := applyHTTPRequestAuth(req, resolved, override); err != nil {
		return "", err
	}
	rememberHTTPRequestAuth(req, resolved)
	return resolved.Source, nil
}

func parseHTTPRequestAuth(raw any) (HTTPRequestAuth, bool, error) {
	if raw == nil {
		return HTTPRequestAuth{}, false, nil
	}
	args, ok := raw.(map[string]any)
	if !ok {
		return HTTPRequestAuth{}, true, fmt.Errorf("auth must be an object")
	}
	auth := HTTPRequestAuth{Type: stringHTTPArg(args, "type"), Token: stringHTTPArg(args, "token"), Username: stringHTTPArg(args, "username"), Password: stringHTTPArg(args, "password"), In: stringHTTPArg(args, "in"), Name: stringHTTPArg(args, "name"), Value: stringHTTPArg(args, "value"), Source: "task"}
	return auth, true, nil
}

func sameHTTPRequestAuth(a, b HTTPRequestAuth) bool {
	a.Source, b.Source = "", ""
	return a == b
}

func applyHTTPRequestAuth(req *http.Request, auth HTTPRequestAuth, override string) error {
	var headerName, value string
	switch strings.ToLower(auth.Type) {
	case "", "none":
		return nil
	case "bearer", "oauth":
		if auth.Token == "" {
			return fmt.Errorf("authentication token is missing")
		}
		headerName, value = "Authorization", "Bearer "+auth.Token
	case "basic":
		clone := req.Clone(req.Context())
		clone.SetBasicAuth(auth.Username, auth.Password)
		headerName, value = "Authorization", clone.Header.Get("Authorization")
	case "api_key":
		if auth.Name == "" || auth.Value == "" {
			return fmt.Errorf("API key name and value are required")
		}
		if auth.In == "query" {
			query := req.URL.Query()
			if existing, exists := query[auth.Name]; exists && (len(existing) != 1 || existing[0] != auth.Value) && override == "" {
				return fmt.Errorf("conflicting authentication query parameter; clarify the intended source")
			}
			if override != "explicit" || len(query[auth.Name]) == 0 {
				query.Set(auth.Name, auth.Value)
				req.URL.RawQuery = query.Encode()
			}
			return nil
		}
		if auth.In != "header" {
			return fmt.Errorf("API key in must be header or query")
		}
		headerName, value = auth.Name, auth.Value
	default:
		return fmt.Errorf("unsupported authentication type")
	}
	if existing := req.Header.Values(headerName); len(existing) > 0 {
		if (len(existing) != 1 || existing[0] != value) && override == "" {
			return fmt.Errorf("conflicting authentication header; clarify the intended source")
		}
		if override == "explicit" {
			return nil
		}
	}
	req.Header.Set(headerName, value)
	return nil
}

func safeHTTPRequestURL(target *url.URL) string {
	copy := *target
	copy.User = nil
	copy.Fragment = ""
	query := copy.Query()
	for key := range query {
		query[key] = []string{"[redacted]"}
	}
	copy.RawQuery = query.Encode()
	return copy.String()
}

type httpResponseCapture struct {
	reader  io.Reader
	preview bytes.Buffer
	bytes   int64
	readErr error
	eof     bool
}

type httpArtifactPersistenceError struct{ cause error }

func (e *httpArtifactPersistenceError) Error() string {
	return "HTTP response received but its artifact could not be preserved"
}

func (e *httpArtifactPersistenceError) Unwrap() error { return e.cause }

func (r *httpResponseCapture) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.bytes += int64(n)
	if remain := (64 << 10) - r.preview.Len(); remain > 0 {
		_, _ = r.preview.Write(p[:min(n, remain)])
	}
	if errors.Is(err, io.EOF) {
		r.eof = true
	} else if err != nil {
		r.readErr = err
	}
	return n, err
}

func (s *BrowserSkill) readAPIResponse(ctx context.Context, req *http.Request, resp *http.Response, authSource string) (*skill.Result, error) {
	detail := httpExecutionDetail(req.URL.String(), strings.ToLower(req.Method), resp)
	contentType := resp.Header.Get("Content-Type")
	mediaType, _, _ := mime.ParseMediaType(contentType)
	textual := strings.HasPrefix(mediaType, "text/") || strings.Contains(mediaType, "json") || strings.Contains(mediaType, "xml") || mediaType == "" && resp.ContentLength == 0
	capture := &httpResponseCapture{reader: resp.Body}
	data := map[string]any{"method": req.Method, "url": safeHTTPRequestURL(req.URL), "status": resp.StatusCode, "headers": responseHTTPHeaders(resp.Header), "auth_source": authSource, "business_success": "not_evaluated"}
	result := &skill.Result{Data: data, Metadata: map[string]string{"url": safeHTTPRequestURL(req.URL), "method": req.Method, "status": strconv.Itoa(resp.StatusCode), "content_type": contentType}, Detail: &detail}
	var persistErr error
	if s.artifactWriter != nil {
		// 先区分无正文响应与待保存文件；缓冲在 capture 外，文件保存仍能核对实际消费字节。
		buffered := bufio.NewReader(resp.Body)
		capture.reader = buffered
		_, peekErr := buffered.Peek(1)
		disposition, dispositionParams, _ := mime.ParseMediaType(resp.Header.Get("Content-Disposition"))
		explicitFile := disposition == "attachment" || dispositionParams["filename"] != ""
		bodyForbidden := req.Method == http.MethodHead || resp.StatusCode == http.StatusNoContent ||
			resp.StatusCode == http.StatusResetContent || resp.StatusCode == http.StatusNotModified
		if errors.Is(peekErr, io.EOF) && (!explicitFile || bodyForbidden) {
			capture.eof = true
		} else if peekErr != nil && !errors.Is(peekErr, io.EOF) {
			capture.readErr = peekErr
		} else {
			name := httpResponseFilename(req.URL, resp.Header, textual)
			ref, err := s.artifactWriter(ctx, name, contentType, capture)
			persistErr = err
			if err == nil {
				if ref == nil {
					persistErr = fmt.Errorf("response artifact reference is missing")
				} else {
					data["artifact"] = ref
				}
			} else if len(ref) > 0 {
				data["pending_artifact"] = ref
			}
		}
		if !capture.eof && capture.readErr == nil {
			before := capture.bytes
			_, err := io.Copy(io.Discard, capture)
			if persistErr == nil && (err != nil || capture.bytes != before) {
				persistErr = fmt.Errorf("artifact writer did not retain the complete response")
			}
		}
	} else {
		body, err := io.ReadAll(io.LimitReader(capture, maxBodySize+1))
		if err == nil && len(body) <= maxBodySize && textual {
			data["body"] = string(body)
		} else if err == nil && len(body) > 0 {
			persistErr = fmt.Errorf("response artifact storage is required for binary or oversized content")
		}
		if len(body) > maxBodySize {
			capture.eof = false
		}
	}
	data["bytes_read"] = capture.bytes
	data["response_complete"] = capture.eof && capture.readErr == nil
	if mediaType == "" {
		detected := http.DetectContentType(capture.preview.Bytes())
		textual = capture.bytes == 0 || strings.HasPrefix(detected, "text/")
		if json.Valid(capture.preview.Bytes()) && capture.eof && capture.bytes == int64(capture.preview.Len()) {
			textual, mediaType = true, "application/json"
		}
	}
	if textual {
		preview := strings.ToValidUTF8(capture.preview.String(), "")
		result.Content = preview
		data["preview_truncated"] = capture.bytes > int64(capture.preview.Len())
		if capture.eof && capture.bytes <= int64(capture.preview.Len()) && strings.Contains(mediaType, "json") {
			var value any
			if json.Unmarshal(capture.preview.Bytes(), &value) == nil {
				data["json"] = value
			}
		}
	} else {
		result.Content = fmt.Sprintf("HTTP %d; binary response (%d bytes).", resp.StatusCode, capture.bytes)
	}
	if capture.readErr != nil || !capture.eof {
		readErr := capture.readErr
		if readErr == nil {
			readErr = io.ErrUnexpectedEOF
		}
		failure := httpExecutionError(detail, "read_response", readErr)
		result.Detail = &failure.Detail
		return result, failure
	}
	if persistErr != nil {
		detail.Phase, detail.Outcome, detail.Reason = "persist_result", "failure", "artifact_store"
		result.Detail = &detail
		return result, &skill.ExecutionError{Detail: detail, Err: &httpArtifactPersistenceError{cause: persistErr}}
	}
	return completeHTTPResult(result, detail)
}

func responseHTTPHeaders(header http.Header) map[string]string {
	result := map[string]string{}
	for _, name := range []string{"Content-Type", "Content-Length", "Content-Disposition", "ETag", "Last-Modified", "Retry-After", "X-Request-ID", "Request-ID"} {
		if value := header.Get(name); value != "" {
			result[name] = value
		}
	}
	return result
}

func httpResponseFilename(target *url.URL, header http.Header, textual bool) string {
	_, params, _ := mime.ParseMediaType(header.Get("Content-Disposition"))
	name := filepath.Base(params["filename"])
	if name == "" || name == "." || name == string(filepath.Separator) {
		name = filepath.Base(target.Path)
	}
	if name == "" || name == "." || name == string(filepath.Separator) {
		name = "http-response.bin"
		if textual {
			name = "http-response.txt"
		}
	}
	if !utf8.ValidString(name) {
		name = "http-response.bin"
	}
	return name
}

func isHTTPWriteMethod(method string) bool {
	switch strings.ToUpper(method) {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

func httpDefinitelyNotDispatched(err error) bool {
	var target *url.Error
	if errors.As(err, &target) {
		err = target.Err
	}
	// net.OpError 的 Op=dial 由标准传输明确报告，其他自定义传输保守保持未知。
	var netOp *net.OpError
	return errors.As(err, &netOp) && netOp.Op == "dial"
}
