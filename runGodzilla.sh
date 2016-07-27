#!/bin/sh -eux
PKG=$1
go get -u -t -v ${PKG}
godzilla ${PKG} || true
