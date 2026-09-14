-- Checks the links in a Markdown file, two ways: a tool that fetches them
-- all at once, and a background job for when there are too many to hold a
-- tool call open.
--
-- Shows both halves of the async story: handles for parallel I/O inside a
-- call, and a job whose result waits in the queue.

local function links_in(path)
  local content = harness.fs.read(path)
  if content == nil then
    return nil, "no such file: " .. path
  end

  local urls = {}
  for url in content:gmatch("%]%((https?://[^%s%)]+)%)") do
    urls[#urls + 1] = url
  end
  return urls
end

-- Fetches every URL at once and returns the ones that did not answer 2xx.
local function check(urls)
  local handles = {}
  for i, url in ipairs(urls) do
    handles[i] = harness.http.request({ url = url, method = "HEAD", async = true })
  end

  local broken = {}
  for i, handle in ipairs(handles) do
    local rsp = harness.await(handle)
    if not rsp.ok then
      broken[#broken + 1] = {
        url = urls[i],
        status = rsp.status or 0,
        error = rsp.error,
      }
    end
  end
  return broken
end

harness.register_tool({
  name = "link_check",
  description = "Checks every link in a Markdown file and reports the broken ones.",
  parameters = {
    path = { type = "string", description = "Markdown file to check", required = true },
  },
  handler = function(input)
    local urls, err = links_in(input.path)
    if urls == nil then
      return { content = err, is_error = true }
    end
    if #urls == 0 then
      return "No links in " .. input.path .. "."
    end

    -- More than a handful is slow enough that the model should not wait
    -- on it: hand the work to a job and answer with its ID.
    if #urls > 25 then
      local id = harness.jobs.start("link_check", { path = input.path })
      return "Checking " .. #urls .. " links in the background as " .. id ..
        ". Collect it with extension_jobs."
    end

    local broken = check(urls)
    if #broken == 0 then
      return "All " .. #urls .. " links in " .. input.path .. " are reachable."
    end
    return { content = harness.json.encode(broken), metadata = { broken = #broken } }
  end,
})

harness.register_job({
  name = "link_check",
  description = "Checks the links in a Markdown file",
  timeout = 600,
  handler = function(args)
    local urls, err = links_in(args.path)
    if urls == nil then
      error(err)
    end
    return { path = args.path, checked = #urls, broken = check(urls) }
  end,
})
