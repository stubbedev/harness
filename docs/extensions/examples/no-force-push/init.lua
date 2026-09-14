-- Blocks force-pushes, and points the model at the safer flag.
--
-- Copy this directory into ~/.config/harness/extensions/ (or
-- .harness/extensions/ in a project) and it takes effect on the next start.

local FORCE = { "%-%-force%s", "%-%-force$", "%s%-f%s", "%s%-f$" }

harness.on("PreToolUse", "^shell$", function(event)
  local command = event.tool_input.command or ""
  if not command:match("git%s+push") then
    return
  end

  for _, pattern in ipairs(FORCE) do
    if command:match(pattern) then
      return {
        decision = "deny",
        reason = "Force-pushing is not allowed here. "
          .. "Use --force-with-lease if the remote really needs rewriting, "
          .. "and say so first.",
      }
    end
  end
end)
