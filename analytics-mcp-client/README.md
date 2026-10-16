# Analytics MCP Client

A terminal client that sends plain-text questions to Claude and lets Claude call tools on the existing Go MCP gateway. The client does not connect to ClickHouse and does not run an MCP server.

Type a question at the `You:` prompt. Claude decides whether to call a discovered MCP tool. The client executes that call through the gateway and sends the result back to Claude for the final answer.

## Prerequisites

- Go 1.25 or newer
- An Anthropic API key
- The MCP gateway from `data-mcp-gateway` running and connected to the official ClickHouse MCP server

## Configuration

Copy `.env.example` to `.env` in this directory. The client loads `.env` directly and does not put those values in the process environment. `CLIENT_ENV_FILE` can point at a different file.

| Variable | Default | Purpose |
| --- | --- | --- |
| `ANTHROPIC_API_KEY` |  | Required. Never logged. |
| `ANTHROPIC_BASE_URL` | `https://api.anthropic.com` | Messages API origin. The client posts to `/v1/messages`. |
| `ANTHROPIC_MODEL` | `claude-sonnet-5-5` | Sent unchanged. An unavailable model returns an API error. |
| `ANTHROPIC_EFFORT` | `medium` | `low`, `medium`, `high`, `xhigh`, or `max`. Thinking shares `ANTHROPIC_MAX_TOKENS`. |
| `ANTHROPIC_MAX_TOKENS` | `32000` | Maximum tokens for one model response, including thinking. |
| `MCP_SERVER_URL` | `http://127.0.0.1:8080/mcp` | Gateway Streamable HTTP endpoint |
| `ANALYTICS_ACCESS_TOKEN` |  | Required. Sent as `Authorization: Bearer` on every gateway request. Never logged. |
| `MAX_TOOL_CALLS` | `10` | Tool calls allowed for one question |
| `REQUEST_TIMEOUT_SECONDS` | `60` | Deadline for one question, including tool calls |
| `REFER_DATABASE` | `lafdatav1` | The only ClickHouse database named in the prompt. The gateway enforces it. |

## Start

Start the gateway first, from `data-mcp-gateway`:

```bash
go run ./cmd/server
```

Then start the client:

```bash
cd analytics-mcp-client
cp .env.example .env
go run ./cmd/client
```

`-v` prints how long each tool call took. Questions are typed at the prompt. `exit`, `quit`, or EOF closes the MCP session. `/reload` lists tools again.

```text
Analytics MCP Client
Model: claude-sonnet-5-5
MCP: connected
Tools discovered: 2
Tools: get_schema_catalog, run_query

You: What is the total distance travelled by each fleet last month?
Assistant: I'll retrieve the fleet-level distance totals.
[Calling tool: run_query]
Assistant: Fleet 101 travelled ... km.
You: exit
```

The tool count and names come from MCP discovery. They are not hardcoded.

## Tool-calling loop

1. The terminal line is sent as a user message.
2. The client posts the conversation and the discovered tools to `POST /v1/messages` with `tool_choice` set to `{"type":"auto"}` and `output_config.effort` set to `medium`.
3. If Claude returns no tool calls, the text blocks are the answer.
4. If Claude returns tool calls, the assistant content blocks are stored unchanged, including thinking blocks and tool-use ids.
5. Each call is checked against the discovered tool list and its JSON schema, then executed sequentially through the gateway.
6. The MCP results for that turn are appended as one user message of `tool_result` blocks.
7. Claude is called again. The loop stops at a final answer, `MAX_TOOL_CALLS`, the request deadline, or a repeated identical tool-call batch.

Failed tool results are sent back to the model. They are not dropped. MCP text longer than 32 KiB is cut only in the model message and marked `[truncated: ...]`. Query safety stays in the gateway.

## Tests

Unit tests use mock HTTP and an in-process MCP server:

```bash
go test ./...
```

The live test needs a real API key and a reachable gateway. It is not part of the default test run:

```bash
go test -tags integration ./internal/agent -run TestLive
```

## Troubleshooting

- `ANTHROPIC_API_KEY is required`: put the key in `.env`.
- HTTP 401: the key was rejected. The error text does not include the key.
- `Anthropic model is unavailable`: `ANTHROPIC_MODEL` is not enabled for the account. The client does not switch models.
- HTTP 429: the API rate limit was reached.
- Context or token limit: shorten the question or lower how much data the gateway returns.
- `connect MCP gateway`: the gateway is not running, or `MCP_SERVER_URL` is not `http://127.0.0.1:8080/mcp`.
- `unknown tool`: Claude named a tool the gateway did not advertise. The call is not executed.
- `maximum tool calls`: raise `MAX_TOOL_CALLS` or ask a narrower question.
