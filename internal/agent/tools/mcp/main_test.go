package mcp

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeServerEnv makes the test binary a stdio MCP server instead of
// running the tests: see TestMain.
const fakeServerEnv = "MCP_TEST_FAKE_SERVER"

// failStartupEnv makes the test binary a stdio server that fails on
// startup, printing the variable's value: see failingStdioCommand.
const failStartupEnv = "MCP_TEST_FAIL_STARTUP"

// failStartupArg is the only argument a failing server accepts, so a
// re-run that mangles the command line is told apart from the server's
// own failure.
const failStartupArg = "-test.run=^$"

func TestMain(m *testing.M) {
	if os.Getenv(fakeServerEnv) == "1" {
		fakeServer()
		os.Exit(0)
	}
	if msg, ok := os.LookupEnv(failStartupEnv); ok {
		failStartup(msg)
	}
	os.Exit(m.Run())
}

// hogHeld keeps what the hog tool allocates reachable.
var hogHeld [][]byte

// fakeServer serves one tool over stdio, hog, which leaves a sleeping
// child behind (so the scope outlives the server and what happened in it
// can still be read) and then allocates memory until the kernel kills
// the server.
func fakeServer() {
	server := mcp.NewServer(&mcp.Implementation{Name: "fake", Version: "0.0.1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "hog", Description: "run out of memory"},
		func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			_ = exec.CommandContext(context.Background(), "sleep", "300").Start()
			for {
				b := make([]byte, 1<<20)
				for i := range b {
					b[i] = 1
				}
				hogHeld = append(hogHeld, b)
			}
		})
	_ = server.Run(context.Background(), &mcp.StdioTransport{})
}

// failStartup prints msg and exits non-zero, the way a stdio server with
// a bad configuration dies before it speaks the protocol.
func failStartup(msg string) {
	if len(os.Args) != 2 || os.Args[1] != failStartupArg {
		fmt.Fprintf(os.Stderr, "unexpected argv %q\n", os.Args)
		os.Exit(4)
	}
	fmt.Fprintln(os.Stderr, msg)
	os.Exit(3)
}

// failingStdioCommand returns a command that runs the test binary as a
// stdio server failing on startup with msg. It needs no shell, so it
// behaves the same on every platform.
func failingStdioCommand(t *testing.T, msg string) *exec.Cmd {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("locating the test binary: %v", err)
	}
	cmd := exec.CommandContext(t.Context(), exe, failStartupArg)
	cmd.Env = append(os.Environ(), failStartupEnv+"="+msg)
	return cmd
}
