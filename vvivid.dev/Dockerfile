# Stage 1: Build the site
FROM hugomods/hugo:0.162.1@sha256:07a5a58a9e612f151df79cb692c077eb32067e265e58ce1d7ae585f521a40e02 AS builder

WORKDIR /src
COPY . .

# Build the site with minification
ARG HUGO_BASEURL=https://vvivid.dev/
RUN ./scripts/build.sh

# Stage 2: Serve the site with Nginx
FROM nginx:alpine

# The official Alpine image bundles njs; use it for Accept quality parsing.
RUN rm -rf /usr/share/nginx/html/* \
    && sed -i '1i load_module /usr/lib/nginx/modules/ngx_http_js_module.so;' /etc/nginx/nginx.conf

# Copy the built site from the builder stage
COPY --from=builder /src/public /usr/share/nginx/html
# Hugo preserves static-file modes, including restrictive checkout permissions.
RUN chmod -R a+rX /usr/share/nginx/html

# Copy custom nginx configuration
COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY nginx/representations.js /etc/nginx/representations.js

# Expose port 80
EXPOSE 80

# Start Nginx
CMD ["nginx", "-g", "daemon off;"]
