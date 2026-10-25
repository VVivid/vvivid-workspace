
---
title: "Understanding JSON-RPC: The Protocol Behind MCP"
date: 2025-08-24
lastmod: 2026-10-25
author: "Ou David"
description: "Breaking down JSON-RPC to understand how MCP (Model Context Protocol) works under the hood"
tags: ["mcp", "json-rpc"]
categories: ["Technical Deep Dives"]
---

JSON-RPC 2.0 defines requests, notifications, responses, and errors as JSON messages. MCP uses that message format and adds its own lifecycle, capabilities, and methods. Start with the `method`, `params`, and `id` fields to understand what travels between an MCP client and server.


I've been diving into the [Model Context Protocol (MCP)](https://modelcontextprotocol.io/) lately. Honestly, it's the most exciting thing happening in AI infrastructure right now. But here's the thing: to really understand MCP, you need to understand [JSON-RPC](https://www.jsonrpc.org/specification) first. That's what MCP is built on.

So let me break down JSON-RPC for you. Once you get this, MCP will make way more sense.

First, let's compare JSON-RPC with the REST APIs we're all used to. REST usually models resources through URLs and HTTP methods; JSON-RPC names the operation in the message body. Statelessness is a REST constraint, while the direction and transport of messages depend on the application. Check this out:

## Compare resource-oriented REST operations {#example-rest-api}

```text
# Getting a user
GET /api/users/123
Response: 200 OK
{
  "id": 123,
  "name": "John Doe",
  "email": "john@example.com"
}

# Creating a user
POST /api/users
Content-Type: application/json
{
  "name": "Jane Smith",
  "email": "jane@example.com"
}
Response: 201 Created

# Updating a user
PUT /api/users/123
Content-Type: application/json
{
  "name": "John Updated",
  "email": "john.updated@example.com"
}
Response: 200 OK

# Deleting a user
DELETE /api/users/123
Response: 204 No Content
```

In REST API, we control resources using multiple methods such as GET, PUT, POST, DELETE, and PATCH. These are all the methods that we use to retrieve or manipulate resources, and the URI is the resource identifier.

On the other hand, JSON-RPC has a very different approach. By design, it supports bidirectional communication and persistent connections. Here's an example:

## Call methods with JSON-RPC messages {#example-json-rpc}

```jsonc
// Request 1: Get a user
{
  "jsonrpc": "2.0",
  "method": "getUser",
  "params": {"id": 123},
  "id": 1
}

// Response 1
{
  "jsonrpc": "2.0",
  "result": {
    "id": 123,
    "name": "John Doe",
    "email": "john@example.com"
  },
  "id": 1
}

// Request 2: Create a user
{
  "jsonrpc": "2.0",
  "method": "createUser",
  "params": {
    "name": "Jane Smith",
    "email": "jane@example.com"
  },
  "id": 2
}

// Response 2
{
  "jsonrpc": "2.0",
  "result": {
    "id": 124,
    "name": "Jane Smith",
    "email": "jane@example.com"
  },
  "id": 2
}

// Notification (no response expected)
{
  "jsonrpc": "2.0",
  "method": "logActivity",
  "params": {
    "action": "user_login",
    "userId": 123
  }
  // Note: No "id" field - this is a notification
}
```

See the difference? JSON-RPC uses a single endpoint with consistent request/response structures. Every call is a method invocation, not a resource operation. And here's something cool: if you don't need a response, you can send a notification (just leave out the `id` field).

What's really powerful is that JSON-RPC is transport-agnostic. It works over HTTP, WebSockets, TCP, or even stdin/stdout (which MCP uses). This flexibility is one of the reasons it's perfect for AI tool integration.

## JSON-RPC request, notification, and response rules {#json-rpc-basics}

### 1. Request Object

Let's break down what's actually in these messages. Here's a typical request:

```json
{
  "jsonrpc": "2.0",
  "method": "getUserInfo",
  "params": {
    "userId": 12345,
    "includeDetails": true
  },
  "id": 1
}
```

Each request will include these properties:

- **jsonrpc**  
  A String specifying the version of the JSON-RPC protocol. MUST be exactly "2.0".

- **method**  
  A String containing the name of the method to be invoked. Method names that begin with the word rpc followed by a period character (U+002E or ASCII 46) are reserved for rpc-internal methods and extensions and MUST NOT be used for anything else.

- **params**  
  A Structured value that holds the parameter values to be used during the invocation of the method. This member MAY be omitted.

- **id**  
  An identifier established by the Client that MUST contain a String, Number, or NULL value if included. If it is not included it is assumed to be a notification. The value SHOULD normally not be Null [[1]](https://www.jsonrpc.org/specification#id1) and Numbers SHOULD NOT contain fractional parts [[2]](https://www.jsonrpc.org/specification#id2)


### 2. Notifications

When the client doesn't need a response, it sends a notification (request without an id):

```json
{
  "jsonrpc": "2.0",
  "method": "logEvent",
  "params": {
    "event": "user_login",
    "timestamp": "2024-12-28T10:30:00Z",
    "userId": 12345
  }
}
```

 The Server MUST NOT reply to a Notification, including those that are within a batch request.

### 3. Response Object

After processing a request, the server sends back a Response object:

```jsonc
// Successful Response
{
  "jsonrpc": "2.0",
  "result": {
    "userId": 12345,
    "name": "John Doe",
    "email": "john@example.com",
    "registered": "2024-01-15"
  },
  "id": 1
}
```

The response has these fields:

- **jsonrpc**  
  A String specifying the version of the JSON-RPC protocol. MUST be exactly "2.0".

- **result**  
  This member is REQUIRED on success.  
  This member MUST NOT exist if there was an error invoking the method.  
  The value of this member is determined by the method invoked on the Server.

- **error**  
  This member is REQUIRED on error.  
  This member MUST NOT exist if there was no error triggered during invocation.  
  The value for this member MUST be an Object as defined in section 5.1.

- **id**  
  This member is REQUIRED.  
  It MUST be the same as the value of the id member in the Request Object.  
  If there was an error in detecting the id in the Request object (e.g. Parse error/Invalid Request), it MUST be Null.

When something goes wrong, you get an error response instead:

```json
{
  "jsonrpc": "2.0",
  "error": {
    "code": -32602,
    "message": "Invalid params",
    "data": "userId must be a positive integer"
  },
  "id": 1
}
```

- **code**  
  A Number that indicates the error type that occurred.  
  This MUST be an integer.

- **message**  
  A String providing a short description of the error.  
  The message SHOULD be limited to a concise single sentence.

- **data**  
  A Primitive or Structured value that contains additional information about the error.  
  This may be omitted.  
  The value of this member is defined by the Server (e.g. detailed error information, nested errors etc.).

### 4. Batch Requests

Here's where JSON-RPC gets really useful: you can send multiple calls in one shot. The server processes them and sends back a batch of responses. Important: responses might come back in a different order! That's why the `id` field matters. It's how you match responses to requests.

```jsonc
// Batch Request
[
  {"jsonrpc": "2.0", "method": "getUser", "params": {"id": 1}, "id": 1},
  {"jsonrpc": "2.0", "method": "getUser", "params": {"id": 2}, "id": 2},
  {"jsonrpc": "2.0", "method": "logEvent", "params": {"event": "batch_query"}}
]

// Batch Response (no response for the notification)
[
  {"jsonrpc": "2.0", "result": {"id": 1, "name": "Alice"}, "id": 1},
  {"jsonrpc": "2.0", "result": {"id": 2, "name": "Bob"}, "id": 2}
]
```

## Why This Matters for the Model Context Protocol

Now that you understand JSON-RPC, MCP will make way more sense. MCP uses JSON-RPC for everything - tools, resources, prompts. When an LLM wants to use a tool through MCP, it's sending JSON-RPC requests. When it gets results back, those are JSON-RPC responses.

The bidirectional nature of JSON-RPC is perfect for MCP because:
- LLMs can call server methods (tools)
- Servers can send notifications back (progress updates)
- Everything uses the same clean protocol

---

## Related JSON-RPC implementation articles {#more-deep-dives-you-might-like}

**[Building a JSON-RPC Server in Go](/posts/jsonrpc_example/)** - Ready to implement what we just learned? This guide walks through building a complete JSON-RPC calculator service in Go.

**[The HTTP Request Journey in Go](/posts/http_in_golang/)** - Curious how Go handles protocols? This deep dive traces HTTP requests from socket to handler, exploring Go's architecture patterns.

---

Got questions about any of this? Hit me up at [oudavid@vvivid.dev](mailto:oudavid@vvivid.dev). I love talking about this stuff!

## References

- [Model Context Protocol Specification](https://modelcontextprotocol.io/)
- [JSON-RPC 2.0 Specification](https://www.jsonrpc.org/specification)
