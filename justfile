# Crush dev tasks.
# `just check` mirrors the CI gates; `just restamp` rewrites recorded
# request bodies after prompt/tool edits (no API key needed).

default:
    @just --list

# vet + test + build (what CI runs).
check: vet test build

vet:
    go vet ./...

test:
    go test ./...

# Same with the race detector.
test-race:
    go test -race ./...

build:
    go build -o /dev/null .

fmt:
    gofumpt -w .

lint:
    golangci-lint run

# Apply golang.org/x/tools modernize fixes (also enforced by
# golangci-lint in CI).
modernize:
    go run golang.org/x/tools/go/analysis/passes/modernize/cmd/modernize@latest -fix -test ./...

# Regenerate schema.json from the config structs (commit the result).
schema:
    go run main.go schema > schema.json

# Rewrite VCR cassette request bodies from the current prompts and tool
# definitions, replaying recorded responses. Run after editing a prompt
# template or tool description; needs no API key. Re-record with
# `just record` when the model's side of the conversation must change.
restamp:
    go test ./internal/agent -run TestCoderAgent -count=1 -restamp

# Re-record all VCR cassettes against the live provider (needs
# CRUSH_HYPER_API_KEY).
record:
    rm -r internal/agent/testdata
    go test -v -count=1 -timeout=1h ./internal/agent

# Update golden snapshot files after intentional TUI output changes.
update-golden:
    go test ./... -update

# Run the TUI with profiling enabled.
dev:
    CRUSH_PROFILE=true go run .
