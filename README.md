<div align="center">

```
██╗      █████╗ ███████╗██╗   ██╗ ██████╗██╗   ██╗██████╗ ██╗
██║     ██╔══██╗╚══███╔╝╚██╗ ██╔╝██╔════╝██║   ██║██╔══██╗██║
██║     ███████║  ███╔╝  ╚████╔╝ ██║     ██║   ██║██████╔╝██║
██║     ██╔══██║ ███╔╝    ╚██╔╝  ██║     ██║   ██║██╔══██╗██║
     ███████╗██║  ██║███████╗   ██║   ╚██████╗╚██████╔╝██║  ██║███████╗
     ╚══════╝╚═╝  ╚═╝╚══════╝   ╚═╝    ╚═════╝ ╚═════╝ ╚═╝  ╚═╝╚══════╝
  HTTP TUI Client
```

</div>

<p align="center">
  <strong>A powerful Terminal User Interface (TUI) HTTP client</strong><br />
  Combining <strong>Lazygit</strong>'s elegant interface with <strong>Postman</strong>'s API testing capabilities
</p>

<p align="center">
  <a href="https://github.com/kbrdn1/LazyCurl/actions/workflows/ci.yml"><img src="https://github.com/kbrdn1/LazyCurl/actions/workflows/ci.yml/badge.svg" alt="CI" /></a>
  <a href="https://github.com/kbrdn1/LazyCurl/releases"><img src="https://img.shields.io/github/v/release/kbrdn1/LazyCurl?style=flat-square" alt="Release" /></a>
  <a href="https://codecov.io/gh/kbrdn1/LazyCurl"><img src="https://codecov.io/gh/kbrdn1/LazyCurl/branch/main/graph/badge.svg" alt="Coverage" /></a>
  <a href="https://goreportcard.com/report/github.com/kbrdn1/LazyCurl"><img src="https://goreportcard.com/badge/github.com/kbrdn1/LazyCurl" alt="Go Report Card" /></a>
</p>

<p align="center">
  <img src="https://img.shields.io/badge/Go-1.21+-00ADD8?style=for-the-badge&logo=go&logoColor=white" alt="Go Version" />
  <img src="https://img.shields.io/badge/TUI-Bubble_Tea-b4befe?style=for-the-badge" alt="Bubble Tea" />
  <img src="https://img.shields.io/badge/License-MIT-a6e3a1?style=for-the-badge" alt="License" />
</p>

---

## Features

- **Lazygit-Style Interface** — Multi-panel layout with vim motions (`h/j/k/l`)
- **JavaScript Scripting** — Pre/post-request scripts via Goja ES5.1+ engine
- **Test Assertions** — 16 matchers with `lc.test()` and `lc.expect()`
- **Collections & Environments** — Organize requests, manage variables with `{{var}}` syntax
- **Import/Export** — cURL, OpenAPI 3.x, Postman collections
- **External Editor** — Edit bodies in vim, VS Code, nano, etc.
- **Session Persistence** — Auto-save/restore your workspace state
- **Jump Mode** — vim-easymotion style quick navigation

<p align="center">
<img src="preview-app.png" alt="LazyCurl Preview" width="100%" />
<br />
<em>LazyCurl main interface with request builder, response viewer, and collections panel</em>
</p>

---

## Installation

### From Source

```bash
git clone https://github.com/kbrdn1/LazyCurl.git
cd LazyCurl
make build
./bin/lazycurl
```

### Using Go Install

```bash
go install github.com/dtour68fc/LazyCurl/cmd/lazycurl@latest
```

---

## Quick Start

```bash
# Navigate to your project
cd my-api-project

# Launch LazyCurl (creates .lazycurl/ workspace)
lazycurl

# Press 'n' to create a request, 'Ctrl+S' to send
# Press '?' for keybinding help
```

**Import existing APIs:**

```bash
# Import OpenAPI spec
lazycurl import openapi api.yaml

# Import Postman collection
lazycurl import postman collection.json
```

---

## MCP server (for AI coding agents)

`lazycurl mcp` starts a stdio MCP (Model Context Protocol) server that lets
AI coding agents create/list/update a NexusCurlServer project's Collections
(folders + requests) and Environments (variables), including bulk/batch
tools for migrating large Postman exports or mass-creating gRPC requests:

```bash
lazycurl mcp --server 10.10.1.107:29500 --project PMC --api-key ncmcp_xxxx...
```

`--api-key` is optional - if omitted, requests go out unauthenticated,
matching NexusCurlServer's opt-in key gating (see its README for issuing
keys with `nexuscurlserver keygen`).

### Setting it up from the TUI

Two ways to open the API-key prompt:

- **Already-linked collection/environment**: select it in the
  Collections/Environments panel and press `m` directly - resolves its
  linked project automatically.
- **Any project on a server**: press `W` to open the workspace list, `e`
  on a server to open its project checklist, then `m` on a project row.

Either way, paste an API key if you already have one, or just leave it
blank and confirm - a brand-new key gets generated and activated on the
server for you (no operator running `nexuscurlserver keygen` required).
Pressing `m` again later overwrites the same entry in place, e.g. to
rotate to a freshly generated key. This writes/merges a `.mcp.json`
entry in the current directory like:

```json
{
  "mcpServers": {
    "nexuscurl-PMC": {
      "command": "lazycurl",
      "args": ["mcp", "--server", "10.10.1.107:29500", "--project", "PMC", "--api-key", "ncmcp_xxxx..."]
    }
  }
}
```

`.mcp.json` with a top-level `mcpServers` object is the convention most
MCP-aware CLIs/agents (Claude Code, Cursor, and others) already read for
project-scoped server config, so this file is usually all that's needed -
no manual config required on the agent side. Repeating `m` for more
projects (even across different servers) accumulates additional entries
in the same file instead of overwriting it.

For CLIs that expect a different top-level shape (e.g. VS Code's
`mcp.json` uses `servers` instead of `mcpServers`), use the example below.

Example MCP client config (VS Code `mcp.json`):

```json
{
  "servers": {
    "lazycurl-nexuscurl": {
      "type": "stdio",
      "command": "lazycurl",
      "args": ["mcp", "--server", "10.10.1.107:29500", "--project", "PMC", "--api-key", "ncmcp_xxxx..."]
    }
  }
}
```

---

## Key Shortcuts

| Action | Key |
|--------|-----|
| Navigate panels | `h` / `l` |
| Move in list | `j` / `k` |
| New request | `n` |
| Send request | `Ctrl+S` |
| Import cURL | `Ctrl+I` |
| Import OpenAPI | `Ctrl+O` |
| Import Postman | `Ctrl+P` |
| External editor | `Ctrl+E` |
| Jump mode | `f` |
| Help | `?` |

---

## Documentation

Full documentation available in [`docs/`](docs/index.md) and [Wiki](https://github.com/kbrdn1/LazyCurl/wiki):

| Guide | Description |
|-------|-------------|
| [Getting Started](docs/getting-started.md) | First steps with LazyCurl |
| [**Scripting API**](docs/api/overview.md) | JavaScript API reference (75+ methods) |
| [Keybindings](docs/keybindings.md) | Complete keyboard reference |
| [Import/Export](docs/import-export.md) | cURL, OpenAPI, Postman |
| [Collections](docs/collections.md) | Organize requests |
| [Environments](docs/environments.md) | Variable management |
| [Configuration](docs/configuration.md) | Customization options |

---

## Contributing

Contributions welcome! See [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines.

```bash
# Development with live reload
make dev

# Run tests
make test
```

---

## License

[MIT License](LICENSE)

---

<p align="center">
  Made with ❤️ by <a href="https://github.com/kbrdn1">@kbrdn1</a>
</p>
