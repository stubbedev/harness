Read a resource by URI from an MCP server; returns text content.

Only call this with a server name and resource URI you actually have — both come
from list_mcp_resources (or from the server's own instructions). Never guess or
fill in placeholder values: a name that is not a configured MCP server fails.
