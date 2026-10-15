# Despliegue de la API (VM + Docker Compose)

La API corre en una VM con Docker Compose:

- **Caddy**: HTTPS automático con Let's Encrypt.
- **forge-api**.
- **PostgreSQL**.

La VM tiene que ser accesible desde internet por HTTPS para recibir los webhooks de GitHub.

## Requisitos

- VM Linux con Docker Engine y el plugin `docker compose`.
- Un DNS (por ejemplo `forge.example.com`) apuntando a la IP pública de la VM.
- Puertos 80 y 443 abiertos (Caddy necesita el 80 para el challenge ACME).
- Acceso de salida a Keycloak (JWKS), `api.github.com`, `models.dev` y `www.ecb.europa.eu`.
- El realm de Keycloak configurado ([keycloak.md](keycloak.md)).
- La GitHub App creada e instalada ([github-app.md](github-app.md)).

## Primer despliegue

```sh
git clone git@github.com:hiberus-industria/forge.git /opt/forge
cd /opt/forge/deploy
cp .env.example .env        # rellena dominio, OIDC, GitHub App y contraseña de Postgres
mkdir -p secrets && chmod 700 secrets
cp ~/github-app.pem secrets/github-app.pem && chmod 600 secrets/github-app.pem
docker compose up -d
docker compose logs -f api
```

Las migraciones (goose) se aplican al arrancar la API. Comprueba que todo responde:

```sh
curl -fsS https://forge.example.com/healthz
curl -fsS https://forge.example.com/readyz   # incluye la base de datos
```

Al arrancar, la API carga los precios de models.dev y los tipos del BCE, y lanza la primera reconciliación con GitHub. Las tres tareas se repiten periódicamente.

## Copias de seguridad

Haz un volcado diario de Postgres con rotación de 14 días. Ejemplo de entrada en el crontab del host (`crontab -e`). El nombre del servicio (`postgres`), el usuario y la base de datos deben coincidir con los de `deploy/docker-compose.yml`:

```cron
15 3 * * * cd /opt/forge/deploy && docker compose exec -T postgres pg_dump -U forge -d forge -Fc > /var/backups/forge/forge-$(date +\%F).dump && find /var/backups/forge -name 'forge-*.dump' -mtime +14 -delete
```

Restauración:

```sh
docker compose exec -T postgres pg_restore -U forge -d forge --clean --if-exists < /var/backups/forge/forge-YYYY-MM-DD.dump
```

Copia los volcados fuera de la VM (bucket o NAS corporativo). Los eventos se guardan de forma indefinida, así que la base de datos solo crece.

## Actualizaciones

```sh
cd /opt/forge && git pull
cd deploy && docker compose pull && docker compose up -d
```

- Si usas la imagen publicada `ghcr.io/hiberus-industria/forge-api:<versión>`, fija la versión en `.env` y súbela de forma explícita.
- Las migraciones solo añaden cambios y se aplican al arrancar. Antes de una versión mayor, haz un `pg_dump` manual.
- Las versiones de la CLI salen en paralelo: los desarrolladores actualizan con `forge update` o volviendo a ejecutar `install.sh`.

## Operación

- **Logs**: `docker compose logs api` (JSON vía slog).
- **Recalcular costes tras cambiar precios**: desde la TUI (admin) o con `POST /v1/usage-events/reprice`.
- **Rotar el secreto del webhook**: cámbialo en la GitHub App y en `.env`, y ejecuta `docker compose up -d`.
