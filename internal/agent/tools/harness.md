Ask Harness about itself, when something is failing and the cause looks like the harness rather than the code.

`action: state` returns the current runtime: active model and provider, LSP and MCP server health, skills and whether they are loaded, hooks, and disabled tools. `action: logs` returns recent internal log entries — `lines` for more — where provider errors, tool failures and LSP or MCP connection problems show up first as ERROR and WARN.
