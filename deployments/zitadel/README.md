# ZITADEL compose pack

This directory contains the official ZITADEL Docker Compose pack for local development.

## Start

```bash
cd deployments/zitadel
docker compose up -d
```

## Notes

* The downloaded compose pack uses the official ZITADEL homelab/local-development defaults.
* Update `.env.example` before exposing it outside a trusted local network.
* For this repo, the backend/frontend should be switched to `AUTH_MODE=oidc` and pointed at this stack when you want real login.
