FROM gcr.io/distroless/static-debian13:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3

ARG TARGETPLATFORM

COPY --chmod=0555 ${TARGETPLATFORM}/vsfleet /usr/bin/vsfleet

# The image upgrades by pulling a new tag, so it never checks for releases.
ENV VSFLEET_NO_UPDATE_NOTIFIER=1

USER 65532:65532
ENTRYPOINT ["/usr/bin/vsfleet"]
