# vvivid.dev

Ou David's personal website, built with Hugo Extended. This project owns its content, theme, build context, and release version. Commands below run from the repository root unless stated otherwise.

## Native build and preview

Install Make, a POSIX shell, and **Hugo Extended 0.162.1**, the version recorded in [.hugo-version](.hugo-version). No Node or Go installation is needed. Download the matching Extended archive for your platform from the [official release](https://github.com/gohugoio/hugo/releases/tag/v0.162.1) and verify its published checksum.

For Linux amd64, a temporary installation can be used without changing system tools:

```sh
site_hugo_dir=$(mktemp -d)
curl -fL --output "$site_hugo_dir/hugo.tar.gz" \
  https://github.com/gohugoio/hugo/releases/download/v0.162.1/hugo_extended_0.162.1_linux-amd64.tar.gz
printf '%s  %s\n' \
  e34160095b6a6406af857fe212f50e4451f67ed1276b9bb0de13d08754980118 \
  "$site_hugo_dir/hugo.tar.gz" | sha256sum --check
tar -xzf "$site_hugo_dir/hugo.tar.gz" -C "$site_hugo_dir"
HUGO="$site_hugo_dir/hugo" make -C vvivid.dev check
```

Use `make website-check`, `make website-test`, or `make website-build` at the repository root, or `make check`, `make test`, or `make build` inside this directory. All three project targets render the production site through [scripts/build.sh](scripts/build.sh); there is no separate unit-test suite. The script requires the pinned Extended version, minifies output, and fails if any case variant of `AGENTS.md` or `AGENT.md` reaches the output. Generated `public/` and `resources/` directories are ignored by Git.

Set `HUGO=/absolute/path/to/hugo` when the binary is outside `PATH`. `HUGO_DESTINATION` changes the output directory; relative paths are resolved from `vvivid.dev/`. `HUGO_BASEURL` overrides the production default of `https://vvivid.dev/`.

For a development server using the installation above:

```sh
cd vvivid.dev
"$site_hugo_dir/hugo" server --bind 127.0.0.1 --baseURL http://localhost:1313/
```

Open [localhost:1313](http://localhost:1313/). The development server is for preview; use a production Make target to validate output before delivery.

## Docker build and local serving

Build using this project as the complete Docker context:

```sh
docker build --build-arg HUGO_BASEURL=http://localhost:8080/ \
  -t vvivid-website:local ./vvivid.dev
docker run --rm --publish 127.0.0.1:8080:80 vvivid-website:local
```

Open [localhost:8080](http://localhost:8080/). The [Dockerfile](Dockerfile) builds with a Hugo image pinned by version and digest, runs the same production build script, and serves the generated site with nginx. Omit `--build-arg HUGO_BASEURL=...` to build with the production URL. This local command uses Docker's default network and requires no external Traefik network. The nginx configuration blocks agent-guide filenames at every URL depth.

The [GitHub workflow](../.github/workflows/website.yml) builds only the Dockerfile's Hugo `builder` stage on site, workflow, or root Makefile changes. It checks the production output with read-only repository permissions; it has no publishing or deployment step.

## Source and dependencies

- [content/](content/) contains the pages and posts; preserve existing front matter and URLs when editing.
- [layouts/](layouts/), [assets/](assets/), and [static/](static/) contain site overrides and public assets.
- [hugo.toml](hugo.toml) configures the site.
- [themes/hugo-blog-awesome/](themes/hugo-blog-awesome/) vendors the theme's runtime files and MIT license at `v2.0.1`, commit `46d1ab1a9cc8ad24a3d482af618771d708d91c2f`. [UPSTREAM.md](themes/hugo-blog-awesome/UPSTREAM.md) records provenance, checksum, and update instructions. No submodule checkout is needed.
- [AGENTS.md](AGENTS.md) contains contributor guidance and stays outside public assets and generated output.

The site was imported from an attached source snapshot without original Git history. The archive's theme directory was empty and its theme revision could not be recovered. The pinned published theme makes the build reproducible, but exact visual parity with the original site cannot be verified. Content and asset files were preserved; template changes adapt the archive to the pinned Hugo version.

Update `.hugo-version` and the Docker builder image version/digest together when deliberately upgrading Hugo, then validate the site and overrides. This project's independent release tags use `vvivid.dev/vX.Y.Z`; see the [workspace development guide](../DEVELOPMENT.md#independent-versions).

## Deployment references

The archived GitLab pipeline is retained under [deploy/legacy/](deploy/legacy/README.md), outside the active CI filename. Its registry, external `/infra/compose.yml`, preview/live services, Traefik configuration, and credentials were not migrated.

The root [docker-compose.yml](docker-compose.yml), [cleanup script](scripts/cleanup-vvivid-images.sh), and [live-tag script](scripts/persist-vvivid-live-tag.sh) remain operational references to that infrastructure. Invoking them can change external services, images, or environment files. The Make targets and GitHub build do not run these operations. Source import and local builds do not establish a live deployment.
