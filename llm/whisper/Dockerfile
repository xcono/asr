# whisper.cpp `whisper-server` with CUDA, built natively for Blackwell (RTX 50xx, sm_120).
#
# WHY A LOCAL BUILD (vs the upstream ghcr.io/ggml-org/whisper.cpp:main-cuda image)
#   Upstream's .devops/main-cuda.Dockerfile hard-codes
#   CMAKE_CUDA_ARCHITECTURES='75;80;86;90', so its image carries no sm_120 code. On an
#   RTX 5060 Ti the driver then JIT-compiles every ggml kernel on first use: ~20 s for the
#   first request after start and 1-4 s once per new short-prompt length. ggml itself lists
#   `120a-real` for CUDA >= 12.8 (ggml/src/ggml-cuda/CMakeLists.txt); upstream's Dockerfile
#   overrides that list. This file is upstream's Dockerfile with that one argument changed
#   and the source pinned to a commit instead of copied from a checkout.
#
# `120a-real` = device code for Blackwell only: this image does NOT run on older cards
# (override the CUDA_ARCHITECTURES build arg if that ever matters).
#
# Needs CUDA >= 12.8 to target sm_120; 13.0.0 matches upstream. DO NOT build in a sandbox
# without network access; the build itself needs no GPU.

ARG UBUNTU_VERSION=22.04
ARG CUDA_VERSION=13.0.0
ARG BASE_CUDA_DEV_CONTAINER=nvidia/cuda:${CUDA_VERSION}-devel-ubuntu${UBUNTU_VERSION}
ARG BASE_CUDA_RUN_CONTAINER=nvidia/cuda:${CUDA_VERSION}-runtime-ubuntu${UBUNTU_VERSION}

FROM ${BASE_CUDA_DEV_CONTAINER} AS build

# Same commit as the upstream image this replaces (main-cuda of 2026-11-16).
ARG WHISPER_CPP_COMMIT=5922450fbce1ed9d1648babd0d5ac4520055b436
ARG CUDA_ARCHITECTURES=120a-real

RUN apt-get update && \
    apt-get install -y build-essential libsdl2-dev wget cmake git \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/* /var/cache/apt/archives/*

# The build stage has no real GPU/driver, so the linker needs the forward-compat libcuda
# stub to resolve CUDA driver-API symbols (same as upstream).
ENV CUDA_MAIN_VERSION=13.0
ENV LD_LIBRARY_PATH=/usr/local/cuda-${CUDA_MAIN_VERSION}/compat:$LD_LIBRARY_PATH

WORKDIR /app
RUN git init -q . \
    && git remote add origin https://github.com/ggml-org/whisper.cpp.git \
    && git fetch -q --depth 1 origin ${WHISPER_CPP_COMMIT} \
    && git checkout -q FETCH_HEAD

# Upstream runs `make base.en`, which also downloads a model and transcribes samples;
# `make build` is the same cmake configure + build without that.
RUN make build CMAKE_ARGS="-DGGML_CUDA=1 -DCMAKE_CUDA_ARCHITECTURES='${CUDA_ARCHITECTURES}' -DGGML_NATIVE=OFF"

RUN find /app/build -name "*.o" -delete && \
    find /app/build -name "*.a" -delete && \
    rm -rf /app/build/CMakeFiles /app/build/cmake_install.cmake /app/build/_deps /app/.git

FROM ${BASE_CUDA_RUN_CONTAINER} AS runtime
WORKDIR /app

RUN apt-get update && \
    apt-get install -y curl ffmpeg wget \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/* /var/cache/apt/archives/*

COPY --from=build /app /app
ENV PATH=/app/build/bin:$PATH
ENTRYPOINT [ "bash", "-c" ]
