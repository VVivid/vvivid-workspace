---
title: "MCPGateway"
weight: 30
draft: false
description: "An authenticated router for HTTP MCP services, with per-service routes, permissions, and signed backend identities."
tags: ["MCP", "OAuth", "Gateway"]
link: "https://github.com/VVivid/vvivid-workspace/tree/main/MCPGateway"
repo: "https://github.com/VVivid/vvivid-workspace/tree/main/MCPGateway"
thumbnail: false
---

MCPGateway is a standalone authenticated router for stateless Streamable HTTP MCP services. Each backend has its own route, audience, and permission policy. The gateway verifies external access tokens, forwards granted permissions, and signs short-lived identities for the selected backend.

Browse the [source code and documentation](https://github.com/VVivid/vvivid-workspace/tree/main/MCPGateway).
