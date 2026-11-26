package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lin9527ya/Norma/llm"
	"github.com/lin9527ya/Norma/permission"
	actool "github.com/lin9527ya/Norma/tool"
	"github.com/lin9527ya/blade/mcphttp"
)

const (
	stdioDialTimeout   = 30 * time.Second
	stdioStderrCapByte = 4096
)

type stdioStderrRing struct {
	mu  sync.Mutex
	buf []byte
}

func (r *stdioStderrRing) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(p) >= stdioStderrCapByte {
		r.buf = append(r.buf[:0], p[len(p)-stdioStderrCapByte:]...)
		return len(p), nil
	}
	r.buf = append(r.buf, p...)
	if len(r.buf) > stdioStderrCapByte {
		excess := len(r.buf) - stdioStderrCapByte
		copy(r.buf, r.buf[excess:])
		r.buf = r.buf[:stdioStderrCapByte]
	}
	return len(p), nil
}

func (r *stdioStderrRing) Summary() string {
	r.mu.Lock()
	s := strings.TrimSpace(string(r.buf))
	r.mu.Unlock()
	if s == "" {
		return ""
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	if len(s) > 600 {
		s = "…" + s[len(s)-600:]
	}
	return s
}

func isReservedHostEnvKey(key string) bool {
	up := strings.ToUpper(strings.TrimSpace(key))
	if up == "" {
		return true
	}
	if agentEnvOverrideKeys[up] {
		return true
	}
	for _, p := range []string{"BLADE_", "ANTHROPIC_", "OPENAI_", "CLAUDE_"} {
		if strings.HasPrefix(up, p) {
			return true
		}
	}
	return false
}

// buildHardenedStdioCmd prepares a stdio MCP subprocess:
//  1. Runs actool.ApplyExecHook(cmd) first on the parent environment so isolation
//     mode drops privileges and scrubs host secrets (BLADE_*/OPENAI_*/etc.).
//  2. Overlays explicit per-MCP env vars (mcp_servers.env) afterward — excluding
//     reserved BLADE/LLM/identity keys — so legitimate tokens configured for an
//     MCP server (e.g. GITHUB_PERSONAL_ACCESS_TOKEN, SHODAN_API_KEY) survive.
func buildHardenedStdioCmd(command string, args []string, explicitEnv map[string]string, stderr io.Writer) *exec.Cmd {
	cmd := exec.Command(command, args...)
	cmd.Stderr = stderr
	actool.ApplyExecHook(cmd)
	if len(explicitEnv) > 0 {
		base := cmd.Env
		if base == nil {
			base = os.Environ()
		}
		merged := make(map[string]string, len(base)+len(explicitEnv))
		order := make([]string, 0, len(base)+len(explicitEnv))
		for _, kv := range base {
			if i := strings.IndexByte(kv, '='); i > 0 {
				k := kv[:i]
				if _, seen := merged[k]; !seen {
					order = append(order, k)
				}
				merged[k] = kv
			}
		}
		extraKeys := make([]string, 0, len(explicitEnv))
		for k := range explicitEnv {
			if k = strings.TrimSpace(k); k != "" && !isReservedHostEnvKey(k) {
				extraKeys = append(extraKeys, k)
			}
		}
		sort.Strings(extraKeys)
		for _, k := range extraKeys {
			if _, seen := merged[k]; !seen {
				order = append(order, k)
			}
			merged[k] = k + "=" + explicitEnv[k]
		}
		out := make([]string, 0, len(order))
		for _, k := range order {
			out = append(out, merged[k])
		}
		cmd.Env = out
	}
	return cmd
}

type stdioRPCRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type stdioRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *stdioRPCError  `json:"error"`
}

type stdioRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *stdioRPCError) Error() string { return fmt.Sprintf("mcp rpc error %d: %s", e.Code, e.Message) }

type stdioMCPClient struct {
	server    string
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	stderr    *stdioStderrRing
	mu        sync.Mutex
	nextID    int
	pending   map[int]chan stdioRPCResponse
	readDone  chan struct{}
	procDone  chan struct{}
	readErr   error
	waitErr   error
	closeOnce sync.Once
}

func dialStdioMCP(ctx context.Context, server, command string, env map[string]string, args ...string) (*stdioMCPClient, error) {
	release, record, err := mcphttp.AcquireGuard(ctx, server)
	if err != nil {
		return nil, err
	}
	defer release()

	stderr := &stdioStderrRing{}
	cmd := buildHardenedStdioCmd(command, args, env, stderr)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		record(err, ctx.Err() != nil)
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		record(err, ctx.Err() != nil)
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		record(err, ctx.Err() != nil)
		return nil, fmt.Errorf("stdio 启动失败 (%s): %w", command, err)
	}

	c := &stdioMCPClient{
		server:   server,
		cmd:      cmd,
		stdin:    stdin,
		stderr:   stderr,
		pending:  make(map[int]chan stdioRPCResponse),
		readDone: make(chan struct{}),
		procDone: make(chan struct{}),
	}
	go c.readLoop(stdout)
	go func() {
		c.waitErr = cmd.Wait()
		close(c.procDone)
	}()

	initCtx, cancel := withBoundedDialTimeout(ctx, stdioDialTimeout)
	defer cancel()
	if err := c.initialize(initCtx); err != nil {
		_ = c.Close()
		record(err, ctx.Err() != nil)
		return nil, err
	}
	record(nil, false)
	return c, nil
}

func withBoundedDialTimeout(ctx context.Context, max time.Duration) (context.Context, context.CancelFunc) {
	if dl, ok := ctx.Deadline(); ok && time.Until(dl) <= max {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, max)
}

func (c *stdioMCPClient) readLoop(r io.Reader) {
	defer close(c.readDone)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var resp stdioRPCResponse
		if err := json.Unmarshal(line, &resp); err != nil || resp.ID == 0 {
			continue
		}
		c.mu.Lock()
		ch := c.pending[resp.ID]
		delete(c.pending, resp.ID)
		c.mu.Unlock()
		if ch != nil {
			ch <- resp
		}
	}
	c.readErr = sc.Err()
}

func (c *stdioMCPClient) closedErr() error {
	msg := fmt.Sprintf("mcp: connection closed (%s)", c.server)
	if c.stderr != nil {
		if tail := c.stderr.Summary(); tail != "" {
			msg += "; stderr: " + tail
		}
	}
	return errors.New(msg)
}

func (c *stdioMCPClient) Alive() bool {
	if c == nil {
		return false
	}
	select {
	case <-c.readDone:
		return false
	case <-c.procDone:
		return false
	default:
		return true
	}
}

func (c *stdioMCPClient) Close() error {
	c.closeOnce.Do(func() {
		if c.stdin != nil {
			_ = c.stdin.Close()
		}
		if c.cmd != nil && c.cmd.Process != nil {
			_ = c.cmd.Process.Kill()
		}
		select {
		case <-c.procDone:
		case <-time.After(2 * time.Second):
		}
		select {
		case <-c.readDone:
		case <-time.After(500 * time.Millisecond):
		}
	})
	return nil
}

func (c *stdioMCPClient) writeFrame(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = c.stdin.Write(data)
	return err
}

func (c *stdioMCPClient) rawCall(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	ch := make(chan stdioRPCResponse, 1)
	c.pending[id] = ch
	werr := c.writeFrame(stdioRPCRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params})
	if werr != nil {
		delete(c.pending, id)
	}
	c.mu.Unlock()
	if werr != nil {
		return nil, fmt.Errorf("%s: %w", c.closedErr().Error(), werr)
	}
	select {
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, ctx.Err()
	case <-c.readDone:
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, c.closedErr()
	case resp := <-ch:
		if resp.Error != nil {
			return nil, resp.Error
		}
		return resp.Result, nil
	}
}

func (c *stdioMCPClient) notify(method string, params any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.writeFrame(stdioRPCRequest{JSONRPC: "2.0", Method: method, Params: params})
}

func (c *stdioMCPClient) initialize(ctx context.Context) error {
	if _, err := c.rawCall(ctx, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "blade", "version": "0.2"},
	}); err != nil {
		return err
	}
	return c.notify("notifications/initialized", map[string]any{})
}

func (c *stdioMCPClient) callGuarded(ctx context.Context, method string, params any) (json.RawMessage, error) {
	release, record, err := mcphttp.AcquireGuard(ctx, c.server)
	if err != nil {
		return nil, err
	}
	defer release()

	raw, callErr := c.rawCall(ctx, method, params)
	if callErr != nil {
		var rpcErr *stdioRPCError
		if errors.As(callErr, &rpcErr) {
			record(nil, false) // server replied cleanly with JSON-RPC error -> transport healthy
		} else {
			record(callErr, ctx.Err() != nil || errors.Is(callErr, context.Canceled) || errors.Is(callErr, context.DeadlineExceeded))
		}
		return nil, callErr
	}
	record(nil, false)
	return raw, nil
}

func (c *stdioMCPClient) Tools(ctx context.Context) ([]actool.CoreTool, error) {
	raw, err := c.callGuarded(ctx, "tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var res struct {
		Tools []struct {
			Name        string         `json:"name"`
			Description string         `json:"description"`
			InputSchema map[string]any `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, err
	}
	out := make([]actool.CoreTool, 0, len(res.Tools))
	for _, rt := range res.Tools {
		rt := rt
		schema := rt.InputSchema
		if schema == nil {
			schema = map[string]any{"type": "object"}
		}
		full := mcphttp.ToolName(c.server, rt.Name)
		out = append(out, actool.Build(actool.Spec{
			Name:        full,
			Description: rt.Description,
			Schema:      schema,
			Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
				return permission.AskUser("call MCP tool " + full + "?")
			},
			Run: func(ctx context.Context, in json.RawMessage, tc *actool.ToolContext) (actool.Result, error) {
				var args any
				if len(in) > 0 {
					_ = json.Unmarshal(in, &args)
				}
				raw, err := c.callGuarded(ctx, "tools/call", map[string]any{"name": rt.Name, "arguments": args})
				if err != nil {
					return actool.Errorf("Error: " + err.Error()), nil
				}
				var callRes struct {
					Content []struct {
						Type string `json:"type"`
						Text string `json:"text"`
					} `json:"content"`
					IsError bool `json:"isError"`
				}
				if err := json.Unmarshal(raw, &callRes); err != nil {
					return actool.Errorf("Error: bad MCP response: " + err.Error()), nil
				}
				var text strings.Builder
				for _, blk := range callRes.Content {
					text.WriteString(blk.Text)
				}
				return actool.Result{
					Content: []llm.ContentBlock{llm.TextBlock(actool.Capture(tc, text.String()))},
					IsError: callRes.IsError,
				}, nil
			},
		}))
	}
	return out, nil
}
