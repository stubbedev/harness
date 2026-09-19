# Oversized tool results

Text tool results larger than 100,000 bytes are saved in full to a private,
per-session scratch file. Harness returns an absolute file path and a UTF-8-safe
preview, bounded to 100,000 bytes including the retrieval notice. Use `view`
with `offset` and `limit`, or search the saved file through `shell`.

This applies to built-in tools and MCP tools through the common result wrapper,
and to individual tools invoked by `batch`. A large JSON result becomes a text
preview with a file reference; batch expressions must not assume that an oversized
response remains inline JSON. Media and binary payloads are not converted.

Spill files are collision-safe and created with mode 0600 in private scratch
directories, outside the project checkout. Status, error flags, stop-turn flags,
and response metadata are preserved. If saving fails, the response explicitly
warns that omitted output was lost and recommends a narrower request. Scratch
files remain subject to operating-system temporary-storage cleanup; they are not
permanent artifacts. This bounds model-visible text, not upstream result memory,
metadata size, or disk usage.
