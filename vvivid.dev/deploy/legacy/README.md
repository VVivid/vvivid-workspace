# Legacy deployment reference

`gitlab-ci.yml` preserves the archived GitLab pipeline for reference. It is intentionally outside the active `.gitlab-ci.yml` location. It built registry images and deployed preview/live services through an external `/infra/compose.yml`, registry credentials, Traefik, and release-tag scripts. Those resources are not included in this import.

The project-root `docker-compose.yml` and `scripts/cleanup-vvivid-images.sh` and `scripts/persist-vvivid-live-tag.sh` are preserved operational references. They can alter external services, images, and environment files when explicitly invoked. The build does not run them. Migration of these deployment operations is outside this import.
