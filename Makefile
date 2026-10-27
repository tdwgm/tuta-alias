BIN := bin/tuta-alias
PKG := ./cmd/tuta-alias
# No version or commit is stamped in: the go tool already records both. It
# derives the module version from the git tag (v0.13.0 at the tag, the
# pseudo-version v0.13.1-0.<date>-<sha> after it, +dirty on a modified tree)
# and writes vcs.revision for any build made inside the checkout, which
# survives -trimpath. internal/consts/version.go reads them back, so the
# version lives in exactly one place: the tag.
GOFLAGS := -trimpath -ldflags="-s -w"

# Linux only for now. arm64 is here because that is where the encrypted vault
# matters: Termux runs Linux binaries and has no Secret Service keyring, so
# the keyring credential backend cannot work there. Other systems build fine
# (the tree is pure Go, CGO off), they are just not released:
#   CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build ./cmd/tuta-alias
PLATFORMS := linux/amd64 linux/arm64

.PHONY: build build-all test test-race fuzz tidy lint scan vulns hooks check clean install version publish publish-dry

build:
	CGO_ENABLED=0 go build $(GOFLAGS) -o $(BIN) $(PKG)

build-all:
	@for p in $(PLATFORMS); do \
	  os=$${p%/*}; arch=$${p#*/}; \
	  out=$(BIN)-$$os-$$arch; \
	  echo "  $$os/$$arch -> $$out"; \
	  CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build $(GOFLAGS) -o $$out $(PKG) || exit 1; \
	done

# Build and install in one step, the way a user would.
install: build
	./$(BIN) install

test:
	go test ./... -count=1

test-race:
	go test ./... -count=1 -race

# The config parser is the one place that takes arbitrary bytes from a file
# the user edits by hand.
fuzz:
	go test ./internal/config/ -run FuzzParseRoundTrip -fuzz FuzzParseRoundTrip -fuzztime 60s

tidy:
	go mod tidy

# go vet and gofmt are the fast floor; golangci-lint then applies the rules in
# .golangci.yml. CI pins one golangci-lint release (its version and the
# checksum of its tarball, in .github/workflows/ci.yml) and this asks for the
# same one, because another version can report different things. CGO off
# because that is how the binary is built.
lint:
	go vet ./...
	@out=$$(gofmt -l . | grep -v '^vendor/' || true); \
	  if [ -n "$$out" ]; then echo "gofmt issues:"; echo "$$out"; exit 1; fi
	@want=$$(sed -n 's/^ *GOLANGCI_LINT_VERSION: *//p' .github/workflows/ci.yml | head -n 1); \
	  command -v golangci-lint >/dev/null 2>&1 || { \
	    echo "golangci-lint is not installed."; \
	    echo "  go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v$$want"; \
	    echo "  (or a release binary: https://golangci-lint.run/welcome/install/)"; \
	    exit 2; }; \
	  have=$$(golangci-lint version --short 2>/dev/null); \
	  if [ -n "$$want" ] && [ "$$have" != "$$want" ]; then \
	    echo "note: CI runs golangci-lint $$want and this is $$have; findings, or the config itself, can differ" >&2; \
	  fi; \
	  CGO_ENABLED=0 golangci-lint run

# Sensitive-data removal on the way to git: the working tree AND the history,
# because a secret that was committed and then deleted is still in the clone.
# gitleaks is MIT and is the established tool for this; the project-specific
# rules (personal paths, real addresses, this program's own state files) live
# in .gitleaks.toml.
scan:
	@command -v gitleaks >/dev/null 2>&1 || { \
	  echo "gitleaks is not installed."; \
	  echo "  https://github.com/gitleaks/gitleaks  (or: brew install gitleaks, dnf install gitleaks)"; \
	  exit 2; }
	gitleaks dir . --config .gitleaks.toml --redact --no-banner
	gitleaks git . --config .gitleaks.toml --redact --no-banner

# Known vulnerabilities in the standard library and in the dependencies, the
# same gate CI runs. Source mode: it walks the call graph, so an advisory in a
# package this program does not import is listed and does not fail. The version
# is read from .github/workflows/ci.yml so there is one place to change it.
vulns:
	@want=$$(sed -n 's/^ *GOVULNCHECK_VERSION: *//p' .github/workflows/ci.yml | head -n 1); \
	  command -v govulncheck >/dev/null 2>&1 || { \
	    echo "govulncheck is not installed."; \
	    echo "  (cd /tmp && go install golang.org/x/vuln/cmd/govulncheck@$$want)"; \
	    echo "  install it from outside this checkout: @version ignores this"; \
	    echo "  module, but x/vuln needs Go 1.26 and 'go get -tool' would raise"; \
	    echo "  the go line in go.mod to match."; \
	    exit 2; }; \
	  govulncheck ./...

# What this tree would report as its version, without installing anything.
# The number comes from the git tag, so `git tag` is how it is changed; the
# pre-push hook does that from the commit messages.
version:
	@tmp=$$(mktemp -d); CGO_ENABLED=0 go build $(GOFLAGS) -o $$tmp/tuta-alias $(PKG) \
	  && $$tmp/tuta-alias version | head -1; rm -rf $$tmp
	@printf 'changelog: %s  (what a push would tag)\n' \
	  "$$(sed -n 's/^## \[\{0,1\}\([0-9][0-9.]*\)\]\{0,1\}.*/\1/p' CHANGELOG.md | head -1)"
	@printf 'last tag:  %s\n' "$$(git describe --tags --abbrev=0 2>/dev/null || echo 'none yet')"
	@command -v svu >/dev/null 2>&1 \
	  && printf 'suggested: %s  (from the Conventional Commits since that tag)\n' "$$(svu next)" \
	  || printf 'suggested: unknown (svu is not installed: go install github.com/caarlos0/svu/v3@latest)\n'

# pre-commit scans the staged diff and opens the next version section;
# pre-push tags the version the changelog names.
hooks:
	git config core.hooksPath .githooks
	@echo "hooks enabled:"
	@echo "  pre-commit  scans the staged diff with gitleaks, then bumps the version"
	@echo "  pre-push    tags the version CHANGELOG.md names, once it has no tag"

# What CI runs. `vulns` is last because it is the slowest and the only one
# that can go red without this tree changing.
check: lint test vulns

clean:
	rm -rf bin/

# Copy this tree's published subset into the prod repository, tdwgm/tuta-alias
# on GitHub, and commit the binary built from it. Nothing is ever coded in
# prod; scripts/publish.sh is the only thing that writes there, and it refuses
# a stage tree that is dirty, off main, or not pushed. `make publish-dry` shows
# what it would do and touches nothing.
publish:
	./scripts/publish.sh

publish-dry:
	./scripts/publish.sh --dry-run
