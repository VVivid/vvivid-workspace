# Accepted UI reference

The user approved the compact mobile design. This folder preserves the last prototype so a new chat does not need to reconstruct it.

- ui-demo/: React/Vite/Tailwind source copied from the design chat, excluding dependencies, build output and temporary compiler state.
- screenshots/: snapshots of the shopping list, History, Insights, and Polar details.
- category-icons-demo.png is the latest icon direction. Earlier screenshots show superseded emoji icons; follow the current source and this requirement.
- polar-product-demo.png shows the correct real-photo detail behavior.
- Any dashboard screenshot or design from earlier in the chat is superseded and is not included.

Reference interactions: list checkoff, Products picker, tap-to-view detail, purchase History with Buy again, Insights derived from sample purchase data. The photo uses the external Walmart image URL and may depend on network access.

The prototype is not a production Go app, has sample data that reset, and includes no real MCP integration, OAuth, durable store or user's actual purchase history.

Rebuild production with Go templates and minimal JavaScript, preserving the appearance and useful interactions. Do not copy React/Radix into the production architecture.
