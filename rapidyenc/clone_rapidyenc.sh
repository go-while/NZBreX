#!/usr/bin/env bash
# rapidyenc C sources are a git submodule pinned in .gitmodules (animetosho/rapidyenc).
# To update: git -C rapidyenc/rapidyenc fetch && git -C rapidyenc/rapidyenc checkout <commit> && git add rapidyenc/rapidyenc
cd "$(dirname "$0")" || exit 1
git submodule update --init -- rapidyenc || exit 1
