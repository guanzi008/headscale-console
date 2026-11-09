FROM debian:bookworm-slim

ARG BINARY_PATH=dist/headscale-console

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates libstdc++6 \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid 10001 headscale-console \
    && useradd --uid 10001 --gid 10001 --home-dir /data --no-create-home \
        --shell /usr/sbin/nologin headscale-console \
    && install -d -o 10001 -g 10001 -m 0700 /data

COPY ${BINARY_PATH} /usr/local/bin/headscale-console
RUN chmod 0755 /usr/local/bin/headscale-console

USER 10001:10001
VOLUME ["/data"]
EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/headscale-console"]
CMD ["--data-dir", "/data"]
