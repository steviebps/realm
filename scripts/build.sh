#!/usr/bin/env bash
set -x

go build -ldflags "-s -w" -tags=ui

./realm server --stdouttraces -c ./configs/realm.json 