#!/usr/bin/env bash
# .github/scripts/check-image-go.sh <toolchain> <Dockerfile>
#
# Fails (exit 1) unless the Dockerfile's builder stage, the stage named
# `builder`, is the official golang image tagged with <toolchain>'s version.
# gate.sh passes the toolchain go.mod pins, the one CI's tests run on, so the
# image production runs is built by the Go that was tested.
#
# The golang image builds with the Go it ships (its GOTOOLCHAIN is local), so
# its tag is the image's Go. Dependabot bumps that tag on its own, and this
# check fails the bump until go.mod's toolchain line moves with it.

set -euo pipefail

toolchain=${1:?usage: check-image-go.sh <toolchain> <Dockerfile>}
dockerfile=${2:?usage: check-image-go.sh <toolchain> <Dockerfile>}

want=golang:${toolchain#go}
got=$(awk '$1 == "FROM" && $(NF-1) == "AS" && $NF == "builder" { print $2; exit }' "$dockerfile")

if [[ "$got" != "$want" ]]; then
    cat >&2 <<EOF
FAIL: the builder stage in $dockerfile is ${got:-missing}, but CI tests on $toolchain.
Build the image on $want, or move go.mod's toolchain line to the image's Go
in the same change.
EOF
    exit 1
fi
echo "ok: the image builds on $want, the Go CI tests on"
