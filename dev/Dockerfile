FROM golang:1.27 AS builder

# The login flow (/index.php/login/v2) and the SQL app password store need
# reva >= v3.13.0, which needs Go 1.27.
ARG REVA_VERSION=v3.13.0

WORKDIR /home/reva
# CGO is required by the SQLite driver used by the login flow and app password
# stores. Link statically so the binary still runs on the scratch image.
ENV CGO_ENABLED=1
RUN go install github.com/cs3org/gaia@latest && \
    gaia build ${REVA_VERSION} --static --tags netgo,osusergo -o revad

FROM scratch

# COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /home/reva/revad revad

ENTRYPOINT [ "./revad" ]
CMD [ "-c", "/etc/revad/cernbox.toml", "-p", "/var/run/revad.pid" ]
