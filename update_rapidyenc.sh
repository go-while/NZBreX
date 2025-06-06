#!/bin/bash
cd rapidyenc || exit 2
git checkout || exit 3
git pull || exit 4
cd ../..
git add rapidyenc
git commit -m "Update submodule: rapidyenc"

