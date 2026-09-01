# spotlab-go

Serveur backend de **Spotlab**, un service de streaming musical auto-hébergé pour un
usage personnel (un petit groupe d'amis, hébergé sur un Proxmox local). Réécriture
en Go d'un ancien serveur Next.js/TypeScript, dont le client web est abandonné —
ce backend ne sert plus qu'une [app Android](https://github.com/LucasBouet/Splotlab)
dédiée.

Ce n'est pas une plateforme commerciale : c'est un serveur bénévole, fermé par
activation RSA pour éviter les abus, pas pour protéger un business.

## Ce que ça fait

- **Catalogue** : recherche titres/albums/artistes via l'API Deezer, paroles
  synchronisées via [lrclib.net](https://lrclib.net).
- **Lecture audio** : les pistes ne sont pas stockées à l'avance — le serveur
  résout chaque titre vers YouTube via `yt-dlp` à la demande (recherche +
  téléchargement), met le résultat en cache disque, et le sert en streaming avec
  lecture possible dès les premiers octets (pas d'attente de fin de téléchargement).
- **Sync multi-appareils & jams** : l'état de lecture (piste en cours, position,
  file d'attente, appareil actif) est piloté par un acteur unique côté serveur et
  diffusé en temps réel par SSE (Server-Sent Events) à tous les appareils d'un
  compte. Une "jam" permet à plusieurs comptes de partager une écoute commune —
  file d'attente collaborative, contrôle transporté au groupe.
- **Bibliothèque & playlists** : titres aimés, playlists personnelles, et des
  "playlists intelligentes" générées automatiquement (top titres d'un artiste,
  playlists par décennie/genre, un vrai résultat éditorial Deezer pour une
  recherche par style/mood, et un "Blend" combinant les goûts de deux amis).
- **Social** : demandes d'amis, présence ("qui écoute quoi en ce moment"),
  invitations aux jams.
- **Stats** : historique d'écoute, morceaux/artistes les plus joués, façon
  Spotify Wrapped.
- **Admin** : panneau minimal de gestion des comptes et de l'activation.

## Architecture

```
spotlab-go/
├── cmd/
│   ├── spotlabd/         # le serveur
│   ├── activate-sign/    # CLI de signature de licence RSA (jamais déployée)
│   └── mtls-ca/          # CLI de génération de certificats client mTLS
├── internal/
│   ├── config/           # variables d'environnement → Config
│   ├── logging/          # slog JSON → stdout (journald)
│   ├── db/                # SQLite (modernc.org/sqlite, sans cgo), migrations goose
│   ├── auth/              # sessions, argon2id, activation RSA, middleware bearer
│   ├── catalog/           # client Deezer, recherche, paroles
│   ├── library/, playlists/, devices/, social/, blend/, shelf/
│   ├── sync/               # l'acteur de lecture : hub, commandes, file d'attente, jams, SSE
│   ├── stream/             # cache disque, résolution yt-dlp, téléchargement en tâche de fond
│   ├── stats/              # historique, recommandations, playlists intelligentes
│   ├── admin/              # panneau d'administration
│   └── apihttp/            # routeur chi, middlewares, format de réponse JSON
└── deploy/                # unit systemd, config nginx (reverse proxy + mTLS)
```

Décisions de conception détaillées (choix techniques, pourquoi SQLite pur Go,
pourquoi un acteur unique pour la synchronisation, historique des choix
retournés en cours de route) : [`docs/PLAN.md`](docs/PLAN.md). Détails du
mTLS entre l'app et nginx : [`docs/MTLS.md`](docs/MTLS.md).

## Développement

Prérequis : Go 1.26+, `yt-dlp` sur le `PATH` (résolution audio).

```bash
go run ./cmd/spotlabd
```

Variables d'environnement (toutes optionnelles, voir `internal/config/config.go`) :
`PORT`, `DATABASE_PATH`, `STREAM_CACHE_DIR`, `LASTFM_API_KEY`, `YTDLP_PATH`,
`ACTIVATION_PUBLIC_KEY_PATH`, `LOG_LEVEL`, `SITE_NAME`.

Le serveur écoute par défaut sur `:8081`.

## Tests

```bash
go test ./...
go test -race ./internal/sync/...   # le moteur de sync est concurrent, à tester avec -race
```

## Déploiement

Binaire unique cross-compilé, déployé derrière nginx en reverse proxy avec
authentification mTLS côté client (voir `deploy/` et `docs/MTLS.md`) :

```bash
GOOS=linux GOARCH=amd64 go build -o spotlabd ./cmd/spotlabd
```

Les migrations SQL (`internal/db/migrations/`) sont embarquées dans le binaire
via `go:embed` et appliquées automatiquement au démarrage — rien à copier
séparément sur la machine cible.
