#!/bin/sh

set -e

if [ ! -f "build/env.sh" ]; then
    echo "$0 must be run from the root of the repository."
    exit 2
fi

# Create fake Go workspace if it doesn't exist yet.
workspace="$PWD/build/_workspace"
root="$PWD"
cpxdir="$workspace/src/github.com/cpexc"
if [ ! -L "$cpxdir/go-cpx" ]; then
    mkdir -p "$cpxdir"
    cd "$cpxdir"
    ln -s ../../../../../. go-cpx
    cd "$root"
fi

# Set up the environment to use the workspace.
GOPATH="$workspace"
export GOPATH

# Run the command inside the workspace.
cd "$cpxdir/go-cpx"
PWD="$cpxdir/go-cpx"

# Launch the arguments with the configured environment.
exec "$@"
