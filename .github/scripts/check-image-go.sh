#!/usr/bin/env bash
# .github/scripts/check-image-go.sh <Dockerfile> <go.mod>
#
# Prints the version in the golang tag of the Dockerfile's builder stage, the
# stage named `builder`, such as 1.27.2. That tag names the Go CI, the gate and
# the image run on (ADR 0005). gate.sh sets GOTOOLCHAIN from it, and CI's
# setup-go steps install it. The golang image builds with the Go it ships (its
# GOTOOLCHAIN is local), so the tag is the image's Go.
#
# Fails (exit 1) when the builder's image isn't golang:X.Y.Z, since a floating
# tag or a digest names no one release, or when go.mod has a toolchain line,
# which would name a second Go.

set -euo pipefail

dockerfile=${1:?usage: check-image-go.sh <Dockerfile> <go.mod>}
gomod=${2:?usage: check-image-go.sh <Dockerfile> <go.mod>}

image=$(awk '$1 == "FROM" && $(NF-1) == "AS" && $NF == "builder" { print $2; exit }' "$dockerfile")
if [[ ! "$image" =~ ^golang:[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    cat >&2 <<EOF
FAIL: the builder stage in $dockerfile is ${image:-missing}. It has to be on
golang:X.Y.Z, one exact Go release, since that tag names the Go that CI, the
gate and the image run on.
EOF
    exit 1
fi

toolchain=$(awk '$1 == "toolchain"' "$gomod")
if [[ -n "$toolchain" ]]; then
    cat >&2 <<EOF
FAIL: $gomod has a toolchain line, "$toolchain". The golang tag of the
builder stage in $dockerfile names the Go that CI, the gate and the image run
on, so remove the line. go get adds it when it raises the go line, which also
moves the binary's GODEBUG defaults, so check that change too.
EOF
    exit 1
fi

echo "${image#golang:}"
