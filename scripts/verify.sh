#!/usr/bin/env sh
set -eu
cd "$(dirname "$0")/../backend"
go test ./...
cd ../frontend
npm run build
