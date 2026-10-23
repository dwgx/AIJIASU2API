# syntax=docker/dockerfile:1

FROM node:24-alpine AS build
WORKDIR /app

COPY package.json ./
RUN npm install --no-audit --no-fund

COPY tsconfig.json tsconfig.app.json tsconfig.node.json vite.config.ts index.html ./
COPY public ./public
COPY src ./src
COPY tests ./tests

# Vite inlines every VITE_* value into the bundle at build time. The mock build
# is the default; live mode is opt-in and its token is readable by anyone who
# can fetch the bundle, so it belongs on loopback only.
ARG VITE_USE_MOCK=true
ARG VITE_API_BASE=
ARG VITE_PANEL_TOKEN=
ENV VITE_USE_MOCK=${VITE_USE_MOCK} \
    VITE_API_BASE=${VITE_API_BASE} \
    VITE_PANEL_TOKEN=${VITE_PANEL_TOKEN}

RUN npm run build

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

EXPOSE 80
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s \
  CMD wget -qO- http://127.0.0.1/healthz >/dev/null || exit 1
