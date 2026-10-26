package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type gatewayRoute struct {
	snapshot *snapshot
	original string
}

// Each Gateway session owns a catalogue, but connection generations belong to
// the running instances. Reconcile before dispatch and list, without replaying
// a tool request when its transport outcome is unknown.
type gatewayCatalog struct {
	mu         sync.Mutex
	app        *App
	server     *mcp.Server
	credential credential
	lease      *gatewayLease
	routes     map[string]gatewayRoute
}

func (c *gatewayCatalog) refresh() map[string]gatewayRoute {
	c.mu.Lock()
	defer c.mu.Unlock()
	next := map[string]gatewayRoute{}
	c.app.Runtime.Range(func(_, value any) bool {
		snap := value.(*snapshot)
		for _, tool := range snap.tools {
			name := routeName(snap.instance.ID, tool.Name)
			route := gatewayRoute{snapshot: snap, original: tool.Name}
			next[name] = route
			if previous, ok := c.routes[name]; ok && previous.snapshot == snap {
				continue
			}
			copyTool := *tool
			copyTool.Name = name
			copyTool.Description = fmt.Sprintf("[%s] %s", snap.instance.Name, tool.Description)
			c.server.AddTool(&copyTool, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return c.app.call(ctx, c.credential, route.snapshot, c.lease, route.original, req.Params.Arguments)
			})
		}
		return true
	})
	var removed []string
	for name := range c.routes {
		if _, exists := next[name]; !exists {
			removed = append(removed, name)
		}
	}
	if len(removed) != 0 {
		c.server.RemoveTools(removed...)
	}
	// Publish an immutable map for the request middleware. Concurrent refreshes
	// replace it rather than mutating a map another request is reading.
	c.routes = next
	return next
}

func sameGatewayTool(registered, current *snapshot, name string) bool {
	var before, after *mcp.Tool
	for _, tool := range registered.tools {
		if tool.Name == name {
			before = tool
			break
		}
	}
	for _, tool := range current.tools {
		if tool.Name == name {
			after = tool
			break
		}
	}
	if before == nil || after == nil {
		return false
	}
	oldSchema, oldErr := json.Marshal(before.InputSchema)
	newSchema, newErr := json.Marshal(after.InputSchema)
	return oldErr == nil && newErr == nil && bytes.Equal(oldSchema, newSchema)
}
