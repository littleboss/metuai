# syntax=docker/dockerfile:1
# Static Vite + Tailwind (Aura) build served by nginx.
# Proxies /v1 /healthz /readyz to the api service. Do not put JWT secrets here.
FROM node:22-alpine AS build
WORKDIR /app
RUN corepack enable && corepack prepare pnpm@9.15.9 --activate
COPY package.json pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
# Vite + Tailwind v4 (@tailwindcss/vite); CSS lives under src/. Include any
# future tailwind/postcss config files at the app root.
COPY index.html vite.config.ts tsconfig.json tsconfig.app.json tsconfig.node.json ./
COPY *.css ./
COPY public ./public
COPY src ./src
RUN pnpm build

FROM nginx:1.27-alpine
RUN apk add --no-cache wget
COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /app/dist /usr/share/nginx/html
EXPOSE 5173
HEALTHCHECK --interval=5s --timeout=3s --start-period=5s --retries=10 \
	CMD wget -qO- http://127.0.0.1:5173/ >/dev/null 2>&1 || exit 1
