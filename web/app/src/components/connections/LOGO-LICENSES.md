# Bundled provider logomarks

The console cannot fetch external images (CSP), so provider marks are bundled
as inline SVG paths in `ProviderLogo.tsx`.

| Slug | Source | License |
| --- | --- | --- |
| `docker` | [Simple Icons](https://simpleicons.org/?q=docker) `docker.svg` | CC0-1.0 (collection); the Docker mark itself remains a trademark of Docker, Inc. — used here nominatively to identify the provider. |
| `runpod`, `shadeform`, `vast` | Typographic monogram (no suitably licensed vector mark found) | n/a |

When a provider publishes a clearly licensed logomark, add its path to
`LOGOMARK_PATHS` under the manifest's slug and record the source here.
