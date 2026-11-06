#!/bin/sh
# Build a relocatable arm64 native engine without linking MLX into Pharos.
# OUT contains the engine, runtime dependencies, Metal library and licenses.
set -eu
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
OUT=${1:-"$ROOT/build/labeler-mlx"}
# TODO: Shrink the bundled MLX runtime (~238 MB; metallib ~214 MB).
# Revisit upstream JIT/MinSizeRel after compatibility and timing validation;
# see the high-priority TODO in docs/local-labeler.md#apple-silicon-gpu.
MLX=$(python3 "$ROOT/macos/fetch-mlx.py")
mkdir -p "$OUT"
# A worker may target macOS 14; it guards dlopen of this optional 26.2 engine.
clang++ -std=c++20 -O2 -dynamiclib -arch arm64 -mmacosx-version-min=26.2 \
    -I"$MLX/include" "$ROOT/macos/labeler-mlx/engine.cc" -L"$MLX/lib" -lmlx \
    -Wl,-rpath,@loader_path -Wl,-install_name,@rpath/libpharos_mlx.dylib -o "$OUT/libpharos_mlx.dylib"
cp "$MLX/lib/libmlx.dylib" "$MLX/lib/libjaccl.dylib" "$MLX/lib/mlx.metallib" "$OUT/"
# Upstream libmlx uses @rpath for jaccl and locates its metallib beside itself.
for name in libmlx.dylib libjaccl.dylib libpharos_mlx.dylib; do
    lipo "$OUT/$name" -verify_arch arm64
    otool -L "$OUT/$name"
done
rm -rf "$OUT/licenses"
cp -R "$MLX/licenses" "$OUT/licenses"
