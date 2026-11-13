// Package mcpserver exposes the tools registry as an MCP server.
//
// The registry in internal/tools is the single source of truth: this adapter
// copies each entry's name, description, JSON schema and annotations without
// redefining anything, so the in-process agent and external MCP clients see
// the same tools.
//
// Identity is never global. Every call resolves the caller through the
// UserResolver supplied by the command and passes it to the handler as an
// explicit tools.UserContext.
package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"unichatgo/internal/tools"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Version is the server implementation version advertised to MCP clients.
const Version = "2.0.0"

// Identity is the account and conversation an MCP call acts as. A stdio
// session has no conversation of its own, so the command resolves a dedicated
// one; drafts need a real session_id for the foreign key and for UI review.
type Identity struct {
	UserID    int64
	SessionID int64
}

// UserResolver returns the identity an MCP session acts as. It must not read
// any global state; the command supplies it (for example from --user-id).
type UserResolver func(ctx context.Context) (Identity, error)

// New builds an MCP server that registers every tool of the registry with the
// same name, description and input schema.
func New(registry *tools.Registry, resolve UserResolver) (*mcp.Server, error) {
	if registry == nil {
		return nil, errors.New("mcpserver: tool registry is required")
	}
	if resolve == nil {
		return nil, errors.New("mcpserver: user resolver is required")
	}
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "unifinsg",
		Title:   "UniFinSG",
		Version: Version,
	}, nil)
	for _, tool := range registry.List() {
		server.AddTool(mcpTool(tool), toolHandler(tool, resolve))
	}
	return server, nil
}

// mcpTool copies one registry entry. The MCP hints mirror Tool.ReadOnly and
// Tool.Destructive so clients can tell reads from writes.
func mcpTool(tool tools.Tool) *mcp.Tool {
	destructive := tool.Destructive
	openWorld := false
	return &mcp.Tool{
		Name:        tool.Name,
		Description: tool.Description,
		InputSchema: tool.InputSchema,
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    tool.ReadOnly,
			DestructiveHint: &destructive,
			OpenWorldHint:   &openWorld,
		},
	}
}

// toolHandler adapts one registry handler to the MCP calling convention.
func toolHandler(tool tools.Tool, resolve UserResolver) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		identity, err := resolve(ctx)
		if err != nil {
			return nil, fmt.Errorf("resolve MCP user: %w", err)
		}
		if identity.UserID <= 0 {
			return nil, errors.New("resolve MCP user: a positive user id is required")
		}
		// The Go SDK does not expose the JSON-RPC request id to handlers, so
		// each call gets its own identifier; drafts use it as tool_call_id.
		requestID := newRequestID()
		userContext := tools.UserContext{
			UserID:     identity.UserID,
			SessionID:  identity.SessionID,
			TurnID:     strings.ReplaceAll(requestID, "-", ""),
			ToolCallID: "mcp-" + requestID,
		}
		result, err := tool.Handler(ctx, userContext, req.Params.Arguments)
		if err != nil {
			// Invalid arguments are a protocol error: the client asked for
			// something the schema does not allow and can correct the call.
			var argError *tools.ArgError
			if errors.As(err, &argError) {
				return nil, fmt.Errorf("%s: %s", tool.Name, argError.Message)
			}
			// Execution failures are reported as tool errors so the session
			// stays usable and the caller can see what went wrong.
			return errorResult(err), nil
		}
		payload, err := json.Marshal(result)
		if err != nil {
			return errorResult(fmt.Errorf("encode %s result: %w", tool.Name, err)), nil
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: string(payload)}},
		}, nil
	}
}

// errorResult turns a handler failure into an MCP tool error instead of
// letting it escape as a panic or a broken connection.
func errorResult(err error) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}},
	}
}

func newRequestID() string { return uuid.NewString() }
