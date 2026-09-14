-- A tool that collects TODO comments from the repository, and a command
-- that asks the model to triage them.
--
-- Shows the three registrations in one file: a tool the model can call, a
-- command in the palette, and a hook handler that adds context.

local function scan(pattern)
  local result = harness.exec(
    "rg --line-number --no-heading " .. pattern .. " || true"
  )
  return result.stdout
end

harness.register_tool({
  name = "todo_list",
  description = "Lists TODO and FIXME comments in the repository, with file and line.",
  parameters = {
    marker = {
      type = "string",
      description = "Which marker to look for: TODO (default) or FIXME",
    },
  },
  handler = function(input)
    local marker = input.marker or "TODO"
    local hits = scan("'" .. marker .. "'")
    if hits == "" then
      return "No " .. marker .. " comments found."
    end
    return hits
  end,
})

harness.register_command({
  name = "triage",
  description = "Triage the repository's TODO comments",
  handler = function()
    return "Here are the open TODOs:\n\n"
      .. scan("'TODO'")
      .. "\nGroup them by area, and tell me which three are worth doing first."
  end,
})

harness.on("SessionStart", function()
  local count = select(2, scan("'TODO'"):gsub("\n", "\n"))
  if count > 50 then
    return "This repository has " .. count .. " TODO comments; expect stale ones."
  end
end)
