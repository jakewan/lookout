# Must stay first: a recipe above this one becomes the default.

# Show this list. Running `just` with no recipe does the same.
default:
    @just --list

# Build the binary
build:
    @mkdir -p bin
    go build -o bin/lookout ./cmd/lookout

# Install the binary to ~/.local/bin (atomic cp+mv — survives "text file busy")
install: build
    @mkdir -p ~/.local/bin
    # cp+mv, not cp alone: mv swaps the directory entry atomically, so the install
    # succeeds even while an older copy is running (cp alone fails with "text file
    # busy" on Linux when overwriting a running binary).
    cp bin/lookout ~/.local/bin/lookout.tmp && mv ~/.local/bin/lookout.tmp ~/.local/bin/lookout
    @echo "Installed to ~/.local/bin/lookout"
    @echo "(ensure ~/.local/bin is in your PATH)"

# Run all tests
test:
    go test ./...

# Run all tests under the race detector (what CI and the pre-push hook run)
test-race:
    go test -race ./...

# Run tests with coverage
test-coverage:
    go test -coverprofile=coverage.out ./...
    go tool cover -html=coverage.out

# Lint with golangci-lint
lint:
    golangci-lint run ./...

# The formatter set is gofmt + goimports, per .golangci.yml. Plain `gofmt -w .`
# would leave import ordering untouched, so a contributor who formats and pushes
# without the commit hook installed could still fail the lint job.

# Format code with the formatter set CI enforces
fmt:
    golangci-lint fmt ./...

# Tidy module dependencies
tidy:
    go mod tidy

# Verify module dependencies
verify:
    go mod verify

# Fail if go.mod/go.sum are not tidy (reports the diff, does not rewrite)
tidy-check:
    go mod tidy -diff

# Scan dependencies and the standard library for known vulnerabilities
vuln:
    go tool govulncheck ./...

# Sweep the tree and unpushed commits for terms in the off-repo denylist
sweep:
    go run ./internal/devtools/sweep

# Positional arguments reach the command as "$@", each quoted, so a path with
# spaces or shell characters arrives intact; `--` keeps one that begins with a
# dash from being read as a flag. Paths resolve from the repository root.

# Sweep drafted PR or issue bodies before posting them
[positional-arguments]
sweep-file +files:
    go run ./internal/devtools/sweep -- "$@"

# --bump is required, not cosmetic: every pin in mise.toml is an exact version,
# and plain `mise outdated` compares against the latest release matching the
# requested spec — which for an exact pin is always the pin itself, so it
# reports "up to date" no matter how far upstream has moved. --local keeps a
# contributor's global mise config out of the report.

# Report mise-managed tools with newer versions available
toolchain-outdated:
    mise outdated --bump --local

# Clean build artifacts
clean:
    rm -rf bin/
    rm -f coverage.out

# Install git hooks via lefthook
hooks:
    lefthook install
