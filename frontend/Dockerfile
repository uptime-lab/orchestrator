# Build stage
FROM --platform=$BUILDPLATFORM node:26-slim@sha256:e6bedef217677b276c591795e169840edd9096dbf13f29df8ef8d1856b8820bc AS build
WORKDIR /app

# Pin pnpm to the major matching package.json engines (>=9). pnpm 10 fatally
# blocks unapproved dependency build scripts (esbuild), which breaks this build;
# pnpm 9 runs them as before.
RUN npm install -g pnpm@9
COPY package.json pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile

COPY . .
RUN pnpm run build

# Nginx stage
FROM nginx:alpine@sha256:4a73073bd557c65b759505da037898b61f1be6cbcc3c2c3aeac22d2a470c1752

COPY --from=build /app/dist /usr/share/nginx/html
COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY security-headers.conf /etc/nginx/security-headers.conf

# Listen on an unprivileged port: this container is the most exposed one in the
# stack, and it runs as a non-root user like the backend. A non-root process
# cannot bind :80, so the server listens on :8080 and the reverse proxy
# forwards there.
EXPOSE 8080

# Create a shell script that writes the runtime config to /config.js. An
# EXTERNAL file, not an inline <script> in index.html: the nginx CSP is
# script-src 'self', which an inline block would violate.
RUN printf '%s\n' \
    '#!/bin/sh' \
    'cat > /usr/share/nginx/html/config.js <<EOF' \
    'window.orchestratorConfig = {' \
    "  VITE_API_URL: '\$API_URL'," \
    "  VITE_WEBSOCKET_URL: '\$WEBSOCKET_URL'," \
    "  VITE_ESSENTIALS_API_ONLY: '\$ESSENTIALS_API_ONLY'," \
    "  VITE_TERMS_URL: '\$TERMS_URL'," \
    '};' \
    'EOF' \
    > /usr/local/bin/startup.sh \
    && chmod +x /usr/local/bin/startup.sh

# nginx:alpine@sha256:4a73073bd557c65b759505da037898b61f1be6cbcc3c2c3aeac22d2a470c1752 ships an unprivileged `nginx` user; it just needs to own the
# paths it writes (pid, caches) and the index.html the startup script rewrites.
# The listen port lives in nginx.conf itself rather than a sed here, so a
# pattern that silently fails to match cannot leave the server on :80. The pid
# rewrite matches the DIRECTIVE rather than a hardcoded path, because the base
# image spells it /run/nginx.pid, not /var/run — a path-specific pattern
# matched nothing and left the pid in a root-owned directory the nginx user
# cannot write.
RUN sed -i 's|^pid .*|pid /tmp/nginx.pid;|' /etc/nginx/nginx.conf \
    && chown -R nginx:nginx /usr/share/nginx/html /var/cache/nginx /etc/nginx/conf.d

# By NAME, not a numeric uid: the nginx user is uid 101 in nginx:alpine@sha256:4a73073bd557c65b759505da037898b61f1be6cbcc3c2c3aeac22d2a470c1752, so
# pinning 1000 here would run as a user that owns none of the paths chowned
# above and nginx would fail to write its cache or rewrite index.html.
USER nginx

# Build provenance. GIT_REVISION is the commit the source came from and is
# supplied by the build (`--build-arg GIT_REVISION=$(git rev-parse HEAD)`); it
# defaults to "unknown" so a build without it still succeeds and says so.
#
# This exists because without the label the only way to answer "which commit
# is running" is to compare binaries. A label
# on the image itself travels with the thing it describes, so
# `docker inspect --format '{{index .Config.Labels "org.opencontainers.image.revision"}}'`
# answers it on the host, with no log to keep in step.
ARG GIT_REVISION=unknown
LABEL org.opencontainers.image.revision=$GIT_REVISION
LABEL org.opencontainers.image.source=https://github.com/uptime-lab/orchestrator

# Start NGINX and execute the startup script
# A single -g carrying ONLY "daemon off;":
#   - nginx keeps the LAST -g and silently drops earlier ones, so two -g flags
#     would lose "daemon off" and the container would exit immediately;
#   - the pid path is already rewritten in nginx.conf above, so repeating it
#     here would be a fatal "pid directive is duplicate".
CMD ["/bin/sh", "-c", "/usr/local/bin/startup.sh && nginx -g 'daemon off;'"]
