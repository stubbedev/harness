# Harness dev tasks.
# `just check` mirrors the CI gates; `just restamp` rewrites recorded
# request bodies after prompt/tool edits (no API key needed).

# The build is pure Go — the SQLite driver needs no cgo — and greenteagc is
# what release builds use, so local builds match.
export CGO_ENABLED := "0"
export GOEXPERIMENT := "greenteagc"

# Stamped into the binary so `harness --version` reports something useful from
# a working copy. Empty outside a git checkout, in which case the version falls
# back to the Go build info.
version := `git describe --long 2>/dev/null || echo ""`
ldflags := if version == "" { "" } else { "-ldflags=-X github.com/stubbedev/harness/internal/version.Version=" + version }

default:
    @just --list

# What CI runs: vet + lint + build + race tests.
check: vet lint build test

vet:
    go vet ./...

# Run the test suite with the race detector, stopping at the first failure.
# The race detector is cgo-only, so it overrides this file's CGO_ENABLED=0.
test *args:
    CGO_ENABLED=1 go test -race -failfast ./... {{ args }}

# Same without the race detector, for a faster loop.
test-fast *args:
    go test ./... {{ args }}

# Build the binary into the working directory.
build:
    go build -v {{ ldflags }} .

# Compile everything without producing a binary.
build-check:
    go build -o /dev/null ./...

# Install the binary into GOBIN.
install:
    git fetch --tags
    go install {{ ldflags }} -v .

fmt:
    gofumpt -w .

# Format the stats page assets.
fmt-html:
    prettier --write internal/cmd/stats/index.html internal/cmd/stats/index.css internal/cmd/stats/index.js

lint: lint-log
    GOEXPERIMENT= golangci-lint run --path-mode=abs --config=".golangci.yml" --timeout=5m

lint-fix:
    GOEXPERIMENT= golangci-lint run --path-mode=abs --config=".golangci.yml" --timeout=5m --fix

# Check that log messages start with capital letters.
lint-log:
    ./scripts/check_log_capitalization.sh

lint-install:
    GOTOOLCHAIN=go1.26.6 go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest

# Apply golang.org/x/tools modernize fixes (golangci-lint enforces them too).
modernize:
    go run golang.org/x/tools/go/analysis/passes/modernize/cmd/modernize@latest -fix -test ./...

# Build, then run the binary with any extra arguments.
run *args: build
    ./harness {{ args }}

# Run against a Catwalk instance on localhost.
run-catwalk *args: build
    CATWALK_URL=http://localhost:8080 ./harness {{ args }}

# Run against a throwaway config and data directory, to exercise onboarding.
run-onboarding *args: build
    rm -rf tmp/onboarding
    HARNESS_GLOBAL_DATA=tmp/onboarding/data HARNESS_GLOBAL_CONFIG=tmp/onboarding/config ./harness {{ args }}

# Run the TUI with profiling enabled.
dev:
    HARNESS_PROFILE=true go run .

# 10s CPU profile of a running `just dev` session.
profile-cpu:
    go tool pprof -http :6061 'http://localhost:6060/debug/pprof/profile?seconds=10'

profile-heap:
    go tool pprof -http :6061 'http://localhost:6060/debug/pprof/heap'

profile-allocs:
    go tool pprof -http :6061 'http://localhost:6060/debug/pprof/allocs'

# Regenerate schema.json from the config structs (commit the result).
schema:
    go run main.go schema > schema.json

# Regenerate the OpenAPI spec from the swag annotations.
swag:
    go run github.com/swaggo/swag/cmd/swag@v1.16.6 init --generalInfo main.go --dir . --output internal/swagger --packageName swagger --parseDependency --parseInternal --parseDepth 5

# Regenerate the database layer from internal/db/sql.
sqlc:
    sqlc generate

# Update the embedded Hyper provider catalog.
hyper:
    go generate ./internal/agent/hyper/...

# GOPROXY=direct because the module proxy lags a fresh release by minutes.
# Update the upstream provider and LLM libraries.
deps:
    GOPROXY=direct GONOSUMDB='charm.land/*' go get charm.land/fantasy@latest
    GOPROXY=direct GONOSUMDB='charm.land/*' go get charm.land/catwalk@latest
    go mod tidy

# Run after editing a prompt template or tool description; needs no API key.
# Re-record with `just record` when the model's side of the conversation
# must change.
# Rewrite VCR cassette request bodies from the current prompts and tools.
restamp:
    go test ./internal/agent -run TestCoderAgent -count=1 -restamp

# Needs a key for whichever provider the cassettes target.
# Re-record all VCR cassettes against the live provider.
record:
    rm -r internal/agent/testdata
    go test -v -count=1 -timeout=1h ./internal/agent

# Update golden snapshot files after intentional TUI output changes.
update-golden:
    go test ./... -update

# CI does this on every dependency change (see .github/workflows/flake.yml);
# run it locally when you want `nix build` to work before pushing.
# Recompute package.nix's vendorHash from go.mod/go.sum.
nix-vendor-hash:
    #!/usr/bin/env bash
    set -euo pipefail
    FAKE="sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
    CUR="$(grep -oP 'vendorHash = "\K[^"]+' package.nix)"
    sed -i "s#vendorHash = \"${CUR}\"#vendorHash = \"${FAKE}\"#" package.nix
    GOT="$(nix build .#default --no-link 2>&1 | grep -oP 'got:\s+\K(sha256-\S+)' | head -1 || true)"
    [ -z "$GOT" ] && GOT="$CUR"
    sed -i "s#vendorHash = \"${FAKE}\"#vendorHash = \"${GOT}\"#" package.nix
    echo "vendorHash = ${GOT}"

# Build the Nix package and print the store path.
nix-build:
    nix build .#default --no-link --print-out-paths

# Show the next major/minor/patch versions.
release-preview:
    #!/usr/bin/env bash
    set -euo pipefail
    v="$(git describe --tags --abbrev=0 --match 'v*' 2>/dev/null || echo v0.0.0)"
    IFS=. read -r maj min pat <<<"${v#v}"
    echo "current: $v"
    echo "patch:   v$maj.$min.$((pat + 1))"
    echo "minor:   v$maj.$((min + 1)).0"
    echo "major:   v$((maj + 1)).0.0"

release-patch: (release "patch")
release-minor: (release "minor")
release-major: (release "major")

# The version lives in the git tag: the binary reads it from the build info and
# publish.yml stamps it into the ldflags, so there is no version file to bump.
# Syncs the flake vendorHash, runs the gates, tags, and pushes -- the tag push
# is what triggers .github/workflows/publish.yml.
# Tag a release and push it.
release level:
    #!/usr/bin/env bash
    set -euo pipefail
    if ! git diff --quiet || ! git diff --cached --quiet; then
        echo "working tree is dirty — commit or stash first" >&2
        exit 1
    fi
    git fetch --tags --quiet
    v="$(git describe --tags --abbrev=0 --match 'v*' 2>/dev/null || echo v0.0.0)"
    IFS=. read -r maj min pat <<<"${v#v}"
    case "{{ level }}" in
        patch) new="v$maj.$min.$((pat + 1))" ;;
        minor) new="v$maj.$((min + 1)).0" ;;
        major) new="v$((maj + 1)).0.0" ;;
        *) echo "unknown level: {{ level }}" >&2; exit 1 ;;
    esac
    if git rev-parse -q --verify "refs/tags/$new" >/dev/null; then
        echo "tag $new already exists" >&2
        exit 1
    fi
    echo "releasing $v -> $new"
    just nix-vendor-hash
    just check
    # nix-vendor-hash rewrites package.nix when dependencies moved; that has to
    # land before the tag so the tagged tree builds under Nix.
    if ! git diff --quiet package.nix; then
        git add package.nix
        git commit -m "chore(nix): update vendorHash for $new"
    fi
    git tag --annotate -m "$new" "$new"
    git push origin HEAD
    git push origin "$new"
    echo "released $new"
