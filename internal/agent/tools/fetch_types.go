package tools

// ResearchToolName is the name of the research tool.
const ResearchToolName = "research"

// WebSearchToolName is the name of the web_search tool.
const WebSearchToolName = "web_search"

// LargeContentThreshold is the size threshold for saving content to a file.
const LargeContentThreshold = 50 * 1024 // 50KB

// ResearchParams defines the parameters for the research tool.
type ResearchParams struct {
	URL    string `json:"url,omitempty" description:"A page to start from. Leave it out to start from a web search instead"`
	Prompt string `json:"prompt" description:"The question to answer, or what to find and extract"`
}

// WebSearchParams defines the parameters for the web_search tool.
type WebSearchParams struct {
	Query      string `json:"query" description:"The search query to find information on the web"`
	MaxResults int    `json:"max_results,omitempty" description:"Maximum number of results to return (default: 10, max: 20)"`
	FetchTop   int    `json:"fetch_top,omitempty" description:"Also fetch the top N results (1-3) and return the start of each page as markdown"`
}

// FetchParams defines the parameters for the fetch tool.
type FetchParams struct {
	URL     string `json:"url" description:"The URL to fetch content from"`
	Format  string `json:"format,omitempty" description:"How to return the content: markdown (default, boilerplate stripped), text, or html"`
	Timeout int    `json:"timeout,omitempty" description:"Optional timeout in seconds (max 600 when downloading, 120 otherwise)"`
	Offset  int    `json:"offset,omitempty" description:"Line of the converted content to start at (0-based)"`
	Limit   int    `json:"limit,omitempty" description:"Maximum number of lines to return"`
	Find    string `json:"find,omitempty" description:"Regular expression; return only the matching lines, numbered, with two lines of context around each"`
	// Download streams the response to a file instead of returning it.
	Download bool   `json:"download,omitempty" description:"Save the response to a file instead of reading it into context. Binary-safe and streaming: use it for archives, images, PDFs and anything else that is not text to read"`
	FileName string `json:"file_name,omitempty" description:"File name to save as when download is true. Defaults to the name in the URL"`
}
