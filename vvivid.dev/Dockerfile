# Stage 1: Build the site
FROM hugomods/hugo:0.162.1@sha256:07a5a58a9e612f151df79cb692c077eb32067e265e58ce1d7ae585f521a40e02 AS builder

WORKDIR /src
COPY . .

# Build the site with minification
ARG HUGO_BASEURL=https://vvivid.dev/
RUN ./scripts/build.sh

# Stage 2: Serve the site with Nginx
FROM nginx:alpine@sha256:785835ea65db4bba6f021acc0f3be91b0fef76a53fe41f8588c6698d2aed0606

# The official Alpine image bundles njs; use it for Accept quality parsing.
# Keep nginx's pid and cache writable for nonroot runs without mounted volumes.
RUN rm -rf /usr/share/nginx/html/* \
    && sed -i '1i load_module /usr/lib/nginx/modules/ngx_http_js_module.so;' /etc/nginx/nginx.conf \
    && sed -i 's@^pid[[:space:]].*;@pid /tmp/nginx.pid;@; /^user[[:space:]]/d' /etc/nginx/nginx.conf \
    && chown -R 101:101 /var/cache/nginx

# Copy the built site from the builder stage
COPY --from=builder /src/public /usr/share/nginx/html
# Hugo preserves static-file modes, including restrictive checkout permissions.
RUN chmod -R a+rX /usr/share/nginx/html

# Copy custom nginx configuration
COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY nginx/representations.js /etc/nginx/representations.js

# Expose port 80
EXPOSE 80
USER 101:101

# Start Nginx
CMD ["nginx", "-g", "daemon off;"]
