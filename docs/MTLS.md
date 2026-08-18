# mTLS : certificat client obligatoire

Toute l'API (`spotlab.ugnbt.com`) est fermée au niveau TLS par nginx
(`ssl_verify_client on`, voir `deploy/nginx-snippet.conf`) : sans certificat
client valide, la connexion échoue avant qu'une seule requête HTTP n'atteigne
le serveur Go. Le serveur applicatif ne voit jamais cette vérification — elle
se fait entièrement en amont.

C'est une autorité distincte de la clé RSA d'activation (`cmd/activate-sign`) :
celle-ci authentifie un *compte*, celle-ci un *appareil*.

## Où vit la CA

`cmd/mtls-ca` est conçu pour tourner hors ligne, sur une machine qui n'est
jamais exposée (voir le commentaire en tête du fichier) — c'est le choix par
défaut recommandé. Ce déploiement s'en écarte délibérément : la clé privée
`mtls-ca.key` vit sur le serveur (`/root/mtls-ca/`, permissions `600`, hors de
`/srv/spotlab-go` et du reste du dépôt), avec un binaire précompilé
(`/root/mtls-ca/mtls-ca`) à côté. Choix assumé pour pouvoir émettre des
certificats à des tiers sans dépendre d'une machine en particulier — au prix
d'un vrai compromis : si ce serveur (ou l'app Go elle-même) est un jour
compromis, l'attaquant peut signer ses propres certificats valides. Aucune
copie ne doit traîner ailleurs (poste de dev, dépôt git) ; `mtls-ca.key` et
`mtls-ca.crt` sont dans `.gitignore` précisément pour ça.

## Régénérer la CA

```
ssh root@<serveur>
cd /root/mtls-ca && ./mtls-ca -genca
```

Écrit `mtls-ca.key`, `mtls-ca.crt` et un registre vide (`issued.json`) dans le
répertoire courant. `mtls-ca.crt` remplace aussi `/etc/nginx/mtls-ca.crt`
(`ssl_client_certificate`) — copier et recharger nginx
(`nginx -t && systemctl reload nginx`) après coup.

Régénérer la CA invalide **tous** les certificats déjà émis : chaque appareil
devra en recevoir un nouveau. Une révocation ciblée (voir plus bas) n'a
jamais besoin de ça.

## Émettre un certificat pour un appareil

```
ssh root@<serveur>
cd /root/mtls-ca && ./mtls-ca -issue -name "nom de l'appareil"
```

Imprime un seul blob base64 (certificat + clé privée du client) à coller dans
l'écran d'enrôlement de l'app, et enregistre le certificat (numéro de série,
nom, date) dans `issued.json` pour pouvoir le retrouver et le révoquer plus
tard. Le blob contient une clé privée en clair : à transmettre par un canal
chiffré, jamais par un canal public — même hypothèse de confiance que les
codes d'activation.

`-days` change la durée de validité (10 ans par défaut).

Pour reconstruire le binaire après une modification de `cmd/mtls-ca` :
```
GOOS=linux GOARCH=amd64 go build -o mtls-ca ./cmd/mtls-ca
scp mtls-ca root@<serveur>:/root/mtls-ca/mtls-ca
```

## Lister les certificats émis

```
ssh root@<serveur>
cd /root/mtls-ca && ./mtls-ca -list
```

Affiche chaque certificat : numéro de série, nom, date d'émission, statut
(actif ou révoqué). Le numéro de série est ce qu'il faut pour `-revoke`.

## Révoquer un appareil

```
ssh root@<serveur>
cd /root/mtls-ca && ./mtls-ca -revoke -serial <numéro-de-série>
cp mtls-ca.crl /etc/nginx/mtls-ca.crl
nginx -t && systemctl reload nginx
```

Marque ce certificat révoqué dans `issued.json` et réécrit la CRL
(`mtls-ca.crl`) que nginx vérifie via `ssl_crl` — seul cet appareil perd
l'accès, tous les autres certificats déjà émis continuent de fonctionner
(vérifié en conditions réelles : un certificat révoqué reçoit `400 The SSL
certificate error` de nginx, un certificat actif continue de recevoir `200`).
Ne pas oublier les deux dernières lignes : `-revoke` ne réécrit que le
fichier local `mtls-ca.crl`, pas la copie que nginx lit.

Pas de renouvellement automatique de la CRL (pas de cron) — elle est valide
10 ans à partir de sa dernière écriture, largement plus que nécessaire tant
qu'une révocation la régénère de temps en temps. `./mtls-ca -refresh-crl`
la réécrit sans rien révoquer, si jamais besoin.
