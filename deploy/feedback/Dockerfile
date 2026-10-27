# The deployment script compiles the Go binary locally. This build context
# contains no source credentials, npm configuration, or private module tokens.
FROM gcr.io/distroless/static-debian12:nonroot
COPY feedback /feedback
USER nonroot:nonroot
ENTRYPOINT ["/feedback"]
