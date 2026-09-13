# The proxy and the SPA are one image on purpose. Caddy already has to be here
# to route /api to the Go binary, and it serves static files perfectly well, so
# a second web server would be a second place for headers and caching rules to
# disagree with each other.

# Build the Hebrew RTL bundle.
FROM node:22-alpine AS build
WORKDIR /src

COPY apps/web/package.json apps/web/package-lock.json* ./
RUN npm ci || npm install

COPY apps/web/ ./
RUN npm run build

# Serve it, and reverse-proxy the API.
FROM caddy:2-alpine
COPY --from=build /src/dist /srv
