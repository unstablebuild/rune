#!/usr/bin/env bash
#
# Convenience wrapper for the docker-driven workspacessh tests. The Go
# harness (internal/workspace/workspacessh/test/harness.go) calls "docker build"
# directly when the image is missing, so this script is now only for
# manual rebuilds.

set -e

DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
IMAGE_NAME=rune_ssh_workspace_test

docker build -f $DIR/Dockerfile -t $IMAGE_NAME $DIR/../../../
