# spotlab-go

Réécriture en Go du serveur Spotlab (`/home/lucas/spotlab_`, Next.js/TypeScript),
pour l'app Android uniquement — le client web est abandonné. Voir
[`docs/PLAN.md`](docs/PLAN.md) pour le plan complet, phase par phase.

## Développement

```bash
go run ./cmd/spotlabd
```

Variables d'environnement (toutes optionnelles, voir `internal/config/config.go`) :
`PORT`, `DATABASE_PATH`, `STREAM_CACHE_DIR`, `LASTFM_API_KEY`, `YTDLP_PATH`,
`ACTIVATION_PUBLIC_KEY_PATH`, `LOG_LEVEL`, `SITE_NAME`.

Le serveur écoute par défaut sur `:8081` — le serveur Next.js existant reste sur
`:3000`, les deux tournent en parallèle sans interférence pendant tout le chantier
(§6 du plan).

## Tests

```bash
go test ./...
go test -race ./internal/sync/...   # une fois la phase 6 écrite
```
