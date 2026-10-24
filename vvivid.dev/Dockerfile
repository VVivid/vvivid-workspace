# Stage 1: Build the site
FROM hugomods/hugo:0.162.1@sha256:07a5a58a9e612f151df79cb692c077eb32067e265e58ce1d7ae585f521a40e02 AS builder

WORKDIR /src
COPY . .

# Build the site with minification
ARG HUGO_BASEURL=https://vvivid.dev/
RUN ./scripts/build.sh

# Stage 2: Serve the site with Nginx
FROM nginx:alpine

# Remove default nginx static assets
RUN rm -rf /usr/share/nginx/html/*

# Copy the built site from the builder stage
COPY --from=builder /src/public /usr/share/nginx/html

# Copy custom nginx configuration
COPY nginx.conf /etc/nginx/conf.d/default.conf

# Expose port 80
EXPOSE 80

# Start Nginx
CMD ["nginx", "-g", "daemon off;"]
