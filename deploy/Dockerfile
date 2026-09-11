# imot-mcp runtime image.
#
# The binary is built on the workstation (CGO_ENABLED=0, linux/amd64) and copied
# in, matching how the imot CLI is delivered to the other VPS workers.
FROM debian:bookworm-slim

# CA certificates are required because scraping egresses through the imot VPN
# proxy over TLS, and nginx talks to this container over plain HTTP.
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/*

COPY imot-mcp /usr/local/bin/imot-mcp

# Cache lives on a mounted volume; /data is the default when nothing is mounted.
ENV IMOT_MCP_DB=/data/mcp-cache.db
RUN mkdir -p /data

EXPOSE 8099

ENTRYPOINT ["/usr/local/bin/imot-mcp"]
