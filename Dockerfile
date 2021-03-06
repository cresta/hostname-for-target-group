ARG BUILDER_IMAGE
# hadolint ignore=DL3006
FROM ${BUILDER_IMAGE} as builder
RUN mkdir /empty_dir
COPY . /work
RUN ./make.sh build

FROM scratch
# Import from builder.
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /etc/passwd /etc/passwd
COPY --from=builder /etc/group /etc/group
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo

# Copy our static executable
COPY --from=builder /work/hostname-for-target-group /hostname-for-target-group
COPY --chown=appuser --from=builder /empty_dir /tmp
# Use an unprivileged user.
USER appuser:appuser


EXPOSE 8080
ENTRYPOINT ["/hostname-for-target-group"]
