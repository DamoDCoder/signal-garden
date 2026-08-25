# The daemon as a container image.
#
# This image is assembled from a binary the repository has already
# cross-compiled, rather than compiling inside a builder stage. That is a
# deliberate trade: the build is a copy instead of a module download and a
# compile, and in exchange the image cannot be built from a bare `docker build`
# without `task build:docker` having run first. `task docker:build` runs both in
# order, and is the supported way in.
#
# Two architectures are in play on a developer machine, and they are not
# interchangeable:
#
#   task build         -> bin/signalgardend              darwin/arm64 on a Mac
#   task build:docker  -> bin/linux_arm64/signalgardend  linux/arm64, for here
#
# Same instruction set, different operating system. A darwin binary does not
# start in a Linux container, and the error it gives when it tries — "exec
# format error" — says nothing about why. BINARY names which one to copy, and
# `task docker:build` passes a matching --platform so the image metadata agrees
# with the bytes inside it.
#
# The image is built locally and never pushed. The compose file that runs it
# lives in app.signal-garden and expects to find it in the local image store.
# See docs/decisions/0015-ship-an-image-but-not-a-stack.md.
FROM alpine:3.21

# Defaulted for a bare `docker build` on Apple Silicon; `task docker:build`
# always passes it explicitly.
ARG BINARY=bin/linux_arm64/signalgardend

# The daemon serves one machine and holds no credentials, but it also has no
# reason to be root: it writes one directory and listens on two ports.
RUN adduser -D -u 10001 garden

COPY ${BINARY} /usr/local/bin/signalgardend

# The event log is a library inside the daemon rather than a broker, so run
# history is a mounted directory rather than a service of its own.
ENV SIGNAL_GARDEN_DATA_DIR=/data
RUN mkdir -p /data && chown garden:garden /data
VOLUME /data

# refuse is the default policy and is restated here so it survives someone
# reading the image rather than the source: a log that opens with bytes the disk
# got wrong stops the run instead of quietly serving a different garden.
ENV SIGNAL_GARDEN_ON_CORRUPT=refuse

USER garden

# 8080 is the generated REST gateway and the projection stream; 9090 is gRPC.
EXPOSE 8080 9090

# readyz rather than healthz: a container that is up but not yet serving is not
# ready, and compose gates the client on this.
HEALTHCHECK --interval=5s --timeout=3s --start-period=5s --retries=10 \
  CMD wget --quiet --tries=1 --spider http://localhost:8080/readyz || exit 1

ENTRYPOINT ["/usr/local/bin/signalgardend"]
