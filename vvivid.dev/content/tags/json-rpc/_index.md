---
title: "JSON-RPC and MCP"
description: "Understand JSON-RPC requests, notifications, responses, and errors, then follow a Go server implementation to see the protocol behind MCP."
index: true
---

MCP uses JSON-RPC 2.0 messages. Start with [the protocol and its message types](/posts/json_rpc_the_protocol_behind_mcp/), then follow [a small JSON-RPC server in Go](/posts/jsonrpc_example/). The calculator example shows parsing, validation, dispatch, and response handling; it is a JSON-RPC example, not a complete MCP server.
