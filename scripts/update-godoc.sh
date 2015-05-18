#!/usr/bin/env bash
set -e
set -x

repoPath=$(cd $(dirname $BASH_SOURCE)/.. && pwd)

if [ -z $GOROOT ]; then
  export GOROOT=/usr/local/go
  export PATH=$GOROOT/bin:$PATH
fi

export GOPATH=$repoPath/Godeps/_workspace:$GOPATH
export PATH=$GOPATH/bin:$PATH

cd $(dirname $0)/..

go run scripts/update-godoc/main.go
