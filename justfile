# Harness dev tasks.
# `just check` mirrors the CI gates. No task here needs an API key: the
# agent tests drive a scripted model rather than a recorded provider.

# The build is pure Go — the SQLite driver needs no cgo — and greenteagc is
# what release builds use, so local builds match.
export CGO_ENABLED := "0"
export GOEXPERIMENT := "greenteagc"

# Stamped into the binary so `harness --version` reports something useful from
# a working copy. Empty outside a git checkout, in which case the version falls
# back to the Go build info.
version := `git describe --long 2>/dev/null || echo ""`
# Quoted at every use: the value holds a space, and an unquoted `{{ ldflags }}`
# is split into two arguments by the shell. Empty means an empty -ldflags, which
# the linker ignores, so the version falls back to the Go build info.
ldflags := if version == "" { "-ldflags=" } else { "-ldflags=-X github.com/stubbedev/harness/internal/version.Version=" + version }

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
    go build -v "{{ ldflags }}" .

# Compile everything without producing a binary.
build-check:
    go build -o /dev/null ./...

# Install the binary into GOBIN.
install:
    git fetch --tags
    go install "{{ ldflags }}" -v .

# Format the tree with the same gofumpt `just lint` gates on, so the two
# can never disagree. A standalone `gofumpt` binary is version-coupled to
# the Go it was built with: one built before this module's Go cannot parse
# the generic methods in internal/app, and one built after formats
# internal/cmd/session.go differently than the linter accepts. Going
# through golangci-lint sidesteps both.
fmt:
    GOEXPERIMENT= golangci-lint fmt --config=".golangci.yml"

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

# GOPROXY=direct because the module proxy lags a fresh release by minutes.
# Update the upstream provider and LLM libraries.
deps:
    GOPROXY=direct GONOSUMDB='charm.land/*' go get charm.land/fantasy@latest
    go mod tidy

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
    # GitHub honours [skip ci] on a tag push too, so a tag landing on the
    # vendorHash commit that .github/workflows/flake.yml pushes ("chore(nix):
    # update vendorHash [skip ci]") creates the tag and the release quietly
    # never builds. Put an empty commit under the tag in that case.
    # The flake workflow can push its own vendorHash commit to main while
    # this recipe runs; both commits carry the same content, so rebasing
    # onto theirs drops ours as empty and both sides converge instead of
    # a push being rejected. Re-checking the guard and re-tagging every
    # iteration keeps the tag on the head that actually lands.
    for _ in 1 2 3 4 5; do
        git fetch origin main --quiet
        git rebase origin/main
        if git log -1 --format=%B | grep -qiE '\[(skip ci|ci skip)\]'; then
            git commit --allow-empty -m "chore: release $new"
        fi
        git tag -f --annotate -m "$new" "$new"
        if git push origin HEAD && git push origin "$new"; then
            echo "released $new"
            exit 0
        fi
    done
    echo "could not push $new after 5 attempts" >&2
    exit 1
