#!/bin/bash
# gen.sh MODE -o OUT.json
#
# Records compattest results (cases.go, record.go) built against:
#   --upstream REV       github.com/klauspost/compress at a release tag, pseudo-version or
#                        commit, from the Go module proxy and verified against sum.golang.org
#                        (how goldens are made)
#   --upstream-tree DIR  an upstream source tree (for scanning upstream commits)
#   --fork DIR           this fork's source tree (default output, no compat option)
# GEN_TAGS sets build tags.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
ct=$(dirname "$here")
upstream=github.com/klauspost/compress
fork=github.com/moby/klauspost-compress

mode= arg= out=
while [ $# -gt 0 ]; do
	case $1 in
	--upstream | --upstream-tree | --fork) mode=${1#--}; arg=$2; shift 2 ;;
	-o) out=$2; shift 2 ;;
	*) echo "usage: gen.sh (--upstream VERSION | --upstream-tree DIR | --fork DIR) -o OUT" >&2; exit 2 ;;
	esac
done
[ -n "$mode" ] && [ -n "$out" ] || { echo "usage: gen.sh (--upstream VERSION | --upstream-tree DIR | --fork DIR) -o OUT" >&2; exit 2; }
out=$(cd "$(dirname "$out")" && pwd)/$(basename "$out")

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
mkdir "$tmp/compattest"
cp "$ct/cases.go" "$ct/record.go" "$tmp/compattest/"
cp "$here/main.go" "$tmp/"

case $mode in
upstream)
	mod=$upstream repl=
	case $arg in
	v*) ver=$arg ;;
	*) # a commit: resolve it to its pseudo-version
		ver=$(cd "$tmp" && printf 'module resolve\ngo 1.25\n' > go.mod && GOFLAGS=-mod=mod GOPROXY=https://proxy.golang.org \
			GOSUMDB=sum.golang.org GONOSUMDB= GOPRIVATE= GONOPROXY= GOINSECURE= go list -m -f '{{.Version}}' "$mod@$arg") || exit 1
		rm -f "$tmp/go.mod" ;;
	esac ;;
upstream-tree) mod=$upstream ver=v1.20.2 repl=$(cd "$arg" && pwd) ;;
fork) mod=$fork ver=v0.0.0 repl=$(cd "$arg" && pwd) ;;
esac
if [ "$mod" != "$fork" ]; then
	sed -i "s#\"$fork/#\"$mod/#g" "$tmp"/compattest/*.go
fi
{
	echo "module compatgen"
	echo "go 1.25"
	echo "require $mod $ver"
	[ -n "$repl" ] && echo "replace $mod => $repl"
} > "$tmp/go.mod"

cd "$tmp"
# Always fetch through the public proxy and verify against sum.golang.org, whatever the
# caller's Go environment says.
export GOWORK=off GOFLAGS=-mod=mod GOPROXY=https://proxy.golang.org GOSUMDB=sum.golang.org \
	GONOSUMDB= GONOSUMCHECK= GOPRIVATE= GONOPROXY= GOINSECURE=
go mod download "$mod" 2>/dev/null || true
case $mode in
upstream) source="$mod@$ver $(go list -m -f '{{.Sum}}' "$mod")" ;;
upstream-tree) source="tree $repl" ;;
fork) source="fork $repl" ;;
esac
go run -tags="${GEN_TAGS:-}" . -corpus "$ct/testdata/corpus" -o "$out" -source "$source"
