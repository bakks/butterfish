#!/bin/bash

mkdir -p lib

cd lib

oras pull ghcr.io/homebrew/core/onnxruntime:1.14.0

mkdir -p arm64
mkdir -p amd64

tar xzvf onnxruntime--1.14.0.arm64_ventura.bottle.tar.gz -C ./arm64
tar xzvf onnxruntime--1.14.0.ventura.bottle.tar.gz -C ./amd64

rm *.tar.gz

cd ..

