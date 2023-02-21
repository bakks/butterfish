#!/bin/bash

git clone --recursive https://github.com/Microsoft/onnxruntime.git
cd onnxruntime

defines="CMAKE_OSX_ARCHITECTURES=x86_64 onnxruntime_RUN_ONNX_TESTS=OFF onnxruntime_GENERATE_TEST_REPORTS=OFF onnxruntime_BUILD_UNIT_TESTS=OFF"
./build.sh --config Release --build_shared_lib --parallel --cmake_extra_defines $defines

cd ..
mkdir -p lib/amd64-build/lib
mkdir -p lib/amd64-build/include
target=lib/amd64-build/lib

cp onnxbuild/onnxruntime/build/MacOS/Release/*.dylib $target/
cp onnxbuild/onnxruntime/build/MacOS/Release/*.a $target/
cp onnxbuild/onnxruntime/build/MacOS/Release/*.h $target/
cp onnxbuild/onnxruntime/build/MacOS/Release/*.lds $target/
cp onnxbuild/onnxruntime/build/MacOS/Release/*.json $target/

cd onnxruntime

defines="CMAKE_OSX_ARCHITECTURES=arm64 onnxruntime_RUN_ONNX_TESTS=OFF onnxruntime_GENERATE_TEST_REPORTS=OFF onnxruntime_BUILD_UNIT_TESTS=OFF USE_COREML=1"
./build.sh --config RelWithDebInfo --build_shared_lib --parallel --cmake_extra_defines CMAKE_OSX_ARCHITECTURES=arm64
