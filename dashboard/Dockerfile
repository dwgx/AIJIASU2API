# syntax=docker/dockerfile:1

# Build context is the repository root, not dashboard/: the runtime image must
# carry the root LICENSE/NOTICE/THIRD-PARTY-NOTICES.md files, and a Docker
# context cannot reach outside itself.
#
#   docker build -f dashboard/Dockerfile -t ajs-dashboard:local .

FROM node:24-alpine AS build
WORKDIR /app

COPY dashboard/package.json dashboard/package-lock.json ./
RUN npm ci --no-audit --no-fund

COPY dashboard/tsconfig.json dashboard/tsconfig.app.json dashboard/tsconfig.node.json dashboard/vite.config.ts dashboard/index.html ./
COPY dashboard/public ./public
COPY dashboard/src ./src
COPY dashboard/tests ./tests

# This image is the production dashboard: it must be live and token-free.
# Vite inlines every VITE_* value into public JavaScript, so VITE_PANEL_TOKEN
# is deliberately not a build argument. The mock console is a development
# mode (`npm run dev`, or dashboard/compose.yml with the explicit
# AJS_ALLOW_MOCK_BUILD=true opt-in, which builds through `npm run build:mock`),
# not a release artifact.
ARG VITE_USE_MOCK=false
ARG VITE_API_BASE=
ARG AJS_ALLOW_MOCK_BUILD=false
ENV VITE_USE_MOCK=${VITE_USE_MOCK} \
    VITE_API_BASE=${VITE_API_BASE}

RUN if [ "$VITE_USE_MOCK" != "false" ] && [ "$AJS_ALLOW_MOCK_BUILD" != "true" ]; then \
      echo "dashboard image refuses a mock build; set VITE_USE_MOCK=false (release) or AJS_ALLOW_MOCK_BUILD=true (local demo only)" >&2; \
      exit 1; \
    fi \
 && if [ "$VITE_USE_MOCK" = "false" ]; then npm run build; else npm run build:mock; fi

FROM nginx:1.29-alpine AS runtime

RUN printf '%s\n' \
  'server {' \
  '  listen 80;' \
  '  server_name _;' \
  '  root /usr/share/nginx/html;' \
  '  index index.html;' \
  '' \
  '  location = /healthz {' \
  '    access_log off;' \
  '    add_header Content-Type text/plain;' \
  '    return 200 "ok\n";' \
  '  }' \
  '' \
  '  location /assets/ {' \
  '    add_header Cache-Control "public, max-age=31536000, immutable";' \
  '    try_files $uri =404;' \
  '  }' \
  '' \
  '  location / {' \
  '    try_files $uri $uri/ /index.html;' \
  '  }' \
  '}' > /etc/nginx/conf.d/default.conf

COPY --from=build /app/dist /usr/share/nginx/html

# Redistribution obligations: the MIT project license plus the third-party
# notices (fonts OFL, Lucide ISC, D3 BSD) travel inside the image.
COPY LICENSE NOTICE THIRD-PARTY-NOTICES.md /usr/share/licenses/ajs-dashboard/

EXPOSE 80
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s \
  CMD wget -qO- http://127.0.0.1/healthz >/dev/null || exit 1
