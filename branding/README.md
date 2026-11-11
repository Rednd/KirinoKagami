# Custom logo

Put your logo in this folder as `logo.png` (or `logo.svg`, `logo.webp`, `logo.jpg`,
`logo.gif` or `logo.ico`) and restart the mirror. The browser frontend then uses it as
the logo in the page header and as the browser-tab icon (favicon). Without a file here,
the built-in logo is used.

- The header shows the image cropped to a 30px circle (centred, filling the circle), so
  use a square image with the important part in the middle. The tab icon uses the image
  as-is.
- Maximum size is 2 MiB. If several `logo.*` files exist, the first of svg, png, webp,
  jpg, jpeg, gif, ico wins.
- With Docker, this folder is mounted into the container by `docker-compose.yml`, so no
  rebuild is needed; just restart (`make restart`).
