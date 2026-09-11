# Harness dev tasks.
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
# HARNESS_HYPER_API_KEY).
record:
    rm -r internal/agent/testdata
    go test -v -count=1 -timeout=1h ./internal/agent

# Update golden snapshot files after intentional TUI output changes.
update-golden:
    go test ./... -update

# Run the TUI with profiling enabled.
dev:
    HARNESS_PROFILE=true go run .

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
