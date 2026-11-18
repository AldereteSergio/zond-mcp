<div align="center">

# zond-mcp

**Web search, page reading and OCR for MCP agents — with the receipts.**

<a href="https://go.dev"><img src="https://img.shields.io/badge/Go-1.26+-00ADD8?logo=go&logoColor=white" alt="Go version"></a>
<a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-green" alt="License"></a>
<a href="https://github.com/AldereteSergio/zond-mcp/releases"><img src="https://img.shields.io/github/v/release/AldereteSergio/zond-mcp?color=blue" alt="Release"></a>
<img src="https://img.shields.io/badge/MCP-stdio-6E56CF" alt="MCP stdio">

</div>

`zond-mcp` is a [Model Context Protocol](https://modelcontextprotocol.io) server that exposes DeepSeek's
three **Tools** endpoints — search, fetch and OCR — as plain MCP tools. One static Go binary, stdio
transport, no runtime to install.

`zond` (зонд) means *probe* in Russian. That is what it does: it puts a probe into the web and into your
documents and hands back what it found, with the cost and the limits in plain sight.

```bash
zond-mcp --check
# ✓ configuration: key present, base https://api.deepseek.com
# ✓ connectivity: the API answered
# ✓ balance: 12.40 USD
```

## Why another one

There are other wrappers around these endpoints. This one is about behavior, not feature count.
Four things it does that a thin passthrough cannot:

- **It does not invent defaults.** If you omit `count`, `count` is not sent and the API applies its own.
  Same for `need_content`, `ori_question` and `localization`. Nothing is decided on the model's behalf.
- **It declares every cut.** A long page comes back in a window with the real total and a cursor to
  continue. Nothing is truncated silently — a truncation you don't see is worse than no tool at all.
- **It translates errors into instructions.** Every failure says what happened and what to do, and marks
  whether retrying makes sense. A `402` tells you to top up; it does not get retried.
- **It treats OCR the way the API documentation says to.** A `200` can carry an `error` body. Disconnecting
  does not cancel a recognition. The timeout is not retried, because it is billed anyway.

## Installation

You need a DeepSeek account with balance and a key from
[platform.deepseek.com/api_keys](https://platform.deepseek.com/api_keys).

Download the binary for your platform from [Releases](https://github.com/AldereteSergio/zond-mcp/releases),
drop it somewhere permanent, and declare it in your MCP client. The path is the full path to the binary;
**the key goes in `env`**, never on the command line.

```jsonc
// Windows: %APPDATA%\Claude\claude_desktop_config.json
{
  "mcpServers": {
    "zond": {
      "command": "C:\\Tools\\zond-mcp-windows-amd64.exe",
      "env": { "DEEPSEEK_API_KEY": "YOUR_KEY_HERE" }
    }
  }
}
```

```jsonc
// macOS and Linux
{
  "mcpServers": {
    "zond": {
      "command": "/usr/local/bin/zond-mcp",
      "env": { "DEEPSEEK_API_KEY": "YOUR_KEY_HERE" }
    }
  }
}
```

Restart the client and run the check, which costs nothing:

```bash
DEEPSEEK_API_KEY=your-key zond-mcp --check
```

If something goes wrong, delete the entry you added and everything is back to how it was. The server
installs nothing, writes no files and leaves no processes behind.

> A server that starts is not a server that works. If the key is missing or wrong, the server still
> starts and the error shows up on the first call, naming the variable.

## The three tools

| Tool | Endpoint | What you get |
|---|---|---|
| `search` | `POST /v1/tools/search` | Ranked results with `rerank_score`, `authority`, date, summary, and optionally the page text |
| `fetch` | `POST /v1/tools/fetch` | One URL → title and Markdown, windowed with a continuation cursor |
| `ocr` | `POST /v1/tools/ocr` | A local file (PDF, Word, PowerPoint or image) → text page by page |

**`search`** takes `query` (1–128 characters, with operators: `site:`, `-site:`, `-term`, `-"exact phrase"`,
`"exact phrase"`, `lang:es,en`, `after:2025`, `before:2026`), `count` (1–50), `need_content`, `ori_question`,
`localization` (an IP address, not a country code) and an optional `min_rerank_score` that filters locally
and reports how many results it dropped.

**`fetch`** takes `url` (HTTP/HTTPS, with host, no credentials, up to 8192 characters) plus `max_chars` and
`offset`, which are a local window over the response rather than API parameters. When it truncates it
returns `truncated`, `total_chars` and `next_offset`. Every call is billed, including the continuation,
so the tool description says so out loud.

**`ocr`** takes `path` (a **local path**, not a URL) and `purpose` (`text_recognition` or
`rich_layout_recognition`, no default). It returns pages with their original `index`, their raw text —
position tokens like `<|ref|>` and `<|box|>` included, coordinates normalized to 0–1000 — and a
`finish_reason` per page, where `length` means the page was cut on the service side.

## The pattern that saves money

```
search(count: 5..10, need_content: false)
  → pick by rerank_score (>= 1.5) and authority (>= 3)
  → fetch only the URLs that matter
  → decide about truncated content before paying for the rest
  → ocr for local files
```

Don't raise `count` to 50 hoping for coverage. A single query rarely yields more than ten useful URLs and
the tail is noise; several distinct queries cover more ground than one query with quantity. This is also
what the tool descriptions tell the model, because the model never reads this file.

## Cost

Prices published by DeepSeek as of **2026-11-16**:

| API | Unit | Price |
|---|---|---|
| `search` | per 1,000 successful requests | **$0.75** |
| `fetch` | per 1,000 successful requests | **$0.75** |
| `ocr` | per 1,000 recognized pages | **$0.225** |

Failed requests are not billed, and neither are searches with no results; `count` does not change the
price. Every response carries an `estimated_cost_usd` computed from these rates — an estimate, not an
invoice. For the exact number, use the DeepSeek dashboard. If the prices change, that number stays stale
until the constants in `internal/deepseek/client.go` are updated.

## Configuration

| Variable | Required | Default | Purpose |
|---|---|---|---|
| `DEEPSEEK_API_KEY` | yes | — | Account key. Environment only. |
| `DEEPSEEK_BASE_URL` | no | `https://api.deepseek.com` | Point at your own gateway. |
| `DEEPSEEK_LOG_LEVEL` | no | `warn` | `debug`, `info`, `warn` or `error`. Logs go to stderr. |

## About `ocr` and your files

The tool receives a path and the server **reads that file from your disk**. Trust is placed in the agent
and in you: if you don't want an agent to be able to read files on your machine, don't enable `ocr`. The
server rejects symlinks (including symlinks anywhere in the path), directories and devices, and never
sends more than 100 MiB — but it has no directory allowlist yet. On Windows the check covers symbolic
links, not necessarily junctions.

Two warnings that come straight from the API documentation:

- **Cancelling an `ocr` call does not cancel the recognition or the billing.** Once the file is uploaded
  the work finishes on the service side and is billed per page.
- Text comes back **raw**, with position tokens and normalized coordinates, and each page reports its
  `finish_reason`.

## What it does not do

ASR and TTS. The Anthropic-compatible endpoint with its `web_search` tool (that returns prose, not
structure). HTTP transport: this is a local stdio server. Caching or any kind of state. Reselling or
proxying the service — every user pays for their own account.

## Development

```bash
gofmt -l .                        # must print nothing
go vet ./...
go test ./...                     # unit tests, no network, no key
go test -tags integration ./...   # against the real API; needs DEEPSEEK_API_KEY with balance
```

The contract this server implements is DeepSeek's official documentation: code comments cite the page each
rule comes from. Integration tests exist so a contract change on their side fails loudly instead of
silently.

## License and notice

MIT. **Not affiliated with DeepSeek**: the terms, prices and rate limits of the API are theirs.
