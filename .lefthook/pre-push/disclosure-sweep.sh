#!/bin/sh
# Run as a lefthook script rather than a command: lefthook skips a pre-push
# command whenever HEAD shows no file changes against its push target (its "no
# matching push files"), which is the case for pushing a branch other than HEAD
# or a push whose leak a later commit removed. Scripts carry no such check
# (lefthook v2.1.12, internal/run/controller/command/build_script.go). If a
# later lefthook added one, this sweep would be skipped silently, so re-check
# that file when moving the lefthook pin.
#
# $1 is the destination remote and stdin lists the refs being pushed
# (githooks(5), pre-push).
set -eu
cd "$(git rev-parse --show-toplevel)"
exec go run ./internal/devtools/sweep --pre-push "$1"
