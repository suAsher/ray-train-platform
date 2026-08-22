# Stage 1: Build static assets
ARG NODE_BUILDER_IMAGE=node:20-alpine
ARG NGINX_RUNTIME_IMAGE=nginx:1.27-alpine
FROM ${NODE_BUILDER_IMAGE} AS builder

ARG NPM_REGISTRY=https://registry.npmmirror.com

WORKDIR /app
COPY package*.json ./
RUN npm install --registry="${NPM_REGISTRY}"

COPY . .
RUN npm run build

# Stage 2: Serve with Nginx
FROM ${NGINX_RUNTIME_IMAGE}

# Harbor's nginx-unprivileged image deliberately sets a non-root default user.
# Switch only for image-time configuration, then restore the nginx user below.
USER root

COPY --from=builder /app/dist /usr/share/nginx/html
COPY nginx.conf /etc/nginx/conf.d/default.conf

RUN sed -i -E 's#^pid[[:space:]]+.*;#pid        /tmp/nginx.pid;#' /etc/nginx/nginx.conf \
  && sed -i -E 's#^error_log.*#error_log /dev/stderr warn;#' /etc/nginx/nginx.conf \
  && sed -i -E 's#^    access_log.*#    access_log /dev/stdout;#' /etc/nginx/nginx.conf \
  && mkdir -p /var/cache/nginx /var/run \
  && chown -R nginx:nginx /var/cache/nginx /var/run /usr/share/nginx/html /etc/nginx

USER nginx

EXPOSE 80

CMD ["nginx", "-g", "daemon off;"]
