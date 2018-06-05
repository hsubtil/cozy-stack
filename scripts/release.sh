#!/bin/bash
set -xe

RELEASE="$(git describe --tags)"

go get -u -v ./...

GOOS=linux   GOARCH=amd64 ./scripts/build.sh release
GOOS=linux   GOARCH=arm   ./scripts/build.sh release
GOOS=freebsd GOARCH=amd64 ./scripts/build.sh release
rm -f "*.sha256"

sha256sum cozy-stack-*-${RELEASE} > "cozy-stack-${RELEASE}.sha256"
gpg --batch --yes --detach-sign -u 0x51F72B6A45D40BBE "cozy-stack-${RELEASE}.sha256"
