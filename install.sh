#!/bin/sh
# Install Prowl.
#
# One command, no build settings to remember. Prowl needs CGO and the
# sqlite_fts5 build tag: without CGO the build fails inside a transitive
# tree-sitter dependency, and without the tag it builds a binary whose code
# index dies at runtime. Both are set here so neither can be forgotten.
#
#   curl -fsSL https://raw.githubusercontent.com/neur0map/PROWL/main/install.sh | sh
#
# Everything else ships in the binary: the dashboard, the embedded code
# embedder, the provider catalogue and the migrations. First run creates its
# own databases.

set -eu

REPO="github.com/neur0map/prowl"
REF="${PROWL_REF:-latest}"

say() { printf '%s\n' "$*"; }
die() { printf 'install: %s\n' "$*" >&2; exit 1; }

need() {
	command -v "$1" >/dev/null 2>&1 || die "$2"
}

need go 'Go is required. Install Go 1.27 or newer from https://go.dev/dl/ and re-run.'

# A C compiler is not optional: the index parses with tree-sitter and searches
# with sqlite-vec, both C libraries. Checking here turns a confusing "build
# constraints exclude all Go files" error into a sentence.
if ! command -v cc >/dev/null 2>&1 && ! command -v gcc >/dev/null 2>&1 && ! command -v clang >/dev/null 2>&1; then
	die 'A C compiler is required (cc, gcc or clang). Install build tools and re-run.'
fi

GO_VERSION="$(go env GOVERSION 2>/dev/null || echo unknown)"
say "Installing Prowl with ${GO_VERSION}…"

CGO_ENABLED=1 \
GOEXPERIMENT=greenteagc \
	go install -tags=sqlite_fts5 "${REPO}@${REF}" || die 'go install failed.'

BIN="$(go env GOBIN)"
[ -n "$BIN" ] || BIN="$(go env GOPATH)/bin"

say ""
say "Installed: ${BIN}/prowl"

case ":${PATH}:" in
*":${BIN}:"*) ;;
*)
	say ""
	say "${BIN} is not on your PATH. Add it:"
	say "  export PATH=\"\$PATH:${BIN}\""
	;;
esac

say ""
say "Run it in the folder you want to work on:"
say "  prowl"
say ""
say "That starts the agent, the dashboard on http://127.0.0.1:8787/ and every"
say "database it needs. Nothing else to install."
