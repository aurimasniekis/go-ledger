# Container image for Ledger (ADR-0018). GoReleaser builds the binaries (CGO_ENABLED=0, UI embedded) and copies
# each platform's binary to $TARGETPLATFORM/ledger in the build context. This file runs no commands, so it needs no
# emulation to build for another platform.
# The base is pinned by its index digest. It provides CA certificates and runs as a non-root user.
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab

ARG TARGETPLATFORM
COPY ${TARGETPLATFORM}/ledger /usr/local/bin/ledger

# The database directory must be writable by the non-root user. Mount a volume here to keep data across restarts.
# The timezone is not set here: the first start needs LEDGER_REPORTING_TIMEZONE (an IANA name such as Europe/Vilnius).
COPY --chown=65532:65532 docker/data/ /data/
ENV LEDGER_DATABASE_PATH=/data/ledger.db

# The HTTP listener must bind to all interfaces inside a container. The default is loopback (127.0.0.1:8080).
ENV LEDGER_HTTP_LISTEN=:8080
EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/ledger"]
CMD ["serve"]
