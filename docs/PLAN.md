# Spotlab : réécriture du backend Next.js → Go

## Contexte

Le serveur actuel (`/home/lucas/spotlab_`, Next.js/TypeScript) sert uniquement l'app Android au quotidien — le client web n'est plus utilisé et est abandonné. Deux problèmes motivent la réécriture : le serveur, pensé pour porter aussi une interface web, est plus lourd que nécessaire pour ce qui reste (une API JSON + un flux SSE), et certains échanges client/serveur sont perçus comme lents. L'objectif est un unique serveur Go, optimisé pour ce seul usage, plus rapide, et fermé par une activation par clé RSA pour empêcher n'importe qui de taper dessus (anti-abus, pas de la protection commerciale — le service reste bénévole).

Il s'agit d'un projet perso, hébergé sur un Proxmox local, pour vous et quelques amis. Décisions actées avec vous avant ce plan :
- **Recherche audio** : abandon de `ytmusic-api` (librairie JS sans équivalent Go) au profit de la recherche intégrée de yt-dlp (`ytsearch:`), qui reste le seul binaire externe invoqué.
- **Base de données** : on repart d'une base vide — pas de copie des données actuelles (comptes, titres aimés, playlists). Tout le monde se réinscrit sur le nouveau serveur, via l'activation RSA. Ça libère le schéma Go de toute contrainte de compatibilité colonne-pour-colonne avec l'ancien schéma Prisma.
- **Client web** : définitivement abandonné. Le panneau admin, l'import de playlist Deezer et les passkeys (web-only) restent donc hors périmètre v1 sans arrière-pensée — rien à préserver pour un futur retour du web.

Ce document est le plan d'exécution, phase par phase, à reprendre au fil de futures sessions sans avoir à redériver l'architecture à chaque fois.

---

## 0. Décisions de conception (à lire en premier)

| Décision | Choix | Pourquoi |
|---|---|---|
| Routeur HTTP | `chi` | Léger, idiomatique, bon écosystème de middleware, sans le poids d'un vrai framework — déjà discuté et validé. |
| Driver SQLite | `modernc.org/sqlite` (pur Go, sans cgo) | Cross-compilation triviale vers la VM Proxmox (`GOOS=linux GOARCH=amd64 go build` marche sans toolchain C). `mattn/go-sqlite3` est un peu plus rapide mais à l'échelle "quelques amis", ça ne compte pas — la simplicité de déploiement gagne. |
| Accès DB | `sqlc` sur `database/sql` | Requêtes typées générées depuis du SQL brut, pas de magie d'ORM — cohérent avec vos migrations SQL déjà écrites à la main côté Prisma. |
| Migrations | `goose`, embarquées via `go:embed`, appliquées au démarrage | Un seul binaire à déployer, pas de fichiers de migration à copier séparément sur la VM. |
| Schéma initial | Un seul `0001_init.sql` propre, écrit pour le besoin actuel — pas de portage des 12 migrations Prisma historiques (qui ne sont que des artefacts d'évolution incrémentale, sans valeur pour un projet neuf) | Base vide actée : plus aucune contrainte de compatibilité, le schéma est écrit librement pour Go. |
| Hachage des mots de passe | `argon2id` (`golang.org/x/crypto/argon2`), aucun besoin de compat avec l'ancien `scrypt` | Base vide → aucun compte existant à faire migrer. |
| Concurrence du moteur de sync | **Un seul acteur Hub** (une goroutine, un channel de commandes) — pas d'acteur par salle, pas de simple mutex global | Justifié en détail en §3 : les opérations de jam touchent intrinsèquement deux « salles » à la fois (sceller un jam depuis l'état solo de l'hôte, renvoyer chaque membre vers son état solo à l'arrêt) — un seul point de sérialisation gère ça nativement, un acteur par salle demanderait une coordination inter-acteurs qui annule l'intérêt du découpage. |
| Emplacement du dépôt | Nouveau dépôt séparé, ex. `/home/lucas/spotlab-go` (à côté de `spotlab_`, pas dedans) | Garde le serveur Next.js actuel comme filet de secours intact pendant toute la durée du chantier. |
| Cutover | Les deux serveurs tournent en parallèle sur des ports différents jusqu'à bascule volontaire de nginx | Zéro risque sur le service qui tourne aujourd'hui. |

---

## 1. Structure du projet Go

```
spotlab-go/
├── go.mod
├── cmd/
│   ├── spotlabd/main.go            # le serveur
│   └── activate-sign/main.go       # CLI de signature de licence RSA — jamais déployée
├── internal/
│   ├── config/config.go            # env → struct Config, pas de viper nécessaire
│   ├── logging/logging.go          # slog.JSONHandler → stdout (journald le capture déjà)
│   ├── db/
│   │   ├── migrations/0001_init.sql  # goose, go:embed
│   │   ├── queries/                  # .sql d'entrée pour sqlc
│   │   └── gen/                      # sortie sqlc (package db)
│   ├── auth/
│   │   ├── password.go             # argon2id hash/verify
│   │   ├── session.go              # génération de token, CRUD, refresh atomique
│   │   ├── middleware.go           # bearer token → contexte utilisateur
│   │   └── activation.go           # vérification RSA, /api/activate
│   ├── catalog/
│   │   ├── deezer.go               # client Deezer
│   │   ├── handlers.go             # search/track/album/artist
│   │   └── lyrics.go               # client lrclib.net + handler
│   ├── library/handlers.go         # titres aimés
│   ├── playlists/handlers.go
│   ├── devices/handlers.go
│   ├── social/handlers.go          # amis, demandes, présence
│   ├── sync/
│   │   ├── hub.go                  # l'acteur : état + channel de commandes
│   │   ├── commands.go             # types de commandes
│   │   ├── queue.go                # portage direct de queue-reducer.ts
│   │   ├── jam.go                  # cycle de vie des jams
│   │   ├── sse.go                  # handler HTTP de /api/sync/stream
│   │   └── types.go                # DTOs identiques à sync-types.ts / jam-types.ts
│   ├── stream/
│   │   ├── cache.go                # scan du dossier de cache
│   │   ├── match.go                # recherche + scoring yt-dlp
│   │   ├── download.go             # type Download, lecteur à tail live via sync.Cond
│   │   └── handlers.go             # /api/stream/{id}, /api/prefetch/{id}
│   ├── stats/handlers.go           # /api/plays, /api/stats, /api/recommendations
│   └── apihttp/
│       ├── router.go               # assemblage chi, montage de chaque module
│       ├── respond.go              # enveloppe {"error": "..."}, helpers JSON
│       └── middleware.go           # logging de requête, recover, request id
└── deploy/
    ├── spotlab-go.service          # unit systemd
    └── nginx-snippet.conf
```

**Config** (`internal/config/config.go`) : une struct simple peuplée depuis l'environnement, defaults raisonnables, pas de dépendance externe à cette échelle :

```go
type Config struct {
    Port                 string // PORT, défaut "8081"
    DatabasePath          string // DATABASE_PATH, défaut "./data/spotlab.db"
    StreamCacheDir         string // STREAM_CACHE_DIR, défaut "./cache/songs"
    LastFMAPIKey           string // LASTFM_API_KEY, optionnel
    YTDLPPath               string // YTDLP_PATH, défaut : exec.LookPath("yt-dlp")
    ActivationPublicKeyPath string // ACTIVATION_PUBLIC_KEY_PATH, sinon clé embarquée
    LogLevel                string // LOG_LEVEL, défaut "info"
    SiteName                string
}
```

**Logs** : `log/slog` en JSON sur stdout — capturé par journald sous systemd, donc `journalctl -u spotlab-go -f | jq` directement exploitable. Middleware qui logue `method, path, status, duration_ms, user_id` par requête.

---

## 2. Ordre de construction, module par module

Ordre volontairement pensé pour avoir quelque chose de testable au curl dès la première session, et pour repousser les deux morceaux vraiment durs (moteur de sync, pipeline audio) une fois que tout le reste fonctionne et sert de terrain de test manuel.

| Phase | Module | Dépend de | Effort |
|---|---|---|---|
| 0 | Squelette : go.mod, chi, slog, config, goose+sqlc, `/healthz`, unit systemd sur un port de test | — | Petit |
| 1 | Schéma + Auth + Config + activation RSA | Phase 0 | Moyen |
| 2 | Catalogue (search/track/album/artist) + Paroles | Phase 1 (middleware auth) | Petit–Moyen |
| 3 | Bibliothèque (titres aimés) + Playlists | Phase 1 | Moyen |
| 4 | Appareils | Phase 1 | Petit |
| 5 | Amis/présence (présence réelle en attente de la phase 6) | Phase 1, 4 | Moyen |
| 6 | **Moteur de sync/lecture + SSE + Jams** | Phase 1, 4, 5 | Grand |
| 7 | Stats + Recommandations | Phase 3, 6 | Moyen |
| 8 | **Pipeline audio** (recherche/score/téléchargement/tail live yt-dlp) | Phase 1, idéalement tout le reste pour tester en conditions réelles | Grand |
| 9 (non v1) | Panneau admin, import de playlist, téléchargement MP3, passkeys, écoute hors ligne | — | Hors périmètre |

**Note importante sur le test manuel** : la séquence de démarrage documentée côté Android (`docs/API.md`) est `config → login → me → devices/register → sync/stream (SSE) → library/likes`. L'étape 5 ouvre un flux SSE de manière inconditionnelle — **impossible de pointer un vrai build Android sur le serveur Go avant que la phase 6 soit terminée**. Avant ça, la vérification se fait au curl et par tests Go. Une fois la phase 6 posée, premier vrai jalon : un build Android debug pointé sur le serveur Go fonctionne sur *tous* les écrans sauf la lecture audio elle-même (le endpoint de streaming peut répondre une erreur propre "pas encore implémenté" jusqu'à la phase 8). C'est un jalon concret à viser.

---

## 3. Moteur de sync/lecture — le morceau central

### 3.1 Architecture : un seul acteur Hub

Le design JS actuel s'appuie sur un fait précis : un seul thread Node rend implicitement atomique tout ce qui se passe entre deux `await`. Deux `Map` au niveau module (`playbackState`, `jams`) sont toute la « base de données », et la correction du système vient uniquement de la règle « jamais d'`await` entre la lecture et la mutation de l'état » — explicitement commentée dans le code source.

Un acteur par salle a été écarté : les opérations de jam sont intrinsèquement transverses. `createJam` lit l'état *solo* de l'hôte pour créer une *nouvelle* salle de jam. `stopJam` parcourt chaque membre et renvoie chacune de ses connexions vers son état *solo*, inchangé. Aucune de ces opérations n'est mono-salle — les implémenter proprement sur N acteurs indépendants demanderait soit un protocole de commit à deux phases entre acteurs, soit de toute façon canaliser les opérations de cycle de vie de jam par un autre point de sérialisation, ce qui annule l'intérêt de la séparation.

Un simple `sync.Mutex` global a aussi été écarté : structurellement correct, mais avec un vrai piège — diffuser un événement veut dire écrire sur des connexions SSE potentiellement lentes (un mobile en arrière-plan), et si cette écriture reste dans la section critique, un client lent bloque les commandes de tout le monde.

**Retenu : un acteur Hub unique**, une goroutine, un `chan command`, miroir direct du modèle JS mono-thread. Chaque mutation — commande de lecture solo, commande de jam, opération de cycle de vie de jam, tick du battement de cœur à 5s — est une valeur `command` envoyée dans un seul channel, traitée dans l'ordre d'arrivée par une seule goroutine. C'est un portage structurel direct du fichier JS (facile à vérifier ligne à ligne face à l'original), sans race sur l'état par construction (une seule goroutine y touche), et ça donne accès à `go test -race` comme preuve de correction que la version JS n'a jamais pu offrir. Les diffusions à l'intérieur de l'acteur sont des **envois non bloquants** vers des channels bufferisés par connexion — un client mort/lent est simplement abandonné, jamais bloquant pour l'acteur (§3.3), l'équivalent Go du `try { enqueue } catch { markDead }` déjà présent côté JS.

À cette échelle (quelques utilisateurs, commandes au rythme d'interactions humaines), un acteur unique n'a aucun souci de débit réel. C'est le bon compromis : simplicité de correction plutôt qu'un parallélisme dont personne n'a besoin.

**Règle dure pour ce module** : le code de traitement des commandes de l'acteur ne doit jamais faire d'I/O bloquant (pas d'appel DB, pas de réseau) — exactement l'invariant du fichier JS d'origine. Ce qui a besoin de la DB (par ex. valider que `SET_ACTIVE_DEVICES` ne porte que des appareils possédés par l'appelant, ce que la route JS fait *avant* d'appeler `applyCommand`) se passe dans le handler HTTP, avant l'envoi de la commande au Hub.

### 3.2 Types concrets

```go
package sync

type Hub struct {
    cmds chan command // le seul point de sérialisation

    // Tout ce qui suit n'est touché que depuis la goroutine de Run().
    solo      map[string]*roomState        // userId -> état de lecture solo
    jams      map[string]*jam              // jamId -> jam
    userJamID map[string]string            // userId -> jamId
    conns     map[string]map[string]*conn  // userId -> connId -> conn
}

type conn struct {
    deviceID string
    out      chan []byte // trames SSE déjà encodées, bufferisées (cap 16)
}

type command interface{ apply(h *Hub) }

func (h *Hub) Run(ctx context.Context) {
    ticker := time.NewTicker(5 * time.Second) // dérive solo + ramassage des jams
    defer ticker.Stop()
    for {
        select {
        case cmd := <-h.cmds:
            cmd.apply(h)
        case <-ticker.C:
            (&heartbeatCmd{}).apply(h)
        case <-ctx.Done():
            return
        }
    }
}
```

API publique en « acteur avec requête/réponse par channel » — chaque méthode exportée construit une commande portant un channel de réponse, l'envoie, et attend (avec `ctx` pour l'annulation) :

```go
type applyCommandCmd struct {
    userID, deviceID string
    action           SyncAction
    reply            chan CanonicalPlaybackStateDTO
}

func (c *applyCommandCmd) apply(h *Hub) {
    c.reply <- h.applyCommandLocked(c.userID, c.deviceID, c.action) // pur, en mémoire
}

func (h *Hub) ApplyCommand(ctx context.Context, userID, deviceID string, action SyncAction) (CanonicalPlaybackStateDTO, error) {
    reply := make(chan CanonicalPlaybackStateDTO, 1)
    select {
    case h.cmds <- &applyCommandCmd{userID, deviceID, action, reply}:
    case <-ctx.Done():
        return CanonicalPlaybackStateDTO{}, ctx.Err()
    }
    select {
    case s := <-reply:
        return s, nil
    case <-ctx.Done():
        return CanonicalPlaybackStateDTO{}, ctx.Err()
    }
}
```

Même forme pour `Subscribe`, `Unsubscribe`, `InviteToJam`, `AcceptJamInvite`, `DeclineJamInvite`, `LeaveJam`, `StopJam`, `BroadcastDeviceList` — mappage direct de chaque fonction exportée de `playback-sync.ts`.

`queue.go` est un portage pur de `queue-reducer.ts` (vérifié ligne à ligne, 10 variantes de `QueueAction`) sur :

```go
type QueueState struct {
    Current        *QueueItem
    Queue, History, ContextTracks []QueueItem
    ActiveContextID *string
    Shuffle        bool
}
```

Deux points à reproduire à l'identique, faciles à rater :

- **Sémantique de `REORDER_QUEUE`** : `toIndex` adresse la liste *après* retrait, exactement `Array.prototype.splice` (vérifié dans `queue-reducer.ts` : `result.splice(fromIndex, 1)` puis `result.splice(toIndex, 0, moved)`). Porter `arrayMove` en retirant d'abord dans un nouveau slice, puis en insérant dans ce slice raccourci à `toIndex`. Tests table-driven pour `fromIndex < toIndex`, `fromIndex > toIndex`, `fromIndex == toIndex`, et déplacement en toute fin de liste.
- **Recalcul de l'union `activeDeviceIds`** : ne jamais accepter la liste d'appareils d'un membre de jam comme écrasant `activeDeviceIds` — toujours recalculer entièrement comme l'union des `DeviceIDs` de tous les membres (`syncJamActiveDevices` dans l'original), y compris au départ d'un membre ou au transfert d'hôte.

Sémantique de `revision` : monotone **par salle**, remise à 0 au moment précis où une salle est fraîchement créée (`cloneStateForJam` à la création d'un jam). Le chemin de retour au solo, lui, ne remet **pas** à 0 — il restitue l'état solo figé, jamais touché depuis avant le jam, avec sa révision d'alors (mesuré en direct : entrer *et* sortir d'un jam donnent tous deux `revision: 0` dans les faits, mais pour deux raisons différentes — nouvelle salle d'un côté, état gelé de l'autre). Reproduire cette distinction telle quelle, ne pas « simplifier » en remettant les deux chemins à 0 par la même logique.

Règle du passage à un seul appareil actif depuis l'inactivité : quand une salle passe de `Current == nil` à non-nil via une commande de lecture, `activeDeviceIds` devient `[deviceID de la commande]` — uniquement sur le chemin solo (le chemin jam ne fait jamais ça, l'ensemble d'appareils d'un jam est toujours l'union, initialisée à la création depuis l'appareil de l'invitant).

### 3.3 Diffusion SSE, abonnement/désabonnement, cycle de vie du ping

Décision de conception clé : le **ping est par connexion** dans l'original (chaque requête SSE fait tourner son propre `setInterval`), alors que le **battement de cœur de dérive/ramassage à 5s est global** (un seul tick au niveau du Hub). Garder cette séparation — ne pas fondre le ping dans le Hub, il ne porte aucun état et n'a besoin d'aucune sérialisation.

```go
func (s *Server) handleSyncStream(w http.ResponseWriter, r *http.Request) {
    user := auth.UserFromContext(r.Context())
    deviceID := r.URL.Query().Get("deviceId")
    if deviceID == "" { apihttp.Error(w, 400, "deviceId manquant."); return }

    flusher, ok := w.(http.Flusher)
    if !ok { apihttp.Error(w, 500, "streaming non supporté."); return }

    w.Header().Set("Content-Type", "text/event-stream")
    w.Header().Set("Cache-Control", "no-cache")
    w.Header().Set("Connection", "keep-alive")
    w.Header().Set("X-Accel-Buffering", "no") // exigence nginx déjà documentée côté README actuel
    w.WriteHeader(http.StatusOK)
    io.WriteString(w, "retry: 3000\n\n")
    flusher.Flush()

    connID, out := s.hub.Subscribe(r.Context(), user.ID, deviceID) // pousse le `snapshot` initial en premier
    go s.touchLastSeen(user.ID, deviceID)
    defer s.hub.Unsubscribe(context.Background(), user.ID, connID)
    go s.hub.BroadcastDeviceList(context.Background(), user.ID)

    ping := time.NewTicker(20 * time.Second)
    defer ping.Stop()

    for {
        select {
        case frame, ok := <-out:
            if !ok { return }
            if _, err := w.Write(frame); err != nil { return }
            flusher.Flush()
        case t := <-ping.C:
            if _, err := io.WriteString(w, sse("ping", t.UnixMilli())); err != nil { return }
            flusher.Flush()
            go s.touchLastSeen(user.ID, deviceID)
        case <-r.Context().Done():
            return
        }
    }
}
```

`Subscribe`, à l'intérieur de l'acteur, enregistre la connexion **et calcule/pousse le snapshot dans le même passage atomique**, garantissant qu'aucune commande traitée après (garanti par l'ordre du channel) ne peut devancer la première vue de l'état du client qui vient de se connecter :

```go
func (c *subscribeCmd) apply(h *Hub) {
    connID := uuid.NewString()
    ch := make(chan []byte, 16)
    if h.conns[c.userID] == nil { h.conns[c.userID] = map[string]*conn{} }
    h.conns[c.userID][connID] = &conn{deviceID: c.deviceID, out: ch}
    ch <- encodeSSE("snapshot", h.snapshotFor(c.userID)) // ne bloque jamais, buffer tout juste ouvert
    c.reply <- subscribeResult{connID: connID, out: ch}
}
```

Diffusion non bloquante (élagage des connexions mortes/lentes — équivalent direct du passage try/catch-puis-nettoyage de l'original) :

```go
func (h *Hub) sendEvent(userID, event string, data any) {
    payload := encodeSSE(event, data)
    for id, c := range h.conns[userID] {
        select {
        case c.out <- payload:
        default: // consommateur trop lent / déjà parti — on abandonne tout de suite
            delete(h.conns[userID], id)
            close(c.out)
        }
    }
}
```

`r.Context().Done()` est l'équivalent Go de l'écoute d'abandon de l'original (`request.signal`) — `net/http` annule fiablement le contexte de la requête à la déconnexion du client, donc c'est l'unique source de vérité pour le nettoyage.

Le refus intégral (pas partiel) de `SET_ACTIVE_DEVICES` : validé dans le handler HTTP *avant* l'appel à `hub.ApplyCommand`, via une requête DB `Device WHERE userId=? AND deviceId IN (...)`, rejet à 400 si le compte ne correspond pas — reproduisant exactement `sync/command/route.ts`.

---

## 4. Pipeline audio (phase 8)

### 4.1 Type `Download` à base de `sync.Cond`, exposé en `io.Reader`

Un endroit où l'idiome Go simplifie franchement par rapport au pattern `EventEmitter` du JS : un seul `sync.Cond` remplace les trois méthodes `waitForExt`/`waitForDone`/`waitForProgress` à base de promesses plus le générateur asynchrone fait main (`tailTrackDownload`), et la logique de lecture au fil de l'eau devient un simple `io.Reader` — consommable directement par tout code Go standard (`io.Copy`, etc.).

```go
type Download struct {
    mu           sync.Mutex
    cond         *sync.Cond
    filePath     string
    contentType  string
    bytesWritten int64
    finished     bool
    err          error
}

func NewDownload() *Download {
    d := &Download{}
    d.cond = sync.NewCond(&d.mu)
    return d
}
func (d *Download) setExt(path, contentType string) {
    d.mu.Lock(); d.filePath, d.contentType = path, contentType; d.cond.Broadcast(); d.mu.Unlock()
}
func (d *Download) addBytes(n int64) {
    d.mu.Lock(); d.bytesWritten += n; d.cond.Broadcast(); d.mu.Unlock()
}
func (d *Download) fail(err error) {
    d.mu.Lock(); d.err = err; d.finished = true; d.cond.Broadcast(); d.mu.Unlock()
}
func (d *Download) complete() {
    d.mu.Lock(); d.finished = true; d.cond.Broadcast(); d.mu.Unlock()
}
```

Lecteur à tail live :

```go
type tailReader struct {
    d        *Download
    f        *os.File
    position int64
}

func (t *tailReader) Read(p []byte) (int, error) {
    t.d.mu.Lock()
    for t.position >= t.d.bytesWritten && t.d.err == nil && !t.d.finished {
        t.d.cond.Wait()
    }
    bw, finished, err := t.d.bytesWritten, t.d.finished, t.d.err
    t.d.mu.Unlock()

    if t.position < bw {
        n, _ := t.f.ReadAt(p, t.position)
        if n > 0 {
            t.position += int64(n)
            return n, nil
        }
    }
    if err != nil { return 0, err }
    if finished && t.position >= bw { return 0, io.EOF }
    return 0, nil // réveil parasite, on reboucle au prochain Read
}
```

**Détail de cycle de vie important, repris de la conception JS** : le processus/goroutine de téléchargement doit tourner sur un contexte de fond, indépendant de toute requête HTTP unique — pas `r.Context()`. La table de déduplication des téléchargements en cours signifie qu'une deuxième requête pour le même titre partage le *même* `Download` ; tuer le processus yt-dlp parce que le *premier* appelant s'est déconnecté casserait le second toujours en train de lire. Seul le nettoyage de la table de dédup est lié à l'achèvement, comme le `.finally()` de l'original.

Borne pour un yt-dlp vraiment bloqué : `exec.CommandContext` avec un timeout généreux (ex. 5 minutes) sur le contexte de fond du téléchargement — à l'expiration, le processus est tué, `fail()` appelé, chaque `tailReader.Read` bloqué se débloque via le même `cond.Broadcast()`.

### 4.2 Recherche + score (sans `ytmusic-api`, recherche yt-dlp native)

```go
func FindBestMatch(ctx context.Context, ytdlpPath, title, artist string, durationSeconds int) (string, error) {
    query := fmt.Sprintf("ytsearch5:%s %s", artist, title)
    out, err := exec.CommandContext(ctx, ytdlpPath, query, "--dump-json", "--no-warnings", "--quiet", "--flat-playlist").Output()
    // parser le JSON en une ligne par candidat

    var best string
    bestScore := math.Inf(-1)
    for _, c := range candidates {
        s := score(c, title, artist, durationSeconds)
        if s > bestScore { bestScore, best = s, c.ID }
    }
    if best == "" { return "", errors.New("aucune correspondance trouvée sur YouTube") }
    return best, nil
}

func score(c Candidate, title, artist string, durationSeconds int) float64 {
    ct, ca := normalize(c.Title), normalize(c.Artist) // vérifier le champ exact renvoyé par yt-dlp --flat-playlist à l'implémentation
    qt, qa := normalize(title), normalize(artist)
    var s float64
    switch { case ct == qt: s += 3; case strings.Contains(ct, qt) || strings.Contains(qt, ct): s += 1.5 }
    switch { case ca == qa: s += 3; case strings.Contains(ca, qa) || strings.Contains(qa, ca): s += 1.5 }
    if c.Duration > 0 {
        diff := math.Abs(float64(c.Duration - durationSeconds))
        if diff <= 12 { s += 2 - diff/12 } else { s -= 1 }
    }
    return s
}
```
(`golang.org/x/text/unicode/norm` pour normaliser les diacritiques, comme côté JS.) **À vérifier en tout début d'implémentation** : le JSON exact que renvoie `yt-dlp ytsearch5:... --dump-json --flat-playlist` pour le titre/uploader/durée de la vidéo — un `yt-dlp ytsearch5:test --dump-json --flat-playlist | jq` suffit à le confirmer avant d'écrire le parsing.

### 4.3 Requêtes Range vs téléchargement en cours

Chemin en cache : `http.ServeContent(w, r, name, modTime, readSeeker)` plutôt que parser les en-têtes `Range` à la main (~25 lignes en moins que côté JS) — donne 200/206, `Content-Range`, `Accept-Ranges: bytes`, `If-Range` gratuitement :

```go
f, _ := os.Open(cachedPath)
defer f.Close()
w.Header().Set("Content-Type", contentTypeFor(cachedPath))
w.Header().Set("Cache-Control", "no-store")
http.ServeContent(w, r, "", time.Time{}, f)
```

Chemin non mis en cache (tail live) : `Accept-Ranges: none`, flush après chaque écriture pour que les octets arrivent immédiatement plutôt que d'attendre le buffer :

```go
type flushWriter struct{ w http.ResponseWriter; f http.Flusher }
func (fw flushWriter) Write(p []byte) (int, error) { n, err := fw.w.Write(p); fw.f.Flush(); return n, err }

w.Header().Set("Accept-Ranges", "none")
w.Header().Set("Content-Type", download.ContentType())
w.Header().Set("Cache-Control", "no-store")
io.Copy(flushWriter{w, w.(http.Flusher)}, download.Reader())
```

Ça reproduit exactement le contrat documenté et vérifié côté Android : en cache → seekable (206) ; pas encore en cache → `Accept-Ranges: none`, non-seekable jusqu'à la requête suivante qui tombera sur le chemin cache.

Déduplication des téléchargements en cours : une `map[string]*Download` derrière un petit `sync.Mutex` (indépendant du Hub — rien à voir avec la sync de lecture), clée par id Deezer, nettoyée une fois `WaitForDone()` résolu.

---

## 5. Activation par clé RSA

**Design retenu** : l'activation *est* un appel d'inscription conditionné — pas une étape séparée avant l'inscription. Un seul endpoint, pas de saut par jeton court.

- Hors-ligne, une fois par personne invitée : `cmd/activate-sign` (CLI jamais déployée) prend `-sub "nom de la personne" -exp 0`, construit `{"sub":"...","iat":<now>,"exp":0}`, signe les octets JSON avec `rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA256, sha256Sum)`, et imprime un code collable : `base64(payloadJSON) + "." + base64(signature)`. PKCS1v15 plutôt que PSS : suffisant ici, c'est de l'anti-abus, pas de la DRM adverse, et PKCS1v15 est plus simple côté stdlib.
- Le serveur n'embarque que la clé **publique**, via `//go:embed activation_public.pem` dans `internal/auth/activation.go` — le plus simple pour un déploiement solo, la clé ne tourne de toute façon jamais sans redéploiement.
- `POST /api/activate` — non authentifié, comme `/api/config`. Corps : `{"code": "...", "email": "...", "password": "...", "name": "..."}`.
  1. Séparer sur `.`, décoder les deux parties en base64.
  2. `pem.Decode` + `x509.ParsePKIXPublicKey` → `*rsa.PublicKey` (une fois au démarrage, pas par requête).
  3. `sha256.Sum256(payloadBytes)`, `rsa.VerifyPKCS1v15(pub, crypto.SHA256, hash[:], sig)`.
  4. Parser le JSON du payload, vérifier `exp` (0 = pas d'expiration) contre `time.Now()`.
  5. Usage unique : `INSERT INTO activation_uses (code_hash, used_at) VALUES (?, ?)` où `code_hash = sha256(code brut)` est la clé primaire — un conflit UNIQUE en cas de rejeu rejette atomiquement, sans race check-then-insert.
  6. Sur succès : même chemin de création de compte qu'une inscription normale (en contournant le paramètre `registration_enabled` — une signature valide *est* l'autorisation), puis émission d'un token de session classique, réponse au même format que `/api/auth/login`.
- Opérationnellement : `registration_enabled=false` en permanence une fois ce système en place — `/api/activate` devient le seul chemin d'entrée pour de nouvelles personnes ; `/api/auth/login` continue de fonctionner normalement pour tout le monde déjà activé.

Nouvelle table (une seule migration nécessaire, puisqu'on part d'une base vide) :
```sql
CREATE TABLE activation_uses (
    code_hash TEXT NOT NULL PRIMARY KEY,
    used_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    user_id   TEXT
);
```

---

## 6. Mise en parallèle et bascule

- Nouveau dépôt `spotlab-go`, unit systemd propre `spotlab-go.service` (même forme que l'unit existante : `WorkingDirectory`, `EnvironmentFile`, `Restart=on-failure`) sur un **port différent** (ex. `8081`), tournant à côté du `spotlab.service` (Next.js, port 3000) actuel, sans interférence.
- Pendant le développement, cibler le serveur Go directement (`http://<ip-vm>:8081`), en contournant nginx — le plus simple, pas besoin de toucher la config nginx de prod avant la bascule réelle.
- Base de données et dossier de cache audio propres au nouveau serveur dès le départ (base vide actée) — aucun risque de collision d'écriture avec l'ancien serveur pendant que les deux tournent.
- Android pointe déjà sur une adresse de serveur configurable par l'utilisateur (écran « configuration du serveur » existant) — c'est la partie la moins risquée de toute la migration. Utiliser une installation de test (ou la même, basculée) pointée sur `http://<ip-vm>:8081` pour toute vérification manuelle à partir de la phase 6 ; l'installation principale continue de parler au Next.js de prod pendant tout ce temps.
- Bascule réelle, une fois satisfait après la phase 8 : basculer le `proxy_pass` nginx de `127.0.0.1:3000` vers `:8081` — les directives déjà en place pour le SSE (`proxy_buffering off`, `proxy_read_timeout 24h`, en-têtes transférés) s'appliquent sans changement, les réponses SSE de Go ayant la même forme. Garder `spotlab.service` installé mais arrêté quelques semaines après la bascule, comme retour arrière immédiat.

---

## 7. Stratégie de vérification par phase

Principe général : `docs/API.md` (côté serveur actuel) documente déjà chaque forme de réponse attendue par l'app Android — c'est l'oracle d'acceptation de toute la réécriture. Comparer champ par champ ce que renvoie chaque handler Go avant de passer à la phase suivante.

- **Logique pure (réducteur de file, sémantique de splice, scoring)** : tests Go table-driven, zéro I/O, écrits en même temps que le portage — les moins chers et les plus utiles de tout le projet ; c'est le seul endroit où une régression de comportement (ex. un décalage d'index sur le déplacement) ne se verrait sinon que trois semaines plus tard, comme « la file se réordonne mal ».
- **Acteur Hub** : un test qui lance un `Hub`, envoie `ApplyCommand` en concurrence depuis plusieurs goroutines (simulant plusieurs appareils), vérifie que la révision finale correspond au nombre de commandes envoyées et que chaque transition intermédiaire est cohérente, sous `go test -race` — une vraie preuve d'absence de race que l'original JS ne pouvait structurellement pas offrir.
- **Tests curl par endpoint, phases 1–5, 7** : inscription/connexion/renouvellement (vérifier que l'ancien token répond 401 *immédiatement* après renouvellement, sans grâce), recherche/titre/album/artiste contre les formes documentées, idempotence like/unlike, création/ajout/toggle/suppression-par-rowKey de playlist, enregistrement/liste/renommage d'appareil.
- **SSE + sync, phase 6, avant de toucher Android** : deux terminaux — `curl -N -H "Authorization: Bearer $T" ".../api/sync/stream?deviceId=x"` dans l'un, `curl -X POST .../api/sync/command -d '{"deviceId":"x","action":{"type":"TOGGLE_PLAY"}}'` dans l'autre — vérifier que le premier flux reçoit un événement `playback` avec une révision incrémentée. Répéter avec deux comptes amis + `/api/jam` invite/accept pour vérifier que les deux flux convergent sur le même `room` (l'id du jam) avec le bon `jam.members`, et que `stopJam`/`leaveJam` renvoient correctement chaque membre restant vers son propre état solo.
- **Premier jalon complet (après la phase 6)** : pointer un build Android debug sur le serveur Go et parcourir la séquence de démarrage documentée écran par écran — config → login → me → devices/register → connexion SSE → titres aimés.
- **Pipeline audio, phase 8** : vérifier au curl les deux comportements documentés — première requête sur un titre jamais mis en cache (`200`, `Accept-Ranges: none`) puis une deuxième requête une fois le téléchargement terminé (`Range: bytes=1000-2000` → `206` + `Content-Range` correct). Puis, **vérification manuelle dans l'app Android réelle** que le titre n'est pas seekable pendant la mise en cache et le redevient une fois en cache — ce comportement précis est vérifié et attendu côté client, le test le plus important du projet.
- **Activation RSA** : signer un code de test avec une paire de clés jetable, vérifier le succès, vérifier que le rejeu du même code est rejeté (conflit sur `activation_uses`), vérifier qu'un `exp` déjà dépassé est rejeté.

---

## 8. Hors périmètre v1 — explicitement écarté

| Fonctionnalité | Pourquoi c'est sans risque de l'écarter |
|---|---|
| Panneau admin (`/api/admin/**`) | Jamais appelé par l'app Android ; client web abandonné, aucune raison de le reconstruire pour l'instant. Les tâches d'administration (promouvoir un compte, ouvrir/fermer les inscriptions) se font à la main en base ou via un petit outil CLI le temps venu. |
| Import de playlist Deezer (NDJSON) | Jamais appelé par l'app Android ; c'est aussi l'item le plus coûteux à porter (progression NDJSON en streaming) pour zéro consommateur actuel. |
| Téléchargement MP3 / transcodage (`/api/download/{id}`) | Jamais appelé par l'app Android ; seule fonctionnalité qui aurait eu besoin de `ffmpeg`, donc l'écarter retire aussi cette dépendance système du serveur Go pour l'instant. |
| Passkeys / WebAuthn | Cérémonie web-only aujourd'hui, jamais une vraie route API — rien à porter pour une v1 API-only. |
| Écoute hors ligne | Pas implémentée non plus côté client actuellement — rien à porter. |

Simplement omettre ces routes du routeur en v1, plutôt que de les stubber en 501 — pas de bénéfice de diagnosticabilité à cette échelle, et moins de code à maintenir.

---

## Fichiers de référence côté serveur actuel (pour le portage)

- `/home/lucas/spotlab_/src/lib/playback-sync.ts` — moteur de sync/jam à porter (§3)
- `/home/lucas/spotlab_/src/features/Player/queue-reducer.ts` — réducteur de file, vérifié ligne à ligne (§3.2)
- `/home/lucas/spotlab_/src/lib/youtube-audio.ts`, `src/lib/stream.ts` — pipeline audio à porter (§4)
- `/home/lucas/spotlab_/src/lib/ytmusic.ts` — logique de scoring à reprendre (recherche remplacée par yt-dlp natif, §4.2)
- `/home/lucas/spotlab_/prisma/schema.prisma` — référence pour écrire `0001_init.sql` (librement, base vide actée)
- `/home/lucas/spotlab_/docs/API.md` — oracle d'acceptation pour chaque endpoint (§7)
- `/home/lucas/spotlab_/src/app/api/sync/stream/route.ts`, `src/app/api/jam/route.ts` — comportements exacts à reproduire (§3)
