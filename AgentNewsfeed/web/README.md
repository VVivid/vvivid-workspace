# Newsfeed frontend

React and TypeScript render the feed, archive and reader. Vite emits `internal/web/static/assets/app.js` and `app.css`; Go embeds those files and its escaped HTML shell. There is no frontend server in production. Run `npm ci --ignore-scripts`, then `make check race build`. `make frontend-check frontend-test frontend-build` runs the frontend checks alone.

The shell supplies escaped JSON in `#root`'s `data-bootstrap` attribute. It contains only the current owner's requested list or detail, the session CSRF token and the actual write capability. Native links and URL-encoded POST forms keep navigation, refresh, deep links and sign-out straightforward. Archived articles remain readable. Markdown runs through `react-markdown` with raw HTML and images disabled; never add a raw-HTML plugin or render article content with `dangerouslySetInnerHTML`.

`styles.css` defines the approved autumn theme: paper `#E8E2D3`, surface `#F2EDE3`, oxblood `#49232D`, brick `#903F32`, ink `#302824`, walnut `#866257` and sage `#AAA68F`. Small muted text uses darker walnut `#79594F` for readable contrast. Serif headlines, broad margins, horizontal rows and thin dividers carry the editorial layout. Components have visible keyboard focus and respect reduced-motion preferences. Search and alternate sorting are deferred.

## shadcn/ui provenance

`components/ui/button.tsx` and `separator.tsx` adapt the canonical shadcn/ui new-york v4 registry components:

- [Button registry source](https://ui.shadcn.com/r/styles/new-york-v4/button.json): CVA variants, Radix Slot composition, typed props, `data-slot` and `cn` merging.
- [Separator registry source](https://ui.shadcn.com/r/styles/new-york-v4/separator.json): Radix Separator, decorative/orientation props, typed props and orientation-aware classes.

The upstream [MIT license](SHADCN-LICENSE.md) is retained and copied into the runtime image. The theme adapts spacing, variants, radius and focus treatment, removes shadows, and uses the individual `@radix-ui/react-slot` and `@radix-ui/react-separator` packages instead of the umbrella Radix package. Dependencies also include `class-variance-authority`, `clsx`, `tailwind-merge` and Tailwind CSS. `components.json` records the shadcn aliases and stylesheet. Arrow and leaf SVGs are local source, requiring no icon/font network request.

Frontend tests cover private shells, feed/archive pagination and keyboard navigation, proxy-only thumbnails and failure placeholders, original Markdown, raw-HTML/image/unsafe-link rejection, explicit CSRF form actions, legacy summary fallback and missing write capability. Go integration tests exercise the corresponding authenticated routes and actual MCP SDK calls.
