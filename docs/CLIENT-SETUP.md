# Beta MCP client setup preparation

October19, 2026. No client configuration, registration or login was changed by this preparation. The deployed application, shopping registrations and live acceptance are still pending. Keep the initial enrollment owner-only and use separately registered shopping client IDs; do not reuse SSH credentials or grants.

## Codex

Current [official MCP guidance](https://learn.chatgpt.com/docs/extend/mcp?surface=cli) supports pre-registered public clients with `codex mcp add NAME --url URL --oauth-client-id ID`. Current local `codex-cli0.162.1` help also exposes that option and `--oauth-resource`. A confidential secret is optional and is not needed for the proposed public shopping-codex registration.

After the app and reviewed shopping registration are ready, the intended configuration is server name `shopping`, URL/resource `https://shopping.vvivid.dev/mcp`, OAuth client `shopping-codex`, with shopping-only consented scopes. Do not configure a static bearer token or copy SSH credentials. Adding a server writes local Codex configuration; this has not been executed.

The exact callback must be copied from the new setup's displayed callback URL. A port in the URL does not select the listener port; the [configuration reference](https://learn.chatgpt.com/docs/config-file/config-reference) documents server-specific `mcp_servers.shopping.oauth.callback_port` and `callback_url`. Select a free explicit shopping port if a fixed callback is required, independently of the existing SSH client. Verify the displayed final callback before adding it to Keycloak; no wildcard or assumed inherited callback.

Issuer discovery was read live and advertises `authorization_response_iss_parameter_supported:true` with the existing exact issuer. Stable callback selection still requires the actual authorization response to include matching `iss`. Existing clients without saved callbacks and providers without issuer support can use callback-ID-specific paths. The observed old SSH callback is reference evidence only, not the shopping callback.

## ChatGPT

The [official authentication guide](https://developers.openai.com/plugins/build/auth) says to copy the exact production redirect URI shown by the MCP server management page. Its callback mode depends on issuer identification: a stable `https://chatgpt.com/connector_platform_oauth_redirect` or a connection-specific `https://chatgpt.com/connector/oauth/{callback_id}`. Existing published connections may retain their stable callback. Do not assume the new shopping connection matches the observed SSH connection.

This beta's selected strategy is explicit pre-registration of `shopping-chatgpt`; open dynamic registration and CIMD adoption are deferred. Verify that the user's actual ChatGPT connection setup accepts the pre-defined registration and records the expected client identity. Current public documentation emphasizes CIMD/DCR and does not prove that setup is available in every account/surface. If it is unavailable, resolve the registration strategy before changing Keycloak or weakening the five-client mapping/ownership policy. Do not silently introduce open registration.

Client management may require user interaction to obtain the exact callback and deliver a generated secret privately. No secret belongs in source, screenshots, command arguments, logs or this document. Keep the client valid while the connection is in use; use supported PKCE/consent/refresh and verify actual tools after sign-in.

## Actual acceptance still required

- A new Codex and ChatGPT connection selects the intended shopping resource/client and exact callback, with no SSH scopes/audience. Observe an actual successful code exchange and resource-bound refresh; do not count configured files as runtime proof.
- Both clients can read their owner's products/list/history/insights and perform only consented writes. Wrong issuer/audience/expired/scopes/resource fail closed. Shopping tokens cannot authorize SSH, and existing SSH grants cannot authorize shopping.
- User opens the real phone UI, recognizes the product/photo, checks without creating spending, and sees a separately sourced confirmed purchase reflected once in History/Insights. Retry/restart behavior and durable sessions remain verified on the live service.
- Account enrollment remains explicit; OAuth login alone does not enroll another realm account. Background/offline consent is a separate future choice.

Local CLI inspection executed only `codex --version` and `codex mcp add --help`. The sandbox printed a PATH-alias permission warning; no server was added, config/credentials inspected or login attempted. Official documentation and this preparation do not establish the live clients' behavior.
