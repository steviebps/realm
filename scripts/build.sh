#!/usr/bin/env bash
set -x

cd http/realm-ui &&

npm i && npm run build &&

cd ../..

go build -ldflags "-s -w" -tags=ui &&

./realm server --notraces -c ./configs/realm.json 2> ./server.log
